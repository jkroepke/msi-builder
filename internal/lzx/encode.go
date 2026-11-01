package lzx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
)

var errTooLarge = errors.New("lzx: data exceeds 2 GiB")

// Options control the encoder.
type Options struct {
	// Window is the window size as a power of two, 15 to 21; 0 means 21.
	Window int
	// Level trades speed for size, 1 to 9; 0 means 9. It sets how deep the
	// match finder searches; from 7 every frame is parsed twice.
	Level int
	// Block forces every block to one type, verbatim (1), aligned offset
	// (2) or uncompressed (3); 0 picks the smaller per block. Tests use it
	// to cover each type.
	Block int
}

const (
	// blockSpan is the uncompressed size of one block: a multiple of the
	// frame size so that blocks and frames end together.
	blockSpan = 4 * FrameSize
	// unusedCost is the estimated code length of a symbol the previous block
	// did not use.
	unusedCost = 14
)

// token is a literal (bit 31 clear, the byte in bits 0-7) or a match (bit 31
// set, length-2 in bits 21-28, the distance in bits 0-20). Distances are
// plain: the writer turns them into repeated offsets.
type token uint32

const matchFlag token = 1 << 31

func literal(b byte) token { return token(b) }

func match(length, dist int) token {
	return matchFlag | token(length-minMatch)<<21 | token(dist) //nolint:gosec // length is at most 256, dist below 2^21
}

func (t token) isMatch() bool { return t&matchFlag != 0 }

// length is the uncompressed size of the token: 1 for a literal.
func (t token) length() int {
	if !t.isMatch() {
		return 1
	}

	return int(t>>21&0xFF) + minMatch
}

func (t token) dist() uint32        { return uint32(t & 0x1FFFFF) }
func (t token) literalByte() uint32 { return uint32(t & 0xFF) }

// repState is the queue of repeated offsets ([MS-PATCH] 2.1.4).
type repState [3]uint32

// initialRep is the queue at the start of a stream.
func initialRep() repState { return repState{1, 1, 1} }

// formatted returns the formatted offset of a match at dist and updates the
// queue, preferring a repeated offset whenever one matches.
func (r *repState) formatted(dist uint32) uint32 {
	switch dist {
	case r[0]:
		return 0
	case r[1]:
		r[0], r[1] = r[1], r[0]

		return 1
	case r[2]:
		r[0], r[2] = r[2], r[0]

		return 2
	default:
		r[0], r[1], r[2] = dist, r[0], r[1]

		return dist + 2
	}
}

// costs estimates code lengths for a parser.
type costs struct {
	main []int32
	len  []int32
}

func newCosts(slots int) costs {
	estimate := costs{main: make([]int32, numChars+8*slots), len: make([]int32, numLengths)}

	// Until a block exists: 8-bit literals, 9-bit match symbols.
	for i := range estimate.main {
		estimate.main[i] = 8
		if i >= numChars {
			estimate.main[i] = 9
		}
	}

	for i := range estimate.len {
		estimate.len[i] = 8
	}

	return estimate
}

// set derives the costs from code lengths; the next block's statistics are
// usually similar to the last one's.
func (c costs) set(mainLens, lenLens []uint8) {
	for i, l := range mainLens {
		c.main[i] = int32(l)
		if l == 0 {
			c.main[i] = unusedCost
		}
	}

	for i, l := range lenLens {
		c.len[i] = int32(l)
		if l == 0 {
			c.len[i] = unusedCost
		}
	}
}

// statistics counts the symbols of tokens, whose offsets are formatted
// starting from the queue rep.
type statistics struct {
	main  []uint32
	len   []uint32
	align []uint32
}

func count(tokens []token, rep repState, slots int) statistics {
	stats := statistics{main: make([]uint32, numChars+8*slots), len: make([]uint32, numLengths), align: make([]uint32, numAligned)}

	for _, tok := range tokens {
		if !tok.isMatch() {
			stats.main[tok.literalByte()]++

			continue
		}

		formatted := rep.formatted(tok.dist())
		slot := slotOf(formatted)
		header := min(tok.length()-minMatch, 7)
		stats.main[numChars+slot<<3+header]++

		if header == 7 {
			stats.len[tok.length()-9]++
		}

		if footerBits[slot] >= 3 {
			stats.align[(formatted-basePosition[slot])&7]++
		}
	}

	return stats
}

