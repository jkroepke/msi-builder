package wixconvert_test

import (
	"strings"
	"testing"
)

// scopeDialogSource is a dual package with WixUI_Minimal and a hand-built
// dialog that picks the install context with the radio property SCOPE.
// The first %s is the Package scope, the second the default of SCOPE, the
// third the rest of the package (setters, sequences).
func scopeDialogSource(scope, value, rest string) string {
	return strings.NewReplacer("{scope}", scope, "{value}", value, "{rest}", rest).Replace(`<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:ui="http://wixtoolset.org/schemas/v4/wxs/ui">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" Scope="{scope}">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Feature Id="Main">
      <Component Directory="INSTALLFOLDER" Guid="B03D3C75-F835-47A9-8224-1D1238B4F74F">
        <RegistryValue Root="HKMU" Key="Software\Example\Hello" Name="Installed" Value="1" Type="string" KeyPath="yes" />
      </Component>
    </Feature>
    <Property Id="PREVMACHINE" Secure="yes">
      <RegistrySearch Id="PrevMachine" Root="HKLM" Key="Software\Example\Hello" Name="Installed" Type="raw" />
    </Property>
    <Property Id="SCOPE" Value="{value}" Secure="yes" />
    <UI>
      <ui:WixUI Id="WixUI_Minimal" />
      <Dialog Id="ScopeDlg" Width="370" Height="220" Title="[ProductName] Setup">
        <Control Id="Scope" Type="RadioButtonGroup" X="20" Y="88" Width="330" Height="60" Property="SCOPE">
          <RadioButtonGroup Property="SCOPE">
            <RadioButton Value="user" X="0" Y="0" Width="330" Height="20" Text="Only for me" />
            <RadioButton Value="machine" X="0" Y="30" Width="330" Height="20" Text="Everyone" />
          </RadioButtonGroup>
        </Control>
        <Control Id="Next" Type="PushButton" X="214" Y="182" Width="66" Height="20" Default="yes" Text="Next">
          <Publish Property="ALLUSERS" Value="1" Order="1" Condition="SCOPE = &quot;machine&quot;" />
          <Publish Property="ALLUSERS" Value="{}" Order="2" Condition="SCOPE = &quot;user&quot;" />
          <Publish Property="MSIINSTALLPERUSER" Value="{}" Order="3" Condition="SCOPE = &quot;machine&quot;" />
          <Publish Property="MSIINSTALLPERUSER" Value="1" Order="4" Condition="SCOPE = &quot;user&quot;" />
          <Publish Event="EndDialog" Value="Return" Order="5" />
        </Control>
      </Dialog>
    </UI>
    <InstallUISequence>
      <Show Dialog="ScopeDlg" After="AppSearch" Condition="NOT Installed AND UILevel = 5" />
    </InstallUISequence>
{rest}
  </Package>
</Wix>`)
}

// TestConvertScopeDialogSetter checks a hand-built scope dialog whose
// radio default a UI setter changes: the setter's condition becomes
// ui.scopePage.perMachine, and the setter and the dialog's property, which
// nothing else reads, are left out; an action placed after the setter
// moves to its place.
func TestConvertScopeDialogSetter(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="ALLUSERS = 1 OR PREVMACHINE" />
    <SetProperty Id="GREETING" Value="hi" After="PreferMachine" Sequence="ui" />`))

	wantAll(t, yaml,
		"  scopePage:\n",
		"    perMachine: ALLUSERS = 1 OR PREVMACHINE\n",
		"the hand-built dialog ScopeDlg picks the install context with SCOPE",
		"Convert left out SCOPE and PreferMachine.",
		"setProperty: {name: GREETING, value: hi}",
		"{sequence: ui, after: AppSearch}",
	)
	wantNone(t, yaml, "PreferMachine\n", "name: SCOPE", "  SCOPE:", "perUser:", "schedules a dialog")
}

// TestConvertScopeDialogDefault checks a scope dialog that preselects
// everyone by default in a perUserOrMachine package, whose scope page
// starts on the context the package opened in.
func TestConvertScopeDialogDefault(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "machine", ""))

	wantAll(t, yaml, "    perMachine: \"1\"\n", "Convert left out SCOPE.")
	wantNone(t, yaml, "perUser:", "  SCOPE:")

	// The same default in a perMachineOrUser package is the page's own.
	yaml = convertValid(t, scopeDialogSource("perMachineOrUser", "machine", ""))
	wantNone(t, yaml, "scopePage:", "TODO: the scope dialog")
	wantAll(t, yaml, "  SCOPE: machine\n")
}

// TestConvertScopeDialogPreferUser checks a setter that preselects the
// current user over a default of everyone: perUser takes its condition,
// and perMachine, which wins over it, the opposite.
func TestConvertScopeDialogPreferUser(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "machine", `
    <CustomAction Id="PreferUser" Property="SCOPE" Value="user" />
    <CustomAction Id="PreferUserAgain" Property="SCOPE" Value="user" />
    <InstallUISequence>
      <Custom Action="PreferUser" After="AppSearch" Condition="NOT PREVMACHINE" />
      <Custom Action="PreferUserAgain" After="PreferUser" Condition="MSIINSTALLPERUSER = 1" />
    </InstallUISequence>`))

	wantAll(t, yaml,
		"    perMachine: NOT ((NOT PREVMACHINE) OR (MSIINSTALLPERUSER = 1))\n",
		"    perUser: (NOT PREVMACHINE) OR (MSIINSTALLPERUSER = 1)\n",
		"Convert left out SCOPE and PreferUser, PreferUserAgain.",
	)
	wantNone(t, yaml, "id: PreferUser")
}

// TestConvertScopeDialogUnmapped checks setters that choose both contexts:
// they stay, with a todo that ui.scopePage needs conditions.
func TestConvertScopeDialogUnmapped(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />
    <SetProperty Id="SCOPE" Action="PreferUser" Value="user" After="PreferMachine" Sequence="ui" Condition="MSIINSTALLPERUSER = 1" />`))

	wantAll(t, yaml,
		`the scope dialog ScopeDlg preselects what SCOPE says, which convert could not map because actions set SCOPE to both "machine" and "user"`,
		"id: PreferMachine", "id: PreferUser", "  SCOPE: user\n",
	)
	wantNone(t, yaml, "scopePage:")
}

// TestConvertScopeDialogPropertyRead checks a dialog property that a
// launch condition reads as well: ui.scopePage takes the setter's
// condition, and the setter and the property stay for that reader.
func TestConvertScopeDialogPropertyRead(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />
    <Launch Condition="Installed OR SCOPE = &quot;machine&quot; OR NOT PREVMACHINE" Message="Installed for everyone already." />`))

	wantAll(t, yaml, "    perMachine: PREVMACHINE\n", "id: PreferMachine", "  SCOPE: user\n")
	wantNone(t, yaml, "Convert left out")
}

// TestConvertScopeDialogPerUser checks a scope dialog in a package that
// does not become a dual one: no ui.scopePage, which needs a dual scope.
func TestConvertScopeDialogPerUser(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUser", "machine", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />`))

	wantAll(t, yaml, "the hand-built dialog ScopeDlg picks the install context with SCOPE", "id: PreferMachine")
	wantNone(t, yaml, "scopePage:")
}
