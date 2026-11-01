# msi-builder YAML schema, version 1

> [!TIP]
> To write a package, start with the [YAML Schema](../YAML%20Schema.md)
> guide: the smallest file and a recipe for each common task. This page is
> the complete reference and the reasons behind each rule.

This document defines the input format of msi-builder: one YAML file that describes
an MSI package. The `config` package implements it (types, loading, defaults
and validation). The MSI writer builds on that package and never reads YAML
itself.

The design is driven by two sources:

- the requirements for a generic builder, with windows_exporter as the
  reference package ([`examples/windows_exporter/msi.yaml`](../../examples/windows_exporter/msi.yaml)
  is the first acceptance test), and
- the Windows Installer specification in [`docs/spec/MSISPEC.md`](../spec/MSISPEC.md)
  and its Windows test results in [`docs/spec/MSISPEC-RESULTS.md`](../spec/MSISPEC-RESULTS.md).

The Windows runs (57 cases on Windows Server 2025, `msi.dll` 5.0.26100.1742)
contradicted eight claims of the specification and found several ways to
build a package that installs with exit code 0 and is still broken. Section 2
lists every finding and the schema rule it produced. The rest of the document
is the schema itself.

## 1. Principles

1. **Say what, not how.** Authors describe directories, files, services,
   registry values and actions. Table rows, attribute bits, `#`/`[~]` value
   encodings, sequence numbers and custom action type numbers are the
   builder's job. Every typed field exists because a raw encoding was easy to
   get wrong.
2. **Make silent failures unrepresentable.** Where Windows accepts a package
   and then misbehaves (an executable without a version, a removal that
   `*` does not imply, a script action whose return code is ignored), the
   schema has no way to write it, or validation fails the build.
3. **Validate against the engine, not the folklore.** Limits come from the
   Windows runs where they exist (31 columns, Page Count 500 at most), and
   from the specification otherwise.
4. **Reproducible by default.** The same file, the same inputs and the same
   `SOURCE_DATE_EPOCH` give a byte-identical MSI. Nothing is random unless the
   author asks for it.
5. **Nothing package-specific is built-in.** windows_exporter's firewall rule,
   service recovery and event log source are generic helpers that any package
   can use.
6. **Strict input.** Unknown keys are errors, so a typo never silently drops a
   setting. Every error names the YAML path (`components[0].files[1].source`).
7. **Short by default.** A simple package is a product, a platform and its
   files. The install folder, the feature, component IDs and the upgrade
   policy have defaults (section 3.3), and the explicit forms stay for the
   packages that need them.

## 2. What the Windows runs changed

Each row is a result from `MSISPEC-RESULTS.md` and the rule it put into the
schema or the validator. "Writer" marks rules for the MSI writer that have no
schema surface; they are listed so that the writer's design starts from them.

### 2.1 Contradicted claims

| Finding | Result on Windows | Schema rule |
|---|---|---|
| 1. Column limit (MSIDB-07) | The 32nd column is rejected (2280, 2216); the limit is 31 | `tables[].columns` allows at most 31 entries. |
| 2. Page Count (SUM-03) | x64 installs from 150 up; above 500 fails with 1613 on every platform; 0 and 1 fail | `package.installerVersion` maps to Page Count. It accepts 2.0 to 5.0 for x64, 5.0 only for Arm64 and 1.0 to 5.0 for x86, and rejects anything above 5.0. x64 keeps 2.0 as its floor because ICE80 was not run (section 4.2). |
| 3. String refcounts (MSIDB-05) | `msi.dll` keeps refcounts in 15 bits and drops a string whose count is a multiple of 32768 | Writer: every refcount stays in 1 … 32767. A string referenced more often is a build error until the writer can split it; no realistic package gets there, so the schema has no knob. |
| 4. `*` and removal (ENVVAR-01) | `=*NAME` (machine scope, no `-`) survives uninstall | `environment[].scope` and `environment[].removeOnUninstall` are separate fields. Removal defaults to `true` and the builder always writes `-` explicitly; scope never implies it. |
| 5. RemoveExistingProducts window (UPG-04) | 1399 (before InstallValidate) works like 1401; only 5802 gives 2613 | `upgrade.removeOld` names one of four windows. The validator checks the numbered result against the windows as the engine accepts them, not against a fixed number. |
| 6. Native CFB version (CFB-01) | `msi.dll` writes v4 even for small databases; v3 and v4 behave the same | Writer: emits v4 (4096-byte sectors) by default. No schema field. |
| 7. Per-user rollback (CA-01) | A deferred action failing after `PublishProduct` rolled back completely for an elevated administrator; for a non-administrator it left the registration | The "deferred action after RegisterProduct in a per-user package" check is a warning, not an error. It stays a warning because the leftover reproduces only for a non-administrator user (CA-01 results). |
| 8. SystemIdentifier (SUM-07) | The engine ignores it; the native writer stores `0x0002000A` | Writer: writes `0x0002000A` like the native writer. No schema field. |

### 2.2 Silent failures

These packages install with exit code 0 and are wrong afterwards. The schema
closes each one.

| Case | What happens | Schema rule |
|---|---|---|
| Executable with an empty `File.Version` (FILE-01) | On a major upgrade with early removal the new, unversioned file is disallowed against the old versioned one, then the old product is removed: the executable is **deleted** | `files[].version` defaults to `auto` (read from the PE version resource). A PE file without a version resource, or whose headers or version resource cannot be read, is a build error. `version: none` must be written explicitly and is only allowed for non-PE files, or with `allowUnversionedExecutable: true`, which also makes the validator warn about the upgrade. An empty string is never accepted. |
| A versioned file whose version goes down in a major upgrade (UPG-07) | The new component is disallowed against the installed file with the higher version, then the early removal of the old product deletes it | None in the builder: it cannot know the version a previous release shipped. When a release ships an older version of a file, set its `version` above the one installed (`files[].version: 2.0.0.1` for an installed 2.0.0.0), or use `upgrade.removeOld: afterInstallExecute`, which keeps the old file instead. |
| Corrupted cabinet data (MSZIP-01, CAB-04) | Wrong DEFLATE history, or a bit flip without a checksum, installs a corrupted file | Writer: every cabinet data block gets a checksum, and the builder re-extracts every cabinet after writing it and compares each file's SHA-256 with the source. Neither can be turned off, so the schema has no field for them. |
| Script custom actions (CA-02) | Inline JScript/VBScript actions (types 37/38) report success whatever they return; a Binary script returning 5 ends the install with 0 and leaves files unregistered | There are no script custom actions. Actions run an executable (`exec`, `execProperty`, `exe`), a command through msi-builder's DLL (`run`), a DLL (`dll`), set a property or directory, or fail with an error. |
| EXE exit codes (CA-02) | Any non-zero exit fails the install unless the action ignores it | `customActions[].return` is required for `exec`, `execProperty`, `exe` and `run` actions (`check` or `ignore`), so the author decides. |
| Same ProductCode, new package (UPG-03) | Installing a rebuilt package over the installed one fails with 1638 | `product.productCode: auto` derives the code from the upgrade code, version, platform and a digest of every input, including the contents of the wizard's licenses and theme pictures; the release that added these derives a new code once for every configuration that uses them. Changed content gives a new ProductCode (a major upgrade); unchanged content gives the same package byte for byte. Keys whose value means the same as a missing key (null, `""`, `[]`, `{}`) are left out of the digest, so a key a later version adds to the schema (as a pointer, string, list or mapping, or left out when empty) does not change the code of a configuration that does not use it; `false` and `0` stay in, since an explicit `secure: false` is not the default. The release that introduced this pruning derives a new code once for every configuration, unchanged ones included. So did renaming `ui.installDirPage` to `chooseDirectory` (its `false` was in every digest), and renaming `ui.wizard` to `enabled` does for every configuration with `wizard: true`. The release that writes HKCU key paths for components in the user profile (INST-04) derives a new code once for every configuration with such a component. A pinned ProductCode makes the validator warn. |
| Quote in a firewall rule description (CA-04) | netsh drops the quote and stores the rest of the description | `firewall[].name` and `firewall[].description` must not contain a double quote. |
| Firewall rule named `all` (CA-06) | `add rule` refuses the name, `delete rule name=all dir=in` deletes every inbound rule, in lower or upper case and with or without double quotes (also `\"all\"` through `cmd`) | `firewall[].name` must not be `all` in any case (spaces around it included) and must not contain `[` or `]`. |
| Versions that differ in the fourth field only (UPG-01) | Without an inclusive maximum both products stay installed side by side | `upgrade.allowSameVersion` defaults to `true` (maximum inclusive), so `0.32.0.5` replaces `0.32.0.4`. Four-field versions get a warning that Windows compares only three. |

### 2.3 Settled facts with a schema rule

| Fact | Source | Schema rule |
|---|---|---|
| Template platform tokens are exact and case-sensitive; `X64` and `arm64` give 1633 | SUM-02 | `package.platform` takes `x86`, `x64` or `arm64` in any case, plus the aliases `intel`, `amd64` and `aarch64`, and the builder writes the exact token. |
| Two languages in the Template make the engine look for a transform named by a language; without it, 1624 | SUM-02 | `product.language` is the one base LCID. Further languages come from `ui.languages`, which embeds a transform per language and lists it in the Template. |
| Summary code page 65001 or 1200 gives 1620; a 65001 database works | SUM-04, MSIDB-08 | `package.codepage` sets the database code page (1252 by default, 65001 allowed). The summary code page is the database code page when that is an ANSI page, else 1252. Every table string must fit the database code page, or the build fails with a `package.codepage` error. Summary text that the summary code page cannot hold is stored with `?` in place of those characters, with a warning. |
| Stream names are limited to 62 characters | MSIDB-11 | `icons` keys are at most 57 characters and `binaries` keys at most 55 (`Icon.` and `Binary.` count). The 62-character limit holds only for names made of `0`–`9`, `A`–`Z`, `a`–`z`, `.` and `_`: every other BMP character takes a stream-name unit of its own and a character outside the BMP two (31 in all), and `!`, `/`, `\`, `:`, NUL, U+3800–U+4840 and letters with case from U+0080 to U+D7FF are rejected. `package.cabinet` is checked with the stream-name encoder. |
| The minimum integer of a column type is stored as NULL | MSIDB-02 | `tables[].rows` reject −32768 in `i2` columns and −2147483648 in `i4` columns. |
| `MsiGetFileHash` returns zeros for an empty file | FILE-02 | Writer: an empty unversioned file gets a hash row of four zeros. |
| A service is deleted at uninstall only with `ServiceControl` event 128 | SVC-01 | `services[].control.deleteOnUninstall` defaults to `true`. |
| `MsiServiceConfigFailureActions` with restart actions fails with 1939; `sc.exe failure` works | SVC-01 | `services[].recovery` is implemented with a deferred `sc.exe` action, never with that table. |
| A launch-condition row and a type-19 action both block a downgrade with 1603 | UPG-02 | `upgrade.downgrade: error` uses a launch condition, which needs no custom action. |
| The root and directory tree of the compound file must be ordered | CFB-04, CFB-06 | Writer: a correctly ordered red-black-compatible tree; colours are free. |

## 3. File layout

```yaml
schema: msi-builder/v1   # required, selects this document;
                         # go-msi/v1 (the former name) fails with a hint
