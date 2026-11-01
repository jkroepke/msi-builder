package lzx

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Decoder decompresses one LZX stream frame by frame. Its state (window,
// trees, repeated offsets, the current block) carries over between frames.
type Decoder struct {
	window      []byte
	mainLens    []uint8
	lenLens     []uint8
	storedFrame []byte // the frame a stored block reads from
	r           bitReader
	main, lens  decoder
	align, pre  decoder
	mask        int64
	pos         int64 // uncompressed bytes decoded, 64-bit also on 32-bit builds
	blockType   int
	blockLeft   int
	storedPos   int
	rep         repState
	e8size      uint32 // the E8 translation size, unsigned
	alignLens   [numAligned]uint8
	started     bool
	blockOdd    bool // stored block of odd size: a pad byte follows it
	padNext     bool // the pad byte of a stored block opens the next frame
}

// NewDecoder returns a decoder for a window of 2^window bytes.
func NewDecoder(window int) (*Decoder, error) {
	if err := checkWindow(window); err != nil {
		return nil, err
	}

	slots := numSlots(window)

	return &Decoder{
		window:   make([]byte, 1<<window),
		mask:     1<<window - 1,
		mainLens: make([]uint8, numChars+8*slots),
		lenLens:  make([]uint8, numLengths),
		rep:      repState{1, 1, 1},
	}, nil
}

var errCorrupt = errors.New("lzx: corrupt data")

// Decode decompresses the next frame, which holds size uncompressed bytes.
func (d *Decoder) Decode(frame []byte, size int) ([]byte, error) {
	if size <= 0 || size > FrameSize {
		return nil, fmt.Errorf("lzx: frame of %d bytes", size)
	}

	out := make([]byte, size)
	if err := d.DecodeInto(frame, out); err != nil {
		return nil, err
	}

	return out, nil
}

// DecodeInto decompresses the next frame into out, which holds the frame's
// uncompressed size, so a caller with a buffer of its own allocates nothing
// per frame.
func (d *Decoder) DecodeInto(frame, out []byte) error {
	size := len(out)
	if size <= 0 || size > FrameSize {
		return fmt.Errorf("lzx: frame of %d bytes", size)
	}

	// A stored block of odd size that ended at the previous frame's end
	// leaves its pad byte at the start of this frame.
	start := 0
	if d.padNext {
		d.padNext, start = false, 1
	}

	d.r.reset(frame, start)

	if !d.started {
		d.started = true

		// The E8 header opens the stream ([MS-PATCH] 2.2.2).
		if d.r.read(1) == 1 {
			high := d.r.read(16)
			d.e8size = high<<16 | d.r.read(16)
		}
	}

	first := d.pos

	if err := d.fill(frame, first+int64(size)); err != nil {
		return err
	}

	// The window keeps the translated bytes as history; only out is
	// restored. A frame never exceeds the window, so it wraps at most once.
	head := copy(out, d.window[first&d.mask:])
	copy(out[head:], d.window)

	if d.e8size != 0 && first < e8Limit {
		e8(out, int(first), d.e8size, false)
	}

	return nil
}

// fill decodes blocks until the window holds end bytes.
func (d *Decoder) fill(frame []byte, end int64) error { //nolint:cyclop // one loop over the block types
	if d.blockLeft > 0 && d.blockType == blockStored {
		d.storedFrame, d.storedPos = frame, 0
	}

	for d.pos < end {
		if d.blockLeft == 0 {
			if err := d.header(frame); err != nil {
				return err
			}
		}

		var err error

		if d.blockType == blockStored {
			err = d.stored(end)
		} else {
			err = d.tokens(end)
		}

		if err != nil {
			return err
		}
	}

	if (d.blockType != blockStored || d.blockLeft == 0) && d.r.truncated() {
		return errTruncated
	}

	return nil
}

// header reads a block header and its trees.
func (d *Decoder) header(frame []byte) error {
	d.blockType = int(d.r.read(3))
	d.blockLeft = int(d.r.read(24))

	if d.blockLeft == 0 {
		return fmt.Errorf("%w: empty block", errCorrupt)
	}

	switch d.blockType {
	case blockAligned:
		for i := range d.alignLens {
			d.alignLens[i] = uint8(d.r.read(3)) //nolint:gosec // 3 bits
		}

		if err := d.align.init(d.alignLens[:]); err != nil {
			return err
		}

		return d.trees()
	case blockVerb:
		return d.trees()
	case blockStored:
		pos := d.r.alignBytes()
		if pos+12 > len(frame) {
			return errTruncated
		}

		for i := range d.rep {
			d.rep[i] = binary.LittleEndian.Uint32(frame[pos+4*i:])
		}

		d.storedFrame, d.storedPos = frame, pos+12
		d.blockOdd = d.blockLeft%2 == 1

		return nil
	default:
		return fmt.Errorf("%w: block type %d", errCorrupt, d.blockType)
	}
}

// trees reads the main and length trees of a verbatim or aligned block.
func (d *Decoder) trees() error {
	for _, lens := range [][]uint8{d.mainLens[:numChars], d.mainLens[numChars:], d.lenLens} {
		if err := d.readTree(lens); err != nil {
			return err
		}
	}

	if err := d.main.init(d.mainLens); err != nil {
		return err
	}

	return d.lens.init(d.lenLens)
}

// readTree reads path lengths through a pretree, as deltas against the
// previous lengths in lens ([MS-PATCH] 2.5).
func (d *Decoder) readTree(lens []uint8) error {
	var pre [numPretree]uint8

	for i := range pre {
		pre[i] = uint8(d.r.read(4)) //nolint:gosec // 4 bits
	}

	if err := d.pre.init(pre[:]); err != nil {
		return err
	}

	for i := 0; i < len(lens); {
		run, value, err := d.pathCode(lens[i])
		if err != nil {
			return err
		}

		if i+run > len(lens) {
			return fmt.Errorf("%w: path length run overflows the tree", errCorrupt)
		}

		for j := range run {
			lens[i+j] = value
		}

		i += run
	}

	return nil
}

// pathCode reads one pretree code and returns how many elements it sets to
// which length; prev is the previous length of the first element.
func (d *Decoder) pathCode(prev uint8) (int, uint8, error) {
	code, err := d.r.decode(&d.pre)
	if err != nil {
		return 0, 0, err
	}

	run := 1

	switch code {
	case 17:
		return 4 + int(d.r.read(4)), 0, nil
	case 18:
		return 20 + int(d.r.read(5)), 0, nil
	case 19:
		run = 4 + int(d.r.read(1))

		code, err = d.r.decode(&d.pre)
		if err != nil {
			return 0, 0, err
		}

		if code > 16 {
			return 0, 0, fmt.Errorf("%w: pretree code %d after a run", errCorrupt, code)
		}
	}

	return run, uint8((int(prev) - code + 17) % 17), nil //nolint:gosec // below 17
}

// stored copies the bytes of a stored block that fall in this frame.
func (d *Decoder) stored(end int64) error {
	count := int(min(int64(d.blockLeft), end-d.pos))
	if d.storedPos+count > len(d.storedFrame) {
		return errTruncated
	}

	for _, b := range d.storedFrame[d.storedPos : d.storedPos+count] {
		d.window[d.pos&d.mask] = b
		d.pos++
	}

	d.storedPos += count
	d.blockLeft -= count

	if d.blockLeft == 0 {
		// The pad byte of a block that fills the frame opens the next
		// frame, where cabinet.dll reads it (Windows CI).
		switch {
		case d.blockOdd && d.pos == end:
			d.padNext = true
		case d.blockOdd:
			d.storedPos++
		}

		// The bitstream resumes after the stored bytes.
		d.r.reset(d.storedFrame, d.storedPos)
	}

	return nil
}

// tokens decodes matches and literals up to the end of the block or frame.
func (d *Decoder) tokens(end int64) error { //nolint:cyclop // one pass over the token format
	for d.blockLeft > 0 && d.pos < end {
		sym, err := d.r.decode(&d.main)
		if err != nil {
			return err
		}

		if sym < numChars {
			d.window[d.pos&d.mask] = byte(sym) //nolint:gosec // a literal symbol
			d.pos++
			d.blockLeft--

			continue
		}

		length, offset, err := d.match(sym - numChars)
		if err != nil {
			return err
		}

		// A repeated offset from a stored block header is any 32-bit value
		// and reaches the full window, while position slots stop at the
		// window less 3; cabinet.dll accepts the full window (Windows CI).
		// The check runs in 64 bits, since int turns an offset of 2^31 or
		// more negative on 32-bit builds.
		if offset == 0 || int64(offset) > min(d.pos, int64(len(d.window))) {
			return fmt.Errorf("%w: match offset %d at %d", errCorrupt, offset, d.pos)
		}

		if length > d.blockLeft || d.pos+int64(length) > end {
			return fmt.Errorf("%w: match of %d bytes crosses the block or frame end", errCorrupt, length)
		}

		for range length {
			d.window[d.pos&d.mask] = d.window[(d.pos-int64(offset))&d.mask]
			d.pos++
		}

		d.blockLeft -= length
	}

	return nil
}

// match reads the rest of a match whose main symbol, less 256, is header
// ([MS-PATCH] 2.7), and returns its length and offset.
func (d *Decoder) match(header int) (int, uint32, error) {
	length := header&7 + minMatch

	if header&7 == 7 {
		extra, err := d.r.decode(&d.lens)
		if err != nil {
			return 0, 0, err
		}

		length += extra
	}

	slot := header >> 3

	switch slot {
	case 0:
		return length, d.rep[0], nil
	case 1, 2:
		d.rep[0], d.rep[slot] = d.rep[slot], d.rep[0]

		return length, d.rep[0], nil
	}

	extraBits := uint(footerBits[slot])

	var footer uint32

	if d.blockType == blockAligned && extraBits >= 3 {
		footer = d.r.read(extraBits-3) << 3

		low, err := d.r.decode(&d.align)
		if err != nil {
			return 0, 0, err
		}

		footer |= uint32(low) //nolint:gosec // below 8
	} else {
		footer = d.r.read(extraBits)
	}

	offset := basePosition[slot] + footer - 2
	d.rep[2], d.rep[1], d.rep[0] = d.rep[1], d.rep[0], offset

	return length, offset, nil
}
