# Agent instructions

Follow [`CONTRIBUTING.md`](CONTRIBUTING.md) for all changes. These instructions
apply throughout the repository; also read any `AGENTS.md` in the area you edit.

## Project and source map

msi-builder builds Windows Installer (MSI) packages from a YAML file. It is pure Go
without cgo, builds the same packages on Linux, macOS and Windows, and never
runs the packages it writes. MSIX is out of scope; signing is `pkg/sign`.
[`docs/design/architecture.md`](docs/design/architecture.md) explains the
packages and the build pipeline; read it before changing more than one package.

Packages under `pkg/` are the importable Go API and only hold what a Go
program needs to build and sign a package: `config`, `builder` and `sign`.
Everything else lives under `internal/` and may change without notice; do not
add a package to `pkg/` or export an `internal/` type through it without
asking the maintainer, since every exported name is bound by semantic
versioning after 1.0.

- [`pkg/config`](pkg/config): the YAML schema (`docs/design/yaml-schema.md`,
  `pkg/config/schema.json`), parsing and validation. The builder consumes a
  validated `*config.Config` and never reads YAML. Folder sources (a file
  `source` ending in `/`) are the one part that lists folders:
  `Config.ExpandFolders` (`folder.go`) replaces their component with one per
  file through the `AddTree` code (`tree.go`, patterns in
  [`internal/folderglob`](internal/folderglob) via doublestar, which
  `wixconvert` shares), and `builder.Build` calls it between two validation
  passes. A folder source's `guids` pin the codes of single files.
  Replaced components stay in `Config.folders`, so the second pass still
  reports problems at their YAML path.
- [`pkg/builder`](pkg/builder): the public `Build`, `Options`, `Result` and
  `File`. It converts to and from `internal/builder` and adds nothing else;
  `Options` and `File` mirror the internal structs field by field so that a
  type conversion compiles, and a new `Result` field is copied in `Build`
  (`TestResultFieldsMatch` fails until it is). The internal
  `Result.Database` is not exposed; only the internal tests read it.
- [`internal/builder`](internal/builder): config to tables. Identities and GUIDs,
  directories and short names, components, files, media, registry, services,
  upgrades, custom actions, sequences, the setup wizard (`ui*.go`),
  `_Validation`, summary information, then the streaming write and a
  verification pass (`finish.go`). `internal/builder/nativetest` holds the Windows
  msi.dll and msiexec tests.
- [`internal/msidb`](internal/msidb): the database: column types, typed rows,
  stream-name encoding, string pool and code pages, catalog, column-major table
  streams, binary streams; `MarshalTo` writes a package, `Unmarshal`/`Decode`
  read one.
- [`internal/cfb`](internal/cfb): Compound File Binary v3/v4 writer and lazy reader.
- [`internal/cab`](internal/cab): cabinet writer and reader, one folder per cabinet
  unless identical files share bytes (`Entry.Copy`: the shared file and the
  file after it or after a copy start new folders),
  stored, MSZIP or LZX blocks, plus an `fs.FS` view. The LZX encoder and
  decoder are [`internal/lzx`](internal/lzx), written from [MS-PATCH] and
  [MS-CAB] only. To test the reader on layouts the writer never
  produces (other tools' cabinets, empty folders, corrupt blocks, legacy
  names), build them with `RawCabinet` (`export_test.go`), which the native
  tests use too to compare with `cabinet.dll`.
- [`internal/summary`](internal/summary): the summary information stream.
- [`pkg/sign`](pkg/sign): Authenticode signing and verification of packages
  and of cabinets written with a signature reserve (spec chapter 12), written
  from the spec without cgo. `MsiDigitalSignature.Hash` is the signer's
  signature value (`SignerInfo` encryptedDigest), not the content digest:
  that is what `MsiGetFileSignatureInformation` returns and msiexec compares
  (SIG-09). `nativetest/cabsign_windows_test.go` probes it with growing
  buffers, because a call without a buffer returns 0 and size 0.
- [`internal/cadll`](internal/cadll): the custom action DLL that `run`
  actions, permission grants and `close` embed, built from C by `build.sh` with
  clang and lld and committed for x86, x64 and arm64; see its `AGENTS.md`
  before touching it. [`internal/rights`](internal/rights) maps grant
  rights names to access masks for `pkg/config` and the builder.
