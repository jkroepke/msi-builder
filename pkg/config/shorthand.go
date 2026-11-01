package config

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Defaults of the shorthands in docs/design/yaml-schema.md section 3.3.
const (
	DefaultInstallDirID = "INSTALLDIR"
	DefaultFeatureID    = "MainFeature"
	// DefaultShortcutDirectory is the Start menu's Programs folder.
	DefaultShortcutDirectory = "ProgramMenuFolder"

	// KeyPathDirectory is the keyPath of a component keyed on its directory.
	KeyPathDirectory = "directory"

	// productNamePath is the YAML path of an install dir name taken from
	// the product name.
	productNamePath = "product.name"

	maxGeneratedIDLength = 60

	// FeatureOn, FeatureOff and FeatureDisabled are the values of
	// features[].default.
	FeatureOn       = "on"
	FeatureOff      = "off"
	FeatureDisabled = "disabled"

	// featureLevelOff is the level of a feature that is off by default.
	featureLevelOff  = 2
	hashSuffixLength = 9 // "_" and 8 hex digits
)

// expandShorthands rewrites the short forms of a file into the explicit
// model that the builder reads: the install dir and folder paths become
// directory nodes, components defined inside features move to components,
// missing component ids are generated, a package without features gets one,
// and conditional service arguments become setProperty actions. Every step
// leaves an already expanded config unchanged, so ApplyDefaults stays
// idempotent.
func expandShorthands(cfg *Config) { //nolint:cyclop // one pass in a fixed order; splitting would scatter it
	hoistFeatureComponents(cfg)
	featureFirewall(cfg)

	tree := newDirTree(cfg)
	tree.installDir()

	cfg.InstallDir.expanded = true

	// Add the install dir before any folder path, so that a path through
	// the same folder reuses it whatever the order of the components.
	for _, comp := range cfg.Components {
		if !unplacedCarrier(comp) && tree.needsInstallDir(comp.Directory) {
			tree.ensureInstallDir()

			break
		}
	}

	for i, comp := range cfg.Components {
		if !unplacedCarrier(comp) {
			tree.origin = comp.Path(i) + ".directory"
			comp.Directory = tree.resolve(comp.Directory)
		}
	}

	shortcutDirectories(cfg, tree)

	// Generated ids first, so that a feature can name the component the
	// carrier joins by one.
	componentIDs(cfg)

	for _, comp := range cfg.Components {
		if unplacedCarrier(comp) {
			comp.Directory = tree.carrierDirectory(comp)

			// A carrier in a 32-bit folder is a 32-bit component (ICE80).
			if cfg.Package.Platform.Is64Bit() && comp.Bitness == "" && folders32[tree.topFolder(comp.Directory)] {
				comp.Bitness = "32"
			}
		}
	}

	for _, rule := range cfg.Firewall {
		// Only the first run fills it in; a component set from Go since
		// stands.
		if rule.carrier != nil && !rule.ownComponent && rule.Component == "" {
			rule.Component = rule.carrier.ID
		}
	}

	rememberInstallDir(cfg, tree)
	referenceFeatureComponents(cfg)
	defaultFeature(cfg)
	serviceArguments(cfg)
}

// shortcutDirectories resolves the folder of every shortcut, the Start
// menu by default, and the target of a folder shortcut, the way a
// component's directory is resolved.
func shortcutDirectories(cfg *Config, tree *dirTree) {
	for i, comp := range cfg.Components {
		for fileIndex, file := range comp.Files {
			for index, shortcut := range file.Shortcuts {
				if shortcut == nil {
					continue // reported by the validator
				}

				if shortcut.Directory == "" {
					shortcut.Directory = DefaultShortcutDirectory
				}

				tree.origin = fmt.Sprintf("%s.files[%d].shortcuts[%d].directory", comp.Path(i), fileIndex, index)
				shortcut.Directory = tree.resolve(shortcut.Directory)
			}
		}

		for index, shortcut := range comp.Shortcuts {
			if shortcut == nil {
				continue // reported by the validator
			}

			if shortcut.Directory == "" {
				shortcut.Directory = DefaultShortcutDirectory
			}

			scPath := fmt.Sprintf("%s.shortcuts[%d]", comp.Path(i), index)
			tree.origin = scPath + ".directory"
			shortcut.Directory = tree.resolve(shortcut.Directory)

			if shortcut.Folder != "" {
				tree.origin = scPath + ".folder"
				shortcut.Folder = tree.resolve(shortcut.Folder)
			}
		}
	}
}

