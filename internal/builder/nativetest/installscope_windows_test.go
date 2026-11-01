//go:build windows

package nativetest_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/ice"
)

// installScopeConfig is a dual-scope package with the wizard's scope and
// install folder pages, a remembered install folder, a Start menu shortcut,
// an HKMU value, an HKCR value, a variable that follows the context
// (ENVVAR-03) and two components that install in one context only, one
// with an HKLM value, an HKLM removal and an event log source, and one
// with an HKCU value. The components are
// transitive, so a repair evaluates their conditions again. ${SCOPE} and
// ${PLATFORM} choose the variant.
const installScopeConfig = `schema: msi-builder/v1
product: { name: MsiBuilderScope, manufacturer: msi-builder tests, version: {{ .VERSION | quote }}, upgradeCode: 7d2c41e8-93a5-4f1b-8e60-2b9d5c7a4f13 }
package: { platform: ${PLATFORM}, scope: ${SCOPE} }
installDir: { remember: true }
ui: { enabled: true, chooseDirectory: true }
components:
  - transitive: true
    files:
      - source: {{ .SOURCE | quote }}
        name: tool.exe
        version: {{ .FILEVERSION | quote }}
        shortcuts: [{ name: MsiBuilderScope }]
    registry:
      - { root: HKMU, key: Software\MsiBuilderTest\Scope, name: Dir, value: "[INSTALLDIR]" }
      - { root: HKCR, key: MsiBuilderTest.Scope, name: Probe, value: x }
    environment:
      - { name: MSIBUILDER_SCOPE_TEST, value: "[INSTALLDIR]", action: append }
  - id: MachineOnly
    directory: INSTALLDIR
    scope: perMachine
    transitive: true
    registry: [{ root: HKLM, key: Software\MsiBuilderTest\ScopeMachine, name: Only, value: machine }]
    removeRegistry: [{ root: HKLM, key: Software\MsiBuilderTest\ScopeMachineOld }]
    eventLog: [{ source: MsiBuilderScopeTest, messageFile: "[System64Folder]EventCreate.exe" }]
  - id: UserOnly
    directory: INSTALLDIR
    scope: perUser
    transitive: true
    registry: [{ root: HKCU, key: Software\MsiBuilderTest\ScopeUser, name: Only, value: user }]
`

// scopeState is where an installation of the scope package left its
// resources: "user", "machine", "both" or "none" for each.
type scopeState struct {
	files, hkmu, hkcr, shortcut, registration, environment, scoped string
}

func where(user, machine bool) string {
	switch {
	case user && machine:
		return "both"
	case user:
		return "user"
	case machine:
		return "machine"
	default:
		return "none"
	}
}

// squash turns a braced GUID into its Installer\Products key name.
func squash(guid string) string {
	hex := strings.ReplaceAll(strings.Trim(guid, "{}"), "-", "")
	reverse := func(s string) string {
		b := []byte(s)
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}

		return string(b)
	}

	var out strings.Builder

	out.WriteString(reverse(hex[0:8]) + reverse(hex[8:12]) + reverse(hex[12:16]))

	for i := 16; i < 32; i += 2 {
		out.WriteString(reverse(hex[i : i+2]))
	}

	return out.String()
}

