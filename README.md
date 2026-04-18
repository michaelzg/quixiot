# quixiot

QuixIoT is a Go proof of concept for running a realistic IoT workload over HTTP/3 + QUIC across a deliberately lossy UDP link.

It exercises three paths on one transport stack:

1. WebTransport pubsub for telemetry datagrams and command streams
2. HTTP/3 streaming uploads with SHA-256 verification
3. HTTP/3 polling for `/state` and `/config/{clientID}`

The repo also includes a profile-driven UDP proxy, Prometheus metrics, a fleet launcher, and demo / verification scripts.

## Requirements

- Go 1.26.2 (`toolchain go1.26.2` is set in `go.mod`)
- `tmux` for `scripts/demo.sh`
- `curl` for proxy/client metrics scraping in `scripts/verify.sh`
- `shasum` or `sha256sum` for upload verification

For larger local runs, increasing the UDP socket buffer helps:

```sh
# macOS
sudo sysctl -w kern.ipc.maxsockbuf=8388608

# Linux
sudo sysctl -w net.core.rmem_max=8388608
sudo sysctl -w net.core.wmem_max=8388608
```

The demo script attempts a best-effort sysctl update and prints a reminder if it cannot apply it.

## Quick Start

Build everything and generate local certs:

```sh
make build
make certs
```

Run the server directly:

```sh
make run-server
```

In another terminal, talk to it without the proxy:

```sh
SERVER_URL=https://127.0.0.1:4444 ROLE=poller make run-client
```

Run through the impairment proxy:

```sh
make run-server
PROFILE=wifi-good make run-proxy
ROLE=mixed make run-client
```

Launch a small fleet through the proxy:

```sh
make run-server
PROFILE=cellular-3g make run-proxy
COUNT=10 ROLE=mixed make run-fleet
```

## Metrics

Every binary exposes Prometheus metrics on a plain HTTP endpoint so a single Prometheus instance can scrape the whole stack:

| Component | Default address | Flag |
| --- | --- | --- |
| server (mirror of the in-band `/metrics`) | `127.0.0.1:9103` | `--metrics-plain-addr` |
| proxy | `127.0.0.1:9104` | `--metrics-addr` |
| client (standalone) | none unless set | `--metrics-addr` |
| fleet child `i` | `127.0.0.1:9200+i` | `--metrics-port-base` |

The server *also* serves the same registry over HTTP/3 at `https://127.0.0.1:4444/metrics` for in-band debugging. The plain endpoint exists because Prometheus does not speak HTTP/3.

If your local `curl` build does not support HTTP/3, use the helper or the Makefile shortcut:

```sh
go run ./scripts/quixiotctl.go h3get \
  --url https://127.0.0.1:4444/metrics \
  --ca-file var/certs/ca.pem

make metrics
```

## Grafana Dashboard

A docker-compose stack ships Prometheus + Grafana with the QuixIoT dashboard pre-provisioned. Components run on the host (so they keep their HTTP/3 sockets) and Prometheus reaches them through `host.docker.internal`.

Start the observability stack (containers):

```sh
make grafana                    # default 1 day retention
PROM_RETENTION=24h make grafana # override (e.g. 24h, 7d)
```

Start the workload (quixiot binaries, background):

```sh
make up                              # server + cellular-3g proxy + 10 mixed clients
PROFILE=flaky COUNT=20 make up       # override profile / size
make status                          # RUNNING / stopped per component
make logs                            # tail -F server + proxy + fleet
make restart PROFILE=satellite       # swap profiles without losing Prometheus history
make down                            # SIGINT everything
```

Prefer to run a single component in the foreground (e.g. to watch server logs inline)? The existing `run-server`, `run-proxy`, `run-client`, `run-fleet` targets still work.

Open Grafana at <http://127.0.0.1:3000/d/quixiot-overview> (admin / admin; anonymous viewer is also enabled). The dashboard has six rows:

1. **At a glance** — active QUIC connections, pubsub subscribers, proxy sessions, reconnects, drop rate, throughput
2. **Network impairment** — drop rate by direction, enforced delay heatmap, packet rates, drops/dupes/reorders
3. **QUIC resilience** — handshake duration heatmap, RTT per client, cumulative handshakes vs reconnects
4. **WebTransport pubsub** — datagrams in/out by topic, datagram round-trip loss, publish-to-receive latency p50/p95/p99
5. **HTTP/3 workload** — request rate by path, latency percentiles, upload bytes/sec, upload duration heatmap
6. **Proxy detail** — bytes/sec by direction, sessions over time, enforced delay percentiles

Variables: `client_id`, `topic`, and `direction` filter every panel.

Tear it down (data volumes survive — drop them with `docker volume rm deploy_prom-data deploy_grafana-data` if you want a clean slate):

```sh
make grafana-down
make grafana-logs    # tail compose logs
make grafana-status  # ps for the two containers
```

Prometheus targets page: <http://127.0.0.1:9090/targets>. The fleet rewrites `deploy/targets/clients.json` on every start, and Prometheus's file_sd refreshes it every 5 s — so per-client breakdowns appear within a few seconds of `make run-fleet`.

## Demo And Verification

Launch the four-pane tmux demo:

```sh
./scripts/demo.sh
```

By default it starts:

- server on `127.0.0.1:4444`
- proxy on `127.0.0.1:4443` with `cellular-3g`
- fleet with `10` mixed-role clients
- a live metrics pane for server and proxy counters

Run the scripted verification sweep:

```sh
./scripts/verify.sh
```

Defaults:

- 30 seconds per profile
- profiles: `wifi-good`, `cellular-lte`, `cellular-3g`, `satellite`, `flaky`
- one mixed-role client through the proxy with client metrics enabled

Useful overrides:

```sh
DURATION_SECONDS=10 PROFILES="wifi-good flaky" ./scripts/verify.sh
COUNT=25 PROFILE=flaky ./scripts/demo.sh
```

## Make Targets

- `make build`: build `bin/server`, `bin/client`, `bin/proxy`, `bin/fleet`
- `make certs`: generate the local CA and server leaf
- `make run-server`: run the server with local certs (plain metrics on `127.0.0.1:9103`)
- `make run-proxy PROFILE=cellular-lte`: run the proxy (metrics on `127.0.0.1:9104`)
- `make run-client ROLE=mixed`: run one client through the proxy (set `CLIENT_METRICS_ADDR=` empty to disable scraping)
- `make run-fleet COUNT=50 ROLE=mixed`: run a fleet through the proxy (per-child metrics from `127.0.0.1:9200`+)
- `make metrics`: print server and proxy metrics
- `make verify`: run `scripts/verify.sh`
- `make demo`: run `scripts/demo.sh`
- `make grafana`: start the docker-compose Prometheus + Grafana stack (`PROM_RETENTION=1d` default)
- `make grafana-down`: stop the observability stack
- `make grafana-logs` / `make grafana-status`: tail logs / show container status
- `make up`: start server + proxy (`PROFILE`) + fleet (`COUNT` × `ROLE`) in the background
- `make down`: stop the background workload
- `make restart`: down, then up (useful for swapping `PROFILE`)
- `make status` / `make logs`: component state / `tail -F` over all three logs

## Expected Behavior

| Profile | What You Should See |
| --- | --- |
| `wifi-good` | Low latency, almost no drops, fast uploads, stable pubsub |
| `cellular-lte` | Mild latency and occasional drops, but steady uploads and polling |
| `cellular-3g` | Noticeable latency and slower uploads, pubsub still keeps flowing |
| `satellite` | High RTT dominates request latency, but connections stay established |
| `flaky` | Most stressful profile; retries and loss recovery are visible, but the workload still completes |

The verification script checks:

- client reconnects stay near zero
- proxy drop rate tracks the configured profile within roughly one standard deviation
- every uploaded file matches the deterministic SHA-256 the client intended to send

## Repo Layout

```text
cmd/{server,client,proxy,fleet}    binaries
internal/                          implementation packages
configs/                           proxy impairment profiles
scripts/                           demo, verify, and helper tooling
deploy/                            docker-compose, prometheus.yml, Grafana provisioning + dashboards
testdata/                          fixture area
var/{certs,uploads}/               runtime artifacts (gitignored)
```

`plan.md` is a gitignored progress tracker for the phase-by-phase implementation history.
