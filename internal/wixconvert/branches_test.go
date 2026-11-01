package wixconvert_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

const branchHead = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
`

// convertWithVars converts src and checks that the YAML validates with each
// set of variables.
func convertWithVars(t *testing.T, src string, opts wixconvert.Options, varSets ...map[string]string) *wixconvert.Result {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(src), opts)
	if err != nil {
		t.Fatal(err)
	}

	for _, vars := range varSets {
		cfg, err := config.Parse(res.YAML, config.Options{Vars: vars})
		if err != nil {
			t.Fatalf("parse with %v: %v\n%s", vars, err, res.YAML)
		}

		if err := config.Validate(cfg).Err(); err != nil {
			t.Errorf("validate with %v: %v\n%s", vars, err, res.YAML)
		}
	}

	return res
}

func hasNote(notes []wixconvert.Note, prefix string) bool {
	return slices.ContainsFunc(notes, func(n wixconvert.Note) bool { return strings.HasPrefix(n.Message, prefix) })
}

// A ComponentGroup in a block, referenced from a feature in a block of the
// same condition, becomes a template too, so that a build that leaves the
// group out has no component without a feature (ICE21).
func TestConvertConditionalComponentGroup(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <?ifndef HasExtras ?>
  <?define HasExtras = 0 ?>
  <?endif ?>
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
    <Feature Id="Main">
      <ComponentGroupRef Id="Core" />
      <?if $(var.HasExtras) = 1 ?>
      <ComponentGroupRef Id="Extras" />
      <?endif ?>
    </Feature>
  </Package>
  <Fragment>
    <ComponentGroup Id="Core" Directory="INSTALLFOLDER">
      <Component Id="App"><File Source="hello.exe" /></Component>
    </ComponentGroup>
    <?if $(var.HasExtras) = 1 ?>
    <ComponentGroup Id="Extras" Directory="INSTALLFOLDER">
      <Files Include="extras\**" />
      <File Id="Readme" Source="extras.txt" />
    </ComponentGroup>
    <?endif ?>
  </Fragment>
</Wix>
`

	res := convertWithVars(t, src, wixconvert.Options{}, map[string]string{"HasExtras": "0"}, map[string]string{"HasExtras": "1"})

	text := string(res.YAML)
	if strings.Count(text, `{{ if eq .HasExtras "1" }}`) != 2 {
		t.Errorf("want one template block around the feature members and one around the components:\n%s", text)
	}

	if hasNote(res.Notes, "<?if $(var.HasExtras) = 1?>") {
		t.Errorf("a block was not converted as a template: %v", res.Notes)
	}

	cfg, err := config.Parse(res.YAML, config.Options{Vars: map[string]string{"HasExtras": "0"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Components) != 1 {
		t.Errorf("without extras, %d components, want 1:\n%s", len(cfg.Components), text)
	}
}

// A ComponentGroup in a loop, or one that references components written
// outside its block, is not converted as a template.
func TestConvertComponentGroupNotKept(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"loop": `<Feature Id="Main"><?foreach D in a;b ?><ComponentGroupRef Id="G_$(var.D)" /><?endforeach ?></Feature>
  </Package>
  <Fragment>
    <?foreach D in a;b ?><ComponentGroup Id="G_$(var.D)" Directory="INSTALLFOLDER"><File Id="F_$(var.D)" Source="$(var.D).txt" /></ComponentGroup><?endforeach ?>
  </Fragment>`,
		"reference": `<Feature Id="Main"><?if $(var.Extra) = 1 ?><ComponentGroupRef Id="Extras" /><?endif ?></Feature>
    <Component Id="App" Directory="INSTALLFOLDER"><File Source="hello.exe" /></Component>
  </Package>
  <Fragment>
    <?if $(var.Extra) = 1 ?><ComponentGroup Id="Extras" Directory="INSTALLFOLDER"><ComponentRef Id="Shared" /></ComponentGroup><?endif ?>
    <Component Id="Shared" Directory="INSTALLFOLDER"><File Source="shared.dll" /></Component>
  </Fragment>`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := strings.Replace(branchHead, "</StandardDirectory>\n", "</StandardDirectory>\n    "+body+"\n</Wix>", 1)

			res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if !hasNote(res.Notes, "<?foreach D in a;b?> is not expanded") && !hasNote(res.Notes, "<?if $(var.Extra) = 1?> is not evaluated") {
				t.Errorf("notes %q, want the block noted", res.Notes)
			}

			_, components, _ := strings.Cut(string(res.YAML), "\ncomponents:")
			if name == "reference" && strings.Contains(components, "{{") {
				t.Errorf("the group became a template:\n%s", res.YAML)
			}
		})
	}
}