func installScopeState(t *testing.T, productCode string, x86 bool) scopeState {
	t.Helper()

	machineRoot := os.Getenv("ProgramFiles")
	if x86 {
		machineRoot = os.Getenv("ProgramFiles(x86)")
	}

	userTool := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "MsiBuilderScope", "tool.exe")
	machineTool := filepath.Join(machineRoot, "MsiBuilderScope", "tool.exe")
	menu := `Microsoft\Windows\Start Menu\Programs\MsiBuilderScope.lnk`
	view := "64"

	if x86 {
		view = "32"
	}

	return scopeState{
		files: where(exists(userTool), exists(machineTool)),
		hkmu: where(regHas(t, view, `HKCU\Software\MsiBuilderTest\Scope`, "Dir"),
			regHas(t, view, `HKLM\Software\MsiBuilderTest\Scope`, "Dir")),
		hkcr: where(regHas(t, view, `HKCU\Software\Classes\MsiBuilderTest.Scope`, "Probe"),
			regHas(t, view, `HKLM\Software\Classes\MsiBuilderTest.Scope`, "Probe")),
		shortcut: where(exists(filepath.Join(os.Getenv("APPDATA"), menu)), exists(filepath.Join(os.Getenv("ProgramData"), menu))),
		registration: where(regHas(t, "64", `HKCU\Software\Microsoft\Installer\Products\`+squash(productCode), ""),
			regHas(t, "64", `HKLM\SOFTWARE\Classes\Installer\Products\`+squash(productCode), "")),
		environment: where(regHas(t, "64", `HKCU\Environment`, "MSIBUILDER_SCOPE_TEST"),
			regHas(t, "64", `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "MSIBUILDER_SCOPE_TEST")),
		// The HKCU value of the perUser component and the HKLM value of
		// the perMachine one: one of them, in its own context.
		scoped: where(regHas(t, view, `HKCU\Software\MsiBuilderTest\ScopeUser`, "Only"),
			regHas(t, view, `HKLM\Software\MsiBuilderTest\ScopeMachine`, "Only")),
	}
}

func scopeEverywhere(context string) scopeState {
	return scopeState{
		files: context, hkmu: context, hkcr: context, shortcut: context, registration: context,
		environment: context, scoped: context,
	}
}

// buildInstallScope builds the scope package for a scope and platform.
func buildInstallScope(t *testing.T, dir, scope, platform string) (string, string) {
	t.Helper()

	replace := []string{"${SCOPE}", scope, "${PLATFORM}", platform}
	if platform == "x86" {
		// A 32-bit package gets the 32-bit PING.EXE.
		replace = append(replace, "{{ .SOURCE | quote }}", filepath.Join(os.Getenv("SystemRoot"), "SysWOW64", "PING.EXE"))
	}

	doc := strings.NewReplacer(replace...).Replace(installScopeConfig)
	path, res := buildFullUIFrom(t, dir, doc, "1.0.0", "auto")

	return path, res.ProductCode
}

// TestMsiexecInstallScopes installs the dual-scope packages silently in
// both contexts (INST-02): the context the scope names first, and the
// other one through MSIINSTALLPERUSER on the command line. Files under
// Program Files, HKMU and HKCR values, the Start menu shortcut, the
// product registration, the variable and the components of one context
// all follow the context, a repair keeps them there, and the uninstall
// removes them (ENVVAR-03).
func TestMsiexecInstallScopes(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	for _, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		name, scope, want string
		args              []string
	}{
		{"perUserOrMachine", "perUserOrMachine", "user", nil},
		{"perUserOrMachine to machine", "perUserOrMachine", "machine", []string{`MSIINSTALLPERUSER=""`}},
		{"perMachineOrUser", "perMachineOrUser", "machine", nil},
		{"perMachineOrUser to user", "perMachineOrUser", "user", []string{"MSIINSTALLPERUSER=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, code := buildInstallScope(t, dir, tc.scope, "x64")
			t.Cleanup(func() { _, _ = runMsiexec([]string{"/x", path, "/qn"}, nil) })

			msiexecQuiet(t, filepath.Join(dir, "install.log"), append([]string{"/i", path}, tc.args...)...)

			if got := installScopeState(t, code, false); got != scopeEverywhere(tc.want) {
				t.Errorf("after the install: %+v, want everything in %s", got, tc.want)
			}

			// A repair that reinstalls every component evaluates the
			// transitive components' conditions again, so ALLUSERS must
			// name the installed context then too (ENVVAR-03).
			repairLog := filepath.Join(dir, "repair.log")
			msiexecQuiet(t, repairLog, "/i", path, "REINSTALL=ALL", "REINSTALLMODE=amus")
			t.Logf("ENVVAR-03 %s repair: ALLUSERS %q", tc.name, serverProperties(readLog(repairLog))["ALLUSERS"])

			if got := installScopeState(t, code, false); got != scopeEverywhere(tc.want) {
				t.Errorf("after the repair: %+v, want everything in %s", got, tc.want)
			}

			msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)

			if got := installScopeState(t, code, false); got != scopeEverywhere("none") {
				t.Errorf("after the uninstall: %+v, want nothing", got)
			}
		})
	}
}

