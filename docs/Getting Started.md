# Getting Started

This guide builds a first package: one executable that installs to
`C:\Program Files\Hello`. You need msi-builder on any operating system, see
[Installation](Installation.md), and a Windows program to package, here
`hello.exe`.

## Write the configuration

Save this file as `msi.yaml` next to `hello.exe`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json
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

Generate your own `upgradeCode` once and keep it for every later version: it
ties the versions of a product together, so a newer version replaces an
installed one. The first line lets editors with the YAML language server
complete and check the file.

The [YAML Schema](YAML%20Schema.md) page explains what this package does by
default and how to change it.

## Validate and build

```shell
msi-builder validate msi.yaml
msi-builder build -o hello.msi msi.yaml
```

`validate` checks the file against the schema and runs the configuration
checks of a build, without reading the input files. It rejects configurations
that Windows would accept and then install in a broken state. `build` also
checks the input files, such as the version of `hello.exe`, and writes
`hello.msi`. With the same configuration, inputs and
`SOURCE_DATE_EPOCH`, the package is byte-identical on every build.

## Sign and verify

Windows shows an unknown publisher for an unsigned package. Pass a key and its
certificate chain to sign while building:

```shell
msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD \
  --timestamp-url http://timestamp.digicert.com -o hello.msi msi.yaml
msi-builder verify hello.msi
```

[Signing](signing.md) covers PEM keys, signing an existing package with
`msi-builder sign`, and the hash algorithms.

## Install

On Windows, open PowerShell as an administrator (**Run as administrator**),
change to the folder with `hello.msi`, then install the package silently and
remove it again:

```powershell
cd C:\path\to\hello
Start-Process msiexec -ArgumentList '/i hello.msi /qn /norestart INSTALLDIR="D:\Apps\Hello"' -Wait
Start-Process msiexec -ArgumentList '/x hello.msi /qn /norestart' -Wait
```

The package installs for all users, which needs administrator rights, and a
silent install (`/qn`) cannot ask for them: from a prompt that is not
elevated, it fails. `msiexec` returns before it has finished, so `-Wait`
keeps the uninstall from starting while the install still runs.
`INSTALLDIR` is optional and changes the target directory. The
[End User Guide](End%20User%20Guide.md) covers the other options, exit
codes and log files.

## Next steps

- Add shortcuts, services, registry values, a firewall rule or the install
  wizard: see the recipes of the [YAML Schema](YAML%20Schema.md). The
  [windows_exporter example](../examples/windows_exporter/msi.yaml) uses most
  of them.
- Coming from WiX: [`msi-builder convert`](cli/msi-builder_convert.md) turns
  a `.wxs` file into a first `msi.yaml` and marks what it could not convert
  with `# TODO` comments.
- Inspect a package: [`msi-builder inspect`](cli/msi-builder_inspect.md) prints its
  summary, streams and tables.
- Build packages from Go code: [Go API](go-api.md).
