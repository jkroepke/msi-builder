package wixconvert

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// staticDefine is the value a variable takes at a <?define?> outside the
// preprocessor blocks.
type staticDefine struct {
	name  string
	value string
	line  int
}

// fillRedefined fills in, where an element or an instruction reads a
// variable that the file defines more than once, the value the variable
// has there. WiX reads a variable at the point it sits in the file; the
// vars header holds one value, the last one, for every reader. Every
// reader is filled in, also those after the last define, so that the IDs
// that two readers write the same way still match; WiX's define wins over
// -d, as here over --var.
//
// Only the variables that the defines outside the blocks set are filled
// in: the conversion of the blocks follows the others. It also records
// the last values of those variables for staticFill.
func (c *converter) fillRedefined() {
	defines, final := c.staticDefines()
	c.staticVars, c.staticDefs = final, defines

	defer c.fillStandardFolders()

	fill := redefinedFill{defines: defines, last: lastDefines(defines), open: make(map[fixedAt]bool)}
	if len(fill.last) == 0 {
		return
	}

	walkElems(c.doc.root, func(elm *elem) {
		loops := posLoops(elm.pos)

		for name, value := range elm.attrs {
			elm.attrs[name] = fill.at(value, elm.line, false, loops)
		}

		elm.text = fill.at(elm.text, elm.line, false, loops)
	})

	branch := make(map[*ppBlock]int)

	for i := range c.doc.procs {
		proc := &c.doc.procs[i]

		switch proc.target {
		case piIf, piElseif, piForeach:
			proc.inst = fill.at(proc.inst, proc.line, proc.target != piForeach, posLoops(proc.pos))
			proc.setBranch(branch[proc.block])
			branch[proc.block]++
		case piIfdef, piIfndef, piElse:
			branch[proc.block]++
		case "error", "warning":
			proc.inst = fill.at(proc.inst, proc.line, false, posLoops(proc.pos))
		}
	}

	for _, open := range slices.SortedFunc(maps.Keys(fill.open), func(a, b fixedAt) int {
		return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(a.name, b.name))
	}) {
		c.todof(nil, "", open.at, "this line defines %s and reads it, and the vars header holds its last value; "+
			"write the value WiX reads here", open.name)
	}
}

// fillStandardFolders fills in each attribute that is just a variable,
// and each [$(var.X)] reference, whose one value is a standard folder,
// such as $(var.PF) set to ProgramFilesFolder: such a value is a
// directory reference, and the converter knows standard folders, their
// platform and their roots by name. All readers are filled in, so that
// the literal and the variable name the same folder.
func (c *converter) fillStandardFolders() {
	walkElems(c.doc.root, func(elm *elem) {
		loops := posLoops(elm.pos)
		fill := func(text string, loops []string) string {
			return replaceVars(text, func(name string) (string, bool) {
				v, ok := c.staticVars[name]

				// A reader on the line of a define is noted and left.
				_, placed := valueAt(c.staticDefs, name, elm.line)

				return v, ok && placed && isStandardFolder(v) && !slices.Contains(loops, name)
			})
		}

		for name, value := range elm.attrs {
			if preprocessorVar.FindString(value) == value {
				elm.attrs[name] = fill(value, loops)

				continue
			}

			elm.attrs[name] = bracketVar.ReplaceAllStringFunc(value, func(ref string) string { return fill(ref, loops) })
		}
	})
}

// bracketVar matches a formatted-text reference to a variable: [$(var.X)].
var bracketVar = regexp.MustCompile(`\[\$\((?:var\.)?[A-Za-z_][A-Za-z0-9_.]*\)\]`)

// posLoops returns the loop variables of the <?foreach?> blocks in pos,
// which hide defines of the same name.
func posLoops(pos []ppPos) []string {
	var loops []string

	for _, inside := range pos {
		if !inside.block.foreach {
			continue
		}

		// planForeach sets loopVar later; the name is the same.
		if name, _, ok := strings.Cut(inside.block.branches[0].inst, " in "); ok {
			loops = append(loops, varName(strings.TrimSpace(name)))
		}
	}

	return loops
}

// lastDefines returns the line of the last define of each variable that
// the defines set more than once.
func lastDefines(defines []staticDefine) map[string]int {
	last, count := make(map[string]int), make(map[string]int)

	for _, def := range defines {
		last[def.name] = def.line
		count[def.name]++
	}

	for name, defined := range count {
		if defined < 2 {
			delete(last, name)
		}
	}

	return last
}

// redefinedFill fills in the values of redefined variables.
type redefinedFill struct {
	// last is the line of the last define of each redefined variable.
	last map[string]int
	// open are the readers on the line of a define, which are left as
	// they are.
	open    map[fixedAt]bool
	defines []staticDefine
}

// at fills in the values that the redefined variables in text have at
// line. In a condition, a value outside quotes
// is quoted, so that it stays one operand.
func (f redefinedFill) at(text string, line int, condition bool, loops []string) string {
	return replaceVarsQuoted(text, func(name string, quoted bool) (string, bool) {
		if _, ok := f.last[name]; !ok || slices.Contains(loops, name) {
			return "", false
		}

		value, ok := valueAt(f.defines, name, line)

		switch {
		case !ok && definedOn(f.defines, name, line):
			f.open[fixedAt{name, line}] = true

			return "", false
		case !ok:
			return "", false
		case condition && !quoted && preprocessorVar.FindString(value) != value:
			return `"` + value + `"`, !strings.Contains(value, `"`)
		}

		return value, true
	})
}

