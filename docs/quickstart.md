# Quick start

Plug in the UPS's USB cable, then:

```sh
currently detect    # list the UPSes currently can see
sudo currently init # pick one, name it, choose when to shut down, write currently.yaml
```

Run it in the foreground to try it, or as a service:

```sh
currently serve                    # foreground; Ctrl-C to stop
sudo currently service install     # at boot (an Administrator terminal on Windows)
sudo currently service start       # also: stop, restart, status, uninstall
```

Packages install the service for you; then `currently init` and
`currently service start` are all you need.

Without a config file, `currently serve` monitors every USB UPS it finds and
serves the REST API, Prometheus metrics and the NUT protocol on this
machine only. It never shuts anything down until you add a
[`shutdown`](shutdown.md) section.

## Looking at the UPS

```sh
currently status                 # summary, or detail when there is one UPS
currently watch                  # live view with recent events
currently vars rack              # every variable the UPS reports
currently cmd rack               # the commands it accepts
currently cmd rack beeper.disable
currently set rack battery.charge.low 20
currently doctor                 # check the whole setup, with fixes for problems
```

No UPS at hand? `currently serve --demo` adds a simulated one whose mains
power fails for a minute every three minutes, and
`currently cmd demo sim.power.fail` rehearses an outage.

## Letting monitoring tools in

Everything listens on `127.0.0.1` until you say otherwise. To let other
machines query Currently, change the addresses under `listen:`:

```yaml
listen:
  http: ":9300"          # REST API and Prometheus /metrics
  nut: ":3493"           # upsc, upsmon, Home Assistant, NAS
  snmp:                  # LibreNMS, Zabbix, PRTG, ...
    addr: ":1161"
    community: my-community
```

Then `sudo currently service restart`. See [Monitoring tools](outputs/index.md).
