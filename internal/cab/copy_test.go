package cab_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/spool"
)

// copyFiles returns files of which some repeat the bytes of an earlier one,
// and the entries that store those bytes once: "shared" is copied by
// "copy1" and "copy2", with stored files between and after them.
func copyFiles() ([]cab.File, []cab.Entry) {
	when := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(3)) //nolint:gosec // test data

	random := func(n int) []byte {
		data := make([]byte, n)
		rng.Read(data)

		return data
	}

	shared := random(3*32768 + 77)
	files := []cab.File{
		{Name: "first", Data: random(40000), Time: when},
		{Name: "shared", Data: shared, Time: when},
		{Name: "middle", Data: bytes.Repeat([]byte("middle "), 9000), Time: when},
		{Name: "copy1", Data: shared, Time: when.Add(2 * time.Second), Attributes: cab.AttrReadOnly},
		{Name: "after", Data: []byte("after the copy"), Time: when},
		{Name: "copy2", Data: shared, Time: when},
	}

	ents := entries(files)
	ents[3].Copy, ents[5].Copy = 2, 2
	ents[3].Open = func() (io.ReadCloser, error) { return nil, errors.New("a copy was opened") }
	ents[5].Open = nil

	return files, ents
}

// record is one parsed CFFILE: size, offset and folder.
type record struct {
	size, offset uint32
	folder       uint16
}

// records parses the CFFILE records of a cabinet without a reserve.
func records(raw []byte) []record {
	order := binary.LittleEndian
	pos := int(order.Uint32(raw[0x10:]))
	out := make([]record, 0, order.Uint16(raw[0x1C:]))

	for range int(order.Uint16(raw[0x1C:])) {
		out = append(out, record{order.Uint32(raw[pos:]), order.Uint32(raw[pos+4:]), order.Uint16(raw[pos+8:])})
		pos += 16 + bytes.IndexByte(raw[pos+16:], 0) + 1
	}

	return out
}

