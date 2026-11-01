//go:build windows

package nativetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/testpe"
)

// CA-10: firewall rules for any protocol with a port, and ignoreFailure.
// Every rule the cases add has a name starting with MsiBuilderCA10, and the
// cleanups delete only those names: an unscoped delete would remove every
// inbound rule (CA-06).

// firewallCA10Config is a package with one rule. The verbs fill in the
// product name, the upgrade code, the protocol, the port line, the remote
// addresses and ignoreFailure.
const firewallCA10Config = `schema: msi-builder/v1
product: { name: %[1]s, manufacturer: msi-builder tests, version: 1.0.0, upgradeCode: %[2]s }
package: { platform: x64 }
installDir: { name: %[1]s }
properties:
  REMOTE: ''
components:
  - id: App
    files: [{ source: app.exe }]
firewall:
  - name: %[1]s
    component: App
    program: '[#app.exe]'
    protocol: %[3]s
%[4]s    remoteAddresses: '%[5]s'
    ignoreFailure: %[6]t
`

// firewallCA10Wix is the WiX source of the black-box comparison: a rule
// with a port and no Protocol, and one with IgnoreFailure whose remote
// address comes from a property.
const firewallCA10Wix = `<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs" xmlns:fw="http://wixtoolset.org/schemas/v4/wxs/firewall">
  <Package Name="MsiBuilderCA10Wix" Manufacturer="msi-builder tests" Version="1.0.0" UpgradeCode="8d4b2e61-7c3a-4f19-b5e0-2a6c9d1f3e84" Scope="perMachine">
    <MediaTemplate EmbedCab="yes" />
    <Property Id="REMOTE" Secure="yes" />
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="MsiBuilderCA10Wix">
        <Component Id="App">
          <File Id="app.exe" Source="app.exe">
            <fw:FirewallException Id="NoProtocol" Name="MsiBuilderCA10WixPort" Port="12346" Scope="any" />
            <fw:FirewallException Id="Ignored" Name="MsiBuilderCA10WixIgnore" Port="12347" Protocol="tcp" IgnoreFailure="yes">
              <fw:RemoteAddress Value="[REMOTE]" />
            </fw:FirewallException>
          </File>
        </Component>
      </Directory>
    </StandardDirectory>
    <Feature Id="Main"><ComponentRef Id="App" /></Feature>
  </Package>
</Wix>
`

// fwRule is one rule as netsh show rule reads it back.
type fwRule struct {
	Protocol, LocalPort, RemoteIP string
}

// fwShow returns the inbound rules named name, sorted by protocol.
func fwShow(t *testing.T, name string) []fwRule {
	t.Helper()

	//nolint:gosec // a test rule name
	out, _ := exec.CommandContext(t.Context(), "netsh", "advfirewall", "firewall", "show", "rule", "name="+name, "dir=in").CombinedOutput()

	var rules []fwRule

	for line := range strings.Lines(string(out)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}

		value = strings.TrimSpace(value)

		switch key {
		case "Rule Name":
			rules = append(rules, fwRule{})
		case "Protocol":
			rules[len(rules)-1].Protocol = value
		case "LocalPort":
			rules[len(rules)-1].LocalPort = value
		case "RemoteIP":
			rules[len(rules)-1].RemoteIP = value
		}
	}

	slices.SortFunc(rules, func(a, b fwRule) int { return strings.Compare(a.Protocol, b.Protocol) })

	return rules
}

// fwDelete deletes the inbound rules named name, which must be one of this
// file's own names.
func fwDelete(name string) {
	if !strings.HasPrefix(name, "MsiBuilderCA10") {
		panic("not a CA-10 rule name: " + name)
	}

	//nolint:gosec // a test rule name
	_ = exec.CommandContext(context.Background(), "netsh", "advfirewall", "firewall", "delete", "rule", "name="+name, "dir=in").Run()
}

// TestNetshAnyProtocolPort asks netsh for a rule with protocol any and a
// local port, which the builder therefore splits into TCP and UDP (CA-10).
//
//nolint:paralleltest // changes the firewall
func TestNetshAnyProtocolPort(t *testing.T) {
	if testing.Short() {
		t.Skip("changes the firewall")
	}

	const name = "MsiBuilderCA10Netsh"

	t.Cleanup(func() { fwDelete(name) })

	out, err := exec.CommandContext(t.Context(), "netsh", "advfirewall", "firewall", "add", "rule", "name="+name, "dir=in",
		"action=allow", "protocol=any", "localport=12345", `program=C:\MsiBuilderCA10\app.exe`).CombinedOutput()
	t.Logf("CA-10 netsh protocol=any localport=12345: err %v\n%s", err, out)
	t.Logf("CA-10 rules afterwards: %+v", fwShow(t, name))

	if err == nil {
		t.Error("netsh accepted protocol=any with a port; the builder need not split the rule")
	}
}

