package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Column limits of the Shortcut table.
const (
	maxShortcutName      = 128
	maxShortcutText      = 255
	maxShortcutIconIndex = 32767
	maxShortcutTarget    = 72
	// minShortcutPropertyPages is the Page Count of Windows Installer 5.0,
	// the first engine that reads MsiShortcutProperty.
	minShortcutPropertyPages = 500
	// maxURLFileName leaves room for ".url" in IniFile.FileName (l255).
	maxURLFileName = 251
	// keyArguments is the YAML key of a shortcut's arguments.
	keyArguments = "arguments"
)

// clsidProperties are the shell properties of type CLSID that shortcuts
// commonly carry.
var clsidProperties = map[string]bool{"System.AppUserModel.ToastActivatorCLSID": true} //nolint:gochecknoglobals // lookup table

// bracedGUID matches a GUID in registry format, which the engine needs for
// a CLSID property.
var bracedGUID = regexp.MustCompile(`^\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}$`)

// propertyKeyName matches the canonical name of a shell property,
// dot-separated words such as System.AppUserModel.ID.
var propertyKeyName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

// shortcuts checks every shortcut under the files of the components and
// in the components' own shortcuts lists.
func (v *validator) shortcuts(cfg *Config, known *refs) {
	ids := make(map[string]string)
	created := make(map[string]string)

	for i, comp := range cfg.Components {
		for j, file := range comp.Files {
			v.shortcutTarget(fmt.Sprintf("%s.files[%d].shortcuts", comp.Path(i), j), file)

			for k, shortcut := range file.Shortcuts {
				scPath := fmt.Sprintf("%s.files[%d].shortcuts[%d]", comp.Path(i), j, k)
				if shortcut == nil {
					v.errorf(scPath, "empty entry")

					continue
				}

				if kind := shortcutKind(shortcut); kind != "" {
					v.errorf(scPath+"."+kind, "a shortcut under a file points at that file; list it in the component's shortcuts instead")
				}

				v.shortcutID(scPath, shortcut, ids)
				v.shortcut(cfg, scPath, shortcut, known)
				v.shortcutCreated(scPath, shortcut, created)
			}
		}

		for k, shortcut := range comp.Shortcuts {
			scPath := fmt.Sprintf("%s.shortcuts[%d]", comp.Path(i), k)
			if shortcut == nil {
				v.errorf(scPath, "empty entry")

				continue
			}

			v.shortcutID(scPath, shortcut, ids)
			v.componentShortcut(cfg, scPath, shortcut, known)
			v.shortcutCreated(scPath, shortcut, created)
		}
	}
}

// shortcutsOnly reports whether a component holds nothing but shortcuts.
// The builder writes no Component row for it: the generated component that
// carries its shortcuts takes its id (internal/builder shortcutsOnly).
func shortcutsOnly(comp *Component) bool {
	if len(comp.Shortcuts) == 0 || comp.Purge.Enabled() || comp.CreateFolder || comp.FolderPermissions != nil ||
		comp.KeyPath != "" || comp.FirewallFeature() != "" {
		return false
	}

	return len(comp.Files)+len(comp.Registry)+len(comp.RemoveRegistry)+len(comp.RemoveFiles)+len(comp.RemoveFolders)+
		len(comp.Services)+len(comp.ServiceControl)+len(comp.RestartResources)+len(comp.EventLog)+len(comp.Environment)+len(comp.FileTypes) == 0
}

// shortcutCreated reports two shortcuts that create one file: the engine
// writes [Directory_]Name.lnk, and a .url shortcut Name.url, so a second
// one of a name in a folder would overwrite the first.
func (v *validator) shortcutCreated(scPath string, shortcut *Shortcut, created map[string]string) {
	file := shortcut.Name + shortcutExtension(shortcut)

	key := shortcut.Directory + "\x1f" + strings.ToLower(file)
	if other, dup := created[key]; dup {
		v.errorf(scPath+".name", "%s also creates %q in %s", other, file, shortcut.Directory)
	}

	created[key] = scPath
}

// shortcutExtension is the extension of the file a shortcut creates.
func shortcutExtension(shortcut *Shortcut) string {
	if shortcut.URL != "" {
		return ".url"
	}

	return ".lnk"
}

