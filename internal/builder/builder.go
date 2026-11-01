// Package builder turns a validated config.Config into an .msi package: it
// derives every table row, numbers the sequences, packs the cabinet, writes
// the database through msidb and verifies the result by reading it back.
//
// The rules come from docs/spec/MSISPEC.md and the Windows results in
// docs/spec/MSISPEC-RESULTS.md; docs/design/architecture.md describes the
// pipeline.
package builder

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/schema"
	"github.com/jkroepke/msi-builder/internal/sequence"
	"github.com/jkroepke/msi-builder/pkg/config"
	"github.com/jkroepke/msi-builder/pkg/sign"
)

// Options control a build.
type Options struct {
	BuildTime time.Time
	// SignCabinets signs each cabinet written beside the package
	// (package.embedCabinets false) with Authenticode and lists the
	// signing certificate and each cabinet's signature value in the
	// MsiDigitalCertificate and MsiDigitalSignature tables, so that Windows
	// Installer refuses a cabinet that was changed or swapped. The package
	// itself is not signed: sign it with sign.Sign after the build, since
	// the tables protect the cabinets only as far as the package is
	// protected. Nil, or embedded cabinets, which the package signature
	// covers, leave the cabinets unsigned. A timestamp request is bounded
	// by the HTTP client's timeout; the build cannot cancel it. An ECDSA key
	// makes the package differ per build, since its signature values are.
	SignCabinets *sign.Options
	ToolName     string
	// Output is the path the package is written to. The cabinet and the
	// package are written to temporary files beside it and the payload
	// files stream through, so the build needs memory for the tables only,
	// not for the files. The package replaces a file at Output only once it
	// is verified; a failed build leaves nothing behind. When Output is
	// empty the package is built in memory and returned in Result.Package.
	// After a build to a file, Result.Database lists the cabinet by name
	// and size only and cannot be marshalled again.
	Output string
	// CabinetSize caps the size of each cabinet in bytes, counted with
	// every block at the largest size its compression allows (no gain for
	// stored, slightly more than the data for MSZIP, 19 % more for LZX);
	// 0 or less means the 2 GiB a cabinet holds. A file larger than the cap
	// gets a cabinet of its own.
	CabinetSize int64
	CFBVersion  int
	SkipVerify  bool
}

// Result is a finished build.
type Result struct {
	// Package holds the bytes when Options.Output was empty.
	Package []byte
	// Cabinets are the cabinets written beside the package
	// (package.embedCabinets false), in DiskId order. After a build to a
	// file they are in the package's folder (that of a symlink's target
	// when Output is a symlink) and Data is nil.
	Cabinets    []Cabinet
	Database    *msidb.Database
	Diagnostics config.Diagnostics
	ProductCode string
	PackageCode string
	// Files lists every payload file in File.Sequence order.
	Files []File
	// Size is the package length in bytes.
	Size int64
}

// Cabinet is a cabinet written beside the package.
type Cabinet struct {
	// Name is the file name, Media.Cabinet.
	Name string
	// Path is where a build to a file wrote the cabinet.
	Path string
	// Data holds the bytes when Options.Output was empty.
	Data []byte
	// Size is the cabinet length in bytes.
	Size int64
}

// File describes one payload file of a finished build.
type File struct {
	// Key is the File table primary key.
	Key string
	// Name is the long file name as installed.
	Name string
	// Source is the source path as written in the config.
	Source string
	// Component is the ID of the component that owns the file.
	Component string
	// Version is the File.Version value, or "" for an unversioned file.
	Version string
	// Language is the File.Language value, or "" when none is recorded.
	Language string
	// Size is the file size in bytes.
	Size int64
	// Sequence is the File.Sequence value, which orders the files in the
	// cabinet.
	Sequence int
}

