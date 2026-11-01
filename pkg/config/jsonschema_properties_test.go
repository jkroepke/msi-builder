package config_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// TestJSONSchemaProperties checks the values the schema offers for the
// Windows Installer properties it lists, and that any other name still
// takes any value.
func TestJSONSchemaProperties(t *testing.T) {
	t.Parallel()

	sch := compileSchema(t)

	for props, valid := range map[string]bool{
		"{MSIRESTARTMANAGERCONTROL: Disable}":                     true,
		"{MSIRESTARTMANAGERCONTROL: DisableShutdown}":             true,
		"{MSIRESTARTMANAGERCONTROL: 0}":                           true,
		"{MSIRESTARTMANAGERCONTROL: {value: Disable}}":            true,
		`{MSIRESTARTMANAGERCONTROL: "{{ .RM }}"}`:                 true,
		"{MSIRESTARTMANAGERCONTROL: Off}":                         false,
		"{MSIRESTARTMANAGERCONTROL: {value: Off}}":                false,
		"{MSIRESTARTMANAGERCONTROL: {value: Disable, x: 1}}":      false,
		"{MSIRMSHUTDOWN: 2, MSIDISABLERMRESTART: \"1\"}":          true,
		"{MSIRMSHUTDOWN: 3}":                                      false,
		"{REBOOT: ReallySuppress, REBOOTPROMPT: Suppress}":        true,
		"{REBOOT: Never}":                                         false,
		"{MSIFASTINSTALL: 7, INSTALLLEVEL: 100}":                  true,
		"{MSIFASTINSTALL: 8}":                                     false,
		"{INSTALLLEVEL: 0}":                                       false,
		"{ARPSYSTEMCOMPONENT: 1, LIMITUI: 1, DISABLEROLLBACK: 1}": true,
		"{ARPSYSTEMCOMPONENT: yes}":                               false,
		"{MsiLogging: voicewarmup, REINSTALLMODE: amus}":          true,
		"{MsiLogging: v*}":                                        false,
		"{REINSTALLMODE: xyz}":                                    false,
		"{MSIINSTALLPERUSER: \"1\"}":                              true,
		"{ARPHELPTELEPHONE: 555-0100, PRIMARYFOLDER: INSTALLDIR}": true,
		"{MY_SETTING: anything, other: {value: 1, hidden: true}}": true,
		"{ALLUSERS: 1}":                                           false,
		"{ARPNOMODIFY: 1}":                                        false,
		"{ADDLOCAL: ALL}":                                         false,
	} {
		err := schemaValidate(t, sch, base+appComponent+"properties: "+props+"\n")
		if (err == nil) != valid {
			t.Errorf("properties %s: valid = %v, want %v (%v)", props, err == nil, valid, err)
		}
	}
}

// TestJSONSchemaBuilderProperties keeps the properties the schema refuses
// equal to the ones Validate refuses.
func TestJSONSchemaBuilderProperties(t *testing.T) {
	t.Parallel()

	var doc struct {
		Properties struct {
			Properties struct {
				Properties map[string]struct {
					Not json.RawMessage `json:"not"`
				} `json:"properties"`
			} `json:"properties"`
		} `json:"properties"`
	}

	if err := json.Unmarshal(config.JSONSchema(), &doc); err != nil {
		t.Fatal(err)
	}

	var refused []string

	for name, prop := range doc.Properties.Properties.Properties {
		if prop.Not != nil {
			refused = append(refused, name)
		}
	}

	slices.Sort(refused)

	if want := config.BuilderPropertyNames(); !slices.Equal(refused, want) {
		t.Errorf("schema refuses %s,\nValidate refuses %s", strings.Join(refused, " "), strings.Join(want, " "))
	}
}
