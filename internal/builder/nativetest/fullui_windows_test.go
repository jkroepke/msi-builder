//go:build windows

package nativetest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/pkg/config"
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")           //nolint:gochecknoglobals // lazy DLL handle
	pProcessIDToSessionID      = kernel32.NewProc("ProcessIdToSessionId")     //nolint:gochecknoglobals // lazy procedure
	user32                     = syscall.NewLazyDLL("user32.dll")             //nolint:gochecknoglobals // lazy DLL handle
	pGetProcessWindowStation   = user32.NewProc("GetProcessWindowStation")    //nolint:gochecknoglobals // lazy procedure
	pGetUserObjectInformationW = user32.NewProc("GetUserObjectInformationW")  //nolint:gochecknoglobals // lazy procedure
	pMsiQueryProductStateW     = msiDLL.NewProc("MsiQueryProductStateW")      //nolint:gochecknoglobals // lazy procedure
	errFullUITimeout           = errors.New("msiexec did not finish in time") //nolint:gochecknoglobals // sentinel error
)

const (
	// The runs take 13 s and 36 s on an idle machine. Every wait stays short,
	// so one test fits go test's default 10-minute budget even when another
	// installation holds the machine at first.
	fullUITimeout       = 120 * time.Second
	driverTimeout       = fullUITimeout + 30*time.Second
	foreignMsiexecWait  = 2 * time.Minute
	fullUIBudget        = 8 * time.Minute
	fullUIName          = "MsiBuilderFullUI"
	fullUIErrorText     = "Forced error for the full UI test."
	installStateDefault = 5
)

const fullUIConfig = `vars:
  FILEVERSION: auto
---
schema: msi-builder/v1

product:
  name: MsiBuilderFullUI
  manufacturer: msi-builder tests
  version: {{ .VERSION | quote }}
  language: 1033
  upgradeCode: 8f3c2a61-4b7e-4d2a-9c15-6e0b7a9d3f21

package:
  platform: x64
  scope: perMachine
  installerVersion: "5.0"

upgrade:
  allowSameVersion: true
  downgrade: error
  removeOld: afterInstallInitialize

directories:
  - id: ProgramFiles64Folder
    children:
      - id: APPLICATIONFOLDER
        name: MsiBuilderFullUI
        configurable: true

features:
  - id: DefaultFeature
    title: MsiBuilderFullUI
    level: 1
    directory: APPLICATIONFOLDER
    components: [Tool]

components:
  - id: Tool
    directory: APPLICATIONFOLDER
    guid: 5B1E0C7A-3D2F-4A6E-9B8C-1D0E2F3A4B5C
    files:
      - source: {{ .SOURCE | quote }}
        name: tool.exe
        version: {{ .FILEVERSION | quote }}

customActions:
  - id: ForcedError
    error: Forced error for the full UI test.
    schedule:
      - { sequence: execute, before: InstallInitialize, condition: FORCEERR }

properties:
  # No default: the builder selects the first option (ICE34).
  LOG_LEVEL: {}

ui:
  enabled: true
  chooseFeatures: true
  dialogs:
    - id: ChoiceDlg
      title: Log level
      after: WelcomeDlg
      fields:
        - property: LOG_LEVEL
          label: Log level
          type: choice
          options:
            - value: warn
              label: '&Warnings'
            - info
            - value: debug
              label: '&Debug'
`

