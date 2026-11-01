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
	"time"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// The non-administrator tests install packages as a standard user that the
// CI step "Create a standard user" makes on the disposable runner. They read
// the account from MSI_BUILDER_NONADMIN_USER and MSI_BUILDER_NONADMIN_PASSWORD
// and skip without them; never run them on a machine you keep, since
// TestNonAdminSecureProperties turns on AlwaysInstallElevated for its run.

var pCreateProcessWithLogonW = syscall.NewLazyDLL("advapi32.dll").NewProc("CreateProcessWithLogonW") //nolint:gochecknoglobals // lazy procedure

const (
	logonWithProfile = 1
	// nonAdminRoot holds the packages and logs, readable and writable by
	// the standard user.
	nonAdminRoot = `C:\MsiBuilderNonAdmin`
	// nonAdminTimeout bounds one msiexec run in milliseconds.
	nonAdminTimeout = 5 * 60 * 1000
)

// nonAdminUser is the standard user the tests run msiexec as.
type nonAdminUser struct {
	name, password string
}

// standardUser returns the test account and prepares nonAdminRoot, or skips.
func standardUser(t *testing.T) nonAdminUser {
	t.Helper()

	if testing.Short() {
		t.Skip("installing needs msiexec time")
	}

	user := nonAdminUser{name: os.Getenv("MSI_BUILDER_NONADMIN_USER"), password: os.Getenv("MSI_BUILDER_NONADMIN_PASSWORD")}
	if user.name == "" || user.password == "" {
		t.Skip("MSI_BUILDER_NONADMIN_USER and MSI_BUILDER_NONADMIN_PASSWORD are not set")
	}

	if err := os.MkdirAll(nonAdminRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// BUILTIN\Users may modify the folder, so the user can write its logs.
	if out, err := exec.CommandContext(t.Context(), "icacls", nonAdminRoot, "/grant", "*S-1-5-32-545:(OI)(CI)M").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}

	return user
}

// run starts command line as the user with its profile loaded and returns
// the exit code.
func (u nonAdminUser) run(t *testing.T, cmdline string) int {
	t.Helper()

	var (
		startup syscall.StartupInfo
		process syscall.ProcessInformation
	)

	startup.Cb = uint32(unsafe.Sizeof(startup))
	line := wstr(t, cmdline)

	r, _, err := pCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(wstr(t, u.name))), uintptr(unsafe.Pointer(wstr(t, "."))), uintptr(unsafe.Pointer(wstr(t, u.password))),
		logonWithProfile, 0, uintptr(unsafe.Pointer(line)), 0, 0, uintptr(unsafe.Pointer(wstr(t, nonAdminRoot))),
		uintptr(unsafe.Pointer(&startup)), uintptr(unsafe.Pointer(&process)))
	if r == 0 {
		t.Fatalf("CreateProcessWithLogonW %s: %v", cmdline, err)
	}

	defer func() {
		_ = syscall.CloseHandle(process.Thread)
		_ = syscall.CloseHandle(process.Process)
	}()

	if event, err := syscall.WaitForSingleObject(process.Process, nonAdminTimeout); err != nil || event != syscall.WAIT_OBJECT_0 {
		_ = syscall.TerminateProcess(process.Process, 1)

		t.Fatalf("%s did not end within %d ms (%d, %v)", cmdline, nonAdminTimeout, event, err)
	}

	var code uint32
	if err := syscall.GetExitCodeProcess(process.Process, &code); err != nil {
		t.Fatal(err)
	}

	return int(code)
}

// msiexec runs msiexec silently as the user with a verbose log.
func (u nonAdminUser) msiexec(t *testing.T, log string, args ...string) int {
	t.Helper()

	// msiexec takes PROPERTY="value", not a quoted whole argument. A /f
	// repair runs as /i (see repairArgs) so REBOOT reaches the server.
	args = repairArgs(args)

	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if name, value, ok := strings.Cut(arg, "="); ok && !strings.HasPrefix(arg, "/") {
			arg = name + `="` + strings.ReplaceAll(value, `"`, `""`) + `"`
		} else {
			arg = syscall.EscapeArg(arg)
		}

		quoted = append(quoted, arg)
	}

	return u.run(t, fmt.Sprintf(`msiexec.exe %s /qn /norestart REBOOT=ReallySuppress /l*v %s`, strings.Join(quoted, " "), syscall.EscapeArg(log)))
}

