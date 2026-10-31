package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// templated is base with the platform and an arm64-only component chosen
// by a template, and the name from the vars header.
const templated = `vars:
  NAME: App
---
schema: msi-builder/v1
product:
  name: {{ .NAME | quote }}
  manufacturer: Example
  version: {{ .VERSION }}
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: {{ .ARCH }}
upgrade: {}
directories:
  - id: ProgramFiles64Folder
    children:
      - id: INSTALLDIR
        name: App
        configurable: true
features:
  - id: Main
    components:
      - App
{{- if eq .ARCH "arm64" }}
      - Arm
{{- end }}
components:
  - id: App
    directory: INSTALLDIR
    files:
      - source: app.exe
{{- if eq .ARCH "arm64" }}
  - id: Arm
    directory: INSTALLDIR
    files:
      - source: arm.dll
{{- end }}
`

func TestTemplate(t *testing.T) {
	t.Parallel()

	for arch, want := range map[string]int{"x64": 1, "arm64": 2} {
		cfg, err := config.Parse([]byte(templated), config.Options{Vars: map[string]string{"ARCH": arch, "VERSION": "1.2.3"}})
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}

		if got := len(cfg.Components); got != want {
			t.Errorf("%s: %d components, want %d", arch, got, want)
		}

		if !strings.EqualFold(string(cfg.Package.Platform), arch) || cfg.Product.Name != "App" || cfg.Product.Version != "1.2.3" {
			t.Errorf("%s: got platform %q, name %q, version %q", arch, cfg.Package.Platform, cfg.Product.Name, cfg.Product.Version)
		}

		if cfg.Vars["NAME"] != "App" {
			t.Errorf("%s: vars %v", arch, cfg.Vars)
		}

		if ds := config.Validate(cfg); ds.Err() != nil {
			t.Errorf("%s: %v", arch, ds)
		}
	}
}

func TestTemplateEnv(t *testing.T) {
	t.Parallel()

	env := func(name string) (string, bool) {
		if name == "NAME" {
			return `a "quoted": name`, true
		}

		return "", false
	}

	cfg, err := config.Parse([]byte(templated), config.Options{
		Vars:      map[string]string{"ARCH": "x64", "VERSION": "1.0.0"},
		LookupEnv: env,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Product.Name != `a "quoted": name` {
		t.Errorf("name = %q", cfg.Product.Name)
	}
}

func TestTemplateErrors(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		doc  string
		opts config.Options
		want string
	}{
		"not set": {
			doc:  templated,
			opts: config.Options{Vars: map[string]string{"VERSION": "1"}},
			want: `template: line 11:15: at <.ARCH>: variable ARCH is not set; pass --var ARCH=VALUE, add it to the vars header, or pass --env to read it from the environment`,
		},
		"not set with env": {
			doc:  "x: {{ .X }}\n",
			opts: config.Options{LookupEnv: func(string) (string, bool) { return "", false }},
			want: "set it in the environment or add it to the vars header",
		},
		"syntax": {
			doc:  "schema: msi-builder/v1\n{{ if }}\n",
			want: "template: line 2:",
		},
		"syntax after header": {
			doc:  "vars:\n  A: a\n---\nx: {{ .B }}\n",
			want: "template: line 4:6: at <.B>: variable B is not set",
		},
		"too large": {
			doc:  "schema: msi-builder/v1\n{{ range 20000000 }}x{{ end }}\n",
			want: "the template writes more than",
		},
		"indent": {
			doc:  "x: {{ indent 2000000000 \"a\\nb\" }}\n",
			want: "is not between 0 and 1024",
		},
		"quote": {
			doc:  "x: {{ .V | quote }}\n",
			opts: config.Options{Vars: map[string]string{"V": "\xff"}},
			want: "not valid UTF-8",
		},
		"empty separator": {
			doc:  "x: {{ split \"\" \"abc\" }}\n",
			want: "the separator is empty",
		},
		"rendered yaml": {
			doc:  "schema: msi-builder/v1\n{{ \"a: [\" }}\n",
			want: "lines of the rendered file",
		},
		"vars in the body": {
			doc:  "schema: msi-builder/v1\nvars:\n  A: one\n",
			want: "line 2: vars: move them into a vars header",
		},
		"vars in header and body": {
			doc:  "vars:\n  A: one\n---\nschema: msi-builder/v1\nvars:\n  B: two\n",
			want: "line 5: vars: move them into a vars header",
		},
		"vars cycle": {
			doc:  "vars:\n  A: x{{ .B }}\n  B: x{{ .A }}\n---\nx: {{ .A }}\n",
			want: "line 3: vars.B: A -> B -> A is a cycle",
		},
		"vars header yaml": {
			doc:  "vars:\n  A: x\n  B: \"x {{ .A }}\n---\nx: {{ .A }}\n",
			want: "vars header: yaml: line",
		},
		"vars entry error": {
			doc:  "vars:\n  A: x{{ .NOPE }}\n---\nx: {{ .A }}\n",
			want: "line 2: vars.A: line 1:4: at <.NOPE>: variable NOPE is not set",
		},
	} {
		_, err := config.Parse([]byte(test.doc), test.opts)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: got %v, want %q", name, err, test.want)
		}
	}
}

