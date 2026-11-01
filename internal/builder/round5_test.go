package builder_test

import (
	"fmt"
	"strings"
	"testing"
)

const round5Header = `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
`

// TestTwinServiceComponents builds two conditional components that install
// the same service, one started and one disabled, as crowdsec's installer
// does. Their ServiceInstall and ServiceControl rows must not share keys.
func TestTwinServiceComponents(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - id: SvcOn
    condition: ENABLE="1"
    files: [{ source: hello.exe }]
    services: [{ name: rwsvc, delayedAutoStart: true }]
  - id: SvcOff
    condition: NOT ENABLE="1"
    files: [{ id: hello2, source: hello.exe, name: hello2.exe }]
    services: [{ name: rwsvc, start: disabled }]
`)

	for table, want := range map[string]int{"ServiceInstall": 2, "ServiceControl": 2, "MsiServiceConfig": 1} {
		if got := len(rows(t, database, table)); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}

	owners := map[any]bool{}

	for _, row := range rows(t, database, "ServiceInstall") {
		owners[row[11]] = true
	}

	if !owners["SvcOn"] || !owners["SvcOff"] {
		t.Errorf("ServiceInstall components: %v", owners)
	}
}

// TestSingleServiceKeepsKeys checks that a service only one component
// installs keeps its readable keys.
func TestSingleServiceKeepsKeys(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - files: [{ source: hello.exe }]
    services: [{ name: rwsvc }]
`)

	if got := rows(t, database, "ServiceInstall")[0][0]; got != "svi_rwsvc" {
		t.Errorf("ServiceInstall key %v, want svi_rwsvc", got)
	}
}

// TestEventLogOnlyComponents puts two event sources into components of their
// own in one folder (datadog-agent). Both keep the folder as key path, and
// the second takes its code from its source, since the folder's is taken.
func TestEventLogOnlyComponents(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - files: [{ source: hello.exe }]
  - eventLog: [{ source: rwA, log: Application, messageFile: 'x.dll' }]
  - eventLog: [{ source: rwB, log: Application, messageFile: 'x.dll' }]
`)

	guids := map[any]bool{}

	for _, row := range rows(t, database, "Component") {
		if row[0] == "hello.exe" {
			continue
		}

		if row[5] != nil {
			t.Errorf("component %v key path %v, want the folder", row[0], row[5])
		}

		guids[row[1]] = true
	}

	if len(guids) != 2 {
		t.Errorf("component codes %v, want two different ones", guids)
	}
}

// TestFileTypeWithoutVerbs checks that verbs: [] writes no shell verb.
func TestFileTypeWithoutVerbs(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - files:
      - source: hello.exe
        fileTypes:
          - progId: Example.Doc
            extensions: [{ extension: rwdoc }]
            verbs: []
`)

	for _, row := range rows(t, database, "Registry") {
		if strings.Contains(fmt.Sprint(row[2]), `\shell`) {
			t.Errorf("verb row %v", row)
		}
	}
}

// TestTablesMoveFileSequenced adds MoveFile rows through tables (duplicati);
// MoveFiles must be scheduled without an anchor.
func TestTablesMoveFileSequenced(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - files: [{ source: hello.exe }]
tables:
  - name: MoveFile
    columns:
      - { name: FileKey, type: s72, key: true }
      - { name: Component_, type: s72 }
      - { name: SourceName, type: L255 }
      - { name: DestName, type: L255 }
      - { name: SourceFolder, type: S72 }
      - { name: DestFolder, type: s72 }
      - { name: Options, type: i2 }
    rows:
      - [mv1, hello.exe, old.txt, new.txt, INSTALLDIR, INSTALLDIR, 0]
`)

	found := false

	for _, row := range rows(t, database, "InstallExecuteSequence") {
		if row[0] == "MoveFiles" {
			found = true
		}
	}

	if !found {
		t.Error("MoveFiles is not in InstallExecuteSequence")
	}
}

// TestAnchorOnSearchApply schedules a custom action after the generated
// action that applies a search result.
func TestAnchorOnSearchApply(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`properties:
  RWDIR:
    secure: true
    search:
      - registry: { root: HKLM, key: 'Software\RW', name: Dir }
components:
  - files: [{ source: hello.exe }]
customActions:
  - id: SetIt
    setProperty: { name: RWX, value: '[RWDIR]' }
    schedule: [{ sequence: execute, after: SearchApply_RWDIR }]
`)

	seq := map[any]int{}

	for _, row := range rows(t, database, "InstallExecuteSequence") {
		if n, ok := row[2].(int); ok {
			seq[row[0]] = n
		}
	}

	if seq["SetIt"] <= seq["SearchApply_RWDIR"] {
		t.Errorf("SetIt at %d, SearchApply_RWDIR at %d", seq["SetIt"], seq["SearchApply_RWDIR"])
	}
}

// TestEventLogOnlyComponentFolderPermissions checks that a component with
// only event sources still owns its folder: it keeps the CreateFolder row
// that folderPermissions locks.
func TestEventLogOnlyComponentFolderPermissions(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, round5Header+`components:
  - files: [{ source: hello.exe }]
  - eventLog: [{ source: rwA, log: Application, messageFile: 'x.dll' }]
    folderPermissions: { sddl: 'D:(A;;GA;;;SY)(A;;GA;;;BA)' }
`)

	if len(rows(t, database, "CreateFolder")) == 0 {
		t.Error("no CreateFolder row")
	}
}

// TestServiceSidAndPreshutdown checks the MsiServiceConfig rows of sidType
// and preshutdownTimeout: ConfigType 5 with the SERVICE_SID_TYPE value and
// ConfigType 7 with milliseconds, both at install and reinstall (SVC-10).
func TestServiceSidAndPreshutdown(t *testing.T) {
	t.Parallel()

	for sidType, value := range map[string]string{"none": "0", "unrestricted": "1", "restricted": "3"} {
		database := buildYAML(t, round5Header+`components:
  - files: [{ source: hello.exe }]
    services: [{ name: rwsvc, sidType: `+sidType+`, preshutdownTimeout: 2m30s }]
`)

		got := map[any][]any{}
		for _, row := range rows(t, database, "MsiServiceConfig") {
			got[row[0]] = row
		}

		for key, want := range map[string]string{
			"scsid_rwsvc": fmt.Sprint([]any{"scsid_rwsvc", "rwsvc", 5, 5, value, "hello.exe"}),
			"scpre_rwsvc": fmt.Sprint([]any{"scpre_rwsvc", "rwsvc", 5, 7, "150000", "hello.exe"}),
		} {
			if fmt.Sprint(got[key]) != want {
				t.Errorf("sidType %s: row %s is %v, want %s", sidType, key, got[key], want)
			}
		}

		if len(got) != 2 {
			t.Errorf("sidType %s: %d MsiServiceConfig rows, want 2", sidType, len(got))
		}

		if seq := rows(t, database, "InstallExecuteSequence"); !strings.Contains(fmt.Sprint(seq), "MsiConfigureServices") {
			t.Errorf("sidType %s: MsiConfigureServices is not sequenced", sidType)
		}
	}
}
