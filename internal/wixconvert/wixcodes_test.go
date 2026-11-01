package wixconvert_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// guidProbeSource is the GuidProbe sample whose WiX 5.0.2 build gave the
// codes of the probe tests on Windows (MSISPEC section 8.10).
const guidProbeSource = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="GuidProbe" Manufacturer="Example" Version="1.0.0" UpgradeCode="11111111-2222-3333-4444-555555555555">
    <MediaTemplate EmbedCab="yes" />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="Example Co\GuidProbe">
        <Directory Id="BinDir" Name="Bin"><Directory Id="DotDir" /></Directory>
        <Directory Id="SeedDir" Name="Seeded" ComponentGuidGenerationSeed="9B1D4C73-6F0E-4B1A-8C2D-0E5F6A7B8C9D" />
      </Directory>
    </StandardDirectory>
    <StandardDirectory Id="CommonAppDataFolder"><Directory Id="DataDir" Name="GuidProbe" /></StandardDirectory>
    <Feature Id="Main">
      <Component Id="AppExe" Directory="INSTALLFOLDER"><File Source="App.EXE" /></Component>
      <Component Id="Tool" Directory="BinDir"><File Source="Tool.dll" /></Component>
      <Component Id="Dot" Directory="DotDir"><File Name="dot.txt" Source="a.txt" /></Component>
      <Component Id="Seed" Directory="SeedDir"><File Name="seed.txt" Source="a.txt" /></Component>
      <Component Id="Data" Directory="DataDir"><File Name="data.txt" Source="a.txt" /></Component>
      <Component Id="RegHklm32" Directory="INSTALLFOLDER" Bitness="always32">
        <RegistryValue Root="HKLM" Key="Software\Example\GuidProbe" Name="InstallDir" Value="[INSTALLFOLDER]" Type="string" />
      </Component>
      <Component Id="RegHkcuDefault" Directory="INSTALLFOLDER">
        <RegistryValue Root="HKCU" Key="Software\Example\GuidProbe" Value="x" Type="string" />
      </Component>
      <Files Directory="INSTALLFOLDER" Include="payload\**" />
    </Feature>
  </Package>
