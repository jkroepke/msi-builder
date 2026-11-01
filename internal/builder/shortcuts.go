package builder

import (
	"cmp"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jkroepke/msi-builder/pkg/config"
)

const (
	// removeOnUninstall is the RemoveFile InstallMode that removes at
	// uninstall.
	removeOnUninstall = 2
	// maxShortcutNameColumn is the width of Shortcut.Name (l128).
	maxShortcutNameColumn = 128
	// maxShortcutTargetColumn is the width of Shortcut.Target (s72).
	maxShortcutTargetColumn = 72
	// maxIniFileNameColumn is the width of IniFile.FileName (l255).
	maxIniFileNameColumn = 255
	// iniAddLine is msidbIniFileActionAddLine, which creates or updates
	// the entry.
	iniAddLine = 0
)

// showCommands maps a shortcut's show setting to ShowCmd; normal is the
// null default (SHORTCUT-01).
var showCommands = map[string]any{config.ShowNormal: nil, config.ShowMaximized: 3, config.ShowMinimized: 7} //nolint:gochecknoglobals // lookup table

// shortcuts writes the Shortcut rows. The shortcuts to the files of one
// component and the component's own shortcuts go into a generated
// component of their own, in the folder of the first shortcut, with an
// HKCU registry value as key path (ICE38, ICE43) and RemoveFile rows for
// the folders it creates in the user profile (ICE64). The component joins
// every feature of the component the shortcuts are listed in.
// SHORTCUT-01 installed and removed shortcuts from such a component and from the
// file's own one alike; the ICE suite asks for this layout.
func (b *build) shortcuts() error {
	for _, comp := range b.cfg.Components {
		info := b.components[comp.ID]
		path := info.cfg.Path(info.index)

		var carrier string

		removed := make(map[string]bool)
		created := make(map[string]bool)

		carry := func(dirID string) string {
			if carrier == "" {
				carrier = b.shortcutComponent(info, dirID)
			}

			b.removeShortcutFolders(carrier, dirID, removed)

			return carrier
		}

		for fileIndex, cfgFile := range comp.Files {
			for index, shortcut := range cfgFile.Shortcuts {
				key := config.FileKey(cfgFile)
				b.writeShortcut(fmt.Sprintf("%s.files[%d].shortcuts[%d]", path, fileIndex, index), carry(shortcut.Directory),
					key, "[#"+key+"]", shortcut.WorkingDirectory, shortcut)
			}
		}

		for index, shortcut := range comp.Shortcuts {
			b.componentShortcut(fmt.Sprintf("%s.shortcuts[%d]", path, index), comp.ID, carry(shortcut.Directory), shortcut, created, removed)
		}
	}

	return nil
}

// componentShortcut writes a shortcut of a component's own list: a .url
// file for a URL, else a Shortcut row whose target is the file, the folder
// or the program.
func (b *build) componentShortcut(path, compID, carrier string, shortcut *config.Shortcut, created, removed map[string]bool) {
	switch {
	case shortcut.URL != "":
		// Unlike CreateShortcuts, WriteIniValues is not known to create
		// the folder of the file.
		b.createFolder(carrier, shortcut.Directory, created)
		b.writeURLShortcut(path, compID, carrier, shortcut)
	case shortcut.File != "":
		file := b.fileByRef(shortcut.File)
		if file == nil {
			b.errorf(path+".file", "file %q is not defined in any component", shortcut.File)

			return
		}

		// A file shortcut starts in the file's folder unless told otherwise.
		b.writeShortcut(path, carrier, file.Key, b.fileTarget(compID, file), cmp.Or(shortcut.WorkingDirectory, file.Component.dir.id), shortcut)
	case shortcut.Folder != "":
		// A folder it creates in the user profile needs RemoveFile rows up
		// to the profile folder, like the shortcut's own folder (ICE64).
		b.createFolder(carrier, shortcut.Folder, created)
		b.removeShortcutFolders(carrier, shortcut.Folder, removed)
		b.writeShortcut(path, carrier, shortcut.Folder, "["+shortcut.Folder+"]", "", shortcut)
	default:
		b.writeShortcut(path, carrier, shortcut.Program, shortcut.Program, shortcut.WorkingDirectory, shortcut)
	}
}

