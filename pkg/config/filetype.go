package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// maxProgIDLength is the longest ProgID Microsoft documents for COM.
const maxProgIDLength = 39

// DefaultVerb is the shell verb a file type gets when it lists none.
const DefaultVerb = "open"

// DefaultVerbArguments follow the quoted file path in a verb's command.
const DefaultVerbArguments = `"%1"`

// systemExtensions are extensions that Windows or common programs already
// own. A package that registers one takes it over from them, and
// uninstalling removes the default, so the extension has none afterwards
// (REG-07).
var systemExtensions = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"bat": true, "bmp": true, "cer": true, "cmd": true, "crt": true, "csv": true, "dll": true,
	"exe": true, "gif": true, "htm": true, "html": true, "inf": true, "ini": true, "jpeg": true,
	"jpg": true, "js": true, "json": true, "lnk": true, "log": true, "md": true, "msi": true,
	"pdf": true, "pem": true, "png": true, "ps1": true, "reg": true, "sys": true, "txt": true,
	"url": true, "vbs": true, "xml": true, "zip": true,
}

// defaultFileTypes gives a file type that leaves out verbs an open verb and
// every verb its default arguments. An explicit empty list keeps it without
// verbs, for a type that only names an icon and its extensions.
func defaultFileTypes(fileTypes []*FileType) {
	for _, fileType := range fileTypes {
		if fileType == nil {
			continue
		}

		if fileType.Verbs == nil {
			fileType.Verbs = []*FileTypeVerb{{ID: DefaultVerb}}
		}

		for _, verb := range fileType.Verbs {
			if verb != nil && verb.Arguments == nil {
				verb.Arguments = new(DefaultVerbArguments)
			}
		}
	}
}

// fileTypes checks the file types of every file and component. ProgIDs
// and extensions are keys under HKEY_CLASSES_ROOT, so each ProgID and each
// extension's default may be registered once per package; any number of
// ProgIDs may list an extension with openWithOnly.
func (v *validator) fileTypes(cfg *Config, known *refs) {
	seen := fileTypeKeys{progIDs: make(map[string]string), extensions: make(map[string]string)}

	for i, comp := range cfg.Components {
		for j, file := range comp.Files {
			for k, fileType := range file.FileTypes {
				typePath := fmt.Sprintf("%s.files[%d].fileTypes[%d]", comp.Path(i), j, k)
				if fileType != nil && fileType.File != "" {
					v.errorf(typePath+".file", "a file type under a file opens that file; list it in the component's fileTypes instead")
				}

				v.fileType(typePath, fileType, known, seen)
			}
		}

		for k, fileType := range comp.FileTypes {
			typePath := fmt.Sprintf("%s.fileTypes[%d]", comp.Path(i), k)
			if fileType != nil {
				v.fileTypeFile(typePath+".file", fileType.File, known)
			}

			v.fileType(typePath, fileType, known, seen)
		}

		if fileTypesOnly(comp) && !slices.ContainsFunc(comp.FileTypes, writesValue) {
			v.errorf(comp.Path(i)+".fileTypes", "the file types write no value the component can take as its key path: "+
				"give one a description, an icon, a verb or an extension it owns")
		}
	}
}

// fileTypeKeys are the ProgIDs and extension defaults registered so far,
// by the YAML path that registers them.
type fileTypeKeys struct {
	progIDs    map[string]string
	extensions map[string]string
}

// fileType checks one file type.
func (v *validator) fileType(typePath string, fileType *FileType, known *refs, seen fileTypeKeys) {
	if fileType == nil {
		v.errorf(typePath, "empty entry")

		return
	}

	v.progID(typePath+".progId", fileType.ProgID, seen.progIDs)

	if fileType.IconFile != "" {
		v.fileTypeFile(typePath+".iconFile", fileType.IconFile, known)
	}

	listed := make(map[string]bool)

	for l, ext := range fileType.Extensions {
		v.fileTypeExtension(fmt.Sprintf("%s.extensions[%d]", typePath, l), ext, seen.extensions, listed)
	}

	verbs := make(map[string]bool)

	for l, verb := range fileType.Verbs {
		v.fileTypeVerb(fmt.Sprintf("%s.verbs[%d]", typePath, l), verb, verbs)
	}
}

// fileTypeFile checks the file a file type opens or takes its icon from.
func (v *validator) fileTypeFile(field, ref string, known *refs) {
	if !v.required(field, ref) {
		return
	}

	if !known.hasFile(ref) {
		v.errorf(field, "file %q is not defined in any component; a file that a folder source adds cannot be named, "+
			"so list it on a component of its own and exclude it from the folder", ref)
	}
}

