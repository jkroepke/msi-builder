package wixconvert

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// emitVars writes the defines as vars. A define that uses an earlier one
// takes in its value, as the WiX preprocessor does: msi-builder does not
// expand a variable inside a vars value.
func (c *converter) emitVars() {
	c.resolveDefines()

	for _, define := range c.defines {
		c.out.child("vars").set(define[0], c.expand(c.pkg, c.defined[define[0]]))
	}
}

// resolveDefines fills the values of earlier defines into each define.
func (c *converter) resolveDefines() {
	c.defined = make(map[string]string)

	for _, define := range c.defines {
		c.defined[define[0]] = c.literal(define[1])
	}
}

// literal fills the values of the defines into text.
func (c *converter) literal(text string) string {
	if c.defined == nil {
		c.resolveDefines()
	}

	return preprocessorVar.ReplaceAllStringFunc(text, func(ref string) string {
		parts := preprocessorVar.FindStringSubmatch(ref)
		if v, ok := c.defined[varName(parts[2])]; ok && parts[1] != "env" && parts[1] != "sys" {
			return v
		}

		return ref
	})
}

// emit writes the output in msi-builder's key order.
func (c *converter) emit() {
	c.out.set("schema", "msi-builder/v1")
	c.out.order("vars", "product", "package", "upgrade", "installDir", "directories", "features", "components",
		"properties", "launchConditions", "customActions", "icons", "binaries", "firewall", "scheduleReboot", "ui")

	c.emitVars()

	c.markNeededDirs()
	tree := c.treeDirs()

	c.emitProduct()
	c.emitPackage()
	c.emitUpgrade()
	c.emitDirs(tree)
	c.emitComponents(tree)
	c.emitCloseApps()
	c.emitFeatures()
	c.emitProperties()
	c.emitLaunchConditions()
	c.emitActions()
	c.emitFiles()
	c.emitUI()
	c.emitErrorRows()
	c.emitActionRefs()
	c.checkCodes()
}

func (c *converter) emitProduct() {
	product := c.out.child("product")
	product.set("name", c.value(c.pkg, "Name"))
	product.set("manufacturer", c.value(c.pkg, "Manufacturer"))
	product.set("version", c.value(c.pkg, "Version"))

	if lang := c.value(c.pkg, "Language"); lang != "" && lang != "1033" && lang != "0" {
		if n, err := strconv.Atoi(lang); err == nil {
			product.set("language", n)
		} else {
			c.todof(product, "", c.pkg.line, "Language=%q is not a number; set product.language", lang)
		}
	}

	product.set("upgradeCode", c.value(c.pkg, "UpgradeCode"))

	code := c.value(c.pkg, "ProductCode")
	if c.v3 {
		code = c.value(c.pkg, "Id")
	}

	if code != "" && code != "*" {
		product.set("productCode", code)
		product.comment("productCode", "A pinned product code; msi-builder derives one per build when this key is left out.")
	}

	if s := c.summary; s != nil {
		product.set("description", c.value(s, "Description"))
		product.set("comments", c.value(s, "Comments"))
	}

	c.emitARP(product)
}

// arpKeys maps ARP properties to product.arp keys.
var arpKeys = map[string]string{ //nolint:gochecknoglobals // lookup table
	"ARPCOMMENTS":        "comments",
	"ARPCONTACT":         "contact",
	"ARPHELPLINK":        "helpLink",
	"ARPURLINFOABOUT":    "aboutLink",
	"ARPURLUPDATEINFO":   "updateLink",
	"ARPPRODUCTICON":     "icon",
	"ARPNOREPAIR":        "noRepair",
	"ARPNOMODIFY":        "noModify",
	"ARPNOREMOVE":        "noRemove",
	"ARPSIZE":            "size",
	"ARPINSTALLLOCATION": "installLocation",
}

func (c *converter) emitARP(p *omap) {
	arp := p.child("arp")

	for _, elm := range c.properties {
		key, ok := arpKeys[elm.attr("Id")]
		if !ok {
			continue
		}

		val := c.value(elm, "Value")

		switch key {
		case "noRepair", "noModify", "noRemove":
			if val != "" && val != "0" {
				arp.set(key, true)
			}
		case "size":
			if n, err := strconv.Atoi(val); err == nil {
				arp.set(key, n)
			}
		default:
			arp.set(key, val)
		}
	}
}

