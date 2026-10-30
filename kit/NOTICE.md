# BirdDog PLAY Patcher kit: notices

This kit holds the files the [BirdDog PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com)
page packs into a `.fw` package: `assets/` is the installer scripts and the on-device programs,
exactly as the page serves them, with their `manifest.json`. [Burrow](https://github.com/stoatworks-labs/burrow)
builds packages from it, so it doesn't have to scrape them off the page.

Not affiliated with or endorsed by BirdDog. Installing a package built from this will not do
your warranty any favours.

## What isn't in here

- **Tailscale.** Whoever builds the package fetches the official arm64 tarball from
  `pkgs.tailscale.com` and checks it against its `.sha256` sidecar. Its binaries are BSD-3-Clause,
  © Tailscale Inc. & AUTHORS.
- **MediaMTX.** Fetched from the pinned GitHub release named in `kit.json` and checked against
  the `checksums.sha256` published beside it. MIT, © its authors. The tarball's own `LICENSE`
  goes into the package as `/userdata/bd-gw/LICENSE.mediamtx`.
- **NDI.** The KVM agent loads the NDI runtime already on the device. None of it is
  redistributed. NDI® is a registered trademark of Vizrt Group.
- Anything from a vendor firmware file. A package doesn't need one.

## This repo's own files

The installer scripts and, in `assets/`, `bdts-linux-arm64`, `bdkvm-linux-arm64`,
`bdcam-linux-arm64`, `bdplay-linux-arm64`, `bdpdf-linux-arm64`, `bdgw-linux-arm64` and
`bdomt-linux-arm64` come from
<https://github.com/stoatworks-labs/birddog-play-patcher>, under the MIT licence in
`NOTICES/birddog-play-patcher-LICENSE`. The binaries are the ones committed in that repo's
`*/dist/` folders at the commit `kit.json` names, and their source sits beside them:
`tailscale-ui/`, `agent/`, `converter/`, `player/`, `gateway/` and `omt/`.

## libvmx

`assets/libvmx.so` is [libvmx](https://github.com/openmediatransport/libvmx), the VMX codec
Open Media Transport uses, built from the upstream commit named in
[omt/vmx/UPSTREAM](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/omt/vmx/UPSTREAM)
with one patch (an NV12 output, `omt/vmx/libvmx-decode-nv12.patch`) by `omt/vmx/build-libvmx.sh`.
MIT, © Open Media Transport Contributors. The licence is `assets/LICENSE.libvmx`, and the page
packs it beside the library as `/userdata/bd-omt/LICENSE.libvmx`.

## PDFium

`assets/libpdfium.so` is the `pdfium-linux-arm64` build from
[bblanchon/pdfium-binaries](https://github.com/bblanchon/pdfium-binaries) release
`chromium/7999`, redistributed unmodified. PDFium is BSD-3-Clause, © The PDFium Authors.
`NOTICES/pdfium/` is the licence directory from that release: `pdfium.txt` is PDFium's own
licence, the other files are the libraries compiled into it, and `LICENSE` is the build's MIT
licence, © Benoit Blanchon.

## fuse-exfat and libfuse

`assets/mount.exfat-fuse` is [fuse-exfat](https://github.com/relan/exfat) 1.3.0 (GPL v2,
© Andrew Nayenko) statically linked against [libfuse](https://github.com/libfuse/libfuse) 2.9.9
(LGPL v2.1). The licences are `NOTICES/fuse-exfat-COPYING` and `NOTICES/libfuse-COPYING.LIB`.

Both are copyleft, so their corresponding source is offered. Each is built unmodified from the
upstream release tarball by
[player/tools/build-exfat.sh](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/player/tools/build-exfat.sh),
which names the exact versions and flags used:

- <https://github.com/relan/exfat/releases/download/v1.3.0/fuse-exfat-1.3.0.tar.gz>
- <https://github.com/libfuse/libfuse/releases/download/fuse-2.9.9/fuse-2.9.9.tar.gz>

Full credits are in
[ATTRIBUTIONS.md](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/ATTRIBUTIONS.md).
