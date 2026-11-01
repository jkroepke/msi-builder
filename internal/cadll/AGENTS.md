# internal/cadll

The custom action DLL of `run` actions, permission grants and `close`. Read
the root `AGENTS.md` first.

- `src/builderrun.c` is the whole DLL: freestanding C11, no C runtime, no SDK
  header. Every Win32 and MSI prototype it uses is declared in the file from
  Microsoft's documentation of that function. Keep it that way: a header or
  CRT dependency would pull in a toolchain's runtime and its licence, and the
  size would grow from a few kilobytes to hundreds.
- `build.sh` cross-builds `builderrun-{x86,x64,arm64}.dll` with clang and lld
  (`--target=<arch>-pc-windows-msvc`, `-nostdlib`, `/Brepro`). The import
  libraries come from `.def` lists in the script through `llvm-dlltool`; the
  x86 entries carry the stdcall argument sizes. After a change to the source
  or the script: run `go generate ./internal/cadll/` (it runs `build.sh`,
  which also rewrites `SHA256SUMS`) and commit the DLLs and the sums. CI
  rebuilds with clang 18 on Ubuntu 24.04 and fails when the committed DLLs
  differ, so `build.sh` refuses any other clang major version; point it at
  the right binaries with `CLANG=clang-18 DLLTOOL=llvm-dlltool-18`. `go
  build` never runs generators, so building msi-builder still needs no C
  toolchain.
- The exported entry point is `BuilderRun` (`UINT __stdcall (MSIHANDLE)`).
  It was `GoMsiRun` before the rename to msi-builder. Never give it, or any
  other name of our own in a package, the prefix `Msi`: Microsoft
  reserves it for the engine (spec 17.6).
  The builder (`internal/builder/actions.go`, `runAction`) passes
  `<directory>|<command>` in `CustomActionData` (deferred, rollback, commit)
  or the `BuilderRun` property (immediate); `|` cannot occur in a path, so the
  first one ends the directory.
- The DLL starts the child with `CreateProcessAsUserW` and the thread's
  impersonation token when there is one, else `CreateProcessW`:
  `CreateProcessW` ignores an impersonation token. Only `ERROR_NO_TOKEN`
  from `OpenThreadToken` takes the `CreateProcessW` path; any other error
  fails the action rather than start the command as the process. CA-03
  showed no thread token in practice: impersonated deferred and immediate
  actions run in a custom action server whose process token is the user's
  (the log says `token: the process`, `whoami` prints the user), so the
  `CreateProcessAsUserW` path is unexercised. The child inherits the custom
  action server's environment block.
