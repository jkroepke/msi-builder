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

// The component codes of the SEARCH-08 packages: the other product's
// folder-keyed (data), registry-keyed and file-keyed components, and this
// package's folder-keyed and registry-keyed ones.
const (
	compSearchBaseData = "3C9A1E52-7B40-4D6F-8E21-5A0B9C3D7E61"
	compSearchBaseReg  = "3C9A1E52-7B40-4D6F-8E21-5A0B9C3D7E62"
	compSearchBaseFile = "3C9A1E52-7B40-4D6F-8E21-5A0B9C3D7E63"
	compSearchAppDir   = "3C9A1E52-7B40-4D6F-8E21-5A0B9C3D7E64"
	compSearchAppReg   = "3C9A1E52-7B40-4D6F-8E21-5A0B9C3D7E65"
)

const compSearchBaseConfig = `schema: msi-builder/v1
product: { name: MsiBuilderCompSearchBase, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 4d0b2f63-8c51-4e70-9f32-6b1c0d4e8f72 }
package: { platform: x64 }
installDir: { name: MsiBuilderCompSearchBase }
components:
  - guid: ` + compSearchBaseData + `
    directory: data
    createFolder: true
    keyPath: directory
  - guid: ` + compSearchBaseReg + `
    registry: [{ root: HKLM, key: 'SOFTWARE\MsiBuilderTest\CompSearch', name: Base, value: x }]
  - guid: ` + compSearchBaseFile + `
    files: [{ source: base.exe }]
`

// compSearchConfig is the package of the reported data loss: CLEANUP_DIR
// searches its own folder-keyed component and removeFolders removes it on
// uninstall. The other properties search the base product's components
// with each keyPath.
const compSearchConfig = `schema: msi-builder/v1
product: { name: MsiBuilderCompSearch, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 5e1c3074-9d62-4f81-a043-7c2d1e5f9083 }
package: { platform: x64 }
installDir: { name: MsiBuilderCompSearch/App }
components:
  - files: [{ source: app.exe }]
  - id: AppFolder
    guid: ` + compSearchAppDir + `
    createFolder: true
    keyPath: directory
    removeFolders: [{ property: CLEANUP_DIR }]
  - id: AppReg
    guid: ` + compSearchAppReg + `
    registry: [{ root: HKLM, key: 'SOFTWARE\MsiBuilderTest\CompSearch', name: App, value: x }]
properties:
  CLEANUP_DIR: { search: { component: { guid: ` + compSearchAppDir + ` } } }
  O_DIR: { value: none, search: { component: { guid: ` + compSearchBaseData + `, keyPath: directory } } }
  O_DIR_AS_FILE: { value: none, search: { component: { guid: ` + compSearchBaseData + `, keyPath: file } } }
  O_FILE: { value: none, search: { component: { guid: ` + compSearchBaseFile + ` } } }
  O_FILE_AS_DIR: { value: none, search: { component: { guid: ` + compSearchBaseFile + `, keyPath: directory } } }
  O_REG: { value: none, search: { component: { guid: ` + compSearchBaseReg + ` } } }
  O_REG_AS_DIR: { value: none, search: { component: { guid: ` + compSearchBaseReg + `, keyPath: directory } } }
  OWN_REG: { value: none, search: { component: { guid: ` + compSearchAppReg + ` } } }
`

// TestMsiexecComponentSearchFolder checks SEARCH-08: a component search
// for a component keyed on its folder returns that folder with
// CompLocator Type 0 and the folder's parent with Type 1, and removeFolders
// on a search of the package's own folder-keyed component removes only
// that folder, not a file of someone else's next to it.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecComponentSearchFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	ctx := context.Background()
	work := t.TempDir()
	programFiles := os.Getenv("ProgramFiles")
	parent := filepath.Join(programFiles, "MsiBuilderCompSearch")
	app := filepath.Join(parent, "App")
	baseDir := filepath.Join(programFiles, "MsiBuilderCompSearchBase")
	sibling := filepath.Join(parent, "sibling.txt")

	exe := func(version string) []byte {
		return testpe.Build(testpe.Options{FileVersion: version, Languages: []int{1033}})
	}

	base := buildLocatorPackage(t, mkdir(t, work, "base"), "base.msi", compSearchBaseConfig, map[string][]byte{"base.exe": exe("3.0.0.0")})
	pkg := buildLocatorPackage(t, mkdir(t, work, "app"), "app.msi", compSearchConfig, map[string][]byte{"app.exe": exe("1.0.0.0")})

	cleanup := func() {
		_ = exec.CommandContext(ctx, "msiexec", "/x", pkg, "/qn").Run()
		_ = exec.CommandContext(ctx, "msiexec", "/x", base, "/qn").Run()
		_ = os.RemoveAll(parent)
		_ = os.RemoveAll(baseDir)
		_ = exec.CommandContext(ctx, "reg.exe", "delete", `HKLM\SOFTWARE\MsiBuilderTest\CompSearch`, "/f", "/reg:64").Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	if code := msiexecCode(t, filepath.Join(work, "base.log"), "/i", base); code != 0 {
		t.Fatalf("msiexec /i base.msi returned %d", code)
	}

	log := filepath.Join(work, "install.log")
	if code := msiexecCode(t, log, "/i", pkg); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	props := serverProperties(readLog(log))

	for name, want := range map[string]string{
		"O_DIR":         baseDir + `\data\`,
		"O_DIR_AS_FILE": baseDir + `\`,
		"O_FILE":        baseDir + `\`,
		"O_FILE_AS_DIR": "none",
	} {
		if got := props[name]; !strings.EqualFold(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// A registry key path: recorded, not asserted.
	for _, name := range []string{"O_REG", "O_REG_AS_DIR"} {
		t.Logf("SEARCH-08 install %s = %q", name, props[name])
	}

	// The package's own components are not installed yet.
	t.Logf("SEARCH-08 install CLEANUP_DIR = %q, OWN_REG = %q", props["CLEANUP_DIR"], props["OWN_REG"])

	writeFile(t, sibling)
	writeFile(t, filepath.Join(app, `runtime\cache.bin`))

	log = filepath.Join(work, "uninstall.log")
	if code := msiexecCode(t, log, "/x", pkg); code != 0 {
		t.Fatalf("msiexec /x returned %d\n%s", code, readLog(log))
	}

	text := readLog(log)
	props = serverProperties(text)
	t.Logf("removal:\n%s", grepLog(text, "BuilderRemoveFolders: "))
	t.Logf("SEARCH-08 uninstall OWN_REG = %q", props["OWN_REG"])

	if got := props["CLEANUP_DIR"]; !strings.EqualFold(got, app+`\`) {
		t.Errorf("CLEANUP_DIR = %q, want %q", got, app+`\`)
	}

	if exists(app) {
		t.Errorf("%s is still there after the removal", app)
	}

	if !exists(sibling) {
		t.Errorf("%s, a file the package does not own next to the install folder, was removed", sibling)
	}
}

// TestICEComponentSearch runs the full ICE suite against the SEARCH-08
// package.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEComponentSearch(t *testing.T) {
	cub := iceCube(t)
	pkg := buildLocatorPackage(t, t.TempDir(), "app.msi", compSearchConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})
	requireCleanICE(t, pkg, cub)
}
