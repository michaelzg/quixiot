GO ?= go
BIN ?= ./bin
CMDS := server client proxy fleet

CERT_DIR ?= var/certs
CA_FILE ?= $(CERT_DIR)/ca.pem
CERT_FILE ?= $(CERT_DIR)/server.pem
KEY_FILE ?= $(CERT_DIR)/server.key
UPLOAD_DIR ?= var/uploads

SERVER_ADDR ?= 127.0.0.1:4444
SERVER_METRICS_PLAIN_ADDR ?= 127.0.0.1:9103
PROXY_LISTEN ?= 127.0.0.1:4443
PROXY_UPSTREAM ?= 127.0.0.1:4444
PROXY_METRICS_ADDR ?= 127.0.0.1:9104
CLIENT_METRICS_ADDR ?= 127.0.0.1:9105

SERVER_URL ?= https://127.0.0.1:4443
CLIENT_ID ?= client-local
COUNT ?= 10
ROLE ?=
PROFILE ?= passthrough
LOG_LEVEL ?= info

FLEET_METRICS_PORT_BASE ?= 9200
# Advertised host for per-child metrics in deploy/targets/clients.json.
# host.docker.internal lets a dockerized Prometheus (see `make grafana`)
# reach the fleet on the host. Override to 127.0.0.1 for host-only scraping.
FLEET_METRICS_HOST ?= host.docker.internal
FLEET_TARGETS_FILE ?= deploy/targets/clients.json

STACK_RUN_DIR ?= var/run
STACK_LOG_DIR ?= var/logs

CLIENT_ROLE = $(or $(ROLE),poller)
FLEET_ROLE = $(or $(ROLE),mixed)
SERVER_METRICS_URL ?= https://127.0.0.1:4444/metrics

COMPOSE ?= docker compose
COMPOSE_FILE ?= deploy/docker-compose.yml
PROM_PORT ?= 9090
GF_PORT ?= 3000
PROM_RETENTION ?= 1d

.PHONY: all build $(CMDS) test vet fmt tidy clean help certs run-server run-proxy run-client run-fleet demo verify metrics grafana grafana-down grafana-logs grafana-status observability observability-down up down restart status logs

all: build

build: $(CMDS)

$(CMDS):
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/$@ ./cmd/$@

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN)

certs: server
	@mkdir -p $(CERT_DIR)
	$(BIN)/server --gen-certs --cert-dir $(CERT_DIR)

run-server: server certs
	$(BIN)/server \
		--addr $(SERVER_ADDR) \
		--cert-file $(CERT_FILE) \
		--key-file $(KEY_FILE) \
		--upload-dir $(UPLOAD_DIR) \
		--metrics-plain-addr $(SERVER_METRICS_PLAIN_ADDR) \
		--log-level $(LOG_LEVEL)

run-proxy: proxy
	$(BIN)/proxy \
		--listen $(PROXY_LISTEN) \
		--upstream $(PROXY_UPSTREAM) \
		--profile $(PROFILE) \
		--metrics-addr $(PROXY_METRICS_ADDR) \
		--log-level $(LOG_LEVEL)

run-client: client certs
	$(BIN)/client \
		--server-url $(SERVER_URL) \
		--ca-file $(CA_FILE) \
		--client-id $(CLIENT_ID) \
		--role $(CLIENT_ROLE) \
		--metrics-addr "$(CLIENT_METRICS_ADDR)" \
		--log-level $(LOG_LEVEL)

run-fleet: fleet client certs
	$(BIN)/fleet \
		--client-bin $(BIN)/client \
		--server-url $(SERVER_URL) \
		--ca-file $(CA_FILE) \
		--count $(COUNT) \
		--role $(FLEET_ROLE) \
		--metrics-port-base $(FLEET_METRICS_PORT_BASE) \
		--metrics-host $(FLEET_METRICS_HOST) \
		--targets-file $(FLEET_TARGETS_FILE) \
		--log-level $(LOG_LEVEL)

demo: build certs
	./scripts/demo.sh

verify: build certs
	./scripts/verify.sh

metrics: certs
	@echo "[server]"
	$(GO) run ./scripts/quixiotctl.go h3get --url $(SERVER_METRICS_URL) --ca-file $(CA_FILE)
	@echo
	@echo "[proxy]"
	curl -fsS http://$(PROXY_METRICS_ADDR)/metrics

