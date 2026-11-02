# Harbor

[![CI](https://github.com/maikama8/proxy-manager-lite/actions/workflows/ci.yml/badge.svg)](https://github.com/maikama8/proxy-manager-lite/actions/workflows/ci.yml)

A pool of upstream proxies is awkward to keep in a shell script. Harbor is a single binary that imports the pool, checks it, picks one proxy per request, and exposes that choice as a local HTTP gateway, a JSON API, and a small dashboard.

It is a portfolio project for legitimate egress: tests, sites you are allowed to call, and choosing which proxy a process uses. The default config loads a demo pool from the documentation ranges in [RFC 5737](https://www.rfc-editor.org/rfc/rfc5737), so the API, the tests, the UI, and Docker start with no live proxies and no network.

![Harbor in the dark theme, demo pool](docs/images/dashboard-dark.png)

![The same opening pool in the light theme](docs/images/dashboard-light.png)

The screenshots are the demo pool. Four proxies are healthy, London is dead after `connection refused`, and one SOCKS5 row has never been checked. Ashburn answered 18 of its last 20 probes at 300 ms, which scores 87.0. The headline success rate is 76%, and the average latency of the healthy rows is 262 ms. Nothing in those pictures was drawn by hand: it is the opening state the process serves.

## Responsible use

Harbor forwards traffic you were already going to send. It does not grant permission to reach a network. Use it for your own services, for sites whose terms allow the access, for integration tests, and for picking an egress. Respect rate limits and the law. Do not use it to hide abuse, to break into systems, or to share access you do not have.

The health checker requests `health.target` through each real proxy on a timer. Point that URL at a host you are allowed to request. The demo pool is not dialed.

## Features

- Import HTTP, HTTPS, and SOCKS5 proxies, with or without a username and password, from a file or an HTTP list.
- Concurrent health checks. A probe is one fetch of the health target through the proxy. Harbor keeps the latency and a recent success window.
- Optional geo labels through a pluggable lookup: none, a static JSON file, or an HTTP endpoint you configure. A lookup failure does not fail the probe.
- Rotation: round-robin, least-latency, and sticky sessions keyed by a request header.
- Dead proxies leave rotation after a run of failures and are probed again on a slower interval.
- A local gateway that forwards absolute-form HTTP and `CONNECT` through the proxy it picked.
- A JSON API for the pool, the strategy, and the counters.
- A dashboard embedded in the binary. Dark and light. The choice is remembered in the browser.
- Config from YAML, with `PROXYMAN_*` environment variables on top.

## Tech stack

- Go 1.22. The standard library, plus `gopkg.in/yaml.v3` for the config file.
- `go test`, `go vet`, and golangci-lint
- Docker

## Architecture

```mermaid
flowchart TD
    client[HTTP client]
    gw[Gateway listener]
    rot[Rotation]
    pool[Pool]
    check[Health checker]
    geo[Geo lookup]
    api[REST API and dashboard]
    up[Upstream HTTP, HTTPS, or SOCKS5]
    origin[Origin]

    client --> gw
    gw --> rot
    rot --> pool
    check --> pool
    check --> geo
    check --> up
    api --> pool
    api --> rot
    gw --> up
    up --> origin
```

The checker and the gateway share the pool and nothing else. Scoring is a pure function of the recent window, so the dashboard, the tests, and the demo rows use the same formula. The gateway does not add `X-Forwarded-For`. The address an origin sees is the upstream proxy.

Demo rows are held out of rotation and out of the checker. With only the demo pool loaded, the gateway answers `502 no healthy proxy`.

## Getting started

Requires Go 1.22.

```bash
git clone https://github.com/maikama8/proxy-manager-lite.git
cd proxy-manager-lite
go test ./...
go run ./cmd/harbor -config configs/harbor.example.yaml
```

- Dashboard: `http://127.0.0.1:8080`
- Health: `http://127.0.0.1:8080/api/health`
- Gateway: `http://127.0.0.1:8081`

`make build`, `make test`, `make lint`, and `make run` wrap the same commands.

```bash
curl -s http://127.0.0.1:8080/api/health
curl -x http://127.0.0.1:8081 http://example.com/
```

The second command is `502` until a non-demo proxy is healthy. To use your own list, set `demo: false` and `pool.file` in the config. `examples/proxies.example.txt` shows the line format. Those addresses are documentation addresses and will not connect.

### Docker

```bash
docker compose up --build
```

The image listens on 8080 and 8081. Compose sets the bind address to `0.0.0.0`, because `127.0.0.1` inside the container is the container. The example config's demo pool is the default. The process runs as uid 65532.

## Configuration

Settings are read from the YAML file passed to `-config` (or `PROXYMAN_CONFIG`). Omitted fields keep the built-in defaults. A `PROXYMAN_*` variable replaces the file, and an empty value counts: `PROXYMAN_POOL_FILE=` clears a path from YAML. Durations use Go's form (`30s`, `1500ms`, `0s`).

The annotated file is [`configs/harbor.example.yaml`](configs/harbor.example.yaml).

| Variable | Default | Purpose |
| --- | --- | --- |
| `PROXYMAN_SERVER_LISTEN` | `127.0.0.1:8080` | API and dashboard |
| `PROXYMAN_SERVER_GATEWAY` | `127.0.0.1:8081` | HTTP gateway |
| `PROXYMAN_SERVER_TOKEN` | empty | Bearer token for `/api`. Empty means no auth |
| `PROXYMAN_SERVER_READ_TIMEOUT` | `15s` | API read timeout |
| `PROXYMAN_SERVER_WRITE_TIMEOUT` | `30s` | API write timeout. The gateway has none, so a CONNECT tunnel is not cut off |
| `PROXYMAN_SERVER_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown |
| `PROXYMAN_SERVER_GATEWAY_DIAL_TIMEOUT` | `15s` | Handshake to the upstream proxy |
| `PROXYMAN_POOL_FILE` | empty | Proxy list loaded at startup |
| `PROXYMAN_POOL_URL` | empty | `http` or `https` list loaded at startup |
| `PROXYMAN_POOL_REFRESH_INTERVAL` | `0s` | Re-fetch `pool.url`. Requires the URL |
| `PROXYMAN_HEALTH_TARGET` | `https://example.com/` | URL fetched through each real proxy |
| `PROXYMAN_HEALTH_INTERVAL` | `30s` | `0s` disables the schedule |
| `PROXYMAN_HEALTH_TIMEOUT` | `8s` | One probe |
| `PROXYMAN_HEALTH_CONCURRENCY` | `8` | Probes in flight, 1 to 256 |
| `PROXYMAN_HEALTH_HISTORY` | `20` | Samples in the score window |
| `PROXYMAN_HEALTH_EVICT_AFTER` | `3` | Consecutive failures before a proxy leaves rotation |
| `PROXYMAN_HEALTH_REVIVE_AFTER` | `1` | Consecutive successes before a dead proxy returns |
| `PROXYMAN_HEALTH_RECHECK_INTERVAL` | `60s` | How often a dead proxy is probed |
| `PROXYMAN_HEALTH_LATENCY_BUDGET` | `1500ms` | Latency at or above this earns no latency points |
| `PROXYMAN_GEO_PROVIDER` | `none` | `none`, `static`, or `http` |
| `PROXYMAN_GEO_STATIC_FILE` | empty | JSON object of host to `{country, region, city}` |
| `PROXYMAN_GEO_HTTP_URL` | empty | Lookup URL. `{host}` is replaced, or the host is appended |
| `PROXYMAN_GEO_TIMEOUT` | `3s` | Lookup timeout |
| `PROXYMAN_ROTATION_STRATEGY` | `round-robin` | `round-robin`, `least-latency`, or `sticky` |
| `PROXYMAN_ROTATION_STICKY_HEADER` | `X-Proxy-Session` | Sticky key. Falls back to the client IP |
| `PROXYMAN_ROTATION_STICKY_MAX` | `10000` | Remembered keys. At the cap the map is dropped |
| `PROXYMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `PROXYMAN_DEMO` | `false` | `true` loads the opening pool. The example file sets `true` |

An unknown strategy, a bad duration, the same address for both listeners, or `geo.provider: static` without a file stops the process at startup. A missing `pool.file` or a failed startup fetch of `pool.url` does the same. Later refresh failures are logged and the pool is kept.

The API has no authentication unless `server.token` is set, and it can import a URL you name. Keep it on localhost, or set a token. The dashboard stays open either way; the browser asks for the token when `/api` returns 401.

## How a proxy is chosen

**Eligible** proxies are `healthy` or `unknown`, and not demo rows. Unknown means the proxy has not passed a probe and has not been evicted, so a new import is used before its first check. **Dead** proxies are out of rotation until they pass `revive_after` probes in a row.

**Round-robin** walks the eligible ids in sorted order.

**Least-latency** prefers the lowest measured latency, then the higher score, then the id. A proxy with no successful sample is treated as slower than every measured one. If none are measured, the lowest id wins.

**Sticky** keeps a key on the same proxy until that proxy leaves the eligible set, then hashes the key across what remains. An empty key uses least-latency. On the gateway the key is the `X-Proxy-Session` header, then the client IP. From localhost, every client shares one IP, so send the header when you want more than one session.

The response carries `X-Harbor-Proxy` with the id that was used. That header is not forwarded to the origin.

## How the score is computed

A probe succeeds when a response comes back and the status is not 407 and not 5xx. A 404 from the origin still means the proxy reached something. Latency is the time to the status line, not the body.

The window is the last `history` probes (20 by default). The score is 0 to 100, in steps of a tenth:

- 70 points times successes in the window divided by samples in the window.
- 30 points times `1 - latency / budget`, clamped to 0. Latency points are awarded only after a probe has succeeded.

Latency is an exponential moving average of successful probes, with weight 3/10 on the new sample. A constant latency stays at that value. Division is integer, so the score is truncated to a tenth.

Ashburn, in the demo pool and in the screenshot, is 18 of 20 at 300 ms with a 1500 ms budget:

`70 × 18/20 + 30 × (1 − 300/1500) = 63 + 24 = 87.0`

Frankfurt is 20 of 20 at 120 ms, which is 97.6. London is 3 of 20 at 800 ms and ends in failures, so it is dead and scores 24.5. The headline success rate is the windows added together, 76 of 100. The headline latency is the mean of the healthy measured rows, 262 ms.

Eviction does not wait for the window to fill with failures. `evict_after` consecutive failures mark the proxy dead. One success brings it back when `revive_after` is 1.

## Proxy list format

Blank lines and lines whose first non-space character is `#` are ignored. `socks5h` is accepted as SOCKS5; dialing always asks the proxy to resolve the name.

```text
http://192.0.2.10:8080
https://edge:secret@192.0.2.21:8443
socks5://svc:secret@198.51.100.20:1080
edge:secret@192.0.2.11:3128
198.51.100.30:8080:ops:secret
```

A line without a scheme is HTTP. IPv6 hosts use the URL form, `http://[2001:db8::10]:8080`. JSON is also accepted, as an array of those strings or as:

```json
{"proxies": [{"scheme": "socks5", "host": "198.51.100.20", "port": 1080, "username": "svc", "password": "secret"}]}
```

The id is the scheme, username, host, and port. A password change updates the row and keeps its health. The password is never returned by the API. Replacing a demo row with a real import clears the demo numbers so the checker starts clean.

## API

### `GET /api/health`

```bash
curl -s http://127.0.0.1:8080/api/health
```

```json
{
  "status": "ok",
  "version": "0.1.0",
  "strategy": "round-robin",
  "proxies": 6,
  "healthy": 4,
  "dead": 1,
  "unknown": 1,
  "inRotation": 0,
  "demo": true
}
```

`inRotation` is 0 on a fresh demo process. Those rows are on the board and are not dialed.

### `GET /api/overview`

The document the dashboard polls. `stats` is the headline. `proxies` is the table. `settings` echoes the budget, the eviction threshold, and the sticky header. Passwords are not included. `hasPassword` is true when one is set.

### `GET /api/proxies` and `GET /api/stats`

The list, and the counters, as separate documents.

### `POST /api/proxies/import`

```bash
curl -s -X POST http://127.0.0.1:8080/api/proxies/import \
  -H 'content-type: application/json' \
  -d '{"text":"http://192.0.2.10:8080\nbogus\n"}'
```

```json
{"imported": 1, "updated": 0, "errors": [{"line": 2, "message": "want host:port or host:port:user:password"}]}
```

Send `{"url":"https://example.com/proxies.txt"}` instead of `text` to fetch a list. The body is capped at 1 MiB, and redirects may not leave HTTP. A bad line is reported. The other lines are still imported.

`POST /api/proxies` with `{"proxy":"socks5://198.51.100.20:1080"}` adds one.

### `PUT /api/strategy`

```bash
curl -s -X PUT http://127.0.0.1:8080/api/strategy \
  -H 'content-type: application/json' \
  -d '{"strategy":"least-latency"}'
```

The change is live. Round-robin position and sticky keys reset.

### `POST /api/proxies/{id}/check` and `DELETE /api/proxies/{id}`

Check runs one probe and returns the row, including a failure. Demo rows are rejected. Delete answers 204.

When `server.token` is set, send `Authorization: Bearer <token>` on every `/api` call.

## Project structure

```
proxy-manager-lite/
├── cmd/harbor/                 # main
├── internal/
│   ├── health/                 # score, window, eviction
│   ├── proxy/                  # parse, pool, demo seed, file and URL import
│   ├── rotate/                 # round-robin, least-latency, sticky
│   ├── dialer/                 # HTTP CONNECT and SOCKS5
│   ├── checker/                # concurrent probes
│   ├── gateway/                # local forward proxy
│   ├── api/                    # JSON API
│   ├── dashboard/static/       # embedded page
│   ├── geo/                    # none, static file, HTTP lookup
│   └── config/                 # YAML and PROXYMAN_* 
├── configs/harbor.example.yaml
├── examples/
├── docs/images/                # screenshots in this README
├── Dockerfile
└── docker-compose.yml
```

## Testing

```bash
go vet ./...
golangci-lint run
go test -race ./...
go build -o bin/harbor ./cmd/harbor
```

GitHub Actions (`.github/workflows/ci.yml`) runs vet, golangci-lint, `go test -race`, and a build on Go 1.22 for pushes to `main` and for pull requests.

The suite covers the score (including the Ashburn 87.0 case), the eviction state machine, config parsing and environment overrides, every proxy line format, round-robin, least-latency, and sticky assignment. The gateway tests use in-process HTTP and SOCKS5 upstreams: a forwarded POST, proxy authentication, a TLS `CONNECT` tunnel, round-robin across two proxies, least-latency, sticky keys, and a dead proxy being skipped. No test dials a proxy outside the process.

## Roadmap

- A SOCKS5 listener beside the HTTP gateway.
- Remember the health window across restarts. SQLite is enough.
- A Prometheus `/metrics` endpoint.
- A health target per proxy, for pools that should not share one URL.
- mTLS when the upstream proxy itself is HTTPS.
- Reload the YAML file without a restart.

## Author

**Abdulrasheed Oseni** ([@maikama8](https://github.com/maikama8)), Zarachtech.

This repository is a public portfolio project.

## License

[MIT](LICENSE). Copyright (c) 2026 Abdulrasheed Oseni.
