// Package cab writes and reads Microsoft cabinet files as Windows Installer
// uses them: stored, MSZIP or LZX compression, a checksum on every data
// block, and no spanning across cabinets. The writer puts every file in one
// folder, except that an entry whose bytes later entries share gets a folder
// of its own (Entry.Copy).
//
// The format is docs/spec/MSISPEC.md section 5. The writer is deterministic:
// identical files and options give identical bytes with one Go version. It
// streams: WriteTo reads every stored file once through its Open function and
// keeps two blocks and the DEFLATE state, or the LZX encoder's bounded
// history and match finder, in memory, however large the files are.
package cab

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jkroepke/msi-builder/internal/codepage"
	"github.com/jkroepke/msi-builder/internal/lzx"
	"github.com/jkroepke/msi-builder/internal/spool"
)

// Compression selects the folder's compression type.
type Compression uint16

// Compression types written by this package.
const (
	None  Compression = 0
	MSZIP Compression = 1
	LZX   Compression = 3
)

const (
	headerSize = 36
	folderSize = 8
	blockSize  = 32768
	// maxBlockData is the largest compressed block MSZIP allows.
	maxBlockData = 32780
	maxNameLen   = 255
	// recordHeaderSize is a CFDATA record without its payload.
	recordHeaderSize = 8
	// fileEntrySize is a CFFILE record without its name and NUL.
	fileEntrySize = 16
	// cbCabinetOffset is where the header holds the cabinet size.
	cbCabinetOffset = 8
	// flagReserve is the CFHEADER flag of the reserve fields.
	flagReserve = 0x4
	// signatureHeaderSize is the per-cabinet reserve an Authenticode
	// signature header fills (section 12).
	signatureHeaderSize = 20
)

// SignatureReserve is what Options.SignatureReserve adds to a cabinet: the
// cbCFHeader, cbCFFolder and cbCFData fields and a per-cabinet reserve of 20
// bytes that a signature header fills later (section 12).
const SignatureReserve = 4 + signatureHeaderSize

// Limits of one cabinet (section 5).
const (
	// MaxFiles is the most files one cabinet holds: cFiles is a u16.
	MaxFiles = 65535
	// MaxFolder is the largest folder whose block count fits cCFData (u16):
	// 65535 blocks of 32768 bytes, the split threshold of section 5.
	MaxFolder = MaxFiles * blockSize
	// MaxSize is the largest cabinet, the FCI default (section 5).
	MaxSize = 0x7FFFFFFF
)

// Bound returns the largest cabinet WriteTo can write for files entries,
// placed in the given number of folders (Folders), whose names hold nameBytes bytes in all and whose
// stored bytes, every entry but the copies, add up to folderBytes, whatever
// the data: every compressed block taking the most bytes its compression
// allows. It is exact for None with one folder. A cabinet of entries that
// fits MaxSize by Bound can always be written; Options.SignatureReserve adds
// SignatureReserve bytes to it. A signature appended after cbCabinet is not
// counted.
func Bound(files, folders int, nameBytes, folderBytes int64, comp Compression) int64 {
	folders = max(folders, 1)
	// Each folder ends with a block that may be short.
	blocks := (folderBytes+blockSize-1)/blockSize + int64(folders) - 1
	header := headerSize + int64(folders)*folderSize + int64(files)*(fileEntrySize+1) + nameBytes + blocks*recordHeaderSize

	switch comp {
	case MSZIP:
		return header + blocks*maxBlockData
	case LZX:
		return header + blocks*lzx.MaxFrame
	case None:
	}

	return header + folderBytes
}

// File attributes.
const (
	AttrReadOnly uint16 = 0x01
	AttrHidden   uint16 = 0x02
	AttrSystem   uint16 = 0x04
	AttrArchive  uint16 = 0x20
	attrUTF8Name uint16 = 0x80
)

// File is one cabinet entry held in memory.
type File struct {
	Time       time.Time
	Name       string
	Data       []byte
	Attributes uint16
}

// Entry is one cabinet entry whose bytes WriteTo reads through Open while it
// writes, so the file never has to be in memory. Open is called once; the
// reader must deliver exactly Size bytes.
type Entry struct {
	Time time.Time
	Open func() (io.ReadCloser, error)
	Name string
	Size int64
	// Copy, when above 0, makes the entry a copy of entry Copy-1: an earlier
	// entry that is not a copy itself and holds the same, non-empty bytes, which the
	// caller has checked. The copy's CFFILE record points at that entry's
	// bytes, so they are stored once, and its Open is not called. The
	// shared entry gets a folder of its own, and the entry after a copy
	// starts a new folder, so that an extractor that goes back for a copy
	// decodes only the shared bytes again (section 5).
	Copy       int
	Attributes uint16
}

// Folders returns the number of folders WriteTo writes for entries: a new
// folder starts at the first stored entry, at every entry a later one
// copies, and at the stored entry after one that a later entry copies or
// after a copy. A cabinet without stored entries has one empty folder.
func Folders(entries []Entry) int {
	return len(planFolders(entries).folders)
}

// folderPlan places the entries in folders.
type folderPlan struct {
	// folders lists the indices of the entries each folder stores.
	folders [][]int
	// folder and offset place every entry, copies too.
	folder []int
	offset []int64
	// size is the stored bytes of each folder.
	size []int64
}

