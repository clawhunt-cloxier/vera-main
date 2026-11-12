.PHONY: setup setup-web check-toolchain check-docs check-contracts check-contract-compat check-api-types test-go test-automation build-web lint-go lint-web verify dev-api dev-web
setup:
	python3 -m venv .venv
	.venv/bin/python -m pip install -r tooling/requirements.txt
	$(MAKE) setup-web
setup-web:
	cd web && npm ci
check-docs:
	python3 scripts/check_docs.py
check-toolchain:
	bash scripts/check_go_version.sh
	bash scripts/check_node_version.sh
check-contracts:
	.venv/bin/python scripts/check_contracts.py
# Base revision: CONTRACT_BASE_REF, else merge-base with origin/dev.
check-contract-compat:
	python3 scripts/check_contract_compat.py
check-api-types:
	cd web && npm ci && npm run check:api
test-go:
	cd backend && go test -race ./...
lint-go:
	cd backend && golangci-lint run -c ../.golangci.yml
lint-web:
	cd web && npm ci && npm run lint && npm run format:check
build-web:
	cd web && npm ci && npm run build
test-automation:
	.venv/bin/python scripts/test_branch_cleanup.py
	python3 scripts/test_contract_compat.py
verify: check-toolchain check-docs check-contracts check-contract-compat check-api-types test-go test-automation lint-go lint-web build-web
dev-api:
	cd backend && go run ./cmd/api
dev-web:
	cd web && npm run dev
