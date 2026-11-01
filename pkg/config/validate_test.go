package config_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jkroepke/msi-builder/pkg/config"
)

// base is a valid package; each case appends to or replaces parts of it.
const base = `schema: msi-builder/v1
product:
  name: App
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
upgrade: {}
directories:
  - id: ProgramFiles64Folder
    children:
      - id: INSTALLDIR
        name: App
        configurable: true
features:
  - id: Main
    components: [App]
`

const appComponent = `components:
  - id: App
    directory: INSTALLDIR
    files:
      - source: app.exe
`

// progressAction is a customActions section with one immediate action
// whose progress text is text; an empty text leaves the key out.
func progressAction(text string) string {
	progress := ""
	if text != "" {
		progress = "    progress: " + text + "\n"
	}

	return "customActions:\n  - id: Prepare\n    setProperty: {name: A, value: x}\n" + progress +
		"    schedule: [{sequence: execute, after: CostFinalize}]\n"
}

// pkg returns base with its package section replaced.
func pkg(flow string) string {
	return strings.Replace(base, "package:\n  platform: x64\n", "package: "+flow+"\n", 1)
}

func validate(t *testing.T, doc string) config.Diagnostics {
	t.Helper()

	cfg, err := config.Parse([]byte(doc), config.Options{})
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, doc)
	}

	return config.Validate(cfg)
}

func find(ds config.Diagnostics, sev config.Severity, path, msg string) bool {
	for _, d := range ds {
		if d.Severity == sev && d.Path == path && strings.Contains(d.Message, msg) {
			return true
		}
	}

	return false
}

func TestBaseIsValid(t *testing.T) {
	t.Parallel()

	if ds := validate(t, base+appComponent); len(ds) != 0 {
		t.Fatalf("unexpected diagnostics: %v", ds)
	}
}

// TestValidateAccepted checks values that must not produce a diagnostic.
func TestValidateAccepted(t *testing.T) {
	t.Parallel()

	firewall := func(addresses string) string {
		return base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: '" + addresses + "'}]\n"
	}
	environment := func(entries string) string {
		return base + strings.Replace(appComponent, "    files:", "    environment: ["+entries+"]\n    files:", 1)
	}

	for name, doc := range map[string]string{
		"upgrade minVersion": strings.Replace(base, "upgrade: {}", "upgrade: {minVersion: 0.18.0, includeMin: false}", 1) + appComponent,
		// No row of its own unless downgrade is error.
		"newer launch condition with downgrade allow": strings.Replace(base, "upgrade: {}", "upgrade: {downgrade: allow}", 1) + appComponent +
			"launchConditions: [{condition: NOT NEWERVERSIONDETECTED, message: newer}]\n",
		"newer launch condition without upgrade": strings.Replace(base, "upgrade: {}", "upgrade: false", 1) + appComponent +
			"launchConditions: [{condition: NOT NEWERVERSIONDETECTED, message: newer}]\n",
		"newer launch condition with downgrade ignore": strings.Replace(base, "upgrade: {}", "upgrade: {downgrade: ignore}", 1) + appComponent +
			"launchConditions: [{condition: NOT NEWERVERSIONDETECTED, message: newer}]\n",
		"close true": base + strings.Replace(appComponent, "      - source: app.exe\n", "      - {source: app.exe, close: true}\n", 1),
		"close mapping": base + strings.Replace(appComponent, "      - source: app.exe\n",
			"      - {source: app.exe, close: {timeout: 0s, terminate: false}}\n", 1),
		"close upper-case extension": base + strings.Replace(appComponent, "      - source: app.exe\n", "      - {source: App.EXE, close: {timeout: 5m}}\n", 1),
		"close false on a non-program": base + strings.Replace(appComponent, "      - source: app.exe\n",
			"      - source: app.exe\n      - {source: readme.txt, close: false}\n", 1),
		"close false on a folder source": base + strings.Replace(appComponent, "      - source: app.exe\n",
			"      - {source: dist/, close: false}\n", 1),
		"remote any":          firewall("ANY"),
		"remote star":         firewall("*"),
		"remote keyword list": firewall("localsubnet,10.0.0.1"),
		"remote keywords":     firewall("LocalSubnet,dns"),
		"remote property":     firewall("[REMOTE_ADDR]"),
		"environment scopes":  environment("{name: FOO, value: a}, {name: FOO, value: b, scope: user}"),
		"environment actions": environment("{name: FOO, value: a}, {name: FOO, value: b, action: append}"),
		"property search": base + appComponent +
			"properties: {P: {value: x, search: {registry: {root: HKLM, key: Software\\X, name: V, bitness: 32, type: directory}}}}\n",
		"property searches": base + appComponent + "properties: {P: {value: x, search: [" +
			"{registry: {root: HKLM, key: Software\\X, name: V}}, " +
			"{directory: {path: '[ProgramFiles64Folder]Vendor\\App'}}, " +
			"{directory: {path: bin, under: {registry: {root: HKLM, key: Software\\X, name: Home}}}}, " +
			"{file: {name: app.exe, path: '\\\\server\\share', depth: 2, minVersion: 1.2, maxVersion: 2.0.0.0, minSize: 1, maxSize: 10, " +
			"minDate: 2020-01-01, maxDate: 2043-12-31, languages: [0, 1033]}}, " +
			"{file: {name: app.exe, under: {component: {guid: 6A0D3C2E-5B1F-4E7A-9C3D-2F8E1B4A7C60}}}}, " +
			"{component: {guid: '{6a0d3c2e-5b1f-4e7a-9c3d-2f8e1b4a7c60}'}}]}}\n",
		"remove registry": base + strings.Replace(appComponent, "    files:",
			"    removeRegistry: [{root: HKCU, key: Software\\Classes\\old}, {root: HKMU, key: Software\\X, name: Legacy}]\n    files:", 1),
		"remove folders": strings.Replace(base, "        configurable: true\n", "        configurable: true\n        children: [{id: DATADIR, name: data}]\n", 1) +
			"installDir: {id: INSTALLDIR, remember: true}\n" + strings.Replace(appComponent, "    files:",
			"    removeFolders: [{property: INSTALLDIR}, {property: DATA_DIR, on: both, condition: DELETE_DATA=\"1\"}, {property: DATADIR}]\n    files:", 1) +
			"properties: {DATA_DIR: {search: {registry: {root: HKLM, key: Software\\X, name: Data, type: directory}}}}\n",
		// The same key at install and at uninstall, and an uninstall id
		// that a RemoveRegistry row of another entry uses too.
		"remove registry on uninstall": base + strings.Replace(appComponent, "    files:",
			"    removeRegistry: [{id: A, root: HKCR, key: '*\\shell\\app'}, {id: B, root: HKCR, key: '*\\shell\\app', on: uninstall}, "+
				"{id: C, root: HKLM, key: Software\\X, on: both}, {id: A, root: HKLM, key: Software\\Y, on: uninstall}]\n    files:", 1),
		// Program Files and HKCR follow the context of a dual package.
		"dual scope": pkg(`{platform: x64, scope: perUserOrMachine}`) + strings.Replace(appComponent, "    files:",
			"    registry: [{root: HKCR, key: .app, value: App.File}, {root: HKMU, key: Software\\X, name: N, value: v}]\n    files:", 1),
		"dual scope machine first": pkg(`{platform: x64, scope: PerMachineOrUser}`) + appComponent,
		// The schema allows installerVersion as a number, so 5 is 5.0.
		"installer version number": pkg(`{platform: x64, installerVersion: 5}`) + appComponent,
		// A variable follows the context; a perMachine component writes
		// machine data only per machine.
		"dual scope variables": strings.Replace(pkg(`{platform: x64, scope: perMachineOrUser}`), "[App]", "[App, Machine, User]", 1) + strings.Replace(appComponent, "    files:",
			"    environment: [{name: PATH, value: '[INSTALLDIR]', action: append}, {name: FOO, value: a, scope: user}]\n    files:", 1) +
			"  - id: Machine\n    directory: INSTALLDIR\n    scope: perMachine\n" +
			"    registry: [{root: HKLM, key: Software\\X, name: N, value: v}]\n" +
			"    removeRegistry: [{root: HKLM, key: Software\\Old}]\n" +
			"    eventLog: [{source: X, messageFile: '[SystemFolder]EventCreate.exe'}]\n" +
			"    environment: [{name: BAR, value: b, scope: machine}, {name: BAZ, value: c}]\n" +
			"  - {id: User, directory: INSTALLDIR, scope: perUser, environment: [{name: BAZ, value: c}]}\n",
		"language dialog texts": base + appComponent + "properties: {OPT: {}, NAME: {}}\nui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" +
			"  launch: {file: app.exe}\n" +
			"  languages: {de: {launch: {text: '&Starten'}, dialogs: {ExtraDlg: {title: y, description: z, " +
			"fields: {OPT: {label: Stufe, options: {b: '&Hoch'}}, NAME: null}}}}, fr: {dialogs: {ExtraDlg: null}}}\n",
		// A dual package runs an action without impersonation per machine
		// only (CA-09).
		"dual scope elevated action": pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule: [{sequence: execute, before: InstallFinalize, when: install}]
  - id: User
    setProperty: {name: A, value: b}
    scope: perUser
    schedule: [{sequence: execute, after: CostFinalize}]
`,
		// A working directory installs nothing: only the folders ICE105
		// rejects are refused, not Program Files.
		"per-user action in Program Files": strings.Replace(pkg(`{platform: x64, scope: perUser}`), "id: ProgramFiles64Folder", "id: LocalAppDataFolder", 1) +
			appComponent + `customActions:
  - id: Tool
    exec: {command: '"[ProgramFiles64Folder]x\x.exe"', directory: ProgramFiles64Folder}
    execute: deferred
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
		// Installed states and properties are fine in a component condition.
		"installed states": base + strings.Replace(appComponent, "    files:",
			"    condition: '!Main=3 AND ?App=3 OR START=\"&x\"'\n    services: [{name: svc, control: {startCondition: 'START=\"1\"'}}]\n    files:", 1),
		// Only a dual package reserves the scope page's names.
		"scope names in a perMachine package": pkg(`{platform: x64}`) + appComponent +
			"properties:\n  BuilderInstallScope: {value: x}\n",
		// A remove dialog follows MaintenanceTypeDlg without an after.
		"remove dialog": base + appComponent + "properties: {DELETE_CONFIGURATION: {}}\nui:\n  enabled: true\n  dialogs:\n" +
			"    - {id: RemoveOptionsDlg, title: x, flow: remove, fields: [{property: DELETE_CONFIGURATION, type: checkbox}]}\n",
	} {
		if ds := validate(t, doc); len(ds) != 0 {
			t.Errorf("%s: unexpected diagnostics: %v", name, ds)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	const (
		sevError   = config.SeverityError
		sevWarning = config.SeverityWarning
	)

	for name, tc := range map[string]struct {
		doc  string
		sev  config.Severity
		path string
		msg  string
	}{
		// The condition is the LaunchCondition table's primary key.
		"launch condition of downgrade error": {
			base + appComponent + "launchConditions: [{condition: NOT NEWERVERSIONDETECTED, message: newer}]\n",
			sevError, "launchConditions[0].condition", "upgrade.downgrade: error already adds",
		},
		"launch condition of own newer property": {
			strings.Replace(base, "upgrade: {}", "upgrade: {newerProperty: NEWERFOUND}", 1) + appComponent +
				"launchConditions: [{condition: NOT NEWERFOUND, message: newer}]\n",
			sevError, "launchConditions[0].condition", "set upgrade.downgradeMessage",
		},
		"duplicate launch condition": {
			base + appComponent +
				"launchConditions: [{condition: VersionNT64, message: a}, {condition: VersionNT64, message: b}]\n",
			sevError, "launchConditions[1].condition", "already used by launchConditions[0]",
		},
		// INST-05: Condition rows.
		"feature condition without level": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - condition: X = 1\n", 1) + appComponent,
			sevError, "features[0].conditions[0]", "set default or level",
		},
		"feature condition with default and level": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: X = 1, default: on, level: 1 }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].default", "not both",
		},
		"feature condition without condition": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "required",
		},
		"feature condition syntax": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: X =, default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "",
		},
		"feature condition on installed state": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: '!Main = 3', default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "before features and components",
		},
		"feature condition on action state": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: '&Main = 3', default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "before features and components",
		},
		"feature condition level range": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: X = 1, level: 40000 }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].level", "between 0 and",
		},
		"feature condition default value": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: X = 1, default: maybe }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].default", "not one of",
		},
		"feature condition blank": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: \" \", default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "required",
		},
		"feature condition on Installed": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: Installed OR X, default: off }\n", 1) + appComponent,
			sevError, "features[0].conditions[0].condition", "which the builder adds itself",
		},
		"feature conditions share a level": {
			strings.Replace(base, "    components: [App]\n", "    components: [App]\n    conditions:\n      - { condition: X = 1, default: off }\n      - { condition: Y = 1, level: 2 }\n", 1) + appComponent,
			sevError, "features[0].conditions[1]", "join the conditions with OR",
		},
		// MSISPEC-RESULTS finding 2 (SUM-03).
		"page count above 500": {
			pkg(`{platform: x64, installerVersion: "5.1"}`) + appComponent,
			sevError, "package.installerVersion", "1613",
		},
		"x64 below 2.0": {
			pkg(`{platform: x64, installerVersion: "1.5"}`) + appComponent,
			sevError, "package.installerVersion", "at least 2.0",
		},
		"signed installer version": {
			pkg(`{platform: x64, installerVersion: "+5.0"}`) + appComponent,
			sevError, "package.installerVersion", "major.minor",
		},
		"installer version major above 99": {
			pkg(`{platform: x64, installerVersion: "100.0"}`) + appComponent,
			sevError, "package.installerVersion", "highest Windows Installer version",
		},
		"installer version as Page Count": {
			pkg(`{platform: x64, installerVersion: 500}`) + appComponent,
			sevError, "package.installerVersion", "not the Page Count: write 5.0",
		},
		"arm64 below 5.0": {
			pkg(`{platform: arm64, installerVersion: "4.5"}`) + appComponent,
			sevError, "package.installerVersion", "at least 5.0",
		},
		// FILE-01: an unversioned executable is deleted by an upgrade.
		"unversioned exe": {
			base + appComponent + "      - source: helper.dll\n        version: none\n",
			sevError, "components[0].files[1].version", "FILE-01",
		},
		"unversioned exe acknowledged": {
			base + appComponent + "      - source: helper.dll\n        version: none\n        allowUnversionedExecutable: true\n",
			sevWarning, "components[0].files[1].version", "FILE-01",
		},
		"empty version is not none": {
			base + appComponent + "      - source: data.txt\n        version: \"1.x\"\n",
			sevError, "components[0].files[1].version", "not auto, none",
		},
		// UPG-01 and UPG-03.
		"four-field version": {
			strings.Replace(base, "version: 1.0.0", "version: 1.0.0.5", 1) + appComponent,
			sevWarning, "product.version", "UPG-01",
		},
		"nil upgrade code": {
			strings.Replace(base, "3f2504e0-4f89-41d3-9a0c-0305e82c3301", `"{00000000-0000-0000-0000-000000000000}"`, 1) + appComponent,
			sevError, "product.upgradeCode", "nil GUID",
		},
		"nil related upgrade code": {
			strings.Replace(base, "upgrade: {}", "upgrade: {related: [{upgradeCode: 00000000-0000-0000-0000-000000000000, maxVersion: 1.0.0}]}", 1) + appComponent,
			sevError, "upgrade.related[0].upgradeCode", "nil GUID",
		},
		"pinned product code": {
			strings.Replace(base, "  upgradeCode:", "  productCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3302\n  upgradeCode:", 1) + appComponent,
			sevWarning, "product.productCode", "1638",
		},
		// CA-02: exec actions must say how an exit code is handled.
		"exec without return": {
			base + appComponent + `customActions:
  - id: Run
    exec: {command: cmd.exe}
    execute: deferred
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].return", "CA-02",
		},
		// CA-08: progress texts.
		"blank progress": {
			base + appComponent + progressAction("'  '"),
			sevError, "customActions[0].progress", "blank",
		},
		"progress wider than its column": {
			base + appComponent + progressAction(strings.Repeat("x", 65)),
			sevWarning, "customActions[0].progress", "ICE03",
		},
		"progress for an unknown action": {
			base + appComponent + progressAction("Preparing") + "ui: {enabled: true, languages: {de: {progress: {Other: Vorbereiten}}}}\n",
			sevError, "ui.languages.de.progress.Other", "not the id of a custom action",
		},
		"progress translation without a base text": {
			base + appComponent + progressAction("") + "ui: {enabled: true, languages: {de: {progress: {Prepare: Vorbereiten}}}}\n",
			sevError, "ui.languages.de.progress.Prepare", "no progress text",
		},
		"progress for a generated action": {
			base + appComponent + "    services: [{ name: svc, arguments: [{ value: -b, condition: B }] }]\n" +
				"ui: {enabled: true, languages: {de: {progress: {SetArg_svc_1: Vorbereiten}}}}\n",
			sevError, "ui.languages.de.progress.SetArg_svc_1", "not the id of a custom action",
		},
		"empty progress translation": {
			base + appComponent + progressAction("Preparing") + "ui: {enabled: true, languages: {de: {progress: {Prepare: ''}}}}\n",
			sevError, "ui.languages.de.progress.Prepare", "empty",
		},
		// CA-03: run actions carry the same return rule and a few of their own.
		"run without return": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: cmd.exe}
    execute: deferred
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].return", "CA-02",
		},
		"run async": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: cmd.exe}
    execute: deferred
    return: asyncNoWait
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].return", "asyncNoWait",
		},
		// An unquoted executable from a property is hijacked through the
		// CreateProcess prefix search (CWE-428).
		"run unquoted executable": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: '[INSTALLDIR]tool.exe --init'}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].run.command", "must be quoted",
		},
		// A property alone may be an UninstallString, which is quoted already.
		"run bare property executable": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: '[INNO_UNINSTALL] /VERYSILENT'}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevWarning, "customActions[0].run.command", "property [INNO_UNINSTALL] alone",
		},
		"run directory alone": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: '[INSTALLDIR] /S'}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].run.command", "must be quoted",
		},
		"exec unquoted executable": {
			base + appComponent + `customActions:
  - id: Run
    exec: {command: '[#app.exe] --init'}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].exec.command", "must be quoted",
		},
		"run unknown directory": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: cmd.exe, directory: NOWHERE}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].run.directory", "not defined",
		},
		"run id too long for .Data": {
			base + appComponent + `customActions:
  - id: Run67890123456789012345678901234567890123456789012345678901234567890
    run: {command: cmd.exe}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].id", ".Data",
		},
		"run data id clash": {
			base + appComponent + `customActions:
  - id: Run
    run: {command: cmd.exe}
    execute: deferred
    return: check
    schedule: [{sequence: execute, after: InstallFiles}]
  - id: Run.Data
    setProperty: {name: A, value: b}
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[1].id", "data action",
		},
		"deferred set property": {
			base + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    execute: deferred
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].execute", "cannot be deferred",
		},
		"no impersonation immediate": {
			base + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    impersonate: false
    schedule: [{sequence: execute, after: InstallFiles}]
`,
			sevError, "customActions[0].impersonate", "deferred",
		},
		"dual scope elevated action without scope": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevError, "customActions[0].impersonate", "add scope: perMachine",
		},
		"dual scope elevated admin action": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    return: check
    schedule: [{sequence: adminExecute, before: InstallFinalize}]
`,
			sevError, "customActions[0].impersonate", "only with scope: perMachine",
		},
		"dual scope elevated perUser action": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    scope: perUser
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevError, "customActions[0].impersonate", "a perUser action runs impersonated",
		},
		"per-machine action with scope": {
			base + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    scope: perMachine
    schedule: [{sequence: execute, after: CostFinalize}]
`,
			sevError, "customActions[0].scope", "only a perUserOrMachine or perMachineOrUser package",
		},
		"per-user elevated action with scope": {
			pkg(`{platform: x64, scope: perUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevError, "customActions[0].scope", "only a perUserOrMachine or perMachineOrUser package",
		},
		"dual scope action in the system folder": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: System64Folder}
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevError, "customActions[0].exec.directory", "System64Folder",
		},
		"per-user action in the temp folder": {
			strings.Replace(pkg(`{platform: x64, scope: perUser}`), "id: ProgramFiles64Folder", "id: LocalAppDataFolder", 1) +
				appComponent + `customActions:
  - id: Tool
    run: {command: '"[INSTALLDIR]tool.exe"', directory: TempFolder}
    execute: deferred
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevError, "customActions[0].run.directory", "TempFolder, which the action's directory adds (ICE105)",
		},
		"action scope in the admin execute sequence": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    scope: perUser
    schedule: [{sequence: adminExecute, after: CostFinalize}]
`,
			sevError, "customActions[0].schedule[0].sequence", "only in the execute sequence",
		},
		"elevated action in a per-user-first package": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + `customActions:
  - id: Machine
    exec: {command: '"[System64Folder]cmd.exe" /c ver', directory: INSTALLDIR}
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule: [{sequence: execute, before: InstallFinalize}]
`,
			sevWarning, "customActions[0].scope", "ICE105 warns",
		},
		"action scope dual": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    scope: perUserOrMachine
    schedule: [{sequence: execute, after: CostFinalize}]
