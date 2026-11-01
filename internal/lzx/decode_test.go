package lzx //nolint:testpackage // the tests write blocks through the unexported writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// crafted writes an LZX stream block by block, for layouts the encoder never
// writes: blocks that do not start on frame boundaries, stored blocks of odd
// size inside the stream and chosen repeated offsets.
type crafted struct {
	w      *writer
	frames [][]byte
	data   []byte        // the uncompressed stream
	lens   map[int]uint8 // the next verbatim block's length tree, nil for all zero
}

func newCrafted(window int) *crafted {
	stream := &crafted{w: newWriter(nil, 0, Options{Window: window})}
	stream.w.bits.write(0, 1) // no E8 translation

	return stream
}

// newCraftedE8 starts a stream whose E8 header sets the translation size.
func newCraftedE8(window int, size uint32) *crafted {
	stream := &crafted{w: newWriter(nil, 0, Options{Window: window})}
	stream.w.bits.write(1, 1)
	stream.w.bits.write(size>>16, 16)
	stream.w.bits.write(size&0xFFFF, 16)

	return stream
}

// advance counts n uncompressed bytes and ends the frame at a frame boundary.
func (c *crafted) advance(n int) {
	before := len(c.data) - n
	if before/FrameSize != len(c.data)/FrameSize {
		c.w.bits.align()
		c.frames = append(c.frames, c.w.bits.out)
		c.w.bits.out = nil
	}
}

// verbatim writes a verbatim block whose main tree holds the symbols 'A'
// and high with one bit each, then the codes: 0 for 'A', 1 for high. out is
// what the block decodes to.
func (c *crafted) verbatim(high int, codes []uint32, out []byte) {
	bits := make([]uint, len(codes))
	for i := range bits {
		bits[i] = 1
	}

	c.verbatimTree(map[int]uint8{'A': 1, high: 1}, codes, bits, out)
}

// verbatimTree writes a verbatim block with the main tree's path lengths
// lens, then each code with its number of bits.
func (c *crafted) verbatimTree(lens map[int]uint8, codes []uint32, bits []uint, out []byte) {
	c.w.bits.write(blockVerb, 3)
	c.w.bits.write(uint32(len(out)), 24) //nolint:gosec // test sizes

	mainLens := make([]uint8, len(c.w.prevMain))
	for sym, length := range lens {
		mainLens[sym] = length
	}

	c.w.writeTree(mainLens[:numChars], c.w.prevMain[:numChars])
	c.w.writeTree(mainLens[numChars:], c.w.prevMain[numChars:])

	lenLens := make([]uint8, numLengths)
	for sym, length := range c.lens {
		lenLens[sym] = length
	}

	c.w.writeTree(lenLens, c.w.prevLen)
	copy(c.w.prevLen, lenLens)
	copy(c.w.prevMain, mainLens)

	for i, code := range codes {
		c.w.bits.write(code, bits[i])
	}

	c.data = append(c.data, out...)
	c.advance(len(out))
}

// stored writes an uncompressed block with the repeated offsets rep. Each
// frame's bytes go into that frame; the pad byte of an odd block goes after
// the bytes, which is the next frame when the block ends at a frame boundary.
func (c *crafted) stored(rep repState, data []byte) {
	c.w.bits.write(blockStored, 3)
	c.w.bits.write(uint32(len(data)), 24) //nolint:gosec // test sizes

	if c.w.bits.n == 0 {
		c.w.bits.write(0, 16)
	} else {
		c.w.bits.align()
	}

	for _, r := range rep {
		c.w.bits.out = binary.LittleEndian.AppendUint32(c.w.bits.out, r)
	}

	for left := data; len(left) > 0; {
		chunk := left[:min(len(left), FrameSize-len(c.data)%FrameSize)]
		c.w.bits.out = append(c.w.bits.out, chunk...)
		c.data = append(c.data, chunk...)
		left = left[len(chunk):]
		c.advance(len(chunk))
	}

	if len(data)%2 == 1 {
		c.w.bits.out = append(c.w.bits.out, 0)
	}
}

