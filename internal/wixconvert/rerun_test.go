package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// The tests in this file cover convert problems found in the second
// real-world rerun: each converted file builds, or a todo says why not.

// validConfig fails the test when msi-builder refuses a converted file.
func validConfig(t *testing.T, yaml []byte) {
	t.Helper()

	cfg, err := config.Parse(yaml, config.Options{})
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, yaml)
	}

	if ds := config.Validate(cfg); ds.Err() != nil {
		t.Fatalf("validate: %v\n%s", ds, yaml)
	}
}

// convertWith converts src with opts and checks that the result builds.
func convertWith(t *testing.T, src string, opts wixconvert.Options) string {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(src), opts)
	if err != nil {
		t.Fatal(err)
	}

	validConfig(t, res.YAML)

	return string(res.YAML)
}

func wantAll(t *testing.T, yaml string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(yaml, want) {
			t.Errorf("no %q in:\n%s", want, yaml)
		}
	}
}

func wantNone(t *testing.T, yaml string, unwanted ...string) {
	t.Helper()

	for _, bad := range unwanted {
		if strings.Contains(yaml, bad) {
			t.Errorf("%q in:\n%s", bad, yaml)
		}
	}
}

// TestConvertPermissionCondition keeps the condition of a PermissionEx,
// from a WiX v3 Condition child or a v4 attribute, and notes a second
// permission element of the same block with another condition.
func TestConvertPermissionCondition(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <CreateFolder>
              <PermissionEx Sddl="D:PAI(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FRFX;;;BU)">
                <Condition>NOT (ALLUSERS &lt;&gt; 1 AND MSIINSTALLPERUSER = 1)</Condition>
              </PermissionEx>
            </CreateFolder>
          </Component>
          <Directory Id="DATA" Name="data">
            <Component Id="Data" Guid="*" Win64="yes">
              <CreateFolder>
                <PermissionEx Sddl="D:PAI(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"><Condition>LOCKED</Condition></PermissionEx>
                <PermissionEx Sddl="D:PAI(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FA;;;BU)" />
              </CreateFolder>
            </Component>
          </Directory>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="Data" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml,
		"      condition: NOT (ALLUSERS <> 1 AND MSIINSTALLPERUSER = 1)\n",
		"      condition: LOCKED\n",
		`this permission element applies under "", but folderPermissions applies under "LOCKED"`,
	)
}

// TestConvertContextConditionParts reads a component condition that
// combines context conditions, as ALLUSERS=2 packages write them: the
// per-machine one becomes components[].scope perMachine, so the machine
// PATH entry builds.
func TestConvertContextConditionParts(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="ALLUSERS" Value="2" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
          <Component Id="PathUser" Guid="3f2504e0-4f89-41d3-9a0c-0305e82c3302" Win64="yes">
            <Condition>ALLUSERS &lt;&gt; 1 and MSIINSTALLPERUSER = 1</Condition>
            <CreateFolder />
            <Environment Id="PathUser" Action="set" Name="PATH" Part="last" System="no" Value="[INSTALLFOLDER]" />
          </Component>
          <Component Id="PathMachine" Guid="3f2504e0-4f89-41d3-9a0c-0305e82c3303" Win64="yes">
            <Condition>NOT (ALLUSERS &lt;&gt; 1 AND MSIINSTALLPERUSER = 1)</Condition>
            <CreateFolder />
            <Environment Id="PathMachine" Action="set" Name="PATH" Part="last" System="yes" Value="[INSTALLFOLDER]" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="PathUser" /><ComponentRef Id="PathMachine" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "    scope: perUser\n", "    scope: perMachine\n")
	wantNone(t, yaml, "condition: ")
}

// TestConvertPlatformFolderReferences maps a reference to a WiX 6432
// folder, the working directory of a run action, to the Program Files
// folder of the platform.
func TestConvertPlatformFolderReferences(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <CustomAction Id="Cleanup" Directory="ProgramFiles6432Folder" ExeCommand="reg.exe delete &quot;HKCU\Software\Example&quot; /f" Execute="deferred" Return="ignore" />
    <InstallExecuteSequence>
      <Custom Action="Cleanup" Before="InstallFinalize" Condition="REMOVE~=&quot;ALL&quot;" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	for arch, want := range map[string]string{"x64": "directory: ProgramFiles64Folder\n", "x86": "directory: ProgramFilesFolder\n"} {
		yaml := convertWith(t, src, wixconvert.Options{Arch: arch})
		wantAll(t, yaml, want)
		wantNone(t, yaml, "6432")
	}
}

