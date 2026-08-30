# ShareDirStat build entry points. `make help` lists targets.

MODULE      := github.com/StevenGann/ShareDirStat
BIN         := bin/sharedirstat
IMAGE       ?= ghcr.io/stevengann/sharedirstat
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.Commit=$(COMMIT) \
  -X $(MODULE)/internal/version.Date=$(DATE)
PLATFORMS   ?= linux/arm64,linux/amd64
GO          ?= go
NPM         ?= npm

.PHONY: help all build web web-install web-test web-lint test test-race lint fmt vet run clean \
        docker docker-push fixture smoke bench tidy

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

all: web build ## Build the UI and the binary

build: ## Build the server binary for the host platform (embeds web/dist if built)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sharedirstat

build-arm64: ## Cross-compile for linux/arm64 (Raspberry Pi 5)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/sharedirstat-linux-arm64 ./cmd/sharedirstat

web-install: ## Install web dependencies
	cd web && $(NPM) ci --no-audit --no-fund

web: ## Build the web UI into web/dist
	cd web && $(NPM) run build

web-test: ## Run web unit tests
	cd web && $(NPM) run test

web-lint: ## Lint and type-check the web UI
	cd web && $(NPM) run lint && $(NPM) run typecheck

test: ## Run Go tests
	$(GO) test -count=1 ./...

test-race: ## Run Go tests with the race detector and coverage
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...

lint: vet ## Run golangci-lint (install: https://golangci-lint.run)
	golangci-lint run ./...

vet: ## go vet
	$(GO) vet ./...

fmt: ## gofmt all Go sources
	gofmt -w $$(git ls-files '*.go')

tidy: ## go mod tidy
	$(GO) mod tidy

run: build ## Run locally against ./shares with text logs (create ./shares/<name> first)
	SDS_DISCOVERY__ROOT=$(CURDIR)/shares SDS_DATA_DIR=$(CURDIR)/data $(BIN) serve --log-format text --log-level debug

fixture: ## Generate a 50k-file fixture tree under ./shares/fixture
	mkdir -p shares && $(GO) run ./hack/mkfixture -root shares/fixture -files 50000 -depth 4 -width 5 -hardlinks 20 -symlink-loop -unreadable -wide 20000 -sparse

smoke: build ## Start the binary, scan a temp share and probe every read API
	./hack/smoke.sh $(BIN)

bench: ## Measure per-node memory, query latency and scan throughput
	$(GO) test -run TestMemoryPerNode -v ./internal/model/
	$(GO) test -run XXX -bench BenchmarkQueries -benchtime 50x ./internal/model/
	./hack/bench.sh 200000

docker: ## Build the container image for the host platform
	docker buildx build --load -t $(IMAGE):dev \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) .

docker-push: ## Build and push a multi-arch image (requires buildx + login)
	docker buildx build --platform $(PLATFORMS) --push -t $(IMAGE):$(VERSION) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) .

clean: ## Remove build outputs
	rm -rf bin dist coverage.out web/dist/* && touch web/dist/.gitkeep