// planFolders applies the rule of Folders. The entries must have passed
// checkEntries.
func planFolders(entries []Entry) folderPlan {
	shared := make([]bool, len(entries))

	for _, ent := range entries {
		if ent.Copy > 0 {
			shared[ent.Copy-1] = true
		}
	}

	plan := folderPlan{folder: make([]int, len(entries)), offset: make([]int64, len(entries))}
	open := false // whether the last folder takes more entries

	for i, ent := range entries {
		if ent.Copy > 0 {
			plan.folder[i], plan.offset[i] = plan.folder[ent.Copy-1], plan.offset[ent.Copy-1]
			open = false

			continue
		}

		if !open || shared[i] {
			plan.folders = append(plan.folders, nil)
			plan.size = append(plan.size, 0)
		}

		last := len(plan.folders) - 1
		plan.folder[i], plan.offset[i] = last, plan.size[last]
		plan.folders[last] = append(plan.folders[last], i)
		plan.size[last] += ent.Size
		open = !shared[i]
	}

	if len(plan.folders) == 0 {
		plan.folders, plan.size = [][]int{nil}, []int64{0}
	}

	return plan
}

// Options control the writer.
type Options struct {
	// Level is the DEFLATE level for MSZIP and the effort for LZX, 1 to 9;
	// 0 means 9.
	Level int
	// Window is the LZX window as a power of two, 15 to 21; 0 means 21.
	Window int
	// Workers is the number of MSZIP blocks compressed at once; 0 means
	// GOMAXPROCS, at most 4. The cabinet is the same for any number; each
	// worker adds about 2 MB of heap at level 9.
	Workers int

	// lzxBlock forces the LZX block type in tests.
	lzxBlock int

	Compression Compression
	SetID       uint16
	// SignatureReserve writes a zeroed 20-byte per-cabinet reserve, the
	// space sign.SignCabinet fills with the Authenticode signature header,
	// so that signing never moves the cabinet's data.
	SignatureReserve bool
}

// maxWorkers caps the default number of MSZIP workers. Each one adds about
// 2 MB of heap at level 9, most of it the DEFLATE state.
const maxWorkers = 4

// workers is the number of MSZIP blocks compressed at once.
func (opts Options) workers() int {
	if opts.Workers > 0 {
		return opts.Workers
	}

	return min(runtime.GOMAXPROCS(0), maxWorkers)
}

// lzxWindow is the LZX window of opts.
func (opts Options) lzxWindow() int {
	if opts.Window == 0 {
		return lzx.MaxWindow
	}

	return opts.Window
}

// Checksum computes the CFDATA checksum over b with the given seed.
func Checksum(data []byte, seed uint32) uint32 {
	acc := seed
	words := len(data) / 4

	for i := range words {
		acc ^= binary.LittleEndian.Uint32(data[4*i:])
	}

	var tail uint32

	for _, c := range data[4*words:] {
		tail = tail<<8 | uint32(c)
	}

	return acc ^ tail
}

// blockChecksum is the stored csum of one CFDATA record.
func blockChecksum(data []byte, uncompressed int) uint32 {
	return Checksum(data, 0) ^ (uint32(len(data)) | uint32(uncompressed)<<16) //nolint:gosec // bounded by the block size
}

// DOSTime converts t (in UTC) to the cabinet date and time words, clamped to
// the representable range 1980-01-01 … 2107-12-31.
func DOSTime(when time.Time) (uint16, uint16) {
	var date, timeField uint16

	if when.IsZero() {
		return 0x0021, 0
	}

	when = when.UTC()

	if when.Year() < 1980 {
		return 0x0021, 0
	}

	if when.Year() > 2107 {
		return 0xFF9F, 0xBF7D
	}

	date = uint16((when.Year()-1980)<<9 | int(when.Month())<<5 | when.Day())  //nolint:gosec // the year was clamped to 1980…2107
	timeField = uint16(when.Hour()<<11 | when.Minute()<<5 | when.Second()>>1) //nolint:gosec // fits 16 bits by construction

	return date, timeField
}

// ParseDOSTime converts cabinet date and time words to a UTC time.
func ParseDOSTime(date, tm uint16) time.Time {
	return time.Date(1980+int(date>>9), time.Month(date>>5&0xF), int(date&0x1F),
		int(tm>>11), int(tm>>5&0x3F), int(tm&0x1F)*2, 0, time.UTC)
}

// compressBlock produces one MSZIP block for chunk with dict as the DEFLATE
// history, falling back to a stored block when compression does not pay.
func compressBlock(chunk, dict []byte, level int) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("CK")

	deflater, err := flate.NewWriterDict(&buf, level, dict)
	if err != nil {
		return nil, fmt.Errorf("cab: deflate: %w", err)
	}

	if _, err := deflater.Write(chunk); err != nil {
		return nil, fmt.Errorf("cab: deflate: %w", err)
	}

	if err := deflater.Close(); err != nil {
		return nil, fmt.Errorf("cab: deflate: %w", err)
	}

	if buf.Len() > len(chunk)+2+5 {
		stored := make([]byte, 0, len(chunk)+7)
		n := len(chunk)                                                                  // at most 32768
		stored = append(stored, 'C', 'K', 1, byte(n), byte(n>>8), byte(^n), byte(^n>>8)) //nolint:gosec // bounded by the block size
		stored = append(stored, chunk...)

		return stored, nil
	}

	return buf.Bytes(), nil
}

// entry converts an in-memory file into a streaming entry.
func (f File) entry() Entry {
	return Entry{
		Name:       f.Name,
		Size:       int64(len(f.Data)),
		Time:       f.Time,
		Attributes: f.Attributes,
		Open:       func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(f.Data)), nil },
	}
}

