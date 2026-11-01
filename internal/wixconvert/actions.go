package wixconvert

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Schedule sequences and event names shared by several keys.
const (
	seqExecute  = "execute"
	seqUI       = "ui"
	onInstall   = "install"
	onUninstall = "uninstall"
	onBoth      = "both"
)

// sequenceNames maps WiX sequence elements to schedule sequences.
var sequenceNames = map[string]string{ //nolint:gochecknoglobals // lookup table
	"InstallExecuteSequence": seqExecute,
	"InstallUISequence":      seqUI,
	"AdminExecuteSequence":   "adminExecute",
	"AdminUISequence":        "adminUI",
}

// quietExec are the WiX extension entry points that run a command line
// without a window, which run actions do.
var quietExec = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"WixQuietExec": true, "WixQuietExec64": true, "CAQuietExec": true, "CAQuietExec64": true,
	"WixSilentExec": true, "WixSilentExec64": true, "CASilentExec": true, "CASilentExec64": true,
}

// quietExecProperties hold the command line of an immediate quiet exec.
var quietExecProperties = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"QtExecCmdLine": true, "QtExec64CmdLine": true, "WixQuietExecCmdLine": true, "WixQuietExec64CmdLine": true,
	"WixSilentExecCmdLine": true, "WixSilentExec64CmdLine": true,
}

// removeOldAfter are the actions after which upgrade.removeOld can remove
// the old version.
var removeOldAfter = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"InstallValidate": true, "InstallInitialize": true, "InstallExecute": true, "InstallFinalize": true,
}

// placements reads the sequence tables into schedule entries by action.
func (c *converter) placements() map[string][]any {
	byAction := make(map[string][]any)

	for _, seq := range c.sequences {
		name, ok := sequenceNames[seq.name]
		if !ok {
			c.todof(nil, "", seq.line, "<%s> has no counterpart", seq.name)

			for _, entry := range seq.kids {
				if id := entry.attr("Action"); entry.is("Custom") && c.otherSequences[id] == nil {
					c.otherSequences[id] = seq
				}
			}

			continue
		}

		for _, entry := range seq.kids {
			if entry.is("Custom") {
				id := entry.attr("Action")
				byAction[id] = append(byAction[id], c.placement(entry, name))
			} else {
				c.standardAction(entry, seq)
			}
		}
	}

	return byAction
}

// resourceActions are the standard actions that install or remove the
// resources of components, so that a component condition can stand in for
// a condition on them.
var resourceActions = map[string]bool{ //nolint:gochecknoglobals // lookup table
	actInstallFiles: true, "RemoveFiles": true, "DuplicateFiles": true, "RemoveDuplicateFiles": true, "MoveFiles": true,
	"CreateFolders": true, "RemoveFolders": true, "CreateShortcuts": true, "RemoveShortcuts": true,
	"WriteRegistryValues": true, "RemoveRegistryValues": true, "WriteIniValues": true, "RemoveIniValues": true,
	"WriteEnvironmentStrings": true, "RemoveEnvironmentStrings": true, "InstallServices": true, "DeleteServices": true,
	"StartServices": true, "StopServices": true, "RegisterTypeLibraries": true, "UnregisterTypeLibraries": true,
	"SelfRegModules": true, "SelfUnregModules": true, "RegisterClassInfo": true, "UnregisterClassInfo": true,
	"RegisterExtensionInfo": true, "UnregisterExtensionInfo": true, "RegisterProgIdInfo": true, "UnregisterProgIdInfo": true,
	"RegisterMIMEInfo": true, "UnregisterMIMEInfo": true, "InstallODBC": true, "RemoveODBC": true,
}

// standardAction converts a standard action in a sequence.
func (c *converter) standardAction(entry, seq *elem) {
	switch {
	case entry.is("ScheduleReboot"):
		c.scheduleReboot(entry)
	case entry.is("RemoveExistingProducts") && c.major == nil && removeOldAfter[entry.attr("After")]:
		c.out.child("upgrade").set("removeOld", "after"+entry.attr("After"))
		c.removeLine = entry.line

		if cond := c.condition(entry); cond != "" {
			c.todof(c.out.child("upgrade"), "removeOld", entry.line,
				"RemoveExistingProducts runs only when %s; msi-builder removes the old version whenever it finds one", cond)
		}
	case entry.is("Show"):
		c.todof(nil, "", entry.line, "<Show Dialog=%q> schedules a dialog; use the ui keys", entry.attr("Dialog"))
	default:
		c.standardActionNote(entry, seq)
	}
}

// standardActionNote notes a standard action that a sequence reschedules or
// conditions, neither of which msi-builder takes.
func (c *converter) standardActionNote(entry, seq *elem) {
	switch cond := c.condition(entry); {
	case cond != "" && resourceActions[entry.name]:
		c.todof(nil, "", entry.line, "the standard action %s runs only when %s; msi-builder has no conditions on standard "+
			"actions, so put the condition on the components whose resources it handles (components[].condition)",
			entry.name, cond)
	case cond != "":
		c.todof(nil, "", entry.line, "the standard action %s in %s runs only when %s; msi-builder schedules the standard "+
			"actions itself, without conditions", entry.name, seq.name, cond)
	default:
		c.todof(nil, "", entry.line, "rescheduling the standard action %s in %s has no counterpart", entry.name, seq.name)
	}
}

func (c *converter) placement(custom *elem, sequence string) *omap {
	placement := flowMap()
	placement.order("sequence", "before", "after", "at", "condition", "when")
	placement.set("sequence", sequence)
	placement.set("before", custom.attr("Before"))
	placement.set("after", custom.attr("After"))

	if at, err := strconv.Atoi(custom.attr("Sequence")); err == nil {
		placement.set("at", at)
	}

	c.setCondition(placement, c.condition(custom))

	if custom.has("OnExit") {
		c.todof(nil, "", custom.line, "actions that run on exit (OnExit) have no counterpart")
	}

	return placement
}

