#!/usr/bin/env bash
# libbdout.so end to end, on Linux: fakedrm.c built as libdrm.so.2, harness.c
# linked against it, and the shim preloaded in front, as it is in PPApp.
#
#   output/shim/test/run-linux.sh                build everything with cc, run (CI)
#   SHIM=dist/libbdout.so run-linux.sh           test that shim rather than a fresh one
#   CC="zig cc -target aarch64-linux-gnu.2.28" NO_RUN=1 run-linux.sh OUT
#                                                cross-build into OUT on a Mac...
#   run-linux.sh --run OUT                       ...and run it on an aarch64 box
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(cd "$HERE/.." && pwd)"

run() {
  local out="$1" shim="$2"
  echo "shim:    $shim"
  rm -f "$out/shim.conf" "$out/shim.status"
  LD_LIBRARY_PATH="$out" BDOUT_CONF="$out/shim.conf" BDOUT_STATUS="$out/shim.status" \
    LD_PRELOAD="$shim" "$out/harness"
  # BirdDogRunner's shell script and everything it runs get the preload too.
  # A program with no libdrm in it must not notice.
  local said
  said="$(LD_PRELOAD="$shim" /bin/sh -c 'echo ok' 2>&1)"
  [ "$said" = ok ] || { echo "FAIL: a shell with the shim preloaded said: $said" >&2; exit 1; }
  echo "preload: a shell with no libdrm runs clean"
}

if [ "${1:-}" = --run ]; then
  OUT="$(cd "$2" && pwd)"
  run "$OUT" "${SHIM:-$OUT/libbdout.so}"
  exit 0
fi

CC="${CC:-cc}"
OUT="${1:-$(mktemp -d -t bdout-shim.XXXXXX)}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

# shellcheck disable=SC2086 # CC may be "zig cc -target ..."
$CC -std=gnu11 -O2 -Wall -Wextra -Werror -fPIC -shared -Wl,-soname,libdrm.so.2 \
  -o "$OUT/libdrm.so.2" "$HERE/fakedrm.c"
ln -sf libdrm.so.2 "$OUT/libdrm.so"
# shellcheck disable=SC2086
$CC -std=gnu11 -O2 -Wall -Wextra -Werror -o "$OUT/harness" "$HERE/harness.c" -L"$OUT" -ldrm
if [ -z "${SHIM:-}" ]; then
  # shellcheck disable=SC2086
  $CC -std=gnu11 -O2 -Wall -Wextra -Werror -fPIC -shared -fvisibility=hidden \
    -o "$OUT/libbdout.so" "$SRC/core.c" "$SRC/shim.c" -ldl -lpthread
  SHIM="$OUT/libbdout.so"
else
  cp "$SHIM" "$OUT/libbdout.so"
  SHIM="$OUT/libbdout.so"
fi
echo "built:   $OUT"

[ "${NO_RUN:-0}" = 1 ] && exit 0
run "$OUT" "$SHIM"
