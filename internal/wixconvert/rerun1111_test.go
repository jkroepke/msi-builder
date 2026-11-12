package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
)

// The tests in this file cover convert problems found in the full rerun
// against WiX of 2026-11-11.

// helloProduct wraps body, the elements of a WiX v3 Product, in a
// per-machine Hello package with an install folder.
func helloProduct(scope, body string) string {
	return `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi" xmlns:util="http://schemas.microsoft.com/wix/UtilExtension">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="` + scope + `" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
` + body + `
  </Product>
</Wix>`
}

// helloDirs is a ProgramFiles64Folder\Hello folder named INSTALLFOLDER
// around components.
func helloDirs(components string) string {
	return `    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
` + components + `
        </Directory>
      </Directory>
    </Directory>`
}

// TestConvertQuietExecPropertyCommand turns the Property that carries
// the command line of a deferred quiet-exec action into its run.command.
func TestConvertQuietExecPropertyCommand(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <Property Id="SetupHello" Value="&quot;SC.EXE&quot; failureflag HelloSvc 1" />
    <CustomAction Id="SetupHello" BinaryKey="WixCA" DllEntry="WixQuietExec64" Impersonate="no" Execute="deferred" Return="ignore" />
    <InstallExecuteSequence>
      <Custom Action="SetupHello" After="InstallFiles">NOT REMOVE</Custom>
    </InstallExecuteSequence>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, `command: '"SC.EXE" failureflag HelloSvc 1'`)
	wantNone(t, got, "id: SetupHello\n    value:", "names no command")
}

// TestConvertServiceControlWithoutEvent leaves out a ServiceControl that
// names no event, which does nothing, with a todo.
func TestConvertServiceControlWithoutEvent(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <ServiceControl Id="HelloSvcCtl" Name="HelloSvc" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "names no Start, Stop or Remove event")
	wantNone(t, got, "serviceControl:")
}

// TestConvertMergedServiceControls merges two controls of one service in
// one component into one services[].control: it starts the service on
// install and stops and removes it on uninstall.
func TestConvertMergedServiceControls(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hellosvc.exe" KeyPath="yes" />
            <ServiceInstall Id="HelloSvc" Name="HelloSvc" Type="ownProcess" Start="auto" ErrorControl="normal" />
            <ServiceControl Id="HelloSvcStart" Name="HelloSvc" Start="install" Wait="no" />
            <ServiceControl Id="HelloSvcStop" Name="HelloSvc" Stop="uninstall" Remove="uninstall" Wait="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "control:\n          stopOnInstall: false\n")
	wantNone(t, got, "startOnInstall: false", "stopOnUninstall: false", "removeOnUninstall: false", "wait: false", "TODO(line")
}

// TestConvertWorkingDirectoryNotDirectory leaves out a shortcut's
// WorkingDirectory that names no directory, such as a property.
func TestConvertWorkingDirectoryNotDirectory(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes">
              <Shortcut Id="HelloLink" Directory="ProgramMenuFolder" Name="Hello" WorkingDirectory="HELLOHOME" />
            </File>
          </Component>`)+`
    <Directory Id="ProgramMenuFolder" />
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "WorkingDirectory HELLOHOME names no directory")
	wantNone(t, got, "workingDirectory: HELLOHOME")
}

// TestConvertWorkingDirectoryStandardFolder declares a standard folder
// that only a shortcut's WorkingDirectory names.
func TestConvertWorkingDirectoryStandardFolder(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes">
              <Shortcut Id="HelloLink" Directory="ProgramMenuFolder" Name="Hello" WorkingDirectory="WindowsVolume" />
            </File>
          </Component>`)+`
    <Directory Id="ProgramMenuFolder" />
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "workingDirectory: WindowsVolume")
}

