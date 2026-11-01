# UPG-14: component codes computed from a WiX source (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), elevated, 2026-11-01
- CI run 41288811109, commit 36459de
- WiX 5.0.2 (`dotnet tool install --global wix --version 5.0.2`), run as a
  black box
- Test: `TestMsiexecComputedComponentCodes` in
  `internal/builder/nativetest/wixcodes_windows_test.go`

The source has seven components whose Guid WiX generates: files in
`ProgramFiles6432Folder` below the two-level name
`MsiBuilder Tests\WixCodes`, in a `Bin` folder, in a `Directory` without a
name below it, in a folder with `ComponentGuidGenerationSeed` and in
`CommonAppDataFolder`, a 32-bit HKLM value (`Bitness="always32"`) and a
default HKLM value; and a `Files` glob that harvests `readme.txt` and
`sub\deep.txt`. `MajorUpgrade Schedule="afterInstallExecute"`.

| Variant | Result |
| --- | --- |
| `wix build -arch x86`; `convert --arch x86` against `convert --arch x86 --msi` | the nine codes are the same |
| `wix build -arch x64`; `convert --arch x64` against `convert --arch x64 --msi` | the nine codes are the same |
| v1 1.0.0 from WiX x64, upgraded by v2 1.1.0 from `convert --arch x64` without `--msi` | every file and both values kept |

`convert` without `--msi` wrote no `# TODO` (`msi.yaml: written, 0 TODO
comments to review`). After the upgrade, `App.EXE`, `Bin\Tool.dll`,
`Bin\dot.txt`, `Seeded\seed.txt`, `readme.txt`, `sub\deep.txt`,
`data.txt`, the 32-bit `InstallDir` value and the 64-bit default value were
all present; the uninstall of v2 removed the files. Every install ended with
exit 0. Confirmed.

In the same run UPG-12 still lost every file without `--msi` when the
source was converted from a folder without the glob's files, so the
generated codes still differ from WiX's.
