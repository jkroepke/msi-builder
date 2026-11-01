package msidb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/codepage"
	"github.com/jkroepke/msi-builder/internal/summary"
)

type poolEntry struct {
	text     string
	length   int
	refcount int
	long     bool
}

// Unmarshal reads a package. It accepts what other writers emit (free pool
// slots, unsorted pool, empty table streams) and records soft problems such
// as refcount mismatches and unsorted rows in Database.Problems.
func Unmarshal(data []byte) (*Database, error) {
	file, err := cfb.Read(data)
	if err != nil {
		return nil, fmt.Errorf("msidb: %w", err)
	}

	return decodeFile(file, true)
}

// Decode reads the database of an opened compound file. It loads the table
// streams, the summary and the binary streams that table cells reference,
// and lists every other stream (such as a cabinet) in Database.Sources by
// name with its size and a nil Open, leaving the bytes in the file for
// cfb.File.Reader. Unmarshal loads those too. Both return the root-level
// storages, such as embedded language transforms, in Database.Storages
// with streams that read from the file, and refuse a storage inside one.
func Decode(file *cfb.File) (*Database, error) {
	return decodeFile(file, false)
}

// Problems with a binary cell's stream, completing "binary stream %q ".
var (
	errBinaryMissing = errors.New("is missing")
	errBinaryShared  = errors.New("already belongs to an earlier row with the same primary key")
)

// streamData returns a stream's bytes, loading them on demand.
func streamData(file *cfb.File, name string) ([]byte, error) {
	data, ok := file.Stream(name)
	if !ok {
		return nil, fmt.Errorf("msidb: cannot read stream %q", name)
	}

	return data, nil
}

// exactName refuses an entry whose stored name does not survive as a Go
// string, such as one with a lone surrogate, which reads as U+FFFD and
// would be written back under another name.
func exactName(ent *cfb.Entry) error {
	if !bytes.Equal(utf16le(utf16.Encode([]rune(ent.Name))), ent.RawName) {
		return fmt.Errorf("msidb: entry name %q holds UTF-16 units that do not round-trip", ent.Name)
	}

	return nil
}

func utf16le(units []uint16) []byte {
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}

	return out
}

// Limits on Database.Problems, so that a crafted package cannot grow them
// without bound: the number of problems kept and the length of each.
const (
	maxProblems      = 100
	maxProblemLength = 256
)

// addProblem records a soft problem, cut to maxProblemLength bytes, and
// after maxProblems of them one note that further ones were left out.
func addProblem(database *Database, msg string) {
	switch {
	case len(database.Problems) > maxProblems:
		return
	case len(database.Problems) == maxProblems:
		database.Problems = append(database.Problems, "further problems left out")

		return
	}

	if len(msg) > maxProblemLength {
		cut := maxProblemLength
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}

		msg = msg[:cut] + "..."
	}

	database.Problems = append(database.Problems, msg)
}

// decodeStorages returns the root-level storages, such as embedded language
// transforms (MSISPEC section 7.6), with readers over their streams, so that
// MarshalTo writes them back. A storage inside a storage has no place in
// cfb.Storage and is refused rather than dropped.
func decodeStorages(file *cfb.File) ([]cfb.Storage, error) {
	children := make(map[int][]*cfb.Entry)

	for _, ent := range file.Entries {
		if ent.Parent > 0 {
			children[ent.Parent] = append(children[ent.Parent], ent)
		}
	}

	var storages []cfb.Storage

	for _, ent := range file.Entries {
		if ent.Type != 1 || ent.Parent != 0 {
			continue
		}

		if err := exactName(ent); err != nil {
			return nil, err
		}

		storage := cfb.Storage{Name: ent.Name, CLSID: ent.CLSID}

		for _, child := range children[ent.SID] {
			stream, err := storageStream(file, ent, child)
			if err != nil {
				return nil, err
			}

			storage.Streams = append(storage.Streams, stream)
		}

		storages = append(storages, storage)
	}

	return storages, nil
}

// storageStream returns a stream of a storage with a reader over the file.
func storageStream(file *cfb.File, storage, child *cfb.Entry) (cfb.Source, error) {
	if child.Type != 2 {
		return cfb.Source{}, fmt.Errorf("msidb: storage %q holds storage %q; nested storages are not supported", storage.Name, child.Name)
	}

	if err := exactName(child); err != nil {
		return cfb.Source{}, err
	}

	// Check the chain now, so that a broken stream fails here and not in
	// a later MarshalTo.
	if _, err := file.EntryReader(child); err != nil {
		return cfb.Source{}, fmt.Errorf("msidb: storage %q: %w", storage.Name, err)
	}

	return cfb.Source{
		Name: child.Name,
		Size: int64(child.Size), //nolint:gosec // checked against the file size
		Open: func() (io.ReadCloser, error) {
			reader, err := file.EntryReader(child)
			if err != nil {
				return nil, fmt.Errorf("msidb: storage %q: %w", storage.Name, err)
			}

			return io.NopCloser(reader), nil
		},
	}, nil
}

