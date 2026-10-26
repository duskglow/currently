# Moving from NUT

`nutng import-nut` reads an existing NUT configuration (`ups.conf`,
`upsd.conf`, `upsd.users`, `upsmon.conf`) and writes the equivalent
`nutng.yaml`, listing anything it could not convert:

```sh
sudo nutng import-nut /etc/nut -o nutng.yaml
nutng config validate --config nutng.yaml
```

| NUT | nut-ng |
|---|---|
| `ups.conf` sections with `usbhid-ups` | `ups:` entries with the `usbhid` driver (the default) |
| other drivers | the [`nut` or `exec` driver](drivers.md), or a plugin |
| `upsd.conf` `LISTEN` | `listen.nut` |
| `upsd.users` | `users:` (roles `secondary`, `primary`, `admin`; `slave`/`master` are accepted) |
| `upsmon.conf` `MONITOR`, `MINSUPPLIES` | `shutdown.ups`, `shutdown.min_supplies` |
| `HOSTSYNC`, `FINALDELAY`, `DEADTIME` | `wait_for_secondaries`, `final_delay`, `dead_time` |
| `SHUTDOWNCMD` | `shutdown.command` |
| `POWERDOWNFLAG` + `upsdrvctl shutdown` | `shutdown.power_off_ups` |

## Running both for a while

nutng and NUT can run side by side while you switch:

- Existing **upsmon** secondaries can follow a nutng primary unchanged:
  give them a `secondary` user in `users:`.
- A nutng secondary can follow NUT's **upsd** with the `nut` driver.
- nutng can read a UPS that NUT's driver owns through `upsd` (the `nut`
  driver), so you can try every output before moving the USB cable.

Two programs cannot share port 3493 or, on Linux and FreeBSD, the USB
device. Stop NUT (`sudo systemctl disable --now nut-server nut-monitor`)
before nutng takes over; `nutng doctor` warns while NUT is still running.
