package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// wixCodesSource is the WiX source of the UPG-14 product: components whose
// Guid WiX generates, keyed on files in a 6432 folder with a two-level
// name, in a folder without a name, in a seeded folder and in a standard
// folder, on a 32-bit HKLM value and on a default value, and a Files glob.
// The old version is removed after the new one is installed.
const wixCodesSource = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="MsiBuilderWixCodes" Manufacturer="msi-builder tests" Version="%s" UpgradeCode="8b2d6e41-3c7a-4f95-a0e8-1d5c9b7f3a26" Scope="perMachine">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <MediaTemplate EmbedCab="yes" />
    <StandardDirectory Id="ProgramFiles6432Folder">
      <Directory Id="INSTALLFOLDER" Name="MsiBuilder Tests\WixCodes">
        <Directory Id="BinDir" Name="Bin"><Directory Id="DotDir" /></Directory>
        <Directory Id="SeedDir" Name="Seeded" ComponentGuidGenerationSeed="4f7c2a91-6d3e-4b85-9a0f-2e8d1c5b7a43" />
      </Directory>
    </StandardDirectory>
    <StandardDirectory Id="CommonAppDataFolder"><Directory Id="DataDir" Name="MsiBuilderWixCodes" /></StandardDirectory>
    <Feature Id="Main">
      <Component Id="AppExe" Directory="INSTALLFOLDER"><File Source="App.EXE" /></Component>
      <Component Id="Tool" Directory="BinDir"><File Source="Tool.dll" /></Component>
      <Component Id="Dot" Directory="DotDir"><File Name="dot.txt" Source="a.txt" /></Component>
      <Component Id="Seed" Directory="SeedDir"><File Name="seed.txt" Source="a.txt" /></Component>
      <Component Id="Data" Directory="DataDir"><File Name="data.txt" Source="a.txt" /></Component>
      <Component Id="RegHklm32" Directory="INSTALLFOLDER" Bitness="always32">
        <RegistryValue Root="HKLM" Key="Software\MsiBuilderTests\WixCodes" Name="InstallDir" Value="[INSTALLFOLDER]" Type="string" />
      </Component>
      <Component Id="RegHklmDefault" Directory="INSTALLFOLDER">
        <RegistryValue Root="HKLM" Key="Software\MsiBuilderTests\WixCodes" Value="x" Type="string" />
      </Component>
      <Files Directory="INSTALLFOLDER" Include="payload\**" />
    </Feature>
  </Package>
