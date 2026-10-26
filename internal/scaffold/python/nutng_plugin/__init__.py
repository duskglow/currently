"""SDK for nutng plugins written in Python.

A driver plugin reads a kind of UPS; an output plugin sends UPS data
somewhere. Subclass Driver/Device or Output, then call serve_driver() or
serve_output() from your script. nutng starts the script (list it under
plugins.commands in nutng.yaml) and talks to it over gRPC.

This package was copied into your project by `nutng plugin new`. It needs
the packages in requirements.txt.
"""

import os
import sys
import threading
from concurrent import futures

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "_gen"))

import grpc  # noqa: E402
from google.protobuf import struct_pb2  # noqa: E402
from grpc_health.v1 import health, health_pb2, health_pb2_grpc  # noqa: E402

from nutng.v1 import driver_pb2, driver_pb2_grpc  # noqa: E402
from nutng.v1 import output_pb2, output_pb2_grpc  # noqa: E402
from nutng.v1 import types_pb2  # noqa: E402

__all__ = [
    "Driver", "Device", "Output", "Host",
    "NotFound", "NotSupported", "Invalid", "NotConnected",
    "serve_driver", "serve_output", "log",
]

PROTOCOL_VERSION = 1
MAGIC_COOKIE_KEY = "NUTNG_PLUGIN"
MAGIC_COOKIE_VALUE = "8c4a5d0e-nutng-plugin-v1"


# Errors. Raise them from your code; nutng shows the message.
class NotFound(Exception):
    """The UPS, variable or command does not exist."""


class NotSupported(Exception):
    """The device does not support this."""


class Invalid(Exception):
    """A value was rejected."""


class NotConnected(Exception):
    """The device is not reachable right now."""


_CODES = [
    (NotFound, grpc.StatusCode.NOT_FOUND),
    (NotSupported, grpc.StatusCode.UNIMPLEMENTED),
    (Invalid, grpc.StatusCode.INVALID_ARGUMENT),
    (NotConnected, grpc.StatusCode.UNAVAILABLE),
]


def _abort(context, exc):
    for cls, code in _CODES:
        if isinstance(exc, cls):
            context.abort(code, str(exc) or cls.__doc__)
    context.abort(grpc.StatusCode.UNKNOWN, f"{type(exc).__name__}: {exc}")


def log(message, level="info", **fields):
    """Writes a log line that appears in nutng's log."""
    import json
    entry = {"@level": level, "@message": message}
    entry.update({k: str(v) for k, v in fields.items()})
    print(json.dumps(entry), file=sys.stderr, flush=True)


def _config(struct):
    from google.protobuf.json_format import MessageToDict
    return MessageToDict(struct) if struct is not None else {}


def _struct(d):
    s = struct_pb2.Struct()
    s.update(d or {})
    return s


# --- Drivers -------------------------------------------------------------

class Device:
    """One open UPS."""

    def watch(self, stop):
        """Yields dicts of variables, such as {"ups.status": "OL"}, until
        stop (a threading.Event) is set or the device fails (raise)."""
        raise NotImplementedError

    def commands(self):
        """Names of the instant commands the device supports."""
        return []

    def writable(self):
        """Names of the variables set_var accepts."""
        return []

    def run_command(self, name, param):
        raise NotSupported(f"command {name}")

    def set_var(self, name, value):
        raise NotSupported(f"variable {name}")

    def close(self):
        pass


class Driver:
    """A kind of UPS. Set name, version and description."""

    name = ""
    version = "0.0.0"
    description = ""

    def validate(self, config):
        """Returns a list of problems with a UPS block's options."""
        return []

    def discover(self):
        """Returns devices found without configuration, as dicts with
        "id", "description" and "config"."""
        return []

    def open(self, config):
        """Opens one UPS and returns a Device."""
        raise NotImplementedError


