import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('configure_github_oauth', Path(__file__).with_name('configure-github-oauth.py'))
configure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(configure)

ARN = 'arn:aws:secretsmanager:eu-north-1:558215002830:secret:spr-ceremony/github-oauth-AbCd12'
SECRET = 'test-secret-never-print-123456789'


def response(value=None, error=None):
    return subprocess.CompletedProcess([], 254 if error else 0, stdout=json.dumps(value).encode(), stderr=(error or '').encode())


def missing():
    return response(error='An error occurred (ResourceNotFoundException) when calling DescribeSecret')


class ConfigureGitHubTests(unittest.TestCase):
    def invoke(self, runner, check=False):
        output = io.StringIO()
        args = ['configure', '--profile', 'test-sso', '--region', 'eu-north-1'] + (['--check'] if check else [])
        prior_mask = os.umask(0o077)
        try:
            with patch.object(sys, 'argv', args), patch.object(sys.stdin, 'isatty', return_value=not check), \
                 patch.object(configure.subprocess, 'run', side_effect=runner), \
                 patch('builtins.input', return_value='Ov23-public-client-id') as client_id, \
                 patch.object(configure.getpass, 'getpass', return_value=SECRET) as secret, \
                 contextlib.redirect_stdout(output):
                error = None
                try:
                    configure.entrypoint()
                except SystemExit as failure:
                    error = str(failure)
                return output.getvalue(), error, client_id.call_count, secret.call_count
        finally:
            os.umask(prior_mask)

    def test_expired_sso_fails_before_asking_for_credentials(self):
        out, error, client_calls, secret_calls = self.invoke(lambda *a, **kw: response(error='Error when retrieving token from sso: Token has expired and refresh failed ' + SECRET))
        self.assertIn('AWS login is missing or expired', error)
        self.assertIn('aws sso login --profile test-sso', out)
        self.assertEqual((client_calls, secret_calls), (0, 0))
        self.assertNotIn(SECRET, out + error)

    def test_check_is_read_only_and_never_prompts(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            if 'get-caller-identity' in argv:
                return response({'Account': '558215002830'})
            self.assertIn('describe-secret', argv)
            return missing()
        out, error, client_calls, secret_calls = self.invoke(run, check=True)
        self.assertIsNone(error)
        self.assertIn('does not exist yet', out)
        self.assertEqual((client_calls, secret_calls), (0, 0))
        self.assertEqual(len(calls), 2)

    def test_success_uses_private_file_not_argv_and_cleans_up(self):
        paths = []
        def run(argv, **kwargs):
            self.assertNotIn(SECRET, ' '.join(argv))
            self.assertEqual(kwargs['env']['AWS_PAGER'], '')
            self.assertEqual(kwargs['env']['AWS_CLI_AUTO_PROMPT'], 'off')
            if 'get-caller-identity' in argv:
                return response({'Account': '558215002830'})
            if 'describe-secret' in argv:
                return missing()
            self.assertIn('create-secret', argv)
            path = Path(argv[argv.index('--secret-string') + 1].removeprefix('file://'))
            paths.append(path)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(json.loads(path.read_text())['client_secret'], SECRET)
            return response({'ARN': ARN})
        out, error, client_calls, secret_calls = self.invoke(run)
        self.assertIsNone(error)
        self.assertIn(ARN, out)
        self.assertNotIn(SECRET, out)
        self.assertEqual((client_calls, secret_calls), (1, 1))
        self.assertEqual(len(paths), 1)
        self.assertFalse(paths[0].exists())

    def test_permission_failure_is_safe_and_cleans_up(self):
        paths = []
        def run(argv, **kwargs):
            if 'get-caller-identity' in argv: return response({'Account': '558215002830'})
            if 'describe-secret' in argv: return missing()
            paths.append(Path(argv[argv.index('--secret-string') + 1].removeprefix('file://')))
            return response(error='An error occurred (AccessDeniedException) when calling CreateSecret: ' + SECRET)
        out, error, _, _ = self.invoke(run)
        self.assertIn('AWS denied access', error)
        self.assertNotIn(SECRET, out + error)
        self.assertFalse(paths[0].exists())

    def test_existing_secret_is_not_overwritten_or_read(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            if 'get-caller-identity' in argv: return response({'Account': '558215002830'})
            self.assertIn('describe-secret', argv)
            return response({'ARN': ARN})
        out, error, client_calls, secret_calls = self.invoke(run)
        self.assertIn('Nothing was overwritten', error)
        self.assertIn(ARN, out)
        self.assertEqual((client_calls, secret_calls), (0, 0))
        self.assertEqual(len(calls), 2)

    def test_unexpected_output_and_missing_cli_do_not_leak(self):
        for runner, expected in [
            (lambda *a, **kw: response(error='unknown failure ' + SECRET), 'Raw output was suppressed'),
            (lambda *a, **kw: subprocess.CompletedProcess([], 0, SECRET.encode(), b''), 'unexpected response'),
            (lambda *a, **kw: (_ for _ in ()).throw(FileNotFoundError(SECRET)), 'AWS CLI was not found'),
        ]:
            with self.subTest(expected=expected):
                out, error, client_calls, secret_calls = self.invoke(runner)
                self.assertIn(expected, error)
                self.assertNotIn(SECRET, out + error)
                self.assertEqual((client_calls, secret_calls), (0, 0))
