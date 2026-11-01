package codepage_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/codepage"
)

// FuzzRoundTrip encodes text and decodes it back to the same string.
func FuzzRoundTrip(f *testing.F) {
	for _, cp := range []int{0, 1252, 1251, 932, 936, 65001} {
		f.Add("Hello ö Ж 日本", cp)
	}

	f.Fuzz(func(t *testing.T, text string, codePage int) {
		raw, err := codepage.Encode(text, codePage)
		if err != nil {
			return
		}

		got, err := codepage.Decode(raw, codePage)
		if err != nil {
			t.Fatalf("decode of encoded %q in %d: %v", text, codePage, err)
		}

		if got != text {
			t.Fatalf("code page %d: %q encodes to %x and decodes to %q", codePage, text, raw, got)
		}
	})
}

// FuzzDecode reads arbitrary bytes: whatever Decode accepts holds no U+FFFD
// for an ANSI code page, encodes, and decodes back to itself from there.
func FuzzDecode(f *testing.F) {
	for _, cp := range []int{0, 1252, 1251, 932, 936, 949, 950, 65001} {
		f.Add([]byte("Hello \x81\x93\xfa\xa4"), cp)
	}

	f.Fuzz(func(t *testing.T, data []byte, codePage int) {
		text, err := codepage.Decode(data, codePage)
		if err != nil {
			return
		}

		if codepage.IsANSI(codePage) && strings.ContainsRune(text, '\uFFFD') {
			t.Fatalf("code page %d: % x decodes to %q with a replacement character", codePage, data, text)
		}

		raw, err := codepage.Encode(text, codePage)
		if err != nil {
			t.Fatalf("code page %d: % x decodes to %q, which does not encode: %v", codePage, data, text, err)
		}

		if back, err := codepage.Decode(raw, codePage); err != nil || back != text {
			t.Fatalf("code page %d: %q encodes to % x and decodes to %q (%v)", codePage, text, raw, back, err)
		}
	})
}