// condition returns the condition of a sequence entry: its Condition
// attribute, or in WiX v3 its text.
func (c *converter) condition(entry *elem) string {
	if entry.has("Condition") {
		return c.value(entry, "Condition")
	}

	return c.expand(entry, strings.TrimSpace(entry.text))
}

func (c *converter) scheduleReboot(entry *elem) {
	reboot := c.out.child("scheduleReboot")
	reboot.keep = true
	c.setCondition(reboot, c.condition(entry))

	if entry.attr("After") != "InstallFinalize" {
		c.todof(c.out, "scheduleReboot", entry.line, "ScheduleReboot is scheduled after InstallFinalize, not here")
	}
}

func (c *converter) emitActions() {
	byAction := c.placements()
	sides := c.extensionSides(byAction)

	c.quietExecCommands()
	reanchor := c.reanchor(byAction)
	progress := c.progress()
	scheduled := c.scheduledBinaries(byAction)

	for _, custom := range c.actionOrder {
		actionID := custom.attr("Id")
		if c.skipAction(custom, byAction) {
			continue
		}

		action := c.action(custom)
		if action == nil {
			continue
		}

		reanchor(actionID, byAction[actionID])

		if len(byAction[actionID]) == 0 {
			c.unscheduledAction(custom, scheduled)

			continue
		}

		c.setProgress(action, actionID, progress)
		action.set("schedule", byAction[actionID])
		delete(byAction, actionID)
		c.actionScope(action, custom)
		c.extensionAnchors(action, custom, byAction, sides)
		c.out.add("customActions", action)
	}

	for _, setter := range c.setters {
		if c.skipSetter(setter, byAction) {
			continue
		}

		action, schedule := c.setter(setter)
		id := action.str("id")
		reanchor(id, schedule)

		c.setProgress(action, id, progress)
		action.set("schedule", append(schedule, byAction[id]...))
		delete(byAction, id)
		c.extensionAnchors(action, setter, byAction, sides)
		c.out.add("customActions", action)
	}

	for _, id := range slices.Sorted(maps.Keys(byAction)) {
		if c.actions[id] == nil && id != c.launchAction {
			c.todof(nil, "", 0, "the sequences schedule %s, which this file does not define (an extension's action?)", id)
		}
	}

	c.leftoverProgress(progress)
}

// actionScope gives an action of a dual package a scope: perMachine for
// one without impersonation, which msi-builder runs only in per-machine
// installs (CA-09), and the scope its schedules' conditions name when each
// of them names the same one. A condition that only names the scope goes,
// since the builder writes it.
func (c *converter) actionScope(action *omap, custom *elem) {
	if !c.dualScope() {
		return
	}

	placements := schedulePlacements(action)
	// Only a deferred, rollback or commit action can run elevated.
	elevated := custom.no("Impersonate") && action.str("execute") != ""

	if !inExecuteOnly(placements) {
		if elevated {
			c.outsideExecute(action, custom, placements)
		}

		return
	}

	scope := c.namedScope(placements)

	switch {
	case scope != "" && (!elevated || scope == scopeMachine):
		dropConditions(placements)
		action.set("scope", scope)
	case scope == scopeUser:
		dropConditions(placements)
		action.del("impersonate")
		action.set("scope", scope)
		c.todof(action, "scope", custom.line, "%s runs only per user; in the per-user installs CA-09 tested, a standard "+
			"user's and an administrator's, an action without impersonation ran as the installing user, so it runs "+
			"impersonated; a per-user install elevated by policy (AlwaysInstallElevated) was not tested", custom.attr("Id"))
	case elevated:
		c.machineScope(action, custom, placements)
	}
}

// machineScope gives an action without impersonation scope: perMachine,
// with a note that names a condition testing ALLUSERS itself.
func (c *converter) machineScope(action *omap, custom *elem, placements []*omap) {
	action.set("scope", scopeMachine)

	note := "check that %[2]s still runs when it should"
	if testsAllUsers(placements) {
		note = "a condition of %[2]s tests ALLUSERS itself: one that holds only per user now never holds, so move " +
			"that schedule into an impersonated action with scope: perUser"
	}

	c.todof(action, "scope", custom.line, "msi-builder runs an action without impersonation in a %[1]s package only in "+
		"per-machine installs (CA-09), so it adds ALLUSERS=1 to the schedule; "+note, c.scope, custom.attr("Id"))
}

// testsAllUsers reports whether a placement's condition names ALLUSERS.
func testsAllUsers(placements []*omap) bool {
	return slices.ContainsFunc(placements, func(placement *omap) bool {
		return strings.Contains(strings.ToUpper(placement.str("condition")), "ALLUSERS")
	})
}

// outsideExecute notes an action without impersonation that runs outside
// the execute sequence, where no scope keeps it out of per-user installs.
func (c *converter) outsideExecute(action *omap, custom *elem, placements []*omap) {
	fix := "move the other placements into an action of their own"
	if !slices.ContainsFunc(placements, func(p *omap) bool { return p.str("sequence") == seqExecute }) {
		fix = "it cannot run without impersonation in this package"
	}

	c.todof(action, "impersonate", custom.line, "%s runs without impersonation outside the execute sequence; in a %s "+
		"package msi-builder runs such an action only with scope: perMachine, which needs the execute sequence: %s",
		custom.attr("Id"), c.scope, fix)
}

// dropConditions removes the conditions of placements whose context the
// action's scope now names.
func dropConditions(placements []*omap) {
	for _, placement := range placements {
		placement.del("condition")
	}
}