// fullUIDriver answers the wizard through UIAutomation: the -Choose radio
// button once, the -Advance buttons (Next, Install and Finish), "Close the applications" and OK on MsiRMFilesInUse, OK on ErrorDlg
// and on any unexpected error box. It writes every window it sees to -Out.
const fullUIDriver = `param([int]$MsiPid, [string]$Out, [string]$ErrorText, [string]$Choose = '', [string]$Advance = '^(Next|Install|Finish)$', [int]$Seconds = 110)
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
Add-Type -Namespace MsiBuilder -Name U -MemberDefinition '[DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, IntPtr l);'
$AE = [System.Windows.Automation.AutomationElement]
$TS = [System.Windows.Automation.TreeScope]
$All = [System.Windows.Automation.Condition]::TrueCondition
function Note($m) { Add-Content -Path $Out -Value $m -Encoding UTF8 }
# MSI dialogs expose their controls as panes, so match names and send BM_CLICK.
# The name is read first: a button that closes its dialog is gone afterwards.
function Press($els, [string]$pattern) {
  $b = $els | Where-Object { ($_.Current.Name -replace '[&<>]', '').Trim() -match $pattern -and $_.Current.IsEnabled -and $_.Current.NativeWindowHandle } | Select-Object -First 1
  if (-not $b) { return $null }
  $name = $b.Current.Name
  [MsiBuilder.U]::SendMessage([IntPtr]$b.Current.NativeWindowHandle, 0x00F5, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null
  return $name
}
$seen = @{}
$chosen = $false
$deadline = (Get-Date).AddSeconds($Seconds)
while ((Get-Date) -lt $deadline -and (Get-Process -Id $MsiPid -ErrorAction SilentlyContinue)) {
  Start-Sleep -Milliseconds 500
  # Only the windows of the msiexec this test started: other installs may
  # be running on the same desktop.
  foreach ($w in ($AE::RootElement.FindAll($TS::Children, $All) | Where-Object { $_.Current.ProcessId -eq $MsiPid })) {
    try { $els = @($w.FindAll($TS::Descendants, $All)) } catch { continue }
    $names = @($els | ForEach-Object { $_.Current.Name } | Where-Object { $_ })
    $text = $names -join ' | '
    if (-not $seen.ContainsKey($text)) { $seen[$text] = 1; Note "WINDOW: $text" }
    if ($text -match 'need to be updated by this setup') {
      Note ('RMDIALOG: radio ' + (Press $els '^Close the applications'))
      Start-Sleep -Milliseconds 300
      Note ('RMDIALOG: button ' + (Press $els '^OK$'))
      Start-Sleep 2
    } elseif ($ErrorText -and $names -contains $ErrorText) {
      Note "ERRORDLG: $text"
      Note ('ERRORDLG: button ' + (Press $els '^OK$'))
      Start-Sleep 1
    } elseif ($text -match 'unexpected error|error code is') {
      Note "UNEXPECTED: $text"
      Press $els '^OK$' | Out-Null
      Start-Sleep 1
    } else {
      if ($Choose -and -not $chosen) {
        $c = Press $els $Choose
        if ($c) { $chosen = $true; Note "CHOSE: $c"; Start-Sleep -Milliseconds 300 }
      }
      $n = Press $els $Advance
      if ($n) { Note ('PRESSED: ' + ($n -replace '&', '')); Start-Sleep 1 }
    }
  }
}
Note 'DRIVER: done'
`

// requireFullUI skips unless msiexec can show and drive a full UI here: an
// elevated process in an interactive desktop session, without -short.
func requireFullUI(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("full UI tests need msiexec time, elevation and a desktop")
	}

	if !elevated() {
		t.Skip("full UI tests need an elevated process")
	}

	var session uint32

	r, _, _ := pProcessIDToSessionID.Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&session)))
	if r == 0 || session == 0 || !interactive() {
		t.Skip("full UI tests need an interactive desktop session; session 0 drops msiexec to no UI")
	}

	if deadline, ok := t.Deadline(); ok && time.Until(deadline) < fullUIBudget {
		t.Skipf("full UI tests need %s of the test timeout", fullUIBudget)
	}
}

func elevated() bool {
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}

	var token syscall.Token
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &token); err != nil {
		return false
	}

	defer token.Close()

	var elevation, n uint32

	const tokenElevation = 20
	if err := syscall.GetTokenInformation(token, tokenElevation, (*byte)(unsafe.Pointer(&elevation)), 4, &n); err != nil {
		return false
	}

	return elevation != 0
}

// interactive reports whether the window station is visible, as
// Environment.UserInteractive does.
func interactive() bool {
	station, _, _ := pGetProcessWindowStation.Call()
	if station == 0 {
		return false
	}

	var flags struct {
		inherit, reserved int32
		flags             uint32
	}

	var n uint32

	const uoiFlags, wsfVisible = 1, 1

	r, _, _ := pGetUserObjectInformationW.Call(station, uoiFlags, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&n)))

	return r != 0 && flags.flags&wsfVisible != 0
}

// waitForMsiexec waits until no other msiexec client runs, so the tests
// never queue behind another installation.
func waitForMsiexec(t *testing.T) {
	t.Helper()

	const ps = `@(Get-CimInstance Win32_Process -Filter "Name='msiexec.exe'" | Where-Object { $_.CommandLine -notmatch 'msiexec\.exe"?\s+/V\s*$' }).Count`

	deadline := time.Now().Add(foreignMsiexecWait)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(t.Context(), "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
		if err == nil && strings.TrimSpace(string(out)) == "0" {
			return
		}

		time.Sleep(5 * time.Second)
	}

	t.Fatalf("another msiexec is still running after %s", foreignMsiexecWait)
}