// A block that is not converted as a template takes the branch that a
// build without -d variables takes: the file's defaults and, with --arch,
// the platform. Without a value to decide it, the first branch stays.
func TestConvertChosenBranch(t *testing.T) {
	t.Parallel()

	const arch = `<?if $(sys.BUILDARCH) = x86 ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="x86" After="CostInitialize" Sequence="execute" />
    <?else ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="native" After="CostInitialize" Sequence="execute" />
    <?endif ?>`

	tests := map[string]struct {
		body, arch    string
		want, without string
		note          string
		vars          map[string]string
	}{
		"other platform": {
			body: arch, arch: "x64", want: "native", without: "x86",
			note: "<?if $(sys.BUILDARCH) = x86?> is not converted as a template: only the branch on line 7, which a build for --arch x64 without -d variables takes, was converted",
		},
		"first platform": {
			body: arch, arch: "x86", want: "x86", without: "native",
			note: "<?if $(sys.BUILDARCH) = x86?> is not converted as a template: only the branch on line 5, which a build for --arch x86 without -d variables takes, was converted",
		},
		"short platform name": {
			body: `<?if $(sys.BUILDARCHSHORT) = A64 ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="arm" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			arch: "arm64", want: "arm",
			note: "<?if $(sys.BUILDARCHSHORT) = A64?> is not converted as a template: only the branch on line 5, which a build for --arch arm64 without -d variables takes, was converted",
		},
		"known false first branch": {
			body: `<?if $(sys.BUILDARCH) = arm64 ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="arm" After="CostInitialize" Sequence="execute" />
    <?elseif $(var.Flavor) = x ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="flavor" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			arch: "x64", want: "flavor", without: "arm",
			note: "<?if $(sys.BUILDARCH) = arm64?> is not evaluated: only the branch on line 7, the first not known to be false, was converted",
		},
		"template block in a later branch": {
			body: `<?define Edition = "pro" ?>
    <?if $(var.Edition) = basic ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="basic" After="CostInitialize" Sequence="execute" />
    <?else ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="pro" After="CostInitialize" Sequence="execute" />
    <?if $(var.Flavor) = a ?><Property Id="FLAVOR" Value="a" /><?else ?><Property Id="FLAVOR" Value="b" /><?endif ?>
    <?endif ?>`,
			want: "pro", without: "basic", vars: map[string]string{"Flavor": "a"},
			note: "<?if $(var.Edition) = basic?> is not converted as a template: only the branch on line 8, which a build without -d variables takes, was converted",
		},
		"variable a guessed branch skips": {
			body: `<?define Tier = "1" ?>
    <?if $(var.Edition) = pro ?>
    <SetProperty Id="EDITION" Action="SetEd" Value="pro" After="CostInitialize" Sequence="execute" />
    <?else ?>
    <?define Tier = "2" ?>
    <SetProperty Id="EDITION" Action="SetEd" Value="basic" After="CostInitialize" Sequence="execute" />
    <?endif ?>
    <?if $(var.Tier) = 2 ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="tier2" After="CostInitialize" Sequence="execute" />
    <?else ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="tier1" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			want: "tier1", without: "tier2",
			note: "<?if $(var.Tier) = 2?> is not evaluated: only the branch on line 14, the first not known to be false, was converted",
		},
		"platform through a define": {
			body: `<?define Plat = "$(sys.BUILDARCH)" ?>
    <?if $(var.Plat) = x86 ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="x86" After="CostInitialize" Sequence="execute" />
    <?else ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="native" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			arch: "x64", want: "native", without: "x86",
			note: "<?if $(var.Plat) = x86?> is not converted as a template: only the branch on line 8, which a build for --arch x64 without -d variables takes, was converted",
		},
		"no platform": {
			body: arch, want: "x86", without: "native",
			note: "<?if $(sys.BUILDARCH) = x86?> is not evaluated: only its first branch was converted",
		},
		"default value": {
			body: `<?ifndef Edition ?><?define Edition = "basic" ?><?endif ?>
    <?if $(var.Edition) = full ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="full" After="CostInitialize" Sequence="execute" />
    <?elseif $(var.Edition) = basic ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="basic" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			want: "basic", without: "full",
			note: "<?if $(var.Edition) = full?> is not converted as a template: only the branch on line 8, which a build without -d variables takes, was converted",
		},
		"no branch applies": {
			body: `<?define Edition = "basic" ?>
    <?if $(var.Edition) = full ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="full" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			without: "full",
			note:    "<?if $(var.Edition) = full?> is not converted as a template: no branch applies in a build without -d variables, so none was converted; convert it by hand if your build takes one",
		},
		"variable only -d sets": {
			body: `<?ifdef Pro ?>
    <SetProperty Id="DEFAULTDIR" Action="SetDir" Value="pro" After="CostInitialize" Sequence="execute" />
    <?endif ?>`,
			want: "pro",
			note: "<?ifdef Pro?> is not evaluated: only its first branch was converted",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			vars := tc.vars
			if vars == nil {
				vars = map[string]string{}
			}

			res := convertWithVars(t, branchHead+"    "+tc.body+"\n  </Package>\n</Wix>", wixconvert.Options{Arch: tc.arch}, vars)

			text := string(res.YAML)
			if tc.want != "" && !strings.Contains(text, "value: "+tc.want) {
				t.Errorf("YAML lacks the value %s:\n%s", tc.want, text)
			}

			if tc.without != "" && strings.Contains(text, "value: "+tc.without) {
				t.Errorf("YAML holds the value %s of a branch left out:\n%s", tc.without, text)
			}

			if !hasNote(res.Notes, tc.note) {
				t.Errorf("notes %q, want %q", res.Notes, tc.note)
			}
		})
	}
}

