package wixconvert

import (
	"regexp"
	"slices"
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
		if parts[1] == varSys {
			c.todof(nil, "", elm.line, "%s is a WiX system variable; replace it", m)

			return m
		}

		var loops []string
		if elm != nil {
			loops = elm.loops
		}

		return "{{ " + c.varOperand(parts[2], loops) + " }}"
	})
}

// expandLeft fills in the variables of a value that convert wrote as it
// is in the source, such as an Id, inside the loops of its entry.
func (c *converter) expandLeft(text string, loops []string) string {
	found := false

	for _, m := range preprocessorVar.FindAllStringSubmatch(text, -1) {
		found = found || m[1] != varSys
	}

	if !found {
		return text
	}

	if !action.MatchString(text) {
		text = escapeTemplate(text)
	}

	return preprocessorVar.ReplaceAllStringFunc(text, func(m string) string {
		parts := preprocessorVar.FindStringSubmatch(m)
		if parts[1] == varSys {
			return m
		}

		return "{{ " + c.varOperand(parts[2], loops) + " }}"
	})
}

// expandLeftOut fills in the variables left in the output's values and
// keys.
func (c *converter) expandLeftOut(value any, loops []string) any {
	switch val := value.(type) {
	case string:
		return c.expandLeft(val, loops)
	case []string:
		for i, s := range val {
			val[i] = c.expandLeft(s, loops)
		}
	case condItem:
		val.value = c.expandLeft(val.value, append(slices.Clone(loops), opensLoops(val.opens)...))

		return val
	case []any:
		for i, item := range val {
			val[i] = c.expandLeftOut(item, loops)
		}
	case *omap:
		if val != nil {
			c.expandLeftMap(val, append(slices.Clone(loops), opensLoops(val.opens)...))
		}
	}

	return value
}

func (c *converter) expandLeftMap(mapping *omap, loops []string) {
	for i, key := range mapping.keys {
		keyLoops := append(slices.Clone(loops), opensLoops(mapping.keyOpens[key])...)

		value, ok := mapping.values[key]
		if ok {
			mapping.values[key] = c.expandLeftOut(value, keyLoops)
		}

		display, suffix, cut := strings.Cut(key, keySep)
		if renamed := c.expandLeft(display, keyLoops); renamed != display {
			if cut {
				renamed += keySep + suffix
			}

			mapping.keys[i] = renamed
			c.renameKey(mapping, key, renamed)
		}
	}
}

// renameKey moves the value, comments and actions of a key.
func (c *converter) renameKey(mapping *omap, from, dest string) {
	if v, ok := mapping.values[from]; ok {
		delete(mapping.values, from)
		mapping.values[dest] = v
	}

	if v, ok := mapping.comments[from]; ok {
		delete(mapping.comments, from)
		mapping.comments[dest] = v
	}

	if v, ok := mapping.keyOpens[from]; ok {
		delete(mapping.keyOpens, from)
		mapping.keyOpens[dest] = v
	}
}

// opensLoops returns the loop variables that opens bring in.
func opensLoops(opens []openRef) []string {
	var loops []string

	for _, open := range opens {
		if name, ok := strings.CutPrefix(open.Action, "range $"); ok {
			name, _, _ = strings.Cut(name, " ")
			loops = append(loops, name)
		}
	}

	return loops
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
	return config.IsStandardFolder(id) || id == "ProgramFiles6432Folder" ||
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