// check fills in the defaults and validates opts.
func (opts Options) check() (Options, error) {
	if opts.Window == 0 {
		opts.Window = MaxWindow
	}

	if opts.Level == 0 {
		opts.Level = 9
	}

	if err := checkWindow(opts.Window); err != nil {
		return opts, err
	}

	if opts.Level < 1 || opts.Level > 9 {
		return opts, fmt.Errorf("lzx: level %d, want 1 to 9", opts.Level)
	}

	if opts.Block < 0 || opts.Block > blockStored {
		return opts, fmt.Errorf("lzx: block type %d, want 0 to 3", opts.Block)
	}

	return opts, nil
}

// Compress encodes data as one LZX stream and returns its frames: each frame
// holds FrameSize uncompressed bytes (the last one the rest) and is a
// cabinet's CFDATA payload.
func Compress(data []byte, opts Options) ([][]byte, error) {
	enc, err := NewEncoder(int64(len(data)), opts)
	if err != nil {
		return nil, err
	}

	var frames [][]byte

	for off := 0; off < len(data); off += min(FrameSize, len(data)-off) {
		out, err := enc.Frame(data[off : off+min(FrameSize, len(data)-off)])
		if err != nil {
			return nil, err
		}

		frames = append(frames, out...)
	}

	return frames, nil
}

// Encoder compresses one LZX stream of a known size frame by frame. It holds
// the match distance of history, one block and the match finder, whatever
// the stream's size.
type Encoder struct {
	hist    *history
	parser  *optimal
	out     *writer
	size    int
	read    int // bytes received
	pending int // first byte of the block not yet written
}

// NewEncoder returns an encoder for a stream of size bytes; the size is the
// E8 translation size and tells the encoder where the last frame ends.
func NewEncoder(size int64, opts Options) (*Encoder, error) {
	opts, err := opts.check()
	if err != nil {
		return nil, err
	}

	if size < 0 || size > math.MaxInt32 {
		return nil, errTooLarge
	}

	hist := newHistory(1 << min(opts.Window, searchBits))
	enc := &Encoder{
		hist:   hist,
		parser: newOptimal(hist, opts),
		out:    newWriter(hist, int(size), opts),
		size:   int(size),
	}

	// E8 translation is on, with the stream's length as the translation
	// size ([MS-PATCH] 2.2.2).
	enc.out.bits.write(1, 1)
	enc.out.bits.write(uint32(size)>>16, 16)
	enc.out.bits.write(uint32(size)&0xFFFF, 16)

	return enc, nil
}

var errFrame = errors.New("lzx: frame size")

// Frame compresses the next frame: FrameSize bytes, or the rest of the
// stream for the last one. It returns the compressed frames that are
// complete, in order: those of a whole block at a time, and the rest after
// the last frame. The encoder copies data.
func (e *Encoder) Frame(data []byte) ([][]byte, error) {
	if want := min(FrameSize, e.size-e.read); len(data) != want || want == 0 {
		return nil, fmt.Errorf("%w: %d bytes at %d of %d", errFrame, len(data), e.read, e.size)
	}

	e.hist.add(data, e.pending)

	// E8 translation runs on the copy, before matching.
	e8(e.hist.buf[e.read-e.hist.base:], e.read, uint32(e.size), true) //nolint:gosec // checked in NewEncoder
	e.read += len(data)

	if e.read-e.pending < blockSpan && e.read < e.size {
		return nil, nil
	}

	e.out.block(e.parser.block(e.pending, e.read))
	e.pending = e.read

	frames := e.out.frames
	e.out.frames = nil

	return frames, nil
}

// writer turns token blocks into frames.
type writer struct {
	hist     *history // the E8-translated data, for uncompressed blocks
	frames   [][]byte
	prevMain []uint8
	prevLen  []uint8
	bits     bitWriter
	pos      int // uncompressed bytes written
	total    int
	block0   int // forced block type
	slots    int
	rep      repState
}

