# bdout: HDMI formats and an area of interest for the BirdDog PLAY

> **AI-assisted project.** This codebase was created with [Claude Code](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author. **It has not yet run on a PLAY.** The shim
> has run inside a stand-in for `PPApp` under the PLAY's own glibc in a Debian 10 arm64 chroot,
> the service has tried, kept and reverted formats there, and the tab has been driven in a
> browser against a fake screen. The list at the end is what only the hardware can answer.

`bdout` gives a BirdDog PLAY two things BirdDog didn't: an HDMI format of your choosing, rather
than whichever one the PLAY picks from the screen's list to match the stream, and an **area of
interest**: part of the picture, shown anywhere on the screen. One PLAY per screen of a video
wall, a 1920×1200 projector that finally gets 1920×1200, a 3840×1080 LED processor fed the
canvas it actually wants, the left half of a stream on one screen and the right half on the
next.

It does both without taking the screen away from the PLAY's own decoder, so NDI, the OSD, tally
and everything birdUI does carry on as before. Untick it all and the PLAY is a PLAY again.

Installed by the [BirdDog PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com) as its
*HDMI formats and area of interest* option, or by `birddog-re`'s `fwbuild --with-output`. The
how-to is in [docs/USER-GUIDE.md](docs/USER-GUIDE.md).

## How it works

The PLAY's decoder, `PPApp`, is closed and takes no orders about formats or cropping. It does
make its display calls through libdrm, though, and four of those are all it takes:

```
                  ┌──────────────── PPApp (BirdDog's, untouched) ────────────────┐
 NDI ──▶ decode ──▶ drmModeGetConnector ─┐   drmModeAddFB2 / RmFB   drmModeSetPlane ─┼─▶ HDMI
                  └──────────────────────┼──────────────┼─────────────────┼───────┘
                                         ▼              ▼                 ▼
                            ┌──────────── libbdout.so (LD_PRELOAD) ────────────┐
                            │ one mode in the list   NV12 or not?   crop/place │
                            └───────────────────────▲──────────────────────────┘
                                                    │ shim.conf, re-read ≤ 2×/s
 Output tab ──https──▶ bdout --serve :8095 ─────────┘  (and /run/bdout-shim.status back)
```

- **The shim, `libbdout.so`**, is preloaded into `BirdDogRunner.service` by a systemd drop-in.
  It wraps four libdrm calls and passes everything else, and everything at all when `shim.conf`
  is missing or doesn't parse, straight through:
  - `drmModeGetConnector`: with a format forced, the HDMI connector's mode list becomes that one
    format, so `PPApp`'s own matcher has nothing else to pick.
  - `drmModeAddFB2` and `drmModeRmFB`: it remembers which framebuffers are YUV, which is how it
    tells the video from the OSD.
  - `drmModeSetPlane`: a YUV frame placed at the origin is the video, and with an area of
    interest on, its source rectangle becomes the crop and its destination the placement.
- **`bdout`**, the service, owns the settings (`config.json`), writes `shim.conf` from them,
  restarts the decoder when a format changes, and serves the Output tab's API.
- **A format change restarts `PPApp`**, because it reads the mode list when it starts. An area
  of interest doesn't: the shim picks it up within half a second, mid-stream.

### Formats

The RK3328's HDMI PHY makes twenty pixel clocks and no others: 27, 40, 59.341, 59.4, 65, 71,
74.176, 74.25, 83.5, 85.75, 88.75, 108, 119, 148.352, 148.5, 162, 296.703, 297, 593.407 and 594
MHz (its pre-PLL table in the PLAY's 4.4.194 kernel). A mode on any other clock doesn't fail, it
just leaves the screen dark, so nothing leaves `bdout` or the shim on one. Four kinds of format:

- **Follow the source**: the PLAY as BirdDog ships it.
- **A standard format**: CEA from 480p to 4096×2160p60, the VESA modes whose clocks the PHY has,
  and a few useful ones fitted to those clocks (1366×768, 1920×1200, 2560×1080, 3840×1080,
  1080×1920).
- **A custom size**: any width, height and refresh. `bdout` builds a reduced-blanking timing on
  the smallest clock that covers it, spreading the blanking so the refresh lands within 0.05 %
  (and within 0.005 % for the NTSC rates if it can). `bdout --fit 1920x1200@50` prints what a
  size gets.
- **Exact timing**: a modeline, for the processor that wants its blanking just so. Its clock
  must be one of the twenty.

**A new format is tried, not set.** The decoder restarts on it and the tab counts down: press
**KEEP IT** within 30 seconds or the last kept format comes back on its own, as a monitor's
"Keep these display settings?" does. Only a kept format is written to `config.json`, so a power
cut or a crash mid-try comes back up on the kept one too. Going back to following the source is
never a trial, since it's the PLAY as shipped.

### Area of interest

A rectangle of the picture (the crop) and a rectangle of the screen (where it goes), both kept as
millionths, so the same setting works whatever size the stream turns up at. The tab lets you
type them in pixels of the sender's canvas, and has presets for the halves and for one screen
of a wall of so many across and down. Stretch to fit, or keep the shape with bars.

The shim does the sums each frame in integer arithmetic: rounded to the nearest pixel, even
sizes and offsets for NV12, at least 16 pixels, and no more than the VOP's 8:1 scale either way.
`bdout` has a Go copy of the same sums for the tab's preview, and a test holds them to each
other. Only the NDI picture moves; the OSD stays where it is.

## What is installed

```
/userdata/bd-out/bdout          the service (6 MB, static Go)
/userdata/bd-out/libbdout.so    the shim (16 KB, glibc 2.17 floor)
/userdata/bd-out/config.json    the settings the tab edits; written on first start, kept across reinstalls
/userdata/bd-out/shim.conf      written by bdout from config.json; what the shim reads
/etc/systemd/system/bd-out.service
    ExecStartPre=-bdout --write-conf    so the decoder never starts on a format being tried
    ExecStart=bdout --serve :8095 --dir /userdata/bd-out
    Before=BirdDogRunner.service
/etc/systemd/system/BirdDogRunner.service.d/bd-out.conf
    Environment=LD_PRELOAD=/userdata/bd-out/libbdout.so
```

The shim says what it's doing in `/run/bdout-shim.status`: `PPApp`'s pid, whether `shim.conf`
parsed, the format it last handed `PPApp`, the area of interest and its geometry, and the size
of the picture `PPApp` last showed on what size of screen. The tab shows all of it.

The **Output** tab is added to `/srv/birddog-web-ui/videoset.html` with the same anchors the
UVC Converter, Streaming and OMT tabs use, so they coexist in whichever order they went in. The
edit is two marker-wrapped lines with no template action in them, and the page is backed up
first as `videoset.html.bdout-stock`.

## The tab

- **HDMI format**: follow the source, a standard format, a custom size or exact timing, with
  what it resolves to (size, refresh, pixel clock, the modeline) before you try it, and a
  warning when the screen doesn't list that size itself. **USE THE SCREEN'S OWN** fills in the
  screen's preferred mode from its EDID. **TRY THIS FORMAT** starts the trial; the banner that
  follows has **KEEP IT** and **GO BACK NOW**.
- **Area of interest**: on or off, the canvas you're measuring in, the crop, the presets, one
  screen of a wall, where it goes on the screen, stretch or keep the shape, and a drawing of the
  lot. **APPLY** takes it at once.
- **Status**: the kept format, what the shim says, the screen and its EDID name, the picture
  size `PPApp` is showing, and the log.

## The API

```
GET  /api/status    open: the runner, the screen, the shim's status, the decoder
GET  /api/formats   open: the standard formats and the PHY's clocks
POST /api/resolve   {"format":{…}} → the mode, its modeline and notes; changes nothing
POST /api/preview   {"aoi":{…},"picture":[w,h],"screen":[w,h]} → the plane the shim would set
POST /api/format    {"format":{…}}: try it (or just keep it, for the source or the same timing)
POST /api/keep      keep the format being tried
POST /api/revert    give it up now
POST /api/aoi       {"aoi":{…}}: save and apply
POST /api/restart   restart the PLAY's decoder (after an install, so the shim loads)
GET  /api/config    the settings
GET  /api/log       the last 200 log lines
```

Everything but the two open GETs needs a birdUI session, through the same gate as the other
modules: the tab sends the `BirdDogSession` cookie it already holds, and `bdout` asks the web UI
on loopback whether that cookie opens the System page. If the page opens without a cookie too,
the PLAY has no password set and the gate refuses rather than pretend it checked anything;
`--allow-unprotected` overrides that, and then anyone who can reach port 8095 can change your
HDMI format.

## Build

```bash
./build.sh            # shim unit tests, vet, Go tests, the tab's syntax, then dist/ (zig, glibc 2.28)
go test ./...         # on the host; builds shim/test/confcheck.c with the host's cc
```

The shim's end-to-end test (`shim/test/harness.c`, a stand-in for `PPApp` against a fake libdrm)
needs Linux. On a Mac:

```bash
CC="zig cc -target aarch64-linux-gnu.2.28" NO_RUN=1 shim/test/run-linux.sh OUT
shim/test/run-linux.sh --run OUT      # on aarch64 Linux; a Debian 10 arm64 chroot is closest
```

## Running it by hand

```bash
bdout --fit 3840x1080@50      # the timing a size gets on the PHY's clocks
bdout --formats               # the standard formats and their modelines
bdout --reset                 # back to following the source, no area of interest
bdout --write-conf            # shim.conf from config.json, as the unit does before each start
bdout --serve :8095           # the API
bdout --patch-ui / --unpatch-ui / --restore-ui   # the Output tab
```

`--decoder=none` and `--sys <dir>` (a fake `class/drm` and `kernel/debug/dri/0/summary`) run
the service on a laptop. The shim reads `BDOUT_CONF` and `BDOUT_STATUS` for its two files.

## Not yet true: the hardware list

In the order they'd change the design if the answer is bad:

1. **The shim loads in `PPApp`.** That the drop-in's `LD_PRELOAD` reaches it through
   `birddog-runner`'s `exec`, and that `PPApp` calls these four functions through the dynamic
   linker rather than its own copy of libdrm. *If bad:* the shim does nothing, the tab says so,
   and the design goes back to the drawing board.
2. **`PPApp` takes a one-mode list.** Its matcher might want the stream's own rate in the list,
   or re-read the list on a hotplug. *If bad:* the shim hands over the forced format first and
   the screen's own list after it.
3. **What `PPApp` passes to `drmModeSetPlane`.** The design assumes the whole picture from the
   origin of the buffer onto the whole screen. A 1080p stream in a 1088-row buffer would show as
   `picture=1920x1088` in the shim's status, and the crop would want to know.
4. **What shows outside a placed area of interest.** Black, the primary plane, or last frame's
   leftovers.
5. **Screens taking custom and fitted formats.** The timings are within spec on paper; LED
   processors and projectors have opinions.
6. **HDMI audio across a forced format.** The PLAY sends audio at the stream's rate, which a
   forced 50 Hz or 24 Hz format shouldn't care about, but nobody has listened.
7. **`Before=BirdDogRunner.service` at boot**, and the session cookie reaching port 8095: the
   same assumption the other modules make.

## Licence

MIT, see [LICENSE](LICENSE). Not affiliated with, endorsed by, or supported by BirdDog.

`shim/bdout.h` mirrors three structure layouts from libdrm's `xf86drmMode.h` (MIT), which are
part of libdrm's stable ABI. Nothing of libdrm is built or shipped here.
