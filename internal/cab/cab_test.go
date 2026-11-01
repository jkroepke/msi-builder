package cab //nolint:testpackage // the tests reach unexported encoders

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand"
	"testing"
	"time"
)

func TestChecksumVector(t *testing.T) {
	t.Parallel()

	// The vector is the CFDATA checksum of a stored 22-byte block.
	b := []byte("Hello, cabinet!\r\nMSI\r\n")
	if got := Checksum(b, 0) ^ (uint32(len(b)) | uint32(len(b))<<16); got != 0x25411F35 { //nolint:gosec // bounded by the format limits checked earlier
		t.Errorf("checksum %#x, want 0x25411F35", got)
	}
}

// Test vector A.4 (stored cabinet) of docs/spec/MSISPEC.md section 5.
func TestStoredVector(t *testing.T) {
	t.Parallel()

	when := time.Date(2024, 6, 15, 13, 45, 30, 0, time.UTC)
	files := []File{
		{Name: "fil_a", Data: []byte("Hello, cabinet!\r\n"), Time: when, Attributes: 0x20},
		{Name: "fil_b", Data: []byte("MSI\r\n"), Time: when, Attributes: 0x21},
	}

	out, err := Write(files, Options{Compression: None, SetID: 0x1234})
	if err != nil {
		t.Fatal(err)
	}

	if len(out) != 118 {
		t.Errorf("%d bytes, want 118", len(out))
	}

	sum := sha256.Sum256(out)
	if got := hex.EncodeToString(sum[:]); got != "81e6018c16afa762ca19768c9c3ce0eed5aa44b5ef26ea555da92a248bbe4ace" {
		t.Errorf("SHA-256 %s", got)
	}

	back, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}

	if len(back) != 2 || back[0].Name != "fil_a" || string(back[1].Data) != "MSI\r\n" || back[1].Attributes != 0x21 || !back[0].Time.Equal(when) {
		t.Errorf("round trip: %+v", back)
	}
}

func TestMSZIPCrossBlockHistory(t *testing.T) {
	t.Parallel()

	// Three blocks with matches that reach back across block boundaries.
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // reproducible test data
	pattern := make([]byte, 5000)
	rng.Read(pattern)

	var data []byte
	for len(data) < 70000 {
		data = append(data, pattern...)
		data = append(data, byte(len(data))) //nolint:gosec // truncation is intended
	}

	files := []File{{Name: "fil_readme", Data: []byte("README: cross-block MSZIP\r\n")}, {Name: "fil_data", Data: data}}

	out, err := Write(files, Options{Compression: MSZIP})
	if err != nil {
		t.Fatal(err)
	}

	if len(out) >= len(data)/2 {
		t.Errorf("%d bytes: compression did not use the cross-block history", len(out))
	}

	back, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(back[1].Data, data) || !bytes.Equal(back[0].Data, files[0].Data) {
		t.Error("round trip differs")
	}

	again, _ := Write(files, Options{Compression: MSZIP})
	if !bytes.Equal(again, out) {
		t.Error("not deterministic")
	}
}

func TestStoredFallbackOnRandomData(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(3)) //nolint:gosec // reproducible test data
	data := make([]byte, 40000)
	rng.Read(data)

	out, err := Write([]File{{Name: "r", Data: data}}, Options{Compression: MSZIP})
	if err != nil {
		t.Fatal(err)
	}

	// First block: 8-byte CFDATA header after 36 + 8 + 17 + 1 bytes.
	p := 36 + 8 + 17 + 1
	cb := int(out[p+4]) | int(out[p+5])<<8

	if cb != 32768+7 {
		t.Errorf("first block has %d bytes, want the stored fallback of 32775", cb)
	}

	back, err := Read(out)
	if err != nil || !bytes.Equal(back[0].Data, data) {
		t.Errorf("round trip failed: %v", err)
	}
}

