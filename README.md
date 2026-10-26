# nut-ng

UPS monitoring that is easy to set up. One self-contained binary, one short
config file, clear error messages, and an API that monitoring tools can
query.

> **Status:** early development (milestone 3 of 7). USB UPSes work on
> Windows and Linux. nutng serves the NUT protocol, Prometheus metrics and
> a REST API, and installs itself as a service. nutng's own shutdown
> handling, plugins and SNMP are next; meanwhile NUT's upsmon works with
> nutng for shutdowns. See [docs/PLAN.md](docs/PLAN.md).

## Get started

Build it (Go 1.26 or newer):

```sh
go build -o bin/nutng ./cmd/nutng
```

Plug in the UPS's USB cable, then:

```sh
nutng detect    # list the UPSes nutng can see
nutng init      # pick one, name it, and write nutng.yaml
nutng serve     # run the daemon
```

Without a config file, `nutng serve` monitors every USB UPS it finds. To
run it at boot, install it as a service (as root, or in an Administrator
terminal on Windows):

```sh
nutng service install    # also: start, stop, restart, status, uninstall
nutng doctor             # check the whole setup, with fixes for problems
```

In another terminal:

```sh
nutng status                     # summary, or detail when there is one UPS
nutng watch                      # live view with recent events
nutng vars cyberpower            # every variable the UPS reports
nutng cmd cyberpower             # list the commands it accepts
nutng cmd cyberpower beeper.disable
nutng set cyberpower battery.charge.low 20
```

No UPS at hand? `nutng serve --demo` adds a simulated one whose mains power
fails for one minute every three minutes; `nutng cmd demo sim.power.fail`
rehearses an outage.

### USB UPS support

The `usbhid` driver reads UPSes that follow the USB HID Power Device class:
most APC, CyberPower, Eaton, Tripp Lite and similar units. It uses the
operating system's own HID driver:

- **Windows:** nothing to install, and no driver replacement (libusb, Zadig).
  Windows keeps showing the UPS as a battery while nutng reads it.
- **Linux:** uses hidraw. To run nutng as a regular user, install
  [packaging/linux/90-nutng-ups.rules](packaging/linux/90-nutng-ups.rules).
- **macOS and FreeBSD:** not yet (milestone 6).

Tested on hardware: CyberPower CP1500PFCLCD on Windows. APC and Eaton
mappings follow the standard layout but haven't been checked on real units
yet.

When a UPS shows wrong or missing values, record it:

```sh
nutng debug capture            # writes nutng-capture-<vendor>-<product>-<time>.json
```

The file lists every field the UPS reports, with the serial number removed.
Attach it to a bug report, or play it back without the hardware:

```yaml
ups:
  lab:
    driver: replay
    file: nutng-capture-0764-0601-20261025-235000.json
```

Field-to-variable mappings live in YAML
([drivers/usbhid/quirks](drivers/usbhid/quirks)). To fix or extend them for
your model without rebuilding, put a file in `quirks.d/` next to your
`nutng.yaml`.

## Monitoring tools

nutng speaks three protocols. All listen on this machine only until you
change the addresses under `listen:` (for example `nut: ":3493"`).

**NUT protocol** (port 3493): anything that works with NUT's upsd works
with nutng: `upsc`, `upsmon`, Home Assistant's NUT integration, Synology
and QNAP NAS "network UPS" settings, and Nagios/Icinga `check_ups`. It is
tested against the real NUT 2.8 clients, including `upsmon` primary and
secondary through a power failure
([test/integration/nut-clients.sh](test/integration/nut-clients.sh)).

```sh
upsc cyberpower@localhost
```

Reading needs no login. upsmon and commands need a user:

```yaml
users:
  upsmon:                       # for MONITOR lines in upsmon.conf
    password_env: NUTNG_UPSMON_PASSWORD
    role: primary               # secondary: watch only; primary: may set FSD
  admin:
    password: change-me
    role: admin                 # primary, plus upscmd and upsrw
```

**Prometheus**: metrics are at `http://127.0.0.1:9300/metrics`, for
example `nutng_battery_charge_percent`, `nutng_battery_runtime_seconds`,
`nutng_ups_status{flag="OB"}` and `nutng_ups_up`.

**REST API**: read-only over the network unless you set a token:

```sh
curl http://127.0.0.1:9300/api/v1/ups
curl http://127.0.0.1:9300/api/v1/ups/cyberpower/vars/battery.charge
curl -N http://127.0.0.1:9300/api/v1/events          # Server-Sent Events
```

The full API is described at `/api/v1/openapi.yaml`.

## Configuration

To configure nutng, run `nutng init` or create `nutng.yaml` yourself
(`/etc/nutng/nutng.yaml` on Linux, `C:\ProgramData\nutng\nutng.yaml` on
Windows, `/usr/local/etc/nutng/nutng.yaml` on macOS and FreeBSD, or pass
`--config`). See [nutng.example.yaml](nutng.example.yaml).

```sh
nutng config validate   # check it without starting anything
nutng config show       # the effective config, with defaults
```

Mistakes are reported with the file, line and a suggestion:

```
nutng.yaml:4: UPS rack (usbhid driver): unknown option "serail" (did you mean "serial"?)
```

## Layout

| Path | What |
|---|---|
| `cmd/nutng` | The CLI and daemon entry point |
| `pkg/sdk` | Contracts for drivers and outputs (built-in and plugins) |
| `proto/nutng/v1` | The same contracts as gRPC services, for external plugins |
| `internal/core` | State store, event bus, driver supervision |
| `internal/config` | Loading and validating `nutng.yaml` |
| `drivers/usbhid` | USB HID UPS driver: transports, mapping, capture/replay |
| `drivers/usbhid/hid` | HID report descriptor parser |
| `drivers/usbhid/quirks` | Field-to-variable mappings per vendor |
| `drivers/simulator` | Simulated UPS |
| `outputs/rest` | REST/JSON API and event stream |
| `outputs/nutproto` | NUT protocol server (upsd compatible) |
| `outputs/prometheus` | Prometheus metrics |
| `internal/svcmgr` | Service install and control |

## Development

```sh
go test ./...
go vet ./...
NUTNG_HW=1 go test ./drivers/usbhid -run TestHardware -v   # with a UPS attached
go build -o nutng ./cmd/nutng && test/integration/nut-clients.sh ./nutng   # Linux
```
