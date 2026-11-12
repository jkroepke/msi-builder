package wixconvert

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// A <?foreach?> over a list that is the same in every build, written out
// or a <?define?> of the file, is expanded before converting when convert
// cannot write it as a template: when its body defines variables
// (FoundationDB) or when a reference in it cannot name a component of a
// loop (QTTabBar's ComponentRef Id="QTTabBarReg_$(var.arch)"). WiX expands
// it the same way, so the converted file needs no loop.

// foreachInst matches a <?foreach?> instruction.
var foreachInst = regexp.MustCompile(`<\?foreach\s+([A-Za-z_][A-Za-z0-9_.]*)\s+in\s+([^?]*)\?>`)

// loopInst matches a <?foreach?> or an <?endforeach?>.
var loopInst = regexp.MustCompile(`<\?(foreach|endforeach)\b[^?]*\?>`)

// markExpansion records a <?foreach?> block for expansion when its list is
// static: one that names no variable, or only variables that the file's
// own defines set (staticFill).
func (c *converter) markExpansion(blk *ppBlock) {
	if blk == nil || !blk.foreach {
		return
	}

	for _, proc := range c.doc.procs {
		if proc.block != blk || proc.target != piForeach {
			continue
		}

		_, list, ok := strings.Cut(proc.text, " in ")
		if !ok {
			return
		}

		list = c.staticFill(strings.TrimSpace(list), nil)
		if strings.Contains(list, "$(") {
			return
		}

		if c.expandLoops == nil {
			c.expandLoops = make(map[int]bool)
		}

		c.expandLoops[proc.line] = true

		return
	}
}

// markRefLoops marks the static <?foreach?> blocks around a reference that
// names no component or group because its Id uses the loop variable.
func (c *converter) markRefLoops(elm *elem, ref string) {
	if elm == nil || !strings.Contains(ref, "$(") && !strings.Contains(ref, "{{") {
		return
	}

	for _, p := range elm.pos {
		c.markExpansion(p.block)
	}
}

// expandStaticLoops writes out the <?foreach?> blocks that start on the
// given lines: one copy of the body per list item, with the loop variable
// replaced. Every copy but the last goes on the line of the <?foreach?>,
// without its line breaks, so that every element after the block keeps
// its line for the notes. values are the file's static variables, which
// fill the list, and arch the platform --arch names.
func expandStaticLoops(data []byte, lines map[int]bool, values map[string]string, arch string) []byte {
	text := string(data)

	out := rewriteLoops(text, func(blk loopBlock) (string, bool) {
		if !lines[strings.Count(text[:blk.start], "\n")+1] || readsLaterDefine(text[blk.start:], blk) {
			return "", false
		}

		list := replaceVars(blk.list, func(name string) (string, bool) {
			v, ok := values[name]

			return v, ok
		})

		return joinCopies(list, blk.body, func(item string) string {
			return resolveCopy(blk.body, blk.name, item, blk.outside, values, arch)
		}), true
	})

	return []byte(out)
}

// laterDefine matches a <?define?> or <?undef?> and the variable it sets.
var laterDefine = regexp.MustCompile(`<\?(?:define|undef)\s+([A-Za-z_][A-Za-z0-9_.]*)`)

// readsLaterDefine reports whether the list or body of blk reads a
// variable that a define after the loop (rest starts at the loop) sets:
// the file's static values hold the last value of each variable, which is
// not the one the loop sees. Such a loop is not expanded.
func readsLaterDefine(rest string, blk loopBlock) bool {
	after := rest[len(rest)-len(blk.outside)+blk.start:]
	read := "<?foreach " + blk.name + " in " + blk.list + "?>" + blk.body

	for _, m := range laterDefine.FindAllStringSubmatch(after, -1) {
		if readsVar(read, varName(m[1])) {
			return true
		}
	}

	return false
}

// loopBlock is a <?foreach?> block that rewriteLoops found.
type loopBlock struct {
	// name is the loop variable, list the list as written, body the text
	// between the instructions and outside the text around the block.
	name, list, body, outside string
	// start is the offset of the <?foreach?>.
	start int
}

// rewriteLoops replaces each outermost <?foreach?> block of text, through
// its <?endforeach?>, with what rewrite returns; a block for which it
// returns false, or one that is not closed, is kept.
func rewriteLoops(text string, rewrite func(loopBlock) (string, bool)) string {
	data := []byte(text)

	var out strings.Builder

	last := 0

	for _, loc := range loopInst.FindAllIndex(data, -1) {
		if loc[0] < last {
			continue
		}

		inst := foreachInst.FindSubmatch(data[loc[0]:loc[1]])
		if inst == nil {
			continue
		}

		end, ok := matchingEnd(data, loc[1])
		if !ok {
			continue
		}

		replaced, ok := rewrite(loopBlock{
			name: string(inst[1]), list: strings.TrimSpace(string(inst[2])), body: text[loc[1]:end[0]],
			outside: text[:loc[0]] + text[end[1]:], start: loc[0],
		})
		if !ok {
			continue
		}

		out.WriteString(text[last:loc[0]])
		out.WriteString(replaced)

		last = end[1]
	}

	out.WriteString(text[last:])

	return out.String()
}

