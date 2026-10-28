import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('publish_records', Path(__file__).with_name('publish-records.py'))
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublicationTests(unittest.TestCase):
    def test_append_only_records_and_contributor_index(self):
        with tempfile.TemporaryDirectory() as temp:
            source, root = Path(temp) / 'source', Path(temp) / 'repo'
            record = json.loads(next((Path(__file__).resolve().parents[1] / 'contributions/records').glob('*/*.json')).read_text())
            suffix = Path(record['manifest']['sha256']) / (record['invitation_id'] + '.json')
            receipt = source / 'records' / suffix
            receipt.parent.mkdir(parents=True)
            receipt.write_text(json.dumps(record))
            publisher.publish(source, root)
            index = (root / 'contributions/README.md').read_text()
            self.assertIn(record['contributor'].replace('_', '\\_'), index)
            self.assertIn('Not linked', index)
            identity = {'format': 'spr-github-identity-v1', 'manifest': record['manifest'], 'invitation_id': record['invitation_id'], 'contributor': record['contributor'], 'github_id': 123, 'github_login': 'alice', 'signature': 'verified-upstream'}
            linked = source / 'github' / suffix
            linked.parent.mkdir(parents=True)
            linked.write_text(json.dumps(identity))
            publisher.publish(source, root)
            publisher.publish(source, root)
            index = (root / 'contributions/README.md').read_text()
            self.assertIn('https://github.com/alice', index)
            self.assertIn('ID 123', index)
            self.assertIn(str(suffix), index)
            original = (root / 'contributions/github' / suffix).read_bytes()
            identity['github_id'] = 999
            linked.write_text(json.dumps(identity))
            with self.assertRaisesRegex(ValueError, 'Existing record differs'):
                publisher.publish(source, root)
            self.assertEqual((root / 'contributions/github' / suffix).read_bytes(), original)

    def test_rejects_unexpected_files_and_symlinks(self):
        with tempfile.TemporaryDirectory() as temp:
            source, root = Path(temp) / 'source', Path(temp) / 'repo'
            source.mkdir()
            bad = source / 'secrets.json'
            bad.write_text('{}')
            with self.assertRaises(ValueError):
                publisher.publish(source, root)
            bad.unlink()
            bad.symlink_to('/dev/null')
            with self.assertRaisesRegex(ValueError, 'symlink'):
                publisher.publish(source, root)

    def test_index_includes_unlinked_records_and_preserves_historical_files(self):
        with tempfile.TemporaryDirectory() as temp:
            source, root = Path(temp) / 'source', Path(temp) / 'repo'
            repo = Path(__file__).resolve().parents[1]
            records = sorted((repo / 'contributions/records').glob('*/*.json'))[:2]
            for path in records:
                dest = source / 'records' / path.parent.name / path.name
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(path.read_bytes())
            historical = root / 'contributions/sergeytimoshin.json'
            historical.parent.mkdir(parents=True)
            historical.write_bytes(b'historical export, preserved verbatim')
            old_index = root / 'attestations/README.md'
            old_index.parent.mkdir()
            old_index.write_text('Historical exports. See ../contributions/.')
            record = json.loads(records[0].read_text())
            identity = {'format': 'spr-github-identity-v1', 'manifest': record['manifest'], 'invitation_id': record['invitation_id'], 'contributor': record['contributor'], 'github_id': 123, 'github_login': 'alice', 'signature': 'verified-upstream'}
            path = source / 'github' / records[0].parent.name / records[0].name
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps(identity))
            publisher.publish(source, root)
            index = (root / 'contributions/README.md').read_text()
            for path in records:
                self.assertIn(f'records/{path.parent.name}/{path.name}', index)
                self.assertEqual((root / 'contributions/records' / path.parent.name / path.name).read_bytes(), path.read_bytes())
            self.assertEqual(index.count('| 41/41 |'), 2)
            self.assertEqual(index.count('Not linked'), 1)
            self.assertEqual(index.count('https://github.com/alice'), 1)
            self.assertIn('Historical exports', index)
            self.assertEqual(historical.read_bytes(), b'historical export, preserved verbatim')
            self.assertEqual(old_index.read_text(), 'Historical exports. See ../contributions/.')
            publisher.publish(source, root)
            self.assertEqual((root / 'contributions/README.md').read_text(), index)
