package builder_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
)

// IniFile table columns.
const (
	iniFileName  = 1
	iniDir       = 2
	iniSection   = 3
	iniKey       = 4
	iniValue     = 5
	iniAction    = 6
	iniComponent = 7
)

func TestComponentShortcuts(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - id: App
    files: [{ source: app.exe }]
  - id: Links
    shortcuts:
      - { name: App, directory: DesktopFolder, file: app.exe, icon: app.ico }
      - { name: Configuration, directory: ProgramMenuFolder/Acme, folder: INSTALLDIR/config }
      - { name: Uninstall, directory: ProgramMenuFolder/Acme, program: "[SystemFolder]msiexec.exe", arguments: "/x [ProductCode]" }
      - { name: Documentation, directory: ProgramMenuFolder/Acme, url: "https://example.com/docs?v=[ProductVersion]" }
features:
  - { id: Main, components: [App] }
  - { id: Desktop, default: off, components: [Links] }
`))
	database := res.Database

	byName := func(name string) []any {
		return rowWhere(t, database, "Shortcut", func(r []any) bool { return longOf(r[scName]) == name })
	}

	app, folder, uninstall := byName("App"), byName("Configuration"), byName("Uninstall")
	// app.exe is in another feature, so the target is its path (ICE69).
	if app[scTarget] != "[INSTALLDIR]app.exe" || app[scWkDir] != "INSTALLDIR" || app[scIcon] != "app.ico" {
		t.Errorf("file shortcut %v", app)
	}

	configDir := rowWhere(t, database, "Directory", func(r []any) bool { return longOf(r[2]) == "config" })[0]
	if folder[scTarget] != "["+fmt.Sprint(configDir)+"]" || folder[scWkDir] != nil || folder[scArguments] != nil {
		t.Errorf("folder shortcut %v, config directory %v", folder, configDir)
	}

	if uninstall[scTarget] != "[SystemFolder]msiexec.exe" || uninstall[scArguments] != "/x [ProductCode]" || uninstall[scWkDir] != nil {
		t.Errorf("program shortcut %v", uninstall)
	}

	// A .url shortcut is IniFile rows, no Shortcut row.
	if n := len(rows(t, database, "Shortcut")); n != 3 {
		t.Errorf("%d Shortcut rows, want 3", n)
	}

	ini := rows(t, database, "IniFile")
	if len(ini) != 1 {
		t.Fatalf("IniFile rows: %v", ini)
	}

	url := ini[0]
	if longOf(url[iniFileName]) != "Documentation.url" || url[iniSection] != "InternetShortcut" || url[iniKey] != "URL" ||
		url[iniValue] != "https://example.com/docs?v=[ProductVersion]" || url[iniAction] != 0 || url[iniDir] != folder[scDirectory] {
		t.Errorf("IniFile row %v", url)
	}

	// All four sit in one carrier, which is in the Desktop feature only.
	carrier := app[scComponent]
	for _, row := range [][]any{folder, uninstall} {
		if row[scComponent] != carrier {
			t.Errorf("carrier %v, want %v", row[scComponent], carrier)
		}
	}

	if url[iniComponent] != carrier {
		t.Errorf("IniFile carrier %v, want %v", url[iniComponent], carrier)
	}

	var features []string

	for _, row := range rows(t, database, "FeatureComponents") {
		if row[1] == carrier {
			features = append(features, fmt.Sprint(row[0]))
		}
	}

	if strings.Join(features, ",") != "Desktop" {
		t.Errorf("carrier features %v, want Desktop", features)
	}

	// The .url file is removed at uninstall; the config folder and the
	// Start menu folder of the .url are created by the carrier.
	rowWhere(t, database, "RemoveFile", func(r []any) bool { return r[2] == url[iniFileName] && r[3] == url[iniDir] && r[4] == 2 })

	created := map[any]bool{}

	for _, row := range rows(t, database, "CreateFolder") {
		if row[1] == carrier {
			created[row[0]] = true
		}
	}

	if !created[configDir] || !created[url[iniDir]] || len(created) != 2 {
		t.Errorf("CreateFolder rows of the carrier %v, want %v and %v", created, configDir, url[iniDir])
	}

	for _, action := range []string{"WriteIniValues", "RemoveIniValues", "CreateShortcuts", "RemoveShortcuts", "CreateFolders"} {
		rowWhere(t, database, "InstallExecuteSequence", func(r []any) bool { return r[0] == action })
	}
}

// A standard folder gets no CreateFolder row from the carrier.
func TestFolderShortcutExistingFolder(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
    shortcuts:
      - { name: Install folder, folder: INSTALLDIR }
      - { name: Documents, folder: PersonalFolder }
`))

	// A component's folder still gets a row, since the component may have
	// no files there; a standard folder gets none.
	created := rows(t, res.Database, "CreateFolder")
	if len(created) != 1 || created[0][0] != "INSTALLDIR" {
		t.Errorf("CreateFolder rows %v", created)
	}
}

func TestComponentShortcutValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ shortcut, path, message string }{
		{`{ name: A }`, "", "set one of file, folder, program or url"},
		{`{ name: A, file: app.exe, url: "https://a" }`, "", "set only one"},
		{`{ name: A, file: nothing.exe }`, ".file", "not defined in any component"},
		{`{ name: A, folder: NOWHERE }`, ".folder", "not defined"},
		{`{ name: A, folder: INSTALLDIR, arguments: x }`, ".arguments", "no arguments"},
		{`{ name: A, folder: INSTALLDIR, workingDirectory: INSTALLDIR }`, ".workingDirectory", "no workingDirectory"},
		{`{ name: A, program: 'C:\Windows\notepad.exe' }`, ".program", "start the path with a folder property"},
		{`{ name: A, program: '[SystemFolder]"cmd.exe"' }`, ".program", "quotes"},
		{`{ name: A, program: "[SystemFolder]` + strings.Repeat("a", 60) + `" }`, ".program", "at most 72 fit"},
		{`{ name: A, program: "[SystemFolder]cmd.exe", workingDirectory: NOWHERE }`, ".workingDirectory", "not defined"},
		{`{ name: A, url: example.com }`, ".url", "scheme"},
		{`{ name: A, url: "https://bücher.example" }`, ".url", "printable ASCII"},
		{`{ name: A, url: "https://a/` + strings.Repeat("a", 250) + `" }`, ".url", "at most 255"},
		{`{ name: A, url: "https://a", icon: app.ico }`, ".icon", "not supported on a url shortcut"},
		{`{ name: A, url: "https://a", description: x }`, ".description", "not supported on a url shortcut"},
		{`{ name: A, url: "https://a", show: minimized }`, ".show", "not supported on a url shortcut"},
		{`{ name: A, url: "https://a", properties: { System.AppUserModel.ID: X } }`, ".properties", "not supported on a url shortcut"},
		{`{ name: A, url: "https://a", directory: NOWHERE }`, ".directory", "not defined"},
		{`{ name: A, url: "https://a", iconIndex: 2 }`, ".iconIndex", "needs iconFile"},
		{`{ name: A, url: "https://a", iconFile: nothing.ico }`, ".iconFile", "not defined in any component"},
		{`{ name: A, program: "[SystemFolder]cmd.exe", iconFile: app.exe }`, ".iconFile", "only a url shortcut takes iconFile"},
	} {
		cfg := shortcutConfig(t, "  - files: [{ source: app.exe }]\n    shortcuts: ["+tc.shortcut+"]\n")

		_, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
		if err == nil || !strings.Contains(err.Error(), "components[0].shortcuts[0]"+tc.path+":") || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: %v", tc.shortcut, err)
		}
	}

	// The target of a file shortcut under a file is that file.
	cfg := shortcutConfig(t, "  - files: [{ source: app.exe, shortcuts: [{ url: \"https://a\" }] }]\n")
	if _, err := builder.Build(cfg, builder.Options{BuildTime: buildTime}); err == nil || !strings.Contains(err.Error(), "files[0].shortcuts[0].url: a shortcut under a file") {
		t.Errorf("a url under a file: %v", err)
	}

	// A .lnk and a .url of one name are two files; two .url files are not.
	cfg = shortcutConfig(t, `  - files: [{ source: app.exe, shortcuts: [{ name: A }] }]
    shortcuts: [{ name: A, url: "https://a" }, { name: a, url: "https://b" }]
`)
	if _, err := builder.Build(cfg, builder.Options{BuildTime: buildTime}); err == nil ||
		!strings.Contains(err.Error(), "shortcuts[1].name") || strings.Contains(err.Error(), "shortcuts[0].name") {
		t.Errorf("shortcut names: %v", err)
	}

	cfg = shortcutConfig(t, "  - files: [{ source: app.exe }]\n    shortcuts: [{ name: A.url, url: \"https://a\" }]\n")

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err != nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "adds .url") {
		t.Errorf("a name ending in .url: %v %s", err, res.Diagnostics)
	}
}

