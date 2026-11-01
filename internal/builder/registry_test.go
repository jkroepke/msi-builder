package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// buildYAML builds a package from src next to a versioned hello.exe and
// decodes its database.
func buildYAML(t *testing.T, src string) *msidb.Database {
	t.Helper()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	database, err := msidb.Unmarshal(mustBuild(t, cfg).Package)
	if err != nil {
		t.Fatal(err)
	}

	return database
}

func TestRemoveRegistry(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
    removeRegistry:
      - { id: OldHandler, root: HKCU, key: 'Software\Classes\hello' }
      - { root: HKLM, key: 'Software\Example\Hello', name: Legacy }
  - id: Cleanup
    removeRegistry:
      - { root: HKMU, key: 'Software\Example\Old' }
`)

	if got := len(rows(t, database, "RemoveRegistry")); got != 3 {
		t.Fatalf("%d RemoveRegistry rows, want 3", got)
	}

	byID := func(id string) func([]any) bool { return func(r []any) bool { return r[0] == id } }
	if got := cell(t, database, "RemoveRegistry", "Name", byID("OldHandler")); got != "-" {
		t.Errorf("whole-key removal has name %v, want -", got)
	}

	if got := cell(t, database, "RemoveRegistry", "Root", byID("OldHandler")); got != 1 {
		t.Errorf("HKCU root %v, want 1", got)
	}

	isLegacy := func(r []any) bool { return r[3] == "Legacy" }
	if got := cell(t, database, "RemoveRegistry", "Root", isLegacy); got != 2 {
		t.Errorf("HKLM root %v, want 2", got)
	}

	isOld := func(r []any) bool { return r[2] == `Software\Example\Old` }
	if got := cell(t, database, "RemoveRegistry", "Root", isOld); got != -1 {
		t.Errorf("HKMU root %v, want -1", got)
	}

	if got := cell(t, database, "RemoveRegistry", "Component_", isOld); got != "Cleanup" {
		t.Errorf("component %v, want Cleanup", got)
	}

	// A component with only removals has its folder as key path.
	if got := cell(t, database, "Component", "KeyPath", func(r []any) bool { return r[0] == "Cleanup" }); got != nil {
		t.Errorf("Cleanup key path %v, want null", got)
	}

	seq := func(action string) any {
		return cell(t, database, "InstallExecuteSequence", "Sequence", func(r []any) bool { return r[0] == action })
	}

	remove, removeOK := seq("RemoveRegistryValues").(int)
	write, writeOK := seq("WriteRegistryValues").(int)

	if !removeOK || !writeOK || remove >= write {
		t.Errorf("RemoveRegistryValues at %v is not before WriteRegistryValues at %v", remove, write)
	}
}

// TestRemoveRegistryOnUninstall checks that uninstall removals become
// Registry rows named - with a Null value (REG-08) and that both adds a
// RemoveRegistry row as well.
func TestRemoveRegistryOnUninstall(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
    removeRegistry:
      - { id: Verb, root: HKCR, key: '*\shell\hello', on: uninstall }
      - { root: HKLM, key: 'Software\Example\Hello', on: both }
      - { root: HKLM, key: 'Software\Example\Old', on: install }
`)

	if got := len(rows(t, database, "RemoveRegistry")); got != 2 {
		t.Errorf("%d RemoveRegistry rows, want 2 (both and install)", got)
	}

	isVerb := func(r []any) bool { return r[0] == "Verb" }
	for column, want := range map[string]any{"Root": 0, "Key": `*\shell\hello`, "Name": "-", "Value": nil} {
		if got := cell(t, database, "Registry", column, isVerb); got != want {
			t.Errorf("uninstall removal %s is %v, want %v", column, got, want)
		}
	}

	isBoth := func(r []any) bool { return r[2] == `Software\Example\Hello` }
	if got := cell(t, database, "Registry", "Name", isBoth); got != "-" {
		t.Errorf("both writes a Registry row named %v, want -", got)
	}

	if got := cell(t, database, "RemoveRegistry", "Name", isBoth); got != "-" {
		t.Errorf("both writes a RemoveRegistry row named %v, want -", got)
	}

	if slices.ContainsFunc(rows(t, database, "Registry"), func(r []any) bool { return r[2] == `Software\Example\Old` }) {
		t.Error("an install removal wrote a Registry row")
	}
}

