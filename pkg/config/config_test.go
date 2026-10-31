package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
	"go.yaml.in/yaml/v3"
)

func load(t *testing.T, path string, vars map[string]string) *config.Config {
	t.Helper()

	cfg, err := config.Load(path, config.Options{Vars: vars})
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func TestExamplesValidate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path string
		vars map[string]string
	}{
		{"../../examples/minimal/msi.yaml", nil},
		{"../../examples/windows_exporter/msi.yaml", map[string]string{"VERSION": "0.32.0"}},
		{"../../examples/windows_exporter/msi.yaml", map[string]string{"VERSION": "0.32.0", "ARCH": "arm64"}},
		{"../../examples/branded/msi.yaml", map[string]string{"VERSION": "1.5.0"}},
		{"../../examples/desktop/msi.yaml", map[string]string{"VERSION": "2.0.0"}},
	} {
		cfg := load(t, tc.path, tc.vars)

		diags := config.Validate(cfg)
		if err := diags.Err(); err != nil {
			t.Errorf("%s: %v", tc.path, err)
		}

		for _, w := range diags.Warnings() {
			t.Errorf("%s: unexpected %s", tc.path, w)
		}
	}
}

func TestWindowsExporterExample(t *testing.T) {
	t.Parallel()

	cfg := load(t, "../../examples/windows_exporter/msi.yaml", map[string]string{"VERSION": "0.32.0", "ARCH": "AMD64"})

	if cfg.Package.Platform != config.PlatformX64 {
		t.Errorf("platform = %q, want the exact token x64", cfg.Package.Platform)
	}

	if got := cfg.Features[0].Title; got != "windows_exporter 0.32.0" {
		t.Errorf("feature title = %q", got)
	}

	if got := cfg.Properties["LISTEN_PORT"].Value; got != "9182" {
		t.Errorf("LISTEN_PORT = %q", got)
	}

	if !cfg.Properties["SERVICE_PASSWORD"].Hidden {
		t.Error("SERVICE_PASSWORD is not hidden")
	}

	svc := cfg.Components[0].Services[0]
	if !*svc.Control.DeleteOnUninstall || *svc.Recovery.RestartDelay != config.Duration(config.DefaultRestartDelay) {
		t.Errorf("service defaults: %+v", svc)
	}

	// waitForStart is set; wait keeps its default.
	if !*svc.Control.Wait || svc.Control.WaitForStart == nil || *svc.Control.WaitForStart {
		t.Errorf("service control: wait %v, waitForStart %v", *svc.Control.Wait, svc.Control.WaitForStart)
	}

	if cfg.Product.ProductCode != config.Auto || cfg.Product.Description != "windows_exporter 0.32.0 installer" {
		t.Errorf("product defaults: %+v", cfg.Product)
	}

	if u := cfg.Upgrade; u.Property != config.DefaultUpgradeProperty || !*u.AllowSameVersion {
		t.Errorf("upgrade defaults: %+v", u)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		yaml string
		want string
	}{
		"unknown key":       {"schema: msi-builder/v1\nproduct: {nickname: x}\n", "nickname"},
		"unknown nested":    {"schema: msi-builder/v1\nproperties: {A: {valeu: x}}\n", "valeu"},
		"schema":            {"schema: msi-builder/v2\n", "msi-builder/v1"},
		"former schema":     {"schema: go-msi/v1\n", "before the rename to msi-builder; write \"msi-builder/v1\""},
		"undefined var":     {"schema: msi-builder/v1\nproduct: {version: \"{{ .NOPE }}\"}\n", "variable NOPE is not set"},
		"platform":          {"schema: msi-builder/v1\npackage: {platform: ia64}\n", "unknown platform"},
		"scope":             {"schema: msi-builder/v1\npackage: {scope: global}\n", "unknown scope"},
		"property list":     {"schema: msi-builder/v1\nproperties: {A: [1]}\n", "value or a mapping"},
		"not a mapping":     {"- a\n", "mapping"},
		"bad restartDelay":  {"schema: msi-builder/v1\ncomponents: [{services: [{recovery: {restartDelay: soon}}]}]\n", "duration"},
		"bad step delay":    {"schema: msi-builder/v1\ncomponents: [{services: [{recovery: {actions: [{restart: soon}]}}]}]\n", "duration"},
		"bad step key":      {"schema: msi-builder/v1\ncomponents: [{services: [{recovery: {actions: [{reboot: 1m}]}}]}]\n", "{restart: <delay>}"},
		"null step delay":   {"schema: msi-builder/v1\ncomponents: [{services: [{recovery: {actions: [{restart: ~}]}}]}]\n", "needs a delay"},
		"former wizard key": {"schema: msi-builder/v1\nui: {wizard: true}\n", "wizard"},
		"enabled value":     {"schema: msi-builder/v1\nui: {enabled: featureTree}\n", "into bool"},
		"close value":       {"schema: msi-builder/v1\ncomponents: [{files: [{source: a.exe, close: soon}]}]\n", "`soon` into bool"},
		"close key":         {"schema: msi-builder/v1\ncomponents: [{files: [{source: a.exe, close: {wait: 1s}}]}]\n", "wait"},
		"null option": {
			"schema: msi-builder/v1\nui: {dialogs: [{id: D, title: T, after: X, fields: [{property: P, type: choice, options: [a, b, ~]}]}]}\n",
			"an option cannot be empty",
		},
		"option key": {
			"schema: msi-builder/v1\nui: {dialogs: [{id: D, title: T, after: X, fields: [{property: P, type: choice, options: [{valeu: a}]}]}]}\n",
			"valeu",
		},
	} {
		_, err := config.Parse([]byte(tc.yaml), config.Options{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestChoiceOptionForms(t *testing.T) {
	t.Parallel()

	const doc = "schema: msi-builder/v1\nui: {dialogs: [{id: D, title: T, after: X, fields: [{property: P, type: choice, " +
		"options: [warn, 1, {value: debug, label: '&Debug'}]}]}]}\n"

	cfg, err := config.Parse([]byte(doc), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := fmt.Sprint(cfg.UI.Dialogs[0].Fields[0].Options)
	if want := "[{warn } {1 } {debug &Debug}]"; got != want {
		t.Errorf("options = %s, want %s", got, want)
	}
}

func TestVariables(t *testing.T) {
	t.Parallel()

	doc := `vars: {NAME: fromFile, PORT: "1"}
---
schema: msi-builder/v1
product: {name: "{{ .NAME }}", version: '{{ "{{" }} LITERAL }}'}
package:
  platform: x64
  codepage: {{ .CP }}
properties: {P: "{{ .PORT }}"}
`
	env := func(k string) (string, bool) {
		if k == "CP" {
			return "65001", true
		}

		if k == "NAME" {
			return "fromEnv", true
		}

		return "", false
	}

	cfg, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"PORT": "9"}, LookupEnv: env})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Product.Name != "fromEnv" {
		t.Errorf("name = %q: the environment overrides the file", cfg.Product.Name)
	}

	if cfg.Product.Version != "{{ LITERAL }}" {
		t.Errorf("version = %q: {{ \"{{\" }} writes a literal", cfg.Product.Version)
	}

	if cfg.Package.Codepage != 65001 {
		t.Errorf("codepage = %d: a variable decodes into an int field", cfg.Package.Codepage)
	}

	if cfg.Properties["P"].Value != "9" {
		t.Errorf("P = %q: --var overrides everything", cfg.Properties["P"].Value)
	}

	if cfg.Vars["PORT"] != "9" || cfg.Vars["NAME"] != "fromEnv" {
		t.Errorf("vars = %v, want the values the template saw", cfg.Vars)
	}
}

// A value in vars may use other variables; one from the command line is
// taken as it is.
func TestNestedVariables(t *testing.T) {
	t.Parallel()

	doc := `vars: {SHORT: Hello, NAME: "{{ .SHORT }} App", FULL: "{{ .NAME }} {{ .VERSION }}"}
---
schema: msi-builder/v1
product: {name: "{{ .FULL }}", version: "{{ .VERSION }}"}
package: {platform: x64}
properties: {P: '{{ .CLI }}'}
`

	cfg, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"VERSION": "1.2.3", "CLI": "{{ .SHORT }}"}})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Product.Name != "Hello App 1.2.3" {
		t.Errorf("name = %q, want the nested vars and the --var expanded", cfg.Product.Name)
	}

	if cfg.Properties["P"].Value != "{{ .SHORT }}" {
		t.Errorf("P = %q: a --var value is taken as it is", cfg.Properties["P"].Value)
	}

	for _, tc := range []struct{ name, vars, want string }{
		{"self", `{A: "x{{ .A }}"}`, "line 1: vars.A: A -> A is a cycle"},
		{"cycle", `{A: "{{ .B }}", B: "{{ .A }}"}`, "line 1: vars.B: A -> B -> A is a cycle"},
		{"three", `{A: "{{ .B }}", B: "{{ .C }}", C: "{{ .A }}"}`, "line 1: vars.C: A -> B -> C -> A is a cycle"},
		{"undefined", `{A: "{{ .NOPE }}"}`, "line 1: vars.A: line 1:3: at <.NOPE>: variable NOPE is not set; pass --var NOPE=VALUE, add it to the vars header, or pass --env to read it from the environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Parse([]byte("vars: "+tc.vars+"\n---\nschema: msi-builder/v1\nproduct: {name: \"{{ .A }}\", manufacturer: \"{{ .A }}\"}\n"), config.Options{})
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want exactly %q", err, tc.want)
			}
		})
	}
}

