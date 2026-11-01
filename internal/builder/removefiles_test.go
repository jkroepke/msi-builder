package builder_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
