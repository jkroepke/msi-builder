// Package summary writes and reads the \u0005SummaryInformation stream of a
// Windows Installer package: a one-section OLE property set
// (docs/spec/MSISPEC.md section 4).
package summary

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jkroepke/msi-builder/internal/codepage"
)

// StreamName is the literal name of the summary stream.
const StreamName = "\u0005SummaryInformation"

// Property IDs.
const (
	PIDCodepage     = 1
	PIDTitle        = 2
	PIDSubject      = 3
	PIDAuthor       = 4
	PIDKeywords     = 5
	PIDComments     = 6
	PIDTemplate     = 7
	PIDLastAuthor   = 8
	PIDRevision     = 9
	PIDLastPrinted  = 11
	PIDCreated      = 12
	PIDLastSaved    = 13
	PIDPageCount    = 14
	PIDWordCount    = 15
	PIDCharCount    = 16
	PIDAppName      = 18
	PIDSecurity     = 19
	maxPID          = 19
	maxStreamSize   = 262144
	systemIdentifer = 0x0002000A // what the native writer stores (SUM-07)
)

// Variant types.
const (
	vtI2       = 0x0002
	vtI4       = 0x0003
	vtLPSTR    = 0x001E
	vtFILETIME = 0x0040
)

var fmtid = [16]byte{0xE0, 0x85, 0x9F, 0xF2, 0xF9, 0x4F, 0x68, 0x10, 0xAB, 0x91, 0x08, 0x00, 0x2B, 0x27, 0xB3, 0xD9} //nolint:gochecknoglobals // lookup table

// expectedType is the variant type each PID takes.
var expectedType = map[int]uint16{ //nolint:gochecknoglobals // lookup table
	PIDCodepage: vtI2, PIDTitle: vtLPSTR, PIDSubject: vtLPSTR, PIDAuthor: vtLPSTR,
	PIDKeywords: vtLPSTR, PIDComments: vtLPSTR, PIDTemplate: vtLPSTR, PIDLastAuthor: vtLPSTR,
	PIDRevision: vtLPSTR, PIDLastPrinted: vtFILETIME, PIDCreated: vtFILETIME, PIDLastSaved: vtFILETIME,
	PIDPageCount: vtI4, PIDWordCount: vtI4, PIDCharCount: vtI4, PIDAppName: vtLPSTR, PIDSecurity: vtI4,
}

// Property is one summary property. Value is int (I2 for PID 1, I4
// otherwise), string or time.Time (UTC).
type Property struct {
	Value any
	ID    int
}

// Word Count flags (PID 15).
const (
	WordShortNames  = 0x1
	WordCompressed  = 0x2
	WordAdminImage  = 0x4
	WordNoElevation = 0x8
)

const (
	epochSeconds = 11644473600 // seconds between 1601 and 1970
	ticksPerSec  = 10000000    // FILETIME counts 100 ns ticks
	maxSeconds   = math.MaxUint64/ticksPerSec - 1
)

// FileTime converts t to a Windows FILETIME value. Times outside the
// FILETIME range (before 1601 or after the year 60056) give 0; Marshal
// rejects them.
func FileTime(when time.Time) uint64 {
	sec, ok := fileTimeSeconds(when)
	if !ok {
		return 0
	}

	return sec*ticksPerSec + uint64(when.Nanosecond()/100) //nolint:gosec // Nanosecond is below 1e9
}

// fileTimeSeconds returns the seconds since 1601, computed without
// UnixNano, which overflows outside 1678 to 2262.
func fileTimeSeconds(when time.Time) (uint64, bool) {
	unix := when.Unix()
	if unix < -epochSeconds || unix > maxSeconds-epochSeconds {
		return 0, false
	}

	return uint64(unix + epochSeconds), true
}

// FromFileTime converts a FILETIME to a UTC time.
func FromFileTime(ft uint64) time.Time {
	return time.Unix(int64(ft/ticksPerSec)-epochSeconds, int64(ft%ticksPerSec)*100).UTC() //nolint:gosec // ft/1e7 fits int64
}

