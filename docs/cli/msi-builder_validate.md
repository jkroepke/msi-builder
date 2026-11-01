# `msi-builder validate`

Check a YAML file without building a package

Validate loads msi.yaml, checks it against the schema and runs the
configuration checks of a build. It reads no input files: checks that need
them, such as PE versions, run only in build. Diagnostics go to stderr; the
exit code is 1 when there is an error.

## Usage

```text
msi-builder validate [flags] msi.yaml
```

## Examples

```text
  msi-builder validate --var VERSION=1.2.3 msi.yaml
```

## Flags

```text
      --env              let environment variables fill {{ .NAME }}
  -h, --help             help for validate
      --var NAME=VALUE   set a variable read as {{ .NAME }}; repeatable
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
