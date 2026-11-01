//go:build windows && (amd64 || arm64)

package cab_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cab"
)

// TestNativeFDIRejectsBadMSZIPStreams checks that cabinet.dll refuses an
// MSZIP block whose DEFLATE stream yields more than cbUncomp bytes or lacks
// its final block, which cab.Read refuses too.
func TestNativeFDIRejectsBadMSZIPStreams(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	hello, err := cab.MSZIPBlock([]byte("hello"), nil, 9)
	if err != nil {
		t.Fatal(err)
	}

	// A stored DEFLATE block of "hello" with BFINAL clear.
	unfinished := append([]byte{'C', 'K', 0, 5, 0, 0xFA, 0xFF}, "hello"...)

	for _, tc := range []struct {
		name  string
		block cab.RawBlock
		ok    bool
	}{
		{"exact", cab.RawBlock{Payload: hello, Uncomp: 5}, true},
		{"longer stream", cab.RawBlock{Payload: hello, Uncomp: 1}, false},
		{"no final block", cab.RawBlock{Payload: unfinished, Uncomp: 5}, false},
	} {
		raw := cab.RawCabinet(
			[]cab.RawFolder{{Type: cab.MSZIP, Blocks: []cab.RawBlock{tc.block}}},
			[]cab.RawFile{{Name: []byte("a"), Size: uint32(tc.block.Uncomp), Attr: cab.AttrArchive}}, //nolint:gosec // small
		)

		_, fdiErr := fdiExtract(t, raw)
		_, readErr := cab.Read(raw)

		t.Logf("%s: FDI %v, cab.Read %v", tc.name, fdiErr, readErr)

		if (fdiErr == nil) != tc.ok || (readErr == nil) != tc.ok {
			t.Errorf("%s: FDI error %v, cab.Read error %v; want success %v from both", tc.name, fdiErr, readErr, tc.ok)
		}
	}
}

// TestNativeFDIEmptyFolderOffset checks that cabinet.dll extracts a cabinet
// whose empty first folder has coffCabStart 0, and that cab.Read and cab.Walk
// read it the same way.
func TestNativeFDIEmptyFolderOffset(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	payload := bytes.Repeat([]byte("data after an empty folder\r\n"), 100)
	raw := cab.RawCabinet([]cab.RawFolder{
		{Type: cab.MSZIP},
		{Type: cab.None, Blocks: []cab.RawBlock{{Payload: payload, Uncomp: len(payload)}}},
	}, []cab.RawFile{
		{Name: []byte("empty"), Folder: 0, Attr: cab.AttrArchive},
		{Name: []byte("data"), Size: uint32(len(payload)), Folder: 1, Attr: cab.AttrArchive}, //nolint:gosec // small
	})

	got, err := fdiExtract(t, raw)
	if err != nil {
		t.Fatalf("FDI: %v", err)
	}

	files, err := cab.Read(raw)
	if err != nil {
		t.Fatalf("cab.Read: %v", err)
	}

	if len(got) != len(files) {
		t.Fatalf("FDI extracted %d files, cab.Read %d", len(got), len(files))
	}

	for i := range got {
		if got[i].Name != files[i].Name || !bytes.Equal(got[i].Data, files[i].Data) {
			t.Errorf("file %d: FDI %q (%d bytes), cab.Read %q (%d bytes)", i, got[i].Name, len(got[i].Data), files[i].Name, len(files[i].Data))
		}
	}

	err = cab.Walk(bytes.NewReader(raw), int64(len(raw)), func(cab.File, io.Reader) error { return nil })
	if err != nil {
		t.Errorf("cab.Walk: %v", err)
	}
}

// TestNativeReadMakecabLegacyName checks that a name makecab.exe stores in
// the ANSI code page, without the UTF-8 attribute, reads as expand.exe
// extracts it on a machine whose ANSI code page is cab.LegacyCodePage.
func TestNativeReadMakecabLegacyName(t *testing.T) { //nolint:paralleltest // makecab runs one at a time
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	acp, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetACP").Call()
	if int(acp) != cab.LegacyCodePage {
		// The CI runners use 1252; a skip there would leave the spec's
		// statement unobserved while the run stays green.
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("the CI runner's ANSI code page is %d, not %d", acp, cab.LegacyCodePage)
		}

		t.Skipf("the ANSI code page is %d, not %d", acp, cab.LegacyCodePage)
	}

	const name = "résumé.txt"

	dir := t.TempDir()
	// A second file makes expand.exe use the names in the cabinet.
	for file, data := range map[string]string{name: "legacy\r\n", "second.txt": "second\r\n"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// makecab reads the directive file as ANSI: é is E9 in code page 1252.
	ddf := ".OPTION EXPLICIT\r\n.Set CabinetNameTemplate=out.cab\r\n.Set DiskDirectoryTemplate=.\r\n" +
		".Set Cabinet=on\r\n.Set Compress=on\r\n.Set CompressionType=MSZIP\r\n" +
		".Set RptFileName=nul\r\n.Set InfFileName=nul\r\n\"r\xE9sum\xE9.txt\" r\xE9sum\xE9.txt\r\nsecond.txt second.txt\r\n"
	if err := os.WriteFile(filepath.Join(dir, "cab.ddf"), []byte(ddf), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), filepath.Join(os.Getenv("SystemRoot"), "System32", "makecab.exe"), "/F", "cab.ddf") //nolint:gosec // fixed Windows tool
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makecab.exe: %v\n%s", err, output)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "out.cab"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(raw, []byte("r\xE9sum\xE9.txt\x00")) {
		t.Fatal("makecab did not store the name as code page 1252 bytes")
	}

	files, err := cab.Read(raw)
	if err != nil || len(files) != 2 || files[0].Name != name || string(files[0].Data) != "legacy\r\n" {
		t.Fatalf("cab.Read = %+v, %v; want %q first", files, err, name)
	}

	if _, err := cab.Open(raw); err != nil {
		t.Errorf("cab.Open: %v", err)
	}

	expanded, err := expandExtract(t, raw, len(name))
	if err != nil {
		t.Fatal(err)
	}

	if data, ok := expanded[name]; !ok || string(data) != "legacy\r\n" {
		t.Errorf("expand.exe wrote %q, want %q with the file's content", keys(expanded), name)
	}
}
