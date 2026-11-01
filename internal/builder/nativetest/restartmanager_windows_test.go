//go:build windows

package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restartManagerConfig installs one file under a value of
// MSIRESTARTMANAGERCONTROL (PROP-04).
const restartManagerConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRestartManager, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 5e2b9c14-7a3d-4f80-b61e-0d4c8a2f7b95 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderRestartManager }
properties: { MSIRESTARTMANAGERCONTROL: %q }
components:
  - files: [{ source: a.txt }]
`

// restartManagerLines returns the log lines that name Restart Manager.
func restartManagerLines(log string) []string {
	var lines []string

	for line := range strings.Lines(readLog(log)) {
		if lower := strings.ToLower(line); strings.Contains(lower, "restart manager") || strings.Contains(lower, "msirestartmanagercontrol") ||
			strings.Contains(lower, "rmshutdown") || strings.Contains(lower, "filesinuse") || strings.Contains(lower, "held in use") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}

	return lines
}

// TestMsiexecRestartManagerControl checks PROP-04: what the engine does with
// each documented value of MSIRESTARTMANAGERCONTROL, and with a value in
// other case, on an install and on a /fa repair while another process
// holds the installed file open.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRestartManagerControl(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	target := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderRestartManager", "a.txt")

	for _, value := range []string{"0", "Disable", "DisableShutdown", "disable"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			path := buildConfig(t, dir, "rm", fmt.Sprintf(restartManagerConfig, value), map[string][]byte{"a.txt": []byte("a")})

			t.Cleanup(func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").Run()
			})

			log := filepath.Join(dir, "install.log")
			if code := msiexecCode(t, log, "/i", path); code != 0 {
				t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
			}

			t.Logf("install: %q", restartManagerLines(log))

			// A second process holds the file open for reading, so the
			// repair finds it in use.
			holder := exec.CommandContext(context.Background(), "powershell", "-NoProfile", "-Command", //nolint:gosec // a test path
				"$f = [IO.File]::Open('"+target+"', 'Open', 'Read', 'Read'); Start-Sleep -Seconds 300")
			if err := holder.Start(); err != nil {
				t.Fatal(err)
			}

			exited := make(chan error, 1)

			go func() { exited <- holder.Wait() }()

			t.Cleanup(func() { _ = holder.Process.Kill() })

			time.Sleep(5 * time.Second) // let PowerShell open the file

			log = filepath.Join(dir, "repair.log")
			code := msiexecCode(t, log, "/fa", path)

			alive := "still running"

			select {
			case err := <-exited:
				alive = fmt.Sprintf("ended (%v)", err)
			case <-time.After(time.Second):
			}

			t.Logf("repair with the file held open: exit %d, holder %s, %q", code, alive, restartManagerLines(log))

			if code != 0 && code != 3010 {
				t.Errorf("msiexec /fa returned %d\n%s", code, readLog(log))
			}
		})
	}
}
