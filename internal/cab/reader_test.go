package cab //nolint:testpackage // the tests build records with the unexported checksum and layout constants

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// walkAll reads every file of raw through Walk.
func walkAll(raw []byte) (map[string]string, error) {
	got := map[string]string{}

	err := Walk(bytes.NewReader(raw), int64(len(raw)), func(file File, data io.Reader) error {
		body, err := io.ReadAll(data)
		got[file.Name] = string(body)

		return err
	})

	return got, err
}

// mszipPayload compresses data as the only block of an MSZIP folder.
func mszipPayload(t *testing.T, data string) []byte {
	t.Helper()

	payload, err := compressBlock([]byte(data), nil, 9)
	if err != nil {
		t.Fatal(err)
	}

	return payload
}

// TestMSZIPStreamMustEndAtBlockSize checks that a DEFLATE stream yielding
// more than cbUncomp bytes, or one without its final block, is refused by
// Read and Walk, as FDI refuses both.
func TestMSZIPStreamMustEndAtBlockSize(t *testing.T) {
	t.Parallel()

	// A stored DEFLATE block of "hello" with BFINAL clear and nothing
	// after it.
	unfinished := append([]byte{'C', 'K', 0, 5, 0, 0xFA, 0xFF}, "hello"...)

	cases := map[string]RawBlock{
		"longer stream":   {Payload: mszipPayload(t, "hello"), Uncomp: 1},
		"no final block":  {Payload: unfinished, Uncomp: 5},
		"trailing blocks": {Payload: append(append([]byte(nil), unfinished...), 1, 0, 0, 0xFF, 0xFF), Uncomp: 4},
	}

	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw := RawCabinet(
				[]RawFolder{{Type: MSZIP, Blocks: []RawBlock{block}}},
				[]RawFile{{Name: []byte("a"), Size: uint32(block.Uncomp), Attr: AttrArchive}}, //nolint:gosec // small
			)

			if files, err := Read(raw); err == nil {
				t.Errorf("Read accepted it: %q", files[0].Data)
			}

			if got, err := walkAll(raw); err == nil {
				t.Errorf("Walk accepted it: %q", got)
			}
		})
	}

	// The same stream with the right size reads.
	raw := RawCabinet(
		[]RawFolder{{Type: MSZIP, Blocks: []RawBlock{{Payload: mszipPayload(t, "hello"), Uncomp: 5}}}},
		[]RawFile{{Name: []byte("a"), Size: 5, Attr: AttrArchive}},
	)

	if files, err := Read(raw); err != nil || string(files[0].Data) != "hello" {
		t.Errorf("Read: %v", err)
	}
}

// TestWalkChecksBlocksAfterLastFile checks that Walk reads a folder's blocks
// past its last file, so that a corrupt or truncated block there fails Walk
// as it fails Read.
func TestWalkChecksBlocksAfterLastFile(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte{'x'}, blockSize+1)

	for name, folder := range map[string]RawFolder{
		"checksum": {Type: None, Blocks: []RawBlock{
			{Payload: data[:blockSize], Uncomp: blockSize},
			{Payload: data[blockSize:], Uncomp: 1, BadSum: true},
		}},
		"MSZIP": {Type: MSZIP, Blocks: []RawBlock{
			{Payload: mszipPayload(t, string(data[:blockSize])), Uncomp: blockSize},
			{Payload: []byte("CK\xFF"), Uncomp: 1},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw := RawCabinet([]RawFolder{folder}, []RawFile{{Name: []byte("a"), Size: 1, Attr: AttrArchive}})

			if _, err := Read(raw); err == nil {
				t.Error("Read accepted the corrupt block")
			}

			if _, err := walkAll(raw); err == nil {
				t.Error("Walk accepted the corrupt block")
			}
		})
	}

	// A folder with blocks but no files is read too.
	raw := RawCabinet([]RawFolder{
		{Type: None, Blocks: []RawBlock{{Payload: []byte("ok"), Uncomp: 2}}},
		{Type: None, Blocks: []RawBlock{{Payload: []byte("no"), Uncomp: 2, BadSum: true}}},
	}, []RawFile{{Name: []byte("a"), Size: 2, Attr: AttrArchive}})

	if _, err := walkAll(raw); err == nil {
		t.Error("Walk accepted a corrupt block in a folder without files")
	}

	// Truncation after the last file fails too.
	good := RawCabinet([]RawFolder{{Type: None, Blocks: []RawBlock{
		{Payload: data[:blockSize], Uncomp: blockSize},
		{Payload: data[blockSize:], Uncomp: 1},
	}}}, []RawFile{{Name: []byte("a"), Size: 1, Attr: AttrArchive}})

	if got, err := walkAll(good); err != nil || got["a"] != "x" {
		t.Errorf("Walk of the intact cabinet: %v %q", err, got)
	}

	if _, err := walkAll(good[:len(good)-1]); err == nil {
		t.Error("Walk accepted a truncated last block")
	}
}