func decodeFile(file *cfb.File, loadAll bool) (*Database, error) { //nolint:maintidx,cyclop,gocyclo,gocognit // one pass over the format
	if file.CLSID != cfb.CLSIDMSI {
		return nil, fmt.Errorf("msidb: root CLSID %x is not the .msi CLSID", file.CLSID)
	}

	tableStreams := make(map[string][]byte)
	otherStreams := make(map[string][]byte)
	sizes := make(map[string]uint64)
	stored := make(map[string]string) // decoded name -> name as stored in the file

	var order []string

	type decodedName struct {
		name  string
		table bool
	}

	seenNames := make(map[decodedName]string) // decoded name -> name as stored

	storages, err := decodeStorages(file)
	if err != nil {
		return nil, err
	}

	for _, ent := range file.Entries {
		if ent.Type != 2 || ent.Parent != 0 {
			continue
		}

		if err := exactName(ent); err != nil {
			return nil, err
		}

		name, isTable := DecodeStreamName(ent.Name)

		// The encoding is not canonical: an encoded and a literal name can
		// decode alike, and the maps below would keep only one payload.
		decoded := decodedName{name: name, table: isTable}
		if first, dup := seenNames[decoded]; dup {
			return nil, fmt.Errorf("msidb: streams %q and %q both decode to %q", first, ent.Name, name)
		}

		seenNames[decoded] = ent.Name

		switch {
		case isTable:
			data, err := streamData(file, ent.Name)
			if err != nil {
				return nil, err
			}

			tableStreams[name] = data
		case loadAll || name == summary.StreamName:
			data, err := streamData(file, ent.Name)
			if err != nil {
				return nil, err
			}

			otherStreams[name] = data
			order = append(order, name)
		default:
			sizes[name] = ent.Size
			stored[name] = ent.Name
			order = append(order, name)
		}
	}

	// handed lists the binary streams a cell already took, so that rows
	// sharing a primary key do not each load a copy of one stream.
	handed := make(map[string]bool)

	// binaryStream hands a table cell its stream, loading it on demand, and
	// takes it out of the extra streams. Each stream goes to one cell only.
	binaryStream := func(name string) ([]byte, error) {
		if handed[name] {
			return nil, errBinaryShared
		}

		if data, ok := otherStreams[name]; ok {
			delete(otherStreams, name)

			handed[name] = true

			return data, nil
		}

		encoded, ok := stored[name]
		if !ok {
			return nil, errBinaryMissing
		}

		// The stored name, not a re-encoding: the encoding is not canonical.
		data, err := streamData(file, encoded)
		if err != nil {
			return nil, errBinaryMissing
		}

		delete(sizes, name)
		delete(stored, name)

		handed[name] = true

		return data, nil
	}

	poolStream, ok := tableStreams[TableStringPool]
	if !ok {
		return nil, errors.New("msidb: no _StringPool stream")
	}

	dataStream := tableStreams[TableStringData]

	if len(poolStream) < 4 {
		return nil, errors.New("msidb: _StringPool is truncated")
	}

	endian := binary.LittleEndian
	hdr := endian.Uint32(poolStream)
	database := &Database{Codepage: int(hdr & 0x7FFFFFFF), Storages: storages}
	longRefs := hdr&0x80000000 != 0

	if !codepage.Supported(database.Codepage) {
		return nil, fmt.Errorf("msidb: database code page %d is not supported", database.Codepage)
	}

	entries := []poolEntry{{}} // ID 0 is NULL
	pos := 0

	for i := 4; i+4 <= len(poolStream); {
		l := int(endian.Uint16(poolStream[i:]))
		r := int(endian.Uint16(poolStream[i+2:]))
		ent := poolEntry{length: l, refcount: r}
		i += 4

		if l == 0 && r != 0 {
			if i+4 > len(poolStream) {
				return nil, errors.New("msidb: truncated long string pool entry")
			}

			ent.long = true

			// Compare before converting: on a 32-bit build a length of
			// 2^31 or more turns negative as an int.
			length := endian.Uint32(poolStream[i:])
			if uint64(length) > uint64(len(dataStream)-pos) {
				return nil, fmt.Errorf("msidb: string pool entry %d overruns _StringData", len(entries))
			}

			ent.length = int(length)
			i += 4
		}

		if pos+ent.length > len(dataStream) {
			return nil, fmt.Errorf("msidb: string pool entry %d overruns _StringData", len(entries))
		}

		if ent.length > 0 {
			s, err := codepage.Decode(dataStream[pos:pos+ent.length], database.Codepage)
			if err != nil {
				return nil, fmt.Errorf("msidb: string %d: %w", len(entries), err)
			}

			ent.text = s
		}

		pos += ent.length
		entries = append(entries, ent)
	}

	if pos != len(dataStream) {
		return nil, fmt.Errorf("msidb: string lengths sum to %d but _StringData has %d bytes", pos, len(dataStream))
	}

	if (len(entries)-1 >= longRefThresh) != longRefs {
		addProblem(database, fmt.Sprintf("long-reference flag is %v with highest string ID %d", longRefs, len(entries)-1))
	}

	str := func(ref uint32) (string, error) {
		if ref == 0 {
			return "", nil
		}

		if int(ref) >= len(entries) || entries[ref].length == 0 {
			return "", fmt.Errorf("msidb: string ID %d is not in the pool", ref)
		}

		return entries[ref].text, nil
	}

	refWidth := 2
	if longRefs {
		refWidth = 3
	}

	readRef := func(b []byte) uint32 {
		v := uint32(endian.Uint16(b))
		if refWidth == 3 {
			v |= uint32(b[2]) << 16
		}

		return v
	}

	refs := make(map[uint32]int)

	// _Tables.
	tablesData := tableStreams[TableTables]
	if len(tablesData)%refWidth != 0 {
		return nil, errors.New("msidb: _Tables stream has a bad length")
	}

	tables := make(map[string]*Table)

	var tableOrder []string

	for i := 0; i+refWidth <= len(tablesData); i += refWidth {
		id := readRef(tablesData[i:])
		refs[id]++

		name, err := str(id)
		if err != nil {
			return nil, fmt.Errorf("msidb: _Tables: %w", err)
		}

		if _, dup := tables[name]; dup || name == "" {
			return nil, fmt.Errorf("msidb: _Tables lists %q twice or lists NULL", name)
		}

		tables[name] = &Table{Name: name}
		tableOrder = append(tableOrder, name)
	}

	// _Columns: column-major with 4 columns: Table (ref), Number (2), Name (ref), Type (2).
	colsData := tableStreams[TableColumns]
	rowSize := 2*refWidth + 4

	if len(colsData)%rowSize != 0 {
		return nil, errors.New("msidb: _Columns stream has a bad length")
	}

	count := len(colsData) / rowSize

	type colRow struct {
		table  string
		column Column
		number int
	}

	colRows := make([]colRow, count)

	for rowIdx := range count {
		tid := readRef(colsData[rowIdx*refWidth:])
		num := int(endian.Uint16(colsData[count*refWidth+rowIdx*2:])) - 0x8000
		nid := readRef(colsData[count*(refWidth+2)+rowIdx*refWidth:])
		typ := endian.Uint16(colsData[count*(2*refWidth+2)+rowIdx*2:]) - 0x8000
		refs[tid]++
		refs[nid]++

		tname, err := str(tid)
		if err != nil {
			return nil, fmt.Errorf("msidb: _Columns: %w", err)
		}

		cname, err := str(nid)
		if err != nil {
			return nil, fmt.Errorf("msidb: _Columns: %w", err)
		}

		t, err := TypeFromWord(typ)
		if err != nil {
			return nil, fmt.Errorf("msidb: _Columns: table %s column %s: %w", tname, cname, err)
		}

		colRows[rowIdx] = colRow{table: tname, column: Column{Name: cname, Type: t}, number: num}
	}

	slices.SortStableFunc(colRows, func(a, b colRow) int {
		if a.table != b.table {
			return strings.Compare(a.table, b.table)
		}

		return a.number - b.number
	})

	for _, col := range colRows {
		tbl, ok := tables[col.table]
		if !ok {
			return nil, fmt.Errorf("msidb: _Columns describes %s, which is not in _Tables", col.table)
		}

		if col.number != len(tbl.Columns)+1 {
			return nil, fmt.Errorf("msidb: table %s: column numbers are not contiguous at %d", col.table, col.number)
		}

		tbl.Columns = append(tbl.Columns, col.column)
	}

	// A table stream that _Tables does not list would be dropped without a
	// trace on the next write (MSISPEC section 3, lossless serialization).
	for name := range tableStreams {
		switch name {
		case TableStringPool, TableStringData, TableTables, TableColumns:
			continue
		}

		if _, listed := tables[name]; !listed {
			return nil, fmt.Errorf("msidb: table stream %s is not in _Tables", name)
		}
	}

	for _, name := range tableOrder {
		tbl := tables[name]
		if len(tbl.Columns) == 0 {
			return nil, fmt.Errorf("msidb: table %s has no columns", name)
		}

		if err := decodeRows(tbl, tableStreams[name], longRefs, readRef, str, refs, binaryStream, database); err != nil {
			return nil, err
		}

		database.Tables = append(database.Tables, tbl)
	}

	for strID, ent := range entries {
		if strID == 0 || ent.length == 0 {
			continue
		}

		if refs[uint32(strID)] != ent.refcount {
			addProblem(database, fmt.Sprintf("string %q has refcount %d but %d references", ent.text, ent.refcount, refs[uint32(strID)]))
		}
	}

	for _, name := range order {
		if name == summary.StreamName {
			props, err := summary.Unmarshal(otherStreams[name])
			if err != nil {
				return nil, fmt.Errorf("msidb: %w", err)
			}

			database.Summary = props

			continue
		}

		if _, used := otherStreams[name]; used {
			database.Streams = append(database.Streams, cfb.Stream{Name: name, Data: otherStreams[name]})
		}

		if size, left := sizes[name]; left {
			database.Sources = append(database.Sources, cfb.Source{Name: name, Size: int64(size)}) //nolint:gosec // checked against the file size
		}
	}

	return database, nil
}

