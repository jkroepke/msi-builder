//go:build windows

package nativetest_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/memtest"
	"github.com/jkroepke/msi-builder/pkg/config"
)

var pGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// cabinetProbe is the package of the media tests. %s is the package section
// below platform.
const cabinetProbe = `schema: msi-builder/v1
product:
  name: Cabinet Probe
  manufacturer: Example
  version: 1.0.0
  upgradeCode: aada8e39-4267-4af1-95c2-656eae31be9d
package:
  platform: x64
%s
components:
  - files:
%s
`

// probeFile is a payload file of the cabinet probe, written from data.
type probeFile struct {
	data func() io.Reader
	name string
	size int64
}

// buildProbe writes the probe's files and configuration to a new folder and
// builds the package there.
func buildProbe(t *testing.T, pkg string, files []probeFile, opts builder.Options) (string, *builder.Result) {
	t.Helper()

	dir := t.TempDir()

	var list strings.Builder

	for _, file := range files {
		writeProbeFile(t, filepath.Join(dir, file.name), file.data())
		fmt.Fprintf(&list, "      - source: %s\n", file.name)
	}

	cfgPath := filepath.Join(dir, "msi.yaml")
	if err := os.WriteFile(cfgPath, fmt.Appendf(nil, cabinetProbe, pkg, list.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath, config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	opts.Output = filepath.Join(dir, "out", "probe.msi")
	if err := os.Mkdir(filepath.Dir(opts.Output), 0o750); err != nil {
		t.Fatal(err)
	}

	res, err := builder.Build(cfg, opts)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	return opts.Output, res
}

func writeProbeFile(t *testing.T, path string, data io.Reader) {
	t.Helper()

	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(out, data); err != nil {
		t.Fatal(err)
	}

	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// installProbe installs the package at path silently, checks every file's
// size and SHA-256 in Program Files\Cabinet Probe, and removes it again.
func installProbe(t *testing.T, path string, files []probeFile) {
	t.Helper()

	log := filepath.Join(t.TempDir(), "install.log")

	start := time.Now()

	msiexecQuiet(t, log, "/i", path)

	t.Cleanup(func() { _ = os.Remove(log) })
	t.Logf("installed in %v", time.Since(start).Round(time.Second))

	removed := false

	// Not t.Cleanup: msiexecQuiet runs under t.Context, which is cancelled
	// before cleanups run.
	defer func() {
		if !removed {
			msiexecQuiet(t, filepath.Join(t.TempDir(), "cleanup.log"), "/x", path)
		}
	}()

	dir := filepath.Join(os.Getenv("ProgramFiles"), "Cabinet Probe")

	for _, file := range files {
		installed, err := os.Open(filepath.Join(dir, file.name))
		if err != nil {
			t.Fatal(err)
		}

		got := sha256.New()
		size, err := io.Copy(got, installed)
		_ = installed.Close()

		if err != nil {
			t.Fatal(err)
		}

		want := sha256.New()
		_, _ = io.Copy(want, file.data())

		if size != file.size || !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
			t.Errorf("%s: installed %d bytes that differ from the source of %d", file.name, size, file.size)
		}
	}

	msiexecQuiet(t, filepath.Join(t.TempDir(), "uninstall.log"), "/x", path)

	removed = true

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the uninstall left %s: %v", dir, err)
	}
}

// smallProbeFiles are three files of 40000 bytes each.
func smallProbeFiles() []probeFile {
	files := make([]probeFile, 0, 3)

	for i := range 3 {
		data := bytes.Repeat([]byte{byte('a' + i)}, 40000)
		files = append(files, probeFile{
			name: fmt.Sprintf("data%d.bin", i),
			size: int64(len(data)),
			data: func() io.Reader { return bytes.NewReader(data) },
		})
	}

	return files
}

// TestMsiexecInstallsSeveralCabinets installs packages whose files are split
// across three cabinets, embedded and beside the package with a long cabinet
// name that has spaces (CAB-13).
func TestMsiexecInstallsSeveralCabinets(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	files := smallProbeFiles()
	opts := builder.Options{BuildTime: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), CabinetSize: 1}

	for _, tc := range []struct {
		name, pkg string
	}{
		{"embedded", "  compression: mszip\n  cabinet: CabinetProbe.cab"},
		{"external", "  compression: mszip\n  cabinet: Cabinet Probe Long Name.cab\n  embedCabinets: false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, res := buildProbe(t, tc.pkg, files, opts)

			if tc.name == "external" && len(res.Cabinets) != 3 {
				t.Fatalf("cabinets %+v, want three", res.Cabinets)
			}

			installProbe(t, path, files)
		})
	}
}