// Write builds a single-folder cabinet in memory.
func Write(files []File, opts Options) ([]byte, error) {
	entries := make([]Entry, 0, len(files))
	for _, f := range files {
		entries = append(entries, f.entry())
	}

	var buf spool.Buffer

	if _, err := WriteTo(&buf, entries, opts); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// checkOptions validates the options and returns the DEFLATE level.
func checkOptions(opts Options) (int, error) {
	if opts.Compression != None && opts.Compression != MSZIP && opts.Compression != LZX {
		return 0, fmt.Errorf("cab: unsupported compression %d", opts.Compression)
	}

	if window := opts.lzxWindow(); opts.Compression == LZX && (window < lzx.MinWindow || window > lzx.MaxWindow) {
		return 0, fmt.Errorf("cab: LZX window 2^%d, want 2^%d to 2^%d", window, lzx.MinWindow, lzx.MaxWindow)
	}

	level := opts.Level
	if level == 0 {
		level = 9
	}

	if level < 1 || level > 9 {
		return 0, fmt.Errorf("cab: DEFLATE level %d, want 1 to 9", level)
	}

	return level, nil
}

// checkEntries validates the entries and returns the stored bytes of all
// folders.
func checkEntries(entries []Entry) (int64, error) { //nolint:cyclop // one check per field
	if len(entries) > MaxFiles {
		return 0, fmt.Errorf("cab: %d files, at most %d fit in one cabinet", len(entries), MaxFiles)
	}

	var folder int64

	for i, ent := range entries {
		if ent.Name == "" || len(ent.Name) > maxNameLen {
			return 0, fmt.Errorf("cab: file %d: name %q must be 1 to %d bytes", i, ent.Name, maxNameLen)
		}

		if strings.IndexByte(ent.Name, 0) >= 0 {
			return 0, fmt.Errorf("cab: file %d: name %q contains NUL", i, ent.Name)
		}

		// A name with a byte of 0x80 or above is written with the UTF-8
		// attribute, which readers decode as UTF-8.
		if !utf8.ValidString(ent.Name) {
			return 0, fmt.Errorf("cab: file %d: name %q is not valid UTF-8", i, ent.Name)
		}

		if ent.Size < 0 {
			return 0, fmt.Errorf("cab: file %d: negative size", i)
		}

		if ent.Copy > 0 {
			if ent.Copy > i || entries[ent.Copy-1].Copy != 0 || entries[ent.Copy-1].Size != ent.Size || ent.Size == 0 {
				return 0, fmt.Errorf("cab: file %d: Copy %d is not an earlier stored entry of the same, non-zero size", i, ent.Copy)
			}

			continue
		}

		if ent.Copy < 0 {
			return 0, fmt.Errorf("cab: file %d: negative Copy", i)
		}

		if ent.Open == nil {
			return 0, fmt.Errorf("cab: file %d: no Open function", i)
		}

		if folder+ent.Size > MaxFolder {
			return 0, fmt.Errorf("cab: folder exceeds %d bytes (65535 blocks); split the files across cabinets", MaxFolder)
		}

		folder += ent.Size
	}

	return folder, nil
}

// fileEntry encodes one CFFILE record.
func fileEntry(ent Entry, folder int, offset int64) []byte {
	order := binary.LittleEndian
	rec := make([]byte, fileEntrySize, fileEntrySize+len(ent.Name)+1)
	order.PutUint32(rec, uint32(ent.Size))   //nolint:gosec // bounded by the folder limit checked earlier
	order.PutUint32(rec[4:], uint32(offset)) //nolint:gosec // bounded by the folder limit checked earlier
	order.PutUint16(rec[8:], uint16(folder)) //nolint:gosec // at most one folder per entry, and MaxFiles entries

	date, tm := DOSTime(ent.Time)
	order.PutUint16(rec[10:], date)
	order.PutUint16(rec[12:], tm)

	attr := ent.Attributes
	if attr == 0 {
		attr = AttrArchive
	}

	attr &^= attrUTF8Name

	for i := range len(ent.Name) {
		if ent.Name[i] >= 0x80 {
			attr |= attrUTF8Name

			break
		}
	}

	order.PutUint16(rec[14:], attr)
	rec = append(rec, ent.Name...)

	return append(rec, 0)
}

// WriteTo writes a cabinet of entries to w. It opens and reads each stored
// entry once, in order, holding two blocks and the DEFLATE state in memory.
// The cabinet size in the header, and the start of every folder after the
// first, are known only at the end, so w must seek. It returns the number of
// bytes written.
func WriteTo(dst io.WriteSeeker, entries []Entry, opts Options) (int64, error) {
	level, err := checkOptions(opts)
	if err != nil {
		return 0, err
	}

	if _, err := checkEntries(entries); err != nil {
		return 0, err
	}

	plan := planFolders(entries)
	out := &countingWriter{w: dst}

	folderRecords, err := writeHeader(out, entries, plan, opts)
	if err != nil {
		return 0, err
	}

	starts, err := writeFolders(out, entries, plan, opts, level)
	if err != nil {
		return 0, err
	}

	if out.n > MaxSize {
		return 0, errors.New("cab: cabinet exceeds 2 GiB")
	}

	// The first folder's start was known up front.
	for i := 1; i < len(starts); i++ {
		if err := patch(dst, out.n, folderRecords+int64(i)*folderSize, uint32(starts[i])); err != nil { //nolint:gosec // below MaxSize
			return 0, err
		}
	}

	if err := patch(dst, out.n, cbCabinetOffset, uint32(out.n)); err != nil { //nolint:gosec // checked against MaxSize
		return 0, err
	}

	return out.n, nil
}

// writeFolders writes the blocks of every folder of plan and returns where
// each starts.
func writeFolders(out *countingWriter, entries []Entry, plan folderPlan, opts Options, level int) ([]int64, error) {
	starts := make([]int64, len(plan.folders))

	for i, indices := range plan.folders {
		starts[i] = out.n

		folder := make([]Entry, 0, len(indices))
		for _, index := range indices {
			folder = append(folder, entries[index])
		}

		src := &concat{entries: folder}
		err := writeBlocks(out, src, plan.size[i], opts, level)
		_ = src.Close()

		if err != nil {
			return nil, err
		}
	}

	return starts, nil
}

// patch overwrites the u32 at offset of a cabinet of which end bytes have
// been written to dst, and returns to the end.
func patch(dst io.WriteSeeker, end, offset int64, value uint32) error {
	var word [4]byte

	binary.LittleEndian.PutUint32(word[:], value)

	if _, err := dst.Seek(offset-end, io.SeekCurrent); err != nil {
		return fmt.Errorf("cab: seek: %w", err)
	}

	if _, err := dst.Write(word[:]); err != nil {
		return fmt.Errorf("cab: write: %w", err)
	}

	if _, err := dst.Seek(end-offset-int64(len(word)), io.SeekCurrent); err != nil {
		return fmt.Errorf("cab: seek: %w", err)
	}

	return nil
}

// writeHeader writes the CFHEADER, the CFFOLDER and the CFFILE records with
// a zero cabinet size and, after the first, zero folder starts, which WriteTo
// patches at the end. It returns where the CFFOLDER records start.
func writeHeader(out *countingWriter, entries []Entry, plan folderPlan, opts Options) (int64, error) {
	order := binary.LittleEndian

	start := headerSize
	if opts.SignatureReserve {
		start += SignatureReserve
	}

	coffFiles := start + folderSize*len(plan.folders)
	dataStart := coffFiles

	for _, ent := range entries {
		dataStart += fileEntrySize + len(ent.Name) + 1
	}

	hdr := make([]byte, coffFiles)
	copy(hdr, "MSCF")
	order.PutUint32(hdr[16:], uint32(coffFiles)) //nolint:gosec // at most 65535 folders
	hdr[0x18], hdr[0x19] = 3, 1
	order.PutUint16(hdr[0x1A:], uint16(len(plan.folders))) //nolint:gosec // at most one folder per entry
	order.PutUint16(hdr[0x1C:], uint16(len(entries)))      //nolint:gosec // the file count was checked against MaxFiles
	order.PutUint16(hdr[0x20:], opts.SetID)

	if opts.SignatureReserve {
		order.PutUint16(hdr[0x1E:], flagReserve)
		order.PutUint16(hdr[headerSize:], signatureHeaderSize)
	}

	typeCompress := uint16(opts.Compression)
	if opts.Compression == LZX {
		typeCompress |= uint16(opts.lzxWindow()) << 8 //nolint:gosec // 15 to 21
	}

	order.PutUint32(hdr[start:], uint32(dataStart)) //nolint:gosec // bounded by the file count and name length

	for i, size := range plan.size {
		record := hdr[start+folderSize*i:]
		order.PutUint16(record[4:], uint16((size+blockSize-1)/blockSize)) //nolint:gosec // at most 65535 blocks by the folder limit
		order.PutUint16(record[6:], typeCompress)
	}

	if _, err := out.Write(hdr); err != nil {
		return 0, err
	}

	for i, ent := range entries {
		if _, err := out.Write(fileEntry(ent, plan.folder[i], plan.offset[i])); err != nil {
			return 0, err
		}
	}

	return int64(start), nil
}

// encodeBlock returns the CFDATA payload for chunk: the bytes themselves or
// an MSZIP block with prev as the DEFLATE history.
func encodeBlock(chunk, prev []byte, comp Compression, level int) ([]byte, error) {
	if comp != MSZIP {
		return chunk, nil
	}

	data, err := compressBlock(chunk, prev, level)
	if err != nil {
		return nil, err
	}

	if len(data) > maxBlockData {
		return nil, fmt.Errorf("cab: compressed block of %d bytes exceeds %d", len(data), maxBlockData)
	}

	return data, nil
}

// writeBlocks compresses the folder bytes from src into CFDATA records.
func writeBlocks(out *countingWriter, src io.Reader, folderBytes int64, opts Options, level int) error {
	var err error
	if opts.Compression == LZX {
		err = writeLZX(out, src, folderBytes, opts, level)
	} else {
		err = writeChunks(out, src, folderBytes, opts, level)
	}

	if err != nil {
		return err
	}

	// The last entry has delivered its size; make sure it ends there, which
	// also closes it.
	var probe [1]byte

	if extra, err := src.Read(probe[:]); extra > 0 || (err != nil && !errors.Is(err, io.EOF)) {
		if err == nil {
			err = errors.New("more bytes than the entries declared")
		}

		return fmt.Errorf("cab: read: %w", err)
	}

	return nil
}

// writeChunks writes the stored or MSZIP records of a folder. Each MSZIP
// block is compressed on its own with the chunk before it as its history, so
// a batch of chunks is compressed at once and written in order: the records
// are the same for any number of workers.
func writeChunks(out *countingWriter, src io.Reader, folderBytes int64, opts Options, level int) error {
	workers := opts.workers()

	// chunks[0] holds the chunk before the batch in chunks[1:].
	chunks := make([][]byte, workers+1)
	for i := range chunks {
		chunks[i] = make([]byte, 0, blockSize)
	}

	payloads := make([][]byte, workers)
	errs := make([]error, workers)

	for remaining := folderBytes; remaining > 0; {
		batch := 0

		for ; batch < workers && remaining > 0; batch++ {
			want := int(min(remaining, blockSize))
			chunk := chunks[batch+1][:want]

			if _, err := io.ReadFull(src, chunk); err != nil {
				return fmt.Errorf("cab: read: %w", err)
			}

			chunks[batch+1] = chunk
			remaining -= int64(want)
		}

		var wg sync.WaitGroup

		for i := range batch {
			wg.Go(func() {
				payloads[i], errs[i] = encodeBlock(chunks[i+1], chunks[i], opts.Compression, level)
			})
		}

		wg.Wait()

		for i := range batch {
			if errs[i] != nil {
				return errs[i]
			}

			if err := writeRecord(out, payloads[i], len(chunks[i+1]), maxBlockData); err != nil {
				return err
			}
		}

		chunks[0], chunks[batch] = chunks[batch], chunks[0]
	}

	return nil
}

// writeLZX writes the LZX frames of a folder.
func writeLZX(out *countingWriter, src io.Reader, folderBytes int64, opts Options, level int) error {
	enc, err := lzx.NewEncoder(folderBytes, lzx.Options{Window: opts.lzxWindow(), Level: level, Block: opts.lzxBlock})
	if err != nil {
		return fmt.Errorf("cab: %w", err)
	}

	chunk := make([]byte, blockSize)

	// sizes holds the uncompressed sizes of the frames not yet written: the
	// encoder returns a block's frames together.
	var sizes []int

	for remaining := folderBytes; remaining > 0; {
		want := int(min(remaining, blockSize))

		if _, err := io.ReadFull(src, chunk[:want]); err != nil {
			return fmt.Errorf("cab: read: %w", err)
		}

		frames, err := enc.Frame(chunk[:want])
		if err != nil {
			return fmt.Errorf("cab: %w", err)
		}

		sizes = append(sizes, want)

		for i, data := range frames {
			if err := writeRecord(out, data, sizes[i], lzx.MaxFrame); err != nil {
				return err
			}
		}

		sizes = sizes[:copy(sizes, sizes[len(frames):])]
		remaining -= int64(want)
	}

	return nil
}

// writeRecord writes one CFDATA record of data, which holds uncompressed
// bytes and may be at most limit bytes long.
func writeRecord(out *countingWriter, data []byte, uncompressed, limit int) error {
	if len(data) > limit {
		return fmt.Errorf("cab: compressed block of %d bytes exceeds %d", len(data), limit)
	}

	var rec [8]byte

	binary.LittleEndian.PutUint32(rec[:], blockChecksum(data, uncompressed))
	binary.LittleEndian.PutUint16(rec[4:], uint16(len(data)))    //nolint:gosec // bounded by limit
	binary.LittleEndian.PutUint16(rec[6:], uint16(uncompressed)) //nolint:gosec // at most blockSize

	if _, err := out.Write(rec[:]); err != nil {
		return err
	}

	_, err := out.Write(data)

	return err
}

// countingWriter counts the bytes written and wraps write errors.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	written, err := c.w.Write(p)
	c.n += int64(written)

	if err != nil {
		return written, fmt.Errorf("cab: write: %w", err)
	}

	return written, nil
}

