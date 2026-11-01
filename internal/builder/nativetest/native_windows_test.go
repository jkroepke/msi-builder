//go:build windows

package nativetest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

var (
	msiDLL                      = syscall.NewLazyDLL("msi.dll")
	pMsiOpenDatabaseW           = msiDLL.NewProc("MsiOpenDatabaseW")
	pMsiDatabaseOpenViewW       = msiDLL.NewProc("MsiDatabaseOpenViewW")
	pMsiViewExecute             = msiDLL.NewProc("MsiViewExecute")
	pMsiViewFetch               = msiDLL.NewProc("MsiViewFetch")
	pMsiViewClose               = msiDLL.NewProc("MsiViewClose")
	pMsiCloseHandle             = msiDLL.NewProc("MsiCloseHandle")
	pMsiRecordGetStringW        = msiDLL.NewProc("MsiRecordGetStringW")
	pMsiRecordGetFieldCount     = msiDLL.NewProc("MsiRecordGetFieldCount")
	pMsiGetSummaryInformationW  = msiDLL.NewProc("MsiGetSummaryInformationW")
	pMsiSummaryInfoGetPropertyW = msiDLL.NewProc("MsiSummaryInfoGetPropertyW")
)

const (
	errorNoMoreItems = 259
	pidRevision      = 9
	pidTemplate      = 7
)

// call runs p and turns a non-zero result into an error. Convert pointers to
// uintptr only inside its argument list: like (*syscall.LazyProc).Call, it is
// a //go:uintptrescapes function, so the pointed-to values move to the heap
// and stay alive for the call. Otherwise an out-parameter can stay on the
// stack and msi.dll writes to a stale copy after a stack move.
//
//go:uintptrescapes
func call(name string, p *syscall.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if r != 0 {
		return fmt.Errorf("%s: error %d", name, r)
	}

	return nil
}

// wstr returns s as a NUL-terminated UTF-16 string. Convert the result to
// uintptr only inside the argument list of call or Call, so that the buffer
// stays alive until msi.dll has read it.
func wstr(t *testing.T, s string) *uint16 {
	t.Helper()

	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func closeHandle(h uint32) { _, _, _ = pMsiCloseHandle.Call(uintptr(h)) }

func openDB(t *testing.T, path string) uint32 {
	t.Helper()

	var h uint32
	if err := call("MsiOpenDatabaseW", pMsiOpenDatabaseW, uintptr(unsafe.Pointer(wstr(t, path))), 0 /* MSIDBOPEN_READONLY */, uintptr(unsafe.Pointer(&h))); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeHandle(h) })

	return h
}

func recordString(r uint32, field uintptr) string {
	buf := make([]uint16, 4096)
	n := uint32(len(buf))
	_, _, _ = pMsiRecordGetStringW.Call(uintptr(r), field, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))

	return syscall.UTF16ToString(buf[:n])
}

// query runs a SELECT and returns every row as strings.
func query(t *testing.T, db uint32, sql string) [][]string {
	t.Helper()

	var view uint32
	if err := call("MsiDatabaseOpenViewW", pMsiDatabaseOpenViewW, uintptr(db), uintptr(unsafe.Pointer(wstr(t, sql))), uintptr(unsafe.Pointer(&view))); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}

	defer closeHandle(view)

	if err := call("MsiViewExecute", pMsiViewExecute, uintptr(view), 0); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}

	var rows [][]string

	for {
		var rec uint32

		r, _, _ := pMsiViewFetch.Call(uintptr(view), uintptr(unsafe.Pointer(&rec)))
		if r == errorNoMoreItems {
			break
		}

		if r != 0 {
			t.Fatalf("%s: MsiViewFetch error %d", sql, r)
		}

		n, _, _ := pMsiRecordGetFieldCount.Call(uintptr(rec))
		row := make([]string, n)

		for i := range row {
			row[i] = recordString(rec, uintptr(i+1))
		}

		closeHandle(rec)
		rows = append(rows, row)
	}

	_, _, _ = pMsiViewClose.Call(uintptr(view))

	return rows
}

func summaryString(t *testing.T, db uint32, pid uintptr) string {
	t.Helper()

	var h uint32
	if err := call("MsiGetSummaryInformationW", pMsiGetSummaryInformationW, uintptr(db), 0, 0, uintptr(unsafe.Pointer(&h))); err != nil {
		t.Fatal(err)
	}

	defer closeHandle(h)

	var (
		typ  uint32
		ival int32
		ft   [2]uint32
		buf  = make([]uint16, 1024)
		size = uint32(len(buf))
	)

	if err := call("MsiSummaryInfoGetPropertyW", pMsiSummaryInfoGetPropertyW, uintptr(h), pid, uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&ival)), uintptr(unsafe.Pointer(&ft)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); err != nil {
		t.Fatal(err)
	}

	return syscall.UTF16ToString(buf[:size])
}

