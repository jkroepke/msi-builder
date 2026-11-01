package config

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// varsKey is the key of the vars header.
const varsKey = "vars"

// header is the vars header of a file: a first YAML document that holds
// nothing but vars and ends with a "---" line, as in
//
//	vars:
//	  ARCH: x64
//	---
//	package:
//	  platform: {{ .ARCH }}
//
// A "---" line may also open the file. The header is plain YAML, so a
// template can read its variables before it is rendered.
type header struct {
	// root is the header's mapping, whose only key is vars.
	root *yaml.Node
	// text is the header up to and including its closing "---" line.
	text []byte
	// lines is the number of lines in text.
	lines int
}

// splitHeader returns the vars header of data, or nil when data has none.
// The body is data[len(h.text):] and starts with the "---" line that ends
// the header. A first document that starts with vars but is not valid YAML
// is an error, so that the YAML error is not lost.
func splitHeader(data []byte) (*header, error) {
	end := headerEnd(data)
	if end < 0 {
		return nil, nil //nolint:nilnil // no header is not an error
	}

	text := data[:end]

	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		if startsWithVars(text) {
			return nil, fmt.Errorf("vars header: %w", err)
		}

		return nil, nil //nolint:nilnil // a first document that is no header
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, nil //nolint:nilnil // a first document that is no header
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content) != 2 || root.Content[0].Value != varsKey {
		return nil, nil //nolint:nilnil // a first document that is no header
	}

	return &header{root: root, text: text, lines: bytes.Count(text, []byte("\n"))}, nil
}

// startsWithVars reports whether the first content line of text is a
// top-level vars key.
func startsWithVars(text []byte) bool {
	for line := range bytes.Lines(text) {
		line = bytes.TrimPrefix(line, []byte("\uFEFF"))

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' || trimmed[0] == '%' || isDocStart(trimmed) {
			continue
		}

		return bytes.HasPrefix(line, []byte(varsKey+":"))
	}

	return false
}

// isDocStart reports whether line is a "---" document marker, alone or
// followed by a space and more, such as a comment.
func isDocStart(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte("---"))

	return ok && (len(rest) == 0 || rest[0] == ' ' || rest[0] == '\t')
}

// headerEnd returns the offset of the "---" line that ends the first
// document of data, or -1 when the first document is not followed by one.
// A "---" line before any content opens the document instead, and a
// directive such as %YAML before it is no content.
func headerEnd(data []byte) int {
	content := false

	for pos := 0; pos < len(data); {
		next := bytes.IndexByte(data[pos:], '\n')
		if next < 0 {
			next = len(data) - pos
		}

		line := bytes.TrimRight(data[pos:pos+next], " \t\r")
		trimmed := bytes.TrimLeft(line, "\uFEFF \t")

		switch {
		case isDocStart(line) && content:
			return pos
		case isDocStart(line):
			// The marker that opens the first document.
			content = true
		case len(trimmed) > 0 && trimmed[0] != '#' && (content || trimmed[0] != '%'):
			content = true
		}

		pos += next + 1
	}

	return -1
}
