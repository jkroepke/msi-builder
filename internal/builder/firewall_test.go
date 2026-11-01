package builder_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestFirewallAnyProtocolPort adds a rule with protocol any and a port once
// for TCP and once for UDP under its name, each add with its own rollback,
// and the removal rollback adds both back (CA-10). ignoreFailure marks only
// the adds Continue; a rule without it keeps the failure.
func TestFirewallAnyProtocolPort(t *testing.T) {
	t.Parallel()

	cfg := shortcutConfig(t, `
  - id: App
    files:
      - source: app.exe
firewall:
  - { name: Both, component: App, program: '[#app.exe]', protocol: any, port: '8080', ignoreFailure: true }
  - { name: Strict, component: App, program: '[#app.exe]', protocol: udp, port: '53' }
  - { name: Program, component: App, program: '[#app.exe]', protocol: any }
`)
	database := mustBuild(t, cfg).Database

	type action struct {
		typ    int
		target string
	}

	actions := make(map[string]action)

	for _, row := range rows(t, database, "CustomAction") {
		name := fmt.Sprint(row[0])
		if strings.HasPrefix(name, "Fw") {
			typ, ok := row[1].(int)
			if !ok {
				t.Fatalf("type of %s: %v", name, row[1])
			}

			actions[name] = action{typ: typ, target: fmt.Sprint(row[3])}
		}
	}

	adds := map[string][]action{}
	removeRollbacks := map[string]int{}

	for name, act := range actions {
		rule := ""

		for _, n := range []string{"Both", "Strict", "Program"} {
			if strings.Contains(act.target, `name="`+n+`"`) {
				rule = n
			}
		}

		switch {
		case strings.HasPrefix(name, "FwAddRollback"), strings.HasPrefix(name, "FwClear"), strings.HasPrefix(name, "FwRemove") &&
			!strings.HasPrefix(name, "FwRemoveRollback"):
		case strings.HasPrefix(name, "FwAdd"):
			adds[rule] = append(adds[rule], act)
		case strings.HasPrefix(name, "FwRemoveRollback"):
			removeRollbacks[rule]++
		}
	}

	if got := len(adds["Both"]); got != 2 {
		t.Fatalf("rule Both has %d adds, want 2 (tcp and udp): %v", got, adds["Both"])
	}

	protocols := map[string]bool{}

	for _, add := range adds["Both"] {
		for _, p := range []string{"tcp", "udp"} {
			if strings.Contains(add.target, " protocol="+p+" localport=8080 ") {
				protocols[p] = true
			}
		}

		if add.typ&64 == 0 {
			t.Errorf("an add of an ignoreFailure rule is not Continue: type %d", add.typ)
		}
	}

	if !protocols["tcp"] || !protocols["udp"] {
		t.Errorf("rule Both adds %v, want tcp and udp with the port", adds["Both"])
	}

	if removeRollbacks["Both"] != 2 {
		t.Errorf("rule Both has %d removal rollbacks, want 2", removeRollbacks["Both"])
	}

	for _, rule := range []string{"Strict", "Program"} {
		if len(adds[rule]) != 1 {
			t.Fatalf("rule %s has %d adds, want 1", rule, len(adds[rule]))
		}

		if adds[rule][0].typ&64 != 0 {
			t.Errorf("the add of rule %s is Continue without ignoreFailure: type %d", rule, adds[rule][0].typ)
		}
	}

	if !strings.Contains(adds["Program"][0].target, " protocol=any ") || strings.Contains(adds["Program"][0].target, "localport") {
		t.Errorf("rule Program without a port: %s", adds["Program"][0].target)
	}
}
