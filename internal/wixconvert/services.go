package wixconvert

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

func (cc *compConv) service(elm *elem) { //nolint:cyclop // a flat list of independent WiX attributes
	conv := cc.c
	svc := newMap()
	svc.order("name", "displayName", "description", "type", "start", "delayedAutoStart", "errorControl", "vital",
		"account", "password", "arguments", "dependencies", "failureActionsOnNonCrashFailures", "control", "recovery", "permissions")
	svc.head = comments(elm)
	svc.set("name", conv.value(elm, "Name"))
	svc.set("displayName", conv.value(elm, "DisplayName"))
	svc.set("description", conv.value(elm, "Description"))

	switch t := elm.attr("Type"); t {
	case "", "ownProcess":
	case "shareProcess":
		svc.set("type", t)
	default:
		conv.todof(svc, "", elm.line, "service Type=%q has no counterpart; drivers are not supported", t)
	}

	switch st := elm.attr("Start"); st {
	case "auto", "demand", "disabled":
		svc.set("start", st)
	case "":
	default:
		conv.todof(svc, "", elm.line, "service Start=%q has no counterpart", st)
	}

	switch ec := elm.attr("ErrorControl"); ec {
	case "", "normal":
	case "ignore", "critical":
		svc.set("errorControl", ec)
	default:
		conv.todof(svc, "", elm.line, "service ErrorControl=%q has no counterpart", ec)
	}

	if elm.yes("Vital") {
		svc.set("vital", true)
	}

	if a := conv.value(elm, "Account"); !strings.EqualFold(a, "LocalSystem") {
		svc.set("account", a)
	}

	svc.set("password", conv.value(elm, "Password"))
	svc.set("arguments", conv.value(elm, "Arguments"))

	for _, a := range []string{"LoadOrderGroup", "Interactive"} {
		if elm.has(a) && !elm.no(a) {
			conv.todof(svc, "", elm.line, "service %s has no counterpart", a)
		}
	}

	for _, k := range elm.kids {
		cc.serviceChild(svc, k)
	}

	cc.services = append(cc.services, svc)
	cc.serviceLines = append(cc.serviceLines, elm.line)
	cc.out.add("services", svc)
}

func (cc *compConv) serviceChild(svc *omap, kid *elem) { //nolint:cyclop // one branch per child element
	conv := cc.c

	switch kid.qname() {
	case "ServiceDependency":
		if kid.yes("Group") {
			conv.todof(svc, "", kid.line, "a dependency on a load order group has no counterpart")

			return
		}

		deps := svc.list("dependencies")
		svc.set("dependencies", append(deps, conv.value(kid, "Id")))
	case "ServiceConfig":
		if kid.yes("DelayedAutoStart") {
			svc.set("delayedAutoStart", true)
		}

		if kid.attr("FailureActionsWhen") == "failedToStopOrReturnedError" {
			svc.set("failureActionsOnNonCrashFailures", true)
		}

		for _, a := range []string{"PreShutdownDelay", "ServiceSid", "RequiredPrivilege"} {
			if kid.has(a) {
				conv.todof(svc, "", kid.line, "ServiceConfig %s has no counterpart", a)
			}
		}
	case "util:ServiceConfig":
		conv.recovery(svc, kid)
	default:
		if permissionElems[kid.qname()] {
			conv.permission(svc, "permissions", kid, rightsService)

			return
		}

		conv.todof(svc, "", kid.line, "<%s> in a ServiceInstall has no counterpart", kid.qname())
	}
}

func (c *converter) recovery(svc *omap, elm *elem) {
	recovery := newMap()

	if days, err := strconv.Atoi(elm.attr("ResetPeriodInDays")); err == nil {
		// The reset period is a count of seconds that msi-builder keeps
		// within a signed 32-bit number.
		if maxDays := math.MaxInt32 / (24 * 60 * 60); days > maxDays {
			c.todof(recovery, "resetAfterDays", elm.line, "ResetPeriodInDays=%d is above %d days, the most resetAfterDays takes; "+
				"convert set %d", days, maxDays, maxDays)
			days = maxDays
		}

		recovery.set("resetAfterDays", days)
	}

	var actions []string

	for _, a := range []string{"FirstFailureActionType", "SecondFailureActionType", "ThirdFailureActionType"} {
		switch v := elm.attr(a); v {
		case "":
		case "restart", valueNone:
			actions = append(actions, v)
		default:
			c.todof(svc, "recovery", elm.line, "the recovery action %q has no counterpart; restart and none are supported", v)

			actions = append(actions, "none")
		}
	}

	recovery.set("actions", actions)

	// Without the attribute WiX restarts at once, while msi-builder
	// waits a minute by default.
	delay := elm.attr("RestartServiceDelayInSeconds")
	if delay == "" {
		delay = "0"
	}

	recovery.set("restartDelay", delay+"s")

	for _, a := range []string{"ProgramCommandLine", "RebootMessage"} {
		if elm.has(a) {
			c.todof(svc, "recovery", elm.line, "util:ServiceConfig %s has no counterpart", a)
		}
	}

	svc.set("recovery", recovery)
}