// TestFullUIInstallScope installs the dual-scope packages through the
// wizard and switches the context on the scope page. The client resolves
// INSTALLDIR for the context the package opened in and passes it on, so
// the page must move it, or a per-machine install would land in the user's
// profile (INST-02). The x86 variant checks the 32-bit Program Files path
// the page reads from the environment.
func TestFullUIInstallScope(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	for _, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		name, scope, platform, choose, want string
	}{
		{"default per user", "perUserOrMachine", "x64", "", "user"},
		{"switch to all users", "perUserOrMachine", "x64", "^Install for all users", "machine"},
		{"switch to all users x86", "perUserOrMachine", "x86", "^Install for all users", "machine"},
		{"switch to just you", "perMachineOrUser", "x64", "^Install just for you", "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, code := buildInstallScope(t, dir, tc.scope, tc.platform)
			t.Cleanup(func() { _, _ = runMsiexec([]string{"/x", path, "/qn"}, nil) })

			log := filepath.Join(dir, "install.log")
			exit, seen := fullUIChoosing(t, dir, tc.choose, "/i", path, "/l*v", log)
			text := readLog(log)

			if exit != 0 {
				t.Fatalf("full UI install: exit %d\nUI:\n%s\nlog excerpt:\n%s", exit, seen, excerpt(text, "INSTALLDIR", "ALLUSERS", "Error"))
			}

			if tc.choose != "" && !strings.Contains(seen, "CHOSE: ") {
				t.Error("the driver found no scope radio button")
			}

			if got := installScopeState(t, code, tc.platform == "x86"); got != scopeEverywhere(tc.want) {
				t.Errorf("after the install: %+v, want everything in %s\nlog excerpt:\n%s", got, tc.want, excerpt(text, "INSTALLDIR", "InstallScope"))
			}

			t.Logf("INST-02 %s UI:\n%s\nlog excerpt:\n%s", tc.name, seen, excerpt(text, "INSTALLDIR", "InstallScope", "ALLUSERS", "MSIINSTALLPERUSER"))
		})
	}
}

// TestICEInstallScope runs the full ICE suite, ICE105 included, against
// both dual-scope packages, with their machine variable and HKLM value in
// components that install only per machine (ENVVAR-03).
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEInstallScope(t *testing.T) {
	cub := iceCube(t)

	for _, scope := range []string{"perUserOrMachine", "perMachineOrUser"} {
		path, _ := buildInstallScope(t, t.TempDir(), scope, "x64")
		requireCleanICE(t, path, cub)

		findings, err := ice.Run(path, cub)
		if err != nil {
			t.Fatal(err)
		}

		// ICE105 warns about the HKLM Registry and RemoveRegistry rows of
		// the perMachine component in a package that starts per user,
		// never about the machine variable, and is silent in one that
		// starts per machine (ENVVAR-03).
		tables := map[string]int{}

		for _, finding := range findings {
			if finding.ICE == "ICE105" && finding.Type != ice.Info {
				tables[finding.Table]++
				t.Logf("ENVVAR-03 %s: %s", scope, finding)

				if finding.Table != "Registry" && finding.Table != "RemoveRegistry" || finding.Type != ice.Warning {
					t.Errorf("%s: unexpected ICE105 finding %s", scope, finding)
				}
			}
		}

		// The HKLM value and the two rows of the event log source, and
		// the HKLM removal.
		want := map[string]int{}
		if scope == "perUserOrMachine" {
			want = map[string]int{"Registry": 3, "RemoveRegistry": 1}
		}

		if !maps.Equal(tables, want) {
			t.Errorf("%s: ICE105 warnings per table %v, want %v", scope, tables, want)
		}
	}
}
