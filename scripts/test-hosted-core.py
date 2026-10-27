#!/usr/bin/env python3
"""Measure an existing marked core rehearsal through the real hosted queue."""
import argparse
import asyncio
import hashlib
import json
from pathlib import Path
import socket
import subprocess
import time
import urllib.request
from playwright.async_api import async_playwright

async def main():
    p = argparse.ArgumentParser()
    p.add_argument('--binary', required=True)
    p.add_argument('--dir', required=True)
    p.add_argument('--web-dir', default='dist/browser')
    p.add_argument('--out', required=True)
    p.add_argument('--browser', choices=['chromium', 'webkit'], default='chromium')
    args = p.parse_args()
    binary, directory, web, out = map(lambda x: Path(x).resolve(), [args.binary, args.dir, args.web_dir, args.out])
    raw = (directory / 'manifest.json').read_bytes()
    manifest = json.loads(raw)
    assert manifest['phase1']['insecure_test_setup'] is True
    assert all(c['spec']['track'] == 'core' for c in manifest['circuits'])
    out.mkdir(parents=True, exist_ok=False)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    invite = out / 'private-invitation.json'
    subprocess.run([binary, 'invite', '--dir', directory, '--url', origin,
                    '--name', 'hosted-' + args.browser, '--out', invite], check=True, capture_output=True)
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
            await context.add_init_script('''
                window.sprRunReports = [];
                const OriginalWorker = window.Worker;
                window.Worker = class extends OriginalWorker {
                  constructor(...args) {
                    super(...args);
                    this.addEventListener('message', event => {
                      if (event.data.type === 'complete') window.sprRunReports.push(event.data.report);
                    });
                  }
                };
            ''')
            page = await context.new_page()
            errors = []
            page.on('pageerror', lambda e: errors.append(str(e)))
            await page.goto(origin)
            await page.locator('#invitation').set_input_files(invite)
            await page.wait_for_function('() => !document.querySelector("#start").disabled')
            start = time.monotonic()
            await page.locator('#start').click()
            last = None
            while time.monotonic() - start < 2400:
                stage = await page.get_attribute('html', 'data-stage')
                if stage != last:
                    print(json.dumps({'stage': stage, 'seconds': round(time.monotonic() - start, 2)}), flush=True)
                    last = stage
                if stage in ['complete', 'error', 'paused']: break
                await asyncio.sleep(1)
            assert stage == 'complete', await page.locator('#status').inner_text()
            assert not errors, errors
            async with page.expect_download() as downloading:
                await page.locator('#receipts').click()
            receipt = out / 'receipts.json'
            await (await downloading.value).save_as(receipt)
            subprocess.run([binary, 'verify-attestation', '--dir', directory, '--attestation', receipt,
                            '--manifest-sha256', hashlib.sha256(raw).hexdigest()], check=True, capture_output=True)
            await page.screenshot(path=out / 'page.png', full_page=True)
            report = {'browser': args.browser, 'version': browser.version, 'wall_seconds': time.monotonic() - start,
                      'reports': await page.evaluate('() => window.sprRunReports'), 'receipts': json.loads(receipt.read_text()),
                      'native_acceptance': True, 'checkpoint_inclusion': True, 'safari_tested': False,
                      'build': json.loads((web / 'build.json').read_text())}
            (out / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps({'complete': True, 'seconds': report['wall_seconds']}), flush=True)
            await browser.close()
    finally:
        server.terminate(); server.wait(timeout=65); log.close()

if __name__ == '__main__': asyncio.run(main())