// TestTemplateFuncs checks has, indent and split.
func TestTemplateFuncs(t *testing.T) {
	t.Parallel()

	doc := "a: {{ if has \"X\" }}set{{ else }}unset{{ end }}\nc:{{ nindent 2 \"l1\\nl2\" }}\nd:{{ range split \";\" .L }} {{ . }}{{ end }}\n"

	out, err := config.Render([]byte(doc), config.Options{Vars: map[string]string{"X": "1", "L": "en;de"}})
	if err != nil {
		t.Fatal(err)
	}

	if want := "a: set\nc:\n  l1\n  l2\nd: en de\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}

	out, err = config.Render([]byte(doc), config.Options{Vars: map[string]string{"L": ""}})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(out), "a: unset\n") || !strings.HasSuffix(string(out), "d:\n") {
		t.Errorf("got %q", out)
	}
}

// TestTemplateSprig checks sprig's functions, the caps on the ones that
// allocate by a number, and the ones left out.
func TestTemplateSprig(t *testing.T) {
	t.Parallel()

	out, err := config.Render([]byte(`{{ .V | upper }} {{ .NOPE | default "d" }} {{ semverCompare ">=1.2" .V2 }} {{ seq 3 }} {{ until 2 }} {{ "a" | repeat 3 }} {{ indent 2 "x" }}`),
		config.Options{Vars: map[string]string{"V": "abc", "V2": "1.4.0"}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err == nil {
		t.Fatalf("a missing variable piped to default renders %q", out)
	}

	out, err = config.Render([]byte(`{{ .V | upper }} {{ semverCompare ">=1.2" .V2 }} {{ seq 3 }} {{ until 2 }} {{ "a" | repeat 3 }} {{ indent 2 "x" }} {{ untilStep 0 6 2 }}`),
		config.Options{Vars: map[string]string{"V": "abc", "V2": "1.4.0"}})
	if err != nil {
		t.Fatal(err)
	}

	if want := "ABC true 1 2 3 [0 1] aaa   x [0 2 4]"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}

	for doc, want := range map[string]string{
		`{{ repeat 2000000000 "ab" }}`:                                                 "writes more than",
		`{{ until 2000000000 }}`:                                                       "until: ",
		`{{ untilStep -9000000000000000000 9000000000000000000 1 }}`:                   "untilStep: ",
		`{{ seq 1 2000000000 }}`:                                                       "seq: ",
		`{{ untilStep 9223372036854775806 9223372036854775807 2 }}{{ until 1048577 }}`: "until: 1048577 entries",
		`{{ $s := repeat 16000000 "\n" }}{{ indent 1024 $s }}`:                         "writes more than",
		`{{ osBase "a" }}`:                                                             `function "osBase" not defined`,
		`{{ nindent 5000 "x" }}`:                                                       "nindent: 5000 is not between",
		`{{ env "HOME" }}`:                                                             "env: read environment variables as {{ .NAME }} and pass --env",
		`{{ now }}`:                                                                    `function "now" not defined`,
		`{{ randAlpha 3 }}`:                                                            `function "randAlpha" not defined`,
		`{{ genPrivateKey "rsa" }}`:                                                    `function "genPrivateKey" not defined`,
	} {
		if _, err := config.Render([]byte(doc), config.Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", doc, err, want)
		}
	}
}

// TestTemplateSprigLists checks the list functions at their limits: no
// overflow at the ends of int, exactly maxList entries, steps that point
// away from the end, and keys and values in sorted order.
func TestTemplateSprigLists(t *testing.T) {
	t.Parallel()

	for doc, want := range map[string]string{
		`{{ untilStep 9223372036854775806 9223372036854775807 2 }}`:                  "[9223372036854775806]",
		`{{ untilStep -9223372036854775807 -9223372036854775808 -5 }}`:               "[-9223372036854775807]",
		`{{ seq 0 4611686018427387904 9223372036854775806 }}`:                        "0 4611686018427387904",
		`{{ seq 9223372036854775806 9223372036854775807 }}`:                          "9223372036854775806 9223372036854775807",
		`{{ len (until 1048576) }} {{ len (until -1048576) }}`:                       "1048576 1048576",
		`{{ untilStep 0 2000000 -1 }} {{ seq 1 -1 2000000 }}|{{ seq 2000000 1 0 }}|`: "[] ||",
		`{{ seq 3 }} {{ seq 3 1 }} {{ seq 1 2 6 }} {{ seq 0 }} {{ until -2 }}`:       "1 2 3 3 2 1 1 3 5 1 0 [0 -1]",
		`{{ keys (dict "f" 1 "a" 2 "c" 3) }} {{ values (dict "f" 1 "a" 2 "c" 3) }}`:  "[a c f] [2 3 1]",
	} {
		out, err := config.Render([]byte(doc), config.Options{})
		if err != nil || string(out) != want {
			t.Errorf("%s: got %q, %v, want %q", doc, out, err, want)
		}
	}
}

// TestTemplateFuncNames pins the function names a template can call, so
// that a sprig upgrade that adds one is checked for repeatability first.
func TestTemplateFuncNames(t *testing.T) {
	t.Parallel()

	got := config.TemplateFuncNames()
	if want := strings.Fields(funcNames); !slices.Equal(got, want) {
		t.Errorf("the functions changed; check new ones for repeatability and update funcNames:\n%s", strings.Join(got, " "))
	}
}

// TestTemplateQuote checks that quote delivers values as given.
func TestTemplateQuote(t *testing.T) {
	t.Parallel()

	values := []string{
		"App", "", "null", "~", "[a, b]", "{a: b}", "a: b", "- x", "# c", "'q'", `"q" \ x`, "1e3", "true",
		"line\nbreak", "{{ .X }}", "a<b&c>", "a\x7fb", "a\u0085\u0080b", "a\ufffeb", "\U0001F600",
	}

	for _, value := range values {
		doc := strings.Replace(base, "manufacturer: Example", `manufacturer: {{ .V | quote }}`, 1) + appComponent

		cfg, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"V": value}})
		if err != nil {
			t.Errorf("%q: %v", value, err)

			continue
		}

		if cfg.Product.Manufacturer != value {
			t.Errorf("%q arrives as %q", value, cfg.Product.Manufacturer)
		}
	}

	out, err := config.Render([]byte(`{{ quote "a<b" }}`), config.Options{})
	if err != nil || string(out) != `"a<b"` {
		t.Errorf("got %q, %v", out, err)
	}
}

