#!/bin/bash
# bd-stagetimer.service's ExecStart: rotate the log, hand over to bdstage.
# exec, so systemd supervises bdstage itself — and its SIGTERM reaches the
# process that knows to stop drawing and give the display back.
cd "$(dirname "$0")" || exit 1
LOG="$(pwd)/bd-stagetimer.log"
if [ -f "$LOG" ] && [ "$(stat -c %s "$LOG" 2>/dev/null || echo 0)" -gt 1048576 ]; then
  mv -f "$LOG" "$LOG.1"
fi
exec ./bin/bdstage --serve :8096 --dir "$(pwd)" "$@" >> "$LOG" 2>&1
