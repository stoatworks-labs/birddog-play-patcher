# AGENTS.md — bdomt

The OMT receiver for the **BirdDog PLAY** (Rockchip RK3328, quad A53, Debian 10 aarch64,
glibc 2.28, GStreamer 1.14.4): a native OMT 1.0 receiver in Go, the patched libvmx for the
video, and GStreamer's `kmssink` and `alsasink` for HDMI. cgo (purego), cross-compiled with zig.
**Public**: `omt/` of the birddog-play-patcher repo.

Start with [`README.md`](README.md) for the design and the hardware list. This file is the
operating rules.

## Where this sits

`omt/` is the canonical home of `bdomt`, beside `player/` (bdplay), `converter/` (bdcam),
`gateway/` (bdgw) and `tailscale-ui/` (bdts). The 2026-10-26 feasibility spike that chose this
design (direct DRM, our own 4-thread libvmx, no stock libomt) was done outside this repo; its
conclusions are in [`docs/NOTES.md`](docs/NOTES.md). `birddog-re`, the private research repo,
must never have key material or vendor firmware copied out of it into here.

The patcher serves the committed `dist/bdomt-linux-arm64` and `dist/libvmx.so`; `./build.sh`
and `vmx/build-libvmx.sh` rebuild them. `birddog-re`'s `tools/fwbuild` takes both from a patcher
checkout (`BDOMT=`, `LIBVMX=`, `LIBVMX_LICENSE=` override, or `PATCHER=` points at the checkout).
The binary is `bdomt`, the directory `/userdata/bd-omt`, the unit `bd-omt`, the port 8094.
`omt` is in `RESERVED_MODULE_NAMES` in `public/fw.js` and in fwbuild's copy.

## Hard rules

1. **Always give the display back.** Every path out of a session ends in `Display.Release`, and
   the `/run/bdomt-took-display` marker is written *before* `BirdDogRunner` is stopped, so
   `ExecStopPost` can restart it after a kill. A change that stops `BirdDogRunner` without the
   marker, or removes the marker before `BirdDogRunner` is back, is a dark-HDMI bug.
2. **Stop `BirdDogRunner` through systemctl, never by killing `PPApp`**, and restore with
   `reset-failed` then `start` then a check that `PPApp` is running. bdplay learned each of
   those on hardware (`player/display.go`). Do not induce runner crashes to exercise this path.
3. **Cycle `PPApp` as little as possible.** Switching source keeps the display; settings changes
   restart the session, not the handover. Resume is rate-limited (three in ten minutes, in
   `/run/bdomt-resume`) and waits for `PPApp` to be running first, so a crash loop can't
   flap the decoder and a resume can't fight the installer for the screen.
4. **The control messages are libomtnet's bytes.** Senders compare them as strings. No NUL
   terminator, and `Program==` keeps its double equals. `TestControlMessagesAreLibomtnetsBytes`
   is the check.
5. **`kmssink` takes NV12 only.** Any new pixel path writes NV12 in GStreamer's default layout
   (`NV12Layout`: stride rounded up to 4, the UV plane after an even number of rows).
   `TestNV12Layout`, `TestDecodeNV12` and `vmx/nv12check.c` (against the stock UYVY decode, over
   a recorded stream) keep the patch and the layout honest.
6. **Never vendor libvmx.** The repo carries the pin (`vmx/UPSTREAM`) and the patch. Bump the
   pin, re-apply the patch, rebuild both copies and rerun the decode tests together.
7. **The template edit must never contain a template action**, and it anchors where bdcam and
   bdgw anchor. `patch_test.go` checks both, and coexistence in every install order.
8. **Keep the auth gate differential.** Only `/api/status` is open and it carries nothing a
   stranger could use to take the screen. CORS echoes the origin, never `*`.
9. **Build only through `./build.sh`.** It pins glibc 2.28 through zig and refuses to build
   without `dist/libvmx.so`. A plain `go build` gives you a binary that won't start on a PLAY.

## Toolchain

```bash
vmx/build-libvmx.sh host   # once: the host libvmx the decode tests load
go test ./...              # protocol, audio, discovery, config, patch, sender, decode, API
./build.sh                 # the above, then dist/bdomt-linux-arm64 (zig, glibc 2.28)
vmx/build-libvmx.sh        # dist/libvmx.so for the PLAY
```

## Layout

```
main.go        CLI: --serve, --discover, --receive, --patch-ui/--unpatch-ui/--restore-ui; resume
protocol.go    OMT 1.0 framing, the control messages, OMTInfo
receiver.go    the two connections per source, subscribe, tally, quality, redirect, retry
discovery.go   our own mDNS querier for _omt._tcp; name and address resolution
vmx.go         libvmx through purego; VMX_DecodeNV12 and the NV12 layout
audio.go       FPA1 planar float to S16 stereo
session.go     one source on HDMI: states, decode and audio loops, loss, restarts
display.go     the BirdDogRunner handover and the marker; the output mode
sink.go        gst-launch pipelines on pipes: rawvideoparse ! kmssink, rawaudioparse ! alsasink
config.go      config.json, validated and written atomically
auth.go        the birdUI session gate (bdgw's, unchanged in reasoning)
serve.go       the API
patch.go       the videoset.html tab: markers, anchors, backup
sysstat.go     CPU and temperature for the tab
web/omt.js     the tab, embedded and written to the web UI's static dir
vmx/           libvmx pin, NV12 patch, build script, nv12check, LICENSE.libvmx
dist/          COMMITTED: bdomt-linux-arm64 and libvmx.so, what the patcher ships
```

## Not yet true

**Nothing here has run on a PLAY.** The README's hardware list is the outstanding work, and
1080p60 decode headroom on the A53s is the item that decides what the module can promise. Until
it's done, every mention of this module says beta.

## Notes

`docs/NOTES.md` carries this module's working notes: status, decisions already made, and the
traps that have actually bitten. Read it before changing anything non-obvious.