// inExecuteOnly reports whether every placement is in the execute
// sequence, the only one where an action may have a scope.
func inExecuteOnly(placements []*omap) bool {
	for _, placement := range placements {
		if placement.str("sequence") != seqExecute {
			return false
		}
	}

	return true
}

// schedulePlacements returns the placements of an action's schedule.
func schedulePlacements(action *omap) []*omap {
	schedule, ok := action.get("schedule").([]any)
	if !ok {
		return nil
	}

	placements := make([]*omap, 0, len(schedule))

	for _, entry := range schedule {
		if placement, ok := entry.(*omap); ok {
			placements = append(placements, placement)
		}
	}

	return placements
}

// namedScope returns the scope that the condition of every placement
// names alone, or "".
func (c *converter) namedScope(placements []*omap) string {
	scope := ""

	for _, placement := range placements {
		found := c.scopeCondition(placement.str("condition"))
		if found == "" || scope != "" && found != scope {
			return ""
		}

		scope = found
	}

	return scope
}

// leftoverProgress notes the progress texts that no converted action took:
// those of standard or extension actions, and those of custom actions that
// became part of another action or were not converted.
func (c *converter) leftoverProgress(progress map[string]*elem) {
	for _, actionID := range slices.Sorted(maps.Keys(progress)) {
		line := progress[actionID].line
		if c.actions[actionID] == nil && !c.isSetter(actionID) {
			c.todof(nil, "", line, "the progress text of %s cannot be converted: msi-builder writes progress texts only for its customActions", actionID)

			continue
		}

		c.todof(nil, "", line, "the progress text of %s is dropped: the action was not converted to a customActions entry of its own", actionID)
	}
}

// isSetter reports whether id names a SetProperty or SetDirectory element.
func (c *converter) isSetter(id string) bool {
	return slices.ContainsFunc(c.setters, func(setter *elem) bool { return setterName(setter) == id })
}

// progress indexes the ProgressText elements by action. setProgress
// removes the ones it uses, so the rest can be reported.
func (c *converter) progress() map[string]*elem {
	texts := make(map[string]*elem, len(c.progressTexts))

	for _, text := range c.progressTexts {
		actionID := text.attr("Action")
		if _, dup := texts[actionID]; dup {
			c.todof(nil, "", text.line, "a second progress text for %s is ignored", actionID)

			continue
		}

		texts[actionID] = text
	}

	return texts
}

// setProgress sets the progress key of the action actionID from its
// ProgressText: the Message attribute of WiX v4 or later, or the element
// text of WiX v3.
func (c *converter) setProgress(action *omap, actionID string, texts map[string]*elem) {
	text := texts[actionID]
	if text == nil {
		return
	}

	delete(texts, actionID)

	message := strings.TrimSpace(c.expand(text, strings.TrimSpace(text.text)))
	if text.has("Message") {
		message = strings.TrimSpace(c.value(text, "Message"))
	}

	if message != "" {
		action.set("progress", message)
	}

	if text.has("Template") {
		key := ""
		if message != "" {
			key = "progress"
		}

		c.todof(action, key, text.line, "the progress template of %s is not converted: msi-builder writes no ActionText templates", actionID)
	}
}

// extensionAction matches the ID of a WiX extension's custom action, such
// as Wix4CloseApplications_X64.
var extensionAction = regexp.MustCompile(`^Wix[0-9]*[A-Z]`)

// extensionAnchors notes placements anchored on a WiX extension's action,
// which the msi-builder package does not have. When the source schedules
// that action itself, such as an override before InstallValidate, the
// placement takes its place; else convert falls back to fallbackAnchor.
func (c *converter) extensionAnchors(action *omap, custom *elem, byAction map[string][]any, sides map[anchorSide]bool) {
	schedule, ok := action.get("schedule").([]any)
	if !ok {
		return
	}

	for i, entry := range schedule {
		placement, ok := entry.(*omap)
		if !ok {
			continue
		}

		for _, key := range []string{keyBefore, keyAfter} {
			anchor := placement.str(key)
			if !extensionAction.MatchString(anchor) || c.actions[anchor] != nil {
				continue
			}

			// The source's own place of the extension action keeps the
			// order only for an action on its far side, such as one after
			// an action that runs before InstallValidate, and only when no
			// action is on its near side, which the fallback would place.
			seq := placement.str("sequence")
			if own := extensionPlacement(anchor, seq, byAction); own != nil && own.str(key) == "" &&
				!sides[anchorSide{anchor, seq, otherSide(key)}] {
				schedule[i] = moveAnchor(placement, own)

				c.todof(action, "schedule", custom.line, "%s is an action of a WiX extension, which the msi-builder package "+
					"does not have; convert placed this action where the source schedules %s, %s, check that place",
					anchor, anchor, anchorText(own))

				break
			}

			// Keeping before or after keeps the order of the actions
			// around the anchor, such as a rollback action before its
			// deferred one.
			target := fallbackAnchor(action.str("execute") != "")
			placement.set(key, target)
			c.todof(action, "schedule", custom.line, "%s is an action of a WiX extension, which the msi-builder package "+
				"does not have; convert placed this action %s %s, check that place", anchor, key, target)
		}
	}
}

// anchorSide is a side of an anchor in a sequence: before or after it.
type anchorSide struct{ anchor, sequence, key string }

