#!/usr/bin/env bash
# Assemble public/assets/ — everything the browser packs into a .fw.
#
# public/assets/ is generated and gitignored, so each file has exactly one
# committed copy: the installer scripts in installer/, the agent binary in
# agent/dist/. Run this before `wrangler dev` or `wrangler deploy`.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
OUT="$REPO/public/assets"

sha() { shasum -a 256 "$1" | cut -d' ' -f1; }

rm -rf "$OUT"
mkdir -p "$OUT"

for f in update probe.sh kvm-run.sh; do
  [ -f "$REPO/installer/$f" ] || { echo "error: missing installer/$f" >&2; exit 1; }
  cp "$REPO/installer/$f" "$OUT/$f"
done

BDKVM="$REPO/agent/dist/bdkvm-linux-arm64"
KVM_LINE='null'
if [ -f "$BDKVM" ]; then
  if file "$BDKVM" | grep -q aarch64; then
    cp "$BDKVM" "$OUT/bdkvm-linux-arm64"
    KVM_LINE="{\"size\": $(wc -c < "$BDKVM" | tr -d ' '), \"sha256\": \"$(sha "$BDKVM")\"}"
  else
    echo "warning: $BDKVM is not aarch64 — KVM asset omitted" >&2
  fi
else
  echo "warning: agent/dist/bdkvm-linux-arm64 missing (run agent/build.sh) —" \
       "the KVM option will be disabled in the UI" >&2
fi

cat > "$OUT/manifest.json" <<EOF
{
  "generated": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source": "installer/",
  "update": { "size": $(wc -c < "$OUT/update" | tr -d ' '),     "sha256": "$(sha "$OUT/update")" },
  "probe":  { "size": $(wc -c < "$OUT/probe.sh" | tr -d ' '),   "sha256": "$(sha "$OUT/probe.sh")" },
  "kvmRun": { "size": $(wc -c < "$OUT/kvm-run.sh" | tr -d ' '), "sha256": "$(sha "$OUT/kvm-run.sh")" },
  "bdkvm":  $KVM_LINE,
  "rootfsOverlay": []
}
EOF

echo "assets written to public/assets"
ls -la "$OUT"
