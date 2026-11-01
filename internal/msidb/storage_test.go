package msidb //nolint:testpackage // the tests reach unexported encoders

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jkroepke/msi-builder/internal/cfb"
	"github.com/jkroepke/msi-builder/internal/summary"
)

// storageContents reads every stream of a storage, by name.
func storageContents(t *testing.T, storage cfb.Storage) map[string]string {
	t.Helper()

	out := make(map[string]string, len(storage.Streams))

	for _, src := range storage.Streams {
		reader, err := src.Open()
		if err != nil {
			t.Fatalf("storage %s stream %q: %v", storage.Name, src.Name, err)
		}

		data, err := io.ReadAll(reader)
		_ = reader.Close()

		if err != nil {
			t.Fatal(err)
		}

		if int64(len(data)) != src.Size {
			t.Errorf("storage %s stream %q: %d bytes, size says %d", storage.Name, src.Name, len(data), src.Size)
		}

		out[src.Name] = string(data)
	}

	return out
}

// TestStoragesRoundTrip checks that Unmarshal and Decode keep an embedded
// language transform, so that marshaling the result writes it back instead
// of silently dropping it.
func TestStoragesRoundTrip(t *testing.T) {
	t.Parallel()

	base := propertyDB([]any{"ProductName", "Example"}, []any{"ProductLanguage", "1033"})
	variant := propertyDB([]any{"ProductName", "Beispiel"}, []any{"ProductLanguage", "1031"})

	storage, err := TransformStorage("1031", base, variant, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// A large stream as well, so that one is read from sectors and not
	// only from the mini stream.
	big := bytes.Repeat([]byte("transform-"), 1000)
	storage.Streams = append(storage.Streams, cfb.Stream{Name: "Big", Data: big}.Source())

	database := propertyDB([]any{"ProductName", "Example"}, []any{"ProductLanguage", "1033"})
	database.Storages = []cfb.Storage{storage}

	first, err := database.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(first)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Storages) != 1 || back.Storages[0].Name != "1031" || back.Storages[0].CLSID != cfb.CLSIDTransform {
		t.Fatalf("storages after Unmarshal: %+v", back.Storages)
	}

	want := storageContents(t, storage)
	if got := storageContents(t, back.Storages[0]); !maps.Equal(got, want) {
		t.Errorf("Unmarshal storage streams %q, want %q", keysOf(got), keysOf(want))
	}

	second, err := back.Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Error("Unmarshal then Marshal changed the package")
	}

	file, err := cfb.Open(bytes.NewReader(second), int64(len(second)))
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := Decode(file)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.Storages) != 1 {
		t.Fatalf("Decode returned %d storages, want 1", len(decoded.Storages))
	}

	if got := storageContents(t, decoded.Storages[0]); !maps.Equal(got, want) {
		t.Errorf("Decode storage streams %q, want %q", keysOf(got), keysOf(want))
	}
}

