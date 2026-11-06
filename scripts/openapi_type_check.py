#!/usr/bin/env python3
"""Check that generated TypeScript types match OpenAPI spec version."""
import json
import sys
from pathlib import Path

root = Path(__file__).resolve().parents[1]
openapi = json.loads((root / 'contracts/openapi.json').read_text())
openapi_version = openapi['info']['version']

# Generated types file will be created by T013 and should have version marker
gen = root / 'web/src/api/generated.ts'
if not gen.exists():
    print(f"SKIP: {gen} does not exist yet (will be generated in T013)")
    sys.exit(0)

content = gen.read_text()
marker = f'// Generated from OpenAPI {openapi_version}'
if marker not in content:
    print(f"FAIL: Generated types do not match OpenAPI version {openapi_version}")
    print(f"Expected marker: {marker}")
    print(f"Regenerate with: make gen-api-types")
    sys.exit(1)

print(f"PASS: Generated types match OpenAPI {openapi_version}")
