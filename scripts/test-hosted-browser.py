#!/usr/bin/env python3
"""CLI-only hosted page, private invitation handoff and real native contributions."""
import argparse
import asyncio
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import traceback
import tempfile
import time
import urllib.request
import uuid
from urllib.parse import parse_qs, urlsplit

from playwright.async_api import async_playwright
from cli_test_proxy import Proxy


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--web-dir', default='dist/browser')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve()); web = Path(args.web_dir).resolve()
    out = Path(args.out).resolve(); out.mkdir(parents=True, exist_ok=True)
    checks = []
    with tempfile.TemporaryDirectory(prefix='spr-cli-web-') as tmp:
        root = Path(tmp)
        def cli(*arguments):
            result = subprocess.run([binary, *map(str, arguments)], cwd=root, capture_output=True, text=True)
            if result.returncode: raise RuntimeError('Fixture command failed: ' + str(arguments[0]))
            return result.stdout
        cli('demo', '--out', 'ceremony')
        endpoint = os.environ.get('SPR_S3_ENDPOINT')
        if endpoint:
            assert endpoint.startswith(('http://127.0.0.1:', 'http://localhost:'))
            os.environ.update(AWS_ACCESS_KEY_ID='testing', AWS_SECRET_ACCESS_KEY='testing', AWS_EC2_METADATA_DISABLED='true')
            bucket = 'spr-cli-web-' + uuid.uuid4().hex
            with urllib.request.urlopen(urllib.request.Request(endpoint + '/' + bucket, data=b'', method='PUT')): pass
            (root / 's3.json').write_text(json.dumps({'bucket': bucket, 'region': 'us-east-1', 'prefix': 'hosted', 'endpoint': endpoint}))
            cli('sync', '--dir', 'ceremony', '--s3-config', 's3.json')
        certificate, key = root / 'cert.pem', root / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key), '-out', str(certificate), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1', '-addext', 'basicConstraints=critical,CA:TRUE'], check=True, capture_output=True)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); upstream = sock.getsockname()[1]
        proxy = Proxy(upstream, certificate, key); origin = proxy.origin
        native = root / 'native'; native.mkdir()
        hashes = {}
        for platform in ['darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64']:
            name = 'spr-ceremony-' + platform
            shutil.copyfile(binary, native / name)
            hashes[name] = hashlib.sha256((native / name).read_bytes()).hexdigest()
        installer = b'#!/usr/bin/env bash\n# UI fixture; native execution is checked separately.\nexit 0\n'
        (native / 'join.sh').write_bytes(installer)
        build = json.loads((web / 'build.json').read_text())
        (native / 'build.json').write_text(json.dumps({'source_revision': build['source_revision'], 'go': 'test', 'sha256': hashes, 'version': 'v0.1.0-rc.1', 'installer': {'sha256': hashlib.sha256(installer).hexdigest(), 'size': len(installer)}}))
        with (out / 'server.log').open('w') as log:
            server = subprocess.Popen([binary, 'serve', '--dir', str(root / 'ceremony'), '--listen', f'127.0.0.1:{upstream}', '--public-url', origin, '--web-dir', str(web), '--native-dir', str(native)], stdout=log, stderr=log)
            try:
                for _ in range(100):
                    try:
                        with urllib.request.urlopen(origin + '/healthz', context=ssl.create_default_context(cafile=certificate), timeout=1): break
                    except OSError: time.sleep(.1)
                else: raise RuntimeError('Coordinator did not start')
                async with async_playwright() as pw:
                    for engine in ['chromium', 'webkit']:
                        invitation_file = root / (engine + '.json'); link_file = root / (engine + '.link')
                        cli('invite', '--dir', 'ceremony', '--url', origin, '--name', engine, '--out', invitation_file, '--link-out', link_file)
                        invitation = json.loads(invitation_file.read_text()); link = link_file.read_text().strip()
                        browser = await getattr(pw, engine).launch()
                        context = await browser.new_context(ignore_https_errors=True, viewport={'width': 390, 'height': 844})
                        await context.add_init_script('''
                          Object.defineProperty(globalThis, 'Worker', {value: undefined});
                          Object.defineProperty(globalThis, 'WebAssembly', {value: undefined});
                          Object.defineProperty(globalThis, 'indexedDB', {get() { throw new Error('Browser storage must not be used'); }});
                          Object.defineProperty(navigator, 'clipboard', {value: {writeText: async text => { window.testClipboard = text; }}});
                        ''')
                        page = await context.new_page(); requests, errors = [], []
                        page.on('request', lambda r: requests.append((r.method, r.url, r.headers)))
                        page.on('pageerror', lambda e: errors.append(str(e)))
                        await page.goto(origin)
                        await page.locator('#copy-command').wait_for(state='visible')
                        assert await page.locator('#invitation-actions').is_hidden()
                        assert await page.locator('#start, #pause, #native, input[type=file]').count() == 0
                        await page.locator('#copy-command').click()
                        command = await page.evaluate('window.testClipboard')
                        assert "curl --proto '=https' --tlsv1.2 -fsS" in command and origin + '/join.sh' in command
                        assert 'SPR_CEREMONY_DOWNLOAD_BASE=' in command and command.endswith(' bash')
                        await page.goto(link)
                        await page.locator('#copy-command').wait_for(state='visible')
                        assert '#' not in page.url
                        await page.locator('#copy-invitation').click()
                        copied = await page.evaluate('window.testClipboard')
                        assert parse_qs(urlsplit(copied).fragment) == parse_qs(urlsplit(link).fragment)
                        assert invitation['token'] not in command
                        assert invitation['token'] not in await page.locator('body').inner_text()
                        await page.locator('#manual-downloads summary').click()
                        assert await page.locator('#downloads a').count() == 4
                        await page.locator('#platform').select_option('linux-amd64')
                        assert './spr-ceremony-linux-amd64 join' in await page.locator('#native-command').inner_text()
                        await page.locator('#manual-downloads summary').click()
                        assert await page.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
                        await page.screenshot(path=out / (engine + '-cli.png'), full_page=True)
                        env = {**os.environ, 'SSL_CERT_FILE': str(certificate)}
                        for attempt in range(2):
                            process = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(root / (engine + '-work')), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=env)
                            stdout, stderr = await asyncio.wait_for(process.communicate(copied.encode() + b'\n'), timeout=60)
                            assert process.returncode == 0, 'Native join failed'
                            assert invitation['token'].encode() not in stdout + stderr
                            if attempt == 0: assert b'[1/2]' in stderr and b'[2/2]' in stderr
                        await page.goto(origin)
                        record = page.locator(f'#records a[href="/v1/records/{invitation["id"]}"]')
                        await record.wait_for(state='attached')
                        assert '2/2 accepted' in await record.text_content()
                        response = await context.request.get(origin + '/v1/records/' + invitation['id'])
                        published = await response.json()
                        assert len(published['receipts']) == 2 and invitation['token'] not in json.dumps(published)
                        for artifact in (root / (engine + '-work')).rglob('*'):
                            if artifact.is_file(): assert invitation['token'].encode() not in artifact.read_bytes()
                        await page.goto(origin + '/#token=invalid&manifest=' + invitation['manifest_sha256'])
                        await page.locator('#copy-command').wait_for(state='visible')
                        assert await page.locator('#invitation-actions').is_hidden()
                        assert 'Invalid invitation' in await page.locator('#status').inner_text()
                        assert not errors, errors
                        assert all(method == 'GET' and 'authorization' not in headers and 'cookie' not in headers for method, _, headers in requests)
                        assert all(invitation['token'] not in url and '/v1/invitation' not in url and '.wasm' not in url for _, url, _ in requests)
                        assert not await context.cookies()
                        assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
                        checks.append(engine + ': HTTPS CLI-only page, private clipboard handoff, no browser engine/storage/auth requests, native contribution and idempotent rerun, signed records, mobile layout')
                        await context.close(); await browser.close()
            finally:
                server.terminate(); server.wait(timeout=15); proxy.close()
        if endpoint: checks.append('Native contributions published through S3')
    (out / 'result.json').write_text(json.dumps({'passed': checks}, indent=2) + '\n')
    print('CLI-only hosted checks passed in Chromium and WebKit.')


if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as error:
        print('Hosted CLI check failed (' + type(error).__name__ + '). Private diagnostics omitted.', file=sys.stderr)
        for frame in traceback.extract_tb(error.__traceback__):
            if Path(frame.filename).name == Path(__file__).name:
                print('Test location: ' + frame.name + ':' + str(frame.lineno), file=sys.stderr)
        raise SystemExit(1)
