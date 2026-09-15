#!/bin/bash
# Entry point for the Streaming tab's API, started by bd-gw.service.
#
# A separate unit from the hub on purpose: a stopped or broken MediaMTX must
# still leave a page that can say so.
cd "$(dirname "$0")" || exit 1

exec ./bdgw --dir /userdata/bd-gw --serve :8093
