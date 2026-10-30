import copy
import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location('scheduled', Path(__file__).with_name('recover-beacon-scheduled.py'))
scheduled = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scheduled)


class ScheduledRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'objects').mkdir()
        (self.root / 'manifest.json').write_text('{}')
        self.binary = self.root / 'cli'
        self.binary.write_text('pinned binary')
        self.old = dt.datetime(2026, 10, 30, 14, 12, 36, tzinfo=dt.timezone.utc)
        self.plan = {'manifest_sha256': scheduled.sha(self.root / 'manifest.json'),
                     'binary_sha256': scheduled.sha(self.binary), 'old_round': 33522064, 'new_round': 33536464,
                     'old_deadline': self.old.isoformat(), 'new_deadline': (self.old + dt.timedelta(hours=12)).isoformat(),
                     'chain_hash': '52db9ba70e0cc0f6eaf7803dd07447a1f5477735fd3f661792ba94600c84e971',
                     'reason': 'Operator requested 12 more hours for contributions'}
        self.cp = {'manifest': {'sha256': self.plan['manifest_sha256']}, 'states': {'circuit': {'sha256': 'a' * 64}},
                   'lifecycle': {'phase': 'open', 'beacon': {'round': self.plan['old_round'], 'round_time': self.plan['old_deadline'], 'chain_hash': self.plan['chain_hash']}}}
        self.ref = self.save(self.cp)

    def save(self, cp):
        data = json.dumps(cp).encode()
        ref = {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
        (self.root / 'objects' / ref['sha256']).write_bytes(data)
        (self.root / 'checkpoint.json').write_text(json.dumps(ref))
        return ref

    def run_recovery(self, now, check=False):
        return scheduled.execute(self.plan, self.root, self.binary, self.root / 's3.json', now, check)

    def test_preflight_is_read_only_and_execution_waits_for_original_deadline(self):
        with patch.object(scheduled.subprocess, 'run') as cli:
            self.assertEqual(self.run_recovery(self.old - dt.timedelta(hours=1), True)['status'], 'ready')
            with self.assertRaisesRegex(ValueError, 'has not passed'):
                self.run_recovery(self.old - dt.timedelta(seconds=1))
            cli.assert_not_called()
        self.assertEqual(scheduled.checkpoint(self.root)[0], self.ref)

    def test_closed_changed_plan_bad_binary_and_late_timer_fail_without_execution(self):
        with patch.object(scheduled.subprocess, 'run') as cli:
            for phase in ['closed', 'finalized']:
                cp = copy.deepcopy(self.cp); cp['lifecycle']['phase'] = phase; self.save(cp)
                with self.assertRaises(ValueError): self.run_recovery(self.old)
            cp = copy.deepcopy(self.cp); cp['lifecycle']['beacon']['round'] += 1; self.save(cp)
            with self.assertRaises(ValueError): self.run_recovery(self.old)
            self.save(self.cp)
            with self.assertRaisesRegex(ValueError, 'too near'):
                self.run_recovery(self.old + dt.timedelta(hours=11))
            self.binary.write_text('changed')
            with self.assertRaisesRegex(ValueError, 'Binary or ceremony'):
                self.run_recovery(self.old)
            cli.assert_not_called()

    def test_uses_cli_snapshot_and_is_idempotent_even_if_contributions_resume(self):
        def recover(arguments, **kwargs):
            self.assertIn('--s3-config', arguments)
            self.assertEqual(arguments[arguments.index('--checkpoint-sha256') + 1], self.ref['sha256'])
            self.assertEqual(arguments[arguments.index('--round') + 1], str(self.plan['new_round']))
            after = copy.deepcopy(self.cp)
            before = copy.deepcopy(after['lifecycle']['beacon'])
            after['lifecycle']['beacon'].update(round=self.plan['new_round'], round_time=self.plan['new_deadline'])
            after['lifecycle']['beacon_recoveries'] = [{'previous': before, 'next': copy.deepcopy(after['lifecycle']['beacon']), 'reason': self.plan['reason']}]
            after['previous'] = self.ref
            ref = self.save(after)
            after['previous'] = ref
            after['states']['circuit'] = {'sha256': 'b' * 64}
            self.save(after)  # A participant advances latest after recovery.
            return SimpleNamespace(returncode=0, stdout=json.dumps(ref))
        with patch.object(scheduled.subprocess, 'run', side_effect=recover) as cli:
            result = self.run_recovery(self.old + dt.timedelta(seconds=5))
            self.assertTrue(result['circuit_states_unchanged'])
            self.assertEqual(self.run_recovery(self.old + dt.timedelta(seconds=6))['status'], 'already_recovered')
            self.assertEqual(cli.call_count, 1)

    def test_failure_preserves_checkpoint_and_does_not_edit_store(self):
        with patch.object(scheduled.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout='')):
            with self.assertRaises(RuntimeError): self.run_recovery(self.old)
        self.assertEqual(scheduled.checkpoint(self.root)[0], self.ref)
