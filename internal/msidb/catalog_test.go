package msidb //nolint:testpackage // the tests patch the encoded catalog

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cfb"
)

// catalogEdit changes one _Columns row of a written package: its name (to a
// string already in the pool) and its type word.
type catalogEdit struct {
	name string // "" keeps the name
	key  int    // 1 sets the key bit, -1 clears it, 0 keeps it
	typ  uint16 // a replacement type word; 0 keeps the type
}

// catalogBase is the table the malformed catalogs start from: a key K, a
// nullable string V and a nullable integer N, all valid.
func catalogBase() *Table {
	tbl := &Table{Name: "T", Columns: []Column{Col("K", "s16", true), Col("V", "S16", false), Col("N", "I2", false)}}
	tbl.Add("a", "x", 1)
	tbl.Add("b", "y", 2)

	return tbl
}

// patchedCatalog writes a database holding tbl and then edits the _Columns
// rows of tbl by column number, so that the catalog holds what Validate
// refuses. Renaming a column moves its _StringPool refcount to the new name,
// so the string pool stays consistent and only the catalog is malformed.
func patchedCatalog(t *testing.T, tbl *Table, edits map[int]catalogEdit) []byte {
	t.Helper()

	out, err := (&Database{Codepage: 1252, Tables: []*Table{tbl}}).Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	streams := file.Streams()
	byName := make(map[string]*cfb.Stream, len(streams))

	for i := range streams {
		name, table := DecodeStreamName(streams[i].Name)
		if table {
			byName[name] = &streams[i]
		}
	}

	pool, data := byName[TableStringPool].Data, byName[TableStringData].Data
	ids := make(map[string]uint16)

	for i, pos := 4, 0; i+4 <= len(pool); i += 4 {
		length := int(binary.LittleEndian.Uint16(pool[i:]))
		ids[string(data[pos:pos+length])] = uint16((i-4)/4 + 1)
		pos += length
	}

	cols := byName[TableColumns].Data
	count := len(cols) / 8 // 2-byte references: Table, Number, Name, Type

	for row := range count {
		if binary.LittleEndian.Uint16(cols[row*2:]) != ids[tbl.Name] {
			continue
		}

		edit, ok := edits[int(binary.LittleEndian.Uint16(cols[count*2+row*2:]))-0x8000]
		if !ok {
			continue
		}

		nameAt, typeAt := cols[count*4+row*2:], cols[count*6+row*2:]

		if edit.name != "" {
			old, renamed := binary.LittleEndian.Uint16(nameAt), ids[edit.name]
			if renamed == 0 {
				t.Fatalf("%q is not in the string pool", edit.name)
			}

			binary.LittleEndian.PutUint16(nameAt, renamed)
			addRefcount(pool, old, -1)
			addRefcount(pool, renamed, 1)
		}

		word := binary.LittleEndian.Uint16(typeAt) - 0x8000
		if edit.typ != 0 {
			word = edit.typ | word&0x2000
		}

		switch edit.key {
		case 1:
			word |= 0x2000
		case -1:
			word &^= 0x2000
		}

		binary.LittleEndian.PutUint16(typeAt, word+0x8000)
	}

	patched, err := cfb.Write(streams, cfb.Options{})
	if err != nil {
		t.Fatal(err)
	}

	return patched
}

func addRefcount(pool []byte, id uint16, delta int) {
	at := pool[4+int(id-1)*4+2:]
	binary.LittleEndian.PutUint16(at, uint16(int(binary.LittleEndian.Uint16(at))+delta)) //nolint:gosec // a small test pool
}

