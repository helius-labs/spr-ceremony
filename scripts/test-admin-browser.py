#!/usr/bin/env python3
"""Private control panel, native join and public key view with real Go servers."""
import argparse
import asyncio
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request
from playwright.async_api import async_playwright


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0)); return sock.getsockname()[1]


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--web-dir', default='dist/browser')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve()); web = str(Path(args.web_dir).resolve())
    out = Path(args.out); out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='spr-admin-') as tmp:
        root = Path(tmp)
        subprocess.run([binary, 'demo', '--out', str(root / 'ceremony')], check=True, capture_output=True)
        public, private = f'http://127.0.0.1:{port()}', f'http://127.0.0.1:{port()}'
        processes = []
        with (out / 'server.log').open('w') as log:
            try:
                for cmd, origin, flags in [('serve', public, ['--web-dir', web]), ('admin', private, [])]:
                    processes.append(subprocess.Popen([binary, cmd, '--dir', str(root / 'ceremony'), '--listen', origin.removeprefix('http://'), '--public-url', public, *flags], stdout=log, stderr=log))
                    for _ in range(100):
                        try:
                            with urllib.request.urlopen(origin, timeout=1): break
                        except OSError: time.sleep(.1)
                    else: raise RuntimeError(cmd + ' did not start')
                async with async_playwright() as pw:
                    for engine in ['chromium', 'webkit']:
                        browser = await getattr(pw, engine).launch()
                        context = await browser.new_context(accept_downloads=True, viewport={'width': 390, 'height': 844})
                        page = await context.new_page()
                        await page.goto(private)
                        await page.locator('#name').fill(engine)
                        await page.locator('#invite').click()
                        await page.wait_for_function('() => !document.querySelector("#secret").hidden')
                        invitation = await page.locator('#link').input_value()
                        assert '#manifest=' in invitation and '&token=' in invitation
                        assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
                        assert not await context.cookies()
                        async with page.expect_download() as downloaded: await page.locator('#download').click()
                        download = await downloaded.value
                        private_file = await download.path()
                        token = json.loads(Path(private_file).read_text())['token']
                        response = await page.evaluate('''async () => JSON.stringify(await (await fetch('/admin/status', {headers: {'X-SPR-Admin': '1'}})).json())''')
                        assert token not in response
                        await page.locator('#clear').click()
                        assert await page.locator('#link').input_value() == ''
                        process = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(root / 'work'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                        stdout, stderr = await asyncio.wait_for(process.communicate(invitation.encode() + b'\n'), timeout=60)
                        assert process.returncode == 0, 'Native join failed'
                        assert token.encode() not in stdout + stderr
                        await page.locator('#refresh').click()
                        await page.wait_for_function('() => document.querySelector("#invitations").textContent.includes("2/2 accepted")')
                        await page.screenshot(path=out / f'{engine}-control.png', full_page=True)
                        assert await page.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
                        visitor = await context.new_page(); await visitor.goto(public)
                        await visitor.locator('#artifacts summary').click()
                        await visitor.wait_for_function('() => document.querySelectorAll("#keys .key-row").length === 2')
                        assert 'after' in await visitor.locator('#keys-status').inner_text()
                        assert await visitor.locator('a', has_text='Proving key').count() == 0
                        await visitor.screenshot(path=out / f'{engine}-parameters.png', full_page=True)
                        public_admin = await context.request.get(public + '/admin/status')
                        assert public_admin.status == 404
                        row = page.locator('.invitation-row').filter(has_text=engine)
                        page.once('dialog', lambda dialog: dialog.accept())
                        await row.locator('button', has_text='Revoke').click()
                        await page.wait_for_function('() => document.querySelector("#status").textContent === "Access revoked."')
                        rejected = await context.request.get(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + token})
                        assert rejected.status == 401
                        assert token not in (out / 'server.log').read_text()
                        await context.close(); await browser.close()
            finally:
                for process in processes: process.terminate()
                for process in processes: process.wait(timeout=15)
    (out / 'result.json').write_text(json.dumps({'passed': ['Chromium and WebKit: create invitation, hidden native join, resume progress, revoke, private API isolation, public parameters, mobile layout, no persisted tokens']}, indent=2) + '\n')
    print('Control panel integration passed in Chromium and WebKit.')


asyncio.run(main())
