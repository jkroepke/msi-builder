//go:build windows

package cfb_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/cfb"
)

// These tests open compound files with the structured storage of Windows
// (ole32 StgOpenStorage and IStorage::OpenStream, through syscall without
// cgo) to check that the reader sees the names Windows sees. They run only
// without -short.

var (
	ole32           = syscall.NewLazyDLL("ole32.dll") //nolint:gochecknoglobals // one DLL table for the file
	pCoInitializeEx = ole32.NewProc("CoInitializeEx") //nolint:gochecknoglobals // one DLL table for the file
	pCoUninitialize = ole32.NewProc("CoUninitialize") //nolint:gochecknoglobals // one DLL table for the file
	pStgOpenStorage = ole32.NewProc("StgOpenStorage") //nolint:gochecknoglobals // one DLL table for the file
)

// errStorageNotFound starts the result of a name OpenStream did not find.
const errStorageNotFound = "no such stream"

// Vtable slots and modes of the calls below.
const (
	slotRelease    = 2 // IUnknown::Release
	slotRead       = 3 // ISequentialStream::Read
	slotOpenStream = 4 // IStorage::OpenStream

	coinitApartmentThreaded = 2
	stgmRead                = 0x00000000
	stgmShareExclusive      = 0x00000010
	stgmShareDenyWrite      = 0x00000020
)

// comCall calls the method in slot of a COM object. It is a
// //go:uintptrescapes function: convert pointers to uintptr only inside its
// argument list, so that out-parameters move to the heap (AGENTS.md).
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(**[8]uintptr)(obj)
	r, _, _ := syscall.SyscallN(vtbl[slot], append([]uintptr{uintptr(obj)}, args...)...)

	return r
}

// stgOpen calls StgOpenStorage.
//
//go:uintptrescapes
func stgOpen(args ...uintptr) uintptr {
	r, _, _ := pStgOpenStorage.Call(args...)

	return r
}

func wstr(t *testing.T, s string) *uint16 {
	t.Helper()

	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// nativeStreams opens the compound file at path with StgOpenStorage and
// reads each named root-level stream with IStorage::OpenStream. A name
// that OpenStream does not find maps to errStorageNotFound plus the
// HRESULT; the returned HRESULT is that of StgOpenStorage.
func nativeStreams(t *testing.T, path string, names ...string) (map[string]string, uint32) {
	t.Helper()

	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	if hr, _, _ := pCoInitializeEx.Call(0, coinitApartmentThreaded); int32(hr) < 0 { //nolint:gosec // an HRESULT
		t.Fatalf("CoInitializeEx: 0x%08X", uint32(hr)) //nolint:gosec // an HRESULT
	}
	defer func() { _, _, _ = pCoUninitialize.Call() }()

	var storage unsafe.Pointer

	if hr := stgOpen(uintptr(unsafe.Pointer(wstr(t, path))), 0, stgmRead|stgmShareDenyWrite, 0, 0, uintptr(unsafe.Pointer(&storage))); int32(hr) < 0 { //nolint:gosec // an HRESULT
		return nil, uint32(hr) //nolint:gosec // an HRESULT
	}
	defer comCall(storage, slotRelease)

	got := make(map[string]string, len(names))

	for _, name := range names {
		var stream unsafe.Pointer

		result := comCall(storage, slotOpenStream, uintptr(unsafe.Pointer(wstr(t, name))), 0, stgmRead|stgmShareExclusive, 0, uintptr(unsafe.Pointer(&stream)))
		if int32(result) < 0 { //nolint:gosec // an HRESULT
			got[name] = errStorageNotFound + " " + hresult(result)

			continue
		}

		buf := make([]byte, 256)

		var read uint32

		result = comCall(stream, slotRead, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&read)))
		comCall(stream, slotRelease)

		if int32(result) < 0 { //nolint:gosec // an HRESULT
			t.Fatalf("%s: IStream::Read(%q): %s", path, name, hresult(result))
		}

		got[name] = string(buf[:read])
	}

	return got, 0
}

func hresult(hr uintptr) string {
	return fmt.Sprintf("0x%08X", uint32(hr)) //nolint:gosec // an HRESULT
}

