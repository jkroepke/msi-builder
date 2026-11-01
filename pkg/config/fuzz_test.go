package config_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// FuzzParse feeds arbitrary documents and variable values to Parse and
// Validate, neither of which may panic. FuzzVariable checks that a value
// arrives as given.
func FuzzParse(f *testing.F) {
	f.Add([]byte(base+appComponent), "1.0.0")
	f.Add([]byte(strings.Replace(base, "version: 1.0.0", "version: {{ .V }}", 1)+appComponent), "1.2.3")
	f.Add([]byte(strings.Replace(base, "version: 1.0.0", "version: {{ .V }}", 1)+appComponent), "[a, b]")
	f.Add([]byte(strings.Replace(base, "name: App\n  manufacturer", "name: {{ .V | quote }}\n  manufacturer", 1)+appComponent), "null")
	f.Add([]byte(strings.Replace(base, "name: App\n  manufacturer", "name: {{ .V }}\n  manufacturer", 1)+appComponent), "a: b\nc: d")
	f.Add([]byte("vars:\n  V: x\n---\n"+base+appComponent), "{{")
	f.Add([]byte("vars:\n  A: \"{{ .B }}{{ .B }}\"\n  B: \"{{ .V }}{{ .A }}\"\n---\n"+
		strings.Replace(base, "name: App\n  manufacturer", "name: {{ .A }}\n  manufacturer", 1)+appComponent), "x")

	f.Fuzz(func(_ *testing.T, doc []byte, value string) {
		// A template is code: a range that writes nothing is not bounded.
		if strings.Contains(string(doc), "range") {
			return
		}

		cfg, err := config.Parse(doc, config.Options{Vars: map[string]string{"V": value}})
		if err != nil {
			return
		}

		_ = config.Validate(cfg)
	})
}

// FuzzVariable writes fuzzed values into a string field with quote. Any value,
// including YAML syntax, must arrive unchanged.
func FuzzVariable(f *testing.F) {
	for _, v := range []string{"App", "", "null", "~", "[a, b]", "{a: b}", "a: b", "- x", "# c", "'q'", `"q"`, "1e3", "true", "\x00", "\xff", "&a", "*a", "!!int 1", "a\nb", " lead"} {
		f.Add(v)
	}

	doc := strings.Replace(base, "manufacturer: Example", "manufacturer: {{ .V | quote }}", 1) + appComponent

	f.Fuzz(func(t *testing.T, value string) {
		cfg, err := config.Parse([]byte(doc), config.Options{Vars: map[string]string{"V": value}})
		if err != nil {
			return
		}

		if cfg.Product.Manufacturer != value {
			t.Fatalf("{{ .V | quote }} = %q arrives as %q", value, cfg.Product.Manufacturer)
		}
	})
}

// FuzzCheckSDDL feeds arbitrary strings to the SDDL check.
func FuzzCheckSDDL(f *testing.F) {
	for _, s := range []string{
		"D:PAI(A;;FA;;;BA)(A;;FRFX;;;BU)", "O:BAG:BAD:(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)",
		"D:(A;;FA;;;S-1-5-21-1-2-3-500)", "D:(A;;FA;;;<BUILTIN\\Users>)", "D:(A;;FA;;;<[%USERDOMAIN]\\[%USERNAME]>)",
		"D:(A;;KA;;;BA)S:(AU;SAFA;FA;;;WD)", "D:(A;;FA;;;BA)(D;;FW;;;BU)",
	} {
		f.Add(s)
	}

	f.Fuzz(func(_ *testing.T, sddl string) {
		_, _ = config.CheckSDDL(sddl)
	})
}
