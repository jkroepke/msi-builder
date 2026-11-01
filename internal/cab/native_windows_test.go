//go:build windows && (amd64 || arm64)

package cab_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/cab"
)

// These tests check the cabinets this package writes against Windows itself:
// cabinet.dll's FDI (the extractor Windows Installer uses, loaded through
// syscall without cgo) and expand.exe extract them, and makecab.exe builds a
// cabinet that this package reads. They run only without -short.

var (
	cabinetDLL  = syscall.NewLazyDLL("cabinet.dll")  //nolint:gochecknoglobals // one DLL table for the file
	pFDICreate  = cabinetDLL.NewProc("FDICreate")    //nolint:gochecknoglobals // one DLL table for the file
	pFDICopy    = cabinetDLL.NewProc("FDICopy")      //nolint:gochecknoglobals // one DLL table for the file
	pFDIDestroy = cabinetDLL.NewProc("FDIDestroy")   //nolint:gochecknoglobals // one DLL table for the file
	pFDIIsCab   = cabinetDLL.NewProc("FDIIsCabinet") //nolint:gochecknoglobals // one DLL table for the file
	kernel32    = syscall.NewLazyDLL("kernel32.dll") //nolint:gochecknoglobals // one DLL table for the file
	pLocalAlloc = kernel32.NewProc("LocalAlloc")     //nolint:gochecknoglobals // one DLL table for the file
	pLocalFree  = kernel32.NewProc("LocalFree")      //nolint:gochecknoglobals // one DLL table for the file
)

// FDI notification types.
const (
	fdintCabinetInfo   = 0
	fdintPartialFile   = 1
	fdintCopyFile      = 2
	fdintCloseFileInfo = 3
	fdintNextCabinet   = 4
)

// fdiErrors names the FDIERROR codes.
var fdiErrors = []string{ //nolint:gochecknoglobals // a constant table
	"FDIERROR_NONE", "FDIERROR_CABINET_NOT_FOUND", "FDIERROR_NOT_A_CABINET", "FDIERROR_UNKNOWN_CABINET_VERSION",
	"FDIERROR_CORRUPT_CABINET", "FDIERROR_ALLOC_FAIL", "FDIERROR_BAD_COMPR_TYPE", "FDIERROR_MDI_FAIL",
	"FDIERROR_TARGET_FILE", "FDIERROR_RESERVE_MISMATCH", "FDIERROR_WRONG_CABINET", "FDIERROR_USER_ABORT",
}

// extracted is one file as FDI reported and wrote it.
type extracted struct {
	Name    string
	Data    []byte
	Date    uint16
	Time    uint16
	Attribs uint16
	Size    int32
}

// memFile is an in-memory file behind an FDI handle.
type memFile struct {
	data []byte
	pos  int
	out  *extracted
}

// fdiState is the single in-flight extraction. FDI calls back on the calling
// thread, so one package-level state is enough for sequential tests.
type fdiState struct {
	cabinet []byte
	handles map[uintptr]*memFile
	next    uintptr
	files   []extracted
	erf     *[3]int32 // erfOper, erfType, fError
	// read counts the bytes FDI read from the cabinet.
	read int64
}

var state *fdiState //nolint:gochecknoglobals // FDI callbacks have no user pointer

// lastRead is the read count of the last fdiExtract.
var lastRead int64 //nolint:gochecknoglobals // see state

// fdiNotification mirrors FDINOTIFICATION on 64-bit Windows.
type fdiNotification struct {
	Cb       int32
	_        int32
	Psz1     *byte
	Psz2     *byte
	Psz3     *byte
	Pv       uintptr
	Hf       uintptr
	Date     uint16
	Time     uint16
	Attribs  uint16
	SetID    uint16
	ICabinet uint16
	IFolder  uint16
	Fdie     int32
}

// fdiCabinetInfo mirrors FDICABINETINFO.
type fdiCabinetInfo struct {
	Size     int32
	Folders  uint16
	Files    uint16
	SetID    uint16
	ICabinet uint16
	Reserve  int32
	Prev     int32
	Next     int32
}

func cstring(ptr *byte) string {
	if ptr == nil {
		return ""
	}

	var n int
	for *(*byte)(unsafe.Add(unsafe.Pointer(ptr), n)) != 0 {
		n++
	}

	return string(unsafe.Slice(ptr, n))
}

