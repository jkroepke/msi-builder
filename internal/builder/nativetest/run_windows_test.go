package nativetest_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// buildRunProbe builds internal/cadll/testdata/probe for a platform.
func buildRunProbe(t *testing.T, arch string) string {
	t.Helper()

	src := filepath.Join("..", "..", "..", "internal", "cadll", "testdata", "probe")
	dir := t.TempDir()

	for _, name := range []string{"msi.yaml", "readme.txt"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{Vars: map[string]string{"ARCH": arch}})
	if err != nil {
		t.Fatal(err)
	}

	res, err := builder.Build(cfg, builder.Options{BuildTime: exampletest.BuildTime})
	if err != nil {
		t.Fatalf("%v\n%v", err, res.Diagnostics)
	}

	path := filepath.Join(dir, "probe-"+arch+".msi")
	if err := os.WriteFile(path, res.Package, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// msiexecCode runs msiexec with a verbose log and returns its exit code.
func msiexecCode(t *testing.T, log string, args ...string) int {
	t.Helper()

	//nolint:gosec // the arguments are test-controlled paths
	out, err := exec.CommandContext(t.Context(), "msiexec", append(args, "/qn", "/norestart", "/l*v", log)...).CombinedOutput()
	if err == nil {
		return 0
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() == 1925 {
			t.Skip("not elevated: msiexec returned 1925")
		}

		return exit.ExitCode()
	}

	t.Fatalf("msiexec %s: %v\n%s", strings.Join(args, " "), err, out)

	return -1
}

// TestMsiexecRunActions installs the run action probe (CA-03): the embedded
// custom action DLL runs each command and copies its output into the log.
// Each runner installs the package of its own platform. An x64 package
// under emulation on arm64 was confirmed once (CA-03) and is not repeated:
// it tests Windows' emulation, not the DLL choice, and doubles the run.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecRunActions(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	archs := []string{"x64"}
	if runtime.GOARCH == "arm64" {
		archs = []string{"arm64"}
	}

	for _, arch := range archs {
		t.Run(arch, func(t *testing.T) {
			path := buildRunProbe(t, arch)
			dir := filepath.Dir(path)

			t.Cleanup(func() {
				_ = exec.Command("msiexec", "/x", path, "/qn", "/norestart").Run()
				_ = os.RemoveAll(filepath.Join(os.Getenv("ProgramFiles"), "BuilderRunProbe"))
			})

			installLog := filepath.Join(dir, "install.log")
			if code := msiexecCode(t, installLog, "/i", path); code != 0 {
				t.Fatalf("install: exit %d\n%s", code, readLog(installLog))
			}

			log := readLog(installLog)

			for _, want := range []string{
				"BuilderRun: deferred-system BuilderRunProbe",
				"BuilderRun: nt authority\\system",
				"BuilderRun: brackets [ProductName] stay literal",
				"BuilderRun: oem-umlaut-äöü",
				"BuilderRun: utf8-umlaut-äöü-euro-€",
				"BuilderRun: readme.txt",
				"BuilderRun: stderr-line",
				"BuilderRun: deferred-user",
				"BuilderRun: powershell says 5.1",
				"oops",
				"BuilderRun: ignored-exit-3",
				"BuilderRun: exit code 3",
				"BuilderRun: immediate-after-finalize",
				"BuilderRun: exit code 0",
				"BuilderRun: cr-one",
				"BuilderRun: cr-two",
				"BuilderRun: cr-three",
				"BuilderRun: äsplit-tail",
				"BuilderRun: background-spawned",
				"BuilderRun: a process the command started keeps the output open",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("install log lacks %q", want)
				}
			}

			if strings.Contains(log, "BuilderRun: background-late") {
				t.Error("the process ProbeBackground left behind was not ended")
			}

			if strings.Contains(log, "BuilderRun: about-to-fail") {
				t.Error("ProbeFails ran without FAILPROBE=1")
			}

			if strings.Contains(log, "BuilderRun: rollback-ran") {
				t.Error("ProbeRollback ran during a successful install")
			}

			uninstallLog := filepath.Join(dir, "uninstall.log")
			if code := msiexecCode(t, uninstallLog, "/x", path); code != 0 {
				t.Fatalf("uninstall: exit %d\n%s", code, readLog(uninstallLog))
			}

			failLog := filepath.Join(dir, "fail.log")
			if code := msiexecCode(t, failLog, "/i", path, "FAILPROBE=1"); code != 1603 {
				t.Fatalf("install with FAILPROBE=1: exit %d, want 1603\n%s", code, readLog(failLog))
			}

			log = readLog(failLog)

			for _, want := range []string{
				"BuilderRun: about-to-fail",
				"BuilderRun: exit code 7",
				"BuilderRun: rollback-ran",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("failed-install log lacks %q", want)
				}
			}

			if strings.Contains(log, "echo about-to-fail") {
				t.Error("the hidden command line of ProbeFails is in the log")
			}

			if t.Failed() {
				t.Logf("install log:\n%s\nfailed-install log:\n%s", readLog(installLog), log)
			}
		})
	}
}