// joinCopies writes one copy per item of list, separated by ;, from
// copyOf. Every copy but the last loses its line breaks; a loop without
// items keeps the line breaks of body.
func joinCopies(list, body string, copyOf func(item string) string) string {
	var items []string

	for item := range strings.SplitSeq(list, ";") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}

	if len(items) == 0 {
		return strings.Repeat("\n", strings.Count(body, "\n"))
	}

	var out strings.Builder

	for i, item := range items {
		copyText := copyOf(item)
		if i < len(items)-1 {
			copyText = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(copyText)
		}

		out.WriteString(copyText)
	}

	return out.String()
}

// matchingEnd returns the location of the <?endforeach?> that closes the
// <?foreach?> ending at from.
func matchingEnd(data []byte, from int) ([]int, bool) {
	depth := 1

	for _, loc := range loopInst.FindAllSubmatchIndex(data[from:], -1) {
		switch string(data[from+loc[2] : from+loc[3]]) {
		case piForeach:
			depth++
		case piEndforeach:
			depth--
			if depth == 0 {
				return []int{from + loc[0], from + loc[1]}, true
			}
		}
	}

	return nil, false
}

// copyInst matches the instructions that resolveCopy works out in a copy
// of a loop body.
var copyInst = regexp.MustCompile(`<\?(define|undef|ifdef|ifndef|if|elseif|else|endif|foreach|endforeach|include|error|warning)\b\s*([^?]*)\?>`)

// copyFrame is an <?if?> block of a loop body copy.
type copyFrame struct {
	// active is set when the build reads the branch; done when a branch
	// of the block was taken.
	active, done bool
}

// resolveCopy returns a copy of a loop body with the loop variable set to
// item. When every condition in it can be worked out from the item and
// the file's static variables, and every variable it defines is read
// nowhere else (outside is the rest of the file), it also takes the
// branches the build takes, fills in its own variables and drops their
// defines, as WiX does for each pass: FoundationDB names a folder and a
// component code per Python version that way. Otherwise only the loop
// variable is filled in, and the blocks are converted as usual.
func resolveCopy(body, loopVar, item, outside string, values map[string]string, arch string) string {
	plain := replaceVars(body, func(v string) (string, bool) { return item, varName(v) == varName(loopVar) })

	// A nested loop over a static list, such as QTTabBar's loop over its
	// classes inside the loop over x86;x64, is written out first.
	body, ok := unrollLoops(body, func(text string) string {
		return replaceVars(text, func(v string) (string, bool) {
			if varName(v) == varName(loopVar) {
				return item, true
			}

			value, known := values[varName(v)]

			return value, known
		})
	})
	if !ok {
		return plain
	}

	// Variables go by the names of the vars header, as values does, with
	// _ for the dots of names such as PyGUID2.7.
	body = replaceVars(body, func(v string) (string, bool) { return "$(var." + varName(v) + ")", true })

	resolved, ok := newCopySim(values, varName(loopVar), item, arch).resolve(body, outside)
	if !ok {
		return plain
	}

	return resolved
}

// copySim works out the instructions of one loop body copy.
type copySim struct {
	sim    *ppSim
	frames []copyFrame
	out    strings.Builder
}

// newCopySim starts a copy with the file's static variables and the loop
// variable set to item.
func newCopySim(values map[string]string, loopVar, item, arch string) *copySim {
	sim := &ppSim{
		values: make(map[string]string), unknown: make(map[string]bool), guessed: make(map[string]bool),
		defined: make(map[string]bool), archVars: make(map[string]bool), arch: arch,
	}

	maps.Copy(sim.values, values)
	sim.values[loopVar] = item

	return &copySim{sim: sim}
}

// active reports whether the build reads the text at this point.
func (cs *copySim) active() bool {
	return !slices.ContainsFunc(cs.frames, func(f copyFrame) bool { return !f.active })
}

// keep writes text that the build reads, or only its line breaks. false
// means it reads a variable the copy cannot fill.
func (cs *copySim) keep(text string) bool {
	if !cs.active() {
		cs.out.WriteString(strings.Repeat("\n", strings.Count(text, "\n")))

		return true
	}

	expanded, err := cs.sim.expand(text)
	if err != nil {
		// An element reads a -d variable, which the conversion handles
		// as a template, but not once the copy is resolved.
		return false
	}

	cs.out.WriteString(expanded)

	return true
}

