.PHONY: setup setup-web check-docs check-contracts test-go test-automation build-web lint-go lint-web verify dev-api dev-web
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
lint-go:
	cd backend && golangci-lint run -c ../.golangci.yml
lint-web:
	cd web && npm ci && npm run lint && npm run format:check
build-web:
	cd web && npm ci && npm run build
test-automation:
	.venv/bin/python scripts/test_branch_cleanup.py
verify: check-docs check-contracts test-go test-automation lint-go lint-web build-web
dev-api:
	cd backend && go run ./cmd/api
dev-web:
	cd web && npm run dev
