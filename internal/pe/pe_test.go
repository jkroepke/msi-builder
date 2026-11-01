package pe //nolint:testpackage // the tests reach unexported encoders

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

func TestSyntheticPE(t *testing.T) {
	t.Parallel()

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "1.2.0.0", Languages: []int{1033, 1031, 1033}, Checksum: 0x1234})

	info, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if !info.HasVersion || info.FileVersion != "1.2.3.4" || info.ProductVersion != "1.2.0.0" {
		t.Errorf("version: %+v", info)
	}

	// Stored 1033,1031,1033: duplicates dropped, last first, as Windows
	// lists them (FILE-14).
	if info.Language() != "1031,1033" {
		t.Errorf("language %q", info.Language())
	}

	if !info.HasChecksum || !info.Is64Bit || info.Machine != MachineAMD64 {
		t.Errorf("header: %+v", info)
	}

	if !IsPE(bytes.NewReader(data)) {
		t.Error("IsPE false")
	}
}

func TestNoVersionResource(t *testing.T) {
	t.Parallel()

	data := testpe.Build(testpe.Options{Machine: MachineI386})

	info, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if info.HasVersion || info.FileVersion != "" || info.Language() != "" || info.HasChecksum {
		t.Errorf("%+v", info)
	}
}

func TestNoTranslations(t *testing.T) {
	t.Parallel()

	data := testpe.Build(testpe.Options{FileVersion: "0.32.0.5"})

	info, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if info.FileVersion != "0.32.0.5" || info.Language() != "" {
		t.Errorf("%+v", info)
	}
}

func TestNotPE(t *testing.T) {
	t.Parallel()

	for _, data := range [][]byte{nil, []byte("hello"), append([]byte("MZ"), make([]byte, 100)...)} {
		if _, err := Parse(bytes.NewReader(data)); !errors.Is(err, ErrNotPE) {
			t.Errorf("%q: %v", data, err)
		}

		if IsPE(bytes.NewReader(data)) {
			t.Errorf("%q: IsPE true", data)
		}
	}
}

// Offsets into a testpe image, derived from its fixed layout: headers are
// 0x400 bytes, .text takes one 0x200 file-aligned block and .rsrc follows.
const (
	testSectionTable = 0x80 + 24 + 240 // PE header, COFF header, optional header
	testRsrcSection  = testSectionTable + 40
	testRsrcRaw      = 0x400 + 0x200
	testRootEntry    = testRsrcRaw + 16 + 4 // offset field of the root directory's only entry
	testDataEntry    = testRsrcRaw + 72     // data entry: RVA, size
	testResourceDir  = 0x80 + 24 + 112 + 8*2
)

// corruptImage builds a synthetic image and overwrites one 32-bit header
// field, checking first that the layout constants still point where they
// claim to.
func corruptImage(t *testing.T, off int, value uint32) []byte {
	t.Helper()

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})

	if binary.LittleEndian.Uint32(data[testDataEntry:]) != 0x2000+88 {
		t.Fatalf("data entry RVA %#x: layout constants are stale", binary.LittleEndian.Uint32(data[testDataEntry:]))
	}

	if binary.LittleEndian.Uint32(data[testRootEntry:]) != 0x80000000|24 {
		t.Fatalf("root entry %#x: layout constants are stale", binary.LittleEndian.Uint32(data[testRootEntry:]))
	}

	if string(data[testRsrcSection:testRsrcSection+5]) != ".rsrc" {
		t.Fatal("section table offset is stale")
	}

	binary.LittleEndian.PutUint32(data[off:], value)

	return data
}

// TestCorruptSizes feeds the parser header fields whose 32-bit values wrap
// around when added to their offsets, or that point outside the file. Each
// must come back as an error, never as a panic or a huge allocation.
func TestCorruptSizes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		off   int
		value uint32
	}{
		"version data size short":    {testDataEntry + 4, 0x20},
		"version data RVA wraps":     {testDataEntry, 0xFFFFFFF0},
		"version data RVA elsewhere": {testDataEntry, 0x1000},
		"directory entry wraps int":  {testRootEntry, 0x80000000 | 0x7FFFFFF8},
		"directory entry past end":   {testRootEntry, 0x80000000 | 0x10000},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(bytes.NewReader(corruptImage(t, tc.off, tc.value))); err == nil {
				t.Error("corrupt image parsed without error")
			}
		})
	}
}