// policy runs reg.exe on the user's Installer policy key. A standard user
// may not write its own HKCU\SOFTWARE\Policies, so the administrator does,
// through HKU\<SID>; when the user's hive is not loaded, it loads
// NTUSER.DAT for the change. A logon with profile first creates the profile.
func (u nonAdminUser) policy(t *testing.T, action string, args ...string) error {
	t.Helper()

	if code := u.run(t, "cmd.exe /c exit 0"); code != 0 {
		return fmt.Errorf("logon of %s returned %d", u.name, code)
	}

	sid, _, _, err := syscall.LookupSID("", u.name)
	if err != nil {
		return fmt.Errorf("security identifier of %s: %w", u.name, err)
	}

	hive, err := sid.String()
	if err != nil {
		return fmt.Errorf("security identifier of %s: %w", u.name, err)
	}

	hive = `HKU\` + hive
	ctx := context.Background()
	key := hive + `\SOFTWARE\Policies\Microsoft\Windows\Installer`

	// The profile service unloads the hive some time after the logon
	// ends, so a loaded hive can go away and a load can meet NTUSER.DAT
	// still open; try again a few times.
	var last error

	for range 10 {
		if last = u.writePolicy(ctx, hive, key, action, args); last == nil {
			return nil
		}

		time.Sleep(time.Second)
	}

	return last
}

// writePolicy runs reg.exe action on key in hive, loading the user's
// NTUSER.DAT for it when the hive is not loaded.
func (u nonAdminUser) writePolicy(ctx context.Context, hive, key, action string, args []string) error {
	if exec.CommandContext(ctx, "reg.exe", "query", hive).Run() != nil {
		profile := filepath.Join(os.Getenv("SystemDrive")+`\`, "Users", u.name, "NTUSER.DAT")
		//nolint:gosec // the hive and profile path come from the test account
		if out, err := exec.CommandContext(ctx, "reg.exe", "load", hive, profile).CombinedOutput(); err != nil {
			return fmt.Errorf("reg load %s: %w\n%s", profile, err, out)
		}

		defer func() { _ = exec.CommandContext(ctx, "reg.exe", "unload", hive).Run() }()
	}

	//nolint:gosec // the test passes fixed reg.exe arguments
	if out, err := exec.CommandContext(ctx, "reg.exe", append([]string{action, key}, args...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("reg %s %s: %w\n%s", action, key, err, out)
	}

	return nil
}

// nonAdminDir returns a fresh folder under nonAdminRoot for one test.
func nonAdminDir(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(nonAdminRoot, name)
	_ = os.RemoveAll(dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

const nonAdminScopeConfig = `schema: msi-builder/v1
product: { name: MsiBuilderNonAdmin, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 8e1c5a7d-3f2b-4d6e-9a0c-1b4d7e2f5a38 }
package: { platform: x64, scope: %s }
components:
  - files: [{ source: app.exe }]
%s`

// nonAdminContextComponents gives a dual package's component a variable
// that follows the context and adds a component with an HKLM value and a
// firewall rule that installs only per machine: none may need elevation
// per user (ENVVAR-03, INST-08).
const nonAdminContextComponents = `    environment: [{ name: MSIBUILDER_NONADMIN_TEST, value: x }]
  - id: MachineOnly
    directory: INSTALLDIR
    scope: perMachine
    registry: [{ root: HKLM, key: Software\MsiBuilderNonAdmin, name: Only, value: machine }]
firewall: [{ name: MsiBuilderNonAdmin, component: MachineOnly, program: '[INSTALLDIR]app.exe' }]
`

// TestNonAdminInstallScopes installs packages of each scope silently as a
// standard user (INST-02 on the CI runner): perUser and perUserOrMachine
// install into the user's profile without elevation, and perMachine and
// perMachineOrUser fail with 1603 after error 1925, as on SERVER2025. The
// perUser package's component has the HKCU key path the builder adds
// (INST-04); a repair as the user restores its deleted file. The dual
// packages have a variable that follows the context and an HKLM value in a
// perMachine component with a firewall rule, which the per-user install
// leaves out (ENVVAR-03, INST-08).
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestNonAdminInstallScopes(t *testing.T) {
	user := standardUser(t)
	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct {
		scope string
		code  int
	}{
		{"perUser", 0},
		{"perUserOrMachine", 0},
		{"perMachineOrUser", 1603},
		{"perMachine", 1603},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			dir := nonAdminDir(t, "scope-"+tc.scope)

			extra := ""
			if strings.Contains(tc.scope, "Or") {
				extra = nonAdminContextComponents
			}

			path := buildConfig(t, dir, "scope", fmt.Sprintf(nonAdminScopeConfig, tc.scope, extra), map[string][]byte{"app.exe": app})
			log := filepath.Join(dir, "install.log")

			t.Cleanup(func() { _ = user.msiexec(t, filepath.Join(dir, "cleanup.log"), "/x", path) })

			code := user.msiexec(t, log, "/i", path)
			props := serverProperties(readLog(log))

			t.Logf("INST-02 %s as %s: exit %d, INSTALLDIR %q, ALLUSERS %q, MSIINSTALLPERUSER %q, Privileged %q",
				tc.scope, user.name, code, props["INSTALLDIR"], props["ALLUSERS"], props["MSIINSTALLPERUSER"], props["Privileged"])

			if code != tc.code {
				t.Fatalf("msiexec /i returned %d, want %d\n%s", code, tc.code, readLog(log))
			}

			if code != 0 {
				if !strings.Contains(readLog(log), "Error 1925.") {
					t.Errorf("the log has no error 1925\n%s", readLog(log))
				}

				return
			}

			installed := filepath.Join(props["INSTALLDIR"], "app.exe")
			if !strings.Contains(strings.ToLower(installed), strings.ToLower(`\Users\`+user.name+`\`)) {
				t.Errorf("installed to %s, not into the profile of %s", installed, user.name)
			}

			if _, err := os.Stat(installed); err != nil {
				t.Errorf("app.exe after the install: %v", err)
			}

			if regHas(t, "64", `HKLM\Software\MsiBuilderNonAdmin`, "Only") {
				t.Error("the per-user install wrote the HKLM value of the perMachine component")
			}

			if regHas(t, "64", `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "MSIBUILDER_NONADMIN_TEST") {
				t.Error("the per-user install wrote the variable to the machine environment")
			}

			if exec.CommandContext(t.Context(), "netsh", "advfirewall", "firewall", "show", "rule", "name=MsiBuilderNonAdmin").Run() == nil {
				t.Error("the per-user install added the firewall rule of the perMachine component")
			}

			if tc.scope == "perUser" {
				if err := os.Remove(installed); err != nil {
					t.Fatal(err)
				}

				if code := user.msiexec(t, filepath.Join(dir, "repair.log"), "/f", path); code != 0 {
					t.Errorf("msiexec /f returned %d", code)
				}

				if _, err := os.Stat(installed); err != nil {
					t.Errorf("app.exe after the repair: %v", err)
				}
			}

			if code := user.msiexec(t, filepath.Join(dir, "uninstall.log"), "/x", path); code != 0 {
				t.Errorf("msiexec /x returned %d", code)
			}

			if _, err := os.Stat(installed); err == nil {
				t.Errorf("%s is still there after the uninstall", installed)
			}
		})
	}
}

const secureConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSecure, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 1d5f9b3e-7a2c-4e8d-b6f0-3c7e1a5d9b42 }
package: { platform: x64 }
installDir: { name: MsiBuilderSecure }
properties:
  PUB_SECURE: { value: default }
  PUB_PLAIN: { value: default, secure: false }
components:
  - files: [{ source: app.exe }]
customActions:
  - id: EchoProps
    run:
      command: '"[%ComSpec]" /c echo PROP-01 SECURE=[PUB_SECURE] PLAIN=[PUB_PLAIN] Privileged=[Privileged]'
    execute: deferred
    impersonate: false
    return: check
    schedule:
      - { sequence: execute, after: InstallFiles, condition: NOT REMOVE~="ALL" }
`

// TestNonAdminSecureProperties installs a per-machine package with
// AlwaysInstallElevated as a standard user, the elevated case in which only
// secure public properties reach the execute sequence (PROP-01), and the
// same package as the administrator for control. A deferred run action
// echoes both properties into the log.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestNonAdminSecureProperties(t *testing.T) {
	user := standardUser(t)
	dir := nonAdminDir(t, "secure")
	path := buildConfig(t, dir, "secure", secureConfig, map[string][]byte{
		"app.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	})

	const policy = `HKLM\SOFTWARE\Policies\Microsoft\Windows\Installer`

	ctx := context.Background()

	if out, err := exec.CommandContext(ctx, "reg.exe", "add", policy, "/v", "AlwaysInstallElevated", "/t", "REG_DWORD", "/d", "1", "/f").CombinedOutput(); err != nil {
		t.Fatalf("reg add: %v\n%s", err, out)
	}

	// Cleanups run last in, first out: the user's policy goes before the
	// machine's, and a failing logon there cannot keep the machine's.
	t.Cleanup(func() {
		_ = exec.CommandContext(ctx, "reg.exe", "delete", policy, "/v", "AlwaysInstallElevated", "/f").Run()
		_ = exec.CommandContext(ctx, "msiexec", "/x", path, "/qn").Run()
		_ = os.RemoveAll(filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderSecure"))
	})
	t.Cleanup(func() { _ = user.policy(t, "delete", "/v", "AlwaysInstallElevated", "/f") })

	if err := user.policy(t, "add", "/v", "AlwaysInstallElevated", "/t", "REG_DWORD", "/d", "1", "/f"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		admin bool
		want  string
	}{
		{"administrator", true, "SECURE=cmd PLAIN=cmd"},
		{"standard user elevated by policy", false, "SECURE=cmd PLAIN=default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".log")
			args := []string{"/i", path, "PUB_SECURE=cmd", "PUB_PLAIN=cmd"}

			var code int
			if tc.admin {
				code = msiexecCode(t, log, args...)
			} else {
				code = user.msiexec(t, log, args...)
			}

			var echoed string

			for line := range strings.Lines(readLog(log)) {
				if _, rest, ok := strings.Cut(line, "BuilderRun: PROP-01 "); ok {
					echoed = strings.TrimSpace(rest)
				}
			}

			t.Logf("PROP-01 %s: exit %d, %s", tc.name, code, echoed)

			if code != 0 {
				t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
			}

			if !strings.HasPrefix(echoed, tc.want) {
				t.Errorf("the deferred action saw %q, want %q", echoed, tc.want)
			}

			if code := msiexecCode(t, filepath.Join(dir, "x.log"), "/x", path); code != 0 {
				t.Errorf("msiexec /x returned %d", code)
			}
		})
	}
}

const grantRepairConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSec03%[1]s, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: %[2]s }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderSec03%[1]s }
properties:
  SVC_ACCOUNT: { %[3]s }
components:
  - files: [{ source: app.exe }]
  - id: Data
    directory: CommonAppDataFolder/MsiBuilderSec03%[1]s/data
    createFolder: true
    folderPermissions:
      grant: [{ account: '[SVC_ACCOUNT]', rights: modify }]
`

