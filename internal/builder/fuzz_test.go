package builder_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/cab"
	"github.com/jkroepke/msi-builder/internal/condition"
	"github.com/jkroepke/msi-builder/internal/msidb"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// fuzzTemplate takes every fuzzed value through the template's quote, so
// that the values need no YAML quoting and quote itself is exercised.
const fuzzTemplate = `schema: msi-builder/v1
product:
  name: {{ .NAME | quote }}
  manufacturer: {{ .MANUFACTURER | quote }}
  version: {{ .VERSION | quote }}
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
  description: {{ .TEXT | quote }}
package:
  platform: x64
  codepage: {{ .CODEPAGE }}
  summary:
    title: {{ .TEXT | quote }}
upgrade: {}
directories:
  - id: ProgramFiles64Folder
    children:
      - id: INSTALLDIR
        name: {{ .DIRNAME | quote }}
features:
  - id: Main
    title: {{ .TEXT | quote }}
    components: [Main, Reg]
components:
  - id: Main
    directory: INSTALLDIR
    files:
      - source: app.exe
      - source: data.bin
        id: Payload
        name: {{ .FILENAME | quote }}
  - id: Reg
    directory: INSTALLDIR
    registry:
      - root: HKLM
        key: {{ printf "Software\\%s" .REGKEY | quote }}
        name: {{ .PROPNAME | quote }}
        value: {{ .TEXT | quote }}
properties:
  FUZZPROP: {{ .TEXT | quote }}
launchConditions:
  - condition: {{ .CONDITION | quote }}
    message: {{ .TEXT | quote }}
`

type fuzzInput struct {
	vars map[string]string
	data []byte
	// exe is app.exe; nil is a valid PE file with version 1.2.3.4.
	exe []byte
}

// defaultVars are template values that build.
func defaultVars() map[string]string {
	return map[string]string{
		"NAME": "App", "MANUFACTURER": "Example", "VERSION": "1.0.0", "DIRNAME": "App",
		"FILENAME": "data.bin", "PROPNAME": "P", "REGKEY": "K", "TEXT": "x",
		"CONDITION": "1", "CODEPAGE": "1252",
	}
}

func buildFuzzInput(t *testing.T, input fuzzInput) (*builder.Result, error) {
	t.Helper()

	dir := t.TempDir()

	exe := input.exe
	if exe == nil {
		exe = testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}})
	}

	files := map[string][]byte{
		"msi.yaml": []byte(fuzzTemplate),
		"app.exe":  exe,
		"data.bin": input.data,
	}

	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{Vars: input.vars})
	if err != nil {
		return nil, err
	}

	return builder.Build(cfg, builder.Options{BuildTime: buildTime})
}

// FuzzBuild builds packages from fuzzed names, versions, text, conditions
// and code pages. A config the builder accepts must give a package that
// reads back cleanly and keeps every value it was given.
func FuzzBuild(f *testing.F) {
	f.Add("Hello", "Example", "1.0.0", "Hello", "data.bin", "MYPROP", "Software", "text", "NOT Installed", "0", []byte("payload"))
	f.Add("Grüße", "Exämple", "255.255.65535", "Prog Files", "long file name.txt", "Secure_Prop", "Vendor\\App", "ö ü ß", "VersionNT64 >= 603", "1252", make([]byte, 0))
	f.Add("Привет", "Пример", "1.2", "Папка", "файл.txt", "P", "K", "Ж", `PROP = "x"`, "1251", []byte("x"))
	f.Add("日本", "例", "0.0.1", "フォルダ", "ファイル", "P", "K", "日本語", "1", "932", []byte("x"))
	f.Add("Hello", "Example", "1.0.0", "Hello", "a.txt", "P", "K", "[ProductName] [%PATH] [#app.exe]", "P", "65001", []byte("x"))
	f.Add("Привет", "Пример", "1.2", "Папка", "файл.txt", "P", "K", "Ж", `PROP = "x"`, "65001", []byte("x"))

	f.Fuzz(func(t *testing.T, name, manufacturer, version, dirName, fileName, propName, regKey, text, cond, codepage string, data []byte) {
		input := fuzzInput{
			vars: map[string]string{
				"NAME": name, "MANUFACTURER": manufacturer, "VERSION": version, "DIRNAME": dirName,
				"FILENAME": fileName, "PROPNAME": propName, "REGKEY": regKey, "TEXT": text,
				"CONDITION": cond, "CODEPAGE": codepage,
			},
			data: data,
		}

		if strings.Trim(codepage, "0123456789") != "" || codepage == "" {
			// The code page is an unquoted number in the template.
			t.Skip()
		}

		res, err := buildFuzzInput(t, input)
		if err != nil {
			// The config and builder diagnostics must explain every
			// rejection; a late error means the validator let it through.
			if res != nil && res.Diagnostics.Err() == nil {
				t.Fatalf("no diagnostic, but the build failed: %v", err)
			}

			return
		}

		checkBuilt(t, res, input)
	})
}