// Marshal serializes the properties. Strings are written in cp, which must
// be an ANSI code page and should equal the PID 1 value.
func Marshal(props []Property, codePage int) ([]byte, error) { //nolint:cyclop,gocyclo,gocognit // one pass over the format; splitting would scatter it
	if !codepage.IsANSI(codePage) {
		return nil, fmt.Errorf("summary: code page %d is not an ANSI code page", codePage)
	}

	sorted := append([]Property(nil), props...)
	slices.SortFunc(sorted, func(a, b Property) int { return a.ID - b.ID })

	hasCodepage := false

	for i, prop := range sorted {
		if prop.ID <= 0 || prop.ID > maxPID || prop.ID == 10 || prop.ID == 17 {
			return nil, fmt.Errorf("summary: PID %d is not valid", prop.ID)
		}

		if i > 0 && sorted[i-1].ID == prop.ID {
			return nil, fmt.Errorf("summary: PID %d given twice", prop.ID)
		}

		if prop.ID == PIDCodepage {
			hasCodepage = true
		}
	}

	if !hasCodepage {
		return nil, errors.New("summary: PID 1 (code page) is required")
	}

	order := binary.LittleEndian

	var values [][]byte

	for _, prop := range sorted {
		vt := expectedType[prop.ID]
		buf := order.AppendUint16(nil, vt)
		buf = append(buf, 0, 0)

		switch vt {
		case vtI2:
			n, ok := prop.Value.(int)
			if !ok || n < 0 || n > 0xFFFF {
				return nil, fmt.Errorf("summary: PID %d wants a 16-bit integer, got %v", prop.ID, prop.Value)
			}

			buf = append(buf, byte(n), byte(n>>8), 0, 0) //nolint:gosec // range checked above
		case vtI4:
			n, ok := prop.Value.(int)
			if !ok || n < -2147483648 || n > 2147483647 {
				return nil, fmt.Errorf("summary: PID %d wants a 32-bit integer, got %v", prop.ID, prop.Value)
			}

			buf = order.AppendUint32(buf, uint32(int32(n))) //nolint:gosec // range checked above; two's complement is the encoding
		case vtLPSTR:
			str, ok := prop.Value.(string)
			if !ok {
				return nil, fmt.Errorf("summary: PID %d wants a string, got %v", prop.ID, prop.Value)
			}

			if str == "" {
				return nil, fmt.Errorf("summary: PID %d is empty; omit it instead", prop.ID)
			}

			encoded, err := codepage.Encode(str, codePage)
			if err != nil {
				return nil, fmt.Errorf("summary: PID %d: %w", prop.ID, err)
			}

			buf = order.AppendUint32(buf, uint32(len(encoded)+1)) //nolint:gosec // bounded by the format limits checked earlier
			buf = append(buf, encoded...)
			buf = append(buf, 0)

			for len(buf)%4 != 0 {
				buf = append(buf, 0)
			}
		case vtFILETIME:
			when, ok := prop.Value.(time.Time)
			if !ok {
				return nil, fmt.Errorf("summary: PID %d wants a time, got %v", prop.ID, prop.Value)
			}

			if _, ok := fileTimeSeconds(when); !ok {
				return nil, fmt.Errorf("summary: PID %d: %v is outside the FILETIME range", prop.ID, when)
			}

			buf = order.AppendUint64(buf, FileTime(when))
		}

		values = append(values, buf)
	}

	count := len(sorted)
	sectionSize := 8 + 8*count

	for _, v := range values {
		sectionSize += len(v)
	}

	out := make([]byte, 0x30, 0x30+sectionSize)
	order.PutUint16(out[0x00:], 0xFFFE)
	order.PutUint32(out[0x04:], systemIdentifer)
	order.PutUint32(out[0x18:], 1)
	copy(out[0x1C:], fmtid[:])
	order.PutUint32(out[0x2C:], 0x30)

	out = order.AppendUint32(out, uint32(sectionSize)) //nolint:gosec // bounded by the format limits checked earlier
	out = order.AppendUint32(out, uint32(count))       //nolint:gosec // bounded by the format limits checked earlier

	offset := 8 + 8*count

	for i, p := range sorted {
		out = order.AppendUint32(out, uint32(p.ID))   //nolint:gosec // bounded by the format limits checked earlier
		out = order.AppendUint32(out, uint32(offset)) //nolint:gosec // bounded by the format limits checked earlier
		offset += len(values[i])
	}

	for _, v := range values {
		out = append(out, v...)
	}

	if len(out) > maxStreamSize {
		return nil, errors.New("summary: stream exceeds 262144 bytes")
	}

	return out, nil
}

// entry is one property's ID and value offset in the section.
type entry struct{ pid, off int }

