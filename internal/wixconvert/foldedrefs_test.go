package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestConvertFoldedConditions checks that a bare condition on a nameless
// Directory that became a standard folder names that folder: the
// Directory's own property no longer exists, so Condition="VOLROOT" would
// skip the action WiX's package runs. Strings and template actions keep
// the name.
func TestConvertFoldedConditions(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <SetDirectory Id="VOLROOT" Value="[WindowsVolume]" />
    <Directory Id="VOLROOT">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component Id="App"><File Source="hello.exe" /></Component>
      </Directory>
    </Directory>
    <SetProperty Id="SEEN" Value="1" After="CostFinalize" Condition="VOLROOT AND VOLROOT&lt;&gt;&quot;VOLROOT&quot; AND $(var.Gate)" />
    <Launch Condition="Installed OR VOLROOT" Message="no volume" />
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	if want := "Installed OR WindowsVolume"; !strings.Contains(out, want) {
		t.Errorf("the launch condition lacks %q:\n%s", want, out)
	}

	if want := "`WindowsVolume AND WindowsVolume<>\"VOLROOT\" AND ` .Gate"; !strings.Contains(out, want) {
		t.Errorf("the YAML lacks the condition %q:\n%s", want, out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{Vars: map[string]string{"Gate": "1"}})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted YAML does not validate: %v\n%s", err, out)
	}
}

// TestConvertRemoveFileVariableCase checks that two RemoveFile patterns
// that differ only in the case of their variables both stay: the
// variables hold two values, such as a.tmp and b.tmp.
func TestConvertRemoveFileVariableCase(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component Id="App">
          <File Source="hello.exe" />
          <RemoveFile Id="CleanA" Name="$(var.CleanMask)" On="uninstall" />
          <RemoveFile Id="CleanB" Name="$(var.CLEANMASK)" On="uninstall" />
          <RemoveFile Id="CleanC" Name="X$(var.CleanMask)" On="uninstall" />
          <RemoveFile Id="CleanD" Name="x$(var.CleanMask)" On="uninstall" />
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	for _, want := range []string{".CleanMask", ".CLEANMASK"} {
		if !strings.Contains(out, want) {
			t.Errorf("the YAML lacks the pattern with %s:\n%s", want, out)
		}
	}

	// X and x are the same pattern to the engine.
	if got := strings.Count(out, "- name: "); got != 3 {
		t.Errorf("%d removeFiles rows, want 3 (CleanD equals CleanC):\n%s", got, out)
	}
}

// TestConvertBuiltCodesFoldedAnonymous checks that --msi finds the code of
// a component in a Directory without Id or name of its own, which became
// the standard folder: the built package has a generated row named "."
// below that folder, and the folder below it has its parent there.
func TestConvertBuiltCodesFoldedAnonymous(t *testing.T) {
	t.Parallel()

	const src = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.1.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Name=".">
        <Component><File Source="hello-world.txt" /></Component>
        <Directory Name="Hello">
          <Component><File Source="hello-data.txt" /></Component>
        </Directory>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main"><ComponentRef Id="hello_world.txt" /></Feature>
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "cmpA", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "dirAnon", KeyFile: "hello-world.txt"},
			{ID: "cmpB", Code: "{00000000-0000-4000-8000-000000000002}", Directory: "dirHello", KeyFile: "hello-data.txt"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles"},
			{ID: "dirAnon", Parent: "ProgramFiles64Folder", Name: "."},
			{ID: "dirHello", Parent: "dirAnon", Name: "Hello"},
		},
	}

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{Built: built})
	if err != nil {
		t.Fatal(err)
	}

	for _, code := range []string{"{00000000-0000-4000-8000-000000000001}", "{00000000-0000-4000-8000-000000000002}"} {
		if !strings.Contains(string(res.YAML), code) {
			t.Errorf("the YAML lacks the code %s:\n%s", code, res.YAML)
		}
	}
}
