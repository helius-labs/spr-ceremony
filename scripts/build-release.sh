#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_dir"
if [[ -n "$(git status --porcelain)" ]]; then
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
python3 scripts/package-release.py "$output" "$revision" "${SPR_RELEASE_VERSION:-}"
