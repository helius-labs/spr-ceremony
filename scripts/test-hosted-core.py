#!/usr/bin/env python3
"""Measure an existing marked core rehearsal through the real hosted queue."""
import argparse
import asyncio
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import traceback
import urllib.request
import sys
from playwright.async_api import async_playwright

async def main():
    p = argparse.ArgumentParser()
    p.add_argument('--binary', required=True)
    p.add_argument('--dir', required=True)
    p.add_argument('--web-dir', default='dist/browser')
    p.add_argument('--out', required=True)
    p.add_argument('--browser', choices=['chromium', 'webkit'], default='chromium')
    p.add_argument('--recover', action='store_true', help='recover a previously accepted marked run and reupload its identical public output')
    p.add_argument('--invitation', help='existing private invitation; required with --recover')
    p.add_argument('--reopen', action='store_true', help='pause after acceptance, reload and resume the same invitation')
    p.add_argument('--timeout', type=int, default=14400, help='maximum seconds for the complete browser session')
    args = p.parse_args()
    if args.recover and not args.invitation:
        p.error('--recover requires --invitation so the original invitation ID is preserved')
    os.umask(0o077)
    binary, directory, web, out = map(lambda x: Path(x).resolve(), [args.binary, args.dir, args.web_dir, args.out])
    raw = (directory / 'manifest.json').read_bytes()
    manifest = json.loads(raw)
    assert manifest['phase1']['insecure_test_setup'] is True
    assert all(c['spec']['track'] == 'core' for c in manifest['circuits'])
    assert manifest.get('coordinator_key'), 'Requires a signed rehearsal'
    out.mkdir(parents=True, exist_ok=False)
    if args.invitation:
        from urllib.parse import urlsplit
        invite = Path(args.invitation).resolve()
        invitation = json.loads(invite.read_text())
        origin = invitation['url']
        parsed = urlsplit(origin)
        assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.port
        assert invitation['manifest_sha256'] == hashlib.sha256(raw).hexdigest()
        port = parsed.port
    else:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        invite = out / 'private-invitation.json'
        subprocess.run([binary, 'invite', '--dir', directory, '--url', origin,
                        '--name', 'hosted-' + args.browser + '-' + str(time.time_ns()), '--out', invite], check=True, capture_output=True)
    log = (out / 'server.log').open('w')
    server = subprocess.Popen([binary, 'serve', '--dir', directory, '--listen', f'127.0.0.1:{port}',
                               '--web-dir', web, '--public-url', origin, '--lease-ttl', '2m'], stdout=log, stderr=log)
    try:
        for _ in range(100):
            try:
                with urllib.request.urlopen(origin + '/healthz', timeout=1): break
            except OSError: time.sleep(.1)
        else: raise RuntimeError('server startup failed')
        async with async_playwright() as pw:
            browser = await getattr(pw, args.browser).launch()
            context = await browser.new_context(accept_downloads=True)
            reports = []
            await context.expose_binding('recordSPRReport', lambda source, report: reports.append(report))
            await context.add_init_script('''
                const OriginalWorker = window.Worker;
                window.Worker = class extends OriginalWorker {
                  constructor(...args) {
                    super(...args);
                    this.addEventListener('message', event => {
                      if (event.data.type === 'complete') window.recordSPRReport(event.data.report);
                    });
                  }
                };
            ''')
            page = await context.new_page()
            errors = []
            page.on('pageerror', lambda e: errors.append(type(e).__name__))
            heartbeat_count = 0
            def request_seen(request):
                nonlocal heartbeat_count
                if '/v1/heartbeat/' in request.url:
                    heartbeat_count += 1
            context.on('request', request_seen)
            await page.goto(origin)
            await page.locator('#invitation').set_input_files(invite)
            await page.wait_for_function('() => !document.querySelector("#refresh").disabled && (!document.querySelector("#start").disabled || document.documentElement.dataset.stage === "complete")')
            already_done = await page.get_attribute('html', 'data-stage') == 'complete'
            assert not already_done or args.recover, 'This pseudonym already finished; use --recover to check recovery'
            start = time.monotonic()
            if not already_done: await page.locator('#start').click()
            last = None
            reopened = False
            while time.monotonic() - start < args.timeout:
                stage = await page.get_attribute('html', 'data-stage')
                count = await page.locator('#count').inner_text()
                current = (stage, count)
                if current != last:
                    print(json.dumps({'stage': stage, 'accepted': count, 'seconds': round(time.monotonic() - start, 2)}), flush=True)
                    last = current
                if args.reopen and not reopened and int(count.split(' / ')[0]) >= 1 and stage not in ['complete', 'error', 'paused']:
                    await page.locator('#pause').click()
                    await page.wait_for_function('() => document.documentElement.dataset.stage === "paused" && !document.querySelector("#native").disabled')
                    saved_count = int((await page.locator('#count').inner_text()).split(' / ')[0])
                    await page.reload()
                    await page.locator('#invitation').set_input_files(invite)
                    await page.wait_for_function('() => !document.querySelector("#start").disabled')
                    assert int((await page.locator('#count').inner_text()).split(' / ')[0]) >= saved_count
                    await page.locator('#start').click()
                    reopened = True
                if stage in ['complete', 'error', 'paused']: break
                await asyncio.sleep(1)
            assert stage == 'complete', await page.locator('#status').inner_text()
            browser_seconds = time.monotonic() - start
            assert not args.reopen or reopened, 'Reopen scenario did not run'
            assert await page.locator('#count').inner_text() == f'{len(manifest["circuits"])} / {len(manifest["circuits"])} accepted'
            assert not errors, errors
            assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
            assert not await context.cookies()
            await page.wait_for_function('() => !document.querySelector("#native").disabled')
            await page.locator('#native').click()
            assert await page.locator('#native-panel').is_visible()
            await page.locator('#native').click()
            async with page.expect_download() as downloading:
                await page.locator('#receipts').click()
            receipt = out / 'receipts.json'
            await (await downloading.value).save_as(receipt)
            subprocess.run([binary, 'verify-attestation', '--dir', directory, '--attestation', receipt,
                            '--manifest-sha256', hashlib.sha256(raw).hexdigest()], check=True, capture_output=True)
            if args.recover:
                receipts = json.loads(receipt.read_text())['receipts']
                invitation = json.loads(invite.read_text())
                # Fetch/upload inside the browser; never serialize the large
                # binary through Playwright's JSON transport. Blob uploads also
                # avoid DevTools expanding raw binary postData into giant text.
                for entry in receipts:
                    result = await page.evaluate('''async data => {
                      const bytes = await (await fetch('/v1/objects/' + data.receipt.output.sha256)).arrayBuffer();
                      const response = await fetch('/v1/contributions/' + data.receipt.circuit, {
                        method: 'POST', headers: {Authorization: 'Bearer ' + data.token, 'Content-Type': 'application/octet-stream'},
                        body: new Blob([bytes], {type: 'application/octet-stream'})
                      });
                      if (!response.ok) throw new Error('Reupload failed: ' + response.status);
                      return await response.json();
                    }''', {'token': invitation['token'], 'receipt': entry})
                    assert result == entry
            await page.screenshot(path=out / 'page.png', full_page=True)
            report = {'browser': args.browser, 'version': browser.version, 'wall_seconds': time.monotonic() - start,
                      'browser_session_seconds': browser_seconds, 'reports': reports, 'receipts': json.loads(receipt.read_text()),
                      'native_acceptance': True, 'checkpoint_inclusion': True, 'recovery_and_reupload': args.recover, 'safari_tested': False,
                      'reopened_after_acceptance': reopened, 'heartbeat_requests': heartbeat_count, 'terminal_handoff': True,
                      'build': await (await context.request.get(origin + '/build.json')).json()}
            (out / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps({'complete': True, 'seconds': report['wall_seconds']}), flush=True)
            await browser.close()
    finally:
        server.terminate(); server.wait(timeout=65); log.close()

if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as error:
        # Browser failures can contain private invitations or signed URLs.
        print('Core browser rehearsal failed (' + type(error).__name__ + ').', file=sys.stderr)
        for frame in traceback.extract_tb(error.__traceback__):
            if Path(frame.filename).name == Path(__file__).name:
                print('Test location: ' + frame.name + ':' + str(frame.lineno), file=sys.stderr)
        raise SystemExit(1)
