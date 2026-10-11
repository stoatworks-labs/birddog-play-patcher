#!/bin/bash
# The unit's ExecStart. Keeps a log beside the payload and hands over to the
# program proper — the same shape as the patcher's own bd-kvm and bd-cam units.
cd "$(dirname "$0")" || exit 1

LOG="$(pwd)/bd-hello.log"
# Rotate at 1 MB: a device with little free space and a chatty service is how
# /userdata fills up.
if [ -f "$LOG" ] && [ "$(stat -c %s "$LOG" 2>/dev/null || echo 0)" -gt 1048576 ]; then
  mv -f "$LOG" "$LOG.1"
fi

# Swap this for your own program — `exec ./bin/yourtool "$@"` once it is in
# payload/bin/. exec, so systemd supervises the program itself, not this shell.
exec ./hello.sh "$@" >> "$LOG" 2>&1