class _DriverServicer(driver_pb2_grpc.DriverServiceServicer):
    def __init__(self, driver):
        self.d = driver
        self.devices = {}
        self.next = 0
        self.lock = threading.Lock()

    def DescribeDriver(self, request, context):
        return driver_pb2.DescribeDriverResponse(info=types_pb2.PluginInfo(
            name=self.d.name, kind=types_pb2.PLUGIN_KIND_DRIVER, version=self.d.version,
            description=self.d.description, protocol_version=PROTOCOL_VERSION))

    def ValidateDriverConfig(self, request, context):
        try:
            errors = self.d.validate(_config(request.config)) or []
        except Exception as e:  # noqa: BLE001
            errors = [str(e)]
        return driver_pb2.ValidateDriverConfigResponse(errors=[str(e) for e in errors])

    def DiscoverDevices(self, request, context):
        try:
            found = self.d.discover() or []
        except Exception as e:  # noqa: BLE001
            _abort(context, e)
        return driver_pb2.DiscoverDevicesResponse(devices=[
            driver_pb2.DiscoveredDevice(id=str(f.get("id", "")), description=str(f.get("description", "")),
                                        config=_struct(f.get("config")))
            for f in found])

    def OpenDevice(self, request, context):
        try:
            dev = self.d.open(_config(request.config))
        except Exception as e:  # noqa: BLE001
            _abort(context, e)
        with self.lock:
            self.next += 1
            dev_id = str(self.next)
            self.devices[dev_id] = dev
        return driver_pb2.OpenDeviceResponse(
            device_id=dev_id,
            commands=[types_pb2.Command(name=c) for c in dev.commands()],
            writable=list(dev.writable()))

    def _device(self, dev_id, context):
        with self.lock:
            dev = self.devices.get(dev_id)
        if dev is None:
            context.abort(grpc.StatusCode.NOT_FOUND, f"no open device {dev_id}")
        return dev

    def WatchDevice(self, request, context):
        dev = self._device(request.device_id, context)
        stop = threading.Event()
        context.add_callback(stop.set)
        try:
            for update in dev.watch(stop):
                if stop.is_set():
                    return
                yield driver_pb2.WatchDeviceResponse(vars={k: str(v) for k, v in update.items()})
        except Exception as e:  # noqa: BLE001
            _abort(context, e)
        if not stop.is_set():
            context.abort(grpc.StatusCode.UNKNOWN, "device stopped reporting")

    def RunCommand(self, request, context):
        try:
            self._device(request.device_id, context).run_command(request.command, request.param)
        except Exception as e:  # noqa: BLE001
            _abort(context, e)
        return driver_pb2.RunCommandResponse()

    def SetVar(self, request, context):
        try:
            self._device(request.device_id, context).set_var(request.name, request.value)
        except Exception as e:  # noqa: BLE001
            _abort(context, e)
        return driver_pb2.SetVarResponse()

    def CloseDevice(self, request, context):
        with self.lock:
            dev = self.devices.pop(request.device_id, None)
        if dev is not None:
            dev.close()
        return driver_pb2.CloseDeviceResponse()


# --- Outputs -------------------------------------------------------------

class Host:
    """nutng, as seen from an output plugin."""

    def __init__(self, address, token):
        self._channel = grpc.insecure_channel(address)
        self._stub = output_pb2_grpc.HostServiceStub(self._channel)
        self._md = (("authorization", "Bearer " + token),)

    def list_ups(self):
        """Returns every UPS as a dict: name, state, vars ({name: value})."""
        resp = self._stub.ListUPS(output_pb2.ListUPSRequest(), metadata=self._md)
        return [_ups(u) for u in resp.ups]

    def get_ups(self, name):
        return _ups(self._stub.GetUPS(output_pb2.GetUPSRequest(name=name), metadata=self._md).ups)

    def recent_events(self, n=20):
        resp = self._stub.GetRecentEvents(output_pb2.GetRecentEventsRequest(limit=n), metadata=self._md)
        return [_event(e) for e in resp.events]

    def events(self, ups=""):
        """Yields events as dicts (type, ups, message, changed) until the
        connection to nutng ends."""
        for resp in self._stub.SubscribeEvents(output_pb2.SubscribeEventsRequest(ups=ups), metadata=self._md):
            yield _event(resp.event)

    def run_command(self, ups, command, param=""):
        self._stub.RunUPSCommand(output_pb2.RunUPSCommandRequest(ups=ups, command=command, param=param), metadata=self._md)

    def set_var(self, ups, variable, value):
        self._stub.SetUPSVar(output_pb2.SetUPSVarRequest(ups=ups, variable=variable, value=value), metadata=self._md)

    def close(self):
        self._channel.close()


_STATES = {types_pb2.CONN_STATE_CONNECTING: "connecting", types_pb2.CONN_STATE_CONNECTED: "connected",
           types_pb2.CONN_STATE_STALE: "stale", types_pb2.CONN_STATE_FAILED: "failed"}


