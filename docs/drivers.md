# UPS drivers

A driver reads one UPS. Pick it with `driver:` in each `ups:` entry; the
other keys in the entry are the driver's options. `currently plugin list`
shows driver plugins as well.

## `usbhid` (default)

USB UPSes that follow the HID Power Device class: most APC, CyberPower,
Eaton, Tripp Lite and similar units. Currently uses the operating system's own
HID driver, so nothing needs installing or replacing; see
[Platforms](platforms.md).

| Option | Meaning |
|---|---|
| `serial` | Choose the UPS by USB serial number |
| `id` | Or by `vendor:product`, such as `0764:0601` |
| `vendor`, `product` | Or by name |
| `path` | Or by OS device path (`/dev/hidraw0`) |
| `interval` | How often to read it (default `2s`) |
| `quirks_dir` | Extra mappings (default `quirks.d/` next to `currently.yaml`) |

With one UPS, none are needed. `currently detect` prints the options that
select each UPS it finds.

**Mappings.** Which HID field becomes which NUT variable is described in
YAML files ([drivers/usbhid/quirks](https://github.com/duskglow/currently/tree/main/drivers/usbhid/quirks)).
To fix or extend them for your model without rebuilding, put a file in
`quirks.d/`.

**Captures.** When a UPS shows wrong or missing values, record it:

```sh
currently debug capture     # writes currently-capture-<vendor>-<product>-<time>.json
```

The file lists every field the UPS reports, with the serial number removed.
Attach it to a bug report, or play it back with the `replay` driver:

```yaml
ups:
  lab:
    driver: replay
    file: currently-capture-0764-0601-20261025-235000.json
```

## `nut`

A UPS served by another Currently or by NUT's upsd. This is how a
[secondary](shutdown.md#primary-and-secondaries) follows its primary.

| Option | Meaning |
|---|---|
| `host` | Server, `host` or `host:port` (port 3493 by default) |
| `ups` | UPS name on that server |
| `username`, `password`, `password_env` | Log in (needed to be counted as a secondary) |
| `tls`, `tls_insecure` | Use STARTTLS; skip certificate checks |
| `interval`, `timeout` | Polling |

## `snmp` (plugin)

UPSes with a network management card, over SNMP v1, v2c or v3. The driver
reads the standard UPS-MIB (RFC 1628), which most cards serve, or APC's
PowerNet-MIB for APC cards, which has more detail. It ships as the
`currently-driver-snmp` plugin, included in the packages.

```yaml
ups:
  rack:
    driver: snmp
    host: 192.168.1.20            # port 161 by default; or host:port
    community: public
    # write_community: private    # to send commands (tests, shutdown)
```

| Option | Meaning |
|---|---|
| `host` | The card's address, `host` or `host:port` |
| `version` | `1`, `2c` (default) or `3` |
| `community`, `community_env` | Read community (default `public`) |
| `write_community`, `write_community_env` | Enables commands with v1/v2c |
| `username`, `auth`, `auth_password[_env]`, `privacy`, `privacy_password[_env]`, `context` | SNMPv3; `auth` is `md5`, `sha`, `sha224` to `sha512`; `privacy` is `des`, `aes`, `aes192` or `aes256` (DES is accepted here because older cards support nothing else) |
| `mib` | `auto` (default: APC's MIB for APC cards, the UPS-MIB otherwise), `ietf` or `apc` |
| `interval`, `timeout`, `retries` | Polling (defaults `10s`, `3s`, 2) |

Commands need write access. With the UPS-MIB: battery tests,
`shutdown.return`, `shutdown.stayoff`, `shutdown.stop` and `load.off`
(through `upsShutdownAfterDelay` and `upsAutoRestart`). With APC's MIB:
battery and panel tests, calibration, `shutdown.return`,
`shutdown.stayoff`, `load.off` and `load.on`; APC cards accept
`shutdown.return` only while on battery.

The UPS-MIB alarm table becomes status flags (battery bad → `RB`, output
overload → `OVER`, shutdown pending → `FSD`, ...) and `ups.alarm`.

## `serial` (plugin)

UPSes on a serial port or USB serial adapter, with the **Megatec/Q1**
protocol (many budget and OEM UPSes: Mustek, Powercom, Fenton, Atlantis,
Ablerex, ...) or the **APC Smart** protocol (Smart-UPS and Back-UPS Pro
with APC's smart signalling cable, 940-0024). It ships as the
`currently-driver-serial` plugin, included in the packages.

```yaml
ups:
  closet:
    driver: serial
    port: /dev/ttyS0              # /dev/ttyUSB0, COM3, /dev/cuau0, ...
    protocol: auto                # megatec or apcsmart
```

| Option | Meaning |
|---|---|
| `port` | The serial device |
| `protocol` | `auto` (default: tries APC Smart, then Megatec), `megatec` or `apcsmart` |
| `baud` | Default 2400, which both protocols use |
| `interval`, `timeout` | Polling, and the wait for each reply (defaults `2s`, `1s`) |
| `battery_voltage_low`, `battery_voltage_high`, `cells` | Megatec: the battery voltages for 0% and 100% (the charge is estimated from the voltage, which is all these UPSes report), and the cell count when the UPS reports volts per cell without a rating |

**Megatec** UPSes report voltages, load, frequency, temperature and status
flags; the charge is an estimate. Commands: battery tests, beeper on, off
and toggle, `shutdown.return` and `shutdown.stayoff` (delays of 12 to 54
seconds in steps of 6, or 1 to 10 whole minutes) and `shutdown.stop`.

**APC Smart** UPSes report charge, runtime, voltages, load, temperature,
status and identity. Commands: battery and panel tests, `shutdown.return`
(the UPS waits its own grace delay, `ups.delay.shutdown`, so set
`shutdown.ups_off_delay` to match it), `shutdown.stayoff`, `shutdown.stop`
and `load.off`.

Serial UPSes are configured by port: Currently does not probe serial
ports by itself, since that could disturb other devices on them.

## `exec`

Any program that prints UPS variables, as JSON objects
(`{"ups.status": "OL", "battery.charge": 87}`) or as `name: value` lines
the way `upsc` does:

```yaml
ups:
  nas:
    driver: exec
    command: upsc ups@nas.local
    interval: 10s
    commands:                       # optional instant commands
      beeper.disable: /usr/local/bin/ups-beeper off
```

| Option | Meaning |
|---|---|
| `command` | A command line, or a list of arguments |
| `mode` | `poll` (run every interval; default) or `stream` (keeps running; one reading per JSON line or blank-line-separated block) |
| `interval`, `timeout` | Polling (defaults `5s`, `10s`) |
| `commands` | Instant commands; the parameter is in `CURRENTLY_PARAM` |

## `simulator`

A simulated UPS for trying Currently out and for rehearsals. It never shuts
the computer down.

| Option | Meaning |
|---|---|
| `scenario` | `online`, `onbattery` or `cycle` |
| `online_for`, `outage_for` | `cycle` timings |
| `speed` | Run the battery model faster than real time |
| `runtime`, `load` | Full-charge runtime at 50% load; load in percent |

## `replay`

Plays back a `currently debug capture` file, mapped exactly as the real UPS
would be. Like the simulator, it never shuts the computer down.
