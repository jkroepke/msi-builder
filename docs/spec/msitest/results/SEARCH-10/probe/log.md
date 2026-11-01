# SEARCH-10: registry values that hold a file path (probe)

- Runner: GitHub `windows-latest` (x64), elevated, 2026-11-01
- Probe run 41315855803 (`.github/workflows/probe.yaml`, deleted afterwards)
- Test: `TestProbeRegFileFolder`, replaced by `TestMsiexecRegistryFileFolder`
  in `internal/builder/nativetest/regfolder_windows_test.go`

The probe wrote `REG_SZ` values under `HKLM\SOFTWARE\MsiBuilderTest\RegFile`
(64-bit view) and installed a package whose properties search them. Every
`RegLocator` row of the package was set to `Type` 17 (file + 64-bit), without
a `Signature` row. Each property has the default `none`.
`C:\ProgramData\MsiBuilderRegFile\data` exists and holds `marker.txt` and the
folder `sub`; `...\missing` does not exist. The `NESTED_*` properties are
`file` and `directory` searches below such a `RegLocator` (a `DrLocator` row
whose `Parent` is the `RegLocator` row).

| Property | Search | Registry data | Result |
| --- | --- | --- | --- |
| `EXISTFILE` | `RegLocator` `Type` 17 | `...\data\marker.txt` | `C:\ProgramData\MsiBuilderRegFile\data\` |
| `MISSINGFILE` | `RegLocator` `Type` 17 | `...\data\missing.exe` | `C:\ProgramData\MsiBuilderRegFile\data\` |
| `MISSINGDIR` | `RegLocator` `Type` 17 | `...\missing\x.exe` | nothing (`none`) |
| `FOLDER` | `RegLocator` `Type` 17 | `...\data` | `C:\ProgramData\MsiBuilderRegFile\` |
| `FOLDERSLASH` | `RegLocator` `Type` 17 | `...\data\` | `C:\ProgramData\MsiBuilderRegFile\` |
| `QUOTED` | `RegLocator` `Type` 17 | `"...\data\marker.txt"` (with quotes) | `C:\ProgramData\MsiBuilderRegFile\data\` |
| `NOVALUE` | `RegLocator` `Type` 17 | value missing | nothing (`none`) |
| `NESTED_EXIST` | `file` `marker.txt` under `Type` 17 | `...\data\marker.txt` | `C:\ProgramData\MsiBuilderRegFile\data\marker.txt` |
| `NESTED_MISSING` | `file` `marker.txt` under `Type` 17 | `...\data\missing.exe` | `C:\ProgramData\MsiBuilderRegFile\data\marker.txt` |
| `NESTED_OTHER` | `file` `other.txt` under `Type` 17 | `...\data\marker.txt` | nothing (`none`) |
| `NESTED_SUB` | `directory` `sub` under `Type` 17 | `...\data\marker.txt` | `C:\ProgramData\MsiBuilderRegFile\data\sub\` |

So `Type` 1 returns the folder that holds the last path element of the
data, with a trailing backslash, only if that folder exists; it does not
check the file (or folder) the data names, and it removes surrounding
quotes. A value that names a folder returns that folder's parent. A search
below it looks in the same folder, so a `file` search with the file's name
checks that the file exists.

Install log excerpt (server):

```text
PROPERTY CHANGE: Adding EXISTFILE_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\'.
PROPERTY CHANGE: Adding FOLDERSLASH_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\'.
PROPERTY CHANGE: Adding FOLDER_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\'.
PROPERTY CHANGE: Adding MISSINGFILE_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\'.
PROPERTY CHANGE: Adding NESTED_EXIST_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\marker.txt'.
PROPERTY CHANGE: Adding NESTED_MISSING_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\marker.txt'.
PROPERTY CHANGE: Adding NESTED_SUB_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\sub\'.
PROPERTY CHANGE: Adding QUOTED_FOUND property. Its value is 'C:\ProgramData\MsiBuilderRegFile\data\'.
```
