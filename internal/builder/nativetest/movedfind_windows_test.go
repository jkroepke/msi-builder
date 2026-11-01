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

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

// movedFindConfig is a perUserOrMachine product whose per-machine install
// leaves an HKLM marker. Each version searches for the marker and sets
// ALLUSERS=1 when it finds one, so that it installs in the context of the
// previous install, as Far Manager and ImageGlass do; %s is the
// standardActions section, empty or one that moves FindRelatedProducts
// behind that setter. Each case replaces MovedFind and the last digit of the
// upgrade code, so that a version a failed cleanup leaves behind does not
// meet the next case.
const movedFindConfig = `schema: msi-builder/v1
product: { name: MsiBuilderMovedFind, manufacturer: msi-builder tests, version: VERSION, upgradeCode: 9b4e2d70-6c1a-4f38-a5d9-3e8b7c2f1a64 }
package: { platform: x64, scope: perUserOrMachine }
installDir: { name: MsiBuilderMovedFind }
components:
  - files: [{ source: app.exe }]
  - id: Marker
    directory: INSTALLDIR
    scope: perMachine
    registry: [{ root: HKLM, key: Software\MsiBuilderTest\MovedFind, name: Machine, value: "1" }]
properties:
  PREVMACHINE:
    search: { registry: { root: HKLM, key: Software\MsiBuilderTest\MovedFind, name: Machine } }
customActions:
  - id: SetAllUsersFromPrevious
    setProperty: { name: ALLUSERS, value: "1" }
    schedule:
      - { sequence: execute, after: AppSearch, condition: PREVMACHINE }
      - { sequence: ui, after: AppSearch, condition: PREVMACHINE }
`

const moveFind = `standardActions:
  - action: FindRelatedProducts
    schedule:
      - { sequence: execute, after: SetAllUsersFromPrevious }
      - { sequence: ui, after: SetAllUsersFromPrevious }
`

// registered returns where a product is registered: "user", "machine",
// "both" or "none".
func registered(t *testing.T, productCode string) string {
	t.Helper()

	return where(regHas(t, "64", `HKCU\Software\Microsoft\Installer\Products\`+squash(productCode), ""),
		regHas(t, "64", `HKLM\SOFTWARE\Classes\Installer\Products\`+squash(productCode), ""))
}

// TestMsiexecMovedFindRelatedProducts upgrades a per-machine install of a
// perUserOrMachine product with a version that starts in the per-user
// context and sets ALLUSERS=1 from a search (UPG-14). FindRelatedProducts
// only finds products in the context it runs in, so at its default number
// (25, before AppSearch) it misses the per-machine v1; moved behind the
// setter it finds and removes it. Silently, and with the reduced UI, which
// runs the UI sequence, where FindRelatedProducts keeps the execute
// sequence from running it again.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecMovedFindRelatedProducts(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	inputs := map[string][]byte{"app.exe": app}

	for i, tc := range []struct {
		name, ui string
		moved    bool
		// oldAfter is where v1 is registered after the v2 install.
		oldAfter string
	}{
		{"default /qn", "/qn", false, "machine"},
		{"moved /qn", "/qn", true, "none"},
		{"default /qr", "/qr", false, "machine"},
		{"moved /qr", "/qr", true, "none"},
	} {
		t.Run("UPG-14 "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := fmt.Sprintf("MovedFind%d", i)
			config := func(version string) string {
				doc := strings.Replace(movedFindConfig, "VERSION", version, 1)
				doc = strings.ReplaceAll(doc, "MovedFind", name)
				doc = strings.Replace(doc, "3e8b7c2f1a64", fmt.Sprintf("3e8b7c2f1a6%d", i), 1)

				if tc.moved {
					doc += moveFind
				}

				return doc
			}

			older, oldRes := buildConfigResult(t, mkdir(t, dir, "v1"), "v1", config("1.0.0"), inputs)
			newer, newRes := buildConfigResult(t, mkdir(t, dir, "v2"), "v2", config("1.1.0"), inputs)

			defer func() {
				for _, res := range []*builder.Result{newRes, oldRes} {
					removed := filepath.Join(dir, "remove-"+res.ProductCode+".log")
					_ = exec.CommandContext(context.Background(), "msiexec", "/x", res.ProductCode, "/qn", "/norestart", "/l*v", removed).Run() //nolint:gosec // test-controlled arguments

					if left := registered(t, res.ProductCode); left != "none" {
						t.Errorf("%s is still registered in %s after the cleanup:\n%s", res.ProductCode, left, readLog(removed))
					}
				}

				_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKLM\Software\MsiBuilderTest\`+name, "/f", "/reg:64").Run() //nolint:gosec // test-controlled arguments
			}()

			msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", older, `MSIINSTALLPERUSER=""`)

			if got := registered(t, oldRes.ProductCode); got != "machine" {
				t.Fatalf("v1 is registered in %s, want machine", got)
			}

			log := filepath.Join(dir, "v2.log")

			out, err := exec.CommandContext(t.Context(), "msiexec", "/i", newer, tc.ui, "/norestart", "/l*v", log).CombinedOutput()
			if err != nil {
				t.Fatalf("msiexec /i v2 %s: %v\n%s\n%s", tc.ui, err, out, readLog(log))
			}

			text := readLog(log)

			for line := range strings.Lines(text) {
				if strings.Contains(line, "Doing action: FindRelatedProducts") || strings.Contains(line, "Skipping action: FindRelatedProducts") ||
					strings.Contains(line, "FindRelatedProducts:") || strings.Contains(line, "Doing action: SetAllUsersFromPrevious") ||
					strings.Contains(line, "PROPERTY CHANGE: Adding ALLUSERS") || strings.Contains(line, "PROPERTY CHANGE: Modifying ALLUSERS") {
					t.Logf("%s", strings.TrimSpace(line))
				}
			}

			oldAfter, newAfter := registered(t, oldRes.ProductCode), registered(t, newRes.ProductCode)
			t.Logf("UPG-14 %s: v1 %s, v2 %s, server ALLUSERS %q, OLDERVERSIONBEINGUPGRADED %q", tc.name, oldAfter, newAfter,
				serverProperties(text)["ALLUSERS"], serverProperties(text)["OLDERVERSIONBEINGUPGRADED"])

			if newAfter != "machine" {
				t.Errorf("v2 is registered in %s, want machine (the setter found the marker)", newAfter)
			}

			if oldAfter != tc.oldAfter {
				t.Errorf("v1 is registered in %s after the v2 install, want %s", oldAfter, tc.oldAfter)
			}

			if _, err := os.Stat(filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilder"+name, "app.exe")); err != nil {
				t.Errorf("app.exe after the v2 install: %v", err)
			}
		})
	}
}
