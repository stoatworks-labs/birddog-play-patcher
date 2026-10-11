#!/bin/bash
# The example service. It writes a heartbeat file so an install can be proven
# from an SSH session with nothing else on the device:
#
#   cat /userdata/bd-hello/heartbeat
#   systemctl status bd-hello
#
# Replace it with whatever your module actually does. Stock PLAY firmware is
# Debian 10 (glibc 2.28) on aarch64 with bash, coreutils, systemd, curl and
# GStreamer — but no python, no perl and no package manager.
cd "$(dirname "$0")" || exit 1

INTERVAL=60
# shellcheck disable=SC1091  # optional, written by the user on the device
[ -f hello.conf ] && . ./hello.conf

VERSION="$(sed -n 's/^VERSION=//p' module.conf 2>/dev/null | head -1 | tr -d "\"'")"

while :; do
  printf '%s up=%ss version=%s interval=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(cut -d' ' -f1 /proc/uptime)" "${VERSION:-?}" "$INTERVAL" \
    > heartbeat.tmp
  # Write-then-rename, so a reader never sees a half-written file.
  mv -f heartbeat.tmp heartbeat
  sleep "$INTERVAL"
done