// TestConvertAnchorOnDroppedAction moves a setter anchored on an action
// convert leaves out, a script or one the file does not define, to that
// action's place, so that the file builds.
func TestConvertAnchorOnDroppedAction(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <Binary Id="FixAcl" SourceFile="fixacl.js" />
    <SetProperty Id="FixAcl" Before="FixAcl" Value="[INSTALLDIR]" Sequence="execute" />
    <CustomAction Id="FixAcl" BinaryKey="FixAcl" JScriptCall="FixAcl" Return="check" Execute="deferred" Impersonate="no" />
    <SetProperty Id="HelperCleanup" Before="HelperCleanup" Value="&quot;Hello&quot;" Sequence="execute" />
    <InstallExecuteSequence>
      <Custom Action="FixAcl" After="InstallFiles">NOT Installed</Custom>
      <Custom Action="HelperCleanup" After="RemoveFiles">REMOVE="ALL"</Custom>
    </InstallExecuteSequence>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml,
		"SetFixAcl was scheduled next to FixAcl, which convert left out",
		"      - {sequence: execute, after: InstallFiles}\n",
		"SetHelperCleanup was scheduled next to HelperCleanup, which convert left out",
		"      - {sequence: execute, after: RemoveFiles}\n",
	)
	wantNone(t, yaml, "before: FixAcl", "before: HelperCleanup")
}

// TestConvertRememberWithOwnSearch leaves installDir.remember out for a
// util:RemoveFolderEx on the install dir when the file searches for that
// property itself; msi-builder refuses both together.
func TestConvertRememberWithOwnSearch(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi" xmlns:util="http://schemas.microsoft.com/wix/UtilExtension">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="APPLICATIONFOLDER">
      <RegistrySearch Id="AppFolder" Root="HKLM" Key="SOFTWARE\Example\Hello" Name="Path" Type="raw" Win64="yes" />
    </Property>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="APPLICATIONFOLDER" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
          <Component Id="Cleanup" Guid="*" Win64="yes">
            <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="Path" Type="string" Value="[APPLICATIONFOLDER]" KeyPath="yes" />
            <util:RemoveFolderEx On="uninstall" Property="APPLICATIONFOLDER" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main" ConfigurableDirectory="APPLICATIONFOLDER"><ComponentRef Id="App" /><ComponentRef Id="Cleanup" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "property: APPLICATIONFOLDER", "  APPLICATIONFOLDER:\n    search:\n")
	wantNone(t, yaml, "remember")
}

// TestConvertLaunchConditionsFirst sets downgrade: ignore for a package
// whose LaunchConditions runs before FindRelatedProducts: no launch
// condition saw a newer version there, and the build refuses the default
// downgrade check in that order.
func TestConvertLaunchConditionsFirst(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <Upgrade Id="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
      <UpgradeVersion Property="PREVIOUSVERSIONSINSTALLED" Maximum="1.0.0" IncludeMaximum="no" />
    </Upgrade>
    <Property Id="RIBBON"><RegistrySearch Id="Ribbon" Root="HKLM" Key="SOFTWARE\Example\Ribbon" Type="raw" /></Property>
    <Condition Message="Hello needs the ribbon.">Installed OR RIBBON</Condition>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <InstallExecuteSequence>
      <AppSearch Sequence="1" />
      <LaunchConditions After="AppSearch" />
      <RemoveExistingProducts After="InstallFinalize">PREVIOUSVERSIONSINSTALLED</RemoveExistingProducts>
    </InstallExecuteSequence>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "  downgrade: ignore\n", "TODO(line 19): LaunchConditions runs before FindRelatedProducts, where",
		"the WiX package has no downgrade check", "  - action: LaunchConditions\n")
	// downgrade: ignore's todo says it all; the move gets no second one
	// (BowPad).
	wantNone(t, yaml, "LaunchConditions runs before FindRelatedProducts here")
}

// TestConvertLaunchConditionsFirstEverywhere sets downgrade: ignore for a
// MajorUpgrade package only when every sequence runs LaunchConditions
// before FindRelatedProducts, so its downgrade check never saw a newer
// version.
func TestConvertLaunchConditionsFirstEverywhere(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Condition Message="Hello needs Windows 10.">Installed OR VersionNT >= 603</Condition>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <InstallExecuteSequence><LaunchConditions Sequence="10" /></InstallExecuteSequence>
    <InstallUISequence><LaunchConditions Sequence="10" /></InstallUISequence>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "  downgrade: ignore\n", "TODO(line 14): LaunchConditions runs before FindRelatedProducts, where",
		"the WiX package never saw a newer version in a launch condition")
	wantNone(t, yaml, "LaunchConditions runs before FindRelatedProducts here")
}

