package wixconvert

import (
	"cmp"
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined over SHA-1
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jkroepke/msi-builder/internal/folderglob"
	"github.com/jkroepke/msi-builder/pkg/config"
)

// WiX computes the code of a component whose Guid it generates from the
// component's key path (MSISPEC section 8.10, observed on Windows): a version 5 UUID over the UTF-16LE path
// of the key file or registry value. Without a built package, convert
// computes the same code, so that a late removal of the WiX package keeps
// the component (UPG-11, UPG-14).

// wixCodeNamespace is the namespace of the UUIDs WiX generates for
// component codes.
var wixCodeNamespace = [16]byte{ //nolint:gochecknoglobals // a constant byte array
	0x30, 0x64, 0xE5, 0xC6, 0xFB, 0x63, 0x4F, 0xE9, 0xAC, 0x49, 0xE4, 0x46, 0xA7, 0x92, 0xEF, 0xA5,
}

// wixRoots are the names of the registry roots in a key path, by the Root
// column value: WiX 4 and later name them, WiX 3 and -bcgg number them.
var wixRoots = map[int]string{-1: "machineuser", 0: "classesroot", 1: "currentuser", 2: "localmachine", 3: "users"} //nolint:gochecknoglobals,lll // lookup table

// wix6432 are the folders that WiX resolves by the platform it builds for:
// the 32-bit folder for x86, else the 64-bit one.
var wix6432 = map[string][2]string{ //nolint:gochecknoglobals // lookup table
	folderProgramFiles6432:  {folderProgramFiles, "ProgramFiles64Folder"},
	"CommonFiles6432Folder": {"CommonFilesFolder", "CommonFiles64Folder"},
	"System6432Folder":      {"SystemFolder", "System64Folder"},
}

// errNoArch reports a code that depends on the platform WiX built for,
// which neither --arch nor the source names.
var errNoArch = errors.New("the platform WiX built for is unknown; pass --arch")

// errUnresolved reports a key path that convert cannot repeat: a name with
// a variable, a folder outside the source, or no file or registry value.
var errUnresolved = errors.New("unresolved")

// wixCode returns the code WiX generates for a key path string.
func wixCode(keyPath string) string {
	units := utf16.Encode([]rune(keyPath))
	name := make([]byte, 0, 2*len(units))

	for _, unit := range units {
		name = binary.LittleEndian.AppendUint16(name, unit)
	}

	h := sha1.New() //nolint:gosec // UUIDv5 is defined over SHA-1
	h.Write(wixCodeNamespace[:])
	h.Write(name)
	sum := h.Sum(nil)
	sum[6] = sum[6]&0x0F | 0x50
	sum[8] = sum[8]&0x3F | 0x80

	return strings.ToUpper(fmt.Sprintf("{%x-%x-%x-%x-%x}", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16]))
}

// computeCode gives a component whose Guid WiX generates the code WiX
// computes from its key path, when convert can repeat it.
func (c *converter) computeCode(comp *comp) {
	keyPath, err := c.componentKeyPath(comp)
	if err != nil {
		comp.needArch = errors.Is(err, errNoArch)

		return
	}

	comp.out.set("guid", wixCode(keyPath))
	comp.computed = true
}

// componentKeyPath returns the key path string of a component: its key
// file or its key registry value.
func (c *converter) componentKeyPath(comp *comp) (string, error) {
	if comp.e.yes("KeyPath") {
		// The folder is the key path, and WiX generates no code.
		return "", errUnresolved
	}

	if branchedInside(comp.e) {
		// Which element is the key path, and its name, may differ
		// between the builds of a template.
		return "", errUnresolved
	}

	key := keyElem(comp.e)

	switch {
	case key == nil:
		key = loneRegistryValue(comp.e)
	case key.is("File"):
		return c.fileKeyPath(comp, key)
	}

	if key == nil || !key.is("RegistryValue") {
		return "", errUnresolved
	}

	return c.registryKeyPath(comp, key)
}

// branchedInside reports whether an element in comp sits in a kept
// preprocessor block that comp does not.
func branchedInside(comp *elem) bool {
	found := false

	walkElems(comp, func(elm *elem) {
		found = found || slices.ContainsFunc(elm.pos, func(p ppPos) bool {
			return p.block.keep && !slices.Contains(comp.pos, p)
		})
	})

	return found
}

// keyPathless are the elements in a component without a File that cannot
// be its key path, so that a lone RegistryValue next to them is.
var keyPathless = map[string]bool{ //nolint:gochecknoglobals // lookup table
	"RegistryValue": true, "RegistryKey": true, elemShortcut: true, elemRemoveFolder: true, elemRemoveFile: true,
	"RemoveRegistryKey": true, "RemoveRegistryValue": true, elemCondition: true, "Environment": true,
}

