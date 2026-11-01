package builder

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/internal/cadll"
	"github.com/jkroepke/msi-builder/internal/condition"
	"github.com/jkroepke/msi-builder/internal/sequence"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// Custom action type words (section 9.4).
const (
	caDLL          = 1
	caEXE          = 2
	caError        = 19
	caExec         = 34
	caSetDirectory = 35
	caExecProperty = 50
	caSetProperty  = 51

	caContinue       = 0x40
	caAsyncWait      = 0x80
	caAsyncNoWait    = 0xC0
	caFirstSequence  = 0x100
	caOncePerProcess = 0x200
	caClientRepeat   = 0x300
	caDeferred       = 0x400
	caRollback       = 0x100 | caDeferred
	caCommit         = 0x200 | caDeferred
	caNoImpersonate  = 0x800
	caHideTarget     = 0x2000

	// maxTargetLength is the width of CustomAction.Target. The engine reads
	// longer values, but ICE03 reports them.
	maxTargetLength = 255
)

// returnIgnore is the custom action return mode that never fails the install.
const returnIgnore = "ignore"

// Sequence table names.
const (
	seqInstallExecute = sequence.InstallExecute
	seqInstallUI      = sequence.InstallUI
	seqAdminExecute   = sequence.AdminExecute
	seqAdminUI        = sequence.AdminUI
	seqAdvtExecute    = sequence.AdvtExecute
)

// Standard action names the builder refers to more than once.
const (
	actValidateProductID    = "ValidateProductID"
	actCostInitialize       = "CostInitialize"
	actFileCost             = "FileCost"
	actCostFinalize         = "CostFinalize"
	actExecuteAction        = "ExecuteAction"
	actInstallValidate      = "InstallValidate"
	actInstallInitialize    = "InstallInitialize"
	actStopServices         = "StopServices"
	actDeleteServices       = "DeleteServices"
	actRemoveRegistryValues = "RemoveRegistryValues"
	actRemoveFiles          = "RemoveFiles"
	actRemoveFolders        = "RemoveFolders"
	actInstallFiles         = "InstallFiles"
	actWriteRegistryValues  = "WriteRegistryValues"
	actInstallServices      = "InstallServices"
	actStartServices        = "StartServices"
	actPublishFeatures      = "PublishFeatures"
	actPublishProduct       = "PublishProduct"
	actInstallFinalize      = "InstallFinalize"
	actScheduleReboot       = "ScheduleReboot"
	actFindRelatedProducts  = "FindRelatedProducts"
	actAppSearch            = "AppSearch"
	actLaunchConditions     = "LaunchConditions"
	actDuplicateFiles       = "DuplicateFiles"

	condNotInstalled = "NOT Installed"
	condRemoveAll    = `REMOVE~="ALL"`
	condVersionNT    = "VersionNT"
	fwAny            = "any"
	targetDir        = "TARGETDIR"
)

var sequenceTables = []string{seqInstallExecute, seqInstallUI, seqAdminExecute, seqAdminUI, seqAdvtExecute} //nolint:gochecknoglobals // lookup table

// tableActions maps a populated table to the InstallExecuteSequence actions
// it needs (section 9.3).
var tableActions = map[string][]string{ //nolint:gochecknoglobals // lookup table
	"Component":        {"CreateFolders", actRemoveFolders},
	"File":             {actInstallFiles, actRemoveFiles},
	"RemoveFile":       {actRemoveFiles, actRemoveFolders},
	"Registry":         {actWriteRegistryValues, actRemoveRegistryValues},
	"RemoveRegistry":   {actWriteRegistryValues, actRemoveRegistryValues},
	"Environment":      {"WriteEnvironmentStrings", "RemoveEnvironmentStrings"},
	"ServiceInstall":   {actInstallServices, actStartServices, actStopServices, actDeleteServices},
	"ServiceControl":   {actInstallServices, actStartServices, actStopServices, actDeleteServices},
	"MsiServiceConfig": {"MsiConfigureServices"},
	"Shortcut":         {"CreateShortcuts", "RemoveShortcuts"},
	"IniFile":          {"WriteIniValues", "RemoveIniValues"},
	"MoveFile":         {"MoveFiles"},
	"DuplicateFile":    {actDuplicateFiles, "RemoveDuplicateFiles"},
}