// TestConvertDroppedActionBothSides keeps the order of actions on both
// sides of a left-out action: taking its place would put them in file
// order, so they keep their sides of InstallFiles instead, which keeps a
// deferred one in the install script even when the left-out action is
// immediate.
func TestConvertDroppedActionBothSides(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <Binary Id="Fix" SourceFile="fix.js" />
    <CustomAction Id="RunJs" BinaryKey="Fix" JScriptCall="Fix" Execute="immediate" />
    <CustomAction Id="SetSecond" Directory="INSTALLDIR" ExeCommand="&quot;[SystemFolder]cmd.exe&quot; /c echo" Execute="deferred" Impersonate="no" Return="ignore" />
    <CustomAction Id="SetFirst" Property="FIRST" Value="1" />
    <InstallExecuteSequence>
      <Custom Action="RunJs" After="InstallFiles" />
      <Custom Action="SetSecond" After="RunJs" />
      <Custom Action="SetFirst" Before="RunJs" />
    </InstallExecuteSequence>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "{sequence: execute, before: InstallFiles}", "{sequence: execute, after: InstallFiles}",
		"which convert left out and which has actions on both sides")
}

// TestConvertDuplicateFirewallNames gives the second of two firewall
// exceptions with one name its Id, so that the rules build.
func TestConvertDuplicateFirewallNames(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:fw="http://wixtoolset.org/schemas/v4/wxs/firewall">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe"><fw:FirewallException Id="ServerRule" Name="Hello" Scope="any" /></File></Component>
        <Component><File Source="hellod.exe"><fw:FirewallException Id="GuiRule" Name="Hello" Scope="any" /></File></Component>
      </Directory>
    </StandardDirectory>
  </Package>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "  - name: Hello\n", "    name: Hello (GuiRule)\n",
		`the firewall exception on line 6 has the same name; msi-builder needs a name per rule, so convert added "(GuiRule)" to this one`)
}

// TestConvertNamelessInTargetDir makes a nameless Directory directly in
// TARGETDIR, which nothing sets, TARGETDIR itself, as WiX resolves it.
func TestConvertNamelessInTargetDir(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="InstallDirectory" />
    </Directory>
    <DirectoryRef Id="InstallDirectory">
      <Directory Id="LIBS" Name="lib">
        <Component Id="Lib" Guid="*" Win64="yes"><File Source="hello.dll" KeyPath="yes" /></Component>
      </Directory>
      <Component Id="App" Guid="*" Win64="yes">
        <File Source="hello.exe" KeyPath="yes" />
        <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="Path" Type="string" Value="[InstallDirectory]" />
      </Component>
    </DirectoryRef>
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="Lib" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{Arch: "x64"})
	wantAll(t, yaml, "directory: TARGETDIR/lib\n", "directory: TARGETDIR\n", "value: '[TARGETDIR]'")
	wantNone(t, yaml, "InstallDirectory", "has no Name")
}

// TestConvertMachineDefaultChoosesContext makes a per-user package with an
// ALLUSERS=1 row that chooses its context with actions perMachineOrUser:
// the row installs per machine by default, which perUserOrMachine would
// flip.
func TestConvertMachineDefaultChoosesContext(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perUser" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="ALLUSERS" Value="1" />
    <Property Id="USERDIR"><RegistrySearch Id="UserDir" Root="HKCU" Key="Software\Example\Hello" Name="Dir" Type="directory" /></Property>
    <CustomAction Id="ResetALLUSERS" Property="ALLUSERS" Value="" Execute="firstSequence" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <InstallExecuteSequence>
      <Custom Action="ResetALLUSERS" Before="FindRelatedProducts">USERDIR</Custom>
    </InstallExecuteSequence>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "  scope: perMachineOrUser\n", "use perUserOrMachine if the install should default to the installing user")
}

// TestConvertLaunchFileElsewhere notes a finish page that launches a file
// another source file defines: msi-builder refuses ui.launch.file there.
func TestConvertLaunchFileElsewhere(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.dll" /></Component>
      </Directory>
    </StandardDirectory>
    <ui:WixUI Id="WixUI_Minimal" />
    <Property Id="WIXUI_EXITDIALOGOPTIONALCHECKBOXTEXT" Value="Launch Hello" />
    <Property Id="WixShellExecTarget" Value="[#MainExecutable]" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	wantAll(t, string(res.YAML), "file: MainExecutable\n",
		"the finish page launches the file MainExecutable, which this file does not define")
}