// A folder shortcut that creates a folder in the user profile removes it
// and the folders above it at uninstall (ICE64).
func TestFolderShortcutProfileFolder(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
    shortcuts:
      - { name: Data, folder: AppDataFolder/Acme/data }
`))

	created := rows(t, res.Database, "CreateFolder")
	if len(created) != 1 {
		t.Fatalf("CreateFolder rows %v", created)
	}

	removed := map[any]bool{}

	for _, row := range rows(t, res.Database, "RemoveFile") {
		if row[1] == created[0][1] && row[2] == nil {
			removed[row[3]] = true
		}
	}

	parent := rowWhere(t, res.Database, "Directory", func(r []any) bool { return r[0] == created[0][0] })[1]
	if !removed[created[0][0]] || !removed[parent] {
		t.Errorf("RemoveFile folders %v, want %v and %v", removed, created[0][0], parent)
	}
}

// A .url file must not take the name of a packaged file.
func TestURLShortcutPackagedName(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - files: [{ source: app.exe, name: Docs.url }]
    shortcuts: [{ name: Docs, directory: INSTALLDIR, url: "https://a" }]
`)
	if _, err := builder.Build(cfg, builder.Options{BuildTime: buildTime}); err == nil || !strings.Contains(err.Error(), "shortcuts[0].name: Docs.url is also a packaged file") {
		t.Errorf("a .url with the name of a packaged file: %v", err)
	}
}

// Two components that only list shortcuts are carriers in the folders of
// their shortcuts, and their automatic ComponentIds differ.
func TestShortcutOnlyComponents(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
  - id: Menu
    shortcuts: [{ name: Docs, url: "https://a" }]
  - id: Desktop
    shortcuts: [{ name: App, directory: DesktopFolder, file: app.exe }]
`))

	// One feature installs both, so the target is the file key.
	if target := rows(t, res.Database, "Shortcut")[0][scTarget]; target != "[#app.exe]" {
		t.Errorf("target %v, want [#app.exe]", target)
	}

	guids := map[any]any{}

	for _, row := range rows(t, res.Database, "Component") {
		if other, dup := guids[row[1]]; dup {
			t.Errorf("components %v and %v share ComponentId %v", other, row[0], row[1])
		}

		guids[row[1]] = row[0]
	}
}

// A url shortcut's icon is IconFile and IconIndex in its section.
func TestURLShortcutIcon(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
    shortcuts: [{ name: Docs, url: "https://a", iconFile: app.exe, iconIndex: 2 }]
`))

	got := map[any]any{}

	for _, row := range rows(t, res.Database, "IniFile") {
		if longOf(row[iniFileName]) != "Docs.url" || row[iniSection] != "InternetShortcut" {
			t.Errorf("IniFile row %v", row)
		}

		got[row[iniKey]] = row[iniValue]
	}

	if got["URL"] != "https://a" || got["IconFile"] != "[#app.exe]" || got["IconIndex"] != "2" || len(got) != 3 {
		t.Errorf("IniFile entries %v", got)
	}
}

// A component that only lists shortcuts is their carrier: no empty
// component of its own, its id from the first shortcut's name, and no
// ICE38 warning in a per-user package.
func TestShortcutsOnlyComponentIsCarrier(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{"perMachine", "perUser"} {
		res := mustBuild(t, scopedShortcutConfig(t, scope, `
  - files: [{ source: app.exe }]
  - shortcuts: [{ name: Docs, url: "https://a" }, { name: Config, folder: INSTALLDIR/config }]
`))
		if strings.Contains(fmt.Sprint(res.Diagnostics), "components[1]") {
			t.Errorf("%s: %s", scope, res.Diagnostics)
		}

		carrier := rows(t, res.Database, "Shortcut")[0][scComponent]
		if carrier != "Docs_Shortcuts" {
			t.Errorf("%s: carrier %v, want the component Docs_Shortcuts", scope, carrier)
		}

		comp := rowWhere(t, res.Database, "Component", func(r []any) bool { return r[0] == carrier })
		if comp[2] != "ProgramMenuFolder" || comp[5] == nil {
			t.Errorf("%s: carrier row %v", scope, comp)
		}

		if n := len(rows(t, res.Database, "Component")); n != 2 {
			t.Errorf("%s: %d Component rows, want app.exe's and the carrier", scope, n)
		}

		rowWhere(t, res.Database, "FeatureComponents", func(r []any) bool { return r[1] == carrier })
	}
}

