package lzx //nolint:testpackage // the tests reach the unexported encoder parts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// scaled returns n, or a fifth of it under -short or -race: with the race
// detector and atomic coverage the encoder runs up to fifty times slower.
func scaled(n int) int {
	if testing.Short() || raceEnabled {
		return n / 5
	}

	return n
}

// decodeAll decodes the frames twice: through Decode, and through DecodeInto
// into one reused buffer that still holds the previous frame and garbage.
func decodeAll(t *testing.T, frames [][]byte, window, size int) []byte {
	t.Helper()

	dec, err := NewDecoder(window)
	if err != nil {
		t.Fatal(err)
	}

	into, err := NewDecoder(window)
	if err != nil {
		t.Fatal(err)
	}

	var out []byte

	buf := bytes.Repeat([]byte{0xAA}, FrameSize)

	for i, frame := range frames {
		frameSize := min(FrameSize, size-i*FrameSize)

		got, err := dec.Decode(frame, frameSize)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}

		if err := into.DecodeInto(frame, buf[:frameSize]); err != nil {
			t.Fatalf("frame %d: DecodeInto: %v", i, err)
		}

		if !bytes.Equal(buf[:frameSize], got) {
			t.Fatalf("frame %d: DecodeInto differs from Decode", i)
		}

		out = append(out, got...)
	}

	return out
}