// TestOversizedSections patches section sizes that wrap around in 32 bits.
// The resource data still lies inside the file, so the parser may either
// read it or refuse the section, but it must not panic.
func TestOversizedSections(t *testing.T) {
	t.Parallel()

	for name, off := range map[string]int{"virtual size": testRsrcSection + 8, "raw size": testRsrcSection + 16} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := Parse(bytes.NewReader(corruptImage(t, off, 0xFFFFFFFF)))
			if err == nil && info.FileVersion != "1.2.3.4" {
				t.Errorf("parsed but lost the version: %+v", info)
			}
		})
	}
}

func TestIsDLL(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{})
	if IsDLL(bytes.NewReader(exe)) {
		t.Error("an EXE reads as a DLL")
	}

	// COFF Characteristics: PE offset + signature + 18 bytes; 0x2000 is
	// IMAGE_FILE_DLL.
	dll := slices.Clone(exe)
	off := int(binary.LittleEndian.Uint32(dll[0x3C:]))
	dll[off+4+18+1] |= 0x20

	if !IsDLL(bytes.NewReader(dll)) {
		t.Error("a DLL does not read as one")
	}

	if IsDLL(bytes.NewReader([]byte("MZ"))) {
		t.Error("a truncated file reads as a DLL")
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r    io.ReaderAt
	read int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += int64(n)

	return n, err
}

// TestLargeResourceRange parses a PE whose resource directory covers a
// 32 MiB section: only the directory records and the version resource are
// read, not the whole range.
func TestLargeResourceRange(t *testing.T) {
	t.Parallel()

	const padding = 32 << 20

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}, Padding: padding})
	size := uint32(len(data) - testRsrcRaw) //nolint:gosec // 32 MiB

	if string(data[testRsrcSection:testRsrcSection+5]) != ".rsrc" {
		t.Fatal("section table offset is stale")
	}

	binary.LittleEndian.PutUint32(data[testRsrcSection+8:], size)  // virtual size
	binary.LittleEndian.PutUint32(data[testRsrcSection+16:], size) // raw size
	binary.LittleEndian.PutUint32(data[testResourceDir+4:], size)

	rdr := &countingReader{r: bytes.NewReader(data)}

	info, err := Parse(rdr)
	if err != nil {
		t.Fatal(err)
	}

	if info.FileVersion != "1.2.3.4" {
		t.Errorf("version %q", info.FileVersion)
	}

	if rdr.read > 64<<10 {
		t.Errorf("read %d bytes of a %d-byte resource directory", rdr.read, size)
	}
}

// TestRawDataRoundedToFileAlignment ends the resource section's raw data
// before and right after the start of the version data (at 0x58). Windows
// rounds SizeOfRawData up to FileAlignment (0x200 here), so the version is
// read either way, but it drops the languages when the data starts past
// the unrounded raw size (FILE-14).
func TestRawDataRoundedToFileAlignment(t *testing.T) {
	t.Parallel()

	for raw, lang := range map[uint32]string{1: "", 0x58: "", 0x60: "1033", 0x200: "1033"} {
		info, err := Parse(bytes.NewReader(corruptImage(t, testRsrcSection+16, raw)))
		if err != nil {
			t.Fatal(err)
		}

		if info.FileVersion != "1.2.3.4" || info.Language() != lang {
			t.Errorf("raw size %#x: %+v, want language %q", raw, info, lang)
		}
	}
}

// TestResourceBeyondRawData clears the SizeOfRawData of the resource
// section. Its VirtualSize still covers the directory, but the loader maps
// zeros there, so the version bytes that stay in the file must not be read.
func TestResourceBeyondRawData(t *testing.T) {
	t.Parallel()

	info, err := Parse(bytes.NewReader(corruptImage(t, testRsrcSection+16, 0)))
	if err != nil {
		t.Fatal(err)
	}

	if info.HasVersion || info.FileVersion != "" || info.Language() != "" {
		t.Errorf("read past the section's raw data: %+v", info)
	}
}

