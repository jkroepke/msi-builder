# `msi-builder render`

Print the YAML that a file renders to

Render runs msi.yaml through Go's text/template, as build and validate do
before they read it, and prints the YAML that comes out, the vars header
as it is. A YAML error names a line of this output.

## Usage

```text
msi-builder render [flags] msi.yaml
```

## Examples

```text
  msi-builder render --var ARCH=arm64 msi.yaml
```

## Flags

```text
      --env[=true|false]    let environment variables fill {{ .NAME }}
  -h, --help[=true|false]   help for render
      --var NAME=VALUE      set a variable read as {{ .NAME }}; repeatable
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
