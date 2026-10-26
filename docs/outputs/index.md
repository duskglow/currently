# Monitoring tools

Currently serves the same UPS state in several ways at once. Everything
listens on this machine only until you change the addresses under
`listen:`.

| Protocol | Default | For |
|---|---|---|
| [NUT protocol](nut.md) | `127.0.0.1:3493` | upsc, upsmon, Home Assistant, Synology/QNAP, Nagios `check_ups` |
| [SNMP](snmp.md) | off (`:1161` when on) | LibreNMS, Zabbix, PRTG, Observium, Cacti, any UPS-MIB poller |
| [Prometheus](prometheus.md) | `127.0.0.1:9300/metrics` | Prometheus, Grafana, VictoriaMetrics |
| [REST API](rest.md) | `127.0.0.1:9300/api/v1` | scripts, dashboards, the Currently CLI |
| [MQTT](mqtt.md) | off (plugin) | Home Assistant, Node-RED |

All of them only read, unless you allow more: NUT users with the `admin`
role, a REST API token, or `allow_control` on an output plugin. The SNMP
agent is always read-only.