// fileTypesOnly reports whether a component writes nothing but its own
// file types, so that the builder keys it on one of their registry values.
func fileTypesOnly(comp *Component) bool {
	return len(comp.FileTypes) > 0 && comp.KeyPath == "" && len(comp.Files) == 0 && len(comp.Registry) == 0
}

// writesValue reports whether a file type writes a registry value with
// data, which can be a component's key path: its description, icon, verb
// commands or the default of an extension it owns. An openWithOnly entry
// is an empty value from a Null Value column (REG-04).
func writesValue(fileType *FileType) bool {
	if fileType == nil {
		return false
	}

	return fileType.Description != "" || fileType.IconFile != "" || fileType.IconIndex != nil || len(fileType.Verbs) > 0 ||
		slices.ContainsFunc(fileType.Extensions, func(ext *FileTypeExtension) bool { return ext != nil && !ext.OpenWithOnly })
}

func (v *validator) progID(field, progID string, seen map[string]string) {
	if !v.required(field, progID) {
		return
	}

	if problem := progIDProblem(progID); problem != "" {
		v.errorf(field, "%q %s", progID, problem)

		return
	}

	if other, dup := seen[strings.ToLower(progID)]; dup {
		v.errorf(field, "%s registers %q too", other, progID)
	}

	seen[strings.ToLower(progID)] = field

	// Microsoft's rules for COM ProgIDs; Explorer itself accepts more.
	if len(progID) > maxProgIDLength || unicode.IsDigit(rune(progID[0])) ||
		strings.ContainsFunc(progID, func(r rune) bool { return r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		v.warnf(field, "%q is not a COM-style ProgID (at most %d letters, digits and periods, not starting with a digit)", progID, maxProgIDLength)
	}
}

func (v *validator) fileTypeExtension(field string, ext *FileTypeExtension, defaults map[string]string, listed map[string]bool) {
	if ext == nil {
		v.errorf(field, "empty entry")

		return
	}

	name := ext.Extension
	if !v.required(field+".extension", name) {
		return
	}

	if problem := extensionProblem(name); problem != "" {
		v.errorf(field+".extension", "%q %s", name, problem)

		return
	}

	lower := strings.ToLower(name)
	if listed[lower] {
		v.errorf(field+".extension", ".%s is listed twice", name)

		return
	}

	listed[lower] = true

	if ext.OpenWithOnly {
		if ext.ContentType != "" {
			v.errorf(field+".contentType", "openWithOnly leaves the content type of .%s alone", name)
		}

		return
	}

	if other, dup := defaults[lower]; dup {
		v.errorf(field+".extension", "%s registers .%s too; the last one written wins (set openWithOnly on one of them)", other, name)
	}

	defaults[lower] = field

	if systemExtensions[lower] {
		v.warnf(field+".extension", ".%s belongs to Windows or other programs: installing takes it over as its default program "+
			"and uninstalling removes its default; openWithOnly adds the program to its Open with menu instead", name)
	}
}

func (v *validator) fileTypeVerb(field string, verb *FileTypeVerb, seen map[string]bool) {
	if verb == nil {
		v.errorf(field, "empty entry")

		return
	}

	if !v.required(field+".id", verb.ID) {
		return
	}

	if problem := classKeyProblem(verb.ID); problem != "" {
		v.errorf(field+".id", "%q %s", verb.ID, problem)

		return
	}

	if seen[strings.ToLower(verb.ID)] {
		v.errorf(field+".id", "verb %q is listed twice", verb.ID)
	}

	seen[strings.ToLower(verb.ID)] = true
}

// progIDProblem reports why progID cannot be a ProgID, or "".
func progIDProblem(progID string) string {
	switch {
	case strings.HasPrefix(progID, "."):
		return "starts with a dot, which makes it an extension key"
	// HKCR\* holds every file's verbs, and +, - and * are the Registry
	// table's key operators in the OpenWithProgids value name.
	case progID == "*" || progID == "+" || progID == "-":
		return "is not a ProgID"
	default:
		return classKeyProblem(progID)
	}
}

// extensionProblem reports why name cannot be an extension, or "".
func extensionProblem(name string) string {
	switch {
	case strings.HasPrefix(name, "."):
		return "has a dot: write the extension without it"
	case classKeyProblem(name) != "" || strings.ContainsAny(name, " *?."):
		return "is not a file name extension"
	default:
		return ""
	}
}

// classKeyProblem reports why name cannot be one key below
// HKEY_CLASSES_ROOT, or "".
func classKeyProblem(name string) string {
	switch {
	case strings.ContainsAny(name, `\[]{}`):
		return `must not hold \, [, ], { or }`
	case strings.TrimSpace(name) != name:
		return "must not start or end with a space"
	default:
		return ""
	}
}
