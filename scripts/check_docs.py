#!/usr/bin/env python3
"""Offline link, contract structure and bootstrap fixture checks; not business acceptance."""
from pathlib import Path
import json
import re
from decimal import Decimal as D, ROUND_HALF_UP

ROOT = Path(__file__).resolve().parents[1]
required = ['README.md','AGENTS.md','CLAUDE.md','SPEC.md','ARCHITECTURE.md','API.md','DATA_MODEL.md','TASKS.md','TESTING.md','CONTRIBUTING.md','docs/README.md','docs/RELEASE.md',
            'docs/HARNESS.md','docs/DATA_SERVICE.md','docs/RESEARCH_SCOPE.md','docs/decisions/0003-product-harness-runtime.md',
            'docs/WEB.md','docs/OPERATIONS.md','docs/CONVENTIONS.md','contracts/datasets/catalog.json','tests/fixtures/demo-universe.json',
            'docs/skills/stock-analysis.md','docs/skills/capital-flow.md','docs/skills/deep-research.md','docs/skills/watchlist.md']
for f in required:
    assert (ROOT/f).is_file(), f
count = 0
for p in ROOT.rglob('*.md'):
    if any(x in p.parts for x in ('node_modules','.git','.venv')):
        continue
    s = p.read_text()
    assert len(re.findall(r'^```',s,re.M)) % 2 == 0, f'Unclosed fence: {p}'
    for target in re.findall(r'\]\(([^)]+)\)',s):
        if target.startswith(('https://','http://','mailto:')):
            continue
        path, _, anchor = target.partition('#')
        q = (p.parent/path).resolve() if path else p
        assert q.is_relative_to(ROOT), f'Outside repo: {p}: {target}'
        assert q.exists(), f'Missing: {p}: {target}'
        if anchor:
            assert f'id="{anchor}"' in q.read_text(), f'Missing explicit anchor: {target}'
    for value in re.findall(r'```json\n(.*?)\n```',s,re.S):
        json.loads(value)
    count += 1
for p in (ROOT/'contracts').rglob('*.json'):
    json.loads(p.read_text())
tasks=(ROOT/'TASKS.md').read_text()
rows=re.findall(r'^\| (T\d{3}[a-z]?) \|[^|]*\| ([^|]*) \| (TODO|IN_PROGRESS|REVIEW|DONE|BLOCKED) \|',tasks,re.M)
ids=[r[0] for r in rows]
assert ids and len(ids)==len(set(ids)), 'Task IDs must be unique and every row must carry a valid status'
for tid, deps, _ in rows:
    for dep in re.findall(r'T\d{3}[a-z]?', deps):
        assert dep in ids, f'{tid} depends on unknown task {dep}'
assert '@AGENTS.md' in (ROOT/'CLAUDE.md').read_text()
f=json.loads((ROOT/'tests/fixtures/annual-review.json').read_text())
assert f['synthetic'] and f['production_eligible'] is False
v=f['values_cny']; old,new=map(D,v['revenue'])
assert str(((new/old-1)*100).quantize(D('.01'),rounding=ROUND_HALF_UP))==f['expected']['revenue_yoy_percent']
for i in range(2):
    rev,cost=D(v['revenue'][i]),D(v['cost'][i])
    assert (rev-cost)/rev*100 == D(f['expected']['gross_margin_percent'][i])
    assert D(v['operating_cash'][i])-D(v['net_profit'][i])==D(f['expected']['cash_profit_gap'][i])
    assert D(v['liabilities'][i])/D(v['assets'][i])*100==D(f['expected']['liability_asset_percent'][i])
print(f'PASS: {count} Markdown files, links/fences/JSON, task IDs and synthetic arithmetic. Not an application E2E test.')
