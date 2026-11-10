#!/usr/bin/env bash
# Cross-compile bdstage for the BirdDog PLAY: dist/bdstage-linux-arm64, the
# binary the patcher commits and serves.
#
# bdstage is Go with no cgo, so it builds to a static aarch64 binary on any
# machine and needs no glibc pin; the fonts and the tab script are embedded in
# it. The fonts' licence is not: the patcher packs fonts/OFL.txt beside the
# binary as /userdata/bd-stage/OFL.txt.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }

# The version the tab and the log show: this folder's own number, plus the
# commit it was built from. -buildvcs=false keeps git's state out of the
# binary, so the same source and version build the same bytes.
VERSION="${VERSION:-0.1.0+$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"

if gofmt -l . | grep .; then echo "error: gofmt" >&2; exit 1; fi
go vet ./...
GOOS=linux GOARCH=arm64 go vet ./...
go test ./...

# The tab has no build step; check the RENDERED script, as the other folders
# do — the port substitution is what makes it parse.
if command -v node >/dev/null; then
  RENDERED="$(mktemp -t bdstage-tab).js"
  trap 'rm -f "$RENDERED"' EXIT
  sed 's/__BDSTAGE_API_PORT__/8096/g' web/stagetimer.js > "$RENDERED"
  node --check "$RENDERED" || { echo "error: the tab script does not parse" >&2; exit 1; }
  echo "tab:     web/stagetimer.js parses"
else
  echo "note: node not found, skipping the tab syntax check" >&2
fi

mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=${VERSION}" \
  -o dist/bdstage-linux-arm64 .

echo "built:   dist/bdstage-linux-arm64"
echo "version: ${VERSION}"
file dist/bdstage-linux-arm64
echo "size:    $(du -h dist/bdstage-linux-arm64 | cut -f1)"
