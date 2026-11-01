package builder

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jkroepke/msi-builder/internal/condition"
	"github.com/jkroepke/msi-builder/pkg/config"
)

const (
	// regTypeKey marks a registry row that creates a key without a value.
	regTypeKey = "key"
	// The registry roots of the Registry table.
	rootHKMU = "HKMU"
	rootHKCR = "HKCR"
	rootHKCU = "HKCU"
	rootHKLM = "HKLM"
	rootHKU  = "HKU"
	// envMachine is the scope of a system environment variable, envUser
	// of a user one and envAuto of one that follows the context of a dual
	// package.
	envMachine = "machine"
	envUser    = "user"
	envAuto    = "auto"
	// ALLUSERS is 1 in a per-machine installation and unset in a per-user
	// one, also in a dual package after the context is chosen (INST-02).
	condPerMachine = "ALLUSERS=1"
	condPerUser    = "NOT ALLUSERS"
)

var registryRoots = map[string]int{rootHKMU: -1, rootHKCR: 0, rootHKCU: 1, rootHKLM: 2, rootHKU: 3} //nolint:gochecknoglobals // lookup table

// registryID returns the Registry key of a value: the explicit id, else one
// generated from the value's identity.
func (b *build) registryID(info *componentInfo, reg *config.RegistryValue) string {
	if reg.ID != "" {
		return reg.ID
	}

	// A key row's Name column is * or +, so it must not share the id of the
	// key's default value.
	name := strings.ToLower(reg.Name)
	if reg.Type == regTypeKey {
		name = registryKeyName(reg)
	}

	return generatedID("reg", "Registry", info.cfg.ID, reg.Root, strings.ToLower(reg.Key), name)
}

// registryKeyName is the Name column of a type key row: * also deletes the
// key on uninstall, + only creates it.
func registryKeyName(r *config.RegistryValue) string {
	if r.RemoveOnUninstall {
		return "*"
	}

	return "+"
}

// registryValue encodes a typed value the way the Registry table expects
// (section 8.13): #n for REG_DWORD, #x for REG_BINARY, #% for
// REG_EXPAND_SZ, [~]-joined strings for REG_MULTI_SZ, ## for a literal #.
func registryValue(reg *config.RegistryValue) any { //nolint:cyclop // one pass over the format; splitting would scatter it
	switch reg.Type {
	case regTypeKey:
		return nil
	case "dword":
		v := reg.Value[0]
		if strings.Contains(v, "[") {
			return "#" + v
		}

		n, _ := strconv.ParseInt(v, 0, 64)

		return "#" + strconv.FormatInt(n, 10)
	case "binary":
		h := strings.TrimPrefix(strings.ToLower(reg.Value[0]), "0x")

		return "#x" + h
	case "expandString":
		return "#%" + reg.Value[0]
	case "multiString":
		joined := strings.Join(reg.Value, "[~]")

		switch {
		case reg.Append:
			return "[~]" + joined
		case reg.Prepend:
			return joined + "[~]"
		default:
			return "[~]" + joined + "[~]"
		}
	default:
		v := reg.Value[0]
		if strings.HasPrefix(v, "#") {
			return "#" + v
		}

		return nullable(v)
	}
}

