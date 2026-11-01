# INST-08: firewall rules in dual-purpose packages (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), the
  runner's administrator with a full token, and the throwaway standard
  user `msitest`, 2026-11-01
- CI run 41298756247 (commit 66323dd), job
  `Build & Test (windows-latest, native)`, PR #207
- Tests in `internal/builder/nativetest/dualscope_windows_test.go`:
  `TestMsiexecDualScopeFirewall`, `TestICEDualScopeFirewall`, and
  `TestNonAdminInstallScopes` in `nonadmin_windows_test.go`

A dual package with a firewall rule under its feature (carrier with
`scope: perMachine`; netsh actions conditioned `$Main_Firewall=3 AND
ALLUSERS=1` and `=2 AND ALLUSERS=1`), installed silently in both contexts,
repaired with `REINSTALL=ALL REINSTALLMODE=amus` and uninstalled; "rule"
is whether `netsh advfirewall firewall show rule` finds it afterwards.
The standard user installed the `perUserOrMachine` package of
`TestNonAdminInstallScopes`, whose `scope: perMachine` component carries a
rule, per user.

```text
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: 3106 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: 3426 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: 3170 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: 3170 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: 3426 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
dualscope_windows_test.go:302: INST-08 perUserOrMachine: ICE105 warning: System64Folder invalid directory for a per user application. [Directory.Directory]
dualscope_windows_test.go:268: INST-08 perUserOrMachine install: ALLUSERS "", rule false
dualscope_windows_test.go:268: INST-08 perUserOrMachine repair: ALLUSERS "", rule false
dualscope_windows_test.go:268: INST-08 perUserOrMachine uninstall: ALLUSERS "", rule false
dualscope_windows_test.go:268: INST-08 perUserOrMachine to machine install: ALLUSERS "1", rule true
dualscope_windows_test.go:268: INST-08 perUserOrMachine to machine repair: ALLUSERS "1", rule true
dualscope_windows_test.go:268: INST-08 perUserOrMachine to machine uninstall: ALLUSERS "1", rule false
dualscope_windows_test.go:268: INST-08 perMachineOrUser install: ALLUSERS "1", rule true
dualscope_windows_test.go:268: INST-08 perMachineOrUser repair: ALLUSERS "1", rule true
dualscope_windows_test.go:268: INST-08 perMachineOrUser uninstall: ALLUSERS "1", rule false
dualscope_windows_test.go:268: INST-08 perMachineOrUser to user install: ALLUSERS "", rule false
dualscope_windows_test.go:268: INST-08 perMachineOrUser to user repair: ALLUSERS "", rule false
dualscope_windows_test.go:268: INST-08 perMachineOrUser to user uninstall: ALLUSERS "", rule false
nonadmin_windows_test.go:263: INST-02 perUserOrMachine as msitest: exit 0, INSTALLDIR "C:\\Users\\msitest\\AppData\\Local\\Programs\\MsiBuilderNonAdmin\\", ALLUSERS "", MSIINSTALLPERUSER "1", Privileged ""
```

`TestNonAdminInstallScopes/perUserOrMachine` passed: exit 0 and no rule
`MsiBuilderNonAdmin` afterwards. `perMachineOrUser` had no ICE105
finding.