// A ProgId without Extension children whose verb command and Open with
// entry are registry values becomes a file type of the file the command
// runs; a ProgId that runs nothing keeps its description and icon on the
// file of its icon.
func TestConvertProgIDRegistry(t *testing.T) {
	t.Parallel()

	src := branchHead + `    <Component Id="App" Directory="INSTALLFOLDER"><File Id="HelloExe" Source="hello.exe" /></Component>
    <Component Id="Types" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKMU" Key="Software\Example\Capabilities" Name="ApplicationName" Value="Hello" Type="string" KeyPath="yes" />
      <ProgId Id="Hello.Document" Description="Hello Document" Icon="HelloExe" IconIndex="1" />
      <RegistryValue Root="HKMU" Key="Software\Classes\Hello.Document\shell\open\command" Value="&quot;[#HelloExe]&quot; &quot;%1&quot;" Type="string" />
      <RegistryValue Root="HKMU" Key="Software\Classes\Hello.Document\shell\print\command" Value="&quot;[#HelloExe]&quot; /p &quot;%1&quot;" Type="string" />
      <RegistryValue Root="HKMU" Key="Software\Classes\.hello\OpenWithProgids" Name="Hello.Document" Value="" Type="string" />
      <RegistryValue Root="HKMU" Key="Software\Classes\.hello\OpenWithProgids" Name="Other.Document" Value="" Type="string" />
      <ProgId Id="Hello.Icon" Description="Hello Icon" Icon="HelloExe" />
    </Component>
  </Package>
</Wix>
`

	res := convertWithVars(t, src, wixconvert.Options{}, map[string]string{})
	text := string(res.YAML)

	for _, want := range []string{
		`        fileTypes:
          - progId: Hello.Document
            description: Hello Document
            iconIndex: 1
            extensions:
              - extension: hello
                openWithOnly: true
            verbs:
              - id: open
              - id: print
                arguments: /p "%1"
          - progId: Hello.Icon
            description: Hello Icon
            iconIndex: 0
            verbs: []
`,
		"name: Other.Document",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("YAML lacks\n%s\nin\n%s", want, text)
		}
	}

	if strings.Contains(text, `shell\open\command`) || strings.Contains(text, "name: Hello.Document") {
		t.Errorf("the file type's registry values stayed:\n%s", text)
	}

	if hasNote(res.Notes, "the ProgId") {
		t.Errorf("unexpected notes %v", res.Notes)
	}
}

