#!/bin/sh
# Runs after the Currently package is removed. The config in /etc/currently and
# the Currently group are kept.
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
if command -v udevadm >/dev/null 2>&1; then
	udevadm control --reload-rules >/dev/null 2>&1 || true
fi