func TestPropertySearch(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
properties:
  NETFX:
    search: { registry: { root: HKLM, key: 'SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full', name: Release } }
  OLDDIR:
    value: '[none]'
    hidden: true
    search: { registry: { root: HKCU, key: 'Software\Example', bitness: 32, type: directory } }
  EXEDIR:
    search: { registry: { root: HKLM, key: 'SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\hello.exe', type: file } }
  EXEFILE:
    search: { file: { name: hello.exe, under: { registry: { root: HKLM, key: 'SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\hello.exe', type: file } } } }
`)

	// type: file is RegLocator type 1, also as the parent of a file search
	// (SEARCH-10).
	appPaths := 0

	for _, row := range rows(t, database, "RegLocator") {
		if strings.HasSuffix(fmt.Sprint(row[2]), `App Paths\hello.exe`) {
			appPaths++

			if row[4] != 17 {
				t.Errorf("%v: RegLocator type %v, want 17 (file, 64-bit)", row[0], row[4])
			}
		}
	}

	if appPaths != 2 {
		t.Errorf("%d App Paths RegLocator rows, want 2", appPaths)
	}

	isNetfx := func(r []any) bool { return r[0] == "Search_NETFX" }
	if got := cell(t, database, "RegLocator", "Type", isNetfx); got != 18 {
		t.Errorf("NETFX RegLocator type %v, want 18 (raw, 64-bit)", got)
	}

	if got := cell(t, database, "RegLocator", "Name", isNetfx); got != "Release" {
		t.Errorf("NETFX RegLocator name %v", got)
	}

	isOld := func(r []any) bool { return r[0] == "Search_OLDDIR" }
	if got := cell(t, database, "RegLocator", "Type", isOld); got != 0 {
		t.Errorf("OLDDIR RegLocator type %v, want 0 (directory, 32-bit)", got)
	}

	if got := cell(t, database, "RegLocator", "Name", isOld); got != nil {
		t.Errorf("OLDDIR RegLocator name %v, want null (the default value)", got)
	}

	if got := cell(t, database, "AppSearch", "Property", func(r []any) bool { return r[1] == "Search_NETFX" }); got != "NETFX_FOUND" {
		t.Errorf("AppSearch property %v, want NETFX_FOUND", got)
	}

	if got := len(rows(t, database, "Signature")); got != 1 {
		t.Errorf("%d Signature rows, want 1 (hello.exe)", got)
	}

	byAction := func(name string) func([]any) bool { return func(r []any) bool { return r[0] == name } }
	if got := cell(t, database, "CustomAction", "Target", byAction("SearchDefault_OLDDIR")); got != `[\[]none[\]]` {
		t.Errorf("default target %v, want the escaped literal", got)
	}

	for _, seq := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		apply := cell(t, database, seq, "Condition", byAction("SearchApply_NETFX"))
		if apply != "NETFX_FOUND AND NOT NETFX" {
			t.Errorf("%s: apply condition %v", seq, apply)
		}

		search, searchOK := cell(t, database, seq, "Sequence", byAction("AppSearch")).(int)
		applyAt, applyOK := cell(t, database, seq, "Sequence", byAction("SearchApply_OLDDIR")).(int)
		fallback, fallbackOK := cell(t, database, seq, "Sequence", byAction("SearchDefault_OLDDIR")).(int)

		if !searchOK || !applyOK || !fallbackOK || search >= applyAt || applyAt >= fallback {
			t.Errorf("%s: AppSearch %d, apply %d, default %d are not in that order", seq, search, applyAt, fallback)
		}
	}

	isProperty := func(name string) func([]any) bool { return func(r []any) bool { return r[0] == name } }
	if got := cell(t, database, "Property", "Value", isProperty("MsiHiddenProperties")); got != "OLDDIR;OLDDIR_FOUND" {
		t.Errorf("MsiHiddenProperties %v", got)
	}

	for _, r := range rows(t, database, "Property") {
		if r[0] == "OLDDIR" {
			t.Errorf("OLDDIR has a Property row %v; its default comes from the action", r)
		}
	}
}

// TestFileSearchLongName checks that a file search for a name that is no
// valid short name writes a short|long pair (ICE03).
func TestFileSearchLongName(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
properties:
  NOTES: { search: { file: { name: release notes.txt, path: 'C:\Example' } } }
`)

	name := fmt.Sprint(rows(t, database, "Signature")[0][1])
	if short, long, ok := strings.Cut(name, "|"); !ok || long != "release notes.txt" || !config.ValidShortName(short) {
		t.Errorf("Signature.FileName = %q, want a short|long pair", name)
	}
}