// TestMsiexecFirewallAnyProtocol installs a rule with protocol any and a
// port, which becomes a TCP and a UDP rule of the same name, and removes
// both on uninstall (CA-10).
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecFirewallAnyProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	const name = "MsiBuilderCA10Any"

	dir := t.TempDir()
	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	src := fmt.Sprintf(firewallCA10Config, name, "6c1e9a52-3d7b-4e08-9f26-b4a8d0c5e713", "any", "    port: '12345,12350-12352'\n", "", false)
	path := buildConfig(t, dir, "any", src, map[string][]byte{"app.exe": app})

	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").Run()

		fwDelete(name)
	})

	msiexecQuiet(t, filepath.Join(dir, "install.log"), "/i", path)

	got := fwShow(t, name)
	t.Logf("CA-10 any with a port: %+v", got)

	want := []fwRule{{"TCP", "12345,12350-12352", "Any"}, {"UDP", "12345,12350-12352", "Any"}}
	if !slices.Equal(got, want) {
		t.Errorf("rules = %+v, want %+v", got, want)
	}

	msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)

	if got := fwShow(t, name); len(got) != 0 {
		t.Errorf("uninstall left %+v", got)
	}
}

// TestMsiexecFirewallIgnoreFailure installs a rule whose remote address
// netsh rejects at run time: without ignoreFailure the install fails with
// 1603 and rolls back, with it the install succeeds without the rule and
// uninstalls (CA-10).
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecFirewallIgnoreFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct {
		name, upgradeCode string
		ignore            bool
		want              int
	}{
		{"MsiBuilderCA10Strict", "0f8d3b6a-9e21-4c57-a3d4-6b2e8f1c7a90", false, 1603},
		{"MsiBuilderCA10Ignore", "4a7c2e95-1b3f-4d68-8e0a-5f9b3c6d2e17", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := fmt.Sprintf(firewallCA10Config, tc.name, tc.upgradeCode, "tcp", "    port: '12345'\n", "[REMOTE]", tc.ignore)
			path := buildConfig(t, dir, "ignore", src, map[string][]byte{"app.exe": app})

			t.Cleanup(func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").Run()

				fwDelete(tc.name)
			})

			log := filepath.Join(dir, "install.log")
			code := msiexecCode(t, log, "/i", path, "REMOTE=not-an-address")
			rules := fwShow(t, tc.name)
			installed := present(filepath.Join(os.Getenv("ProgramFiles"), tc.name, "app.exe"))

			t.Logf("CA-10 %s, remote address not-an-address: exit %d, rules %+v, app.exe installed %t", tc.name, code, rules, installed)

			if code != tc.want {
				t.Errorf("exit code %d, want %d\n%s", code, tc.want, readLog(log))
			}

			if len(rules) != 0 {
				t.Errorf("rules %+v after a failed add", rules)
			}

			if installed != (tc.want == 0) {
				t.Errorf("app.exe installed %t", installed)
			}

			if tc.want == 0 {
				msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)
			}
		})
	}
}

// TestMsiexecFirewallWixBlackBox builds a WiX 5 package with a port rule
// without Protocol and a failing rule with IgnoreFailure, installs it and
// reads the rules back (CA-10). WiX runs as a black box; the test skips
// without wix.exe unless MSI_BUILDER_WIX_REQUIRED is set.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecFirewallWixBlackBox(t *testing.T) {
	if testing.Short() {
		t.Skip("installing needs msiexec time and elevation")
	}

	wix, err := exec.LookPath("wix")
	if err != nil {
		if os.Getenv("MSI_BUILDER_WIX_REQUIRED") != "" {
			t.Fatalf("wix is not on PATH: %v", err)
		}

		t.Skip("wix is not on PATH")
	}

	dir := t.TempDir()
	writeText(t, filepath.Join(dir, "app.exe"), string(testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})))
	writeText(t, filepath.Join(dir, "ca10.wxs"), firewallCA10Wix)
	runTool(t, dir, wix, "extension", "add", "-g", "WixToolset.Firewall.wixext/5.0.2")

	path := filepath.Join(dir, "ca10.msi")
	runTool(t, dir, wix, "build", "-arch", "x64", "-ext", "WixToolset.Firewall.wixext", "-o", path, "ca10.wxs")

	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").Run()

		fwDelete("MsiBuilderCA10WixPort")
		fwDelete("MsiBuilderCA10WixIgnore")
	})

	log := filepath.Join(dir, "install.log")
	code := msiexecCode(t, log, "/i", path, "REMOTE=not-an-address")
	port, ignored := fwShow(t, "MsiBuilderCA10WixPort"), fwShow(t, "MsiBuilderCA10WixIgnore")

	t.Logf("CA-10 WiX, remote address not-an-address: exit %d; Port without Protocol: %+v; IgnoreFailure: %+v", code, port, ignored)

	// A powershell view of the same rules, for the protocol numbers.
	out, _ := exec.CommandContext(t.Context(), "powershell", "-NoProfile", "-Command",
		"Get-NetFirewallRule -DisplayName MsiBuilderCA10Wix* | ForEach-Object { $f = $_ | Get-NetFirewallPortFilter; "+
			"'{0} protocol={1} localport={2}' -f $_.DisplayName, $f.Protocol, ($f.LocalPort -join ',') }").CombinedOutput()
	t.Logf("CA-10 WiX rules:\n%s", out)

	// A failure here is an observation, not a test failure: the rule
	// without Protocol has no IgnoreFailure, and what WiX makes of it is
	// the question.
	if code == 0 {
		msiexecQuiet(t, filepath.Join(dir, "uninstall.log"), "/x", path)
	}
}

