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
		"Convert left out SCOPE and PreferMachine, so msiexec SCOPE=... no longer preselects an option.",
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

	wantAll(t, yaml, "    perMachine: \"1\"\n", "Convert left out SCOPE, so msiexec")
	wantNone(t, yaml, "perUser:", "  SCOPE:")

	// The same default in a perMachineOrUser package is the page's own.
	yaml = convertValid(t, scopeDialogSource("perMachineOrUser", "machine", ""))
	wantNone(t, yaml, "scopePage:", "TODO: the scope dialog", "  SCOPE:")
	wantAll(t, yaml, "Convert left out SCOPE: only the dialog ScopeDlg read it")
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
		"Convert left out SCOPE and PreferUser, PreferUserAgain, so msiexec",
	)
	wantNone(t, yaml, "id: PreferUser")
}

// TestConvertScopeDialogKept checks the cases where the dialog's property
// stays: setters that choose both options, a launch condition that reads
// it, a setter whose condition reads it, a search that sets it, and
// conditions too long for ui.scopePage. The page then tests the property
// itself, which its default, search and setters fill in before the page.
func TestConvertScopeDialogKept(t *testing.T) {
	t.Parallel()

	long := "PREVMACHINE AND " + strings.Repeat("A", 230) + " = 1"

	for name, rest := range map[string]string{
		"both": `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />
    <SetProperty Id="SCOPE" Action="PreferUser" Value="user" After="PreferMachine" Sequence="ui" Condition="MSIINSTALLPERUSER = 1" />`,
		"read": `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />
    <Launch Condition="Installed OR SCOPE = &quot;machine&quot; OR NOT PREVMACHINE" Message="Installed for everyone already." />`,
		"guard": `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="SCOPE = &quot;user&quot; AND PREVMACHINE" />`,
		"search": `
    <Property Id="LASTSCOPE"><RegistrySearch Id="Last" Root="HKCU" Key="Software\Example\Hello" Name="Scope" Type="raw" /></Property>
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />
    <SetProperty Id="SCOPE" Action="PreferLast" Value="[LASTSCOPE]" After="PreferMachine" Sequence="ui" Condition="LASTSCOPE" />`,
		"long": `
    <SetProperty Id="SCOPE" Action="PreferUser" Value="user" After="AppSearch" Sequence="ui" Condition="` + long + `" />`,
	} {
		value := "user"
		if name == "long" {
			value = "machine"
		}

		yaml := convertValid(t, scopeDialogSource("perUserOrMachine", value, rest))

		wantAll(t, yaml,
			"    perMachine: SCOPE = \"machine\"\n", "    perUser: SCOPE = \"user\"\n",
			"SCOPE and the actions that set it stay.",
			"  SCOPE: "+value+"\n",
		)
		wantNone(t, yaml, "Convert left out")

		// Only a reader after the dialog misses the chosen option.
		if follows := strings.Contains(yaml, "SCOPE no longer follows the option chosen"); follows != (name == "read") {
			t.Errorf("%s: todo about readers = %v:\n%s", name, follows, yaml)
		}
	}
}