// TestTranslationBoundedByVar shrinks the Translation Var block to one
// language while its value length still claims two. The value runs past
// the block, so no language is reported, as by MsiGetFileVersionW.
func TestTranslationBoundedByVar(t *testing.T) {
	t.Parallel()

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033, 1031}})

	translation := blockOffset(t, data, "Translation")
	if binary.LittleEndian.Uint16(data[translation+2:]) != 8 {
		t.Fatal("Translation value length is not two languages")
	}

	// Header 6, key 24 with terminator, padded to 32, one language: 36.
	binary.LittleEndian.PutUint16(data[translation:], 36)

	info, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if info.FileVersion != "1.2.3.4" || info.Language() != "" {
		t.Errorf("version %q language %q, want 1.2.3.4 and none", info.FileVersion, info.Language())
	}
}

// TestOversizedResourceDirectory declares a resource directory far larger
// than its section. Windows still reads the version through the records it
// visits, which all lie in the section, so the parser must too.
func TestOversizedResourceDirectory(t *testing.T) {
	t.Parallel()

	for name, size := range map[string]uint32{"wraps": 0xFFFFF000, "huge": 0x7FFFFFFF} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := Parse(bytes.NewReader(corruptImage(t, testResourceDir+4, size)))
			if err != nil {
				t.Fatal(err)
			}

			if info.FileVersion != "1.2.3.4" || info.Language() != "1033" {
				t.Errorf("%+v", info)
			}
		})
	}
}

// TestNoResourceSlot declares fewer data directories than the resource
// slot needs. Windows ignores the slot's bytes, so there is no version.
func TestNoResourceSlot(t *testing.T) {
	t.Parallel()

	for _, count := range []uint32{0, 2} {
		info, err := Parse(bytes.NewReader(corruptImage(t, testResourceDir-20, count)))
		if err != nil {
			t.Fatal(err)
		}

		if info.HasVersion || info.FileVersion != "" {
			t.Errorf("NumberOfRvaAndSizes %d: version read from an undeclared slot: %+v", count, info)
		}
	}
}

// blockOffset returns the file offset of the version block whose key is
// key: its 6-byte header precedes the UTF-16 key.
func blockOffset(t *testing.T, data []byte, key string) int {
	t.Helper()

	units := utf16.Encode([]rune(key))
	encoded := make([]byte, 0, 2*len(units))

	for _, u := range units {
		encoded = binary.LittleEndian.AppendUint16(encoded, u)
	}

	off := bytes.Index(data, encoded) - 6
	if off < 0 {
		t.Fatalf("version block %q not found", key)
	}

	return off
}

// TestInvalidVersionHeader corrupts the VS_VERSIONINFO header in ways
// Windows refuses. The parser must not report a version, so the builder's
// unversioned-executable check still applies.
func TestInvalidVersionHeader(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		field int
		value uint16
	}{
		"value runs past the block":  {2, 0xFFFF},
		"block longer than its data": {0, 0xFFFF},
		"text type":                  {4, 1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
			binary.LittleEndian.PutUint16(data[blockOffset(t, data, "VS_VERSION_INFO")+tc.field:], tc.value)

			info, err := Parse(bytes.NewReader(data))
			if err == nil && info.HasVersion {
				t.Errorf("invalid header accepted: %+v", info)
			}
		})
	}
}

// TestShortOptionalHeader declares a PE32+ optional header long enough for
// the checksum but too short for NumberOfRvaAndSizes and the directories.
func TestShortOptionalHeader(t *testing.T) {
	t.Parallel()

	for _, size := range []uint16{96, 100, 111} {
		data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4"})
		binary.LittleEndian.PutUint16(data[0x80+4+16:], size) // SizeOfOptionalHeader

		info, err := Parse(bytes.NewReader(data))
		if err == nil && info.HasVersion {
			t.Errorf("size %d: version read without a resource directory: %+v", size, info)
		}
	}
}

// movedVersion builds an image whose version resource data lies at RVA
// 0x2200, past the resource section's 0x200 bytes of raw data but inside
// its VirtualSize of 0x400, with the bytes in the file right after the
// raw data. rawSize sets the section's SizeOfRawData.
func movedVersion(t *testing.T, rawSize uint32) []byte {
	t.Helper()

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}, Padding: 0x200})

	src := blockOffset(t, data, "VS_VERSION_INFO")
	size := int(binary.LittleEndian.Uint32(data[testDataEntry+4:]))
	copy(data[testRsrcRaw+0x200:], data[src:src+size])
	clear(data[src : src+size])

	binary.LittleEndian.PutUint32(data[testDataEntry:], 0x2200)
	binary.LittleEndian.PutUint32(data[testRsrcSection+8:], 0x400) // VirtualSize
	binary.LittleEndian.PutUint32(data[testRsrcSection+16:], rawSize)

	return data
}