</Wix>
`

// computedRowsSource returns the GuidProbe sample with the registry components
// of the rows that follow from the rule: a 64-bit HKLM value, in a
// RegistryKey, and an HKMU value.
func computedRowsSource() string {
	return strings.Replace(guidProbeSource, "      <Files ", `      <Component Id="RegHklm64" Directory="INSTALLFOLDER" Bitness="always64">
        <RegistryKey Root="HKLM" Key="Software\Example">
          <RegistryValue Key="GuidProbe" Name="InstallDir" Value="[INSTALLFOLDER]" Type="string" />
        </RegistryKey>
      </Component>
      <Component Id="RegHkmu32" Directory="INSTALLFOLDER" Bitness="always32">
        <RegistryValue Root="HKMU" Key="Software\Example\GuidProbe" Name="InstallDir" Value="[INSTALLFOLDER]" Type="string" />
      </Component>
      <Files `, 1)
}

// guidProbeFiles is the folder of the GuidProbe source.
var guidProbeFiles = fstest.MapFS{ //nolint:gochecknoglobals // test fixture
	"payload/sub/deep.txt": {Data: []byte("deep\n")},
}

// convertCodes converts a source and returns its components by Id, with
// the folder source's guids under "payload/<path>".
func convertCodes(t *testing.T, source string, opts wixconvert.Options) (map[string]string, *wixconvert.Result) {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(source), opts)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(res.YAML, config.Options{})
	if err != nil {
		t.Fatalf("%v\n%s", err, res.YAML)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Errorf("the converted file does not validate: %v\n%s", err, res.YAML)
	}

	codes := make(map[string]string)

	for _, comp := range cfg.Components {
		if comp.ID != "" && comp.GUID != "" && comp.GUID != config.Auto {
			codes[comp.ID] = comp.GUID
		}

		for _, file := range comp.Files {
			for rel, guid := range file.GUIDs {
				codes["payload/"+rel] = guid
			}
		}
	}

	return codes, res
}

// checkCodes compares the codes with the ones WiX gave.
func checkCodes(t *testing.T, got, want map[string]string, yaml []byte) {
	t.Helper()

	for id, code := range want {
		if got[id] != code {
			t.Errorf("%s: guid %q, want %s", id, got[id], code)
		}
	}

	if t.Failed() {
		t.Logf("msi.yaml:\n%s", yaml)
	}
}

// TestConvertComputedCodes computes the codes of the GuidProbe components:
// the rows WiX 5.0.2 built for x64 and x86 (verified on Windows), and the
// 64-bit HKLM and HKMU rows, which follow from the same rule.
func TestConvertComputedCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		arch   string
		source string
		want   map[string]string
	}{
		{"x64", computedRowsSource(), map[string]string{
			"AppExe":               "{1C2DB505-EC99-5268-95D7-15C86F1C7D04}",
			"Tool":                 "{20007141-C4E9-58E7-B4D4-0056BA9F182C}",
			"Dot":                  "{63B133D7-33C0-5620-9DB3-E5A0A865B7DA}",
			"Seed":                 "{275B22EF-3C86-5836-BFF2-4526D108D00A}",
			"Data":                 "{CA3B5A2D-81D6-52D9-BA6E-927961D104E3}",
			"RegHklm32":            "{162CB2F4-2850-5726-8CA7-E3C87872F97F}",
			"RegHkcuDefault":       "{B863E501-C42E-52C2-8391-A94CDFA00A85}",
			"RegHklm64":            "{867F0D2A-7D90-51FC-8A1F-A827059CCCAB}",
			"RegHkmu32":            "{B498994D-1FA5-5E44-BAE0-DE5721BA87BE}",
			"payload/sub/deep.txt": "{662D877D-ADA7-54BF-9239-6BE212AD887D}",
		}},
		{"x86", guidProbeSource, map[string]string{
			"AppExe":         "{BDABDDA1-76FA-5C45-A447-F219E8BC607F}",
			"Seed":           "{275B22EF-3C86-5836-BFF2-4526D108D00A}",
			"Data":           "{CA3B5A2D-81D6-52D9-BA6E-927961D104E3}",
			"RegHklm32":      "{162CB2F4-2850-5726-8CA7-E3C87872F97F}",
			"RegHkcuDefault": "{7F5FD924-A06F-5DC1-ACCC-27E6C804749B}",
		}},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			t.Parallel()

			got, res := convertCodes(t, tc.source, wixconvert.Options{Arch: tc.arch, FS: guidProbeFiles})
			checkCodes(t, got, tc.want, res.YAML)

			if !strings.Contains(string(res.YAML), "platform: "+tc.arch) {
				t.Errorf("package.platform is not %s:\n%s", tc.arch, res.YAML)
			}
		})
	}
}

// TestConvertComputedCodesBCGG computes registry codes the way WiX 3 and
// WiX 6 with -bcgg do: numbered roots. File codes stay the same.
func TestConvertComputedCodesBCGG(t *testing.T) {
	t.Parallel()

	got, res := convertCodes(t, computedRowsSource(), wixconvert.Options{Arch: "x64", BCGG: true, FS: guidProbeFiles})
	checkCodes(t, got, map[string]string{
		"AppExe":    "{1C2DB505-EC99-5268-95D7-15C86F1C7D04}",
		"RegHklm32": "{9CA1B61F-85B6-5B11-A923-ACACF0FCC8D7}",
		"RegHklm64": "{BBFCEBAE-A020-5402-9EAF-EB8A3F7CD9FE}",
		"RegHkmu32": "{D2064F2C-4F75-5D61-B667-90A31797FA2E}",
	}, res.YAML)
}

// v3ProbeSource is a WiX 3 source with the GuidProbe registry values: the
// v3 namespace numbers the roots, and Win64 sets the bitness.
const v3ProbeSource = `<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Product Id="*" Name="GuidProbe" Manufacturer="Example" Version="1.0.0" Language="1033" UpgradeCode="11111111-2222-3333-4444-555555555555">
    <Package InstallerVersion="500" Compressed="yes" InstallScope="perMachine" Platform="x64" />
    <MediaTemplate EmbedCab="yes" />
    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFiles64Folder">
        <Directory Id="INSTALLFOLDER" Name="Example Co">
          <Directory Id="ProbeDir" Name="GuidProbe">
            <Component Id="AppExe" Guid="*"><File Id="AppExe" Source="App.EXE" KeyPath="yes" /></Component>
            <Component Id="Reg32" Guid="*" Win64="no">
              <RegistryValue Root="HKLM" Key="Software\Example\GuidProbe" Name="InstallDir" Value="[INSTALLFOLDER]" Type="string" KeyPath="yes" />
            </Component>
            <Component Id="Reg64" Guid="*">
              <RegistryValue Root="HKLM" Key="Software\Example\GuidProbe" Name="InstallDir" Value="[ProbeDir]" Type="string" KeyPath="yes" />
            </Component>
          </Directory>
        </Directory>
      </Directory>
    </Directory>
    <Feature Id="Main" Level="1">
      <ComponentRef Id="AppExe" />
      <ComponentRef Id="Reg32" />
      <ComponentRef Id="Reg64" />
    </Feature>
  </Product>
