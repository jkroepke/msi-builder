package msidb //nolint:testpackage // the tests reach unexported encoders

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cfb"
)

func propertyDB(rows ...[]any) *Database {
	tbl := &Table{Name: "Property", Columns: []Column{Col("Property", "s72", true), Col("Value", "l0", false)}, Rows: rows}

	return &Database{Tables: []*Table{tbl}, Codepage: 1252}
}

// TestMarshalTransform checks the record layout MSIDB-12 observed in
// transforms that msi.dll writes: a 16-bit mask, insert with the column
// count in the high byte, update with one bit per present column, delete
// with the keys alone, rows in key order.
func TestMarshalTransform(t *testing.T) {
	t.Parallel()

	base := propertyDB([]any{"ProductName", "A"}, []any{"Gone", "x"}, []any{"Same", "s"})
	variant := propertyDB([]any{"ProductName", "B"}, []any{"New", "n"}, []any{"Same", "s"})

	out, err := MarshalTransform(base, variant, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	if file.CLSID != cfb.CLSIDTransform {
		t.Errorf("CLSID %x", file.CLSID)
	}

	stream := func(name string) string {
		encoded, err := EncodeStreamName(name, true)
		if err != nil {
			t.Fatal(err)
		}

		data, ok := file.Stream(encoded)
		if !ok {
			t.Fatalf("no stream %s", name)
		}

		return hex.EncodeToString(data)
	}

	// IDs by encoded bytes: B 1, Gone 2, New 3, ProductName 4, n 5.
	for name, want := range map[string]string{
		TableStringPool: "e4040000" + "01000100" + "04000100" + "03000100" + "0b000100" + "01000100",
		TableStringData: hex.EncodeToString([]byte("BGoneNewProductNamen")),
		"Property":      "0000" + "0200" + "0102" + "0300" + "0500" + "0200" + "0400" + "0100",
	} {
		if got := stream(name); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}

	if len(file.Streams()) != 3 {
		t.Errorf("%d streams, want the pool and Property only", len(file.Streams()))
	}
}

func TestMarshalTransformIntegers(t *testing.T) {
	t.Parallel()

	cols := []Column{Col("Key", "s72", true), Col("Num", "I2", false), Col("Big", "I4", false)}
	base := &Database{Codepage: 1252, Tables: []*Table{{Name: "T", Columns: cols, Rows: [][]any{{"b", 2, 200000}}}}}
	variant := &Database{Codepage: 1252, Tables: []*Table{{Name: "T", Columns: cols, Rows: [][]any{{"b", nil, -5}}}}}

	out, err := MarshalTransform(base, variant, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := EncodeStreamName("T", true)
	data, _ := file.Stream(encoded)

	// The bytes msi.dll wrote for the same change in MSIDB-12.
	if want, _ := hex.DecodeString("060001000000fbffff7f"); !bytes.Equal(data, want) {
		t.Errorf("T = %x, want %x", data, want)
	}
}

// intDB is a database whose only change between versions is an integer, so
// no string reaches the pool encoder.
func intDB(codePage, value int) *Database {
	cols := []Column{Col("Key", "i2", true), Col("Num", "i2", false)}

	return &Database{Codepage: codePage, Tables: []*Table{{Name: "T", Columns: cols, Rows: [][]any{{1, value}}}}}
}

func TestMarshalTransformRefuses(t *testing.T) {
	t.Parallel()

	wide := make([]Column, 17)
	for i := range wide {
		wide[i] = Col("C"+strings.Repeat("x", i+1), "s72", i == 0)
	}

	other := &Database{Codepage: 1252, Tables: []*Table{{Name: "Other", Columns: []Column{Col("K", "s72", true)}}}}

	withOther := func() *Database {
		return &Database{Codepage: 1252, Tables: append(propertyDB().Tables, other.Tables...)}
	}
	unicode := propertyDB()
	unicode.Codepage = 65001
	neutral := propertyDB()
	neutral.Codepage = 0
	wideDB := func() *Database { return &Database{Codepage: 1252, Tables: []*Table{{Name: "W", Columns: wide}}} }

	for name, pair := range map[string][2]*Database{
		"added table":   {propertyDB(), withOther()},
		"dropped table": {withOther(), propertyDB()},
		"code page":     {propertyDB(), unicode},
		"to neutral":    {unicode, neutral},
		"wide table":    {wideDB(), wideDB()},
		"unsupported":   {intDB(9999, 1), intDB(9999, 2)},
		"UTF-16":        {intDB(1200, 1), intDB(1200, 2)},
	} {
		if _, err := MarshalTransform(pair[0], pair[1], TransformOptions{}); err == nil {
			t.Errorf("%s: MarshalTransform succeeded", name)
		}
	}
}

// TestMarshalTransformNeutralBase checks that a neutral base may take the
// variant's code page, the one switch msi.dll applied in MSIDB-12.
func TestMarshalTransformNeutralBase(t *testing.T) {
	t.Parallel()

	base := propertyDB([]any{"ProductName", "A"})
	base.Codepage = 0
	variant := propertyDB([]any{"ProductName", "Пример"})
	variant.Codepage = 65001

	out, err := MarshalTransform(base, variant, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := EncodeStreamName(TableStringPool, true)
	if err != nil {
		t.Fatal(err)
	}

	data, ok := file.Stream(encoded)
	if !ok || !bytes.HasPrefix(data, []byte{0xe9, 0xfd, 0, 0}) {
		t.Errorf("pool header % x, want code page 65001", data[:min(4, len(data))])
	}
}

// TestMarshalTransformRefusesInsertedBinary checks that a new row with a
// binary cell is refused: the transform would mark the cell present but
// carry no stream for it.
func TestMarshalTransformRefusesInsertedBinary(t *testing.T) {
	t.Parallel()

	cols := []Column{Col("Name", "s72", true), Col("Data", "V0", false)} // nullable
	base := &Database{Codepage: 1252, Tables: []*Table{{Name: "Binary", Columns: cols, Rows: [][]any{{"old", []byte("o")}}}}}
	variant := &Database{Codepage: 1252, Tables: []*Table{{Name: "Binary", Columns: cols, Rows: [][]any{{"old", []byte("o")}, {"new", []byte("n")}}}}}

	if _, err := MarshalTransform(base, variant, TransformOptions{}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("inserted binary row: %v, want a refusal", err)
	}

	// A new row whose binary cell is null needs no stream.
	variant.Tables[0].Rows[1] = []any{"new", nil}

	if _, err := MarshalTransform(base, variant, TransformOptions{}); err != nil {
		t.Errorf("inserted row with a null binary cell: %v", err)
	}
}

// TestMarshalTransformRefusesDuplicateKeys checks that two rows with one
// primary key, in either database, fail instead of yielding a transform
// whose result depends on row order.
func TestMarshalTransformRefusesDuplicateKeys(t *testing.T) {
	t.Parallel()

	dup := func() *Database { return propertyDB([]any{"ProductName", "A"}, []any{"ProductName", "B"}) }
	plain := func() *Database { return propertyDB([]any{"ProductName", "A"}) }

	for name, pair := range map[string][2]*Database{
		"base":    {dup(), plain()},
		"variant": {plain(), dup()},
	} {
		if _, err := MarshalTransform(pair[0], pair[1], TransformOptions{}); err == nil || !strings.Contains(err.Error(), "same primary key") {
			t.Errorf("%s with a duplicate key: %v, want a refusal", name, err)
		}
	}
}

// TestMarshalTransformCompositeKeysWithNUL checks that composite keys whose
// strings hold NULs stay distinct: ("a", "b\x00string:c") and
// ("a\x00string:b", "c") once encoded to the same row key.
func TestMarshalTransformCompositeKeysWithNUL(t *testing.T) {
	t.Parallel()

	cols := []Column{Col("A", "s72", true), Col("B", "s72", true), Col("V", "s72", false)}
	base := &Database{Codepage: 1252, Tables: []*Table{{Name: "T", Columns: cols, Rows: [][]any{{"a", "b\x00string:c", "v"}}}}}
	variant := &Database{Codepage: 1252, Tables: []*Table{{Name: "T", Columns: cols, Rows: [][]any{{"a\x00string:b", "c", "v"}}}}}

	out, err := MarshalTransform(base, variant, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := EncodeStreamName("T", true)
	if _, ok := file.Stream(encoded); !ok {
		t.Error("the transform has no records for T, want a delete and an insert")
	}
}
