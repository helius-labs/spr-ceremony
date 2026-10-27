#!/usr/bin/env python3
"""Stage Zolana's core key rotation and S3 publication plan; never publish it."""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

LOCK = Path('prover/server/prover/provingkeys/proving-keys.lock')
SDK = Path('sdk-libs/ts/src/interface/proving-keys.ts')
VKEYS = Path('program-libs/interface/src/verifying_keys')


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return {'sha256': h.hexdigest(), 'size': path.stat().st_size}


def check(path, ref):
    if digest(path) != ref:
        raise ValueError(f'artifact mismatch: {path}')


def update_lock(previous, replacements):
    result = copy.deepcopy(previous)
    for name, ref in replacements.items():
        if name not in result['keys'] or 'source' in result['keys'][name]:
            raise ValueError(f'core key missing or not distributed by S3: {name}')
        result['keys'][name] = ref
    canonical = json.dumps(
        {n: e['sha256'] for n, e in result['keys'].items() if 'source' not in e},
        sort_keys=True, separators=(',', ':'),
    ).encode()
    version = hashlib.sha256(canonical).hexdigest()[:16]
    result['prefix'] = previous['prefix'].rsplit('/', 1)[0] + '/' + version
    return result


def update_sdk(text, previous, replacements):
    for name, ref in replacements.items():
        pattern = re.compile(r'("' + re.escape(name) + r'"\s*:\s*")([0-9a-f]{64})(")')
        matches = list(pattern.finditer(text))
        if len(matches) != 1 or matches[0][2] != previous['keys'][name]['sha256']:
            raise ValueError(f'SDK/lock mismatch: {name}')
        text = pattern.sub(lambda m: m[1] + ref['sha256'] + m[3], text)
    return text


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def stage(args):
    package, source, out = args.package.resolve(), args.zolana.resolve(), args.out.resolve()
    if out.exists():
        raise ValueError('output directory must be new')
    if digest(package / 'package.json')['sha256'] != args.package_sha256:
        raise ValueError('package metadata digest mismatch')
    metadata = json.loads((package / 'package.json').read_text())
    revision = subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip()
    if revision != metadata['source_revision']:
        raise ValueError('Zolana source differs from packaged circuit source')
    if subprocess.check_output(['git', '-C', str(source), 'status', '--porcelain', '--untracked-files=no']):
        raise ValueError('Zolana checkout has tracked changes')
    catalog = json.loads(subprocess.check_output([str(args.ceremony_bin.resolve()), 'catalog', '--track', 'core']))
    names = {entry['id'] + '.key' for entry in catalog}
    if len(names) != 41 or set(metadata['keys']) != names:
        raise ValueError('package must contain exactly the 41 agreed core keys')
    raw_names = {name[:-4] + '.vkbin' for name in names}
    if set(metadata['raw_vks']) != raw_names:
        raise ValueError('raw verifier set differs from core catalog')
    check(package / 'ceremony-export.json', metadata['export'])
    exported = json.loads((package / 'ceremony-export.json').read_text())
    if exported['manifest_sha256'] != metadata['manifest']['sha256']:
        raise ValueError('package/export manifest mismatch')
    if exported['insecure_test_setup'] != args.insecure_test_setup:
        raise ValueError('rehearsal marker mismatch; production never accepts rehearsal keys')
    for name, ref in {**metadata['keys'], **metadata['raw_vks']}.items():
        check(package / name, ref)
    previous = json.loads((source / LOCK).read_text())
    updated = update_lock(previous, metadata['keys'])
    sdk = update_sdk((source / SDK).read_text(), previous, metadata['keys'])
    plan = {'source_revision': revision, 'package': digest(package / 'package.json'),
            'insecure_test_setup': args.insecure_test_setup,
            'old_prefix': previous['prefix'], 'new_prefix': updated['prefix'],
            'upload': [], 'copy_existing': []}
    for name, ref in updated['keys'].items():
        if 'source' in ref:
            continue
        destination = updated['prefix'] + '/' + name
        if name in metadata['keys']:
            plan['upload'].append({'source': str(package / name), 'destination': destination, **ref})
        else:
            plan['copy_existing'].append({'source': previous['prefix'] + '/' + name,
                                          'destination': destination, **ref,
                                          'multipart_required': ref['size'] > 5 * 1024**3})
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=out.parent, prefix='.core-release-') as temporary:
        tmp = Path(temporary)
        write_json(tmp / LOCK, updated)
        (tmp / SDK).parent.mkdir(parents=True, exist_ok=True)
        (tmp / SDK).write_text(sdk)
        command = [str(args.emitter.resolve()), str(package), str(tmp / VKEYS)]
        if args.insecure_test_setup:
            command.append('--insecure-test-setup')
        subprocess.run(command, check=True)
        if {p.stem + '.key' for p in (tmp / VKEYS).glob('*.rs')} != names:
            raise ValueError('generated verifier set differs from core catalog')
        write_json(tmp / 'publication.json', plan)
        shutil.copyfile(package / 'package.json', tmp / 'package.json')
        tmp.rename(out)
    print(f'Staged 41 core keys in {out}; publish every planned object before changing Zolana pins.')


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--package', type=Path, required=True)
    parser.add_argument('--package-sha256', required=True)
    parser.add_argument('--zolana', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--ceremony-bin', type=Path, default=root / 'spr-ceremony')
    parser.add_argument('--emitter', type=Path, default=root / 'adapters/solana/target/debug/spr-emit-vkeys')
    parser.add_argument('--insecure-test-setup', action='store_true')
    stage(parser.parse_args())


if __name__ == '__main__':
    main()
