//go:build windows

package nativetest_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// sidConfig is a package with one service, MsiBuilderSidSvc, whose
// MsiServiceConfig settings are the format argument (SVC-10).
const sidConfig = `schema: msi-builder/v1
product: { name: MsiBuilderSidSvc, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 0c6f3e8a-5d21-4b97-9a4e-1f7d2b8c6e30 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderSidSvc }
components:
  - files: [{ source: svc.exe }]
    services:
      - { name: MsiBuilderSidSvc, start: demand, %s }
`

const sidService = "MsiBuilderSidSvc"

var (
	procOpenSCManagerW       = advapi32.NewProc("OpenSCManagerW")        //nolint:gochecknoglobals // lazy procedure
	procOpenServiceW         = advapi32.NewProc("OpenServiceW")          //nolint:gochecknoglobals // lazy procedure
	procQueryServiceConfig2W = advapi32.NewProc("QueryServiceConfig2W")  //nolint:gochecknoglobals // lazy procedure
	procChangeServiceConfig2 = advapi32.NewProc("ChangeServiceConfig2W") //nolint:gochecknoglobals // lazy procedure
	procCloseServiceHandle   = advapi32.NewProc("CloseServiceHandle")    //nolint:gochecknoglobals // lazy procedure
)

const (
	scManagerConnect     = 0x0001
	serviceQueryConfig   = 0x0001
	serviceChangeConfig  = 0x0002
	configServiceSidInfo = 5
	configPreshutdown    = 7
)

// serviceConfig2 reads one DWORD of QueryServiceConfig2W: the SID type
// (level 5) or the preshutdown timeout in milliseconds (level 7). Both
// structures hold a single DWORD.
func serviceConfig2(name string, level uint32) (uint32, error) {
	var value uint32

	err := withService(name, serviceQueryConfig, func(service uintptr) error {
		var err error

		value, err = queryConfig2(service, name, level)

		return err
	})

	return value, err
}

// setServiceConfig2 writes one DWORD with ChangeServiceConfig2W: the SID
// type (level 5) or the preshutdown timeout (level 7).
func setServiceConfig2(name string, level, value uint32) error {
	return withService(name, serviceChangeConfig, func(service uintptr) error {
		ok, _, err := procChangeServiceConfig2.Call(service, uintptr(level), uintptr(unsafe.Pointer(&value)))
		if ok == 0 {
			return fmt.Errorf("change config level %d of %s: %w", level, name, err)
		}

		return nil
	})
}

// withService opens a service with the given access for use.
func withService(name string, access uintptr, use func(service uintptr) error) error {
	manager, _, err := procOpenSCManagerW.Call(0, 0, scManagerConnect)
	if manager == 0 {
		return fmt.Errorf("open service manager: %w", err)
	}

	defer procCloseServiceHandle.Call(manager) //nolint:errcheck // a close failure changes nothing here

	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("service name: %w", err)
	}

	service, _, err := procOpenServiceW.Call(manager, uintptr(unsafe.Pointer(namePtr)), access)
	if service == 0 {
		return fmt.Errorf("open service %s: %w", name, err)
	}

	defer procCloseServiceHandle.Call(service) //nolint:errcheck // a close failure changes nothing here

	return use(service)
}

// queryConfig2 reads the first DWORD of a QueryServiceConfig2W level.
func queryConfig2(service uintptr, name string, level uint32) (uint32, error) {
	buf := make([]byte, 64)

	var needed uint32

	ok, _, err := procQueryServiceConfig2W.Call(service, uintptr(level), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return 0, fmt.Errorf("query config level %d of %s: %w", level, name, err)
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// sidTypeOf returns the SERVICE_SID_TYPE line of sc qsidtype.
func sidTypeOf(t *testing.T, name string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "sc", "qsidtype", name).CombinedOutput()
	if err != nil {
		t.Fatalf("sc qsidtype %s: %v\n%s", name, err, out)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		if _, value, ok := strings.Cut(line, "SERVICE_SID_TYPE:"); ok {
			return strings.TrimSpace(value)
		}
	}

	t.Fatalf("sc qsidtype %s has no SERVICE_SID_TYPE line:\n%s", name, out)

	return ""
}

// TestMsiexecConfiguresServiceSid installs a service with a SID type and a
// preshutdown timeout through MsiServiceConfig (SVC-10) and reads both back
// with sc qsidtype and QueryServiceConfig2W after the install, and again
// after changing both and repairing with /fa, which must restore the
// package's values. A service that sc.exe registered before with an
// unrestricted SID gets none from sidType: none, as InstallServices
// reconfigures an existing service (SVC-06); without preshutdownTimeout its
// own timeout stays, also through the repair.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecConfiguresServiceSid(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	svc := testService(t)

	for _, tc := range []sidCase{
		{"unrestricted", "sidType: unrestricted, preshutdownTimeout: 2m30s, control: { startOnInstall: true }", "UNRESTRICTED", 1, 150000, false},
		{"restricted", "sidType: restricted, preshutdownTimeout: 5s, control: { startOnInstall: true }", "RESTRICTED", 3, 5000, false},
		{"none on an existing service", "sidType: none, control: { startOnInstall: false }", "NONE", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) { testServiceSid(t, svc, &tc) })
	}
}

