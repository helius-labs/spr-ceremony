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
        result = subprocess.run([binary, *map(str, args)], cwd=root, env=env, check=False, capture_output=True, text=True)
        if result.returncode:
            print(result.stdout, file=sys.stderr)
            print(result.stderr, file=sys.stderr)
            result.check_returncode()
        return result.stdout
    run('demo', '--out', 'ceremony')
    manifest = hashlib.sha256((root / 'ceremony/manifest.json').read_bytes()).hexdigest()
    if endpoint:
        # Each smoke run owns its own emulator bucket; Go test caching must
        # never determine whether this independent integration test works.
        bucket = 'spr-cli-' + uuid.uuid4().hex
        request = urllib.request.Request(endpoint.rstrip('/') + '/' + bucket, data=b'', method='PUT')
        with urllib.request.urlopen(request, timeout=10) as response:
            assert response.status == 200
        (root / 's3.json').write_text(json.dumps({'bucket': bucket, 'region': 'us-east-1',
                                                'prefix': 'smoke', 'endpoint': endpoint}))
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
            run('verify-attestation', '--dir', 'ceremony', '--attestation', 'attestation.json', '--manifest-sha256', manifest)
            status = json.loads(run('status', '--dir', 'ceremony'))
            assert status['lifecycle']['phase'] == 'open'
            assert len(status['circuits']) == 2 and all(len(c['history']) == 1 for c in status['circuits'])
            checkpoint = status['checkpoint']['sha256']
            public_audit = ('audit', '--url', origin, '--work', 'public-audit',
                            '--manifest-sha256', manifest,
                            '--checkpoint-sha256', checkpoint, '--workers', '2')
            for _ in range(2):
                result = run(*public_audit)
                assert 'Transcript verified: core' in result and checkpoint in result
            snapshot = root / 'public-audit' / manifest / checkpoint
            assert snapshot.is_dir()
            assert not (snapshot / 'invitations').exists()
            run('audit', '--dir', snapshot, '--manifest-sha256', manifest,
                '--checkpoint-sha256', checkpoint, '--workers', '2')
            run('export', '--dir', 'ceremony', '--out', 'keys', '--beacon', '42' * 32, '--workers', '2')
            run('verify-export', '--dir', 'ceremony', '--export', 'keys', '--manifest-sha256', manifest, '--workers', '2')
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
print('CLI contribution, retry, public audit, export, and configured recovery checks passed')
