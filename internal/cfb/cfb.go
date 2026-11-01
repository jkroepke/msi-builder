// Package cfb writes and reads Compound File Binary (CFB) containers, the
// structured storage format that every .msi file uses.
//
// The writer fixes every free choice the way docs/spec/MSISPEC.md section 2
// describes: siblings are sorted with Compare, the directory tree is a
// balanced red-black tree, SIDs follow the sorted order, sectors are laid out
// as FAT, DIFAT, directory, MiniFAT, mini stream and then the large streams,
// timestamps and state bits are zero, and an empty stream starts at
// ENDOFCHAIN. Identical input gives identical bytes.
//
// Root-level streams and root-level storages that hold streams are
// supported: a package with embedded transforms needs one level of storages.
//
// WriteTo streams: it needs every stream's size up front, lays the file out
// from the sizes alone and then copies the large streams through, so a
// cabinet of any size passes through a few sectors of memory.
package cfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Special sector values.
const (
	freeSect   = 0xFFFFFFFF
	endOfChain = 0xFFFFFFFE
	fatSect    = 0xFFFFFFFD
	difSect    = 0xFFFFFFFC
	noStream   = 0xFFFFFFFF

	miniCutoff     = 4096
	miniSectorSize = 64
	entrySize      = 128
	maxNameUnits   = 31
	// maxFileSize is the largest file the writer produces. Windows Installer
	// cannot install a package above 2 GB, so the v4 range-lock sector is
	// never needed.
	maxFileSize = 0x7FFFFE00
)

var errFileTooLarge = errors.New("cfb: file would exceed 2 GB")

var signature = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1} //nolint:gochecknoglobals // lookup table

// CLSIDs of the root storage, stored little-endian.
var (
	// CLSIDMSI is {000C1084-0000-0000-C000-000000000046}: .msi, .msm and .pcp.
	CLSIDMSI = [16]byte{0x84, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} //nolint:gochecknoglobals // lookup table
	// CLSIDTransform is {000C1082-…}: .mst.
	CLSIDTransform = [16]byte{0x82, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} //nolint:gochecknoglobals // table
	// CLSIDPatch is {000C1086-…}: .msp.
	CLSIDPatch = [16]byte{0x86, 0x10, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} //nolint:gochecknoglobals // lookup table
)

// Stream is one named root-level stream. Name is 1 to 31 UTF-16 units and
// may contain the characters Windows Installer uses for encoded names
// (U+3800–U+4840) and the U+0005 prefix of system streams.
type Stream struct {
	Name string
	Data []byte
}

// Source is one named root-level stream whose bytes WriteTo copies through
// Open while it writes. Open is called once; the reader must deliver exactly
// Size bytes.
type Source struct {
	Open func() (io.ReadCloser, error)
	Name string
	Size int64
}

// Storage is one named root-level storage, such as a transform embedded in a
// package, holding streams. Its name follows the rules of a stream name.
type Storage struct {
	Name    string
	Streams []Source
	CLSID   [16]byte
}

// Layout reports where WriteTo put each stream.
type Layout struct {
	// Offsets maps each stream name to the file offset of its first byte;
	// a stream in a storage is named "storage/stream" (CFB names cannot
	// contain "/").
	// Every stream of this writer is contiguous, in the mini stream or in
	// consecutive sectors, so the bytes at Offsets[name] through
	// Offsets[name]+Size are the stream.
	Offsets map[string]int64
	// Size is the length of the file.
	Size int64
}

// Options control the container layout.
type Options struct {
	// Version is 3 (512-byte sectors) or 4 (4096-byte sectors). 0 means 4,
	// which is what the native writer emits (MSISPEC-RESULTS CFB-01).
	Version int
	// CLSID of the root storage. Zero means CLSIDMSI.
	CLSID [16]byte
	// AllBlack colours every directory entry black instead of building a
	// red-black tree. Both are valid; the engine ignores colours.
	AllBlack bool
}

