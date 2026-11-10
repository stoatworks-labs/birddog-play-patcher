#!/usr/bin/env bash
# Build the module archive the PLAY patcher takes: dist/stagetimer-<VERSION>.tgz
#
# bdstage is Go with no cgo, so it cross-compiles to a static aarch64 binary on
# any machine; the fonts and the tab script are embedded in it. The archive is
# plain ustar owned by root, exactly what the patcher writes into the .fw.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

NAME="$(sed -n 's/^NAME=//p' module.conf | head -1 | tr -d "\"'")"
VERSION="$(sed -n 's/^VERSION=//p' module.conf | head -1 | tr -d "\"'")"
GITV="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

if gofmt -l . | grep .; then echo "error: gofmt" >&2; exit 1; fi
go vet ./...
GOOS=linux GOARCH=arm64 go vet ./...
go test ./...

# The tab has no build step; nothing else would catch a syntax error until a
# browser rendered an empty tab.
T="$(mktemp -d)"
STAGE="$(mktemp -d)"; chmod 755 "$STAGE"
trap 'rm -rf "$T" "$STAGE"' EXIT
if command -v node >/dev/null; then
  sed 's/__BDSTAGE_API_PORT__/8096/g' web/stagetimer.js > "$T/tab.js"
  node --check "$T/tab.js"
  echo "tab:     web/stagetimer.js parses"
else
  echo "note: node not found, skipping the tab syntax check" >&2
fi

mkdir -p payload/bin
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=${VERSION}+${GITV}" -o payload/bin/bdstage .

./check.sh

OUT="dist/${NAME}-${VERSION}.tgz"
cp module.conf install uninstall "$STAGE/"
cp -R payload "$STAGE/payload"
find "$STAGE/payload" \( -name .DS_Store -o -name '._*' -o -name .gitkeep \) -delete
chmod 755 "$STAGE/install" "$STAGE/uninstall" "$STAGE/payload/run.sh" "$STAGE/payload/bin/bdstage"

mkdir -p dist
if tar --version 2>/dev/null | grep -q GNU; then
  ( cd "$STAGE" && tar --format=ustar --owner=0 --group=0 --numeric-owner \
      --sort=name --mtime='2020-01-01 00:00:00Z' -czf "$OLDPWD/$OUT" . )
else
  ( cd "$STAGE" && COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 \
      --uname root --gname root -czf "$OLDPWD/$OUT" . )
fi

echo
echo "built:   $OUT ($(du -h "$OUT" | cut -f1))"
echo "sha256:  $(shasum -a 256 "$OUT" | cut -d' ' -f1)"
tar tzvf "$OUT" | sed 's/^/  /'
echo
echo "Next: choose this file under 'Custom modules' at"
echo "  https://birddog-play-patcher.stoatworks-labs.com/"
