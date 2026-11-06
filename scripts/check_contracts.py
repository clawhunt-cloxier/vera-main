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

print('PASS: OpenAPI 3.1, plan/task/policy schemas, examples and invalid input cases; no business endpoints executed.')
