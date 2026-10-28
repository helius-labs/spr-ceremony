#!/usr/bin/env python3
"""Load optional OAuth credentials from Secrets Manager without logging values."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    os.umask(0o077)
    secret = Path('/etc/spr/github-oauth-secret').read_text().strip()
    result = subprocess.run(['aws', 'secretsmanager', 'get-secret-value', '--secret-id', secret,
                             '--output', 'json', '--cli-connect-timeout', '10', '--cli-read-timeout', '30'],
                            capture_output=True, timeout=50)
    if result.returncode:
        raise RuntimeError('OAuth credentials unavailable')
    value = json.loads(json.loads(result.stdout)['SecretString'])
    if set(value) != {'client_id', 'client_secret'} or not all(isinstance(v, str) and 5 <= len(v) <= 256 and not any(c.isspace() for c in v) for v in value.values()):
        raise ValueError('Invalid OAuth credentials')
    directory = Path('/var/lib/spr/github')
    directory.mkdir(mode=0o700, exist_ok=True)
    fd, path = tempfile.mkstemp(prefix='.oauth-', dir=directory)
    try:
        with os.fdopen(fd, 'w') as handle:
            json.dump(value, handle)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(path, directory / 'oauth.json')
    finally:
        if os.path.exists(path):
            os.unlink(path)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        sys.exit('GitHub OAuth credentials unavailable; check Secrets Manager and IAM access.')
