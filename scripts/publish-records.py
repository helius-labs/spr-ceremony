#!/usr/bin/env python3
"""Append verified workflow artifacts and render the contributor index.

Signature verification happens in `spr-ceremony records --github-out` in the
read-only job. This writer checks paths/structure and never replaces records.
"""
import argparse
import json
import re
from pathlib import Path


def publish(source, root):
    for path in sorted(source.rglob('*')):
        if path.is_symlink():
            raise ValueError('Artifact contains a symlink')
        if path.is_dir():
            continue
        relative = path.relative_to(source)
        if not re.fullmatch(r'(records|github)/[a-f0-9]{64}/[a-f0-9]{64}\.json', relative.as_posix()):
            raise ValueError('Invalid artifact path')
        data = path.read_bytes()
        if len(data) > 4 << 20:
            raise ValueError('Oversized record')
        record = json.loads(data)
        if record['manifest']['sha256'] != relative.parts[1] or record['invitation_id'] + '.json' != relative.name:
            raise ValueError('Record path differs from identity')
        if relative.parts[0] == 'records':
            if record['format'] != 'spr-contribution-record-v1' or record['status'] != 'accepted' or record['total'] != 41 or len(record['receipts']) != 41:
                raise ValueError('Incomplete contribution record')
        elif record['format'] != 'spr-github-identity-v1' or type(record['github_id']) is not int or record['github_id'] <= 0 or not re.fullmatch(r'[a-zA-Z0-9-]{1,39}', record['github_login']) or not record['signature']:
            raise ValueError('Invalid GitHub identity')
        target = root / 'contributions' / relative
        if target.exists():
            if target.read_bytes() != data:
                raise ValueError('Existing record differs; do not overwrite')
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open('xb') as output:
                output.write(data)
    identities = {}
    for path in sorted((root / 'contributions/github').glob('*/*.json')):
        identity = json.loads(path.read_text())
        manifest, invitation = identity['manifest']['sha256'], identity['invitation_id']
        record_path = root / 'contributions/records' / manifest / (invitation + '.json')
        record = json.loads(record_path.read_text())
        if identity['contributor'] != record['contributor']:
            raise ValueError('Contribution identity mismatch')
        if not re.fullmatch(r'[a-zA-Z0-9-]{1,39}', identity['github_login']):
            raise ValueError('Invalid profile name')
        identities[(manifest, invitation)] = identity
    campaigns = {}
    for path in sorted((root / 'contributions/records').glob('*/*.json')):
        record = json.loads(path.read_text())
        if not re.fullmatch(r'[a-zA-Z0-9_-]+', record['contributor']):
            raise ValueError('Invalid contributor name')
        manifest, invitation = record['manifest']['sha256'], record['invitation_id']
        name = record['contributor'].replace('_', '\\_')
        profile, proof = 'Not linked', '—'
        identity = identities.get((manifest, invitation))
        if identity:
            login = identity['github_login']
            profile = f"[@{login}](https://github.com/{login}) · ID {identity['github_id']}"
            proof = f'[Account proof](github/{manifest}/{invitation}.json)'
        row = f"| {name} | {record['total']}/41 | {profile} | [Receipts](records/{manifest}/{invitation}.json) | {proof} |"
        campaigns.setdefault(manifest, []).append((record['contributor'].lower(), invitation, row))
    text = '# Contributors\n\nCompleted contributions, with or without a GitHub account link. One row per\ninvitation; separate invitations remain separate contributions.\n\n'
    for manifest, rows in sorted(campaigns.items()):
        text += f'## Manifest `{manifest}`\n\n'
        text += '| Contributor | Circuits | GitHub | Contribution | Account link |\n| --- | --- | --- | --- | --- |\n'
        text += '\n'.join(row for _, _, row in sorted(rows)) + '\n\n'
    if not campaigns:
        text += 'No completed contributions have been published yet.\n\n'
    text += 'Receipts and account proofs are signed by the coordinator. GitHub linking is\noptional and proves account control, not a personal signature. Recorded profile\nnames can change; numeric account IDs identify the accounts.\n\n'
    text += '## Historical exports\n\n'
    text += 'Older exports are retained at their original URLs; they do not add contributions\nto the table above. [Historical attestation directories](../attestations/).\n'
    if (root / 'contributions/sergeytimoshin.json').exists():
        text += '\n[sergeytimoshin.json](sergeytimoshin.json) is the first manual receipt export,\nsuperseded by the `sergeytimoshin` record above.\n'
    destination = root / 'contributions/README.md'
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(text)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('--root', type=Path, default=Path('.'))
    args = parser.parse_args()
    publish(args.source, args.root)
