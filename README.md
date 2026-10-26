# nut-ng

UPS monitoring that is easy to set up. One self-contained binary, one short
config file, clear error messages, and an API that monitoring tools can
query.

> **Status:** early development (milestone 1 of 7). The core, the plugin
> contracts, a simulated UPS, the REST API and the CLI work. Real USB UPS
> support, the NUT protocol, Prometheus, SNMP and shutdown handling are
> next. See [docs/PLAN.md](docs/PLAN.md).

## Try it

Build it (Go 1.26 or newer):

```sh
go build -o bin/nutng ./cmd/nutng
```

Start the daemon with a simulated UPS whose mains power fails for one minute
every three minutes:

```sh
nutng serve --demo
```

In another terminal:

```sh
nutng status                    # summary of every UPS (or detail if there is one)
nutng watch                     # live view with recent events
nutng vars demo                 # every variable the UPS reports
nutng cmd demo                  # list the commands it accepts
nutng cmd demo sim.power.fail   # rehearse a power failure
nutng set demo ups.load 80
```

Monitoring tools can use the REST API, which is read-only over the network
unless you set a token:

```sh
curl http://127.0.0.1:9300/api/v1/ups
curl http://127.0.0.1:9300/api/v1/ups/demo/vars/battery.charge
curl -N http://127.0.0.1:9300/api/v1/events          # Server-Sent Events
```

The full API is described at `/api/v1/openapi.yaml`.

## Configuration

nutng runs without a config file. To configure it, create `nutng.yaml`
(`/etc/nutng/nutng.yaml` on Linux, `C:\ProgramData\nutng\nutng.yaml` on
Windows, `/usr/local/etc/nutng/nutng.yaml` on macOS and FreeBSD, or pass
`--config`). See [nutng.example.yaml](nutng.example.yaml).

```sh
nutng config validate   # check it without starting anything
nutng config show       # the effective config, with defaults
```

Mistakes are reported with the file, line and a suggestion:

```
nutng.yaml:4: UPS rack (simulator driver): unknown option "scenaro" (did you mean "scenario"?)
```

## Layout

| Path | What |
|---|---|
| `cmd/nutng` | The CLI and daemon entry point |
| `pkg/sdk` | Contracts for drivers and outputs (built-in and plugins) |
| `proto/nutng/v1` | The same contracts as gRPC services, for external plugins |
| `internal/core` | State store, event bus, driver supervision |
| `internal/config` | Loading and validating `nutng.yaml` |
| `drivers/simulator` | Simulated UPS |
| `outputs/rest` | REST/JSON API and event stream |

## Development

```sh
go test ./...
go vet ./...
```
