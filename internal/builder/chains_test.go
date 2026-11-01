package builder_test

import (
	"fmt"
	"strings"
	"testing"
)

// chainsDoc has every chain the builder adds after AppSearch and
// CostFinalize in the execute sequence that validate counts, and fixed is
// a custom action at the given number.
func chainsDoc(fixed string) string {
	return `schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
  arp: { installLocation: INSTALLDIR }
package: { platform: x64 }
installDir: { remember: true }
directories:
  - id: ProgramFiles64Folder
    children: [{ id: INSTALLDIR, name: Hello, configurable: true }]
components:
  - id: App
    directory: INSTALLDIR
    files: [{ source: hello.exe }]
properties:
  Q: { value: d, search: { registry: { root: HKLM, key: 'Software\Example', name: Q } } }
firewall: [{ name: r, component: App, program: '[#hello.exe]', port: '80', remoteAddresses: '[PEERS]' }]
customActions:
  - id: Fixed
    setProperty: { name: P, value: x }
    schedule: [{ sequence: execute, at: ` + fixed + ` }]
`
}

// The chains validate counts after AppSearch and CostFinalize are the ones
// the build writes: right after them a custom action builds, one number
// earlier validate refuses it.
func TestBuilderChainsMatchValidate(t *testing.T) {
	t.Parallel()

	res, err := buildMoved(t, chainsDoc("54"))
	if err != nil {
		t.Fatalf("at 54: %v\n%v", err, res.Diagnostics)
	}

	seq := sequenceOf(t, res, "InstallExecuteSequence")

	for _, name := range []string{"SearchApply_Q", "SearchDefault_Q", "RestoreInstallDir"} {
		if n := seq[name]; n < 51 || n > 53 {
			t.Errorf("%s at %d, want 51…53", name, n)
		}
	}

	chained := 0

	for name, n := range seq {
		if name == "SetARPINSTALLLOCATION" || strings.HasPrefix(name, "SetFwRemoteIP") {
			chained++

			if n < 1001 || n > 1003 {
				t.Errorf("%s at %d, want 1001…1003", name, n)
			}
		}
	}

	if chained != 3 {
		t.Errorf("%d actions after CostFinalize, want 3: %v", chained, seq)
	}

	if res, err := buildMoved(t, chainsDoc("1004")); err != nil {
		t.Errorf("at 1004: %v\n%v", err, res.Diagnostics)
	}

	for _, at := range []string{"53", "1003"} {
		res, err := buildMoved(t, chainsDoc(at))
		if err == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "the build chains it after") {
			t.Errorf("at %s: validate did not refuse it: %v", at, res.Diagnostics)
		}
	}
}
