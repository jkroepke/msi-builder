package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestFileTypes writes a file type as HKCR Registry rows in the file's
// component: the ProgID, its icon and verbs, the extensions that point at
// it and its entry in their OpenWithProgids lists. An openWithOnly
// extension gets only the list entry, also for an extension another type
// owns.
func TestFileTypes(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - id: App
    files:
      - source: app.exe
        fileTypes:
          - progId: Acme.Document
            description: '#1 [Beta] document'
            iconIndex: 1
            extensions:
              - { extension: acmedoc, contentType: application/x-acme }
              - extension: acmd
            verbs:
              - { id: open, text: '&Open' }
              - { id: print, arguments: '/p "%1"' }
              - { id: run, arguments: '' }
          - progId: Acme.Plain
            extensions:
              - { extension: acmedoc, openWithOnly: true }
              - { extension: txt, openWithOnly: true }
`)
	res := mustBuild(t, cfg)
	database := res.Database

	if len(res.Diagnostics) != 0 {
		t.Errorf("diagnostics: %v", res.Diagnostics)
	}

	// Every row is in the file's component, and file types need no
	// custom action or extra component.
	if n := len(rows(t, database, "Component")); n != 1 {
		t.Errorf("%d components, want only App", n)
	}

	for _, table := range []string{"CustomAction", "Binary"} {
		if tbl := database.Table(table); tbl != nil && len(tbl.Rows) != 0 {
			t.Errorf("%s rows %v, want none", table, tbl.Rows)
		}
	}

	got := make(map[string]string)

	for _, row := range rows(t, database, "Registry") {
		if row[5] != "App" {
			t.Errorf("row %v is not in App", row)

			continue
		}

		if fmt.Sprint(row[1]) != "0" {
			t.Errorf("row %v is not under HKCR", row)
		}

		got[fmt.Sprintf("%v|%v", row[2], row[3])] = fmt.Sprint(row[4])
	}

	want := map[string]string{
		`Acme.Document|<nil>`:                     `##1 [\[]Beta[\]] document`,
		`Acme.Document\DefaultIcon|<nil>`:         `"[#app.exe]",1`,
		`Acme.Document\shell\open|<nil>`:          "&Open",
		`Acme.Document\shell\open\command|<nil>`:  `"[#app.exe]" "%1"`,
		`Acme.Document\shell\print\command|<nil>`: `"[#app.exe]" /p "%1"`,
		`Acme.Document\shell\run\command|<nil>`:   `"[#app.exe]"`,
		`.acmedoc|<nil>`:                          "Acme.Document",
		`.acmedoc|Content Type`:                   "application/x-acme",
		`.acmd|<nil>`:                             "Acme.Document",
		`.acmedoc\OpenWithProgids|Acme.Document`:  "<nil>",
		`.acmd\OpenWithProgids|Acme.Document`:     "<nil>",
		`.acmedoc\OpenWithProgids|Acme.Plain`:     "<nil>",
		`.txt\OpenWithProgids|Acme.Plain`:         "<nil>",
		`Acme.Plain\shell\open\command|<nil>`:     `"[#app.exe]" "%1"`,
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}

	if len(got) != len(want) {
		t.Errorf("got %d rows, want %d: %v", len(got), len(want), got)
	}
}

