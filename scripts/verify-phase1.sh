#!/usr/bin/env bash
# Verify and convert the public power-21 transcript. No ceremony is opened here.
set -euo pipefail
repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
ptau="$(realpath "${1:?usage: verify-phase1.sh public.ptau /path/to/semaphore-mtb-setup new-output}")"
converter_dir="$(cd "${2:?converter checkout required}" && pwd)"
out="${3:?new output directory required}"
expected_revision=577c91f3f8b8e6bccf9d2fbc629c9b18f5c56cad
expected_blake2b=9aef0573cef4ded9c4a75f148709056bf989f80dad96876aadeb6f1c6d062391f07a394a9e756d16f7eb233198d5b69407cca44594c763ab4a5b67ae73254678
if [[ "$(git -C "$converter_dir" rev-parse HEAD)" != "$expected_revision" ]] ||
   [[ -n "$(git -C "$converter_dir" status --porcelain --untracked-files=no)" ]]; then
    echo "Converter must be clean at $expected_revision" >&2
    exit 1
fi
if [[ -e "$out" ]]; then echo 'Output must be new.' >&2; exit 1; fi
mkdir -p "$out"
out="$(cd "$out" && pwd)"
python3 - "$ptau" "$expected_blake2b" <<'PY'
import hashlib, sys
h = hashlib.blake2b()
with open(sys.argv[1], 'rb') as f:
    for b in iter(lambda: f.read(1024 * 1024), b''): h.update(b)
if h.hexdigest() != sys.argv[2]: raise SystemExit('Published power-21 BLAKE2b mismatch')
PY
mkdir "$out/tools"
npm install --prefix "$out/tools" --ignore-scripts --no-audit --no-fund snarkjs@0.7.6 > "$out/npm.log" 2>&1
"$out/tools/node_modules/.bin/snarkjs" powersoftau verify "$ptau" > "$out/verification.log" 2>&1
(cd "$converter_dir" && go build -mod=readonly -o "$out/converter" .)
"$out/converter" p1i "$ptau" "$out/public-21.ph1" > "$out/conversion.log" 2>&1
ph1_sha="$(python3 - "$out" "$ptau" "$expected_revision" "$expected_blake2b" <<'PY'
import hashlib, json, pathlib, sys
out, ptau = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
def digest(p):
    h = hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda: f.read(1024 * 1024), b''): h.update(b)
    return {'sha256': h.hexdigest(), 'size': p.stat().st_size}
record = {'source': 'https://circom.info/powersOfTau28_hez_final_21.ptau',
          'hash_source': 'https://github.com/iden3/snarkjs/blob/master/README.md',
          'published_blake2b': sys.argv[4], 'ptau': digest(ptau),
          'snarkjs': '0.7.6', 'npm_lock': digest(out / 'tools/package-lock.json'),
          'verification_log': digest(out / 'verification.log'),
          'converter_revision': sys.argv[3], 'converter_binary': digest(out / 'converter'),
          'conversion_log': digest(out / 'conversion.log'), 'ph1': digest(out / 'public-21.ph1')}
(out / 'provenance.json').write_text(json.dumps(record, indent=2) + '\n')
print(record['ph1']['sha256'])
PY
)"
provenance_sha="$(sha256sum "$out/provenance.json" | cut -d' ' -f1)"
"$repo_dir/spr-ceremony" import-ph1 --input "$out/public-21.ph1" --sha256 "$ph1_sha" \
    --provenance "Verified power-21 transcript; provenance.json SHA256 $provenance_sha" \
    --max-domain 2097152 --out "$out/public-21.srs"
echo "Verified Phase 1 artifacts and provenance: $out"
