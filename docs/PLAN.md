# nut-ng — an easy, pluggable UPS monitoring service

## Context

NUT (Network UPS Tools) does the job, but setting it up is painful. You edit five config files (`ups.conf`, `upsd.conf`, `upsd.users`, `upsmon.conf`, `nut.conf`). You run several daemons (driver, `upsd`, `upsmon`) that have to start in the right order. USB permissions break quietly. And when something fails, you're usually left with "driver failed to start" and nothing more.

**Goal:** a single, self-contained Go binary (`nutng`) that:
- finds the UPS by itself
- works with no config, or with one short YAML file
- installs itself as a service on Linux, Windows, macOS and FreeBSD
- tells you clearly what's wrong

Network monitoring tools can query it over the NUT protocol, Prometheus, REST/JSON and SNMP. Input drivers and output methods are **plugins**, so new ones can be added without rebuilding the core.

The repo (`nut-ng/`) is empty, so this is a greenfield design.

## Decisions (from the clarifying questions)

- **Language:** Go. One static binary, `CGO_ENABLED=0` everywhere except macOS (IOKit needs cgo).
- **Platforms in v1:** Linux (amd64/arm64/armv7), Windows, macOS, FreeBSD.
- **Built-in input in v1:** USB HID Power Device. All other inputs are plugins.
- **Built-in outputs in v1:** NUT protocol (3493), Prometheus `/metrics`, REST/JSON, SNMP agent (UPS-MIB). All are written against the same output-plugin interface.
- **Inputs and outputs can both be plugged in** without rebuilding.

## Architecture

```
            ┌──────────── nutng daemon (one process) ─────────────┐
 USB UPS ──▶│ Driver (built-in usbhid)  ─┐                         │
 SNMP UPS ─▶│ Driver plugin (gRPC) ──────┼─▶ State store ─▶ Event bus ─┬─▶ NUT proto :3493
 script  ──▶│ exec driver (JSON/stdio) ──┘   (per-UPS vars,   │       ├─▶ Prometheus :9300/metrics
            │                                 history ring)  │       ├─▶ REST/JSON :9300/api/v1
            │        Plugin supervisor (restart+backoff,     │       ├─▶ SNMP agent :161/1161
            │        health)                                 │       └─▶ Output plugins (gRPC): MQTT, webhook…
            │                        Shutdown policy engine ◀┘                                   │
            └────────────────────────────────────────────────────────────────────────────────┘
   CLI (`nutng status`, `nutng cmd`…) ──▶ local control API (Unix socket / Windows named pipe)
```

### Core concepts
- **Canonical data model:** use NUT's variable names (`ups.status`, `battery.charge`, `battery.runtime`, `input.voltage`, `ups.load`…) and NUT's status flags (`OL OB LB CHRG RB FSD…`). Existing NUT knowledge, dashboards and clients carry over, and the NUT-protocol output becomes a thin mapping. Each value is stored with a type, unit and timestamp.
- **State store:** keeps the latest value of every variable for each UPS, a short history buffer for `nutng watch` and charts, and staleness tracking (a UPS that stops reporting gets marked `STALE` and an alert is raised).
- **Event bus:** typed events (`OnBattery`, `OnLine`, `LowBattery`, `CommLost`, `ReplaceBattery`, `ShutdownStarted`…) that outputs, notifiers and the policy engine subscribe to.

### Plugin system (the key requirement)
- **Protocol:** gRPC over a local connection, using `hashicorp/go-plugin` (handshake, version negotiation, stdio log capture). Go's native `plugin` package is out: it doesn't support Windows and it's brittle.
- **Contracts** live in `proto/nutng/v1/`:
  - `driver.proto`: `Describe` (name, version, capabilities, config schema), `Discover` (list candidate devices), `Open(config)`, `Watch` (server-stream of variable updates), `GetVars`, `SetVar`, `ListCommands`, `RunCommand` (for example `test.battery.start`, `beeper.disable`, `load.off`), `Close`.
  - `output.proto`: `Describe`, `Configure`, `Start`. The plugin receives a stream of state snapshots and events, and gets a **callback client** to the core so it can query state or issue commands, subject to an ACL.
- **Built-ins use the same interfaces in-process.** `usbhid`, `nutproto`, `prometheus`, `rest` and `snmp` implement the Go interfaces in `pkg/sdk` directly. Any of them can also be compiled as a standalone plugin, so built-in and external are treated the same.
- **Exec driver:** for very simple integrations, any executable that prints JSON lines (`{"battery.charge":87,"ups.status":"OL"}`) to stdout becomes a driver. This works for any language with no gRPC needed.
- **Discovery and lifecycle:**
  - Executables named `nutng-driver-*` or `nutng-output-*` are found in `<libdir>/nutng/plugins` and `<confdir>/plugins.d`.
  - The supervisor restarts crashed plugins with exponential backoff and reports their health in `nutng status` and `nutng doctor`.
  - Each plugin declares a JSON-Schema for its config block, so `nutng config validate` checks plugin config too.
