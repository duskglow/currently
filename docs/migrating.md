# Moving from NUT

`currently import-nut` reads an existing NUT configuration (`ups.conf`,
`upsd.conf`, `upsd.users`, `upsmon.conf`) and writes the equivalent
`currently.yaml`, listing anything it could not convert:

```sh
sudo currently import-nut /etc/nut -o currently.yaml
currently config validate --config currently.yaml
```

| NUT | Currently |
|---|---|
| `ups.conf` sections with `usbhid-ups` | `ups:` entries with the `usbhid` driver (the default) |
| `snmp-ups` | the [`snmp` driver](drivers.md#snmp-plugin) (UPS-MIB and APC) |
| `apcsmart`, `blazer_ser`, `nutdrv_qx` on a serial port | the [`serial` driver](drivers.md#serial-plugin) |
| other drivers | the [`nut` or `exec` driver](drivers.md), or a plugin |
| `upsd.conf` `LISTEN` | `listen.nut` |
| `upsd.users` | `users:` (roles `secondary`, `primary`, `admin`; `slave`/`master` are accepted) |
| `upsmon.conf` `MONITOR`, `MINSUPPLIES` | `shutdown.ups`, `shutdown.min_supplies` |
| `HOSTSYNC`, `FINALDELAY`, `DEADTIME` | `wait_for_secondaries`, `final_delay`, `dead_time` |
| `SHUTDOWNCMD` | `shutdown.command` |
| `POWERDOWNFLAG` + `upsdrvctl shutdown` | `shutdown.power_off_ups` |

## Running both for a while

Currently and NUT can run side by side while you switch:

- Existing **upsmon** secondaries can follow a Currently primary unchanged:
  give them a `secondary` user in `users:`.
- A Currently secondary can follow NUT's **upsd** with the `nut` driver.
- Currently can read a UPS that NUT's driver owns through `upsd` (the `nut`
  driver), so you can try every output before moving the USB cable.

Two programs cannot share port 3493 or, on Linux and FreeBSD, the USB
device. Stop NUT (`sudo systemctl disable --now nut-server nut-monitor`)
before Currently takes over; `currently doctor` warns while NUT is still running.
