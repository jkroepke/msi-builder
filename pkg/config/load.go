package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Options control how a file is loaded.
type Options struct {
	// Vars are the command-line variables. They override the environment
	// and the file's vars.
	Vars map[string]string
	// LookupEnv, when set, is consulted after Vars and before the file's vars.
	LookupEnv func(string) (string, bool)
}

// Load reads, parses and defaults the file at path. It does not validate;
// call Validate on the result.
func Load(path string, opts Options) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err //nolint:wrapcheck // the error names the file
	}

	cfg, err := Parse(data, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	cfg.BaseDir = filepath.Dir(path)

	return cfg, nil
}

// Parse renders a file as a template (see Render), parses the result as
// YAML and applies defaults.
func Parse(data []byte, opts Options) (*Config, error) {
	out, err := renderFile(data, opts)
	if err != nil {
		return nil, err
	}

	cfg, err := decode(out.body)
	if err != nil {
		return nil, err
	}

	cfg.Vars = out.vars

	ApplyDefaults(cfg)

	return cfg, nil
}

// renderedLines is the note on an error that names a line of the rendered
// text, which can differ from the file's where the template wrote lines.
const renderedLines = " (lines of the rendered file; msi-builder render prints it with the same --var and --env flags)"

// checkBody checks that the body is a mapping without vars, which belong
// in the header.
func checkBody(doc *yaml.Node) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("the document must be a mapping")
	}

	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == varsKey {
			return fmt.Errorf("line %d: vars: move them into a vars header, a first document that ends with a --- line, so the template can read them",
				root.Content[i].Line)
		}
	}

	return nil
}

// decode reads the rendered body of a file, which keeps the line numbers
// of the file up to where the template wrote lines.
func decode(body []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w%s", err, renderedLines)
	}

	if err := checkBody(&doc); err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)

	cfg := &Config{}
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w%s", err, renderedLines)
	}

	if cfg.Schema == legacySchemaV1 {
		return nil, fmt.Errorf("schema: %q is the identifier from before the rename to msi-builder; write %q", cfg.Schema, SchemaV1)
	}

	if cfg.Schema != SchemaV1 {
		return nil, fmt.Errorf("schema: got %q, want %q", cfg.Schema, SchemaV1)
	}

	if paths := nullEntries(cfg); len(paths) > 0 {
		errs := make([]error, 0, len(paths))
		for _, path := range paths {
			errs = append(errs, fmt.Errorf("%s: empty entry", path))
		}

		return nil, errors.Join(errs...)
	}

	return cfg, nil
}

// fileVars returns the file's vars and the line of each value. A value
// that comes in through a merge key gets the line of the vars mapping.
func fileVars(root *yaml.Node) (map[string]string, map[string]int, error) {
	lines := make(map[string]int)

	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != varsKey {
			continue
		}

		node := root.Content[i+1]

		vars := make(map[string]string)
		if err := node.Decode(&vars); err != nil {
			return nil, nil, fmt.Errorf("vars: %w", err)
		}

		for name := range vars {
			lines[name] = node.Line
		}

		if node.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(node.Content); j += 2 {
				if _, ok := vars[node.Content[j].Value]; ok && node.Content[j].Value != "<<" {
					lines[node.Content[j].Value] = node.Content[j+1].Line
				}
			}
		}

		return vars, lines, nil
	}

	return make(map[string]string), lines, nil
}