func (c *converter) emitPackage() { //nolint:cyclop // a flat list of independent WiX attributes
	pkgOut := c.out.child("package")

	if c.platform == "" {
		pkgOut.set("platform", "x64")
		c.todof(pkgOut, "platform", c.pkg.line, "WiX takes the platform from wix build -arch; check package.platform")
	} else {
		pkgOut.set("platform", c.platform)
	}

	c.emitScope(pkgOut)

	ver := c.value(c.pkg, "InstallerVersion")
	if c.v3 && c.summary != nil {
		ver = c.value(c.summary, "InstallerVersion")
	}

	if version, err := strconv.Atoi(ver); err == nil {
		// A dual scope needs MSIINSTALLPERUSER, which 5.0 brought.
		if c.dualScope() && version < dualInstallerVersion {
			version = dualInstallerVersion
		}

		pkgOut.set("installerVersion", strconv.Itoa(version/100)+"."+strconv.Itoa(version%100))
	}

	cp := c.value(c.pkg, "Codepage")
	if n, err := strconv.Atoi(cp); err == nil {
		pkgOut.set("codepage", n)
	}

	if c.v3 && c.summary != nil && c.summary.no("Compressed") || !c.v3 && c.pkg.no("Compressed") {
		c.todof(pkgOut, "", c.pkg.line, "Compressed=\"no\": msi-builder always compresses the files into cabinets")
	}

	c.emitMedia(pkgOut)

	if s := c.summary; s != nil {
		sum := pkgOut.child("summary")
		sum.set("keywords", c.value(s, "Keywords"))

		if !c.v3 {
			sum.set("author", c.value(s, "Manufacturer"))
		}
	}
}

func (c *converter) emitScope(pkgOut *omap) {
	scope := c.value(c.pkg, "Scope")
	if c.v3 && c.summary != nil {
		scope = c.value(c.summary, "InstallScope")
	}

	scope, allUsers := c.allUsers(pkgOut, scope)
	if c.v3 && scope == "" && !allUsers {
		c.perUserNote(pkgOut)
	}

	c.scope = scope
	if scope != "" && scope != scopeMachine {
		pkgOut.set("scope", scope)
	}
}

// allUsers applies an ALLUSERS property to the scope and reports whether
// the file sets one.
func (c *converter) allUsers(pkgOut *omap, scope string) (string, bool) {
	found := false

	for _, elm := range c.properties {
		if elm.attr("Id") != propAllUsers {
			continue
		}

		found = true

		switch c.value(elm, "Value") {
		case "2":
			// MSIINSTALLPERUSER=1 makes the per-user install the default;
			// a dual scope writes both properties itself.
			scope = scopeMachineOrUser
			if c.propertyValue(propInstallPerUser) == "1" {
				scope = scopeUserOrMachine
			}

			c.todof(pkgOut, "scope", elm.line, "ALLUSERS=2 became scope %s, which lets the user choose the scope and "+
				"sets ALLUSERS and MSIINSTALLPERUSER itself; it needs installerVersion 5.0", scope)
		case "":
			scope = scopeUser
		}
	}

	return scope, found
}

// propertyValue returns the Value of the last Property with an Id.
func (c *converter) propertyValue(id string) string {
	value := ""

	for _, elm := range c.properties {
		if elm.attr("Id") == id {
			value = c.value(elm, "Value")
		}
	}

	return value
}

// perUserNote notes a WiX v3 package that sets no scope at all.
func (c *converter) perUserNote(pkgOut *omap) {
	line := c.pkg.line
	if c.summary != nil {
		line = c.summary.line
	}

	c.todof(pkgOut, "scope", line,
		"the WiX v3 Package has no InstallScope and the file sets no ALLUSERS, which Windows Installer installs per user; "+
			"msi-builder installs per machine by default. Keep that when the package writes to Program Files or other "+
			"machine-wide places, which a per-user package cannot; an upgrade may then not find a version installed per user. "+
			"Set scope: perUser only for a package that installs into the user profile")
}

func (c *converter) emitMedia(pkgOut *omap) {
	if len(c.media) > 1 {
		c.todof(pkgOut, "", c.media[1].line, "several Media elements: msi-builder writes one cabinet, or several beside the package (embedCabinets: false)")
	}

	for _, medium := range c.media {
		if medium.no("EmbedCab") {
			pkgOut.set("embedCabinets", false)
			pkgOut.set("cabinet", c.value(medium, "Cabinet"))
		}

		if medium.attr("CompressionLevel") == valueNone {
			pkgOut.set("compression", "none")
		}

		if medium.has("DiskPrompt") || medium.has("VolumeLabel") {
			c.todof(pkgOut, "", medium.line, "DiskPrompt and VolumeLabel are left out: they only matter for packages on several disks")
		}
	}
}

func (c *converter) emitUpgrade() {
	upgrade := c.out.child("upgrade")
	upgradeCode := strings.Trim(strings.ToLower(c.pkg.attr("UpgradeCode")), "{}")

	if c.major != nil {
		c.emitMajor(upgrade, c.major)
	}

	for _, upgradeElem := range c.upgrades {
		c.emitRelated(upgrade, upgradeElem, strings.Trim(strings.ToLower(upgradeElem.attr("Id")), "{}") == upgradeCode)
	}

	c.emitProductSearches(upgrade)

	if c.major == nil && len(c.upgrades) == 0 {
		c.todof(nil, "", c.pkg.line,
			"the WiX package has no MajorUpgrade; msi-builder adds the standard major upgrade (set upgrade: false to leave it out)")
	}

	text := c.allText()
	// A hand-made row that became upgrade.minVersion keeps its own
	// property: without a MajorUpgrade, WiX does not set
	// WIX_UPGRADE_DETECTED for it.
	if strings.Contains(text, "WIX_UPGRADE_DETECTED") && !c.floorSeen {
		upgrade.set("property", "WIX_UPGRADE_DETECTED")
	}

	if strings.Contains(text, "WIX_DOWNGRADE_DETECTED") {
		c.newerProperty(upgrade, "WIX_DOWNGRADE_DETECTED", 0)
	}
}

