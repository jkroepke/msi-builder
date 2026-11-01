package msidb //nolint:testpackage // the tests reach unexported encoders

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/summary"
)

// Test vector A.1 of docs/spec/MSISPEC.md section 3.
func TestStreamNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		want  []rune
		table bool
	}{
		{name: "Property", table: true, want: []rune{0x4840, 0x4559, 0x44F2, 0x4568, 0x4737}},
		{name: "_Tables", table: true, want: []rune{0x4840, 0x3F7F, 0x4164, 0x422F, 0x4836}},
		{name: "cab1.cab", table: false, want: []rune{0x4126, 0x3865, 0x41BE, 0x4164}},
		{name: "My-File", table: false, want: []rune{0x4716, 0x002D, 0x430F, 0x422F}},
	} {
		got, err := EncodeStreamName(tc.name, tc.table)
		if err != nil {
			t.Fatal(err)
		}

		if string(tc.want) != got {
			t.Errorf("%q: %U, want %U", tc.name, []rune(got), tc.want)
		}

		back, table := DecodeStreamName(got)
		if back != tc.name || table != tc.table {
			t.Errorf("%q decodes to %q (table %v)", tc.name, back, table)
		}
	}

	// 62 alphabet characters fit, 63 do not (MSIDB-11).
	if _, err := EncodeStreamName("Binary."+strings.Repeat("k", 55), false); err != nil {
		t.Error(err)
	}

	if _, err := EncodeStreamName("Binary."+strings.Repeat("k", 56), false); err == nil {
		t.Error("63-character stream name accepted")
	}
}

