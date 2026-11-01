package config

import (
	"maps"
	"slices"
)

// TemplateFuncNames returns the sorted names of the template functions.
func TemplateFuncNames() []string {
	return slices.Sorted(maps.Keys(templateFuncs(&varResolver{})))
}

// SprigUntil is until, for comparing it with sprig's.
func SprigUntil(count int) ([]int, error) { return until(count) }

// SprigUntilStep is untilStep, for comparing it with sprig's.
func SprigUntilStep(start, stop, step int) ([]int, error) { return untilStep(start, stop, step) }

// SprigSeq is seq, for comparing it with sprig's.
func SprigSeq(params ...int) (string, error) { return seq(params...) }

// BuilderPropertyNames returns the sorted properties the builder or the
// installer sets, which properties cannot define.
func BuilderPropertyNames() []string { return slices.Sorted(maps.Keys(builderProperties)) }
