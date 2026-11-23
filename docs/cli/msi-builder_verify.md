# `msi-builder verify`

Check the Authenticode signature of a package

Verify checks the content digest, the signature and the timestamp token
of a signed package and prints the signer. It does not check certificate
trust or revocation; Windows does that at install time. The exit code is 1
when the package is not signed or the signature does not match.

## Usage

```text
msi-builder verify package.msi [flags]
```

## Examples

```text
  msi-builder verify hello.msi
```

## Flags

```text
  -h, --help[=true|false]   help for verify
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