// resolve returns body with its instructions worked out; false means it
// cannot be.
func (cs *copySim) resolve(body, outside string) (string, bool) {
	last := 0

	for _, loc := range copyInst.FindAllStringSubmatchIndex(body, -1) {
		if !cs.keep(body[last:loc[0]]) {
			return "", false
		}

		last = loc[1]

		target, inst := body[loc[2]:loc[3]], strings.TrimSpace(body[loc[4]:loc[5]])
		if !resolveInst(cs.sim, &cs.frames, target, inst, outside) {
			return "", false
		}

		// The instruction itself goes; keep its line breaks.
		cs.out.WriteString(strings.Repeat("\n", strings.Count(body[loc[0]:loc[1]], "\n")))
	}

	if !cs.keep(body[last:]) || len(cs.frames) > 0 {
		return "", false
	}

	return cs.out.String(), true
}

// resolveInst works out one instruction of a loop body copy; false means
// the copy cannot be resolved.
func resolveInst(sim *ppSim, frames *[]copyFrame, target, inst, outside string) bool { //nolint:cyclop // one branch per instruction
	reading := !slices.ContainsFunc(*frames, func(f copyFrame) bool { return !f.active })

	switch target {
	case piDefine, piUndef:
		name, value, _ := strings.Cut(inst, "=")
		name = varName(strings.TrimSpace(name))

		if readsVar(outside, name) {
			return false
		}

		if !reading {
			return true
		}

		if target == piUndef {
			delete(sim.values, name)

			return true
		}

		expanded, err := sim.expand(defineValue(value))
		if err != nil {
			return false
		}

		sim.values[name] = expanded
	case piIf, piIfdef, piIfndef:
		taken, ok := copyCondition(sim, target, inst, reading)
		if !ok {
			return false
		}

		*frames = append(*frames, copyFrame{active: taken, done: taken})
	case piElseif, piElse:
		if len(*frames) == 0 {
			return false
		}

		frame := &(*frames)[len(*frames)-1]
		if frame.done {
			frame.active = false

			return true
		}

		outer := !slices.ContainsFunc((*frames)[:len(*frames)-1], func(f copyFrame) bool { return !f.active })

		taken, ok := true, true
		if target == piElseif {
			taken, ok = copyCondition(sim, piIf, inst, outer)
		}

		frame.active, frame.done = taken, taken

		if !ok {
			return false
		}
	case piEndif:
		if len(*frames) == 0 {
			return false
		}

		*frames = (*frames)[:len(*frames)-1]
	default:
		// A nested loop, an include or an error: left to the conversion.
		return false
	}

	return true
}

// copyCondition evaluates an <?if?>, <?ifdef?> or <?ifndef?> of a copy;
// a block the build does not read is not evaluated.
func copyCondition(sim *ppSim, target, inst string, reading bool) (bool, bool) {
	if !reading {
		return false, true
	}

	if target != piIf {
		_, defined := sim.values[varName(strings.TrimSpace(inst))]

		return defined == (target == piIfdef), defined
	}

	taken, err := sim.condition(inst)

	return taken, err == nil
}

// ifdefName matches the variable an <?ifdef?> or <?ifndef?> tests.
var ifdefName = regexp.MustCompile(`<\?ifn?def\s+([A-Za-z_][A-Za-z0-9_.]*)\s*\?>`)

// readsVar reports whether text reads the preprocessor variable name,
// also by testing whether it is defined.
func readsVar(text, name string) bool {
	for _, m := range ifdefName.FindAllStringSubmatch(text, -1) {
		if varName(m[1]) == name {
			return true
		}
	}

	found := false

	replaceVars(text, func(v string) (string, bool) {
		found = found || varName(v) == name

		return "", false
	})

	return found
}

// unrollLoops writes out the <?foreach?> blocks of a loop body copy, one
// copy of each body per item, with the loop variable replaced; fill fills
// in the variables a list names. Every copy but the last loses its line
// breaks, as in expandStaticLoops. false means a list names a variable
// that fill cannot set.
func unrollLoops(body string, fill func(string) string) (string, bool) {
	static := true

	out := rewriteLoops(body, func(blk loopBlock) (string, bool) {
		list := fill(blk.list)
		if strings.Contains(list, "$(") {
			static = false

			return "", false
		}

		return joinCopies(list, blk.body, func(item string) string {
			copyText, ok := unrollLoops(replaceVars(blk.body, func(v string) (string, bool) { return item, varName(v) == varName(blk.name) }), fill)
			static = static && ok

			return copyText
		}), true
	})

	return out, static
}
