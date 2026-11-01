package config_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(config.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		t.Fatal(err)
	}

	sch, err := c.Compile("schema.json")
	if err != nil {
		t.Fatal(err)
	}

	return sch
}

// schemaValidate checks a YAML document, before variable substitution, the
// way an editor sees it.
func schemaValidate(t *testing.T, sch *jsonschema.Schema, doc string) error {
	t.Helper()

	var v any
	if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	return sch.Validate(inst)
}

func TestJSONSchemaAcceptsValidFiles(t *testing.T) {
	t.Parallel()

	sch := compileSchema(t)

	docs := map[string]string{"base": base + appComponent}

	for _, path := range []string{"../../examples/minimal/msi.yaml", "../../examples/windows_exporter/msi.yaml", "../../examples/branded/msi.yaml", "../../examples/desktop/msi.yaml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		docs[path] = string(data)
	}

	for name, doc := range docs {
		if err := schemaValidate(t, sch, doc); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestJSONSchemaRejectsMistakes(t *testing.T) {
	t.Parallel()

	sch := compileSchema(t)

	for name, doc := range map[string]string{
		"unknown key":            base + appComponent + "extraKey: []\n",
		"bad enum":               strings.Replace(base, "upgrade: {}", "upgrade: {downgrade: block}", 1) + appComponent,
		"bad platform":           pkg("{platform: mips}") + appComponent,
		"string for bool":        strings.Replace(base, "upgrade: {}", "upgrade: {allowSameVersion: yes please}", 1) + appComponent,
		"missing required":       strings.Replace(base, "  manufacturer: Example\n", "", 1) + appComponent,
		"rule without component": base + appComponent + "firewall: [{ name: a, program: x }]\n",
		"default with level": strings.Replace(base, "components: [App]\n", "components: [App]\n    default: off\n    level: 3\n", 1) +
			appComponent,
		"feature rule with component": strings.Replace(base, "components: [App]\n",
			"components: [App]\n    firewall: [{ name: a, program: x, component: App }]\n", 1) + appComponent,
	} {
		if err := schemaValidate(t, sch, doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestJSONSchemaUpgradeCode(t *testing.T) {
	t.Parallel()

	sch := compileSchema(t)

	for code, valid := range map[string]bool{
		"3f2504e0-4f89-41d3-9a0c-0305e82c3301":     true,
		`"{3F2504E0-4F89-41D3-9A0C-0305E82C3301}"`: true,
		`"{{ .UPGRADE_CODE }}"`:                    true,
		"00000000-0000-0000-0000-000000000000":     false,
		`"{00000000-0000-0000-0000-000000000000}"`: false,
		"not-a-guid": false,
	} {
		doc := strings.Replace(base, "3f2504e0-4f89-41d3-9a0c-0305e82c3301", code, 1) + appComponent

		related := strings.Replace(base, "upgrade: {}",
			"upgrade: {related: [{upgradeCode: "+code+", maxVersion: 1.0.0}]}", 1) + appComponent

		for name, d := range map[string]string{"product": doc, "related": related} {
			if err := schemaValidate(t, sch, d); (err == nil) != valid {
				t.Errorf("%s upgradeCode %s: valid = %v, want %v (%v)", name, code, err == nil, valid, err)
			}
		}
	}
}

// schemaNode is the part of a JSON Schema that TestJSONSchemaMatchesTypes
// walks.
type schemaNode struct {
	Ref                  string                 `json:"$ref"`
	Defs                 map[string]*schemaNode `json:"$defs"`
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	AdditionalProperties json.RawMessage        `json:"additionalProperties"`
	AnyOf                []*schemaNode          `json:"anyOf"`
	AllOf                []*schemaNode          `json:"allOf"`
}

// additional returns the schema of a map's values, or nil when
// additionalProperties is false or absent.
func (n *schemaNode) additional() *schemaNode {
	var m schemaNode
	if json.Unmarshal(n.AdditionalProperties, &m) != nil {
		return nil
	}

	return &m
}

// TestJSONSchemaMatchesTypes keeps the schema in step with the Go types: every
// YAML key of a struct must be a property of the matching schema object, and
// the other way round.
func TestJSONSchemaMatchesTypes(t *testing.T) {
	t.Parallel()

	var root schemaNode
	if err := json.Unmarshal(config.JSONSchema(), &root); err != nil {
		t.Fatal(err)
	}

	// resolve follows $ref and picks the branch of an anyOf that matches,
	// since typed values also accept a {{ }} string.
	var resolve func(n *schemaNode, match func(*schemaNode) bool) *schemaNode

	resolve = func(node *schemaNode, match func(*schemaNode) bool) *schemaNode {
		if node == nil {
			return nil
		}

		if node.Ref != "" {
			return resolve(root.Defs[strings.TrimPrefix(node.Ref, "#/$defs/")], match)
		}

		if match(node) {
			return node
		}

		for _, branch := range append(node.AnyOf, node.AllOf...) {
			if found := resolve(branch, match); found != nil {
				return found
			}
		}

		return nil
	}

	seen := map[reflect.Type]bool{}

	var walk func(path string, typ reflect.Type, n *schemaNode)

	walk = func(path string, typ reflect.Type, node *schemaNode) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}

		//nolint:exhaustive // scalars have no keys to compare
		switch typ.Kind() {
		case reflect.Struct:
			if seen[typ] {
				return
			}

			seen[typ] = true

			obj := resolve(node, func(n *schemaNode) bool { return n.Properties != nil })
			if obj == nil {
				t.Errorf("%s: no object in the schema for %s", path, typ)

				return
			}

			keys := map[string]bool{}

			for field := range typ.Fields() {
				key, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
				if key == "-" || !field.IsExported() {
					continue
				}

				keys[key] = true

				prop, ok := obj.Properties[key]
				if !ok {
					t.Errorf("%s.%s: missing in the schema", path, key)

					continue
				}

				walk(path+"."+key, field.Type, prop)
			}

			for key := range obj.Properties {
				if !keys[key] {
					t.Errorf("%s.%s: in the schema but not in %s", path, key, typ)
				}
			}
		case reflect.Slice:
			if typ.Elem().Kind() != reflect.Pointer && typ.Elem().Kind() != reflect.Struct {
				return
			}

			if arr := resolve(node, func(n *schemaNode) bool { return n.Items != nil }); arr != nil {
				walk(path+"[]", typ.Elem(), arr.Items)
			} else {
				t.Errorf("%s: no array in the schema", path)
			}
		case reflect.Map:
			if typ.Elem().Kind() == reflect.String {
				return
			}

			if m := resolve(node, func(n *schemaNode) bool { return n.additional() != nil }); m != nil {
				walk(path+"{}", typ.Elem(), m.additional())
			} else {
				t.Errorf("%s: no map in the schema", path)
			}
		}
	}

	walk("config", reflect.TypeFor[config.Config](), &root)
}

// TestJSONSchemaUIStrings keeps the keys the schema offers for ui.strings
// and ui.languages.*.strings equal to the built-in wizard texts.
func TestJSONSchemaUIStrings(t *testing.T) {
	t.Parallel()

	var doc struct {
		Defs map[string]struct {
			PropertyNames struct {
				Enum []string `json:"enum"`
			} `json:"propertyNames"`
		} `json:"$defs"`
	}

	if err := json.Unmarshal(config.JSONSchema(), &doc); err != nil {
		t.Fatal(err)
	}

	got := doc.Defs["uiStrings"].PropertyNames.Enum
	want := slices.Sorted(maps.Keys(config.UIStrings()))

	if !slices.Equal(got, want) {
		t.Errorf("ui.strings keys in the schema:\n%v\nwant (sorted):\n%v", got, want)
	}
}
