#!/bin/sh
# Builds a FreeBSD package (.pkg) from a Currently release archive. Run it on
# FreeBSD (the release workflow runs it in a FreeBSD VM):
#
#   packaging/freebsd/make-pkg.sh 1.2.3 dist/currently_1.2.3_freebsd_amd64.tar.gz dist/
#
# Install the result with `pkg add currently-1.2.3.pkg`.
set -eu
version=$1 archive=$2 out=$3
# pkg versions cannot contain "-"; 0.1.0.alpha.1 sorts before 0.1.0.
pkgversion=$(echo "$version" | tr - .)
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
stage=$work/stage
mkdir -p "$work/x" "$stage/usr/local/bin" "$stage/usr/local/etc/rc.d" \
	"$stage/usr/local/lib/currently/plugins" "$stage/usr/local/share/examples/currently" \
	"$stage/usr/local/share/doc/currently"
tar -xzf "$archive" -C "$work/x"
install -m 755 "$work/x/currently" "$stage/usr/local/bin/currently"
for p in "$work"/x/plugins/currently-*; do
	[ -f "$p" ] && install -m 755 "$p" "$stage/usr/local/lib/currently/plugins/"
done
install -m 555 "$here/currently" "$stage/usr/local/etc/rc.d/currently"
install -m 644 "$work/x/currently.example.yaml" "$stage/usr/local/share/examples/currently/currently.example.yaml"
install -m 644 "$work/x/README.md" "$stage/usr/local/share/doc/currently/README.md"
install -m 644 "$work/x/LICENSE" "$stage/usr/local/share/doc/currently/LICENSE"
install -m 644 "$work/x/NOTICE" "$stage/usr/local/share/doc/currently/NOTICE"

cat > "$work/+MANIFEST" <<MANIFEST
name: currently
version: "$pkgversion"
origin: sysutils/currently
comment: "Easy UPS monitor: NUT protocol, SNMP, Prometheus and REST"
www: "https://github.com/duskglow/currently"
maintainer: "Currently maintainers"
prefix: /usr/local
licenselogic: single
licenses: [APACHE20]
categories: [sysutils]
desc: <<EOD
Currently monitors UPSes over USB and other drivers, shuts computers down
safely on power failure, and serves UPS state over the NUT protocol,
SNMP (UPS-MIB), Prometheus and a REST API.
EOD
messages: [ { message: "Set up currently with \`currently init\`, then \`sysrc currently_enable=YES\` and \`service currently start\`." } ]
MANIFEST
(cd "$stage" && find . -type f | sed 's|^\./|/|') > "$work/plist"
pkg create -M "$work/+MANIFEST" -r "$stage" -p "$work/plist" -o "$out"