// x86ish builds data with E8 calls, repeated records and text, the mix an
// executable has.
func x86ish(size int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // reproducible test data
	out := make([]byte, 0, size)

	words := []string{"windows", "installer", "cabinet", "exporter", "alloy", "metrics", "\x00\x00\x00\x00"}

	for len(out) < size {
		switch rng.Intn(5) {
		case 0:
			out = append(out, 0xE8)
			out = binary.LittleEndian.AppendUint32(out, uint32(rng.Intn(1<<20)-1<<19)) //nolint:gosec // test data
		case 1:
			out = append(out, words[rng.Intn(len(words))]...)
		case 2:
			if len(out) > 300 {
				from := len(out) - 1 - rng.Intn(min(len(out)-260, 1<<18))
				n := 3 + rng.Intn(250)
				out = append(out, out[from:from+n]...)
			}
		case 3:
			// 16-byte records: aligned-offset friendly distances.
			for range 4 {
				out = binary.LittleEndian.AppendUint32(out, uint32(rng.Intn(16))*16) //nolint:gosec // test data
			}
		default:
			out = append(out, byte(rng.Intn(256))) //nolint:gosec // below 256
		}
	}

	return out[:size]
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	random := make([]byte, scaled(200000))
	rand.New(rand.NewSource(1)).Read(random) //nolint:gosec // reproducible test data

	// Long zero runs make the most match candidates per byte, so under -race
	// two frames of them stand in for five.
	zeros := 5*FrameSize + 17
	if raceEnabled {
		zeros = 2*FrameSize + 17
	}

	inputs := map[string][]byte{
		"one":         {42},
		"short":       []byte("abc"),
		"ten":         []byte("0123456789"),
		"eleven-e8":   {0xE8, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		"frame":       bytes.Repeat([]byte("ab"), FrameSize/2),
		"frame+1":     bytes.Repeat([]byte("x"), FrameSize+1),
		"zeros":       make([]byte, zeros),
		"random":      random,
		"x86":         x86ish(scaled(700000), 2),
		"text":        bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. 0123456789\r\n"), scaled(9000)),
		"e8-tail":     append(bytes.Repeat([]byte{0xE8, 0x10, 0, 0, 0}, 7000), 0xE8, 1, 2, 3, 4),
		"block-split": x86ish(blockSpan+FrameSize+5, 3),
		"shift":       alphabetShift(),
	}

	for name, data := range inputs {
		for _, opts := range []Options{{}, {Window: 15, Level: 1}, {Window: 16, Block: blockVerb}, {Window: 18, Block: blockAligned}, {Window: 21, Level: 4, Block: blockAligned}, {Window: 17, Block: blockStored}} {
			if raceEnabled {
				// The inputs of more than two frames run only with the 32 KB
				// and the 2 MB window, and the default two-pass parse of
				// levels 7 to 9 is replaced with level 4; CI runs every
				// combination without -race.
				if len(data) > 2*FrameSize && opts.Window != 15 && opts.Window != 21 {
					continue
				}

				if opts.Level == 0 {
					opts.Level = 4
				}
			}

			t.Run(fmt.Sprintf("%s/w%d-l%d-b%d", name, opts.Window, opts.Level, opts.Block), func(t *testing.T) {
				t.Parallel()

				frames, err := Compress(data, opts)
				if err != nil {
					t.Fatal(err)
				}

				if want := (len(data) + FrameSize - 1) / FrameSize; len(frames) != want {
					t.Fatalf("%d frames, want %d", len(frames), want)
				}

				for i, f := range frames {
					if len(f)%2 != 0 || len(f) > MaxFrame {
						t.Fatalf("frame %d is %d bytes", i, len(f))
					}
				}

				window := opts.Window
				if window == 0 {
					window = MaxWindow
				}

				if got := decodeAll(t, frames, window, len(data)); !bytes.Equal(got, data) {
					t.Fatalf("round trip differs at %d", firstDiff(got, data))
				}
			})
		}
	}
}

// alphabetShift is incompressible data whose first frame uses other byte
// values than the rest of its block, so that the block's trees code that
// frame with long literal codes: compressed, it would exceed MaxFrame.
func alphabetShift() []byte {
	rng := rand.New(rand.NewSource(2)) //nolint:gosec // reproducible test data
	out := make([]byte, 4*FrameSize)

	for i := range out {
		if i < FrameSize {
			out[i] = byte(101 + rng.Intn(155)) //nolint:gosec // below 256
		} else {
			out[i] = byte(rng.Intn(101)) //nolint:gosec // below 101
		}
	}

	return out
}

// TestOverflowStored checks that a block whose compressed frame would exceed
// MaxFrame is written as an uncompressed block.
func TestOverflowStored(t *testing.T) {
	t.Parallel()

	data := alphabetShift()

	for _, level := range []int{1, 9} {
		frames, err := Compress(data, Options{Window: 15, Level: level})
		if err != nil {
			t.Fatal(err)
		}

		// The E8 header takes 33 bits, so the block type is bits 14-12 of
		// the third 16-bit word.
		if typ := binary.LittleEndian.Uint16(frames[0][4:]) >> 12 & 7; typ != blockStored {
			t.Fatalf("level %d: block type %d, want %d", level, typ, blockStored)
		}
	}
}

func firstDiff(got, want []byte) int {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return i
		}
	}

	return min(len(got), len(want))
}

