package wixconvert

import (
	"regexp"
	"strings"
)

// A WiX package can choose its install context without ALLUSERS=2: a
// per-user package that a bootstrapper installs per machine with
// ALLUSERS=1, or one that sets ALLUSERS and MSIINSTALLPERUSER with its own
// actions and conditions components on them. msi-builder's form of such a
// package is a dual scope, whose components[].scope keeps the machine and
// user data in their context (docs/spec/MSISPEC.md chapter 16, INST-07).

// contextConditions are the component conditions that install a component
// in one context only, normalized by normalizeCondition: ALLUSERS is 1 per
// machine and unset per user, and MSIINSTALLPERUSER, which only an
// ALLUSERS=2 package reads, is 1 for a per-user install (INST-07).
var contextConditions = map[string]string{ //nolint:gochecknoglobals // lookup table
	propAllUsers:              scopeMachine,
	"ALLUSERS=1":              scopeMachine,
	`ALLUSERS="1"`:            scopeMachine,
	"NOT ALLUSERS":            scopeUser,
	"NOT ALLUSERS=1":          scopeUser,
	`ALLUSERS=""`:             scopeUser,
	"ALLUSERS<>1":             scopeUser,
	"MSIINSTALLPERUSER=1":     scopeUser,
	`MSIINSTALLPERUSER="1"`:   scopeUser,
	"MSIINSTALLPERUSER":       scopeUser,
	"MSIINSTALLPERUSER<>1":    scopeMachine,
	`MSIINSTALLPERUSER<>"1"`:  scopeMachine,
	"NOT MSIINSTALLPERUSER":   scopeMachine,
	`MSIINSTALLPERUSER=""`:    scopeMachine,
	"NOT MSIINSTALLPERUSER=1": scopeMachine,
}

// operatorSpace matches the spaces around a comparison operator.
var operatorSpace = regexp.MustCompile(`\s*(<>|=)\s*`)

// logicKeyword matches the NOT, AND and OR operators in any case; property
// names stay case-sensitive.
var logicKeyword = regexp.MustCompile(`(?i)\b(not|and|or)\b`)

// normalizeCondition collapses the spaces of a condition, removes them
// around = and <>, and writes NOT, AND and OR in upper case.
func normalizeCondition(cond string) string {
	cond = logicKeyword.ReplaceAllStringFunc(strings.Join(strings.Fields(cond), " "), strings.ToUpper)

	return operatorSpace.ReplaceAllString(cond, "$1")
}

// contextCondition returns the scope a component condition limits the
// component to, or "". Besides the conditions in contextConditions, it
// reads them in parentheses, negated with NOT (...), and joined with AND or
// OR when every part limits to the same scope:
// "ALLUSERS <> 1 AND MSIINSTALLPERUSER = 1" is per user, and its negation
// per machine.
func contextCondition(cond string) string {
	return normalizedContext(normalizeCondition(cond))
}

func normalizedContext(cond string) string {
	cond = trimParens(strings.TrimSpace(cond))
	if scope, ok := contextConditions[cond]; ok {
		return scope
	}

	if inner, ok := strings.CutPrefix(cond, "NOT"); ok {
		if inner = strings.TrimSpace(inner); strings.HasPrefix(inner, "(") && trimParens(inner) != inner {
			return oppositeScope(normalizedContext(inner))
		}
	}

	for _, operator := range []string{" AND ", " OR "} {
		if parts := splitTopLevel(cond, operator); len(parts) > 1 {
			return commonContext(parts)
		}
	}

	return ""
}

// oppositeScope returns the other context of a scope, or "".
func oppositeScope(scope string) string {
	switch scope {
	case scopeMachine:
		return scopeUser
	case scopeUser:
		return scopeMachine
	}

	return ""
}

// commonContext returns the context every part limits to, or "".
func commonContext(parts []string) string {
	scope := normalizedContext(parts[0])
	for _, part := range parts[1:] {
		if normalizedContext(part) != scope {
			return ""
		}
	}

	return scope
}

// trimParens removes parentheses that enclose the whole condition.
func trimParens(cond string) string {
	for strings.HasPrefix(cond, "(") && strings.HasSuffix(cond, ")") && closingParen(cond) == len(cond)-1 {
		cond = strings.TrimSpace(cond[1 : len(cond)-1])
	}

	return cond
}