- The child is created suspended with a `PROC_THREAD_ATTRIBUTE_HANDLE_LIST`
  that holds only the pipe's write end, so no other inheritable handle of
  the custom action server (another run action's pipe, for one) goes along,
  put into a job with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, then resumed. A
  cancel ends the job, and closing the job at the end of the action ends
  whatever the command left running. When the job cannot be created or
  assigned (a server in a job that forbids nesting), the action logs it and
  goes on with the child alone.
- Output is read from one named pipe for stdout and stderr (a random
  `\\.\pipe\BuilderRun-<32 hex>` name from `RtlGenRandom`, one instance,
  inbound: only a named pipe can be read overlapped; the random name, the
  single instance and the default pipe security, which gives accounts
  other than the creator, LocalSystem and administrators read access only,
  keep other accounts from writing into the log). A line ends at `\n`, `\r`
  or `\r\n`; each is logged through `MsiProcessMessage(INSTALLMESSAGE_INFO)`
  with the fixed template `BuilderRun: [1]`, so brackets in the output are
  not formatted. The buffer is 64 KiB (`LINE_BUFFER`); a longer line is
  logged in pieces, each cut before a UTF-8 character the cut would split
  (`pieceEnd`), whose bytes start the next piece: a piece is decoded on
  its own, and half a character fails UTF-8 and sends the whole piece to
  the OEM code page. The overlapped read waits on the child too: once it has
  exited, another process that keeps the pipe open gets `EXIT_GRACE_MS`
  (5 s), then the job is ended and the rest is not read. The grace period
  limits only the wait for more output (a read that pends): output already
  in the pipe is read to its end, since logging a backlog can outlast it
  after the writers closed the pipe, unless `LATE_OUTPUT` (4 x the pipe
  buffer) arrives after it, which only a writer that is still alive can
  send; while nothing
  arrives, a zero progress message every `CANCEL_POLL_MS` (1 s) asks the
  installer about a cancel, which `IDCANCEL` from a logged line also
  signals (that a progress report returns the cancel and stays out of the
  log is documented, not yet seen: the tests have no UI handler). A read
  that fails for another reason closes the pipe and the action waits for
  the child without it, still polling for a cancel. A line is decoded as UTF-8 when valid, else in the OEM code
  page. `chcp 65001` inside a `cmd /c` line does not make cmd.exe's `echo`
  write UTF-8 on the pipe (CA-03 on the x64 and Arm64 runners: the line
  stayed OEM and `€` came out as `?`); PowerShell with
  `[Console]::OutputEncoding` does.
- The build has no stack probes (`-mno-stack-arg-probe`) and no stack
  protector, so keep locals well under a page (4 KB): the line buffer and
  every string of unknown length live on the heap. Constants that do not
  fit an `int` are macros, not enumerators (C11 6.7.2.2). The struct
  layouts the DLL declares itself are pinned by `_Static_assert`s on their
  documented sizes for 32- and 64-bit builds.
- `cadll_test.go` checks the checksums, the PE machine, the export and that
  the imports are only kernel32, advapi32, msi, user32, authz (the deferred folder removal's administrator check) and ntdll (NtCreateFile, NtSetInformationFile, RtlNtStatusToDosError). `testdata/probe` is the
  CA-03 package that `internal/builder/nativetest` installs on Windows
  (`TestMsiexecRunActions`), natively on the x64 and arm64 runners. The x64
  package under emulation on arm64 passed once (CA-03) and is not rerun.
- Permission grants (spec 17.7) use the export `BuilderGrant` (the
  deferred `BuilderGrantExec`, its rollback and the commit action, told
  apart by the first character of `CustomActionData`: `+`, `-`, `c`, and
  `x` for the commit of a removal; then `i` or `m` for a first or a
  maintenance install, after `:` and one `#` per entry line). The engine builds that data with type-51 actions
  the builder writes (`internal/builder/permissions.go`); never add an
  immediate action that computes data for a deferred LocalSystem action,
  because it runs as the installing user, who can change it, and keep the
  setters right before the deferred actions; `removeFolders` (spec 17.10)
  works the same way since 2026-10-30. The deferred action fails when
  the number of lines differs from the count (a line break in a formatted
  value), and rejects `|` and control characters in a formatted account and
  control characters in an object; the account is the last field of a
  line. The deferred action records the rights it adds in
  `HKCU\Software\msi-builder\GrantRollback\<ProductCode>` (the LocalSystem
  hive) before it writes the DACL, so the rollback takes away only those;
  never make the rollback revoke the authored mask, which strips rights an
  object had before a failed repair or upgrade. Next to the mask (`REG_DWORD`
  `<n>`) it records the account's SID (`REG_BINARY` `<n>s`), and the
  rollback revokes from that SID and never works the account out again: in
  a per-user maintenance install that would read the remembered account
  from a key the user can write. An account from a property
  is remembered by the commit action (lines whose template has `[`,
  `HKLM\SOFTWARE\msi-builder\GrantAccounts\<ProductCode>`, value named
  after the template; in a per-user install the head line ends with `|`,
  the DLL reads `UserSID` itself (the engine's value, which an action that
  changes the session property cannot change, FILE-12; never format it into
  the data) and the key is `HKEY_USERS\<SID>\Software\msi-builder\GrantAccounts\<ProductCode>`:
  the context the engine registered the product in (`MsiGetProductInfoExW`,
  `State`) overrides the marker, unless the product is registered both per
  machine and for the user, in a maintenance install (registered
  nowhere or a failed query: no remembered account) and in the commit of a
  first install (the marker only when registered nowhere), because a user
  may pass `ALLUSERS` to an elevated repair; only the removal's commit,
  after the product was unregistered, goes by the marker;
  never use `HKCU` there, which is LocalSystem's hive when an elevated
  per-user install runs the action as LocalSystem, and never fall back to
  HKLM, which users who install the same product per user would share), because `msiexec /fa` and Apps & Features
  repairs run without the first install's command line properties (observed
  on the CI runner, SEC-02). Wide strings in this part
  are `u"..."` literals, which clang types as `unsigned short` arrays.
  A file or folder grant never reads or writes a DACL by name:
  `openGrantObject` opens the object (`READ_CONTROL | WRITE_DAC`, all
  share modes, never `MAXIMUM_ALLOWED`, or `SetSecurityInfo` does not
  propagate to children), refuses it unless the handle's final path is
  the long form of the formatted path (a junction above it reached
  another tree, SEC-06), and the DACL goes through
  `GetSecurityInfo`/`SetSecurityInfo` on that handle. Services stay on the
  named calls.
- `close` (spec 17.9) uses the exports `BuilderCloseSchedule` (immediate,
  before InstallValidate, as the user: `WM_CLOSE` to the visible unowned
  top-level windows of every process whose image is the file,
  to its hidden ones only when it has none, then a wait) and `BuilderClose`
  (the deferred `BuilderCloseExec`, LocalSystem, after StopServices:
  `TerminateProcess` with 1602). They are the only user32 imports; keep
  user32 out of every other path. A process matches when its
  `QueryFullProcessImageNameW` path equals the file's (`CompareStringOrdinal`
  without case) or, failing that, names the same file
  (`FileIdInfo`: volume serial and 128-bit ID), never by name alone, so
  copies elsewhere survive: a process started by the 8.3 path reports
  that path (CLOSE-02; one started through a junction reported the
  resolved path, so the text matched there). Every
  process is opened with `PROCESS_QUERY_LIMITED_INFORMATION` only and a
  match again with the action's access, so the deferred action does not
  hold terminate handles to unrelated processes. The paths
  `BuilderCloseExec` ends come from type-51 actions of the engine
  (`[BuilderCloseExec][DIR]file|`), never from the schedule action: the
  impersonated custom action server runs with the user's token, so data it
  sets for a LocalSystem action is under the user's control. Grants
  (above) work the same way since 2026-10-30. Neither close action fails the install; the schedule action
  returns `ERROR_INSTALL_USEREXIT` only after a cancel.
