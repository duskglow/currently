#!/usr/bin/env python3
"""nutng driver plugin "example-py", created by `nutng plugin new`.

Add it to nutng.yaml:

    plugins:
      commands:
        - ["python3", "/path/to/nut-ng/examples/plugins/python-driver/plugin.py"]
    ups:
      myups:
        driver: example-py
        address: 192.168.1.50
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import nutng_plugin  # noqa: E402


class Device(nutng_plugin.Device):
    def __init__(self, config):
        self.address = config["address"]
        self.interval = float(config.get("interval", 5))
        self.charge = 100
        self.beeper = "enabled"

    def watch(self, stop):
        while not stop.is_set():
            # TODO: read the UPS. Use NUT variable names (`nutng vars` shows them).
            yield {"ups.status": "OL", "battery.charge": self.charge,
                   "ups.beeper.status": self.beeper, "device.model": "example-py example"}
            stop.wait(self.interval)

    def commands(self):
        return ["beeper.disable"]

    def writable(self):
        return ["battery.charge"]

    def run_command(self, name, param):
        if name != "beeper.disable":
            raise nutng_plugin.NotSupported(f"command {name}")
        self.beeper = "disabled"  # TODO: send it to the UPS

    def set_var(self, name, value):
        if not value.isdigit():
            raise nutng_plugin.Invalid(f"{name} must be a number")
        self.charge = int(value)


class Driver(nutng_plugin.Driver):
    name = "example-py"
    version = "0.1.0"
    description = "TODO: what this driver reads"

    def validate(self, config):
        problems = []
        if "address" not in config:
            problems.append("address is required")
        for key in config:
            if key not in ("address", "interval"):
                problems.append(f"unknown option {key}")
        return problems

    def open(self, config):
        nutng_plugin.log("opened", address=config.get("address"))
        return Device(config)


if __name__ == "__main__":
    nutng_plugin.serve_driver(Driver())
