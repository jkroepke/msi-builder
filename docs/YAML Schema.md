# YAML Schema

msi-builder reads one YAML file, usually `msi.yaml`, that says what your
package installs. This page shows the smallest file and then one recipe per
common task. Copy a recipe's keys into your file. A key your file already
has (`product`, `package`, `components`, `ui`, ...) must not appear twice:
replace yours with the recipe's, or merge the two. `hello.exe` is repeated
in the recipes only to show where the new keys go.

Every key and rule is described in [YAML Schema Design](design/yaml-schema.md),
which you only need for the unusual cases. A test builds every example on
this page with this version of msi-builder.

## The smallest package

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json
schema: msi-builder/v1

product:
  name: Hello
  manufacturer: Example
  version: 1.0.0
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301

package:
  platform: x64      # x86, x64 or arm64

components:
  - files:
      - source: hello.exe
```

Build it with `msi-builder build -o hello.msi msi.yaml`. This package:

- installs `hello.exe` to `C:\Program Files\Hello`, for all users;
- replaces an older installed version of Hello when a newer one is
  installed, and refuses to install over a newer one;
- can be installed to another folder with `msiexec /i hello.msi INSTALLDIR=D:\Hello`;
- shows the standard Windows Installer progress window, no wizard.

Two values matter beyond the obvious ones:

- `upgradeCode` ties all versions of your product together. Generate it
  once (for example with `New-Guid` or `uuidgen`) and never change it.
- `version` must go up with every release, in the form `1.2.3`. Windows
  only compares the first three numbers.

The first line is optional: it lets editors with the YAML language server
complete and check the file as you type.

`files[].source` is relative to the YAML file. A program (`.exe`, `.dll`)
must have a version resource; msi-builder reads its version from it.

## Versions from the build

`${NAME}` is replaced by a variable, so one file serves every release:

```yaml
product:
  name: Hello
  manufacturer: Example
  version: ${VERSION}
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
```

```shell
msi-builder build --var VERSION=1.4.0 -o hello.msi msi.yaml
```

With `--env`, environment variables are used too. `vars:` at the top of the
file sets defaults, and a default may use other variables
(`NAME: Acme ${ARCH}`); `--var` and `--env` values replace it. `[INSTALLDIR]` and other names in brackets are not
variables: Windows Installer fills them in at install time.

## Recipes

### Install several files and folders

Each component installs files into one folder. `directory` names a folder
below the install folder, or a standard Windows folder:

```yaml
components:
  - files:
      - source: hello.exe
      - source: README.txt
  - directory: config          # C:\Program Files\Hello\config
    files:
      - source: config/hello.yaml
  - directory: CommonAppDataFolder/Example/Hello   # C:\ProgramData\Example\Hello
    createFolder: true         # an empty folder for the program's data
```

To add a whole build output without listing every file, end the `source`
with a `/`. Every file below it is installed, in the same folder structure:

```yaml
components:
  - files:
      - source: dist/
        exclude: ['*.pdb']
```

Use a separate output folder, not the folder of the YAML file or one above
it, so the built `.msi` does not end up in the next build. The folder must be
the component's only file: a file that needs a shortcut or a service goes in
a component of its own and is left out of the folder with `exclude`.

### Choose the install folder

```yaml
installDir:
  name: Example/Hello    # C:\Program Files\Example\Hello
  remember: true         # an upgrade keeps a folder the user picked
```

Without `installDir` the folder is `<product name>` in the Program Files
folder for the package's platform (`C:\Program Files (x86)` for x86 on 64-bit
Windows), or in `%LOCALAPPDATA%\Programs` for a per-user package.

### Start menu and desktop shortcuts

```yaml
components:
  - files:
      - source: hello.exe
        shortcuts:
          - description: Say hello          # Start menu > Hello
          - directory: DesktopFolder        # the desktop
```

A shortcut is named after the product unless it has a `name`. Add
`arguments` for a command line and `directory: ProgramMenuFolder/Example`
for a Start menu folder.

### Install a Windows service

Put the service in the component whose file is the service program:

```yaml
components:
  - files:
      - source: hello.exe
    services:
      - name: Hello
        displayName: Hello Service
        description: Answers hello requests.
        start: auto                  # auto, demand or disabled
        arguments: --port 8080
        recovery:
          actions: [restart, restart, none]
          restartDelay: 30s
