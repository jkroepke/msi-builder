//go:build windows && (amd64 || arm64)

package cab_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cab"
)

// readLZXFixture reads a stream crafted by internal/lzx's TestWindowsFixtures:
// the window as one byte, the uncompressed size, the SHA-256 of the
// uncompressed data, the frame count, then each frame's length and bytes;
// numbers are little-endian 32-bit.
func readLZXFixture(t *testing.T, path string) (int, int, [sha256.Size]byte, [][]byte) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	window := int(raw[0])
	size := int(binary.LittleEndian.Uint32(raw[1:]))
	sum := [sha256.Size]byte(raw[5 : 5+sha256.Size])
	raw = raw[5+sha256.Size:]

	count := binary.LittleEndian.Uint32(raw)
	raw = raw[4:]

	frames := make([][]byte, count)
	for i := range frames {
		length := binary.LittleEndian.Uint32(raw)
		frames[i] = raw[4 : 4+length]
		raw = raw[4+length:]
	}

	return window, size, sum, frames
}

// lzxCabinet wraps LZX frames into a cabinet of one folder and one file.
func lzxCabinet(window, size int, frames [][]byte) []byte {
	const name = "data.bin"

	headerSize := 36 + 8
	fileSize := 16 + len(name) + 1

	out := []byte("MSCF")
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint32(out, 0) // cbCabinet, set below
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = binary.LittleEndian.AppendUint32(out, 36+8) // coffFiles
	out = binary.LittleEndian.AppendUint32(out, 0)
	out = append(out, 3, 1)                        // version 1.3
	out = binary.LittleEndian.AppendUint16(out, 1) // cFolders
	out = binary.LittleEndian.AppendUint16(out, 1) // cFiles
	out = binary.LittleEndian.AppendUint16(out, 0) // flags
	out = binary.LittleEndian.AppendUint16(out, 0) // setID
	out = binary.LittleEndian.AppendUint16(out, 0) // iCabinet
	out = binary.LittleEndian.AppendUint32(out, uint32(headerSize+fileSize))
	out = binary.LittleEndian.AppendUint16(out, uint16(len(frames)))               //nolint:gosec // small
	out = binary.LittleEndian.AppendUint16(out, uint16(cab.LZX)|uint16(window)<<8) //nolint:gosec // 15 to 21
	out = binary.LittleEndian.AppendUint32(out, uint32(size))                      //nolint:gosec // small
	out = binary.LittleEndian.AppendUint32(out, 0)                                 // uoffFolderStart
	out = binary.LittleEndian.AppendUint16(out, 0)                                 // iFolder
	out = binary.LittleEndian.AppendUint16(out, 0x5821)                            // 2024-01-01
	out = binary.LittleEndian.AppendUint16(out, 0)
	out = binary.LittleEndian.AppendUint16(out, cab.AttrArchive)
	out = append(out, name+"\x00"...)

	for i, frame := range frames {
		frameSize := min(lzxFrameSize, size-i*lzxFrameSize)

		var rec [8]byte

		binary.LittleEndian.PutUint16(rec[4:], uint16(len(frame))) //nolint:gosec // at most 38912
		binary.LittleEndian.PutUint16(rec[6:], uint16(frameSize))  //nolint:gosec // at most 32768
		binary.LittleEndian.PutUint32(rec[:], cab.Checksum(frame, 0)^binary.LittleEndian.Uint32(rec[4:]))

		out = append(out, rec[:]...)
		out = append(out, frame...)
	}

	binary.LittleEndian.PutUint32(out[8:], uint32(len(out))) //nolint:gosec // small

	return out
}

const lzxFrameSize = 32768

// TestNativeFDICraftedLZX extracts LZX streams that msi-builder's encoder
// never writes through cabinet.dll and through cab.Read, which must agree
// byte for byte: an odd stored block that ends at a frame's end, whose pad
// byte opens the next frame, repeated offsets from a stored block header
// that reach the window less 3 to the full window and an E8 translation size
// of 2^31. Both must reject the reject-* streams (incomplete Huffman trees).
// cabinet.dll extracts the lenient-* streams (a match at the window plus
// one, read as offset 1), which the reader rejects. The reader rejects the
// varies-* streams (a match before the stream's start), where cabinet.dll's
// outcome depends on the Windows version: windows-latest CI rejected it,
// Windows Server 2025 extracted it (2026-11-01).
func TestNativeFDICraftedLZX(t *testing.T) { //nolint:paralleltest // the FDI callbacks share one package state
	if testing.Short() {
		t.Skip("native tests run without -short")
	}

	paths, err := filepath.Glob(filepath.Join("..", "lzx", "testdata", "windows", "*.lzx"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no LZX fixtures: %v", err)
	}

	for _, path := range paths { //nolint:paralleltest // shared FDI state
		t.Run(strings.TrimSuffix(filepath.Base(path), ".lzx"), func(t *testing.T) {
			window, size, sum, frames := readLZXFixture(t, path)
			raw := lzxCabinet(window, size, frames)

			got, err := fdiExtract(t, raw)

			if strings.HasPrefix(filepath.Base(path), "lenient-") {
				if err != nil {
					t.Fatalf("cabinet.dll rejects it now: %v", err)
				}

				if _, readErr := cab.Read(raw); readErr == nil {
					t.Fatal("cab.Read accepted a stream it rejects by design")
				}

				// The lenient streams end in a two-byte match at the window
				// plus one, which cabinet.dll copies as offset 1.
				if len(got) != 1 || len(got[0].Data) != size {
					t.Fatal(describeExtract(got, size))
				}

				if data := got[0].Data; data[size-1] != data[size-3] || data[size-2] != data[size-3] {
					t.Fatal("cabinet.dll no longer copies the match as offset 1")
				}

				t.Logf("%s: cabinet.dll extracts it with offset 1, cab.Read rejects it", filepath.Base(path))

				return
			}

			if strings.HasPrefix(filepath.Base(path), "varies-") {
				if _, readErr := cab.Read(raw); readErr == nil {
					t.Fatal("cab.Read accepted a stream it rejects by design")
				}

				t.Logf("%s: cab.Read rejects it; cabinet.dll: extracted %d files, error %v", filepath.Base(path), len(got), err)

				return
			}

			if strings.HasPrefix(filepath.Base(path), "reject-") {
				if err == nil {
					t.Fatal("cabinet.dll extracted a stream it must reject")
				}

				if _, readErr := cab.Read(raw); readErr == nil {
					t.Fatalf("cab.Read accepted a stream cabinet.dll rejects (%v)", err)
				}

				t.Logf("%s: cabinet.dll and cab.Read reject it; cabinet.dll: %v", filepath.Base(path), err)

				return
			}

			if err != nil {
				t.Fatalf("cabinet.dll: %v", err)
			}

			if len(got) != 1 || len(got[0].Data) != size || sha256.Sum256(got[0].Data) != sum {
				t.Fatal(describeExtract(got, size))
			}

			files, err := cab.Read(raw)
			if err != nil {
				t.Fatalf("cab.Read: %v", err)
			}

			if len(files) != 1 || !bytes.Equal(files[0].Data, got[0].Data) {
				t.Fatal("cab.Read differs from cabinet.dll")
			}

			t.Logf("%s: cabinet.dll and cab.Read extract the same %d bytes", filepath.Base(path), size)
		})
	}
}

func describeExtract(got []extracted, size int) string {
	if len(got) != 1 {
		return fmt.Sprintf("cabinet.dll extracted %d files, want 1", len(got))
	}

	return fmt.Sprintf("cabinet.dll wrote %d bytes, want %d with the fixture's SHA-256", len(got[0].Data), size)
}
