package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// buildScope builds a one-file package with the given scope, platform and
// ui section.
func buildScope(t *testing.T, scope, platform, extra string) *builder.Result {
	t.Helper()

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Acme App, manufacturer: Acme, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: `+platform+`, scope: `+scope+` }
components:
  - files: [{ source: a.txt }]
`+extra), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.BaseDir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	return mustBuild(t, cfg)
}

// properties returns the Property table as a map.
func properties(t *testing.T, res *builder.Result) map[string]string {
	t.Helper()

	props := map[string]string{}
	for _, row := range rows(t, res.Database, "Property") {
		props[fmt.Sprint(row[0])] = fmt.Sprint(row[1])
	}

	return props
}

// TestScopeProperties checks ALLUSERS and MSIINSTALLPERUSER per scope
// (spec section 8.2, INST-02).
func TestScopeProperties(t *testing.T) {
	t.Parallel()

	for scope, want := range map[string][2]string{
		"perMachine":       {"1", ""},
		"perUser":          {"", ""},
		"perUserOrMachine": {"2", "1"},
		"perMachineOrUser": {"2", ""},
	} {
		props := properties(t, buildScope(t, scope, "x64", "ui: { enabled: false }\n"))
		if got := [2]string{props["ALLUSERS"], props["MSIINSTALLPERUSER"]}; got != want {
			t.Errorf("%s: ALLUSERS, MSIINSTALLPERUSER = %q, want %q", scope, got, want)
		}

		// A standard user's choice must reach the elevated server.
		secure := slices.Contains(strings.Split(props["SecureCustomProperties"], ";"), "MSIINSTALLPERUSER")
		if dual := want[0] == "2"; secure != dual {
			t.Errorf("%s: MSIINSTALLPERUSER secure = %v, want %v", scope, secure, dual)
		}
	}
}

// TestScopePage checks the scope page of a dual package: its place before
// the wizard, the events of Next, the folders it moves and the two Install
// buttons of the ready page.
func TestScopePage(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perMachineOrUser", "x86", "installDir: { remember: true }\nui: { enabled: true, chooseDirectory: true }\n")

	for _, want := range []struct {
		dialog, control string
		next            map[string]string
	}{
		{"WelcomeDlg", "Next", map[string]string{"1": "InstallDirDlg"}},
		{"InstallScopeDlg", "Next", map[string]string{}},
		{"InstallDirDlg", "Back", map[string]string{"1": "WelcomeDlg"}},
	} {
		if got := nextOf(t, res, want.dialog, want.control); fmt.Sprint(got) != fmt.Sprint(want.next) {
			t.Errorf("%s %s goes to %v, want %v", want.dialog, want.control, got, want.next)
		}
	}

	if got := properties(t, res)["BuilderInstallScope"]; got != "perMachine" {
		t.Errorf("BuilderInstallScope = %q, want perMachine", got)
	}

	unchanged := `(NOT INSTALLDIR OR INSTALLDIR ~= InstallScopeUser_INSTALLDIR OR INSTALLDIR ~= InstallScopeMachine_INSTALLDIR)`
	want := []string{
		`[ALLUSERS] 1 [BuilderInstallScope="perMachine"]`,
		`[ALLUSERS] {} [NOT BuilderInstallScope="perMachine"]`,
		`[MSIINSTALLPERUSER] 1 [NOT BuilderInstallScope="perMachine"]`,
		`[MSIINSTALLPERUSER] {} [BuilderInstallScope="perMachine"]`,
		`[InstallScopeDir_INSTALLDIR] [InstallScopeUser_INSTALLDIR] [NOT BuilderInstallScope="perMachine"]`,
		`[InstallScopeDir_INSTALLDIR] [InstallScopeMachine_INSTALLDIR] [BuilderInstallScope="perMachine"]`,
		"EndDialog Return [1]",
	}

	if got := eventsOf(t, res, "InstallScopeDlg", "Next"); !slices.Equal(got, want) {
		t.Errorf("Next events\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Next leaves INSTALLDIR alone: the folder is set right before
	// CostFinalize, so that an action after the page that restores a
	// folder into an empty INSTALLDIR still runs.
	numbers, conds := map[string]int{}, map[string]string{}

	for _, row := range rows(t, res.Database, "InstallUISequence") {
		if n, ok := row[2].(int); ok {
			numbers[fmt.Sprint(row[0])], conds[fmt.Sprint(row[0])] = n, fmt.Sprint(row[1])
		}
	}

	setter := "SetInstallScopeDir_INSTALLDIR"
	if numbers[setter] != numbers["CostFinalize"]-1 || conds[setter] != "InstallScopeDir_INSTALLDIR AND "+unchanged {
		t.Errorf("%s at %d under %q, CostFinalize at %d; want it right before CostFinalize under the chosen folder",
			setter, numbers[setter], conds[setter], numbers["CostFinalize"])
	}

	// The x86 package's folder under ProgramFilesFolder: per user below
	// LocalAppData, per machine from the environment, the 32-bit one on
	// 64-bit Windows, and the remembered folders over both.
	targets := map[string]string{}
	for _, row := range rows(t, res.Database, "CustomAction") {
		targets[fmt.Sprint(row[0])] = fmt.Sprint(row[3])
	}

	for action, target := range map[string]string{
		"SetInstallScopeUser_INSTALLDIR":               `[LocalAppDataFolder]Programs\Acme App\`,
		"SetInstallScopeMachine_INSTALLDIR":            `[%ProgramFiles]\Acme App\`,
		"SetInstallScopeMachine_INSTALLDIR_WOW":        `[%ProgramFiles(x86)]\Acme App\`,
		"SetInstallScopeUser_INSTALLDIR_Remembered":    `[INSTALLDIR_REMEMBERED_USER]`,
		"SetInstallScopeMachine_INSTALLDIR_Remembered": `[INSTALLDIR_REMEMBERED_MACHINE]`,
		"RestoreInstallDirUser":                        `[INSTALLDIR_REMEMBERED_USER]`,
		"RestoreInstallDirMachine":                     `[INSTALLDIR_REMEMBERED_MACHINE]`,
	} {
		if targets[action] != target {
			t.Errorf("%s sets %q, want %q", action, targets[action], target)
		}
	}

	if got := controlRow(t, res, "VerifyReadyDlg", "InstallElevated"); fmt.Sprint(got[7]) != strconv.Itoa(0x800003) {
		t.Errorf("elevated Install button %v, want attributes 0x800003", got)
	}
}

// TestScopePageOnlyForDual checks that a single-context package has no
// scope page.
func TestScopePageOnlyForDual(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUser", "x64", "ui: { enabled: true }\n")
	if got := nextOf(t, res, "WelcomeDlg", "Next"); fmt.Sprint(got) != fmt.Sprint(map[string]string{"1": "VerifyReadyDlg"}) {
		t.Errorf("WelcomeDlg Next goes to %v", got)
	}
}

// TestScopePageAlias checks that Next sets only the topmost public folder:
// CostFinalize, which runs after the page, resolves a public directory
// below it, such as one named ".", in the chosen context.
func TestScopePageAlias(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perMachineOrUser", "x64", `directories:
  - id: ProgramFiles64Folder
    children:
      - id: INSTALLDIR
        name: Acme App
        configurable: true
        children: [{ id: BINDIR, name: ., configurable: true }]
