package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// serviceComponent is appComponent with a service whose argument is
// conditional, which generates a custom action before InstallServices.
func serviceComponent() string {
	return strings.Replace(appComponent, "    files:", "    services: [{name: svc, arguments: [{value: -b, condition: B}]}]\n    files:", 1)
}

// setProps returns custom actions that set a property, one per placement.
func setProps(placements ...string) string {
	var sb strings.Builder

	sb.WriteString("customActions:\n")

	for i, placement := range placements {
		fmt.Fprintf(&sb, "  - id: Set%d\n    setProperty: {name: P%d, value: x}\n    schedule: [{sequence: execute, %s}]\n", i, i, placement)
	}

	return sb.String()
}

// Validate numbers the sequences as far as the configuration decides them,
// so that what the build would reject fails validate at its placement.
func TestScheduleConflicts(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		doc, path, msg string
	}{
		// Two actions anchored to each other passed validate and
		// failed the build without a path.
		"cycle": {
			setProps("before: Set1", "before: Set0"),
			"customActions[0].schedule[0]", "Set0 → Set1 → Set0",
		},
		// An action that only hangs off the cycle is not blamed.
		"cycle behind an innocent action": {
			setProps("before: Set1", "before: Set2", "before: Set1"),
			"customActions[1].schedule[0]", "Set1 → Set2 → Set1",
		},
		"chain into a fixed number": {
			setProps("at: 1402", "after: InstallValidate", "after: Set1"),
			"customActions[2].schedule[0]", "Set0 already has",
		},
		"same number": {
			setProps("at: 1400", "at: 1500"),
			"customActions[0].schedule[0]", "InstallValidate",
		},
		"below one": {
			setProps("at: 1", "before: Set0"),
			"customActions[1].schedule[0]", "below 1",
		},
		"above max": {
			setProps("at: 32767", "after: Set0"),
			"customActions[1].schedule[0]", "above 32767",
		},
		"twice in a sequence": {
			"customActions:\n  - id: Twice\n    setProperty: {name: P, value: x}\n" +
				"    schedule: [{sequence: execute, after: CostFinalize}, {sequence: execute, before: InstallFinalize}]\n",
			"customActions[0].schedule[1]", "already scheduled in this sequence at customActions[0].schedule[0]",
		},
		"anchor in another sequence": {
			"customActions:\n  - id: UIOnly\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: ui, after: CostFinalize}]\n" +
				"  - id: Next\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: execute, after: UIOnly}]\n",
			"customActions[1].schedule[0]", "not scheduled in the execute sequence",
		},
		// A generated service-argument action is blamed at its argument.
		"service argument": {
			"customActions:\n  - id: Fixed\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: execute, at: 5799}]\n",
			"components[0].services[0].arguments[0]", "5799",
		},
		// The data action of a run action takes the number before it.
		"run data action": {
			"customActions:\n  - id: Fixed\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: execute, at: 4003}]\n" +
				"  - id: Run\n    run: {command: cmd.exe}\n    return: check\n    execute: deferred\n    schedule: [{sequence: execute, before: Fixed}]\n" +
				"  - id: Late\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: execute, after: InstallFiles}]\n",
			"customActions[1].schedule[0]", "Run.Data",
		},
	} {
		component := appComponent
		if strings.HasPrefix(test.path, "components") {
			component = serviceComponent()
		}

		ds := validate(t, base+component+test.doc)
		if !find(ds, config.SeverityError, test.path, test.msg) || errorCount(ds) != 1 {
			t.Errorf("%s: want one error at %s with %q: %v", name, test.path, test.msg, ds)
		}
	}
}

// Chains that end at an action whose number only the builder knows, and
// chains that fit, pass.
func TestScheduleAccepted(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"chains":                     setProps("after: InstallValidate", "after: InstallValidate", "after: Set0", "before: InstallFinalize", "at: 1650"),
		"after a builder placement":  setProps("after: RemoveExistingProducts", "after: Set0", "at: 1402"),
		"in different sequences":     setProps("after: CostFinalize") + "  - id: Other\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: ui, after: CostFinalize}]\n",
		"standard without a default": setProps("after: ScheduleReboot"),
		"after LaunchConditions":     "launchConditions: [{condition: VersionNT, message: x}]\n" + setProps("after: LaunchConditions", "after: AppSearch"),
		"after a search action": "properties: {P: {search: {registry: {root: HKLM, key: X, name: V}}}}\n" +
			setProps("after: SearchApply_P", "after: Set0", "at: 51"),
		"after a dialog": "properties: {OPT: {}}\nui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT}]}\n" +
			"customActions:\n  - id: AfterDlg\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: ui, after: ExtraDlg}]\n",
		"next to a service argument": setProps("after: InstallServices", "before: InstallServices"),
	} {
		ds := validate(t, base+serviceComponent()+doc)
		if errorCount(ds) != 0 {
			t.Errorf("%s: %v", name, ds)
		}
	}
}

func errorCount(ds config.Diagnostics) int {
	count := 0

	for _, d := range ds {
		if d.Severity == config.SeverityError {
			count++
		}
	}

	return count
}

// A second action with an id already taken is reported once, by its id.
func TestScheduleDuplicateID(t *testing.T) {
	t.Parallel()

	ds := validate(t, base+appComponent+setProps("after: CostFinalize")+
		"  - id: Set0\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: execute, after: CostFinalize}]\n")
	if !find(ds, config.SeverityError, "customActions[1].id", "defined twice") || errorCount(ds) != 1 {
		t.Error(ds)
	}
}

// Errors that other checks report are not repeated by the schedule check.
func TestScheduleNoRepeats(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ doc, path, msg string }{
		"standard action id": {
			"customActions:\n  - id: CostFinalize\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: execute, after: InstallValidate}]\n",
			"customActions[0].id", "standard action name",
		},
		"termination number in execute": {
			setProps("at: -1", "after: Set0"),
			"customActions[0].schedule[0].at", "termination numbers",
		},
	} {
		ds := validate(t, base+appComponent+test.doc)
		if !find(ds, config.SeverityError, test.path, test.msg) || errorCount(ds) != 1 {
			t.Errorf("%s: want one error at %s with %q: %v", name, test.path, test.msg, ds)
		}
	}
}
