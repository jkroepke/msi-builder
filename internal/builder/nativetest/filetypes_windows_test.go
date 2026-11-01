//go:build windows

package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

const fileTypesConfig = `schema: msi-builder/v1
product: { name: MsiBuilderFileTypes, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 6b0e3c9d-2f4a-4e81-b7c5-1d9a8e3f5b20 }
package: { platform: x64 }
components:
  - id: App
    files:
      - source: app.exe
        fileTypes:
          - progId: MsiBuilderTest.Document
            description: msi-builder test document
            iconIndex: 0
            extensions:
              - { extension: msibtdoc, contentType: application/x-msibt }
              - extension: msibtkeep
            verbs:
              - { id: open, text: '&Open' }
              - { id: print, arguments: '/p "%1"' }
          - progId: MsiBuilderTest.Other
            extensions: [{ extension: msibtoth, openWithOnly: true }]
`

// componentFileTypesConfig gives each extension a component and an
// optional feature of its own, with the icon from an .ico file (REG-09).
const componentFileTypesConfig = `schema: msi-builder/v1
product: { name: MsiBuilderFileTypeFeatures, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 0d6f2a71-5c3b-4e9a-8f14-7b2e9c6d4a35 }
package: { platform: x64 }
components:
  - id: App
    files: [{ source: app.exe }, { source: doc.ico }]
  - id: AssocDoc
    fileTypes:
      - { progId: MsiBuilderTest.FeatureDoc, file: app.exe, description: msi-builder feature document, iconFile: doc.ico, extensions: [{ extension: msibtfdoc }] }
  - id: AssocPlain
    fileTypes:
      - { progId: MsiBuilderTest.FeaturePlain, file: app.exe, iconFile: doc.ico, iconIndex: 1, extensions: [{ extension: msibtfpln }] }
features:
  - id: Main
    components: [App]
    features:
      - { id: Doc, components: [AssocDoc] }
      - { id: Plain, default: off, components: [AssocPlain] }
`

// buildFileTypes writes the file type test package.
func buildFileTypes(t *testing.T) string {
	t.Helper()

	return buildFileTypePackage(t, fileTypesConfig, "filetypes.msi")
}

