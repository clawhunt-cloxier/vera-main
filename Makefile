.PHONY: setup setup-web check-docs check-contracts test-go test-automation build-web verify dev-api dev-web lint-go lint-web fmt-web gen-api-types
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
verify: check-docs check-contracts test-go test-automation lint-go build-web
	.venv/bin/python scripts/openapi_type_check.py
lint-go:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found; install: https://golangci-lint.run/welcome/install/"; exit 1; }
	cd backend && golangci-lint run
lint-web:
	cd web && npm run lint
fmt-web:
	cd web && npm run format
gen-api-types:
	@echo "API type generation will be implemented in T013"
	@echo "For now, this is a placeholder that T011 checks with openapi_type_check.py"
dev-api:
	cd backend && go run ./cmd/api
dev-web:
	cd web && npm run dev