// TestRecordPastRawData reads a version resource that lies past the
// section's raw data rounded to FileAlignment: it reads as zeros, so there
// is no version even though the file holds its bytes. With the raw data
// covering it, the version is read.
func TestRecordPastRawData(t *testing.T) {
	t.Parallel()

	info, err := Parse(bytes.NewReader(movedVersion(t, 0x200)))
	if err == nil && info.HasVersion {
		t.Errorf("read past the raw data: %+v", info)
	}

	info, err = Parse(bytes.NewReader(movedVersion(t, 0x400)))
	if err != nil || info.FileVersion != "1.2.3.4" || info.Language() != "1033" {
		t.Errorf("covered by the raw data: %+v, %v", info, err)
	}
}

// TestInvalidFileAlignment falls back to the unrounded raw size when
// FileAlignment is not a power of two from 512 to 64 KiB.
func TestInvalidFileAlignment(t *testing.T) {
	t.Parallel()

	data := corruptImage(t, testRsrcSection+16, 1)
	binary.LittleEndian.PutUint32(data[0x80+24+36:], 0) // FileAlignment

	info, err := Parse(bytes.NewReader(data))
	if err == nil && info.HasVersion {
		t.Errorf("one raw byte gave a version: %+v", info)
	}
}

// TestOversizedVersionData declares more version data than the section
// holds, and a resource directory of size 0. Windows reads only the block
// VS_VERSIONINFO declares and ignores the directory size (FILE-14).
func TestOversizedVersionData(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		off   int
		value uint32
	}{
		"version data size 512":   {testDataEntry + 4, 512},
		"version data size huge":  {testDataEntry + 4, 0x7FFFFFFF},
		"version data size wraps": {testDataEntry + 4, 0xFFFFFFF8},
		"directory size 0":        {testResourceDir + 4, 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := Parse(bytes.NewReader(corruptImage(t, tc.off, tc.value)))
			if err != nil {
				t.Fatal(err)
			}

			if info.FileVersion != "1.2.3.4" || info.Language() != "1033" {
				t.Errorf("%+v", info)
			}
		})
	}
}

// resEntry is one RT_VERSION resource for versionResources.
type resEntry struct {
	id, lang  uint32
	version   string
	languages []int
	blob      []byte // VS_VERSIONINFO bytes to use instead of building them
}