// maxEmptyReads is how many reads in a row may return no bytes and no error
// before concat gives up with io.ErrNoProgress, as bufio does.
const maxEmptyReads = 100

// concat reads the entries back to back, checking that each delivers its
// declared size.
type concat struct {
	cur io.ReadCloser
	// err is a source error that came with bytes; it is returned by the next
	// Read, so that a caller filling its buffer with those bytes cannot drop
	// it.
	err     error
	entries []Entry
	left    int64
	index   int
}

func (c *concat) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	for empty := 0; ; {
		if err := c.advance(); err != nil {
			return 0, err
		}

		read, err := c.readCurrent(buf)
		if err != nil || read > 0 || c.left == 0 {
			return read, err
		}

		if empty++; empty >= maxEmptyReads {
			c.err = fmt.Errorf("%s: %w", c.entries[c.index].Name, io.ErrNoProgress)

			return 0, c.err
		}
	}
}

// Close closes the entry that is open, if any; finish has closed the others.
func (c *concat) Close() error {
	if c.cur == nil {
		return nil
	}

	err := c.cur.Close()
	c.cur = nil

	if err != nil {
		return fmt.Errorf("%s: %w", c.entries[c.index].Name, err)
	}

	return nil
}

// advance opens the next entry with bytes left, finishing the entries that
// have delivered their size. It returns io.EOF after the last entry and
// keeps any other error in c.err.
func (c *concat) advance() error {
	for c.err == nil {
		if c.cur == nil {
			if c.index >= len(c.entries) {
				return io.EOF
			}

			c.err = c.open()

			continue
		}

		if c.left > 0 {
			return nil
		}

		c.err = c.finish()
	}

	return c.err
}