// shortcutKind returns the YAML key of the target a shortcut names, or ""
// when it names none.
func shortcutKind(shortcut *Shortcut) string {
	switch {
	case shortcut.File != "":
		return "file"
	case shortcut.Folder != "":
		return "folder"
	case shortcut.Program != "":
		return "program"
	case shortcut.URL != "":
		return "url"
	default:
		return ""
	}
}

// shortcutTargets returns the number of targets a component's shortcut
// names; exactly one is valid.
func shortcutTargets(shortcut *Shortcut) int {
	count := 0

	for _, set := range []bool{shortcut.File != "", shortcut.Folder != "", shortcut.Program != "", shortcut.URL != ""} {
		if set {
			count++
		}
	}

	return count
}

// componentShortcut checks a shortcut of a component's own shortcuts list.
func (v *validator) componentShortcut(cfg *Config, scPath string, shortcut *Shortcut, known *refs) {
	switch shortcutTargets(shortcut) {
	case 0:
		v.errorf(scPath, "set one of file, folder, program or url")

		return
	case 1:
	default:
		v.errorf(scPath, "set only one of file, folder, program or url")

		return
	}

	if shortcut.URL != "" {
		v.urlShortcut(scPath, shortcut, known)

		return
	}

	switch {
	case shortcut.File != "":
		if !known.hasFile(shortcut.File) {
			v.errorf(scPath+".file", "file %q is not defined in any component; a file that a folder source adds cannot be named, "+
				"so list it on a component of its own and exclude it from the folder", shortcut.File)
		} else if file := anyFileByRef(cfg, shortcut.File); file != nil {
			v.targetLength(scPath+".file", "[#"+fileID(file)+"]", "file id")
		}
	case shortcut.Folder != "":
		v.folderTarget(scPath, shortcut, known)
	case shortcut.Program != "":
		v.programTarget(scPath, shortcut)
	}

	v.shortcut(cfg, scPath, shortcut, known)
}

// anyFileByRef returns the file that ref names in any component.
func anyFileByRef(cfg *Config, ref string) *File {
	for _, comp := range cfg.Components {
		if file := FileByRef(comp.Files, ref); file != nil {
			return file
		}
	}

	return nil
}

// targetLength checks that a formatted target fits Shortcut.Target.
func (v *validator) targetLength(field, target, what string) {
	if len(target) > maxShortcutTarget {
		v.errorf(field, "the shortcut target %s is %d characters, at most %d fit; use a shorter %s", target, len(target), maxShortcutTarget, what)
	}
}

// folderTarget checks the folder of a folder shortcut. A folder that is not
// a standard folder gets a CreateFolder row, so that the shortcut does not
// point at nothing.
func (v *validator) folderTarget(scPath string, shortcut *Shortcut, known *refs) {
	field := scPath + ".folder"
	if known.dirs[shortcut.Folder] == nil {
		v.errorf(field, "directory %q is not defined", shortcut.Folder)

		return
	}

	v.targetLength(field, "["+shortcut.Folder+"]", "directory id")

	if top := known.topFolder(shortcut.Folder); v.forbiddenFolder(top) && top != shortcut.Folder {
		v.errorf(field, "%s cannot create a folder under %s (ICE105)", v.perUserPackage(), top)
	}

	for _, field := range []struct{ name, value string }{{keyArguments, shortcut.Arguments}, {"workingDirectory", shortcut.WorkingDirectory}} {
		if field.value != "" {
			v.errorf(scPath+"."+field.name, "a folder shortcut opens the folder; it has no %s", field.name)
		}
	}
}

// programTarget checks the target of a program shortcut. A Target without
// a property reference is a Feature key and the row an advertised shortcut
// (section 8.11), so the path must start with a folder property.
func (v *validator) programTarget(scPath string, shortcut *Shortcut) {
	field := scPath + ".program"

	switch {
	case !strings.HasPrefix(shortcut.Program, "["):
		v.errorf(field, "start the path with a folder property such as [SystemFolder]; "+
			"a target without one is an advertised shortcut")
	case strings.ContainsAny(shortcut.Program, "\"\r\n"):
		v.errorf(field, "the path cannot hold quotes or line breaks; put the arguments in arguments")
	}

	v.targetLength(field, shortcut.Program, "path")
}

