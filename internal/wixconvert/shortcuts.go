package wixconvert

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	fileTarget   = regexp.MustCompile(`^\[[#!]([A-Za-z_][A-Za-z0-9_.]*)\]$`)
	pathTarget   = regexp.MustCompile(`^\[([A-Za-z_][A-Za-z0-9_.]*)\]\\?([^\[\]\\]+)$`)
	folderTarget = regexp.MustCompile(`^\[([A-Za-z_][A-Za-z0-9_.]*)\]\\?$`)
)

// shortcut converts the attributes that every kind of shortcut has. dirID
// is the WiX directory the shortcut goes into.
func (c *converter) shortcut(elm *elem, dirID string, tree map[*dir]bool) *omap { //nolint:cyclop // a flat list of independent WiX attributes
	link := newMap()
	link.order("id", "name", "file", "folder", "program", "url", "directory", "arguments", "description",
		"workingDirectory", "icon", "iconFile", "iconIndex", "show", "properties")
	link.head = comments(elm)
	link.set("name", c.value(elm, "Name"))

	if d := elm.attr(attrDirectory); d != "" {
		dirID = d
	}

	dirValue := c.dirValue(dirID, tree)
	if dirValue == "" && c.installDir != nil { // the install dir; empty means the Start menu
		dirValue = c.dirRef(c.installDir.id)
	}

	link.set("directory", dirValue)
	link.set("arguments", c.value(elm, "Arguments"))
	link.set("description", c.value(elm, "Description"))
	link.set("icon", c.iconRef(elm, "Icon"))

	if n, err := strconv.Atoi(elm.attr("IconIndex")); err == nil && n != 0 {
		link.set("iconIndex", n)
	}

	switch show := elm.attr("Show"); show {
	case "", "normal":
	case "maximized", "minimized":
		link.set("show", show)
	default:
		c.todof(link, "", elm.line, "Show=%q has no counterpart", show)
	}

	if elm.yes("Advertise") {
		c.todof(link, "", elm.line, "advertised shortcuts are never supported; this one is a plain shortcut")
	}

	for _, a := range []string{"Hotkey", "DescriptionResourceDll", "DisplayResourceDll"} {
		if elm.has(a) {
			c.todof(link, "", elm.line, "Shortcut %s has no counterpart", a)
		}
	}

	for _, kid := range elm.kids {
		switch kid.qname() {
		case "ShortcutProperty":
			if key, ok := c.shellPropertyName(link, kid); ok {
				link.child("properties").set(key, clsidValue(key, c.value(kid, "Value")))
			}
		case "Icon":
			c.icons = append(c.icons, kid)
			link.set("icon", iconID(c.literalStatic(kid.attr("Id"))))
		default:
			c.todof(link, "", kid.line, "<%s> in a Shortcut has no counterpart", kid.qname())
		}
	}

	return link
}

// propertyKey is a shell property key in the "{FMTID}, PID" form.
var propertyKey = regexp.MustCompile(`^\{?([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})\}?\s*,\s*([0-9]+)$`)

// canonicalProperty is the canonical name of a shell property, such as
// System.AppUserModel.ID.
var canonicalProperty = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

// bareGUID is a GUID without braces.
var bareGUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// clsidValue adds the braces that a CLSID shell property needs.
func clsidValue(key, value string) string {
	if strings.HasSuffix(key, "CLSID") && bareGUID.MatchString(value) {
		return "{" + value + "}"
	}

	return value
}

// appUserModelFMTID is the format ID of the System.AppUserModel properties.
const appUserModelFMTID = "9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"

// appUserModelPIDs are the canonical names of the System.AppUserModel
// properties by property ID, from the Windows property system reference.
var appUserModelPIDs = map[string]string{ //nolint:gochecknoglobals // lookup table
	"2": "RelaunchCommand", "3": "RelaunchIconResource", "4": "RelaunchDisplayNameResource", "5": "ID",
	"8": "ExcludeFromShowInNewInstall", "9": "PreventPinning", "11": "IsDualMode", "12": "StartPinOption",
	"26": "ToastActivatorCLSID",
}

// shellPropertyName returns the canonical name msi-builder takes for a
// ShortcutProperty Key, which WiX also takes as "{FMTID}, PID".
func (c *converter) shellPropertyName(link *omap, kid *elem) (string, bool) {
	key := strings.TrimSpace(kid.attr("Key"))

	if m := propertyKey.FindStringSubmatch(key); m != nil {
		pid, _ := strconv.Atoi(m[2])
		if name, ok := appUserModelPIDs[strconv.Itoa(pid)]; ok && strings.EqualFold(m[1], appUserModelFMTID) {
			return "System.AppUserModel." + name, true
		}
	} else if canonicalProperty.MatchString(key) {
		return key, true
	}

	c.todof(link, "", kid.line, "the shell property %s was left out: write it by its canonical name, such as System.AppUserModel.ID", key)

	return "", false
}

// fileShortcut converts a Shortcut nested in a File.
func (c *converter) fileShortcut(f *omap, e *elem, ref *fileRef, tree map[*dir]bool) {
	s := c.shortcut(e, ref.comp.dir, tree)
	c.workingDirectory(s, e, ref.dir)
	f.add("shortcuts", s)
	c.markAdvertised(e)
}

