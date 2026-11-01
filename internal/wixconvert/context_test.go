package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// convertValid converts src, requires the result to parse and validate,
// and returns its YAML.
func convertValid(t *testing.T, src string) string {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, res.YAML)
	}

	if ds := config.Validate(cfg); ds.Err() != nil {
		t.Fatalf("validate: %v\n%s", ds, res.YAML)
	}

	return string(res.YAML)
}

// convertVars is convertValid with the variables of the result set.
func convertVars(t *testing.T, src string) string {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	vars := map[string]string{}
	for _, name := range res.Vars {
		vars[name] = "1.0.0"
	}

	cfg, err := config.Parse(res.YAML, config.Options{Vars: vars})
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, res.YAML)
	}

	if ds := config.Validate(cfg); ds.Err() != nil {
		t.Fatalf("validate: %v\n%s", ds, res.YAML)
	}

	return string(res.YAML)
}

// TestConvertPerUserPackageForMachine checks a per-user WiX v3 package
// that a bootstrapper installs per machine with ALLUSERS=1 and whose
// components test ALLUSERS: it becomes perUserOrMachine, the conditions
// become components[].scope, and one component appends two parts to PATH
// (INST-07, ENVVAR-04).
func TestConvertPerUserPackageForMachine(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="200" Compressed="yes" InstallScope="perUser" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="LocalAppDataFolder">
        <Directory Id="INSTALLFOLDER" Name="Hello" />
      </Directory>
    </Directory>
    <Feature Id="Main">
      <Component Id="PathUser" Directory="INSTALLFOLDER" Guid="*">
        <Condition>NOT ALLUSERS=1</Condition>
        <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="PathUser" Value="1" Type="string" KeyPath="yes" />
        <Environment Id="UserDir" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]" />
        <Environment Id="UserBin" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]bin" />
      </Component>
      <Component Id="PathMachine" Directory="INSTALLFOLDER" Guid="*">
        <Condition>ALLUSERS = 1</Condition>
        <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="PathMachine" Value="1" Type="string" KeyPath="yes" />
        <Environment Id="MachineDir" Action="set" Name="PATH" Part="first" Value="[INSTALLFOLDER]" System="yes" />
        <Environment Id="MachineBin" Action="set" Name="PATH" Part="first" Value="[INSTALLFOLDER]bin" System="yes" />
      </Component>
    </Feature>
  </Product>
</Wix>`

	yaml := convertValid(t, src)

	for _, want := range []string{
		"  scope: perUserOrMachine\n", `installerVersion: "5.0"`, "    scope: perUser\n", "    scope: perMachine\n",
		"the per-user package chooses its install context itself",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("no %q in:\n%s", want, yaml)
		}
	}

	if strings.Contains(yaml, "condition: ") {
		t.Errorf("a context condition was kept:\n%s", yaml)
	}
}

// TestConvertContextSetters checks a WiX v3 package without a scope that
// sets ALLUSERS and MSIINSTALLPERUSER with SetProperty and conditions its
// components and its firewall rule on MSIINSTALLPERUSER: it is per user,
// so it becomes perUserOrMachine, the setters are left out with a note, the conditions
// become components[].scope and the firewall rule stays on its perMachine
// component (INST-07, INST-08). A SetProperty keeps the condition WiX v3
// writes as its text.
func TestConvertContextSetters(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi" xmlns:fire="http://schemas.microsoft.com/wix/FirewallExtension">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <SetProperty Id="ALLUSERS" Sequence="both" After="ValidateProductID" Value="1">NOT PICKED AND NOT Installed</SetProperty>
    <SetProperty Id="MSIINSTALLPERUSER" Sequence="first" After="ValidateProductID" Value="0">NOT PICKED</SetProperty>
    <SetProperty Id="GREETING" After="SetALLUSERS" Value="hi">NOT Installed</SetProperty>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*">
            <File Id="hello.exe" Source="hello.exe" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Component Id="PathUser" Directory="INSTALLFOLDER" Guid="*">
      <Condition>MSIINSTALLPERUSER = 1</Condition>
      <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="PathUser" Value="1" Type="string" KeyPath="yes" />
      <Environment Id="UserDir" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]" />
    </Component>
    <Component Id="AdminFirewall" Directory="INSTALLFOLDER" Guid="B03D3C75-F835-47A9-8224-1D1238B4F74F" KeyPath="yes">
      <Condition><![CDATA[MSIINSTALLPERUSER <> 1]]></Condition>
      <fire:FirewallException Id="Rule" Name="Hello rule" Program="[#hello.exe]" Profile="private" Scope="any" />
    </Component>
    <Feature Id="Main">
      <ComponentRef Id="App" />
      <ComponentRef Id="PathUser" />
      <ComponentRef Id="AdminFirewall" />
    </Feature>
  </Product>
</Wix>`

	yaml := convertValid(t, src)

	for _, want := range []string{
		"  scope: perUserOrMachine\n", "    scope: perUser\n", "    scope: perMachine\n", "component: AdminFirewall",
		"the action SetALLUSERS, which sets ALLUSERS, was left out",
		"the action SetMSIINSTALLPERUSER, which sets MSIINSTALLPERUSER, was left out",
		"setProperty: {name: GREETING, value: hi}", "after: ValidateProductID, condition: NOT Installed",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("no %q in:\n%s", want, yaml)
		}
	}

	if strings.Contains(yaml, "name: ALLUSERS") || strings.Contains(yaml, "name: MSIINSTALLPERUSER") || strings.Contains(yaml, "MSIINSTALLPERUSER <>") {
		t.Errorf("a context setter or condition was kept:\n%s", yaml)
	}
}

