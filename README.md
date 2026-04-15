# quixiot

HTTP/3 + QUIC IoT proof of concept.

Three use cases over a deliberately lossy UDP link:

1. **Real-time pubsub** over WebTransport (streams for commands, datagrams for telemetry)
2. **File upload** over HTTP/3 streaming POST
3. **Plain HTTP/3 GET** for server state and per-client config

All three share one QUIC connection per client. A custom UDP proxy sits in the middle and applies configurable drop/dup/reorder/latency+jitter/bandwidth impairments per direction.

## Status

Under active construction — see `plan.md` for phase progress.

## Layout

```
cmd/{server,client,proxy,fleet}    # binaries
internal/                           # implementation packages
configs/                            # server/client/proxy YAMLs
scripts/                            # demo.sh, verify.sh
deploy/grafana/                     # dashboard JSON
testdata/                           # upload fixtures
var/{certs,uploads}/                # runtime artifacts (gitignored)
```

## Quick build

```sh
make build     # -> ./bin/{server,client,proxy,fleet}
make test
```

Full run instructions land in Phase 10.
