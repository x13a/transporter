GO        ?= go
BINARY    ?= bin/transporter
GOCACHE   ?= $(CURDIR)/.gocache

.PHONY: default build test live-test run fmt tidy clean

default: build

all: build

build: ## Build transporter binary
	@mkdir -p $(dir $(BINARY))
	GOCACHE=$(GOCACHE) $(GO) build ./...
	GOCACHE=$(GOCACHE) $(GO) build -o $(BINARY) .

test: ## Run go test on every package
	GOCACHE=$(GOCACHE) $(GO) test ./...

LIVE_TESTS := \
	TestLiveHTTPTransportProxy \
	TestLiveHTTPTransportUDPProxy \
	TestLiveFileTransportProxy \
	TestLiveFileTransportUDPProxy \
	TestLiveTCPTransportProxy \
	TestLiveTCPTransportUDPProxy \
	TestLiveUDSTransportProxy \
	TestLiveUDSTransportUDPProxy

LIVE_ENV := \
	E2E_HTTP_ENABLE=1 \
	E2E_HTTP_UDP_ENABLE=1 \
	E2E_FILE_ENABLE=1 \
	E2E_FILE_UDP_ENABLE=1 \
	E2E_TCP_ENABLE=1 \
	E2E_TCP_UDP_ENABLE=1 \
	E2E_UDS_ENABLE=1 \
	E2E_UDS_UDP_ENABLE=1

live-test: ## Run all live end-to-end tests with required env flags
	@for t in $(LIVE_TESTS); do \
		echo "==> $$t"; \
		$(LIVE_ENV) GOCACHE=$(GOCACHE) $(GO) test -run $$t ./... || exit $$?; \
	done

run: build ## Build and run (override ARGS to pass flags)
	$(BINARY) $(ARGS)

fmt: ## gofmt the entire module
	$(GO)fmt ./...

tidy: ## go mod tidy
	GOCACHE=$(GOCACHE) $(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(BINARY) $(GOCACHE)
