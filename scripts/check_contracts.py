#!/usr/bin/env python3
"""OpenAPI and JSON Schema contract checks with invalid-input cases; no business endpoint is executed."""
from pathlib import Path
import copy
import json
from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import validate_spec

root = Path(__file__).resolve().parents[1]
api = json.loads((root/'contracts/openapi.json').read_text())
validate_spec(api)
schemas = api['components']['schemas']
draft = json.loads((root/'contracts/drafts/openapi-0.4.json').read_text())
validate_spec(draft)
draft_schemas = draft['components']['schemas']
assert 'snapshot_mode' in schemas['ResearchPlan']['properties']
assert 'snapshot_mode' in draft_schemas['ResearchPlan']['required']
assert 'quote_requirement' in schemas['ResearchPlan']['properties']
assert 'quote_requirement' in draft_schemas['ResearchPlan']['required']
for spec in (schemas, draft_schemas):
    plan_request_04 = Draft202012Validator(
        {'$ref': '#/components/schemas/CreatePlanV04', 'components': {'schemas': spec}},
        format_checker=FormatChecker(),
    )
    text_request = {'text': '研究该公司'}
    structured_request = {'skill_id': 'stock_analysis', 'subject_id': '00000000-0000-4000-8000-000000000001', 'params': {}}
    for request in (text_request, structured_request):
        plan_request_04.validate(request)
        for requirement in ('latest_available', 'realtime_trade'):
            plan_request_04.validate({**request, 'quote_requirement': requirement})
        for invalid in (None, '', 'unknown'):
            assert list(plan_request_04.iter_errors({**request, 'quote_requirement': invalid}))
        assert list(plan_request_04.iter_errors({**request, 'workspace_id': 'forged'}))
plan_mode_04 = Draft202012Validator({'type': 'object', 'properties': {k: draft_schemas['ResearchPlan']['properties'][k] for k in ('snapshot_mode', 'quote_requirement')},
                                   'required': ['snapshot_mode', 'quote_requirement'], 'allOf': draft_schemas['ResearchPlan']['allOf']})
assert not list(plan_mode_04.iter_errors({'snapshot_mode': 'collect_latest', 'quote_requirement': 'realtime_trade'}))
assert list(plan_mode_04.iter_errors({'snapshot_mode': 'collect_latest', 'quote_requirement': None}))
assert not list(plan_mode_04.iter_errors({'snapshot_mode': 'fixed_legacy', 'quote_requirement': None}))
assert list(plan_mode_04.iter_errors({'snapshot_mode': 'fixed_legacy', 'quote_requirement': 'realtime_trade'}))
for path in ('/api/v1/research-plans', '/api/v1/research-tasks'):
    assert 'application/vnd.vera.v0.4+json' in draft['paths'][path]['post']['requestBody']['content']
assert {'snapshot_mode', 'snapshot_as_of'} <= set(draft_schemas['Task']['required'])
assert 'available_at' in draft_schemas['SourceFreshness']['required']
report_rule = Draft202012Validator({
    'type': 'object',
    'properties': {'snapshot_mode': draft_schemas['Report']['properties']['snapshot_mode']},
    'required': ['snapshot_mode'],
    'allOf': draft_schemas['Report']['allOf'],
})
assert list(report_rule.iter_errors({'snapshot_mode': 'collect_latest'}))
assert not list(report_rule.iter_errors({'snapshot_mode': 'collect_latest', 'snapshot_as_of': 'example', 'source_freshness': []}))
assert not list(report_rule.iter_errors({'snapshot_mode': 'fixed_legacy'}))
freshness = Draft202012Validator(draft_schemas['SourceFreshness'], format_checker=FormatChecker())
source = {'dataset_id': 'mkt.intraday_quote', 'provider': 'example', 'event_at': None, 'published_at': None,
          'received_at': None, 'available_at': None, 'period': None, 'latency_mode': 'unknown',
          'status': 'unknown', 'note': 'synthetic test'}
assert not list(freshness.iter_errors(source))
assert list(freshness.iter_errors({**source, 'latency_mode': 'real_time'}))
assert not list(freshness.iter_errors({**source, 'latency_mode': 'real_time',
                                     'event_at': '2026-06-30T05:01:55Z',
                                     'received_at': '2026-06-30T05:01:56Z',
                                     'available_at': '2026-06-30T05:01:57Z'}))
