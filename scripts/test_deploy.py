import ast
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('deploy', Path(__file__).with_name('deploy-aws.py'))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeploymentTests(unittest.TestCase):
    def test_rendered_runtime_configuration_keeps_admin_private(self):
        tree = ast.parse(Path(deploy.__file__).read_text())
        blocks = [node.value.split("<<'UNITS'\n", 1)[1].split('\nUNITS', 1)[0]
                  for node in ast.walk(tree) if isinstance(node, ast.Constant)
                  and isinstance(node.value, str) and "<<'UNITS'\n" in node.value]
        self.assertEqual(len(blocks), 1)
        code = compile(blocks[0], 'generated-runtime-configuration', 'exec')
        gateway = 'https://example.execute-api.eu-north-1.amazonaws.com'
        role = 'arn:aws:iam::123456789012:role/Operator'
        for mode, origin, operator, workers in [('ssm', 'http://127.0.0.1:8080', '', 1), ('cloudfront', 'https://example.cloudfront.net', '', 1), ('cloudfront', 'https://example.cloudfront.net', gateway, 1), ('cloudfront', 'https://example.cloudfront.net', gateway, 24), ('public', 'https://ceremony.example.com', '', 1)]:
            with self.subTest(mode=mode, operator=operator), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                for directory in ['etc/systemd/system', 'etc/caddy', 'usr/local/bin']:
                    (root / directory).mkdir(parents=True)
                for service, command, port in [('spr-ceremony', 'serve', 8080), ('spr-admin', 'admin', 8081)]:
                    (root / f'etc/systemd/system/{service}.service').write_text(
                        f'[Service]\nMemoryMax=1G\nExecStart=/opt/spr/current/native/spr-ceremony-linux-amd64 {command} --dir /var/lib/spr/core --listen 127.0.0.1:{port} --public-url http://127.0.0.1:8080\n')
                (root / 'usr/local/bin/spr-health').write_text("curl -H 'Host: 127.0.0.1:8080' http://127.0.0.1:8080/healthz\n")
                with patch('pathlib.Path', side_effect=lambda path: root / path.lstrip('/')), patch.object(sys, 'argv', ['configure', origin, mode, operator, role if operator else '', str(workers)]):
                    exec(code, {})
                    exec(code, {})
                admin = (root / 'etc/systemd/system/spr-admin.service').read_text()
                participant = (root / 'etc/systemd/system/spr-ceremony.service').read_text()
                self.assertIn('--listen 127.0.0.1:8081', admin)
                self.assertIn('--public-url ' + origin, admin)
                self.assertIn('--public-url ' + origin, participant)
                self.assertEqual('--trust-loopback-proxy' in participant, mode != 'ssm')
                self.assertIn('Host: ' + origin.split('://')[1], (root / 'usr/local/bin/spr-health').read_text())
                if mode == 'cloudfront':
                    proxy = (root / 'etc/caddy/Caddyfile').read_text()
                    self.assertIn('http://:8082', proxy)
                    self.assertIn('reverse_proxy 127.0.0.1:8080', proxy)
                    if operator:
                        self.assertIn('http://:8083', proxy)
                        self.assertIn('reverse_proxy 127.0.0.1:8081', proxy)
                        self.assertIn('Host ' + gateway.split('://')[1], proxy)
                        self.assertIn('--operator-origin ' + gateway, admin)
                        self.assertIn('--operator-role ' + role, admin)
                        self.assertIn('Environment=GOMAXPROCS=' + str(max(4, workers * 4)) + '\n', admin)
                        self.assertIn('Environment=SPR_FINALIZE_WORKERS=' + str(workers) + '\n', admin)
                        self.assertEqual(admin.count('Environment=GOMAXPROCS='), 1)
                        self.assertEqual(admin.count('Environment=SPR_FINALIZE_WORKERS='), 1)
                        self.assertIn('MemoryMax=' + str(max(12, workers * 4)) + 'G', admin)
                    else:
                        self.assertNotIn('8081', proxy)
                        self.assertNotIn('--operator-origin', admin)
                    self.assertIn('Host example.cloudfront.net', proxy)

    def test_hosted_build_comparison_accepts_serialization_and_rejects_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            actual, expected = Path(tmp) / 'actual.json', Path(tmp) / 'expected.json'
            value = {'source_revision': 'a' * 40, 'source_modified': False, 'assets': {'worker.js': {'sha256': 'b' * 64, 'size': 5}}}
            expected.write_text(json.dumps(value, indent=2) + '\n')
            actual.write_text(json.dumps(value, separators=(',', ':')))
            command = [sys.executable, '-c', deploy.CHECK_HOSTED_BUILD, str(actual), str(expected)]
            self.assertEqual(subprocess.run(command, capture_output=True).returncode, 0)
            value['assets']['worker.js']['sha256'] = 'c' * 64
            actual.write_text(json.dumps(value))
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)
            value['source_modified'] = True
            actual.write_text(json.dumps(value)); expected.write_text(json.dumps(value))
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)

    def test_rehearsal_cannot_be_published_or_treated_as_production(self):
        def manifest(test, count):
            return {'coordinator_key': {'sha256': 'd' * 64, 'size': 1024}, 'phase1': {'insecure_test_setup': test}, 'circuits': [{'spec': {'track': 'core'}} for _ in range(count)]}
        prepared, opened = {'lifecycle': {'phase': 'prepared'}}, {'lifecycle': {'phase': 'open'}}
        deploy.check_initial_campaign(manifest(True, 2), opened, True, 'ssm')
        deploy.check_initial_campaign(manifest(True, 41), prepared, True, 'cloudfront')
        deploy.check_initial_campaign(manifest(True, 41), prepared, True, 'cloudfront', background_audit=True)
        for m, cp, rehearsal in [(manifest(False, 41), prepared, False), (manifest(True, 41), opened, True)]:
            with self.assertRaises(ValueError):
                deploy.check_initial_campaign(m, cp, rehearsal, 'cloudfront', background_audit=True)
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

    def test_background_audit_freezes_initial_snapshot(self):
        recovery = {'manifest': {'sha256': 'a' * 64}, 'checkpoint': {'sha256': 'b' * 64}}
        for background in [False, True]:
            script = deploy.initial_audit_command('/opt/release/spr-ceremony', '/var/lib/spr/incoming', recovery, background)
            result = subprocess.run(['bash', '-n'], input=script, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0)
            self.assertIn('--manifest-sha256 ' + 'a' * 64, script)
            self.assertIn('--checkpoint-sha256 ' + 'b' * 64, script)
            if background:
                self.assertIn('cp -al /var/lib/spr/incoming /var/lib/spr/initial-audit-', script)
                self.assertIn('systemd-run', script)
                self.assertNotIn('audit --dir /var/lib/spr/incoming', script)
            else:
                self.assertIn('audit --dir /var/lib/spr/incoming', script)
                self.assertNotIn('systemd-run', script)

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
