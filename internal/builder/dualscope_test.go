package builder_test

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
)

// contextComponents is a PATH entry with the default scope and a component
// that writes an HKLM value only per machine.
const contextComponents = `  - { id: Path, directory: INSTALLDIR, environment: [{ name: PATH, value: '[INSTALLDIR]', action: append }] }
  - { id: Machine, directory: INSTALLDIR, scope: perMachine, condition: OPT, registry: [{ root: HKLM, key: Software\Acme, name: N, value: v }] }
`

// environmentRows maps each Environment name of res to the condition of
// the component that installs it.
func environmentRows(t *testing.T, res *builder.Result) map[string]string {
	t.Helper()

	conditions := map[string]string{}
	for _, row := range rows(t, res.Database, "Component") {
		conditions[fmt.Sprint(row[0])] = fmt.Sprint(row[4])
	}

	got := map[string]string{}
	for _, row := range rows(t, res.Database, "Environment") {
		got[fmt.Sprint(row[1])] = conditions[fmt.Sprint(row[3])]
	}

	return got
}

// TestContextEnvironment checks where a variable with the default scope
// goes: the machine environment per machine, the user's per user, and in
// a dual package both, each in a component of its own that installs only
// in its context (ENVVAR-03).
func TestContextEnvironment(t *testing.T) {
	t.Parallel()

	const noCondition = "<nil>"

	for scope, want := range map[string]map[string]string{
		"perMachine":       {"=-*PATH": noCondition},
		"perUser":          {"=-PATH": noCondition},
		"perUserOrMachine": {"=-*PATH": "ALLUSERS=1", "=-PATH": "NOT ALLUSERS"},
		"perMachineOrUser": {"=-*PATH": "ALLUSERS=1", "=-PATH": "NOT ALLUSERS"},
	} {
		extra := `  - { id: Path, directory: INSTALLDIR, environment: [{ name: PATH, value: '[INSTALLDIR]', action: append }] }
ui: { enabled: false }
`

		res := buildScope(t, scope, "x64", extra)
		if got := environmentRows(t, res); !maps.Equal(got, want) {
			t.Errorf("%s: variables and their component conditions %v, want %v", scope, got, want)
		}
	}
}

// TestContextComponents checks the components of a dual package that
// install in one context: the scope's condition joins the component's,
// and the carriers of context variables are in the variable's features
// with a key path in the root of their context.
func TestContextComponents(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUserOrMachine", "x64", contextComponents+"ui: { enabled: false }\n")
	database := res.Database

	cond := cell(t, database, "Component", "Condition", func(row []any) bool { return row[0] == "Machine" })
	if cond != "(OPT) AND ALLUSERS=1" {
		t.Errorf("Machine condition %v, want (OPT) AND ALLUSERS=1", cond)
	}

	roots := map[string]any{}
	for _, row := range rows(t, database, "Registry") {
		roots[fmt.Sprint(row[0])] = row[1]
	}

	features := map[string]bool{}
	for _, row := range rows(t, database, "FeatureComponents") {
		features[fmt.Sprint(row[1])] = true
	}

	variables := rows(t, database, "Environment")
	carriers := make([]string, 0, len(variables))

	for _, row := range variables {
		carrier := fmt.Sprint(row[3])
		carriers = append(carriers, carrier)

		keyPath := cell(t, database, "Component", "KeyPath", func(r []any) bool { return r[0] == carrier })
		dir := cell(t, database, "Component", "Directory_", func(r []any) bool { return r[0] == carrier })
		wantRoot := 1 // HKCU for the user variable

		if row[1] == "=-*PATH" {
			wantRoot = -1 // HKMU, which is HKLM per machine
		}

		if !features[carrier] || dir != "TARGETDIR" || fmt.Sprint(roots[fmt.Sprint(keyPath)]) != strconv.Itoa(wantRoot) {
			t.Errorf("carrier %s of %v: in a feature %v, directory %v, key path root %v; want true, TARGETDIR, %d",
				carrier, row[1], features[carrier], dir, roots[fmt.Sprint(keyPath)], wantRoot)
		}
	}

	if slices.Contains(carriers, "Path") || len(slices.Compact(slices.Sorted(slices.Values(carriers)))) != 2 {
		t.Errorf("variables in components %v, want two carriers", carriers)
	}
}

