# quixiot-lb — a Rust load balancer for the QuixIoT QUIC servers

An **L4 (UDP) load balancer** that sits in front of one or more QuixIoT
HTTP/3 + QUIC servers, written in Rust as an educational exercise. It mirrors the
Go impairment proxy in [`internal/proxy`](../internal/proxy/proxy.go) closely
enough that the two are worth reading side by side — same session-per-client
forwarding model, different language, different concurrency primitives.

```
                                        ┌────────────────► server :4444
   clients ──UDP──►  quixiot-lb :4450 ──┼────────────────► server :4445
   (QUIC/HTTP3)      (this crate)       └────────────────► server :4446
                          │
                          └── /metrics :9106 (Prometheus text)
```

It does **not** terminate TLS. QUIC's handshake stays end-to-end between the
client and whichever backend it lands on, so the load balancer never sees
plaintext and needs no keys — it just moves UDP datagrams and keeps each client
pinned to one backend.

## Why this is a good "strengths of Rust" exercise

| Concern | How Rust handles it here | Where to look |
| --- | --- | --- |
| **Shared mutable state across tasks** | The session table is `Arc<Mutex<HashMap>>`; every `Backend`'s live state is atomics. The compiler auto-derives `Send + Sync` only because every field qualifies — a data race is a *compile error*, not a code-review catch. | [`balancer.rs`](src/balancer.rs), [`backend.rs`](src/backend.rs) |
| **"Who frees the session?"** | Answered by ownership + one `AtomicBool` CAS (`finish`), instead of Go's `sync.Once` + channel-close dance. The return task holds an `Arc<Session>`, so the upstream socket lives exactly as long as something needs it. | [`balancer.rs`](src/balancer.rs) |
| **Closed set of choices** | Strategies and QUIC packet types are `enum`s matched exhaustively — add a variant and the compiler makes you handle it everywhere. | [`strategy.rs`](src/strategy.rs), [`quic.rs`](src/quic.rs) |
| **"No healthy backend" is a real state** | `select` returns `Option<Arc<Backend>>`; the caller *must* handle `None`. There is no null to forget. | [`strategy.rs`](src/strategy.rs) |
| **Parsing untrusted bytes** | The QUIC header parser reads every field through `slice.get(range)?`. A truncated or hostile datagram yields `None` — no panic, no out-of-bounds read, zero `unsafe`. | [`quic.rs`](src/quic.rs) |
| **Fallible setup** | `Result<_, String>` + `?` thread errors up to `main`, which turns them into an `ExitCode`. Nothing half-initializes. | [`config.rs`](src/config.rs), [`main.rs`](src/main.rs) |
| **Async I/O without callbacks** | `tokio::select!` expresses "reply, or cancellation, whichever comes first"; `tokio::time::timeout` turns "no health reply in 500 ms" into an ordinary `Result` branch. | [`balancer.rs`](src/balancer.rs), [`health.rs`](src/health.rs) |
| **Drop-not-block under load** | The hot path uses `try_send`, never `send().await`: a full socket buffer drops that one datagram (counted in metrics — UDP semantics, QUIC recovers) instead of parking the accept loop and stalling *every* client. Subtlety: tokio's `try_*` consults the reactor's *cached* readiness, so each socket's writability is primed once with `writable().await` — without that, the first `try_send` on a fresh socket spuriously reports `WouldBlock`. The unit tests caught this; live QUIC traffic had masked it because clients retransmit. | [`balancer.rs`](src/balancer.rs) |
| **Small dependency surface** | `tokio` plus `socket2` (only for SO_RCVBUF/SO_SNDBUF, which std doesn't expose). The RNG (xorshift), the hash (FNV-1a), the Prometheus text exposition, and the HTTP endpoint are all hand-rolled std, so the language stays in view. | everywhere |

## Design

**Sticky sessions by client 4-tuple.** The first datagram from a new client
address picks a backend (via the strategy), opens a dedicated upstream UDP
socket `connect`ed to that backend, and spawns a *return task* that pumps replies
back. Every later datagram from that client reuses the same session. This is what
keeps all of a QUIC connection's packets on one backend — a hard requirement,
since a different backend has no idea about the connection.

**Two-sided health checking.**
- *Passive* — an upstream socket is `connect`ed, so a dead backend surfaces
  `ECONNREFUSED` (from ICMP port-unreachable) on the very next send/recv. That
  marks the backend down instantly.
- *Active* — a periodic **QUIC Version Negotiation** probe: we send a
  long-header packet carrying a deliberately unsupported version, and per
  RFC 9000 §6.1 a live server *must* answer with a Version Negotiation packet.
  Silence for N probes marks it down; a reply brings it back. This is what lets a
  recovered backend rejoin the pool (passive detection alone can only find
  failures). Verified to work against `quic-go`.

**Strategies** (`--strategy`): `round-robin`, `least-conn`, `random`, `ip-hash`.
All operate only on the *healthy* subset, so a down backend never receives a new
session. (Note: `ip-hash` keys on the client IP, so an all-localhost demo maps
every client to one backend — it only spreads load when clients have distinct IPs.)

**Kernel buffer sizing.** Every UDP socket asks for 8 MiB send/recv buffers
(matching the Go proxy's `ReadBufferSize`) via `socket2`, halving until the
kernel accepts — this repo measures drop rates, so an LB with default-sized
buffers would skew any comparison run through it. If macOS caps the request
(`kern.ipc.maxsockbuf`), the LB logs what it actually got and keeps going,
where the Go proxy hard-fails.

### Known limitation: connection migration

Because sessions are keyed on the client's `ip:port`, a QUIC **connection
migration** (client changes source address/port mid-connection) would hash to a
new session and possibly a new backend, breaking that connection. Real
migration-safe QUIC load balancing needs the server to encode a routing token in
its Connection IDs (the QUIC-LB draft). For the local QuixIoT fleet, clients
don't migrate, so 4-tuple stickiness is correct and simple — the same choice the
Go proxy makes. The header parser in [`quic.rs`](src/quic.rs) is the natural
starting point if you want to explore CID-based routing.

## Build & test

```sh
cargo build --release        # -> target/release/quixiot-lb
cargo test                   # strategy math, QUIC parsing, config, metrics, health probes,
                             # and end-to-end forwarding/stickiness through real UDP sockets
```

Or via the repo Makefile: `make lb`, `make lb-test`.

## Run

Point it at any number of running QuixIoT servers:

```sh
quixiot-lb \
  --listen   127.0.0.1:4450 \
  --backends 127.0.0.1:4444,127.0.0.1:4445,127.0.0.1:4446 \
  --strategy round-robin
```

Then send a client at the LB instead of a server:

```sh
SERVER_URL=https://127.0.0.1:4450 ROLE=poller make run-client
```

`--help` lists every flag (strategy, idle timeout, health-check tuning, metrics
address, log level).

## One-command demo

```sh
make lb-demo          # 3 servers + LB: distribution, failover, AND recovery
```

This starts three servers, runs the LB in front, pushes a handful of poller
clients through it, prints the distribution from `/metrics`, kills a backend and
shows the pool healing around it, then **restarts** the dead backend and shows
the health probes bringing it back into rotation. See
[`scripts/lb-demo.sh`](../scripts/lb-demo.sh).

## Metrics

`GET http://127.0.0.1:9106/metrics` (Prometheus text). Slots in beside the
existing exporters — server `:9103`, proxy `:9104`, client `:9105`:

| Metric | Meaning |
| --- | --- |
| `quixiot_lb_packets_total{direction}` / `quixiot_lb_bytes_total{direction}` | datagrams / bytes accepted for forwarding |
| `quixiot_lb_packets_dropped_total{direction}` | datagrams dropped on a full socket buffer (drop-not-block) |
| `quixiot_lb_sessions_active` / `quixiot_lb_sessions_total` | live and cumulative sessions |
| `quixiot_lb_sessions_rejected_total` | new sessions dropped (no healthy backend) |
| `quixiot_lb_quic_initials_total` | QUIC Initial packets observed (new-connection attempts) |
| `quixiot_lb_backend_up{backend}` | 1 healthy / 0 down |
| `quixiot_lb_backend_sessions_active{backend}` | live sessions per backend |
| `quixiot_lb_backend_selected_total{backend}` | times chosen for a new session |
| `quixiot_lb_strategy_info{strategy}` | active strategy (as a label) |

## Layout

```
src/main.rs        wiring: parse config, bind, spawn balancer/health/metrics, handle signals
src/config.rs      hand-rolled CLI parser -> Config (Result + ?)
src/strategy.rs    Strategy enum + Selector (round-robin / least-conn / random / ip-hash)
src/backend.rs     one upstream server's shared, atomic live state
src/balancer.rs    the forwarding core: listen socket, session table, return tasks, sweeper
src/health.rs      active QUIC Version-Negotiation probes (one reused socket per backend)
src/net.rs         UDP socket construction with sized kernel buffers (socket2)
src/quic.rs        bounds-checked QUIC header parse (observability only)
src/metrics.rs     atomic counters + Prometheus text + a tiny HTTP endpoint
src/log.rs         minimal leveled logger (no log/tracing/chrono)
```
