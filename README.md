[![CI](https://github.com/jkroepke/msi-builder/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/jkroepke/msi-builder/actions/workflows/ci.yaml)
[![codecov](https://codecov.io/gh/jkroepke/msi-builder/graph/badge.svg)](https://codecov.io/gh/jkroepke/msi-builder)
[![GitHub license](https://img.shields.io/github/license/jkroepke/msi-builder)](https://github.com/jkroepke/msi-builder/blob/main/LICENSE)
[![Current Release](https://img.shields.io/github/release/jkroepke/msi-builder.svg?logo=github)](https://github.com/jkroepke/msi-builder/releases/latest)
[![GitHub Repo stars](https://img.shields.io/github/stars/jkroepke/msi-builder?style=flat&logo=github)](https://github.com/jkroepke/msi-builder/stargazers)

# msi-builder

⭐ Don't forget to star this repository! ⭐

## About

**YAML in, MSI out.** msi-builder turns one short YAML file into a Windows
Installer (MSI) package. Write down what to install, run one command, ship the
`.msi`. No XML, no database tables to fill in, no component GUIDs to manage
(just one upgrade code you generate once) and no Windows machine to build on.

```text
msi.yaml + your files  ──▶  msi-builder build  ──▶  app.msi
```

msi-builder is a single, self-contained binary, so the same command builds the
package on Linux, macOS and Windows: on your laptop, in a container or in
a CI job next to the rest of your release.

- **Easy to write.** Say what you want: files, whole folders, shortcuts,
  services, registry values, firewall rules and actions. msi-builder works
  out the tables, component GUIDs, short names, attribute bits and sequence
  numbers. Editors complete and check the file through its JSON schema.
- **Sensible defaults.** A dozen lines make a working package that installs
  to Program Files and replaces an older version on upgrade. Spell out a
  default only when it does not fit.
- **One tool for the whole job.** Validate, build, sign and verify with the
  same binary; one command turns the YAML into a finished MSI. The
  Authenticode signature and its RFC 3161 timestamp need no Windows SDK or
  `signtool`.
- **Safe by default.** Most configurations that Windows would accept and
  then install in a broken state are rejected at build time, each with the
  YAML path of the problem.
- **Reproducible.** The same configuration, inputs and `SOURCE_DATE_EPOCH`
  produce a byte-identical MSI.
- **Focused.** msi-builder only builds and signs MSI packages. For MSIX
  packages, use [nFPM](https://github.com/goreleaser/nfpm).

> [!NOTE]
> msi-builder is under active development and has not reached 1.0 yet, so the
> YAML schema can still change between releases.

## Example

This is a complete package:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/jkroepke/msi-builder/main/pkg/config/schema.json
# The smallest useful package: one executable, silent install only.
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

The file goes to `C:\Program Files\Hello`, which `INSTALLDIR=…` can change,
and a newer version replaces an installed one. Every default can be spelled
out; the [YAML schema design](docs/design/yaml-schema.md) lists them. Validate and
build it on any operating system:

```shell
msi-builder validate msi.yaml
msi-builder build -o hello.msi msi.yaml
```

To sign the package, pass a key and its certificate chain:

```shell
msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD \
  --timestamp-url http://timestamp.digicert.com -o hello.msi msi.yaml
msi-builder verify hello.msi
```

See [Signing](docs/signing.md) for PEM keys, `msi-builder sign` and the Go API.

A whole build output can be added without listing each file: a `source`
that ends in `/` is a folder, and every file below it gets a component of
its own:

```yaml
components:
  - files:
      - source: dist/
        exclude: '*.pdb'
```

Packages can also be built from Go without a YAML file; see the
[Go API](docs/go-api.md).

A complete package with a Windows service, a firewall rule, an event log source
and custom actions is in
[`examples/windows_exporter`](examples/windows_exporter/msi.yaml).

## Out of scope

msi-builder aims at simple, single-product installers such as an exporter or an
agent with a service. These are not supported:

- **MSIX.** Use [nFPM](https://github.com/goreleaser/nfpm).
- **Running packages.** msi-builder never installs or executes an MSI. Only its
  tests do, on Windows without `-short` (elevated).
- **Standalone transforms (`.mst`), patches (`.msp`), merge modules
  (`.msm`) and bundles or bootstrappers.** Ship a new major-upgrade MSI
  instead of a patch. Multi-language packages are supported: `ui.languages`
  builds one MSI with an embedded language transform per further language
  ([YAML schema](docs/design/yaml-schema.md)).
- **Spanned cabinets.** A file never continues into the next cabinet. The
  payload is split at file boundaries into as many cabinets as it needs; a
  package whose cabinets add up to more than 2 GB keeps them beside the
  `.msi` (`package.embedCabinets: false`). A single file is at most
  2 GiB − 32 KiB. Cabinets are LZX (the default), MSZIP or stored.
- **Script custom actions** (VBScript, JScript).
- **Editing existing packages and XML input.** `msi-builder inspect` only
  reads packages to help debugging. `msi-builder convert` turns a WiX
  source file into a YAML starting point once; builds never read XML.
- **Advertised installs and advertised shortcuts.**
- **Hand-built dialogs.** The UI is a built-in wizard plus optional form
  pages that ask for properties; there is no free control layout.

## Installation

Download a binary for Linux, macOS or Windows from the
[releases page](https://github.com/jkroepke/msi-builder/releases/latest), or install
it with Go:

```shell
go install github.com/jkroepke/msi-builder/cmd/msi-builder@latest
```

or run the container image for Linux on amd64 and arm64:

```shell
docker run --rm -v "$PWD:/work" ghcr.io/jkroepke/msi-builder build -o hello.msi msi.yaml
```

`msi-builder completion <shell>` prints a completion script for Bash, Zsh, fish or
PowerShell; `msi-builder completion <shell> --help` shows how to load it.

## Documentation

The documentation lives in the [wiki](https://github.com/jkroepke/msi-builder/wiki):

- [Getting Started](https://github.com/jkroepke/msi-builder/wiki/Getting-Started)
- [End user guide](https://github.com/jkroepke/msi-builder/wiki/End-User-Guide) for the people who install your packages
- [Command-line reference](https://github.com/jkroepke/msi-builder/wiki/Command-Line)
- [YAML schema](https://github.com/jkroepke/msi-builder/wiki/YAML-Schema): a recipe for each common task
- [YAML schema design](https://github.com/jkroepke/msi-builder/wiki/YAML-Schema-Design): every key, default and rule
- [Signing](https://github.com/jkroepke/msi-builder/wiki/Signing)
- [Go API](https://github.com/jkroepke/msi-builder/wiki/Go-API)
- [MSI specification](https://github.com/jkroepke/msi-builder/wiki/MSI-Specification) and its
  [Windows test results](https://github.com/jkroepke/msi-builder/wiki/Windows-Test-Results)

## Contributing

See the [contributing guide](CONTRIBUTING.md) to propose and submit changes, and
the [code of conduct](CODE_OF_CONDUCT.md) for how we work together. Report
security issues as described in the [security policy](SECURITY.md).

## License

This project is licensed under the [Apache License 2.0](LICENSE).
