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
BROWSER = {'index.html', 'style.css', 'app.js', 'worker.js', 'network.js', 'wasm_exec.js', 'contributor.wasm', 'hosted.html', 'hosted.js', 'protocol.js', 'cache.js', 'transfer.js', 'session.js', 'signatures.js', 'signature-worker.js', 'signatures.wasm', 'build.json', 'admin.html', 'admin.js'}
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


def check_initial_campaign(manifest, checkpoint, rehearsal, access_mode, background_audit=False):
    circuits = manifest.get('circuits', [])
    marked_test = manifest.get('phase1', {}).get('insecure_test_setup')
    phase = checkpoint.get('lifecycle', {}).get('phase')
    key = manifest.get('coordinator_key', {})
    if not re.fullmatch('[a-f0-9]{64}', key.get('sha256', '')) or not 0 < key.get('size', 0) <= 16384:
        raise ValueError('Initial deployment requires a manifest-bound signing key')
    if not circuits or any(c.get('spec', {}).get('track') != 'core' for c in circuits):
        raise ValueError('Initial deployment requires core circuits only')
    if rehearsal:
        if access_mode not in ['ssm', 'cloudfront'] or marked_test is not True or phase not in ['prepared', 'open']:
            raise ValueError('Rehearsals require SSM administration and an explicitly marked test campaign')
    elif marked_test is not False or len(circuits) != 41 or phase != 'prepared':
        raise ValueError('Initial deployment requires 41 production core circuits in prepared phase')
    if background_audit and (not rehearsal or marked_test is not True or phase != 'prepared'):
        raise ValueError('Background initialization audit is only allowed for a prepared, explicitly marked rehearsal')


def initial_audit_command(binary, incoming, recovery, background):
    q = shlex.quote
    manifest, checkpoint = recovery['manifest']['sha256'], recovery['checkpoint']['sha256']
    arguments = f'--manifest-sha256 {q(manifest)} --checkpoint-sha256 {q(checkpoint)}'
    if not background:
        return f'runuser -u spr -- env GOMAXPROCS=4 {q(binary)} audit --dir {q(incoming)} {arguments}\n'
    snapshot = '/var/lib/spr/initial-audit-' + manifest
    unit = 'spr-initial-audit-' + manifest[:16]
    # Restore has already authenticated every downloaded object's hash. Freeze
    # the prepared snapshot before the live store can accept any contributions.
    # CAS objects and metadata use atomic replacement, so hard links stay fixed.
    return f'''test ! -e {q(snapshot)}
cp -al {q(incoming)} {q(snapshot)}
systemd-run --unit={q(unit)} --property=Type=oneshot --property=RemainAfterExit=yes --property=User=spr --property=Group=spr --property=MemoryMax=8G --property=NoNewPrivileges=yes --setenv=GOMAXPROCS=2 {q(binary)} audit --dir {q(snapshot)} {arguments}
printf '%s\\n' {q('REHEARSAL: initial audit continues separately in ' + unit + '. Contributions are provisional until it passes.')}
'''


