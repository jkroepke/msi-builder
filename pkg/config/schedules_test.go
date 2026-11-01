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
			setProps("after: SearchApply_P", "after: Set0", "at: 60"),
		"after a dialog": "properties: {OPT: {}}\nui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT}]}\n" +
			"customActions:\n  - id: AfterDlg\n    setProperty: {name: Q, value: x}\n    schedule: [{sequence: ui, after: ExtraDlg}]\n",
		"next to a service argument": setProps("after: InstallServices", "before: InstallServices"),
		// The builder's search chain comes first under AppSearch, so a
		// FindRelatedProducts after it still runs before LaunchConditions.
		"FindRelatedProducts after a search result": "properties: {Q: {search: {registry: {root: HKLM, key: X, name: V}}}}\n" +
			"launchConditions: [{condition: VersionNT, message: x}]\n" +
			"standardActions:\n  - {action: LaunchConditions, schedule: [{sequence: ui, after: AppSearch}]}\n" +
			"  - {action: FindRelatedProducts, schedule: [{sequence: ui, after: SearchApply_Q}]}\n",
		"right after the chains": "properties: {Q: {search: {registry: {root: HKLM, key: X, name: V}}}}\n" +
			setProps("at: 52") + "  - id: UISet\n    setProperty: {name: U, value: x}\n    schedule: [{sequence: ui, at: 52}]\n",
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

// uiSetProps is setProps in the UI sequence.
func uiSetProps(placements ...string) string {
	return strings.ReplaceAll(setProps(placements...), "sequence: execute", "sequence: ui")
}

// The actions the builder chains after CostFinalize and AppSearch take the
// numbers next to them: a placement at one of those numbers failed only
// the build and now fails validate, with a note on where the action comes
// from.
func TestScheduleBuilderChains(t *testing.T) {
	t.Parallel()

	arp := func(doc string) string {
		return strings.Replace(doc, "  upgradeCode:", "  arp: {installLocation: INSTALLDIR}\n  upgradeCode:", 1)
	}
	dual := func(doc string) string {
		return strings.Replace(doc, "  platform: x64\n", "  platform: x64\n  scope: perUserOrMachine\n", 1)
	}
	search := "properties: {Q: {search: {registry: {root: HKLM, key: X, name: V}}}}\n"

	for name, test := range map[string]struct {
		doc, msg string
	}{
		"search result in execute": {
			base + appComponent + search + setProps("at: 51"),
			"SearchApply_Q comes from properties.Q.search: the build chains it after AppSearch",
		},
		"search result in ui": {
			base + appComponent + search + uiSetProps("at: 51"),
			"SearchApply_Q comes from properties.Q.search",
		},
		"search default": {
			base + appComponent + "properties: {Q: {value: d, search: [{registry: {root: HKLM, key: X, name: V}}, {registry: {root: HKLM, key: Y, name: V}}]}}\n" +
				setProps("at: 53"),
			"SearchDefault_Q comes from properties.Q.search: the build chains it after SearchApply2_Q",
		},
		"remembered install dir": {
			strings.Replace(base, "upgrade: {}\n", "upgrade: {}\ninstallDir: {remember: true}\n", 1) + appComponent + uiSetProps("at: 51"),
			"RestoreInstallDir comes from installDir.remember",
		},
		"search behind a moved AppSearch": {
			base + appComponent + search + "standardActions: [{action: AppSearch, schedule: [{sequence: execute, at: 30}]}]\n" + setProps("at: 31"),
			"SearchApply_Q comes from properties.Q.search",
		},
		"arp install location in execute": {
			arp(base) + appComponent + setProps("at: 1001"),
			"SetARPINSTALLLOCATION comes from product.arp.installLocation: the build chains it after CostFinalize",
		},
		"arp install location in ui": {
			arp(base) + appComponent + uiSetProps("at: 1001"),
			"SetARPINSTALLLOCATION comes from product.arp.installLocation",
		},
		"firewall remote addresses": {
			base + appComponent + "firewall: [{name: r, component: App, program: '[INSTALLDIR]app.exe', port: '80', remoteAddresses: '[PEERS]'}]\n" + setProps("at: 1002"),
			"comes from firewall[0].remoteAddresses: the build chains it after SetFwRemoteIPDefault",
		},
		"scope page presets": {
			dual(base) + appComponent + "ui: {enabled: true}\n" + uiSetProps("at: 1001"),
			"SetInstallScopeMachine comes from the scope page of package.scope perUserOrMachine: the build chains it after CostFinalize",
		},
		"scope page": {
			dual(base) + appComponent + "ui: {enabled: true}\n" + uiSetProps("at: 1003"),
			"action InstallScopeDlg would get number 1003",
		},
		"FindRelatedProducts after the scope page": {
			dual(base) + appComponent + "ui: {enabled: true}\n" + uiSetProps("at: 1004"),
			"FindRelatedProducts comes from the scope page",
		},
	} {
		ds := validate(t, test.doc)
		if !find(ds, config.SeverityError, "customActions[0].schedule[0]", test.msg) || errorCount(ds) != 1 {
			t.Errorf("%s: want one error at customActions[0].schedule[0] with %q: %v", name, test.msg, ds)
		}
	}
}

// A custom action named like an action the builder chains is reported at
// its id, once.
func TestScheduleBuilderChainName(t *testing.T) {
	t.Parallel()

	ds := validate(t, base+appComponent+"properties: {Q: {value: d, search: {registry: {root: HKLM, key: X, name: V}}}}\n"+
		"customActions:\n  - id: SearchApply_Q\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: ui, at: 1500}]\n")
	if !find(ds, config.SeverityError, "customActions[0].id", "SearchApply_Q is the name of an action the build adds") || errorCount(ds) != 1 {
		t.Error(ds)
	}
}

// A FindRelatedProducts moved into a long chain after AppSearch runs before
// LaunchConditions, which the builder puts after that chain once it reaches
// 100; ids the older checks refuse are not reported twice.
func TestScheduleFindInLongSearchChain(t *testing.T) {
	t.Parallel()

	doc := base + appComponent + "properties: {Q: {search: {registry: {root: HKLM, key: X, name: V}}}}\n" +
		"launchConditions: [{condition: VersionNT, message: x}]\n" +
		"standardActions: [{action: FindRelatedProducts, schedule: [{sequence: execute, after: SearchApply_Q}]}]\n"

	placements := make([]string, 49)
	for i := range placements {
		placements[i] = "after: SearchApply_Q"
	}

	if ds := validate(t, doc+setProps(placements...)); errorCount(ds) != 0 || len(ds) != 0 {
		t.Errorf("long chain: %v", ds)
	}

	dual := strings.Replace(base, "  platform: x64\n", "  platform: x64\n  scope: perUserOrMachine\n", 1) + appComponent + "ui: {enabled: true}\n"
	for _, id := range []string{"SetInstallScopeUser", "FindRelatedProducts"} {
		ds := validate(t, dual+"customActions:\n  - id: "+id+"\n    setProperty: {name: P, value: x}\n    schedule: [{sequence: execute, at: 6700}]\n")
		if errorCount(ds) != 1 {
			t.Errorf("%s: want one error: %v", id, ds)
		}
	}
}