func newWriter(hist *history, total int, opts Options) *writer {
	slots := numSlots(opts.Window)

	return &writer{
		hist:     hist,
		total:    total,
		block0:   opts.Block,
		slots:    slots,
		prevMain: make([]uint8, numChars+8*slots),
		prevLen:  make([]uint8, numLengths),
		rep:      initialRep(),
	}
}

// writerState is what a block changes in the writer.
type writerState struct {
	prevMain, prevLen []uint8
	bits              bitWriter
	frames, pos       int
	rep               repState
}

func (w *writer) save() writerState {
	stream := w.bits
	stream.out = slices.Clone(stream.out)

	return writerState{
		prevMain: slices.Clone(w.prevMain),
		prevLen:  slices.Clone(w.prevLen),
		bits:     stream,
		frames:   len(w.frames),
		pos:      w.pos,
		rep:      w.rep,
	}
}

func (w *writer) restore(state writerState) {
	copy(w.prevMain, state.prevMain)
	copy(w.prevLen, state.prevLen)
	w.bits, w.frames, w.pos, w.rep = state.bits, w.frames[:state.frames], state.pos, state.rep
}

// block writes one block. Blocks start and end on frame boundaries, so when
// a compressed frame would exceed MaxFrame the block is written again as an
// uncompressed block.
func (w *writer) block(tokens []token) {
	if w.block0 != blockStored {
		state := w.save()
		w.compressed(tokens)

		fits := true
		for _, frame := range w.frames[state.frames:] {
			fits = fits && len(frame) <= MaxFrame
		}

		if fits {
			return
		}

		w.restore(state)
	}

	size := 0
	for _, tok := range tokens {
		size += tok.length()
	}

	w.stored(size)
}

// stored writes the next size bytes as an uncompressed block ([MS-PATCH]
// 2.3.2.1): after the header, 1 to 16 bits pad to a 16-bit boundary, the
// repeated offsets follow as three 32-bit words, then the bytes and a pad
// byte if their count is odd. Each frame's bytes go into that frame. The
// repeated offsets and the previous trees stay as they are.
func (w *writer) stored(size int) {
	w.bits.write(blockStored, 3)
	w.bits.write(uint32(size), 24) //nolint:gosec // at most blockSpan

	if w.bits.n == 0 {
		w.bits.write(0, 16)
	} else {
		w.bits.align()
	}

	for _, r := range w.rep {
		w.bits.out = binary.LittleEndian.AppendUint32(w.bits.out, r)
	}

	for left := size; left > 0; {
		chunk := min(left, FrameSize-w.pos%FrameSize)
		w.bits.out = append(w.bits.out, w.hist.buf[w.pos-w.hist.base:][:chunk]...)
		left -= chunk

		if left == 0 && size%2 == 1 {
			w.bits.out = append(w.bits.out, 0)
		}

		w.advance(chunk)
	}
}

// compressed writes the tokens of one block, verbatim or aligned offset.
func (w *writer) compressed(tokens []token) {
	stats := count(tokens, w.rep, w.slots)
	mainLens := buildLengths(stats.main, maxCodeLen)
	lenLens := buildLengths(stats.len, maxCodeLen)
	alignLens := buildLengths(stats.align, maxAligned)
	kind := w.blockType(stats.align, alignLens)

	size := 0
	for _, tok := range tokens {
		size += tok.length()
	}

	w.bits.write(uint32(kind), 3) //nolint:gosec // 1 or 2
	w.bits.write(uint32(size), 24)

	if kind == blockAligned {
		for _, length := range alignLens {
			w.bits.write(uint32(length), 3)
		}
	}

	w.writeTree(mainLens[:numChars], w.prevMain[:numChars])
	w.writeTree(mainLens[numChars:], w.prevMain[numChars:])
	w.writeTree(lenLens, w.prevLen)
	copy(w.prevMain, mainLens)
	copy(w.prevLen, lenLens)

	main := huffmanCode{lens: mainLens, codes: canonicalCodes(mainLens)}
	lens := huffmanCode{lens: lenLens, codes: canonicalCodes(lenLens)}
	align := huffmanCode{lens: alignLens, codes: canonicalCodes(alignLens)}

	for _, tok := range tokens {
		if !tok.isMatch() {
			main.write(&w.bits, int(tok.literalByte()))
			w.advance(1)

			continue
		}

		formatted := w.rep.formatted(tok.dist())
		slot := slotOf(formatted)
		header := min(tok.length()-minMatch, 7)
		main.write(&w.bits, numChars+slot<<3+header)

		if header == 7 {
			lens.write(&w.bits, tok.length()-9)
		}

		footer := formatted - basePosition[slot]

		switch extra := uint(footerBits[slot]); {
		case kind == blockAligned && extra >= 3:
			w.bits.write(footer>>3, extra-3)
			align.write(&w.bits, int(footer&7))
		case extra > 0:
			w.bits.write(footer, extra)
		}

		w.advance(tok.length())
	}
}

