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
		"ifdef of a variable the file defines": {
			body: `<?ifdef Debug ?><?define Extra = "1" ?><?endif ?>
    <?ifdef Extra ?><Property Id="P" Value="1" /><?endif ?>`,
			note: "<?ifdef Extra?> is not evaluated: only its first branch was converted",
		},
		"ifndef with more than its default": {
			body: `<?ifndef Version ?><?define Version = "1.0.0" ?><?define Dev = "yes" ?><?endif ?>`,
			note: "<?ifndef Version?> is not evaluated: only its first branch was converted",
		},
		"variable defined again after the block": {
			body: `<?define Arch = "x86" ?><?if $(var.Arch) = x86 ?><?define Folder = "a" ?><?endif ?><?define Arch = "x64" ?>`,
			note: "<?if $(var.Arch) = x86?> is not evaluated: only its first branch was converted",
		},
		"key path in a branch": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" />
    <?ifdef Extra ?><RegistryValue Root="HKLM" Key="Software\Example" Name="x" Value="1" KeyPath="yes" /><?endif ?></Component>`,
			note: "<?ifdef Extra?> is not evaluated: only its first branch was converted",
		},
		"error under a block convert cannot read": {
			body: `<?ifdef Debug ?><?if $(sys.BUILDARCH) = x64 ?><?error no debug on x64 ?><?endif ?><?endif ?>`,
			note: "<?error no debug on x64?> has no counterpart",
		},
		"feature level read from a block": {
			body: `<?if $(var.Edition) = full ?><?define ToolsLevel = "1" ?><?else ?><?define ToolsLevel = "1000" ?><?endif ?>
    <Feature Id="Main" Level="$(var.ToolsLevel)"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>`,
			note: "<?if $(var.Edition) = full?> is not evaluated: only its first branch was converted",
		},
		"shortcut to a file in a branch": {
			body: `<?ifdef Tools ?><Component Id="Tool" Directory="INSTALLFOLDER"><File Id="tool.exe" Source="tool.exe" /></Component><?endif ?>
    <Component Id="Links" Directory="INSTALLFOLDER"><File Source="a.exe" /><Shortcut Id="L" Name="Tool" Target="[#tool.exe]" /></Component>`,
			note: "<?ifdef Tools?> is not evaluated: only its first branch was converted",
		},
		"service control in a branch": {
			body: `<Component Id="Svc" Directory="INSTALLFOLDER"><File Source="svc.exe" /><ServiceInstall Name="HelloSvc" Type="ownProcess" Start="auto" ErrorControl="normal" /></Component>
    <?ifdef AutoStart ?><Component Id="Ctl" Directory="INSTALLFOLDER"><ServiceControl Id="SC" Name="HelloSvc" Start="install" /><CreateFolder /></Component><?endif ?>`,
			note: "<?ifdef AutoStart?> is not evaluated: only its first branch was converted",
		},
		"component without an Id in a loop": {
			body: `<?foreach L in en;de ?><Component Directory="INSTALLFOLDER"><File Source="$(var.L).txt" /></Component><?endforeach ?>`,
			note: "<?foreach L in en;de?> is not expanded: its body was converted once",
		},
		"define that extends a passed variable": {
			body: `<?ifdef Extra ?><?define Flags = "$(var.Flags) extra" ?><?endif ?><Property Id="FLAGS" Value="$(var.Flags)" />`,
			note: "<?ifdef Extra?> is not evaluated: only its first branch was converted",
		},
		"implied key file in a branch": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><?ifdef Pro ?><File Id="pro" Source="pro.exe" /><?endif ?><File Id="base" Source="base.exe" /></Component>`,
			note: "<?ifdef Pro?> is not evaluated: only its first branch was converted",
		},
		"shortcut to a file in a branch by its folder": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><File Id="base" Source="base.exe" /><?ifdef Pro ?><File Id="pro" Source="pro.exe" /><?endif ?>
    <Shortcut Id="L" Name="Pro" Directory="INSTALLFOLDER" Target="[INSTALLFOLDER]pro.exe" /></Component>`,
			note: "<?ifdef Pro?> is not evaluated: only its first branch was converted",
		},
		"shortcut to a file without an Id in a branch": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><File Id="base" Source="base.exe" /><?ifdef Pro ?><File Source="dist\pro.exe" Name="app.exe" /><?endif ?></Component>
    <Component Id="Links" Directory="INSTALLFOLDER"><File Id="b" Source="b.exe" /><Shortcut Id="L" Name="App" Target="[#app.exe]" /></Component>`,
			note: "<?ifdef Pro?> is not evaluated: only its first branch was converted",
		},
		"service in a branch controlled from outside": {
			body: `<?ifdef Svc ?><Component Id="Svc" Directory="INSTALLFOLDER"><File Source="svc.exe" /><ServiceInstall Name="HelloSvc" Type="ownProcess" Start="auto" ErrorControl="normal" /></Component><?endif ?>
    <Component Id="Ctl" Directory="INSTALLFOLDER"><File Source="ctl.exe" /><ServiceControl Id="SC" Name="HelloSvc" Stop="both" Remove="uninstall" /></Component>`,
			note: "<?ifdef Svc?> is not evaluated: only its first branch was converted",
		},
		"variable read before a block defines it": {
			body: `<Property Id="P" Value="$(var.Mode)" /><?ifdef Debug ?><?define Mode = "debug" ?><?endif ?>`,
			note: "<?ifdef Debug?> is not evaluated: only its first branch was converted",
		},
		"implied key file after a file in a branch": {
			body: `<Component Id="C" Directory="INSTALLFOLDER"><?ifdef X ?><RemoveFolder Id="rf" On="uninstall" /><?else ?><File Id="x" Source="x.exe" /><?endif ?>
    <?ifdef Pro ?><File Id="pro" Source="pro.exe" /><?endif ?><File Id="base" Source="base.exe" /></Component>`,
			note: "<?ifdef Pro?> is not evaluated: only its first branch was converted",
		},
		"service controlled by a name in another case": {
			body: `<?ifdef Svc ?><Component Id="Svc" Directory="INSTALLFOLDER"><File Source="svc.exe" /><ServiceInstall Name="HelloSvc" Type="ownProcess" Start="auto" ErrorControl="normal" /></Component><?endif ?>
    <Component Id="Ctl" Directory="INSTALLFOLDER"><File Source="ctl.exe" /><ServiceControl Id="SC" Name="hellosvc" Stop="both" /></Component>`,
			note: "<?ifdef Svc?> is not evaluated: only its first branch was converted",
		},
		"variable read before a later define": {
			body: `<?ifdef D ?><?define Mode = "d" ?><?else ?><?define Mode = "r" ?><?endif ?>
    <Property Id="P" Value="$(var.Mode)" />
    <?define Mode = "x" ?>`,
			note: "<?ifdef D?> is not evaluated: only its first branch was converted",
		},
		"define that extends a default": {
			body: `<?ifndef V ?><?define V = "1" ?><?endif ?><?define V = "$(var.V)-x" ?><Property Id="P" Value="$(var.V)" />`,
			note: "<?ifndef V?> is not evaluated: only its first branch was converted",
		},
		"default given twice": {
			body: `<?ifndef X ?><?define X = "1" ?><?endif ?><?ifndef X ?><?define X = "9" ?><?endif ?><Property Id="Q" Value="$(var.X)" />`,
			note: "<?ifndef X?> is not evaluated: only its first branch was converted",
		},
		"default after a block that defines the variable": {
			body: `<?ifdef D ?><?define X = "d" ?><?endif ?><?ifndef X ?><?define X = "def" ?><?endif ?><Property Id="Q" Value="$(var.X)" />`,
			note: "<?ifndef X?> is not evaluated: only its first branch was converted",
		},
		"ifdef of a variable defined without a value": {
			body: `<?define Debug ?><?ifdef Debug ?><Property Id="DBG" Value="1" /><?endif ?>`,
			note: "<?ifdef Debug?> is not evaluated: only its first branch was converted",
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

			if strings.Contains(string(res.YAML), "{{ fail") || strings.Contains(string(res.YAML), "{{ range") ||
				strings.Contains(string(res.YAML), "{{ if") && !strings.Contains(tc.body, "ifdef Debug") {
				t.Errorf("the block became a template:\n%s", res.YAML)
			}
		})
	}
}