// resolveControls converts the ServiceControls of the component. One of a
// service that the component installs becomes its control. One of a
// service that other components of the package install becomes the
// control of those without a ServiceControl of their own, which msi-builder
// requires, or is left out with a note.
func (cc *compConv) resolveControls() {
	conv := cc.c

	for _, elm := range cc.controls {
		name := conv.value(elm, "Name")

		var own *omap

		for _, s := range cc.services {
			if strings.EqualFold(s.str("name"), name) {
				own = s
			}
		}

		switch {
		case own != nil && !cc.controlled[own]:
			// A component that installs the service keeps its own
			// control, also when another component installs it too.
			cc.ownControl(own, elm)
		case own != nil:
			conv.todof(cc.out, "", elm.line, "the ServiceControl for %s was left out: msi-builder takes one control per "+
				"service of a component; merge the events into the first one", name)
		default:
			cc.otherComponentControl(elm, name)
		}
	}

	cc.otherControls()
}

// otherComponentControl converts a ServiceControl of a service that the
// component does not install.
func (cc *compConv) otherComponentControl(elm *elem, name string) {
	conv := cc.c
	withOwn, without := conv.serviceInstallers(name)

	switch {
	case len(withOwn)+len(without) == 0:
		cc.foreignControl(elm)
	case elm == conv.borrowedControl(name) && len(without) > 0:
		msg := "the ServiceControl for %s became services[].control of %s, which keeps that component's condition " +
			"and feature instead of this one's"
		if len(withOwn) > 0 {
			msg += "; merge its events into the control of " + conv.compNames(withOwn) + " too"
		}

		conv.todof(cc.out, "", elm.line, msg, name, conv.compNames(without))
	default:
		conv.todof(cc.out, "", elm.line, "the ServiceControl for %s was left out: msi-builder takes one control per "+
			"service; merge the events into services[].control of %s", name, conv.compNames(append(withOwn, without...)))
	}
}

// otherControls gives the services without a ServiceControl in their own
// component the one of another component, or notes msi-builder's default.
func (cc *compConv) otherControls() {
	for i, svc := range cc.services {
		if cc.controlled[svc] {
			continue
		}

		name := svc.str("name")
		if elm := cc.c.borrowedControl(name); elm != nil {
			cc.ownControl(svc, elm)

			withOwn, _ := cc.c.serviceInstallers(name)
			if slices.ContainsFunc(withOwn, func(comp *comp) bool { return slices.Contains(comp.e.kids, elm) }) {
				cc.c.todof(svc, "", cc.serviceLines[i], "services[].control of %s comes from the ServiceControl on line %d, "+
					"which belongs to another component that installs it; WiX runs that one only when its own component "+
					"is installed, so check that this component needs it", name, elm.line)
			}

			continue
		}

		cc.c.todof(svc, "", cc.serviceLines[i],
			"the WiX package has no ServiceControl for %v; msi-builder starts it on install and stops and deletes it by default (services[].control)",
			name)
	}
}

// namedKid returns a matcher of the elements of a kind that name a service;
// service names do not depend on case.
func (c *converter) namedKid(kind, name string) func(e *elem) bool {
	return func(e *elem) bool { return e.is(kind) && strings.EqualFold(c.value(e, "Name"), name) }
}

// serviceInstallers returns the components that install a service, those
// with a ServiceControl of it and those without.
func (c *converter) serviceInstallers(name string) ([]*comp, []*comp) {
	var withOwn, without []*comp

	for _, comp := range c.comps {
		if !slices.ContainsFunc(comp.e.kids, c.namedKid("ServiceInstall", name)) {
			continue
		}

		if slices.ContainsFunc(comp.e.kids, c.namedKid("ServiceControl", name)) {
			withOwn = append(withOwn, comp)
		} else {
			without = append(without, comp)
		}
	}

	return withOwn, without
}

// borrowedControl returns the ServiceControl that becomes the control of a
// service in the components that install it without one of their own: the
// first in a component that does not install the service, else the first
// in one that does.
func (c *converter) borrowedControl(name string) *elem {
	var installer *elem

	for _, comp := range c.comps {
		i := slices.IndexFunc(comp.e.kids, c.namedKid("ServiceControl", name))
		if i < 0 {
			continue
		}

		if !slices.ContainsFunc(comp.e.kids, c.namedKid("ServiceInstall", name)) {
			return comp.e.kids[i]
		}

		if installer == nil {
			installer = comp.e.kids[i]
		}
	}

	return installer
}

// compNames lists components by ID.
func (c *converter) compNames(comps []*comp) string {
	names := make([]string, 0, len(comps))
	for _, comp := range comps {
		names = append(names, c.compID(comp))
	}

	return strings.Join(names, ", ")
}

// events reports whether a ServiceControl event value includes install and
// uninstall.
func events(v string) (bool, bool) {
	return v == onInstall || v == onBoth, v == onUninstall || v == onBoth
}

func (cc *compConv) ownControl(svc *omap, elm *elem) {
	if cc.controlled == nil {
		cc.controlled = make(map[*omap]bool)
	}

	cc.controlled[svc] = true
	ctl := newMap()
	ctl.order("startOnInstall", "stopOnInstall", "deleteOnInstall", "stopOnUninstall", "deleteOnUninstall", "wait")

	startInstall, startUninstall := events(elm.attr("Start"))
	stopInstall, stopUninstall := events(elm.attr("Stop"))
	removeInstall, removeUninstall := events(elm.attr("Remove"))

	set := func(key string, value, def bool) {
		if value != def {
			ctl.set(key, value)
		}
	}

	set("startOnInstall", startInstall, true)
	set("stopOnInstall", stopInstall, true)
	set("deleteOnInstall", removeInstall, false)
	set("stopOnUninstall", stopUninstall, true)
	set("deleteOnUninstall", removeUninstall, true)

	// Without Wait, WiX leaves the column null, which waits like 1 by the
	// ServiceControl table documentation, so wait keeps its default true.
	if elm.no("Wait") {
		ctl.set("wait", false)
	}

	if startUninstall {
		cc.c.todof(ctl, "", elm.line, "starting the package's own service on uninstall has no counterpart")
	}

	svc.set("control", ctl)
}

