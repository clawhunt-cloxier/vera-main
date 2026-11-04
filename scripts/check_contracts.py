#!/usr/bin/env python3
from pathlib import Path
import json
from jsonschema import Draft202012Validator, FormatChecker, ValidationError
from openapi_spec_validator import validate_spec

root=Path(__file__).resolve().parents[1]
api=json.loads((root/'contracts/openapi.json').read_text())
validate_spec(api)
for name in ('create-task','policy'):
    schema=json.loads((root/f'contracts/schemas/{name}.schema.json').read_text())
    Draft202012Validator.check_schema(schema)
    validator=Draft202012Validator(schema,format_checker=FormatChecker())
    value=json.loads((root/f'contracts/examples/{name}.json').read_text())
    validator.validate(value)
    invalid=dict(value);invalid['tenant_id']='client-controlled'
    assert list(validator.iter_errors(invalid)), 'unknown fields must be rejected'
    if name=='create-task':
        for field,bad in [('purpose','internal_research'),('research_question',''),('research_as_of','not-a-date'),('entity_id','not-a-uuid')]:
            invalid=dict(value);invalid[field]=bad
            assert list(validator.iter_errors(invalid)), field
assert api['components']['schemas']['CreateTask']=={k:v for k,v in json.loads((root/'contracts/schemas/create-task.schema.json').read_text()).items() if k!='$schema'}
print('PASS: OpenAPI 3.1, JSON Schema examples and invalid input cases; no business endpoints executed.')
