#!/usr/bin/env bash
# Run bdstage's real video pipeline inside the PLAY's own factory rootfs and
# check what comes out of the far end of GStreamer.
#
#   sudo tools/verify-stock.sh ROOTFS_IMG BDSTAGE MOCKSTAGE [OUT_DIR]
#
# ROOTFS_IMG is the rootfs partition of BirdDog's factory image (PLAY_1.0.30.img,
# split with birddog-re's tools/rkfw/unpack_rkfw.py --all). It is BirdDog's, so
# it is never in this repo; it is mounted read-only under an overlay and never
# written. BDSTAGE and MOCKSTAGE are linux/arm64 builds of . and tools/mockstage.
# Needs an aarch64 Linux host (the tart VM wlos-build is the one used).
#
# bdstage runs exactly as on a PLAY — fdsrc, rawvideoparse as NV12, the queue —
# except that kmssink is swapped (--gst-video-sink) for videoconvert and a PNG
# per frame. So this proves the frames bdstage writes are what the PLAY's
# GStreamer 1.14 parses as 1080p NV12, and that its BT.709 conversion reads our
# colours back. It cannot prove anything about kmssink on the RK3328's VOP, the
# PPApp handover or the birdUI gate: those need a PLAY.
set -euo pipefail
IMG="${1:?rootfs image}"; BD="${2:?bdstage binary}"; MOCK="${3:?mockstage binary}"
OUT="${4:-$PWD/verify-out}"
W="$(mktemp -d /tmp/bdstage-verify.XXXXXX)"

die() { echo "verify: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || die "run as root"

MOCK_PID=
cleanup() {
  pkill -f 'bin/bdstage --serve 127.0.0.1:8096' 2>/dev/null || true
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
  sleep 1
  for d in "$W/root/dev/pts" "$W/root/dev" "$W/root/sys" "$W/root/proc" "$W/root" "$W/ro"; do
    mountpoint -q "$d" && umount "$d" || true
  done
}
trap 'cleanup; rm -rf "$W"' EXIT
mkdir -p "$W"/{ro,upper,work,root} "$OUT"
mount -o loop,ro "$IMG" "$W/ro"
grep -q 'buster' "$W/ro/etc/os-release" || die "$IMG is not the PLAY's Debian 10 rootfs"
mount -t overlay overlay -o "lowerdir=$W/ro,upperdir=$W/upper,workdir=$W/work" "$W/root"
for d in proc sys dev dev/pts; do mount --bind "/$d" "$W/root/$d"; done

echo "== the PLAY's GStreamer"
chroot "$W/root" gst-launch-1.0 --version | head -2
for e in fdsrc rawvideoparse queue kmssink videoconvert pngenc multifilesink; do
  if chroot "$W/root" gst-inspect-1.0 "$e" >/dev/null 2>&1; then echo "  ok      $e"; else echo "  MISSING $e"; fi
done

D="$W/root/userdata/bd-stagetimer"
mkdir -p "$D/bin" "$W/root/tmp/frames"
cp "$BD" "$D/bin/bdstage"; chmod 755 "$D/bin/bdstage"

"$MOCK" -addr 127.0.0.1:18097 -script > "$OUT/mock.log" 2>&1 &
MOCK_PID=$!
sleep 1
chroot "$W/root" /bin/bash -c "cd /userdata/bd-stagetimer && setsid ./bin/bdstage --serve 127.0.0.1:8096 --dir /userdata/bd-stagetimer \
  --display none --allow-unprotected \
  --gst-video-sink 'videoconvert ! video/x-raw,format=RGB ! pngenc ! multifilesink location=/tmp/frames/f-%05d.png' \
  > /userdata/bd-stagetimer/bd-stagetimer.log 2>&1 < /dev/null &"
sleep 2

api() { curl -s -m 10 -H 'Content-Type: application/json' "$@"; echo; }
api "127.0.0.1:8096/api/status"
api -X POST "127.0.0.1:8096/api/config" -d '{"room_id":"mock","api_key":"mock-key-0001","api_base":"http://127.0.0.1:18097",
  "display":{"view":"timer","show_title":true,"show_speaker":true,"show_progress":true,"show_next":true,"show_clock":true,"show_messages":true,"clock_24h":true,"mirror":""},"resume":false}' >/dev/null
api -X POST "127.0.0.1:8096/api/show" -d '{}'
# The mock's script runs about 50 s from its start: running, yellow, a
# message, a flash, red, overtime, paused, a focus message, next, blackout.
sleep 50
api "127.0.0.1:8096/api/status"
api -X POST "127.0.0.1:8096/api/stop" -d '{}' >/dev/null
sleep 2

cp "$W/upper/userdata/bd-stagetimer/bd-stagetimer.log" "$OUT/"
cp "$W/upper/tmp/frames/"*.png "$OUT/" 2>/dev/null || true
echo "== $(ls "$OUT"/f-*.png 2>/dev/null | wc -l) frames came out of GStreamer into $OUT"
grep -E 'video:|error|screen:' "$OUT/bd-stagetimer.log" | head -20