// versionResources builds an image whose resource section holds the given
// RT_VERSION resources, sorted by ID and then language as a resource
// compiler writes them.
func versionResources(t *testing.T, entries []resEntry) []byte {
	t.Helper()

	order := binary.LittleEndian

	slices.SortFunc(entries, func(a, b resEntry) int {
		if a.id != b.id {
			return int(a.id) - int(b.id)
		}

		return int(a.lang) - int(b.lang)
	})

	var ids []uint32
	for _, e := range entries {
		if len(ids) == 0 || ids[len(ids)-1] != e.id {
			ids = append(ids, e.id)
		}
	}

	dirHead := func(n int) []byte {
		d := make([]byte, 16)
		order.PutUint16(d[14:], uint16(n)) //nolint:gosec // a few entries

		return d
	}

	// Layout: root, name directory, one language directory per ID, the
	// data entries, then the data.
	rootLen := 16 + 8
	nameLen := 16 + 8*len(ids)
	langOff := make(map[uint32]int)
	off := rootLen + nameLen

	for _, id := range ids {
		langOff[id] = off
		count := 0

		for _, e := range entries {
			if e.id == id {
				count++
			}
		}

		off += 16 + 8*count
	}

	entryOff := off
	dataOff := entryOff + 16*len(entries)

	rsrc := dirHead(1)
	rsrc = order.AppendUint32(rsrc, 16)
	rsrc = order.AppendUint32(rsrc, 0x80000000|uint32(rootLen))
	rsrc = append(rsrc, dirHead(len(ids))...)

	for _, id := range ids {
		rsrc = order.AppendUint32(rsrc, id)
		rsrc = order.AppendUint32(rsrc, 0x80000000|uint32(langOff[id])) //nolint:gosec // small
	}

	blobs := make([][]byte, 0, len(entries))

	for _, entry := range entries {
		if entry.blob != nil {
			blobs = append(blobs, entry.blob)

			continue
		}

		img := testpe.Build(testpe.Options{FileVersion: entry.version, Languages: entry.languages})
		start := blockOffset(t, img, "VS_VERSION_INFO")
		blobs = append(blobs, img[start:start+int(order.Uint32(img[testDataEntry+4:]))])
	}

	for _, id := range ids {
		var langs []int

		for i, e := range entries {
			if e.id == id {
				langs = append(langs, i)
			}
		}

		rsrc = append(rsrc, dirHead(len(langs))...)
		for _, i := range langs {
			rsrc = order.AppendUint32(rsrc, entries[i].lang)
			rsrc = order.AppendUint32(rsrc, uint32(entryOff+16*i)) //nolint:gosec // small
		}
	}

	// Identical blobs are stored once, and their data entries share it.
	pos := dataOff
	offsets := make(map[string]int)

	var unique [][]byte

	for _, blob := range blobs {
		off, ok := offsets[string(blob)]
		if !ok {
			off = pos
			offsets[string(blob)] = off
			unique = append(unique, blob)
			pos = align4(pos + len(blob))
		}

		rsrc = order.AppendUint32(rsrc, 0x2000+uint32(off)) //nolint:gosec // small
		rsrc = order.AppendUint32(rsrc, uint32(len(blob)))  //nolint:gosec // small
		rsrc = append(rsrc, make([]byte, 8)...)
	}

	for _, b := range unique {
		rsrc = append(rsrc, b...)
		for len(rsrc)%4 != 0 {
			rsrc = append(rsrc, 0)
		}
	}

	// The section's raw size: 0x200 plus the padding, at least 0x4000.
	area := max(0x4000, (len(rsrc)+0x1FF)&^0x1FF)
	data := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Padding: area - 0x200})

	clear(data[testRsrcRaw : testRsrcRaw+area])
	copy(data[testRsrcRaw:], rsrc)
	order.PutUint32(data[0x80+24+56:], 0x2000+uint32(area))      //nolint:gosec // small
	order.PutUint32(data[testRsrcSection+8:], uint32(area))      //nolint:gosec // small
	order.PutUint32(data[testRsrcSection+16:], uint32(area))     //nolint:gosec // small
	order.PutUint32(data[testResourceDir+4:], uint32(len(rsrc))) //nolint:gosec // small

	return data
}