// addAction registers a custom action. The CustomAction row is written by
// sequences, once every contributor has run.
func (b *build) addAction(action *actionInfo) {
	for _, old := range b.actions {
		if old.name == action.name {
			b.errorf("customActions", "custom action %s is defined twice", action.name)

			return
		}
	}

	if s, ok := action.target.(string); ok && len(s) > maxTargetLength {
		b.warnf("customActions", "%s: Target is %d characters, CustomAction.Target is declared with %d (ICE03); the engine still runs it",
			action.name, len(s), maxTargetLength)
	}

	b.actions = append(b.actions, action)
}

func (b *build) action(name string) *actionInfo {
	for _, a := range b.actions {
		if a.name == name {
			return a
		}
	}

	return nil
}

// schedule places an action into a sequence table. Exactly one of before,
// after or at is set.
func (b *build) schedule(table, name, cond, before, after string, at int) {
	b.schedules[table] = append(b.schedules[table], sequence.Action{
		Name: name, Condition: cond, Sequence: at, Before: before, After: after,
	})
}

func (b *build) scheduled(table, name string) bool {
	for _, a := range b.schedules[table] {
		if a.Name == name {
			return true
		}
	}

	return false
}

// anchoredTo counts the actions of a table that are scheduled relative to
// name, directly or through other relative actions.
func (b *build) anchoredTo(table, name string) int {
	anchors := make(map[string]string, len(b.schedules[table]))
	for _, a := range b.schedules[table] {
		anchors[a.Name] = a.Before + a.After
	}

	count := 0

	for _, a := range b.schedules[table] {
		seen := make(map[string]bool)

		for anchor := anchors[a.Name]; anchor != "" && !seen[anchor]; anchor = anchors[anchor] {
			seen[anchor] = true

			if anchor == name {
				count++

				break
			}
		}
	}

	return count
}

// standardAt adds a standard action at its default number for the table,
// or where standardActions moves it, unless it is already scheduled there. It panics on an action without a
// default in that table, which is a builder bug.
func (b *build) standardAt(table, name string) {
	if b.scheduled(table, name) {
		return
	}

	std, ok := sequence.LookupStandard(name)
	if !ok || std.Number(table) == 0 {
		panic(fmt.Sprintf("builder: %s has no default number in %s", name, table))
	}

	if place, _, moved := b.cfg.StandardPlacement(table, name); moved {
		b.schedule(table, name, std.Condition, place.Before, place.After, place.At)

		return
	}

	b.schedule(table, name, std.Condition, "", "", std.Number(table))
}

// standardNumber is the default number of a standard action in a table.
func standardNumber(table, name string) int {
	s, _ := sequence.LookupStandard(name)

	return s.Number(table)
}

// standardCondition is the default condition of a standard action.
func standardCondition(name string) string {
	s, _ := sequence.LookupStandard(name)

	return s.Condition
}

// anchorActions adds a standard action that an action is scheduled
// relative to, but that no table needs, at its default number.
func (b *build) anchorActions() {
	for _, table := range sequenceTables {
		for _, action := range b.schedules[table] {
			anchor := action.Before + action.After
			if b.scopeAnchor(table, anchor) {
				continue
			}

			if s, ok := sequence.LookupStandard(anchor); ok && s.Number(table) != 0 {
				b.standardAt(table, anchor)

				// DuplicateFiles copies installed files (specification
				// section 11.7).
				if anchor == actDuplicateFiles {
					b.standardAt(table, actInstallFiles)
				}

				continue
			}

			if config.IsStandardAction(anchor) && !b.scheduled(table, anchor) {
				b.errorf("customActions", "%s: %s is scheduled relative to standard action %s, which has no default number there in msi-builder; "+
					"schedule %s with an absolute number instead", table, action.Name, anchor, action.Name)
			}
		}
	}
}