vars: {}                 # defaults for ${NAME} substitution
product: {}              # identity
package: {}              # platform, scope, compression, summary information
upgrade: {}              # major upgrade policy (on by default)
installDir: {}           # the product's folder (default: Program Files\<name>)
directories: []          # the install tree, for folders the shorthands do not cover
features: []             # the feature tree
components: []           # files, registry, services, folders, environment
properties: {}           # Property table, Secure and Hidden flags
launchConditions: []
customActions: []
icons: {}
binaries: {}
firewall: []             # helper: firewall rules
scheduleReboot: {}       # ask for a restart after the installation
ui: {}                   # wizard (off by default), its pages and added dialogs
tables: []               # escape hatch: custom tables
```

### 3.1 Variables

`${NAME}` anywhere in a string value is replaced before parsing the rest of
the file. Values come, in order of precedence, from the command line
(`--var NAME=value`), from the environment when the CLI is started with
`--env`, and from `vars`. A value in `vars` may itself use variables,
which are expanded the same way; a value from the command line or the
environment is taken as it is, and a `vars` entry it overrides is not
expanded at all. A cycle (`A: ${B}`, `B: ${A}`, or `A: ${A}`) is an error,
reported on the entry that closes it. An undefined variable is an error
where it is used, so a `vars` entry that nothing uses is not checked.
`$${` writes a literal `${`, also inside `vars`. The text that variables
insert into the document is capped at 16 MiB, and so is the text they
insert into `vars` values (a value nested through several entries counts at
each one), so doubling `vars` cannot blow up a document.
Windows Installer's own `[Property]` syntax is left alone, so
`[INSTALLDIR]` and `${VERSION}` never collide. Inside a flow mapping
(`{ … }`) a value that starts with `${` must be quoted, as YAML reads `{` as
the start of a mapping.

```yaml
vars:
  ARCH: x64
  NAME: Acme ${ARCH}
product:
  name: ${NAME}
  version: ${VERSION}
package:
  platform: ${ARCH}
```

### 3.2 Editor support

[`pkg/config/schema.json`](../../pkg/config/schema.json) is a JSON Schema of this
format. Editors that use the YAML language server pick it up from a comment
in the first line:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json
schema: msi-builder/v1
```

The JSON Schema checks keys, types, enums and syntax such as GUIDs, and
rejects the nil GUID as an upgrade code. Editors see the file before
variable substitution, so every typed value also accepts a string with a
`${NAME}` variable. References between sections, lengths that depend on
other values and the rules of section 2 are checked only by
`config.Validate`, which stays the authority. A test keeps the JSON Schema
in step with the Go types.

### 3.3 Defaults and shorthands

The smallest package needs no directories, features or upgrade section:

```yaml
schema: msi-builder/v1
product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
package:
  platform: x64
components:
  - files:
      - source: hello.exe
```

The loader expands the short forms into the explicit model before
validation, so the builder and every rule below see the same thing either
way:

| Short form | Expands to |
|---|---|
| no `installDir` | `INSTALLDIR`, configurable, named after the product, under `ProgramFiles64Folder` (`ProgramFilesFolder` on x86, `LocalAppDataFolder\Programs` for `perUser`). Not added when `directories` already defines that ID (section 4.4); added next to a tree with a configurable directory, or an upper-case one below Program Files, it warns, since components without `directory` go there. |
| component without `directory` | the install dir |
| `directory: bin` or `directory: CommonAppDataFolder/Hello/logs` | directory nodes for the path, with generated IDs such as `INSTALLDIR_bin` (section 4.4) |
| component without `id` | an ID from the key file's name (`hello.exe`), else the first service name, else the directory ID |
| no `features` | one feature `MainFeature`, titled with the product name, holding every component, with the install dir as its configurable directory |
| a component written inside `features[].components` | the same component in `components`, referenced by ID |
| `features[].components` entry that is only an ID | a reference to that component; in Go, `config.ComponentRef(id)` |
| no `upgrade` | the standard policy of section 4.3, as `upgrade: {}`; `upgrade: false` writes no Upgrade rows |
| `features[].default: on`, `off` or `disabled` | `level: 1`, `2` or `0` (section 4.5) |
| `features[].firewall` | the rules in `firewall`, following an empty component `<feature>_Firewall` that the feature holds (section 4.11) |
| a file `source` that ends in `/` (or `\`) | in place of its component, a directory per folder that holds a file and a component per file, as `Config.AddTree` adds them; the builder expands it after a first validation and validates the result again (section 4.6, "Adding a folder") |
| a service argument with a `condition` | a private property set by an immediate `setProperty` action before InstallServices under that condition (section 4.6) |

Generated IDs are stable: the same file gives the same IDs. Component GUIDs
do not depend on component IDs (section 4.6), so renaming or dropping an ID
never changes a GUID.

## 4. Sections

### 4.1 `product`

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | required | `ProductName`, at most 63 characters |
| `manufacturer` | string | required | `Manufacturer` and summary Author |
| `version` | string | required | `major.minor.build[.revision]`, at most 255.255.65535; a fourth field warns (section 2.2) |
| `language` | int | `1033` | One LCID; 0 is neutral |
| `upgradeCode` | GUID | required | Never changes for a product; the nil GUID is rejected |
| `productCode` | GUID or `auto` | `auto` | Section 2.2 |
| `packageCode` | GUID or `auto` | `auto` | Digest of the finished database |
| `description` | string | `<name> <version> installer` | Summary Subject |
| `comments` | string | `This installer database contains the logic and data required to install <name>.` | Summary Comments |
| `arp` | map | | `ARP*` properties: `comments`, `contact`, `helpLink`, `aboutLink`, `updateLink`, `icon` (an `icons` key), `noRepair`, `noModify`, `noRemove`, `size` (KB), `installLocation` (a directory ID; schedules `ARPINSTALLLOCATION` after CostFinalize) |

GUIDs are accepted with or without braces and in any case. The builder writes
them braced and upper case.

### 4.2 `package`

| Key | Type | Default | Notes |
|---|---|---|---|
| `platform` | enum | required | `x86`, `x64`, `arm64` (aliases `intel`, `amd64`, `aarch64`), any case |
| `scope` | enum | `perMachine` | `perMachine` (`ALLUSERS=1`), `perUser` (no `ALLUSERS`), or a dual scope that lets the user choose at install time: `perUserOrMachine` (`ALLUSERS=2`, `MSIINSTALLPERUSER=1`, per user unless the user chooses all users) or `perMachineOrUser` (`ALLUSERS=2`, per machine unless the user chooses per user); see "Install scope" below |
| `installerVersion` | string | `"5.0"` | Page Count = major × 100 + minor; a major alone, such as `5`, is 5.0. x86: 1.0 to 5.0, x64: 2.0 to 5.0, arm64: 5.0 |
| `codepage` | int | `1252` | Database code page, section 2.3 |
| `compression` | enum | `lzx` | `lzx` (LZX with a 2 MB window), `lzx:15` … `lzx:21` (a window of 2^N bytes), `mszip` or `none` (stored blocks) |
| `cabinet` | string | `<name>.cab` | Name of the first cabinet; further cabinets insert their number before the extension (`Hello2.cab`, `Hello3.cab`). Embedded, it is a stream name of at most 62 characters (fewer outside `0-9A-Za-z._`). ICE03 wants an identifier (letters, digits, `_` and `.`, not starting with a digit), so the default keeps only those characters of `<name>`, and an explicit name that is no identifier warns. The default is `product.cab` when that is no identifier, cannot be a stream name or `<name>` has non-ASCII characters. Beside the package (`embedCabinets: false`) it is a plain filename and need not be a stream name |
| `embedCabinets` | bool | `true` | `false` writes the cabinets as files beside the `.msi` (`Media.Cabinet` without `#`), which the package needs at install time. A package cannot exceed 2 GB, so a payload whose cabinets add up to more needs `false`; an embedded build above that fails with an error that says so. The payload is split into cabinets at file boundaries either way: a cabinet holds at most 65535 files and 2 GiB, counted with every block at the largest size its compression allows, so with LZX one cabinet takes about 1.8 GB of files whatever they compress to. A single file is at most 2 GiB − 32 KiB (section 5, CAB-10). A build with signing options signs the external cabinets too, and the package lists them in `MsiDigitalSignature`, so Windows Installer installs only from those exact cabinets (section 12, SIG-09) |
| `longFileNames` | bool | `true` | `false` sets Word Count bit 1 (short source paths). Each listed filename must itself be a valid 8.3 name, and a folder source is refused; directories and shortcuts get a generated `short\|long` when their name is not a valid 8.3 name. Installed folders still get the long names unless `SHORTFILENAMES=1` is set (FILE-06) |
| `summary` | map | | `title` (`Installation Database`), `keywords` (`Installer`), `author` (manufacturer) |

The x64 floor stays at 2.0 although the engine accepted 1.5 (SUM-03): ICE80
was not part of the run, and the requirements ask for ICE-clean output.
Above 5.0 every platform fails with 1613 on Windows Installer 5.0, the
current engine, so it is rejected.

`perUser` packages are checked for ICE105: no services, no HKLM registry
values, no no-impersonate actions and no per-machine directories. A dual
package may run a no-impersonate action with `scope: perMachine` (CA-09).

#### Install scope

`perUserOrMachine` and `perMachineOrUser` build one package that installs
either for the current user or for all users (Microsoft's "dual-purpose
package"; specification section 8.2, INST-02). Both need `installerVersion`
5.0. The command line chooses with `MSIINSTALLPERUSER=1` (per user) or
`MSIINSTALLPERUSER=""` (per machine); a wizard shows a scope page after
the welcome and license pages, see `ui`.

```yaml
package:
  platform: x64
  scope: perUserOrMachine   # per user by default, all users on request
```

The install folder stays under `ProgramFiles64Folder` (or
`ProgramFilesFolder`), which Windows Installer moves to
`%LOCALAPPDATA%\Programs` in a per-user install; the Start menu, the desktop,
HKMU and HKCR values follow the context the same way. A standard user can
install per user without elevation; a silent per-machine install by a
standard user fails with 1925. The ICE105 checks of `perUser` apply, except
that Program Files, Common Files and HKCR are allowed because they follow
the context. Directories below the Start menu, the desktop, the startup
and template folders need private (mixed-case) IDs: the scope page can only
move public folders under Program Files and Common Files, so the install
folder stays there too. `purge` is rejected under Program Files, Common
Files and those shell folders, because ICE105 keeps the action unelevated.
Custom action IDs starting with `SetInstallScope`, `SetScope` or
`RestoreInstallDir` are taken by the scope page. In the other scopes a
`MSIINSTALLPERUSER` property gets a warning: it has no effect unless
`ALLUSERS` is 2, and a per-user install was seen deleting it (reported). Like every per-user
package, a dual one needs its deferred actions that can fail before
RegisterProduct (section 8.2). A major upgrade only finds a product installed in the same context, so a product
installed per user and then per machine is installed twice; offer one
scope as the default and keep upgrades in it.

Machine-wide data has two forms in a dual package (ENVVAR-03). An
environment variable's `scope` defaults to `auto`: the builder writes it
as a machine variable in a generated component with the condition
`ALLUSERS=1` and as a user variable in one with `NOT ALLUSERS`, so a
`PATH` entry goes to the machine `PATH` for all users and to the user's
`PATH` per user. ALLUSERS is 1 per machine and unset per user when the
install runs, also after a switch on the scope page (INST-02), and in a
repair (ENVVAR-03). A component with `scope: perMachine` installs only per
machine and may write HKLM and HKU values and removals, machine variables
and event log sources; one with `scope: perUser` installs only per user.
The builder adds `ALLUSERS=1` or `NOT ALLUSERS` to the component's
`condition`, which must leave room for it in the 255 characters of
`Component.Condition`. Services and `serviceControl` stay refused, and so
do the folders ICE105 rejects.

```yaml
package:
  platform: x64
  scope: perMachineOrUser
components:
  - files:
      - source: dist/tool.exe
    environment:
      - { name: PATH, value: '[INSTALLDIR]', action: append }   # scope auto
  - id: MachineSettings
    directory: INSTALLDIR
    scope: perMachine                # left out of a per-user install
    registry:
      - { root: HKLM, key: Software\Acme\Tool, name: InstallDir, value: '[INSTALLDIR]' }
```

A `perMachine` component's resources are not installed per user at all,
so a value that both contexts need belongs under HKMU, which follows the
context. `scope` on a component of a package with a single context is an
error. In a `perUserOrMachine` package ICE105 warns about the HKLM
values, removals and event log sources of a `perMachine` component
(`Registry` and `RemoveRegistry` rows) although the condition keeps them
out of a per-user install, and the builder passes that warning on; it
reported nothing for them in a `perMachineOrUser` package and nothing for
the machine variable in either (ENVVAR-03). HKU values were not tested,
and a standard user's install was tested with an HKLM value only.

A custom action with `scope: perMachine` runs only per machine, and one
with `scope: perUser` only per user: the builder adds `ALLUSERS=1` or `NOT
ALLUSERS` to the condition of each of its schedules (and of the data
action of a `run` action). Only a `perMachine` action may have
`impersonate: false`, so an action that changes the machine as LocalSystem
is left out of every per-user install, also one that an administrator
runs elevated, and a standard user's per-user install needs no elevation
for it (CA-09). A scoped action runs in the execute sequence only: in the
UI sequence the scope page sets `ALLUSERS` back to 2, and an administrative
install has no context. ICE105 reads only the action's type, so in a
`perUserOrMachine` package it warns about a `perMachine` action without
impersonation although the condition keeps it out of per-user installs,
and the builder passes that warning on; it is silent in a
`perMachineOrUser` package (CA-09). The same holds for the Directory row
that an action's `directory` adds, so a dual or per-user package refuses a
folder ICE105 rejects, such as `System64Folder`, `WindowsFolder` or
`TempFolder`, as an action's working directory, as it does for components.

```yaml
customActions:
  - id: RegisterMachine
    run: { command: '"[#tool.exe]" register --all-users' }
    execute: deferred
    impersonate: false
    scope: perMachine                # left out of a per-user install
    return: check
    schedule:
      - { sequence: execute, after: InstallFiles, when: install }
```

### 4.3 `upgrade`

```yaml
upgrade:
  allowSameVersion: true          # maximum inclusive (default true)
  minVersion: 0.18.0              # oldest version removed (default: none, every older one)
  includeMin: true                # minVersion inclusive (default true)
  downgrade: error                # error | allow | ignore
  downgradeMessage: A later version of [ProductName] is already installed. Setup will now exit.
  removeOld: afterInstallInitialize
  migrateFeatures: true
  ignoreRemoveFailure: false      # go on when removing an old product fails
  property: OLDERVERSIONBEINGUPGRADED
  newerProperty: NEWERVERSIONDETECTED
  related:                        # extra Upgrade rows, e.g. a renamed predecessor
    - upgradeCode: 11111111-2222-3333-4444-555555555555
      minVersion: 0.0.0
      maxVersion: 1.0.0
      includeMin: true            # default true
      includeMax: false
      onlyDetect: false
      property: OLDPRODUCTFOUND
```

`upgrade` absent, `upgrade: true` and `upgrade: {}` all mean the standard
policy, and `upgrade: false` means no Upgrade rows at all. The builder
writes the two standard rows: one removes versions up to this one
(inclusive when `allowSameVersion`), and one detects newer versions.

`minVersion` is the VersionMin of the removing row, inclusive unless
`includeMin: false`. An installed version below it is not removed and stays
registered next to the new one (observed, UPG-13). Two msi-builder
packages with generated codes and the same key path locations share their
components, so uninstalling the new one leaves the shared files in place
(observed, UPG-13, with the same file in both versions; a file the newer
version replaced presumably stays in its newer version, not tested); a version built by another tool into the same
folder has other component codes, and uninstalling either product
presumably deletes files the other needs (inferred, not tested; section
8.10). A package that must not install next to such a
version detects it with a `related` row of its own upgrade code
(`maxVersion` at the floor, `includeMax: true` when `includeMin: false`,
`onlyDetect: true`) and a launch condition on that row's property, which
fails the install with 1603 and leaves the old version alone (observed,
UPG-13). The floor must be below `product.version` in the first three
fields, since Windows Installer compares only those.

`downgrade: error` adds a launch condition on `newerProperty`; `ignore`
detects newer versions but installs side by side; `allow` drops the
detection row.

`removeOld` windows (section 9.2 of the specification, widened by UPG-04):

| Value | Placed | Behaviour |
|---|---|---|
| `afterInstallValidate` | right after InstallValidate | old product removed first, outside the transaction |
| `afterInstallInitialize` (default) | right after InstallInitialize | removed first, inside the transaction, so it rolls back |
| `afterInstallExecute` (default when a component has `preserve: true`) | right after InstallExecute | new first, then old, one transaction; strict component rules |
| `afterInstallFinalize` | after InstallFinalize | separate transaction; a failed removal leaves both |

"Strict component rules" bite when a package moves from another toolset
whose components install the same files under different GUIDs, for example
harvested components replaced by a folder source. With
`afterInstallExecute`, the new product skips the files that are already
there ("hash matches"), and removing the old product then deletes them
(observed on Windows Server 2025). A component that kept WiX's GUID through
`guid` survived, and so does a file of a folder source that keeps it
through `guids` (UPG-12). Early removal keeps all of them.

`ignoreRemoveFailure: true` sets the IgnoreRemoveFailure bit (4) on every
Upgrade row that removes products, `related` rows included. When the
removal of an old product fails, the install goes on instead of failing
(1603 with the default `afterInstallInitialize`), and the old product
stays registered next to the new one, in every `removeOld` window
(observed, UPG-10, section 8.10 of the specification). It is meant for a
release whose own uninstall is known to fail. That uninstall still fails
afterwards; removing the old product needs whatever its uninstall offers
to get past the failure, or a fixed package (not tested).

Every property named here must be public, is added to
`SecureCustomProperties` and must not appear in `properties`.

### 4.4 `installDir` and `directories`

Most packages only need `installDir`, the product's folder:

```yaml
installDir: windows_exporter          # the folder name alone
installDir:
  id: APPLICATIONFOLDER               # Directory key and property, default INSTALLDIR
  name: Example/windows_exporter      # "/" nests folders, default product.name
  parent: ProgramFiles64Folder        # a standard folder, default by platform and scope;
                                      # perUser: LocalAppDataFolder\Programs; a name written
                                      # next to an explicit parent is used as written
  remember: true                      # reuse the folder of the last install, default false
```

The generated install dir is configurable, so `msiexec /i … INSTALLDIR=D:\App`
and the wizard's Browse button can move it, and its ID must be upper case.

`remember: true` keeps a custom install dir across major upgrades. The
package writes the folder to the registry value `InstallDir` below
`HKLM\Software\<manufacturer>\<name>` (`HKCU` for `perUser`), in a component
`RememberInstallDir` that joins every top-level feature. The next install
searches that value with `AppSearch` into `<ID>_REMEMBERED` (a directory
`RegLocator` search, in the 64-bit view on x64 and arm64, plus the empty
`Signature` table that `AppSearch` needs) and a type-51 action
`RestoreInstallDir` copies it into the install dir only when
`<ID>_REMEMBERED AND NOT <ID>`. SEARCH-03 tested this form on x64 per machine,
in the execute sequence of a quiet install: the upgrade went back to the
remembered folder, and `INSTALLDIR=` on the command line still won, while a
search straight into `INSTALLDIR` overrode the command line, and a search
without a `Signature` table failed with 2228 and 1603. SEARCH-06 confirmed
it for `perUser` (also as a standard user), x86, a 32-bit folder in an
x64 package, arm64 and both dual scopes in both contexts. The builder also
schedules both actions in the UI sequence, so the wizard's install folder
page shows the remembered folder and the upgrade installs there (UI-08).

A dual scope writes the value with root HKMU, which follows the context, and
searches both roots, since `RegLocator` has no HKMU: `<ID>_REMEMBERED_USER`
from HKCU with `RestoreInstallDirUser` under `NOT ALLUSERS`, and
`<ID>_REMEMBERED_MACHINE` from HKLM with `RestoreInstallDirMachine` under
`ALLUSERS=1`, the values the engine leaves in `ALLUSERS` per user and per
machine (INST-02). The scope page offers each context's remembered folder.

The search finds only a folder that exists (SEARCH-07). A remembered folder
that is gone, deleted or on a drive that is no longer there, leaves
`<ID>_REMEMBERED` empty and the package installs into its default folder.
A raw search would apply the folder as it is: that recreates a deleted folder,
but a folder on a missing drive fails the install with 1327 (Invalid Drive)
and 1603, so msi-builder does not use it.

The install dir must be a configurable directory with an upper-case ID, and
the IDs `RememberInstallDir` and `RestoreInstallDir` are taken. The component
and the search use the 64-bit registry view in a 64-bit package, and the
32-bit view when the install dir is below a 32-bit folder or the package is
x86. So the folder should not be found after a change of platform or of
that parent folder, nor after a version without `remember`, and the upgrade
should use the default folder; this follows from the search but was not
tested. Do not write your own
registry value `InstallDir` under the same key, which the generated
component owns.
When `directories` defines the install dir's ID, that node is used as
written, including its `configurable` setting. A package with a directory
tree and no `installDir` key gets the install dir only when a component
lands in it, so an explicit tree never gains an unused folder.

A per-user install dir lies in the user profile. Windows Installer wants an
HKCU registry value as the key path of such components (ICE38) and
RemoveFolder rows for their folders (ICE64). The builder writes both, for
every component below a profile folder in every scope: the RemoveFolder
rows (a `RemoveFile` row with no filename, at uninstall, for every folder
from the component's up to the profile folder), and a `Registry` row
`HKCU\Software\[Manufacturer]\[ProductName]` with a generated value name
and the DWORD 1 as its key path (INST-04). The value is left out when the
key path already is an HKCU value, or an HKMU value in a `perUser` package
(a dual package installed per machine writes HKMU to HKLM). The
ComponentId is derived before that, from the key file, registry value or
folder the component would have had, so a package built before keeps its
component codes; the automatic ProductCode changes once, so a rebuild of
an unchanged configuration does not collide with the package an older
msi-builder made (error 1638). These components keep their key path and
get the ICE38 warning instead: one with an explicit `keyPath`, one with
services (the key path is the executable), one with `neverOverwrite`,
`preserve` or `sharedDllRefCount`, whose engine checks test the key path
(reported), and one that writes a per-machine registry value (HKLM, HKU,
and HKMU or HKCR outside a `perUser` package), which an HKCU key path
would mix with per-user data (ICE57). With `upgrade.removeOld`
`afterInstallExecute` or `afterInstallFinalize` (also set by `preserve`)
the value is written but not made the key path: an upgrade from a file key
path to the HKCU value with that removal lost the component's files
(INST-04). Turning the late removal on in a later version (or adding
`preserve`, which does) changes those key paths back; that upgrade kept
the files, and since the new version writes the same values, its
uninstall removes them too (INST-04). Event log sources count as per-machine registry
values, and so do machine environment variables, HKLM and HKU
`removeRegistry` entries, and file types and HKMU or HKCR removals outside
a `perUser` package. A firewall carrier in the profile
gets the value. A `component` search for the GUID of a component whose
key path becomes the value is expected to find nothing, not the file (not
tested for this HKCU value; an HKLM registry key path found nothing with
either type, SEARCH-08). In a per-machine install the desktop and Start menu folders
are the all-users ones; the value still goes to the installing user's
HKCU, as ICE38 asks. A dual package's install dir is under Program Files in the
package, so its components keep their key files; components in profile
folders get the HKCU value in both contexts.

A component's `directory` is a directory ID, a standard folder, or a folder
path. A path starts at its first segment when that is a directory ID or a
standard folder, and in the install dir otherwise; `./` forces the install
dir. A single upper-case word is always an ID, and so is a word that
matches a known ID or standard folder in another case; the same holds for
the first segment of a path, so typos such as
`INSTALDIR` or `ProgramFiles64folder` fail validation instead of creating a
folder. Folders a path
names are added below their parent with IDs from the parent ID and the name
(`INSTALLDIR_textfile_inputs`); a folder that already exists is reused.

`directories` is the explicit tree, for directory IDs that something else
refers to. Every node has an `id` (the
Directory key and the property that holds its path) and a `name`.

```yaml
directories:
  - id: ProgramFiles64Folder
    children:
      - id: APPLICATIONFOLDER
        name: windows_exporter
        configurable: true       # public + Secure (not when a per-machine grant uses it), can be set on the command line and in the UI
        children:
          - id: TEXTFILEDIR
            name: textfile_inputs
```

- Top-level nodes must be standard folder properties (`ProgramFilesFolder`,
  `ProgramFiles64Folder`, `CommonAppDataFolder`, `System64Folder`, …) or
  `TARGETDIR`. The builder adds `TARGETDIR` with `SourceDir` itself.
- `name` is the long name. The builder generates the 8.3 short name the
  format needs from a hash of the parent key and the long name
  (specification section 11.3), unique among the directories and files of
  its folder; a long name that is itself a valid short name is used as is.
  A hand-written `short|long` name is an error: short names matter only
  with `SHORTFILENAMES=1`, on a volume without long names and in `[!file]`
  references (specification sections 8.3 and 8.9; reported, the last two
  not tested), and generating them keeps them unique. Directory IDs that
  name one folder (the same long names from the same root) get the same
  short name; with two, `SHORTFILENAMES=1` would presumably split the
  folder (inferred, not tested).
- File and directory names must be valid on Windows: no `\/:*?"<>|` or
  control characters, no trailing dot or space, and no device name such as
  `CON`, `CONIN$`, `nul.txt` or `COM1`.
- A 64-bit package that installs a component under `ProgramFilesFolder`,
  `CommonFilesFolder` or `SystemFolder` gets a warning (ICE80): those are the
  32-bit folders on 64-bit Windows.
- `configurable: true` requires an upper-case ID.

### 4.5 `features`

`features` is optional; without it every component goes into one feature
(section 3.3). A feature lists components by ID, or defines them in place,
which is the shortest form for a component that only one feature uses:

```yaml
features:
  - id: DefaultFeature
    title: windows_exporter ${VERSION}
    description: The binary and configuration files for windows_exporter
    default: on                # on | off | disabled
    conditions:                # level changes, mostly at a first install
      - { condition: NOT VersionNT64, default: disabled }
    display: expanded          # expanded | collapsed | hidden
    directory: APPLICATIONFOLDER
    allowAbsent: false
    allowAdvertise: false
    components:
      - Executable                 # defined in components
      - files:                     # defined here
          - source: README.md
    features:
      - id: FirewallException
        title: Firewall Exception
        default: off
        firewall:                  # section 4.11
          - name: windows_exporter
            program: '[#windows_exporter.exe]'
            protocol: tcp
            port: '[LISTEN_PORT]'
```

- Feature IDs are at most 38 characters; depth is at most 16.
- `default` says whether the feature is installed: `on` (the default) is
  `level: 1`, `off` is `level: 2`, which a user turns on in the feature tree
  or with `ADDLOCAL`, and `disabled` is `level: 0`. A higher `INSTALLLEVEL`
  also installs `off` features. Set `default` or `level`, not both; `level`
  is for packages that use more than these three levels.
- `level: 0` disables a feature; `ADDLOCAL=ALL` cannot turn it on unless one of its `conditions` is true.
- `conditions` are `Condition` table rows: each entry has a `condition`
  and the `default` (`on`, `off`, `disabled`) or `level` that replaces the
  feature's level when the condition is true. Two entries of one feature
  cannot set the same level (the row key is feature and level); join their
  conditions with `OR`. A condition cannot ask for a feature or component
  state (`!`, `?`, `&`, `$`): the documentation says the engine evaluates
  the table before it has them (not tested). The
  builder writes each row as `NOT Installed AND (<condition>)`: the engine
  evaluates the rows at every run, also with `ADDLOCAL` and at the
  uninstall, and a feature that a row disables when the product is removed
  keeps its files (INST-05). A row that enables a feature whose own level
  is 0 (`default: disabled`) is written as
  `(Installed AND (REMOVE OR REINSTALL)) OR (<condition>)` instead: with
  the first-install guard the feature fell back to level 0 at
  `msiexec /x`, which left it, its files and the product installed and
  still returned 0 (reported from a real-world port on SERVER2025;
  the fix is confirmed by INST-06). The maintenance term covers removals
  (`/x`, `REMOVE`, the old product's removal in a major upgrade) and
  repairs only, so `ADDLOCAL` in a later run still needs the condition.
  A package built before this change keeps the feature at its uninstall
  and at the old product's removal in a major upgrade (inferred, not
  tested). The conditions therefore decide first installs and, in part,
  major upgrades, removals and repairs; a feature that a condition
  disabled at the first install can be added later with `ADDLOCAL`,
  while a `default: disabled` feature still needs its condition. A major upgrade
  without the property drops a feature whose `disabled` condition is then
  true (`ASSOC <> 1`), and the old version's copy is removed. A feature that is off by default and has no
  `disabled` condition was kept by an upgrade when the old version had
  installed it, and stayed absent when the old version had left it out,
  even with its condition true; one that the old version had disabled
  followed its condition. A condition with `default: off` was not tested
  in an upgrade. A condition cannot use `Installed`, which the builder
  adds itself.
  When several conditions are true, the higher level won (INST-05).
- Every component is in at least one feature (ICE21). A component used by
  two features is defined once in `components` and named by ID in both.
- `allowAdvertise` defaults to `false`; advertised installs are not supported (section 6).

### 4.6 `components`

A component is one unit that Windows Installer installs, repairs and removes
together. Rules from section 8.4 of the specification apply: one versioned
key file, resources never move between components, and GUIDs are stable.

```yaml
components:
  - id: Executable                 # default: the key file's name
    directory: APPLICATIONFOLDER   # default: the install dir
    guid: 5546455E-B5EA-5AF8-A21A-AA526F04D083   # or auto (default) or none
    transitive: true
    files:
      - source: dist/windows_exporter.exe
        checksum: true
    registry: []
    removeRegistry: []
    removeFiles: []                  # RemoveFile rows, see "File removal" below
    services: []
    serviceControl: []               # services the package does not install
    eventLog: []
    environment: []
    createFolder: false
    condition: ""                    # no &Feature/$Component action states (SVC-09)
    # scope: perMachine              # dual packages only: perMachine | perUser, see "Install scope"
    permanent: false
    neverOverwrite: false
    preserve: false                  # neverOverwrite + a late removeOld, see below
    purge: false                     # true or {condition: …}, see below
    removeFolders: []                # a folder from a property, with its subfolders, see below
    sharedDllRefCount: false
    keyPath: windows_exporter.exe   # file name, registry id or "directory"
    bitness: auto                    # auto (from platform) | 32 | 64
```

**GUIDs.** `auto` derives a UUIDv5 from the upgrade code, the bitness and
the key path's canonical location (directory path and filename, or
registry root, key and value name). A changed key path therefore gets a new
GUID, as the component rules require. The HKCU value the builder adds as
the key path of a component in the user profile is the exception: the GUID
follows the key path the component would have without it (section 4.4). A pinned GUID is kept as it is (the
windows_exporter executable component keeps the GUID of its released
packages). `none` writes a NULL ComponentId and is only accepted with
`permanent: true`, because such a component is never removed (specification
section 8.4). Two components must not end up with one ComponentId (ICE08):
two `auto` components with the same bitness and key path, for example two
components in one directory with only environment variables and so the
directory as key path, need a pinned `guid` on one of them. The builder
reports this, because it resolves the key paths; `msi-builder validate` does not.

**Preserved configuration.** `preserve: true` keeps a configuration file
or registry value as the user left it across major upgrades. It sets
`neverOverwrite`, and `upgrade.removeOld` then defaults to
`afterInstallExecute`; an explicit early `removeOld` is an error, also
with `permanent`, and `afterInstallFinalize` gets a warning because UPG-05
did not test it. UPG-05 showed why, with one file or one registry value
as the component's key path and the early removal right after
InstallValidate (`afterInstallValidate`; `afterInstallInitialize` also
removes the old product first and is treated the same, not tested): with
the old product removed first, the new
product skips a `neverOverwrite` component because its key file exists, and
the removal of the old product then deletes it, so the edit is gone. With
the late removal the edit survives, also when upgrading from a version
built without `preserve` that already had the file or value as the key path
of its own component with the same component code (moving it out of another
component was not tested), and uninstall still removes the resource.
`neverOverwrite` + `permanent` with an early removal keeps the edit too
when the old version already had both flags (lost when it had neither; other
combinations not tested), and the resource then stays after uninstall.
The engine decides on the key path, so give each preserved file or value its
own component; resources beside the key path were not tested. A preserved
component is never updated, so a service is rejected. `neverOverwrite`
without `permanent` and an early `removeOld` gets a warning.

```yaml
components:
  - id: Config
    preserve: true
    files:
      - source: config.alloy
```

**File removal.** `removeFiles` writes `RemoveFile` rows of the component.
Each entry removes the files of one folder that match a pattern, or the
folder itself when it is empty:

```yaml
components:
  - id: Server
    files:
      - source: redis-server.exe
    removeFiles:
      - name: '*.log'                 # * and ? match; one folder, no subfolders
        directory: LOGS               # a directory ID, default the component's
        on: uninstall                 # install | uninstall (default) | both
      - directory: LOGS               # no name: the folder itself, once empty
      - { name: stale.txt, on: install }
```

The engine removes the files when the component is installed (`install`),
removed (`uninstall`) or both, and resolves the folder at that time, so the
rows follow an install dir the user moved, unlike `purge`. The name of a
packaged file in that folder gets the file's short name; any other pattern
that is no valid short name gets a generated short name part, which matches
only itself, so such a wildcard pattern does nothing when the package is
installed with short names. Two limits come from
FILE-03: a pattern never matches in subfolders, so the folder row only
removes a folder whose subfolders are gone, and `uninstall` rows also run
when a major upgrade removes the old product, so logs or data matched by
them do not survive an upgrade. Use `purge` for a data folder that must.
The builder already removes the folders of shortcuts and of components in
the user profile on uninstall; a folder entry for one of them warns, since
it only adds a second row.

**Data removal.** `purge` deletes the component's directory with every
file and subfolder in it when the product is uninstalled, including what the
application created there at runtime, but not on the uninstall half of a
major upgrade. `RemoveFile` rows cannot do this: in FILE-03 a `*` row left a subfolder
behind, and the rows also deleted files during a major upgrade. The builder
writes a deferred action (type 3170, no-impersonate, in a per-machine x64
package) running `"[System64Folder]cmd.exe" /c rmdir /s /q "[DIR]."`
right before InstallFinalize. An arm64 package gets the same action, a
per-user package type 1122 and an x86 package `[SystemFolder]cmd.exe`;
none of these was tested. The action has the
condition `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE AND DIR`. FILE-03 tested
this command at 6590 without `AND DIR`; the builder adds it so that an empty
property never runs `rmdir` on the working folder. FILE-03 removed the old
product at 1401 only, so "not on upgrade" is untested with a late
`removeOld` (for example with `preserve`).
`purge: {condition: DELETE_DATA="1"}` adds a condition, for example an
opt-in property set on the command line or by a checkbox; the public
properties it names are added to `SecureCustomProperties`, as in FILE-03.
The action ignores failures; what stays when a file is in use, and whether a
rollback after the action restores anything, were not tested. The condition does not look at the component's state, so the
folder is deleted even when the component was never installed.

The directory must be a named folder below a standard folder, and no folder
from it up to and including that standard folder may have a path that something besides
the Directory table can set (section 8.3 of the specification): a public
(upper-case) ID, which a command line such as `DATADIR=C:\` can set
(section 8.3; not tested at uninstall), `configurable`, a `setDirectory` or `setProperty` action, a
`properties` entry or a dialog field. The action deletes whatever the
property points to, so any of these could move the purge out of the
product's folders. Whether the installer ignores a private directory ID
given on the command line was not tested. A standard folder nested below
another folder is rejected too, because the installer resolves its path
itself, and so is a `%` in any folder name, because `cmd.exe` expands
`%NAME%` as an environment variable, and a `~`, because a name such as
`PROGRA~1` can resolve to another folder's 8.3 alias (not tested). A permanent component in the purged folder or below it
is an error: the component would outlive the product, but its folder would
not. Data below the install dir therefore cannot be purged;
use a path below a fixed standard folder other than TARGETDIR (public, and
its drive can change), whose generated IDs are private, such as
`CommonAppDataFolder/Vendor/Product`.

```yaml
components:
  - id: Data
    directory: CommonAppDataFolder/Example/App/data
    createFolder: true
    purge:
      condition: DELETE_DATA="1"
```

**Folder trees from a property.** `removeFolders` deletes the folder that
a property or directory ID holds, with every file and subfolder in it,
including what the application wrote there. The removal reads the folder
after costing, so a directory ID has its path; an install dir chosen at
install time is only known again at uninstall with `installDir.remember`
(a warning otherwise), and any other property needs something that sets
it, typically a search for a path the first install wrote to the
registry.

```yaml
installDir:
  remember: true
properties:
  DATA_DIR:
    search: { registry: { root: HKLM, key: SOFTWARE\Example, name: DataDir, type: directory } }
components:
  - files: [{ source: app.exe }]
    registry: [{ root: HKLM, key: SOFTWARE\Example, name: DataDir, value: '[DATA_DIR]' }]
    removeFolders:
      - property: INSTALLDIR          # on: uninstall (default) | install | both
      - { property: DATA_DIR, condition: DELETE_DATA="1" }
```

Actions of the embedded DLL (specification section 17.10) run right after
RemoveFiles, with data the engine builds from the entries (no action that
runs as the user hands it over): a deferred one checks each folder and
moves it by handle into a private folder at its volume's root, a rollback one moves
it back, and a commit one deletes it, opening each entry relative to its
folder without following junctions or links, which it deletes as links.
`uninstall` removes when the component is removed, but not on the
uninstall half of a major upgrade (`UPGRADINGPRODUCTCODE`), and a folder
of the package only when the whole product goes (`REMOVE~="ALL"`), so
that removing one feature keeps the files of the others; the DLL also
compares paths and refuses, unless `REMOVE~="ALL"`, any folder that is or
holds one of the package's directories, so a property that holds the
install dir under another name is refused as well (confirmed by FILE-11 on a major upgrade); `install` is
expected to remove every time the component is installed, which includes
a repair that reinstalls it and the new product of an upgrade (only the
first install is tested); `both` does both. The condition is ANDed and may
not contain line breaks; with the component's state it must fit the 255
characters of a sequence condition. Each entry takes two actions after
RemoveFiles, which the package's directories share, so about 40 entries
fit (fewer with hundreds of directories; the builder says when not). The properties keep the security their own
settings give them (`secure`, `configurable`). Unlike `purge`, the folder may be the install dir
or any folder the property names. That is the point of the feature, and
also its risk: `INSTALLDIR=D:\Tools` on the command line makes the
uninstall empty `D:\Tools`. An elevated session that a standard user
started (a repair of a per-machine product, for one) removes nothing, so
the user cannot point LocalSystem at a folder of their choice (expected). The action refuses an empty value, a path that
is not a full local path (`C:\…`, no `.` or `..`, no name ending in a space
or a dot), a drive root, a folder reached through a junction, a symbolic
link or a `subst` drive, a junction itself, the profiles folder, every
profile and every folder inside another user's profile and in Public and
Default (the profile of the account that started the session is allowed;
a session that runs as LocalSystem, such as a deployment tool's, has none
and refuses every user's data), everything when the profiles cannot be
read, every standard folder of the engine (Program Files, ProgramData,
the Windows and system folders, the profile folders such as AppData,
Documents and the Desktop, Temp) and every folder that holds one, and any
folder inside the Windows folder; it logs the reason as
`BuilderRemoveFolders: …` and removes nothing for that entry. FILE-11
tested the junction path, the Public profile, a folder that holds
`TemplateFolder`, a folder inside the Windows folder and the installing
account's own profile; the other refusals are expected, not tested.
A junction below the folder is deleted as a link; what it points to stays
(tested). A folder that cannot be moved, typically because a file in it
is open, is expected to stay whole (not tested). The actions never fail
the installation.

**Key path.** Default: the first file, else the first registry value, else
the directory, which gets a `CreateFolder` row (ICE18). A component below a
profile folder gets a generated HKCU value instead, with the exceptions in
section 4.4. A component with a
service must have the service executable as key file.

**Files.**

| Key | Default | Notes |
|---|---|---|
| `source` | required | Path relative to the YAML file; `\` counts as `/` on every OS |
| `name` | base name of `source` | The long name; the short name is generated |
| `id` | the name, made into an identifier (see below) | File key, used in `[#id]` references |
| `version` | `auto` | `auto`, a four-field version, the ID of another file (companion) or its name when it has no `id`, or `none`; never empty (section 2.2) |
| `language` | `auto` | From the PE resource; `none` for fonts |
| `allowUnversionedExecutable` | `false` | Required to build a PE file with `version: none` or without a version resource |
| `vital` | `true` | |
| `checksum` | `false` | Verifies the PE checksum at install: sets `File.Attributes` 0x400 on a PE file that has a checksum and `MSICHECKCRCS=1`, without which the engine ignores the attribute (FILE-09). A wrong checksum then fails the install (FILE-09). A non-empty `MSICHECKCRCS` in `properties` replaces the value. |
| `readOnly`, `hidden`, `system` | `false` | |

Unversioned files get an `MsiFileHash` row automatically.

Without `id`, a name that is an identifier of at most 72 characters is the
file key as it is. Any other name is made into one: every character other
than letters, digits, `_` and `.` becomes `_`, a name that starts with a
digit or `.` gets a leading `_`, and a result longer than 60 characters is
cut and ends in `_` and eight hex digits of the SHA-256 of the replaced
name. So `fleetspeak-client.exe` is keyed `fleetspeak_client.exe`, which is
the name to use in `[#fleetspeak_client.exe]`. Give long names an explicit
`id`, which is also easier to reference. `version` (a companion) and
`ui.launch.file` also accept the long name of a file without `id`. Two files
that end up with one key are an error that asks for an `id` on one of them.

Two files must not resolve to the same target path, even through two
directory IDs for one folder, and a file must not have the path of a
directory. The one exception is two conditional components in different
directory IDs, for example an x64 and an x86 variant of one file: that is a
warning (ICE30), and the conditions must never both be true. ICE30 also
only warns for two conditional components in one directory ID; msi-builder keeps
that an error. The
components still need different ComponentIds; an automatic GUID differs by
bitness and key file, otherwise set `guid` on one of them.

**Shortcuts.** A file can list `shortcuts`, each a non-advertised `.lnk`
to that file:

```yaml
files:
  - source: viewer.exe
    shortcuts:
      - description: View example files         # Start menu > Programs > product.name
      - directory: DesktopFolder                 # the desktop
      - name: Viewer (safe mode)                 # default: product.name, no .lnk
        directory: ProgramMenuFolder/Example     # an id, a standard folder or a folder path
        arguments: --safe-mode                   # formatted text
        workingDirectory: INSTALLDIR             # default: the file's directory
        icon: viewer.ico                         # a key of icons; default: the file's own icon
        iconIndex: 0                             # needs icon
        show: minimized                          # normal (default), maximized or minimized
        properties:                              # shell properties of the .lnk, formatted text
          System.AppUserModel.ID: Example.Viewer
          System.AppUserModel.ToastActivatorCLSID: "{6E3F1A3C-61B6-4C1B-9A0F-2C5D8B7E4A10}"
```

`directory` defaults to `ProgramMenuFolder` and is resolved like a
component's directory, so `ProgramMenuFolder/Example` creates the Start menu
folder. In a per-machine package the Start menu and desktop are the common
ones for all users, in a per-user package the user's own (SHORTCUT-01). Two
shortcuts with one name in one folder are an error, and a name ending in
`.lnk` gets a warning, because Windows adds the extension. The target
`[#id]` must fit the 72 characters of `Shortcut.Target`, so a file with
shortcuts has an ID of at most 69 characters.

The builder puts the shortcuts of one component's files into a generated
component of their own: in the folder of the first shortcut, with the
component's condition, 64-bit unless that folder is a 32-bit one (ICE80),
an HKCU value under `Software\[Manufacturer]\[ProductName]` as its key path
(ICE38, ICE43), `RemoveFile` rows for the folders it creates below a
profile folder (ICE64), and in every feature of the file's component. In
a per-user package that folder must not be a per-machine one (ICE105).
The generated component of a component that also installs something is
never permanent, so the shortcuts of a file in a `permanent` component
are removed on uninstall while the file stays
(observed on a clean Windows machine: wazuh's "Edit conf" shortcut of its
permanent `ossec.conf`). Builds before this rule made that component
permanent, and Windows keeps a permanent ComponentId registered after
uninstall: a later package with the same ComponentId kept the shortcut and
its key path value after its own uninstall (observed). The generated
component of a `permanent` component therefore gets a ComponentId that no
older build used, and the ProductCode digest changes with it, so that a
rebuild of the same version does not fail with 1638. The old registration
stays for good, but no longer keeps the new package's shortcuts: the new
package's uninstall removed the shortcut and the key path value of the
same name (observed).
SHORTCUT-01 installed and removed shortcuts from an own HKCU-keyed component and
from the file's own component alike, and removed an empty Start menu folder
without a `RemoveFile` row; the Windows tests run the ICE suite on the
generated layout of a per-machine package (desktop, Start menu, a Start
menu subfolder and an icon), in per-machine, per-user and dual packages. An `icon` is stored in the `Icon` table and
written to the product's installer folder; without one the shell shows the
target's icon, and `iconIndex` without `icon` is an error. Advertised
shortcuts are not supported (section 6).

`properties` sets shell properties on the `.lnk`, one `MsiShortcutProperty`
row each: the key is the canonical name the Windows property system knows
the property by, the value formatted text that the engine converts to the
property's type: text, `1` or `true` for a boolean, a number, a CLSID in
braces. The common ones are `System.AppUserModel.ID`, which ties
the shortcut to the program's windows and notifications, and
`System.AppUserModel.ToastActivatorCLSID`, which a program that is started
from a toast notification needs. The engine applies them when it creates the
shortcut, on install and repair; a name the property system does not know
is warning 1946 in the log and the install goes on, and so is a value the
engine cannot convert (SHORTCUT-02). A `ToastActivatorCLSID` without braces
is such a value, so the validator rejects it. The table needs Windows
Installer 5.0, so a lower `installerVersion` gets a warning.

**File types.** A file can list `fileTypes`, each a ProgID that the file
opens, with the extensions that point at it and its shell verbs:

```yaml
files:
  - source: viewer.exe
    fileTypes:
      - progId: Example.Document                 # a key under HKEY_CLASSES_ROOT
        description: Example document            # the type's name in Explorer
        iconIndex: 0                             # DefaultIcon: "[#viewer.exe]",0; leave out for none
        extensions:
          - extension: exdoc                     # without the dot
            contentType: application/x-exdoc     # optional Content Type value
          - extension: txt                       # only add the type to Open with
            openWithOnly: true
        verbs:                                   # default: one open verb; [] for none
          - id: open
            text: '&Open'                        # menu text; empty: the shell's text for the id
          - id: print
            arguments: /p "%1"                   # after the quoted file; default "%1", '' for none
```

The builder writes them as `Registry` rows of the file's component, so
they follow its features and key path: `<progId>` = description,
`<progId>\DefaultIcon`, `<progId>\shell\<verb>` = text,
`<progId>\shell\<verb>\command` = `"[#file]" <arguments>`, `.<extension>` =
progId and its `Content Type`. These are the rows WiX writes for
non-advertised `ProgId`, `Extension` and `Verb` elements: the five real WiX
packages with file associations that were checked have no `ProgId`,
`Extension` or `Verb` table at all, and the msi-builder ports with the same
rows left the same registry behind (specification section 10.2); a
later survey found the same rows in 9 of 10 real packages with file
associations, the tenth being advertised. Microsoft documents those tables
for normal installs too (`RegisterExtensionInfo` registers the extensions
of features set to be installed; documented, and REG-05 observed it), but
their uninstall deletes the `.<extension>` key's values, other programs'
included (REG-06; with their subkeys in a real package, REG-07), so
msi-builder uses `Registry` rows like those packages do (specification
chapter 16). msi-builder also writes an empty `<progId>` value in
`.<extension>\OpenWithProgids`, which Microsoft asks of every program that
takes over an extension and which lists the program under Open with
(specification section 10.2, REG-04). The root is HKCR, which the engine
writes to `HKLM\Software\Classes` per machine; a per-user package names
`HKCU\Software\Classes` itself, because HKCR fails ICE105.

`arguments` is formatted text, so `[INSTALLDIR]` works there;
`description`, `text` and `contentType` are written literally. A ProgID may
be registered once per package, must not start with a dot and must not be
`*`, `+` or `-`. A ProgID that is not COM-style (at most 39 letters, digits
and periods, not starting with a digit) gets a warning.

Uninstalling removes the values the package wrote, as in the real
packages checked: the ProgID, its verbs, the `OpenWithProgids` entries and
the `.<extension>` default and `Content Type`. The default goes even when
another program set it after the install, because it is the same value;
other programs' values and subkeys in the extension key stay (observed,
REG-07). Microsoft's File Types page asks installers to leave the default
in place; msi-builder follows the real packages instead (Joe's choice,
2026-10-29; the rejected alternative is in specification chapter 16). No
package checked tells the shell that associations changed, and neither does
msi-builder, so Explorer may pick the change up only later (documented,
unverified). Registering an
extension that Windows or other programs own (`txt`, `xml`, `pdf`, …) gets
a warning, because the package takes it over from them and uninstalling
removes their default. For such an extension, `openWithOnly: true` writes only the
`OpenWithProgids` entry: the program shows up under Open with and the
extension keeps its default program and content type (`contentType` is then
an error). Uninstalling removes only that entry and keeps other programs'
entries (REG-04). Any number of ProgIDs may list an extension with
`openWithOnly`; the default may be registered once. Explorer's "Open with"
choice of the user (`UserChoice`) still wins over the package's default.

**Closing running programs.** `close` on a program stops its running
copies when its component is installed, repaired or removed, so that an
upgrade does not leave the old version running from a pending-delete copy
and an uninstall does not wait for a reboot:

```yaml
files:
  - source: tray.exe
    close: true                    # WM_CLOSE, 5 s, then terminate
  - source: agent.exe
    close:
      timeout: 10s                 # how long it gets to quit; 0s to 5m, default 5s
      terminate: false             # default true: end what is still running
```

Two actions on the embedded DLL do it (specification section 17.9).
Before `InstallValidate`, as the installing user, every process whose
image is the installed file gets `WM_CLOSE` on its visible top-level
windows (on its hidden ones when it has no visible window, as a program in
the notification area), and the install waits up to `timeout` for those
to exit, per file: two files with `30s` can wait a minute. After `StopServices` and before `RemoveFiles`, as LocalSystem in a
per-machine install, the copies still running from that path are ended
with exit code 1602, in every session. A process matches by its full path,
compared without case, so a copy of the program in another folder (a
portable copy, a Kubernetes HostProcess container) keeps running; WiX's
`util:CloseApplication` matches the filename only (reported), which the
2026-10-25 evaluation of the `windows_exporter` example found a problem.
An install folder given as an 8.3 path, a `subst` drive or a junction does
not match the image path the process reports, so nothing is closed then
(fails safe; not tested).

What gets ended, and when:

- Every repair of the component counts, also one that does not replace
  the file (`$Component=3`), and so does every removal.
- Copies that other users run on the same machine, a terminal server
  included, are ended without a `WM_CLOSE`, because the first action only
  reaches the installing user's desktop (CLOSE-01 confirmed that side on
  the CI runner; other sessions are not tested). Ending a program loses what it has not saved, so
  `terminate: false` suits editors and tools where the user should decide;
  the engine's files-in-use handling then deals with what still runs.
  A program without a visible window also gets `WM_CLOSE` on its hidden
  helper windows; one that only hides itself then may keep running in a
  broken state, so `terminate: false` suits programs with a main window.
- That files-in-use check runs before the programs without a window are
  ended, so it still lists them, and also a program of the same name in
  another folder (observed, CLOSE-01). A silent install only logs this; a
  full UI may show its files-in-use dialog (not tested).
- A console program has no window of its own (its console window is
  left alone), so it is ended, or left alone with `terminate: false`.
- A service the package installs is stopped by its `control` events
  first when `stopOnInstall` and `stopOnUninstall` are on (the defaults);
  without them its process is ended like any other, and the service's
  recovery actions may start it again.
- In the first major upgrade from a version without `close`, the old
  version is removed before the deferred action runs when `removeOld` is
  early (the default), so a windowless program of the old version keeps
  running while its file goes to a pending-delete copy; whether the
  deferred action still finds it by path afterwards is not tested. From
  then on both versions carry `close`.

Only files that end in `.exe` take `close`, and a folder source does not.

Set `MSIRESTARTMANAGERCONTROL: Disable` with `close`. Restart Manager
otherwise asks every process that holds a package file to shut down in a
silent install, also an unrelated one that only has the file open, and
waits for the user in a basic UI (reported by the close-apps evaluation, 2026-10-25);
`close` covers the programs the package knows about. Not adopted from WiX:
closing a program by name wherever it runs, end-session messages
(`WM_QUERYENDSESSION`), registering programs for a restart after the
install, and a reboot prompt for programs that refuse to exit.

**Component shortcuts.** A component can list `shortcuts` of its own, for
shortcuts that do not point at a file listed with them. Each names exactly
one target:

```yaml
components:
  - id: Viewer
    files: [{ source: viewer.exe }]
  - id: StartMenu
    shortcuts:
      - name: Configuration
        directory: ProgramMenuFolder/Example
        folder: INSTALLDIR/config                # opens a folder
      - name: Documentation
        directory: ProgramMenuFolder/Example
        url: https://example.com/docs            # a .url file
        iconFile: viewer.exe                     # a packaged file; iconIndex picks the icon
      - name: Uninstall Example
        directory: ProgramMenuFolder/Example
        program: "[SystemFolder]msiexec.exe"     # a program outside the package
        arguments: /x [ProductCode]
features:
  - id: Main
    components: [Viewer, StartMenu]
  - id: DesktopShortcut
    title: Desktop shortcut
    default: off
    components:
      - shortcuts:
          - { directory: DesktopFolder, file: viewer.exe }   # a file of any component
```

They take the keys of a file's shortcuts, with these differences:

- `file` is the ID or name of a packaged file in any component. The
  shortcut starts in that file's folder unless `workingDirectory` says
  otherwise. It is installed with the features of the component that lists
  it, not those of the file, which is how a shortcut gets a feature of its
  own. When a feature of that component does not also install the file,
  the target is the file's folder and long name, `[INSTALLDIR]viewer.exe`,
  instead of `[#id]`: ICE69 reports a `[#id]` into another feature's
  component as an error, and the shortcut then points at a file that may be
  absent (SHORTCUT-04). A file that a folder source adds has a generated
  ID and cannot be named, so list it on a component of its own and exclude
  it from the folder.
- `folder` is resolved like `directory`. A folder that is not a standard
  folder gets a `CreateFolder` row, even when a component names it, since
  that component may have no files, a condition or another feature; so the
  folder exists while the shortcut does and is removed with it when empty; below a profile folder it also gets `RemoveFile` rows (ICE64). A
  folder shortcut has no `arguments` or `workingDirectory`.
- `program` is the formatted path of a program outside the package, such
  as `[SystemFolder]msiexec.exe` or `[System64Folder]cmd.exe`;
  `SystemFolder` is `SysWOW64` on 64-bit Windows, also in an x64 package
  (SHORTCUT-04). It must start with a property reference: a
  `Shortcut.Target` without one is a feature key, which makes the row an advertised shortcut (section 6). The path
  holds no quotes; arguments go in `arguments`. It has no working directory
  unless one is set.
- `url` makes an internet shortcut: `Name.url` with `URL=` in its
  `[InternetShortcut]` section, written by `IniFile` rows instead of a
  `Shortcut` row, plus a `RemoveFile` row that deletes the file at
  uninstall, also after the shell added sections to it. The address is
  formatted text (`[ProductVersion]` works, `[\[]` is a literal bracket),
  starts with a scheme and holds printable ASCII only, so percent-encode
  other characters. A `.url` folder that is not a standard folder gets a
  `CreateFolder` row. `arguments`,
  `description`, `workingDirectory`, `icon`, `show` and `properties` do
  not apply to it. Its icon is `iconFile`, the ID or name of a packaged
  `.ico`, `.exe` or `.dll`, written as `IconFile=[#id]` (or the folder and
  name form of `file`, by the same feature rule) with
  `IconIndex=` from `iconIndex` (default 0); an `icons` entry cannot be
  used, because the engine writes those only for `Shortcut` rows. Without
  `iconFile` the shell shows the browser's icon. Its name must not be that
  of a packaged file in the folder.

The targets of `file`, `folder` and `program` fill the 72 characters of
`Shortcut.Target` as `[#id]` (or `[DIRECTORY]name`), `[DIRECTORY]` and the
path; a longer target is an error at build time, and a shorter `id` on the
file or its directory fixes it. A `.lnk` and a `.url` of one name are two
files and may share a folder. Component shortcuts go into the same
generated component as the shortcuts of the component's files, so they
need no file, registry value or key path of their own. A component that
lists nothing but shortcuts is that generated component itself: it gets no
row of its own in its `directory`, so it raises no ICE38 in a per-user
package, and without an `id` it is named after its first shortcut (or the
product, when that shortcut has no `name`)
(`Docs_Shortcuts`), so that its ComponentId does not depend on the order of
the components. Its `guid`, `condition`, `permanent` and `transitive`
apply to that component; its `directory` is not used. In a component that
also installs something, `permanent` stays with that component, not with
its shortcuts. A component with a folder source cannot list them.
SHORTCUT-03 and SHORTCUT-04 installed every kind on Windows.

**Permissions.** A file, registry value, service or the component's folder
can carry a `permissions` block (`folderPermissions` on the component, which
then needs a `CreateFolder` row: `createFolder: true` or a directory key
path). It becomes one `MsiLockPermissionsEx` row; the authored DACL replaces
the inherited one (specification section 6.2, SEC-01):

```yaml
files:
  - source: windows_exporter.exe
    permissions:
      sddl: 'D:PAI(A;;FA;;;BA)(A;;FRFX;;;<BUILTIN\Users>)'
      condition: LOCKDOWN=1      # optional; a false condition keeps the inherited ACL
```

The SDDL is checked at build time after each `<DOMAIN\account>` form, which
the engine resolves (including `[%ENV]` inside it), is replaced by a
placeholder SID. `[Property]` references are not expanded by the engine and
are rejected. A deny ACE gets a warning, because SEC-01 saw a deny for
`BUILTIN\Users` lock the installer out of its own file on uninstall. A
DACL with no allow ACE for SYSTEM, Administrators or Everyone (which
SYSTEM is part of) on the object itself (`SY`, `BA`, `WD`, their SIDs,
`<SYSTEM>`, `<NT AUTHORITY\SYSTEM>`, `<Administrators>`,
`<BUILTIN\Administrators>` or `<Everyone>`; localized or
machine-qualified names are not recognized) gets a
warning too: the engine runs as SYSTEM, and a folder locked to
`NetworkService` alone failed its install and the rollback of an upgrade. A
`LockPermissions` custom table cannot coexist with it (ICE104, error 1941).

**Grants.** `grant` in a `permissions` block of a file or service, or in
`folderPermissions`, adds an access-allowed ACE to the DACL the object has
after it is installed and keeps every ACE already there, inherited or
explicit. That is what a data folder below `ProgramData` usually needs, and
the account may come from a property, which `sddl` cannot do
(specification section 17.7, SEC-02):

```yaml
folderPermissions:
  grant:
    - account: '[SERVICE_ACCOUNT]'   # formatted at install time
      rights: [modify]               # names or hexadecimal masks
      inherit: true                  # folders only; default true (OICI)
  condition: ''                      # optional; applies to sddl and grant
```

- `account` is a SID string, a two-letter SDDL alias such as `NS` or `BU`,
  one of the service manager's names `LocalSystem`,
  `NT AUTHORITY\LocalService` and `NT AUTHORITY\NetworkService` (the
  spellings `ServiceInstall` takes, mapped to their SIDs), `.\name` for a
  local account, or another name for `LookupAccountNameW`, such as
  `CONTOSO\svc`. Windows localizes the names of built-in accounts and
  groups (`BUILTIN\Users`, `NT AUTHORITY\NETWORK SERVICE`), so use their
  SIDs or aliases; a two-letter name that is an SDDL alias is read as the
  alias. Spaces
  around a formatted account are dropped. An account that contains `|` or
  a control character, cannot be resolved or names a domain fails the
  installation, and so does an empty one on a first install. An account
  from a property is remembered per product (per product and user in a
  per-user install), so a maintenance install (a
  repair from Apps & Features, a self-repair, a feature added later, a
  patch) whose properties in the account are all empty grants to the
  account of the last successful install;
  only an account that was never remembered is skipped there, with a log
  line. Removing the product forgets it. A major upgrade is a first
  install of the new product and needs the account again.
- An inheriting grant on a folder that a property, an action or a dialog
  can move, such as `INSTALLDIR`, gets a warning: `INSTALLDIR=C:\Windows`
  would grant on that whole tree.
- `rights` for files and folders: `full`, `modify`, `readExecute`, `read`
  and `write` (the simple rights Explorer shows); for services: `full`
  (`SERVICE_ALL_ACCESS`), `read` (`SERVICE_GENERIC_READ`), `start`, `stop`
  and `pauseContinue`. A `0x` hexadecimal mask adds other rights of the
  object; generic rights, `MAXIMUM_ALLOWED` and `ACCESS_SYSTEM_SECURITY` are
  rejected, because an explicit ACE keeps them unmapped. The rights are
  merged into the account's explicit allow ACE with the same inheritance,
  if there is one. `write` is 0x100116, without `READ_CONTROL`; a port of
  a `util:PermissionEx` with `GenericWrite` (`FILE_GENERIC_WRITE`, 0x120116)
  writes `rights: ['0x120116']` to get the same mask, as `msi-builder
  convert` does.
- Registry values cannot have grants: secure the key with `sddl`.
- `sddl` and `grant` can be combined: the engine applies the SDDL when it
  creates the object, then the grants are added to it.

The engine builds the list of grants itself, with one property-setting
action per grant that runs when its component is installed or repaired
(`$Component=3`) and the block's condition holds, so an action running as
the installing user cannot change the list after the engine formatted it.
A package with services has at most 44 grants, 19 when a service uses
`delayedAutoStart` or `failureActionsOnNonCrashFailures`, fewer with other
actions after `InstallServices` such as `recovery`; the builder says how
many fit. The account and the
folder still come from properties, and a standard user can start a
repair of a per-machine product with their own `SVC_ACCOUNT=...` on the
command line, which the engine passes only for a secure property (SEC-03;
current Windows then refuses a silent repair of a package with grants
with error 1730 unless an administrator enters credentials, older builds
presumably do not). So the
properties in an account and the public directories above a folder
grant's folder, or a file grant's component directory, up to the standard
folder (`INSTALLDIR` for `INSTALLDIR/data`) are not secure, unlike other
public properties: an administrator's command line still reaches the
installation, and a standard user's value is dropped (SEC-03), so a
repair that runs grants to the remembered account. `secure:
true` in `properties` keeps one secure, and so does a `search`; the
builder then warns about the repair unless the wizard sets the property.
The wizard needs `secure: true` when a dialog field, the folder page or
the feature tree sets the account or folder, and the builder warns when
a property the wizard sets, or a configurable directory other than the
install dir, loses the secure status it would otherwise have. A `setProperty` or `setDirectory`
action that sets such a property makes the properties in its value count
too. An account, and the value of such an action, cannot use a nested
reference (`[[NAME]]`, `[A[B]]`). Per-user packages are not affected. Three actions on the embedded custom action
DLL apply them, after `InstallFiles` (after `InstallServices` when the
package has services) and before `StartServices`, so a service can use its
folder when it starts: the deferred `BuilderGrantExec` grants without
impersonation (as LocalSystem in a per-machine install) and records the
rights it added, `BuilderGrantRollback` takes exactly those away again, so
rights an object had before a failed install or repair stay, and
`BuilderGrantCommit` drops the record and remembers the accounts that
came from properties. A repair grants again; an object that kept its ACE is
left as it is (a file a `/fa` repair overwrote kept its ACE, SEC-02). Uninstalling does not take them away: the objects the
package created are removed, and a folder that existed before keeps the
ACE.

**Registry values.**

```yaml
registry:
  - id: EventMessageFile          # optional; generated from the content otherwise
    root: HKLM                    # HKLM | HKCU | HKCR | HKU | HKMU (per-user or per-machine)
    key: SYSTEM\CurrentControlSet\Services\EventLog\Application\windows_exporter
    name: EventMessageFile        # empty: the default value
    type: expandString            # string | expandString | multiString | dword | binary | key
    value: '%SystemRoot%\System32\EventCreate.exe'
```

The builder encodes the value: `dword` writes `#n`, `binary` writes `#x…`,
`expandString` writes `#%…`, `multiString` takes a list and joins it with
`[~]`, and a `string` that starts with `#` is escaped as `##`. `type: key`
creates the key (`+`) or, with `removeOnUninstall: true`, creates and removes
it (`*`). `append: true` and `prepend: true` on a `multiString` merge with an
existing value. A `type: key` entry and the key's default value are separate
rows; the same value written twice in one component is an error unless
`id` tells the two apart.

**Registry removal.** `removeRegistry` deletes keys or values that exist
before the install, for example the handlers of an old version or a value
an earlier release wrote outside its own components:

```yaml
removeRegistry:
  - root: HKCU                       # HKLM | HKCU | HKCR | HKU | HKMU
    key: Software\Classes\oldapp      # no name: the key with all values and subkeys
  - id: LegacyArgs                   # optional; generated from the content otherwise
    root: HKLM
    key: SOFTWARE\Example\App
    name: Arguments                  # one value
  - root: HKCR
    key: Directory\shell\exampleapp   # a key the program creates while it runs
    on: uninstall                    # install (default) | uninstall | both
```

An install entry (the default) is a `RemoveRegistry` row of the component
(name `-` for a whole key). The engine deletes it when the component is installed, in the
component's registry view only, again on a repair, and leaves it alone at
uninstall (REG-02, observed on Windows). `RemoveRegistryValues` runs before
`WriteRegistryValues`, so a key that is removed and also written by the
package ends up with exactly the package's values, without values the user
added there (REG-02). `HKMU` in a per-machine package is `HKLM`. In a per-machine package an `HKCU` entry
can only reach the profile of the user who runs the install; other users'
profiles are not touched (not tested). A component that only removes
registry entries has its directory as key path (in the user profile the
HKCU value of section 4.4, unless it removes per-machine entries). Removing the default value
of a key is not supported. The same root, key and name twice in one
component and table is an error, with or without `id` (an install and an
uninstall entry for the same key are fine; `both` counts as either), and
so is a per-user package that removes from HKLM or HKCR.

`on: uninstall` deletes a whole key with its values and subkeys when the
component is removed, for keys the program creates while it runs (shell
verbs, settings) that the package does not write. It is a `Registry` row
with name `-` and a Null value: the install neither creates nor deletes
the key (one that existed before keeps its subkey and its value),
a repair leaves a key that exists, and uninstall deletes it with its
subkeys in the component's registry view (REG-08, observed on Windows per
machine; per-user and dual-scope installs not tested; whether a repair
would create a missing key not tested). The old product's removal in
a major upgrade removes its components too, so with the default
`removeOld: afterInstallInitialize` an upgrade deletes the key although the
new version has the same entry; with `afterInstallExecute` the key stayed
(REG-08), presumably because the new version keeps the components with the
same ComponentId installed (inferred), so a component whose `id` or `guid`
changes in the new version loses its key there too (not tested). `on: both` adds the install
removal of a `RemoveRegistry` row. Both forms take whole keys only: a
single value cannot be removed at uninstall without writing it, because a
`Registry` row with a name and no value writes an empty value (REG-04); a
value the package writes with `registry` is removed at uninstall anyway.
An uninstall entry is a row of the `Registry` table, so its `id` must not
be the `id` of a `registry` entry. An entry for a `shell` key under HKCR gets the ICE33
warning that `fileTypes` rows get too (the engine would rather see the
`Verb` table); it does not stop the build.

**Services** (only in a component whose key file is the service executable):

```yaml
services:
  - name: windows_exporter
    displayName: windows_exporter
    description: Exports Prometheus metrics about the system
    type: ownProcess             # ownProcess | shareProcess
    start: auto                  # auto | demand | disabled
    delayedAutoStart: true       # MsiServiceConfig ConfigType 3
    errorControl: normal         # ignore | normal | critical
    vital: true                  # ErrorControl | 0x8000
    account: '[SERVICE_ACCOUNT]'
    password: '[SERVICE_PASSWORD]'
    arguments:                   # one string, or pieces
      - --log.format=json
      - value: --collectors.enabled [ENABLED_COLLECTORS]
        condition: ENABLED_COLLECTORS
    dependencies: [wmiApSrv]
    failureActionsOnNonCrashFailures: true   # MsiServiceConfig ConfigType 4
    control:
      startOnInstall: true
      stopOnInstall: true
      deleteOnInstall: false     # replace a stale service of the same name
      stopOnUninstall: true
      deleteOnUninstall: true
      startCondition: START_AFTER_INSTALL="1"   # start only when true
      wait: true                 # stop and delete wait for completion
      waitForStart: true         # defaults to wait
    recovery:
      resetAfter: 24h            # or resetAfterDays: 1
      actions:                   # one to three steps
        - restart                # waits restartDelay
        - { restart: 5m }        # a delay for this step only
        - none
      restartDelay: 60s
```

`arguments` is the service command line, as one formatted string or as a
list of pieces joined with spaces. A piece with a `condition` is only part of
the command line when the condition is true: it becomes a private property
`Arg_<service>_<n>`, set by an immediate `setProperty` action
`SetArg_<service>_<n>` before InstallServices, and the command line names
the property. A property that is not set formats to an empty string, so the
command line has no placeholder left over. Conditions are checked like
those of section 4.9.

`control` becomes `ServiceControl` rows. `Wait` is a column of the row, so
when `waitForStart` differs from `wait` the start gets a row of its own
(`scs_<service>`) next to the stop and delete row (`sc_<service>`).
Two components may install the same service, for example twins under
opposite conditions that start it or leave it disabled; the rows of such a
service get generated keys that include the component, so they do not
collide. With
`waitForStart: true`, StartServices waits for `SERVICE_RUNNING`, and a
service that does not get there fails the install with error 1920 and rolls
it back. With `false` the install succeeds and leaves such a service stopped;
`recovery`, if set, restarts it (SVC-04).
`startOnInstall` defaults to `false` for `start: disabled` and may not be
set to `true` there: StartServices cannot start a disabled service and ends
the install with error 1920 (SVC-05).

`deleteOnInstall` (default `false`) adds event 8: DeleteServices removes a
service of the same name that is already registered, for example by an old
installer or by hand, before InstallServices creates the package's own.
Without it InstallServices reconfigures the existing registration: the
binary path and display name become the package's, but a setting the
package does not write, such as a description, stays (SVC-06). With it
nothing of the old registration carries over. After a `/fa` repair the
service was running (SVC-06); whether event 8 ran during the repair was not
checked.

`startCondition` starts the service at install only when the condition is
true. `ServiceControl` has no condition column, so the start row goes to a
generated component (`cmp…`) in the same folder and features, with the
service component's condition and this one as its `Condition`, the
component's `transitive` and `permanent` settings and an HKLM registry value
as key path; together the two conditions must fit in 255 characters. Public
properties in the condition become secure, so a value set in the wizard
reaches the server. SVC-08 showed the start following the property at a
first install. How a repair or a major upgrade treats the generated
component (for example, whether a repair starts the service again) is not
tested. It needs `startOnInstall`. Like a component's `condition`, it
cannot ask for the action state of a feature (`&Feature`) or a component
(`$Component`), and the validator rejects either: component conditions are
evaluated before those states exist (inferred from Microsoft's Component
table and Conditional Statement Syntax pages), and a start condition on
`&StartService=3` did not start the service at a first install (observed,
SVC-09; `$Component` not tested). A property does the job instead, for example one that a
wizard `checkbox` field sets. Installed states (`!Feature`, `?Component`)
stay allowed.

`recovery` becomes a deferred, no-impersonate `sc.exe failure` action after
InstallServices, with its exit code ignored (SVC-01). Each step is `restart`,
`none` or `{restart: <delay>}`; a plain `restart` waits `restartDelay`
(default 60s; `0s` restarts at once). Delays are whole milliseconds and
`resetAfter` is whole seconds, as `sc.exe` takes them (SVC-03); `resetAfter`
and `resetAfterDays` exclude each other. A `password` is added
to `MsiHiddenProperties` when it names a property.

**Services the package does not install** are stopped, started or deleted
with `serviceControl` entries, which become `ServiceControl` rows of the
component (SVC-07). A typical use retires a service an older version
installed under another name:

```yaml
components:
  - files: [{ source: app.exe }]
    serviceControl:
      - name: app-old            # may hold [PROPERTY] references
        stopOnInstall: true
        deleteOnInstall: true
        stopOnUninstall: true
        deleteOnUninstall: true
      - name: Spooler
        stopOnInstall: true
        startOnInstall: true     # starts again after InstallFiles
        wait: false              # default true
```

Each event is off unless set, and an entry needs at least one. Install
events run when the component is installed, uninstall events when it is
removed; repairs were not tested. Stopping and deleting a service that does
not exist is not an error (SVC-07); starting one is not tested. Deleting and
starting a service in the same phase gives a warning, because DeleteServices
runs before StartServices. A name longer than 255 characters is an error. A service the component installs is controlled with
`services[].control` instead: naming any service of the package here is an
error, because a row in another component would stop or delete it when that
component is removed or repaired. A name listed twice in one component is an
error too. A per-user package cannot control services.

**Event log sources** become Registry rows of the component:

```yaml
eventLog:
  - source: windows_exporter
    log: Application
    messageFile: '%SystemRoot%\System32\EventCreate.exe'
    typesSupported: 7
```

They write `EventMessageFile` (`REG_EXPAND_SZ`) and `TypesSupported`
(`REG_DWORD`) under
`HKLM\SYSTEM\CurrentControlSet\Services\EventLog\<log>\<source>`.
A component with only event sources is keyed on its folder like any
component without files or `registry` entries. When a component listed
earlier already has that folder's component code, it derives its code from
its first source instead, so several such components can share a folder;
list them after any other component keyed on the folder.
Nothing else was needed in REG-03: a key there with these two values was a
registered source for `Write-EventLog`, `[EventLog]::SourceExists` and
`Remove-EventLog`, and events showed their text through the generic `%1`
message file `EventCreate.exe`. There is no `customSource` option: a
`CustomSource` value made no difference to any of these in REG-03, and
`New-EventLog` itself does not write it. A package that wants it anyway can
add it as a `registry` entry.

**Environment variables** (section 2.1, finding 4):

```yaml
environment:
  - name: PATH
    value: '[APPLICATIONFOLDER]'
    action: append               # set | setIfAbsent | append | prepend | remove
    scope: machine               # machine | user | auto; see below for the default
    removeOnUninstall: true      # default true (false for remove); written as an explicit "-"
```

`scope` defaults to `user` in a `perUser` package or component, to `auto`
in a component of a dual package without a `scope` of its own, and to
`machine` otherwise. `auto` writes the variable per machine or per user as
the package is installed (section 4.2, "Install scope"; ENVVAR-03); in a
package or component of one context it is the same as that context's
scope.

`append` and `prepend` build the `[~];` form, and a `PATH`-like value without
them gets a warning, since `set` replaces the whole variable. A component may
not list the same variable (names compared without case) twice with the same
action and scope.

**Top-level `files` shorthand.** Not in version 1. Every listed file belongs
to an explicit component, so that its GUID and key path are visible in
review. A whole folder is added with a folder source instead.

#### Adding a folder (folder sources)

A file `source` that ends in `/` (or `\`) is a folder of the build machine. Its
component becomes one component per file below it, so a package does not
list each file of a build output:

```yaml
components:
  - id: App                      # starts the generated ids; default: the folder's name
    directory: INSTALLDIR        # default: the install dir; also a folder path such as bin
    condition: INSTALLAPP        # copied to every generated component
    bitness: auto                # copied, as are permanent, neverOverwrite, preserve,
                                 # sharedDllRefCount and transitive
    files:
      - source: ../dist/         # relative to the YAML file, or absolute; the / makes it a folder
        include: ['**']          # default: every file
        exclude: ['*.pdb', logs/, 'docs/**/*.tmp']
        allowUnversionedExecutable: false   # copied to every file (FILE-01), as are vital,
                                            # checksum, readOnly, hidden, system, permissions
        guids:                   # the component GUID of a file, by its path below the folder
          bin/app.exe: 6d1c0a42-93b8-4e57-a2f1-0c7e5d9b3a61
```

The folder must be the component's only file. Settings that name one file
or one component cannot be copied to many: `guid` (other than `auto`),
`keyPath`, `registry`, `removeRegistry`, `removeFiles`, `removeFolders`, `services`, `serviceControl`, `eventLog`,
`environment`, `purge`, `createFolder` and `folderPermissions` on the
component, and `name`, `id`, `version`, `language` and `shortcuts` on the
file, are errors. Put them on a component of their own and exclude its file
from the folder, or, for a GUID, list the file in `guids`. `include`,
`exclude` and `guids` on a file that is not a folder are errors too, and so is a `firewall` rule whose `component` is a folder
component. The generated components take the component's place in
`components` and in the features that hold it.

Add a separate output folder, not the YAML file's own: a package built next
to the YAML file would be added by the next build. The builder warns when
the folder is the YAML file's folder or one above it.

- **Layout.** Each folder below `source` that holds a file, directly or
  further down, becomes a directory below `directory`; a folder that
  already exists there under the same name (case ignored), from
  `directories` or a folder path, is reused. Folders without a file are
  left out; give a component `createFolder: true` for an empty one. The
  `source` folder itself is not installed, only its content.
- **One component per file.** Every file is the key path of its own
  component (below a profile folder the HKCU value of section 4.4 is) with an `auto` GUID. That GUID comes from the upgrade code,
  the bitness and the install path in lower case (see GUIDs above), not
  from the source path or the generated IDs, so a file that stays at its
  install path keeps its component in every later build, also when the
  source folder moves or the case of the name changes. A file that is gone
  from a later version is expected to leave with the old product on the
  major upgrade, with an early as well as a late `removeOld`, while a file
  that is new gets a new component; FILE-08 confirmed this on a Windows
  CI runner, including a name whose case changed.
- **Kept GUIDs.** `guids` maps the slash path of a file below `source`,
  matched ignoring case, to the GUID its component takes instead of the
  `auto` one. It is for a port from a tool that gave each harvested file a
  component code of its own: with a late `removeOld`, the first upgrade from
  that tool's package deletes every file whose code changed (UPG-11), and a
  kept code keeps the file (UPG-12, a WiX `Files` glob upgraded by the
  converted package). `msi-builder convert --msi` fills it from the old
  package. The other files keep their `auto` GUID, so files added later
  need no entry. A path the folder does not add, because the file is gone
  or a pattern leaves it out, is a warning, since a file may well leave in
  a later version; a path with `\`, a leading `/` or a `.` or `..` folder,
  two paths that differ in case only, a value that is not a GUID and two
  paths with one GUID are errors.
- **IDs.** Component, file and directory IDs are the component's ID (cut to
  50 characters), `_`, a kind letter and 20 hex digits, derived from the
  directory, `source` as written and the path below it, exactly as
  `Config.AddTree` derives them. They are stable between builds of the same
  input; an absolute `source` puts the build machine's path into them, and
  with it into the automatic ProductCode, so prefer a relative one. A major
  upgrade presumably does not depend on them, since
  components are matched by GUID (inferred, not tested). Diagnostics name a
  generated component as `components[0]{bin/app.exe}`. A generated file's
  `source` (also in `builder.Result.Files`) is its path below the folder
  when the folder is read from disk, and its path in `Config.FS`, with the
  folder in front, when that is set.
- **Patterns.** `include` and `exclude` take one pattern or a list, in the
  syntax of [doublestar](https://github.com/bmatcuk/doublestar): `*`, `?`,
  `[a-z]` and `{a,b}` within one name, and a `**` folder for any number of
  folders. A pattern is matched against the slash path below `source`,
  ignoring case, as Windows does. A pattern without a `/` matches a name in
  any folder (`*.pdb`), one with a `/` is anchored at `source`
  (`lib/**/*.dll`), and a trailing `/` matches folders only (`logs/`). A
  pattern that matches a folder covers everything in it. Without `include`
  every file is added; `exclude` wins over `include`. `\`, a leading `/`,
  and `.` or `..` folders are errors.
- **Errors.** A symbolic link or another special file, a name Windows does
  not allow (`a|b.txt`, `nul.txt`, a trailing dot), a missing folder and a
  folder without any file are errors, unless the patterns leave them out.
  So is a file that another component installs to the same path: exclude it
  from the folder to give it a component of its own, for example one with
  `permissions`, `preserve` or a shortcut. `longFileNames: false` cannot be
  combined with a folder source, since its names need not be 8.3 names.
- **32-bit folders.** In an x64 package, a folder source in a 32-bit folder
  such as `ProgramFilesFolder` gets one warning (ICE80) unless its
  component sets `bitness: 32`, which makes every generated component
  32-bit.
- **When.** `msi-builder validate` checks the component but does not read
  the folder; `msi-builder build` and `builder.Build` expand it first and
  then validate the whole package. In Go, `Config.ExpandFolders` expands
  folder sources on its own; a relative `source` is read from the file's
  `FS`, else `Config.FS`, when one is set.

These were considered and not adopted; section 16 of the specification has
the reasons: a separate top-level `harvest` list, a component per folder,
following symbolic links, adding empty folders, leaving out dot files by
default, and matching patterns with case.

### 4.7 `properties`

```yaml
properties:
  LISTEN_PORT: "9182"                  # short form: value only
  ENABLED_COLLECTORS: {}               # declared, no default value
  SERVICE_PASSWORD: { hidden: true }
  MSIRESTARTMANAGERCONTROL: Disable
  myPrivate: { value: x }
```

| Key | Default | Notes |
|---|---|---|
| `value` | none | Written to the Property table; an empty value writes no row. With `search` it is set by an action after `AppSearch` instead (see below) |
| `secure` | `true` for public names, except, in per-machine and dual packages, those a permission grant's account or folder comes from | Listed in `SecureCustomProperties` |
| `hidden` | `false` | Listed in `MsiHiddenProperties` |
| `search` | none | Fills the property from the registry, a folder, a file or an installed component; one search or a list, see below |

Public names have no lower-case letter. `secure: true` on a private name is
an error. The request properties (`ADDLOCAL`, `REMOVE`, `REINSTALL`, …) and
the properties the builder owns (`ProductCode`, `ProductVersion`, `ALLUSERS`,
`SecureCustomProperties`, …) cannot be set here (ICE87).

**Search.** `search` fills a property from the target machine before the
install. It is one search or a list of them; each search has exactly one of
`registry`, `directory`, `file` or `component`:

```yaml
properties:
  NETFX_RELEASE:
    search:
      registry:
        root: HKLM                     # HKLM | HKCU | HKCR | HKU
        key: SOFTWARE\Microsoft\NET Framework Setup\NDP\v4\Full
        name: Release                  # empty: the key's default value
        bitness: 64                    # 32 | 64; default: the package's
        type: value                    # value (default) | directory
  DATA_DIR:
    value: C:\Data                     # used when no search finds anything
    search:
      registry: { root: HKLM, key: SOFTWARE\Example, name: DataDir, type: directory }
  TOOL_EXE:
    search:                            # the first search that finds something wins
      - file:
          name: tool.exe
          under: { registry: { root: HKLM, key: SOFTWARE\Example, name: Home } }
          path: bin                    # relative to the folder under finds
          minVersion: 2.0              # 2.0 or later, in any language
      - file:
          name: tool.exe
          path: '[ProgramFiles64Folder]Example'
          depth: 2                     # also two levels of subfolders below
  LEGACY_DIR:
    search:
      directory: { path: '[ProgramFilesFolder]Example Legacy' }
  OTHER_APP_DIR:
    search:
      component: { guid: 6A0D3C2E-5B1F-4E7A-9C3D-2F8E1B4A7C60 }
```

`registry` with `type: value` returns the registry data the way the engine
formats it: a `REG_SZ` as it is (brackets stay literal), a `REG_DWORD` as
`#` and the number, a `REG_EXPAND_SZ` already expanded (observed, SEARCH-04;
Microsoft documents a `#%` prefix that the engine does not add). Conditions
compare such a value as a string: `NETFX_RELEASE >= 394802` is false and
`NETFX_RELEASE >= "#394802"` only works while both numbers have the same
number of digits (SEARCH-04). Do not search a `REG_MULTI_SZ` value: the
property starts and ends with a NUL, which is reported to break the full UI
(specification section 8.7). `directory` returns the folder the data names
with a trailing backslash, and nothing when it names a file or a missing
folder (SEARCH-04).

| Search | Key | Notes |
|---|---|---|
| `directory` | `path` | A full path (a drive, a share or a property in brackets such as `[ProgramFiles64Folder]`), or with `under` a path relative to the folder that search finds. Returns the folder with a trailing backslash, and nothing for a file or a missing folder |
| | `under` | A search that finds a folder: `registry` (its `type` defaults to `directory` there, `value` is an error), `directory` or `component`. Up to eight searches in one chain |
| | `depth` | Default 0. How many folders may lie between the path's parent and its last folder: `C:\a\b` with depth 2 also finds `C:\a\x\y\b` |
| `file` | `name` | The filename, without a folder. The result is the folder plus `name` as written here, in this case, not the case on disk. A name that is no valid 8.3 name is written with a generated short name (`short\|long`); the long name is the one matched and returned (SEARCH-05) |
| | `path`, `under` | The folder to look in, as for `directory` |
| | `depth` | Default 0. How many levels of subfolders below the folder are searched as well |
| | `minVersion`, `maxVersion` | Inclusive bounds, one to four fields. A `minVersion` rules out files without a version (SEARCH-05) |
| | `minSize`, `maxSize` | Inclusive bounds in bytes, up to 2147483647 |
| | `minDate`, `maxDate` | Inclusive bounds on the modification date, `YYYY-MM-DD` from 1980-01-01 to 2043-12-31: `minDate` starts at 00:00:00 and `maxDate` ends at 23:59:58, since the engine compares the time of day too (SEARCH-05). Whether it compares local time or UTC is not tested |
| | `languages` | Language IDs the file's version resource must list; compared only when the file's version equals `minVersion` |
| `component` | `guid` | The component code of an installed component, of this or another product. Returns the folder of its key file, or the folder itself for a component keyed on its folder (SEARCH-08) |
| | `keyPath` | `file` (default) or `directory`: what the component's key path is. The engine returns the folder's parent for a folder key path searched as `file` (SEARCH-08), and nothing for a file key path searched as `directory` (SEARCH-05), so it must match. For a component of this package the builder sets it from the component, and a `keyPath` that contradicts it is an error. A component of this package keyed on a registry value (also one with only shortcuts) gets a warning, and `keyPath` on it is an error: the search found nothing for an HKLM registry key path with either type (SEARCH-08; HKCU not tested) |

The engine finds a file whose version equals `MinVersion` only when
`Languages` lists the file's languages, even for a language-neutral file
(observed, SEARCH-05). Without `languages` the builder therefore writes
`minVersion` lowered by one in its last field (`2.0` becomes
`1.65535.65535.65535`), so `minVersion` means "this version or later" for
every file; the one difference is a file of exactly that lower version
without any language, which is found too. With `languages` the bound is
written as given. Versions of Windows' own files are unreliable to a search
(a `minVersion` search for `ucrtbase.dll` in `System64Folder` at its real
version found nothing, while the same search for a copy in another folder
found it, consistent with the reported compatibility shim; SEARCH-02), so the builder warns about version bounds in
`[SystemFolder]`, `[System64Folder]`, `[WindowsFolder]` and `C:\Windows`;
search the registry value `CurrentBuildNumber` under
`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion` instead and compare it
with a number in a condition (`OS_BUILD >= 22000`): `VersionNT` and
`WindowsBuild` stop at Windows 8.1, 603 and 9600, also on Windows Server
2025 (MSISPEC PROP-02). A path that names a folder of the
package itself (`[INSTALLDIR]`) gets a warning: `AppSearch` runs before
`CostFinalize` resolves those folders. A path that names a searched
property is an error, since its result is set only after `AppSearch`; use
`under` instead. A path that starts with another property that is not a
standard folder gets a warning: if that property is empty, the path is
relative and the engine searches every fixed drive. The standard folder
properties are set before `AppSearch` (`[WindowsFolder]` and
`[ProgramFiles64Folder]` work, SEARCH-05). A component search finds components
whose key path is a file (SEARCH-05) and, with the locator type of a folder
key path, those keyed on their folder (`keyPath`, SEARCH-08); it found nothing
for an HKLM registry key path (SEARCH-08; HKCU not tested).

The precedence is: a value from the command line, then the first search
that finds something, then `value`. Search *n* fills `<NAME>_FOUND`
(`<NAME>_FOUND2`, `<NAME>_FOUND3`, … for the later ones; public, so ICE52
stays quiet) through an `AppSearch` row, and immediate type-51 actions after
`AppSearch` in both sequences copy the results in order: `SearchApply_<NAME>`
(`SearchApply2_<NAME>`, …) copies its result when the property is still
empty, and `SearchDefault_<NAME>` sets `value` when it is still empty after
all of them. A custom action can name these actions in `before` or `after`
to run once a result is applied (only for names short enough to be used as
written; a longer name gets a generated identifier). A search straight into the property would override a
command-line value (SEARCH-03), and several `AppSearch` rows for one
property leave the result of the last row that finds something, in an order
the package does not control (SEARCH-05). `value` therefore has no
`Property` row; it is written as a literal, with `[`, `]`, `{` and `}`
escaped. A searched property must be public and secure, because after a UI
sequence the engine does not run `AppSearch` again in the execute sequence
(Microsoft documents this; not tested) and only secure properties reach it.
The `<NAME>_FOUND` properties cannot be defined in `properties`, and
`installDir.remember` already searches the install dir. Searches of `.ini`
files (`IniLocator`) and of all fixed drives are not offered; the
specification's "Decisions not adopted" says why.

### 4.8 `launchConditions`

```yaml
launchConditions:
  - condition: Installed OR VersionNT64
    message: This package requires 64-bit Windows.
```

Conditions are parsed with the grammar of specification section 8.8 at build
time, because a launch condition that does not parse is ignored by the
engine.

The condition is the table's primary key, so two entries with the same
condition are an error, and so is `NOT <upgrade.newerProperty>` while
`upgrade.downgrade` is `error`, which writes that launch condition itself:
set `upgrade.downgradeMessage` instead.

### 4.9 `customActions`

```yaml
customActions:
  - id: CheckExtraFlags
    error: The parameter '--config.file' must not be included in EXTRA_FLAGS.
    schedule:
      - sequence: execute
        before: InstallInitialize
        condition: EXTRA_FLAGS AND (EXTRA_FLAGS >< "--config.file")

  - id: SetCollectorsFlag
    setProperty: { name: CollectorsFlag, value: '--collectors.enabled [ENABLED_COLLECTORS]' }
    schedule:
      - { sequence: execute, after: InstallFiles, condition: ENABLED_COLLECTORS }

  - id: CreateConfigFile
    run:
      command: '"[%ComSpec]" /c TYPE NUL >>"[ConfigFile_Default]"'
      directory: APPLICATIONFOLDER   # working directory, optional
    execute: deferred                # immediate | deferred | rollback | commit
    impersonate: false
    return: check                    # check | ignore | asyncWait | asyncNoWait
    schedule:
      - { sequence: execute, before: InstallServices, condition: ConfigFile_Default AND NOT REMOVE~="ALL" }
```

Kinds, exactly one per action:

| Key | Type | Notes |
|---|---|---|
| `setProperty` | 51 | Never deferred |
| `setDirectory` | 35 | After CostFinalize |
| `error` | 19 | Text or an `Error` table number |
| `exec` | 34 | `command` is a formatted command line; deferred commands are formatted when the script is written, so no CustomActionData is needed. The executable must be quoted when it comes from a property or folder reference (`"[#tool.exe]" --init`): an unquoted path with a space is tried piece by piece by `CreateProcess`. A property alone (`[OLD_UNINSTALL] /S`) is a warning, since an `UninstallString` is usually quoted already; a directory key alone stays an error |
| `execProperty` | 50 | `property` holds the executable, `arguments` the rest |
| `run` | 1 + 51 | `command` and an optional `directory`; see below |
| `dll` | 1 | `binary` (a `binaries` key) and `entry` |
| `exe` | 2 | `binary` (a `binaries` key holding a program) and optional `arguments`; see below |

**`run`.** The engine runs `exec` and `execProperty` commands with a visible
console window and keeps none of their output (specification section
17.1). `run` instead calls the custom action DLL that msi-builder embeds
(`internal/cadll`, written into the `Binary` table once as `BuilderRun`, 25 to
31 KB for the package's platform): it starts the command with no window,
copies every line it writes to stdout or stderr into the verbose log as
`BuilderRun: <line>`, logs the exit code and returns success for 0 and
`ERROR_INSTALL_FAILURE` otherwise, which `return: ignore` turns into a
warning. The command line is formatted when the action is scheduled, like
`exec`; a type-51 action named `<id>.Data` passes `<directory>|<command>`
through `CustomActionData` to deferred, rollback and commit actions and
through the `BuilderRun` property to immediate ones. `hideTarget` hides the
command in both places. The command runs in a job object: a cancel ends it
with everything it started, and so does the end of the action, so a
command cannot leave a background process behind. `return` is required (`check` or `ignore`;
`asyncWait` is allowed, `asyncNoWait` is not). The executable must be
quoted when it comes from a property or folder reference, as for `exec`
(a property alone is a warning, as there);
`directory` is the working directory only, not where a bare program name
is looked up. Impersonation follows
`impersonate`: the DLL starts the process with the impersonated user's
token when there is one. The key `BuilderRun` is reserved in `binaries`.
Output is decoded as UTF-8 when it is valid UTF-8 and in the OEM code page
otherwise, which covers Go programs, cmd.exe and PowerShell. Specification
section 17.6 and case CA-03 (Appendix C) hold the Windows evidence.

**`exe`.** The engine writes the program from the `Binary` table to a
temporary file, runs it with `arguments` and deletes it (CA-05). Like `exec`,
the arguments of a deferred action are formatted when the script is written,
so no CustomActionData is needed. Unlike an `exec` of an installed file, it
works before `InstallFiles` and after `RemoveFiles`, for example a
prerequisite installer or a cleanup tool that is not part of the product.
Like `exec`, it shows a console program's window and keeps none of its
output, so pass the program's own silent and log switches. `return` is
required, and any exit code but 0 fails the install unless it is `ignore`,
including 3010 (CA-05). A program that installs a Windows Installer package
itself is reported to fail with 1618 while this install runs its execute
sequence, because the engine runs one installation at a time (section 8.11;
not tested). The builder rejects a binary that is a Windows Installer
package, a DLL or no Windows program. How long the engine keeps the
temporary file of an `asyncNoWait` action is not tested.

Options: `execute` (default `immediate`), `impersonate` (default `true`;
`false` needs a deferred action, and in a dual package `scope: perMachine`),
`scope` (`perMachine` or `perUser`, dual packages only, see "Install
scope"), `return` (required for `exec`,
`execProperty`, `exe` and `run`, default `check` otherwise), `hideTarget`, `once`
(`firstSequence`, `oncePerProcess`, `clientRepeat`), `progress`.

**Progress text.** `progress` names what the action does while it runs:

```yaml
customActions:
  - id: ImportData
    run: { command: '"[#hello.exe]" import' }
    execute: deferred
    impersonate: false
    return: check
    progress: Importing the existing data
    schedule:
      - { sequence: execute, after: InstallFiles, when: install }
```

The builder writes it to the `ActionText` table, without a template.
The text is formatted, so `[ProductName]` and other properties work: the
wizard's progress page shows a deferred action's text while it runs (an
immediate action's was not watched there, and one in the `ui` sequence
runs before the progress page; basic UI is documented to show it, not
tested), and the verbose
log writes it as `Action hh:mm:ss: <id>. <text>` with the full UI (basic
UI not tested), and
for a deferred action as the script's `ActionStart` line also under `/qn`
(CA-08). A deferred action's text is formatted when the script is written,
so it shows property values from that moment.
A `ui.languages` entry translates it with `progress: {<id>: text}`. A
blank text is an error, and one longer than 64 characters, the width of
`ActionText.Description`, is a warning (ICE03). Standard actions keep the
engine's own texts, and the actions msi-builder generates (firewall rules,
grants, service arguments) get none; specification section 17.8 records
why, and why there is no `Template`.

**Schedule.** A list of placements, one per sequence table: `execute`, `ui`,
`adminExecute`, `adminUI`. Each has exactly one of `before`, `after` or
`at` (an absolute number) and an optional `condition`. Actions that share an
anchor keep their order in the file: the builder expands every chain
next to its anchor (specification section 11.7) and fails, rather than
shifts, when a chain runs into another fixed number. After numbering it
checks deferred actions against InstallInitialize … InstallFinalize (ICE77)
and RemoveExistingProducts against the windows of section 4.3.

`when` names the case a placement runs in, ANDed with its `condition`:

| `when` | Condition |
|---|---|
| `install` | `NOT Installed AND NOT <upgrade properties>` (`NOT Installed` with `upgrade: false`) |
| `upgrade` | `NOT Installed AND <upgrade properties>`; needs `upgrade` |
| `uninstall` | `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE` |
| `maintenance` | `Installed AND NOT REMOVE~="ALL"`: repair, change, and also patches and minor upgrades |

```yaml
schedule:
  - { sequence: execute, before: RemoveFiles, when: uninstall, condition: DELETE_DATA="1" }
```

The upgrade properties are `upgrade.property` and the `property` of every
`upgrade.related` row without `onlyDetect`, ORed. `when` is allowed in the
`execute` sequence, and `install` and `upgrade` also in `ui`; the
administrative sequences have none of these properties. The builder warns
when `install` or `upgrade` runs before FindRelatedProducts, which sets the
upgrade properties, and when `uninstall` or `maintenance` runs before
InstallValidate, since REMOVE is reliable only after it (specification
section 8.8).

FILE-03 tested the `uninstall` condition on a deferred execute action near
InstallFinalize: true on `msiexec /x`, false while a major upgrade with
RemoveExistingProducts at 1401 removed the old product. Other `removeOld`
windows, a removal from the maintenance dialog and the other three
conditions follow from the Installed, REMOVE and upgrade properties and
were not tested.

Standard actions are added automatically for the tables a package uses. An
action used only as an anchor is added at its default number.

**Restart.** `scheduleReboot` asks for a restart at the end of the
installation, after Windows features, drivers or system files changed that
only take effect then:

```yaml
scheduleReboot:
  when: install                  # optional: install | upgrade | uninstall | maintenance
  condition: NOT NO_REBOOT       # optional, ANDed with when
```

The builder schedules the `ScheduleReboot` standard action after
`InstallFinalize` with that condition; `scheduleReboot: {}` asks every time
the execute sequence runs, uninstall and repair included. With a UI the
engine asks the user to restart at the end. With `/norestart` or
`REBOOT=ReallySuppress` it does not restart and returns 3010
(ERROR_SUCCESS_REBOOT_REQUIRED), which deployment tools treat as success
(`TestMsiexecScheduleReboot`). A quiet install (`/qn`) without one of them
may restart the machine without asking, so unattended deployments pass
`/norestart`. When `scheduleReboot` is set, execute-sequence actions can use
`ScheduleReboot` as an anchor. `ForceReboot`, which restarts in the middle
of the installation and resumes it afterwards, is not offered: a simple
package has nothing that needs it.

### 4.10 `icons` and `binaries`

```yaml
icons:
  icon.ico: installer/icon.ico
binaries:
  helper.dll: build/helper.dll
```

Keys are stream names, limited by the 62-character stream limit (section
2.3). Icons are for `product.arp.icon` and shortcut `icon`s; binaries are
for `dll` and `exe` custom actions and for the UI's bitmaps.

### 4.11 `firewall` helper

```yaml
firewall:
  - name: windows_exporter
    description: windows_exporter HTTP endpoint
    component: FirewallException      # the rule follows this component's state
    program: '[#windows_exporter.exe]'
    protocol: tcp
    port: '[LISTEN_PORT]'
    remoteAddresses: '[REMOTE_ADDR]'  # empty means any
    profiles: all                     # all | domain | private | public, or a list
```

The builder writes deferred, no-impersonate `netsh advfirewall` actions: on
install and reinstall it deletes by name and program, then adds, with a
rollback that deletes; on removal it deletes, with a rollback that adds. The
`component` is usually an empty component with its own GUID, in the feature
that switches the rule.

`remoteAddresses` is passed to netsh's `remoteip=` unquoted, so it has no
spaces or double quotes. A value with property references goes through a
property that falls back to `any` when any property it references is empty
at runtime. `any` and `*` mean any address and cannot be combined with other
entries: netsh rejects `any` before or after an address and `*` before one
and, since the add action has no Continue flag, the install fails (MSISPEC
CA-02), so the build reports an error. `*` after an address is rejected as
well, although that case is not tested. Keywords such as `localsubnet` combine with addresses.
Neither the name nor the description may contain a double quote; netsh drops
quotes inside the quoted description (MSISPEC CA-04). The name is literal
text without `[` or `]`, and `all` in any case is an error: netsh treats
`name=all` as every rule, so the delete that runs before each add would
remove every inbound rule on the machine, and a property in the name could
resolve to `all` at runtime (MSISPEC section 17.3, CA-06: on a CI runner the
unscoped delete removed all 208 inbound rules, 206 of them the runner's and
two the probe's, and no quoting escapes it).

A rule that a feature switches can be written under that feature instead,
without `component`:

```yaml
features:
  - id: FirewallException
    title: Firewall Exception
    default: off
    firewall:
      - name: windows_exporter
        program: '[#windows_exporter.exe]'
        protocol: tcp
        port: '[LISTEN_PORT]'
```

The loader adds an empty component `FirewallException_Firewall` to the
feature and points the rules at it. The component goes in the feature's
`directory`, else in the install dir, else beside one of the feature's
components, else in the first configurable directory, both outside the user
profile; only a tree with none of these gets the install dir added for it. Its GUID comes from the
upgrade code, the bitness and the feature ID, so it does not collide with
other components keyed on the install dir. Errors name the rule's place
under the feature, such as `features[1].firewall[0].program`.

### 4.12 `ui`

```yaml
ui:
  enabled: true                     # the setup wizard; default false (basic UI)
  chooseFeatures: true              # adds the feature tree page
  dialogs:
    - id: ConfigDlg
      title: windows_exporter configuration
      description: Configuration of the windows_exporter service
      after: CustomizeDlg            # inserted into the wizard's Next/Back flow
      fields:
        - property: ENABLED_COLLECTORS
          label: Comma-separated list of collectors to use. Use '[\[]defaults[\]]' for the defaults.
        - property: LISTEN_PORT
          label: Port to listen on
        - property: API_TOKEN
          label: API token
          type: password              # text (default) | password | checkbox | choice
        - property: DELETE_DATA
          label: '&Delete the data folder on uninstall'
          type: checkbox
          value: "1"                  # what a checked box sets, default 1
        - property: LOG_LEVEL
          label: Log level
          type: choice
          options:                    # a value, or a value with a label
            - value: warn
              label: '&Warnings'
            - info                    # shown as "info"
            - value: debug
              label: '&Debug'
```

The wizards are written from scratch for msi-builder, including the bitmaps,
which the `artwork` package draws from a colour palette at build time.
`enabled: true` shows the wizard: welcome, ready, progress and finish;
`chooseFeatures` adds the feature tree (`CustomizeDlg`) with Browse and disk
usage. Browse changes the directory of the selected feature
(`features[].directory`); the generated `MainFeature` has the install dir.
Every wizard includes maintenance (Repair and Remove; Change with the
feature tree), cancel, user exit, fatal error, files in use, Restart Manager
and the error dialog. Added dialogs are laid out automatically: one label
and one edit field per property, Back, Next and Cancel. The fields of one
dialog must fit in 160 dialog units (four text fields); the error names the
height they need. A `password` field is an edit field with the Password
attribute (0x200000), and its property is added to `MsiHiddenProperties`. A
`checkbox` field is a checkbox with the label as its text, in the space of a
label and an edit field; its `CheckBox` row holds `value`. Two checkboxes on
one property must set the same value. Microsoft's documentation reports that
a checked box sets the property to `value`, that a box starts checked when
its property has any value, and that the password characters are masked;
none of this, what an unchecked box leaves in the property, or whether the
password stays out of the log was tested on Windows. The validator warns
when a checkbox property has a default in `properties`, since the box would
start checked. An `&` in a checkbox label marks the access key, as in
`&Delete`; write `&&` for a literal `&`. Text and password labels show `&`
as it is. A `choice` field is a label above a `RadioButtonGroup` with one
radio button per option, 16 dialog units each. It needs at least two
options; values are unique, at most 64 characters and contain no `[ ] { }`,
labels at most 64 characters (default: the value, shown literally; in a
label `&` marks the access key). The property's default in `properties` must
be one of the values; without one the first option becomes the default,
since ICE34 requires a group's property to match a button. A choice on a
searched property is an error for the same reason, and so is a choice on a
property that another field binds too. In the full UI the group starts on
the default and selecting another option sets the property on the server
(UI-05, observed on Windows). `after` names the page the dialog follows:
`WelcomeDlg`, `LicenseAgreementDlg`, `InstallScopeDlg`, `InstallDirDlg`,
`CustomizeDlg` (each when the wizard has it) or an added dialog. With `enabled: false` (the
default) no UI tables are written and msiexec shows its basic progress
window; silent installs never depend on the UI in any mode. Without
`chooseFeatures` and without remove dialogs `ARPNOMODIFY` is set, whatever
`product.arp.noModify` says: such a package has no page that Change could
show, so `noModify: false` does not bring the button back. A feature tree
page without the wizard is an error.

#### Remove dialogs

```yaml
properties:
  DELETE_CONFIGURATION: {}           # secure, so the purge on the server sees it

components:
  - directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    purge:
      condition: DELETE_CONFIGURATION="1"

ui:
  enabled: true
  dialogs:
    - id: RemoveOptionsDlg
      flow: remove                   # install (default) | remove
      title: Remove options
      fields:
        - property: DELETE_CONFIGURATION
          type: checkbox
          label: '&Delete the configuration'
```

`flow: remove` puts the dialog on the maintenance menu's remove path:
Remove in `MaintenanceTypeDlg` opens it, and its Next goes to
`VerifyRemoveDlg`, the page that confirms the removal. `after` defaults to
`MaintenanceTypeDlg` and may name another remove dialog; install pages and
remove pages cannot follow each other. The fields are the same as on install
pages. A package with remove dialogs keeps `ARPNOMODIFY` unset, so Add or
Remove Programs is expected to offer Change (unverified); its `ModifyPath` command,
`MsiExec.exe /I{ProductCode}`, opens the maintenance menu (UI-10; which
command the button runs is unverified); with `product.arp.noModify: true` the
validator warns that only running the package again shows the page, and with
`product.arp.noRemove: true`, which disables Remove in the menu, that no one
can reach it.
`msiexec /x` asks for confirmation and removes the product without the
wizard (spec section 7.7, UI-10; what the Uninstall button shows is
unverified), so the property is also the command-line switch:
`msiexec /x hello.msi DELETE_CONFIGURATION=1`.

#### License page

```yaml
ui:
  enabled: true
  chooseFeatures: true
  license: LICENSE.rtf     # RTF as is, any other file is text converted to RTF
```

`license` adds `LicenseAgreementDlg` after the welcome page: the license in a
ScrollableText control, an "I accept" checkbox bound to `LicenseAccepted`,
Print (`MsiPrint`) and Back, Next and Cancel. Next is disabled until the box
is checked. A file that starts with `{\rtf` is embedded as it is, after two
checks: it must be 7-bit ASCII, because the Control table stores text in the
package code page and RTF writes other characters as `\'xx` or `\u`
escapes, and its braces must balance. Any other file is UTF-8 text (or
UTF-16 with a byte order mark) and is converted to RTF in Courier New 8 pt,
which keeps the layout of hand-wrapped license files; every non-ASCII
character becomes a `\u` escape. Change in the maintenance menu skips the
license page. `ui.dialogs` can follow it with `after: LicenseAgreementDlg`;
`after: WelcomeDlg` puts a dialog before it.

#### Install folder page

```yaml
ui:
  enabled: true
  chooseDirectory: true     # needs installDir
```

`chooseDirectory` adds `InstallDirDlg` after the welcome and license pages:
the install folder (`installDir.id`) in a PathEdit control and Change,
which opens the folder browser (`BrowseDlg`) on the same folder. Next sets
the typed path as the folder's target, so the engine checks it before the
next page. Like the license page, an installed product's Change and Back
step over it, since an installed product keeps its folder. `ui.dialogs`
can follow it with `after: InstallDirDlg`.

#### Scope page

A package with a dual `package.scope` gets `InstallScopeDlg` after the
welcome and license pages, before the install folder page: "Install just
for you" and "Install for all users of this computer", preselected with the
context the package opened in (the scope's default, or the command line's
`MSIINSTALLPERUSER`). Next sets `ALLUSERS=2` and `MSIINSTALLPERUSER`, and
the ready page then shows Install with the UAC shield for all users. The
client keeps the folders it resolved for the context it opened in, so Next
also sets every public folder under Program Files or Common Files (such as
the install folder) to its path in the chosen context, unless it holds
another path from the command line or `installDir.remember`. Installed
products step over the page, since they keep their context. Its texts are
the `ui.strings` keys `InstallScopeTitle`, `InstallScopeText`,
`InstallScopeUser`, `InstallScopeMachine` and `InstallScopeHint`, translated
like the other pages. The page's property is `BuilderInstallScope`; the
builder writes it and `MSIINSTALLPERUSER`, which it makes secure so that a
standard user's choice reaches the elevated server, and `properties` cannot
define either.

#### Launch checkbox

```yaml
ui:
  enabled: true
  launch:
    file: viewer.exe       # files[].id, or the file name when it has no id
    arguments: --welcome   # formatted, optional
    text: "Start Example Viewer now"   # default "&Launch [ProductName]"
    checked: false         # default true
```

`launch` adds a checkbox (`LaunchOnFinish`) to the finish page, on the
bottom strip left of the buttons: Windows paints the page's background
picture over an opaque control placed on it (verified on Windows Server
2025). In `text`, `&` marks the access key. When it is
checked, Finish runs the custom action `LaunchOnFinish`, a type 34 action
that starts `"[#file]" arguments` in the file's folder and does not wait
for it (226). It runs in the setup's own process, so the program runs as
the user who started the setup. The box is shown, and the action runs, only
after a new installation that installed the file's component
(`NOT Installed AND NOT ACTION = "ADMIN" AND $component = 3`); silent
installs never show the finish page, so they never start the program. `file`
must be a program, a PE file named `.exe` or `.com`: process creation
cannot start a document or a library.
When `launch` is set, the custom action name and the property
`LaunchOnFinish` are reserved.

#### Finish page text

```yaml
ui:
  enabled: true
  finishText: Open a new command prompt to use [ProductName].
```

`finishText` adds a paragraph (`FinishText`) to the finish page (`ExitDlg`)
below its standard text, for advice the user needs right after the
installation, such as opening a new shell to pick up a changed `PATH`. It is
formatted, so `[Property]` references resolve and any other `[...]` or
`{...}` is formatted syntax too: Microsoft documents `[\[]` and `[\]]` for
literal brackets (not tested). The control has NoPrefix, so `&` is shown
as it is. The standard text keeps the first 40 dialog units below the title
and the paragraph gets the rest of the page above the bottom strip, 220×89
units (about nine lines of the default font, not observed). The control is
hidden by `Installed OR ACTION = "ADMIN"`, so that a finish page after a
repair does not repeat advice about the installation. UI-07 observed the
resolved text after a full UI install and no paragraph after a repair
through the maintenance menu. That a major upgrade shows it, and that
Change, Remove, a patch, a minor upgrade or an administrative install hide
it, follows from the documented `Installed` and `ACTION` properties and was
not tested. Whether `Installed` is set when a suspended first installation
resumes, which would hide the paragraph, is a guess and was not tested
either. The pages where the setup was cancelled or failed keep their text,
and a silent install shows no finish page. A Text control wraps long text
(not observed for this one); leading and trailing whitespace is dropped, a
blank text is an error, and how a line break inside the text renders was not
tested. A NoPrefix text showing `&` literally is from UI-01.

#### Theme

```yaml
ui:
  theme:
    banner: banner.bmp     # top of the inner pages, 493×58 pixels
    dialog: dialog.bmp     # background of the welcome and finish pages, 493×312 pixels
    colors:                # the built-in pictures, #RRGGBB
      dark: "#122B4A"
      light: "#1E8C9E"
      accent: "#F2A93B"
      paper: "#F4F6F8"
```

| Picture | Control | Pixels at 96 dpi | Default |
|---|---|---|---|
| `banner` | `BannerBmp`, 370×44 dialog units at the top of every inner page; the title and the two-line description are drawn over x 15 to 325, so a logo belongs in the right 45 units (60 pixels) | 493×58 | paper with an accent line and a mark at the right |
| `dialog` | `DialogBmp`, 370×234 dialog units, the whole welcome, resume and finish pages above the buttons; the title and text are drawn over it from 135 units on, so the picture's own art stays in the left 130 units | 493×312 | the side panel gradient in the left 123 units, paper to the right |

These are the sizes that other installer toolkits use for the same two
pictures, so existing artwork fits unchanged. A picture is BMP (any header
version, 1 to 32 bits, uncompressed or bit fields), PNG or JPEG, chosen by
its content rather than its filename. The builder decodes it, blends
transparent pixels onto `paper`, and always writes an uncompressed 24-bit
bitmap with a BITMAPINFOHEADER, the form every Windows Installer version
shows (newer engines also read PNG, older ones only BMP and JPEG, section
19.1 of the specification, UI-04). The Bitmap control stretches a picture
to its rectangle, so other sizes work as long as the ratio is kept: a ratio
more than 5 % away from the control's is an error, a picture smaller than
the size above is a warning, and a side above 8192 pixels or more than
4096×4096 pixels in all is an error.
`colors` recolours the built-in pictures and the error dialog's icon (drawn
in `accent`); a colour that is not given keeps its default. A license,
theme, install folder page (`chooseDirectory`), finish page text or launch
checkbox without a wizard is an error.

#### Wizard texts and languages

```yaml
product:
  language: 1031           # LCID of the package (Template, ProductLanguage)
ui:
  enabled: true
  strings:
    Next: '&Weiter >'
    Back: '< &Zurück'
    Cancel: Abbrechen
    WelcomeTitle: 'Willkommen beim Setup-Assistenten für [ProductName]'
```

The wizard's built-in texts are English, with built-in translations for
the languages below. `strings` replaces any of them by key; a key it leaves
out keeps the built-in text of `product.language`. The keys are listed in
`pkg/config/schema.json` and returned by `config.UIStrings`, together with
the English texts: the shared buttons (`Back`, `Next`, `Cancel`, `Install`,
`Finish`, `OK`, ...), `DialogTitle`, a `Title` and `Text` per page
(`WelcomeTitle`, `ReadyInstallText`, `ExitTitle`, ...), the other labels of
a page under its prefix (`LicenseAccept`, `BrowseLookIn`, `ProgressStatus`,
...), the default `Launch` caption (`ui.launch.text` still wins), and the
`UIText` rows that the feature tree, the disk usage list and the progress
page read (`MenuLocal`, `SelLocalLocal`, `VolumeCostSize`, `KB`, ...).
Texts other than those `UIText` rows are formatted, so `[ProductName]`
works and any other `[name]` is read as a property; `UIText` is documented
as plain text (unverified). On buttons, checkboxes (`LicenseAccept`), the
Restart Manager radio buttons (`RMUseRM`, `RMDontUseRM`, which have none in
English) and the browse labels (`BrowseLookIn`, `BrowseFolderName`) `&`
marks the access key, and `&&` writes a literal `&`; the other labels and
page texts show `&` as it is. Quote a value that starts with `&`, `<`, `[` or
`{`: unquoted, YAML reads `&Weiter >` as an anchor and the value as empty.
An unknown key, an empty text and `strings` without a wizard are errors; a
text longer than its column (`DialogTitle` 128, `RMUseRM` and `RMDontUseRM`
64, `UIText` rows 255 characters) is a warning, since ICE03 reports it.
`ui.dialogs` and `ui.launch.text` take their texts from the
configuration; a `languages` entry translates them with `dialogs` and
`launch` (below), and the buttons of a `ui.dialogs` page follow the
language on their own.

Messages that the engine itself shows (error texts and the progress texts
of standard actions) come from the engine in the language of
`ProductLanguage`, since msi-builder writes no `Error` table and
`ActionText` rows only for `customActions[].progress` (UI-06). Full-UI
installs of a German package (code page 1252, language 1031) and of a
Russian and Japanese one (code page 65001) passed on Windows Server 2025
(UI-06).

A multi-language product is one MSI with an embedded language transform per
further language (spec section 7.6):

```yaml
product:
  language: 1033           # base language, in the database itself
ui:
  enabled: true
  finishText: Thanks for installing [ProductName].
  languages:
    de: {}                 # or 1031: the built-in German texts
    fr-CA:                 # or 3084
      finishText: Merci d'avoir installé [ProductName].
    ja:
      license: LICENSE.ja.rtf
      strings:
        WelcomeTitle: '[ProductName] へようこそ'
```

The texts the configuration itself brings, the pages of `ui.dialogs` and
the label of `ui.launch`, are translated in the same entry:

```yaml
ui:
  enabled: true
  launch: {file: app.exe, text: '&Open [ProductName]'}
  dialogs:
    - id: ServerDlg
      title: Server connection
      description: Where the program reads its data.
      after: WelcomeDlg
      fields:
        - {property: SERVER_URL, label: 'Server address:'}
        - property: LOG_LEVEL
          label: 'Log level:'
          type: choice
          options: [{value: warn, label: '&Warnings'}, {value: debug, label: '&Debug'}]
  languages:
    de:
      launch: {text: '[ProductName] &öffnen'}
      dialogs:
        ServerDlg:              # a ui.dialogs id
          title: Serververbindung
          description: Woher die Anwendung ihre Daten liest.
          fields:
            SERVER_URL: {label: 'Serveradresse:'}   # the property a field binds
            LOG_LEVEL:
              label: 'Protokollstufe:'
              options: {warn: '&Warnungen', debug: '&Ausführlich'}   # by option value
```

`dialogs` is keyed by the page's `id`, its `fields` by the property a field
binds and a choice field's `options` by option value, so the entry does not
repeat the page layout or depend on the order of the fields. Every text is
optional: one left out keeps the base language's. A key that names no page,
no field of the page or no option of the field is an error, and so is a
field key whose property two fields of the page bind, `options` on a field
that is not a choice, an option label longer than 64 characters,
`launch` without `ui.launch`, and a text that is only blanks. Values,
properties, conditions and the page order stay those of `ui.dialogs`; only
texts change. The texts are formatted and `&` marks the access key on the
same controls as in `ui.dialogs` (checkboxes and radio buttons). LANG-04
runs a German transform's translated page through the full UI and checks
that the option chosen by its translated label sets its value; it passed
on the CI runner.

A `languages` key is a decimal LCID or a language tag: a full tag such as
`de-AT` (3079) or `pt-PT` (2070) names that locale, and a short tag such as
`de` (1031), `es` (3082) or `hi` (1081) the language's main locale. `pt`,
`sr` and `zh` have no short tag, because their spellings or scripts differ
(`pt-BR`/`pt-PT`, `zh-CN`/`zh-TW`); `product.language` stays a decimal
LCID. An entry may set `strings`, `finishText`, `license`, `launch`,
`dialogs` and `progress`, which replace `ui.strings`, `ui.finishText`,
`ui.license`, `ui.launch.text`, the texts of `ui.dialogs` and the
`progress` texts of `customActions` (keyed by action ID; an ID without a
base text is an error) in that language; a field it
leaves out keeps the base language's value, and a text key it leaves out
takes the built-in translation, and without one the base language's text.
Two keys for one LCID, or an entry for `product.language`, are errors. A
problem in an entry's `strings`, `finishText` or `license` is reported
under the entry's key as written, such as `ui.languages.fr.license`; a text
that the code page cannot hold is reported under `package.codepage`.

msi-builder ships its own translations (not taken from another toolset) of
every text for the languages WixUI also covers: ar-SA, bg-BG, ca-ES, cs-CZ,
da-DK, de-DE, el-GR, es-ES, et-EE, fi-FI, fr-FR, he-IL, hi-IN, hr-HR,
hu-HU, it-IT, ja-JP, kk-KZ, ko-KR, lt-LT, lv-LV, nb-NO, nl-NL, pl-PL,
pt-BR, pt-PT, ro-RO, ru-RU, sk-SK, sl-SI, sr-Latn-CS, sv-SE, th-TH, tr-TR,
uk-UA, zh-CN, zh-HK and zh-TW, besides English. A locale without its own
translation takes the one of its language (`de-AT` the `de-DE` texts).
They have not been reviewed by native speakers yet. Arabic and Hebrew get
their texts, but the wizard keeps its left-to-right layout. The base
language uses its translation too: `product.language: 1031` with a wizard
shows the German texts. `pkg/config/wizardtexts/*.yaml` holds them, one
file per tag. A test keeps their keys, placeholders, access keys and
column widths in step with the English texts, and another builds both
wizards in every translation and checks that no two controls of a dialog
share an access key. A locale without its own file whose primary language
covers several scripts takes the matching one: `sr-Latn-RS` the
`sr-Latn-CS` texts, `zh-SG` the `zh-CN` ones.

Each entry becomes a transform storage named by its LCID. The Template
lists the base language and then the entries in LCID order. The builder
builds the package once per entry, reusing the files it read for the
base, and stores the difference: `ProductLanguage` and the changed wizard
rows. A transform cannot change the code page of a base that has one
(MSIDB-12), so without `package.codepage` a package with `ui.languages`,
or whose wizard shows a translation, gets the ANSI code page all its
languages share (1252 for German and French), and 65001 when they need
different ones or one of them has none (Hindi, Kazakh). msiexec picks the
transform that matches the user's regional format
(`GetUserDefaultLangID`): an exact LCID first, then the same primary
language, else the first entry, which is the base language;
`ProductLanguage=1031` or `TRANSFORMS=:1031` on the command line forces one
(LANG-01, LANG-03, spec section 7.6). On the en-US CI runner, a full-UI
install with `ProductLanguage=1031` showed the German wizard from the
built-in translation, with the `de` entry's finish text (LANG-03).
The wizard cannot offer a language choice, since the engine applies the
transform before the first dialog; spec section 16, "Decisions not
adopted", records that and the one-MSI-per-language alternative.

### 4.13 `tables`

An escape hatch for tables the schema does not model.

```yaml
tables:
  - name: MyTable
    columns:
      - { name: Key, type: s72, key: true }
      - { name: Value, type: I4 }
    rows:
      - [first, 1]
```

At most 31 columns (MSIDB-07), a name of at most 31 characters, integers inside
the column type's range without its minimum (MSIDB-02). Standard tables that the
builder writes cannot be redefined here.

A binary cell (a `v0` or `V0` column, at most one per table) names a file to embed. It
is stored in the stream `<table>.<key1>.<key2>…`, which must be a valid stream
name (MSIDB-11) and must not match the name of the cabinet, an `icons` or
`binaries` entry, a wizard picture (`BannerBmp`, `DialogBmp`, `ErrorIco`), or another row. A bad name is reported at
`tables[i].rows[j]`. A clash names both owners and is reported at the later
one: `binaries`, `ui` and `icons` come first, then `tables` in file order,
then `package.cabinet`.

## 5. Validation

`config.Validate` returns every problem at once, each with its YAML path and
a severity. Errors fail the build; warnings are printed. The checks that need
only the configuration live in the `config` package:

- required fields, enums, GUID and version syntax, identifier syntax and
  lengths;
- references: feature components, component directories, key paths, icon and
  binary keys, firewall components, dialog properties;
- the rules of section 2 that are visible in the configuration.

Checks that need the inputs or the numbered tables run in the builder: PE
versions, sequence numbering and windows, condition parsing, ICE rules over
the generated tables, and the cabinet round trip.

## 6. Not in version 1

Nothing yet; a future `schema: msi-builder/v2` can add features without
changing the meaning of a v1 file.

Advertised installs, advertised shortcuts, script custom actions, patches,
standalone transforms (.mst files), merge modules and bundles are out of scope for good
(see [Out of scope](../../README.md#out-of-scope) and
[architecture.md](architecture.md)).