// TestSpecExample decodes the uncompressed block example of [MS-PATCH]
// section 3 without its chunk-size prefix, which cabinets do not have.
func TestSpecExample(t *testing.T) {
	t.Parallel()

	frame := []byte{0x00, 0x30, 0x30, 0x00, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0x61, 0x62, 0x63, 0x00}

	dec, err := NewDecoder(17)
	if err != nil {
		t.Fatal(err)
	}

	got, err := dec.Decode(frame, 3)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestE8Inverse(t *testing.T) {
	t.Parallel()

	data := x86ish(3*FrameSize, 7)
	orig := append([]byte(nil), data...)

	for off := 0; off < len(data); off += FrameSize {
		e8(data[off:off+FrameSize], off, uint32(len(data)), true) //nolint:gosec // 96 KB
	}

	if bytes.Equal(data, orig) {
		t.Fatal("translation changed nothing")
	}

	for off := 0; off < len(data); off += FrameSize {
		e8(data[off:off+FrameSize], off, uint32(len(data)), false) //nolint:gosec // 96 KB
	}

	if !bytes.Equal(data, orig) {
		t.Fatalf("restore differs at %d", firstDiff(data, orig))
	}
}

func TestSlots(t *testing.T) {
	t.Parallel()

	// [MS-PATCH] 2.6.2: slot 49 ends at 2^21-1, the 2 MB window.
	if got := basePosition[49] + 1<<footerBits[49] - 1; got != 1<<21-1 {
		t.Fatalf("slot 49 ends at %d", got)
	}

	for formatted := range uint32(1 << 21) {
		slot := slotOf(formatted)
		if formatted < basePosition[slot] || formatted-basePosition[slot] >= 1<<footerBits[slot] && slot >= 3 {
			t.Fatalf("formatted %d in slot %d", formatted, slot)
		}
	}
}

func TestCorruptFramesFail(t *testing.T) {
	t.Parallel()

	data := x86ish(70000, 9)

	frames, err := Compress(data, Options{Level: 4})
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(4)) //nolint:gosec // reproducible test data

	for range scaled(200) {
		dec, _ := NewDecoder(MaxWindow)

		broken := append([]byte(nil), frames[0]...)
		broken[rng.Intn(len(broken))] ^= byte(1 + rng.Intn(255)) //nolint:gosec // 1 to 255

		// Must not panic; an error or different data are both fine.
		_, _ = dec.Decode(broken, FrameSize)
		_, _ = dec.Decode(frames[1][:len(frames[1])/2], FrameSize)
	}
}

func FuzzDecode(f *testing.F) {
	frames, _ := Compress(x86ish(40000, 5), Options{Window: 15})
	f.Add(frames[0], 15)
	f.Fuzz(func(_ *testing.T, frame []byte, window int) {
		dec, err := NewDecoder(MinWindow + (window&0xFF)%7)
		if err != nil {
			return
		}

		_, _ = dec.Decode(frame, FrameSize)
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello hello hello"), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		opts := Options{Window: MinWindow + int(mode%7), Level: 1 + int(mode/7%9), Block: int(mode / 63 % 4)}

		frames, err := Compress(data, opts)
		if err != nil {
			t.Fatal(err)
		}

		if len(data) == 0 {
			return
		}

		if got := decodeAll(t, frames, opts.Window, len(data)); !bytes.Equal(got, data) {
			t.Fatalf("round trip differs at %d", firstDiff(got, data))
		}
	})
}

func TestSlotTables(t *testing.T) {
	t.Parallel()

	for slot := range footerBits {
		want := uint32(0)
		if slot >= 4 {
			want = uint32(min((slot-2)/2, 17))
		}

		if footerBits[slot] != want {
			t.Errorf("slot %d: %d footer bits, want %d", slot, footerBits[slot], want)
		}

		if slot > 0 && basePosition[slot] != basePosition[slot-1]+1<<footerBits[slot-1] {
			t.Errorf("slot %d: base %d", slot, basePosition[slot])
		}
	}
}

// TestEncoderSlides streams more than the history buffer holds, with
// repeats both inside and beyond the match distance.
func TestEncoderSlides(t *testing.T) {
	t.Parallel()

	// Under -short a 128 KB window keeps the history small, and under -race
	// a 32 KB one.
	window := MaxWindow

	switch {
	case raceEnabled:
		window = 15
	case testing.Short():
		window = 17
	}

	// The two copies of chunk alone overflow the history, so under -race one
	// frame between them is enough.
	middle := 2 * blockSpan
	if raceEnabled {
		middle = FrameSize
	}

	chunk := x86ish(1<<min(window, searchBits)+3*FrameSize+11, 13)
	data := append(append(bytes.Clone(chunk), x86ish(middle, 14)...), chunk...)

	frames, err := Compress(data, Options{Window: window, Level: 4})
	if err != nil {
		t.Fatal(err)
	}

	if got := decodeAll(t, frames, window, len(data)); !bytes.Equal(got, data) {
		t.Fatalf("round trip differs at %d", firstDiff(got, data))
	}
}