// The callbacks. Win64 has one calling convention, so cdecl FDI callbacks are
// plain syscall.NewCallback functions.
// syscall.NewCallback must run once per function, so the callbacks are globals.
var (
	cbAlloc = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(n uintptr) uintptr {
			// ULONG: the upper half of the register is unspecified.
			p, _, _ := pLocalAlloc.Call(0x40 /* LMEM_ZEROINIT */, uintptr(uint32(n)))

			return p
		})
	cbFree = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(p uintptr) uintptr {
			_, _, _ = pLocalFree.Call(p)

			return 0
		})
	cbOpen = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(_, _, _ uintptr) uintptr {
			// Only the cabinet is ever opened by name; extracted files are created
			// in the COPY_FILE notification.
			return state.open(&memFile{data: state.cabinet})
		})
	cbRead = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(handle uintptr, buf *byte, n uintptr) uintptr {
			n = uintptr(uint32(n)) // UINT: the upper half of the register is unspecified

			file := state.handles[handle]
			if file == nil {
				return ^uintptr(0)
			}

			got := copy(unsafe.Slice(buf, n), file.data[file.pos:])
			file.pos += got

			if file.out == nil {
				state.read += int64(got)
			}

			return uintptr(got)
		})
	cbWrite = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(handle uintptr, buf *byte, n uintptr) uintptr {
			n = uintptr(uint32(n)) // UINT: the upper half of the register is unspecified

			file := state.handles[handle]
			if file == nil {
				return ^uintptr(0)
			}

			file.data = append(file.data, unsafe.Slice(buf, n)...)

			return n
		})
	cbClose = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(handle uintptr) uintptr {
			file := state.handles[handle]
			if file == nil {
				return ^uintptr(0)
			}

			if file.out != nil {
				file.out.Data = file.data
				state.files = append(state.files, *file.out)
			}

			delete(state.handles, handle)

			return 0
		})
	cbSeek = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(handle, dist, whence uintptr) uintptr {
			file := state.handles[handle]
			if file == nil {
				return ^uintptr(0)
			}

			delta := int(int32(uint32(dist))) // long is 32 bits on Windows
			pos := file.pos

			switch int32(uint32(whence)) { // int: the upper half of the register is unspecified
			case 0:
				pos = delta
			case 1:
				pos += delta
			case 2:
				pos = len(file.data) + delta
			default:
				return ^uintptr(0)
			}

			if pos < 0 || pos > len(file.data) {
				return ^uintptr(0)
			}

			file.pos = pos

			return uintptr(pos)
		})
	cbNotify = syscall.NewCallback( //nolint:gochecknoglobals // see above
		func(typ uintptr, note *fdiNotification) uintptr {
			switch uint32(typ) { // a 32-bit enum: the upper half of the register is unspecified
			case fdintCabinetInfo, fdintPartialFile:
				return 0
			case fdintCopyFile:
				out := &extracted{Name: cstring(note.Psz1), Size: note.Cb, Date: note.Date, Time: note.Time, Attribs: note.Attribs}

				return state.open(&memFile{out: out})
			case fdintCloseFileInfo:
				if file := state.handles[note.Hf]; file != nil && file.out != nil {
					file.out.Data = file.data
					state.files = append(state.files, *file.out)
					file.out = nil
				}

				delete(state.handles, note.Hf)

				return 1
			case fdintNextCabinet:
				return ^uintptr(0)
			default:
				return 0
			}
		})
)

func (s *fdiState) open(f *memFile) uintptr {
	h := s.next
	s.next++
	s.handles[h] = f

	return h
}

func (s *fdiState) errorText() string {
	oper := int(s.erf[0])
	name := fmt.Sprintf("FDIERROR %d", oper)

	if oper >= 0 && oper < len(fdiErrors) {
		name = fdiErrors[oper]
	}

	return fmt.Sprintf("%s (erfType %d)", name, s.erf[1])
}

// fdiExtract extracts every file of raw with cabinet.dll and returns them in
// cabinet order.
func fdiExtract(t *testing.T, raw []byte) ([]extracted, error) {
	t.Helper()

	state = &fdiState{cabinet: raw, handles: make(map[uintptr]*memFile), next: 1, erf: new([3]int32)}
	defer func() { lastRead = state.read; state = nil }()

	hfdi, _, _ := pFDICreate.Call(cbAlloc, cbFree, cbOpen, cbRead, cbWrite, cbClose, cbSeek,
		^uintptr(0) /* cpuUNKNOWN */, uintptr(unsafe.Pointer(state.erf)))
	if hfdi == 0 {
		return nil, fmt.Errorf("fdi: FDICreate failed: %s", state.errorText())
	}

	defer func() { _, _, _ = pFDIDestroy.Call(hfdi) }()

	// FDIIsCabinet checks the header the way FDICopy will.
	var info fdiCabinetInfo

	hf := state.open(&memFile{data: raw})

	isCab, _, _ := pFDIIsCab.Call(hfdi, hf, uintptr(unsafe.Pointer(&info)))
	delete(state.handles, hf)

	if uint32(isCab) == 0 { // BOOL
		return nil, fmt.Errorf("fdi: FDIIsCabinet rejected the cabinet: %s", state.errorText())
	}

	if int(info.Size) != len(raw) {
		return nil, fmt.Errorf("fdi: FDIIsCabinet reports cbCabinet %d, the cabinet has %d bytes", info.Size, len(raw))
	}

	name, _ := syscall.BytePtrFromString("test.cab")
	path, _ := syscall.BytePtrFromString("")

	copied, _, _ := pFDICopy.Call(hfdi, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(path)), 0, cbNotify, 0, 0)
	if uint32(copied) == 0 { // BOOL
		return nil, fmt.Errorf("fdi: FDICopy failed: %s", state.errorText())
	}

	if len(state.handles) != 0 {
		return nil, fmt.Errorf("%d handles left open", len(state.handles))
	}

	return state.files, nil
}

// errLongPath marks an expand.exe failure on a machine without long paths
// enabled, where the output path exceeds MAX_PATH.
var errLongPath = errors.New("output path exceeds MAX_PATH")

// expandExtract extracts raw with expand.exe into a temporary directory.
// longestName is the longest cabinet name, used to tell a MAX_PATH failure
// from a cabinet defect.
func expandExtract(t *testing.T, raw []byte, longestName int) (map[string][]byte, error) {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "in.cab")
	out := filepath.Join(dir, "out")

	if err := os.WriteFile(src, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), filepath.Join(os.Getenv("SystemRoot"), "System32", "expand.exe"), "-F:*", src, out) //nolint:gosec // fixed Windows tool
	if output, err := cmd.CombinedOutput(); err != nil {
		if entries, _ := os.ReadDir(out); len(entries) == 0 && len(out)+1+longestName >= 260 {
			return nil, fmt.Errorf("%w: expand.exe wrote nothing under the %d-byte path %s: %w\n%s", errLongPath, len(out), out, err, output)
		}

		return nil, fmt.Errorf("expand.exe: %w\n%s", err, output)
	}

	files := make(map[string][]byte)

	err := filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, _ := filepath.Rel(out, path)

		data, err := os.ReadFile(path) //nolint:gosec // a file expand.exe just wrote
		files[filepath.ToSlash(rel)] = data

		return err
	})

	if err == nil && len(files) == 0 && len(out)+1+longestName >= 260 {
		return nil, fmt.Errorf("%w: expand.exe exited 0 but wrote nothing under the %d-byte path %s", errLongPath, len(out), out)
	}

	return files, err
}

