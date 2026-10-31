package builder

import (
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
	seqInstallExecute = "InstallExecuteSequence"
	seqInstallUI      = "InstallUISequence"
	seqAdminExecute   = "AdminExecuteSequence"
	seqAdminUI        = "AdminUISequence"
	seqAdvtExecute    = "AdvtExecuteSequence"
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
	actDuplicateFiles       = "DuplicateFiles"

	condNotInstalled = "NOT Installed"
	condRemoveAll    = `REMOVE~="ALL"`
	condVersionNT    = "VersionNT"
	fwAny            = "any"
	targetDir        = "TARGETDIR"
)

var sequenceTables = []string{seqInstallExecute, seqInstallUI, seqAdminExecute, seqAdminUI, seqAdvtExecute} //nolint:gochecknoglobals // lookup table

// standardAction is a standard action's default number per table (section
// 9.2) and its condition.
type standardAction struct {
	condition string
	ies       int
	ius       int
	aes       int
	aus       int
	advt      int
}

func (s standardAction) number(table string) int {
	switch table {
	case seqInstallExecute:
		return s.ies
	case seqInstallUI:
		return s.ius
	case seqAdminExecute:
		return s.aes
	case seqAdminUI:
		return s.aus
	default:
		return s.advt
	}
}

// standardActions holds the default numbers of every standard action this
// builder may schedule.
var standardActions = map[string]standardAction{ //nolint:gochecknoglobals // lookup table
	"FindRelatedProducts":      {ies: 25, ius: 25},
	"AppSearch":                {ies: 50, ius: 50},
	"LaunchConditions":         {ies: 100, ius: 100, aes: 100, aus: 100},
	"CCPSearch":                {ies: 500, ius: 500, condition: condNotInstalled},
	"RMCCPSearch":              {ies: 600, ius: 600, condition: condNotInstalled},
	actValidateProductID:       {ies: 700, ius: 700},
	actCostInitialize:          {ies: 800, ius: 800, aes: 800, aus: 800, advt: 800},
	actFileCost:                {ies: 900, ius: 900, aes: 900, aus: 900},
	"IsolateComponents":        {ies: 950, ius: 950},
	actCostFinalize:            {ies: 1000, ius: 1000, aes: 1000, aus: 1000, advt: 1000},
	"SetODBCFolders":           {ies: 1100},
	"MigrateFeatureStates":     {ies: 1200, ius: 1200},
	actExecuteAction:           {ius: 1300, aus: 1300},
	actInstallValidate:         {ies: 1400, aes: 1400, advt: 1400},
	"RemoveExistingProducts":   {ies: 1401},
	actInstallInitialize:       {ies: 1500, aes: 1500, advt: 1500},
	"AllocateRegistrySpace":    {ies: 1550, condition: condNotInstalled},
	"ProcessComponents":        {ies: 1600},
	"UnpublishComponents":      {ies: 1700},
	"MsiUnpublishAssemblies":   {ies: 1750},
	"UnpublishFeatures":        {ies: 1800},
	actStopServices:            {ies: 1900, condition: condVersionNT},
	actDeleteServices:          {ies: 2000, condition: condVersionNT},
	"UnregisterComPlus":        {ies: 2100},
	"SelfUnregModules":         {ies: 2200},
	"UnregisterTypeLibraries":  {ies: 2300},
	"RemoveODBC":               {ies: 2400},
	"UnregisterFonts":          {ies: 2500},
	actRemoveRegistryValues:    {ies: 2600},
	"UnregisterClassInfo":      {ies: 2700},
	"UnregisterExtensionInfo":  {ies: 2800},
	"UnregisterProgIdInfo":     {ies: 2900},
	"UnregisterMIMEInfo":       {ies: 3000},
	"RemoveIniValues":          {ies: 3100},
	"RemoveShortcuts":          {ies: 3200},
	"RemoveEnvironmentStrings": {ies: 3300},
	"RemoveDuplicateFiles":     {ies: 3400},
	actRemoveFiles:             {ies: 3500},
	actRemoveFolders:           {ies: 3600},
	"CreateFolders":            {ies: 3700},
	"MoveFiles":                {ies: 3800},
	"InstallAdminPackage":      {aes: 3900},
	actInstallFiles:            {ies: 4000, aes: 4000},
	"PatchFiles":               {ies: 4090, aes: 4090},
	actDuplicateFiles:          {ies: 4210},
	"BindImage":                {ies: 4300},
	"CreateShortcuts":          {ies: 4500, advt: 4500},
	"RegisterClassInfo":        {ies: 4600, advt: 4600},
	"RegisterExtensionInfo":    {ies: 4700, advt: 4700},
	"RegisterProgIdInfo":       {ies: 4800, advt: 4800},
	"RegisterMIMEInfo":         {ies: 4900, advt: 4900},
	actWriteRegistryValues:     {ies: 5000},
	"WriteIniValues":           {ies: 5100},
	"WriteEnvironmentStrings":  {ies: 5200},
	"RegisterFonts":            {ies: 5300},
	"InstallODBC":              {ies: 5400},
	"RegisterTypeLibraries":    {ies: 5500},
	"SelfRegModules":           {ies: 5600},
	"RegisterComPlus":          {ies: 5700},
	actInstallServices:         {ies: 5800, condition: condVersionNT},
	"MsiConfigureServices":     {ies: 5850, condition: "VersionNT>=600"},
	actStartServices:           {ies: 5900, condition: condVersionNT},
	"RegisterUser":             {ies: 6000},
	"RegisterProduct":          {ies: 6100},
	"PublishComponents":        {ies: 6200, advt: 6200},
	"MsiPublishAssemblies":     {ies: 6250, advt: 6250},
	actPublishFeatures:         {ies: 6300, advt: 6300},
	actPublishProduct:          {ies: 6400, advt: 6400},
	actScheduleReboot:          {},
	"InstallExecute":           {ies: 6500, condition: condNotInstalled},
	"InstallExecuteAgain":      {ies: 6550, condition: condNotInstalled},
	actInstallFinalize:         {ies: 6600, aes: 6600, advt: 6600},
}

