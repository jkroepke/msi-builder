//go:build windows

package pe //nolint:testpackage // the cases reuse the unexported image builders

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

var (
	msiDLL             = syscall.NewLazyDLL("msi.dll")        //nolint:gochecknoglobals // one DLL table for the file
	pMsiGetFileVersion = msiDLL.NewProc("MsiGetFileVersionW") //nolint:gochecknoglobals // one DLL table for the file
)

// msiFileVersion returns what MsiGetFileVersionW reports for path: the
// version and language strings, and ok false when it reports no version.
func msiFileVersion(t *testing.T, path string) (string, string, bool) {
	t.Helper()

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	const verSize, langSize = 256, 1024

	ver := make([]uint16, verSize)
	lang := make([]uint16, langSize)
	verLen, langLen := uint32(verSize), uint32(langSize)

	ret, _, _ := pMsiGetFileVersion.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&ver[0])), uintptr(unsafe.Pointer(&verLen)),
		uintptr(unsafe.Pointer(&lang[0])), uintptr(unsafe.Pointer(&langLen)),
	)

	switch syscall.Errno(ret) { //nolint:exhaustive // the other codes fail the test
	case 0:
		return syscall.UTF16ToString(ver), syscall.UTF16ToString(lang), true
	case syscall.Errno(1006), syscall.Errno(13): // ERROR_FILE_INVALID, ERROR_INVALID_DATA: no version
		return "", "", false
	default:
		t.Fatalf("MsiGetFileVersionW: %v", syscall.Errno(ret))

		return "", "", false
	}
}

// msiVersion returns the file version of the msi.dll in System32, read by
// this package.
func msiVersion(t *testing.T) string {
	t.Helper()

	file, err := os.Open(filepath.Join(os.Getenv("SystemRoot"), "System32", "msi.dll"))
	if err != nil {
		return err.Error()
	}

	defer file.Close()

	info, err := Parse(file)
	if err != nil {
		return err.Error()
	}

	return info.FileVersion
}

