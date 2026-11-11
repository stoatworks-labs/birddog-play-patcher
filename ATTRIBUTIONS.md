# Attributions

BirdDog PLAY Patcher is built on other people's work. This file lists what that work is, who did
it, and what it is doing here.

It is generated — the master lists live in the `stoatworks-backend` repo and are
pushed out by `scripts/sync-attributions.py`. Edit it there, not here.

## Third-party code this project uses

Libraries, SDKs and frameworks the project is built on or bundles.

### NDI SDK

<https://ndi.video/for-developers/ndi-sdk/>  
Licence: NDI SDK Licence Agreement (proprietary)  
Copyright: Vizrt Group

Headers only, vendored so the backend compiles everywhere. The runtime is never redistributed — it is loaded with dlopen at run time if the user has installed it.

NDI is the video transport most of this fleet's users already run. Compiling against the headers without shipping the runtime keeps the licence intact and still gives every build the backend.

### Tailscale

<https://github.com/tailscale/tailscale>  
Licence: BSD-3-Clause  
Copyright: Tailscale Inc. & AUTHORS

Not vendored and not bundled in the repo. The official static arm64 release tarball is fetched at build time, verified against its published SHA-256, and its tailscaled/tailscale binaries are placed inside the firmware package the user downloads. That is a binary redistribution, so the BSD-3 notice travels with it.

It is what puts a device on a tailnet, and the official static build has no library dependencies at all — which is what makes it installable on a locked-down appliance rootfs without touching anything else.

### MediaMTX

<https://github.com/bluenviron/mediamtx>  
Licence: MIT  
Copyright: aler9 (Copyright (c) 2019) and the MediaMTX contributors

Not vendored and not bundled in the repo. The official static linux/arm64 release tarball of one pinned version is fetched at build time, verified against the checksums.sha256 GitHub publishes beside it, and its mediamtx binary is placed inside the firmware package the user downloads, with the tarball's LICENSE file next to it. That is a binary redistribution, so the MIT notice travels with it.

It is the protocol hub the streaming gateway is built on — RTMP, RTSP, HLS, WebRTC, SRT and UDP in and out, remuxing rather than transcoding — so the PLAY gains every one of those endpoints without a streaming stack of its own, and the picture can still go through the device's own decoder over SRT on loopback.

### libvmx

<https://github.com/openmediatransport/libvmx>  
Licence: MIT  
Copyright: Open Media Transport Contributors (Copyright (c) 2025)

Not vendored. The repo carries the upstream commit it is pinned to and one patch, which adds a decode straight to NV12 (VMX_DecodeNV12). The build fetches the source at that commit, applies the patch and cross-compiles it for the PLAY, and the resulting libvmx.so is committed and placed inside the package the user downloads, beside the receiver that loads it, with libvmx's LICENSE file next to it as LICENSE.libvmx. That is a binary redistribution of modified code, so the MIT notice travels with it.

Open Media Transport's video is VMX, and libvmx is that codec's reference library. The OMT receiver loads it at run time and decodes each frame straight to NV12, the one layout the PLAY's display sink accepts, so the receiver needs no codec of its own.

### PDFium

<https://pdfium.googlesource.com/pdfium/>  
Licence: BSD-3-Clause and Apache-2.0 (its LICENSE carries both)  
Copyright: The PDFium Authors (Copyright 2014)

Not vendored and not built here. The prebuilt linux-arm64 libpdfium.so from one release of bblanchon/pdfium-binaries is committed unmodified and placed inside the package the user downloads, beside the PDF renderer that links it. That build compiles in its own copies of FreeType (FreeType License), ICU (Unicode License v3), libjpeg-turbo (IJG, BSD-3-Clause and zlib), OpenJPEG (BSD-2-Clause), libpng (PNG Reference Library License v2), libtiff (the libtiff licence), Little CMS, fast_float and simdutf (MIT), zlib (zlib), Abseil (Apache-2.0), LLVM's libc (Apache-2.0 with LLVM Exceptions) and Anti-Grain Geometry 2.3 (its own permissive notice), none of them copyleft. The release's licence directory, with each of those texts and the build's own MIT licence (Benoit Blanchon, Copyright 2014-2025), is committed in the repo and travels with its release kit.