// featureFirewall moves the firewall rules written under features to
// cfg.Firewall. Each such feature gets an empty component in the install dir
// whose state the rules follow.
func featureFirewall(cfg *Config) {
	var walk func([]*Feature, string)

	walk = func(features []*Feature, prefix string) {
		for i, feature := range features {
			featurePath := fmt.Sprintf("%s[%d]", prefix, i)

			if len(feature.Firewall) > 0 {
				carrier := firewallCarrier(cfg, feature, featurePath)

				for j, rule := range feature.Firewall {
					rule.origin = fmt.Sprintf("%s.firewall[%d]", featurePath, j)
					rule.ownComponent = rule.Component != ""
					rule.carrier = carrier
				}

				cfg.Firewall = append(cfg.Firewall, feature.Firewall...)
				feature.Firewall = nil
			}

			walk(feature.Features, featurePath+".features")
		}
	}

	walk(cfg.Features, "features")
}

// firewallCarrier returns the component that carries a feature's firewall
// rules, adding it on the first call. carrierDirectory places it once the
// tree is known.
func firewallCarrier(cfg *Config, feature *Feature, featurePath string) *Component {
	for _, comp := range cfg.Components {
		if comp.firewallFeature != "" && comp.firewallFeature == feature.ID {
			return comp
		}
	}

	carrier := &Component{
		KeyPath: KeyPathDirectory,
		// Diagnostics about the carrier name the feature, the place the
		// user can change.
		origin:          featurePath,
		firewallFeature: feature.ID,
		carrierOf:       feature,
	}

	cfg.Components = append(cfg.Components, carrier)
	feature.Components = append(feature.Components, carrier)

	return carrier
}

// unplacedCarrier reports whether a component is a firewall carrier whose
// directory carrierDirectory still has to pick.
func unplacedCarrier(comp *Component) bool {
	return comp.firewallFeature != "" && comp.Directory == ""
}

// carrierDirectory places a carrier: in its feature's directory, else in
// the install dir when the package has one, else beside a component of the
// feature, else in the first configurable directory, both outside the user
// profile. Only without any of these does it add the install dir.
func (t *dirTree) carrierDirectory(carrier *Component) string {
	for _, dirID := range []string{carrier.carrierOf.Directory, t.cfg.InstallDir.ID} {
		if dirID != "" && t.byID[dirID] != nil {
			return dirID
		}
	}

	if dirID := t.featureComponentDir(carrier.carrierOf); dirID != "" {
		return dirID
	}

	if dirID := configurableDir(t.cfg.Directories, ""); dirID != "" {
		return dirID
	}

	t.ensureInstallDir()

	return t.cfg.InstallDir.ID
}

// featureComponentDir returns the directory of the first component of a
// feature, or of its sub-features, outside the user profile.
func (t *dirTree) featureComponentDir(feature *Feature) string {
	byID := make(map[string]*Component, len(t.cfg.Components))
	for _, comp := range t.cfg.Components {
		if comp.ID != "" {
			byID[comp.ID] = comp
		}
	}

	found := ""

	eachFeature([]*Feature{feature}, func(f *Feature, _ int) {
		for _, entry := range f.Components {
			comp := entry
			if entry.ref {
				comp = byID[entry.ID]
			}

			if found == "" && comp != nil && !unplacedCarrier(comp) && t.byID[comp.Directory] != nil &&
				!profileFolders[t.topFolder(comp.Directory)] {
				found = comp.Directory
			}
		}
	})

	return found
}

