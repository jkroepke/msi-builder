package wixconvert

import (
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// installDirNames are directory IDs that name the install folder by
// convention.
var installDirNames = []string{"INSTALLFOLDER", "INSTALLDIR", "APPLICATIONFOLDER", "INSTALLLOCATION", "TARGETFOLDER"} //nolint:gochecknoglobals // lookup table

// chooseInstallDir picks the directory that becomes installDir: the one
// the wizard or a feature makes configurable, else one with a
// conventional name, else the first folder below Program Files.
func (c *converter) chooseInstallDir() {
	var chosen []string

	for _, setter := range c.wizardDirs() {
		chosen = append(chosen, setter.id)
	}

	c.eachFeature(func(f *feature) { chosen = append(chosen, f.e.attr("ConfigurableDirectory")) })

	c.installDir = c.firstInstallDir(chosen, true)
	if c.installDir == nil {
		c.installDir = c.firstInstallDir(installDirNames, false)
	}

	if c.installDir == nil {
		c.installDir = c.programFilesDir()
	}

	if c.installDir == nil {
		return
	}

	if id := c.installDir.id; !config.IsPublicProperty(id) {
		upper := strings.ToUpper(id)
		if _, taken := c.dirs[upper]; !taken && config.IsIdentifier(upper) {
			c.renames[id] = upper
		}
	}
}

// firstInstallDir returns the first of ids that can be installDir. A
// directory named "." qualifies only when the wizard or a feature makes it
// configurable: it then stays in the tree as a configurable alias of its
// parent, so that moving it moves only its own components, as in the
// source. An alias that only has a conventional name is its parent's
// second id, not the install folder.
func (c *converter) firstInstallDir(ids []string, configurable bool) *dir {
	for _, id := range ids {
		folder := c.dirs[id]
		if folder == nil || folder.std || folder.name == "" || c.top(folder) == nil {
			continue
		}

		if folder.name != config.AliasName || configurable {
			return folder
		}
	}

	return nil
}

// programFilesDir returns the folder a package installs its program into
// by convention: below Program Files, or below the local application data
// of a per-user package.
func (c *converter) programFilesDir() *dir {
	if d := c.firstDirBelow("ProgramFiles"); d != nil {
		return d
	}

	return c.firstDirBelow("LocalAppDataFolder")
}

// firstDirBelow returns the first folder below a standard folder whose ID
// starts with prefix, going down through folders that hold only one
// folder and no files: a component without files, such as one that
// remembers a data folder, does not make its folder the program's.
func (c *converter) firstDirBelow(prefix string) *dir {
	for _, folder := range c.dirOrder {
		if folder.std || folder.parent == nil || !strings.HasPrefix(folder.parent.id, prefix) {
			continue
		}

		for {
			kids := c.childDirs(folder)
			if len(kids) != 1 || c.hasFiles(folder) || kids[0].sub {
				return folder
			}

			folder = kids[0]
		}
	}

	return nil
}

func (c *converter) childDirs(d *dir) []*dir {
	var kids []*dir

	for _, x := range c.dirOrder {
		if x.parent == d {
			kids = append(kids, x)
		}
	}

	return kids
}

func (c *converter) hasFiles(d *dir) bool {
	for _, cp := range c.comps {
		if cp.dir != d.id {
			continue
		}

		if isFile(cp.e) || slices.ContainsFunc(cp.e.kids, isFile) {
			return true
		}
	}

	return false
}

// top returns the standard folder a directory is below.
func (c *converter) top(d *dir) *dir {
	for x := d; x != nil; x = x.parent {
		if x.std {
			return x
		}
	}

	return nil
}

// choosePlatform derives package.platform from the folders and component
// bitness the file uses; WiX takes it from the -arch switch.
func (c *converter) choosePlatform() { //nolint:cyclop // the fallbacks in their order of preference
	if c.summary != nil && c.v3 {
		switch p := strings.ToLower(c.summary.attr("Platform")); p {
		case "x64", "arm64", platformX86:
			c.platform = p

			return
		case "intel":
			c.platform = platformX86

			return
		}
	}

	has64 := false
	has32 := false

	for _, d := range c.dirOrder {
		switch {
		case strings.Contains(d.id, "64Folder"):
			has64 = true
		case d.id == folderProgramFiles, d.id == "CommonFilesFolder":
			has32 = true
		}
	}

	for _, cp := range c.comps {
		if cp.e.attr("Bitness") == "always64" || cp.e.yes("Win64") {
			has64 = true
		}
	}

	switch {
	case has64:
		c.platform = "x64"
	case has32:
		c.platform = platformX86
	}
}

// stdID maps a standard folder to the name msi-builder knows: the WiX
// 6432 folders are the Program Files folders of the package's platform.
func (c *converter) stdID(id string) string {
	base, ok := strings.CutSuffix(id, "6432Folder")
	if !ok {
		return id
	}

	if c.platform == platformX86 {
		return base + "Folder"
	}

	return base + "64Folder"
}

// markNeededDirs finds the directories whose IDs the output must keep:
// those that formatted text or an ID-valued attribute refers to.
func (c *converter) markNeededDirs() {
	idAttrs := map[string][]string{
		elemShortcut:      {"WorkingDirectory"},
		"CustomAction":    {attrDirectory},
		elemSetDirectory:  {"Id"},
		elemRemoveFile:    {attrDirectory, attrProperty},
		elemRemoveFolder:  {attrDirectory, attrProperty},
		"Property":        {"Id"},
		"DirectorySearch": {"Id"},
		"RemoveFolderEx":  {attrProperty},
		elemFeature:       {"ConfigurableDirectory"},
	}

	own := make(map[*elem]string)

	for _, cp := range c.comps {
		cp.dir = ownFolder(cp)

		for _, k := range cp.e.kids {
			own[k] = cp.dir
		}
	}

	var walk func(*elem)

	walk = func(elm *elem) {
		for _, v := range elm.attrs {
			c.noteDirRefs(v)
		}

		c.noteDirRefs(elm.text)

		for _, a := range idAttrs[elm.name] {
			// A RemoveFolderEx on its own folder may become a
			// removeFolders entry that names it.
			if v := elm.attr(a); v != "" && (v != own[elm] || elm.name == "RemoveFolderEx") {
				c.needDirs[v] = true
			}
		}

		for _, k := range elm.kids {
			walk(k)
		}
	}

	walk(c.doc.root)
}

// ownFolder is the folder a component goes into: its directory, or the
// folder its CreateFolder names when it has no files.
func ownFolder(comp *comp) string {
	folder := ""

	for _, k := range comp.e.kids {
		switch {
		case k.is(elemFile):
			return comp.dir
		case k.is(elemCreateFolder) && k.attr(attrDirectory) != "":
			folder = k.attr(attrDirectory)
		}
	}

	if folder != "" {
		return folder
	}

	return comp.dir
}

// treeDirs returns the directories that go into the directories key: the
// needed ones that are not the install dir, with their parents.
func (c *converter) treeDirs() map[*dir]bool { //nolint:cyclop // one pass in a fixed order; splitting would scatter it
	tree := make(map[*dir]bool)

	for id := range c.needDirs {
		d := c.dirs[id]
		if d == nil || d.std || d == c.installDir {
			continue
		}

		for x := d; x != nil && !x.std; x = x.parent {
			tree[x] = true
		}
	}

	// An alias install dir has no path of its own, so it stays in the tree
	// below its parent.
	if inst := c.installDir; inst != nil && inst.name == config.AliasName {
		for x := inst; x != nil && !x.std; x = x.parent {
			tree[x] = true
		}
	}

	// A folder above the install dir in the tree takes the install dir
	// with it, so that the path is not created twice.
	if c.installDir != nil {
		for x := c.installDir.parent; x != nil && !x.std; x = x.parent {
			if tree[x] {
				for y := c.installDir; y != nil && !y.std; y = y.parent {
					tree[y] = true
				}

				break
			}
		}
	}

	return tree
}

// emitDirs writes installDir and directories.
func (c *converter) emitDirs(tree map[*dir]bool) { //nolint:cyclop,gocognit // one pass in a fixed order; splitting would scatter it
	if inst := c.installDir; inst != nil {
		out := c.out.child("installDir")
		out.set("id", c.dirRef(inst.id))

		if to, ok := c.renames[inst.id]; ok {
			out.comment("id", "WiX ID "+inst.id+"; the install folder must be an upper-case public property,",
				"so every ["+inst.id+"] reference became ["+to+"].")
		}

		if !tree[inst] {
			var names []string

			cur := inst
			for ; cur != nil && !cur.std; cur = cur.parent {
				names = append(dirNames(cur.name), names...)
			}

			out.set("name", strings.Join(names, "/"))

			if cur != nil && !c.defaultInstallParent(cur.id) {
				out.set("parent", c.stdID(cur.id))
			}
		}
	}

	// A folder a feature lets the user move is configurable.
	configurable := make(map[string]bool)

	c.eachFeature(func(f *feature) { configurable[f.e.attr("ConfigurableDirectory")] = true })

	var roots []*omap

	rootByID := make(map[string]*omap)
	nodes := make(map[*dir]*omap)

	var place func(d *dir) *omap

	place = func(folder *dir) *omap {
		if n, ok := nodes[folder]; ok {
			return n
		}

		entry := newMap()
		nodes[folder] = entry

		if folder.parent == nil || folder.parent.std {
			rootID := folderTargetDir
			if folder.parent != nil {
				rootID = c.stdID(folder.parent.id)
			}

			root, ok := rootByID[rootID]
			if !ok {
				root = newMap().set("id", rootID)
				rootByID[rootID] = root
				roots = append(roots, root)
			}

			root.add("children", entry)
		} else {
			place(folder.parent).add("children", entry)
		}

		entry.set("id", c.dirRef(folder.id))
		entry.set("name", folder.name)

		if folder.name == "" && !folder.std {
			c.todof(entry, "", folder.line, "the Directory %s has no Name, so it is its parent folder unless an action "+
				"sets it; msi-builder has no such folder, give it a name or use a standard folder", folder.id)
		}

		if folder == c.installDir || configurable[folder.id] {
			entry.set("configurable", true)
		}

		return entry
	}

	for _, d := range c.dirOrder {
		if tree[d] {
			place(d)
		}
	}

	for _, r := range roots {
		c.out.add("directories", r)
	}
}

// foldAliasDirs handles the Directories without a name of their own,
// which name their parent folder (DefaultDir "."). Below a named folder
// such a directory stays a second id for it, written as name ".". Directly
// in a standard folder it is that folder, and directly in TARGETDIR it is
// the standard folder a SetDirectory sets it to, typically [WindowsVolume]
// for a folder such as C:\opt; the SetDirectory is then left out. A
// directory that another action sets keeps its own row.
func (c *converter) foldAliasDirs() {
	order := c.dirOrder
	fold := &aliasFold{setters: c.dirSetters(), done: make(map[*dir]bool), added: make(map[string]*dir), limit: len(order)}

	for _, folder := range order {
		c.wixDirs[folder.id] = folder
	}

	for _, folder := range order {
		c.settleAlias(fold, folder, 0)
	}

	kept := make([]*dir, 0, len(order))

	for _, folder := range order {
		target := c.folded[folder.id]
		if target == "" {
			kept = append(kept, folder)
		}

		if std := fold.added[target]; std != nil {
			kept = append(kept, std)

			delete(fold.added, target)
		}
	}

	c.dirOrder = kept
}

// aliasFold is the state of foldAliasDirs.
type aliasFold struct {
	setters map[string][]*elem
	done    map[*dir]bool
	// added are the standard folders that folding introduced.
	added map[string]*dir
	limit int
}

// settleAlias settles a folder after its parent, whatever the order of the
// source, so that a child sees whether its parent became a standard folder.
func (c *converter) settleAlias(fold *aliasFold, folder *dir, depth int) {
	if fold.done[folder] || depth > fold.limit {
		return
	}

	fold.done[folder] = true

	if parent := c.wixDirs[folder.parentID]; parent != nil {
		c.settleAlias(fold, parent, depth+1)
	}

	target := c.aliasTarget(folder, fold.setters[folder.id])
	if target == "" {
		c.keepMovedRoot(folder, fold.setters[folder.id])

		return
	}

	std := c.dirs[target]
	if std == nil {
		std = &dir{id: target, std: true, line: folder.line}
		c.dirs[target] = std
		fold.added[target] = std
	}

	c.dirs[folder.id] = std
	c.renames[folder.id] = target
	c.folded[folder.id] = target
}

// platformFolded maps the folders that nameless Directories became, such
// as ProgramFiles6432Folder, to the folder of the platform, which is known
// only now.
func (c *converter) platformFolded() {
	for id, target := range c.folded {
		c.folded[id] = c.stdID(target)
		c.renames[id] = c.stdID(target)
	}
}

// dirSetters returns, by ID, the elements that set a directory or name it
// in the UI.
func (c *converter) dirSetters() map[string][]*elem {
	setters := make(map[string][]*elem)

	for _, setter := range c.setters {
		setters[setter.attr("Id")] = append(setters[setter.attr("Id")], setter)
	}

	// Anything else that sets the directory or names it in the UI keeps
	// its row: a type-35 or type-51 action, a Property element (also one
	// a search fills), a dialog control or a SetTargetPath event.
	for _, custom := range c.actionOrder {
		if custom.has("Value") {
			for _, id := range []string{custom.attr(attrDirectory), custom.attr(attrProperty)} {
				if id != "" {
					setters[id] = append(setters[id], custom)
				}
			}
		}
	}

	for _, prop := range c.properties {
		setters[prop.attr("Id")] = append(setters[prop.attr("Id")], prop)
	}

	// The wizard's install folder is one too.
	for _, setter := range c.wizardDirs() {
		setters[setter.id] = append(setters[setter.id], setter.elm)
	}

	c.uiDirRefs(c.doc.root, setters)

	return setters
}

// keepMovedRoot keeps the row of a nameless folder in TARGETDIR or a
// standard folder that something moves, which then gets a todo note.
func (c *converter) keepMovedRoot(folder *dir, setters []*elem) {
	if parent := c.dirs[folder.parentID]; !folder.std && unnamed(folder.name) && (parent == nil || parent.std) && len(setters) > 0 {
		c.needDirs[folder.id] = true
	}
}

// anchored reports whether an element below elm is sequenced Before or
// After the action name.
func (c *converter) anchored(elm *elem, name string) bool {
	if elm == nil {
		return false
	}

	if elm.attr("Before") == name || elm.attr("After") == name {
		return true
	}

	return slices.ContainsFunc(elm.kids, func(kid *elem) bool { return c.anchored(kid, name) })
}

// uiDirRefs adds the dialog controls bound to a property and the
// SetTargetPath events of the UI to setters, by the ID they change.
func (c *converter) uiDirRefs(elm *elem, setters map[string][]*elem) {
	if elm == nil {
		return
	}

	if id := uiDirRef(elm); id != "" {
		setters[id] = append(setters[id], elm)
	}

	for _, kid := range elm.kids {
		c.uiDirRefs(kid, setters)
	}
}

// uiDirRef returns the directory a dialog control, a SetTargetPath event, a
// property event or a feature's ConfigurableDirectory lets the user set.
func uiDirRef(elm *elem) string {
	switch {
	case elm.is("Control"), elm.is("Publish") && elm.attr("Event") != "SetTargetPath":
		return elm.attr(attrProperty)
	case elm.is("Publish"):
		return strings.TrimSuffix(strings.TrimPrefix(elm.attr("Value"), "["), "]")
	case elm.is(elemFeature):
		return elm.attr("ConfigurableDirectory")
	}

	return ""
}

// dirSetter is a directory ID and the element that names it.
type dirSetter struct {
	elm *elem
	id  string
}

// wizardDirs returns the folders the WixUI wizard installs into: its
// InstallDirectory and the WIXUI_INSTALLDIR property.
func (c *converter) wizardDirs() []dirSetter {
	var dirs []dirSetter

	if c.wixUI != nil && c.wixUI.has("InstallDirectory") {
		dirs = append(dirs, dirSetter{id: c.wixUI.attr("InstallDirectory"), elm: c.wixUI})
	}

	for _, prop := range c.properties {
		if prop.attr("Id") == propWixUIInstallDir && prop.has("Value") {
			dirs = append(dirs, dirSetter{id: prop.attr("Value"), elm: prop})
		}
	}

	return dirs
}

// aliasTarget returns the standard folder that a directory without a name
// stands for, or "" when it keeps a row of its own; it names one below a
// named folder ".".
func (c *converter) aliasTarget(folder *dir, setters []*elem) string {
	if folder.std || folder.sub || !unnamed(folder.name) {
		return ""
	}

	parent := c.dirs[folder.parentID]

	switch {
	case parent != nil && !parent.std:
		folder.name = config.AliasName

		return ""
	case len(setters) == 0 && parent != nil && parent.id != folderTargetDir:
		return parent.id
	case len(setters) == 1:
		return c.setterFolder(setters[0])
	}

	return ""
}

// unnamed reports whether a WiX directory name names the parent folder.
func unnamed(name string) bool {
	return name == "" || name == config.AliasName
}

// setterFolder returns the standard folder an unconditioned SetDirectory
// sets its directory to, such as [WindowsVolume], and leaves the action
// out; "" for any other value.
func (c *converter) setterFolder(setter *elem) string {
	// A setter that runs in one sequence only, or that another action is
	// sequenced against, keeps its row and its action.
	if !setter.is(elemSetDirectory) || c.condition(setter) != "" || setter.attr("Sequence") != "" && setter.attr("Sequence") != "both" ||
		c.anchored(c.doc.root, setterName(setter)) {
		return ""
	}

	value := strings.TrimSpace(c.literal(setter.attr("Value")))
	inner, ok := strings.CutPrefix(value, "[")
	inner, closed := strings.CutSuffix(inner, "]")

	if !ok || !closed || !config.IsStandardFolder(inner) || inner == folderTargetDir {
		return ""
	}

	c.consumed[setter] = true

	return inner
}

// defaultInstallParent reports whether msi-builder picks the folder as the
// install dir's parent by itself.
// folderProgramFiles is the 32-bit Program Files folder.
const folderProgramFiles = "ProgramFilesFolder"

func (c *converter) defaultInstallParent(id string) bool {
	switch id {
	case folderProgramFiles6432:
		return true
	case "ProgramFiles64Folder":
		return c.platform != platformX86
	case folderProgramFiles:
		return c.platform == platformX86
	}

	return false
}

// dirNames splits a WiX directory name, which v4 lets hold several levels.
func dirNames(name string) []string {
	var names []string

	for _, n := range strings.FieldsFunc(name, func(r rune) bool { return r == '\\' || r == '/' }) {
		if n != "." {
			names = append(names, n)
		}
	}

	return names
}

// dirValue returns a component's directory key: empty for the install dir,
// a path below it, a standard folder with a path, or a directory ID.
func (c *converter) dirValue(id string, tree map[*dir]bool) string {
	folder := c.dirs[id]
	if folder == nil {
		return c.stdID(c.dirRef(id))
	}

	var names []string

	for cur := folder; cur != nil; cur = cur.parent {
		switch {
		case cur == c.installDir:
			return c.relative(names)
		case cur.std:
			return joinPath(c.stdID(cur.id), names)
		case tree[cur]:
			return joinPath(c.dirRef(cur.id), names)
		}

		names = append(dirNames(cur.name), names...)
	}

	return joinPath(folderTargetDir, names)
}

// relative returns a path below the install dir, with "./" when its first
// folder would read as a directory ID.
func (c *converter) relative(names []string) string {
	if len(names) == 0 {
		return ""
	}

	p := strings.Join(names, "/")
	if c.looksLikeID(names[0]) {
		p = "./" + p
	}

	return p
}

func (c *converter) looksLikeID(word string) bool {
	if !config.IsIdentifier(word) {
		return false
	}

	if config.IsPublicProperty(word) {
		return true
	}

	for id := range c.dirs {
		if strings.EqualFold(id, word) {
			return true
		}
	}

	return false
}

func joinPath(start string, names []string) string {
	return strings.Join(append([]string{start}, names...), "/")
}

func isFile(e *elem) bool { return e.is(elemFile) || e.is("Files") }
