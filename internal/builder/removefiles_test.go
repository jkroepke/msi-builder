package builder_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/msidb"
)

// TestRemoveFiles writes one RemoveFile row per removeFiles entry, with a
// generated short name for a long pattern, and RemoveFile rows for the
// folders a component creates in the user profile.
func TestRemoveFiles(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - files:
      - source: app.exe
      - { source: tool.exe, name: my-application.exe }
    removeFiles:
      - name: MY-APPLICATION.exe
      - name: '*.log'
      - { name: acme-server-*.log, on: both }
      - { id: StaleConfig, name: stale.txt, on: install }
      - {}
  - directory: ProgramMenuFolder/Acme/Tools
    registry: [{ root: HKCU, key: Software\Acme, name: tools, type: dword, value: 1 }]
`)
	database := mustBuild(t, cfg).Database

	byName := make(map[any][]any)

	for _, row := range rows(t, database, "RemoveFile") {
		byName[row[2]] = row
	}

	if row := byName["*.log"]; row == nil || fmt.Sprint(row[4]) != "2" {
		t.Errorf("*.log row %v", row)
	}

	var long []any

	for name, row := range byName {
		if s, ok := name.(string); ok && strings.HasSuffix(s, "|acme-server-*.log") {
			long = row
		}
	}

	if long == nil || fmt.Sprint(long[4]) != "3" {
		t.Errorf("long pattern row %v in %v", long, byName)
	}

	if row := byName["stale.txt"]; row == nil || row[0] != "StaleConfig" || fmt.Sprint(row[4]) != "1" {
		t.Errorf("stale.txt row %v", row)
	}

	// A packaged file's long name gets that file's short name.
	file := rowWhere(t, database, "File", func(r []any) bool { return r[0] == "my_application.exe" })

	var same []any

	for _, row := range rows(t, database, "RemoveFile") {
		if row[2] == file[2] {
			same = row
		}
	}

	if same == nil {
		t.Errorf("no RemoveFile row with the File name %v: %v", file[2], byName)
	}

	folders := 0

	for _, row := range rows(t, database, "RemoveFile") {
		if row[2] == nil && fmt.Sprint(row[4]) == "2" {
			folders++
		}
	}

	// The empty entry, plus Acme and Tools below ProgramMenuFolder.
	if folders != 3 {
		t.Errorf("%d folder rows, want 3: %v", folders, byName)
	}
}

// TestRemoveFilesDuplicateFolder warns about a hand-written folder entry for
// a folder that a generated row already removes (round 4: renderdoc,
// rakudo), and not about one that also removes the folder on install.
func TestRemoveFilesDuplicateFolder(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - files:
      - source: app.exe
        shortcuts: [{ directory: ProgramMenuFolder/Acme }]
  - directory: ProgramMenuFolder/Acme
    registry: [{ root: HKCU, key: Software\Acme, name: menu, type: dword, value: 1 }]
    removeFiles: [{}, { on: install }, { on: both }]
`)

	var warned []string

	for _, d := range mustBuild(t, cfg).Diagnostics {
		if strings.Contains(d.Message, "already removes this folder") {
			warned = append(warned, d.Path)
		}
	}

	if want := []string{"components[1].removeFiles[0]"}; !slices.Equal(warned, want) {
		t.Errorf("warnings at %v, want %v", warned, want)
	}
}

// TestFolderCases writes a directory named "." as DefaultDir "." with its
// parent's short-name set and component GUID path, a WindowsVolume row
// below TARGETDIR, and a RemoveFile row whose DirProperty is a property.
func TestFolderCases(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
directories:
  - id: WindowsVolume
    children:
      - id: INSTALLDIR
        name: Hello
        configurable: true
        children: [{ id: BINDIR, name: . }]
properties:
  LOGDIR: { search: { registry: { root: HKLM, key: 'SOFTWARE\Example', name: Logs, type: directory } } }
components:
  - id: App
    files: [{ source: hello.exe }]
    removeFiles:
      - { name: 'application-*.log', property: LOGDIR }
      - { property: LOGDIR }
  - id: Tool
    directory: BINDIR
    files: [{ source: hello.exe, name: hello-tool.exe }]
`)

	byKey := func(id string) func([]any) bool { return func(r []any) bool { return r[0] == id } }

	if got := cell(t, database, "Directory", "DefaultDir", byKey("BINDIR")); got != "." {
		t.Errorf("BINDIR DefaultDir %v, want .", got)
	}

	if got := cell(t, database, "Directory", "Directory_Parent", byKey("WindowsVolume")); got != "TARGETDIR" {
		t.Errorf("WindowsVolume parent %v, want TARGETDIR", got)
	}

	var props int

	for _, row := range rows(t, database, "RemoveFile") {
		if row[3] == "LOGDIR" {
			props++

			if name, ok := row[2].(string); ok && !strings.HasSuffix(name, "|application-*.log") {
				t.Errorf("pattern %q has no generated short name part", name)
			}
		}
	}

	if props != 2 {
		t.Errorf("%d RemoveFile rows on LOGDIR, want 2: %v", props, rows(t, database, "RemoveFile"))
	}
}

// removeFilePropsYAML is a package of the scope with removeFiles entries
// on properties: LOGDIR from a search, CLEAN_DIR from an action whose
// value names SRC_DIR, and KEEP, which nothing removes; props adds to the
// properties.
func removeFilePropsYAML(scope, props string) string {
	return `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: ` + scope + ` }