`,
			sevError, "customActions[0].scope", "not one of",
		},
		"action scope in the UI sequence": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + `customActions:
  - id: Set
    setProperty: {name: A, value: b}
    scope: perMachine
    schedule: [{sequence: execute, after: CostFinalize}, {sequence: ui, after: CostFinalize}]
`,
			sevError, "customActions[0].schedule[1].sequence", "only in the execute sequence",
		},
		"standard action name": {
			base + appComponent + `customActions:
  - id: InstallFiles
    error: no
    schedule: [{sequence: execute, after: CostFinalize}]
`,
			sevError, "customActions[0].id", "ICE93",
		},
		"unknown anchor": {
			base + appComponent + `customActions:
  - id: Fail
    error: no
    schedule: [{sequence: execute, after: Nowhere}]
`,
			sevError, "customActions[0].schedule[0]", "Nowhere",
		},
		// Finding 1 (MSIDB-07) and MSIDB-02.
		"32 columns": {
			base + appComponent + "tables:\n  - name: Wide\n    columns:\n" +
				strings.Repeat("      - {name: C, type: I2, key: true}\n", 32),
			sevError, "tables[0].columns", "MSIDB-07",
		},
		"i2 minimum": {
			base + appComponent + `tables:
  - name: T
    columns: [{name: K, type: s72, key: true}, {name: V, type: I2}]
    rows: [[a, -32768]]
