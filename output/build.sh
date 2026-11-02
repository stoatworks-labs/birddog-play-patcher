#!/usr/bin/env bash
# Build bdout and libbdout.so for the BirdDog PLAY (aarch64 Linux, Debian 10 /
# glibc 2.28), after vetting and testing both.
#
# bdout is pure Go, so it cross-compiles with CGO off. The shim is C and is
# built by zig with the target pinned to glibc 2.28 — without the pin, zig
# targets a newer glibc and PPApp would refuse to load it (and say so only in
# the journal, with HDMI none the wiser).
#
# The Go tests build shim/test/confcheck.c with this machine's cc and check
# every shim.conf bdout writes against the shim's own parser, so they need a C
# compiler as well. The shim's end-to-end test needs Linux: on a Mac,
#   CC="zig cc -target aarch64-linux-gnu.2.28" NO_RUN=1 shim/test/run-linux.sh OUT
# and run `shim/test/run-linux.sh --run OUT` on an aarch64 Linux box (a Debian
# 10 arm64 chroot is the closest thing to a PLAY).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

command -v zig >/dev/null || { echo "error: zig not found (brew install zig)" >&2; exit 1; }
command -v go  >/dev/null || { echo "error: go not found" >&2; exit 1; }
HOSTCC="$(command -v clang || command -v gcc || true)"
[ -n "$HOSTCC" ] || { echo "error: no host C compiler for the shim's tests" >&2; exit 1; }

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# The shim's pure half, on this machine.
TMP="$(mktemp -d -t bdout-build)"
trap 'rm -rf "$TMP"' EXIT
"$HOSTCC" -std=c11 -O2 -Wall -Wextra -Werror -o "$TMP/core_test" shim/test/core_test.c shim/core.c
"$TMP/core_test"

go vet ./...
go test ./...

# The tab is a plain script with no build step; check the RENDERED asset — the
# port substitution is what makes it parse.
if command -v node >/dev/null; then
  sed 's/__BDOUT_API_PORT__/8095/g' web/output.js > "$TMP/tab.js"
  node --check "$TMP/tab.js" || { echo "error: the tab script does not parse" >&2; exit 1; }
  echo "tab:     web/output.js parses"
else
  echo "note: node not found, skipping the tab syntax check" >&2
fi

mkdir -p dist
zig cc -target aarch64-linux-gnu.2.28 -std=gnu11 -O2 -s -Wall -Wextra -Werror -fPIC -shared -fvisibility=hidden \
  -o dist/libbdout.so shim/core.c shim/shim.c -ldl -lpthread
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o dist/bdout-linux-arm64 .

echo "built:   dist/bdout-linux-arm64, dist/libbdout.so"
echo "version: ${VERSION}"
file dist/bdout-linux-arm64 dist/libbdout.so
echo "size:    bdout $(du -h dist/bdout-linux-arm64 | cut -f1), shim $(du -h dist/libbdout.so | cut -f1)"
echo "glibc:   shim needs $(strings -a dist/libbdout.so | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tail -1) (device has 2.28)"
# The shim must export the four wrappers and nothing that would shadow
# another library: it is preloaded into BirdDogRunner's shell and everything
# that runs.
EXPORTS="$(zig objdump -T dist/libbdout.so 2>/dev/null || objdump -T dist/libbdout.so)"
for f in drmModeGetConnector drmModeAddFB2 drmModeRmFB drmModeSetPlane; do
  grep -q " $f\$" <<<"$EXPORTS" || { echo "error: libbdout.so does not export $f" >&2; exit 1; }
done
echo "exports: $(grep -cE '^[0-9a-f]+ g +DF \.text' <<<"$EXPORTS") functions (the four drmMode wrappers)"