// TestMsiGetFileVersionAgrees parses well-formed and corrupt images with
// this package and with MsiGetFileVersionW, which fills File.Version and
// File.Language for Windows toolchains, and checks that both report the
// same version and languages, or both none (FILE-15).
func TestMsiGetFileVersionAgrees(t *testing.T) {
	t.Parallel()

	shrinkTranslation := func(t *testing.T) []byte {
		t.Helper()

		data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033, 1031}})

		binary.LittleEndian.PutUint16(data[blockOffset(t, data, "Translation"):], 36)

		return data
	}

	// cutTranslation ends the resource section's raw data after the first
	// of two languages, inside VirtualSize; rounded up to FileAlignment it
	// still holds both.
	cutTranslation := func(t *testing.T) []byte {
		t.Helper()

		data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033, 1031}})
		// Header 6, key 24 padded to 32, then the first language.
		cut := blockOffset(t, data, "Translation") + 36 - testRsrcRaw
		binary.LittleEndian.PutUint32(data[testRsrcSection+16:], uint32(cut)) //nolint:gosec // a few hundred bytes

		return data
	}

	versionHeader := func(field int, value uint16) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			t.Helper()

			data := testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
			binary.LittleEndian.PutUint16(data[blockOffset(t, data, "VS_VERSION_INFO")+field:], value)

			return data
		}
	}

	langs := func(languages ...int) func(*testing.T) []byte {
		return func(*testing.T) []byte {
			return testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: languages})
		}
	}

	moved := func(rawSize uint32) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			t.Helper()

			return movedVersion(t, rawSize)
		}
	}

	translationLen := func(n uint16) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			t.Helper()

			return partialTranslation(t, n)
		}
	}

	resources := func(entries ...resEntry) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			t.Helper()

			return versionResources(t, entries)
		}
	}

	patch := func(off int, value uint32) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			t.Helper()

			return corruptImage(t, off, value)
		}
	}

	cases := map[string]func(*testing.T) []byte{
		"well formed": func(*testing.T) []byte {
			return testpe.Build(testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "1.2.0.0", Languages: []int{1033, 1031}})
		},
		// The order in which Windows lists the languages (FILE-15).
		"languages 1031,1033":             langs(1031, 1033),
		"languages 1033,1031,2057":        langs(1033, 1031, 2057),
		"languages 2057,1033,1031":        langs(2057, 1033, 1031),
		"languages 1033,1033,1031":        langs(1033, 1033, 1031),
		"languages 1033,1031,1033":        langs(1033, 1031, 1033),
		"no translations":                 func(*testing.T) []byte { return testpe.Build(testpe.Options{FileVersion: "0.32.0.5"}) },
		"no version resource":             func(*testing.T) []byte { return testpe.Build(testpe.Options{}) },
		"resource section raw size 0":     patch(testRsrcSection+16, 0),
		"translation past its Var":        shrinkTranslation,
		"raw data ends in Translation":    cutTranslation,
		"record past rounded raw data":    moved(0x200),
		"record inside raw data":          moved(0x400),
		"resource section raw size 0x58":  patch(testRsrcSection+16, 0x58),
		"resource section raw size 0x60":  patch(testRsrcSection+16, 0x60),
		"Translation length 5":            translationLen(5),
		"Translation length 6":            translationLen(6),
		"Translation length 7":            translationLen(7),
		"version IDs 0 and 1":             resources(resEntry{0, 1033, "1.2.3.4", []int{1033}, nil}, resEntry{1, 1033, "9.8.7.6", []int{1033}, nil}),
		"version ID 2 only":               resources(resEntry{2, 1033, "1.2.3.4", []int{1033}, nil}),
		"two languages agree":             resources(resEntry{1, 1031, "1.2.3.4", []int{1031, 1033}, nil}, resEntry{1, 1033, "1.2.3.4", []int{1031, 1033}, nil}),
		"resources in the headers":        headerResources,
		"two Translation Vars":            resources(resEntry{id: 1, lang: 1033, blob: twoTranslations(t, true)}),
		"two VarFileInfo blocks":          resources(resEntry{id: 1, lang: 1033, blob: twoTranslations(t, false)}),
		"root entry counted as named":     patch(testRsrcRaw+12, 1),
		"name entry counted as named":     patch(testRsrcRaw+24+12, 1),
		"language entry counted as named": patch(testRsrcRaw+48+12, 1),
		"same version, own translations":  resources(resEntry{1, 1031, "1.2.3.4", []int{1031}, nil}, resEntry{1, 1033, "1.2.3.4", []int{1033}, nil}),
		"neutral and English entries":     resources(resEntry{1, 0, "1.2.3.4", []int{1031}, nil}, resEntry{1, 1033, "1.2.3.4", []int{1033}, nil}),
		"65 languages":                    resources(manyLanguages(65)...),
		"FileAlignment 0":                 patch(0x80+24+36, 0),
		"product versions differ": resources(
			resEntry{1, 1031, "", nil, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "1.0.0.0", Languages: []int{1031}})},
			resEntry{1, 1033, "", nil, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", ProductVersion: "2.0.0.0", Languages: []int{1033}})}),
		"resource section raw size 1":   patch(testRsrcSection+16, 1),
		"NumberOfRvaAndSizes 0":         patch(testResourceDir-20, 0),
		"NumberOfRvaAndSizes 2":         patch(testResourceDir-20, 2),
		"wValueLength 65535":            versionHeader(2, 0xFFFF),
		"root wType text":               versionHeader(4, 1),
		"wLength 65535":                 versionHeader(0, 0xFFFF),
		"version data size 512":         patch(testDataEntry+4, 512),
		"version data size short":       patch(testDataEntry+4, 0x20),
		"resource directory size 0":     patch(testResourceDir+4, 0),
		"resource directory size huge":  patch(testResourceDir+4, 0x7FFFFFFF),
		"resource directory size wraps": patch(testResourceDir+4, 0xFFFFF000),
	}

	// Windows versions differ on a partial last Translation entry: msi.dll
	// 5.0.26100.1742 counted one whose language word starts inside the
	// value (length 5 gave 1031,1033), the windows-latest runner did not
	// (1033). Parse follows the runner; the other answer is accepted and
	// logged with the msi.dll version.
	// Each maps the other Windows answer to the one Parse gives.
	alternative := map[string][2]string{"Translation length 5": {"1031,1033", "1033"}}

	t.Logf("msi.dll %s", msiVersion(t))

	dir := t.TempDir()

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data := build(t)

			path := filepath.Join(dir, filepath.Base(t.Name())+".exe")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			wantVer, wantLang, wantOK := msiFileVersion(t, path)

			info, err := Parse(bytes.NewReader(data))
			if err != nil && !errors.Is(err, ErrNotPE) {
				t.Logf("Parse: %v", err)
			}

			gotOK := err == nil && info.HasVersion

			t.Logf("Windows: ok=%v version %q language %q", wantOK, wantVer, wantLang)

			if gotOK != wantOK {
				t.Fatalf("Parse reports a version: %v, MsiGetFileVersionW: %v (%+v)", gotOK, wantOK, info)
			}

			if alt, ok := alternative[name]; ok && wantLang == alt[0] {
				t.Logf("Windows gave the alternative %q; Parse must give %q", alt[0], alt[1])

				wantLang = alt[1]
			}

			if gotOK && (info.FileVersion != wantVer || info.Language() != wantLang) {
				t.Errorf("Parse: %q %q, MsiGetFileVersionW: %q %q", info.FileVersion, info.Language(), wantVer, wantLang)
			}
		})
	}
}

// TestMsiGetFileVersionDisagreeingLanguages records which of two language
// entries with different versions Windows reports; Parse refuses the file
// (FILE-15).
func TestMsiGetFileVersionDisagreeingLanguages(t *testing.T) {
	t.Parallel()

	tests := map[string][]resEntry{
		"versions differ": {{1, 1031, "1.2.3.4", []int{1031}, nil}, {1, 1033, "9.8.7.6", []int{1033}, nil}},
		"other language unreadable": {
			{1, 1031, "", nil, binaryRoot(t, blobOf(t, testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1031}}))},
			{1, 1033, "1.2.3.4", []int{1033}, nil},
		},
	}

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data := versionResources(t, entries)

			path := filepath.Join(t.TempDir(), "disagree.exe")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			ver, lang, ok := msiFileVersion(t, path)
			t.Logf("Windows: ok=%v version %q language %q", ok, ver, lang)

			if info, err := Parse(bytes.NewReader(data)); err == nil {
				t.Errorf("Parse accepted language entries a German system may read differently: %+v", info)
			}
		})
	}
}