type nativeCase struct {
	name  string
	files []cab.File
	opts  cab.Options
}

func nativeCases() []nativeCase {
	when := time.Date(2024, 6, 15, 13, 45, 30, 0, time.UTC)
	rng := rand.New(rand.NewSource(11)) //nolint:gosec // reproducible test data
	random := make([]byte, 100000)
	rng.Read(random)

	// Compressible data with matches that reach back across block boundaries.
	text := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. 0123456789\r\n"), 2000)

	many := make([]cab.File, 0, 2000)
	for i := range 2000 {
		many = append(many, cab.File{Name: fmt.Sprintf("fil_%04d", i), Data: fmt.Appendf(nil, "file %d\r\n", i), Time: when})
	}

	cases := []nativeCase{
		{name: "stored-vector", opts: cab.Options{Compression: cab.None, SetID: 0x1234}, files: []cab.File{
			{Name: "fil_a", Data: []byte("Hello, cabinet!\r\n"), Time: when, Attributes: 0x20},
			{Name: "fil_b", Data: []byte("MSI\r\n"), Time: when, Attributes: 0x21},
		}},
		{name: "mszip-small", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "fil_a", Data: []byte("Hello, cabinet!\r\n"), Time: when},
			{Name: "fil_b", Data: []byte("MSI\r\n"), Time: when, Attributes: cab.AttrReadOnly | cab.AttrHidden | cab.AttrSystem},
		}},
		{name: "empty-file-only", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "empty", Time: when},
		}},
		{name: "empty-files-stored", opts: cab.Options{Compression: cab.None}, files: []cab.File{
			{Name: "empty1", Time: when}, {Name: "empty2", Time: when},
		}},
		{name: "empty-between-files", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "first", Data: text[:1000], Time: when},
			{Name: "empty", Time: when},
			{Name: "last", Data: text[1000:3000], Time: when},
		}},
		{name: "empty-at-end", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "first", Data: text[:40000], Time: when},
			{Name: "empty", Time: when},
		}},
		{name: "exactly-one-block", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "block", Data: text[:32768], Time: when},
		}},
		{name: "one-block-plus-one", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "block", Data: text[:32769], Time: when},
		}},
		{name: "file-starts-at-block-boundary", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "fil_a", Data: text[:32768], Time: when},
			{Name: "fil_b", Data: text[32768:65536], Time: when},
			{Name: "fil_c", Data: text[65536:65537], Time: when},
		}},
		{name: "file-crosses-blocks", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "fil_a", Data: text[:20000], Time: when},
			{Name: "fil_b", Data: text[20000:70000], Time: when},
			{Name: "fil_c", Data: text[70000:], Time: when},
		}},
		{name: "stored-multi-block", opts: cab.Options{Compression: cab.None}, files: []cab.File{
			{Name: "fil_a", Data: text[:70000], Time: when},
			{Name: "fil_b", Data: random[:3], Time: when},
		}},
		{name: "random-stored-fallback", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "random", Data: random, Time: when},
		}},
		{name: "mixed-random-and-text", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "r1", Data: random[:33000], Time: when},
			{Name: "t1", Data: text[:50000], Time: when},
			{Name: "r2", Data: random[33000:], Time: when},
		}},
		{name: "many-files", opts: cab.Options{Compression: cab.MSZIP}, files: many},
		{name: "dates", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "zero", Data: []byte("x")},
			{Name: "early", Data: []byte("x"), Time: time.Date(1979, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Name: "late", Data: []byte("x"), Time: time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Name: "odd", Data: []byte("x"), Time: time.Date(2026, 10, 25, 23, 59, 59, 0, time.UTC)},
		}},
		{name: "utf8-name", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: "grüße.txt", Data: []byte("x"), Time: when},
		}},
		{name: "long-name", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: strings.Repeat("n", 255), Data: []byte("x"), Time: when},
		}},
		{name: "directories", opts: cab.Options{Compression: cab.MSZIP}, files: []cab.File{
			{Name: `top.txt`, Data: []byte("top\r\n"), Time: when},
			{Name: `sub\deep\nested.txt`, Data: text[:5000], Time: when},
			{Name: `sub\other.txt`, Data: []byte("other\r\n"), Time: when},
		}},
	}

	cases = append(cases, lzxCases(when, random, text)...)

	// Checksum tails: compressed sizes of 1, 2 and 3 bytes modulo 4.
	for tail := range 4 {
		cases = append(cases, nativeCase{
			name: fmt.Sprintf("checksum-tail-%d", tail),
			opts: cab.Options{Compression: cab.None},
			files: []cab.File{
				{Name: "fil_a", Data: random[:32768], Time: when},
				{Name: "fil_b", Data: random[32768 : 32768+100+tail], Time: when},
			},
		})
	}

	return cases
}

// alphabetShift is incompressible data whose first frame uses other byte
// values than the rest of its LZX block: compressed with the block's trees,
// that frame would exceed the CFDATA limit, so the encoder stores the block.
func alphabetShift() []byte {
	rng := rand.New(rand.NewSource(2)) //nolint:gosec // reproducible test data
	out := make([]byte, 4*32768)

	for i := range out {
		if i < 32768 {
			out[i] = byte(101 + rng.Intn(155)) //nolint:gosec // below 256
		} else {
			out[i] = byte(rng.Intn(101)) //nolint:gosec // below 101
		}
	}

	return out
}

