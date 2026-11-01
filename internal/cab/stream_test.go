package cab_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/memtest"
	"github.com/jkroepke/msi-builder/internal/spool"
)

func sampleFiles() []cab.File {
	when := time.Date(2024, 5, 6, 7, 8, 10, 0, time.UTC)
	big := make([]byte, 3*32768+123)

	for i := range big {
		big[i] = byte(i % 251)
	}

	return []cab.File{
		{Name: "fil_a", Data: []byte("Hello, cabinet!\r\n"), Time: when},
		{Name: "fil_empty", Time: when},
		{Name: "fil_big", Data: big, Time: when, Attributes: cab.AttrReadOnly},
		{Name: "fil_tail", Data: []byte("tail"), Time: when},
	}
}

func entries(files []cab.File) []cab.Entry {
	out := make([]cab.Entry, 0, len(files))

	for _, f := range files {
		out = append(out, cab.Entry{
			Name: f.Name, Size: int64(len(f.Data)), Time: f.Time, Attributes: f.Attributes,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(f.Data)), nil },
		})
	}

	return out
}

// TestWriteToMatchesWrite checks that the streaming writer produces the same
// bytes as the in-memory one, for both compression types.
func TestWriteToMatchesWrite(t *testing.T) {
	t.Parallel()

	for _, comp := range []cab.Compression{cab.None, cab.MSZIP} {
		want, err := cab.Write(sampleFiles(), cab.Options{Compression: comp})
		if err != nil {
			t.Fatal(err)
		}

		var buf spool.Buffer

		size, err := cab.WriteTo(&buf, entries(sampleFiles()), cab.Options{Compression: comp})
		if err != nil {
			t.Fatal(err)
		}

		if size != int64(len(want)) || !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("compression %d: WriteTo differs from Write (%d vs %d bytes)", comp, size, len(want))
		}
	}
}

// TestWalkMatchesRead checks the one-pass reader against the in-memory one.
func TestWalkMatchesRead(t *testing.T) {
	t.Parallel()

	for _, comp := range []cab.Compression{cab.None, cab.MSZIP} {
		raw, err := cab.Write(sampleFiles(), cab.Options{Compression: comp})
		if err != nil {
			t.Fatal(err)
		}

		want, err := cab.Read(raw)
		if err != nil {
			t.Fatal(err)
		}

		var got []cab.File

		err = cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(file cab.File, data io.Reader) error {
			if file.Data != nil {
				t.Error("Walk passed data in the File")
			}

			file.Data, err = io.ReadAll(data)
			got = append(got, file)

			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != len(want) {
			t.Fatalf("compression %d: Walk saw %d files, Read %d", comp, len(got), len(want))
		}

		for i := range want {
			if got[i].Name != want[i].Name || !bytes.Equal(got[i].Data, want[i].Data) ||
				!got[i].Time.Equal(want[i].Time) || got[i].Attributes != want[i].Attributes {
				t.Errorf("compression %d: file %d differs: %+v vs %+v", comp, i, got[i], want[i])
			}
		}
	}
}

// TestWalkStopsEarly checks that a callback error ends the walk, and that a
// callback need not drain its reader.
func TestWalkStopsEarly(t *testing.T) {
	t.Parallel()

	raw, _ := cab.Write(sampleFiles(), cab.Options{Compression: cab.MSZIP})
	errStop := errors.New("stop")
	seen := 0

	err := cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(file cab.File, _ io.Reader) error {
		seen++

		if file.Name == "fil_big" {
			return errStop
		}

		return nil
	})
	if !errors.Is(err, errStop) || seen != 3 {
		t.Errorf("walk returned %v after %d files", err, seen)
	}

	var names []string

	err = cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(file cab.File, data io.Reader) error {
		names = append(names, file.Name)

		// Read nothing from fil_big; the next file must still be right.
		if file.Name == "fil_tail" {
			b, err := io.ReadAll(data)
			if err != nil || string(b) != "tail" {
				t.Errorf("fil_tail read %q, %v", b, err)
			}
		}

		return nil
	})
	if err != nil || strings.Join(names, ",") != "fil_a,fil_empty,fil_big,fil_tail" {
		t.Errorf("walk: %v, files %v", err, names)
	}
}

