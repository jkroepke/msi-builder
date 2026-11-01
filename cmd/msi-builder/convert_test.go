package main

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
)

func TestConvert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wxs := filepath.Join(dir, "hello.wxs")
	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="$(var.Version)" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Component Directory="INSTALLFOLDER">
      <File Source="hello.exe" />
    </Component>
    <Merge Id="Runtime" SourceFile="runtime.msm" />
  </Package>
</Wix>
`

	if err := os.WriteFile(wxs, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	if code := run([]string{"convert", wxs}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert: exit %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "version: {{ .Version | quote }}") || !strings.Contains(stdout.String(), "- source: hello.exe") {
		t.Errorf("stdout %q", stdout.String())
	}

	if want := wxs + ":10: TODO: <Merge> is not supported"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}

	out := filepath.Join(dir, "msi.yaml")

	stdout.Reset()
	stderr.Reset()

	if code := run([]string{"convert", "-o", out, wxs}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("convert -o: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}

	if code := run([]string{"validate", "--var", "Version=1.0.0", out}, &stdout, &stderr); code != 0 {
		t.Errorf("validate: exit %d: %s", code, stderr.String())
	}

	stderr.Reset()

	if code := run([]string{"convert", "-o", out, wxs}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "--force") {
		t.Errorf("existing output: exit %d, stderr %q", code, stderr.String())
	}

	if code := run([]string{"convert", "--force", "-o", out, wxs}, &stdout, &stderr); code != 0 {
		t.Errorf("--force: exit %d, stderr %q", code, stderr.String())
	}

	stderr.Reset()

	if code := run([]string{"convert", filepath.Join(dir, "missing.wxs")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing input: exit %d, stderr %q", code, stderr.String())
	}

	if code := run([]string{"convert", "--force", wxs}, &stdout, &stderr); code != 2 {
		t.Errorf("--force without --output: exit %d", code)
	}

	if code := run([]string{"convert"}, &stdout, &stderr); code != 2 {
		t.Errorf("no argument: exit %d", code)
	}
}

// TestConvertKeepsBuiltCodes converts with --msi: a package with the
// component codes WiX generated stands in for WiX's build, one component
// found by its Id and one, without an Id, by its key file.
func TestConvertKeepsBuiltCodes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	built := `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
installDir: { id: INSTALLFOLDER, name: Hello }
components:
  - { id: Main, guid: 6d1c0a42-93b8-4e57-a2f1-0c7e5d9b3a61, files: [{ source: hello.exe }] }
  - { id: cmpNotes, guid: 2b8f4e17-5a0c-4d93-b6e2-9f1a3c7d5e08, files: [{ source: notes.txt }] }
  - id: Settings
    files: [{ source: settings.txt }]
    registry: [{ id: SettingsKey, root: HKLM, key: 'SOFTWARE\Example\Hello', name: Level, value: '1' }]
    keyPath: SettingsKey
  - id: regPath
    guid: 8c3e1f5a-7b2d-4e9c-a6f0-1d4b7e2c9a53
    registry: [{ id: regPath, root: HKLM, key: 'SOFTWARE\Example\Hello', name: Path, value: '[INSTALLFOLDER]' }]
`
	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Component Id="Main" Directory="INSTALLFOLDER">
      <File Source="hello.exe" />
    </Component>
    <Component Directory="INSTALLFOLDER">
      <File Source="notes.txt" />
    </Component>
    <Component Id="Readme" Directory="INSTALLFOLDER">
      <File Source="readme.txt" />
    </Component>
    <Component Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="Path" Value="[INSTALLFOLDER]" Type="string" />
    </Component>
  </Package>
</Wix>
`

	for name, data := range map[string]string{"msi.yaml": built, "app.wxs": src, "hello.exe": "MZ", "notes.txt": "notes", "settings.txt": "level: 1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer

	pkg := filepath.Join(dir, "hello-1.0.0.msi")
	if code := run([]string{"build", "-o", pkg, filepath.Join(dir, "msi.yaml")}, &stdout, &stderr); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	// A registry key path is no key file, even with a file in the component.
	read, err := builtPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}

	keyFiles := map[string]string{}
	for _, comp := range read.Components {
		keyFiles[comp.ID] = comp.KeyFile
	}

	if want := map[string]string{"Main": "hello.exe", "cmpNotes": "notes.txt", "Settings": "", "regPath": ""}; !maps.Equal(keyFiles, want) {
		t.Errorf("key files %v, want %v", keyFiles, want)
	}

	for _, comp := range read.Components {
		want := map[string]wixconvert.BuiltRegistry{
			"Settings": {Root: 2, Key: `SOFTWARE\Example\Hello`, Name: "Level"},
			"regPath":  {Root: 2, Key: `SOFTWARE\Example\Hello`, Name: "Path"},
		}[comp.ID]
		if got := comp.KeyRegistry; (got == nil) != (want.Key == "") || got != nil && *got != want {
			t.Errorf("%s: key registry %+v, want %+v", comp.ID, got, want)
		}
	}

	stdout.Reset()
	stderr.Reset()

	wxs := filepath.Join(dir, "app.wxs")
	if code := run([]string{"convert", "--msi", pkg, wxs}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert --msi: exit %d: %s", code, stderr.String())
	}

	for _, want := range []string{
		"guid: '{6D1C0A42-93B8-4E57-A2F1-0C7E5D9B3A61}'",
		"guid: '{2B8F4E17-5A0C-4D93-B6E2-9F1A3C7D5E08}'",
		"guid: '{8C3E1F5A-7B2D-4E9C-A6F0-1D4B7E2C9A53}'",
		"The guid of 3 components whose Guid WiX generated comes from hello-1.0.0.msi.",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}

	if want := wxs + ":13: TODO: hello-1.0.0.msi has no matching component"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}

	stdout.Reset()
	stderr.Reset()

	// Without --msi, convert computes the codes as WiX does for the --arch
	// given, so the late removal needs no note.
	if code := run([]string{"convert", "--arch", "x64", wxs}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert: exit %d: %s", code, stderr.String())
	}

	if want := "convert computed the guids of 4 components"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
	}

	if strings.Contains(stderr.String(), "WiX generated the Guid") {
		t.Errorf("stderr names a component without a code: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := run([]string{"convert", "--arch", "ia64", wxs}, &stdout, &stderr); code != 1 {
		t.Errorf("--arch ia64: exit %d", code)
	}

	if code := run([]string{"convert", "--msi", filepath.Join(dir, "notes.txt"), wxs}, &stdout, &stderr); code != 1 {
		t.Errorf("--msi with a file that is no package: exit %d", code)
	}
}