// lzxCases cover the LZX encoder: every window size, all three block types,
// frame and block boundaries, E8 call translation near frame ends, matches
// that reach back as far as the encoder searches, and data that makes its
// history buffer slide.
func lzxCases(when time.Time, random, text []byte) []nativeCase {
	lzx := func(window int) cab.Options { return cab.Options{Compression: cab.LZX, Window: window} }

	// x86-like data: E8 calls with near and far targets, repeated records,
	// text and noise.
	rng := rand.New(rand.NewSource(21)) //nolint:gosec // reproducible test data
	code := make([]byte, 0, 3<<20)

	for len(code) < 3<<20 {
		switch rng.Intn(4) {
		case 0:
			target := uint32(rng.Intn(1<<22) - 1<<21) //nolint:gosec // two's complement call target
			code = binary.LittleEndian.AppendUint32(append(code, 0xE8), target)
		case 1:
			code = append(code, text[:rng.Intn(200)]...)
		case 2:
			for range 4 {
				code = binary.LittleEndian.AppendUint32(code, uint32(rng.Intn(16)*16)) //nolint:gosec // below 256
			}
		default:
			code = append(code, random[:rng.Intn(64)]...)
		}
	}

	// A random 64 KB chunk repeated almost the encoder's 512 KB search span
	// later.
	far := make([]byte, 0, 1<<19+1<<16)
	far = append(far, random[:65536]...)

	for len(far) < 1<<19-100 {
		far = append(far, byte(rng.Intn(256))) //nolint:gosec // below 256
	}

	far = append(far, random[:65536]...)

	// E8 bytes in the last 10 bytes of frames and in short final frames.
	calls := bytes.Repeat([]byte{0xE8, 0x00, 0x10, 0x00, 0x00}, 32768/5*3)
	e8tail := append(bytes.Clone(calls[:32768+11]), 0xE8, 1, 2, 3, 4, 0xE8)

	// Under the race detector the large cases shrink to a few blocks; CI
	// runs them at full size in a step without -race.
	limit := len(code)
	if raceEnabled {
		limit = 5 * 32768
	}

	cases := make([]nativeCase, 0, 32)

	for window := 15; window <= 21; window++ {
		cases = append(cases, nativeCase{name: fmt.Sprintf("lzx-%d-code", window), opts: lzx(window), files: []cab.File{
			{Name: "code.bin", Data: code[:min(limit, 3<<window)], Time: when},
			{Name: "text.txt", Data: text, Time: when},
		}})
	}

	for _, block := range []int{1, 2, 3} {
		cases = append(cases, nativeCase{name: fmt.Sprintf("lzx-block-type-%d", block), opts: cab.WithLZXBlock(lzx(21), block), files: []cab.File{
			{Name: "code.bin", Data: code[:min(limit, 1<<20)], Time: when},
		}})
	}

	cases = append(cases,
		nativeCase{name: "lzx-small", opts: lzx(0), files: []cab.File{
			{Name: "fil_a", Data: []byte("Hello, cabinet!\r\n"), Time: when},
			{Name: "fil_b", Data: []byte("M"), Time: when},
		}},
		nativeCase{name: "lzx-empty-file-only", opts: lzx(0), files: []cab.File{{Name: "empty", Time: when}}},
		nativeCase{name: "lzx-exactly-one-block", opts: lzx(0), files: []cab.File{{Name: "block", Data: text[:32768], Time: when}}},
		nativeCase{name: "lzx-one-block-plus-one", opts: lzx(0), files: []cab.File{{Name: "block", Data: text[:32769], Time: when}}},
		nativeCase{name: "lzx-random", opts: lzx(0), files: []cab.File{{Name: "random", Data: random, Time: when}}},
		nativeCase{name: "lzx-stored-fallback", opts: lzx(0), files: []cab.File{{Name: "shift", Data: alphabetShift(), Time: when}}},
		nativeCase{name: "lzx-e8-frame-ends", opts: lzx(0), files: []cab.File{{Name: "e8", Data: calls, Time: when}}},
		nativeCase{name: "lzx-e8-short-tail", opts: lzx(0), files: []cab.File{{Name: "e8", Data: e8tail, Time: when}}},
		nativeCase{name: "lzx-e8-tiny-tail", opts: lzx(0), files: []cab.File{{Name: "e8", Data: calls[:32768+10], Time: when}}},
	)

	if raceEnabled {
		return cases
	}

	return append(cases,
		nativeCase{name: "lzx-far-match", opts: lzx(21), files: []cab.File{{Name: "far", Data: far, Time: when}}},
		nativeCase{name: "lzx-history-slides", opts: lzx(0), files: []cab.File{
			{Name: "code1.bin", Data: code, Time: when},
			{Name: "code2.bin", Data: code, Time: when},
			{Name: "code3.bin", Data: code, Time: when},
			{Name: "far.bin", Data: far, Time: when},
		}},
	)
}

