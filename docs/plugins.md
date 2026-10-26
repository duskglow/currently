# Plugins

Drivers (new kinds of UPS) and outputs (new places to send UPS data) can
be added without rebuilding nutng. A plugin is a separate program that
nutng starts and talks to over gRPC, using
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin).

```sh
nutng plugin list          # installed plugins, and the directories searched
nutng plugin info mqtt     # a plugin's description and config options
```

## Installing a plugin

Put an executable named `nutng-driver-<name>` or `nutng-output-<name>` in
a plugin directory:

1. `plugins/` next to the nutng executable;
2. `plugins.d/` next to `nutng.yaml`;
3. `/usr/local/lib/nutng/plugins`, `/usr/lib/nutng/plugins` (Linux),
   `C:\Program Files\nutng\plugins` (Windows);

or list more directories, and plugins that need an interpreter, in the
config:

```yaml
plugins:
  dirs: [/opt/nutng/plugins]
  commands:
    - ["python3", "/opt/nutng/myups/plugin.py"]
```

Then use it like a built-in: `driver: <name>` in a UPS entry, or an entry
under `outputs:`. nutng restarts a plugin that crashes, stops plugins the
config no longer uses, and plugins exit when nutng does. Outputs can only
read unless their entry has `allow_control: true`.

## Writing a plugin

```sh
nutng plugin new driver myups              # Go
nutng plugin new output alerts --lang python
```

creates a project to start from, with a README.

- **Go** plugins implement the same `sdk.Driver` / `sdk.Output`
  interfaces as the built-in drivers and outputs
  ([pkg/sdk](https://github.com/nut-ng/nut-ng/tree/main/pkg/sdk)) and call
  `plugin.ServeDriver` / `plugin.ServeOutput`
  ([pkg/plugin](https://github.com/nut-ng/nut-ng/tree/main/pkg/plugin)).
- **Python** plugins use the bundled `nutng_plugin` package (grpcio). See
  [examples/plugins/python-driver](https://github.com/nut-ng/nut-ng/tree/main/examples/plugins/python-driver).
- **Other languages** implement the gRPC services in
  [proto/nutng/v1](https://github.com/nut-ng/nut-ng/tree/main/proto/nutng/v1)
  and print go-plugin's handshake line.

For a quick integration without gRPC, the [`exec` driver](drivers.md#exec)
turns any program that prints UPS variables into a UPS.

The plugin protocol is versioned (`v1`) separately from nutng, so plugins
keep working across nutng upgrades.