// TestFileTypesPerUser names HKCU\Software\Classes in a per-user package.
func TestFileTypesPerUser(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]byte(`schema: msi-builder/v1
product: { name: Acme App, manufacturer: Acme, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: perUser }
components:
  - id: App
    files:
      - source: a.txt
        fileTypes: [{ progId: Acme.Document, extensions: [{ extension: acmedoc }] }]
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.BaseDir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	database := mustBuild(t, cfg).Database

	row := rowWhere(t, database, "Registry", func(r []any) bool { return r[2] == `Software\Classes\.acmedoc` })
	if row == nil || fmt.Sprint(row[1]) != "1" {
		t.Errorf("per-user extension row %v", row)
	}
}

// TestComponentFileTypes writes a component's own file types into that
// component, which is keyed on the first value with data they write, so
// that each extension can have a feature of its own. A file in another
// feature is named by its folder and long name (ICE69), one in the same
// feature by [#key]; iconFile names another file in DefaultIcon.
func TestComponentFileTypes(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - id: App
    files:
      - source: app.exe
      - source: app.ico
  - id: AssocDoc
    fileTypes:
      - progId: Acme.Document
        file: app.exe
        description: Acme document
        iconFile: app.ico
        extensions: [{ extension: acmedoc }]
  - id: AssocPlain
    fileTypes:
      - progId: Acme.Plain
        file: app.exe
        iconFile: app.ico
        iconIndex: 2
        verbs: []
        extensions: [{ extension: acmeplain }]
  - fileTypes:
      - progId: Acme.Same
        file: app.exe
        verbs: []
        extensions: [{ extension: acmesame, openWithOnly: true }, { extension: acmesame2 }]
features:
  - id: Main
    components: [App]
    features:
      - { id: Doc, components: [AssocDoc] }
      - { id: Plain, default: off, components: [AssocPlain] }
  - { id: Same, components: [App, Acme.Same] }
`)
	res := mustBuild(t, cfg)
	database := res.Database

	if len(res.Diagnostics) != 0 {
		t.Errorf("diagnostics: %v", res.Diagnostics)
	}

	got := make(map[string]string)
	ids := make(map[string]string)

	for _, row := range rows(t, database, "Registry") {
		key := fmt.Sprintf("%v|%v|%v", row[5], row[2], row[3])
		got[key] = fmt.Sprint(row[4])
		ids[key] = fmt.Sprint(row[0])
	}

	want := map[string]string{
		`AssocDoc|Acme.Document|<nil>`:                     "Acme document",
		`AssocDoc|Acme.Document\DefaultIcon|<nil>`:         `"[INSTALLDIR]app.ico",0`,
		`AssocDoc|Acme.Document\shell\open\command|<nil>`:  `"[INSTALLDIR]app.exe" "%1"`,
		`AssocDoc|.acmedoc|<nil>`:                          "Acme.Document",
		`AssocDoc|.acmedoc\OpenWithProgids|Acme.Document`:  "<nil>",
		`AssocPlain|Acme.Plain\DefaultIcon|<nil>`:          `"[INSTALLDIR]app.ico",2`,
		`AssocPlain|.acmeplain|<nil>`:                      "Acme.Plain",
		`AssocPlain|.acmeplain\OpenWithProgids|Acme.Plain`: "<nil>",
		`Acme.Same|.acmesame\OpenWithProgids|Acme.Same`:    "<nil>",
		`Acme.Same|.acmesame2|<nil>`:                       "Acme.Same",
		`Acme.Same|.acmesame2\OpenWithProgids|Acme.Same`:   "<nil>",
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}

	if len(got) != len(want) {
		t.Errorf("got %d rows, want %d: %v", len(got), len(want), got)
	}

	// Each component is keyed on its first value with data; an
	// openWithOnly entry is skipped.
	for comp, key := range map[string]string{
		"AssocDoc":   `AssocDoc|Acme.Document|<nil>`,
		"AssocPlain": `AssocPlain|Acme.Plain\DefaultIcon|<nil>`,
		"Acme.Same":  `Acme.Same|.acmesame2|<nil>`,
	} {
		row := rowWhere(t, database, "Component", func(r []any) bool { return r[0] == comp })
		if row == nil {
			t.Errorf("no component %s", comp)

			continue
		}

		if attrs, ok := row[3].(int); !ok || fmt.Sprint(row[5]) != ids[key] || attrs&4 == 0 {
			t.Errorf("component %v, want key path %s (%s) with the registry attribute", row, ids[key], key)
		}
	}
}

// TestComponentFileTypesSameFeature names the file by [#key] when every
// feature of the component also installs the file's component, and keeps
// the component codes of two file-type components apart.
func TestComponentFileTypesSameFeature(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - id: App
    files: [{ source: app.exe }]
  - id: AssocA
    fileTypes: [{ progId: Acme.A, file: app.exe, iconFile: app.exe, extensions: [{ extension: acmea }] }]
  - id: AssocB
    fileTypes: [{ progId: Acme.B, file: app.exe, extensions: [{ extension: acmeb }] }]
features:
  - { id: Main, components: [App, AssocA, AssocB] }
`))
	database := res.Database

	row := rowWhere(t, database, "Registry", func(r []any) bool { return r[2] == `Acme.A\shell\open\command` })
	if row == nil || row[4] != `"[#app.exe]" "%1"` {
		t.Errorf("command row %v, want [#app.exe]", row)
	}

	icon := rowWhere(t, database, "Registry", func(r []any) bool { return r[2] == `Acme.A\DefaultIcon` })
	if icon == nil || icon[4] != `"[#app.exe]",0` {
		t.Errorf("icon row %v", icon)
	}

	a := rowWhere(t, database, "Component", func(r []any) bool { return r[0] == "AssocA" })
	b := rowWhere(t, database, "Component", func(r []any) bool { return r[0] == "AssocB" })

	if a == nil || b == nil || a[1] == nil || a[1] == b[1] {
		t.Errorf("component codes %v and %v, want two different ones", a, b)
	}
}

// TestComponentFileTypesPerUser keys a component with nothing but file
// types on its HKCU\Software\Classes value in a per-user package, which
// the profile folder needs (ICE38), without an extra HKCU value.
func TestComponentFileTypesPerUser(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, scopedShortcutConfig(t, "perUser", `
  - id: App
    files: [{ source: app.exe }]
  - id: Assoc
    fileTypes: [{ progId: Acme.Doc, file: app.exe, extensions: [{ extension: acmedoc }] }]
`))
	database := res.Database

	comp := rowWhere(t, database, "Component", func(r []any) bool { return r[0] == "Assoc" })
	if comp == nil {
		t.Fatal("no Assoc component")
	}

	key := rowWhere(t, database, "Registry", func(r []any) bool { return r[0] == comp[5] })
	if key == nil || fmt.Sprint(key[1]) != "1" || key[2] != `Software\Classes\Acme.Doc\shell\open\command` {
		t.Errorf("Assoc key path %v, want the HKCU open command", key)
	}

	for _, row := range rows(t, database, "Registry") {
		if row[5] == "Assoc" && !strings.HasPrefix(fmt.Sprint(row[2]), `Software\Classes\`) {
			t.Errorf("Assoc writes %v outside Software\\Classes", row)
		}
	}
}