func TestEmptyFileAndEmptyFolder(t *testing.T) {
	t.Parallel()

	out, err := Write([]File{{Name: "empty"}}, Options{Compression: MSZIP})
	if err != nil {
		t.Fatal(err)
	}

	// cCFData must be 0 for an empty folder.
	if out[36+4] != 0 || out[36+5] != 0 {
		t.Error("empty folder has data blocks")
	}

	back, err := Read(out)
	if err != nil || len(back) != 1 || len(back[0].Data) != 0 {
		t.Errorf("round trip: %v %+v", err, back)
	}
}

func TestChecksumDetectsBitFlip(t *testing.T) {
	t.Parallel()

	out, _ := Write([]File{{Name: "a", Data: bytes.Repeat([]byte("abc"), 1000)}}, Options{Compression: MSZIP})
	out[len(out)-1] ^= 0x40

	if _, err := Read(out); err == nil {
		t.Error("bit flip not detected")
	}
}

func TestDOSTime(t *testing.T) {
	t.Parallel()

	date, timeField := DOSTime(time.Time{})
	if date != 0x0021 || timeField != 0 {
		t.Errorf("zero time: %#x %#x", date, timeField)
	}

	when := time.Date(2026, 10, 25, 18, 33, 39, 0, time.UTC)
	date, timeField = DOSTime(when)

	if got := ParseDOSTime(date, timeField); !got.Equal(when.Truncate(2 * time.Second)) {
		t.Errorf("round trip %v", got)
	}

	// Local time must not leak in.
	d2, tm2 := DOSTime(when.In(time.FixedZone("x", 3600)))
	if d2 != date || tm2 != timeField {
		t.Error("time zone changed the DOS time")
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()

	if _, err := Write([]File{{Name: ""}}, Options{}); err == nil {
		t.Error("empty name accepted")
	}

	if _, err := Write([]File{{Name: "x"}}, Options{Compression: 2}); err == nil {
		t.Error("Quantum accepted")
	}

	if _, err := Write([]File{{Name: "x"}}, Options{Compression: LZX, Window: 14}); err == nil {
		t.Error("LZX window 2^14 accepted")
	}

	if _, err := Read([]byte("MSCF")); err == nil {
		t.Error("truncated cabinet accepted")
	}
}

// TestLZXIncompressibleShift writes data whose first frame would exceed the
// CFDATA limit when compressed with its block's trees.
func TestLZXIncompressibleShift(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(2)) //nolint:gosec // reproducible test data
	data := make([]byte, 4*blockSize)

	for i := range data {
		if i < blockSize {
			data[i] = byte(101 + rng.Intn(155)) //nolint:gosec // below 256
		} else {
			data[i] = byte(rng.Intn(101)) //nolint:gosec // below 101
		}
	}

	out, err := Write([]File{{Name: "shift", Data: data}}, Options{Compression: LZX})
	if err != nil {
		t.Fatal(err)
	}

	back, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(back[0].Data, data) {
		t.Fatal("round trip differs")
	}
}

func TestLZXRoundTrip(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(3)) //nolint:gosec // reproducible test data
	random := make([]byte, 50000)
	rng.Read(random)

	text := bytes.Repeat([]byte("LZX matches reach back across CFDATA blocks.\r\n"), 3000)
	files := []File{
		{Name: "text", Data: text},
		{Name: "empty"},
		{Name: "random", Data: random},
		{Name: "again", Data: text[:40000]},
	}

	windows := []int{0, 15, 18}
	if raceEnabled {
		windows = []int{15} // CI runs all of them without -race
	}

	for _, window := range windows {
		out, err := Write(files, Options{Compression: LZX, Window: window})
		if err != nil {
			t.Fatal(err)
		}

		want := uint16(0x1503)
		if window != 0 {
			want = uint16(3 | window<<8) //nolint:gosec // 15 to 21
		}

		if got := uint16(out[headerSize+6]) | uint16(out[headerSize+7])<<8; got != want {
			t.Errorf("window %d: typeCompress %#x, want %#x", window, got, want)
		}

		back, err := Read(out)
		if err != nil {
			t.Fatal(err)
		}

		for i, f := range back {
			if f.Name != files[i].Name || !bytes.Equal(f.Data, files[i].Data) {
				t.Errorf("window %d: file %d differs", window, i)
			}
		}
	}
}