// TestConvertLaunchNotProgram keeps no ui.launch for a document, which
// the finish page cannot run, and none when nothing publishes the launch.
func TestConvertLaunchNotProgram(t *testing.T) {
	t.Parallel()

	product := func(target, publish string) string {
		return helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Id="HelloExe" Source="hello.exe" KeyPath="yes" />
            <File Id="Readme" Source="readme.html" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <Property Id="WIXUI_INSTALLDIR" Value="INSTALLFOLDER" />
    <Property Id="WixShellExecTarget" Value="[#`+target+`]" />
    <CustomAction Id="LaunchApplication" BinaryKey="WixCA" DllEntry="WixShellExec" Impersonate="yes" />
    <UI>
      <UIRef Id="WixUI_InstallDir" />
`+publish+`
    </UI>`)
	}

	publish := `      <Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="LaunchApplication">NOT Installed</Publish>`

	got := convertWith(t, product("Readme", publish), wixconvert.Options{})
	wantAll(t, got, "the finish page opens readme.html, which is no program")
	wantNone(t, got, "launch:")

	got = convertWith(t, product("HelloExe", ""), wixconvert.Options{})
	wantAll(t, got, "no Finish button or check box of the finish page runs WixShellExec")
	wantNone(t, got, "launch:")
}

// TestConvertNamelessDirectoryWithSetter names a directory without a Name
// that an action sets, so that it builds.
func TestConvertNamelessDirectoryWithSetter(t *testing.T) {
	t.Parallel()

	src := helloProduct("perUser", `    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="HelloInstallDirectory" />
    </Directory>
    <DirectoryRef Id="HelloInstallDirectory">
      <Component Id="App" Guid="*">
        <File Source="hello.exe" KeyPath="yes" />
      </Component>
    </DirectoryRef>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <CustomAction Id="SetHelloDir" Property="HelloInstallDirectory" Value="[LocalAppDataFolder]Programs\Hello" />
    <InstallExecuteSequence>
      <Custom Action="SetHelloDir" Before="CostFinalize">NOT Installed</Custom>
    </InstallExecuteSequence>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "so it is named HelloInstallDirectory wherever no action sets it")
}

// TestConvertPrivilegedComponents maps the components that Privileged
// conditions pick in a dual package to their scope.
func TestConvertPrivilegedComponents(t *testing.T) {
	t.Parallel()

	src := helloProduct("perUser", `    <Property Id="ALLUSERS" Value="2" />
`+helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>
          <Component Id="MachineReg" Guid="3f2504e0-4f89-41d3-9a0c-0305e82c3302" Win64="yes">
            <Condition>Privileged</Condition>
            <RegistryValue Root="HKLM" Key="Software\Example\Hello" Name="Path" Value="[INSTALLFOLDER]" Type="string" KeyPath="yes" />
          </Component>
          <Component Id="UserReg" Guid="3f2504e0-4f89-41d3-9a0c-0305e82c3303" Win64="yes">
            <Condition>ALLUSERS=2 AND NOT Privileged</Condition>
            <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="Path" Value="[INSTALLFOLDER]" Type="string" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="MachineReg" /><ComponentRef Id="UserReg" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "scope: perMachine", "scope: perUser")
	wantNone(t, got, "condition: Privileged", "NOT Privileged")
}

