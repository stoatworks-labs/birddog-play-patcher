#!/usr/bin/env bash
# Build libvmx, the VMX codec OMT video uses, with this folder's NV12 patch.
#
#   vmx/build-libvmx.sh            the PLAY's copy: dist/libvmx.so (aarch64, glibc 2.28)
#   vmx/build-libvmx.sh host       this machine's copy, for the tests: vmx/.build/
#
# Upstream is github.com/openmediatransport/libvmx (MIT), pinned to the commit in
# UPSTREAM. The source is fetched into vmx/.src (gitignored) rather than vendored:
# the repo carries only the patch, so what changed is one reviewable diff.
#
# The PLAY build follows upstream's buildlinuxarm64.sh flags, with zig as the
# cross compiler and the target pinned to glibc 2.28 (Debian 10) — the same pin
# as bdcam and bdkvm. zig links libc++ statically, so the library needs nothing
# beyond libc, libm and libpthread. Give it a soname, or zig records the build
# path as its NEEDED name.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
PIN="$(awk '$1=="libvmx"{print $2}' "$HERE/UPSTREAM")"
SRC="$HERE/.src/libvmx"
MODE="${1:-play}"

if [ ! -d "$SRC/.git" ]; then
  mkdir -p "$HERE/.src"
  git clone -q https://github.com/openmediatransport/libvmx "$SRC"
fi
git -C "$SRC" fetch -q origin 2>/dev/null || true
git -C "$SRC" checkout -q --force "$PIN"
git -C "$SRC" clean -qfdx
git -C "$SRC" apply "$HERE/libvmx-decode-nv12.patch"

CPP=("$SRC/src/vmxcodec_arm.cpp" "$SRC/src/vmxcodec.cpp")
FLAGS=(-O3 -std=c++17 -fdeclspec -fPIC)

case "$MODE" in
  play)
    command -v zig >/dev/null || { echo "error: zig not found (brew install zig)" >&2; exit 1; }
    mkdir -p "$ROOT/dist"
    zig c++ -target aarch64-linux-gnu.2.28 "${FLAGS[@]}" -shared \
      -Wl,-soname,libvmx.so -s "${CPP[@]}" -o "$ROOT/dist/libvmx.so"
    zig cc -target aarch64-linux-gnu.2.28 -O2 -I"$SRC/src" "$HERE/nv12check.c" \
      -L"$ROOT/dist" -lvmx -Wl,-rpath,'$ORIGIN' -s -o "$HERE/.build-nv12check-linux-arm64" 2>/dev/null || true
    echo "built: dist/libvmx.so (libvmx $PIN + NV12 patch)"
    file "$ROOT/dist/libvmx.so"
    echo "glibc: $(strings -a "$ROOT/dist/libvmx.so" | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tail -1) (device has 2.28)"
    ;;
  host)
    mkdir -p "$HERE/.build"
    case "$(uname -s)-$(uname -m)" in
      Darwin-arm64)
        clang++ "${FLAGS[@]}" -dynamiclib -install_name @rpath/libvmx.dylib \
          "${CPP[@]}" -o "$HERE/.build/libvmx.dylib"
        LIB="$HERE/.build/libvmx.dylib" ;;
      Linux-aarch64)
        clang++ "${FLAGS[@]}" -shared -Wl,-soname,libvmx.so "${CPP[@]}" -o "$HERE/.build/libvmx.so"
        LIB="$HERE/.build/libvmx.so" ;;
      Linux-x86_64)
        clang++ "${FLAGS[@]}" -mlzcnt -mavx2 -mbmi -shared -Wl,-soname,libvmx.so \
          "$SRC/src/vmxcodec_x86.cpp" "$SRC/src/vmxcodec_avx2.cpp" "$SRC/src/vmxcodec.cpp" \
          -o "$HERE/.build/libvmx.so"
        LIB="$HERE/.build/libvmx.so" ;;
      *) echo "error: no host build for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
    esac
    cc -O2 -I"$SRC/src" "$HERE/nv12check.c" -L"$HERE/.build" -lvmx \
      -Wl,-rpath,"$HERE/.build" -o "$HERE/.build/nv12check"
    echo "built: $LIB and vmx/.build/nv12check"
    ;;
  *)
    echo "usage: vmx/build-libvmx.sh [play|host]" >&2
    exit 2
    ;;
esac
