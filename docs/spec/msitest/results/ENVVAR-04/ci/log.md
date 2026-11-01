# ENVVAR-04: several parts of one variable in one component (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), the
  runner's administrator with a full token, and the throwaway standard
  user `msitest`, 2026-11-01
- CI run 41298756247 (commit 66323dd), job
  `Build & Test (windows-latest, native)`, PR #207
- Test in `internal/builder/nativetest/dualscope_windows_test.go`:
  `TestMsiexecEnvironmentParts`

A `perUserOrMachine` package whose one component appends
`C:\MsiBuilderEnv04\append1` and `append2` to `PATH` and prepends
`prepend1` and `prepend2` (YAML order), all `scope: auto`, so eight
`Environment` rows in the two generated carriers. Installed silently per
machine (`MSIINSTALLPERUSER=""`) and per user, then uninstalled.

```text
dualscope_windows_test.go:78: ENVVAR-04 machine: PATH parts after the install [prepend2 prepend1 append2 append1]
dualscope_windows_test.go:98: ENVVAR-04 machine: PATH unchanged by install and uninstall: true
dualscope_windows_test.go:78: ENVVAR-04 user: PATH parts after the install [prepend2 prepend1 append2 append1]
dualscope_windows_test.go:98: ENVVAR-04 user: PATH unchanged by install and uninstall: false
```

The user `PATH` started with a trailing `;`; the uninstall wrote it back
without that empty entry, with the same entries. The test compared whole
values in this run; later commits compare the non-empty entries.
