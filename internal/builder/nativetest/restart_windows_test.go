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

// restartConfig registers a file, a program and a service the package does
// not install with the installer's Restart Manager session (CLOSE-04). The
// file and the program come from public properties on the command line, so
// that the test can point them into its own temporary folders. Restart
// Manager is on, the installer's default.
const restartConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRestart, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 3c1f5e0a-8d27-4b6e-9a43-2f7d0b9c1e85 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderRestart }
components:
  - files: [{ source: app.exe }]
    restartResources:
      - file: '[HELD_FILE]'
      - program: '[OTHER_PROGRAM]'
      - service: MsiBuilderRestartSvc
`

const restartService = "MsiBuilderRestartSvc"

// restartLines returns the BuilderRestart and Restart Manager lines of a
// verbose log.
func restartLines(log string) string {
	var lines []string

	for line := range strings.SplitSeq(readLog(log), "\n") {
		if strings.Contains(line, "BuilderRestart") || strings.Contains(line, "RESTART MANAGER") || strings.Contains(line, "BuilderClose") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}

	return strings.Join(lines, "\n")
}

// TestMsiexecRestartResources installs a package that registers a file
// held by a program outside the package, a program outside the package and
// a service it does not install (CLOSE-04). It expects that in a silent
// install Restart Manager shuts down the holder and the program, and that a
// copy of the program in another folder, which is not registered, keeps
// running. A repair with MSIRESTARTMANAGERCONTROL=Disable on the command
// line, run as /i REINSTALL=ALL, and the same package with the property in
// its Property table start no session, so the action registers nothing and
// the programs keep running.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRestartResources(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	helper := closeHelper(t)
	dir := longPath(t.TempDir())
	path := buildConfig(t, dir, "restart", restartConfig, map[string][]byte{"app.exe": helper})
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderRestart")

	defer func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").CombinedOutput()
		_ = os.RemoveAll(installDir)
	}()

	svcExe := filepath.Join(dir, "svc.exe")
	if err := os.WriteFile(svcExe, testService(t), 0o600); err != nil {
		t.Fatal(err)
	}

	createService(t, restartService, svcExe, true)

	held := filepath.Join(dir, "held.dat")
	if err := os.WriteFile(held, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Long paths: the runner's TEMP may be an 8.3 path, and the image path
	// a process reports is the long one.
	progDir, otherDir := longPath(t.TempDir()), longPath(t.TempDir())
	prog, other := filepath.Join(progDir, "tool.exe"), filepath.Join(otherDir, "tool.exe")
	holderExe := filepath.Join(dir, "holder.exe")

	for _, exe := range []string{prog, other, holderExe} {
		if err := os.WriteFile(exe, helper, 0o700); err != nil { //nolint:gosec // an executable
			t.Fatal(err)
		}
	}

	props := []string{"HELD_FILE=" + held, "OTHER_PROGRAM=" + prog}

	// The same package with Restart Manager turned off in the Property
	// table, as another product.
	offPath := buildConfig(t, dir, "restartoff", restartOffConfig(), map[string][]byte{"app.exe": helper})

	defer func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", offPath, "/qn", "/norestart").CombinedOutput()
		_ = os.RemoveAll(installDir + "Off")
	}()

	for _, step := range []struct {
		name string
		args []string
		kind string
	}{
		{"install", append([]string{"/i", path}, props...), "registered"},
		// The property on the command line of a repair. msiexecCode runs
		// /fomus as /i REINSTALL=ALL REINSTALLMODE=omus, which passes it to
		// the server, and it turns Restart Manager off (CI, 2026-11-01). A
		// real /fomus dropped it with the other public properties and left
		// Restart Manager on (CLOSE-04, first runs).
		{"repair-cmdline", []string{"/fomus", path, "MSIRESTARTMANAGERCONTROL=Disable"}, "disabled"},
		{"install-disabled", append([]string{"/i", offPath}, props...), "disabled"},
	} {
		holder := startHelper(t, holderExe, "hold", filepath.Join(dir, step.name+".holder"), held)
		run := startHelper(t, prog, "window", filepath.Join(dir, step.name+".prog"))
		copyElsewhere := startHelper(t, other, "window", filepath.Join(dir, step.name+".other"))

		for _, marker := range []string{".holder", ".prog", ".other"} {
			waitFile(t, filepath.Join(dir, step.name+marker+".ready"))
		}

		pid := servicePID(t, restartService)
		log := filepath.Join(dir, step.name+".log")
		code := msiexecCode(t, log, step.args...)
		t.Logf("%s: msiexec exit code %d, service %q, PID %s before, %s after\n%s", step.name, code,
			serviceState(t, restartService), pid, servicePID(t, restartService), restartLines(log))

		// 3010: Restart Manager could not shut something down and the
		// installer wants a reboot; the checks below tell what.
		if code != 0 && code != 3010 {
			t.Fatalf("%s failed with %d\n%s", step.name, code, readLog(log))
		}

		text := readLog(log)

		switch step.kind {
		case "disabled":
			checkNoSession(t, step.name, text, map[string]*running{"holder": holder, "program": run})
		default:
			checkRegistered(t, step.name, text, []string{
				"BuilderRestart: file " + held + ": registered",
				"BuilderRestart: service " + restartService + ": registered",
				"(" + prog + "): registered",
			}, map[string]*running{"holder of the file": holder, "registered program": run})

			// Restart Manager sends WM_ENDSESSION to a program it shuts
			// down; the helper writes a marker for it.
			for _, marker := range []string{".holder", ".prog"} {
				_, err := os.Stat(filepath.Join(dir, step.name+marker+".endsession"))
				t.Logf("%s: %s got WM_ENDSESSION: %v", step.name, marker[1:], err == nil)
			}
		}

		if got := copyElsewhere.exited(time.Second); got != -1 {
			t.Errorf("%s: the copy of the program in another folder exited with %d", step.name, got)
		}

		for _, proc := range []*running{holder, run, copyElsewhere} {
			_ = proc.cmd.Process.Kill()
			<-proc.done
		}
	}
}

// restartOffConfig returns restartConfig with Restart Manager turned off in
// the Property table, as another product.
func restartOffConfig() string {
	return strings.NewReplacer(
		"MsiBuilderRestart", "MsiBuilderRestartOff",
		"3c1f5e0a-8d27-4b6e-9a43-2f7d0b9c1e85", "8e4a1c27-5f93-4d0b-a6e2-1b7c9d3f5a60",
		"components:\n", "properties: { MSIRESTARTMANAGERCONTROL: Disable }\ncomponents:\n",
	).Replace(restartConfig)
}

// servicePID returns the process ID sc queryex reports for a service, or
// "?".
func servicePID(t *testing.T, name string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "sc", "queryex", name).CombinedOutput()
	if err != nil {
		return "?"
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "PID" {
			return strings.TrimSpace(value)
		}
	}

	return "?"
}

// checkNoSession checks a step with Restart Manager off: the action reports
// the missing session and the programs keep running.
func checkNoSession(t *testing.T, step, text string, procs map[string]*running) {
	t.Helper()

	if !strings.Contains(text, "BuilderRestart: no Restart Manager session") {
		t.Errorf("%s: the action did not report the missing session", step)
	}

	for name, proc := range procs {
		if got := proc.exited(time.Second); got != -1 {
			t.Errorf("%s: the %s exited with %d although Restart Manager was off", step, name, got)
		}
	}
}

// checkRegistered checks a step with Restart Manager on: the log has every
// registration and the programs are gone.
func checkRegistered(t *testing.T, step, text string, lines []string, procs map[string]*running) {
	t.Helper()

	for _, want := range lines {
		if !strings.Contains(text, want) {
			t.Errorf("%s: the log has no %q", step, want)
		}
	}

	for name, proc := range procs {
		if got := proc.exited(15 * time.Second); got == -1 {
			t.Errorf("%s: the %s is still running", step, name)
		}
	}
}

// closeRMConfig returns closeConfig with Restart Manager on, the
// installer's default, as another product.
func closeRMConfig() string {
	return strings.NewReplacer(
		"properties: { MSIRESTARTMANAGERCONTROL: Disable }\n", "",
		"MsiBuilderClose", "MsiBuilderCloseRM",
		"0079f4be-66b6-4022-936e-9f6d9cfcc7e8", "5b0e2d71-9c3a-4f86-b154-6a8e1d7c2f09",
	).Replace(closeConfig)
}

// TestMsiexecClosesAppsWithRestartManager repairs a package with close on
// its programs while Restart Manager is on (CLOSE-03). It expects that the
// program with a window gets close's WM_CLOSE before InstallValidate and
// exits with 0, that the console program is gone afterwards (by Restart
// Manager or by close's deferred action), that msiexec succeeds, and that a
// copy of the console program in another folder keeps running; it records
// what happens to the program with terminate: false.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecClosesAppsWithRestartManager(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	src := closeRMConfig()
	if strings.Contains(src, "MSIRESTARTMANAGERCONTROL") || !strings.Contains(src, "5b0e2d71") || !strings.Contains(src, "MsiBuilderCloseRM") {
		t.Fatal("closeRMConfig still turns Restart Manager off, or keeps the upgrade code or the product name")
	}

	helper := closeHelper(t)
	dir := t.TempDir()
	path := buildConfig(t, dir, "closerm", src, map[string][]byte{"gui.exe": helper, "cli.exe": helper, "keep.exe": helper})
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderCloseRM")

	defer func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").CombinedOutput()
		_ = os.RemoveAll(installDir)
	}()

	if code := msiexecCode(t, filepath.Join(dir, "install.log"), "/i", path); code != 0 {
		t.Fatalf("install failed with %d\n%s", code, readLog(filepath.Join(dir, "install.log")))
	}

	other := filepath.Join(t.TempDir(), "cli.exe")
	if err := os.WriteFile(other, helper, 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}

	elsewhere := startHelper(t, other, "console")

	for _, step := range []struct {
		name string
		args []string
	}{
		{"repair", []string{"/fomus", path}},
		{"uninstall", []string{"/x", path}},
	} {
		marker := filepath.Join(dir, step.name+".closed")
		gui := startHelper(t, filepath.Join(installDir, "gui.exe"), "window", marker)
		con := startHelper(t, filepath.Join(installDir, "cli.exe"), "console")
		keep := startHelper(t, filepath.Join(installDir, "keep.exe"), "console")

		waitFile(t, marker+".ready")

		log := filepath.Join(dir, step.name+".log")
		code := msiexecCode(t, log, step.args...)
		t.Logf("%s: msiexec exit code %d\n%s", step.name, code, restartLines(log))
		// terminate: false only keeps close from ending it; Restart
		// Manager may still shut it down. Record what happened.
		t.Logf("%s: keep.exe (terminate: false) exit code %d (-1: still running)", step.name, keep.exited(5*time.Second))

		// 3010: a program still held a file (keep.exe, if Restart Manager
		// could not shut it down) and the removal waits for a reboot.
		if code != 0 && code != 3010 {
			t.Fatalf("%s failed with %d\n%s", step.name, code, readLog(log))
		}

		if got := gui.exited(15 * time.Second); got != 0 {
			t.Errorf("%s: gui.exe exit code %d, want 0 after WM_CLOSE (-1: still running)", step.name, got)
		}

		if _, err := os.Stat(marker); err != nil {
			t.Errorf("%s: gui.exe got no WM_CLOSE: %v", step.name, err)
		}

		if got := con.exited(15 * time.Second); got == -1 {
			t.Errorf("%s: cli.exe is still running", step.name)
		}

		if got := elsewhere.exited(time.Second); got != -1 {
			t.Errorf("%s: the copy of cli.exe in another folder exited with %d", step.name, got)
		}

		_ = keep.cmd.Process.Kill()
		<-keep.done
	}
}

// restartAppConfig registers one program from the command line with
// Restart Manager (CLOSE-05).
const restartAppConfig = `schema: msi-builder/v1
product: { name: MsiBuilderRestartApp, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 6d2b9e40-1c7a-4f35-8b06-9e3a5c1d7f28 }
package: { platform: x64, compression: none }
installDir: { name: MsiBuilderRestartApp }
components:
  - files: [{ source: app.exe }]
    restartResources:
      - program: '[RESTART_PROGRAM]'