// TestConvertKeepsHarvestedCodes converts a Files glob with --msi: a package
// with one component and code per file, below a subfolder too, stands in
// for WiX's harvest. The folder source keeps those codes in guids, and the
// package built from the converted file has them. A second glob into the
// same folder matches the same paths; the file on disk picks the glob.
func TestConvertKeepsHarvestedCodes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	built := `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
installDir: { id: INSTALLFOLDER, name: Hello }
directories:
  - { id: ProgramFiles64Folder, children: [{ id: INSTALLFOLDER, name: Hello, children: [{ id: dirDocs, name: docs }] }] }
components:
  - { id: filApp, guid: 6d1c0a42-93b8-4e57-a2f1-0c7e5d9b3a61, files: [{ source: dist/hello.exe }] }
  - { id: filGuide, directory: dirDocs, guid: 2b8f4e17-5a0c-4d93-b6e2-9f1a3c7d5e08, files: [{ source: dist/docs/guide.md }] }
`
	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Files Include="dist\**" Directory="INSTALLFOLDER" />
    <Files Include="extra\**" Directory="INSTALLFOLDER" />
  </Package>
</Wix>
`

	for _, sub := range []string{"dist/docs", "extra"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	for name, data := range map[string]string{
		"v1.yaml": built, "app.wxs": src, "dist/hello.exe": "MZ", "dist/docs/guide.md": "# Guide", "dist/news.md": "new in 1.1.0",
		"extra/more.txt": "more",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer

	pkg := filepath.Join(dir, "hello-1.0.0.msi")
	if code := run([]string{"build", "-o", pkg, filepath.Join(dir, "v1.yaml")}, &stdout, &stderr); code != 0 {
		t.Fatalf("build: exit %d: %s", code, stderr.String())
	}

	converted := filepath.Join(dir, "msi.yaml")
	if code := run([]string{"convert", "--msi", pkg, "-o", converted, filepath.Join(dir, "app.wxs")}, &stdout, &stderr); code != 0 {
		t.Fatalf("convert --msi: exit %d: %s", code, stderr.String())
	}

	yaml, err := os.ReadFile(converted)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"docs/guide.md: '{2B8F4E17-5A0C-4D93-B6E2-9F1A3C7D5E08}'",
		"hello.exe: '{6D1C0A42-93B8-4E57-A2F1-0C7E5D9B3A61}'",
		"The guids of 2 files that WiX harvested with Files come from hello-1.0.0.msi.",
	} {
		if !strings.Contains(string(yaml), want) {
			t.Errorf("msi.yaml lacks %q:\n%s", want, yaml)
		}
	}

	// Both globs match every file; the disk tells which one holds it.
	if i := strings.Index(string(yaml), "source: extra/"); i < 0 || strings.Index(string(yaml), "hello.exe:") > i {
		t.Errorf("the codes are not on dist/:\n%s", yaml)
	}

	// v1 has no file of extra/, which line 8 adds, so only that glob gets
	// a note.
	if strings.Contains(stderr.String(), "app.wxs:7:") || !strings.Contains(stderr.String(), "app.wxs:8: TODO: hello-1.0.0.msi has no component") {
		t.Errorf("convert notes: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	newer := filepath.Join(dir, "hello-1.1.0.msi")
	if code := run([]string{"build", "-o", newer, converted}, &stdout, &stderr); code != 0 {
		t.Fatalf("build the converted file: exit %d: %s", code, stderr.String())
	}

	if code := run([]string{"inspect", "--table", "Component", newer}, &stdout, &stderr); code != 0 {
		t.Fatalf("inspect: exit %d: %s", code, stderr.String())
	}

	for _, want := range []string{"{6D1C0A42-93B8-4E57-A2F1-0C7E5D9B3A61}", "{2B8F4E17-5A0C-4D93-B6E2-9F1A3C7D5E08}"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the Component table lacks %s:\n%s", want, stdout.String())
		}
	}
}

func TestTargetName(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{
		"docs":                       "docs",
		"LONGNA~1|Long Name":         "Long Name",
		"SHORT~1|Long:SRC~1|SrcLong": "Long",
		"bin:src":                    "bin",
		".":                          ".",
		".:SourceDir":                ".",
	} {
		if got := targetName(value); got != want {
			t.Errorf("targetName(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestExistsBeside(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "dist", "app.exe"), []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}

	exists := existsBeside(dir)

	for rel, want := range map[string]bool{
		"dist/app.exe": true,
		"dist/no.exe":  false,
		filepath.ToSlash(filepath.Join(dir, "dist", "app.exe")): true,
	} {
		if got := exists(rel); got != want {
			t.Errorf("exists(%q) = %t, want %t", rel, got, want)
		}
	}
}