- [`internal/schema`](internal/schema), [`internal/pe`](internal/pe),
  [`internal/condition`](internal/condition), [`internal/formatted`](internal/formatted), [`internal/sequence`](internal/sequence),
  [`internal/codepage`](internal/codepage), [`internal/artwork`](internal/artwork):
  standard table definitions, PE version resources, the condition grammar,
  checks of formatted strings,
  sequence numbering, code pages and the wizard bitmaps.
- [`cmd/msi-builder`](cmd/msi-builder): CLI with `build`, `validate`, `inspect`, `convert`,
  `sign`, `verify`, `completion` and `version`, built on `spf13/cobra`.
- [`internal/wixconvert`](internal/wixconvert): the best-effort WiX `.wxs` to
  YAML converter behind `msi-builder convert`, on `encoding/xml` only. It
  reads the source in one pass and emits an ordered `omap`, so the YAML keeps
  the schema's key order; everything without a counterpart becomes a
  `# TODO` comment next to the key it belongs to and a `Note` for stderr.
  Snapshots in `testdata/` (regenerate with `MSI_BUILDER_UPDATE_SNAPSHOTS=1`)
  must stay valid configs. With `--msi` (`Options.Built`) it pins the
  component codes WiX generated, matched by `Id`, by key file or by registry key path (Root/Key/Name, as
  WiX names an Id-less registry component by a hash) (`codes.go`),
  because a late `removeOld` deletes the files of every changed code
  (UPG-11); a WiX shortcut component, which keeps its shortcut, is pinned too (UPG-11
  shows a shared code keeps it; with WiX's other key path it is inferred). A `Files`
  glob keeps the code of each harvested file in the folder source's `guids`
  (`pinHarvested`, after the components took theirs): the key file's path
  comes from the built `Directory` table below the glob's folder, filtered by
  the same include/exclude matcher as the build; among the globs whose source
  folder holds a file on disk (`Options.Exists`), when any does, it goes to
  the one whose folder is nearest; a tie leaves it unpinned with a todo note (UPG-12, which installs a real
  WiX 5 package on CI). The fixtures are our own sample app: never copy
  third-party `.wxs` files in, and never read WiX source code (clean room).
- [`internal/spool`](internal/spool), [`internal/memtest`](internal/memtest),
  [`internal/testpe`](internal/testpe): the spool target shared by file and
  in-memory builds, the peak-heap sampler for bounded-memory tests, and a
  generator for small PE files used in tests.
- [`docs/spec`](docs/spec): the MSI specification msi-builder implements
  (`MSISPEC.md`), the Windows test results that back it
  (`MSISPEC-RESULTS.md`) and the Windows test harness (`msitest`).
  A test case ID is `<PREFIX>-<NN>`, the prefix naming the package or area
  under test (Appendix C, "Case IDs"); a new case takes the next free
  number of its prefix, and retired IDs are never reused.
  [`examples`](examples) holds complete configurations that tests build.

## Clean-room rules

- Write code from [`docs/spec`](docs/spec) and from Microsoft's documentation.
  Never read, copy or port code from other MSI tools or installer engines, in
  particular projects under the LGPL (this includes the WiX toolset, Wine and
  msitools). Do not cite or link their sources in code or documentation.
- Microsoft's documentation is not assumed correct. A statement that
  `MSISPEC-RESULTS.md` has not verified on Windows is a hypothesis: mark it as
  unverified in the spec, or verify it with the `msitest` harness, before the
  builder relies on it.
- Artwork and bitmaps are generated by [`internal/artwork`](internal/artwork)
  from a palette.
  Never add image files taken from another installer toolkit.

## Development and checks

Use the Go version in [`go.mod`](go.mod) and never change it. The toolchain
and the tasks CI runs are pinned in [`mise.toml`](mise.toml); run the same
commands from the repository root before committing:

```sh
gofmt -l .
go mod tidy -diff
GOTOOLCHAIN=go1.27.3 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
go test ./... -shuffle=on -race
```

- [`.golangci.yaml`](.golangci.yaml) enables every linter with few exceptions
  and stays that way. Fix findings instead of adding `//nolint`; when a
  directive is unavoidable it needs a reason (`nolintlint`). Common findings:
  identifiers shorter than three characters (`varnamelen`), functions above
  cyclomatic complexity 10 (`cyclop`), unwrapped errors from other packages
  (`wrapcheck`), blank-line rules (`wsl_v5`), missing `t.Parallel()`
  (`paralleltest`) and method order (`funcorder`).
- The `_windows_test.go` files (nativetest) are only linted with
  `GOOS=windows`. From Linux, build the linter once for the host
  (`GOBIN=<dir> go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`)
  and run `GOOS=windows <dir>/golangci-lint run ./internal/builder/...`;
  `go run` with `GOOS=windows` would build a Windows binary. Existing files
  still have findings there, so check the files you changed.
- Tests add no test-only module dependencies. Test vectors from the spec appendix
  (stream names, compound files, summary stream, cabinets) are golden hashes:
  a writer change that alters them is a bug unless the spec changed.
- `TestConformance` (`internal/cfb/conformance_test.go`) is an independent
  [MS-CFB] checker for the writer's output (header, FAT/DIFAT/MiniFAT,
  sector ownership, directory entries, red-black trees, zero padding). Run a
  new writer layout through it; the coverage table in `MSISPEC.md` section 2
  lists which rules are supported and which are not needed, with reasons.