// alwaysActions are the standard actions every product gets, per table
// (sections 9.2 and 11.7).
var alwaysActions = map[string][]string{ //nolint:gochecknoglobals // lookup table
	seqInstallExecute: {
		actValidateProductID, actCostInitialize, actFileCost, actCostFinalize, actInstallValidate,
		actInstallInitialize, "ProcessComponents", "UnpublishFeatures", "RegisterUser",
		"RegisterProduct", actPublishFeatures, actPublishProduct, actInstallFinalize,
	},
	seqInstallUI: {actValidateProductID, actCostInitialize, actFileCost, actCostFinalize, actExecuteAction},
	seqAdminExecute: {
		actCostInitialize, actFileCost, actCostFinalize, actInstallValidate, actInstallInitialize, "InstallAdminPackage", actInstallFiles, actInstallFinalize,
	},
	seqAdminUI:     {actCostInitialize, actFileCost, actCostFinalize, actExecuteAction},
	seqAdvtExecute: {actCostInitialize, actCostFinalize, actInstallValidate, actInstallInitialize, actPublishFeatures, actPublishProduct, actInstallFinalize},
}

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

// standardAt adds a standard action at its default number for the table
// unless it is already scheduled there. It panics on an action without a
// default in that table, which is a builder bug.
func (b *build) standardAt(table, name string) {
	if b.scheduled(table, name) {
		return
	}

	s, ok := standardActions[name]
	if !ok || s.number(table) == 0 {
		panic(fmt.Sprintf("builder: %s has no default number in %s", name, table))
	}

	b.schedule(table, name, s.condition, "", "", s.number(table))
}

