package wixconvert_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestConvertPreprocessorBuilds renders the converted preprocessor sample
// for several builds and checks that each takes the branches WiX would.
func TestConvertPreprocessorBuilds(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "preprocessor.wxs"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := wixconvert.Convert(strings.NewReader(string(data)), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		vars       map[string]string
		components []string
		source     string
		log        string
		registry   int
	}{
		"x64 full": {
			vars:       map[string]string{"Platform": "x64", "Edition": "full"},
			components: []string{"MainExe", "Plugins", "Settings", "Lang_en", "Lang_de", "Lang_fr"},
			source:     "dist/x64/hello.exe",
			log:        "info",
			registry:   1,
		},
		"x64 case-insensitive, lite, debug": {
			vars:       map[string]string{"Platform": "X64", "Edition": "lite", "Debug": ""},
			components: []string{"MainExe", "Settings", "Lang_en", "Lang_de", "Lang_fr"},
			source:     "dist/x64/hello.exe",
			log:        "debug",
			registry:   2,
		},
		"arm64": {
			vars:       map[string]string{"Platform": "arm64", "Edition": "full"},
			components: []string{"MainExe", "Plugins", "Settings", "Lang_en", "Lang_de", "Lang_fr"},
			source:     "dist/arm64/hello.exe",
			log:        "info",
			registry:   1,
		},
		"x86 leaves out the plugins": {
			vars:       map[string]string{"Platform": "x86", "Edition": "full"},
			components: []string{"MainExe", "Settings", "Lang_en", "Lang_de", "Lang_fr"},
			source:     "dist/x86/hello.exe",
			log:        "info",
			registry:   1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Parse(res.YAML, config.Options{Vars: tc.vars})
			if err != nil {
				t.Fatal(err)
			}

			if err := config.Validate(cfg).Err(); err != nil {
				t.Fatalf("the rendered file does not validate: %v", err)
			}

			ids := make([]string, 0, len(cfg.Components))
			members := make([]string, 0, len(cfg.Features[0].Components))

			for _, comp := range cfg.Components {
				ids = append(ids, comp.ID)
			}

			for _, comp := range cfg.Features[0].Components {
				members = append(members, comp.ID)
			}

			if !slices.Equal(ids, tc.components) || !slices.Equal(members, tc.components) {
				t.Errorf("components %q, feature %q, want %q", ids, members, tc.components)
			}

			if got := cfg.Components[0].Files[0].Source; got != tc.source {
				t.Errorf("source %q, want %q", got, tc.source)
			}

			if got := cfg.Properties["HELLO_LOG"].Value; got != tc.log {
				t.Errorf("HELLO_LOG %q, want %q", got, tc.log)
			}

			settings := cfg.Components[slices.Index(ids, "Settings")]
			if len(settings.Registry) != tc.registry {
				t.Errorf("%d registry values, want %d", len(settings.Registry), tc.registry)
			}

			if cfg.Product.Version != "1.0.0" {
				t.Errorf("version %q, want the <?ifndef?> default 1.0.0", cfg.Product.Version)
			}
		})
	}

	t.Run("error", func(t *testing.T) {
		t.Parallel()

		_, err := config.Parse(res.YAML, config.Options{Vars: map[string]string{"Platform": "ia64", "Edition": "full"}})
		if err == nil || !strings.Contains(err.Error(), "Platform ia64 is not supported") {
			t.Errorf("err %v, want the <?error?> message", err)
		}
	})
}

// TestConvertPreprocessorFallback checks the blocks that convert cannot
// write as templates: only their first branch is converted, with a note.
func TestConvertPreprocessorFallback(t *testing.T) {
	t.Parallel()

	const head = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
`

	tests := map[string]struct {
		body string
		note string
	}{
		"directory": {
			body: `<?if $(var.A) = 1 ?><StandardDirectory Id="DesktopFolder" /><?endif ?>`,
			note: `<?if $(var.A) = 1?> is not evaluated: only its first branch was converted`,
		},
		"component Id twice": {
			body: `<?if $(var.A) = 1 ?>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <?else ?>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="b.exe" /></Component>
    <?endif ?>`,
			note: "the <?else?> branch was left out; convert it by hand if your build takes it",
		},
		"system variable": {
			body: `<?if $(sys.BUILDARCH) = x64 ?><Property Id="P" Value="1" /><?endif ?>`,
			note: "<?if $(sys.BUILDARCH) = x64?> is not evaluated: only its first branch was converted",
		},
		"element across branches": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><?if $(var.A) = 1 ?><File Source="a.exe"><?else ?><File Source="b.exe"></File><?endif ?></File></Component>`,
			note: "the <?else?> branch was left out; convert it by hand if your build takes it",
		},
		"define in a loop": {
			body: `<?foreach L in a;b ?><?define X = $(var.L) ?><?endforeach ?>`,
			note: "<?foreach L in a;b?> is not expanded: its body was converted once",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, err := wixconvert.Convert(strings.NewReader(head+tc.body+"\n  </Package>\n</Wix>"), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if !slices.ContainsFunc(res.Notes, func(n wixconvert.Note) bool { return n.Message == tc.note }) {
				t.Errorf("notes %q, want %q", res.Notes, tc.note)
			}

			if strings.Contains(string(res.YAML), "{{ if") || strings.Contains(string(res.YAML), "{{ range") {
				t.Errorf("the block became a template:\n%s", res.YAML)
			}
		})
	}
}
