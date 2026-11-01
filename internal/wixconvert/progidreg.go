package wixconvert

import (
	"regexp"
	"slices"
	"strings"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// typeRegistry are the registry values of a component that complete a
// ProgId without Extension or Verb children: the commands of its verbs
// and the extensions that list it under OpenWithProgids. A file type
// writes both itself, so convert folds them into it.
type typeRegistry struct {
	// fileIDs are the Ids of the files of the source, which a verb must
	// run: a file type of another file would leave the values out.
	fileIDs map[string]bool
	verbs   []typeVerb
	// openWith are the extensions, without the dot.
	openWith []string
}

// typeVerb is a verb command written as a registry value.
type typeVerb struct {
	id, file, arguments string
}

// classesKey matches a key under the classes root as HKMU or HKCR write
// it: Software\Classes\<rest> or <rest>.
var classesKey = regexp.MustCompile(`(?i)^(?:software\\classes\\)?(.+)$`)

// verbCommand matches a command that runs a file of the package: "[#file]"
// or [#file], then the arguments.
var verbCommand = regexp.MustCompile(`^(?:"\[#([^\]]+)\]"|\[#([^\]\s]+)\])(?:\s+(.*))?$`)

// progIDRegistry finds the registry values that complete the ProgIds of a
// component, marks them consumed and returns them by ProgId element. Only
// plain values in the same branches as the ProgId count, and only in a
// component whose key path is another resource, so that the key path
// stays where WiX has it.
func (c *converter) progIDRegistry(comp *elem) map[*elem]*typeRegistry {
	if !comp.yes("KeyPath") && !slices.ContainsFunc(comp.kids, func(kid *elem) bool { return kid.is(elemFile) || kid.yes("KeyPath") }) {
		return nil
	}

	found := make(map[*elem]*typeRegistry)

	for _, progID := range comp.kids {
		if !progID.is("ProgId") || len(progID.kids) > 0 || progID.yes("Advertise") {
			continue
		}

		if regs := c.progIDValues(comp, progID); len(regs.verbs) > 0 || len(regs.openWith) > 0 {
			found[progID] = regs
		}
	}

	return found
}

// progIDValues collects and consumes the registry values of a component
// that complete one ProgId.
func (c *converter) progIDValues(comp, progID *elem) *typeRegistry {
	regs := &typeRegistry{fileIDs: c.sourceFileIDs()}

	var taken []*elem

	for _, value := range comp.kids {
		if !c.consumed[value] && c.plainClassesValue(value, progID) && c.typeRegValue(value, progID.attr("Id"), regs) {
			taken = append(taken, value)
		}
	}

	if len(regs.verbs) == 0 && !regs.fileIDs[strings.TrimSpace(c.literal(progID.attr("Icon")))] {
		// The file type would go on no file; the values stay.
		return &typeRegistry{}
	}

	for _, value := range taken {
		c.consumed[value] = true
	}

	return regs
}

// sourceFileIDs returns the Ids of the File elements the conversion keeps.
func (c *converter) sourceFileIDs() map[string]bool {
	if c.fileIDs == nil {
		c.fileIDs = make(map[string]bool)

		walkElems(c.doc.root, func(elm *elem) {
			if elm.is(elemFile) && elm.has("Id") {
				c.fileIDs[elm.attr("Id")] = true
			}
		})
	}

	return c.fileIDs
}

// plainClassesValue reports whether a RegistryValue is a string value
// under the classes root, in the branches of the ProgId, that is not the
// key path.
func (c *converter) plainClassesValue(value, progID *elem) bool {
	if !value.is(elemRegistryValue) || value.yes("KeyPath") || len(value.kids) > 0 || value.has("Action") ||
		!slices.Equal(value.pos, progID.pos) {
		return false
	}

	switch value.attr("Type") {
	case "", "string":
	default:
		return false
	}

	switch value.attr("Root") {
	case "HKCR":
		return !strings.HasPrefix(strings.ToLower(c.value(value, "Key")), `software\classes\`)
	case rootHKMU:
		return strings.HasPrefix(strings.ToLower(c.value(value, "Key")), `software\classes\`)
	}

	return false
}

// typeRegValue adds a registry value to regs when it is a verb command of
// the ProgId or lists it under an extension's OpenWithProgids.
func (c *converter) typeRegValue(value *elem, progID string, regs *typeRegistry) bool {
	match := classesKey.FindStringSubmatch(c.value(value, "Key"))
	if match == nil {
		return false
	}

	parts := strings.Split(match[1], `\`)
	name, data := c.value(value, "Name"), c.value(value, "Value")

	switch len(parts) {
	case 4:
		return name == "" && strings.EqualFold(parts[0], progID) && strings.EqualFold(parts[1], "shell") &&
			strings.EqualFold(parts[3], "command") && regs.addVerb(parts[2], data)
	case 2:
		return strings.EqualFold(name, progID) && data == "" && regs.addOpenWith(parts[0], parts[1])
	}

	return false
}

// addOpenWith adds the extension of an .ext\OpenWithProgids key.
func (regs *typeRegistry) addOpenWith(ext, sub string) bool {
	if !strings.HasPrefix(ext, ".") || len(ext) < 2 || !strings.EqualFold(sub, "OpenWithProgids") {
		return false
	}

	regs.openWith = append(regs.openWith, ext[1:])

	return true
}

// addVerb adds a verb whose command runs a file of the package. A file
// type runs one file, so a verb of another file stays a registry value.
func (regs *typeRegistry) addVerb(verb, command string) bool {
	cmd := verbCommand.FindStringSubmatch(command)
	if cmd == nil {
		return false
	}

	file := cmd[1] + cmd[2]
	if !regs.fileIDs[file] || slices.ContainsFunc(regs.verbs, func(v typeVerb) bool { return strings.EqualFold(v.id, verb) }) ||
		len(regs.verbs) > 0 && regs.verbs[0].file != file {
		return false
	}

	regs.verbs = append(regs.verbs, typeVerb{id: verb, file: file, arguments: cmd[3]})

	return true
}

// applyTypeRegistry folds the registry values of a ProgId into its file
// type and returns the file its verbs run, or "".
func applyTypeRegistry(fileType *omap, regs *typeRegistry) string {
	for _, ext := range regs.openWith {
		fileType.add("extensions", newMap().set("extension", ext).set("openWithOnly", true))
	}

	for _, verb := range regs.verbs {
		out := newMap().set("id", verb.id)
		if verb.arguments != config.DefaultVerbArguments {
			out.set("arguments", verb.arguments)

			if out.get("arguments") == nil {
				// No arguments, which an empty string states.
				out.values["arguments"] = emptyText{}
				out.addKey("arguments")
			}
		}

		fileType.add("verbs", out)
	}

	if len(regs.verbs) == 0 {
		return ""
	}

	return regs.verbs[0].file
}
