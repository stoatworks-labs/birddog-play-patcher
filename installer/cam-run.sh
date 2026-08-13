#!/bin/bash
# Entry point for bdcam, started by bd-cam.service.
#
# Arguments live in bdcam.conf next to this script rather than in the unit file,
# so resolution and format can be changed over SSH with a restart and no
# repackaging:
#
#   ssh -p 9031 root@<play> 'echo BDCAM_ARGS=\"--size 1920x1080 --fps 30\" \
#       > /userdata/bd-cam/bdcam.conf; systemctl restart bd-cam'
#
# bdcam exits 0 when no camera is present, and the unit is Restart=always, so
# the service doubles as the hotplug poller — same pattern as bd-kvm.
cd "$(dirname "$0")" || exit 1

# Settings come from config.json, which the web UI tab writes. BDCAM_ARGS is
# still honoured for anything the tab does not cover.
BDCAM_ARGS=""
# shellcheck disable=SC1091
[ -f ./bdcam.conf ] && . ./bdcam.conf

LOG="$(dirname "$0")/bdcam.log"
# keep the log bounded — /userdata is roomy but this runs for days
if [ -f "$LOG" ] && [ "$(stat -c %s "$LOG" 2>/dev/null || echo 0)" -gt 1048576 ]; then
  mv -f "$LOG" "$LOG.1"
fi

# shellcheck disable=SC2086
exec ./bdcam --config /userdata/bd-cam/config.json $BDCAM_ARGS >> "$LOG" 2>&1