// TestVersionResourceSelection reads RT_VERSION ID 1 only, and refuses
// language entries that disagree, since Windows picks one by the user's
// UI language (FILE-14).
func TestVersionResourceSelection(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		entries []resEntry
		version string
		lang    string
		// fails is a part of the expected error; "" expects none.
		fails string
	}{
		"ID 0 and ID 1": {
			entries: []resEntry{{0, 1033, "1.2.3.4", []int{1033}, nil}, {1, 1033, "9.8.7.6", []int{1033}, nil}},
			version: "9.8.7.6", lang: "1033",
		},
		"ID 2 only": {entries: []resEntry{{2, 1033, "1.2.3.4", []int{1033}, nil}}},
		"two languages agree": {
			entries: []resEntry{{1, 1031, "1.2.3.4", []int{1031, 1033}, nil}, {1, 1033, "1.2.3.4", []int{1031, 1033}, nil}},
			version: "1.2.3.4", lang: "1033,1031",
		},
		"same version, own translations": {
			entries: []resEntry{{1, 1031, "1.2.3.4", []int{1031}, nil}, {1, 1033, "1.2.3.4", []int{1033}, nil}},
			version: "1.2.3.4", lang: "1033,1031",
		},
		"neutral entry first": {
			entries: []resEntry{{1, 0, "1.2.3.4", []int{1031}, nil}, {1, 1033, "1.2.3.4", []int{1033}, nil}},
			version: "1.2.3.4", lang: "1033,1031",
		},
		"65 languages": {entries: manyLanguages(65), version: "1.2.3.4", lang: "1033"},
		"product versions differ": {
			entries: []resEntry{
				{1, 1031, "", nil, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "1.0.0.0", Languages: []int{1031}})},
				{1, 1033, "", nil, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "2.0.0.0", Languages: []int{1033}})},
			},
			version: "1.2.3.4", lang: "1033,1031",
		},
		"version data within the budget": {entries: hungryLanguages(t, 256), version: "1.2.3.4", lang: "1033"},
		"too much version data":          {entries: hungryLanguages(t, 257), fails: "too much data"},
		"other language unreadable": {
			entries: []resEntry{
				{1, 1031, "", nil, binaryRoot(t, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1031}}))},
				{1, 1033, "1.2.3.4", []int{1033}, nil},
			},
			fails: "not binary",
		},
		"no fixed file info": {
			entries: []resEntry{{1, 1033, "", nil, noFixedInfo(t)}},
		},
		"one language without fixed file info": {
			entries: []resEntry{{1, 1031, "", nil, noFixedInfo(t)}, {1, 1033, "1.2.3.4", []int{1033}, nil}},
			fails:   "fixed file info",
		},
		"two languages disagree": {
			entries: []resEntry{{1, 1031, "1.2.3.4", []int{1031}, nil}, {1, 1033, "9.8.7.6", []int{1033}, nil}},
			fails:   "disagree",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			info, err := Parse(bytes.NewReader(versionResources(t, tc.entries)))

			switch {
			case tc.fails != "":
				if err == nil || !strings.Contains(err.Error(), tc.fails) {
					t.Errorf("got %+v, %v; want an error with %q", info, err, tc.fails)
				}
			case err != nil:
				t.Fatal(err)
			case info.FileVersion != tc.version || info.Language() != tc.lang:
				t.Errorf("version %q language %q, want %q %q", info.FileVersion, info.Language(), tc.version, tc.lang)
			}
		})
	}
}

// partialTranslation sets the Translation value length of a two-language
// image to n bytes, inside its Var block.
func partialTranslation(t *testing.T, n uint16) []byte {
	t.Helper()

	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033, 1031}})
	binary.LittleEndian.PutUint16(data[blockOffset(t, data, "Translation")+2:], n)

	return data
}

// TestPartialTranslation counts a partial last Translation entry once its
// language word fits, as msi.dll 5.0.26100.33648 did for value lengths 4
// to 7; 5.0.26100.1742 also counted length 5 as two (FILE-14).
func TestPartialTranslation(t *testing.T) {
	t.Parallel()

	for n, want := range map[uint16]string{4: "1033", 5: "1033", 6: "1031,1033", 7: "1031,1033", 8: "1031,1033"} {
		info, err := Parse(bytes.NewReader(partialTranslation(t, n)))
		if err != nil || info.Language() != want {
			t.Errorf("value length %d: %+v %v, want %q", n, info, err, want)
		}
	}
}

// twoTranslations returns a VS_VERSIONINFO for 1.2.3.4 with a Translation
// of 1033 followed by a second one of 1031: a second Var in the same
// VarFileInfo, or a second VarFileInfo block.
func twoTranslations(t *testing.T, sameBlock bool) []byte {
	t.Helper()

	order := binary.LittleEndian
	img := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
	start := blockOffset(t, img, "VS_VERSION_INFO")
	blob := slices.Clone(img[start : start+int(order.Uint32(img[testDataEntry+4:]))])

	vfi := blockOffset(t, blob, "VarFileInfo")
	vfiLen := int(order.Uint16(blob[vfi:]))
	tr := blockOffset(t, blob, "Translation")
	trLen := int(order.Uint16(blob[tr:]))

	var extra []byte
	if sameBlock {
		extra = slices.Clone(blob[tr : tr+trLen])
	} else {
		extra = slices.Clone(blob[vfi : vfi+vfiLen])
	}

	// The copied block's last 4 bytes are its only language.
	order.PutUint16(extra[len(extra)-4:], 1031)

	if sameBlock {
		order.PutUint16(blob[vfi:], uint16(vfiLen+len(extra))) //nolint:gosec // small
	}

	blob = append(blob, extra...)
	order.PutUint16(blob, uint16(len(blob))) //nolint:gosec // small

	return blob
}

