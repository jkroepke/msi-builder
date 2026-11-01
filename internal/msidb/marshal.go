package msidb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/codepage"
	"github.com/jkroepke/msi-builder/internal/summary"
)

// System table and stream names.
const (
	TableTables     = "_Tables"
	TableColumns    = "_Columns"
	TableStringPool = "_StringPool"
	TableStringData = "_StringData"
	TableValidation = "_Validation"
)

// Options control Marshal.
type Options struct {
	// CFBVersion is 3 or 4; 0 means 4.
	CFBVersion int
}

// pool assigns string IDs and counts references.
type pool struct {
	bytes    map[string][]byte
	ids      map[string]uint32
	refs     map[string]int
	ordered  []string
	cp       int
	longRefs bool
}

func newPool(cp int) *pool {
	return &pool{cp: cp, bytes: make(map[string][]byte), ids: make(map[string]uint32), refs: make(map[string]int)}
}

// use records one reference to s.
func (p *pool) use(str string) error {
	if _, ok := p.bytes[str]; !ok {
		encoded, err := codepage.Encode(str, p.cp)
		if err != nil {
			return fmt.Errorf("msidb: %w", err)
		}

		if len(encoded) == 0 {
			return errors.New("msidb: empty string in the pool")
		}

		p.bytes[str] = encoded
	}

	p.refs[str]++

	return nil
}

// assign numbers the strings by their encoded bytes, from 1.
func (p *pool) assign() error {
	p.ordered = p.ordered[:0]
	for s := range p.bytes {
		p.ordered = append(p.ordered, s)
	}

	slices.SortFunc(p.ordered, func(a, b string) int { return bytes.Compare(p.bytes[a], p.bytes[b]) })

	for i, s := range p.ordered {
		p.ids[s] = uint32(i + 1)

		if p.refs[s] > MaxRefcount {
			return fmt.Errorf("msidb: string %.40q is referenced %d times; msi.dll keeps only 15 bits of a refcount (MSIDB-05), the limit is %d",
				s, p.refs[s], MaxRefcount)
		}
	}

	p.longRefs = len(p.ordered) >= longRefThresh

	return nil
}

func (p *pool) id(s string) uint32 { return p.ids[s] }

func (p *pool) marshal() ([]byte, []byte) {
	var poolStream, dataStream []byte

	order := binary.LittleEndian

	hdr := uint32(p.cp) //nolint:gosec // bounded by the format limits checked earlier
	if p.longRefs {
		hdr |= 0x80000000
	}

	poolStream = order.AppendUint32(poolStream, hdr)

	for _, s := range p.ordered {
		encoded := p.bytes[s]
		ref := uint16(p.refs[s]) //nolint:gosec // bounded by the format limits checked earlier

		if len(encoded) > 0xFFFF {
			poolStream = order.AppendUint16(poolStream, 0)
			poolStream = order.AppendUint16(poolStream, ref)
			poolStream = order.AppendUint32(poolStream, uint32(len(encoded))) //nolint:gosec // bounded by the format limits checked earlier
		} else {
			poolStream = order.AppendUint16(poolStream, uint16(len(encoded))) //nolint:gosec // bounded by the format limits checked earlier
			poolStream = order.AppendUint16(poolStream, ref)
		}

		dataStream = append(dataStream, encoded...)
	}

	return poolStream, dataStream
}

// stored returns the raw stored value of a normalized cell.
func (p *pool) stored(c Column, value any) uint32 {
	if value == nil {
		return 0
	}

	switch c.Type.Kind { //nolint:exhaustive // the default covers the remaining kinds
	case KindString:
		return p.id(value.(string)) //nolint:forcetypeassert // normalizeCell checked the type
	case KindInt16:
		return uint32(value.(int)+0x8000) & 0xFFFF //nolint:forcetypeassert,gosec // normalizeCell checked the type and range
	case KindInt32:
		return uint32(int32(value.(int))) ^ 0x80000000 //nolint:forcetypeassert,gosec // normalizeCell checked the type and range
	default:
		return 1
	}
}

