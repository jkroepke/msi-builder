//go:build windows

package nativetest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// closeConfig installs the close helper three times: gui.exe and cli.exe
// are closed with the defaults (WM_CLOSE, 5 s, then terminate), keep.exe
// only gets WM_CLOSE. Restart Manager is off, as the design doc recommends
// with close: it would shut the programs down itself in a silent install.
// The packages are stored uncompressed, like the service tests, to keep
// -race builds fast.
const closeConfig = `schema: msi-builder/v1
product: { name: MsiBuilderClose, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 0079f4be-66b6-4022-936e-9f6d9cfcc7e8 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderClose }
properties: { MSIRESTARTMANAGERCONTROL: Disable }
components:
  - files: [{ source: gui.exe, close: true }]
  - files: [{ source: cli.exe, close: true }]
  - files: [{ source: keep.exe, close: { timeout: 2s, terminate: false } }]
`

// closeHelperSource is a program with four modes: "window <marker>" opens
// a hidden top-level window, writes <marker>.ready, and on WM_CLOSE writes
// <marker> and exits with 0; "visible <marker>" does the same with a
// visible window and also opens a hidden one, which writes <marker>.hidden
// when it gets WM_CLOSE; "hold <marker> <file>" is "window <marker>" with
// <file> held open; "console" sleeps without a window. The window modes
// write <marker>.endsession and exit on WM_ENDSESSION, as Restart Manager
// sends it to a program it shuts down.
const closeHelperSource = `//go:build windows

package main

import (
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

func init() { runtime.LockOSThread() }

type wndClassEx struct {
	size, style                            uint32
	wndProc                                uintptr
	clsExtra, wndExtra                     int32
	instance, icon, cursor, background     uintptr
	menuName, className                    *uint16
	iconSm                                 uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
	private uint32
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	registerClassEx  = user32.NewProc("RegisterClassExW")
	createWindowEx   = user32.NewProc("CreateWindowExW")
	defWindowProc    = user32.NewProc("DefWindowProcW")
	getMessage       = user32.NewProc("GetMessageW")
	dispatchMessage  = user32.NewProc("DispatchMessageW")
	destroyWindow    = user32.NewProc("DestroyWindow")
	postQuitMessage  = user32.NewProc("PostQuitMessage")
	getModuleHandle  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

// held keeps the file of "hold" open while the window runs.
var held *os.File

func main() {
	if len(os.Args) > 3 && os.Args[1] == "hold" {
		f, err := os.Open(os.Args[3])
		if err != nil {
			panic(err)
		}
		held = f
		window(os.Args[2], false)
		return
	}
	if len(os.Args) > 2 && (os.Args[1] == "window" || os.Args[1] == "visible") {
		window(os.Args[2], os.Args[1] == "visible")
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

var mainWindow uintptr

func window(marker string, visible bool) {
	proc := syscall.NewCallback(func(hwnd, m, wp, lp uintptr) uintptr {
		switch {
		case m == 0x10 && hwnd != mainWindow: // WM_CLOSE to the helper window
			_ = os.WriteFile(marker+".hidden", []byte("WM_CLOSE"), 0o644)
			return 0
		case m == 0x10: // WM_CLOSE
			_ = os.WriteFile(marker, []byte("WM_CLOSE"), 0o644)
			destroyWindow.Call(hwnd)
			return 0
		case m == 0x16 && wp != 0 && hwnd == mainWindow: // WM_ENDSESSION, the session ends
			_ = os.WriteFile(marker+".endsession", []byte("WM_ENDSESSION"), 0o644)
			os.Exit(0)
		case m == 0x2 && hwnd == mainWindow: // WM_DESTROY
			postQuitMessage.Call(0)
			return 0
		}
		r, _, _ := defWindowProc.Call(hwnd, m, wp, lp)
		return r
	})
	inst, _, _ := getModuleHandle.Call(0)
	cls, _ := syscall.UTF16PtrFromString("MsiBuilderCloseTest")
	wc := wndClassEx{wndProc: proc, instance: inst, className: cls}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := registerClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		panic(err)
	}
	style := uintptr(0xCF0000) // WS_OVERLAPPEDWINDOW
	if visible {
		style |= 0x10000000 // WS_VISIBLE
		if h, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(cls)), 0xCF0000, 0, 0, 200, 100, 0, 0, inst, 0); h == 0 {
			panic(err)
		}
	}
	hwnd, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(cls)), style, 0, 0, 200, 100, 0, 0, inst, 0)
	if hwnd == 0 {
		panic(err)
	}
	mainWindow = hwnd
	_ = os.WriteFile(marker+".ready", nil, 0o644)
	var m msg
	for {
		r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}
`

