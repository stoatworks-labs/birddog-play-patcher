#!/usr/bin/env bash
# Cross-compile bdomt for the BirdDog PLAY (aarch64 Linux, Debian 10 / glibc 2.28).
#
# bdcam's toolchain story: purego needs cgo for dlopen on Linux, and macOS has no
# aarch64-linux gcc, so zig is the C compiler, with the target pinned to glibc
# 2.28 — without the pin, zig targets a newer glibc and the binary refuses to
# start on the device. libvmx is built the same way by vmx/build-libvmx.sh; run
# that first if dist/libvmx.so is missing or the patch changed.
#
# The tests run against this machine's libvmx (vmx/build-libvmx.sh host) and
# skip the decoding ones without it, so build that too before trusting a pass.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

command -v zig >/dev/null || { echo "error: zig not found (brew install zig)" >&2; exit 1; }
command -v go  >/dev/null || { echo "error: go not found" >&2; exit 1; }

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

go vet ./...
go test ./...

# The tab is a plain script with no build step; check the RENDERED asset, as
# the gateway's build does — the port substitution is what makes it parse.
if command -v node >/dev/null; then
  RENDERED="$(mktemp -t bdomt-tab).js"
  trap 'rm -f "$RENDERED"' EXIT
  sed 's/__BDOMT_API_PORT__/8094/g' web/omt.js > "$RENDERED"
  node --check "$RENDERED" || { echo "error: the tab script does not parse" >&2; exit 1; }
  echo "tab:     web/omt.js parses"
else
  echo "note: node not found, skipping the tab syntax check" >&2
fi

[ -f dist/libvmx.so ] || { echo "error: dist/libvmx.so missing — run vmx/build-libvmx.sh" >&2; exit 1; }

mkdir -p dist
export CGO_ENABLED=1 GOOS=linux GOARCH=arm64
export CC="zig cc -target aarch64-linux-gnu.2.28"
export CXX="zig c++ -target aarch64-linux-gnu.2.28"

go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o dist/bdomt-linux-arm64 .

echo "built:   dist/bdomt-linux-arm64"
echo "version: ${VERSION}"
file dist/bdomt-linux-arm64
echo "size:    $(du -h dist/bdomt-linux-arm64 | cut -f1)"
echo "glibc:   $(strings -a dist/bdomt-linux-arm64 | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tail -1) (device has 2.28)"
echo "libvmx:  dist/libvmx.so, $(du -h dist/libvmx.so | cut -f1)"