// extensionSides returns the sides of the WiX extension actions that the
// source schedules actions on: Custom entries and SetProperty or
// SetDirectory elements, counted in every sequence a setter may run in.
func (c *converter) extensionSides(byAction map[string][]any) map[anchorSide]bool {
	sides := make(map[anchorSide]bool)

	for _, setter := range c.setters {
		for _, key := range []string{keyBefore, keyAfter} {
			if anchor := setter.attr(strings.ToUpper(key[:1]) + key[1:]); extensionAction.MatchString(anchor) {
				for _, seq := range sequenceNames {
					sides[anchorSide{anchor, seq, key}] = true
				}
			}
		}
	}

	for _, schedule := range byAction {
		for _, entry := range schedule {
			placement, ok := entry.(*omap)
			if !ok {
				continue
			}

			for _, key := range []string{keyBefore, keyAfter} {
				if anchor := placement.str(key); extensionAction.MatchString(anchor) {
					sides[anchorSide{anchor, placement.str("sequence"), key}] = true
				}
			}
		}
	}

	return sides
}

// otherSide returns after for before and before for after.
func otherSide(key string) string {
	if key == keyBefore {
		return keyAfter
	}

	return keyBefore
}

// extensionPlacement returns the placement the source gives a WiX
// extension's action, also as an override, in a sequence, when it is next
// to an action that is no extension's, or nil.
func extensionPlacement(anchor, sequence string, byAction map[string][]any) *omap {
	for _, id := range []string{anchor, "override " + anchor} {
		for _, entry := range byAction[id] {
			own, ok := entry.(*omap)
			if !ok || own.str("sequence") != sequence {
				continue
			}

			if next := own.str(keyBefore) + own.str(keyAfter); next != "" && !extensionAction.MatchString(next) {
				return own
			}
		}
	}

	return nil
}

// anchorText describes a placement's place, such as "before InstallValidate".
func anchorText(placement *omap) string {
	if name := placement.str(keyBefore); name != "" {
		return keyBefore + " " + name
	}

	return keyAfter + " " + placement.str(keyAfter)
}

// unscheduledAction notes a custom action that no sequence schedules,
// which msi-builder refuses: one that only a DoAction control event runs,
// or one that never runs.
func (c *converter) unscheduledAction(custom *elem, scheduled map[string]bool) {
	actionID := custom.attr("Id")

	// The action's binary stays in binaries, which nothing may use now.
	binary := ""
	if id := actionBinary(custom); id != "" && !scheduled[id] {
		binary = "; drop the binary " + id + " from binaries unless something else uses it"
	}

	if publish := c.doActions[actionID]; publish != nil {
		where := "a dialog"
		if dialog := publish.attr("Dialog"); dialog != "" {
			where = "the dialog " + dialog
		}

		c.todof(nil, "", custom.line, "the action %s runs only from the DoAction control event of %s (line %d), which has "+
			"no counterpart; convert left it out%s", actionID, where, publish.line, binary)

		return
	}

	if seq := c.otherSequences[actionID]; seq != nil {
		c.todof(nil, "", custom.line, "only <%s> (line %d), which has no counterpart, schedules the action %s; convert left it out%s",
			seq.name, seq.line, actionID, binary)

		return
	}

	c.todof(nil, "", custom.line, "no sequence schedules the action %s, so it never runs; convert left it out%s", actionID, binary)
}

// actionBinary returns the Binary a custom action runs, if any.
func actionBinary(custom *elem) string {
	return custom.attr("BinaryRef") + custom.attr("BinaryKey")
}

// scheduledBinaries returns the binaries that scheduled custom actions
// run, before emitActions takes the schedules out of byAction.
func (c *converter) scheduledBinaries(byAction map[string][]any) map[string]bool {
	scheduled := make(map[string]bool)

	for id, custom := range c.actions {
		if binary := actionBinary(custom); binary != "" && len(byAction[id]) > 0 {
			scheduled[binary] = true
		}
	}

	return scheduled
}

// skipAction reports whether a custom action is left out of
// customActions: taken in by another action or key, or not needed.
func (c *converter) skipAction(custom *elem, byAction map[string][]any) bool {
	return c.consumed[custom] || c.onlyLaunch(custom.attr("Id"), byAction) ||
		c.perMachineFolder(custom, custom.attr(attrProperty), byAction)
}

// skipSetter reports whether a SetProperty or SetDirectory is left out.
func (c *converter) skipSetter(setter *elem, byAction map[string][]any) bool {
	return c.consumed[setter] || setter.is("SetProperty") && c.perMachineFolder(setter, setter.attr("Id"), byAction)
}

// perMachineFolder reports whether an action sets WixPerMachineFolder, the
// per-machine folder of WixUI_Advanced, and leaves it out: msi-builder's
// dual-scope packages pick the folder for each scope themselves.
func (c *converter) perMachineFolder(action *elem, property string, byAction map[string][]any) bool {
	if property != propPerMachineFolder || !action.has("Value") {
		return false
	}

	if action.is("SetProperty") {
		delete(byAction, setterName(action))
	} else {
		delete(byAction, action.attr("Id"))
	}

	c.todof(nil, "", action.line, "the action %s, which sets WixPerMachineFolder for WixUI_Advanced, was left out: "+
		"msi-builder sets the install folder of each scope itself", action.attr("Id"))

	return true
}

// onlyLaunch reports whether the action is the finish page's launch action
// and nothing else: ui.launch takes it. One that a sequence also schedules,
// such as an auto-launch after a quiet update, stays an action as well.
func (c *converter) onlyLaunch(actionID string, byAction map[string][]any) bool {
	return actionID == c.launchAction && len(byAction[actionID]) == 0
}

// anchorKey names an action in one sequence.
type anchorKey struct{ action, sequence string }