// buildFullUI builds the full UI test package with tool.exe copied from
// PING.EXE, a runnable versioned console program.
func buildFullUI(t *testing.T, dir, version, fileVersion string) (string, *builder.Result) {
	t.Helper()

	return buildFullUIFrom(t, dir, fullUIConfig, version, fileVersion)
}

// buildFullUIFrom is buildFullUI with another configuration.
func buildFullUIFrom(t *testing.T, dir, doc, version, fileVersion string) (string, *builder.Result) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "msi.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	vars := map[string]string{
		"VERSION":     version,
		"FILEVERSION": fileVersion,
		"SOURCE":      filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"),
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{Vars: vars})
	if err != nil {
		t.Fatal(err)
	}

	res, err := builder.Build(cfg, builder.Options{BuildTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	if err != nil {
		t.Fatalf("%v\n%v", err, res.Diagnostics)
	}

	path := filepath.Join(dir, "fullui-"+version+".msi")
	if err := os.WriteFile(path, res.Package, 0o600); err != nil {
		t.Fatal(err)
	}

	return path, res
}

// msiexec runs msiexec and returns its exit code; a run that outlives
// fullUITimeout is killed with its process tree.
func msiexec(t *testing.T, args ...string) int {
	t.Helper()

	code, err := runMsiexec(args, nil)
	if err != nil {
		t.Fatalf("msiexec %v: %v", args, err)
	}

	return code
}

// runMsiexec starts msiexec, calls started with its PID and waits for it.
// Killing only the client would leave its dialogs behind, so a timeout
// kills the whole process tree.
func runMsiexec(args []string, started func(pid int)) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fullUITimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "msiexec", args...)
	cmd.Cancel = func() error {
		return exec.CommandContext(context.Background(), "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run() //nolint:gosec // a PID
	}
	// If taskkill fails, Wait falls back to killing the client itself.
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	if started != nil {
		started(cmd.Process.Pid)
	}

	err := cmd.Wait()

	if ctx.Err() != nil {
		return 0, errFullUITimeout
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), nil
	}

	return 0, err
}

// fullUI runs msiexec with the full UI while the driver answers it, and
// returns the exit code and what the driver saw.
func fullUI(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()

	return fullUIChoosing(t, dir, "", args...)
}

// fullUIChoosing is fullUI with the driver clicking the first radio button
// whose name matches choose.
func fullUIChoosing(t *testing.T, dir, choose string, args ...string) (int, string) {
	t.Helper()

	return fullUIDriven(t, dir, choose, "", args...)
}

// fullUIDriven is fullUIChoosing with the driver pressing the buttons whose
// names match advance instead of Next, Install and Finish.
func fullUIDriven(t *testing.T, dir, choose, advance string, args ...string) (int, string) {
	t.Helper()

	driver := filepath.Join(dir, "driver.ps1")
	if err := os.WriteFile(driver, []byte(fullUIDriver), 0o600); err != nil {
		t.Fatal(err)
	}

	// The driver appends, so a second run in the same folder starts empty.
	seen := filepath.Join(dir, "driver.txt")
	_ = os.Remove(seen)

	var drv *exec.Cmd

	ctx, cancel := context.WithTimeout(context.Background(), driverTimeout)
	defer cancel()

	code, err := runMsiexec(args, func(pid int) {
		args := []string{
			"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", driver, "-MsiPid", strconv.Itoa(pid), "-Out", seen, "-ErrorText", fullUIErrorText,
			"-Seconds", strconv.Itoa(int((fullUITimeout - 10*time.Second).Seconds())),
		}
		if choose != "" {
			args = append(args, "-Choose", choose)
		}

		if advance != "" {
			args = append(args, "-Advance", advance)
		}

		drv = exec.CommandContext(ctx, "powershell", args...)
		if err := drv.Start(); err != nil {
			t.Errorf("driver: %v", err)

			drv = nil
		}
	})

	if drv != nil {
		_ = drv.Wait()
	}

	data, _ := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("msiexec %v: %v\nUI:\n%s", args, err, data)
	}

	return code, string(data)
}

func productInstalled(t *testing.T, code string) bool {
	t.Helper()

	r, _, _ := pMsiQueryProductStateW.Call(uintptr(unsafe.Pointer(wstr(t, code))))

	return r == installStateDefault
}

