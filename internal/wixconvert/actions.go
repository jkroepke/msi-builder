package wixconvert

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	msisequence "github.com/jkroepke/msi-builder/internal/sequence"
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
	case movable(entry, seq):
		c.moveStandard(entry, sequenceNames[seq.name])
	default:
		c.standardActionNote(entry, seq)
	}
}

// movableActions are the standard actions that standardActions can move in
// the execute and UI sequences.
var movableActions = map[string]bool{actFindRelatedProducts: true, actAppSearch: true, actLaunchConditions: true} //nolint:gochecknoglobals // lookup table

// The standard actions that standardActions moves.
const (
	actFindRelatedProducts = "FindRelatedProducts"
	actAppSearch           = "AppSearch"
	actLaunchConditions    = "LaunchConditions"
)

// movable reports whether standardActions can take a sequence entry: one
// of the three movable actions, not suppressed, in the execute or UI
// sequence.
func movable(entry, seq *elem) bool {
	name := sequenceNames[seq.name]

	return movableActions[entry.name] && !entry.has("Suppress") && (name == seqExecute || name == seqUI)
}

// moveStandard takes the place a sequence gives FindRelatedProducts,
// AppSearch or LaunchConditions into standardActions. An entry without a
// place keeps the default number; a condition has no counterpart.
func (c *converter) moveStandard(entry *elem, sequence string) {
	placement := flowMap()
	placement.order("sequence", "before", "after", "at")
	placement.set("sequence", sequence)
	placement.set("before", entry.attr("Before"))
	placement.set("after", entry.attr("After"))

	if at, err := strconv.Atoi(entry.attr("Sequence")); err == nil {
		placement.set("at", at)
	}

	if cond := c.condition(entry); cond != "" {
		c.todof(nil, "", entry.line, "the standard action %s in %s runs only when %s; msi-builder schedules the standard "+
			"actions without conditions", entry.name, sequence, cond)
	}

	if placement.str("before") == "" && placement.str("after") == "" && placement.get("at") == nil {
		return
	}

	// A place at the default number moves nothing.
	if std, _ := msisequence.LookupStandard(entry.name); placement.get("at") == std.Number(sequenceTable(sequence)) {
		return
	}

	if c.moved == nil {
		c.moved = make(map[string][]any)
		c.movedLines = make(map[*omap]int)
	}

	c.moved[entry.name] = append(c.moved[entry.name], placement)
	c.movedLines[placement] = entry.line
}

// sequenceTable returns the table of the execute or the UI sequence.
func sequenceTable(schedule string) string {
	if schedule == seqUI {
		return msisequence.InstallUI
	}

	return msisequence.InstallExecute
}

// movedIdle returns why the converted package does not run a moved
// standard action, or "": AppSearch needs a property search or a
// remembered install dir, LaunchConditions a launch condition, its own or
// the downgrade check. The converted package always finds older versions.
func (c *converter) movedIdle(name string) string {
	switch {
	case name == actAppSearch && !c.searches():
		return "it has no property searches"
	case name == actLaunchConditions && !c.runsLaunchConditions():
		return "it has no launch conditions"
	default:
		return ""
	}
}

// searches reports whether the converted package runs AppSearch: for a
// property search or the remembered install dir.
func (c *converter) searches() bool {
	if install := c.out.mapAt("installDir"); install != nil && install.get("remember") != nil {
		return true
	}

	props := c.out.mapAt("properties")
	if props == nil {
		return false
	}

	return slices.ContainsFunc(props.keys, func(key string) bool {
		prop := props.mapAt(key)

		return prop != nil && prop.get("search") != nil
	})
}

// runsLaunchConditions reports whether the converted package runs LaunchConditions:
// for its launch conditions or for the downgrade check, which the default
// upgrade.downgrade (error) writes.
func (c *converter) runsLaunchConditions() bool {
	if c.out.get("launchConditions") != nil {
		return true
	}

	upgrade := c.out.mapAt("upgrade")
	downgrade := ""

	if upgrade != nil {
		downgrade = upgrade.str("downgrade")
	}

	return downgrade == "" || downgrade == downgradeError
}

// downgradeError is the upgrade.downgrade value that refuses a downgrade.
const downgradeError = "error"

