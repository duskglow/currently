# CLAUDE.md

Guidance for Claude (and other AI assistants) working on Currently, a UPS
monitor and shutdown daemon written in Go. The [README](README.md) explains
what it does and has a table of where the code lives; the
[design plan](docs/PLAN.md) explains why it is built the way it is.

Currently is **alpha**. It has been tested on one model of UPS (CyberPower
CP1500PFCLCD, on Windows and Linux). Most requests will be about making it
work with someone else's UPS; the second half of this file is about that.

## Safety first

Currently shuts computers down and can tell UPSes to cut power. When
building or testing:

- **Never shut anything down.** Tests use the fake executor. For manual or
  integration runs, set `CURRENTLY_NEVER_SHUTDOWN=1`, point
  `shutdown.command` and hooks at harmless commands (writing a marker
  file), use `shutdown.dry_run: true`, and use the `simulator` or `replay`
  driver for outages.
- **Never send power commands** (`load.off`, `load.off.delay`,
  `shutdown.return`, `shutdown.stayoff`, `test.battery.*`, APC `Z`/`K`/`S`,
  SNMP control OIDs) to a real UPS unless its owner says that unit is a
  spare they are willing to switch off. Reading a UPS is always safe.
- **Don't commit serial numbers.** Captures replace them with `REDACTED`;
  check anything pasted by a user before adding it to `testdata/`.
- **Don't copy code from NUT** (Network UPS Tools is GPL). Facts such as
  OIDs, protocol commands and HID usages are fine; check them against
  NUT's protocol docs or the vendor's MIB rather than memory.

## Working on the code

```sh
go build ./...
go vet ./...
go test ./...                       # everything; Windows, Linux, macOS
golangci-lint run ./...             # CI runs it on every OS (GOOS=linux etc.)
go test -run X -fuzz . -fuzztime 30s ./drivers/usbhid/hid   # after parser changes
scripts/gen.sh                      # after changing proto/ (needs grpcio-tools)
test/integration/nut-clients.sh ./currently   # Linux: real NUT clients
test/integration/snmp.sh ./currently          # Linux: net-snmp tools
goreleaser release --snapshot --clean --skip=docker,sign,sbom   # all packages
```

Conventions:

- The product is **Currently** in prose, messages and docs; anything typed
  is lowercase: the `currently` command, `currently.yaml`, the service,
  packages, `currently-driver-*`/`currently-output-*` plugins,
  `CURRENTLY_*` environment variables. Go error strings start lowercase.
- No cgo, on any platform (macOS uses IOKit through purego).
- Every error a user can see says how to fix it; config errors carry
  `file:line` and "did you mean" hints. Match the style of the surrounding
  code and its comments.
- New behaviour comes with tests; bugs found on real hardware get a
  regression test built from that hardware's data (see below).

## Adding or fixing support for a USB UPS

USB UPSes follow the HID Power Device class. Currently reads the UPS's HID
report descriptor, decodes each report into fields named by their usage
path (such as `UPS.PowerSummary.RemainingCapacity`), and maps fields to
NUT variables (`battery.charge`) with YAML quirk files.

### 1. Get a capture

Ask the user to run, with the UPS attached:

```sh
currently detect                                  # lists UPSes and their serials
currently debug capture --samples 3               # or --serial X / --id vvvv:pppp
```

It writes `currently-capture-<vendor>-<product>-<time>.json` with:

| Key | What it tells you |
|---|---|
| `device` | USB IDs, names, USB port (serial redacted) |
| `report_descriptor` | the raw descriptor, hex (not on Windows) |
| `fields` | every field the parser found: path, report type (1 input, 3 feature), report ID, bit offset and size, logical range, unit and exponent |
| `samples` | raw field values from each read |
| `raw_reports` | the bytes of every report, hex; byte 0 is the report ID (not on Windows) |
| `report_errors` | reports that could not be read, with the error |
| `unmapped` | fields no quirk rule uses |

