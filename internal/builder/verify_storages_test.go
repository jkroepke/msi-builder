package builder //nolint:testpackage // the tests reach the unexported comparison

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cfb"
)

// TestCompareStorages checks that the read-back verify notices a missing,
// changed or extra transform storage.
func TestCompareStorages(t *testing.T) {
	t.Parallel()

	storage := func(name string, clsid byte, sizes ...int) cfb.Storage {
		out := cfb.Storage{Name: name, CLSID: [16]byte{clsid}}
		for i, size := range sizes {
			stream := cfb.Stream{Name: string(rune('A' + i)), Data: bytes.Repeat([]byte{'x'}, size)}
			out.Streams = append(out.Streams, stream.Source())
		}

		return out
	}

	changed := storage("1031", 1, 10, 20)
	changed.Streams[1] = cfb.Stream{Name: "B", Data: bytes.Repeat([]byte{'y'}, 20)}.Source()

	want := []cfb.Storage{storage("1031", 1, 10, 20)}

	if err := compareStorages(want, []cfb.Storage{storage("1031", 1, 10, 20)}); err != nil {
		t.Errorf("identical storages: %v", err)
	}

	for name, tc := range map[string]struct {
		got  []cfb.Storage
		want string
	}{
		"missing":      {nil, "0 storages"},
		"renamed":      {[]cfb.Storage{storage("1036", 1, 10, 20)}, "is missing"},
		"extra":        {[]cfb.Storage{storage("1031", 1, 10, 20), storage("1036", 1)}, "2 storages"},
		"CLSID":        {[]cfb.Storage{storage("1031", 2, 10, 20)}, "CLSID"},
		"stream size":  {[]cfb.Storage{storage("1031", 1, 10, 21)}, "stream"},
		"stream count": {[]cfb.Storage{storage("1031", 1, 10)}, "1 streams"},
		"content":      {[]cfb.Storage{changed}, "other bytes"},
	} {
		if err := compareStorages(want, tc.got); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, tc.want)
		}
	}
}