`

// TestMsiexecRestartsRegisteredProgram installs a package that registers a
// running program which called RegisterApplicationRestart more than 60
// seconds before (CLOSE-05), the case of a shell that must pick up a new
// extension: does Restart Manager shut it down, does the installer start
// it again, and when, compared with the end of msiexec?
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRestartsRegisteredProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	helper := closeHelper(t)
	dir := longPath(t.TempDir())
	path := buildConfig(t, dir, "restartapp", restartAppConfig, map[string][]byte{"app.exe": helper})
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "MsiBuilderRestartApp")

	defer func() {
		_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn", "/norestart").CombinedOutput()
		_ = os.RemoveAll(installDir)
	}()

	prog := filepath.Join(longPath(t.TempDir()), "shell.exe")
	if err := os.WriteFile(prog, helper, 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}

	marker := filepath.Join(dir, "shell")
	first := startHelper(t, prog, "restartable", marker)
	waitFile(t, marker+".ready")

	// Restart Manager restarts only programs that have run for 60 seconds
	// (documented for RegisterApplicationRestart).
	time.Sleep(65 * time.Second)

	t.Cleanup(func() {
		data, err := os.ReadFile(marker + ".restarted")
		if err != nil {
			return
		}

		var pid int
		if _, err := fmt.Sscan(string(data), &pid); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	})

	log := filepath.Join(dir, "install.log")
	code := msiexecCode(t, log, "/i", path, "RESTART_PROGRAM="+prog)
	end := time.Now().UTC()
	t.Logf("install: msiexec exit code %d, returned at %s\n%s", code, end.Format(time.RFC3339Nano), restartLines(log))

	if code != 0 && code != 3010 {
		t.Fatalf("install failed with %d\n%s", code, readLog(log))
	}

	if got := first.exited(15 * time.Second); got == -1 {
		t.Error("the registered program is still running")
	}

	_, err := os.Stat(marker + ".endsession")
	t.Logf("the program got WM_ENDSESSION: %v", err == nil)

	// Record whether and when it came back; the spec answers the timing.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if data, err := os.ReadFile(marker + ".restarted"); err == nil {
			t.Logf("restarted: %s (msiexec returned at %s)", data, end.Format(time.RFC3339Nano))

			return
		}
	}

	t.Log("the program was not restarted within 30 seconds after msiexec returned")
}
