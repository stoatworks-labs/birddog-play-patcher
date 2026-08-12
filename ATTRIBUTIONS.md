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

### purego

<https://github.com/ebitengine/purego>  
Licence: Apache-2.0  
Copyright: The Ebitengine Authors

A Go module dependency of the on-device agent.

Calls into a C shared library from Go without cgo, which is how the agent dlopens whatever libndi the device already has instead of linking against a redistributable copy.

### The npm ecosystem

<https://www.npmjs.com>  
Licence: predominantly MIT  
Copyright: the individual package authors

npm dependencies, resolved and pinned in the lockfile.

Build tooling, test runners and the libraries the front ends are assembled from. The exact set and versions for any build are in that repo's lockfile, which is the authoritative list.

The full transitive dependency set for any build is pinned in this repo's lockfile,
which is the authoritative list. What is named above is the layers a reader would
want to know about, not every package that has ever been resolved.

## Getting this wrong

If your work is here and the description is inaccurate, the licence is wrong, or you would rather not be listed — open an issue and it will be fixed.
