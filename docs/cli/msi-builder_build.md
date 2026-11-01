# `msi-builder build`

Build a package from a YAML file

Build writes the package that msi.yaml describes. Relative paths in the
YAML file resolve against its directory. The package is read back and
compared with its sources unless --skip-verify is given, and it is signed
when a key is given (see the signing flags and docs/signing.md).

Diagnostics go to stderr; on success one summary line goes to stdout.

## Usage

```text
msi-builder build [flags] msi.yaml
```

## Examples

```text
  msi-builder build -o hello.msi msi.yaml
  msi-builder build --var VERSION=1.2.3 --var ARCH=x64 msi.yaml
  msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD \
    --timestamp-url http://timestamp.digicert.com -o hello.msi msi.yaml
```

## Flags

```text
      --cert string            sign with this PEM certificate chain (signing certificate first)
      --cfb int                compound file version, 3 (512-byte sectors) or 4 (4096-byte sectors) (default 4)
      --description string     program name shown in the UAC prompt
      --env                    let environment variables fill {{ .NAME }}
      --hash string            signature digest: sha256, sha384, sha512 or sha1 (default "sha256")
  -h, --help                   help for build
      --key string             PEM private key for --cert
  -o, --output string          output file (default: <product name>-<version>.msi next to the YAML file, characters a file name cannot hold replaced by _)
      --password-env string    environment variable holding the --pfx password
      --pfx string             sign with the key and certificates in this PKCS #12 file
  -q, --quiet                  print nothing on success
      --skip-verify            do not read the finished package back
      --timestamp-url string   RFC 3161 time-stamping authority
      --url string             more-information link of the signature
      --var NAME=VALUE         set a variable read as {{ .NAME }}; repeatable
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
