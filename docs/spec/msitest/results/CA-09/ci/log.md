# CA-09: custom actions in one context of a dual-purpose package (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), the
  runner's administrator with a full token, and the throwaway standard
  user `msitest`, 2026-10-31
- CI run 41236083486 (commit 8aeb1cc), job
  `Build & Test (windows-latest, native)`, PR #183
- Tests in `internal/builder/nativetest/contextactions_windows_test.go`:
  `TestMsiexecContextActions`, `TestNonAdminContextActions`,
  `TestICEContextActions`

The package has three deferred `run` actions after `InstallFiles` that
print a tag and run `whoami`: `CA09Machine` (no impersonation, sequence
condition `ALLUSERS=1`, type 3073), `CA09User` (`NOT ALLUSERS`) and
`CA09Both` (no condition). The unconditioned copy clears the condition of
`CA09Machine` and its data action; in this run it still had the package code of the conditioned package (later commits give it its own), and each earlier subtest had uninstalled its product, so its install was a first install. A
field that is empty means the action did not run.

```text
contextactions_windows_test.go:165: CA-09 perUserOrMachine install: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perUserOrMachine repair: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perUserOrMachine uninstall: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perUserOrMachine to machine install: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perUserOrMachine to machine repair: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perUserOrMachine to machine uninstall: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser install: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser repair: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser uninstall: ALLUSERS "1", ran as {machine:nt authority\system user: both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser to user install: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser to user repair: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:165: CA-09 perMachineOrUser to user uninstall: ALLUSERS "", ran as {machine: user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:196: CA-09 unconditioned elevated action, administrator's per-user install: ALLUSERS "", ran as {machine:runnervmdaln5\runneradmin user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:231: CA-09 perUserOrMachine as msitest: exit 0, ALLUSERS "", ran as {machine: user:runnervmdaln5\msitest both:runnervmdaln5\msitest}
contextactions_windows_test.go:231: CA-09 perMachineOrUser to user as msitest: exit 0, ALLUSERS "", ran as {machine: user:runnervmdaln5\msitest both:runnervmdaln5\msitest}
contextactions_windows_test.go:231: CA-09 unconditioned as msitest: exit 0, ALLUSERS "", ran as {machine:runnervmdaln5\msitest user:runnervmdaln5\msitest both:runnervmdaln5\msitest}
contextactions_windows_test.go:276: CA-09 perUserOrMachine unconditioned=false: ICE105 warning: 3073 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
contextactions_windows_test.go:276: CA-09 perUserOrMachine unconditioned=true: ICE105 warning: 3073 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
```

## CI run 41239378858 (commit e783560)

The unconditioned copy now has its own package code. New subtests switch
the unconditioned `perMachineOrUser` package to per user
(`MSIINSTALLPERUSER=1`), as the administrator and as `msitest`, and
`TestICEActionFolder` adds a Directory row `System64Folder` under
`TARGETDIR` to the package and runs the ICE suite for both dual scopes.
The other lines matched the first run.

```text
contextactions_windows_test.go:220: CA-09 unconditioned elevated action, administrator's per-user install of perUserOrMachine: ALLUSERS "", ran as {machine:runnervmdaln5\runneradmin user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:220: CA-09 unconditioned elevated action, administrator's per-user install of perMachineOrUser: ALLUSERS "", ran as {machine:runnervmdaln5\runneradmin user:runnervmdaln5\runneradmin both:runnervmdaln5\runneradmin}
contextactions_windows_test.go:257: CA-09 unconditioned as msitest: exit 0, ALLUSERS "", ran as {machine:runnervmdaln5\msitest user:runnervmdaln5\msitest both:runnervmdaln5\msitest}
contextactions_windows_test.go:257: CA-09 unconditioned perMachineOrUser to user as msitest: exit 0, ALLUSERS "", ran as {machine:runnervmdaln5\msitest user:runnervmdaln5\msitest both:runnervmdaln5\msitest}
contextactions_windows_test.go:353: CA-09 perUserOrMachine with a System64Folder Directory row: ICE105 warning: 3073 contains msidbCustomActionTypeNoImpersonate execution option. This is invalid for a per user application. [CustomAction.Action]
contextactions_windows_test.go:353: CA-09 perUserOrMachine with a System64Folder Directory row: ICE105 warning: System64Folder invalid directory for a per user application. [Directory.Directory]
```

`TestICEActionFolder` logged nothing for `perMachineOrUser`.
