package wixconvert

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// scopeDialog is a hand-built dialog that picks the install context: a
// radio button group on a property whose Next publishes ALLUSERS or
// MSIINSTALLPERUSER by the property's value.
type scopeDialog struct {
	e *elem
	// prop is the property of the radio button group, machine and user
	// its values that choose all users and the current user.
	prop, machine, user string
}

// scopePreference is what convert makes of a scope dialog's preselection:
// the ui.scopePage conditions, the setters of the dialog's property that
// they replace, and whether those setters are left out.
type scopePreference struct {
	dialog *scopeDialog
	// before holds the setters that run before the dialog.
	before map[string]bool
	// problem says why the preselection could not be mapped.
	problem             string
	perMachine, perUser string
	// setters are the actions that set the dialog's property, and from
	// names those that run in the UI sequence, for the comments.
	setters []commandSource
	from    []string
	// drop is set when nothing but the dialog reads its property, so that
	// the setters and the property are left out; direct when the page
	// reads the property itself.
	drop, direct bool
}

// propertyIs matches a condition that compares a property with a quoted
// value, such as SCOPE = "machine".
var propertyIs = regexp.MustCompile(`^\s*([A-Za-z_][\w.]*)\s*=\s*"([^"]*)"\s*$`)

// scopeDialogOf reports whether dlg is a scope dialog and returns it.
func (c *converter) scopeDialogOf(dlg *elem) (*scopeDialog, bool) {
	machine := make(map[string]map[string]bool)
	user := make(map[string]map[string]bool)
	radios := make(map[string]bool)

	var walk func(*elem)

	walk = func(elm *elem) {
		if prop := elm.attr(attrProperty); prop != "" && (elm.is("RadioButtonGroup") || elm.is("Control") && elm.attr("Type") == "RadioButtonGroup") {
			radios[prop] = true
		}

		if elm.is("Publish") {
			c.scopePublish(elm, machine, user)
		}

		for _, kid := range elm.kids {
			walk(kid)
		}
	}

	walk(dlg)

	for _, prop := range slices.Sorted(maps.Keys(radios)) {
		machineValue, userValue, ok := onlyValues(machine[prop], user[prop])
		if ok {
			return &scopeDialog{e: dlg, prop: prop, machine: machineValue, user: userValue}, true
		}
	}

	return nil, false
}

// publishSides maps a Publish of ALLUSERS or MSIINSTALLPERUSER, by the
// property and whether it sets 1 or nothing, to the context it picks.
var publishSides = map[[2]string]string{ //nolint:gochecknoglobals // lookup table
	{propAllUsers, "1"}: scopeMachine, {propAllUsers, ""}: scopeUser,
	{propInstallPerUser, "1"}: scopeUser, {propInstallPerUser, ""}: scopeMachine,
}

// scopePublish records the value of the property a Publish of ALLUSERS or
// MSIINSTALLPERUSER tests, as a value that picks all users or the current
// user.
func (c *converter) scopePublish(publish *elem, machine, user map[string]map[string]bool) {
	match := propertyIs.FindStringSubmatch(c.condition(publish))
	if match == nil {
		return
	}

	value := strings.TrimSpace(c.value(publish, "Value"))
	if value == "{}" {
		value = ""
	}

	sides := map[string]map[string]map[string]bool{scopeMachine: machine, scopeUser: user}

	side := sides[publishSides[[2]string{publish.attr(attrProperty), value}]]
	if side == nil {
		return
	}

	if side[match[1]] == nil {
		side[match[1]] = make(map[string]bool)
	}

	side[match[1]][match[2]] = true
}

// onlyValues returns the one value that picks all users and the one that
// picks the current user, when there is exactly one of each and they differ.
func onlyValues(machine, user map[string]bool) (string, string, bool) {
	if len(machine) != 1 || len(user) != 1 {
		return "", "", false
	}

	machineValue, userValue := slices.Collect(maps.Keys(machine))[0], slices.Collect(maps.Keys(user))[0]

	return machineValue, userValue, machineValue != userValue
}

// isScopeDialog reports whether the dialog named id is a scope dialog.
func (c *converter) isScopeDialog(id string) bool {
	return slices.ContainsFunc(c.scopeDialogs, func(d *scopeDialog) bool { return d.e.attr("Id") == id })
}

// scopePref maps the preselection of the package's scope dialog, once.
func (c *converter) scopePref() *scopePreference {
	if c.scopePrefDone {
		return c.scopePrefResult
	}

	c.scopePrefDone = true

	if len(c.scopeDialogs) != 1 || !c.dualScope() || c.uiID() == "" {
		return nil
	}

	pref := &scopePreference{dialog: c.scopeDialogs[0]}
	c.scopePrefResult = pref
	c.mapScopePref(pref)

	return pref
}

