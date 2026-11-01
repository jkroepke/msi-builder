# UPG-15: FindRelatedProducts moved behind an ALLUSERS setter (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), elevated,
  2026-11-01
- CI run 41312024506 (commit d3c026b), job
  `Build & Test (windows-latest, native)`: the four cases without the
  folder setter; Probe run 41313596371 (commit b7a6396), job
  `Probe (UPG-15)`: all six cases, PR #209
- Test `TestMsiexecMovedFindRelatedProducts` in
  `internal/builder/nativetest/movedfind_windows_test.go`

The product is `perUserOrMachine`. v1 is installed per machine
(`MSIINSTALLPERUSER=""`) and writes the HKLM value
`Software\MsiBuilderTest\MovedFind<n>\Machine`. v2 (1.1.0) is installed
without properties, so it opens in the per-user context; a registry
search finds the value and the action SetAllUsersFromPrevious sets
`ALLUSERS=1` after AppSearch in both sequences. Each case gets its own
name and upgrade code.

## Results

| Case | FindRelatedProducts | v1 after the v2 install | v2 registered | app.exe of v2 |
|---|---|---|---|---|
| default /qn | 25 | per machine | per machine | `%LOCALAPPDATA%\Programs\MsiBuilderMovedFind0` |
| moved /qn | after SetAllUsersFromPrevious | removed | per machine | `%LOCALAPPDATA%\Programs\MsiBuilderMovedFind1` |
| default /qr | 25 | per machine | per machine | `%LOCALAPPDATA%\Programs\MsiBuilderMovedFind2` |
| moved /qr | after SetAllUsersFromPrevious | removed | per machine | `%LOCALAPPDATA%\Programs\MsiBuilderMovedFind3` |
| moved with folder /qn | after SetAllUsersFromPrevious | removed | per machine | `C:\Program Files\MsiBuilderMovedFind4` |
| moved with folder /qr | after SetAllUsersFromPrevious | removed | per machine | `C:\Program Files\MsiBuilderMovedFind5` |

At 25, the log of v2 shows (default /qn):

```text
FindRelatedProducts: current install is per-user.  Related install for product '{B68459BD-F321-5F07-BE44-E9D9457D52F8}' is per-machine.  Skipping...
Doing action: SetAllUsersFromPrevious
PROPERTY CHANGE: Adding ALLUSERS property. Its value is '1'.
PROPERTY CHANGE: Adding INSTALLDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderMovedFind0\'.
```

v2 ends registered per machine next to v1, which stays registered per
machine with its files in Program Files.

Moved behind the setter (moved /qn), FindRelatedProducts runs in the
per-machine context, OLDERVERSIONBEINGUPGRADED holds v1's product code
and RemoveExistingProducts removes v1 from `C:\Program Files\MsiBuilderMovedFind1`.
INSTALLDIR still holds the per-user folder that was resolved when the
package opened, and v2's files go there:

```text
PROPERTY CHANGE: Adding INSTALLDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderMovedFind1\'.
Executing op: ComponentRegister(ComponentId={6D4AF294-E850-5B96-9CAF-284E3695C420},KeyPath=C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderMovedFind1\app.exe,State=3,,Disk=1,SharedDllRefCount=0,BinaryType=1)
```

With the reduced UI the UI sequence runs FindRelatedProducts, and the
execute sequence does not run it again; the results are the same.

The "with folder" cases add a second setter after
SetAllUsersFromPrevious, under the same condition, that sets INSTALLDIR to
`[%ProgramW6432]\MsiBuilderMovedFind<n>\`. v2 then installs into Program
Files (moved with folder /qr):

```text
Executing op: ComponentRegister(ComponentId={617BCB4B-5B05-54FF-9395-551CB73FF3FC},KeyPath=C:\Program Files\MsiBuilderMovedFind5\app.exe,State=3,,Disk=1,SharedDllRefCount=0,BinaryType=1)
UPG-15 moved with folder /qr: v1 none, v2 machine, server ALLUSERS "1", OLDERVERSIONBEINGUPGRADED "{896D214B-6FE7-51C5-B192-B3C9F9C7C6AE}"
```

The cleanup removed every version by product code; no case left a
registration behind.
