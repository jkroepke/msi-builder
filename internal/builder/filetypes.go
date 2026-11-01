package builder

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// fileTypes writes the file types of every file as Registry rows in the
// file's component, the way non-advertised ProgId, Extension and Verb
// elements are written and the way the real packages checked do it
// (specification section 10.2), plus the ProgID in each extension's
// OpenWithProgids list: HKCR, which the engine maps to
// HKLM\Software\Classes per machine and HKCU\Software\Classes per user. A
// per-user package names HKCU\Software\Classes itself (ICE105); a dual one
// keeps HKCR, which follows the context it installs in (INST-02).
// Uninstalling removes the values the package wrote, the extension's
// default and Content Type included, and keeps other programs' values in
// the key (REG-07).
func (b *build) fileTypes() error {
	for _, comp := range b.cfg.Components {
		writer := b.classWriter(comp.ID)

		for _, file := range comp.Files {
			for _, fileType := range file.FileTypes {
				for _, row := range b.fileTypeRows(comp.ID, "[#"+config.FileKey(file)+"]", fileType) {
					writer.add(row.key, row.name, row.value)
				}
			}
		}

		for _, fileType := range comp.FileTypes {
			for _, row := range b.fileTypeRows(comp.ID, b.fileTypeTarget(comp.ID, fileType.File), fileType) {
				writer.add(row.key, row.name, row.value)
			}
		}
	}

	return nil
}

// classWriter returns the writer of the Registry rows of a component's
// file types: HKCR, or HKCU\Software\Classes in a per-user package.
func (b *build) classWriter(compID string) classWriter {
	root, prefix := registryRoots[rootHKCR], ""
	if b.cfg.Package.Scope == config.ScopePerUser {
		root, prefix = registryRoots["HKCU"], `Software\Classes\`
	}

	return classWriter{b: b, comp: compID, root: root, prefix: prefix}
}

// fileTypeTarget returns how a file type of component compID names the
// packaged file ref: [#key] or, when a feature of compID does not install
// the file, its folder and long name (fileTarget, ICE69).
func (b *build) fileTypeTarget(compID, ref string) string {
	file := b.fileByRef(ref)
	if file == nil {
		// The validator reported it.
		return ""
	}

	return b.fileTarget(compID, file)
}

// fileTypeKeyPath returns the Registry key of the value that keys a
// component listing nothing but file types, the first value with data
// that its file types write, and that value's root, key and name for the
// component code. The validator makes sure there is one.
func (b *build) fileTypeKeyPath(comp *config.Component) (string, string) {
	writer := b.classWriter(comp.ID)

	for _, fileType := range comp.FileTypes {
		for _, row := range b.fileTypeRows(comp.ID, b.fileTypeTarget(comp.ID, fileType.File), fileType) {
			if row.value != nil {
				return writer.id(row.key, row.name), strings.ToLower(strconv.Itoa(writer.root) + `\` + writer.prefix + row.key + `\` + row.name)
			}
		}
	}

	return "", ""
}

// classRow is one Registry row of a file type, below the class root; a
// nil value is a named value from a Null Value column.
type classRow struct {
	value any
	key   string
	name  string
}

// fileTypeRows returns the rows of one file type that opens target, in
// the order they are written: the ProgID, its icon and verbs, the default
// and Content Type of each extension it owns, and its OpenWithProgids
// entries. Empty values are left out; a key without values is created by
// the values below it.
func (b *build) fileTypeRows(compID, target string, fileType *config.FileType) []classRow {
	var rows []classRow

	value := func(key, name, text string) {
		if text == "" {
			return
		}

		if strings.HasPrefix(text, "#") {
			text = "#" + text
		}

		rows = append(rows, classRow{key: key, name: name, value: text})
	}

	target = `"` + target + `"`
	progID := fileType.ProgID

	value(progID, "", escapeFormatted(fileType.Description))

	if icon := b.fileTypeIcon(compID, target, fileType); icon != "" {
		value(progID+`\DefaultIcon`, "", icon)
	}

	for _, verb := range fileType.Verbs {
		key := progID + `\shell\` + verb.ID
		value(key, "", escapeFormatted(verb.Text))
		value(key+`\command`, "", strings.TrimSpace(target+" "+*verb.Arguments))
	}

	for _, ext := range fileType.Extensions {
		if !ext.OpenWithOnly {
			value("."+ext.Extension, "", progID)
			value("."+ext.Extension, "Content Type", escapeFormatted(ext.ContentType))
		}

		// A named value with a Null Value column, which the engine stores
		// as an empty REG_SZ (REG-04; Microsoft documents a Null Value only
		// for the names +, - and *). Shell lists such as OpenWithProgids
		// name their entries this way.
		rows = append(rows, classRow{key: "." + ext.Extension + `\OpenWithProgids`, name: escapeFormatted(progID)})
	}

	return rows
}

// fileTypeIcon returns the DefaultIcon value of a file type: the quoted
// file it opens, or its iconFile, and the icon index, which defaults to 0
// with an iconFile. Without either it writes none.
func (b *build) fileTypeIcon(compID, target string, fileType *config.FileType) string {
	index := 0
	if fileType.IconIndex != nil {
		index = *fileType.IconIndex
	}

	switch {
	case fileType.IconFile != "":
		target = `"` + b.fileTypeTarget(compID, fileType.IconFile) + `"`
	case fileType.IconIndex == nil:
		return ""
	}

	return fmt.Sprintf("%s,%d", target, index)
}

// classWriter writes the Registry rows of one file type.
type classWriter struct {
	b      *build
	comp   string
	prefix string
	root   int
}

// add writes one Registry row.
func (w classWriter) add(key, name string, value any) {
	w.b.table("Registry").Add(w.id(key, name), w.root, w.prefix+key, nullable(name), value, w.comp)
}

// id returns the key of the Registry row of a value.
func (w classWriter) id(key, name string) string {
	return generatedID("reg", "FileType", w.comp, strconv.Itoa(w.root), strings.ToLower(w.prefix+key), strings.ToLower(name))
}

// fileTypesOnly reports whether a component writes nothing but its own
// file types, so that resolveKeyPath keys it on one of their values, as
// the validator's fileTypesOnly decides it.
func fileTypesOnly(comp *config.Component) bool {
	return len(comp.FileTypes) > 0 && comp.KeyPath == "" && len(comp.Files) == 0 && len(comp.Registry) == 0
}
