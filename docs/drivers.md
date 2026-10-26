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
