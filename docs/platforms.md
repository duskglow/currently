# Platforms

Currently is a single binary with no dependencies on every platform, built
without cgo. It reads USB UPSes through the operating system's own HID
driver, so there is nothing to install and no driver to replace.

| | USB transport | Service | Tested with a real UPS |
|---|---|---|---|
| Linux | hidraw | systemd, OpenRC, SysV | not yet (captures and simulated UPSes) |
| Windows | hid.dll | Service Control Manager | CyberPower CP1500PFCLCD |
| macOS | IOKit (IOHIDDevice) | launchd | not yet |
| FreeBSD | uhid(4) or hidraw(4) | rc.d | not yet |

Reports of UPSes working (or not) on any platform are very welcome;
include a `currently debug capture` file.

## Linux

Currently reads `/dev/hidraw*`. The service runs as root. To run `currently
detect`, `currently serve` or `currently debug capture` as a regular user, add it
to the `currently` group, which the packaged udev rule
(`90-currently-ups.rules`) gives access to UPSes from the common vendors. For
another vendor, copy a line of the rule and change `idVendor`.

## Windows

Currently uses hid.dll, the same driver Windows uses to show the UPS as a
battery, so both keep working. Run `currently service install` in an
Administrator terminal, or use the MSI. Logs go to the Event Viewer
(Windows Logs → Application, source `currently`).

## macOS

Currently uses IOKit and opens the UPS without seizing it, so macOS's own UPS
support (the battery menu, and the UPS shutdown options in System
Settings) keeps working. Turn macOS's own UPS shutdown options off if
Currently should decide when to shut down. UPSes need no privacy permission
(only keyboards and similar devices do).

`sudo currently service install` installs a launchd daemon
(`/Library/LaunchDaemons/currently.plist`).

## FreeBSD

Currently opens `/dev/uhid*` (uhid(4), the default through FreeBSD 14) or
`/dev/hidraw*` (hidraw(4), with `hw.usb.usbhid.enable=1` in
`/boot/loader.conf`). A uhid device can be opened by one program at a
time, so stop NUT's `usbhid-ups` first. If the UPS appears only as
`ugen` in `usbconfig list`, load a HID driver with `kldload uhid`.

The package installs an rc.d script:

```sh
sysrc currently_enable=YES
service currently start
```

Logs go to syslog (`/var/log/messages`), tagged `currently`.

## Containers

The `ghcr.io/duskglow/currently` image is for monitoring: pass the UPS with
`--device /dev/hidrawN`, mount `/etc/currently`, and listen on all interfaces.
A container cannot shut its host down; run Currently on the host for that, or
a Currently secondary on each host that must shut down.
