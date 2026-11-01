package msidb_test

import (
	"fmt"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/summary"
)

func seedDatabase(f *testing.F) {
	f.Helper()

	tbl := &msidb.Table{Name: "Property", Columns: []msidb.Column{
		msidb.Col("Property", "s72", true),
		msidb.Col("Value", "l0", false),
	}}
	tbl.Add("ProductName", "Hello")
	tbl.Add("ProductVersion", "1.0.0")

	nums := &msidb.Table{Name: "Nums", Columns: []msidb.Column{
		msidb.Col("Key", "i2", true),
		msidb.Col("Long", "I4", false),
		msidb.Col("Data", "V0", false),
	}}
	nums.Add(1, int32(-5), nil)
	nums.Add(2, nil, nil)

	for _, cp := range []int{0, 1252, 65001} {
		database := &msidb.Database{
			Codepage: cp,
			Tables:   []*msidb.Table{tbl, nums},
			Summary:  []summary.Property{{ID: summary.PIDCodepage, Value: 1252}, {ID: summary.PIDTitle, Value: "T"}},
		}

		raw, err := database.Marshal(msidb.Options{})
		if err != nil {
			f.Fatal(err)
		}

		f.Add(raw)
	}

	// A package with an embedded transform storage, so that the fuzzer
	// reaches the storage decoder.
	base := &msidb.Database{Codepage: 1252, Tables: []*msidb.Table{tbl}}
	variant := &msidb.Database{Codepage: 1252, Tables: []*msidb.Table{{Name: tbl.Name, Columns: tbl.Columns, Rows: [][]any{{"ProductName", "Hallo"}}}}}

	storage, err := msidb.TransformStorage("1031", base, variant, msidb.TransformOptions{})
	if err != nil {
		f.Fatal(err)
	}

	base.Storages = []cfb.Storage{storage}

	raw, err := base.Marshal(msidb.Options{CFBVersion: 3})
	if err != nil {
		f.Fatal(err)
	}

	f.Add(raw)
}

// FuzzUnmarshal feeds corrupted packages to the database reader. It must
// return an error or a database, never panic.
func FuzzUnmarshal(f *testing.F) {
	seedDatabase(f)

	f.Fuzz(func(t *testing.T, raw []byte) {
		db, err := msidb.Unmarshal(raw)
		if err != nil {
			return
		}

		for _, tbl := range db.Tables {
			for i, row := range tbl.Rows {
				if len(row) != len(tbl.Columns) {
					t.Fatalf("table %s row %d has %d cells for %d columns", tbl.Name, i, len(row), len(tbl.Columns))
				}
			}
		}
	})
}

// FuzzStreamName round-trips stream names through the encoding.
func FuzzStreamName(f *testing.F) {
	f.Add("Property", true)
	f.Add("Binary.Icon_1", false)
	f.Add("\u0005SummaryInformation", false)

	f.Fuzz(func(t *testing.T, name string, table bool) {
		enc, err := msidb.EncodeStreamName(name, table)
		if err != nil {
			return
		}

		dec, isTable := msidb.DecodeStreamName(enc)
		if dec != name || isTable != table {
			t.Fatalf("EncodeStreamName(%q, %v) = %q decodes to (%q, %v)", name, table, enc, dec, isTable)
		}
	})
}

// TestStreamNameEdgeCases are the FuzzStreamName findings.
func TestStreamNameEdgeCases(t *testing.T) {
	t.Parallel()

	if _, err := msidb.EncodeStreamName("\x80", false); err == nil {
		t.Error("EncodeStreamName accepted a name that is not valid UTF-8")
	}

	// A character outside the BMP is copied as a surrogate pair and must
	// decode to itself.
	for _, name := range []string{"\U00015041", "a\U0001F600b.cab", "\U0001F600\U0001F600"} {
		enc, err := msidb.EncodeStreamName(name, true)
		if err != nil {
			t.Fatal(err)
		}

		if dec, table := msidb.DecodeStreamName(enc); dec != name || !table {
			t.Errorf("%q decodes to (%q, %v)", name, dec, table)
		}
	}
}

// A refused name is reported as written, not in its encoded form (review
// finding on the FuzzBuild fixes).
func TestCheckStreamNameMessage(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"a!b.cab": `msidb: stream name "a!b.cab" contains '!'`,
		"T.Grüße": `msidb: stream name "T.Grüße" contains the cased character 'ü'`,
		"T.ｗide":  `msidb: stream name "T.ｗide" contains the cased character 'ｗ'`,
		"T.ok":    "",
	} {
		err := msidb.CheckStreamName(name)
		if got := fmt.Sprint(err); (want == "" && err != nil) || (want != "" && got != want) {
			t.Errorf("CheckStreamName(%q) = %v, want %q", name, err, want)
		}
	}
}