// readCurrent reads from the current entry, which must have bytes left.
func (c *concat) readCurrent(buf []byte) (int, error) {
	read, err := c.cur.Read(buf[:min(int64(len(buf)), c.left)])
	c.left -= int64(read)

	switch {
	case errors.Is(err, io.EOF) && c.left > 0:
		c.err = fmt.Errorf("%s: %d bytes short of its declared size", c.entries[c.index].Name, c.left)
	case errors.Is(err, io.EOF):
		return read, nil
	case err != nil:
		c.err = fmt.Errorf("%s: %w", c.entries[c.index].Name, err)
	default:
		return read, nil
	}

	// The bytes that came with the error are delivered first; the error
	// stays in c.err for every later Read.
	if read > 0 {
		return read, nil
	}

	return 0, c.err
}

// open starts the current entry.
func (c *concat) open() error {
	ent := c.entries[c.index]

	reader, err := ent.Open()
	if err != nil {
		return fmt.Errorf("%s: %w", ent.Name, err)
	}

	c.cur, c.left = reader, ent.Size

	return nil
}

// finish checks that the current entry ends at its size, which only io.EOF
// tells: io.Reader allows a read of no bytes and no error at any time. It then
// closes the entry.
func (c *concat) finish() error {
	name := c.entries[c.index].Name

	var (
		probe [1]byte
		extra int
		err   error
	)

	for range maxEmptyReads {
		if extra, err = c.cur.Read(probe[:]); extra > 0 || err != nil {
			break
		}
	}

	if extra == 0 && err == nil {
		err = io.ErrNoProgress
	}

	if extra > 0 {
		_ = c.Close()

		return fmt.Errorf("%s: longer than its declared size", name)
	}

	if err != nil && !errors.Is(err, io.EOF) {
		_ = c.Close()

		return fmt.Errorf("%s: %w", name, err)
	}

	if err := c.Close(); err != nil {
		return err
	}

	c.index++

	return nil
}