// reanchor returns the function that moves the placements anchored on an
// action that convert leaves out, a command line setter that becomes part
// of its run action or a WixPerMachineFolder setter, to that action's own
// anchor in the same sequence. emitActions calls it for the actions it
// writes only, so that no note names a placement left out of the output.
func (c *converter) reanchor(byAction map[string][]any) func(string, []any) {
	anchors, sources := c.sourceAnchors(byAction)

	move := func(id string, entries []any) {
		for i, entry := range entries {
			if placement, ok := entry.(*omap); ok {
				entries[i] = c.followAnchors(id, placement, anchors, sources)
			}
		}
	}

	return move
}

// The standard actions that convert places actions on.
const (
	actCostFinalize = "CostFinalize"
	actInstallFiles = "InstallFiles"
)

// fallbackAnchor is the standard action an action whose anchor is gone
// moves next to: CostFinalize, in every sequence that takes custom actions,
// before the installation changes anything, or InstallFiles for a deferred
// action, which must run inside the install script.
func fallbackAnchor(deferred bool) string {
	if deferred {
		return actInstallFiles
	}

	return actCostFinalize
}

// The keys of a placement that name its anchor.
const (
	keyBefore = "before"
	keyAfter  = "after"
)

// followAnchors moves a placement on a left-out setter to that setter's
// anchor, and on through setters that are left out too. A setter that is
// not in the placement's sequence leaves the placement at CostFinalize,
// which both sequences have, or at InstallFiles for an action that runs in
// the install script, with a note.
func (c *converter) followAnchors(actionID string, placement *omap, anchors map[anchorKey]*omap, sources map[string]*elem) *omap {
	for range len(sources) + 1 {
		key := keyAfter
		if placement.str(keyBefore) != "" {
			key = keyBefore
		}

		name := placement.str(key)

		src := sources[name]
		if src == nil {
			return placement
		}

		anchor := anchors[anchorKey{name, placement.str("sequence")}]
		if anchor == nil {
			custom := c.actions[actionID]
			target := fallbackAnchor(custom != nil && inScript(custom))

			moved := moveAnchor(placement, flowMap().set(key, target))
			c.todof(moved, "", src.line, "%s was scheduled next to %s, which is not in the %s sequence; convert "+
				"placed it %s %s, check that place", actionID, name, placement.str("sequence"), key, target)

			return moved
		}

		placement = moveAnchor(placement, anchor)
	}

	return placement
}

// sourceAnchors returns the placement of each left-out setter in each
// sequence, and the setters by name.
func (c *converter) sourceAnchors(byAction map[string][]any) (map[anchorKey]*omap, map[string]*elem) {
	anchors := make(map[anchorKey]*omap)
	sources := make(map[string]*elem)

	for _, src := range append(c.consumedSources(), c.perMachineSources()...) {
		sources[src.id] = src.e

		schedule := byAction[src.id]
		if src.e.is("SetProperty") {
			_, schedule = c.setter(src.e)
		}

		for _, entry := range schedule {
			if placement, ok := entry.(*omap); ok {
				key := anchorKey{src.id, placement.str("sequence")}
				if anchors[key] == nil {
					anchors[key] = placement
				}
			}
		}
	}

	return anchors, sources
}

// perMachineSources returns the setters of WixPerMachineFolder, which
// perMachineFolder leaves out.
func (c *converter) perMachineSources() []commandSource {
	var sources []commandSource

	for _, src := range c.commandSources() {
		if src.property == propPerMachineFolder {
			sources = append(sources, src)
		}
	}

	return sources
}

// consumedSources returns each command line source that became part of a
// run action once, in the order of the actions.
func (c *converter) consumedSources() []commandSource {
	var sources []commandSource

	seen := make(map[*elem]bool)

	for _, quiet := range c.actionOrder {
		if src, ok := c.quietCommands[quiet.attr("Id")]; ok && !seen[src.e] {
			seen[src.e] = true
			sources = append(sources, src)
		}
	}

	return sources
}

// moveAnchor returns the placement with the anchor of another one.
func moveAnchor(placement, anchor *omap) *omap {
	moved := flowMap()
	moved.order("sequence", "before", "after", "at", "condition", "when")

	for _, key := range []string{"sequence", "condition", "when"} {
		moved.set(key, placement.get(key))
	}

	for _, key := range []string{"before", "after", "at"} {
		moved.set(key, anchor.get(key))
	}

	return moved
}

// inScript reports whether a custom action runs in the install script:
// deferred, rollback or commit.
func inScript(ca *elem) bool {
	switch ca.attr("Execute") {
	case "deferred", "rollback", "commit":
		return true
	default:
		return false
	}
}

func immediate(ca *elem) bool {
	x := ca.attr("Execute")

	return x == "" || x == "immediate"
}

// commandSource is an element that sets a property a quiet exec action
// reads its command line from: a CustomAction with Property and Value, or
// a SetProperty.
type commandSource struct {
	e        *elem
	id       string
	property string
}

// commandSources lists the elements that set a property.
func (c *converter) commandSources() []commandSource {
	var sources []commandSource

	for _, custom := range c.actionOrder {
		if custom.has(attrProperty) && custom.has("Value") {
			sources = append(sources, commandSource{e: custom, id: custom.attr("Id"), property: custom.attr(attrProperty)})
		}
	}

	for _, setter := range c.setters {
		if setter.is("SetProperty") {
			sources = append(sources, commandSource{e: setter, id: setterName(setter), property: setter.attr("Id")})
		}
	}

	return sources
}

// passesCommand reports whether a source passes the command line to the
// quiet exec action qe: through CustomActionData, the property named like
// a deferred action, or through the extension's command line property for
// an immediate one.
func passesCommand(src commandSource, qe *elem) bool {
	return src.property == qe.attr("Id") || quietExecProperties[src.property] && immediate(qe)
}

