#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
verifier_dir="$(cd "${1:?usage: scripts/test-solana.sh /path/to/groth16-solana /path/to/vectors}" && pwd)"
vectors_dir="$(cd "${2:?vector directory is required}" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
python3 - "$verifier_dir" "$tmp_dir/config.toml" <<'PY'
import json, pathlib, sys
pathlib.Path(sys.argv[2]).write_text('[patch.crates-io.groth16-solana]\npath = ' + json.dumps(sys.argv[1]) + '\n')
PY
cargo run --manifest-path "$repo_dir/adapters/solana/Cargo.toml" \
    --config "$tmp_dir/config.toml" -- "$vectors_dir"