// With --msi, a Files glob that keeps no code gets a note also when the
// upgrade removes the old version early, so that missing codes never go
// unnoticed.
func TestConvertBuiltCodesTiedGlobsEarly(t *testing.T) {
	t.Parallel()

	src := branchHead + `    <Files Include="bin\**" Directory="INSTALLFOLDER" />
    <Files Include="docs\**" Directory="INSTALLFOLDER" />
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "fil1", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "app.exe"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
		},
	}

	res := convertWithVars(t, src, wixconvert.Options{Built: built}, map[string]string{})

	var lines []int

	for _, note := range res.Notes {
		if strings.HasPrefix(note.Message, "another Files glob into the same folder may add app.exe too") &&
			strings.Contains(note.Message, "msi-builder gives them other codes") {
			lines = append(lines, note.Line)
		}
	}

	if !slices.Equal(lines, []int{5, 6}) {
		t.Errorf("tie notes in lines %v, want [5 6]: %v", lines, res.Notes)
	}

	// Without --msi and with an early removal, nothing is lost.
	res = convertWithVars(t, src, wixconvert.Options{}, map[string]string{})
	if hasNote(res.Notes, "WiX gave each file") {
		t.Errorf("unexpected glob note: %v", res.Notes)
	}
}

// With --msi, a File Id that repeats in branches that never apply together
// keeps the built code only in the branch whose key file the package has.
func TestConvertBuiltCodesExclusiveBranches(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
    <Feature Id="Main"><ComponentGroupRef Id="G" /></Feature>
  </Package>
  <Fragment>
    <ComponentGroup Id="G" Directory="INSTALLFOLDER">
      <?if $(var.A) = 1 ?>
      <File Id="F" Source="hello.exe" />
      <?else ?>
      <File Id="F" Source="extras.txt" />
      <?endif ?>
    </ComponentGroup>
  </Fragment>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "F", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "hello.exe"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
		},
	}

	res := convertWithVars(t, src, wixconvert.Options{Built: built}, map[string]string{"A": "1"}, map[string]string{"A": "0"})
	if n := strings.Count(string(res.YAML), "000000000001"); n != 1 {
		t.Errorf("the built code is pinned %d times, want once:\n%s", n, res.YAML)
	}
}

// With --msi, a component of its own Id in a template block keeps the
// built code also when convert cannot fill in its key file's name.
func TestConvertBuiltCodesTemplateKeyFile(t *testing.T) {
	t.Parallel()

	src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <?define N = "extra" ?>
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
    <Feature Id="Main">
      <?if $(var.A) = 1 ?>
      <Component Id="C" Directory="INSTALLFOLDER"><File Name="$(var.N).txt" Source="x.txt" /></Component>
      <?endif ?>
      <Component Id="App" Directory="INSTALLFOLDER" Guid="00000000-0000-4000-8000-000000000009"><File Source="hello.exe" /></Component>
    </Feature>
  </Package>
</Wix>
`

	built := &wixconvert.BuiltPackage{
		Name: "hello.msi",
		Components: []wixconvert.BuiltComponent{
			{ID: "C", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "extra.txt"},
		},
		Directories: []wixconvert.BuiltDirectory{
			{ID: "TARGETDIR", Name: "SourceDir"},
			{ID: "ProgramFiles64Folder", Parent: "TARGETDIR", Name: "PFiles64"},
			{ID: "INSTALLFOLDER", Parent: "ProgramFiles64Folder", Name: "Hello"},
		},
	}

	res := convertWithVars(t, src, wixconvert.Options{Built: built}, map[string]string{"A": "1"}, map[string]string{"A": "0"})
	if !strings.Contains(string(res.YAML), "000000000001") {
		t.Errorf("the built code is not pinned:\n%s", res.YAML)
	}
}
