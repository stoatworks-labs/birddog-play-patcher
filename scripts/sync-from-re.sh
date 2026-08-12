#!/usr/bin/env bash
# Pull the installer and agent from the private research repo, which is canonical.
#
# `birddog-re` is where the installer is developed and where the reasoning behind
# every line of it lives (its notes/01 and notes/04 explain why this package
# format works at all). This repo carries a copy so it can be built and deployed
# on its own, but the copy is DOWNSTREAM — never edit installer/ or agent/*.go
# here and expect it to survive.
#
#   scripts/sync-from-re.sh [path-to-birddog-re]
#
# Prints a diff and exits 1 if anything changed, so CI or a pre-release check can
# catch a stale copy. Nothing here reaches into the research repo's firmware,
# notes or keys — only the files that are already shipped to users.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
RE="${1:-$HOME/Projects/birddog-re}"

[ -d "$RE/tools/fwbuild/payload" ] || {
  echo "error: no birddog-re checkout at $RE" >&2
  echo "usage: scripts/sync-from-re.sh [path-to-birddog-re]" >&2
  exit 2
}

changed=0
copy() { # src dst
  if ! cmp -s "$1" "$2" 2>/dev/null; then
    echo "updated: ${2#$REPO/}"
    diff -u "$2" "$1" 2>/dev/null | head -40 || true
    cp "$1" "$2"
    changed=1
  fi
}

for f in update probe.sh kvm-run.sh; do
  copy "$RE/tools/fwbuild/payload/$f" "$REPO/installer/$f"
done
for f in "$RE"/tools/bdkvm/*.go "$RE"/tools/bdkvm/go.mod "$RE"/tools/bdkvm/go.sum; do
  copy "$f" "$REPO/agent/$(basename "$f")"
done
[ -f "$RE/tools/bdkvm/dist/bdkvm-linux-arm64" ] &&
  copy "$RE/tools/bdkvm/dist/bdkvm-linux-arm64" "$REPO/agent/dist/bdkvm-linux-arm64"

chmod 755 "$REPO"/installer/* "$REPO"/agent/dist/* 2>/dev/null || true

if [ "$changed" = 1 ]; then
  echo
  echo "payload changed — re-run scripts/build-assets.sh and re-test before deploying"
  exit 1
fi
echo "in sync with $RE"