// productSearch reports whether elm is a ProductSearch by UpgradeCode,
// which WiX writes as a detect-only Upgrade row.
func productSearch(elm *elem) bool {
	return elm.is("ProductSearch") && elm.has("UpgradeCode") && !elm.has("Guid")
}

// productSearchOnly reports whether a Property only holds ProductSearch
// elements by UpgradeCode: it becomes their upgrade.related rows. One with
// a Value or another search stays a property, which no upgrade row may set
// (ICE61), so its ProductSearch stays a TODO.
func productSearchOnly(prop *elem) bool {
	return !prop.has("Value") && len(prop.kids) > 0 &&
		!slices.ContainsFunc(prop.kids, func(kid *elem) bool { return !productSearch(kid) })
}

// emitProductSearches turns the ProductSearch elements by UpgradeCode into
// upgrade.related onlyDetect rows that set their property.
func (c *converter) emitProductSearches(upgrade *omap) {
	for _, prop := range c.properties {
		if !productSearchOnly(prop) {
			continue
		}

		for _, kid := range prop.kids {
			row := newMap()
			row.set("upgradeCode", c.value(kid, "UpgradeCode"))
			row.set("minVersion", c.value(kid, "Minimum"))
			row.set("maxVersion", c.value(kid, "Maximum"))

			if !kid.has("Minimum") && !kid.has("Maximum") {
				// An Upgrade row needs a version; this one finds every version.
				row.set("minVersion", "0.0.0")
			}

			if kid.no("IncludeMinimum") {
				row.set("includeMin", false)
			}

			if kid.yes("IncludeMaximum") {
				row.set("includeMax", true)
			}

			row.set("onlyDetect", true)
			row.set("property", prop.attr("Id"))

			if kid.has("Language") {
				c.todof(row, "", kid.line, "ProductSearch Language has no counterpart in upgrade.related")
			}

			upgrade.add("related", row)
		}
	}
}

// newerProperty sets the property that the detection of newer versions sets,
// with a TODO when the file tests two of them.
func (c *converter) newerProperty(upgrade *omap, prop string, line int) {
	switch current := c.newerClaimed; {
	case current == "":
		c.newerClaimed = prop
		if prop != config.DefaultNewerProperty {
			upgrade.set("newerProperty", prop)
		}
	case prop != current:
		key := "newerProperty"
		if upgrade.str(key) == "" {
			key = ""
		}

		c.todof(upgrade, key, line, "the file tests both %s and %s for a newer version; msi-builder sets only %s, "+
			"use it in both places", current, prop, current)
	}
}

// emitMajor converts a MajorUpgrade element.
func (c *converter) emitMajor(upgrade *omap, major *elem) {
	c.majorSchedule(upgrade, major)

	if major.yes("AllowDowngrades") {
		upgrade.set("downgrade", "allow")
	}

	// WiX leaves the same version out of the upgrade unless asked;
	// msi-builder's default takes it in. With AllowDowngrades WiX's row
	// has no upper bound, so the same version is upgraded too.
	if !major.yes("AllowSameVersionUpgrades") && !major.yes("AllowDowngrades") {
		upgrade.set("allowSameVersion", false)
	}

	if major.yes("Disallow") {
		c.todof(upgrade, "", major.line, "MajorUpgrade Disallow=\"yes\": msi-builder always upgrades; set upgrade: false to turn upgrades off")
	}

	upgrade.set("downgradeMessage", c.value(major, "DowngradeErrorMessage"))

	if major.yes("IgnoreRemoveFailure") {
		upgrade.set("ignoreRemoveFailure", true)
	}

	if major.no("MigrateFeatures") {
		upgrade.set("migrateFeatures", false)
	}

	if major.has("RemoveFeatures") {
		c.todof(upgrade, "", major.line, "MajorUpgrade RemoveFeatures has no counterpart")
	}
}

// majorSchedule converts the MajorUpgrade Schedule to upgrade.removeOld.
func (c *converter) majorSchedule(upgrade *omap, major *elem) {
	switch schedule := major.attr("Schedule"); schedule {
	case "", "afterInstallValidate":
		// WiX's default; msi-builder's afterInstallInitialize also
		// removes the old version first, inside the transaction.
		if schedule != "" {
			upgrade.set("removeOld", schedule)
		}
	case "afterInstallInitialize", "afterInstallExecute", "afterInstallFinalize":
		upgrade.set("removeOld", schedule)
	default:
		c.todof(upgrade, "", major.line, "MajorUpgrade Schedule=%q has no counterpart", schedule)
	}
}

// majorUpgradeProperty are the properties of the major upgrade msi-builder
// writes, which a related row cannot take.
var majorUpgradeProperty = map[string]bool{ //nolint:gochecknoglobals // lookup table
	config.DefaultUpgradeProperty: true, config.DefaultNewerProperty: true, "WIX_UPGRADE_DETECTED": true, "WIX_DOWNGRADE_DETECTED": true,
}

