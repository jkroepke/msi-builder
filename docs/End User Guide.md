# End User Guide

This page is for the people who install a package that msi-builder built:
users who double-click the `.msi` and administrators who deploy it with a
script or a management tool. Every package is a standard Windows Installer
package, so it is installed, upgraded and removed with `msiexec`, which is
part of Windows.

Some details depend on how the package author configured the package, such
as the name of the install folder property or the features to choose from.
The examples use `hello.msi`, a product named Hello, and the defaults that
msi-builder uses when the author does not change them. Package authors can
link to this page and list their own properties next to it, see
[For package authors](#for-package-authors).

## Install

Double-click the `.msi` file, or run:

```bat
msiexec /i "C:\Downloads\hello.msi"
```

A package with a setup wizard shows it; a package without one installs
straight away. A package for all users (the default) needs administrator
rights. A package that installs only for the current user usually needs
none on Windows client versions; on Windows Server, Windows Installer lets
only administrators install by default.

To check the signature of a signed package before you install it, run this
in PowerShell:

```powershell
Get-AuthenticodeSignature C:\Downloads\hello.msi
```

## Install silently

Add a display option to install without questions:

| Option | What you see |
|---|---|
| `/qn` or `/quiet` | Nothing at all. |
| `/passive` | A progress bar only; nothing to click. May restart without asking, so add `/norestart`. |
| `/qb` | A basic progress window with a Cancel button. |
| none | The full user interface, the same as a double-click. |

```bat
msiexec /i "C:\Downloads\hello.msi" /qn /norestart
```

An install with `/qn` cannot ask for administrator rights. Run it, and a
silent uninstall too, from an elevated command prompt, as SYSTEM, or from a
deployment tool that runs as an administrator; otherwise a package for all
users fails, for example with exit code 1603 and error 1925 in the log.
Always pass `/norestart` in unattended installs, see [Restarts](#restarts).

## Set properties

Properties change how a package installs. Write them after the options as
`NAME=value`, and quote values with spaces:

```bat
msiexec /i hello.msi /qn INSTALLDIR="D:\Apps\Hello"
```

Property names are case-sensitive; write them exactly as shown.

### Install folder

`INSTALLDIR` sets the folder the product installs to. By default the
product goes to `C:\Program Files\<product name>` (`C:\Program Files (x86)`
for a 32-bit package on 64-bit Windows), or to
`%LOCALAPPDATA%\Programs\<product name>` for a package that installs only
for the current user. The package author can change the folder and give the
property another name.

Some packages remember the folder: an upgrade then normally installs to
the folder of the earlier install, if that version remembered it too and
the folder still exists; otherwise it uses the default folder. For
other packages, or if in doubt, pass `INSTALLDIR` again on every upgrade if
you did not use the default folder.

### Features

A package holds one or more features. By default every feature that the
author turned on is installed. Choose features by their IDs, separated by
commas:

```bat
msiexec /i hello.msi /qn ADDLOCAL=Core,Tools
msiexec /i hello.msi /qn ADDLOCAL=ALL
```

On a first install, list every feature you want in `ADDLOCAL`; `REMOVE`
removes features that are already installed. `ADDLOCAL=ALL` also installs
features that are off by default.
A package without features of its own has one feature, `MainFeature`.
The package author documents the feature IDs; you can also list them with
`msi-builder inspect --table Feature hello.msi` on any operating system.

To add or remove a feature after the install, run the same command with the
`.msi` of the installed version and the features to change:

```bat
msiexec /i hello.msi /qn ADDLOCAL=Tools
msiexec /i hello.msi /qn REMOVE=Tools
```

### Package settings

Many packages take their own settings as properties, such as a port number
or a server address. Their names and values are listed in the package's
documentation:

```bat
msiexec /i hello.msi /qn LISTEN_PORT=9000
```

A package does not remember the properties of an earlier install unless its
documentation says so: pass the same properties again on every upgrade.

### For the current user or for all users

Some packages let you choose whether to install for the current user or for
all users. In the wizard this is a page of its own. On the command line:

```bat
rem Only for the current user
msiexec /i hello.msi /qn MSIINSTALLPERUSER=1
rem For all users of the machine
msiexec /i hello.msi /qn MSIINSTALLPERUSER=""
```

A package without this choice ignores the property. Keep one choice for a
product: an upgrade only finds a version installed the same way, so a
product installed once per user and once for all users is installed twice.

### Language

A package with several languages shows the setup wizard in the language of
your regional format, or in its base language when it has no translation
for it. To choose one of the package's languages, pass its language ID:

```bat
msiexec /i hello.msi ProductLanguage=1031
```

1031 is German; the IDs are Windows' locale IDs (LCIDs), such as 1033 for
English (United States) or 1036 for French.

## Upgrade

Install the newer version as you installed the first one, interactively or
silently; you do not need to uninstall the old version first. The newer
version replaces the installed one and normally keeps your feature
choice. Pass the properties of the first install again, see
[Set properties](#set-properties). To change features during an upgrade,
list every feature you want in `ADDLOCAL`: `ADDLOCAL` or `REMOVE` on the
command line replaces the kept feature choice.

By default a package refuses to replace a newer version with an older one.
It then shows a message such as "A later version of Hello is already
installed", and a silent install fails with exit code 1603. To go back to an
older version, uninstall the newer one first.

## Repair

A repair puts back missing files and registry values. In
**Control Panel > Programs and Features**, select the product and choose
**Repair**, or **Change** to choose features, where the package offers
them. On the command line:

```bat
msiexec /fomus hello.msi /qn
```

`/fomus` reinstalls missing and older files and rewrites the registry
values and shortcuts; `/famus` reinstalls every file, also a damaged one of
the same version.

A repair can undo your own changes. It writes every registry value and
shortcut of the package again, so a setting the program keeps in such a
value, or a shortcut you edited, goes back to what the package installed.
`/famus` also overwrites the files of the package that you edited, such as
a configuration file; `/fomus` normally leaves an edited file alone. Back
up such settings before a repair, or ask the package author which ones a
repair resets.

Windows keeps a copy of the installed package, but without its files. A
repair that has to put back or replace a file therefore needs the `.msi`
of the installed version; one that finds every file in place and only
rewrites registry values and shortcuts runs without it. With the product
code in place of the file, or with the **Repair** button, Windows Installer
looks for the `.msi` where it was first installed from; if it has moved or
been deleted, a silent repair that needs it fails with 1612, so pass the
file again.

## Uninstall

Uninstall the product in **Settings > Apps** or in
**Control Panel > Programs and Features**, or with the `.msi` of the
installed version:

```bat
msiexec /x hello.msi /qn /norestart
```

Without the package file, uninstall with the product code, a GUID such as
`{12345678-...}` that identifies the installed product. PowerShell lists the
product codes of the installed products:

```powershell
Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*,
    HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*,
    HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\* |
  Where-Object DisplayName -like 'Hello*' |
  Select-Object DisplayName, DisplayVersion, PSChildName
```

`PSChildName` is the product code. Pass it to `/x` in quotes, which
PowerShell needs:

```bat
msiexec /x "{12345678-1234-1234-1234-123456789012}" /qn /norestart
```

`msi-builder inspect --table Property hello.msi` also shows the product
code, as `ProductCode`. It usually changes with every version of a
product.

## Restarts

Most packages never need a restart. When a file is in use, or when the
package asks for one, Windows Installer finishes the install and asks for a
restart. In a silent (`/qn`) or passive (`/passive`) install it may restart
the machine without asking, so unattended installs pass one of these:

| Option | Effect |
|---|---|
| `/norestart` | Never restart; the install returns 3010 when a restart is needed. |
| `/promptrestart` | Ask the user before a restart. |
| `/forcerestart` | Always restart after the install. |

Deployment tools treat 3010 as success: the product is installed and
finishes setting up at the next restart.

## Log files

When an install fails, a log file shows why. `/l*v` writes a verbose log:

```bat
msiexec /i hello.msi /qn /l*v "%TEMP%\hello-install.log"
msiexec /x hello.msi /qn /l*v "%TEMP%\hello-uninstall.log"
```

Search the log for `Return value 3`: the lines just before it name the step
that failed. Attach the log when you report a problem to the package author.

## Exit codes

`msiexec` returns one of these codes, which scripts and deployment tools
check. In PowerShell and in an interactive command prompt, `msiexec` returns
before the install has finished, so wait for it to read the code:

```powershell
$p = Start-Process msiexec -ArgumentList '/i hello.msi /qn /norestart' -Wait -PassThru
$p.ExitCode
```

```bat
start /wait msiexec /i hello.msi /qn /norestart
echo %ERRORLEVEL%
```

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1602 | The user cancelled the install. |
| 1603 | The install failed, for example because a newer version is installed or administrator rights are missing. Check the log. |
| 1605 | The product is not installed, or the `.msi` or product code belongs to another version. |
| 1612 | The installation source is not available. Repair or install with the `.msi` file. |
| 1618 | Another installation is already running. Try again when it has finished. |
| 1619 | The package could not be opened. Check the path. |
| 1625 | A system policy blocks the install, for example for a standard user on Windows Server. Install as an administrator. |
| 1638 | Another version of this product is already installed. Uninstall it first. |
| 1641 | Success; Windows Installer started the restart. |
| 3010 | Success; a restart is needed to finish. |

Run `msiexec /?` for all options of your Windows version.

## For package authors

Link your users to this page from your release notes or download page.
Next to the link, list what this page cannot know about your package:

- the install folder property, if you set `installDir.id`, and whether
  upgrades keep the folder (`installDir.remember`);
- whether the package has a setup wizard (`ui.enabled`);
- the feature IDs and which features are off by default (`features`);
- your own properties (`properties`) and their values;
- whether the package installs per user, for all users or lets the user
  choose (`package.scope`);
- the languages, if you set `ui.languages`;
- whether upgrades keep the feature choice (`upgrade.migrateFeatures`),
  whether an upgrade can leave the old version installed when its removal
  fails (`upgrade.ignoreRemoveFailure`) and how to remove it then, and
  whether Repair, Change or Uninstall is hidden (`product.arp`).

The [YAML Schema Design](design/yaml-schema.md) describes each of these settings.