// fileTarget returns how a shortcut listed in component compID refers to
// a packaged file: [#key] when every feature of compID also installs the
// file's component, else the file's folder and long name. ICE69 reports a
// [#key] whose file is in a component of another feature as an error
// (SHORTCUT-04): the reference is empty when that feature is absent.
func (b *build) fileTarget(compID string, file *fileInfo) string {
	if file.Component.cfg.ID == compID {
		return "[#" + file.Key + "]"
	}

	shared := true

	config.WalkFeatures(b.cfg.Features, func(feature, _ *config.Feature) {
		var listsComp, listsFile bool

		for _, comp := range feature.Components {
			listsComp = listsComp || comp.ID == compID
			listsFile = listsFile || comp.ID == file.Component.cfg.ID
		}

		if listsComp && !listsFile {
			shared = false
		}
	})

	if shared {
		return "[#" + file.Key + "]"
	}

	return "[" + file.Component.dir.id + "]" + file.LongName
}

// fileByRef returns the packaged file that ref names, by key or by the long
// name of a file without an id.
func (b *build) fileByRef(ref string) *fileInfo {
	for _, comp := range b.cfg.Components {
		cfgFile := config.FileByRef(comp.Files, ref)
		if cfgFile == nil {
			continue
		}

		for _, file := range b.files {
			if file.cfg == cfgFile {
				return file
			}
		}
	}

	return nil
}

// createFolder makes sure a folder that a shortcut opens or that holds a
// .url file exists: one that is not a standard folder gets a CreateFolder
// row in the carrier, once, which RemoveFolders removes again once it is
// empty. A component that installs to the folder is not enough, since it
// may have no files, a condition or another feature.
func (b *build) createFolder(carrier, dirID string, created map[string]bool) {
	b.dirs[dirID].used = true

	if config.IsStandardFolder(dirID) || created[dirID] {
		return
	}

	created[dirID] = true
	b.table("CreateFolder").Add(dirID, carrier)
}

// hashRemovableCarriers adds a marker to the ProductCode digest when a
// permanent component has shortcuts in a generated component. Earlier
// builds made the generated component permanent under the ComponentId that
// carrierGUID now changes, so the rebuild needs another code, or installing
// it over the old one fails with 1638.
func (b *build) hashRemovableCarriers(hasher io.Writer) {
	if slices.ContainsFunc(b.cfg.Components, func(comp *config.Component) bool {
		return comp.Permanent && !shortcutsOnly(comp) && hasShortcuts(comp)
	}) {
		_, _ = hasher.Write([]byte("shortcuts:removable-carrier"))
	}
}

// hasShortcuts reports whether a component lists shortcuts, of its own or
// of its files.
func hasShortcuts(comp *config.Component) bool {
	return len(comp.Shortcuts) > 0 || slices.ContainsFunc(comp.Files, func(file *config.File) bool {
		return len(file.Shortcuts) > 0
	})
}

// shortcutsOnly reports whether a component holds nothing but shortcuts,
// as the config package decides it: it gets no Component row of its own,
// and the generated component that carries its shortcuts takes its id.
func shortcutsOnly(comp *config.Component) bool {
	if len(comp.Shortcuts) == 0 || comp.Purge.Enabled() || comp.CreateFolder || comp.FolderPermissions != nil ||
		comp.KeyPath != "" || comp.FirewallFeature() != "" {
		return false
	}

	return len(comp.Files)+len(comp.Registry)+len(comp.RemoveRegistry)+len(comp.RemoveFiles)+len(comp.RemoveFolders)+
		len(comp.Services)+len(comp.ServiceControl)+len(comp.EventLog)+len(comp.Environment) == 0
}

// topFolder returns the standard folder above a directory.
func (b *build) topFolder(dirID string) string {
	dir := b.dirs[dirID]
	for dir != nil && !dir.standard {
		dir = b.dirs[dir.parent]
	}

	if dir == nil {
		return ""
	}

	return dir.id
}

