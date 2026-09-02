.PHONY: help build server gateway audio kaikki-import kaikki-translate run run-gateway run-audio stop seed-demo import-kaikki web web-install web-api-types web-typecheck test test-live vet fmt tidy clean

# Live gateway test (opt-in): a real OpenCode server to verify the gateway
# end-to-end. Override the model with TIFL_LIVE_MODEL.
TIFL_LIVE_OPENCODE_URL ?= http://127.0.0.1:4202

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- Go: API server + LLM gateway ---

build: server gateway audio kaikki-import kaikki-translate ## build Go binaries into bin/

server: ## build the API server
	go build -o bin/tifl-server ./cmd/server

gateway: ## build the LLM gateway
	go build -o bin/tifl-gateway ./cmd/gateway

audio: ## build the audio manager
	go build -o bin/tifl-audio ./audio/cmd/audio

kaikki-import: ## build the Wiktextract/kaikki importer
	go build -o bin/tifl-kaikki-import ./cmd/kaikki-import

kaikki-translate: ## build the native-definition LLM translator
	go build -o bin/tifl-kaikki-translate ./cmd/kaikki-translate

run: ## run the API server (http://127.0.0.1:8000)
	go run ./cmd/server

run-gateway: ## run the LLM gateway (http://127.0.0.1:8001)
	go run ./cmd/gateway

run-audio: ## run the audio manager (http://127.0.0.1:8010)
	go run ./audio/cmd/audio

SERVER_PORT ?= 8000

stop: ## stop any running API server (frees http://127.0.0.1:$(SERVER_PORT))
	@pids=$$(lsof -ti tcp:$(SERVER_PORT) -sTCP:LISTEN 2>/dev/null); \
	if [ -z "$$pids" ]; then \
		echo "no server listening on port $(SERVER_PORT)"; \
		exit 0; \
	fi; \
	targets=""; \
	for pid in $$pids; do \
		targets="$$targets $$pid $$(ps -o ppid= -p $$pid 2>/dev/null | tr -d ' ')"; \
	done; \
	echo "stopping server on port $(SERVER_PORT):$$targets"; \
	kill $$targets 2>/dev/null || true; \
	sleep 1; \
	left=$$(lsof -ti tcp:$(SERVER_PORT) -sTCP:LISTEN 2>/dev/null); \
	if [ -n "$$left" ]; then echo "force killing:$$left"; kill -9 $$left 2>/dev/null || true; fi

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

eval-smoke: ## run one tiny prompt-eval scenario (needs api_key in tifl.yaml; see eval/README.md)
	python3 eval/harness/prompt_dag.py \
		--pipelines eval/pipelines/single.json \
		--scenarios eval/scenarios/scenarios_beginner.json \
		--out eval/results/run_smoke.json
	@echo "smoke output: eval/results/run_smoke.json (gitignored)"

generate-api: ## regenerate Go + TS wire types from spec/openapi.yaml (#213)
	go generate ./internal/handler/oapigen/
	cd web && npm run api:types
