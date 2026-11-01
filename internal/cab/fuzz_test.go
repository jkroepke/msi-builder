package cab_test

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/internal/cab"
)

func seedCabinets(f *testing.F) {
	f.Helper()

	when := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)

	for _, comp := range []cab.Compression{cab.None, cab.MSZIP, cab.LZX} {
		raw, err := cab.Write([]cab.File{
			{Name: "a.txt", Data: []byte("hello"), Time: when},
			{Name: "b.bin", Data: bytes.Repeat([]byte("0123456789"), 7000), Time: when},
			{Name: "empty", Time: when},
			{Name: `dir\x.txt`, Data: []byte("in a folder"), Time: when},
		}, cab.Options{Compression: comp})
		if err != nil {
			f.Fatal(err)
		}

		f.Add(raw)
	}
}

// FuzzRead feeds corrupted cabinets to Read, Walk and Open. They must not
// panic, Walk must fail whenever Read fails and otherwise serve the same
// files, and every file the fs.FS view lists must read back with the bytes
// Read returns.
func FuzzRead(f *testing.F) {
	seedCabinets(f)

	f.Fuzz(func(t *testing.T, raw []byte) {
		compareWalk(t, raw)

		fsys, err := cab.Open(raw)
		if err != nil {
			return
		}

		// Open is Read plus the fs.FS checks, so Read succeeds here.
		files, err := cab.Read(raw)
		if err != nil {
			t.Fatalf("Open succeeds, but Read fails: %v", err)
		}

		for _, file := range files {
			// The fs.FS view uses "/" for the cabinet's "\".
			name := path.Clean(strings.ReplaceAll(file.Name, `\`, "/"))

			got, err := fs.ReadFile(fsys, name)
			if err != nil {
				t.Fatalf("Read lists %q but the fs.FS cannot read it: %v", file.Name, err)
			}

			if !bytes.Equal(got, file.Data) {
				t.Fatalf("%q: the fs.FS returns %d bytes that differ from Read's %d", file.Name, len(got), len(file.Data))
			}
		}
	})
}

// compareWalk checks that Walk fails when Read fails, and that it serves the
// files Read returns when both succeed.
func compareWalk(t *testing.T, raw []byte) {
	t.Helper()

	files, readErr := cab.Read(raw)

	var walked []string

	walkErr := cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(file cab.File, data io.Reader) error {
		body, err := io.ReadAll(data)
		walked = append(walked, file.Name+"\x00"+string(body))

		return err
	})

	switch {
	case readErr != nil && walkErr == nil:
		t.Fatalf("Read fails (%v), but Walk succeeds", readErr)
	case readErr != nil || walkErr != nil:
		return
	}

	read := make([]string, 0, len(files))
	for _, file := range files {
		read = append(read, file.Name+"\x00"+string(file.Data))
	}

	slices.Sort(read)
	slices.Sort(walked)

	if !slices.Equal(read, walked) {
		t.Fatalf("Walk serves %d files that differ from Read's %d", len(walked), len(read))
	}
}

// FuzzRoundTrip writes fuzzed files and reads them back unchanged.
func FuzzRoundTrip(f *testing.F) {
	f.Add("a.txt", []byte("hello"), "b", make([]byte, 0), true, uint16(0x20))
	f.Add("x", bytes.Repeat([]byte{0}, 40000), "y", []byte("zz"), false, uint16(1))
	f.Add("ü", []byte("a"), "z", []byte("b"), true, uint16(0))
	f.Add("a", []byte("a"), "b", []byte("b"), false, uint16(0xFFFF))
	f.Add("\x8c", []byte("a"), "b", []byte("b"), true, uint16(0x20))

	when := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)

	f.Fuzz(func(t *testing.T, name1 string, data1 []byte, name2 string, data2 []byte, zip bool, attrs uint16) {
		comp := cab.None
		if zip {
			comp = cab.MSZIP
		}

		want := []cab.File{
			{Name: name1, Data: data1, Time: when, Attributes: attrs},
			{Name: name2, Data: data2, Time: when, Attributes: attrs},
		}

		raw, err := cab.Write(want, cab.Options{Compression: comp})
		if err != nil {
			return
		}

		out, err := cab.Read(raw)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}

		if len(out) != len(want) {
			t.Fatalf("read %d files, wrote %d", len(out), len(want))
		}

		for i := range want {
			if out[i].Name != want[i].Name {
				t.Fatalf("file %d: name %q, wrote %q", i, out[i].Name, want[i].Name)
			}

			if !bytes.Equal(out[i].Data, want[i].Data) {
				t.Fatalf("file %d: data differs", i)
			}

			wantAttrs := attrs &^ 0x80 // the UTF-8 name flag is the writer's own
			if attrs == 0 {
				wantAttrs = cab.AttrArchive
			}

			if out[i].Attributes != wantAttrs {
				t.Fatalf("file %d: attributes %#x, wrote %#x", i, out[i].Attributes, attrs)
			}

			if !out[i].Time.Equal(want[i].Time) {
				t.Fatalf("file %d: time %v, wrote %v", i, out[i].Time, want[i].Time)
			}
		}
	})
}