func (cc *compConv) foreignControl(elm *elem) {
	ctl := newMap()
	ctl.order("name", "stopOnInstall", "startOnInstall", "deleteOnInstall", "stopOnUninstall", "startOnUninstall", "deleteOnUninstall", "wait")
	ctl.set("name", cc.c.value(elm, "Name"))

	for _, event := range [][3]string{
		{"Stop", "stopOnInstall", "stopOnUninstall"},
		{"Start", "startOnInstall", "startOnUninstall"},
		{"Remove", "deleteOnInstall", "deleteOnUninstall"},
	} {
		onInstall, onUninstall := events(elm.attr(event[0]))
		if onInstall {
			ctl.set(event[1], true)
		}

		if onUninstall {
			ctl.set(event[2], true)
		}
	}

	if elm.no("Wait") {
		ctl.set("wait", false)
	}

	cc.out.add("serviceControl", ctl)
}

func (cc *compConv) createFolder(elm *elem) {
	conv := cc.c

	if d := elm.attr(attrDirectory); d != "" && d != cc.cp.dir {
		conv.todof(cc.out, "", elm.line, "CreateFolder for %s, another folder than the component's: move it to a component of its own", d)

		return
	}

	cc.out.set("createFolder", true)

	for _, k := range elm.kids {
		if permissionElems[k.qname()] {
			conv.permission(cc.out, "folderPermissions", k, rightsFile)
		} else {
			conv.todof(cc.out, "", k.line, "<%s> in a CreateFolder has no counterpart", k.qname())
		}
	}
}

func (cc *compConv) removeFile(elm *elem) {
	conv := cc.c
	row := newMap()
	row.order("id", "name", "directory", "on")

	if elm.is("RemoveFile") {
		row.set("name", conv.value(elm, "Name"))
	}

	if elm.has(attrProperty) {
		conv.todof(row, "", elm.line, "the folder comes from the property %s: msi-builder takes a directory ID", elm.attr(attrProperty))
	}

	if d := elm.attr(attrDirectory); d != "" && d != cc.cp.dir {
		row.set("directory", conv.dirRef(d))
	}

	switch on := elm.attr("On"); on {
	case onInstall, onBoth:
		row.set("on", on)
	}

	if elm.is("RemoveFolder") {
		cc.removeFolder = append(cc.removeFolder, row)

		return
	}

	cc.out.add("removeFiles", row)
}

// movableDir returns the first directory from dirID up to its root that can
// point anywhere: one whose output ID is public, which the command line sets,
// or one an action or property sets.
func (c *converter) movableDir(dirID string) string {
	set := c.setDirs()

	for folder := c.dirs[dirID]; folder != nil; folder = folder.parent {
		if id := c.dirRef(folder.id); set[folder.id] || config.IsPublicProperty(id) && !config.IsStandardFolder(id) {
			return id
		}
	}

	if c.dirs[dirID] == nil && (set[dirID] || config.IsPublicProperty(c.dirRef(dirID))) {
		return c.dirRef(dirID)
	}

	return ""
}

// setDirs returns the WiX IDs that a SetDirectory, SetProperty, custom
// action or property sets.
func (c *converter) setDirs() map[string]bool {
	set := make(map[string]bool)

	for _, setter := range c.setters {
		set[setter.attr("Id")] = true
	}

	for _, custom := range c.actionOrder {
		if custom.has("Value") {
			set[custom.attr(attrDirectory)+custom.attr(attrProperty)] = true
		}
	}

	for _, prop := range c.properties {
		set[prop.attr("Id")] = true
	}

	return set
}

// purge converts util:RemoveFolderEx. The removal of the component's own
// folder on uninstall, when no directory up to its root can point
// elsewhere, becomes purge, whose deferred rmdir FILE-03 verified; any
// other folder or On becomes a removeFolders entry, which reads the folder
// at install time.
func (cc *compConv) purge(elm *elem) {
	conv := cc.c
	prop := elm.attr(attrProperty)
	when := elm.attr("On")
	cond := conv.value(elm, "Condition")

	// A property that only reads the component's own folder back, by a
	// RegistrySearch of a value the component writes, names that folder.
	if prop != "" && cc.remembers(prop) {
		prop = cc.cp.dir
	}

	when = cc.uninstallHalf(elm, cmp.Or(prop, cc.cp.dir), when)

	if (prop == "" || prop == cc.cp.dir) && (when == "" || when == "uninstall") && conv.movableDir(cc.cp.dir) == "" &&
		cc.out.get("purge") == nil {
		if cond != "" {
			cc.out.set("purge", newMap().set("condition", cond))
		} else {
			cc.out.set("purge", true)
		}

		return
	}

	cc.removeFolders(elm, cmp.Or(prop, cc.cp.dir), when, cond)
}

// uninstallHalf returns the On a util:RemoveFolderEx keeps: a folder of
// the package is removed on uninstall only, so the install half of
// On="both" is left out with a note.
func (cc *compConv) uninstallHalf(elm *elem, folder, when string) string {
	conv := cc.c
	if _, isDir := conv.dirs[folder]; !isDir || when != onBoth {
		return when
	}

	conv.todof(cc.out, "", elm.line, "the install half of util:RemoveFolderEx On=\"both\" on %s was not converted: "+
		"removeFolders removes a folder of the package on uninstall only, since a repair would lose the files the "+
		"installer keeps there", folder)

	return ""
}

