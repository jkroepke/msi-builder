# UI-12: scope page Next and a folder restored after the page (probe)

- Runner: GitHub `windows-latest` (x64), elevated, interactive desktop, 2026-11-01
- Probe run 41334878746 (`.github/workflows/probe.yaml`, deleted afterwards), main at fe530df
- Test: `TestProbeScopePage`

Each case builds a `perUserOrMachine` package with `ui: { enabled: true }`,
a directory search `SAVEDDIR` of `HKCU\Software\MsiBuilderTest\<case>\Dir`, a
registry search `PREVMACHINE` of an HKLM marker, and a `setProperty` action
`RestoreSaved` (`INSTALLDIR = [SAVEDDIR]`, condition
`SAVEDDIR AND NOT INSTALLDIR`) placed `after: LaunchConditions` in `ui` and
`after: AppSearch` in `execute`. The UI driver accepts every default (Next,
Install, Finish, OK).

| Case | Exit | Result |
| --- | --- | --- |
| `Dir` set to an existing folder under `%LOCALAPPDATA%` | 0 | registered per user; the scope page's Next added `INSTALLDIR` with the per-user default folder, so `RestoreSaved` was skipped in the client ("condition is false") and in the server, which took the client's `INSTALLDIR` |
| HKLM marker set, launch condition `Installed OR ALLUSERS = 1 OR NOT PREVMACHINE` | 1603 | the page preselected "just for you"; after Next the launch condition's message "Already installed for everyone." ended the install |

Client log of the first case:

```text
MSI (c) (E8:70) [18:07:39:064]: PROPERTY CHANGE: Adding SAVEDDIR_FOUND property. Its value is 'C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\'.
MSI (c) (E8:70) [18:07:39:064]: Doing action: SearchApply_SAVEDDIR
Action 18:07:39: SearchApply_SAVEDDIR. 
Action start 18:07:39: SearchApply_SAVEDDIR.
MSI (c) (E8:70) [18:07:39:066]: PROPERTY CHANGE: Adding SAVEDDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\'.
Action ended 18:07:39: SearchApply_SAVEDDIR. Return value 1.
MSI (c) (E8:70) [18:07:39:066]: Skipping action: SetInstallScopeMachine (condition is false)
MSI (c) (E8:70) [18:07:39:066]: Doing action: SetInstallScopeUser
Action 18:07:39: SetInstallScopeUser. 
Action start 18:07:39: SetInstallScopeUser.
Action ended 18:07:39: SetInstallScopeUser. Return value 1.
MSI (c) (E8:70) [18:07:39:066]: Doing action: SetInstallScopeUser_INSTALLDIR
Action 18:07:39: SetInstallScopeUser_INSTALLDIR. 
Action start 18:07:39: SetInstallScopeUser_INSTALLDIR.
MSI (c) (E8:70) [18:07:39:066]: PROPERTY CHANGE: Adding InstallScopeUser_INSTALLDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\'.
Action ended 18:07:39: SetInstallScopeUser_INSTALLDIR. Return value 1.
MSI (c) (E8:70) [18:07:39:066]: Doing action: SetInstallScopeMachine_INSTALLDIR
Action 18:07:39: SetInstallScopeMachine_INSTALLDIR. 
Action start 18:07:39: SetInstallScopeMachine_INSTALLDIR.
MSI (c) (E8:70) [18:07:39:066]: PROPERTY CHANGE: Adding InstallScopeMachine_INSTALLDIR property. Its value is 'C:\Program Files\MsiBuilderScopeProbe0\'.
Action ended 18:07:39: SetInstallScopeMachine_INSTALLDIR. Return value 1.
MSI (c) (E8:70) [18:07:39:066]: Doing action: InstallScopeDlg
Action 18:07:39: InstallScopeDlg. 
Action start 18:07:39: InstallScopeDlg.
Action 18:07:39: InstallScopeDlg. Dialog created
MSI (c) (E8:E4) [18:07:41:135]: PROPERTY CHANGE: Adding INSTALLDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\'.
Action ended 18:07:41: InstallScopeDlg. Return value 1.
MSI (c) (E8:70) [18:07:41:169]: Doing action: LaunchConditions
Action 18:07:41: LaunchConditions. Evaluating launch conditions
Action start 18:07:41: LaunchConditions.
Action ended 18:07:41: LaunchConditions. Return value 1.
MSI (c) (E8:70) [18:07:41:173]: Skipping action: RestoreSaved (condition is false)
MSI (c) (E8:70) [18:07:41:185]: Dir (target): Key: INSTALLDIR	, Object: C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\
MSI (c) (E8:70) [18:07:44:307]: Switching to server: INSTALLDIR="C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\" SAVEDDIR="C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\" SAVEDDIR_FOUND="C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\" TARGETDIR="D:\" REBOOT="ReallySuppress" CURRENTDIRECTORY="D:\a\msi-builder\msi-builder\internal\builder\nativetest" CLIENTUILEVEL="0" CLIENTPROCESSID="6376" SOURCEDIR="C:\Users\RUNNER~1\AppData\Local\Temp\TestProbeScopePage2304225682\001\" ACTION="INSTALL" EXECUTEACTION="INSTALL" ROOTDRIVE="D:\" INSTALLLEVEL="1" SECONDSEQUENCE="1"  ADDLOCAL=MainFeature  
MSI (s) (18:64) [18:07:44:442]: Command Line: INSTALLDIR=C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\ SAVEDDIR=C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\ SAVEDDIR_FOUND=C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\ TARGETDIR=D:\ REBOOT=ReallySuppress CURRENTDIRECTORY=D:\a\msi-builder\msi-builder\internal\builder\nativetest CLIENTUILEVEL=0 CLIENTPROCESSID=6376 SOURCEDIR=C:\Users\RUNNER~1\AppData\Local\Temp\TestProbeScopePage2304225682\001\ ACTION=INSTALL EXECUTEACTION=INSTALL ROOTDRIVE=D:\ INSTALLLEVEL=1 SECONDSEQUENCE=1 ADDLOCAL=MainFeature ACTION=INSTALL 
MSI (s) (18:64) [18:07:44:446]: PROPERTY CHANGE: Adding INSTALLDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\Programs\MsiBuilderScopeProbe0\'.
MSI (s) (18:64) [18:07:44:446]: PROPERTY CHANGE: Adding SAVEDDIR property. Its value is 'C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\'.
MSI (s) (18:64) [18:07:44:446]: PROPERTY CHANGE: Adding SAVEDDIR_FOUND property. Its value is 'C:\Users\runneradmin\AppData\Local\MsiBuilderCustomScopeProbe0\'.
MSI (s) (18:64) [18:07:44:566]: Skipping action: SearchApply_SAVEDDIR (condition is false)
MSI (s) (18:64) [18:07:44:566]: Skipping action: RestoreSaved (condition is false)
```
