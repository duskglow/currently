#!/bin/sh
# Runs after the nutng package is installed or upgraded.
set -e

# The nutng group may open USB UPSes without root (see the udev rule), so
# users in it can run `nutng detect` and `nutng debug capture`.
if ! getent group nutng >/dev/null 2>&1; then
	if command -v groupadd >/dev/null 2>&1; then
		groupadd --system nutng
	elif command -v addgroup >/dev/null 2>&1; then
		addgroup -S nutng
	fi
fi
chmod 600 /etc/default/nutng 2>/dev/null || true

if command -v udevadm >/dev/null 2>&1; then
	udevadm control --reload-rules 2>/dev/null || true
	udevadm trigger --subsystem-match=hidraw 2>/dev/null || true
fi

# Start the service only when there is a config file: without one, the
# computer may be running NUT, which uses the same port and UPS.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
	if [ -f /etc/nutng/nutng.yaml ]; then
		systemctl enable nutng.service >/dev/null 2>&1 || true
		systemctl restart nutng.service || true
	fi
elif command -v rc-update >/dev/null 2>&1; then
	if [ -f /etc/nutng/nutng.yaml ]; then
		rc-update add nutng default >/dev/null 2>&1 || true
		rc-service nutng restart || true
	fi
fi

if [ ! -f /etc/nutng/nutng.yaml ]; then
	cat <<'MSG'
nutng is installed. To set it up:
  sudo nutng init                  # find the UPS and write /etc/nutng/nutng.yaml
  sudo nutng service start
or start it without a config (it finds USB UPSes by itself):
  sudo systemctl enable --now nutng
MSG
fi
