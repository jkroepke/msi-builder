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
func (c *converter) chooseInstallDir() { //nolint:cyclop // the fallbacks in their order of preference
	var candidates []string

	if c.wixUI != nil {
		candidates = append(candidates, c.wixUI.attr("InstallDirectory"))
	}

	for _, p := range c.properties {
		if p.attr("Id") == "WIXUI_INSTALLDIR" {
			candidates = append(candidates, p.attr("Value"))
		}
	}

	c.eachFeature(func(f *feature) { candidates = append(candidates, f.e.attr("ConfigurableDirectory")) })

	candidates = append(candidates, installDirNames...)

	for _, id := range candidates {
		if d := c.dirs[id]; d != nil && !d.std && d.name != "" && c.top(d) != nil {
			c.installDir = d

			break
		}
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
		"Shortcut":        {"WorkingDirectory"},
		"CustomAction":    {attrDirectory},
		"SetDirectory":    {"Id"},
		"RemoveFile":      {attrDirectory},
		"RemoveFolder":    {attrDirectory},
		"Property":        {"Id"},
		"DirectorySearch": {"Id"},
		"RemoveFolderEx":  {attrProperty},
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
		case k.is("File"):
			return comp.dir
		case k.is("CreateFolder") && k.attr(attrDirectory) != "":
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
			rootID := "TARGETDIR"
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

		if folder == c.installDir {
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

// defaultInstallParent reports whether msi-builder picks the folder as the
// install dir's parent by itself.
// folderProgramFiles is the 32-bit Program Files folder.
const folderProgramFiles = "ProgramFilesFolder"

func (c *converter) defaultInstallParent(id string) bool {
	switch id {
	case "ProgramFiles6432Folder":
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

	return joinPath("TARGETDIR", names)
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

func isFile(e *elem) bool { return e.is("File") || e.is("Files") }