// A --var or environment value replaces a vars entry everywhere, also where
// another vars entry uses it; an entry nothing uses is not checked.
func TestNestedVariablePrecedence(t *testing.T) {
	t.Parallel()

	doc := `vars: {A: "x{{ .B }}", B: vb, C: "{{ .D }}", SELF: "{{ .SELF }}", BROKEN: "{{ .NOPE }}"}
---
schema: msi-builder/v1
product: {name: "{{ .A }}", manufacturer: "{{ .C }}", version: "{{ .SELF }}"}
`
	env := map[string]string{"D": "from-env"}

	cfg, err := config.Parse([]byte(doc), config.Options{
		Vars: map[string]string{"B": "cli", "SELF": "1.0.0"},
		LookupEnv: func(name string) (string, bool) {
			v, ok := env[name]

			return v, ok
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Product.Name != "xcli" || cfg.Product.Manufacturer != "from-env" || cfg.Product.Version != "1.0.0" {
		t.Errorf("product = %q, %q, %q, want xcli, from-env, 1.0.0", cfg.Product.Name, cfg.Product.Manufacturer, cfg.Product.Version)
	}
}

// Doubling vars cannot grow without bound.
func TestNestedVariableBomb(t *testing.T) {
	t.Parallel()

	var doc strings.Builder

	doc.WriteString("vars:\n")

	for i := range 40 {
		fmt.Fprintf(&doc, "  V%d: \"{{ .V%d }}{{ .V%d }}\"\n", i, i+1, i+1)
	}

	doc.WriteString("  V40: xx\n---\nschema: msi-builder/v1\nproduct: {name: \"{{ .V0 }}\"}\n")

	_, err := config.Parse([]byte(doc.String()), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "writes more than") {
		t.Fatalf("err = %v, want the size limit", err)
	}
}

// A large --var used through vars still fits.
func TestNestedVariableLargeValue(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 9<<20)

	cfg, err := config.Parse([]byte("vars: {X: \"{{ .BIG }}\"}\n---\nschema: msi-builder/v1\nproduct: {name: \"{{ .X }}\"}\n"),
		config.Options{Vars: map[string]string{"BIG": big}})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Product.Name) != len(big) {
		t.Errorf("name has %d bytes, want %d", len(cfg.Product.Name), len(big))
	}
}

// A vars entry from a merge key is reported on the line of vars.
func TestNestedVariableMergeKeyLine(t *testing.T) {
	t.Parallel()

	doc := "vars:\n  <<: {M: \"{{ .NOPE }}\"}\n---\nschema: msi-builder/v1\nproduct: {name: \"{{ .M }}\"}\n"

	_, err := config.Parse([]byte(doc), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "line 2: vars.M: ") {
		t.Errorf("err = %v, want the line of the vars mapping", err)
	}
}

// An invalid --var used through a vars entry is reported on the entry.
func TestNestedVariableInvalidUTF8(t *testing.T) {
	t.Parallel()

	_, err := config.Parse([]byte("vars: {A: \"x{{ .C }}\"}\n---\nschema: msi-builder/v1\nproduct: {name: \"{{ .A }}\"}\n"),
		config.Options{Vars: map[string]string{"C": "\xff"}})
	if err == nil || err.Error() != "line 1: vars.A: the value is not valid UTF-8" {
		t.Errorf("err = %v, want one error on the vars entry", err)
	}
}

func TestPlatformTokens(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]config.Platform{
		"x86": config.PlatformX86, "Intel": config.PlatformX86, "X64": config.PlatformX64,
		"amd64": config.PlatformX64, "ARM64": config.PlatformArm64, "aarch64": config.PlatformArm64,
	} {
		cfg, err := config.Parse([]byte("schema: msi-builder/v1\npackage: {platform: "+input+"}\n"), config.Options{})
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}

		if cfg.Package.Platform != want {
			t.Errorf("%s: got %q, want %q", input, cfg.Package.Platform, want)
		}
	}
}

