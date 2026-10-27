#!/usr/bin/env python3
"""Verify staged pins and execute all 41 proof vectors with the emitted Rust keys.

This uses a temporary host-only crate. It never edits Zolana, publishes keys, or
enables rehearsal keys in a deployable program.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('release', ROOT / 'scripts/stage-core-release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)

RUST = r'''
use base64::{engine::general_purpose::STANDARD, Engine};
use groth16_solana::{groth16::{negate_g1_be, Groth16Verifier, Groth16Verifyingkey},
                    vk::gnark::parse_gnark_vk_bytes};
use serde_json::Value;
fn point<const N: usize>(v: &Value) -> [u8; N] {
    STANDARD.decode(v.as_str().unwrap()).unwrap().try_into().unwrap()
}
fn verify(v: &Value, public: &[[u8; 32]; 1], vk: &Groth16Verifyingkey) -> bool {
    let a = negate_g1_be(&point::<64>(&v["a"]));
    let b = point::<128>(&v["b"]);
    let c = point::<64>(&v["c"]);
    if v.get("commitment").is_some() {
        let commitment = point::<64>(&v["commitment"]);
        let pok = point::<64>(&v["pok"]);
        Groth16Verifier::new_with_commitment(&a, &b, &c, &commitment, &pok, public, vk)
            .unwrap().verify().is_ok()
    } else {
        Groth16Verifier::new(&a, &b, &c, public, vk).unwrap().verify().is_ok()
    }
}
fn check(id: &str, vk: &Groth16Verifyingkey, digest: [u8; 32], rehearsal: bool,
         expected_digest: [u8; 32], expected_rehearsal: bool) {
    assert_eq!(digest, expected_digest, "generated proving key pin: {id}");
    assert_eq!(rehearsal, expected_rehearsal, "generated setup marker: {id}");
    let dir = std::env::args().nth(1).unwrap();
    let v: Value = serde_json::from_slice(&std::fs::read(
        std::path::Path::new(&dir).join(format!("{id}.json"))).unwrap()).unwrap();
    assert_eq!(v["id"].as_str(), Some(id));
    let raw = STANDARD.decode(v["vk"].as_str().unwrap()).unwrap();
    let parsed = parse_gnark_vk_bytes(&raw).unwrap();
    assert_eq!(*vk, parsed.as_borrowed(), "generated verifier: {id}");
    let mut public: [[u8; 32]; 1] = serde_json::from_value(v["public"].clone()).unwrap();
    assert!(verify(&v, &public, vk), "valid proof rejected: {id}");
    public[0][31] ^= 1;
    assert!(!verify(&v, &public, vk), "changed public input accepted: {id}");
    println!("verified staged {id}: key pin, setup marker, proof, and invalid public input");
}
'''


def verify(args):
    package, staged, source = args.package.resolve(), args.staged.resolve(), args.zolana.resolve()
    metadata = json.loads((package / 'package.json').read_text())
    plan = json.loads((staged / 'publication.json').read_text())
    release.check(package / 'package.json', plan['package'])
    release.check(staged / 'package.json', plan['package'])
    if subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip() != metadata['source_revision']:
        raise ValueError('source revision mismatch')
    if subprocess.check_output(['git', '-C', str(source), 'status', '--porcelain', '--untracked-files=no']):
        raise ValueError('Zolana checkout has tracked changes')
    catalog = json.loads(subprocess.check_output([str(args.ceremony_bin.resolve()), 'catalog', '--track', 'core']))
    names = {s['id'] + '.key' for s in catalog}
    if len(names) != 41 or set(metadata['keys']) != names:
        raise ValueError('expected exactly 41 core keys')
    if set(metadata['raw_vks']) != {n[:-4] + '.vkbin' for n in names}:
        raise ValueError('raw verifier set differs from core catalog')
    if {p.stem + '.key' for p in (staged / release.VKEYS).glob('*.rs')} != names:
        raise ValueError('generated verifier set differs from core catalog')
    release.check(package / 'ceremony-export.json', metadata['export'])
    exported = json.loads((package / 'ceremony-export.json').read_text())
    if exported['manifest_sha256'] != metadata['manifest']['sha256'] or exported['insecure_test_setup'] != plan['insecure_test_setup']:
        raise ValueError('export provenance or setup marker mismatch')
    for name, ref in {**metadata['keys'], **metadata['raw_vks']}.items():
        release.check(package / name, ref)
    previous = json.loads((source / release.LOCK).read_text())
    actual = json.loads((staged / release.LOCK).read_text())
    expected = release.update_lock(previous, metadata['keys'])
    if actual != expected:
        raise ValueError('staged lock differs from core replacement; excluded entries must be preserved')
    if (staged / release.SDK).read_text() != release.update_sdk((source / release.SDK).read_text(), previous, metadata['keys']):
        raise ValueError('staged SDK mapping mismatch')
    if plan['source_revision'] != metadata['source_revision'] or plan['old_prefix'] != previous['prefix'] or plan['new_prefix'] != actual['prefix']:
        raise ValueError('publication source or prefix mismatch')
    expected_upload, expected_copy = [], []
    for name, ref in actual['keys'].items():
        if 'source' in ref:
            continue
        destination = actual['prefix'] + '/' + name
        if name in names:
            expected_upload.append({'source': str(package / name), 'destination': destination, **ref})
        else:
            expected_copy.append({'source': previous['prefix'] + '/' + name,
                                  'destination': destination, **ref,
                                  'multipart_required': ref['size'] > 5 * 1024**3})
    order = lambda entries: sorted(entries, key=lambda e: e['destination'])
    if order(plan['upload']) != order(expected_upload) or order(plan['copy_existing']) != order(expected_copy):
        raise ValueError('publication plan omitted or changed an object')
    modules, calls = [], []
    for name in sorted(names):
        circuit = name[:-4]
        if not re.fullmatch(r'[a-z0-9_]+', circuit):
            raise ValueError('invalid circuit ID')
        module = staged / release.VKEYS / (circuit + '.rs')
        modules.append(f'#[path = {json.dumps(str(module), ensure_ascii=False)}] mod {circuit};')
        digest = list(bytes.fromhex(metadata['keys'][name]['sha256']))
        calls.append(f'check("{circuit}", &{circuit}::VERIFYINGKEY, '
                     f'{circuit}::VERIFYINGKEY_PROVING_KEY_SHA256, '
                     f'{circuit}::VERIFYINGKEY_INSECURE_TEST_SETUP, '
                     f'{digest}, {str(plan["insecure_test_setup"]).lower()});')
    with tempfile.TemporaryDirectory(prefix='spr-staged-host-check-') as tmp:
        crate = Path(tmp)
        (crate / 'src').mkdir()
        manifest = (ROOT / 'adapters/solana/Cargo.toml').read_text()
        (crate / 'Cargo.toml').write_text(manifest + '\n[features]\ninsecure-test-setup = []\n')
        shutil.copyfile(ROOT / 'adapters/solana/Cargo.lock', crate / 'Cargo.lock')
        (crate / 'src/main.rs').write_text('\n'.join(modules) + RUST + '\nfn main() {\n' + '\n'.join(calls) + '\n}\n')
        env = {**os.environ, 'CARGO_TARGET_DIR': str(ROOT / 'adapters/solana/target')}
        command = ['cargo', 'run', '--locked', '--manifest-path', str(crate / 'Cargo.toml')]
        if plan['insecure_test_setup']:
            command += ['--features', 'insecure-test-setup']
        subprocess.run(command + ['--', str(args.vectors.resolve())], env=env, check=True)
    print(f'Verified 41 emitted verifiers, {len(expected_upload)} uploads, '
          f'{len(expected_copy)} unchanged copies, and all excluded pins.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('package', 'staged', 'zolana', 'vectors'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--ceremony-bin', type=Path, default=ROOT / 'spr-ceremony')
    verify(parser.parse_args())
