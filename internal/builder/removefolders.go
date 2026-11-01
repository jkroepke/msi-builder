package builder

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/jkroepke/msi-builder/internal/cadll"
	"github.com/jkroepke/msi-builder/internal/sequence"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// removeFolders adds the deferred, rollback and commit actions of the
// removeFolders entries (specification section 17.10): the deferred one
// checks each folder and moves it by handle into a private backup folder,
// the rollback one moves it back, the commit one deletes it without
// following junctions. Their data comes from type-51 actions the engine
// runs right before them, never from an action that runs as the
// installing user, who could change it.
func (b *build) removeFolders() error {
	// Condition and property of each entry, in turn.
	var entries []string

	// The properties are not made secure: an administrator's values reach
	// the server anyway (PROP-01), and a secure folder property would let
	// a standard user's command line steer every other use of it in an
	// elevated session, where the DLL removes nothing.
	for i, comp := range b.cfg.Components {
		for index, entry := range comp.RemoveFolders {
			_, packageDir := b.dirs[entry.Property]
			cond := removeFolderCondition(comp.ID, entry, packageDir)

			// The condition goes into InstallExecuteSequence.Condition.
			if len(cond) > maxTargetLength {
				b.errorf(fmt.Sprintf("%s.removeFolders[%d].condition", comp.Path(i), index),
					"the condition with the component's state is %d characters, InstallExecuteSequence.Condition holds %d",
					len(cond), maxTargetLength)
			}

			entries = append(entries, cond, entry.Property)
		}
	}

	if len(entries) == 0 || !b.useRunDLL() {
		return nil
	}

	for _, name := range []string{
		cadll.RemoveFoldersUndo, cadll.RemoveFoldersExec, cadll.RemoveFoldersCommit,
		cadll.RemoveFoldersData, cadll.RemoveFoldersCount, cadll.RemoveFoldersScope,
	} {
		if _, ok := b.cfg.Properties[name]; ok {
			b.errorf("properties."+name, "the property %s is reserved for removeFolders", name)

			return nil
		}
	}

	b.removeFoldersActions()
	b.removeFoldersData(entries)

	return nil
}

// removeFoldersFixed returns the lines of the removal data that every
// install has: the standard folders, which the DLL refuses, and the
// package's directories, which it checks by path, so that a property
// holding the install dir is no way around the REMOVE~="ALL" rule for
// folders of the package.
func (b *build) removeFoldersFixed() []string {
	standard := cadll.RemoveFoldersStandard()
	fixed := make([]string, 0, len(standard)+len(b.dirs))

	for _, name := range standard {
		fixed = append(fixed, cadll.RemoveFoldersStandardLine(name))
	}

	dirs := make([]string, 0, len(b.dirs))

	for id, dir := range b.dirs {
		// An alias counts too: the command line or an action can move it
		// away from its parent.
		if !dir.standard {
			dirs = append(dirs, id)
		}
	}

	slices.Sort(dirs)

	for _, id := range dirs {
		fixed = append(fixed, cadll.RemoveFoldersPackageLine(id))
	}

	return fixed
}

// removeFoldersData builds the data of the deferred actions with type-51
// actions right before them, so that no action that runs as the user comes
// between: a reset of the count and of the scope, the fixed lines, of
// which the first setter resets the data, then two actions per entry under
// its condition, one that appends the entry with its folder formatted by
// the engine and one that counts it, then the data of each deferred
// action. The count lets the DLL find a line break that a formatted value
// added. entries alternate condition and property.
func (b *build) removeFoldersData(entries []string) {
	type setter struct{ name, property, cond, target string }

	fixed := b.removeFoldersFixed()
	scope := "[" + cadll.RemoveFoldersScope + "]"
	data := scope + "[ProductCode][" + cadll.RemoveFoldersCount + "][" + cadll.RemoveFoldersData + "]"
	setters := []setter{
		{"BuilderRemoveFoldersResetCount", cadll.RemoveFoldersCount, "", ":"},
		{"BuilderRemoveFoldersKeep", cadll.RemoveFoldersScope, "", string(cadll.RemoveFoldersKeep)},
		{"BuilderRemoveFoldersAll", cadll.RemoveFoldersScope, condRemoveAll, string(cadll.RemoveFoldersAll)},
	}

	// The first setter resets the data to a line with the number of fixed
	// lines, which the builder writes, so the DLL can count them; it and
	// the others append as many fixed lines as fit the 255 characters of
	// CustomAction.Target (ICE03).
	appendData := "[" + cadll.RemoveFoldersData + "]"
	target, parts := "\nn"+strconv.Itoa(len(fixed)), 0
	flush := func() {
		parts++
		setters = append(setters, setter{"BuilderRemoveFoldersFixed" + strconv.Itoa(parts), cadll.RemoveFoldersData, "", target})
		target = appendData
	}

	for _, line := range fixed {
		if len(target)+1+len(line) > maxTargetLength {
			flush()
		}

		target += "\n" + line
	}

	flush()

	for i := 0; i < len(entries); i += 2 {
		cond, property := entries[i], entries[i+1]
		id := generatedID("", strconv.Itoa(i/2), property)
		setters = append(setters,
			setter{"BuilderRemoveFoldersEntry" + id, cadll.RemoveFoldersData, cond, appendData + "\n" + cadll.RemoveFoldersEntry(property)},
			setter{"BuilderRemoveFoldersCount" + id, cadll.RemoveFoldersCount, cond, "[" + cadll.RemoveFoldersCount + "]#"},
		)
	}

	// With rollback disabled the installer runs no commit action, so the
	// deferred action then deletes at once.
	setters = append(setters,
		setter{"BuilderRemoveFoldersSetUndo", cadll.RemoveFoldersUndo, "", string(cadll.RemoveFoldersRollback) + data},
		setter{"BuilderRemoveFoldersSetExec", cadll.RemoveFoldersExec, "", string(cadll.RemoveFoldersMove) + data},
		setter{"BuilderRemoveFoldersSetDelete", cadll.RemoveFoldersExec, "RollbackDisabled", string(cadll.RemoveFoldersDelete) + data},
		setter{"BuilderRemoveFoldersSetCommit", cadll.RemoveFoldersCommit, "", string(cadll.RemoveFoldersFinish) + data},
	)

	for _, s := range setters {
		b.addAction(&actionInfo{name: s.name, typ: caSetProperty, source: s.property, target: s.target})
		b.schedule(seqInstallExecute, s.name, s.cond, cadll.RemoveFoldersUndo, "", 0)
		b.removeFolderActions = append(b.removeFolderActions, s.name)
	}

	b.removeFolderEntries, b.removeFolderDirs = len(entries)/2, len(fixed)-len(cadll.RemoveFoldersStandard())
}