// readAt fills p from r at off; a reader that reports io.EOF together with
// the last bytes counts as a full read.
func readAt(r io.ReaderAt, p []byte, off int64) error {
	read, err := r.ReadAt(p, off)
	if err != nil && (!errors.Is(err, io.EOF) || read != len(p)) {
		return err //nolint:wrapcheck // callers add the record context
	}

	return nil
}

// header is a parsed CFHEADER with its folders and file records.
type header struct {
	folders []folderInfo
	files   []fileRecord
	size    int64
}

type folderInfo struct {
	start  int64
	blocks int
	typ    Compression
	window int // LZX window bits from the high byte of typeCompress
}

// fileRecord is a parsed CFFILE.
type fileRecord struct {
	file   File
	size   int64
	offset int64
	folder int
}

// reserveEnd returns where the CFFOLDER records start: after the header and,
// with flagReserve, after the reserve fields and the per-cabinet reserve,
// which holds a signature header in a signed cabinet. Per-folder and
// per-block reserves are not supported.
func reserveEnd(src io.ReaderAt, size int64, flags uint16) (int64, error) {
	if flags&flagReserve == 0 {
		return headerSize, nil
	}

	var fields [4]byte

	if size < headerSize+int64(len(fields)) {
		return 0, errors.New("cab: truncated header")
	}

	if err := readAt(src, fields[:], headerSize); err != nil {
		return 0, fmt.Errorf("cab: header: %w", err)
	}

	if fields[2] != 0 || fields[3] != 0 {
		return 0, errors.New("cab: per-folder and per-block reserves are not supported")
	}

	return headerSize + int64(len(fields)) + int64(binary.LittleEndian.Uint16(fields[:])), nil
}

// parseHeader reads the header, folders and file records of a cabinet.
func parseHeader(src io.ReaderAt, size int64) (*header, error) { //nolint:cyclop // one pass over the format
	order := binary.LittleEndian
	raw := make([]byte, headerSize)

	if size < headerSize {
		return nil, errors.New("cab: not a cabinet")
	}

	if err := readAt(src, raw, 0); err != nil {
		return nil, fmt.Errorf("cab: header: %w", err)
	}

	if string(raw[:4]) != "MSCF" {
		return nil, errors.New("cab: not a cabinet")
	}

	if raw[0x18] != 3 || raw[0x19] != 1 {
		return nil, fmt.Errorf("cab: version %d.%d, want 1.3", raw[0x19], raw[0x18])
	}

	flags := order.Uint16(raw[0x1E:])
	if flags&0x3 != 0 {
		return nil, errors.New("cab: cabinet sets (previous/next cabinet) are not supported")
	}

	start, err := reserveEnd(src, size, flags)
	if err != nil {
		return nil, err
	}

	numFolders := int(order.Uint16(raw[0x1A:]))
	numFiles := int(order.Uint16(raw[0x1C:]))
	coffFiles := int64(order.Uint32(raw[0x10:]))

	if start+int64(folderSize*numFolders) > size || coffFiles > size {
		return nil, errors.New("cab: truncated header")
	}

	hdr := &header{size: size, folders: make([]folderInfo, 0, numFolders)}
	folderRaw := make([]byte, folderSize*numFolders)

	if err := readAt(src, folderRaw, start); err != nil {
		return nil, fmt.Errorf("cab: folders: %w", err)
	}

	dataStart := size

	for i := range numFolders {
		fo := folderRaw[folderSize*i:]
		info := folderInfo{
			start:  int64(order.Uint32(fo)),
			blocks: int(order.Uint16(fo[4:])),
			typ:    Compression(order.Uint16(fo[6:]) & 0xF),
			window: int(order.Uint16(fo[6:]) >> 8 & 0x1F),
		}
		hdr.folders = append(hdr.folders, info)

		// A folder without blocks has no data, and its offset is unused
		// (FDI extracts a cabinet whose empty folder has offset 0).
		if info.blocks > 0 {
			dataStart = min(dataStart, info.start)
		}
	}

	if dataStart < coffFiles {
		return nil, errors.New("cab: folder data starts before the file records")
	}

	// The records cannot be longer than numFiles maximal entries, whatever
	// the folder offsets claim.
	filesLen := min(dataStart-coffFiles, int64(numFiles)*(fileEntrySize+maxNameLen+1))
	filesRaw := make([]byte, filesLen)

	if err := readAt(src, filesRaw, coffFiles); err != nil {
		return nil, fmt.Errorf("cab: file records: %w", err)
	}

	pos := 0

	for range numFiles {
		if pos+fileEntrySize > len(filesRaw) {
			return nil, errors.New("cab: truncated file entry")
		}

		end := bytes.IndexByte(filesRaw[pos+fileEntrySize:], 0)
		if end < 0 {
			return nil, errors.New("cab: unterminated file name")
		}

		attr := order.Uint16(filesRaw[pos+14:])

		name, err := decodeName(filesRaw[pos+fileEntrySize:pos+fileEntrySize+end], attr)
		if err != nil {
			return nil, err
		}

		rec := fileRecord{
			file: File{
				Name:       name,
				Time:       ParseDOSTime(order.Uint16(filesRaw[pos+10:]), order.Uint16(filesRaw[pos+12:])),
				Attributes: attr &^ attrUTF8Name,
			},
			size:   int64(order.Uint32(filesRaw[pos:])),
			offset: int64(order.Uint32(filesRaw[pos+4:])),
			folder: int(order.Uint16(filesRaw[pos+8:])),
		}

		if rec.folder >= numFolders {
			return nil, fmt.Errorf("cab: file %q in folder %d (spanning is not supported)", rec.file.Name, rec.folder)
		}

		hdr.files = append(hdr.files, rec)
		pos += fileEntrySize + end + 1
	}

	return hdr, nil
}

