# `msi-builder convert`

Convert a WiX source file to a YAML file

Convert reads a WiX source file (.wxs, WiX v3 or later) and writes the
msi.yaml that describes the same package, as far as msi-builder can express
it. It is a starting point for a port, not a byte-for-byte translation:
everything it cannot convert, such as preprocessor conditions, includes,
bind variables, hand-built dialogs and extensions without a counterpart,
becomes a "# TODO" comment in the YAML file and a line on stderr.

A component whose Guid WiX generates (Guid="*" or none) keeps the code WiX
gives it: convert computes it from the key file or key registry value as
WiX does, for the platform --arch names (default: package.platform, from a
WiX 3 Package Platform or the folders) and, with --bcgg, as WiX 6 and later
do with -bcgg. Each file a WiX Files glob harvests keeps its code in the
guids of the folder source the glob became, when the folder is next to the
source file. With --msi and the package WiX built from the source file,
convert takes the codes from that package instead, finding each component
by its ID, its key file or its registry key path. A component whose code
convert finds neither way gets a "# TODO" comment: msi-builder generates
another code for it, and an upgrade that removes the old version after
installing the new one (MajorUpgrade Schedule="afterInstallExecute", and
presumably "afterInstallFinalize") deletes the files of every component
whose code changed.

The YAML file goes to stdout, or to the file --output names. Review it, then
check it with msi-builder validate.

## Usage

```text
msi-builder convert [flags] app.wxs
```

## Examples

```text
  msi-builder convert app.wxs
  msi-builder convert -o msi.yaml app.wxs
  msi-builder convert --arch x64 -o msi.yaml app.wxs
  msi-builder convert --msi app-1.4.0.msi -o msi.yaml app.wxs
```

## Flags

```text
      --arch string     the platform WiX built for (wix build -arch): x86, x64 or arm64; default package.platform
      --bcgg            WiX 6 or later built the package with -bcgg (WiX 3 codes for registry key paths)
      --force           overwrite an existing output file
  -h, --help            help for convert
      --msi string      keep the component codes of this package, built by WiX from app.wxs
  -o, --output string   write the YAML file here instead of stdout
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