// TestMsiexecFirewallServiceStopped stops the Windows Defender Firewall
// service and installs a rule with and without ignoreFailure (CA-10). An
// administrator cannot stop the service until its security descriptor
// grants the stop right, so the test changes it and puts it back; it runs
// only when MSI_BUILDER_STOP_FIREWALL is set, on a throwaway machine.
//
//nolint:paralleltest // msiexec runs one installation at a time
func TestMsiexecFirewallServiceStopped(t *testing.T) {
	if os.Getenv("MSI_BUILDER_STOP_FIREWALL") == "" {
		t.Skip("stops the firewall service; set MSI_BUILDER_STOP_FIREWALL on a throwaway machine")
	}

	sd, err := exec.CommandContext(t.Context(), "sc.exe", "sdshow", "mpssvc").Output()
	if err != nil {
		t.Fatal(err)
	}

	original := strings.TrimSpace(string(sd))

	const admins = "(A;;CCLCSWRPLORCWDWO;;;BA)"

	if !strings.Contains(original, admins) {
		t.Fatalf("unexpected MpsSvc security descriptor %s", original)
	}

	// Registered first, so that it runs last: the packages and rules are
	// removed once the service runs again.
	var cleanups []func()

	t.Cleanup(func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	})

	t.Cleanup(func() {
		out, err := exec.CommandContext(context.Background(), "sc.exe", "start", "mpssvc").CombinedOutput()
		t.Logf("sc start mpssvc: %v\n%s", err, out)
		out, err = exec.CommandContext(context.Background(), "sc.exe", "sdset", "mpssvc", original).CombinedOutput()
		t.Logf("sc sdset (restore): %v\n%s", err, out)
	})

	//nolint:gosec // the descriptor read above with the stop right added
	out, err := exec.CommandContext(t.Context(), "sc.exe", "sdset", "mpssvc",
		strings.Replace(original, admins, "(A;;CCLCSWRPWPLORCWDWO;;;BA)", 1)).CombinedOutput()
	if err != nil {
		t.Fatalf("sc sdset: %v\n%s", err, out)
	}

	out, err = exec.CommandContext(t.Context(), "powershell", "-NoProfile", "-Command",
		"Stop-Service mpssvc -Force; (Get-Service mpssvc).Status").CombinedOutput()
	t.Logf("CA-10 stop mpssvc: %v\n%s", err, out)

	if !strings.HasSuffix(strings.TrimSpace(string(out)), "Stopped") {
		// The first run (CI run 41298591380) could not stop it with the
		// stop right granted; the service's accepted controls say why.
		query, _ := exec.CommandContext(t.Context(), "sc.exe", "queryex", "mpssvc").CombinedOutput()
		t.Skipf("CA-10 MpsSvc did not stop:\n%s", query)
	}

	app := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for _, tc := range []struct {
		name, upgradeCode string
		ignore            bool
	}{
		{"MsiBuilderCA10StoppedStrict", "2b9e5d71-6a4c-4f83-9d10-c3e7a5b8f246", false},
		{"MsiBuilderCA10StoppedIgnore", "7e3f1a84-5c2d-4b96-a0e8-1d6b4c9f2a35", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := fmt.Sprintf(firewallCA10Config, tc.name, tc.upgradeCode, "tcp", "    port: '12345'\n", "", tc.ignore)
			path := buildConfig(t, dir, "stopped", src, map[string][]byte{"app.exe": app})

			cleanups = append(cleanups, func() {
				_ = exec.CommandContext(context.Background(), "msiexec", "/x", path, "/qn").Run()

				fwDelete(tc.name)
			})

			log := filepath.Join(dir, "install.log")
			code := msiexecCode(t, log, "/i", path)

			var netsh []string

			for line := range strings.Lines(readLog(log)) {
				if strings.Contains(line, "FwAdd") || strings.Contains(line, "FwClear") {
					netsh = append(netsh, strings.TrimSpace(line))
				}
			}

			t.Logf("CA-10 %s with MpsSvc stopped: exit %d, rules %+v\n%s", tc.name, code, fwShow(t, tc.name), strings.Join(netsh, "\n"))

			if code == 0 {
				code = msiexecCode(t, filepath.Join(dir, "uninstall.log"), "/x", path)
				t.Logf("CA-10 %s uninstall with MpsSvc stopped: exit %d", tc.name, code)
			}
		})
	}
}