// FuzzBuildPE builds a package whose executable is fuzzed: truncated or
// corrupted headers, version resources and checksums. The parser must not
// panic, and the File row must carry a version and language that Windows
// Installer can read.
func FuzzBuildPE(f *testing.F) {
	f.Add(testpe.Build(testpe.Options{FileVersion: "1.2.3.4", Languages: []int{1033}}))
	f.Add(testpe.Build(testpe.Options{FileVersion: "65535.65535.65535.65535", Languages: []int{0, 1031, 65535}, Checksum: 1}))
	f.Add(testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Machine: 0x014C}))
	f.Add(testpe.Build(testpe.Options{FileVersion: "2.0.0.0", ProductVersion: "9.9.9.9", Machine: 0xAA64, Padding: 4096}))

	f.Fuzz(func(t *testing.T, exe []byte) {
		input := fuzzInput{vars: defaultVars(), data: []byte("payload"), exe: exe}

		res, err := buildFuzzInput(t, input)
		if err != nil {
			if res != nil && res.Diagnostics.Err() == nil {
				t.Fatalf("no diagnostic, but the build failed: %v", err)
			}

			return
		}

		checkBuilt(t, res, input)
	})
}

var msiVersion = regexp.MustCompile(`^\d{1,5}(\.\d{1,5}){0,3}$`)

// checkFileRows checks the File.Version and File.Language columns: a
// version is up to four parts of at most 65535 or the key of another file
// (companion file), a language a comma-separated list of LCIDs.
func checkFileRows(t *testing.T, database *msidb.Database) {
	t.Helper()

	tbl := database.Table("File")
	if tbl == nil {
		return
	}

	keyCol, versionCol, langCol := tbl.Column("File"), tbl.Column("Version"), tbl.Column("Language")
	keys := map[any]bool{}

	for _, row := range tbl.Rows {
		keys[row[keyCol]] = true
	}

	for _, row := range tbl.Rows {
		if version, ok := row[versionCol].(string); ok && !keys[version] {
			if !msiVersion.MatchString(version) {
				t.Fatalf("file %v: version %q is not a version", row[keyCol], version)
			}

			for part := range strings.SplitSeq(version, ".") {
				if n, _ := strconv.Atoi(part); n > 65535 {
					t.Fatalf("file %v: version %q has a part above 65535", row[keyCol], version)
				}
			}
		}

		if lang, ok := row[langCol].(string); ok {
			for part := range strings.SplitSeq(lang, ",") {
				if n, err := strconv.Atoi(part); err != nil || n < 0 || n > 65535 {
					t.Fatalf("file %v: language %q is not a list of LCIDs", row[keyCol], lang)
				}
			}
		}
	}
}

// checkBuilt reads a package back without the builder's own state and
// checks it against the input.
func checkBuilt(t *testing.T, res *builder.Result, input fuzzInput) {
	t.Helper()

	database, err := msidb.Unmarshal(res.Package)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if len(database.Problems) > 0 {
		t.Fatalf("read-back problems: %v", database.Problems)
	}

	property := func(name string) any {
		tbl := database.Table("Property")
		for _, row := range tbl.Rows {
			if row[0] == name {
				return row[1]
			}
		}

		return nil
	}

	for prop, want := range map[string]string{"ProductName": input.vars["NAME"], "Manufacturer": input.vars["MANUFACTURER"]} {
		if got := property(prop); got != want {
			t.Fatalf("Property %s = %q, config has %q", prop, got, want)
		}
	}

	if want := input.vars["TEXT"]; want != "" {
		if got := property("FUZZPROP"); got != want {
			t.Fatalf("Property FUZZPROP = %q, config has %q", got, want)
		}
	}

	checkFileRows(t, database)

	for _, tbl := range database.Tables {
		keys := tbl.Keys()
		seen := map[string]bool{}

		for _, row := range tbl.Rows {
			k := fmtKey(row[:keys])
			if seen[k] {
				t.Fatalf("table %s: duplicate primary key %s", tbl.Name, k)
			}

			seen[k] = true

			for i, col := range tbl.Columns {
				if col.Name == "Condition" && row[i] != nil && tbl.Name != "Condition" {
					if s, ok := row[i].(string); ok {
						if err := condition.Check(s); err != nil {
							t.Fatalf("table %s: condition %q does not parse: %v", tbl.Name, s, err)
						}
					}
				}
			}
		}
	}

	raw, ok := database.Stream("cab1.cab")
	if !ok {
		for _, s := range database.Streams {
			raw, ok = s.Data, true
		}
	}

	if !ok {
		t.Fatal("no cabinet stream")
	}

	entries, err := cab.Read(raw)
	if err != nil {
		t.Fatalf("cabinet: %v", err)
	}

	found := false

	for _, e := range entries {
		if bytes.Equal(e.Data, input.data) {
			found = true
		}
	}

	if !found {
		t.Fatal("the cabinet does not hold the payload")
	}
}

func fmtKey(cells []any) string {
	var buf bytes.Buffer

	for _, c := range cells {
		buf.WriteString(fmtCell(c))
		buf.WriteByte(0)
	}

	return buf.String()
}

func fmtCell(c any) string {
	switch value := c.(type) {
	case nil:
		return "\x01NULL"
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}