// EncodeName converts a name into UTF-16 units and checks the CFB rules.
func EncodeName(name string) ([]uint16, error) {
	if !utf8.ValidString(name) {
		// []rune would turn each invalid byte into U+FFFD, so the stream
		// could not be found by the name it was written with.
		return nil, fmt.Errorf("cfb: name %q is not valid UTF-8", name)
	}

	units := utf16.Encode([]rune(name))
	if len(units) == 0 || len(units) > maxNameUnits {
		return nil, fmt.Errorf("cfb: name %q has %d UTF-16 units, want 1 to %d", name, len(units), maxNameUnits)
	}

	for _, unit := range units {
		switch unit {
		case 0, '/', '\\', ':', '!':
			return nil, fmt.Errorf("cfb: name %q contains %q", name, rune(unit))
		}

		if Cased(rune(unit)) {
			// Compare only knows the ASCII case folding; MSI names never
			// need more.
			return nil, fmt.Errorf("cfb: name %q contains the cased character %q", name, rune(unit))
		}
	}

	return units, nil
}

// Cased reports whether a name must not hold char: a non-ASCII BMP character
// with a case mapping, such as U+00E9 or the fullwidth U+FF41, or one that
// shares a case folding orbit with another character without a mapping,
// such as U+FB05 and U+FB06. The reader folds whole orbits, so two names
// with such a pair would be written as two siblings and read as one. U+00DF
// stays allowed: its only partner, U+1E9E, has a mapping and is refused.
// Surrogate units are never upper-cased (MS-CFB 2.6.4).
func Cased(char rune) bool {
	if char <= 0x7F || char > 0xFFFF || (char >= 0xD800 && char <= 0xDFFF) {
		return false
	}

	if hasCaseMapping(char) {
		return true
	}

	for other := unicode.SimpleFold(char); other != char; other = unicode.SimpleFold(other) {
		if other > 0x7F && other <= 0xFFFF && !hasCaseMapping(other) {
			return true
		}
	}

	return false
}

func hasCaseMapping(r rune) bool { return unicode.ToUpper(r) != r || unicode.ToLower(r) != r }

func upper(u uint16) uint16 {
	if u >= 'a' && u <= 'z' {
		return u - 32
	}

	return u
}

// CompareUnits orders two directory entry names: the shorter name first, then
// by upper-cased units as unsigned 16-bit values.
func CompareUnits(first, second []uint16) int {
	if len(first) != len(second) {
		if len(first) < len(second) {
			return -1
		}

		return 1
	}

	for i := range first {
		x, y := upper(first[i]), upper(second[i])
		if x != y {
			if x < y {
				return -1
			}

			return 1
		}
	}

	return 0
}

