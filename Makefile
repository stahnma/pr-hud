BINARY  := pr-hud
PREFIX  ?= $(HOME)/.local
ENVFILE ?= pr-hud.env

.DEFAULT_GOAL := build
.PHONY: build test vet fmt check run install uninstall clean help

build: ## Build the pr-hud binary
	go build -o $(BINARY) .

test: ## Run tests
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format code with gofmt
	gofmt -w .

check: vet test ## Fail if code is unformatted, then vet and test
	@test -z "$$(gofmt -l .)" || { echo "unformatted files:"; gofmt -l .; exit 1; }

run: build ## Build and run, loading pr-hud.env if present (override with ENVFILE=)
	@if [ -f $(ENVFILE) ]; then set -a; . ./$(ENVFILE); set +a; fi; LISTEN_ADDR=0.0.0.0 exec ./$(BINARY)

install: build ## Install to ~/.local/bin (override with PREFIX=)
	install -D -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)

uninstall: ## Remove the installed binary
	rm -f $(PREFIX)/bin/$(BINARY)

clean: ## Remove the built binary
	rm -f $(BINARY)

help: ## Show this help
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
