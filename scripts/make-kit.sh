#!/usr/bin/env bash
# Build the release kit: birddog-play-patcher-kit-<version>.zip.
#
# The page serves its installer scripts and module binaries from its own
# origin, so nothing outside a browser can fetch them. The kit is those files
# as one release asset, plus kit.json (where each goes in a .fw, which option
# needs it, the upstream pins) and the licence notices. Burrow builds packages
# from it. Tailscale and MediaMTX are never in it: whoever builds a package
# fetches them, as the page does.
#
#   scripts/make-kit.sh [outdir]     # default kit/out, which is gitignored
#
# Every zip entry is deflated, never stored: Burrow's zip reader has deflate
# and nothing else. Entry times are fixed, so the same tree gives the same
# entries. test/kit.mjs runs this and checks the result.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
OUT="${1:-$REPO/kit/out}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

# Its listing is noise here; warnings about missing binaries go to stderr, and
# kit.mjs refuses to build a kit without every one of them.
"$HERE/build-assets.sh" > /dev/null

VERSION="$(node -p "require('$REPO/package.json').version")"
NAME="birddog-play-patcher-kit-$VERSION"
STAGE="$OUT/$NAME"
ZIP="$OUT/$NAME.zip"

rm -rf "$STAGE" "$ZIP"
mkdir -p "$STAGE"
node "$HERE/kit.mjs" "$STAGE"

python3 -I - "$STAGE" "$ZIP" <<'PY'
import os, sys, zipfile

stage, out = sys.argv[1], sys.argv[2]
paths = []
for root, _, names in os.walk(stage):
    for n in names:
        paths.append(os.path.relpath(os.path.join(root, n), stage).replace(os.sep, '/'))
# kit.json first, so a reader that only wants the manifest finds it at once.
paths.sort(key=lambda p: (p != 'kit.json', p))

with zipfile.ZipFile(out, 'w') as z:
    for p in paths:
        full = os.path.join(stage, p)
        info = zipfile.ZipInfo(p, date_time=(2020, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_DEFLATED
        info.create_system = 3  # unix, so the mode below means something
        info.external_attr = (0o100000 | (os.stat(full).st_mode & 0o777)) << 16
        with open(full, 'rb') as f:
            z.writestr(info, f.read(), compresslevel=9)
PY

echo "$ZIP"
echo "  $(wc -c < "$ZIP" | tr -d ' ') bytes, sha256 $(shasum -a 256 "$ZIP" | cut -d' ' -f1)"