// valueAny and valueNone are WiX and msi-builder words for no limit and
// for nothing.
const (
	valueAny  = "any"
	valueNone = "none"
)

// The install scopes, as package.scope and components[].scope name them.
const (
	scopeMachine       = "perMachine"
	scopeUser          = "perUser"
	scopeUserOrMachine = "perUserOrMachine"
	scopeMachineOrUser = "perMachineOrUser"
	// propAllUsers is the property that selects the scope.
	propAllUsers = "ALLUSERS"
	// propInstallPerUser picks the per-user install of an ALLUSERS=2
	// package.
	propInstallPerUser = "MSIINSTALLPERUSER"
	// dualInstallerVersion is Windows Installer 5.0, the first with
	// MSIINSTALLPERUSER.
	dualInstallerVersion = 500
	// propPerMachineFolder is WixUI_Advanced's per-machine install folder.
	propPerMachineFolder = "WixPerMachineFolder"
)

func orAny(version string) string {
	if version == "" {
		return valueAny
	}

	return version
}

// ownUpgradeRow handles an UpgradeVersion row of the package's own
// UpgradeCode that does not carry over to upgrade.related and reports
// whether it did.
func (c *converter) ownUpgradeRow(upgrade *omap, kid *elem) bool {
	switch {
	case kid.yes("OnlyDetect") && kid.no("IncludeMinimum") && !kid.has("Maximum") &&
		kid.attr("Minimum") == c.pkg.attr("Version") && kid.has("Property"):
		// The detection of newer versions, which msi-builder writes
		// itself; keep the property conditions test.
		c.newerProperty(upgrade, kid.attr("Property"), kid.line)
	case c.floorRow(upgrade, kid):
		// A hand-made major upgrade with a version floor.
	case majorUpgradeProperty[kid.attr("Property")]:
		c.todof(upgrade, "", kid.line, "the UpgradeVersion property %s is one msi-builder sets for its own major upgrade; "+
			"check the conditions that test it", kid.attr("Property"))
	case !kid.yes("OnlyDetect"):
		c.todof(upgrade, "", kid.line, "an UpgradeVersion for the package's own UpgradeCode (versions %s to %s) is a hand-made major upgrade; "+
			"msi-builder writes its own, which removes every older version (down to upgrade.minVersion) and never a newer one (downgrade: allow installs "+
			"next to a newer version), check the upgrade keys",
			orAny(c.value(kid, "Minimum")), orAny(c.value(kid, "Maximum")))
	default:
		return false
	}

	return true
}

// floorRow converts a hand-made major upgrade of the package's own
// UpgradeCode with a Minimum, from that version up to this one, into
// upgrade.minVersion and reports whether it did. The row takes the place
// of msi-builder's own, so it needs no MajorUpgrade next to it and nothing
// msi-builder's row cannot express (UPG-13). An inclusive minimum of 0.0.0
// is no floor and becomes the standard row.
func (c *converter) floorRow(upgrade *omap, kid *elem) bool {
	if !c.isFloorRow(kid) {
		return false
	}

	c.floorSeen = true

	// WiX's UpgradeVersion includes the minimum and excludes the maximum
	// by default, and sets the MigrateFeatures bit only when asked.
	upgrade.set("allowSameVersion", kid.yes("IncludeMaximum"))

	if !noFloor(kid) {
		upgrade.set("minVersion", c.value(kid, "Minimum"))

		if kid.no("IncludeMinimum") {
			upgrade.set("includeMin", false)
		}

		older := "an older version than %s stays installed next to this one"
		if kid.no("IncludeMinimum") {
			older = "a version up to and including %s stays installed next to this one"
		}

		c.todof(upgrade, "minVersion", kid.line, older+"; a WiX-built one keeps its own component codes unless they are "+
			"pinned (convert --msi or guid), so uninstalling either product may delete files the other uses. To refuse it "+
			"instead, detect it with a related onlyDetect row and a launch condition", c.value(kid, "Minimum"))
	}

	if !c.detectsNewer() {
		c.todof(upgrade, "", kid.line, "the WiX package does not detect newer versions; msi-builder refuses to install "+
			"over one (downgrade: ignore installs next to it)")
	}

	if !kid.yes("MigrateFeatures") {
		upgrade.set("migrateFeatures", false)
	}

	if kid.yes("IgnoreRemoveFailure") {
		upgrade.set("ignoreRemoveFailure", true)
	}

	if prop := kid.attr("Property"); prop != config.DefaultUpgradeProperty {
		upgrade.set("property", prop)
	}

	return true
}

