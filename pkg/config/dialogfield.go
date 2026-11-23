package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

// tagNull is the YAML tag of a null node.
const tagNull = "!!null"

// maxChoiceText is the width of the RadioButton table's Value (s64) and
// Text (L64) columns.
const maxChoiceText = 64

// UnmarshalYAML accepts a scalar as the short form of {value: scalar}.
func (o *ChoiceOption) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&o.Value) //nolint:wrapcheck // yaml.v4 lists a returned *yaml.LoadErrors with the others
	}

	type plain ChoiceOption

	return decodeStrict(node, (*plain)(o))
}

// ChoiceOptions are the options of a choice field.
type ChoiceOptions []ChoiceOption

// errNullOption reports an empty entry in a choice field's options.
var errNullOption = errors.New("an option cannot be empty")

// UnmarshalYAML rejects a null entry, which the YAML decoder would otherwise
// drop from the list without a trace.
func (o *ChoiceOptions) UnmarshalYAML(node *yaml.Node) error {
	for _, item := range node.Content {
		if item.Tag == tagNull {
			return lineError(item.Line, errNullOption)
		}
	}

	var options []ChoiceOption
	if err := node.Decode(&options); err != nil {
		return err //nolint:wrapcheck // yaml.v4 lists a returned *yaml.LoadErrors with the others
	}

	*o = options

	return nil
}

// wizardRadioProperties are bound to the wizard's own RadioButtonGroups; a
// choice on one of them would add its buttons to that group.
var wizardRadioProperties = set("RMOPTION", "BuilderInstallScope") //nolint:gochecknoglobals // lookup table

// choiceProperties reports a choice field whose property another field, or
// one of the wizard's own radio groups, also binds: a RadioButtonGroup shows
// every RadioButton row of its property, so the buttons would overlap.
func (v *validator) choiceProperties(cfg *Config) {
	bound := make(map[string]int)

	for _, dialog := range cfg.UI.Dialogs {
		for _, field := range dialog.Fields {
			bound[field.Property]++
		}
	}

	for i, dialog := range cfg.UI.Dialogs {
		for index, field := range dialog.Fields {
			if field.Type != FieldChoice {
				continue
			}

			fieldPath := fmt.Sprintf("ui.dialogs[%d].fields[%d].property", i, index)

			switch {
			case wizardRadioProperties[field.Property]:
				v.errorf(fieldPath, "%s belongs to a radio button group of the wizard", field.Property)
			case bound[field.Property] > 1:
				v.errorf(fieldPath, "%s is bound by another field too; a choice needs a property of its own, "+
					"because its group shows every radio button of the property", field.Property)
			}
		}
	}
}

// dialogField checks the type of a ui.dialogs field and the value of a
// checkbox.
func (v *validator) dialogField(cfg *Config, fieldPath string, field *DialogField) {
	v.oneOf(fieldPath+".type", field.Type, "", FieldText, FieldPassword, FieldCheckBox, FieldChoice)

	if field.Type == FieldChoice {
		v.choiceField(cfg, fieldPath, field)
	} else if len(field.Options) > 0 {
		v.errorf(fieldPath+".options", "only a choice field has options")
	}

	if field.Value != "" && field.Type != FieldCheckBox {
		v.errorf(fieldPath+".value", "only a checkbox field has a value")
	}

	// A check box is drawn checked when its property has any value, so a
	// declared default such as "0" starts it checked (reported, not tested).
	if prop := cfg.Properties[field.Property]; field.Type == FieldCheckBox && prop != nil && prop.Value != "" {
		v.warnf(fieldPath+".property", "%s has the default %q, so the checkbox starts checked and leaves that value "+
			"when the user does not touch it; leave the default empty for an unchecked box", field.Property, prop.Value)
	}
}

// choiceOptions checks each option of a choice field and returns the values.
func (v *validator) choiceOptions(fieldPath string, options []ChoiceOption) []string {
	values := make([]string, 0, len(options))

	for i, option := range options {
		optionPath := fmt.Sprintf("%s.options[%d]", fieldPath, i)

		switch {
		case option.Value == "":
			v.errorf(optionPath+".value", "required")
		case utf8.RuneCountInString(option.Value) > maxChoiceText:
			v.errorf(optionPath+".value", "at most %d characters", maxChoiceText)
		case strings.ContainsAny(option.Value, "[]{}"):
			v.errorf(optionPath+".value", "%q: [ ] { } would be formatted by the installer", option.Value)
		case slices.Contains(values, option.Value):
			v.errorf(optionPath+".value", "%q is offered twice", option.Value)
		}

		if utf8.RuneCountInString(option.Label) > maxChoiceText {
			v.errorf(optionPath+".label", "at most %d characters", maxChoiceText)
		}

		values = append(values, option.Value)
	}

	return values
}

// choiceField checks the options of a choice field and the default of its
// property, which must select one of them (ICE34).
func (v *validator) choiceField(cfg *Config, fieldPath string, field *DialogField) {
	if len(field.Options) < 2 {
		v.errorf(fieldPath+".options", "a choice field needs at least two options")
	}

	values := v.choiceOptions(fieldPath, field.Options)

	prop := cfg.Properties[field.Property]
	if prop == nil {
		return
	}

	if prop.Search != nil {
		v.errorf(fieldPath+".property", "%s is searched, so it has no Property row; a choice needs one that selects an "+
			"option (ICE34)", field.Property)
	}

	if prop.Value != "" && !slices.Contains(values, prop.Value) {
		v.errorf(fieldPath+".property", "the default %q of %s is none of the options", prop.Value, field.Property)
	}
}
