//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

// regFolderKey holds the folder paths the SEARCH-09 package reads back.
const regFolderKey = `HKLM\SOFTWARE\MsiBuilderTest\RegFolder`

// regFolderConfig searches folders named in the registry: directory
// registry searches of a folder with and without a trailing backslash, of
// a missing folder and of a file, and folder searches under a registry
// search whose path the test removes afterwards (pathlessNested), the form of
// a path-less DirectorySearch nested in a RegistrySearch.
const regFolderConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRegFolder, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 6f2d4185-ae73-4092-b154-8d3e2f6a0194 }
package: { platform: x64 }
installDir: { name: MsiBuilderRegFolder }
components:
  - files: [{ source: app.exe }]
properties:
  SLASH: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: Slash, type: directory } } }
  NOSLASH: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: NoSlash, type: directory } } }
  MISSING: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: Missing, type: directory } } }
  FILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: File, type: directory } } }
  NESTED_DIR: { value: none, search: { directory: { path: nesteddir, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: NoSlash } } } } }
  NESTED_DIR_MISSING: { value: none, search: { directory: { path: nesteddirmissing, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: Missing } } } } }
  NESTED_RAW: { value: none, search: { directory: { path: nestedraw, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: NoSlash } } } } }
  NESTED_RAW_MISSING: { value: none, search: { directory: { path: nestedrawmissing, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFolder', name: Missing } } } } }
`

// pathlessNested removes the Path of the nested DrLocator rows, so each
// returns the folder its RegLocator parent found, and turns the parents of
// the nestedraw* rows into raw searches (type 2, + 16 for the 64-bit view).
func pathlessNested(t *testing.T, database *msidb.Database) {
	t.Helper()

	drLocator, regLocator := database.Table("DrLocator"), database.Table("RegLocator")
	if drLocator == nil || regLocator == nil {
		t.Fatal("the package has no DrLocator or RegLocator table")
	}

	pathCol, parentCol := drLocator.Column("Path"), drLocator.Column("Parent")
	sigCol, typeCol := regLocator.Column("Signature_"), regLocator.Column("Type")
	edited := 0

	for _, row := range drLocator.Rows {
		path, ok := row[pathCol].(string)
		if !ok || !strings.HasPrefix(path, "nested") {
			continue
		}

		row[pathCol] = nil
		edited++

		if !strings.HasPrefix(path, "nestedraw") {
			continue
		}

		for _, reg := range regLocator.Rows {
			if reg[sigCol] == row[parentCol] {
				reg[typeCol] = 2 | 16
			}
		}
	}

	if edited != 4 {
		t.Fatalf("edited %d nested DrLocator rows, want 4", edited)
	}
}

// TestMsiexecRegistryFolderExists checks SEARCH-09: a directory registry
// search (RegLocator Type 0, + 16 for the 64-bit view) finds a folder named in the registry only if
// it exists, with or without a trailing backslash in the value, and a
// path-less DrLocator under a RegLocator does the same. A file path and
// the raw parent are recorded, not asserted.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRegistryFolderExists(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	ctx := context.Background()
	work := t.TempDir()
	root := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderRegFolder")
	existing := filepath.Join(root, "data")
	missing := filepath.Join(root, "missing")
	marker := filepath.Join(existing, "marker.txt")

	built := buildLocatorPackage(t, mkdir(t, work, "pkg"), "regfolder.msi", regFolderConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})
	pkg := editPackage(t, built, pathlessNested)

	cleanup := func() {
		_ = exec.CommandContext(ctx, "msiexec", "/x", pkg, "/qn").Run()
		_ = os.RemoveAll(root)
		_ = exec.CommandContext(ctx, "reg.exe", "delete", regFolderKey, "/f", "/reg:64").Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	writeFile(t, marker)

	for name, value := range map[string]string{
		"Slash":   existing + `\`,
		"NoSlash": existing,
		"Missing": missing,
		"File":    marker,
	} {
		regSet(t, "64", regFolderKey, name, value)
	}

	log := filepath.Join(work, "install.log")
	if code := msiexecCode(t, log, "/i", pkg); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	props := serverProperties(readLog(log))
	for _, name := range []string{"SLASH", "NOSLASH", "MISSING", "FILE", "NESTED_DIR", "NESTED_DIR_MISSING", "NESTED_RAW", "NESTED_RAW_MISSING"} {
		t.Logf("SEARCH-09 %s = %q", name, props[name])
	}

	for _, name := range []string{"SLASH", "NOSLASH", "NESTED_DIR"} {
		if got := props[name]; !strings.EqualFold(strings.TrimSuffix(got, `\`), existing) {
			t.Errorf("%s = %q, want %s", name, got, existing)
		}
	}

	for _, name := range []string{"MISSING", "NESTED_DIR_MISSING"} {
		if got := props[name]; got != "none" {
			t.Errorf("%s = %q, want none (missing folder)", name, got)
		}
	}
}

// regFileKey holds the file paths the SEARCH-10 package reads back.
const regFileKey = `HKLM\SOFTWARE\MsiBuilderTest\RegFile`

// regFileConfig reads file paths from the registry with type: file, the
// form of a WiX RegistrySearch Type="file", and searches a file and a
// folder below such a search, the form of a nested FileSearch.
const regFileConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRegFile, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 1c7e3a52-9d40-4b6f-8a21-5e3f0d9b7c48 }
package: { platform: x64 }
installDir: { name: MsiBuilderRegFile }
components:
  - files: [{ source: app.exe }]
properties:
  EXISTFILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile, type: file } } }
  MISSINGFILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingFile, type: file } } }
  MISSINGDIR: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingDir, type: file } } }
  FOLDER: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: Folder, type: file } } }
  FOLDERSLASH: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: FolderSlash, type: file } } }
  QUOTED: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: Quoted, type: file } } }
  NOVALUE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: NoSuchValue, type: file } } }
  NESTED_EXIST: { value: none, search: { file: { name: marker.txt, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile, type: file } } } } }
  NESTED_MISSING: { value: none, search: { file: { name: missing.exe, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: MissingFile, type: file } } } } }
  NESTED_SUB: { value: none, search: { directory: { path: sub, under: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\RegFile', name: ExistFile, type: file } } } } }
`

// TestMsiexecRegistryFileFolder checks SEARCH-10: a registry search of
// type file (RegLocator Type 1, + 16 for the 64-bit view, without a
// Signature row) returns the folder that holds the last element of the
// path in the value, quotes removed, only when that folder exists, without
// checking the file; a file search below it checks the file.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRegistryFileFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	ctx := context.Background()
	work := t.TempDir()
	root := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderRegFile")
	data := filepath.Join(root, "data")
	marker := filepath.Join(data, "marker.txt")

	pkg := buildLocatorPackage(t, mkdir(t, work, "pkg"), "regfile.msi", regFileConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})

	cleanup := func() {
		_ = exec.CommandContext(ctx, "msiexec", "/x", pkg, "/qn", "/norestart", "REBOOT=ReallySuppress").Run()
		_ = os.RemoveAll(root)
		_ = exec.CommandContext(ctx, "reg.exe", "delete", regFileKey, "/f", "/reg:64").Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	writeFile(t, marker)

	if err := os.MkdirAll(filepath.Join(data, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{
		"ExistFile":   marker,
		"MissingFile": filepath.Join(data, "missing.exe"),
		"MissingDir":  filepath.Join(root, "missing", "x.exe"),
		"Folder":      data,
		"FolderSlash": data + `\`,
		"Quoted":      `"` + marker + `"`,
	} {
		regSet(t, "64", regFileKey, name, value)
	}

	log := filepath.Join(work, "install.log")
	if code := msiexecCode(t, log, "/i", pkg); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	props := serverProperties(readLog(log))
	want := map[string]string{
		"EXISTFILE":      data + `\`,
		"MISSINGFILE":    data + `\`,
		"MISSINGDIR":     "none",
		"FOLDER":         root + `\`,
		"FOLDERSLASH":    root + `\`,
		"QUOTED":         data + `\`,
		"NOVALUE":        "none",
		"NESTED_EXIST":   marker,
		"NESTED_MISSING": "none",
		"NESTED_SUB":     filepath.Join(data, "sub") + `\`,
	}

	for name, value := range want {
		t.Logf("SEARCH-10 %s = %q", name, props[name])

		if !strings.EqualFold(props[name], value) {
			t.Errorf("%s = %q, want %q", name, props[name], value)
		}
	}
}