- **SDK:** `pkg/sdk` (Go helpers: `sdk.ServeDriver(impl)`, `sdk.ServeOutput(impl)`) plus a `nutng plugin new driver|output <name>` scaffold. An example plugin in Python (via grpcio) proves other languages work.
- **Data-driven USB quirks:** vendor-specific HID mappings (APC, CyberPower, Eaton, Tripp Lite…) live in YAML files under `quirks/`. They are embedded by default and can be overridden from `<confdir>/quirks.d`, so support for a new UPS model is often just a YAML file.

### USB HID driver (built-in)
- **HID report-descriptor parser** plus the Power Device (0x84) and Battery System (0x85) usage tables. These map to canonical variables through the quirk tables.
- **Per-OS transport, pure Go where possible:**
  - Linux: `/dev/hidraw*` with ioctls
  - Windows: `hid.dll`/`setupapi` through `x/sys/windows`, so **no libusb or Zadig driver swap is needed**
  - FreeBSD: `hidraw`/`uhid`
  - macOS: IOKit (cgo)
- Hot-plug aware: reconnects automatically when the UPS is unplugged and plugged back in.
- **Record/replay:** `nutng debug capture` dumps the descriptor and report traffic to a file. The `replay` driver plays it back. This drives unit tests and lets users attach a capture to bug reports.

### Outputs (built-in)
- **NUT protocol server:** `LIST UPS/VAR/CMD/RW`, `GET VAR`, `INSTCMD`, `SET VAR`, `LOGIN`/`PRIMARY` (and `MASTER`), `FSD`, `STARTTLS`, with users and ACLs. Existing `upsmon`, `upsc`, Home Assistant, Synology DSM and Nagios `check_ups` keep working.
- **Prometheus:** `nutng_ups_*` gauges with `ups` and `model` labels, a status-flag gauge, and an `up` metric for each UPS.
- **REST/JSON** at `/api/v1`: `GET /ups`, `GET /ups/{name}`, `GET /ups/{name}/vars`, `POST /ups/{name}/commands/{cmd}`, and `GET /events` (Server-Sent Events stream). Includes an OpenAPI spec. Mutating calls need a bearer token.
- **SNMP agent:** serves RFC 1628 UPS-MIB (v2c, plus v3 auth), so LibreNMS, PRTG and Zabbix can poll it like a hardware network card. Defaults to port 1161 unless it runs with privileges or with `CAP_NET_BIND_SERVICE`.
- **Notifiers:** events trigger shell, webhook and email actions, handled as output plugins. MQTT, with Home Assistant auto-discovery, is the first external output plugin.

### Shutdown orchestration (the job `upsmon` does today)
- **Policy:** shut down when *on battery longer than X* OR *runtime below Y* OR *charge below Z%* OR the `LB` flag is set. Policies can be set per UPS, and a host can depend on several UPSes (with a "minimum supplies" rule).
- **Primary and secondary hosts:**
  - Secondaries run `nutng` in `client` mode and connect over the NUT protocol, so they can also follow a classic NUT `upsd`.
  - Classic `upsmon` secondaries can follow a nutng primary.
  - The primary raises FSD, waits for secondaries to disconnect or for a timeout, then runs its own shutdown and optionally a UPS `load.off` with a delay.
- **OS shutdown:** the right command for each OS (`systemctl poweroff`, `shutdown /s`, `shutdown -h`), with pre-shutdown hooks.
- **Safe testing:** `nutng test shutdown --dry-run` walks through the full sequence and prints every step, so you can check it without cutting power.

## Configuration UX

- **Zero-config:** with no config file, `nutng serve` finds every USB UPS, names it after its model, serves the REST API and Prometheus on localhost only, and logs exactly how to go further.
- **`nutng init`:** runs detection and asks a few short questions:
  - which UPS to use and what to call it
  - whether to listen on the network for monitoring tools
  - when to shut down
  - whether this host is the primary or a secondary

  It then writes **one** file, `/etc/nutng/nutng.yaml` (or `%ProgramData%\nutng\nutng.yaml`), and offers to install and start the service.
- **Example config:**
  ```yaml
  ups:
    rack:
      driver: usbhid          # optional; auto by default
      match: { vendor: APC, serial: 3B1234X }
  listen:
    nut: ":3493"
    http: ":9300"            # REST + /metrics
    snmp: { addr: ":1161", community: public }
  shutdown:
    when: { on_battery_for: 2m, runtime_below: 5m }
  users:
    monitor: { password_env: NUTNG_MONITOR_PW, role: readonly }
  ```
- **Validation and editing:**
  - Strict validation with file:line errors and "did you mean…" suggestions.
  - A published JSON Schema gives editor autocomplete.
  - Config changes hot-reload on SIGHUP or a file change, with no restart.
- **`nutng doctor`:** checks USB visibility and permissions (and the udev rule), port conflicts with a running NUT, service state, plugin health and clock skew. Every failure comes with the exact command to fix it.