// closeHelper builds closeHelperSource with a version resource (FILE-01).
func closeHelper(t *testing.T) []byte {
	t.Helper()

	dir := t.TempDir()
	files := map[string][]byte{
		"main.go":                 []byte(closeHelperSource),
		"go.mod":                  []byte("module closehelper\n\ngo 1.27\n"),
		"rsrc_windows_amd64.syso": testpe.Syso(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
	}

	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	exe := filepath.Join(dir, "closehelper.exe")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOARCH=amd64") //nolint:wsl_v5 // configures cmd

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the close helper: %v\n%s", err, out)
	}

	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// running is a started helper and the result of waiting for it.
type running struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// startHelper starts exe with args and kills it when the test ends.
func startHelper(t *testing.T, exe string, args ...string) *running {
	t.Helper()

	proc := &running{cmd: exec.CommandContext(context.Background(), exe, args...), done: make(chan struct{})} //nolint:gosec // test-controlled path
	// Without a console of its own, so a console window cannot take the
	// place of the helper's window when go test runs without a console.
	proc.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW

	if err := proc.cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", exe, err)
	}

	go func() {
		proc.err = proc.cmd.Wait()
		close(proc.done)
	}()

	t.Cleanup(func() {
		_ = proc.cmd.Process.Kill()
		<-proc.done
	})

	return proc
}

// exited waits up to timeout and returns the exit code, or -1 when the
// process still runs.
func (r *running) exited(timeout time.Duration) int {
	select {
	case <-r.done:
		if exit, ok := errors.AsType[*exec.ExitError](r.err); ok {
			return exit.ExitCode()
		}

		return 0
	case <-time.After(timeout):
		return -1
	}
}

// waitFile waits up to ten seconds for a file to appear.
func waitFile(t *testing.T, path string) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}

	t.Fatalf("%s did not appear", path)
}

// closeLines returns the BuilderClose lines of a verbose log.
func closeLines(log string) string {
	var lines []string

	for line := range strings.SplitSeq(readLog(log), "\n") {
		if strings.Contains(line, "BuilderClose") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}

	return strings.Join(lines, "\n")
}

// TestMsiexecClosesApps repairs and removes a package while copies of its
// programs run (CLOSE-01): a program with a window gets WM_CLOSE and exits
// by itself, a console program without one is ended by the deferred action
// (exit code 1602), a copy of it in another folder keeps running, and a
// program with terminate: false keeps running.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecClosesApps(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	helper := closeHelper(t)
	dir := t.TempDir()
	path := buildConfig(t, dir, "close", closeConfig, map[string][]byte{"gui.exe": helper, "cli.exe": helper, "keep.exe": helper})
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderClose")

	defer func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").CombinedOutput()
		_ = os.RemoveAll(installDir)
	}()

	if code := msiexecCode(t, filepath.Join(dir, "install.log"), "/i", path); code != 0 {
		t.Fatalf("install failed with %d\n%s", code, readLog(filepath.Join(dir, "install.log")))
	}

	copyDir := t.TempDir()
	other := filepath.Join(copyDir, "cli.exe")

	if err := os.WriteFile(other, helper, 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}

	elsewhere := startHelper(t, other, "console")

	for _, step := range []struct {
		name string
		args []string
	}{
		// REINSTALLMODE o: every component is reinstalled (action state 3),
		// but no file of the same version is copied, so keep.exe, which
		// keeps running, leaves no replacement pending for a reboot.
		{"repair", []string{"/fomus", path}},
		{"uninstall", []string{"/x", path}},
	} {
		marker := filepath.Join(dir, step.name+".closed")
		gui := startHelper(t, filepath.Join(installDir, "gui.exe"), "window", marker)
		con := startHelper(t, filepath.Join(installDir, "cli.exe"), "console")

		var keep, vis *running

		visMarker := filepath.Join(dir, step.name+".visible")
		if step.name == "repair" {
			keep = startHelper(t, filepath.Join(installDir, "keep.exe"), "console")
			// A program with a visible window and a hidden one: only the
			// visible window gets WM_CLOSE.
			vis = startHelper(t, filepath.Join(installDir, "keep.exe"), "visible", visMarker)
			waitFile(t, visMarker+".ready")
		}

		waitFile(t, marker+".ready")

		log := filepath.Join(dir, step.name+".log")
		code := msiexecCode(t, log, step.args...)
		t.Logf("%s: msiexec exit code %d\n%s", step.name, code, closeLines(log))

		if code != 0 {
			t.Fatalf("%s failed with %d\n%s", step.name, code, readLog(log))
		}

		if got := gui.exited(15 * time.Second); got != 0 {
			t.Errorf("%s: gui.exe exit code %d, want 0 after WM_CLOSE (-1: still running)", step.name, got)
		}

		if _, err := os.Stat(marker); err != nil {
			t.Errorf("%s: gui.exe got no WM_CLOSE: %v", step.name, err)
		}

		if got := con.exited(15 * time.Second); got != 1602 {
			t.Errorf("%s: cli.exe exit code %d, want 1602 from the deferred action (-1: still running)", step.name, got)
		}

		if got := elsewhere.exited(time.Second); got != -1 {
			t.Errorf("%s: the copy of cli.exe in another folder exited with %d", step.name, got)
		}

		if vis != nil {
			if got := vis.exited(15 * time.Second); got != 0 {
				t.Errorf("%s: keep.exe with a visible window exit code %d, want 0 after WM_CLOSE (-1: still running)", step.name, got)
			}

			if _, err := os.Stat(visMarker + ".hidden"); err == nil {
				t.Errorf("%s: the hidden window of a program with a visible one got WM_CLOSE", step.name)
			}
		}

		if keep != nil {
			if got := keep.exited(time.Second); got != -1 {
				t.Errorf("%s: keep.exe (terminate: false, no window) exited with %d", step.name, got)
			}

			_ = keep.cmd.Process.Kill()
			<-keep.done
		}
	}
}
