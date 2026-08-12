#!/bin/bash
# First-boot diagnostics. Answers the questions static analysis could not.
#
# Writes to the web UI's static dir, which is served over plain HTTP with NO
# authentication (verified on a live unit), so the report can be collected
# without SSH — useful precisely when SSH is what isn't working yet:
#
#     curl http://<play-ip>/static/bd-probe.txt
#
# BECAUSE that path is unauthenticated, this script deliberately collects
# hardware and configuration facts ONLY. No keys, no wpa_supplicant, no
# /etc/shadow, no authorized_keys, nothing from /userdata/tailscale/state.
BUILD_TAG="${1:-unknown}"
OUT_WEB=/srv/birddog-web-ui/static/bd-probe.txt
OUT_LOCAL=/userdata/bd-probe.txt

sec() { echo; echo "===== $* ====="; }
have() { command -v "$1" >/dev/null 2>&1; }

{
echo "BirdDog PLAY probe — build ${BUILD_TAG} — $(date -u 2>/dev/null) UTC"

sec "identity"
echo "hardware-version : $(cat /etc/birddog-hardware-version 2>/dev/null)"
echo "firmware-version : $(cat /etc/birddog-firmware-version 2>/dev/null)"
echo "common-version   : $(cat /etc/birddog-firmware-version-common 2>/dev/null)"
echo "shortname        : $(cat /etc/birddog-shortname 2>/dev/null)"
echo "mode             : $(cat /etc/birddog-mode 2>/dev/null)"
echo "hostname         : $(hostname 2>/dev/null)"

sec "SoC — settles RK3328 vs RK3318 (notes/03)"
tr '\000' '\n' < /proc/device-tree/compatible 2>/dev/null
echo "-- model:"
tr '\000' '\n' < /proc/device-tree/model 2>/dev/null
echo "-- serial-number:"
tr '\000' '\n' < /proc/device-tree/serial-number 2>/dev/null

sec "kernel / cpu"
uname -a
echo "-- cpu:"
grep -E 'processor|model name|Hardware|Revision|BogoMIPS|Features' /proc/cpuinfo 2>/dev/null | head -40
echo "-- cpufreq policies:"
ls /sys/devices/system/cpu/cpufreq/ 2>/dev/null
for p in /sys/devices/system/cpu/cpufreq/policy*/; do
  echo "  $p max=$(cat "$p/cpuinfo_max_freq" 2>/dev/null) cur=$(cat "$p/scaling_cur_freq" 2>/dev/null) gov=$(cat "$p/scaling_governor" 2>/dev/null)"
done

sec "TUN — decides tailscale full vs userspace mode (notes/06)"
ls -l /dev/net/tun 2>&1
echo "-- tun in /proc/misc:  $(grep -c tun /proc/misc 2>/dev/null)"
echo "-- modprobe tun:"; modprobe tun 2>&1 && echo "  modprobe ok"; ls -l /dev/net/tun 2>&1
echo "-- module list:"; lsmod 2>/dev/null | head -30

sec "storage — decides whether 68 MB of tailscale fits (notes/06)"
df -h 2>/dev/null
echo "-- mounts:"; mount 2>/dev/null
echo "-- /userdata:"; ls -la /userdata 2>/dev/null | head -30

sec "memory"
free -m 2>/dev/null || cat /proc/meminfo 2>/dev/null | head -6

sec "usb — host port, hub, and any attached input devices"
have lsusb && lsusb 2>/dev/null
have lsusb && lsusb -t 2>/dev/null
echo "-- /sys/bus/usb/devices:"; ls /sys/bus/usb/devices 2>/dev/null
echo "-- input devices:"; ls -l /dev/input/by-id/ 2>/dev/null; ls -l /dev/input/ 2>/dev/null
echo "-- udc (usb gadget capable?):"; ls -l /sys/class/udc/ 2>/dev/null

sec "video / drm"
ls -l /dev/dri/ 2>/dev/null
for c in /sys/class/drm/card*/status; do echo "  $c = $(cat "$c" 2>/dev/null)"; done
echo "-- modes:"; cat /sys/class/drm/card0-HDMI-A-1/modes 2>/dev/null | head -20
echo "-- video nodes:"; ls -l /dev/video* /dev/mpp_service /dev/rga /dev/vpu_service /dev/rkvdec 2>/dev/null
# If a camera is attached at install time, its format list decides the whole CPU
# budget for bdcam (uncompressed is nearly free, MJPEG is not), so capture it
# here — it saves an SSH session.
if [ -x /userdata/bd-cam/bdcam ]; then
  echo "-- uvc capture formats:"; /userdata/bd-cam/bdcam --list 2>&1 | head -40
fi

sec "ndi / codec libraries present"
ls -l /usr/lib/aarch64-linux-gnu/libndi* /usr/lib/aarch64-linux-gnu/librockchip_mpp* /usr/lib/aarch64-linux-gnu/librga* 2>/dev/null

sec "network"
ip addr 2>/dev/null || ifconfig -a 2>/dev/null
echo "-- listening sockets:"
(ss -lntup 2>/dev/null || netstat -lntup 2>/dev/null) | head -40

sec "sshd policy (does our authorized_keys actually work?)"
grep -iE '^\s*(Port|PermitRootLogin|PubkeyAuthentication|PasswordAuthentication|AuthorizedKeysFile)' /etc/ssh/sshd_config 2>/dev/null
echo "-- root authorized_keys line count: $(wc -l < /root/.ssh/authorized_keys 2>/dev/null)"

sec "birddog services"
systemctl list-units --type=service --no-pager 2>/dev/null | grep -iE 'birddog|bd-|tailscale'
echo "-- our units:"
systemctl status bd-tailscaled --no-pager 2>/dev/null | head -12

sec "os"
cat /etc/os-release 2>/dev/null | head -4
echo "-- glibc: $(ldd --version 2>/dev/null | head -1)"

echo
echo "===== end of probe ====="
} > "$OUT_LOCAL" 2>&1

# publish where it can be fetched without credentials
if [ -d /srv/birddog-web-ui/static ]; then
  cp -f "$OUT_LOCAL" "$OUT_WEB" 2>/dev/null && chmod 644 "$OUT_WEB" 2>/dev/null
fi

exit 0
