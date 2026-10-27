# NUT protocol

Currently serves the network protocol of NUT's upsd (version 1.3), so
anything that works with NUT should work with Currently. It is tested
against the real NUT 2.8 clients (`upsc`, `upscmd`, `upsrw`, and upsmon as
primary and secondary through a power failure) and with Home Assistant's NUT
integration. Synology and QNAP "network UPS server" settings and
Nagios/Icinga `check_ups` use the same protocol but are not tested yet;
reports welcome.

```yaml
listen:
  nut: ":3493"               # all interfaces; default 127.0.0.1:3493
  # nut_tls:                 # STARTTLS
  #   cert_file: /etc/currently/cert.pem
  #   key_file: /etc/currently/key.pem
```

```sh
upsc rack@currently-host
```

Reading needs no login. upsmon and commands need a user:

```yaml
users:
  upsmon:                       # for MONITOR lines in upsmon.conf
    password_env: CURRENTLY_UPSMON_PASSWORD
    role: secondary             # secondary: watch only; primary: may set FSD
  admin:
    password_env: CURRENTLY_ADMIN_PASSWORD
    role: admin                 # primary, plus upscmd and upsrw
```

## Clients

**upsmon** (a NUT secondary): in `upsmon.conf`,

```
MONITOR rack@currently-host 1 upsmon <password> secondary
```

**Home Assistant**: add the *Network UPS Tools (NUT)* integration with the
Currently host, port 3493, and (optionally) a user.

**Synology DSM**: Control Panel → Hardware & Power → UPS → *Synology UPS
server*, with the Currently host as the server. DSM logs in as `monuser` /
`secret` to the UPS named `ups`, so name the UPS `ups` and add that user
(role `secondary`).

**QNAP**: Control Panel → External Device → UPS → *Network UPS slave*,
with the Currently host's address.

**Nagios / Icinga**:

```sh
check_ups -H currently-host -u rack -v BATTPCT -w 50 -c 20
```