func decodeRows( //nolint:cyclop,revive,gocognit // one pass over the format
	tbl *Table, data []byte, longRefs bool, readRef func([]byte) uint32, str func(uint32) (string, error),
	refs map[uint32]int, binaryStream func(string) ([]byte, error), database *Database,
) error {
	rowSize := 0
	for _, c := range tbl.Columns {
		rowSize += c.Type.storageWidth(longRefs)
	}

	if len(data) == 0 {
		return nil
	}

	if len(data)%rowSize != 0 {
		return fmt.Errorf("msidb: table %s: stream of %d bytes is not a multiple of the row size %d", tbl.Name, len(data), rowSize)
	}

	order := binary.LittleEndian
	count := len(data) / rowSize
	keys := tbl.Keys()
	stored := make([][]uint32, count)
	rows := make([][]any, count)

	for r := range rows {
		rows[r] = make([]any, len(tbl.Columns))
		stored[r] = make([]uint32, len(tbl.Columns))
	}

	off := 0

	for colIdx, col := range tbl.Columns {
		width := col.Type.storageWidth(longRefs)

		for rowIdx := range count {
			cell := data[off+rowIdx*width:]

			var value uint32

			switch width {
			case 2:
				value = uint32(order.Uint16(cell))
			case 3:
				value = readRef(cell)
			case 4:
				value = order.Uint32(cell)
			}

			stored[rowIdx][colIdx] = value

			if value == 0 {
				if !col.Type.Nullable {
					addProblem(database, fmt.Sprintf("table %s row %d: NULL in non-nullable column %s", tbl.Name, rowIdx, col.Name))
				}

				continue
			}

			switch col.Type.Kind { //nolint:exhaustive // the default covers the remaining kinds
			case KindString:
				refs[value]++

				s, err := str(value)
				if err != nil {
					return fmt.Errorf("msidb: table %s column %s: %w", tbl.Name, col.Name, err)
				}

				rows[rowIdx][colIdx] = s
			case KindInt16:
				rows[rowIdx][colIdx] = int(value) - 0x8000
			case KindInt32:
				rows[rowIdx][colIdx] = int(int32(value ^ 0x80000000)) //nolint:gosec // the stored form is the biased two's complement
			}
		}

		off += count * width
	}

	// Binary cells and the stored-key order.
	for colIdx, col := range tbl.Columns {
		if col.Type.Kind != KindBinary {
			continue
		}

		for rowIdx := range rows {
			if stored[rowIdx][colIdx] == 0 {
				continue
			}

			name := BinaryStreamName(tbl.Name, rows[rowIdx][:keys])

			// A name no stream can have is not looked up, and the message
			// leaves it out: key strings can be long.
			if _, err := EncodeStreamName(name, false); err != nil {
				addProblem(database, fmt.Sprintf("table %s row %d: the binary stream name of its key cannot be a stream name", tbl.Name, rowIdx))

				continue
			}

			data, err := binaryStream(name)
			if err != nil {
				addProblem(database, fmt.Sprintf("table %s row %d: binary stream %q %v", tbl.Name, rowIdx, name, err))

				continue
			}

			rows[rowIdx][colIdx] = append([]byte(nil), data...)
		}
	}

	for rowIdx := 1; rowIdx < count; rowIdx++ {
		cmp := 0

		for k := 0; k < keys && cmp == 0; k++ {
			switch {
			case stored[rowIdx-1][k] < stored[rowIdx][k]:
				cmp = -1
			case stored[rowIdx-1][k] > stored[rowIdx][k]:
				cmp = 1
			}
		}

		if cmp > 0 {
			addProblem(database, fmt.Sprintf("table %s: rows %d and %d are not in key order", tbl.Name, rowIdx-1, rowIdx))
		} else if cmp == 0 {
			addProblem(database, fmt.Sprintf("table %s: rows %d and %d share a primary key", tbl.Name, rowIdx-1, rowIdx))
		}
	}

	tbl.Rows = rows

	return nil
}
