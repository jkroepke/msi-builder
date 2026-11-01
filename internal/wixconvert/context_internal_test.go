package wixconvert

import "testing"

// TestContextCondition checks the component conditions that limit a
// component to one install context, alone, in parentheses, negated and
// joined, and that mixed or other conditions limit nothing.
func TestContextCondition(t *testing.T) {
	t.Parallel()

	for cond, want := range map[string]string{
		"ALLUSERS = 1": scopeMachine,
		"(ALLUSERS=1)": scopeMachine,
		"ALLUSERS <> 1 AND MSIINSTALLPERUSER = 1":             scopeUser,
		"ALLUSERS<>1 and MSIINSTALLPERUSER=1":                 scopeUser,
		"NOT (ALLUSERS <> 1 AND MSIINSTALLPERUSER = 1)":       scopeMachine,
		"not(ALLUSERS <> 1 AND MSIINSTALLPERUSER = 1)":        scopeMachine,
		"(NOT ALLUSERS) OR (MSIINSTALLPERUSER = 1)":           scopeUser,
		"NOT ((ALLUSERS <> 1) AND (MSIINSTALLPERUSER = 1))":   scopeMachine,
		"ALLUSERS = 1 AND NOT MSIINSTALLPERUSER":              scopeMachine,
		"ALLUSERS = 1 AND MSIINSTALLPERUSER = 1":              "",
		"ALLUSERS = 1 AND VersionNT64":                        "",
		"NOT (ALLUSERS = 1) AND VersionNT64":                  "",
		"(ALLUSERS = 1) AND (MSIINSTALLPERUSER = 1) OR OTHER": "",
		`ALLUSERS = "1" AND MSIINSTALLPERUSER <> "1"`:         "",
		"NOT (ALLUSERS = 1) OR (ALLUSERS = 1)":                "",
	} {
		if got := contextCondition(cond); got != want {
			t.Errorf("contextCondition(%q) = %q, want %q", cond, got, want)
		}
	}
}
