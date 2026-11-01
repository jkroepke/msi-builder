package wixconvert

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Elements that blocks may hold.
const (
	elemComponent         = "Component"
	elemFile              = "File"
	elemRegistryKey       = "RegistryKey"
	elemRegistryValue     = "RegistryValue"
	elemEnvironment       = "Environment"
	elemServiceInstall    = "ServiceInstall"
	elemServiceControl    = "ServiceControl"
	elemServiceDependency = "ServiceDependency"
	elemCreateFolder      = "CreateFolder"
	elemRemoveFile        = "RemoveFile"
	elemRemoveFolder      = "RemoveFolder"
)

// Preprocessor instructions.
const (
	piDefine     = "define"
	piIf         = "if"
	piIfdef      = "ifdef"
	piIfndef     = "ifndef"
	piElseif     = "elseif"
	piElse       = "else"
	piEndif      = "endif"
	piForeach    = "foreach"
	piEndforeach = "endforeach"
)

// varSys is the kind of WiX system variables, $(sys.NAME).
const varSys = "sys"

// ppBlock is an <?if?> or <?foreach?> block of the source.
type ppBlock struct {
	// list is the template operand of a <?foreach?> list and loopVar
	// its variable.
	list    string
	loopVar string
	// pos are the blocks around this one.
	pos      []ppPos
	branches []ppBranch
	// conds are the template conditions of the branches; the one of an
	// <?else?> is empty.
	conds []string
	line  int
	// keep is set when every branch is converted, each inside the
	// template action that tests it. Otherwise only the first branch is.
	keep bool
	// elems is set when an element sits in the block.
	elems   bool
	foreach bool
}

// ppBranch is the instruction that opens a branch: if, ifdef, ifndef,
// elseif, else or foreach.
type ppBranch struct {
	target string
	inst   string
	line   int
}

// ppPos is a branch of a block.
type ppPos struct {
	block  *ppBlock
	branch int
}

// pruned reports whether pos lies in a branch that is left out.
func pruned(pos []ppPos) bool {
	return slices.ContainsFunc(pos, func(p ppPos) bool { return !p.block.keep && p.branch > 0 })
}

// keptLoops returns the loop variables of the kept <?foreach?> blocks in pos.
func keptLoops(pos []ppPos) []string {
	var loops []string

	for _, p := range pos {
		if p.block.keep && p.block.foreach {
			loops = append(loops, p.block.loopVar)
		}
	}

	return loops
}

// openRef is the template action that opens a kept branch.
type openRef struct {
	// Action opens the branch on its own, such as `if eq .Arch "x64"`.
	Action string `json:"a"`
	// Else continues the branch before it, such as `else if ...` or
	// `else`; it is empty for the first branch and for a loop.
	Else   string `json:"e,omitempty"`
	Block  int    `json:"b"`
	Branch int    `json:"i"`
}

// opensOf returns the template actions of the kept blocks in pos.
func (c *converter) opensOf(pos []ppPos) []openRef {
	var opens []openRef

	for _, p := range pos {
		if !p.block.keep {
			continue
		}

		opens = append(opens, c.openOf(p))
	}

	return opens
}

func (c *converter) openOf(pos ppPos) openRef {
	blk := pos.block
	ref := openRef{Block: slices.Index(c.doc.blocks, blk), Branch: pos.branch}

	if blk.foreach {
		ref.Action = fmt.Sprintf("range $%s := split %q %s", blk.loopVar, ";", blk.list)

		return ref
	}

	cond := blk.conds[pos.branch]
	if pos.branch == 0 {
		ref.Action = "if " + cond

		return ref
	}

	notBefore := "not " + paren(blk.conds[0])

	if pos.branch > 1 {
		parts := make([]string, pos.branch)
		for i := range parts {
			parts[i] = paren(blk.conds[i])
		}

		notBefore = "not (or " + strings.Join(parts, " ") + ")"
	}

	if cond == "" {
		ref.Action, ref.Else = "if "+notBefore, piElse
	} else {
		ref.Action, ref.Else = "if and ("+notBefore+") "+paren(cond), "else if "+cond
	}

	return ref
}