func (b *build) writeRegistry(info *componentInfo) {
	tbl := b.table("Registry")
	comp := info.cfg

	for _, reg := range comp.Registry {
		var name any

		if reg.Type == regTypeKey {
			name = registryKeyName(reg)
		} else {
			name = nullable(reg.Name)
		}

		tbl.Add(b.registryID(info, reg), registryRoots[reg.Root], reg.Key, name, registryValue(reg), comp.ID)
	}

	// RemoveRegistryValues runs before WriteRegistryValues, so a key that is
	// removed and written again ends up with only the package's values.
	for _, entry := range comp.RemoveRegistry {
		if entry.On != config.RemoveOnUninstall {
			key := entry.ID
			if key == "" {
				key = generatedID("rreg", "RemoveRegistry", comp.ID, entry.Root, strings.ToLower(entry.Key), strings.ToLower(entry.Name))
			}

			name := entry.Name
			if name == "" {
				name = "-"
			}

			b.table("RemoveRegistry").Add(key, registryRoots[entry.Root], entry.Key, name, comp.ID)
		}

		// A Registry row named - with a Null value never creates the key
		// and deletes it with its subkeys when the component is removed
		// (REG-08).
		if entry.On == config.RemoveOnUninstall || entry.On == config.RemoveOnBoth {
			key := cmp.Or(entry.ID, generatedID("reg", "Registry", comp.ID, entry.Root, strings.ToLower(entry.Key), "-"))
			tbl.Add(key, registryRoots[entry.Root], entry.Key, "-", nil, comp.ID)
		}
	}

	for _, e := range comp.EventLog {
		key := eventLogKey(e)
		tbl.Add(eventLogID(comp, e), registryRoots[rootHKLM], key, "EventMessageFile",
			"#%"+e.MessageFile, comp.ID)
		tbl.Add(generatedID("reg", "EventLog", comp.ID, e.Log, e.Source, "TypesSupported"), registryRoots[rootHKLM], key, "TypesSupported",
			"#"+strconv.Itoa(*e.TypesSupported), comp.ID)
	}
}