// cleanupFullUI removes whatever version of the test product is installed.
func cleanupFullUI(t *testing.T, packages ...string) {
	t.Helper()

	t.Cleanup(func() {
		for _, pkg := range packages {
			_, _ = runMsiexec([]string{"/x", pkg, "/qn"}, nil)
		}

		_ = os.RemoveAll(filepath.Join(os.Getenv("ProgramFiles"), fullUIName))
	})
}

// TestFullUIRestartManager upgrades while the old tool.exe runs from the
// install folder and chooses "Close the applications" on MsiRMFilesInUse:
// the Restart Manager stops the process and the upgrade succeeds.
func TestFullUIRestartManager(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	dir := t.TempDir()
	oldPkg, _ := buildFullUI(t, dir, "1.0.0", "auto")
	// A higher file version than PING.EXE makes the engine replace the file
	// that the running process holds.
	newPkg, newRes := buildFullUI(t, dir, "1.0.1", "99.0.0.0")

	cleanupFullUI(t, newPkg, oldPkg)

	if code := msiexec(t, "/i", oldPkg, "/qn", "/l*v", filepath.Join(dir, "old.log")); code != 0 {
		t.Fatalf("installing the old package: exit %d\n%s", code, readLog(filepath.Join(dir, "old.log")))
	}

	tool := exec.CommandContext(context.Background(), filepath.Join(os.Getenv("ProgramFiles"), fullUIName, "tool.exe"), "-n", "3000", "127.0.0.1") //nolint:gosec // the installed test program
	// Its own hidden console: the Restart Manager stops console programs with
	// Ctrl+C, which must not reach this test.
	tool.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x10 /* CREATE_NEW_CONSOLE */, HideWindow: true}
	if err := tool.Start(); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})

	go func() { _ = tool.Wait(); close(stopped) }()

	t.Cleanup(func() { _ = tool.Process.Kill() })

	time.Sleep(2 * time.Second)

	log := filepath.Join(dir, "upgrade.log")
	code, seen := fullUI(t, dir, "/i", newPkg, "/l*v", log)
	text := readLog(log)

	if code != 0 {
		t.Errorf("full UI upgrade: exit %d, want 0", code)
	}

	if !strings.Contains(seen, "RMDIALOG: button OK") {
		t.Error("MsiRMFilesInUse was not shown or not answered")
	}

	if strings.Contains(text, "Error 2866") || strings.Contains(text, "error code is 2866") {
		t.Error("the log reports error 2866")
	}

	if strings.Contains(seen, "UNEXPECTED:") {
		t.Error("the engine showed an unexpected error box")
	}

	if !strings.Contains(text, "RESTART MANAGER: Successfully shut down all applications") {
		t.Error("the Restart Manager did not shut down the process")
	}

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Error("tool.exe is still running after the upgrade")
	}

	if !productInstalled(t, newRes.ProductCode) {
		t.Error("the new version is not installed")
	}

	if t.Failed() {
		t.Logf("UI:\n%s\nlog excerpt:\n%s", seen, excerpt(text, "RESTART MANAGER", "Error 28", "error code is"))
	}
}

// TestFullUIErrorDialog shows ErrorDlg through a type-19 action and checks
// that the engine draws it without errors 2835 (no ErrorIcon) or 2888.
func TestFullUIErrorDialog(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	dir := t.TempDir()
	pkg, _ := buildFullUI(t, dir, "1.0.0", "auto")

	cleanupFullUI(t, pkg)

	log := filepath.Join(dir, "error.log")
	code, seen := fullUI(t, dir, "/i", pkg, "FORCEERR=1", "/l*v", log)
	text := readLog(log)

	if code != 1603 {
		t.Errorf("full UI install: exit %d, want 1603", code)
	}

	if !strings.Contains(seen, "ERRORDLG: ") || !strings.Contains(seen, "ERRORDLG: button OK") {
		t.Error("ErrorDlg did not show the error text or was not answered")
	}

	if strings.Contains(seen, "UNEXPECTED:") {
		t.Error("the engine showed an unexpected error box")
	}

	for _, code := range []string{"2835", "2888"} {
		if strings.Contains(text, "Error "+code) || strings.Contains(text, "error code is "+code) {
			t.Errorf("the log reports error %s", code)
		}
	}

	if t.Failed() {
		t.Logf("UI:\n%s\nlog excerpt:\n%s", seen, excerpt(text, "Error 28", "error code is", fullUIErrorText))
	}
}