// A folder whose only component has no files gets a CreateFolder row.
func TestFolderShortcutRegistryOnlyFolder(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - directory: INSTALLDIR/config
    registry: [{ root: HKLM, key: 'SOFTWARE\Acme', name: x, type: string, value: y }]
  - files: [{ source: app.exe }]
    shortcuts: [{ name: Config, folder: INSTALLDIR/config }]
`))

	target := rows(t, res.Database, "Shortcut")[0]
	rowWhere(t, res.Database, "CreateFolder", func(r []any) bool { return "["+fmt.Sprint(r[0])+"]" == target[scTarget] && r[1] == target[scComponent] })
}

// A component of shortcuts only keeps its pinned guid on the carrier, and
// its directory is not checked as an install folder.
func TestShortcutsOnlyComponentGUIDAndDirectory(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, scopedShortcutConfig(t, "perUser", `
  - files: [{ source: app.exe }]
  - id: Links
    guid: 11111111-2222-3333-4444-555555555555
    condition: VersionNT >= 600
    transitive: true
    directory: CommonAppDataFolder/Acme
    shortcuts: [{ name: Docs, url: "https://a" }]
  - id: Kept
    guid: none
    permanent: true
    shortcuts: [{ name: Docs2, url: "https://b" }]
`))

	links := rowWhere(t, res.Database, "Component", func(r []any) bool { return r[0] == "Links" })
	if links[1] != "{11111111-2222-3333-4444-555555555555}" {
		t.Errorf("Links ComponentId %v", links[1])
	}

	if attrs, ok := links[3].(int); !ok || links[4] != "VersionNT >= 600" || attrs&64 == 0 {
		t.Errorf("Links condition %v, attributes %v, want the condition and transitive", links[4], links[3])
	}

	kept := rowWhere(t, res.Database, "Component", func(r []any) bool { return r[0] == "Kept" })
	if kept[1] != nil {
		t.Errorf("Kept ComponentId %v, want null", kept[1])
	}

	// Without a ComponentId the component is never removed; the Permanent
	// bit on it would be an ICE92 error.
	if attrs, ok := kept[3].(int); !ok || attrs&16 != 0 {
		t.Errorf("Kept attributes %v, want no Permanent bit", kept[3])
	}

	if strings.Contains(fmt.Sprint(res.Diagnostics), "components[1]") {
		t.Errorf("diagnostics %s", res.Diagnostics)
	}
}

// An id-less component of shortcuts whose first shortcut has no name is
// named after the product, the shortcut's default name, not its folder.
func TestShortcutsOnlyComponentUnnamedShortcut(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
  - shortcuts: [{ directory: DesktopFolder, file: app.exe }]
`))

	if carrier := rows(t, res.Database, "Shortcut")[0][scComponent]; carrier != "Acme_App_Shortcuts" {
		t.Errorf("carrier %v, want Acme_App_Shortcuts", carrier)
	}
}

// Components of shortcuts only whose ids differ in case get distinct key
// path values, which the registry compares without case.
func TestShortcutsOnlyComponentKeyPath(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - files: [{ source: app.exe }]
  - { id: Links, shortcuts: [{ name: One, url: "https://a" }] }
  - { id: links, shortcuts: [{ name: Two, url: "https://b" }] }
`))

	values := map[string]bool{}

	for _, id := range []string{"Links", "links"} {
		comp := rowWhere(t, res.Database, "Component", func(r []any) bool { return r[0] == id })
		reg := rowWhere(t, res.Database, "Registry", func(r []any) bool { return r[0] == comp[5] })
		values[strings.ToLower(fmt.Sprint(reg[2], `\`, reg[3]))] = true
	}

	if len(values) != 2 {
		t.Errorf("key path values %v", values)
	}
}

// A component of shortcuts only may not share its pinned guid with another
// component (ICE08).
func TestShortcutsOnlyComponentDuplicateGUID(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - id: Main
    guid: 11111111-2222-3333-4444-555555555555
    files: [{ source: app.exe }]
  - id: Links
    guid: "{11111111-2222-3333-4444-555555555555}"
    shortcuts: [{ name: Docs, url: "https://a" }]
`)
	if _, err := builder.Build(cfg, builder.Options{BuildTime: buildTime}); err == nil || !strings.Contains(err.Error(), "component Links has the same ComponentId") {
		t.Errorf("a shared guid: %v", err)
	}
}