assert 'application/vnd.vera.v0.4+json' in draft['paths']['/api/v1/research-plans']['post']['responses']['200']['content']


def load(name):
    schema = json.loads((root/f'contracts/schemas/{name}.schema.json').read_text())
    Draft202012Validator.check_schema(schema)
    example = json.loads((root/f'contracts/examples/{name}.json').read_text())
    validator = Draft202012Validator(schema, format_checker=FormatChecker())
    validator.validate(example)
    return schema, validator, example


def rejects(validator, value, label):
    assert list(validator.iter_errors(value)), f'{label} must be rejected'


# Research plan: free text or structured request; never client-controlled tenancy.
schema, v, ex = load('create-plan')
rejects(v, {**ex, 'workspace_id': 'client-controlled'}, 'unknown field')
rejects(v, {'text': '   '}, 'blank text')
rejects(v, {'text': 'x' * 501}, 'oversized text')
rejects(v, {'skill_id': 'macro', 'subject_id': '00000000-0000-4000-8000-000000000001', 'params': {}}, 'post-P0 skill')
rejects(v, {'skill_id': 'stock_analysis', 'subject_id': 'not-a-uuid', 'params': {}}, 'bad subject id')
rejects(v, {'text': '看看', 'skill_id': 'stock_analysis'}, 'mixed text and structured input')
assert schemas['CreatePlan'] == {k: val for k, val in schema.items() if k != '$schema'}

# Task creation only confirms a plan; skill, subject and data scope cannot be supplied by the client.
schema, v, ex = load('create-task')
for field, bad in [('tenant_id', 'x'), ('skill_id', 'stock_analysis'), ('entity_id', '00000000-0000-4000-8000-000000000001'),
                   ('validation_passed', True)]:
    rejects(v, {**ex, field: bad}, f'client field {field}')
rejects(v, {**ex, 'plan_hash': 'short'}, 'bad plan hash')
rejects(v, {**ex, 'plan_id': 'not-a-uuid'}, 'bad plan id')
rejects(v, {**ex, 'disabled_sections': ['S9', 'S9']}, 'duplicate disabled section')
assert schemas['CreateTask'] == {k: val for k, val in schema.items() if k != '$schema'}

# Budget/quota policy.
schema, v, ex = load('policy')
bad = copy.deepcopy(ex); bad['budgets']['stock_analysis']['cost_limit_usd'] = 0.2
rejects(v, bad, 'float money')
bad = copy.deepcopy(ex); del bad['budgets']['deep_research']
rejects(v, bad, 'missing skill budget')
bad = copy.deepcopy(ex); bad['model_call']['format_repairs'] = 3
rejects(v, bad, 'unbounded format repair')

# Task states follow ADR-0003; retired P0 states must not reappear.
states = set(schemas['Task']['properties']['status']['enum'])
assert {'COLLECTING', 'ANALYZING', 'ASSEMBLING', 'DELIVERING', 'COMPLETED', 'ABSTAINED'} <= states
assert not states & {'PREPARING', 'WAITING_EVIDENCE', 'VERIFYING', 'FINAL_CHECK', 'DEBATING', 'ESCALATING'}
assert schemas['Report']['properties']['ai_generated'] == {'const': True}

# Model output schemas must stay inside the provider strict-mode subset (HARNESS §6.2):
# every object closed and fully required; no length/count keywords, no null/boolean types.
ALLOWED = {'type', 'properties', 'required', 'additionalProperties', 'items', 'enum', 'pattern', 'description',
           'anyOf', 'minimum', 'maximum'}


def strict_subset(node, where):
    if isinstance(node, dict):
        bad = set(node) - ALLOWED
        assert not bad, f'{where}: keywords outside strict subset {sorted(bad)}'
        if node.get('type') == 'object':
            assert node.get('additionalProperties') is False, f'{where}: object must be closed'
            assert sorted(node.get('required', [])) == sorted(node.get('properties', {})), f'{where}: all properties required'
        assert node.get('type') not in ('null', 'boolean') and not isinstance(node.get('type'), list), f'{where}: type'
        for k, val in node.items():
            if k == 'properties':
                for pk, pv in val.items():
                    strict_subset(pv, f'{where}.{pk}')
            elif k in ('items',):
                strict_subset(val, f'{where}[]')
            elif k == 'anyOf':
                for i, sub in enumerate(val):
                    strict_subset(sub, f'{where}|{i}')


