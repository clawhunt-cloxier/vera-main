"""Offline checks for the deletion guards; never contacts GitHub."""
import copy
from pathlib import Path
import unittest
from unittest.mock import Mock
import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = yaml.load((ROOT / '.github/workflows/cleanup-merged-branch.yml').read_text(), Loader=yaml.BaseLoader)
SCOPE = {'__name__': 'branch_cleanup_test'}
exec(compile(WORKFLOW['jobs']['cleanup']['steps'][0]['run'], 'cleanup-workflow', 'exec'), SCOPE)
cleanup = SCOPE['cleanup']

class CleanupTest(unittest.TestCase):
    def setUp(self):
        self.repo = 'owner/vera'
        self.pr = {'number': 1, 'state': 'closed', 'merged': True,
                   'base': {'ref': 'dev'},
                   'head': {'ref': 'agent/codex/task', 'sha': 'a' * 40,
                            'repo': {'full_name': self.repo}}}
        self.event = {'action': 'closed', 'pull_request': copy.deepcopy(self.pr)}
        self.current = copy.deepcopy(self.pr)
        self.default = 'main'
        self.ref = {'object': {'sha': 'a' * 40}}
        self.open_filter = None
        self.remove = Mock()

    def api(self, path, missing_ok=False):
        if path == '/pulls/1':
            return self.current
        if path == '':
            return {'default_branch': self.default}
        if path.startswith('/pulls?'):
            return [self.pr] if self.open_filter and self.open_filter in path else []
        if path.startswith('/git/ref/heads/'):
            return self.ref
        raise AssertionError(path)

    def run_cleanup(self):
        return cleanup(self.event, self.repo, self.api, self.remove)

    def test_merged_task(self):
        self.assertIn('Deleted', self.run_cleanup())
        self.remove.assert_called_once_with('agent/codex/task', 'a' * 40)

    def test_ineligible_events(self):
        for field, value in [('merged', False), ('base', {'ref': 'main'}),
                             ('head', {**self.pr['head'], 'repo': {'full_name': 'fork/vera'}})]:
            with self.subTest(field=field):
                self.event['pull_request'] = {**copy.deepcopy(self.pr), field: value}
                self.run_cleanup()
                self.remove.assert_not_called()

    def test_preserve_special_branches(self):
        for name in ['main', 'dev', 'release/v1', 'hotfix/urgent', 'agent/']:
            with self.subTest(name=name):
                self.event['pull_request']['head']['ref'] = name
                self.run_cleanup()
                self.remove.assert_not_called()

    def test_preserve_dynamic_default(self):
        self.default = self.pr['head']['ref']
        self.run_cleanup()
        self.remove.assert_not_called()

    def test_open_pr_source_or_base(self):
        for key in ['head=', 'base=']:
            with self.subTest(key=key):
                self.open_filter = key
                self.run_cleanup()
                self.remove.assert_not_called()

    def test_updated_pr(self):
        self.current['head']['sha'] = 'b' * 40
        self.run_cleanup()
        self.remove.assert_not_called()

    def test_new_commits_or_already_deleted(self):
        for ref in [{'object': {'sha': 'b' * 40}}, None]:
            with self.subTest(ref=ref):
                self.ref = ref
                self.run_cleanup()
                self.remove.assert_not_called()

    def test_api_failure_never_deletes(self):
        with self.assertRaises(PermissionError):
            cleanup(self.event, self.repo, Mock(side_effect=PermissionError), self.remove)
        self.remove.assert_not_called()

    def test_deletion_rejection_is_not_hidden(self):
        self.remove.side_effect = RuntimeError('lease or branch protection rejected deletion')
        with self.assertRaises(RuntimeError):
            self.run_cleanup()

if __name__ == '__main__':
    unittest.main()