// markAdvertised records an advertised shortcut that became a plain one.
func (c *converter) markAdvertised(e *elem) {
	if e.yes("Advertise") {
		c.advertised = append(c.advertised, e.line)
	}
}

func (c *converter) workingDirectory(s *omap, e *elem, fileDir string) {
	if wd := e.attr("WorkingDirectory"); wd != "" && wd != fileDir {
		s.set("workingDirectory", c.stdID(c.dirRef(wd)))
	}
}

// placeShortcut puts a Shortcut outside a File onto the file it starts when
// that file is in the shortcut's component. On a file of another component
// it would take that component's lifetime, such as permanence, so it stays
// in its own component: a [#file] target as file, which the builder writes
// as WiX does, and a [DIRECTORY]name target as the same program path.
func (c *converter) placeShortcut(pending pendingShortcut) {
	elm := pending.e

	if elm.qname() == "util:InternetShortcut" {
		c.urlShortcut(pending)

		return
	}

	target := elm.attr("Target")
	if file := c.shortcutFile(pending, target); file != nil {
		c.fileTargetShortcut(pending, file)

		return
	}

	link := c.shortcut(elm, pending.comp.dir, pending.tree)

	switch match := folderTarget.FindStringSubmatch(target); {
	case match != nil && !c.isDir(match[1]):
		// A property, such as a URL, not a folder.
		link.set("program", c.expand(elm, target))
	case target == "":
		// The shortcut is left out; its notes stay with the component.
		pending.comp.out.head = append(pending.comp.out.head, link.commentLines()...)
		c.todof(pending.comp.out, "", elm.line,
			"the shortcut %q has no Target: advertised shortcuts are never supported; point it at a file", elm.attr("Name"))

		return
	case match != nil:
		link.set("folder", c.stdID(c.dirRef(match[1])))
	default:
		link.set("program", c.expand(elm, target))
	}

	link.set("workingDirectory", c.stdID(c.dirRef(elm.attr("WorkingDirectory"))))
	pending.comp.out.add("shortcuts", link)
	c.markAdvertised(elm)
}

// shortcutFile returns the packaged file a shortcut starts, when it goes on
// that file or names it with file: the file of an advertised shortcut
// (shortcut() notes that it is no longer advertised), a file of its own
// component, or a [#file] or [!file] target.
func (c *converter) shortcutFile(pending pendingShortcut, target string) *fileRef {
	if target == "" {
		return c.advertisedFile(pending)
	}

	if file := c.targetFile(target); file != nil && (file.comp == pending.comp || fileTarget.MatchString(target)) {
		return file
	}

	return nil
}

// advertisedFile returns the file an advertised shortcut starts: the key
// path of its component, the File marked KeyPath or else the first one.
func (c *converter) advertisedFile(pending pendingShortcut) *fileRef {
	if !pending.e.yes("Advertise") {
		return nil
	}

	var first *fileRef

	key := keyElem(pending.comp.e)

	for _, ref := range c.files {
		switch {
		case ref.comp != pending.comp:
		case ref.e == key:
			return ref
		case first == nil:
			first = ref
		}
	}

	return first
}

// fileTargetShortcut places a shortcut to a packaged file: on the file when
// it is in the shortcut's component, else in its own component with file.
func (c *converter) fileTargetShortcut(pending pendingShortcut, file *fileRef) {
	c.markAdvertised(pending.e)

	link := c.shortcut(pending.e, pending.comp.dir, pending.tree)
	c.workingDirectory(link, pending.e, file.dir)

	if file.comp == pending.comp {
		file.out.add("shortcuts", link)

		return
	}

	name := file.out.str("id")
	if name == "" {
		name = file.name
	}

	link.set("file", name)
	pending.comp.out.add("shortcuts", link)
}

// isDir reports whether an ID names a directory of the file or a standard
// folder.
func (c *converter) isDir(id string) bool {
	return c.dirs[id] != nil || c.isStandard(id)
}

// targetFile finds the packaged file a shortcut target names, as [#id] or
// as [DIRECTORY]name.
func (c *converter) targetFile(target string) *fileRef {
	if m := fileTarget.FindStringSubmatch(target); m != nil {
		for _, f := range c.files {
			if f.key == m[1] {
				return f
			}
		}

		return nil
	}

	if m := pathTarget.FindStringSubmatch(target); m != nil {
		for _, f := range c.files {
			if f.dir == m[1] && strings.EqualFold(f.name, m[2]) {
				return f
			}
		}
	}

	return nil
}

// urlShortcut converts a util:InternetShortcut to a url shortcut.
func (c *converter) urlShortcut(pending pendingShortcut) {
	elm := pending.e
	link := c.shortcut(elm, pending.comp.dir, pending.tree)
	link.set("url", c.value(elm, "Target"))

	if elm.attr("Type") == "link" {
		c.todof(link, "", elm.line, "Type=\"link\" writes a .lnk; msi-builder writes a .url file for url shortcuts")
	}

	if icon := elm.attr("IconFile"); icon != "" {
		if m := fileTarget.FindStringSubmatch(icon); m != nil {
			link.set("iconFile", m[1])
		} else {
			c.todof(link, "", elm.line, "IconFile=%q: iconFile names a file of the package", icon)
		}
	}

	pending.comp.out.add("shortcuts", link)
}
