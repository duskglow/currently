# Plugins

Drivers (new kinds of UPS) and outputs (new places to send UPS data) can
be added without rebuilding Currently. A plugin is a separate program that
Currently starts and talks to over gRPC, using
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin).

```sh
currently plugin list          # installed plugins, and the directories searched
currently plugin info mqtt     # a plugin's description and config options
```

## Installing a plugin

Put an executable named `currently-driver-<name>` or `currently-output-<name>` in
a plugin directory:

1. `plugins/` next to the Currently executable;
2. `plugins.d/` next to `currently.yaml`;
3. `/usr/local/lib/currently/plugins`, `/usr/lib/currently/plugins` (Linux),
   `C:\Program Files\currently\plugins` (Windows);

or list more directories, and plugins that need an interpreter, in the
config:

```yaml
plugins:
  dirs: [/opt/currently/plugins]
  commands:
    - ["python3", "/opt/currently/myups/plugin.py"]
```

Then use it like a built-in: `driver: <name>` in a UPS entry, or an entry
under `outputs:`. Currently restarts a plugin that crashes, stops plugins the
config no longer uses, and plugins exit when Currently does. Outputs can only
read unless their entry has `allow_control: true`.

## Writing a plugin

```sh
currently plugin new driver myups              # Go
currently plugin new output alerts --lang python
```

creates a project to start from, with a README.

- **Go** plugins implement the same `sdk.Driver` / `sdk.Output`
  interfaces as the built-in drivers and outputs
  ([pkg/sdk](https://github.com/duskglow/currently/tree/main/pkg/sdk)) and call
  `plugin.ServeDriver` / `plugin.ServeOutput`
  ([pkg/plugin](https://github.com/duskglow/currently/tree/main/pkg/plugin)).
- **Python** plugins use the bundled `currently_plugin` package (grpcio). See
  [examples/plugins/python-driver](https://github.com/duskglow/currently/tree/main/examples/plugins/python-driver).
- **Other languages** implement the gRPC services in
  [proto/currently/v1](https://github.com/duskglow/currently/tree/main/proto/currently/v1)
  and print go-plugin's handshake line.

For a quick integration without gRPC, the [`exec` driver](drivers.md#exec)
turns any program that prints UPS variables into a UPS.

The plugin protocol is versioned (`v1`) separately from Currently, so plugins
keep working across Currently upgrades.
