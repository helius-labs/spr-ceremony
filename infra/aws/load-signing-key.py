#!/usr/bin/env python3
"""Load only this manifest's private signing-key version; never log key bytes."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    os.umask(0o077)
    manifest = Path('/var/lib/spr/core/manifest.json').read_bytes()
    version = hashlib.sha256(manifest).hexdigest()
    secret = Path('/etc/spr/signing-secret').read_text().strip()
    result = subprocess.run(['aws', 'secretsmanager', 'get-secret-value', '--secret-id', secret,
                             '--version-id', version, '--output', 'json', '--cli-connect-timeout', '10',
                             '--cli-read-timeout', '30'], capture_output=True, timeout=50)
    if result.returncode != 0:
        raise RuntimeError('Signing key retrieval failed')
    value = json.loads(result.stdout)
    if value['VersionId'] != version:
        raise RuntimeError('Signing key version mismatch')
    private = value['SecretString'].encode('utf-8')
    if not 0 < len(private) <= 16384 or not private.startswith(b'-----BEGIN PGP PRIVATE KEY BLOCK-----'):
        raise RuntimeError('Invalid signing key')
    directory = Path('/var/lib/spr/signing')
    directory.mkdir(mode=0o700, exist_ok=True)
    fd, path = tempfile.mkstemp(prefix='.key-', dir=directory)
    try:
        with os.fdopen(fd, 'wb') as handle:
            handle.write(private)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(path, directory / 'coordinator.private.asc')
    finally:
        if os.path.exists(path): os.unlink(path)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        # Exception messages or subprocess output must never include secrets.
        sys.exit('Coordinator signing key unavailable; check the manifest version and IAM access.')
