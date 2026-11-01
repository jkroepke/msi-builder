# UI-11: scope page, FindRelatedProducts and launch conditions (probe)

- Runner: GitHub `windows-latest` (x64), elevated, interactive desktop, 2026-11-01
- Probe run 41315855803 (`.github/workflows/probe.yaml`, deleted afterwards)
- Test: `TestProbeScopeLaunchConditions`

Each case builds a `perUserOrMachine` package (v1 1.0.0 and v2 1.1.0, one
upgrade code per case) with `ui: { enabled: true }`, a `scope: perMachine`
component that writes an HKLM marker, and a `PREVMACHINE` registry search of
that marker. "lc" cases add the launch condition
`Installed OR ALLUSERS = 1 OR NOT PREVMACHINE`. v1 is installed silently per
machine (`MSIINSTALLPERUSER=""`); v2 runs through the full UI with the UI
driver, which picks the scope radio button named in the case and presses
Next, Install, Finish and OK. "deferred" cases edit v2's
`InstallUISequence` so that `FindRelatedProducts` and `LaunchConditions` have
the condition `UILevel < 5 OR Installed` (they do not run in the client of a
full-UI first install). The scope page of this build sits in the wizard after
the welcome page, after `CostFinalize`.

| Case | v2 exit | v1 after | v2 after |
| --- | --- | --- | --- |
| lc, all users | 1603 (the launch condition's message box before the welcome page) | machine | none |
| no lc, all users | 0 | machine | machine |
| deferred lc, all users | 0 | machine | machine |
| deferred lc, just for you | 1603 (message box from the server after Install) | machine | none |
| deferred no lc, all users | 0 | machine | machine |

Client log of "no lc, all users":

```text
MSI (c): Doing action: FindRelatedProducts
MSI (c): FindRelatedProducts: current install is per-user.  Related install for product '{F012F9D9-...}' is per-machine.  Skipping...
MSI (c): Doing action: LaunchConditions
MSI (s): Doing action: FindRelatedProducts
MSI (s): Doing action: LaunchConditions
MSI (s): Doing action: RemoveExistingProducts
```

Client and server log of "deferred lc, all users":

```text
MSI (c): Skipping action: FindRelatedProducts (condition is false)
MSI (c): Skipping action: LaunchConditions (condition is false)
MSI (s): Doing action: FindRelatedProducts
MSI (s): Doing action: LaunchConditions
MSI (s): Doing action: RemoveExistingProducts
```

The server's `FindRelatedProducts` logged no related product in any case (the probe's excerpt left out its `Skipping FindRelatedProducts action: already done on client side` line, which the CI run of `TestFullUIScopeUpdate` shows for a client that ran it),
although the server installed per machine, so `RemoveExistingProducts`
removed nothing and both versions stayed registered per machine. Skipping
the client's action does not make the server's find anything: after the UI
sequence ran, only a `FindRelatedProducts` in the client counts. The server's
`LaunchConditions` does evaluate again, with the chosen context.