func TestTypes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		idt  string
		word uint16
	}{
		{"s72", 0x0D48}, {"S72", 0x1D48}, {"l0", 0x0F00}, {"L255", 0x1FFF},
		{"i2", 0x0502}, {"I2", 0x1502}, {"i4", 0x0104}, {"I4", 0x1104}, {"v0", 0x0900}, {"V0", 0x1900},
	} {
		typ, err := ParseType(tc.idt)
		if err != nil {
			t.Fatal(err)
		}

		if typ.Word() != tc.word || typ.String() != tc.idt {
			t.Errorf("%s: word %#x, string %s", tc.idt, typ.Word(), typ.String())
		}

		back, err := TypeFromWord(tc.word)
		if err != nil || back != typ {
			t.Errorf("%s: TypeFromWord %+v %v", tc.idt, back, err)
		}
	}

	if Col("File", "s72", true).Type.Word() != 0x2D48 {
		t.Error("File.File is not 0x2D48")
	}

	for _, bad := range []string{"s256", "i3", "x0", "", "s", "v1"} {
		if _, err := ParseType(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func sampleDB() *Database {
	prop := &Table{Name: "Property", Columns: []Column{Col("Property", "s72", true), Col("Value", "l0", false)}}
	prop.Add("ProductName", "Hello")
	prop.Add("ALLUSERS", "1")
	prop.Add("Manufacturer", "Exämple")

	media := &Table{Name: "Media", Columns: []Column{
		Col("DiskId", "i2", true), Col("LastSequence", "i4", false), Col("DiskPrompt", "L64", false),
		Col("Cabinet", "S255", false), Col("VolumeLabel", "S32", false), Col("Source", "S72", false),
	}}
	media.Add(2, 300000, nil, "#cab2.cab", "", nil)
	media.Add(1, 3, nil, "#cab1.cab", nil, nil)

	bin := &Table{Name: "Binary", Columns: []Column{Col("Name", "s72", true), Col("Data", "v0", false)}}
	bin.Add("helper.dll", []byte("MZ-not-really"))

	empty := &Table{Name: "LaunchCondition", Columns: []Column{Col("Condition", "s255", true), Col("Description", "l255", false)}}

	return &Database{
		Codepage: 1252,
		Tables:   []*Table{prop, media, bin, empty},
		Streams:  []cfb.Stream{{Name: "cab1.cab", Data: []byte("MSCF...")}},
		Summary: []summary.Property{
			{ID: summary.PIDCodepage, Value: 1252}, {ID: summary.PIDTemplate, Value: "x64;1033"},
			{ID: summary.PIDRevision, Value: "{00000000-0000-0000-0000-000000000001}"},
			{ID: summary.PIDPageCount, Value: 500}, {ID: summary.PIDWordCount, Value: 2},
			{ID: summary.PIDCreated, Value: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	database := sampleDB()

	out, err := database.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Problems) != 0 {
		t.Errorf("problems: %v", back.Problems)
	}

	if back.Codepage != 1252 {
		t.Errorf("code page %d", back.Codepage)
	}

	names := make([]string, 0, len(back.Tables))
	for _, tb := range back.Tables {
		names = append(names, tb.Name)
	}

	if fmt.Sprint(names) != "[Binary LaunchCondition Media Property]" {
		t.Errorf("tables %v", names)
	}

	media := back.Table("Media")
	if len(media.Rows) != 2 || media.Rows[0][0] != 1 || media.Rows[1][1] != 300000 || media.Rows[1][3] != "#cab2.cab" || media.Rows[0][4] != nil {
		t.Errorf("Media rows %v", media.Rows)
	}

	prop := back.Table("Property")
	if prop.Columns[1].Type.String() != "l0" || len(prop.Rows) != 3 {
		t.Errorf("Property %+v", prop)
	}

	found := false

	for _, r := range prop.Rows {
		if r[0] == "Manufacturer" && r[1] == "Exämple" {
			found = true
		}
	}

	if !found {
		t.Errorf("Manufacturer row lost: %v", prop.Rows)
	}

	if b := back.Table("Binary"); !bytes.Equal(fmt.Append(nil, b.Rows[0][1]), fmt.Append(nil, []byte("MZ-not-really"))) {
		t.Errorf("Binary %v", b.Rows)
	}

	if lc := back.Table("LaunchCondition"); lc == nil || len(lc.Rows) != 0 || len(lc.Columns) != 2 {
		t.Error("empty table lost")
	}

	if cab, ok := back.Stream("cab1.cab"); !ok || string(cab) != "MSCF..." {
		t.Error("cabinet stream lost")
	}

	if len(back.Streams) != 1 {
		t.Errorf("extra streams %v", back.Streams)
	}

	if summary.Get(back.Summary, summary.PIDTemplate) != "x64;1033" {
		t.Errorf("summary %v", back.Summary)
	}

	// The raw _StringPool: code page header, no long-ref flag.
	file, _ := cfb.Read(out)
	poolName, _ := EncodeStreamName(TableStringPool, true)
	pool, _ := file.Stream(poolName)

	if pool[0] != 0xE4 || pool[1] != 0x04 || pool[3]&0x80 != 0 {
		t.Errorf("pool header %x", pool[:4])
	}

	// Media stream: DiskId 1 is 01 80, LastSequence 3 is 03 00 00 80.
	mediaName, _ := EncodeStreamName("Media", true)
	ms, _ := file.Stream(mediaName)

	if !bytes.Equal(ms[:4], []byte{1, 0x80, 2, 0x80}) || !bytes.Equal(ms[4:8], []byte{3, 0, 0, 0x80}) {
		t.Errorf("Media stream %x", ms)
	}

	again, _ := database.Marshal(Options{})
	if !bytes.Equal(again, out) {
		t.Error("not deterministic")
	}
}

func TestLongString(t *testing.T) {
	t.Parallel()

	database := &Database{Codepage: 65001}
	tb := &Table{Name: "T", Columns: []Column{Col("K", "s72", true), Col("V", "L0", false)}}
	long := strings.Repeat("ü", 40000) // 80000 bytes in UTF-8
	tb.Add("k", long)
	tb.Add("short", "x")
	database.Tables = []*Table{tb}

	out, err := database.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}

	if back.Table("T").Rows[0][1] != long || len(back.Problems) != 0 {
		t.Errorf("long string lost: %v", back.Problems)
	}

	// Long entry layout: 0, refcount, low16, high16.
	f, _ := cfb.Read(out)
	poolName, _ := EncodeStreamName(TableStringPool, true)
	pool, _ := f.Stream(poolName)

	n := 80000

	want := []byte{0, 0, 1, 0, byte(n), byte(n >> 8), byte(n >> 16), 0} //nolint:gosec // 3-byte reference
	if !bytes.Contains(pool, want) {
		t.Errorf("long entry not found in %x", pool)
	}
}

func TestLongReferences(t *testing.T) {
	t.Parallel()

	database := &Database{Codepage: 1252}
	tbl := &Table{Name: "T", Columns: []Column{Col("K", "s72", true), Col("V", "S72", false)}}

	for i := range 65540 {
		tbl.Add(fmt.Sprintf("k%06d", i), "same")
	}

	database.Tables = []*Table{tbl}

	if _, err := database.Marshal(Options{}); err == nil {
		t.Fatal("refcount above 32767 accepted")
	}

	for i := range tbl.Rows {
		tbl.Rows[i][1] = fmt.Sprintf("v%d", i%30000)
	}

	out, err := database.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Table("T").Rows) != 65540 || back.Table("T").Rows[65539][0] != "k065539" || len(back.Problems) != 0 {
		t.Errorf("3-byte references failed: %v", back.Problems)
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()

	cols := func(n int) []Column {
		out := []Column{Col("K", "s72", true)}
		for i := 1; i < n; i++ {
			out = append(out, Col(fmt.Sprintf("C%d", i), "S0", false))
		}

		return out
	}

	ok := &Table{Name: "T", Columns: cols(31)}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}

	bad := &Table{Name: "T", Columns: cols(32)}
	if err := bad.Validate(); err == nil {
		t.Error("32 columns accepted")
	}

	for _, tbl := range []*Table{
		{Name: "T", Columns: []Column{Col("A", "S72", false), Col("K", "s72", true)}},
		{Name: "T", Columns: []Column{Col("K", "v0", true)}},
		{Name: "T", Columns: []Column{Col("K", "s72", true), Col("A", "v0", false), Col("B", "V0", false)}},
		{Name: strings.Repeat("T", 32), Columns: cols(2)},
		{Name: "T", Columns: cols(2), Rows: [][]any{{nil, "x"}}},
		{Name: "T", Columns: cols(2), Rows: [][]any{{"", "x"}}},
		{Name: "T", Columns: cols(2), Rows: [][]any{{"k"}}},
		{Name: "T", Columns: []Column{Col("K", "i2", true)}, Rows: [][]any{{-32768}}},
		{Name: "T", Columns: []Column{Col("K", "i4", true)}, Rows: [][]any{{int64(2147483648)}}},
		{Name: "T", Columns: []Column{Col("K", "i4", true)}, Rows: [][]any{{"1"}}},
	} {
		if err := tbl.Validate(); err == nil {
			t.Errorf("table %+v accepted", tbl)
		}
	}

	dup := &Database{Codepage: 1252, Tables: []*Table{{Name: "T", Columns: cols(2), Rows: [][]any{{"a", "1"}, {"a", "2"}}}}}
	if _, err := dup.Marshal(Options{}); err == nil {
		t.Error("duplicate keys accepted")
	}

	ascii := &Database{Codepage: 0, Tables: []*Table{{Name: "T", Columns: cols(2), Rows: [][]any{{"ä", "1"}}}}}
	if _, err := ascii.Marshal(Options{}); err == nil {
		t.Error("non-ASCII string in a code page 0 database accepted")
	}

	sys := &Database{Codepage: 1252, Tables: []*Table{{Name: "_Tables", Columns: cols(2)}}}
	if _, err := sys.Marshal(Options{}); err == nil {
		t.Error("_Tables accepted as a user table")
	}
}

func TestUnmarshalDetectsUnsortedRows(t *testing.T) {
	t.Parallel()

	db := sampleDB()

	out, _ := db.Marshal(Options{})
	file, _ := cfb.Read(out)

	// Swap the two Media rows' DiskId cells (bytes 0-1 and 2-3 of the stream).
	mediaName, _ := EncodeStreamName("Media", true)

	streams := make([]cfb.Stream, 0, len(file.Streams()))

	for _, stream := range file.Streams() {
		if stream.Name == mediaName {
			d := append([]byte(nil), stream.Data...)
			d[0], d[1], d[2], d[3] = d[2], d[3], d[0], d[1]
			stream.Data = d
		}

		streams = append(streams, stream)
	}

	bad, _ := cfb.Write(streams, cfb.Options{Version: file.Version})

	back, err := Unmarshal(bad)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Problems) == 0 {
		t.Error("unsorted rows not reported")
	}
}

// TestValidateKeepsCells checks that Validate and Marshal read the caller's
// rows without rewriting them into the canonical cell types.
func TestValidateKeepsCells(t *testing.T) {
	t.Parallel()

	tbl := &Table{
		Name:    "T",
		Columns: []Column{Col("K", "s72", true), Col("N", "I2", false), Col("S", "S0", false)},
		Rows:    [][]any{{"a", int32(7), ""}},
	}

	if err := tbl.Validate(); err != nil {
		t.Fatal(err)
	}

	db := &Database{Codepage: 1252, Tables: []*Table{tbl}}

	data, err := db.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := tbl.Rows[0][1].(int32); !ok || tbl.Rows[0][2] != "" {
		t.Errorf("cells rewritten: %#v", tbl.Rows[0])
	}

	back, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}

	got := back.Table("T").Rows[0]
	if got[0] != "a" || got[1] != 7 || got[2] != nil {
		t.Errorf("round trip %#v", got)
	}
}

