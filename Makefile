.PHONY: build run test test-race check fmt vet lint tidy clean help

BIN      := bin/linklore
PKG      := ./...
# sqlite_fts5 enables the FTS5 virtual-table module in mattn/go-sqlite3.
# Required at build *and* test time — without it the driver reports
# "no such module: fts5" when CREATE VIRTUAL TABLE ... USING fts5 runs.
TAGS     := sqlite_fts5
GOFLAGS  := -trimpath -tags=$(TAGS)
LDFLAGS  := -s -w

build: ## build the binary
	@mkdir -p bin
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/linklore

run: ## run the server (uses configs/config.yaml)
	go run -tags=$(TAGS) ./cmd/linklore serve --config ./configs/config.yaml

test: ## run tests
	go test -tags=$(TAGS) $(PKG)

test-race: ## run tests with race detector
	go test -race -tags=$(TAGS) -count=1 $(PKG)

fmt: ## go fmt
	gofmt -s -w .

vet: ## go vet
	go vet -tags=$(TAGS) $(PKG)

lint: ## golangci-lint (if installed)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run --build-tags=$(TAGS) $(PKG) || echo "golangci-lint not installed, skipping"

tidy: ## go mod tidy
	go mod tidy

check: fmt vet lint test-race ## fmt + vet + lint + test

clean: ## remove build artefacts
	rm -rf bin

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
