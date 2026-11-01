package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jkroepke/msi-builder/internal/sequence"
)

// placedByBuilder are standard actions the builder places relative to
// another action rather than at their default number (RemoveExistingProducts
// by upgrade.removeOld, LaunchConditions behind a long AppSearch chain,
// ScheduleReboot after InstallFinalize), so validate cannot know their
// numbers.
var placedByBuilder = set("RemoveExistingProducts", "LaunchConditions", "ScheduleReboot") //nolint:gochecknoglobals // lookup table

// schedules numbers each sequence as far as the configuration decides it, so
// that validate reports the cycles, overlapping chains and numbers out of
// range that would fail the build, at the placement that causes them. A
// sequence holds the custom actions, the data action of each run action, the
// standard actions every product gets and those the custom actions are
// anchored to, at their default numbers. The builder adds actions of its
// own, which only lengthen the chains or take further numbers, so what fails
// here fails the build too; a chain anchored to an action whose number only
// the builder knows (a dialog, a search action, the actions in
// placedByBuilder) is left to the build.
func (v *validator) schedules(cfg *Config) {
	byID := make(map[string]*CustomAction, len(cfg.CustomActions))
	for _, action := range cfg.CustomActions {
		if _, dup := byID[action.ID]; !dup {
			byID[action.ID] = action
		}
	}

	for _, name := range slices.Sorted(maps.Keys(sequenceTables)) {
		table := sequenceTables[name]
		actions, paths, dups := scheduleOf(cfg, byID, name)
		v.diags = append(v.diags, dups...)

		v.unscheduledAnchors(byID, name, actions, paths)

		_, err := sequence.Number(withAnchors(actions, table))

		var serr *sequence.Error
		if !errors.As(err, &serr) {
			continue
		}

		path := paths[serr.Action]
		if path == "" {
			path = paths[serr.Other]
		}

		if path != "" {
			v.errorf(path, "%s", serr.Reason)
		}
	}
}

// unscheduledAnchors reports a placement anchored to a custom action that
// has no placement in the same sequence: the build finds no such action in
// the table.
func (v *validator) unscheduledAnchors(byID map[string]*CustomAction, name string, actions []sequence.Action, paths map[string]string) {
	for _, action := range actions {
		anchor := action.Before + action.After
		if anchor == "" || paths[anchor] != "" {
			continue
		}

		if custom := byID[anchor]; custom != nil && !slices.ContainsFunc(custom.Schedule, func(p Placement) bool { return p.Sequence == name }) {
			v.errorf(paths[action.Name], "anchor %s is a custom action that is not scheduled in the %s sequence", anchor, name)
		}
	}
}

// scheduleOf returns the actions of one sequence, the YAML path of the
// placement behind each custom action, as described at schedules, and an
// error for each further placement of an action in the same sequence.
//
//nolint:cyclop // a flat filter over the placements
func scheduleOf(cfg *Config, byID map[string]*CustomAction, name string) ([]sequence.Action, map[string]string, Diagnostics) {
	var actions []sequence.Action

	paths := make(map[string]string)

	var (
		runs []string
		dups Diagnostics
	)

	for i, action := range cfg.CustomActions {
		// A second action with the same id and one named like a standard
		// action are reported by actionID.
		if action.ID == "" || byID[action.ID] != action || standardActions[action.ID] {
			continue
		}

		for j, place := range action.Schedule {
			if place.Sequence != name || !wellPlaced(place) {
				continue
			}

			path := fmt.Sprintf("customActions[%d].schedule[%d]", i, j)
			if action.origin != "" {
				path = action.origin
			}

			if first, dup := paths[action.ID]; dup {
				dups = append(dups, Diagnostic{SeverityError, path, fmt.Sprintf("%s is already scheduled in this sequence at %s", action.ID, first)})

				continue
			}

			paths[action.ID] = path
			actions = append(actions, sequence.Action{Name: action.ID, Before: place.Before, After: place.After, Sequence: place.At})

			if action.Run != nil {
				runs = append(runs, action.ID)
			}
		}
	}

	// The builder schedules each run action's data action right before
	// it, after every other placement.
	for _, id := range runs {
		if _, taken := paths[id+".Data"]; !taken {
			paths[id+".Data"] = paths[id]
			actions = append(actions, sequence.Action{Name: id + ".Data", Before: id})
		}
	}

	return actions, paths, dups
}

// wellPlaced reports whether a placement passed placementPosition, so that
// its errors are not reported twice.
func wellPlaced(place Placement) bool {
	count := 0

	for _, set := range []bool{place.Before != "", place.After != "", place.At != 0} {
		if set {
			count++
		}
	}

	termination := place.Sequence == "ui" || place.Sequence == sequenceAdminUI

	return count == 1 && place.At >= minSequenceNumber && place.At <= maxSequenceNumber && (place.At >= 0 || termination)
}

// withAnchors adds the standard actions every product gets and those the
// actions are anchored to at their default numbers, as the builder does, and leaves out every action whose
// chain ends at an anchor with a number only the builder knows.
func withAnchors(actions []sequence.Action, table string) []sequence.Action {
	custom := make(map[string]bool, len(actions))
	for _, action := range actions {
		custom[action.Name] = true
	}

	children := make(map[string][]string)
	fixed := &standardSet{table: table, added: make(map[string]bool)}

	var unknown []string

	// The builder adds these to every product.
	for _, name := range sequence.Always(table) {
		fixed.add(name)
	}

	for _, action := range actions {
		anchor := action.Before + action.After

		switch {
		case anchor == "":
		case custom[anchor]:
			children[anchor] = append(children[anchor], action.Name)
		case fixed.add(anchor):
			// DuplicateFiles copies installed files: the builder adds
			// InstallFiles with it.
			if anchor == "DuplicateFiles" {
				fixed.add("InstallFiles")
			}
		default:
			unknown = append(unknown, action.Name)
		}
	}

	kept := dropChains(actions, unknown, children)

	return append(kept, fixed.actions...)
}

// standardSet collects the standard actions of a sequence at their default
// numbers.
type standardSet struct {
	added   map[string]bool
	table   string
	actions []sequence.Action
}

// add adds a standard action at its default number and reports whether it
// has one that validate can rely on.
func (s *standardSet) add(name string) bool {
	std, ok := sequence.LookupStandard(name)
	if !ok || std.Number(s.table) == 0 || placedByBuilder[name] {
		return false
	}

	if !s.added[name] {
		s.added[name] = true
		s.actions = append(s.actions, sequence.Action{Name: name, Sequence: std.Number(s.table)})
	}

	return true
}

// dropChains leaves out the actions named in unknown and every action
// chained to them through children.
func dropChains(actions []sequence.Action, unknown []string, children map[string][]string) []sequence.Action {
	dropped := make(map[string]bool)

	for len(unknown) > 0 {
		name := unknown[len(unknown)-1]
		unknown = unknown[:len(unknown)-1]

		if !dropped[name] {
			dropped[name] = true
			unknown = append(unknown, children[name]...)
		}
	}

	return slices.DeleteFunc(actions, func(a sequence.Action) bool { return dropped[a.Name] })
}
