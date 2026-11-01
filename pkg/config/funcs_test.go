package config_test

import (
	"fmt"
	"testing"

	"github.com/Masterminds/sprig/v3"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestListsMatchSprig checks until, untilStep and seq against sprig's own
// on small bounds, where sprig's loops cannot overflow.
func TestListsMatchSprig(t *testing.T) {
	t.Parallel()

	orig := sprig.TxtFuncMap()
	sprigUntil := orig["until"].(func(int) []int)                   //nolint:forcetypeassert,revive // sprig's signature
	sprigUntilStep := orig["untilStep"].(func(int, int, int) []int) //nolint:forcetypeassert,revive // sprig's signature
	sprigSeq := orig["seq"].(func(...int) string)                   //nolint:forcetypeassert,revive // sprig's signature

	check := func(call string, got any, err error, want any) {
		t.Helper()

		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, %v, want %v", call, got, err, want)
		}
	}

	for first := -6; first <= 6; first++ {
		got, err := config.SprigUntil(first)
		check(fmt.Sprint("until ", first), got, err, sprigUntil(first))

		line, err := config.SprigSeq(first)
		check(fmt.Sprint("seq ", first), line, err, sprigSeq(first))

		for last := -6; last <= 6; last++ {
			line, err := config.SprigSeq(first, last)
			check(fmt.Sprint("seq ", first, last), line, err, sprigSeq(first, last))

			for step := -4; step <= 4; step++ {
				got, err := config.SprigUntilStep(first, last, step)
				check(fmt.Sprint("untilStep ", first, last, step), got, err, sprigUntilStep(first, last, step))

				line, err := config.SprigSeq(first, step, last)
				check(fmt.Sprint("seq ", first, step, last), line, err, sprigSeq(first, step, last))
			}
		}
	}
}