// TestTemplateLoad checks that Load names the file in a template error.
func TestTemplateLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("x: {{ .X }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path, config.Options{})
	if err == nil || !strings.HasPrefix(err.Error(), path+": template: line 1:6: at <.X>:") {
		t.Errorf("got %v", err)
	}
}

// TestTemplateVars checks that variables come from --var, then the
// environment, then the vars header, whose values may read other
// variables, and that a broken entry the template does not read is no
// error.
func TestTemplateVars(t *testing.T) {
	t.Parallel()

	doc := `# comment
vars:
  ARCH: x64
  NAME: Acme {{ .ARCH }}

  EMPTY: ""
  BROKEN: "{{ .UNSET }}"
---
y: {{ if has "UNSET" }}{{ .BROKEN }}{{ end }}
x: {{ .ARCH }} {{ .NAME }} {{ has "EMPTY" }} {{ has "NOPE" }}{{ range split ";" "a" }} {{ $.ARCH }} {{ index $ "NAME" }}{{ end }}
`
	env := func(name string) (string, bool) {
		if name == "NAME" {
			return "env", true
		}

		return "", false
	}

	for _, test := range []struct {
		opts config.Options
		want string
	}{
		{config.Options{}, "x: x64 Acme x64 true false x64 Acme x64\n"},
		{config.Options{Vars: map[string]string{"ARCH": "arm64"}}, "x: arm64 Acme arm64 true false arm64 Acme arm64\n"},
		{config.Options{LookupEnv: env}, "x: x64 env true false x64 env\n"},
	} {
		out, err := config.Render([]byte(doc), test.opts)
		if err != nil {
			t.Fatal(err)
		}

		if got := string(out[strings.Index(string(out), "x: "):]); got != test.want {
			t.Errorf("got %q, want %q", got, test.want)
		}
	}
}