// mapScopePref works out the ui.scopePage conditions that preselect what
// the scope dialog's property does. When only the dialog reads the
// property, the conditions of the actions that set it before the dialog
// stand in for them (mapConditions) and they are left out; otherwise they
// stay and the page reads the property itself (mapProperty).
func (c *converter) mapScopePref(pref *scopePreference) {
	c.scopeSetters(pref)

	if pref.problem != "" {
		return
	}

	if !c.keepsProperty(pref) && c.mapConditions(pref) {
		pref.drop = true

		return
	}

	c.mapProperty(pref)
}

// mapProperty preselects each option when the dialog's property holds its
// value: its default, its search and the actions that set it all run
// before the page, after AppSearch or right after it.
func (c *converter) mapProperty(pref *scopePreference) {
	dlg := pref.dialog
	pref.direct = true
	pref.perMachine = dlg.prop + " = " + strconv.Quote(dlg.machine)
	pref.perUser = dlg.prop + " = " + strconv.Quote(dlg.user)

	if len(pref.perMachine) > maxScopeCondition || len(pref.perUser) > maxScopeCondition {
		pref.problem = "the conditions that test " + dlg.prop + " are longer than 255 characters"
	}
}

// keepsProperty reports whether the dialog's property must stay: something
// else names it, a search sets it, or an action that sets it reads it.
func (c *converter) keepsProperty(pref *scopePreference) bool {
	prop := pref.dialog.prop
	word := propertyWord(prop)

	for _, elm := range c.properties {
		if elm.attr("Id") == prop && len(elm.kids) > 0 {
			return true
		}
	}

	for _, src := range pref.setters {
		if word.MatchString(c.value(src.e, "Value")) || slices.ContainsFunc(c.uiConditionsOf(src), word.MatchString) {
			return true
		}
	}

	return c.readsElsewhere(pref)
}

// mapConditions sets the conditions from the property's default and the
// conditions of the actions that set it before the dialog. It reports
// false when they do not map: a default or a value that is neither
// option, actions that choose both, or a condition that gets too long.
func (c *converter) mapConditions(pref *scopePreference) bool {
	base := pref.dialog.side(c.propertyValue(pref.dialog.prop))
	if base == "" {
		return false
	}

	set, conds, ok := c.setterChoice(pref)
	if !ok {
		return false
	}

	initial := scopeUser
	if c.scope == scopeMachineOrUser {
		initial = scopeMachine
	}

	pref.decide(set, base, initial, joinOr(conds))

	if len(pref.perMachine) > maxScopeCondition || len(pref.perUser) > maxScopeCondition {
		pref.perMachine, pref.perUser = "", ""

		return false
	}

	return true
}

// side returns the context a value of the dialog's property picks, or "".
func (dlg *scopeDialog) side(value string) string {
	switch value {
	case dlg.machine:
		return scopeMachine
	case dlg.user:
		return scopeUser
	default:
		return ""
	}
}

// setterChoice returns the context the setters before the dialog choose
// and their conditions, or false when one sets another value or they
// choose both.
func (c *converter) setterChoice(pref *scopePreference) (string, []string, bool) {
	set := ""

	var conds []string

	for _, src := range pref.setters {
		if !pref.before[src.id] {
			continue
		}

		chosen := pref.dialog.side(strings.TrimSpace(c.value(src.e, "Value")))
		if chosen == "" || set != "" && chosen != set {
			return "", nil, false
		}

		set = chosen

		conds = append(conds, c.uiConditionsOf(src)...)
	}

	return set, conds, true
}

// decide sets the conditions from the context the setters choose under
// cond, the one the property starts with and the one the page starts on.
func (pref *scopePreference) decide(set, base, initial, cond string) {
	switch set {
	case "", base:
		// The setters change nothing: the property's default stands.
		if base != initial {
			pref.setAlways(base)
		}
	case scopeMachine:
		pref.perMachine = cond
		if base != initial {
			pref.perUser = "1"
		}
	default:
		pref.perUser = cond
		if base != initial {
			pref.perMachine = "NOT (" + cond + ")"
		}
	}
}

// maxScopeCondition is the longest condition ui.scopePage takes.
const maxScopeCondition = 255

// setAlways preselects one context whatever the context the package opened in.
func (pref *scopePreference) setAlways(side string) {
	if side == scopeMachine {
		pref.perMachine = "1"
	} else {
		pref.perUser = "1"
	}
}