- Snapshots: `TestExampleTables` (`cmd/msi-builder/testdata/tables`) holds the
  tables of every example, and `TestMsiexecInstallsExamples`
  (`internal/builder/nativetest/testdata/install`) holds the entries that
  installing and removing each example adds under the watched folders and
  registry keys on Windows, plus the firewall rules. Only entries whose name,
  with or without its extension, occurs as a whole word in one of the
  package's strings, or that carry its product code, are recorded, so other
  programs writing on the runner (`HKCU\Software\Mozilla`) stay out;
  `TestScopeOwns` checks that rule on every platform. Changes inside entries
  that existed before are not recorded. A change to the generated packages
  updates the snapshots in the same pull request: rerun the tests with
  `MSI_BUILDER_UPDATE_SNAPSHOTS=1` and review the diff. The install snapshots need
  an elevated Windows machine; on a mismatch CI uploads the new ones as the
  `install-snapshots` artifact. The example inputs live in
  `internal/exampletest`, and a new example needs an entry there, in
  `installExamples` and in `TestScopeOwns`.
- `-short` skips the slow tests: the native tests that open packages with
  `msi.dll` and install them with `msiexec` (Windows only,
  `internal/builder/nativetest`, `internal/cab/native_windows_test.go`) and the bounded-memory
  tests (`TestBoundedMemory`, `TestLargePayloadBoundedMemory`). CI runs
  `-short` on Linux and the full set on Windows.
  `TestMsiexecInstallsLargePackage` (CAB-10) also needs
  `MSI_BUILDER_LARGE_PACKAGE=1` and about 10 GB of free disk: it builds and
  installs a package of about 2.9 GB in external cabinets. CI runs it in a
  step of its own, without `-race`. Tests that need several cabinets set
  `builder.Options.CabinetSize` instead of writing 2 GB.
- Bounded-memory tests assert on the live heap that `memtest.PeakHeap` reads
  from `/gc/heap/live:bytes`, not on `HeapAlloc`. `HeapAlloc` includes
  uncollected garbage up to the collector's goal (about twice the live heap),
  so it moved with allocation rate and load: `TestBoundedMemory/MSZIP`
  measured 9.6 MB locally and 17.7 MB on a busy `windows-latest` runner for a
  live heap of about 5 MB. One collection's live value also counts objects
  allocated while it marks, which grows with a slow mark on a loaded machine,
  so `Peak.Max` is the larger of min(live[i], live[i+1]) over successive sampled
  collections: data the code holds survives both, mark-time garbage does not.
  Under 8 CPU hogs on 4 cores the MSZIP writer reads about 5 MB with
  `GOMAXPROCS=4` and about 10 MB with `GOMAXPROCS=16` (one cycle up to
  15 MB). Do not raise a limit to fix such a failure; compare the peak, the
  one-cycle peak and the allocated peak in the failure message first.
- Windows syscall helpers that forward `...uintptr` to `(*syscall.LazyProc).Call`
  need `//go:uintptrescapes`, and every `uintptr(unsafe.Pointer(...))` must be
  written inside the call's argument list. Without that, out-parameters stay on
  the goroutine stack: a stack move makes `msi.dll` write the handle to the old
  copy, the caller reads 0, and the next call fails with error 6 while the
  leaked handle keeps the package open. Check with
  `GOOS=windows go test -c -o /dev/null -gcflags=-m ./internal/builder/nativetest`
  (or `GOOS=windows go build -o /dev/null -gcflags=-m .` in
  `docs/spec/msitest`) and look for `moved to heap`.