// sidCase is one case of TestMsiexecConfiguresServiceSid. A wantPre of 0
// means the package has no preshutdownTimeout: the service's own value
// must stay.
type sidCase struct {
	name, settings, wantSc string
	wantSid, wantPre       uint32
	existing               bool
}

// changedPreshutdown is the timeout set before the repair.
const changedPreshutdown = 1234

// testServiceSid installs, changes, repairs and removes one case.
func testServiceSid(t *testing.T, svc []byte, tc *sidCase) {
	t.Helper()

	installDir := filepath.Join(os.Getenv("ProgramFiles"), sidService)
	dir := t.TempDir()
	path := buildConfig(t, dir, "sid", fmt.Sprintf(sidConfig, tc.settings), map[string][]byte{"svc.exe": svc})
	wantPre := tc.wantPre

	if tc.existing {
		createService(t, sidService, filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe"), false)

		if out, err := exec.CommandContext(t.Context(), "sc", "sidtype", sidService, "unrestricted").CombinedOutput(); err != nil {
			t.Fatalf("sc sidtype: %v\n%s", err, out)
		}

		if got := sidTypeOf(t, sidService); got != "UNRESTRICTED" {
			t.Fatalf("sc sidtype left %q, want UNRESTRICTED before the install", got)
		}

		before, err := serviceConfig2(sidService, configPreshutdown)
		if err != nil {
			t.Fatal(err)
		}

		if wantPre == 0 {
			wantPre = before
		}
	}

	defer uninstall(path, sidService, installDir)

	msiexecQuiet(t, filepath.Join(dir, "install.log"), "/i", path)
	checkServiceConfig(t, "install", tc.wantSc, tc.wantSid, wantPre)

	if strings.Contains(tc.settings, "startOnInstall: true") {
		if state := serviceState(t, sidService); !strings.Contains(state, "RUNNING") {
			t.Errorf("service state %q, want RUNNING", state)
		}
	}

	// Change both, so that the repair has to restore the package's values.
	changedSid := uint32(1)
	if tc.wantSid != 0 {
		changedSid = 0
	}

	if err := setServiceConfig2(sidService, configServiceSidInfo, changedSid); err != nil {
		t.Fatal(err)
	}

	if err := setServiceConfig2(sidService, configPreshutdown, changedPreshutdown); err != nil {
		t.Fatal(err)
	}

	if tc.wantPre == 0 {
		wantPre = changedPreshutdown
	}

	msiexecQuiet(t, filepath.Join(dir, "repair.log"), "/fa", path)
	checkServiceConfig(t, "repair", tc.wantSc, tc.wantSid, wantPre)

	msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)

	if state := serviceState(t, sidService); state != "" {
		t.Errorf("service still registered after uninstall: %q", state)
	}
}

// checkServiceConfig compares the SID type and the preshutdown timeout of
// MsiBuilderSidSvc.
func checkServiceConfig(t *testing.T, step, wantSc string, wantSid, wantPre uint32) {
	t.Helper()

	if got := sidTypeOf(t, sidService); got != wantSc {
		t.Errorf("%s: sc qsidtype %q, want %q", step, got, wantSc)
	}

	sid, err := serviceConfig2(sidService, configServiceSidInfo)
	if err != nil {
		t.Fatal(err)
	}

	if sid != wantSid {
		t.Errorf("%s: SERVICE_SID_INFO %d, want %d", step, sid, wantSid)
	}

	pre, err := serviceConfig2(sidService, configPreshutdown)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("%s: SID type %d, preshutdown timeout %d ms", step, sid, pre)

	if pre != wantPre {
		t.Errorf("%s: preshutdown timeout %d ms, want %d", step, pre, wantPre)
	}
}
