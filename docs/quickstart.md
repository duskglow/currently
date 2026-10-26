# Quick start

Plug in the UPS's USB cable, then:

```sh
nutng detect    # list the UPSes nutng can see
sudo nutng init # pick one, name it, choose when to shut down, write nutng.yaml
```

Run it in the foreground to try it, or as a service:

```sh
nutng serve                    # foreground; Ctrl-C to stop
sudo nutng service install     # at boot (an Administrator terminal on Windows)
sudo nutng service start       # also: stop, restart, status, uninstall
```

Packages install the service for you; then `nutng init` and
`nutng service start` are all you need.

Without a config file, `nutng serve` monitors every USB UPS it finds and
serves the REST API, Prometheus metrics and the NUT protocol on this
machine only. It never shuts anything down until you add a
[`shutdown`](shutdown.md) section.

## Looking at the UPS

```sh
nutng status                 # summary, or detail when there is one UPS
nutng watch                  # live view with recent events
nutng vars rack              # every variable the UPS reports
nutng cmd rack               # the commands it accepts
nutng cmd rack beeper.disable
nutng set rack battery.charge.low 20
nutng doctor                 # check the whole setup, with fixes for problems
```

No UPS at hand? `nutng serve --demo` adds a simulated one whose mains
power fails for a minute every three minutes, and
`nutng cmd demo sim.power.fail` rehearses an outage.

## Letting monitoring tools in

Everything listens on `127.0.0.1` until you say otherwise. To let other
machines query nutng, change the addresses under `listen:`:

```yaml
listen:
  http: ":9300"          # REST API and Prometheus /metrics
  nut: ":3493"           # upsc, upsmon, Home Assistant, NAS
  snmp:                  # LibreNMS, Zabbix, PRTG, ...
    addr: ":1161"
    community: my-community
```

Then `sudo nutng service restart`. See [Monitoring tools](outputs/index.md).