// topFolder returns the top-level folder above a directory.
func (t *dirTree) topFolder(dirID string) string {
	var find func([]*Directory) bool

	top := dirID

	find = func(dirs []*Directory) bool {
		for _, dir := range dirs {
			if dir.ID == dirID || find(dir.Children) {
				return true
			}
		}

		return false
	}

	for _, dir := range t.cfg.Directories {
		if dir.ID == dirID || find(dir.Children) {
			top = dir.ID

			break
		}
	}

	return top
}

// configurableDir returns the first configurable directory whose top-level
// folder is not in the user profile.
func configurableDir(dirs []*Directory, top string) string {
	for _, dir := range dirs {
		dirTop := top
		if dirTop == "" {
			dirTop = dir.ID
		}

		if profileFolders[dirTop] {
			continue
		}

		if dir.Configurable {
			return dir.ID
		}

		if found := configurableDir(dir.Children, dirTop); found != "" {
			return found
		}
	}

	return ""
}

// hoistFeatureComponents appends the components defined inside features to
// cfg.Components. The feature entries are turned into references later, once
// every component has an id.
func hoistFeatureComponents(cfg *Config) {
	listed := make(map[*Component]bool, len(cfg.Components))
	for _, comp := range cfg.Components {
		listed[comp] = true
	}

	var walk func([]*Feature, string)

	walk = func(features []*Feature, prefix string) {
		for i, feature := range features {
			featurePath := fmt.Sprintf("%s[%d]", prefix, i)

			for entry, comp := range feature.Components {
				if comp.ref || comp.origin != "" {
					continue
				}

				// A Config built in Go may put the same component in
				// components and in a feature.
				// referenceFeatureComponents turns the entry into a reference
				// once the component has an id.
				if listed[comp] {
					continue
				}

				// A Config built in Go refers to a component with an entry
				// that carries nothing but the id.
				if !comp.defined && reflect.DeepEqual(*comp, Component{ID: comp.ID}) {
					comp.ref = true

					continue
				}

				comp.origin = fmt.Sprintf("%s.components[%d]", featurePath, entry)
				cfg.Components = append(cfg.Components, comp)
			}

			walk(feature.Features, featurePath+".features")
		}
	}

	walk(cfg.Features, "features")
}

// ComponentRef returns a features[].components entry that refers to the
// component with the given id, for a Config built in Go.
func ComponentRef(id string) *Component {
	return &Component{ID: id, ref: true}
}

// referenceFeatureComponents replaces each component defined inside a feature
// with a reference to it by id.
func referenceFeatureComponents(cfg *Config) {
	eachFeature(cfg.Features, func(f *Feature, _ int) {
		for j, comp := range f.Components {
			if !comp.ref {
				f.Components[j] = &Component{ID: comp.ID, ref: true}
			}
		}
	})
}

// componentIDs gives every component without an id one derived from its
// content: the key file's name, else the service name, else the directory.
func componentIDs(cfg *Config) {
	used := make(map[string]bool, len(cfg.Components))

	for _, comp := range cfg.Components {
		if comp.ID != "" {
			used[comp.ID] = true
		}
	}

	for _, comp := range cfg.Components {
		if comp.ID != "" {
			continue
		}

		comp.ID = uniqueID(identifier(componentBase(comp, cfg.Product.Name)), used)
	}
}

// componentBase is the name a component without an id is named after.
// product is the product name, which a shortcut without a name defaults to.
func componentBase(comp *Component, product string) string {
	base := "Component"

	switch {
	case comp.firewallFeature != "":
		base = comp.firewallFeature + "_Firewall"
	case keyFileByRef(comp, comp.KeyPath) != nil:
		base = fileName(keyFileByRef(comp, comp.KeyPath))
	case len(comp.Files) > 0:
		base = fileName(comp.Files[0])
	case len(comp.Services) > 0:
		base = comp.Services[0].Name
	case shortcutsOnly(comp) && comp.Shortcuts[0] != nil && cmp.Or(comp.Shortcuts[0].Name, product) != "":
		// Named after its first shortcut, so that the id, and the
		// ComponentId of its shortcuts, do not move with the order. The
		// shortcut's name is not defaulted yet.
		base = cmp.Or(comp.Shortcuts[0].Name, product) + " Shortcuts"
	case len(comp.FileTypes) > 0 && comp.FileTypes[0] != nil && comp.FileTypes[0].ProgID != "":
		base = comp.FileTypes[0].ProgID
	case comp.Directory != "":
		base = comp.Directory
	}

	return base
}

