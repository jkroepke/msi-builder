package wixconvert

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jkroepke/msi-builder/internal/folderglob"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// BuiltPackage is the package WiX built from the source file.
type BuiltPackage struct {
	// Name names the package in comments, such as app.msi.
	Name        string
	Components  []BuiltComponent
	Directories []BuiltDirectory
}

// BuiltDirectory is a row of the Directory table of the package WiX built
// from the source file.
type BuiltDirectory struct {
	// ID is the Directory key, Parent the Directory_Parent column.
	ID, Parent string
	// Name is the long target name of DefaultDir: "." or empty for a row
	// that stands for its parent.
	Name string
}

// BuiltComponent is a row of the Component table of the package WiX built
// from the source file.
type BuiltComponent struct {
	// KeyRegistry is the key path registry value, or nil.
	KeyRegistry *BuiltRegistry
	// ID is the Component key.
	ID string
	// Code is the ComponentId.
	Code string
	// Directory is the Directory_ column.
	Directory string
	// KeyFile is the long name of the key path file, empty when the key
	// path is a registry value or the folder.
	KeyFile string
}

// BuiltRegistry is a row of the Registry table of a built package.
type BuiltRegistry struct {
	// Key and Name are the Key and Name columns; Name is empty for the
	// default value.
	Key, Name string
	// Root is the Root column: -1 for HKMU, 0 HKCR, 1 HKCU, 2 HKLM, 3 HKU.
	Root int
}

// lateRemoval are the upgrade.removeOld values that install the new version
// before the old one is removed. The removal then deletes the resources of
// every component code the new version does not share (UPG-11).
var lateRemoval = map[string]bool{"afterInstallExecute": true, "afterInstallFinalize": true} //nolint:gochecknoglobals // lookup table

// pinCode gives a component whose Guid WiX generated the code it has in the
// built package, and reports whether the package has a matching component.
func (c *converter) pinCode(comp *comp, keyFile string) bool {
	built := c.builtComponent(comp, keyFile)
	if built == nil {
		return false
	}

	comp.out.set("guid", built.Code)
	comp.pinned = true

	c.claim(built)

	return true
}

// builtComponent finds a component in the built package: by its Id, or,
// when the source gave it none, by the Id of its key path or the name and
// folder of its key file.
func (c *converter) builtComponent(comp *comp, keyFile string) *BuiltComponent {
	if id := comp.e.attr("Id"); id != "" {
		return c.builtByID(id)
	}

	// WiX gives a component without an Id the Id of its key path, which
	// convert takes too.
	if comp.id != "" {
		if row := c.builtByID(comp.id); row != nil && c.compByID[row.ID] == comp && !c.claimed[row.ID] {
			return row
		}
	}

	if row := c.builtByKeyFile(comp, keyFile); row != nil {
		return row
	}

	return c.builtByRegistry(comp)
}

// registryRoots are the Root column values of the WiX registry roots.
var registryRoots = map[string]int{"HKMU": -1, "HKCR": 0, "HKCU": 1, "HKLM": 2, "HKU": 3} //nolint:gochecknoglobals // lookup table

// builtByRegistry returns the one free component of the built package
// whose key path is a registry value of comp: its key path, when the source
// marks one, else any of its values. WiX names a component without an Id
// after a registry key path by a hash, which convert cannot repeat.
func (c *converter) builtByRegistry(comp *comp) *BuiltComponent {
	values := c.registryValues(comp.e, "", "")
	if key := keyElem(comp.e); key != nil {
		if !key.is("RegistryValue") {
			return nil
		}

		values = slices.DeleteFunc(values, func(v builtValue) bool { return v.elm != key })
	}

	var found *BuiltComponent

	for i := range c.built.Components {
		row := &c.built.Components[i]
		if !c.free(row) || row.KeyRegistry == nil || !slices.ContainsFunc(values, row.KeyRegistry.matches) {
			continue
		}

		if found != nil {
			return nil
		}

		found = row
	}

	return found
}

// builtValue is a RegistryValue with its Root, Key and Name as the built
// package has them.
type builtValue struct {
	elm *elem
	reg BuiltRegistry
}

// matches reports whether a registry row is the value v.
func (r *BuiltRegistry) matches(v builtValue) bool {
	return r.Root == v.reg.Root && strings.EqualFold(r.Key, v.reg.Key) && strings.EqualFold(r.Name, v.reg.Name)
}

