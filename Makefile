GO        ?= go
BINARY    ?= bin/transporter
GOCACHE   ?= $(CURDIR)/.gocache

.PHONY: default build test run fmt tidy clean

default: build

all: build

build: ## Build transporter binary
	@mkdir -p $(dir $(BINARY))
	GOCACHE=$(GOCACHE) $(GO) build ./...
	GOCACHE=$(GOCACHE) $(GO) build -o $(BINARY) .

test: ## Run go test on every package
	GOCACHE=$(GOCACHE) $(GO) test ./...

run: build ## Build and run (override ARGS to pass flags)
	$(BINARY) $(ARGS)

fmt: ## gofmt the entire module
	$(GO)fmt ./...

tidy: ## go mod tidy
	GOCACHE=$(GOCACHE) $(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(BINARY) $(GOCACHE)