// movedAnchorFails returns why a moved standard action cannot take a
// placement, or "": a number must be below CostInitialize, a standard
// action anchor must run before it, and any other anchor must be one of
// the converted custom actions.
func (c *converter) movedAnchorFails(placement *omap, emitted map[string]bool) string {
	if at, ok := placement.get("at").(int); ok && (at < 1 || at >= costInitialize) {
		return fmt.Sprintf("the number %d is not between 1 and CostInitialize (%d)", at, costInitialize)
	}

	for _, key := range []string{keyBefore, keyAfter} {
		if why := movedAnchorFailure(placement, key, emitted); why != "" {
			return why
		}
	}

	return ""
}

// movedAnchorFailure checks the before or after anchor of a placement.
func movedAnchorFailure(placement *omap, key string, emitted map[string]bool) string {
	anchor := placement.str(key)
	if anchor == "" || movableActions[anchor] || emitted[anchor] {
		return ""
	}

	std, ok := msisequence.LookupStandard(anchor)
	if !ok {
		return anchor + " is not a converted custom action"
	}

	n := std.Number(sequenceTable(placement.str("sequence")))
	if n == 0 || n > costInitialize || n == costInitialize && key == keyAfter {
		return fmt.Sprintf("it would run %s %s, not before CostInitialize", key, anchor)
	}

	return ""
}

// costInitialize is the default number of CostInitialize.
const costInitialize = 800

// emittedActions returns the ids of the converted custom actions.
func (c *converter) emittedActions() map[string]bool {
	ids := make(map[string]bool)

	if list, ok := c.out.get("customActions").([]any); ok {
		for _, entry := range list {
			if action, isMap := entry.(*omap); isMap {
				ids[action.str("id")] = true
			}
		}
	}

	return ids
}

// emitMoved writes standardActions, in the order of the actions' default
// numbers, with each placement on a left-out setter moved to the setter's
// anchor, as for custom actions. A move of an action the converted package
// does not run, or to a place msi-builder refuses, is left out with a note.
func (c *converter) emitMoved(reanchor func(string, []any)) {
	emitted := c.emittedActions()
	kept := make(map[string][]*omap)

	c.reanchorMoved(reanchor)
	c.decideMoves(emitted)

	for _, name := range []string{actFindRelatedProducts, actAppSearch, actLaunchConditions} {
		placements := c.moved[name]
		if len(placements) == 0 {
			continue
		}

		why := c.movedIdle(name)

		var schedule []any

		for _, entry := range placements {
			placement, ok := entry.(*omap)
			if !ok {
				continue
			}

			silent, reason := c.moveRefused(name, why, placement, emitted)
			if silent {
				continue
			}

			if reason != "" {
				c.todof(nil, "", c.movedLines[placement], "the sequence moves the standard action %s in %s, which msi-builder cannot "+
					"take (%s); convert left the move out", name, placement.str("sequence"), reason)

				continue
			}

			schedule = append(schedule, placement)
			kept[name] = append(kept[name], placement)
		}

		if len(schedule) == 0 {
			continue
		}

		moved := newMap()
		moved.order("action", "schedule")
		moved.set("action", name)
		moved.set("schedule", schedule)
		c.out.add("standardActions", moved)
	}

	c.noteLaunchBeforeFind(kept)
}

// moveRefused returns why msi-builder cannot take a placement of a moved
// action, or silent for a move that changes nothing; why is the reason the
// package does not run the action, if any.
func (c *converter) moveRefused(name, why string, placement *omap, emitted map[string]bool) (bool, string) {
	if why != "" {
		return false, why
	}

	if c.anchoredBack(name, placement) {
		return true, ""
	}

	if silent, reason := c.searchNextToLaunch(name, placement); silent || reason != "" {
		return silent, reason
	}

	if reason := c.atCollision(name, placement); reason != "" {
		return false, reason
	}

	return false, c.movedAnchorFails(placement, emitted)
}

// anchoredBack reports whether a placement starts a loop of moves anchored
// to one another in its sequence, such as AppSearch before LaunchConditions
// and LaunchConditions after AppSearch, and its action comes first in the
// loop, counting the LaunchConditions the builder puts after a moved
// AppSearch. Such moves state one order, which msi-builder would see as a
// cycle; dropping the move of the first action keeps the order.
func (c *converter) anchoredBack(name string, placement *omap) bool {
	order := []string{actFindRelatedProducts, actAppSearch, actLaunchConditions}
	seq := placement.str("sequence")
	seen := map[string]bool{name: true}
	anchor := placement.str(keyBefore) + placement.str(keyAfter)

	for anchor != name {
		if !movableActions[anchor] || seen[anchor] || slices.Index(order, anchor) < slices.Index(order, name) {
			return false
		}

		seen[anchor] = true
		anchor = c.movedAnchor(anchor, seq)
	}

	return true
}

