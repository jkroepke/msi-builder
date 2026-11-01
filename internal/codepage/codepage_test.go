package codepage_test

import (
	"testing"

	"github.com/jkroepke/msi-builder/internal/codepage"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cp   int
		s    string
		want []byte
	}{
		{codepage.ASCII, "Hello", []byte("Hello")},
		{codepage.UTF8, "Grüße €", []byte("Grüße €")},
		{codepage.Windows1252, "Grüße €", []byte{'G', 'r', 0xFC, 0xDF, 'e', ' ', 0x80}},
		{932, "日本", []byte{0x93, 0xFA, 0x96, 0x7B}},
	}

	for _, tc := range cases {
		got, err := codepage.Encode(tc.s, tc.cp)
		if err != nil {
			t.Errorf("Encode(%q, %d): %v", tc.s, tc.cp, err)

			continue
		}

		if string(got) != string(tc.want) {
			t.Errorf("Encode(%q, %d) = % x, want % x", tc.s, tc.cp, got, tc.want)
		}

		back, err := codepage.Decode(got, tc.cp)
		if err != nil || back != tc.s {
			t.Errorf("Decode(%d) = %q, %v", tc.cp, back, err)
		}
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	if _, err := codepage.Encode("日本", codepage.Windows1252); err == nil {
		t.Error("expected an error for characters outside the code page")
	}

	if _, err := codepage.Encode("é", codepage.ASCII); err == nil {
		t.Error("expected an error for non-ASCII in code page 0")
	}

	if _, err := codepage.Encode("x", 12345); err == nil {
		t.Error("expected an error for an unknown code page")
	}

	if codepage.Supported(12345) || !codepage.Supported(1252) || !codepage.IsANSI(1252) || codepage.IsANSI(codepage.UTF8) {
		t.Error("Supported/IsANSI")
	}
}

// TestWindowsVectors holds conversions verified with MultiByteToWideChar and
// WideCharToMultiByte where generic character set tables disagree with
// Windows. TestWindowsTables compares every mapping on Windows CI.
func TestWindowsVectors(t *testing.T) {
	t.Parallel()

	decodes := []struct {
		cp   int
		data []byte
		want string
	}{
		{936, []byte{0xA8, 0xBF}, ""},
		{950, []byte{0x88, 0x66}, ""},
		{950, []byte{0xF9, 0xFE}, "▓"},
		{932, []byte{0xF0, 0x40}, ""},
	}

	for _, tc := range decodes {
		got, err := codepage.Decode(tc.data, tc.cp)
		if err != nil || got != tc.want {
			t.Errorf("Decode(% x, %d) = %+q, %v; want %+q", tc.data, tc.cp, got, err, tc.want)
		}

		enc, err := codepage.Encode(tc.want, tc.cp)
		if err != nil || string(enc) != string(tc.data) {
			t.Errorf("Encode(%+q, %d) = % x, %v; want % x", tc.want, tc.cp, enc, err, tc.data)
		}
	}

	// CP932 has U+7E8A twice: in the NEC-selected IBM rows (ED 40) and in
	// the IBM rows (FA 5C). Windows reads both and writes FA 5C, while
	// generic Shift-JIS tables write ED 40.
	for _, data := range [][]byte{{0xED, 0x40}, {0xFA, 0x5C}} {
		if got, err := codepage.Decode(data, 932); err != nil || got != "\u7E8A" {
			t.Errorf("Decode(% x, 932) = %+q, %v; want U+7E8A", data, got, err)
		}
	}

	if got, err := codepage.Encode("\u7E8A", 932); err != nil || string(got) != "\xFA\x5C" {
		t.Errorf("Encode(U+7E8A, 932) = % x, %v; want fa 5c", got, err)
	}

	// Windows has no mapping for these, so the bytes a generic table
	// produces would read back as other characters.
	for _, tc := range []struct {
		cp   int
		text string
	}{{936, "ǹ"}, {950, "Ê"}} {
		if got, err := codepage.Encode(tc.text, tc.cp); err == nil {
			t.Errorf("Encode(%+q, %d) = % x, want an error", tc.text, tc.cp, got)
		}
	}
}

func TestDecodeMalformed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		cp   int
		data []byte
	}{
		{932, []byte{'a', 0x81}},      // lead byte without a trail byte
		{932, []byte{0x81, 0x0A}},     // trail byte out of range
		{codepage.UTF8, []byte{0xFF}}, // invalid UTF-8
		{codepage.ASCII, []byte{0xE9}},
	} {
		if got, err := codepage.Decode(tc.data, tc.cp); err == nil {
			t.Errorf("Decode(% x, %d) = %+q, want an error", tc.data, tc.cp, got)
		}
	}
}

// TestTablesRoundTrip checks that every character a table encodes decodes
// back to itself, in every ANSI code page.
func TestTablesRoundTrip(t *testing.T) {
	t.Parallel()

	for _, codePage := range []int{874, 932, 936, 949, 950, 1250, 1251, 1252, 1253, 1254, 1255, 1256, 1257, 1258} {
		mapped := 0

		for code := range rune(0x10000) {
			if code >= 0xD800 && code <= 0xDFFF {
				continue
			}

			raw, err := codepage.Encode(string(code), codePage)
			if err != nil {
				continue
			}

			mapped++

			back, err := codepage.Decode(raw, codePage)
			if err != nil || back != string(code) {
				t.Fatalf("code page %d: U+%04X encodes to % x and decodes to %+q, %v", codePage, code, raw, back, err)
			}
		}

		if mapped < 128 {
			t.Errorf("code page %d maps only %d characters", codePage, mapped)
		}
	}
}