</Wix>
`

// TestConvertComputedCodesV3 computes the codes of a WiX 3 source.
func TestConvertComputedCodesV3(t *testing.T) {
	t.Parallel()

	got, res := convertCodes(t, v3ProbeSource, wixconvert.Options{})
	checkCodes(t, got, map[string]string{
		"AppExe": "{1C2DB505-EC99-5268-95D7-15C86F1C7D04}",
		"Reg32":  "{9CA1B61F-85B6-5B11-A923-ACACF0FCC8D7}",
		"Reg64":  "{BBFCEBAE-A020-5402-9EAF-EB8A3F7CD9FE}",
	}, res.YAML)
}

// TestConvertComputedCodesUnresolved keeps the todo for the codes convert
// cannot compute: without --arch for a 6432 folder or a default bitness, a
// name from an undefined variable, a folder from another source, and a
// folder key path.
func TestConvertComputedCodesUnresolved(t *testing.T) {
	t.Parallel()

	const source = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="GuidProbe" Manufacturer="Example" Version="1.0.0" UpgradeCode="11111111-2222-3333-4444-555555555555">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="GuidProbe">
        <Directory Id="VarDir" Name="$(var.Missing)" />
      </Directory>
    </StandardDirectory>
    <StandardDirectory Id="CommonAppDataFolder"><Directory Id="DataDir" Name="GuidProbe" /></StandardDirectory>
    <Feature Id="Main">
      <Component Id="AppExe" Directory="INSTALLFOLDER"><File Source="App.EXE" /></Component>
      <Component Id="Var" Directory="VarDir"><File Source="var.txt" /></Component>
      <Component Id="Other" Directory="OtherSourceDir"><File Source="other.txt" /></Component>
      <Component Id="Folder" Directory="DataDir" KeyPath="yes"><CreateFolder /></Component>
      <Component Id="Reg" Directory="DataDir">
        <RegistryValue Root="HKCU" Key="Software\Example\GuidProbe" Value="x" Type="string" />
      </Component>
      <Component Id="Data" Directory="DataDir"><File Name="data.txt" Source="a.txt" /></Component>
    </Feature>
  </Package>
</Wix>
`

	res, err := wixconvert.Convert(strings.NewReader(source), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	text := string(res.YAML)

	// The code of a file below a standard folder needs no platform.
	if !strings.Contains(text, "guid: '{CA3B5A2D-81D6-52D9-BA6E-927961D104E3}'") {
		t.Errorf("Data has no computed guid:\n%s", text)
	}

	if strings.Count(text, "guid: '{") != 1 {
		t.Errorf("convert computed codes it cannot know:\n%s", text)
	}

	for _, want := range []string{
		"WiX generated the Guid of AppExe, Var, Other, Folder, Reg, which convert cannot compute from the source",
		"Run msi-builder convert again with --arch",
	} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
			t.Errorf("YAML lacks %q:\n%s", want, text)
		}
	}

	// With --arch, the platform-dependent ones follow.
	res, err = wixconvert.Convert(strings.NewReader(source), wixconvert.Options{Arch: "x64"})
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(res.YAML), "guid: '{"); n != 3 {
		t.Errorf("%d computed codes with --arch x64, want 3:\n%s", n, res.YAML)
	}
}

