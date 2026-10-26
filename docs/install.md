# Install

Every release has packages for each platform on the
[releases page](https://github.com/nut-ng/nut-ng/releases). They install
the `nutng` binary, the MQTT plugin and a system service, and leave the
service stopped until there is a config file (except on Windows, where it
starts at once and finds USB UPSes by itself).

=== "Debian / Ubuntu"

    ```sh
    sudo apt install ./nutng_<version>_linux_amd64.deb
    ```

=== "Fedora / RHEL / openSUSE"

    ```sh
    sudo dnf install ./nutng_<version>_linux_amd64.rpm
    ```

=== "Alpine"

    ```sh
    sudo apk add --allow-untrusted ./nutng_<version>_linux_amd64.apk
    ```

=== "Arch"

    ```sh
    sudo pacman -U ./nutng_<version>_linux_amd64.pkg.tar.zst
    ```

=== "Windows"

    Run `nutng_<version>_windows_amd64.msi`, or with winget:

    ```powershell
    winget install nut-ng.nutng
    ```

    The MSI installs to `C:\Program Files\nutng`, adds it to `PATH`, and
    installs and starts the `nutng` service.

=== "macOS"

    ```sh
    brew install --cask nut-ng/tap/nutng
    sudo nutng service install
    ```

=== "FreeBSD"

    ```sh
    pkg add nutng_<version>_freebsd_amd64.pkg
    sysrc nutng_enable=YES
    ```

=== "Docker"

    ```sh
    docker run -d --name nutng --restart unless-stopped \
      --device /dev/hidraw0 -v /etc/nutng:/etc/nutng \
      -p 3493:3493 -p 9300:9300 -p 1161:1161/udp ghcr.io/nut-ng/nutng
    ```

    A container cannot shut its host down: use it for monitoring, and
    mount a config whose `listen` addresses use all interfaces (`":9300"`).

Packages are for amd64 and arm64 (and armv7 on Linux, for Raspberry Pi
and similar boards). Archives (`.tar.gz`, `.zip`) of the same builds work
anywhere: unpack them and run `nutng`.

## What the packages install (Linux)

| Path | What |
|---|---|
| `/usr/bin/nutng` | The CLI and daemon (a link to `/usr/lib/nutng/nutng`) |
| `/usr/lib/nutng/plugins/` | Bundled plugins (MQTT) |
| `/usr/lib/systemd/system/nutng.service` | The service (`/etc/init.d/nutng` on Alpine) |
| `/usr/lib/udev/rules.d/90-nutng-ups.rules` | Lets the `nutng` group open USB UPSes |
| `/etc/nutng/` | Your `nutng.yaml`, and `plugins.d/` for more plugins |
| `/etc/default/nutng` | Secrets for `*_env` settings (mode 600) |

## Verifying a download

`checksums.txt` lists every file's SHA-256 and is signed with
[cosign](https://docs.sigstore.dev/) keyless signing:

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'github.com/nut-ng/nut-ng' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

Each archive has an SPDX SBOM next to it (`*.sbom.json`).

## From source

With Go 1.26 or newer:

```sh
git clone https://github.com/nut-ng/nut-ng && cd nut-ng
go build -o nutng ./cmd/nutng
go build -o plugins/nutng-output-mqtt ./plugins/mqtt   # optional; plugins/ next to nutng
```

No C compiler is needed on any platform (`CGO_ENABLED=0`).
