# SNMP

nutng includes an SNMP agent that serves the standard UPS-MIB
([RFC 1628](https://www.rfc-editor.org/rfc/rfc1628)), so network monitors
poll it like a UPS network card. It answers SNMPv1, v2c and v3 (with
authentication and AES privacy) and sends v2c traps. It is read-only.

```yaml
listen:
  snmp:
    addr: ":1161"              # UDP; 161 needs root
    community: my-community    # v1/v2c; "public" if neither this nor users is set
    location: Server room
    contact: ops@example.com
    users:                     # SNMPv3
      monitor:
        auth: sha256           # none, md5, sha, sha224, sha256, sha384, sha512
        auth_password_env: NUTNG_SNMP_AUTH
        privacy: aes           # none or aes (AES-128)
        privacy_password_env: NUTNG_SNMP_PRIV
    traps:
      - target: librenms.example.com     # port 162 by default
        community: my-community
```

`listen.snmp: ":1161"` alone turns it on with community `public`.

```sh
snmpwalk -v2c -c my-community nutng-host:1161 1.3.6.1.2.1.33
snmpget -v3 -l authPriv -u monitor -a SHA-256 -A '...' -x AES -X '...' \
  nutng-host:1161 1.3.6.1.2.1.33.1.2.4.0
```

## Several UPSes

The UPS-MIB describes one UPS. nutng serves the UPS named in
`listen.snmp.ups` (default: the first one) and the others by name:

- **SNMPv3**: the context name selects the UPS (`snmpget -n lab ...`).
- **v1/v2c**: append `@<ups>` to the community (`-c my-community@lab`).

In a monitor, add one device per UPS with that context or community.

## What is served

| Group | Objects | From |
|---|---|---|
| system | sysDescr, sysObjectID (the UPS-MIB), sysUpTime, sysName, sysLocation, sysContact | |
| upsIdent | manufacturer, model, firmware, agent version, name, description | `device.*`, `ups.firmware` |
| upsBattery | status (normal, low), seconds on battery, minutes remaining, charge, voltage (0.1 V), current, temperature | `ups.status`, `battery.*` |
| upsInput | line bads, lines, frequency (0.1 Hz), voltage, current, true power | `input.*` (three phases when `input.phases` is 3) |
| upsOutput | source (normal, battery, bypass, booster, reducer, none), frequency, voltage, current, power, percent load | `ups.status`, `output.*`, `ups.load` |
| upsBypass | frequency, voltage, current, power | `input.bypass.*` |
| upsAlarm | alarms present, and the alarm table | see below |
| upsConfig | nominal input/output voltage and frequency, VA and W ratings, low-battery time, beeper, transfer points | `*.nominal`, `battery.runtime.low`, `ups.beeper.status`, `input.transfer.*` |

Objects the UPS does not report are left out (`noSuchInstance`), as on
hardware cards.

**Alarms** come from the status flags: battery bad (`RB`), on battery
(`OB`), low battery (`LB`), output overload (`OVER`), on bypass, output
off, general fault (`ALARM`), shutdown pending (`FSD`), test in progress,
and communications lost when nutng loses the UPS.

**Traps** (SNMPv2c, to every `traps` target): `upsTrapOnBattery` when the
UPS goes on battery and every minute while it stays there, and
`upsTrapAlarmEntryAdded` / `upsTrapAlarmEntryRemoved` as alarms come and
go. Each trap also carries `upsIdentName.0`, the UPS's name.

## Monitoring tools

**LibreNMS / Observium**: add the device with its host, SNMP port 1161
and the community or v3 credentials. It is detected as a UPS from the
UPS-MIB; battery, load, voltages and runtime appear as sensors. For more
UPSes, add the same host once per UPS with `community@ups`, or the v3
context.

**Zabbix**: link the *Generic SNMP* and a UPS-MIB template (for example
the community "UPS SNMP" templates) to a host with an SNMP interface on
port 1161.

**PRTG**: add a device with the nutng host and the SNMP settings, then the
*SNMP Custom Table* sensor on `1.3.6.1.2.1.33.1.2` (or PRTG's UPS health
sensor, which reads the UPS-MIB).

Any tool that can read a MIB can load `UPS-MIB` (shipped with net-snmp as
`/usr/share/snmp/mibs/ietf/UPS-MIB` once `snmp-mibs-downloader` or similar
is installed).

## Notes

- SNMPv3 users need passwords of at least 8 characters. DES privacy is not
  supported; use AES.
- The SNMPv3 engine ID is derived from the host name, so it survives
  restarts; set `engine_id` (hex) to choose one. `engineBoots` comes from
  the clock, so it grows at every restart without a state file.
- Ports below 1024 need root (or `CAP_NET_BIND_SERVICE` on Linux). If
  snmpd already uses 161, keep nutng on 1161, or have snmpd proxy
  `1.3.6.1.2.1.33` to it.
- `nutng doctor` checks that the agent answers.
