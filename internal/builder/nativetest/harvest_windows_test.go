package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// harvestSource is the WiX source of the UPG-12 product: a Files glob
// harvests dist, so WiX gives each file a component and code of its own,
// and the old version is removed after the new one is installed.
const harvestSource = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="MsiBuilderHarvest" Manufacturer="msi-builder tests" Version="%s" UpgradeCode="5e3a9c17-2b6d-4f80-9a4e-7c1d3b5f8e02" Scope="perMachine">
    <MajorUpgrade Schedule="afterInstallExecute" DowngradeErrorMessage="A newer version is installed." />
    <MediaTemplate EmbedCab="yes" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="MsiBuilderHarvest" />
    </StandardDirectory>
    <Files Include="dist\**" Directory="INSTALLFOLDER" />
  </Package>
</Wix>
`

// harvestFiles are the files of dist in both versions, below the install
// folder.
var harvestFiles = []string{"app.exe", "readme.txt", `docs\guide.md`, `docs\api\index.md`} //nolint:gochecknoglobals // test table

// TestMsiexecHarvestedComponentCodes upgrades a package that WiX built from
// a Files glob by the package msi-builder builds from the converted source
// (UPG-12). With --msi the folder source keeps the code WiX gave each file
// in guids, and the late removal of the WiX package keeps every file;
// without it the files get other codes, and the removal deletes them
// (UPG-11). The WiX package needs wix.exe on PATH (WiX 5, which has Files);
// the test skips without it unless MSI_BUILDER_WIX_REQUIRED is set.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecHarvestedComponentCodes(t *testing.T) {
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
	folder := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderHarvest")

	for _, tc := range []struct {
		name string
		msi  bool
		kept bool
	}{
		{"convert --msi", true, true},
		{"convert", false, false},
	} {
		t.Run("UPG-12 "+tc.name, func(t *testing.T) {
			if present(folder) {
				t.Fatalf("%s exists before the installation; remove what an earlier run left", folder)
			}

			dir := t.TempDir()
			writeHarvestDist(t, dir)

			older := filepath.Join(dir, "v1.msi")
			writeText(t, filepath.Join(dir, "v1.wxs"), fmt.Sprintf(harvestSource, "1.0.0"))
			runTool(t, dir, wix, "build", "-arch", "x64", "-o", older, "v1.wxs")

			// Next to dist, convert computes the codes WiX gives the files
			// (UPG-14); without --msi the source is converted from a folder
			// without dist, so that the files get generated codes.
			source := "v2.wxs"
			if !tc.msi {
				source = filepath.Join("elsewhere", source)
			}

			if err := os.MkdirAll(filepath.Join(dir, "elsewhere"), 0o750); err != nil {
				t.Fatal(err)
			}

			writeText(t, filepath.Join(dir, source), fmt.Sprintf(harvestSource, "1.1.0"))

			args := []string{"convert", "-o", "msi.yaml"}
			if tc.msi {
				args = append(args, "--msi", older)
			}

			t.Log(runTool(t, dir, cli, append(args, source)...))

			converted, err := os.ReadFile(filepath.Join(dir, "msi.yaml"))
			if err != nil {
				t.Fatal(err)
			}

			t.Logf("msi.yaml:\n%s", converted)

			if got := strings.Contains(string(converted), "guids:"); got != tc.msi {
				t.Errorf("msi.yaml has guids: %t, want %t", got, tc.msi)
			}

			newer := filepath.Join(dir, "v2.msi")
			runTool(t, dir, cli, "build", "-o", newer, "msi.yaml")

			t.Cleanup(func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", newer, "/qn", "/norestart").Run()
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", older, "/qn", "/norestart").Run()
				_ = os.RemoveAll(folder)
			})

			msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", older)
			msiexecQuiet(t, filepath.Join(dir, "v2.log"), "/i", newer)

			got := make(map[string]bool)
			for _, name := range harvestFiles {
				got[name] = present(filepath.Join(folder, name))
			}

			t.Logf("after the upgrade: %v", got)

			for _, name := range harvestFiles {
				if got[name] != tc.kept {
					t.Errorf("%s present: %t, want %t", name, got[name], tc.kept)
				}
			}

			msiexecQuiet(t, filepath.Join(dir, "x2.log"), "/x", newer)

			// Without the codes the late removal already deleted the files;
			// what is left of the folders then is not what this case tests.
			if tc.kept && present(folder) {
				t.Errorf("v2's uninstall left %s behind", folder)
			}
		})
	}
}

// writeHarvestDist writes the files of dist below dir.
func writeHarvestDist(t *testing.T, dir string) {
	t.Helper()

	for _, name := range harvestFiles {
		data := []byte(name + "\n")
		if name == "app.exe" {
			data = testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
		}

		path := filepath.Join(dir, "dist", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		writeText(t, path, string(data))
	}
}

// writeText writes data to the file path.
func writeText(t *testing.T, path, data string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildCLI builds msi-builder into a temporary folder.
func buildCLI(t *testing.T) string {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "msi-builder.exe")
	runTool(t, "", "go", "build", "-o", exe, "github.com/jkroepke/msi-builder/cmd/msi-builder")

	return exe
}

// runTool runs a program in dir and returns its output; it fails the test
// when the program fails.
func runTool(t *testing.T, dir, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(name), strings.Join(args, " "), err, out)
	}

	return string(out)
}
