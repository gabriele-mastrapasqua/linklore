.PHONY: build run dev install uninstall \
        test test-race test-pkg test-cover bench \
        fmt vet lint tidy check \
        smoke smoke-up smoke-down clean reset-db \
        help

BIN      := bin/linklore
PKG      := ./...
# sqlite_fts5 enables the FTS5 virtual-table module in mattn/go-sqlite3.
# Required at build *and* test time — without it the driver reports
# "no such module: fts5" when CREATE VIRTUAL TABLE ... USING fts5 runs.
TAGS         := sqlite_fts5
GOFLAGS      := -trimpath -tags=$(TAGS)
LDFLAGS      := -s -w
INSTALL_DIR  ?= /usr/local/bin
INSTALL_NAME := linklore
ADDR         ?= 127.0.0.1:8080
DB_PATH      ?= ./data/linklore.db
CONFIG       ?= ./configs/config.yaml

# ---- build / run ----

build: ## build the binary into ./bin/linklore
	@mkdir -p bin
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/linklore
	@echo "built $(BIN) ($$(du -h $(BIN) | cut -f1))"

run: ## run the server with ./configs/config.yaml
	go run -tags=$(TAGS) ./cmd/linklore serve --config $(CONFIG)

dev: reset-db build ## fresh db + build + run (handy when iterating)
	$(BIN) serve --config $(CONFIG)

install: build ## install ./bin/linklore to $(INSTALL_DIR) (sudo may prompt)
	@if [ -w "$(INSTALL_DIR)" ]; then \
		install -m 0755 $(BIN) $(INSTALL_DIR)/$(INSTALL_NAME); \
	else \
		sudo install -m 0755 $(BIN) $(INSTALL_DIR)/$(INSTALL_NAME); \
	fi
	@echo "installed → $(INSTALL_DIR)/$(INSTALL_NAME)"
	@command -v $(INSTALL_NAME) >/dev/null && echo "ok: '$(INSTALL_NAME)' is on PATH" || \
		echo "warn: $(INSTALL_DIR) is not on PATH"

uninstall: ## remove $(INSTALL_DIR)/linklore
	@if [ -w "$(INSTALL_DIR)" ]; then rm -f $(INSTALL_DIR)/$(INSTALL_NAME); \
	else sudo rm -f $(INSTALL_DIR)/$(INSTALL_NAME); fi
	@echo "uninstalled"

# ---- tests ----

test: ## run all tests (no race, faster)
	go test -tags=$(TAGS) $(PKG)

test-race: ## run all tests with -race (use this in CI)
	go test -race -tags=$(TAGS) -count=1 $(PKG)

# Run a single package: make test-pkg PKG=./internal/search
test-pkg: ## run one package (PKG=./internal/foo, optional NAME=TestX)
	@if [ -n "$(NAME)" ]; then \
		go test -race -tags=$(TAGS) -count=1 -run $(NAME) $(PKG); \
	else \
		go test -race -tags=$(TAGS) -count=1 $(PKG); \
	fi

test-cover: ## test with coverage report → ./coverage.html
	go test -tags=$(TAGS) -coverprofile=coverage.out $(PKG)
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

bench: ## run any benchmarks present
	go test -tags=$(TAGS) -bench=. -benchmem $(PKG)

# ---- code hygiene ----

fmt: ## gofmt -s -w
	gofmt -s -w .

vet: ## go vet
	go vet -tags=$(TAGS) $(PKG)

lint: ## golangci-lint (no-op if not installed)
	@command -v golangci-lint >/dev/null 2>&1 && \
		golangci-lint run --build-tags=$(TAGS) $(PKG) || \
		echo "golangci-lint not installed, skipping"

tidy: ## go mod tidy
	go mod tidy

check: fmt vet lint test-race ## fmt + vet + lint + test (use before commit)

# ---- smoke / live HTTP testing ----

smoke-up: build reset-db ## start server in the background for smoke tests
	@nohup $(BIN) serve --config $(CONFIG) > /tmp/linklore-smoke.log 2>&1 & echo $$! > /tmp/linklore.pid
	@sleep 1
	@echo "started: pid=$$(cat /tmp/linklore.pid), log=/tmp/linklore-smoke.log"

smoke-down: ## stop the smoke server
	@if [ -f /tmp/linklore.pid ]; then kill $$(cat /tmp/linklore.pid) 2>/dev/null || true; rm -f /tmp/linklore.pid; fi
	@echo "stopped"

smoke: smoke-up ## end-to-end smoke against http://$(ADDR)
	@echo "GET /            -> $$(curl -s -o /dev/null -w '%{http_code}' http://$(ADDR)/)"
	@echo "GET /healthz     -> $$(curl -s -o /dev/null -w '%{http_code}' http://$(ADDR)/healthz)"
	@curl -s -X POST -d 'slug=smoke&name=Smoke' http://$(ADDR)/collections > /dev/null
	@echo "POST /collections-> 200 (smoke)"
	@curl -s -X POST -d 'url=https://example.com/x' http://$(ADDR)/c/smoke/links > /dev/null
	@echo "POST /links      -> 200"
	@echo "GET /search?q=x  -> $$(curl -s -o /dev/null -w '%{http_code}' 'http://$(ADDR)/search?q=x')"
	@$(MAKE) -s smoke-down

# ---- housekeeping ----

reset-db: ## delete the local sqlite database (data/linklore.db*)
	@find data -maxdepth 1 -name 'linklore.db*' -delete 2>/dev/null || true
	@echo "data/linklore.db cleared"

clean: ## remove build artefacts and coverage files
	rm -rf bin coverage.out coverage.html

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
