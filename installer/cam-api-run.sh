#!/bin/bash
# Entry point for bdcam's configuration API, started by bd-cam-api.service.
#
# This is a separate unit from bd-cam on purpose: the streamer exits when no
# camera is attached, and the settings page has to stay answerable regardless.
cd "$(dirname "$0")" || exit 1

BDCAM_API_ADDR=":8090"
# shellcheck disable=SC1091
[ -f ./bdcam.conf ] && . ./bdcam.conf

exec ./bdcam --serve "$BDCAM_API_ADDR" \
  --config /userdata/bd-cam/config.json \
  --log /userdata/bd-cam/bdcam.log \
  --unit bd-cam
