// Package sequence numbers the actions of a Windows Installer sequence table.
// Standard actions sit at fixed numbers; custom actions are scheduled before
// or after an anchor and get the numbers next to it, in definition order
// (docs/spec/MSISPEC.md sections 9.2 and 11.7). A chain that runs into
// another fixed number is an error, not a shift.
package sequence

import (
	"fmt"
	"slices"
	"strings"
)

// Limits of the Sequence column.
const (
	MaxSequence = 32767
	// MinTermination is the lowest termination number: termination actions
	// use -1 (success), -2 (user exit), -3 (fatal error) and -4 (suspend).
	MinTermination = -4
)

// Action is an action to schedule. Exactly one of Sequence, Before or After
// is set: Sequence fixes the number, Before and After name an anchor.
type Action struct {
	Name      string
	Condition string
	Before    string
	After     string
	Sequence  int
}

// Numbered is a scheduled action.
type Numbered struct {
	Name      string
	Condition string
	Sequence  int
}

// Error is a numbering error. Action names the action whose placement fails,
// so that a caller can point at the place that defines it; Other names a
// second action involved (the other holder of a number, or "" when there is
// none), which a caller may point at when Action is not one of its own.
type Error struct {
	Action string
	Other  string
	Reason string
	// Cycle lists the actions of a scheduling cycle in anchor order,
	// starting and ending with Action; nil for other errors.
	Cycle []string
}

func (e *Error) Error() string { return "sequence: " + e.Reason }

func errorf(action, other, format string, args ...any) *Error {
	return &Error{Action: action, Other: other, Reason: fmt.Sprintf(format, args...)}
}

// node is an action in the anchor graph. Every relative node has exactly one
// anchor, so the graph is a forest below the fixed nodes plus, when the
// schedule is wrong, cycles and the trees hanging off them.
type node struct {
	action Action
	anchor *node
	before []*node
	after  []*node
	index  int
	fixed  bool
}

// Number assigns sequence numbers. The result is sorted by number. Time and
// memory are linear in the number of actions (plus the final sort).
func Number(actions []Action) ([]Numbered, error) {
	nodes, ordered, err := graph(actions)
	if err != nil {
		return nil, err
	}

	result := make([]Numbered, 0, len(ordered))
	placed := make([]bool, len(ordered))

	for _, item := range ordered {
		if !item.fixed {
			continue
		}

		start := len(result)
		result = expand(result, placed, item)

		if err := numberBlock(result[start:], item); err != nil {
			return nil, err
		}
	}

	if len(result) != len(ordered) {
		return nil, cycle(nodes, ordered, placed)
	}

	slices.SortStableFunc(result, func(a, b Numbered) int { return a.Sequence - b.Sequence })

	for i := 1; i < len(result); i++ {
		if result[i].Sequence == result[i-1].Sequence {
			return nil, collision(nodes, result[i-1].Name, result[i].Name, result[i].Sequence)
		}
	}

	return result, nil
}

// graph checks each action on its own and links it to its anchor.
func graph(actions []Action) (map[string]*node, []*node, error) {
	nodes := make(map[string]*node, len(actions))
	ordered := make([]*node, 0, len(actions))

	for i, action := range actions {
		if err := checkAction(action); err != nil {
			return nil, nil, err
		}

		if _, dup := nodes[action.Name]; dup {
			return nil, nil, errorf(action.Name, "", "action %s scheduled twice", action.Name)
		}

		n := &node{action: action, fixed: action.Sequence != 0, index: i}
		nodes[action.Name] = n
		ordered = append(ordered, n)
	}

	for _, item := range ordered {
		if item.fixed {
			continue
		}

		anchorName := item.action.Before + item.action.After

		anchor, ok := nodes[anchorName]
		if !ok {
			return nil, nil, errorf(item.action.Name, "", "action %s is scheduled relative to %s, which is not in the table", item.action.Name, anchorName)
		}

		if anchor.fixed && anchor.action.Sequence < 0 {
			return nil, nil, errorf(item.action.Name, anchorName, "action %s cannot be scheduled relative to the termination action %s", item.action.Name, anchorName)
		}

		item.anchor = anchor

		if item.action.Before != "" {
			anchor.before = append(anchor.before, item)
		} else {
			anchor.after = append(anchor.after, item)
		}
	}

	return nodes, ordered, nil
}

func checkAction(action Action) error {
	if action.Name == "" {
		return errorf("", "", "action without a name")
	}

	set := 0

	for _, given := range []bool{action.Sequence != 0, action.Before != "", action.After != ""} {
		if given {
			set++
		}
	}

	if set != 1 {
		return errorf(action.Name, "", "action %s needs exactly one of a number, before or after", action.Name)
	}

	if action.Sequence != 0 && (action.Sequence < MinTermination || action.Sequence > MaxSequence) {
		return errorf(action.Name, "", "action %s: number %d is outside -4…-1 and 1…%d", action.Name, action.Sequence, MaxSequence)
	}

	return nil
}