// anchorActions adds a standard action that an action is scheduled
// relative to, but that no table needs, at its default number.
func (b *build) anchorActions() {
	for _, table := range sequenceTables {
		for _, action := range b.schedules[table] {
			anchor := action.Before + action.After
			if s, ok := standardActions[anchor]; ok && s.number(table) != 0 {
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
		b.errorf("binaries."+cadll.Key, "the key %s is reserved for the custom action DLL of run actions, permission grants and close", cadll.Key)
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

// firewall writes the netsh actions of the firewall helper (section 17.3,
// design section 4.11): on install and repair every rule is deleted by name
// before any is added, with a rollback that deletes again; on removal the
// rule is deleted, with a rollback that adds it back.
func (b *build) firewall() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	if len(b.cfg.Firewall) == 0 {
		return nil
	}

	sysDir := b.systemDir()
	netsh := fmt.Sprintf(`"[%s]netsh.exe" advfirewall firewall `, sysDir)

	type rule struct {
		id, add, del, install, remove string
	}

	rules := make([]rule, 0, len(b.cfg.Firewall))

	for _, cfgRule := range b.cfg.Firewall {
		ruleID := generatedID("", "Firewall", cfgRule.Name, cfgRule.Protocol, cfgRule.Port, cfgRule.Program)[:8]
		install := "$" + cfgRule.Component + "=3"
		remove := "$" + cfgRule.Component + "=2"

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

		var parts []string

		parts = append(parts, `name="`+cfgRule.Name+`"`, "dir=in", "action=allow", "protocol="+cfgRule.Protocol)

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

		rules = append(rules, rule{
			id:      ruleID,
			add:     netsh + "add rule " + strings.Join(parts, " "),
			del:     netsh + `delete rule name="` + cfgRule.Name + `" dir=in`,
			install: install,
			remove:  remove,
		})
	}

	deferred := caExec | caDeferred | caNoImpersonate
	rollback := caExec | caRollback | caNoImpersonate

	// Removal: rollback (add back) precedes its deferred action (delete).
	for _, r := range rules {
		b.addAction(&actionInfo{name: "FwRemoveRollback" + r.id, typ: rollback | caContinue, source: sysDir, target: r.add})
		b.addAction(&actionInfo{name: "FwRemove" + r.id, typ: deferred | caContinue, source: sysDir, target: r.del})
	}

	// Install: delete every rule, then add each with a rollback that deletes.
	for _, r := range rules {
		b.addAction(&actionInfo{name: "FwClear" + r.id, typ: deferred | caContinue, source: sysDir, target: r.del})
	}

	for _, r := range rules {
		b.addAction(&actionInfo{name: "FwAddRollback" + r.id, typ: rollback | caContinue, source: sysDir, target: r.del})
		b.addAction(&actionInfo{name: "FwAdd" + r.id, typ: deferred, source: sysDir, target: r.add})
	}

	// After InstallFiles, so that the program exists; the chain keeps the
	// definition order.
	anchor := actInstallFiles
	b.standardAt(seqInstallExecute, anchor)

	for _, r := range rules {
		b.schedule(seqInstallExecute, "FwRemoveRollback"+r.id, r.remove, "", anchor, 0)
		b.schedule(seqInstallExecute, "FwRemove"+r.id, r.remove, "", anchor, 0)
	}

	for _, r := range rules {
		b.schedule(seqInstallExecute, "FwClear"+r.id, r.install, "", anchor, 0)
	}

	for _, r := range rules {
		b.schedule(seqInstallExecute, "FwAddRollback"+r.id, r.install, "", anchor, 0)
		b.schedule(seqInstallExecute, "FwAdd"+r.id, r.install, "", anchor, 0)
	}

	return nil
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
		for _, name := range alwaysActions[table] {
			b.standardAt(table, name)
		}
	}

	if b.hasRows("LaunchCondition") {
		for _, table := range []string{seqInstallExecute, seqInstallUI} {
			// After AppSearch and the actions that apply its results when
			// they do not fit between 50 and 100, so that the gap does not
			// limit how many properties a package can search.
			if b.scheduled(table, "AppSearch") && !b.scheduled(table, "LaunchConditions") &&
				standardActions["AppSearch"].number(table)+b.anchoredTo(table, "AppSearch") >= standardActions["LaunchConditions"].number(table) {
				b.schedule(table, "LaunchConditions", standardActions["LaunchConditions"].condition, "", "AppSearch", 0)
			}

			b.standardAt(table, "LaunchConditions")
		}
	}

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
			if _, std := standardActions[action.Name]; std || b.action(action.Name) != nil || b.isDialog(action.Name) {
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
			return fmt.Errorf("%s: %w", table, err)
		}

		b.numbered[table] = numbered
		t := b.table(table)

		for _, n := range numbered {
			t.Add(n.Name, nullable(n.Condition), n.Sequence)
		}
	}

	b.checkSequences()
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
