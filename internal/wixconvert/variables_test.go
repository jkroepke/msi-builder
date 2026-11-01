package wixconvert_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/wixconvert"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// convertParse converts a WiX source and renders the result with vars.
func convertParse(t *testing.T, src string, vars map[string]string) (*wixconvert.Result, *config.Config) {
	t.Helper()

	res, err := wixconvert.Convert(strings.NewReader(src), wixconvert.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(res.YAML, config.Options{Vars: vars})
	if err != nil {
		t.Fatalf("%v\n%s", err, res.YAML)
	}

	if err := config.Validate(cfg).Err(); err != nil {
		t.Fatalf("the converted file does not validate: %v\n%s", err, res.YAML)
	}

	return res, cfg
}

// TestConvertRedefinedVar checks a variable that a define outside the
// blocks sets again: each reader takes the value it has at the reader,
// as in WiX, not the last one.
func TestConvertRedefinedVar(t *testing.T) {
	t.Parallel()

	src := `<?define Sub = "one" ?>
<?define Both = "$(var.Sub)-$(var.Sub)" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Component Id="A" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\$(var.Sub)" Name="a" Value="$(var.Both)" Type="string" KeyPath="yes" />
    </Component>
    <?if $(var.Sub) = one ?>
    <Component Id="C" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\c" Name="c" Value="x" Type="string" KeyPath="yes" />
    </Component>
    <?endif ?>
    <?foreach L in en;de ?>
    <Component Id="L_$(var.L)" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\$(var.Sub)\$(var.L)" Name="l" Value="x" Type="string" KeyPath="yes" />
    </Component>
    <?endforeach ?>
<?define Sub = "two" ?>
    <Component Id="B" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\$(var.Sub)" Name="b" Value="$(var.Both)" Type="string" KeyPath="yes" />
    </Component>
    <Feature Id="Main">
      <ComponentRef Id="A" />
      <ComponentRef Id="B" />
      <ComponentRef Id="C" />
      <?foreach L in en;de ?><ComponentRef Id="L_$(var.L)" /><?endforeach ?>
    </Feature>
  </Package>
</Wix>`

	res, cfg := convertParse(t, src, nil)

	got := make(map[string]string)

	for _, comp := range cfg.Components {
		got[comp.ID] = comp.Registry[0].Key + "=" + strings.Join(comp.Registry[0].Value, ";")
	}

	want := map[string]string{
		"A":    `Software\Example\one=one-one`,
		"C":    `Software\Example\c=x`,
		"L_en": `Software\Example\one\en=x`,
		"L_de": `Software\Example\one\de=x`,
		"B":    `Software\Example\two=one-one`,
	}

	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q\n%s", id, got[id], w, res.YAML)
		}
	}

	if len(got) != len(want) {
		t.Errorf("components %v, want %v", got, want)
	}

	// The defines win over --var, as over -d in WiX.
	_, cfg = convertParse(t, src, map[string]string{"Sub": "three"})
	for _, comp := range cfg.Components {
		if comp.ID == "B" && comp.Registry[0].Key != `Software\Example\two` {
			t.Errorf("B with --var Sub=three: key %q", comp.Registry[0].Key)
		}
	}
}

const (
	platformRoot = `<?if $(var.Arch) = x86 ?>
<?define Root = "ProgramFilesFolder" ?>
<?else ?>
<?define Root = "ProgramFiles64Folder" ?>
<?endif ?>`
	perUserRoot = `<?ifdef PerUser ?>
<?define Root = "LocalAppDataFolder" ?>
<?else ?>
<?define Root = "ProgramFiles64Folder" ?>
<?endif ?>`
)

