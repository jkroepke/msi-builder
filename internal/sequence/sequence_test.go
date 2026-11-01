package sequence //nolint:testpackage // the tests reach unexported encoders

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func names(seq []Numbered) string {
	var sb strings.Builder

	for _, n := range seq {
		fmt.Fprintf(&sb, "%s=%d ", n.Name, n.Sequence)
	}

	return sb.String()
}

// The examples of docs/spec/MSISPEC.md section 11.7.
func TestSpecExamples(t *testing.T) {
	t.Parallel()

	seq, err := Number([]Action{
		{Name: "InstallFinalize", Sequence: 6600},
		{Name: "InstallValidate", Sequence: 1400},
		{Name: "A", Before: "InstallFinalize"},
		{Name: "B", After: "InstallFinalize"},
		{Name: "REP", After: "InstallValidate"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := names(seq); got != "InstallValidate=1400 REP=1401 A=6599 InstallFinalize=6600 B=6601 " {
		t.Error(got)
	}

	seq, err = Number([]Action{
		{Name: "WriteRegistryValues", Sequence: 5000},
		{Name: "PrepA", Before: "WriteRegistryValues"},
		{Name: "PrepA2", After: "PrepA"},
		{Name: "PrepB", Before: "WriteRegistryValues"},
		{Name: "PostA", After: "WriteRegistryValues"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := names(seq); got != "PrepA=4997 PrepA2=4998 PrepB=4999 WriteRegistryValues=5000 PostA=5001 " {
		t.Error(got)
	}

	seq, err = Number([]Action{
		{Name: "StopServices", Sequence: 1900, Condition: "VersionNT"},
		{Name: "X1", Before: "X2"},
		{Name: "X2", Before: "StopServices"},
		{Name: "BindImage", Sequence: 4300},
		{Name: "P1", After: "BindImage"}, {Name: "P2", After: "BindImage"}, {Name: "P3", After: "BindImage"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := names(seq); got != "X1=1898 X2=1899 StopServices=1900 BindImage=4300 P1=4301 P2=4302 P3=4303 " {
		t.Error(got)
	}

	if Find(seq, "StopServices") != 1900 || seq[2].Condition != "VersionNT" || Find(seq, "nope") != 0 {
		t.Error("Find or condition lost")
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	for name, actions := range map[string][]Action{
		"missing anchor":  {{Name: "A", Before: "Nope"}},
		"cycle":           {{Name: "A", Before: "B"}, {Name: "B", Before: "A"}},
		"both":            {{Name: "A", Sequence: 5, Before: "B"}, {Name: "B", Sequence: 10}},
		"neither":         {{Name: "A"}},
		"duplicate":       {{Name: "A", Sequence: 5}, {Name: "A", Sequence: 10}},
		"same number":     {{Name: "A", Sequence: 5}, {Name: "B", Sequence: 5}},
		"collision":       {{Name: "A", Sequence: 100}, {Name: "B", Sequence: 101}, {Name: "C", After: "A"}},
		"below one":       {{Name: "A", Sequence: 1}, {Name: "B", Before: "A"}},
		"above max":       {{Name: "A", Sequence: 32767}, {Name: "B", After: "A"}},
		"bad number":      {{Name: "A", Sequence: -5}},
		"termination":     {{Name: "A", Sequence: -1}, {Name: "B", After: "A"}},
		"zero and above":  {{Name: "A", Sequence: 40000}},
		"unnamed":         {{Sequence: 1}},
		"chain collision": {{Name: "A", Sequence: 100}, {Name: "B", Sequence: 103}, {Name: "C", After: "A"}, {Name: "D", After: "C"}, {Name: "E", After: "D"}},
	} {
		if _, err := Number(actions); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTerminationActionsAreFine(t *testing.T) {
	t.Parallel()

	seq, err := Number([]Action{{Name: "ExitDialog", Sequence: -1}, {Name: "FatalError", Sequence: -3}, {Name: "A", Sequence: 10}})
	if err != nil {
		t.Fatal(err)
	}

	if got := names(seq); got != "FatalError=-3 ExitDialog=-1 A=10 " {
		t.Error(got)
	}
}

// A cycle names an action on it and the cycle's path, not an action that
// only hangs off the cycle.
func TestCycleBlamesAMember(t *testing.T) {
	t.Parallel()

	_, err := Number([]Action{
		{Name: "Innocent", Before: "B"},
		{Name: "B", Before: "C"},
		{Name: "C", Before: "B"},
		{Name: "Fixed", Sequence: 10},
	})

	var serr *Error
	if !errors.As(err, &serr) {
		t.Fatalf("want *Error, got %v", err)
	}

	if serr.Action != "B" || strings.Join(serr.Cycle, " ") != "B C B" {
		t.Errorf("action %q, cycle %q", serr.Action, serr.Cycle)
	}

	if strings.Contains(err.Error(), "Innocent") || !strings.Contains(err.Error(), "B → C → B") {
		t.Error(err)
	}
}

func TestErrorNamesTheAction(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		actions       []Action
		action, other string
	}{
		"missing anchor":  {[]Action{{Name: "A", Before: "Nope"}}, "A", ""},
		"termination":     {[]Action{{Name: "T", Sequence: -1}, {Name: "B", After: "T"}}, "B", "T"},
		"same number":     {[]Action{{Name: "A", Sequence: 5}, {Name: "B", Sequence: 5}}, "B", "A"},
		"chain collision": {[]Action{{Name: "A", Sequence: 100}, {Name: "B", Sequence: 103}, {Name: "C", After: "A"}, {Name: "D", After: "C"}, {Name: "E", After: "D"}}, "E", "B"},
		"chain on chain":  {[]Action{{Name: "A", Sequence: 100}, {Name: "B", Sequence: 102}, {Name: "C", After: "A"}, {Name: "D", Before: "B"}}, "D", "C"},
		"below one":       {[]Action{{Name: "A", Sequence: 2}, {Name: "B", Before: "A"}, {Name: "C", Before: "B"}}, "C", "A"},
		"above max":       {[]Action{{Name: "A", Sequence: MaxSequence - 1}, {Name: "B", After: "A"}, {Name: "C", After: "A"}}, "C", "A"},
		"duplicate":       {[]Action{{Name: "A", Sequence: 5}, {Name: "A", Sequence: 10}}, "A", ""},
	} {
		_, err := Number(test.actions)

		var serr *Error
		if !errors.As(err, &serr) {
			t.Errorf("%s: want *Error, got %v", name, err)

			continue
		}

		if serr.Action != test.action || serr.Other != test.other {
			t.Errorf("%s: action %q other %q, want %q %q (%v)", name, serr.Action, serr.Other, test.action, test.other, err)
		}
	}
}

// chain returns a fixed action at 1 and n actions after it, each anchored to
// the one before.
func chain(n int) []Action {
	actions := make([]Action, 0, n+1)
	actions = append(actions, Action{Name: "Fixed", Sequence: 1})

	for i := range n {
		actions = append(actions, Action{Name: fmt.Sprintf("A%d", i), After: actions[len(actions)-1].Name})
	}

	return actions
}

func TestLongChain(t *testing.T) {
	t.Parallel()

	seq, err := Number(chain(30000))
	if err != nil {
		t.Fatal(err)
	}

	if len(seq) != 30001 || seq[0].Name != "Fixed" || seq[30000].Name != "A29999" || seq[30000].Sequence != 30001 {
		t.Errorf("%d actions, last %v", len(seq), seq[len(seq)-1])
	}

	// A chain before a fixed action is as deep and keeps its order.
	actions := make([]Action, 0, 20001)

	actions = append(actions, Action{Name: "Fixed", Sequence: 30000})
	for i := range 20000 {
		actions = append(actions, Action{Name: fmt.Sprintf("B%d", i), Before: actions[len(actions)-1].Name})
	}

	seq, err = Number(actions)
	if err != nil {
		t.Fatal(err)
	}

	if seq[0].Name != "B19999" || seq[0].Sequence != 10000 || seq[19999].Name != "B0" {
		t.Errorf("first %v, before the fixed action %v", seq[0], seq[19999])
	}
}

// Numbering takes memory linear in the number of actions: the old expansion
// kept every node's descendants, quadratic in the length of a chain.
func TestLinearMemory(t *testing.T) { //nolint:paralleltest // measures allocations, which parallel tests would add to
	perOp := func(n int) uint64 {
		actions := chain(n)

		var before, after runtime.MemStats

		runtime.ReadMemStats(&before)

		if _, err := Number(actions); err != nil {
			t.Fatal(err)
		}

		runtime.ReadMemStats(&after)

		return after.TotalAlloc - before.TotalAlloc
	}

	small, large := perOp(2000), perOp(16000)

	// Eight times the actions: linear is about 8×, quadratic 64×.
	if large > 12*small {
		t.Errorf("2000 actions: %d bytes, 16000 actions: %d bytes", small, large)
	}

	if large > 16<<20 {
		t.Errorf("16000 actions allocate %d bytes", large)
	}
}

// A long tail hanging off a cycle is walked once: the cycle is found from
// the tail's first action and named by its members.
func TestCycleBehindLongTail(t *testing.T) {
	t.Parallel()

	actions := make([]Action, 0, 20002)

	for i := range 20000 {
		actions = append(actions, Action{Name: fmt.Sprintf("T%d", i), Before: fmt.Sprintf("T%d", i+1)})
	}

	actions = append(actions, Action{Name: "T20000", Before: "T20001"}, Action{Name: "T20001", Before: "T20000"})

	_, err := Number(actions)

	var serr *Error
	if !errors.As(err, &serr) || strings.Join(serr.Cycle, " ") != "T20000 T20001 T20000" {
		t.Errorf("%v", err)
	}

	// A long cycle lists all its actions in Cycle, a few in the message.
	ring := make([]Action, 0, 1000)
	for i := range 1000 {
		ring = append(ring, Action{Name: fmt.Sprintf("R%d", i), Before: fmt.Sprintf("R%d", (i+1)%1000)})
	}

	_, err = Number(ring)
	if !errors.As(err, &serr) || len(serr.Cycle) != 1001 || len(err.Error()) > 200 || !strings.Contains(err.Error(), "(1000 actions)") {
		t.Errorf("%d actions in the cycle: %v", len(serr.Cycle), err)
	}
}
