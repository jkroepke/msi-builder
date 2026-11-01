package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// movedBase is a dual-scope package with an upgrade, a property search and
// a launch condition, the actions standardActions can move.
const movedBase = `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: perUserOrMachine, installerVersion: "5.0" }
components:
  - files: [{ source: hello.exe }]
properties:
  PREVMACHINE:
    search: { registry: { root: HKLM, key: 'Software\Example\Hello', name: Installed } }
launchConditions:
  - { condition: 'Installed OR VersionNT >= 601', message: Needs a newer Windows. }
`

// setAllUsersAction sets ALLUSERS from the search in both sequences.
const setAllUsersAction = `customActions:
  - id: SetAllUsers
    setProperty: { name: ALLUSERS, value: "1" }
    schedule:
      - { sequence: execute, after: AppSearch, condition: PREVMACHINE }
      - { sequence: ui, after: AppSearch, condition: PREVMACHINE }
`

// buildMoved builds src next to hello.exe.
func buildMoved(t *testing.T, src string) (*builder.Result, error) {
	t.Helper()

	dir := t.TempDir()
	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	return builder.Build(cfg, builder.Options{BuildTime: buildTime})
}

// sequenceOf returns the numbers of the actions in a sequence table.
func sequenceOf(t *testing.T, res *builder.Result, table string) map[string]int {
	t.Helper()

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatal(err)
	}

	numbers := make(map[string]int)

	for _, row := range database.Table(table).Rows {
		n, ok := row[2].(int)
		if !ok {
			t.Fatalf("%s: %v has no number", table, row)
		}

		numbers[fmt.Sprint(row[0])] = n
	}

	return numbers
}

// TestMoveFindRelatedProducts puts FindRelatedProducts behind an action that
// sets ALLUSERS from a search, as Far Manager and ImageGlass do: after the
// search results are applied and before LaunchConditions.
func TestMoveFindRelatedProducts(t *testing.T) {
	t.Parallel()

	res, err := buildMoved(t, movedBase+setAllUsersAction+`standardActions:
  - action: FindRelatedProducts
    schedule:
      - { sequence: execute, after: SetAllUsers }
      - { sequence: ui, after: SetAllUsers }
`)
	if err != nil {
		t.Fatalf("%v: %v", err, res.Diagnostics)
	}

	for _, table := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		seq := sequenceOf(t, res, table)
		apply, set, find, launch := seq["SearchApply_PREVMACHINE"], seq["SetAllUsers"], seq["FindRelatedProducts"], seq["LaunchConditions"]

		if apply != 51 || set != 52 || find != 53 || launch != 100 {
			t.Errorf("%s: SearchApply_PREVMACHINE %d, SetAllUsers %d, FindRelatedProducts %d, LaunchConditions %d; want 51, 52, 53, 100",
				table, apply, set, find, launch)
		}
	}
}

// TestMoveAfterAppSearch places a FindRelatedProducts moved after AppSearch
// behind the actions that apply the search results, and before the
// LaunchConditions that follow AppSearch when AppSearch itself moves (BowPad).
func TestMoveAfterAppSearch(t *testing.T) {
	t.Parallel()

	res, err := buildMoved(t, movedBase+`standardActions:
  - action: AppSearch
    schedule: [{ sequence: execute, at: 1 }]
  - action: FindRelatedProducts
    schedule: [{ sequence: execute, after: AppSearch }]
`)
	if err != nil {
		t.Fatalf("%v: %v", err, res.Diagnostics)
	}

	seq := sequenceOf(t, res, "InstallExecuteSequence")
	if got := []int{seq["AppSearch"], seq["SearchApply_PREVMACHINE"], seq["FindRelatedProducts"], seq["LaunchConditions"]}; fmt.Sprint(got) != "[1 2 3 4]" {
		t.Errorf("AppSearch, SearchApply_PREVMACHINE, FindRelatedProducts, LaunchConditions at %v, want [1 2 3 4]", got)
	}

	// The UI sequence keeps the defaults.
	ui := sequenceOf(t, res, "InstallUISequence")
	if ui["FindRelatedProducts"] != 25 || ui["AppSearch"] != 50 || ui["LaunchConditions"] != 100 {
		t.Errorf("UI sequence moved: %v", ui)
	}
}

func TestMoveStandardActionRejected(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		src, path, msg string
	}{
		"nothing to find": {
			strings.Replace(movedBase, "launchConditions:", "upgrade: false\nlaunchConditions:", 1) +
				"standardActions:\n  - {action: FindRelatedProducts, schedule: [{sequence: execute, after: AppSearch}]}\n",
			"standardActions[0].schedule[0]", "needs the upgrade section",
		},
		"past CostInitialize through a custom action": {
			"customActions:\n  - {id: Late, setProperty: {name: P, value: x}, schedule: [{sequence: execute, after: CostFinalize}]}\n" +
				movedBase + "standardActions:\n  - {action: AppSearch, schedule: [{sequence: execute, after: Late}]}\n",
			"standardActions[0].schedule[0]", "must run before CostInitialize",
		},
		// LaunchConditions follows a moved AppSearch, here to before
		// FindRelatedProducts.
		"downgrade check before FindRelatedProducts through AppSearch": {
			movedBase + "standardActions:\n  - {action: AppSearch, schedule: [{sequence: execute, at: 10}]}\n",
			"standardActions[0].schedule[0]", "downgrade check",
		},
		"downgrade check before FindRelatedProducts": {
			movedBase + "standardActions:\n  - {action: LaunchConditions, schedule: [{sequence: ui, before: FindRelatedProducts}]}\n",
			"standardActions[0].schedule[0]", "downgrade check",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := buildMoved(t, test.src)
			if err == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), test.path+": ") || !strings.Contains(fmt.Sprint(res.Diagnostics), test.msg) {
				t.Errorf("err %v, diagnostics %s, want %q at %s", err, res.Diagnostics, test.msg, test.path)
			}
		})
	}
}