func TestEncoderFrameSizes(t *testing.T) {
	t.Parallel()

	enc, err := NewEncoder(FrameSize+10, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := enc.Frame(make([]byte, 10)); err == nil {
		t.Fatal("a short frame before the last one was accepted")
	}

	if out, err := enc.Frame(make([]byte, FrameSize)); err != nil || out != nil {
		t.Fatalf("first frame: %d frames, %v", len(out), err)
	}

	if out, err := enc.Frame(make([]byte, 10)); err != nil || len(out) != 2 {
		t.Fatalf("last frame: %d frames, %v", len(out), err)
	}

	if _, err := enc.Frame(make([]byte, 1)); err == nil {
		t.Fatal("a frame after the end was accepted")
	}
}

// TestDecodeIntoAllocates nothing per frame once the decoder exists, so the
// cabinet reader decodes a folder without a temporary buffer per block.
func TestDecodeIntoAllocates(t *testing.T) { //nolint:paralleltest // AllocsPerRun refuses parallel tests
	if raceEnabled {
		t.Skip("the race detector allocates")
	}

	data := x86ish(8*FrameSize, 4)

	for _, block := range []int{blockVerb, blockAligned, blockStored} {
		frames, err := Compress(data, Options{Window: 15, Level: 1, Block: block})
		if err != nil {
			t.Fatal(err)
		}

		dec, err := NewDecoder(15)
		if err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, FrameSize)
		next := 0

		allocs := testing.AllocsPerRun(len(frames)-1, func() {
			if err := dec.DecodeInto(frames[next], buf); err != nil {
				t.Fatal(err)
			}

			next++
		})

		if allocs != 0 {
			t.Errorf("block type %d: %.1f allocations per frame, want 0", block, allocs)
		}
	}
}

// TestDecoderRejectsSingleCode: a tree that is not all zero must be
// complete, so it has at least two codes ([MS-PATCH] 2.4), which is also
// what buildLengths writes; cabinet.dll rejects an incomplete tree.
func TestDecoderRejectsSingleCode(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		lengths []uint8
		ok      bool
	}{
		"empty":           {lengths: nil, ok: true},
		"all zero":        {lengths: make([]uint8, 8), ok: true},
		"two codes":       {lengths: []uint8{1, 0, 1}, ok: true},
		"one code":        {lengths: []uint8{0, 1, 0}},
		"one long code":   {lengths: []uint8{0, 0, 16}},
		"oversubscribed":  {lengths: []uint8{1, 1, 1}},
		"incomplete pair": {lengths: []uint8{1, 2}},
		"incomplete deep": {lengths: []uint8{1, 2, 3}},
		"complete three":  {lengths: []uint8{1, 2, 2}, ok: true},
	} {
		var dec decoder

		err := dec.init(tc.lengths)
		if tc.ok != (err == nil) {
			t.Errorf("%s: init = %v, want ok %v", name, err, tc.ok)
		}
	}

	// A verbatim block whose main tree holds only the literal 'A', with a
	// valid pretree, decoded 'A' before the fix.
	frame := newWriter(nil, 1, Options{Window: 15})
	frame.bits.write(0, 1) // no E8 translation
	frame.bits.write(blockVerb, 3)
	frame.bits.write(1, 24)

	mainLens := make([]uint8, len(frame.prevMain))
	mainLens['A'] = 1

	frame.writeTree(mainLens[:numChars], frame.prevMain[:numChars])
	frame.writeTree(mainLens[numChars:], frame.prevMain[numChars:])
	frame.writeTree(make([]uint8, numLengths), frame.prevLen)
	frame.bits.write(0, 1) // the code of 'A'
	frame.bits.align()

	dec, err := NewDecoder(15)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := dec.Decode(frame.bits.out, 1); !errors.Is(err, errBadTree) {
		t.Fatalf("Decode = %q, %v; want %v", got, err, errBadTree)
	}
}
