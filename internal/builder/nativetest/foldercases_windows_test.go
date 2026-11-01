//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// folderCasesConfig installs below WindowsVolume, gives the install folder a
// second id with a directory named "." (BINDIR), and removes files from a
// folder that a registry search puts into a property (FILE-16).
const folderCasesConfig = `schema: msi-builder/v1
product: { name: MsiBuilderFolderCases, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 7c4d7ae1-ab4d-48cb-9c60-19b7c170b1da }
package: { platform: x64 }
directories:
  - id: WindowsVolume
    children:
      - id: OPTDIR
        name: MsiBuilderFolderCases
        configurable: true
        children:
          - { id: BINDIR, name: ., configurable: true }
          - { id: LIBDIR, name: lib }
properties:
  CLEANDIR:
    search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\FolderCases', name: CleanDir, type: directory } }
installDir: { id: OPTDIR }
components:
  - id: App
    directory: OPTDIR
    files: [{ source: app.exe }]
    removeFiles:
      - { name: 'msibuilder-foldercases-*.tmp', property: CLEANDIR }
  - id: Tool
    directory: BINDIR
    files: [{ source: tool.exe }]
  - id: Lib
    directory: LIBDIR
    files: [{ source: lib.dll }]
`

// TestMsiexecFolderCases checks three folder cases of real-world packages
// (FILE-16):
//
//   - a Directory row WindowsVolume below TARGETDIR resolves to the Windows
//     volume, not below TARGETDIR or ROOTDRIVE, so its children land in
//     C:\name even with TARGETDIR set elsewhere, and a configurable child
//     can still be moved on the command line;
//   - a directory named "." (DefaultDir ".") is its parent folder and
//     follows it when the parent moves, while setting it on the command
//     line moves its components alone;
//   - RemoveFile rows whose DirProperty is a property, not a directory,
//     remove files in the folder that property holds at uninstall, and do
//     nothing when the property is empty.
func TestMsiexecFolderCases(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	dir := t.TempDir()
	path := buildConfig(t, dir, "foldercases", folderCasesConfig, map[string][]byte{
		"app.exe":  testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
		"tool.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
		"lib.dll":  testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})

	volume := os.Getenv("SystemDrive") + `\`
	def := filepath.Join(volume, "MsiBuilderFolderCases")
	target := filepath.Join(volume, "MsiBuilderFolderCasesTarget")
	custom := filepath.Join(volume, "MsiBuilderFolderCasesCustom")
	bin := filepath.Join(volume, "MsiBuilderFolderCasesBin")
	data := filepath.Join(volume, "MsiBuilderFolderCasesData")
	regKey := `HKLM\SOFTWARE\MsiBuilderTest\FolderCases`

	cleanup := func() {
		ctx := context.Background()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn").Run()

		for _, folder := range []string{def, target, custom, bin, data} {
			_ = os.RemoveAll(folder)
		}

		_ = exec.CommandContext(ctx, "reg.exe", "delete", regKey, "/f").Run()
	}

	cleanup()
	t.Cleanup(cleanup)

	install := func(name string, args ...string) {
		t.Helper()

		log := filepath.Join(dir, name+".log")
		if code := msiexecCode(t, log, append([]string{"/i", path}, args...)...); code != 0 {
			t.Fatalf("msiexec /i %v returned %d\n%s", args, code, readLog(log))
		}
	}

	uninstall := func(name string) string {
		t.Helper()

		log := filepath.Join(dir, name+".log")
		if code := msiexecCode(t, log, "/x", path); code != 0 {
			t.Fatalf("msiexec /x returned %d\n%s", code, readLog(log))
		}

		return readLog(log)
	}

	want := func(when string, files ...string) {
		t.Helper()

		for _, file := range files {
			if !exists(file) {
				t.Errorf("%s: %s is missing", when, file)
			}
		}
	}

	gone := func(when string, files ...string) {
		t.Helper()

		for _, file := range files {
			if exists(file) {
				t.Errorf("%s: %s is still there", when, file)
			}
		}
	}

	// WindowsVolume with TARGETDIR set elsewhere.
	install("windowsvolume", "TARGETDIR="+target+`\`)
	want("TARGETDIR set", filepath.Join(def, "app.exe"), filepath.Join(def, "tool.exe"), filepath.Join(def, "lib", "lib.dll"))
	gone("TARGETDIR set", filepath.Join(target, "MsiBuilderFolderCases"))
	uninstall("windowsvolume-x")
	gone("uninstall", def)

	// The parent moves, and its alias with it.
	install("custom", "OPTDIR="+custom+`\`)
	want("OPTDIR set", filepath.Join(custom, "app.exe"), filepath.Join(custom, "tool.exe"), filepath.Join(custom, "lib", "lib.dll"))
	gone("OPTDIR set", def)
	uninstall("custom-x")
	gone("uninstall", custom)

	// The alias moves alone.
	install("alias", "BINDIR="+bin+`\`)
	want("BINDIR set", filepath.Join(def, "app.exe"), filepath.Join(bin, "tool.exe"), filepath.Join(def, "lib", "lib.dll"))
	gone("BINDIR set", filepath.Join(def, "tool.exe"))
	uninstall("alias-x")
	gone("uninstall", def, bin)

	// RemoveFile rows on a property that a registry search fills.
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // test-controlled arguments
	if out, err := exec.CommandContext(t.Context(), "reg.exe", "add", regKey, "/v", "CleanDir", "/d", data+`\`, "/f").CombinedOutput(); err != nil {
		t.Fatalf("reg add: %v\n%s", err, out)
	}

	writeFile(t, filepath.Join(data, "msibuilder-foldercases-keep.txt"))
	install("property")

	writeFile(t, filepath.Join(data, "msibuilder-foldercases-a.tmp"))
	writeFile(t, filepath.Join(data, "msibuilder-foldercases-b.tmp"))
	uninstall("property-x")
	gone("uninstall", filepath.Join(data, "msibuilder-foldercases-a.tmp"), filepath.Join(data, "msibuilder-foldercases-b.tmp"))
	want("uninstall", filepath.Join(data, "msibuilder-foldercases-keep.txt"))

	// Without the registry value the property is empty: the rows must not
	// fall back to the current folder of either process.
	if out, err := exec.CommandContext(t.Context(), "reg.exe", "delete", regKey, "/f").CombinedOutput(); err != nil {
		t.Fatalf("reg delete: %v\n%s", err, out)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// A name the uninstall pattern matches, in the current folders of
	// both msiexec processes.
	folders := []string{
		filepath.Join(os.Getenv("SystemRoot"), "System32"),
		filepath.Join(os.Getenv("SystemRoot"), "SysWOW64"),
		cwd,
		volume,
	}
	decoys := make([]string, 0, len(folders))

	for _, folder := range folders {
		decoys = append(decoys, filepath.Join(folder, "msibuilder-foldercases-decoy.tmp"))
	}

	t.Cleanup(func() {
		for _, decoy := range decoys {
			_ = os.Remove(decoy)
		}
	})

	for _, decoy := range decoys {
		writeFile(t, decoy)
	}

	install("empty")
	want("install with an empty property", decoys...)

	log := uninstall("empty-x")

	want("uninstall with an empty property", decoys...)

	for line := range strings.SplitSeq(log, "\n") {
		if strings.Contains(line, "CLEANDIR") {
			t.Log(strings.TrimSpace(line))
		}
	}
}

// TestICEFolderCases runs the ICE suite on the FILE-16 package: a
// WindowsVolume row below TARGETDIR, an alias directory and RemoveFile rows
// on a property.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEFolderCases(t *testing.T) {
	cub := iceCube(t)
	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	path := buildConfig(t, t.TempDir(), "foldercases", folderCasesConfig, map[string][]byte{
		"app.exe": exe, "tool.exe": exe, "lib.dll": exe,
	})

	requireCleanICE(t, path, cub)
}