// TestEmptyFolderOffsetIgnored checks that a folder without blocks does not
// bound the file records, whatever its unused offset says: FDI extracts such
// a cabinet.
func TestEmptyFolderOffsetIgnored(t *testing.T) {
	t.Parallel()

	raw := RawCabinet([]RawFolder{
		{Type: MSZIP, Start: 0},
		{Type: None, Blocks: []RawBlock{{Payload: []byte("hello"), Uncomp: 5}}},
	}, []RawFile{
		{Name: []byte("empty"), Folder: 0, Attr: AttrArchive},
		{Name: []byte("hello"), Size: 5, Folder: 1, Attr: AttrArchive},
	})

	files, err := Read(raw)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(files) != 2 || len(files[0].Data) != 0 || string(files[1].Data) != "hello" {
		t.Errorf("Read = %+v", files)
	}

	got, err := walkAll(raw)
	if err != nil || got["hello"] != "hello" || got["empty"] != "" || len(got) != 2 {
		t.Errorf("Walk = %q, %v", got, err)
	}
}

// TestLegacyNames checks that a name without the UTF-8 attribute is decoded
// in LegacyCodePage, as makecab.exe writes and expand.exe reads it on a
// Western system, and that a name with the attribute must be UTF-8.
func TestLegacyNames(t *testing.T) {
	t.Parallel()

	cabinet := func(name string, attr uint16) []byte {
		return RawCabinet(
			[]RawFolder{{Type: None, Blocks: []RawBlock{{Payload: []byte("x"), Uncomp: 1}}}},
			[]RawFile{{Name: []byte(name), Size: 1, Attr: attr}},
		)
	}

	for _, tc := range []struct {
		raw  string
		want string
		attr uint16
	}{
		{"dir\\r\xE9sum\xE9.txt", "dir\\résumé.txt", AttrArchive},
		{"\x80uro", "€uro", AttrArchive},
		{"plain.txt", "plain.txt", AttrArchive},
		{"dir\\résumé.txt", "dir\\résumé.txt", AttrArchive | attrUTF8Name},
	} {
		files, err := Read(cabinet(tc.raw, tc.attr))
		if err != nil || files[0].Name != tc.want || files[0].Attributes != AttrArchive {
			t.Errorf("Read(%q, %#x) = %+v, %v; want %q", tc.raw, tc.attr, files, err, tc.want)

			continue
		}

		fsys, err := Open(cabinet(tc.raw, tc.attr))
		if err != nil {
			t.Errorf("Open(%q): %v", tc.raw, err)

			continue
		}

		if data, err := fs.ReadFile(fsys, strings.ReplaceAll(tc.want, `\`, "/")); err != nil || string(data) != "x" {
			t.Errorf("ReadFile(%q) = %q, %v", tc.want, data, err)
		}
	}

	if _, err := Read(cabinet("r\xE9sum\xE9", AttrArchive|attrUTF8Name)); err == nil {
		t.Error("a name flagged UTF-8 with invalid UTF-8 was accepted")
	}

	// These five bytes are undefined in code page 1252; decoding them to one
	// replacement character would merge distinct names.
	for _, raw := range []string{"a\x81", "a\x8D", "a\x8F", "a\x90", "a\x9D"} {
		if files, err := Read(cabinet(raw, AttrArchive)); err == nil {
			t.Errorf("Read(%q) = %q, want an error for a byte undefined in code page 1252", raw, files[0].Name)
		}
	}

	// The writer marks a non-ASCII name as UTF-8, so it reads back as is.
	out, err := Write([]File{{Name: "résumé", Data: []byte("x")}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if files, err := Read(out); err != nil || files[0].Name != "résumé" {
		t.Errorf("round trip: %+v, %v", files, err)
	}
}

// TestNewFSRejectsFileDirectoryCollisions checks that a path cannot be both
// a file and the directory of another file, in either order.
func TestNewFSRejectsFileDirectoryCollisions(t *testing.T) {
	t.Parallel()

	for _, names := range [][]string{
		{"a", `a\b`},
		{`a\b`, "a"},
		{"a", `a\b\c`},
		{`a\b\c`, "a"},
		{`d\a`, `d\a\b`},
		{`d\a\b`, `d\a`},
		{"a/", `a\b`},
	} {
		files := make([]File, 0, len(names))
		for _, name := range names {
			files = append(files, File{Name: name, Data: []byte(name)})
		}

		if _, err := NewFS(files); err == nil {
			t.Errorf("NewFS(%q) accepted a file that is also a directory", names)
		}
	}

	fsys, err := NewFS([]File{
		{Name: `a\b`, Data: []byte("1")},
		{Name: "ab", Data: []byte("2")},
		{Name: `a\c\d`, Data: []byte("3")},
		{Name: "a.txt", Data: []byte("4")},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fstest.TestFS(fsys, "a/b", "ab", "a/c/d", "a.txt"); err != nil {
		t.Error(err)
	}
}

// scriptReader returns one scripted result per Read, then io.EOF.
type scriptReader struct {
	steps []readStep
	index int
}

type readStep struct {
	err  error
	data string
}

func (r *scriptReader) Read(buf []byte) (int, error) {
	if r.index >= len(r.steps) {
		return 0, io.EOF
	}

	step := r.steps[r.index]
	r.index++

	return copy(buf, step.data), step.err
}

func (*scriptReader) Close() error { return nil }

// writeScripted writes a one-entry cabinet whose source follows steps.
func writeScripted(comp Compression, size int64, steps []readStep) error {
	ent := Entry{
		Name: "a", Size: size,
		Open: func() (io.ReadCloser, error) { return &scriptReader{steps: steps}, nil },
	}

	var buf seekBuffer

	_, err := WriteTo(&buf, []Entry{ent}, Options{Compression: comp})

	return err
}

// seekBuffer is a minimal io.WriteSeeker for WriteTo.
type seekBuffer struct {
	data []byte
	pos  int
}

func (b *seekBuffer) Write(chunk []byte) (int, error) {
	if grow := b.pos + len(chunk) - len(b.data); grow > 0 {
		b.data = append(b.data, make([]byte, grow)...)
	}

	b.pos += copy(b.data[b.pos:], chunk)

	return len(chunk), nil
}

func (b *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = int(offset)
	case io.SeekCurrent:
		b.pos += int(offset)
	case io.SeekEnd:
		b.pos = len(b.data) + int(offset)
	}

	return int64(b.pos), nil
}

// TestWriteToSourceErrors checks that WriteTo reports a source error that
// comes with the last bytes, and that it accepts the end of a source only
// at io.EOF, never at a read of no bytes and no error.
func TestWriteToSourceErrors(t *testing.T) {
	t.Parallel()

	errOnce := errors.New("disk read failed")

	for _, comp := range []Compression{None, MSZIP, LZX} {
		if err := writeScripted(comp, 5, []readStep{{data: "hello", err: errOnce}}); !errors.Is(err, errOnce) {
			t.Errorf("compression %d: error with the last bytes: got %v", comp, err)
		}

		if err := writeScripted(comp, 6, []readStep{{data: "hello", err: errOnce}, {data: "!"}}); !errors.Is(err, errOnce) {
			t.Errorf("compression %d: error with bytes then more bytes: got %v", comp, err)
		}

		if err := writeScripted(comp, 1, []readStep{{data: "a"}, {}, {data: "b"}}); err == nil {
			t.Errorf("compression %d: a source longer than its size after an empty read was accepted", comp)
		}

		if err := writeScripted(comp, 1, []readStep{{data: "a"}, {}, {}, {}}); err != nil {
			t.Errorf("compression %d: empty reads before io.EOF: %v", comp, err)
		}

		stuck := make([]readStep, 2*maxEmptyReads)
		stuck[0] = readStep{data: "a"}

		if err := writeScripted(comp, 1, stuck); !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("compression %d: a source that never ends: got %v", comp, err)
		}

		if err := writeScripted(comp, 2, stuck); !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("compression %d: a source that stops delivering: got %v", comp, err)
		}

		if err := writeScripted(comp, 5, []readStep{{data: "abc", err: io.EOF}}); err == nil {
			t.Errorf("compression %d: a short source was accepted", comp)
		}
	}
}

// TestWriteRejectsInvalidUTF8Names checks that the writer refuses a name it
// would flag as UTF-8 although it is not, which no reader could decode.
func TestWriteRejectsInvalidUTF8Names(t *testing.T) {
	t.Parallel()

	if _, err := Write([]File{{Name: "\x8c", Data: []byte("a")}}, Options{}); err == nil {
		t.Error("a name that is not valid UTF-8 was written")
	}
}

// TestMSZIPEmptyBlocks checks blocks with cbUncomp 0: an empty final block
// reads, a block without BFINAL or with data does not.
func TestMSZIPEmptyBlocks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		payload []byte
		ok      bool
	}{
		{"empty final block", []byte{'C', 'K', 1, 0, 0, 0xFF, 0xFF}, true},
		{"empty block without BFINAL", []byte{'C', 'K', 0, 0, 0, 0xFF, 0xFF}, false},
		{"block with data", mszipPayload(t, "x"), false},
	} {
		raw := RawCabinet([]RawFolder{
			{Type: MSZIP, Blocks: []RawBlock{{Payload: tc.payload, Uncomp: 0}}},
		}, []RawFile{{Name: []byte("a"), Attr: AttrArchive}})

		_, readErr := Read(raw)
		_, walkErr := walkAll(raw)

		if (readErr == nil) != tc.ok || (walkErr == nil) != tc.ok {
			t.Errorf("%s: Read %v, Walk %v; want success %v", tc.name, readErr, walkErr, tc.ok)
		}
	}
}

// TestWalkChecksBlocksVisitSkipped checks that Walk reads and verifies the
// blocks of a file whose visit function does not read it, here a corrupt
// block inside the file.
func TestWalkChecksBlocksVisitSkipped(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte{'y'}, blockSize+10)
	raw := RawCabinet([]RawFolder{{Type: None, Blocks: []RawBlock{
		{Payload: data[:blockSize], Uncomp: blockSize},
		{Payload: data[blockSize:], Uncomp: 10, BadSum: true},
	}}}, []RawFile{{Name: []byte("a"), Size: blockSize + 10, Attr: AttrArchive}})

	err := Walk(bytes.NewReader(raw), int64(len(raw)), func(File, io.Reader) error { return nil })
	if err == nil {
		t.Error("Walk accepted a corrupt block when visit read nothing")
	}
}

// TestWalkChecksTailOnFolderSwitch checks that Walk verifies the blocks after
// a folder's last file when it moves on to another folder, not only at the
// end of the cabinet.
func TestWalkChecksTailOnFolderSwitch(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte{'z'}, blockSize+4)
	raw := RawCabinet([]RawFolder{
		{Type: None, Blocks: []RawBlock{
			{Payload: data[:blockSize], Uncomp: blockSize},
			{Payload: data[blockSize:], Uncomp: 4, BadSum: true},
		}},
		{Type: None, Blocks: []RawBlock{{Payload: []byte("next"), Uncomp: 4}}},
	}, []RawFile{
		{Name: []byte("a"), Size: 4, Folder: 0, Attr: AttrArchive},
		{Name: []byte("x"), Size: 4, Folder: 1, Attr: AttrArchive},
	})

	var visited []string

	err := Walk(bytes.NewReader(raw), int64(len(raw)), func(file File, _ io.Reader) error {
		visited = append(visited, file.Name)

		return nil
	})
	if err == nil {
		t.Fatal("Walk accepted a corrupt block after the last file of the first folder")
	}

	if len(visited) != 1 {
		t.Errorf("Walk visited %q before failing, want only the first folder's file", visited)
	}
}
