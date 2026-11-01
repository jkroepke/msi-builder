# `msi-builder`

Build Windows Installer (MSI) packages from a YAML file

msi-builder builds Windows Installer (MSI) packages from a single YAML file,
signs them with Authenticode and inspects existing packages. It is pure Go
and builds the same packages on Linux, macOS and Windows.

## Usage

```text
msi-builder [command]
```

## Flags

```text
  -h, --help      help for msi-builder
      --version   print the version
```

## See also

- [`msi-builder build`](msi-builder_build.md): Build a package from a YAML file
- [`msi-builder completion`](msi-builder_completion.md): Print a shell completion script
- [`msi-builder convert`](msi-builder_convert.md): Convert a WiX source file to a YAML file
- [`msi-builder inspect`](msi-builder_inspect.md): Print the tables, streams and summary of a package
- [`msi-builder render`](msi-builder_render.md): Print the YAML that a file renders to
- [`msi-builder sign`](msi-builder_sign.md): Add an Authenticode signature to a package
- [`msi-builder validate`](msi-builder_validate.md): Check a YAML file without building a package
- [`msi-builder verify`](msi-builder_verify.md): Check the Authenticode signature of a package
- [`msi-builder version`](msi-builder_version.md): Print the version
