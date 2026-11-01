package wixconvert

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// noBranch is the chosen branch of a block when none applies.
const noBranch = -1

// buildArchShort are the values of $(sys.BUILDARCHSHORT) by platform.
var buildArchShort = map[string]string{platformX86: "X86", platformX64: "X64", platformArm64: "A64"} //nolint:gochecknoglobals // lookup table

// errPPUnknown reports a condition whose value depends on something a
// build passes in, such as a -d variable or the platform without --arch.
var errPPUnknown = errors.New("the value is not known before the build")

// ppFrame is a block that the default build is inside of.
type ppFrame struct {
	block *ppBlock
	// branch is the branch being read and taken the one that applies,
	// or noBranch.
	branch, taken int
	// active is set when the build reads the block at all; unknown is
	// set on a loop, whose variable takes each value in turn. guessed is
	// set when taken depends on a value convert does not know: it is
	// then the first branch not known to be false, which is the one
	// convert writes.
	active, unknown, guessed bool
	// arch is set when taken depends on the platform.
	arch bool
}

// ppSim follows the preprocessor as a build without -d variables runs it:
// the defines of the file and, with --arch, the platform. Where a
// condition reads anything else, it follows the branch convert writes,
// so that later blocks agree with it, and marks what depends on that.
type ppSim struct {
	values map[string]string
	// unknown are the variables a loop body may set; guessed the ones a
	// guessed branch set.
	unknown, guessed map[string]bool
	// defined are the variables some define of the file sets; whether a
	// build defines any other is up to its -d variables.
	defined map[string]bool
	// chosen are the branches that apply, by block, and guessedBlocks
	// the blocks whose branch depends on a guess.
	chosen        map[*ppBlock]int
	guessedBlocks map[*ppBlock]bool
	// archRead are the blocks whose condition reads the platform.
	archRead map[*ppBlock]bool
	// archVars are the variables whose value read the platform.
	archVars map[string]bool
	arch     string
	frames   []*ppFrame
	// readGuess and readArch are set when the condition or define being
	// evaluated read a guessed value or the platform.
	readGuess, readArch bool
}

// chooseBranches sets on each block the branch that a build without -d
// variables takes, which a block that is not converted as a template
// converts. A branch whose condition reads anything else, such as an
// undefined variable, is taken unless it is known to be false.
func (c *converter) chooseBranches() {
	if c.doc.firstBranch {
		return
	}

	sim := &ppSim{
		arch: c.arch, values: make(map[string]string), unknown: make(map[string]bool), guessed: make(map[string]bool),
		chosen: make(map[*ppBlock]int), guessedBlocks: make(map[*ppBlock]bool), archRead: make(map[*ppBlock]bool),
		defined: make(map[string]bool), archVars: make(map[string]bool),
	}

	for _, proc := range c.doc.procs {
		if name, _, ok := strings.Cut(proc.text, "="); ok && proc.target == piDefine {
			sim.defined[strings.TrimSpace(name)] = true
		}
	}

	for _, proc := range c.doc.procs {
		sim.step(proc)
	}

	for _, blk := range c.doc.blocks {
		if branch, ok := sim.chosen[blk]; ok && !blk.foreach {
			blk.chosen, blk.evaluated, blk.archRead = branch, !sim.guessedBlocks[blk], sim.archRead[blk]
		}
	}
}

// ppReading is whether the build reads the instructions at a point:
// reads when it does, maybe when it might in a loop, guess when that
// depends on a guess, and skipGuess when it skips them because of one.
type ppReading struct {
	reads, maybe, guess, skipGuess bool
	// arch is set when reading depends on the platform.
	arch bool
}

// reading reports whether the build reads the instructions here.
func (s *ppSim) reading() ppReading {
	var here ppReading

	for _, frame := range s.frames {
		switch {
		case !frame.active:
			return ppReading{}
		case frame.unknown:
			here.maybe = true
		case frame.taken != frame.branch:
			return ppReading{skipGuess: here.guess || frame.guessed, arch: here.arch || frame.arch}
		}

		here.guess = here.guess || frame.guessed
		here.arch = here.arch || frame.arch
	}

	here.reads = !here.maybe

	return here
}

// skip records an instruction that sets a variable and that the build
// does not run: a guess decided that, so the variable depends on it.
func (s *ppSim) skip(name string, here ppReading) {
	switch {
	case here.maybe:
		s.unknown[name] = true
	case here.skipGuess:
		s.guessed[name] = true
	}

	if here.arch {
		// Whether the build sets it depends on the platform.
		s.archVars[name] = true
	}
}

