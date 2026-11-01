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
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

const probeRegFileKey = `HKLM\SOFTWARE\MsiBuilderTest\RegFile`

// probeRegFileConfig: every RegLocator row is turned into a file search
// (Type 1 + 16) without a Signature row after the build.
const probeRegFileConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRegFile, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 1c7e3a52-9d40-4b6f-8a21-5e3f0d9b7c48 }
package: { platform: x64 }
installDir: { name: MsiBuilderRegFile }
components:
  - files: [{ source: app.exe }]
properties:
  EXISTFILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile, type: directory } } }
  MISSINGFILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingFile, type: directory } } }
  MISSINGDIR: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingDir, type: directory } } }
  FOLDER: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: Folder, type: directory } } }
  FOLDERSLASH: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: FolderSlash, type: directory } } }
  QUOTED: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: Quoted, type: directory } } }
  NOVALUE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: NoSuchValue, type: directory } } }
  NESTED_EXIST: { value: none, search: { file: { name: marker.txt, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile } } } } }
  NESTED_MISSING: { value: none, search: { file: { name: marker.txt, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingFile } } } } }
  NESTED_OTHER: { value: none, search: { file: { name: other.txt, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile } } } } }
  NESTED_SUB: { value: none, search: { directory: { path: sub, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile } } } } }
`

// TestProbeRegFileFolder records what RegLocator Type 1 without a
// Signature row returns (SEARCH-10 probe).
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestProbeRegFileFolder(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	root := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderRegFile")
	data := filepath.Join(root, "data")
	marker := filepath.Join(data, "marker.txt")

	built := buildLocatorPackage(t, mkdir(t, work, "pkg"), "regfile.msi", probeRegFileConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})
	pkg := editPackage(t, built, func(t *testing.T, database *msidb.Database) {
		t.Helper()

		reg := database.Table("RegLocator")
		typeCol := reg.Column("Type")

		for _, row := range reg.Rows {
			row[typeCol] = 1 | 16
		}

		t.Logf("RegLocator rows: %d", len(reg.Rows))
	})

	cleanup := func() {
		_ = exec.CommandContext(ctx, "msiexec", "/x", pkg, "/qn", "/norestart", "REBOOT=ReallySuppress").Run()
		_ = os.RemoveAll(root)
		_ = exec.CommandContext(ctx, "reg.exe", "delete", probeRegFileKey, "/f", "/reg:64").Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	writeFile(t, marker)

	if err := os.MkdirAll(filepath.Join(data, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{
		"ExistFile":   marker,
		"MissingFile": filepath.Join(data, "missing.exe"),
		"MissingDir":  filepath.Join(root, "missing", "x.exe"),
		"Folder":      data,
		"FolderSlash": data + `\`,
		"Quoted":      `"` + marker + `"`,
	} {
		regSet(t, "64", probeRegFileKey, name, value)
	}

	log := filepath.Join(work, "install.log")
	code := msiexecCode(t, log, "/i", pkg)
	text := readLog(log)
	props := serverProperties(text)

	var report strings.Builder

	fmt.Fprintf(&report, "install exit %d\n", code)

	for _, name := range []string{"EXISTFILE", "MISSINGFILE", "MISSINGDIR", "FOLDER", "FOLDERSLASH", "QUOTED", "NOVALUE", "NESTED_EXIST", "NESTED_MISSING", "NESTED_OTHER", "NESTED_SUB"} {
		fmt.Fprintf(&report, "SEARCH-10 %s = %q\n", name, props[name])
	}

	report.WriteString(excerpt(text, "AppSearch", "_FOUND"))
	t.Errorf("probe report:\n%s", report.String())
}

const probeScopeConfig = `schema: msi-builder/v1
product: { name: MsiBuilderScopeLC, manufacturer: msi-builder tests, version: VERSION, upgradeCode: 4a8d2c61-7e3b-4f90-b5a2-6c1e9d3f7b5N }
package: { platform: x64, scope: perUserOrMachine }
installDir: { name: MsiBuilderScopeLC }
ui: { enabled: true }
components:
  - files: [{ source: app.exe }]
  - id: Marker
    directory: INSTALLDIR
    scope: perMachine
    registry: [{ root: HKLM, key: Software\MsiBuilderTest\ScopeLC, name: Machine, value: "1" }]
properties:
  PREVMACHINE:
    search: { registry: { root: HKLM, key: Software\MsiBuilderTest\ScopeLC, name: Machine } }
