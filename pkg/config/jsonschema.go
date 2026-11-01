package config

import (
	"bytes"
	_ "embed"
)

// schemaJSON is the JSON Schema of the YAML format, for editors. It accepts
// a {{ }} template action wherever a typed value is expected, because
// editors see the file before it is rendered, and it cannot express the cross-reference rules, so
// Validate stays the authority.
//
//go:embed schema.json
var schemaJSON []byte

// JSONSchema returns the JSON Schema (draft 2020-12) of schema msi-builder/v1.
func JSONSchema() []byte {
	return bytes.Clone(schemaJSON)
}
