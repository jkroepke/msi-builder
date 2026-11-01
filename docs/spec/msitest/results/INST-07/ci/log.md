# INST-07: context conditions and context setters in a dual-purpose package (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), the
  runner's administrator with a full token, and the throwaway standard
  user `msitest`, 2026-11-01
- CI run 41298756247 (commit 66323dd), job
  `Build & Test (windows-latest, native)`, PR #207
- Test in `internal/builder/nativetest/dualscope_windows_test.go`:
  `TestMsiexecContextConditions`

Four components each write an HKCU value: `MsiPerUser` (condition
`MSIINSTALLPERUSER=1`), `MsiNotPerUser` (`MSIINSTALLPERUSER<>1`),
`AllUsers` (`ALLUSERS=1`) and `NotAllUsers` (`NOT ALLUSERS`). `FLIP=1`
runs a type-51 action setting `ALLUSERS=1` after `ValidateProductID` in
the execute sequence, `FLIPUSER=1` one setting `MSIINSTALLPERUSER=1`.
"registered" is where `Installer\Products\<squashed code>` exists;
`ALLUSERS` and `MSIINSTALLPERUSER` are the last `Property(S)` values in
the log (empty also when the property is absent). All installs silent.

```text
dualscope_windows_test.go:184: INST-07 perUserOrMachine: registered user, INSTALLDIR "C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderContext\", ALLUSERS "", MSIINSTALLPERUSER "1", components [MsiPerUser NotAllUsers]
dualscope_windows_test.go:184: INST-07 perUserOrMachine MSIINSTALLPERUSER empty: registered machine, INSTALLDIR "C:\Program Files\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "", components [MsiNotPerUser AllUsers]
dualscope_windows_test.go:184: INST-07 perUserOrMachine MSIINSTALLPERUSER 0: registered machine, INSTALLDIR "C:\Program Files\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "", components [MsiNotPerUser AllUsers]
dualscope_windows_test.go:184: INST-07 perUserOrMachine ALLUSERS 1: registered machine, INSTALLDIR "C:\Program Files\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "", components [MsiNotPerUser AllUsers]
dualscope_windows_test.go:184: INST-07 perUserOrMachine setter ALLUSERS 1: registered machine, INSTALLDIR "C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "1", components [MsiPerUser AllUsers]
dualscope_windows_test.go:184: INST-07 perMachineOrUser: registered machine, INSTALLDIR "C:\Program Files\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "", components [MsiNotPerUser AllUsers]
dualscope_windows_test.go:184: INST-07 perMachineOrUser MSIINSTALLPERUSER 1: registered user, INSTALLDIR "C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderContext\", ALLUSERS "", MSIINSTALLPERUSER "1", components [MsiPerUser NotAllUsers]
dualscope_windows_test.go:184: INST-07 perMachineOrUser setter MSIINSTALLPERUSER 1: registered machine, INSTALLDIR "C:\Program Files\MsiBuilderContext\", ALLUSERS "1", MSIINSTALLPERUSER "1", components [MsiPerUser AllUsers]
```
