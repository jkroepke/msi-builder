# SEARCH-08: component searches for folder and registry key paths (CI)

- Runner: GitHub `windows-latest` (x64), elevated, 2026-10-31
- CI run 41231619181, commit fc6887a
- Test: `TestMsiexecComponentSearchFolder` (and `TestICEComponentSearch`,
  clean) in `internal/builder/nativetest/componentsearch_windows_test.go`

The base product `MsiBuilderCompSearchBase` has three components: one keyed
on its folder `data` (`createFolder`, `keyPath: directory`), one keyed on an
HKLM registry value and one keyed on `base.exe`. The app package installs to
`C:\Program Files\MsiBuilderCompSearch\App`, has a component keyed on that
folder with `removeFolders` on `CLEANUP_DIR`, and one keyed on an HKLM
registry value. `CLEANUP_DIR` searches the app's own folder-keyed component
without `keyPath`, so the builder writes `CompLocator.Type` 0.

Properties after the install of the app package (base installed):

| Property | Component | `keyPath` | `CompLocator.Type` | Result |
| --- | --- | --- | --- | --- |
| `O_DIR` | base, folder `data` | `directory` | 0 | `C:\Program Files\MsiBuilderCompSearchBase\data\` |
| `O_DIR_AS_FILE` | base, folder `data` | `file` | 1 | `C:\Program Files\MsiBuilderCompSearchBase\` (the parent) |
| `O_FILE` | base, `base.exe` | default | 1 | `C:\Program Files\MsiBuilderCompSearchBase\` |
| `O_FILE_AS_DIR` | base, `base.exe` | `directory` | 0 | nothing (default `none`) |
| `O_REG` | base, registry value | default | 1 | nothing (default `none`) |
| `O_REG_AS_DIR` | base, registry value | `directory` | 0 | nothing (default `none`) |
| `OWN_REG` | app, registry value | default (warning) | 1 | nothing, also at the uninstall |
| `CLEANUP_DIR` | app, folder | default | 0 | empty at the install (not installed yet) |

Then `C:\Program Files\MsiBuilderCompSearch\sibling.txt` and
`App\runtime\cache.bin` were created and the app package uninstalled with
`msiexec /x`: `CLEANUP_DIR` was `C:\Program Files\MsiBuilderCompSearch\App\`,
`BuilderRemoveFolders` moved that folder aside and deleted it, and
`sibling.txt` was still there. Confirmed.