// customActions turns the customActions section into CustomAction rows and
// placements.
func (b *build) customActions() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	// The type-51 data actions of run actions, scheduled after the loop.
	type dataPlacement struct {
		table, name, condition, before string
	}

	var setters []dataPlacement

	for i, custom := range b.cfg.CustomActions {
		path := fmt.Sprintf("customActions[%d]", i)
		action := &actionInfo{name: custom.ID, progress: custom.Progress}
		setter := ""

		switch {
		case custom.SetProperty != nil:
			action.typ, action.source, action.target = caSetProperty, custom.SetProperty.Name, custom.SetProperty.Value
		case custom.SetDirectory != nil:
			action.typ, action.source, action.target = caSetDirectory, custom.SetDirectory.Directory, custom.SetDirectory.Value
		case custom.Error != "":
			action.typ, action.target = caError, custom.Error
		case custom.Exec != nil:
			action.typ, action.source, action.target = caExec, custom.Exec.Directory, custom.Exec.Command

			if config.IsStandardFolder(custom.Exec.Directory) {
				b.ensureStandardDir(custom.Exec.Directory)
			}
		case custom.ExecProperty != nil:
			action.typ, action.source, action.target = caExecProperty, custom.ExecProperty.Property, nullable(custom.ExecProperty.Arguments)
		case custom.DLL != nil:
			action.typ, action.source, action.target = caDLL, custom.DLL.Binary, custom.DLL.Entry
		case custom.EXE != nil:
			action.typ, action.source, action.target = caEXE, custom.EXE.Binary, nullable(custom.EXE.Arguments)
		case custom.Run != nil:
			setter = b.runAction(custom, action)
		}

		switch custom.Execute {
		case "deferred":
			action.typ |= caDeferred
		case "rollback":
			action.typ |= caRollback
		case "commit":
			action.typ |= caCommit
		}

		if custom.Impersonate != nil && !*custom.Impersonate {
			action.typ |= caNoImpersonate
		}

		switch custom.Return {
		case returnIgnore:
			action.typ |= caContinue
		case "asyncWait":
			action.typ |= caAsyncWait
		case "asyncNoWait":
			action.typ |= caAsyncNoWait
		}

		switch custom.Once {
		case "firstSequence":
			action.typ |= caFirstSequence
		case "oncePerProcess":
			action.typ |= caOncePerProcess
		case "clientRepeat":
			action.typ |= caClientRepeat
		}

		if custom.HideTarget {
			action.typ |= caHideTarget
		}

		b.addAction(action)

		for j, placement := range custom.Schedule {
			placementPath := fmt.Sprintf("%s.schedule[%d]", path, j)

			table, ok := config.SequenceTable(placement.Sequence)
			if !ok {
				b.errorf(placementPath+".sequence", "unknown sequence %q", placement.Sequence)

				continue
			}

			if err := condition.Check(placement.Condition); err != nil {
				b.errorf(placementPath+".condition", "%v", err)

				continue
			}

			cond := custom.ScheduleCondition(b.cfg, &placement)
			b.schedule(table, custom.ID, cond, placement.Before, placement.After, placement.At)

			if setter != "" {
				setters = append(setters, dataPlacement{table: table, name: setter, condition: cond, before: custom.ID})
			}
		}
	}

	// The data action must be the last one scheduled before its run action:
	// internal/sequence places the before-anchored actions in the order they
	// were added, so one scheduled later with before: <id> would land
	// between the two, and an immediate run action would read another
	// action's command line from the shared BuilderRun property.
	for _, setter := range setters {
		b.schedule(setter.table, setter.name, setter.condition, setter.before, "", 0)
	}

	return nil
}

// runAction turns a run action into a type-1 action on the embedded custom
// action DLL (internal/cadll) plus a type-51 action that hands it the
// working directory and command line: through CustomActionData for
// deferred, rollback and commit actions, which only see that property, and
// through the BuilderRun property for immediate ones. It returns the name of
// the type-51 action, which the caller schedules right before the action.
func (b *build) runAction(custom *config.CustomAction, action *actionInfo) string {
	if !b.useRunDLL() {
		return ""
	}

	directory := ""

	if custom.Run.Directory != "" {
		if config.IsStandardFolder(custom.Run.Directory) {
			b.ensureStandardDir(custom.Run.Directory)
		}

		directory = "[" + custom.Run.Directory + "]"
	}

	property := cadll.DataProperty
	if custom.Execute != "" && custom.Execute != "immediate" {
		property = custom.ID
	}

	setter := &actionInfo{name: custom.ID + ".Data", typ: caSetProperty, source: property, target: cadll.Data(directory, custom.Run.Command)}
	if custom.HideTarget {
		setter.typ |= caHideTarget
		b.hidden[property] = true
	}

	b.addAction(setter)

	action.typ, action.source, action.target = caDLL, cadll.Key, cadll.Entry

	return setter.name
}