// isFloorRow reports whether an UpgradeVersion of the package's own
// UpgradeCode is the first hand-made major upgrade with a Minimum below
// this version, up to this version, in a package without MajorUpgrade.
func (c *converter) isFloorRow(kid *elem) bool {
	if c.major != nil || c.floorSeen || kid.yes("OnlyDetect") || !kid.has("Minimum") || !kid.has("Property") ||
		newerDetection[kid.attr("Property")] || !c.schedulesRemoval() {
		return false
	}

	version := c.pkg.attr("Version")
	if kid.attr("Maximum") != version || !belowVersion(kid.attr("Minimum"), version) {
		return false
	}

	return !slices.ContainsFunc([]string{"Language", "ExcludeLanguages", "RemoveFeatures"}, kid.has)
}

// schedulesRemoval reports whether the source schedules
// RemoveExistingProducts in InstallExecuteSequence; without it a hand-made
// Upgrade row only detects the old version.
func (c *converter) schedulesRemoval() bool {
	for _, seq := range c.sequences {
		if sequenceNames[seq.name] == seqExecute && slices.ContainsFunc(seq.kids, func(e *elem) bool { return e.is("RemoveExistingProducts") }) {
			return true
		}
	}

	return false
}

// detectsNewer reports whether the source has its own row that detects
// newer versions of the package, which msi-builder writes too.
func (c *converter) detectsNewer() bool {
	upgradeCode := strings.Trim(strings.ToLower(c.pkg.attr("UpgradeCode")), "{}")

	for _, upgradeElem := range c.upgrades {
		if strings.Trim(strings.ToLower(upgradeElem.attr("Id")), "{}") != upgradeCode {
			continue
		}

		for _, kid := range upgradeElem.kids {
			if kid.is("UpgradeVersion") && kid.yes("OnlyDetect") && kid.attr("Minimum") == c.pkg.attr("Version") && !kid.has("Maximum") {
				return true
			}
		}
	}

	return false
}

// noFloor reports whether a Minimum lets every older version through: an
// inclusive 0.0.0.
func noFloor(kid *elem) bool {
	floor, err := config.ParseVersion(kid.attr("Minimum"))

	return err == nil && floor == config.Version{Fields: floor.Fields} && !kid.no("IncludeMinimum")
}

// newerDetection are the properties that detect newer versions, which a
// removing row cannot take.
var newerDetection = map[string]bool{ //nolint:gochecknoglobals // lookup table
	config.DefaultNewerProperty: true, "WIX_DOWNGRADE_DETECTED": true,
}

// belowVersion reports whether minimum is below version in the first three
// fields, as FindRelatedProducts compares them. Values that are not
// versions, such as WiX variables, count as below unless they are equal.
func belowVersion(minimum, version string) bool {
	low, errLow := config.ParseVersion(minimum)
	high, errHigh := config.ParseVersion(version)

	if errLow != nil || errHigh != nil {
		return minimum != version
	}

	return cmp.Or(cmp.Compare(low.Major, high.Major), cmp.Compare(low.Minor, high.Minor), cmp.Compare(low.Build, high.Build)) < 0
}

// emitRelated converts the UpgradeVersion rows of an Upgrade element. For
// the package's own UpgradeCode only the detect-only rows carry over: the
// rows that remove older versions are the major upgrade msi-builder writes
// itself.
func (c *converter) emitRelated(upgrade *omap, upgradeElem *elem, own bool) {
	for _, kid := range upgradeElem.kids {
		switch {
		case !kid.is("UpgradeVersion"):
			c.todof(upgrade, "", kid.line, "<%s> in <Upgrade> has no counterpart", kid.qname())

			continue
		case own && c.ownUpgradeRow(upgrade, kid):
			continue
		}

		row := newMap()
		row.set("upgradeCode", c.value(upgradeElem, "Id"))
		row.set("minVersion", c.value(kid, "Minimum"))
		row.set("maxVersion", c.value(kid, "Maximum"))

		if kid.no("IncludeMinimum") {
			row.set("includeMin", false)
		}

		if kid.yes("IncludeMaximum") {
			row.set("includeMax", true)
		}

		if kid.yes("OnlyDetect") {
			row.set("onlyDetect", true)
		}

		row.set("property", c.value(kid, "Property"))

		for _, a := range []string{"Language", "ExcludeLanguages", "RemoveFeatures", "MigrateFeatures", "IgnoreRemoveFailure"} {
			if kid.has(a) {
				c.todof(row, "", kid.line, "UpgradeVersion %s has no counterpart in upgrade.related", a)
			}
		}

		upgrade.add("related", row)
	}
}

// allText is every attribute value and text of the file, for checks that
// look for a name anywhere.
func (c *converter) allText() string {
	var text strings.Builder

	var walk func(*elem)

	walk = func(elm *elem) {
		for _, v := range elm.attrs {
			text.WriteString(v)
			text.WriteByte('\n')
		}

		text.WriteString(elm.text)

		for _, k := range elm.kids {
			walk(k)
		}
	}

	walk(c.doc.root)

	return text.String()
}