`,
			sevError, "tables[0].rows[0][1]", "outside",
		},
		"standard table": {
			base + appComponent + "tables:\n  - name: Property\n    columns: [{name: K, type: s72, key: true}]\n",
			sevError, "tables[0].name", "written by the builder",
		},
		// MSIDB-11.
		"binary key too long": {
			base + appComponent + "binaries:\n  " + strings.Repeat("b", 56) + ": x.dll\n",
			sevError, "binaries." + strings.Repeat("b", 56), "MSIDB-11",
		},
		// ENVVAR-01: removal is never implied by scope.
		"environment prefix": {
			base + strings.Replace(appComponent, "    files:", "    environment: [{name: '*PATH', value: x}]\n    files:", 1),
			sevError, "components[0].environment[0].name", "plain name",
		},
		"environment twice": {
			base + strings.Replace(appComponent, "    files:", "    environment: [{name: FOO, value: a}, {name: FOO, value: b}]\n    files:", 1),
			sevError, "components[0].environment[1]", "FOO with action set and scope machine is set twice",
		},
		"firewall description quote": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, description: 'say \"hi\"'}]\n",
			sevError, "firewall[0].description", "double quote",
		},
		"firewall name all": {
			base + appComponent + "firewall: [{name: ' ALL', component: App, program: x}]\n",
			sevError, "firewall[0].name", "reserved by netsh",
		},
		"firewall name property": {
			base + appComponent + "firewall: [{name: '[RULE]', component: App, program: x}]\n",
			sevError, "firewall[0].name", "literal text",
		},
		"firewall any with addresses": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: 'ANY,10.0.0.1'}]\n",
			sevError, "firewall[0].remoteAddresses", "ANY cannot be combined",
		},
		"firewall spaced addresses": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: '10.0.0.1, 10.0.0.2'}]\n",
			sevError, "firewall[0].remoteAddresses", "must not contain spaces",
		},
		"firewall quoted address": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: '\"10.0.0.1\"'}]\n",
			sevError, "firewall[0].remoteAddresses", "double quotes",
		},
		"firewall empty address": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: 'any,'}]\n",
			sevError, "firewall[0].remoteAddresses", "empty entry",
		},
		"firewall star with addresses": {
			base + appComponent + "firewall: [{name: a, component: App, program: x, remoteAddresses: '10.0.0.1,*'}]\n",
			sevError, "firewall[0].remoteAddresses", "* cannot be combined",
		},
		"environment twice in another case": {
			base + strings.Replace(appComponent, "    files:", "    environment: [{name: FOO, value: a}, {name: foo, value: b}]\n    files:", 1),
			sevError, "components[0].environment[1]", "foo with action set and scope machine is set twice",
		},
		"PATH set": {
			base + strings.Replace(appComponent, "    files:", "    environment: [{name: PATH, value: x}]\n    files:", 1),
			sevWarning, "components[0].environment[0].action", "append",
		},
		"lzx window too small": {
			pkg(`{platform: x64, compression: "lzx:14"}`) + appComponent,
			sevError, "package.compression", `"lzx:14" is not one of`,
		},
		// SUM-04.
		"utf-16 codepage": {
			pkg(`{platform: x64, codepage: 1200}`) + appComponent,
			sevError, "package.codepage", "1200",
		},
		// SVC-01.
		"service keeps registration": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, control: {deleteOnUninstall: false}}]\n    files:", 1),
			sevWarning, "components[0].services[0].control.deleteOnUninstall", "SVC-01",
		},
		"waitForStart without start": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, control: {startOnInstall: false, waitForStart: false}}]\n    files:", 1),
			sevWarning, "components[0].services[0].control.waitForStart", "startOnInstall",
		},
		// SVC-05.
		"start a disabled service": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, start: disabled, control: {startOnInstall: true}}]\n    files:", 1),
			sevError, "components[0].services[0].control.startOnInstall", "SVC-05",
		},
		"service on dll": {
			base + strings.Replace(strings.Replace(appComponent, "app.exe", "app.dll", 1), "    files:", "    services: [{name: svc}]\n    files:", 1),
			sevError, "components[0].services[0]", "not an .exe",
		},
		"literal password": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, password: hunter2}]\n    files:", 1),
			sevError, "components[0].services[0].password", "hidden property",
		},
		"recovery reset twice": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {resetAfterDays: 1, resetAfter: 24h, actions: [restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.resetAfter", "not both",
		},
		"recovery reset fraction": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {resetAfter: 1500ms, actions: [restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.resetAfter", "whole number of seconds",
		},
		"fileTypes no progId": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{extensions: [{extension: acme}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].progId", "required",
		},
		"fileTypes progId backslash": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: 'Acme\\\\Doc'}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].progId", "must not hold",
		},
		"fileTypes progId dot": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: .acme}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].progId", "starts with a dot",
		},
		"fileTypes progId twice": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc}, {progId: acme.doc}]\n", 1),
			sevError, "components[0].files[0].fileTypes[1].progId", "registers",
		},
		"fileTypes progId style": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme_Doc}]\n", 1),
			sevWarning, "components[0].files[0].fileTypes[0].progId", "COM-style",
		},
		"fileTypes extension dot": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, extensions: [{extension: .acme}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].extensions[0].extension", "without it",
		},
		"fileTypes extension twice": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, extensions: [{extension: acme}]}, {progId: Acme.Other, extensions: [{extension: ACME}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[1].extensions[0].extension", "the last one written wins",
		},
		"fileTypes system extension": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, extensions: [{extension: txt}]}]\n", 1),
			sevWarning, "components[0].files[0].fileTypes[0].extensions[0].extension", "takes it over",
		},
		"fileTypes progId star": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: '*'}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].progId", "not a ProgID",
		},
		"fileTypes extension twice in one type": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, extensions: [{extension: acme}, {extension: Acme, openWithOnly: true}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].extensions[1].extension", "listed twice",
		},
		"fileTypes openWithOnly contentType": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, extensions: [{extension: txt, openWithOnly: true, contentType: text/plain}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].extensions[0].contentType", "openWithOnly",
		},
		"fileTypes verb twice": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n        fileTypes: [{progId: Acme.Doc, verbs: [{id: open}, {id: Open}]}]\n", 1),
			sevError, "components[0].files[0].fileTypes[0].verbs[1].id", "listed twice",
		},
		"registry value twice": {
			base + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\App, name: a, value: x}, {root: HKLM, key: software\\app, name: A, value: y}]\n    files:", 1),
			sevError, "components[0].registry[1]", "already writes this registry value",
		},
		"default install dir beside a tree": {
			strings.ReplaceAll(base, "INSTALLDIR", "APPLICATIONFOLDER") + "components:\n  - files: [{ source: app.exe }]\n",
			sevWarning, "installDir", "next to APPLICATIONFOLDER",
		},
		"scheduleReboot bad when": {
			base + appComponent + "scheduleReboot: { when: always }\n",
			sevError, "scheduleReboot.when", "always",
		},
		"scheduleReboot bad condition": {
			base + appComponent + "scheduleReboot: { condition: 'A AND' }\n",
			sevError, "scheduleReboot.condition", "",
		},
		"removeFiles bad on": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"*.log\", on: upgrade}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].on", "upgrade",
		},
		"removeFiles undefined directory": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"*\", directory: Logs}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].directory", "not defined",
		},
		"removeFiles path in name": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"logs\\\\*.log\"}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].name", "one folder only",
		},
		"removeFiles short name part": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"app~1.log|app.log\"}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].name", "short name part",
		},
		"removeFiles pattern too long": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \""+strings.Repeat("a", 243)+"\"}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].name", "longer than 242",
		},
		"removeFiles twice": {
			base + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"*.log\"}, {name: \"*.LOG\"}]\n    files:", 1),
			sevError, "components[0].removeFiles[1]", "already removes",
		},
		"removeFiles long pattern in a short-name package": {
			pkg("{platform: x64, longFileNames: false}") + strings.Replace(appComponent, "    files:", "    removeFiles: [{name: \"service-*.log\"}]\n    files:", 1),
			sevError, "components[0].removeFiles[0].name", "8.3 pattern",
		},
		"recovery restart delay negative": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {restartDelay: -1s, actions: [restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.restartDelay", "negative",
		},
		"recovery restart delay fraction": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {restartDelay: 1500us, actions: [restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.restartDelay", "whole number of milliseconds",
		},
		"recovery step delay negative": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {actions: [{restart: -1s}]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.actions[0].restart", "negative",
		},
		"recovery step delay too long": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {actions: [{restart: 600h}]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.actions[0].restart", "too long",
		},
		"recovery reset days too long": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {resetAfterDays: 30000, actions: [restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.resetAfterDays", "too long",
		},
		"recovery four steps": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {actions: [restart, restart, restart, restart]}}]\n    files:", 1),
			sevError, "components[0].services[0].recovery.actions", "one to three",
		},
		"unknown wizard text": {
			base + appComponent + "ui:\n  strings: {NextButton: Weiter}\n",
			sevError, "ui.strings.NextButton", "not a wizard text",
		},
		"empty wizard text": {
			base + appComponent + "ui:\n  strings: {Next: ''}\n",
			sevError, "ui.strings.Next", "leave the key out",
		},
		"wizard text over its column": {
			base + appComponent + "ui:\n  strings: {RMUseRM: " + strings.Repeat("x", 65) + "}\n",
			sevWarning, "ui.strings.RMUseRM", "column width of 64",
		},
		"wizard texts without a wizard": {
			base + appComponent + "ui:\n  enabled: false\n  strings: {Next: Weiter}\n",
			sevError, "ui.strings", "need a wizard",
		},
		"dialog flow": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, flow: uninstall, fields: [{property: OPT}]}\n",
			sevError, "ui.dialogs[0].flow", "not one of",
		},
		"install dialog without after": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, flow: install, fields: [{property: OPT}]}\n",
			sevError, "ui.dialogs[0].after", "required",
		},
		"remove dialog without Change": {
			strings.Replace(base, "product:\n", "product:\n  arp: {noModify: true}\n", 1) + appComponent + "ui:\n  enabled: true\n  dialogs:\n" +
				"    - {id: ExtraDlg, title: x, flow: remove, fields: [{property: OPT}]}\n",
			sevWarning, "ui.dialogs[0].flow", "only when the package runs again",
		},
		"remove dialog without Remove": {
			strings.Replace(base, "product:\n", "product:\n  arp: {noRemove: true}\n", 1) + appComponent + "ui:\n  enabled: true\n  dialogs:\n" +
				"    - {id: ExtraDlg, title: x, flow: remove, fields: [{property: OPT}]}\n",
			sevWarning, "ui.dialogs[0].flow", "no one can reach this page",
		},
		"dialog field type": {
			base + appComponent + "ui:\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: radio}]}\n",
			sevError, "ui.dialogs[0].fields[0].type", "",
		},
		"dialog field value on text": {
			base + appComponent + "ui:\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, value: x}]}\n",
			sevError, "ui.dialogs[0].fields[0].value", "only a checkbox",
		},
		"choice one option": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options", "at least two",
		},
		"choice duplicate": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, a]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options[1].value", "offered twice",
		},
		"choice brackets": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, '[b]']}]}\n",
			sevError, "ui.dialogs[0].fields[0].options[1].value", "formatted",
		},
		"choice long value": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options[1].value", "at most 64",
		},
		"choice long label": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, {value: b, label: yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy}]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options[1].label", "at most 64",
		},
		"choice empty value": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, {label: b}]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options[1].value", "required",
		},
		"choice default not an option": {
			base + appComponent + "properties: {OPT: c}\nui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}]}\n",
			sevError, "ui.dialogs[0].fields[0].property", "none of the options",
		},
		"choice on a searched property": {
			base + appComponent + "properties: {OPT: {search: {registry: {root: HKLM, key: X}}}}\nui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}]}\n",
			sevError, "ui.dialogs[0].fields[0].property", "ICE34",
		},
		"options on text": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, options: [a, b]}]}\n",
			sevError, "ui.dialogs[0].fields[0].options", "only a choice",
		},
		"choice shares a property": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: OPT}]}\n",
			sevError, "ui.dialogs[0].fields[0].property", "another field",
		},
		"choice on RMOPTION": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: RMOPTION, type: choice, options: [a, b]}]}\n",
			sevError, "ui.dialogs[0].fields[0].property", "wizard",
		},
		"checkbox with a default": {
			base + appComponent + "properties: {OPT: '0'}\nui:\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: checkbox}]}\n",
			sevWarning, "ui.dialogs[0].fields[0].property", "starts checked",
		},
		"language dialog unknown": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {OtherDlg: {title: y}}}}\n",
			sevError, "ui.languages.de.dialogs.OtherDlg", "not the id of a ui.dialogs page",
		},
		"language dialog blank title": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {ExtraDlg: {title: ' '}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.title", "blank",
		},
		"language field unknown": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {ExtraDlg: {fields: {OTHER: {label: y}}}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.fields.OTHER", "no field of ExtraDlg binds OTHER",
		},
		"language field shared property": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT}, {property: OPT, type: password}]}\n" +
				"  languages: {de: {dialogs: {ExtraDlg: {fields: {OPT: {label: y}}}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.fields.OPT", "cannot tell them apart",
		},
		"language options on text": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {ExtraDlg: {fields: {NAME: {options: {a: y}}}}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.fields.NAME.options", "only a choice",
		},
		"language option unknown": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {ExtraDlg: {fields: {OPT: {options: {c: y}}}}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.fields.OPT.options.c", "not an option",
		},
		"language option long label": {
			base + appComponent + "ui:\n  enabled: true\n  dialogs:\n    - {id: ExtraDlg, title: x, after: WelcomeDlg, fields: [{property: OPT, type: choice, options: [a, b]}, {property: NAME}]}\n" + "  languages: {de: {dialogs: {ExtraDlg: {fields: {OPT: {options: {a: " + strings.Repeat("y", 65) + "}}}}}}}\n",
			sevError, "ui.languages.de.dialogs.ExtraDlg.fields.OPT.options.a", "at most 64",
		},
		"language launch without launch": {
			base + appComponent + "ui:\n  enabled: true\n  languages: {de: {launch: {text: Starten}}}\n",
			sevError, "ui.languages.de.launch", "no ui.launch",
		},
		"language launch blank": {
			base + appComponent + "ui:\n  enabled: true\n  launch: {file: app.exe}\n  languages: {de: {launch: {text: ' '}}}\n",
			sevError, "ui.languages.de.launch.text", "blank",
		},
		"schedule when": {
			base + appComponent + "customActions:\n  - id: Set\n    setProperty: {name: A, value: b}\n    schedule: [{sequence: execute, after: CostFinalize, when: always}]\n",
			sevError, "customActions[0].schedule[0].when", "",
		},
		"schedule when upgrade without upgrades": {
			strings.Replace(base, "upgrade: {}", "upgrade: false", 1) + appComponent +
				"customActions:\n  - id: Set\n    setProperty: {name: A, value: b}\n    schedule: [{sequence: execute, after: CostFinalize, when: upgrade}]\n",
			sevError, "customActions[0].schedule[0].when", "upgrade section",
		},
		"schedule when uninstall in ui": {
			base + appComponent + "customActions:\n  - id: Set\n    setProperty: {name: A, value: b}\n    schedule: [{sequence: ui, after: CostFinalize, when: uninstall}]\n",
			sevError, "customActions[0].schedule[0].when", "InstallValidate",
		},
		"schedule when in admin": {
			base + appComponent + "customActions:\n  - id: Set\n    setProperty: {name: A, value: b}\n    schedule: [{sequence: adminExecute, after: CostFinalize, when: install}]\n",
			sevError, "customActions[0].schedule[0].when", "administrative",
		},
		// SEARCH-03.
		"remember with a component of the same id": {
			strings.Replace(base, "        configurable: true\n", "        configurable: true\ninstallDir: {id: INSTALLDIR, remember: true}\n", 1) +
				strings.Replace(appComponent, "id: App", "id: RememberInstallDir", 1),
			sevError, "components[0].id", "installDir.remember adds",
		},
		"remember a private install dir": {
			strings.Replace(base, "      - id: INSTALLDIR\n        name: App\n        configurable: true\n", "      - id: AppDir\n        name: App\n", 1) +
				"installDir: {id: AppDir, remember: true}\n" + strings.Replace(appComponent, "INSTALLDIR", "AppDir", 1),
			sevError, "installDir.remember", "configurable directory with an upper-case id",
		},
		"remember with an action of the same id": {
			base + "installDir: {id: INSTALLDIR, remember: true}\n" + appComponent +
				"customActions:\n  - id: RestoreInstallDir\n    setProperty: {name: A, value: b}\n    schedule: [{sequence: execute, after: CostFinalize}]\n",
			sevError, "customActions[0].id", "installDir.remember adds",
		},
		"removeFolders without property": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{on: uninstall}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].property", "required",
		},
		"removeFolders standard folder": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: CommonAppDataFolder}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].property", "standard folder",
		},
		"removeFolders reserved property": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: BuilderRemoveFoldersExec}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].property", "reserved",
		},
		"removeFolders bad on": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATA_DIR, on: upgrade}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].on", "upgrade",
		},
		"removeFolders bad condition": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATA_DIR, condition: 'A ='}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].condition", "",
		},
		"removeFolders condition with a line break": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATA_DIR, condition: \"A\\nOR B\"}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].condition", "line breaks",
		},
		"removeFolders twice": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATA_DIR}, {property: DATA_DIR, on: uninstall}]\n    files:", 1),
			sevError, "components[0].removeFolders[1]", "already removes",
		},
		"removeFolders install dir without remember": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: INSTALLDIR}]\n    files:", 1),
			sevWarning, "components[0].removeFolders[0].property", "installDir.remember",
		},
		"removeFolders below the install dir without remember": {
			strings.Replace(base, "        configurable: true\n", "        configurable: true\n        children: [{id: DATADIR, name: data}]\n", 1) +
				strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATADIR}]\n    files:", 1),
			sevWarning, "components[0].removeFolders[0].property", "chosen at install time for INSTALLDIR",
		},
		"removeFolders configurable folder": {
			strings.Replace(base, "        configurable: true\n", "        configurable: true\n  - id: CommonAppDataFolder\n"+
				"    children: [{id: DATADIR, name: data, configurable: true}]\n", 1) +
				"installDir: {id: INSTALLDIR, remember: true}\n" +
				strings.Replace(appComponent, "    files:", "    removeFolders: [{property: DATADIR}]\n    files:", 1),
			sevWarning, "components[0].removeFolders[0].property", "remember it in the registry",
		},
		"removeFolders install on a package folder": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: INSTALLDIR, on: both}]\n    files:", 1),
			sevError, "components[0].removeFolders[0].on", "a folder of the package",
		},
		"removeFolders unset property": {
			base + strings.Replace(appComponent, "    files:", "    removeFolders: [{property: NVS_HOME}]\n    files:", 1),
			sevWarning, "components[0].removeFolders[0].property", "only the command line",
		},
		// CLOSE-01.
		"close on a non-program": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - source: app.exe\n      - {source: readme.txt, close: true}\n", 1),
			sevError, "components[0].files[1].close", "ends in .exe",
		},
		"close timeout too long": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - {source: app.exe, close: {timeout: 6m}}\n", 1),
			sevError, "components[0].files[0].close.timeout", "between 0s and 5m0s",
		},
		"close timeout below a millisecond": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - {source: app.exe, close: {timeout: 1500us}}\n", 1),
			sevError, "components[0].files[0].close.timeout", "whole number of milliseconds",
		},
		"close on a folder source": {
			base + strings.Replace(appComponent, "      - source: app.exe\n", "      - {source: dist/, close: true}\n", 1),
			sevError, "components[0].files[0].close", "not allowed on a folder source",
		},
		// FILE-03.
		"purge configurable folder": {
			base + strings.Replace(appComponent, "    files:", "    purge: true\n    files:", 1),
			sevError, "components[0].purge", "configurable",
		},
		"purge standard folder": {
			base + strings.Replace(appComponent, "directory: INSTALLDIR", "directory: ProgramFiles64Folder\n    purge: true", 1),
			sevError, "components[0].purge", "standard folder",
		},
		"purge below configurable folder": {
			strings.Replace(base, "        configurable: true\n", "        configurable: true\n        children: [{id: DataDir, name: data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1),
			sevError, "components[0].purge", "INSTALLDIR can be pointed",
		},
		"purge nested standard folder": {
			strings.Replace(base, "directories:\n", "directories:\n  - id: TARGETDIR\n    children: [{id: CommonAppDataFolder, name: ProgramData}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: CommonAppDataFolder\n    createFolder: true\n    purge: true", 1),
			sevError, "components[0].purge", "standard folder",
		},
		"purge below nested standard folder": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: CommonAppDataFolder, name: x, children: [{id: DataDir, name: data}]}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1),
			sevError, "components[0].purge", "CommonAppDataFolder can be pointed",
		},
		"purge percent in name": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DataDir, name: '%TEMP%'}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1),
			sevError, "components[0].purge", "has a %",
		},
		"purge tilde in name": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DataDir, name: 'PROGRA~1'}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1),
			sevError, "components[0].purge", "8.3 alias",
		},
		"short|long source base name": {
			base + strings.Replace(appComponent, "source: app.exe", "source: 'APP.EXE|app.exe'", 1),
			sevError, "components[0].files[0].source", "short names are generated",
		},
		"purge below a standard folder set by setDirectory": {
			base + "components:\n  - id: Data\n    directory: CommonAppDataFolder/Example\n    createFolder: true\n    purge: true\n" +
				"customActions:\n  - id: SetIt\n    setDirectory: {directory: CommonAppDataFolder, value: '[DATAROOT]'}\n" +
				"    schedule: [{sequence: execute, after: CostFinalize}]\n",
			sevError, "components[0].purge", "CommonAppDataFolder can be pointed",
		},
		"short-name package with a long file name": {
			pkg("{platform: x64, longFileNames: false}") + strings.Replace(appComponent, "source: app.exe", "{source: app.exe, name: Application.exe}", 1),
			sevError, "components[0].files[0].name", "valid 8.3 name",
		},
		"short-name package with a long source name": {
			pkg("{platform: x64, longFileNames: false}") + strings.Replace(appComponent, "source: app.exe", "source: Application.exe", 1),
			sevError, "components[0].files[0].source", "valid 8.3 name",
		},
		"short|long file name": {
			base + strings.Replace(appComponent, "source: app.exe", "{source: app.exe, name: 'APP.EXE|Application.exe'}", 1),
			sevError, "components[0].files[0].name", "short names are generated",
		},
		"purge above a permanent component": {
			base + appComponent + "  - id: Data\n    directory: CommonAppDataFolder/Example/Hello\n    createFolder: true\n    purge: true\n" +
				"  - id: Keep\n    directory: CommonAppDataFolder/Example/Hello/keep\n    createFolder: true\n    permanent: true\n",
			sevError, "components[1].purge", "permanent component Keep",
		},
		"purge permanent": {
			base + strings.Replace(appComponent, "    files:", "    permanent: true\n    purge: true\n    files:", 1),
			sevError, "components[0].purge", "permanent",
		},
		"purge below setDirectory": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DataDir, name: data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1) +
				"customActions:\n  - id: MoveIt\n    setDirectory: {directory: INSTALLDIR, value: '[WindowsFolder]'}\n",
			sevError, "components[0].purge", "INSTALLDIR can be pointed",
		},
		"purge public folder id": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DATADIR, name: data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DATADIR\n    purge: true", 1),
			sevError, "components[0].purge", "DATADIR can be pointed",
		},
		"purge below TARGETDIR": {
			strings.Replace(base, "directories:\n", "directories:\n  - id: TARGETDIR\n    children: [{id: DataDir, name: Data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    createFolder: true\n    purge: true", 1),
			sevError, "components[0].purge", "TARGETDIR",
		},
		"purge folder set by a property": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DataDir, name: data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1) +
				"properties:\n  DataDir: 'C:\\'\n",
			sevError, "components[0].purge", "DataDir can be pointed",
		},
		"purge folder set by setProperty": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DataDir, name: data}]\n", 1) +
				strings.Replace(appComponent, "directory: INSTALLDIR", "directory: DataDir\n    purge: true", 1) +
				"customActions:\n  - id: SetIt\n    setProperty: {name: DataDir, value: '[WindowsFolder]'}\n",
			sevError, "components[0].purge", "DataDir can be pointed",
		},
		"purge condition": {
			strings.Replace(base, "        configurable: true\n", "        children: [{id: DATADIR, name: data}]\n", 1) + strings.Replace(appComponent, "    files:", "    purge: {condition: 'DELETE_DATA='}\n    files:", 1),
			sevError, "components[0].purge.condition", "",
		},
		// UPG-05.
		"neverOverwrite with early removal": {
			base + strings.Replace(appComponent, "    files:", "    neverOverwrite: true\n    files:", 1),
			sevWarning, "components[0].neverOverwrite", "UPG-05",
		},
		"neverOverwrite service with early removal": {
			base + strings.Replace(appComponent, "    files:", "    neverOverwrite: true\n    services: [{name: svc}]\n    files:", 1),
			sevWarning, "components[0].neverOverwrite", "own component",
		},
		"preserved service": {
			base + strings.Replace(appComponent, "    files:", "    preserve: true\n    services: [{name: svc}]\n    files:", 1),
			sevError, "components[0].preserve", "own component",
		},
		// Upgrade action properties (ICE61, section 8.10).
		"private upgrade property": {
			strings.Replace(base, "upgrade: {}", "upgrade: {property: oldFound}", 1) + appComponent,
			sevError, "upgrade.property", "upper-case",
		},
		"upgrade property in properties": {
			base + appComponent + "properties: {OLDERVERSIONBEINGUPGRADED: x}\n",
			sevError, "upgrade.property", "ICE61",
		},
		"upgrade minVersion not a version": {
			strings.Replace(base, "upgrade: {}", "upgrade: {minVersion: 1.x}", 1) + appComponent,
			sevError, "upgrade.minVersion", "not a number",
		},
		"upgrade minVersion at product version": {
			strings.Replace(base, "upgrade: {}", "upgrade: {minVersion: 1.0.0.5}", 1) + appComponent,
			sevError, "upgrade.minVersion", "below product.version",
		},
		"upgrade minVersion above product version": {
			strings.Replace(base, "upgrade: {}", "upgrade: {minVersion: 2.0.0}", 1) + appComponent,
			sevError, "upgrade.minVersion", "below product.version",
		},
		"upgrade minVersion fourth field": {
			strings.Replace(base, "upgrade: {}", "upgrade: {minVersion: 0.9.0.1}", 1) + appComponent,
			sevWarning, "upgrade.minVersion", "fourth is ignored",
		},
		"upgrade includeMin without minVersion": {
			strings.Replace(base, "upgrade: {}", "upgrade: {includeMin: false}", 1) + appComponent,
			sevWarning, "upgrade.includeMin", "without minVersion",
		},
		"removeOld window": {
			strings.Replace(base, "upgrade: {}", "upgrade: {removeOld: afterStartServices}", 1) + appComponent,
			sevError, "upgrade.removeOld", "afterInstallValidate",
		},
		// Components.
		"component without feature": {
			base + appComponent + "  - id: Other\n    directory: INSTALLDIR\n    createFolder: true\n",
			sevError, "components[1]", "ICE21",
		},
		"guid none not permanent": {
			base + strings.Replace(appComponent, "    files:", "    guid: none\n    files:", 1),
			sevError, "components[0].guid", "never removed",
		},
		"32-bit folder in x64": {
			strings.Replace(base, "ProgramFiles64Folder", "ProgramFilesFolder", 1) + appComponent,
			sevWarning, "components[0].directory", "ICE80",
		},
		"duplicate generated file key": {
			base + appComponent + "  - id: Two\n    directory: INSTALLDIR\n    files: [{source: other/a-b.exe}, {source: other/a_b.exe}]\n",
			sevError, "components[1].files[1].id", "also used",
		},
		"duplicate file key": {
			base + appComponent + "  - id: Two\n    directory: INSTALLDIR\n    files: [{source: other/app.exe, name: x.exe, id: app.exe}]\n",
			sevError, "components[1].files[0].id", "also used",
		},
		"per-user service": {
			pkg(`{platform: x64, scope: perUser}`) + strings.Replace(appComponent, "    files:", "    services: [{name: svc}]\n    files:", 1),
			sevError, "components[0].services[0]", "ICE105",
		},
		"start condition without start": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, control: {startOnInstall: false, startCondition: GO}}]\n    files:", 1),
			sevError, "components[0].services[0].control.startCondition", "needs startOnInstall",
		},
		"start condition syntax": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, control: {startCondition: 'A AND'}}]\n    files:", 1),
			sevError, "components[0].services[0].control.startCondition", "",
		},
		"start condition on a feature state": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, control: {startCondition: '&Main=3'}}]\n    files:", 1),
			sevError, "components[0].services[0].control.startCondition", "action state of &Main",
		},
		"component condition on a component state": {
			base + strings.Replace(appComponent, "    files:", "    condition: '$App=3 OR ?App=3'\n    files:", 1),
			sevError, "components[0].condition", "action state of $App, but",
		},
		"component condition syntax": {
			base + strings.Replace(appComponent, "    files:", "    condition: 'A AND'\n    files:", 1),
			sevError, "components[0].condition", "",
		},
		"service control without event": {
			base + strings.Replace(appComponent, "    files:", "    serviceControl: [{name: Old}]\n    files:", 1),
			sevError, "components[0].serviceControl[0]", "no event",
		},
		"service control delete and start": {
			base + strings.Replace(appComponent, "    files:", "    serviceControl: [{name: Old, deleteOnInstall: true, startOnInstall: true}]\n    files:", 1),
			sevWarning, "components[0].serviceControl[0]", "same phase",
		},
		"service control long name": {
			base + strings.Replace(appComponent, "    files:", "    serviceControl: [{name: S"+strings.Repeat("s", 255)+", stopOnInstall: true}]\n    files:", 1),
			sevError, "components[0].serviceControl[0].name", "holds 255",
		},
		"service control without name": {
			base + strings.Replace(appComponent, "    files:", "    serviceControl: [{stopOnInstall: true}]\n    files:", 1),
			sevError, "components[0].serviceControl[0].name", "required",
		},
		"anchor on a search action in the admin sequence": {
			base + "properties:\n  RWDIR:\n    secure: true\n    search: [{registry: {root: HKLM, key: 'Software\\RW', name: Dir}}]\n" + appComponent +
				"customActions:\n  - id: SetIt\n    setProperty: {name: RWX, value: '[RWDIR]'}\n    schedule: [{sequence: adminExecute, after: SearchApply_RWDIR}]\n",
			sevError, "customActions[0].schedule[0]", "neither a standard action",
		},
		"anchor on a search action with a generated name": {
			base + "properties:\n  PAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:\n    secure: true\n    search: [{registry: {root: HKLM, key: 'Software\\RW', name: Dir}}]\n" + appComponent +
				"customActions:\n  - id: SetIt\n    setProperty: {name: RWX, value: '[PAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA]'}\n    schedule: [{sequence: execute, after: SearchApply_PAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA}]\n",
			sevError, "customActions[0].schedule[0]", "neither a standard action",
		},
		"service control twice": {
			base + strings.Replace(appComponent, "    files:", "    serviceControl: [{name: Old, stopOnInstall: true}, {name: old, deleteOnInstall: true}]\n    files:", 1),
			sevError, "components[0].serviceControl[1].name", "already controls",
		},
		"service control of a service in another component": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: Svc}]\n    files:", 1) +
				"  - id: Other\n    directory: INSTALLDIR\n    createFolder: true\n    serviceControl: [{name: svc, stopOnUninstall: true}]\n",
			sevError, "components[1].serviceControl[0].name", "services[].control",
		},
		"start condition too long": {
			base + strings.Replace(appComponent, "    files:", "    condition: A"+strings.Repeat("A", 200)+"\n    services: [{name: svc, control: {startCondition: B"+strings.Repeat("B", 100)+"}}]\n    files:", 1),
			sevError, "components[0].services[0].control.startCondition", "at most 255",
		},
		"service control of own service": {
			base + strings.Replace(appComponent, "    files:", "    services: [{name: svc}]\n    serviceControl: [{name: svc, stopOnInstall: true}]\n    files:", 1),
			sevError, "components[0].serviceControl[0].name", "services[].control",
		},
		"per-user service control": {
			pkg(`{platform: x64, scope: perUser}`) + strings.Replace(appComponent, "    files:", "    serviceControl: [{name: Old, stopOnInstall: true}]\n    files:", 1),
			sevError, "components[0].serviceControl[0]", "elevation",
		},
		"dword value": {
			base + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\X, name: N, type: dword, value: abc}]\n    files:", 1),
			sevError, "components[0].registry[0].value", "32-bit integer",
		},
		"multi string marker": {
			base + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\X, name: N, value: 'a[~]b'}]\n    files:", 1),
			sevError, "components[0].registry[0].value", "multiString",
		},
		"remove registry name dash": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X, name: '-'}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].name", "leave name out",
		},
		"remove registry twice": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X}, {root: HKLM, key: software\\x}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry twice with ids": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{id: A, root: HKLM, key: Software\\X}, {root: HKLM, key: Software\\X}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry twice id last": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X}, {id: B, root: HKLM, key: Software\\X}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry bad id": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{id: 'a b', root: HKLM, key: Software\\X}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].id", "not an identifier",
		},
		"remove registry bad on": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X, on: never}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].on", "not one of",
		},
		"remove registry value on uninstall": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X, name: V, on: uninstall}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].name", "whole keys only",
		},
		"remove registry uninstall twice": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X, on: uninstall}, {root: HKLM, key: software\\x, on: uninstall}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry install and both": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X}, {root: HKLM, key: Software\\X, on: both}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry uninstall and both": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X, on: uninstall}, {root: HKLM, key: Software\\X, on: both}]\n    files:", 1),
			sevError, "components[0].removeRegistry[1]", "already removed",
		},
		"remove registry uninstall over a value named dash": {
			base + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\X, name: '-', value: v}]\n"+
				"    removeRegistry: [{root: HKLM, key: software\\x, on: uninstall}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].id", "same generated id",
		},
		"remove registry uninstall id of a registry entry": {
			base + strings.Replace(appComponent, "    files:", "    registry: [{id: A, root: HKLM, key: Software\\X, name: N, value: v}]\n"+
				"    removeRegistry: [{id: A, root: HKLM, key: Software\\Y, on: both}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].id", "registry id A is defined twice",
		},
		"remove registry id twice": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{id: A, root: HKLM, key: Software\\X}]\n    files:", 1) +
				"  - id: Other\n    directory: INSTALLDIR\n    removeRegistry: [{id: A, root: HKLM, key: Software\\Y}]\n",
			sevError, "components[1].removeRegistry[0].id", "defined twice",
		},
		"dual scope service": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + strings.Replace(appComponent, "    files:", "    services: [{name: svc}]\n    files:", 1),
			sevError, "components[0].services[0]", "a perMachineOrUser package cannot install services (ICE105)",
		},
		"dual scope HKLM": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\X, name: N, value: v}]\n    files:", 1),
			sevError, "components[0].registry[0].root", "ICE105",
		},
		"dual scope system folder": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + "  - id: Data\n    directory: CommonAppDataFolder/App\n    createFolder: true\n",
			sevError, "components[1].directory", "cannot install under CommonAppDataFolder (ICE105)",
		},
		"dual scope installer 4.5": {
			pkg(`{platform: x64, scope: perUserOrMachine, installerVersion: "4.5"}`) + appComponent,
			sevError, "package.installerVersion", "MSIINSTALLPERUSER",
		},
		"dual scope machine variable": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + strings.Replace(appComponent, "    files:", "    environment: [{name: FOO, value: a, scope: machine}]\n    files:", 1),
			sevError, "components[0].environment[0].scope", "scope auto, or a component with scope perMachine",
		},
		"dual scope HKLM hint": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + strings.Replace(appComponent, "    files:", "    registry: [{root: HKLM, key: Software\\X, name: N, value: v}]\n    files:", 1),
			sevError, "components[0].registry[0].root", "HKMU, or a component with scope perMachine",
		},
		"dual scope perUser component HKLM": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent +
				"  - {id: Reg, directory: INSTALLDIR, scope: perUser, registry: [{root: HKLM, key: Software\\X, name: N, value: v}]}\n",
			sevError, "components[1].registry[0].root", "ICE105",
		},
		"dual scope perMachine component service": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) +
				"components:\n  - {id: App, directory: INSTALLDIR, scope: perMachine, files: [{source: app.exe}], services: [{name: Svc}]}\n",
			sevError, "components[0].services[0]", "cannot install services (ICE105)",
		},
		"component scope outside a dual package": {
			base + appComponent + "  - {id: Reg, directory: INSTALLDIR, scope: perMachine, registry: [{root: HKLM, key: Software\\X, name: N, value: v}]}\n",
			sevError, "components[1].scope", "only a perUserOrMachine or perMachineOrUser package",
		},
		"folder component scope outside a dual package": {
			base + "components:\n  - {id: App, directory: INSTALLDIR, scope: perMachine, files: [{source: dist/}]}\n",
			sevError, "components[0].scope", "only a perUserOrMachine or perMachineOrUser package",
		},
		"component scope condition too long": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent +
				"  - {id: Reg, directory: INSTALLDIR, scope: perMachine, condition: " + strings.Repeat("A", 240) +
				", registry: [{root: HKLM, key: Software\\X, name: N, value: v}]}\n",
			sevError, "components[1].condition", "Component.Condition holds 255",
		},
		"context variable condition too long": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent +
				"  - {id: Env, directory: INSTALLDIR, condition: " + strings.Repeat("A", 240) + ", environment: [{name: FOO, value: a}]}\n",
			sevError, "components[1].condition", "Component.Condition holds 255",
		},
		"perMachine component in a per-user-first package": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent +
				"  - {id: Reg, directory: INSTALLDIR, scope: perMachine, registry: [{root: HKLM, key: Software\\X, name: N, value: v}]}\n",
			sevWarning, "components[1].scope", "ICE105 warns",
		},
		"perMachine component removal in a per-user-first package": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent +
				"  - {id: Reg, directory: INSTALLDIR, scope: perMachine, removeRegistry: [{root: HKLM, key: Software\\Old}]}\n",
			sevWarning, "components[1].scope", "ICE105 warns",
		},
		"component scope dual": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent +
				"  - {id: Reg, directory: INSTALLDIR, scope: perUserOrMachine, registry: [{root: HKMU, key: Software\\X, name: N, value: v}]}\n",
			sevError, "components[1].scope", "not one of",
		},
		"dual scope purge": {
			pkg(`{platform: x64, scope: perMachineOrUser}`) + appComponent + "  - id: Data\n    directory: ProgramFiles64Folder/AcmeData\n    createFolder: true\n    purge: true\n",
			sevError, "components[1].purge", "runs as the user",
		},
		"dual scope public start menu folder": {
			strings.Replace(pkg(`{platform: x64, scope: perUserOrMachine}`), "directories:\n",
				"directories:\n  - id: ProgramMenuFolder\n    children: [{id: MENUDIR, name: Acme, configurable: true}]\n", 1) +
				appComponent + "  - id: Menu\n    directory: MENUDIR\n    createFolder: true\n",
			sevError, "directories[0].children[0].id", "public directory under ProgramMenuFolder",
		},
		"dual scope purge start menu": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + "  - id: Menu\n    directory: ProgramMenuFolder/Acme/Tools\n    createFolder: true\n    purge: true\n",
			sevError, "components[1].purge", "runs as the user",
		},
		"dual scope install dir on desktop": {
			`schema: msi-builder/v1
