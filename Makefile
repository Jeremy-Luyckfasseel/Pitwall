# Pitwall — developer entrypoints. Thin wrappers over docker compose + the contract gates.
# Targets grow as the platform does (walking-skeleton-first). Recipes are POSIX sh; run on
# Linux / macOS / WSL / git-bash (the CI runners and the VPS).
.DEFAULT_GOAL := help
.PHONY: help up down clean logs contract-test contract test smoke smoke-quarantine

help: ## Show this help
	@echo "Pitwall make targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up: ## Bring up the local stack (RabbitMQ) and wait until it is healthy
	@if [ ! -f .env ]; then \
		echo "No .env found — creating one from .env.example (edit it for real values)."; \
		cp .env.example .env; \
	fi
	docker compose up -d
	@echo "Waiting for RabbitMQ to report healthy (bus-only liveness, no HTTP /health)..."
	@for i in $$(seq 1 30); do \
		status=$$(docker inspect -f '{{.State.Health.Status}}' pitwall-rabbitmq 2>/dev/null || echo starting); \
		if [ "$$status" = "healthy" ]; then \
			echo "RabbitMQ is healthy. Management API: http://127.0.0.1:15672"; \
			exit 0; \
		fi; \
		echo "  ... $$status ($$i/30)"; sleep 2; \
	done; \
	echo "ERROR: RabbitMQ did not become healthy in time." >&2; \
	docker compose ps; exit 1

down: ## Stop the stack (keeps the durable broker volume)
	docker compose down

clean: ## Stop the stack AND delete its data volumes (destructive)
	docker compose down -v

logs: ## Tail the stack logs
	docker compose logs -f

contract-test: ## Run the contract gates (schema-lint + example validation + known-bad rejection + corpus coherence + pytest)
	python3 scripts/check-schema-lint.py
	python3 scripts/validate-contract.py
	python3 scripts/check-invalid-fixtures.py
	bash scripts/check-corpus-coherence.sh
	python3 -m pytest tests/contract

contract: ## (placeholder) Wire-DTO codegen — introduced with the 2nd language (Epic 2 / AR15 step 4)
	@echo "make contract: nothing to generate yet — codegen arrives with the 2nd language (Epic 2 / AR15 step 4)."

test: ## Per-service tests. Timing (Go): unit always; integration (real RabbitMQ via testcontainers) needs Docker.
	cd services/timing && go build ./... && go vet ./... && go test ./...
	@echo "Integration tests (need Docker): cd services/timing && go test -tags=integration ./test/integration/..."

smoke: ## Cross-language conformance harness + e2e smoke — REQUIRED lane (real binaries + real RabbitMQ via testcontainers; needs Docker)
	cd tests/conformance/go && go test -tags=integration -timeout 900s ./...

smoke-quarantine: ## Conformance QUARANTINE lane — flaky scenarios, non-blocking (AR16: quarantine, never @skip)
	cd tests/conformance/go && CONFORMANCE_LANE=quarantine go test -tags=integration -timeout 900s ./...