// commandFor picks the source of a quiet exec action's command line. Each
// immediate quiet exec action reads the one shared property, so the source
// is the setter scheduled right before it.
func (c *converter) commandFor(quiet *elem, sources []commandSource) (commandSource, bool) {
	var candidates []commandSource

	for _, src := range sources {
		if src.property == quiet.attr("Id") {
			return src, true
		}

		if passesCommand(src, quiet) {
			candidates = append(candidates, src)
		}
	}

	switch len(candidates) {
	case 0:
		return commandSource{}, false
	case 1:
		return candidates[0], true
	}

	if src, ok := c.scheduledBefore(quiet, candidates); ok {
		return src, true
	}

	c.todof(nil, "", quiet.line,
		"several actions set the command line property of %s and none is scheduled right before it; check its run.command", quiet.attr("Id"))

	return candidates[0], true
}

// seqEntry is a Custom element of a sequence, or a SetProperty, in one
// schedule sequence.
type seqEntry struct {
	e        *elem
	sequence string
}

// scheduledBefore finds the candidate that the same sequence schedules right
// before the action: Before it, named in its After, or with the highest
// sequence number below its own.
func (c *converter) scheduledBefore(quiet *elem, candidates []commandSource) (commandSource, bool) {
	actionID := quiet.attr("Id")
	entries := c.sequenceEntries()
	own := entries[actionID]

	for _, src := range candidates {
		for _, srcEntry := range sourceEntries(src, entries) {
			for _, ownEntry := range own {
				if srcEntry.sequence == ownEntry.sequence &&
					(srcEntry.e.attr("Before") == actionID || ownEntry.e.attr("After") == src.id) {
					return src, true
				}
			}
		}
	}

	return lowerSequence(entries, own, candidates)
}

// sourceEntries returns where a command line source is scheduled: in the
// sequences, or for a SetProperty, by its own attributes.
func sourceEntries(src commandSource, entries map[string][]seqEntry) []seqEntry {
	if !src.e.is("SetProperty") {
		return entries[src.id]
	}

	var own []seqEntry
	for _, seq := range setterSequenceNames(src.e) {
		own = append(own, seqEntry{e: src.e, sequence: seq})
	}

	return own
}

// lowerSequence finds the candidate with the highest sequence number below
// the action's in the same sequence.
func lowerSequence(entries map[string][]seqEntry, own []seqEntry, candidates []commandSource) (commandSource, bool) {
	best, bestAt := commandSource{}, -1

	for _, ownEntry := range own {
		ownAt, err := strconv.Atoi(ownEntry.e.attr("Sequence"))
		if err != nil {
			continue
		}

		for _, src := range candidates {
			for _, srcEntry := range entries[src.id] {
				if srcEntry.sequence != ownEntry.sequence {
					continue
				}

				if srcAt, err := strconv.Atoi(srcEntry.e.attr("Sequence")); err == nil && srcAt < ownAt && srcAt > bestAt {
					best, bestAt = src, srcAt
				}
			}
		}
	}

	return best, bestAt >= 0
}

// sequenceEntries indexes the Custom elements of the sequences by action.
func (c *converter) sequenceEntries() map[string][]seqEntry {
	entries := make(map[string][]seqEntry)

	for _, seq := range c.sequences {
		for _, entry := range seq.kids {
			if entry.is("Custom") {
				id := entry.attr("Action")
				entries[id] = append(entries[id], seqEntry{e: entry, sequence: sequenceNames[seq.name]})
			}
		}
	}

	return entries
}

// quietExecCommands picks the command line source of every quiet exec
// action. The sources become part of the run actions, not actions of their
// own.
func (c *converter) quietExecCommands() {
	sources := c.commandSources()
	c.quietCommands = make(map[string]commandSource)
	c.consumed = make(map[*elem]bool)

	for _, quiet := range c.actionOrder {
		if !quietExec[quiet.attr("DllEntry")] {
			continue
		}

		if src, ok := c.commandFor(quiet, sources); ok {
			c.quietCommands[quiet.attr("Id")] = src
			c.consumed[src.e] = true
		}
	}
}

// command returns the command line a quiet exec action runs.
func (c *converter) command(quiet *elem) (string, bool) {
	if src, ok := c.quietCommands[quiet.attr("Id")]; ok {
		return c.value(src.e, "Value"), true
	}

	for _, prop := range c.properties {
		if quietExecProperties[prop.attr("Id")] && immediate(quiet) {
			return c.value(prop, "Value"), true
		}
	}

	return "", false
}

func newAction(e *elem) *omap {
	action := newMap()
	action.order("id", "setProperty", "setDirectory", "error", "exec", "execProperty", "dll", "exe", "run",
		"execute", "impersonate", "scope", "return", "hideTarget", "once", "progress", "schedule")
	action.head = comments(e)

	return action
}

func flowMap() *omap {
	m := newMap()
	m.flow = true

	return m
}

// action converts a CustomAction; nil means it cannot be converted.
func (c *converter) action(custom *elem) *omap {
	action := newAction(custom)
	action.set("id", custom.attr("Id"))

	if c.setterKind(action, custom) {
		c.actionFlags(action, custom, false)

		return action
	}

	if !c.programKind(action, custom) {
		return nil
	}

	c.asyncExec(action, custom)
	c.actionFlags(action, custom, true)

	return action
}

// ice105Folders are the standard folders whose Directory row ICE105
// rejects in a package that may install per user; msi-builder refuses them
// as an action's working directory there (section 8.2, CA-09).
var ice105Folders = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"AdminToolsFolder": true, "CommonAppDataFolder": true, "FontsFolder": true, "System16Folder": true,
	"System64Folder": true, "SystemFolder": true, "TempFolder": true, "WindowsFolder": true, "WindowsVolume": true,
}

