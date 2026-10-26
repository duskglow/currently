# NUT protocol

nutng serves the network protocol of NUT's upsd (version 1.3), so
anything that works with NUT works with nutng: `upsc`, `upscmd`, `upsrw`,
`upsmon`, Home Assistant's NUT integration, Synology and QNAP "network UPS
server" settings, and Nagios/Icinga `check_ups`. It is tested against the
real NUT 2.8 clients, including upsmon as primary and secondary through a
power failure.

```yaml
listen:
  nut: ":3493"               # all interfaces; default 127.0.0.1:3493
  # nut_tls:                 # STARTTLS
  #   cert_file: /etc/nutng/cert.pem
  #   key_file: /etc/nutng/key.pem
```

```sh
upsc rack@nutng-host
```

Reading needs no login. upsmon and commands need a user:

```yaml
users:
  upsmon:                       # for MONITOR lines in upsmon.conf
    password_env: NUTNG_UPSMON_PASSWORD
    role: secondary             # secondary: watch only; primary: may set FSD
  admin:
    password_env: NUTNG_ADMIN_PASSWORD
    role: admin                 # primary, plus upscmd and upsrw
```

## Clients

**upsmon** (a NUT secondary): in `upsmon.conf`,

```
MONITOR rack@nutng-host 1 upsmon <password> secondary
```

**Home Assistant**: add the *Network UPS Tools (NUT)* integration with the
nutng host, port 3493, and (optionally) a user.

**Synology DSM**: Control Panel → Hardware & Power → UPS → *Synology UPS
server*, with the nutng host as the server. DSM logs in as `monuser` /
`secret` to the UPS named `ups`, so name the UPS `ups` and add that user
(role `secondary`).

**QNAP**: Control Panel → External Device → UPS → *Network UPS slave*,
with the nutng host's address.

**Nagios / Icinga**:

```sh
check_ups -H nutng-host -u rack -v BATTPCT -w 50 -c 20
```
