package codepage

import (
	"bytes"
	"compress/flate"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// The tables in tables/ are dumped from Windows by TestWindowsTables
// (tables_windows_test.go): run it on Windows with
// MSI_BUILDER_UPDATE_TABLES=1 to write them, and without it on every Windows
// CI run to compare them with the code page functions of the runner.
//
// A file is a raw DEFLATE stream of little-endian uint16 values:
//
//   - 256 entries for the single bytes 0x00–0xFF: the UTF-16 code unit
//     MultiByteToWideChar returns, unmapped (0xFFFF) when it refuses the
//     byte with MB_ERR_INVALID_CHARS, or leadByte (0xFFFE) for a lead byte
//     (IsDBCSLeadByteEx).
//   - For each lead byte in ascending order, 256 entries for the trail bytes
//     0x00–0xFF, unmapped when the pair does not convert to one code unit.
//   - Two values, the low and high half of a count N, then N pairs of a code
//     unit and its bytes (a single byte, or lead<<8|trail; unmapped when
//     there are none). They are where WideCharToMultiByte with
//     WC_NO_BEST_FIT_CHARS differs from the inverse of the decoding table,
//     which takes the first byte sequence in table order for a code unit.
//
//go:embed tables/*.bin
var tableFiles embed.FS

const (
	unmapped = 0xFFFF
	leadByte = 0xFFFE
)

// table holds one ANSI code page.
type table struct {
	double [256]*[256]uint16
	// encode maps a BMP code unit to its bytes.
	encode []uint16
	single [256]uint16
}

type loaded struct {
	err  error
	tab  *table
	once sync.Once
}

// tables loads each code page on first use.
var tables = func() map[int]*loaded { //nolint:gochecknoglobals // lazily loaded, read-only tables
	slots := make(map[int]*loaded, len(ansi))
	for _, codePage := range ansi {
		slots[codePage] = &loaded{}
	}

	return slots
}()

func lookup(codePage int) (*table, error) {
	slot, ok := tables[codePage]
	if !ok {
		return nil, fmt.Errorf("codepage: code page %d is not supported", codePage)
	}

	slot.once.Do(func() {
		slot.tab, slot.err = load(codePage)
	})

	return slot.tab, slot.err
}

func load(codePage int) (*table, error) {
	raw, err := tableFiles.ReadFile(fmt.Sprintf("tables/%d.bin", codePage))
	if err != nil {
		return nil, fmt.Errorf("codepage: table for code page %d: %w", codePage, err)
	}

	data, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return nil, fmt.Errorf("codepage: table for code page %d: %w", codePage, err)
	}

	tab, err := parseTable(data)
	if err != nil {
		return nil, fmt.Errorf("codepage: table for code page %d: %w", codePage, err)
	}

	return tab, nil
}

var errTable = errors.New("malformed table")

// parseTable reads the uncompressed layout described above.
func parseTable(data []byte) (*table, error) {
	if len(data)%2 != 0 {
		return nil, errTable
	}

	values := make([]uint16, len(data)/2)
	for i := range values {
		values[i] = binary.LittleEndian.Uint16(data[2*i:])
	}

	tab := &table{}

	values, err := tab.parseDecoding(values)
	if err != nil {
		return nil, err
	}

	if len(values) < 2 {
		return nil, errTable
	}

	count := int(values[0]) | int(values[1])<<16
	values = values[2:]

	if len(values) != 2*count {
		return nil, errTable
	}

	tab.encode = tab.inverse()

	for i := 0; i < len(values); i += 2 {
		if !tab.validSequence(values[i+1]) {
			return nil, errTable
		}

		tab.encode[values[i]] = values[i+1]
	}

	return tab, nil
}

// parseDecoding reads the single-byte and lead-byte rows and returns the
// rest.
func (t *table) parseDecoding(values []uint16) ([]uint16, error) {
	if len(values) < len(t.single) {
		return nil, errTable
	}

	copy(t.single[:], values)
	values = values[len(t.single):]

	for lead, unit := range t.single {
		if unit != leadByte {
			continue
		}

		row := new([256]uint16)
		if len(values) < len(row) {
			return nil, errTable
		}

		copy(row[:], values)
		t.double[lead] = row
		values = values[len(row):]
	}

	return values, nil
}

// inverse maps each code unit to its first byte sequence in table order.
func (t *table) inverse() []uint16 {
	enc := make([]uint16, 0x10000)
	for i := range enc {
		enc[i] = unmapped
	}

	for b, unit := range t.single {
		if unit != unmapped && unit != leadByte && enc[unit] == unmapped {
			enc[unit] = uint16(b)
		}
	}

	for lead, row := range t.double {
		if row != nil {
			fillInverse(enc, uint16(lead)<<8, row[:])
		}
	}

	return enc
}

// fillInverse records base|trail for each unit of row that has no bytes yet.
func fillInverse(enc []uint16, base uint16, row []uint16) {
	for trail, unit := range row {
		if unit != unmapped && enc[unit] == unmapped {
			enc[unit] = base | uint16(trail)
		}
	}
}

// validSequence reports whether an override is unmapped, a single byte that
// is not a lead byte, or a lead byte with a trail byte.
func (t *table) validSequence(seq uint16) bool {
	switch {
	case seq == unmapped:
		return true
	case seq <= 0xFF:
		return t.single[seq] != leadByte && t.single[seq] != unmapped
	default:
		return t.single[seq>>8] == leadByte
	}
}
