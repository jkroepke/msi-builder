package msidb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jkroepke/msi-builder/internal/cfb"
)

// Stream name encoding of section 3: table streams carry a U+4840 prefix,
// pairs of alphabet characters pack into U+3800–U+47FF and single ones into
// U+4800–U+483F; every other character is copied.
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz._"

const (
	tablePrefix = 0x4840
	pairBase    = 0x3800
	singleBase  = 0x4800
	maxEncoded  = 31
)

func alphaIndex(char rune) int {
	switch {
	case char >= '0' && char <= '9':
		return int(char - '0')
	case char >= 'A' && char <= 'Z':
		return int(char-'A') + 10
	case char >= 'a' && char <= 'z':
		return int(char-'a') + 36
	case char == '.':
		return 62
	case char == '_':
		return 63
	}

	return -1
}

// EncodeStreamName encodes a decoded stream or table name. It fails when the
// result exceeds 31 UTF-16 units, which is the engine's limit (MSIDB-11).
func EncodeStreamName(name string, table bool) (string, error) {
	if err := checkDecodedName(name); err != nil {
		return "", err
	}

	var out []uint16

	if table {
		out = append(out, tablePrefix)
	}

	runes := []rune(name)

	for i := 0; i < len(runes); i++ {
		char := runes[i]
		if char >= pairBase && char <= tablePrefix {
			return "", fmt.Errorf("msidb: stream name %q contains the reserved character %U", name, char)
		}

		idx := alphaIndex(char)
		if idx < 0 {
			out = append(out, utf16.Encode([]rune{char})...)

			continue
		}

		if i+1 < len(runes) {
			if b := alphaIndex(runes[i+1]); b >= 0 {
				out = append(out, uint16(pairBase+idx+(b<<6))) //nolint:gosec // bounded by the format limits checked earlier
				i++

				continue
			}
		}

		out = append(out, uint16(singleBase+idx)) //nolint:gosec // bounded by the format limits checked earlier
	}

	if len(out) > maxEncoded {
		return "", fmt.Errorf("msidb: stream name %q encodes to %d UTF-16 units, the limit is %d", name, len(out), maxEncoded)
	}

	return string(utf16.Decode(out)), nil
}

func checkDecodedName(name string) error {
	if name == "" {
		return errors.New("msidb: empty stream name")
	}

	if !utf8.ValidString(name) {
		return fmt.Errorf("msidb: stream name %q is not valid UTF-8", name)
	}

	return nil
}

// DecodeStreamName decodes an encoded directory entry name.
func DecodeStreamName(encoded string) (string, bool) {
	units := utf16.Encode([]rune(encoded))

	var table bool

	if len(units) > 0 && units[0] == tablePrefix {
		table = true
		units = units[1:]
	}

	// Decode into UTF-16 units first, so that a surrogate pair copied
	// through unchanged is joined again by utf16.Decode.
	out := make([]uint16, 0, 2*len(units))

	for _, unit := range units {
		switch {
		case unit >= pairBase && unit < singleBase:
			v := unit - pairBase
			out = append(out, uint16(alphabet[v&0x3F]), uint16(alphabet[(v>>6)&0x3F]))
		case unit >= singleBase && unit < tablePrefix:
			out = append(out, uint16(alphabet[unit-singleBase]))
		default:
			out = append(out, unit)
		}
	}

	return string(utf16.Decode(out)), table
}

// CheckStreamName reports whether name can be stored as a root stream that
// is not a table: it must encode (section 3) to at most 31 UTF-16 units that
// the compound file accepts. The 62-character limit of MSIDB-11 holds only for
// names made of the 64 alphabet characters; every other character takes a
// unit of its own.
func CheckStreamName(name string) error {
	encoded, err := EncodeStreamName(name, false)
	if err != nil {
		return err
	}

	if err := checkStoredRunes(name); err != nil {
		return err
	}

	if _, err := cfb.EncodeName(encoded); err != nil {
		return fmt.Errorf("msidb: stream name %q cannot be stored: %w", name, err)
	}

	return nil
}

// checkStoredRunes names a character the compound file refuses as the user
// wrote it; the encoded name is unreadable. Alphabet characters encode to
// uncased units, so only the others can be refused.
func checkStoredRunes(name string) error {
	for _, r := range name {
		switch {
		case strings.ContainsRune("\x00/\\:!", r):
			return fmt.Errorf("msidb: stream name %q contains %q", name, r)
		case cfb.Cased(r):
			return fmt.Errorf("msidb: stream name %q contains the cased character %q", name, r)
		}
	}

	return nil
}

// BinaryStreamName is the decoded name of the stream holding a binary cell:
// the table name and the row's key values joined with ".", integer keys in
// decimal and a NULL key as an empty segment.
func BinaryStreamName(table string, keys []any) string {
	parts := []string{table}

	for _, k := range keys {
		switch key := k.(type) {
		case nil:
			// A NULL key is an empty segment: msi.dll reads the stream of
			// a NULL string or integer key as "T." (reported by a
			// review's native probes).
			parts = append(parts, "")
		case string:
			parts = append(parts, key)
		case int:
			parts = append(parts, strconv.Itoa(key))
		default:
			parts = append(parts, fmt.Sprint(key))
		}
	}

	return strings.Join(parts, ".")
}
