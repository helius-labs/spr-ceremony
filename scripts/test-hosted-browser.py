#!/usr/bin/env python3
"""Real browser/Go coordinator integration; all parameters are marked test data."""
import argparse
import asyncio
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid
from urllib.parse import urlparse

from playwright.async_api import async_playwright


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--web-dir', default='dist/browser')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    web = str(Path(args.web_dir).resolve())
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    checks = []
    with tempfile.TemporaryDirectory(prefix='spr-hosted-') as tmp:
        root = Path(tmp)
        def cli(*args):
            result = subprocess.run([binary, *map(str, args)], cwd=root, capture_output=True, text=True)
            if result.returncode:
                raise RuntimeError(result.stderr + result.stdout)
            return result.stdout
        cli('demo', '--out', 'ceremony')
        endpoint = os.environ.get('SPR_S3_ENDPOINT')
        if endpoint:
            assert endpoint.startswith(('http://127.0.0.1:', 'http://localhost:'))
            os.environ.update(AWS_ACCESS_KEY_ID='testing', AWS_SECRET_ACCESS_KEY='testing', AWS_EC2_METADATA_DISABLED='true')
            bucket = 'spr-browser-' + uuid.uuid4().hex
            with urllib.request.urlopen(urllib.request.Request(endpoint + '/' + bucket, data=b'', method='PUT')): pass
            (root / 's3.json').write_text(json.dumps({'bucket': bucket, 'region': 'us-east-1', 'prefix': 'hosted', 'endpoint': endpoint}))
            cli('sync', '--dir', 'ceremony', '--s3-config', 's3.json')
        manifest_hash = hashlib.sha256((root / 'ceremony/manifest.json').read_bytes()).hexdigest()
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        if endpoint:
            cors = f'<CORSConfiguration><CORSRule><AllowedOrigin>{origin}</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedMethod>HEAD</AllowedMethod><AllowedHeader>Range</AllowedHeader><ExposeHeader>Content-Range</ExposeHeader><ExposeHeader>Content-Length</ExposeHeader><ExposeHeader>ETag</ExposeHeader></CORSRule></CORSConfiguration>'
            with urllib.request.urlopen(urllib.request.Request(endpoint + '/' + bucket + '?cors', data=cors.encode(), method='PUT', headers={'Content-Type': 'application/xml'})): pass
        def invite(name):
            path = root / f'{name}.json'
            link = root / f'{name}.link'
            cli('invite', '--dir', 'ceremony', '--url', origin, '--name', name,
                '--out', path, '--link-out', link)
            assert (link.stat().st_mode & 0o077) == 0
            return json.loads(path.read_text()), link.read_text().strip(), path
        log = (out / 'server.log').open('w')
        server = subprocess.Popen([binary, 'serve', '--dir', str(root / 'ceremony'),
                                   '--listen', f'127.0.0.1:{port}', '--public-url', origin,
                                   '--web-dir', web, '--lease-ttl', '6s'] + (['--browser-download-origin', endpoint] if endpoint else []), stdout=log, stderr=log)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(origin + '/healthz', timeout=1): break
                except OSError: time.sleep(.1)
            else: raise RuntimeError('coordinator did not start')
            async with async_playwright() as pw:
                async def connected(browser, name, context=None):
                    invitation, link, path = invite(name)
                    context = context or await browser.new_context(accept_downloads=True)
                    # A browser may never settle the optional wake-lock release.
                    # Completion must still restore controls and native handoff.
                    await context.add_init_script("Object.defineProperty(navigator, 'wakeLock', {value: {request: async () => ({release: () => new Promise(() => {})})}})")
                    page = await context.new_page()
                    await page.goto(link)
                    await page.wait_for_function('() => !document.querySelector("#start").disabled', timeout=20000)
                    assert '#' not in page.url
                    return context, page, invitation, path
                async def terminal(page, success=True):
                    await page.wait_for_function('() => ["complete", "error", "paused"].includes(document.documentElement.dataset.stage)', timeout=60000)
                    stage = await page.get_attribute('html', 'data-stage')
                    message = await page.locator('#status').inner_text()
                    if stage == 'error':
                        await page.screenshot(path=out / 'error.png', full_page=True)
                    assert stage == ('complete' if success else 'error'), (stage, message)
                    if success: assert '2 / 2 accepted' in await page.locator('#count').inner_text()
                    await page.wait_for_function('() => document.querySelector("#pause").disabled && !document.querySelector("#native").disabled')
                for engine in ['chromium', 'webkit']:
                    browser = await getattr(pw, engine).launch()
                    context, page, invitation, path = await connected(browser, engine)
                    requests, errors = [], []
                    context.on('request', lambda r: requests.append((r.method, r.url, r.headers)))
                    page.on('pageerror', lambda e: errors.append(str(e)))
                    await page.locator('#start').click()
                    await terminal(page)
                    await page.locator('#native').click()
                    assert await page.locator('#native-panel').is_visible()
                    await page.locator('#native').click()
                    assert not errors, errors
                    allowed = {urlparse(origin).netloc, urlparse(endpoint).netloc if endpoint else ''}
                    assert requests and all(urlparse(u).netloc in allowed for _, u, _ in requests)
                    external = [(m, u, h) for m, u, h in requests if urlparse(u).netloc != urlparse(origin).netloc]
                    assert all(m == 'GET' and 'authorization' not in h and 'cookie' not in h for m, u, h in external)
                    if endpoint: assert external
                    assert all(invitation['token'] not in u for _, u, _ in requests)
                    assert not await context.cookies()
                    assert await page.evaluate('() => localStorage.length + sessionStorage.length') == 0
                    persisted = await page.evaluate('''() => new Promise((resolve, reject) => {
                      const r = indexedDB.open("spr-public-work-v1");
                      r.onsuccess = () => {
                        const db = r.result, tx = db.transaction("records"), request = tx.objectStore("records").getAll();
                        request.onsuccess = () => resolve(JSON.stringify(request.result));
                        tx.oncomplete = () => db.close();
                      };
                      r.onerror = () => reject(r.error);
                    })''')
                    assert invitation['token'] not in persisted
                    async with page.expect_download() as downloaded:
                        await page.locator('#receipts').click()
                    receipt_path = out / f'{engine}-receipts.json'
                    await (await downloaded.value).save_as(receipt_path)
                    assert invitation['token'] not in receipt_path.read_text()
                    cli('verify-attestation', '--dir', 'ceremony', '--attestation', receipt_path, '--manifest-sha256', manifest_hash)
                    await page.screenshot(path=out / f'{engine}.png', full_page=True)
                    checks.append(f'{engine}: real queued contribution, checkpoint inclusion, receipts export, credential isolation')
                    await context.close()
                    await browser.close()

                browser = await pw.chromium.launch()
                # A lost successful response must retry exactly the same output.
                context, page, invitation, path = await connected(browser, 'lost_response')
                lost = []
                async def lose_response(route):
                    response = await route.fetch()
                    if not lost and response.status == 200:
                        lost.append(True)
                        await route.abort()
                    else: await route.fulfill(response=response)
                await context.route('**/v1/contributions/**', lose_response)
                await page.locator('#start').click()
                await terminal(page)
                assert lost
                checks.append('lost acceptance response: idempotent upload and receipt recovery')
                await context.close()

                # Persist output, fail upload, reload, then reuse without recomputing.
                context, page, invitation, path = await connected(browser, 'reload')
                worker_requests = []
                context.on('request', lambda r: worker_requests.append(r.url) if r.url.endswith('/worker.js') else None)
                async def reject_upload(route): await route.fulfill(status=503, body='test publication unavailable')
                await context.route('**/v1/contributions/**', reject_upload)
                await page.locator('#start').click()
                await terminal(page, False)
                assert len(worker_requests) == 1
                await context.unroute('**/v1/contributions/**', reject_upload)
                await page.reload()
                await page.locator('#invitation').set_input_files(path)
                await page.wait_for_function('() => !document.querySelector("#start").disabled')
                await page.locator('#start').click()
                await terminal(page)
                assert len(worker_requests) == 2, worker_requests
                checks.append('reload after failed publication: retained output reused, no repeated entropy computation')
                await context.close()

                # Hold one worker past the short lease lifetime. The main thread
                # must renew while another participant advances another circuit.
                context, page, invitation, path = await connected(browser, 'parallel_a')
                heartbeats = []
                context.on('request', lambda r: heartbeats.append(r.url) if '/heartbeat/' in r.url else None)
                async def slow_worker(route):
                    await asyncio.sleep(8)
                    await route.continue_()
                await context.route('**/worker.js', slow_worker)
                await page.locator('#start').click()
                await page.wait_for_function('() => document.documentElement.dataset.stage === "download" || document.documentElement.dataset.stage === "load_engine"')
                # Same-profile duplicate tab cannot claim while the first holds its lock.
                duplicate = await context.new_page()
                await duplicate.goto(origin)
                await duplicate.locator('#invitation').set_input_files(path)
                await duplicate.wait_for_function('() => !document.querySelector("#start").disabled')
                await duplicate.locator('#start').click()
                await terminal(duplicate, False)
                assert 'another tab' in await duplicate.locator('#status').inner_text()
                second, page2, _, _ = await connected(browser, 'parallel_b')
                await page2.locator('#start').click()
                await asyncio.gather(terminal(page), terminal(page2))
                assert len(heartbeats) >= 3
                checks.append('short leases renewed; concurrent contributors on separate circuits; duplicate tab refused')
                await context.close(); await second.close()

                # Saved output on an advanced head is discarded, never transplanted.
                context, page, invitation, path = await connected(browser, 'stale_recovery')
                workers = []
                context.on('request', lambda r: workers.append(r.url) if r.url.endswith('/worker.js') else None)
                await context.route('**/v1/contributions/**', reject_upload)
                await page.locator('#start').click()
                await terminal(page, False)
                _, _, intervening = invite('intervening_native')
                await asyncio.to_thread(cli, 'contribute', '--invitation', intervening, '--work', 'intervening-work')
                await context.unroute('**/v1/contributions/**', reject_upload)
                await page.locator('#start').click()
                await terminal(page)
                assert len(workers) == 3
                checks.append('advanced circuit head: stale saved output discarded and freshly recomputed')
                await context.close()

                # Invalid receipt data cannot turn a completed computation into a
                # claimed acceptance; reopening recovers the authentic checkpoint.
                context, page, invitation, path = await connected(browser, 'receipt_mismatch')
                async def alter_receipt(route):
                    response = await route.fetch()
                    body = await response.json()
                    body['output']['sha256'] = '0' * 64
                    await route.fulfill(response=response, json=body)
                await context.route('**/v1/contributions/**', alter_receipt)
                await page.locator('#start').click()
                await terminal(page, False)
                assert 'receipt does not match' in await page.locator('#status').inner_text()
                await context.unroute('**/v1/contributions/**', alter_receipt)
                await page.locator('#start').click()
                await terminal(page)
                checks.append('altered acceptance receipt rejected; authentic checkpoint recovers response')
                await context.close()

                # Valid receipt fields do not compensate for a forged GPG signature.
                context, page, invitation, path = await connected(browser, 'receipt_signature')
                async def alter_signature(route):
                    response = await route.fetch()
                    body = await response.json(); body['signature'] = 'invalid signature'
                    await route.fulfill(response=response, json=body)
                await context.route('**/v1/contributions/**', alter_signature)
                await page.locator('#start').click(); await terminal(page, False)
                assert 'Invalid coordinator signature' in await page.locator('#status').inner_text()
                await context.unroute('**/v1/contributions/**', alter_signature)
                await page.locator('#start').click(); await terminal(page)
                checks.append('forged GPG acceptance signature rejected; authentic signed checkpoint recovers')
                await context.close()

                invitation, link, path = invite('invitation_signature')
                context = await browser.new_context()
                async def alter_invitation(route):
                    response = await route.fetch()
                    body = await response.json(); body['signature'] = 'invalid signature'
                    await route.fulfill(response=response, json=body)
                await context.route('**/v1/invitation', alter_invitation)
                page = await context.new_page(); await page.goto(link)
                await page.wait_for_function('() => document.querySelector("#status").textContent.includes("Invalid coordinator signature")')
                assert await page.locator('#start').is_disabled()
                checks.append('forged GPG invitation signature rejected before contribution')
                await context.close()

                # Abort while downloading releases the lease immediately.
                context, page, invitation, path = await connected(browser, 'pause_resume')
                started_download = asyncio.Event()
                async def delay_download(route):
                    if route.request.headers.get('range'):
                        started_download.set()
                        await asyncio.sleep(1)
                    try: await route.continue_()
                    except Exception: pass  # Request was cancelled by Pause.
                pattern = '**/v1/objects/**' if not endpoint else endpoint + '/**'
                await context.route(pattern, delay_download)
                await page.locator('#start').click()
                await asyncio.wait_for(started_download.wait(), 15)
                await page.locator('#pause').click()
                await page.wait_for_function('() => document.documentElement.dataset.stage === "paused"')
                assert not list((root / 'ceremony/leases').glob('*.json'))
                await context.unroute(pattern, delay_download)
                await page.locator('#start').click()
                await terminal(page)
                checks.append('pause cancels transfer and releases the circuit; resume completes')
                await context.close()

                # Exercise chunk recovery and range authentication independently
                # of expensive curve arithmetic, with a multi-chunk public file.
                context = await browser.new_context()
                page = await context.new_page()
                await page.goto(origin)
                payload = b'public-parameters' * (600000)
                digest = hashlib.sha256(payload).hexdigest()
                ranges, broken = [], [True]
                async def chunks(route):
                    bounds = route.request.headers['range'].removeprefix('bytes=').split('-')
                    start, end = map(int, bounds)
                    ranges.append(start)
                    if start > 0 and broken[0]:
                        await route.fulfill(status=400, body='interrupted transfer')
                        return
                    await route.fulfill(status=206, body=payload[start:end + 1], headers={
                        'Content-Range': f'bytes {start}-{end}/{len(payload)}', 'Content-Type': 'application/octet-stream'})
                await context.route('**/v1/objects/' + digest, chunks)
                transfer = """async ref => {
                    const { download } = await import('/transfer.js');
                    const { PublicCache } = await import('/cache.js');
                    const cache = await PublicCache.open('range-test');
                    try {
                      const bytes = await download(ref, cache, {}, new AbortController().signal, () => {});
                      return {hash: await sprSHA256(bytes)};
                    } catch(e) { return {error: e.message}; }
                    finally { cache.close(); }
                }"""
                ref = {'sha256': digest, 'size': len(payload)}
                assert 'error' in await page.evaluate(transfer, ref)
                broken[0] = False
                result = await page.evaluate(transfer, ref)
                assert result['hash'] == digest and ranges.count(0) == 1, (result, ranges)
                async def wrong_range(route):
                    await route.fulfill(status=206, body=b'bad', headers={'Content-Range': 'bytes 0-2/3'})
                await context.route('**/v1/objects/' + 'e' * 64, wrong_range)
                result = await page.evaluate(transfer, {'sha256': 'e' * 64, 'size': 100})
                assert 'Invalid download range' in result['error']
                checks.append('multi-chunk download resumes saved ranges; malformed Content-Range rejected')
                await context.close()

                # Native receives the exact same invitation and completes the queue.
                context, page, invitation, path = await connected(browser, 'native_handoff')
                await context.route('**/v1/contributions/**', reject_upload)
                await page.locator('#start').click()
                await terminal(page, False)
                await context.unroute('**/v1/contributions/**', reject_upload)
                await page.locator('#native').click()
                async with page.expect_download() as downloaded:
                    await page.locator('#manual-downloads summary').click()
                    await page.locator('#save-invitation').click()
                native_invite = root / 'native-download.json'
                await (await downloaded.value).save_as(native_invite)
                assert json.loads(native_invite.read_text()) == invitation
                await asyncio.to_thread(cli, 'contribute', '--invitation', native_invite, '--work', 'native-work')
                await page.locator('#refresh').click()
                await page.wait_for_function('() => document.documentElement.dataset.stage === "complete"')
                await terminal(page)
                checks.append('native invitation handoff and accepted progress displayed')
                await context.close()

                # Wrong manifest pins and an expired invitation stop before scheduling.
                invitation, link, path = invite('wrong_pin')
                context = await browser.new_context()
                page = await context.new_page()
                claims = []
                context.on('request', lambda r: claims.append(r.url) if r.url.endswith('/v1/claim') else None)
                await page.goto(link.replace(manifest_hash, '0' * 64))
                await page.wait_for_function('() => document.querySelector("#status").classList.contains("error")')
                assert not claims
                checks.append('manifest substitution rejected before claiming')
                await context.close()
                pending, _, pending_path = invite('closed_pending')
                cli('close', '--dir', 'ceremony')
                context = await browser.new_context()
                page = await context.new_page()
                await page.goto(origin)
                await page.locator('#invitation').set_input_files(root / 'chromium.json')
                await terminal(page)
                await page.reload()
                await page.locator('#invitation').set_input_files(pending_path)
                await page.wait_for_function('() => document.querySelector("#status").textContent.includes("closed")')
                assert await page.locator('#start').is_disabled()
                checks.append('closed ceremony retains accepted progress and disables unfinished work')
                await context.close()
                await browser.close()
            # All browser updates receive independent native cryptographic replay.
            cli('audit', '--dir', 'ceremony', '--manifest-sha256', manifest_hash)
            states = json.loads(cli('status', '--dir', 'ceremony'))['circuits']
            expected = {'chromium', 'webkit', 'lost_response', 'reload', 'parallel_a', 'parallel_b', 'native_handoff', 'stale_recovery', 'intervening_native', 'receipt_mismatch', 'receipt_signature', 'pause_resume'}
            assert all({r['contributor'] for r in s['history']} == expected and len(s['history']) == len(expected) for s in states)
            checks.append('native audit replays every browser transition; no duplicate accepted entries')
            if endpoint:
                cli('sync', '--dir', 'ceremony', '--out', 'recovery.json')
                digest = hashlib.sha256((root / 'recovery.json').read_bytes()).hexdigest()
                cli('restore', '--s3-config', 's3.json', '--recovery-file', 'recovery.json', '--recovery-sha256', digest, '--out', 'restored')
                cli('audit', '--dir', 'restored', '--manifest-sha256', manifest_hash)
                checks.append('S3 presigned range downloads, no browser AWS credentials, accepted transcript restored and audited')
        finally:
            server.terminate()
            server.wait(timeout=65)
            log.close()
    (out / 'result.json').write_text(json.dumps({'checks': checks, 'safari_tested': False}, indent=2) + '\n')
    print(json.dumps({'passed': len(checks), 'checks': checks}))

if __name__ == '__main__':
    asyncio.run(main())