// paren puts an expression that is more than one operand in parentheses.
func paren(expr string) string {
	if strings.ContainsAny(expr, " ") {
		return "(" + expr + ")"
	}

	return expr
}

// relOpens returns the opens of elm that its container's output does not
// sit in already.
func relOpens(elm, container *elem) []openRef {
	if container == nil || len(container.opens) > len(elm.opens) {
		return elm.opens
	}

	return elm.opens[len(container.opens):]
}

// planBlocks decides which blocks are converted as templates and leaves
// out the branches of the others but the first.
func (c *converter) planBlocks() {
	for _, blk := range c.doc.blocks {
		if blk.foreach && blk.keep {
			c.planForeach(blk)
		}
	}

	c.checkDirect(c.doc.root, nil)
	c.checkCollisions()
	c.translateKept()
	c.prune(c.doc.root)
	c.markProcs()
}

// translateKept translates the conditions of the kept blocks, outer
// blocks first, so that a block inside a kept loop reads its variable.
// A block whose conditions convert cannot read is not kept.
func (c *converter) translateKept() {
	for _, blk := range c.doc.blocks {
		if !blk.keep || pruned(blk.pos) {
			continue
		}

		c.ppUsed = make(map[string]bool)
		if err := c.translate(blk); err != nil || c.redefinedAfter(blk) {
			blk.keep = false

			continue
		}

		maps.Copy(c.usedVars, c.ppUsed)
	}

	c.ppUsed = c.usedVars
}

// markProcs marks the instructions and extension attributes of left-out
// branches.
func (c *converter) markProcs() {
	c.doc.extAttrs = slices.DeleteFunc(c.doc.extAttrs, func(a extAttr) bool { return pruned(a.e.pos) })
	for i := range c.doc.procs {
		proc := &c.doc.procs[i]
		if proc.skipped || c.doc.blocks == nil {
			continue
		}

		proc.skipped = pruned(proc.pos)
		if proc.block != nil && !proc.block.keep && (proc.target == piElseif || proc.target == piElse) {
			proc.leftOut = !proc.skipped
		}
	}
}

// planForeach keeps a <?foreach?> only when it defines nothing: a define
// in a loop has no counterpart in the vars.
func (c *converter) planForeach(blk *ppBlock) {
	inst := blk.branches[0].inst

	name, list, ok := strings.Cut(inst, " in ")
	name = strings.TrimSpace(name)

	if !ok || !isName(name) {
		blk.keep = false

		return
	}

	blk.loopVar = varName(name)

	for _, proc := range c.doc.procs {
		if proc.target == piDefine && slices.ContainsFunc(proc.pos, func(p ppPos) bool { return p.block == blk }) {
			blk.keep = false

			return
		}
	}

	blk.list = strings.TrimSpace(list)
}

func isName(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return r != '_' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) < 0
}

// checkDirect keeps a block only when convert can carry the condition of
// each element that sits directly in one of its branches into the output.
func (c *converter) checkDirect(elm, parent *elem) {
	start := 0
	if parent != nil {
		start = len(parent.pos)
	}

	for _, p := range elm.pos[start:] {
		p.block.elems = true
	}

	if len(elm.pos) > start && !directAllowed(elm, parent) {
		for _, p := range elm.pos[start:] {
			p.block.keep = false
		}
	}

	for _, kid := range elm.kids {
		c.checkDirect(kid, elm)
	}
}

// componentContent are the elements that a component writes into its own
// entry, by the elements they may hold.
var componentContent = map[string][]string{ //nolint:gochecknoglobals // lookup table
	elemComponent: {
		elemFile, elemRegistryValue, elemRegistryKey, elemEnvironment, elemRemoveFile, elemRemoveFolder,
		elemCreateFolder, elemServiceInstall, elemServiceControl, elemCondition,
	},
	elemFile:              {elemShortcut},
	elemRegistryKey:       {elemRegistryValue, elemRegistryKey},
	elemRegistryValue:     {"MultiStringValue"},
	elemServiceInstall:    {elemServiceDependency},
	elemEnvironment:       nil,
	elemRemoveFile:        nil,
	elemRemoveFolder:      nil,
	elemCreateFolder:      nil,
	elemServiceControl:    nil,
	elemShortcut:          nil,
	elemServiceDependency: nil,
	"MultiStringValue":    nil,
	elemCondition:         nil,
}