// TestConvertMachineSetter checks a WiX v3 package without a scope that
// always sets ALLUSERS=1, the WiX v3 way to install per machine: it stays
// per machine, also when a component tests the context, the setter is left out with a note, and its HKLM data
// validates.
func TestConvertMachineSetter(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="200" Compressed="yes" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <SetProperty Id="ALLUSERS" Value="1" Before="FindRelatedProducts" Sequence="first" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="Machine" Guid="*">
            <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="M" Value="1" Type="string" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="Machine" /></Feature>
  </Product>
</Wix>`

	anchored := func(setter, other string) string {
		return strings.Replace(src, `<SetProperty Id="ALLUSERS" Value="1" Before="FindRelatedProducts" Sequence="first" />`, setter+other, 1)
	}

	for name, doc := range map[string]string{
		"plain": src,
		// Actions placed on the dropped setter move to its place.
		"anchored custom action": anchored(`<CustomAction Id="SetAllUsers" Property="ALLUSERS" Value="1" />`,
			`<CustomAction Id="SetOther" Property="FOO" Value="x" />
    <InstallExecuteSequence><Custom Action="SetAllUsers" Before="FindRelatedProducts" /><Custom Action="SetOther" After="SetAllUsers" /></InstallExecuteSequence>`),
		// The usual WiX v3 form runs on every first install.
		"not installed": anchored(`<CustomAction Id="SetAU" Property="ALLUSERS" Value="1" />`,
			`<InstallUISequence><Custom Action="SetAU" Before="FindRelatedProducts">NOT Installed</Custom></InstallUISequence>
    <InstallExecuteSequence><Custom Action="SetAU" Before="FindRelatedProducts">NOT Installed</Custom></InstallExecuteSequence>`),
		"not installed set property": anchored(`<SetProperty Id="ALLUSERS" Value="1" Before="FindRelatedProducts">NOT Installed</SetProperty>`, ""),
		"anchored set property": anchored(`<SetProperty Id="ALLUSERS" Value="1" Before="FindRelatedProducts" />`,
			`<SetProperty Id="FOO" Value="x" After="SetALLUSERS" />`),
		// A condition on the context always holds per machine.
		"conditioned": strings.Replace(src, `<Component Id="Machine" Guid="*">`,
			`<Component Id="Machine" Guid="*"><Condition>ALLUSERS</Condition>`, 1),
	} {
		yaml := convertValid(t, doc)

		if strings.Contains(yaml, "scope:") || strings.Contains(yaml, "name: ALLUSERS") ||
			!strings.Contains(yaml, "which always sets ALLUSERS=1, was left out") {
			t.Errorf("%s: want a per-machine package without the setter:\n%s", name, yaml)
		}

		if name == "conditioned" && !strings.Contains(yaml, "condition: ALLUSERS") {
			t.Errorf("%s: the condition was lost:\n%s", name, yaml)
		}
	}
}

// TestConvertChosenMachineSetter checks ALLUSERS=1 setters that do not run
// in every install, or that a per-user package has: the install chooses
// its context, so the package becomes perUserOrMachine and drops them.
func TestConvertChosenMachineSetter(t *testing.T) {
	t.Parallel()

	src := func(pkg, setter string) string {
		return `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" ` + pkg + ` />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    ` + setter + `
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="LocalAppDataFolder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="User" Guid="*"><Condition>NOT ALLUSERS</Condition>
            <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="U" Value="1" Type="string" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="User" /></Feature>
  </Product>
</Wix>`
	}

	for name, doc := range map[string]string{
		"ui sequence": src("", `<SetProperty Id="ALLUSERS" Value="1" Before="FindRelatedProducts" Sequence="ui" />`),
		"dialog button": src("", `<CustomAction Id="SetAU" Property="ALLUSERS" Value="1" />
    <UI><Dialog Id="ScopeDlg" Width="370" Height="270" Title="Scope">
      <Control Id="All" Type="PushButton" X="10" Y="10" Width="56" Height="17" Text="All users"><Publish Event="DoAction" Value="SetAU">1</Publish></Control>
    </Dialog></UI>`),
		"ui only custom": src("", `<CustomAction Id="SetAU" Property="ALLUSERS" Value="1" />
    <InstallUISequence><Custom Action="SetAU" Before="FindRelatedProducts" /></InstallUISequence>`),
		// Without a context condition, the setter alone chooses the context.
		"per-user package": strings.Replace(src(`InstallScope="perUser"`,
			`<SetProperty Id="ALLUSERS" Value="1" Before="CostInitialize" Sequence="execute" />`), "<Condition>NOT ALLUSERS</Condition>", "", 1),
	} {
		yaml := convertValid(t, doc)

		if !strings.Contains(yaml, "  scope: perUserOrMachine\n") ||
			strings.Contains(yaml, "always sets ALLUSERS=1") || strings.Contains(yaml, "name: ALLUSERS") {
			t.Errorf("%s: want a perUserOrMachine package without the setter:\n%s", name, yaml)
		}
	}
}

// TestConvertMachineContent checks a WiX v3 package without a scope whose
// components hold machine data or folders: a bootstrapper installs it per
// machine, so it stays perMachine with its conditions when the dual
// package would refuse that data.
func TestConvertMachineContent(t *testing.T) {
	t.Parallel()

	for name, data := range map[string]string{
		"hklm":            `<RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="S" Value="1" Type="string" KeyPath="yes" />`,
		"service":         `<File Id="svc.exe" Source="svc.exe" KeyPath="yes" /><ServiceInstall Id="Svc" Name="HelloSvc" Type="ownProcess" Start="auto" ErrorControl="normal" />`,
		"common app data": `<File Id="data.txt" Source="data.txt" KeyPath="yes" />`,
		"variable":        `<RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="S" Value="1" Type="string" KeyPath="yes" />`,
	} {
		folder := "ProgramFiles64Folder"
		if name == "common app data" {
			folder = "CommonAppDataFolder"
		}

		src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="200" Compressed="yes" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="` + folder + `">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="Svc" Guid="*">` + data + `</Component>
          <Component Id="M" Guid="*"><Condition>ALLUSERS=1</Condition>
            <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="M" Value="1" Type="string" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="Svc" /><ComponentRef Id="M" /></Feature>
  </Product>
</Wix>`

		if name == "variable" {
			src = strings.Replace(src, `Version="1.0.0"`, `Version="$(var.ProductVersion)"`, 1)
		}

		yaml := convertVars(t, src)
		if strings.Contains(yaml, "scope: perUserOrMachine") || !strings.Contains(yaml, "condition: ALLUSERS=1") {
			t.Errorf("%s: want a perMachine package with its condition:\n%s", name, yaml)
		}
	}
}

