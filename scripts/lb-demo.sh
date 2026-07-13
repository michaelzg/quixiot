#!/usr/bin/env bash
#
# lb-demo.sh — demonstrate quixiot-lb (the Rust load balancer in lb/).
#
# Starts N QuixIoT QUIC servers, runs quixiot-lb in front of them, pushes a
# handful of poller clients through the LB, and prints how sessions were
# distributed across backends. Then it kills one backend and shows the pool
# healing around the failure.
#
# Overrides (env): BACKENDS_COUNT, CLIENTS, STRATEGY, LB_LISTEN, LB_METRICS_ADDR,
#                  BACKEND_PORT_BASE, TRAFFIC_SECONDS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BACKENDS_COUNT="${BACKENDS_COUNT:-3}"
CLIENTS="${CLIENTS:-6}"
STRATEGY="${STRATEGY:-round-robin}"
LB_LISTEN="${LB_LISTEN:-127.0.0.1:4450}"
LB_METRICS_ADDR="${LB_METRICS_ADDR:-127.0.0.1:9106}"
BACKEND_PORT_BASE="${BACKEND_PORT_BASE:-4444}"
TRAFFIC_SECONDS="${TRAFFIC_SECONDS:-4}"

CA_FILE="${CA_FILE:-var/certs/ca.pem}"
CERT_FILE="${CERT_FILE:-var/certs/server.pem}"
KEY_FILE="${KEY_FILE:-var/certs/server.key}"
LB_BIN="${LB_BIN:-lb/target/release/quixiot-lb}"

RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/quixiot-lb-demo.XXXXXX")"
PIDS=()
CLIENT_PIDS=()

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

cleanup() {
	log "cleanup"
	for p in "${CLIENT_PIDS[@]:-}"; do kill -INT "$p" 2>/dev/null || true; done
	for p in "${PIDS[@]:-}"; do kill -INT "$p" 2>/dev/null || true; done
	sleep 1
	for p in "${PIDS[@]:-}" "${CLIENT_PIDS[@]:-}"; do kill -9 "$p" 2>/dev/null || true; done
	rm -rf "$RUN_DIR"
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }
need curl

# --- prerequisites -----------------------------------------------------------
[ -x "$LB_BIN" ] || { echo "load balancer not built; run 'make lb' first ($LB_BIN)" >&2; exit 1; }
[ -x bin/server ] || { echo "server not built; run 'make server' first" >&2; exit 1; }
[ -x bin/client ] || { echo "client not built; run 'make client' first" >&2; exit 1; }
[ -f "$CA_FILE" ] || { echo "certs missing; run 'make certs' first" >&2; exit 1; }

# --- backends ----------------------------------------------------------------
start_backend() { # port
	local port="$1" updir="$RUN_DIR/up-$1"
	mkdir -p "$updir"
	./bin/server --addr "127.0.0.1:$port" \
		--cert-file "$CERT_FILE" --key-file "$KEY_FILE" \
		--upload-dir "$updir" --metrics-plain-addr "" --log-level warn \
		>>"$RUN_DIR/server-$port.log" 2>&1 &
	BACKEND_PID="$!"
	PIDS+=("$BACKEND_PID"); disown "$BACKEND_PID" 2>/dev/null || true
	note "backend 127.0.0.1:$port (pid $BACKEND_PID)"
}

backend_list=""
first_backend_pid=""
log "starting $BACKENDS_COUNT QUIC backends"
for i in $(seq 0 $((BACKENDS_COUNT - 1))); do
	port=$((BACKEND_PORT_BASE + i))
	start_backend "$port"
	[ -z "$first_backend_pid" ] && first_backend_pid="$BACKEND_PID"
	backend_list+="127.0.0.1:$port,"
done
backend_list="${backend_list%,}"
sleep 1

# --- load balancer -----------------------------------------------------------
log "starting quixiot-lb on $LB_LISTEN | strategy=$STRATEGY"
"$LB_BIN" --listen "$LB_LISTEN" --backends "$backend_list" \
	--strategy "$STRATEGY" --metrics-addr "$LB_METRICS_ADDR" --log-level info \
	>"$RUN_DIR/lb.log" 2>&1 &
PIDS+=("$!"); disown "$!" 2>/dev/null || true
note "lb pid $! | metrics http://$LB_METRICS_ADDR/metrics"
sleep 2

metric() { curl -fsS "http://$LB_METRICS_ADDR/metrics" | grep -E "$1" || true; }

# --- traffic -----------------------------------------------------------------
log "sending $CLIENTS poller clients through the LB for ${TRAFFIC_SECONDS}s"
for i in $(seq 1 "$CLIENTS"); do
	./bin/client --server-url "https://$LB_LISTEN" --ca-file "$CA_FILE" \
		--client-id "demo-$i" --role poller --poll-interval 300ms \
		--metrics-addr "" --log-level warn \
		>"$RUN_DIR/client-$i.log" 2>&1 &
	CLIENT_PIDS+=("$!"); disown "$!" 2>/dev/null || true
done
sleep "$TRAFFIC_SECONDS"

echo
log "distribution across backends ($STRATEGY)"
metric 'quixiot_lb_backend_selected_total|quixiot_lb_backend_sessions_active|quixiot_lb_sessions_total|quixiot_lb_packets_total|quixiot_lb_quic_initials'

# --- failover ----------------------------------------------------------------
dead_port="$BACKEND_PORT_BASE"
echo
log "FAILOVER: killing backend 127.0.0.1:$dead_port (pid $first_backend_pid)"
kill -9 "$first_backend_pid" 2>/dev/null || true
note "waiting for active health probes to notice..."
sleep 5
metric 'quixiot_lb_backend_up'
note "lb log:"
grep -iE 'unhealthy|recovered|no healthy' "$RUN_DIR/lb.log" | tail -3 | sed 's/^/      /' || true

echo
log "sending 3 more clients — none should land on the dead backend"
for i in $(seq $((CLIENTS + 1)) $((CLIENTS + 3))); do
	./bin/client --server-url "https://$LB_LISTEN" --ca-file "$CA_FILE" \
		--client-id "demo-$i" --role poller --poll-interval 300ms \
		--metrics-addr "" --log-level warn \
		>"$RUN_DIR/client-$i.log" 2>&1 &
	CLIENT_PIDS+=("$!"); disown "$!" 2>/dev/null || true
done
sleep "$TRAFFIC_SECONDS"
metric 'quixiot_lb_backend_selected_total|quixiot_lb_backend_up'

# --- recovery ------------------------------------------------------------------
echo
log "RECOVERY: restarting backend 127.0.0.1:$dead_port"
start_backend "$dead_port"
note "waiting for a health probe to succeed..."
sleep 4
metric 'quixiot_lb_backend_up'
note "lb log:"
grep -iE 'recovered' "$RUN_DIR/lb.log" | tail -2 | sed 's/^/      /' || true

echo
log "sending 3 more clients — the recovered backend is back in rotation"
for i in $(seq $((CLIENTS + 4)) $((CLIENTS + 6))); do
	./bin/client --server-url "https://$LB_LISTEN" --ca-file "$CA_FILE" \
		--client-id "demo-$i" --role poller --poll-interval 300ms \
		--metrics-addr "" --log-level warn \
		>"$RUN_DIR/client-$i.log" 2>&1 &
	CLIENT_PIDS+=("$!"); disown "$!" 2>/dev/null || true
done
sleep "$TRAFFIC_SECONDS"
metric 'quixiot_lb_backend_selected_total|quixiot_lb_backend_up'

echo
log "done — failover: dead backend's selected_total went flat; recovery: it rejoined and grew again"