// finish ends the last frame and returns the frames.
func (c *crafted) finish() [][]byte {
	if len(c.data)%FrameSize != 0 {
		c.w.bits.align()
		c.frames = append(c.frames, c.w.bits.out)
		c.w.bits.out = nil
	}

	return c.frames
}

func pattern(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i*31 + i>>9)
	}

	return out
}

// storedPadNextFrame is one compressed literal, 32767 stored bytes that end
// at the first frame's end and another literal: the stored block's pad byte
// opens the second frame.
func storedPadNextFrame() *crafted {
	stream := newCrafted(15)
	stream.verbatim('B', []uint32{0}, []byte("A"))
	stream.stored(initialRep(), pattern(FrameSize-1))
	stream.verbatim('B', []uint32{1}, []byte("B"))

	return stream
}

// repeatedOffsetMatch stores size bytes with R0 = offset, then writes a
// two-byte match with R0.
func repeatedOffsetMatch(window, size int, offset uint32) *crafted {
	stream := newCrafted(window)
	stored := pattern(size)
	stream.stored(repState{offset, 1, 1}, stored)

	// The match bytes, copied from offset bytes back; a rejected match
	// leaves zeros, which are never compared.
	all := append(stored, 0, 0) //nolint:gocritic // stored is not used again

	if offset > 0 && int64(offset) <= int64(size) {
		for k := size; k < size+2; k++ {
			all[k] = all[k-int(offset)]
		}
	}

	match := all[size:]

	stream.verbatim(numChars, []uint32{1}, match) // main symbol 256: R0, length 2

	return stream
}

// decodeCrafted decodes every frame of stream with a fresh decoder.
func decodeCrafted(window int, frames [][]byte, size int) ([]byte, error) {
	dec, err := NewDecoder(window)
	if err != nil {
		return nil, err
	}

	var out []byte

	for i, frame := range frames {
		got, err := dec.Decode(frame, min(FrameSize, size-i*FrameSize))
		if err != nil {
			return out, fmt.Errorf("frame %d: %w", i, err)
		}

		out = append(out, got...)
	}

	return out, nil
}

// TestDecoderRejectsIncompleteWord: a frame that lost its last byte ends in
// half a 16-bit word, which must not decode as zero bits.
func TestDecoderRejectsIncompleteWord(t *testing.T) {
	t.Parallel()

	data := []byte{2, 2, 4, 3, 1, 4, 2}

	frames, err := Compress(data, Options{Window: 15, Level: 1, Block: blockVerb})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := decodeCrafted(15, frames, len(data)); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("whole frame: % x, %v", got, err)
	}

	// One byte more than the bitstream needs is not read.
	longer := [][]byte{append(append([]byte(nil), frames[0]...), 0xFF)}
	if got, err := decodeCrafted(15, longer, len(data)); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("frame with a trailing byte: % x, %v", got, err)
	}

	for cut := 1; cut <= 2; cut++ {
		short := [][]byte{frames[0][:len(frames[0])-cut]}
		if got, err := decodeCrafted(15, short, len(data)); !errors.Is(err, errTruncated) {
			t.Errorf("frame %d bytes short: % x, %v; want %v", cut, got, err, errTruncated)
		}
	}
}

// incompleteLengthTree is a verbatim block with a complete main tree and a
// length tree of the path lengths 1 and 2.
func incompleteLengthTree() *crafted {
	stream := newCrafted(15)
	stream.lens = map[int]uint8{0: 1, 1: 2}
	stream.verbatim('B', []uint32{0}, []byte("A"))

	return stream
}