// Unmarshal parses a summary stream. Strings are decoded with the PID 1 code
// page.
func Unmarshal(data []byte) ([]Property, error) { //nolint:cyclop,gocognit // one pass over the format; splitting would scatter it
	order := binary.LittleEndian

	if len(data) < 0x30+8 || order.Uint16(data) != 0xFFFE {
		return nil, errors.New("summary: not a property set")
	}

	if order.Uint32(data[0x18:]) < 1 || [16]byte(data[0x1C:0x2C]) != fmtid {
		return nil, errors.New("summary: not a SummaryInformation property set")
	}

	// Each field is compared as uint32 before it becomes an int: on a
	// 32-bit build a value of 2^31 or more turns negative and would pass
	// the checks below.
	rawSec := order.Uint32(data[0x2C:])
	if uint64(rawSec)+8 > uint64(len(data)) {
		return nil, errors.New("summary: section offset out of range")
	}

	sec := int(rawSec)
	rawSize, rawCount := order.Uint32(data[sec:]), order.Uint32(data[sec+4:])

	room := uint64(len(data) - sec) //nolint:gosec // sec <= len(data)-8 was checked
	if uint64(rawSize) > room || (rawCount > 0 && (rawSize < 8 || uint64(rawCount) > uint64(rawSize-8)/8)) {
		return nil, errors.New("summary: section size or count out of range")
	}

	size, count := int(rawSize), int(rawCount)

	section := data[sec : sec+size]

	ents := make([]entry, count)
	for i := range ents {
		ents[i] = entry{int(order.Uint32(section[8+8*i:])), int(order.Uint32(section[12+8*i:]))}
	}

	if err := checkEntries(section, ents); err != nil {
		return nil, err
	}

	codePage := codepage.Windows1252

	for _, e := range ents {
		if e.pid == PIDCodepage && e.off+8 <= len(section) {
			codePage = int(order.Uint16(section[e.off+4:]))
		}
	}

	var props []Property

	for _, ent := range ents {
		if ent.off+4 > len(section) {
			return nil, fmt.Errorf("summary: PID %d value out of range", ent.pid)
		}

		valueType := order.Uint16(section[ent.off:])
		data := section[ent.off+4:]

		switch valueType {
		case vtI2:
			if len(data) < 2 {
				return nil, fmt.Errorf("summary: PID %d truncated", ent.pid)
			}

			props = append(props, Property{ID: ent.pid, Value: int(order.Uint16(data))})
		case vtI4:
			if len(data) < 4 {
				return nil, fmt.Errorf("summary: PID %d truncated", ent.pid)
			}

			props = append(props, Property{ID: ent.pid, Value: int(int32(order.Uint32(data)))}) //nolint:gosec // VT_I4 is a signed 32-bit value
		case vtLPSTR:
			if len(data) < 4 {
				return nil, fmt.Errorf("summary: PID %d truncated", ent.pid)
			}

			n := int(order.Uint32(data))
			if n < 1 || n > len(data)-4 {
				return nil, fmt.Errorf("summary: PID %d string length %d out of range", ent.pid, n)
			}

			s, err := codepage.Decode(data[4:4+n-1], codePage)
			if err != nil {
				return nil, fmt.Errorf("summary: PID %d: %w", ent.pid, err)
			}

			props = append(props, Property{ID: ent.pid, Value: s})
		case vtFILETIME:
			if len(data) < 8 {
				return nil, fmt.Errorf("summary: PID %d truncated", ent.pid)
			}

			props = append(props, Property{ID: ent.pid, Value: FromFileTime(order.Uint64(data))})
		case 0, 1: // VT_EMPTY, VT_NULL: absent
		default:
			return nil, fmt.Errorf("summary: PID %d has variant type %#x", ent.pid, valueType)
		}
	}

	return props, nil
}

// checkEntries rejects a property list that names a PID twice or whose
// values overlap each other or the list itself. Values are decoded one by
// one, so a list of aliases would multiply one large string in memory.
func checkEntries(section []byte, ents []entry) error {
	seen := make(map[int]bool, len(ents))

	type extent struct{ pid, start, end int }

	extents := make([]extent, 0, len(ents))

	for _, ent := range ents {
		if seen[ent.pid] {
			return fmt.Errorf("summary: PID %d appears twice", ent.pid)
		}

		seen[ent.pid] = true

		end, err := valueEnd(section, ent.off)
		if err != nil {
			return fmt.Errorf("summary: PID %d %w", ent.pid, err)
		}

		extents = append(extents, extent{ent.pid, ent.off, end})
	}

	slices.SortFunc(extents, func(a, b extent) int { return a.start - b.start })

	lastEnd := 8 + 8*len(ents)

	for _, ext := range extents {
		if ext.start < lastEnd {
			return fmt.Errorf("summary: PID %d value overlaps another entry", ext.pid)
		}

		lastEnd = ext.end
	}

	return nil
}

// valueEnd returns the end offset of the typed value at off, or an error for
// a value that does not fit the section. Unknown types end after their
// header; Unmarshal rejects them later.
func valueEnd(section []byte, off int) (int, error) {
	if off < 0 || off > len(section)-4 {
		return 0, errors.New("value out of range")
	}

	size := 0

	switch binary.LittleEndian.Uint16(section[off:]) {
	case vtI2:
		size = 2
	case vtI4:
		size = 4
	case vtFILETIME:
		size = 8
	case vtLPSTR:
		if off > len(section)-8 {
			return 0, errors.New("truncated")
		}

		n := binary.LittleEndian.Uint32(section[off+4:])
		if int64(n) > int64(len(section)-off-8) {
			return 0, errors.New("truncated")
		}

		size = 4 + int(n)
	}

	if size > len(section)-off-4 {
		return 0, errors.New("truncated")
	}

	return off + 4 + size, nil
}

// Get returns the value of a PID, or nil.
func Get(props []Property, pid int) any {
	for _, p := range props {
		if p.ID == pid {
			return p.Value
		}
	}

	return nil
}
