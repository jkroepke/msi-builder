package config_test

import (
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestDecodeErrorLines checks that an error from a custom decoding hook
// names its line once, the line of the entry at fault: yaml.v4 puts the
// hook node's line in front of any error that is not its own kind.
func TestDecodeErrorLines(t *testing.T) {
	t.Parallel()

	const head = "schema: msi-builder/v1\n"

	for name, tc := range map[string]struct{ yaml, want string }{
		"platform":       {head + "package:\n  platform: ia64\n", "construct errors: line 3: unknown platform \"ia64\""},
		"property twice": {head + "properties:\n  A: 1\n  A: 2\n", "line 4: "},
		"property list":  {head + "properties:\n  A:\n    - 1\n", "construct errors: line 4: property A must be a value or a mapping"},
		"null option": {
			head + "ui:\n  dialogs:\n    - {id: D, title: T, after: X, fields: [{property: P, type: choice, options: [\n        a,\n        ~]}]}\n",
			"construct errors: line 6: an option cannot be empty",
		},
		"search key": {
			head + "properties:\n  P:\n    search:\n      directory:\n        path: 'C:\\a'\n        bogus: 1\n",
			"construct errors: line 7: field bogus not found in type config.DirectorySearch",
		},
		"raw type name": {head + "upgrade:\n  bogus: 1\n", "construct errors: line 3: field bogus not found in type config.Upgrade"},
	} {
		_, err := config.Parse([]byte(tc.yaml), config.Options{})
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Count(err.Error(), "line ") != 1 {
			t.Errorf("%s: got %v, want one line number and %q", name, err, tc.want)
		}
	}
}
