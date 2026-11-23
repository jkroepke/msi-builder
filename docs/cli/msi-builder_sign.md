# `msi-builder sign`

Add an Authenticode signature to a package

Sign adds an Authenticode signature to a package, replacing an existing
one. It writes in place unless --output is given; a failed signing keeps the
original file. The key comes from a PKCS #12 file (--pfx) or from a PEM
certificate chain and key (--cert and --key). See docs/signing.md.

## Usage

```text
msi-builder sign [flags] package.msi
```

## Examples

```text
  msi-builder sign --pfx codesign.pfx --password-env PFX_PASSWORD hello.msi
  msi-builder sign --cert chain.pem --key key.pem --timestamp-url http://timestamp.digicert.com hello.msi
```

## Flags

```text
      --cert string            sign with this PEM certificate chain (signing certificate first)
      --description string     program name shown in the UAC prompt
      --hash string            signature digest: sha256, sha384, sha512 or sha1 (default "sha256")
  -h, --help[=true|false]      help for sign
      --key string             PEM private key for --cert
  -o, --output string          output file (default: sign in place)
      --password-env string    environment variable holding the --pfx password
      --pfx string             sign with the key and certificates in this PKCS #12 file
  -q, --quiet[=true|false]     print nothing on success
      --timestamp-url string   RFC 3161 time-stamping authority
      --url string             more-information link of the signature
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
