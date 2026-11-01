package wixconvert

import (
	"strings"
	"testing"
)

func TestPPCondition(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		loops []string
		want  string
	}{
		`$(var.Arch) = arm64`:                             {want: `eq .Arch "arm64"`},
		`$(Arch) != "x64"`:                                {want: `ne .Arch "x64"`},
		`$(env.ARCH) ~= X64`:                              {want: `eq (lower .ARCH) (lower "X64")`},
		`$(var.Level) >= 2`:                               {want: `ge (atoi .Level) 2`},
		`$(var.A) < $(var.B)`:                             {want: `lt .A .B`},
		`Not $(var.A) = 1 And ($(var.B) = 2 Or $(var.C))`: {want: `and (not (eq .A "1")) (or (eq .B "2") .C)`},
		`$(var.Lang) = de and $(var.Debug)`:               {loops: []string{"Lang"}, want: `and (eq $Lang "de") $.Debug`},
		`$(var.Build.Type) = "release build"`:             {want: `eq .Build_Type "release build"`},
	}

	for text, tc := range tests {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			conv := &converter{ppUsed: make(map[string]bool)}

			got, err := conv.ppCondition(text, tc.loops)
			if err != nil {
				t.Fatal(err)
			}

			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}

	for _, text := range []string{`$(sys.BUILDARCH) = x64`, `x64`, `$(var.A) =`, `($(var.A) = 1`, `$(var.A) = 1 1`, `$(var.A) ! 1`, `"open`} {
		if got, err := (&converter{ppUsed: make(map[string]bool)}).ppCondition(text, nil); err == nil {
			t.Errorf("%s: got %s, want an error", text, got)
		}
	}
}

func TestApplyBlocks(t *testing.T) {
	t.Parallel()

	first := openRef{Action: "if A", Block: 0}
	second := openRef{Action: "if not A", Else: "else", Block: 0, Branch: 1}
	inner := openRef{Action: "if B", Block: 1}

	marker := func(indent string, opens ...openRef) string {
		return indent + strings.ReplaceAll(markerText(opens), "\n", "\n"+indent) + "\n"
	}

	text := "list:\n" +
		marker("  ", first) + "  - one\n" +
		marker("  ", first, inner) + "  # note\n  - two:\n      x: 1\n" +
		marker("  ", second) + "  - three\n" +
		"  - four\n" +
		"next: 1\n" +
		marker("  ", inner) + "  - k\n"

	// "next" starts no region; the last region closes at the end.
	want := "list:\n" +
		"  {{ if A }}\n  - one\n" +
		"  {{ if B }}\n  # note\n  - two:\n      x: 1\n  {{ end }}\n" +
		"  {{ else }}\n  - three\n  {{ end }}\n" +
		"  - four\n" +
		"next: 1\n" +
		"  {{ if B }}\n  - k\n  {{ end }}\n"

	if got := applyBlocks(text); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
