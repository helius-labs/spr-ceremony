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
import re
import shlex
import subprocess
import sys
import tempfile
import warnings


class ConfigurationError(Exception):
    """Only fixed, safe diagnostic messages belong in this exception."""


def aws_error(result):
    # Never print raw AWS stdout/stderr: providers can echo request values.
    message = result.stderr.decode('utf-8', errors='replace').lower()
    if any(word in message for word in ['expired', 'sso token', 'sso session', 'invalid_grant', 'invalidclienttokenid', 'unrecognizedclientexception', 'unauthorizedexception', 'unable to locate credentials']):
        return 'AWS login is missing or expired. Run the SSO login command above, then retry.'
    if 'profile' in message and any(word in message for word in ['could not be found', 'not found', 'does not exist']):
        return 'AWS profile was not found. Check its name with: aws configure list-profiles'
    if 'accessdenied' in message or 'unauthorizedoperation' in message:
        return 'AWS denied access. This profile needs secretsmanager:DescribeSecret and secretsmanager:CreateSecret for the requested secret.'
    if 'resourceexistsexception' in message:
        return 'That secret name already exists. Nothing was overwritten. Run with --check to inspect it, or choose another --name.'
    if 'invalidrequestexception' in message:
        return 'AWS rejected the secret state. Check whether that name is scheduled for deletion, or choose another --name.'
    if 'invalidparameterexception' in message or 'validationexception' in message:
        return 'AWS rejected a parameter. Check --name and --region.'
    if 'limitexceededexception' in message or 'throttling' in message:
        return 'AWS quota or request limit reached. Check the Secrets Manager quota, then retry.'
    if any(word in message for word in ['could not connect', 'ssl validation', 'proxy', 'timed out', 'name or service not known', 'connection aborted']):
        return 'Cannot reach AWS securely. Check your network, VPN/proxy and AWS CLI certificate configuration, then retry.'
    return 'AWS command failed. Raw output was suppressed to protect credentials. Run with --check to isolate the AWS login/metadata step.'


def run_aws(base, *arguments, missing_ok=False):
    try:
        result = subprocess.run(base + list(arguments), capture_output=True, timeout=90,
                                env={**os.environ, 'AWS_PAGER': '', 'AWS_CLI_AUTO_PROMPT': 'off'})
    except FileNotFoundError:
        raise ConfigurationError('AWS CLI was not found. Install it and ensure aws is on PATH.') from None
    except subprocess.TimeoutExpired:
        raise ConfigurationError('AWS request timed out. Run with --check before retrying; a create request may already have succeeded.') from None
    if result.returncode:
        if missing_ok and b'(ResourceNotFoundException)' in result.stderr:
            return None
        raise ConfigurationError(aws_error(result))
    try:
        value = json.loads(result.stdout)
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (ValueError, TypeError):
        raise ConfigurationError('AWS returned an unexpected response. Run with --check before retrying; the secret may already exist.') from None


def secret_arn(value):
    arn = value.get('ARN', '')
    if not isinstance(arn, str) or not re.fullmatch(r'arn:aws[a-z-]*:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@-]+', arn):
        raise ConfigurationError('AWS returned no valid secret ARN. Run with --check before retrying.')
    return arn


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', required=True)
    parser.add_argument('--region', required=True)
    parser.add_argument('--name', default='spr-ceremony/github-oauth')
    parser.add_argument('--check', action='store_true', help='check AWS login and secret metadata without reading, prompting for or changing credentials')
    args = parser.parse_args()
    if not args.check and not sys.stdin.isatty():
        raise ConfigurationError('Run from an interactive terminal; use --check for a noninteractive AWS diagnostic.')
    aws = ['aws', '--profile', args.profile, '--region', args.region, '--output', 'json',
           '--cli-connect-timeout', '10', '--cli-read-timeout', '30']
    print('SSO login if needed: ' + shlex.join(['aws', 'sso', 'login', '--profile', args.profile]))
    print('Checking AWS login and secret metadata…', flush=True)
    identity = run_aws(aws, 'sts', 'get-caller-identity')
    account = identity.get('Account', '')
    if not isinstance(account, str) or not re.fullmatch(r'[0-9]{12}', account):
        raise ConfigurationError('AWS returned no valid account ID. Check the selected profile.')
    print('AWS account: ' + account)
    existing = run_aws(aws, 'secretsmanager', 'describe-secret', '--secret-id', args.name, missing_ok=True)
    if existing is not None:
        print('Existing secret ARN: ' + secret_arn(existing))
        if existing.get('DeletedDate'):
            raise ConfigurationError('That secret is scheduled for deletion. Restore it explicitly in AWS, or choose another --name.')
        if args.check:
            print('AWS access works. The secret exists; its value was not read or changed.')
            return
        raise ConfigurationError('That secret name already exists; its contents were not checked. Nothing was overwritten. Use another --name for a new app.')
    if args.check:
        print('AWS login and metadata access work. The secret does not exist yet. Run without --check to create it; write permission is checked on creation.')
        return
    client_id = input('GitHub OAuth client ID: ').strip()
    with warnings.catch_warnings():
        warnings.simplefilter('error', getpass.GetPassWarning)
        try:
            client_secret = getpass.getpass('GitHub OAuth client secret (hidden): ').strip()
        except getpass.GetPassWarning:
            raise ConfigurationError('Cannot hide terminal input. Run in a normal interactive terminal; no secret was requested with echo enabled.') from None
    if not 5 <= len(client_id) <= 128 or not 16 <= len(client_secret) <= 256 or any(c.isspace() for c in client_id + client_secret):
        raise ConfigurationError('Invalid OAuth credential format. Copy the client ID and generated client secret without labels or embedded whitespace.')
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='spr-oauth-') as directory:
        path = Path(directory) / 'credentials.json'
        path.write_text(json.dumps({'client_id': client_id, 'client_secret': client_secret}))
        # Create-only: accidental reuse cannot rotate an existing application's
        # secret during an active ceremony. Rotation is an explicit AWS action.
        value = run_aws(aws, 'secretsmanager', 'create-secret', '--name', args.name,
                                '--description', 'Solana Privacy Rings optional public-profile GitHub linking',
                                '--secret-string', 'file://' + str(path))
        arn = secret_arn(value)
        print('Saved. Add this non-secret ARN to the deployment configuration:')
        print('github_oauth_secret_arn = ' + json.dumps(arn))
        print('No deployment or coordinator restart was performed.')


def entrypoint():
    try:
        main()
    except ConfigurationError as error:
        sys.exit('OAuth configuration: ' + str(error))
    except (KeyboardInterrupt, EOFError):
        sys.exit('OAuth configuration cancelled. No secret values were printed.')
    except Exception:
        sys.exit('OAuth configuration could not finish locally. Check AWS CLI installation and temporary-directory access; use --check to test AWS separately. No secret values were printed.')


if __name__ == '__main__':
    entrypoint()
