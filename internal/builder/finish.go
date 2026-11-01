package builder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/pe"
	"github.com/jkroepke/msi-builder/internal/schema"
	"github.com/jkroepke/msi-builder/internal/spool"
	"github.com/jkroepke/msi-builder/internal/summary"
	"github.com/jkroepke/msi-builder/pkg/config"
	"github.com/jkroepke/msi-builder/pkg/sign"
)

// placeholderCode stands in for an automatic package code while the
// database is marshalled the first time; it has the length of a real GUID so
// that replacing it changes no offsets.
const placeholderCode = "{00000000-0000-0000-0000-000000000000}"

// readInput reads a source path relative to the YAML file, or from cfg.FS.
func (b *build) readInput(src string) ([]byte, error) {
	file, err := openSource(b.cfg, nil, src)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", src, err)
	}

	return data, nil
}

// slashPath cleans a relative source path into slash form. Like the file
// names of a config, it treats a backslash as a separator on every OS.
func slashPath(src string) string {
	return path.Clean(strings.ReplaceAll(src, `\`, "/"))
}

// openSource opens src from fsys, else from cfg.FS, else relative to
// cfg.BaseDir. An absolute path is always opened from the file system.
func openSource(cfg *config.Config, fsys fs.FS, src string) (fs.File, error) {
	if fsys == nil {
		fsys = cfg.FS
	}

	var (
		file fs.File
		err  error
	)

	switch {
	case filepath.IsAbs(src):
		file, err = os.Open(src)
	case fsys != nil:
		file, err = fsys.Open(slashPath(src))
	default:
		src = filepath.Join(cfg.BaseDir, filepath.FromSlash(slashPath(src)))
		file, err = os.Open(src)
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // the *fs.PathError names the path
	}

	return file, nil
}

// streams fills the Icon and Binary tables from the icons and binaries
// sections.
func (b *build) streams() error {
	for _, section := range []struct {
		src   map[string]string
		table string
	}{{table: "Icon", src: b.cfg.Icons}, {table: tableBinary, src: b.cfg.Binaries}} {
		for _, name := range sortedStringKeys(section.src) {
			data, err := b.readInput(section.src[name])
			if err != nil {
				b.errorf(strings.ToLower(section.table)+"s."+name, "%v", err)

				continue
			}

			if len(data) == 0 {
				b.errorf(strings.ToLower(section.table)+"s."+name, "%s is empty", section.src[name])

				continue
			}

			if section.table == tableBinary {
				b.checkEXEBinary(name, data)
			}

			b.table(section.table).Add(name, data)
		}
	}

	return nil
}

// tableBinary is the table of custom action and UI binaries.
const tableBinary = "Binary"

// cfbSignature starts every compound file, and so every .msi.
const cfbSignature = "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"

// checkEXEBinary rejects a binary that an exe action cannot start. The
// engine writes the stream to a temporary .exe and starts it, so a DLL or
// another file would fail only at install time, or be skipped silently
// with return: ignore.
func (b *build) checkEXEBinary(name string, data []byte) {
	for i, custom := range b.cfg.CustomActions {
		if custom.EXE == nil || custom.EXE.Binary != name {
			continue
		}

		cfgPath := fmt.Sprintf("customActions[%d].exe.binary", i)
		reader := bytes.NewReader(data)

		switch {
		case bytes.HasPrefix(data, []byte(cfbSignature)):
			b.errorf(cfgPath, "binary %s is a Windows Installer package, not a program; the engine cannot start it", name)
		case !pe.IsPE(reader):
			b.errorf(cfgPath, "binary %s is not a Windows program (no PE header); the engine cannot start it", name)
		case pe.IsDLL(reader):
			b.errorf(cfgPath, "binary %s is a DLL; use a dll action to call it", name)
		}
	}
}

// customTables copies the tables section into the database. A string in a
// binary column names a file to embed.
func (b *build) customTables() error { //nolint:cyclop,gocognit // one pass over the format; splitting would scatter it
	for i, custom := range b.cfg.Tables {
		tablePath := fmt.Sprintf("tables[%d]", i)

		if _, exists := b.tables[custom.Name]; exists {
			b.errorf(tablePath+".name", "table %s is written by the builder", custom.Name)

			continue
		}

		if custom.Name == "LockPermissions" && b.hasRows(lockTableName) {
			b.errorf(tablePath+".name", "LockPermissions and MsiLockPermissionsEx cannot both exist (ICE104, error 1941); use permissions")

			continue
		}

		tbl := &msidb.Table{Name: custom.Name}

		for _, col := range custom.Columns {
			typ, err := msidb.ParseType(col.Type)
			if err != nil {
				return fmt.Errorf("%s: %w", tablePath, err)
			}

			typ.Key = col.Key
			tbl.Columns = append(tbl.Columns, msidb.Column{Name: col.Name, Type: typ})
		}

		for rowIndex, row := range custom.Rows {
			cells := make([]any, len(row))

			for colIndex, value := range row {
				cells[colIndex] = value

				if tbl.Columns[colIndex].Type.Kind != msidb.KindBinary {
					continue
				}

				if src, ok := value.(string); ok && src != "" {
					data, err := b.readInput(src)
					if err != nil {
						b.errorf(fmt.Sprintf("%s.rows[%d][%d]", tablePath, rowIndex, colIndex), "%v", err)

						continue
					}

					cells[colIndex] = data
				}
			}

			tbl.Rows = append(tbl.Rows, cells)
		}

		if err := tbl.Validate(); err != nil {
			b.errorf(tablePath, "%v", err)

			continue
		}

		b.checkBinaryStreams(tablePath, tbl)
		b.tables[custom.Name] = tbl
	}

	return nil
}

// finish assembles the database, packs the cabinet, derives the package code
// and verifies the package by reading it back.
func (b *build) finish(res *Result) (*msidb.Database, error) {
	database, err := b.assemble()
	if err != nil {
		return nil, err
	}

	if err := b.languageTransforms(database); err != nil {
		return nil, err
	}

	return b.write(res, database)
}

// assemble collects the tables into a database with its _Validation table.
func (b *build) assemble() (*msidb.Database, error) {
	// Written last: custom actions, firewall rules and service recovery add
	// standard folders such as System64Folder after the tree is walked.
	b.writeDirectoryTable()
	b.finishProperties()

	database := &msidb.Database{Codepage: b.cfg.Package.Codepage}

	for _, name := range sortedTableNames(b.tables) {
		tbl := b.tables[name]

		if err := tbl.Validate(); err != nil {
			return nil, fmt.Errorf("table %s: %w", name, err)
		}

		database.Tables = append(database.Tables, tbl)
	}

	b.checkText(database.Tables)
	b.checkStreams(database.Tables)

	if err := b.diags.Err(); err != nil {
		return nil, err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	database.Tables = append(database.Tables, schema.Validation(database.Tables))

	slices.SortFunc(database.Tables, func(a, b *msidb.Table) int { return strings.Compare(a.Name, b.Name) })

	return database, nil
}

// write packs the cabinets, derives the package code, writes the package
// and verifies it by reading it back.
func (b *build) write(res *Result, database *msidb.Database) (*msidb.Database, error) { //nolint:cyclop // one pass over the format; splitting would scatter it
	// A symlink to an existing file at Output is written through: the
	// temporary files go next to its target so that the final rename stays
	// on one file system. A dangling symlink is replaced by the package.
	output := b.opts.Output
	if resolved, err := filepath.EvalSymlinks(output); err == nil {
		output = resolved
	}

	target, closeTarget, err := b.newTarget("package", output)
	if err != nil {
		return nil, err
	}

	defer closeTarget()

	closeCabinets, err := b.writeCabinets(database, output)
	defer closeCabinets()

	if err != nil {
		return nil, err
	}

	auto := b.packageCode == ""
	if auto {
		b.packageCode = placeholderCode
	}

	database.Summary = b.summaryProperties()

	layout, err := database.MarshalTo(target, msidb.Options{CFBVersion: b.opts.CFBVersion})
	if err != nil {
		return nil, fmt.Errorf("marshal database: %w", err)
	}

	if auto {
		if err := b.patchPackageCode(target, layout, database); err != nil {
			return nil, err
		}
	}

	if !b.opts.SkipVerify {
		if err := b.verify(target, layout.Size, database); err != nil {
			return nil, fmt.Errorf("verification of the built package failed: %w", err)
		}
	}

	res.Size = layout.Size

	switch out := target.(type) {
	case *spool.Buffer:
		res.Package = out.Bytes()
		res.Cabinets = b.externalCabinets("")
	case *os.File:
		if err := b.commit(out, output); err != nil {
			return nil, err
		}

		res.Cabinets = b.externalCabinets(filepath.Dir(output))

		// The cabinet spool is removed when finish returns; the returned
		// database keeps the cabinet's name and size only.
		for i := range database.Sources {
			database.Sources[i].Open = nil
		}
	}

	return database, nil
}

// patchPackageCode derives the automatic package code from a digest of the
// package as written with the placeholder and writes the summary stream
// again in place. The real code has the placeholder's length, so the stream
// and the layout do not change.
func (b *build) patchPackageCode(target spool.Target, layout *cfb.Layout, database *msidb.Database) error {
	written, err := database.MarshalSummary()
	if err != nil {
		return fmt.Errorf("marshal database: %w", err)
	}

	hasher := sha256.New()

	if _, err := io.Copy(hasher, io.NewSectionReader(target, 0, layout.Size)); err != nil {
		return fmt.Errorf("digest of the package: %w", err)
	}

	b.packageCode = uuid5(namespacePackage, hex.EncodeToString(hasher.Sum(nil)))
	database.Summary = b.summaryProperties()

	data, err := database.MarshalSummary()
	if err != nil {
		return fmt.Errorf("marshal database: %w", err)
	}

	offset, ok := layout.Offsets[summary.StreamName]
	if !ok {
		return errors.New("marshal database: the summary stream has no offset")
	}

	if len(data) != len(written) {
		return fmt.Errorf("marshal database: the summary stream changed from %d to %d bytes with the package code", len(written), len(data))
	}

	if _, err := target.WriteAt(data, offset); err != nil {
		return fmt.Errorf("write the summary stream: %w", err)
	}

	return nil
}

// newTarget returns where a build product is written: memory when the
// package stays in memory, otherwise a temporary file next to the output
// (the package itself is written to the output path). output is the
// resolved output path; the temporary files are created next to it. The
// returned function
// closes the file, and removes it unless it is the finished package.
func (b *build) newTarget(kind, output string) (spool.Target, func(), error) {
	if b.opts.Output == "" {
		return &spool.Buffer{}, func() {}, nil
	}

	file, err := os.CreateTemp(filepath.Dir(output), ".msi-builder-*."+kind)
	if err != nil {
		return nil, nil, fmt.Errorf("create a temporary %s file: %w", kind, err)
	}

	// After commit the package has moved and there is nothing to
	// remove; a failed build leaves no file behind.
	return file, func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}, nil
}

// prepareOutput sets the mode of the finished file and closes it for the
// move onto dest (a symlink already resolved). An existing file keeps its
// mode.
func prepareOutput(file *os.File, dest string) (spool.Move, error) {
	mode := os.FileMode(0o644)

	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	if err := file.Chmod(mode); err != nil {
		return spool.Move{}, fmt.Errorf("set the mode of %s: %w", dest, err)
	}

	if err := file.Close(); err != nil {
		return spool.Move{}, fmt.Errorf("close %s: %w", file.Name(), err)
	}

	return spool.Move{From: file.Name(), To: dest}, nil
}

func sortedTableNames(m map[string]*msidb.Table) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// summaryProperties builds the summary information (sections 4 and 11.1).
func (b *build) summaryProperties() []summary.Property {
	product := b.cfg.Product
	pkg := b.cfg.Package

	pageCount, _ := config.PageCount(pkg.InstallerVersion)

	subject := product.Description
	if subject == "" {
		subject = product.Name
	}

	comments := product.Comments
	if comments == "" {
		comments = "This installer database contains the logic and data required to install " + product.Name + "."
	}

	words := summary.WordCompressed
	if pkg.LongFileNames != nil && !*pkg.LongFileNames {
		words |= summary.WordShortNames
	}

	props := []summary.Property{
		{ID: summary.PIDCodepage, Value: config.SummaryCodepage(pkg.Codepage)},
		{ID: summary.PIDTitle, Value: pkg.Summary.Title},
		{ID: summary.PIDSubject, Value: subject},
		{ID: summary.PIDAuthor, Value: pkg.Summary.Author},
		{ID: summary.PIDKeywords, Value: pkg.Summary.Keywords},
		{ID: summary.PIDComments, Value: comments},
		{ID: summary.PIDTemplate, Value: b.template()},
		{ID: summary.PIDRevision, Value: b.packageCode},
		{ID: summary.PIDCreated, Value: b.opts.BuildTime},
		{ID: summary.PIDLastSaved, Value: b.opts.BuildTime},
		{ID: summary.PIDPageCount, Value: pageCount},
		{ID: summary.PIDWordCount, Value: words},
		{ID: summary.PIDAppName, Value: b.opts.ToolName},
		{ID: summary.PIDSecurity, Value: 2},
	}

	b.summaryText(props)

	return props
}

// verify reads the finished package back and compares it with what was
// written: every table cell, the summary and the cabinet contents. It reads through
// the file, so a large cabinet is never in memory.
func (b *build) verify(target spool.Target, size int64, want *msidb.Database) error { //nolint:cyclop // one pass over the format; splitting would scatter it
	file, err := cfb.Open(target, size)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}

	got, err := msidb.Decode(file)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}

	if len(got.Problems) > 0 {
		return fmt.Errorf("read-back problems: %s", strings.Join(got.Problems, "; "))
	}

	if len(got.Tables) != len(want.Tables) {
		return fmt.Errorf("read back %d tables, wrote %d", len(got.Tables), len(want.Tables))
	}

	for _, wantTable := range want.Tables {
		gotTable := got.Table(wantTable.Name)
		if gotTable == nil {
			return fmt.Errorf("table %s is missing", wantTable.Name)
		}

		if err := compareTable(wantTable, gotTable); err != nil {
			return fmt.Errorf("read back: %w", err)
		}
	}

	if err := compareStorages(want.Storages, got.Storages); err != nil {
		return fmt.Errorf("read back: %w", err)
	}

	if code, ok := summary.Get(got.Summary, summary.PIDRevision).(string); !ok || code != b.packageCode {
		return fmt.Errorf("summary package code %q, want %q", code, b.packageCode)
	}

	for _, cabinet := range b.cabinets {
		if err := b.verifyCabinet(file, cabinet); err != nil {
			return fmt.Errorf("cabinet %s: %w", cabinet.name, err)
		}
	}

	return nil
}

// compareStorages checks that the package holds the storages written, such
// as the embedded language transforms, each with its CLSID and its streams
// by name, size and content.
func compareStorages(want, got []cfb.Storage) error {
	if len(got) != len(want) {
		return fmt.Errorf("%d storages, wrote %d", len(got), len(want))
	}

	byName := make(map[string]cfb.Storage, len(got))
	for _, storage := range got {
		byName[storage.Name] = storage
	}

	for _, wrote := range want {
		read, ok := byName[wrote.Name]
		if !ok {
			return fmt.Errorf("storage %q is missing", wrote.Name)
		}

		if err := compareStorage(wrote, read); err != nil {
			return err
		}
	}

	return nil
}

// compareStorage checks one storage's CLSID and streams.
func compareStorage(wrote, read cfb.Storage) error {
	if read.CLSID != wrote.CLSID {
		return fmt.Errorf("storage %q has CLSID %x, wrote %x", wrote.Name, read.CLSID, wrote.CLSID)
	}

	streams := make(map[string]cfb.Source, len(read.Streams))
	for _, stream := range read.Streams {
		streams[stream.Name] = stream
	}

	if len(streams) != len(wrote.Streams) {
		return fmt.Errorf("storage %q has %d streams, wrote %d", wrote.Name, len(streams), len(wrote.Streams))
	}

	for _, stream := range wrote.Streams {
		got, ok := streams[stream.Name]
		if !ok || got.Size != stream.Size {
			return fmt.Errorf("storage %q stream %q: read back %d bytes (present %v), wrote %d", wrote.Name, stream.Name, got.Size, ok, stream.Size)
		}

		// Transform streams are small, so they are compared byte by byte;
		// the written ones are in memory (TransformStorage) and reopen.
		if err := sameContent(stream, got); err != nil {
			return fmt.Errorf("storage %q stream %q: %w", wrote.Name, stream.Name, err)
		}
	}

	return nil
}

// sameContent compares the bytes of two stream sources.
func sameContent(wrote, read cfb.Source) error {
	if wrote.Open == nil || read.Open == nil {
		return errors.New("stream has no reader")
	}

	load := func(src cfb.Source) ([]byte, error) {
		reader, err := src.Open()
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}

		defer func() { _ = reader.Close() }()

		return io.ReadAll(reader)
	}

	want, err := load(wrote)
	if err != nil {
		return err
	}

	got, err := load(read)
	if err != nil {
		return err
	}

	if !bytes.Equal(want, got) {
		return errors.New("read back other bytes than written")
	}

	return nil
}

// writeCabinets packs the cabinets, each into a spool of its own next to
// output, and adds the embedded ones to database. The returned function
// closes and removes the spools; a spool committed beside the package is
// already gone from where it would remove it.
func (b *build) writeCabinets(database *msidb.Database, output string) (func(), error) {
	var (
		embedded int64
		closers  []func()
	)

	closeAll := func() {
		for _, closeSpool := range closers {
			closeSpool()
		}
	}

	if err := b.checkCabinetNames(output); err != nil {
		return closeAll, err
	}

	for i, cabinet := range b.cabinets {
		target, closeSpool, err := b.newTarget("cabinet", output)
		if err != nil {
			return closeAll, err
		}

		closers = append(closers, closeSpool)

		if err := b.writeCabinet(target, cabinet); err != nil {
			return closeAll, err
		}

		if b.signsCabinets() {
			if err := b.signCabinet(cabinet, i); err != nil {
				return closeAll, err
			}
		}

		if !b.cfg.Package.CabinetsEmbedded() {
			continue
		}

		embedded += cabinet.size
		database.Sources = append(database.Sources, cabinet.source())

		// Stop at the first cabinet that crosses the limit rather than
		// compress the rest.
		if embedded > maxEmbedded {
			return closeAll, fmt.Errorf("the embedded cabinets add up to more than %d bytes, but a package cannot exceed 2 GB; "+
				"set package.embedCabinets to false to write them beside the package", maxEmbedded)
		}
	}

	return closeAll, nil
}

// checkCabinetNames refuses an external cabinet that would take the place of
// the package itself; Windows compares names without case.
func (b *build) checkCabinetNames(output string) error {
	if b.cfg.Package.CabinetsEmbedded() || b.opts.Output == "" {
		return nil
	}

	for _, cabinet := range b.cabinets {
		if strings.EqualFold(cabinet.name, filepath.Base(output)) || strings.EqualFold(cabinet.name, filepath.Base(b.opts.Output)) {
			return fmt.Errorf("the cabinet %s would replace the package; choose another package.cabinet or output name", cabinet.name)
		}
	}

	return nil
}

// commit moves the external cabinets into the folder of output and the
// verified package onto output, all or none, so that an existing package is
// replaced only by a verified one and never ends up beside cabinets of
// another build.
func (b *build) commit(pkg *os.File, output string) error {
	var moves []spool.Move

	if !b.cfg.Package.CabinetsEmbedded() {
		for _, cabinet := range b.cabinets {
			file, ok := cabinet.target.(*os.File)
			if !ok {
				return fmt.Errorf("cabinet %s has no file", cabinet.name)
			}

			move, err := prepareOutput(file, filepath.Join(filepath.Dir(output), cabinet.name))
			if err != nil {
				return err
			}

			moves = append(moves, move)
		}
	}

	move, err := prepareOutput(pkg, output)
	if err != nil {
		return err
	}

	if err := spool.Commit(append(moves, move)); err != nil {
		return fmt.Errorf("write the package: %w", err)
	}

	return nil
}

// externalCabinets lists the cabinets written beside the package: with their
// bytes for an in-memory build (dir ""), else with their path in dir.
func (b *build) externalCabinets(dir string) []Cabinet {
	if b.cfg.Package.CabinetsEmbedded() {
		return nil
	}

	out := make([]Cabinet, 0, len(b.cabinets))

	for _, cabinet := range b.cabinets {
		entry := Cabinet{Name: cabinet.name, Size: cabinet.size}

		if buf, ok := cabinet.target.(*spool.Buffer); ok && dir == "" {
			entry.Data = buf.Bytes()
		} else {
			entry.Path = filepath.Join(dir, cabinet.name)
		}

		out = append(out, entry)
	}

	return out
}

// verifyCabinet reads cabinet back, from its stream in file or, when it is
// external, from its spool, and compares every entry with its source.
func (b *build) verifyCabinet(file *cfb.File, cabinet *cabinetInfo) error {
	var (
		src  io.ReaderAt = cabinet.target
		size             = cabinet.size
	)

	if b.cfg.Package.CabinetsEmbedded() {
		encoded, err := msidb.EncodeStreamName(cabinet.name, false)
		if err != nil {
			return fmt.Errorf("stream name: %w", err)
		}

		stream, err := file.Reader(encoded)
		if err != nil {
			return fmt.Errorf("the stream is missing: %w", err)
		}

		src, size = stream, stream.Size()
	}

	index := 0

	err := cab.Walk(src, size, func(entry cab.File, data io.Reader) error {
		if index >= len(cabinet.files) {
			return fmt.Errorf("holds more than %d files", len(cabinet.files))
		}

		index++

		return compareEntry(entry, data, cabinet.files[index-1], index-1)
	})
	if err != nil {
		return err //nolint:wrapcheck // the caller names the cabinet
	}

	if index != len(cabinet.files) {
		return fmt.Errorf("holds %d files, want %d", index, len(cabinet.files))
	}

	if b.signsCabinets() {
		sig, err := sign.VerifyCabinet(cabinet.target)
		if err != nil {
			return err //nolint:wrapcheck // the caller names the cabinet
		}

		if !bytes.Equal(sig.Value, cabinet.signature) {
			return errors.New("the signature is not the one MsiDigitalSignature.Hash holds")
		}
	}

	return nil
}

// compareEntry compares the index-th cabinet entry with the file it holds.
func compareEntry(entry cab.File, data io.Reader, want *fileInfo, index int) error {
	if entry.Name != want.Key {
		return fmt.Errorf("entry %d is %q, want %q", index, entry.Name, want.Key)
	}

	hasher := sha256.New()

	n, err := io.Copy(hasher, data)
	if err != nil {
		return fmt.Errorf("entry %s: %w", want.Key, err)
	}

	if n != want.Size || !bytes.Equal(hasher.Sum(nil), want.digest[:]) {
		return fmt.Errorf("entry %s differs from its source", want.Key)
	}

	return nil
}
