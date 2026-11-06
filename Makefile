.PHONY: setup setup-web check-docs check-contracts test-go test-automation build-web verify dev-api dev-web dev-up dev-down dev-logs migrate-up migrate-down migrate-status test-integration
setup:
	python3 -m venv .venv
	.venv/bin/python -m pip install -r tooling/requirements.txt
	$(MAKE) setup-web
setup-web:
	cd web && npm ci
check-docs:
	python3 scripts/check_docs.py
check-contracts:
	.venv/bin/python scripts/check_contracts.py
test-go:
	cd backend && go test -race ./...
build-web:
	cd web && npm ci && npm run build
test-automation:
	.venv/bin/python scripts/test_branch_cleanup.py
verify: check-docs check-contracts test-go test-automation build-web
dev-up:
	cd deploy && docker compose -f compose.dev.yml up -d
	@echo "Waiting for services to be healthy..."
	@sleep 5
	@echo "Services started. Access:"
	@echo "  Keycloak admin: http://localhost:8180 (admin/admin_change_in_production)"
	@echo "  MinIO console:  http://localhost:9001 (minioadmin/minioadmin_change_in_production)"
	@echo "  API:            http://localhost:8080"
	@echo ""
	@echo "Next: Run migrations with 'make migrate-up' and start API with 'make dev-api'"
dev-down:
	cd deploy && docker compose -f compose.dev.yml down
dev-logs:
	cd deploy && docker compose -f compose.dev.yml logs -f
migrate-up:
	@command -v goose >/dev/null 2>&1 || { echo "goose not found; install: go install github.com/pressly/goose/v3/cmd/goose@latest"; exit 1; }
	cd backend/migrations && goose postgres "$${DATABASE_URL}" up
migrate-down:
	cd backend/migrations && goose postgres "$${DATABASE_URL}" down
migrate-status:
	cd backend/migrations && goose postgres "$${DATABASE_URL}" status
test-integration:
	@echo "Integration tests will be implemented as T012–T019 progress"
	@echo "For now: make dev-up && make migrate-up to verify the dev environment"
dev-api:
	cd backend && go run ./cmd/api
dev-web:
	cd web && npm run dev