// buildFileTypePackage writes a package from a file type test
// configuration, with app.exe and doc.ico as its inputs.
func buildFileTypePackage(t *testing.T, text, name string) string {
	t.Helper()

	dir := t.TempDir()
	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	if err := os.WriteFile(filepath.Join(dir, "app.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	// An icon directory header without images is enough for the registry.
	if err := os.WriteFile(filepath.Join(dir, "doc.ico"), []byte{0, 0, 1, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(text), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = dir

	res, err := builder.Build(cfg, builder.Options{BuildTime: exampletest.BuildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, res.Package, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// regDefault returns the REG_SZ default value of a key in the 64-bit view,
// or "<absent>" when the key is missing. For a key without a default it
// returns reg.exe's localized "(value not set)" (hasDefault tells them
// apart). The value's name is localised, so the line is found by its type.
func regDefault(t *testing.T, key string) string {
	t.Helper()

	for line := range strings.Lines(reg(t, "query", key, "/ve", "/reg:64")) {
		if _, data, ok := strings.Cut(line, "REG_SZ"); ok {
			return strings.TrimSpace(data)
		}
	}

	return "<absent>"
}

// hasDefault reports whether a key in the 64-bit view has a default value.
// reg.exe prints a localized "(value not set)" REG_SZ line for a key
// without one, so the value is read through .NET instead.
func hasDefault(t *testing.T, key string) bool {
	t.Helper()

	path := "Registry::" + strings.Replace(key, `HKLM\`, `HKEY_LOCAL_MACHINE\`, 1)
	script := `$k = Get-Item -LiteralPath '` + path + `' -ErrorAction SilentlyContinue; ` +
		`if ($k -and $null -ne $k.GetValue('')) { 'set' } else { 'unset' }`

	out, err := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}

	return strings.TrimSpace(string(out)) == "set"
}

// regNamed returns the REG_SZ value of a key in the 64-bit view whose name
// may hold spaces, or "<absent>".
func regNamed(t *testing.T, key, name string) string {
	t.Helper()

	for line := range strings.Lines(reg(t, "query", key, "/v", name, "/reg:64")) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), name)
		if !ok {
			continue
		}

		if _, data, ok := strings.Cut(rest, "REG_SZ"); ok {
			return strings.TrimSpace(data)
		}
	}

	return "<absent>"
}

// TestMsiexecFileTypes installs a package with file types: the per-machine
// HKCR rows land in HKLM\Software\Classes, cmd's assoc and ftype resolve
// the extension and the open command to the installed file, each ProgID is
// an empty REG_SZ in its extensions' OpenWithProgids (REG-04), an
// openWithOnly extension gets no default. Uninstall removes the values the
// package wrote: the ProgIDs, their OpenWithProgids entries, the extension
// defaults and Content Type, a default that another program set after the
// install included (the value name is the same). Other programs' values and
// subkeys in an extension key stay (REG-07).
func TestMsiexecFileTypes(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	path := buildFileTypes(t)
	exe := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderFileTypes", "app.exe")
	classes := `HKLM\SOFTWARE\Classes\`

	cleanup := func() {
		ctx := context.Background()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+".msibtdoc", "/f", "/reg:64").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+"MsiBuilderTest.Document", "/f", "/reg:64").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+".msibtoth", "/f", "/reg:64").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+".msibtkeep", "/f", "/reg:64").Run()
		_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+"MsiBuilderTest.Other", "/f", "/reg:64").Run()
	}

	cleanup()
	t.Cleanup(cleanup)

	// Another program already lists itself under .msibtoth, and keeps a
	// value and subkeys of its own in .msibtkeep.
	regSet(t, "64", classes+`.msibtoth\OpenWithProgids`, "MsiBuilderTest.Foreign", "")
	regSet(t, "64", classes+".msibtkeep", "PerceivedType", "text")
	regSet(t, "64", classes+`.msibtkeep\ShellNew`, "NullFile", "")
	regSet(t, "64", classes+`.msibtkeep\OpenWithProgids`, "MsiBuilderTest.Foreign", "")

	log := filepath.Join(filepath.Dir(path), "install.log")
	if code := msiexecCode(t, log, "/i", path); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	for key, want := range map[string]string{
		".msibtdoc":                                   "MsiBuilderTest.Document",
		"MsiBuilderTest.Document":                     "msi-builder test document",
		`MsiBuilderTest.Document\DefaultIcon`:         `"` + exe + `",0`,
		`MsiBuilderTest.Document\shell\open`:          "&Open",
		`MsiBuilderTest.Document\shell\open\command`:  `"` + exe + `" "%1"`,
		`MsiBuilderTest.Document\shell\print\command`: `"` + exe + `" /p "%1"`,
		`MsiBuilderTest.Other\shell\open\command`:     `"` + exe + `" "%1"`,
	} {
		if got := regDefault(t, classes+key); !strings.EqualFold(got, want) {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if got := regNamed(t, classes+".msibtdoc", "Content Type"); got != "application/x-msibt" {
		t.Errorf("Content Type = %q", got)
	}

	// A Registry row with a Name and a Null Value (REG-04).
	for key, name := range map[string]string{
		`.msibtdoc\OpenWithProgids`: "MsiBuilderTest.Document",
		`.msibtoth\OpenWithProgids`: "MsiBuilderTest.Other",
	} {
		if got := regNamed(t, classes+key, name); got != "" {
			t.Errorf("%s %s = %q, want an empty REG_SZ", key, name, got)
		}
	}

	// A key without a default lists no REG_SZ line without /ve; with /ve,
	// reg prints a localized "(value not set)".
	if text := reg(t, "query", classes+".msibtoth", "/reg:64"); strings.Contains(text, "REG_SZ") {
		t.Errorf(".msibtoth has a value, want none for openWithOnly:\n%s", text)
	}

	// cmd reads HKEY_CLASSES_ROOT, the merged view Explorer uses.
	// The control for the checks after the uninstall.
	if !hasDefault(t, classes+".msibtdoc") {
		t.Error("hasDefault does not see the .msibtdoc default after the install")
	}

	out, _ := exec.CommandContext(t.Context(), "cmd.exe", "/c", "assoc .msibtdoc && ftype MsiBuilderTest.Document").CombinedOutput()
	if text := string(out); !strings.Contains(text, ".msibtdoc=MsiBuilderTest.Document") ||
		!strings.Contains(strings.ToLower(text), strings.ToLower(`MsiBuilderTest.Document="`+exe+`" "%1"`)) {
		t.Errorf("assoc/ftype:\n%s", text)
	}

	// Another program takes .msibtdoc over after the install.
	if out, err := exec.CommandContext(t.Context(), "reg.exe", "add", classes+".msibtdoc", "/ve", "/d", "MsiBuilderTest.Foreign", "/f", "/reg:64").CombinedOutput(); err != nil {
		t.Fatalf("reg add: %v\n%s", err, out)
	}

	log = filepath.Join(filepath.Dir(path), "uninstall.log")
	if code := msiexecCode(t, log, "/x", path); code != 0 {
		t.Fatalf("msiexec /x returned %d\n%s", code, readLog(log))
	}

	for _, key := range []string{"MsiBuilderTest.Document", "MsiBuilderTest.Other", `.msibtdoc\OpenWithProgids`} {
		if regHas(t, "64", classes+key, "") {
			t.Errorf("%s is still there after the uninstall", key)
		}
	}

	// The defaults go, the one another program set after the install too;
	// other programs' values and subkeys stay (REG-07).
	for _, key := range []string{".msibtdoc", ".msibtkeep"} {
		if hasDefault(t, classes+key) {
			t.Errorf("%s = %q after the uninstall, want no default", key, regDefault(t, classes+key))
		}
	}

	if got := regNamed(t, classes+".msibtdoc", "Content Type"); got != "<absent>" {
		t.Errorf("Content Type = %q after the uninstall, want it removed", got)
	}

	if regHas(t, "64", classes+`.msibtkeep\OpenWithProgids`, "MsiBuilderTest.Document") {
		t.Error(`.msibtkeep\OpenWithProgids still lists MsiBuilderTest.Document after the uninstall`)
	}

	if got := regNamed(t, classes+".msibtkeep", "PerceivedType"); got != "text" {
		t.Errorf(".msibtkeep PerceivedType = %q after the uninstall, want another program's value kept", got)
	}

	for key, name := range map[string]string{`.msibtkeep\ShellNew`: "NullFile", `.msibtkeep\OpenWithProgids`: "MsiBuilderTest.Foreign"} {
		if !regHas(t, "64", classes+key, name) {
			t.Errorf("%s %s is gone after the uninstall, want another program's value kept", key, name)
		}
	}

	out, _ = exec.CommandContext(t.Context(), "cmd.exe", "/c", "ftype MsiBuilderTest.Document").CombinedOutput()
	t.Logf("ftype after the uninstall: %s", strings.TrimSpace(string(out)))

	if regHas(t, "64", classes+`.msibtoth\OpenWithProgids`, "MsiBuilderTest.Other") ||
		!regHas(t, "64", classes+`.msibtoth\OpenWithProgids`, "MsiBuilderTest.Foreign") {
		t.Errorf("uninstall left .msibtoth\\OpenWithProgids as:\n%s", reg(t, "query", classes+`.msibtoth\OpenWithProgids`, "/reg:64"))
	}
}

// TestICEFileTypes runs the full ICE suite against the file type test
// package.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEFileTypes(t *testing.T) {
	cub := iceCube(t)
	requireCleanICE(t, buildFileTypes(t), cub)
}

// TestMsiexecComponentFileTypes installs file types that components list
// for a file of another component, each in an optional feature, with the
// icon from an .ico file (REG-09). Only the selected features' file types
// are registered; adding and removing a feature later adds and removes its
// rows alone, and the uninstall removes the rest. The command and the icon
// name the files by folder and name, because the file is in another
// feature (ICE69), and the shell resolves the extension's icon to the .ico.
func TestMsiexecComponentFileTypes(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	path := buildFileTypePackage(t, componentFileTypesConfig, "filetypefeatures.msi")
	dir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderFileTypeFeatures")
	exe, ico := filepath.Join(dir, "app.exe"), filepath.Join(dir, "doc.ico")
	classes := `HKLM\SOFTWARE\Classes\`
	keys := []string{".msibtfdoc", ".msibtfpln", "MsiBuilderTest.FeatureDoc", "MsiBuilderTest.FeaturePlain"}

	cleanup := func() {
		ctx := context.Background()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn").Run()

		for _, key := range keys {
			_ = exec.CommandContext(ctx, "reg.exe", "delete", classes+key, "/f", "/reg:64").Run() //nolint:gosec // test-controlled keys
		}
	}

	cleanup()
	t.Cleanup(cleanup)

	run := func(name string, args ...string) {
		t.Helper()

		log := filepath.Join(filepath.Dir(path), name+".log")
		if code := msiexecCode(t, log, args...); code != 0 {
			t.Fatalf("msiexec %v returned %d\n%s", args, code, readLog(log))
		}
	}

	doc := map[string]string{
		".msibtfdoc":                                   "MsiBuilderTest.FeatureDoc",
		"MsiBuilderTest.FeatureDoc":                    "msi-builder feature document",
		`MsiBuilderTest.FeatureDoc\DefaultIcon`:        `"` + ico + `",0`,
		`MsiBuilderTest.FeatureDoc\shell\open\command`: `"` + exe + `" "%1"`,
	}
	plain := map[string]string{
		".msibtfpln": "MsiBuilderTest.FeaturePlain",
		`MsiBuilderTest.FeaturePlain\DefaultIcon`:        `"` + ico + `",1`,
		`MsiBuilderTest.FeaturePlain\shell\open\command`: `"` + exe + `" "%1"`,
	}

	expect := func(when string, want map[string]string, present bool) {
		t.Helper()

		for key, value := range want {
			got := regDefault(t, classes+key)
			if present && !strings.EqualFold(got, value) {
				t.Errorf("%s: %s = %q, want %q", when, key, got, value)
			}

			if !present && regHas(t, "64", classes+key, "") {
				t.Errorf("%s: %s = %q, want no key", when, key, got)
			}
		}
	}

	run("install", "/i", path)
	expect("install", doc, true)
	expect("install", plain, false)

	if got := assocDefaultIcon(t, ".msibtfdoc"); !strings.EqualFold(got, `"`+ico+`",0`) {
		t.Errorf("AssocQueryString DEFAULTICON .msibtfdoc = %q, want the .ico", got)
	}

	run("addlocal", "/i", path, "ADDLOCAL=Plain")
	expect("ADDLOCAL=Plain", doc, true)
	expect("ADDLOCAL=Plain", plain, true)

	run("remove", "/i", path, "REMOVE=Plain")
	expect("REMOVE=Plain", doc, true)
	expect("REMOVE=Plain", plain, false)

	run("uninstall", "/x", path)
	expect("uninstall", doc, false)
	expect("uninstall", plain, false)
}

// assocDefaultIcon returns what the shell's AssocQueryString reports as the
// DefaultIcon of an extension, or "<error ...>".
func assocDefaultIcon(t *testing.T, ext string) string {
	t.Helper()

	const (
		assocStrDefaultIcon = 15
		bufLen              = 1024
	)

	proc := syscall.NewLazyDLL("shlwapi.dll").NewProc("AssocQueryStringW")
	buf := make([]uint16, bufLen)
	size := uint32(bufLen)

	extPtr, err := syscall.UTF16PtrFromString(ext)
	if err != nil {
		t.Fatal(err)
	}

	hr, _, _ := proc.Call(0, assocStrDefaultIcon, uintptr(unsafe.Pointer(extPtr)), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if hr != 0 {
		return fmt.Sprintf("<error %#x>", hr)
	}

	return syscall.UTF16ToString(buf)
}

// TestICEComponentFileTypes runs the full ICE suite against the package
// with file types in components and features of their own.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEComponentFileTypes(t *testing.T) {
	cub := iceCube(t)

	// Per user, the components are below LocalAppDataFolder and keyed on
	// their HKCU\Software\Classes values (ICE38).
	for scope, text := range map[string]string{
		"perMachine": componentFileTypesConfig,
		"perUser":    strings.Replace(componentFileTypesConfig, "platform: x64 }", "platform: x64, scope: perUser }", 1),
	} {
		t.Run(scope, func(t *testing.T) {
			requireCleanICE(t, buildFileTypePackage(t, text, "filetypefeatures-"+scope+".msi"), cub)
		})
	}
}