for name in ('section-output', 'verify-output', 'summary-output', 'plan-parse-output', 'conversation-answer-output', 'conversation-review-output', 'conversation-title-output',
             'conversation-window-tool', 'conversation-depth-tool', 'conversation-reference-tool', 'conversation-knowledge-tool', 'conversation-comparison-tool'):
    schema = json.loads((root/f'contracts/model/{name}.schema.json').read_text())
    Draft202012Validator.check_schema(schema)
    strict_subset(schema, name)
    Draft202012Validator(schema).validate(json.loads((root/f'contracts/model/examples/{name}.json').read_text()))
v = Draft202012Validator(json.loads((root/'contracts/model/section-output.schema.json').read_text()))
ex = json.loads((root/'contracts/model/examples/section-output.json').read_text())
bad = copy.deepcopy(ex); bad['claims'][0]['kind'] = 'rating'
rejects(v, bad, 'unknown claim kind')
bad = copy.deepcopy(ex); bad['claims'][0]['refs'] = ['E1']
rejects(v, bad, 'non-fact reference')

# Dataset field dictionary.
cat = json.loads((root/'contracts/datasets/catalog.json').read_text())
units = set(cat['units'])
items = {i['code'] for i in cat['statement_items']}
assert len(items) == len(cat['statement_items'])
ids = [ds['id'] for ds in cat['datasets']]
assert len(ids) == len(set(ids))
for ds in cat['datasets']:
    names = [f['name'] for f in ds['fields']]
    assert len(names) == len(set(names)), ds['id']
    assert ds['default_tier'] in ('L1', 'L2', 'L3', 'L4'), ds['id']
    for g in ds['grain']:
        assert g in names or g in ('entity_id', 'provider_id', 'raw_sha256'), f"{ds['id']}: grain {g}"
    for f in ds['fields']:
        assert f['unit'] in units, f"{ds['id']}.{f['name']}: unit"
        assert f['type'] in ('string', 'text', 'date', 'datetime', 'decimal', 'integer', 'boolean', 'enum', 'enum_ref'), f['name']
        if f['type'] == 'decimal':
            assert f['unit'] != 'none' or f['name'] == 'adj_factor', f"{ds['id']}.{f['name']}: decimal needs unit"
        if f['type'] == 'enum':
            assert f['values'], f['name']
        if f['type'] == 'enum_ref':
            assert f['ref'] == 'statement_items'
    if ds['origin'] == 'vendor_derived' and ds['default_tier'] == 'L4':
        assert any(f['name'] == 'method_id' for f in ds['fields']), f"{ds['id']}: L4 needs method_id"

# Demo universe fixture: synthetic only, internally consistent.
demo = json.loads((root/'tests/fixtures/demo-universe.json').read_text())
assert demo['synthetic'] is True and demo['production_eligible'] is False
codes = {s['code'] for s in demo['securities']}
assert all(c.endswith('.DEMO') for c in codes)
inds = {i['id'] for i in demo['industries']}
for i in demo['industries']:
    assert set(i['members']) <= {c.split('.')[0] for c in codes}, i['id']
adv = {a['id'] for a in demo['adversarial']}
skills = {'stock_analysis', 'capital_flow', 'deep_research'}
for c in demo['cases']:
    assert c['skill'] in skills, c['id']
    assert c['subject'] in codes | inds | {'market'}, c['id']
    assert ('expect_status' in c) != ('expect_plan' in c), c['id']
    assert set(c.get('adversarial', [])) <= adv, c['id']
covered = {c['id'][:2] for c in demo['cases']}
assert {'SA', 'CF', 'DR'} <= covered

print('PASS: OpenAPI 3.1, plan/task/policy schemas, model output strict subset, dataset dictionary, demo fixture; no business endpoints executed.')
