# UI-12: scope page Next and a folder restored after the page (CI)

- Runner: GitHub `windows-latest` (x64), elevated, interactive desktop, 2026-11-01
- Probe run 41335276366 (`.github/workflows/probe.yaml`, deleted afterwards)
- Test: `TestFullUIScopePage` in
  `internal/builder/nativetest/scopepage_windows_test.go`

The package is the probe's (see [probe](../probe/log.md)), built with the fix:
Next records the chosen context's folder in `InstallScopeDir_INSTALLDIR`,
and `SetInstallScopeDir_INSTALLDIR` sets `INSTALLDIR` right before
`CostFinalize`. The last case adds `ui.scopePage.perMachine: PREVMACHINE` and
sets the HKLM marker, so the launch condition refuses "just for you". The
UI driver accepts the defaults, choosing all users only in the second case.

```text
scopepage_windows_test.go:113: UI-12 folder restored after the page: exit 0, registered user, INSTALLDIR C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopePage0\
scopepage_windows_test.go:113: UI-12 all users without a saved folder: exit 0, registered machine, INSTALLDIR C:\Program Files\MsiBuilderScopePage1\
scopepage_windows_test.go:113: UI-12 just you without a saved folder: exit 0, registered user, INSTALLDIR C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopePage2\
scopepage_windows_test.go:113: UI-12 perMachine preselected: exit 0, registered machine, INSTALLDIR C:\Program Files\MsiBuilderScopePage3\
--- PASS: TestFullUIScopePage (34.73s)
--- PASS: TestFullUIScopePage/UI-12_folder_restored_after_the_page (9.58s)
--- PASS: TestFullUIScopePage/UI-12_all_users_without_a_saved_folder (7.64s)
--- PASS: TestFullUIScopePage/UI-12_just_you_without_a_saved_folder (7.33s)
--- PASS: TestFullUIScopePage/UI-12_perMachine_preselected (7.29s)
```
