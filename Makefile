.PHONY: help build server gateway run run-gateway web web-install web-typecheck test vet fmt tidy clean

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- Go: API server + LLM gateway ---

build: server gateway ## build both Go binaries into bin/

server: ## build the API server
	go build -o bin/tifl-server ./cmd/server

gateway: ## build the LLM gateway
	go build -o bin/tifl-gateway ./cmd/gateway

run: ## run the API server (http://127.0.0.1:8000)
	go run ./cmd/server

run-gateway: ## run the LLM gateway (http://127.0.0.1:8001)
	go run ./cmd/gateway

## --- Web: SolidJS client ---

web-install: ## install web dependencies
	cd web && npm install

web: ## build the SolidJS client into web/dist
	cd web && npm run build

web-typecheck: ## typecheck the web client
	cd web && npm run typecheck

## --- Quality ---

test: ## run Go unit tests
	go test ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt the tree in place
	gofmt -l -w .

tidy: ## sync go.mod / go.sum
	go mod tidy

clean: ## remove build artifacts
	rm -rf bin web/dist