- Changes that affect the generated packages need a Windows run. Use the
  `msitest` harness or the native tests on a Windows machine; state in the
  pull request which checks ran and which could not.
- Per-user behaviour needs a non-administrator run as well: an elevated
  administrator's install can hide failures (CA-01: the rollback of the product
  registration only fails for a non-administrator). Windows Server refuses
  non-administrator installs with 1625 unless the machine policy `DisableMsi`
  is 0, and a per-user test package needs `INSTALLDIR` under
  `LocalAppDataFolder`, or it stops with 1303 at Program Files.
- A standard user can start a repair of a per-machine product, which the
  server runs elevated with the user's command-line values of secure
  properties only (SEC-03). A property that steers what
  such an action changes (a grant's account or folder) stays out of
  `SecureCustomProperties` (`unsecureGrantProperties`, spec 17.7);
  `TestNonAdminGrantRepair` repairs as the standard user to check that.
  On the CI runner such a silent repair of a package with an elevated
  custom action fails with 1730 in `InstallInitialize` (`SECREPAIR`,
  `MSI_LUA` credential prompt), after the property decisions are logged,
  so read those from the log rather than from what the repair changed.
- CI splits Windows into two runners: suite `unit` runs every package but
  `internal/builder/nativetest` under `-race`, suite `native` runs
  nativetest verbose and without `-race` (msiexec installs one package at a
  time, so nativetest sets the length of the run). A result test's `t.Log`
  output is in the `go test (native)` log; do not add a step that reruns
  tests with `-v` only to print them. A test that cannot run in either
  suite (it needs a lot of disk, an environment variable or a full-size
  run the race build shrinks) gets its own step. The `TestNonAdmin` tests
  run in the native step: the step before it creates the standard user
  `msitest` and exports `MSI_BUILDER_NONADMIN_USER` and
  `MSI_BUILDER_NONADMIN_PASSWORD`.
- To learn what Windows does before writing a test, a temporary nativetest
  that collects its observations and reports them with one `t.Errorf` runs
  on the `windows-latest` runner: push the branch and start CI with
  `workflow_dispatch` on it, then read the job log. Replace the probe with a
  test that asserts the observed behavior before the pull request merges.
  The runner does not let the Windows Firewall service stop (access denied),
  so firewall behavior with the service stopped cannot be probed there.
  To probe table rows the builder does not write yet, build a package with
  `builder.Build`, `msidb.Unmarshal` it, add rows (`schema.New` for a missing
  table), replace `_Validation` with `schema.Validation` of the other tables
  and `Marshal` it again; SEARCH-05 probed 70 locator rows in one install
  that way (`docs/spec/msitest/results/SEARCH-05/probe`). A probe opened as a
  pull request runs on every push too; keep the raw results in
  `docs/spec/msitest/results` before deleting it.
- Native tests run in shuffled order, so a test that checks what an install
  creates must not share registry keys or folders with other tests: give it
  its own root and delete it before and after. Whether a key's parent exists
  before the install changes the DACL `MsiLockPermissionsEx` gives the key
  (a created parent gets the protected DACL and the key inherits it;
  SEC-02 flaked with a shared `HKLM\SOFTWARE\MsiBuilderTest` parent).
- `Get-Acl` leaves a NULL DACL out of its `Sddl` (`O:LAG:…` with no `D:`),
  so it cannot tell no DACL from a missing read. Tests that need to see one
  read the DACL with `GetFileSecurityW` and
  `ConvertSecurityDescriptorToStringSecurityDescriptorW`
  (`D:NO_ACCESS_CONTROL`), and write one with `SetFileSecurityW`, which
  stores the descriptor without inheritance but drops an `AI` flag
  (`fileDACL` and `setSecurity` in `nulldacl_windows_test.go`, SEC-04).
- Wizard changes need a full-UI run: `/qn` never shows a dialog, so dialog
  bugs (wrong event names, missing ErrorIcon) only appear with the full UI.
  `internal/builder/nativetest/fullui_windows_test.go` drives msiexec through
  UIAutomation (BM_CLICK on the msiexec PID's own windows only, because other
  installs may share the desktop). It needs an elevated process in an
  interactive session: in session 0 msiexec silently drops to no UI. Control
  event names are case-sensitive (`RMShutdownAndRestart`).
  The GitHub `windows-latest` runner qualifies, but `go test ./...` there has
  the default 10-minute timeout and a test skips when less than
  `fullUIBudget` is left, so a full-UI test runs in CI only when the shuffle
  starts it early. UI Automation names drop `&`, even on a NoPrefix Text
  control; a Text control hidden by its ControlCondition from the start was
  absent from the control view the driver walks (UI-07). A PathEdit's
  text (the install folder) is among the window's names (UI-08). Each
  driver run starts with an empty log.
- `ActionText.Description` is declared `Text`, but the engine formats it:
  `[ProductName]` was resolved on the progress page and in the log, and a
  deferred action's text is resolved when the script is written (its
  `ActionStart` operation). A `/qn` log omits an immediate action's
  description; it appears as `Action hh:mm:ss: <name>. <text>` with the full
  UI (CA-08; `/qb` not tested).