```

The service is started during the installation (unless `start: disabled`);
in a silent install, a service that does not start fails the
installation, which is rolled back. It is
stopped and removed on uninstall, and runs as LocalSystem unless `account`
and `password` say otherwise.

### Give a service account access to a folder

`grant` adds rights for an account to the folder and keeps the permissions
it already has, including the ones it inherits from `ProgramData`:

```yaml
components:
  - files:
      - source: hello.exe
    services:
      - name: Hello
        account: NT AUTHORITY\NetworkService
  - directory: CommonAppDataFolder/Hello/data
    createFolder: true
    folderPermissions:
      grant:
        - account: NT AUTHORITY\NetworkService
          rights: modify        # full, modify, readExecute, read or write
```

The rights apply to the subfolders and files too. Files and services take
`grant` in their `permissions` block. The account can also be a property,
`account: '[SERVICE_ACCOUNT]'`; the account is remembered, so a repair
from Apps & Features grants to it again, but a major upgrade needs it on
its command line again. In a per-machine package such a property is not
secure, so a standard user's command line does not set it (SEC-03),
only an administrator's; a wizard field for it needs `secure: true` in
`properties`. To replace the permissions
instead of adding to them, write an SDDL string in `sddl`.

### Add the program to PATH

```yaml
components:
  - files:
      - source: hello.exe
    environment:
      - name: PATH
        value: '[INSTALLDIR]'
        action: append       # set, setIfAbsent, append, prepend or remove
```

The entry is removed again on uninstall. Windows announces the change, so
programs started from Explorer afterwards see it; a command prompt that is already open
keeps the old `PATH`, so open a new one.

### Write registry values

```yaml
components:
  - files:
      - source: hello.exe
    registry:
      - root: HKLM
        key: SOFTWARE\Example\Hello
        name: InstallPath
        value: '[INSTALLDIR]'
      - root: HKLM
        key: SOFTWARE\Example\Hello
        name: Port
        type: dword          # string (default), expandString, multiString, dword or binary
        value: 8080
```

The values are removed on uninstall.

### Open a firewall port

```yaml
components:
  - id: Hello
    files:
      - source: hello.exe

firewall:
  - name: Hello
    component: Hello            # the rule is installed with this component
    program: '[#hello.exe]'
    protocol: tcp
    port: 8080