// TestContextCarrierAttributes checks that the carriers of a variable that
// follows the context keep the condition, transitive and 64-bit flags of
// its component, so that a repair evaluates them again (ENVVAR-03).
func TestContextCarrierAttributes(t *testing.T) {
	t.Parallel()

	const (
		transitive = 0x40
		bits64     = 0x100
	)

	extra := `  - { id: Path, directory: INSTALLDIR, condition: OPT, transitive: true, environment: [{ name: PATH, value: '[INSTALLDIR]', action: append }] }
ui: { enabled: false }
`
	database := buildScope(t, "perMachineOrUser", "x64", extra).Database

	want := map[string]string{"=-*PATH": "(OPT) AND ALLUSERS=1", "=-PATH": "(OPT) AND NOT ALLUSERS"}

	variables := rows(t, database, "Environment")
	if len(variables) != len(want) {
		t.Fatalf("%d Environment rows, want %d", len(variables), len(want))
	}

	seen := map[string]bool{}

	for _, row := range variables {
		if name := fmt.Sprint(row[1]); seen[name] {
			t.Errorf("%s is written twice", name)
		} else {
			seen[name] = true
		}

		carrier := fmt.Sprint(row[3])
		cond := cell(t, database, "Component", "Condition", func(r []any) bool { return r[0] == carrier })
		attrs, ok := cell(t, database, "Component", "Attributes", func(r []any) bool { return r[0] == carrier }).(int)

		if !ok || cond != want[fmt.Sprint(row[1])] || attrs&transitive == 0 || attrs&bits64 == 0 {
			t.Errorf("carrier of %v: condition %v, attributes %#x; want %s, transitive and 64-bit",
				row[1], cond, attrs, want[fmt.Sprint(row[1])])
		}
	}
}

// TestContextActions checks the custom actions of a dual package that run
// in one context: the scope's condition joins each schedule's, the data
// action of a run action gets the same, and a perMachine action keeps its
// no-impersonate bit (CA-09).
func TestContextActions(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUserOrMachine", "x64", `customActions:
  - id: Machine
    run: { command: '"[System64Folder]whoami.exe"' }
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule: [{ sequence: execute, before: InstallFinalize, when: install }]
  - id: User
    setProperty: { name: A, value: b }
    scope: perUser
    schedule: [{ sequence: execute, after: CostFinalize }]
  - id: Both
    setProperty: { name: B, value: c }
    schedule: [{ sequence: execute, after: CostFinalize, condition: OPT }]
ui: { enabled: false }
`)
	database := res.Database

	conditions := map[string]any{}
	for _, row := range rows(t, database, "InstallExecuteSequence") {
		conditions[fmt.Sprint(row[0])] = row[1]
	}

	want := map[string]any{
		"Machine":      "(NOT Installed AND NOT OLDERVERSIONBEINGUPGRADED) AND ALLUSERS=1",
		"Machine.Data": "(NOT Installed AND NOT OLDERVERSIONBEINGUPGRADED) AND ALLUSERS=1",
		"User":         "NOT ALLUSERS",
		"Both":         "OPT",
	}
	for action, cond := range want {
		if conditions[action] != cond {
			t.Errorf("%s: condition %v, want %v", action, conditions[action], cond)
		}
	}

	typ := cell(t, database, "CustomAction", "Type", func(row []any) bool { return row[0] == "Machine" })
	if n, err := strconv.Atoi(fmt.Sprint(typ)); err != nil || n&0x800 == 0 {
		t.Errorf("Machine type %v, want the no-impersonate bit 0x800", typ)
	}
}
