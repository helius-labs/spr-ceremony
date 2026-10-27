#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_dir"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
    echo 'Release builds require a clean committed checkout.' >&2
    exit 1
fi
revision="$(git rev-parse HEAD)"
output="${1:-dist/$revision}"
if [[ -e "$output" ]]; then
    echo 'Release output must be new.' >&2
    exit 1
fi
mkdir -p "$output"
for os in linux darwin; do
    for arch in amd64 arm64; do
        target="spr-ceremony-$os-$arch"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=true \
            -o "$output/$target" ./cmd/spr-ceremony
    done
done
python3 - "$output" "$revision" <<'PY'
import hashlib, json, pathlib, subprocess, sys
out = pathlib.Path(sys.argv[1])
hashes = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(out.glob('spr-ceremony-*'))}
(out / 'SHA256SUMS').write_text(''.join(f'{digest}  {name}\n' for name, digest in hashes.items()))
(out / 'build.json').write_text(json.dumps({'source_revision': sys.argv[2], 'go': subprocess.check_output(['go', 'version'], text=True).strip(), 'sha256': hashes}, indent=2) + '\n')
PY
