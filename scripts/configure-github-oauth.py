#!/usr/bin/env python3
"""Store a dedicated GitHub OAuth App's credentials directly in AWS Secrets Manager.

Run on the operator's machine. The secret is read at a hidden terminal prompt,
passed to AWS through a private temporary file, and never printed or put in argv.
This command does not deploy or restart the coordinator.
"""
import argparse
import getpass
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', required=True)
    parser.add_argument('--region', required=True)
    parser.add_argument('--name', default='spr-ceremony/github-oauth')
    args = parser.parse_args()
    if not sys.stdin.isatty():
        raise ValueError('Run from an interactive terminal')
    client_id = input('GitHub OAuth client ID: ').strip()
    client_secret = getpass.getpass('GitHub OAuth client secret (hidden): ').strip()
    if not 5 <= len(client_id) <= 128 or not 16 <= len(client_secret) <= 256 or any(c.isspace() for c in client_id + client_secret):
        raise ValueError('Invalid OAuth credentials')
    os.umask(0o077)
    aws = ['aws', '--profile', args.profile, '--region', args.region, '--output', 'json', 'secretsmanager']
    with tempfile.TemporaryDirectory(prefix='spr-oauth-') as directory:
        path = Path(directory) / 'credentials.json'
        path.write_text(json.dumps({'client_id': client_id, 'client_secret': client_secret}))
        # Create-only: accidental reuse cannot rotate an existing application's
        # secret during an active ceremony. Rotation is an explicit AWS action.
        result = subprocess.run(aws + ['create-secret', '--name', args.name,
                                '--description', 'Solana Privacy Rings optional public-profile GitHub linking',
                                '--secret-string', 'file://' + str(path)], capture_output=True)
        if result.returncode:
            raise RuntimeError('Could not create secret; check AWS login, permissions and whether the name already exists')
        print('Saved. Add this non-secret ARN to the deployment configuration:')
        print('github_oauth_secret_arn = ' + json.dumps(json.loads(result.stdout)['ARN']))
        print('No deployment or coordinator restart was performed.')


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt):
        sys.exit('OAuth configuration was not completed. No secret values were printed.')