// renamed writes streams with the given ASCII names and data, then
// overwrites the stored name units of each name in from with the units of
// the name at the same position in to, which must have the same length.
func renamed(t *testing.T, streams []cfb.Stream, from, into []string) []byte {
	t.Helper()

	raw, err := cfb.Write(streams, cfb.Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	for i := range from {
		old := utf16le(from[i] + "\x00")
		repl := utf16le(into[i] + "\x00")

		if len(old) != len(repl) || bytes.Count(raw, old) != 1 {
			t.Fatalf("cannot rename %q to %q", from[i], into[i])
		}

		raw = bytes.Replace(raw, old, repl, 1)
	}

	return raw
}

func utf16le(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(units))

	for i, unit := range units {
		binary.LittleEndian.PutUint16(out[2*i:], unit)
	}

	return out
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestNativeCaseAliasesCFB10: two siblings whose names differ only in the
// case of a non-ASCII letter are one name to Windows: IStorage::OpenStream
// finds the same stream by both spellings. The reader rejects such a file,
// so it never shows two streams where Windows sees one (CFB-10).
func TestNativeCaseAliasesCFB10(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("native storage tests")
	}

	streams := []cfb.Stream{{Name: "Xbcd", Data: []byte("first")}, {Name: "Ybcd", Data: []byte("second")}}

	for _, pair := range [][2]string{{"Ébcd", "ébcd"}, {"ÿbcd", "Ÿbcd"}, {"Σbcd", "ςbcd"}, {"Ａbcd", "ａbcd"}, {"Ibcd", "ıbcd"}} {
		raw := renamed(t, streams, []string{"Xbcd", "Ybcd"}, pair[:])

		if _, err := cfb.Read(raw); err == nil || !strings.Contains(err.Error(), "duplicate name") {
			t.Errorf("CFB-10 %s/%s: cfb.Read = %v, want a duplicate name error", pair[0], pair[1], err)
		}

		got, status := nativeStreams(t, writeFile(t, "alias.cfb", raw), pair[0], pair[1])
		t.Logf("CFB-10 %s/%s: StgOpenStorage 0x%08X, OpenStream %q", pair[0], pair[1], status, got)

		// Only É/é is asserted. The reader also rejects the other pairs,
		// whether or not Windows folds them: rejecting a pair Windows keeps
		// apart is safe, the log records which ones it folds.
		if pair[0] == "Ébcd" && (status != 0 || got[pair[0]] != got[pair[1]] || strings.HasPrefix(got[pair[0]], errStorageNotFound)) {
			t.Errorf("CFB-10 %s/%s: Windows did not resolve both spellings to one stream: 0x%08X %q", pair[0], pair[1], status, got)
		}
	}

	// Control: different letters stay different streams, for Windows and
	// for the reader. Á sorts before É, so the tree order stays valid.
	raw := renamed(t, streams, []string{"Xbcd", "Ybcd"}, []string{"Ábcd", "Ébcd"})
	if _, err := cfb.Read(raw); err != nil {
		t.Errorf("CFB-10 control: cfb.Read: %v", err)
	}

	got, status := nativeStreams(t, writeFile(t, "control.cfb", raw), "Ábcd", "Ébcd")
	if status != 0 || got["Ábcd"] != "first" || got["Ébcd"] != "second" {
		t.Errorf("CFB-10 control: 0x%08X %q, want first and second", status, got)
	}
}

// TestNativeNameTerminatorCFB11: a stream whose stored name has its NUL
// terminator overwritten is not found by that name, so the reader rejects
// it rather than expose it (CFB-11, MS-CFB 2.6.1).
func TestNativeNameTerminatorCFB11(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("native storage tests")
	}

	streams := []cfb.Stream{{Name: "Abcd", Data: []byte("payload")}, {Name: "x", Data: []byte("x")}}

	raw, err := cfb.Write(streams, cfb.Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	got, status := nativeStreams(t, writeFile(t, "control.cfb", raw), "Abcd")
	if status != 0 || got["Abcd"] != "payload" {
		t.Fatalf("CFB-11 control: 0x%08X %q", status, got)
	}

	// The terminator sits right after the 4 name units, so only that unit
	// changes; the name length field still says 5 units.
	old := utf16le("Abcd\x00")
	if bytes.Count(raw, old) != 1 {
		t.Fatal("cannot find the directory entry")
	}

	bad := bytes.Replace(raw, old, utf16le("AbcdX"), 1)

	if _, err := cfb.Read(bad); err == nil || !strings.Contains(err.Error(), "terminator") {
		t.Errorf("CFB-11: cfb.Read = %v, want an error about the terminator", err)
	}

	got, status = nativeStreams(t, writeFile(t, "unterminated.cfb", bad), "Abcd", "AbcdX")
	t.Logf("CFB-11: StgOpenStorage 0x%08X, OpenStream %q", status, got)

	if status == 0 && !strings.HasPrefix(got["Abcd"], errStorageNotFound) {
		t.Errorf("CFB-11: Windows found the unterminated stream by its name: %q", got)
	}

	// A NUL inside the name, which the reader also rejects, is only logged.
	inner := bytes.Replace(raw, old, utf16le("Ab\x00d\x00"), 1)
	got, status = nativeStreams(t, writeFile(t, "inner.cfb", inner), "Abcd", "Ab")
	_, readErr := cfb.Read(inner)
	t.Logf("CFB-11 NUL inside: StgOpenStorage 0x%08X, OpenStream %q, cfb.Read %v", status, got, readErr)
}

