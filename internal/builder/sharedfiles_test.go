package builder_test

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// sharedConfig is the minimal example with a.bin and c.bin holding the same
// 100000 bytes, b.bin holding other bytes, and d.bin and e.bin the same 1000
// bytes, below the size whose copies share data. It returns the inputs too.
func sharedConfig(t *testing.T) (*config.Config, map[string][]byte) {
	t.Helper()

	rng := rand.New(rand.NewSource(11)) //nolint:gosec // test data
	random := func(n int) []byte {
		data := make([]byte, n)
		rng.Read(data)

		return data
	}

	same, small := random(100000), random(1000)
	inputs := map[string][]byte{
		"hello.exe": testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}}),
		"a.bin":     same,
		"b.bin":     random(50000),
		"c.bin":     same,
		"d.bin":     small,
		"e.bin":     small,
	}

	cfg := example(t, "minimal", nil, inputs)
	cfg.Package.Compression = "none"

	for _, name := range []string{"a.bin", "b.bin", "c.bin", "d.bin", "e.bin"} {
		cfg.Components[0].Files = append(cfg.Components[0].Files, &config.File{Source: name})
	}

	return cfg, inputs
}

// cabinetStreams returns the cabinet streams of the Media rows in order.
func cabinetStreams(t *testing.T, pkg []byte) [][]byte {
	t.Helper()

	database, err := msidb.Unmarshal(pkg)
	if err != nil {
		t.Fatal(err)
	}

	media := mediaRows(t, database)
	out := make([][]byte, 0, len(media))

	for _, row := range media {
		data, ok := database.Stream(row[2].(string)[1:]) //nolint:forcetypeassert // the rows name embedded cabinets
		if !ok {
			t.Fatalf("cabinet stream %v missing", row[2])
		}

		out = append(out, data)
	}

	return out
}

// checkCabinetFiles checks that the cabinets hold every payload file under
// its key with its bytes.
func checkCabinetFiles(t *testing.T, cabinets [][]byte, inputs map[string][]byte) {
	t.Helper()

	count := 0

	for _, raw := range cabinets {
		files, err := cab.Read(raw)
		if err != nil {
			t.Fatal(err)
		}

		for _, file := range files {
			count++

			if want, ok := inputs[file.Name]; !ok || !bytes.Equal(file.Data, want) {
				t.Errorf("cabinet entry %s: %d bytes, not its source", file.Name, len(file.Data))
			}
		}
	}

	if count != len(inputs) {
		t.Errorf("the cabinets hold %d files, want %d", count, len(inputs))
	}
}

// TestIdenticalFilesShareData checks that a file with the same bytes as an
// earlier one of its cabinet is stored once (section 5) while every File
// row keeps its own cabinet entry, that small copies are stored again, and
// that a copy in another cabinet than its original stores the bytes there.
func TestIdenticalFilesShareData(t *testing.T) {
	t.Parallel()

	cfg, inputs := sharedConfig(t)

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	cabinets := cabinetStreams(t, res.Package)
	if len(cabinets) != 1 {
		t.Fatalf("%d cabinets, want 1", len(cabinets))
	}

	checkCabinetFiles(t, cabinets, inputs)

	// Stored: a.bin, b.bin, d.bin, e.bin and hello.exe, but not c.bin;
	// a.bin is a folder of its own, b.bin and the rest one each.
	stored := 0

	for name, data := range inputs {
		if name != "c.bin" {
			stored += len(data)
		}
	}

	raw := cabinets[0]
	if folders := binary.LittleEndian.Uint16(raw[0x1A:]); folders != 3 {
		t.Errorf("%d folders, want 3", folders)
	}

	if len(raw) > stored+2000 {
		t.Errorf("the cabinet has %d bytes for %d stored bytes", len(raw), stored)
	}

	// A limit that holds a.bin but not a.bin and b.bin puts a.bin, b.bin
	// and c.bin into cabinets of their own: c.bin is no copy in its
	// cabinet, which stores its bytes again.
	res, err = builder.Build(cfg, builder.Options{BuildTime: buildTime, CabinetSize: 120000})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, res.Diagnostics)
	}

	cabinets = cabinetStreams(t, res.Package)
	checkCabinetFiles(t, cabinets, inputs)

	if len(cabinets) != 3 {
		t.Fatalf("%d cabinets, want 3", len(cabinets))
	}

	if files, _ := cab.Read(cabinets[2]); len(files) == 0 || files[0].Name != "c.bin" || len(cabinets[2]) < len(inputs["c.bin"]) {
		t.Errorf("the third cabinet has %d bytes and starts with %v, want c.bin stored", len(cabinets[2]), files)
	}
}

// TestSharedDataCabinetLimits builds the shared configuration stored, with
// MSZIP and with LZX under a range of cabinet limits and checks that no cabinet
// exceeds its limit: the split must count every folder sharing adds.
func TestSharedDataCabinetLimits(t *testing.T) {
	t.Parallel()

	cfg, inputs := sharedConfig(t)

	for _, compression := range []string{"none", "mszip", "lzx", "lzx:15"} {
		cfg.Package.Compression = compression

		for limit := int64(100000); limit <= 400000; limit += 7919 {
			res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime, CabinetSize: limit})
			if err != nil {
				t.Fatalf("%s, limit %d: %v\n%s", compression, limit, err, res.Diagnostics)
			}

			cabinets := cabinetStreams(t, res.Package)
			checkCabinetFiles(t, cabinets, inputs)

			// A file above the limit alone gets a cabinet of its own.
			for i, raw := range cabinets {
				if files, _ := cab.Read(raw); int64(len(raw)) > limit && len(files) > 1 {
					t.Errorf("%s, limit %d: cabinet %d has %d bytes", compression, limit, i+1, len(raw))
				}
			}
		}
	}
}