// build holds the state of one build.
type build struct { //nolint:govet // grouped by build step
	secure map[string]bool
	// lastScopeAction is the last UI action the scope page added; the
	// next one follows it.
	lastScopeAction string
	// runDLL is set once the custom action DLL of run actions is in the
	// Binary table.
	runDLL bool
	// grants are the permission grants that grantActions turns into
	// custom actions.
	grants []*grantInfo
	// grantSetters are the type-51 actions of the grants, two each, and
	// grantAnchor the action the grant actions follow.
	grantSetters []string
	grantAnchor  string
	// removeFolderActions are the type-51 actions of removeFolders, for
	// the numbers of entries and package directories behind them.
	removeFolderActions                   []string
	removeFolderEntries, removeFolderDirs int
	cfg                                   *config.Config
	numbered                              map[string][]sequence.Numbered
	tables                                map[string]*msidb.Table
	dirs                                  map[string]*dirInfo
	schedules                             map[string][]sequence.Action
	components                            map[string]*componentInfo
	hidden                                map[string]bool
	// carriers maps a component to the generated components that install
	// with it: the one that holds the shortcuts to its files and those that
	// start its services under a condition.
	carriers map[string][]string
	// removedFolders are the folders that generated RemoveFile rows remove
	// on uninstall.
	removedFolders map[string]bool
	// guids maps each ComponentId to the component that has it, to report
	// duplicates (ICE08).
	guids map[any]string
	// shortNames are the short names taken per folder (canonical path),
	// shared by the directories and files in it and by every directory key
	// that names it (section 11.3).
	shortNames map[string]map[string]bool
	// shortcutNames are the short names taken per shortcut directory: a
	// shortcut is written as name.lnk, so it has a set of its own.
	shortcutNames map[string]map[string]bool
	fileByKey     map[string]*fileInfo
	properties    map[string]string
	upgradeCode   string
	productCode   string
	packageCode   string
	files         []*fileInfo
	cabinets      []*cabinetInfo
	actions       []*actionInfo
	dirOrder      []*dirInfo
	diags         config.Diagnostics
	opts          Options
	version       config.Version
	is64          bool
	// language is the LCID this build writes: product.language, or a
	// language of ui.languages in the build of its transform.
	language int
	// fileCache holds the payload files read by the base build, which the
	// builds of the language transforms reuse instead of reading them again.
	fileCache map[*config.File]*fileInfo
	// summaryWarned holds the config paths already reported as made to fit
	// the summary code page.
	summaryWarned map[string]bool
}

type dirInfo struct {
	id        string
	parent    string
	long      string // long name, "" for standard folders and TARGETDIR
	canonical string // canonical path for GUIDs and sorting
	standard  bool
	used      bool
}

type componentInfo struct {
	guid       any
	keyPath    any
	cfg        *config.Component
	dir        *dirInfo
	keyFile    *fileInfo
	index      int
	attributes int
	is64       bool
	// createFolder records that the component has a CreateFolder row.
	createFolder bool
}

// fileInfo is a payload file.
type fileInfo struct {
	Version   any
	Language  any
	Component *componentInfo
	cfg       *config.File
	Hash      *[4]int32
	// open reads the payload again, for the cabinet.
	open     func() (io.ReadCloser, error)
	FileName string
	LongName string
	Key      string
	Attrs    int
	Sequence int
	// Size is the file length; digest its SHA-256.
	Size   int64
	digest [sha256.Size]byte
	isPE   bool
}

type actionInfo struct {
	source any
	target any
	name   string
	// progress is the ActionText description of the action.
	progress string
	typ      int
	// fromUI marks an action that a control runs with DoAction.
	fromUI bool
}