// The reader reports a catalog that Table.Validate refuses as a problem, with
// Validate's message, so that a table Marshal cannot write back never reads
// as sound (MSIDB-21).
func TestUnmarshalReportsMalformedCatalog(t *testing.T) {
	t.Parallel()

	withBinary := func() *Table {
		tbl := &Table{Name: "T", Columns: []Column{Col("K", "s16", true), Col("D", "V0", false), Col("E", "S16", false)}}
		tbl.Add("a", nil, nil)

		return tbl
	}

	for _, tc := range []struct {
		base  func() *Table
		edits map[int]catalogEdit
		name  string
		want  string
		keys  int
	}{
		{name: "duplicate key name", base: catalogBase, edits: map[int]catalogEdit{2: {name: "K"}}, want: "column K defined twice", keys: 1},
		{name: "duplicate non-key name", base: catalogBase, edits: map[int]catalogEdit{3: {name: "V"}}, want: "column V defined twice", keys: 1},
		{name: "no primary key", base: catalogBase, edits: map[int]catalogEdit{1: {key: -1}}, want: "table T has no primary key"},
		{name: "key after a non-key column", base: catalogBase, edits: map[int]catalogEdit{3: {key: 1}}, want: "key column N must come before the other columns", keys: 1},
		{name: "only key last", base: catalogBase, edits: map[int]catalogEdit{1: {key: -1}, 3: {key: 1}}, want: "key column N must come before the other columns"},
		{name: "binary key", base: withBinary, edits: map[int]catalogEdit{2: {key: 1}}, want: "binary column D cannot be a key", keys: 2},
		{name: "two binary columns", base: withBinary, edits: map[int]catalogEdit{3: {typ: 0x1900}}, want: "table T has 2 binary columns", keys: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := patchedCatalog(t, tc.base(), tc.edits)

			database, err := Unmarshal(raw)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.ContainsFunc(database.Problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("problems %q, want one containing %q", database.Problems, tc.want)
			}

			// msi.dll counts the key columns before the first non-key one
			// (MSIDB-21), and the rows read as written.
			tbl := database.Table("T")
			if tbl.Keys() != tc.keys {
				t.Errorf("Keys %d, want %d", tbl.Keys(), tc.keys)
			}

			if want := tc.base().Rows; fmt.Sprint(tbl.Rows) != fmt.Sprint(want) {
				t.Errorf("rows %v, want %v", tbl.Rows, want)
			}

			if _, err := database.Marshal(Options{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Marshal: %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// A binary stream is named by the row's key, so the reader cannot find the
// streams of a table without one. It reports that once and leaves the streams
// as extra streams instead of naming every row's stream "T".
func TestUnmarshalKeylessBinary(t *testing.T) {
	t.Parallel()

	tbl := &Table{Name: "T", Columns: []Column{Col("K", "s16", true), Col("D", "V0", false)}}
	tbl.Add("a", []byte("first"))
	tbl.Add("b", []byte("second"))

	database, err := Unmarshal(patchedCatalog(t, tbl, map[int]catalogEdit{1: {key: -1}}))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"table T has no primary key", "table T: the binary cells of column D are not read, the table has no primary key"}
	if !slices.Equal(database.Problems, want) {
		t.Errorf("problems %q, want %q", database.Problems, want)
	}

	if rows := database.Table("T").Rows; rows[0][1] != nil || rows[1][1] != nil {
		t.Errorf("rows %v", rows)
	}

	names := make([]string, 0, len(database.Streams))
	for _, stream := range database.Streams {
		names = append(names, stream.Name)
	}

	if !slices.Contains(names, "T.a") || !slices.Contains(names, "T.b") {
		t.Errorf("streams %q, want T.a and T.b kept", names)
	}
}

// The patch itself leaves a consistent database: without edits the package
// reads back without problems, so a refusal above comes from the catalog.
func TestPatchedCatalogControl(t *testing.T) {
	t.Parallel()

	database, err := Unmarshal(patchedCatalog(t, catalogBase(), nil))
	if err != nil {
		t.Fatal(err)
	}

	if len(database.Problems) != 0 {
		t.Errorf("problems: %q", database.Problems)
	}

	// A rename that keeps the catalog valid keeps the refcounts right too.
	database, err = Unmarshal(patchedCatalog(t, catalogBase(), map[int]catalogEdit{3: {name: "T"}}))
	if err != nil {
		t.Fatal(err)
	}

	if len(database.Problems) != 0 || database.Tables[0].Columns[2].Name != "T" {
		t.Errorf("columns %+v, problems: %q", database.Tables[0].Columns, database.Problems)
	}
}
