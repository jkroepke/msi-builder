package lzx

import "errors"

// bitWriter writes the LZX bitstream: 16-bit little-endian words, each
// filled from its most significant bit ([MS-PATCH] 2.1.1).
type bitWriter struct {
	out []byte
	acc uint32
	n   uint // bits in acc, below 16 between calls
}

func (w *bitWriter) write(value uint32, bits uint) {
	for bits > 16 {
		bits -= 16
		w.write(value>>bits&0xFFFF, 16)
	}

	if bits == 0 {
		return
	}

	w.acc = w.acc<<bits | value&(1<<bits-1)
	w.n += bits

	if w.n >= 16 {
		w.n -= 16
		word := w.acc >> w.n
		w.out = append(w.out, byte(word), byte(word>>8)) //nolint:gosec // the low 16 bits
		w.acc &= 1<<w.n - 1
	}
}

// align pads with zero bits to the next 16-bit boundary.
func (w *bitWriter) align() {
	if w.n > 0 {
		w.write(0, 16-w.n)
	}
}

var errTruncated = errors.New("lzx: truncated frame")

// bitReader reads one frame's bitstream, 16-bit words from a start offset.
// Reading past the last whole word yields zero bits; pos keeps counting so
// that a decoder can report truncation. A lone byte after the last whole
// word is not part of the bitstream: reading into it counts as truncation.
type bitReader struct {
	data  []byte
	pos   int
	limit int // end of the last whole word
	acc   uint64
	n     uint
}

// reset starts reading data's bitstream at byte start.
func (r *bitReader) reset(data []byte, start int) {
	limit := len(data)
	if start <= limit {
		limit = start + (limit-start)&^1
	}

	*r = bitReader{data: data, pos: start, limit: limit}
}

func (r *bitReader) fill() {
	for r.n <= 32 {
		var word uint64

		if r.pos+1 < r.limit {
			word = uint64(r.data[r.pos]) | uint64(r.data[r.pos+1])<<8
		}

		r.pos += 2

		r.acc = r.acc<<16 | word
		r.n += 16
	}
}

// peek returns the next bits (at most 32) without consuming them.
func (r *bitReader) peek(bits uint) uint32 {
	if r.n < bits {
		r.fill()
	}

	return uint32(r.acc>>(r.n-bits)) & (1<<bits - 1) //nolint:gosec // masked to bits
}

func (r *bitReader) skip(bits uint) {
	r.n -= bits
	r.acc &= 1<<r.n - 1
}

func (r *bitReader) read(bits uint) uint32 {
	v := r.peek(bits)
	r.skip(bits)

	return v
}

// truncated reports whether the reader consumed bits past the frame's end.
func (r *bitReader) truncated() bool {
	return 8*r.pos-int(r.n) > 8*r.limit
}

// alignBytes drops the 1 to 16 padding bits of an uncompressed block header
// ([MS-PATCH] 2.3.2.1) and returns the byte position that follows them.
func (r *bitReader) alignBytes() int {
	if r.n%16 != 0 {
		r.skip(r.n % 16)
	} else {
		r.peek(16)
		r.skip(16)
	}

	pos := r.pos - int(r.n/8)
	r.acc, r.n = 0, 0

	return pos
}

// decode reads one symbol of the tree.
func (r *bitReader) decode(tree *decoder) (int, error) {
	if !tree.present {
		return 0, errBadTree
	}

	peeked := r.peek(maxCodeLen)

	if entry := tree.table[peeked>>(maxCodeLen-tableBits)]; entry != 0 {
		r.skip(uint(entry & 31))

		return int(entry >> 5), nil
	}

	for length := uint(tableBits + 1); length <= maxCodeLen; length++ {
		code := int(peeked >> (maxCodeLen - length))
		if i := code - tree.first[length]; i >= 0 && i < tree.count[length] {
			r.skip(length)

			return int(tree.sorted[tree.offset[length]+i]), nil
		}
	}

	return 0, errBadTree
}
