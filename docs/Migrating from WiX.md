# Migrating from WiX

This page is for people who build their installer with the WiX toolset
today and want to build the same package with msi-builder. It shows how a
`.wxs` file turns into an `msi.yaml` file, element by element, and lists
what msi-builder does not do.

Most of a WiX file has no counterpart at all: msi-builder writes component
key paths, cabinets, short names, `ServiceControl` elements, Start menu cleanup
and the wizard's dialogs itself. A port is therefore usually much shorter
than the `.wxs` file it replaces. A test builds every YAML example on this
page with this version of msi-builder. The XML snippets use WiX v4 and later
syntax; v3 names are mentioned where they differ.

## How a port goes

1. Let msi-builder write a first `msi.yaml`:
   `msi-builder convert --msi app-1.4.0.msi -o msi.yaml app.wxs`, where
   `app-1.4.0.msi` is your last release built by WiX from that file. It
   converts what it can, keeps the component GUIDs WiX generated, and
   leaves a `# TODO` comment for everything else; see
   [convert](cli/msi-builder_convert.md). It reads one `.wxs` file, the one
   with `Package` (v3 `Product`); port fragments from other files by hand.
2. Work through these comments with the table and the sections below,
   and check the result with `msi-builder validate`. WiX variables become
   `${NAME}` references, which the comment at the top of the file lists;
   pass each one, such as `msi-builder validate --var Version=1.4.0 msi.yaml`,
   or give it a default under `vars:`.
3. Build it, and compare the tables of both packages with
   `msi-builder inspect`, which reads any `.msi`, also one built by WiX:
   `msi-builder inspect --table Registry old.msi`.