// Marshal serializes the database into a compound file.
func (db *Database) Marshal(opts Options) ([]byte, error) {
	var buf bytes.Buffer

	if _, err := db.MarshalTo(&buf, opts); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// checkCodepage refuses a database code page the string pool cannot use.
func checkCodepage(codePage int) error {
	if codePage == 1200 || codePage == 1201 {
		return errors.New("msidb: UTF-16 database code pages are not valid")
	}

	if !codepage.Supported(codePage) {
		return fmt.Errorf("msidb: database code page %d is not supported", codePage)
	}

	return nil
}

// MarshalTo serializes the database into a compound file written to dst in one
// pass. Tables and Streams are encoded in memory; Sources are copied through
// from their readers, so a cabinet of any size needs no memory beyond a few
// sectors. The layout says where each stream landed, by encoded name.
func (db *Database) MarshalTo(dst io.Writer, opts Options) (*cfb.Layout, error) { //nolint:maintidx,cyclop,gocyclo,gocognit // one pass over the format
	if err := checkCodepage(db.Codepage); err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	tables := make([]*Table, 0, len(db.Tables))

	// Encode normalized copies so the caller's rows keep the values it
	// stored; the encoders below rely on the canonical cell types.
	for _, tbl := range db.Tables {
		normalized, err := tbl.normalized()
		if err != nil {
			return nil, err
		}

		tables = append(tables, normalized)

		if seen[tbl.Name] {
			return nil, fmt.Errorf("msidb: table %s defined twice", tbl.Name)
		}

		seen[tbl.Name] = true

		switch tbl.Name {
		case TableTables, TableColumns, TableStringPool, TableStringData, "_Streams", "_Storages":
			return nil, fmt.Errorf("msidb: table %s is a system table and cannot be defined", tbl.Name)
		}
	}

	slices.SortFunc(tables, func(a, b *Table) int { return strings.Compare(a.Name, b.Name) })

	strPool := newPool(db.Codepage)

	for _, tbl := range tables {
		// _Tables row and the Table column of every _Columns row.
		if err := strPool.use(tbl.Name); err != nil {
			return nil, fmt.Errorf("msidb: table name %q: %w", tbl.Name, err)
		}

		for _, col := range tbl.Columns {
			if err := strPool.use(tbl.Name); err != nil {
				return nil, err
			}

			if err := strPool.use(col.Name); err != nil {
				return nil, fmt.Errorf("msidb: column name %q: %w", col.Name, err)
			}
		}

		for _, row := range tbl.Rows {
			for i, c := range tbl.Columns {
				if s, ok := row[i].(string); ok && c.Type.Kind == KindString {
					if err := strPool.use(s); err != nil {
						return nil, fmt.Errorf("msidb: table %s column %s: %w", tbl.Name, c.Name, err)
					}
				}
			}
		}
	}

	if err := strPool.assign(); err != nil {
		return nil, err
	}

	var streams []cfb.Source

	addStream := func(decoded string, table bool, data []byte) error {
		name, err := EncodeStreamName(decoded, table)
		if err != nil {
			return err
		}

		streams = append(streams, cfb.Stream{Name: name, Data: data}.Source())

		return nil
	}

	poolStream, dataStream := strPool.marshal()

	if err := addStream(TableStringPool, true, poolStream); err != nil {
		return nil, err
	}

	if err := addStream(TableStringData, true, dataStream); err != nil {
		return nil, err
	}

	// _Tables: one string column, rows sorted by ID.
	tablesTable := &Table{Name: TableTables, Columns: []Column{Col("Name", "s64", true)}}
	for _, t := range tables {
		tablesTable.Add(t.Name)
	}

	// _Columns: Table, Number, Name, Type.
	columnsTable := &Table{Name: TableColumns, Columns: []Column{
		Col("Table", "s64", true), Col("Number", "i2", true), Col("Name", "s64", false), Col("Type", "i2", false),
	}}

	for _, t := range tables {
		for i, c := range t.Columns {
			columnsTable.Add(t.Name, i+1, c.Name, int(c.Type.Word()))
		}
	}

	for _, system := range []*Table{tablesTable, columnsTable} {
		tbl, err := system.normalized()
		if err != nil {
			return nil, err
		}

		data, err := strPool.encodeTable(tbl)
		if err != nil {
			return nil, err
		}

		if err := addStream(tbl.Name, true, data); err != nil {
			return nil, err
		}
	}

	streamNames := make(map[string]bool)

	for _, tbl := range tables {
		if len(tbl.Rows) == 0 {
			continue
		}

		data, err := strPool.encodeTable(tbl)
		if err != nil {
			return nil, err
		}

		if err := addStream(tbl.Name, true, data); err != nil {
			return nil, err
		}

		// Binary cells.
		keys := tbl.Keys()

		for colIdx, col := range tbl.Columns {
			if col.Type.Kind != KindBinary {
				continue
			}

			for _, row := range tbl.Rows {
				if row[colIdx] == nil {
					continue
				}

				name := BinaryStreamName(tbl.Name, row[:keys])
				if streamNames[name] {
					return nil, fmt.Errorf("msidb: binary stream %q written twice", name)
				}

				streamNames[name] = true

				data, ok := row[colIdx].([]byte)
				if !ok {
					return nil, fmt.Errorf("msidb: table %s: binary cell holds %T", tbl.Name, row[colIdx])
				}

				if err := addStream(name, false, data); err != nil {
					return nil, fmt.Errorf("msidb: table %s: %w", tbl.Name, err)
				}
			}
		}
	}

	// Database.Summary is the only way to write the summary stream: one in
	// Streams or Sources as well would be stored under a second name.
	streamNames[summary.StreamName] = true

	for _, stream := range db.Streams {
		if streamNames[stream.Name] {
			return nil, fmt.Errorf("msidb: stream %q written twice", stream.Name)
		}

		streamNames[stream.Name] = true

		if err := addStream(stream.Name, false, stream.Data); err != nil {
			return nil, err
		}
	}

	for _, src := range db.Sources {
		if streamNames[src.Name] {
			return nil, fmt.Errorf("msidb: stream %q written twice", src.Name)
		}

		streamNames[src.Name] = true

		name, err := EncodeStreamName(src.Name, false)
		if err != nil {
			return nil, err
		}

		streams = append(streams, cfb.Source{Name: name, Size: src.Size, Open: src.Open})
	}

	if db.Summary != nil {
		data, err := db.MarshalSummary()
		if err != nil {
			return nil, err
		}

		streams = append(streams, cfb.Stream{Name: summary.StreamName, Data: data}.Source())
	}

	layout, err := cfb.WriteStoragesTo(dst, streams, db.Storages, cfb.Options{Version: opts.CFBVersion, CLSID: cfb.CLSIDMSI})
	if err != nil {
		return nil, fmt.Errorf("msidb: %w", err)
	}

	return layout, nil
}

// MarshalSummary encodes the summary information stream the way MarshalTo
// writes it, in the summary's own code page or the database's ANSI code page.
func (db *Database) MarshalSummary() ([]byte, error) {
	codePage := db.Codepage
	if !codepage.IsANSI(codePage) {
		codePage = codepage.Windows1252
	}

	if v, ok := summary.Get(db.Summary, summary.PIDCodepage).(int); ok {
		codePage = v
	}

	data, err := summary.Marshal(db.Summary, codePage)
	if err != nil {
		return nil, fmt.Errorf("msidb: %w", err)
	}

	return data, nil
}

// encodeTable sorts the rows by their stored keys and writes the column-major
// stream.
func (p *pool) encodeTable(tbl *Table) ([]byte, error) { //nolint:cyclop // one pass over the format; splitting would scatter it
	keys := tbl.Keys()

	type sortedRow struct {
		stored []uint32
	}

	rows := make([]sortedRow, len(tbl.Rows))

	for r, row := range tbl.Rows {
		rows[r].stored = make([]uint32, len(tbl.Columns))
		for i, c := range tbl.Columns {
			rows[r].stored[i] = p.stored(c, row[i])
		}
	}

	less := func(a, b []uint32) int {
		for i := range keys {
			if a[i] != b[i] {
				if a[i] < b[i] {
					return -1
				}

				return 1
			}
		}

		return 0
	}

	slices.SortStableFunc(rows, func(a, b sortedRow) int { return less(a.stored, b.stored) })

	for i := 1; i < len(rows); i++ {
		if less(rows[i-1].stored, rows[i].stored) == 0 {
			return nil, fmt.Errorf("msidb: table %s has two rows with the same primary key %v", tbl.Name, describeKeys(tbl, rows[i].stored[:keys], p))
		}
	}

	var out []byte

	for ci, c := range tbl.Columns {
		w := c.Type.storageWidth(p.longRefs)

		for _, r := range rows {
			value := r.stored[ci]

			switch w {
			case 2:
				out = append(out, byte(value), byte(value>>8)) //nolint:gosec // truncation to the column width is the encoding
			case 3:
				out = append(out, byte(value), byte(value>>8), byte(value>>16)) //nolint:gosec // truncation to the column width is the encoding
			case 4:
				out = append(out, byte(value), byte(value>>8), byte(value>>16), byte(value>>24)) //nolint:gosec // truncation to the column width is the encoding
			}
		}
	}

	return out, nil
}

func describeKeys(t *Table, stored []uint32, strPool *pool) []any {
	var out []any

	for i, value := range stored {
		col := t.Columns[i]

		switch {
		case value == 0:
			out = append(out, nil)
		case col.Type.Kind == KindString:
			out = append(out, strPool.ordered[value-1])
		case col.Type.Kind == KindInt16:
			out = append(out, int(value)-0x8000)
		default:
			out = append(out, int(int32(value^0x80000000))) //nolint:gosec // the stored form is the biased two's complement
		}
	}

	return out
}
