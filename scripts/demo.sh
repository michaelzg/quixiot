#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SESSION="${SESSION:-quixiot-demo}"
PROFILE="${PROFILE:-cellular-3g}"
COUNT="${COUNT:-10}"
ROLE="${ROLE:-mixed}"
SERVER_ADDR="${SERVER_ADDR:-127.0.0.1:4444}"
PROXY_LISTEN="${PROXY_LISTEN:-127.0.0.1:4443}"
PROXY_UPSTREAM="${PROXY_UPSTREAM:-127.0.0.1:4444}"
PROXY_METRICS_ADDR="${PROXY_METRICS_ADDR:-127.0.0.1:9104}"
CLIENT_SERVER_URL="${CLIENT_SERVER_URL:-https://127.0.0.1:4443}"
CA_FILE="${CA_FILE:-var/certs/ca.pem}"

if ! command -v tmux >/dev/null 2>&1; then
	echo "demo: tmux is required" >&2
	exit 1
fi

best_effort_udp_tune() {
	case "$(uname -s)" in
	Darwin)
		if sysctl -w kern.ipc.maxsockbuf=8388608 >/dev/null 2>&1; then
			echo "demo: set kern.ipc.maxsockbuf=8388608"
		else
			echo "demo: could not set kern.ipc.maxsockbuf automatically; run sudo sysctl -w kern.ipc.maxsockbuf=8388608" >&2
		fi
		;;
	Linux)
		if sysctl -w net.core.rmem_max=8388608 >/dev/null 2>&1 && sysctl -w net.core.wmem_max=8388608 >/dev/null 2>&1; then
			echo "demo: set net.core.rmem_max/net.core.wmem_max to 8388608"
		else
			echo "demo: could not set UDP buffer sysctls automatically; run sudo sysctl -w net.core.rmem_max=8388608 net.core.wmem_max=8388608" >&2
		fi
		;;
	esac
}

best_effort_udp_tune
make build certs

helper_bin="$ROOT/bin/quixiotctl"
go build -o "$helper_bin" ./scripts/quixiotctl.go

if tmux has-session -t "$SESSION" 2>/dev/null; then
	echo "demo: tmux session $SESSION already exists" >&2
	exit 1
fi

metrics_cmd=$(cat <<EOF
cd "$ROOT"
while true; do
  clear
  date
  echo
  echo "[server]"
  "$helper_bin" h3get --url "https://$SERVER_ADDR/metrics" --ca-file "$CA_FILE" 2>/dev/null | grep -E 'quixiot_server_connections_active|quixiot_server_pubsub_subscribers|quixiot_server_upload_duration_seconds_(count|sum)' || true
  echo
  echo "[proxy]"
  curl -fsS "http://$PROXY_METRICS_ADDR/metrics" 2>/dev/null | grep -E 'quixiot_proxy_sessions_active|quixiot_proxy_packets_total|quixiot_proxy_dropped_total' || true
  sleep 2
done
EOF
)

server_cmd="cd \"$ROOT\" && SERVER_ADDR=\"$SERVER_ADDR\" LOG_LEVEL=info make run-server"
proxy_cmd="cd \"$ROOT\" && PROXY_LISTEN=\"$PROXY_LISTEN\" PROXY_UPSTREAM=\"$PROXY_UPSTREAM\" PROXY_METRICS_ADDR=\"$PROXY_METRICS_ADDR\" PROFILE=\"$PROFILE\" LOG_LEVEL=info make run-proxy"
fleet_cmd="cd \"$ROOT\" && SERVER_URL=\"$CLIENT_SERVER_URL\" COUNT=\"$COUNT\" ROLE=\"$ROLE\" LOG_LEVEL=info make run-fleet"

tmux new-session -d -s "$SESSION" -c "$ROOT"
tmux split-window -h -t "$SESSION":0 -c "$ROOT"
tmux split-window -v -t "$SESSION":0.0 -c "$ROOT"
tmux split-window -v -t "$SESSION":0.1 -c "$ROOT"
tmux send-keys -t "$SESSION":0.0 "bash -lc $(printf '%q' "$server_cmd")" C-m
tmux send-keys -t "$SESSION":0.1 "bash -lc $(printf '%q' "$proxy_cmd")" C-m
tmux send-keys -t "$SESSION":0.2 "bash -lc $(printf '%q' "$fleet_cmd")" C-m
tmux send-keys -t "$SESSION":0.3 "bash -lc $(printf '%q' "$metrics_cmd")" C-m
tmux set-option -t "$SESSION" remain-on-exit on
tmux select-layout -t "$SESSION" tiled >/dev/null
tmux attach-session -t "$SESSION"