# --- Observability stack (Prometheus + Grafana via docker compose) ---

# Bring up the Grafana + Prometheus stack. Components keep running on the host
# so Prometheus reaches them via host.docker.internal:<port>. Override
# PROM_RETENTION (default 1d) to extend history (e.g. PROM_RETENTION=24h, 7d).
grafana:
	@mkdir -p deploy/targets
	@if [ ! -f $(FLEET_TARGETS_FILE) ]; then echo '[]' > $(FLEET_TARGETS_FILE); fi
	PROM_PORT=$(PROM_PORT) GF_PORT=$(GF_PORT) PROM_RETENTION=$(PROM_RETENTION) \
		$(COMPOSE) -f $(COMPOSE_FILE) up -d
	@echo
	@echo "Grafana:    http://127.0.0.1:$(GF_PORT)/d/quixiot/quixiot-overview (admin/admin)"
	@echo "Prometheus: http://127.0.0.1:$(PROM_PORT)/targets"
	@echo "Retention:  $(PROM_RETENTION)  (override with PROM_RETENTION=...)"

grafana-down:
	$(COMPOSE) -f $(COMPOSE_FILE) down

grafana-logs:
	$(COMPOSE) -f $(COMPOSE_FILE) logs -f --tail=100

grafana-status:
	$(COMPOSE) -f $(COMPOSE_FILE) ps

# Convenience: alias bundle for the full observability stack.
observability: grafana
observability-down: grafana-down

# --- QuixIoT workload (server + proxy + fleet, background) ---

# Bring up the workload in the background: server, proxy on PROFILE, fleet
# of COUNT x FLEET_ROLE. PIDs land in $(STACK_RUN_DIR); logs in $(STACK_LOG_DIR).
# Pairs with `make grafana` — the Grafana dashboard renders live once both are up.
# Overrides: PROFILE, COUNT, ROLE, LOG_LEVEL, SERVER_ADDR, PROXY_LISTEN, etc.
up: build certs
	@mkdir -p $(STACK_RUN_DIR) $(STACK_LOG_DIR) $(UPLOAD_DIR)
	@if [ -f $(STACK_RUN_DIR)/server.pid ] && kill -0 $$(cat $(STACK_RUN_DIR)/server.pid) 2>/dev/null; then \
		echo "up: server already running (pid $$(cat $(STACK_RUN_DIR)/server.pid)); run 'make down' first" >&2; \
		exit 1; \
	fi
	@rm -f $(STACK_RUN_DIR)/server.pid $(STACK_RUN_DIR)/proxy.pid $(STACK_RUN_DIR)/fleet.pid
	@$(BIN)/server \
		--addr $(SERVER_ADDR) \
		--cert-file $(CERT_FILE) \
		--key-file $(KEY_FILE) \
		--upload-dir $(UPLOAD_DIR) \
		--metrics-plain-addr $(SERVER_METRICS_PLAIN_ADDR) \
		--log-level $(LOG_LEVEL) \
		>$(STACK_LOG_DIR)/server.log 2>&1 & echo $$! > $(STACK_RUN_DIR)/server.pid
	@sleep 1
	@$(BIN)/proxy \
		--listen $(PROXY_LISTEN) \
		--upstream $(PROXY_UPSTREAM) \
		--profile $(PROFILE) \
		--metrics-addr $(PROXY_METRICS_ADDR) \
		--log-level $(LOG_LEVEL) \
		>$(STACK_LOG_DIR)/proxy.log 2>&1 & echo $$! > $(STACK_RUN_DIR)/proxy.pid
	@sleep 1
	@$(BIN)/fleet \
		--client-bin $(BIN)/client \
		--server-url $(SERVER_URL) \
		--ca-file $(CA_FILE) \
		--count $(COUNT) \
		--role $(FLEET_ROLE) \
		--metrics-port-base $(FLEET_METRICS_PORT_BASE) \
		--metrics-host $(FLEET_METRICS_HOST) \
		--targets-file $(FLEET_TARGETS_FILE) \
		--log-level $(LOG_LEVEL) \
		>$(STACK_LOG_DIR)/fleet.log 2>&1 & echo $$! > $(STACK_RUN_DIR)/fleet.pid
	@sleep 2
	@echo "up: profile=$(PROFILE) count=$(COUNT) role=$(FLEET_ROLE)"
	@echo "  server pid: $$(cat $(STACK_RUN_DIR)/server.pid)   log: $(STACK_LOG_DIR)/server.log"
	@echo "  proxy  pid: $$(cat $(STACK_RUN_DIR)/proxy.pid)   log: $(STACK_LOG_DIR)/proxy.log"
	@echo "  fleet  pid: $$(cat $(STACK_RUN_DIR)/fleet.pid)   log: $(STACK_LOG_DIR)/fleet.log"
	@echo "  grafana:    http://127.0.0.1:$(GF_PORT)/d/quixiot-overview  (run 'make grafana' first if needed)"

