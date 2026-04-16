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

The server exposes Prometheus metrics over HTTP/3 at `https://127.0.0.1:4444/metrics`.

The proxy exposes Prometheus metrics over plain HTTP at `http://127.0.0.1:9104/metrics`.

If your local `curl` build does not support HTTP/3, use the helper:

```sh
go run ./scripts/quixiotctl.go h3get \
  --url https://127.0.0.1:4444/metrics \
  --ca-file var/certs/ca.pem
```

Or use the Makefile shortcut:

```sh
make metrics
```

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
- `make run-server`: run the server with local certs
- `make run-proxy PROFILE=cellular-lte`: run the proxy
- `make run-client ROLE=mixed`: run one client through the proxy
- `make run-fleet COUNT=50 ROLE=mixed`: run a fleet through the proxy
- `make metrics`: print server and proxy metrics
- `make verify`: run `scripts/verify.sh`
- `make demo`: run `scripts/demo.sh`

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
deploy/grafana/                    optional dashboards
testdata/                          fixture area
var/{certs,uploads}/               runtime artifacts (gitignored)
```

`plan.md` is a gitignored progress tracker for the phase-by-phase implementation history.