// LegacyCodePage is the code page the reader decodes a name in when its
// UTF-8 attribute (0x80) is not set: Windows-1252, the ANSI code page of a
// Western European Windows. makecab.exe stores such names in the ANSI code
// page of the machine that wrote the cabinet, and a cabinet records no code
// page, so a reader has to pick one; that expand.exe uses the ANSI code page
// of the extracting machine is assumed, not verified. The writer never needs
// it: it sets 0x80 on every name with a byte of 0x80 or above. A decoded
// legacy name can be longer than 255 bytes in UTF-8, and then cannot be
// written to a cabinet again.
const LegacyCodePage = codepage.Windows1252

// decodeName converts a CFFILE szName to UTF-8: a name with the UTF-8
// attribute must be valid UTF-8, and any other name is read in
// LegacyCodePage.
func decodeName(raw []byte, attr uint16) (string, error) {
	codePage := LegacyCodePage
	if attr&attrUTF8Name != 0 {
		codePage = codepage.UTF8
	}

	name, err := codepage.Decode(raw, codePage)
	if err != nil {
		return "", fmt.Errorf("cab: file name %q: %w", raw, err)
	}

	// No byte of a single-byte code page decodes to U+FFFD, so one in the
	// name is a byte the code page leaves undefined (0x81, 0x8D, 0x8F, 0x90
	// and 0x9D in 1252). Refuse it rather than merge distinct names.
	if codePage != codepage.UTF8 && strings.ContainsRune(name, utf8.RuneError) {
		return "", fmt.Errorf("cab: file name %q: a byte is undefined in code page %d", raw, codePage)
	}

	return name, nil
}

// blockReader decodes the CFDATA records of one folder in order. Each call to
// next returns the uncompressed bytes of one block, valid until the next call.
type blockReader struct {
	r       io.ReaderAt
	lzx     *lzx.Decoder // created at the first LZX block
	payload []byte
	out     []byte
	hist    []byte
	pos     int64
	size    int64
	folder  int
	index   int
	blocks  int
	window  int
	typ     Compression
}

func newBlockReader(src io.ReaderAt, size int64, folder int, info folderInfo) *blockReader {
	limit := maxBlockData
	if info.typ == LZX {
		limit = lzx.MaxFrame
	}

	return &blockReader{
		r: src, size: size, folder: folder, pos: info.start, blocks: info.blocks, typ: info.typ, window: info.window,
		payload: make([]byte, limit), out: make([]byte, blockSize), hist: make([]byte, 0, blockSize),
	}
}

// errorf names the current block in an error.
func (b *blockReader) errorf(format string, args ...any) error {
	return fmt.Errorf("cab: folder %d block %d: %s", b.folder, b.index, fmt.Sprintf(format, args...))
}

// record reads and checks the next CFDATA record, returning its payload and
// uncompressed size.
func (b *blockReader) record() ([]byte, int, error) {
	order := binary.LittleEndian

	var rec [8]byte

	if b.pos+8 > b.size {
		return nil, 0, b.errorf("truncated")
	}

	if err := readAt(b.r, rec[:], b.pos); err != nil {
		return nil, 0, b.errorf("%v", err)
	}

	csum := order.Uint32(rec[:])
	compressed := int(order.Uint16(rec[4:]))
	uncompressed := int(order.Uint16(rec[6:]))

	if b.pos+8+int64(compressed) > b.size {
		return nil, 0, b.errorf("truncated")
	}

	if compressed > len(b.payload) {
		return nil, 0, b.errorf("%d payload bytes exceed %d", compressed, len(b.payload))
	}

	if uncompressed > blockSize {
		return nil, 0, b.errorf("%d uncompressed bytes exceed %d", uncompressed, blockSize)
	}

	payload := b.payload[:compressed]

	if err := readAt(b.r, payload, b.pos+8); err != nil {
		return nil, 0, b.errorf("%v", err)
	}

	if csum != 0 {
		if want := blockChecksum(payload, uncompressed); want != csum {
			return nil, 0, b.errorf("checksum %#x, want %#x", csum, want)
		}
	}

	return payload, uncompressed, nil
}

// next decodes the next block, or returns io.EOF after the last one.
func (b *blockReader) next() ([]byte, error) {
	if b.index >= b.blocks {
		return nil, io.EOF
	}

	payload, uncompressed, err := b.record()
	if err != nil {
		return nil, err
	}

	out := b.out[:uncompressed]

	if err := b.decode(payload, out); err != nil {
		return nil, err
	}

	// The DEFLATE history is the last 32 KiB of the folder, which spans more
	// than one block when blocks are short.
	if keep := blockSize - len(out); keep <= 0 {
		b.hist = append(b.hist[:0], out...)
	} else {
		if len(b.hist) > keep {
			b.hist = b.hist[:copy(b.hist, b.hist[len(b.hist)-keep:])]
		}

		b.hist = append(b.hist, out...)
	}

	b.pos += 8 + int64(len(payload))
	b.index++

	return out, nil
}

// decode fills out with the uncompressed bytes of payload.
func (b *blockReader) decode(payload, out []byte) error {
	switch b.typ {
	case None:
		if len(payload) != len(out) {
			return b.errorf("stored block sizes differ")
		}

		copy(out, payload)
	case MSZIP:
		if len(payload) < 2 || payload[0] != 'C' || payload[1] != 'K' {
			return b.errorf("missing CK signature")
		}

		return b.decodeMSZIP(payload[2:], out)
	case LZX:
		return b.decodeLZX(payload, out)
	default:
		return fmt.Errorf("cab: compression type %d is not supported", b.typ)
	}

	return nil
}

// decodeMSZIP inflates one MSZIP block into out. The DEFLATE stream must
// produce exactly len(out) bytes and end with its final block: a stream that
// decodes to more bytes than cbUncomp, or lacks its final block, is corrupt
// (FDI rejects both).
func (b *blockReader) decodeMSZIP(stream, out []byte) error {
	inflater := flate.NewReaderDict(bytes.NewReader(stream), b.hist)

	if _, err := io.ReadFull(inflater, out); err != nil {
		return b.errorf("%v", err)
	}

	var probe [1]byte

	switch extra, err := inflater.Read(probe[:]); {
	case extra > 0:
		return b.errorf("DEFLATE stream holds more than %d bytes", len(out))
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return b.errorf("DEFLATE stream does not end after %d bytes", len(out))
	default:
		return b.errorf("%v", err)
	}
}