`

const probeScopeLC = `launchConditions:
  - condition: Installed OR ALLUSERS = 1 OR NOT PREVMACHINE
    message: Probe launch condition, installed for everyone.
`

// deferToServer conditions FindRelatedProducts and LaunchConditions in
// InstallUISequence so that they do not run in a full-UI first install.
func deferToServer(t *testing.T, database *msidb.Database) {
	t.Helper()

	seq := database.Table("InstallUISequence")
	actionCol, condCol := seq.Column("Action"), seq.Column("Condition")
	edited := 0

	for _, row := range seq.Rows {
		if a := row[actionCol]; a == "FindRelatedProducts" || a == "LaunchConditions" {
			row[condCol] = "UILevel < 5 OR Installed"
			edited++
		}
	}

	t.Logf("deferred %d UI actions", edited)
}

// TestProbeScopeLaunchConditions upgrades a per-machine v1 through the
// wizard of a perUserOrMachine v2 (UI-11 probe).
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestProbeScopeLaunchConditions(t *testing.T) {
	requireFullUI(t)
	waitForMsiexec(t)

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	inputs := map[string][]byte{"app.exe": app}

	var report strings.Builder

	for i, tc := range []struct {
		name     string
		lc, edit bool
		choose   string
	}{
		{"lc all users", true, false, "^Install for all users"},
		{"no lc all users", false, false, "^Install for all users"},
		{"deferred lc all users", true, true, "^Install for all users"},
		{"deferred lc just you", true, true, ""},
		{"deferred no lc all users", false, true, "^Install for all users"},
	} {
		dir := t.TempDir()
		name := fmt.Sprintf("ScopeLC%d", i)
		config := func(version string) string {
			doc := strings.Replace(probeScopeConfig, "VERSION", version, 1)
			if tc.lc {
				doc += probeScopeLC
			}

			doc = strings.ReplaceAll(doc, "ScopeLC", name)

			return strings.Replace(doc, "6c1e9d3f7b5N", fmt.Sprintf("6c1e9d3f7b5%d", i), 1)
		}

		older, oldRes := buildConfigResult(t, mkdir(t, dir, "v1"), "v1", config("1.0.0"), inputs)
		newer, newRes := buildConfigResult(t, mkdir(t, dir, "v2"), "v2", config("1.1.0"), inputs)

		if tc.edit {
			newer = editPackage(t, newer, deferToServer)
		}

		msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", older, `MSIINSTALLPERUSER=""`)

		log := filepath.Join(dir, "v2.log")
		exit, seen := fullUIDriven(t, dir, tc.choose, "^(Next|Install|Finish|OK)$", "/i", newer, "/l*v", log)
		text := readLog(log)

		fmt.Fprintf(&report, "=== UI-11 %s: exit %d, v1 %s, v2 %s\nUI:\n%s\n", tc.name, exit,
			registered(t, oldRes.ProductCode), registered(t, newRes.ProductCode), seen)

		for line := range strings.Lines(text) {
			if strings.Contains(line, "Doing action: FindRelatedProducts") || strings.Contains(line, "Skipping action: FindRelatedProducts") ||
				strings.Contains(line, "Doing action: LaunchConditions") || strings.Contains(line, "Skipping action: LaunchConditions") ||
				strings.Contains(line, "FindRelatedProducts:") || strings.Contains(line, "Action start") && strings.Contains(line, "LaunchConditions") ||
				strings.Contains(line, "Probe launch condition") || strings.Contains(line, "RemoveExistingProducts") ||
				strings.Contains(line, "Action ended") && (strings.Contains(line, "LaunchConditions") || strings.Contains(line, "INSTALL.")) {
				report.WriteString(strings.TrimSpace(line) + "\n")
			}
		}

		for _, res := range []*builder.Result{newRes, oldRes} {
			_ = exec.CommandContext(context.Background(), "msiexec", "/x", res.ProductCode, "/qn", "/norestart", "REBOOT=ReallySuppress").Run() //nolint:gosec // test-controlled arguments
			fmt.Fprintf(&report, "after cleanup %s: %s\n", res.ProductCode, registered(t, res.ProductCode))
		}

		_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKLM\Software\MsiBuilderTest\`+name, "/f", "/reg:64").Run() //nolint:gosec // test-controlled arguments
	}

	t.Errorf("probe report:\n%s", report.String())
}