product: {name: App, manufacturer: Example, version: 1.0.0, upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301}
package: {platform: x64, scope: perUserOrMachine}
installDir: {parent: DesktopFolder, name: Acme}
components:
  - files: [{source: app.exe}]
`,
			sevError, "installDir.parent", "must be under Program Files",
		},
		"dual scope install dir defined on desktop": {
			strings.Replace(pkg(`{platform: x64, scope: perUserOrMachine}`), "id: ProgramFiles64Folder", "id: DesktopFolder", 1) + appComponent,
			sevError, "directories[0].children[0].id", "must be under Program Files",
		},
		"dual scope BuilderInstallScope": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + "properties:\n  BuilderInstallScope: {value: perMachine}\n",
			sevError, "properties.BuilderInstallScope", "set by the builder",
		},
		"dual scope action prefix": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + `customActions:
  - id: SetInstallScopeX
    setProperty: {name: X, value: "1"}
    schedule: [{sequence: execute, after: CostFinalize}]
`,
			sevError, "customActions[0].id", "scope page",
		},
		"MSIINSTALLPERUSER without a dual scope": {
			pkg(`{platform: x64, scope: perUser}`) + appComponent + "properties:\n  MSIINSTALLPERUSER: {value: \"1\"}\n",
			config.SeverityWarning, "properties.MSIINSTALLPERUSER", "no effect unless ALLUSERS is 2",
		},
		"dual scope MSIINSTALLPERUSER": {
			pkg(`{platform: x64, scope: perUserOrMachine}`) + appComponent + "properties:\n  MSIINSTALLPERUSER: {value: \"1\"}\n",
			sevError, "properties.MSIINSTALLPERUSER", "set by the builder",
		},
		"per-user HKCR": {
			pkg(`{platform: x64, scope: perUser}`) + strings.Replace(appComponent, "    files:", "    registry: [{root: HKCR, key: .app, value: v}]\n    files:", 1),
			sevError, "components[0].registry[0].root", "a per-user package cannot change HKCR",
		},
		"remove registry per user": {
			pkg(`{platform: x64, scope: perUser}`) + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].root", "ICE105",
		},
		"remove registry key slash": {
			base + strings.Replace(appComponent, "    files:", "    removeRegistry: [{root: HKLM, key: Software\\X\\}]\n    files:", 1),
			sevError, "components[0].removeRegistry[0].key", "backslash",
		},
		"search private property": {
			base + appComponent + "properties: {lower: {search: {registry: {root: HKLM, key: Software\\X}}}}\n",
			sevError, "properties.lower.search", "public",
		},
		"search not secure": {
			base + appComponent + "properties: {P: {secure: false, search: {registry: {root: HKLM, key: Software\\X}}}}\n",
			sevError, "properties.P.secure", "must be secure",
		},
		"search without kind": {
			base + appComponent + "properties: {P: {search: {}}}\n",
			sevError, "properties.P.search", "exactly one of",
		},
		"search two kinds": {
			base + appComponent + "properties: {P: {search: {registry: {root: HKLM, key: X}, component: {guid: 6A0D3C2E-5B1F-4E7A-9C3D-2F8E1B4A7C60}}}}\n",
			sevError, "properties.P.search", "exactly one of",
		},
		"search empty list": {
			base + appComponent + "properties: {P: {search: []}}\n",
			sevError, "properties.P.search", "at least one",
		},
		"search list found taken": {
			base + appComponent + "properties: {P: {search: [{directory: {path: 'C:\\a'}}, {directory: {path: 'C:\\b'}}]}, P_FOUND2: x}\n",
			sevError, "properties.P.search[1]", "P_FOUND2",
		},
		"search list item": {
			base + appComponent + "properties: {P: {search: [{directory: {path: 'C:\\a'}}, {directory: {}}]}}\n",
			sevError, "properties.P.search[1].directory.path", "required",
		},
		"search directory relative": {
			base + appComponent + "properties: {P: {search: {directory: {path: sub}}}}\n",
			sevError, "properties.P.search.directory.path", "not a full path",
		},
		"search directory under full path": {
			base + appComponent + "properties: {P: {search: {directory: {path: 'C:\\a', under: {directory: {path: 'C:\\b'}}}}}}\n",
			sevError, "properties.P.search.directory.path", "relative",
		},
		"search under file": {
			base + appComponent + "properties: {P: {search: {directory: {under: {file: {name: a.exe, path: 'C:\\b'}}}}}}\n",
			sevError, "properties.P.search.directory.under.file", "must find a folder",
		},
		"search under registry value": {
			base + appComponent + "properties: {P: {search: {directory: {path: bin, under: {registry: {root: HKLM, key: X, type: value}}}}}}\n",
			sevError, "properties.P.search.directory.under.registry.type", "must find a folder",
		},
		"search under nested too deep": {
			base + appComponent + "properties: {P: {search: {directory: {under: {directory: {under: {directory: {under: {directory: {under: " +
				"{directory: {under: {directory: {under: {directory: {under: {directory: {under: {directory: {path: 'C:\\a'}}}}}}}}}}}}}}}}}}}}\n",
			sevError, "properties.P.search" + strings.Repeat(".directory.under", 8), "searches in one chain",
		},
		"search short long path": {
			base + appComponent + "properties: {P: {search: {directory: {path: '[ProgramFiles64Folder]a~1|a b'}}}}\n",
			sevError, "properties.P.search.directory.path", "short|long",
		},
		"search install dir in path": {
			base + appComponent + "properties: {P: {search: {directory: {path: '[INSTALLDIR]sub'}}}}\n",
			sevWarning, "properties.P.search.directory.path", "CostFinalize",
		},
		"search file name": {
			base + appComponent + "properties: {P: {search: {file: {name: 'bin\\a.exe', path: 'C:\\a'}}}}\n",
			sevError, "properties.P.search.file.name", "not a file name",
		},
		"search file without name": {
			base + appComponent + "properties: {P: {search: {file: {path: 'C:\\a'}}}}\n",
			sevError, "properties.P.search.file.name", "required",
		},
		"search file depth": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', depth: -1}}}}\n",
			sevError, "properties.P.search.file.depth", "0 to",
		},
		"search file version": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minVersion: 1.x}}}}\n",
			sevError, "properties.P.search.file.minVersion", "not a number",
		},
		"search file version fields": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', maxVersion: 1.2.3.4.5}}}}\n",
			sevError, "properties.P.search.file.maxVersion", "four fields",
		},
		"search file size": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minSize: 10, maxSize: 5}}}}\n",
			sevError, "properties.P.search.file.maxSize", "less than minSize",
		},
		"search file size range": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', maxSize: 3000000000}}}}\n",
			sevError, "properties.P.search.file.maxSize", "0 to",
		},
		"search file date": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minDate: 2050-01-01}}}}\n",
			sevError, "properties.P.search.file.minDate", "2043-12-31",
		},
		"search file date format": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minDate: 01.02.2020}}}}\n",
			sevError, "properties.P.search.file.minDate", "YYYY-MM-DD",
		},
		"search file date order": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minDate: 2020-02-01, maxDate: 2020-01-01}}}}\n",
			sevError, "properties.P.search.file.maxDate", "before minDate",
		},
		"search file language": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', languages: [70000]}}}}\n",
			sevError, "properties.P.search.file.languages[0]", "language ID",
		},
		"search directory under without path": {
			base + appComponent + "properties: {P: {search: {directory: {under: {registry: {root: HKLM, key: X, name: Dir}}}}}}\n",
			sevError, "properties.P.search.directory.path", "required under another search",
		},
		"search file version order": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minVersion: 2.0, maxVersion: 1.10}}}}\n",
			sevError, "properties.P.search.file.maxVersion", "less than minVersion",
		},
		"search directory depth without path": {
			base + appComponent + "properties: {P: {search: {directory: {under: {directory: {path: 'C:\\a'}}, depth: 2}}}}\n",
			sevError, "properties.P.search.directory.depth", "needs a path",
		},
		"search system file version": {
			base + appComponent + "properties: {P: {search: {file: {name: kernel32.dll, path: '[System64Folder]', minVersion: 10.0}}}}\n",
			sevWarning, "properties.P.search.file", "VersionNT",
		},
		"search languages without min version": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', languages: [1033]}}}}\n",
			sevWarning, "properties.P.search.file.languages", "no effect",
		},
		"search searched property in path": {
			base + appComponent + "properties: {DATA: {search: {directory: {path: 'C:\\data'}}}, P: {search: {directory: {path: '[DATA]logs'}}}}\n",
			sevError, "properties.P.search.directory.path", "under",
		},
		"search unknown property in path": {
			base + appComponent + "properties: {P: {search: {directory: {path: '[SOMEWHERE]logs'}}}}\n",
			sevWarning, "properties.P.search.directory.path", "every fixed drive",
		},
		"search path length": {
			base + appComponent + "properties: {P: {search: {directory: {path: 'C:\\" + strings.Repeat("a", 260) + "'}}}}\n",
			sevError, "properties.P.search.directory.path", "255",
		},
		"search name length": {
			base + appComponent + "properties: {P: {search: {file: {name: " + strings.Repeat("a", 260) + ", path: 'C:\\a'}}}}\n",
			sevError, "properties.P.search.file.name", "255",
		},
		"search lowered min version length": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', minVersion: '1000.0'}}}}\n",
			sevError, "properties.P.search.file.minVersion", "999.65535.65535.65535",
		},
		"search max version length": {
			base + appComponent + "properties: {P: {search: {file: {name: a.exe, path: 'C:\\a', maxVersion: 65535.65535.65535.65535}}}}\n",
			sevError, "properties.P.search.file.maxVersion", "20",
		},
		"search system file under": {
			base + appComponent + "properties: {P: {search: {file: {name: a.dll, under: {directory: {path: '[SystemFolder]'}}, maxVersion: 1.0}}}}\n",
			sevWarning, "properties.P.search.file", "VersionNT",
		},
		"search component guid": {
			base + appComponent + "properties: {P: {search: {component: {guid: nope}}}}\n",
			sevError, "properties.P.search.component.guid", "not a GUID",
		},
		"search hkmu": {
			base + appComponent + "properties: {P: {search: {registry: {root: HKMU, key: Software\\X}}}}\n",
			sevError, "properties.P.search.registry.root", "not one of",
		},
		"search type": {
			base + appComponent + "properties: {P: {search: {registry: {root: HKLM, key: Software\\X, type: file}}}}\n",
			sevError, "properties.P.search.registry.type", "not one of",
		},
		"search 64 in x86": {
			pkg(`{platform: x86}`) + appComponent + "properties: {P: {search: {registry: {root: HKLM, key: Software\\X, bitness: 64}}}}\n",
			sevError, "properties.P.search.registry.bitness", "32-bit package",
		},
		"search found taken": {
			base + appComponent + "properties: {P: {search: {registry: {root: HKLM, key: Software\\X}}}, P_FOUND: x}\n",
			sevError, "properties.P.search", "P_FOUND",
		},
		// Properties.
		"builder property": {
			base + appComponent + "properties: {ALLUSERS: \"1\"}\n",
			sevError, "properties.ALLUSERS", "set by the builder",
		},
		"secure private": {
			base + appComponent + "properties: {lower: {value: x, secure: true}}\n",
			sevError, "properties.lower.secure", "public",
		},
		// UI.
		"dialog without wizard": {
			base + appComponent + "ui: {dialogs: [{id: D, title: T, after: X, fields: [{property: P}]}]}\n",
			sevError, "ui.dialogs", "need a wizard",
		},
		"feature tree without wizard": {
			base + appComponent + "ui: {chooseFeatures: true}\n",
			sevError, "ui.chooseFeatures", "needs a wizard",
		},
		"license without wizard": {
			base + appComponent + "ui: {license: LICENSE.rtf}\n",
			sevError, "ui.license", "needs a wizard",
		},
		"theme without wizard": {
			base + appComponent + "ui: {theme: {banner: banner.bmp}}\n",
			sevError, "ui.theme", "needs a wizard",
		},
		"bad theme colour": {
			base + appComponent + "ui: {enabled: true, theme: {colors: {accent: \"#12345\"}}}\n",
			sevError, "ui.theme.colors.accent", "#RRGGBB",
		},
	} {
		ds := validate(t, tc.doc)
		if !find(ds, tc.sev, tc.path, tc.msg) {
			t.Errorf("%s: want %s at %s containing %q, got:\n%v", name, tc.sev, tc.path, tc.msg, ds)
		}
	}
}

// TestVersionedFiles checks the "more than one versioned file" warning: auto
// counts as versioned only on a PE file, so data files never trigger it.
func TestVersionedFiles(t *testing.T) {
	t.Parallel()

	const warning = "more than one versioned file"

	for name, tc := range map[string]struct {
		files string
		want  bool
	}{
		"two data files":        {"[{source: a.txt}, {source: b.txt}]", false},
		"exe and data file":     {"[{source: app.exe}, {source: a.txt}]", false},
		"two executables":       {"[{source: app.exe}, {source: helper.dll}]", true},
		"explicit versions":     {"[{source: a.txt, version: 1.0.0.0}, {source: b.txt, version: 1.0.0.0}]", true},
		"explicit none on exes": {"[{source: app.exe}, {source: helper.dll, version: none, allowUnversionedExecutable: true}]", false},
	} {
		doc := base + strings.Replace(appComponent, "    files:\n      - source: app.exe\n", "    files: "+tc.files+"\n", 1)

		ds := validate(t, doc)
		if got := find(ds, config.SeverityWarning, "components[0].files", warning); got != tc.want {
			t.Errorf("%s: warning present = %v, want %v, got:\n%v", name, got, tc.want, ds)
		}
	}
}

// TestGeneratedFileKey checks the file key of a file whose name is no
// identifier.
func TestGeneratedFileKey(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"app.exe":               "app.exe",
		"fleetspeak-client.exe": "fleetspeak_client.exe",
		"7zip.dll":              "_7zip.dll",
		"my app (x64).exe":      "my_app__x64_.exe",
		".config":               "_.config",
	} {
		if got := config.FileKey(&config.File{Source: "bin/" + name}); got != want {
			t.Errorf("FileKey(%q) = %q, want %q", name, got, want)
		}
	}

	if got := config.FileKey(&config.File{Source: "a-b.exe", ID: "Main"}); got != "Main" {
		t.Errorf("FileKey with id = %q, want Main", got)
	}

	// Long names are cut to at most 60 characters with a hash suffix,
	// whether or not they were identifiers.
	for _, name := range []string{strings.Repeat("a-", 35) + ".exe", strings.Repeat("a", 80) + ".exe"} {
		if got := config.FileKey(&config.File{Source: name}); len(got) > 60 || !config.IsIdentifier(got) {
			t.Errorf("FileKey(%q) = %q", name, got)
		}
	}

	doc := base + strings.Replace(appComponent, "app.exe", "my-app.exe", 1)
	if ds := validate(t, doc); len(ds) != 0 {
		t.Errorf("unexpected diagnostics for my-app.exe: %v", ds)
	}

	// launch.file and a companion version may name the file by its long name.
	doc = base + strings.Replace(appComponent, "      - source: app.exe\n",
		"      - source: my-app.exe\n      - { source: data.bin, version: my-app.exe }\n", 1) +
		"ui: {enabled: true, launch: {file: my-app.exe}}\n"
	if err := validate(t, doc).Err(); err != nil {
		t.Errorf("references by long name: %v", err)
	}

	// A file with an id is named by its id only.
	doc = base + strings.Replace(appComponent, "      - source: app.exe\n",
		"      - { source: my-app.exe, id: Main }\n      - { source: data.bin, version: my-app.exe }\n", 1) +
		"ui: {enabled: true, launch: {file: my-app.exe}}\n"
	ds := validate(t, doc)

	if !find(ds, config.SeverityError, "components[0].files[1].version", "not auto") || !find(ds, config.SeverityError, "ui.launch.file", "not defined") {
		t.Errorf("references by long name to a file with an id: %v", ds)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	if g, err := config.NormalizeGUID("3f2504e0-4f89-41d3-9a0c-0305e82c3301"); err != nil || g != "{3F2504E0-4F89-41D3-9A0C-0305E82C3301}" {
		t.Errorf("NormalizeGUID = %q, %v", g, err)
	}

	for _, bad := range []string{"{3f2504e0-4f89-41d3-9a0c-0305e82c3301", "3f2504e04f8941d39a0c0305e82c3301", "*"} {
		if _, err := config.NormalizeGUID(bad); err == nil {
			t.Errorf("NormalizeGUID(%q) accepted", bad)
		}
	}

	for _, bad := range []string{"256.0.0", "1.256.0", "1.0.65536", "1", "1.0.0.0.0", "1..0", "1.-1.0", "1.-0.0", "1.+1.0", "v1.0.0", "1.0 .0", "a.b.c"} {
		if _, err := config.ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}

	for db, want := range map[int]int{1252: 1252, 932: 932, 65001: 1252, 0: 1252} {
		if got := config.SummaryCodepage(db); got != want {
			t.Errorf("SummaryCodepage(%d) = %d, want %d", db, got, want)
		}
	}
}

func TestPageCount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		version string
		want    int
	}{
		{"5.0", 500},
		{"4.5", 405},
		{"2.0", 200},
		{"05.00", 500},
		// The schema allows a number, and an unquoted 5 reaches the field as "5".
		{"5", 500},
	} {
		if got, err := config.PageCount(tc.version); err != nil || got != tc.want {
			t.Errorf("PageCount(%q) = %d, %v, want %d", tc.version, got, err, tc.want)
		}
	}

	for _, bad := range []string{
		"", ".", "5.", ".5", "5.0.0", "5.100", "a.b", "v5.0", " 5.0", "5.0 ",
		// Digits only, as in ParseVersion: strconv.Atoi also takes a sign.
		"+5.0", "-5.0", "5.-0", "5.+0", "+5", "-0",
		// The major is at most 99, so major × 100 cannot wrap around to 500.
		"100.0", "100", "4611686018427387909.0",
	} {
		if got, err := config.PageCount(bad); err == nil {
			t.Errorf("PageCount(%q) = %d, want an error", bad, got)
		}
	}

	for version, want := range map[string]string{
		"+5.0":                   "digits only",
		"5.":                     "digits only",
		"5.100":                  "minor above 99",
		"5.99999999999999999999": "minor above 99",
		"100.0":                  "highest Windows Installer version",
		"99999999999999999999":   "highest Windows Installer version",
		"501":                    "highest Windows Installer version",
		"600":                    "highest Windows Installer version",
		"500":                    "not the Page Count: write 5.0",
		"450":                    "write 4.50",
	} {
		if _, err := config.PageCount(version); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PageCount(%q) error = %v, want it to mention %q", version, err, want)
		}
	}
}

// TestRecoveryDelayOnNone covers a config built in Go, where a delay can be
// set on a step that is not a restart.
func TestRecoveryDelayOnNone(t *testing.T) {
	t.Parallel()

	doc := base + strings.Replace(appComponent, "    files:", "    services: [{name: svc, recovery: {actions: [none]}}]\n    files:", 1)

	cfg, err := config.Parse([]byte(doc), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	delay := config.Duration(time.Minute)
	cfg.Components[0].Services[0].Recovery.Actions[0].Restart = &delay

	field := "components[0].services[0].recovery.actions[0].restart"
	if ds := config.Validate(cfg); !find(ds, config.SeverityError, field, "only allowed on a restart") {
		t.Errorf("diagnostics %v, want an error at %s", ds, field)
	}
}

// The default cabinet name comes from the product name, which may not fit a
// stream name (FuzzBuild finding).
func TestDefaultCabinet(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"App": "App.cab",
		// ICE03 wants an identifier.
		"My App":                "MyApp.cab",
		"Acme-Tool":             "AcmeTool.cab",
		"Notepad++":             "Notepad.cab",
		"7Zip":                  config.DefaultCabinet,
		"   ":                   config.DefaultCabinet,
		"Grüße":                 config.DefaultCabinet,
		"Привет":                config.DefaultCabinet,
		"A#B":                   "AB.cab",
		strings.Repeat("A", 59): config.DefaultCabinet,
	} {
		doc := strings.Replace(base, "name: App\n  manufacturer", "name: '"+name+"'\n  manufacturer", 1)

		cfg, err := config.Parse([]byte(doc+appComponent), config.Options{})
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Package.Cabinet != want {
			t.Errorf("product %q: cabinet %q, want %q", name, cfg.Package.Cabinet, want)
		}

		if ds := config.Validate(cfg); find(ds, config.SeverityError, "package.cabinet", "") {
			t.Errorf("product %q: the default cabinet is invalid: %v", name, ds)
		}
	}
}

func TestCabinetName(t *testing.T) {
	t.Parallel()

	for cabinet, valid := range map[string]bool{
		"product.cab":                    true,
		strings.Repeat("a", 58) + ".cab": true,
		strings.Repeat("a", 59) + ".cab": false,
		// Every character outside 0-9A-Za-z._ takes a unit of its own.
		strings.Repeat("-", 31): true,
		strings.Repeat("-", 32): false,
		"daten-ü.cab":           false,
		"a/b.cab":               false,
		// Not a forbidden file-name character, but CFB rejects it.
		"a!b.cab": false,
		// A control character could name a stream msi-builder writes itself.
		"\x05SummaryInformation": false,
		"a\tb.cab":               false,
		// Outside the BMP: two units each.
		strings.Repeat("\U0001F600", 15): true,
		strings.Repeat("\U0001F600", 16): false,
	} {
		ds := validate(t, pkg(`{platform: x64, cabinet: `+strconv.Quote(cabinet)+`}`)+appComponent)
		if got := !find(ds, config.SeverityError, "package.cabinet", ""); got != valid {
			t.Errorf("cabinet %q: valid = %v, want %v (%v)", cabinet, got, valid, ds)
		}
	}
}

func TestCabinetIdentifierWarns(t *testing.T) {
	t.Parallel()

	for cabinet, warns := range map[string]bool{"my app.cab": true, "my-app.cab": true, "my_app.cab": false} {
		ds := validate(t, pkg(`{platform: x64, cabinet: "`+cabinet+`"}`)+appComponent)
		if got := find(ds, config.SeverityWarning, "package.cabinet", "ICE03"); got != warns {
			t.Errorf("cabinet %q: ICE03 warning = %v, want %v (%v)", cabinet, got, warns, ds)
		}
	}
}

func TestPreserve(t *testing.T) {
	t.Parallel()

	doc := base + strings.Replace(appComponent, "    files:", "    preserve: true\n    files:", 1)

	cfg, err := config.Parse([]byte(doc), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if ds := config.Validate(cfg); len(ds) != 0 {
		t.Errorf("unexpected diagnostics: %v", ds)
	}

	if c := cfg.Components[0]; c.Permanent || !c.NeverOverwrite {
		t.Errorf("preserve: permanent %v, neverOverwrite %v, want only neverOverwrite", c.Permanent, c.NeverOverwrite)
	}

	if got := cfg.Upgrade.RemoveOld; got != config.PreserveRemoveOld {
		t.Errorf("preserve: removeOld %q, want %q", got, config.PreserveRemoveOld)
	}

	early := strings.Replace(doc, "upgrade: {}", "upgrade: {removeOld: afterInstallInitialize}", 1)
	if ds := validate(t, early); !find(ds, config.SeverityError, "components[0].preserve", "UPG-05") {
		t.Errorf("preserve with an early removeOld: diagnostics %v, want an error", ds)
	}

	// UPG-05 neverperm-retrofit-early: permanent does not save an edit made
	// in a version without the flags.
	perm := strings.Replace(early, "    preserve: true\n", "    preserve: true\n    permanent: true\n", 1)
	if ds := validate(t, perm); !find(ds, config.SeverityError, "components[0].preserve", "UPG-05") {
		t.Errorf("preserve and permanent with an early removeOld: diagnostics %v, want an error", ds)
	}

	finalize := strings.Replace(doc, "upgrade: {}", "upgrade: {removeOld: afterInstallFinalize}", 1)
	if ds := validate(t, finalize); !find(ds, config.SeverityWarning, "components[0].preserve", "not") {
		t.Errorf("preserve with removeOld afterInstallFinalize: diagnostics %v, want a warning", ds)
	}

	off := strings.Replace(early, "upgrade: {removeOld: afterInstallInitialize}", "upgrade: false", 1)
	if ds := validate(t, off); len(ds) != 0 {
		t.Errorf("preserve without upgrades: unexpected diagnostics: %v", ds)
	}

	late := strings.Replace(base, "upgrade: {}", "upgrade: {removeOld: afterInstallExecute}", 1) +
		strings.Replace(appComponent, "    files:", "    neverOverwrite: true\n    files:", 1)
	if ds := validate(t, late); len(ds) != 0 {
		t.Errorf("neverOverwrite with a late removal: unexpected diagnostics: %v", ds)
	}
}

// TestPreserveInFeature checks that a preserved component defined inside a
// feature also moves the removal of the old product late.
func TestPreserveInFeature(t *testing.T) {
	t.Parallel()

	cfg := parseValid(t, short+`features:
  - id: Main
    components:
      - files: [{ source: app.exe }]
      - id: Config
        preserve: true
        files: [{ source: config.yaml }]
