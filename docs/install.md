# Install

Every release has packages for each platform on the
[releases page](https://github.com/duskglow/currently/releases). For now,
releases are alpha pre-releases (see the [home page](index.md)).

The packages install the `currently` binary, the bundled plugins (the SNMP and serial drivers,
and the MQTT output) and a system service, and leave the
service stopped until there is a config file (except on Windows, where it
starts at once and finds USB UPSes by itself).

=== "Debian / Ubuntu"

    ```sh
    sudo apt install ./currently_<version>_linux_amd64.deb
    ```

=== "Fedora / RHEL / openSUSE"

    ```sh
    sudo dnf install ./currently_<version>_linux_amd64.rpm
    ```

=== "Alpine"

    ```sh
    sudo apk add --allow-untrusted ./currently_<version>_linux_amd64.apk
    ```

=== "Arch"

    ```sh
    sudo pacman -U ./currently_<version>_linux_amd64.pkg.tar.zst
    ```

=== "Windows"

    Run `currently_<version>_windows_amd64.msi`, or with winget:

    ```powershell
    winget install duskglow.Currently
    ```

    The MSI installs to `C:\Program Files\currently`, adds it to `PATH`, and
    installs and starts the `currently` service.

=== "macOS"

    ```sh
    brew install --cask duskglow/tap/currently
    sudo currently service install
    ```

=== "FreeBSD"

    ```sh
    pkg add currently_<version>_freebsd_amd64.pkg
    sysrc currently_enable=YES
    ```

=== "Docker"

    ```sh
    docker run -d --name currently --restart unless-stopped \
      --device /dev/hidraw0 -v /etc/currently:/etc/currently \
      -p 3493:3493 -p 9300:9300 -p 1161:1161/udp ghcr.io/duskglow/currently
    ```

    A container cannot shut its host down: use it for monitoring, and
    mount a config whose `listen` addresses use all interfaces (`":9300"`).

Packages are for amd64 and arm64 (and armv7 on Linux, for Raspberry Pi
and similar boards). Archives (`.tar.gz`, `.zip`) of the same builds work
anywhere: unpack them and run `currently`.

## What the packages install (Linux)

| Path | What |
|---|---|
| `/usr/bin/currently` | The CLI and daemon (a link to `/usr/lib/currently/currently`) |
| `/usr/lib/currently/plugins/` | Bundled plugins (SNMP and serial drivers, MQTT output) |
| `/usr/lib/systemd/system/currently.service` | The service (`/etc/init.d/currently` on Alpine) |
| `/usr/lib/udev/rules.d/90-currently-ups.rules` | Lets the `currently` group open USB UPSes |
| `/etc/currently/` | Your `currently.yaml`, and `plugins.d/` for more plugins |
| `/etc/default/currently` | Secrets for `*_env` settings (mode 600) |

## Verifying a download

`checksums.txt` lists every file's SHA-256 and is signed with
[cosign](https://docs.sigstore.dev/) keyless signing:

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'github.com/duskglow/currently' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

Each archive has an SPDX SBOM next to it (`*.sbom.json`).

## From source

With Go 1.26 or newer:

```sh
git clone https://github.com/duskglow/currently && cd Currently
go build -o currently ./cmd/currently
# optional plugins, in plugins/ next to currently:
go build -o plugins/currently-output-mqtt ./plugins/mqtt
go build -o plugins/currently-driver-snmp ./plugins/snmp
go build -o plugins/currently-driver-serial ./plugins/serial
```

No C compiler is needed on any platform (`CGO_ENABLED=0`).
