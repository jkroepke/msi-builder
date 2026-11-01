//go:build windows

package pe //nolint:testpackage // temporary probe

import (
	"os"
	"testing"
)

// TestProbeMsiVersion reports the runner's msi.dll version and its answer
// for a 5-byte Translation value. Temporary: it fails on purpose so the
// values show in the CI log; removed before review.
func TestProbeMsiVersion(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + `\partial5.exe`
	writeProbe(t, path, partialTranslation(t, 5))

	ver, lang, ok := msiFileVersion(t, path)
	t.Errorf("PROBE msi.dll %s: length 5 gave ok=%v %q %q", msiVersion(t), ok, ver, lang)
}

func writeProbe(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