- To see what a broadcast reaches, `broadcast_windows_test.go` logs
  `WM_SETTINGCHANGE` in a hidden top-level window (message-only windows
  get no broadcasts: documented, unverified) and reads Explorer's current environment block
  through its PEB, which shows what programs started from the shell
  inherit. The runner's session has an Explorer (ENVVAR-02).
- `HKCU\Software` is shared by the 32-bit and 64-bit registry views, so
  `reg query /reg:32` finds a per-user value the package wrote in the 64-bit
  view; only HKLM tells the views apart (SEARCH-06).
- Quote `${VAR}` inside a YAML flow mapping (`{ version: "${VERSION}" }`):
  `{` and `}` end a plain scalar there, so the document fails to parse
  ("did not find expected ',' or '}'"). The windows-only tests only show
  this in CI, so parse their configs with `config.Parse` on Linux first.
- Uninstall test products with `defer`, not `t.Cleanup`: `msiexecQuiet` runs
  under `t.Context()`, which is cancelled before cleanups run, so a cleanup
  uninstall fails with "context canceled" and leaves the product installed.

## Implementation rules

- Output is deterministic. The same configuration, inputs and
  `SOURCE_DATE_EPOCH` produce a byte-identical package: no maps iterated into
  output without sorting, no wall-clock times, no random GUIDs. Identities are
  derived from the upgrade code and stable names.
- Keep memory bounded by the tables, not by the payload. File data flows from
  `cab.Entry.Open` readers through MSZIP or LZX into the cabinet spool and from there
  through `cfb.WriteTo` into the package; nothing reads a payload file into a
  byte slice. Verification reads the package back lazily (`cfb.Open`,
  `msidb.Decode`, `cab.Walk`) and compares the SHA-256 digests computed while
  hashing the sources. A new feature that needs file contents gets them from a
  reader in the same pass as `MsiFileHash`. Every input goes through
  `openSource` (`internal/builder/finish.go`), which picks `File.FS`, `Config.FS`
  or `BaseDir`; never call `os.Open` on a source path directly.
- Byte-slice entry points (`cab.Write`, `cfb.Write`, `msidb.Marshal`) are thin
  wrappers over the streaming functions and must stay byte-identical to them.
- `productCode: auto` hashes the encoded configuration with null, `""`,
  `[]` and `{}` values pruned (`pruneEmpty` in `internal/builder/tree.go`); `false`
  and `0` stay, because an explicit `secure: false` differs from the default.
  A new optional config field must therefore be a pointer, string, slice or
  map, or carry `yaml:",omitempty"`; a plain `bool` or `int` would change
  the automatic ProductCode of every existing configuration. Every input
  file the build reads (payloads, icons, binaries, wizard licenses and
  pictures) also goes into the digest by content through `hashInput`, since
  the config only holds its path.
- A null list entry decodes to a nil pointer. `nullEntries`
  (`pkg/config/nulls.go`) finds them by reflection over the YAML keys, in
  `Parse` and in `Validate`, before the defaults dereference anything, so a
  new list field is covered without listing it there.
- The builder removes a partial output when a build fails; every spool or
  temporary file is cleaned up on every error path.
- Reject at build time what Windows would accept and then install in a broken
  state. Findings are `config.Diagnostic` values with the YAML path, an error
  or warning severity and a message; errors stop the build, warnings are
  returned in `Result.Diagnostics`. Prefer an error over a silently corrected
  value.
