# SEARCH-09: folders named in the registry that must exist (CI)

- Runner: GitHub `windows-latest` (x64), elevated, 2026-10-31
- CI run 41244973608, job "Build & Test (windows-latest, native)"
- Test: `TestMsiexecRegistryFolderExists` in
  `internal/builder/nativetest/regfolder_windows_test.go`

The test writes `REG_SZ` values under `HKLM\SOFTWARE\MsiBuilderTest\RegFolder`
(64-bit view) and installs a package whose properties search them. Each
property has the default `none`, so `none` means the search found nothing.
`C:\ProgramData\MsiBuilderRegFolder\data` exists and holds `marker.txt`;
`...\missing` does not exist.

The `NESTED_*` searches are built from `directory: {path, under: registry}`
and edited afterwards: the `DrLocator.Path` is set to null, and for
`NESTED_RAW*` the parent `RegLocator.Type` is set to 18 (raw + 64-bit).

| Property | Search | Registry data | Result |
| --- | --- | --- | --- |
| `SLASH` | `RegLocator` `Type` 16 | `...\data\` | `C:\ProgramData\MsiBuilderRegFolder\data\` |
| `NOSLASH` | `RegLocator` `Type` 16 | `...\data` | `C:\ProgramData\MsiBuilderRegFolder\data\` |
| `MISSING` | `RegLocator` `Type` 16 | `...\missing` | `none` |
| `FILE` | `RegLocator` `Type` 16 | `...\data\marker.txt` | `none` |
| `NESTED_DIR` | `DrLocator` null `Path`, parent `Type` 16 | `...\data` | `C:\ProgramData\MsiBuilderRegFolder\data\` |
| `NESTED_DIR_MISSING` | `DrLocator` null `Path`, parent `Type` 16 | `...\missing` | `none` |
| `NESTED_RAW` | `DrLocator` null `Path`, parent `Type` 18 | `...\data` | `C:\ProgramData\MsiBuilderRegFolder\` |
| `NESTED_RAW_MISSING` | `DrLocator` null `Path`, parent `Type` 18 | `...\missing` | `C:\ProgramData\MsiBuilderRegFolder\` |

Confirmed. A directory search checks that the folder exists and accepts data
without a trailing backslash. A path-less `DrLocator` adds nothing to a
directory parent. Under a raw parent it treats the data as a file path and
returns the containing folder, without checking that the named folder exists.
