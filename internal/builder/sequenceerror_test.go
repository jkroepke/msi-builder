package builder_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// A numbering error is a diagnostic at the placement it concerns, also when
// only the build knows the number it runs into: InstallFiles comes with the
// File table, which validate does not decide.
func TestSequenceErrorDiagnostic(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})

	for name, tc := range map[string]struct {
		first, late config.Placement
		path, want  string
	}{
		"collision found by the build": {
			config.Placement{Sequence: "execute", After: "CostFinalize"}, config.Placement{Sequence: "execute", At: 4000},
			"customActions[1].schedule[0]", "InstallExecuteSequence: actions Late and InstallFiles both have number 4000",
		},
		"cycle found by validate": {
			config.Placement{Sequence: "execute", Before: "Late"}, config.Placement{Sequence: "execute", Before: "First"},
			"customActions[0].schedule[0]", "First → Late → First",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
			cfg.CustomActions = append(cfg.CustomActions,
				&config.CustomAction{ID: "First", SetProperty: &config.SetProperty{Name: "FIRST", Value: "1"}, Schedule: []config.Placement{tc.first}},
				&config.CustomAction{ID: "Late", SetProperty: &config.SetProperty{Name: "LATE", Value: "1"}, Schedule: []config.Placement{tc.late}},
			)

			res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
			if err == nil {
				t.Fatal("built")
			}

			for _, d := range res.Diagnostics {
				if d.Severity == config.SeverityError && d.Path == tc.path && strings.Contains(d.Message, tc.want) {
					return
				}
			}

			t.Errorf("no error at %s with %q: %v", tc.path, tc.want, res.Diagnostics)
		})
	}
}

// An action the builder adds and runs into a standard action blames the
// first user placement in its chain: 85 actions after InstallFiles plus the
// firewall actions there reach PatchFiles (4090), which one more action is
// anchored to.
func TestSequenceErrorGeneratedAction(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
	cfg.Features[0].Firewall = []*config.FirewallRule{{Name: "fw", Program: "[#hello.exe]", Protocol: "tcp", Port: "80"}}

	for i := range 85 {
		cfg.CustomActions = append(cfg.CustomActions, &config.CustomAction{
			ID: fmt.Sprintf("A%d", i), SetProperty: &config.SetProperty{Name: fmt.Sprintf("PA%d", i), Value: "x"},
			Schedule: []config.Placement{{Sequence: "execute", After: "InstallFiles"}},
		})
	}

	cfg.CustomActions = append(cfg.CustomActions, &config.CustomAction{
		ID: "Patch", SetProperty: &config.SetProperty{Name: "PATCH", Value: "x"},
		Schedule: []config.Placement{{Sequence: "execute", After: "PatchFiles"}},
	})

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err == nil {
		t.Fatal("built")
	}

	for _, d := range res.Diagnostics {
		if d.Severity == config.SeverityError && d.Path == "customActions[0].schedule[0]" && strings.Contains(d.Message, "PatchFiles already has") {
			return
		}
	}

	t.Errorf("no error at the chain's first placement: %v", res.Diagnostics)
}

// When both colliding actions are the builder's, the error goes to the
// user placement in either chain: the close action before InstallValidate
// runs into the firewall address setters after CostFinalize, behind 397
// user actions. An action before CostFinalize, on the other side of the
// anchor, is not blamed.
func TestSequenceErrorBuilderActions(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
	cfg.Upgrade.Disabled = true
	cfg.Components[0].Files[0].Close = &config.Close{}
	cfg.Features[0].Firewall = []*config.FirewallRule{{Name: "fw", Program: "[#hello.exe]", Protocol: "tcp", Port: "80", RemoteAddresses: "[FWIP]"}}

	cfg.CustomActions = append(cfg.CustomActions, &config.CustomAction{
		ID: "Early", SetProperty: &config.SetProperty{Name: "EARLY", Value: "x"},
		Schedule: []config.Placement{{Sequence: "execute", Before: "CostFinalize"}},
	})

	for i := range 397 {
		cfg.CustomActions = append(cfg.CustomActions, &config.CustomAction{
			ID: fmt.Sprintf("A%d", i), SetProperty: &config.SetProperty{Name: fmt.Sprintf("PA%d", i), Value: "x"},
			Schedule: []config.Placement{{Sequence: "execute", After: "CostFinalize"}},
		})
	}

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err == nil {
		t.Fatal("built")
	}

	for _, d := range res.Diagnostics {
		if d.Severity == config.SeverityError && d.Path == "customActions[1].schedule[0]" && strings.Contains(d.Message, "overlap") {
			return
		}
	}

	t.Errorf("no error at the chain's first placement: %v", res.Diagnostics)
}
