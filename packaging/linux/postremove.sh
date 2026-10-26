#!/bin/sh
# Runs after the nutng package is removed. The config in /etc/nutng and
# the nutng group are kept.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
if command -v udevadm >/dev/null 2>&1; then
	udevadm control --reload-rules >/dev/null 2>&1 || true
fi
