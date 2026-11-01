package msidb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/summary"
)

// Record masks of a transform table stream (MSISPEC section 3, MSIDB-12).
const (
	maskInsert = 0x0001
	// maxMaskColumns is the widest table whose records use a 16-bit mask.
	maxMaskColumns = 16
)

// TransformOptions control MarshalTransform.
type TransformOptions struct {
	// Summary is the transform's summary information.
	Summary []summary.Property
	// CFBVersion is 3 or 4; 0 means 4.
	CFBVersion int
}

// transformRecord is one record of a transform table stream: the cells it
// carries, in column order, and its mask.
type transformRecord struct {
	cells   []any
	columns []int
	mask    uint16
}

// MarshalTransform writes the transform that turns base into variant: a
// compound file with the transform CLSID whose table streams insert, update
// and delete rows (MSISPEC section 3, "Text archives and transforms"). Both
// databases must have the same tables and columns and unique primary keys;
// schema changes and binary cells in changed or inserted rows are refused,
// since no transform that needs them has been observed yet.
func MarshalTransform(base, variant *Database, opts TransformOptions) ([]byte, error) {
	streams, err := transformAll(base, variant, opts)
	if err != nil {
		return nil, err
	}

	out, err := cfb.Write(streams, cfb.Options{Version: opts.CFBVersion, CLSID: cfb.CLSIDTransform})
	if err != nil {
		return nil, fmt.Errorf("msidb: transform: %w", err)
	}

	return out, nil
}

// TransformStorage returns the transform that turns base into variant as a
// storage named name, to embed in the base package: a language transform
// is named by its decimal LCID (MSISPEC section 7.6).
func TransformStorage(name string, base, variant *Database, opts TransformOptions) (cfb.Storage, error) {
	streams, err := transformAll(base, variant, opts)
	if err != nil {
		return cfb.Storage{}, err
	}

	storage := cfb.Storage{Name: name, CLSID: cfb.CLSIDTransform}
	for _, s := range streams {
		storage.Streams = append(storage.Streams, s.Source())
	}

	return storage, nil
}

// transformAll returns the streams of the transform from base to variant.
func transformAll(base, variant *Database, opts TransformOptions) ([]cfb.Stream, error) {
	if err := transformCodepages(base.Codepage, variant.Codepage); err != nil {
		return nil, err
	}

	baseTables, variantTables, err := transformTables(base, variant)
	if err != nil {
		return nil, err
	}

	changes, names, err := diffTables(baseTables, variantTables)
	if err != nil {
		return nil, err
	}

	strPool, err := transformPool(variant.Codepage, variantTables, changes, names)
	if err != nil {
		return nil, err
	}

	streams, err := transformStreams(strPool, variantTables, changes, names)
	if err != nil {
		return nil, err
	}

	if opts.Summary != nil {
		data, err := summary.Marshal(opts.Summary, summaryCodepage(opts.Summary, variant.Codepage))
		if err != nil {
			return nil, fmt.Errorf("msidb: transform: %w", err)
		}

		streams = append(streams, cfb.Stream{Name: summary.StreamName, Data: data})
	}

	return streams, nil
}

// transformCodepages checks both code pages. It runs before any string is
// encoded: a transform that changes integers alone still writes the code
// page in its pool header.
func transformCodepages(base, variant int) error {
	for _, codePage := range []int{base, variant} {
		if err := checkCodepage(codePage); err != nil {
			return fmt.Errorf("msidb: transform: %w", err)
		}
	}

	// msi.dll applies a code page change to a neutral base only (MSIDB-12).
	if base != 0 && base != variant {
		return fmt.Errorf("msidb: transform: code page %d differs from the base's %d; only a neutral base can change it", variant, base)
	}

	return nil
}

// transformStreams encodes the string pool and the changed tables.
func transformStreams(strPool *pool, tables map[string]*Table, changes map[string][]transformRecord, names []string) ([]cfb.Stream, error) {
	streams := make([]cfb.Stream, 0, len(names)+3)
	poolStream, dataStream := strPool.marshal()

	for _, poolPart := range []struct {
		name string
		data []byte
	}{{TableStringPool, poolStream}, {TableStringData, dataStream}} {
		encoded, err := EncodeStreamName(poolPart.name, true)
		if err != nil {
			return nil, err
		}

		streams = append(streams, cfb.Stream{Name: encoded, Data: poolPart.data})
	}

	for _, name := range names {
		encoded, err := EncodeStreamName(name, true)
		if err != nil {
			return nil, err
		}

		streams = append(streams, cfb.Stream{Name: encoded, Data: strPool.encodeRecords(tables[name], changes[name])})
	}

	return streams, nil
}

