package condition //nolint:testpackage // the tests reach unexported encoders

import (
	"slices"
	"testing"
)

func TestValid(t *testing.T) {
	t.Parallel()

	for _, cond := range []string{
		"",
		"  ",
		"Installed",
		"NOT Installed",
		"Installed OR VersionNT64",
		"Installed AND (NOT REMOVE) AND (NOT UPGRADINGPRODUCTCODE)",
		`EXTRA_FLAGS AND (EXTRA_FLAGS >< "--config.file")`,
		`CONFIG_FILE AND CONFIG_FILE<>"config.yaml"`,
		`LISTEN_ADDR<>"" OR LISTEN_PORT<>9182`,
		"VersionNT>=600",
		"VersionNT >= 600 and not Installed",
		`REMOVE="ALL"`,
		`REMOVE~="all"`,
		"&FirewallException=3 AND NOT !FirewallException=3",
		"$Executable=3",
		"?Executable=2",
		"%PROCESSOR_ARCHITECTURE=\"AMD64\"",
		"1",
		"-1",
		"\"0\"",
		"a IMP b EQV c XOR d OR e AND f",
		"((a))",
		"a.b = 1.1",
		"a >> \"x\" AND b << \"y\"",
		"NOT NOT a",
	} {
		if err := Check(cond); err != nil {
			t.Errorf("%q: %v", cond, err)
		}
	}
}

func TestInvalid(t *testing.T) {
	t.Parallel()

	for _, cond := range []string{
		"0 >=",
		"~not",
		"0 < > 0",
		"==",
		"IMP",
		"!=",
		"(a",
		"a)",
		`"unterminated`,
		"a AND",
		"AND a",
		"a b",
		"a = = b",
		"%",
		"a ~ b",
		"-",
		"a ; b",
	} {
		if err := Check(cond); err == nil {
			t.Errorf("%q accepted", cond)
		}
	}
}

func TestProperties(t *testing.T) {
	t.Parallel()

	got := Properties(`Installed OR (NOT SILENT AND &F=3 AND %ENV="x" AND LISTEN_PORT<>9182)`)
	want := []string{"Installed", "SILENT", "LISTEN_PORT"}

	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%v", got)
		}
	}
}

func TestActionStates(t *testing.T) {
	t.Parallel()

	got := ActionStates(`&Main.Sub=3 AND !Main<>3 OR $Comp=2 AND ?Comp=3 AND %PATH AND START="&Quoted"`)
	if want := []string{"&Main.Sub", "$Comp"}; !slices.Equal(got, want) {
		t.Errorf("ActionStates = %v, want %v", got, want)
	}
}

func TestStates(t *testing.T) {
	t.Parallel()

	got := States(`&Main.Sub=3 AND !Main<>3 OR $Comp=2 AND ?Comp=3 AND %PATH AND START="!Quoted"`)
	if want := []string{"&Main.Sub", "!Main", "$Comp", "?Comp"}; !slices.Equal(got, want) {
		t.Errorf("States = %v, want %v", got, want)
	}
}

func TestRenameProperties(t *testing.T) {
	t.Parallel()

	rename := func(name string) (string, bool) {
		if name == "VOLROOT" {
			return "WindowsVolume", true
		}

		return "", false
	}

	for cond, want := range map[string]string{
		"VOLROOT":                               "WindowsVolume",
		`VOLROOT AND VOLROOT<>"VOLROOT"`:        `WindowsVolume AND WindowsVolume<>"VOLROOT"`,
		"NOT VOLROOT OR $VOLROOT=3 OR %VOLROOT": "NOT WindowsVolume OR $VOLROOT=3 OR %VOLROOT",
		"(VOLROOT)AND VOLROOTX AND X.VOLROOT":   "(WindowsVolume)AND VOLROOTX AND X.VOLROOT",
		`VOLROOT AND "unterminated VOLROOT`:     `WindowsVolume AND "unterminated WindowsVolume`,
		"@ VOLROOT":                             "@ WindowsVolume",
		"":                                      "",
	} {
		if got := RenameProperties(cond, rename); got != want {
			t.Errorf("RenameProperties(%q) = %q, want %q", cond, got, want)
		}
	}
}