// valueAt returns the value a variable has at line: the value of the last
// define before it. A define on the line itself leaves it open whether the
// reader comes before or after it, unless it is the variable's only define
// up to there: a reader before it would read an undefined variable.
func valueAt(defines []staticDefine, name string, line int) (string, bool) {
	var before, onLine []string

	for _, def := range defines {
		switch {
		case def.name != name || def.line > line:
		case def.line == line:
			onLine = append(onLine, def.value)
		default:
			before = append(before, def.value)
		}
	}

	switch {
	case len(onLine) == 0 && len(before) > 0:
		return before[len(before)-1], true
	case len(onLine) == 1 && len(before) == 0:
		return onLine[0], true
	}

	return "", false
}

// definedOn reports whether a define of the variable sits on line.
func definedOn(defines []staticDefine, name string, line int) bool {
	return slices.ContainsFunc(defines, func(def staticDefine) bool { return def.name == name && def.line == line })
}

// setBranch copies the instruction of a procInst that opens branch i of
// its block into the block.
func (p *procInst) setBranch(i int) {
	if p.block != nil && i < len(p.block.branches) && p.block.branches[i].target == p.target {
		p.block.branches[i].inst = p.inst
	}
}

// staticDefines returns the defines outside the blocks, in file order,
// with the values of earlier defines filled in, and the last value of each
// variable they set. A variable that a define in a block sets, or that a
// define sets from such a variable, is left out.
func (c *converter) staticDefines() ([]staticDefine, map[string]string) {
	inBlock := c.definedInBlocks()

	var defines []staticDefine

	final := make(map[string]string)

	for _, proc := range c.doc.procs {
		name := defineName(proc)
		if name == "" || proc.skipped || inBlock[name] {
			continue
		}

		_, value, _ := strings.Cut(proc.inst, "=")
		value = replaceVars(strings.Trim(strings.TrimSpace(value), `"`), func(ref string) (string, bool) {
			v, ok := final[ref]

			return v, ok
		})

		final[name] = value
		defines = append(defines, staticDefine{name: name, value: value, line: proc.line})
	}

	return defines, final
}

// definedInBlocks returns the variables that a define in a block sets,
// and those that a define sets from one of them.
func (c *converter) definedInBlocks() map[string]bool {
	inBlock := make(map[string]bool)

	for _, proc := range c.doc.procs {
		if name := defineName(proc); name != "" && !proc.skipped && len(proc.pos) > 0 {
			inBlock[name] = true
		}
	}

	for changed := true; changed; {
		changed = false

		for _, proc := range c.doc.procs {
			if name := defineName(proc); name != "" && !inBlock[name] && readsAny(proc, inBlock) {
				inBlock[name], changed = true, true
			}
		}
	}

	return inBlock
}

// readsAny reports whether a define that is not left out reads one of
// the variables.
func readsAny(proc procInst, names map[string]bool) bool {
	return !proc.skipped && slices.ContainsFunc(definedFrom(proc), func(ref string) bool { return names[ref] })
}

// staticFill fills in the variables that only defines outside the blocks
// set, for comparing names that one element writes with a variable and
// another without.
// Loop variables of the same name are left.
func (c *converter) staticFill(text string, loops []string) string {
	return replaceVars(text, func(name string) (string, bool) {
		v, ok := c.staticVars[name]

		return v, ok && !slices.Contains(loops, name)
	})
}

// replaceVars replaces the preprocessor variables ($(var.X) and $(X)) in
// text for which value returns true.
func replaceVars(text string, value func(name string) (string, bool)) string {
	return replaceVarsQuoted(text, func(name string, _ bool) (string, bool) { return value(name) })
}

// replaceVarsQuoted is replaceVars that tells value whether the variable
// sits inside a double-quoted string.
func replaceVarsQuoted(text string, value func(name string, quoted bool) (string, bool)) string {
	if !strings.Contains(text, "$(") {
		return text
	}

	var out strings.Builder

	last := 0

	for _, loc := range preprocessorVar.FindAllStringSubmatchIndex(text, -1) {
		out.WriteString(text[last:loc[0]])
		last = loc[1]

		kind := ""
		if loc[2] >= 0 {
			kind = text[loc[2]:loc[3]]
		}

		quoted := strings.Count(text[:loc[0]], `"`)%2 == 1

		if kind == varEnv || kind == varSys {
			out.WriteString(text[loc[0]:loc[1]])

			continue
		}

		if v, ok := value(varName(text[loc[4]:loc[5]]), quoted); ok {
			out.WriteString(v)
		} else {
			out.WriteString(text[loc[0]:loc[1]])
		}
	}

	out.WriteString(text[last:])

	return out.String()
}

// Elements whose names other files use too.
const (
	elemDirectoryRef = "DirectoryRef"
	elemSetDirectory = "SetDirectory"
)

// isStandard reports whether a directory ID is a standard folder, also
// one that a variable names (standardVar).
func (c *converter) isStandard(id string) bool {
	return isStandardFolder(id) || c.standardVar(id)
}

// standardVar reports whether a directory ID is a variable, such as
// $(var.ProgramFiles), that every define sets to a standard folder. The ID
// keeps the variable, which the output fills in as a template, so that a
// folder per build and --var still work.
func (c *converter) standardVar(id string) bool {
	refs := varRefs(id)
	if len(refs) != 1 || preprocessorVar.FindString(id) != id {
		return false
	}

	defined := false

	for _, proc := range c.doc.procs {
		if proc.skipped || defineName(proc) != refs[0] {
			continue
		}

		_, value, _ := strings.Cut(proc.inst, "=")
		if !config.IsStandardFolder(c.staticFill(strings.Trim(strings.TrimSpace(value), `"`), nil)) {
			return false
		}

		defined = true
	}

	return defined
}
