package cfb //nolint:testpackage // the tests patch directory entries

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestSourceSizeLimit: a declared size beyond the 2 GB limit is rejected
// before any sector arithmetic, so it neither wraps around the check nor
// sizes an allocation, and large streams that only together exceed the limit
// are counted in sectors.
func TestSourceSizeLimit(t *testing.T) {
	t.Parallel()

	opened := 0
	src := func(name string, size int64) Source {
		return Source{Name: name, Size: size, Open: func() (io.ReadCloser, error) {
			opened++

			return io.NopCloser(strings.NewReader("")), nil
		}}
	}

	for name, sources := range map[string][]Source{
		"max int64":            {src("huge", math.MaxInt64)},
		"wraps the byte count": {src("huge", 9223272036854775807)},
		"one byte over":        {src("big", maxFileSize+1)},
		"together":             {src("a", maxFileSize/2), src("b", maxFileSize/2), src("c", maxFileSize/2)},
		"many":                 {src("a", maxFileSize), src("b", maxFileSize), src("c", maxFileSize), src("d", maxFileSize)},
	} {
		for _, version := range []int{3, 4} {
			_, err := WriteTo(io.Discard, sources, Options{Version: version})
			if err == nil || !strings.Contains(err.Error(), "exceed 2 GB") {
				t.Errorf("%s v%d: %v, want an error about 2 GB", name, version, err)
			}
		}
	}

	if opened != 0 {
		t.Errorf("a source of a file too large was opened %d times", opened)
	}
}

