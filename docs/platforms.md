# Platforms

Currently is a single binary with no dependencies on every platform, built
without cgo. It reads USB UPSes through the operating system's own HID
driver, so there is nothing to install and no driver to replace.

| | USB transport | Service | Tested with a real UPS |
|---|---|---|---|
| Linux | hidraw | systemd, OpenRC, SysV | CyberPower CP1500PFCLCD (Ubuntu 24.04, in a container) |
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

Currently runs in LXC, Proxmox, Incus and Docker containers, but a container
only sees the devices its host passes in. A container shares the host's
kernel, so Currently can list the host's UPSes (in `/sys`) even when it
cannot open them; `currently detect` and `currently doctor` then say that
the device node was not passed in.

Pass in each UPS's **hidraw** device. Passing the USB bus (`/dev/bus/usb`),
as NUT setups do, is not enough: Currently talks to UPSes through hidraw.

### Stable names

hidraw numbers (`/dev/hidraw2`) can change when a UPS is replugged or the
host restarts, so give each UPS a stable name on the host with a udev rule,
and pass that in. Currently finds a UPS by its device number, so the name
inside the container does not matter. For example, in
`/etc/udev/rules.d/91-ups-names.rules` on the host:

```
# by serial number (see `currently detect` or `udevadm info -a /dev/hidraw2`)
SUBSYSTEM=="hidraw", ATTRS{serial}=="CTHGP2001234", SYMLINK+="ups-rack"
# or, for identical UPSes without serial numbers, by USB port
SUBSYSTEM=="hidraw", KERNELS=="3-1.2", SYMLINK+="ups-desk"
```

then `sudo udevadm control --reload && sudo udevadm trigger -s hidraw`.
`udevadm info -a /dev/hidraw2 | grep -m1 KERNELS` shows a UPS's port. In the
container's `currently.yaml`, choose UPSes by `serial:` or `id:`, not
`path:`.

### Passing the device in

=== "Proxmox"

    In `/etc/pve/lxc/<id>.conf` (Proxmox VE 8.1 and later), one line per
    UPS; Proxmox creates the node in the container with the right owner:

    ```
    dev0: /dev/ups-rack
    dev1: /dev/ups-desk
    ```

=== "LXC"

    In the container's config, allow the hidraw device major (the first
    number in `ls -l /dev/hidraw*` on the host) and bind each node:

    ```
    lxc.cgroup2.devices.allow: c 245:* rwm
    lxc.mount.entry: /dev/ups-rack dev/ups-rack none bind,optional,create=file
    ```

    In an unprivileged container, root inside is an unprivileged user on
    the host, so let it open the device: add `OWNER="100000"` (the
    container's root, as mapped in `/etc/subuid`) to the host's udev rule.

=== "Incus"

    ```
    incus config device add <container> ups-rack unix-char source=/dev/ups-rack path=/dev/ups-rack
    ```

=== "Docker"

    ```
    docker run ... --device /dev/ups-rack:/dev/ups-rack ghcr.io/duskglow/currently
    ```

If a UPS is replugged while the container runs, restart the container so
it gets the new device.

### What a container can do

A container can monitor, serve monitoring tools and run shutdown hooks,
but it cannot shut its host down: shutting down a container only stops the
container. To shut the host down, run Currently on the host, or run a
Currently secondary (`nut` driver) on each machine that must shut down.

The `ghcr.io/duskglow/currently` image is set up for monitoring: pass the UPS
in as above, mount `/etc/currently`, and listen on all interfaces.