// TestConvertComputedCodesBuiltWins keeps the codes of the built package
// and computes none with --msi.
func TestConvertComputedCodesBuiltWins(t *testing.T) {
	t.Parallel()

	built := &wixconvert.BuiltPackage{Name: "probe.msi", Components: []wixconvert.BuiltComponent{
		{ID: "AppExe", Code: "{00000000-0000-4000-8000-000000000001}", Directory: "INSTALLFOLDER", KeyFile: "App.EXE"},
	}}

	got, res := convertCodes(t, guidProbeSource, wixconvert.Options{Arch: "x64", Built: built, FS: guidProbeFiles})
	checkCodes(t, got, map[string]string{"AppExe": "{00000000-0000-4000-8000-000000000001}"}, res.YAML)

	if _, ok := got["Tool"]; ok {
		t.Errorf("convert computed a code next to --msi:\n%s", res.YAML)
	}
}

// TestConvertComputedCodesArch refuses an unknown platform.
func TestConvertComputedCodesArch(t *testing.T) {
	t.Parallel()

	if _, err := wixconvert.Convert(strings.NewReader(guidProbeSource), wixconvert.Options{Arch: "ia64"}); err == nil {
		t.Error("convert took --arch ia64")
	}
}

// TestConvertComputedCodesUPG12 computes the codes of the Files glob that
// WiX 5.0.2 built on the CI runner in UPG-12, as its log records them.
func TestConvertComputedCodesUPG12(t *testing.T) {
	t.Parallel()

	const source = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="MsiBuilderHarvest" Manufacturer="msi-builder tests" Version="1.1.0" UpgradeCode="5e3a9c17-2b6d-4f80-9a4e-7c1d3b5f8e02" Scope="perMachine">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <MediaTemplate EmbedCab="yes" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="MsiBuilderHarvest" />
    </StandardDirectory>
    <Files Include="dist\**" Directory="INSTALLFOLDER" />
  </Package>
</Wix>
`

	files := fstest.MapFS{}
	for _, name := range []string{"app.exe", "readme.txt", "docs/guide.md", "docs/api/index.md"} {
		files["dist/"+name] = &fstest.MapFile{Data: []byte(name + "\n")}
	}

	got, res := convertCodes(t, source, wixconvert.Options{FS: files})
	checkCodes(t, got, map[string]string{
		"payload/app.exe":           "{F60D2DDC-2553-5D4D-BA94-987744B5976C}",
		"payload/docs/api/index.md": "{8F112EF4-46C0-555A-9DEE-F60E89CD4724}",
		"payload/docs/guide.md":     "{F824AE54-E7B7-5FE0-8879-9D17126A71D7}",
		"payload/readme.txt":        "{CAF3105C-A653-52AA-9B6D-53925BC1345D}",
	}, res.YAML)

	if len(res.Notes) > 0 {
		t.Errorf("notes %v, want none", res.Notes)
	}
}
