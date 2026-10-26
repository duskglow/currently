#!/bin/sh
# Runs before the Currently package is removed (not on upgrade).
set -e
case "$1" in
	upgrade|1) exit 0 ;; # deb passes "upgrade", rpm passes the remaining count
esac
if [ -d /run/systemd/system ]; then
	systemctl disable --now currently.service >/dev/null 2>&1 || true
elif command -v rc-update >/dev/null 2>&1; then
	rc-service currently stop >/dev/null 2>&1 || true
	rc-update del currently default >/dev/null 2>&1 || true
fi