// contained reports whether everything elm converts to stays in its own
// entry.
func contained(elm *elem) bool {
	allowed, ok := componentContent[elm.name]
	if !ok || elm.ns != nsCore {
		return false
	}

	for _, kid := range elm.kids {
		if !slices.Contains(allowed, kid.name) || !contained(kid) {
			return false
		}
	}

	return true
}

// directAllowed reports whether an element in a branch converts to output
// that convert can put inside the branch's template action.
func directAllowed(elm, parent *elem) bool {
	if elm.ns != nsCore || parent == nil {
		return false
	}

	switch elm.name {
	case elemComponent:
		return contained(elm)
	case "DirectoryRef":
		// A reference that only places components, such as one per
		// language in a <?foreach?>.
		return placesComponents(elm)
	}

	parents, ok := directParents[elm.name]
	if !ok || !slices.Contains(parents, parent.name) {
		return false
	}

	switch elm.name {
	case attrProperty:
		return plainProperty(elm)
	case refComponent, refComponentGroup:
		return true
	}

	return componentResource(elm, parent)
}

// componentResource reports whether a resource in a branch of a
// component converts to an entry of its own. The component's keyPath, or
// its Id taken from its first resource, would name an entry that a build
// leaves out.
func componentResource(elm, parent *elem) bool {
	return !holdsKeyPath(elm) && parent.has("Id") && contained(elm)
}

// The references that put components in features and groups.
const (
	refComponent      = "ComponentRef"
	refComponentGroup = "ComponentGroupRef"
)

// memberParents are the elements that list components.
var memberParents = []string{"Feature", "FeatureRef", "ComponentGroup", "FeatureGroup"} //nolint:gochecknoglobals // lookup table

// placesComponents reports whether a DirectoryRef holds components and
// nothing else.
func placesComponents(elm *elem) bool {
	return len(elm.kids) > 0 && !slices.ContainsFunc(elm.kids, func(kid *elem) bool {
		return !kid.is(elemComponent) || !contained(kid)
	})
}

// directParents are the elements, other than components, that may sit
// in a branch, by the elements they may sit in.
var directParents = map[string][]string{ //nolint:gochecknoglobals // lookup table
	elemFile:          {elemComponent},
	elemRegistryValue: {elemComponent},
	elemRegistryKey:   {elemComponent},
	elemEnvironment:   {elemComponent},
	refComponent:      memberParents,
	refComponentGroup: memberParents,
	attrProperty:      {"Package", "Product", "Fragment"},
}

// plainProperty reports whether a Property only becomes a properties
// entry: one with a value that no other key reads.
func plainProperty(elm *elem) bool {
	propID := elm.attr("Id")
	if !elm.has("Value") || len(elm.kids) > 0 || strings.HasPrefix(propID, "WIXUI_") || quietExecProperties[propID] {
		return false
	}

	if _, ok := arpKeys[propID]; ok {
		return false
	}

	switch propID {
	case propAllUsers, propInstallPerUser, "ApplicationFolderName", "WixShellExecTarget":
		return false
	}

	return true
}

// collidable are the elements whose Id may repeat in branches that never
// apply together, such as one File per platform.
var collidable = []string{elemFile, elemRegistryValue, attrProperty} //nolint:gochecknoglobals // lookup table

// checkCollisions leaves out the other branches of blocks that would
// define an Id twice for one build.
func (c *converter) checkCollisions() {
	byID := make(map[string][]*elem)
	definitions(c.doc.root, byID)

	for _, key := range slices.Sorted(maps.Keys(byID)) {
		elems := byID[key]
		if len(elems) < 2 || slices.Contains(collidable, elems[0].name) && exclusive(elems) {
			continue
		}

		for _, elm := range elems {
			for _, p := range elm.pos {
				p.block.keep = false
			}
		}
	}
}

