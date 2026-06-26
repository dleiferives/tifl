.PHONY: help build server gateway kaikki-import kaikki-translate run run-gateway seed-demo import-kaikki web web-install web-api-types web-typecheck test test-live vet fmt tidy clean

# Live gateway test (opt-in): a real OpenCode server to verify the gateway
# end-to-end. Override the model with TIFL_LIVE_MODEL.
TIFL_LIVE_OPENCODE_URL ?= http://127.0.0.1:4202

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- Go: API server + LLM gateway ---

build: server gateway kaikki-import kaikki-translate ## build Go binaries into bin/

server: ## build the API server
	go build -o bin/tifl-server ./cmd/server

gateway: ## build the LLM gateway
	go build -o bin/tifl-gateway ./cmd/gateway

kaikki-import: ## build the Wiktextract/kaikki importer
	go build -o bin/tifl-kaikki-import ./cmd/kaikki-import

kaikki-translate: ## build the native-definition LLM translator
	go build -o bin/tifl-kaikki-translate ./cmd/kaikki-translate

run: ## run the API server (http://127.0.0.1:8000)
	go run ./cmd/server

run-gateway: ## run the LLM gateway (http://127.0.0.1:8001)
	go run ./cmd/gateway

seed-demo: ## seed deterministic local demo data for UI/API development
	go run ./cmd/devseed

import-kaikki: ## import Wiktextract JSONL; pass ARGS="-input el-extract.jsonl.gz -language el"
	go run ./cmd/kaikki-import $(ARGS)

## --- Web: SolidJS client ---

web-install: ## install web dependencies
	cd web && npm install

web: ## build the SolidJS client into web/dist
	cd web && npm run build

web-api-types: ## regenerate TypeScript API types from spec/openapi.yaml
	cd web && npm run api:types

web-typecheck: ## typecheck the web client
	cd web && npm run typecheck

## --- Quality ---

test: ## run Go unit tests (no network)
	go test ./...

test-live: ## live OpenCode tests vs a running `opencode serve` (needs TIFL_LIVE_OPENCODE_URL)
	TIFL_LIVE_OPENCODE_URL=$(TIFL_LIVE_OPENCODE_URL) go test -tags live ./internal/gateway ./internal/handler -run Live -v

vet: ## go vet
	go vet ./...

fmt: ## gofmt the tree in place
	gofmt -l -w .

tidy: ## sync go.mod / go.sum
	go mod tidy

install-hooks: ## install git hooks (run once after cloning)
	git config core.hooksPath .githooks

clean: ## remove build artifacts
	rm -rf bin web/dist
