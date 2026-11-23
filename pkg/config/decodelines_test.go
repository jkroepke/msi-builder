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
		"close value": {
			head + "components:\n  - files:\n      - source: a.exe\n        close: soon\n",
			"construct errors: line 5: close is true, false or a mapping: cannot construct !!str `soon` into bool",
		},
		"purge value": {
			head + "components:\n  - directory: d\n    purge: sometimes\n",
			"construct errors: line 4: purge is true, false or a mapping: cannot construct !!str `sometimes` into bool",
		},
		"merge in property": {head + "properties:\n  P: {<<: 1, value: a}\n", "construct errors: line 3: map merge requires map"},
		"binary platform":   {head + "package:\n  platform: !!binary \"@@@\"\n", "construct errors: line 3: !!binary value contains invalid base64 data"},
		"binary close": {
			head + "components:\n  - files:\n      - source: a.exe\n        close: !!binary \"@@@\"\n",
			"construct errors: line 5: close is true, false or a mapping: !!binary value contains invalid base64 data",
		},
		"binary option": {
			head + "ui:\n  dialogs:\n    - {id: D, title: T, after: X, fields: [{property: P, type: choice, options: [a, !!binary \"@@@\"]}]}\n",
			"construct errors: line 4: !!binary value contains invalid base64 data",
		},
		"binary argument": {
			head + "components:\n  - services:\n      - name: S\n        arguments: [a, !!binary \"@@@\"]\n",
			"construct errors: line 5: !!binary value contains invalid base64 data",
		},
		"merge in search": {
			head + "properties:\n  P:\n    search: {<<: 1, directory: {path: 'C:\\a'}}\n",
			"construct errors: line 4: map merge requires map",
		},
	} {
		_, err := config.Parse([]byte(tc.yaml), config.Options{})
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Count(err.Error(), "line ") != 1 ||
			strings.Contains(err.Error(), "go-yaml load error") {
			t.Errorf("%s: got %v, want one line number and %q", name, err, tc.want)
		}
	}
}

// TestTableNegativeZero checks that -0 in a custom table row is the integer
// 0, as it was before yaml.v4 resolved it to a float.
func TestTableNegativeZero(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]byte("schema: msi-builder/v1\ntables:\n  - name: T\n    rows: [[a, -0]]\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Tables[0].Rows[0][1]; got != 0 {
		t.Errorf("cell %#v, want the int 0", got)
	}
}