`)

	if got := cfg.Upgrade.RemoveOld; got != config.PreserveRemoveOld {
		t.Errorf("removeOld %q, want %q", got, config.PreserveRemoveOld)
	}

	for _, c := range cfg.Components {
		if c.ID == "Config" && !c.NeverOverwrite {
			t.Error("preserved component without neverOverwrite")
		}
	}
}

// A zero restartDelay is a delay of its own, not the default.
func TestZeroRestartDelay(t *testing.T) {
	t.Parallel()

	for doc, want := range map[string]time.Duration{
		"recovery: {restartDelay: 0s, actions: [restart]}": 0,
		"recovery: {actions: [restart]}":                   config.DefaultRestartDelay,
	} {
		cfg, err := config.Parse([]byte(base+strings.Replace(appComponent, "    files:", "    services: [{name: svc, "+doc+"}]\n    files:", 1)), config.Options{})
		if err != nil {
			t.Fatal(err)
		}

		if got := time.Duration(*cfg.Components[0].Services[0].Recovery.RestartDelay); got != want {
			t.Errorf("%s: restartDelay %v, want %v", doc, got, want)
		}
	}
}

// A null removeFiles entry is an error, not a panic.
func TestRemoveFilesEmptyEntry(t *testing.T) {
	t.Parallel()

	doc := base + strings.Replace(appComponent, "    files:", "    removeFiles: [~]\n    files:", 1)
	if _, err := config.Parse([]byte(doc), config.Options{}); err == nil || !strings.Contains(err.Error(), "components[0].removeFiles[0]: empty entry") {
		t.Errorf("parse: %v", err)
	}
}

func TestSearchMinVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in    string
		langs []int
		want  string
	}{
		{"1.2.3.4", nil, "1.2.3.3"},
		{"1.2.3.0", nil, "1.2.2.65535"},
		{"1.2", nil, "1.1.65535.65535"},
		{"2", nil, "1.65535.65535.65535"},
		{"0.0.0.1", nil, "0.0.0.0"},
		{"0.0", nil, "0.0.0.0"},
		{"1.2.3.4", []int{1033}, "1.2.3.4"},
	} {
		if got := config.SearchMinVersion(tc.in, tc.langs); got != tc.want {
			t.Errorf("SearchMinVersion(%q, %v) = %q, want %q", tc.in, tc.langs, got, tc.want)
		}
	}
}

// TestInstallDirBesideDataFolder does not warn about a default install dir
// next to an upper-case data folder, which is no install dir.
func TestInstallDirBesideDataFolder(t *testing.T) {
	t.Parallel()

	doc := strings.Replace(base, "  - id: ProgramFiles64Folder\n    children:\n      - id: INSTALLDIR\n        name: App\n        configurable: true\n",
		"  - id: CommonAppDataFolder\n    children:\n      - id: DATADIR\n        name: App\n", 1) +
		"components:\n  - files: [{ source: app.exe }]\n  - { directory: DATADIR, createFolder: true }\n"
	if !strings.Contains(doc, "DATADIR") {
		t.Fatal("base changed; update the test")
	}

	for _, d := range validate(t, doc) {
		if strings.Contains(d.Message, "next to") {
			t.Errorf("unexpected warning: %s: %s", d.Path, d.Message)
		}
	}
}

func TestSearchDate(t *testing.T) {
	t.Parallel()

	// (year - 1980) × 512 + month × 32 + day in the high word.
	if got, err := config.SearchDate("2020-01-02", false); err != nil || got != (40*512+32+2)<<16 {
		t.Errorf("SearchDate(2020-01-02) = %d, %v", got, err)
	}

	if got, err := config.SearchDate("2020-01-02", true); err != nil || got != (40*512+32+2)<<16|0xBF7D {
		t.Errorf("SearchDate(2020-01-02, end of day) = %#x, %v", got, err)
	}

	for _, bad := range []string{"1979-12-31", "2044-01-01", "2020-1-2", "2020-02-30"} {
		if _, err := config.SearchDate(bad, true); err == nil {
			t.Errorf("SearchDate(%q) accepted", bad)
		}
	}
}

func TestIsFullSearchPath(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		`C:\a`: true, `c:\`: true, `\\server\share`: true, `[ProgramFilesFolder]x`: true,
		`a\b`: false, `\a`: false, `C:a`: false, `1:\a`: false, ``: false,
	} {
		if got := config.IsFullSearchPath(path); got != want {
			t.Errorf("IsFullSearchPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSearchDecodeErrors(t *testing.T) {
	t.Parallel()

	const head = "schema: msi-builder/v1\nproperties:\n  P:\n    search: "

	for doc, want := range map[string]string{
		"{directory: {path: 'C:\\a', bogus: 1}}":     "field bogus not found",
		"{reg: {root: HKLM, key: X}}":                "field reg not found",
		"{file: {name: [a, b], path: 'C:\\a'}}":      "cannot unmarshal !!seq into string",
		"[{directory: {path: 'C:\\a'}}, {bogus: 1}]": "field bogus not found",
		"[{file: {name: {x: 1}, path: 'C:\\a'}}]":    "cannot unmarshal !!map into string",
		"x": "cannot unmarshal",
	} {
		_, err := config.Parse([]byte(head+doc+"\n"), config.Options{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("search %s: error %v, want one containing %q", doc, err, want)
		}
	}
}

// TestInstallDirBesideTargetDirTree warns about an upper-case directory
// below Program Files in a tree rooted at TARGETDIR.
func TestInstallDirBesideTargetDirTree(t *testing.T) {
	t.Parallel()

	doc := strings.Replace(base, "  - id: ProgramFiles64Folder\n    children:\n      - id: INSTALLDIR\n        name: App\n        configurable: true\n",
		"  - id: TARGETDIR\n    children:\n      - id: ProgramFiles64Folder\n        children:\n          - id: APPDIR\n            name: App\n", 1) +
		"components:\n  - files: [{ source: app.exe }]\n"

	if !find(validate(t, doc), config.SeverityWarning, "installDir", "next to APPDIR") {
		t.Error("no warning about APPDIR")
	}
}
