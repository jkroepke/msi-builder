# CA-10: firewall rules for any protocol and ignoreFailure (CI)

- Runner: GitHub `windows-latest` (Windows Server 2025, x64), elevated,
  2026-11-01
- CI run 41298591380 (commit f44ad00), job
  `Build & Test (windows-latest, native)`, PR #206
- Tests in `internal/builder/nativetest/firewall_windows_test.go`:
  `TestNetshAnyProtocolPort`, `TestMsiexecFirewallAnyProtocol`,
  `TestMsiexecFirewallIgnoreFailure`, `TestMsiexecFirewallWixBlackBox`,
  `TestMsiexecFirewallServiceStopped` (run once with
  `MSI_BUILDER_STOP_FIREWALL=1`)

Every rule name starts with `MsiBuilderCA10`, and the cleanups delete only
those names.

## netsh with protocol any and a port

```text
netsh advfirewall firewall add rule name=MsiBuilderCA10Netsh dir=in action=allow protocol=any localport=12345 program=C:\MsiBuilderCA10\app.exe
exit status 1
Ports can only be specified if the protocol is TCP or UDP.
```

No rule was added.

## msi-builder rule with protocol any and a port

`protocol: any`, `port: '12345,12350-12352'`. After the install,
`show rule name=MsiBuilderCA10Any dir=in` lists two rules:

| Protocol | LocalPort | RemoteIP |
| --- | --- | --- |
| TCP | `12345,12350-12352` | Any |
| UDP | `12345,12350-12352` | Any |

After the uninstall no rule of the name is left.

## A failing add with and without ignoreFailure

`remoteAddresses: '[REMOTE]'`, installed with `REMOTE=not-an-address`:

| Rule | Exit code | Rules | `app.exe` installed |
| --- | --- | --- | --- |
| without `ignoreFailure` | 1603 | none | no (rolled back) |
| `ignoreFailure: true` | 0 | none | yes; uninstall exit 0 |

## WiX 5 package (black box)

WiX 5.0.2 with `WixToolset.Firewall.wixext` 5.0.2, one file with two
exceptions: `Port="12346"` without `Protocol`, and `Port="12347"
Protocol="tcp" IgnoreFailure="yes"` with `<fw:RemoteAddress Value="[REMOTE]" />`.
Installed with `REMOTE=not-an-address`:

- Exit code 0.
- `MsiBuilderCA10WixPort`: one rule, protocol TCP, local port 12346
  (`Get-NetFirewallPortFilter`: `protocol=TCP localport=12346`). No UDP rule.
- `MsiBuilderCA10WixIgnore`: no rule.

## Firewall service stopped

The test added the stop right for Administrators to the service's security
descriptor (`(A;;CCLCSWRPLORCWDWO;;;BA)` became
`(A;;CCLCSWRPWPLORCWDWO;;;BA)`; `sc sdset` succeeded) and ran
`Stop-Service mpssvc -Force`:

```text
Stop-Service : Service 'Windows Defender Firewall (mpssvc)' cannot be stopped due to the following error: Cannot stop
mpssvc service on computer '.'.
Running
```

The service kept running, so both packages installed their rule (exit 0)
and uninstalled with exit 0; that says nothing about a stopped service.
`sc start` reported 1056 (already running), and the original descriptor
was restored.

Not tested: an install while the firewall service is stopped.
