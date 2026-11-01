//go:build windows

package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// scopePageConfig is a perUserOrMachine product with the wizard and an
// action after the scope page that restores a folder saved under HKCU into
// an empty INSTALLDIR, as packages that remember their folder themselves
// do. SCOPEPAGE is replaced per case.
const scopePageConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSCOPEPAGE, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 5b9e3d72-8f4c-4a01-86b3-7d2f0e4a8c6N }
package: { platform: x64, scope: perUserOrMachine }
installDir: { name: MsiBuilderSCOPEPAGE }
ui: { enabled: true }
components:
  - files: [{ source: app.exe }]
properties:
  SAVEDDIR:
    search: { registry: { root: HKCU, key: Software\MsiBuilderTest\SCOPEPAGE, name: Dir, type: directory } }
  PREVMACHINE:
    search: { registry: { root: HKLM, key: Software\MsiBuilderTest\SCOPEPAGE, name: Machine } }
customActions:
  - id: RestoreSaved
    setProperty: { name: INSTALLDIR, value: '[SAVEDDIR]' }
    schedule:
      - { sequence: ui, after: LaunchConditions, condition: SAVEDDIR AND NOT INSTALLDIR }
      - { sequence: execute, after: AppSearch, condition: SAVEDDIR AND NOT INSTALLDIR }
launchConditions:
  - condition: Installed OR ALLUSERS = 1 OR NOT PREVMACHINE
    message: Already installed for everyone.
`

// TestFullUIScopePage runs the scope page's Next with the full UI (UI-12).
// Next once set an empty INSTALLDIR to the chosen context's default
// folder, so an action between the page and costing that restores a folder
// into an empty INSTALLDIR never ran; it now only records the folder, which
// an action right before CostFinalize sets. ui.scopePage.perMachine
// preselects all users, so accepting the defaults passes a launch
// condition that refuses a per-user install next to a per-machine copy,
// which without it ended the install (UI-11).
func TestFullUIScopePage(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	inputs := map[string][]byte{"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})}

	for i, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		name, choose, extra string
		saved, machine      bool
		exit                int
		context, folder     string
	}{
		{"folder restored after the page", "", "", true, false, 0, "user", "custom"},
		{"all users without a saved folder", "^Install for all users", "", false, false, 0, "machine", "machine"},
		{"just you without a saved folder", "", "", false, false, 0, "user", "user"},
		{"perMachine preselected", "", "  scopePage: { perMachine: PREVMACHINE }\n", false, true, 0, "machine", "machine"},
	} {
		t.Run("UI-12 "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := fmt.Sprintf("ScopePage%d", i)
			doc := strings.NewReplacer("SCOPEPAGE", name, "7d2f0e4a8c6N", fmt.Sprintf("7d2f0e4a8c6%d", i)).Replace(scopePageConfig)

			if tc.extra != "" {
				doc = strings.Replace(doc, "ui: { enabled: true }\n", "ui:\n  enabled: true\n"+tc.extra, 1)
			}

			pkg, res := buildConfigResult(t, dir, name, doc, inputs)
			folders := map[string]string{
				"custom":  filepath.Join(os.Getenv("LOCALAPPDATA"), "MsiBuilderCustom"+name) + `\`,
				"user":    filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "MsiBuilder"+name) + `\`,
				"machine": filepath.Join(os.Getenv("ProgramW6432"), "MsiBuilder"+name) + `\`,
			}
			key := `Software\MsiBuilderTest\` + name

			cleanup := func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", res.ProductCode, "/qn", "/norestart", "REBOOT=ReallySuppress").Run() //nolint:gosec // test-controlled arguments
				_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKCU\`+key, "/f").Run()                                       //nolint:gosec // test-controlled arguments
				_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKLM\`+key, "/f", "/reg:64").Run()                            //nolint:gosec // test-controlled arguments
				_ = os.RemoveAll(folders["custom"])
			}
			cleanup()
			defer cleanup()

			if err := os.MkdirAll(folders["custom"], 0o750); err != nil {
				t.Fatal(err)
			}

			if tc.saved {
				regAdd(t, `HKCU\`+key, "Dir", folders["custom"])
			}

			if tc.machine {
				regAdd(t, `HKLM\`+key, "Machine", "1", "/reg:64")
			}

			log := filepath.Join(dir, "install.log")
			exit, seen := fullUIDriven(t, dir, tc.choose, "^(Next|Install|Finish|OK)$", "/i", pkg, "/l*v", log, "/norestart", "REBOOT=ReallySuppress")
			text := readLog(log)
			installDir := serverProperties(text)["INSTALLDIR"]
			scope := registered(t, res.ProductCode)

			t.Logf("UI-12 %s: exit %d, registered %s, INSTALLDIR %s\nUI:\n%s\nlog excerpt:\n%s", tc.name, exit, scope, installDir, seen,
				excerpt(text, "RestoreSaved", "InstallScope", "Already installed"))

			if exit != tc.exit || scope != tc.context {
				t.Errorf("exit %d, registered %s; want %d, %s", exit, scope, tc.exit, tc.context)
			}

			if !strings.EqualFold(installDir, folders[tc.folder]) {
				t.Errorf("INSTALLDIR = %q, want the %s folder %q", installDir, tc.folder, folders[tc.folder])
			}
		})
	}
}

// regAdd writes a string value with reg.exe.
func regAdd(t *testing.T, key, name, value string, args ...string) {
	t.Helper()

	args = append([]string{"add", key, "/v", name, "/d", value, "/f"}, args...)
	if out, err := exec.CommandContext(t.Context(), "reg.exe", args...).CombinedOutput(); err != nil { //nolint:gosec // test-controlled arguments
		t.Fatalf("reg %v: %v\n%s", args, err, out)
	}
}
