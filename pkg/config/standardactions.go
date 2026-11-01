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
var movableActions = []string{actFindRelatedProducts, actAppSearch, actLaunchConditions} //nolint:gochecknoglobals // lookup table

const (
	actFindRelatedProducts = "FindRelatedProducts"
	actAppSearch           = "AppSearch"
	actLaunchConditions    = "LaunchConditions"
)

// hasLaunchConditions reports whether the package gets LaunchCondition
// rows: its own, or the downgrade check of upgrade.downgrade: error.
func hasLaunchConditions(cfg *Config) bool {
	return len(cfg.LaunchConditions) > 0 || upgrades(cfg) && cfg.Upgrade.Downgrade == downgradeError
}

// idle returns why the package does not run a movable standard action in a
// sequence, or "" when it does: FindRelatedProducts needs the upgrade
// section, AppSearch a property search or a remembered install dir, and
// LaunchConditions a launch condition. An action that a placement in the
// sequence is anchored to runs anyway, as the builder adds it.
func idle(cfg *Config, action, schedule string) string {
	if anchoredIn(cfg, action, schedule) {
		return ""
	}

	switch {
	case action == actFindRelatedProducts && !upgrades(cfg):
		return "it needs the upgrade section"
	case action == actAppSearch && !searches(cfg):
		return "it searches for nothing"
	case action == actLaunchConditions && !hasLaunchConditions(cfg):
		return "it has no launch conditions"
	default:
		return ""
	}
}

// searches reports whether the builder runs AppSearch for the package.
func searches(cfg *Config) bool {
	if cfg.InstallDir.Remember {
		return true
	}

	for _, prop := range cfg.Properties {
		if prop != nil && len(prop.Search) > 0 {
			return true
		}
	}

	return false
}

// anchoredIn reports whether a custom action or another moved standard
// action is placed next to action in a sequence.
func anchoredIn(cfg *Config, action, schedule string) bool {
	next := func(place Placement) bool {
		return place.Sequence == schedule && (place.Before == action || place.After == action)
	}

	for _, custom := range cfg.CustomActions {
		if custom != nil && slices.ContainsFunc(custom.Schedule, next) {
			return true
		}
	}

	for _, moved := range cfg.StandardActions {
		if moved != nil && moved.Action != action && slices.ContainsFunc(moved.Schedule, next) {
			return true
		}
	}

	return false
}

// launchBeforeFind reports launch conditions that a move puts before
// FindRelatedProducts in a numbered sequence, as the build does. When
// LaunchConditions keeps its place (100, or later behind a long chain of
// search actions) and is not numbered here, a FindRelatedProducts at 100
// or later counts as after it.
func (v *validator) launchBeforeFind(cfg *Config, table string, numbered []sequence.Numbered) {
	if !hasLaunchConditions(cfg) {
		return
	}

	// After an unmoved LaunchConditions, whose number validate leaves to
	// the build.
	place, path, moved := cfg.StandardPlacement(table, actFindRelatedProducts)
	if _, _, launchMoved := cfg.StandardPlacement(table, actLaunchConditions); moved && place.After == actLaunchConditions && !launchMoved {
		v.reportLaunchBeforeFind(cfg, path)

		return
	}

	find := sequence.Find(numbered, actFindRelatedProducts)
	if find == 0 {
		return
	}

	launch := sequence.Find(numbered, actLaunchConditions)
	if launch == 0 {
		std, _ := sequence.LookupStandard(actLaunchConditions)
		launch = std.Number(table)

		if find == launch {
			v.findOnLaunch(cfg, table, find)

			return
		}
	}

	path = movePath(cfg, table)
	if launch > find || path == "" {
		return
	}

	v.reportLaunchBeforeFind(cfg, path)
}

// findOnLaunch reports FindRelatedProducts on the number of an unmoved
// LaunchConditions: a collision when AppSearch does not run, otherwise left
// to the build, which may put LaunchConditions behind the search actions.
func (v *validator) findOnLaunch(cfg *Config, table string, find int) {
	if path := movePath(cfg, table); path != "" && idle(cfg, actAppSearch, scheduleName(table)) != "" {
		v.errorf(path, "FindRelatedProducts at %d takes the number of LaunchConditions; place it before or after LaunchConditions", find)
	}
}