// RememberComponentID is the component that installDir.remember adds.
const RememberComponentID = "RememberInstallDir"

// RememberActionID is the type-51 action that installDir.remember adds.
const RememberActionID = "RestoreInstallDir"

// rememberOrigin marks the component installDir.remember adds.
const rememberOrigin = "installDir.remember"

// RememberValueName is the registry value that holds the remembered folder.
const RememberValueName = "InstallDir"

// RememberKey returns the registry key below the root (HKLM per machine,
// HKCU per user, HKMU for a dual scope) that installDir.remember writes and
// searches.
func RememberKey(cfg *Config) string {
	return `Software\` + cfg.Product.Manufacturer + `\` + cfg.Product.Name
}

// RememberRoot returns the registry root of installDir.remember.
func RememberRoot(cfg *Config) string {
	switch {
	case cfg.Package.Scope == ScopePerUser:
		return "HKCU"
	case cfg.Package.Scope.Dual():
		return rootHKMU
	default:
		return rootHKLM
	}
}

// rememberInstallDir adds the component that writes the install dir to the
// registry for installDir.remember, and puts it into every top-level
// feature. The builder adds the search that reads it back.
func rememberInstallDir(cfg *Config, tree *dirTree) {
	if !cfg.InstallDir.Remember || tree.ensureInstallDir() == nil {
		return
	}

	for _, comp := range cfg.Components {
		if comp.ID == RememberComponentID {
			return
		}
	}

	cfg.Components = append(cfg.Components, &Component{
		ID:        RememberComponentID,
		Directory: cfg.InstallDir.ID,
		Registry: []*RegistryValue{{
			Root:  RememberRoot(cfg),
			Key:   RememberKey(cfg),
			Name:  RememberValueName,
			Value: StringList{"[" + cfg.InstallDir.ID + "]"},
		}},
		origin: rememberOrigin,
	})

	// In a 32-bit folder it is a 32-bit component (ICE80), and the search
	// reads the same registry view.
	if cfg.Package.Platform.Is64Bit() && folders32[tree.topFolder(cfg.InstallDir.ID)] {
		cfg.Components[len(cfg.Components)-1].Bitness = "32"
	}

	for _, feature := range cfg.Features {
		feature.Components = append(feature.Components, ComponentRef(RememberComponentID))
	}
}

// defaultFeature puts every component into one feature when the file
// defines none.
func defaultFeature(cfg *Config) {
	if len(cfg.Features) > 0 || len(cfg.Components) == 0 {
		return
	}

	feature := &Feature{ID: DefaultFeatureID, Title: cfg.Product.Name, Display: "expanded"}

	if dir := findDirectory(cfg.Directories, cfg.InstallDir.ID); dir != nil && dir.Configurable {
		feature.Directory = dir.ID
	}

	for _, comp := range cfg.Components {
		feature.Components = append(feature.Components, &Component{ID: comp.ID, ref: true})
	}

	cfg.Features = []*Feature{feature}
}

// serviceArguments turns every argument with a condition into a private
// property, set by an immediate setProperty action before InstallServices
// under that condition, and references the property in the command line.
// A property that is never set formats to an empty string.
func serviceArguments(cfg *Config) {
	used := make(map[string]bool, len(cfg.CustomActions))
	for _, ca := range cfg.CustomActions {
		used[ca.ID] = true
	}

	for c, comp := range cfg.Components {
		compPath := comp.Path(c)

		for svcIndex, svc := range comp.Services {
			conditional := 0

			for i, arg := range svc.Arguments {
				if arg.Condition == "" {
					continue
				}

				conditional++

				action := uniqueID(identifier("SetArg_"+svc.Name+"_"+strconv.Itoa(conditional)), used)
				property := "Arg_" + strings.TrimPrefix(action, "SetArg_")

				cfg.CustomActions = append(cfg.CustomActions, &CustomAction{
					origin:      fmt.Sprintf("%s.services[%d].arguments[%d]", compPath, svcIndex, i),
					ID:          action,
					SetProperty: &SetProperty{Name: property, Value: arg.Value},
					Schedule: []Placement{{
						Sequence:  sequenceExecute,
						Before:    "InstallServices",
						Condition: arg.Condition,
					}},
				})

				svc.Arguments[i] = Argument{Value: "[" + property + "]"}
			}
		}
	}
}

// identifier turns text into a Windows Installer identifier: characters
// other than letters, digits, "_" and "." become "_", and a leading digit or
// "." gets a "_" in front.
func identifier(text string) string {
	var sb strings.Builder

	for _, char := range text {
		if !identifierChar(char) {
			char = '_'
		}

		if sb.Len() == 0 && !identifierStart(char) {
			sb.WriteByte('_')
		}

		sb.WriteRune(char)
	}

	if sb.Len() == 0 {
		return "_"
	}

	result := sb.String()

	// Leave room for the suffix of uniqueID and for prefixes.
	if len(result) > maxGeneratedIDLength {
		sum := sha256.Sum256([]byte(result))
		result = result[:maxGeneratedIDLength-hashSuffixLength] + "_" + hex.EncodeToString(sum[:4])
	}

	return result
}

func identifierStart(char rune) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '_'
}

func identifierChar(char rune) bool {
	return identifierStart(char) || char >= '0' && char <= '9' || char == '.'
}

// uniqueID returns base, or base with a numeric suffix if base is taken, and
// marks the result as used.
func uniqueID(base string, used map[string]bool) string {
	result := base
	for n := 2; used[result]; n++ {
		result = base + "_" + strconv.Itoa(n)
	}

	used[result] = true

	return result
}

func findDirectory(dirs []*Directory, dirID string) *Directory {
	for _, dir := range dirs {
		if dir.ID == dirID {
			return dir
		}

		if found := findDirectory(dir.Children, dirID); found != nil {
			return found
		}
	}

	return nil
}

// dirTree adds directory nodes for the install dir and for folder paths.
type dirTree struct {
	cfg  *Config
	byID map[string]*Directory
	// origin is the YAML path of the value that new nodes come from.
	origin string
	// installOrigin is the YAML path of the install dir's name.
	installOrigin string
	// installCandidates are the directories of the file's own tree that
	// could be meant as the install dir.
	installCandidates []string
	// installDefaulted is set when the file sets no installDir.
	installDefaulted bool
}

func newDirTree(cfg *Config) *dirTree {
	tree := &dirTree{cfg: cfg, byID: make(map[string]*Directory)}

	var walk func([]*Directory, string)

	walk = func(dirs []*Directory, top string) {
		for _, dir := range dirs {
			if _, dup := tree.byID[dir.ID]; !dup {
				tree.byID[dir.ID] = dir
			}

			if top == "" && standardFolders[dir.ID] && dir.ID != targetDir {
				walk(dir.Children, dir.ID)

				continue
			}

			// A configurable directory, or one below Program Files, may be
			// the install dir the file meant.
			if !standardFolders[dir.ID] && IsPublicProperty(dir.ID) && (dir.Configurable || programFilesFolders[top]) {
				tree.installCandidates = append(tree.installCandidates, dir.ID)
			}

			walk(dir.Children, top)
		}
	}
	walk(cfg.Directories, "")

	slices.Sort(tree.installCandidates)

	return tree
}

// installDir fills in the install dir's defaults. A package without a
// directory tree, or one that sets installDir, gets the node right away;
// otherwise it is added when a component lands in it. The first expansion
// decides, so that a second one adds nothing.
func (t *dirTree) installDir() {
	eager := !t.cfg.InstallDir.expanded && (len(t.cfg.Directories) == 0 || t.cfg.InstallDir != InstallDir{})

	install := &t.cfg.InstallDir
	perUser := t.cfg.Package.Scope == ScopePerUser
	t.installDefaulted = *install == InstallDir{}

	if install.ID == "" {
		install.ID = DefaultInstallDirID
	}

	if install.Parent == "" {
		install.Parent = defaultInstallParent(t.cfg.Package.Platform, perUser)
		if perUser {
			install.Name = perUserProgramsName(install.Name)
		}
	}

	t.installOrigin = "installDir.name"

	if install.Name == "" {
		t.installOrigin = productNamePath
		install.Name = t.cfg.Product.Name

		if perUser {
			install.Name = "Programs/" + install.Name
		}
	}

	if eager {
		t.ensureInstallDir()
	}
}

// perUserProgramsName puts a per-user install dir name below Programs, the
// per-user Program Files in LocalAppDataFolder, unless it already starts
// there or is empty.
func perUserProgramsName(name string) string {
	if names := splitPath(name); len(names) == 0 || strings.EqualFold(names[0], "Programs") {
		return name
	}

	return "Programs/" + name
}

// ensureInstallDir returns the install dir's node, adding it unless the
// directories define its id. It adds nothing for an invalid installDir,
// which the validator reports.
func (t *dirTree) ensureInstallDir() *Directory {
	install := t.cfg.InstallDir
	if dir := t.byID[install.ID]; dir != nil {
		return dir
	}

	if !validInstallDir(install) {
		return nil
	}

	names := t.installNames()

	origin := t.origin
	t.origin = t.installOrigin

	defer func() { t.origin = origin }()

	parent := t.top(install.Parent)
	for _, name := range names[:len(names)-1] {
		parent = t.child(parent, name, "")
	}

	if t.installDefaulted {
		t.cfg.InstallDir.besides = strings.Join(t.installCandidates, ", ")
	}

	dir := t.child(parent, names[len(names)-1], install.ID)
	dir.Configurable = true

	return dir
}

// installNames returns the folders of the install dir's path. A name taken
// from product.name is one folder, even if it contains a slash.
func (t *dirTree) installNames() []string {
	install := t.cfg.InstallDir
	if t.installOrigin != productNamePath {
		return splitPath(install.Name)
	}

	if t.cfg.Package.Scope == ScopePerUser {
		return []string{"Programs", t.cfg.Product.Name}
	}

	return []string{t.cfg.Product.Name}
}

// validInstallDir reports whether the install dir can be generated: a public
// id, a standard parent folder and a name.
func validInstallDir(install InstallDir) bool {
	return IsIdentifier(install.ID) && IsPublicProperty(install.ID) &&
		standardFolders[install.Parent] && len(splitPath(install.Name)) > 0
}

// defaultInstallParent is Program Files for the package's bitness, or the
// user's local application data for a per-user package (ICE105).
func defaultInstallParent(platform Platform, perUser bool) string {
	switch {
	case perUser:
		return "LocalAppDataFolder"
	case platform == PlatformX86:
		return "ProgramFilesFolder"
	default:
		return "ProgramFiles64Folder"
	}
}

// resolve returns the directory id for a component's directory value: empty
// is the install dir, a known id or standard folder stays as it is, and
// anything else is a folder path. A path whose first segment is a known id or
// standard folder starts there; any other path starts in the install dir.
// "./" forces a path, for a folder name that looks like an id.
func (t *dirTree) resolve(value string) string {
	switch {
	case value == "", value == t.cfg.InstallDir.ID:
		t.ensureInstallDir()

		return t.cfg.InstallDir.ID
	case t.byID[value] != nil:
		return value
	case standardFolders[value]:
		t.top(value)

		return value
	}

	forced := strings.HasPrefix(value, "./") || strings.HasPrefix(value, `.\`)
	names := splitPath(value)

	// An upper-case first segment is meant as an id, and one that differs
	// from a known id only in case is a typo. Leave both for the validator
	// to report instead of creating a folder.
	if !forced && len(names) > 0 && t.looksLikeID(names[0]) {
		return value
	}

	parent, names := t.pathStart(names, forced)
	if parent == nil {
		// The install dir is not in the tree; the validator reports it.
		return t.cfg.InstallDir.ID
	}

	for _, name := range names {
		parent = t.child(parent, name, "")
	}

	return parent.ID
}

// needsInstallDir reports whether resolve puts a directory value in the
// install dir.
func (t *dirTree) needsInstallDir(value string) bool {
	switch {
	case value == "", value == t.cfg.InstallDir.ID:
		return true
	case t.byID[value] != nil, standardFolders[value]:
		return false
	case strings.HasPrefix(value, "./"), strings.HasPrefix(value, `.\`):
		return true
	}

	names := splitPath(value)
	if len(names) == 0 {
		return true
	}

	first := names[0]

	return first == t.cfg.InstallDir.ID ||
		(t.byID[first] == nil && !standardFolders[first] && !t.looksLikeID(first))
}

// looksLikeID reports whether a word that is not a known id was meant as
// one: it is upper case, or a known id or standard folder in another case.
func (t *dirTree) looksLikeID(word string) bool {
	known := t.byID[word] != nil || standardFolders[word] || word == t.cfg.InstallDir.ID

	return !known && IsIdentifier(word) && (IsPublicProperty(word) || t.knownIgnoringCase(word))
}

func (t *dirTree) knownIgnoringCase(value string) bool {
	if strings.EqualFold(t.cfg.InstallDir.ID, value) {
		return true
	}

	for known := range t.byID {
		if strings.EqualFold(known, value) {
			return true
		}
	}

	for known := range standardFolders {
		if strings.EqualFold(known, value) {
			return true
		}
	}

	return false
}

// pathStart returns the node a folder path starts at and the names below it.
func (t *dirTree) pathStart(names []string, forced bool) (*Directory, []string) {
	if forced || len(names) == 0 {
		return t.ensureInstallDir(), names
	}

	if names[0] == t.cfg.InstallDir.ID {
		return t.ensureInstallDir(), names[1:]
	}

	if dir := t.byID[names[0]]; dir != nil {
		return dir, names[1:]
	}

	if standardFolders[names[0]] {
		return t.top(names[0]), names[1:]
	}

	return t.ensureInstallDir(), names
}

// top returns the node of a standard folder, adding it at the top level if
// needed.
func (t *dirTree) top(folder string) *Directory {
	// The tree may nest standard folders under TARGETDIR.
	if dir := t.byID[folder]; dir != nil {
		return dir
	}

	dir := &Directory{ID: folder}
	t.cfg.Directories = append(t.cfg.Directories, dir)
	t.byID[folder] = dir

	return dir
}

// child returns parent's child folder called name, adding it if needed. A
// new node gets dirID, or one generated from the parent and the name.
func (t *dirTree) child(parent *Directory, name, dirID string) *Directory {
	for _, existing := range parent.Children {
		if strings.EqualFold(existing.Name, name) && (dirID == "" || existing.ID == dirID) {
			return existing
		}
	}

	if dirID == "" {
		used := make(map[string]bool, len(t.byID))
		for key := range t.byID {
			used[key] = true
		}

		dirID = uniqueID(identifier(parent.ID+"_"+name), used)
	}

	dir := &Directory{ID: dirID, Name: name, origin: t.origin}
	parent.Children = append(parent.Children, dir)
	t.byID[dirID] = dir

	return dir
}

// splitPath splits a folder path at "/" and "\", dropping "." and empty
// segments.
func splitPath(folders string) []string {
	var names []string

	for _, name := range strings.FieldsFunc(folders, func(r rune) bool { return r == '/' || r == '\\' }) {
		if name != "." {
			names = append(names, name)
		}
	}

	return names
}
