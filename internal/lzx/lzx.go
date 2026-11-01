// Package lzx compresses and decompresses the LZX format that Microsoft
// cabinets use for typeCompress 3 folders.
//
// The bitstream follows [MS-PATCH] (LZX DELTA) without reference data, with
// the cabinet framing of [MS-CAB]: no chunk-size prefixes (the CFDATA headers
// carry the sizes), window sizes 2^15 to 2^21, and the E8 header at the start
// of the folder. The encoder stays inside the subset both formats agree on:
// matches of at most 256 bytes (so the LZXD extra-length field never applies),
// no match crosses a 32 KB frame, and uncompressed blocks only where a
// compressed frame would exceed MaxFrame.
//
// Encoder streams: it holds the last 2^19 bytes as match history, one block
// and the match finder, so its memory depends on neither the window nor the
// stream's length. Matches therefore reach at most 512 KiB back, even in a
// 2 MiB window.
//
// The format is docs/spec/MSISPEC.md section 5. cabinet.dll is the reference
// decoder; internal/cab/native_windows_test.go checks the encoder against it.
package lzx

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// FrameSize is the uncompressed size of every frame but the last. A cabinet
// stores one frame per CFDATA block.
const FrameSize = 32768

// MaxFrame is the largest compressed frame a cabinet allows: the CFDATA
// limit of [MS-CAB] for LZX, 6144 bytes above the frame size.
const MaxFrame = FrameSize + 6144

// Window sizes a cabinet allows, as powers of two.
const (
	MinWindow = 15
	MaxWindow = 21
)

const (
	numChars     = 256
	minMatch     = 2
	maxMatch     = 256 // 257 would need the LZXD extra-length field
	numLengths   = 249
	numAligned   = 8
	numPretree   = 20
	maxCodeLen   = 16
	maxPretree   = 15 // stored in 4 bits
	maxAligned   = 7  // stored in 3 bits
	blockVerb    = 1
	blockAligned = 2
	blockStored  = 3
	// e8Limit is where E8 translation stops: after 32768 frames (1 GB).
	e8Limit = 1 << 30
)

// footerBits and basePosition describe the position slots ([MS-PATCH] 2.6.2):
// slot s >= 4 has min((s-2)/2, 17) footer bits, and each base is the previous
// base plus the previous slot's range.
//
//nolint:gochecknoglobals // read-only lookup tables
var (
	footerBits = [50]uint32{
		0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11,
		12, 12, 13, 13, 14, 14, 15, 15, 16, 16, 17, 17, 17, 17, 17, 17, 17, 17, 17, 17, 17, 17, 17, 17,
	}
	basePosition = [50]uint32{
		0, 1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512, 768, 1024, 1536,
		2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768, 49152, 65536, 98304, 131072,
		196608, 262144, 393216, 524288, 655360, 786432, 917504, 1048576, 1179648, 1310720,
		1441792, 1572864, 1703936, 1835008, 1966080,
	}
)

// numSlots is the number of position slots for a window of 2^window bytes.
func numSlots(window int) int {
	switch window {
	case 20:
		return 42
	case 21:
		return 50
	default:
		return 2 * window
	}
}

func checkWindow(window int) error {
	if window < MinWindow || window > MaxWindow {
		return fmt.Errorf("lzx: window 2^%d, want 2^%d to 2^%d", window, MinWindow, MaxWindow)
	}

	return nil
}

// slotOf returns the position slot of a formatted offset.
func slotOf(formatted uint32) int {
	switch {
	case formatted < 4:
		return int(formatted)
	case formatted >= 1<<18:
		// Slots 36 and up all have 17 footer bits.
		return 36 + int((formatted-1<<18)>>17)
	default:
		// Below that, the base positions double every two slots.
		high := bits.Len32(formatted) - 1

		return 2*high + int(formatted>>(high-1)&1)
	}
}

// e8 translates (encode true) or restores (encode false) the x86 call
// operands of one frame starting at offset in the folder ([MS-PATCH] 2.2.2).
// The translation size is the header's unsigned 32-bit value.
func e8(frame []byte, offset int, size uint32, encode bool) {
	if offset >= e8Limit || len(frame) <= 10 {
		return
	}

	for i := 0; i < len(frame)-10; i++ {
		if frame[i] != 0xE8 {
			continue
		}

		value := int32(binary.LittleEndian.Uint32(frame[i+1:])) //nolint:gosec // reinterpreting the operand

		if out, ok := e8Value(int64(value), int64(offset+i), int64(size), encode); ok {
			binary.LittleEndian.PutUint32(frame[i+1:], uint32(out)) //nolint:gosec // the operand modulo 2^32
		}

		i += 4
	}
}

// e8Value maps one call operand, read as signed 32-bit, at position cur;
// ok is false when the operand stays as it is. The arithmetic runs in 64
// bits, since the translation size reaches 2^32-1 and value + size does not
// fit 32 bits; the result is written back modulo 2^32.
func e8Value(value, cur, size int64, encode bool) (int64, bool) {
	if value < -cur || value >= size {
		return 0, false
	}

	switch {
	case encode && value < size-cur:
		return value + cur, true
	case encode:
		return value - size, true
	case value >= 0:
		return value - cur, true
	default:
		return value + size, true
	}
}
