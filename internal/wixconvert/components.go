package wixconvert

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// fileRef is a converted file, for shortcuts and firewall rules that point
// at it.
type fileRef struct {
	e    *elem
	comp *comp
	out  *omap
	key  string
	dir  string
	name string
}

// pendingShortcut is a Shortcut outside a File element, placed once every
// file is known.
type pendingShortcut struct {
	e    *elem
	comp *comp
	tree map[*dir]bool
}

// Hints for unsupported elements.
const (
	hintTwice = "install the file twice, in two components"
	hintCOM   = "write the COM registration with registry entries"
	hintRun   = "a run action, if you need it"
)

// keyDirectory is the directory key of components, shortcuts and features.
const keyDirectory = "directory"

// unsupported are elements that msi-builder cannot express, with what to
// do instead (docs/Migrating from WiX.md, "Not supported").
var unsupported = map[string]string{ //nolint:gochecknoglobals // lookup table
	"IniFile":                  "ship the complete file",
	"CopyFile":                 hintTwice,
	"DuplicateFile":            hintTwice,
	"MoveFile":                 hintTwice,
	"Class":                    hintCOM,
	"TypeLib":                  hintCOM,
	"AppId":                    hintCOM,
	"ODBCDriver":               "write the ODBC registration with registry entries",
	"ODBCDataSource":           "write the ODBC registration with registry entries",
	"IsolateComponent":         "leave it out",
	"Merge":                    "merge modules are not supported; install their files in components",
	"MergeRef":                 "merge modules are not supported; install their files in components",
	"EmbeddedChainer":          "not supported",
	"util:XmlFile":             "ship the complete file, or edit it with a run action (no rollback)",
	"util:XmlConfig":           "ship the complete file, or edit it with a run action (no rollback)",
	"util:EventManifest":       "install the manifest and register it with wevtutil in run actions",
	"util:User":                hintRun,
	"util:Group":               hintRun,
	"util:FileShare":           hintRun,
	"util:PerformanceCategory": hintRun,
	"util:TouchFile":           "leave it out",
}

func (c *converter) emitComponents(tree map[*dir]bool) {
	for _, cp := range c.comps {
		cp.out = c.component(cp, tree)
	}

	for _, pending := range c.pending {
		c.placeShortcut(pending)
	}

	c.placeTypes()

	for _, comp := range c.comps {
		if comp.dropIfEmpty && !hasContent(comp.out) {
			// Its notes move to the file header.
			for _, line := range comp.out.commentLines() {
				if strings.HasPrefix(line, "TODO") {
					c.header = append(c.header, line)
				}
			}

			comp.out = nil

			continue
		}

		c.out.add("components", comp.out)
	}
}

// hasContent reports whether a component installs anything.
func hasContent(component *omap) bool {
	for _, key := range component.keys {
		switch key {
		case "id", keyDirectory, "guid", "condition", "bitness", "keyPath":
		default:
			if _, ok := component.values[key]; ok {
				return true
			}
		}
	}

	return false
}

// componentFlags are the yes/no attributes of a Component with a key of
// the same meaning.
var componentFlags = [][2]string{ //nolint:gochecknoglobals // lookup table
	{"Permanent", "permanent"}, {"NeverOverwrite", "neverOverwrite"},
	{"SharedDllRefCount", "sharedDllRefCount"}, {"Transitive", "transitive"},
}

