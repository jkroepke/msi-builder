# internal/codepage

- The ANSI code pages come from `tables/<cp>.bin`, dumped from Windows by
  `TestWindowsTables` (`tables_windows_test.go`); the layout is documented in
  `tables.go`. Never replace them with a generic character set library
  (`golang.org/x/text` and the standards differ from Windows in private-use
  rows, vendor rows and duplicates) or with third-party table files.
- To regenerate, run on Windows:
  `$env:MSI_BUILDER_UPDATE_TABLES='1'; go test ./internal/codepage -run TestWindowsTables -count=1`.
  Without the variable the test compares the embedded tables with the
  runner's `MultiByteToWideChar`/`WideCharToMultiByte` on every Windows CI
  run, and `TestWindowsDecodePairs` checks `Decode` for every one- and
  two-byte input.
- `Decode` accepts exactly what Windows accepts with `MB_ERR_INVALID_CHARS`,
  including many-to-one rows (CP932 `FA 5C` and `ED 40`), so a decode and
  re-encode does not have to give back the same bytes. Never add a byte
  round-trip check.
- Cross-platform reference vectors verified on Windows are in
  `TestWindowsVectors`.
- Without a Windows machine, a CI run can carry a fresh dump: let
  `TestWindowsTables` log `compress(native.marshal())` in base64 on failure,
  read it from the job log and drop the logging again (how the first tables
  were made).