// loneRegistryValue returns the RegistryValue that is the key path of a
// component without files and without a marked key path: the only one,
// when nothing else in the component could be the key path.
func loneRegistryValue(elm *elem) *elem {
	var found []*elem

	var walk func(*elem) bool

	walk = func(parent *elem) bool {
		for _, kid := range parent.kids {
			switch {
			case kid.is("RegistryValue"):
				found = append(found, kid)
			case kid.is("RegistryKey"):
				if !walk(kid) {
					return false
				}
			case parent == elm && !keyPathless[kid.name]:
				return false
			}
		}

		return true
	}

	if !walk(elm) || len(found) != 1 {
		return nil
	}

	return found[0]
}

// fileKeyPath returns the key path string of a component whose key path is
// the File key: its folder and its long name, lower case.
func (c *converter) fileKeyPath(comp *comp, key *elem) (string, error) {
	name := c.literalStatic(key.attr("Name"))
	if name == "" {
		source := c.literalStatic(cmp.Or(key.attr("Source"), key.attr("src")))
		name = baseName(sourcePath(source))
	}

	if name == "" || unresolved(name) {
		return "", errUnresolved
	}

	folder, err := c.wixFolder(comp.wixDir)
	if err != nil {
		return "", err
	}

	return folder + `\` + strings.ToLower(name), nil
}

// registryKeyPath returns the key path string of a component whose key
// path is the RegistryValue key, lower case: bitness, root, key and name.
func (c *converter) registryKeyPath(comp *comp, key *elem) (string, error) {
	values := c.registryValues(comp.e, "", "")

	i := slices.IndexFunc(values, func(v builtValue) bool { return v.elm == key })
	if i < 0 {
		return "", errUnresolved
	}

	value := values[i].reg
	if unresolved(value.Key) || unresolved(value.Name) {
		return "", errUnresolved
	}

	bitness, err := c.wixBitness(comp.e)
	if err != nil {
		return "", err
	}

	root := wixRoots[value.Root]
	if c.v3 || c.bcgg {
		root = strconv.Itoa(value.Root)
	}

	return strings.ToLower(bitness + root + `\` + value.Key + `\` + value.Name), nil
}

// wixBitness returns "64" for a 64-bit component and "" for a 32-bit one:
// its Bitness or Win64, else the platform WiX built for.
func (c *converter) wixBitness(elm *elem) (string, error) {
	switch {
	case elm.attr("Bitness") == always64, elm.yes("Win64"):
		return "64", nil
	case elm.attr("Bitness") == always32, elm.no("Win64"):
		return "", nil
	}

	switch c.codeArch() {
	case platformX86:
		return "", nil
	case platformX64, platformArm64:
		return "64", nil
	}

	return "", errNoArch
}

// wixFolder returns the canonical path of a folder: its seed, a standard
// folder, or its parent's path and its long name, lower case.
func (c *converter) wixFolder(dirID string) (string, error) {
	// A parent loop would be a broken source; stop after every folder.
	var names []string

	for range len(c.dirs) + 1 {
		folder := c.wixDir(dirID)

		start, err := c.folderStart(folder, dirID)
		if err == nil && unresolved(start) {
			// A folder that a template picks differs between builds.
			return "", errUnresolved
		}

		if err != nil || start != "" {
			slices.Reverse(names)

			return strings.Join(append([]string{start}, names...), `\`), err
		}

		name := c.literalStatic(folder.wixName)
		if unresolved(name) {
			return "", errUnresolved
		}

		if name == "" {
			// A folder without a name stands for its parent, and WiX
			// writes it as a dot.
			name = "."
		}

		parts := strings.Split(strings.ToLower(name), `\`)
		slices.Reverse(parts)
		names = append(names, parts...)
		dirID = folder.parentID
	}

	return "", errUnresolved
}

// wixDir returns a Directory as the source has it: a nameless one that
// became a standard folder keeps its own record, as WiX computes its codes.
func (c *converter) wixDir(dirID string) *dir {
	if folder := c.wixDirs[dirID]; folder != nil {
		return folder
	}

	return c.dirs[dirID]
}

// folderStart returns the start of a canonical folder path when the folder
// is a seeded or standard one, and "" for another folder.
func (c *converter) folderStart(folder *dir, dirID string) (string, error) {
	if folder != nil && folder.seed != "" {
		seed, err := config.NormalizeGUID(c.literalStatic(folder.seed))
		if err != nil {
			return "", errUnresolved
		}

		return seed, nil
	}

	switch {
	case dirID == folderTargetDir, dirID == "":
		// WiX refuses a key file below TARGETDIR.
		return "", errUnresolved
	case folder == nil && !isStandardFolder(dirID):
		// The folder is in another source file.
		return "", errUnresolved
	case folder != nil && !folder.std:
		return "", nil
	}

	return c.platformFolder(dirID)
}

// platformFolder returns the ID of a standard folder in a key path: a 6432
// folder becomes that of the platform WiX builds for.
func (c *converter) platformFolder(dirID string) (string, error) {
	pair, ok := wix6432[dirID]
	if !ok {
		return dirID, nil
	}

	switch c.codeArch() {
	case platformX86:
		return pair[0], nil
	case platformX64, platformArm64:
		return pair[1], nil
	}

	return "", errNoArch
}

// computeHarvested gives the folder source that a WiX Files glob became
// the codes WiX computes for the files it harvests, in guids: each file is
// in a folder below the glob's folder named as on disk.
func (c *converter) computeHarvested(comp *comp) {
	source := comp.folder.str("source")
	if c.fsys == nil || unresolved(source) || strings.Contains(source, "{{") || path.IsAbs(source) {
		return
	}

	filter, err := folderglob.New(comp.folder.list("include"), comp.folder.list("exclude"))
	if err != nil {
		return
	}

	folder, err := c.wixFolder(comp.wixDir)
	if err != nil {
		comp.needArch = errors.Is(err, errNoArch)

		return
	}

	codes, count := c.harvestCodes(path.Clean(source), filter, folder)
	if count == 0 {
		return
	}

	comp.folder.set("guids", codes)
	comp.computed, comp.pinned = true, true
	comp.computedFiles = count
}

// harvestCodes returns the codes of the files below root that filter
// includes, by their path below root, and how many there are: none when
// the folder cannot be read.
func (c *converter) harvestCodes(root string, filter *folderglob.Filter, folder string) (*omap, int) {
	codes := newMap()
	count := 0

	err := fs.WalkDir(c.fsys, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return err
		}

		rel := name
		if root != "." {
			rel = strings.TrimPrefix(name, root+"/")
		}

		if filter.Included(rel) {
			codes.set(rel, wixCode(folder+`\`+strings.ToLower(strings.ReplaceAll(rel, "/", `\`))))

			count++
		}

		return nil
	})
	if err != nil {
		return nil, 0
	}

	return codes, count
}

// dropSharedCodes takes the computed code off components whose code another
// component has too, since two components never share one; they get the
// note of a component without a code.
func (c *converter) dropSharedCodes() {
	owners := make(map[string][]*comp)

	for _, comp := range c.comps {
		if comp.out == nil || comp.folder != nil {
			continue
		}

		if guid, err := config.NormalizeGUID(comp.out.str("guid")); err == nil {
			owners[guid] = append(owners[guid], comp)
		}
	}

	for _, comps := range owners {
		if len(comps) < 2 {
			continue
		}

		for _, comp := range comps {
			if comp.computed {
				comp.out.del("guid")
				comp.computed = false
			}
		}
	}
}

// unresolved reports whether text still holds a WiX variable after the
// defines were filled in.
func unresolved(text string) bool {
	return strings.Contains(text, "$(") || strings.Contains(text, "!(") || strings.Contains(text, "{{")
}

// computedHeader names the codes convert computed in the header.
func (c *converter) computedHeader() {
	comps, files := c.computedCounts()
	if comps == 0 && files == 0 {
		return
	}

	build := "wix build for any -arch"
	if arch := c.codeArch(); arch != "" {
		build = "wix build -arch " + arch
	}

	if c.bcgg {
		build += " -bcgg"
	}

	var counted []string

	if comps > 0 {
		counted = append(counted, plural(comps, "component"))
	}

	if files > 0 {
		counted = append(counted, plural(files, "file")+" of Files globs")
	}

	c.header = append(c.header, fmt.Sprintf("convert computed the guids of %s whose code WiX generates from their key "+
		"paths, as %s does; with --msi and the package WiX built, it takes them from that package instead.",
		strings.Join(counted, " and "), build))
}

// computedCounts counts the components and the files of folder sources
// whose codes convert computed.
func (c *converter) computedCounts() (int, int) {
	comps, files := 0, 0

	for _, comp := range c.comps {
		switch {
		case !comp.computed || comp.out == nil:
		case comp.folder != nil:
			files += comp.computedFiles
		default:
			comps++
		}
	}

	return comps, files
}

// plural counts things: "1 file", "2 files".
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}

	return strconv.Itoa(n) + " " + thing + "s"
}

// codeArch returns the platform WiX built for when --arch names it, else
// "": the folders a source uses and a WiX 3 Package Platform do not tell
// which -arch WiX built with, and WiX builds for x86 without one.
func (c *converter) codeArch() string {
	if !c.platformNamed {
		return ""
	}

	return c.platform
}