func writeEntries(t *testing.T, ents []cab.Entry, opts cab.Options) []byte {
	t.Helper()

	var buf spool.Buffer

	if _, err := cab.WriteTo(&buf, ents, opts); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// TestCopiesShareBytes checks that a copy's record points at the bytes of
// the entry it copies, that the shared entry has a folder of its own, that
// the stored entry after a copy starts a new folder, and that Read and Walk
// return every file with its own metadata.
func TestCopiesShareBytes(t *testing.T) {
	t.Parallel()

	for _, opts := range []cab.Options{{Compression: cab.None}, {Compression: cab.MSZIP}, {Compression: cab.LZX}, {Compression: cab.LZX, Window: 15}} {
		files, ents := copyFiles()
		raw := writeEntries(t, ents, opts)

		full, err := cab.Write(files, opts)
		if err != nil {
			t.Fatal(err)
		}

		// LZX at 2^21 finds the copies in its window on its own; a
		// smaller window and the others store them again.
		saved := 2 * len(files[1].Data)
		if opts.Compression == cab.LZX && opts.Window == 0 {
			saved = 0
		}

		if len(raw) > len(full)-saved+1000 {
			t.Errorf("compression %d: %d bytes with copies, %d without", opts.Compression, len(raw), len(full))
		}

		// first | shared | middle (copy1) | after (copy2): four folders.
		if folders := binary.LittleEndian.Uint16(raw[0x1A:]); folders != 4 || cab.Folders(ents) != 4 {
			t.Errorf("compression %d: %d folders, Folders says %d, want 4", opts.Compression, folders, cab.Folders(ents))
		}

		recs := records(raw)
		size := uint32(3*32768 + 77)
		want := []record{{40000, 0, 0}, {size, 0, 1}, {63000, 0, 2}, {size, 0, 1}, {14, 0, 3}, {size, 0, 1}}

		for i := range want {
			if recs[i] != want[i] {
				t.Errorf("compression %d: record %d is %+v, want %+v", opts.Compression, i, recs[i], want[i])
			}
		}

		if bound := cab.Bound(len(ents), cab.Folders(ents), 36, 40000+int64(len(files[1].Data))+63000+14, opts.Compression); int64(len(raw)) > bound {
			t.Errorf("compression %d: %d bytes above the bound %d", opts.Compression, len(raw), bound)
		}

		checkCopies(t, raw, files)
	}
}

// checkCopies reads raw with Read and Walk and compares the files.
func checkCopies(t *testing.T, raw []byte, files []cab.File) {
	t.Helper()

	got, err := cab.Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	var walked []cab.File

	err = cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(file cab.File, data io.Reader) error {
		file.Data, err = io.ReadAll(data)
		walked = append(walked, file)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, list := range map[string][]cab.File{"Read": got, "Walk": walked} {
		if len(list) != len(files) {
			t.Fatalf("%s returned %d files, want %d", name, len(list), len(files))
		}

		for i, want := range files {
			attr := want.Attributes
			if attr == 0 {
				attr = cab.AttrArchive
			}

			if list[i].Name != want.Name || !bytes.Equal(list[i].Data, want.Data) || !list[i].Time.Equal(want.Time) || list[i].Attributes != attr {
				t.Errorf("%s: file %d is %s (%d bytes, %v, %#x), want %s (%d bytes, %v, %#x)", name, i,
					list[i].Name, len(list[i].Data), list[i].Time, list[i].Attributes, want.Name, len(want.Data), want.Time, attr)
			}
		}
	}
}

// TestCopiesWithoutStoredFiles checks the folders of cabinets that are only
// a shared entry and its copies, and of copies at the start and end of a run.
func TestCopiesWithoutStoredFiles(t *testing.T) {
	t.Parallel()

	data := []byte("same bytes")
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }

	cases := []struct {
		name    string
		copies  []int
		folders int
	}{
		{"only copies", []int{0, 1, 1}, 1},
		{"no copies", []int{0, 0, 0}, 1},
		{"shared last", []int{0, 0, 2}, 2},
		{"two shared", []int{0, 0, 1, 2, 0}, 3},
	}

	for _, tc := range cases {
		ents := make([]cab.Entry, 0, len(tc.copies))
		files := make([]cab.File, 0, len(tc.copies))

		for i, copyOf := range tc.copies {
			name := string(rune('a' + i))
			ents = append(ents, cab.Entry{Name: name, Size: int64(len(data)), Open: open, Copy: copyOf})
			files = append(files, cab.File{Name: name, Data: data, Time: cab.ParseDOSTime(0x21, 0)})
		}

		if got := cab.Folders(ents); got != tc.folders {
			t.Errorf("%s: Folders is %d, want %d", tc.name, got, tc.folders)
		}

		raw := writeEntries(t, ents, cab.Options{Compression: cab.MSZIP})
		if got := int(binary.LittleEndian.Uint16(raw[0x1A:])); got != tc.folders {
			t.Errorf("%s: %d folders written, want %d", tc.name, got, tc.folders)
		}

		checkCopies(t, raw, files)
	}

	if got := cab.Folders(nil); got != 1 {
		t.Errorf("Folders of no entries is %d, want 1", got)
	}
}

// TestCopyErrors checks that a copy must point at an earlier stored entry
// of its size.
func TestCopyErrors(t *testing.T) {
	t.Parallel()

	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("abc")), nil }

	for name, copies := range map[string][]int{
		"itself":    {0, 2},
		"later":     {2, 0},
		"a copy":    {0, 1, 2},
		"negative":  {0, -1},
		"past them": {0, 9},
	} {
		ents := make([]cab.Entry, 0, len(copies))
		for i, copyOf := range copies {
			ents = append(ents, cab.Entry{Name: string(rune('a' + i)), Size: 3, Open: open, Copy: copyOf})
		}

		var buf spool.Buffer
		if _, err := cab.WriteTo(&buf, ents, cab.Options{}); err == nil {
			t.Errorf("%s: WriteTo accepted copies %v", name, copies)
		}
	}

	empty := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }

	for name, ents := range map[string][]cab.Entry{
		"another size": {{Name: "a", Size: 3, Open: open}, {Name: "b", Size: 2, Copy: 1}},
		"empty":        {{Name: "a", Open: empty}, {Name: "b", Copy: 1}},
	} {
		var buf spool.Buffer
		if _, err := cab.WriteTo(&buf, ents, cab.Options{}); err == nil {
			t.Errorf("WriteTo accepted a copy of %s", name)
		}
	}
}

// TestCopiesAtPosition checks that the folder starts patched at the end
// land in the cabinet when it does not start at offset 0 of the target.
func TestCopiesAtPosition(t *testing.T) {
	t.Parallel()

	files, ents := copyFiles()

	var buf spool.Buffer

	_, _ = buf.Write(make([]byte, 100))

	written, err := cab.WriteTo(&buf, ents, cab.Options{Compression: cab.MSZIP})
	if err != nil {
		t.Fatal(err)
	}

	checkCopies(t, buf.Bytes()[100:100+written], files)
}