// TestWalkSharedRecord checks that Walk, like Read, serves a file whose
// record points back at the bytes of the file before it (section 5).
func TestWalkSharedRecord(t *testing.T) {
	t.Parallel()

	raw, _ := cab.Write([]cab.File{{Name: "one", Data: []byte("abcdef")}, {Name: "two", Data: []byte("xyz")}}, cab.Options{Compression: cab.None})

	// Point "two" at offset 0 like "one": a shared record (section 5).
	shared := append([]byte(nil), raw...)
	pos := 36 + 8 + 16 + len("one") + 1
	copy(shared[pos+4:], []byte{0, 0, 0, 0})

	files, err := cab.Read(shared)
	if err != nil || string(files[1].Data) != "abc" {
		t.Errorf("Read of the shared record: %v, %+v", err, files)
	}

	var got []string

	err = cab.Walk(bytes.NewReader(shared), int64(len(shared)), func(_ cab.File, data io.Reader) error {
		read, err := io.ReadAll(data)
		got = append(got, string(read))

		return err
	})
	if err != nil || strings.Join(got, ",") != "abcdef,abc" {
		t.Errorf("Walk of the shared record: %v, %q", err, got)
	}
}

// TestWalkRejectsOverlong checks that a file record whose size runs past
// its folder is an error in Walk as it is in Read, not a short file.
func TestWalkRejectsOverlong(t *testing.T) {
	t.Parallel()

	raw, _ := cab.Write([]cab.File{{Name: "one", Data: []byte("abcdef")}}, cab.Options{Compression: cab.None})

	bad := append([]byte(nil), raw...)
	copy(bad[36+8:], []byte{100, 0, 0, 0}) // cbFile of "one"

	if _, err := cab.Read(bad); err == nil {
		t.Error("Read accepted the overlong record")
	}

	var got []byte

	err := cab.Walk(bytes.NewReader(bad), int64(len(bad)), func(_ cab.File, data io.Reader) error {
		got, _ = io.ReadAll(data)

		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "extends past its folder") {
		t.Errorf("Walk accepted the overlong record: %v (visitor read %q)", err, got)
	}
}

// TestWriteToPosition checks that WriteTo leaves the destination positioned
// right after the cabinet, also when the destination holds bytes beyond it.
func TestWriteToPosition(t *testing.T) {
	t.Parallel()

	var buf spool.Buffer

	_, _ = buf.Write(make([]byte, 1000))
	_, _ = buf.Seek(10, io.SeekStart)

	written, err := cab.WriteTo(&buf, []cab.Entry{{Name: "a", Size: 3, Open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("abc")), nil
	}}}, cab.Options{Compression: cab.None})
	if err != nil {
		t.Fatal(err)
	}

	pos, _ := buf.Seek(0, io.SeekCurrent)
	if pos != 10+written {
		t.Errorf("position after WriteTo is %d, want %d", pos, 10+written)
	}

	if _, err := cab.Read(buf.Bytes()[10 : 10+written]); err != nil {
		t.Errorf("cabinet at offset 10: %v", err)
	}
}

// closeCounter counts Close calls on the readers it hands out.
type closeCounter struct {
	opened, closed int
}

func (c *closeCounter) open(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		c.opened++

		return &countedReader{Reader: strings.NewReader(s), closer: c}, nil
	}
}

type countedReader struct {
	io.Reader

	closer *closeCounter
}

func (r *countedReader) Close() error {
	r.closer.closed++

	return nil
}

// TestEntryErrors checks the streaming writer's checks on its sources, and
// that every opened entry is closed again, also on errors.
func TestEntryErrors(t *testing.T) {
	t.Parallel()

	opts := cab.Options{Compression: cab.MSZIP}

	var counter closeCounter

	open := counter.open

	for name, tc := range map[string]struct {
		entries []cab.Entry
		want    string
	}{
		"short":    {[]cab.Entry{{Name: "a", Size: 10, Open: open("12345")}}, "short of its declared size"},
		"long":     {[]cab.Entry{{Name: "a", Size: 3, Open: open("12345")}}, "longer than its declared size"},
		"no open":  {[]cab.Entry{{Name: "a", Size: 3}}, "no Open function"},
		"negative": {[]cab.Entry{{Name: "a", Size: -1, Open: open("")}}, "negative size"},
		"open fails": {[]cab.Entry{{Name: "a", Size: 1, Open: func() (io.ReadCloser, error) {
			return nil, errors.New("boom")
		}}}, "boom"},
		"too big": {[]cab.Entry{{Name: "a", Size: 65535*32768 + 1, Open: open("")}}, "65535 blocks"},
	} {
		var buf spool.Buffer

		_, err := cab.WriteTo(&buf, tc.entries, opts)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}

	// A failing destination must close the entry too.
	_, err := cab.WriteTo(failingSeeker{}, []cab.Entry{{Name: "a", Size: 5, Open: open("12345")}}, opts)
	if err == nil {
		t.Error("WriteTo to a failing destination succeeded")
	}

	if counter.opened == 0 || counter.opened != counter.closed {
		t.Errorf("opened %d entries, closed %d", counter.opened, counter.closed)
	}
}