// TestNativeHeaderCountsCFB12: StgOpenStorage refuses a file whose header
// has a MiniFAT start but a MiniFAT sector count of 0, and a v4 file whose
// directory sector count is 0; the reader refuses both too (CFB-12). Counts
// that are off by one in the other direction are only logged: the reader
// accepts them.
func TestNativeHeaderCountsCFB12(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("native storage tests")
	}

	for _, version := range []int{3, 4} {
		raw, err := cfb.Write([]cfb.Stream{{Name: "s", Data: []byte("small")}}, cfb.Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		for _, tc := range []struct {
			name   string
			off    int
			value  uint32
			refuse bool
		}{
			{"control", 0x40, binary.LittleEndian.Uint32(raw[0x40:]), false},
			{"MiniFAT count 0", 0x40, 0, true},
			{"MiniFAT count +1", 0x40, binary.LittleEndian.Uint32(raw[0x40:]) + 1, false},
			{"directory count 0", 0x28, 0, version == 4},
			{"directory count +1", 0x28, binary.LittleEndian.Uint32(raw[0x28:]) + 1, false},
		} {
			bad := bytes.Clone(raw)
			binary.LittleEndian.PutUint32(bad[tc.off:], tc.value)

			_, readErr := cfb.Read(bad)
			got, status := nativeStreams(t, writeFile(t, "counts.cfb", bad), "s")
			t.Logf("CFB-12 v%d %s: StgOpenStorage 0x%08X, OpenStream %q, cfb.Read %v", version, tc.name, status, got, readErr)

			switch {
			case tc.refuse && status == 0:
				t.Errorf("CFB-12 v%d %s: Windows opened the file", version, tc.name)
			case tc.refuse && readErr == nil:
				t.Errorf("CFB-12 v%d %s: cfb.Read accepted the file", version, tc.name)
			case tc.name == "control" && (status != 0 || readErr != nil):
				t.Errorf("CFB-12 v%d control: 0x%08X, %v", version, status, readErr)
			}
		}

		// Without small streams there is no MiniFAT. A count without a
		// start is rejected by the reader and asserted; FREESECT as the
		// start, which it accepts, is only logged.
		large, err := cfb.Write([]cfb.Stream{{Name: "s", Data: make([]byte, 5000)}}, cfb.Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		for name, patch := range map[string]func([]byte){
			"MiniFAT count without a start": func(b []byte) { binary.LittleEndian.PutUint32(b[0x40:], 1) },
			"FREESECT MiniFAT start":        func(b []byte) { binary.LittleEndian.PutUint32(b[0x3C:], 0xFFFFFFFF) },
		} {
			bad := bytes.Clone(large)
			patch(bad)

			_, readErr := cfb.Read(bad)
			got, status := nativeStreams(t, writeFile(t, "counts.cfb", bad), "s")
			t.Logf("CFB-12 v%d %s: StgOpenStorage 0x%08X, OpenStream %d bytes, cfb.Read %v", version, name, status, len(got["s"]), readErr)

			// The reader rejects a count without a start; Windows must too.
			if strings.HasPrefix(name, "MiniFAT count") && status == 0 {
				t.Errorf("CFB-12 v%d %s: Windows opened the file the reader rejects", version, name)
			}
		}
	}
}