// Compare orders two names as CompareUnits does.
func Compare(a, b string) int {
	return CompareUnits(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

type entry struct {
	open     func() (io.ReadCloser, error)
	left     *entry
	right    *entry
	top      *entry // a storage's tree of children
	path     string // the Layout key
	name     []uint16
	small    []byte // the data of a stream below miniCutoff
	children []*entry
	size     int64
	sid      int
	start    uint32
	storage  bool
	black    bool
	clsid    [16]byte
}

// Source converts an in-memory stream into a Source.
func (s Stream) Source() Source {
	return Source{
		Name: s.Name,
		Size: int64(len(s.Data)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(s.Data)), nil },
	}
}

// Write builds a compound file holding the streams.
func Write(streams []Stream, opts Options) ([]byte, error) {
	sources := make([]Source, 0, len(streams))
	for _, s := range streams {
		sources = append(sources, s.Source())
	}

	var buf bytes.Buffer

	if _, err := WriteTo(&buf, sources, opts); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// readSource reads a source whose size is known, checking the size.
func readSource(src Source) ([]byte, error) {
	reader, err := src.Open()
	if err != nil {
		return nil, fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	data, err := io.ReadAll(io.LimitReader(&progressReader{reader: reader}, src.Size+1))
	if cerr := reader.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return nil, fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	if int64(len(data)) != src.Size {
		return nil, fmt.Errorf("cfb: stream %q delivered %d bytes, declared %d", src.Name, len(data), src.Size)
	}

	return data, nil
}

// progressReader fails with io.ErrNoProgress after maxEmptyReads reads in
// a row that return neither a byte nor an error. io.Reader allows such
// reads, but io.ReadAll and io.Copy into most writers loop on them for ever.
type progressReader struct {
	reader io.Reader
	empty  int
}

func (p *progressReader) Read(buf []byte) (int, error) {
	count, err := p.reader.Read(buf)
	if count > 0 || err != nil || len(buf) == 0 {
		p.empty = 0

		return count, err //nolint:wrapcheck // the caller wraps it with the stream name
	}

	p.empty++
	if p.empty >= maxEmptyReads {
		return 0, io.ErrNoProgress
	}

	return 0, nil
}

// maxEmptyReads is how many reads in a row may return neither a byte nor
// an error before progressReader and probeEnd give up, as bufio does.
const maxEmptyReads = 100

// errLonger is the error of a source with bytes past its declared size.
var errLonger = errors.New("longer than its declared size")

// probeEnd checks that reader has nothing left. A Read may return (0, nil)
// without being at the end, so it reads until a byte, io.EOF or an error.
func probeEnd(reader io.Reader) error {
	var probe [1]byte

	for range maxEmptyReads {
		extra, err := reader.Read(probe[:])

		switch {
		case extra > 0:
			return errLonger
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return fmt.Errorf("probing the end: %w", err)
		}
	}

	return io.ErrNoProgress
}

// sectorWriter writes whole sectors to w and counts them.
type sectorWriter struct {
	w    *bufio.Writer
	size int
	pos  int64
}

func (s *sectorWriter) write(p []byte) error {
	n, err := s.w.Write(p)
	s.pos += int64(n)

	if err != nil {
		return fmt.Errorf("cfb: write: %w", err)
	}

	return nil
}

// pad fills the current sector with zeros.
func (s *sectorWriter) pad() error {
	if rem := int(s.pos % int64(s.size)); rem != 0 {
		return s.write(make([]byte, s.size-rem))
	}

	return nil
}

// words writes little-endian 32-bit values.
func (s *sectorWriter) words(values []uint32) error {
	buf := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[4*i:], v)
	}

	return s.write(buf)
}

// copyFrom copies exactly size bytes of a source and pads to the sector.
func (s *sectorWriter) copyFrom(src Source) error {
	reader, err := src.Open()
	if err != nil {
		return fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	copied, err := io.Copy(s.w, io.LimitReader(&progressReader{reader: reader}, src.Size))
	s.pos += copied

	if err != nil {
		_ = reader.Close()

		return fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	if copied != src.Size {
		_ = reader.Close()

		return fmt.Errorf("cfb: stream %q delivered %d bytes, declared %d", src.Name, copied, src.Size)
	}

	if err := probeEnd(reader); err != nil {
		_ = reader.Close()

		return fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	if err := reader.Close(); err != nil {
		return fmt.Errorf("cfb: stream %q: %w", src.Name, err)
	}

	return s.pad()
}

// plan is the sector layout computed from the stream sizes.
type plan struct {
	top                                                  *entry
	entries                                              []*entry // every entry but the root, in SID order
	mini                                                 []byte
	fat                                                  []uint32
	sectorSize, numEntries                               int
	fatSectors, difatSectors, dirSectors, miniFATSectors int
	miniStreamSectors, largeSectors, total               int
	fatStart, difatStart, dirStart, miniFATStart         int
	miniStreamStart, largeStart                          int
}

func (p *plan) sectorOffset(s int) int64 { return int64(s+1) * int64(p.sectorSize) }

// chain links count sectors from start in the FAT.
func (p *plan) chain(start, count int) {
	for i := range count {
		if i == count-1 {
			p.fat[start+i] = endOfChain
		} else {
			p.fat[start+i] = uint32(start + i + 1) //nolint:gosec // bounded by the format limits checked earlier
		}
	}
}

// newEntry checks one stream source and reads it into memory when it goes
// into the mini stream or is empty.
func newEntry(src Source, prefix string) (*entry, error) {
	name, err := EncodeName(src.Name)
	if err != nil {
		return nil, err
	}

	if src.Size < 0 || src.Open == nil {
		return nil, fmt.Errorf("cfb: stream %q has no size or source", prefix+src.Name)
	}

	// A stream larger than the file can never fit; checking it here keeps
	// the sector arithmetic below from overflowing.
	if src.Size > maxFileSize {
		return nil, fmt.Errorf("cfb: stream %q of %d bytes would exceed 2 GB", prefix+src.Name, src.Size)
	}

	ent := &entry{name: name, size: src.Size, open: src.Open, path: prefix + src.Name}

	// An empty stream is read too, so that a source that fails or delivers
	// bytes is rejected like any other source that breaks its size.
	if src.Size < miniCutoff {
		ent.small, err = readSource(src)
		if err != nil {
			return nil, err
		}
	}

	return ent, nil
}

// siblings sorts the entries of one storage, rejects duplicate names and
// returns the top of their balanced red-black tree: the middle element is
// the top node, recursively, and the nodes below the black depth are red.
func siblings(entries []*entry, allBlack bool) (*entry, error) {
	slices.SortStableFunc(entries, func(a, b *entry) int { return CompareUnits(a.name, b.name) })

	for i := 1; i < len(entries); i++ {
		if CompareUnits(entries[i-1].name, entries[i].name) == 0 {
			return nil, fmt.Errorf("cfb: duplicate name %q", entries[i].path)
		}
	}

	blackDepth := 0
	for (1 << (blackDepth + 1)) <= len(entries)+1 {
		blackDepth++
	}

	var build func(lo, hi, depth int) *entry

	build = func(low, high, depth int) *entry {
		if low >= high {
			return nil
		}

		mid := low + (high-low)/2
		e := entries[mid]
		e.black = allBlack || depth < blackDepth
		e.left = build(low, mid, depth+1)
		e.right = build(mid+1, high, depth+1)

		return e
	}

	return build(0, len(entries), 0), nil
}

// newPlan sorts the sources and storages, builds the directory trees and
// lays out the sectors. Streams below miniCutoff are read into memory here.
// SIDs number the root's children in sorted order from 1, then the children
// of each storage in turn, so a file without storages is laid out as before.
func newPlan(sources []Source, storages []Storage, opts Options) (*plan, error) { //nolint:cyclop,gocognit // one pass over the format
	pln := &plan{sectorSize: 512}
	if opts.Version == 4 {
		pln.sectorSize = 4096
	}

	// The most sectors, header included, that fit in maxFileSize.
	maxSectors := maxFileSize / pln.sectorSize

	root := make([]*entry, 0, len(sources)+len(storages))

	for _, src := range sources {
		ent, err := newEntry(src, "")
		if err != nil {
			return nil, err
		}

		root = append(root, ent)
	}

	for _, stg := range storages {
		name, err := EncodeName(stg.Name)
		if err != nil {
			return nil, err
		}

		ent := &entry{name: name, path: stg.Name, storage: true, clsid: stg.CLSID}

		for _, src := range stg.Streams {
			child, err := newEntry(src, stg.Name+"/")
			if err != nil {
				return nil, err
			}

			ent.children = append(ent.children, child)
		}

		if ent.top, err = siblings(ent.children, opts.AllBlack); err != nil {
			return nil, err
		}

		root = append(root, ent)
	}

	var err error
	if pln.top, err = siblings(root, opts.AllBlack); err != nil {
		return nil, err
	}

	pln.entries = append(pln.entries, root...)

	for _, ent := range root {
		pln.entries = append(pln.entries, ent.children...)
	}

	for i, ent := range pln.entries {
		ent.sid = i + 1
	}

	pln.numEntries = len(pln.entries) + 1
	perSector := pln.sectorSize / entrySize
	pln.dirSectors = (pln.numEntries + perSector - 1) / perSector

	// Pack small streams into the mini stream in SID order.
	for _, ent := range pln.entries {
		switch {
		case ent.storage:
		case ent.size == 0:
			ent.start = endOfChain
		case ent.size < miniCutoff:
			ent.start = uint32(len(pln.mini) / miniSectorSize) //nolint:gosec // bounded by the format limits checked earlier
			pln.mini = append(pln.mini, ent.small...)

			if pad := len(pln.mini) % miniSectorSize; pad != 0 {
				pln.mini = append(pln.mini, make([]byte, miniSectorSize-pad)...)
			}
		default:
			pln.largeSectors += int((ent.size + int64(pln.sectorSize) - 1) / int64(pln.sectorSize))

			// Counted in sectors, not bytes, so that no sum can overflow.
			if pln.largeSectors > maxSectors {
				return nil, errFileTooLarge
			}
		}
	}

	numMini := len(pln.mini) / miniSectorSize
	pln.miniFATSectors = (numMini + pln.sectorSize/4 - 1) / (pln.sectorSize / 4)
	pln.miniStreamSectors = (len(pln.mini) + pln.sectorSize - 1) / pln.sectorSize

	// Fixed point for the FAT and DIFAT sector counts.
	for {
		total := pln.fatSectors + pln.difatSectors + pln.dirSectors + pln.miniFATSectors + pln.miniStreamSectors + pln.largeSectors
		fatCount := (total + pln.sectorSize/4 - 1) / (pln.sectorSize / 4)

		next := 0
		if fatCount > 109 {
			next = (fatCount - 109 + pln.sectorSize/4 - 2) / (pln.sectorSize/4 - 1)
		}

		if fatCount == pln.fatSectors && next == pln.difatSectors {
			break
		}

		pln.fatSectors, pln.difatSectors = fatCount, next
	}

	pln.total = pln.fatSectors + pln.difatSectors + pln.dirSectors + pln.miniFATSectors + pln.miniStreamSectors + pln.largeSectors
	if pln.total+1 > maxSectors {
		return nil, errFileTooLarge
	}

	pln.fatStart = 0
	pln.difatStart = pln.fatSectors
	pln.dirStart = pln.difatStart + pln.difatSectors
	pln.miniFATStart = pln.dirStart + pln.dirSectors
	pln.miniStreamStart = pln.miniFATStart + pln.miniFATSectors
	pln.largeStart = pln.miniStreamStart + pln.miniStreamSectors

	// FAT.
	pln.fat = make([]uint32, pln.fatSectors*pln.sectorSize/4)
	for i := range pln.fat {
		pln.fat[i] = freeSect
	}

	for i := range pln.fatSectors {
		pln.fat[pln.fatStart+i] = fatSect
	}

	for i := range pln.difatSectors {
		pln.fat[pln.difatStart+i] = difSect
	}

	pln.chain(pln.dirStart, pln.dirSectors)
	pln.chain(pln.miniFATStart, pln.miniFATSectors)
	pln.chain(pln.miniStreamStart, pln.miniStreamSectors)

	cur := pln.largeStart

	for _, e := range pln.entries {
		if !e.storage && e.size >= miniCutoff {
			c := int((e.size + int64(pln.sectorSize) - 1) / int64(pln.sectorSize))
			e.start = uint32(cur) //nolint:gosec // bounded by the format limits checked earlier
			pln.chain(cur, c)
			cur += c
		}
	}

	return pln, nil
}

// header encodes the 512-byte header, padded to a sector.
func (p *plan) header(opts Options) []byte {
	out := make([]byte, p.sectorSize)
	order := binary.LittleEndian

	copy(out, signature)
	order.PutUint16(out[0x18:], 0x3E)
	order.PutUint16(out[0x1A:], uint16(opts.Version)) //nolint:gosec // bounded by the format limits checked earlier
	order.PutUint16(out[0x1C:], 0xFFFE)

	if opts.Version == 4 {
		order.PutUint16(out[0x1E:], 12)
		order.PutUint32(out[0x28:], uint32(p.dirSectors)) //nolint:gosec // bounded by the format limits checked earlier
	} else {
		order.PutUint16(out[0x1E:], 9)
	}

	order.PutUint16(out[0x20:], 6)
	order.PutUint32(out[0x2C:], uint32(p.fatSectors)) //nolint:gosec // bounded by the format limits checked earlier
	order.PutUint32(out[0x30:], uint32(p.dirStart))   //nolint:gosec // bounded by the format limits checked earlier
	order.PutUint32(out[0x38:], miniCutoff)

	if p.miniFATSectors > 0 {
		order.PutUint32(out[0x3C:], uint32(p.miniFATStart)) //nolint:gosec // bounded by the format limits checked earlier
	} else {
		order.PutUint32(out[0x3C:], endOfChain)
	}

	order.PutUint32(out[0x40:], uint32(p.miniFATSectors)) //nolint:gosec // bounded by the format limits checked earlier

	if p.difatSectors > 0 {
		order.PutUint32(out[0x44:], uint32(p.difatStart)) //nolint:gosec // bounded by the format limits checked earlier
	} else {
		order.PutUint32(out[0x44:], endOfChain)
	}

	order.PutUint32(out[0x48:], uint32(p.difatSectors)) //nolint:gosec // bounded by the format limits checked earlier

	for i := range 109 {
		v := uint32(freeSect)
		if i < p.fatSectors {
			v = uint32(p.fatStart + i) //nolint:gosec // bounded by the format limits checked earlier
		}

		order.PutUint32(out[0x4C+4*i:], v)
	}

	return out
}

// difat encodes DIFAT sector i.
func (p *plan) difat(difatIdx int) []uint32 {
	per := p.sectorSize/4 - 1
	words := make([]uint32, 0, per+1)

	for i := range per {
		k := 109 + difatIdx*per + i

		v := uint32(freeSect)
		if k < p.fatSectors {
			v = uint32(p.fatStart + k) //nolint:gosec // bounded by the format limits checked earlier
		}

		words = append(words, v)
	}

	if difatIdx == p.difatSectors-1 {
		return append(words, endOfChain)
	}

	return append(words, uint32(p.difatStart+difatIdx+1)) //nolint:gosec // bounded by the format limits checked earlier
}

// directory encodes the directory sectors.
func (p *plan) directory(opts Options) []byte {
	order := binary.LittleEndian
	perSector := p.sectorSize / entrySize
	out := make([]byte, p.dirSectors*p.sectorSize)

	putEntry := func(sid int, name []uint16, typ, colour byte, left, right, child uint32, clsid []byte, start uint32, size uint64) {
		rec := out[entrySize*sid : entrySize*(sid+1)]

		for i, u := range name {
			order.PutUint16(rec[2*i:], u)
		}

		order.PutUint16(rec[0x40:], uint16(2*(len(name)+1))) //nolint:gosec // bounded by the format limits checked earlier
		rec[0x42] = typ
		rec[0x43] = colour
		order.PutUint32(rec[0x44:], left)
		order.PutUint32(rec[0x48:], right)
		order.PutUint32(rec[0x4C:], child)

		if clsid != nil {
			copy(rec[0x50:], clsid)
		}

		order.PutUint32(rec[0x74:], start)
		order.PutUint64(rec[0x78:], size)
	}

	link := func(e *entry) uint32 {
		if e == nil {
			return noStream
		}

		return uint32(e.sid) //nolint:gosec // bounded by the format limits checked earlier
	}

	rootStart := uint32(endOfChain)
	if p.miniStreamSectors > 0 {
		rootStart = uint32(p.miniStreamStart) //nolint:gosec // bounded by the format limits checked earlier
	}

	putEntry(0, utf16.Encode([]rune("Root Entry")), 5, 1, noStream, noStream, link(p.top), opts.CLSID[:], rootStart, uint64(len(p.mini)))

	for _, ent := range p.entries {
		colour := byte(0)
		if ent.black {
			colour = 1
		}

		if ent.storage {
			// A storage has no stream: start sector and size are 0.
			putEntry(ent.sid, ent.name, 1, colour, link(ent.left), link(ent.right), link(ent.top), ent.clsid[:], 0, 0)

			continue
		}

		putEntry(ent.sid, ent.name, 2, colour, link(ent.left), link(ent.right), noStream, nil, ent.start, uint64(ent.size)) //nolint:gosec // sizes are non-negative
	}

	for k := p.numEntries; k < p.dirSectors*perSector; k++ {
		e := out[entrySize*k:]
		order.PutUint32(e[0x44:], noStream)
		order.PutUint32(e[0x48:], noStream)
		order.PutUint32(e[0x4C:], noStream)
	}

	return out
}

// miniFAT encodes the MiniFAT sectors.
func (p *plan) miniFAT() []uint32 {
	words := make([]uint32, p.miniFATSectors*p.sectorSize/4)
	for i := range words {
		words[i] = freeSect
	}

	for _, e := range p.entries {
		if !e.storage && e.size > 0 && e.size < miniCutoff {
			c := int((e.size + miniSectorSize - 1) / miniSectorSize)
			startSector := int(e.start)

			for k := range c {
				if k == c-1 {
					words[startSector+k] = endOfChain
				} else {
					words[startSector+k] = uint32(startSector + k + 1) //nolint:gosec // bounded by the format limits checked earlier
				}
			}
		}
	}

	return words
}

// WriteTo writes a compound file holding the sources to w, in one pass:
// header, FAT, DIFAT, directory, MiniFAT, mini stream and then the large
// streams, each copied from its source. Streams below 4096 bytes are read
// into memory for the mini stream; everything else passes through.
func WriteTo(dst io.Writer, sources []Source, opts Options) (*Layout, error) {
	return WriteStoragesTo(dst, sources, nil, opts)
}

// WriteStoragesTo is WriteTo for a file that also holds root-level storages,
// each with its own streams.
func WriteStoragesTo(dst io.Writer, sources []Source, storages []Storage, opts Options) (*Layout, error) { //nolint:cyclop // one pass over the format
	switch opts.Version {
	case 0:
		opts.Version = 4
	case 3, 4:
	default:
		return nil, fmt.Errorf("cfb: version %d, want 3 or 4", opts.Version)
	}

	if opts.CLSID == [16]byte{} {
		opts.CLSID = CLSIDMSI
	}

	pln, err := newPlan(sources, storages, opts)
	if err != nil {
		return nil, err
	}

	out := &sectorWriter{w: bufio.NewWriterSize(dst, 1<<16), size: pln.sectorSize}

	if err := out.write(pln.header(opts)); err != nil {
		return nil, err
	}

	if err := out.words(pln.fat); err != nil {
		return nil, err
	}

	for i := range pln.difatSectors {
		if err := out.words(pln.difat(i)); err != nil {
			return nil, err
		}
	}

	if err := out.write(pln.directory(opts)); err != nil {
		return nil, err
	}

	if pln.miniFATSectors > 0 {
		if err := out.words(pln.miniFAT()); err != nil {
			return nil, err
		}

		if err := out.write(pln.mini); err != nil {
			return nil, err
		}

		if err := out.pad(); err != nil {
			return nil, err
		}
	}

	layout := &Layout{Offsets: make(map[string]int64, len(pln.entries))}

	for _, ent := range pln.entries {
		name := ent.path

		switch {
		case ent.storage:
		case ent.size == 0:
			layout.Offsets[name] = out.pos
		case ent.size < miniCutoff:
			layout.Offsets[name] = pln.sectorOffset(pln.miniStreamStart) + int64(ent.start)*miniSectorSize
		default:
			layout.Offsets[name] = out.pos

			if err := out.copyFrom(Source{Name: name, Size: ent.size, Open: ent.open}); err != nil {
				return nil, err
			}
		}
	}

	if err := out.w.Flush(); err != nil {
		return nil, fmt.Errorf("cfb: write: %w", err)
	}

	layout.Size = out.pos

	return layout, nil
}