// removeFolders writes the removeFolders entry of a util:RemoveFolderEx.
func (cc *compConv) removeFolders(elm *elem, prop, when, cond string) {
	conv := cc.c
	entry := newMap().set("property", conv.dirRef(prop))

	// The removal reads the folder after costing, so a directory works
	// (markNeededDirs keeps its ID), but the install dir has the path chosen at install time only when
	// installDir.remember searches for it, as WiX packages do with a
	// RegistrySearch of their own.
	inst := conv.installDir
	if _, isDir := conv.dirs[prop]; isDir && when == onInstall {
		conv.todof(cc.out, "", elm.line, "util:RemoveFolderEx On=\"install\" on %s was not converted: removeFolders removes a folder "+
			"of the package on uninstall only, since a repair would lose the files the installer keeps there", prop)

		return
	}

	if inst != nil && conv.dirRef(inst.id) == conv.dirRef(prop) {
		install := conv.out.child("installDir")
		if install.get("remember") == nil {
			install.set("remember", true)
			install.comment("remember", "util:RemoveFolderEx needs the install dir chosen at install time; remember searches for it.")
		}
	}

	switch when {
	case onInstall, onBoth:
		entry.set("on", when)
	}

	if cond != "" {
		entry.set("condition", cond)
	}

	cc.out.add("removeFolders", entry)
}

// remembers reports whether prop is the folder of this component as the
// component remembers it: the property only reads, by a RegistrySearch, a
// registry value of the component that holds [folder]. A RemoveFolderEx on
// such a property removes the component's folder, which purge does.
func (cc *compConv) remembers(prop string) bool {
	conv := cc.c

	if !conv.onlyProperty(prop) {
		return false
	}

	for _, p := range conv.properties {
		if p.attr("Id") != prop || p.has("Value") {
			continue
		}

		for _, search := range p.kids {
			if search.is("RegistrySearch") && cc.writesFolder(search) {
				return true
			}
		}
	}

	return false
}

// onlyProperty reports whether a Property element is all that sets prop.
func (c *converter) onlyProperty(prop string) bool {
	for _, setter := range c.setters {
		if setter.attr("Id") == prop {
			return false
		}
	}

	for _, custom := range c.actionOrder {
		if custom.has("Value") && custom.attr(attrDirectory)+custom.attr(attrProperty) == prop {
			return false
		}
	}

	return true
}

