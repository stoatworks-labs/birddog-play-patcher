#!/bin/bash
# Entry point for the streaming gateway, started by bd-mtx.service.
#
# bdgw renders mediamtx.yml from config.json first, so what MediaMTX runs is
# always what the Streaming tab shows. Exit 3 from the render means the gateway
# is switched off in the config: exit 0 and let Restart=always poll for it
# being switched on, the same trick bd-cam uses for a missing camera.
cd "$(dirname "$0")" || exit 1

./bdgw --dir /userdata/bd-gw --render
rc=$?
if [ "$rc" = 3 ]; then
  exit 0
fi
[ "$rc" = 0 ] || exit "$rc"

exec ./mediamtx /userdata/bd-gw/mediamtx.yml