// TestConvertContextConditions checks the context conditions in other
// forms: an explicit per-machine package keeps its conditions and its
// scope, since ALLUSERS=1 always holds there; NOT in another case and
// spaces still name the context; and a dual package drops its own setters.
func TestConvertContextConditions(t *testing.T) {
	t.Parallel()

	component := func(id, cond string) string {
		return `<Component Id="` + id + `" Directory="INSTALLFOLDER" Guid="*"><Condition>` + cond + `</Condition>` +
			`<RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="` + id + `" Value="1" Type="string" KeyPath="yes" /></Component>`
	}
	src := func(pkg, extra string, components ...string) string {
		return `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" ` + pkg + ` />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    ` + extra + `
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></Directory>
    </Directory>
    <Feature Id="Main">` + strings.Join(components, "") + `</Feature>
  </Product>
</Wix>`
	}

	machine := convertValid(t, src(`InstallScope="perMachine"`, "", component("Always", "ALLUSERS"),
		`<Component Id="Machine" Directory="INSTALLFOLDER" Guid="*"><RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="M" Value="1" Type="string" KeyPath="yes" /></Component>`))
	if strings.Contains(machine, "scope:") || !strings.Contains(machine, "condition: ALLUSERS") {
		t.Errorf("the per-machine package changed:\n%s", machine)
	}

	forms := convertValid(t, src(`InstallScope="perUser"`, "", component("A", "Not ALLUSERS"), component("B", "not MSIINSTALLPERUSER"),
		component("C", "ALLUSERS &lt;&gt; 1"), component("D", "MSIINSTALLPERUSER  =  1")))
	if strings.Count(forms, "    scope: perUser\n") != 3 || strings.Count(forms, "    scope: perMachine\n") != 1 || strings.Contains(forms, "condition:") {
		t.Errorf("want A, C and D per user and B per machine, without conditions:\n%s", forms)
	}

	dual := convertValid(t, src(``, `<Property Id="ALLUSERS" Value="2" /><Property Id="MSIINSTALLPERUSER" Value="1" />
    <CustomAction Id="ForceMachine" Property="ALLUSERS" Value="1" />
    <InstallExecuteSequence><Custom Action="ForceMachine" After="ValidateProductID">NOT Installed</Custom></InstallExecuteSequence>`,
		component("A", "NOT ALLUSERS")))
	if !strings.Contains(dual, "  scope: perUserOrMachine\n") || !strings.Contains(dual, "the action ForceMachine, which sets ALLUSERS, was left out") ||
		strings.Contains(dual, "id: ForceMachine") {
		t.Errorf("want the dual package without its setter:\n%s", dual)
	}
}
