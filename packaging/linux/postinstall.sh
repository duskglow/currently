#!/bin/sh
# Runs after the Currently package is installed or upgraded.
set -e

# The Currently group may open USB UPSes without root (see the udev rule), so
# users in it can run `currently detect` and `currently debug capture`.
if ! getent group currently >/dev/null 2>&1; then
	if command -v groupadd >/dev/null 2>&1; then
		groupadd --system currently
	elif command -v addgroup >/dev/null 2>&1; then
		addgroup -S currently
	fi
fi
chmod 600 /etc/default/currently 2>/dev/null || true

if command -v udevadm >/dev/null 2>&1; then
	udevadm control --reload-rules 2>/dev/null || true
	udevadm trigger --subsystem-match=hidraw 2>/dev/null || true
fi

# Start the service only when there is a config file: without one, the
# computer may be running NUT, which uses the same port and UPS.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	if [ -f /etc/currently/currently.yaml ]; then
		systemctl enable currently.service >/dev/null 2>&1 || true
		systemctl restart currently.service || true
	fi
elif command -v rc-update >/dev/null 2>&1; then
	if [ -f /etc/currently/currently.yaml ]; then
		rc-update add currently default >/dev/null 2>&1 || true
		rc-service currently restart || true
	fi
fi

if [ ! -f /etc/currently/currently.yaml ]; then
	cat <<'MSG'
currently is installed. To set it up:
  sudo currently init                  # find the UPS and write /etc/currently/currently.yaml
  sudo currently service start
or start it without a config (it finds USB UPSes by itself):
  sudo systemctl enable --now currently
MSG
fi
