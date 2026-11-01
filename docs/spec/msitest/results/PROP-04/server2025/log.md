# PROP-04 on Windows Server 2025

Run on 2026-11-01 on a Windows Server 2025 VM (elevated, local time UTC+1)
with the probe `TestMsiexecRestartManagerControl` of PR #208 (commit
8d79437, removed again before the merge). The package installs one file,
`a.txt`, to `C:\Program Files\MsiBuilderRestartManager` with
`MSIRESTARTMANAGERCONTROL` in the `Property` table. For each value the probe
installs it, starts a hidden PowerShell (`CREATE_NO_WINDOW`) that opens the
file with `FileShare.Read`, and runs `msiexec /fa <package> /qn /norestart
/l*v <log>`. The run ended in a machine restart during the second value, so
`DisableShutdown` and `disable` were not run. The verbose logs stayed on the
VM; the lines below are copied from them and from the event logs.

## Command lines

Both calls passed `/qn /norestart`; the server-side command lines differ:

- install: `Command Line: REBOOT=ReallySuppress CURRENTDIRECTORY=...`
- repair: `Command Line: REINSTALL=ALL REINSTALLMODE=a CURRENTDIRECTORY=...`

`/norestart` was turned into `REBOOT=ReallySuppress` on `/i` but not on
`/fa`. A preflight `/fa` repair with `REBOOT=ReallySuppress` on its command
line (and no file held) gave the same server line without `REBOOT`: `/f`
did not pass `REBOOT` to the server (other command-line properties were not
checked).

## `0`

Install:

```text
RESTART MANAGER: Session opened.
Note: 1: 2205 2:  3: FilesInUse
RESTART MANAGER: Session closed.
```

Repair (exit code 0; the holder ended with exit status 1):

```text
RESTART MANAGER: Will attempt to shut down and restart applications in no UI modes.
RESTART MANAGER: Successfully shut down all applications in the service's session that held files in use.
(c) RESTART MANAGER: Successfully shut down all applications that held files in use.
RESTART MANAGER: Previously shut down applications have been restarted.
```

Application log, source RestartManager, 14:28:53:

- 10002: "Shutting down application or service 'Windows PowerShell'."
- 10005: "Machine restart is required."
- 10010: "Application '...powershell.exe' (pid 31544) cannot be restarted -
  Application SID does not match Conductor SID.."

The msiexec log says the programs were restarted while event 10010 says
the holder could not be, and the repair returned 0 although event 10005
says a restart is required.

## `Disable`

Install and repair:

```text
RESTART MANAGER: Disabled by MSIRESTARTMANAGERCONTROL property; Windows Installer will use the built-in FilesInUse functionality.
```

Repair:

```text
Info 1603.The file C:\Program Files\MsiBuilderRestartManager\a.txt is being held in use.  Close that application and retry.
Info 1903.Scheduling reboot operation: Deleting file C:\Program Files\MsiBuilderRestartManager\a.txt. Must reboot to complete operation.
Info 1902.Scheduling reboot operation: Renaming file ...\TBMFF0C.tmp to ...\a.txt. Must reboot to complete operation.
You must restart your system for the configuration changes made to MsiBuilderRestartManager to take effect.
Windows Installer requires a system restart. ... Type of System Restart: 1. Reason for Restart: 1.
MainEngineThread is returning 1641
```

The holder was not ended. System log, 14:29:13: event 1074, "The process
msiexec.exe has initiated the restart of computer <host> on behalf of
user NT AUTHORITY\SYSTEM ... Reason Code: 0x80030002 Shutdown Type: restart
Comment: The Windows Installer initiated a system restart to complete or
continue the configuration of 'MsiBuilderRestartManager'."; MsiInstaller
events 1038 and 1005 at the same time; 6006 at 14:29:19 and 6005 at
14:29:34, no 6008 or 41.

The same probe on the GitHub `windows-latest` runner ended four times with
"The hosted runner lost communication with the server" and no job log,
which is consistent with this restart; which value the runner was on is
not known.

## Second run: `DisableShutdown` and `disable`

The same day, the probe ran again for these two values with the repair as
`msiexec /i <package> REINSTALL=ALL REINSTALLMODE=a /qn /norestart
REBOOT=ReallySuppress /l*v <log>`; a preflight first confirmed
`Property(S): REBOOT = ReallySuppress` on that command line. The server
command line was `REINSTALL=ALL REINSTALLMODE=a REBOOT=ReallySuppress
REBOOT=ReallySuppress CURRENTDIRECTORY=...`. The machine did not restart (no
event 1074).

### `DisableShutdown`

Exit code 3010; the holder kept running.

```text
RESTART MANAGER: Session opened.
RESTART MANAGER: Will attempt to shut down and restart applications in no UI modes.
RESTART MANAGER: Session closed.
Info 1603.The file C:\Program Files\MsiBuilderRestartManager\a.txt is being held in use.  Close that application and retry.
Info 1903.Scheduling reboot operation: Deleting file C:\Program Files\MsiBuilderRestartManager\a.txt. Must reboot to complete operation.
Info 1902.Scheduling reboot operation: Renaming file ...\TBMCC8C.tmp to ...\a.txt. Must reboot to complete operation.
Property(S): ReplacedInUseFiles = 1
Windows Installer requires a system restart. ... Type of System Restart: 2. Reason for Restart: 1.
MainEngineThread is returning 3010
```

There was no "Successfully shut down" line, so nothing was shut down despite
the "Will attempt" line. Events: RestartManager 10010 (holder "cannot be
restarted - Application SID does not match Conductor SID") and 10005
("Machine restart is required."), no 10002; MsiInstaller 1038 ("Type of
System Restart: 2") and 1029 ("The restart was deferred to a later time."),
no 1005.

### `disable`

Exit code 0; the holder was ended (exit status 1). The log had no
"Disabled by MSIRESTARTMANAGERCONTROL property" line and the same lines as
`0`:

```text
RESTART MANAGER: Will attempt to shut down and restart applications in no UI modes.
RESTART MANAGER: Successfully shut down all applications in the service's session that held files in use.
(c) RESTART MANAGER: Successfully shut down all applications that held files in use.
RESTART MANAGER: Previously shut down applications have been restarted.
MainEngineThread is returning 0
```

Events: RestartManager 10002 ("Shutting down application or service
'Windows PowerShell'."), 10005 and 10010; no 1038, 1005 or 1029. The value
is read case-sensitively: `disable` is not `Disable`.

`Disable` has not been run with `REBOOT=ReallySuppress`; 3010 is expected
from the `DisableShutdown` result but not observed.