// skippedProperties are properties that other keys express.
func (c *converter) skippedProperty(propID string) bool {
	// A dual scope sets MSIINSTALLPERUSER itself.
	if propID == propInstallPerUser && c.dualScope() {
		return true
	}

	if _, ok := arpKeys[propID]; ok {
		return true
	}

	switch propID {
	case propAllUsers, "WixShellExecTarget", "QtExecCmdLine", "QtExec64CmdLine", "WixQuietExecCmdLine", "WixQuietExec64CmdLine", "WIXUI_INSTALLDIR",
		"WIXUI_EXITDIALOGOPTIONALCHECKBOX", "WIXUI_EXITDIALOGOPTIONALCHECKBOXTEXT", "WIXUI_EXITDIALOGOPTIONALTEXT":
		return true
	}

	return false
}

func (c *converter) emitProperties() {
	props := c.out.child("properties")

	upgradeProps := c.upgradeProperties()

	for _, elm := range c.properties {
		propID := elm.attr("Id")
		if c.skippedProperty(propID) || upgradeOnly(elm, upgradeProps) {
			continue
		}

		propOut := newMap()
		propOut.keep = true
		propOut.set("value", c.value(elm, "Value"))

		if elm.yes("Hidden") {
			propOut.set("hidden", true)
		}

		c.emitSearches(propOut, elm)

		var value any = propOut
		if len(propOut.keys) == 1 && propOut.keys[0] == "value" {
			value = propOut.get("value")
		}

		props.set(propID, value)

		if lines := comments(elm); len(lines) > 0 {
			props.comment(propID, lines...)
		}

		if comment := propOut.head; len(comment) > 0 {
			props.comment(propID, comment...)

			propOut.head = nil
		}
	}
}

// upgradeOnly reports whether a Property only declares one that an
// upgrade row sets: msi-builder declares and secures those itself, and one
// in properties fails ICE61.
func upgradeOnly(elm *elem, upgradeProps map[string]bool) bool {
	return upgradeProps[elm.attr("Id")] && (!elm.has("Value") && len(elm.kids) == 0 || productSearchOnly(elm))
}

// upgradeProperties are the properties that the converted upgrade rows
// set.
func (c *converter) upgradeProperties() map[string]bool {
	props := make(map[string]bool)

	upgrade := c.out.mapAt("upgrade")
	if upgrade == nil {
		return props
	}

	props[cmp.Or(upgrade.str("property"), config.DefaultUpgradeProperty)] = true
	props[cmp.Or(upgrade.str("newerProperty"), config.DefaultNewerProperty)] = true

	related, ok := upgrade.get("related").([]any)
	if !ok {
		related = nil
	}

	for _, row := range related {
		if rowMap, ok := row.(*omap); ok {
			props[rowMap.str("property")] = true
		}
	}

	delete(props, "")

	return props
}

func (c *converter) emitSearches(propOut *omap, prop *elem) {
	var searches []any

	for _, k := range prop.kids {
		if s := c.search(propOut, k); s != nil {
			searches = append(searches, s)
		}
	}

	switch len(searches) {
	case 0:
	case 1:
		propOut.set("search", searches[0])
	default:
		propOut.set("search", searches)
	}
}

// search converts a search element, with a nested search as the inner
// search that is relative to it.
func (c *converter) search(propOut *omap, elm *elem) *omap {
	kind, found := c.searchKind(propOut, elm)
	if found == nil {
		return nil
	}

	outer := newMap().set(kind, found)

	return c.nestedSearch(propOut, elm, outer)
}

// nestedSearch returns the search nested in elm with outer as its under
// search, or outer. A DirectorySearch without Path below another search
// stands for the folder the outer one finds; existenceCheck says what it
// checks.
func (c *converter) nestedSearch(propOut *omap, elm *elem, outer *omap) *omap {
	for _, kid := range elm.kids {
		if kid.is("DirectorySearch") && strings.TrimSpace(kid.attr("Path")) == "" {
			c.existenceCheck(propOut, elm, kid, outer)

			if depth, err := strconv.Atoi(kid.attr("Depth")); err == nil && depth > 0 && hasNestedSearch(kid) {
				c.todof(propOut, "", kid.line, "the DirectorySearch without Path has Depth=\"%d\"; convert left Depth out, so the "+
					"search nested in it looks only in the folder the outer %s finds, not in its subfolders", depth, elm.qname())
			}

			return c.nestedSearch(propOut, kid, outer)
		}

		if inner := c.search(propOut, kid); inner != nil {
			// The nested search looks below the folder this one finds.
			attachUnder(inner, outer)

			return inner
		}
	}

	return outer
}

// existenceCheck converts a DirectorySearch without Path below the search
// elm, whose converted form is outer. Below a registry search of type
// directory, which returns the folder only if it exists (SEARCH-07,
// SEARCH-09), it adds nothing.
func (c *converter) existenceCheck(propOut *omap, elm, kid *elem, outer *omap) {
	switch {
	case elm.is("RegistrySearch") && outer.mapAt("registry") != nil && elm.attr("Type") == keyDirectory:
		// type: directory already returns the folder only if it exists.
	case elm.is("RegistrySearch") && outer.mapAt("registry") != nil && slices.Contains([]string{"", "raw"}, elm.attr("Type")):
		c.rawFolderNote(propOut, kid)
	case elm.is("DirectorySearch"), hasNestedSearch(kid):
		// A search nested in the folder only finds something if the folder
		// exists, so it is the existence check.
	default:
		c.todof(propOut, "", kid.line, "the DirectorySearch without Path only checks that the folder the outer %s finds exists; "+
			"properties.search has no such check, so convert left it out and the property is set even if the folder is missing",
			elm.qname())
	}
}