// Build builds the package described by cfg. Validation diagnostics of the
// config are included; the returned error lists every error-level finding.
func Build(cfg *config.Config, opts Options) (*Result, error) {
	diags := config.Validate(cfg)
	if err := diags.Err(); err != nil {
		return &Result{Diagnostics: diags}, err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	// Folder sources are expanded once they are known to be valid, and
	// the config is checked again with their files.
	expanded := cfg.ExpandFolders()
	if err := expanded.Err(); err != nil {
		return &Result{Diagnostics: append(diags, expanded...)}, err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	diags = append(config.Validate(cfg), expanded...)
	if err := diags.Err(); err != nil {
		return &Result{Diagnostics: diags}, err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	if err := checkSigning(cfg, &opts); err != nil {
		return &Result{Diagnostics: diags}, err
	}

	if opts.BuildTime.IsZero() {
		opts.BuildTime = defaultBuildTime()
	}

	opts.BuildTime = opts.BuildTime.UTC().Truncate(time.Second)

	if opts.ToolName == "" {
		opts.ToolName = "msi-builder"
	}

	bld := newBuild(cfg, opts, diags)
	bld.fileCache = make(map[*config.File]*fileInfo)

	res := &Result{}

	err := bld.run(res)
	res.Diagnostics = bld.diags

	if err != nil {
		return res, err
	}

	if err := bld.diags.Err(); err != nil {
		return res, err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	return res, nil
}

// checkSigning checks the cabinet signing options before the first cabinet
// is compressed; embedded cabinets are not signed.
func checkSigning(cfg *config.Config, opts *Options) error {
	if opts.SignCabinets == nil || cfg.Package.CabinetsEmbedded() {
		return nil
	}

	if err := opts.SignCabinets.Check(); err != nil {
		return fmt.Errorf("builder: SignCabinets: %w", err)
	}

	return nil
}

// newBuild returns the state of one build of cfg, in product.language.
func newBuild(cfg *config.Config, opts Options, diags config.Diagnostics) *build {
	return &build{
		cfg:            cfg,
		opts:           opts,
		diags:          diags,
		tables:         make(map[string]*msidb.Table),
		dirs:           make(map[string]*dirInfo),
		components:     make(map[string]*componentInfo),
		fileByKey:      make(map[string]*fileInfo),
		properties:     make(map[string]string),
		secure:         make(map[string]bool),
		hidden:         make(map[string]bool),
		carriers:       make(map[string][]string),
		removedFolders: make(map[string]bool),
		shortNames:     make(map[string]map[string]bool),
		shortcutNames:  make(map[string]map[string]bool),
		schedules:      make(map[string][]sequence.Action),
		numbered:       make(map[string][]sequence.Numbered),
		is64:           cfg.Package.Platform.Is64Bit(),
		language:       *cfg.Product.Language,
	}
}

func (b *build) run(res *Result) error {
	if err := b.writeTables(); err != nil {
		return err
	}

	database, err := b.finish(res)
	if err != nil {
		return err
	}

	res.Database = database
	res.ProductCode = b.productCode
	res.PackageCode = b.packageCode
	res.Files = make([]File, 0, len(b.files))

	for _, file := range b.files {
		res.Files = append(res.Files, File{
			Key:       file.Key,
			Name:      file.LongName,
			Source:    file.cfg.Source,
			Component: file.Component.cfg.ID,
			Version:   stringCell(file.Version),
			Language:  stringCell(file.Language),
			Size:      file.Size,
			Sequence:  file.Sequence,
		})
	}

	return nil
}

// writeTables runs the build steps that fill the tables.
func (b *build) writeTables() error {
	steps := []func() error{
		b.identity,
		b.directories,
		b.writeComponents,
		b.inputs,
		b.reserveShortNames,
		b.sequenceFiles,
		b.shortcuts,
		b.fileTypes,
		b.removeFiles,
		b.features,
		b.writeProperties,
		b.upgrade,
		b.launchConditions,
		b.rememberInstallDir,
		b.searchProperties,
		b.customActions,
		b.firewall,
		b.permissions,
		b.removeFolders,
		b.closeApps,
		b.restartResources,
		b.streams,
		b.customTables,
		b.ui,
		b.sequences,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}

		if err := b.diags.Err(); err != nil {
			return err //nolint:wrapcheck // the diagnostics carry their own paths
		}
	}

	return nil
}

func defaultBuildTime() time.Time {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0).UTC()
		}
	}

	return time.Now().UTC()
}

// table returns the named standard table, creating it empty on first use.
func (b *build) table(name string) *msidb.Table {
	tbl, ok := b.tables[name]
	if !ok {
		tbl = schema.New(name)
		b.tables[name] = tbl
	}

	return tbl
}

func (b *build) hasRows(name string) bool {
	t, ok := b.tables[name]

	return ok && len(t.Rows) > 0
}

func (b *build) errorf(path, format string, args ...any) {
	b.diags = append(b.diags, config.Diagnostic{Severity: config.SeverityError, Path: path, Message: fmt.Sprintf(format, args...)})
}

func (b *build) warnf(path, format string, args ...any) {
	b.diags = append(b.diags, config.Diagnostic{Severity: config.SeverityWarning, Path: path, Message: fmt.Sprintf(format, args...)})
}

// nullable returns nil for "" so that optional strings become NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}

	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// stringCell returns a string cell, or "" for a NULL cell.
func stringCell(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	return ""
}