// storedPadVariants are odd stored blocks around the case of
// storedPadNextFrame: one that ends inside a frame and is followed by a
// compressed block in the same frame, and one that spans three frames and
// ends at the third one's end.
func storedPadVariants() map[string]*crafted {
	inside := newCrafted(15)
	inside.verbatim('B', []uint32{0}, []byte("A"))
	inside.stored(initialRep(), pattern(101))
	inside.verbatim('B', []uint32{1, 0}, []byte("BA"))

	spanning := newCrafted(15)
	spanning.verbatim('B', []uint32{0}, []byte("A"))
	spanning.stored(initialRep(), pattern(3*FrameSize-1))
	spanning.verbatim('B', []uint32{1}, []byte("B"))

	return map[string]*crafted{"inside a frame": inside, "over three frames": spanning}
}

// TestDecoderStoredPadNextFrame: cabinet.dll reads the pad byte of an odd
// stored block that ends at a frame's end from the next frame.
func TestDecoderStoredPadNextFrame(t *testing.T) {
	t.Parallel()

	stream := storedPadNextFrame()
	frames := stream.finish()

	if len(frames) != 2 || frames[1][0] != 0 {
		t.Fatalf("crafted %d frames, want 2 with the pad byte first", len(frames))
	}

	got, err := decodeCrafted(15, frames, len(stream.data))
	if err != nil || !bytes.Equal(got, stream.data) {
		t.Fatalf("decoded %d bytes, %v; differs at %d", len(got), err, firstDiff(got, stream.data))
	}

	for name, variant := range storedPadVariants() {
		got, err := decodeCrafted(15, variant.finish(), len(variant.data))
		if err != nil || !bytes.Equal(got, variant.data) {
			t.Errorf("%s: decoded %d bytes, %v; differs at %d", name, len(got), err, firstDiff(got, variant.data))
		}
	}

	// The pad byte left in the first frame instead puts the second
	// frame's bitstream one byte off.
	moved := [][]byte{append(append([]byte(nil), frames[0]...), 0), frames[1][1:]}
	if got, err := decodeCrafted(15, moved, len(stream.data)); err == nil && bytes.Equal(got, stream.data) {
		t.Fatal("decoded the pad byte at the end of the first frame")
	}
}

// TestDecoderRepeatedOffsets: a stored block sets the repeated offsets to
// any 32-bit value. A match may reach as far back as the window and the
// history allow; cabinet.dll accepts the full window, beyond the window less
// 3 that position slots reach. 2^31 and more must fail on 32-bit builds
// too, where int would turn them negative.
func TestDecoderRepeatedOffsets(t *testing.T) {
	t.Parallel()

	for window := MinWindow; window <= MaxWindow; window++ {
		size := 1 << window

		for _, tc := range []struct {
			size   int
			offset uint32
			ok     bool
		}{
			{size, uint32(size - 3), true}, //nolint:gosec // at most 2^21
			{size, uint32(size - 2), true}, //nolint:gosec // at most 2^21
			{size, uint32(size - 1), true}, //nolint:gosec // at most 2^21
			{size, uint32(size), true},
			{size, uint32(size + 1), false},
			{size, 0x80000000, false},
			{size, 0xFFFFFFFF, false},
			{1, 0x80000000, false},
			{4, 4, true},
			{4, 5, false},
		} {
			stream := repeatedOffsetMatch(window, tc.size, tc.offset)

			got, err := decodeCrafted(window, stream.finish(), len(stream.data))

			switch {
			case tc.ok && (err != nil || !bytes.Equal(got, stream.data)):
				t.Errorf("window %d, %d stored bytes, offset %#x: %v, data differs at %d", window, tc.size, tc.offset, err, firstDiff(got, stream.data))
			case !tc.ok && !errors.Is(err, errCorrupt):
				t.Errorf("window %d, %d stored bytes, offset %#x: %v, want %v", window, tc.size, tc.offset, err, errCorrupt)
			}
		}
	}
}

// e8LargeSize stores a call operand of zero at offset 1 under the
// translation size 2^31, which int32 turned negative. The decoder restores
// the operand to -1: 0 lies in [-1, size), so it becomes 0 - 1.
func e8LargeSize() *crafted {
	stream := newCraftedE8(15, 0x80000000)
	data := make([]byte, 20)
	data[1] = 0xE8
	stream.stored(initialRep(), data)

	stream.data[2], stream.data[3], stream.data[4], stream.data[5] = 0xFF, 0xFF, 0xFF, 0xFF

	return stream
}

