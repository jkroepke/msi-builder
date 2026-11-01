package codepage //nolint:testpackage // the tests reach the unexported table parser

import (
	"encoding/binary"
	"testing"
)

func encodeValues(values ...uint16) []byte {
	raw := make([]byte, 2*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint16(raw[2*i:], v)
	}

	return raw
}

// TestParseTableMalformed feeds the parser broken layouts; each must fail
// rather than panic or build a table that writes a lone lead byte.
func TestParseTableMalformed(t *testing.T) {
	t.Parallel()

	single := make([]uint16, 256)
	for i := range single {
		single[i] = uint16(i)
	}

	withLead := append([]uint16(nil), single...)
	withLead[0x81] = leadByte

	row := make([]uint16, 256)
	for i := range row {
		row[i] = unmapped
	}

	full := func(values ...[]uint16) []byte {
		var all []uint16
		for _, v := range values {
			all = append(all, v...)
		}

		return encodeValues(all...)
	}

	cases := map[string][]byte{
		"odd length":           {0x00},
		"short single table":   encodeValues(1, 2, 3),
		"missing count":        full(single),
		"missing lead row":     full(withLead, []uint16{0, 0}),
		"count mismatch":       full(single, []uint16{1, 0}),
		"lone lead override":   full(withLead, row, []uint16{1, 0, 'A', 0x81}),
		"unmapped byte target": full(withLead, row, []uint16{1, 0, 'A', 0x0100 | 0x41}),
	}

	for name, data := range cases {
		if _, err := parseTable(data); err == nil {
			t.Errorf("%s: parseTable succeeded", name)
		}
	}

	if _, err := parseTable(full(withLead, row, []uint16{1, 0, 'A', 0x8140})); err != nil {
		t.Errorf("valid override: %v", err)
	}
}

func TestLookupUnknown(t *testing.T) {
	t.Parallel()

	if _, err := lookup(437); err == nil {
		t.Error("lookup(437) succeeded")
	}

	if _, err := Decode([]byte{0x41}, 437); err == nil {
		t.Error("Decode in code page 437 succeeded")
	}
}