// expand appends the chain of a fixed node to out in the order of section
// 11.7: expand(before…), the node, expand(after…). Each Numbered keeps the
// node's index in Sequence until numberBlock replaces it. An explicit stack
// keeps the depth of a long chain off the goroutine stack, and every node is
// visited once: nodes below a fixed node cannot be on a cycle.
func expand(out []Numbered, placed []bool, root *node) []Numbered {
	type frame struct {
		n    *node
		step int // 0: walking the before-list, 1: walking the after-list
		i    int
	}

	stack := []frame{{n: root}}

	for len(stack) > 0 {
		top := &stack[len(stack)-1]

		switch {
		case top.step == 0 && top.i < len(top.n.before):
			child := top.n.before[top.i]
			top.i++

			stack = append(stack, frame{n: child})
		case top.step == 0:
			placed[top.n.index] = true
			out = append(out, Numbered{Name: top.n.action.Name, Condition: top.n.action.Condition, Sequence: top.n.index})
			top.step, top.i = 1, 0
		case top.i < len(top.n.after):
			child := top.n.after[top.i]
			top.i++

			stack = append(stack, frame{n: child})
		default:
			stack = stack[:len(stack)-1]
		}
	}

	return out
}

// numberBlock numbers the chain of the fixed node item in place.
func numberBlock(chain []Numbered, item *node) error {
	pos := slices.IndexFunc(chain, func(n Numbered) bool { return n.Sequence == item.index })
	start := item.action.Sequence

	low := start - pos
	high := start + len(chain) - 1 - pos

	if low < 1 && start > 0 {
		return errorf(chain[0].Name, item.action.Name, "the actions before %s would need numbers below 1 (%d before it, from %s)",
			item.action.Name, pos, chain[0].Name)
	}

	if high > MaxSequence {
		last := chain[len(chain)-1].Name

		return errorf(last, item.action.Name, "the actions after %s would need numbers above %d (%d after it, up to %s)",
			item.action.Name, MaxSequence, len(chain)-1-pos, last)
	}

	for i := range chain {
		chain[i].Sequence = low + i
	}

	return nil
}

// maxShownCycle is the most actions a cycle error lists; Error.Cycle keeps
// them all.
const maxShownCycle = 10

// cycle reports a cycle among the nodes that no fixed node reached. Every
// such node leads through its anchors into a cycle, so following the anchors
// from the first one in definition order finds one; the action reported is
// the first of the cycle that walk meets, so an action that only hangs off
// the cycle is never blamed.
func cycle(nodes map[string]*node, ordered []*node, placed []bool) error {
	start := ordered[slices.Index(placed, false)]
	onPath := make(map[*node]int, len(nodes))

	var path []*node

	cur := start
	for {
		if at, seen := onPath[cur]; seen {
			path = path[at:]

			break
		}

		onPath[cur] = len(path)
		path = append(path, cur)
		cur = cur.anchor
	}

	names := make([]string, 0, len(path)+1)
	for _, n := range path {
		names = append(names, n.action.Name)
	}

	names = append(names, names[0])

	shown := strings.Join(names, " → ")
	if len(names) > maxShownCycle {
		shown = strings.Join(names[:maxShownCycle-1], " → ") + fmt.Sprintf(" → … (%d actions) → %s", len(path), names[0])
	}

	return &Error{
		Action: names[0],
		Cycle:  names,
		Reason: fmt.Sprintf("action %s is part of a scheduling cycle: %s", names[0], shown),
	}
}

// collision reports two actions that would get the same number. The action
// blamed is the one that moved there through an anchor rather than one with
// the number given; between two of a kind it is the one defined later.
func collision(nodes map[string]*node, first, second string, number int) error {
	keep, blame := nodes[first], nodes[second]

	if keep.fixed == blame.fixed && keep.index > blame.index || !keep.fixed && blame.fixed {
		keep, blame = blame, keep
	}

	if blame.fixed {
		return errorf(blame.action.Name, keep.action.Name, "actions %s and %s both have number %d", keep.action.Name, blame.action.Name, number)
	}

	return errorf(blame.action.Name, keep.action.Name,
		"action %s would get number %d next to its anchor, which %s already has: the chains around their anchors overlap",
		blame.action.Name, number, keep.action.Name)
}

// Find returns the number of an action in a numbered sequence, or 0.
func Find(seq []Numbered, name string) int {
	for _, n := range seq {
		if n.Name == name {
			return n.Sequence
		}
	}

	return 0
}