// actionFolder handles a working directory that ICE105 rejects in a
// per-user or dual package: a run action leaves it out, an exec action
// keeps it with a note. It reports whether it left the directory out.
func (c *converter) actionFolder(action *omap, custom *elem, dir string) bool {
	if !ice105Folders[dir] || c.scope != scopeUser && !c.dualScope() {
		return false
	}

	if runKey(custom) == "run" {
		c.todof(action, "run", custom.line, "%s ran in %s, which msi-builder refuses as a working directory in a %s package "+
			"(ICE105), so it runs without one; check that the command does not rely on it", custom.attr("Id"), dir, c.scope)

		return true
	}

	c.todof(action, "exec", custom.line, "%s runs in %s, which msi-builder refuses as a working directory in a %s package "+
		"(ICE105): use INSTALLDIR", custom.attr("Id"), dir, c.scope)

	return false
}

// runKey returns the kind a program action becomes: run, or exec for one
// WiX does not wait for, since the run DLL waits for its command and run
// refuses asyncNoWait.
func runKey(custom *elem) string {
	if custom.attr("Return") == "asyncNoWait" {
		return "exec"
	}

	return "run"
}

// asyncExec notes that a quiet exec action WiX does not wait for became an
// exec action, which shows the program's window.
func (c *converter) asyncExec(action *omap, custom *elem) {
	if runKey(custom) == "exec" && quietExec[custom.attr("DllEntry")] {
		c.todof(action, "exec", custom.line, "%s does not wait for its command, which run actions do; "+
			"exec starts the command with its window", custom.attr("DllEntry"))
	}
}

// setterKind converts the actions that set a value or show an error.
func (c *converter) setterKind(action *omap, custom *elem) bool {
	switch {
	case custom.has("Error"):
		action.set("error", c.errorText(custom))
	case custom.has(attrProperty) && custom.has("Value"):
		action.set("setProperty", flowMap().set("name", custom.attr(attrProperty)).set("value", c.value(custom, "Value")))
	case custom.has(attrDirectory) && custom.has("Value"):
		action.set("setDirectory", flowMap().set("directory", c.dirRef(custom.attr(attrDirectory))).set("value", c.value(custom, "Value")))
	default:
		return false
	}

	return true
}

// programKind converts the actions that run a program or a DLL; false
// means the action has no counterpart.
func (c *converter) programKind(action *omap, custom *elem) bool { //nolint:cyclop // one branch per WiX custom action type
	binary := custom.attr("BinaryRef") + custom.attr("BinaryKey")
	file := custom.attr("FileRef") + custom.attr("FileKey")
	exe := c.value(custom, "ExeCommand")

	switch {
	case file != "" && custom.has("ExeCommand"):
		action.set(runKey(custom), newMap().set("command", strings.TrimSpace(`"[#`+file+`]" `+exe)))
	case custom.has(attrDirectory) && custom.has("ExeCommand"):
		program := newMap().set("command", exe)
		action.set(runKey(custom), program)

		if dir := c.dirRef(custom.attr(attrDirectory)); !c.actionFolder(action, custom, dir) {
			program.set("directory", dir)
		}

		c.quoteNote(action, runKey(custom), custom.line, exe)
	case custom.has(attrProperty) && custom.has("ExeCommand"):
		action.set("execProperty", newMap().set("property", custom.attr(attrProperty)).set("arguments", exe))
	case binary != "" && quietExec[custom.attr("DllEntry")]:
		c.quietExecRun(action, custom)
	case binary != "" && strings.HasPrefix(binary, "Wix"):
		c.todof(nil, "", custom.line, "the custom action %s calls the WiX extension %s!%s, which has no counterpart",
			custom.attr("Id"), binary, custom.attr("DllEntry"))

		return false
	case binary != "" && custom.has("DllEntry"):
		action.set("dll", flowMap().set("binary", binary).set("entry", custom.attr("DllEntry")))
	case binary != "" && custom.has("ExeCommand"):
		action.set("exe", newMap().set("binary", binary).set("arguments", exe))
	case custom.has("Script") || custom.has("VBScriptCall") || custom.has("JScriptCall") || strings.TrimSpace(custom.text) != "":
		c.todof(nil, "", custom.line, "the custom action %s is a script; script custom actions are never supported, use a run action", custom.attr("Id"))

		return false
	default:
		c.todof(nil, "", custom.line, "the custom action %s has no counterpart", custom.attr("Id"))

		return false
	}

	return true
}

func (c *converter) quietExecRun(action *omap, quiet *elem) {
	cmd, ok := c.command(quiet)
	if !ok {
		c.todof(action, "", quiet.line, "the command line of %s is set elsewhere; write it into %s.command", quiet.attr("DllEntry"), runKey(quiet))
	}

	run := newMap().set("command", cmd)
	run.keep = true
	action.set(runKey(quiet), run)
}

func (c *converter) actionFlags(action *omap, custom *elem, program bool) { //nolint:cyclop // a flat list of independent WiX attributes
	switch execute := custom.attr("Execute"); execute {
	case "", "immediate":
	case "deferred", "rollback", "commit":
		action.set("execute", execute)
	case "oncePerProcess", "firstSequence":
		action.set("once", execute)
	case "secondSequence":
		action.set("once", "clientRepeat")
	default:
		c.todof(action, "", custom.line, "Execute=%q has no counterpart", execute)
	}

	// Impersonation only matters for actions in the script: an immediate
	// action always runs as the installing user.
	switch {
	case !custom.no("Impersonate") || action.str("execute") == "":
	case c.scope == scopeUser:
		c.todof(action, "execute", custom.line, "%s ran without impersonation, which a per-user package cannot (ICE105), so it "+
			"runs impersonated; in the per-user installs of dual packages CA-09 tested such an action ran as the installing user",
			custom.attr("Id"))
	default:
		action.set("impersonate", false)
	}

	// Programs need an explicit return in msi-builder.
	ret := custom.attr("Return")
	if ret == "" && program {
		ret = "check"
	}

	action.set("return", ret)

	if custom.yes("HideTarget") {
		action.set("hideTarget", true)
	}

	if custom.has("Win64") || custom.has("Bitness") {
		c.todof(action, "", custom.line, "the action's bitness follows package.platform in msi-builder")
	}
}