// registryValues returns the RegistryValues of a component, also those in
// a RegistryKey, with the defines in their Key and Name filled in.
func (c *converter) registryValues(elm *elem, root, parentKey string) []builtValue {
	var values []builtValue

	for _, kid := range elm.kids {
		if !kid.is("RegistryValue") && !kid.is("RegistryKey") {
			continue
		}

		kidRoot := root
		if r := kid.attr("Root"); r != "" {
			kidRoot = r
		}

		key := c.literal(kid.attr("Key"))
		if parentKey != "" {
			key = joinKey(parentKey, key)
		}

		if kid.is("RegistryKey") {
			values = append(values, c.registryValues(kid, kidRoot, key)...)

			continue
		}

		if code, ok := registryRoots[strings.ToUpper(kidRoot)]; ok {
			values = append(values, builtValue{elm: kid, reg: BuiltRegistry{Root: code, Key: key, Name: c.literal(kid.attr("Name"))}})
		}
	}

	return values
}

// builtByKeyFile returns the one free component of the built package with
// the key file in the component's folder, or nil.
func (c *converter) builtByKeyFile(comp *comp, keyFile string) *BuiltComponent {
	if keyFile == "" {
		return nil
	}

	want, anyDir := c.keyFileDir(comp)

	var found *BuiltComponent

	for i := range c.built.Components {
		row := &c.built.Components[i]
		if !c.free(row) || !strings.EqualFold(row.KeyFile, keyFile) || !anyDir && row.Directory != want {
			continue
		}

		if found != nil {
			return nil
		}

		found = row
	}

	return found
}

// keyFileDir returns the folder of the built package that holds the key
// file of comp, or true when it may be any folder: a folder without an Id
// got a generated one in the package, and only its name below its parent
// can find it.
func (c *converter) keyFileDir(comp *comp) (string, bool) {
	folder := c.dirs[comp.dir]
	if folder == nil || !folder.generated {
		return comp.dir, false
	}

	if found := c.builtDir(folder, comp.dir); c.built.Directories != nil && found != "" {
		return found, false
	}

	return comp.dir, true
}

// free reports whether a built component may match by key file: it has a
// code, and neither a source Id names it nor another component took it, so
// that two components never share a code.
func (c *converter) free(row *BuiltComponent) bool {
	_, named := c.compByID[row.ID]

	return row.Code != "" && !named && !c.claimed[row.ID]
}

// builtByID returns the component of the built package with an Id and a
// code, or nil.
func (c *converter) builtByID(id string) *BuiltComponent {
	for i := range c.built.Components {
		if c.built.Components[i].ID == id && c.built.Components[i].Code != "" {
			return &c.built.Components[i]
		}
	}

	return nil
}

// checkCodes notes the components that keep no code of the WiX package:
// with a built package every one it lacks, without one all of them at once
// when the upgrade removes the old version late.
func (c *converter) checkCodes() {
	files := 0
	if c.built != nil {
		files = c.pinHarvested()
	}

	open, pinned := c.openCodes()

	c.noteDroppedValues()
	c.noteAdvertised()
	c.noteHarvested()

	if c.built == nil {
		c.noteLateRemoval(open)

		return
	}

	if pinned > 0 {
		c.header = append(c.header, fmt.Sprintf("The guid of %d components whose Guid WiX generated comes from %s.", pinned, c.built.Name))
	}

	if files > 0 {
		c.header = append(c.header, fmt.Sprintf("The guids of %d files that WiX harvested with Files come from %s.", files, c.built.Name))
	}

	for _, comp := range open {
		c.todof(comp.out, "", comp.e.line,
			"%s has no matching component; set guid to the ComponentId WiX gave this one (msi-builder inspect --table Component)",
			c.built.Name)
	}
}

// noteDroppedValues notes the HKCU key path values that a shortcut
// component keeping its WiX code no longer writes: with a late removal the
// old product's removal keeps them, since the code still has a client, and
// nothing removes them later (seen in a real-world port, like INST-04 for
// HKCU values the new version did not list).
func (c *converter) noteDroppedValues() {
	upgrade := c.out.mapAt("upgrade")
	if upgrade == nil || !lateRemoval[upgrade.str("removeOld")] {
		return
	}

	for _, comp := range c.comps {
		if comp.out == nil || !hasCode(comp.out) || len(comp.droppedValues) == 0 {
			continue
		}

		c.todof(comp.out, "", comp.e.line,
			"msi-builder writes its own HKCU key path for this shortcut component; with the late removeOld the WiX value "+
				"HKCU\\%s stays behind after the upgrade and the uninstall, so remove it with removeRegistry in another component",
			strings.Join(comp.droppedValues, ", HKCU\\"))
	}
}