// failingSeeker fails every write.
type failingSeeker struct{}

func (failingSeeker) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func (failingSeeker) Seek(int64, int) (int64, error) { return 0, nil }

// TestEntriesFromFS checks the fs.FS source for streaming entries.
func TestEntriesFromFS(t *testing.T) {
	t.Parallel()

	when := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	fsys := fstest.MapFS{
		"bin/app.exe": {Data: []byte("MZ app"), ModTime: when},
		"readme.txt":  {Data: []byte("read me"), ModTime: when},
	}

	ents, err := cab.Entries(fsys)
	if err != nil {
		t.Fatal(err)
	}

	var buf spool.Buffer

	if _, err := cab.WriteTo(&buf, ents, cab.Options{Compression: cab.MSZIP}); err != nil {
		t.Fatal(err)
	}

	files, err := cab.Files(fsys)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := cab.Write(files, cab.Options{Compression: cab.MSZIP})
	if !bytes.Equal(buf.Bytes(), want) {
		t.Error("Entries and Files give different cabinets")
	}

	back, _ := cab.Read(buf.Bytes())
	if len(back) != 2 || back[0].Name != `bin\app.exe` || !back[0].Time.Equal(when) {
		t.Errorf("read back %+v", back)
	}
}

// TestBoundedMemory writes a cabinet far larger than the memory the writer
// may use, from a generated source, and reads it back the same way.
func TestBoundedMemory(t *testing.T) { //nolint:paralleltest // measures the heap of the whole process
	if testing.Short() {
		t.Skip("writes 32 MB")
	}

	for name, comp := range map[string]cab.Compression{"MSZIP": cab.MSZIP, "LZX": cab.LZX} { //nolint:paralleltest // measures the heap of the whole process
		t.Run(name, func(t *testing.T) { boundedMemory(t, comp) })
	}
}

func boundedMemory(t *testing.T, comp cab.Compression) {
	t.Helper()

	if raceEnabled {
		t.Skip("32 MB with -race and coverage takes minutes; CI runs it without -race")
	}

	const (
		size  = 32 << 20
		limit = 16 << 20
	)

	path := filepath.Join(t.TempDir(), "big.cab")

	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	defer out.Close()

	ents := []cab.Entry{
		{Name: "fil_small", Size: 5, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("small")), nil }},
		{Name: "fil_big", Size: size, Open: func() (io.ReadCloser, error) { return memtest.Pattern(size), nil }},
	}

	var (
		written int64
		werr    error
	)

	peak := memtest.PeakHeap(limit, func() {
		written, werr = cab.WriteTo(out, ents, cab.Options{Compression: comp})
	})

	if werr != nil {
		t.Fatal(werr)
	}

	t.Logf("wrote %d bytes for %d bytes of input; %v", written, size, peak)

	if peak.Used() > limit {
		t.Errorf("writer used %d bytes of live heap, want at most %d; %v", peak.Used(), limit, peak)
	}

	want := sha256.New()
	if _, err := io.Copy(want, memtest.Pattern(size)); err != nil {
		t.Fatal(err)
	}

	var names []string

	peak = memtest.PeakHeap(limit, func() {
		werr = cab.Walk(out, written, func(file cab.File, data io.Reader) error {
			names = append(names, file.Name)

			if file.Name != "fil_big" {
				return nil
			}

			got := sha256.New()

			copied, err := io.Copy(got, data)
			if err != nil {
				return err
			}

			if copied != size || !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
				t.Errorf("fil_big read back %d bytes with a different digest", copied)
			}

			return nil
		})
	})

	if werr != nil || len(names) != 2 {
		t.Fatalf("walk: %v, %v", werr, names)
	}

	t.Logf("read back; %v", peak)

	if peak.Used() > limit {
		t.Errorf("reader used %d bytes of live heap, want at most %d; %v", peak.Used(), limit, peak)
	}
}