func (c *converter) component(comp *comp, tree map[*dir]bool) *omap {
	elm := comp.e
	out := newMap()
	comp.out = out
	out.order("id", keyDirectory, "guid", "files", "registry", "removeRegistry", "removeFiles", "removeFolders", "services",
		"serviceControl", "eventLog", "environment", "purge", "createFolder", "condition", "permanent",
		"neverOverwrite", "sharedDllRefCount", "transitive", "keyPath", "bitness", "folderPermissions", "shortcuts")
	out.head = comments(elm)
	out.set("id", comp.id)
	out.set(keyDirectory, c.dirValue(comp.dir, tree))

	switch guid := c.value(elm, "Guid"); {
	case guid == "*", !elm.has("Guid"):
		comp.generated = true
	case guid == "":
		out.set("guid", "none")
	default:
		out.set("guid", guid)
	}

	c.componentCondition(out, elm.line, c.value(elm, "Condition"))

	for _, flag := range componentFlags {
		if elm.yes(flag[0]) {
			out.set(flag[1], true)
		}
	}

	c.bitness(out, elm)

	content := &compConv{c: c, cp: comp, out: out, tree: tree, keyIsDir: elm.yes("KeyPath")}
	for _, kid := range elm.kids {
		content.child(kid)
	}

	// A folder source takes no guid; pinHarvested gives its files theirs.
	if comp.generated && comp.folder == nil && c.built != nil {
		c.pinCode(comp, builtKeyFile(elm, content.keyFile))
	}

	content.finish()

	return out
}

// builtKeyFile returns the key file a component has in the built package:
// none when its key path is no file.
func builtKeyFile(elm *elem, keyFile string) string {
	if key := keyElem(elm); key != nil && !key.is("File") && !key.is("Files") {
		return ""
	}

	return keyFile
}

// Bitness values.
const (
	platformX64 = "x64"
	platformX86 = "x86"
	always32    = "always32"
	always64    = "always64"
)

// Names that several elements share.
const (
	attrDirectory  = "Directory"
	attrProperty   = "Property"
	elemPermission = "Permission"
	elemCondition  = "Condition"
	rightsFull     = "full"
)

// WiX standard wizards and their variables.
const (
	uiMondo            = "WixUI_Mondo"
	uiAdvanced         = "WixUI_Advanced"
	uiFeatureTree      = "WixUI_FeatureTree"
	varWixUILicenseRtf = "WixUILicenseRtf"
	varWixUIBannerBmp  = "WixUIBannerBmp"
	varWixUIDialogBmp  = "WixUIDialogBmp"
)

func (c *converter) bitness(out *omap, elm *elem) {
	is64 := c.platform == platformX64 || c.platform == "arm64" || c.platform == ""

	switch {
	case elm.attr("Bitness") == always32, elm.no("Win64"):
		if is64 {
			out.set("bitness", "32")
		}
	case elm.attr("Bitness") == always64, elm.yes("Win64"):
		if !is64 {
			out.set("bitness", "64")
		}
	}
}

// compConv converts the content of one component.
type compConv struct {
	c          *converter
	cp         *comp
	out        *omap
	tree       map[*dir]bool
	controlled map[*omap]bool
	// firstKey is a key path RegistryValue that came before any other
	// resource, and firstKeyID its ID.
	firstKey     *omap
	keyPath      string // the key path a File or RegistryValue asks for
	keyFile      string // the name of the key path file WiX picks
	firstKeyID   string
	hkcuKeyPaths []*omap
	removeFolder []*omap
	services     []*omap
	serviceLines []int
	controls     []*elem
	shortcuts    int
	files        int
	keyIsDir     bool
	keyIsFile    bool // keyPath is a File
}

// componentChildren convert the elements a Component holds.
var componentChildren = map[string]func(cc *compConv, e *elem){ //nolint:gochecknoglobals // dispatch table
	"File":                  (*compConv).file,
	"Files":                 (*compConv).folderFiles,
	"RegistryKey":           func(cc *compConv, e *elem) { cc.registry(e, e.attr("Root"), "") },
	"RegistryValue":         func(cc *compConv, e *elem) { cc.registry(e, e.attr("Root"), "") },
	"RemoveRegistryKey":     (*compConv).removeRegistry,
	"RemoveRegistryValue":   (*compConv).removeRegistry,
	"Environment":           (*compConv).environment,
	"ServiceInstall":        (*compConv).service,
	"ServiceControl":        func(cc *compConv, e *elem) { cc.controls = append(cc.controls, e) },
	elemCloseApp:            func(cc *compConv, e *elem) { cc.c.closeApps = append(cc.c.closeApps, e) },
	"CreateFolder":          (*compConv).createFolder,
	"RemoveFile":            (*compConv).removeFile,
	"RemoveFolder":          (*compConv).removeFile,
	"util:RemoveFolderEx":   (*compConv).purge,
	elemShortcut:            (*compConv).shortcut,
	"util:InternetShortcut": (*compConv).shortcut,
	"util:EventSource":      (*compConv).eventSource,
	"fw:FirewallException":  func(cc *compConv, e *elem) { cc.c.firewall(e, cc.cp, "") },
	"ProgId":                (*compConv).progID,
	// WiX v3: the condition is the text.
	elemCondition: func(cc *compConv, e *elem) {
		cc.c.componentCondition(cc.out, e.line, cc.c.expand(e, strings.TrimSpace(e.text)))
	},
}

