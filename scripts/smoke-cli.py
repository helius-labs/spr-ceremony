#!/usr/bin/env python3
"""Exercise participant and recovery commands against local test storage."""
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

binary = str(Path(sys.argv[1] if len(sys.argv) > 1 else './spr-ceremony').resolve())
endpoint = os.environ.get('SPR_S3_ENDPOINT')
env = dict(os.environ)
if endpoint:
    if not endpoint.startswith(('http://127.0.0.1:', 'http://localhost:')):
        raise SystemExit('Smoke test S3 endpoint must be loopback')
    env.update(AWS_ACCESS_KEY_ID='testing', AWS_SECRET_ACCESS_KEY='testing', AWS_EC2_METADATA_DISABLED='true')

with tempfile.TemporaryDirectory(prefix='spr-cli-') as temporary:
    root = Path(temporary)
    def run(*args):
        result = subprocess.run([binary, *map(str, args)], cwd=root, env=env, check=True, capture_output=True, text=True)
        return result.stdout
    run('demo', '--out', 'ceremony')
    manifest = hashlib.sha256((root / 'ceremony/manifest.json').read_bytes()).hexdigest()
    if endpoint:
        # The storage Go tests create this local test bucket first.
        (root / 's3.json').write_text(json.dumps({'bucket': 'spr-ceremony-test', 'region': 'us-east-1',
                                                'prefix': 'cli-' + uuid.uuid4().hex, 'endpoint': endpoint}))
        run('sync', '--dir', 'ceremony', '--s3-config', 's3.json')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    with (root / 'server.log').open('w') as log:
        server = subprocess.Popen([binary, 'serve', '--dir', str(root / 'ceremony'), '--listen', f'127.0.0.1:{port}'], env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(origin + '/healthz', timeout=1) as response:
                        if response.status == 200: break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError('coordinator did not start')
            run('invite', '--dir', 'ceremony', '--url', origin, '--name', 'alice', '--out', 'alice.json')
            run('contribute', '--invitation', 'alice.json')
            run('contribute', '--invitation', 'alice.json')
            run('verify-contribution', '--invitation', 'alice.json', '--out', 'attestation.json')
            assert 'token' not in json.loads((root / 'attestation.json').read_text())
            status = json.loads(run('status', '--dir', 'ceremony'))
            assert status['lifecycle']['phase'] == 'open'
            assert len(status['circuits']) == 2 and all(len(c['history']) == 1 for c in status['circuits'])
            run('export', '--dir', 'ceremony', '--out', 'keys', '--beacon', '42' * 32)
            run('verify-export', '--dir', 'ceremony', '--export', 'keys', '--manifest-sha256', manifest)
            if endpoint:
                run('sync', '--dir', 'ceremony', '--out', 'recovery.json')
                digest = hashlib.sha256((root / 'recovery.json').read_bytes()).hexdigest()
                run('restore', '--s3-config', 's3.json', '--recovery-file', 'recovery.json', '--recovery-sha256', digest, '--out', 'restored')
                run('audit', '--dir', 'restored', '--manifest-sha256', manifest)
                assert not (root / 'restored/invitations').exists()
        finally:
            server.terminate()
            server.wait(timeout=65)
            if server.returncode != 0:
                print((root / 'server.log').read_text(), file=sys.stderr)
                raise RuntimeError(f'coordinator exited {server.returncode}')
print('CLI contribution, retry, export, and configured recovery checks passed')
