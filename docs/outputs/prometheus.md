# Prometheus

Metrics are served at `/metrics` on the REST API's address
(`http://127.0.0.1:9300/metrics`), or on their own address with
`listen.metrics`.

```yaml
listen:
  http: ":9300"
  # metrics: ":9301"      # a separate port, or "off"
```

```yaml
# prometheus.yml
scrape_configs:
  - job_name: currently
    static_configs:
      - targets: ["currently-host:9300"]
```

| Metric | Meaning |
|---|---|
| `currently_ups_up{ups}` | 1 when Currently is receiving data from the UPS |
| `currently_ups_info{ups,driver,model,serial,description}` | Identity; always 1 |
| `currently_ups_status{ups,flag}` | 1 when the status has the flag (`OL`, `OB`, `LB`, `RB`, `CHRG`, `FSD`, ...) |
| `currently_battery_charge_percent` | Battery charge |
| `currently_battery_runtime_seconds` | Runtime left |
| `currently_input_voltage_volts`, `currently_output_voltage_volts` | Voltages |
| `currently_ups_load_percent`, `currently_ups_realpower_watts` | Load |
| `currently_ups_driver_restarts_total` | Times the driver was reopened |
| `currently_ups_last_update_timestamp_seconds` | Time of the last reading |
| `currently_build_info{version}` | Currently version |

Every numeric NUT variable becomes `currently_<name>_<unit>`. Values of a UPS
that stopped reporting are not exported, so they cannot look current.

Example alerts:

```yaml
- alert: UPSOnBattery
  expr: currently_ups_status{flag="OB"} == 1
  for: 1m
- alert: UPSReplaceBattery
  expr: currently_ups_status{flag="RB"} == 1
- alert: UPSDown
  expr: currently_ups_up == 0
  for: 2m
```