// buildExample builds examples/<name> with the given inputs and writes the
// package to a temporary file.
func buildExample(t *testing.T, name string, vars map[string]string, inputs map[string][]byte) (string, *builder.Result) {
	t.Helper()

	return exampletest.Build(t, name, vars, inputs)
}

func minimalInputs() map[string][]byte { return exampletest.Examples()["minimal"].Inputs }

// TestMsiDLLOpensPackages opens both examples with msi.dll, reads every
// table and the summary information.
func TestMsiDLLOpensPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("native tests need msi.dll time")
	}

	icon := append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0xAB}, 64)...)

	for _, tc := range []struct {
		name   string
		vars   map[string]string
		inputs map[string][]byte
	}{
		{"minimal", nil, minimalInputs()},
		{"windows_exporter", map[string]string{"VERSION": "0.32.0.5"}, map[string][]byte{
			"windows_exporter.exe": testpe.Build(testpe.Options{FileVersion: "0.32.0.5", Languages: []int{1033}}),
			"icon.ico":             icon,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, res := buildExample(t, tc.name, tc.vars, tc.inputs)
			db := openDB(t, path)

			if got := summaryString(t, db, pidRevision); got != res.PackageCode {
				t.Errorf("summary package code %q, want %q", got, res.PackageCode)
			}

			if got := summaryString(t, db, pidTemplate); !strings.HasPrefix(got, "x64;") {
				t.Errorf("template %q", got)
			}

			tables := query(t, db, "SELECT `Name` FROM `_Tables`")
			if len(tables) == 0 {
				t.Fatal("no tables")
			}

			for _, row := range tables {
				name := row[0]
				rows := query(t, db, "SELECT * FROM `"+name+"`")

				want := res.Database.Table(name)
				if want == nil {
					t.Errorf("msi.dll lists table %s that the builder did not write", name)

					continue
				}

				if len(rows) != len(want.Rows) {
					t.Errorf("%s: msi.dll reads %d rows, builder wrote %d", name, len(rows), len(want.Rows))
				}
			}

			props := query(t, db, "SELECT `Value` FROM `Property` WHERE `Property` = 'ProductCode'")
			if len(props) != 1 || props[0][0] != res.ProductCode {
				t.Errorf("ProductCode %v, want %s", props, res.ProductCode)
			}
		})
	}
}

// installExample says how an example is built and checked once installed.
type installExample struct {
	vars map[string]string
	// inputs builds the input files; it runs inside the subtest so an
	// example that needs a compiled binary pays for it only when it runs.
	inputs func(t *testing.T) map[string][]byte
	// installed is the path of the main payload after the installation; its
	// base name is the input it is compared with.
	installed string
	// service is the name of a service the package installs and starts.
	service string
	// properties are passed to msiexec /i.
	properties []string
}

// installExamples lists every example with its inputs and checks. A new
// example directory needs an entry here or TestMsiexecInstallsExamples fails.
func installExamples(t *testing.T) map[string]installExample {
	t.Helper()

	examples := exampletest.Examples()

	return map[string]installExample{
		"minimal": {
			inputs:    func(*testing.T) map[string][]byte { return minimalInputs() },
			installed: filepath.Join(os.Getenv("ProgramFiles"), "Hello", "hello.exe"),
		},
		"windows_exporter": {
			vars: examples["windows_exporter"].Vars,
			inputs: func(t *testing.T) map[string][]byte {
				t.Helper()

				// StartServices runs the installed binary as a service, so
				// the synthetic PE is replaced by a real one.
				_, inputs := windowsExporterInputs()
				inputs["windows_exporter.exe"] = testService(t)

				return inputs
			},
			installed: filepath.Join(os.Getenv("ProgramFiles"), "windows_exporter", "windows_exporter.exe"),
			service:   "windows_exporter",
			// The firewall rule is an optional feature.
			properties: []string{"ADDLOCAL=ALL"},
		},
		"branded": {
			vars:      examples["branded"].Vars,
			inputs:    brandedInputs,
			installed: filepath.Join(os.Getenv("ProgramFiles"), "Example", "Agent", "agent.exe"),
			service:   "ExampleAgent",
		},
		"desktop": {
			vars:      examples["desktop"].Vars,
			inputs:    func(*testing.T) map[string][]byte { return desktopInputs() },
			installed: filepath.Join(os.Getenv("ProgramFiles"), "Example Viewer", "viewer.exe"),
		},
	}
}

// brandedInputs are the input files of examples/branded with a service
// binary that really runs.
func brandedInputs(t *testing.T) map[string][]byte {
	t.Helper()

	inputs := exampletest.Examples()["branded"].Inputs
	inputs["agent-service.exe"] = testService(t)

	return inputs
}

