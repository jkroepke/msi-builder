package wixconvert

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// TestScalarBlanksAtBreaks checks that a string with a blank at the end of
// a line is written double-quoted: as a literal block, its trailing blanks
// would not survive an editor that trims them.
func TestScalarBlanksAtBreaks(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]string{
		"Installed OR \nVersionNT >= 603": `k: "Installed OR \nVersionNT >= 603"`,
		"a\t\nb":                          `k: "a\t\nb"`,
		"a\nb ":                           `k: "a\nb "`,
		"a\nb":                            "k: |-\n  a\n  b",
	} {
		doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
			Kind:    yaml.MappingNode,
			Content: []*yaml.Node{scalar("k"), scalar(value)},
		}}}

		out, err := encodeDocs([]*yaml.Node{doc})
		if err != nil {
			t.Fatal(err)
		}

		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%q: got %q, want %q", value, got, want)
		}
	}
}
