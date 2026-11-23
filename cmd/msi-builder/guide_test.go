package main

import (
	"bytes"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/exampletest"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// guidePages returns the user guides whose examples TestGuideExamples builds.
func guidePages() []string {
	return []string{"../../docs/YAML Schema.md", "../../docs/Migrating from WiX.md"}
}

// yamlBlock matches a fenced YAML code block of the guide.
var yamlBlock = regexp.MustCompile("(?ms)^```yaml\n(.*?)^```")

// heading matches a heading line of the guide.
var heading = regexp.MustCompile("(?m)^#+ (.+)$")

// TestGuideExamples builds every YAML example of the user guides without a
// warning. The first example of a page is a whole file; every later one is a
// part of a file, whose top-level keys replace those of the first. A part with
// components or features replaces both, so its files are not installed
// twice. Subtests are named after the heading above the example. Each file
// a source or license names is created: a versioned program for .exe and
// .dll, text otherwise, and a folder source gets a program, a .pdb and a
// text file. Each file that icons and binaries name is text too.
func TestGuideExamples(t *testing.T) {
	t.Parallel()

	for _, guidePage := range guidePages() {
		t.Run(path.Base(guidePage), func(t *testing.T) {
			t.Parallel()

			testGuidePage(t, guidePage)
		})
	}
}

func testGuidePage(t *testing.T, guidePage string) {
	t.Helper()

	page, err := os.ReadFile(guidePage)
	if err != nil {
		t.Fatal(err)
	}

	// A Windows checkout may have turned the line ends into CRLF.
	page = bytes.ReplaceAll(page, []byte("\r\n"), []byte("\n"))

	blocks := yamlBlock.FindAllSubmatchIndex(page, -1)
	if len(blocks) < 2 || len(blocks) != bytes.Count(page, []byte("```y")) {
		t.Fatalf("%s has %d YAML examples of %d blocks fenced with ```y", guidePage, len(blocks), bytes.Count(page, []byte("```y")))
	}

	var base map[string]any
	if err := yaml.Unmarshal(page[blocks[0][2]:blocks[0][3]], &base); err != nil {
		t.Fatal(err)
	}

	for i, block := range blocks {
		name := strconv.Itoa(i)
		if titles := heading.FindAllSubmatch(page[:block[0]], -1); len(titles) > 0 {
			name += " " + string(titles[len(titles)-1][1])
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			buildGuideExample(t, base, page[block[2]:block[3]])
		})
	}
}

func buildGuideExample(t *testing.T, base map[string]any, example []byte) {
	t.Helper()

	var part map[string]any
	if err := yaml.Unmarshal(example, &part); err != nil {
		t.Fatalf("%v\n%s", err, example)
	}

	doc := maps.Clone(base)

	if part["components"] != nil || part["features"] != nil {
		delete(doc, "components")
		delete(doc, "features")
	}

	maps.Copy(doc, part)

	src, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeGuideFile(t, dir, "msi.yaml", src)

	for _, name := range guideInputs(doc) {
		if strings.HasSuffix(name, "/") {
			for _, file := range []string{"app.exe", "app.pdb", "lib/readme.txt"} {
				writeGuideFile(t, dir, path.Join(name, file), guideInput(file))
			}

			continue
		}

		writeGuideFile(t, dir, name, guideInput(name))
	}

	cfg, err := config.Load(filepath.Join(dir, "msi.yaml"), config.Options{Vars: map[string]string{"VERSION": "1.4.0"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}

	res, err := builder.Build(cfg, builder.Options{BuildTime: exampletest.BuildTime})
	if err != nil {
		t.Fatalf("%v\n%v\n%s", err, res.Diagnostics, src)
	}

	if len(res.Diagnostics) > 0 {
		t.Errorf("%v\n%s", res.Diagnostics, src)
	}
}

// guideInputs returns the values of every source and license key in value,
// and the files of the icons and binaries maps.
func guideInputs(value any) []string {
	var names []string

	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if name, ok := item.(string); ok && (key == "source" || key == "license") {
				names = append(names, name)

				continue
			}

			if files, ok := item.(map[string]any); ok && (key == "icons" || key == "binaries") {
				for _, file := range files {
					if name, ok := file.(string); ok {
						names = append(names, name)
					}
				}

				continue
			}

			names = append(names, guideInputs(item)...)
		}
	case []any:
		for _, item := range value {
			names = append(names, guideInputs(item)...)
		}
	}

	slices.Sort(names)

	return slices.Compact(names)
}

func guideInput(name string) []byte {
	switch path.Ext(name) {
	case ".exe", ".dll":
		return testpe.Build(testpe.Options{FileVersion: "1.4.0.0", Languages: []int{1033}})
	default:
		return []byte("Example content\n")
	}
}

func writeGuideFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()

	file := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