func TestNativeFDIExtracts(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	for _, tc := range nativeCases() { //nolint:paralleltest // shared FDI state
		t.Run(tc.name, func(t *testing.T) {
			raw, err := cab.Write(tc.files, tc.opts)
			if err != nil {
				t.Fatal(err)
			}

			got, err := fdiExtract(t, raw)
			if err != nil {
				t.Fatalf("cabinet.dll: %v", err)
			}

			if len(got) != len(tc.files) {
				t.Fatalf("FDI extracted %d files, want %d", len(got), len(tc.files))
			}

			for i, want := range tc.files {
				got := got[i]

				if got.Name != want.Name {
					t.Errorf("file %d: FDI reports name %q, want %q", i, got.Name, want.Name)
				}

				if !bytes.Equal(got.Data, want.Data) {
					t.Errorf("file %d (%s): FDI wrote %d bytes, want %d; data differs at %d", i, want.Name, len(got.Data), len(want.Data), firstDiff(got.Data, want.Data))
				}

				if int(got.Size) != len(want.Data) {
					t.Errorf("file %d (%s): FDI reports size %d, want %d", i, want.Name, got.Size, len(want.Data))
				}

				date, tm := cab.DOSTime(want.Time)
				if got.Date != date || got.Time != tm {
					t.Errorf("file %d (%s): FDI reports date %#x time %#x, want %#x %#x", i, want.Name, got.Date, got.Time, date, tm)
				}

				wantAttr := want.Attributes
				if wantAttr == 0 {
					wantAttr = cab.AttrArchive
				}

				for _, c := range []byte(want.Name) {
					if c >= 0x80 {
						wantAttr |= 0x80
					}
				}

				if got.Attribs != wantAttr {
					t.Errorf("file %d (%s): FDI reports attributes %#x, want %#x", i, want.Name, got.Attribs, wantAttr)
				}
			}

			checkFSAgainstFDI(t, raw, got)
		})
	}
}

