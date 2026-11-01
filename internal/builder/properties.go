package builder

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/internal/condition"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// writeProperties fills the Property table from the product identity, the ARP
// settings and the properties section. SecureCustomProperties and
// MsiHiddenProperties are written at the end, once every contributor has
// run (finishProperties).
func (b *build) writeProperties() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	product := b.cfg.Product

	b.properties["ProductCode"] = b.productCode
	b.properties["ProductName"] = product.Name
	b.properties["ProductVersion"] = product.Version
	b.properties["ProductLanguage"] = strconv.Itoa(b.language)
	b.properties["Manufacturer"] = product.Manufacturer
	b.properties["UpgradeCode"] = b.upgradeCode

	// A dual package starts in the context its scope names first; the
	// user switches it with MSIINSTALLPERUSER (spec section 8.2, INST-02).
	switch b.cfg.Package.Scope {
	case config.ScopePerMachine:
		b.properties["ALLUSERS"] = "1"
	case config.ScopePerUserOrMachine:
		b.properties["ALLUSERS"] = "2"
		b.properties["MSIINSTALLPERUSER"] = "1"
	case config.ScopePerMachineOrUser:
		b.properties["ALLUSERS"] = "2"
	case config.ScopePerUser:
	}

	// The scope page sets MSIINSTALLPERUSER in the client; a standard user's
	// elevated install passes only restricted public properties
	// (ALLUSERS among them) and secure ones to the server.
	if b.cfg.Package.Scope.Dual() {
		b.secure["MSIINSTALLPERUSER"] = true
	}

	arp := product.ARP
	set := func(name, value string) {
		if value != "" {
			b.properties[name] = value
		}
	}

	set("ARPCOMMENTS", arp.Comments)
	set("ARPCONTACT", arp.Contact)
	set("ARPHELPLINK", arp.HelpLink)
	set("ARPURLINFOABOUT", arp.AboutLink)
	set("ARPURLUPDATEINFO", arp.UpdateLink)
	set("ARPPRODUCTICON", arp.Icon)

	if arp.NoRepair {
		b.properties["ARPNOREPAIR"] = "1"
	}

	if arp.NoModify {
		b.properties["ARPNOMODIFY"] = "1"
	}

	if arp.NoRemove {
		b.properties["ARPNOREMOVE"] = "1"
	}

	if arp.Size > 0 {
		b.properties["ARPSIZE"] = strconv.Itoa(arp.Size)
	}

	if arp.InstallLocation != "" {
		// ARPINSTALLLOCATION takes the resolved directory after costing.
		b.addAction(&actionInfo{name: "SetARPINSTALLLOCATION", typ: caSetProperty, source: "ARPINSTALLLOCATION", target: "[" + arp.InstallLocation + "]"})
		b.schedule("InstallExecuteSequence", "SetARPINSTALLLOCATION", "", "", actCostFinalize, 0)
		b.schedule("InstallUISequence", "SetARPINSTALLLOCATION", "", "", actCostFinalize, 0)
	}

	for _, name := range b.cfg.Properties.Names() {
		prop := b.cfg.Properties[name]

		// A searched property gets its default from an action after the
		// search, so that the guard can tell a command-line value from it.
		if prop.Value != "" && prop.Search == nil {
			b.properties[name] = prop.Value
		}

		public := config.IsPublicProperty(name)
		if (prop.Secure == nil && public) || (prop.Secure != nil && *prop.Secure) {
			b.secure[name] = true
		}

		if prop.Hidden {
			b.hidden[name] = true
		}
	}

	return nil
}

// finishProperties writes the Property table rows.
func (b *build) finishProperties() {
	if len(b.secure) > 0 {
		b.properties["SecureCustomProperties"] = strings.Join(sortedKeys(b.secure), ";")
	}

	if len(b.hidden) > 0 {
		b.properties["MsiHiddenProperties"] = strings.Join(sortedKeys(b.hidden), ";")
	}

	t := b.table("Property")

	for _, name := range sortedStringKeys(b.properties) {
		t.Add(name, b.properties[name])
	}
}

