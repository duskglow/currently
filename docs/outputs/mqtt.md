# MQTT

The MQTT output plugin (`nutng-output-mqtt`, included in the packages)
publishes each UPS's state and events to a broker, with Home Assistant
discovery.

```yaml
outputs:
  mqtt:
    broker: tcp://192.168.1.5:1883     # or ssl://...:8883
    username: nutng
    password_env: NUTNG_MQTT_PASSWORD
    # topic_prefix: nutng
    # home_assistant: true             # publish discovery messages
    # discovery_prefix: homeassistant
    # qos: 1
    # min_interval: 1s                 # at most one state message per UPS this often
    allow_control: false               # true: accept commands over MQTT
```

| Topic | Payload |
|---|---|
| `nutng/status` | `online` / `offline` for the plugin (retained, last will) |
| `nutng/<ups>/available` | `online` / `offline` for the UPS's data (retained) |
| `nutng/<ups>/state` | JSON: status, state and every variable (retained) |
| `nutng/<ups>/event` | JSON: each notable event |
| `nutng/<ups>/command/<command>` | publish here to run a command (with `allow_control`); the payload is its parameter |
| `nutng/<ups>/command/result` | JSON: the outcome of each command |

With Home Assistant's MQTT integration, each UPS appears as a device with
sensors for its status, charge, runtime, load and voltages.
