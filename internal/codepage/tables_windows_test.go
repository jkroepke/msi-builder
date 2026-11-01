package codepage //nolint:testpackage // the tests compare the unexported tables with Windows

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"
)

const (
	mbErrInvalidChars = 0x8
	wcNoBestFitChars  = 0x400
)

//nolint:gochecknoglobals // the kernel32 functions the tables come from
var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
	procIsDBCSLeadByteEx    = kernel32.NewProc("IsDBCSLeadByteEx")
)

// nativeDecode converts data with MB_ERR_INVALID_CHARS; ok is false when
// Windows refuses it.
func nativeDecode(codePage int, data []byte) (string, bool) {
	var buf [8]uint16

	count, _, _ := procMultiByteToWideChar.Call(uintptr(codePage), mbErrInvalidChars,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if count == 0 {
		return "", false
	}

	return string(utf16.Decode(buf[:count])), true
}

// nativeEncode converts one code unit with WC_NO_BEST_FIT_CHARS; ok is false
// when Windows has to use the default character.
func nativeEncode(codePage int, unit uint16) ([]byte, bool) {
	var (
		buf         [8]byte
		usedDefault int32
	)

	count, _, _ := procWideCharToMultiByte.Call(uintptr(codePage), wcNoBestFitChars,
		uintptr(unsafe.Pointer(&unit)), 1,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		0, uintptr(unsafe.Pointer(&usedDefault)))
	if count == 0 || usedDefault != 0 {
		return nil, false
	}

	return buf[:count], true
}

func isLeadByte(codePage int, b byte) bool {
	r, _, _ := procIsDBCSLeadByteEx.Call(uintptr(codePage), uintptr(b))

	return r != 0
}

// unit returns the single code unit of s, or unmapped.
func unit(s string, ok bool) uint16 {
	units := utf16.Encode([]rune(s))
	if !ok || len(units) != 1 || units[0] >= leadByte {
		return unmapped
	}

	return units[0]
}

// dump reads a code page from Windows into a table.
func dump(t *testing.T, codePage int) *table {
	t.Helper()

	tab := &table{}

	for first := range 256 {
		if isLeadByte(codePage, byte(first)) {
			tab.single[first] = leadByte
			tab.double[first] = new([256]uint16)

			for trail := range 256 {
				tab.double[first][trail] = unit(nativeDecode(codePage, []byte{byte(first), byte(trail)}))
			}

			continue
		}

		tab.single[first] = unit(nativeDecode(codePage, []byte{byte(first)}))
	}

	tab.encode = make([]uint16, 0x10000)
	tab.encode[0xFFFF] = unmapped

	for code := range uint16(0xFFFF) {
		tab.encode[code] = unmapped

		if code >= 0xD800 && code <= 0xDFFF {
			continue
		}

		seq, ok := nativeEncode(codePage, code)

		switch {
		case !ok:
			continue
		case len(seq) == 1:
			tab.encode[code] = uint16(seq[0])
		case len(seq) == 2 && tab.single[seq[0]] == leadByte:
			tab.encode[code] = uint16(seq[0])<<8 | uint16(seq[1])
		default:
			t.Fatalf("code page %d: U+%04X encodes to % x", codePage, code, seq)
		}

		// Even with WC_NO_BEST_FIT_CHARS, Windows writes some private-use
		// characters as bytes that MB_ERR_INVALID_CHARS refuses (CP874
		// U+F8C1 as DB). Decode refuses those bytes, so Encode must not
		// write them: only round trips count.
		if back, ok := nativeDecode(codePage, seq); !ok || back != string(rune(code)) {
			tab.encode[code] = unmapped
		}
	}

	return tab
}

// marshal writes the layout that parseTable reads.
func (t *table) marshal() []byte {
	values := append([]uint16(nil), t.single[:]...)

	for lead := range 256 {
		if t.double[lead] != nil {
			values = append(values, t.double[lead][:]...)
		}
	}

	inverse := t.inverse()

	var overrides []uint16

	for code := range uint16(0xFFFF) {
		if inverse[code] != t.encode[code] {
			overrides = append(overrides, code, t.encode[code])
		}
	}

	count := uint32(len(overrides) / 2) //nolint:gosec // at most 65535 overrides
	values = append(values, uint16(count&0xFFFF), uint16(count>>16))
	values = append(values, overrides...)

	raw := make([]byte, 2*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint16(raw[2*i:], v)
	}

	return raw
}

func compress(t *testing.T, raw []byte) []byte {
	t.Helper()

	var out bytes.Buffer

	writer, err := flate.NewWriter(&out, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return out.Bytes()
}

// TestWindowsTables compares the embedded tables with the code page
// functions of this Windows. MSI_BUILDER_UPDATE_TABLES=1 writes them
// instead.
func TestWindowsTables(t *testing.T) {
	t.Parallel()

	update := os.Getenv("MSI_BUILDER_UPDATE_TABLES") != ""

	for _, codePage := range ansi {
		t.Run(strconv.Itoa(codePage), func(t *testing.T) {
			t.Parallel()

			native := dump(t, codePage)

			if update {
				path := filepath.Join("tables", fmt.Sprintf("%d.bin", codePage))
				if err := os.WriteFile(path, compress(t, native.marshal()), 0o600); err != nil {
					t.Fatal(err)
				}

				return
			}

			embedded, err := lookup(codePage)
			if err != nil {
				t.Fatal(err)
			}

			compareTables(t, codePage, native, embedded)
		})
	}
}

func compareTables(t *testing.T, codePage int, native, embedded *table) {
	t.Helper()

	errs := 0
	report := func(format string, args ...any) {
		if errs++; errs <= 20 {
			t.Errorf(format, args...)
		} else {
			t.Fail()
		}
	}

	for first := range 256 {
		if native.single[first] != embedded.single[first] {
			report("code page %d byte %02x: Windows %04x, table %04x", codePage, first, native.single[first], embedded.single[first])
		}

		if native.double[first] == nil || embedded.double[first] == nil {
			continue
		}

		for trail := range 256 {
			if native.double[first][trail] != embedded.double[first][trail] {
				report("code page %d bytes %02x %02x: Windows %04x, table %04x",
					codePage, first, trail, native.double[first][trail], embedded.double[first][trail])
			}
		}
	}

	for code := range native.encode {
		if native.encode[code] != embedded.encode[code] {
			report("code page %d U+%04X: Windows %04x, table %04x", codePage, code, native.encode[code], embedded.encode[code])
		}
	}
}

// TestWindowsDecodePairs checks Decode against MultiByteToWideChar for every
// one- and two-byte input, so that a byte Windows reads as two code units or
// a lead byte it treats differently cannot hide behind the table layout.
func TestWindowsDecodePairs(t *testing.T) {
	t.Parallel()

	for _, codePage := range ansi {
		t.Run(strconv.Itoa(codePage), func(t *testing.T) {
			t.Parallel()

			errs := 0

			for first := range 256 {
				errs += checkDecode(t, codePage, []byte{byte(first)})

				for second := range 256 {
					errs += checkDecode(t, codePage, []byte{byte(first), byte(second)})
				}

				if errs > 20 {
					t.Fatal("too many differences")
				}
			}
		})
	}
}

// checkDecode returns 1 when Decode and Windows disagree on data.
func checkDecode(t *testing.T, codePage int, data []byte) int {
	t.Helper()

	want, ok := nativeDecode(codePage, data)
	got, err := Decode(data, codePage)

	switch {
	case ok && err != nil:
		t.Errorf("code page %d % x: Windows reads %+q, Decode fails: %v", codePage, data, want, err)
	case !ok && err == nil:
		t.Errorf("code page %d % x: Windows refuses it, Decode returns %+q", codePage, data, got)
	case ok && got != want:
		t.Errorf("code page %d % x: Windows reads %+q, Decode returns %+q", codePage, data, want, got)
	default:
		return 0
	}

	return 1
}
