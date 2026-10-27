#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
verifier_dir="$(cd "${1:?usage: scripts/test-solana.sh /path/to/groth16-solana /path/to/vectors}" && pwd)"
vectors_dir="$(cd "${2:?vector directory is required}" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
expected_revision=70a93803e5f55b8425301d506c625169ef557c9d
if [[ "$(git -C "$verifier_dir" rev-parse HEAD)" != "$expected_revision" ]] ||
   [[ -n "$(git -C "$verifier_dir" status --porcelain --untracked-files=no)" ]]; then
    echo "Verifier checkout must be clean at Zolana's pinned revision $expected_revision" >&2
    exit 1
fi
args=("$vectors_dir")
if [[ "${SPR_CORE_ALL:-}" == 1 ]]; then
    (cd "$repo_dir" && go run ./cmd/spr-ceremony catalog --track core) > "$tmp_dir/catalog.json"
    args+=("$tmp_dir/catalog.json")
fi
cargo run --manifest-path "$repo_dir/adapters/solana/Cargo.toml" \
    --locked --bin spr-solana-rehearsal -- "${args[@]}"
