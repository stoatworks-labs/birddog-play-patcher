#!/usr/bin/env bash
# Static checks on the module — what can be known before anything runs as root
# on someone's device. build.sh runs this first; CI runs it on every push.
#
# It refuses what the package format or the device cannot carry, and WARNS
# about the things that would make a unit unrecoverable. Warnings are not
# errors because there are legitimate reasons (bdts and bdcam patch the web
# UI, with a health check and rollback) — but you should know you are doing it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

fail=0
ok()   { echo "  ok    $*"; }
bad()  { echo " FAIL   $*"; fail=1; }
warn() { echo " warn   $*"; }

# ---------------------------------------------------------------- manifest
[ -f module.conf ] || { bad "module.conf is missing"; exit 1; }
NAME="$(sed -n 's/^NAME=//p' module.conf | head -1 | tr -d "\"'")"
VERSION="$(sed -n 's/^VERSION=//p' module.conf | head -1 | tr -d "\"'")"

if [[ "$NAME" =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]]; then ok "NAME=$NAME"
else bad "NAME='$NAME' — needs 1–32 of a-z 0-9 -, starting with a letter or digit"; fi
case "$NAME" in
  tailscale|tailscale-ui|kvm|cam|cam-api|play|probe|modules)
    bad "NAME='$NAME' is a payload the patcher already ships" ;;
esac
if [[ "$VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$ ]]; then ok "VERSION=$VERSION"
else bad "VERSION='$VERSION' — needs 1–32 of A-Z a-z 0-9 . _ + -"; fi

# ------------------------------------------------------------------ scripts
[ -f install ] || bad "install is missing — the installer runs modules/<name>/install"
for f in install uninstall payload/*.sh; do
  [ -f "$f" ] || continue
  if bash -n "$f" 2>/dev/null; then ok "parses: $f"; else bad "does not parse: $f"; fi
done

# The recovery path. Mentioning these is not wrong, but it had better be deliberate.
if [ -f install ]; then
  # Comment lines are skipped: the template's own header names all of these.
  code() { grep -nE "$1" install | grep -vE '^[0-9]+:[[:space:]]*#' || true; }
  hits="$(code 'sshd_config|birddog-update-wrapper|BirdDogUpdateRunner|birddog-web-ui|/srv/birddog-web-ui')"
  [ -z "$hits" ] || warn "install touches the recovery path — be sure you have a rollback:"$'\n'"$hits"
  hits="$(code '\breboot\b|birddog-reboot-request')"
  [ -z "$hits" ] || warn "install mentions rebooting — the package has its own reboot option; leave it to the user:"$'\n'"$hits"
  if [ -n "$(code 'systemctl (stop|disable)[^|&;]*BirdDogRunner')" ] && [ -z "$(code 'systemctl (start|restart)[^|&;]*BirdDogRunner')" ]; then
    warn "install stops BirdDogRunner and never starts it — that is the dark-HDMI, looks-bricked failure"
  fi
fi

# --------------------------------------------------------------- the files
# What build.sh packs, with the path each will have inside the .fw.
prefix="./modules/${NAME}/"
while IFS= read -r f; do
  rel="${f#./}"
  full="${prefix}${rel}"
  if [ "${#full}" -gt 100 ]; then
    bad "path too long for ustar (${#full} > 100): $full — shorten it by $(( ${#full} - 100 ))"
  fi
  if [ -L "$f" ]; then
    bad "symlink: $rel — the package format does not carry links; create it from install"
  fi
  # An ELF that is not aarch64 can never run on the device. Checked from the
  # header bytes so this needs no `file`: magic 7f 45 4c 46, EI_CLASS 02 at +4,
  # e_machine b7 00 little-endian at +18.
  if [ -f "$f" ] && [ "$(od -An -tx1 -N4 "$f" | tr -d ' \n')" = "7f454c46" ]; then
    cls="$(od -An -tx1 -j4 -N1 "$f" | tr -d ' \n')"
    mach="$(od -An -tx1 -j18 -N2 "$f" | tr -d ' \n')"
    if [ "$cls" = "02" ] && [ "$mach" = "b700" ]; then ok "aarch64 ELF: $rel"
    else bad "ELF but not aarch64 (class $cls, machine $mach): $rel"; fi
  fi
done < <(find . -path ./payload -prune -o -name module.conf -print -o -name install -print -o -name uninstall -print; find ./payload -not -type d -not -name .DS_Store -not -name '._*' 2>/dev/null)

if [ "$fail" = 0 ]; then echo "checks passed for ${NAME} ${VERSION}"; else echo "checks FAILED"; exit 1; fi