// seqFirst is the Sequence of a setter that runs in the first sequence only.
const seqFirst = "first"

// setterSequences are the sequences a SetProperty or SetDirectory runs in.
func (c *converter) setterSequences(action *omap, setter *elem) []string {
	switch seq := setter.attr("Sequence"); seq {
	case "", onBoth, seqExecute, seqUI:
	case seqFirst:
		// Runs in the first sequence that reaches it: the UI sequence
		// when there is one, else the execute sequence.
		action.set("once", "firstSequence")
	default:
		c.todof(action, "", setter.line, "Sequence=%q has no counterpart", seq)
	}

	return setterSequenceNames(setter)
}

// setterSequenceNames returns the sequences a SetProperty or SetDirectory
// runs in.
func setterSequenceNames(setter *elem) []string {
	switch seq := setter.attr("Sequence"); seq {
	case "", onBoth, seqFirst:
		return []string{seqUI, seqExecute}
	case seqUI:
		return []string{seqUI}
	default:
		return []string{seqExecute}
	}
}

// setterName returns the name of the action WiX makes for a SetProperty or
// SetDirectory: its Action attribute, or Set and its Id.
func setterName(setter *elem) string {
	if name := setter.attr("Action"); name != "" {
		return name
	}

	return "Set" + setter.attr("Id")
}

// setter converts SetProperty and SetDirectory elements, which carry their
// own schedule.
func (c *converter) setter(setter *elem) (*omap, []any) {
	action := newAction(setter)
	setterID := setter.attr("Id")
	action.set("id", setterName(setter))

	before, after := setter.attr("Before"), setter.attr("After")

	if setter.is("SetDirectory") {
		action.set("setDirectory", flowMap().set("directory", c.dirRef(setterID)).set("value", c.value(setter, "Value")))

		if before == "" && after == "" {
			after = actCostFinalize
		}
	} else {
		action.set("setProperty", flowMap().set("name", setterID).set("value", c.value(setter, "Value")))
	}

	sequences := c.setterSequences(action, setter)
	schedule := make([]any, 0, len(sequences))

	for _, seq := range sequences {
		placement := flowMap().set("sequence", seq).set("before", before).set("after", after)
		c.setCondition(placement, c.value(setter, "Condition"))
		schedule = append(schedule, placement)
	}

	return action, schedule
}

// emitActionRefs notes references to actions of WiX extensions. A
// reference to an action that the file defines itself, such as one in a
// Fragment, needs nothing: the action converts with its definition.
func (c *converter) emitActionRefs() {
	for _, ref := range c.actionRefs {
		refID := ref.attr("Id")

		switch {
		case c.actions[refID] != nil || c.isSetter(refID):
		case strings.Contains(refID, "BroadcastEnvironmentChange"):
			c.todof(nil, "", ref.line,
				"drop %s: Windows Installer announces environment changes itself", refID)
		case strings.Contains(refID, "BroadcastSettingChange"):
			c.todof(nil, "", ref.line,
				"%s has no counterpart; if the package writes a setting that Windows reads only on WM_SETTINGCHANGE, send it from a run action", refID)
		default:
			c.todof(nil, "", ref.line, "the custom action %s comes from a WiX extension and has no counterpart", refID)
		}
	}
}

// whenConditions are the usual WiX conditions that a schedule's when
// expresses (docs/Migrating from WiX.md, "Custom actions").
var whenConditions = map[string]string{ //nolint:gochecknoglobals // lookup table
	`NOT Installed AND NOT WIX_UPGRADE_DETECTED`: onInstall,
	`WIX_UPGRADE_DETECTED`:                       "upgrade",
	`REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE`: onUninstall,
	`Installed AND NOT REMOVE~="ALL"`:            "maintenance",
}

// setCondition sets a schedule's condition, as when where one fits.
func (c *converter) setCondition(schedule *omap, cond string) {
	normalized := strings.Join(strings.Fields(cond), " ")

	// The when conditions name msi-builder's upgrade property, which
	// emitUpgrade sets to WIX_UPGRADE_DETECTED whenever the file uses it.
	if when, ok := whenConditions[normalized]; ok {
		schedule.set("when", when)

		return
	}

	schedule.set("condition", cond)
}

// quoteNote adds a note when the executable of a command line is an unquoted
// reference: a property alone may hold a quoted command line already (an
// UninstallString), anything else formats to a bare path that CreateProcess
// tries piece by piece (CWE-428), which msi-builder rejects.
func (c *converter) quoteNote(action *omap, key string, line int, command string) {
	token, _, _ := strings.Cut(strings.TrimLeft(command, " \t"), " ")
	if strings.HasPrefix(token, `"`) || !strings.Contains(token, "[") {
		return
	}

	bare := len(token) >= 3 && token[0] == '[' && token[len(token)-1] == ']' &&
		strings.Count(token, "[") == 1 && strings.Count(token, "]") == 1 && !strings.ContainsRune(`#!$%\~`, rune(token[1]))
	if bare {
		c.todof(action, key, line, "check %s: quote it if it holds a bare path; leave it if it holds a quoted command line such as an UninstallString", token)

		return
	}

	c.todof(action, key, line, "quote the executable %s: an unquoted path with a space is tried piece by piece by CreateProcess", token)
}
