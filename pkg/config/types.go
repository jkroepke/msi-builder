// Package config defines msi-builder's YAML input format (schema msi-builder/v1).
//
// The format is specified in docs/design/yaml-schema.md. This package loads a
// file, substitutes ${NAME} variables, applies defaults and validates
// everything that can be checked without reading the referenced input files.
package config

import (
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// SchemaV1 is the only schema identifier this package accepts.
const SchemaV1 = "msi-builder/v1"

// legacySchemaV1 is SchemaV1 under the project's former name go-msi. It is
// rejected with a hint, so that an old file fails with the fix in the error.
const legacySchemaV1 = "go-msi/v1"

// Config is a whole package description.
type Config struct { //nolint:govet // field order is the YAML key order
	Schema           string            `yaml:"schema"`
	Vars             map[string]string `yaml:"vars"`
	Product          Product           `yaml:"product"`
	Package          Package           `yaml:"package"`
	Upgrade          *Upgrade          `yaml:"upgrade"`
	InstallDir       InstallDir        `yaml:"installDir"`
	Directories      []*Directory      `yaml:"directories"`
	Features         []*Feature        `yaml:"features"`
	Components       []*Component      `yaml:"components"`
	Properties       Properties        `yaml:"properties"`
	LaunchConditions []LaunchCondition `yaml:"launchConditions"`
	CustomActions    []*CustomAction   `yaml:"customActions"`
	Icons            map[string]string `yaml:"icons"`
	Binaries         map[string]string `yaml:"binaries"`
	Firewall         []*FirewallRule   `yaml:"firewall"`
	ScheduleReboot   *ScheduleReboot   `yaml:"scheduleReboot"`
	UI               UI                `yaml:"ui"`
	Tables           []*Table          `yaml:"tables"`

	// BaseDir is the directory of the YAML file. Relative source paths
	// resolve against it. It is set by Load, not read from YAML.
	BaseDir string `yaml:"-"`

	// FS, when set, is where every relative input path (files, icons,
	// binaries, binary cells of custom tables, the license and theme
	// pictures) is read from instead of BaseDir.
	// Paths in it use forward slashes. It is for Go callers and is never
	// read from YAML.
	FS fs.FS `yaml:"-"`

	// folders are the components that ExpandFolders replaced.
	folders []folderSource
}

// Product holds the product's identity.
type Product struct {
	Name         string `yaml:"name"`
	Manufacturer string `yaml:"manufacturer"`
	Version      string `yaml:"version"`
	Language     *int   `yaml:"language"`
	UpgradeCode  string `yaml:"upgradeCode"`
	ProductCode  string `yaml:"productCode"`
	PackageCode  string `yaml:"packageCode"`
	Description  string `yaml:"description"`
	Comments     string `yaml:"comments"`
	ARP          ARP    `yaml:"arp"`
}

// ARP holds the Add/Remove Programs properties.
type ARP struct { //nolint:govet // field order is the YAML key order
	Comments        string `yaml:"comments"`
	Contact         string `yaml:"contact"`
	HelpLink        string `yaml:"helpLink"`
	AboutLink       string `yaml:"aboutLink"`
	UpdateLink      string `yaml:"updateLink"`
	Icon            string `yaml:"icon"`
	NoRepair        bool   `yaml:"noRepair"`
	NoModify        bool   `yaml:"noModify"`
	NoRemove        bool   `yaml:"noRemove"`
	Size            int    `yaml:"size"`
	InstallLocation string `yaml:"installLocation"`
}

// Package holds platform and container settings.
type Package struct { //nolint:govet // field order is the YAML key order
	Platform         Platform `yaml:"platform"`
	Scope            Scope    `yaml:"scope"`
	InstallerVersion string   `yaml:"installerVersion"`
	Codepage         int      `yaml:"codepage"`
	Compression      string   `yaml:"compression"`
	Cabinet          string   `yaml:"cabinet"`
	// EmbedCabinets false writes the cabinets beside the package instead
	// of into it; nil means true. It has no default so that the
	// ProductCode of an unchanged configuration stays the same.
	EmbedCabinets *bool   `yaml:"embedCabinets"`
	LongFileNames *bool   `yaml:"longFileNames"`
	Summary       Summary `yaml:"summary"`
}

// Summary overrides Summary Information strings.
type Summary struct {
	Title    string `yaml:"title"`
	Keywords string `yaml:"keywords"`
	Author   string `yaml:"author"`
}

// Upgrade is the major-upgrade policy. ApplyDefaults turns a nil *Upgrade
// into the standard policy; "upgrade: false" sets Disabled, which writes no
// Upgrade rows. MinVersion is the oldest version the major upgrade removes;
// an older one stays installed side by side (UPG-13).
type Upgrade struct { //nolint:govet // field order is the YAML key order
	Disabled            bool             `yaml:"-"`
	AllowSameVersion    *bool            `yaml:"allowSameVersion"`
	MinVersion          string           `yaml:"minVersion"`
	IncludeMin          *bool            `yaml:"includeMin"`
	Downgrade           string           `yaml:"downgrade"`
	DowngradeMessage    string           `yaml:"downgradeMessage"`
	RemoveOld           string           `yaml:"removeOld"`
	MigrateFeatures     *bool            `yaml:"migrateFeatures"`
	IgnoreRemoveFailure bool             `yaml:"ignoreRemoveFailure,omitempty"`
	Property            string           `yaml:"property"`
	NewerProperty       string           `yaml:"newerProperty"`
	Related             []RelatedProduct `yaml:"related"`
}

// RelatedProduct is an extra Upgrade row.
type RelatedProduct struct { //nolint:govet // field order is the YAML key order
	UpgradeCode string `yaml:"upgradeCode"`
	MinVersion  string `yaml:"minVersion"`
	MaxVersion  string `yaml:"maxVersion"`
	IncludeMin  *bool  `yaml:"includeMin"`
	IncludeMax  bool   `yaml:"includeMax"`
	OnlyDetect  bool   `yaml:"onlyDetect"`
	Property    string `yaml:"property"`
}

// InstallDir is the product's main folder, the default directory of every
// component. ApplyDefaults adds it to the directory tree unless directories
// already define its id.
type InstallDir struct {
	// ID is the Directory key and the property that holds the path.
	ID string `yaml:"id"`
	// Name is the folder name below Parent; "/" separates nested folders.
	Name string `yaml:"name"`
	// Parent is the standard folder the install dir is created in.
	Parent string `yaml:"parent"`
	// besides lists, comma separated, the directories of the file's own
	// tree that could be meant as the install dir (configurable, or upper
	// case below Program Files) when the default install dir was added
	// next to them, for the validator to warn about.
	besides string
	// Remember stores the chosen folder in the registry and reads it back
	// on the next install, so that a major upgrade keeps a custom folder
	// (SEARCH-03).
	Remember bool `yaml:"remember"`

	// expanded is set by the first expandShorthands.
	expanded bool
}

// Directory is a node of the install tree.
type Directory struct { //nolint:govet // field order is the YAML key order
	ID           string       `yaml:"id"`
	Name         string       `yaml:"name"`
	Configurable bool         `yaml:"configurable"`
	Children     []*Directory `yaml:"children"`

	// origin is the YAML path of the value a generated node comes from.
	origin string
}

// Feature is a node of the feature tree.
type Feature struct { //nolint:govet // field order is the YAML key order
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Default is on, off or disabled: whether the feature is installed by
	// default. ApplyDefaults turns it into Level 1, 2 or 0.
	Default string `yaml:"default"`
	Level   *int   `yaml:"level"`
	// Conditions replace Level when their condition is true (Condition
	// table rows); see FeatureCondition.FullCondition for the runs they
	// apply to.
	Conditions     []*FeatureCondition `yaml:"conditions"`
	Display        string              `yaml:"display"`
	Directory      string              `yaml:"directory"`
	AllowAbsent    *bool               `yaml:"allowAbsent"`
	AllowAdvertise bool                `yaml:"allowAdvertise"`
	// Components are the feature's components: an id of an entry in
	// components, or a component defined in place. ApplyDefaults moves
	// components defined in place to Config.Components, so afterwards every
	// entry only carries an ID.
	Components []*Component `yaml:"components"`
	Features   []*Feature   `yaml:"features"`
	// Firewall are rules that follow the feature's state. ApplyDefaults
	// moves them to Config.Firewall, with a generated component that
	// carries the state.
	Firewall []*FirewallRule `yaml:"firewall"`

	// defaultedLevel and defaultedValue record the Level that ApplyDefaults
	// filled in, so that a later run can tell it from one set by the caller.
	defaultedLevel *int
	defaultedValue int
}

// baseLevel returns the feature's own level: Level, or the level of
// Default when the defaults have not been applied.
func (f *Feature) baseLevel() int {
	if f.Level != nil {
		return *f.Level
	}

	return defaultLevel(f.Default)
}

// levelIsDefault reports whether Level is still the one ApplyDefaults filled
// in.
func (f *Feature) levelIsDefault() bool {
	return f.Level != nil && f.Level == f.defaultedLevel && *f.Level == f.defaultedValue
}

// FeatureCondition is a level that replaces the feature's level when the
// condition is true. The engine evaluates it in CostFinalize at every run,
// also when ADDLOCAL is set (INST-05); FullCondition limits the runs.
type FeatureCondition struct { //nolint:govet // field order is the YAML key order
	Condition string `yaml:"condition"`
	// Default is on, off or disabled, like Feature.Default; set it or
	// Level.
	Default string `yaml:"default"`
	Level   *int   `yaml:"level"`
}

// FullCondition returns the Condition row's condition for a feature whose
// own level is base. Most rows get NOT Installed, so they decide first
// installs only: a feature that a row disables (level 0) when the product
// is removed keeps its files (INST-05). A row that enables a feature whose
// own level is 0 gets Installed AND (REMOVE OR REINSTALL) instead: without
// it, the feature falls back to level 0 at the uninstall, where the engine
// leaves it and the product installed (INST-06). Restricting the
// maintenance term to removals and repairs keeps ADDLOCAL in a later run
// behind the condition, as at a first install.
func (c *FeatureCondition) FullCondition(base int) string {
	if base == 0 && c.Value() != 0 {
		return "(Installed AND (REMOVE OR REINSTALL)) OR (" + c.Condition + ")"
	}

	return "NOT Installed AND (" + c.Condition + ")"
}

// Value returns the level the condition sets: Level, or the level of
// Default.
func (c *FeatureCondition) Value() int {
	if c.Level != nil {
		return *c.Level
	}

	return defaultLevel(c.Default)
}

// Component is a unit that is installed, repaired and removed together.
type Component struct { //nolint:govet // field order is the YAML key order
	ID        string           `yaml:"id"`
	Directory string           `yaml:"directory"`
	GUID      string           `yaml:"guid"`
	Files     []*File          `yaml:"files"`
	Registry  []*RegistryValue `yaml:"registry"`
	// RemoveRegistry deletes registry keys or values when the component is
	// installed or, for whole keys, removed.
	RemoveRegistry []*RemoveRegistry `yaml:"removeRegistry"`
	// RemoveFiles deletes files or empty folders when the component is
	// installed or removed (RemoveFile rows).
	RemoveFiles []*RemoveFile `yaml:"removeFiles"`
	// RemoveFolders deletes the folder a property names, with every file
	// and subfolder in it, when the component is installed or removed, but
	// not on the uninstall half of a major upgrade (custom action DLL).
	RemoveFolders []*RemoveFolder `yaml:"removeFolders"`
	Services      []*Service      `yaml:"services"`
	// ServiceControl stops, starts or deletes services that the package
	// does not install, when the component is installed or removed.
	ServiceControl []*ControlledService `yaml:"serviceControl"`
	EventLog       []*EventSource       `yaml:"eventLog"`
	Environment    []*Environment       `yaml:"environment"`
	// Purge deletes the component's directory with everything in it when
	// the product is uninstalled, but not on a major upgrade (FILE-03).
	Purge        *Purge `yaml:"purge"`
	CreateFolder bool   `yaml:"createFolder"`
	Condition    string `yaml:"condition"`
	// Scope, in a dual package only, installs the component only in that
	// context, perMachine or perUser; empty installs it in both.
	Scope          Scope `yaml:"scope"`
	Permanent      bool  `yaml:"permanent"`
	NeverOverwrite bool  `yaml:"neverOverwrite"`
	// Preserve keeps the component's key file or registry value as the user
	// left it across major upgrades: it sets NeverOverwrite, and removeOld
	// defaults to afterInstallExecute (UPG-05).
	Preserve          bool         `yaml:"preserve"`
	SharedDLLRefCount bool         `yaml:"sharedDllRefCount"`
	Transitive        bool         `yaml:"transitive"`
	KeyPath           string       `yaml:"keyPath"`
	Bitness           string       `yaml:"bitness"`
	FolderPermissions *Permissions `yaml:"folderPermissions"`
	// Shortcuts are shortcuts that this component installs and that do not
	// point at a file they are listed under: each names a file, a folder,
	// a program or a URL.
	Shortcuts []*Shortcut `yaml:"shortcuts"`

	// ref marks a features[].components entry written as an id.
	ref bool
	// defined marks a component decoded from a YAML mapping.
	defined bool
	// origin is the YAML path of a component defined inside a feature.
	origin string
	// firewallFeature is the feature whose firewall rules a generated
	// component carries.
	firewallFeature string
	// carrierOf is that feature.
	carrierOf *Feature
	// folder is the component with a folder source that this one was
	// generated from.
	folder *Component
}

// FirewallFeature returns the feature whose firewall rules the component
// carries, for a component generated from features[].firewall, else "".
func (c *Component) FirewallFeature() string {
	return c.firewallFeature
}

// Path returns the component's YAML path, given its index in
// Config.Components: components[i], or its place in a feature for a
// component defined there.
func (c *Component) Path(index int) string {
	if c.origin != "" {
		return c.origin
	}

	return "components[" + strconv.Itoa(index) + "]"
}

// Purge removes a directory tree on uninstall. "purge: true" is the short
// form of an empty mapping; "purge: false" sets Disabled.
type Purge struct {
	// Condition is ANDed with the uninstall condition, e.g. DELETE_DATA="1".
	Condition string `yaml:"condition"`
	Disabled  bool   `yaml:"-"`
}

// Enabled reports whether p asks for a purge.
func (p *Purge) Enabled() bool {
	return p != nil && !p.Disabled
}

// Permissions secures an installed object with an SDDL string through
// MsiLockPermissionsEx (specification section 6.2), which replaces the
// DACL, and grants rights that are added to the DACL the object has, by the
// custom action DLL (specification section 17.7). Condition applies to both.
type Permissions struct { //nolint:govet // field order is the YAML key order
	SDDL      string   `yaml:"sddl"`
	Grant     []*Grant `yaml:"grant"`
	Condition string   `yaml:"condition"`
}

// Grant adds an access-allowed ACE for an account to the DACL of a file,
// folder or service and keeps the ACEs that are already there.
type Grant struct { //nolint:govet // field order is the YAML key order
	// Account is formatted at install time: a [Property], a SID string, an
	// SDDL alias such as NS, the service manager's names LocalSystem,
	// NT AUTHORITY\LocalService and NT AUTHORITY\NetworkService, .\name
	// for a local account, or another account name.
	Account string `yaml:"account"`
	// Rights are rights names or hexadecimal access masks
	// (docs/design/yaml-schema.md, "Grants").
	Rights StringList `yaml:"rights"`
	// Inherit, for a folder only, makes the ACE apply to subfolders and
	// files (OICI); it defaults to true.
	Inherit *bool `yaml:"inherit"`
}

// File is a payload file.
type File struct { //nolint:govet // field order is the YAML key order
	Source                     string       `yaml:"source"`
	Name                       string       `yaml:"name"`
	ID                         string       `yaml:"id"`
	Version                    string       `yaml:"version"`
	Language                   string       `yaml:"language"`
	AllowUnversionedExecutable bool         `yaml:"allowUnversionedExecutable"`
	Vital                      *bool        `yaml:"vital"`
	Checksum                   bool         `yaml:"checksum"`
	ReadOnly                   bool         `yaml:"readOnly"`
	Hidden                     bool         `yaml:"hidden"`
	System                     bool         `yaml:"system"`
	Permissions                *Permissions `yaml:"permissions"`
	Shortcuts                  []*Shortcut  `yaml:"shortcuts"`
	FileTypes                  []*FileType  `yaml:"fileTypes"`
	// Close asks running copies of the program to close before it is
	// replaced or removed, and ends those that do not.
	Close *Close `yaml:"close"`
	// Include and Exclude are glob patterns over the paths below a folder
	// source (one that ends in a slash); see Config.ExpandFolders.
	Include StringList `yaml:"include"`
	Exclude StringList `yaml:"exclude"`
	// GUIDs gives files of a folder source a fixed component GUID: the key
	// is the slash path of the file below the folder, matched ignoring
	// case, the value its GUID. A port keeps the codes of the components
	// another tool gave each file this way; the other files keep generated
	// ones.
	GUIDs map[string]string `yaml:"guids"`

	// FS, when set, is where Source is read from, overriding Config.FS for
	// this file. AddTree sets it. It is never read from YAML.
	FS fs.FS `yaml:"-"`
}

// Close stops the running copies of an installed program when its
// component is installed, repaired or removed (specification section 17.9):
// windows on the installing user's desktop get WM_CLOSE before
// InstallValidate, and with Terminate the processes still running from the
// installed path are ended after StopServices. "close: true" is the short
// form of an empty mapping; "close: false" sets Disabled.
type Close struct {
	// Timeout is how long the programs get to close; nil means
	// DefaultCloseTimeout.
	Timeout *Duration `yaml:"timeout"`
	// Terminate ends the processes that are still running; nil means true.
	Terminate *bool `yaml:"terminate"`
	Disabled  bool  `yaml:"-"`
}

// DefaultCloseTimeout is how long running programs get to close when
// close.timeout is not set.
const DefaultCloseTimeout = 5 * time.Second

// MaxCloseTimeout is the longest close.timeout: the install waits up to that
// long for each such file before InstallValidate.
const MaxCloseTimeout = 5 * time.Minute

// Enabled reports whether c asks for the program to be closed.
func (c *Close) Enabled() bool {
	return c != nil && !c.Disabled
}

// TimeoutOrDefault returns Timeout, or DefaultCloseTimeout when it is unset.
func (c *Close) TimeoutOrDefault() time.Duration {
	if c.Timeout == nil {
		return DefaultCloseTimeout
	}

	return time.Duration(*c.Timeout)
}

// Terminates reports whether the processes that do not close are ended.
func (c *Close) Terminates() bool {
	return c.Terminate == nil || *c.Terminate
}

// FileType is a ProgId that the file it is listed under opens, with the
// extensions that point at it and its shell verbs. The builder writes them
// as Registry rows under HKEY_CLASSES_ROOT, in the file's component, the
// way non-advertised ProgId, Extension and Verb elements are written.
type FileType struct {
	// ProgID is the programmatic identifier, such as Acme.Document.1.
	ProgID string `yaml:"progId"`
	// Description is the type's name in Explorer, written as it is:
	// [PROPERTY] references are not formatted.
	Description string `yaml:"description"`
	// IconIndex picks an icon resource of the file for DefaultIcon; nil
	// writes no DefaultIcon.
	IconIndex  *int                 `yaml:"iconIndex"`
	Extensions []*FileTypeExtension `yaml:"extensions"`
	// Verbs are the shell verbs. nil gets one open verb; an empty,
	// non-nil list writes none.
	Verbs []*FileTypeVerb `yaml:"verbs"`
}

// FileTypeExtension is a file name extension that opens with a FileType.
type FileTypeExtension struct {
	// Extension is the extension without its dot, such as acme.
	Extension   string `yaml:"extension"`
	ContentType string `yaml:"contentType"`
	// OpenWithOnly lists the ProgID in the extension's Open with menu
	// (OpenWithProgids) and leaves the extension's default program and
	// content type alone.
	OpenWithOnly bool `yaml:"openWithOnly,omitempty"`
}

// FileTypeVerb is a shell verb of a FileType that runs the file.
type FileTypeVerb struct { //nolint:govet // field order is the YAML key order
	// ID is the verb's key, such as open or edit.
	ID string `yaml:"id"`
	// Text is the menu text; empty shows the shell's text for the id.
	Text string `yaml:"text"`
	// Arguments follow the quoted file path in the command, default "%1";
	// an empty string runs the file without arguments. They are formatted
	// text, so [INSTALLDIR] works; description, text and contentType are
	// written literally.
	Arguments *string `yaml:"arguments"`
}

// Shortcut is a non-advertised shortcut in the Start menu, on the desktop
// or in any other folder. Under a file it points at that file; under a
// component it points at exactly one of File, Folder, Program or URL.
type Shortcut struct { //nolint:govet // field order is the YAML key order
	ID string `yaml:"id"`
	// Name is the shortcut's name without ".lnk" or ".url", default
	// product.name.
	Name string `yaml:"name"`
	// File is the id or name of a packaged file, in any component.
	File string `yaml:"file"`
	// Folder is a directory ID, a standard folder or a folder path.
	Folder string `yaml:"folder"`
	// Program is the formatted path of a program outside the package,
	// starting with a folder property such as [SystemFolder].
	Program string `yaml:"program"`
	// URL makes an internet shortcut, a .url file, instead of a .lnk.
	URL string `yaml:"url"`
	// Directory is a directory ID, a standard folder or a folder path,
	// default ProgramMenuFolder.
	Directory   string `yaml:"directory"`
	Arguments   string `yaml:"arguments"`
	Description string `yaml:"description"`
	// WorkingDirectory is a directory ID, default the file's directory.
	WorkingDirectory string `yaml:"workingDirectory"`
	// Icon names an entry of Config.Icons; empty shows the target's icon.
	Icon string `yaml:"icon"`
	// IconFile is the id or name of a packaged file whose icon a url
	// shortcut shows; .lnk shortcuts take Icon.
	IconFile  string `yaml:"iconFile"`
	IconIndex int    `yaml:"iconIndex"`
	// Show is normal (default), maximized or minimized.
	Show string `yaml:"show"`
	// Properties are shell properties of the .lnk, by canonical name
	// (System.AppUserModel.ID); the values are formatted text.
	Properties map[string]string `yaml:"properties"`
}

// RegistryValue is a typed registry value or key.
type RegistryValue struct { //nolint:govet // field order is the YAML key order
	ID                string       `yaml:"id"`
	Root              string       `yaml:"root"`
	Key               string       `yaml:"key"`
	Name              string       `yaml:"name"`
	Type              string       `yaml:"type"`
	Value             StringList   `yaml:"value"`
	Append            bool         `yaml:"append"`
	Prepend           bool         `yaml:"prepend"`
	RemoveOnUninstall bool         `yaml:"removeOnUninstall"`
	Permissions       *Permissions `yaml:"permissions"`
}

// RemoveRegistry is a registry key or value that is deleted when its
// component is installed, before the component's own values are written,
// or a key that is deleted when the component is removed.
type RemoveRegistry struct {
	ID   string `yaml:"id"`
	Root string `yaml:"root"`
	Key  string `yaml:"key"`
	// Name is the value to delete; empty deletes the key with all its values
	// and subkeys.
	Name string `yaml:"name"`
	// On is install (empty means install), uninstall or both. Uninstall
	// and both delete whole keys only.
	On string `yaml:"on"`
}

// RemoveFile is one RemoveFile row: the files of one folder that match a
// pattern, or the folder itself when it is empty.
type RemoveFile struct {
	ID string `yaml:"id"`
	// Name is a filename that may hold * and ?; empty removes the folder
	// itself, if it is empty.
	Name string `yaml:"name"`
	// Directory is a directory ID, default the component's.
	Directory string `yaml:"directory"`
	// On is install, uninstall (default) or both.
	On string `yaml:"on"`
}

// RemoveFolder removes the folder that a property or directory holds at
// install time, with everything in it: the custom action DLL moves it aside
// and deletes it at commit without following junctions (specification
// section 17.10).
type RemoveFolder struct {
	// Property holds the full path of the folder: a directory ID or a
	// property, for example one a registry search fills.
	Property string `yaml:"property"`
	// On is install, uninstall (default) or both.
	On string `yaml:"on"`
	// Condition is ANDed with the condition that decides whether the folder is
	// listed; the component's install or remove state still applies.
	Condition string `yaml:"condition"`
}

// Values of RemoveFile.On, RemoveFolder.On and RemoveRegistry.On.
const (
	RemoveOnInstall   = "install"
	RemoveOnUninstall = "uninstall"
	RemoveOnBoth      = "both"
)

// Service is a Windows service installed from the component's key file.
type Service struct { //nolint:govet // field order is the YAML key order
	Name                             string           `yaml:"name"`
	DisplayName                      string           `yaml:"displayName"`
	Description                      string           `yaml:"description"`
	Type                             string           `yaml:"type"`
	Start                            string           `yaml:"start"`
	DelayedAutoStart                 bool             `yaml:"delayedAutoStart"`
	ErrorControl                     string           `yaml:"errorControl"`
	Vital                            bool             `yaml:"vital"`
	Account                          string           `yaml:"account"`
	Password                         string           `yaml:"password"`
	Arguments                        Arguments        `yaml:"arguments"`
	Dependencies                     []string         `yaml:"dependencies"`
	FailureActionsOnNonCrashFailures bool             `yaml:"failureActionsOnNonCrashFailures"`
	Control                          ServiceControl   `yaml:"control"`
	Recovery                         *ServiceRecovery `yaml:"recovery"`
	Permissions                      *Permissions     `yaml:"permissions"`
}

// Arguments is a service command line. Each entry is a piece of it; a piece
// with a condition is only added when the condition is true at install time.
type Arguments []Argument

// Argument is one piece of a service command line.
type Argument struct {
	Value     string `yaml:"value"`
	Condition string `yaml:"condition"`
}

// String joins the pieces that have no condition.
func (a Arguments) String() string {
	parts := make([]string, 0, len(a))

	for _, arg := range a {
		if arg.Condition == "" && arg.Value != "" {
			parts = append(parts, arg.Value)
		}
	}

	return strings.Join(parts, " ")
}

// ServiceControl selects the ServiceControl events.
type ServiceControl struct { //nolint:govet // field order is the YAML key order
	StartOnInstall *bool `yaml:"startOnInstall"`
	StopOnInstall  *bool `yaml:"stopOnInstall"`
	// DeleteOnInstall deletes a service of the same name that is already
	// registered before InstallServices creates this one (SVC-06). Nil means
	// false: it stays unset after defaults so the derived product code of
	// configurations that do not use it does not change.
	DeleteOnInstall   *bool `yaml:"deleteOnInstall"`
	StopOnUninstall   *bool `yaml:"stopOnUninstall"`
	DeleteOnUninstall *bool `yaml:"deleteOnUninstall"`
	// StartCondition starts the service at install only when the condition
	// is true at install time (SVC-08).
	StartCondition string `yaml:"startCondition"`
	// Wait waits for the stop and delete events to complete.
	Wait *bool `yaml:"wait"`
	// WaitForStart waits for the start to complete, so a service that does
	// not reach SERVICE_RUNNING fails the install. Nil means Wait: it stays
	// unset after defaults so the derived product code of configurations
	// that do not use it does not change.
	WaitForStart *bool `yaml:"waitForStart"`
}

// ControlledService is a service that the package does not install, stopped,
// started or deleted when its component is installed or removed (SVC-07).
// Each event is off unless set.
type ControlledService struct { //nolint:govet // field order is the YAML key order
	Name              string `yaml:"name"`
	StopOnInstall     bool   `yaml:"stopOnInstall,omitempty"`
	StartOnInstall    bool   `yaml:"startOnInstall,omitempty"`
	DeleteOnInstall   bool   `yaml:"deleteOnInstall,omitempty"`
	StopOnUninstall   bool   `yaml:"stopOnUninstall,omitempty"`
	StartOnUninstall  bool   `yaml:"startOnUninstall,omitempty"`
	DeleteOnUninstall bool   `yaml:"deleteOnUninstall,omitempty"`
	// Wait waits for each event to complete; nil means true.
	Wait *bool `yaml:"wait"`
}

// anyEvent reports whether the entry selects at least one event.
func (c *ControlledService) anyEvent() bool {
	return c.StopOnInstall || c.StartOnInstall || c.DeleteOnInstall ||
		c.StopOnUninstall || c.StartOnUninstall || c.DeleteOnUninstall
}

// ServiceRecovery is applied with sc.exe after InstallServices.
type ServiceRecovery struct { //nolint:govet // field order is the YAML key order
	ResetAfterDays int              `yaml:"resetAfterDays"`
	ResetAfter     *Duration        `yaml:"resetAfter"`
	Actions        []RecoveryAction `yaml:"actions"`
	RestartDelay   *Duration        `yaml:"restartDelay"`
}

// RecoveryAction is one failure step: "restart", "none", or
// {restart: 5m} for a restart with its own delay.
type RecoveryAction struct {
	Restart *Duration `yaml:"restart"`
	Action  string    `yaml:"-"`
}

// EventSource is an event log source registered with plain Registry rows.
type EventSource struct { //nolint:govet // field order is the YAML key order
	Source         string `yaml:"source"`
	Log            string `yaml:"log"`
	MessageFile    string `yaml:"messageFile"`
	TypesSupported *int   `yaml:"typesSupported"`
}

// Environment is an environment variable change.
type Environment struct { //nolint:govet // field order is the YAML key order
	Name              string `yaml:"name"`
	Value             string `yaml:"value"`
	Action            string `yaml:"action"`
	Scope             string `yaml:"scope"`
	RemoveOnUninstall *bool  `yaml:"removeOnUninstall"`
}

// LaunchCondition must be true for the install to start.
type LaunchCondition struct {
	Condition string `yaml:"condition"`
	Message   string `yaml:"message"`
}

// CustomAction is one custom action and where it runs.
type CustomAction struct { //nolint:govet // field order is the YAML key order
	ID           string        `yaml:"id"`
	SetProperty  *SetProperty  `yaml:"setProperty"`
	SetDirectory *SetDirectory `yaml:"setDirectory"`
	Error        string        `yaml:"error"`
	Exec         *Exec         `yaml:"exec"`
	ExecProperty *ExecProperty `yaml:"execProperty"`
	DLL          *DLL          `yaml:"dll"`
	EXE          *EXE          `yaml:"exe"`
	Run          *Run          `yaml:"run"`
	Execute      string        `yaml:"execute"`
	Impersonate  *bool         `yaml:"impersonate"`
	// Scope, in a dual package only, runs the action only in that context:
	// perMachine ANDs ALLUSERS=1 into each schedule's condition, perUser
	// NOT ALLUSERS. Only a perMachine action may run without impersonation.
	Scope      Scope  `yaml:"scope"`
	Return     string `yaml:"return"`
	HideTarget bool   `yaml:"hideTarget"`
	Once       string `yaml:"once"`
	// Progress is the action's description in the ActionText table, shown
	// by the wizard's progress page while a deferred action runs and
	// written to the verbose log (under /qn only for deferred actions).
	Progress string      `yaml:"progress"`
	Schedule []Placement `yaml:"schedule"`

	// origin is the YAML path of the service argument an action was
	// generated from.
	origin string
}

// SetProperty is a type 51 action.
type SetProperty struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// SetDirectory is a type 35 action.
type SetDirectory struct {
	Directory string `yaml:"directory"`
	Value     string `yaml:"value"`
}

// Exec is a type 34 action: a command line run in a directory.
type Exec struct {
	Command   string `yaml:"command"`
	Directory string `yaml:"directory"`
}

// ExecProperty is a type 50 action: the executable path is in a property.
type ExecProperty struct {
	Property  string `yaml:"property"`
	Arguments string `yaml:"arguments"`
}

// Run is a type 1 action on the custom action DLL msi-builder embeds: the
// command runs with a hidden window and its output goes to the log.
type Run struct {
	Command   string `yaml:"command"`
	Directory string `yaml:"directory"`
}

// DLL is a type 1 action from the Binary table.
type DLL struct {
	Binary string `yaml:"binary"`
	Entry  string `yaml:"entry"`
}

// EXE is a type 2 action: the engine writes the program from the Binary
// table to a temporary file, runs it with the arguments and deletes it.
type EXE struct {
	Binary    string `yaml:"binary"`
	Arguments string `yaml:"arguments"`
}

// Placement puts an action into one sequence table.
type Placement struct { //nolint:govet // field order is the YAML key order
	Sequence  string `yaml:"sequence"`
	Before    string `yaml:"before"`
	After     string `yaml:"after"`
	At        int    `yaml:"at"`
	Condition string `yaml:"condition"`
	// When is install, upgrade, uninstall or maintenance: a condition that
	// is ANDed with Condition (see Placement.FullCondition).
	When string `yaml:"when"`
}

// registryTypeKey is the registry type of an entry that creates a key
// without a value.
const registryTypeKey = "key"

// ScheduleReboot asks for a restart at the end of the installation: the
// ScheduleReboot action after InstallFinalize. With /norestart or
// REBOOT=ReallySuppress msiexec returns 3010 instead of restarting; a quiet
// install without them may restart without asking.
type ScheduleReboot struct {
	// When is install, upgrade, uninstall or maintenance, as in a schedule.
	When      string `yaml:"when"`
	Condition string `yaml:"condition"`
}

// Placement returns the reboot's placement in the execute sequence.
func (r *ScheduleReboot) Placement() *Placement {
	return &Placement{Sequence: "execute", After: "InstallFinalize", Condition: r.Condition, When: r.When}
}

// FirewallRule is an inbound rule that follows a component's state.
type FirewallRule struct { //nolint:govet // field order is the YAML key order
	Name            string     `yaml:"name"`
	Description     string     `yaml:"description"`
	Component       string     `yaml:"component"`
	Program         string     `yaml:"program"`
	Protocol        string     `yaml:"protocol"`
	Port            string     `yaml:"port"`
	RemoteAddresses string     `yaml:"remoteAddresses"`
	Profiles        StringList `yaml:"profiles"`

	// origin is the YAML path of a rule written under a feature.
	origin string
	// ownComponent records that a rule under a feature named a component.
	ownComponent bool
	// carrier is the generated component of a rule under a feature.
	carrier *Component
}

// Path returns the YAML path of the rule at index in Config.Firewall.
func (r *FirewallRule) Path(index int) string {
	if r.origin != "" {
		return r.origin
	}

	return "firewall[" + strconv.Itoa(index) + "]"
}

// UI selects the built-in wizard and adds dialogs to it.
type UI struct {
	// License is a license file shown on a page with an accept check box
	// after the welcome page: RTF, or plain text that is converted to RTF.
	License string `yaml:"license"`
	// Launch adds a check box to the finish page that starts an installed
	// program when the user clicks Finish.
	Launch *Launch `yaml:"launch"`
	// Strings replaces built-in wizard texts by key, for example to build
	// the wizard in another language (UIStrings lists the keys).
	Strings map[string]string `yaml:"strings"`
	// Languages adds wizard languages by LCID or language tag, each built
	// as an embedded language transform that msiexec picks by the user's
	// language (spec section 7.6).
	Languages map[string]*Language `yaml:"languages"`
	// FinishText is a formatted paragraph shown on the finish page below
	// its standard text; hidden by Installed OR ACTION = "ADMIN".
	FinishText string    `yaml:"finishText"`
	Theme      Theme     `yaml:"theme"`
	Dialogs    []*Dialog `yaml:"dialogs"`
	// Enabled shows the setup wizard: welcome, ready, progress and finish
	// pages plus maintenance, cancel and error dialogs. Without it msiexec
	// shows only its basic progress window.
	Enabled bool `yaml:"enabled,omitempty"`
	// ChooseFeatures adds the feature tree page after the welcome and
	// license pages, with Browse for a feature's directory.
	ChooseFeatures bool `yaml:"chooseFeatures,omitempty"`
	// ChooseDirectory adds a page after the welcome and license pages that
	// shows the install folder and lets the user change it.
	ChooseDirectory bool `yaml:"chooseDirectory,omitempty"`
}

// Launch is the finish page's check box that starts an installed program.
type Launch struct {
	// Checked sets the box's initial state; the default is checked.
	Checked *bool `yaml:"checked"`
	// File is the id of an installed file: files[].id, or its name when
	// it has no id.
	File      string `yaml:"file"`
	Arguments string `yaml:"arguments"`
	// Text is the check box label, "&" marks the access key; the default is
	// "&Launch [ProductName]".
	Text string `yaml:"text"`
}

// Theme replaces the wizard's pictures or the colours they are drawn in.
type Theme struct {
	// Banner is the picture at the top of the inner pages, shown at
	// 370×44 dialog units (493×58 pixels at 96 dpi).
	Banner string `yaml:"banner"`
	// Dialog is the background of the welcome and finish pages, shown at
	// 370×234 dialog units (493×312 pixels at 96 dpi). The page text is
	// drawn on its right part, from 135 units on.
	Dialog string      `yaml:"dialog"`
	Colors ThemeColors `yaml:"colors"`
}

// ThemeColors are the colours of the built-in pictures as #RRGGBB.
type ThemeColors struct {
	// Dark and Light are the two ends of the side panel gradient.
	Dark  string `yaml:"dark"`
	Light string `yaml:"light"`
	// Accent draws the banner stripe and the shapes on the side panel.
	Accent string `yaml:"accent"`
	// Paper is the background behind the page titles and texts.
	Paper string `yaml:"paper"`
}

// Dialog is a form dialog laid out by the builder.
type Dialog struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// After names the page the dialog follows in its flow; a remove
	// dialog follows MaintenanceTypeDlg by default.
	After string `yaml:"after"`
	// Flow is install (default), the pages of a new installation, or
	// remove, the pages between Remove in the maintenance menu and the
	// page that confirms the removal.
	Flow   string        `yaml:"flow"`
	Fields []DialogField `yaml:"fields"`
}