// movedAnchor returns the action a movable action is anchored to in a
// sequence: that of its kept move, AppSearch for an unmoved
// LaunchConditions, which the builder puts after a moved AppSearch, or "".
func (c *converter) movedAnchor(name, seq string) string {
	if c.movesIn(name, seq) {
		if placement := c.movedPlacement(name, seq); placement != nil {
			return placement.str(keyBefore) + placement.str(keyAfter)
		}

		return ""
	}

	if name == actLaunchConditions && c.runsLaunchConditions() && c.movesIn(actAppSearch, seq) {
		return actAppSearch
	}

	return ""
}

// movedPlacement returns the move of an action in a sequence, or nil.
func (c *converter) movedPlacement(name, seq string) *omap {
	for _, entry := range c.moved[name] {
		if placement, ok := entry.(*omap); ok && placement.str("sequence") == seq {
			return placement
		}
	}

	return nil
}

// searchNextToLaunch checks an AppSearch move next to LaunchConditions,
// which the builder puts after a moved AppSearch unless LaunchConditions is
// moved in the same sequence too: before it is the default order, so the
// move is dropped silently; after it cannot be taken.
func (c *converter) searchNextToLaunch(name string, placement *omap) (bool, string) {
	if name != actAppSearch || !c.runsLaunchConditions() || c.movesIn(actLaunchConditions, placement.str("sequence")) {
		return false, ""
	}

	switch actLaunchConditions {
	case placement.str(keyBefore):
		return true, ""
	case placement.str(keyAfter):
		return false, "LaunchConditions follows a moved AppSearch"
	default:
		return false, ""
	}
}

// reanchorMoved moves the placements on left-out setters to the setters'
// anchors, before decideMoves, so that it sees what is emitted.
func (c *converter) reanchorMoved(reanchor func(string, []any)) {
	for _, name := range []string{actFindRelatedProducts, actAppSearch, actLaunchConditions} {
		if placements := c.moved[name]; len(placements) > 0 && c.movedIdle(name) == "" {
			reanchor(name, placements)
		}
	}
}

// decideMoves works out in which sequences convert keeps the move of each
// movable action. Whether one move can be taken depends on whether the
// others are kept (a number that collides with an unmoved action,
// AppSearch next to an unmoved LaunchConditions), so it starts from every
// move in the sequences and drops refused ones until nothing changes.
func (c *converter) decideMoves(emitted map[string]bool) {
	c.keptMoves = nil
	current := c.movesBySequence(func(string, *omap) bool { return true })

	for range len(movableActions) + 1 {
		c.keptMoves = current
		next := c.movesBySequence(func(name string, placement *omap) bool {
			silent, reason := c.moveRefused(name, c.movedIdle(name), placement, emitted)

			return !silent && reason == ""
		})

		if maps.EqualFunc(current, next, maps.Equal) {
			break
		}

		current = next
	}

	c.keptMoves = current
}

// movesBySequence returns, per movable action, the sequences of the moves
// that keep reports true for.
func (c *converter) movesBySequence(keep func(string, *omap) bool) map[string]map[string]bool {
	kept := make(map[string]map[string]bool)

	for name, entries := range c.moved {
		kept[name] = make(map[string]bool)

		for _, entry := range entries {
			if placement, ok := entry.(*omap); ok && keep(name, placement) {
				kept[name][placement.str("sequence")] = true
			}
		}
	}

	return kept
}

// atCollision returns why a moved action's number collides with the
// default number of another movable action that the converted package runs
// and does not move in that sequence, or "".
func (c *converter) atCollision(name string, placement *omap) string {
	number, ok := placement.get("at").(int)
	if !ok {
		return ""
	}

	seq := placement.str("sequence")

	for _, other := range []string{actFindRelatedProducts, actAppSearch, actLaunchConditions} {
		std, _ := msisequence.LookupStandard(other)
		if other == name || std.Number(sequenceTable(seq)) != number || c.movedIdle(other) != "" || c.movesIn(other, seq) {
			continue
		}

		// LaunchConditions follows a moved AppSearch.
		if other == actLaunchConditions && c.movesIn(actAppSearch, seq) {
			continue
		}

		return fmt.Sprintf("the number %d is that of %s", number, other)
	}

	return ""
}

