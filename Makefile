.PHONY: setup setup-web check-docs check-contracts test-go build-web verify dev-api dev-web
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
verify: check-docs check-contracts test-go build-web
dev-api:
	cd backend && go run ./cmd/api
dev-web:
	cd web && npm run dev
