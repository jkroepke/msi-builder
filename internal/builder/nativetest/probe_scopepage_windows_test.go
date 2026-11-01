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

// Temporary probe: what the scope page does to a folder that an action after
// the page restores, and what accepting the preselected scope does when a
// launch condition refuses that scope. Deleted before the pull request is
// ready.
const probeScopeConfig = `schema: msi-builder/v1
product: { name: MsiBuilderScopeProbeN, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 5b9e3d72-8f4c-4a01-c6b3-7d2f0e4a8c6N }
package: { platform: x64, scope: perUserOrMachine }
installDir: { name: MsiBuilderScopeProbeN }
ui: { enabled: true }
components:
  - files: [{ source: app.exe }]
properties:
  SAVEDDIR:
    search: { registry: { root: HKCU, key: Software\MsiBuilderTest\ScopeProbeN, name: Dir, type: directory } }
  PREVMACHINE:
    search: { registry: { root: HKLM, key: Software\MsiBuilderTest\ScopeProbeN, name: Machine } }
customActions:
  - id: RestoreSaved
    setProperty: { name: INSTALLDIR, value: '[SAVEDDIR]' }
    schedule:
      - { sequence: ui, after: LaunchConditions, condition: SAVEDDIR AND NOT INSTALLDIR }
      - { sequence: execute, after: AppSearch, condition: SAVEDDIR AND NOT INSTALLDIR }
`

const probeScopeLaunch = `launchConditions:
  - condition: Installed OR ALLUSERS = 1 OR NOT PREVMACHINE
    message: Already installed for everyone.
`

func TestProbeScopePage(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	inputs := map[string][]byte{"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})}

	var report strings.Builder

	for i, tc := range []struct {
		name           string
		saved, machine bool
	}{
		{"remembered folder restored after the page", true, false},
		{"machine copy marker, launch condition, defaults", false, true},
	} {
		dir := t.TempDir()
		name := fmt.Sprintf("ScopeProbe%d", i)
		doc := strings.NewReplacer("ScopeProbeN", name, "7d2f0e4a8c6N", fmt.Sprintf("7d2f0e4a8c6%d", i)).Replace(probeScopeConfig)

		if tc.machine {
			doc += probeScopeLaunch
		}

		pkg, res := buildConfigResult(t, dir, name, doc, inputs)
		custom := filepath.Join(os.Getenv("LOCALAPPDATA"), "MsiBuilderCustom"+name) + `\`

		if err := os.MkdirAll(custom, 0o750); err != nil {
			t.Fatal(err)
		}

		if tc.saved {
			_ = exec.CommandContext(context.Background(), "reg.exe", "add", `HKCU\Software\MsiBuilderTest\`+name, "/v", "Dir", "/d", custom, "/f").Run() //nolint:gosec // test-controlled arguments
		}

		if tc.machine {
			_ = exec.CommandContext(context.Background(), "reg.exe", "add", `HKLM\Software\MsiBuilderTest\`+name, "/v", "Machine", "/d", "1", "/f", "/reg:64").Run() //nolint:gosec // test-controlled arguments
		}

		log := filepath.Join(dir, "install.log")
		exit, seen := fullUIDriven(t, dir, "", "^(Next|Install|Finish|OK)$", "/i", pkg, "/l*v", log, "/norestart", "REBOOT=ReallySuppress")
		text := readLog(log)

		_, customErr := os.Stat(filepath.Join(custom, "app.exe"))
		defaultDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", name)
		_, defaultErr := os.Stat(filepath.Join(defaultDir, "app.exe"))

		fmt.Fprintf(&report, "\n=== %s: exit %d, registered %s, app.exe in custom folder %v, in default per-user folder %v\nUI:\n%s\nlog excerpt:\n%s\n",
			tc.name, exit, registered(t, res.ProductCode), customErr == nil, defaultErr == nil, seen,
			excerpt(text, "RestoreSaved", "INSTALLDIR", "InstallScope", "SAVEDDIR", "Already installed", "LaunchConditions"))

		_ = exec.CommandContext(context.Background(), "msiexec", "/x", res.ProductCode, "/qn", "/norestart", "REBOOT=ReallySuppress").Run() //nolint:gosec // test-controlled arguments
		_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKCU\Software\MsiBuilderTest\`+name, "/f").Run()                //nolint:gosec // test-controlled arguments
		_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKLM\Software\MsiBuilderTest\`+name, "/f", "/reg:64").Run()      //nolint:gosec // test-controlled arguments
		_ = os.RemoveAll(custom)
	}

	t.Errorf("probe report:%s", report.String())
}