// noteAdvertised notes the advertised shortcuts of the WiX package under a
// late removal. A real-world port lost all of them with every component
// code pinned: the old product's removal deleted them after this package
// had installed its plain shortcuts at the same paths, presumably because
// an advertised shortcut goes with its feature, not with its component.
func (c *converter) noteAdvertised() {
	upgrade := c.out.mapAt("upgrade")
	if len(c.advertised) == 0 || upgrade == nil || !lateRemoval[upgrade.str("removeOld")] {
		return
	}

	slices.Sort(c.advertised)

	numbers := make([]string, 0, len(c.advertised))
	for _, line := range c.advertised {
		numbers = append(numbers, strconv.Itoa(line))
	}

	lines := "line " + numbers[0]
	if len(numbers) > 1 {
		lines = "lines " + strings.Join(numbers, ", ")
	}

	c.todof(upgrade, "removeOld", 0,
		"the WiX package's advertised shortcuts (%s) are deleted by this late removal after this package installed its "+
			"own at the same paths, also with every guid pinned; remove the old version early (afterInstallInitialize), at "+
			"least for the first upgrade from WiX, or give the shortcuts other names",
		lines)
}

// noteHarvested notes the folder sources that a WiX Files glob became and
// that keep no code of the WiX package under a late removal: WiX gave every
// harvested file a component and code of its own, so the removal of the
// WiX package deletes the files whose code changed (UPG-11, UPG-12).
func (c *converter) noteHarvested() {
	upgrade := c.out.mapAt("upgrade")
	if upgrade == nil || !lateRemoval[upgrade.str("removeOld")] {
		return
	}

	for _, comp := range c.comps {
		switch {
		case comp.out == nil || comp.folder == nil || comp.pinned && len(comp.tied) == 0:
		case len(comp.tied) > 0:
			// A tied file has a component, so the other notes would be
			// wrong.
			slices.Sort(comp.tied)
			c.todof(comp.folder, "", comp.e.line,
				"another Files glob into the same folder may add %s too, and convert did not find the files in exactly one "+
					"of their source folders, so it kept no code for them and the late removeOld deletes them on the first "+
					"upgrade from the WiX package; add the ComponentId WiX gave each one to the guids of the glob whose source "+
					"folder holds it",
				strings.Join(comp.tied, ", "))
		default:
			c.todof(comp.folder, "", comp.e.line, "%s", c.harvestNote(comp))
		}
	}
}

// harvestNote explains why a folder source that a Files glob became keeps
// no code of the WiX package.
func (c *converter) harvestNote(comp *comp) string {
	const early = "or remove the old version early (afterInstallInitialize)"

	switch {
	case c.built == nil:
		return "WiX gave each file of this Files glob a component of its own, so the late removeOld deletes these files on " +
			"the first upgrade from the WiX package; run msi-builder convert again with --msi and the package WiX built, " +
			"which keeps the code of each file in guids, " + early
	case c.builtDir(c.dirs[comp.dir], comp.dir) == "":
		return "convert did not find the folder of this Files glob in " + c.built.Name + ", so it kept no code for its " +
			"files and the late removeOld deletes them on the first upgrade from the WiX package; add the ComponentId WiX " +
			"gave each file to guids (msi-builder inspect --table Component), " + early
	}

	return c.built.Name + " has no component for a file of this Files glob, so the late removeOld deletes these files on " +
		"the first upgrade from the WiX package; add the ComponentId WiX gave each file to guids (msi-builder inspect " +
		"--table Component), " + early
}

// pinHarvested gives each folder source that a WiX Files glob became the
// codes WiX gave the files it harvested, as guids keyed by the path of the
// file below the folder, and returns how many files it pinned. It runs
// after the components took theirs, so a file that a component lists keeps
// its code there. A file the folder source leaves out keeps none.
func (c *converter) pinHarvested() int {
	found := c.harvestedCodes()
	pinned := 0

	for _, comp := range c.comps {
		guids := found[comp]
		if len(guids) == 0 {
			continue
		}

		codes := newMap()

		for _, rel := range slices.Sorted(maps.Keys(guids)) {
			row := guids[rel]
			codes.set(rel, row.Code)
			c.claim(row)
		}

		comp.folder.set("guids", codes)
		comp.pinned = true
		pinned += len(guids)
	}

	return pinned
}

// harvest is a folder source with its folder in the built package.
type harvest struct {
	comp   *comp
	filter *folderglob.Filter
	target string
}

