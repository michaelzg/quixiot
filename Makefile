# QuixIoT — HTTP/3 + QUIC IoT PoC
#
# Phase 1 skeleton: build / test / fmt / clean. More targets land as phases ship.

GO      ?= go
BIN     ?= ./bin
CMDS    := server client proxy fleet

.PHONY: all build $(CMDS) test vet fmt tidy clean help

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

help:
	@echo "Targets:"
	@echo "  build       build all cmd binaries into $(BIN)/"
	@echo "  server      build cmd/server"
	@echo "  client      build cmd/client"
	@echo "  proxy       build cmd/proxy"
	@echo "  fleet       build cmd/fleet"
	@echo "  test        go test ./..."
	@echo "  vet         go vet ./..."
	@echo "  fmt         go fmt ./..."
	@echo "  tidy        go mod tidy"
	@echo "  clean       remove $(BIN)"