// testService builds docs/spec/msitest/testsvc, a minimal Windows service
// that starts and stops on request. The source is copied into a temporary
// module next to a resource object from testpe, so the executable carries a
// version resource and passes the config validation (FILE-01) like a real
// service binary; the checked-in source stays unversioned for the spec
// tests.
func testService(t *testing.T) []byte {
	t.Helper()

	src := filepath.Join("..", "..", "..", "docs", "spec", "msitest")
	dir := t.TempDir()

	for _, name := range []string{filepath.Join("testsvc", "main.go"), "go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	syso := testpe.Syso(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(dir, "rsrc_windows_amd64.syso"), syso, 0o600); err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(dir, "testsvc.exe")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOARCH=amd64") //nolint:wsl_v5 // configures cmd

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test service: %v\n%s", err, out)
	}

	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// msiexecQuiet runs msiexec silently with a verbose log and prints the log on
// failure. It skips the test when the process is not elevated. A reboot
// request (3010) fails the test: the examples stop their own services, so
// a file that is still in use when RemoveFiles runs is a packaging bug.
// quietArgs keeps a repair from restarting the machine.
func msiexecQuiet(t *testing.T, log string, args ...string) {
	t.Helper()

	args = quietArgs(log, args...)

	//nolint:gosec // the arguments are test-controlled paths
	out, err := exec.CommandContext(t.Context(), "msiexec", args...).CombinedOutput()
	if err == nil {
		return
	}

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		switch exit.ExitCode() {
		case 1925:
			t.Skip("not elevated: msiexec returned 1925")
		case 3010:
			t.Fatalf("msiexec %s asked for a reboot (3010): a file was still in use\n%s", strings.Join(args, " "), readLog(log))
		}
	}

	t.Fatalf("msiexec %s: %v\n%s\n%s", strings.Join(args, " "), err, out, readLog(log))
}

// serviceState returns the STATE line of sc query, or "" when the service
// does not exist.
func serviceState(t *testing.T, name string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "sc", "query", name).CombinedOutput()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1060 {
			return ""
		}

		t.Fatalf("sc query %s: %v\n%s", name, err, out)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "STATE") {
			return strings.TrimSpace(line)
		}
	}

	t.Fatalf("sc query %s has no STATE line:\n%s", name, out)

	return ""
}

// TestMsiexecInstallsExamples installs and removes every example silently
// and checks the payload and services in between. It needs an elevated
// process; without one it is skipped.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecInstallsExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	examples := installExamples(t)

	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if _, ok := examples[entry.Name()]; !ok {
				t.Errorf("examples/%s has no entry in installExamples", entry.Name())
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(examples)) {
		example := examples[name]

		t.Run(name, func(t *testing.T) {
			inputs := example.inputs(t)
			path, res := buildExample(t, name, example.vars, inputs)
			log := filepath.Join(filepath.Dir(path), "install.log")
			installed := true

			// The top folder of the installation under Program Files, such
			// as Example for Example\Agent. The snapshot only sees what is
			// new, so a folder left by an earlier run would hide the files.
			rel, _ := filepath.Rel(os.Getenv("ProgramFiles"), example.installed)
			installDir := filepath.Join(os.Getenv("ProgramFiles"), strings.SplitN(rel, string(filepath.Separator), 2)[0])

			if _, err := os.Stat(installDir); err == nil {
				t.Fatalf("%s exists before the installation; remove what an earlier run left", installDir)
			}

			// Leave the machine clean when a check fails after installing,
			// and remove what the uninstall keeps on purpose.
			t.Cleanup(func() {
				if installed {
					_, _ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").CombinedOutput()
				}

				_ = os.RemoveAll(installDir)
			})

			pkg := newScope(res, pathLabels(filepath.Dir(path)), inputs)
			before := takeSnapshot(t, nil, pkg)

			msiexecQuiet(t, log, append([]string{"/i", path}, example.properties...)...)

			afterInstall := takeSnapshot(t, before, pkg)

			data, err := os.ReadFile(example.installed)
			if err != nil {
				t.Fatalf("after install: %v\n%s", err, readLog(log))
			}

			if !bytes.Equal(data, inputs[filepath.Base(example.installed)]) {
				t.Errorf("installed %s differs from the source", filepath.Base(example.installed))
			}

			if example.service != "" {
				if state := serviceState(t, example.service); !strings.Contains(state, "RUNNING") {
					t.Errorf("service %s after install: %q\n%s", example.service, state, readLog(log))
				}
			}

			msiexecQuiet(t, log, "/x", path)

			installed = false
			afterUninstall := takeSnapshot(t, before, pkg)

			if _, err := os.Stat(example.installed); err == nil {
				t.Errorf("%s is still installed after the uninstall", example.installed)
			}

			if example.service != "" {
				if state := serviceState(t, example.service); state != "" {
					t.Errorf("service %s after uninstall: %q", example.service, state)
				}
			}

			report := snapshotReport(name, res.ProductCode, changes(before, afterInstall), changes(before, afterUninstall))
			if !exampletest.CompareSnapshot(t, filepath.Join("testdata", "install", name+".txt"), report) {
				saveSnapshot(t, name, report)
			}
		})
	}
}

// readLog decodes the UTF-16 log msiexec writes.
func readLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		u := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			u = append(u, uint16(data[i])|uint16(data[i+1])<<8)
		}

		return syscall.UTF16ToString(u)
	}

	return string(data)
}