func TestValidateGoAPI(t *testing.T) {
	t.Parallel()

	// A Config built in Go, without Load, must not panic on unset pointers.
	cfg := &config.Config{Schema: config.SchemaV1, Features: []*config.Feature{{ID: "F"}},
		Components: []*config.Component{{ID: "C", Directory: "D", Services: []*config.Service{{Name: "s"}}}}}

	if err := config.Validate(cfg).Err(); err == nil {
		t.Error("an incomplete config validated")
	}
}

// TestRecoveryActionsMarshal keeps every recovery step in the marshalled
// config, from which the builder derives the product code.
func TestRecoveryActionsMarshal(t *testing.T) {
	t.Parallel()

	doc := "schema: msi-builder/v1\ncomponents: [{services: [{recovery: {actions: [restart, none, {restart: 5m}]}}]}]\n"

	cfg, err := config.Parse([]byte(doc), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out, err := yaml.Marshal(cfg.Components[0].Services[0].Recovery.Actions)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := string(out), "- restart\n- none\n- restart: 5m0s\n"; got != want {
		t.Errorf("marshalled actions %q, want %q", got, want)
	}
}

// FuzzVariable findings: a value that spells null emptied a string field,
// and a value that is not valid UTF-8 reached the config.
func TestVariableValues(t *testing.T) {
	t.Parallel()

	doc := "schema: msi-builder/v1\nproduct:\n  name: {{ .V | quote }}\npackage: {platform: x64}\n"

	for _, value := range []string{"null", "Null", "NULL", "~"} {
		cfg, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"V": value}})
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Product.Name != value {
			t.Errorf("{{ .V | quote }} = %q gives name %q", value, cfg.Product.Name)
		}
	}

	if _, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"V": "\xff"}}); err == nil {
		t.Error("a variable that is not valid UTF-8 was accepted")
	}
}

// TestRepoConfigs parses the msi.yaml files of the spec probes and test
// data, which no other Linux test reads.
func TestRepoConfigs(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"../../docs/spec/msitest/inst02/probe.yaml",
		"../../docs/spec/msitest/reg05/probe.yaml",
		"../../docs/spec/msitest/reg05/baseline.yaml",
		"../../internal/cadll/testdata/probe/msi.yaml",
	} {
		if _, err := config.Load(path, config.Options{}); err != nil {
			t.Error(err)
		}
	}
}