// TestNestedStorageRefused checks that a storage inside a storage, which
// Database.Storages cannot hold, fails decoding instead of being dropped.
func TestNestedStorageRefused(t *testing.T) {
	t.Parallel()

	database := propertyDB([]any{"ProductName", "Example"})
	database.Storages = []cfb.Storage{{Name: "1031", CLSID: cfb.CLSIDTransform, Streams: []cfb.Source{
		cfb.Stream{Name: "A", Data: []byte("a")}.Source(),
		cfb.Stream{Name: "B", Data: []byte("b")}.Source(),
		cfb.Stream{Name: "C", Data: []byte("c")}.Source(),
	}}}

	var buf bytes.Buffer
	if _, err := database.MarshalTo(&buf, Options{CFBVersion: 3}); err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()

	file, err := cfb.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	var storage *cfb.Entry

	for _, ent := range file.Entries {
		if ent.Type == 1 && ent.Name == "1031" {
			storage = ent
		}
	}

	if storage == nil {
		t.Fatal("no storage 1031")
	}

	// Turn a sibling of the storage's tree root into an empty storage.
	top := file.Entries[storage.Child]

	child := top.Left
	if child == 0xFFFFFFFF {
		child = top.Right
	}

	if child == 0xFFFFFFFF {
		t.Fatalf("unexpected tree: %+v", top)
	}

	dir := int64(binary.LittleEndian.Uint32(data[0x30:])+1) * 512
	entry := data[dir+128*int64(child):]
	entry[0x42] = 1                                         // a storage
	binary.LittleEndian.PutUint32(entry[0x4C:], 0xFFFFFFFF) // with no children
	binary.LittleEndian.PutUint32(entry[0x74:], 0)          // no start sector
	binary.LittleEndian.PutUint64(entry[0x78:], 0)          // and no size

	if _, err := Unmarshal(data); err == nil || !strings.Contains(err.Error(), "nested storages") {
		t.Errorf("Unmarshal with a nested storage: %v, want a refusal", err)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// TestHugeLongStringLength checks that a long pool entry whose length does
// not fit the remaining _StringData fails before it becomes an int: on a
// 32-bit build 0x80000000 turned negative and a later string panicked.
func TestHugeLongStringLength(t *testing.T) {
	t.Parallel()

	raw, err := propertyDB([]any{"ProductName", "Example"}).Marshal(Options{CFBVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	poolName, _ := EncodeStreamName(TableStringPool, true)
	streams := file.Streams()

	for i, stream := range streams {
		if stream.Name != poolName {
			continue
		}

		// The code page header, a long entry of 0x80000000 bytes, then
		// the original entries.
		patched := make([]byte, 0, len(stream.Data)+8)
		patched = append(patched, stream.Data[:4]...)
		patched = append(patched, 0, 0, 1, 0, 0, 0, 0, 0x80)
		patched = append(patched, stream.Data[4:]...)
		streams[i].Data = patched
	}

	bad, err := cfb.Write(streams, cfb.Options{Version: 3, CLSID: cfb.CLSIDMSI})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unmarshal(bad); err == nil || !strings.Contains(err.Error(), "overruns _StringData") {
		t.Errorf("Unmarshal with a 2 GiB long string: %v, want an overrun error", err)
	}
}

// TestConstructedTypesChecked checks that a column type built directly,
// not by ParseType, is held to the same limits: a width of 256 would be
// stored as s0, and other values would spill into the type flags.
func TestConstructedTypesChecked(t *testing.T) {
	t.Parallel()

	for name, typ := range map[string]Type{
		"string width 256":    {Kind: KindString, Width: 256},
		"negative width":      {Kind: KindString, Width: -1},
		"int16 width 4":       {Kind: KindInt16, Width: 4},
		"localizable integer": {Kind: KindInt32, Width: 4, Localizable: true},
		"binary with width":   {Kind: KindBinary, Width: 2},
		"unknown kind":        {Kind: Kind(7)},
	} {
		tbl := &Table{Name: "T", Columns: []Column{Col("K", "s72", true), {Name: "C", Type: typ}}}
		if _, err := (&Database{Codepage: 1252, Tables: []*Table{tbl}}).Marshal(Options{}); err == nil {
			t.Errorf("%s: Marshal accepted %+v", name, typ)
		}
	}

	// Types as ParseType returns them pass, keys included.
	tbl := &Table{Name: "T", Columns: []Column{{Name: "K", Type: Type{Kind: KindString, Width: 255, Key: true}}, {Name: "C", Type: Type{Kind: KindInt32, Width: 4, Nullable: true}}}}
	if _, err := (&Database{Codepage: 1252, Tables: []*Table{tbl}}).Marshal(Options{}); err != nil {
		t.Errorf("valid constructed types: %v", err)
	}
}

// TestStreamNameCollision checks that two stored names that decode alike,
// the encoded and the literal form of one name, fail instead of one payload
// silently replacing the other.
func TestStreamNameCollision(t *testing.T) {
	t.Parallel()

	raw, err := propertyDB([]any{"ProductName", "Example"}).Marshal(Options{CFBVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := EncodeStreamName("cab", false)
	if err != nil || encoded == "cab" {
		t.Fatalf("encoded name %q, %v", encoded, err)
	}

	streams := append(file.Streams(), cfb.Stream{Name: encoded, Data: []byte("first")}, cfb.Stream{Name: "cab", Data: []byte("second")})

	bad, err := cfb.Write(streams, cfb.Options{Version: 3, CLSID: cfb.CLSIDMSI})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unmarshal(bad); err == nil || !strings.Contains(err.Error(), "both decode to") {
		t.Errorf("Unmarshal with colliding names: %v, want a refusal", err)
	}
}

// TestLoneSurrogateNameRefused checks that a stored name holding a lone
// surrogate, which reads as U+FFFD, fails instead of being written back
// under another name.
func TestLoneSurrogateNameRefused(t *testing.T) {
	t.Parallel()

	for _, inStorage := range []bool{false, true} {
		database := propertyDB([]any{"ProductName", "Example"})
		extra := cfb.Stream{Name: "Zq", Data: []byte("x")}

		if inStorage {
			database.Storages = []cfb.Storage{{Name: "1031", CLSID: cfb.CLSIDTransform, Streams: []cfb.Source{extra.Source()}}}
		} else {
			database.Streams = []cfb.Stream{extra}
		}

		var buf bytes.Buffer
		if _, err := database.MarshalTo(&buf, Options{CFBVersion: 3}); err != nil {
			t.Fatal(err)
		}

		data := buf.Bytes()
		dir := int(binary.LittleEndian.Uint32(data[0x30:])+1) * 512

		encoded := "Zq"
		if !inStorage {
			encoded, _ = EncodeStreamName("Zq", false)
		}

		patched := false

		for off := dir; off+128 <= len(data); off += 128 {
			name := data[off : off+2*len([]rune(encoded))]
			if string(name) == string(utf16le(utf16.Encode([]rune(encoded)))) {
				binary.LittleEndian.PutUint16(data[off:], 0xD800) // a lone high surrogate

				patched = true
			}
		}

		if !patched {
			t.Fatal("entry not found")
		}

		if _, err := Unmarshal(data); err == nil || !strings.Contains(err.Error(), "round-trip") {
			t.Errorf("in storage %v: Unmarshal with a lone surrogate name: %v, want a refusal", inStorage, err)
		}
	}
}

// TestOrphanTableStreamRefused checks that a table stream _Tables does not
// list fails instead of being dropped by the next Marshal.
func TestOrphanTableStreamRefused(t *testing.T) {
	t.Parallel()

	raw, err := propertyDB([]any{"ProductName", "Example"}).Marshal(Options{CFBVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	orphan, err := EncodeStreamName("UnknownTable", true)
	if err != nil {
		t.Fatal(err)
	}

	bad, err := cfb.Write(append(file.Streams(), cfb.Stream{Name: orphan, Data: []byte{1, 0}}), cfb.Options{Version: 3, CLSID: cfb.CLSIDMSI})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unmarshal(bad); err == nil || !strings.Contains(err.Error(), "UnknownTable is not in _Tables") {
		t.Errorf("Unmarshal with an orphan table stream: %v, want a refusal", err)
	}
}

// TestProblemsBounded checks that rows naming missing binary streams with
// long keys cannot grow Database.Problems without bound: each problem left
// the whole key-derived name in its message.
func TestProblemsBounded(t *testing.T) {
	t.Parallel()

	const rows = 300

	tbl := &Table{Name: "B", Columns: []Column{Col("Name", "s0", true), Col("Data", "V0", false)}}
	for i := range rows {
		tbl.Add(fmt.Sprintf("%04d%s", i, strings.Repeat("k", 2000)), nil)
	}

	raw, err := (&Database{Codepage: 1252, Tables: []*Table{tbl}}).Marshal(Options{CFBVersion: 3})
	if err != nil {
		t.Fatal(err)
	}

	file, err := cfb.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := EncodeStreamName("B", true)
	streams := file.Streams()

	for i, stream := range streams {
		if stream.Name != encoded {
			continue
		}

		// Column-major: the key references, then the binary column; mark
		// every binary cell present, with no stream behind it.
		data := bytes.Clone(stream.Data)
		for row := range rows {
			binary.LittleEndian.PutUint16(data[2*rows+2*row:], 1)
		}

		streams[i].Data = data
	}

	bad, err := cfb.Write(streams, cfb.Options{Version: 3, CLSID: cfb.CLSIDMSI})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(bad)
	if err != nil {
		t.Fatal(err)
	}

	total := 0
	for _, p := range back.Problems {
		total += len(p)
	}

	if len(back.Problems) > maxProblems+1 || total > (maxProblems+1)*(maxProblemLength+3) {
		t.Errorf("%d problems with %d bytes, want at most %d", len(back.Problems), total, maxProblems+1)
	}

	if len(back.Problems) == 0 || !strings.Contains(back.Problems[0], "cannot be a stream name") {
		t.Errorf("first problem %q, want the impossible stream name", back.Problems[:min(1, len(back.Problems))])
	}
}

// TestSummaryStreamReserved checks that a stream named like the summary
// beside Database.Summary is refused: it was written under its encoded
// name next to the literal one, and the result did not read back.
func TestSummaryStreamReserved(t *testing.T) {
	t.Parallel()

	database := propertyDB([]any{"ProductName", "Example"})
	database.Summary = []summary.Property{{ID: summary.PIDCodepage, Value: 1252}}
	database.Streams = []cfb.Stream{{Name: summary.StreamName, Data: []byte("x")}}

	if _, err := database.Marshal(Options{}); err == nil {
		t.Error("Marshal accepted a summary stream in Streams")
	}

	database.Streams = nil
	database.Sources = []cfb.Source{cfb.Stream{Name: summary.StreamName, Data: []byte("x")}.Source()}

	if _, err := database.Marshal(Options{}); err == nil {
		t.Error("Marshal accepted a summary stream in Sources")
	}
}

// TestAddProblemCutsWholeRunes checks that a long problem is cut before a
// character, never inside one.
func TestAddProblemCutsWholeRunes(t *testing.T) {
	t.Parallel()

	for pad := range 4 {
		var database Database

		addProblem(&database, strings.Repeat("a", maxProblemLength-pad)+strings.Repeat("日", 200))

		got := database.Problems[0]
		if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") || len(got) > maxProblemLength+3 {
			t.Errorf("pad %d: cut problem %q is not whole runes within the limit", pad, got[max(0, len(got)-12):])
		}
	}
}

// TestUnicodeNameLimits checks that table and column name limits count
// characters (UTF-16 units), not UTF-8 bytes: msi.dll creates and commits
// an 11-character Japanese table name (reported by a review's native probe).
func TestUnicodeNameLimits(t *testing.T) {
	t.Parallel()

	table := strings.Repeat("表", 11)  // 33 UTF-8 bytes
	column := strings.Repeat("列", 30) // 90 UTF-8 bytes
	tbl := &Table{Name: table, Columns: []Column{Col(column, "s72", true)}, Rows: [][]any{{"x"}}}

	raw, err := (&Database{Codepage: 65001, Tables: []*Table{tbl}}).Marshal(Options{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	back, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	if got := back.Table(table); got == nil || got.Columns[0].Name != column {
		t.Errorf("table %q did not read back", table)
	}

	// The limits still hold in characters, and the encoded stream name
	// adds its prefix: 31 such characters are one unit too many.
	for name, bad := range map[string]*Table{
		"table":         {Name: strings.Repeat("表", MaxTableName+1), Columns: []Column{Col("K", "s72", true)}},
		"encoded table": {Name: strings.Repeat("表", MaxTableName), Columns: []Column{Col("K", "s72", true)}},
		"column":        {Name: "T", Columns: []Column{Col(strings.Repeat("列", maxColumnName+1), "s72", true)}},
	} {
		if _, err := (&Database{Codepage: 65001, Tables: []*Table{bad}}).Marshal(Options{}); err == nil {
			t.Errorf("%s name over the limit accepted", name)
		}
	}
}

// TestNullKeyBinaryStreamName checks that a NULL key segment is empty in a
// binary stream name, "T." and not "T.<nil>", which msi.dll cannot read.
func TestNullKeyBinaryStreamName(t *testing.T) {
	t.Parallel()

	if got := BinaryStreamName("T", []any{nil}); got != "T." {
		t.Errorf("BinaryStreamName with a NULL key = %q, want %q", got, "T.")
	}

	tbl := &Table{Name: "T", Columns: []Column{Col("K", "S72", true), Col("Data", "V0", false)}, Rows: [][]any{{nil, []byte("payload")}}}

	raw, err := (&Database{Codepage: 1252, Tables: []*Table{tbl}}).Marshal(Options{})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Problems) != 0 {
		t.Errorf("problems: %v", back.Problems)
	}

	if _, ok := back.Stream("T."); ok {
		t.Error("the binary stream was left over as an extra stream")
	}

	if got, ok := back.Table("T").Rows[0][1].([]byte); !ok || string(got) != "payload" {
		t.Errorf("binary cell %v, want the payload", back.Table("T").Rows[0][1])
	}

	file, err := cfb.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := EncodeStreamName("T.", false)
	if _, ok := file.Stream(encoded); !ok {
		t.Error("no stream named T.")
	}
}
