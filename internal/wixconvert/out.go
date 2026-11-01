package wixconvert

import (
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// omap is a YAML mapping that keeps its keys in insertion order and carries
// comments for the keys.
type omap struct {
	keys     []string
	values   map[string]any
	comments map[string][]string
	// head are comment lines above the mapping, shown above "- " when it
	// is a list item.
	head []string
	// flow writes the mapping on one line, { a: b }.
	flow bool
	// keep writes the mapping even when it is empty, as {}.
	keep bool
}

func newMap() *omap {
	return &omap{values: make(map[string]any), comments: make(map[string][]string)}
}

// set adds or replaces a key. Empty strings, nil maps and empty lists are
// left out, so callers can pass attribute values as they are.
func (m *omap) set(key string, value any) *omap {
	if isEmpty(value) {
		return m
	}

	m.addKey(key)
	m.values[key] = value

	return m
}

// order fixes the position of keys that are set later.
func (m *omap) order(keys ...string) {
	for _, key := range keys {
		m.addKey(key)
	}
}

func (m *omap) addKey(key string) {
	if !slices.Contains(m.keys, key) {
		m.keys = append(m.keys, key)
	}
}

// child returns the mapping at key, adding an empty one.
func (m *omap) child(key string) *omap {
	if c, ok := m.values[key].(*omap); ok {
		return c
	}

	c := newMap()

	m.addKey(key)
	m.values[key] = c

	return c
}

// add appends value to the list at key.
func (m *omap) add(key string, value any) {
	list, ok := m.values[key].([]any)
	if !ok {
		list = nil
	}

	m.addKey(key)

	m.values[key] = append(list, value)
}

func (m *omap) get(key string) any { return m.values[key] }

// str returns the string at key, or "".
func (m *omap) str(key string) string {
	if s, ok := m.values[key].(string); ok {
		return s
	}

	return ""
}

// list returns the string list at key, or nil.
func (m *omap) list(key string) []string {
	if l, ok := m.values[key].([]string); ok {
		return l
	}

	return nil
}

// mapAt returns the mapping at key, or nil.
func (m *omap) mapAt(key string) *omap {
	if sub, ok := m.values[key].(*omap); ok {
		return sub
	}

	return nil
}

func (m *omap) del(key string) {
	if _, ok := m.values[key]; !ok {
		return
	}

	delete(m.values, key)

	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)

			break
		}
	}
}

// comment adds comment lines above key. A key that is never set keeps its
// place, so the comment moves to the next key.
func (m *omap) comment(key string, lines ...string) {
	m.addKey(key)
	m.comments[key] = append(m.comments[key], lines...)
}

func (m *omap) empty() bool { return len(m.values) == 0 }

// commentLines returns the comment lines of an empty mapping: those above
// it and those of its keys.
func (m *omap) commentLines() []string {
	lines := slices.Clone(m.head)
	for _, key := range m.keys {
		lines = append(lines, m.comments[key]...)
	}

	return lines
}

func isEmpty(value any) bool {
	switch val := value.(type) {
	case nil:
		return true
	case string:
		return val == ""
	case []any:
		return len(val) == 0
	case []string:
		return len(val) == 0
	case *omap:
		return val == nil
	}

	return false
}

// node turns a value into a YAML node.
// emptyText is an empty string that is written, as ”, where set leaves
// other empty strings out.
type emptyText struct{}

// emptyList is an empty list that is written, as [].
type emptyList struct{}

func node(value any) *yaml.Node {
	switch val := value.(type) {
	case emptyText:
		return scalar("")
	case emptyList:
		return &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	case *omap:
		return val.node()
	case []any:
		return sequence(val, node, yaml.Style(0))
	case []string:
		return sequence(val, scalar, yaml.FlowStyle)
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(val)}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(val)}
	case string:
		return scalar(val)
	}

	panic("wixconvert: unsupported value type")
}

func sequence[T any](items []T, convert func(T) *yaml.Node, style yaml.Style) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: style}
	for _, item := range items {
		seq.Content = append(seq.Content, convert(item))
	}

	return seq
}

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// node writes the mapping. Comments of keys that ended up without a value
// move to the next key, or above the last one.
func (m *omap) node() *yaml.Node { //nolint:cyclop // one pass in a fixed order; splitting would scatter it
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	if m.flow {
		mapping.Style = yaml.FlowStyle
	}

	if len(m.head) > 0 {
		mapping.HeadComment = commentText(m.head)
	}

	var pending []string

	for _, key := range m.keys {
		pending = append(pending, m.comments[key]...)

		value, ok := m.values[key]
		if !ok || isEmpty(value) {
			continue
		}

		if sub, ok := value.(*omap); ok && sub.empty() && !sub.keep {
			// The mapping is left out; its comments are not.
			pending = append(pending, sub.commentLines()...)

			continue
		}

		keyNode := scalar(key)
		if len(pending) > 0 {
			keyNode.HeadComment = commentText(pending)
			pending = nil
		}

		mapping.Content = append(mapping.Content, keyNode, node(value))
	}

	switch {
	case len(pending) == 0:
	case len(mapping.Content) > 0:
		// A foot comment of a list item lands below the next item's dash,
		// so the comments go above the last key instead.
		last := mapping.Content[len(mapping.Content)-2]
		last.HeadComment = strings.TrimPrefix(last.HeadComment+"\n"+commentText(pending), "\n")
	default:
		mapping.FootComment = commentText(pending)
	}

	return mapping
}

// commentText formats comment lines for a yaml.Node.
func commentText(lines []string) string {
	var text strings.Builder

	for i, line := range lines {
		if i > 0 {
			text.WriteByte('\n')
		}

		// msi-builder renders comments as a template too.
		text.WriteString("# ")
		text.WriteString(escapeTemplate(line))
	}

	return text.String()
}
