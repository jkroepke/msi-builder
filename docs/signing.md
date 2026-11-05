# Signing

msi-builder signs packages with Authenticode without `signtool`, cgo or the Windows
SDK. The format is described in [MSISPEC §12](spec/MSISPEC.md#12-digital-signatures),
and the Windows results are SIG-01 to SIG-09 in
[MSISPEC-RESULTS](spec/MSISPEC-RESULTS.md).

## Command line

Sign while building, or sign an existing package:

```shell
msi-builder build --pfx codesign.pfx --password-env PFX_PASSWORD -o hello.msi msi.yaml
msi-builder sign --cert chain.pem --key key.pem --timestamp-url http://timestamp.digicert.com hello.msi
msi-builder verify hello.msi
```

| Option | Meaning |
|---|---|
| `--pfx FILE` | PKCS #12 file with the key and the certificate chain |
| `--password-env NAME` | environment variable that holds the `--pfx` password; never pass it on the command line |
| `--cert FILE`, `--key FILE` | PEM certificate chain (signing certificate first) and unencrypted PEM key (PKCS #8, PKCS #1 or SEC 1) |
| `--hash` | `sha256` (default), `sha384`, `sha512` or the legacy `sha1` |
| `--timestamp-url URL` | RFC 3161 time-stamping authority; without one, the signature stops validating when the certificate expires |
| `--description TEXT` | program name in the UAC prompt; `build` uses the product name |
| `--url URL` | "more information" link |

`msi-builder sign` replaces an existing signature and writes in place unless `--output`
(`-o`) is given. `msi-builder verify` checks the content digest, the signature and the
timestamp token, and prints the signer. It does not check certificate trust
or revocation; Windows does that at install time.

## What msi-builder writes

- A basic-mode signature: `\u0005DigitalSignature` only, never
  `\u0005MsiDigitalSignatureEx`. The signature covers the stream contents and
  the root CLSID, not the container layout, so re-laying out the file keeps it
  valid.
- RSA keys of at least 2048 bits with PKCS #1 v1.5, or ECDSA keys. Windows
  Server 2025 accepts RSA with SHA-1/256/384/512 and ECDSA P-256 and P-384
  (SIG-06). ECDSA and SHA-384/512 are not yet tested on older Windows
  versions such as Server 2016; use RSA with SHA-256 to be safe.
- `SpcSpOpusInfo` with the description and URL, and the individual
  code-signing `SpcStatementType`.
- RSA signatures without a timestamp are deterministic: the same package and
  key give the same bytes.

Packages with storages directly under the root, such as the embedded
language transforms of a multi-language package (`ui.languages`), are
signed like any other: the signature covers each storage's streams and CLSID,
and streams named like the signature streams inside a storage are left out.
A storage inside a storage is refused rather than dropped, since msi-builder
does not write deeper trees. `msi-builder verify` reads both.

## External cabinets

With `package.embedCabinets: false`, `msi-builder build` with signing options
signs every cabinet beside the package with the same key, then signs the
package. The package lists each cabinet in `MsiDigitalSignature`, so Windows
Installer refuses to install from a cabinet that was changed, replaced by a
cabinet of another build, or left unsigned (SIG-09). Ship the cabinets
together with the `.msi` they were built with.

The check pins the certificate and each cabinet's signature in the package;
it does not ask Windows whether the certificate is trusted (a self-signed
one passes). It is only as strong as the package signature, which keeps
anyone from replacing the tables along with the cabinets.

- `msi-builder sign` signs only the package; it cannot add cabinet signatures
  to a package built without them, because they change the package's tables.
- Embedded cabinets are covered by the package signature and are not signed
  on their own.
- Windows Server 2025 installs cabinets signed with RSA (SHA-1, SHA-256,
  SHA-384), ECDSA P-256 and with a timestamp (SIG-09). Older Windows
  versions are not yet confirmed for cabinets.
- Each cabinet's signature value is in the package, so an ECDSA key (whose
  signatures are randomized) gives a different package and automatic
  PackageCode on every build. RSA without a timestamp keeps both
  reproducible; a timestamp changes only the cabinets.
  Windows Installer treats an ECDSA rebuild of a release as another
  package of the same product version: it refuses to install it over the
  installed build (error 1638, another version of this product is already
  installed) and, expected but not tested, does not accept it as the
  source of a repair. Do not rebuild a released version with an ECDSA
  key; ship and keep the `.msi` and cabinets of the one build.
- Size limits (`builder.Options.CabinetSize`, 2 GiB) count the cabinet
  without its signature, which adds a few kilobytes to each file.
- `msi-builder verify` checks the package signature only; it does not yet
  check the cabinets beside it against `MsiDigitalSignature`.

## Go API

The [`sign`](../pkg/sign) package takes any `crypto.Signer`, so keys in an
HSM or a cloud KMS work without exporting them:

```go
signed, err := sign.Sign(ctx, pkg, sign.Options{
	Signer:       kmsSigner,
	Certificates: chain,
	TimestampURL: "http://timestamp.digicert.com",
})
```

For signers that only return a finished PKCS #7, `sign.Digest` gives the
content digest to sign and `sign.Attach` stores and verifies the result.
`sign.SignCabinet` and `sign.VerifyCabinet` sign and check a cabinet written
with a signature reserve; the builder does that with
`builder.Options.SignCabinets`.
`sign.Verify` and `sign.Unsign` check and remove a signature.
