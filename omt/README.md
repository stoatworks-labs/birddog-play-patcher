# bdomt: an OMT receiver for the BirdDog PLAY

> **AI-assisted project.** This codebase was created with [Claude Code](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author. **It has not yet run on a PLAY.** It has
> received a real OMT sender off the device, its GStreamer pipelines have run under the PLAY's own
> GStreamer version in a Debian 10 chroot, and the binary it ships links against the PLAY's glibc.
> The list at the end is what only the hardware can answer.

`bdomt` puts an [Open Media Transport](https://github.com/openmediatransport) source on a BirdDog
PLAY's HDMI output, with its sound. OMT is the open video-over-IP protocol that came out of vMix,
and the PLAY's own decoder has never heard of it, which is a shame, because a PLAY is otherwise a
perfectly good little box for putting a network source on a screen.

Pick a source from the ones announcing themselves on the network, or type `host:port` for one on
another subnet, and press **SHOW ON HDMI**. Press **STOP** and the PLAY goes back to being a
PLAY.

Installed by the [BirdDog PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com) as its
*OMT receiver* option, or by `birddog-re`'s `fwbuild --with-omt`. The how-to is in
[docs/USER-GUIDE.md](docs/USER-GUIDE.md).

## How the picture reaches HDMI

The PLAY's decoder, `PPApp`, can't be pointed at OMT the way the streaming gateway points it at
SRT, so this one takes the display, as the USB media player does:

```
OMT sender ──tcp (video, metadata, tally)──▶ ┌───────┐  NV12   ┌──────────────────────────────┐
           ──tcp (audio)───────────────────▶ │ bdomt │ ──────▶ │ gst-launch: rawvideoparse !  │ ─▶ HDMI
                                             │       │         │   kmssink                    │
                                             │ libvmx│  S16    ├──────────────────────────────┤
                                             │       │ ──────▶ │ rawaudioparse ! alsasink     │ ─▶ HDMI audio
                                             └───────┘         └──────────────────────────────┘
```

- **The wire protocol is spoken here**, in Go (`protocol.go`, `receiver.go`). Stock libomt is a
  .NET library and on Linux it finds senders through Avahi, which the PLAY doesn't run; the
  feasibility spike watched it hand back a NULL receiver in one case and abort in the other. So
  `bdomt` has its own small mDNS querier and its own OMT 1.0 receiver: two TCP connections per
  source, one subscribed to video, metadata, quality and tally, the other to audio, with
  redirects followed.
- **Video is decoded by libvmx**, the codec's reference library, loaded at runtime with purego.
  The PLAY's display plane takes NV12 and nothing else, and libvmx doesn't write NV12, so
  [vmx/libvmx-decode-nv12.patch](vmx/libvmx-decode-nv12.patch) adds `VMX_DecodeNV12`.
  `vmx/nv12check.c` holds it to libvmx's own UYVY decode over a recorded stream: the same Y byte
  for byte, and each chroma sample the rounded mean of the two rows it stands for.
- **Sound** arrives as OMT's planar float (FPA1) and goes to the HDMI card as 16-bit stereo.
- **The display handover** is bdplay's, with its scars intact. `BirdDogRunner` is stopped through
  systemd (never killed, or systemd restarts it mid-frame), restored with `reset-failed` first
  because `PPApp` gets itself rate-limited, and checked afterwards, because `systemctl start`
  returning 0 only means systemd has queued it. If `BirdDogRunner` was already stopped when we
  took over, it stays stopped.
- **A kill can't leave HDMI dark.** While `bdomt` holds the display, `/run/bdomt-took-display`
  exists, and `bd-omt.service`'s `ExecStopPost` restarts `BirdDogRunner` if it finds it.

The output stays at whatever resolution birdUI set and `kmssink` scales the source onto it, so a
720p source on a 1080p screen doesn't flick the telly into a new mode. *Match output to source*
turns mode-setting back on.

## What is installed

```
/userdata/bd-omt/bdomt            this program (7 MB, cgo, glibc 2.17 floor)
/userdata/bd-omt/libvmx.so        the patched codec (320 KB)
/userdata/bd-omt/LICENSE.libvmx   its MIT licence
/userdata/bd-omt/config.json      the settings the tab edits; written on first start, kept across reinstalls
/etc/systemd/system/bd-omt.service  bdomt --serve :8094 --dir /userdata/bd-omt
```

One unit. It runs the API whether or not anything is on screen, so the tab can always say what's
going on. The **OMT** tab is added to `/srv/birddog-web-ui/videoset.html` with the same anchors
bdcam's *UVC Converter* and bdgw's *Streaming* tabs use, so all three coexist in whichever order
they went in. Like theirs, the edit is two marker-wrapped lines with no template action in them,
and the page is backed up first as `videoset.html.bdomt-stock`.

## The tab

- **Source**: what's announcing itself on the network (**LOOK AGAIN** to browse again), or an
  address typed in. A source picked by name is found again by name, so a sender that gets a new
  address from DHCP is still found.
- **SHOW ON HDMI** takes the display and shows the source; with something already showing it's
  **SWITCH**, which changes source without handing the display back in between. **STOP** gives
  it back.
- **Settings**: the quality to ask the sender for, sound on or off, tally, hold the last frame
  or go black when the source stops, match output to source, come back on after a restart, and
  decode threads.
- **Status**: what's happening, the stream's size, rate and codec, frames and drops, CPU and
  temperature, and the log.

A source that was on air comes back after a reboot or a crash, at most three times in ten
minutes, and only once `PPApp` has started (so a show doesn't fight the installer for the
screen). Stopping from the tab clears it.

OMT's quality setting is a suggestion. The sender encodes at the highest quality any of its
receivers asks for, so the PLAY can ask for *Low* but can't insist on it.

## The API

```
GET  /api/status    open: state, source, stream, counters, CPU, temperature
GET  /api/sources   OMT sources on the network (?refresh=1 browses again)
POST /api/show      {"source":{"name":"…","address":"…"}}
POST /api/stop
GET  /api/config    the settings
POST /api/config    change them (a live session restarts with them)
GET  /api/log       the last 200 log lines
```

Everything but `/api/status` needs a birdUI session. The gate is the gateway's: the tab sends the
`BirdDogSession` cookie it already holds (cookies are scoped by host, not port), and `bdomt` asks
the web UI on loopback whether that cookie opens the System page. If the page opens without a
cookie too, the PLAY has no password set and the gate refuses rather than pretend it checked
anything; `--allow-unprotected` overrides that, and then anyone who can reach port 8094 can put
any OMT source on your screen.

## What a PLAY can show

Progressive OMT, VMX video, up to whatever four Cortex-A53s at 1.4 GHz can decode. The spike
modelled that from instruction counts rather than measuring it on the hardware:

| Source | Decode, of 4 cores | Verdict |
|---|---|---|
| 720p60 | about 1.4 | should be fine |
| 1080p30 | about 1.3 | should be fine |
| 1080p60, low quality | about 1.7 | probably fine |
| 1080p60, standard quality, camera content | about 2.6 to 2.8, plus the network | might be |
| 2160p | no | |

Interlaced sources are refused. The sound isn't lined up with the picture: video is shown as it's
decoded and audio goes through ALSA's buffer, so the sound runs a little late, by an amount
nobody has measured yet. Alpha is ignored, since HDMI has nowhere to put it.

## Build

```bash
vmx/build-libvmx.sh         # dist/libvmx.so for the PLAY (zig, aarch64, glibc 2.28)
vmx/build-libvmx.sh host    # vmx/.build/ for this machine, which the decode tests need
./build.sh                  # vet, test, check the tab script, then dist/bdomt-linux-arm64 (zig)
go test ./...               # on the host; the decode tests skip without a host libvmx
```

libvmx's source is fetched into `vmx/.src` at the commit pinned in [vmx/UPSTREAM](vmx/UPSTREAM),
not vendored, so what changed is one patch you can read. CI builds the host copy before running
the tests, so they decode rather than skip. `TestShowThroughGStreamer` runs the real pipelines
into a file, and skips on a machine without GStreamer's `rawvideoparse` and `rawaudioparse`.

## Running it by hand

```bash
bdomt --discover                           # list the OMT sources on the network
bdomt --receive "HOST (Name)" --seconds 5  # receive and decode, print what arrived, no display
bdomt --receive host:6400 --video-out f.nv12 --frames 10 --audio-out a.s16
bdomt --serve :8094                        # the receiver and its API
bdomt --patch-ui / --unpatch-ui / --restore-ui   # the OMT tab
```

`--display=none`, `--gst-video-sink=fakesink` and `--gst-audio-sink=fakesink` run the whole
service on a laptop without taking anything; `--dir`, `--ui-dir`, `--ui-base` and `--vmx` move
everything else.

## Not yet true: the hardware list

In the order they'd change the design if the answer is bad:

1. **1080p60 decode on the A53s.** Modelled, never run. *If bad:* 720p60 and 1080p30 are the
   promise, and the tab says so.
2. **`kmssink` scaling a 720p source onto a 1080p plane.** bdplay's playback relies on the same
   thing, but through a different element chain. *If bad:* match output to source becomes the
   default.
3. **The handover with this pipeline.** Inherited from bdplay, which proved it on hardware, but
   not run with `bdomt` itself, nor with a reboot while on air.
4. **Discovery from the PLAY.** Whether `PPApp`'s own mDNS holds port 5353 exclusively. The
   unicast query works without it, and a typed address works without either.
5. **How late the sound is**, and whether a small fixed delay on the video is worth adding.
6. **The session cookie reaching port 8094**: the same assumption bdts and bdgw make.

## Licence

MIT, see [LICENSE](LICENSE). Not affiliated with, endorsed by, or supported by BirdDog or vMix.

libvmx is © Open Media Transport Contributors, MIT. The patcher redistributes a build of it
with this folder's patch, and its licence travels with it as `LICENSE.libvmx`. The OMT protocol
was read from libomtnet, which is MIT too; nothing of it is built or shipped here.
