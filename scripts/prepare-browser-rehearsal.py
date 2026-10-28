#!/usr/bin/env python3
"""Create a separate, signed test campaign from untouched prepared core parameters.

Only public immutable objects are reused. The source campaign is never changed;
the new manifest is explicitly marked insecure and receives a new signing key.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def reference(path):
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
    return {'sha256': digest, 'size': path.stat().st_size}


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--from-prepared', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--prepared', action='store_true', help='leave the rehearsal prepared so an operator can announce a beacon and open it')
    args = parser.parse_args()
    os.umask(0o077)
    binary = str(Path(args.binary).resolve())
    source, out = Path(args.from_prepared).resolve(), Path(args.out).resolve()
    if out.exists():
        raise ValueError('Output must be a new directory')
    manifest = json.loads((source / 'manifest.json').read_text())
    manifest_ref = reference(source / 'manifest.json')
    checkpoint_ref = json.loads((source / 'checkpoint.json').read_text())

    def checked(ref):
        digest = ref['sha256']
        if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
            raise ValueError('Invalid public object digest')
        path = source / 'objects' / digest
        if reference(path) != ref:
            raise ValueError('Source object hash mismatch')
        return path

    checkpoint = json.loads(checked(checkpoint_ref).read_text())
    if (checkpoint['manifest'] != manifest_ref or checkpoint['sequence'] != 0
            or checkpoint.get('previous') or checkpoint.get('exports')
            or checkpoint['lifecycle'] != {'phase': 'prepared'}):
        raise ValueError('Requires untouched prepared parameters')
    catalog = json.loads(subprocess.run([binary, 'catalog'], check=True, capture_output=True).stdout)
    if sorted((c['spec'] for c in manifest['circuits']), key=lambda s: s['id']) != sorted(catalog, key=lambda s: s['id']):
        raise ValueError('Requires the complete fixed core catalog')
    refs = {manifest['phase1']['artifact']['sha256']: manifest['phase1']['artifact']}
    for circuit in manifest['circuits']:
        state_ref = checkpoint['states'][circuit['spec']['id']]
        state = json.loads(checked(state_ref).read_text())
        if state != {'circuit': circuit['spec']['id'], 'head': circuit['initial'], 'history': []}:
            raise ValueError('Source already has contributions')
        for ref in [state_ref, circuit['r1cs'], circuit['srs'], circuit['initial']]:
            refs[ref['sha256']] = ref

    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.browser-rehearsal-', dir=out.parent) as temporary:
        root = Path(temporary)
        unsigned = root / 'unsigned'
        objects = unsigned / 'objects'
        objects.mkdir(parents=True)
        for ref in refs.values():
            path = checked(ref)
            target = objects / ref['sha256']
            try:
                os.link(path, target)
            except OSError:
                shutil.copyfile(path, target)
        manifest.pop('coordinator_key', None)
        manifest['phase1']['insecure_test_setup'] = True
        manifest['phase1']['provenance'] = 'Browser rehearsal only; public parameters copied from prepared manifest ' + manifest_ref['sha256']
        write_json(unsigned / 'manifest.json', manifest)
        checkpoint['manifest'] = reference(unsigned / 'manifest.json')
        shutil.copyfile(unsigned / 'manifest.json', objects / checkpoint['manifest']['sha256'])
        checkpoint['lifecycle'] = {'phase': 'prepared' if args.prepared else 'open'}
        write_json(root / 'checkpoint.json', checkpoint)
        ref = reference(root / 'checkpoint.json')
        shutil.copyfile(root / 'checkpoint.json', objects / ref['sha256'])
        write_json(unsigned / 'checkpoint.json', ref)
        private, public = root / 'coordinator.private.asc', root / 'coordinator.asc'
        subprocess.run([binary, 'keygen', '--out', private, '--public-out', public], check=True, capture_output=True)
        subprocess.run([binary, 'bind-key', '--dir', unsigned, '--public-key', public, '--out', out], check=True, capture_output=True)
        (out / 'private').mkdir(mode=0o700)
        os.rename(private, out / 'private/coordinator.private.asc')
        subprocess.run([binary, 'check-signing-key', '--dir', out], check=True, capture_output=True)
    if reference(source / 'manifest.json') != manifest_ref or json.loads((source / 'checkpoint.json').read_text()) != checkpoint_ref:
        raise ValueError('Prepared source changed during rehearsal creation')
    print(json.dumps({'rehearsal_only': True, 'circuits': len(manifest['circuits']),
                      'source_manifest': manifest_ref, 'manifest': reference(out / 'manifest.json')}))


if __name__ == '__main__':
    main()