properties:
  LOGDIR: { search: { registry: { root: HKLM, key: 'SOFTWARE\Example', name: Logs, type: directory } } }
  SRC_DIR: { value: 'C:\Example\' }
  KEEP: { value: "1" }
` + props + `components:
  - id: App
    files: [{ source: hello.exe }]
    removeFiles:
      - { name: '*.log', property: LOGDIR }
      - { name: '*.tmp', property: CLEAN_DIR }
customActions:
  - id: SetClean
    setProperty: { name: CLEAN_DIR, value: '[SRC_DIR]cache\' }
    schedule: [{ sequence: execute, after: CostFinalize }]
`
}

// TestRemoveFilePropertiesNotSecure checks that outside a per-user
// package the properties a removeFiles entry reads its folder from, and
// those an action sets them from, are not secure: a standard user who
// removes a feature in an elevated maintenance install could otherwise
// name any folder on the command line. A per-user package keeps them, and
// secure: true is refused.
func TestRemoveFilePropertiesNotSecure(t *testing.T) {
	t.Parallel()

	for scope, want := range map[string]bool{"perMachine": false, "perUserOrMachine": false, "perUser": true} {
		res, err := buildMoved(t, removeFilePropsYAML(scope, ""))
		if err != nil {
			t.Fatalf("%s: %v\n%s", scope, err, res.Diagnostics)
		}

		database, err := msidb.Unmarshal(res.Package)
		if err != nil {
			t.Fatal(err)
		}

		secure := ";" + fmt.Sprint(cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "SecureCustomProperties" })) + ";"
		for _, name := range []string{"LOGDIR", "SRC_DIR"} {
			if got := strings.Contains(secure, ";"+name+";"); got != want {
				t.Errorf("%s: SecureCustomProperties %q: %s secure %t, want %t", scope, secure, name, got, want)
			}
		}

		if !strings.Contains(secure, ";KEEP;") {
			t.Errorf("%s: SecureCustomProperties %q lacks KEEP", scope, secure)
		}
	}

	// Validation refuses secure: true on the entry's own property; the
	// builder refuses it on one an action sets that property from.
	for name, src := range map[string]string{
		"CLEAN_DIR": removeFilePropsYAML("perMachine", "  CLEAN_DIR: { secure: true }\n"),
		"SRC_DIR":   strings.Replace(removeFilePropsYAML("perMachine", ""), `SRC_DIR: { value: 'C:\Example\' }`, `SRC_DIR: { value: 'C:\Example\', secure: true }`, 1),
	} {
		res, err := buildMoved(t, src)
		if err == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "properties."+name+".secure") {
			t.Errorf("%s secure: true built: err %v, diagnostics %v", name, err, res)
		}
	}
}

// TestRemoveFilePropertyFromMovableDir warns about an action that sets a
// removeFiles property from a secure directory, such as a configurable
// install dir, which a standard user can move in an elevated removal.
func TestRemoveFilePropertyFromMovableDir(t *testing.T) {
	t.Parallel()

	src := strings.Replace(removeFilePropsYAML("perMachine", ""), "[SRC_DIR]cache", "[INSTALLDIR]cache", 1)
	src = strings.Replace(src, "properties:\n", "installDir: { id: INSTALLDIR, name: Hello }\nproperties:\n", 1)

	res, err := buildMoved(t, src)
	if err != nil {
		t.Fatalf("%v\n%v", err, res)
	}

	if !strings.Contains(fmt.Sprint(res.Diagnostics), "customActions[0].setProperty.value") {
		t.Errorf("no warning on SetClean: %v", res.Diagnostics)
	}
}
