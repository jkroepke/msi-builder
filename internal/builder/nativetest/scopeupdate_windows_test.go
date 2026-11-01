//go:build windows

package nativetest_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

// scopeUpdateConfig is a perUserOrMachine product with the wizard and an
// HKLM marker that only its per-machine install writes; SCOPEUPDATE is
// replaced per case, so that each case has its own product, upgrade code
// and marker key.
const scopeUpdateConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSCOPEUPDATE, manufacturer: msi-builder tests, version: VERSION, upgradeCode: 4a8d2c61-7e3b-4f90-b5a2-6c1e9d3f7b5N }
package: { platform: x64, scope: perUserOrMachine }
installDir: { name: MsiBuilderSCOPEUPDATE, remember: REMEMBER }
ui: { enabled: true }
components:
  - files: [{ source: app.exe }]
  - id: Marker
    directory: INSTALLDIR
    scope: perMachine
    registry: [{ root: HKLM, key: Software\MsiBuilderTest\SCOPEUPDATE, name: Machine, value: "1" }]
properties:
  PREVMACHINE:
    search: { registry: { root: HKLM, key: Software\MsiBuilderTest\SCOPEUPDATE, name: Machine } }
`

// scopeUpdateLaunch blocks an install for the current user while a
// per-machine copy exists, as packages that never change the context of
// an existing install do.
const scopeUpdateLaunch = `launchConditions:
  - condition: Installed OR ALLUSERS = 1 OR NOT PREVMACHINE
    message: Already installed for everyone.
`

// TestFullUIScopeUpdate updates a per-machine v1 through the wizard of a
// perUserOrMachine v2 (UI-11). The scope page runs before the welcome page
// and FindRelatedProducts and LaunchConditions of the UI sequence after
// it: in a full-UI install only the client's FindRelatedProducts counts,
// and before the page it ran in the per-user context and missed v1, which
// stayed installed next to v2, while a launch condition on ALLUSERS ended
// the install before the page appeared. With a remembered install folder
// the page starts on the context of the previous install.
func TestFullUIScopeUpdate(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	inputs := map[string][]byte{"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})}

	for i, tc := range []struct {
		name, choose       string
		launch, remember   bool
		exit               int
		oldAfter, newAfter string
	}{
		{"all users", "^Install for all users", false, false, 0, "none", "machine"},
		{"all users with launch condition", "^Install for all users", true, false, 0, "none", "machine"},
		{"just you with launch condition", "", true, false, 1603, "machine", "none"},
		{"remembered machine folder", "", true, true, 0, "none", "machine"},
	} {
		t.Run("UI-11 "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := fmt.Sprintf("ScopeUpdate%d", i)
			config := func(version string) string {
				doc := strings.NewReplacer("VERSION", version, "SCOPEUPDATE", name, "REMEMBER", fmt.Sprint(tc.remember),
					"6c1e9d3f7b5N", fmt.Sprintf("6c1e9d3f7b5%d", i)).Replace(scopeUpdateConfig)
				if tc.launch {
					doc += scopeUpdateLaunch
				}

				return doc
			}

			older, oldRes := buildConfigResult(t, mkdir(t, dir, "v1"), "v1", config("1.0.0"), inputs)
			newer, newRes := buildConfigResult(t, mkdir(t, dir, "v2"), "v2", config("1.1.0"), inputs)

			defer func() {
				for _, res := range []*builder.Result{newRes, oldRes} {
					_ = exec.CommandContext(context.Background(), "msiexec", "/x", res.ProductCode, "/qn", "/norestart", "REBOOT=ReallySuppress").Run() //nolint:gosec // test-controlled arguments

					if left := registered(t, res.ProductCode); left != "none" {
						t.Errorf("%s is still registered in %s after the cleanup", res.ProductCode, left)
					}
				}

				_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKLM\Software\MsiBuilderTest\`+name, "/f", "/reg:64").Run() //nolint:gosec // test-controlled arguments
			}()

			msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", older, `MSIINSTALLPERUSER=""`)

			log := filepath.Join(dir, "v2.log")
			exit, seen := fullUIDriven(t, dir, tc.choose, "^(Next|Install|Finish|OK)$", "/i", newer, "/l*v", log)
			text := readLog(log)
			oldAfter, newAfter := registered(t, oldRes.ProductCode), registered(t, newRes.ProductCode)

			t.Logf("UI-11 %s: exit %d, v1 %s, v2 %s\nUI:\n%s\nlog excerpt:\n%s", tc.name, exit, oldAfter, newAfter, seen,
				excerpt(text, "FindRelatedProducts", "LaunchConditions", "InstallScope", "Already installed", "RemoveExistingProducts"))

			if exit != tc.exit {
				t.Errorf("v2 exit %d, want %d", exit, tc.exit)
			}

			if oldAfter != tc.oldAfter || newAfter != tc.newAfter {
				t.Errorf("v1 is registered in %s and v2 in %s, want %s and %s", oldAfter, newAfter, tc.oldAfter, tc.newAfter)
			}

			// The page is the first window: the launch condition no longer
			// ends the install before it.
			if first, _, _ := strings.Cut(seen, "\n"); !strings.Contains(first, "Installation Scope") {
				t.Errorf("the first window is not the scope page: %s", first)
			}
		})
	}
}
