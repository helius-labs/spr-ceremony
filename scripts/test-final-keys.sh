#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
zolana_dir="$(cd "${1:?usage: scripts/test-zolana.sh /path/to/zolana}" && pwd)"
export_dir="$(cd "${2:?usage: scripts/test-final-keys.sh /path/to/zolana /path/to/final-keys /new/package-dir}" && pwd)"
package_dir="${3:?new package directory required}"
export SPR_CORE_ALL=1
if [[ -n "$(git -C "$zolana_dir" status --porcelain --untracked-files=no)" ]]; then
    echo 'Use a clean source checkout for compilation and witnesses.' >&2
    exit 1
fi
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/witnesses"

# Filter existing fixture exporters using Go's overlay support. The Zolana
# checkout is never edited, and excluded ring circuits are not generated.
python3 - "$zolana_dir" "$tmp_dir" <<'PY'
import json, pathlib, sys, os
root, out = map(pathlib.Path, sys.argv[1:])
base = root / 'prover/server/circuits'
changes = [
    (base / 'spp_transaction/shared/aeglos_fixture_test.go',
     'for _, shape := range protocol.SupportedShapes {',
     'for _, shape := range protocol.SupportedShapes {\nif shape.NInputs != 1 || shape.NOutputs != 2 { continue }'),
    (base / 'spp_merge/aeglos_fixture_test.go',
     'for _, inputs := range mergeshared.SupportedInputCounts {',
     'for _, inputs := range mergeshared.SupportedInputCounts {\nif inputs != 8 { continue }'),
]
replacements = {}
for i, (path, old, new) in enumerate(changes):
    text = path.read_text()
    if text.count(old) != 1:
        raise SystemExit(f'fixture exporter changed: {path}')
    if os.getenv('SPR_CORE_ALL') != '1':
        text = text.replace(old, new)
    if i == 0:
        old = 'for _, family := range []string{"transfer_ring", "transfer_confidential", "transfer_eddsa_ring", "transfer_ring_authority"} {'
        if text.count(old) != 1:
            raise SystemExit('transfer fixture families changed')
        text = text.replace(old, 'for _, family := range []string{"transfer_confidential"} {')
    else:
        text = text.replace('for _, ring := range []bool{false, true} {', 'for _, ring := range []bool{false} {')
    target = out / f'fixture-{i}_test.go'
    target.write_text(text)
    replacements[str(path)] = str(target)
(out / 'overlay.json').write_text(json.dumps({'Replace': replacements}))
PY

AEGLOS_FIXTURES="$tmp_dir/witnesses" go -C "$zolana_dir/prover/server" test \
    -overlay "$tmp_dir/overlay.json" \
    ./circuits/spp_transaction/shared ./circuits/spp_merge \
    -run '^TestExportAeglos(Transfer|Merge)Fixtures$' -count=1

cp "$repo_dir/adapters/zolana/go.mod" "$tmp_dir/go.mod"
cd "$repo_dir/adapters/zolana"
go mod edit -modfile "$tmp_dir/go.mod" \
    -replace "github.com/helius-labs/zolana/prover/server=$zolana_dir/prover/server" \
    -replace "github.com/helius-labs/spr-ceremony=$repo_dir"
go mod tidy -modfile "$tmp_dir/go.mod"
SPR_FINAL_EXPORT="$export_dir" SPR_PACKAGE="$package_dir" SPR_WITNESSES="$tmp_dir/witnesses" \
SPR_SOURCE_REVISION="$(git -C "$zolana_dir" rev-parse HEAD)" \
go test -modfile "$tmp_dir/go.mod" -run '^TestFinalizedCoreIntegration$' \
    -v -count=1 -parallel "${SPR_PARALLEL:-4}" -timeout 2h .