// urlShortcut checks an Internet shortcut, which the builder writes as a
// .url file through IniFile rows, not as a Shortcut row.
func (v *validator) urlShortcut(scPath string, shortcut *Shortcut, known *refs) {
	v.shortcutName(scPath, shortcut, maxURLFileName, "msi-builder", ".url")
	v.shortcutDirectory(scPath, shortcut, known)
	v.urlIcon(scPath, shortcut, known)

	field := scPath + ".url"

	switch {
	case !urlScheme.MatchString(shortcut.URL):
		v.errorf(field, "%q does not start with a scheme such as https:", shortcut.URL)
	case !printableASCII(shortcut.URL):
		v.errorf(field, "only printable ASCII fits a .url file; percent-encode other characters")
	case utf8.RuneCountInString(shortcut.URL) > maxShortcutText:
		v.errorf(field, "at most %d characters", maxShortcutText)
	}

	for _, field := range []struct {
		name string
		set  bool
	}{
		{keyArguments, shortcut.Arguments != ""},
		{"description", shortcut.Description != ""},
		{"workingDirectory", shortcut.WorkingDirectory != ""},
		{"icon", shortcut.Icon != ""},
		{"show", shortcut.Show != ShowNormal},
		{"properties", len(shortcut.Properties) > 0},
	} {
		if field.set {
			v.errorf(scPath+"."+field.name, "not supported on a url shortcut, which is a .url file and not a .lnk")
		}
	}
}

// urlIcon checks the icon of a url shortcut: a packaged file and an index
// into its icons.
func (v *validator) urlIcon(scPath string, shortcut *Shortcut, known *refs) {
	switch {
	case shortcut.IconFile != "" && !known.hasFile(shortcut.IconFile):
		v.errorf(scPath+".iconFile", "file %q is not defined in any component", shortcut.IconFile)
	case shortcut.IconFile == "" && shortcut.IconIndex != 0:
		v.errorf(scPath+".iconIndex", "an icon index needs iconFile; without one the shell shows the browser's icon")
	}

	if shortcut.IconIndex < -maxShortcutIconIndex || shortcut.IconIndex > maxShortcutIconIndex {
		v.errorf(scPath+".iconIndex", "%d is out of range -%d to %d", shortcut.IconIndex, maxShortcutIconIndex, maxShortcutIconIndex)
	}
}

// urlScheme matches the scheme of an absolute URL (RFC 3986 section 3.1).
var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

func printableASCII(s string) bool {
	for _, r := range s {
		if r < ' ' || r > '~' {
			return false
		}
	}

	return true
}

// shortcutTarget checks that [#key], the target the builder writes, fits
// the s72 Target column.
func (v *validator) shortcutTarget(scPath string, file *File) {
	if key := fileID(file); len(file.Shortcuts) > 0 && len(key)+3 > maxShortcutTarget {
		v.errorf(scPath, "the shortcut target [#%s] is %d characters, at most %d fit; use a shorter file id", key, len(key)+3, maxShortcutTarget)
	}
}

func (v *validator) shortcutID(scPath string, shortcut *Shortcut, ids map[string]string) {
	if shortcut.ID == "" {
		return
	}

	if !IsIdentifier(shortcut.ID) || len(shortcut.ID) > MaxIdentifierLength {
		v.errorf(scPath+".id", "%q is not an identifier of at most %d characters", shortcut.ID, MaxIdentifierLength)
	}

	if other, dup := ids[shortcut.ID]; dup {
		v.errorf(scPath+".id", "shortcut %s is also defined at %s", shortcut.ID, other)
	}

	ids[shortcut.ID] = scPath
}

// shortcutName checks the name of the file a shortcut creates, without the
// extension the engine or the builder adds.
func (v *validator) shortcutName(scPath string, shortcut *Shortcut, limit int, adder, extension string) {
	if !v.required(scPath+".name", shortcut.Name) {
		return
	}

	switch problem := nameProblem(shortcut.Name, "file name"); {
	case problem != "":
		v.errorf(scPath+".name", "%s", problem)
	case utf8.RuneCountInString(shortcut.Name) > limit:
		v.errorf(scPath+".name", "at most %d characters", limit)
	case strings.HasSuffix(strings.ToLower(shortcut.Name), extension):
		v.warnf(scPath+".name", "%s adds %s to the name; %q becomes %s%s", adder, extension, shortcut.Name, shortcut.Name, extension)
	}
}