// checkRemoveFoldersRoom reports removeFolders actions that do not fit
// between RemoveFiles and the next numbered action, such as RemoveFolders
// (3600), before numbering fails with a less helpful error: two per entry
// and one per few fixed lines, of which every directory of the package
// adds one, plus the three deferred actions.
func (b *build) checkRemoveFoldersRoom() {
	actions := b.schedules[seqInstallExecute]
	if len(b.removeFolderActions) == 0 {
		return
	}

	if _, err := sequence.Number(actions); err == nil {
		return
	}

	ours := make(map[string]bool, len(b.removeFolderActions)+3)
	for _, name := range b.removeFolderActions {
		ours[name] = true
	}

	for _, name := range []string{cadll.RemoveFoldersUndo, cadll.RemoveFoldersExec, cadll.RemoveFoldersCommit} {
		ours[name] = true
	}

	if _, err := sequence.Number(slices.DeleteFunc(slices.Clone(actions), func(a sequence.Action) bool { return ours[a.Name] })); err != nil {
		return // the numbering error is not the folder removal's
	}

	b.errorf("components", "removeFolders takes %d actions after %s (two per entry for %d entries, the lines of the "+
		"standard folders and of the package's %d directories, and three deferred ones), which do not fit before the next "+
		"numbered action; remove entries or directories", len(ours), actRemoveFiles, b.removeFolderEntries, b.removeFolderDirs)
}

// removeFoldersActions schedules the rollback, deferred and commit actions
// right after RemoveFiles, when the component states and the directories'
// paths are known and the engine's own files are gone. They run when an
// entry's condition holds, so that its setter counted it.
func (b *build) removeFoldersActions() {
	actions := []*actionInfo{
		{name: cadll.RemoveFoldersUndo, typ: caDLL | caRollback | caNoImpersonate, source: cadll.Key, target: cadll.RemoveFoldersExec},
		{name: cadll.RemoveFoldersExec, typ: caDLL | caDeferred | caNoImpersonate, source: cadll.Key, target: cadll.RemoveFoldersExec},
		{name: cadll.RemoveFoldersCommit, typ: caDLL | caCommit | caNoImpersonate, source: cadll.Key, target: cadll.RemoveFoldersExec},
	}

	b.standardAt(seqInstallExecute, actRemoveFiles)

	after := actRemoveFiles

	for _, action := range actions {
		b.addAction(action)
		b.schedule(seqInstallExecute, action.name, cadll.RemoveFoldersCount+`<>":"`, "", after, 0)
		after = action.name
	}
}

// removeFolderCondition is the condition under which the action removes an
// entry's folder: when the component is removed ($comp=2) unless the major
// upgrade of another product removes this one, which must keep the folder,
// or when it is installed locally ($comp=3), ANDed with the entry's own. A
// folder of the package (packageDir) goes only with the whole product
// (REMOVE="ALL"): removing one feature must not take away the files the
// components of the others keep there.
func removeFolderCondition(component string, entry *config.RemoveFolder, packageDir bool) string {
	removed := "($" + component + "=2 AND NOT UPGRADINGPRODUCTCODE)"
	if packageDir {
		removed = "($" + component + `=2 AND REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE)`
	}

	installed := "$" + component + "=3"

	var cond string

	switch entry.On {
	case config.RemoveOnInstall:
		cond = installed
	case config.RemoveOnBoth:
		cond = "(" + removed + " OR " + installed + ")"
	default:
		cond = removed
	}

	if entry.Condition != "" {
		cond += " AND (" + entry.Condition + ")"
	}

	return cond
}