// movesIn reports whether convert keeps a move of an action in a
// sequence, as far as decideMoves has worked it out.
func (c *converter) movesIn(name, seq string) bool {
	return c.keptMoves[name][seq]
}

// noteLaunchBeforeFind notes a move that puts LaunchConditions before
// FindRelatedProducts, where a launch condition on an upgrade property,
// the downgrade check among them, sees it unset; the build refuses that
// order with upgrade.downgrade: error. Places next to custom actions are
// not followed.
func (c *converter) noteLaunchBeforeFind(kept map[string][]*omap) {
	if !c.runsLaunchConditions() {
		return
	}

	bad := make(map[string]*omap)

	for _, seq := range []string{seqExecute, seqUI} {
		bad[seq] = c.launchFirstIn(kept, seq)
	}

	first := bad[seqExecute]
	if first == nil {
		first = bad[seqUI]
	}

	if first == nil {
		return
	}

	// When ignore stands in for the WiX check, its note covers the moves.
	if why := c.ignoreReason(bad); why != "" && c.ignoreDowngrades(kept, c.movedLines[first], why) {
		return
	}

	for _, seq := range []string{seqExecute, seqUI} {
		if placement := bad[seq]; placement != nil {
			c.todof(placement, "", c.movedLines[placement], "LaunchConditions runs before FindRelatedProducts here, so a launch "+
				"condition on an upgrade property, the downgrade check of upgrade.downgrade: error among them, sees it unset; "+
				"the build refuses that order with downgrade: error")
		}
	}
}

// ignoreReason says why downgrade: ignore stands in for the WiX downgrade
// check when bad holds the moves that put LaunchConditions first, or "": a
// WiX check still works in a sequence that keeps the order, so ignore only
// stands in for it when no sequence does.
func (c *converter) ignoreReason(bad map[string]*omap) string {
	switch {
	case !c.wixDowngradeCheck():
		return "the WiX package has no downgrade check"
	case bad[seqExecute] != nil && bad[seqUI] != nil:
		return "the WiX package never saw a newer version in a launch condition"
	default:
		return ""
	}
}

// launchFirstIn returns the first kept move of LaunchConditions,
// FindRelatedProducts or AppSearch when the moves put LaunchConditions
// before FindRelatedProducts in seq, else nil; a move of LaunchConditions
// that would only run the downgrade check is left out.
func (c *converter) launchFirstIn(kept map[string][]*omap, seq string) *omap {
	ranks, before := launchBeforeFind(kept, seq)
	if !before {
		return nil
	}

	// Without launch conditions of the file's own, a moved
	// LaunchConditions only holds the downgrade check: leave its move
	// out rather than check nothing.
	if c.out.get("launchConditions") == nil && c.downgradeChecked() && ranks.place(actLaunchConditions) != nil {
		c.dropLaunchMove(seq)

		kept[actLaunchConditions] = slices.DeleteFunc(slices.Clone(kept[actLaunchConditions]), func(p *omap) bool {
			return p.str("sequence") == seq
		})

		if ranks, before = launchBeforeFind(kept, seq); !before {
			return nil
		}
	}

	for _, name := range []string{actLaunchConditions, actFindRelatedProducts, actAppSearch} {
		if placement := ranks.place(name); placement != nil {
			return placement
		}
	}

	return nil
}

// wixDowngradeCheck reports whether the WiX package refuses downgrades
// itself: a MajorUpgrade without AllowDowngrades, or a launch condition on
// a property a related row sets for newer versions.
func (c *converter) wixDowngradeCheck() bool {
	upgrade := c.out.mapAt("upgrade")

	return c.major != nil && !c.major.yes("AllowDowngrades") || upgrade != nil && upgrade.str("newerProperty") != ""
}

// launchBeforeFind reports whether the kept moves put LaunchConditions
// before FindRelatedProducts in seq.
func launchBeforeFind(kept map[string][]*omap, seq string) (movedRanks, bool) {
	ranks := movedRanks{kept: kept, seq: seq, seen: make(map[string]bool)}

	find, findOK := ranks.rank(actFindRelatedProducts)
	launch, launchOK := ranks.rank(actLaunchConditions)

	return ranks, findOK && launchOK && launch <= find
}

