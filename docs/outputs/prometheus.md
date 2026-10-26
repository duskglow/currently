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
  - job_name: nutng
    static_configs:
      - targets: ["nutng-host:9300"]
```

| Metric | Meaning |
|---|---|
| `nutng_ups_up{ups}` | 1 when nutng is receiving data from the UPS |
| `nutng_ups_info{ups,driver,model,serial,description}` | Identity; always 1 |
| `nutng_ups_status{ups,flag}` | 1 when the status has the flag (`OL`, `OB`, `LB`, `RB`, `CHRG`, `FSD`, ...) |
| `nutng_battery_charge_percent` | Battery charge |
| `nutng_battery_runtime_seconds` | Runtime left |
| `nutng_input_voltage_volts`, `nutng_output_voltage_volts` | Voltages |
| `nutng_ups_load_percent`, `nutng_ups_realpower_watts` | Load |
| `nutng_ups_driver_restarts_total` | Times the driver was reopened |
| `nutng_ups_last_update_timestamp_seconds` | Time of the last reading |
| `nutng_build_info{version}` | nutng version |

Every numeric NUT variable becomes `nutng_<name>_<unit>`. Values of a UPS
that stopped reporting are not exported, so they cannot look current.

Example alerts:

```yaml
- alert: UPSOnBattery
  expr: nutng_ups_status{flag="OB"} == 1
  for: 1m
- alert: UPSReplaceBattery
  expr: nutng_ups_status{flag="RB"} == 1
- alert: UPSDown
  expr: nutng_ups_up == 0
  for: 2m
```
