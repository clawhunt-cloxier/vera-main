"""Offline cases for scripts/check_contract_compat.py; synthetic contracts only."""
import copy
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('compat', ROOT / 'scripts/check_contract_compat.py')
compat = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compat)

TASK = dict(type='object', additionalProperties=False, required=['id', 'status'], properties=dict(
    id=dict(type='string'), status=dict(type='string', enum=['QUEUED', 'DONE']), note=dict(type='string')))
CREATE = dict(type='object', additionalProperties=False, required=['text'], properties=dict(
    text=dict(type='string', maxLength=500), mode=dict(type='string', enum=['a', 'b'])))
BASE = dict(openapi='3.1.0', paths={
    '/tasks': dict(post=dict(
        requestBody=dict(required=True, content={'application/json': dict(schema={'$ref': '#/components/schemas/Create'})}),
        responses={'201': dict(content={'application/json': dict(schema={'$ref': '#/components/schemas/Task'})})})),
    '/tasks/{id}': dict(get=dict(
        parameters=[dict(name='id', required=True, schema=dict(type='string')), dict(name='view', schema=dict(type='string'))],
        responses={'200': dict(content={'application/json': dict(schema={'$ref': '#/components/schemas/Task'})})})),
}, components=dict(schemas=dict(Task=TASK, Create=CREATE)))


def run(mutate):
    new = copy.deepcopy(BASE)
    mutate(new)
    d = compat.compare(BASE, new, dict(), dict())
    return d.breaking, d.warnings


def schemas(doc):
    return doc['components']['schemas']


class CompatTest(unittest.TestCase):
    def assertBreaks(self, mutate, fragment):
        breaking, _ = run(mutate)
        self.assertTrue(any(fragment in b for b in breaking), f'{fragment!r} not in {breaking}')

    def test_identical_is_clean(self):
        self.assertEqual(run(lambda d: None), ([], []))

    def test_additive_changes_are_compatible(self):
        def mutate(d):
            schemas(d)['Task']['properties']['extra'] = dict(type='string')
            schemas(d)['Create']['properties']['opt'] = dict(type='string')
            schemas(d)['Create']['properties']['mode']['enum'].append('c')
            schemas(d)['Create']['properties']['text']['maxLength'] = 800
            d['paths']['/new'] = dict(get=dict(responses=dict()))
        self.assertEqual(run(mutate)[0], [])

    def test_removed_operation(self):
        self.assertBreaks(lambda d: d['paths'].pop('/tasks/{id}'), 'GET /tasks/{id}: operation removed')

    def test_removed_response_field(self):
        self.assertBreaks(lambda d: schemas(d)['Task']['properties'].pop('note'), 'response field note removed')

    def test_response_field_no_longer_required(self):
        self.assertBreaks(lambda d: schemas(d)['Task']['required'].remove('status'), 'status no longer required')

    def test_new_required_request_field(self):
        def mutate(d):
            schemas(d)['Create']['properties']['must'] = dict(type='string')
            schemas(d)['Create']['required'].append('must')
        self.assertBreaks(mutate, 'request field must became required')

    def test_request_enum_narrowed(self):
        self.assertBreaks(lambda d: schemas(d)['Create']['properties']['mode']['enum'].remove('b'), "no longer accepts ['b']")

    def test_request_limit_tightened(self):
        self.assertBreaks(lambda d: schemas(d)['Create']['properties']['text'].update(maxLength=100), 'maxLength tightened')

    def test_request_field_removed_from_closed_object(self):
        self.assertBreaks(lambda d: schemas(d)['Create']['properties'].pop('mode'), 'mode removed from closed object')

    def test_response_type_widened(self):
        self.assertBreaks(lambda d: schemas(d)['Task']['properties']['note'].update(type=['string', 'null']), 'response type widened')

    def test_parameter_became_required(self):
        def mutate(d):
            d['paths']['/tasks/{id}']['get']['parameters'][1]['required'] = True
        self.assertBreaks(mutate, 'parameter view is now required')

    def test_success_response_removed(self):
        self.assertBreaks(lambda d: d['paths']['/tasks']['post']['responses'].pop('201'), 'success response 201 removed')

    def test_response_enum_addition_is_warning(self):
        breaking, warnings = run(lambda d: schemas(d)['Task']['properties']['status']['enum'].append('FAILED'))
        self.assertEqual(breaking, [])
        self.assertTrue(any("gained ['FAILED']" in w for w in warnings))

    def test_standalone_request_schema_removed(self):
        d = compat.compare(BASE, BASE, {'x.schema.json': CREATE}, dict())
        self.assertIn('contracts/schemas/x.schema.json: request schema removed', d.breaking)

    def test_exceptions_require_reason_and_approval(self):
        tmp = ROOT / '.compat-exceptions-test.json'
        try:
            tmp.write_text('[{"finding": "x: y"}]')
            with self.assertRaises(SystemExit):
                compat.load_exceptions(tmp)
            tmp.write_text('[{"finding": "x: y", "reason": "window", "approved_in": "PR #1"}]')
            self.assertIn('x: y', compat.load_exceptions(tmp))
        finally:
            tmp.unlink(missing_ok=True)


if __name__ == '__main__':
    unittest.main()
