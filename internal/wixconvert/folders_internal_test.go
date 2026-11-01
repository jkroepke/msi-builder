package wixconvert

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestICE105FoldersMatchConfig checks that convert's list of the folders
// ICE105 rejects matches the validator's, so that convert drops exactly the
// action directories the validator refuses.
func TestICE105FoldersMatchConfig(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("../../pkg/config/names.go")
	if err != nil {
		t.Fatal(err)
	}

	block := regexp.MustCompile(`(?s)var ice105Folders = set\(.*?\n(.*?)\n\)`).FindSubmatch(src)
	if block == nil {
		t.Fatal("no ice105Folders in pkg/config/names.go")
	}

	want := regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(string(block[1]), -1)
	names := make([]string, 0, len(want))

	for _, m := range want {
		names = append(names, m[1])
	}

	got := make([]string, 0, len(ice105Folders))
	for name := range ice105Folders {
		got = append(got, name)
	}

	slices.Sort(names)
	slices.Sort(got)

	if !slices.Equal(got, names) {
		t.Errorf("convert has %s, pkg/config has %s", strings.Join(got, ", "), strings.Join(names, ", "))
	}
}
