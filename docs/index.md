# Currently

Currently (`currently`) monitors UPSes and shuts computers down safely when the
power runs out, like [NUT](https://networkupstools.org/), but with one
self-contained binary, one short config file and error messages that say
how to fix the problem.

- **Finds your UPS by itself.** Plug in the USB cable and run `currently serve`:
  no config is needed to start. `currently init` writes a config in a few
  questions.
- **Works with the tools you already use.** It speaks the
  [NUT protocol](outputs/nut.md) (upsc, upsmon, Home Assistant, Synology,
  QNAP), [SNMP](outputs/snmp.md) (LibreNMS, Zabbix, PRTG, Observium),
  [Prometheus](outputs/prometheus.md), a [REST API](outputs/rest.md) and
  [MQTT](outputs/mqtt.md).
- **Shuts down safely.** As primary or secondary, alongside NUT's own
  upsmon and upsd, with [rehearsals](shutdown.md#testing-it-safely) that
  shut nothing down.
- **Runs everywhere.** Linux, Windows, macOS and FreeBSD, as a service, with
  no drivers to install. See [Platforms](platforms.md).
- **Extensible without rebuilding.** New UPS drivers and new outputs are
  [plugins](plugins.md), in Go, Python or anything that speaks gRPC.

```console
$ currently status
rack  CyberPower CP1500PFCLCD  (usbhid driver)
  Status      online, charging
  Battery     100%, 45m30s left, 24.0 V
  Input       121.0 V, 60.0 Hz
  Output      120.0 V, 60.0 Hz
```

!!! warning "Alpha"
    Every planned feature is built, but Currently has been tested with one
    model of UPS (the CyberPower CP1500PFCLCD, on Windows and Linux), and
    much of it only against simulators. Run it alongside your current setup
    until you have watched it through a real power cut, and rehearse
    shutdowns with `currently test shutdown` and `shutdown.dry_run` first.
    Other UPSes should work (most follow the same USB standard), but are
    untested: if you try one, please report it with the file from
    `currently debug capture`.

Start with [Install](install.md) and the [Quick start](quickstart.md).
