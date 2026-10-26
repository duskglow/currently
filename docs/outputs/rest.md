# REST API

JSON over HTTP at `/api/v1` on `listen.http` (default
`127.0.0.1:9300`). The full description is served at
`/api/v1/openapi.yaml`.

```sh
curl http://127.0.0.1:9300/api/v1/ups
curl http://127.0.0.1:9300/api/v1/ups/rack
curl http://127.0.0.1:9300/api/v1/ups/rack/vars/battery.charge
curl http://127.0.0.1:9300/api/v1/shutdown           # shutdown policy state
curl -N http://127.0.0.1:9300/api/v1/events          # Server-Sent Events
```

Over the network the API only reads, unless `api.token` (or `token_env`)
is set; then commands and variable changes need it as a bearer token:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://currently-host:9300/api/v1/ups/rack/commands/beeper.disable
```

The Currently CLI talks to the daemon over a local socket with the same API,
and falls back to the read-only HTTP API when it may not open the socket.
