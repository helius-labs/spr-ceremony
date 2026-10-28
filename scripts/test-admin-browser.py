#!/usr/bin/env python3
"""Private control panel, native join and public key view with real Go servers."""
import argparse
import asyncio
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import traceback
import urllib.request
from playwright.async_api import async_playwright
from cli_test_proxy import Proxy


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
        upstream = port()
        proxy = Proxy(upstream)
        public, private = proxy.origin, f'http://127.0.0.1:{port()}'
        processes = []
        with (out / 'server.log').open('w') as log:
            try:
                for cmd, origin, flags in [('serve', public, ['--web-dir', web]), ('admin', private, [])]:
                    processes.append(subprocess.Popen([binary, cmd, '--dir', str(root / 'ceremony'), '--listen', f'127.0.0.1:{upstream}' if cmd == 'serve' else origin.removeprefix('http://'), '--public-url', public, *flags], stdout=log, stderr=log))
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
                        await page.goto(private.replace('127.0.0.1', 'localhost'))
                        assert page.url == private + '/', 'Localhost landing page did not redirect to the canonical address'
                        await page.locator('#name').fill(engine)
                        await page.locator('#invite').click()
                        await page.wait_for_function('() => !document.querySelector("#secret").hidden')
                        assert await page.locator('#secret-name').inner_text() == engine
                        assert await page.locator('#name').input_value() == ''
                        assert await page.locator('#secret').evaluate('(element) => element.open')
                        await page.screenshot(path=out / f'{engine}-invitation.png', mask=[page.locator('#link')])
                        invitation = await page.locator('#link').input_value()
                        assert '#manifest=' in invitation and '&token=' in invitation
                        assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
                        assert not await context.cookies()
                        async with page.expect_download() as downloaded: await page.locator('#download').click()
                        download = await downloaded.value
                        private_file = await download.path()
                        saved = json.loads(Path(private_file).read_text())
                        token, identity = saved['token'], saved['id']
                        response = await page.evaluate('''async () => JSON.stringify(await (await fetch('/admin/status', {headers: {'X-SPR-Admin': '1'}})).json())''')
                        assert token not in response
                        await page.locator('#clear').click()
                        assert await page.locator('#link').input_value() == ''
                        # Keep the first invitation partially complete while the
                        # operator creates another contributor and reissues it.
                        participant = await context.new_page()
                        await participant.goto(invitation)
                        await participant.locator('#copy-invitation').wait_for(state='visible')
                        assert await participant.locator('#start, #pause, input[type=file]').count() == 0
                        assert '#' not in participant.url
                        await participant.close()
                        proxy.block_after_first_claim.add(token)
                        partial = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(root / 'partial-work'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                        partial_result = asyncio.create_task(partial.communicate(invitation.encode() + b'\n'))
                        try:
                            for attempt in range(150):
                                response = await page.evaluate("async () => (await fetch('/admin/status', {headers: {'X-SPR-Admin': '1'}})).json()")
                                if any(i['id'] == identity and i['accepted'] == 1 for i in response['invitations']):
                                    break
                                await asyncio.sleep(.2)
                            else: raise RuntimeError('Partial CLI contribution did not complete')
                        finally:
                            if partial.returncode is None: partial.terminate()
                            stdout, stderr = await asyncio.wait_for(partial_result, timeout=15)
                        assert token.encode() not in stdout + stderr
                        await page.locator('#refresh').click()
                        row = page.locator(f'.invitation-row[data-id="{identity}"]')
                        await row.get_by_text(f'{engine} · 1/2 accepted', exact=True).wait_for()
                        await page.locator('#name').fill(engine)
                        assert await page.locator('#existing').is_visible()
                        assert '1/2 accepted' in await page.locator('#existing-name').inner_text()
                        assert await page.locator('#invite').is_disabled()
                        await page.locator('#resume-existing').click()
                        await page.wait_for_function('() => document.querySelector("#secret").open')
                        assert await page.locator('#secret-name').inner_text() == engine
                        invitation = await page.locator('#link').input_value()
                        assert (await context.request.get(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + token})).status == 401
                        from urllib.parse import parse_qs, urlsplit
                        token = parse_qs(urlsplit(invitation).fragment)['token'][0]
                        await page.locator('#clear').click()
                        await page.locator('#name').fill(engine + '-next')
                        assert await page.locator('#existing').is_hidden()
                        assert await page.locator('#invite').is_enabled()
                        await page.locator('#invite').click()
                        await page.wait_for_function('() => document.querySelector("#secret").open')
                        assert await page.locator('#secret-name').inner_text() == engine + '-next'
                        await page.keyboard.press('Escape')
                        await page.wait_for_function('() => document.querySelector("#secret").hidden')
                        assert await page.locator('#link').input_value() == ''
                        assert '1/2 accepted' in await row.inner_text()
                        process = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(root / 'work'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                        stdout, stderr = await asyncio.wait_for(process.communicate(invitation.encode() + b'\n'), timeout=60)
                        assert process.returncode == 0, 'Native join failed'
                        assert token.encode() not in stdout + stderr
                        await page.locator('#refresh').click()
                        await row.get_by_text(f'{engine} · 2/2 accepted', exact=True).wait_for()
                        # A row action must identify its contributor independently
                        # of the name currently entered in the new-invitation form.
                        await page.locator('#name').fill('someone-else')
                        await row.locator('button', has_text='Reissue link').click()
                        await page.wait_for_function('() => !document.querySelector("#secret").hidden')
                        assert await page.locator('#secret-name').inner_text() == engine
                        assert await page.locator('#name').input_value() == 'someone-else'
                        renewed = await page.locator('#link').input_value()
                        assert renewed != invitation
                        assert (await context.request.get(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + token})).status == 401
                        token = parse_qs(urlsplit(renewed).fragment)['token'][0]
                        await page.locator('#clear').click()
                        visitor_check = await context.new_page(); await visitor_check.goto(renewed)
                        await visitor_check.locator(f'#records a[href="/v1/records/{identity}"]').wait_for(state='attached')
                        await visitor_check.close()
                        await page.screenshot(path=out / f'{engine}-control.png', full_page=True)
                        assert await page.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
                        visitor = await context.new_page(); await visitor.goto(public)
                        await visitor.locator('#native-title').wait_for(state='visible')
                        assert await visitor.locator('#start, #pause, input[type=file]').count() == 0
                        await visitor.locator('#artifacts summary').click()
                        await visitor.wait_for_function('() => document.querySelectorAll("#keys .key-row").length === 2')
                        assert 'after' in await visitor.locator('#keys-status').inner_text()
                        assert await visitor.locator('a', has_text='Proving key').count() == 0
                        await visitor.screenshot(path=out / f'{engine}-parameters.png', full_page=True)
                        public_admin = await context.request.get(public + '/admin/status')
                        assert public_admin.status == 404
                        await row.get_by_role('button', name='Revoke', exact=True).click()
                        await row.get_by_role('button', name='Cancel', exact=True).click()
                        assert await row.locator('.revoke-confirmation').count() == 0
                        await row.get_by_role('button', name='Revoke', exact=True).click()
                        await row.get_by_role('button', name='Revoke access', exact=True).click()
                        await page.wait_for_function('() => document.querySelector("#status").textContent === "Access revoked."')
                        assert await row.locator('.invitation-state').inner_text() == 'Revoked'
                        assert '2/2 accepted' in await row.inner_text()
                        assert await row.get_by_role('button', name='Revoke', exact=True).count() == 0
                        await page.screenshot(path=out / f'{engine}-revoked.png', full_page=True)
                        rejected = await context.request.get(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + token})
                        assert rejected.status == 401
                        await visitor.close()
                        visitor = await context.new_page()
                        await visitor.goto(renewed)
                        await visitor.locator('#copy-invitation').wait_for(state='visible')
                        rejected_cli = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(root / 'revoked-work'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                        stdout, stderr = await asyncio.wait_for(rejected_cli.communicate(renewed.encode() + b'\n'), timeout=30)
                        assert rejected_cli.returncode != 0
                        assert token.encode() not in stdout + stderr
                        assert token not in (out / 'server.log').read_text()
                        await context.close(); await browser.close()
            finally:
                for process in processes: process.terminate()
                for process in processes: process.wait(timeout=15)
                proxy.close()
    (out / 'result.json').write_text(json.dumps({'passed': ['Chromium and WebKit: partial native contribution, CLI-only participant page, new invitation while another is incomplete, duplicate-name guidance, named private-link dialog, Escape clears link, hidden native resume, reissue with unrelated form input, revoke/cancel, visible revoked state, preserved accepted work, private API isolation, public parameters, mobile layout, no persisted tokens']}, indent=2) + '\n')
    print('Control panel integration passed in Chromium and WebKit.')


if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as error:
        print('Control panel check failed (' + type(error).__name__ + '). Private diagnostics omitted.', file=sys.stderr)
        for frame in traceback.extract_tb(error.__traceback__):
            if Path(frame.filename).name == Path(__file__).name:
                print('Test location: ' + frame.name + ':' + str(frame.lineno), file=sys.stderr)
        raise SystemExit(1)