// downgradeChecked reports whether the converted package refuses
// downgrades with upgrade.downgrade: error, the default.
func (c *converter) downgradeChecked() bool {
	upgrade := c.out.mapAt("upgrade")
	if upgrade == nil {
		return false
	}

	downgrade := upgrade.str("downgrade")

	return downgrade == "" || downgrade == downgradeError
}

// dropLaunchMove leaves out the move of LaunchConditions in seq when the
// package has no launch conditions of its own: there LaunchConditions only
// holds the downgrade check, which the move would put before
// FindRelatedProducts, an order the build refuses.
func (c *converter) dropLaunchMove(seq string) {
	list, ok := c.out.get("standardActions").([]any)
	if !ok {
		return
	}

	for i, entry := range list {
		moved, isMap := entry.(*omap)
		if !isMap || moved.str("action") != actLaunchConditions {
			continue
		}

		schedule, isList := moved.get("schedule").([]any)
		if !isList {
			continue
		}

		switch keep, rest := c.dropSequence(schedule, seq), slices.Delete(slices.Clone(list), i, i+1); {
		case len(keep) > 0:
			moved.set("schedule", keep)
		case len(rest) > 0:
			c.out.set("standardActions", rest)
		default:
			c.out.del("standardActions")
		}

		return
	}
}

// dropSequence returns the LaunchConditions schedule without its
// placements in seq, each of which gets a note.
func (c *converter) dropSequence(schedule []any, seq string) []any {
	var keep []any

	for _, item := range schedule {
		if placement, isMap := item.(*omap); isMap && placement.str("sequence") == seq {
			c.todof(nil, "", c.movedLines[placement], "the sequence moves LaunchConditions before FindRelatedProducts in %s, "+
				"where it holds only the downgrade check of upgrade.downgrade: error, which would never see a newer "+
				"version; convert left the move out", seq)

			continue
		}

		keep = append(keep, item)
	}

	return keep
}

// ignoreDowngrades sets upgrade.downgrade: ignore when LaunchConditions
// runs before FindRelatedProducts in a package that would otherwise check
// for downgrades (the default error), which the build refuses, and the WiX
// package did not refuse a downgrade either: it had no check, or no
// sequence ran its check after FindRelatedProducts (why says which). A package without launch
// conditions of its own then runs no LaunchConditions, so a kept move of it
// would fail the build; that one keeps the todo only. It reports whether it
// set ignore; its todo names line, the first move that put LaunchConditions
// first.
func (c *converter) ignoreDowngrades(kept map[string][]*omap, line int, why string) bool {
	if !c.downgradeChecked() || c.out.get("launchConditions") == nil && len(kept[actLaunchConditions]) > 0 {
		return false
	}

	upgrade := c.out.mapAt("upgrade")
	upgrade.set("downgrade", "ignore")
	c.todof(upgrade, "downgrade", line, "LaunchConditions runs before FindRelatedProducts, where the build refuses the downgrade "+
		"check of downgrade: error; %s, so it installed next to a newer version, as downgrade: ignore does. To refuse "+
		"downgrades, let LaunchConditions run after FindRelatedProducts and remove this line", why)

	return true
}

// movedRanks estimates where the moves of one sequence put the three
// movable actions.
type movedRanks struct {
	kept map[string][]*omap
	seen map[string]bool
	seq  string
}

// place returns the kept placement of an action in the sequence, or nil.
func (r movedRanks) place(name string) *omap {
	for _, placement := range r.kept[name] {
		if placement.str("sequence") == r.seq {
			return placement
		}
	}

	return nil
}

