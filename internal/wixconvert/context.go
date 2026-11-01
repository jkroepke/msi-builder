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

// notKeyword matches the NOT operator in any case; property names stay
// case-sensitive.
var notKeyword = regexp.MustCompile(`(?i)\bnot\b`)

// normalizeCondition collapses the spaces of a condition, removes them
// around = and <>, and writes NOT in upper case.
func normalizeCondition(cond string) string {
	cond = notKeyword.ReplaceAllString(strings.Join(strings.Fields(cond), " "), "NOT")

	return operatorSpace.ReplaceAllString(cond, "$1")
}

// contextCondition returns the scope a component condition limits the
// component to, or "".
func contextCondition(cond string) string {
	return contextConditions[normalizeCondition(cond)]
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
// there is none.
func (c *converter) choosesContext() int {
	line := 0

	var walk func(*elem)

	walk = func(elm *elem) {
		if line != 0 {
			return
		}

		switch {
		case elm.is("Component") && contextCondition(c.componentConditionText(elm)) != "":
			line = elm.line
		case contextSetter(elm) != "" && !c.machineSetter(elm):
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
// WiX v3 package without a scope and without ALLUSERS is per user too. A
// per-machine package stays: there a condition on ALLUSERS=1 always holds.
func (c *converter) contextScope(pkgOut *omap, scope string, allUsers bool) string {
	perUser := scope == scopeUser || c.v3 && scope == "" && !allUsers && !c.setsMachine()
	if !perUser {
		return scope
	}

	line := c.choosesContext()
	if line == 0 {
		return scope
	}

	c.todof(pkgOut, "scope", line, "the per-user package chooses its install context itself, with components "+
		"conditioned on ALLUSERS or MSIINSTALLPERUSER or actions that set them; it became scope %s, whose "+
		"components[].scope keeps machine and user data in their context, and ALLUSERS=1 or MSIINSTALLPERUSER on "+
		"the command line still picks the context (INST-07); use %s if the install should default to all users; "+
		"it needs installerVersion 5.0", scopeUserOrMachine, scopeMachineOrUser)

	return scopeUserOrMachine
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
// to their anchors.
func (c *converter) contextSources() []commandSource {
	if !c.dualScope() {
		return nil
	}

	var sources []commandSource

	for _, src := range c.commandSources() {
		if contextSetter(src.e) != "" {
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

// conditionedSetter reports whether a context setter runs only under a
// condition: its own (SetProperty) or one of a sequence entry that
// schedules it.
func (c *converter) conditionedSetter(action *elem) bool {
	if action.is("SetProperty") {
		return c.condition(action) != ""
	}

	conditioned := false
	id := action.attr("Id")

	var walk func(*elem)

	walk = func(elm *elem) {
		if elm.is("Custom") && elm.attr("Action") == id && c.condition(elm) != "" {
			conditioned = true
		}

		for _, kid := range elm.kids {
			walk(kid)
		}
	}

	walk(c.doc.root)

	return conditioned
}

// machineSetter reports whether an action always sets ALLUSERS=1, as WiX v3
// packages without InstallScope do to install per machine: that does not
// choose a context, it states the per-machine one.
func (c *converter) machineSetter(action *elem) bool {
	return contextSetter(action) == propAllUsers && c.setterValue(action) == "1" && !c.conditionedSetter(action)
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