// writesFolder reports whether the component writes its folder, as
// [folder], to the registry value that search reads.
func (cc *compConv) writesFolder(search *elem) bool {
	want := []string{
		strings.ToUpper(search.attr("Root")), strings.ToLower(strings.Trim(search.attr("Key"), `\`)),
		strings.ToLower(search.attr("Name")),
	}
	folder := "[" + cc.cp.dir + "]"

	var walk func(e *elem, root, key string) bool

	walk = func(e *elem, root, key string) bool {
		for _, kid := range e.kids {
			regRoot, regKey := root, key
			if kid.has("Root") {
				regRoot = kid.attr("Root")
			}

			if kid.has("Key") {
				regKey = joinKey(regKey, kid.attr("Key"))
			}

			switch {
			case kid.is("RegistryKey") && walk(kid, regRoot, regKey):
				return true
			case kid.is("RegistryValue") && strings.TrimSuffix(kid.attr("Value"), `\`) == folder &&
				slices.Equal(want, []string{strings.ToUpper(regRoot), strings.ToLower(strings.Trim(regKey, `\`)), strings.ToLower(kid.attr("Name"))}):
				return true
			}
		}

		return false
	}

	return walk(cc.cp.e, "", "")
}

func (cc *compConv) eventSource(elm *elem) {
	conv := cc.c
	source := newMap()
	source.set("source", conv.value(elm, "Name"))
	source.set("log", conv.value(elm, "Log"))
	source.set("messageFile", conv.value(elm, "EventMessageFile"))

	types := 0

	for i, a := range []string{"SupportsErrors", "SupportsWarnings", "SupportsInformationals", "SupportsSuccessAudits", "SupportsFailureAudits"} {
		if elm.yes(a) {
			types |= 1 << i
		}
	}

	if types != 0 {
		source.set("typesSupported", types)
	}

	for _, a := range []string{"CategoryMessageFile", "CategoryCount", "ParameterMessageFile"} {
		if elm.has(a) {
			conv.todof(source, "", elm.line, "util:EventSource %s has no counterpart", a)
		}
	}

	cc.out.add("eventLog", source)
}

// firewall converts a firewall exception into a firewall rule that follows
// the component. program is the file the rule is nested in, if any.
func (c *converter) firewall(elm *elem, comp *comp, program string) { //nolint:cyclop // a flat list of independent WiX attributes
	rule := newMap()
	rule.order("name", "description", "component", "program", "protocol", "port", "remoteAddresses", "profiles")
	rule.head = comments(elm)
	rule.set("name", c.value(elm, "Name"))
	rule.set("description", c.value(elm, "Description"))
	rule.set("component", c.compID(comp))

	if first, dup := c.firewallNames[rule.str("name")]; dup {
		c.todof(rule, "name", elm.line, "the firewall exception on line %d has the same name; msi-builder needs a name "+
			"per rule, rename one or merge them", first)
	} else {
		c.firewallNames[rule.str("name")] = elm.line
	}

	if p := c.value(elm, "Program"); p != "" {
		program = p
	}

	if f := elm.attr("File"); f != "" {
		program = "[#" + f + "]"
	}

	rule.set("program", program)

	if program == "" {
		c.todof(rule, "program", elm.line, "the firewall exception names no program; msi-builder rules open ports for one program, set program")
	}

	c.firewallProtocol(rule, elm)

	var remote []string

	switch scope := elm.attr("Scope"); scope {
	case "", valueAny:
	case "localSubnet":
		remote = append(remote, "LocalSubnet")
	default:
		c.todof(rule, "", elm.line, "firewall Scope=%q has no counterpart; list the addresses in remoteAddresses", scope)
	}

	for _, kid := range elm.kids {
		if kid.qname() == "fw:RemoteAddress" {
			v := kid.attr("Value")
			if v == "" {
				v = strings.TrimSpace(kid.text)
			}

			remote = append(remote, c.expand(kid, v))
		} else {
			c.todof(rule, "", kid.line, "<%s> in a firewall exception has no counterpart", kid.qname())
		}
	}

	rule.set("remoteAddresses", strings.Join(remote, ","))

	switch profile := elm.attr("Profile"); profile {
	case "", "all":
		rule.set("profiles", "all")
	default:
		rule.set("profiles", profile)
	}

	for _, a := range []string{"IgnoreFailure", "Outbound", "Interface", "InterfaceType", "LocalScope", "EdgeTraversal", "Grouping"} {
		if elm.has(a) && !elm.no(a) {
			c.todof(rule, "", elm.line, "firewall %s has no counterpart; msi-builder writes inbound rules", a)
		}
	}

	c.out.add("firewall", rule)
}

// firewallProtocol sets the protocol and port of a firewall rule. A WiX
// exception without Protocol is not limited to TCP, msi-builder's default:
// without a port it becomes any, and with one it needs a choice, because a
// Windows firewall rule with a port must name TCP or UDP.
func (c *converter) firewallProtocol(rule *omap, elm *elem) {
	protocol, port := c.value(elm, "Protocol"), c.value(elm, "Port")

	switch {
	case protocol != "":
	case port == "":
		protocol = valueAny
	default:
		protocol = "tcp"

		c.todof(rule, "protocol", elm.line, "the firewall exception sets no Protocol; msi-builder opens port %s for tcp only, "+
			"add a udp rule if the program needs it", port)
	}

	rule.set("protocol", protocol)
	rule.set("port", port)
}

// compID returns the component's ID, giving it one when WiX generated it.
func (c *converter) compID(comp *comp) string {
	if comp.id == "" {
		// Skip the IDs that components took from the source.
		for comp.id == "" || c.compByID[comp.id] != nil {
			c.anonymous++
			comp.id = "Component" + strconv.Itoa(c.anonymous)
		}

		if comp.out != nil {
			comp.out.set("id", comp.id)
		}
	}

	return comp.id
}

func (cc *compConv) progID(elm *elem) { //nolint:cyclop // a flat list of independent WiX attributes
	conv := cc.c

	if elm.yes("Advertise") {
		conv.todof(cc.out, "", elm.line, "advertised ProgIds are never supported; converted as plain file types")
	}

	fileType := newMap()
	fileType.order("progId", "description", "iconIndex", "extensions", "verbs")
	fileType.set("progId", conv.typeText(fileType, elm, "Id"))
	fileType.set("description", conv.typeText(fileType, elm, "Description"))

	if elm.has("Icon") {
		cc.iconIndex(elm, fileType)
	}

	target := ""

	for _, ext := range elm.kids {
		if !ext.is("Extension") {
			conv.todof(fileType, "", ext.line, "<%s> in a ProgId has no counterpart", ext.qname())

			continue
		}

		x := newMap().set("extension", conv.typeText(fileType, ext, "Id")).set("contentType", conv.typeText(fileType, ext, "ContentType"))
		fileType.add("extensions", x)

		for _, verb := range ext.kids {
			if !verb.is("Verb") {
				conv.todof(fileType, "", verb.line, "<%s> in an Extension has no counterpart", verb.qname())

				continue
			}

			verbOut := newMap().set("id", conv.value(verb, "Id")).set("text", conv.typeText(fileType, verb, "Command"))
			if verb.has("Argument") {
				verbOut.set("arguments", conv.value(verb, "Argument"))

				if verbOut.get("arguments") == nil {
					verbOut.values["arguments"] = ""
					verbOut.addKey("arguments")
				}
			}

			fileType.add("verbs", verbOut)

			if f := verb.attr("TargetFile"); f != "" {
				target = f
			}
		}
	}

	if fileType.get("verbs") == nil {
		// Without verbs msi-builder writes an open verb; WiX writes none.
		fileType.set("verbs", emptyList{})
	}

	conv.pendingTypes = append(conv.pendingTypes, pendingType{e: elm, comp: cc.cp, target: target, out: fileType, parent: cc.out})
}

// iconIndex sets a ProgId's icon index. Without an IconIndex, DefaultIcon
// names the file alone, which Windows reads as its first icon.
func (cc *compConv) iconIndex(elm *elem, fileType *omap) {
	if !elm.has("IconIndex") {
		fileType.set("iconIndex", 0)

		return
	}

	text := strings.TrimSpace(cc.c.literal(elm.attr("IconIndex")))

	index, err := strconv.Atoi(text)
	if err != nil {
		cc.c.todof(fileType, "", elm.line, "the ProgId %s has the icon index %q, which is no number; set iconIndex by hand",
			elm.attr("Id"), text)

		return
	}

	fileType.set("iconIndex", index)
}

// typeText returns an attribute of a ProgId, Extension or Verb with the
// properties it references filled in when they have a constant value:
// msi-builder writes these texts as they are, and WiX formats them. Other
// formatted text left over gets a TODO.
func (c *converter) typeText(out *omap, elm *elem, attr string) string {
	text := bracketRef.ReplaceAllStringFunc(c.value(elm, attr), func(ref string) string {
		if value, ok := c.constProperty(ref[1 : len(ref)-1]); ok {
			return value
		}

		return ref
	})

	if formattedLeft(text) {
		c.todof(out, "", elm.line, "%s %s=%q holds formatted text convert cannot fill in (a property without a constant "+
			"value, or another reference); msi-builder writes file type texts as they are, so write the text out",
			elm.name, attr, text)
	}

	return text
}

// constProperty returns the value of a property that a Property element
// or the package sets and nothing in the source changes. A public one could
// still come from the command line; convert assumes it does not.
func (c *converter) constProperty(name string) (string, bool) {
	if attr, ok := map[string]string{"ProductName": "Name", "Manufacturer": "Manufacturer", "ProductVersion": "Version"}[name]; ok {
		value := c.expandVars(c.pkg, c.pkg.attr(attr))

		return value, c.pkg.has(attr) && !formattedLeft(value)
	}

	var found *elem

	for _, prop := range c.properties {
		if prop.attr("Id") == name {
			found = prop
		}
	}

	if found == nil || !found.has("Value") || len(found.kids) > 0 || c.propertyChanged(name) {
		return "", false
	}

	value := c.value(found, "Value")

	return value, !formattedLeft(value)
}

// varRef matches a {{ .NAME }} action that msi-builder fills in on load.
var varRef = regexp.MustCompile(`\{\{ \.[A-Za-z_][A-Za-z0-9_]* \}\}`)

// formattedLeft reports whether text still holds formatted text or a WiX
// bind or localization variable once the {{ .NAME }} actions are left out.
func formattedLeft(text string) bool {
	text = varRef.ReplaceAllString(text, "")

	return strings.ContainsAny(text, "[]{}") || strings.Contains(text, "!(")
}

// propertyChanged reports whether an action, a SetProperty, a Publish
// event or a dialog control sets the property.
func (c *converter) propertyChanged(name string) bool {
	if slices.ContainsFunc(c.commandSources(), func(src commandSource) bool { return src.property == name }) {
		return true
	}

	var changed func(elm *elem) bool

	changed = func(elm *elem) bool {
		if (elm.is("Publish") || elm.is("Control")) && elm.attr(attrProperty) == name {
			return true
		}

		return slices.ContainsFunc(elm.kids, changed)
	}

	return changed(c.doc.root)
}

// pendingType is a ProgId that goes onto the file its verbs start, once
// every file is known.
type pendingType struct {
	e      *elem
	comp   *comp
	out    *omap
	parent *omap
	target string
}

func (c *converter) placeTypes() {
	placed := make(map[string]placedType)

	for _, pending := range c.pendingTypes {
		var file *fileRef

		for _, f := range c.files {
			if pending.target != "" && f.key == pending.target || pending.target == "" && f.comp == pending.comp && file == nil {
				file = f
			}
		}

		if file == nil {
			c.todof(pending.parent, "", pending.e.line, "the ProgId %s opens no file of the package; file types go on the file they start", pending.e.attr("Id"))

			continue
		}

		progID := strings.ToLower(pending.out.str("progId"))
		if first, ok := placed[progID]; ok {
			c.mergeType(first, pending, file)

			continue
		}

		placed[progID] = placedType{pending: pending, file: file}

		file.out.add("fileTypes", pending.out)
		c.typeIconNote(pending, file)
		c.typeFeatureNote(pending, file)
		c.typeConditionNote(pending, file)
	}
}

// placedType is a ProgId placed on the file it opens.
type placedType struct {
	file    *fileRef
	pending pendingType
}

// typeConditionNote notes a ProgId whose component has a condition that the
// component of the file it opens does not share: the file type now
// installs with the file.
func (c *converter) typeConditionNote(pending pendingType, file *fileRef) {
	if pending.comp == file.comp || pending.comp.out == nil {
		return
	}

	cond := pending.comp.out.str("condition")
	if cond == "" || file.comp.out != nil && file.comp.out.str("condition") == cond {
		return
	}

	c.todof(pending.out, "", pending.e.line, "the ProgId %s was in a component with the condition %s; msi-builder installs "+
		"file types with their file, so that condition no longer applies to it", pending.e.attr("Id"), cond)
}

// typeExtensions returns the extensions of a converted file type.
func typeExtensions(fileType *omap) []*omap {
	list, ok := fileType.get("extensions").([]any)
	if !ok {
		return nil
	}

	var exts []*omap

	for _, item := range list {
		if entry, ok := item.(*omap); ok {
			exts = append(exts, entry)
		}
	}

	return exts
}

// mergeType adds the extensions of a second declaration of a ProgId to
// the first one: msi-builder registers each ProgId once. WiX writes each
// declaration into its own component, so a condition of that component
// no longer applies. A second declaration that opens another file is
// left out.
func (c *converter) mergeType(first placedType, again pendingType, file *fileRef) {
	if file != first.file {
		c.todof(first.pending.out, "", again.e.line, "the ProgId %s is declared again for the file %s; msi-builder registers a ProgId "+
			"once, so convert left that declaration out, add its extensions to a ProgId of its own", again.e.attr("Id"), file.key)
		first.pending.out.head = append(first.pending.out.head, commentsOf(again.out, nil)...)

		return
	}

	seen := make(map[string]bool)

	for _, entry := range typeExtensions(first.pending.out) {
		seen[strings.ToLower(entry.str("extension"))] = true
	}

	merged := make(map[*omap]bool)

	for _, entry := range typeExtensions(again.out) {
		if !seen[strings.ToLower(entry.str("extension"))] {
			seen[strings.ToLower(entry.str("extension"))] = true
			merged[entry] = true

			first.pending.out.add("extensions", entry)
		}
	}

	note := ""
	if again.comp != file.comp && c.componentDiffers(file.comp, again.comp) {
		note = "; it installs with the file now, so the condition and features of the component that declared it again no longer apply"
	}

	c.todof(first.pending.out, "", again.e.line, "the ProgId %s is declared again; convert merged its extensions into this file type "+
		"and kept the description and verbs of the first one%s", again.e.attr("Id"), note)

	// The notes of the second declaration stay in the YAML, except those
	// of the extensions that moved over with their own notes.
	first.pending.out.head = append(first.pending.out.head, commentsOf(again.out, merged)...)
}

// componentDiffers reports whether the second component has a condition
// or features that the first one does not share.
func (c *converter) componentDiffers(first, again *comp) bool {
	if again.out == nil {
		return false
	}

	if cond := again.out.str("condition"); cond != "" && (first.out == nil || first.out.str("condition") != cond) {
		return true
	}

	return !slices.Equal(c.featureIDs(first), c.featureIDs(again))
}

// commentsOf returns the comment lines of mapping and of the mappings in it,
// leaving out the mappings in skip.
func commentsOf(mapping *omap, skip map[*omap]bool) []string {
	if mapping == nil || skip[mapping] {
		return nil
	}

	lines := slices.Clone(mapping.head)

	for _, key := range mapping.keys {
		lines = append(lines, mapping.comments[key]...)
		lines = append(lines, commentsIn(mapping.values[key], skip)...)
	}

	return lines
}

func commentsIn(value any, skip map[*omap]bool) []string {
	switch typed := value.(type) {
	case *omap:
		return commentsOf(typed, skip)
	case []any:
		var lines []string

		for _, item := range typed {
			lines = append(lines, commentsIn(item, skip)...)
		}

		return lines
	}

	return nil
}

// typeIconNote notes a ProgId whose icon is in another file than the one
// it opens: iconIndex only picks an icon of the file the type goes on.
func (c *converter) typeIconNote(pending pendingType, file *fileRef) {
	icon := strings.TrimSpace(c.literal(pending.e.attr("Icon")))
	if icon == "" || icon == file.key || icon == strings.TrimSpace(c.literal(file.key)) {
		return
	}

	pending.out.del("iconIndex")

	progID := pending.e.attr("Id")
	isFile := slices.ContainsFunc(c.files, func(f *fileRef) bool { return f.key == icon })

	if !isFile && slices.ContainsFunc(c.icons, func(e *elem) bool { return e.attr("Id") == icon }) {
		c.todof(pending.out, "", pending.e.line, "the ProgId %s takes its icon from the <Icon> %s, which only advertised "+
			"ProgIds use; install the icon as a file and add the DefaultIcon key HKCR\\%s\\DefaultIcon as a registry value "+
			"naming it", progID, icon, progID)

		return
	}

	where := ""
	if !isFile {
		where = " (a file this source does not define)"
	}

	c.todof(pending.out, "", pending.e.line, "the ProgId %s takes its icon from %s%s, not from the file it opens; "+
		"iconIndex only picks an icon of that file, so add the DefaultIcon key as a registry value "+
		"(HKCR\\%s\\DefaultIcon = [#%s],%s)", progID, icon, where, progID, icon, c.iconIndexText(pending.e))
}

// iconIndexText returns a ProgId's icon index, 0 when it gives none.
func (c *converter) iconIndexText(elm *elem) string {
	if elm.has("IconIndex") {
		return strings.TrimSpace(c.literal(elm.attr("IconIndex")))
	}

	return "0"
}

// typeFeatureNote notes a ProgId whose component is in other features than
// the file it opens: its file type now installs with the file.
func (c *converter) typeFeatureNote(pending pendingType, file *fileRef) {
	if file.comp == pending.comp {
		return
	}

	own, target := c.featureIDs(pending.comp), c.featureIDs(file.comp)
	if slices.Equal(own, target) {
		return
	}

	c.todof(pending.out, "", pending.e.line, "the ProgId %s was in features %s, the file it opens in %s; "+
		"msi-builder installs file types with their file, so this one is no longer optional", pending.e.attr("Id"),
		orNone(own), orNone(target))
}

// featureIDs returns the IDs of the features that hold a component, sorted.
func (c *converter) featureIDs(cp *comp) []string {
	var ids []string

	c.eachFeature(func(f *feature) {
		if slices.Contains(c.members(f.members, make(map[string]bool)), cp) {
			ids = append(ids, f.e.attr("Id"))
		}
	})

	slices.Sort(ids)

	return slices.Compact(ids)
}

func orNone(ids []string) string {
	if len(ids) == 0 {
		return valueNone
	}

	return strings.Join(ids, ", ")
}

// Rights names of grants.
const (
	rightsFile    = "file"
	rightsService = "service"
)

// namedRight is a util:PermissionEx attribute and the right it grants: a
// msi-builder right name, or else access mask bits.
type namedRight struct {
	attr  string
	right string
	mask  int
}

// fileRights maps util:PermissionEx attributes to file rights: names
// where msi-builder has one, else the access mask bits from the Windows
// file access rights.
var fileRights = []namedRight{ //nolint:gochecknoglobals // lookup table
	{"GenericAll", rightsFull, 0},
	{"FileAllRights", rightsFull, 0},
	{"GenericRead", "read", 0},
	{"GenericWrite", "", 0x120116}, // FILE_GENERIC_WRITE; write is 0x100116
	{"GenericExecute", "", 0x1200A0},
	{"Read", "", 0x1},
	{"CreateFile", "", 0x2},
	{"Write", "", 0x2},
	{"CreateChild", "", 0x4},
	{"Append", "", 0x4},
	{"CreateSubdirectory", "", 0x4},
	{"ReadExtendedAttributes", "", 0x8},
	{"WriteExtendedAttributes", "", 0x10},
	{"Execute", "", 0x20},
	{"Traverse", "", 0x20},
	{"DeleteChild", "", 0x40},
	{"ReadAttributes", "", 0x80},
	{"WriteAttributes", "", 0x100},
	{"Delete", "", 0x10000},
	{"ReadPermission", "", 0x20000},
	{"ChangePermission", "", 0x40000},
	{"TakeOwnership", "", 0x80000},
	{"Synchronize", "", 0x100000},
}

// serviceRights maps util:PermissionEx attributes to service rights: names
// where msi-builder has one, else the access mask bits from the Windows
// service access rights.
var serviceRights = []namedRight{ //nolint:gochecknoglobals // lookup table
	{"GenericAll", rightsFull, 0},
	{"GenericRead", "read", 0},
	{"GenericWrite", "", 0x20002},   // READ_CONTROL | SERVICE_CHANGE_CONFIG
	{"GenericExecute", "", 0x20170}, // READ_CONTROL | start, stop, pause, user-defined control
	{"ServiceQueryConfig", "", 0x1},
	{"ServiceChangeConfig", "", 0x2},
	{"ServiceQueryStatus", "", 0x4},
	{"ServiceEnumerateDependents", "", 0x8},
	{"ServiceStart", "start", 0},
	{"ServiceStop", "stop", 0},
	{"ServicePauseContinue", "pauseContinue", 0},
	{"ServiceInterrogate", "", 0x80},
	{"ServiceUserDefinedControl", "", 0x100},
	{"Delete", "", 0x10000},
	{"ReadPermission", "", 0x20000},
	{"ChangePermission", "", 0x40000},
	{"TakeOwnership", "", 0x80000},
}

// wellKnownAccounts are the SDDL aliases of built-in accounts by their
// English names, which other Windows languages translate.
var wellKnownAccounts = map[string]string{ //nolint:gochecknoglobals // lookup table
	"administrators": "BA", "users": "BU", "guests": "BG", "power users": "PU", "everyone": "WD",
	"authenticated users": "AU", "system": "SY", "localsystem": "SY", "local service": "LS", "localservice": "LS",
	"network service": "NS", "networkservice": "NS", "interactive": "IU", "remote desktop users": "RD",
}

// grantAccount names a util:PermissionEx account the way grants take it:
// a built-in account by its SDDL alias, so the name resolves in every
// Windows language.
func grantAccount(domain, user string) string {
	switch strings.ToLower(domain) {
	case "", "builtin", "nt authority":
		if alias, ok := wellKnownAccounts[strings.ToLower(user)]; ok {
			return alias
		}
	}

	if domain != "" {
		return domain + `\` + user
	}

	return user
}

// permission converts a permission element into the permissions key of target.
func (c *converter) permission(target *omap, key string, elm *elem, kind string) {
	perms := target.mapAt(key)
	if perms == nil {
		perms = newMap()
		perms.order("sddl", "grant", "condition")
		target.set(key, perms)
	}

	switch elm.qname() {
	case "PermissionEx":
		if sddl := c.value(elm, "Sddl"); sddl != "" {
			perms.set("sddl", sddl)

			return
		}

		c.todof(target, key, elm.line, "a PermissionEx without Sddl has no counterpart")
	case elemPermission:
		c.todof(target, key, elm.line, "<Permission> replaces the object's permissions; write them as an SDDL string in %s.sddl "+
			"and add (A;OICI;FA;;;SY): Windows Installer also gives SYSTEM full access to an object it locks", key)
	case "util:PermissionEx":
		account := grantAccount(c.value(elm, "Domain"), c.value(elm, "User"))

		g := newMap().set("account", account).set("rights", c.rights(target, key, elm, kind))
		perms.add("grant", g)
	}
}

func (c *converter) rights(target *omap, key string, elm *elem, kind string) []string {
	var (
		names []string
		mask  int
	)

	table := fileRights
	if kind == rightsService {
		table = serviceRights
	}

	for _, attrName := range elm.order {
		if !elm.yes(attrName) {
			continue
		}

		idx := slices.IndexFunc(table, func(r namedRight) bool { return r.attr == attrName })

		switch {
		case idx < 0 && kind == rightsService:
			c.todof(target, key, elm.line, "the service right %s has no counterpart; add its access mask", attrName)
		case idx < 0:
		case table[idx].right != "":
			names = append(names, table[idx].right)
		default:
			mask |= table[idx].mask
		}
	}

	if mask != 0 {
		names = append(names, "0x"+strings.ToUpper(strconv.FormatInt(int64(mask), 16)))
	}

	return names
}