// rank estimates an action's place: its number, or just before or after
// its anchor; an unmoved LaunchConditions follows a moved AppSearch. ok is
// false for a place next to a custom action and for a cycle.
func (r movedRanks) rank(name string) (float64, bool) {
	if r.seen[name] {
		return 0, false
	}

	r.seen[name] = true
	defer delete(r.seen, name)

	placement := r.place(name)
	if placement == nil {
		if name == actLaunchConditions && r.place(actAppSearch) != nil {
			n, ok := r.rank(actAppSearch)

			return n + 0.5, ok
		}

		std, _ := msisequence.LookupStandard(name)

		return float64(std.Number(sequenceTable(r.seq))), true
	}

	if at, ok := placement.get("at").(int); ok {
		return float64(at), true
	}

	offset, anchor := 0.5, placement.str(keyAfter)
	if anchor == "" {
		offset, anchor = -0.5, placement.str(keyBefore)
	}

	if movableActions[anchor] {
		n, ok := r.rank(anchor)

		return n + offset, ok
	}

	std, ok := msisequence.LookupStandard(anchor)
	if n := std.Number(sequenceTable(r.seq)); ok && n != 0 {
		return float64(n) + offset, true
	}

	return 0, false
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
	reanchor, drop := c.reanchor(byAction)
	progress := c.progress()
	scheduled := c.scheduledBinaries(byAction)
	kept := c.convertActions(byAction, drop)

	for _, custom := range c.actionOrder {
		actionID := custom.attr("Id")

		action := kept[custom]
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

	c.emitMoved(reanchor)

	for _, id := range slices.Sorted(maps.Keys(byAction)) {
		if c.actions[id] == nil && id != c.launchAction {
			c.todof(nil, "", 0, "the sequences schedule %s, which this file does not define (an extension's action?)", id)
		}
	}

	c.leftoverProgress(progress)
}

// convertActions converts the custom actions convert keeps, before any is
// written, and hands the scheduled ones it leaves out to drop: a script, an
// extension's call or another action without a counterpart, and an action
// the sequences schedule that the file does not define and that is not a
// WiX extension's (extensionAnchors places those). Placements anchored
// on one of them then move to its anchor, so the output still builds.
func (c *converter) convertActions(byAction map[string][]any, drop func(string, *elem, []any)) map[*elem]*omap {
	kept := make(map[*elem]*omap)

	for _, custom := range c.actionOrder {
		if c.skipAction(custom, byAction) {
			continue
		}

		if action := c.action(custom); action != nil {
			kept[custom] = action
		} else if id := custom.attr("Id"); len(byAction[id]) > 0 {
			drop(id, custom, byAction[id])
		}
	}

	for _, id := range c.undefinedActions(byAction) {
		drop(id, nil, byAction[id])
	}

	return kept
}

// undefinedActions returns the actions the sequences schedule that the
// file defines neither as a custom action nor as a setter, leaving out the
// finish page's launch action and the actions of WiX extensions, which
// extensionAnchors places actions next to, keeping the order of those on
// both sides of one.
func (c *converter) undefinedActions(byAction map[string][]any) []string {
	setters := make(map[string]bool)
	for _, setter := range c.setters {
		setters[setterName(setter)] = true
	}

	var ids []string

	for _, id := range slices.Sorted(maps.Keys(byAction)) {
		if c.actions[id] == nil && id != c.launchAction && !setters[id] &&
			!extensionAction.MatchString(strings.TrimPrefix(id, "override ")) {
			ids = append(ids, id)
		}
	}

	return ids
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
	return c.anchorSides(byAction, extensionAction.MatchString)
}

// anchorSides returns the sides of the anchors match accepts that the
// source schedules actions on, as extensionSides describes.
func (c *converter) anchorSides(byAction map[string][]any, match func(string) bool) map[anchorSide]bool {
	sides := make(map[anchorSide]bool)

	for _, setter := range c.setters {
		for _, key := range []string{keyBefore, keyAfter} {
			if anchor := setter.attr(strings.ToUpper(key[:1]) + key[1:]); match(anchor) {
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
				if anchor := placement.str(key); match(anchor) {
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
		c.perMachineFolder(custom, custom.attr(attrProperty), byAction) || c.skipContextSetter(custom, byAction)
}

// skipSetter reports whether a SetProperty or SetDirectory is left out.
func (c *converter) skipSetter(setter *elem, byAction map[string][]any) bool {
	return c.consumed[setter] || setter.is("SetProperty") && c.perMachineFolder(setter, setter.attr("Id"), byAction) ||
		c.skipContextSetter(setter, byAction)
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
//
// The second function adds an action convert leaves out without making it
// part of another one (convertActions); a placement that moves off such an
// action gets a note, since the action it ran next to is gone.
func (c *converter) reanchor(byAction map[string][]any) (func(string, []any), func(string, *elem, []any)) {
	anchors, sources := c.sourceAnchors(byAction)
	dropped := make(map[string]*elem)

	var sides map[anchorSide]bool

	move := func(actionID string, entries []any) {
		if sides == nil {
			sides = c.anchorSides(byAction, func(name string) bool { return dropped[name] != nil })
		}

		for i, entry := range entries {
			if placement, ok := entry.(*omap); ok {
				entries[i] = c.moveOff(actionID, placement, dropped, sides, anchors, sources)
			}
		}
	}

	drop := func(actionID string, custom *elem, entries []any) {
		if sources[actionID] != nil {
			return
		}

		if custom == nil {
			custom = &elem{}
		}

		dropped[actionID] = custom
		sources[actionID] = custom

		for _, entry := range entries {
			if placement, ok := entry.(*omap); ok {
				key := anchorKey{actionID, placement.str("sequence")}
				if anchors[key] == nil {
					anchors[key] = placement
				}
			}
		}
	}

	return move, drop
}

// moveOff moves one placement off a left-out action: through followAnchors,
// or, next to a dropped action with actions on both of its sides, by
// pivotAnchor.
func (c *converter) moveOff(actionID string, placement *omap, dropped map[string]*elem, sides map[anchorSide]bool,
	anchors map[anchorKey]*omap, sources map[string]*elem,
) *omap {
	gone := placement.str(keyBefore) + placement.str(keyAfter)
	seq := placement.str("sequence")

	src, isDropped := dropped[gone]
	if isDropped && sides[anchorSide{gone, seq, keyBefore}] && sides[anchorSide{gone, seq, keyAfter}] {
		return c.pivotAnchor(actionID, placement, gone, src)
	}

	moved := c.followAnchors(actionID, placement, anchors, sources)
	if isDropped && moved != placement {
		c.todof(moved, "", src.line, "%s was scheduled next to %s, which convert left out; it now runs "+
			"where %s was scheduled, check that place", actionID, gone, gone)
	}

	return moved
}

// pivotAnchor places an action scheduled next to a left-out action that
// has actions on both of its sides: taking the left-out action's place
// would lose their order, so, as for a WiX extension's action, the
// placement keeps its side of one fallback anchor in the sequence.
func (c *converter) pivotAnchor(actionID string, placement *omap, gone string, src *elem) *omap {
	key := keyAfter
	if placement.str(keyBefore) != "" {
		key = keyBefore
	}

	// One anchor for every action around the left-out one: InstallFiles in
	// the execute sequence, where it keeps a deferred one in the script and
	// an immediate one may run too, CostFinalize in the UI sequence.
	target := fallbackAnchor(placement.str("sequence") == seqExecute)
	moved := moveAnchor(placement, flowMap().set(key, target))
	c.todof(moved, "", src.line, "%s was scheduled %s %s, which convert left out and which has actions on both sides; "+
		"convert placed it %s %s to keep their order; actions with the same place run in the order of customActions, so "+
		"check the order of all actions placed there", actionID, key, gone, key, target)

	return moved
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

	for _, src := range slices.Concat(c.consumedSources(), c.perMachineSources(), c.contextSources()) {
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

// setValue returns the value a setter gives the property name; an icon id
// for ARPPRODUCTICON is renamed as its <Icon> is.
func (c *converter) setValue(setter *elem, name string) string {
	if name == "ARPPRODUCTICON" {
		return c.iconRef(setter, "Value")
	}

	return c.value(setter, "Value")
}

// setterKind converts the actions that set a value or show an error.
func (c *converter) setterKind(action *omap, custom *elem) bool {
	switch {
	case custom.has("Error"):
		action.set("error", c.errorText(custom))
	case custom.has(attrProperty) && custom.has("Value"):
		action.set("setProperty", flowMap().set("name", custom.attr(attrProperty)).set("value", c.setValue(custom, custom.attr(attrProperty))))
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

		if dir := c.stdID(c.dirRef(custom.attr(attrDirectory))); !c.actionFolder(action, custom, dir) {
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

	if setter.is(elemSetDirectory) {
		action.set("setDirectory", flowMap().set("directory", c.dirRef(setterID)).set("value", c.value(setter, "Value")))

		if before == "" && after == "" {
			after = actCostFinalize
		}
	} else {
		action.set("setProperty", flowMap().set("name", setterID).set("value", c.setValue(setter, setterID)))
	}

	sequences := c.setterSequences(action, setter)
	schedule := make([]any, 0, len(sequences))

	for _, seq := range sequences {
		placement := flowMap().set("sequence", seq).set("before", before).set("after", after)
		c.setCondition(placement, c.condition(setter))
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