// TestConvertMachineEnvironmentPerUser writes a System environment
// variable of a per-user package for the user, which Windows Installer
// does too.
func TestConvertMachineEnvironmentPerUser(t *testing.T) {
	t.Parallel()

	src := helloProduct("perUser", `    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="LocalAppDataFolder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*">
            <File Source="hello.exe" KeyPath="yes" />
            <Environment Id="HelloPath" Name="PATH" Value="[INSTALLFOLDER]" Part="last" Action="set" System="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "scope: user")
	wantNone(t, got, "scope: machine")
}

// TestConvertRegistryKeyPathComponentID keeps a component whose key path
// is a registry value without an Id.
func TestConvertRegistryKeyPathComponentID(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>
          <Component Id="InstallKey" Guid="*" Win64="yes">
            <RegistryKey Root="HKLM" Key="Software\Example\Hello">
              <RegistryValue Name="InstallPath" Value="[INSTALLFOLDER]" Type="string" KeyPath="yes" />
            </RegistryKey>
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="InstallKey" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "id: InstallKey")
}

// TestConvertDuplicateFileKey gives a second file without an Id whose
// name another file of the package has its own key.
func TestConvertDuplicateFileKey(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <File Source="docs\index.html" />
          </Component>
          <Directory Id="ApiDocs" Name="api">
            <Component Id="Api" Guid="*" Win64="yes">
              <File Source="api\index.html" KeyPath="yes" />
            </Component>
          </Directory>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="Api" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "index.html_2")
}

// TestConvertAtPlacementAfterBuilderActions chains Sequence numbers that
// fall on the actions msi-builder adds after a standard action.
func TestConvertAtPlacementAfterBuilderActions(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <CustomAction Id="SaveVersion" Property="SaveVersionData" Value="[ProductVersion]" />
    <InstallExecuteSequence>
      <Custom Action="SaveVersion" Sequence="1501" />
    </InstallExecuteSequence>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "after: InstallInitialize")
	wantNone(t, got, "at: 1501")
}

// TestConvertBindProperty fills in a !(bind.property.X) of a property
// that the product names.
func TestConvertBindProperty(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
            <RegistryValue Root="HKLM" Key="Software\!(bind.Property.Manufacturer)\!(bind.property.ProductName)" Name="Version" Value="!(bind.property.ProductVersion)" Type="string" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, `Software\Example\Hello`, "value: 1.0.0")
	wantNone(t, got, "bind.")
}

// TestConvertStaticLoop writes out a loop over a static list whose body
// defines variables, and one whose references name components by the
// loop variable, as WiX does.
func TestConvertStaticLoop(t *testing.T) {
	t.Parallel()

	src := `<?xml version="1.0"?>
<?define Langs = "de;fr" ?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>
          <?foreach L in $(var.Langs) ?>
            <?if $(var.L) = de ?>
              <?define LangGuid = "3f2504e0-4f89-41d3-9a0c-0305e82c3310" ?>
            <?else ?>
              <?define LangGuid = '3f2504e0-4f89-41d3-9a0c-0305e82c3311' ?>
            <?endif ?>
            <Directory Id="Lang_$(var.L)" Name="$(var.L)">
              <Component Id="Lang_$(var.L)" Guid="$(var.LangGuid)" Win64="yes">
                <?foreach Part in core;extra ?>
                <File Id="$(var.Part)_$(var.L)" Source="$(var.L)\$(var.Part).dll" />
                <?endforeach ?>
              </Component>
            </Directory>
          <?endforeach ?>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main">
      <ComponentRef Id="App" />
      <?foreach L in de;fr ?>
      <ComponentRef Id="Lang_$(var.L)" />
      <?endforeach ?>
    </Feature>
  </Product>
</Wix>`

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "guid: 3f2504e0-4f89-41d3-9a0c-0305e82c3310", "guid: 3f2504e0-4f89-41d3-9a0c-0305e82c3311",
		"id: core_fr", "id: extra_de", "Lang_de, Lang_fr")
	wantNone(t, got, "not expanded", "names no component", "LangGuid")
}

// TestConvertProductCodeVariableStar treats a product code variable that
// the file defines as * like * itself.
func TestConvertProductCodeVariableStar(t *testing.T) {
	t.Parallel()

	src := `<?xml version="1.0"?>
<?define ProductCode = "*" ?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="$(var.ProductCode)" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
` + helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`) + `
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Product>
</Wix>`

	got := convertWith(t, src, wixconvert.Options{})
	wantNone(t, got, "productCode:")
}

// TestConvertEmptyGuidPermanent writes a component with an empty Guid,
// which Windows Installer never removes, as permanent.
func TestConvertEmptyGuidPermanent(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "guid: none", "permanent: true", "empty Guid")
}

// TestConvertAtPlacementKeepsAfterOrder keeps a WiX After placement on the
// anchor before a Sequence number in the same gap: a rollback action
// scheduled After the anchor still runs before its deferred action.
func TestConvertAtPlacementKeepsAfterOrder(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <CustomAction Id="DoHello" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]hello.exe&quot; --do" Execute="deferred" Impersonate="no" Return="check" />
    <CustomAction Id="UndoHello" Directory="INSTALLFOLDER" ExeCommand="&quot;[INSTALLFOLDER]hello.exe&quot; --undo" Execute="rollback" Impersonate="no" Return="ignore" />
    <InstallExecuteSequence>
      <Custom Action="UndoHello" After="InstallInitialize">NOT Installed</Custom>
      <Custom Action="DoHello" Sequence="1510">NOT Installed</Custom>
    </InstallExecuteSequence>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "after: InstallInitialize", "after: UndoHello")
	wantNone(t, got, "at: 1510", "after: DoHello")
}

