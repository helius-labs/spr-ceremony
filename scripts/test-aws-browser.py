#!/usr/bin/env python3
"""Exercise an internal marked rehearsal through its existing SSM tunnels."""
import argparse
import asyncio
import json
from pathlib import Path
import sys
import time
import traceback
import urllib.parse
import urllib.request

from playwright.async_api import async_playwright


async def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, help='independently verified release binary')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    out = Path(args.out); out.mkdir(parents=True, exist_ok=True)
    public, private = 'http://127.0.0.1:8080', 'http://127.0.0.1:8081'
    with urllib.request.urlopen(public + '/v1/ceremony', timeout=30) as response:
        before = json.load(response)
    assert before['insecure_test_setup'] is True and before['phase'] == 'open', 'Requires an open marked rehearsal'
    assert len(before['circuits']) == 2, 'Requires the two-circuit demo'
    stamp = str(time.time_ns())
    results = []
    async with async_playwright() as pw:
        for engine in ['chromium', 'webkit']:
            print(engine + ': invitation, browser contribution and revocation', flush=True)
            browser = await getattr(pw, engine).launch()
            context = await browser.new_context(viewport={'width': 390, 'height': 844})
            context.set_default_timeout(120000)
            page = await context.new_page()
            await page.goto(private)
            name = 'aws-' + engine + '-' + stamp
            await page.locator('#name').fill(name)
            await page.locator('#invite').click()
            await page.wait_for_function('() => !document.querySelector("#secret").hidden')
            invitation = await page.locator('#link').input_value()
            token = urllib.parse.parse_qs(urllib.parse.urlsplit(invitation).fragment)['token'][0]
            await page.locator('#clear').click()
            admin_status = await page.evaluate('''async () => await (await fetch('/admin/status', {headers: {'X-SPR-Admin':'1'}})).text()''')
            assert token not in admin_status, 'Control status exposed an invitation'
            assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
            visitor = await context.new_page()
            s3_downloads = []

            def response_seen(response):
                host = urllib.parse.urlsplit(response.url).hostname or ''
                if '.s3.' in host and host.endswith('.amazonaws.com') and response.status in [200, 206]:
                    s3_downloads.append(response.status)

            visitor.on('response', response_seen)
            await visitor.goto(invitation)
            await visitor.wait_for_function('() => !document.querySelector("#start").disabled')
            assert urllib.parse.urlsplit(visitor.url).fragment == '', 'Invitation was not removed from the address bar'
            assert 'Locally generated parameters' in await visitor.locator('#notice').inner_text()
            await visitor.locator('#start').click()
            await visitor.wait_for_function('() => document.querySelector("#count").textContent === "2 / 2 accepted"', timeout=300000)
            await visitor.wait_for_function('() => document.documentElement.dataset.stage === "complete" && !document.querySelector("#native").disabled')
            print(engine + ': contributions accepted; checking terminal handoff', flush=True)
            print('Public UI state:', await visitor.evaluate('() => ({stage: document.documentElement.dataset.stage, nativeDisabled: document.querySelector("#native").disabled})'), flush=True)
            assert s3_downloads, 'Browser did not exercise real S3 downloads'
            assert await visitor.evaluate('() => localStorage.length + sessionStorage.length') == 0
            assert not await context.cookies()
            assert await visitor.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
            await visitor.locator('#native').click()
            assert not await visitor.locator('#one-liner').is_visible(), 'HTTPS installer offered on an HTTP tunnel'
            command = await visitor.locator('#verify-command').text_content()
            assert "--proto '=http'" in command and '--bundle provenance.json' in command
            await visitor.screenshot(path=out / (engine + '-participant.png'), full_page=True)
            print(engine + ': checking control panel progress', flush=True)
            await page.locator('#refresh').click()
            row = page.locator('.invitation-row').filter(has_text=name)
            await page.wait_for_function('(name) => [...document.querySelectorAll(".invitation-row")].some(row => row.textContent.includes(name) && row.textContent.includes("2/2 accepted"))', arg=name)
            await page.screenshot(path=out / (engine + '-control.png'), full_page=True)
            assert await page.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
            assert (await context.request.get(public + '/admin/status')).status == 404
            await row.get_by_role('button', name='Revoke', exact=True).click()
            await row.get_by_role('button', name='Revoke access', exact=True).click()
            await page.wait_for_function('() => document.querySelector("#status").textContent === "Access revoked."')
            assert (await context.request.get(public + '/v1/invitation', headers={'Authorization': 'Bearer ' + token})).status == 401
            results.append({'engine': engine, 'accepted': 2, 'real_s3_downloads': len(s3_downloads), 'revocation': 'passed'})
            await context.close(); await browser.close()

        print('native: invitation, join and idempotent retry', flush=True)
        browser = await pw.chromium.launch()
        context = await browser.new_context()
        page = await context.new_page()
        await page.goto(private)
        name = 'aws-native-' + stamp
        await page.locator('#name').fill(name); await page.locator('#invite').click()
        await page.wait_for_function('() => !document.querySelector("#secret").hidden')
        invitation = await page.locator('#link').input_value()
        token = urllib.parse.parse_qs(urllib.parse.urlsplit(invitation).fragment)['token'][0]
        await page.locator('#clear').click()
        for _ in range(2):
            process = await asyncio.create_subprocess_exec(binary, 'join', '--work', str(out / 'public-work'), stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            stdout, stderr = await asyncio.wait_for(process.communicate(invitation.encode() + b'\n'), timeout=180)
            assert process.returncode == 0, 'Native join failed'
            assert token.encode() not in stdout + stderr, 'Native output exposed an invitation'
        await page.locator('#refresh').click()
        row = page.locator('.invitation-row').filter(has_text=name)
        await row.get_by_role('button', name='Revoke', exact=True).click()
        await row.get_by_role('button', name='Revoke access', exact=True).click()
        await page.wait_for_function('() => document.querySelector("#status").textContent === "Access revoked."')
        await context.close(); await browser.close()

    with urllib.request.urlopen(public + '/v1/ceremony', timeout=30) as response:
        after = json.load(response)
    old_counts = {c['id']: c['contributions'] for c in before['circuits']}
    assert all(c['contributions'] == old_counts[c['id']] + 3 for c in after['circuits']), 'Duplicate or missing contribution'
    report = {'browser': results, 'native_join_and_retry': 'passed', 'private_api_isolation': 'passed', 'checkpoint': after['checkpoint'], 'manifest': after['manifest'], 'rehearsal_only': True}
    (out / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
    print('Internal AWS browser and native contribution checks passed.')


if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as error:
        # Browser exceptions can include private URLs. Keep those out of test logs.
        print('Internal rehearsal check failed (' + type(error).__name__ + '). No private browser diagnostics were logged.', file=sys.stderr)
        for frame in traceback.extract_tb(error.__traceback__):
            if Path(frame.filename).name == Path(__file__).name:
                print('Test location: ' + frame.name + ':' + str(frame.lineno), file=sys.stderr)
        raise SystemExit(1)