const elemShortcut = "Shortcut"

// actionState matches a feature or component action state, &Feature or
// $Component, in a condition.
var actionState = regexp.MustCompile(`[&$][A-Za-z_][A-Za-z0-9_.]*`)

// quotedText matches a string literal in a condition.
var quotedText = regexp.MustCompile(`"[^"]*"`)

// scopeConditions are the conditions that install a component of a dual
// package in one scope only: ALLUSERS is 1 per machine and unset per user.
var scopeConditions = map[string]string{ //nolint:gochecknoglobals // lookup table
	propAllUsers:     scopeMachine,
	"ALLUSERS=1":     scopeMachine,
	`ALLUSERS="1"`:   scopeMachine,
	"NOT ALLUSERS":   scopeUser,
	"NOT ALLUSERS=1": scopeUser,
	`ALLUSERS=""`:    scopeUser,
}

// dualScope reports whether the package lets the user pick the scope.
func (c *converter) dualScope() bool {
	return c.scope == scopeUserOrMachine || c.scope == scopeMachineOrUser
}

// scopeCondition returns the components[].scope a condition stands for in
// a dual package, or "".
func (c *converter) scopeCondition(cond string) string {
	if !c.dualScope() {
		return ""
	}

	normalized := strings.Join(strings.Fields(strings.ReplaceAll(cond, "=", " = ")), " ")
	normalized = strings.ReplaceAll(normalized, " = ", "=")

	return scopeConditions[normalized]
}

// componentCondition sets a component's condition. The installer evaluates
// it before features and components have action states, so msi-builder
// rejects one that tests them and convert leaves it out.
func (c *converter) componentCondition(out *omap, line int, cond string) {
	if scope := c.scopeCondition(cond); scope != "" {
		// The condition the builder writes for components[].scope.
		out.set("scope", scope)

		return
	}

	if state := actionState.FindString(quotedText.ReplaceAllString(cond, `""`)); state != "" {
		c.todof(out, "", line, "the condition %q was left out: it tests the action state %s, which is not set yet when "+
			"the installer evaluates component conditions; test a property instead", cond, state)

		return
	}

	out.set("condition", cond)
}

func (cc *compConv) child(elm *elem) {
	if hint, ok := unsupported[elm.qname()]; ok {
		cc.c.todof(cc.out, "", elm.line, "<%s> is not supported: %s", elm.qname(), hint)

		return
	}

	if fn, ok := componentChildren[elm.qname()]; ok {
		fn(cc, elm)

		return
	}

	cc.c.todof(cc.out, "", elm.line, "<%s> in a component has no counterpart", elm.qname())
}

func (cc *compConv) shortcut(e *elem) {
	cc.shortcuts++
	cc.c.pending = append(cc.c.pending, pendingShortcut{e: e, comp: cc.cp, tree: cc.tree})
}

