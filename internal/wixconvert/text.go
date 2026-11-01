package wixconvert

import (
	"regexp"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

var (
	// preprocessorVar matches $(var.Name), $(env.Name), $(sys.Name) and
	// the v4 short form $(Name).
	preprocessorVar = regexp.MustCompile(`\$\((?:(var|env|sys)\.)?([A-Za-z_][A-Za-z0-9_.]*)\)`)
	// bindVar matches !(bind.X), !(loc.X), !(wix.X) and !(bindpath.X).
	bindVar = regexp.MustCompile(`!\((bind|loc|wix|bindpath)\.([^)]*)\)`)
	// bracketRef matches a formatted-text reference such as [INSTALLDIR].
	bracketRef = regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_.]*)\]`)
)

// varName makes a preprocessor variable name into an msi-builder variable
// name, which has no dots.
func varName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// value returns an attribute with WiX variables replaced.
func (c *converter) value(e *elem, attr string) string {
	return c.expand(e, e.attr(attr))
}

// expand replaces WiX variables in text: preprocessor variables become
// {{ .NAME }} template actions, bind variables that the converter knows are
// filled in and the others are noted. A "{{" in the source is written so
// that the template keeps it. Directory references renamed by the converter
// are updated.
func (c *converter) expand(elm *elem, text string) string {
	text = c.expandVars(elm, escapeTemplate(text))

	text = bindVar.ReplaceAllStringFunc(text, func(match string) string {
		parts := bindVar.FindStringSubmatch(match)
		if v, ok := c.bindValue(parts[1], parts[2]); ok {
			return v
		}

		c.todof(nil, "", elm.line, "%s is a WiX %s variable; replace it", match, parts[1])

		return match
	})

	if len(c.renames) > 0 {
		text = bracketRef.ReplaceAllStringFunc(text, func(m string) string {
			if to, ok := c.renames[m[1:len(m)-1]]; ok {
				return "[" + to + "]"
			}

			return m
		})
	}

	return text
}

// expandVars turns preprocessor variables into {{ .NAME }} actions.
func (c *converter) expandVars(elm *elem, text string) string {
	return preprocessorVar.ReplaceAllStringFunc(text, func(m string) string {
		parts := preprocessorVar.FindStringSubmatch(m)
		if parts[1] == "sys" {
			c.todof(nil, "", elm.line, "%s is a WiX system variable; replace it", m)

			return m
		}

		name := varName(parts[2])
		c.usedVars[name] = true

		return varAction(name)
	})
}

// bindValue resolves the bind variables the converter can fill in.
func (c *converter) bindValue(kind, name string) (string, bool) {
	switch kind {
	case "wix":
		v, ok := c.wixVars[name]

		return c.expandVars(c.pkg, v), ok
	case "bind":
		switch name {
		case "Property.ProductName":
			return c.expandVars(c.pkg, c.pkg.attr("Name")), c.pkg.has("Name")
		case "Property.Manufacturer":
			return c.expandVars(c.pkg, c.pkg.attr("Manufacturer")), c.pkg.has("Manufacturer")
		}
	}

	return "", false
}

// dirRef returns a directory ID as the output names it.
func (c *converter) dirRef(id string) string {
	if to, ok := c.renames[id]; ok {
		return to
	}

	return id
}

// noteDirRefs marks the directories that formatted text refers to, so
// that they keep their IDs in the output.
func (c *converter) noteDirRefs(s string) {
	for _, m := range bracketRef.FindAllStringSubmatch(s, -1) {
		c.needDirs[m[1]] = true
	}
}

func isStandardFolder(id string) bool {
	return config.IsStandardFolder(id) || id == folderProgramFiles6432 ||
		id == "CommonFiles6432Folder" || id == "System6432Folder"
}

// path turns a WiX source path into a slash path.
func sourcePath(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	s = strings.TrimPrefix(s, "SourceDir/")

	return s
}

// baseName is the last element of a slash path.
func baseName(p string) string {
	if _, after, ok := strings.CutLast(p, "/"); ok {
		return after
	}

	return p
}