// harvests returns the folder sources whose folder the built package has.
func (c *converter) harvests() []harvest {
	var found []harvest

	for _, comp := range c.comps {
		if comp.out == nil || comp.folder == nil {
			continue
		}

		target := c.builtDir(c.dirs[comp.dir], comp.dir)

		filter, err := folderglob.New(comp.folder.list("include"), comp.folder.list("exclude"))
		if target == "" || err != nil {
			continue
		}

		found = append(found, harvest{comp: comp, filter: filter, target: target})
	}

	return found
}

// harvestedCodes returns, for each folder source, the components of the
// built package whose key file it adds, by the path below its folder. A
// file two folder sources add goes to the one whose folder is nearest
// above it; when both folders are the same, it goes to neither.
func (c *converter) harvestedCodes() map[*comp]map[string]*BuiltComponent {
	sources := c.harvests()
	found := make(map[*comp]map[string]*BuiltComponent)
	seen := make(map[*comp]map[string]bool)
	known := c.listedCodes()

	for i := range c.built.Components {
		row := &c.built.Components[i]
		if !c.free(row) || row.KeyFile == "" || known.has(row) {
			continue
		}

		src, rel, tied := c.nearestHarvest(sources, row)
		for _, other := range tied {
			other.comp.tied = append(other.comp.tied, rel)
		}

		if src == nil || seen[src.comp][strings.ToLower(rel)] {
			// Windows matches names ignoring case, and so does the build.
			continue
		}

		if found[src.comp] == nil {
			found[src.comp] = make(map[string]*BuiltComponent)
			seen[src.comp] = make(map[string]bool)
		}

		seen[src.comp][strings.ToLower(rel)] = true
		found[src.comp][rel] = row
	}

	return found
}

// candidate is a folder source that adds a file, with the file's path
// below its folder.
type candidate struct {
	src *harvest
	rel string
}

// nearestHarvest returns the folder source that adds the key file of row
// and whose folder is nearest above it, with the file's path below that
// folder. Only the sources whose source folder holds the file count when
// one does. When none adds it, it returns nil; when several with the same
// folder do, nil and those sources.
func (c *converter) nearestHarvest(sources []harvest, row *BuiltComponent) (*harvest, string, []*harvest) {
	cands := make([]candidate, 0, len(sources))

	for i := range sources {
		folder, below := c.builtPath(row.Directory, sources[i].target)
		rel := path.Join(folder, row.KeyFile)

		if below && sources[i].filter.Included(rel) {
			cands = append(cands, candidate{src: &sources[i], rel: rel})
		}
	}

	nearest := make([]*harvest, 0, len(cands))
	bestRel, depth := "", -1

	for _, cand := range c.onDisk(cands) {
		switch steps := strings.Count(cand.rel, "/"); {
		case len(nearest) == 0 || steps < depth:
			nearest, bestRel, depth = append(nearest[:0], cand.src), cand.rel, steps
		case steps == depth:
			nearest = append(nearest, cand.src)
		}
	}

	switch len(nearest) {
	case 0:
		return nil, "", nil
	case 1:
		return nearest[0], bestRel, nil
	}

	return nil, bestRel, nearest
}

// onDisk returns the candidates whose source folder, next to the source
// file, holds the file, or all of them when none does or convert cannot
// look.
func (c *converter) onDisk(cands []candidate) []candidate {
	if c.exists == nil {
		return cands
	}

	held := make([]candidate, 0, len(cands))

	for _, cand := range cands {
		folder := strings.TrimSuffix(cand.src.comp.folder.str("source"), "/")
		if !strings.Contains(folder, "{{") && c.exists(path.Join(folder, cand.rel)) {
			held = append(held, cand)
		}
	}

	if len(held) == 0 {
		return cands
	}

	return held
}

// listed are the codes the source's components give themselves: WiX took
// them from the source, so no folder source may claim those built
// components.
type listed map[string]bool

// has reports whether a component of the source has the code of row.
func (l listed) has(row *BuiltComponent) bool {
	guid, err := config.NormalizeGUID(row.Code)

	return err == nil && l[guid]
}

// listedCodes collects the codes of the source's components that are no
// folder source.
func (c *converter) listedCodes() listed {
	known := make(listed)

	for _, comp := range c.comps {
		if comp.out == nil || comp.folder != nil {
			continue
		}

		if guid, err := config.NormalizeGUID(comp.out.str("guid")); err == nil {
			known[guid] = true
		}
	}

	return known
}

// claim marks a built component as taken, so that no other component gets
// its code.
func (c *converter) claim(row *BuiltComponent) {
	if c.claimed == nil {
		c.claimed = make(map[string]bool)
	}

	c.claimed[row.ID] = true
}