// Upgrade attribute bits (section 8.10).
const (
	upgradeMigrateFeatures     = 1
	upgradeOnlyDetect          = 2
	upgradeIgnoreRemoveFailure = 4
	upgradeVersionMinInclusive = 256
	upgradeVersionMaxInclusive = 512
	upgradeLanguagesExclusive  = 1024
)

// removeAttributes returns the attribute bits of an Upgrade row whose
// products RemoveExistingProducts removes.
func removeAttributes(upg *config.Upgrade) int {
	attrs := 0
	if *upg.MigrateFeatures {
		attrs |= upgradeMigrateFeatures
	}

	if upg.IgnoreRemoveFailure {
		attrs |= upgradeIgnoreRemoveFailure
	}

	return attrs
}

// upgrade writes the Upgrade rows and schedules the upgrade actions.
func (b *build) upgrade() error { //nolint:cyclop // one pass over the format; splitting would scatter it
	upg := b.cfg.Upgrade
	if upg == nil || upg.Disabled {
		return nil
	}

	tbl := b.table("Upgrade")
	version := b.cfg.Product.Version

	// Older (and, with allowSameVersion, equal) versions are removed,
	// down to minVersion; an older one stays side by side (UPG-13).
	attrs := removeAttributes(upg)

	if *upg.AllowSameVersion {
		attrs |= upgradeVersionMaxInclusive
	}

	if upg.MinVersion != "" && upg.IncludeMin != nil && *upg.IncludeMin {
		attrs |= upgradeVersionMinInclusive
	}

	tbl.Add(b.upgradeCode, nullable(upg.MinVersion), version, nil, attrs, nil, upg.Property)
	b.secure[upg.Property] = true

	if upg.Downgrade != "allow" {
		tbl.Add(b.upgradeCode, version, nil, nil, upgradeOnlyDetect, nil, upg.NewerProperty)
		b.secure[upg.NewerProperty] = true

		if upg.Downgrade == "error" {
			b.table("LaunchCondition").Add("NOT "+upg.NewerProperty, upg.DowngradeMessage)
		}
	}

	for i, related := range upg.Related {
		code, err := config.NormalizeGUID(related.UpgradeCode)
		if err != nil {
			return fmt.Errorf("upgrade.related[%d]: %w", i, err)
		}

		attrs := 0

		if related.IncludeMin != nil && *related.IncludeMin {
			attrs |= upgradeVersionMinInclusive
		}

		if related.IncludeMax {
			attrs |= upgradeVersionMaxInclusive
		}

		if related.OnlyDetect {
			attrs |= upgradeOnlyDetect
		} else {
			attrs |= removeAttributes(upg)
		}

		tbl.Add(code, nullable(related.MinVersion), nullable(related.MaxVersion), nil, attrs, nil, related.Property)
		b.secure[related.Property] = true
	}

	for _, seq := range []string{"InstallExecuteSequence", "InstallUISequence"} {
		b.standardAt(seq, "FindRelatedProducts")

		if *upg.MigrateFeatures {
			b.standardAt(seq, "MigrateFeatureStates")
		}
	}

	switch upg.RemoveOld {
	case "afterInstallValidate":
		b.schedule("InstallExecuteSequence", "RemoveExistingProducts", "", "", actInstallValidate, 0)
	case "afterInstallExecute":
		b.standardAt("InstallExecuteSequence", "InstallExecute")
		b.schedule("InstallExecuteSequence", "RemoveExistingProducts", "", "", "InstallExecute", 0)
	case "afterInstallFinalize":
		b.schedule("InstallExecuteSequence", "RemoveExistingProducts", "", "", actInstallFinalize, 0)
	default:
		b.schedule("InstallExecuteSequence", "RemoveExistingProducts", "", "", actInstallInitialize, 0)
	}

	return nil
}

