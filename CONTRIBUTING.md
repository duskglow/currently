# Contributing

Thanks for helping. A UPS monitor is only as good as the UPSes, protocols and failure
cases it has seen.

## Project status

Currently is alpha. It has been tested on one model of UPS (CyberPower CP1500PFCLCD), so
the most valuable contribution is a report from other hardware, working or not, with a
capture attached (see below). Well-tested fixes, quirk files for new models and new
plugins (drivers and outputs) are the most likely to be merged. [CLAUDE.md](CLAUDE.md)
explains how support for a UPS is added.

## Setup

With Go 1.26 or newer:

```bash
go build ./...
go test ./...
go vet ./...
golangci-lint run ./...
```

On Linux, `test/integration/nut-clients.sh` and `test/integration/snmp.sh` test Currently
against the real NUT clients and net-snmp tools. They shut nothing down.

## Conduct

Be professional, keep discussions technical, and keep politics out of the project entirely.
See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Using AI tools

AI-assisted contributions are welcome. You remain responsible for everything you submit:
test it, review it, and keep commit messages factual. See [AI_POLICY.md](AI_POLICY.md).

## Ground rules

* **Tests must never shut anything down.** Nothing in `go test`, the integration scripts or
  CI may shut a computer down or send a power command (`load.off`, `shutdown.return` and
  the like) to a real UPS. Tests use the fake executor, simulated UPSes (`simulator`,
  `replay`), `CURRENTLY_NEVER_SHUTDOWN=1` and harmless `shutdown.command`s, so anyone can run
  them on any machine.
* **Testing on your own hardware is up to you.** Letting Currently shut your own machines
  down, or cut a UPS's power, is a good test, and the only way to know that shutdowns and
  `power_off_ups` work with your setup. Do it knowingly: rehearse first with
  `currently test shutdown` and `shutdown.dry_run: true`, and remember that a UPS power
  command cuts power to everything plugged into it.
* **Never guess silently.** If Currently cannot read or decode something, it says so, in
  the log, `currently doctor` or the error, with how to fix it.
* **No cgo**, on any platform: Currently is one self-contained binary.
* **Don't copy code from NUT** (it is GPL). Protocol facts are fine; check them against the
  specification or vendor documentation.
* New kinds of UPS and new outputs belong in plugins where possible (see
  [docs/plugins.md](docs/plugins.md)); new USB models usually need only a quirk file.
* Add tests. Bugs found on real hardware get a regression test built from that hardware's
  capture (`drivers/usbhid/testdata`), with serial numbers removed.

## Reporting a UPS problem

Please include:
* the output of `currently version` and `currently doctor`;
* the output of `currently vars <ups>`, and what the UPS's own display shows;
* the file from `currently debug capture` (it removes the serial number), ideally made on
  Linux, macOS or FreeBSD, which show more than Windows;
* the log with `log.level: debug`, around the problem.

Never attach configs with passwords, API tokens or SNMP communities publicly.
