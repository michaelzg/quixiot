#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DURATION_SECONDS="${DURATION_SECONDS:-30}"
PROFILES_RAW="${PROFILES:-wifi-good cellular-lte cellular-3g satellite flaky}"
UPLOAD_SIZE="${UPLOAD_SIZE:-4096}"
UPLOAD_INTERVAL="${UPLOAD_INTERVAL:-5s}"
POLL_INTERVAL="${POLL_INTERVAL:-3s}"
TELEMETRY_INTERVAL="${TELEMETRY_INTERVAL:-1s}"
COMMAND_INTERVAL="${COMMAND_INTERVAL:-4s}"

SERVER_ADDR="${SERVER_ADDR:-127.0.0.1:4444}"
PROXY_LISTEN="${PROXY_LISTEN:-127.0.0.1:4443}"
PROXY_UPSTREAM="${PROXY_UPSTREAM:-127.0.0.1:4444}"
PROXY_METRICS_ADDR="${PROXY_METRICS_ADDR:-127.0.0.1:9104}"
CLIENT_METRICS_ADDR="${CLIENT_METRICS_ADDR:-127.0.0.1:9105}"
CA_FILE="${CA_FILE:-var/certs/ca.pem}"

IFS=' ' read -r -a PROFILES <<< "$PROFILES_RAW"

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "verify: required command not found: $1" >&2
		exit 1
	fi
}

sha256_file() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
		return
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
		return
	fi
	echo "verify: need shasum or sha256sum" >&2
	exit 1
}

metric_value() {
	local file="$1"
	local name="$2"
	awk -v metric="$name" '
	$1 == metric { print $2; found=1; exit }
	END { if (!found) exit 1 }
	' "$file"
}

metric_labeled_value_or_zero() {
	local file="$1"
	local name="$2"
	local direction="$3"
	awk -v metric="$name" -v direction="$direction" '
	$1 ~ "^" metric "\\{" && $1 ~ ("direction=\"" direction "\"") { print $2; found=1; exit }
	END { if (!found) print 0 }
	' "$file"
}

profile_drop_probability() {
	local profile="$1"
	local section="$2"
	local config_file="configs/proxy-$profile.yaml"
	awk -v section="$section" '
	$1 == section ":" { in_section=1; next }
	$1 ~ /^[A-Za-z0-9_-]+:/ && $1 != section ":" { in_section=0 }
	in_section && $1 == "drop_probability:" { print $2; exit }
	' "$config_file"
}

helper_dir="$(mktemp -d "${TMPDIR:-/tmp}/quixiot-verify.XXXXXX")"
helper_bin="$helper_dir/quixiotctl"
run_root="$helper_dir/runs"
mkdir -p "$run_root"

server_pid=""
proxy_pid=""
client_pid=""

stop_processes() {
	local pid
	for pid in "$client_pid" "$proxy_pid" "$server_pid"; do
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	client_pid=""
	proxy_pid=""
	server_pid=""
}

cleanup() {
	stop_processes
	rm -rf "$helper_dir"
}
trap cleanup EXIT INT TERM

need_cmd go
need_cmd curl
need_cmd awk

make build certs >/dev/null
go build -o "$helper_bin" ./scripts/quixiotctl.go

wait_for_http() {
	local url="$1"
	local attempts="${2:-80}"
	local i
	for ((i = 0; i < attempts; i++)); do
		if curl -fsS "$url" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.25
	done
	return 1
}

wait_for_h3() {
	local url="$1"
	local attempts="${2:-80}"
	local i
	for ((i = 0; i < attempts; i++)); do
		if "$helper_bin" h3get --url "$url" --ca-file "$CA_FILE" --timeout 2s >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.25
	done
	return 1
}

start_server() {
	local upload_dir="$1"
	local log_file="$2"
	./bin/server \
		--addr "$SERVER_ADDR" \
		--cert-file "var/certs/server.pem" \
		--key-file "var/certs/server.key" \
		--upload-dir "$upload_dir" \
		--log-level warn >"$log_file" 2>&1 &
	server_pid=$!
	wait_for_h3 "https://$SERVER_ADDR/state" 120
}

start_proxy() {
	local profile="$1"
	local log_file="$2"
	./bin/proxy \
		--listen "$PROXY_LISTEN" \
		--upstream "$PROXY_UPSTREAM" \
		--profile "$profile" \
		--metrics-addr "$PROXY_METRICS_ADDR" \
		--log-level warn >"$log_file" 2>&1 &
	proxy_pid=$!
	wait_for_http "http://$PROXY_METRICS_ADDR/metrics" 120
}

