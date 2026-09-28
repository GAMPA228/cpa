#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")" && pwd)"
out="${1:?Pass an output directory}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cd "$root/go"
export CGO_ENABLED=1 GOOS=linux GOARCH=amd64
go test -race -buildvcs=false ./...
go build -buildvcs=false -trimpath -buildmode=c-shared -ldflags='-s -w' -o "$out/codex-timezone.so" .
python3 "$root/smoke.py" "$out/codex-timezone.so"
cp "$root/README.md" "$out/README.md"
cp "$root/README_CN.md" "$out/README_CN.md"
{
  go version -m "$out/codex-timezone.so"
  readelf -h "$out/codex-timezone.so"
  readelf --version-info "$out/codex-timezone.so" | grep 'Name: GLIBC'
} > "$out/BUILD-INFO.txt"
cd "$out"
sha256sum codex-timezone.so README.md README_CN.md BUILD-INFO.txt > SHA256SUMS