// definitions collects the elements below elm that define an Id, by
// element name and Id.
func definitions(elm *elem, byID map[string][]*elem) {
	if id := elm.attr("Id"); id != "" && !strings.HasSuffix(elm.name, "Ref") && elm.ns == nsCore {
		key := elm.name + "\x00" + id
		byID[key] = append(byID[key], elm)
	}

	for _, kid := range elm.kids {
		definitions(kid, byID)
	}
}

// exclusive reports whether no two of the elements apply in one build:
// each pair sits in other branches of one block.
func exclusive(elems []*elem) bool {
	for i, one := range elems {
		for _, other := range elems[i+1:] {
			apart := false

			for _, p := range one.pos {
				for _, q := range other.pos {
					if p.block == q.block && !p.block.foreach && p.branch != q.branch {
						apart = true
					}
				}
			}

			if !apart {
				return false
			}
		}
	}

	return true
}

// prune drops the elements of left-out branches and sets the template
// actions of the others.
func (c *converter) prune(elm *elem) {
	elm.opens = c.opensOf(elm.pos)
	elm.loops = keptLoops(elm.pos)

	elm.kids = slices.DeleteFunc(elm.kids, func(kid *elem) bool { return pruned(kid.pos) })
	for _, kid := range elm.kids {
		c.prune(kid)
	}
}

// translate turns the conditions of a block into template expressions.
func (c *converter) translate(blk *ppBlock) error {
	loops := keptLoops(blk.pos)

	if blk.foreach {
		list, err := c.ppOperand(blk.list, loops)
		if err != nil {
			return err
		}

		blk.list = list

		return nil
	}

	blk.conds = make([]string, len(blk.branches))

	for i, branch := range blk.branches {
		var err error

		switch branch.target {
		case piIf, piElseif:
			blk.conds[i], err = c.ppCondition(branch.inst, loops)
		case piIfdef, piIfndef:
			blk.conds[i], err = c.ppDefined(blk, branch)
		case piElse:
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// ppDefined turns an <?ifdef?> or <?ifndef?> into a has condition.
func (c *converter) ppDefined(blk *ppBlock, branch ppBranch) (string, error) {
	name := ppDefName(branch.inst)
	if !isName(name) {
		return "", fmt.Errorf("<?%s %s?>: not a variable name", branch.target, branch.inst)
	}

	if c.definesVar(varName(name)) && !defaultBlock(blk, varName(name), c.doc.procs) {
		// has is true for a variable the vars header defines, also in
		// builds where the define did not run.
		return "", fmt.Errorf("<?%s %s?>: the file defines the variable", branch.target, branch.inst)
	}

	cond := fmt.Sprintf("has %q", varName(name))
	if branch.target == piIfndef {
		cond = "not (" + cond + ")"
	}

	return cond, nil
}

// ppDefName returns the variable an <?ifdef?> names: Name, var.Name or
// $(var.Name).
func ppDefName(inst string) string {
	inst = strings.TrimSpace(inst)
	if m := preprocessorVar.FindStringSubmatch(inst); m != nil && m[0] == inst && m[1] != varSys {
		return m[2]
	}

	inst = strings.TrimPrefix(inst, "var.")

	return strings.TrimPrefix(inst, "env.")
}

// ppOperand turns the list of a <?foreach?> into a template operand.
func (c *converter) ppOperand(text string, loops []string) (string, error) {
	if strings.Contains(text, "$(sys.") {
		return "", errors.New("a system variable has no counterpart")
	}

	locs := preprocessorVar.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return strconv.Quote(text), nil
	}

	var operands []string

	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			operands = append(operands, strconv.Quote(text[last:loc[0]]))
		}

		operands = append(operands, c.varOperand(text[loc[4]:loc[5]], loops))
		last = loc[1]
	}

	if last < len(text) {
		operands = append(operands, strconv.Quote(text[last:]))
	}

	if len(operands) == 1 {
		return operands[0], nil
	}

	return "(print " + strings.Join(operands, " ") + ")", nil
}

