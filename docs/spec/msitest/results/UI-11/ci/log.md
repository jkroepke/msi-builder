# UI-11: scope page before the welcome page (CI)

- Runner: GitHub `windows-latest` (x64), elevated, interactive desktop, 2026-11-01
- Probe run 41318245109 (`.github/workflows/probe.yaml`, deleted afterwards)
- Test: `TestFullUIScopeUpdate` in
  `internal/builder/nativetest/scopeupdate_windows_test.go`

Each case builds a `perUserOrMachine` package (v1 1.0.0, v2 1.1.0) with
`ui: { enabled: true }`, an HKLM marker in a `scope: perMachine` component and
a `PREVMACHINE` search of it; "launch condition" cases add
`Installed OR ALLUSERS = 1 OR NOT PREVMACHINE`, "remembered" sets
`installDir.remember: true`. v1 is installed silently per machine
(`MSIINSTALLPERUSER=""`), in the last case per user (`MSIINSTALLPERUSER=1`).
v2 runs through the full UI; the driver picks the radio button of the case
and presses Next, Install, Finish and OK. The scope page is the first window
in every case.

| Case | Choice | v2 exit | v1 after | v2 after |
| --- | --- | --- | --- | --- |
| all users | all users | 0 | none | machine |
| all users with launch condition | all users | 0 | none | machine |
| just you with launch condition | default (just you) | 1603, the launch condition's message after the page | machine | none |
| remembered machine folder | default, preselected all users by `SetInstallScopePrevious` | 0 | none | machine |
| per-user v1 just you | default (just you) | 0 | none | user |

Client and server log of "all users":

```text
MSI (c): Doing action: InstallScopeDlg
MSI (c): PROPERTY CHANGE: Modifying BuilderInstallScope property. Its current value is 'perUser'. Its new value: 'perMachine'.
MSI (c): Doing action: FindRelatedProducts
Action 14:44:05: FindRelatedProducts. Searching for related applications Found application: [1]
MSI (c): Doing action: LaunchConditions
MSI (s): Skipping FindRelatedProducts action: already done on client side
MSI (s): Doing action: LaunchConditions
MSI (s): Doing action: RemoveExistingProducts
```

An earlier run of the same build with the page setting `ALLUSERS=2` (run
41317478236) passed "all users" and "just you", but in "all users with
launch condition" and "remembered machine folder" the client's
`LaunchConditions` after the page still failed `ALLUSERS = 1` (exit 1603):
the client keeps `ALLUSERS=2`. Next now sets `ALLUSERS` to 1 or nothing.
The same runs passed `TestFullUIInstallScope` (INST-02), `TestFullUIRememberInstallDir`
(UI-08) and `TestMsiexecRegistryFileFolder` (SEARCH-10).
