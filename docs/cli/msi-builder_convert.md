# `msi-builder convert`

Convert a WiX source file to a YAML file

Convert reads a WiX source file (.wxs, WiX v3 or later) and writes the
msi.yaml that describes the same package, as far as msi-builder can express
it. It is a starting point for a port, not a byte-for-byte translation:
everything it cannot convert, such as includes, bind variables, hand-built
dialogs and extensions without a counterpart, becomes a "# TODO" comment in
the YAML file and a line on stderr.

Preprocessor blocks become template actions: <?if?>, <?ifdef?>, <?elseif?>
and <?else?> become {{ if }}, {{ if has "NAME" }} and {{ else }}, and
<?foreach?> becomes {{ range }} over split ";". A <?define?> inside a block
becomes a var whose value tests the block, and an <?error?> becomes fail.
Convert writes a block as a template when every element in it is a
component, a file, registry value or key, environment variable, plain
property, or a reference that puts components in a feature or group; for
any other block, it converts only the first branch and leaves a "# TODO".

With --msi and the package WiX built from the source file, every component
whose Guid WiX generated (Guid="*" or none) keeps the ComponentId it has in
that package, when convert finds it there by its ID, its key file or its registry key
path, and
each file a WiX Files glob harvested keeps its code in the guids of the
folder source the glob became; the others get a "# TODO" comment. Without
them, msi-builder generates other component codes, and
an upgrade that removes the old version after installing the new one
(MajorUpgrade Schedule="afterInstallExecute", and presumably
"afterInstallFinalize") deletes the files of every component whose code
changed.

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
  msi-builder convert --msi app-1.4.0.msi -o msi.yaml app.wxs
```

## Flags

```text
      --force           overwrite an existing output file
  -h, --help            help for convert
      --msi string      keep the component codes of this package, built by WiX from app.wxs
  -o, --output string   write the YAML file here instead of stdout
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