// useRunDLL adds the custom action DLL (internal/cadll) to the Binary table
// once and reports whether it is there.
func (b *build) useRunDLL() bool {
	if b.runDLL {
		return true
	}

	if b.cfg.Binaries[cadll.Key] != "" {
		b.errorf("binaries."+cadll.Key, "the key %s is reserved for the custom action DLL of run actions, permission grants, close and restartResources", cadll.Key)
	}

	dll, err := cadll.Binary(string(b.cfg.Package.Platform))
	if err != nil {
		b.errorf("package.platform", "%v", err)

		return false
	}

	b.table("Binary").Add(cadll.Key, dll)
	b.runDLL = true

	return true
}

// machineOnly adds the per-machine test to a condition of the firewall
// actions in a dual package, whose rule components install only per
// machine, as a perMachine custom action does (CA-09, INST-08).
func (b *build) machineOnly(cond string) string {
	if b.cfg.Package.Scope.Dual() {
		return cond + " AND " + condPerMachine
	}

	return cond
}

// firewall writes the netsh actions of the firewall helper (section 17.3,
// design section 4.11): on install and repair every rule is deleted by name
// before any is added, with a rollback that deletes again; on removal the
// rule is deleted, with a rollback that adds it back. A rule with protocol
// any and a port is added once for TCP and once for UDP under its name,
// because netsh takes a port only with TCP or UDP (CA-10).
func (b *build) firewall() error { //nolint:cyclop,funlen // one pass over the format; splitting would scatter it
	if len(b.cfg.Firewall) == 0 {
		return nil
	}

	sysDir := b.systemDir()
	netsh := fmt.Sprintf(`"[%s]netsh.exe" advfirewall firewall `, sysDir)

	// add is one netsh add of a rule; its id names the add's actions.
	type add struct {
		id, target string
	}

	type rule struct {
		id, del, install, remove string
		adds                     []add
		ignoreFailure            bool
	}

	rules := make([]rule, 0, len(b.cfg.Firewall))

	for _, cfgRule := range b.cfg.Firewall {
		ruleID := generatedID("", "Firewall", cfgRule.Name, cfgRule.Protocol, cfgRule.Port, cfgRule.Program)[:8]

		remoteIP := cfgRule.RemoteAddresses
		if remoteIP == "" {
			remoteIP = fwAny
		} else if refs := propertyRefs(remoteIP); len(refs) > 0 {
			// A property may be empty at run time; fall back to fwAny then.
			prop := "FwIP" + ruleID
			b.addAction(&actionInfo{name: "SetFwRemoteIPDefault" + ruleID, typ: caSetProperty, source: prop, target: fwAny})
			b.addAction(&actionInfo{name: "SetFwRemoteIP" + ruleID, typ: caSetProperty, source: prop, target: remoteIP})
			b.schedule(seqInstallExecute, "SetFwRemoteIPDefault"+ruleID, "", "", actCostFinalize, 0)
			b.schedule(seqInstallExecute, "SetFwRemoteIP"+ruleID, strings.Join(refs, " AND "), "", "SetFwRemoteIPDefault"+ruleID, 0)

			remoteIP = "[" + prop + "]"
		}

		protocols := []string{cfgRule.Protocol}
		if cfgRule.Protocol == fwAny && cfgRule.Port != "" {
			protocols = []string{"tcp", "udp"}
		}

		fwRule := rule{
			id:            ruleID,
			del:           netsh + `delete rule name="` + cfgRule.Name + `" dir=in`,
			install:       b.machineOnly("$" + cfgRule.Component + "=3"),
			remove:        b.machineOnly("$" + cfgRule.Component + "=2"),
			ignoreFailure: cfgRule.IgnoreFailure,
		}

		for _, protocol := range protocols {
			addID := ruleID
			if len(protocols) > 1 {
				addID = generatedID("", "Firewall", cfgRule.Name, protocol, cfgRule.Port, cfgRule.Program)[:8]
			}

			fwRule.adds = append(fwRule.adds, add{id: addID, target: netsh + "add rule " + firewallArgs(cfgRule, protocol, remoteIP)})
		}

		rules = append(rules, fwRule)
	}

	deferred := caExec | caDeferred | caNoImpersonate
	rollback := caExec | caRollback | caNoImpersonate

	// Removal: rollback (add back) precedes its deferred action (delete).
	for _, r := range rules {
		for _, a := range r.adds {
			b.addAction(&actionInfo{name: "FwRemoveRollback" + a.id, typ: rollback | caContinue, source: sysDir, target: a.target})
		}

		b.addAction(&actionInfo{name: "FwRemove" + r.id, typ: deferred | caContinue, source: sysDir, target: r.del})
	}

	// Install: delete every rule, then add each with a rollback that deletes.
	for _, r := range rules {
		b.addAction(&actionInfo{name: "FwClear" + r.id, typ: deferred | caContinue, source: sysDir, target: r.del})
	}

	for _, fwRule := range rules {
		typ := deferred
		if fwRule.ignoreFailure {
			typ |= caContinue
		}

		for _, a := range fwRule.adds {
			b.addAction(&actionInfo{name: "FwAddRollback" + a.id, typ: rollback | caContinue, source: sysDir, target: fwRule.del})
			b.addAction(&actionInfo{name: "FwAdd" + a.id, typ: typ, source: sysDir, target: a.target})
		}
	}

	// After InstallFiles, so that the program exists; the chain keeps the
	// definition order.
	anchor := actInstallFiles
	b.standardAt(seqInstallExecute, anchor)

	for _, r := range rules {
		for _, a := range r.adds {
			b.schedule(seqInstallExecute, "FwRemoveRollback"+a.id, r.remove, "", anchor, 0)
		}

		b.schedule(seqInstallExecute, "FwRemove"+r.id, r.remove, "", anchor, 0)
	}

	for _, r := range rules {
		b.schedule(seqInstallExecute, "FwClear"+r.id, r.install, "", anchor, 0)
	}

	for _, r := range rules {
		for _, a := range r.adds {
			b.schedule(seqInstallExecute, "FwAddRollback"+a.id, r.install, "", anchor, 0)
			b.schedule(seqInstallExecute, "FwAdd"+a.id, r.install, "", anchor, 0)
		}
	}

	return nil
}

