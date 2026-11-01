//go:build windows

package nativetest_test

import (
	"bufio"
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

	report := make([]string, 0, 12)

	for _, value := range []string{"0", "Disable", "DisableShutdown", "disable"} {
		lines := restartManagerRun(t, value, target)
		t.Log(strings.Join(lines, "\n")) // kept if a later value fails

		report = append(report, lines...)
	}

	t.Errorf("PROP-04 probe:\n%s", strings.Join(report, "\n"))
}

// restartManagerRun installs the package with one value, repairs it while
// PowerShell holds the file open and uninstalls it, and returns what the
// logs show.
func restartManagerRun(t *testing.T, value, target string) []string {
	t.Helper()

	dir := t.TempDir()
	path := buildConfig(t, dir, "rm", fmt.Sprintf(restartManagerConfig, value), map[string][]byte{"a.txt": []byte("a")})

	defer func() {
		if err := exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").Run(); err != nil {
			t.Logf("%s: uninstall: %v", value, err)
		}
	}()

	// A run that failed earlier may have left the product installed.
	_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").Run()

	report := []string{"== " + value}

	log := filepath.Join(dir, "install.log")
	if code := msiexecCode(t, log, "/i", path); code != 0 {
		t.Fatalf("msiexec /i returned %d\n%s", code, readLog(log))
	}

	report = append(report, fmt.Sprintf("install: %q", restartManagerLines(log)))

	// A second process holds the file open for reading, so the repair
	// finds it in use. It prints a line once the file is open.
	holder := exec.CommandContext(context.Background(), "powershell", "-NoProfile", "-Command", //nolint:gosec // a test path
		"$f = [IO.File]::Open('"+target+"', 'Open', 'Read', 'Read'); [Console]::Out.WriteLine('open'); [Console]::Out.Flush(); Start-Sleep -Seconds 300")

	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}

	exited := make(chan error, 1)

	defer func() {
		_ = holder.Process.Kill()

		<-exited //nolint:errcheck // the process was killed
	}()

	line, err := bufio.NewReader(out).ReadString('\n')

	go func() { exited <- holder.Wait() }()

	if err != nil || strings.TrimSpace(line) != "open" {
		t.Fatalf("PowerShell did not open %s: %q %v", target, line, err)
	}

	log = filepath.Join(dir, "repair.log")
	code := msiexecCode(t, log, "/fa", path)

	if code == 3010 {
		// The engine replaces the held file at the next restart; drop
		// that, so that a developer machine does not get it back.
		defer dropPendingRenames(t, filepath.Dir(target))
	}

	alive := "still running"

	select {
	case err := <-exited:
		exited <- err

		alive = fmt.Sprintf("ended (%v)", err)
	case <-time.After(time.Second):
	}

	return append(report, fmt.Sprintf("repair with the file held open: exit %d, holder %s, %q", code, alive, restartManagerLines(log)))
}

// dropPendingRenames removes the restart renames of files below dir from
// PendingFileRenameOperations.
func dropPendingRenames(t *testing.T, dir string) {
	t.Helper()

	script := `$k = 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager'
$v = (Get-ItemProperty $k -Name PendingFileRenameOperations -ErrorAction SilentlyContinue).PendingFileRenameOperations
if ($v) {
  if ($v.Count % 2) { throw "odd PendingFileRenameOperations count $($v.Count)" }
  $keep = @()
  $pat = '*' + $env:RM_DIR + '\*'
  for ($i = 0; $i + 1 -lt $v.Count; $i += 2) {
    if ($v[$i] -notlike $pat -and $v[$i + 1] -notlike $pat) { $keep += $v[$i]; $keep += $v[$i + 1] }
  }
  if ($keep.Count) { Set-ItemProperty $k -Name PendingFileRenameOperations -Value $keep -Type MultiString }
  else { Remove-ItemProperty $k -Name PendingFileRenameOperations }
}`

	cmd := exec.CommandContext(context.Background(), "powershell", "-NoProfile", "-Command", script)

	cmd.Env = append(os.Environ(), "RM_DIR="+dir)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("clearing the pending renames: %v\n%s", err, out)
	}
}