def _ups(u):
    return {"name": u.name, "description": u.description, "driver": u.driver,
            "state": _STATES.get(u.state, "unknown"), "vars": {k: v.value for k, v in u.vars.items()},
            "commands": [c.name for c in u.commands]}


def _event(e):
    return {"seq": e.seq, "type": e.type, "ups": e.ups, "message": e.message, "changed": dict(e.changed)}


class Output:
    """A place to send UPS data. Set name, version and description."""

    name = ""
    version = "0.0.0"
    description = ""

    def validate(self, config):
        """Returns a list of problems with the output's options."""
        return []

    def run(self, host, config, stop):
        """Runs until stop (a threading.Event) is set. Raise to report a
        failure; nutng restarts the output."""
        raise NotImplementedError


class _OutputServicer(output_pb2_grpc.OutputServiceServicer):
    def __init__(self, output):
        self.o = output

    def DescribeOutput(self, request, context):
        return output_pb2.DescribeOutputResponse(info=types_pb2.PluginInfo(
            name=self.o.name, kind=types_pb2.PLUGIN_KIND_OUTPUT, version=self.o.version,
            description=self.o.description, protocol_version=PROTOCOL_VERSION))

    def ValidateOutputConfig(self, request, context):
        try:
            errors = self.o.validate(_config(request.config)) or []
        except Exception as e:  # noqa: BLE001
            errors = [str(e)]
        return output_pb2.ValidateOutputConfigResponse(errors=[str(e) for e in errors])

    def RunOutput(self, request, context):
        stop = threading.Event()
        context.add_callback(stop.set)
        host = Host(request.host_address, request.host_token)
        try:
            self.o.run(host, _config(request.config), stop)
        except Exception as e:  # noqa: BLE001
            if not stop.is_set():
                _abort(context, e)
        finally:
            host.close()
        return output_pb2.RunOutputResponse()


# --- Serving -------------------------------------------------------------

def _watch_parent():
    """Exits when nutng does, even if nutng is killed and cannot stop us."""
    try:
        pid = int(os.environ.get("NUTNG_PARENT_PID", "0"))
    except ValueError:
        return
    if pid <= 0:
        return

    def wait():
        if os.name == "nt":
            import ctypes
            kernel32 = ctypes.windll.kernel32
            handle = kernel32.OpenProcess(0x00100000, False, pid)  # SYNCHRONIZE
            if not handle:
                return
            kernel32.WaitForSingleObject(handle, 0xFFFFFFFF)  # INFINITE
        else:
            import time
            while os.getppid() == pid:
                time.sleep(2)
        os._exit(0)

    threading.Thread(target=wait, daemon=True).start()


def _serve(register):
    if os.environ.get(MAGIC_COOKIE_KEY) != MAGIC_COOKIE_VALUE:
        print("This is a nutng plugin. List it under plugins.commands in nutng.yaml; nutng starts it.",
              file=sys.stderr)
        sys.exit(1)
    _watch_parent()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=16))
    register(server)
    hs = health.HealthServicer()
    hs.set("plugin", health_pb2.HealthCheckResponse.SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(hs, server)

    # go-plugin's controller: nutng calls Shutdown to stop the plugin. The
    # request and reply are empty messages, so no generated code is needed.
    def shutdown(request, context):
        threading.Thread(target=lambda: server.stop(grace=1), daemon=True).start()
        return b""

    server.add_generic_rpc_handlers((grpc.method_handlers_generic_handler("plugin.GRPCController", {
        "Shutdown": grpc.unary_unary_rpc_method_handler(
            shutdown, request_deserializer=lambda b: b, response_serializer=lambda b: b),
    }),))
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    # The go-plugin handshake: core protocol | app protocol | network | address | protocol.
    print(f"1|{PROTOCOL_VERSION}|tcp|127.0.0.1:{port}|grpc", flush=True)
    server.wait_for_termination()


def serve_driver(driver):
    """Serves a Driver until nutng stops the plugin."""
    _serve(lambda s: driver_pb2_grpc.add_DriverServiceServicer_to_server(_DriverServicer(driver), s))


def serve_output(output):
    """Serves an Output until nutng stops the plugin."""
    _serve(lambda s: output_pb2_grpc.add_OutputServiceServicer_to_server(_OutputServicer(output), s))