## CLI (single binary, cobra)

| Command | Purpose |
|---|---|
| `nutng serve` | Run the daemon (foreground). Systemd, the Windows SCM and launchd call this. |
| `nutng init` / `detect` | Guided setup / list the UPSes it can see. |
| `nutng status [ups] [--json]` / `watch` | Pretty snapshot / live view. |
| `nutng vars`, `cmd <ups> <command>`, `set <ups> <var> <val>` | Inspect variables and control the UPS. |
| `nutng service install\|uninstall\|start\|stop\|status` | Cross-platform service management (`kardianos/service`). |
| `nutng config validate\|show\|schema` | Config tooling. |
| `nutng plugin list\|info\|new` | Plugin tooling. |
| `nutng doctor`, `nutng test shutdown --dry-run`, `nutng debug capture` | Diagnostics. |
| `nutng import-nut /etc/nut` | Convert an existing NUT config into `nutng.yaml`. |

The CLI talks to the daemon over a local control socket (a Unix socket, or a named pipe on Windows) with peer-credential auth.

## Repository layout

```
cmd/nutng/                 main + cobra commands
internal/core/             state store, event bus, supervisor
internal/config/           load, validate, schema, hot-reload, NUT import
internal/policy/           shutdown engine + OS shutdown adapters
internal/control/          local control API used by the CLI
pkg/sdk/                   public Go interfaces + plugin serve helpers
proto/nutng/v1/            driver.proto, output.proto (buf-managed)
drivers/usbhid/            HID parser, usage tables, per-OS transports
drivers/{exec,replay,simulator}/
outputs/{nutproto,prometheus,rest,snmp,notify}/
quirks/                    vendor YAML mappings (embedded)
plugins/examples/          Go + Python sample plugins
packaging/                 systemd unit, udev rules, rc.d, launchd plist, MSI (WiX)
```

## Modern engineering practices
- Go modules, `log/slog` structured logs (journald, Windows Event Log, or JSON), context-based cancellation, `buf` for protos.
- Tests:
  - table-driven unit tests
  - **fuzzing** for the HID descriptor parser and the NUT protocol parser
  - golden tests from recorded HID captures
  - an integration suite that runs the real `upsc` and `upsmon` (in a container) against `nutng`
- GitHub Actions matrix (linux/windows/macos/freebsd-vm), `golangci-lint`, `govulncheck`.
- **Releases with GoReleaser:** `.deb`, `.rpm`, `.apk`, Homebrew, winget/MSI, a FreeBSD pkg, a multi-arch Docker image and tarballs. Release artifacts are signed with cosign and ship with an SBOM. Each package installs the udev rule, a service user and the service unit, so installing the package is all you need.
- Semantic versioning, with the plugin protocol versioned separately (`v1`) so plugins keep working across core upgrades.

## Milestones
1. **Foundations:** repo, CI, `pkg/sdk` interfaces and protos, state store and event bus, the `simulator` driver, REST API and CLI `status`/`watch`, config loading and validation.
2. **USB HID:** descriptor parser, quirks for APC/CyberPower/Eaton, Linux and Windows transports, `detect`/`init`, capture and replay.
3. **Monitoring outputs:** NUT protocol server (tested against the real `upsc`/`upsmon`), Prometheus, `service install`, `doctor`.
4. **Shutdown:** policy engine, primary/secondary roles and FSD, client mode, dry-run test, `import-nut`.
5. **External plugins:** go-plugin loader and supervisor, exec driver, `plugin new` scaffold, Python example, MQTT output plugin.
6. **Breadth:** SNMP agent output, macOS and FreeBSD HID transports, full packaging matrix, docs site.
7. **More inputs as plugins (this also tests the plugin architecture):**
   - SNMP-card driver (UPS-MIB, APC PowerNet MIB)
   - serial drivers (APC smart, Megatec/Q1)
   - an "upstream NUT" driver for gradual migration

## Verification
- `go test ./...` (unit, golden and fuzz corpora) and `golangci-lint run` stay green in the CI matrix.
- **Simulator end-to-end:**
  1. `nutng serve` with the simulator driver.
  2. `nutng status` and `curl :9300/api/v1/ups` show its values.
  3. `curl :9300/metrics` parses with `promtool check metrics`.
  4. `upsc sim@localhost` (real NUT client) lists the variables.
  5. `snmpwalk -v2c -c public localhost:1161 1.3.6.1.2.1.33` returns UPS-MIB values.
- **Shutdown:** the simulator scripts a power-fail scenario, `nutng test shutdown --dry-run` prints the expected sequence, and a secondary host in client mode gets FSD.
- **Plugins:** build the example Go and Python plugins, drop them into `plugins.d`, and check that `nutng plugin list` shows them. Kill one and confirm the supervisor restarts it, then reload config with no core rebuild.
- **Real hardware:** plug in a USB UPS on Linux and Windows. `nutng init` then `nutng service install` should give a working service with no extra packages. Pulling AC power should show `OB` within a few seconds on every output.
