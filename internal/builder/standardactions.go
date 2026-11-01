package builder

import (
	"fmt"
	"slices"

	"github.com/jkroepke/msi-builder/internal/sequence"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// movedLast puts the standard actions that standardActions moves behind
// every action scheduled so far, so that one placed after an anchor follows
// the actions the builder and the custom actions already put there (after
// AppSearch: the actions that apply the search results), and one placed
// before an anchor directly precedes it. The builder places LaunchConditions
// after AppSearch later, so it still follows a FindRelatedProducts moved
// behind AppSearch.
func (b *build) movedLast() {
	for _, table := range sequenceTables {
		schedule := b.schedules[table]

		var moved []sequence.Action

		schedule = slices.DeleteFunc(schedule, func(a sequence.Action) bool {
			if _, _, ok := b.cfg.StandardPlacement(table, a.Name); ok {
				moved = append(moved, a)

				return true
			}

			return false
		})

		b.schedules[table] = append(schedule, moved...)
	}
}

// checkMoved checks the numbered sequences against standardActions: a
// moved action must be one the package runs, it must still run before
// CostInitialize, and FindRelatedProducts must run before the launch
// conditions, which may test the properties it sets.
func (b *build) checkMoved() {
	for actionIndex, moved := range b.cfg.StandardActions {
		for placeIndex, place := range moved.Schedule {
			table, ok := config.SequenceTable(place.Sequence)
			if !ok {
				continue
			}

			path := fmt.Sprintf("standardActions[%d].schedule[%d]", actionIndex, placeIndex)
			seq := b.numbered[table]
			number := sequence.Find(seq, moved.Action)

			if number == 0 {
				b.errorf(path, "%s: %s does not run in this package (%s), so there is nothing to move", table, moved.Action, notNeeded(moved.Action))

				continue
			}

			if cost := sequence.Find(seq, actCostInitialize); cost != 0 && number > cost {
				b.errorf(path, "%s: %s at %d must run before CostInitialize (%d)", table, moved.Action, number, cost)
			}
		}
	}

	b.checkLaunchAfterFind()
}

// checkLaunchAfterFind reports launch conditions that run before
// FindRelatedProducts because one of them was moved: a launch condition
// that tests an upgrade property sees it unset. The downgrade check of
// upgrade.downgrade: error is such a condition, so it is an error there.
func (b *build) checkLaunchAfterFind() {
	// Without the upgrade section FindRelatedProducts, run only as an
	// anchor, sets no properties.
	if !b.hasRows("LaunchCondition") || b.cfg.Upgrade == nil || b.cfg.Upgrade.Disabled {
		return
	}

	for _, table := range []string{seqInstallExecute, seqInstallUI} {
		find := sequence.Find(b.numbered[table], actFindRelatedProducts)
		launch := sequence.Find(b.numbered[table], actLaunchConditions)

		if find == 0 || launch == 0 || launch > find {
			continue
		}

		path := b.movePath(table)
		if path == "" {
			continue
		}

		if b.cfg.Upgrade.Downgrade == "error" {
			b.errorf(path, "%s: LaunchConditions (%d) runs before FindRelatedProducts (%d), "+
				"so the downgrade check of upgrade.downgrade: error never sees a newer version", table, launch, find)

			continue
		}

		b.warnf(path, "%s: LaunchConditions (%d) runs before FindRelatedProducts (%d): a launch condition sees the upgrade properties unset",
			table, launch, find)
	}
}

// movePath returns the YAML path of the move that put LaunchConditions
// before FindRelatedProducts in a table: that of LaunchConditions, of
// FindRelatedProducts, or of AppSearch, which LaunchConditions follows.
func (b *build) movePath(table string) string {
	for _, name := range []string{actLaunchConditions, actFindRelatedProducts, actAppSearch} {
		if _, path, ok := b.cfg.StandardPlacement(table, name); ok {
			return path
		}
	}

	return ""
}

// notNeeded says why a movable standard action is missing from a package.
func notNeeded(action string) string {
	switch action {
	case actFindRelatedProducts:
		return "it needs the upgrade section"
	case actAppSearch:
		return "it searches for nothing"
	default:
		return "it has no launch conditions"
	}
}

// launchConditionsAction schedules LaunchConditions in both install
// sequences when the package has launch conditions: at 100, or after
// AppSearch and the actions that apply its results when they do not fit
// between 50 and 100, so that the gap does not limit how many properties a
// package can search, or when AppSearch was moved, so that the launch
// conditions still see what it found; or where standardActions moves it.
func (b *build) launchConditionsAction() {
	if !b.hasRows("LaunchCondition") {
		return
	}

	for _, table := range []string{seqInstallExecute, seqInstallUI} {
		_, _, searchMoved := b.cfg.StandardPlacement(table, actAppSearch)
		_, _, launchMoved := b.cfg.StandardPlacement(table, actLaunchConditions)

		if b.scheduled(table, actAppSearch) && !b.scheduled(table, actLaunchConditions) && !launchMoved &&
			(searchMoved || standardNumber(table, actAppSearch)+b.anchoredTo(table, actAppSearch) >= standardNumber(table, actLaunchConditions)) {
			b.schedule(table, actLaunchConditions, standardCondition(actLaunchConditions), "", actAppSearch, 0)
		}

		b.standardAt(table, actLaunchConditions)
	}
}

// userPath returns the YAML path of the placement behind an action in a
// table: a custom action's, or that of a standard action standardActions
// moves.
func (b *build) userPath(table, action string) string {
	if _, path, moved := b.cfg.StandardPlacement(table, action); moved {
		return path
	}

	return b.placementPath(table, action)
}
