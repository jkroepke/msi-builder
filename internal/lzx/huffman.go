package lzx

import (
	"cmp"
	"errors"
	"slices"
)

// buildLengths returns Huffman path lengths of at most limit bits for freq.
// Every tree it returns has at least two codes ([MS-PATCH] 2.4): unused
// symbols get a frequency of one until two are present.
func buildLengths(freq []uint32, limit int) []uint8 {
	weights := slices.Clone(freq)

	used := 0

	for _, v := range weights {
		if v > 0 {
			used++
		}
	}

	for i := 0; used < 2 && i < len(weights); i++ {
		if weights[i] == 0 {
			weights[i] = 1
			used++
		}
	}

	for {
		lengths, longest := huffman(weights)
		if longest <= limit {
			return lengths
		}

		// Flatten the distribution and retry; the halving keeps every used
		// symbol present.
		for i, v := range weights {
			if v > 0 {
				weights[i] = v/2 + 1
			}
		}
	}
}

// huffman returns unlimited Huffman path lengths and the longest one. It
// merges with two queues: the leaves sorted by weight, and the internal nodes,
// which are created in order of weight.
func huffman(freq []uint32) ([]uint8, int) {
	type item struct {
		weight uint64
		id     int // a symbol for leaves, len(freq)+i for internal node i
	}

	leaves := make([]item, 0, len(freq))

	for sym, v := range freq {
		if v > 0 {
			leaves = append(leaves, item{weight: uint64(v), id: sym})
		}
	}

	slices.SortStableFunc(leaves, func(a, b item) int { return cmp.Compare(a.weight, b.weight) })

	parent := make([]int, len(freq)+len(leaves))
	internal := make([]item, 0, len(leaves))
	next := 0 // first unmerged internal node

	pop := func() item {
		if len(leaves) > 0 && (next == len(internal) || leaves[0].weight <= internal[next].weight) {
			first := leaves[0]
			leaves = leaves[1:]

			return first
		}

		next++

		return internal[next-1]
	}

	for len(leaves)+len(internal)-next > 1 {
		a, b := pop(), pop()
		id := len(freq) + len(internal)
		parent[a.id], parent[b.id] = id, id
		internal = append(internal, item{weight: a.weight + b.weight, id: id})
	}

	root := len(freq) + len(internal) - 1
	depth := make([]int, len(parent))
	lengths := make([]uint8, len(freq))
	longest := 0

	// Internal nodes are numbered after their children, so walking them
	// downwards from the root sees every parent before its children.
	for id := root - 1; id >= len(freq); id-- {
		depth[id] = depth[parent[id]] + 1
	}

	for sym, v := range freq {
		if v > 0 {
			d := depth[parent[sym]] + 1
			lengths[sym] = uint8(min(d, 255)) //nolint:gosec // clamped
			longest = max(longest, d)
		}
	}

	return lengths, longest
}

// canonicalCodes assigns the canonical codes of [MS-PATCH] 2.4: shorter codes
// sort first, and within a length lower symbols get lower codes.
func canonicalCodes(lengths []uint8) []uint16 {
	var count, next [maxCodeLen + 2]int

	for _, length := range lengths {
		count[length]++
	}

	count[0] = 0
	code := 0

	for length := 1; length <= maxCodeLen; length++ {
		code = (code + count[length-1]) << 1
		next[length] = code
	}

	codes := make([]uint16, len(lengths))

	for sym, length := range lengths {
		if length > 0 {
			codes[sym] = uint16(next[length]) //nolint:gosec // fits the code length
			next[length]++
		}
	}

	return codes
}

// tableBits is the width of the decoder's direct lookup table.
const tableBits = 10

var errBadTree = errors.New("lzx: invalid Huffman tree")

// decoder decodes one canonical Huffman tree.
type decoder struct {
	count   [maxCodeLen + 1]int
	first   [maxCodeLen + 1]int    // first code of each length
	offset  [maxCodeLen + 1]int    // index into sorted of each length
	table   [1 << tableBits]uint16 // symbol<<5 | length; 0 means a longer code
	sorted  [maxSymbols]uint16     // symbols by code length, then value
	present bool
}

// maxSymbols is the largest tree: the main tree of a 2 MB window.
const maxSymbols = numChars + 8*50

// init builds the decoder; an all-zero tree is accepted but cannot decode.
// Any other tree must be complete, which also means at least two codes
// ([MS-PATCH] 2.4), as buildLengths writes them: cabinet.dll rejects an
// incomplete tree (Windows CI).
func (d *decoder) init(lengths []uint8) error { //nolint:cyclop // validation and table filling in one pass
	if len(lengths) > maxSymbols {
		return errBadTree
	}

	*d = decoder{}

	for _, length := range lengths {
		if length > maxCodeLen {
			return errBadTree
		}

		d.count[length]++
	}

	d.count[0] = 0

	left, code, sorted := 1, 0, 0
	for length := 1; length <= maxCodeLen; length++ {
		left = left<<1 - d.count[length]
		if left < 0 {
			return errBadTree
		}

		d.first[length] = code
		d.offset[length] = sorted
		code = (code + d.count[length]) << 1
		sorted += d.count[length]
	}

	if sorted == 0 {
		return nil
	}

	if left != 0 {
		return errBadTree
	}

	d.present = true
	codes := canonicalCodes(lengths)
	next := d.offset

	for sym, length := range lengths {
		if length == 0 {
			continue
		}

		d.sorted[next[length]] = uint16(sym)
		next[length]++

		if length <= tableBits {
			start := int(codes[sym]) << (tableBits - length)
			for i := range 1 << (tableBits - length) {
				d.table[start+i] = uint16(sym)<<5 | uint16(length)
			}
		}
	}

	return nil
}