```

The rule is added on install and removed on uninstall. `[#hello.exe]` is
the installed path of the file `hello.exe`, the program the rule allows.
`profiles` limits the rule to `domain`, `private` or `public` networks.

### Register a file type

```yaml
components:
  - files:
      - source: hello.exe
        fileTypes:
          - progId: Example.HelloDocument
            description: Hello document
            iconIndex: 0                    # the first icon in hello.exe
            extensions:
              - extension: hello            # without the dot
```

The package registers `hello.exe` as the default program for `.hello`
files, with the file as its argument. A program the user picked under Open
with still wins.

### Close the running program before an upgrade

```yaml
properties:
  MSIRESTARTMANAGERCONTROL: Disable   # let close handle running programs
components:
  - files:
      - source: hello.exe
        close: true
```

When the package installs, repairs or removes `hello.exe` while it runs,
its windows are asked to close and get up to five seconds to do so. Then,
when the files are about to be replaced or removed, every `hello.exe` still
running from the installed folder is ended, also a copy without a window
(which gets no grace time) and copies other users run on the same machine.
This also happens on a repair that does not replace the file. A copy of `hello.exe` in another folder keeps running.
`close: { timeout: 30s, terminate: false }` waits longer and never ends
the program; Windows Installer then treats the file as in use.

### Let the user pick optional parts

Features are the parts a user can choose. A feature with `default: off` is
installed only on request. With `features`, the components go inside the
features, in place of the top-level `components` list:

```yaml
features:
  - id: Main
    title: Hello
    components:
      - files:
          - source: hello.exe
  - id: Docs
    title: Documentation
    default: off
    components:
      - directory: docs
        files:
          - source: manual.pdf

ui:
  enabled: true
  chooseFeatures: true
```

From the command line, `ADDLOCAL=ALL` installs every feature and
`ADDLOCAL=Main,Docs` the ones named.

`conditions` change whether a feature is installed from a property, the
Windows version or anything else a condition can test. An entry whose
condition is true sets the feature's default:

```yaml
properties:
  ASSOCIATE: { value: "1", secure: true }

features:
  - id: Main
    components:
      - files:
          - source: hello.exe
  - id: Associations
    title: File associations
    conditions:
      - { condition: ASSOCIATE <> 1, default: off }
    components:
      - registry:
          - { root: HKLM, key: Software\Classes\.hello, value: Hello.File }
```

`msiexec /i hello.msi ASSOCIATE=0` leaves the associations out. The
conditions apply to a first install; a feature that only a condition
enables (`default: disabled` on the feature) is still removed with the
product and repaired. At an upgrade, a feature keeps
the state the old version gave it, except that a condition or its own
`disabled` level can still leave it out (pass the property again), and one
the old version had disabled follows its conditions. `default: disabled`
keeps a feature out even of `ADDLOCAL=ALL` unless one of its conditions is
true; `off` lets the user still pick it.

### Show a setup wizard

Without `ui`, the installer shows only a progress window. `ui.enabled`
turns on a wizard with welcome, ready, progress and finish pages; the other
keys add pages or change the finish page:

```yaml
ui:
  enabled: true
  license: LICENSE.txt      # a license page with an "I accept" box (.rtf or text)
  chooseDirectory: true     # a page to change the install folder
  finishText: Open a new command prompt to use hello.
  launch:
    file: hello.exe         # "Launch Hello" on the finish page
```

`chooseFeatures: true` adds the feature tree (see the previous recipe). A
silent install (`msiexec /i hello.msi /qn`) skips the wizard and uses the
defaults.

The wizard follows `product.language`, and `ui.languages` adds more
languages to one package; msi-builder ships texts for 39 languages. Your
own logo and colours go in `ui.theme`. Both are described in
[YAML Schema Design](design/yaml-schema.md#412-ui).

### Ask for a setting during installation

Public properties, with upper-case names, can be set on the command line
(`msiexec /i hello.msi PORT=9090`) and used as `[PORT]` in most text values,
such as service arguments, registry values and shortcut arguments. `ui.dialogs` adds a wizard page to ask for them:

```yaml
properties:
  PORT: "8080"              # the default

components:
  - files:
      - source: hello.exe
    services:
      - name: Hello
        arguments: --port [PORT]

ui:
  enabled: true
  dialogs:
    - id: SettingsDlg
      title: Settings
      description: How Hello listens for requests.
      after: WelcomeDlg
      fields:
        - property: PORT
          label: Port to listen on
```

Fields can also be `type: password`, `checkbox` or `choice`.

### Change or translate the wizard's texts

`ui.strings` replaces single texts of the built-in wizard by key, and
`ui.languages` adds languages to the same package. An entry there
translates the texts your configuration brings too: the pages of
`ui.dialogs` by their `id`, their fields by property, and the launch
checkbox:

```yaml
properties:
  PORT: "8080"

ui:
  enabled: true
  strings:
    WelcomeText: Hello is a small web server. Click Next to install it.
    Finish: '&Done'
  launch:
    file: hello.exe
    text: '&Start Hello now'
  dialogs:
    - id: SettingsDlg
      title: Settings
      after: WelcomeDlg
      fields:
        - property: PORT
          label: Port to listen on
  languages:
    de:
      strings:
        WelcomeText: Hello, der kleine Webserver. Klicken Sie auf Weiter.
      launch:
        text: 'Hello jetzt &starten'
      dialogs:
        SettingsDlg:
          title: Einstellungen
          fields:
            PORT: {label: Port für Anfragen}
```

The keys of `strings` are the names in `pkg/config/schema.json`, such as
`Next`, `WelcomeTitle` or `ExitText`. A text you leave out keeps the
built-in one, or its built-in translation in another language; a page
text you leave out of a language keeps your own. `[ProductName]` and
other properties work in the page texts and labels, and `&` marks the access key on
buttons, checkboxes and radio buttons. Quote a text that starts with `&`,
`[`, `{` or `<`. msiexec shows the language that matches the user's regional
format, and `msiexec /i hello.msi ProductLanguage=1031` picks one.

### Keep a configuration file on upgrade

A user's changes to a configuration file survive upgrades with `preserve`.
The installer then never replaces that file once it exists, not even with a
newer version of it; uninstall removes it. Give the file a component of its
own:

```yaml
components:
  - files:
      - source: hello.exe
  - preserve: true
    files:
      - source: hello.conf
```

### Upgrade even when the old version cannot be removed

An upgrade removes the old version. When the old version fails during
uninstall, the upgrade normally fails too and the old version stays.
`ignoreRemoveFailure` installs the new version anyway:

```yaml
upgrade:
  ignoreRemoveFailure: true
```

The old version then stays installed next to the new one, and its own
uninstall will still fail the same way. Use this only when an old release is
known to fail on uninstall.

### Leave versions older than a release alone

An upgrade removes every older version. When the versions before some
release were built differently, for example with another folder, and must
not be upgraded, `minVersion` sets the oldest version the upgrade removes:

```yaml
upgrade:
  minVersion: 0.18.0     # removes 0.18.0 and later; includeMin: false removes only later ones
```

An older version is not upgraded: it stays installed next to the new one.
When both versions were built by msi-builder into the same folder, they
share their files: uninstalling the new one leaves them in place, but a file
the new version replaced is likely left in its newer version (not tested).
A version built by another tool into the same folder is likely a problem:
uninstalling either one can delete files the other needs (not tested). To
make users uninstall an older version first, detect it and stop the install
with a message:

```yaml
upgrade:
  minVersion: 0.18.0
  related:
    - upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301   # your own product.upgradeCode
      maxVersion: 0.18.0
      onlyDetect: true
      property: OLDVERSIONFOUND
launchConditions:
  - condition: NOT OLDVERSIONFOUND
    message: Please uninstall versions before 0.18.0 first.
```

With `includeMin: false`, version 0.18.0 itself is not upgraded either, so
add `includeMax: true` to the `related` entry to detect it too.

### Delete program data on uninstall

`purge` deletes a folder with everything the program wrote into it when the
product is uninstalled, but not when an upgrade replaces it. The example
deletes it only when the user asks for it with
`msiexec /x hello.msi DELETE_DATA=1`. The folder must
lie outside the install folder, for example below `CommonAppDataFolder`:

```yaml
components:
  - files:
      - source: hello.exe
  - directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    purge:
      condition: DELETE_DATA="1"
```

### Delete a folder that a property names

`removeFolders` deletes the folder in a property, with everything in it,
when the product is removed (and, expected but not tested, the feature
that holds the component), but not when an upgrade replaces the product. It works for any directory,
the install folder (with `installDir.remember`, so the uninstall finds a
folder chosen at install time) and a folder the first install remembered
in the registry. The folder is moved aside and deleted at the end, so a
failed uninstall puts it back, and a junction inside it is deleted as
a link, never followed (tested on Windows). `on:
install` and `on: both` are for folders the package does not install to,
and a folder that is or holds a folder of the package goes only when the
whole product is removed, not with one feature, on install or on repair,
whichever property names it (tested for an upgrade, expected for a
repair and a feature removal). It refuses folders in the Windows folder, folders
that hold a standard folder, folders behind junctions and the Public
profile (tested), and drive roots, standard folders such as Program
Files, other users' profiles and anything in an elevated session a
standard user started (expected, not tested), but a folder an
administrator names, such as `INSTALLDIR=D:\Tools`, is emptied:

```yaml
installDir:
  remember: true            # the uninstall finds the install folder again

properties:
  DATA_DIR:
    search:
      registry: { root: HKLM, key: SOFTWARE\Example\Hello, name: DataDir, type: directory }

components:
  - files:
      - source: hello.exe
    registry:
      - { root: HKLM, key: SOFTWARE\Example\Hello, name: DataDir, value: '[DATA_DIR]' }
    removeFolders:
      - property: INSTALLDIR      # also what the program wrote into it
      - property: DATA_DIR
        condition: DELETE_DATA="1"
```

### Ask before deleting data on uninstall

A `flow: remove` dialog adds a page to the wizard's remove path, after
Remove in the maintenance menu, for example a checkbox that the purge above
reads. Users reach it by running the package again or, expected but not yet
verified on Windows, through Change in Add or Remove Programs. `msiexec /x`
removes the product without the wizard, so the property also works on the
command line:
`msiexec /x hello.msi DELETE_CONFIGURATION=1`.

```yaml
properties:
  DELETE_CONFIGURATION: {}  # declared, so the uninstall sees the box

components:
  - files:
      - source: hello.exe
  - directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    purge:
      condition: DELETE_CONFIGURATION="1"

ui:
  enabled: true
  dialogs:
    - id: RemoveOptionsDlg
      flow: remove
      title: Remove options
      fields:
        - property: DELETE_CONFIGURATION
          type: checkbox
          label: '&Delete the configuration'
```

### Run a command during installation

```yaml
customActions:
  - id: InitConfig
    run:
      command: '"[INSTALLDIR]hello.exe" init'
    execute: deferred         # runs while the files are installed
    impersonate: false        # as LocalSystem, like the installation
    return: check             # check fails the install on a non-zero exit, ignore does not
    progress: Creating the configuration   # shown on the progress page while it runs
    schedule:
      - sequence: execute
        after: InstallFiles
        when: install         # install, upgrade, uninstall or maintenance
```

`when: install` runs on a first installation only, not on an upgrade or a
repair; add a second placement with `when: upgrade` for upgrades. The
command runs without a window, and its output goes into the
installation log (`msiexec /i hello.msi /l*v install.log`). Quote the
executable when it comes from a folder or property, as above: an
unquoted `[INSTALLDIR]hello.exe` is rejected, because Windows would try
`C:\Program.exe` first and the command runs as LocalSystem. A property
alone, such as `[OLD_UNINSTALL] /S` with an `UninstallString` found by a
registry search, only gets a warning: such a value is usually quoted
already, and quoting it again would break it. `directory`
only sets the working directory; name the program with its folder or
`[#hello.exe]`, since a bare `hello.exe` is looked up next to msiexec and
on `PATH`. Pass a folder as `"[INSTALLDIR]."`, not `"[INSTALLDIR]"`, whose
trailing backslash would escape the closing quote. Everything the
command starts ends with it: a background process it leaves behind is
ended when the action finishes, so start a service through `services`,
not from a command. `progress` is optional: it names the step on
the progress page while the command runs. A `ui.languages` entry
translates it with `progress: {InitConfig: ...}`. Prefer the
recipes above where one fits: they also undo themselves on uninstall.

### Install for the current user only

```yaml
package:
  platform: x64
  scope: perUser

components:
  - files:
      - source: hello.exe
```

The package then installs to `%LOCALAPPDATA%\Programs\Hello` and into
the user's part of the registry, so it cannot install services or write to
HKLM. Windows Installer wants each component in the user's profile to
mark itself with an HKCU value; msi-builder writes one under
`HKCU\Software\<manufacturer>\<product name>` for you.
`perUserOrMachine` and `perMachineOrUser` let the user choose. In such a
package a `PATH` entry goes to the machine `PATH` when the user installs
for all users and to their own `PATH` otherwise, and a component with
`scope: perMachine` holds what only an all-users install writes, such as
HKLM values:

```yaml
package:
  platform: x64
  scope: perMachineOrUser

components:
  - files:
      - source: hello.exe
    environment:
      - { name: PATH, value: '[INSTALLDIR]', action: append }
  - id: MachineSettings
    directory: INSTALLDIR
    scope: perMachine
    registry:
      - { root: HKLM, key: Software\Example Corp\Hello, name: Path, value: '[INSTALLDIR]' }
```

A custom action that runs as LocalSystem (`impersonate: false`) needs
`scope: perMachine` too: it then runs only when the user installs for all
users, and every per-user install leaves it out, an administrator's too:

```yaml
package:
  platform: x64
  scope: perMachineOrUser

customActions:
  - id: RegisterMachine
    run: { command: '"[#hello.exe]" --register' }
    execute: deferred
    impersonate: false
    scope: perMachine
    return: check
    schedule:
      - { sequence: execute, after: InstallFiles, when: install }
```

### Ask for a restart

```yaml
scheduleReboot:
  when: install
```

With a UI, Windows asks the user to restart at the end. A silent install
(`/qn`) may restart the machine without asking, so pass `/norestart`;
msiexec then returns exit code 3010.

## Key reference

The keys at the top level of the file. Only `schema`, `product`, `package`
and `components` (or `features`) are needed.

| Key | What it sets | Default |
|---|---|---|
| `schema` | Always `msi-builder/v1` | required |
| `vars` | Defaults for `${NAME}` variables | none |
| `product` | Name, manufacturer, version, upgrade code | required |
| `package` | Platform, install scope, compression | required (`platform`) |
| `installDir` | The program's folder | `Program Files\<product name>` |
| `components` | Files, services, registry, environment, shortcuts | required |
| `features` | Parts the user can choose | one feature with everything |
| `properties` | Settings that the command line and the wizard can change | none |
| `upgrade` | How a new version replaces an old one | replace older, refuse newer |
| `launchConditions` | Conditions that stop the install with a message | none |
| `customActions` | Commands and other steps run during installation | none |
| `firewall` | Firewall rules | none |
| `scheduleReboot` | Ask for a restart afterwards | no restart |
| `ui` | The setup wizard | progress window only |
| `icons`, `binaries` | Icons and helper programs stored in the package | none |
| `directories` | An explicit folder tree, rarely needed | from the components |
| `tables` | Extra database tables, for experts | none |

The keys used most inside them:

| Key | Default | Notes |
|---|---|---|
| `product.name` | required | Shown in Programs and Features |
| `product.manufacturer` | required | Shown in Programs and Features |
| `product.version` | required | `major.minor.build`, at most 255.255.65535 |
| `product.upgradeCode` | required | A GUID that never changes |
| `product.language` | `1033` (English) | A Windows language ID, such as 1031 for German |
| `product.arp.icon` | none | An `icons` key shown in Programs and Features |
| `package.platform` | required | `x86`, `x64` or `arm64` |
| `package.scope` | `perMachine` | `perUser`, `perUserOrMachine`, `perMachineOrUser` |
| `package.compression` | `lzx` | `mszip` gives larger packages, `none` stores the files |
| `installDir.name` | product name | `/` makes nested folders |
| `installDir.remember` | `false` | Keep a custom folder on upgrade |
| `components[].directory` | the install folder | A subfolder or a standard folder |
| `components[].files[].source` | required | A file, or a folder ending in `/` |
| `components[].files[].name` | the source's name | The installed name |
| `components[].files[].shortcuts` | none | Start menu and desktop shortcuts |
| `components[].files[].close` | `false` | Close the running program before it is replaced or removed |
| `components[].services` | none | A Windows service for the component's program |
| `components[].registry` | none | Registry values |
| `components[].environment` | none | Environment variables such as `PATH` |
| `components[].preserve` | `false` | Keep the user's changes on upgrade |
| `components[].purge` | `false` | Delete the folder on uninstall |
| `components[].removeFolders` | none | Delete the folder in a property on uninstall |
| `features[].default` | `on` | `off` installs only on request |
| `ui.enabled` | `false` | The setup wizard |
| `ui.license` | none | License page |
| `ui.chooseDirectory` | `false` | Install folder page |
| `ui.chooseFeatures` | `false` | Feature tree page |
| `ui.launch.file` | none | "Launch" checkbox on the finish page |
| `ui.finishText` | none | A paragraph on the finish page |
| `ui.dialogs[].flow` | `install` | `remove` shows the page after Remove in the maintenance menu |
| `ui.strings` | none | Your own texts for the built-in wizard |
| `ui.languages` | none | More wizard languages in the same package |

Unknown keys are errors, so a typo never drops a setting silently. Every
error names the line or the key, such as `components[0].files[1].source`.
`msi-builder validate msi.yaml` checks a file without building it.

## More

- [Getting Started](Getting%20Started.md) builds, signs and installs a first
  package.
- [End User Guide](End%20User%20Guide.md) is for the people who install
  your package.
- [Migrating from WiX](Migrating%20from%20WiX.md) ports a `.wxs` file.
- [YAML Schema Design](design/yaml-schema.md) has every key, every default,
  the generated tables and the Windows test results behind each rule.
- The [examples](../examples) folder holds complete files, such as a
  [desktop program](../examples/desktop/msi.yaml) and a
  [service](../examples/branded/msi.yaml).
