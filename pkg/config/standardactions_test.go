package config_test

import (
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// setAllUsers is a custom action that a moved FindRelatedProducts follows.
const setAllUsers = `customActions:
  - id: SetAllUsers
    setProperty: {name: ALLUSERS, value: "1"}
    schedule:
      - {sequence: execute, after: AppSearch, condition: PREV}
      - {sequence: ui, after: AppSearch, condition: PREV}
`

// moveFind returns a standardActions section that moves
// FindRelatedProducts to the given placements.
func moveFind(placements string) string {
	return "standardActions:\n  - action: FindRelatedProducts\n    schedule: [" + placements + "]\n"
}

func TestStandardActionsAccepted(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"after a custom action": setAllUsers + moveFind("{sequence: execute, after: SetAllUsers}, {sequence: ui, after: SetAllUsers}"),
		"after AppSearch":       moveFind("{sequence: execute, after: AppSearch}"),
		"before CostInitialize": moveFind("{sequence: ui, before: CostInitialize}"),
		"at a number":           "standardActions:\n  - {action: AppSearch, schedule: [{sequence: execute, at: 1}]}\n",
		"launch conditions after a custom action": setAllUsers +
			"standardActions:\n  - {action: LaunchConditions, schedule: [{sequence: execute, after: SetAllUsers}]}\n",
	} {
		if ds := validate(t, base+appComponent+doc); errorCount(ds) != 0 {
			t.Errorf("%s: %v", name, ds)
		}
	}
}

func TestStandardActionsRejected(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		doc, path, msg string
	}{
		"not movable": {
			"standardActions:\n  - {action: InstallFiles, schedule: [{sequence: execute, at: 10}]}\n",
			"standardActions[0].action", "only the standard actions that run before costing",
		},
		"missing action": {
			"standardActions:\n  - {schedule: [{sequence: execute, at: 10}]}\n",
			"standardActions[0].action", "required",
		},
		"twice": {
			moveFind("{sequence: execute, at: 10}") + "  - {action: FindRelatedProducts, schedule: [{sequence: ui, at: 10}]}\n",
			"standardActions[1].action", "already moved at standardActions[0]",
		},
		"no schedule": {
			"standardActions:\n  - {action: AppSearch}\n",
			"standardActions[0].schedule", "required",
		},
		"twice in a sequence": {
			moveFind("{sequence: execute, at: 10}, {sequence: execute, at: 20}"),
			"standardActions[0].schedule[1].sequence", "already moved in the execute sequence",
		},
		"admin sequence": {
			"standardActions:\n  - {action: LaunchConditions, schedule: [{sequence: adminUI, at: 10}]}\n",
			"standardActions[0].schedule[0].sequence", "not one of execute, ui",
		},
		"condition": {
			moveFind("{sequence: execute, at: 10, condition: NOT Installed}"),
			"standardActions[0].schedule[0].condition", "keeps its own condition",
		},
		"when": {
			moveFind("{sequence: execute, at: 10, when: install}"),
			"standardActions[0].schedule[0].when", "keeps its own condition",
		},
		"two positions": {
			moveFind("{sequence: execute, at: 10, after: AppSearch}"),
			"standardActions[0].schedule[0]", "exactly one of before, after or at",
		},
		"number after costing": {
			moveFind("{sequence: execute, at: 800}"),
			"standardActions[0].schedule[0].at", "before CostInitialize (800)",
		},
		"after CostInitialize": {
			moveFind("{sequence: execute, after: CostInitialize}"),
			"standardActions[0].schedule[0]", "which it does not after CostInitialize",
		},
		"after InstallFiles": {
			moveFind("{sequence: execute, after: InstallFiles}"),
			"standardActions[0].schedule[0]", "which it does not after InstallFiles",
		},
		"unknown anchor": {
			moveFind("{sequence: execute, after: Nothing}"),
			"standardActions[0].schedule[0]", `anchor "Nothing"`,
		},
		"itself": {
			moveFind("{sequence: execute, after: FindRelatedProducts}"),
			"standardActions[0].schedule[0]", "next to itself",
		},
		// Validate numbers the moved action with the custom actions.
		"cycle with a custom action": {
			"customActions:\n  - id: Before\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: execute, before: FindRelatedProducts}]\n" +
				moveFind("{sequence: execute, after: Before}"),
			"customActions[0].schedule[0]", "scheduling cycle",
		},
	} {
		ds := validate(t, base+appComponent+test.doc)
		if !find(ds, config.SeverityError, test.path, test.msg) || errorCount(ds) != 1 {
			t.Errorf("%s: want one error at %s with %q: %v", name, test.path, test.msg, ds)
		}
	}
}

// Validate sees what the build adds around a moved action: LaunchConditions
// after a moved AppSearch, and the number a chain gives the moved action.
func TestStandardActionsNumbered(t *testing.T) {
	t.Parallel()

	launch := "launchConditions: [{condition: VersionNT, message: x}]\n"

	for name, test := range map[string]struct {
		doc, path, msg string
	}{
		"AppSearch after the LaunchConditions that follow it": {
			launch + "standardActions:\n  - {action: AppSearch, schedule: [{sequence: execute, after: LaunchConditions}]}\n",
			"standardActions[0].schedule[0]", "AppSearch → LaunchConditions → AppSearch",
		},
		"past CostInitialize through a custom action": {
			"customActions:\n  - {id: Late, setProperty: {name: P, value: x}, schedule: [{sequence: execute, after: CostFinalize}]}\n" +
				"standardActions:\n  - {action: AppSearch, schedule: [{sequence: execute, after: Late}]}\n",
			"standardActions[0].schedule[0]", "must run before CostInitialize (800)",
		},
		"termination number": {
			"standardActions:\n  - {action: AppSearch, schedule: [{sequence: ui, at: -1}]}\n",
			"standardActions[0].schedule[0].at", "termination action",
		},
	} {
		ds := validate(t, base+appComponent+test.doc)
		if !find(ds, config.SeverityError, test.path, test.msg) || errorCount(ds) != 1 {
			t.Errorf("%s: want one error at %s with %q: %v", name, test.path, test.msg, ds)
		}
	}
}
