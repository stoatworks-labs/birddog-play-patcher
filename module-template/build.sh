#!/usr/bin/env bash
# Build the module archive the PLAY patcher takes: dist/<NAME>-<VERSION>.tgz
#
# Only what the device needs goes in — module.conf, install, uninstall and
# payload/. README, this script and .github stay out. The archive is plain
# ustar owned by root, which is exactly what the patcher will write into the
# .fw; anything the patcher would refuse (a path over 100 bytes, a symlink, an
# x86 binary) is refused here first by check.sh.
#
# Works with GNU tar and with the bsdtar macOS ships.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

./check.sh

NAME="$(sed -n 's/^NAME=//p' module.conf | head -1 | tr -d "\"'")"
VERSION="$(sed -n 's/^VERSION=//p' module.conf | head -1 | tr -d "\"'")"
OUT="dist/${NAME}-${VERSION}.tgz"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp module.conf install "$STAGE/"
[ -f uninstall ] && cp uninstall "$STAGE/"
if [ -d payload ]; then
  cp -R payload "$STAGE/payload"
  find "$STAGE/payload" \( -name .DS_Store -o -name '._*' \) -delete
fi
chmod 755 "$STAGE/install"
[ -f "$STAGE/uninstall" ] && chmod 755 "$STAGE/uninstall"

mkdir -p dist
# COPYFILE_DISABLE keeps macOS from adding ._ AppleDouble members. The two tar
# families spell "owned by root, ustar format" differently.
if tar --version 2>/dev/null | grep -q GNU; then
  ( cd "$STAGE" && tar --format=ustar --owner=0 --group=0 --numeric-owner \
      --sort=name --mtime='2020-01-01 00:00:00Z' -czf "$OLDPWD/$OUT" . )
else
  ( cd "$STAGE" && COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 \
      --uname root --gname root -czf "$OLDPWD/$OUT" . )
fi

echo
echo "built: $OUT ($(du -h "$OUT" | cut -f1))"
echo "sha256: $(shasum -a 256 "$OUT" | cut -d' ' -f1)"
echo "contents, as the device's tar will list them under modules/${NAME}/:"
tar tzvf "$OUT" | sed 's/^/  /'
echo
echo "Next: choose this file under 'Custom modules' at"
echo "  https://birddog-play-patcher.stoatworks-labs.com/"
echo "and read the install log on the device afterwards: /tmp/bd-custom-install.log"