// checkFSAgainstFDI opens raw through cab.Open and checks that the fs.FS view
// serves every file FDI extracted, under the slash-separated spelling of the
// backslash name FDI reported, with the same bytes, size and time.
func checkFSAgainstFDI(t *testing.T, raw []byte, extractedFiles []extracted) {
	t.Helper()

	fsys, err := cab.Open(raw)
	if err != nil {
		t.Fatalf("cab.Open: %v", err)
	}

	if err := fstest.TestFS(fsys, slashNames(extractedFiles)...); err != nil {
		t.Errorf("fstest.TestFS: %v", err)
	}

	for _, file := range extractedFiles {
		name := strings.ReplaceAll(file.Name, `\`, "/")

		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Errorf("fs.ReadFile(%q): %v", name, err)

			continue
		}

		if !bytes.Equal(data, file.Data) {
			t.Errorf("%s: cab.Open serves %d bytes, FDI wrote %d; data differs at %d", name, len(data), len(file.Data), firstDiff(data, file.Data))
		}

		info, err := fs.Stat(fsys, name)
		if err != nil {
			t.Errorf("fs.Stat(%q): %v", name, err)

			continue
		}

		if info.Size() != int64(len(file.Data)) {
			t.Errorf("%s: Stat size %d, FDI wrote %d", name, info.Size(), len(file.Data))
		}

		if date, tm := cab.DOSTime(info.ModTime()); date != file.Date || tm != file.Time {
			t.Errorf("%s: Stat time %v gives %#x %#x, FDI reported %#x %#x", name, info.ModTime(), date, tm, file.Date, file.Time)
		}
	}
}

func slashNames(files []extracted) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, strings.ReplaceAll(file.Name, `\`, "/"))
	}

	return names
}

func TestNativeExpandExtracts(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	for _, tc := range nativeCases() { //nolint:paralleltest // shared FDI state
		t.Run(tc.name, func(t *testing.T) {
			raw, err := cab.Write(tc.files, tc.opts)
			if err != nil {
				t.Fatal(err)
			}

			longest := 0
			for _, f := range tc.files {
				longest = max(longest, len(f.Name))
			}

			got, err := expandExtract(t, raw, longest)
			if errors.Is(err, errLongPath) {
				// A machine without long paths enabled cannot write a 255-byte name
				// under the temporary directory; the FDI test covers the same bytes.
				t.Skip(err)
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(got) != len(tc.files) {
				t.Errorf("expand.exe extracted %d files, want %d", len(got), len(tc.files))
			}

			for _, want := range tc.files {
				data, ok := got[strings.ReplaceAll(want.Name, `\`, "/")]
				if !ok && len(tc.files) == 1 {
					// Observed on Windows Server 2025: expand.exe -F:* writes the only
					// file of a single-file cabinet under the cabinet's own name
					// ("in.cab"), not the entry's. Only the data is compared then;
					// the FDI test checks the name.
					data, ok = got["in.cab"]
				}

				if !ok {
					t.Errorf("expand.exe did not extract %q (got %v)", want.Name, keys(got))

					continue
				}

				if !bytes.Equal(data, want.Data) {
					t.Errorf("%s: expand.exe wrote %d bytes, want %d; data differs at %d", want.Name, len(data), len(want.Data), firstDiff(data, want.Data))
				}
			}
		})
	}
}

// TestNativeReadMakecab reads a cabinet built by makecab.exe, Microsoft's own
// compressor, through cab.Read: cross-block MSZIP history, checksums and the
// DOS times are then checked against a native writer.
func TestNativeReadMakecab(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	rng := rand.New(rand.NewSource(5)) //nolint:gosec // reproducible test data
	random := make([]byte, 40000)
	rng.Read(random)

	text := bytes.Repeat([]byte("MSZIP history crosses CFDATA blocks when makecab compresses it.\r\n"), 1500)

	for _, comp := range []string{"MSZIP", "stored", "LZX:15", "LZX:21"} { //nolint:paralleltest // shared FDI state
		t.Run(comp, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src") // only the inputs, so cab.Files sees nothing else
			inputs := map[string][]byte{
				"text.txt": text, "random.bin": random, "small.txt": []byte("small\r\n"), "empty.txt": nil,
				`sub\deep\nested.txt`: []byte("nested\r\n"),
			}

			var ddf strings.Builder

			// makecab reads the directive file as ANSI, so it holds only relative
			// paths; cmd.Dir makes them resolve under the (possibly non-ASCII) dir.
			ddf.WriteString(".OPTION EXPLICIT\r\n.Set CabinetNameTemplate=out.cab\r\n.Set DiskDirectoryTemplate=.\r\n")

			if comp == "stored" {
				// makecab knows no CompressionType "NONE"; Compress=off writes typeCompress 0.
				ddf.WriteString(".Set Cabinet=on\r\n.Set Compress=off\r\n")
			} else if kind, memory, ok := strings.Cut(comp, ":"); ok {
				ddf.WriteString(".Set Cabinet=on\r\n.Set Compress=on\r\n.Set CompressionType=" + kind + "\r\n.Set CompressionMemory=" + memory + "\r\n")
			} else {
				ddf.WriteString(".Set Cabinet=on\r\n.Set Compress=on\r\n.Set CompressionType=" + comp + "\r\n")
			}

			ddf.WriteString(".Set MaxDiskSize=CDROM\r\n.Set RptFileName=nul\r\n.Set InfFileName=nul\r\n.Set UniqueFiles=off\r\n")

			for _, name := range []string{"text.txt", "empty.txt", "random.bin", `sub\deep\nested.txt`, "small.txt"} {
				path := filepath.Join(src, filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, inputs[name], 0o600); err != nil {
					t.Fatal(err)
				}

				ddf.WriteString(`"src\` + name + `" ` + name + "\r\n")
			}

			ddfPath := filepath.Join(dir, "cab.ddf")
			if err := os.WriteFile(ddfPath, []byte(ddf.String()), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), filepath.Join(os.Getenv("SystemRoot"), "System32", "makecab.exe"), "/F", "cab.ddf") //nolint:gosec // fixed Windows tool
			cmd.Dir = dir

			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("makecab.exe: %v\n%s", err, output)
			}

			raw, err := os.ReadFile(filepath.Join(dir, "out.cab"))
			if err != nil {
				t.Fatal(err)
			}

			t.Logf("makecab %s: %d bytes, folders %d, files %d, blocks %d, typeCompress %#x", comp, len(raw),
				int(raw[0x1A])|int(raw[0x1B])<<8, int(raw[0x1C])|int(raw[0x1D])<<8, int(raw[36+4])|int(raw[36+5])<<8, int(raw[36+6])|int(raw[36+7])<<8)

			wantType := uint16(cab.MSZIP)
			if comp == "stored" {
				wantType = uint16(cab.None)
			} else if _, memory, ok := strings.Cut(comp, ":"); ok {
				window, err := strconv.Atoi(memory)
				if err != nil {
					t.Fatal(err)
				}

				wantType = uint16(cab.LZX) | uint16(window)<<8 //nolint:gosec // window is 15 or 21
			}

			if typ := uint16(raw[36+6]) | uint16(raw[36+7])<<8; typ != wantType {
				t.Fatalf("makecab wrote typeCompress %#x, want %#x", typ, wantType)
			}

			files, err := cab.Read(raw)
			if err != nil {
				t.Fatalf("cab.Read of a makecab cabinet: %v", err)
			}

			if len(files) != len(inputs) {
				t.Fatalf("read %d files, want %d", len(files), len(inputs))
			}

			seen := make(map[string]bool, len(inputs))

			for _, f := range files {
				want, ok := inputs[f.Name]
				if !ok || seen[f.Name] {
					t.Errorf("unexpected or repeated file %q", f.Name)

					continue
				}

				seen[f.Name] = true

				if !bytes.Equal(f.Data, want) {
					t.Errorf("%s: read %d bytes, want %d; data differs at %d", f.Name, len(f.Data), len(want), firstDiff(f.Data, want))
				}

				// makecab records the local time and rounds an odd second up where
				// DOSTime truncates, so allow one 2-second step either way.
				st, _ := os.Stat(filepath.Join(src, filepath.FromSlash(strings.ReplaceAll(f.Name, `\`, "/"))))
				local := st.ModTime().Local()
				stamp := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, time.UTC)

				if delta := f.Time.Sub(stamp); delta < -2*time.Second || delta > 2*time.Second {
					t.Errorf("%s: read time %v, makecab should have written the local time %v", f.Name, f.Time, stamp)
				}
			}

			// The fs.FS view of the makecab cabinet must match the source tree.
			fsys, err := cab.Open(raw)
			if err != nil {
				t.Fatalf("cab.Open of a makecab cabinet: %v", err)
			}

			if err := fstest.TestFS(fsys, "text.txt", "sub/deep/nested.txt"); err != nil {
				t.Errorf("fstest.TestFS: %v", err)
			}

			for name, want := range inputs {
				slash := strings.ReplaceAll(name, `\`, "/")

				data, err := fs.ReadFile(fsys, slash)
				if err != nil {
					t.Errorf("fs.ReadFile(%q): %v", slash, err)

					continue
				}

				if !bytes.Equal(data, want) {
					t.Errorf("%s: cab.Open serves %d bytes, want %d", slash, len(data), len(want))
				}
			}

			// cab.Files of the source tree, written by this package, must extract
			// through FDI under the same backslash names makecab used.
			ours, err := cab.Files(os.DirFS(src))
			if err != nil {
				t.Fatal(err)
			}

			raw, err = cab.Write(ours, cab.Options{Compression: cab.MSZIP})
			if err != nil {
				t.Fatal(err)
			}

			got, err := fdiExtract(t, raw)
			if err != nil {
				t.Fatalf("cabinet.dll on cab.Files output: %v", err)
			}

			if len(got) != len(inputs) {
				t.Fatalf("FDI extracted %d files from the cab.Files cabinet, want %d", len(got), len(inputs))
			}

			clear(seen)

			for _, file := range got {
				want, ok := inputs[file.Name]
				if !ok || seen[file.Name] {
					t.Errorf("FDI reports %q, not a makecab name or a repeat", file.Name)

					continue
				}

				seen[file.Name] = true

				if !bytes.Equal(file.Data, want) {
					t.Errorf("%s: FDI wrote %d bytes, want %d", file.Name, len(file.Data), len(want))
				}
			}
		})
	}
}

// TestNativeFDIRejectsCorruption checks that the checksum we write is the one
// cabinet.dll verifies: a flipped payload byte must be refused.
func TestNativeFDIRejectsCorruption(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	text := bytes.Repeat([]byte("checksums are verified by FDI\r\n"), 3000)

	// A stored folder has no decoder that could fail first, so only the
	// checksum can make FDI refuse the flipped byte.
	for _, comp := range []cab.Compression{cab.None, cab.MSZIP} {
		raw, err := cab.Write([]cab.File{{Name: "fil_a", Data: text}}, cab.Options{Compression: comp})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := fdiExtract(t, raw); err != nil {
			t.Fatalf("intact cabinet (compression %d): %v", comp, err)
		}

		bad := bytes.Clone(raw)
		bad[len(bad)-1] ^= 0x40

		_, err = fdiExtract(t, bad)
		if err == nil {
			t.Fatalf("FDI extracted a cabinet (compression %d) with a flipped payload byte", comp)
		}

		t.Logf("compression %d, corrupt last block: %v", comp, err)

		if comp == cab.None && !strings.Contains(err.Error(), "FDIERROR_CORRUPT_CABINET") {
			t.Errorf("stored cabinet with a flipped byte: want FDIERROR_CORRUPT_CABINET from the checksum, got %v", err)
		}
	}
}

func firstDiff(a, b []byte) int {
	limit := min(len(a), len(b))
	for i := range limit {
		if a[i] != b[i] {
			return i
		}
	}

	return limit
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// TestNativeLZXSystemBinaries compresses real x86-64 code, Windows' own
// binaries, both ways: cabinet.dll must extract msi-builder's LZX cabinet, and
// cab.Read must extract makecab's. The log compares the sizes.
func TestNativeLZXSystemBinaries(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	if raceEnabled {
		t.Skip("compresses about 20 MB; CI runs it without -race")
	}

	system := filepath.Join(os.Getenv("SystemRoot"), "System32")
	dir := t.TempDir()

	var (
		files []cab.File
		ddf   strings.Builder
		total int
	)

	ddf.WriteString(".OPTION EXPLICIT\r\n.Set CabinetNameTemplate=out.cab\r\n.Set DiskDirectoryTemplate=" + dir + "\r\n")
	ddf.WriteString(".Set Cabinet=on\r\n.Set Compress=on\r\n.Set CompressionType=LZX\r\n.Set CompressionMemory=21\r\n")
	ddf.WriteString(".Set MaxDiskSize=0\r\n.Set FolderSizeThreshold=0\r\n.Set RptFileName=nul\r\n.Set InfFileName=nul\r\n")

	for _, name := range []string{"ntoskrnl.exe", "shell32.dll", "kernel32.dll", "msi.dll", "cabinet.dll"} {
		data, err := os.ReadFile(filepath.Join(system, name))
		if err != nil {
			t.Logf("skipping %s: %v", name, err)

			continue
		}

		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		files = append(files, cab.File{Name: name, Data: data})
		total += len(data)

		ddf.WriteString(`"` + path + `" ` + name + "\r\n")
	}

	if len(files) == 0 {
		t.Skip("no system binaries found")
	}

	ours, err := cab.Write(files, cab.Options{Compression: cab.LZX})
	if err != nil {
		t.Fatal(err)
	}

	got, err := fdiExtract(t, ours)
	if err != nil {
		t.Fatalf("cabinet.dll: %v", err)
	}

	for i, want := range files {
		if i >= len(got) || got[i].Name != want.Name || !bytes.Equal(got[i].Data, want.Data) {
			t.Errorf("%s: FDI output differs", want.Name)
		}
	}

	ddfPath := filepath.Join(dir, "cab.ddf")
	if err := os.WriteFile(ddfPath, []byte(ddf.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), filepath.Join(system, "makecab.exe"), "/F", ddfPath) //nolint:gosec // fixed Windows tool
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makecab.exe: %v\n%s", err, output)
	}

	theirs, err := os.ReadFile(filepath.Join(dir, "out.cab"))
	if err != nil {
		t.Fatal(err)
	}

	back, err := cab.Read(theirs)
	if err != nil {
		t.Fatalf("cab.Read of makecab's LZX cabinet: %v", err)
	}

	for i, want := range files {
		if i >= len(back) || back[i].Name != want.Name || !bytes.Equal(back[i].Data, want.Data) {
			t.Errorf("%s: cab.Read of makecab's cabinet differs", want.Name)
		}
	}

	t.Logf("%d files, %d bytes: msi-builder LZX:21 %d bytes, makecab LZX:21 %d bytes", len(files), total, len(ours), len(theirs))
}

// TestNativeReadMakecabFewCodes reads makecab LZX cabinets of inputs whose
// blocks have the fewest distinct symbols: a run of zeros (one literal, one
// match length) and bytes from 64 values with no match of 9 bytes or more
// (an all-zero length tree, written as runs of one pretree code). The reader
// rejects a nonempty Huffman tree of one code (MS-PATCH 2.4), so these
// cabinets show whether makecab pads such trees to two codes.
func TestNativeReadMakecabFewCodes(t *testing.T) { //nolint:paralleltest // makecab runs one at a time
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	rng := rand.New(rand.NewSource(7)) //nolint:gosec // reproducible test data
	short := make([]byte, 100000)

	for i := range short {
		short[i] = byte(32 + rng.Intn(64)) //nolint:gosec // below 96
	}

	for name, data := range map[string][]byte{"zeros.bin": make([]byte, 200000), "short.bin": short} { //nolint:paralleltest // makecab runs one at a time
		for _, memory := range []string{"15", "21"} {
			t.Run(name+"/LZX:"+memory, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}

				ddf := ".OPTION EXPLICIT\r\n.Set CabinetNameTemplate=out.cab\r\n.Set DiskDirectoryTemplate=.\r\n" +
					".Set Cabinet=on\r\n.Set Compress=on\r\n.Set CompressionType=LZX\r\n.Set CompressionMemory=" + memory + "\r\n" +
					".Set MaxDiskSize=0\r\n.Set RptFileName=nul\r\n.Set InfFileName=nul\r\n" + name + " " + name + "\r\n"
				if err := os.WriteFile(filepath.Join(dir, "cab.ddf"), []byte(ddf), 0o600); err != nil {
					t.Fatal(err)
				}

				cmd := exec.CommandContext(t.Context(), filepath.Join(os.Getenv("SystemRoot"), "System32", "makecab.exe"), "/F", "cab.ddf") //nolint:gosec // fixed Windows tool
				cmd.Dir = dir

				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("makecab.exe: %v\n%s", err, output)
				}

				raw, err := os.ReadFile(filepath.Join(dir, "out.cab"))
				if err != nil {
					t.Fatal(err)
				}

				files, err := cab.Read(raw)
				if err != nil {
					t.Fatalf("cab.Read of makecab's cabinet: %v", err)
				}

				if len(files) != 1 || !bytes.Equal(files[0].Data, data) {
					t.Fatal("cab.Read of makecab's cabinet differs")
				}

				t.Logf("%s: %d bytes in a %d-byte cabinet", name, len(data), len(raw))
			})
		}
	}
}

// TestNativeFDIMaxFiles extracts a cabinet with cab.MaxFiles entries, the
// most cFiles holds and the count at which the builder starts a new cabinet,
// through cabinet.dll (CAB-09).
func TestNativeFDIMaxFiles(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	if raceEnabled {
		t.Skip("65535 FDI callbacks; CI runs it without -race")
	}

	when := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)

	files := make([]cab.File, cab.MaxFiles)
	for i := range files {
		files[i] = cab.File{Name: "f" + strconv.Itoa(i), Data: []byte{byte(i)}, Time: when}
	}

	raw, err := cab.Write(files, cab.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := fdiExtract(t, raw)
	if err != nil {
		t.Fatalf("cabinet.dll: %v", err)
	}

	if len(got) != len(files) {
		t.Fatalf("FDI extracted %d files, want %d", len(got), len(files))
	}

	for i, file := range got {
		if file.Name != files[i].Name || !bytes.Equal(file.Data, files[i].Data) {
			t.Fatalf("file %d: FDI reports %q with %v, want %q with %v", i, file.Name, file.Data, files[i].Name, files[i].Data)
		}
	}

	t.Logf("CAB-09: cabinet.dll extracted all %d entries of a %d-byte cabinet", len(got), len(raw))

	if _, err := cab.Write(append(slices.Clone(files), cab.File{Name: "one-more", Time: when}), cab.Options{}); err == nil {
		t.Error("cab.Write accepted 65536 files")
	}
}

// TestNativeFDIExtractsCopies checks that cabinet.dll extracts cabinets
// whose copies point back at a shared entry's folder (Entry.Copy, CAB-14),
// with every copy's own name, date and attributes, and that going back for
// a copy reads only the shared folder again.
func TestNativeFDIExtractsCopies(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	for _, opts := range []cab.Options{{Compression: cab.None}, {Compression: cab.MSZIP}, {Compression: cab.LZX}, {Compression: cab.LZX, Window: 15}} {
		files, ents := copyFiles()

		// An empty file after the last copy is an empty last folder.
		files = append(files, cab.File{Name: "empty", Time: files[0].Time})
		ents = append(ents, entries(files[len(files)-1:])...)
		raw := writeEntries(t, ents, opts)

		got, err := fdiExtract(t, raw)
		if err != nil {
			t.Fatalf("compression %d window %d: cabinet.dll: %v", opts.Compression, opts.Window, err)
		}

		if len(got) != len(files) {
			t.Fatalf("compression %d: FDI extracted %d files, want %d", opts.Compression, len(got), len(files))
		}

		for i, want := range files {
			attr := want.Attributes
			if attr == 0 {
				attr = cab.AttrArchive
			}

			date, clock := cab.DOSTime(want.Time)
			if got[i].Name != want.Name || !bytes.Equal(got[i].Data, want.Data) || got[i].Date != date || got[i].Time != clock || got[i].Attribs != attr {
				t.Errorf("compression %d: file %d is %s (%d bytes, %#x %#x, %#x), want %s (%d bytes, %#x %#x, %#x)", opts.Compression, i,
					got[i].Name, len(got[i].Data), got[i].Date, got[i].Time, got[i].Attribs, want.Name, len(want.Data), date, clock, attr)
			}
		}

		// The shared folder is the second (its CFFOLDER at 36 + 8); the
		// third starts where it ends. Two copies read it again.
		folder := 36 + 8
		shared := int64(binary.LittleEndian.Uint32(raw[folder+8:]) - binary.LittleEndian.Uint32(raw[folder:]))

		t.Logf("CAB-14 compression %d window %d: cabinet of %d bytes, shared folder %d bytes, cabinet.dll read %d bytes",
			opts.Compression, opts.Window, len(raw), shared, lastRead)

		if lastRead > int64(len(raw))+2*shared+64<<10 {
			t.Errorf("compression %d: cabinet.dll read %d bytes of a %d-byte cabinet whose shared folder has %d", opts.Compression, lastRead, len(raw), shared)
		}
	}
}
