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