ui: { enabled: true }
`)

	targets := map[string]string{}
	for _, row := range rows(t, res.Database, "CustomAction") {
		targets[fmt.Sprint(row[0])] = fmt.Sprint(row[3])
	}

	for action, target := range map[string]string{
		"SetInstallScopeUser_INSTALLDIR":    `[LocalAppDataFolder]Programs\Acme App\`,
		"SetInstallScopeMachine_INSTALLDIR": `[%ProgramW6432]\Acme App\`,
		"SetInstallScopeUser_BINDIR":        "",
		"SetInstallScopeMachine_BINDIR":     "",
	} {
		if targets[action] != target {
			t.Errorf("%s sets %q, want %q", action, targets[action], target)
		}
	}
}

// TestScopePageFirst checks where the scope page runs: on its own after
// AppSearch, with FindRelatedProducts and LaunchConditions of the UI
// sequence after it, so that both see the chosen context (UI-11), all
// before costing, as ICE27 wants; the execute sequence keeps their numbers. A
// folder remembered only in the other context starts the page there.
func TestScopePageFirst(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUserOrMachine", "x64", "installDir: { remember: true }\nui: { enabled: true }\n"+
		"launchConditions: [{ condition: Installed OR VersionNT64, message: 64-bit only }]\n")

	numbers := func(table string) map[string]int {
		got := map[string]int{}

		for _, row := range rows(t, res.Database, table) {
			if n, ok := row[2].(int); ok {
				got[fmt.Sprint(row[0])] = n
			}
		}

		return got
	}

	uiSeq := numbers("InstallUISequence")
	order := []string{"AppSearch", "SetInstallScopeUser", "InstallScopeDlg", "FindRelatedProducts", "LaunchConditions", "CostInitialize", "WelcomeDlg"}

	for i := 1; i < len(order); i++ {
		if uiSeq[order[i-1]] == 0 || uiSeq[order[i]] <= uiSeq[order[i-1]] {
			t.Errorf("InstallUISequence: %s at %d, %s at %d, want the first before the second", order[i-1], uiSeq[order[i-1]], order[i], uiSeq[order[i]])
		}
	}

	if got := cell(t, res.Database, "InstallUISequence", "Condition", func(r []any) bool { return r[0] == "InstallScopeDlg" }); got != "NOT Installed" {
		t.Errorf("InstallScopeDlg condition %v, want NOT Installed", got)
	}

	execute := numbers("InstallExecuteSequence")
	if execute["FindRelatedProducts"] != 25 || execute["LaunchConditions"] != 100 {
		t.Errorf("InstallExecuteSequence: FindRelatedProducts %d, LaunchConditions %d, want 25 and 100",
			execute["FindRelatedProducts"], execute["LaunchConditions"])
	}

	if got := eventsOf(t, res, "InstallScopeDlg", "Back"); len(got) != 0 {
		t.Errorf("Back events %v, want none", got)
	}

	if got := cell(t, res.Database, "InstallUISequence", "Condition", func(r []any) bool { return r[0] == "SetInstallScopePrevious" }); got != "NOT ALLUSERS AND INSTALLDIR_REMEMBERED_MACHINE AND NOT INSTALLDIR_REMEMBERED_USER" {
		t.Errorf("SetInstallScopePrevious condition %v", got)
	}

	if uiSeq["SetInstallScopePrevious"] <= uiSeq["SetInstallScopeUser"] || uiSeq["SetInstallScopePrevious"] >= uiSeq["InstallScopeDlg"] {
		t.Errorf("SetInstallScopePrevious at %d, want between SetInstallScopeUser (%d) and the page (%d)",
			uiSeq["SetInstallScopePrevious"], uiSeq["SetInstallScopeUser"], uiSeq["InstallScopeDlg"])
	}
}

// An action of the UI sequence placed relative to FindRelatedProducts or
// LaunchConditions follows the page even when the package needs neither.
func TestScopePageAnchors(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perMachineOrUser", "x64", "upgrade: false\nui: { enabled: true }\ncustomActions:\n"+
		"  - { id: AfterLC, setProperty: { name: BAR, value: \"1\" }, schedule: [{ sequence: ui, after: LaunchConditions }] }\n"+
		"  - { id: AfterFind, setProperty: { name: FOO, value: \"1\" }, schedule: [{ sequence: ui, after: FindRelatedProducts }] }\n")

	uiSeq := map[string]int{}

	for _, row := range rows(t, res.Database, "InstallUISequence") {
		if n, ok := row[2].(int); ok {
			uiSeq[fmt.Sprint(row[0])] = n
		}
	}

	order := []string{"AppSearch", "InstallScopeDlg", "FindRelatedProducts", "AfterFind", "LaunchConditions", "AfterLC", "CostInitialize"}

	for i := 1; i < len(order); i++ {
		if uiSeq[order[i-1]] == 0 || uiSeq[order[i]] <= uiSeq[order[i-1]] {
			t.Errorf("InstallUISequence: %s at %d, %s at %d, want the first before the second", order[i-1], uiSeq[order[i-1]], order[i], uiSeq[order[i]])
		}
	}
}

// TestScopePageNestedRemembered checks that a remembered install dir below
// another public folder keeps its setters: the restore actions set it in
// the context the package opened in, before the page.
func TestScopePageNestedRemembered(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUserOrMachine", "x64", `installDir: { remember: true }
directories:
  - id: ProgramFiles64Folder
    children:
      - id: COMPANYDIR
        name: Acme
        configurable: true
        children: [{ id: INSTALLDIR, name: App, configurable: true }]
