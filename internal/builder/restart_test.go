package builder_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/internal/builder"
	"github.com/jkroepke/msi-builder/internal/testpe"
	"github.com/jkroepke/msi-builder/pkg/config"
)

func TestRestartResources(t *testing.T) {
	t.Parallel()

	for _, withClose := range []bool{false, true} {
		t.Run(fmt.Sprint("close=", withClose), func(t *testing.T) {
			t.Parallel()

			exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
			cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})

			comp := cfg.Components[0]
			if withClose {
				comp.Files[0].Close = &config.Close{}
			}

			comp.RestartResources = []*config.RestartResource{
				{File: `[CommonAppDataFolder]Sample\state.db`},
				{Program: `[SystemFolder]sample.exe`},
				{Service: "SampleSvc"},
			}

			database := mustBuild(t, cfg).Database

			cond := "$" + comp.ID + "=2 OR $" + comp.ID + "=3\x1f"
			want := strings.Join([]string{
				cond + `f` + "\x1f" + `[CommonAppDataFolder]Sample\state.db`,
				cond + `p` + "\x1f" + `[SystemFolder]sample.exe`,
				cond + `s` + "\x1f" + `SampleSvc`,
			}, "\n")

			if got := cell(t, database, "Property", "Value", func(r []any) bool { return r[0] == "BuilderRestartResources" }); got != want {
				t.Errorf("BuilderRestartResources\n%q\nwant\n%q", got, want)
			}

			r := rowWhere(t, database, "CustomAction", func(r []any) bool { return r[0] == "BuilderRestartRegister" })
			if fmt.Sprint(r[1]) != strconv.Itoa(1) || r[2] != "BuilderRun" || r[3] != "BuilderRestartRegister" {
				t.Errorf("BuilderRestartRegister row %v", r)
			}

			seq := make(map[string]int)

			for _, row := range rows(t, database, "InstallExecuteSequence") {
				if number, ok := row[2].(int); ok {
					seq[fmt.Sprint(row[0])] = number
				}
			}

			register := seq["BuilderRestartRegister"]
			if register <= seq["CostFinalize"] || register >= seq["InstallValidate"] {
				t.Errorf("BuilderRestartRegister at %d is not between CostFinalize and InstallValidate: %v", register, seq)
			}

			if withClose && register <= seq["BuilderCloseSchedule"] {
				t.Errorf("BuilderRestartRegister at %d runs before BuilderCloseSchedule at %d", register, seq["BuilderCloseSchedule"])
			}
		})
	}
}

func TestRestartResourcesReservedProperty(t *testing.T) {
	t.Parallel()

	exe := testpe.Build(testpe.Options{FileVersion: "1.0.0.0", Languages: []int{1033}})
	cfg := example(t, "minimal", nil, map[string][]byte{"hello.exe": exe})
	cfg.Components[0].RestartResources = []*config.RestartResource{{Service: "SampleSvc"}}
	cfg.Properties = config.Properties{}
	cfg.Properties["BuilderRestartResources"] = &config.Property{Value: "x"}

	res, err := builder.Build(cfg, builder.Options{BuildTime: buildTime})
	if err == nil || !strings.Contains(fmt.Sprint(res.Diagnostics), "reserved for restartResources") {
		t.Errorf("err %v, diagnostics %s", err, res.Diagnostics)
	}
}
