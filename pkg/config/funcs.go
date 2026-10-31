package config

import (
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

// maxIndent caps indent and nindent, so one call cannot allocate a huge
// string. It does not bound a template's memory: a template is code.
const maxIndent = 1024

// maxList caps the lists until, untilStep and seq build.
const maxList = 1 << 20

// unrepeatable are the sprig functions that are left out on top of the ones
// sprig itself calls non-hermetic (clock, randomness, environment,
// network): they give a different result on each run, or depend on the
// machine's time zone, so the same file and variables would build a
// different package.
func unrepeatable() []string {
	return []string{
		"ago", "toDate", "mustToDate", "randInt", "shuffle", "bcrypt", "htpasswd", "encryptAES",
		"genPrivateKey", "genCA", "genCAWithKey", "genSelfSignedCert", "genSelfSignedCertWithKey",
		"genSignedCert", "genSignedCertWithKey",
	}
}

// templateFuncs returns the functions a template can call besides Go's
// built-in ones: sprig's repeatable functions
// (https://masterminds.github.io/sprig/), with caps on the ones that
// allocate by a number, and msi-builder's has, quote and split in place of
// sprig's functions of these names.
func templateFuncs(vars *varResolver) template.FuncMap {
	funcs := sprig.HermeticTxtFuncMap()
	for _, name := range unrepeatable() {
		delete(funcs, name)
	}

	for _, name := range []string{"env", "expandenv"} {
		funcs[name] = func(...string) (string, error) {
			return "", fmt.Errorf("%s: read environment variables as {{ .NAME }} and pass --env", name)
		}
	}

	funcs["has"] = vars.defined
	funcs["quote"] = quote
	funcs["split"] = split
	funcs["indent"] = indentFunc("indent", false)
	funcs["nindent"] = indentFunc("nindent", true)
	funcs["repeat"] = repeat
	capList(funcs)

	return funcs
}

// split returns the parts of list between each sep; an empty list has
// none. Unlike sprig's split, which gives a dict, it gives a list, as
// sprig's splitList does.
func split(sep, list string) ([]string, error) {
	if sep == "" {
		return nil, errors.New("split: the separator is empty")
	}

	if list == "" {
		return nil, nil
	}

	return strings.Split(list, sep), nil
}

// indentFunc returns sprig's indent or nindent with spaces capped.
func indentFunc(name string, newline bool) func(int, string) (string, error) {
	return func(spaces int, text string) (string, error) {
		if spaces < 0 || spaces > maxIndent {
			return "", fmt.Errorf("%s: %d is not between 0 and %d", name, spaces, maxIndent)
		}

		pad := strings.Repeat(" ", spaces)
		out := pad + strings.ReplaceAll(text, "\n", "\n"+pad)

		if newline {
			return "\n" + out, nil
		}

		return out, nil
	}
}

// repeat is sprig's repeat, capped at the size a template may write.
func repeat(count int, text string) (string, error) {
	if count < 0 {
		return "", fmt.Errorf("repeat: %d is negative", count)
	}

	if text != "" && count > maxRendered/len(text) {
		return "", errRenderedTooLarge
	}

	return strings.Repeat(text, count), nil
}

// span returns how many steps of size step lie between start and stop,
// in uint64 so that extreme bounds do not overflow.
func span(start, stop, step int) uint64 {
	diff := uint64(stop) - uint64(start) //nolint:gosec // two's complement difference
	if stop < start {
		diff = uint64(start) - uint64(stop) //nolint:gosec // two's complement difference
	}

	stride := uint64(step) //nolint:gosec // made positive below
	if step < 0 {
		stride = -stride
	}

	if stride == 0 {
		return 0
	}

	return diff / stride
}

// capList wraps sprig's until, untilStep and seq, which allocate a list
// as long as their bounds, so that they refuse more than maxList entries.
func capList(funcs template.FuncMap) {
	until := funcs["until"].(func(int) []int)                   //nolint:forcetypeassert,revive // sprig's signature
	untilStep := funcs["untilStep"].(func(int, int, int) []int) //nolint:forcetypeassert,revive // sprig's signature
	seq := funcs["seq"].(func(...int) string)                   //nolint:forcetypeassert,revive // sprig's signature

	tooLong := func(name string, start, stop, step int) error {
		if span(start, stop, step) >= maxList {
			return fmt.Errorf("%s: more than %d entries", name, maxList)
		}

		return nil
	}

	funcs["until"] = func(count int) ([]int, error) {
		if err := tooLong("until", 0, count, 1); err != nil {
			return nil, err
		}

		return until(count), nil
	}

	funcs["untilStep"] = func(start, stop, step int) ([]int, error) {
		if err := tooLong("untilStep", start, stop, step); err != nil {
			return nil, err
		}

		return untilStep(start, stop, step), nil
	}

	funcs["seq"] = func(params ...int) (string, error) {
		start, step, stop := 1, 1, 0

		switch len(params) {
		case 1:
			stop = params[0]
		case 2: //nolint:mnd // seq FIRST LAST
			start, stop = params[0], params[1]
		case 3: //nolint:mnd // seq FIRST STEP LAST
			start, step, stop = params[0], params[1], params[2]
		}

		if err := tooLong("seq", start, stop, step); err != nil {
			return "", err
		}

		return seq(params...), nil
	}
}