ui: { enabled: true }
`)

	events := strings.Join(eventsOf(t, res, "InstallScopeDlg", "Next"), "\n")
	for _, want := range []string{"[InstallScopeDir_COMPANYDIR] [InstallScopeUser_COMPANYDIR]", "[InstallScopeDir_INSTALLDIR] [InstallScopeMachine_INSTALLDIR]"} {
		if !strings.Contains(events, want) {
			t.Errorf("Next events lack %s:\n%s", want, events)
		}
	}
}

// TestScopePagePreselect checks ui.scopePage: its conditions set the
// page's property after the context and remembered-folder setters, perUser
// before perMachine, and before the page.
func TestScopePagePreselect(t *testing.T) {
	t.Parallel()

	res := buildScope(t, "perUserOrMachine", "x64", "installDir: { remember: true }\n"+
		"ui: { enabled: true, scopePage: { perMachine: PREVMACHINE, perUser: NOT PREVMACHINE } }\n")

	seq := map[string]int{}
	conds := map[string]string{}

	for _, row := range rows(t, res.Database, "InstallUISequence") {
		if n, ok := row[2].(int); ok {
			seq[fmt.Sprint(row[0])] = n
			conds[fmt.Sprint(row[0])] = fmt.Sprint(row[1])
		}
	}

	order := []string{"SetInstallScopeUser", "SetInstallScopePrevious", "SetInstallScopePreferUser", "SetInstallScopePreferMachine", "InstallScopeDlg"}
	for i := 1; i < len(order); i++ {
		if seq[order[i-1]] == 0 || seq[order[i]] <= seq[order[i-1]] {
			t.Errorf("InstallUISequence: %s at %d, %s at %d, want the first before the second", order[i-1], seq[order[i-1]], order[i], seq[order[i]])
		}
	}

	if conds["SetInstallScopePreferMachine"] != "PREVMACHINE" || conds["SetInstallScopePreferUser"] != "NOT PREVMACHINE" {
		t.Errorf("conditions %q and %q", conds["SetInstallScopePreferMachine"], conds["SetInstallScopePreferUser"])
	}

	targets := map[string]string{}
	for _, row := range rows(t, res.Database, "CustomAction") {
		targets[fmt.Sprint(row[0])] = fmt.Sprint(row[2]) + "=" + fmt.Sprint(row[3])
	}

	if targets["SetInstallScopePreferMachine"] != "BuilderInstallScope=perMachine" || targets["SetInstallScopePreferUser"] != "BuilderInstallScope=perUser" {
		t.Errorf("targets %q and %q", targets["SetInstallScopePreferMachine"], targets["SetInstallScopePreferUser"])
	}
}