// TestMsiexecInstallsLargePackage installs a package of about 2.9 GB in two
// cabinets beside the .msi, and checks that msi-builder refuses to embed the
// same files (CAB-10; whether Windows would install such a package is not
// tested). It writes about 9 GB, so it runs only with
// MSI_BUILDER_LARGE_PACKAGE=1.
func TestMsiexecInstallsLargePackage(t *testing.T) {
	if testing.Short() || os.Getenv("MSI_BUILDER_LARGE_PACKAGE") != "1" {
		t.Skip("writes about 9 GB; set MSI_BUILDER_LARGE_PACKAGE=1")
	}

	// The sources, the cabinets and the installed files each take the
	// payload once.
	const need = 10 << 30

	if free := freeBytes(t, os.TempDir()); free < need {
		t.Fatalf("%s has %d bytes free, the test needs %d", os.TempDir(), free, need)
	}

	files := make([]probeFile, 0, 3)

	// Sizes differ, so that no file is a copy of another (CAB-14).
	for i, size := range []int64{1 << 30, 1<<30 + 4096, 900 << 20} {
		files = append(files, probeFile{
			name: fmt.Sprintf("big%d.bin", i),
			size: size,
			data: func() io.Reader { return memtest.Pattern(size) },
		})
	}

	opts := builder.Options{BuildTime: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}
	start := time.Now()

	path, res := buildProbe(t, "  compression: none\n  embedCabinets: false", files, opts)

	t.Logf("built in %v: package of %d bytes", time.Since(start).Round(time.Second), res.Size)

	var total int64

	for _, cabinet := range res.Cabinets {
		t.Logf("cabinet %s: %d bytes", cabinet.Name, cabinet.Size)
		total += cabinet.Size
	}

	// big0.bin and big1.bin do not fit one cabinet; big1.bin and big2.bin
	// do.
	if len(res.Cabinets) != 2 || total <= 2<<30 {
		t.Fatalf("%d cabinets of %d bytes in all, want two above 2 GiB", len(res.Cabinets), total)
	}

	installProbe(t, path, files)

	// Embedded, the same cabinets would make a package above 2 GB.
	cfg, err := config.Load(filepath.Join(filepath.Dir(filepath.Dir(path)), "msi.yaml"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.Package.EmbedCabinets = nil

	_, err = builder.Build(cfg, builder.Options{BuildTime: opts.BuildTime, Output: filepath.Join(filepath.Dir(path), "embedded.msi")})
	if err == nil || !strings.Contains(err.Error(), "embedCabinets") {
		t.Errorf("the embedded build gave %v, want the 2 GB error", err)
	}
}

// freeBytes is the space free to the caller on the volume of dir.
func freeBytes(t *testing.T, dir string) int64 {
	t.Helper()

	var free uint64

	ok, _, err := pGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(wstr(t, dir))), uintptr(unsafe.Pointer(&free)), 0, 0)
	if ok == 0 {
		t.Fatalf("GetDiskFreeSpaceExW %s: %v", dir, err)
	}

	return int64(free)
}

// TestMsiexecInstallsSharedData installs packages in which identical files
// share their cabinet bytes (CAB-14): a.bin, c.bin and e.bin hold the same
// 300000 bytes and f.bin and g.bin other ones, with stored files between
// them, so that every copy points back at a folder before the one the
// extraction is in, and the empty h.bin after the last copy makes an empty
// last folder. MSZIP and LZX, embedded and beside the package.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecInstallsSharedData(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	content := func(seed byte, size int) func() io.Reader {
		data := make([]byte, size)
		state := uint32(seed) + 1

		for i := range data {
			state = state*1664525 + 1013904223
			data[i] = byte(state >> 24)
		}

		return func() io.Reader { return bytes.NewReader(data) }
	}

	same, other := content(1, 300000), content(2, 70000)
	files := []probeFile{
		{name: "a.bin", size: 300000, data: same},
		{name: "b.bin", size: 50000, data: content(3, 50000)},
		{name: "c.bin", size: 300000, data: same},
		{name: "d.bin", size: 40000, data: content(4, 40000)},
		{name: "e.bin", size: 300000, data: same},
		{name: "f.bin", size: 70000, data: other},
		{name: "g.bin", size: 70000, data: other},
		// After the last copy, an empty file is an empty last folder.
		{name: "h.bin", size: 0, data: content(5, 0)},
	}

	var stored int64

	for _, file := range files {
		stored += file.size
	}

	// Without sharing, the random bytes would not compress.
	stored -= 2*300000 + 70000

	opts := builder.Options{BuildTime: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}

	for _, tc := range []struct {
		name, pkg string
	}{
		{"mszip embedded", "  compression: mszip"},
		{"lzx15 embedded", "  compression: lzx:15"},
		{"lzx external", "  compression: lzx\n  embedCabinets: false"},
	} {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest // see above
			path, res := buildProbe(t, tc.pkg, files, opts)

			size := res.Size
			for _, cabinet := range res.Cabinets {
				size += cabinet.Size
			}

			if size > stored+200000 {
				t.Fatalf("package and cabinets take %d bytes for %d stored bytes", size, stored)
			}

			installProbe(t, path, files)
		})
	}
}
