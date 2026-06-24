# wadist developer commands. Integration tests use testcontainers (need Docker).
export PATH := /usr/local/go/bin:$(PATH)

GO  ?= go
PKG ?= ./...
# Local Docker-in-Docker / sandbox environments often cannot run the testcontainers
# Ryuk reaper; disable it for local runs. CI on GitHub runners can keep it enabled.
TEST_ENV := TESTCONTAINERS_RYUK_DISABLED=true

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n",$$1,$$2}'

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: vet
vet: ## go vet
	$(GO) vet $(PKG)

.PHONY: staticcheck
staticcheck: ## staticcheck (downloaded on demand)
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest $(PKG)

.PHONY: test
test: ## run tests
	$(TEST_ENV) $(GO) test $(PKG)

.PHONY: test-race
test-race: ## run tests with the race detector
	$(TEST_ENV) $(GO) test -race $(PKG)

.PHONY: vuln
vuln: ## govulncheck (downloaded on demand)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest $(PKG)

.PHONY: labels
labels: ## scan for high-cardinality metric labels
	./scripts/check_metric_labels.sh

.PHONY: gate
gate: tidy vet test-race labels vuln ## full local release gate

.PHONY: up
up: ## start local postgres + redis
	docker compose up -d

.PHONY: down
down: ## stop local services
	docker compose down

.PHONY: migrate-twice
migrate-twice: ## apply migrations twice against $$DSN (idempotency check)
	./scripts/migrate_twice.sh "$(DSN)"

.PHONY: memory-gate
memory-gate: ## run memory baseline gate (requires build tag memory_gate)
	$(TEST_ENV) $(GO) test -tags memory_gate -run TestMemoryBaseline ./internal/capacity/

.PHONY: chaos
chaos: ## run takeover chaos test (requires Docker for testcontainers)
	$(TEST_ENV) $(GO) test -run TestTakeoverChaos -count=5 ./internal/node/
