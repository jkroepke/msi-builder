# CAB-15: the cabinet reader against FDI and expand.exe (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025), 2026-10-31
- CI run 41242586230, commit 4a9cff5, job "Build & Test (windows-latest, unit)", step "go test"
- Tests: `TestNativeFDIRejectsBadMSZIPStreams`, `TestNativeFDIEmptyFolderOffset` and
  `TestNativeReadMakecabLegacyName` in `internal/cab/native_reader_windows_test.go`

All three passed. The run is not verbose, so the observations are what the
tests assert:

- cabinet.dll's `FDICopy` refused an MSZIP block whose DEFLATE stream decodes
  "hello" with `cbUncomp` 1, and one whose only DEFLATE block is a stored
  block with BFINAL clear; it extracted the same "hello" stream with
  `cbUncomp` 5. `cab.Read` agreed in all three cases.
- `FDICopy` extracted a cabinet whose first folder has no blocks and
  `coffCabStart` 0, beside a stored folder with one file, and `cab.Read`
  returned the same names and bytes; `cab.Walk` succeeded.
- The runner's ANSI code page was 1252 (the test fails on GitHub Actions
  otherwise instead of skipping). makecab.exe stored `résumé.txt` as the
  bytes `72 E9 73 75 6D E9 2E 74 78 74` without attribute 0x80; `cab.Read`
  returned `résumé.txt` and expand.exe extracted a file named `résumé.txt`
  with the same content.

Not covered: machines with another ANSI code page, and what expand.exe does
with the bytes 1252 leaves undefined.