// launchConditions writes LaunchCondition rows after parsing each condition,
// because the engine ignores one that does not parse (section 9.1).
func (b *build) launchConditions() error {
	tbl := b.table("LaunchCondition")

	for i, launch := range b.cfg.LaunchConditions {
		p := fmt.Sprintf("launchConditions[%d]", i)

		if err := condition.Check(launch.Condition); err != nil {
			b.errorf(p+".condition", "%v; the engine ignores a launch condition that does not parse", err)

			continue
		}

		tbl.Add(launch.Condition, launch.Message)
	}

	return nil
}

// Registry locator types (RegLocator.Type).
const (
	regLocatorDirectory = 0
	regLocatorRaw       = 2
	regLocator64Bit     = 16
)

// rememberInstallDir reads back the folder installDir.remember stored. The
// search fills a separate public property, and a type-51 action copies it
// into the install dir only when the command line did not set one: SEARCH-03
// showed that searching straight into the install dir overrides a command
// line value, and that AppSearch fails with 2228 without a Signature table,
// even for a registry search. It is a directory search, which finds only a
// folder that exists: a raw search applied a folder on a drive that is gone,
// and the install failed with 1327 and 1603 (SEARCH-07). Both run in the UI
// and the execute sequence, so the wizard shows the remembered folder (UI-08).
func (b *build) rememberInstallDir() error {
	install := b.cfg.InstallDir
	if !install.Remember {
		return nil
	}

	typ := regLocatorDirectory
	if b.is64 && !remember32Bit(b.cfg) {
		typ |= regLocator64Bit
	}

	b.table("Signature")

	if !b.cfg.Package.Scope.Dual() {
		root := regRootLocalMachine
		if config.RememberRoot(b.cfg) == rootHKCU {
			root = regRootCurrentUser
		}

		b.rememberSearch(install.ID+"_REMEMBERED", config.RememberComponentID, config.RememberActionID, root, typ, "")

		return nil
	}

	// HKMU follows the context, which RegLocator cannot name: a dual
	// package searches both roots and restores the one of the context the
	// engine chose; ALLUSERS is 1 per machine and unset per user (INST-02).
	b.rememberSearch(rememberedUser(install.ID), config.RememberComponentID+"User",
		config.RememberActionID+"User", regRootCurrentUser, typ, "NOT ALLUSERS")
	b.rememberSearch(rememberedMachine(install.ID), config.RememberComponentID+"Machine",
		config.RememberActionID+"Machine", regRootLocalMachine, typ, "ALLUSERS=1")

	return nil
}

// RegLocator roots.
const (
	regRootCurrentUser  = 1
	regRootLocalMachine = 2
)

// rememberedUser and rememberedMachine name the properties a dual package
// searches the remembered install dir into.
func rememberedUser(dir string) string    { return dir + "_REMEMBERED_USER" }
func rememberedMachine(dir string) string { return dir + "_REMEMBERED_MACHINE" }

// rememberSearch reads the remembered install dir from one root into
// property, and copies it into the install dir under context.
func (b *build) rememberSearch(property, signature, action string, root, typ int, context string) {
	install := b.cfg.InstallDir.ID

	b.table("AppSearch").Add(property, signature)
	b.table("RegLocator").Add(signature, root, config.RememberKey(b.cfg), config.RememberValueName, typ)
	b.secure[property] = true

	b.addAction(&actionInfo{name: action, typ: caSetProperty, source: install, target: "[" + property + "]"})

	cond := property + " AND NOT " + install
	if context != "" {
		cond += " AND " + context
	}

	for _, seq := range []string{seqInstallExecute, seqInstallUI} {
		b.standardAt(seq, "AppSearch")
		b.schedule(seq, action, cond, "", "AppSearch", 0)
	}
}

// searchProperties writes the AppSearch and locator rows of the properties
// with a search. Each search fills a property of its own, and type-51
// actions copy the first result, or the default value, into the property
// only when it is still empty, so that a command-line value wins (SEARCH-03
// showed that a search straight into the property overrides one). Both run
// in the UI and the execute sequence; after a UI sequence the engine skips
// AppSearch in the execute sequence (reported), so the property must be
// secure to arrive there.
func (b *build) searchProperties() error {
	for _, name := range b.cfg.Properties.Names() {
		if prop := b.cfg.Properties[name]; len(prop.Search) > 0 {
			b.searchProperty(name, prop)
		}
	}

	return nil
}

