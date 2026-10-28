#!/usr/bin/env python3
"""Deploy an attested GitHub release through SSM. Never uploads invitations."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request

REPO = 'helius-labs/spr-ceremony'
NATIVE = [f'spr-ceremony-{system}-{arch}' for system in ['darwin', 'linux'] for arch in ['amd64', 'arm64']]
BROWSER = {'index.html', 'style.css', 'app.js', 'worker.js', 'network.js', 'wasm_exec.js', 'contributor.wasm', 'hosted.html', 'hosted.js', 'protocol.js', 'cache.js', 'transfer.js', 'session.js', 'build.json', 'admin.html', 'admin.js'}
CHECK_HOSTED_BUILD = 'import json,sys; actual=json.load(open(sys.argv[1])); expected=json.load(open(sys.argv[2])); assert actual == expected and actual["source_modified"] is False, "Hosted build differs from release"'


def run(*args):
    return subprocess.check_output([str(a) for a in args], text=True).strip()


def digest(path):
    with open(path, 'rb') as handle:
        return hashlib.file_digest(handle, 'sha256').hexdigest()


def extract_browser(archive, target):
    target.mkdir()
    seen = set()
    with tarfile.open(archive, 'r:gz') as tar:
        for member in tar:
            if member.name in ['.', './'] and member.isdir():
                continue
            name = member.name.removeprefix('./')
            if name not in BROWSER or name in seen or not member.isfile() or member.size > 64 << 20:
                raise ValueError('Unexpected browser archive entry')
            seen.add(name)
            with tar.extractfile(member) as source, (target / name).open('wb') as output:
                shutil.copyfileobj(source, output)
    if not (BROWSER - {'admin.html', 'admin.js'}).issubset(seen):
        raise ValueError('Incomplete browser archive')


def checked_release(directory, revision):
    metadata = json.loads((directory / 'build.json').read_text())
    if metadata['source_revision'] != revision:
        raise ValueError('Release source revision mismatch')
    expected = set(NATIVE + ['join.sh', 'build.json', f'browser-{revision}.tar.gz'])
    seen = set()
    for line in (directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  ([a-zA-Z0-9_.-]+)', line)
        if not match or match[2] not in expected or match[2] in seen:
            raise ValueError('Invalid checksum manifest')
        seen.add(match[2])
        if digest(directory / match[2]) != match[1]:
            raise ValueError('Release checksum mismatch')
    if seen != expected:
        raise ValueError('Incomplete checksum manifest')


def check_initial_campaign(manifest, checkpoint, rehearsal, access_mode):
    circuits = manifest.get('circuits', [])
    marked_test = manifest.get('phase1', {}).get('insecure_test_setup')
    phase = checkpoint.get('lifecycle', {}).get('phase')
    if not circuits or any(c.get('spec', {}).get('track') != 'core' for c in circuits):
        raise ValueError('Initial deployment requires core circuits only')
    if rehearsal:
        if access_mode != 'ssm' or marked_test is not True or phase not in ['prepared', 'open']:
            raise ValueError('Rehearsals require SSM access and an explicitly marked test campaign')
    elif marked_test is not False or len(circuits) != 41 or phase != 'prepared':
        raise ValueError('Initial deployment requires 41 production core circuits in prepared phase')


def ssm(aws, instance, commands, tmp):
    request = tmp / 'ssm-request.json'
    request.write_text(json.dumps({'DocumentName': 'AWS-RunShellScript', 'InstanceIds': [instance], 'Parameters': {'commands': [commands], 'executionTimeout': ['7200']}, 'TimeoutSeconds': 600}))
    request.chmod(0o600)
    command_id = json.loads(run(*aws, 'ssm', 'send-command', '--cli-input-json', 'file://' + str(request)))['Command']['CommandId']
    print('SSM deployment started:', command_id, flush=True)
    deadline = time.monotonic() + 7500
    while time.monotonic() < deadline:
        time.sleep(5)
        result = subprocess.run([*aws, 'ssm', 'get-command-invocation', '--command-id', command_id, '--instance-id', instance], capture_output=True, text=True)
        if result.returncode:
            if 'InvocationDoesNotExist' in result.stderr: continue
            raise RuntimeError('Cannot read SSM status; inspect command ' + command_id)
        view = json.loads(result.stdout)
        if view['Status'] in ['Pending', 'InProgress', 'Delayed']: continue
        if view['Status'] != 'Success':
            raise RuntimeError('SSM deployment ' + view['Status'] + '; inspect command ' + command_id)
        print('SSM deployment completed.', flush=True)
        return
    raise RuntimeError('SSM deployment is still running; inspect command ' + command_id)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--terraform-dir', default='infra/aws')
    parser.add_argument('--release', required=True)
    parser.add_argument('--source-sha', required=True, help='independently reviewed source commit')
    parser.add_argument('--ceremony', type=Path, help='first deployment only: prepared production core store')
    parser.add_argument('--rehearsal', action='store_true', help='allow a marked test campaign, only on an internal SSM deployment')
    args = parser.parse_args()
    if not re.fullmatch(r'[a-f0-9]{40}', args.source_sha) or not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?', args.release):
        parser.error('Invalid version or source revision')
    outputs = json.loads(run('terraform', '-chdir=' + args.terraform_dir, 'output', '-json'))
    get = lambda key: outputs[key]['value']
    access_mode = outputs.get('access_mode', {}).get('value', 'public')
    if args.rehearsal and (access_mode != 'ssm' or not args.ceremony):
        parser.error('--rehearsal requires --ceremony and access_mode = ssm')
    config = json.loads(get('s3_config'))
    aws = ['aws', '--region', config['region'], '--output', 'json']
    if json.loads(run(*aws, 'sts', 'get-caller-identity'))['Account'] not in get('bucket'):
        raise RuntimeError('AWS account differs from Terraform deployment')
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='spr-deploy-') as temp:
        tmp = Path(temp)
        assets = tmp / 'assets'; assets.mkdir()
        run('gh', 'release', 'verify', args.release, '--repo', REPO)
        run('gh', 'release', 'download', args.release, '--repo', REPO, '--dir', assets)
        for name in NATIVE + ['join.sh', 'build.json', 'SHA256SUMS', f'browser-{args.source_sha}.tar.gz']:
            run('gh', 'attestation', 'verify', assets / name, '--repo', REPO, '--bundle', assets / 'provenance.json', '--source-digest', args.source_sha, '--signer-workflow', REPO + '/.github/workflows/binaries.yml', '--deny-self-hosted-runners')
        checked_release(assets, args.source_sha)
        bundle = tmp / 'bundle'; bundle.mkdir()
        native = bundle / 'native'; native.mkdir()
        for name in NATIVE + ['join.sh', 'build.json', 'SHA256SUMS', 'provenance.json']:
            shutil.copyfile(assets / name, native / name)
        extract_browser(assets / f'browser-{args.source_sha}.tar.gz', bundle / 'browser')
        recovery = None
        if args.ceremony:
            manifest = json.loads((args.ceremony / 'manifest.json').read_text())
            cp_ref = json.loads((args.ceremony / 'checkpoint.json').read_text())
            checkpoint = json.loads((args.ceremony / 'objects' / cp_ref['sha256']).read_text())
            check_initial_campaign(manifest, checkpoint, args.rehearsal, access_mode)
            config_path = tmp / 's3.json'; config_path.write_text(json.dumps(config))
            platform = 'darwin' if os.uname().sysname == 'Darwin' else 'linux'
            arch = 'arm64' if os.uname().machine in ['arm64', 'aarch64'] else 'amd64'
            binary = assets / f'spr-ceremony-{platform}-{arch}'; binary.chmod(0o700)
            subprocess.run([str(binary), 'sync', '--dir', str(args.ceremony), '--s3-config', str(config_path), '--out', str(bundle / 'recovery.json')], check=True)
            recovery = json.loads((bundle / 'recovery.json').read_text())
        archive = tmp / 'deployment.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            for path in sorted(bundle.rglob('*')): tar.add(path, arcname=str(path.relative_to(bundle)), recursive=False)
        archive_sha = digest(archive)
        key = f'deployment/{archive_sha}.tar.gz'
        run(*aws, 's3', 'cp', archive, f's3://{get("bucket")}/{key}', '--sse', 'AES256', '--only-show-errors')
        release_dir = '/opt/spr/releases/' + args.release + '-' + args.source_sha
        q = shlex.quote
        commands = f'''#!/usr/bin/env bash
set -euo pipefail
umask 077
test -f /etc/spr/s3.json
mountpoint -q /var/lib/spr
stage=$(mktemp -d /opt/spr/releases/.stage-XXXXXXXX)
trap 'rm -rf -- "$stage"' EXIT
aws s3 cp {q('s3://' + get('bucket') + '/' + key)} "$stage/deployment.tar.gz" --region {q(config['region'])} --only-show-errors
printf '%s  %s\\n' {q(archive_sha)} "$stage/deployment.tar.gz" | sha256sum --check
mkdir "$stage/release"
tar --no-same-owner -xzf "$stage/deployment.tar.gz" -C "$stage/release"
chmod -R u=rwX,go=rX "$stage/release"
chmod 755 "$stage/release/native/spr-ceremony-linux-amd64"
if [ ! -d {q(release_dir)} ]; then mv "$stage/release" {q(release_dir)}; fi
'''
        if recovery:
            commands += f'''
test ! -e /var/lib/spr/core
runuser -u spr -- env GOMAXPROCS=4 {q(release_dir + '/native/spr-ceremony-linux-amd64')} restore --out /var/lib/spr/core --s3-config /etc/spr/s3.json --recovery-file {q(release_dir + '/recovery.json')} --recovery-sha256 {q(digest(bundle / 'recovery.json'))}
runuser -u spr -- env GOMAXPROCS=4 {q(release_dir + '/native/spr-ceremony-linux-amd64')} audit --dir /var/lib/spr/core --manifest-sha256 {q(recovery['manifest']['sha256'])} --checkpoint-sha256 {q(recovery['checkpoint']['sha256'])}
'''
        commands += f'''
test -f /var/lib/spr/core/manifest.json
systemctl stop spr-admin spr-ceremony
ln -s {q(release_dir)} "$stage/current"
mv -Tf "$stage/current" /opt/spr/current
systemctl start spr-ceremony spr-admin
ready=0
for attempt in $(seq 1 180); do
  if curl -fsS --max-time 10 -H {q('Host: ' + urllib.parse.urlsplit(get('url')).netloc)} http://127.0.0.1:8080/healthz >/dev/null; then ready=1; break; fi
  sleep 5
done
test "$ready" = 1
curl -fsS --max-time 30 -H {q('Host: ' + urllib.parse.urlsplit(get('url')).netloc)} http://127.0.0.1:8080/build.json > "$stage/hosted-build.json"
curl -fsS --max-time 30 -H {q('Host: ' + urllib.parse.urlsplit(get('url')).netloc)} http://127.0.0.1:8080/join.sh > "$stage/hosted-join.sh"
python3 -c {q(CHECK_HOSTED_BUILD)} "$stage/hosted-build.json" {q(release_dir + '/browser/build.json')}
printf '%s  %s\\n' {q(digest(assets / 'join.sh'))} "$stage/hosted-join.sh" | sha256sum --check
'''
        ssm(aws, get('instance_id'), commands, tmp)
        if access_mode == 'public':
            with urllib.request.urlopen(get('url') + '/build.json', timeout=30) as response: hosted = json.load(response)
            if hosted['source_revision'] != args.source_sha or hosted['source_modified']:
                raise RuntimeError('Hosted build differs from reviewed release')
            with urllib.request.urlopen(get('url') + '/join.sh', timeout=30) as response:
                if hashlib.sha256(response.read(64 << 10)).hexdigest() != digest(assets / 'join.sh'):
                    raise RuntimeError('Hosted installer differs from release')
            print('Verified HTTPS deployment:', get('url'))
        else:
            print('Verified internal deployment:', get('url'))
            print('Participant tunnel: aws ssm start-session --target', get('instance_id'), '--document-name', get('participant_document'))
        print('Control panel: aws ssm start-session --target', get('instance_id'), '--document-name', get('panel_document'))


if __name__ == '__main__':
    main()
