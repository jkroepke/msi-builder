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
	// problem says why the preselection could not be mapped.
	problem             string
	perMachine, perUser string
	// setters are the actions that set the dialog's property, and from
	// names those that run in the UI sequence, for the comments.
	setters []commandSource
	from    []string
	// drop is set when nothing but the dialog reads its property, so that
	// the setters and the property are left out.
	drop bool
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
// the scope dialog's property does: its default, and the actions that set
// it in the UI sequence before the dialog.
func (c *converter) mapScopePref(pref *scopePreference) {
	dlg := pref.dialog

	side := func(value string) string {
		switch value {
		case dlg.machine:
			return scopeMachine
		case dlg.user:
			return scopeUser
		default:
			return ""
		}
	}

	base := side(c.propertyValue(dlg.prop))
	if base == "" {
		pref.problem = "its property " + dlg.prop + " starts with neither " + strconv.Quote(dlg.machine) + " nor " + strconv.Quote(dlg.user)

		return
	}

	set, conds, ok := c.scopeSetters(pref, side)
	if !ok {
		return
	}

	cond := joinOr(conds)
	if len(cond) > maxScopeCondition {
		pref.problem = "the conditions of the actions that set " + dlg.prop + " are longer than 255 characters"

		return
	}

	initial := scopeUser
	if c.scope == scopeMachineOrUser {
		initial = scopeMachine
	}

	pref.decide(set, base, initial, cond)
	pref.drop = (pref.perMachine != "" || pref.perUser != "") && !c.readsElsewhere(pref)
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

// scopeSetters collects the actions that set the dialog's property in the
// UI sequence: the context they all choose and their conditions. An action
// that sets another value, or setters that choose both contexts, leave the
// preselection unmapped.
func (c *converter) scopeSetters(pref *scopePreference, side func(string) string) (string, []string, bool) {
	dlg := pref.dialog
	set := ""

	var conds []string

	for _, src := range c.commandSources() {
		if src.property != dlg.prop {
			continue
		}

		pref.setters = append(pref.setters, src)
		value := strings.TrimSpace(c.value(src.e, "Value"))

		uiConds, inUI := c.uiConditions(src)
		if !inUI {
			continue
		}

		chosen := side(value)
		if chosen == "" || set != "" && chosen != set {
			pref.problem = "the action " + src.id + " sets " + dlg.prop + " to " + strconv.Quote(value)
			if chosen != "" {
				pref.problem = "actions set " + dlg.prop + " to both " + strconv.Quote(dlg.machine) + " and " + strconv.Quote(dlg.user)
			}

			return "", nil, false
		}

		set = chosen

		pref.from = append(pref.from, src.id)
		conds = append(conds, uiConds...)
	}

	return set, conds, true
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
	word := regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(prop) + `([^\w.]|$)`)
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
		return
	}

	page := wizard.child("scopePage")
	page.order("perMachine", "perUser")
	page.set("perMachine", pref.perMachine)
	page.set("perUser", pref.perUser)

	what := "From the default of " + dlg.prop + ", the option the dialog " + dlg.e.attr("Id") + " preselects"
	if len(pref.from) > 0 {
		what += ", and " + strings.Join(pref.from, ", ") + ", which set it"
	}

	what += "."
	if pref.drop {
		what += " Convert left out " + dlg.prop
		if len(pref.from) > 0 {
			what += " and " + strings.Join(pref.from, ", ")
		}

		what += "."
	}

	for _, key := range []string{"perMachine", "perUser"} {
		if page.str(key) != "" {
			page.comment(key, what)

			break
		}
	}
}
