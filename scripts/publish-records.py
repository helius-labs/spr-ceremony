#!/usr/bin/env python3
"""Append verified workflow artifacts and render the optional GitHub directory.

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
    rows = []
    for path in sorted((root / 'contributions/github').glob('*/*.json')):
        identity = json.loads(path.read_text())
        manifest, invitation = identity['manifest']['sha256'], identity['invitation_id']
        record_path = root / 'contributions/records' / manifest / (invitation + '.json')
        record = json.loads(record_path.read_text())
        if identity['contributor'] != record['contributor'] or not re.fullmatch(r'[a-zA-Z0-9_-]+', record['contributor']):
            raise ValueError('Contribution identity mismatch')
        login = identity['github_login']
        if not re.fullmatch(r'[a-zA-Z0-9-]{1,39}', login):
            raise ValueError('Invalid profile name')
        name = record['contributor'].replace('_', '\\_')
        rows.append(f"| {name} | [@{login}](https://github.com/{login}) · ID {identity['github_id']} | [Receipts](../contributions/records/{manifest}/{invitation}.json) | [Account proof](../contributions/github/{manifest}/{invitation}.json) |")
    if rows:
        text = '# Contributions\n\nGitHub accounts linked by participants after completion. Account proofs are signed\nby the coordinator; they are not personal GitHub signatures. Profile names are\nrecorded at linking time; numeric account IDs identify the accounts.\n\n'
        text += '| Contributor | GitHub | Contribution | Account link |\n| --- | --- | --- | --- |\n' + '\n'.join(rows) + '\n\n'
        text += 'All accepted contributors, including those without GitHub linking: [public records](../contributions/records/).\n\nExisting numbered directories retain their original records. Numbers are per-circuit\nreceipt sequences, not a universal participant order.\n'
        destination = root / 'attestations/README.md'
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(text)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('--root', type=Path, default=Path('.'))
    args = parser.parse_args()
    publish(args.source, args.root)
