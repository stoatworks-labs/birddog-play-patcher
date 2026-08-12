#!/bin/bash
# Entry point for BirdDog's own KVM plumbing.
#
#   udev (99-usb-detect-kvm.rules)
#     -> /bin/birddog-kvm-manager   (checks /dev/input/by-id for -kbd / -mouse
#                                    and that a source is selected)
#     -> BirdDogKVM.service
#     -> /bin/birddog-kvm-runner    (cd /userdata/birddog-kvm && exec ./run.sh)
#     -> this script
#
# So hotplug, supervision and restart are all inherited from stock firmware.
cd "$(dirname "$0")" || exit 1

LOG="$(dirname "$0")/bdkvm.log"
# keep the log from growing without bound on a device with little free space
if [ -f "$LOG" ] && [ "$(stat -c %s "$LOG" 2>/dev/null || echo 0)" -gt 1048576 ]; then
  mv -f "$LOG" "$LOG.1"
fi

exec ./bdkvm "$@" >> "$LOG" 2>&1