func (s *ppSim) step(proc procInst) { //nolint:cyclop // one branch per instruction
	here := s.reading()

	switch proc.target {
	case piDefine:
		name, value, ok := strings.Cut(proc.text, "=")
		if !ok {
			// A define without a value has no counterpart.
			s.unknown[strings.TrimSpace(name)] = true

			return
		}

		s.set(strings.TrimSpace(name), strings.Trim(strings.TrimSpace(value), `"`), here)
	case "undef":
		name := strings.TrimSpace(proc.text)
		if !here.reads {
			s.skip(name, here)

			return
		}

		delete(s.values, name)
		delete(s.unknown, name)
		s.archVars[name] = here.arch
		s.guessed[name] = here.guess
	case piIf, piIfdef, piIfndef, piForeach:
		if proc.block == nil {
			return
		}

		frame := &ppFrame{
			block: proc.block, taken: noBranch, active: here.reads || here.maybe,
			unknown: here.maybe || proc.block.foreach, guessed: here.guess, arch: here.arch,
		}

		s.frames = append(s.frames, frame)
		s.test(frame, proc.block.branches[0])
	case piElseif, piElse:
		frame := s.top(proc.block)
		if frame == nil {
			return
		}

		frame.branch++
		if frame.branch < len(proc.block.branches) {
			s.test(frame, proc.block.branches[frame.branch])
		}
	case piEndif, piEndforeach:
		frame := s.top(proc.block)
		if frame == nil {
			return
		}

		s.frames = s.frames[:len(s.frames)-1]
		if frame.active && !frame.unknown {
			s.chosen[frame.block] = frame.taken
			s.guessedBlocks[frame.block] = frame.guessed
			s.archRead[frame.block] = frame.arch
		}
	}
}

// top returns the innermost frame when it is blk's.
func (s *ppSim) top(blk *ppBlock) *ppFrame {
	if blk == nil || len(s.frames) == 0 || s.frames[len(s.frames)-1].block != blk {
		return nil
	}

	return s.frames[len(s.frames)-1]
}

// test evaluates the branch that frame reads now, unless an earlier one
// applies. A branch whose condition convert cannot evaluate is taken as
// a guess.
func (s *ppSim) test(frame *ppFrame, branch ppBranch) {
	if frame.unknown || frame.taken != noBranch {
		return
	}

	s.readGuess, s.readArch = false, false

	var (
		applies bool
		err     error
	)

	switch branch.target {
	case piElse:
		applies = true
	case piIfdef, piIfndef:
		applies, err = s.definedTest(branch)
	default:
		applies, err = s.condition(branch.inst)
	}

	frame.guessed = frame.guessed || s.readGuess
	frame.arch = frame.arch || s.readArch

	switch {
	case err != nil:
		frame.taken, frame.guessed = frame.branch, true
	case applies:
		frame.taken = frame.branch
	}
}

// definedTest evaluates an <?ifdef?> or <?ifndef?>.
func (s *ppSim) definedTest(branch ppBranch) (bool, error) {
	name := ppDefName(branch.inst)
	s.readGuess, s.readArch = s.guessed[name], s.archVars[name]
	_, defined := s.values[name]

	if s.unknown[name] || !s.defined[name] {
		return false, errPPUnknown
	}

	return defined == (branch.target == piIfdef), nil
}

// set records a define, which a build may or may not run.
func (s *ppSim) set(name, value string, here ppReading) {
	if !here.reads {
		s.skip(name, here)

		return
	}

	s.readGuess, s.readArch = false, false

	expanded, err := s.expand(value)
	if err != nil {
		s.unknown[name] = true

		return
	}

	s.values[name] = expanded
	s.guessed[name] = here.guess || s.readGuess
	s.archVars[name] = s.readArch || here.arch
	delete(s.unknown, name)
}

// expand fills the variables into text.
func (s *ppSim) expand(text string) (string, error) {
	var out strings.Builder

	last := 0

	for _, loc := range preprocessorVar.FindAllStringSubmatchIndex(text, -1) {
		kind := ""
		if loc[2] >= 0 {
			kind = text[loc[2]:loc[3]]
		}

		value, err := s.value(kind, text[loc[4]:loc[5]])
		if err != nil {
			return "", err
		}

		out.WriteString(text[last:loc[0]])
		out.WriteString(value)

		last = loc[1]
	}

	out.WriteString(text[last:])

	return out.String(), nil
}

// value returns the value of a variable as the build sees it.
// It records whether the value read a guess or the platform.
func (s *ppSim) value(kind, name string) (string, error) {
	switch kind {
	case "", varVar:
		value, ok := s.values[name]
		if !ok || s.unknown[name] {
			return "", fmt.Errorf("$(var.%s): %w", name, errPPUnknown)
		}

		s.readGuess = s.readGuess || s.guessed[name]
		s.readArch = s.readArch || s.archVars[name]

		return value, nil
	case varSys:
		// WiX 5 -arch x86, x64 and arm64 give BUILDARCH x86, x64 and
		// arm64 and BUILDARCHSHORT X86, X64 and A64 (seen on SERVER2025).
		value, ok := map[string]string{"BUILDARCH": s.arch, "BUILDARCHSHORT": buildArchShort[s.arch]}[name]
		if ok && s.arch != "" {
			s.readArch = true

			return value, nil
		}
	}

	return "", fmt.Errorf("$(%s.%s): %w", kind, name, errPPUnknown)
}