// rawFolderNote notes a DirectorySearch without Path below a raw
// RegistrySearch. Windows Installer then returns the folder that holds
// the path in the value, whether it exists or not (SEARCH-09).
func (c *converter) rawFolderNote(propOut *omap, kid *elem) {
	if hasNestedSearch(kid) {
		// The builder searches below a registry search as a directory
		// search: in the folder the value names, if it exists.
		c.todof(propOut, "", kid.line, "the DirectorySearch without Path below a raw RegistrySearch stands for the folder "+
			"that holds the path in the value; convert searches below the folder the value names instead, so check that "+
			"the value names the folder to search in")

		return
	}

	c.todof(propOut, "", kid.line, "the DirectorySearch without Path below a raw RegistrySearch returns the folder that "+
		"holds the path in the value, without checking that it exists; convert kept the value itself, so check whether "+
		"it names that folder, or use type: directory to get the folder only if it exists; for a value that is a path, "+
		"the property is set whenever the value exists, so if only that matters, the raw value is enough")
}

// hasNestedSearch reports whether a search that looks below the folder
// is nested in elm. A DirectorySearch without Path stands for the same
// folder, so it only counts by what is nested in it.
func hasNestedSearch(elm *elem) bool {
	return slices.ContainsFunc(elm.kids, func(kid *elem) bool {
		if kid.is("DirectorySearch") && strings.TrimSpace(kid.attr("Path")) == "" {
			return hasNestedSearch(kid)
		}

		return kid.is("DirectorySearch") || kid.is("FileSearch")
	})
}

// searchKind converts the attributes of one search element.
func (c *converter) searchKind(propOut *omap, elm *elem) (string, *omap) {
	found := newMap()

	switch {
	case elm.is("RegistrySearch"):
		c.registrySearch(propOut, elm, found)

		return "registry", found
	case elm.is("DirectorySearch"):
		found.set("path", c.value(elm, "Path"))

		if n, err := strconv.Atoi(elm.attr("Depth")); err == nil && n > 0 {
			found.set("depth", n)
		}

		return "directory", found
	case elm.is("FileSearch"):
		c.fileSearch(elm, found)

		return "file", found
	case elm.is("ComponentSearch"):
		found.set("guid", c.value(elm, "Guid"))

		// The engine returns the parent of a folder key path unless the
		// locator says directory (SEARCH-08). For a component of the
		// package msi-builder derives it from its own key path, which may
		// differ from WiX's (an HKCU one in the profile), so it is left out.
		if strings.EqualFold(elm.attr("Type"), "directory") && !c.ownComponentGUID(c.value(elm, "Guid")) {
			found.set("keyPath", "directory")
		}

		return "component", found
	}

	c.todof(propOut, "", elm.line, "<%s> has no counterpart in properties.search", elm.qname())

	return "", nil
}

// attachUnder sets outer as the under search of the outermost search in
// the chain that inner starts.
func attachUnder(inner, outer *omap) {
	for {
		var found *omap

		for _, key := range inner.keys {
			if m := inner.mapAt(key); m != nil {
				found = m
			}
		}

		if found == nil {
			return
		}

		next := found.mapAt("under")
		if next == nil {
			found.set("under", outer)

			return
		}

		inner = next
	}
}

func (c *converter) registrySearch(propOut *omap, elm *elem, found *omap) {
	root := elm.attr("Root")
	if root == "HKMU" {
		c.todof(propOut, "", elm.line, "a registry search cannot read HKMU; pick HKLM or HKCU")
	}

	found.set("root", root)
	found.set("key", c.value(elm, "Key"))
	found.set("name", c.value(elm, "Name"))

	switch {
	case elm.attr("Bitness") == "always64", elm.yes("Win64"):
		found.set("bitness", "64")
	case elm.attr("Bitness") == "always32", elm.no("Win64"):
		found.set("bitness", "32")
	}

	switch elm.attr("Type") {
	case "directory":
		found.set("type", "directory")
	case "file":
		c.todof(propOut, "", elm.line, "RegistrySearch Type=\"file\" has no counterpart; search the folder and add a file search under it")
	}
}

func (c *converter) fileSearch(elm *elem, found *omap) {
	found.set("name", c.value(elm, "Name"))

	for _, attr := range []string{"MinVersion", "MaxVersion", "MinDate", "MaxDate"} {
		found.set(lowerFirst(attr), c.value(elm, attr))
	}

	for _, attr := range []string{"MinSize", "MaxSize"} {
		if n, err := strconv.Atoi(elm.attr(attr)); err == nil {
			found.set(lowerFirst(attr), n)
		}
	}
}

