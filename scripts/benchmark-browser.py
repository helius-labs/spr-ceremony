#!/usr/bin/env python3
"""Run one browser rehearsal and verify its public output with the native engine.

Developer automation only: pip install -r scripts/browser-requirements.txt
Participants need only a browser to use a prepared static rehearsal bundle.
"""
import argparse
import hashlib
import json
import pathlib
import platform
import subprocess
import time

from playwright.sync_api import sync_playwright


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=pathlib.Path, required=True)
    parser.add_argument('--native', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--browser', choices=['chromium', 'chrome', 'webkit'], default='chromium')
    parser.add_argument('--timeout', type=int, default=1800, help='seconds per browser contribution')
    parser.add_argument('--fault', choices=['input-hash', 'length-prefix', 'entropy', 'cancel'])
    args = parser.parse_args()
    args.bundle = args.bundle.resolve()
    args.native = args.native.resolve()
    args.out.mkdir(parents=True, exist_ok=False)
    fixture = json.loads((args.bundle / 'fixture.json').read_text())
    started = time.monotonic()
    result = None
    server = subprocess.Popen([str(args.native), 'serve', '--dir', str(args.bundle), '--listen', '127.0.0.1:0'],
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        line = server.stdout.readline()
        if not line:
            raise RuntimeError('Preview server failed: ' + server.stderr.read())
        origin = json.loads(line)['url']
        with sync_playwright() as p:
            engine = p.chromium if args.browser == 'chrome' else getattr(p, args.browser)
            browser = engine.launch(**({'channel': 'chrome'} if args.browser == 'chrome' else {}))
            version = browser.version
            page = browser.new_page(viewport={'width': 1100, 'height': 950}, accept_downloads=True)
            page.set_default_timeout(min(args.timeout * 1000, 30000))
            events, requests = [], []
            page.on('pageerror', lambda error: events.append(str(error)))
            page.on('request', lambda request: requests.append({'method': request.method, 'path': request.url.removeprefix(origin)}))
            if args.fault == 'input-hash':
                def wrong_hash(route):
                    changed = dict(fixture, input=dict(fixture['input'], sha256='0' * 64))
                    route.fulfill(content_type='application/json', body=json.dumps(changed))
                page.route('**/fixture.json', wrong_hash)
            elif args.fault == 'length-prefix':
                bad = bytearray((args.bundle / 'input.ph2').read_bytes())
                bad[34:38] = b'\xff\xff\xff\xff'
                changed = dict(fixture, input=dict(fixture['input'], sha256=hashlib.sha256(bad).hexdigest()))
                page.route('**/fixture.json', lambda route: route.fulfill(content_type='application/json', body=json.dumps(changed)))
                page.route('**/input.ph2', lambda route: route.fulfill(content_type='application/octet-stream', body=bytes(bad)))
            elif args.fault == 'entropy':
                # Test-only response substitution. The shipped engine has no seed
                # injection API or non-cryptographic randomness fallback.
                original = (args.bundle / 'wasm_exec.js').read_text()
                broken = 'globalThis.crypto.getRandomValues = () => { throw new Error("entropy unavailable"); };\n' + original
                page.route('**/wasm_exec.js', lambda route: route.fulfill(content_type='text/javascript', body=broken))
            page.goto(origin, wait_until='networkidle')
            page.wait_for_function('() => window.sprBenchmark.status === "ready"')
            if args.fault == 'cancel':
                page.evaluate('() => { document.getElementById("start").click(); document.getElementById("cancel").click(); }')
            else:
                page.click('#start')
            deadline = time.monotonic() + args.timeout
            previous_stage = None
            while True:
                result = page.evaluate('() => window.sprBenchmark')
                if result['status'] in ['complete', 'error', 'cancelled']:
                    break
                if time.monotonic() >= deadline:
                    raise TimeoutError(f'Browser rehearsal timed out: {result}')
                if result.get('stage') != previous_stage:
                    previous_stage = result.get('stage')
                    print(json.dumps({'browser': args.browser, 'circuit': fixture['circuit'], 'stage': previous_stage}), flush=True)
                page.wait_for_timeout(1000)
            page.screenshot(path=str(args.out / 'page.png'), full_page=True)
            if args.fault:
                expected = 'cancelled' if args.fault == 'cancel' else 'error'
                if result['status'] != expected:
                    raise AssertionError(f'{args.fault} did not fail closed: {result}')
                messages = {'input-hash': 'SHA256 mismatch', 'length-prefix': 'vector size mismatch', 'entropy': 'entropy unavailable'}
                if args.fault in messages and messages[args.fault] not in result.get('error', ''):
                    raise AssertionError(f'Unexpected error: {result}')
                result['fault_rejected'] = args.fault
                assert page.locator('#results').is_hidden()
            else:
                if result['status'] != 'complete':
                    raise RuntimeError(result.get('error', str(result)))
                with page.expect_download() as download:
                    page.click('#save-output')
                output = args.out / 'contribution.ph2'
                download.value.save_as(output)
                with page.expect_download() as download:
                    page.click('#save-report')
                download.value.save_as(args.out / 'browser-report.json')
                verified = subprocess.run([str(args.native), 'verify', '--dir', str(args.bundle), '--contribution', str(output)],
                                          capture_output=True, text=True, check=True)
                result['native'] = json.loads(verified.stdout)
                assert result['native']['output'] == result['report']['output']
                # Alter the public challenge while retaining all point encodings.
                with output.open('r+b') as f:
                    f.seek(-1, 2)
                    last = f.read(1)
                    f.seek(-1, 2)
                    f.write(bytes([last[0] ^ 1]))
                try:
                    rejected = subprocess.run([str(args.native), 'verify', '--dir', str(args.bundle), '--contribution', str(output)],
                                              capture_output=True, text=True)
                finally:
                    with output.open('r+b') as f:
                        f.seek(-1, 2)
                        f.write(last)
                assert rejected.returncode != 0, 'Native verifier accepted a changed challenge'
                result['changed_challenge_rejected'] = True
                result['native_rejection'] = rejected.stderr.strip()
                assert not events, events
            assert all(r['method'] == 'GET' and r['path'].startswith('/') for r in requests), requests
            result.update(browser_engine=args.browser, browser_version=version,
                          platform=platform.platform(), wall_seconds=time.monotonic() - started,
                          fixture=fixture, requests=requests, page_errors=events,
                          safari_tested=False, note='Linux WebKit is not Safari. WASM memory is capacity, not total browser RSS.')
            (args.out / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
            print(json.dumps({k: result[k] for k in ['status', 'browser_engine', 'browser_version', 'wall_seconds']}), flush=True)
            if 'report' in result:
                print(json.dumps({k: result['report'][k] for k in ['circuit', 'engine_seconds', 'wasm_memory_bytes', 'stage_seconds']}), flush=True)
            browser.close()
    except BaseException as error:
        (args.out / 'failure.json').write_text(json.dumps({
            'status': 'failed', 'error': str(error), 'last_browser_state': result,
            'browser_engine': args.browser, 'fixture': fixture,
            'wall_seconds': time.monotonic() - started, 'safari_tested': False,
        }, indent=2) + '\n')
        raise
    finally:
        server.terminate()
        try:
            server.wait(timeout=10)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()


if __name__ == '__main__':
    main()