// varOperand returns the template operand that reads a variable: the
// loop variable of an enclosing kept <?foreach?>, or a variable, through
// $ inside a loop, where the dot is the loop's element.
func (c *converter) varOperand(name string, loops []string) string {
	name = varName(name)
	if slices.Contains(loops, name) {
		return "$" + name
	}

	c.ppUsed[name] = true

	if len(loops) > 0 {
		return "$." + name
	}

	return "." + name
}

// ppFail turns an <?error?> in a kept block into a template line that
// fails the build where the block's branch applies.
func (c *converter) ppFail(proc procInst) bool {
	opens := c.opensOf(proc.pos)
	if len(opens) == 0 || slices.ContainsFunc(proc.pos, func(p ppPos) bool { return !p.block.keep }) {
		// An <?error?> in a block that convert reads one branch of
		// would fail builds that WiX passes.
		return false
	}

	msg, err := c.ppOperand(proc.inst, keptLoops(proc.pos))
	if err != nil {
		return false
	}

	var line strings.Builder

	for _, open := range opens {
		line.WriteString("{{ " + open.Action + " }}")
	}

	line.WriteString("{{ fail " + msg + " }}")

	for range opens {
		line.WriteString("{{ end }}")
	}

	c.ppFails = append(c.ppFails, line.String())

	return true
}

// conditionalVars are the variables that a define in a kept block sets.
func (c *converter) conditionalVars() map[string]bool {
	names := make(map[string]bool)

	for _, proc := range c.doc.procs {
		if proc.skipped || proc.target != piDefine || len(c.opensOf(proc.pos)) == 0 {
			continue
		}

		if name, _, ok := strings.Cut(proc.inst, "="); ok {
			names[varName(strings.TrimSpace(name))] = true
		}
	}

	return names
}

// defineFrame is a kept <?if?> whose branches set variables.
type defineFrame struct {
	block *ppBlock
	// before are the values before the block.
	before map[string]string
	// branches are the values at the end of each branch read so far.
	branches []map[string]string
}

// defineEval follows the values of the variables through the
// instructions.
type defineEval struct {
	conv   *converter
	env    map[string]string
	frames []*defineFrame
}

// conditionalValues returns the values of the variables that kept blocks
// set, as templates that test the blocks' conditions. The last define
// wins, as in WiX.
func (c *converter) conditionalValues(names map[string]bool) map[string]string {
	eval := &defineEval{conv: c, env: make(map[string]string)}

	for _, proc := range c.doc.procs {
		if !proc.skipped {
			eval.step(proc)
		}
	}

	values := make(map[string]string)

	for name := range names {
		if v, ok := eval.env[name]; ok {
			values[name] = v
		}
	}

	return values
}

func (e *defineEval) step(proc procInst) {
	if proc.target == piDefine {
		e.define(proc)

		return
	}

	if proc.block == nil || !proc.block.keep || proc.block.foreach {
		return
	}

	switch proc.target {
	case piIf, piIfdef, piIfndef:
		e.frames = append(e.frames, &defineFrame{block: proc.block, before: maps.Clone(e.env)})
	case piElseif, piElse:
		if len(e.frames) > 0 {
			top := e.frames[len(e.frames)-1]
			top.branches = append(top.branches, e.env)
			e.env = maps.Clone(top.before)
		}
	case piEndif:
		if len(e.frames) > 0 {
			top := e.frames[len(e.frames)-1]
			e.frames = e.frames[:len(e.frames)-1]
			top.branches = append(top.branches, e.env)
			e.env = e.conv.mergeBranches(top)
		}
	}
}

// define sets a variable to its value as a template.
// A variable that has a value at this point takes that value, as in
// WiX, so that a define that extends a variable reads its earlier value.
func (e *defineEval) define(proc procInst) {
	name, value, ok := strings.Cut(proc.inst, "=")
	if !ok {
		return
	}

	value = strings.Trim(strings.TrimSpace(value), `"`)

	var text strings.Builder

	last := 0

	for _, loc := range preprocessorVar.FindAllStringSubmatchIndex(value, -1) {
		known, ok := e.env[varName(value[loc[4]:loc[5]])]
		if !ok || loc[2] >= 0 && value[loc[2]:loc[3]] != "var" {
			continue
		}

		text.WriteString(e.conv.expand(e.conv.pkg, e.conv.literal(value[last:loc[0]])))
		text.WriteString(known)

		last = loc[1]
	}

	text.WriteString(e.conv.expand(e.conv.pkg, e.conv.literal(value[last:])))
	e.env[varName(strings.TrimSpace(name))] = text.String()
}

