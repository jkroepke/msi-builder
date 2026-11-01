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
        <Environment Id="PathA" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]" />
        <Environment Id="PathB" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]bin" />
      </Component>
      <Component Id="PathMachine" Directory="INSTALLFOLDER" Guid="*">
        <Condition>ALLUSERS = 1</Condition>
        <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="PathMachine" Value="1" Type="string" KeyPath="yes" />
        <Environment Id="PathC" Action="set" Name="PATH" Part="first" Value="[INSTALLFOLDER]" System="yes" />
        <Environment Id="PathD" Action="set" Name="PATH" Part="first" Value="[INSTALLFOLDER]bin" System="yes" />
      </Component>
    </Feature>
  </Product>
</Wix>`

	yaml := convertValid(t, src)

	for _, want := range []string{
		"  scope: perUserOrMachine\n", `installerVersion: "5.0"`, "    scope: perUser\n", "    scope: perMachine\n",
		"the package chooses its install context itself",
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
// components and its firewall rule on MSIINSTALLPERUSER: it becomes
// perMachineOrUser, the setters are left out with a note, the conditions
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
    <SetProperty Id="GREETING" After="CostFinalize" Value="hi">NOT Installed</SetProperty>
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
      <Environment Id="PathA" Action="set" Name="PATH" Part="last" Value="[INSTALLFOLDER]" />
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
		"  scope: perMachineOrUser\n", "    scope: perUser\n", "    scope: perMachine\n", "component: AdminFirewall",
		"the action SetALLUSERS, which sets ALLUSERS, was left out",
		"the action SetMSIINSTALLPERUSER, which sets MSIINSTALLPERUSER, was left out",
		"setProperty: {name: GREETING, value: hi}", "condition: NOT Installed",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("no %q in:\n%s", want, yaml)
		}
	}

	if strings.Contains(yaml, "name: ALLUSERS") || strings.Contains(yaml, "name: MSIINSTALLPERUSER") || strings.Contains(yaml, "MSIINSTALLPERUSER <>") {
		t.Errorf("a context setter or condition was kept:\n%s", yaml)
	}
}