func (cc *compConv) finish() { //nolint:cyclop // one pass in a fixed order; splitting would scatter it
	cc.resolveControls()

	switch {
	case cc.keyIsDir && (cc.files > 0 || cc.out.get("registry") != nil):
		cc.out.set("keyPath", keyDirectory)
	case cc.keyPath != "":
		cc.out.set("keyPath", cc.keyPath)
	case cc.firstKey != nil && cc.files > 0:
		// A File after it would be msi-builder's default key path.
		cc.firstKey.set("id", cc.firstKeyID)
		cc.out.set("keyPath", cc.firstKeyID)
	}

	if cc.shortcuts > 0 {
		cc.dropShortcutRows()

		return
	}

	profile := cc.inProfile()

	for _, folder := range cc.removeFolder {
		if profile && folder.empty() {
			cc.out.head = append(cc.out.head,
				"The RemoveFolder of this component was left out: msi-builder writes one for every folder in the user profile.")

			continue
		}

		cc.out.add("removeFiles", folder)
	}

	for _, value := range cc.hkcuKeyPaths {
		// Not the first resource: name it as the key path.
		if (cc.out.get("registry") != nil || cc.files > 0) && cc.keyPath == "" {
			cc.out.set("keyPath", value.get("id"))
		} else {
			value.del("id")
		}

		cc.out.add("registry", value)
	}
}

