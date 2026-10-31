package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

// maxIndent caps the spaces of indent and nindent. With repeat and
// indent, whose output is capped at maxRendered too, it keeps the
// functions that allocate by a number from filling the memory in one
// call. Other functions are not bounded: a template is code.
const maxIndent = 1024

// maxList caps the lists until, untilStep and seq build.
const maxList = 1 << 20

// unrepeatable are the sprig functions that are left out on top of the ones
// sprig itself calls non-hermetic (clock, randomness, environment,
// network): they give a different result on each run, or depend on the
// machine's time zone or operating system, so the same file and variables would build a
// different package.
func unrepeatable() []string {
	return []string{
		"ago", "toDate", "mustToDate", "randInt", "shuffle", "bcrypt", "htpasswd", "encryptAES",
		"genPrivateKey", "genCA", "genCAWithKey", "genSelfSignedCert", "genSelfSignedCertWithKey",
		"genSignedCert", "genSignedCertWithKey",
		// path/filepath: the result depends on the operating system.
		"osBase", "osClean", "osDir", "osExt", "osIsAbs",
	}
}

// templateFuncs returns the functions a template can call besides Go's
// built-in ones: sprig's repeatable functions
// (https://masterminds.github.io/sprig/), with caps on the ones that
// allocate by a number, keys and values in sorted order, and msi-builder's
// has, quote and split in place of sprig's functions of these names.
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
	funcs["until"] = until
	funcs["untilStep"] = untilStep
	funcs["seq"] = seq
	funcs["keys"] = keys
	funcs["values"] = values

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

		if size := (strings.Count(text, "\n") + 1) * spaces; size > maxRendered-len(text) {
			return "", errRenderedTooLarge
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

// direction returns the number of entries start, start+step, ... before
// stop (exclusive), or through stop when inclusive, without overflow: 0
// when step is 0 or points away from stop, as in sprig.
func direction(start, stop, step int, inclusive bool) uint64 {
	if step == 0 || (step > 0 && stop < start) || (step < 0 && stop > start) {
		return 0
	}

	// Two's complement differences are exact in uint64.
	diff := uint64(stop) - uint64(start) //nolint:gosec // see above
	stride := uint64(step)               //nolint:gosec // see above

	if step < 0 {
		diff, stride = uint64(start)-uint64(stop), -stride //nolint:gosec // see above
	}

	if inclusive {
		return diff/stride + 1
	}

	return (diff + stride - 1) / stride
}

// steps returns count entries start, start+step, ...; every entry lies
// between start and stop, so the sum does not overflow.
func steps(name string, start, step int, count uint64) ([]int, error) {
	if count > maxList {
		return nil, fmt.Errorf("%s: %d entries are more than %d", name, count, maxList)
	}

	out := make([]int, 0, count)
	for i, value := uint64(0), start; i < count; i++ {
		out = append(out, value)

		if i+1 < count {
			value += step
		}
	}

	return out, nil
}

// until is sprig's until: 0 up to count, or down to it when it is
// negative, without count.
func until(count int) ([]int, error) {
	step := 1
	if count < 0 {
		step = -1
	}

	return steps("until", 0, step, direction(0, count, step, false))
}

// untilStep is sprig's untilStep: start, start+step, ... before stop.
func untilStep(start, stop, step int) ([]int, error) {
	return steps("untilStep", start, step, direction(start, stop, step, false))
}

// seq is sprig's seq, the numbers through the last one joined by spaces:
// seq LAST counts from 1, seq FIRST LAST counts up or down, and
// seq FIRST STEP LAST steps by STEP.
func seq(params ...int) (string, error) {
	start, step, stop, ok := seqArgs(params)
	if !ok {
		return "", nil
	}

	nums, err := steps("seq", start, step, direction(start, stop, step, true))
	if err != nil {
		return "", err
	}

	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}

	return strings.Join(parts, " "), nil
}

// seqArgs returns the first number, the step and the last number of seq,
// or false when seq writes nothing.
func seqArgs(params []int) (int, int, int, bool) {
	switch len(params) {
	case 1:
		return 1, towards(1, params[0]), params[0], true
	case 2: //nolint:mnd // seq FIRST LAST
		return params[0], towards(params[0], params[1]), params[1], true
	case 3: //nolint:mnd // seq FIRST STEP LAST
		// sprig counts up when LAST is not below FIRST.
		return params[0], params[1], params[2], params[2] != params[0] || params[1] > 0
	default:
		return 0, 0, 0, false
	}
}

// towards is the step from first towards last: -1 when last is below it
// and 1 otherwise.
func towards(first, last int) int {
	if last < first {
		return -1
	}

	return 1
}

// keys is sprig's keys, sorted: sprig gives them in map order, which
// changes from run to run.
func keys(dicts ...map[string]any) []string {
	size := 0
	for _, dict := range dicts {
		size += len(dict)
	}

	out := make([]string, 0, size)
	for _, dict := range dicts {
		out = append(out, slices.Collect(maps.Keys(dict))...)
	}

	slices.Sort(out)

	return out
}

// values is sprig's values in the order of the sorted keys.
func values(dict map[string]any) []any {
	out := make([]any, 0, len(dict))
	for _, key := range slices.Sorted(maps.Keys(dict)) {
		out = append(out, dict[key])
	}

	return out
}