// TestConvertLaunchVariableName keeps ui.launch for a file whose name
// comes from a variable: its extension is not known.
func TestConvertLaunchVariableName(t *testing.T) {
	t.Parallel()

	src := helloProduct("perMachine", helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Id="HelloExe" Source="$(var.HelloApp.TargetPath)" KeyPath="yes" />
          </Component>`)+`
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
    <Property Id="WIXUI_INSTALLDIR" Value="INSTALLFOLDER" />
    <Property Id="WixShellExecTarget" Value="[#HelloExe]" />
    <CustomAction Id="LaunchApplication" BinaryKey="WixCA" DllEntry="WixShellExec" Impersonate="yes" />
    <UI>
      <UIRef Id="WixUI_InstallDir" />
      <Publish Dialog="ExitDialog" Control="Finish" Event="DoAction" Value="LaunchApplication">NOT Installed</Publish>
    </UI>`)

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := string(res.YAML)
	wantAll(t, got, "launch:", "file: HelloExe")
	wantNone(t, got, "which is no program")
}

// TestConvertPrivilegedWithoutAllUsers2 keeps a per-user package without
// ALLUSERS=2 per user when a component tests Privileged.
func TestConvertPrivilegedWithoutAllUsers2(t *testing.T) {
	t.Parallel()

	src := helloProduct("perUser", `    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="LocalAppDataFolder">
        <Directory Id="INSTALLFOLDER" Name="Hello">
          <Component Id="App" Guid="*">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>
          <Component Id="AdminReg" Guid="3f2504e0-4f89-41d3-9a0c-0305e82c3302">
            <Condition>Privileged</Condition>
            <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="Admin" Value="1" Type="string" KeyPath="yes" />
          </Component>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main"><ComponentRef Id="App" /><ComponentRef Id="AdminReg" /></Feature>`)

	got := convertWith(t, src, wixconvert.Options{})
	wantAll(t, got, "scope: perUser\n", "condition: Privileged")
	wantNone(t, got, "perUserOrMachine")
}

// TestConvertStaticLoopLaterDefine leaves a loop that reads a variable a
// later define sets again unexpanded: the static value is the last one.
func TestConvertStaticLoopLaterDefine(t *testing.T) {
	t.Parallel()

	src := `<?xml version="1.0"?>
<?define Langs = "de;fr" ?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
` + helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>`) + `
    <Feature Id="Main">
      <ComponentRef Id="App" />
      <?foreach L in $(var.Langs) ?>
      <ComponentRef Id="Lang_$(var.L)" />
      <?endforeach ?>
    </Feature>
    <?define Langs = "it" ?>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	wantNone(t, string(res.YAML), "Lang_it")
}

// TestConvertStaticLoopDottedVariable fills a loop variable with a dot in
// its name into the copies that are not resolved.
func TestConvertStaticLoopDottedVariable(t *testing.T) {
	t.Parallel()

	src := `<?xml version="1.0"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="Hello" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" />
    <MajorUpgrade DowngradeErrorMessage="newer" />
` + helloDirs(`          <Component Id="App" Guid="*" Win64="yes">
            <File Source="hello.exe" KeyPath="yes" />
          </Component>
          <?foreach Lang.Code in de;fr ?>
          <?define Seen = "1" ?>
          <Component Id="Lang_$(var.Lang.Code)" Guid="*" Win64="yes">
            <File Id="Text_$(var.Lang.Code)" Source="$(var.Lang.Code).txt" KeyPath="yes" />
          </Component>
          <?endforeach ?>`) + `
    <Feature Id="Main">
      <ComponentRef Id="App" />
      <?ifdef Seen ?>
      <ComponentRef Id="Lang_de" />
      <ComponentRef Id="Lang_fr" />
      <?endif ?>
    </Feature>
  </Product>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := string(res.YAML)
	wantAll(t, got, "id: Lang_de", "id: Lang_fr")
	wantNone(t, got, "Lang_Code")
}