// closingParen returns the index of the parenthesis that closes the one
// cond starts with, or -1.
func closingParen(cond string) int {
	depth := 0

	for i, r := range cond {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// splitTopLevel splits cond at op outside parentheses. A condition with a
// string literal is not split, since the literal could hold either.
func splitTopLevel(cond, operator string) []string {
	if strings.Contains(cond, `"`) {
		return []string{cond}
	}

	var (
		parts []string
		depth int
		start int
	)

	for i := 0; i < len(cond); i++ {
		switch {
		case cond[i] == '(':
			depth++
		case cond[i] == ')':
			depth--
		case depth == 0 && strings.HasPrefix(cond[i:], operator):
			parts = append(parts, cond[start:i])
			start = i + len(operator)
			i += len(operator) - 1
		}
	}

	return append(parts, cond[start:])
}

// componentConditionText returns a component's condition: its Condition
// attribute, or in WiX v3 the text of its Condition child.
func (c *converter) componentConditionText(comp *elem) string {
	if comp.has(elemCondition) {
		return c.value(comp, elemCondition)
	}

	for _, kid := range comp.kids {
		if kid.is(elemCondition) {
			return c.expand(kid, strings.TrimSpace(kid.text))
		}
	}

	return ""
}

// contextProperty reports whether name is a property that picks the
// install context.
func contextProperty(name string) bool {
	return name == propAllUsers || name == propInstallPerUser
}

// contextSetter returns the property a SetProperty element or a type 51
// CustomAction sets when it picks the install context, or "".
func contextSetter(action *elem) string {
	switch {
	case action.is("SetProperty") && contextProperty(action.attr("Id")):
		return action.attr("Id")
	case action.is("CustomAction") && contextProperty(action.attr(attrProperty)) && action.has("Value"):
		return action.attr(attrProperty)
	}

	return ""
}

// choosesContext returns the line of the first sign that a package which
// is not dual chooses its context itself: a component conditioned on the
// context, or an action that sets ALLUSERS or MSIINSTALLPERUSER; 0 when
// there is none. With machineOK, a setter that always sets ALLUSERS=1 does
// not count: in a WiX v3 package without a scope it states per machine.
func (c *converter) choosesContext(machineOK bool) int {
	line := 0

	var walk func(*elem)

	walk = func(elm *elem) {
		if line != 0 {
			return
		}

		switch {
		case elm.is("Component") && contextCondition(c.componentConditionText(elm)) != "":
			line = elm.line
		case contextSetter(elm) != "" && (!machineOK || !c.machineSetter(elm)):
			line = elm.line
		}

		for _, kid := range elm.kids {
			walk(kid)
		}
	}

	walk(c.doc.root)

	return line
}

// contextScope makes a per-user package that chooses its context itself
// the dual scope perUserOrMachine, which still installs per user by
// default and per machine with a bootstrapper's ALLUSERS=1 (INST-07). A
// WiX v3 package without a scope and without ALLUSERS is per user too, but
// only one that reads MSIINSTALLPERUSER becomes dual, and Convert keeps it
// perMachine when the dual package does not validate. A
// per-machine package stays: there a condition on ALLUSERS=1 always holds.
func (c *converter) contextScope(pkgOut *omap, scope string, allUsers bool) string {
	// A WiX v3 package without a scope converts to perMachine, as a
	// bootstrapper installs it, whatever it does with ALLUSERS; only
	// MSIINSTALLPERUSER, which nothing but an ALLUSERS=2 package reads,
	// shows that it chooses its context itself.
	perUser := scope == scopeUser || c.v3 && scope == "" && !allUsers && c.readsInstallPerUser() && !c.noDual
	if !perUser {
		return scope
	}

	line := c.choosesContext(scope == "")
	if line == 0 {
		return scope
	}

	// An ALLUSERS=1 row makes the install per machine by default, which
	// perMachineOrUser keeps.
	dual, other, otherDefault := scopeUserOrMachine, scopeMachineOrUser, "all users"
	if c.propertyValue(propAllUsers) == "1" {
		dual, other, otherDefault = scopeMachineOrUser, scopeUserOrMachine, "the installing user"
	}

	c.todof(pkgOut, "scope", line, "the package chooses its install context itself, with components "+
		"conditioned on ALLUSERS or MSIINSTALLPERUSER or actions that set them; it became scope %s, whose "+
		"components[].scope keeps machine and user data in their context, and ALLUSERS=1 or MSIINSTALLPERUSER on "+
		"the command line still picks the context (INST-07); use %s if the install should default to %s; "+
		"it needs installerVersion 5.0", dual, other, otherDefault)

	c.guessedDual = scope == ""

	return dual
}

// skipContextSetter leaves out an action that sets ALLUSERS or
// MSIINSTALLPERUSER in a dual package, whose scope page and command line
// choose the context.
func (c *converter) skipContextSetter(action *elem, byAction map[string][]any) bool {
	property := contextSetter(action)
	if property == "" {
		return false
	}

	if !c.dualScope() {
		return c.skipMachineSetter(action, byAction)
	}

	actionID := action.attr("Id")
	if action.is("SetProperty") {
		actionID = setterName(action)
	}

	delete(byAction, actionID)

	c.todof(nil, "", action.line, "the action %s, which sets %s, was left out: a %s package sets ALLUSERS and "+
		"MSIINSTALLPERUSER itself, and the scope page or the command line picks the context; components[].scope "+
		"keeps components in one context (INST-07)", actionID, property, c.scope)

	return true
}

// contextSources returns the setters of ALLUSERS and MSIINSTALLPERUSER
// that skipContextSetter leaves out, so that actions placed on them move
// to their anchors: all of them in a dual package, the ALLUSERS=1 setters
// that always run in a perMachine one.
func (c *converter) contextSources() []commandSource {
	dual := c.dualScope()
	if !dual && c.scope != "" && c.scope != scopeMachine {
		return nil
	}

	var sources []commandSource

	for _, src := range c.commandSources() {
		if contextSetter(src.e) != "" && (dual || c.machineSetter(src.e)) {
			sources = append(sources, src)
		}
	}

	return sources
}

// setterValue returns the value an ALLUSERS or MSIINSTALLPERUSER setter
// sets.
func (c *converter) setterValue(action *elem) string {
	return c.value(action, "Value")
}

// firstInstallConditions are the setter conditions that hold in every
// first install, normalized by normalizeCondition; a later maintenance
// install keeps the context the product was registered in.
var firstInstallConditions = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"":                             true,
	"1":                            true,
	"NOT Installed":                true,
	"NOT Installed AND NOT REMOVE": true,
	"NOT REMOVE AND NOT Installed": true,
	"NOT Installed AND NOT UPGRADINGPRODUCTCODE": true,
}

// firstInstallCondition reports whether a setter's condition holds in
// every first install. Any other condition, such as a public property of
// the package, lets the install choose the context.
func firstInstallCondition(cond string) bool {
	return firstInstallConditions[normalizeCondition(cond)]
}

// alwaysRuns reports whether a context setter runs in every first install:
// a SetProperty outside the UI sequence, or a CustomAction that
// InstallExecuteSequence schedules, without a condition or with one of
// firstInstallConditions.
func (c *converter) alwaysRuns(action *elem) bool {
	if action.is("SetProperty") {
		return firstInstallCondition(c.condition(action)) && action.attr("Sequence") != "ui"
	}

	runs := false
	actionID := action.attr("Id")

	var walk func(*elem, bool)

	walk = func(elm *elem, execute bool) {
		execute = execute || elm.is("InstallExecuteSequence")
		if execute && elm.is("Custom") && elm.attr("Action") == actionID && firstInstallCondition(c.condition(elm)) {
			runs = true
		}

		for _, kid := range elm.kids {
			walk(kid, execute)
		}
	}

	walk(c.doc.root, false)

	return runs
}

// machineSetter reports whether an action always sets ALLUSERS=1, as WiX v3
// packages without InstallScope do to install per machine: that does not
// choose a context, it states the per-machine one.
func (c *converter) machineSetter(action *elem) bool {
	return contextSetter(action) == propAllUsers && c.setterValue(action) == "1" && c.alwaysRuns(action)
}

// skipMachineSetter leaves out an unconditioned ALLUSERS=1 setter of a
// package that converts to perMachine, which writes ALLUSERS=1 itself.
func (c *converter) skipMachineSetter(action *elem, byAction map[string][]any) bool {
	if c.scope != "" && c.scope != scopeMachine || !c.machineSetter(action) {
		return false
	}

	actionID := action.attr("Id")
	if action.is("SetProperty") {
		actionID = setterName(action)
	}

	delete(byAction, actionID)

	c.todof(nil, "", action.line, "the action %s, which always sets ALLUSERS=1, was left out: a perMachine package "+
		"writes ALLUSERS=1 itself", actionID)

	return true
}

// setsMachine reports whether the file has an unconditioned ALLUSERS=1
// setter.
func (c *converter) setsMachine() bool {
	found := false

	var walk func(*elem)

	walk = func(elm *elem) {
		if contextSetter(elm) != "" && c.machineSetter(elm) {
			found = true
		}

		for _, kid := range elm.kids {
			walk(kid)
		}
	}

	walk(c.doc.root)

	return found
}

// readsInstallPerUser reports whether the file sets MSIINSTALLPERUSER or
// conditions a component or an action on it.
func (c *converter) readsInstallPerUser() bool {
	found := false

	var walk func(*elem)

	walk = func(elm *elem) {
		switch {
		case contextSetter(elm) == propInstallPerUser,
			elm.is("Component") && strings.Contains(c.componentConditionText(elm), propInstallPerUser),
			(elm.is("SetProperty") || elm.is("Custom")) && strings.Contains(c.condition(elm), propInstallPerUser):
			found = true
		}

		for _, kid := range elm.kids {
			walk(kid)
		}
	}

	walk(c.doc.root)

	return found
}