// diffTables returns the records per changed table and the changed tables
// with their names, sorted.
func diffTables(base, variant map[string]*Table) (map[string][]transformRecord, []string, error) {
	changes := make(map[string][]transformRecord)
	names := make([]string, 0, len(variant))

	for name, tbl := range variant {
		records, err := diffTable(base[name], tbl)
		if err != nil {
			return nil, nil, err
		}

		if len(records) > 0 {
			changes[name] = records
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return changes, names, nil
}

// transformPool counts the strings the records carry and numbers them.
func transformPool(codepage int, tables map[string]*Table, changes map[string][]transformRecord, names []string) (*pool, error) {
	strPool := newPool(codepage)

	for _, name := range names {
		tbl := tables[name]

		for _, rec := range changes[name] {
			for i, ci := range rec.columns {
				if str, ok := rec.cells[i].(string); ok && tbl.Columns[ci].Type.Kind == KindString {
					if err := strPool.use(str); err != nil {
						return nil, fmt.Errorf("msidb: transform: table %s: %w", name, err)
					}
				}
			}
		}
	}

	if err := strPool.assign(); err != nil {
		return nil, err
	}

	return strPool, nil
}

// summaryCodepage is the code page of the summary stream: its own PID 1,
// or the database's ANSI code page.
func summaryCodepage(props []summary.Property, dbCodepage int) int {
	if v, ok := summary.Get(props, summary.PIDCodepage).(int); ok {
		return v
	}

	if dbCodepage == 65001 || dbCodepage == 0 {
		return 1252
	}

	return dbCodepage
}

// transformTables normalizes both databases' tables and checks that they
// have the same schema.
func transformTables(base, variant *Database) (map[string]*Table, map[string]*Table, error) {
	baseTables, err := normalizedTables(base)
	if err != nil {
		return nil, nil, err
	}

	variantTables, err := normalizedTables(variant)
	if err != nil {
		return nil, nil, err
	}

	if err := sameSchema(baseTables, variantTables); err != nil {
		return nil, nil, err
	}

	return baseTables, variantTables, nil
}

// normalizedTables returns the database's tables by name, normalized.
func normalizedTables(db *Database) (map[string]*Table, error) {
	out := make(map[string]*Table, len(db.Tables))

	for _, tbl := range db.Tables {
		norm, err := tbl.normalized()
		if err != nil {
			return nil, err
		}

		if _, dup := out[norm.Name]; dup {
			return nil, fmt.Errorf("msidb: table %s defined twice", norm.Name)
		}

		out[norm.Name] = norm
	}

	return out, nil
}

// sameSchema checks that both databases have the same tables and columns.
func sameSchema(base, variant map[string]*Table) error {
	for name, tbl := range variant {
		other, ok := base[name]
		if !ok {
			return fmt.Errorf("msidb: transform: table %s is not in the base; transforms that add tables are not supported", name)
		}

		if !slices.Equal(tbl.Columns, other.Columns) {
			return fmt.Errorf("msidb: transform: table %s has other columns than in the base; transforms that change columns are not supported", name)
		}

		if len(tbl.Columns) > maxMaskColumns {
			return fmt.Errorf("msidb: transform: table %s has %d columns; transforms of tables wider than %d are not supported", name, len(tbl.Columns), maxMaskColumns)
		}
	}

	for name := range base {
		if _, ok := variant[name]; !ok {
			return fmt.Errorf("msidb: transform: table %s is not in the variant; transforms that drop tables are not supported", name)
		}
	}

	return nil
}

// rowKey is a comparable form of a normalized row's key cells.
func rowKey(tbl *Table, row []any) string {
	var key strings.Builder

	// Strings are quoted, so a key cell holding a NUL cannot imitate the
	// separator and make two different keys equal.
	for i := range tbl.Keys() {
		if str, ok := row[i].(string); ok {
			fmt.Fprintf(&key, "s%q\x00", str)

			continue
		}

		fmt.Fprintf(&key, "%T:%v\x00", row[i], row[i])
	}

	return key.String()
}

// diffTable returns the records that turn base into variant.
func diffTable(base, variant *Table) ([]transformRecord, error) { //nolint:cyclop // one pass over both tables
	keys := variant.Keys()
	all := make([]int, len(variant.Columns))

	for i := range all {
		all[i] = i
	}

	baseRows := make(map[string][]any, len(base.Rows))

	// A key held by two rows makes the diff ambiguous; Marshal refuses
	// such a table too.
	for _, row := range base.Rows {
		key := rowKey(base, row)
		if _, dup := baseRows[key]; dup {
			return nil, fmt.Errorf("msidb: transform: base table %s has two rows with the same primary key %v", base.Name, row[:keys])
		}

		baseRows[key] = row
	}

	variantKeys := make(map[string]bool, len(variant.Rows))

	var out []transformRecord

	for _, row := range variant.Rows {
		key := rowKey(variant, row)
		if variantKeys[key] {
			return nil, fmt.Errorf("msidb: transform: table %s has two rows with the same primary key %v", variant.Name, row[:keys])
		}

		variantKeys[key] = true

		old, ok := baseRows[key]
		if !ok {
			if err := insertedBinary(variant, row); err != nil {
				return nil, err
			}

			out = append(out, transformRecord{cells: row, columns: all, mask: maskInsert | uint16(len(row))<<8}) //nolint:gosec // at most 16 columns

			continue
		}

		columns := slices.Clone(all[:keys])
		cells := slices.Clone(row[:keys])

		var mask uint16

		for i := keys; i < len(row); i++ {
			if cellEqual(old[i], row[i]) {
				continue
			}

			if variant.Columns[i].Type.Kind == KindBinary {
				return nil, fmt.Errorf("msidb: transform: table %s: changed binary column %s is not supported", variant.Name, variant.Columns[i].Name)
			}

			mask |= 1 << i
			columns = append(columns, i)
			cells = append(cells, row[i])
		}

		if mask != 0 {
			out = append(out, transformRecord{cells: cells, columns: columns, mask: mask})
		}
	}

	for _, row := range base.Rows {
		if !variantKeys[rowKey(base, row)] {
			out = append(out, transformRecord{cells: slices.Clone(row[:keys]), columns: all[:keys]})
		}
	}

	return out, nil
}

// insertedBinary refuses a new row with a non-null binary cell: the cell
// needs its own stream in the transform, which this writer does not emit
// (MSISPEC section 3).
func insertedBinary(tbl *Table, row []any) error {
	for i, col := range tbl.Columns {
		if col.Type.Kind == KindBinary && row[i] != nil {
			return fmt.Errorf("msidb: transform: table %s: inserted row %v sets binary column %s; binary cells are not supported",
				tbl.Name, row[:tbl.Keys()], col.Name)
		}
	}

	return nil
}

func cellEqual(old, cur any) bool {
	if oldBytes, ok := old.([]byte); ok {
		curBytes, ok := cur.([]byte)

		return ok && bytes.Equal(oldBytes, curBytes)
	}

	return old == cur
}

// encodeRecords writes the records of one table, row by row, sorted by
// their stored keys.
func (p *pool) encodeRecords(tbl *Table, records []transformRecord) []byte {
	keys := tbl.Keys()

	stored := func(rec transformRecord) []uint32 {
		out := make([]uint32, len(rec.cells))
		for i, ci := range rec.columns {
			out[i] = p.stored(tbl.Columns[ci], rec.cells[i])
		}

		return out
	}

	type encoded struct {
		values []uint32
		rec    transformRecord
	}

	rows := make([]encoded, len(records))
	for i, rec := range records {
		rows[i] = encoded{values: stored(rec), rec: rec}
	}

	slices.SortStableFunc(rows, func(a, b encoded) int { return slices.Compare(a.values[:keys], b.values[:keys]) })

	var out []byte

	for _, r := range rows {
		out = binary.LittleEndian.AppendUint16(out, r.rec.mask)

		for i, ci := range r.rec.columns {
			value := r.values[i]

			switch tbl.Columns[ci].Type.storageWidth(p.longRefs) {
			case 2:
				out = append(out, byte(value), byte(value>>8)) //nolint:gosec // truncation to the column width is the encoding
			case 3:
				out = append(out, byte(value), byte(value>>8), byte(value>>16)) //nolint:gosec // truncation to the column width is the encoding
			case 4:
				out = append(out, byte(value), byte(value>>8), byte(value>>16), byte(value>>24)) //nolint:gosec // truncation to the column width is the encoding
			}
		}
	}

	return out
}