func (c *converter) emitLaunchConditions() {
	for _, elm := range c.launches {
		raw := elm.attr("Condition")
		if elm.is("Condition") { // v3: the condition is the text
			raw = strings.TrimSpace(elm.text)
		}

		// The fold compares the condition as the package gets it, with
		// the defines filled in.
		if c.downgradeCondition(c.literal(raw), elm) {
			continue
		}

		cond := c.expand(elm, raw)
		entry := newMap().set("condition", cond).set("message", c.value(elm, "Message"))

		// A variable without a define may be the newer property.
		if literal := c.literal(raw); c.out.mapAt("upgrade").str("downgrade") == "" && strings.HasPrefix(literal, "NOT ") &&
			preprocessorVar.FindString(literal) == strings.TrimPrefix(literal, "NOT ") {
			c.todof(entry, "condition", elm.line, "if %s is the property that detects newer versions, drop this entry and "+
				"set upgrade.downgradeMessage to its message: downgrade: error writes this launch condition itself", cond)
		}

		c.out.add("launchConditions", entry)
	}
}

// relatedProperty reports whether an upgrade.related row sets prop: a launch
// condition on it refuses that product, not a newer version of this one.
func (c *converter) relatedProperty(upgrade *omap, prop string) bool {
	related, ok := upgrade.get("related").([]any)
	if !ok {
		return false
	}

	for _, entry := range related {
		if row, ok := entry.(*omap); ok && c.literal(row.str("property")) == prop {
			return true
		}
	}

	return false
}

// downgradeCondition folds a launch condition that refuses newer versions
// into upgrade.downgradeMessage and reports whether it did. downgrade: error
// writes that launch condition itself, and the LaunchCondition table keys on
// the condition, so the package cannot hold both.
func (c *converter) downgradeCondition(cond string, elm *elem) bool {
	upgrade := c.out.mapAt("upgrade")
	if upgrade == nil || upgrade.str("downgrade") != "" {
		return false
	}

	newer := c.literal(cmp.Or(upgrade.str("newerProperty"), config.DefaultNewerProperty))
	if cond != "NOT "+newer || c.relatedProperty(upgrade, newer) {
		return false
	}

	c.foldMessage(upgrade, cond, elm)

	return true
}

// foldMessage keeps the message of a folded launch condition in
// upgrade.downgradeMessage, or notes it when another message is there.
func (c *converter) foldMessage(upgrade *omap, cond string, elm *elem) {
	msg := c.value(elm, "Message")

	switch {
	case c.downgradeFrom == nil && c.major != nil && c.major.has("DowngradeErrorMessage"):
		c.downgradeFrom = c.major
	case c.downgradeFrom == nil:
		c.downgradeFrom = elm

		upgrade.set("downgradeMessage", msg)

		if c.major == nil && len(c.upgrades) == 0 {
			c.todof(upgrade, "downgradeMessage", elm.line, "the launch condition %s became upgrade.downgradeMessage; "+
				"with upgrade: false, add it back to launchConditions", cond)
		}

		return
	}

	from, attr, source := c.downgradeFrom, "Message", "the Launch on line "+strconv.Itoa(c.downgradeFrom.line)
	if from == c.major {
		attr, source = "DowngradeErrorMessage", "the MajorUpgrade's DowngradeErrorMessage"
	}

	// The messages compare like the condition, with the defines filled in.
	if c.literal(from.attr(attr)) != c.literal(elm.attr("Message")) {
		c.todof(upgrade, "downgradeMessage", elm.line, "the launch condition %s is the one downgrade: error writes; "+
			"its message %q is left out for %s", cond, msg, source)
	}
}

// emitFiles writes icons and binaries.
func (c *converter) emitFiles() {
	for _, e := range c.icons {
		c.out.child("icons").set(e.attr("Id"), sourcePath(c.value(e, "SourceFile")))
	}

	for _, elm := range c.binaries {
		src := elm.attr("SourceFile")
		if src == "" {
			src = elm.attr("src")
		}

		c.out.child("binaries").set(elm.attr("Id"), sourcePath(c.expand(elm, src)))
	}
}

// comments returns an element's XML comments as comment lines.
func comments(e *elem) []string {
	var lines []string

	for _, text := range e.comments {
		for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}

	return lines
}

// lowerFirst turns a WiX attribute name into the msi-builder key.
func lowerFirst(s string) string {
	return strings.ToLower(s[:1]) + s[1:]
}

// ownComponentGUID reports whether guid is the Guid of a component in the
// source.
func (c *converter) ownComponentGUID(guid string) bool {
	norm := func(s string) string { return strings.ToUpper(strings.Trim(s, "{}")) }
	want := norm(guid)

	for _, cp := range c.comps {
		if norm(cp.e.attr("Guid")) == want || norm(c.value(cp.e, "Guid")) == want {
			return true
		}
	}

	// With --msi a generated code is pinned from the built package, by the
	// component's Id. A row of a merge module or extension is not the
	// source's; an Id-less row pinned by key path is left out too, which at
	// worst makes the builder reject a contradicting keyPath.
	if c.built != nil {
		for _, row := range c.built.Components {
			if norm(row.Code) == want && c.compByID[row.ID] != nil {
				return true
			}
		}
	}

	return false
}