# Stop the workload. Sends SIGINT for an orderly QUIC close. Also sweeps
# orphaned client children — the fleet SIGTERMs them on shutdown, but if the
# user killed the fleet pid directly they can linger.
down:
	@for name in fleet proxy server; do \
		pidfile=$(STACK_RUN_DIR)/$$name.pid; \
		if [ -f $$pidfile ]; then \
			pid=$$(cat $$pidfile); \
			if [ -n "$$pid" ] && kill -0 $$pid 2>/dev/null; then \
				echo "down: stopping $$name (pid $$pid)"; \
				kill -INT $$pid 2>/dev/null || true; \
			fi; \
			rm -f $$pidfile; \
		fi; \
	done
	@# Sweep any orphaned client children (best-effort, ignore 'no matches').
	@pkill -INT -f '$(BIN)/client ' 2>/dev/null || true
	@sleep 1
	@echo "down: done"

restart: down up

status:
	@for name in server proxy fleet; do \
		pidfile=$(STACK_RUN_DIR)/$$name.pid; \
		if [ -f $$pidfile ] && kill -0 $$(cat $$pidfile) 2>/dev/null; then \
			printf "  %-7s RUNNING   pid=%s\n" $$name $$(cat $$pidfile); \
		else \
			printf "  %-7s stopped\n" $$name; \
		fi; \
	done
	@fleetpid=$$(cat $(STACK_RUN_DIR)/fleet.pid 2>/dev/null); \
	if [ -n "$$fleetpid" ]; then \
		clients=$$(pgrep -P $$fleetpid 2>/dev/null | wc -l | tr -d ' '); \
	else clients=0; fi; \
	printf "  %-7s %s child(ren)\n" clients $$clients

# Follow all three logs together. Ctrl-C to exit.
logs:
	@tail -n 40 -F $(STACK_LOG_DIR)/server.log $(STACK_LOG_DIR)/proxy.log $(STACK_LOG_DIR)/fleet.log

help:
	@echo "Targets:"
	@echo "  build            build all cmd binaries into $(BIN)/"
	@echo "  test             go test ./..."
	@echo "  vet              go vet ./..."
	@echo "  fmt              go fmt ./..."
	@echo "  tidy             go mod tidy"
	@echo "  clean            remove $(BIN)"
	@echo "  certs            generate local CA and server leaf into $(CERT_DIR)/"
	@echo "  run-server       run the HTTP/3 + WebTransport server (plain metrics on $(SERVER_METRICS_PLAIN_ADDR))"
	@echo "  run-proxy        run the UDP impairment proxy (PROFILE=...)"
	@echo "  run-client       run one simulated device (ROLE=...) through SERVER_URL"
	@echo "  run-fleet        spawn COUNT simulated devices (ROLE defaults to mixed); writes $(FLEET_TARGETS_FILE)"
	@echo "  demo             launch the tmux demo"
	@echo "  verify           run the scripted profile verification sweep"
	@echo "  metrics          print server and proxy metrics"
	@echo "  grafana          start Prometheus + Grafana via docker compose (PROM_RETENTION=$(PROM_RETENTION))"
	@echo "  grafana-down     stop the docker compose stack"
	@echo "  grafana-logs     tail compose logs"
	@echo "  grafana-status   show compose container status"
	@echo "  up               start server + proxy (PROFILE=$(PROFILE)) + fleet (COUNT=$(COUNT)) in background"
	@echo "  down             stop the background workload (SIGINT)"
	@echo "  restart          down then up"
	@echo "  status           show running/stopped state per component"
	@echo "  logs             tail -F server + proxy + fleet logs"