A capture from Linux, macOS or FreeBSD is far more useful than one from
Windows, which hides the descriptor and raw bytes. Also ask what the UPS's
own display (or its maker's software) shows, and whether it is on mains or
battery, so you know the true values.

### 2. Work out which layer is wrong

Decode the important reports by hand from `raw_reports` (little-endian;
after the ID byte, fields sit at the bit offsets the descriptor gives).
Then compare:

- **Bytes are there, but the field is missing from `fields`:** a
  descriptor-parser problem in `drivers/usbhid/hid/descriptor.go`.
  Example: CyberPower declares read-only measurements as *Constant* items
  with usages, which the parser once discarded as padding. Fuzz after any
  parser change.
- **The field is in `fields` and `samples`, but no variable appears:** no
  quirk maps that path. Add it (step 4). `unmapped` lists candidates.
- **A variable has the wrong value:** units or scale. Compare the field's
  `unit`, `unit_exponent` and logical/physical ranges with the true value;
  fix with `scale`, `decimals`, `convert` or, for odd firmware, a fixup.
- **Reports in `report_errors`:** a transport problem
  (`drivers/usbhid/transport_*.go`, `rawdevice.go`): buffer sizes, report
  IDs, or the OS refusing access.
- **No UPS found, or "was not passed into this container":** a device
  access problem, not a decoding one; see `docs/platforms.md`
  (Containers, Linux udev rule).

### 3. Turn the capture into a test

Save the descriptor and report bytes as
`drivers/usbhid/testdata/<vendor>-<model>-<os>-reports.json`, in the format
of `cyberpower-cp1500pfclcd-linux-reports.json`, with a `comment` saying
where it came from. Write a test like `TestCyberPowerLinux` in
`drivers/usbhid/usbhid_test.go` that decodes it and checks the variables
against the true values. Make sure it fails before your fix and passes
after.

### 4. Map the fields

Mappings live in `drivers/usbhid/quirks/`:

- `00-generic.yaml` holds the standard layout for every UPS (its header
  explains the format: `vars` with candidate `paths`, `flags` for
  `ups.status`, `commands`).
- A vendor file (`cyberpower.yaml`, `apc.yaml`, `eaton.yaml`) matches on
  USB vendor IDs and overrides or adds rules. Create one for a new vendor.
- `fixups` names Go functions in `drivers/usbhid/mapping.go` for firmware
  quirks a YAML rule can't express (for example `cps_battery_voltage`).
- Add a model to a file's `verified:` list only once someone has run it on
  real hardware.

Users don't have to wait for a release: the same YAML, saved in
`quirks.d/` next to their `currently.yaml`, applies without rebuilding.
Offer that as the immediate fix.

### 5. Update the docs

When a user confirms a UPS works on real hardware, add it to the
**Supported UPSes** table in the README (model, USB ID, connection, OS),
and to `verified:` in its quirk file. Don't list models that were only
tested against captures or simulators.

## Network-card (SNMP) and serial UPSes

These are plugins, built and shipped with Currently:

- **SNMP** (`plugins/snmp`): `mibs.go` maps a MIB's OIDs to variables,
  status flags and command SETs. A new card family is a new `mib` entry
  plus detection in `pickMIB` (by `sysObjectID`). Check OIDs against the
  vendor's MIB file. Test with the fake agent in `driver_test.go`; ask the
  user for `snmpwalk -v2c -c <community> <host> .1.3.6.1` output to build
  its table.
- **Serial** (`plugins/serial`): a protocol implements the `protocol`
  interface in `driver.go` (`megatec.go`, `apcsmart.go` are the examples).
  Test against a simulated UPS (`driver_test.go`) and over a real serial
  pair: `TestRealPort` with `socat pty,raw,echo=0,link=/tmp/ttyUPS
  pty,raw,echo=0,link=/tmp/ttyFAKE`.
- Other one-offs: the `exec` driver turns any program that prints
  `name: value` lines into a UPS, which may be all a user needs.