// blockType picks the aligned offset block when its tree pays for itself.
func (w *writer) blockType(alignFreq []uint32, alignLens []uint8) int {
	if w.block0 != 0 {
		return w.block0
	}

	alignedBits, verbatimBits := 3*numAligned, 0

	for i, f := range alignFreq {
		alignedBits += int(f) * int(alignLens[i])
		verbatimBits += 3 * int(f)
	}

	if alignedBits < verbatimBits {
		return blockAligned
	}

	return blockVerb
}

// huffmanCode is one tree's code lengths and canonical codes.
type huffmanCode struct {
	lens  []uint8
	codes []uint16
}

func (h huffmanCode) write(out *bitWriter, sym int) {
	out.write(uint32(h.codes[sym]), uint(h.lens[sym]))
}

// advance counts n uncompressed bytes and closes the frame at its end.
func (w *writer) advance(n int) {
	w.pos += n

	if w.pos%FrameSize == 0 || w.pos == w.total {
		w.bits.align()
		w.frames = append(w.frames, w.bits.out)
		w.bits.out = nil
	}
}

// writeTree writes the path lengths of one tree through a pretree, as deltas
// against the previous block's lengths ([MS-PATCH] 2.5). Runs of zeros use
// codes 17 and 18; code 19 is never written.
func (w *writer) writeTree(lens, prev []uint8) {
	type code struct {
		sym   int
		extra uint32
		bits  uint
	}

	codes := make([]code, 0, len(lens))

	for i := 0; i < len(lens); {
		run := 0
		for i+run < len(lens) && lens[i+run] == 0 {
			run++
		}

		switch {
		case run >= 20:
			run = min(run, 51)
			codes = append(codes, code{sym: 18, extra: uint32(run - 20), bits: 5})
			i += run
		case run >= 4:
			codes = append(codes, code{sym: 17, extra: uint32(run - 4), bits: 4})
			i += run
		default:
			codes = append(codes, code{sym: (int(prev[i]) - int(lens[i]) + 17) % 17})
			i++
		}
	}

	freq := make([]uint32, numPretree)
	for _, c := range codes {
		freq[c.sym]++
	}

	preLens := buildLengths(freq, maxPretree)
	for _, l := range preLens {
		w.bits.write(uint32(l), 4)
	}

	preCodes := canonicalCodes(preLens)
	for _, c := range codes {
		w.bits.write(uint32(preCodes[c.sym]), uint(preLens[c.sym]))
		w.bits.write(c.extra, c.bits)
	}
}

// matchLen returns how many bytes at pos and from agree, at most limit.
func matchLen(data []byte, pos, from, limit int) int {
	length := 0

	for length+8 <= limit {
		diff := binary.LittleEndian.Uint64(data[pos+length:]) ^ binary.LittleEndian.Uint64(data[from+length:])
		if diff != 0 {
			return length + bits.TrailingZeros64(diff)/8
		}

		length += 8
	}

	for length < limit && data[pos+length] == data[from+length] {
		length++
	}

	return length
}

func hash4(data []byte, pos, width int) int {
	return int((binary.LittleEndian.Uint32(data[pos:]) * 2654435761) >> (32 - width))
}

func hash3(data []byte, pos, width int) int {
	v := uint32(data[pos]) | uint32(data[pos+1])<<8 | uint32(data[pos+2])<<16

	return int((v * 2654435761) >> (32 - width))
}
