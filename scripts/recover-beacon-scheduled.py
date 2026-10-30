#!/usr/bin/env python3
"""Run an announced beacon recovery through the pinned CLI; never edit the store."""
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import re
import subprocess


def timestamp(value):
    result = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.utcoffset() != dt.timedelta(0):
        raise ValueError('UTC deadline required')
    return result


def sha(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def checkpoint(root, ref=None):
    if ref is None:
        ref = json.loads((root / 'checkpoint.json').read_text())
    if not re.fullmatch('[a-f0-9]{64}', ref['sha256']) or not 0 < ref['size'] <= 4 << 20:
        raise ValueError('Invalid checkpoint reference')
    path = root / 'objects' / ref['sha256']
    if path.stat().st_size != ref['size'] or sha(path) != ref['sha256']:
        raise ValueError('Checkpoint hash mismatch')
    return ref, json.loads(path.read_text())


def check_plan(plan, root, binary, now, ref=None):
    if sha(binary) != plan['binary_sha256'] or sha(root / 'manifest.json') != plan['manifest_sha256']:
        raise ValueError('Binary or ceremony differs from the announced plan')
    old, new = timestamp(plan['old_deadline']), timestamp(plan['new_deadline'])
    if new - old != dt.timedelta(hours=12) or plan['new_round'] - plan['old_round'] != 14400:
        raise ValueError('Expected an exact 12-hour quicknet extension')
    if plan['chain_hash'] != '52db9ba70e0cc0f6eaf7803dd07447a1f5477735fd3f661792ba94600c84e971':
        raise ValueError('Wrong beacon network')
    ref, cp = checkpoint(root, ref)
    if cp['manifest']['sha256'] != plan['manifest_sha256']:
        raise ValueError('Checkpoint belongs to another ceremony')
    life = cp['lifecycle']
    beacon = life.get('beacon', {})
    if beacon.get('chain_hash') != plan['chain_hash']:
        raise ValueError('Beacon network changed')
    if beacon.get('round') == plan['new_round'] and timestamp(beacon['round_time']) == new:
        for recovery in life.get('beacon_recoveries', []):
            if recovery['previous']['round'] == plan['old_round'] and recovery['next']['round'] == plan['new_round'] and recovery['reason'] == plan['reason']:
                return ref, cp, True
        raise ValueError('Target round is active without the expected recovery record')
    if life['phase'] != 'open' or beacon.get('round') != plan['old_round'] or timestamp(beacon['round_time']) != old:
        raise ValueError('Ceremony closed or beacon plan changed; do not recover')
    if now + dt.timedelta(hours=1) >= new:
        raise ValueError('Target beacon is too near or has passed')
    return ref, cp, False


def execute(plan, root, binary, s3_config, now, check_only=False):
    before_ref, before, done = check_plan(plan, root, binary, now)
    if done:
        return {'status': 'already_recovered', 'checkpoint': before_ref}
    if check_only:
        return {'status': 'ready', 'checkpoint': before_ref, 'new_deadline': plan['new_deadline']}
    if now < timestamp(plan['old_deadline']):
        raise ValueError('Original beacon has not passed; no recovery attempted')
    result = subprocess.run([str(binary), 'recover-beacon', '--dir', str(root),
                             '--s3-config', str(s3_config), '--round', str(plan['new_round']),
                             '--checkpoint-sha256', before_ref['sha256'], '--reason', plan['reason']],
                            capture_output=True, text=True, timeout=1800)
    if result.returncode:
        raise RuntimeError('CLI recovery failed; inspect the coordinator before retrying')
    after_ref, after = checkpoint(root, json.loads(result.stdout))
    if after['states'] != before['states'] or after.get('exports') != before.get('exports'):
        raise RuntimeError('Unexpected circuit or export state change')
    if after['previous'] != before_ref or after['lifecycle']['beacon']['round'] != plan['new_round']:
        raise RuntimeError('Recovery checkpoint does not match the announcement')
    # Check the CLI-recorded transition as well as its new public deadline.
    _, _, done = check_plan(plan, root, binary, now, after_ref)
    if not done:
        raise RuntimeError('Recovery transition missing')
    return {'status': 'recovered', 'previous_checkpoint': before_ref, 'checkpoint': after_ref,
            'new_round': plan['new_round'], 'new_deadline': plan['new_deadline'], 'circuit_states_unchanged': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plan', type=Path, required=True)
    parser.add_argument('--dir', type=Path, required=True)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--s3-config', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    plan = json.loads(args.plan.read_text())
    print(json.dumps(execute(plan, args.dir, args.binary, args.s3_config,
                             dt.datetime.now(dt.timezone.utc), args.check)))


if __name__ == '__main__':
    main()