start_client() {
	local profile="$1"
	local log_file="$2"
	local client_id="verify-$profile"
	./bin/client \
		--server-url "https://$PROXY_LISTEN" \
		--ca-file "$CA_FILE" \
		--client-id "$client_id" \
		--role mixed \
		--metrics-addr "$CLIENT_METRICS_ADDR" \
		--upload-size "$UPLOAD_SIZE" \
		--upload-interval "$UPLOAD_INTERVAL" \
		--poll-interval "$POLL_INTERVAL" \
		--telemetry-interval "$TELEMETRY_INTERVAL" \
		--command-interval "$COMMAND_INTERVAL" \
		--log-level warn >"$log_file" 2>&1 &
	client_pid=$!
	wait_for_http "http://$CLIENT_METRICS_ADDR/metrics" 120
}

assert_drop_rate() {
	local profile="$1"
	local direction="$2"
	local metrics_file="$3"
	local expected packets dropped observed bounds within

	expected="$(profile_drop_probability "$profile" "$direction")"
	packets="$(metric_labeled_value_or_zero "$metrics_file" quixiot_proxy_packets_total "$direction")"
	dropped="$(metric_labeled_value_or_zero "$metrics_file" quixiot_proxy_dropped_total "$direction")"

	within="$(awk -v p="$expected" -v n="$packets" -v d="$dropped" '
	BEGIN {
		if (n <= 0) {
			print 0
			exit
		}
		obs = d / n
		sigma = sqrt((p * (1 - p)) / n)
		low = p - sigma
		if (low < 0) low = 0
		high = p + sigma
		if (obs >= low && obs <= high) print 1
		else print 0
	}
	')"

	if [[ "$within" != "1" ]]; then
		observed="$(awk -v n="$packets" -v d="$dropped" 'BEGIN { if (n <= 0) print 0; else print d / n }')"
		bounds="$(awk -v p="$expected" -v n="$packets" '
		BEGIN {
			if (n <= 0) {
				print "no-packets"
				exit
			}
			sigma = sqrt((p * (1 - p)) / n)
			low = p - sigma
			if (low < 0) low = 0
			high = p + sigma
			printf "[%.6f, %.6f]", low, high
		}
		')"
		echo "verify: drop-rate assertion failed for $profile $direction: observed=$observed expected=$expected bounds=$bounds packets=$packets dropped=$dropped" >&2
		return 1
	fi
}

verify_upload_hashes() {
	local profile="$1"
	local upload_dir="$2"
	local client_prefix="verify-$profile"
	local total=0
	local matched=0
	local path base stem seq seed expected actual

	while IFS= read -r path; do
		[[ -z "$path" ]] && continue
		base="$(basename "$path")"
		stem="${base%.bin}"
		seq="${stem##*-}"
		seed=$((10#$seq + 1))
		expected="$("$helper_bin" expected-upload-sha --size "$UPLOAD_SIZE" --seed "$seed")"
		actual="$(sha256_file "$path")"
		total=$((total + 1))
		if [[ "$actual" == "$expected" ]]; then
			matched=$((matched + 1))
		else
			echo "verify: upload hash mismatch for $base: expected=$expected actual=$actual" >&2
			return 1
		fi
	done < <(find "$upload_dir" -type f -name "${client_prefix}-*.bin" | sort)

	if [[ "$total" -eq 0 ]]; then
		echo "verify: no uploads were produced for $profile" >&2
		return 1
	fi
	if [[ "$matched" -ne "$total" ]]; then
		echo "verify: upload hash match rate below 100% for $profile ($matched/$total)" >&2
		return 1
	fi
	echo "$matched/$total"
}

printf "%-14s %-12s %-12s %-14s\n" "profile" "reconnects" "drops" "upload_sha"

for profile in "${PROFILES[@]}"; do
	stop_processes

	run_dir="$run_root/$profile"
	upload_dir="$run_dir/uploads"
	mkdir -p "$upload_dir"

	start_server "$upload_dir" "$run_dir/server.log"
	start_proxy "$profile" "$run_dir/proxy.log"
	start_client "$profile" "$run_dir/client.log"

	sleep "$DURATION_SECONDS"

	curl -fsS "http://$PROXY_METRICS_ADDR/metrics" >"$run_dir/proxy.metrics"
	curl -fsS "http://$CLIENT_METRICS_ADDR/metrics" >"$run_dir/client.metrics"

	reconnects="$(metric_value "$run_dir/client.metrics" quixiot_client_reconnects_total)"
	if ! awk -v reconnects="$reconnects" 'BEGIN { exit !(reconnects <= 0.5) }'; then
		echo "verify: reconnect counter too high for $profile: $reconnects" >&2
		exit 1
	fi

	assert_drop_rate "$profile" "to_server" "$run_dir/proxy.metrics"
	assert_drop_rate "$profile" "to_client" "$run_dir/proxy.metrics"
	upload_result="$(verify_upload_hashes "$profile" "$upload_dir")"

	printf "%-14s %-12s %-12s %-14s\n" "$profile" "$reconnects" "ok" "$upload_result"
done

echo
echo "verify: all profiles passed"
