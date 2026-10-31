package builder_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// perUserRollbackConfig is a per-user package with one deferred action;
// the placement and return mode are filled in per case.
const perUserRollbackConfig = `schema: msi-builder/v1
product: { name: A, manufacturer: B, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301 }
package: { platform: x64, scope: %s }
components:
  - files: [{ source: a.txt }]
customActions:
  - id: Step
    exec: { command: '"[System64Folder]cmd.exe" /c exit 1', directory: INSTALLDIR }
    execute: %s
    return: %s
    schedule: [{ sequence: execute, after: %s }]
%s`

// TestPerUserRollbackWarning checks the CA-01 warning: only a per-user
// deferred action that can fail and runs after RegisterProduct gets it.
func TestPerUserRollbackWarning(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		scope, execute, ret, after, extra string
		want                              bool
	}{
		{"perUser", "deferred", "check", "PublishProduct", "", true},
		{"perUser", "deferred", "check", "RegisterProduct", "", true},
		{"perUser", "deferred", "asyncWait", "PublishProduct", "", true},
		{"perUser", "deferred", "check", "InstallFiles", "", false},
		{"perUser", "deferred", "ignore", "PublishProduct", "", false},
		{"perUser", "deferred", "asyncNoWait", "PublishProduct", "", false},
		{"perUser", "rollback", "check", "PublishProduct", "", false},
		{"perUser", "commit", "check", "PublishProduct", "", false},
		{"perMachine", "deferred", "check", "PublishProduct", "", false},
		{"perUserOrMachine", "deferred", "check", "PublishProduct", "", true},
		{"perMachineOrUser", "deferred", "check", "PublishProduct", "", true},
		{"perMachineOrUser", "deferred", "check", "InstallFiles", "", false},
		// A perMachine action never runs in a per-user install (CA-09).
		{"perUserOrMachine", "deferred", "check", "PublishProduct", "    scope: perMachine\n", false},
		{"perUserOrMachine", "deferred", "check", "PublishProduct", "    scope: perUser\n", true},
	} {
		t.Run(tc.scope+"-"+tc.execute+"-"+tc.ret+"-"+tc.after+"-"+strings.TrimSpace(tc.extra), func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Parse(fmt.Appendf(nil, perUserRollbackConfig, tc.scope, tc.execute, tc.ret, tc.after, tc.extra), config.Options{})
			if err != nil {
				t.Fatal(err)
			}

			cfg.BaseDir = t.TempDir()
			if err := os.WriteFile(filepath.Join(cfg.BaseDir, "a.txt"), []byte("a"), 0o600); err != nil {
				t.Fatal(err)
			}

			res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
			if err != nil {
				t.Fatalf("build: %v\n%s", err, res.Diagnostics)
			}

			got := false

			for _, d := range res.Diagnostics {
				if strings.Contains(d.Message, "runs after RegisterProduct") {
					got = true

					if d.Path != "customActions[0].schedule[0]" {
						t.Errorf("warning path = %q", d.Path)
					}
				}
			}

			if got != tc.want {
				t.Errorf("warning = %v, want %v: %s", got, tc.want, res.Diagnostics)
			}
		})
	}
}
