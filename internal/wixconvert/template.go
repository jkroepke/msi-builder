package wixconvert

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// literalBraces is the action that writes a literal "{{". It uses a raw
// string, which needs no escape in any YAML quoting style.
const literalBraces = "{{ `{{` }}"

// action matches the actions convert writes into text: a variable or a
// literal "{{".
var action = regexp.MustCompile(`\{\{ \.([A-Za-z_][A-Za-z0-9_]*) \}\}|` + regexp.QuoteMeta(literalBraces))

// escapeTemplate writes each "{{" in text as a template action that
// renders it, so that msi-builder reads the text as it is.
func escapeTemplate(text string) string {
	return strings.ReplaceAll(text, "{{", literalBraces)
}

// varAction returns the template action that reads the variable name.
func varAction(name string) string {
	return "{{ ." + name + " }}"
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
			placeholder := fmt.Sprintf("msi-builder-template-placeholder-%d", len(pairs)/2)
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
			operands = append(operands, "."+text[loc[2]:loc[3]])
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

	return []byte(replacer.Replace(buf.String())), nil
}