// TestEmptySourceChecked: an empty source is opened and read like any other,
// so one that fails or delivers bytes is an error instead of an empty stream.
func TestEmptySourceChecked(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		open func() (io.ReadCloser, error)
		want string
	}{
		"delivers data": {func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("data")), nil }, "delivered 1 bytes, declared 0"},
		"open fails":    {func() (io.ReadCloser, error) { return nil, errors.New("boom") }, "boom"},
		"read fails":    {func() (io.ReadCloser, error) { return io.NopCloser(iotestErr{}), nil }, "read failed"},
	} {
		for _, storage := range []bool{false, true} {
			src := []Source{{Name: "E", Size: 0, Open: tc.open}}

			var err error
			if storage {
				_, err = WriteStoragesTo(io.Discard, nil, []Storage{{Name: "S", Streams: src}}, Options{})
			} else {
				_, err = WriteTo(io.Discard, src, Options{})
			}

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s (storage %v): %v, want %q", name, storage, err, tc.want)
			}
		}
	}

	raw, err := Write([]Stream{{Name: "E"}}, Options{})
	if err != nil {
		t.Fatalf("an empty stream that delivers nothing: %v", err)
	}

	if data, ok := parse(t, raw).file.Stream("E"); !ok || len(data) != 0 {
		t.Errorf("empty stream read back as %q, %v", data, ok)
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// TestNameTerminator: a name must end with its NUL terminator and hold no
// NUL before it (MS-CFB 2.6.1). Windows does not find a stream whose
// terminator was overwritten (CFB-11), so the reader must not either.
func TestNameTerminator(t *testing.T) {
	t.Parallel()

	raw, err := Write([]Stream{{Name: "Abcd", Data: []byte("payload")}, {Name: "x", Data: []byte("x")}}, Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	off := parse(t, raw).entry(t, "Abcd")

	for name, patch := range map[string]func([]byte){
		"terminator replaced": func(b []byte) { binary.LittleEndian.PutUint16(b[off+8:], 'X') },
		"NUL inside":          func(b []byte) { binary.LittleEndian.PutUint16(b[off+2:], 0) },
		"NUL first":           func(b []byte) { binary.LittleEndian.PutUint16(b[off:], 0) },
	} {
		bad := bytes.Clone(raw)
		patch(bad)
		wantError(t, name, bad, "invalid name length or terminator")
	}
}

// TestReadsStayInsideSize: the reader behind Open may hold more bytes than
// the size given to Open; a stream that needs a byte past that size fails
// with io.ErrUnexpectedEOF instead of serving it, in a sector and in the
// mini stream.
func TestReadsStayInsideSize(t *testing.T) {
	t.Parallel()

	for _, version := range []int{3, 4} {
		sectorSize := 512 << (3 * (version - 3))

		// Large (5000 bytes) is the last stream, so its tail ends the file.
		large, err := Write(vectorStreams(), Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		end := len(large) - sectorSize + 5000%sectorSize
		checkCut(t, version, "Large", large, end)

		// With only small streams, the mini stream's sector ends the file.
		small, err := Write([]Stream{{Name: "Small", Data: bytes.Repeat([]byte("s"), 100)}}, Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		checkCut(t, version, "Small", small, len(small)-sectorSize+100)
	}
}

// checkCut opens the complete raw file with the size end, which still holds
// all of name, and with end-1, which cuts its last byte off.
func checkCut(t *testing.T, version int, name string, raw []byte, end int) {
	t.Helper()

	whole, err := Read(raw)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := whole.Stream(name)

	for _, size := range []int{end, end - 1} {
		file, err := Open(bytes.NewReader(raw), int64(size))
		if err != nil {
			t.Fatalf("v%d %s size %d: %v", version, name, size, err)
		}

		stream, err := file.Reader(name)
		if err != nil {
			t.Fatalf("v%d %s size %d: %v", version, name, size, err)
		}

		got, err := io.ReadAll(stream)

		switch {
		case size == end && (err != nil || !bytes.Equal(got, want)):
			t.Errorf("v%d %s: read inside the size: %d bytes, %v", version, name, len(got), err)
		case size < end && !errors.Is(err, io.ErrUnexpectedEOF):
			t.Errorf("v%d %s: read past the size: %d bytes, %v, want io.ErrUnexpectedEOF", version, name, len(got), err)
		}

		if size < end {
			if _, err := stream.ReadAt(make([]byte, 1), int64(len(want)-1)); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("v%d %s: ReadAt of the last byte past the size: %v, want io.ErrUnexpectedEOF", version, name, err)
			}

			if _, ok := file.Stream(name); ok {
				t.Errorf("v%d %s: Stream served a stream past the size", version, name)
			}
		}
	}
}

// TestUnicodeCaseDuplicate: Windows resolves sibling names that differ only
// in the case of a non-ASCII letter to one entry (CFB-10), so the reader
// rejects them as duplicates. Units of a surrogate pair are never folded
// (MS-CFB 2.6.4), and different letters stay different names.
func TestUnicodeCaseDuplicate(t *testing.T) {
	t.Parallel()

	raw, err := Write([]Stream{{Name: "Xbcd", Data: []byte("first")}, {Name: "Ybcd", Data: []byte("second")}}, Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	rename := func(names ...string) []byte {
		out := bytes.Clone(raw)
		lay := layout{file: parse(t, raw).file, raw: out}

		for i, old := range []string{"Xbcd", "Ybcd"} {
			off := lay.entry(t, old)
			units := utf16.Encode([]rune(names[i]))

			if len(units) != 4 {
				t.Fatalf("%q is not 4 units", names[i])
			}

			for k, unit := range units {
				binary.LittleEndian.PutUint16(out[off+2*k:], unit)
			}
		}

		return out
	}

	for _, pair := range [][2]string{
		{"Ébcd", "ébcd"},
		{"ÿbcd", "Ÿbcd"}, // U+00FF and U+0178, far apart
		{"Kbcd", "Kbcd"}, // KELVIN SIGN folds to K
		{"Σbcd", "ςbcd"}, // final sigma
		{"ǅbcd", "ǆbcd"}, // title case
		{"Ａbcd", "ａbcd"}, // fullwidth
		{"ıbcd", "Ibcd"}, // dotless i: upper case I, but alone in its fold orbit
	} {
		wantError(t, pair[0]+"/"+pair[1], rename(pair[0], pair[1]), "duplicate name")
	}

	// Units without a case mapping that still share a folding orbit are
	// refused by the writer, so it never writes a file the reader rejects.
	for _, name := range []string{"a\uFB05", "a\uFB06", "\u0390", "\u1FD3", "\u03B0", "\u1FE3"} {
		if _, err := Write([]Stream{{Name: name}}, Options{}); err == nil || !strings.Contains(err.Error(), "cased character") {
			t.Errorf("Write %q: %v, want an error about a cased character", name, err)
		}
	}

	if _, err := Write([]Stream{{Name: "Straße"}}, Options{}); err != nil {
		t.Errorf("Write ß, whose only folding partner has a mapping: %v", err)
	}

	for _, pair := range [][2]string{
		{"Ébcd", "Ábcd"},
		{"\U00010400cd", "\U00010428cd"}, // Deseret: a surrogate pair each, not folded
		{"ßbcd", "Sbcd"},
	} {
		if _, err := Read(rename(pair[0], pair[1])); err != nil {
			t.Errorf("%s/%s: %v", pair[0], pair[1], err)
		}
	}
}

// TestHeaderCounts: the MiniFAT sector count and the MiniFAT start must
// agree on whether a MiniFAT exists, and a v4 header counts its directory
// sectors. Windows refuses a file where they do not (CFB-12), so the reader
// must not follow the chain anyway.
func TestHeaderCounts(t *testing.T) {
	t.Parallel()

	for _, version := range []int{3, 4} {
		small, err := Write([]Stream{{Name: "s", Data: []byte("small")}}, Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		bad := bytes.Clone(small)
		put32(bad, 0x40, 0)
		wantError(t, "MiniFAT start without sectors", bad, "MiniFAT start")

		large, err := Write([]Stream{{Name: "l", Data: make([]byte, miniCutoff)}}, Options{Version: version})
		if err != nil {
			t.Fatal(err)
		}

		bad = bytes.Clone(large)
		put32(bad, 0x40, 1)
		wantError(t, "MiniFAT sectors without a start", bad, "MiniFAT start")

		bad = bytes.Clone(large)
		put32(bad, 0x3C, freeSect)
		parse(t, bad) // FREESECT as an absent start, as before

		bad = bytes.Clone(small)
		if version == 4 {
			put32(bad, 0x28, 0)
			wantError(t, "v4 without directory sectors", bad, "0 directory sectors")
		} else {
			put32(bad, 0x28, 7)
			parse(t, bad) // v3 ignores the field, which it writes as 0
		}
	}
}

// pausing delivers data, returning (0, nil) once at pause, which io.Reader
// allows, then the rest.
type pausing struct {
	data   []byte
	pause  int
	paused bool
}

func (p *pausing) Read(buf []byte) (int, error) {
	if len(p.data) == 0 {
		return 0, io.EOF
	}

	if p.pause == 0 && !p.paused {
		p.paused = true

		return 0, nil
	}

	count := len(p.data)
	if p.pause > 0 {
		count = min(count, p.pause)
	}

	count = copy(buf, p.data[:count])
	p.data, p.pause = p.data[count:], max(p.pause-count, 0)

	return count, nil
}

// stalled returns (0, nil) for ever once its data is read.
type stalled struct{ data []byte }

func (s *stalled) Read(buf []byte) (int, error) {
	n := copy(buf, s.data)
	s.data = s.data[n:]

	return n, nil
}

// TestSourcePauseAtSize: a large source that pauses with (0, nil) right
// at its declared size and then delivers one more byte is longer than its
// size; a source that never reports its end fails instead of hanging.
func TestSourcePauseAtSize(t *testing.T) {
	t.Parallel()

	source := func(r io.Reader) []Source {
		return []Source{{Name: "L", Size: miniCutoff, Open: func() (io.ReadCloser, error) { return io.NopCloser(r), nil }}}
	}

	if _, err := WriteTo(io.Discard, source(&pausing{data: make([]byte, miniCutoff+1), pause: miniCutoff}), Options{}); err == nil || !strings.Contains(err.Error(), "longer than its declared size") {
		t.Errorf("pause at the size: %v, want an error about the declared size", err)
	}

	if _, err := WriteTo(io.Discard, source(&pausing{data: make([]byte, miniCutoff), pause: miniCutoff}), Options{}); err != nil {
		t.Errorf("pause at the size, then io.EOF: %v", err)
	}

	if _, err := WriteTo(io.Discard, source(&stalled{data: make([]byte, miniCutoff)}), Options{}); !errors.Is(err, io.ErrNoProgress) {
		t.Errorf("no end: %v, want io.ErrNoProgress", err)
	}

	// A stall before the declared size, after a stream that flushed the
	// buffer, so that io.Copy hands the copy to the destination's ReadFrom.
	stall := []Source{
		Stream{Name: "a", Data: make([]byte, 120*512)}.Source(),
		{Name: "b", Size: 10000, Open: func() (io.ReadCloser, error) {
			return io.NopCloser(&stalled{data: make([]byte, 5000)}), nil
		}},
	}
	if _, err := WriteTo(&bytes.Buffer{}, stall, Options{}); !errors.Is(err, io.ErrNoProgress) {
		t.Errorf("stall before the size: %v, want io.ErrNoProgress", err)
	}

	// Small and empty sources are read into memory, by another path.
	for _, size := range []int64{0, 10} {
		src := []Source{{Name: "S", Size: size, Open: func() (io.ReadCloser, error) {
			return io.NopCloser(&stalled{data: make([]byte, size)}), nil
		}}}

		if _, err := WriteTo(io.Discard, src, Options{}); !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("no end, size %d: %v, want io.ErrNoProgress", size, err)
		}
	}

	small := []Source{{Name: "S", Size: 10, Open: func() (io.ReadCloser, error) {
		return io.NopCloser(&pausing{data: make([]byte, 11), pause: 10}), nil
	}}}
	if _, err := WriteTo(io.Discard, small, Options{}); err == nil || !strings.Contains(err.Error(), "delivered 11 bytes") {
		t.Errorf("small source, pause at the size: %v, want an error about 11 bytes", err)
	}
}

// failingAt returns every byte but reports err for reads at or after from.
type failingAt struct {
	data []byte
	from int64
	err  error
}

func (f failingAt) ReadAt(buf []byte, off int64) (int, error) {
	n := copy(buf, f.data[off:])
	if off >= f.from {
		return n, f.err
	}

	return n, nil
}

// TestMetadataReadError: a read of a FAT, directory or MiniFAT sector that
// fills the buffer but reports an error other than io.EOF fails Open.
func TestMetadataReadError(t *testing.T) {
	t.Parallel()

	raw, err := Write(vectorStreams(), Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	checksum := errors.New("checksum mismatch")

	if _, err := Open(failingAt{data: raw, from: 512, err: checksum}, int64(len(raw))); !errors.Is(err, checksum) {
		t.Errorf("Open = %v, want the backing reader's error", err)
	}
}

// TestChainInsideFile: a FAT sector describes more sectors than a small
// file holds. A chain through such a sector, here the root's mini stream
// starting at sector 100 of a file of a few sectors, fails at Open instead
// of being followed sector by sector past the end of the file.
func TestChainInsideFile(t *testing.T) {
	t.Parallel()

	raw, err := Write([]Stream{{Name: "s", Data: []byte("x")}}, Options{Version: 3})
	if err != nil {
		t.Fatal(err)
	}

	lay := parse(t, raw)
	if len(raw)/512-1 >= 100 {
		t.Fatalf("the test file has %d sectors", len(raw)/512-1)
	}

	bad := bytes.Clone(raw)
	put32(bad, lay.fat(100), endOfChain)
	put32(bad, lay.entry(t, "Root Entry")+0x74, 100)
	wantError(t, "root chain past the file", bad, "out of range")
}