// dropShortcutRows leaves out the RemoveFolder rows and the HKCU key path
// of a WiX shortcut component, which msi-builder writes for every shortcut
// itself.
func (cc *compConv) dropShortcutRows() {
	if len(cc.hkcuKeyPaths) > 0 || len(cc.removeFolder) > 0 {
		cc.out.head = append(cc.out.head,
			"The RemoveFolder and HKCU key path of this shortcut component were left out:",
			"msi-builder writes both for every shortcut.")
	}

	cc.cp.dropIfEmpty = true

	for _, value := range cc.hkcuKeyPaths {
		cc.cp.droppedValues = append(cc.cp.droppedValues, value.str("key")+`\`+value.str("name"))
	}

	if cc.keyPath != "" && !cc.keyIsFile && cc.out.get("registry") == nil {
		cc.out.del("keyPath")
	}
}

// fileFlags are the yes/no attributes of a File with a key of the same
// meaning.
var fileFlags = [][2]string{{"Checksum", "checksum"}, {"ReadOnly", "readOnly"}, {"Hidden", "hidden"}, {"System", "system"}} //nolint:gochecknoglobals,lll // lookup table

func (cc *compConv) file(elm *elem) { //nolint:cyclop // a flat list of independent WiX attributes
	conv := cc.c
	file := newMap()
	file.order("source", "name", "id", "version", "vital", "checksum", "readOnly", "hidden", "system",
		"permissions", "shortcuts", "fileTypes", "close")
	file.head = comments(elm)

	src, name := cc.fileSource(file, elm)
	file.set("id", elm.attr("Id"))
	file.set("version", conv.value(elm, "DefaultVersion"))
	file.set("version", conv.value(elm, "CompanionFile"))

	if elm.no("Vital") {
		file.set("vital", false)
	}

	for _, flag := range fileFlags {
		if elm.yes(flag[0]) {
			file.set(flag[1], true)
		}
	}

	if elm.has("Assembly") {
		conv.todof(file, "", elm.line, "assemblies (Assembly=%q) have no counterpart", elm.attr("Assembly"))
	}

	key := config.FileKey(&config.File{Source: src, Name: name, ID: elm.attr("Id")})
	ref := &fileRef{e: elm, key: key, dir: cc.cp.dir, name: name, comp: cc.cp, out: file}
	conv.files = append(conv.files, ref)

	if elm.yes("KeyPath") && cc.files > 0 {
		cc.keyPath, cc.keyIsFile = name, true
	}

	if cc.files == 0 || elm.yes("KeyPath") {
		cc.keyFile = name
	}

	cc.files++

	for _, kid := range elm.kids {
		switch {
		case kid.qname() == elemShortcut:
			conv.fileShortcut(file, kid, ref, cc.tree)
		case permissionElems[kid.qname()]:
			conv.permission(file, "permissions", kid, rightsFile)
		case kid.qname() == "fw:FirewallException":
			conv.firewall(kid, cc.cp, "[#"+key+"]")
		default:
			conv.todof(file, "", kid.line, "<%s> in a File has no counterpart", kid.qname())
		}
	}

	cc.out.add("files", file)
}

// dirSource returns the source of a File without Source: the FileSource
// that applies to its component joined with its name. Without one, WiX
// finds it through its bind paths.
func (cc *compConv) dirSource(file *omap, elm *elem, name string) string {
	conv := cc.c

	if src := cc.cp.files; src.path != "" && name != "" {
		if src.joined {
			conv.todof(file, "", elm.line, "the File has no Source; convert joined the FileSource of an outer folder "+
				"with the folder names below it, check the path")
		}

		return src.path + "/" + name
	}

	conv.todof(file, "", elm.line, "the File has no Source; WiX finds %q through its bind paths, set the path", name)

	return name
}

// fileSource sets the file's source and name and returns both.
func (cc *compConv) fileSource(file *omap, elm *elem) (string, string) {
	conv := cc.c

	src := elm.attr("Source")
	if src == "" {
		src = elm.attr("src")
	}

	name := conv.value(elm, "Name")
	if src == "" {
		src = cc.dirSource(file, elm, name)
	}

	if strings.Contains(src, "!(bindpath.") {
		conv.todof(file, "", elm.line, "bind paths have no counterpart; write the path to the file")
	}

	src = sourcePath(conv.expand(elm, src))
	file.set("source", src)

	if name == "" {
		return src, baseName(src)
	}

	if name != baseName(src) {
		file.set("name", name)
	}

	return src, name
}

// permissionElems are the elements that set permissions.
var permissionElems = map[string]bool{"util:PermissionEx": true, "PermissionEx": true, elemPermission: true} //nolint:gochecknoglobals // lookup table

func (cc *compConv) registry(elm *elem, root, parentKey string) {
	conv := cc.c

	if r := elm.attr("Root"); r != "" {
		root = r
	}

	key := conv.value(elm, "Key")
	if parentKey != "" {
		key = joinKey(parentKey, key)
	}

	if elm.is("RegistryValue") {
		cc.registryValue(elm, root, key)

		return
	}

	cc.forcedKey(elm, root, key)

	for _, kid := range elm.kids {
		switch {
		case kid.is("RegistryKey"), kid.is("RegistryValue"):
			cc.registry(kid, root, key)
		case permissionElems[kid.qname()]:
			conv.todof(cc.out, "", kid.line, "permissions on registry keys: add permissions to a registry entry of this key by hand")
		default:
			conv.todof(cc.out, "", kid.line, "<%s> in a RegistryKey has no counterpart", kid.qname())
		}
	}
}

// forcedKey writes the key entry of a RegistryKey that is created or
// deleted as a whole.
func (cc *compConv) forcedKey(elm *elem, root, key string) {
	action := elm.attr("Action") // WiX v3
	forceCreate := elm.yes("ForceCreateOnInstall") || action == "create" || action == "createAndRemoveOnUninstall"
	forceDelete := elm.yes("ForceDeleteOnUninstall") || action == "createAndRemoveOnUninstall"

	if !forceCreate && !forceDelete {
		return
	}

	if forceDelete && !forceCreate && !hasRegistryValue(elm) {
		// WiX writes only the "-" row: the key is deleted on uninstall
		// but never created (REG-08). A key entry would create it.
		cc.addRemoveRegistry(newMap().set("root", root).set("key", key).set("on", "uninstall"))

		return
	}

	entry := newMap().set("root", root).set("key", key).set("type", "key")
	if forceDelete {
		entry.set("removeOnUninstall", true)
	}

	cc.out.add("registry", entry)
}

// hasRegistryValue reports whether a RegistryKey writes a value, also in
// a nested key, which creates the key on install.
func hasRegistryValue(elm *elem) bool {
	return slices.ContainsFunc(elm.kids, func(kid *elem) bool {
		return kid.is("RegistryValue") || kid.is("RegistryKey") && hasRegistryValue(kid)
	})
}

const typeMultiString = "multiString"

// registryTypes maps WiX value types to msi-builder's.
var registryTypes = map[string]string{ //nolint:gochecknoglobals // lookup table
	"":              "",
	"string":        "",
	"expandable":    "expandString",
	typeMultiString: typeMultiString,
	"integer":       "dword",
	"binary":        "binary",
}

func (cc *compConv) registryValue(elm *elem, root, key string) { //nolint:cyclop // a flat list of independent WiX attributes
	conv := cc.c
	entry := newMap()
	entry.order("id", "root", "key", "name", "type", "value", "append", "prepend")
	entry.head = comments(elm)

	typ, ok := registryTypes[elm.attr("Type")]
	if !ok {
		conv.todof(entry, "", elm.line, "registry Type=%q has no counterpart", elm.attr("Type"))
	}

	entry.set("root", root).set("key", key).set("name", conv.value(elm, "Name")).set("type", typ)
	entry.set("value", cc.registryData(elm, typ))

	switch elm.attr("Action") {
	case "append":
		entry.set("append", true)
	case "prepend":
		entry.set("prepend", true)
	}

	entry.set("id", elm.attr("Id"))

	if !elm.yes("KeyPath") {
		cc.out.add("registry", entry)

		return
	}

	if entry.get("id") == nil {
		entry.set("id", cc.keyPathID(elm))
	}

	// An HKCU key path beside no file may be the one WiX needs for a
	// shortcut; finish decides.
	if strings.EqualFold(root, "HKCU") && cc.files == 0 {
		cc.hkcuKeyPaths = append(cc.hkcuKeyPaths, entry)

		return
	}

	if cc.files > 0 || cc.out.get("registry") != nil {
		cc.keyPath = entry.str("id")
	} else {
		// The first resource is the default key path, unless a File
		// follows; finish decides.
		cc.firstKey, cc.firstKeyID = entry, entry.str("id")
		if elm.attr("Id") == "" {
			entry.del("id")
		}
	}

	cc.out.add("registry", entry)
}

// keyPathID names a registry value that is a component's key path.
func (cc *compConv) keyPathID(e *elem) string {
	if cc.cp.id != "" {
		return cc.cp.id + "_KeyPath"
	}

	return "KeyPath_line" + strconv.Itoa(e.line)
}

// registryData is the value of a RegistryValue: its Value, its text (WiX
// v3) or the list of a multi-string value.
func (cc *compConv) registryData(elm *elem, typ string) any {
	conv := cc.c

	if typ != typeMultiString {
		return cc.scalarData(elm, typ)
	}

	var values []string

	if v := conv.value(elm, "Value"); v != "" {
		values = append(values, v)
	}

	for _, kid := range elm.kids {
		if kid.is("MultiStringValue") || kid.is("MultiString") {
			v := kid.attr("Value")
			if v == "" {
				v = strings.TrimSpace(kid.text)
			}

			values = append(values, conv.expand(kid, v))
		}
	}

	return values
}

// scalarData is the value of a RegistryValue that is not a multi-string.
func (cc *compConv) scalarData(elm *elem, typ string) any {
	conv := cc.c

	if !elm.has("Value") {
		return conv.expand(elm, strings.TrimSpace(elm.text))
	}

	if v := conv.value(elm, "Value"); v != "" || typ != "" && typ != "expandString" {
		return v
	}

	// An empty string value, which a registry entry must state.
	return emptyText{}
}

func joinKey(parent, key string) string {
	if key == "" {
		return parent
	}

	return strings.TrimRight(parent, `\`) + `\` + key
}

func (cc *compConv) removeRegistry(elm *elem) {
	conv := cc.c
	entry := newMap().set("root", elm.attr("Root")).set("key", conv.value(elm, "Key"))

	if elm.is("RemoveRegistryValue") {
		name := conv.value(elm, "Name")
		if name == "" {
			// removeRegistry without a name would delete the whole key.
			conv.todof(cc.out, "", elm.line, "RemoveRegistryValue %s\\%s without Name removes the default value and was left out: "+
				"removeRegistry without name removes the whole key", elm.attr("Root"), conv.value(elm, "Key"))

			return
		}

		entry.set("name", name)
		cc.addRemoveRegistry(entry)

		return
	}

	if elm.attr("Action") == "removeOnUninstall" {
		entry.set("on", "uninstall")
	}

	cc.addRemoveRegistry(entry)
}

// addRemoveRegistry adds a removeRegistry entry unless the component has
// the same one already, such as a value-less ForceDeleteOnUninstall key and
// a RemoveRegistryKey of that key, which validate refuses twice.
func (cc *compConv) addRemoveRegistry(entry *omap) {
	existing, ok := cc.out.get("removeRegistry").([]any)
	if !ok {
		existing = nil
	}

	for _, item := range existing {
		if other, ok := item.(*omap); ok && sameRemoval(other, entry) {
			return
		}
	}

	cc.out.add("removeRegistry", entry)
}

// sameRemoval reports whether two removeRegistry entries remove the same.
func sameRemoval(one, other *omap) bool {
	for _, key := range []string{"root", "key", "name", "on"} {
		if !strings.EqualFold(one.str(key), other.str(key)) {
			return false
		}
	}

	return true
}

func (cc *compConv) environment(elm *elem) {
	conv := cc.c
	env := newMap()
	env.order("name", "value", "action", "scope", "removeOnUninstall")
	env.head = comments(elm)
	env.set("name", conv.value(elm, "Name")).set("value", conv.value(elm, "Value"))

	switch action, part := elm.attr("Action"), elm.attr("Part"); {
	case action == "create":
		env.set("action", "setIfAbsent")
	case action == "remove":
		env.set("action", "remove")
	case part == "last":
		env.set("action", "append")
	case part == "first":
		env.set("action", "prepend")
	default:
		env.set("action", "set")
	}

	if elm.yes("System") {
		env.set("scope", "machine")
	} else {
		env.set("scope", "user")
	}

	if elm.yes("Permanent") {
		env.set("removeOnUninstall", false)
	}

	if sep := elm.attr("Separator"); sep != "" && sep != ";" {
		conv.todof(env, "", elm.line, "Separator=%q has no counterpart; msi-builder joins with ;", sep)
	}

	cc.out.add("environment", env)
}

// inProfile reports whether the component's folder is in the user profile.
func (cc *compConv) inProfile() bool {
	folder := cc.c.dirs[cc.cp.dir]
	if folder == nil {
		return config.IsProfileFolder(cc.cp.dir)
	}

	top := cc.c.top(folder)

	return top != nil && config.IsProfileFolder(top.id)
}

// folderFiles converts a WiX 4 Files element, which harvests a glob, to a
// folder source with include and exclude patterns, or a plain file source
// when the Include names one file.
func (cc *compConv) folderFiles(elm *elem) {
	conv := cc.c
	full := sourcePath(conv.expand(elm, elm.attr("Include")))
	src, include := splitGlob(full)

	files := newMap()
	files.order("source", "include", "exclude")
	files.head = comments(elm)

	if include == "" {
		src = full
	} else if include != "**" {
		files.set("include", []string{include})
	}

	files.set("source", src)

	var exclude []string

	for _, kid := range elm.kids {
		pattern := sourcePath(conv.expand(kid, kid.attr("Files")))

		rel, below := strings.CutPrefix(pattern, src)
		if src == "./" {
			rel, below = pattern, !strings.HasPrefix(pattern, "../") && !strings.HasPrefix(pattern, "/")
		}

		switch {
		case !kid.is("Exclude"):
			conv.todof(files, "", kid.line, "<%s> in <Files> has no counterpart", kid.qname())
		case include == "" || !below:
			conv.todof(files, "", kid.line, "the Exclude %s is not below the folder %s; leave the file out by hand", pattern, src)
		default:
			exclude = append(exclude, rel)
		}
	}

	files.set("exclude", exclude)
	cc.out.add("files", files)
	cc.countFiles(files, src, include != "")
}

// countFiles counts a Files element: a glob harvests into a folder source,
// and one file is the key file when it comes first.
func (cc *compConv) countFiles(files *omap, src string, glob bool) {
	switch {
	case glob:
		cc.cp.folder = files
	case cc.files == 0:
		cc.keyFile = path.Base(src)
	}

	cc.files++
}

// splitGlob splits a glob path into the folder before its first wildcard,
// with a trailing slash, and the pattern below that folder.
func splitGlob(pattern string) (string, string) {
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			folder := strings.Join(parts[:i], "/")
			if folder == "" {
				folder = "."
			}

			return folder + "/", strings.Join(parts[i:], "/")
		}
	}

	return pattern, ""
}
