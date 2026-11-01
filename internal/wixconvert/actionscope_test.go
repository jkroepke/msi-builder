package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestConvertElevatedActionScopes checks the scope convert gives actions
// without impersonation in a dual package (CA-09): none for an immediate
// action, which never runs elevated, none but a note for one that also
// runs outside the execute sequence, where a scope is refused, and
// perMachine with a note for one whose condition tests ALLUSERS itself.
func TestConvertElevatedActionScopes(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" Scope="perUserOrMachine">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <CustomAction Id="Immediate" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]a.exe&quot;" Impersonate="no" Return="ignore" />
    <CustomAction Id="AdminToo" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]a.exe&quot;" Execute="deferred" Impersonate="no" />
    <CustomAction Id="AdminOnly" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]a.exe&quot;" Execute="deferred" Impersonate="no" />
    <CustomAction Id="UserOnly" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]a.exe&quot;" Execute="deferred" Impersonate="no" />
    <InstallExecuteSequence>
      <Custom Action="Immediate" After="InstallFiles" Condition="NOT Installed" />
      <Custom Action="UserOnly" After="InstallFiles" Condition="NOT ALLUSERS AND NOT Installed" />
      <Custom Action="AdminToo" After="InstallFiles" />
    </InstallExecuteSequence>
    <AdminExecuteSequence>
      <Custom Action="AdminToo" After="InstallFiles" />
      <Custom Action="AdminOnly" After="InstallFiles" />
    </AdminExecuteSequence>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	yaml := string(res.YAML)
	if strings.Count(yaml, "    scope: perMachine\n") != 1 {
		t.Errorf("want scope: perMachine for UserOnly only:\n%s", yaml)
	}

	if !strings.Contains(yaml, "a condition of UserOnly tests ALLUSERS itself") {
		t.Errorf("no note for the ALLUSERS test in UserOnly's condition:\n%s", yaml)
	}

	if !strings.Contains(yaml, "AdminToo runs without impersonation outside the execute sequence") {
		t.Errorf("no note for AdminToo:\n%s", yaml)
	}

	if strings.Count(yaml, "move the other placements") != 1 || !strings.Contains(yaml, "it cannot run without impersonation in this package") {
		t.Errorf("want the move note for AdminToo only and the cannot-run note for AdminOnly:\n%s", yaml)
	}

	if !strings.Contains(yaml, "condition: NOT Installed") || strings.Count(yaml, "impersonate: false") != 3 {
		t.Errorf("want the immediate action with its condition and without impersonate: false:\n%s", yaml)
	}
}

// TestConvertActionSystemFolder checks that a run action of a per-user
// package loses a working directory that ICE105 rejects, with a note, so
// that the converted file validates, and that an exec action keeps it with
// a note.
func TestConvertActionSystemFolder(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" Scope="perUser">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="LocalAppDataFolder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main" Title="Hello"><ComponentRef Id="hello.exe" /></Feature>
    <CustomAction Id="Ver" Directory="SystemFolder" ExeCommand="&quot;[SystemFolder]cmd.exe&quot; /c ver" Execute="deferred" Return="check" />
    <InstallExecuteSequence>
      <Custom Action="Ver" After="InstallFiles" Condition="NOT Installed" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out := string(res.YAML)
	if strings.Contains(out, "directory: SystemFolder") || !strings.Contains(out, "Ver ran in SystemFolder") {
		t.Errorf("want the run action without its directory and with a note:\n%s", out)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, out)
	}

	elevated := strings.Replace(src, `Execute="deferred"`, `Execute="deferred" Impersonate="no"`, 1)
	for name, doc := range map[string]string{
		"per-user elevated": elevated,
		"dual":              strings.Replace(elevated, `Scope="perUser"`, `Scope="perUserOrMachine"`, 1),
	} {
		res, err := wixconvert.Convert(strings.NewReader(doc), wixconvert.Options{})
		if err != nil {
			t.Fatal(err)
		}

		cfg, err := config.Parse(res.YAML, config.Options{})
		if err != nil {
			t.Fatal(err)
		}

		if err := config.Validate(cfg).Err(); err != nil || strings.Contains(string(res.YAML), "directory: SystemFolder") {
			t.Errorf("%s: want a valid file without the directory: %v\n%s", name, err, res.YAML)
		}
	}

	async := strings.Replace(src, `Return="check"`, `Return="asyncNoWait"`, 1)

	res, err = wixconvert.Convert(strings.NewReader(async), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if out := string(res.YAML); !strings.Contains(out, "directory: SystemFolder") || !strings.Contains(out, "Ver runs in SystemFolder") {
		t.Errorf("want the exec action to keep its directory with a note:\n%s", out)
	}
}
