import hashlib
from itertools import product
import os
from pathlib import Path
import pty
import subprocess
import tempfile
import unittest


class InstallerTests(unittest.TestCase):
    def test_platforms_checksum_and_cleanup(self):
        for (platform, system, machine), action in product([('DARWIN_ARM64', 'Darwin', 'arm64'), ('DARWIN_AMD64', 'Darwin', 'x86_64'), ('LINUX_ARM64', 'Linux', 'aarch64'), ('LINUX_AMD64', 'Linux', 'x86_64')], ['join', 'link-github']):
            with self.subTest(platform=platform, action=action), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                binary = b'#!/bin/bash\nprintf "%s\\n" "$@" > "$SPR_TEST_ARGS"\n'
                template = Path(__file__).with_name('join.sh.in').read_text().replace('@VERSION@', 'v0.1.0').replace('@REVISION@', 'a' * 40)
                for target in ['DARWIN_ARM64', 'DARWIN_AMD64', 'LINUX_ARM64', 'LINUX_AMD64']:
                    template = template.replace('@' + target + '@', hashlib.sha256(binary).hexdigest() if target == platform else '0' * 64)
                (root / 'join.sh').write_text(template)
                (root / 'binary').write_bytes(binary)
                (root / 'curl').write_text('#!/bin/bash\nfor last; do :; done\ncp "$SPR_TEST_BINARY" "$last"\n')
                (root / 'uname').write_text(f'#!/bin/bash\nif [[ "$1" == -s ]]; then echo {system}; else echo {machine}; fi\n')
                for name in ['curl', 'uname']: (root / name).chmod(0o700)
                env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'], TMPDIR=tmp, SPR_TEST_BINARY=str(root / 'binary'), SPR_TEST_ARGS=str(root / 'args'), SPR_CEREMONY_WORK=str(root / 'public-work'))
                pid, master = pty.fork()
                if pid == 0:
                    os.execve('/bin/bash', ['bash', str(root / 'join.sh'), action], env)
                output = b''
                try:
                    while True:
                        try: chunk = os.read(master, 4096)
                        except OSError: break
                        if not chunk: break
                        output += chunk
                    _, status = os.waitpid(pid, 0)
                finally: os.close(master)
                self.assertEqual(os.waitstatus_to_exitcode(status), 0, output)
                self.assertEqual((root / 'args').read_text().splitlines(), ['join', '--work', str(root / 'public-work')] if action == 'join' else ['link-github'])
                self.assertFalse(list(root.glob('spr-ceremony.*')))
                (root / 'args').unlink()
                (root / 'binary').write_bytes(binary + b'# tampered\n')
                failed = subprocess.run(['bash', str(root / 'join.sh')], env=env, capture_output=True)
                self.assertNotEqual(failed.returncode, 0)
                self.assertIn(b'checksum mismatch', failed.stderr)
                self.assertFalse((root / 'args').exists())
                self.assertFalse(list(root.glob('spr-ceremony.*')))

    def test_rejects_insecure_origin(self):
        result = subprocess.run(['bash', str(Path(__file__).with_name('join.sh.in'))], env=dict(os.environ, SPR_CEREMONY_DOWNLOAD_BASE='http://example.test'), capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'HTTPS', result.stderr)

    def test_rejects_unknown_actions_and_extra_arguments(self):
        for args in [['unexpected'], ['link-github', 'private-invitation']]:
            result = subprocess.run(['bash', str(Path(__file__).with_name('join.sh.in')), *args], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b'Usage:', result.stderr)
            self.assertNotIn(b'private-invitation', result.stderr)