</Wix>
`

// wixCodesKey is the registry key of the UPG-14 product.
const wixCodesKey = `HKLM\Software\MsiBuilderTests\WixCodes`

// wixCodesFiles are the files of the UPG-14 product below the install
// folder; data.txt is in ProgramData.
var wixCodesFiles = []string{ //nolint:gochecknoglobals // test table
	"App.EXE", `Bin\Tool.dll`, `Bin\dot.txt`, `Seeded\seed.txt`, "readme.txt", `sub\deep.txt`,
}

// componentCode matches a component code in a converted file.
var componentCode = regexp.MustCompile(`'\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}'`)

// TestMsiexecComputedComponentCodes checks the component codes that
// convert computes from a WiX source without the package (UPG-14): they
// are the codes of the package WiX 5 built, for -arch x86 and x64, and the
// msi-builder package converted without --msi upgrades the WiX package
// with a late removal and keeps every file and registry value (unlike
// generated codes, UPG-11). The WiX package needs wix.exe on PATH; the
// test skips without it unless MSI_BUILDER_WIX_REQUIRED is set.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecComputedComponentCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	wix, err := exec.LookPath("wix")
	if err != nil {
		if os.Getenv("MSI_BUILDER_WIX_REQUIRED") != "" {
			t.Fatalf("wix is not on PATH: %v", err)
		}

		t.Skip("wix is not on PATH")
	}

	cli := buildCLI(t)
	dir := t.TempDir()
	writeWixCodesPayload(t, dir)
	writeText(t, filepath.Join(dir, "v1.wxs"), fmt.Sprintf(wixCodesSource, "1.0.0"))

	for _, arch := range []string{"x86", "x64"} {
		t.Run("UPG-14 codes "+arch, func(t *testing.T) {
			built := filepath.Join(dir, "v1-"+arch+".msi")
			runTool(t, dir, wix, "build", "-arch", arch, "-o", built, "v1.wxs")

			computed := convertCodes(t, dir, cli, "--arch", arch, "v1.wxs")
			fromMsi := convertCodes(t, dir, cli, "--arch", arch, "--msi", built, "v1.wxs")

			// Seven components and two files of the glob.
			if len(fromMsi) != 9 {
				t.Errorf("convert --msi found %d codes, want 9: %v", len(fromMsi), fromMsi)
			}

			if !slices.Equal(computed, fromMsi) {
				t.Errorf("computed codes %v, want the codes of the WiX package %v", computed, fromMsi)
			}
		})
	}

	t.Run("UPG-14 upgrade", func(t *testing.T) {
		upgradeWixCodes(t, dir, cli)
	})
}

// upgradeWixCodes installs the x64 WiX package and upgrades it by the
// msi-builder package converted without --msi.
func upgradeWixCodes(t *testing.T, dir, cli string) {
	t.Helper()

	folder := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilder Tests")
	data := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderWixCodes")

	if present(folder) || present(data) {
		t.Fatalf("%s or %s exists before the installation; remove what an earlier run left", folder, data)
	}

	writeText(t, filepath.Join(dir, "v2.wxs"), fmt.Sprintf(wixCodesSource, "1.1.0"))

	out := runTool(t, dir, cli, "convert", "--arch", "x64", "-o", "msi.yaml", "v2.wxs")
	t.Log(out)

	if strings.Contains(out, "WiX generated the Guid") {
		t.Errorf("convert left components without a code:\n%s", out)
	}

	older := filepath.Join(dir, "v1-x64.msi")
	newer := filepath.Join(dir, "v2.msi")
	runTool(t, dir, cli, "build", "-o", newer, "msi.yaml")

	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "msiexec", "/x", newer, "/qn", "/norestart").Run()
		_ = exec.CommandContext(context.Background(), "msiexec", "/x", older, "/qn", "/norestart").Run()
		_ = os.RemoveAll(folder)
		_ = os.RemoveAll(data)
		_ = exec.CommandContext(context.Background(), "reg.exe", "delete", wixCodesKey, "/f", "/reg:64").Run()
		_ = exec.CommandContext(context.Background(), "reg.exe", "delete", wixCodesKey, "/f", "/reg:32").Run()
	})

	msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", older)
	msiexecQuiet(t, filepath.Join(dir, "v2.log"), "/i", newer)

	got := make(map[string]bool)
	for _, name := range wixCodesFiles {
		got[name] = present(filepath.Join(folder, "WixCodes", name))
	}

	got["data.txt"] = present(filepath.Join(data, "data.txt"))
	got["HKLM 32-bit InstallDir"] = regHas(t, "32", wixCodesKey, "InstallDir")
	got["HKLM 64-bit default value"] = exec.CommandContext(t.Context(), "reg.exe", "query", wixCodesKey, "/ve", "/reg:64").Run() == nil

	t.Logf("after the upgrade: %v", got)

	for name, kept := range got {
		if !kept {
			t.Errorf("%s is gone after the upgrade", name)
		}
	}

	msiexecQuiet(t, filepath.Join(dir, "x2.log"), "/x", newer)

	if present(filepath.Join(folder, "WixCodes")) || present(filepath.Join(data, "data.txt")) {
		t.Error("v2's uninstall left files behind")
	}
}

// convertCodes converts a source with convert's arguments and returns the
// component codes of the YAML file, sorted.
func convertCodes(t *testing.T, dir, cli string, args ...string) []string {
	t.Helper()

	yaml := runTool(t, dir, cli, append([]string{"convert"}, args...)...)
	codes := componentCode.FindAllString(yaml, -1)
	slices.Sort(codes)

	return codes
}

// writeWixCodesPayload writes the files of the UPG-14 source below dir.
func writeWixCodesPayload(t *testing.T, dir string) {
	t.Helper()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for name, data := range map[string]string{
		"App.EXE": string(exe), "Tool.dll": "tool\n", "a.txt": "a\n",
		`payload\readme.txt`: "readme\n", `payload\sub\deep.txt`: "deep\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		writeText(t, path, data)
	}
}
