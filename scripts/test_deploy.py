import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('deploy', Path(__file__).with_name('deploy-aws.py'))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeploymentTests(unittest.TestCase):
    def test_rehearsal_cannot_be_published_or_treated_as_production(self):
        def manifest(test, count):
            return {'phase1': {'insecure_test_setup': test}, 'circuits': [{'spec': {'track': 'core'}} for _ in range(count)]}
        prepared, opened = {'lifecycle': {'phase': 'prepared'}}, {'lifecycle': {'phase': 'open'}}
        deploy.check_initial_campaign(manifest(True, 2), opened, True, 'ssm')
        deploy.check_initial_campaign(manifest(False, 41), prepared, False, 'public')
        deploy.check_initial_campaign(manifest(False, 41), prepared, False, 'ssm')
        for m, cp, rehearsal, mode in [
            (manifest(True, 2), opened, True, 'public'),
            (manifest(True, 41), prepared, False, 'public'),
            (manifest(True, 41), prepared, False, 'ssm'),
            (manifest(False, 41), prepared, True, 'ssm'),
            (manifest(False, 41), opened, False, 'public'),
            (manifest(False, 2), prepared, False, 'public'),
            (manifest(None, 41), prepared, False, 'public'),
            ({'phase1': {'insecure_test_setup': True}, 'circuits': [{'spec': {'track': 'large'}}]}, opened, True, 'ssm'),
        ]:
            with self.subTest(rehearsal=rehearsal, mode=mode), self.assertRaises(ValueError):
                deploy.check_initial_campaign(m, cp, rehearsal, mode)

    def test_rejects_archive_traversal_links_and_duplicates(self):
        for name, kind in [('../escape', 'file'), ('hosted.html', 'link'), ('hosted.html', 'duplicate')]:
            with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                with tarfile.open(root / 'browser.tar.gz', 'w:gz') as archive:
                    member = tarfile.TarInfo(name)
                    if kind == 'link':
                        member.type = tarfile.SYMTYPE; member.linkname = '/etc/passwd'; archive.addfile(member)
                    else:
                        member.size = 1; archive.addfile(member, io.BytesIO(b'x'))
                        if kind == 'duplicate': archive.addfile(member, io.BytesIO(b'x'))
                with self.assertRaises(ValueError): deploy.extract_browser(root / 'browser.tar.gz', root / 'output')
                self.assertFalse((root / 'escape').exists())

    def test_every_release_asset_is_pinned(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); revision = 'a' * 40
            (root / 'build.json').write_text(json.dumps({'source_revision': revision}))
            names = deploy.NATIVE + ['join.sh', 'build.json', f'browser-{revision}.tar.gz']
            for name in names:
                if name != 'build.json': (root / name).write_bytes(b'checked')
            lines = [f'{deploy.digest(root / name)}  {name}\n' for name in names]
            (root / 'SHA256SUMS').write_text(''.join(lines))
            deploy.checked_release(root, revision)
            (root / 'join.sh').write_bytes(b'tampered')
            with self.assertRaises(ValueError): deploy.checked_release(root, revision)
            (root / 'join.sh').write_bytes(b'checked')
            (root / 'SHA256SUMS').write_text(''.join(lines[:-1]))
            with self.assertRaises(ValueError): deploy.checked_release(root, revision)