// TestTemplateHeader checks which first documents are a vars header.
func TestTemplateHeader(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		doc  string
		want string
	}{
		"plain":           {"vars:\n  A: b\n---\nx: {{ .A }}\n", "vars:\n  A: b\n---\nx: b\n"},
		"opening marker":  {"# c\n---\nvars:\n  A: b\n---\nx: {{ .A }}\n", "# c\n---\nvars:\n  A: b\n---\nx: b\n"},
		"bom":             {"\ufeffvars:\n  A: b\n---\nx: {{ .A }}\n", "\ufeffvars:\n  A: b\n---\nx: b\n"},
		"flow":            {"vars: {A: b}\n--- \r\nx: {{ .A }}\n", "vars: {A: b}\n--- \r\nx: b\n"},
		"crlf":            {"vars:\r\n  A: b\r\n---\r\nx: {{ .A }}\r\n", "vars:\r\n  A: b\r\n---\r\nx: b\r\n"},
		"no header":       {"---\nx: {{ \"b\" }}\n", "---\nx: b\n"},
		"other first key": {"y: 1\n---\nx: {{ \"b\" }}\n", "y: 1\n---\nx: b\n"},
		"marker comment":  {"vars:\n  A: b\n--- # end\nx: {{ .A }}\n", "vars:\n  A: b\n--- # end\nx: b\n"},
		"no last newline": {"vars:\n  A: b\n---", "vars:\n  A: b\n---"},
		"directive":       {"%YAML 1.1\n---\nvars:\n  A: b\n---\nx: {{ .A }}\n", "%YAML 1.1\n---\nvars:\n  A: b\n---\nx: b\n"},
	} {
		out, err := config.Render([]byte(test.doc), config.Options{})
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		if string(out) != test.want {
			t.Errorf("%s: got %q, want %q", name, out, test.want)
		}
	}

	// The body keeps the lines of the file.
	_, err := config.Parse([]byte("vars:\n  V: 1\n---\nschema: msi-builder/v1\nproduct: [\n"), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "line 5") {
		t.Errorf("header line: got %v", err)
	}
}

// TestTemplateUnknownKeyLine checks that an unknown key names its line.
func TestTemplateUnknownKeyLine(t *testing.T) {
	t.Parallel()

	doc := base + appComponent + "\n\n# c\nbogus: {{ 1 }}\n"
	line := strings.Count(doc, "\n")

	_, err := config.Parse([]byte(doc), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "line "+strconv.Itoa(line)+": field bogus not found") {
		t.Errorf("got %v", err)
	}
}

// funcNames are the template functions, from TestTemplateFuncNames.
const funcNames = `
abbrev abbrevboth add add1 add1f addf adler32sum all any append
atoi b32dec b32enc b64dec b64enc base biggest buildCustomCert camelcase cat
ceil chunk clean coalesce compact concat contains decryptAES deepCopy
deepEqual default derivePassword dict dig dir div divf duration
durationRound empty env expandenv ext fail first float64 floor fromJson get
has hasKey hasPrefix hasSuffix hello indent initial initials int int64
isAbs join kebabcase keys kindIs kindOf last list lower max maxf merge
mergeOverwrite min minf mod mul mulf mustAppend mustChunk mustCompact
mustDateModify mustDeepCopy mustFirst mustFromJson mustHas mustInitial
mustLast mustMerge mustMergeOverwrite mustPrepend mustPush mustRegexFind
mustRegexFindAll mustRegexMatch mustRegexReplaceAll
mustRegexReplaceAllLiteral mustRegexSplit mustRest mustReverse mustSlice
mustToJson mustToPrettyJson mustToRawJson mustUniq mustWithout
must_date_modify nindent nospace omit pick pluck plural prepend push quote
regexFind regexFindAll regexMatch regexQuoteMeta regexReplaceAll
regexReplaceAllLiteral regexSplit repeat replace rest reverse round semver
semverCompare seq set sha1sum sha256sum sha512sum slice snakecase sortAlpha
split splitList splitn squote sub subf substr swapcase ternary title
toDecimal toJson toPrettyJson toRawJson toString toStrings trim trimAll
trimPrefix trimSuffix trimall trunc tuple typeIs typeIsLike typeOf uniq
unixEpoch unset until untilStep untitle upper urlJoin urlParse values
without wrap wrapWith
`