// searchProperty writes the searches of one property.
func (b *build) searchProperty(name string, prop *config.Property) {
	// AppSearch fails with 2228 without a Signature table (SEARCH-03).
	b.table("Signature")

	var previous string

	for i, search := range prop.Search {
		suffix := ""
		if i > 0 {
			suffix = strconv.Itoa(i + 1)
		}

		signature := searchName("Search"+suffix+"_", name)
		found := config.SearchFoundProperty(name, i)

		if prop.Hidden {
			b.hidden[found] = true
		}

		itemPath := "properties." + name + ".search"
		if len(prop.Search) > 1 {
			itemPath += "[" + strconv.Itoa(i) + "]"
		}

		b.locate(itemPath, signature, search, false)
		b.table("AppSearch").Add(found, signature)

		apply := searchName("SearchApply"+suffix+"_", name)
		b.addAction(&actionInfo{name: apply, typ: caSetProperty, source: name, target: "[" + found + "]"})

		after := previous
		if after == "" {
			after = "AppSearch"
		}

		for _, seq := range []string{seqInstallExecute, seqInstallUI} {
			b.standardAt(seq, "AppSearch")
			b.schedule(seq, apply, found+" AND NOT "+name, "", after, 0)
		}

		previous = apply
	}

	if prop.Value != "" {
		fallback := searchName("SearchDefault_", name)
		b.addAction(&actionInfo{name: fallback, typ: caSetProperty, source: name, target: escapeFormatted(prop.Value)})

		for _, seq := range []string{seqInstallExecute, seqInstallUI} {
			b.schedule(seq, fallback, "NOT "+name, "", previous, 0)
		}
	}
}

// Locator types (RegLocator.Type, CompLocator.Type).
const (
	locatorDirectory = 0
	locatorFile      = 1
)

// locate writes the locator rows of one search under signature; path is
// the search's YAML path. A folder search (under another search) never has
// a Signature row, so the engine returns the folder.
func (b *build) locate(path, signature string, search *config.Search, folder bool) {
	switch {
	case search.Registry != nil:
		reg := search.Registry

		typ := regLocatorRaw
		if folder || reg.Type == config.SearchDirectory {
			typ = regLocatorDirectory
		}

		if reg.Bitness == "64" || reg.Bitness == "" && b.is64 {
			typ |= regLocator64Bit
		}

		b.table("RegLocator").Add(signature, registryRoots[reg.Root], reg.Key, nullable(reg.Name), typ)
	case search.Directory != nil:
		dir := search.Directory
		b.table("DrLocator").Add(signature, b.locateParent(path+".directory", signature, dir.Under), nullable(dir.Path), optionalDepth(dir.Depth))
	case search.File != nil:
		file := search.File
		b.fileSignature(signature, file)
		b.table("DrLocator").Add(signature, b.locateParent(path+".file", signature, file.Under), nullable(file.Path), optionalDepth(file.Depth))
	case search.Component != nil:
		guid, _ := config.NormalizeGUID(search.Component.GUID)
		b.table("CompLocator").Add(signature, guid, b.componentLocatorType(path+".component", guid, search.Component.KeyPath))
	}
}

// componentLocatorType returns the CompLocator type for a component search.
// The type must match the component's key path: type 1 on a folder key path
// returns the folder's parent, and type 0 on a file key path returns
// nothing (SEARCH-05, SEARCH-08). A component of this package decides it;
// keyPath does for another product's, file by default.
func (b *build) componentLocatorType(path, guid, keyPath string) int {
	kind := keyPath
	if compID, own := b.guids[guid]; own {
		kind = b.ownKeyPathKind(path, compID, keyPath)
	}

	if kind == config.SearchDirectory {
		return locatorDirectory
	}

	return locatorFile
}