// shortcutDirectory checks the folder a shortcut is created in, which is
// also the folder of the component that carries it.
func (v *validator) shortcutDirectory(scPath string, shortcut *Shortcut, known *refs) {
	if known.dirs[shortcut.Directory] == nil {
		v.errorf(scPath+".directory", "directory %q is not defined", shortcut.Directory)
	} else if top := known.topFolder(shortcut.Directory); v.forbiddenFolder(top) {
		// The shortcuts' component lives in this folder.
		v.errorf(scPath+".directory", "%s cannot install under %s (ICE105)", v.perUserPackage(), top)
	}
}

func (v *validator) shortcut(cfg *Config, scPath string, shortcut *Shortcut, known *refs) { //nolint:cyclop // a flat list of independent checks
	v.shortcutName(scPath, shortcut, maxShortcutName, "Windows", ".lnk")
	v.shortcutDirectory(scPath, shortcut, known)

	// A component's shortcut other than a file one has no working directory
	// by default.
	if (shortcut.WorkingDirectory != "" || shortcutKind(shortcut) == "") && known.dirs[shortcut.WorkingDirectory] == nil {
		v.errorf(scPath+".workingDirectory", "directory %q is not defined", shortcut.WorkingDirectory)
	}

	if shortcut.Icon != "" {
		if _, ok := cfg.Icons[shortcut.Icon]; !ok {
			v.errorf(scPath+".icon", "icon %q is not defined in icons", shortcut.Icon)
		}
	}

	if shortcut.IconFile != "" {
		v.errorf(scPath+".iconFile", "only a url shortcut takes iconFile; a .lnk takes icon, a key of icons")
	}

	if shortcut.Icon == "" && shortcut.IconIndex != 0 {
		v.errorf(scPath+".iconIndex", "an icon index needs icon; without one the shell shows the target's first icon")
	}

	if shortcut.IconIndex < -maxShortcutIconIndex || shortcut.IconIndex > maxShortcutIconIndex {
		v.errorf(scPath+".iconIndex", "%d is out of range -%d to %d", shortcut.IconIndex, maxShortcutIconIndex, maxShortcutIconIndex)
	}

	for _, field := range []struct{ name, text string }{{keyArguments, shortcut.Arguments}, {"description", shortcut.Description}} {
		if utf8.RuneCountInString(field.text) > maxShortcutText {
			v.errorf(scPath+"."+field.name, "at most %d characters", maxShortcutText)
		}
	}

	v.oneOf(scPath+".show", shortcut.Show, ShowNormal, ShowMaximized, ShowMinimized)
	v.shortcutProperties(cfg, scPath, shortcut)
}

// shortcutProperties checks the shell properties of a shortcut, the rows of
// MsiShortcutProperty (spec section 19.1).
func (v *validator) shortcutProperties(cfg *Config, scPath string, shortcut *Shortcut) {
	for _, name := range slices.Sorted(maps.Keys(shortcut.Properties)) {
		propPath := scPath + ".properties." + name
		if !propertyKeyName.MatchString(name) {
			v.errorf(propPath, "%q is not the canonical name of a shell property, such as System.AppUserModel.ID", name)
		}

		value := shortcut.Properties[name]

		switch {
		case value == "":
			v.errorf(propPath, "a value is required; PropVariantValue cannot be empty")
		case clsidProperties[name] && !strings.Contains(value, "[") && !bracedGUID.MatchString(value):
			// SHORTCUT-02: without braces the engine logs warning 1946
			// (HRESULT 87) and leaves the property unset.
			v.errorf(propPath, "%q is not a CLSID in braces, such as {6E3F1A3C-61B6-4C1B-9A0F-2C5D8B7E4A10}", value)
		}
	}

	if len(shortcut.Properties) == 0 {
		return
	}

	if pages, err := PageCount(cfg.Package.InstallerVersion); err == nil && pages < minShortcutPropertyPages {
		v.warnf(scPath+".properties", "Windows Installer %s ignores shortcut properties; they need installerVersion 5.0", cfg.Package.InstallerVersion)
	}
}