// TestWorkersSameCabinet checks that the MSZIP cabinet does not depend on how
// many blocks are compressed at once, including batches that end mid-folder.
func TestWorkersSameCabinet(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(4)) //nolint:gosec // reproducible test data
	random := make([]byte, 70000)
	rng.Read(random)

	text := bytes.Repeat([]byte("MSZIP blocks keep the chunk before them as history.\r\n"), 9000)
	files := []File{{Name: "text", Data: text}, {Name: "random", Data: random}, {Name: "tail", Data: text[:5]}}

	for _, comp := range []Compression{MSZIP, None} {
		want, err := Write(files, Options{Compression: comp, Workers: 1})
		if err != nil {
			t.Fatal(err)
		}

		for _, workers := range []int{2, 3, 4, 16} {
			got, err := Write(files, Options{Compression: comp, Workers: workers})
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, want) {
				t.Errorf("compression %d, %d workers: the cabinet differs from one worker", comp, workers)
			}
		}

		back, err := Read(want)
		if err != nil {
			t.Fatal(err)
		}

		for i, f := range back {
			if !bytes.Equal(f.Data, files[i].Data) {
				t.Errorf("compression %d: file %d differs", comp, i)
			}
		}
	}
}

// TestBound checks that Bound is never below what WriteTo writes, for random
// data that does not compress and for data that does, and that it is exact
// for stored folders.
func TestBound(t *testing.T) {
	t.Parallel()

	random := make([]byte, 100000)
	rand.New(rand.NewSource(7)).Read(random) //nolint:gosec // test data

	for _, comp := range []Compression{None, MSZIP, LZX} {
		for _, data := range [][]byte{random, bytes.Repeat([]byte("msi"), 30000)} {
			files := []File{{Name: "first.bin", Data: data}, {Name: "b", Data: data[:5]}}

			out, err := Write(files, Options{Compression: comp})
			if err != nil {
				t.Fatal(err)
			}

			bound := Bound(len(files), 1, int64(len("first.bin")+len("b")), int64(len(data)+5), comp)
			if int64(len(out)) > bound || comp == None && int64(len(out)) != bound {
				t.Errorf("compression %d: wrote %d bytes, bound %d", comp, len(out), bound)
			}
		}
	}
}

// TestSignatureReserve writes the zeroed per-cabinet reserve a signature
// header fills, and reads the cabinet back with and without trailing
// signature bytes.
func TestSignatureReserve(t *testing.T) {
	t.Parallel()

	files := []File{{Name: "a.txt", Data: []byte("reserved")}, {Name: "b.txt", Data: bytes.Repeat([]byte("x"), 70000)}}

	plain, err := Write(files, Options{Compression: MSZIP})
	if err != nil {
		t.Fatal(err)
	}

	out, err := Write(files, Options{Compression: MSZIP, SignatureReserve: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(out) != len(plain)+SignatureReserve {
		t.Fatalf("%d bytes, want %d plus the reserve", len(out), len(plain))
	}

	order := binary.LittleEndian
	if flags := order.Uint16(out[0x1E:]); flags != flagReserve {
		t.Errorf("flags %#x, want %#x", flags, flagReserve)
	}

	if got := out[headerSize : headerSize+SignatureReserve]; !bytes.Equal(got, append([]byte{20, 0, 0, 0}, make([]byte, 20)...)) {
		t.Errorf("reserve % x", got)
	}

	if got := order.Uint32(out[8:]); int(got) != len(out) {
		t.Errorf("cbCabinet %d, want %d", got, len(out))
	}

	// A signature follows cbCabinet; readers go by the offsets.
	for _, data := range [][]byte{out, append(bytes.Clone(out), make([]byte, 64)...)} {
		got, err := Read(data)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 || !bytes.Equal(got[1].Data, files[1].Data) {
			t.Errorf("read back %d files", len(got))
		}
	}

	bad := bytes.Clone(out)
	bad[headerSize+2] = 1

	if _, err := Read(bad); err == nil {
		t.Error("a per-folder reserve was accepted")
	}
}