// builtDir returns the ID that the folder, with the source ID dirID, has
// in the built package, or "". A folder WiX gave an ID of its own is found
// by its name below its parent.
func (c *converter) builtDir(folder *dir, dirID string) string {
	if folder != nil && folder.generated {
		parent := ""
		if folder.parent != nil {
			parent = c.builtDir(folder.parent, folder.parent.id)
		}

		return c.builtChild(parent, folder.name)
	}

	dirs := c.builtDirs()
	if dirs[dirID] != nil {
		return dirID
	}

	// WiX writes a 6432 folder under the name of the platform it built
	// for, which -arch chose.
	if base, ok := strings.CutSuffix(dirID, "6432Folder"); ok {
		for _, name := range []string{base + "64Folder", base + "Folder"} {
			if dirs[name] != nil {
				return name
			}
		}
	}

	return ""
}

// builtChild returns the ID of the only folder of the built package named
// name below parent, or "".
func (c *converter) builtChild(parent, name string) string {
	if parent == "" || name == "" {
		return ""
	}

	found := ""

	for _, row := range c.built.Directories {
		if row.Parent != parent || !strings.EqualFold(row.Name, name) {
			continue
		}

		if found != "" {
			return ""
		}

		found = row.ID
	}

	return found
}

// builtPath returns the slash path of the folder dirID below the folder
// target in the built package, and whether dirID is target or below it.
func (c *converter) builtPath(dirID, target string) (string, bool) {
	dirs := c.builtDirs()

	var names []string

	// A parent loop would be a broken package; stop after every row.
	for range len(dirs) + 1 {
		if dirID == target {
			slices.Reverse(names)

			return path.Join(names...), true
		}

		row := dirs[dirID]
		if row == nil || row.Parent == dirID {
			return "", false
		}

		if row.Name != "" && row.Name != "." {
			names = append(names, row.Name)
		}

		dirID = row.Parent
	}

	return "", false
}

// builtDirs indexes the folders of the built package by ID.
func (c *converter) builtDirs() map[string]*BuiltDirectory {
	if c.builtDirIndex == nil {
		c.builtDirIndex = make(map[string]*BuiltDirectory, len(c.built.Directories))
		for i := range c.built.Directories {
			c.builtDirIndex[c.built.Directories[i].ID] = &c.built.Directories[i]
		}
	}

	return c.builtDirIndex
}

// openCodes returns the components whose Guid WiX generated and that have
// no code of the WiX package, and counts the ones that have one.
func (c *converter) openCodes() ([]*comp, int) {
	var open []*comp

	pinned := 0

	for _, comp := range c.comps {
		switch {
		case comp.folder != nil:
			// pinHarvested and noteHarvested cover it.
		case comp.out != nil && comp.pinned:
			pinned++
		case comp.generated && comp.out != nil && comp.out.get("guid") == nil:
			// A dropped component installs nothing, so it loses nothing.
			open = append(open, comp)
		}
	}

	return open, pinned
}

// noteLateRemoval notes the components without a WiX Guid when the upgrade
// removes the old version after installing the new one.
func (c *converter) noteLateRemoval(open []*comp) {
	upgrade := c.out.mapAt("upgrade")
	if len(open) == 0 || upgrade == nil || !lateRemoval[upgrade.str("removeOld")] {
		return
	}

	line := c.removeLine
	if c.major != nil {
		line = c.major.line
	}

	const listed = 5

	ids := make([]string, 0, listed)
	for _, comp := range open[:min(len(open), listed)] {
		ids = append(ids, cmp.Or(comp.e.attr("Id"), comp.id, "the component in line "+strconv.Itoa(comp.e.line)))
	}

	list := strings.Join(ids, ", ")

	switch more := len(open) - listed; {
	case more == 1:
		list += " and one more component"
	case more > 1:
		list += fmt.Sprintf(" and %d more components", more)
	}

	c.todof(upgrade, "removeOld", line,
		"WiX generated the Guid of %s; msi-builder generates other codes, and removing the old version after installing "+
			"this one deletes their files on the first upgrade from the WiX package. Run msi-builder convert again with "+
			"--msi and the package WiX built, or set each guid from msi-builder inspect --table Component",
		list)
}

// hasCode reports whether a converted component has a pinned code, not
// none: an unmanaged component is never removed, so it keeps nothing.
func hasCode(component *omap) bool {
	guid := component.str("guid")

	return guid != "" && guid != config.None
}