// Dialog flows.
const (
	FlowInstall = "install"
	FlowRemove  = "remove"
)

// Removal reports whether the dialog is in the remove flow.
func (d *Dialog) Removal() bool {
	return d.Flow == FlowRemove
}

// DialogField is one field bound to a property: a label and an edit field,
// a check box, or a label and a group of radio buttons.
type DialogField struct {
	Property string `yaml:"property"`
	Label    string `yaml:"label"`
	// Type is text (default), password, checkbox or choice.
	Type string `yaml:"type"`
	// Value is what a checked checkbox sets the property to, default 1.
	Value string `yaml:"value"`
	// Options are the values a choice field offers, in the order shown.
	Options ChoiceOptions `yaml:"options"`
}

// ChoiceOption is one radio button of a choice field. "info" is the short
// form of {value: info}.
type ChoiceOption struct {
	Value string `yaml:"value"`
	// Label is the button's text, default Value; "&" marks the access key.
	Label string `yaml:"label"`
}

// Dialog field types.
const (
	FieldText     = "text"
	FieldPassword = "password"
	FieldCheckBox = "checkbox"
	FieldChoice   = "choice"
)

// CheckBoxValue returns the value a checked checkbox field sets.
func (f *DialogField) CheckBoxValue() string {
	if f.Value == "" {
		return "1"
	}

	return f.Value
}

// Table is a custom table written as given.
type Table struct {
	Name    string   `yaml:"name"`
	Columns []Column `yaml:"columns"`
	Rows    [][]any  `yaml:"rows"`
}

// Column is a custom table column in .idt type notation (s72, I2, L0, V0, ...).
type Column struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Key  bool   `yaml:"key"`
}

// CabinetsEmbedded reports whether the cabinets go into the package, which
// they do unless EmbedCabinets is false.
func (p *Package) CabinetsEmbedded() bool {
	return p.EmbedCabinets == nil || *p.EmbedCabinets
}