// condition evaluates the expression of an <?if?> or <?elseif?> as the
// template that ppCondition writes for it does.
func (s *ppSim) condition(text string) (bool, error) {
	tokens, err := ppTokens(text)
	if err != nil {
		return false, err
	}

	eval := &ppEvaluator{sim: s, tokens: tokens}

	result, err := eval.or()

	if err == nil && len(eval.tokens) > 0 {
		err = errPPSyntax
	}

	return result, err
}

// ppEvaluator evaluates an <?if?> expression with the grammar of ppParser.
type ppEvaluator struct {
	sim    *ppSim
	tokens []ppToken
}

func (e *ppEvaluator) peekKeyword(word string) bool {
	return len(e.tokens) > 0 && e.tokens[0].kind == ppWord && strings.EqualFold(e.tokens[0].text, word)
}

func (e *ppEvaluator) or() (bool, error) {
	return e.chain("or", e.and)
}

func (e *ppEvaluator) and() (bool, error) {
	return e.chain("and", e.unary)
}

// chain reads operands joined by a keyword; it reads them all, so that an
// unknown operand makes the whole expression unknown.
func (e *ppEvaluator) chain(keyword string, next func() (bool, error)) (bool, error) {
	// A known operand of the deciding value decides even when another
	// is unknown: false for and, true for or.
	deciding := keyword == "or"
	decided := false

	var unknown error

	for first := true; first || e.peekKeyword(keyword); first = false {
		if !first {
			e.tokens = e.tokens[1:]
		}

		operand, err := next()

		switch {
		case errors.Is(err, errPPUnknown):
			unknown = err
		case err != nil:
			return false, err
		case operand == deciding:
			decided = true
		}
	}

	switch {
	case decided:
		return deciding, nil
	case unknown != nil:
		return false, unknown
	}

	return !deciding, nil
}

func (e *ppEvaluator) unary() (bool, error) {
	if e.peekKeyword("not") {
		e.tokens = e.tokens[1:]

		operand, err := e.unary()

		return !operand, err
	}

	if len(e.tokens) == 0 {
		return false, errPPSyntax
	}

	if e.tokens[0].kind != ppOpen {
		return e.comparison()
	}

	e.tokens = e.tokens[1:]

	result, err := e.or()
	if err != nil {
		return false, err
	}

	if len(e.tokens) == 0 || e.tokens[0].kind != ppClose {
		return false, errPPSyntax
	}

	e.tokens = e.tokens[1:]

	return result, nil
}

// ppValue is an evaluated operand; number is set on a literal that is a
// whole number.
type ppValue struct {
	text   string
	isVar  bool
	number bool
}

// comparison reads a whole comparison, also when an operand is unknown,
// so that the operands after it are read.
func (e *ppEvaluator) comparison() (bool, error) {
	left, leftErr := e.operand()
	if leftErr != nil && !errors.Is(leftErr, errPPUnknown) {
		return false, leftErr
	}

	if len(e.tokens) == 0 || e.tokens[0].kind != ppOp {
		if !left.isVar {
			return false, errPPSyntax
		}

		// A variable on its own tests that it is not empty.
		return left.text != "", leftErr
	}

	oper := e.tokens[0].text
	e.tokens = e.tokens[1:]

	right, err := e.operand()

	switch {
	case err != nil:
		return false, err
	case leftErr != nil:
		return false, leftErr
	}

	return compareValues(oper, left, right), nil
}

func (e *ppEvaluator) operand() (ppValue, error) {
	if len(e.tokens) == 0 {
		return ppValue{}, errPPSyntax
	}

	tok := e.tokens[0]
	e.tokens = e.tokens[1:]

	switch tok.kind {
	case ppVar:
		m := preprocessorVar.FindStringSubmatch(tok.text)
		if m == nil || m[0] != tok.text {
			return ppValue{}, errPPSyntax
		}

		value, err := e.sim.value(m[1], m[2])

		return ppValue{text: value, isVar: true}, err
	case ppWord, ppString:
		if tok.kind == ppWord && isKeyword(tok.text) || strings.Contains(tok.text, "$(") {
			return ppValue{}, errPPSyntax
		}

		_, err := strconv.Atoi(tok.text)

		return ppValue{text: tok.text, number: err == nil}, nil
	case ppOp, ppOpen, ppClose:
	}

	return ppValue{}, errPPSyntax
}

// compareValues compares as compare's template does: case-sensitive but
// ~=, and as whole numbers when one side is a number literal, where a
// text that is no number counts as 0.
func compareValues(oper string, left, right ppValue) bool {
	switch oper {
	case "=":
		return left.text == right.text
	case "!=":
		return left.text != right.text
	case "~=":
		return strings.EqualFold(left.text, right.text)
	}

	order := strings.Compare(left.text, right.text)
	if left.number || right.number {
		one, _ := strconv.Atoi(left.text)
		other, _ := strconv.Atoi(right.text)
		order = cmp.Compare(one, other)
	}

	return slices.Contains(map[string][]int{"<": {-1}, "<=": {-1, 0}, ">": {1}, ">=": {0, 1}}[oper], order)
}
