# Currently

What's your UPS doing? **Currently** knows.

UPS monitoring that is easy to set up. One self-contained binary, one short
config file, clear error messages, and an API that monitoring tools can
query. It speaks NUT's protocol, so NUT clients work with it unchanged.
(The project was called nut-ng until October 2026.)

> **Alpha.** Every planned feature is built, but Currently has been tested
> with one model of UPS, and much of it only against simulators. Don't rely
> on it alone to protect equipment yet: run it alongside your current setup
> (NUT, or your UPS maker's software) until you have watched it through a
> real power cut, and rehearse shutdowns with `currently test shutdown` and
> `shutdown.dry_run` first. Reports of what works and what doesn't are very
> welcome.

It monitors USB UPSes on Linux, Windows, macOS and FreeBSD, network-card
and serial UPSes through bundled plugins, shuts computers down safely (as
primary or secondary, and alongside NUT's upsmon and upsd), and serves the
NUT protocol, SNMP (UPS-MIB), Prometheus metrics, MQTT and a REST API. See
the [documentation](docs/index.md) and the [design plan](docs/PLAN.md).

## Supported UPSes

Tested with real hardware so far:

| UPS | Connection | Tested on |
|---|---|---|
| CyberPower CP1500PFCLCD (USB ID 0764:0601) | USB | Windows 11; Linux (Ubuntu 24.04 in a container, four units at once) |

That is the whole list because it is the only UPS the project has had to
test with, **not** because others won't work. Currently follows the
standard USB HID Power Device class that most APC, CyberPower, Eaton,
Tripp Lite and similar UPSes use, and also reads network cards (UPS-MIB,
APC PowerNet-MIB) and serial UPSes (Megatec/Q1, APC Smart). Those paths
are tested against simulators and recorded data, not yet against real
units:

- other CyberPower models, and APC and Eaton USB UPSes
- USB on macOS and FreeBSD
- SNMP network cards and serial UPSes
- UPS power commands (`power_off_ups`) on any hardware

If you try Currently with another UPS, please open an issue either way,
with the file from `currently debug capture` attached: it records what the
UPS reports (with the serial number removed), which is usually all it
takes to add or fix support. See [CLAUDE.md](CLAUDE.md) for how that is
done.

## Get started

Install a package from the releases page (deb, rpm, apk, Arch, MSI,
winget, Homebrew, FreeBSD pkg, or a container image; see
[Install](docs/install.md)), or build it (Go 1.26 or newer):

```sh
go build -o bin/currently ./cmd/currently
```

Plug in the UPS's USB cable, then:

```sh
currently detect    # list the UPSes currently can see
currently init      # pick one, name it, and write currently.yaml
currently serve     # run the daemon
```

Without a config file, `currently serve` monitors every USB UPS it finds. To
run it at boot, install it as a service (as root, or in an Administrator
terminal on Windows):

```sh
currently service install    # also: start, stop, restart, status, uninstall
currently doctor             # check the whole setup, with fixes for problems
```

In another terminal:

```sh
currently status                     # summary, or detail when there is one UPS
currently watch                      # live view with recent events
currently vars cyberpower            # every variable the UPS reports
currently cmd cyberpower             # list the commands it accepts
currently cmd cyberpower beeper.disable
currently set cyberpower battery.charge.low 20
```

No UPS at hand? `currently serve --demo` adds a simulated one whose mains power
fails for one minute every three minutes; `currently cmd demo sim.power.fail`
rehearses an outage.

Moving from NUT? `currently import-nut` converts `/etc/nut` (ups.conf,
upsd.conf, upsd.users, upsmon.conf) into a currently.yaml and lists anything it
could not convert.

## Plugins

Drivers (new kinds of UPS) and outputs (new places to send UPS data) can
be added without rebuilding Currently. A plugin is an executable named
`currently-driver-<name>` or `currently-output-<name>` in a plugin directory, or a
command listed in currently.yaml for interpreted languages:

```sh
currently plugin list          # installed plugins, and the directories searched
currently plugin info mqtt
```

```yaml
plugins:
  dirs: [/opt/currently/plugins]             # searched before the defaults
  commands:                              # plugins that need an interpreter
    - ["python3", "/opt/currently/myups/plugin.py"]
```

Currently restarts a plugin that crashes, stops plugins the config does not
use, and plugins exit when Currently does.

**MQTT** (`plugins/mqtt`, built as `currently-output-mqtt`) publishes each UPS's
state and events, with Home Assistant discovery:

```yaml
outputs:
  mqtt:
    broker: tcp://192.168.1.5:1883
    username: currently
    password_env: CURRENTLY_MQTT_PASSWORD
    allow_control: true      # accept commands on currently/<ups>/command/<name>
```

Outputs can only read unless `allow_control: true`.

**The exec driver** turns any program that prints UPS variables, as JSON
or as `name: value` lines, into a UPS. For example, to read a UPS from
another NUT server with `upsc`:

```yaml
ups:
  nas:
    driver: exec
    command: upsc ups@nas.local
    interval: 10s
```

**The SNMP driver** (`plugins/snmp`, `currently-driver-snmp`) reads UPS
network cards with the UPS-MIB or APC's PowerNet-MIB, over v1, v2c or v3:

```yaml
ups:
  rack:
    driver: snmp
    host: 192.168.1.20
    community: public
```

**The serial driver** (`plugins/serial`, `currently-driver-serial`) reads
serial UPSes with the Megatec/Q1 or APC Smart protocol:

```yaml
ups:
  closet:
    driver: serial
    port: /dev/ttyUSB0       # COM3 on Windows
```

`currently import-nut` converts NUT's `snmp-ups`, `apcsmart` and
`blazer_ser`/`nutdrv_qx` sections to these. See
[UPS drivers](docs/drivers.md).

**Writing a plugin.** `currently plugin new driver|output <name>` creates a Go
or Python (`--lang python`) project to start from. Go plugins implement the
same `sdk.Driver` / `sdk.Output` interfaces as the built-in ones
([pkg/sdk](pkg/sdk)) and call `plugin.ServeDriver` / `plugin.ServeOutput`
([pkg/plugin](pkg/plugin)). Other languages speak the gRPC protocol in
[proto/currently/v1](proto/currently/v1). See
[examples/plugins/python-driver](examples/plugins/python-driver).

## Shutting down

Currently shuts this computer down when the UPS is about to run out, as NUT's
upsmon does. Without a `shutdown` section it only monitors.

```yaml
shutdown:
  when:
    low_battery: true        # the UPS's own low-battery signal (default)
    on_battery_for: 5m       # optional: or after this long on battery
    runtime_below: 5m        # optional: or when less runtime is left
  hooks:                     # optional: run first, such as stopping VMs
    - /usr/local/bin/stop-vms
```

```sh
currently test shutdown   # the policy, how each UPS looks to it now, and every step (does nothing)
```

**Primary and secondaries.** The computer the UPS is plugged into is its
*primary*. Other computers on the same UPS follow it as *secondaries*,
either with NUT's upsmon or with Currently's `nut` driver, which also follows
NUT's own upsd:

```yaml
# on a secondary
ups:
  rack:
    driver: nut
    host: 192.168.1.10       # the primary (currently or NUT upsd)
    ups: rack
    username: mon            # a secondary user there, so the primary waits for us
    password_env: CURRENTLY_MON_PASSWORD
shutdown: {}                 # follow the primary's FSD, or low battery
```

When the primary decides to shut down, it sets FSD (forced shutdown),
waits up to `wait_for_secondaries` (15s) for the secondaries to shut down
and log out, runs its hooks, and shuts down. With `power_off_ups: true`, it
first tells the UPS to cut power after `ups_off_delay` (60s) and to restore
it when mains returns, so everything boots again by itself.

**Testing it safely.**

- `shutdown.dry_run: true` logs every step and does none of them.
- A simulated UPS (`simulator`, `replay`) never shuts the computer down:
  its outages only act on simulated UPSes, unless you set a custom
  `shutdown.command`.
- `CURRENTLY_NEVER_SHUTDOWN=1` in the environment blocks the operating
  system's shutdown command (a custom `shutdown.command` still runs, so
  point it at something harmless).
- Every policy decision and step appears in `currently watch`, the log, and
  `GET /api/v1/shutdown`.

### USB UPS support

The `usbhid` driver reads UPSes that follow the USB HID Power Device class:
most APC, CyberPower, Eaton, Tripp Lite and similar units. It uses the
operating system's own HID driver:

- **Windows:** nothing to install, and no driver replacement (libusb, Zadig).
  Windows keeps showing the UPS as a battery while Currently reads it.
- **Linux:** uses hidraw. To run Currently as a regular user, install
  [packaging/linux/90-currently-ups.rules](packaging/linux/90-currently-ups.rules).
- **macOS:** uses IOKit without seizing the UPS, so macOS's own UPS
  support keeps working. No cgo or extra libraries.
- **FreeBSD:** uses uhid(4) or hidraw(4).

See [Supported UPSes](#supported-upses) for what has been tested on real
hardware, and [Platforms](docs/platforms.md) for each operating system.

When a UPS shows wrong or missing values, record it:

```sh
currently debug capture            # writes currently-capture-<vendor>-<product>-<time>.json
```

The file lists every field the UPS reports, with the serial number removed.
Attach it to a bug report, or play it back without the hardware:

```yaml
ups:
  lab:
    driver: replay
    file: currently-capture-0764-0601-20261025-235000.json
```

Field-to-variable mappings live in YAML
([drivers/usbhid/quirks](drivers/usbhid/quirks)). To fix or extend them for
your model without rebuilding, put a file in `quirks.d/` next to your
`currently.yaml`.

## Monitoring tools

Currently speaks four protocols. All listen on this machine only until you
change the addresses under `listen:` (for example `nut: ":3493"`). See
[Monitoring tools](docs/outputs/index.md).

**NUT protocol** (port 3493): anything that works with NUT's upsd works
with Currently: `upsc`, `upsmon`, Home Assistant's NUT integration, Synology
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
    password_env: CURRENTLY_UPSMON_PASSWORD
    role: primary               # secondary: watch only; primary: may set FSD
  admin:
    password: change-me
    role: admin                 # primary, plus upscmd and upsrw
```

**SNMP** (UPS-MIB, RFC 1628): LibreNMS, Zabbix, PRTG, Observium and other
network monitors poll Currently like a UPS network card. v1, v2c and v3 (SHA-2
authentication, AES privacy), with v2c traps on power events. Tested
against net-snmp's tools and snmptrapd
([test/integration/snmp.sh](test/integration/snmp.sh)).

```yaml
listen:
  snmp:
    addr: ":1161"
    community: my-community      # extra UPSes: my-community@<ups>, or the v3 context
    users:
      monitor: {auth: sha256, auth_password_env: CURRENTLY_SNMP_AUTH, privacy: aes, privacy_password_env: CURRENTLY_SNMP_PRIV}
    traps: [{target: librenms.example.com}]
```

```sh
snmpwalk -v2c -c my-community localhost:1161 1.3.6.1.2.1.33
```

**Prometheus**: metrics are at `http://127.0.0.1:9300/metrics`, for
example `currently_battery_charge_percent`, `currently_battery_runtime_seconds`,
`currently_ups_status{flag="OB"}` and `currently_ups_up`.

**REST API**: read-only over the network unless you set a token:

```sh
curl http://127.0.0.1:9300/api/v1/ups
curl http://127.0.0.1:9300/api/v1/ups/cyberpower/vars/battery.charge
curl -N http://127.0.0.1:9300/api/v1/events          # Server-Sent Events
```

The full API is described at `/api/v1/openapi.yaml`.

## Configuration

To configure Currently, run `currently init` or create `currently.yaml` yourself
(`/etc/currently/currently.yaml` on Linux, `C:\ProgramData\currently\currently.yaml` on
Windows, `/usr/local/etc/currently/currently.yaml` on macOS and FreeBSD, or pass
`--config`). See [currently.example.yaml](currently.example.yaml).

```sh
currently config validate   # check it without starting anything
currently config show       # the effective config, with defaults
```

Mistakes are reported with the file, line and a suggestion:

```
currently.yaml:4: UPS rack (usbhid driver): unknown option "serail" (did you mean "serial"?)
```

## Layout

| Path | What |
|---|---|
| `cmd/currently` | The CLI and daemon entry point |
| `pkg/sdk` | Contracts for drivers and outputs (built-in and plugins) |
| `proto/currently/v1` | The same contracts as gRPC services, for external plugins |
| `internal/core` | State store, event bus, driver supervision |
| `internal/config` | Loading and validating `currently.yaml` |
| `drivers/usbhid` | USB HID UPS driver: transports, mapping, capture/replay |
| `drivers/usbhid/hid` | HID report descriptor parser |
| `drivers/usbhid/quirks` | Field-to-variable mappings per vendor |
| `drivers/simulator` | Simulated UPS |
| `internal/policy` | Shutdown policy: when and how to shut down |
| `internal/plugins` | Plugin loader and supervisor |
| `internal/scaffold` | `currently plugin new` templates and the Python SDK |
| `pkg/plugin` | Go SDK for plugins |
| `plugins/mqtt` | MQTT output plugin |
| `plugins/snmp` | SNMP driver plugin (UPS-MIB, APC PowerNet-MIB) |
| `plugins/serial` | Serial driver plugin (Megatec/Q1, APC Smart) |
| `drivers/execdriver` | `exec` driver: any program that prints UPS variables |
| `internal/nutimport` | `currently import-nut` |
| `drivers/nutclient` | `nut` driver: a UPS on another Currently or NUT upsd |
| `outputs/rest` | REST/JSON API and event stream |
| `outputs/nutproto` | NUT protocol server (upsd compatible) |
| `outputs/prometheus` | Prometheus metrics |
| `outputs/snmp` | SNMP agent (UPS-MIB; v1, v2c, v3) |
| `packaging` | Service files, udev rule, MSI, FreeBSD package, container |
| `docs` | Documentation site (`mkdocs serve`) |
| `internal/svcmgr` | Service install and control |

## License

Currently is licensed under the [Apache License, Version 2.0](LICENSE).

## Development

```sh
go test ./...
go vet ./...
CURRENTLY_HW=1 go test ./drivers/usbhid -run TestHardware -v   # with a UPS attached
go build -o currently ./cmd/currently && test/integration/nut-clients.sh ./currently   # Linux
test/integration/shutdown.sh ./currently   # Linux; shuts nothing down (marker files only)
test/integration/snmp.sh ./currently       # Linux; needs the snmp and snmptrapd packages
goreleaser release --snapshot --clean --skip=docker,sign,sbom   # every archive and package
CURRENTLY_TEST_PYTHON=python3 go test ./internal/scaffold   # Python plugin SDK (needs grpcio)
scripts/gen.sh                         # after changing proto/
```