// TestFullUIChoice selects the third radio button of the added choice
// dialog and checks that the server sees its value (UI-05).
func TestFullUIChoice(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	dir := t.TempDir()
	pkg, _ := buildFullUI(t, dir, "1.0.0", "auto")

	cleanupFullUI(t, pkg)

	log := filepath.Join(dir, "choice.log")
	code, seen := fullUIChoosing(t, dir, "^Debug$", "/i", pkg, "/l*v", log)
	text := readLog(log)

	if code != 0 {
		t.Errorf("full UI install: exit %d, want 0", code)
	}

	if !strings.Contains(seen, "CHOSE: ") {
		t.Error("the driver found no Debug radio button")
	}

	if got := serverProperties(text)["LOG_LEVEL"]; got != "debug" {
		t.Errorf("LOG_LEVEL on the server = %q, want debug", got)
	}

	if strings.Contains(seen, "UNEXPECTED:") {
		t.Error("the engine showed an unexpected error box")
	}

	t.Logf("UI-05 UI:\n%s\nlog excerpt:\n%s", seen, excerpt(text, "LOG_LEVEL", "Error 28"))
}

// TestFullUIFinishText installs with the full UI, where the finish page
// shows ui.finishText, then repairs through the maintenance menu, where the
// finish page hides it.
func TestFullUIFinishText(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	requireFullUI(t)
	waitForMsiexec(t)

	const finishText = "Open a new command prompt to use [ProductName] & its tools."

	// UI Automation names drop "&" even with NoPrefix, which the page shows
	// (UI-01), so the checks look for the part before it.
	want := "Open a new command prompt to use " + fullUIName

	doc := strings.Replace(fullUIConfig, "  enabled: true\n", "  enabled: true\n  finishText: "+finishText+"\n", 1)
	if !strings.Contains(doc, "finishText:") {
		t.Fatal("fullUIConfig has no enabled: true line to add finishText to")
	}

	dir := t.TempDir()
	pkg, _ := buildFullUIFrom(t, dir, doc, "1.0.0", "auto")

	cleanupFullUI(t, pkg)

	code, install := fullUI(t, dir, "/i", pkg)
	if code != 0 {
		t.Fatalf("full UI install: exit %d, want 0\nUI:\n%s", code, install)
	}

	if !finishShows(install, want) {
		t.Errorf("the finish page of the installation does not show %q", want)
	}

	code, repair := fullUIDriven(t, dir, "", "^(Next|Repair|Finish)$", "/i", pkg)
	if code != 0 {
		t.Fatalf("full UI repair: exit %d, want 0\nUI:\n%s", code, repair)
	}

	if !strings.Contains(repair, "PRESSED: Repair") || !strings.Contains(repair, "Completed the "+fullUIName) {
		t.Error("the driver did not repair through the maintenance menu")
	}

	if finishShows(repair, want) {
		t.Errorf("the finish page of the repair shows %q", want)
	}

	if strings.Contains(install+repair, "UNEXPECTED:") {
		t.Error("the engine showed an unexpected error box")
	}

	t.Logf("UI-07 install:\n%s\nrepair:\n%s", install, repair)
}

// finishShows reports whether any finish page the driver saw shows s. The
// driver logs the page once, then presses Finish. A control hidden by a
// ControlCondition is not in the control view the driver walks (UI-07), so
// the WINDOW line lists only the shown texts.
func finishShows(seen, paragraph string) bool {
	for line := range strings.SplitSeq(seen, "\n") {
		if strings.HasPrefix(line, "WINDOW: ") && strings.Contains(line, "Completed the ") && strings.Contains(line, paragraph) {
			return true
		}
	}

	return false
}

// TestICEFullUI runs the ICE suite on the full-UI package, including ICE34
// on the choice dialog.
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEFullUI(t *testing.T) {
	cub := iceCube(t)
	pkg, _ := buildFullUI(t, t.TempDir(), "1.0.0", "auto")
	requireCleanICE(t, pkg, cub)
}

// excerpt returns the log lines that contain any of the markers.
func excerpt(log string, markers ...string) string {
	var lines strings.Builder

	for line := range strings.Lines(log) {
		for _, m := range markers {
			if strings.Contains(line, m) {
				lines.WriteString(line)

				break
			}
		}
	}

	return lines.String()
}
