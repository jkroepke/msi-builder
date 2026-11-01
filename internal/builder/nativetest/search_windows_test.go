//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// searchTestKey is the key the registry search test reads.
const searchTestKey = `HKLM\SOFTWARE\MsiBuilderTest\Search`

const searchConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSearch, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 0b8f2c6d-71e4-4a95-8d2b-3e6f9a1c5d70 }
package: { platform: x64 }
installDir: { name: MsiBuilderSearch }
components:
  - files: [{ source: app.exe }]
properties:
  P_STR: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Str } } }
  P_DWORD: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Dword } } }
  P_EXPAND: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Expand } } }
  P_MULTI: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Multi } } }
  P_KEYDEFAULT: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search' } } }
  P_BRACKETS: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Brackets } } }
  P_32: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Str32, bitness: 32 } } }
  P_64MISS: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Str32 } } }
  P_DEFAULT: { value: 'fallback [x] {y} {{ "{{" }}z}}', search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Missing } } }
  P_NODEFAULT: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Missing } } }
  P_CMD: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Str } } }
  P_CMDDEFAULT: { value: fallback, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Missing } } }
  P_DIR: { search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: Dir, type: directory } } }
  P_DIRFILE: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: DirFile, type: directory } } }
  P_DIRMISSING: { value: none, search: { registry: { root: HKLM, key: 'SOFTWARE\MsiBuilderTest\Search', name: DirMissing, type: directory } } }
customActions:
  - id: CondNumber
    setProperty: { name: C_NUMBER, value: "1" }
    schedule: [{ sequence: execute, before: CostInitialize, condition: 'P_DWORD >= 394802' }]
  - id: CondString
    setProperty: { name: C_STRING, value: "1" }
    schedule: [{ sequence: execute, before: CostInitialize, condition: 'P_DWORD >= "#394802"' }]
  - id: CondLonger
    setProperty: { name: C_LONGER, value: "1" }
    schedule: [{ sequence: execute, before: CostInitialize, condition: 'P_DWORD >= "#1000000"' }]
`

// buildSearch writes the registry search test package.
func buildSearch(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	if err := os.WriteFile(filepath.Join(dir, "app.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(searchConfig), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	res, err := builder.Build(cfg, builder.Options{BuildTime: exampletest.BuildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	path := filepath.Join(dir, "search.msi")
	if err := os.WriteFile(path, res.Package, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// serverProperties reads the property dump the execute sequence writes at
// the end of a verbose log ("Property(S): NAME = value").
func serverProperties(log string) map[string]string {
	props := make(map[string]string)

	for line := range strings.Lines(log) {
		_, rest, ok := strings.Cut(line, "Property(S): ")
		if !ok {
			continue
		}

		name, value, ok := strings.Cut(strings.TrimRight(rest, "\r\n"), " = ")
		if ok {
			props[name] = value
		}
	}

	return props
}

// TestMsiexecSearchesRegistry installs a package whose properties search
// the registry (SEARCH-04): value types, the default value of a key, the
// 32-bit view, a missing value with and without a default, command-line
// values, directory searches, and conditions on a DWORD result.
func TestMsiexecSearchesRegistry(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	path := buildSearch(t)
	log := filepath.Join(filepath.Dir(path), "install.log")
	fileDir := t.TempDir()
	file := filepath.Join(fileDir, "file.txt")

	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		ctx := context.Background()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", `HKLM\SOFTWARE\MsiBuilderTest`, "/f", "/reg:64").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", `HKLM\SOFTWARE\MsiBuilderTest`, "/f", "/reg:32").Run()
	}

	cleanup()
	t.Cleanup(cleanup)

	for _, args := range [][]string{
		{"/v", "Str", "/d", "hello", "/reg:64"},
		{"/v", "Dword", "/t", "REG_DWORD", "/d", "394802", "/reg:64"},
		{"/v", "Expand", "/t", "REG_EXPAND_SZ", "/d", `%SystemRoot%\x`, "/reg:64"},
		{"/v", "Multi", "/t", "REG_MULTI_SZ", "/d", `a\0b`, "/reg:64"},
		{"/ve", "/d", "keydefault", "/reg:64"},
		{"/v", "Brackets", "/d", "a[ProductName]b{c}", "/reg:64"},
		{"/v", "Str32", "/d", "from32", "/reg:32"},
		{"/v", "Dir", "/d", os.Getenv("SystemRoot"), "/reg:64"},
		{"/v", "DirFile", "/d", file, "/reg:64"},
		{"/v", "DirMissing", "/d", `C:\MsiBuilderNoSuchFolder`, "/reg:64"},
	} {
		//nolint:gosec // test-controlled arguments
		if out, err := exec.CommandContext(t.Context(), "reg.exe", append([]string{"add", searchTestKey, "/f"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("reg add %v: %v\n%s", args, err, out)
		}
	}

	if code := msiexecCode(t, log, "/i", path, "P_CMD=cmd", "P_CMDDEFAULT=cmd"); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	props := serverProperties(readLog(log))
	root := os.Getenv("SystemRoot")

	// The verbose log shows the NULs of a multi-string as [~]. A DWORD
	// compares as the string "#394802": not with a number, and before
	// "#1000000" (SEARCH-04).
	for name, want := range map[string]string{
		"P_STR":        "hello",
		"P_DWORD":      "#394802",
		"P_MULTI":      "[~]a[~]b[~]",
		"P_KEYDEFAULT": "keydefault",
		"P_BRACKETS":   "a[ProductName]b{c}",
		"P_32":         "from32",
		"P_64MISS":     "none",
		"P_CMD":        "cmd",
		"P_CMDDEFAULT": "cmd",
		"P_DEFAULT":    "fallback [x] {y} {{z}}",
		"P_DIRFILE":    "none",
		"P_DIRMISSING": "none",
		"C_STRING":     "1",
		"C_LONGER":     "1",
	} {
		if got := props[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// %SystemRoot% may differ in case from what the engine expands.
	for name, want := range map[string]string{"P_EXPAND": root + `\x`, "P_DIR": root + `\`} {
		if got := props[name]; !strings.EqualFold(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	for _, name := range []string{"P_NODEFAULT", "C_NUMBER"} {
		if value, ok := props[name]; ok {
			t.Errorf("%s is set to %q, want it unset", name, value)
		}
	}

	if code := msiexecCode(t, filepath.Join(filepath.Dir(path), "uninstall.log"), "/x", path); code != 0 {
		t.Errorf("msiexec /x returned %d", code)
	}
}

// TestICESearch runs the full ICE suite against the registry search test
// package.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICESearch(t *testing.T) {
	cub := iceCube(t)
	requireCleanICE(t, buildSearch(t), cub)
}
