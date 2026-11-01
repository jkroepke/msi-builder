//go:build windows

package nativetest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// closeAliasConfig installs one program that the deferred close action
// ends (CLOSE-02).
const closeAliasConfig = `schema: msi-builder/v1
product: { name: MsiBuilderCloseAlias, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 8a4d2c71-0b9e-4f36-a5d8-3c1e7b02f9a6 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderCloseAlias }
properties: { MSIRESTARTMANAGERCONTROL: Disable }
components:
  - files: [{ source: cli.exe, close: true }]
`

// shortPath returns the 8.3 form of an existing path, or path when the
// volume has no short names.
func shortPath(t *testing.T, path string) string {
	t.Helper()

	long, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]uint16, syscall.MAX_PATH)

	n, err := syscall.GetShortPathName(long, &buf[0], syscall.MAX_PATH)
	if err != nil || n == 0 || int(n) >= len(buf) {
		return path
	}

	return syscall.UTF16ToString(buf[:n])
}

// imagePath returns what QueryFullProcessImageNameW, which the DLL
// compares, reports for a process, or the error text.
func imagePath(pid int) string {
	const queryLimited = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION

	process, err := syscall.OpenProcess(queryLimited, false, uint32(pid)) //nolint:gosec // a process ID fits
	if err != nil {
		return err.Error()
	}
	defer syscall.CloseHandle(process) //nolint:errcheck // nothing to do on failure

	buf := make([]uint16, syscall.MAX_LONG_PATH)
	size := uint32(syscall.MAX_LONG_PATH)

	query := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")
	if r, _, err := query.Call(uintptr(process), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return err.Error()
	}

	return syscall.UTF16ToString(buf[:size])
}

// TestMsiexecClosesAliases checks CLOSE-02: a copy of a package program
// started by another path to the same file, its short 8.3 form or a
// junction to the install dir, is ended by the uninstall as well. A copy
// started by the short name reports that path, which a match by the path
// text alone missed; the DLL compares the file's identity. One started
// through the junction reported the resolved path on the CI runner.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecClosesAliases(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	helper := closeHelper(t)
	dir := t.TempDir()
	path := buildConfig(t, dir, "closealias", closeAliasConfig, map[string][]byte{"cli.exe": helper})
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderCloseAlias")
	alias := filepath.Join(os.Getenv("ProgramData"), "MsiBuilderCloseAlias")

	cleanup := func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").CombinedOutput()
		_ = os.Remove(alias) // the junction only
		_ = os.RemoveAll(installDir)
	}
	cleanup()
	t.Cleanup(cleanup)

	if code := msiexecCode(t, filepath.Join(dir, "install.log"), "/i", path); code != 0 {
		t.Fatalf("install failed with %d\n%s", code, readLog(filepath.Join(dir, "install.log")))
	}

	//nolint:gosec // test-controlled paths
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", alias, installDir).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v\n%s", err, out)
	}

	exe := filepath.Join(installDir, "cli.exe")
	starts := map[string]string{"junction": filepath.Join(alias, "cli.exe")}

	if short := shortPath(t, exe); !strings.EqualFold(short, exe) {
		starts["short name"] = short
	} else {
		t.Log("CLOSE-02: the volume has no short names; only the junction is tested")
	}

	procs := make(map[string]*running, len(starts))
	for name, start := range starts {
		procs[name] = startHelper(t, start, "console")
		t.Logf("CLOSE-02: started by the %s: %s", name, start)
	}

	// Give the helpers a moment to be running before the uninstall lists them.
	time.Sleep(time.Second)

	// The image path each copy reports: a match by text alone found a
	// copy only when this equals the installed path.
	for name, proc := range procs {
		image := imagePath(proc.cmd.Process.Pid)
		t.Logf("CLOSE-02: the copy started by the %s reports %s (installed: %s, same text: %t)", name, image, exe, strings.EqualFold(image, exe))
	}

	log := filepath.Join(dir, "uninstall.log")
	code := msiexecCode(t, log, "/x", path)
	t.Logf("CLOSE-02: uninstall exit code %d\n%s", code, closeLines(log))

	if code != 0 {
		t.Fatalf("uninstall failed with %d\n%s", code, readLog(log))
	}

	for name, proc := range procs {
		if got := proc.exited(15 * time.Second); got != 1602 {
			t.Errorf("the copy started by the %s: exit code %d, want 1602 from the deferred action (-1: still running)", name, got)
		}
	}
}
