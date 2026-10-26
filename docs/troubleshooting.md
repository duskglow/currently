# Troubleshooting

## Start with `nutng doctor`

```sh
sudo nutng doctor
```

It checks the config, the service, whether the daemon answers on each port
(REST, metrics, NUT, SNMP), the USB UPSes it can see and whether this user
may open them, each UPS's state, plugins, the shutdown policy, and NUT
programs that would conflict. Every problem comes with the command that
fixes it.

## Common problems

**"no USB UPS found".** Check the cable and that the UPS appears in
`lsusb` (Linux), Device Manager under Batteries (Windows), System
Information → USB (macOS) or `usbconfig list` (FreeBSD). On Linux and
FreeBSD only one program can use the UPS: stop NUT's driver first.

**"permission denied".** The service runs as root and is unaffected. To
use the CLI as a regular user on Linux, install the udev rule and join the
`nutng` group (see [Platforms](platforms.md#linux)).

**A port is in use.** Another program (NUT's upsd on 3493, snmpd on 161)
has it. Stop it, or move nutng with the `listen:` settings. SNMP on a port
below 1024 needs root.

**Wrong or missing values.** Record the UPS with `nutng debug capture` and
open an issue with the file attached. Most fixes are a few lines in a
[quirks file](drivers.md#usbhid-default), which you can also add yourself
in `quirks.d/`.

**Config errors.** `nutng config validate` names the file, the line and
usually the fix:

```
nutng.yaml:4: UPS rack (usbhid driver): unknown option "serail" (did you mean "serial"?)
```

## Logs

| Platform | Where |
|---|---|
| Linux (systemd) | `journalctl -u nutng` |
| Windows | Event Viewer → Windows Logs → Application, source `nutng` |
| macOS | `/var/log/nutng.log` with the packaged plist, or the unified log (`log show --predicate 'process == "nutng"'`) |
| FreeBSD | `/var/log/messages` |

`log.level: debug` in `nutng.yaml` shows every request and reading.