// TestFirstTranslationOnly takes the languages of the first Translation
// in the first VarFileInfo, as Windows did (FILE-14).
func TestFirstTranslationOnly(t *testing.T) {
	t.Parallel()

	for _, sameBlock := range []bool{true, false} {
		data := versionResources(t, []resEntry{{id: 1, lang: 1033, blob: twoTranslations(t, sameBlock)}})

		info, err := Parse(bytes.NewReader(data))
		if err != nil || info.FileVersion != "1.2.3.4" || info.Language() != "1033" {
			t.Errorf("same block %v: %+v %v, want 1.2.3.4 and 1033", sameBlock, info, err)
		}
	}
}

// headerResources moves the resource directory and the version data of a
// testpe image into its header area, at RVA and file offset 0x200.
func headerResources(t *testing.T) []byte {
	t.Helper()

	order := binary.LittleEndian
	data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
	size := int(order.Uint32(data[testResourceDir+4:]))

	if !bytes.Equal(data[0x200:0x400], make([]byte, 0x200)) || size > 0x200 {
		t.Fatal("header area 0x200-0x400 is not free")
	}

	copy(data[0x200:], data[testRsrcRaw:testRsrcRaw+size])
	order.PutUint32(data[0x200+72:], 0x200+88) // data entry RVA
	order.PutUint32(data[testResourceDir:], 0x200)

	return data
}

// TestResourcesInHeaders reads a version resource that lies in the
// headers, which Windows maps at RVA 0: the version, but no languages,
// as Windows reported (FILE-14).
func TestResourcesInHeaders(t *testing.T) {
	t.Parallel()

	info, err := Parse(bytes.NewReader(headerResources(t)))
	if err != nil || info.FileVersion != "1.2.3.4" || info.Language() != "" {
		t.Errorf("%+v %v", info, err)
	}
}

// TestNamedEntryPartition counts a directory's only entry as named at each
// level of the version resource: it is then never matched by its ID, so
// there is no version, as on Windows (FILE-14).
func TestNamedEntryPartition(t *testing.T) {
	t.Parallel()

	for _, dir := range []int{0, 24, 48} {
		// NumberOfNamedEntries 1, NumberOfIdEntries 0.
		info, err := Parse(bytes.NewReader(corruptImage(t, testRsrcRaw+dir+12, 1)))
		if err == nil && info.HasVersion {
			t.Errorf("directory at %d: entry counted as named matched by ID: %+v", dir, info)
		}
	}
}

// manyLanguages returns n language entries of version 1.2.3.4, each
// listing 1033.
func manyLanguages(n int) []resEntry {
	entries := make([]resEntry, 0, n)
	for i := range n {
		entries = append(entries, resEntry{1, uint32(1024 + i), "1.2.3.4", []int{1033}, nil})
	}

	return entries
}

// blobOf returns the VS_VERSIONINFO bytes testpe writes for opts.
func blobOf(t *testing.T, opts testpe.Options) []byte {
	t.Helper()

	img := testpe.Build(opts)
	start := blockOffset(t, img, "VS_VERSION_INFO")

	return img[start : start+int(binary.LittleEndian.Uint32(img[testDataEntry+4:]))]
}