// incompleteTree is a verbatim block whose main tree has the path lengths
// 1 ('A') and 2 ('B'): one code of length 2 is left unused, which
// cabinet.dll rejects.
func incompleteTree() *crafted {
	stream := newCrafted(15)
	stream.verbatimTree(map[int]uint8{'A': 1, 'B': 2}, []uint32{0}, []uint{1}, []byte("A"))

	return stream
}

func TestDecoderE8LargeSize(t *testing.T) {
	t.Parallel()

	stream := e8LargeSize()

	got, err := decodeCrafted(15, stream.finish(), len(stream.data))
	if err != nil || !bytes.Equal(got, stream.data) {
		t.Fatalf("decoded % x, %v; want % x", got, err, stream.data)
	}

	// Decoding with sizes of 2^31 and more ([MS-PATCH] 2.2.2: a value in
	// [-cur, size) becomes value - cur when not negative, else value + size).
	for _, tc := range []struct {
		size        uint32
		value, want uint32
	}{
		{0x80000000, 0, 0xFFFFFFFF},
		{0x80000000, 0xFFFFFFFF, 0x7FFFFFFF},
		{0x80000000, 0xFFFFFFFE, 0xFFFFFFFE}, // below -cur: unchanged
		{0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFE},
		{0xFFFFFFFF, 0x7FFFFFFF, 0x7FFFFFFE},
		{0xFFFFFFFF, 0x80000000, 0x80000000}, // below -cur: unchanged
	} {
		frame := make([]byte, 20)
		frame[1] = 0xE8
		binary.LittleEndian.PutUint32(frame[2:], tc.value)

		e8(frame, 0, tc.size, false)

		if got := binary.LittleEndian.Uint32(frame[2:]); got != tc.want {
			t.Errorf("size %#x, operand %#x at 1: decoded %#x, want %#x", tc.size, tc.value, got, tc.want)
		}
	}

	// The encoder's sizes stay below 2^31, where encoding inverts decoding.
	for _, value := range []int32{0, 1, -1, 100, -100, 0x7FFFFFFF, -0x80000000} {
		frame := make([]byte, 20)
		frame[3] = 0xE8
		binary.LittleEndian.PutUint32(frame[4:], uint32(value)) //nolint:gosec // reinterpreting the operand

		want := slices.Clone(frame)

		e8(frame, 1000, 0x7FFFFFFF, true)
		e8(frame, 1000, 0x7FFFFFFF, false)

		if !bytes.Equal(frame, want) {
			t.Errorf("operand %d: encode then decode gives % x", value, frame[4:8])
		}
	}
}

func TestDecoderRejectsIncompleteTree(t *testing.T) {
	t.Parallel()

	stream := incompleteTree()

	if got, err := decodeCrafted(15, stream.finish(), len(stream.data)); !errors.Is(err, errBadTree) {
		t.Fatalf("decoded % x, %v; want %v", got, err, errBadTree)
	}

	stream = incompleteLengthTree()

	if got, err := decodeCrafted(15, stream.finish(), len(stream.data)); !errors.Is(err, errBadTree) {
		t.Fatalf("length tree: decoded % x, %v; want %v", got, err, errBadTree)
	}
}