// scopeSetters collects the actions that set the dialog's property and
// records those that run in the UI sequence before the dialog. One whose
// place convert cannot tell leaves the preselection unmapped: the page
// runs after AppSearch and the actions placed after it.
func (c *converter) scopeSetters(pref *scopePreference) {
	dlg := pref.dialog
	anchors := c.uiAnchors()
	pref.before = make(map[string]bool)

	for _, src := range c.commandSources() {
		if src.property != dlg.prop {
			continue
		}

		pref.setters = append(pref.setters, src)

		if _, inUI := c.uiConditions(src); !inUI {
			continue
		}

		switch beforeDialog(anchors, src.id, dlg.e.attr("Id")) {
		case placeBefore:
			pref.before[src.id] = true
			pref.from = append(pref.from, src.id)
		case placeAfter:
			// The dialog has shown its choice by then.
		default:
			pref.problem = "the place of the action " + src.id + ", which sets " + dlg.prop +
				", does not lead to the dialog or AppSearch; the scope page reads its preselection right after AppSearch"

			return
		}
	}
}

// uiConditionsOf returns the conditions under which an action runs in the
// UI sequence.
func (c *converter) uiConditionsOf(src commandSource) []string {
	conds, _ := c.uiConditions(src)

	return conds
}

// uiAnchor is the place of an entry of the UI sequence: before or after
// another one.
type uiAnchor struct{ key, target string }

// uiAnchors returns the place of each action and dialog of the UI
// sequence that is placed relative to another one.
func (c *converter) uiAnchors() map[string]uiAnchor {
	anchors := make(map[string]uiAnchor)
	place := func(name string, elm *elem) {
		switch {
		case elm.attr("Before") != "":
			anchors[name] = uiAnchor{keyBefore, elm.attr("Before")}
		case elm.attr("After") != "":
			anchors[name] = uiAnchor{keyAfter, elm.attr("After")}
		}
	}

	for _, setter := range c.setters {
		if setter.is("SetProperty") && slices.Contains(setterSequenceNames(setter), seqUI) {
			place(setterName(setter), setter)
		}
	}

	for _, entry := range c.uiEntries() {
		switch {
		case entry.is("Custom"):
			place(entry.attr("Action"), entry)
		case entry.is("Show"):
			place(entry.attr("Dialog"), entry)
		}
	}

	return anchors
}

// uiEntries returns the entries of the InstallUISequence elements.
func (c *converter) uiEntries() []*elem {
	var entries []*elem

	for _, seq := range c.sequences {
		if sequenceNames[seq.name] == seqUI {
			entries = append(entries, seq.kids...)
		}
	}

	return entries
}

// The places of a setter relative to the scope dialog.
const (
	placeUnknown = iota
	placeBefore
	placeAfter
)

// beforeDialog follows an action's anchors in the UI sequence to the
// dialog or to AppSearch: an action next to AppSearch, or before the
// dialog, runs before the scope page as before the dialog.
func beforeDialog(anchors map[string]uiAnchor, action, dialog string) int {
	for range len(anchors) + 1 {
		anchor, ok := anchors[action]

		switch {
		case !ok:
			return placeUnknown
		case anchor.target == dialog && anchor.key == keyBefore:
			return placeBefore
		case anchor.target == dialog:
			return placeAfter
		case anchor.target == "AppSearch":
			return placeBefore
		}

		action = anchor.target
	}

	return placeUnknown
}

// uiConditions returns the conditions under which an action runs in the UI
// sequence, and whether it runs there at all.
func (c *converter) uiConditions(src commandSource) ([]string, bool) {
	if src.e.is("SetProperty") {
		if !slices.Contains(setterSequenceNames(src.e), seqUI) {
			return nil, false
		}

		return []string{c.condition(src.e)}, true
	}

	var conds []string

	inUI := false

	for _, seq := range c.sequences {
		if sequenceNames[seq.name] != seqUI {
			continue
		}

		for _, entry := range seq.kids {
			if entry.is("Custom") && entry.attr("Action") == src.id {
				inUI = true

				conds = append(conds, c.condition(entry))
			}
		}
	}

	return conds, inUI
}

// joinOr joins conditions with OR; an empty one always holds.
func joinOr(conds []string) string {
	parts := make([]string, 0, len(conds))

	for _, cond := range conds {
		cond = strings.TrimSpace(cond)
		if cond == "" || cond == "1" {
			return "1"
		}

		if !slices.Contains(parts, cond) {
			parts = append(parts, cond)
		}
	}

	if len(parts) == 1 {
		return parts[0]
	}

	for i, part := range parts {
		parts[i] = "(" + part + ")"
	}

	return strings.Join(parts, " OR ")
}