// hungryLanguages returns count language entries with distinct, valid version
// blocks of 65532 bytes each, near the largest a block can be, so the
// parser reads all of them: 256 such blocks fit maxVersionData, 257 do not.
func hungryLanguages(t *testing.T, count int) []resEntry {
	t.Helper()

	const size = 0xFFFC

	base := blobOf(t, testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
	entries := make([]resEntry, 0, count)

	for i := range count {
		blob := make([]byte, size)
		copy(blob, base)
		binary.LittleEndian.PutUint16(blob, size)
		// Distinct padding keeps versionResources from sharing the blocks.
		binary.LittleEndian.PutUint16(blob[size-2:], uint16(i))

		entries = append(entries, resEntry{1, uint32(1024 + i), "", nil, blob})
	}

	return entries
}

// noFixedInfo returns a version block whose root value is empty
// (wValueLength 0), so it holds no VS_FIXEDFILEINFO.
func noFixedInfo(t *testing.T) []byte {
	t.Helper()

	blob := blobOf(t, testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
	binary.LittleEndian.PutUint16(blob[2:], 0)

	return blob
}

// TestSharedBlockShortEntry refuses a language entry whose data entry is
// too short for the block it shares with a readable one: a system that
// picks it reads no version.
func TestSharedBlockShortEntry(t *testing.T) {
	t.Parallel()

	entries := []resEntry{{1, 1033, "1.2.3.4", []int{1033}, nil}, {1, 2057, "1.2.3.4", []int{1033}, nil}}
	data := versionResources(t, entries)

	// versionResources stores the identical blocks once: root (24 bytes),
	// name directory (24) and language directory (32), then the second
	// data entry at 80+16, whose size follows its RVA.
	second := testRsrcRaw + 80 + 16
	if binary.LittleEndian.Uint32(data[second:]) != binary.LittleEndian.Uint32(data[second-16:]) {
		t.Fatal("the entries do not share a block")
	}

	if _, err := Parse(bytes.NewReader(data)); err != nil {
		t.Fatalf("shared block refused: %v", err)
	}

	binary.LittleEndian.PutUint32(data[second+4:], 4)

	if info, err := Parse(bytes.NewReader(data)); err == nil {
		t.Errorf("short entry accepted: %+v", info)
	}
}

// binaryRoot returns a copy of a version block whose root claims text
// (wType 1), which Windows refuses.
func binaryRoot(t *testing.T, blob []byte) []byte {
	t.Helper()

	blob = slices.Clone(blob)
	binary.LittleEndian.PutUint16(blob[4:], 1)

	return blob
}

// TestManySectionsAndEntries refuses a file whose language entries times
// sections pass maxEntryScans: each entry costs a few section scans, and
// 65535 of both took 21 s to parse before the bound.
func TestManySectionsAndEntries(t *testing.T) {
	t.Parallel()

	const sections = 0xFFFF // plus the header pseudo-section

	tests := map[string]struct {
		entries int
		fails   bool
	}{
		"within the bound": {entries: maxEntryScans / (sections + 1)},
		"past the bound":   {entries: maxEntryScans/(sections+1) + 1, fails: true},
		// 65535 × 65536 wraps a 32-bit int to a negative product.
		"most entries": {entries: 0xFFFF, fails: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entries := make([]resEntry, tc.entries)
			for i := range entries {
				entries[i] = resEntry{1, uint32(i), "1.2.3.4", []int{1033}, nil}
			}

			// The section table grows over the raw data: the sections past
			// the first two read as garbage or zeros, which no lookup
			// matches before the real ones.
			data := versionResources(t, entries)
			data = append(data, make([]byte, testSectionTable+40*sections)...)
			binary.LittleEndian.PutUint16(data[0x80+6:], sections)

			info, err := Parse(bytes.NewReader(data))

			switch {
			case tc.fails:
				if err == nil || !strings.Contains(err.Error(), "too many language entries") {
					t.Errorf("got %+v, %v; want too many language entries", info, err)
				}
			case err != nil:
				t.Fatal(err)
			case info.FileVersion != "1.2.3.4":
				t.Errorf("version %q, want 1.2.3.4", info.FileVersion)
			}
		})
	}
}

// TestRepointedBlockReadOnce reads a block that many language entries
// share once, also when their data entries declare different sizes: 256
// entries on one block of 15000 languages allocated 326 MB before.
func TestRepointedBlockReadOnce(t *testing.T) { //nolint:paralleltest // measures the allocations of the process
	const count = 256

	langs := make([]int, 15000)
	for i := range langs {
		langs[i] = 1033
	}

	entries := make([]resEntry, count)
	for i := range entries {
		entries[i] = resEntry{1, uint32(1024 + i), "1.2.3.4", langs, nil}
	}

	data := versionResources(t, entries)

	// The data entries follow the root (24 bytes), the name directory (24)
	// and the language directory; each gets a size of its own.
	entryOff := testRsrcRaw + 24 + 24 + 16 + 8*count
	for i := range count {
		e := entryOff + 16*i
		binary.LittleEndian.PutUint32(data[e+4:], binary.LittleEndian.Uint32(data[e+4:])+uint32(i))
	}

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	info, err := Parse(bytes.NewReader(data))

	runtime.ReadMemStats(&after)

	if err != nil || info.Language() != "1033" {
		t.Fatalf("got %+v, %v; want language 1033", info, err)
	}

	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Errorf("parse allocated %d MB", alloc>>20)
	}
}
