# REG-09 / ci
GitHub Actions `windows-latest` (Windows Server 2025, image windows-2025-vs2026 20261021.278), elevated, 2026-11-01.
`TestMsiexecComponentFileTypes` and `TestICEComponentFileTypes` (`internal/builder/nativetest/filetypes_windows_test.go`), first at commit 89c46ca, run 41299486106 (job 124827639988, per machine only), then at commit 03fc1c4, run 41300610014 (job 124834269769, ICE suite per machine and per user), PR #212.
The package (`MsiBuilderFileTypeFeatures`) has features `Main` (with `app.exe` and `doc.ico`) > `Doc` (on) and `Plain` (off by default). `Doc` holds component `AssocDoc` with only component `fileTypes` for `file: app.exe` (ProgID `MsiBuilderTest.FeatureDoc`, a description, `iconFile: doc.ico`, extension `.msibtfdoc`), `Plain` holds `AssocPlain` (ProgID `MsiBuilderTest.FeaturePlain` without a description, `iconFile: doc.ico` with `iconIndex: 1`, extension `.msibtfpln`). Neither component has a file: `AssocDoc` is keyed on the ProgID's description, `AssocPlain` on its `DefaultIcon`.
The msiexec test asserts, per machine, and logs nothing when it passes:
- install: under `HKLM\SOFTWARE\Classes`, `.msibtfdoc` = the ProgID, the description, `DefaultIcon` = `"C:\Program Files\MsiBuilderFileTypeFeatures\doc.ico",0` and `shell\open\command` = `"C:\Program Files\MsiBuilderFileTypeFeatures\app.exe" "%1"`; none of the `Plain` keys exists; `AssocQueryStringW(ASSOCSTR_DEFAULTICON, ".msibtfdoc")` returns `"C:\Program Files\MsiBuilderFileTypeFeatures\doc.ico",0`.
- `ADDLOCAL=Plain`: the `Plain` keys hold the same paths (`DefaultIcon` index 1), the `Doc` values are unchanged.
- `REMOVE=Plain`: the `Plain` keys are gone, the `Doc` values unchanged.
- uninstall: none of the keys of either component is left.
```
=== RUN   TestMsiexecComponentFileTypes
--- PASS: TestMsiexecComponentFileTypes (1.77s)
```
ICE suite, run 41300610014: no errors. Warnings only: ICE33 for every association row (the usual warning for `Registry`-row associations, section 10.2), ICE61 for the `Upgrade` row that includes the package's own version, and per user ICE91 for the two files in `INSTALLDIR`. ICE38 (HKCU key path for a profile folder) and ICE69 (cross-feature references) reported nothing.
```
=== RUN   TestICEComponentFileTypes/perMachine
    filetypes_windows_test.go:439: ICE33 warning: … ProgId should be registered via the ProgId table. … (3 rows)
    filetypes_windows_test.go:439: ICE33 warning: … ProgId - Icon associations should be registered via the ProgId and Icon tables. … (2 rows)
    filetypes_windows_test.go:439: ICE33 warning: … Shell extension verbs info should be registered via the Verb table. … (2 rows)
    filetypes_windows_test.go:439: ICE33 warning: … Extensions should be registered via the Extension table. … (2 rows)
    filetypes_windows_test.go:439: ICE61 warning: This product should remove only older versions of itself. The Maximum version is not less than the current product. (1.0.0 1.0.0)
=== RUN   TestICEComponentFileTypes/perUser
    (the same ICE33 and ICE61 warnings)
    filetypes_windows_test.go:439: ICE91 warning: The file 'app.exe' will be installed to the per user directory 'INSTALLDIR' that doesn't vary based on ALLUSERS value. …
    filetypes_windows_test.go:439: ICE91 warning: The file 'doc.ico' will be installed to the per user directory 'INSTALLDIR' that doesn't vary based on ALLUSERS value. …
--- PASS: TestICEComponentFileTypes (3.19s)
    --- PASS: TestICEComponentFileTypes/perMachine (1.59s)
    --- PASS: TestICEComponentFileTypes/perUser (1.60s)
```
Not tested: installing the package per user with msiexec (only the ICE suite ran per user).
