#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
zolana_dir="$(cd "${1:?usage: scripts/build-zolana.sh /path/to/zolana}" && pwd)"
revision="$(git -C "$zolana_dir" rev-parse HEAD)"
dirty=false
if [[ -n "$(git -C "$zolana_dir" status --porcelain --untracked-files=no)" ]] ||
   [[ -n "$(git -C "$zolana_dir" status --porcelain --untracked-files=all -- prover/server)" ]]; then
    echo 'Zolana checkout has tracked changes or untracked prover files; use clean circuit source.' >&2
    exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
cp "$repo_dir/adapters/zolana/go.mod" "$tmp_dir/go.mod"
cd "$repo_dir/adapters/zolana"
go mod edit -modfile "$tmp_dir/go.mod" \
    -replace "github.com/helius-labs/zolana/prover/server=$zolana_dir/prover/server"
go mod tidy -modfile "$tmp_dir/go.mod"
gnark_version="$(go list -modfile "$tmp_dir/go.mod" -m -f '{{.Version}}' github.com/consensys/gnark)"
crypto_version="$(go list -modfile "$tmp_dir/go.mod" -m -f '{{.Version}}' github.com/consensys/gnark-crypto)"
if [[ "$gnark_version" != v0.16.4 ]] || [[ "$crypto_version" != v0.21.1-0.20260913224755-1409fe592052 ]]; then
    echo 'Zolana dependencies differ from the ceremony engine; align and validate them before building.' >&2
    exit 1
fi
go build -modfile "$tmp_dir/go.mod" \
    -ldflags "-X main.zolanaRevision=$revision -X main.zolanaDirty=$dirty" \
    -o "$repo_dir/spr-zolana" .
echo "Built spr-zolana against $revision"
