#!/bin/sh
# Builds a FreeBSD package (.pkg) from a nutng release archive. Run it on
# FreeBSD (the release workflow runs it in a FreeBSD VM):
#
#   packaging/freebsd/make-pkg.sh 1.2.3 dist/nutng_1.2.3_freebsd_amd64.tar.gz dist/
#
# Install the result with `pkg add nutng-1.2.3.pkg`.
set -eu
version=$1 archive=$2 out=$3
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
stage=$work/stage
mkdir -p "$work/x" "$stage/usr/local/bin" "$stage/usr/local/etc/rc.d" \
	"$stage/usr/local/lib/nutng/plugins" "$stage/usr/local/share/examples/nutng" \
	"$stage/usr/local/share/doc/nutng"
tar -xzf "$archive" -C "$work/x"
install -m 755 "$work/x/nutng" "$stage/usr/local/bin/nutng"
for p in "$work"/x/plugins/nutng-*; do
	[ -f "$p" ] && install -m 755 "$p" "$stage/usr/local/lib/nutng/plugins/"
done
install -m 555 "$here/nutng" "$stage/usr/local/etc/rc.d/nutng"
install -m 644 "$work/x/nutng.example.yaml" "$stage/usr/local/share/examples/nutng/nutng.example.yaml"
install -m 644 "$work/x/README.md" "$stage/usr/local/share/doc/nutng/README.md"
install -m 644 "$work/x/LICENSE" "$stage/usr/local/share/doc/nutng/LICENSE"

cat > "$work/+MANIFEST" <<MANIFEST
name: nutng
version: "$version"
origin: sysutils/nutng
comment: "Easy UPS monitor: NUT protocol, SNMP, Prometheus and REST"
www: "https://github.com/nut-ng/nut-ng"
maintainer: "nut-ng maintainers"
prefix: /usr/local
licenselogic: single
licenses: [APACHE20]
categories: [sysutils]
desc: <<EOD
nut-ng monitors UPSes over USB and other drivers, shuts computers down
safely on power failure, and serves UPS state over the NUT protocol,
SNMP (UPS-MIB), Prometheus and a REST API.
EOD
messages: [ { message: "Set up nutng with \`nutng init\`, then \`sysrc nutng_enable=YES\` and \`service nutng start\`." } ]
MANIFEST
(cd "$stage" && find . -type f | sed 's|^\./|/|') > "$work/plist"
pkg create -M "$work/+MANIFEST" -r "$stage" -p "$work/plist" -o "$out"