// The generated carrier of a permanent component's shortcuts, of its files or
// its own, is not permanent: the file stays after uninstall, its shortcuts do
// not (wazuh's "Edit conf"). The carrier keeps condition and transitive.
// A component of shortcuts alone keeps its own permanent flag.
func TestShortcutCarrierNotPermanent(t *testing.T) {
	t.Parallel()

	res := mustBuild(t, shortcutConfig(t, `
  - id: Conf
    permanent: true
    files:
      - source: app.exe
        shortcuts: [{ name: Edit }]
  - id: Site
    permanent: true
    transitive: true
    condition: SITE
    files: [{ source: app.exe, name: site.exe }]
    shortcuts: [{ name: Home, url: "https://a" }]
  - id: Menu
    permanent: true
    shortcuts: [{ name: Docs, url: "https://a" }]
`))

	generated, siteCarriers := 0, 0

	for _, row := range rows(t, res.Database, "Component") {
		attributes, ok := row[3].(int)
		if !ok {
			t.Fatalf("component %v attributes %v", row[0], row[3])
		}

		permanent := attributes&0x10 != 0 // msidbComponentAttributesPermanent

		switch row[0] {
		case "Conf", "Site", "Menu":
			if !permanent {
				t.Errorf("component %v lost its permanent flag", row[0])
			}
		default:
			generated++

			if permanent {
				t.Errorf("generated shortcut component %v is permanent (attributes %v)", row[0], row[3])
			}

			// Site's carrier keeps its condition and the transitive bit.
			if row[4] != "SITE" {
				continue
			}

			siteCarriers++

			if attributes&0x40 == 0 { // msidbComponentAttributesTransitive
				t.Errorf("carrier %v of Site lost the transitive bit (attributes %v)", row[0], row[3])
			}
		}
	}

	if generated != 2 {
		t.Errorf("%d generated shortcut components, want 2 (Conf and Site)", generated)
	}

	if siteCarriers != 1 {
		t.Errorf("%d carriers with condition SITE, want 1", siteCarriers)
	}
}

// Builds that made the carrier of a permanent component permanent left its
// ComponentId registered after uninstall, so the carrier of a permanent
// component gets a ComponentId of its own: the same component without
// permanent keeps the old one.
func TestShortcutCarrierGUIDOfPermanentComponent(t *testing.T) {
	t.Parallel()

	build := func(permanent bool) (any, string) {
		res := mustBuild(t, shortcutConfig(t, fmt.Sprintf(`
  - id: Conf
    permanent: %v
    files:
      - source: app.exe
        shortcuts: [{ name: Edit }]
`, permanent)))

		var guids []any

		for _, row := range rows(t, res.Database, "Component") {
			if row[0] != "Conf" {
				guids = append(guids, row[1])
			}
		}

		if len(guids) != 1 {
			t.Fatalf("generated shortcut components %v, want 1", guids)
		}

		return guids[0], res.ProductCode
	}

	// The carrier of a removable component keeps the ComponentId that
	// every earlier build gave it.
	const earlier = "{E8F093D1-5257-5AA8-804A-DF8360647E81}"

	kept, keptCode := build(false)
	removable, removableCode := build(true)

	if kept != earlier {
		t.Errorf("carrier of a removable component has the ComponentId %v, want %v", kept, earlier)
	}

	if removable == kept {
		t.Errorf("carrier of a permanent component has the ComponentId %v of a removable one", kept)
	}

	// The ProductCode changes with it (hashRemovableCarriers), or a rebuild
	// over an earlier build fails with 1638; the removable one keeps its own.
	const keptCodeEarlier, permanentCodeEarlier = "{0F9FB8C6-3B99-519E-815E-27EE6DF18EB8}", "{9CB60842-0523-5131-9926-CDF051520981}"

	if keptCode != keptCodeEarlier {
		t.Errorf("ProductCode %v, want the earlier %v", keptCode, keptCodeEarlier)
	}

	if removableCode == permanentCodeEarlier {
		t.Errorf("ProductCode %v of a permanent component's carrier did not change", removableCode)
	}
}
