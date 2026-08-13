#!/usr/bin/env bash
# Build the monitor-api binary.
#
#   ./build.sh          → build for this machine
#   ./build.sh arm64    → cross-compile for a 64-bit ARM box (Pi, ARM VPS)
#
# There is no cgo in the dependency set, so cross-compiling needs no toolchain
# beyond Go itself.
set -euo pipefail

cd "$(dirname "$0")"

ARCH="${1:-$(go env GOARCH)}"
OUT="monitor-api"
[ "$ARCH" != "$(go env GOARCH)" ] && OUT="monitor-api-linux-$ARCH"

# -s -w drop the symbol table and DWARF info: ~30% smaller, and stack traces
# still carry function names and line numbers.
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
    go build -trimpath -ldflags="-s -w" -o "$OUT" .

echo "built $OUT ($(du -h "$OUT" | cut -f1), linux/$ARCH)"
