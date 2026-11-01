//go:build windows

package nativetest_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// rememberConfig is a package with installDir.remember. ${SCOPE},
// ${PLATFORM}, ${PARENT} and ${UI} choose the variant.
const rememberConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRemember, manufacturer: msi-builder tests, version: {{ .VERSION | quote }}, upgradeCode: 804ffd51-5f96-4893-a2cd-9b7891066cf4 }
package: { platform: ${PLATFORM}, scope: ${SCOPE} }
installDir: { remember: true${PARENT} }
${UI}
components:
  - files:
      - source: {{ .SOURCE | quote }}
        name: tool.exe
        version: {{ .FILEVERSION | quote }}
`

const (
	rememberName = "MsiBuilderRemember"
	rememberKey  = `Software\msi-builder tests\MsiBuilderRemember`
)

// rememberCase is one variant of the remember package.
type rememberCase struct {
	name, scope, platform, parent, ui string
	// args move a dual package into the other context.
	args []string
	// root and view name where the package writes the remembered folder.
	root, view string
}

// x86 reports whether the case installs into the 32-bit Program Files.
func (tc rememberCase) x86() bool {
	return tc.platform == "x86" || tc.parent == "ProgramFilesFolder"
}

// defaultDir is the folder the package installs to without a remembered one.
func (tc rememberCase) defaultDir() string {
	switch {
	case tc.root == "HKCU":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", rememberName)
	case tc.x86():
		return filepath.Join(os.Getenv("ProgramFiles(x86)"), rememberName)
	default:
		return filepath.Join(os.Getenv("ProgramFiles"), rememberName)
	}
}

// skipOtherArch runs arm64 packages only on Windows on Arm, and the others
// only on x64: msiexec under emulation is slow and tests Windows, not the
// package.
func (tc rememberCase) skipOtherArch(t *testing.T) {
	t.Helper()

	if (tc.platform == "arm64") != (runtime.GOARCH == "arm64") {
		t.Skipf("a %s package is installed on its own runner", tc.platform)
	}
}

// buildRemember builds version of the remember package into dir.
func buildRemember(t *testing.T, dir string, tc rememberCase, version string) string {
	t.Helper()

	parent := ""
	if tc.parent != "" {
		parent = ", parent: " + tc.parent
	}

	replace := []string{"${SCOPE}", tc.scope, "${PLATFORM}", tc.platform, "${PARENT}", parent, "${UI}", tc.ui}
	if tc.platform == "x86" {
		// A 32-bit package gets the 32-bit PING.EXE.
		replace = append(replace, "{{ .SOURCE | quote }}", filepath.Join(os.Getenv("SystemRoot"), "SysWOW64", "PING.EXE"))
	}

	versionDir := filepath.Join(dir, version)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path, _ := buildFullUIFrom(t, versionDir, strings.NewReplacer(replace...).Replace(rememberConfig), version, "auto")

	return path
}

// rememberedValue returns the remembered folder in a root and view, or
// "<absent>".
func rememberedValue(t *testing.T, root, view string) string {
	t.Helper()

	for line := range strings.Lines(reg(t, "query", root+`\`+rememberKey, "/v", "InstallDir", "/reg:"+view)) {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.EqualFold(fields[0], "InstallDir") {
			return strings.Join(fields[2:], " ")
		}
	}

	return "<absent>"
}

// cleanRemember removes what a failed run of the remember tests can leave.
func cleanRemember(t *testing.T, packages ...string) {
	t.Helper()

	for _, pkg := range packages {
		_, _ = runMsiexec([]string{"/x", pkg, "/qn"}, nil)
	}

	for _, root := range []string{"HKLM", "HKCU"} {
		for _, view := range []string{"64", "32"} {
			_ = reg(t, "delete", root+`\`+rememberKey, "/f", "/reg:"+view)
		}
	}

	_ = os.RemoveAll(rememberRoot())
}

// rememberRoot holds the custom install folders of the remember tests.
func rememberRoot() string {
	return filepath.Join(os.Getenv("SystemDrive")+`\`, rememberName)
}

// rememberCases are the variants the upgrade tests install.
func rememberCases() []rememberCase {
	return []rememberCase{
		{name: "perMachine x64", scope: "perMachine", platform: "x64", root: "HKLM", view: "64"},
		{name: "perUser x64", scope: "perUser", platform: "x64", root: "HKCU", view: "64"},
		{name: "perMachine x86", scope: "perMachine", platform: "x86", root: "HKLM", view: "32"},
		{name: "perMachine x64 in ProgramFilesFolder", scope: "perMachine", platform: "x64", parent: "ProgramFilesFolder", root: "HKLM", view: "32"},
		{name: "perMachine arm64", scope: "perMachine", platform: "arm64", root: "HKLM", view: "64"},
		{name: "perMachineOrUser per machine", scope: "perMachineOrUser", platform: "x64", root: "HKLM", view: "64"},
		{name: "perMachineOrUser per user", scope: "perMachineOrUser", platform: "x64", args: []string{"MSIINSTALLPERUSER=1"}, root: "HKCU", view: "64"},
		{name: "perUserOrMachine per user", scope: "perUserOrMachine", platform: "x64", root: "HKCU", view: "64"},
		{name: "perUserOrMachine per machine", scope: "perUserOrMachine", platform: "x64", args: []string{`MSIINSTALLPERUSER=""`}, root: "HKLM", view: "64"},
	}
}

// TestMsiexecRememberInstallDir installs version 1 of each variant into a
// custom folder and upgrades to version 2 without INSTALLDIR (SEARCH-06):
// the package writes the folder to the registry root and view of its
// scope, platform and parent folder, and the upgrade goes back to it.
func TestMsiexecRememberInstallDir(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	for _, tc := range rememberCases() { //nolint:paralleltest // msiexec runs one installation at a time
		t.Run(tc.name, func(t *testing.T) {
			tc.skipOtherArch(t)

			dir := t.TempDir()
			oldPkg, newPkg := buildRemember(t, dir, tc, "1.0.0"), buildRemember(t, dir, tc, "2.0.0")
			custom := filepath.Join(rememberRoot(), "custom") + `\`

			defer cleanRemember(t, newPkg, oldPkg)

			cleanRemember(t)

			log1 := filepath.Join(dir, "v1.log")
			msiexecQuiet(t, log1, append([]string{"/i", oldPkg, "INSTALLDIR=" + custom}, tc.args...)...)

			other := map[string]string{"64": "32", "32": "64"}[tc.view]
			stored, elsewhere := rememberedValue(t, tc.root, tc.view), rememberedValue(t, tc.root, other)

			log2 := filepath.Join(dir, "v2.log")
			msiexecQuiet(t, log2, append([]string{"/i", newPkg}, tc.args...)...)

			props := serverProperties(readLog(log2))
			t.Logf("SEARCH-06 %s: v1 stored %q in %s view %s (%q in view %s); v2 INSTALLDIR=%q, ALLUSERS=%q, in custom=%v, in default=%v",
				tc.name, stored, tc.root, tc.view, elsewhere, other, props["INSTALLDIR"], props["ALLUSERS"],
				exists(custom+"tool.exe"), exists(filepath.Join(tc.defaultDir(), "tool.exe")))

			if !strings.EqualFold(stored, custom) {
				t.Errorf("v1 stored %q in %s view %s, want %q", stored, tc.root, tc.view, custom)
			}

			// HKCU\Software is shared by both views; HKLM\Software is not.
			if tc.root == "HKLM" && elsewhere != "<absent>" {
				t.Errorf("v1 also stored %q in view %s", elsewhere, other)
			}

			if !exists(custom + "tool.exe") {
				t.Errorf("v2 did not install into the remembered folder %s\n%s", custom,
					excerpt(readLog(log2), "INSTALLDIR", "AppSearch", "RegLocator", "RestoreInstallDir"))
			}

			if exists(filepath.Join(tc.defaultDir(), "tool.exe")) {
				t.Errorf("v2 installed into the default folder %s", tc.defaultDir())
			}
		})
	}
}

// missingDrive returns a drive letter that is not mounted.
func missingDrive(t *testing.T) string {
	t.Helper()

	for letter := 'Z'; letter > 'D'; letter-- {
		drive := string(letter) + `:\`
		if _, err := os.Stat(drive); err != nil {
			return drive
		}
	}

	t.Skip("every drive letter is in use")

	return ""
}

// TestMsiexecRememberMissingFolder installs the package while the registry
// remembers a folder that does not exist (SEARCH-07): one on the system
// drive and one on a drive that is not mounted, and an existing one for
// control. The directory search finds only the existing folder, so the
// others install into the default folder; a raw search applied the folder
// on the missing drive, and the install failed with 1327 and 1603.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRememberMissingFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	tc := rememberCase{scope: "perMachine", platform: "x64", root: "HKLM", view: "64"}
	tc.skipOtherArch(t)

	dir := t.TempDir()
	pkg := buildRemember(t, dir, tc, "1.0.0")

	for _, folder := range []struct {
		name, path string
		found      bool
	}{
		{"existing", filepath.Join(rememberRoot(), "existing") + `\`, true},
		{"missing", filepath.Join(rememberRoot(), "missing") + `\`, false},
		{"missing drive", missingDrive(t) + rememberName + `\`, false},
	} {
		t.Run(folder.name, func(t *testing.T) {
			defer cleanRemember(t, pkg)

			cleanRemember(t)

			if folder.found {
				if err := os.MkdirAll(folder.path, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			regSet(t, tc.view, tc.root+`\`+rememberKey, "InstallDir", folder.path)

			log := filepath.Join(dir, strings.ReplaceAll(folder.name, " ", "-")+".log")
			msiexecQuiet(t, log, "/i", pkg)

			props := serverProperties(readLog(log))
			t.Logf("SEARCH-07 %s folder %s: INSTALLDIR_REMEMBERED=%q, INSTALLDIR=%q", folder.name, folder.path,
				props["INSTALLDIR_REMEMBERED"], props["INSTALLDIR"])

			want := tc.defaultDir() + `\`
			if folder.found {
				want = folder.path
			}

			if !exists(want + "tool.exe") {
				t.Errorf("tool.exe is not in %s", want)
			}
		})
	}
}

// TestFullUIRememberInstallDir installs version 1 silently into a custom
// folder and upgrades to version 2 through the wizard (UI-08): the UI
// sequence restores the remembered folder, the install folder page shows
// it, and the client passes it to the server. The dual package also shows
// the scope page, whose Next sets the folder of the chosen context.
func TestFullUIRememberInstallDir(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	for _, tc := range []rememberCase{ //nolint:paralleltest // msiexec runs one installation at a time
		{name: "welcome only", scope: "perMachine", platform: "x64", ui: "ui: { enabled: true }", root: "HKLM", view: "64"},
		{name: "install folder page", scope: "perMachine", platform: "x64", ui: "ui: { enabled: true, chooseDirectory: true }", root: "HKLM", view: "64"},
		{name: "scope page", scope: "perMachineOrUser", platform: "x64", ui: "ui: { enabled: true, chooseDirectory: true }", root: "HKLM", view: "64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			oldPkg, newPkg := buildRemember(t, dir, tc, "1.0.0"), buildRemember(t, dir, tc, "2.0.0")
			custom := filepath.Join(rememberRoot(), "custom") + `\`

			defer cleanRemember(t, newPkg, oldPkg)

			cleanRemember(t)

			msiexecQuiet(t, filepath.Join(dir, "v1.log"), "/i", oldPkg, "INSTALLDIR="+custom)

			log := filepath.Join(dir, "v2.log")
			exit, seen := fullUI(t, dir, "/i", newPkg, "/l*v", log)
			text := readLog(log)

			t.Logf("UI-08 %s: exit %d, in custom=%v, in default=%v\nUI:\n%s\nlog excerpt:\n%s", tc.name, exit,
				exists(custom+"tool.exe"), exists(filepath.Join(tc.defaultDir(), "tool.exe")), seen,
				excerpt(text, "INSTALLDIR", "RestoreInstallDir", "InstallScope"))

			if exit != 0 {
				t.Fatalf("full UI upgrade: exit %d", exit)
			}

			if !exists(custom + "tool.exe") {
				t.Errorf("the wizard did not install into the remembered folder %s", custom)
			}

			// The page's PathEdit shows up among the window's names.
			if strings.Contains(tc.ui, "chooseDirectory") && !strings.Contains(seen, ": "+custom+" |") && !strings.Contains(seen, "| "+custom+" |") {
				t.Errorf("the install folder page did not show %s", custom)
			}
		})
	}
}

// TestNonAdminRememberInstallDir upgrades the per-user packages as a
// standard user (SEARCH-06): the remembered folder is written to and read
// from the user's own HKCU without elevation.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestNonAdminRememberInstallDir(t *testing.T) {
	user := standardUser(t)

	for _, tc := range []rememberCase{
		{name: "perUser", scope: "perUser", platform: "x64", root: "HKCU", view: "64"},
		{name: "perUserOrMachine", scope: "perUserOrMachine", platform: "x64", root: "HKCU", view: "64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := nonAdminDir(t, "remember-"+tc.name)
			oldPkg, newPkg := buildRemember(t, dir, tc, "1.0.0"), buildRemember(t, dir, tc, "2.0.0")
			custom := filepath.Join(dir, "custom") + `\`

			defer func() {
				_ = user.msiexec(t, filepath.Join(dir, "cleanup2.log"), "/x", newPkg)
				_ = user.msiexec(t, filepath.Join(dir, "cleanup1.log"), "/x", oldPkg)
			}()

			if code := user.msiexec(t, filepath.Join(dir, "v1.log"), "/i", oldPkg, "INSTALLDIR="+custom); code != 0 {
				t.Fatalf("v1 install returned %d\n%s", code, readLog(filepath.Join(dir, "v1.log")))
			}

			log := filepath.Join(dir, "v2.log")
			code := user.msiexec(t, log, "/i", newPkg)
			props := serverProperties(readLog(log))

			t.Logf("SEARCH-06 %s as %s: v2 exit %d, INSTALLDIR_REMEMBERED=%q, INSTALLDIR_REMEMBERED_USER=%q, INSTALLDIR=%q, in custom=%v",
				tc.name, user.name, code, props["INSTALLDIR_REMEMBERED"], props["INSTALLDIR_REMEMBERED_USER"], props["INSTALLDIR"],
				exists(custom+"tool.exe"))

			if code != 0 {
				t.Fatalf("v2 install returned %d\n%s", code, readLog(log))
			}

			if !exists(custom + "tool.exe") {
				t.Errorf("v2 did not install into the remembered folder %s", custom)
			}
		})
	}
}