// reportLaunchBeforeFind reports launch conditions before
// FindRelatedProducts: an error with the downgrade check of
// upgrade.downgrade: error, a warning otherwise. The numbers are left out,
// because the builder adds actions validate does not number.
func (v *validator) reportLaunchBeforeFind(cfg *Config, path string) {
	// Without the upgrade section FindRelatedProducts, run only as an
	// anchor, sets no properties, so the order does not matter.
	if !upgrades(cfg) {
		return
	}

	if cfg.Upgrade.Downgrade == downgradeError {
		v.errorf(path, "LaunchConditions runs before FindRelatedProducts, so the downgrade check of upgrade.downgrade: "+
			"error never sees a newer version; keep FindRelatedProducts before it or move LaunchConditions too")

		return
	}

	v.warnf(path, "LaunchConditions runs before FindRelatedProducts: a launch condition sees the upgrade properties unset")
}

// scheduleName returns the schedule name of a sequence table.
func scheduleName(table string) string {
	for name, t := range sequenceTables {
		if t == table {
			return name
		}
	}

	return ""
}

// movePath returns the YAML path of the move that puts LaunchConditions
// before FindRelatedProducts: that of LaunchConditions, of
// FindRelatedProducts, or of AppSearch, which LaunchConditions follows.
func movePath(cfg *Config, table string) string {
	for _, name := range []string{actLaunchConditions, actFindRelatedProducts, actAppSearch} {
		if _, path, ok := cfg.StandardPlacement(table, name); ok {
			return path
		}
	}

	return ""
}

// movedNumbers reports the moved standard actions that a numbered sequence
// puts at or after CostInitialize, at their YAML paths.
func (v *validator) movedNumbers(cfg *Config, table string, numbered []sequence.Numbered) {
	cost := sequence.Find(numbered, "CostInitialize")

	for _, name := range movableActions {
		place, path, moved := cfg.StandardPlacement(table, name)
		if n := sequence.Find(numbered, name); moved && cost != 0 && n > cost && !scopePageRefuses(cfg, place) {
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

			if why := idle(cfg, moved.Action, place.Sequence); why != "" {
				v.errorf(placePath, "%s does not run in this package (%s), so there is nothing to move", moved.Action, why)
			} else {
				v.scopePageMove(cfg, placePath, moved.Action, place)
			}
		}
	}
}

// scopePageMove refuses a ui move of FindRelatedProducts, AppSearch or
// LaunchConditions in a dual-scope package with the wizard: the scope page
// runs after AppSearch, and FindRelatedProducts and LaunchConditions after
// it, where they see the chosen context (UI-11).
func (v *validator) scopePageMove(cfg *Config, placePath, action string, place Placement) {
	if scopePageRefuses(cfg, place) {
		v.errorf(placePath+".sequence", "the scope page of a dual-scope wizard orders %s of the ui sequence: "+
			"AppSearch, the page, FindRelatedProducts, LaunchConditions; move it in the execute sequence only", action)
	}
}

// scopePageRefuses reports whether the scope page refuses a placement of a
// moved standard action: any ui move.
func scopePageRefuses(cfg *Config, place Placement) bool {
	return place.Sequence == "ui" && scopePage(cfg)
}

// scopePage reports whether the package has the wizard's scope page.
func scopePage(cfg *Config) bool {
	return cfg.Package.Scope.Dual() && cfg.UI.Enabled
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
		case anchor == actLaunchConditions && searchBesideLaunch(cfg, action, place.Sequence):
			v.errorf(placePath, "AppSearch cannot be placed next to LaunchConditions, which follows a moved AppSearch; move LaunchConditions instead")
		case standardActions[anchor] && !earlyAnchor(anchor, place.Sequence, i == 0):
			v.errorf(placePath, "%s must run before CostInitialize, which it does not %s %s", action, []string{"before", "after"}[i], anchor)
		}
	}
}

// searchBesideLaunch reports whether an action is an AppSearch that cannot
// be placed next to LaunchConditions in a sequence: the builder puts an
// unmoved LaunchConditions after a moved AppSearch when the package has
// launch conditions.
func searchBesideLaunch(cfg *Config, action, schedule string) bool {
	return action == actAppSearch && hasLaunchConditions(cfg) && !launchMovedIn(cfg, schedule)
}

// launchMovedIn reports whether standardActions moves LaunchConditions in
// a sequence.
func launchMovedIn(cfg *Config, schedule string) bool {
	_, _, moved := cfg.StandardPlacement(sequenceTables[schedule], actLaunchConditions)

	return moved
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
