package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/sequence"
)

// StandardAction moves one of the standard actions that run before costing
// (FindRelatedProducts, AppSearch, LaunchConditions) away from its default
// number, for example FindRelatedProducts behind an action that sets
// ALLUSERS, so that it looks for older versions in the context the install
// runs in (specification section 9.2).
type StandardAction struct {
	Action   string      `yaml:"action"`
	Schedule []Placement `yaml:"schedule"`
}

// movableActions are the standard actions that standardActions may move.
// They only read the system and set properties, and each must still run
// before CostInitialize.
var movableActions = []string{"FindRelatedProducts", actAppSearch, actLaunchConditions} //nolint:gochecknoglobals // lookup table

const (
	actAppSearch        = "AppSearch"
	actLaunchConditions = "LaunchConditions"
)

// hasLaunchConditions reports whether the package gets LaunchCondition
// rows: its own, or the downgrade check of upgrade.downgrade: error.
func hasLaunchConditions(cfg *Config) bool {
	return len(cfg.LaunchConditions) > 0 || upgrades(cfg) && cfg.Upgrade.Downgrade == downgradeError
}

// movedNumbers reports the moved standard actions that a numbered sequence
// puts at or after CostInitialize, at their YAML paths.
func (v *validator) movedNumbers(cfg *Config, table string, numbered []sequence.Numbered) {
	cost := sequence.Find(numbered, "CostInitialize")

	for _, name := range movableActions {
		_, path, moved := cfg.StandardPlacement(table, name)
		if n := sequence.Find(numbered, name); moved && cost != 0 && n > cost {
			v.errorf(path, "%s at %d must run before CostInitialize (%d)", name, n, cost)
		}
	}
}

// StandardPlacement returns the placement that standardActions gives a
// standard action in a sequence table, and its YAML path; ok is false when
// the action keeps its default number there.
func (c *Config) StandardPlacement(table, action string) (Placement, string, bool) {
	for i, moved := range c.StandardActions {
		if moved == nil || moved.Action != action {
			continue
		}

		for j, place := range moved.Schedule {
			if t, ok := sequenceTables[place.Sequence]; ok && t == table {
				return place, fmt.Sprintf("standardActions[%d].schedule[%d]", i, j), true
			}
		}
	}

	return Placement{}, "", false
}

// standardActions checks the standardActions section.
func (v *validator) standardActions(cfg *Config) {
	seen := make(map[string]string)

	for i, moved := range cfg.StandardActions {
		actionPath := fmt.Sprintf("standardActions[%d]", i)

		switch {
		case moved.Action == "":
			v.errorf(actionPath+".action", "required")
		case !slices.Contains(movableActions, moved.Action):
			v.errorf(actionPath+".action", "%q is not one of %s: only the standard actions that run before costing can be moved",
				moved.Action, strings.Join(movableActions, ", "))
		case seen[moved.Action] != "":
			v.errorf(actionPath+".action", "%s is already moved at %s", moved.Action, seen[moved.Action])
		default:
			seen[moved.Action] = actionPath
		}

		if len(moved.Schedule) == 0 {
			v.errorf(actionPath+".schedule", "required")
		}

		sequences := make(map[string]bool)

		for j, place := range moved.Schedule {
			placePath := fmt.Sprintf("%s.schedule[%d]", actionPath, j)

			if sequences[place.Sequence] {
				v.errorf(placePath+".sequence", "%s is already moved in the %s sequence", moved.Action, place.Sequence)
			}

			sequences[place.Sequence] = true

			v.standardPlacement(cfg, placePath, moved.Action, place)
		}
	}
}

// standardPlacement checks one placement of a moved standard action.
func (v *validator) standardPlacement(cfg *Config, placePath, action string, place Placement) {
	if place.Sequence != sequenceExecute && place.Sequence != "ui" {
		v.errorf(placePath+".sequence", "%q is not one of execute, ui", place.Sequence)
	}

	// Conditions on standard actions are not offered (specification
	// section 9.2): the actions msi-builder adds already carry the
	// conditions they need, and a component or feature condition covers
	// what packages condition a standard action for.
	if place.Condition != "" {
		v.errorf(placePath+".condition", "a moved standard action keeps its own condition")
	}

	if place.When != "" {
		v.errorf(placePath+".when", "a moved standard action keeps its own condition")
	}

	v.placementPosition(placePath, place)

	if place.At < 0 {
		v.errorf(placePath+".at", "a moved standard action cannot be a termination action")

		return
	}

	if limit := costInitialize(); place.At >= limit {
		v.errorf(placePath+".at", "%s must run before CostInitialize (%d)", action, limit)
	}

	v.standardAnchors(cfg, placePath, action, place)
}

// standardAnchors checks the anchor of a moved standard action.
func (v *validator) standardAnchors(cfg *Config, placePath, action string, place Placement) {
	for i, anchor := range []string{place.Before, place.After} {
		switch {
		case anchor == "":
		case anchor == action:
			v.errorf(placePath, "%s cannot be placed next to itself", action)
		case !knownAnchor(cfg, anchor, place.Sequence):
			v.errorf(placePath, "anchor %q is neither a standard action nor a custom action", anchor)
		case standardActions[anchor] && !earlyAnchor(anchor, place.Sequence, i == 0):
			v.errorf(placePath, "%s must run before CostInitialize, which it does not %s %s", action, []string{"before", "after"}[i], anchor)
		}
	}
}

// costInitialize is the default number of CostInitialize, the same in both
// install sequences.
func costInitialize() int {
	std, _ := sequence.LookupStandard("CostInitialize")

	return std.Number(sequence.InstallExecute)
}

// earlyAnchor reports whether an action placed before (or after) a
// standard action runs before CostInitialize in a sequence.
func earlyAnchor(anchor, schedule string, before bool) bool {
	if slices.Contains(movableActions, anchor) {
		return true
	}

	std, ok := sequence.LookupStandard(anchor)
	if !ok {
		return false
	}

	n := std.Number(sequenceTables[schedule])

	return n != 0 && (n < costInitialize() || before && n == costInitialize())
}
