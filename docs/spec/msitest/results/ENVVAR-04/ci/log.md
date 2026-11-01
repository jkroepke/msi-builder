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

## Order of the parts (PR #220)

- CI runs 41322373604 (commit 2cda4e6) and 41323573618 (commit fe274c3),
  probe workflow, `windows-latest`, 2026-11-01, same test

The run above matches the stored order of the hash keys msi-builder wrote
then (`prepend1`, `prepend2`, `append2`, `append1`) applied one after the
other. PR #220 numbers the keys in YAML order (`env0`, `env1`, …, with `m`
and `u` for the two contexts). Run 41322373604 added a second component
`Extra` that appends `append3`; its carriers sort before the first
component's, which rules out an order by component in ascending key
order only. Run 41323573618 added a third component `Last` that appends
`append4`. In both contexts the carriers sort `Extra`, `Last`, then the
first component, so grouping by component in ascending key order would
give `…;append3;append4;append1;append2` and in descending order
`…;append1;append2;append4;append3`.

```text
dualscope_windows_test.go:104: ENVVAR-04 machine: PATH parts after the install [prepend2 prepend1 append1 append2 append3 append4]
dualscope_windows_test.go:104: ENVVAR-04 user: PATH parts after the install [prepend2 prepend1 append1 append2 append3 append4]
dualscope_windows_test.go:124: ENVVAR-04 user: PATH before "%USERPROFILE%\\.dotnet\\tools;%USERPROFILE%\\.cargo\\bin;%USERPROFILE%\\AppData\\Local\\Microsoft\\WindowsApps;", after the uninstall "%USERPROFILE%\\.dotnet\\tools;%USERPROFILE%\\.cargo\\bin;%USERPROFILE%\\AppData\\Local\\Microsoft\\WindowsApps"
--- PASS: TestMsiexecEnvironmentParts/machine (3.56s)
--- PASS: TestMsiexecEnvironmentParts/user (0.87s)
```

The engine applies the rows in key order (table order), not grouped by
component. The machine `PATH` after the uninstall was the same string as
before (line 124, too long to quote); the user `PATH` lost its trailing
`;`.
