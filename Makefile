GO ?= go
BIN ?= ./bin
CMDS := server client proxy fleet

CERT_DIR ?= var/certs
CA_FILE ?= $(CERT_DIR)/ca.pem
CERT_FILE ?= $(CERT_DIR)/server.pem
KEY_FILE ?= $(CERT_DIR)/server.key
UPLOAD_DIR ?= var/uploads

SERVER_ADDR ?= 127.0.0.1:4444
PROXY_LISTEN ?= 127.0.0.1:4443
PROXY_UPSTREAM ?= 127.0.0.1:4444
PROXY_METRICS_ADDR ?= 127.0.0.1:9104
CLIENT_METRICS_ADDR ?=

SERVER_URL ?= https://127.0.0.1:4443
CLIENT_ID ?= client-local
COUNT ?= 10
ROLE ?=
PROFILE ?= passthrough
LOG_LEVEL ?= info

CLIENT_ROLE = $(or $(ROLE),poller)
FLEET_ROLE = $(or $(ROLE),mixed)
SERVER_METRICS_URL ?= https://127.0.0.1:4444/metrics

.PHONY: all build $(CMDS) test vet fmt tidy clean help certs run-server run-proxy run-client run-fleet demo verify metrics

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

help:
	@echo "Targets:"
	@echo "  build       build all cmd binaries into $(BIN)/"
	@echo "  test        go test ./..."
	@echo "  vet         go vet ./..."
	@echo "  fmt         go fmt ./..."
	@echo "  tidy        go mod tidy"
	@echo "  clean       remove $(BIN)"
	@echo "  certs       generate local CA and server leaf into $(CERT_DIR)/"
	@echo "  run-server  run the HTTP/3 + WebTransport server"
	@echo "  run-proxy   run the UDP impairment proxy (PROFILE=...)"
	@echo "  run-client  run one simulated device (ROLE=...) through SERVER_URL"
	@echo "  run-fleet   spawn COUNT simulated devices (ROLE defaults to mixed)"
	@echo "  demo        launch the tmux demo"
	@echo "  verify      run the scripted profile verification sweep"
	@echo "  metrics     print server and proxy metrics"
