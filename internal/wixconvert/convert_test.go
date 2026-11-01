package wixconvert_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestConvertSnapshots converts every testdata/*.wxs, compares the result
// with testdata/<name>.yaml and checks that msi-builder accepts it.
func TestConvertSnapshots(t *testing.T) {
	t.Parallel()

	sources, err := filepath.Glob(filepath.Join("testdata", "*.wxs"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("no test sources: %v", err)
	}

	for _, src := range sources {
		name := strings.TrimSuffix(filepath.Base(src), ".wxs")

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}

			res, err := wixconvert.Convert(bytes.NewReader(data), wixconvert.Options{Source: filepath.Base(src)})
			if err != nil {
				t.Fatal(err)
			}

			exampletest.CompareSnapshot(t, filepath.Join("testdata", name+".yaml"), string(res.YAML))

			for _, n := range res.Notes {
				if !strings.Contains(string(res.YAML), n.Message) {
					t.Errorf("note %q is not a comment in the YAML file", n)
				}
			}

			vars := map[string]string{}
			for _, name := range res.Vars {
				vars[name] = "1.2.3"
			}

			cfg, err := config.Parse(res.YAML, config.Options{Vars: vars})
			if err != nil {
				t.Fatal(err)
			}

			if err := config.Validate(cfg).Err(); err != nil {
				t.Errorf("the converted file does not validate: %v", err)
			}
		})
	}
}

func TestConvertErrors(t *testing.T) {
	t.Parallel()

	for name, src := range map[string]string{
		"not xml":       `<Wix`,
		"not wix":       `<Project/>`,
		"bundle":        `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Bundle/></Wix>`,
		"no package":    `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Fragment/></Wix>`,
		"two packages":  `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Package/><Package/></Wix>`,
		"two root tags": `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs"><Package/></Wix><Wix/>`,
	} {
		if _, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestConvertNotes(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="!(bind.FileVersion.hello.exe)" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Feature Id="Main"><ComponentRef Id="Missing" /></Feature>
    <Feature Id="Other"><ComponentRef Id="Missing" /></Feature>
    <Merge Id="Runtime" SourceFile="runtime.msm" />
    <CustomAction Id="Init" Directory="INSTALLFOLDER" ExeCommand="[INSTALLFOLDER]tool.exe /s" Execute="deferred" Return="check" />
    <CustomAction Id="Old" Directory="INSTALLFOLDER" ExeCommand="[OLD_UNINSTALL] /S" Execute="deferred" Return="check" />
    <CustomAction Id="Quoted" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]tool.exe&quot; /s" Execute="deferred" Return="check" />
    <InstallExecuteSequence>
      <Custom Action="Init" After="InstallFiles" />
      <Custom Action="Old" After="Init" />
      <Custom Action="Quoted" After="Old" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := []wixconvert.Note{
		{Line: 0, Message: "ComponentRef Missing names no component of this file"},
		{Line: 2, Message: "!(bind.FileVersion.hello.exe) is a WiX bind variable; replace it"},
		{Line: 2, Message: "WiX takes the platform from wix build -arch; check package.platform"},
		{Line: 2, Message: "the WiX package has no MajorUpgrade; msi-builder adds the standard major upgrade (set upgrade: false to leave it out)"},
		{Line: 5, Message: "<Merge> is not supported: merge modules are not supported; install their files in components"},
		{Line: 6, Message: "quote the executable [INSTALLFOLDER]tool.exe: an unquoted path with a space is tried piece by piece by CreateProcess"},
		{Line: 7, Message: "check [OLD_UNINSTALL]: quote it if it holds a bare path; leave it if it holds a quoted command line such as an UninstallString"},
	}

	if len(res.Notes) != len(want) {
		t.Fatalf("notes %q, want %q", res.Notes, want)
	}

	for i := range want {
		if res.Notes[i] != want[i] {
			t.Errorf("note %d is %q, want %q", i, res.Notes[i], want[i])
		}
	}
}

// TestConvertUINavigation checks that Publish rows that reorder the WiX
// dialogs stay TODOs unless they take out the whole license page.
func TestConvertUINavigation(t *testing.T) {
	t.Parallel()

	const todo = "<Publish> in <UI> customizes the WiX dialogs and cannot be converted"

	for name, tc := range map[string]struct {
		ui      string
		want    []wixconvert.Note
		license bool
	}{
		"no wizard": {
			ui:   `<UI Id="MyUI"><Publish Dialog="WelcomeDlg" Control="Next" Event="NewDialog" Value="InstallDirDlg" /></UI>`,
			want: []wixconvert.Note{{Line: 4, Message: todo}},
		},
		"license skipped": {
			ui: `<ui:WixUI Id="WixUI_InstallDir" InstallDirectory="INSTALLFOLDER" /><UI>
<Publish Dialog="WelcomeDlg" Control="Next" Event="NewDialog" Value="InstallDirDlg" Condition="NOT Installed" />
<Publish Dialog="InstallDirDlg" Control="Back" Event="NewDialog" Value="WelcomeDlg" Condition="NOT Installed" /></UI>`,
		},
		"back row alone": {
			ui: `<ui:WixUI Id="WixUI_InstallDir" InstallDirectory="INSTALLFOLDER" /><UI>
<Publish Dialog="InstallDirDlg" Control="Back" Event="NewDialog" Value="WelcomeDlg" /></UI>`,
			want:    []wixconvert.Note{{Line: 5, Message: todo}},
			license: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" /><WixVariable Id="WixUILicenseRtf" Value="license.rtf" />
    ` + tc.ui + `
  </Package>
</Wix>`

			res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			var notes []wixconvert.Note

			for _, note := range res.Notes {
				if note.Message == todo {
					notes = append(notes, note)
				}
			}

			if !slices.Equal(notes, tc.want) {
				t.Errorf("notes %q, want %q", notes, tc.want)
			}

			if got := strings.Contains(string(res.YAML), "license: license.rtf"); got != tc.license {
				t.Errorf("license kept: %v, want %v\n%s", got, tc.license, res.YAML)
			}
		})
	}
}

// TestConvertV3Scope checks that a WiX v3 package without InstallScope,
// which Windows Installer installs per user, is not silently made a
// per-machine package.
func TestConvertV3Scope(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package Platform="x64" />
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(res.YAML), "no InstallScope") {
		t.Errorf("no scope note in\n%s", res.YAML)
	}
}

// codesSource has components that a built package finds by Id, by key file,
// and not at all, plus a shortcut component.
const codesSource = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Directory Id="DocsDir" Name="docs" />
        <Directory Name="extra">
          <Component>
            <File Source="a.txt" />
          </Component>
        </Directory>
      </Directory>
    </StandardDirectory>
    <StandardDirectory Id="ProgramMenuFolder" />
    <Component Id="Main" Directory="INSTALLFOLDER">
      <File Id="main.exe" Source="main.exe" />
    </Component>
    <Component Directory="INSTALLFOLDER">
      <File Source="notes.txt" />
    </Component>
    <Component Directory="DocsDir">
      <File Source="readme.txt" />
    </Component>
    <Component Id="Menu" Directory="ProgramMenuFolder">
      <Shortcut Name="Hello" Target="[#main.exe]" />
      <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="menu" Value="1" KeyPath="yes" />
    </Component>
    <Component Directory="ProgramMenuFolder">
      <Shortcut Name="Hello notes" Target="[#main.exe]" Arguments="notes" />
      <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="notes" Value="1" KeyPath="yes" />
    </Component>
    <Component Id="Unmanaged" Directory="ProgramMenuFolder" Guid="" Permanent="yes">
      <Shortcut Name="Hello tools" Target="[#main.exe]" Arguments="tools" />
    </Component>
    <Component Directory="INSTALLFOLDER" Subdirectory="bin">
      <File Source="tool.exe" />
    </Component>
    <InstallExecuteSequence>
      <RemoveExistingProducts After="InstallExecute" />
    </InstallExecuteSequence>
  </Package>
</Wix>
`

func TestConvertBuiltCodes(t *testing.T) {
	t.Parallel()

	built := &wixconvert.BuiltPackage{Name: "hello.msi", Components: []wixconvert.BuiltComponent{
		{ID: "Main", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "main.exe"},
		// Two rows with the key file notes.txt: no match.
		{ID: "cmpA", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "INSTALLFOLDER", KeyFile: "notes.txt"},
		{ID: "cmpB", Code: "{00000000-0000-4000-8000-000000000003}", Directory: "INSTALLFOLDER", KeyFile: "notes.txt"},
		// Another folder: no match.
		{ID: "cmpC", Code: "{00000000-0000-4000-8000-000000000004}", Directory: "OtherDir", KeyFile: "readme.txt"},
		// A folder without an Id matches any folder.
		{ID: "cmpD", Code: "{00000000-0000-4000-8000-000000000005}", Directory: "dirE1A2", KeyFile: "a.txt"},
		{ID: "Menu", Code: "{00000000-0000-4000-8000-000000000006}", Directory: "ProgramMenuFolder"},
		// A Subdirectory folder has a generated ID in the package.
		{ID: "cmpE", Code: "{00000000-0000-4000-8000-000000000007}", Directory: "dirB7C3", KeyFile: "tool.exe"},
	}}

	res, err := wixconvert.Convert(strings.NewReader(codesSource), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	text := string(res.YAML)
	for _, want := range []string{
		"guid: '{00000000-0000-4000-8000-000000000001}'",
		"guid: '{00000000-0000-4000-8000-000000000005}'",
		// The shortcut component keeps its code and its shortcut.
		"guid: '{00000000-0000-4000-8000-000000000006}'\n    shortcuts:\n      - name: Hello\n        file: main.exe",
		"guid: '{00000000-0000-4000-8000-000000000007}'",
		// An unmanaged shortcut component keeps its shortcut too.
		"guid: none\n    permanent: true\n    shortcuts:\n      - name: Hello tools",
		"The guid of 4 components whose Guid WiX generated comes from hello.msi.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("YAML lacks %q:\n%s", want, text)
		}
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v", err)
	}

	var lines []int

	for _, n := range res.Notes {
		if strings.HasPrefix(n.Message, "hello.msi has no matching component") {
			lines = append(lines, n.Line)
		}
	}

	for _, want := range []string{
		`the WiX value HKCU\Software\Example\Hello\menu stays behind`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("YAML lacks %q:\n%s", want, text)
		}
	}

	if !slices.Equal(lines, []int{17, 20, 27}) {
		t.Errorf("unmatched components in lines %v, want [17 20 27]", lines)
	}

	// Without the package, the late removal names every component, also
	// the shortcut component.
	res, err = wixconvert.Convert(strings.NewReader(codesSource), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := wixconvert.Note{Line: 38, Message: "WiX generated the Guid of a.txt, Main, notes.txt, readme.txt, Menu and 2 more components; " +
		"msi-builder generates other codes"}

	if !slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool {
		return n.Line == want.Line && strings.HasPrefix(n.Message, want.Message)
	}) {
		t.Errorf("notes %v lack %v", res.Notes, want)
	}
}

// A row that a source Id names is not matched again by key file.
func TestConvertBuiltCodesOnce(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Component Id="X" Directory="INSTALLFOLDER">
      <File Source="a.txt" />
    </Component>
    <Component Directory="INSTALLFOLDER" Subdirectory="copy">
      <File Source="a.txt" />
    </Component>
  </Package>
</Wix>
`
	built := &wixconvert.BuiltPackage{Name: "hello.msi", Components: []wixconvert.BuiltComponent{
		{ID: "X", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "a.txt"},
	}}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(res.YAML), "guid: '{00000000-0000-4000-8000-000000000001}'"); n != 1 {
		t.Errorf("the code is pinned %d times:\n%s", n, res.YAML)
	}

	if !slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool {
		return n.Line == 9 && strings.HasPrefix(n.Message, "hello.msi has no matching component")
	}) {
		t.Errorf("notes %v lack the unmatched component in line 9", res.Notes)
	}
}

// A Files element: one file outside a component matches by its name, and a
// glob, which became a folder source, keeps the codes of the files it adds
// in guids, found by their path below its folder, also through a "."
// folder; a file it excludes, or one another component took or names by
// its Guid, keeps none there. A component in a folder WiX named matches its
// key file in that folder only, not a harvested file of the same name. A glob without a file in
// the package gets the note instead, also in a component with an Id.
func TestConvertBuiltCodesFiles(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Files Include="dist\tools\**" Directory="INSTALLFOLDER" Subdirectory="tools"><Exclude Files="dist\tools\*.pdb" /></Files>
    <Files Include="dist\LICENSE.txt" Directory="INSTALLFOLDER" />
    <Component Id="Tools" Directory="INSTALLFOLDER"><Files Include="dist\more\*.dll" /></Component>
    <Component Directory="INSTALLFOLDER" Subdirectory="tools"><File Source="build\helper.exe" /></Component>
    <Component Directory="INSTALLFOLDER" Subdirectory="tools" Guid="00000000-0000-4000-8000-000000000006"><File Source="build\config.xml" /></Component>
  </Package>
</Wix>
`
	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "fil1", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "LICENSE.txt"},
			{ID: "fil2", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "dirA1", KeyFile: "tool.exe"},
			{ID: "fil4", Code: "{00000000-0000-4000-8000-000000000004}", Directory: "dirA2", KeyFile: "Help.md"},
			{ID: "fil5", Code: "{00000000-0000-4000-8000-000000000005}", Directory: "dirA1", KeyFile: "tool.pdb"},
			{ID: "Tools", Code: "{00000000-0000-4000-8000-000000000003}", Directory: "INSTALLFOLDER", KeyFile: "a.dll"},
			{ID: "cmp6", Code: "{00000000-0000-4000-8000-000000000006}", Directory: "dirA1", KeyFile: "config.xml"},
			{ID: "cmp7", Code: "{00000000-0000-4000-8000-000000000007}", Directory: "dirA1", KeyFile: "helper.exe"},
			{ID: "fil8", Code: "{00000000-0000-4000-8000-000000000008}", Directory: "dirA2", KeyFile: "helper.exe"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
			{ID: "dirA1", Parent: "INSTALLFOLDER", Name: "tools"},
			{ID: "dirDot", Parent: "dirA1", Name: "."},
			{ID: "dirA2", Parent: "dirDot", Name: "docs"},
		},
	}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	if lines := globNotes(t, res.Notes, true); !slices.Equal(lines, []int{9}) {
		t.Errorf("glob notes in lines %v, want [9]", lines)
	}

	for _, want := range []string{
		"guid: '{00000000-0000-4000-8000-000000000001}'",
		"guids:\n",
		"docs/Help.md: '{00000000-0000-4000-8000-000000000004}'",
		"tool.exe: '{00000000-0000-4000-8000-000000000002}'",
		"docs/helper.exe: '{00000000-0000-4000-8000-000000000008}'",
		"The guids of 3 files that WiX harvested with Files come from hello.msi.",
	} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("YAML lacks %q:\n%s", want, res.YAML)
		}
	}

	for _, code := range []string{"000000000003", "000000000005"} {
		if strings.Contains(string(res.YAML), code) {
			t.Errorf("YAML has %s:\n%s", code, res.YAML)
		}
	}

	// The components below the glob's folder keep their own codes: one
	// that WiX generated, which pinCode took first, and one from the
	// source; the folder source lists neither.
	for _, file := range []string{" helper.exe:", "config.xml:"} {
		if strings.Contains(string(res.YAML), file) {
			t.Errorf("guids lists %s\n%s", file, res.YAML)
		}
	}

	if !strings.Contains(string(res.YAML), "guid: '{00000000-0000-4000-8000-000000000007}'") {
		t.Errorf("helper.exe lost its code:\n%s", res.YAML)
	}

	res, err = wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if lines := globNotes(t, res.Notes, false); !slices.Equal(lines, []int{7, 9}) {
		t.Errorf("without a package: glob notes in lines %v, want [7 9]", lines)
	}
}

// globNotes returns the lines of the Files glob notes and fails on any
// other note about codes but the late removal of the files in lines 8 and
// 10.
func globNotes(t *testing.T, notes []wixconvert.Note, built bool) []int {
	t.Helper()

	var lines []int

	for _, note := range notes {
		switch {
		case strings.HasPrefix(note.Message, "WiX gave each file of this Files glob a component of its own"),
			strings.HasPrefix(note.Message, "hello.msi has no component for a file of this Files glob"):
			lines = append(lines, note.Line)
		case strings.Contains(note.Message, "no matching component"), strings.HasPrefix(note.Message, "WiX generated the Guid"):
			if built || !strings.Contains(note.Message, "the component in line 8, helper.exe;") {
				t.Errorf("unexpected note %v", note)
			}
		}
	}

	return lines
}

// Advertised shortcuts get a note when the old version is removed late,
// unless convert left them out.
func TestConvertAdvertisedLateRemoval(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    %s
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <StandardDirectory Id="ProgramMenuFolder" />
    <Component Id="Main" Directory="INSTALLFOLDER" Guid="6d1c0a42-93b8-4e57-a2f1-0c7e5d9b3a61">
      <File Source="hello.exe">
        <Shortcut Name="Hello" Directory="ProgramMenuFolder" Advertise="yes" />
        <Shortcut Name="Hello again" Directory="ProgramMenuFolder" Advertise="yes" />
      </File>
    </Component>
    <Component Id="Menu" Directory="ProgramMenuFolder" Guid="2b8f4e17-5a0c-4d93-b6e2-9f1a3c7d5e08">
      <Shortcut Name="Hello docs" Advertise="yes" />
      <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="menu" Value="1" KeyPath="yes" />
      <Shortcut Name="Hello link" Target="[#hello.exe]" Advertise="yes" />
      <Shortcut Name="Hello folder" Target="[INSTALLFOLDER]" Advertise="yes" />
    </Component>
  </Package>
</Wix>
`

	const note = "the WiX package's advertised shortcuts (lines 10, 11, 17, 18) are deleted by this late removal"

	for upgrade, want := range map[string]bool{
		`<MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />`:    true,
		`<MajorUpgrade Schedule="afterInstallInitialize" DowngradeErrorMessage="A newer version is installed." />`: false,
		``: false,
	} {
		res, err := wixconvert.Convert(strings.NewReader(fmt.Sprintf(src, upgrade)), wixconvert.Options{})
		if err != nil {
			t.Fatal(err)
		}

		// The shortcut without a Target in line 15 is left out, so the
		// note does not list it.
		got := slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool { return strings.HasPrefix(n.Message, note) })
		if got != want {
			t.Errorf("%s: note %t, want %t: %v", upgrade, got, want, res.Notes)
		}
	}
}

// TestConvertRemoveFolderExDirectory checks that a util:RemoveFolderEx on a
// directory other than the install dir becomes a removeFolders entry that
// names the directory, whose ID the output keeps.
func TestConvertRemoveFolderExDirectory(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:util="http://wixtoolset.org/schemas/v4/wxs/util">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Directory Id="DATADIR" Name="data" />
      </Directory>
    </StandardDirectory>
    <Feature Id="Main">
      <Component Directory="DATADIR">
        <File Source="hello.exe" />
        <util:RemoveFolderEx On="uninstall" Property="DATADIR" />
      </Component>
    </Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"- id: DATADIR", "directory: DATADIR", "- property: DATADIR"} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("the YAML lacks %q:\n%s", want, res.YAML)
		}
	}
}

// TestConvertUpgradeFloor checks that a hand-made major upgrade of the
// package's own UpgradeCode with a Minimum becomes upgrade.minVersion, and
// that the rows it cannot express keep their TODO (UPG-13).
func TestConvertUpgradeFloor(t *testing.T) {
	t.Parallel()

	const (
		handMade = "is a hand-made major upgrade"
		removal  = `<InstallExecuteSequence><RemoveExistingProducts After="InstallValidate" /></InstallExecuteSequence>`
		noNewer  = "does not detect newer versions"
	)

	for name, tc := range map[string]struct {
		rows, sequence string
		want, not      []string
	}{
		"floor": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" IncludeMinimum="no" Maximum="1.2.0" IncludeMaximum="yes" MigrateFeatures="yes" Property="PREVIOUSFOUND" />
    </Upgrade>
    <Property Id="PREVIOUSFOUND" Secure="yes" />`,
			removal,
			[]string{"allowSameVersion: true", "minVersion: 0.18.0", "includeMin: false", "property: PREVIOUSFOUND", "up to and including 0.18.0", noNewer},
			[]string{handMade, "migrateFeatures", "property: WIX_UPGRADE_DETECTED"},
		},
		"default property": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="OLDERVERSIONBEINGUPGRADED" />
      <UpgradeVersion Minimum="1.2.0" IncludeMinimum="no" OnlyDetect="yes" Property="NEWERVERSIONDETECTED" />
    </Upgrade>
    <Property Id="OLDERVERSIONBEINGUPGRADED" Secure="yes" />
    <Property Id="NEWERVERSIONDETECTED" Secure="yes" />`,
			removal,
			[]string{"allowSameVersion: false", "minVersion: 0.18.0", "migrateFeatures: false"},
			[]string{handMade, "includeMin", "property: OLDERVERSIONBEINGUPGRADED"},
		},
		"no floor": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.0.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			removal,
			[]string{"allowSameVersion: false", "property: PREVIOUSFOUND", noNewer},
			[]string{handMade, "minVersion: ", "older version than"},
		},
		"minimum at this version": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="1.2.0" Maximum="1.2.0" IncludeMaximum="yes" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			removal,
			[]string{handMade},
			[]string{"minVersion: "},
		},
		"own newer detection": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
      <UpgradeVersion Minimum="1.2.0" IncludeMinimum="no" OnlyDetect="yes" Property="NEWERFOUND" />
    </Upgrade>`,
			removal,
			[]string{"minVersion: 0.18.0", "newerProperty: NEWERFOUND"},
			[]string{handMade, noNewer},
		},
		"own inclusive newer detection": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
      <UpgradeVersion Minimum="1.2.0" OnlyDetect="yes" Property="NEWERFOUND" />
    </Upgrade>`,
			removal,
			[]string{"minVersion: 0.18.0", "property: NEWERFOUND"},
			[]string{handMade, noNewer},
		},
		"newer detection of another product": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
    </Upgrade>
    <Upgrade Id="11111111-2222-3333-4444-555555555555">
      <UpgradeVersion Minimum="1.2.0" IncludeMinimum="no" OnlyDetect="yes" Property="OTHERFOUND" />
    </Upgrade>`,
			removal,
			[]string{"minVersion: 0.18.0", "older version than 0.18.0", noNewer},
			[]string{handMade},
		},
		"without RemoveExistingProducts": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			"",
			[]string{handMade},
			[]string{"minVersion: "},
		},
		"maximum below this version": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.0.0" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			removal,
			[]string{handMade},
			[]string{"minVersion: "},
		},
		"next to a MajorUpgrade": {
			`<MajorUpgrade DowngradeErrorMessage="newer" />
    <Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			removal,
			[]string{handMade},
			[]string{"minVersion: "},
		},
		"language": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Language="1033" Property="PREVIOUSFOUND" />
    </Upgrade>`,
			removal,
			[]string{handMade},
			[]string{"minVersion: "},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.2.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    ` + tc.rows + `
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Feature Id="Main">
      <Component Directory="INSTALLFOLDER">
        <File Source="hello.exe" />
      </Component>
    </Feature>
    <SetProperty Id="X" Value="1" After="AppSearch" Condition="WIX_UPGRADE_DETECTED" Sequence="execute" />
    ` + tc.sequence + `
  </Package>
</Wix>`

			res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			yaml := string(res.YAML)
			for _, want := range tc.want {
				if !strings.Contains(yaml, want) {
					t.Errorf("the YAML lacks %q:\n%s", want, yaml)
				}
			}

			for _, not := range tc.not {
				if strings.Contains(yaml, not) {
					t.Errorf("the YAML has %q:\n%s", not, yaml)
				}
			}

			cfg, err := config.Parse(res.YAML, config.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if err := config.Validate(cfg).Err(); err != nil {
				t.Errorf("the converted file does not validate: %v\n%s", err, yaml)
			}
		})
	}
}

// TestConvertDowngradeLaunchCondition checks that a launch condition equal to
// the one downgrade: error writes becomes upgrade.downgradeMessage: the
// LaunchCondition table keys on the condition, so the package cannot hold it
// twice.
func TestConvertDowngradeLaunchCondition(t *testing.T) {
	t.Parallel()

	const handMade = `<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="OLDERVERSIONBEINGUPGRADED" />
      <UpgradeVersion Minimum="1.2.0" IncludeMinimum="no" OnlyDetect="yes" Property="NEWERVERSIONDETECTED" />
    </Upgrade>
    <InstallExecuteSequence><RemoveExistingProducts After="InstallValidate" /></InstallExecuteSequence>`

	for name, tc := range map[string]struct {
		rows      string
		want, not []string
	}{
		"hand-made upgrade": {
			handMade + `
    <Launch Condition="NOT NEWERVERSIONDETECTED" Message="A newer Hello is installed." />
    <Launch Condition="VersionNT64" Message="64-bit only." />`,
			[]string{"downgradeMessage: A newer Hello is installed.", "condition: VersionNT64"},
			[]string{"condition: NOT NEWERVERSIONDETECTED"},
		},
		"own newer property": {
			`<Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Minimum="0.18.0" Maximum="1.2.0" Property="PREVIOUSFOUND" />
      <UpgradeVersion Minimum="1.2.0" IncludeMinimum="no" OnlyDetect="yes" Property="NEWERFOUND" />
    </Upgrade>
    <InstallExecuteSequence><RemoveExistingProducts After="InstallValidate" /></InstallExecuteSequence>
    <Launch Condition="NOT NEWERFOUND" Message="newer" />`,
			[]string{"newerProperty: NEWERFOUND", "downgradeMessage: newer"},
			[]string{"launchConditions"},
		},
		"MajorUpgrade message wins": {
			`<MajorUpgrade DowngradeErrorMessage="from MajorUpgrade" />
    <Launch Condition="NOT WIX_DOWNGRADE_DETECTED" Message="from Launch" />`,
			[]string{"newerProperty: WIX_DOWNGRADE_DETECTED", "downgradeMessage: from MajorUpgrade", `its message "from Launch" is left out`},
			[]string{"launchConditions"},
		},
		"same message": {
			`<MajorUpgrade DowngradeErrorMessage="newer" />
    <Launch Condition="NOT WIX_DOWNGRADE_DETECTED" Message="newer" />`,
			[]string{"downgradeMessage: newer"},
			[]string{"launchConditions", "is left out"},
		},
		"downgrades allowed": {
			`<MajorUpgrade AllowDowngrades="yes" />
    <Launch Condition="NOT NEWERVERSIONDETECTED" Message="newer" />`,
			[]string{"downgrade: allow", "condition: NOT NEWERVERSIONDETECTED"},
			[]string{"downgradeMessage"},
		},
		"preprocessor variable": {
			`<?define NP = "NEWERVERSIONDETECTED" ?>
    <MajorUpgrade DowngradeErrorMessage="x" />
    <Launch Condition="NOT $(var.NP)" Message="newer" />`,
			[]string{"downgradeMessage: x", `its message "newer" is left out`},
			[]string{"launchConditions"},
		},
		"MajorUpgrade without a message": {
			`<MajorUpgrade AllowSameVersionUpgrades="yes" />
    <Launch Condition="NOT WIX_DOWNGRADE_DETECTED" Message="from launch" />`,
			[]string{"downgradeMessage: from launch"},
			[]string{"launchConditions", "is left out"},
		},
		"property of another product": {
			`<MajorUpgrade DowngradeErrorMessage="mu" />
    <Upgrade Id="11111111-2222-3333-4444-555555555555">
      <UpgradeVersion Minimum="0.0.0" OnlyDetect="yes" Property="OTHERFOUND" />
    </Upgrade>
    <Launch Condition="NOT OTHERFOUND" Message="other" />
    <Launch Condition="NOT WIX_DOWNGRADE_DETECTED" Message="mu" />`,
			[]string{"condition: NOT OTHERFOUND", "downgradeMessage: mu"},
			[]string{"condition: NOT WIX_DOWNGRADE_DETECTED"},
		},
		"preprocessor variable in the messages": {
			`<?define M = "m1" ?>
    <MajorUpgrade DowngradeErrorMessage="$(var.M)" />
    <Launch Condition="NOT WIX_DOWNGRADE_DETECTED" Message="m1" />`,
			[]string{"downgradeMessage: ${M}"},
			[]string{"launchConditions", "is left out"},
		},
		"two folded launch conditions": {
			`<?define NP = "NEWERVERSIONDETECTED" ?>
    <Launch Condition="NOT NEWERVERSIONDETECTED" Message="a" />
    <Launch Condition="NOT $(var.NP)" Message="b" />`,
			[]string{"downgradeMessage: a", `its message "b" is left out for the Launch on line`},
			[]string{"launchConditions:", "MajorUpgrade's"},
		},
		"undefined preprocessor variable": {
			`<MajorUpgrade DowngradeErrorMessage="x" />
    <Launch Condition="NOT $(var.NP)" Message="newer" />`,
			[]string{"condition: NOT ${NP}", "if NOT ${NP} is the property"},
			nil,
		},
		"no upgrade elements": {
			`<Launch Condition="NOT NEWERVERSIONDETECTED" Message="newer" />`,
			[]string{"downgradeMessage: newer", "with upgrade: false, add it back to launchConditions"},
			[]string{"launchConditions:"},
		},
		"not exactly the block": {
			handMade + `
    <Launch Condition="NOT NEWERVERSIONDETECTED OR Installed" Message="newer" />`,
			[]string{"condition: NOT NEWERVERSIONDETECTED OR Installed"},
			[]string{"downgradeMessage"},
		},
		"other property": {
			handMade + `
    <Launch Condition="NOT OTHERFOUND" Message="other" />`,
			[]string{"condition: NOT OTHERFOUND"},
			[]string{"downgradeMessage"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.2.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    ` + tc.rows + `
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Feature Id="Main">
      <Component Directory="INSTALLFOLDER">
        <File Source="hello.exe" />
      </Component>
    </Feature>
  </Package>
</Wix>`

			res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			yaml := string(res.YAML)
			for _, want := range tc.want {
				if !strings.Contains(yaml, want) {
					t.Errorf("the YAML lacks %q:\n%s", want, yaml)
				}
			}

			for _, not := range tc.not {
				if strings.Contains(yaml, not) {
					t.Errorf("the YAML has %q:\n%s", not, yaml)
				}
			}

			cfg, err := config.Parse(res.YAML, config.Options{Vars: map[string]string{"NP": "OTHERFOUND"}})
			if err != nil {
				t.Fatal(err)
			}

			if err := config.Validate(cfg).Err(); err != nil {
				t.Errorf("the converted file does not validate: %v\n%s", err, yaml)
			}
		})
	}
}

// TestConvertRelatedNewerCondition checks that a launch condition on a
// property that another product's Upgrade row sets stays a launch condition:
// it refuses that product, not a newer version of this one.
func TestConvertRelatedNewerCondition(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.2.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Upgrade Id="11111111-2222-3333-4444-555555555555">
      <UpgradeVersion Minimum="0.0.0" OnlyDetect="yes" Property="NEWERVERSIONDETECTED" />
    </Upgrade>
    <Launch Condition="NOT NEWERVERSIONDETECTED" Message="The other product is installed." />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	yaml := string(res.YAML)
	if !strings.Contains(yaml, "condition: NOT NEWERVERSIONDETECTED") || strings.Contains(yaml, "downgradeMessage") {
		t.Errorf("the launch condition of the other product was folded:\n%s", yaml)
	}
}

// TestConvertV3DowngradeCondition checks that a v3 Condition element under
// Product that repeats the downgrade block becomes upgrade.downgradeMessage.
func TestConvertV3DowngradeCondition(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallScope="perMachine" Platform="x64" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Condition Message="newer"> NOT WIX_DOWNGRADE_DETECTED </Condition>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	yaml := string(res.YAML)
	if !strings.Contains(yaml, "downgradeMessage: newer") || strings.Contains(yaml, "launchConditions") {
		t.Errorf("the condition was not folded into downgradeMessage:\n%s", yaml)
	}
}

// TestConvertRemoveFolderExBothOwnFolder checks that On="both" on the
// component's own fixed folder keeps its uninstall half as purge.
func TestConvertRemoveFolderExBothOwnFolder(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:util="http://wixtoolset.org/schemas/v4/wxs/util">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="CommonAppDataFolder">
      <Directory Id="DataDir" Name="Hello" />
    </StandardDirectory>
    <Feature Id="Main">
      <Component Directory="DataDir">
        <File Source="hello.conf" />
        <util:RemoveFolderEx On="both" Property="DataDir" />
      </Component>
    </Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"purge: true", "the install half of util:RemoveFolderEx"} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("the YAML lacks %q:\n%s", want, res.YAML)
		}
	}
}

// TestConvertUnmappedNotes checks the TODOs of WiX constructs whose output
// cannot validate or would be lost silently.
func TestConvertUnmappedNotes(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui"
     xmlns:fw="http://wixtoolset.org/schemas/v4/wxs/firewall">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="VOLUME">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe"><fw:FirewallException Name="Hello" Port="4985" /></File></Component>
        <Component><File Source="hellod.exe"><fw:FirewallException Name="Hello" Port="4985" /></File></Component>
      </Directory>
    </Directory>
    <SetDirectory Id="VOLUME" Value="[WindowsVolume]" />
    <CustomAction Id="Notes" Directory="INSTALLFOLDER" ExeCommand="explorer.exe https://example.com/notes" Return="asyncNoWait" />
    <ui:WixUI Id="WixUI_Minimal" />
    <UI><Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="Notes" Condition="NOT Installed" /></UI>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"the Directory VOLUME has no Name", "the Finish button runs the action Notes",
		"the firewall exception on line 7 has the same name",
	} {
		if !slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool { return strings.HasPrefix(n.Message, want) }) {
			t.Errorf("no note %q in %q", want, res.Notes)
		}
	}
}

// TestConvertWindows1252 checks that a source declared windows-1252 is
// decoded: 0xE9 is é there and invalid UTF-8.
func TestConvertWindows1252(t *testing.T) {
	t.Parallel()

	src := "<?xml version=\"1.0\" encoding=\"windows-1252\"?>\n" +
		`<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <SummaryInformation Description="Hello ` + "\xe9" + `dition" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(res.YAML), "Hello édition") {
		t.Errorf("the description was not decoded:\n%s", res.YAML)
	}
}

// TestConvertBuiltCodeOfKeyPathID checks that a component without an Id
// finds its built row by the Id of its key file, which WiX gives it.
func TestConvertBuiltCodeOfKeyPathID(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Id="main.exe" Source="main.exe" />
        </Component>
        <!-- WiX names it after the registry value, not the file. -->
        <Component>
          <RegistryKey Root="HKLM" Key="Software\Example\Hello">
            <RegistryValue Id="installed" Name="installed" Value="1" Type="string" KeyPath="yes" />
          </RegistryKey>
          <File Id="notes.txt" Source="notes.txt" />
        </Component>
        <!-- Its key path is no file, so no built row matches its file. -->
        <Component>
          <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="extra" Value="1" Type="string" KeyPath="yes" />
          <File Source="extra.txt" />
        </Component>
        <!-- An HKCU key path before a file stays the key path. -->
        <Component Id="Prefs">
          <RegistryValue Id="prefs" Root="HKCU" Key="Software\Example\Hello" Name="prefs" Value="1" Type="integer" KeyPath="yes" />
          <File Id="prefs.ini" Source="prefs.ini" />
        </Component>
      </Directory>
    </StandardDirectory>
  </Package>
</Wix>`

	built := &wixconvert.BuiltPackage{Name: "hello.msi", Components: []wixconvert.BuiltComponent{
		{ID: "main.exe", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "main.exe"},
		{ID: "installed", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "INSTALLFOLDER"},
		{ID: "other", Code: "{00000000-0000-4000-8000-000000000003}", Directory: "INSTALLFOLDER", KeyFile: "extra.txt"},
	}}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"guid: '{00000000-0000-4000-8000-000000000001}'", "- id: installed\n",
		"guid: '{00000000-0000-4000-8000-000000000002}'", "keyPath: installed\n",
		"keyPath: prefs\n",
	} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("YAML lacks %q:\n%s", want, res.YAML)
		}
	}

	if strings.Contains(string(res.YAML), "000000000003") {
		t.Errorf("a component with a registry key path took the code of a file component:\n%s", res.YAML)
	}
}

// TestConvertKeyFileIDs checks the Ids that components without one take
// from their key file: a define is filled in, a name over 72 characters is
// no Id, and a generated Id skips the names files took.
func TestConvertKeyFileIDs(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("A", 76) + ".dll"
	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <?define Exe = "hello.exe" ?>
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="a" Value="1" Type="string" KeyPath="yes" /></Component>
        <Component><File Source="Component1" /></Component>
        <Component><File Source="dist\$(var.Exe)" /></Component>
        <Component><File Source="` + long + `" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="MainFeature"><ComponentRef Id="hello.exe" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(res.YAML), "id: "+long) {
		t.Errorf("a name over 72 characters became an Id:\n%s", res.YAML)
	}

	if !strings.Contains(string(res.YAML), "- id: hello.exe\n") {
		t.Errorf("the component of $(var.Exe) is not hello.exe:\n%s", res.YAML)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, res.YAML)
	}
}

// A file two Files globs add goes to the glob whose folder is nearest above
// it, among those whose source folder holds it when convert can look; with
// the same folder it goes to neither, and both name it.
func TestConvertBuiltCodesNestedGlobs(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Files Include="bin\**" Directory="INSTALLFOLDER" />
    <Files Include="assets\**" Directory="INSTALLFOLDER" Subdirectory="assets" />
    %s
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "fil1", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "app.exe"},
			{ID: "fil2", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "dirA1", KeyFile: "logo.png"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
			{ID: "dirA1", Parent: "INSTALLFOLDER", Name: "assets"},
		},
	}

	res, err := wixconvert.Convert(strings.NewReader(fmt.Sprintf(src, "")), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		" app.exe: '{00000000-0000-4000-8000-000000000001}'",
		" logo.png: '{00000000-0000-4000-8000-000000000002}'",
	} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("YAML lacks %q:\n%s", want, res.YAML)
		}
	}

	if strings.Contains(string(res.YAML), "assets/logo.png") {
		t.Errorf("bin/ took logo.png:\n%s", res.YAML)
	}

	if lines := globNotes(t, res.Notes, true); len(lines) > 0 {
		t.Errorf("glob notes in lines %v, want none", lines)
	}

	// When only bin/ holds assets/logo.png next to the source, the nearer
	// assets/ glob does not take its code.
	res, err = wixconvert.Convert(strings.NewReader(fmt.Sprintf(src, "")), wixconvert.Options{
		Built: built, Exists: func(rel string) bool { return rel == "bin/assets/logo.png" || rel == "bin/app.exe" },
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(res.YAML), "assets/logo.png: '{00000000-0000-4000-8000-000000000002}'") {
		t.Errorf("bin/ lacks assets/logo.png:\n%s", res.YAML)
	}

	// A second glob into the install folder adds app.exe too.
	res, err = wixconvert.Convert(strings.NewReader(fmt.Sprintf(src, `<Files Include="lib\**" Directory="INSTALLFOLDER" />`)), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(res.YAML), "000000000001") {
		t.Errorf("a glob took a file another glob adds to the same folder:\n%s", res.YAML)
	}

	if lines := globNotes(t, res.Notes, true); len(lines) > 0 {
		t.Errorf("glob notes in lines %v, want only the tie notes", lines)
	}

	var tied []int

	for _, note := range res.Notes {
		if strings.HasPrefix(note.Message, "another Files glob into the same folder may add app.exe too") {
			tied = append(tied, note.Line)
		}
	}

	if !slices.Equal(tied, []int{7, 9}) {
		t.Errorf("tie notes in lines %v, want [7 9]", tied)
	}
}

// A file that two globs into the same folder add keeps no code, and both
// globs name it, also when each pinned other files, unless only one of
// their source folders holds it.
func TestConvertBuiltCodesTiedGlobs(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Files Include="bin\a*" Directory="INSTALLFOLDER" />
    <Files Include="lib\*.exe" Directory="INSTALLFOLDER" />
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "fil1", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "app.exe"},
			{ID: "fil2", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "INSTALLFOLDER", KeyFile: "abc.txt"},
			{ID: "fil3", Code: "{00000000-0000-4000-8000-000000000003}", Directory: "INSTALLFOLDER", KeyFile: "x.exe"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
		},
	}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{" abc.txt: '{00000000-0000-4000-8000-000000000002}'", " x.exe: '{00000000-0000-4000-8000-000000000003}'"} {
		if !strings.Contains(string(res.YAML), want) {
			t.Errorf("YAML lacks %q:\n%s", want, res.YAML)
		}
	}

	if strings.Contains(string(res.YAML), "000000000001") {
		t.Errorf("a glob took app.exe:\n%s", res.YAML)
	}

	var lines []int

	for _, note := range res.Notes {
		if strings.HasPrefix(note.Message, "another Files glob into the same folder may add app.exe too") {
			lines = append(lines, note.Line)
		}
	}

	if !slices.Equal(lines, []int{7, 8}) {
		t.Errorf("tie notes in lines %v, want [7 8]: %v", lines, res.Notes)
	}

	// With the source folders at hand, app.exe goes to the glob whose
	// folder holds it.
	res, err = wixconvert.Convert(strings.NewReader(src), wixconvert.Options{
		Built: built, Exists: func(rel string) bool { return rel == "lib/app.exe" },
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := "app.exe: '{00000000-0000-4000-8000-000000000001}'\n"; !strings.Contains(string(res.YAML), want) ||
		strings.Index(string(res.YAML), want) < strings.Index(string(res.YAML), "source: lib/") {
		t.Errorf("lib/ lacks app.exe:\n%s", res.YAML)
	}

	for _, note := range res.Notes {
		if strings.HasPrefix(note.Message, "another Files glob") {
			t.Errorf("unexpected tie note %v", note)
		}
	}
}

// A glob into a WiX 6432 folder finds its folder under the name of the
// platform WiX built for; a glob whose folder the package lacks says so.
func TestConvertBuiltCodes6432Folder(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <Files Include="dist\**" Directory="ProgramFiles6432Folder" Subdirectory="Hello" />
  </Package>
</Wix>
`

	for _, platform := range []string{"ProgramFiles64Folder", "ProgramFilesFolder"} {
		built := &wixconvert.BuiltPackage{
			Name: "hello.msi",
			Components: []wixconvert.BuiltComponent{
				{ID: "fil1", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "dirH", KeyFile: "app.exe"},
			},
			Directories: []wixconvert.BuiltDirectory{
				{ID: "TARGETDIR", Name: "SourceDir"},
				{ID: platform, Parent: "TARGETDIR", Name: "PFiles"},
				{ID: "dirH", Parent: platform, Name: "Hello"},
			},
		}

		res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(string(res.YAML), "app.exe: '{00000000-0000-4000-8000-000000000001}'") {
			t.Errorf("%s: app.exe has no guid:\n%s", platform, res.YAML)
		}
	}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: &wixconvert.BuiltPackage{Name: "hello.msi"}})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool {
		return strings.HasPrefix(n.Message, "convert did not find the folder of this Files glob in hello.msi")
	}) {
		t.Errorf("no note on the missing folder: %v", res.Notes)
	}
}

// TestConvertCloseApplication checks the util:CloseApplication cases that
// cannot become files[].close: a target that is no program, one a Files
// glob may install, and the Property a closing element also sets.
func TestConvertCloseApplication(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:util="http://wixtoolset.org/schemas/v4/wxs/util">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <ComponentGroup Id="Harvested" Directory="INSTALLFOLDER">
      <Files Include="dist\**" />
    </ComponentGroup>
    <ComponentGroup Id="Own" Directory="INSTALLFOLDER">
      <Component>
        <File Source="hello.exe" />
        <File Source="hello.com" />
      </Component>
    </ComponentGroup>
    <Feature Id="Main"><ComponentGroupRef Id="Harvested" /><ComponentGroupRef Id="Own" /></Feature>
    <util:CloseApplication Target="hello.com" CloseMessage="yes" />
    <util:CloseApplication Target="example.exe" CloseMessage="yes" />
    <util:CloseApplication Target="hello.exe" CloseMessage="yes" Property="HELLORUNNING" PromptToContinue="yes" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"closes hello.com, which is no program ending in .exe",
		"closes example.exe, which is no single file of this package; when a Files glob (line 7) installs it",
		"also sets the property HELLORUNNING while hello.exe in INSTALLFOLDER runs",
		"the attributes RebootPrompt, PromptToContinue of <util:CloseApplication> map to it only roughly or not at all",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if got := strings.Count(out, "close:"); got != 1 {
		t.Errorf("the YAML closes %d files, want only hello.exe:\n%s", got, out)
	}
}

// TestConvertCloseApplicationV3Src checks that a v3 File with the
// deprecated src attribute is found by its installed name.
func TestConvertCloseApplicationV3Src(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi" xmlns:util="http://schemas.microsoft.com/wix/UtilExtension">
  <Product Id="*" Name="Hello" Language="1033" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="C" Guid="*">
            <File Id="hello" src="bin\hello.exe" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <util:CloseApplication Id="x" Target="hello.exe" CloseMessage="yes">NOT Installed</util:CloseApplication>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(res.YAML), "close: {terminate: false}") {
		t.Errorf("hello.exe is not closed:\n%s", res.YAML)
	}
}

// TestConvertCloseApplicationUndefinedVar checks that variables defined
// only on the WiX command line, in the Target or before the file name in
// the Source, still close the file, and that files of one source in
// several folders each get their notes.
func TestConvertCloseApplicationUndefinedVar(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:util="http://wixtoolset.org/schemas/v4/wxs/util">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Source="bin\$(var.ExeName)" />
          <File Source="$(var.App.TargetDir)hello.exe" />
        </Component>
        <Directory Id="X86" Name="x86">
          <Component>
            <File Source="$(var.App.TargetDir)hello.exe" />
          </Component>
        </Directory>
      </Directory>
    </StandardDirectory>
    <util:CloseApplication Target="$(var.ExeName)" CloseMessage="yes" />
    <util:CloseApplication Target="hello.exe" CloseMessage="yes" Timeout="10" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	if got := strings.Count(out, "close: {terminate: false}"); got != 3 {
		t.Errorf("the YAML closes %d files, want 3:\n%s", got, out)
	}

	// Both copies of hello.exe get every note, told apart by their folder.
	if got := strings.Count(out, "the attributes RebootPrompt, Timeout of"); got != 2 {
		t.Errorf("the YAML notes Timeout on %d files, want 2:\n%s", got, out)
	}
}

// TestConvertBuiltCodesRegistryKeyPath checks that a component without an
// Id whose key path is a registry value is found by that value's Root, Key
// and Name, also inside a RegistryKey, and only when one row matches.
func TestConvertBuiltCodesRegistryKeyPath(t *testing.T) {
	t.Parallel()

	reg := func(name string) *wixconvert.BuiltRegistry {
		return &wixconvert.BuiltRegistry{Root: 2, Key: `SOFTWARE\Example\Hello`, Name: name}
	}

	built := &wixconvert.BuiltPackage{Name: "hello.msi", Components: []wixconvert.BuiltComponent{
		{ID: "regA", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyRegistry: reg("A")},
		{ID: "regB", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "INSTALLFOLDER", KeyRegistry: reg("B")},
		// Two rows with the value C: no match.
		{ID: "regC1", Code: "{00000000-0000-4000-8000-000000000003}", Directory: "INSTALLFOLDER", KeyRegistry: reg("C")},
		{ID: "regC2", Code: "{00000000-0000-4000-8000-000000000004}", Directory: "INSTALLFOLDER", KeyRegistry: reg("C")},
		// HKCU, not HKLM: no match.
		{ID: "regD", Code: "{00000000-0000-4000-8000-000000000005}", Directory: "INSTALLFOLDER",
			KeyRegistry: &wixconvert.BuiltRegistry{Root: 1, Key: `SOFTWARE\Example\Hello`, Name: "D"}},
	}}

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
    <Feature Id="Main">
      <Component Directory="INSTALLFOLDER">
        <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="A" Value="1" Type="string" />
      </Component>
      <Component Directory="INSTALLFOLDER">
        <RegistryKey Root="HKLM" Key="SOFTWARE\Example">
          <RegistryValue Key="hello" Name="b" Value="1" Type="string" KeyPath="yes" />
        </RegistryKey>
      </Component>
      <Component Directory="INSTALLFOLDER">
        <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="C" Value="1" Type="string" />
      </Component>
      <Component Directory="INSTALLFOLDER">
        <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="D" Value="1" Type="string" />
      </Component>
    </Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{"{00000000-0000-4000-8000-000000000001}", "{00000000-0000-4000-8000-000000000002}"} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %s:\n%s", want, out)
		}
	}

	for _, unwanted := range []string{"-000000000003}", "-000000000004}", "-000000000005}"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the YAML has %s:\n%s", unwanted, out)
		}
	}
}

// TestConvertRealWorldBatch checks the fixes from a real-world rerun: a
// bind variable in a Directory name, ALLUSERS=2 with and without
// MSIINSTALLPERUSER, a ResetPeriodInDays above the limit and an action
// anchored on a WiX extension's action that the source schedules itself.
func TestConvertRealWorldBatch(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:util="http://wixtoolset.org/schemas/v4/wxs/util">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" InstallerVersion="200">
    <Property Id="ALLUSERS" Value="2" />
    %s
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="!(bind.Property.ProductName)">
        <Component>
          <File Source="hello.exe" />
          <ServiceInstall Name="Hello" Start="auto" Type="ownProcess" ErrorControl="normal">
            <util:ServiceConfig ResetPeriodInDays="49710" FirstFailureActionType="restart" SecondFailureActionType="none" ThirdFailureActionType="none" />
          </ServiceInstall>
        </Component>
      </Directory>
    </StandardDirectory>
    <CustomAction Id="FailRunning" Error="Close Hello first." />
    <InstallExecuteSequence>
      <Custom Action="override Wix4CloseApplications_X64" Before="InstallValidate" />
      <Custom Action="FailRunning" After="Wix4CloseApplications_X64" Condition="HELLORUNNING" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	for perUser, scope := range map[string]string{"": "perMachineOrUser", "1": "perUserOrMachine"} {
		prop := ""
		if perUser != "" {
			prop = `<Property Id="MSIINSTALLPERUSER" Value="` + perUser + `" />`
		}

		res, err := wixconvert.Convert(strings.NewReader(fmt.Sprintf(src, prop)), wixconvert.Options{})
		if err != nil {
			t.Fatal(err)
		}

		out := string(res.YAML)
		for _, want := range []string{
			"scope: " + scope, `installerVersion: "5.0"`, "  name: Hello\n",
			"resetAfterDays: 24855", "ResetPeriodInDays=49710 is above 24855 days",
			"{sequence: execute, before: InstallValidate, condition: HELLORUNNING}",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("MSIINSTALLPERUSER=%q: the YAML lacks %q:\n%s", perUser, want, out)
			}
		}

		if strings.Contains(out, "MSIINSTALLPERUSER:") || strings.Contains(out, "!(bind") {
			t.Errorf("MSIINSTALLPERUSER=%q: the YAML keeps MSIINSTALLPERUSER or a bind variable:\n%s", perUser, out)
		}
	}
}

// TestConvertLoneFeature checks that a lone feature with only an Id keeps
// it, so that ADDLOCAL and the feature states of an upgrade from the WiX
// package still name it.
func TestConvertLoneFeature(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <FeatureRef Id="Main" />
  </Package>
  <Fragment><Feature Id="Main"><ComponentGroupRef Id="Group" /></Feature></Fragment>
  <Fragment>
    <ComponentGroup Id="Group" Directory="INSTALLFOLDER">
      <Component Id="Settings"><RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="A" Value="1" Type="string" /></Component>
    </ComponentGroup>
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
  </Fragment>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if want := "features:\n  - id: Main\n    components: [Settings]\n"; !strings.Contains(string(res.YAML), want) {
		t.Errorf("the YAML lacks %q:\n%s", want, res.YAML)
	}
}

// TestConvertExtensionAnchorSides checks that actions on both sides of a
// WiX extension's action keep their order, and that a WixVariable later in
// the file fills in a folder name.
func TestConvertExtensionAnchorSides(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="!(wix.FolderName)">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <CustomAction Id="Pre" Error="Pre" />
    <CustomAction Id="Post" Error="Post" />
    <InstallExecuteSequence>
      <Custom Action="override Wix4CloseApplications_X64" Before="InstallValidate" />
      <Custom Action="Pre" Before="Wix4CloseApplications_X64" Condition="A" />
      <Custom Action="Post" After="Wix4CloseApplications_X64" Condition="B" />
    </InstallExecuteSequence>
    <WixVariable Id="FolderName" Value="Hello" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"{sequence: execute, before: CostFinalize, condition: A}",
		"{sequence: execute, after: CostFinalize, condition: B}",
		"installDir:\n  id: INSTALLFOLDER\n  name: Hello\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}
}

// TestConvertExtensionAnchorSetter checks that a SetProperty on the near
// side of a WiX extension's action keeps an action on its far side behind
// it.
func TestConvertExtensionAnchorSetter(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <SetProperty Id="PREP" Value="1" Before="Wix4CloseApplications_X64" Sequence="execute" />
    <CustomAction Id="Post" Property="POSTP" Value="[PREP]" />
    <InstallExecuteSequence>
      <Custom Action="override Wix4CloseApplications_X64" Before="LaunchConditions" />
      <Custom Action="Post" After="Wix4CloseApplications_X64" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{"{sequence: execute, before: CostFinalize}", "{sequence: execute, after: CostFinalize}"} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}
}

// TestConvertUnscheduledActions checks that a custom action no sequence
// schedules is left out with a note, naming the DoAction control event that
// runs it, and that a WixUI element inside UI picks the wizard.
func TestConvertUnscheduledActions(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
    <CustomAction Id="Check" Property="CHECKED" Value="1" />
    <CustomAction Id="Never" Property="NEVER" Value="1" />
    <UI>
      <ui:WixUI Id="WixUI_FeatureTree" />
      <Publish Dialog="LicenseAgreementDlg" Control="Next" Event="DoAction" Value="Check" />
      <Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="Readme" Condition="SHOWREADME" />
      <Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="Launch" Condition="WIXUI_EXITDIALOGOPTIONALCHECKBOX = 1" />
    </UI>
    <Binary Id="Tools" SourceFile="tools.dll" />
    <CustomAction Id="Readme" BinaryRef="Tools" DllEntry="ShowReadme" />
    <CustomAction Id="Launch" FileRef="hello.exe" ExeCommand="" Return="asyncNoWait" />
    <CustomAction Id="Adv" Property="ADV" Value="1" />
    <AdvertiseExecuteSequence><Custom Action="Adv" After="CostFinalize" /></AdvertiseExecuteSequence>
    <Binary Id="Shared" SourceFile="shared.dll" />
    <CustomAction Id="Used" BinaryRef="Shared" DllEntry="Run" Execute="deferred" Impersonate="no" />
    <CustomAction Id="Unused" BinaryRef="Shared" DllEntry="Other" />
    <InstallExecuteSequence><Custom Action="Used" Before="InstallFinalize" /></InstallExecuteSequence>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"the action Check runs only from the DoAction control event of the dialog LicenseAgreementDlg (line 14)",
		"no sequence schedules the action Never, so it never runs; convert left it out",
		"the action Readme runs only from the DoAction control event of the dialog ExitDialog (line 15), which has no " +
			"counterpart; convert left it out; drop the binary Tools from binaries unless something else uses it",
		"only <AdvertiseExecuteSequence> (line 22), which has no counterpart, schedules the action Adv; convert left it out",
		"no sequence schedules the action Unused, so it never runs; convert left it out\n",
		"  enabled: true\n", "  chooseFeatures: true\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "id: Never") || strings.Contains(out, "id: Unused") {
		t.Errorf("the YAML keeps an unscheduled action:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertLocalActionRefAndIcon(t *testing.T) {
	t.Parallel()

	src := `<?define Idx="2"?>
<?define IconFile="hello.exe"?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Icon Id="App.ico" SourceFile="app.ico" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Source="hello.exe" />
          <ProgId Id="Example.Doc" Description="Hello document" Icon="doc.ico">
            <Extension Id="hello"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="Example.Own" Description="Own icon" Icon="hello.exe">
            <Extension Id="hellown"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="Example.Var" Description="Var icon" Icon="hello.exe" IconIndex="$(var.Idx)">
            <Extension Id="hellovar"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="Example.Defined" Description="Defined icon" Icon="$(var.IconFile)" IconIndex="1">
            <Extension Id="hellodef"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="Example.Elsewhere" Description="Icon elsewhere" Icon="other.dll">
            <Extension Id="helloels"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="Example.Table" Description="Icon table" Icon="App.ico">
            <Extension Id="hellotab"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
        <Component><File Source="doc.ico" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /><ComponentRef Id="doc.ico" /></Feature>
    <CustomActionRef Id="SaveDir" />
    <CustomActionRef Id="SetFoo" />
  </Package>
  <Fragment>
    <SetProperty Id="FOO" Action="SetFoo" Value="bar" Before="CostFinalize" Sequence="execute" />
    <CustomAction Id="SaveDir" Property="SAVED" Value="[INSTALLFOLDER]" />
    <InstallExecuteSequence><Custom Action="SaveDir" Before="InstallFinalize" /></InstallExecuteSequence>
  </Fragment>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	if strings.Contains(out, "comes from a WiX extension") {
		t.Errorf("a locally defined action is noted as an extension's:\n%s", out)
	}

	for _, want := range []string{
		"the ProgId Example.Doc takes its icon from doc.ico", `HKCR\Example.Doc\DefaultIcon = [#doc.ico],0`,
		"progId: Example.Own\n", "iconIndex: 0\n", "iconIndex: 2\n", "iconIndex: 1\n",
		"the ProgId Example.Elsewhere takes its icon from other.dll (a file this source does not define), not from the file it opens",
		"the ProgId Example.Table takes its icon from the <Icon> App.ico, which only advertised ProgIds use",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "iconIndex:") != 3 {
		t.Errorf("want iconIndex only on Example.Own, Example.Var and Example.Defined:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

// TestConvertComponentSearchKeyPath checks that a ComponentSearch with
// Type="directory" gets keyPath: directory only for another product's
// component, also one the --msi package holds from a merge module: for one
// of the source, also one whose code --msi pins, the builder derives the
// type itself (SEARCH-08).
func TestConvertComponentSearchKeyPath(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component Id="Folder" Guid="*"><CreateFolder /></Component>
      </Directory>
    </StandardDirectory>
    <Property Id="OWN_DIR"><ComponentSearch Id="OwnSearch" Guid="{00000000-0000-4000-8000-0000000000aa}" Type="directory" /></Property>
    <Property Id="OTHER_DIR"><ComponentSearch Id="OtherSearch" Guid="{00000000-0000-4000-8000-0000000000bb}" Type="directory" /></Property>
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "Folder", Code: "{00000000-0000-4000-8000-0000000000AA}", Directory: "INSTALLFOLDER"},
			// A merge module's component is not the source's.
			{ID: "MergedFolder.1A2B", Code: "{00000000-0000-4000-8000-0000000000BB}", Directory: "INSTALLFOLDER"},
		},
	}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(string(res.YAML), "keyPath: directory"); got != 1 {
		t.Fatalf("keyPath: directory appears %d times, want once (OTHER_DIR):\n%s", got, res.YAML)
	}

	// The properties keep the source order, so the one keyPath follows
	// OTHER_DIR, which follows OWN_DIR.
	text := string(res.YAML)
	if own, other, key := strings.Index(text, "OWN_DIR:"), strings.Index(text, "OTHER_DIR:"), strings.Index(text, "keyPath: directory"); own < 0 || other < own || key < other {
		t.Errorf("keyPath: directory is not under OTHER_DIR:\n%s", res.YAML)
	}
}

func TestConvertFileSource(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" FileSource="dist\app\">
        <Component><File Name="hello.exe" /></Component>
        <Directory Id="DocDir" Name="doc">
          <Component><File Name="readme.txt" /></Component>
        </Directory>
        <Directory Id="Same">
          <Component><File Name="same.txt" /></Component>
        </Directory>
      </Directory>
      <Directory Id="Other" Name="Other">
        <Component><File Name="other.txt" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello">
      <ComponentRef Id="hello.exe" /><ComponentRef Id="readme.txt" /><ComponentRef Id="other.txt" />
      <ComponentRef Id="same.txt" /><ComponentRef Id="ref.txt" />
    </Feature>
  </Package>
  <Fragment>
    <DirectoryRef Id="Other" FileSource="build\out\">
      <Component><File Name="ref.txt" /></Component>
    </DirectoryRef>
  </Fragment>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"source: dist/app/hello.exe\n", "source: dist/app/doc/readme.txt\n", "source: dist/app/same.txt\n",
		"source: build/out/ref.txt\n", "convert joined the FileSource of an outer folder with the folder names below it",
		`WiX finds "other.txt" through its bind paths`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "through its bind paths") != 1 || strings.Count(out, "check the path") != 1 {
		t.Errorf("want the bind path note only for other.txt and the check note only for readme.txt:\n%s", out)
	}
}

func TestConvertAdvancedFolder(t *testing.T) {
	t.Parallel()

	perUser := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" Scope="perUser">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="ApplicationFolderName" Value="Example Corp\Hello" />
    <StandardDirectory Id="LocalAppDataFolder">
      <Directory Id="APPLICATIONFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
    <ui:WixUI Id="WixUI_Advanced" />
  </Package>
</Wix>`

	user, err := wixconvert.Convert(strings.NewReader(perUser), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if out := string(user.YAML); strings.Contains(out, "name: Example Corp") || strings.Contains(out, "ProgramFilesFolder") {
		t.Errorf("a per-user package took the per-machine folder:\n%s", out)
	}

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" Scope="perUserOrMachine">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="ApplicationFolderName" Value="Example Corp\Hello" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="APPLICATIONFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
    <ui:WixUI Id="WixUI_Advanced" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"name: Example Corp/Hello\n", "parent: ProgramFilesFolder\n",
		"WixUI_Advanced puts this 64-bit package below Program Files (x86)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertProductSearch(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="OLDTOOL" Secure="yes">
      <ProductSearch UpgradeCode="{6f2504e0-4f89-41d3-9a0c-0305e82c3302}" Maximum="2.0.0" IncludeMaximum="yes" />
    </Property>
    <Property Id="BYCODE"><ProductSearch Guid="{7f2504e0-4f89-41d3-9a0c-0305e82c3303}" /></Property>
    <Property Id="ANYTOOL"><ProductSearch UpgradeCode="{8f2504e0-4f89-41d3-9a0c-0305e82c3304}" /></Property>
    <Property Id="WITHVALUE" Value="none"><ProductSearch UpgradeCode="{9f2504e0-4f89-41d3-9a0c-0305e82c3305}" /></Property>
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"upgradeCode: '{6f2504e0-4f89-41d3-9a0c-0305e82c3302}'", "maxVersion: 2.0.0", "includeMax: true",
		"onlyDetect: true", "property: OLDTOOL", "<ProductSearch> has no counterpart in properties.search",
		"minVersion: 0.0.0", "property: ANYTOOL", "WITHVALUE: none", "allowSameVersion: false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "<ProductSearch> has no counterpart") != 2 || strings.Contains(out, "OLDTOOL:") ||
		strings.Contains(out, "9f2504e0") {
		t.Errorf("want OLDTOOL only as a related row:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertSameVersionUpgrades(t *testing.T) {
	t.Parallel()

	for attrs, want := range map[string]bool{
		``:                               true,
		`AllowSameVersionUpgrades="yes"`: false,
		`AllowDowngrades="yes"`:          false,
		`AllowSameVersionUpgrades="no"`:  true,
	} {
		src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade ` + attrs + ` DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello"><Component><File Source="hello.exe" /></Component></Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
  </Package>
</Wix>`

		res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
		if err != nil {
			t.Fatal(err)
		}

		if got := strings.Contains(string(res.YAML), "allowSameVersion: false"); got != want {
			t.Errorf("%s: allowSameVersion: false written = %v, want %v:\n%s", attrs, got, want, res.YAML)
		}
	}
}

func TestConvertForceDeleteKey(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Source="hello.exe" />
          <RegistryKey Root="HKLM" Key="SOFTWARE\Example\Stale" ForceDeleteOnUninstall="yes" />
          <RemoveRegistryKey Root="HKLM" Key="SOFTWARE\Example\Stale" Action="removeOnUninstall" />
          <RegistryKey Root="HKLM" Key="SOFTWARE\Example\Hello" ForceDeleteOnUninstall="yes">
            <RegistryKey Key="Sub"><RegistryValue Name="v" Value="1" /></RegistryKey>
          </RegistryKey>
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"removeRegistry:\n", `key: SOFTWARE\Example\Stale`, "on: uninstall", "removeOnUninstall: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "type: key") != 1 || strings.Count(out, `SOFTWARE\Example\Stale`) != 1 {
		t.Errorf("want a key entry only for SOFTWARE\\Example\\Hello and one removal of Stale:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertTypeTextsAndURLLaunch(t *testing.T) {
	t.Parallel()

	src := `<?define AppName="Hello"?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="APPNAME" Value="$(var.AppName)" />
    <Property Id="SOFTWARE" Value="Hello" />
    <Property Id="EXT" Value="hlo" />
    <Property Id="CHANGING" Value="a" />
    <Property Id="PUB" Value="x" />
    <SetProperty Id="CHANGING" Value="b" Before="CostFinalize" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Source="hello.exe" />
          <ProgId Id="[ProductName].Pub" Description="[PUB] [%USERNAME] by [Manufacturer]">
            <Extension Id="hlopub"><Verb Id="open" Command="Open" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
          <ProgId Id="[SOFTWARE].Project" Description="[SOFTWARE] Project File">
            <Extension Id="[EXT]" ContentType="application/x-[APPNAME]"><Verb Id="open" Command="Open in [CHANGING]" TargetFile="hello.exe" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
    <Property Id="WIXUI_EXITDIALOGOPTIONALCHECKBOXTEXT" Value="Open the website" />
    <CustomAction Id="OpenSite" Directory="INSTALLFOLDER" ExeCommand="powershell Start-Process https://example.com" Return="asyncNoWait" />
    <UI>
      <ui:WixUI Id="WixUI_Minimal" />
      <Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="OpenSite" Condition="WIXUI_EXITDIALOGOPTIONALCHECKBOX = 1" />
      <Publish Dialog="ExitDialog" Control="Finish" Property="PUB" Value="z" />
    </UI>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"progId: Hello.Project\n", "description: Hello Project File\n", "extension: hlo\n",
		`Verb Command="Open in [CHANGING]" holds formatted text convert cannot fill in`,
		`the launch checkbox (action OpenSite: powershell Start-Process https://example.com) "Open the website" starts no file`,
		"progId: Hello.Pub\n", "contentType: application/x-${AppName}\n", `ProgId Description="[PUB] [%USERNAME] by Example" holds formatted text`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "launch:") || strings.Contains(out, "application/x-${AppName}\" holds") {
		t.Errorf("ui.launch without a file, or a TODO on a filled-in text:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertSharedProgIDAndExistenceSearch(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="TOOLDIR">
      <RegistrySearch Root="HKLM" Key="SOFTWARE\Example\Tool" Name="Path" Type="directory">
        <DirectorySearch />
      </RegistrySearch>
    </Property>
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Id="app" Source="hello.exe" /></Component>
        <Component Id="cmpA" Condition="ASSOC=&quot;1&quot;">
          <RegistryValue Root="HKLM" Key="SOFTWARE\Example\A" Value="1" KeyPath="yes" />
          <ProgId Id="Example.Shared" Description="Shared">
            <Extension Id="hla"><Verb Id="open" TargetFile="app" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
        <Component Id="cmpB" Condition="ASSOC=&quot;1&quot;">
          <RegistryValue Root="HKLM" Key="SOFTWARE\Example\B" Value="1" KeyPath="yes" />
          <ProgId Id="Example.Shared" Description="Shared">
            <Extension Id="hlb"><Verb Id="open" TargetFile="app" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="app" /><ComponentRef Id="cmpA" /><ComponentRef Id="cmpB" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"extension: hla\n", "extension: hlb\n", "the ProgId Example.Shared is declared again",
		"so the condition and features of the component that declared it again no longer apply",
		`was in a component with the condition ASSOC="1"`,
		"type: directory\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Count(out, "progId: Example.Shared") != 1 || strings.Contains(out, "directory:") ||
		strings.Contains(out, "DirectorySearch without Path") || strings.Count(out, "type: directory\n") != 1 {
		t.Errorf("want one Example.Shared file type and no directory search:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertProgIDOtherFileAndSearchTypes(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="TOOLFILE">
      <RegistrySearch Root="HKLM" Key="SOFTWARE\Example\Tool" Name="File" Type="file">
        <DirectorySearch Depth="2" />
      </RegistrySearch>
    </Property>
    <Property Id="TOOLDIR">
      <RegistrySearch Root="HKLM" Key="SOFTWARE\Example\Tool" Name="Dir" Type="raw">
        <DirectorySearch Depth="3"><FileSearch Name="tool.exe" /></DirectorySearch>
      </RegistrySearch>
    </Property>
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component>
          <File Id="app" Source="hello.exe" />
          <ProgId Id="Example.Doc" Description="Doc">
            <Extension Id="hld"><Verb Id="open" TargetFile="app" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
        <Component>
          <File Id="viewer" Source="viewer.exe" />
          <ProgId Id="Example.Doc" Description="Doc">
            <Extension Id="hlv"><Verb Id="open" TargetFile="viewer" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
        <Component Id="same1">
          <RegistryValue Root="HKLM" Key="SOFTWARE\Example\S1" Value="1" KeyPath="yes" />
          <ProgId Id="Example.Same" Description="Same">
            <Extension Id="hls"><Verb Id="open" TargetFile="app" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
        <Component Id="same2">
          <RegistryValue Root="HKLM" Key="SOFTWARE\Example\S2" Value="1" KeyPath="yes" />
          <ProgId Id="Example.Same" Description="Same">
            <Extension Id="hls"><MIME ContentType="text/plain" /><Verb Id="open" TargetFile="app" Argument="&quot;%1&quot;" /></Extension>
          </ProgId>
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello">
      <ComponentRef Id="app" /><ComponentRef Id="viewer" /><ComponentRef Id="same1" /><ComponentRef Id="same2" />
    </Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{
		"the ProgId Example.Doc is declared again for the file viewer",
		"the ProgId Example.Same is declared again",
		"<MIME> in an Extension has no counterpart",
		`the DirectorySearch without Path has Depth="3"`,
		"the DirectorySearch without Path below a raw RegistrySearch stands for the folder that holds the path in the value",
		"only checks that the folder the outer RegistrySearch finds exists",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "extension: hlv") || strings.Count(out, "extension: hls") != 1 ||
		strings.Count(out, "only checks that the folder") != 1 || strings.Contains(out, "    type: directory\n") ||
		strings.Contains(out, "condition and features of the component") || strings.Contains(out, `Depth="2"`) {
		t.Errorf("want hlv left out, one hls, one existence note and no directory type:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}
}

func TestConvertRawRegistryFolderSearch(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{` Type="raw"`, ""} {
		src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="OLDDIR">
      <RegistrySearch Root="HKLM" Key="SOFTWARE\Example\Old" Name="InstallLocation"` + typ + `>
        <DirectorySearch AssignToProperty="yes"><DirectorySearch /></DirectorySearch>
      </RegistrySearch>
    </Property>
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Id="app" Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="app" /></Feature>
  </Package>
</Wix>`

		res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
		if err != nil {
			t.Fatal(err)
		}

		out := string(res.YAML)
		if !strings.Contains(out, "below a raw RegistrySearch returns the folder that holds the path in the value") ||
			!strings.Contains(out, "if only that matters, the raw value is enough") ||
			strings.Contains(out, "    type: directory\n") || strings.Contains(out, "stands for the folder") {
			t.Errorf("type %q: want the raw value with the raw note:\n%s", typ, out)
		}

		cfg, err := config.Parse(res.YAML, config.Options{})
		if err != nil {
			t.Fatal(err)
		}

		if err := config.Validate(cfg).Err(); err != nil {
			t.Errorf("the converted file does not validate: %v\n%s", err, out)
		}
	}
}