// firewallArgs returns the netsh add rule arguments of a rule for one
// protocol.
func firewallArgs(cfgRule *config.FirewallRule, protocol, remoteIP string) string {
	parts := []string{`name="` + cfgRule.Name + `"`, "dir=in", "action=allow", "protocol=" + protocol}

	if cfgRule.Port != "" {
		parts = append(parts, "localport="+cfgRule.Port)
	}

	if cfgRule.Program != "" {
		parts = append(parts, `program="`+cfgRule.Program+`"`)
	}

	// Defaults (remoteip=any, profile=any, enable=yes) are left out to
	// keep the Target within 255 characters.
	if remoteIP != fwAny {
		parts = append(parts, "remoteip="+remoteIP)
	}

	if profiles := firewallProfiles(cfgRule.Profiles); profiles != fwAny {
		parts = append(parts, "profile="+profiles)
	}

	if cfgRule.Description != "" {
		parts = append(parts, `description="`+cfgRule.Description+`"`)
	}

	return strings.Join(parts, " ")
}

func firewallProfiles(list config.StringList) string {
	if slices.Contains(list, "all") {
		return fwAny
	}

	return strings.Join(list, ",")
}

// sequences adds the standard actions each table needs, numbers every table
// and writes the five sequence tables and the CustomAction table.
func (b *build) sequences() error { //nolint:cyclop,gocognit // one pass over the format; splitting would scatter it
	for _, table := range sequenceTables {
		for _, name := range sequence.Always(table) {
			b.standardAt(table, name)
		}
	}

	b.movedLast()

	b.launchConditionsAction()

	for _, tbl := range slices.Sorted(maps.Keys(tableActions)) {
		if !b.hasRows(tbl) {
			continue
		}

		for _, name := range tableActions[tbl] {
			b.standardAt(seqInstallExecute, name)
		}
	}

	if b.hasRows("Component") {
		b.standardAt(seqInstallExecute, "AllocateRegistrySpace")
	}

	// ScheduleReboot has no default number: it goes after InstallFinalize,
	// where packages place it.
	if reboot := b.cfg.ScheduleReboot; reboot != nil {
		b.schedule(seqInstallExecute, actScheduleReboot, reboot.Placement().FullCondition(b.cfg), "", actInstallFinalize, 0)
	}

	b.anchorActions()

	// Every scheduled name must be a standard action or a custom action.
	for _, table := range sequenceTables {
		for _, action := range b.schedules[table] {
			if _, std := sequence.LookupStandard(action.Name); std || b.action(action.Name) != nil || b.isDialog(action.Name) {
				continue
			}

			if config.IsStandardAction(action.Name) {
				b.errorf("customActions", "%s: standard action %s has no default number in msi-builder; schedule it with an absolute number", table, action.Name)

				continue
			}

			b.errorf("customActions", "%s: action %s is scheduled but not defined", table, action.Name)
		}
	}

	b.checkGrantRoom()
	b.checkRemoveFoldersRoom()

	if err := b.diags.Err(); err != nil {
		return err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	for _, table := range sequenceTables {
		numbered, err := sequence.Number(b.schedules[table])
		if err != nil {
			b.sequenceError(table, err)

			continue
		}

		b.numbered[table] = numbered
		t := b.table(table)

		for _, n := range numbered {
			t.Add(n.Name, nullable(n.Condition), n.Sequence)
		}
	}

	if err := b.diags.Err(); err != nil {
		return err //nolint:wrapcheck // the diagnostics carry their own paths
	}

	b.checkSequences()
	b.checkMoved()
	b.checkWhen()
	b.checkPerUserRollback()

	scheduled := make(map[string]bool)

	for _, table := range sequenceTables {
		for _, a := range b.schedules[table] {
			scheduled[a.Name] = true
		}
	}

	caTable := b.table("CustomAction")

	slices.SortStableFunc(b.actions, func(a, b *actionInfo) int { return strings.Compare(a.name, b.name) })

	for _, action := range b.actions {
		if !scheduled[action.name] && !action.fromUI {
			b.warnf("customActions", "custom action %s is not scheduled in any sequence", action.name)
		}

		caTable.Add(action.name, action.typ, action.source, action.target, nil)

		// Without a template: the action kinds msi-builder writes send
		// no ActionData messages (specification section 17.8).
		if action.progress != "" {
			b.table("ActionText").Add(action.name, action.progress, nil)
		}
	}

	return nil
}

// sequenceError reports a numbering error at the YAML placement of the
// action it names, or of the other action involved when the first one is
// not the user's (a standard action or one the builder adds), else at the
// first user placement in the chain of either action. An error that leads
// to none falls back to the customActions section.
func (b *build) sequenceError(table string, err error) {
	var serr *sequence.Error
	if !errors.As(err, &serr) {
		b.errorf("customActions", "%s: %v", table, err)

		return
	}

	path := b.userPath(table, serr.Action)
	if path == "" {
		path = b.userPath(table, serr.Other)
	}

	if path == "" {
		path = b.chainPlacement(table, serr.Action, serr.Other)
	}

	if path == "" {
		path = "customActions"
	}

	b.errorf(path, "%s: %s", table, serr.Reason)
}

// chainPlacement returns the YAML path of the first user placement in the
// chain of an action the builder added, the chain whose anchors end at the
// same action, trying the actions in turn; "" when no chain has one.
func (b *build) chainPlacement(table string, actions ...string) string {
	anchors := make(map[string]string, len(b.schedules[table]))
	before := make(map[string]bool, len(b.schedules[table]))

	for _, a := range b.schedules[table] {
		anchors[a.Name] = a.Before + a.After
		before[a.Name] = a.Before != ""
	}

	chains := &chainRoots{anchors: anchors, before: before, roots: make(map[string]string, len(anchors))}

	user := make(map[string]bool, len(b.cfg.CustomActions))
	for _, custom := range b.cfg.CustomActions {
		user[custom.ID] = true

		if custom.Run != nil {
			user[custom.ID+".Data"] = true
		}
	}

	first := make(map[string]string)

	for _, a := range b.schedules[table] {
		if root := chains.root(a.Name); user[a.Name] && first[root] == "" {
			first[root] = a.Name
		}
	}

	for _, action := range actions {
		if name := first[chains.root(action)]; action != "" && name != "" {
			return b.placementPath(table, name)
		}
	}

	return ""
}

// chainRoots finds the action that ends each chain of anchors.
type chainRoots struct {
	anchors map[string]string
	before  map[string]bool
	roots   map[string]string
}

// root names the chain of an action: the action its anchors end at, and
// whether the chain runs before or after it, since the two sides take
// different numbers. Each name is resolved once, and a cycle ends where it
// closes. An action with no anchor is its own chain.
func (c *chainRoots) root(name string) string {
	seen := make(map[string]bool)

	var path []string

	cur := name
	for c.anchors[cur] != "" && c.roots[cur] == "" && !seen[cur] {
		seen[cur] = true
		path = append(path, cur)
		cur = c.anchors[cur]
	}

	key := cur

	switch {
	case c.roots[cur] != "":
		key = c.roots[cur]
	case len(path) > 0 && c.before[path[len(path)-1]]:
		key = cur + " before"
	case len(path) > 0:
		key = cur + " after"
	}

	for _, n := range path {
		c.roots[n] = key
	}

	return key
}

// placementPath returns the YAML path of the placement that schedules action
// in table: that of a custom action, or of the run action whose data action
// it is; "" for any other action.
// An action generated from a service argument gets the index it has in
// cfg.CustomActions; config.Validate, which knows its origin, reports the
// conflicts it can take part in first, and only a conflict the build alone
// sees could reach one through chainPlacement.
func (b *build) placementPath(table, action string) string {
	if action == "" {
		return ""
	}

	id, isData := strings.CutSuffix(action, ".Data")

	for i, custom := range b.cfg.CustomActions {
		if custom.ID != action && (!isData || custom.ID != id || custom.Run == nil) {
			continue
		}

		for j, placement := range custom.Schedule {
			if t, ok := config.SequenceTable(placement.Sequence); ok && t == table {
				return fmt.Sprintf("customActions[%d].schedule[%d]", i, j)
			}
		}
	}

	return ""
}

// checkSequences applies the ordering rules that numbering cannot: ICE77
// for script actions and the RemoveExistingProducts windows (ICE63).
func (b *build) checkSequences() { //nolint:cyclop // one pass over the format; splitting would scatter it
	for _, table := range sequenceTables {
		seq := b.numbered[table]
		init := sequence.Find(seq, actInstallInitialize)
		fin := sequence.Find(seq, actInstallFinalize)

		for _, numbered := range seq {
			a := b.action(numbered.Name)
			if a == nil || a.typ&caDeferred == 0 {
				continue
			}

			if table != seqInstallExecute && table != seqAdminExecute {
				b.errorf("customActions", "%s: deferred action %s belongs in an execute sequence (ICE77)", table, numbered.Name)

				continue
			}

			// Find returns 0 for an action that is not scheduled.
			if init == 0 || fin == 0 || numbered.Sequence <= init || numbered.Sequence >= fin {
				b.errorf("customActions", "%s: deferred action %s at %d must run between InstallInitialize and InstallFinalize (ICE77)",
					table, numbered.Name, numbered.Sequence)
			}
		}
	}

	seq := b.numbered[seqInstallExecute]

	// Find returns 0 for an action that is not scheduled, as in a package
	// without upgrade.
	rep := sequence.Find(seq, "RemoveExistingProducts")
	if rep == 0 {
		return
	}

	validate := sequence.Find(seq, actInstallValidate)
	init := sequence.Find(seq, actInstallInitialize)
	fin := sequence.Find(seq, actInstallFinalize)
	next := func(after int) int {
		if after == 0 {
			return -2
		}

		for _, a := range seq {
			if a.Sequence > after {
				return a.Sequence
			}
		}

		return -1
	}

	ok := (rep > validate && rep < init) || // window 1
		rep == next(init) || // window 2
		rep == next(sequence.Find(seq, "InstallExecute")) || // window 3
		rep == next(sequence.Find(seq, "InstallExecuteAgain")) ||
		fin != 0 && rep > fin // window 4

	if !ok {
		b.errorf("upgrade.removeOld", "RemoveExistingProducts at %d is outside its four windows (ICE63); the engine fails with 2613", rep)
	}
}