// TestConvertDirectoryIDVar checks directory IDs that come from a
// variable: the converter knows the standard folder behind them.
func TestConvertDirectoryIDVar(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		defines string
		vars    map[string]string
		parent  string
		notes   []string
	}{
		"static program files": {
			defines: `<?define Root = "ProgramFiles64Folder" ?>`,
			parent:  "ProgramFiles64Folder",
		},
		"static": {
			defines: `<?define Root = "LocalAppDataFolder" ?>`,
			parent:  "LocalAppDataFolder",
		},
		"static 32-bit program files": {
			defines: `<?define Root = "ProgramFilesFolder" ?>`,
			parent:  "ProgramFilesFolder",
		},
		"folder per platform, x86": {
			defines: platformRoot,
			vars:    map[string]string{"Arch": "x86"},
			parent:  "ProgramFilesFolder",
		},
		"folder per platform, x64": {
			defines: platformRoot,
			vars:    map[string]string{"Arch": "x64"},
			parent:  "ProgramFiles64Folder",
		},
		"other folder per build": {
			defines: perUserRoot,
			vars:    map[string]string{"PerUser": ""},
			parent:  "LocalAppDataFolder",
		},
		"other folder per build, default": {
			defines: perUserRoot,
			parent:  "ProgramFiles64Folder",
		},
		"ifndef default": {
			defines: `<?ifndef Root ?><?define Root = "ProgramFiles64Folder" ?><?endif ?>`,
			parent:  "ProgramFiles64Folder",
		},
		"ifndef default with --var": {
			defines: `<?ifndef Root ?><?define Root = "ProgramFiles64Folder" ?><?endif ?>`,
			vars:    map[string]string{"Root": "LocalAppDataFolder"},
			parent:  "LocalAppDataFolder",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<?define Inst = "INSTALLFOLDER" ?>
` + tc.defines + `
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="$(var.Root)">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </Directory>
    <DirectoryRef Id="$(var.Root)">
      <Directory Id="Other" Name="Other" />
    </DirectoryRef>
    <Component Id="Main" Directory="$(var.Inst)">
      <File Source="hello.exe" />
    </Component>
    <Component Id="Extra" Directory="Other">
      <File Source="extra.exe" />
    </Component>
    <Feature Id="Main">
      <ComponentRef Id="Main" />
      <ComponentRef Id="Extra" />
    </Feature>
  </Package>
</Wix>`

			res, cfg := convertParse(t, src, tc.vars)

			if dir := cfg.Components[0].Directory; dir != "" && dir != "INSTALLFOLDER" {
				t.Errorf("Main in %q, want the install dir", cfg.Components[0].Directory)
			}

			if cfg.InstallDir.ID != "INSTALLFOLDER" || cfg.InstallDir.Name != "Hello" || cfg.InstallDir.Parent != tc.parent {
				t.Errorf("installDir %+v, want INSTALLFOLDER Hello below %q\n%s", cfg.InstallDir, tc.parent, res.YAML)
			}

			// A folder that a block picks per build has no one component
			// code (WiX's code depends on the folder).
			if tc.vars != nil && strings.Contains(string(res.YAML), "guid:") {
				t.Errorf("a component code below a per-build folder\n%s", res.YAML)
			}

			for _, note := range res.Notes {
				if tc.notes == nil && strings.Contains(note.Message, "first branch") {
					t.Errorf("note %q, want the blocks kept", note.Message)
				}
			}

			for _, want := range tc.notes {
				found := false
				for _, note := range res.Notes {
					found = found || strings.Contains(note.Message, want)
				}

				if !found {
					t.Errorf("no note %q in %v", want, res.Notes)
				}
			}

			for _, comp := range cfg.Components {
				if comp.ID == "Extra" && (!strings.HasSuffix(comp.Directory, "Other") || strings.Contains(comp.Directory, "Root")) {
					t.Errorf("Extra in %q, want the standard folder's Other\n%s", comp.Directory, res.YAML)
				}
			}
		})
	}
}

// TestConvertServiceNameVar checks a ServiceInstall and its
// ServiceControl when one names the service with a variable and the
// other without.
func TestConvertServiceNameVar(t *testing.T) {
	t.Parallel()

	for name, names := range map[string][2]string{
		"install from a variable":   {"$(var.Svc)", "HelloSvc"},
		"control from a variable":   {"hellosvc", "$(var.Svc)"},
		"both from a variable":      {"$(var.Svc)", "$(var.Svc)"},
		"redefined variable":        {"$(var.Svc)", "$(var.Old)"},
		"block variable, two forms": {"$(Blk)", "$(var.Blk)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `<?define Svc = "HelloSvc" ?>
<?define Old = "$(var.Svc)" ?>
<?ifdef Other ?><?define Blk = "Other" ?><?else ?><?define Blk = "HelloSvc" ?><?endif ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <Component Id="Svc" Directory="INSTALLFOLDER">
      <File Source="hello.exe" KeyPath="yes" />
      <ServiceInstall Name="` + names[0] + `" Start="auto" Type="ownProcess" ErrorControl="normal" />
      <ServiceControl Id="SvcCtl" Name="` + names[1] + `" Stop="uninstall" Remove="uninstall" Wait="no" />
    </Component>
    <Feature Id="Main"><ComponentRef Id="Svc" /></Feature>
  </Package>
</Wix>
<?define Svc = "Other" ?>`

			res, cfg := convertParse(t, src, nil)
			comp := cfg.Components[0]

			if len(comp.Services) != 1 || len(comp.ServiceControl) != 0 {
				t.Fatalf("services %d, serviceControl %d, want the control in the service\n%s", len(comp.Services), len(comp.ServiceControl), res.YAML)
			}

			svc := comp.Services[0]
			if !strings.EqualFold(svc.Name, "HelloSvc") {
				t.Errorf("service name %q, want HelloSvc", svc.Name)
			}

			ctl := svc.Control
			if ctl.StartOnInstall == nil || *ctl.StartOnInstall || ctl.StopOnInstall == nil || *ctl.StopOnInstall ||
				ctl.Wait == nil || *ctl.Wait {
				t.Errorf("control %+v, want no start or stop on install and no wait\n%s", ctl, res.YAML)
			}
		})
	}
}

// TestConvertRedefinedVarEdges checks the readers of a redefined variable
// that convert cannot place, or that sit in conditions.
func TestConvertRedefinedVarEdges(t *testing.T) {
	t.Parallel()

	src := `<?define X = "a b" ?>
<?define Y = "a" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <?if $(var.X) = "a b" ?>
    <Component Id="A" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\a" Name="a" Value="$(var.X)" Type="string" KeyPath="yes" />
    </Component>
    <?endif ?>
    <?if "$(var.Y)" = "a" ?>
    <Component Id="B" Directory="INSTALLFOLDER">
      <RegistryValue Root="HKLM" Key="Software\Example\b" Name="b" Value="$(var.Y)" Type="string" KeyPath="yes" />
    </Component>
    <?endif ?>
<?define X = "c" ?><?define Y = "b" ?>
<?define Y = "a" ?><Component Id="C" Directory="INSTALLFOLDER"><RegistryValue Root="HKLM" Key="Software\Example\c" Name="c" Value="$(var.X)$(var.Y)" Type="string" KeyPath="yes" /></Component>
    <Feature Id="Main">
      <ComponentRef Id="A" />
      <ComponentRef Id="B" />
      <ComponentRef Id="C" />
    </Feature>
  </Package>
</Wix>`

	res, cfg := convertParse(t, src, nil)

	got := make(map[string]string)
	for _, comp := range cfg.Components {
		got[comp.ID] = strings.Join(comp.Registry[0].Value, ";")
	}

	want := map[string]string{"A": "a b", "B": "a", "C": "ca"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q\n%s", id, got[id], w, res.YAML)
		}
	}

	// Y is defined on the line of C, which reads it: WiX reads "a" or "b"
	// by their order on the line.
	if len(res.Notes) != 1 || res.Notes[0].Line != 20 || !strings.Contains(res.Notes[0].Message, "defines Y and reads it") {
		t.Errorf("notes %v, want one for the define of Y on the line of C; both blocks are kept", res.Notes)
	}

	if !strings.Contains(string(res.YAML), "{{ if eq") {
		t.Errorf("the blocks are not templates\n%s", res.YAML)
	}
}

// TestConvertRedefinedLoopVar checks a <?foreach?> variable that has the
// name of a define: the readers in the loop read the loop variable.
func TestConvertRedefinedLoopVar(t *testing.T) {
	t.Parallel()

	src := `<?define Lang = "en" ?>
<?define Svc = "Hello" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>
    <?foreach Lang in de;fr ?>
    <Property Id="P_$(var.Lang)" Value="$(var.Lang)" />
    <?endforeach ?>
    <Property Id="BEFORE" Value="$(var.Lang)" />
<?define Lang = "xx" ?>
    <Feature Id="Main" />
  </Package>
</Wix>`

	res, cfg := convertParse(t, src, nil)

	for id, want := range map[string]string{"P_de": "de", "P_fr": "fr", "BEFORE": "en"} {
		if prop := cfg.Properties[id]; prop == nil || prop.Value != want {
			t.Errorf("%s: %+v, want %q\n%s", id, prop, want, res.YAML)
		}
	}
}

// TestConvertStandardFolderVar checks a variable with one value that is a
// standard folder: every reader gets the folder, so the literal and the
// variable are one root and the platform follows the folder.
func TestConvertStandardFolderVar(t *testing.T) {
	t.Parallel()

	src := `<?define PF = "ProgramFilesFolder" ?>
<?define PATH = "p" ?><?define X = "$(var.Ext)" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <Directory Id="$(var.PF)">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
      <Directory Id="DATA1" Name="Data1" />
    </Directory>
    <StandardDirectory Id="ProgramFilesFolder">
      <Directory Id="DATA2" Name="Data2" />
    </StandardDirectory>
    <Property Id="P1" Value="[DATA1]" />
    <Property Id="P2" Value="[DATA2]" />
    <Property Id="ROOT" Value="[$(var.PF)]" />
    <?if $(var.X) = "on" ?><Property Id="MODE" Value="on" /><?else ?><Property Id="MODE" Value="off" /><?endif ?>
    <Property Id="E" Value="$(env.PATH)" /><?define PATH = "q" ?>
    <Component Id="A" Directory="DATA1"><File Source="a.txt" /></Component>
    <Component Id="B" Directory="DATA2"><File Source="b.txt" /></Component>
    <Feature Id="Main"><ComponentRef Id="A" /><ComponentRef Id="B" /></Feature>
  </Package>
</Wix>
<?define X = "1" ?>`

	res, cfg := convertParse(t, src, map[string]string{"Ext": "on"})

	if !strings.Contains(string(res.YAML), "platform: x86") {
		t.Errorf("platform %q, want x86 from ProgramFilesFolder\n%s", cfg.Package.Platform, res.YAML)
	}

	if got := cfg.Properties["ROOT"].Value; got != "[ProgramFilesFolder]" {
		t.Errorf("ROOT %q, want [ProgramFilesFolder]", got)
	}

	if !strings.Contains(string(res.YAML), "{{ if eq .Ext") {
		t.Errorf("the <?if?> on a variable set from --var is not a template\n%s", res.YAML)
	}

	if got := cfg.Properties["MODE"].Value; got != "on" {
		t.Errorf("MODE %q, want on", got)
	}

	for _, note := range res.Notes {
		if strings.Contains(note.Message, "PATH") {
			t.Errorf("note %q on $(env.PATH), which reads no define", note.Message)
		}
	}
}

// TestConvertRedefinedSameLineBranches checks <?elseif?> branches on one
// line in a file that redefines a variable: each keeps its condition.
func TestConvertRedefinedSameLineBranches(t *testing.T) {
	t.Parallel()

	src := `<?define Z = "1" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <?if $(var.A) = 1 ?><Property Id="P" Value="one" /><?elseif $(var.A) = 2 ?><Property Id="P" Value="two" /><?elseif $(var.A) = 3 ?><Property Id="P" Value="three" /><?endif ?>
    <Feature Id="Main" />
  </Package>
</Wix>
<?define Z = "2" ?>`

	for a, want := range map[string]string{"1": "one", "2": "two", "3": "three"} {
		res, cfg := convertParse(t, src, map[string]string{"A": a})
		if prop := cfg.Properties["P"]; prop == nil || prop.Value != want {
			t.Errorf("A=%s: P %+v, want %q\n%s", a, prop, want, res.YAML)
		}
	}
}

// TestConvertRedefinedIfdefBranches checks the <?elseif?> branches of a
// block that <?ifdef?> opens, which read a redefined variable.
func TestConvertRedefinedIfdefBranches(t *testing.T) {
	t.Parallel()

	src := `<?define A = "1" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <?ifdef B ?><Property Id="P" Value="b" /><?elseif $(var.Q) = $(var.A) ?><Property Id="P" Value="one" /><?elseif $(var.Q) = 2 ?><Property Id="P" Value="two" /><?endif ?>
    <Feature Id="Main" />
  </Package>
</Wix>
<?define A = "2" ?>`

	for q, want := range map[string]string{"1": "one", "2": "two"} {
		res, cfg := convertParse(t, src, map[string]string{"Q": q})
		if prop := cfg.Properties["P"]; prop == nil || prop.Value != want {
			t.Errorf("Q=%s: P %+v, want %q\n%s", q, prop, want, res.YAML)
		}

		for _, note := range res.Notes {
			if strings.Contains(note.Message, "branch") {
				t.Errorf("note %q, want the block kept", note.Message)
			}
		}
	}
}

// TestConvertRedefinedIDs checks IDs that read a variable defined more
// than once, before and after its last define: they still match.
func TestConvertRedefinedIDs(t *testing.T) {
	t.Parallel()

	src := `<?define C = "MainComp" ?>
<?define Dir = "APPDIR" ?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="1.0.0" UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="newer" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello">
        <Directory Id="$(var.Dir)" Name="bin" />
      </Directory>
    </StandardDirectory>
    <Component Id="$(var.C)" Directory="INSTALLFOLDER"><File Source="a.txt" /></Component>
<?define C = "Other" ?><?define C = "MainComp" ?><?define Dir = "APPDIR" ?>
    <Component Id="Bin" Directory="$(var.Dir)"><File Source="b.txt" /></Component>
    <Feature Id="Main"><ComponentRef Id="$(var.C)" /><ComponentRef Id="Bin" /></Feature>
  </Package>
</Wix>`
	res, cfg := convertParse(t, src, nil)

	if got := cfg.Features[0].Components; len(got) != 2 {
		t.Errorf("feature components %v, want MainComp and Bin\n%s", got, res.YAML)
	}

	for _, comp := range cfg.Components {
		if comp.ID == "Bin" && !strings.HasSuffix(comp.Directory, "bin") && comp.Directory != "APPDIR" {
			t.Errorf("Bin in %q, want APPDIR\n%s", comp.Directory, res.YAML)
		}
	}
}