// shortcutComponent writes the component that carries the shortcuts to the
// files of info and returns its key. The key path value name holds the
// upgrade code and the case of the component id, so that neither two
// components nor two products share a key path; so does the key, unless the
// component lists only shortcuts and is the carrier itself.
func (b *build) shortcutComponent(info *componentInfo, dirID string) string {
	comp := info.cfg

	valueName := generatedID("cmp", "Shortcuts", b.upgradeCode, comp.ID)
	own := shortcutsOnly(comp)

	carrier := valueName
	if own {
		carrier = comp.ID
	}

	registry := generatedID("reg", "Shortcuts", b.upgradeCode, comp.ID)

	// The component's bitness follows its folder: a 64-bit component in a
	// 32-bit folder fails ICE80.
	is64 := info.is64 && !config.Is32BitFolder(b.topFolder(dirID))

	// A generated carrier keeps the component's transitive condition but not
	// its permanence: a file that stays installed is no reason for its
	// shortcuts to outlive the product. Only a component of shortcuts alone,
	// which is the carrier itself, is permanent when it says so.
	bitness := "32"
	attributes := compRegistryKeyPath | info.attributes&compTransitive

	if own {
		attributes |= info.attributes & compPermanent
	}

	if is64 {
		bitness = "64"
		attributes |= comp64Bit
	}

	guid := b.carrierGUID(info, own, bitness, carrier)

	b.table("Component").Add(carrier, guid, dirID, attributes, componentCondition(comp), registry)
	b.table("Registry").Add(registry, registryRoots["HKCU"], `Software\[Manufacturer]\[ProductName]`, valueName, "#1", carrier)
	b.dirs[dirID].used = true
	b.carriers[comp.ID] = append(b.carriers[comp.ID], carrier)

	return carrier
}

// carrierGUID returns the ComponentId of a shortcut carrier: generated from
// the upgrade code, its bitness and key, or the component's own guid when
// the component of shortcuts only is the carrier.
func (b *build) carrierGUID(info *componentInfo, own bool, bitness, carrier string) any {
	comp := info.cfg

	seed := b.upgradeCode + "|" + bitness + "|shortcuts|" + carrier

	// Builds that made the carrier of a permanent component permanent as
	// well left its ComponentId registered after uninstall, and a later
	// package with the same code kept its shortcuts too (observed). A new
	// code leaves that registration behind.
	if !own && info.attributes&compPermanent != 0 {
		seed += "|removable"
	}

	var guid any = uuid5(namespaceComponent, seed)

	// A component of shortcuts only is the carrier, so its guid applies.
	if own {
		switch comp.GUID {
		case config.Auto:
		case config.None:
			guid = nil
		default:
			pinned, err := config.NormalizeGUID(comp.GUID)
			if err != nil {
				b.errorf(info.cfg.Path(info.index)+".guid", "%v", err)
			}

			guid = pinned
		}

		b.uniqueGUID(info.cfg.Path(info.index), comp.ID, guid)
	}

	return guid
}

// writeShortcut writes a Shortcut row. seed makes the generated key
// unique per target; workDir is a directory key or empty.
func (b *build) writeShortcut(path, carrier, seed, target, workDir string, shortcut *config.Shortcut) {
	dirID := shortcut.Directory

	if b.shortcutNames[dirID] == nil {
		b.shortcutNames[dirID] = make(map[string]bool)
	}

	key := shortcut.ID
	if key == "" {
		key = generatedID("sc", "Shortcut", seed, dirID, strings.ToLower(shortcut.Name))
	}

	// The column holds the generated short name too.
	name := filename(dirID, shortcut.Name, false, b.shortcutNames[dirID], config.ValidShortName)
	if n := utf8.RuneCountInString(name); n > maxShortcutNameColumn {
		b.errorf(path+".name", "with its short name the shortcut name is %d characters, at most %d fit", n, maxShortcutNameColumn)
	}

	if len(target) > maxShortcutTargetColumn {
		b.errorf(path, "the shortcut target %s is %d characters, at most %d fit; give the file or its directory a shorter id",
			target, len(target), maxShortcutTargetColumn)
	}

	var icon, iconIndex any
	if shortcut.Icon != "" {
		icon, iconIndex = shortcut.Icon, shortcut.IconIndex
	}

	if workDir != "" {
		b.dirs[workDir].used = true
	}

	b.table("Shortcut").Add(key, dirID, name, carrier, target, nullable(shortcut.Arguments), nullable(shortcut.Description),
		nil, icon, iconIndex, showCommands[shortcut.Show], nullable(workDir), nil, nil, nil, nil)

	// The engine sets these when it creates the .lnk (SHORTCUT-02).
	for _, name := range slices.Sorted(maps.Keys(shortcut.Properties)) {
		b.table("MsiShortcutProperty").Add(generatedID("scp", key, name), key, name, shortcut.Properties[name])
	}
}