// TestWindowsFixtures keeps the streams in testdata/windows in step with the
// code that crafts them. internal/cab's native tests extract them through
// cabinet.dll, and expect both cabinet.dll and the reader to fail on the
// ones named reject-*, and only the reader on the ones named lenient-*; on
// the ones named varies-* the reader fails and cabinet.dll's outcome
// depends on the Windows version.
// MSI_BUILDER_UPDATE_SNAPSHOTS=1 rewrites them.
func TestWindowsFixtures(t *testing.T) {
	t.Parallel()

	fixtures := map[string]*crafted{
		"stored-pad-next-frame":         storedPadNextFrame(),
		"e8-size-2gib":                  e8LargeSize(),
		"reject-incomplete-tree":        incompleteTree(),
		"reject-incomplete-length-tree": incompleteLengthTree(),
		"varies-offset-before-start":    repeatedOffsetMatch(15, 4, 5),
		// An offset of the window plus one: cabinet.dll takes it modulo the
		// window, as offset 1, both right after a full window of bytes
		// (where it also reaches before the stream's start) and after two;
		// the reader rejects both, since the window does not hold the byte.
		"lenient-offset-before-start-at-window": repeatedOffsetMatch(15, 1<<15, 1<<15+1),
		"lenient-offset-beyond-window":          repeatedOffsetMatch(15, 2<<15, 1<<15+1),
	}

	for name, stream := range storedPadVariants() {
		fixtures["stored-pad-"+strings.ReplaceAll(name, " ", "-")] = stream
	}

	for _, less := range []int{0, 1, 2, 3} {
		fixtures[fmt.Sprintf("repeated-offset-window-less-%d", less)] = repeatedOffsetMatch(15, 1<<15, uint32(1<<15-less)) //nolint:gosec // below 2^15
	}

	for name, stream := range fixtures {
		raw := encodeFixture(15, stream.finish(), stream.data)
		path := filepath.Join("testdata", "windows", name+".lzx")

		if os.Getenv("MSI_BUILDER_UPDATE_SNAPSHOTS") == "1" {
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(raw, want) {
			t.Errorf("%s differs from the crafted stream; rerun with MSI_BUILDER_UPDATE_SNAPSHOTS=1", path)
		}
	}
}

// encodeFixture lays out a fixture: the window as one byte, the
// uncompressed size, the SHA-256 of the uncompressed data, the frame count,
// then each frame's length and bytes; numbers are little-endian 32-bit.
// internal/cab reads the same layout.
func encodeFixture(window int, frames [][]byte, data []byte) []byte {
	sum := sha256.Sum256(data)

	out := []byte{byte(window)}                                    //nolint:gosec // 15 to 21
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data))) //nolint:gosec // test sizes
	out = append(out, sum[:]...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(frames))) //nolint:gosec // test sizes

	for _, frame := range frames {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(frame))) //nolint:gosec // test sizes
		out = append(out, frame...)
	}

	return out
}

// TestEncoderLastFrameAtMaxSize: NewEncoder accepts streams of up to
// math.MaxInt32 bytes, whose last frame starts at 0x7FFF8000. Adding
// FrameSize to that position overflows int on 32-bit builds, which made the
// match collector read past the buffered data. The encoder is moved to the
// start of the last block instead of being fed 2 GiB.
func TestEncoderLastFrameAtMaxSize(t *testing.T) {
	t.Parallel()

	for _, level := range []int{1, 9} {
		frames := encodeLastBlock(t, level)
		if want := (math.MaxInt32%blockSpan + FrameSize - 1) / FrameSize; len(frames) != want {
			t.Fatalf("level %d: %d frames, want %d", level, len(frames), want)
		}
	}
}

// encodeLastBlock moves an encoder for math.MaxInt32 bytes to its last
// block and compresses that block.
func encodeLastBlock(t *testing.T, level int) [][]byte {
	t.Helper()

	const size = math.MaxInt32

	enc, err := NewEncoder(size, Options{Window: 15, Level: level})
	if err != nil {
		t.Fatal(err)
	}

	// The history holds a frame before the last block, so that the
	// repeated offsets of 1 point at data.
	start := size / blockSpan * blockSpan
	enc.read, enc.pending, enc.out.pos = start, start, start
	enc.hist.base = start - FrameSize
	enc.hist.buf = append(enc.hist.buf[:0], x86ish(FrameSize, 4)...)

	data := x86ish(size-start, 3)

	var frames [][]byte

	for off := 0; off < len(data); off += FrameSize {
		out, err := enc.Frame(data[off:min(off+FrameSize, len(data))])
		if err != nil {
			t.Fatal(err)
		}

		frames = append(frames, out...)
	}

	return frames
}
