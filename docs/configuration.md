# Configuration reference

Currently reads one YAML file:

| Platform | Default path |
|---|---|
| Linux | `/etc/currently/currently.yaml` |
| Windows | `C:\ProgramData\currently\currently.yaml` |
| macOS, FreeBSD | `/usr/local/etc/currently/currently.yaml` |

`--config` or the `CURRENTLY_CONFIG` environment variable choose another
file. Every setting is optional; without a file Currently monitors every USB
UPS it finds. [currently.example.yaml](https://github.com/duskglow/currently/blob/main/currently.example.yaml)
shows them all with comments.

```sh
currently config validate   # check it, with file:line errors and "did you mean" hints
currently config show       # the effective config with defaults (secrets hidden)
```

Settings ending in `_env` name an environment variable that holds a
secret, so the file itself can be world-readable. The packaged services
read those variables from `/etc/default/currently` (Linux) or
`/usr/local/etc/currently/currently.env` (FreeBSD).

## `ups`

Each key is a UPS name (letters, digits, `.`, `_`, `-`), used by the CLI,
the NUT protocol (`upsc rack@host`) and the API.

| Setting | Default | Meaning |
|---|---|---|
| `driver` | `usbhid` | `usbhid`, `nut`, `exec`, `simulator`, `replay`, or a driver plugin such as `snmp` or `serial` |
| `description` | | Shown by `currently status` and `upsc -L` |
| `stale_after` | `30s` | Mark the UPS stale when it stays silent this long |
| *anything else* | | Passed to the driver; see [UPS drivers](drivers.md) |

## `listen`

| Setting | Default | Meaning |
|---|---|---|
| `http` | `127.0.0.1:9300` | REST API and `/metrics`, or `off` |
| `nut` | `127.0.0.1:3493` | NUT protocol, or `off` |
| `nut_tls.cert_file`, `nut_tls.key_file` | | Enable STARTTLS on the NUT protocol |
| `metrics` | (on `http`) | Serve `/metrics` on its own `host:port`, or `off` |
| `control` | per platform | The local socket the CLI uses |
| `snmp` | off | The [SNMP agent](outputs/snmp.md): an address, or the settings below |

`listen.snmp`:

| Setting | Default | Meaning |
|---|---|---|
| `addr` | `:1161` | UDP address |
| `community`, `community_env` | `public` when there are no v3 users | Read-only v1/v2c community |
| `ups` | the first UPS | The UPS served when a request names none |
| `users.<name>.auth` | | `none`, `md5`, `sha`, `sha224`, `sha256`, `sha384`, `sha512` |
| `users.<name>.auth_password[_env]` | | At least 8 characters |
| `users.<name>.privacy` | `none` | `none` or `aes` |
| `users.<name>.privacy_password[_env]` | | At least 8 characters |
| `engine_id` | from the host name | SNMPv3 engine ID, in hex |
| `traps[].target`, `traps[].community` | port 162, `public` | SNMPv2c trap receivers |
| `location`, `contact` | | sysLocation and sysContact |

## `users`

Accounts for the NUT protocol, as in NUT's `upsd.users`. Reading needs no
login.

| Setting | Meaning |
|---|---|
| `password`, `password_env` | One of them is required |
| `role` | `secondary` (upsmon that only watches), `primary` (may set FSD), `admin` (primary, plus `upscmd` and `upsrw`) |

## `api`

| Setting | Meaning |
|---|---|
| `token`, `token_env` | Bearer token for commands and changes over HTTP. Without one, the HTTP API is read-only. |

## `shutdown`

Without this section Currently never shuts the computer down. See
[Shutting down](shutdown.md).

| Setting | Default | Meaning |
|---|---|---|
| `ups` | all | UPSes powering this computer |
| `min_supplies` | `1` | How many must be healthy to keep running |
| `when.low_battery` | `true` | Follow the UPS's low-battery signal |
| `when.on_battery_for` | | Or after this long on battery |
| `when.runtime_below` | | Or when less runtime is left |
| `when.charge_below` | | Or below this charge (%) |
| `dead_time` | `15s` | How long a UPS on battery may stay silent before it counts as critical |
| `wait_for_secondaries` | `15s` | As primary: wait for NUT clients to log out after FSD |
| `final_delay` | `5s` | Pause before the shutdown command |
| `hooks`, `hook_timeout` | | Commands to run first, such as stopping VMs |
| `command` | the OS's | Replace the shutdown command |
| `power_off_ups`, `ups_off_delay` | `false`, `60s` | Cut UPS power after the delay and restore it when mains returns |
| `dry_run` | `false` | Log every step without doing any |

## `outputs` and `plugins`

```yaml
outputs:
  mqtt:                        # an output plugin, by name
    plugin: mqtt               # default: the entry's name
    allow_control: false       # let it run commands and set FSD
    broker: tcp://192.168.1.5:1883   # everything else goes to the plugin
plugins:
  dirs: [/opt/currently/plugins]   # searched before the defaults
  commands:                    # plugins that need an interpreter
    - ["python3", "/opt/currently/myups/plugin.py"]
```

See [Plugins](plugins.md).

## `log`

| Setting | Default | Meaning |
|---|---|---|
| `level` | `info` | `debug`, `info`, `warn`, `error` |
| `format` | `text` | `text` or `json` |
