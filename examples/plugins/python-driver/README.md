# Currently driver plugin "example-py" (Python)

Created by `currently plugin new`. `currently_plugin/` is the Currently plugin SDK;
`currently_plugin/_gen/` is generated from Currently's proto files.

```sh
python -m pip install -r requirements.txt
```

Then add the plugin to currently.yaml (see the top of plugin.py) and check it
with `currently plugin info example-py`.
