// Package codepage converts text to and from the Windows code pages a
// Windows Installer database may declare. Unmappable text is an error, never
// a substitution character (docs/spec/MSISPEC.md section 3, "Lossless
// database serialization").
//
// The ANSI code pages use tables dumped from Windows itself
// (MultiByteToWideChar and WideCharToMultiByte, see tables.go), not a
// generic implementation of the character sets, because the engine reads a
// package with the Windows mappings and those differ from the standards in
// places (private-use areas, vendor rows, duplicates).
package codepage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// Well-known code pages.
const (
	// ASCII is code page 0: the database holds 7-bit ASCII only.
	ASCII = 0
	// UTF8 is code page 65001.
	UTF8 = 65001
	// Windows1252 is the Western European ANSI page and the default.
	Windows1252 = 1252
)

// ansi lists the ANSI code pages; each has a table in tables/.
var ansi = []int{ //nolint:gochecknoglobals // lookup table
	874, 932, 936, 949, 950,
	1250, 1251, 1252, 1253, 1254, 1255, 1256, 1257, 1258,
}

// IsANSI reports whether cp is an ANSI code page that the Summary
// Information stream may declare (SUM-04: 65001 and 1200 fail with 1620).
func IsANSI(cp int) bool {
	return slices.Contains(ansi, cp)
}

// Supported reports whether this package can encode cp.
func Supported(cp int) bool {
	return cp == ASCII || cp == UTF8 || IsANSI(cp)
}

// Encode converts s to the code page.
func Encode(text string, codePage int) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("codepage: text is not valid UTF-8")
	}

	switch codePage {
	case UTF8:
		return []byte(text), nil
	case ASCII:
		for i := range len(text) {
			if text[i] >= 0x80 {
				return nil, fmt.Errorf("codepage: %q is not ASCII and the database code page is 0", text)
			}
		}

		return []byte(text), nil
	}

	tab, err := lookup(codePage)
	if err != nil {
		return nil, err
	}

	return tab.encodeText(text, codePage)
}

func (t *table) encodeText(text string, codePage int) ([]byte, error) {
	out := make([]byte, 0, len(text))

	for _, char := range text {
		if char > 0xFFFF || t.encode[char] == unmapped {
			return nil, fmt.Errorf("codepage: %q cannot be represented in code page %d", text, codePage)
		}

		if seq := t.encode[char]; seq > 0xFF {
			out = binary.BigEndian.AppendUint16(out, seq)
		} else {
			out = append(out, byte(seq&0xFF))
		}
	}

	return out, nil
}

// Decode converts bytes in the code page to a string. A byte or byte pair
// that Windows refuses to convert (MB_ERR_INVALID_CHARS) is an error.
func Decode(data []byte, codePage int) (string, error) {
	switch codePage {
	case UTF8:
		if !utf8.Valid(data) {
			return "", errors.New("codepage: invalid UTF-8")
		}

		return string(data), nil
	case ASCII:
		for _, c := range data {
			if c >= 0x80 {
				return "", errors.New("codepage: non-ASCII byte in a code page 0 database")
			}
		}

		return string(data), nil
	}

	tab, err := lookup(codePage)
	if err != nil {
		return "", err
	}

	return tab.decodeBytes(data, codePage)
}

func (t *table) decodeBytes(data []byte, codePage int) (string, error) {
	out := make([]byte, 0, len(data))

	for pos := 0; pos < len(data); pos++ {
		unit := t.single[data[pos]]

		switch unit {
		case unmapped:
			return "", fmt.Errorf("codepage: invalid byte %#04x for code page %d", data[pos], codePage)
		case leadByte:
			if pos+1 == len(data) {
				return "", fmt.Errorf("codepage: lead byte %#04x at the end of the text in code page %d", data[pos], codePage)
			}

			pos++

			unit = t.double[data[pos-1]][data[pos]]
			if unit == unmapped {
				return "", fmt.Errorf("codepage: invalid bytes %#04x %#04x for code page %d", data[pos-1], data[pos], codePage)
			}
		}

		out = utf8.AppendRune(out, rune(unit))
	}

	return string(out), nil
}
