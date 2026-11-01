package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// componentSearchBase has a component keyed on a folder (A), one keyed on a
// file (B) and one keyed on a registry value (C).
const componentSearchBase = `schema: msi-builder/v1
product: { name: Hello, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64 }
components:
  - id: AppFolder
    guid: 1a2b3c4d-0000-4000-8000-00000000000a
    directory: data
    createFolder: true
    keyPath: directory
  - id: AppExe
    guid: 1a2b3c4d-0000-4000-8000-00000000000b
    files: [{ source: hello.exe }]
  - id: AppReg
    guid: 1a2b3c4d-0000-4000-8000-00000000000c
    registry: [{ id: Marker, root: HKLM, key: 'SOFTWARE\Example', name: Marker, value: x }]
  - id: Links
    guid: 1a2b3c4d-0000-4000-8000-00000000000d
    shortcuts: [{ name: Example, directory: ProgramMenuFolder, url: 'https://example.com' }]
`

// TestComponentSearchKeyPath checks CompLocator.Type: 0 for a component of
// the package keyed on its folder, also under another search, 1 for one
// keyed on a file, and keyPath for another product's component (SEARCH-08).
func TestComponentSearchKeyPath(t *testing.T) {
	t.Parallel()

	database := buildYAML(t, componentSearchBase+`properties:
  OWN_DIR: { search: { component: { guid: 1A2B3C4D-0000-4000-8000-00000000000A } } }
  OWN_DIR_EXPLICIT: { search: { component: { guid: '{1a2b3c4d-0000-4000-8000-00000000000a}', keyPath: directory } } }
  OWN_FILE: { search: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000b } } }
  OWN_UNDER: { search: { directory: { path: logs, under: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000a } } } } }
  OTHER_FILE: { search: { component: { guid: 6a0d3c2e-5b1f-4e7a-9c3d-2f8e1b4a7c60 } } }
  OTHER_FILE_EXPLICIT: { search: { component: { guid: 6a0d3c2e-5b1f-4e7a-9c3d-2f8e1b4a7c61, keyPath: file } } }
  OTHER_DIR: { search: { component: { guid: 6a0d3c2e-5b1f-4e7a-9c3d-2f8e1b4a7c62, keyPath: directory } } }
`)

	types := make(map[any]any)
	for _, r := range rows(t, database, "CompLocator") {
		types[r[0]] = r[2]
	}

	for sig, want := range map[string]int{
		"Search_OWN_DIR":             0,
		"Search_OWN_DIR_EXPLICIT":    0,
		"Search_OWN_FILE":            1,
		"In_Search_OWN_UNDER":        0,
		"Search_OTHER_FILE":          1,
		"Search_OTHER_FILE_EXPLICIT": 1,
		"Search_OTHER_DIR":           0,
	} {
		if got := types[sig]; got != want {
			t.Errorf("CompLocator %s Type = %v, want %d", sig, got, want)
		}
	}
}

// buildComponentSearch builds componentSearchBase with props and returns the
// result and error.
func buildComponentSearch(t *testing.T, props string) (*builder.Result, error) {
	t.Helper()

	cfg, err := config.Parse([]byte(componentSearchBase+"properties:\n"+props), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	cfg.BaseDir = t.TempDir()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	if err := os.WriteFile(filepath.Join(cfg.BaseDir, "hello.exe"), exe, 0o600); err != nil {
		t.Fatal(err)
	}

	return builder.Build(cfg, builder.Options{BuildTime: buildTime})
}

// TestComponentSearchKeyPathMismatch checks that a keyPath that contradicts
// a component of the package is an error, and that a search for one keyed
// on a registry value warns.
func TestComponentSearchKeyPathMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, props, want string
		fails             bool
	}{
		{
			name:  "file on folder",
			props: "  P: { search: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000a, keyPath: file } } }\n",
			want:  "properties.P.search.component.keyPath: component AppFolder of this package has a directory key path, not file",
			fails: true,
		},
		{
			name: "directory on file under",
			props: "  P: { search: [{ directory: { path: 'C:\\x' } }, { directory: { path: logs, under: " +
				"{ component: { guid: 1a2b3c4d-0000-4000-8000-00000000000b, keyPath: directory } } } }] }\n",
			want:  "properties.P.search[1].directory.under.component.keyPath: component AppExe of this package has a file key path, not directory",
			fails: true,
		},
		{
			name:  "registry key path",
			props: "  P: { search: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000c } } }\n",
			want:  "properties.P.search.component.guid: component AppReg of this package has a registry key path",
		},
		{
			name:  "registry key path with keyPath",
			props: "  P: { search: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000c, keyPath: directory } } }\n",
			want:  "properties.P.search.component.keyPath: component AppReg of this package has a registry key path, not directory",
			fails: true,
		},
		{
			name:  "shortcuts only",
			props: "  P: { search: { component: { guid: 1a2b3c4d-0000-4000-8000-00000000000d } } }\n",
			want:  "properties.P.search.component.guid: component Links of this package has a registry key path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := buildComponentSearch(t, tc.props)
			if (err != nil) != tc.fails {
				t.Fatalf("err = %v, want failure %v", err, tc.fails)
			}

			msgs := make([]string, 0, len(res.Diagnostics))
			for _, d := range res.Diagnostics {
				msgs = append(msgs, fmt.Sprintf("%s: %s", d.Path, d.Message))
			}

			if got := strings.Join(msgs, "\n"); !strings.Contains(got, tc.want) {
				t.Errorf("diagnostics\n%s\nwant %q", got, tc.want)
			}
		})
	}
}
