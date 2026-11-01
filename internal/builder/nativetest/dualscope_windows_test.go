//go:build windows

package nativetest_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/ice"
	"github.com/jkroepke/msi-builder/internal/testpe"
)

const (
	machineEnvironment = `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvironment    = `HKCU\Environment`
)

// environmentPartsConfig is a dual package whose component adds two parts
// to the end and two to the front of PATH, following the context
// (ENVVAR-04).
const environmentPartsConfig = `schema: msi-builder/v1
product: { name: MsiBuilderEnvParts, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: d7714381-0ecb-45c7-8ddf-8c92b2ba4c8e }
package: { platform: x64, scope: perUserOrMachine }
components:
  - files: [{ source: app.exe }]
    environment:
      - { name: PATH, value: 'C:\MsiBuilderEnv04\append1', action: append }
      - { name: PATH, value: 'C:\MsiBuilderEnv04\append2', action: append }
      - { name: PATH, value: 'C:\MsiBuilderEnv04\prepend1', action: prepend }
      - { name: PATH, value: 'C:\MsiBuilderEnv04\prepend2', action: prepend }
`

// envParts returns the MsiBuilderEnv04 entries of a PATH value in order.
func envParts(value string) []string {
	var parts []string

	for part := range strings.SplitSeq(value, ";") {
		if _, name, ok := strings.Cut(part, `C:\MsiBuilderEnv04\`); ok {
			parts = append(parts, name)
		}
	}

	return parts
}

// entries returns the non-empty entries of a PATH value, joined by ;.
func entries(value string) string {
	var out []string

	for part := range strings.SplitSeq(value, ";") {
		if part != "" {
			out = append(out, part)
		}
	}

	return strings.Join(out, ";")
}

// TestMsiexecEnvironmentParts installs the ENVVAR-04 package per machine
// and per user: each of the four Environment rows adds its part to PATH in
// the context's environment, and the uninstall removes all four and leaves
// the rest of PATH as it was.
func TestMsiexecEnvironmentParts(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		context, key string
		args         []string
	}{
		{"machine", machineEnvironment, []string{`MSIINSTALLPERUSER=""`}},
		{"user", userEnvironment, nil},
	} {
		t.Run(tc.context, func(t *testing.T) {
			dir := t.TempDir()
			path := buildConfig(t, dir, "envparts", environmentPartsConfig, map[string][]byte{"app.exe": app})
			t.Cleanup(func() { _, _ = runMsiexec([]string{"/x", path, "/qn"}, nil) })

			before := regData(t, tc.key, "Path")
			msiexecQuiet(t, filepath.Join(dir, "install.log"), append([]string{"/i", path}, tc.args...)...)

			installed := regData(t, tc.key, "Path")
			parts := envParts(installed)
			t.Logf("ENVVAR-04 %s: PATH parts after the install %v", tc.context, parts)

			slices.Sort(parts)

			if want := []string{"append1", "append2", "prepend1", "prepend2"}; !slices.Equal(parts, want) {
				t.Errorf("parts %v, want %v in %s", parts, want, installed)
			}

			other := machineEnvironment
			if tc.key == machineEnvironment {
				other = userEnvironment
			}

			if got := envParts(regData(t, other, "Path")); len(got) != 0 {
				t.Errorf("the other context's PATH has the parts %v", got)
			}

			msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)

			after := regData(t, tc.key, "Path")
			t.Logf("ENVVAR-04 %s: PATH before %q, after the uninstall %q", tc.context, before, after)

			if got := envParts(after); len(got) != 0 {
				t.Errorf("parts %v left after the uninstall", got)
			}

			// The engine drops empty entries, such as a trailing
			// separator, when it rewrites the variable (ENVVAR-04).
			if entries(after) != entries(before) {
				t.Errorf("PATH %q after the uninstall, want the entries of %q", after, before)
			}
		})
	}
}

// contextConditionsConfig is a dual package with four components that
// each write an HKCU value, conditioned on the ways WiX packages test the
// context: MSIINSTALLPERUSER=1, MSIINSTALLPERUSER<>1, ALLUSERS=1 and NOT
// ALLUSERS (INST-07). FLIP=1 runs a setter of ALLUSERS=1 after
// ValidateProductID in the execute sequence and FLIPUSER=1 one of
// MSIINSTALLPERUSER=1, as packages that choose the context themselves do.
const contextConditionsConfig = `schema: msi-builder/v1
product: { name: MsiBuilderContext, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 6183696d-a8a9-4e58-b769-002d8e08576b }
package: { platform: x64, scope: ${SCOPE} }
components:
  - files: [{ source: app.exe }]
  - { id: MsiPerUser, directory: INSTALLDIR, condition: MSIINSTALLPERUSER=1, registry: [{ root: HKCU, key: Software\MsiBuilderTest\Inst07, name: MsiPerUser, value: x }] }
  - { id: MsiNotPerUser, directory: INSTALLDIR, condition: MSIINSTALLPERUSER<>1, registry: [{ root: HKCU, key: Software\MsiBuilderTest\Inst07, name: MsiNotPerUser, value: x }] }
  - { id: AllUsers, directory: INSTALLDIR, condition: ALLUSERS=1, registry: [{ root: HKCU, key: Software\MsiBuilderTest\Inst07, name: AllUsers, value: x }] }
  - { id: NotAllUsers, directory: INSTALLDIR, condition: NOT ALLUSERS, registry: [{ root: HKCU, key: Software\MsiBuilderTest\Inst07, name: NotAllUsers, value: x }] }
customActions:
  - id: FlipMachine
    setProperty: { name: ALLUSERS, value: "1" }
    schedule: [{ sequence: execute, after: ValidateProductID, condition: FLIP=1 }]
  - id: FlipUser
    setProperty: { name: MSIINSTALLPERUSER, value: "1" }
    schedule: [{ sequence: execute, after: ValidateProductID, condition: FLIPUSER=1 }]
`

// TestMsiexecContextConditions installs the INST-07 package silently in
// each way a context is chosen and records which components installed,
// where the product registered, and the server's ALLUSERS and
// MSIINSTALLPERUSER. The ALLUSERS conditions must follow the context, and
// without a setter the MSIINSTALLPERUSER ones too; what the setters do is
// recorded, not asserted.
func TestMsiexecContextConditions(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		name, scope string
		args        []string
	}{
		{"perUserOrMachine", "perUserOrMachine", nil},
		{"perUserOrMachine MSIINSTALLPERUSER empty", "perUserOrMachine", []string{`MSIINSTALLPERUSER=""`}},
		{"perUserOrMachine MSIINSTALLPERUSER 0", "perUserOrMachine", []string{"MSIINSTALLPERUSER=0"}},
		{"perUserOrMachine ALLUSERS 1", "perUserOrMachine", []string{"ALLUSERS=1"}},
		{"perUserOrMachine setter ALLUSERS 1", "perUserOrMachine", []string{"FLIP=1"}},
		{"perMachineOrUser", "perMachineOrUser", nil},
		{"perMachineOrUser MSIINSTALLPERUSER 1", "perMachineOrUser", []string{"MSIINSTALLPERUSER=1"}},
		{"perMachineOrUser setter MSIINSTALLPERUSER 1", "perMachineOrUser", []string{"FLIPUSER=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := strings.ReplaceAll(contextConditionsConfig, "${SCOPE}", tc.scope)
			path, res := buildConfigResult(t, dir, "context", doc, map[string][]byte{"app.exe": app})
			t.Cleanup(func() {
				_, _ = runMsiexec([]string{"/x", path, "/qn"}, nil)
				_ = exec.CommandContext(context.Background(), "reg.exe", "delete", `HKCU\Software\MsiBuilderTest\Inst07`, "/f").Run()
			})

			log := filepath.Join(dir, "install.log")
			msiexecQuiet(t, log, append([]string{"/i", path}, tc.args...)...)

			props := serverProperties(readLog(log))
			registration := where(regHas(t, "64", `HKCU\Software\Microsoft\Installer\Products\`+squash(res.ProductCode), ""),
				regHas(t, "64", `HKLM\SOFTWARE\Classes\Installer\Products\`+squash(res.ProductCode), ""))

			var installed []string

			for _, name := range []string{"MsiPerUser", "MsiNotPerUser", "AllUsers", "NotAllUsers"} {
				if regHas(t, "64", `HKCU\Software\MsiBuilderTest\Inst07`, name) {
					installed = append(installed, name)
				}
			}

			t.Logf("INST-07 %s: registered %s, INSTALLDIR %q, ALLUSERS %q, MSIINSTALLPERUSER %q, components %v",
				tc.name, registration, props["INSTALLDIR"], props["ALLUSERS"], props["MSIINSTALLPERUSER"], installed)

			want := "NotAllUsers"
			if registration == "machine" {
				want = "AllUsers"
			}

			if registration != "machine" && registration != "user" || !slices.Contains(installed, want) ||
				slices.Contains(installed, map[string]string{"AllUsers": "NotAllUsers", "NotAllUsers": "AllUsers"}[want]) {
				t.Errorf("registered %s with components %v: the ALLUSERS conditions do not follow the context", registration, installed)
			}

			// Without a setter the MSIINSTALLPERUSER conditions follow the
			// context too: the engine empties MSIINSTALLPERUSER per machine.
			if !strings.Contains(tc.name, "setter") && slices.Contains(installed, "MsiPerUser") != (registration == "user") {
				t.Errorf("registered %s with components %v: the MSIINSTALLPERUSER conditions do not follow the context", registration, installed)
			}

			msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)
		})
	}
}

// dualFirewallConfig is a dual package with a firewall rule under its
// feature, whose carrier installs only per machine (INST-08).
const dualFirewallConfig = `schema: msi-builder/v1
product: { name: MsiBuilderDualFirewall, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: 8cf2e5cc-2aee-4411-aa0e-0788ef59224f }
package: { platform: x64, scope: ${SCOPE} }
features:
  - id: Main
    components:
      - files: [{ source: app.exe }]
    firewall: [{ name: MsiBuilderINST08, program: '[#app.exe]', protocol: tcp, port: '50808' }]
`

// firewallRule reports whether netsh shows the INST-08 rule.
func firewallRule(t *testing.T) bool {
	t.Helper()

	return exec.CommandContext(t.Context(), "netsh", "advfirewall", "firewall", "show", "rule", "name=MsiBuilderINST08").Run() == nil
}

// TestMsiexecDualScopeFirewall installs the INST-08 packages silently in
// both contexts, repairs and uninstalls them: the rule exists only after a
// per-machine install and its repair, the per-user install succeeds
// without it, and the uninstall removes it.
func TestMsiexecDualScopeFirewall(t *testing.T) { //nolint:paralleltest // msiexec runs one installation at a time
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct { //nolint:paralleltest // msiexec runs one installation at a time
		name, scope string
		machine     bool
		args        []string
	}{
		{"perUserOrMachine", "perUserOrMachine", false, nil},
		{"perUserOrMachine to machine", "perUserOrMachine", true, []string{`MSIINSTALLPERUSER=""`}},
		{"perMachineOrUser", "perMachineOrUser", true, nil},
		{"perMachineOrUser to user", "perMachineOrUser", false, []string{"MSIINSTALLPERUSER=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := strings.ReplaceAll(dualFirewallConfig, "${SCOPE}", tc.scope)
			path := buildConfig(t, dir, "dualfirewall", doc, map[string][]byte{"app.exe": app})
			t.Cleanup(func() {
				_, _ = runMsiexec([]string{"/x", path, "/qn"}, nil)
				_ = exec.CommandContext(context.Background(), "netsh", "advfirewall", "firewall", "delete", "rule", "name=MsiBuilderINST08").Run()
			})

			if firewallRule(t) {
				t.Fatal("the rule exists before the install")
			}

			for _, step := range []struct {
				name string
				args []string
				want bool
			}{
				{"install", append([]string{"/i", path}, tc.args...), tc.machine},
				{"repair", []string{"/i", path, "REINSTALL=ALL", "REINSTALLMODE=amus"}, tc.machine},
				{"uninstall", []string{"/x", path}, false},
			} {
				log := filepath.Join(dir, step.name+".log")
				msiexecQuiet(t, log, step.args...)

				got := firewallRule(t)
				t.Logf("INST-08 %s %s: ALLUSERS %q, rule %v", tc.name, step.name, serverProperties(readLog(log))["ALLUSERS"], got)

				if got != step.want {
					t.Errorf("%s: rule %v, want %v", step.name, got, step.want)
				}
			}
		})
	}
}

// TestICEDualScopeFirewall runs the ICE suite on both INST-08 packages and
// records ICE105's findings: it reads only the CustomAction type, so the
// netsh actions without impersonation are expected to warn in a package
// that starts per user and to pass in one that starts per machine (CA-09).
//
//nolint:paralleltest // msi.dll's external UI handler is process-wide
func TestICEDualScopeFirewall(t *testing.T) {
	cub := iceCube(t)
	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, scope := range []string{"perUserOrMachine", "perMachineOrUser"} {
		dir := t.TempDir()
		path := buildConfig(t, dir, "dualfirewall-"+scope, strings.ReplaceAll(dualFirewallConfig, "${SCOPE}", scope), map[string][]byte{"app.exe": app})
		requireCleanICE(t, path, cub)

		findings, err := ice.Run(path, cub)
		if err != nil {
			t.Fatal(err)
		}

		warnings := 0

		for _, finding := range findings {
			if finding.ICE == "ICE105" && finding.Type != ice.Info {
				t.Logf("INST-08 %s: %s", scope, finding)

				warnings++
			}
		}

		if scope == "perMachineOrUser" && warnings != 0 {
			t.Errorf("%s: %d ICE105 findings, want none", scope, warnings)
		}
	}
}