// mergeBranches returns the values after a block: one template per
// variable that a branch changed.
func (c *converter) mergeBranches(frame *defineFrame) map[string]string {
	env := maps.Clone(frame.before)
	blk := frame.block
	changed := make(map[string]bool)

	for _, branch := range frame.branches {
		for name, v := range branch {
			if prior, ok := frame.before[name]; !ok || prior != v {
				changed[name] = true
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(changed)) {
		_, hadPrior := frame.before[name]

		// <?ifndef X?><?define X = ...?> is a default, which a header
		// var is: --var replaces it.
		if len(frame.branches) == 1 && blk.branches[0].target == piIfndef && !hadPrior &&
			varName(ppDefName(blk.branches[0].inst)) == name {
			env[name] = frame.branches[0][name]

			continue
		}

		env[name] = c.branchValue(frame, name)
	}

	return env
}

// branchValue returns the template that gives a variable the value of
// the branch that applies.
func (c *converter) branchValue(frame *defineFrame, name string) string {
	blk := frame.block
	prior, hadPrior := frame.before[name]
	hasElse := blk.branches[len(blk.branches)-1].target == piElse

	var text strings.Builder

	for i, branch := range frame.branches {
		value, ok := branch[name]
		if !ok {
			value = prior
		}

		switch {
		case i == 0:
			text.WriteString("{{ if " + blk.conds[0] + " }}")
		case blk.branches[i].target == piElse:
			text.WriteString("{{ else }}")
		default:
			text.WriteString("{{ else if " + blk.conds[i] + " }}")
		}

		text.WriteString(value)
	}

	switch {
	case hasElse:
	case hadPrior:
		text.WriteString("{{ else }}" + prior)
	default:
		c.todof(nil, "", blk.line, "the variable %s is empty when no branch of this <?%s?> applies, where WiX leaves it undefined", name, blk.branches[0].target)
	}

	text.WriteString("{{ end }}")

	return text.String()
}

// definesVar reports whether a <?define?> of the file sets the variable.
func (c *converter) definesVar(name string) bool {
	return slices.ContainsFunc(c.doc.procs, func(proc procInst) bool { return defineName(proc) == name })
}

// defineName returns the variable a <?define?> sets, or "".
func defineName(proc procInst) string {
	if proc.target != piDefine {
		return ""
	}

	name, _, ok := strings.Cut(proc.inst, "=")
	if !ok {
		return ""
	}

	return varName(strings.TrimSpace(name))
}

// defaultBlock reports whether blk is <?ifndef X?><?define X = ...?><?endif?>
// and nothing else, which becomes a default for X in the vars header.
func defaultBlock(blk *ppBlock, name string, procs []procInst) bool {
	if len(blk.branches) != 1 || blk.branches[0].target != piIfndef || blk.elems {
		return false
	}

	inside := 0

	for _, proc := range procs {
		if slices.ContainsFunc(proc.pos, func(p ppPos) bool { return p.block == blk }) {
			inside++

			if defineName(proc) != name {
				return false
			}
		}
	}

	return inside == 1
}

// redefinedAfter reports whether a variable that the conditions of blk
// read is defined again after the block starts: the template reads the
// last value, where WiX reads the one the variable has at the block.
func (c *converter) redefinedAfter(blk *ppBlock) bool {
	started := false

	for _, proc := range c.doc.procs {
		started = started || proc.block == blk
		if started && c.ppUsed[defineName(proc)] {
			return true
		}
	}

	return false
}

// holdsKeyPath reports whether elm or an element in it is a key path.
func holdsKeyPath(elm *elem) bool {
	return elm.yes("KeyPath") || slices.ContainsFunc(elm.kids, holdsKeyPath)
}