4. Install your last WiX release, upgrade it with the new package, and
   uninstall it, as described in [Checking the port](#checking-the-port).

## Keep these values

- **`UpgradeCode`** goes into `product.upgradeCode` unchanged. With another
  code, Windows sees a new product and installs it next to the old one.
- **Names that users type**, such as the install folder property
  (`INSTALLFOLDER`) and the public properties of your command line
  (`PORT=8080`). Keep the install folder's ID with `installDir.id`.
- **Component GUIDs** go into `guid`: the ones you wrote yourself, and with
  `msi-builder convert --msi` also the ones WiX generated for `Guid="*"` or
  no `Guid`. Without them, msi-builder generates new GUIDs, which is fine
  with the default upgrade, because it removes the old version before it
  installs the new one. With a late removal the upgrade deletes files; see
  [Checking the port](#checking-the-port).

## A whole package

A small WiX package with a service and a `PATH` entry:

```xml
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="Hello" Manufacturer="Example" Version="$(var.Version)"
           UpgradeCode="3f2504e0-4f89-41d3-9a0c-0305e82c3301">
    <MajorUpgrade DowngradeErrorMessage="A newer version of [ProductName] is already installed." />
    <MediaTemplate EmbedCab="yes" />

    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="Hello" />
    </StandardDirectory>

    <Feature Id="Main">
      <ComponentGroupRef Id="HelloComponents" />
    </Feature>

    <ComponentGroup Id="HelloComponents" Directory="INSTALLFOLDER">
      <Component>
        <File Source="hello.exe" KeyPath="yes" />
        <ServiceInstall Name="Hello" DisplayName="Hello Service" Type="ownProcess"
                        Start="auto" ErrorControl="normal" Account="LocalSystem" />
        <ServiceControl Id="HelloControl" Name="Hello" Start="install" Stop="both"
                        Remove="uninstall" Wait="yes" />
      </Component>
      <Component>
        <File Source="hello.yaml" />
      </Component>
      <Component Id="PathEnv" Guid="0b7e6b4e-6c1a-4c55-9e43-3b8f3c0d6a11" KeyPath="yes">
        <Environment Id="PATH" Name="PATH" Value="[INSTALLFOLDER]" Action="set"
                     Part="last" System="yes" Permanent="no" />
      </Component>
    </ComponentGroup>
  </Package>
</Wix>
```

The same package in msi-builder:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json
schema: msi-builder/v1

product:
  name: Hello
  manufacturer: Example
  version: ${VERSION}
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301

package:
  platform: x64            # wix build -arch x64

upgrade:
  allowSameVersion: false  # WiX's AllowSameVersionUpgrades="no" default
  downgradeMessage: A newer version of [ProductName] is already installed.

installDir:
  id: INSTALLFOLDER
  name: Hello

components:
  - files:
      - source: hello.exe
    services:
      - name: Hello
        displayName: Hello Service
        start: auto
  - files:
      - source: hello.yaml
  - id: PathEnv
    guid: 0b7e6b4e-6c1a-4c55-9e43-3b8f3c0d6a11
    environment:
      - name: PATH
        value: '[INSTALLFOLDER]'
        action: append       # Action="set" Part="last"
        scope: machine       # System="yes"
```

Build it with `msi-builder build --var VERSION=1.4.0 -o hello.msi msi.yaml`.
The rest of this page shows the parts of a file one at a time. Each example
replaces the keys of the same name in this file.

## What you can leave out

These WiX elements and attributes need nothing in `msi.yaml`, because
msi-builder does the same by default:

| WiX | msi-builder |
|---|---|
| `MediaTemplate`, `Media`, `EmbedCab`, `Compressed="yes"` | Embedded LZX cabinets, split automatically for large payloads; `package.compression`, `package.cabinet` and `package.embedCabinets` change it |
| `InstallerVersion` | 5.0; `package.installerVersion` sets another |
| `KeyPath="yes"` on the first file, or on the component | The first file of a component, else its first registry value, else its folder; `keyPath` names another one |
| `Guid="*"` | Component GUIDs are generated from the upgrade code and stable names |
| `ProductCode="*"` (v3 `Product Id="*"`) | Generated from the upgrade code, the version, the platform and the inputs: an unchanged rebuild keeps it, any change gives a new one |
| `ComponentGroup`, `ComponentGroupRef`, `ComponentRef`, `FeatureRef` | Features list their components; without `features` every component is in one feature. Convert keeps a feature with any ID other than `MainFeature`, so `ADDLOCAL` and the feature states of an upgrade from the WiX package still name it |
| `ServiceControl` for the package's own service | Started on install, stopped on install and uninstall, deleted on uninstall, with `Wait="yes"` |
| `RemoveFolder` and an HKCU `RegistryValue` key path next to a Start menu shortcut | Written for every shortcut and profile folder |
| Short names (`ShortName`) | Generated |
| `<Property Id="ALLUSERS">`, `Scope="perMachine"` | `package.scope`, `perMachine` by default |
| `Secure="yes"` on a public property | Public properties are secure |
| `WixUIExtension`, `WixUI` references | The `ui` keys |

## Element by element

| WiX | `msi.yaml` |
|---|---|
| `Package` (v3 `Product`) `Name`, `Manufacturer`, `Version`, `UpgradeCode`, `Language` | `product.name`, `manufacturer`, `version`, `upgradeCode`, `language` |
| `Package` `Codepage`, `Scope`; `-arch` | `package.codepage`, `package.scope`, `package.platform` |
| `SummaryInformation` | `package.summary`, `product.description`, `product.comments` |
| `ARPPRODUCTICON`, `ARPHELPLINK`, `ARPURLINFOABOUT`, `ARPURLUPDATEINFO`, `ARPCONTACT`, `ARPCOMMENTS`, `ARPNOREPAIR`, `ARPNOMODIFY`, `ARPNOREMOVE`, `ARPINSTALLLOCATION` | `product.arp` keys `icon`, `helpLink`, `aboutLink`, `updateLink`, `contact`, `comments`, `noRepair`, `noModify`, `noRemove`, `installLocation` |
| `Icon` | `icons` |
| `Binary` | `binaries` |
| `MajorUpgrade`, `Upgrade`, `UpgradeVersion` | `upgrade` |
| `StandardDirectory`, `Directory`, `DirectoryRef` | `installDir`, `components[].directory`, `directories` |
| `SetDirectory` | `customActions[].setDirectory` |
| `Feature` | `features` |
| `Component` | `components` |
| `File` | `components[].files`; a File without `Source` takes the `FileSource` of its Directory |
| Heat harvesting, `Files` (v5) | A folder `source` ending in `/` |
| `CreateFolder` | `components[].createFolder` |
| `RemoveFile`, `RemoveFolder` | `components[].removeFiles` |
| `util:RemoveFolderEx` | `components[].purge` (the component's own folder), `components[].removeFolders` (a folder from a property) |
| `RegistryKey`, `RegistryValue` | `components[].registry` |
| `RemoveRegistryKey`, `RemoveRegistryValue` | `components[].removeRegistry` |
| `Environment` | `components[].environment` |
| `Shortcut`, `util:InternetShortcut` | `files[].shortcuts`, `components[].shortcuts` |
| `ProgId`, `Extension`, `Verb` | `files[].fileTypes`; an icon from another file than the one the type opens becomes a note to add the DefaultIcon registry value. msi-builder writes file type texts as they are, so convert fills in `[PROPERTY]` references to properties that nothing in the source changes, assuming none is set on the command line. msi-builder registers each ProgId once: convert merges the extensions of a ProgId declared again for the same file into the first one, and leaves a declaration for another file out with a note |
| `util:CloseApplication` | `files[].close` on the program's file; it matches the installed path, not the filename, and ends the program only with `TerminateProcess` (`terminate: false` otherwise). One that only sets its `Property` has no counterpart |
| `ServiceInstall`, `ServiceConfig`, `util:ServiceConfig` | `components[].services` |
| `ServiceControl` for another service | `components[].serviceControl` |
| `util:EventSource` | `components[].eventLog` |
| `fw:FirewallException` | `firewall` |
| `PermissionEx` (SDDL), `Permission` | `permissions.sddl`, `folderPermissions.sddl` |
| `util:PermissionEx` | `permissions.grant`, `folderPermissions.grant`; on a registry key, `permissions.sddl` |
| `Property` | `properties` |
| `RegistrySearch`, `DirectorySearch`, `FileSearch`, `ComponentSearch` | `properties.<NAME>.search`; a `DirectorySearch` without `Path` in a `RegistrySearch` with `Type="directory"` adds nothing, because that search already finds only a folder that exists. In a `Type="raw"` search, or one without `Type`, it returns the folder that holds the path in the value without checking that it exists (SEARCH-09). Convert keeps the value and adds a note, which says that for a value that is a path the property is set whenever the value exists, so the raw value is enough when only that matters. A search nested in it looks in that folder, while convert searches in the folder the value names, so convert notes that too |
| `ProductSearch UpgradeCode` | an `upgrade.related` row with `onlyDetect: true` that sets the property |
| `Launch` (v3 `Condition` under `Product`) | `launchConditions` |
| `CustomAction`, `SetProperty`, `Custom` | `customActions` |
| `InstallExecuteSequence`, `InstallUISequence` | `customActions[].schedule` |
| `ScheduleReboot` | `scheduleReboot` |
| `ProgressText` for a custom action | `customActions[].progress` |
| `WixUI`, `WixVariable` `WixUILicenseRtf`, `WixUIBannerBmp`, `WixUIDialogBmp` | `ui` |
| `.wxl` localization files | `ui.strings`, `ui.languages` |
| Custom tables (`CustomTable`) | `tables` |

## Variables and versions

WiX preprocessor variables become `${NAME}` variables:

| WiX | msi-builder |
|---|---|
| `$(var.Version)` with `wix build -d Version=1.4.0` | `${VERSION}` with `msi-builder build --var VERSION=1.4.0` |
| `$(env.VERSION)` | `${VERSION}` with `--env` |
| `<?define Port = 8080 ?>` | `vars:` at the top of the file |
| `!(bind.FileVersion.hello.exe)` | Pass the version as a variable |
| `<?if?>`, `<?include?>` | Not available; use component `condition`s, or generate the YAML file in your build |

`[INSTALLFOLDER]`, `[#hello.exe]` and other names in brackets work as they
do in WiX: Windows Installer fills them in at install time.

## Package and upgrades

```xml
<Icon Id="HelloIcon" SourceFile="hello.ico" />
<Property Id="ARPPRODUCTICON" Value="HelloIcon" />
<Property Id="ARPHELPLINK" Value="https://example.com/help" />
<Property Id="ARPNOREPAIR" Value="1" />

<MajorUpgrade Schedule="afterInstallValidate" AllowSameVersionUpgrades="yes"
              DowngradeErrorMessage="A newer version of [ProductName] is already installed." />
<Upgrade Id="513044d7-fae5-4715-984a-14f911d06fcc">
  <UpgradeVersion Maximum="$(var.Version)" Property="OLDX86FOUND" />
</Upgrade>
```

```yaml
product:
  name: Hello
  manufacturer: Example
  version: ${VERSION}
  upgradeCode: 3f2504e0-4f89-41d3-9a0c-0305e82c3301
  arp:
    icon: hello.ico
    helpLink: https://example.com/help
    noRepair: true

icons:
  hello.ico: hello.ico

upgrade:
  removeOld: afterInstallValidate   # MajorUpgrade Schedule
  allowSameVersion: true            # AllowSameVersionUpgrades="yes"
  downgradeMessage: A newer version of [ProductName] is already installed.
  related:                          # Upgrade / UpgradeVersion
    - upgradeCode: 513044d7-fae5-4715-984a-14f911d06fcc
      maxVersion: ${VERSION}
      includeMax: false
      property: OLDX86FOUND
```

- `Schedule` takes the same names in `removeOld`: `afterInstallValidate`,
  `afterInstallInitialize`, `afterInstallExecute` and
  `afterInstallFinalize`; `afterInstallExecuteAgain` has no counterpart, use
  `afterInstallExecute`. WiX's default is
  `afterInstallValidate`; msi-builder's is `afterInstallInitialize`, which
  also removes the old version first, but inside the installation, so a
  failed upgrade puts the old version back.
- `AllowDowngrades="yes"` has no direct counterpart: msi-builder's upgrade
  never removes a newer version. `downgrade: allow` and `downgrade: ignore`
  install the older version next to the newer one.
- `IgnoreRemoveFailure="yes"` is `ignoreRemoveFailure: true`.
- WiX's `AllowSameVersionUpgrades` defaults to `no`, msi-builder's
  `allowSameVersion` to `true`. convert writes `allowSameVersion: false` for
  every `MajorUpgrade` without `AllowSameVersionUpgrades="yes"` or
  `AllowDowngrades="yes"`, so the same version is still not taken as an upgrade.
- A hand-made major upgrade without `MajorUpgrade`, an `UpgradeVersion` of
  the package's own `UpgradeCode` with a `Minimum` below this version and a
  `Maximum` at it, with `RemoveExistingProducts` scheduled, is `minVersion`
  (`IncludeMinimum="no"` is `includeMin: false`); `convert` writes it. A
  version below the minimum stays installed next to the new one, and a
  WiX-built one keeps its own component codes unless they are pinned
  (`convert --msi` or `guid`), so uninstalling either product may delete
  files the other uses (inferred, not tested); "Leave versions older than a
  release alone" in the YAML guide shows how to refuse it instead.
- A WiX v3 `<Product>` whose `<Package>` has no `InstallScope` leaves
  `ALLUSERS` unset, so Windows Installer registers it for the installing
  user only, even when it installs into Program Files. A `perMachine`
  package does not find a product registered per user, so the upgrade
  installs next to it (seen in a real-world port). Remove the old version
  first; as it is registered for one user, run `msiexec /x {old ProductCode}`
  as the user who installed it.
- WiX sets `WIX_UPGRADE_DETECTED` during an upgrade, msi-builder sets
  `OLDERVERSIONBEINGUPGRADED`. If your conditions use the WiX name, keep it
  with `upgrade.property: WIX_UPGRADE_DETECTED`, and
  `upgrade.newerProperty: WIX_DOWNGRADE_DETECTED` for the other one.

## Folders

```xml
<StandardDirectory Id="ProgramFiles64Folder">
  <Directory Id="INSTALLFOLDER" Name="Hello">
    <Directory Id="CONFIGDIR" Name="config" />
  </Directory>
</StandardDirectory>
<StandardDirectory Id="CommonAppDataFolder">
  <Directory Id="DATADIR" Name="Hello" />
</StandardDirectory>
```

A component names its folder with `directory`: a folder below the install
folder, a standard folder with a path, or a directory ID:

```yaml
installDir:
  id: INSTALLFOLDER
  name: Hello

components:
  - files:
      - source: hello.exe
  - directory: config                  # CONFIGDIR: INSTALLFOLDER\config
    files:
      - source: config/hello.yaml
  - directory: CommonAppDataFolder/Hello   # DATADIR
    createFolder: true
```

Only when something else refers to a folder by its ID, such as a
`[DATADIR]` in a registry value, write the tree with `directories`:

```yaml
directories:
  - id: CommonAppDataFolder
    children:
      - id: DATADIR
        name: Hello

components:
  - files:
      - source: hello.exe
    registry:
      - root: HKLM
        key: SOFTWARE\Example\Hello
        name: DataDir
        value: '[DATADIR]'
  - directory: DATADIR
    createFolder: true
```

- The install folder is configurable by default, so
  `msiexec /i hello.msi INSTALLFOLDER=D:\Hello` works without
  `ConfigurableDirectory`.
- `ProgramFiles6432Folder` is not needed: `installDir` picks the Program
  Files folder of `package.platform`.
- A `SetDirectory` that points a folder somewhere else at install time
  becomes a `setDirectory` custom action. Often a standard folder does the
  same: `[%SYSTEMROOT]\Temp\Hello` is `WindowsFolder/Temp/Hello`.
- A WiX `RegistrySearch` that restores the install folder from the last
  install is `installDir.remember: true`. It writes its own value,
  `InstallDir` under `HKLM\Software\<manufacturer>\<product name>`, so the
  first upgrade from the WiX package does not find the WiX value. To keep
  the WiX value instead, leave `remember` out, keep the WiX registry value
  in `registry`, and turn the search into a `search` of the install folder
  property, such as `properties.INSTALLFOLDER.search` (see
  [Properties and searches](#properties-and-searches)). Use one of the two,
  not both. With `remember`, drop an `InstallDir` value that your WiX
  package wrote under that same key from `registry`.

## Components, files and features

| WiX `Component` / `File` | msi-builder |
|---|---|
| `Component Id`, `Guid`, `Directory`, `Condition` (v3 child `Condition`) | `id`, `guid`, `directory`, `condition` |
| `Permanent`, `NeverOverwrite`, `SharedDllRefCount`, `Transitive` | `permanent`, `neverOverwrite`, `sharedDllRefCount`, `transitive` |
| `Bitness="always32"`, `"always64"` (v3 `Win64`) | `bitness: 32`, `64` |
| `KeyPath="yes"` on a later file or a registry value | `keyPath` |
| `File Source`, `Name`, `Id` | `source`, `name`, `id` |
| `Checksum`, `Vital`, `ReadOnly`, `Hidden`, `System` | `checksum`, `vital`, `readOnly`, `hidden`, `system` |
| `DefaultVersion`, `CompanionFile` | `version` |

msi-builder requires a version resource on programs (`.exe`, `.dll`),
because an upgrade can lose a program without one.
`allowUnversionedExecutable: true` on the file allows it, with a warning.

A feature lists its components, by ID or in place. `Level="1"` is the
default, `Level="2"` is `default: off`, `Level="0"` is
`default: disabled`, and any other level goes into `level`:

```xml
<Feature Id="Main" Title="Hello">
  <ComponentRef Id="HelloExe" />
</Feature>
<Feature Id="Docs" Title="Documentation" Level="2">
  <ComponentGroupRef Id="DocComponents" />
</Feature>
```

```yaml
components:
  - id: HelloExe
    files:
      - source: hello.exe
  - id: Manual
    directory: docs
    files:
      - source: docs/manual.pdf

features:
  - id: Main
    title: Hello
    components: [HelloExe]
  - id: Docs
    title: Documentation
    default: off
    components: [Manual]
```

A conditional level, `<Level Value="0" Condition="…"/>` (WiX v3:
`<Condition Level="0">…</Condition>`), is an entry of the feature's
`conditions`:

```yaml
features:
  - id: Docs
    title: Documentation
    default: off
    conditions:
      - { condition: INSTALLDOCS = 1, default: on }
    components:
      - files:
          - source: docs/manual.pdf
```

msi-builder adds `NOT Installed` to most conditions, because a feature
that its condition disables when the product is removed keeps its files.
A condition that enables a feature whose own level is 0
(`default: disabled`, WiX `Level="0"` with a higher conditional level) also holds
at removals and repairs, because the uninstall leaves a level-0 feature
alone (reported; the fix is confirmed by INST-06). `ADDLOCAL` in a later
run still needs the condition.
A condition cannot test feature or component states, nor `Installed`; `convert` leaves such levels as a `# TODO`.

`AllowAbsent` and `ConfigurableDirectory` are `allowAbsent` and
`directory` on the feature, and `Display` is `display` with the values
`expanded`, `collapsed` and `hidden`.

Files that Heat harvested, or a WiX v5 `Files` element, become a folder
source. Every file below the folder is installed in the same structure:

```yaml
components:
  - files:
      - source: dist/
        exclude: ['*.pdb']
```

## Registry

```xml
<RegistryKey Root="HKLM" Key="SOFTWARE\Example\Hello" ForceDeleteOnUninstall="yes">
  <RegistryValue Name="InstallDir" Type="string" Value="[INSTALLFOLDER]" />
  <RegistryValue Name="Port" Type="integer" Value="8080" />
  <RegistryKey Key="Plugins" ForceCreateOnInstall="yes" />
</RegistryKey>
<RemoveRegistryKey Root="HKLM" Key="SOFTWARE\Example\HelloOld" Action="removeOnInstall" />
```

Each value is one entry with its full key. A key without values is an entry
with `type: key`:

```yaml
components:
  - files:
      - source: hello.exe
    registry:
      - root: HKLM
        key: SOFTWARE\Example\Hello
        type: key
        removeOnUninstall: true       # ForceDeleteOnUninstall="yes"
      - root: HKLM
        key: SOFTWARE\Example\Hello
        name: InstallDir
        value: '[INSTALLFOLDER]'
      - root: HKLM
        key: SOFTWARE\Example\Hello
        name: Port
        type: dword                   # Type="integer"
        value: 8080
      - root: HKLM
        key: SOFTWARE\Example\Hello\Plugins
        type: key                     # ForceCreateOnInstall="yes"
    removeRegistry:
      - root: HKLM
        key: SOFTWARE\Example\HelloOld   # RemoveRegistryKey Action="removeOnInstall"
```

| WiX `Type` | `type` |
|---|---|
| `string` | `string` (the default) |
| `expandable` | `expandString` |
| `multiString` | `multiString`, with a list as `value` |
| `integer` | `dword` |
| `binary` | `binary` |

Values the package writes are removed on uninstall, as in WiX.
`removeRegistry` removes keys and values that exist before the install,
when the component is installed. `RemoveRegistryKey` with
`Action="removeOnUninstall"` is a `removeRegistry` entry with
`on: uninstall`: it deletes the key with its subkeys when the component is
removed, and the install does not create it. A `type: key` entry with
`removeOnUninstall: true` deletes the key at uninstall too, but also
creates it at install.
A `RegistryKey` with `ForceDeleteOnUninstall="yes"` and no values, also none in
nested keys, only deletes the key, so convert writes it as such a
`removeRegistry` entry.

## Environment variables

| WiX `Environment` | `environment` entry |
|---|---|
| `Action="set"` `Part="all"` (or no `Part`) | `action: set` |
| `Action="set"` `Part="last"` | `action: append` |
| `Action="set"` `Part="first"` | `action: prepend` |
| `Action="create"` | `action: setIfAbsent` |
| `Action="remove"` | `action: remove` |
| `System="yes"`, `System="no"` | `scope: machine`, `scope: user` |
| `Permanent="yes"` | `removeOnUninstall: false` |

Drop the `BroadcastEnvironmentChange` custom action reference that WiX
packages often carry. Windows Installer itself tells running programs that
the environment changed, after the install, a repair and the uninstall, so
programs started from Explorer afterwards see the new value. A command
prompt that is already open keeps the old `PATH`; open a new one.

## Shortcuts

A shortcut goes on the file it starts. msi-builder adds the folder cleanup
and the HKCU key path that WiX needs a separate component for:

```xml
<Component Id="StartMenu" Directory="ProgramMenuDir" Guid="...">
  <Shortcut Name="Hello" Description="Say hello" Target="[INSTALLFOLDER]hello.exe"
            WorkingDirectory="INSTALLFOLDER" />
  <RemoveFolder Id="RemoveProgramMenuDir" On="uninstall" />
  <RegistryValue Root="HKCU" Key="Software\Example\Hello" Name="installed"
                 Type="integer" Value="1" KeyPath="yes" />
</Component>
<Component Id="Website" Directory="ProgramMenuDir" Guid="...">
  <util:InternetShortcut Id="HelloWebsite" Name="Hello website" Target="https://example.com" />
</Component>
```

```yaml
components:
  - files:
      - source: hello.exe
        shortcuts:
          - name: Hello
            directory: ProgramMenuFolder/Example
            description: Say hello
  - id: Website
    shortcuts:
      - name: Hello website
        directory: ProgramMenuFolder/Example
        url: https://example.com
```

A shortcut starts in its program's folder. Shortcuts to folders and to
programs outside the package are component shortcuts with `folder` or
`program`. Advertised shortcuts (`Advertise="yes"`) are not supported.

`msi-builder convert` moves a shortcut onto its file only when the file is
in the shortcut's own component; on a file of another component it would
take that component's lifetime, such as `permanent`. Otherwise the shortcut
stays a component shortcut: a `[#file]` target becomes `file`, and a
`[DIRECTORY]name` target stays the same path as `program`.

## Services

```xml
<ServiceInstall Name="Hello" DisplayName="Hello Service" Description="Answers hello requests."
                Type="ownProcess" Start="auto" ErrorControl="normal" Vital="yes"
                Account="NT AUTHORITY\NetworkService" Arguments="--config &quot;[INSTALLFOLDER]hello.yaml&quot;">
  <ServiceDependency Id="Tcpip" />
  <ServiceConfig DelayedAutoStart="yes" OnInstall="yes" OnReinstall="yes" />
  <util:ServiceConfig ResetPeriodInDays="1" RestartServiceDelayInSeconds="120"
                      FirstFailureActionType="restart" SecondFailureActionType="restart"
                      ThirdFailureActionType="restart" />
</ServiceInstall>
<ServiceControl Id="HelloControl" Name="Hello" Start="install" Stop="both" Remove="uninstall" Wait="no" />
<ServiceControl Id="OldControl" Name="HelloLegacy" Stop="install" Remove="install" />
```

```yaml
components:
  - files:
      - source: hello.exe
    services:
      - name: Hello
        displayName: Hello Service
        description: Answers hello requests.
        start: auto                   # auto, demand or disabled
        vital: true
        account: NT AUTHORITY\NetworkService
        arguments: --config "[INSTALLFOLDER]hello.yaml"
        dependencies: [Tcpip]
        delayedAutoStart: true        # ServiceConfig DelayedAutoStart
        control:
          wait: false                 # ServiceControl Wait="no"
        recovery:                     # util:ServiceConfig
          resetAfterDays: 1
          actions: [restart, restart, restart]
          restartDelay: 120s
    serviceControl:                   # services the package does not install
      - name: HelloLegacy
        stopOnInstall: true
        deleteOnInstall: true
```

The service must be in the component whose file is the service program.
`ServiceControl` for the package's own service is in `control`, and only
needs keys where it differs from the defaults (`startOnInstall`,
`stopOnInstall`, `stopOnUninstall` and `deleteOnUninstall` are on, `wait`
is `true`).

## Event sources, firewall rules and permissions

```xml
<util:EventSource Name="Hello" Log="Application"
                  EventMessageFile="%SystemRoot%\System32\EventCreate.exe" />
<fw:FirewallException Id="HelloRule" Name="Hello" Program="[#hello.exe]"
                      Protocol="tcp" Port="8080" Scope="any" />
<CreateFolder Directory="DATADIR">
  <util:PermissionEx User="NT AUTHORITY\NetworkService" GenericAll="yes" />
</CreateFolder>
```

```yaml
components:
  - id: Hello
    files:
      - source: hello.exe
    eventLog:
      - source: Hello
        log: Application
        messageFile: '%SystemRoot%\System32\EventCreate.exe'
  - directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    folderPermissions:
      grant:
        - account: NT AUTHORITY\NetworkService
          rights: full                # GenericAll="yes"

firewall:
  - name: Hello
    component: Hello                  # the component the rule is installed with
    program: '[#hello.exe]'
    protocol: tcp
    port: 8080
```

- `util:PermissionEx` adds rights and keeps the permissions the object
  already has; so does `grant`. The rights are `full`, `modify`,
  `readExecute`, `read` and `write`, and more in
  [YAML Schema Design](design/yaml-schema.md). `GenericWrite` becomes
  `'0x120116'`, because `write` leaves out the right to read the
  permissions.
- The core `PermissionEx` with an `Sddl` attribute, and `Permission`,
  replace the permissions; write an SDDL string in `sddl` for those.
  For a `Permission`, add `(A;OICI;FA;;;SY)`: Windows Installer also
  gives SYSTEM full access to every object it locks through
  `Permission`, which a WiX package reads back as
  `D:P(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)` for `User="Everyone"`.
  Registry values take only `sddl`, so a `util:PermissionEx` on a registry
  key becomes an SDDL string too.
- `Scope="any"` is the default; `remoteAddresses` limits the rule and
  `profiles` the networks.

## Properties and searches

```xml
<Property Id="PORT" Value="8080" />
<Property Id="API_TOKEN" Hidden="yes" />
<Property Id="OLD_INSTALLDIR">
  <RegistrySearch Id="OldDir" Root="HKLM" Key="SOFTWARE\Example\Hello"
                  Name="InstallDir" Type="directory" />
</Property>
<Launch Condition="Installed OR VersionNT64" Message="Hello needs 64-bit Windows." />
```

```yaml
properties:
  PORT: "8080"
  API_TOKEN: { hidden: true }
  OLD_INSTALLDIR:
    search:
      registry:
        root: HKLM
        key: SOFTWARE\Example\Hello
        name: InstallDir
        type: directory

launchConditions:
  - condition: Installed OR VersionNT64
    message: Hello needs 64-bit Windows.
```

`DirectorySearch`, `FileSearch` and `ComponentSearch` are `directory`,
`file` and `component` searches; a list of searches takes the first one
that finds something. `ComponentSearch Type="directory"`, for another product's
component keyed on its folder, is `keyPath: directory`; without it the
search returns the folder's parent (tested on Windows). For a component
of the same package msi-builder picks the type from the component itself,
and `convert` leaves `keyPath` out. One thing works differently: in WiX, a search
overwrites a value given on the command line. In msi-builder the command
line wins, and the search result is used only when the property is still
empty.

A `Launch` whose condition is exactly `NOT <newer property>`, such as
`NOT NEWERVERSIONDETECTED` next to a hand-made `Upgrade`, is the launch
condition that `upgrade.downgrade: error` (the default) writes itself, so it
cannot also be a `launchConditions` entry: its message is
`upgrade.downgradeMessage`, and `convert` moves it there. When the
`MajorUpgrade` has a `DowngradeErrorMessage`, that message stays, and when
the two messages differ a `# TODO` names the one left out. When the file has
no upgrade elements at all, a `# TODO` reminds you to add the launch
condition back if you set `upgrade: false`. Any other condition, such as
`NOT NEWERVERSIONDETECTED OR Installed`, stays a launch condition, and so
does every condition when downgrades are allowed or when the property
belongs to another product's `Upgrade` row.

## Custom actions

Each `CustomAction` becomes an entry of `customActions`, and its `Custom`
element in a sequence becomes a `schedule` placement of that entry:

| WiX `CustomAction` | msi-builder |
|---|---|
| `Property` + `Value`, or `SetProperty` | `setProperty: {name, value}` |
| `Directory` + `Value`, or `SetDirectory` | `setDirectory` |
| `FileRef` (v3 `FileKey`) + `ExeCommand`; `Directory` + `ExeCommand` | `run` (hidden window, output in the log) or `exec` |
| `Property` + `ExeCommand` | `execProperty` |
| `WixQuietExec` / `CAQuietExec` with the command in a property | `run` |
| `BinaryRef` (v3 `BinaryKey`) + `DllEntry` | `dll` with a `binaries` entry |
| `BinaryRef` + `ExeCommand` | `exe` with a `binaries` entry |
| `Error` | `error` |
| `Execute`, `Impersonate`, `Return`, `HideTarget` | `execute`, `impersonate`, `return`, `hideTarget` |
| `ProgressText` for the action (in `UI`) | `progress`; a `Template` has no counterpart |

| WiX `Custom` | `schedule` entry |
|---|---|
| In `InstallExecuteSequence`, `InstallUISequence` | `sequence: execute`, `sequence: ui` |
| `Before`, `After`, `Sequence` | `before`, `after`, `at` |
| `Condition` (v3: the element's text) | `condition` |

```xml
<Property Id="HELLO_ARGS" Secure="yes" />
<CustomAction Id="SetHelloArgs" Property="HELLO_ARGS" Value="--config &quot;[INSTALLFOLDER]hello.yaml&quot;" />
<CustomAction Id="StopClients" FileRef="hello.exe" ExeCommand="detach --all"
              Execute="deferred" Impersonate="no" Return="ignore" />
<InstallExecuteSequence>
  <Custom Action="SetHelloArgs" Before="InstallFiles" Condition="NOT HELLO_ARGS" />
  <Custom Action="StopClients" Before="StopServices" Condition="REMOVE~=&quot;ALL&quot; AND NOT UPGRADINGPRODUCTCODE" />
</InstallExecuteSequence>
```

```yaml
properties:
  HELLO_ARGS: {}

customActions:
  - id: SetHelloArgs
    setProperty: { name: HELLO_ARGS, value: '--config "[INSTALLFOLDER]hello.yaml"' }
    schedule:
      - { sequence: execute, before: InstallFiles, condition: NOT HELLO_ARGS }
  - id: StopClients
    run:
      command: '"[#hello.exe]" detach --all'
    execute: deferred
    impersonate: false
    return: ignore
    schedule:
      - { sequence: execute, before: StopServices, when: uninstall }
```

`when` replaces the usual conditions, and is combined with `condition`
when both are given:

| WiX condition | `when` |
|---|---|
| `NOT Installed AND NOT WIX_UPGRADE_DETECTED` | `install` |
| `WIX_UPGRADE_DETECTED` | `upgrade` |
| `REMOVE~="ALL" AND NOT UPGRADINGPRODUCTCODE` | `uninstall` |
| `Installed AND NOT REMOVE~="ALL"` | `maintenance` (repair and change) |

Actions with the same `before` or `after` run in the order of the file, as
in WiX. Prefer the keys of the sections above over a custom action where
one fits: services, registry values, environment variables and permissions
undo themselves on uninstall and roll back on a failed install.

## The setup wizard

| WiX | msi-builder |
|---|---|
| `WixUI_Minimal` | `ui.enabled: true` with `ui.license` |
| `WixUI_InstallDir` | `ui.enabled: true` with `chooseDirectory: true` |
| `WixUI_FeatureTree`, `WixUI_Mondo` | `ui.enabled: true` with `chooseFeatures: true` |
| `WixUI_Advanced` | `package.scope: perUserOrMachine` or `perMachineOrUser`, which adds a scope page, with `chooseDirectory: true` and `chooseFeatures: true` |
| `WixUILicenseRtf` | `ui.license` (`.rtf` or text) |
| `WixUIBannerBmp`, `WixUIDialogBmp` | `ui.theme.banner`, `ui.theme.dialog` (BMP, PNG or JPEG, same sizes) |
| `WIXUI_EXITDIALOGOPTIONALCHECKBOX` with `WixShellExec` | `ui.launch.file` |
| `WIXUI_EXITDIALOGOPTIONALTEXT` | `ui.finishText` |
| A `Dialog` that asks for properties | `ui.dialogs` with `fields` |
| `.wxl` strings | `ui.strings`, and `ui.languages` for more languages in one package |

```yaml
ui:
  enabled: true
  license: license.rtf
  chooseDirectory: true
  launch:
    file: hello.exe
```

`WixUI_Advanced` sets the per-user folder to `%LOCALAPPDATA%\Apps\<folder>`
(seen in a real-world port, also with `/qn`). msi-builder installs per
user to `%LOCALAPPDATA%\Programs\<folder>`. A per-user msi-builder upgrade of
such an installation should therefore move the files there.

Per machine, `WixUI_Advanced` installs to
`[ProgramFilesFolder][ApplicationFolderName]`, also with `/qn` (seen in real-world
packages), or to the value of an action that sets `WixPerMachineFolder`. It
does not use the name of the install folder's Directory. convert takes
`installDir.name` and `parent` from that folder. In a 64-bit package
`ProgramFilesFolder` is `Program Files (x86)`, and convert adds a note to keep
it for installed versions or switch to `ProgramFiles64Folder`.

The wizard pages are msi-builder's own, with its own bitmaps. It has no
setup type page (Typical, Custom, Complete) like `WixUI_Mondo`, and dialogs
with a free layout of controls cannot be ported; `ui.dialogs` lays out
text, password, checkbox and choice fields for you. See
[YAML Schema](YAML%20Schema.md#show-a-setup-wizard) for the wizard recipes.

## Removing files

```xml
<Component Id="Logs" Directory="LOGSDIR" Guid="..." KeyPath="yes">
  <CreateFolder />
  <RemoveFile Id="RemoveLogs" Name="*.log" On="uninstall" />
</Component>
<Component Id="Data" Directory="DATADIR" Guid="...">
  <util:RemoveFolderEx On="uninstall" Property="DATADIR" />
</Component>
```

```yaml
components:
  - files:
      - source: hello.exe
  - directory: logs
    createFolder: true
    removeFiles:
      - name: '*.log'                 # one folder, no subfolders
        on: uninstall                 # install, uninstall (the default) or both
  - directory: CommonAppDataFolder/Example/Hello
    createFolder: true
    purge: true                       # util:RemoveFolderEx: the folder and everything in it
```

`purge` deletes the component's folder on uninstall, but not when an
upgrade replaces the product. The folder must be outside the install
folder, written as a path below a standard folder like
`CommonAppDataFolder/Example/Hello`; a public directory ID such as
`DATADIR`, which the command line could point anywhere, is rejected. It
takes a `condition`, such as `DELETE_DATA="1"`.

When the folder comes from a property, as `Property="APPLICATIONFOLDER"`
with a registry search does, or is a public directory, use
`removeFolders` instead. The install folder needs `installDir.remember`,
the counterpart of the RegistrySearch that `RemoveFolderEx` packages use
to find it again at uninstall, and any other property a `properties`
search:

```yaml
installDir:
  id: INSTALLFOLDER
  name: Hello
  remember: true                      # the RegistrySearch that RemoveFolderEx needs

components:
  - files:
      - source: hello.exe
    removeFolders:
      - property: INSTALLFOLDER       # util:RemoveFolderEx Property="INSTALLFOLDER"
        on: uninstall                 # install, uninstall (the default) or both
```

Like `purge`, it keeps the folder when an upgrade replaces the product.
Unlike `RemoveFolderEx`, which hands the subfolders to the engine's file
removal, it moves the folder aside and deletes it at the end without
following junctions, so a failed uninstall puts it back and a junction
swapped in during the uninstall does not redirect the deletion (tested
on Windows). It removes a folder the package
installs to on uninstall only. It refuses folders in the Windows folder,
folders that hold a standard folder, folders behind junctions and the
Public profile (tested), and drive roots, standard folders such as
Program Files and other users' profiles (expected, not tested). A folder that is or
holds a folder of the package goes only when the whole product is removed,
not with one feature, on install or on repair, whichever property names it,
for example one a `SetDirectory` or `SetProperty` points at `INSTALLFOLDER`
(tested for an upgrade, expected for a repair and a feature removal).

`msi-builder convert` turns a `util:RemoveFolderEx` on the component's
own folder into `purge` when that folder is a fixed one (no directory up
to its root can point elsewhere): one without `Property`, one whose
`Property` is that directory, and one whose property only reads the
folder back, by a `RegistrySearch`, from a registry value of that
component. Any other `util:RemoveFolderEx` becomes a `removeFolders`
entry. On a directory of the package `On="install"` is left out with a
note, and `On="both"` keeps only its uninstall half, also with a note.

## Signing

`signtool sign` after `wix build` becomes a flag of the build, or the
`sign` command:

```shell
msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD \
  --timestamp-url http://timestamp.digicert.com -o hello.msi msi.yaml
```

msi-builder signs on Linux and macOS too; see [Signing](signing.md).

## Not supported

| WiX | What to do instead |
|---|---|
| Drivers (`difx:Driver`, DIFxApp, driver install custom actions) | Not supported |
| `util:XmlFile`, `util:XmlConfig` | Ship the complete file, or edit it with a `run` action (no rollback) |
| `util:EventManifest` | Install the manifest file and register it with `wevtutil`, see below |
| `CopyFile`, `DuplicateFile` of a package file | Install the file twice, in two components |
| `MoveFile`, `CopyFile` of a file already on the machine | A `run` action |
| `IniFile` | Ship the complete file; `.url` shortcuts are supported |
| `Class`, `TypeLib`, `AppId` (COM) | Write the registry values with `registry` |
| `util:User`, `util:Group`, `util:FileShare`, `util:PerformanceCategory`, IIS, SQL and HTTP extensions | A `run` action, if you need it |
| `util:BroadcastEnvironmentChange` | Drop it: Windows Installer broadcasts environment changes itself; see [Environment variables](#environment-variables) |
| `util:BroadcastSettingChange` | None; if your `registry` values are a setting that Windows reads only on `WM_SETTINGCHANGE`, send it from a `run` action |
| `DiskPrompt`, `Media DiskPrompt` and `VolumeLabel` | Leave them out: they only matter for packages on several disks |
| Several `Media` disks, spanned cabinets | Large payloads are split into several cabinets automatically; above 2 GB, `package.embedCabinets: false` puts them beside the `.msi` |
| XML fragments, `<?include?>`, raw table XML | No XML input; `tables` adds tables the schema does not cover, but cannot change the standard ones |
| Hand-built dialogs | `ui.dialogs` fields |
| Merge modules (`Merge`, `MergeRef`), patches, transforms (`.mst`), Burn bundles | Never supported; ship a new package instead of a patch |
| Advertised shortcuts and installs (`Advertise="yes"`) | Never supported |
| VBScript and JScript custom actions | Never supported; use a `run` action |
| `WixExitEarlyWithSuccess` | A launch condition, which stops the install as a failure |

An event manifest needs its file installed and two `run` actions, one that
imports it on install and one that removes it on uninstall. msi-builder has
not tested this on Windows:

```yaml
components:
  - files:
      - source: hello.exe
      - source: hello.man

customActions:
  - id: ImportManifest
    run:
      command: '"[System64Folder]wevtutil.exe" im "[#hello.man]"'
    execute: deferred
    impersonate: false
    return: check
    schedule:
      - { sequence: execute, after: InstallFiles, condition: NOT REMOVE~="ALL" }
  - id: RemoveManifest
    run:
      command: '"[System64Folder]wevtutil.exe" um "[#hello.man]"'
    execute: deferred
    impersonate: false
    return: ignore
    schedule:
      - { sequence: execute, before: RemoveFiles, when: uninstall }
```

## Checking the port

1. `msi-builder validate msi.yaml` checks the file without building it.
   Every error names the key, such as `components[1].files[0].source`.
2. Compare the tables with `msi-builder inspect --table <name>` on both
   packages, for example `Registry`, `ServiceInstall` and `Environment`.
   Generated IDs and GUIDs differ; values and paths should not.
3. On a test machine, install your last WiX release, then install the new
   package over it as an upgrade, with a log:
   `msiexec /i hello.msi /qn /l*v upgrade.log`. Check that the files,
   services and settings are still there, then uninstall and check that
   nothing is left behind.

The upgrade from WiX is where a changed component GUID shows. With the
default `upgrade.removeOld` (`afterInstallInitialize`), or
`afterInstallValidate`, the old version is removed before the new one is
installed, and new GUIDs do no harm. With `afterInstallExecute`, which
`preserve: true` also selects, the new version is installed first: it
skips files that are already there, and removing the old version then
deletes the files, registry values, environment variables and shortcuts
of every component whose GUID changed (case UPG-11 in the spec). Keep the WiX
GUIDs with `guid` on those components, or keep the early removal.
`msi-builder convert --msi old.msi` does this for you: it takes the GUID
of each component from the WiX package, by its `Id` or, for a component
without one, by its key file or its registry key path (root, key and
value name), also for a shortcut component, which keeps
its shortcut (a shared GUID kept the shortcut in UPG-11; with WiX's own
key path value this is inferred), and leaves a `# TODO` on any
component it cannot find there. Such a kept shortcut component gets
msi-builder's own HKCU key path, so the HKCU value WiX wrote for it
stays behind after a late removal and the uninstall; convert names it in a
`# TODO`, and a `removeRegistry` entry in another component removes it. Without `--msi`, convert leaves one `# TODO` at
`upgrade.removeOld` that names the components without a WiX `Guid`.
Advertised WiX shortcuts are lost in that upgrade even with every GUID
kept: msi-builder installs plain shortcuts at the same paths, and the late
removal of the WiX package then deletes them (seen in a real-world port).
Convert leaves a `# TODO` for them; remove the old version early, at least
for the first upgrade from WiX, or give the shortcuts other names (both
untested for advertised shortcuts).
`msi-builder inspect --table Component old.msi` shows the GUIDs of the WiX
package, also the ones WiX generated. WiX gives each file of a `Files`
glob a component and GUID of its own; the folder source the glob becomes
keeps them in `guids`, by the path of each file below the folder, and
convert with `--msi` fills it in from the WiX package (UPG-12 upgraded a
WiX `Files` package this way and kept every file). A Heat harvest is a
list of `Component` elements, which convert keeps as components with
their `guid` pinned; to replace them with a folder source by hand, move
each GUID into `guids`:

```yaml
components:
  - files:
      - source: dist/
        guids:
          app.exe: '{6D1C0A42-93B8-4E57-A2F1-0C7E5D9B3A61}'
          lib/readme.txt: '{2B8F4E17-5A0C-4D93-B6E2-9F1A3C7D5E08}'
```

Files added in a later version need no entry. Without `--msi`, convert
leaves a `# TODO` on a WiX `Files` glob it turned into a folder source
under a late removal.