It turns each page of a PDF on a USB stick into a still the media player can put on HDMI, so a deck exported to PDF plays like slides. It is permissive where MuPDF is AGPL, small enough to be served by the web patcher, and the prebuilt library needs nothing the stock firmware lacks: it asks for glibc 2.17 against the device's 2.28 and carries its own C++ runtime.

### fuse-exfat

<https://github.com/relan/exfat>  
Licence: GPL-2.0-or-later  
Copyright: Andrew Nayenko (Copyright (C) 2010-2018)

Not vendored. A build script run by hand fetches the upstream release tarball, builds it unmodified and links it statically, with libfuse, into a single mount.exfat-fuse binary, which is committed and placed inside the package the user downloads, beside the media player that runs it. That is a binary redistribution of GPL code, so its corresponding source is offered: the build script names the exact release tarball, version and flags it was built from, and the GPL's text is committed in the repo and travels with its release kit. The player only executes the helper as a separate program, so the player itself is not a derived work and keeps its own licence.

The PLAY's kernel has no exFAT driver and no loadable modules, but it does have FUSE, so exFAT can be mounted entirely in user space and the OS image is never touched. Without it, a stick formatted exFAT does not mount at all.

### libfuse

<https://github.com/libfuse/libfuse>  
Licence: LGPL-2.1  
Copyright: Miklos Szeredi (Copyright (C) 2001-2007) and the libfuse contributors

Not vendored. The 2.x release tarball is fetched by the same build script as fuse-exfat, built unmodified as a static library and linked into the mount.exfat-fuse binary placed inside the package the user downloads. That is a redistribution of LGPL code inside a statically linked program, so its corresponding source is offered the same way: the build script names the exact tarball, version and flags, and COPYING.LIB is committed in the repo and travels with its release kit.

The FUSE library fuse-exfat is written against. The device has neither libfuse nor fusermount, so the library is linked in, and libfuse 2.x tries the mount(2) system call before it falls back to fusermount; the player runs as root, so that call succeeds and no helper has to be shipped beside it.

### purego

<https://github.com/ebitengine/purego>  
Licence: Apache-2.0  
Copyright: The Ebitengine Authors

A Go module dependency of the on-device agent.

Calls into a C shared library from Go without cgo, which is how the agent dlopens whatever libndi the device already has instead of linking against a redistributable copy.

### Noto Sans

<https://github.com/notofonts/latin-greek-cyrillic>  
Licence: SIL Open Font License 1.1  
Copyright: The Noto Project Authors (Copyright 2022)

Regular and Bold, complete and unmodified (not subset), committed with OFL.txt beside them and compiled into a Go program with //go:embed, so they reach the user inside that program's binary rather than as font files. The binary is placed in the package the user downloads with OFL.txt next to it, so the copyright notice and the licence travel with the fonts.

Text a program draws onto a video frame itself, at any size and in Latin, Greek or Cyrillic, with no font to install on the device. The OFL allows the fonts to be bundled with any software, provided each copy carries the notice and the licence and the fonts are never sold on their own.

### The npm ecosystem

<https://www.npmjs.com>  
Licence: predominantly MIT  
Copyright: the individual package authors

npm dependencies, resolved and pinned in the lockfile.

Build tooling, test runners and the libraries the front ends are assembled from. The exact set and versions for any build are in that repo's lockfile, which is the authoritative list.

The full transitive dependency set for any build is pinned in this repo's lockfile,
which is the authoritative list. What is named above is the layers a reader would
want to know about, not every package that has ever been resolved.

## Work we checked ourselves against

No code was taken from these — but they were how we knew we had it right, and that is worth saying out loud.

### libomtnet — Open Media Transport Contributors

<https://github.com/openmediatransport/libomtnet>  
Licence: MIT

The OMT receiver in omt/, its framing and its mDNS querier are Go written against libomtnet's source, the protocol's reference implementation, read at the commit pinned in omt/vmx/UPSTREAM. The frame layout, the size limits and the control messages follow it exactly, the `Program==` typo included, because senders compare those messages as strings. No code is copied, and nothing of libomtnet is built or shipped here.

## Getting this wrong

If your work is here and the description is inaccurate, the licence is wrong, or you would rather not be listed — open an issue and it will be fixed.
