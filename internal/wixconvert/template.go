package wixconvert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// literalBraces is the action that writes a literal "{{". It uses a raw
// string, which needs no escape in any YAML quoting style.
const literalBraces = "{{ `{{` }}"

// action matches the actions convert writes into text: a variable, read
// as .NAME, $.NAME inside a loop or $NAME for a loop variable, or a
// literal "{{".
var action = regexp.MustCompile(`\{\{ ((?:\$\.|\$|\.)[A-Za-z_][A-Za-z0-9_]*) \}\}|` + regexp.QuoteMeta(literalBraces))

// escapeTemplate writes each "{{" in text as a template action that
// renders it, so that msi-builder reads the text as it is.
func escapeTemplate(text string) string {
	return strings.ReplaceAll(text, "{{", literalBraces)
}

// quoteVars makes every scalar under node that reads a variable one
// template action that writes the whole value through quote, so that a
// value with a quote, a backslash or YAML syntax arrives as given. The
// scalar gets a placeholder, which the returned replacer swaps for the
// action once the document is encoded, because the encoder would quote
// the action.
func quoteVars(node *yaml.Node) *strings.Replacer {
	var pairs []string

	var walk func(*yaml.Node)

	walk = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && varRef.MatchString(node.Value) {
			placeholder := fmt.Sprintf("msi-builder-template-placeholder-%d-end", len(pairs)/2)
			pairs = append(pairs, placeholder, quotedAction(node.Value))
			node.Value, node.Style, node.Tag = placeholder, 0, ""

			return
		}

		for _, child := range node.Content {
			walk(child)
		}
	}

	walk(node)

	return strings.NewReplacer(pairs...)
}

// quotedAction returns the action that writes text, which holds variable
// actions, as one double-quoted YAML string.
func quotedAction(text string) string {
	var operands []string

	last := 0
	for _, loc := range action.FindAllStringSubmatchIndex(text, -1) {
		if loc[0] > last {
			operands = append(operands, stringOperand(text[last:loc[0]]))
		}

		if loc[2] >= 0 {
			operands = append(operands, text[loc[2]:loc[3]])
		} else {
			operands = append(operands, stringOperand("{{"))
		}

		last = loc[1]
	}

	if last < len(text) {
		operands = append(operands, stringOperand(text[last:]))
	}

	if len(operands) == 1 {
		return "{{ " + operands[0] + " | quote }}"
	}

	// print puts no space between operands that are strings.
	return "{{ print " + strings.Join(operands, " ") + " | quote }}"
}

// stringOperand returns s as a template string constant on one line.
func stringOperand(s string) string {
	if strings.ContainsAny(s, "`\r\n") || !strconv.CanBackquote(s) {
		return strconv.Quote(s)
	}

	return "`" + s + "`"
}

// encodeDocs encodes docs, the last of which is the body.
func encodeDocs(docs []*yaml.Node) ([]byte, error) {
	replacer := quoteVars(docs[len(docs)-1])

	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			return nil, fmt.Errorf("encode: %w", err)
		}
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return []byte(applyBlocks(replacer.Replace(buf.String()))), nil
}

// markerPrefix starts the comment lines that stand for the template
// actions an entry sits in, until applyBlocks replaces them.
const markerPrefix = "#msi-builder-template-block:"

// markerText returns the marker comment lines of opens.
func markerText(opens []openRef) string {
	lines := make([]string, len(opens))

	for i, open := range opens {
		data, err := json.Marshal(open)
		if err != nil {
			panic(err)
		}

		lines[i] = markerPrefix + string(data)
	}

	return strings.Join(lines, "\n")
}

// region is an entry, or a run of entries, inside template actions.
type region struct {
	opens []openRef
	// chain is set for an if that convert opened at the block's first
	// branch and continued with else only, which a later branch can
	// continue with else.
	chain   []bool
	indent  int
	started bool
}

// applyBlocks replaces the marker comments with the template actions they
// stand for and closes each action after the entry: at the next line
// that is not indented deeper than the entry. Entries in the same actions
// share them, and the next branch of a block continues with else.
func applyBlocks(text string) string { //nolint:cyclop // one pass over the lines
	if !strings.Contains(text, markerPrefix) {
		return text
	}

	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]string, 0, len(lines))

	var stack []*region

	emit := func(indent int, action string) {
		out = append(out, strings.Repeat(" ", indent)+"{{ "+action+" }}")
	}

	closeFrom := func(r *region, from int) {
		for range r.opens[from:] {
			emit(r.indent, "end")
		}

		r.opens, r.chain = r.opens[:from], r.chain[:from]
	}

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		marker := strings.HasPrefix(trimmed, markerPrefix)

		if strings.TrimSpace(line) != "" {
			for len(stack) > 0 {
				top := stack[len(stack)-1]
				if !top.started || indent > top.indent {
					break
				}

				if marker && indent == top.indent {
					break
				}

				closeFrom(top, 0)

				stack = stack[:len(stack)-1]
			}
		}

		if !marker {
			if len(stack) > 0 && strings.TrimSpace(line) != "" && !strings.HasPrefix(trimmed, "#") {
				stack[len(stack)-1].started = true
			}

			out = append(out, line)
			i++

			continue
		}

		group := readMarkers(lines, i, indent)
		i += len(group)

		if len(stack) > 0 && stack[len(stack)-1].indent == indent && stack[len(stack)-1].started {
			top := stack[len(stack)-1]
			continueRegion(top, group, emit, closeFrom)
			top.started = false

			continue
		}

		top := &region{indent: indent}
		openFrom(top, group, 0, emit)
		stack = append(stack, top)
	}

	for len(stack) > 0 {
		closeFrom(stack[len(stack)-1], 0)
		stack = stack[:len(stack)-1]
	}

	return strings.Join(out, "\n") + "\n"
}

// readMarkers parses the marker lines at indent from line i on.
func readMarkers(lines []string, i, indent int) []openRef {
	var group []openRef

	for ; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if len(lines[i])-len(trimmed) != indent || !strings.HasPrefix(trimmed, markerPrefix) {
			break
		}

		var open openRef
		if err := json.Unmarshal([]byte(strings.TrimPrefix(trimmed, markerPrefix)), &open); err != nil {
			panic(err)
		}

		group = append(group, open)
	}

	return group
}

// openFrom opens the actions of group from position from on.
func openFrom(r *region, group []openRef, from int, emit func(int, string)) {
	for _, open := range group[from:] {
		emit(r.indent, open.Action)
		r.opens = append(r.opens, open)
		r.chain = append(r.chain, open.Branch == 0 && open.Else == "")
	}
}

// continueRegion moves a region on to the actions of the next entry: it
// keeps the actions both share, continues a block with else when the
// entry is in its next branch, and closes and opens the others.
func continueRegion(reg *region, group []openRef, emit func(int, string), closeFrom func(*region, int)) {
	shared := 0
	for shared < len(reg.opens) && shared < len(group) && reg.opens[shared] == group[shared] {
		shared++
	}

	if shared < len(reg.opens) && shared < len(group) {
		last, next := reg.opens[shared], group[shared]
		if last.Block == next.Block && next.Branch == last.Branch+1 && next.Else != "" && reg.chain[shared] {
			closeFrom(reg, shared+1)
			emit(reg.indent, next.Else)
			reg.opens[shared] = next
			openFrom(reg, group, shared+1, emit)

			return
		}
	}

	closeFrom(reg, shared)
	openFrom(reg, group, shared, emit)
}