// readsElsewhere reports whether anything but the scope dialog, the
// property's own definition and its setters names the dialog's property.
func (c *converter) readsElsewhere(pref *scopePreference) bool {
	prop := pref.dialog.prop
	word := propertyWord(prop)
	skip := map[*elem]bool{pref.dialog.e: true}

	for _, src := range pref.setters {
		skip[src.e] = true
	}

	var walk func(*elem) bool

	walk = func(elm *elem) bool {
		if skip[elm] || elm.is("Property") && elm.attr("Id") == prop && len(elm.kids) == 0 ||
			elm.is("RadioButtonGroup") && elm.attr(attrProperty) == prop {
			return false
		}

		return names(elm, word) || slices.ContainsFunc(elm.kids, walk)
	}

	return walk(c.doc.root)
}

// propertyWord matches a property's name as a whole word.
func propertyWord(prop string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(prop) + `([^\w.]|$)`)
}

// names reports whether the text or an attribute of elm matches word.
func names(elm *elem, word *regexp.Regexp) bool {
	if word.MatchString(elm.text) {
		return true
	}

	for _, value := range elm.attrs {
		if word.MatchString(value) {
			return true
		}
	}

	return false
}

// skipScopeSetter leaves out a setter of the scope dialog's property that
// ui.scopePage replaces.
func (c *converter) skipScopeSetter(action *elem, byAction map[string][]any) bool {
	pref := c.scopePref()
	if pref == nil || !pref.drop || pref.problem != "" {
		return false
	}

	for _, src := range pref.setters {
		if src.e == action {
			delete(byAction, src.id)

			return true
		}
	}

	return false
}

// scopeSources returns the setters skipScopeSetter leaves out, so that
// actions placed on them move to their anchors.
func (c *converter) scopeSources() []commandSource {
	if pref := c.scopePref(); pref != nil && pref.drop && pref.problem == "" {
		return pref.setters
	}

	return nil
}

// skippedScopeProperty reports whether propID is the scope dialog's
// property, which nothing reads once ui.scopePage replaces the dialog.
func (c *converter) skippedScopeProperty(propID string) bool {
	pref := c.scopePref()

	return pref != nil && pref.drop && pref.problem == "" && propID == pref.dialog.prop
}

// uiScopePage writes ui.scopePage from the scope dialog's preselection, or
// a todo that says why it could not.
func (c *converter) uiScopePage(wizard *omap) {
	pref := c.scopePref()
	if pref == nil {
		return
	}

	dlg := pref.dialog

	if pref.problem != "" {
		c.todof(wizard, "", dlg.e.line, "the scope dialog %s preselects what %s says, which convert could not map because %s; "+
			"set ui.scopePage.perMachine and perUser to the conditions that preselect each option", dlg.e.attr("Id"), dlg.prop, pref.problem)

		return
	}

	if pref.perMachine == "" && pref.perUser == "" {
		if pref.drop {
			wizard.comment("enabled", "Convert left out "+dlg.prop+pref.droppedSetters()+": only the dialog "+dlg.e.attr("Id")+
				" read it, and the scope page, which takes its place, starts on the same option.")
		}

		return
	}

	page := wizard.child("scopePage")
	page.order("perMachine", "perUser")
	page.set("perMachine", pref.perMachine)
	page.set("perUser", pref.perUser)

	what := pref.comment()

	for _, key := range []string{"perMachine", "perUser"} {
		if page.str(key) != "" {
			page.comment(key, what)

			break
		}
	}
}

// droppedSetters names the setters left out with the dialog's property.
func (pref *scopePreference) droppedSetters() string {
	if len(pref.setters) == 0 {
		return ""
	}

	ids := make([]string, 0, len(pref.setters))
	for _, src := range pref.setters {
		ids = append(ids, src.id)
	}

	return " and " + strings.Join(ids, ", ")
}

// comment says where the ui.scopePage conditions come from.
func (pref *scopePreference) comment() string {
	what := "From the default of " + pref.dialog.prop + ", the option the dialog " + pref.dialog.e.attr("Id") + " preselects"
	if len(pref.from) > 0 {
		what += ", and " + strings.Join(pref.from, ", ") + ", which set it before the dialog"
	}

	what += "."

	switch {
	case pref.direct:
		what += " " + pref.dialog.prop + " and the actions that set it stay; they run before the scope page."
	case pref.drop:
		what += " Convert left out " + pref.dialog.prop + pref.droppedSetters() + ", so msiexec " + pref.dialog.prop + "=... no longer preselects an option."
	}

	return what
}