// TestPropertyLocators checks the rows of directory, file and component
// searches, of an under search, and the order of several searches into one
// property.
func TestPropertyLocators(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
properties:
  TOOL:
    value: none
    search:
      - file:
          name: tool.exe
          under: { registry: { root: HKLM, key: 'SOFTWARE\Example', name: Home } }
          path: bin
          depth: 1
          minVersion: 1.2
          maxVersion: 2.0.0.0
          minSize: 10
          maxSize: 20
          minDate: 2020-01-02
          maxDate: 2043-12-31
          languages: [0, 1033]
      - directory: { path: '[ProgramFiles64Folder]Example' }
      - component: { guid: 6a0d3c2e-5b1f-4e7a-9c3d-2f8e1b4a7c60 }
`)

	bySig := func(name string) func([]any) bool { return func(r []any) bool { return r[0] == name } }

	signature := rows(t, database, "Signature")
	if len(signature) != 1 {
		t.Fatalf("Signature rows %v, want the file search only", signature)
	}

	// 2020-01-02 is ((40 × 512) + (1 × 32) + 2) << 16.
	want := []any{"Search_TOOL", "tool.exe", "1.2", "2.0.0.0", 10, 20, (40*512 + 32 + 2) << 16, (63*512+12*32+31)<<16 | 0xBF7D, "0,1033"}
	if !slices.Equal(signature[0], want) {
		t.Errorf("Signature row %v, want %v", signature[0], want)
	}

	for _, tc := range []struct {
		table, sig string
		want       []any
	}{
		{"DrLocator", "Search_TOOL", []any{"Search_TOOL", "In_Search_TOOL", "bin", 1}},
		{"RegLocator", "In_Search_TOOL", []any{"In_Search_TOOL", 2, `SOFTWARE\Example`, "Home", 16}},
		{"DrLocator", "Search2_TOOL", []any{"Search2_TOOL", nil, `[ProgramFiles64Folder]Example`, nil}},
		{"CompLocator", "Search3_TOOL", []any{"Search3_TOOL", "{6A0D3C2E-5B1F-4E7A-9C3D-2F8E1B4A7C60}", 1}},
	} {
		var got []any

		for _, r := range rows(t, database, tc.table) {
			if bySig(tc.sig)(r) {
				got = r
			}
		}

		if !slices.Equal(got, tc.want) {
			t.Errorf("%s row %v, want %v", tc.table, got, tc.want)
		}
	}

	for i, found := range []string{"TOOL_FOUND", "TOOL_FOUND2", "TOOL_FOUND3"} {
		sig := "Search_TOOL"
		if i > 0 {
			sig = fmt.Sprintf("Search%d_TOOL", i+1)
		}

		if got := cell(t, database, "AppSearch", "Signature_", func(r []any) bool { return r[0] == found }); got != sig {
			t.Errorf("AppSearch %s searches %v, want %s", found, got, sig)
		}
	}

	// The first search that found something wins, then the default.
	for _, seq := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		last, ok := cell(t, database, seq, "Sequence", bySig("AppSearch")).(int)
		if !ok {
			t.Fatalf("%s: no AppSearch", seq)
		}

		for _, action := range []string{"SearchApply_TOOL", "SearchApply2_TOOL", "SearchApply3_TOOL", "SearchDefault_TOOL"} {
			at, ok := cell(t, database, seq, "Sequence", bySig(action)).(int)
			if !ok || at <= last {
				t.Errorf("%s: %s at %v, not after %d", seq, action, at, last)
			}

			last = at
		}

		if got := cell(t, database, seq, "Condition", bySig("SearchApply2_TOOL")); got != "TOOL_FOUND2 AND NOT TOOL" {
			t.Errorf("%s: second apply condition %v", seq, got)
		}
	}
}

// TestProductCodeIgnoresEmptyKeys checks that keys with an empty value do
// not change the automatic ProductCode, so that an optional key added to the
// schema keeps the code of an unchanged configuration.
func TestProductCodeIgnoresEmptyKeys(t *testing.T) {
	t.Parallel()

	const src = `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
`

	productCode := func(doc string) any {
		return cell(t, buildYAML(t, doc), "Property", "Value", func(r []any) bool { return r[0] == "ProductCode" })
	}

	plain := productCode(src)
	explicit := productCode(src + "    removeRegistry: []\n    condition: ''\n")

	if plain != explicit {
		t.Errorf("ProductCode %v with empty keys, %v without", explicit, plain)
	}

	// One search written as a list of one is the same configuration.
	const search = "{ registry: { root: HKLM, key: Software\\Example, name: Dir } }"
	if one, list := productCode(src+"properties:\n  FOO: { search: "+search+" }\n"),
		productCode(src+"properties:\n  FOO: { search: ["+search+"] }\n"); one != list {
		t.Errorf("ProductCode %v for a list of one search, %v for the search alone", list, one)
	}

	// An explicit value, false and a mapping that pruning empties included,
	// is not the same as a missing key.
	const (
		data = "  - directory: dataDir\n"
		dirs = "directories:\n  - id: CommonAppDataFolder\n    children: [{ id: dataDir, name: Hello }]\n"
	)

	const launch = "ui: { enabled: true, launch: { file: hello.exe"

	for _, pair := range []struct{ without, with string }{
		{src, src + "    createFolder: true\n"},
		{src + "properties:\n  FOO: { value: x }\n", src + "properties:\n  FOO: { value: x, secure: false }\n"},
		{src + data + dirs, src + data + "    purge: true\n" + dirs},
		{src + launch + " } }\n", src + launch + ", checked: false } }\n"},
	} {
		if without, with := productCode(pair.without), productCode(pair.with); without == with {
			t.Errorf("ProductCode %v is the same for\n%s\nand\n%s", with, pair.without, pair.with)
		}
	}
}

// TestManySearchesMoveLaunchConditions checks that more search actions than
// fit between AppSearch (50) and LaunchConditions (100) move
// LaunchConditions after the last of them.
func TestManySearchesMoveLaunchConditions(t *testing.T) {
	t.Parallel()

	var props strings.Builder
	for i := range 40 {
		fmt.Fprintf(&props, "  P%d: { value: none, search: { directory: { path: 'C:\\P%d' } } }\n", i, i)
	}

	database := buildYAML(t, `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - files: [{ source: hello.exe }]
launchConditions:
  - { condition: VersionNT >= 600, message: Needs a newer Windows. }
properties:
`+props.String())

	for _, table := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		seq := func(name string) int {
			n, ok := cell(t, database, table, "Sequence", func(r []any) bool { return r[0] == name }).(int)
			if !ok {
				t.Fatalf("%s: no %s", table, name)
			}

			return n
		}

		if launch, last := seq("LaunchConditions"), seq("SearchDefault_P39"); launch <= last || last == 0 {
			t.Errorf("%s: LaunchConditions at %d, want after SearchDefault_P39 at %d", table, launch, last)
		}
	}
}

// TestRegistryKeyAndDefaultValue gives a type key row and the key's default
// value different generated ids (round 4: aspia, qttabbar).
func TestRegistryKeyAndDefaultValue(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - registry:
      - { root: HKLM, key: Software\Acme, type: key, removeOnUninstall: true }
      - { root: HKLM, key: Software\Acme, value: x }
      - { root: HKLM, key: Software\Acme\Sub, type: key }
`)

	names := make(map[any]bool)

	for _, row := range rows(t, mustBuild(t, cfg).Database, "Registry") {
		names[row[3]] = true
	}

	if !names["*"] || !names["+"] || !names[nil] {
		t.Errorf("Registry names %v, want *, + and the default value", names)
	}
}
