package cadll_test

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/cadll"
)

// TestBinaries checks every committed DLL against SHA256SUMS and inspects
// its PE headers: the machine of its platform, the exported entry point and
// no import beyond kernel32, advapi32, msi, authz, ntdll, user32 and
// rstrtmgr.
func TestBinaries(t *testing.T) {
	t.Parallel()

	sums := map[string]string{}

	for line := range strings.Lines(readFile(t, "SHA256SUMS")) {
		if sum, name, ok := strings.Cut(strings.TrimSpace(line), "  "); ok {
			sums[name] = sum
		}
	}

	for _, tc := range []struct {
		platform, file string
		machine        uint16
	}{
		{"Intel", "builderrun-x86.dll", pe.IMAGE_FILE_MACHINE_I386},
		{"x64", "builderrun-x64.dll", pe.IMAGE_FILE_MACHINE_AMD64},
		{"Arm64", "builderrun-arm64.dll", pe.IMAGE_FILE_MACHINE_ARM64},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			t.Parallel()

			data, err := cadll.Binary(tc.platform)
			if err != nil {
				t.Fatal(err)
			}

			if got := hex.EncodeToString(sha256Sum(data)); got != sums[tc.file] {
				t.Errorf("%s: sha256 %s, SHA256SUMS has %s; rerun build.sh and commit both", tc.file, got, sums[tc.file])
			}

			img, err := pe.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}

			if img.Machine != tc.machine {
				t.Errorf("machine %#x, want %#x", img.Machine, tc.machine)
			}

			if img.Characteristics&pe.IMAGE_FILE_DLL == 0 {
				t.Error("not a DLL")
			}

			symbols, err := img.ImportedSymbols()
			if err != nil {
				t.Fatal(err)
			}

			var libs []string

			for _, symbol := range symbols {
				if _, lib, ok := strings.Cut(symbol, ":"); ok && !slices.Contains(libs, lib) {
					libs = append(libs, lib)
				}
			}

			slices.Sort(libs)

			if want := []string{"advapi32.dll", "authz.dll", "kernel32.dll", "msi.dll", "ntdll.dll", "rstrtmgr.dll", "user32.dll"}; !slices.Equal(libs, want) {
				t.Errorf("imports %v, want %v", libs, want)
			}

			for _, entry := range []string{
				cadll.Entry, cadll.GrantEntryPoint, cadll.CloseEntryPoint, cadll.CloseSchedule,
				cadll.RemoveFoldersExec, cadll.RestartRegister,
			} {
				if !bytes.Contains(data, []byte(entry+"\x00")) {
					t.Errorf("export %s not found", entry)
				}
			}

			// The engine builds the grant and folder removal data (spec
			// 17.7, 17.10); an action that runs as the user must not.
			for _, gone := range []string{"BuilderGrantSchedule", cadll.RemoveFolders} {
				if bytes.Contains(data, []byte(gone+"\x00")) {
					t.Errorf("export %s is back", gone)
				}
			}

			// The log template and the immediate property are UTF-16 arrays
			// in the C source, which a rename of the Go constants misses.
			for _, want := range []string{cadll.Entry + ": [1]", cadll.GrantEntryPoint + ": [1]", cadll.DataProperty,
				cadll.CloseEntryPoint + ": [1]", cadll.CloseProperty,
				cadll.RemoveFolders + ": [1]", "BuilderRestart: [1]", cadll.RestartProperty,
			} {
				if !bytes.Contains(data, utf16z(want)) {
					t.Errorf("UTF-16 string %q not found", want)
				}
			}
		})
	}

	if _, err := cadll.Binary("Intel64"); err == nil {
		t.Error("Intel64 has no DLL and should fail")
	}
}

func TestData(t *testing.T) {
	t.Parallel()

	if got, want := cadll.Data(`C:\App\`, `"C:\App\a.exe" x`), `C:\App\|"C:\App\a.exe" x`; got != want {
		t.Errorf("Data = %q, want %q", got, want)
	}

	if got, want := cadll.Data("", "cmd"), "|cmd"; got != want {
		t.Errorf("Data = %q, want %q", got, want)
	}
}

func TestRestartEntry(t *testing.T) {
	t.Parallel()

	if got, want := cadll.RestartEntry("$C=2", cadll.RestartFile, "[A]b.dll"), "$C=2\x1ff\x1f[A]b.dll"; got != want {
		t.Errorf("RestartEntry = %q, want %q", got, want)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// utf16z encodes an ASCII string as NUL-terminated UTF-16LE.
func utf16z(s string) []byte {
	out := make([]byte, 0, 2*len(s)+2)
	for _, c := range []byte(s) {
		out = append(out, c, 0)
	}

	return append(out, 0, 0)
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)

	return sum[:]
}