// TestNonAdminGrantRepair installs a per-machine package with a folder
// grant to [SVC_ACCOUNT] as the administrator, with NetworkService, and
// then repairs it silently as a standard user who passes their own account
// on the command line (SEC-03). The builder no longer makes SVC_ACCOUNT
// secure, so the engine drops the user's value ("Ignoring disallowed
// property"); the control package declares SVC_ACCOUNT secure: true, and
// there the user's value reaches the server. On the CI runner the engine
// refuses the silent repair with error 1730 (exit 1603) in
// InstallInitialize, before the grant actions run (SECREPAIR:
// the package has an elevated custom action, and a standard user's repair
// then needs a credential prompt); a Windows that runs the repair grants to
// the remembered account in the default case and to the user with
// secure: true.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestNonAdminGrantRepair(t *testing.T) {
	user := standardUser(t)
	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	sid, _, _, err := syscall.LookupSID("", user.name)
	if err != nil {
		t.Fatal(err)
	}

	userSID, err := sid.String()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, suffix, upgradeCode, property, serverValue string
	}{
		{"default", "", "6c3a9e15-2b7d-4f80-a4c1-8d5e0b2f7a63", "", ""},
		{"secure", "Secure", "9d4b0f26-3c8e-4a91-b5d2-7e6f1c3a8b04", "secure: true", `.\` + user.name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := nonAdminDir(t, "grant-"+tc.name)
			path := buildConfig(t, dir, "grant", fmt.Sprintf(grantRepairConfig, tc.suffix, tc.upgradeCode, tc.property),
				map[string][]byte{"app.exe": app})
			data := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderSec03"+tc.suffix, "data")

			// t.Context is canceled before cleanups run.
			t.Cleanup(func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").Run()
				_ = os.RemoveAll(filepath.Dir(data))
			})

			if code := msiexecCode(t, filepath.Join(dir, "install.log"), "/i", path, "SVC_ACCOUNT=NS"); code != 0 {
				t.Fatalf("msiexec /i as the administrator returned %d\n%s", code, readLog(filepath.Join(dir, "install.log")))
			}

			log := filepath.Join(dir, "repair.log")
			code := user.msiexec(t, log, "/i", path, "REINSTALL=ALL", "REINSTALLMODE=amus", `SVC_ACCOUNT=.\`+user.name)
			sddl := objectSDDL(t, data)
			repair := readLog(log)
			server := serverProperties(repair)["SVC_ACCOUNT"]

			t.Logf("SEC-03 %s: repair as %s exit %d, server SVC_ACCOUNT %q, %s\nfolder %s",
				tc.name, user.name, code, server, grepLog(repair, "disallowed property", "SECREPAIR", "MSI_LUA", "Error 1730", "BuilderGrant: "), sddl)

			if server != tc.serverValue {
				t.Errorf("the server's SVC_ACCOUNT is %q, want %q", server, tc.serverValue)
			}

			if dropped := strings.Contains(repair, "Ignoring disallowed property SVC_ACCOUNT"); dropped != (tc.serverValue == "") {
				t.Errorf("the log says the engine dropped SVC_ACCOUNT: %v, want %v", dropped, tc.serverValue == "")
			}

			granted := strings.Contains(sddl, ";;;"+userSID+")")

			switch {
			case code == 1603 && strings.Contains(repair, "Error 1730."):
				// The repair stopped in InstallInitialize, before the grants.
				if granted {
					t.Errorf("the refused repair granted the folder to %s\n%s", user.name, sddl)
				}
			case code == 0:
				if granted != (tc.serverValue != "") {
					t.Errorf("the folder grants to %s: %v, want %v\n%s", user.name, granted, tc.serverValue != "", sddl)
				}

				if fellBack := strings.Contains(repair, "granting to the remembered account"); fellBack != (tc.serverValue == "") {
					t.Errorf("the repair fell back to the remembered account: %v, want %v", fellBack, tc.serverValue == "")
				}
			default:
				t.Fatalf("msiexec /i REINSTALL=ALL as %s returned %d, want 0, or 1603 after error 1730\n%s", user.name, code, repair)
			}

			if !strings.Contains(sddl, ";;;NS)") {
				t.Errorf("the folder lost the NetworkService grant\n%s", sddl)
			}
		})
	}
}