def ssm(aws, instance, commands, tmp):
    request = tmp / 'ssm-request.json'
    request.write_text(json.dumps({'DocumentName': 'AWS-RunShellScript', 'InstanceIds': [instance], 'Parameters': {'commands': [commands], 'executionTimeout': ['21600']}, 'TimeoutSeconds': 600}))
    request.chmod(0o600)
    command_id = json.loads(run(*aws, 'ssm', 'send-command', '--cli-input-json', 'file://' + str(request)))['Command']['CommandId']
    print('SSM deployment started:', command_id, flush=True)
    deadline = time.monotonic() + 21900
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
    parser.add_argument('--rehearsal', action='store_true', help='allow an explicitly marked test campaign')
    parser.add_argument('--rehearsal-background-audit', action='store_true', help='prepared rehearsal only: activate after hash-checked restore while initialization audit runs on a frozen snapshot')
    parser.add_argument('--signing-key', type=Path, help='initial deployment: private key file; defaults to ceremony/private/coordinator.private.asc')
    parser.add_argument('--replace-test-manifest', help='internal rehearsal only: archive and replace this exact existing test manifest')
    args = parser.parse_args()
    if not re.fullmatch(r'[a-f0-9]{40}', args.source_sha) or not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?', args.release):
        parser.error('Invalid version or source revision')
    outputs = json.loads(run('terraform', '-chdir=' + args.terraform_dir, 'output', '-json'))
    get = lambda key: outputs[key]['value']
    access_mode = outputs.get('access_mode', {}).get('value', 'public')
    operator_origin = outputs.get('operator_origin', {}).get('value', '')
    operator_role = outputs.get('operator_role_arn', {}).get('value', '')
    if operator_origin:
        # Refuse to configure gateway flags on a release predating IAM support.
        run('git', 'cat-file', '-e', args.source_sha + ':operator_gateway.go')
    if args.rehearsal and (access_mode not in ['ssm', 'cloudfront'] or not args.ceremony):
        parser.error('--rehearsal requires --ceremony and access_mode = ssm or cloudfront')
    if args.rehearsal_background_audit and (not args.rehearsal or not args.ceremony):
        parser.error('--rehearsal-background-audit requires --rehearsal and --ceremony')
    if args.replace_test_manifest and (not args.rehearsal or not re.fullmatch('[a-f0-9]{64}', args.replace_test_manifest)):
        parser.error('--replace-test-manifest requires --rehearsal and a current manifest digest')
    if args.signing_key and not args.ceremony:
        parser.error('--signing-key is only used with --ceremony')
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
        # Runtime key-loading code must match the attested release's source.
        loader = subprocess.check_output(['git', 'show', args.source_sha + ':infra/aws/load-signing-key.py'])
        (bundle / 'load-signing-key.py').write_bytes(loader)
        recovery = None
        if args.ceremony:
            manifest = json.loads((args.ceremony / 'manifest.json').read_text())
            cp_ref = json.loads((args.ceremony / 'checkpoint.json').read_text())
            checkpoint = json.loads((args.ceremony / 'objects' / cp_ref['sha256']).read_text())
            check_initial_campaign(manifest, checkpoint, args.rehearsal, access_mode, args.rehearsal_background_audit)
            config_path = tmp / 's3.json'; config_path.write_text(json.dumps(config))
            platform = 'darwin' if os.uname().sysname == 'Darwin' else 'linux'
            arch = 'arm64' if os.uname().machine in ['arm64', 'aarch64'] else 'amd64'
            binary = assets / f'spr-ceremony-{platform}-{arch}'; binary.chmod(0o700)
            private_key = args.signing_key or args.ceremony / 'private/coordinator.private.asc'
            run(binary, 'check-signing-key', '--dir', args.ceremony, '--signing-key', private_key)
            subprocess.run([str(binary), 'sync', '--dir', str(args.ceremony), '--s3-config', str(config_path), '--out', str(bundle / 'recovery.json')], check=True)
            recovery = json.loads((bundle / 'recovery.json').read_text())
            # AWS redacts SecretString in CloudTrail. The command contains only a
            # protected filename; no key bytes enter argv, SSM, S3, or Terraform.
            run(*aws, 'secretsmanager', 'put-secret-value', '--secret-id', get('signing_key_secret_arn'),
                '--client-request-token', recovery['manifest']['sha256'],
                '--version-stages', 'campaign-' + recovery['manifest']['sha256'],
                '--secret-string', 'file://' + str(private_key.resolve()))
        archive = tmp / 'deployment.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            for path in sorted(bundle.rglob('*')): tar.add(path, arcname=str(path.relative_to(bundle)), recursive=False)
        archive_sha = digest(archive)
        key = f'deployment/{archive_sha}.tar.gz'
        run(*aws, 's3', 'cp', archive, f's3://{get("bucket")}/{key}', '--sse', 'AES256', '--only-show-errors')
        release_dir = '/opt/spr/releases/' + args.release + '-' + args.source_sha
        if recovery:
            # A new campaign may reuse the installed binary release. Keep its
            # recovery pin separate from any earlier deployment of that release.
            release_dir += '-' + recovery['manifest']['sha256']
        q = shlex.quote
        proxy_start = 'caddy validate --config /etc/caddy/Caddyfile\nsystemctl enable --now caddy\nsystemctl reload caddy' if access_mode == 'cloudfront' else ''
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
            incoming = '/var/lib/spr/incoming-' + recovery['manifest']['sha256']
            commands += f'''
test ! -e {q(incoming)}
runuser -u spr -- env GOMAXPROCS=4 {q(release_dir + '/native/spr-ceremony-linux-amd64')} restore --out {q(incoming)} --s3-config /etc/spr/s3.json --recovery-file {q(release_dir + '/recovery.json')} --recovery-sha256 {q(digest(bundle / 'recovery.json'))}
'''
            commands += initial_audit_command(release_dir + '/native/spr-ceremony-linux-amd64', incoming, recovery, args.rehearsal_background_audit)
            if args.replace_test_manifest:
                guard = 'import hashlib,json,sys; b=open("/var/lib/spr/core/manifest.json","rb").read(); m=json.loads(b); assert hashlib.sha256(b).hexdigest()==sys.argv[1] and m["phase1"]["insecure_test_setup"] is True, "Existing test campaign mismatch"'
                commands += f'''
python3 -c {q(guard)} {q(args.replace_test_manifest)}
test ! -e {q('/var/lib/spr/replaced-' + args.replace_test_manifest)}
systemctl stop spr-admin spr-ceremony
mv /var/lib/spr/core {q('/var/lib/spr/replaced-' + args.replace_test_manifest)}
'''
            commands += f'''
test ! -e /var/lib/spr/core
mv {q(incoming)} /var/lib/spr/core
'''
        commands += f'''
test -f /var/lib/spr/core/manifest.json
systemctl stop spr-admin spr-ceremony
install -o root -g root -m 0755 {q(release_dir + '/load-signing-key.py')} /usr/local/bin/spr-load-signing-key
printf '%s\\n' {q(get('signing_key_secret_arn'))} > /etc/spr/signing-secret
chown root:spr /etc/spr/signing-secret
chmod 640 /etc/spr/signing-secret
python3 - {q(get('url'))} {q(access_mode)} {q(operator_origin)} {q(operator_role)} <<'UNITS'
from pathlib import Path
import re, shlex, sys, urllib.parse
origin, access_mode = sys.argv[1:3]
operator_origin, operator_role = sys.argv[3:5] if len(sys.argv) >= 5 else ('', '')
hostname = urllib.parse.urlsplit(origin).netloc
for name in ['spr-ceremony', 'spr-admin']:
    path = Path('/etc/systemd/system/' + name + '.service')
    text = path.read_text()
    if 'ExecStartPre=/usr/local/bin/spr-load-signing-key' not in text:
        text = text.replace('ExecStart=', 'ExecStartPre=/usr/local/bin/spr-load-signing-key\\nExecStart=', 1)
    if '--signing-key ' not in text:
        text = text.replace(' --dir /var/lib/spr/core', ' --signing-key /var/lib/spr/signing/coordinator.private.asc --dir /var/lib/spr/core', 1)
    lines = text.splitlines()
    for n, line in enumerate(lines):
        if not line.startswith('ExecStart='): continue
        arguments = shlex.split(line[len('ExecStart='):])
        arguments[arguments.index('--public-url') + 1] = origin
        if name == 'spr-ceremony':
            arguments = [arg for arg in arguments if arg != '--trust-loopback-proxy']
            if access_mode != 'ssm': arguments.append('--trust-loopback-proxy')
        if name == 'spr-admin':
            for flag in ['--operator-origin', '--operator-role']:
                while flag in arguments:
                    index = arguments.index(flag); del arguments[index:index + 2]
            if operator_origin: arguments.extend(['--operator-origin', operator_origin, '--operator-role', operator_role])
        lines[n] = 'ExecStart=' + shlex.join(arguments)
    text = '\\n'.join(lines) + '\\n'
    if name == 'spr-admin' and operator_origin:
        text = re.sub(r'^MemoryMax=.*$', 'MemoryMax=12G', text, flags=re.MULTILINE)
        if 'Environment=GOMAXPROCS=4' not in text: text = text.replace('[Service]', '[Service]\\nEnvironment=GOMAXPROCS=4')
    path.write_text(text)
health = Path('/usr/local/bin/spr-health')
health.write_text(re.sub(r"(-H 'Host: )[^']*(')", lambda match: match[1] + hostname + match[2], health.read_text()))
if access_mode == 'cloudfront':
    Path('/etc/caddy/Caddyfile').write_text("""{{
  auto_https off
}}
http://:8082 {{
  request_body {{
    max_size 512MB
  }}
  reverse_proxy 127.0.0.1:8080 {{
    header_up Host """ + hostname + """
    header_up X-Forwarded-Proto https
    transport http {{
      response_header_timeout 30m
      read_timeout 30m
      write_timeout 30m
    }}
  }}
}}
""")
    if operator_origin:
        with Path('/etc/caddy/Caddyfile').open('a') as proxy:
            proxy.write("""
http://:8083 {{
  request_body {{
    max_size 4MB
  }}
  reverse_proxy 127.0.0.1:8081 {{
    header_up Host """ + urllib.parse.urlsplit(operator_origin).netloc + """
    header_up X-Forwarded-Proto https
  }}
}}
""")
UNITS
systemctl daemon-reload
ln -s {q(release_dir)} "$stage/current"
mv -Tf "$stage/current" /opt/spr/current
systemctl start spr-ceremony spr-admin
{proxy_start}
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
        if access_mode in ['public', 'cloudfront']:
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
        if operator_origin:
            print('Control panel:', operator_origin)
        else:
            print('Control panel: aws ssm start-session --target', get('instance_id'), '--document-name', get('panel_document'))


if __name__ == '__main__':
    main()