// writeURLShortcut writes an Internet shortcut: WriteIniValues writes
// URL= into the [InternetShortcut] section of Name.url, and a RemoveFile
// row deletes the file at uninstall, also when the shell has added
// sections RemoveIniValues does not know (SHORTCUT-03).
func (b *build) writeURLShortcut(path, compID, carrier string, shortcut *config.Shortcut) {
	dirID := shortcut.Directory
	long := shortcut.Name + ".url"

	key := shortcut.ID
	if key == "" {
		key = generatedID("ini", "IniFile", carrier, dirID, strings.ToLower(long))
	}

	for _, file := range b.files {
		if b.folderKey(file.Component.dir.id) == b.folderKey(dirID) && strings.EqualFold(file.LongName, long) {
			b.errorf(path+".name", "%s is also a packaged file in this folder", long)

			return
		}
	}

	name := b.removeFileName(dirID, long)
	if n := utf8.RuneCountInString(name); n > maxIniFileNameColumn {
		b.errorf(path+".name", "with its short name the file name is %d characters, at most %d fit", n, maxIniFileNameColumn)
	}

	b.table("IniFile").Add(key, name, dirID, "InternetShortcut", "URL", shortcut.URL, iniAddLine, carrier)

	b.table("RemoveFile").Add(generatedID("rmf", "IniFile", key), carrier, name, dirID, removeOnUninstall)

	if shortcut.IconFile != "" {
		b.writeURLIcon(path, compID, carrier, key, name, shortcut)
	}
}

// writeURLIcon writes the icon of a .url: the shell reads IconFile and
// IconIndex in the [InternetShortcut] section (SHORTCUT-03).
func (b *build) writeURLIcon(path, compID, carrier, key, name string, shortcut *config.Shortcut) {
	file := b.fileByRef(shortcut.IconFile)
	if file == nil {
		b.errorf(path+".iconFile", "file %q is not defined in any component", shortcut.IconFile)

		return
	}

	ini := b.table("IniFile")
	ini.Add(generatedID("ini", "IconFile", key), name, shortcut.Directory, "InternetShortcut", "IconFile", b.fileTarget(compID, file), iniAddLine, carrier)
	ini.Add(generatedID("ini", "IconIndex", key), name, shortcut.Directory, "InternetShortcut", "IconIndex", strconv.Itoa(shortcut.IconIndex), iniAddLine, carrier)
}

// removeShortcutFolders writes a RemoveFile row for every folder between a
// shortcut's directory and the profile folder above it, once per carrier.
func (b *build) removeShortcutFolders(carrier, dirID string, removed map[string]bool) {
	if !config.IsProfileFolder(b.topFolder(dirID)) {
		return
	}

	for dir := b.dirs[dirID]; dir != nil && !dir.standard; dir = b.dirs[dir.parent] {
		if removed[dir.id] {
			continue
		}

		removed[dir.id] = true
		b.removedFolders[dir.id] = true
		b.table("RemoveFile").Add(generatedID("rmf", "Shortcuts", carrier, dir.id), carrier, nil, dir.id, removeOnUninstall)
	}
}