- Folder removal (spec 17.10) is the export `BuilderRemoveFoldersExec` for
  the rollback (`u`), deferred (`m`, or `d` with `RollbackDisabled`) and
  commit (`c`) actions. Type-51 actions of the engine right after
  RemoveFiles build its data (`internal/builder/removefolders.go`): the
  head `<op><scope><ProductCode>:` plus one `#` per entry, then `n<count>`
  with the number of fixed lines (written by the builder), `s<name>|value`
  lines for the standard folders (`cadll.RemoveFoldersStandard()`),
  `p<path>` lines for the package's directories and one `f<property>|value`
  line per entry whose condition held; `parseRemoveData` fails on a line
  count that differs (a line break in a formatted value; FILE-13) and on lines out of that order. Never put a count of fixed lines
  into a setter's target as `#`s: with many directories it outgrows the
  255 characters of ICE03. There is
  no immediate action any more: every check (`checkEntry`) runs in the
  deferred action as LocalSystem, the own profile is matched by the
  engine's `UserSID`, never by the token, which is LocalSystem's there.
  Package folders are protected by path, not by property name: unless the
  scope is `a` (`REMOVE~="ALL"`), `packageRefusal` refuses a folder that is
  or holds one of the `p` paths, because any property can alias the
  install dir. The deferred action draws the token and records it, and
  each folder's final path under the number of its `f` line before the
  move, in `HKCU\Software\msi-builder\RemoveFolders\<ProductCode>`
  (LocalSystem's hive); the rollback and the commit take the folders and
  the token from that record, never from their data, and delete it. Commit
  actions cannot read `ProductCode`, so it comes in the data.
  Read-only files go with `FileDispositionInfoEx` and
  `FILE_DISPOSITION_FLAG_IGNORE_READONLY_ATTRIBUTE` (the handle needs
  `FILE_WRITE_ATTRIBUTES`); before Windows 10 1809, Server 2016 included,
  that call fails and the old clear-then-delete path with its link-count
  check runs.
  Do not go back to temporary `RemoveFile` rows: RemoveFiles
  deletes by path and followed a junction swapped in after the listing
  (FILE-11, CI run 41058565605), an arbitrary delete as LocalSystem. The
  deferred side therefore works by handle: open without following
  (`FILE_FLAG_OPEN_REPARSE_POINT`), re-check the final path by that
  handle, rename by that handle into `X:\BuilderRemoveFolders.<token>`,
  and delete entries opened relative to their folder's handle with
  `NtCreateFile`, deleting links as links. Rename into a directory handle
  with `NtSetInformationFile(FileRenameInformation)`:
  `SetFileInformationByHandle(FileRenameInfo)` with `RootDirectory` set
  fails with error 87 (FILE-11, CI run 41064763472). Public folder
  properties are secure by default (or `configurable`), so a standard
  user's command line can reach an elevated session: the deferred side
  reads the engine's `UserSID` and asks Authz
  (`AuthzInitializeContextFromSid`) for that account's groups, and does
  nothing unless it is LocalSystem or holds Administrators. Every op
  checks, but only when the deferred action holds Administrators itself (a
  per-user install without elevation runs it as the user). FILE-12
  checks that a `UserSID` set by an earlier action does not reach the
  deferred side. The backup folder's name (the token) is visible to anyone who
  lists the volume root; never rely on it being secret, only on
  `openBackup`'s owner and protected-DACL check. The
  guards (drive roots, standard folder properties and their parents
  compared by final path, profiles by the engine's `UserSID`, the Windows folder, a
  reparse point as the folder, a junction or link above it; not a volume
  mounted in a folder above it) only ever skip an entry, and no action
  fails the installation: a cleanup must not block an uninstall. Test guards only on
  folders the test creates (FILE-11 points `TemplateFolder` into one), so
  a broken guard cannot delete anything that matters.
  The record is the only tie between a backup folder's random name and
  the original path: when a restore or a delete fails, `keepLeft` logs
  the backup path (the one case the log shows the token) and copies the
  mapping to `HKCU\Software\msi-builder\RemoveFoldersLeft\<token>`
  before the record goes, and `keepStale` does the same for a stale
  record before a new removal replaces it. Nothing deletes that key.
- Never take code from another installer toolkit's custom action library;
  the Windows Installer custom action contract is documented by Microsoft
  and the spec (section 9.4).