// TestConvertScopeDialogSearchedProperty checks a dialog property with its
// own search: the page tests the property, which keeps its search.
func TestConvertScopeDialogSearchedProperty(t *testing.T) {
	t.Parallel()

	src := strings.Replace(scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="AppSearch" Sequence="ui" Condition="PREVMACHINE" />`),
		`<Property Id="SCOPE" Value="user" Secure="yes" />`,
		`<Property Id="SCOPE" Value="user" Secure="yes"><RegistrySearch Id="S" Root="HKCU" Key="Software\Example\Hello" Name="Scope" Type="raw" /></Property>`, 1)

	yaml := convertValid(t, src)
	wantAll(t, yaml, "    perMachine: SCOPE = \"machine\"\n", "id: PreferMachine", "name: Scope\n")
}

// TestConvertScopeDialogSetterPlace checks setters by their place: one
// after the dialog does not change what it preselects, and one whose place
// convert cannot follow to the dialog or AppSearch leaves a todo.
func TestConvertScopeDialogSetterPlace(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="ScopeDlg" Sequence="ui" Condition="PREVMACHINE" />`))
	wantNone(t, yaml, "perMachine: PREVMACHINE", "TODO: the scope dialog", "id: PreferMachine")
	wantAll(t, yaml, "Convert left out SCOPE and PreferMachine: only the dialog ScopeDlg read it")

	yaml = convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="CostFinalize" Sequence="ui" Condition="PREVMACHINE" />`))
	wantAll(t, yaml, "the place of the action PreferMachine, which sets SCOPE, does not lead to the dialog or AppSearch")
	wantNone(t, yaml, "scopePage:")
}

// TestConvertScopeDialogAnchors checks actions placed on the scope dialog,
// which the scope page replaces: one before it runs right after AppSearch,
// one after it after LaunchConditions, which follows the page, also when
// the setter they followed is left out.
func TestConvertScopeDialogAnchors(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" Before="ScopeDlg" Sequence="ui" Condition="PREVMACHINE" />
    <SetProperty Id="GREETING" Value="hi" After="PreferMachine" Sequence="ui" />
    <SetProperty Id="CHOSEN" Value="1" After="ScopeDlg" Sequence="ui" />`))

	wantAll(t, yaml, "    perMachine: PREVMACHINE\n", "{sequence: ui, after: AppSearch}", "{sequence: ui, after: LaunchConditions}")
	wantNone(t, yaml, "ScopeDlg}", "id: PreferMachine")

	// Kept, the setter itself moves.
	yaml = convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" Before="ScopeDlg" Sequence="ui" Condition="PREVMACHINE" />
    <Launch Condition="Installed OR SCOPE = &quot;machine&quot; OR NOT PREVMACHINE" Message="Installed for everyone already." />`))
	wantAll(t, yaml, "{sequence: ui, after: AppSearch, condition: PREVMACHINE}")
}

// TestConvertScopeDialogEarlySetter checks a setter before AppSearch,
// whose condition WiX evaluates before the searches: it stays, and the
// page tests the property, instead of evaluating the condition after them.
func TestConvertScopeDialogEarlySetter(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" Before="AppSearch" Sequence="ui" Condition="PREVMACHINE" />`))

	wantAll(t, yaml, "    perMachine: SCOPE = \"machine\"\n", "id: PreferMachine", "before: AppSearch")
	wantNone(t, yaml, "perMachine: PREVMACHINE")
}

// TestConvertScopeDialogProblemAnchors checks that actions placed on the
// dialog move also when its preselection does not map, and that a dialog
// shown after costing gets a todo, as do the actions placed on it, which
// now run before costing.
func TestConvertScopeDialogProblemAnchors(t *testing.T) {
	t.Parallel()

	yaml := convertValid(t, scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="SCOPE" Action="PreferMachine" Value="machine" After="CostFinalize" Sequence="ui" Condition="PREVMACHINE" />
    <SetProperty Id="CHOSEN" Value="1" After="ScopeDlg" Sequence="ui" />`))
	wantAll(t, yaml, "does not lead to the dialog or AppSearch", "{sequence: ui, after: LaunchConditions}")

	src := strings.Replace(scopeDialogSource("perUserOrMachine", "user", `
    <SetProperty Id="DIRCOPY" Value="[INSTALLFOLDER]" After="ScopeDlg" Sequence="ui" />`),
		`<Show Dialog="ScopeDlg" After="AppSearch"`, `<Show Dialog="ScopeDlg" After="CostFinalize"`, 1)
	yaml = convertValid(t, src)
	wantAll(t, yaml,
		"the sequence does not show it right after AppSearch",
		"SetDIRCOPY was placed next to the dialog ScopeDlg, which the scope page replaces",
		"{sequence: ui, after: LaunchConditions}",
	)
	wantNone(t, yaml, "scopePage:")
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