// removeModes maps removeFiles[].on to RemoveFile.InstallMode.
var removeModes = map[string]int{ //nolint:gochecknoglobals // lookup table
	config.RemoveOnInstall:   1,
	config.RemoveOnUninstall: removeOnUninstall,
	config.RemoveOnBoth:      3,
}

// removeFiles writes the RemoveFile rows of every component's removeFiles,
// and one for each folder a component creates in the user profile, which
// the engine removes once it is empty (ICE64).
func (b *build) removeFiles() error {
	for _, comp := range b.cfg.Components {
		if shortcutsOnly(comp) {
			continue
		}

		for dir := range b.profileFolders(comp.Directory) {
			b.removedFolders[dir.id] = true
		}
	}

	for i, comp := range b.cfg.Components {
		for j, entry := range comp.RemoveFiles {
			b.removeFileRow(fmt.Sprintf("%s.removeFiles[%d]", comp.Path(i), j), comp, entry)
		}

		// A component of shortcuts only has no row in its directory.
		if shortcutsOnly(comp) {
			continue
		}

		for dir := range b.profileFolders(comp.Directory) {
			b.table("RemoveFile").Add(generatedID("rmf", "Folder", comp.ID, dir.id), comp.ID, nil, dir.id, removeOnUninstall)
		}
	}

	return nil
}

// removeFileRow writes the RemoveFile row of one removeFiles entry.
func (b *build) removeFileRow(path string, comp *config.Component, entry *config.RemoveFile) {
	dirID := cmp.Or(entry.Directory, comp.Directory)
	// A folder from a property is no directory of the package: its short
	// names have a set of their own.
	names := dirID

	if entry.Property != "" {
		dirID = entry.Property
		names = "property:" + entry.Property
	} else {
		b.dirs[dirID].used = true

		b.warnDuplicateFolderRow(path, dirID, entry)
	}

	key := cmp.Or(entry.ID, generatedID("rmf", "RemoveFile", comp.ID, names, strings.ToLower(entry.Name), entry.On))

	var name any
	if entry.Name != "" {
		name = b.removeFileName(names, entry.Name)
	}

	b.table("RemoveFile").Add(key, comp.ID, name, dirID, removeModes[entry.On])
}

// warnDuplicateFolderRow warns about a removeFiles entry that removes a
// folder on uninstall that a generated row already removes.
func (b *build) warnDuplicateFolderRow(path, dirID string, entry *config.RemoveFile) {
	if entry.Name == "" && entry.On == config.RemoveOnUninstall && b.removedFolders[dirID] {
		b.warnf(path, "the builder already removes this folder on uninstall, for a shortcut or a component in the user profile; this entry adds a second row")
	}
}

// profileFolders yields the folders between a directory in the user
// profile and the standard folder above it; nothing outside the profile.
func (b *build) profileFolders(dirID string) iter.Seq[*dirInfo] {
	return func(yield func(*dirInfo) bool) {
		if !config.IsProfileFolder(b.topFolder(dirID)) {
			return
		}

		for dir := b.dirs[dirID]; dir != nil && !dir.standard; dir = b.dirs[dir.parent] {
			if !yield(dir) {
				return
			}
		}
	}
}

// removeFileName returns the FileName of a RemoveFile row: that of a
// packaged file with the same long name in the folder, so that both carry
// the same short name, else the pattern with a generated short name part
// when it is no valid short name. Such a short name matches only the
// literal name, so a long wildcard pattern works with long names only.
func (b *build) removeFileName(dirID, pattern string) string {
	folder := b.folderKey(dirID)

	for _, file := range b.files {
		if b.folderKey(file.Component.dir.id) == folder && strings.EqualFold(file.LongName, pattern) {
			return file.FileName
		}
	}

	return filename(dirID, pattern, false, b.taken(dirID), validShortPattern)
}

// validShortPattern reports whether a filename pattern is a valid short
// name once its wildcards stand for one character each.
func validShortPattern(pattern string) bool {
	return config.ValidShortName(strings.NewReplacer("*", "x", "?", "x").Replace(pattern))
}