// decodeLZX decodes the next frame of the folder's LZX stream.
func (b *blockReader) decodeLZX(payload, out []byte) error {
	if b.lzx == nil {
		dec, err := lzx.NewDecoder(b.window)
		if err != nil {
			return b.errorf("%v", err)
		}

		b.lzx = dec
	}

	if len(out) == 0 {
		return b.errorf("empty LZX block")
	}

	if err := b.lzx.DecodeInto(payload, out); err != nil {
		return b.errorf("%v", err)
	}

	return nil
}

// folderReader serves the uncompressed bytes of a folder as a stream.
type folderReader struct {
	blocks *blockReader
	buf    []byte
}

func (f *folderReader) Read(buf []byte) (int, error) {
	for len(f.buf) == 0 {
		out, err := f.blocks.next()
		if err != nil {
			return 0, err
		}

		f.buf = out
	}

	copied := copy(buf, f.buf)
	f.buf = f.buf[copied:]

	return copied, nil
}

// Read extracts the files of a cabinet written by this package or any other
// tool that uses stored, MSZIP or LZX folders without spanning. It verifies
// every non-zero block checksum. Files may share or overlap folder bytes.
func Read(raw []byte) ([]File, error) {
	hdr, err := parseHeader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}

	folders := make([][]byte, 0, len(hdr.folders))

	for i, info := range hdr.folders {
		blocks := newBlockReader(bytes.NewReader(raw), int64(len(raw)), i, info)

		var out []byte

		for {
			block, err := blocks.next()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				return nil, err
			}

			out = append(out, block...)
		}

		folders = append(folders, out)
	}

	files := make([]File, 0, len(hdr.files))

	for _, rec := range hdr.files {
		folder := folders[rec.folder]
		if rec.offset+rec.size > int64(len(folder)) {
			return nil, fmt.Errorf("cab: file %q extends past its folder", rec.file.Name)
		}

		file := rec.file
		file.Data = append([]byte(nil), folder[rec.offset:rec.offset+rec.size]...)
		files = append(files, file)
	}

	return files, nil
}

// Walk reads the cabinet in r and calls fn for every file in CFFILE order
// with a reader over its bytes, holding one block in memory at a time. The
// File passed to fn has no Data. Files that lie back to back in their folder
// are read in one pass; a file in another folder than the one before it, or
// before the end of the one before it, as a copy that shares bytes is,
// decodes its folder again from the start. Every folder is decoded to its
// end once, so a corrupt block that no file needs fails Walk as it fails Read.
func Walk(src io.ReaderAt, size int64, visit func(File, io.Reader) error) error {
	hdr, err := parseHeader(src, size)
	if err != nil {
		return err
	}

	walker := &walker{src: src, size: size, hdr: hdr, current: -1, verified: make([]bool, len(hdr.folders))}

	for _, rec := range hdr.files {
		if err := walker.file(rec, visit); err != nil {
			return err
		}
	}

	return walker.verifyRest()
}

// walker holds Walk's position: the folder stream it reads and which folders
// it has decoded to their end.
type walker struct {
	src      io.ReaderAt
	hdr      *header
	stream   *folderReader
	verified []bool
	size     int64
	pos      int64
	current  int
}

// file serves one file to visit, starting its folder again when the file is
// in another folder or before the end of the file before it.
func (w *walker) file(rec fileRecord, visit func(File, io.Reader) error) error {
	if rec.folder != w.current || rec.offset < w.pos {
		if err := w.leave(); err != nil {
			return err
		}

		w.stream = &folderReader{blocks: newBlockReader(w.src, w.size, rec.folder, w.hdr.folders[rec.folder])}
		w.current, w.pos = rec.folder, 0
	}

	if _, err := io.CopyN(io.Discard, w.stream, rec.offset-w.pos); err != nil {
		return fmt.Errorf("cab: file %q: %w", rec.file.Name, err)
	}

	limited := io.LimitReader(w.stream, rec.size)

	if err := visit(rec.file, limited); err != nil {
		return err
	}

	if _, err := io.Copy(io.Discard, limited); err != nil {
		return fmt.Errorf("cab: file %q: %w", rec.file.Name, err)
	}

	if limited.(*io.LimitedReader).N > 0 { //nolint:forcetypeassert // io.LimitReader returns *io.LimitedReader
		return fmt.Errorf("cab: file %q extends past its folder", rec.file.Name)
	}

	w.pos = rec.offset + rec.size

	return nil
}

// leave reads the rest of the open folder stream, the first time Walk leaves
// that folder, so that a corrupt or truncated block no file needed fails Walk
// as it fails Read. Each folder is decoded to its end at most once.
func (w *walker) leave() error {
	if w.stream == nil || w.verified[w.current] {
		return nil
	}

	if _, err := io.Copy(io.Discard, w.stream); err != nil {
		return fmt.Errorf("cab: verifying the rest of the folder: %w", err)
	}

	w.verified[w.current] = true

	return nil
}

// verifyRest finishes the open folder and decodes the folders no file
// reached.
func (w *walker) verifyRest() error {
	if err := w.leave(); err != nil {
		return err
	}

	for idx, info := range w.hdr.folders {
		if w.verified[idx] {
			continue
		}

		stream := &folderReader{blocks: newBlockReader(w.src, w.size, idx, info)}
		if _, err := io.Copy(io.Discard, stream); err != nil {
			return fmt.Errorf("cab: verifying a folder no file uses: %w", err)
		}
	}

	return nil
}