// TestConvertExtensionActionSidesKept leaves actions next to a WiX
// extension's action to extensionAnchors, which keeps the order of actions
// on both sides of it, rather than moving them all to its place.
func TestConvertExtensionActionSidesKept(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Component><File Source="hello.exe" /></Component>
      </Directory>
    </StandardDirectory>
    <CustomAction Id="SetFirst" Property="FIRST" Value="1" />
    <CustomAction Id="SetSecond" Property="SECOND" Value="2" />
    <InstallExecuteSequence>
      <Custom Action="Wix4CloseApplications_X64" Before="InstallValidate" />
      <Custom Action="SetSecond" After="Wix4CloseApplications_X64" />
      <Custom Action="SetFirst" Before="Wix4CloseApplications_X64" />
    </InstallExecuteSequence>
  </Package>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "{sequence: execute, before: CostFinalize}", "{sequence: execute, after: CostFinalize}")
	wantNone(t, yaml, "which convert left out")
}

// TestConvertRememberWithUnconvertedSearch keeps installDir.remember for
// util:RemoveFolderEx when the file's own search of the install dir is one
// convert cannot write, which would leave the folder unfound.
func TestConvertRememberWithUnconvertedSearch(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi" xmlns:util="http://schemas.microsoft.com/wix/UtilExtension">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Property Id="APPLICATIONFOLDER">
      <IniFileSearch Id="AppFolder" Name="hello.ini" Section="main" Key="dir" Type="raw" />
    </Property>
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="APPLICATIONFOLDER" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
          <Component Id="Cleanup" Guid="*" Win64="yes">
            <RegistryValue Root="HKLM" Key="SOFTWARE\Example\Hello" Name="Path" Type="string" Value="[APPLICATIONFOLDER]" KeyPath="yes" />
            <util:RemoveFolderEx On="uninstall" Property="APPLICATIONFOLDER" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main" ConfigurableDirectory="APPLICATIONFOLDER"><ComponentRef Id="App" /><ComponentRef Id="Cleanup" /></Feature>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	wantAll(t, string(res.YAML), "  remember: true\n")
}

// TestConvertIconIDNotIdentifier renames a WiX v3 <Icon> Id that is no MSI
// identifier, which the build refuses, and every reference to it (Input
// Leap).
func TestConvertIconIDNotIdentifier(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Icon Id="hello-app.ico" SourceFile="res/hello-app.ico" />
    <Icon Id="1st.ico" SourceFile="res/first.ico" />
    <Property Id="ARPPRODUCTICON" Value="hello-app.ico" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <Shortcut Id="Menu" Directory="ProgramMenuFolder" Name="Hello" Target="[INSTALLDIR]hello.exe" Icon="hello-app.ico" />
            <Shortcut Id="Other" Directory="ProgramMenuFolder" Name="Hello 2" Target="[INSTALLDIR]hello.exe" Icon="1st.ico" />
          </Component>
        </Directory>
      </Directory>
      <Directory Id="ProgramMenuFolder" />
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "    icon: hello_app.ico\n", "icon: _1st.ico\n", "  hello_app.ico: res/hello-app.ico\n",
		"  _1st.ico: res/first.ico\n", `TODO(line 5): <Icon> Id="hello-app.ico" is not an MSI identifier`)
	wantNone(t, yaml, "icon: hello-app.ico", "icon: 1st.ico", "  hello-app.ico:")
}

// TestConvertIconIDThroughDefine renames icon ids that a define, an
// <Icon> Id and a SetProperty of ARPPRODUCTICON give, so that every
// reference matches the icons key.
func TestConvertIconIDThroughDefine(t *testing.T) {
	t.Parallel()

	src := `<?define IconName = "hello-app.ico" ?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Icon Id="$(var.IconName)" SourceFile="a.ico" />
    <SetProperty Id="ARPPRODUCTICON" Value="hello-app.ico" After="CostFinalize" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <Shortcut Id="Menu" Directory="ProgramMenuFolder" Name="Hello" Target="[INSTALLDIR]hello.exe" Icon="$(var.IconName)" />
          </Component>
        </Directory>
      </Directory>
      <Directory Id="ProgramMenuFolder" />
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Product>
</Wix>`

	yaml := convertWith(t, src, wixconvert.Options{})
	wantAll(t, yaml, "  hello_app.ico: a.ico\n", "icon: hello_app.ico\n", "value: hello_app.ico")
	wantNone(t, yaml, "icon: {{", "__var")
}

// TestConvertIconIDClash notes a renamed icon id that another icon has.
func TestConvertIconIDClash(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <Icon Id="app-icon" SourceFile="a.ico" />
    <Icon Id="app_icon" SourceFile="b.ico" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLDIR" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes"><File Source="hello.exe" KeyPath="yes" /></Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	yaml := string(res.YAML)
	wantAll(t, yaml, "  app_icon: b.ico\n", `<Icon> Id="app-icon" is not an MSI identifier and becomes "app_icon", the id of another icon`)
	wantNone(t, yaml, "a.ico")
}