// ownKeyPathKind returns what the key path of a component of this package
// is, file or directory, and reports a keyPath that contradicts it. A
// registry key path searches as a file, with a warning.
func (b *build) ownKeyPathKind(path, compID, keyPath string) string {
	info := b.components[compID]

	// A shortcuts-only component is keyed on its HKCU value (shortcuts.go),
	// which info does not record.
	if info.attributes&compRegistryKeyPath != 0 || shortcutsOnly(info.cfg) {
		if keyPath != "" {
			b.errorf(path+".keyPath", "component %s of this package has a registry key path, not %s", compID, keyPath)

			return config.SearchFile
		}

		b.warnf(path+".guid", "component %s of this package has a registry key path, which is no folder (an HKLM one found nothing, SEARCH-08); "+
			"search a component keyed on a file or on its folder", compID)

		return config.SearchFile
	}

	kind := config.SearchFile
	if info.keyPath == nil {
		kind = config.SearchDirectory
	}

	if keyPath != "" && keyPath != kind {
		b.errorf(path+".keyPath", "component %s of this package has a %s key path, not %s", compID, kind, keyPath)
	}

	return kind
}

// locateParent writes the folder search a DrLocator row is relative to and
// returns its signature, or nil; path is the YAML path of the search that
// has under.
func (b *build) locateParent(path, signature string, under *config.Search) any {
	if under == nil {
		return nil
	}

	parent := searchName("In_", signature)
	b.locate(path+".under", parent, under, true)

	return parent
}

// fileSignature writes the Signature row of a file search.
func (b *build) fileSignature(signature string, file *config.FileSearch) {
	var minDate, maxDate any

	if file.MinDate != "" {
		minDate, _ = config.SearchDate(file.MinDate, false)
	}

	if file.MaxDate != "" {
		maxDate, _ = config.SearchDate(file.MaxDate, true)
	}

	langs := make([]string, 0, len(file.Languages))
	for _, lang := range file.Languages {
		langs = append(langs, strconv.Itoa(lang))
	}

	minVersion := file.MinVersion
	if minVersion != "" {
		minVersion = config.SearchMinVersion(minVersion, file.Languages)
	}

	// Signature.FileName is a Filename column: a name that is no valid
	// short name is written as a generated short|long pair (ICE03).
	name := filename(signature, file.Name, false, make(map[string]bool), config.ValidShortName)

	b.table("Signature").Add(signature, name, nullable(minVersion), nullable(file.MaxVersion),
		optionalSize(file.MinSize), optionalSize(file.MaxSize), minDate, maxDate, nullable(strings.Join(langs, ",")))
}

// optionalDepth is a DrLocator.Depth cell: null for 0, which the engine
// reads as 0 (SEARCH-05).
func optionalDepth(depth int) any {
	if depth == 0 {
		return nil
	}

	return depth
}

// optionalSize is a nullable DoubleInteger cell.
func optionalSize(size *int64) any {
	if size == nil {
		return nil
	}

	return int(*size)
}

// searchName joins a prefix and a property name into an identifier of at
// most 72 characters.
func searchName(prefix, name string) string {
	if len(prefix)+len(name) <= config.MaxIdentifierLength {
		return prefix + name
	}

	return generatedID(strings.TrimSuffix(prefix, "_"), name)
}

// escapeFormatted makes text a literal in a Formatted column: [ ] { } become
// [\x], the escape the formatted-text grammar defines (section 8.9).
func escapeFormatted(text string) string {
	var out strings.Builder

	for _, char := range text {
		switch char {
		case '[', ']', '{', '}':
			out.WriteString(`[\`)
			out.WriteRune(char)
			out.WriteByte(']')
		default:
			out.WriteRune(char)
		}
	}

	return out.String()
}

// remember32Bit reports whether the component installDir.remember adds is a
// 32-bit one, which writes to the 32-bit registry view.
func remember32Bit(cfg *config.Config) bool {
	for _, comp := range cfg.Components {
		if comp.ID == config.RememberComponentID {
			return comp.Bitness == "32"
		}
	}

	return false
}
