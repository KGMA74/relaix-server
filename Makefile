# relaix-server
#
# Note on `test-race`: the race detector needs cgo, and the usual dev machine
# here is Windows without a C toolchain. So it runs in a container rather than
# silently not running at all — a concurrent component reported as "tests pass"
# without the detector is not actually tested.

GO          ?= go
BIN         ?= bin/gatewayd
PKG         ?= ./...
GOLANG_IMG  ?= golang:1.26

# Injected into main.version.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X main.version=$(VERSION)

# Local throwaway Postgres used by `make db-up` / `make migrate`.
PG_CONTAINER ?= relaix-pg
PG_PORT      ?= 55432
PG_IMAGE     ?= postgres:17-alpine
DATABASE_URL ?= postgres://postgres:relaix@localhost:$(PG_PORT)/relaix?sslmode=disable

# Absolute path in the form Docker wants, so bind mounts work from Git Bash too
# (MSYS rewrites /c/... into a Windows path and the mount silently fails).
REPO_DIR := $(shell pwd)

IMAGE ?= relaix-server
TAG   ?= $(VERSION)

.DEFAULT_GOAL := help
.PHONY: help build run test test-race cover lint fmt vet tidy \
        db-up db-down migrate migrate-down psql proto clean check \
        docker-build docker-run test-integration

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------------------
## build & run
## ---------------------------------------------------------------------------

build: ## Build gatewayd into bin/
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gatewayd

run: ## Run gatewayd from source
	$(GO) run -ldflags "$(LDFLAGS)" ./cmd/gatewayd

clean: ## Remove build artifacts
	rm -rf bin/ coverage.out

## ---------------------------------------------------------------------------
## tests & checks
## ---------------------------------------------------------------------------

test: ## Run tests (no race detector; see test-race)
	$(GO) test -count=1 $(PKG)

test-race: ## Run tests with the race detector, in a container
	MSYS_NO_PATHCONV=1 docker run --rm -v "$(REPO_DIR):/src:ro" -w /src $(GOLANG_IMG) \
		sh -c 'CGO_ENABLED=1 go test -race -count=1 $(PKG)'

test-integration: db-up migrate ## Run the store conformance suite against a real Postgres
	RELAIX_TEST_DATABASE_URL="$(DATABASE_URL)" $(GO) test -count=1 ./store/...
	@$(MAKE) --no-print-directory db-down

cover: ## Run tests with coverage and print the summary
	$(GO) test -count=1 -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1

fmt: ## Format all Go code
	gofmt -w .

lint: ## Fail if anything is unformatted, then vet
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi
	$(GO) vet $(PKG)

vet: ## Run go vet
	$(GO) vet $(PKG)

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

check: lint test ## What CI should run at minimum

## ---------------------------------------------------------------------------
## docker
## ---------------------------------------------------------------------------

docker-build: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) -t $(IMAGE):latest .

docker-run: docker-build ## Run the image in the foreground
	docker run --rm -it $(IMAGE):latest

## ---------------------------------------------------------------------------
## database
## ---------------------------------------------------------------------------

db-up: ## Start a throwaway Postgres and wait for it
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_PASSWORD=relaix -e POSTGRES_DB=relaix \
		-p $(PG_PORT):5432 $(PG_IMAGE) >/dev/null
	@echo "waiting for postgres..."
	@for i in $$(seq 1 45); do \
		if docker exec $(PG_CONTAINER) pg_isready -U postgres -d relaix >/dev/null 2>&1; then \
			echo "ready on port $(PG_PORT)"; exit 0; \
		fi; sleep 1; \
	done; echo "postgres did not become ready"; exit 1

db-down: ## Remove the throwaway Postgres
	-docker rm -f $(PG_CONTAINER)

migrate: ## Apply migrations to DATABASE_URL
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_URL)" \
		goose -dir db/migrations up

migrate-down: ## Roll back the last migration
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_URL)" \
		goose -dir db/migrations down

psql: ## Open a psql shell on the throwaway Postgres
	docker exec -it $(PG_CONTAINER) psql -U postgres -d relaix

## ---------------------------------------------------------------------------
## proto
## ---------------------------------------------------------------------------

proto: ## Regenerate gen/ from the monorepo's proto/ (run from the parent repo)
	@echo "proto/ lives in the relaix monorepo, one level up."
	@echo "Run 'buf generate' there; its buf.gen.yaml writes into this repository."
	@cd .. && buf lint && buf generate