// TestDecodeLoadsSharedBinaryStreamOnce checks that rows sharing a primary
// key do not each load a copy of the one binary stream they name: a small
// file could otherwise expand into gigabytes of cells (REVIEW1 #2).
func TestDecodeLoadsSharedBinaryStreamOnce(t *testing.T) {
	t.Parallel()

	const copies = 200

	payload := bytes.Repeat([]byte{0xAB}, 64<<10)

	bin := &Table{Name: "Blob", Columns: []Column{Col("Key", "s72", true), Col("Data", "v0", false)}}
	bin.Add("k", payload)

	out, err := (&Database{Codepage: 1252, Tables: []*Table{bin}}).Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	// The one-row stream is the key's string reference and the binary
	// marker, two bytes each; repeat each column copies times.
	blobName, _ := EncodeStreamName("Blob", true)
	streams := make([]cfb.Stream, 0, len(file.Streams()))

	for _, stream := range file.Streams() {
		if stream.Name == blobName {
			if len(stream.Data) != 4 {
				t.Fatalf("Blob stream %x", stream.Data)
			}

			stream.Data = append(bytes.Repeat(stream.Data[:2], copies), bytes.Repeat(stream.Data[2:], copies)...)
		}

		streams = append(streams, stream)
	}

	bad, err := cfb.Write(streams, cfb.Options{Version: file.Version})
	if err != nil {
		t.Fatal(err)
	}

	opened, err := cfb.Read(bad)
	if err != nil {
		t.Fatal(err)
	}

	back, err := Decode(opened)
	if err != nil {
		t.Fatal(err)
	}

	rows := back.Table("Blob").Rows
	if len(rows) != copies {
		t.Fatalf("%d rows", len(rows))
	}

	loaded := 0

	for _, row := range rows {
		if row[1] != nil {
			loaded++
		}
	}

	if loaded != 1 {
		t.Errorf("%d rows hold a copy of the stream, want 1", loaded)
	}

	if first, ok := rows[0][1].([]byte); !ok || !bytes.Equal(first, payload) {
		t.Error("first row lost its stream")
	}

	shared := 0

	for _, problem := range back.Problems {
		if strings.Contains(problem, "already belongs to an earlier row") {
			shared++
		}
	}

	// Problems are capped at maxProblems, with a note that more were left out.
	if want := min(copies-1, maxProblems); shared != want {
		t.Errorf("%d shared-stream problems, want %d: %v", shared, want, back.Problems[:min(3, len(back.Problems))])
	}

	if copies-1 > maxProblems && back.Problems[len(back.Problems)-1] != "further problems left out" {
		t.Errorf("last problem %q, want the note that more were left out", back.Problems[len(back.Problems)-1])
	}
}