// TestConvertPreprocessorExtendDefine checks that a define that reads a
// variable takes the value the variable has at that point, as WiX does,
// also when a block or a later define changes the variable.
func TestConvertPreprocessorExtendDefine(t *testing.T) {
	t.Parallel()

	src := `<?define Name = "Hello" ?>
<?ifdef Debug ?><?define Name = "$(var.Name) Debug" ?><?endif ?>
<?define Title = "$(var.Name)" ?>
<?ifdef Debug ?><?define Name = "$(var.Title) Debug" ?><?endif ?>
<?define Short = "$(var.Name)" ?>
<?define Name = "x" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="$(var.Short)" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
  </Package>
</Wix>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for vars, want := range map[string]string{"": "Hello", "Debug": "Hello Debug Debug"} {
		opts := config.Options{Vars: map[string]string{}}
		if vars != "" {
			opts.Vars[vars] = ""
		}

		cfg, err := config.Parse(res.YAML, opts)
		if err != nil {
			t.Fatalf("%s: %v\n%s", vars, err, res.YAML)
		}

		if cfg.Product.Name != want {
			t.Errorf("%s: name %q, want %q", vars, cfg.Product.Name, want)
		}
	}
}

// TestConvertPreprocessorRoots checks a file with one root element per
// branch, which is only well-formed one branch at a time.
func TestConvertPreprocessorRoots(t *testing.T) {
	t.Parallel()

	src := `<?ifdef V4 ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" />
</Wix>
<?else ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Other" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301" />
</Wix>
<?endif ?>`

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(res.YAML), "name: Hello") {
		t.Errorf("the first branch was not converted:\n%s", res.YAML)
	}
}

// TestConvertPreprocessorKeeps checks blocks that convert writes as
// templates, and that every build of them validates.
func TestConvertPreprocessorKeeps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string
		want string
		vars []string
	}{
		"key file in a loop": {
			body: `<Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><?foreach L in en;de ?><File Id="f_$(var.L)" Source="$(var.L).txt" /><?endforeach ?></Component>`,
			want: `{{ range $L := split ";" "en;de" }}`,
		},
		"level from a variable set before the block": {
			body: `<?define A = "1" ?><?define B = "$(var.A)" ?><?ifdef D ?><?define A = "2" ?><?else ?><?define A = "3" ?><?endif ?>
    <Feature Id="Main" Level="$(var.B)"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <Property Id="P" Value="$(var.A)" />`,
			want: `A: '{{ if has "D" }}2{{ else }}3{{ end }}'`,
			vars: []string{"D"},
		},
		"define from a default": {
			body: `<?ifndef Ver ?><?define Ver = "1.0" ?><?endif ?><?ifdef D ?><?define Out = "$(var.Ver)-d" ?><?else ?><?define Out = "$(var.Ver)" ?><?endif ?>
    <Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <Property Id="Q" Value="$(var.Out)" />`,
			want: `Out: '{{ if has "D" }}{{ .Ver }}-d{{ else }}{{ .Ver }}{{ end }}'`,
			vars: []string{"D", "Ver"},
		},
		"define from a default defined again": {
			body: `<?ifndef X ?><?define X = "1" ?><?endif ?><?define Y = "$(var.X)" ?><?define X = "2" ?>
    <Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <Property Id="Q" Value="$(var.Y)" />`,
			want: `Y: "1"`,
		},
		"default in a block read through --var": {
			body: `<?ifdef D ?><?ifndef X ?><?define X = "1" ?><?endif ?><?define Y = "$(var.X)-y" ?><?endif ?>
    <Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <Property Id="Q" Value="$(var.Y)" />`,
			want: `Y: '{{ if has "D" }}{{ .X }}-y{{ end }}'`,
			vars: []string{"D"},
		},
		"property in both branches on one line": {
			body: `<Feature Id="Main"><ComponentRef Id="C" /></Feature>
    <Component Id="C" Directory="INSTALLFOLDER"><File Source="a.exe" /></Component>
    <?ifdef P ?><Property Id="MODE" Value="p" /><?else ?><Property Id="MODE" Value="n" /><?endif ?>`,
			want: "MODE: p",
			vars: []string{"P"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder"><Directory Id="INSTALLFOLDER" Name="Hello" /></StandardDirectory>
    ` + tc.body + `
  </Package>
</Wix>`

			res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(string(res.YAML), tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, res.YAML)
			}

			for _, note := range res.Notes {
				if strings.Contains(note.Message, "not evaluated") || strings.Contains(note.Message, "not expanded") ||
					strings.Contains(note.Message, "feature level") {
					t.Errorf("note %q", note.Message)
				}
			}

			for _, set := range append([]string{""}, tc.vars...) {
				opts := config.Options{Vars: map[string]string{}}
				if set != "" {
					opts.Vars[set] = ""
				}

				if _, err := config.Parse(res.YAML, opts); err != nil {
					t.Errorf("%s: %v\n%s", set, err, res.YAML)
				}
			}
		})
	}
}
