package wixconvert

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

const elemCloseApp = "util:CloseApplication"

// closeNote lists what files[].close does differently from
// util:CloseApplication.
const closeNote = "WiX matches the running program by its file name, close by its installed path"

// emitCloseApps converts util:CloseApplication elements that close a
// program the package installs into files[].close of its file, in the
// order of the source.
func (c *converter) emitCloseApps() {
	apps := slices.Clone(c.closeApps)
	slices.SortStableFunc(apps, func(a, b *elem) int { return a.line - b.line })

	for _, elm := range apps {
		target := strings.TrimSpace(c.literal(elm.attr("Target")))

		found := ""
		if p := elm.attr(attrProperty); p != "" {
			found = " (Property " + p + ")"
		}

		if !closesApp(elm) {
			c.todof(nil, "", elm.line, "<util:CloseApplication> for %s only detects the running program%s, "+
				"which has no counterpart; files[].close on the program's file closes it instead", target, found)

			continue
		}

		c.closeTarget(elm, target, found)
	}
}

// closeTarget closes the files of the package named target, or notes why
// files[].close cannot close it.
func (c *converter) closeTarget(elm *elem, target, found string) {
	refs := c.filesNamed(target)

	switch {
	case !strings.EqualFold(path.Ext(target), ".exe") && !strings.Contains(target, "$("):
		c.todof(nil, "", elm.line, "<util:CloseApplication>%s closes %s, which is no program ending in .exe; "+
			"files[].close closes only programs", found, target)
	case len(refs) == 0 && len(c.globLines()) > 0:
		c.todof(nil, "", elm.line, "<util:CloseApplication>%s closes %s, which is no single file of this package; "+
			"when a Files glob (line %s) installs it, exclude it there and list it as a file of its own with close",
			found, target, strings.Join(c.globLines(), ", "))
	case len(refs) == 0:
		c.todof(nil, "", elm.line, "<util:CloseApplication>%s closes %s, which is no file this package installs; "+
			"files[].close closes only files of the package", found, target)
	default:
		for _, ref := range refs {
			c.closeFile(ref, elm)
		}
	}
}

// closesApp reports whether a util:CloseApplication closes or ends the
// program, rather than only setting its Property when it runs.
func closesApp(elm *elem) bool {
	return elm.yes("CloseMessage") || elm.yes("EndSessionMessage") || elm.yes("ElevatedCloseMessage") ||
		elm.yes("ElevatedEndSessionMessage") || elm.has("TerminateProcess")
}

// filesNamed returns the files of the package with an installed name, with
// the defines in their Name or Source filled in.
func (c *converter) filesNamed(name string) []*fileRef {
	var refs []*fileRef

	for _, ref := range c.files {
		if ref.out == nil || ref.e == nil || name == "" {
			continue
		}

		installed := c.literal(ref.e.attr("Name"))
		if installed == "" {
			src := ref.e.attr("Source")
			if src == "" {
				src = ref.e.attr("src")
			}

			installed = baseName(sourcePath(c.literal(src)))
		}

		if strings.EqualFold(installed, name) || strings.EqualFold(afterVars(installed), name) {
			refs = append(refs, ref)
		}
	}

	return refs
}

// closeFile sets close on a file. WiX ends the program only with
// TerminateProcess, close by default; when several elements close one
// file, one that ends it wins, as WiX runs them all.
func (c *converter) closeFile(ref *fileRef, elm *elem) {
	file := ref.out

	switch prev := file.get("close"); {
	case elm.has("TerminateProcess"):
		file.set("close", true)
	case prev == nil:
		file.set("close", flowMap().set("terminate", false))
	}

	// The folder tells apart files of one source in several folders, as
	// todof drops a note it has already recorded.
	source := file.str("source")
	if ref.dir != "" {
		source += " in " + ref.dir
	}

	if cond := c.condition(elm); cond != "" {
		c.todof(file, "close", elm.line, "<util:CloseApplication> closes %s only when %s; close has no condition", source, cond)
	}

	if p := elm.attr(attrProperty); p != "" {
		c.todof(file, "close", elm.line, "<util:CloseApplication> also sets the property %s while %s runs; close "+
			"sets no property, so conditions that read it need another check", p, source)
	}

	if rough := roughCloseAttrs(elm); len(rough) > 0 {
		c.todof(file, "close", elm.line, "close of %s sends the program WM_CLOSE and waits close.timeout; the "+
			"attributes %s of <util:CloseApplication> map to it only roughly or not at all", source, strings.Join(rough, ", "))
	}

	c.todof(file, "close", elm.line, "close of %s came from <util:CloseApplication>: %s", source, closeNote)
}

// globLines returns the source lines of the Files globs, which install
// files that c.files does not list one by one.
func (c *converter) globLines() []string {
	var lines []string

	for _, comp := range c.comps {
		if comp.folder != nil && comp.e != nil {
			lines = append(lines, strconv.Itoa(comp.e.line))
		}
	}

	return lines
}

// roughCloseAttrs returns the util:CloseApplication attributes of elm
// that close maps only roughly or not at all. RebootPrompt is yes unless
// set to no, and close never asks for a reboot; TerminateProcess ends the
// program, so no reboot is needed there.
func roughCloseAttrs(elm *elem) []string {
	var rough []string

	if !elm.no("RebootPrompt") && !elm.has("TerminateProcess") {
		rough = append(rough, "RebootPrompt")
	}

	for _, name := range []string{
		"EndSessionMessage", "ElevatedCloseMessage", "ElevatedEndSessionMessage",
		"PromptToContinue", "Description", "Sequence", "Timeout",
	} {
		if elm.has(name) {
			rough = append(rough, name)
		}
	}

	return rough
}

// afterVars returns the part of an installed name after the last
// variable or bind path that convert could not fill in, as in
// $(var.App.TargetDir)hello.exe, or "" when there is none.
func afterVars(name string) string {
	end := strings.LastIndex(name, ")")
	if end < 0 || !strings.Contains(name[:end], "$(") && !strings.Contains(name[:end], "!(") {
		return ""
	}

	return name[end+1:]
}
