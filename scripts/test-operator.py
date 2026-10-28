#!/usr/bin/env python3
"""Exercise the operator commands and private-file participant handoff."""
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    binary = str(Path(sys.argv[1]).resolve())
    with tempfile.TemporaryDirectory(prefix='spr-operator-cli-') as temporary:
        root = Path(temporary)
        def cli(*args, success=True):
            result = subprocess.run([binary, *map(str, args)], cwd=root, capture_output=True, text=True, timeout=90)
            assert (result.returncode == 0) == success, 'Command failed: ' + ' '.join(map(str, args[:3]))
            return result.stdout + result.stderr
        cli('demo', '--out', 'ceremony')
        public, control = f'http://127.0.0.1:{port()}', f'http://127.0.0.1:{port()}'
        processes = []
        with (root / 'servers.log').open('w') as log:
            try:
                for command, origin, path in [('serve', public, '/healthz'), ('admin', control, '/')]:
                    flags = ['--public-url', public] if command == 'admin' else []
                    processes.append(subprocess.Popen([binary, command, '--dir', str(root / 'ceremony'), '--listen', origin.removeprefix('http://'), *flags], stdout=log, stderr=log))
                    for _ in range(100):
                        try:
                            with urllib.request.urlopen(origin + path, timeout=1):
                                break
                        except OSError:
                            time.sleep(.1)
                    else:
                        raise RuntimeError('Coordinator did not start')
                cli('operator', 'connect', '--url', public, '--control-url', control)
                config = root / 'ceremony-operator.json'
                assert config.stat().st_mode & 0o077 == 0
                assert set(json.loads(config.read_text())) == {'format', 'url', 'control_url', 'manifest'}
                old_config = config.read_bytes()
                cli('operator', 'connect', '--url', public, '--control-url', control, success=False)
                assert config.read_bytes() == old_config
                invitation_file = root / "bob's invitation.json"
                output = cli('operator', 'invite', 'bob', '--out', invitation_file.name)
                invitation = json.loads(invitation_file.read_text())
                assert invitation_file.stat().st_mode & 0o077 == 0
                assert invitation['token'] not in output
                handoff = [line for line in output.splitlines() if line.startswith('./spr-ceremony contribute ')]
                assert len(handoff) == 1
                arguments = shlex.split(handoff[0])
                assert arguments[1:] == ['contribute', '--invitation', invitation_file.name]
                output += cli(*arguments[1:])
                output += cli(*arguments[1:])
                status = json.loads(cli('operator', 'status', '--json'))
                assert len(status['invitations']) == 1 and status['invitations'][0]['accepted'] == 2
                output += cli('operator', 'audit')
                assert 'Transcript verified: core' in output
                output += cli('operator', 'audit')
                cli('operator', 'invite', 'bob', '--out', 'duplicate.json', success=False)
                assert not (root / 'duplicate.json').exists()
                output += cli('operator', 'reissue', 'bob', '--out', 'renewed.json')
                renewed = json.loads((root / 'renewed.json').read_text())
                assert renewed['id'] == invitation['id'] and renewed['token'] != invitation['token']
                cli('operator', 'reissue', 'bob', '--out', 'renewed.json', success=False)
                assert json.loads((root / 'renewed.json').read_text()) == renewed
                output += cli('contribute', '--invitation', 'renewed.json')
                output += cli('operator', 'revoke', renewed['id'][:12])
                output += cli('operator', 'revoke', renewed['id'])
                status = json.loads(cli('operator', 'status', '--json'))
                assert status['invitations'][0]['accepted'] == 2 and status['invitations'][0]['has_link'] is False
                request = urllib.request.Request(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + renewed['token']})
                try:
                    urllib.request.urlopen(request, timeout=5)
                    raise AssertionError('Revoked bearer still works')
                except urllib.error.HTTPError as error:
                    assert error.code == 401
                output += (root / 'servers.log').read_text()
                for token in [invitation['token'], renewed['token']]:
                    assert token not in output
                    for path in (root / 'audit').rglob('*'):
                        if path.is_file():
                            assert token.encode() not in path.read_bytes()
            finally:
                for process in processes:
                    process.terminate()
                for process in processes:
                    process.wait(timeout=15)
    print('Operator connect, private handoff, contribution/retry, remote audit, reissue and revoke passed.')


if __name__ == '__main__':
    main()