// eventLogKey is the HKLM key of an event log source.
func eventLogKey(e *config.EventSource) string {
	return `SYSTEM\CurrentControlSet\Services\EventLog\` + e.Log + `\` + e.Source
}

// eventLogID is the Registry key of a source's EventMessageFile row, which
// is the key path of a component that has no files or registry entries.
func eventLogID(comp *config.Component, e *config.EventSource) string {
	return generatedID("reg", "EventLog", comp.ID, e.Log, e.Source, "EventMessageFile")
}

// writeEnvironment emits Environment rows. The removal flag is always
// written explicitly, because the machine-scope flag does not imply it
// (MSISPEC-RESULTS ENVVAR-01).
//
// msiexec applies the rows in table order, which is the order of their
// keys (ENVVAR-04), so that two parts of one variable come out in the
// order the rows are applied in. The keys number the variables of the
// package in config order (Config.Components: top-level components, then
// those defined inside features), padded to one width, so that the table
// keeps that order across components too: a later append goes behind an
// earlier one and a later prepend in front of it.
func (b *build) writeEnvironment(info *componentInfo) {
	// Every package has the table, also without rows.
	b.table("Environment")

	comp := info.cfg

	for _, env := range comp.Environment {
		key := environmentKey(b.environments, b.cfg.Components)
		b.environments++

		if env.Scope != envAuto {
			b.environmentRow(key, env, env.Scope, comp.ID)

			continue
		}

		// A variable that follows the context is a machine variable in a
		// component installed only per machine and a user variable in one
		// installed only per user (ENVVAR-03).
		for _, scope := range []string{envMachine, envUser} {
			b.environmentRow(key+scope[:1], env, scope, b.environmentCarrier(info, scope))
		}
	}
}

// environmentKey returns the Environment key of the variable with index i
// of components, in config order: env and i, padded to the digits of the
// number of variables, so that the keys sort like the indexes.
func environmentKey(i int, components []*config.Component) string {
	total := 0
	for _, comp := range components {
		total += len(comp.Environment)
	}

	return fmt.Sprintf("env%0*d", len(strconv.Itoa(total)), i)
}

// environmentRow writes the Environment row key of env in scope for
// component.
func (b *build) environmentRow(key string, env *config.Environment, scope, component string) {
	var prefix string

	switch env.Action {
	case "setIfAbsent":
		prefix = "+"
	case "remove":
		prefix = "!"
	default:
		prefix = "="
	}

	if *env.RemoveOnUninstall {
		prefix += "-"
	}

	if scope == envMachine {
		prefix += "*"
	}

	var value any

	switch env.Action {
	case "append":
		value = "[~];" + env.Value
	case "prepend":
		value = env.Value + ";[~]"
	default:
		value = nullable(env.Value)
	}

	b.table("Environment").Add(key, prefix+env.Name, value, component)
}

// environmentCarrier returns the component that installs a dual package's
// variables that follow the context, in scope: it installs with the
// component of info, in its features, but only in that context, since a
// machine variable in a per-user installation would need elevation. Its
// key path is a registry value in the context's root, named after the
// carrier, and it lies in TARGETDIR, so that the carrier of a component in
// a profile folder needs no HKCU key path in both contexts (ICE38).
func (b *build) environmentCarrier(info *componentInfo, scope string) string {
	comp := info.cfg
	carrier := generatedID("cmp", "Environment", b.upgradeCode, comp.ID, scope)

	if slices.Contains(b.carriers[comp.ID], carrier) {
		return carrier
	}

	registry := generatedID("reg", "Environment", b.upgradeCode, comp.ID, scope)

	bitness := "32"
	attributes := compRegistryKeyPath | info.attributes&(compPermanent|compTransitive)

	if info.attributes&comp64Bit != 0 {
		bitness = "64"
		attributes |= comp64Bit
	}

	guid := uuid5(namespaceComponent, b.upgradeCode+"|"+bitness+"|environment|"+carrier)
	root, context := rootHKMU, condPerMachine

	if scope == envUser {
		root, context = rootHKCU, condPerUser
	}

	cond := context
	if comp.Condition != "" {
		cond = "(" + comp.Condition + ") AND " + cond
	}

	b.table("Component").Add(carrier, guid, targetDir, attributes, cond, registry)
	b.table("Registry").Add(registry, registryRoots[root], `Software\[Manufacturer]\[ProductName]`, carrier, "#1", carrier)
	b.carriers[comp.ID] = append(b.carriers[comp.ID], carrier)

	return carrier
}

// componentCondition is the Component.Condition of comp: its condition,
// and in a dual package the context its scope limits it to.
func componentCondition(comp *config.Component) any {
	var context string

	switch comp.Scope {
	case config.ScopePerMachine:
		context = condPerMachine
	case config.ScopePerUser:
		context = condPerUser
	case "", config.ScopePerUserOrMachine, config.ScopePerMachineOrUser:
		return nullable(comp.Condition)
	}

	if comp.Condition == "" {
		return context
	}

	return "(" + comp.Condition + ") AND " + context
}

// Service constants.
const (
	serviceOwnProcess   = 0x10
	serviceShareProcess = 0x20
	startAuto           = 2
	startDemand         = 3
	startDisabled       = 4
	errorIgnore         = 0
	errorNormal         = 1
	errorCritical       = 3
	errorVital          = 0x8000

	eventStartInstall    = 1
	eventStopInstall     = 2
	eventDeleteInstall   = 8
	eventStartUninstall  = 16
	eventStopUninstall   = 32
	eventDeleteUninstall = 128

	configEventInstall    = 1
	configEventReinstall  = 4
	configDelayedStart    = 3
	configFailureNonCrash = 4
	configSidType         = 5
	configPreshutdown     = 7
)

// serviceSidType returns the SERVICE_SID_TYPE value of a sidType for a
// MsiServiceConfig row (SVC-10).
func serviceSidType(sidType string) string {
	switch sidType {
	case "unrestricted":
		return "1"
	case "restricted":
		return "3"
	default:
		return "0"
	}
}

func (b *build) writeServices(info *componentInfo) { //nolint:cyclop // one pass over the format; splitting would scatter it
	comp := info.cfg

	for _, svc := range comp.Services {
		installID := b.componentServiceID("svi", comp, svc.Name)

		typ := serviceOwnProcess
		if svc.Type == "shareProcess" {
			typ = serviceShareProcess
		}

		start := startAuto

		switch svc.Start {
		case "demand":
			start = startDemand
		case "disabled":
			start = startDisabled
		}

		errCtl := errorNormal

		switch svc.ErrorControl {
		case "ignore":
			errCtl = errorIgnore
		case "critical":
			errCtl = errorCritical
		}

		if svc.Vital {
			errCtl |= errorVital
		}

		var deps any
		if len(svc.Dependencies) > 0 {
			deps = strings.Join(svc.Dependencies, "[~]") + "[~][~]"
		}

		b.table("ServiceInstall").Add(installID, svc.Name, nullable(svc.DisplayName), typ, start, errCtl, nil, deps,
			nullable(svc.Account), nullable(svc.Password), nullable(svc.Arguments.String()), comp.ID, nullable(svc.Description))

		if svc.Password != "" {
			for _, prop := range propertyRefs(svc.Password) {
				b.hidden[prop] = true
			}
		}

		b.serviceControl(info, svc)

		if svc.DelayedAutoStart {
			b.table("MsiServiceConfig").Add(b.componentServiceID("scd", comp, svc.Name), svc.Name,
				configEventInstall|configEventReinstall, configDelayedStart, "1", comp.ID)
		}

		if svc.FailureActionsOnNonCrashFailures {
			b.table("MsiServiceConfig").Add(b.componentServiceID("scf", comp, svc.Name), svc.Name,
				configEventInstall|configEventReinstall, configFailureNonCrash, "1", comp.ID)
		}

		if svc.SidType != "" {
			b.table("MsiServiceConfig").Add(b.componentServiceID("scsid", comp, svc.Name), svc.Name,
				configEventInstall|configEventReinstall, configSidType, serviceSidType(svc.SidType), comp.ID)
		}

		if svc.PreshutdownTimeout != nil {
			ms := int64(time.Duration(*svc.PreshutdownTimeout) / time.Millisecond)
			b.table("MsiServiceConfig").Add(b.componentServiceID("scpre", comp, svc.Name), svc.Name,
				configEventInstall|configEventReinstall, configPreshutdown, strconv.FormatInt(ms, 10), comp.ID)
		}

		if svc.Recovery != nil {
			b.recoveryAction(info, svc)
		}
	}
}

// serviceControl writes the ServiceControl rows of a service. Wait is a
// column of the row, so a start that waits differently from the stop and
// delete events gets a row of its own. A start with a condition goes to a
// component of its own, since the row has no condition column (SVC-08).
func (b *build) serviceControl(info *componentInfo, svc *config.Service) {
	ctl := svc.Control
	event, startEvent := serviceEvents(ctl)

	wait, waitForStart := *ctl.Wait, *ctl.Wait
	if ctl.WaitForStart != nil {
		waitForStart = *ctl.WaitForStart
	}

	if startEvent != 0 && ctl.StartCondition != "" {
		carrier := b.startCarrier(info, svc)
		b.table("ServiceControl").Add(b.componentServiceID("scs", info.cfg, svc.Name), svc.Name, startEvent, nil, waitColumn(waitForStart), carrier)
		startEvent = 0
	}

	switch {
	case startEvent == 0:
	case event == 0:
		event, startEvent, wait = startEvent, 0, waitForStart
	case waitForStart == wait:
		event, startEvent = event|startEvent, 0
	}

	if event != 0 {
		b.table("ServiceControl").Add(b.componentServiceID("sc", info.cfg, svc.Name), svc.Name, event, nil, waitColumn(wait), info.cfg.ID)
	}

	if startEvent != 0 {
		b.table("ServiceControl").Add(b.componentServiceID("scs", info.cfg, svc.Name), svc.Name, startEvent, nil, waitColumn(waitForStart), info.cfg.ID)
	}
}

// serviceEvents returns the stop and delete events and the start event that
// a service's control settings select.
func serviceEvents(ctl config.ServiceControl) (int, int) {
	event, startEvent := 0, 0

	if *ctl.StartOnInstall {
		startEvent = eventStartInstall
	}

	for _, set := range []struct {
		on    *bool
		event int
	}{
		{ctl.StopOnInstall, eventStopInstall},
		{ctl.DeleteOnInstall, eventDeleteInstall},
		{ctl.StopOnUninstall, eventStopUninstall},
		{ctl.DeleteOnUninstall, eventDeleteUninstall},
	} {
		if set.on != nil && *set.on {
			event |= set.event
		}
	}

	return event, startEvent
}

// startCarrier writes the component that starts svc only when its start
// condition, and the condition of the service's component, are true at
// install time, and returns its key. It installs with the service's
// component: same folder, bitness and features. Its key path is a registry
// value named after the component, so that neither two components nor two
// products share it.
func (b *build) startCarrier(info *componentInfo, svc *config.Service) string {
	comp := info.cfg
	carrier := generatedID("cmp", "StartService", b.upgradeCode, comp.ID, svc.Name)
	registry := generatedID("reg", "StartService", b.upgradeCode, comp.ID, svc.Name)

	// Permanent and transitive follow the service's component, so a
	// transitive component's carrier is re-evaluated on a reinstall too.
	bitness := "32"
	attributes := compRegistryKeyPath | info.attributes&(compPermanent|compTransitive)

	if info.attributes&comp64Bit != 0 {
		bitness = "64"
		attributes |= comp64Bit
	}

	guid := uuid5(namespaceComponent, b.upgradeCode+"|"+bitness+"|start|"+carrier)

	cond := "(" + svc.Control.StartCondition + ")"
	if compCond, ok := componentCondition(comp).(string); ok {
		cond = "(" + compCond + ") AND " + cond
	}

	// A public property set in the wizard must reach the server, unless
	// the configuration declares it not secure.
	for _, prop := range condition.Properties(svc.Control.StartCondition) {
		if p, ok := b.cfg.Properties[prop]; config.IsPublicProperty(prop) && (!ok || p.Secure == nil || *p.Secure) {
			b.secure[prop] = true
		}
	}

	b.table("Component").Add(carrier, guid, comp.Directory, attributes, cond, registry)
	b.table("Registry").Add(registry, registryRoots[rootHKLM], `Software\[Manufacturer]\[ProductName]`, carrier, "#1", carrier)
	b.carriers[comp.ID] = append(b.carriers[comp.ID], carrier)

	return carrier
}

// controlServices writes the ServiceControl rows of the services a
// component stops, starts or deletes without installing them (SVC-07).
func (b *build) controlServices(info *componentInfo) {
	comp := info.cfg

	for _, ctl := range comp.ServiceControl {
		event := 0

		for _, set := range []struct {
			on    bool
			event int
		}{
			{ctl.StartOnInstall, eventStartInstall},
			{ctl.StopOnInstall, eventStopInstall},
			{ctl.DeleteOnInstall, eventDeleteInstall},
			{ctl.StartOnUninstall, eventStartUninstall},
			{ctl.StopOnUninstall, eventStopUninstall},
			{ctl.DeleteOnUninstall, eventDeleteUninstall},
		} {
			if set.on {
				event |= set.event
			}
		}

		key := generatedID("scx", comp.ID, strings.ToLower(ctl.Name))
		b.table("ServiceControl").Add(key, ctl.Name, event, nil, waitColumn(*ctl.Wait), comp.ID)
	}
}

// waitColumn is the ServiceControl.Wait value.
func waitColumn(wait bool) int {
	if wait {
		return 1
	}

	return 0
}

// componentServiceID is serviceID for a service of comp. Conditional
// components may install the same service in different ways (a twin that
// starts it and one that does not); the rows of such a name carry the
// component in their keys, so they do not collide.
func (b *build) componentServiceID(prefix string, comp *config.Component, name string) string {
	owners := 0

	for _, other := range b.cfg.Components {
		if slices.ContainsFunc(other.Services, func(svc *config.Service) bool { return strings.EqualFold(svc.Name, name) }) {
			owners++
		}
	}

	if owners > 1 {
		return generatedID(prefix, comp.ID, strings.ToLower(name))
	}

	return serviceID(prefix, name)
}

// serviceID builds a table key from a service name.
func serviceID(prefix, name string) string {
	if config.IsIdentifier(name) && len(prefix)+1+len(name) <= 72 {
		return prefix + "_" + name
	}

	return generatedID(prefix, name)
}

// recoveryAction schedules the sc.exe failure command that replaces the
// MsiServiceConfigFailureActions table, which fails with 1939 for restart
// actions (MSISPEC-RESULTS SVC-01).
func (b *build) recoveryAction(info *componentInfo, svc *config.Service) {
	rec := svc.Recovery

	var actions []string

	for _, a := range rec.Actions {
		switch a.Action {
		case "restart":
			delay := *rec.RestartDelay
			if a.Restart != nil {
				delay = *a.Restart
			}

			actions = append(actions, "restart/"+strconv.FormatInt(int64(time.Duration(delay)/time.Millisecond), 10))
		default:
			actions = append(actions, "none/0")
		}
	}

	reset := int64(rec.ResetAfterDays) * 86400
	if rec.ResetAfter != nil {
		reset = int64(time.Duration(*rec.ResetAfter) / time.Second)
	}

	for len(actions) < 3 {
		actions = append(actions, "none/0")
	}

	sysDir := b.systemDir()
	cmd := fmt.Sprintf(`"[%s]sc.exe" failure "%s" reset= %d actions= %s`, sysDir, svc.Name, reset, strings.Join(actions, "/"))
	name := b.componentServiceID("ServiceRecovery", info.cfg, svc.Name)

	b.addAction(&actionInfo{name: name, typ: caExec | caDeferred | caNoImpersonate | caContinue, source: sysDir, target: cmd})
	b.schedule("InstallExecuteSequence", name, "$"+info.cfg.ID+"=3", "", actInstallServices, 0)
}

// purgeAction schedules a deferred rmdir of the component's directory tree
// on a real uninstall. RemoveFile rows cannot do it: they never recurse and
// they also run on the uninstall half of a major upgrade (FILE-03).
func (b *build) purgeAction(info *componentInfo) {
	comp := info.cfg
	if !comp.Purge.Enabled() {
		return
	}

	sysDir := b.systemDir()
	// The "." keeps the trailing backslash of the folder path away from the
	// closing quote.
	cmd := fmt.Sprintf(`"[%s]cmd.exe" /c rmdir /s /q "[%s]."`, sysDir, comp.Directory)

	typ := caExec | caDeferred | caContinue
	if b.cfg.Package.Scope == config.ScopePerMachine {
		typ |= caNoImpersonate
	}

	// An empty property would leave "." alone, the working folder.
	cond := `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE AND ` + comp.Directory
	if comp.Purge.Condition != "" {
		cond += " AND (" + comp.Purge.Condition + ")"

		// FILE-03 tested the opt-in with a secure property; a public one
		// set on the client must reach the server.
		for _, prop := range condition.Properties(comp.Purge.Condition) {
			if config.IsPublicProperty(prop) {
				b.secure[prop] = true
			}
		}
	}

	name := generatedID("Purge", "Purge", comp.ID)
	if config.IsIdentifier("Purge_"+comp.ID) && len("Purge_"+comp.ID) <= 72 {
		name = "Purge_" + comp.ID
	}

	b.addAction(&actionInfo{name: name, typ: typ, source: sysDir, target: cmd})
	b.schedule(seqInstallExecute, name, cond, actInstallFinalize, "", 0)
}

// systemDir returns the Directory key of the system folder that holds the
// native command-line tools for this package's bitness.
func (b *build) systemDir() string {
	dirID := "SystemFolder"
	if b.is64 {
		dirID = "System64Folder"
	}

	b.ensureStandardDir(dirID)

	return dirID
}

// propertyRefs lists the [PROPERTY] references in a formatted string.
func propertyRefs(text string) []string {
	var out []string

	for {
		i := strings.IndexByte(text, '[')
		if i < 0 {
			return out
		}

		end := strings.IndexByte(text[i:], ']')
		if end < 0 {
			return out
		}

		name := text[i+1 : i+end]
		if config.IsIdentifier(name) {
			out = append(out, name)
		}

		text = text[i+end+1:]
	}
}