- Format limits from the spec are enforced, not assumed: 31 columns and 32767
  string references per database, 65535 files and blocks and 2 GiB per
  cabinet, 31 UTF-16 units per stream name, 255 bytes per cabinet filename.
- Readers (`cfb`, `cab`, `msidb.Unmarshal`) treat their input as untrusted:
  bounds-check every offset and sector, detect chain cycles, and never allocate
  from a size the input declares without checking it against the input length.
- Wizard translations live in `pkg/config/wizardtexts/<tag>.yaml`, one file
  per locale with every `ui.strings` key. Write them yourself; never copy
  another toolset's localization files. `TestTranslations` checks keys,
  placeholders, `&` counts, column widths and the code page, and
  `TestAccessKeysUnique` (`internal/builder`) fails when two controls of a
  dialog share an access key: move the `&` to another letter of the text.
  In formatted text any `[name]` is a property reference, so a translation
  never adds brackets (Japanese uses 「」). A language whose letters no
  ANSI code page holds gets code page 0 in `locales` (Hindi, Kazakh).
- A dual-scope package (`perUserOrMachine`, `perMachineOrUser`) must follow
  ICE105 in both contexts. The client resolves folder properties for the
  context the package opened in, and a public folder it passes to the server
  wins over the server's own resolution, so a UI that switches the context
  must set every public folder under Program Files or Common Files itself
  (`internal/builder/ui_scope.go`, INST-02); public directories below the
  other context-dependent shell folders (Start menu, desktop) are rejected. The other context's paths come
  from `[LocalAppDataFolder]Programs\` and the `ProgramFiles` environment
  variables, since a client only has its own context's folder properties.
- `cmd/msi-builder` uses `spf13/cobra` (with `spf13/pflag`) and nothing else
  outside the standard library; never import `cobra/doc`, which pulls in a
  man-page renderer. Add no new module dependencies elsewhere without
  discussing them first.

## Command line

- Flags are POSIX style: long flags take two dashes (`--pfx`), and only common
  flags get a one-letter shorthand (`-o`, `-q`). Every flag and positional
  argument registers a completion (`MarkFlagFilename`,
  `RegisterFlagCompletionFunc`, `ValidArgsFunction`).
- Help goes to stdout; errors, diagnostics and usage hints go to stderr. `run`
  returns 0 on success, 1 when the command failed and 2 for a usage error.
  Wrap command-line mistakes (argument count, flag combinations) in
  `usageError`; flag parse errors are wrapped by the root's flag error func.
- [`docs/cli`](docs/cli) is generated from the command tree by `TestDocs`
  (`cmd/msi-builder/docs_test.go`). After changing a command, flag or help text,
  run `go generate ./cmd/msi-builder`; the test fails while the pages are stale.

## Documentation

- Update [`docs/design/architecture.md`](docs/design/architecture.md) when a
  public API or the pipeline changes, and
  [`docs/design/yaml-schema.md`](docs/design/yaml-schema.md) together with
  `pkg/config/schema.json` and the examples when the YAML schema changes.
  A renamed or new everyday key also goes into the user guide
  [`docs/YAML Schema.md`](docs/YAML%20Schema.md), whose YAML blocks
  `TestGuideExamples` builds; keep internals out of that page. The same
  test builds the blocks of
  [`docs/Migrating from WiX.md`](docs/Migrating%20from%20WiX.md), which
  maps WiX elements to keys; a key that replaces a WiX element goes there
  too.
- `docs` is published to the wiki by `.github/scripts/wiki.py`; the first
  line of every file is its title and is removed on publish. Link with
  relative `.md` paths; the script turns them into wiki page links. A new
  page also goes into `docs/Home.md` and `docs/_Sidebar.md`, see
  [`docs/README.md`](docs/README.md).
- When a change establishes a useful implementation detail or debugging
  technique, record it here or in a scoped `AGENTS.md` next to the code.

## Pull requests

Follow the template in `.github/PULL_REQUEST_TEMPLATE.md`. Titles start with
`feat`, `fix`, `docs` or `chore` (`!` for breaking changes) and every pull
request carries one release label. Sign off each commit (`git commit -s`).
Report in the pull request which checks ran, including the Windows run, and
which could not run and why.

Automatic releases are controlled by the repository Actions variable
`RELEASE_ENABLED`: the `release` job in `ci.yaml` runs only when it is
`true` (compared case-insensitively). Unset means merges to `main` create no
tag. Don't change it from a pull request; the maintainer sets it.
