#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$repo_dir/dist/browser}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cd "$repo_dir"
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -o "$out/contributor.wasm" ./cmd/spr-browser-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/wasm_exec.js"
cp web/index.html web/style.css web/app.js web/worker.js web/network.js "$out/"
go run -buildvcs=true ./cmd/spr-browser-rehearsal build-info --out "$out"
echo "Built browser prototype: $out"
