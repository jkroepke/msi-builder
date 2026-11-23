# `msi-builder inspect`

Print the tables, streams and summary of a package

Inspect prints the code page, the summary information, the streams and
every table of a package, or a single table with --table. Columns are
separated by tabs; the second line of a table holds the column types, with
"!" marking key columns. Control characters in a value are shown as their
control pictures (U+2400 to U+2421), so a newline appears as "␊". Cabinets
stay in the file and are listed by size.

## Usage

```text
msi-builder inspect [flags] package.msi
```

## Examples

```text
  msi-builder inspect hello.msi
  msi-builder inspect --table File hello.msi
```

## Flags

```text
  -h, --help[=true|false]   help for inspect
      --table string        print only this table
```

## See also

- [`msi-builder`](msi-builder.md): Build Windows Installer (MSI) packages from a YAML file
