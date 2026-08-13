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

# --------------------------------------------------------------- bdplay
# The USB media player, plus its two optional helpers. Sizes matter here:
# Cloudflare Workers static assets cap at 25 MiB per file, which is why PDF is
# PDFium (7.5 MB, BSD-3) and not MuPDF (37 MB, AGPL v3).
stage_play() { # varname filename source-path label
  local var="$1" name="$2" src="$3" label="$4"
  if [ -f "$src" ]; then
    if file "$src" | grep -q aarch64; then
      cp "$src" "$OUT/$name"
      printf -v "$var" '{"size": %s, "sha256": "%s"}' \
        "$(wc -c < "$src" | tr -d ' ')" "$(sha "$src")"
      return
    fi
    echo "warning: $src is not aarch64 — $label omitted" >&2
  fi
  printf -v "$var" 'null'
}

BDTS_LINE=null
PLAY_LINE=null PDF_LINE=null PDFLIB_LINE=null EXFAT_LINE=null
stage_play PLAY_LINE   bdplay-linux-arm64      "$REPO/player/dist/bdplay-linux-arm64"           "USB media player"
stage_play PDF_LINE    bdpdf-linux-arm64       "$REPO/player/dist/bdpdf-linux-arm64"            "PDF renderer"
stage_play PDFLIB_LINE libpdfium.so            "$REPO/player/dist/libpdfium.so"                 "PDFium"
stage_play EXFAT_LINE  mount.exfat-fuse        "$REPO/player/dist/mount.exfat-fuse-linux-arm64" "exFAT helper"

# --------------------------------------------------------------- bdts
# The birdUI Tailscale panel. Ships with the Tailscale payload rather than as
# its own option: without it a device installs Tailscale and then still needs
# SSH to sign in, which is the gap it closes.
stage_play BDTS_LINE bdts-linux-arm64 "$REPO/tailscale-ui/dist/bdts-linux-arm64" "Tailscale panel"
[ "$BDTS_LINE" = null ] && echo "warning: tailscale-ui/dist/bdts-linux-arm64 missing" \
  "(run bdts's build.sh, then scripts/sync-from-re.sh) — packages will install" \
  "Tailscale WITHOUT the birdUI panel, so signing in will need SSH" >&2

[ "$PLAY_LINE" = null ] && echo "warning: player/dist/bdplay-linux-arm64 missing" \
  "(run bdplay's build.sh, then scripts/sync-from-re.sh) — the USB media player" \
  "option will be disabled in the UI" >&2

# Belt and braces against the one mistake that would matter: an AGPL binary
# reaching a public CDN.
if [ -e "$OUT/mutool" ] || [ -e "$REPO/player/dist/mutool-linux-arm64" ]; then
  echo "error: mutool (AGPL v3) must never be staged as a web asset" >&2
  exit 1
fi

cat > "$OUT/manifest.json" <<EOF
{
  "generated": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source": "installer/",
  "update": { "size": $(wc -c < "$OUT/update" | tr -d ' '),     "sha256": "$(sha "$OUT/update")" },
  "probe":  { "size": $(wc -c < "$OUT/probe.sh" | tr -d ' '),   "sha256": "$(sha "$OUT/probe.sh")" },
  "kvmRun": { "size": $(wc -c < "$OUT/kvm-run.sh" | tr -d ' '), "sha256": "$(sha "$OUT/kvm-run.sh")" },
  "bdkvm":  $KVM_LINE,
  "bdts":   $BDTS_LINE,
  "bdplay": $PLAY_LINE,
  "bdpdf":  $PDF_LINE,
  "pdfium": $PDFLIB_LINE,
  "exfat":  $EXFAT_LINE,
  "rootfsOverlay": []
}
EOF

echo "assets written to public/assets"
ls -la "$OUT"
