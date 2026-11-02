# Notes

Working notes for `output/`: status, decisions, and the traps that have actually bitten. First
person, dated by when each thing was learned.

*bdout: custom HDMI formats and an area of interest for the BirdDog PLAY; part of the public
birddog-play-patcher repo; built 2026-11-02, NOT YET RUN ON A PLAY*

**Why a shim inside `PPApp` and not a display of our own (decided 2026-11-02).** bdplay and bdomt
take the screen, and while they have it the PLAY isn't a PLAY: no NDI, no OSD, no tally. Custom
formats and an area of interest are wanted *while* it's being a PLAY, so they have to happen to
`PPApp`'s own output. `PPApp` is closed and has no setting for either, but it draws through
libdrm, and libdrm is a shared library, so `LD_PRELOAD` gets between them.

- **Four calls are enough.** `PPApp` reads the HDMI connector's mode list once, at start, and
  picks the mode that matches the stream; after that every frame is one `drmModeSetPlane` with
  the whole NV12 buffer placed at the origin. The OSD is ARGB on another plane. So: a one-mode
  list forces the format, the framebuffer's fourcc (from `drmModeAddFB2`) tells the video from
  the OSD, and the video plane's rectangles are the area of interest.
- **The PHY's twenty clocks.** The RK3328's HDMI PHY in the 4.4.194 kernel has a pre-PLL table,
  and only rows whose TMDS clock equals the pixel clock are any use for RGB/YUV444 8-bit. An EDID
  mode on another clock is pruned by the kernel; a mode handed over directly, as the shim does,
  gets a VOP pixel clock of 0 and a dark screen with no error anywhere. So the clock list is a
  hard limit, checked in C and in Go, and custom sizes are fitted to it rather than asked for.
- **Custom sizes are reduced blanking on the smallest clock that covers them.** CVT-RB's
  minimums (80 pixels of horizontal blanking, 460 µs of vertical), then the blanking spread in
  proportion so the refresh lands within 0.05 %, preferring a fit within 0.005 % when there is
  one (59.94 wants it). The search is a grid, not a formula, because the clocks are a list.
- **The VOP scales at most 8:1 either way.** An area of interest that would need more is refused
  (the plane passes through whole) rather than handed to the kernel to refuse every frame.
- **Trial and revert.** A wrong format can leave the only screen dark, and the person looking at
  it may have no other way in. A monitor's "Keep these settings?" is the pattern everyone
  already knows, so that's what this does, with the kept format in `config.json` and the one
  being tried only in `shim.conf`.

**Built 2026-11-02.**

- **Millionths, rounded to the nearest pixel, not floored.** The AOI is stored as millionths of
  the picture and the screen so it survives a change of stream size. Floor lost pixels on the
  way back: a third of 1920 is 333333 millionths, which floors to 639. Nearest gives the typed
  pixel back for any picture up to 4096 wide, and the tab clamps a width so that x + w never
  passes a million. `TestPixelsSurviveTheRoundTrip` is the check, in both directions.
- **The shim's `mode=` says what `PPApp` was handed, not what `shim.conf` says.** The first
  version set it when the conf was read. Then a `bdout` crash mid-trial left `PPApp` on the
  format being tried while `ExecStartPre` had already put the kept one back in `shim.conf`, and
  `Boot` saw no change and didn't restart. Now `mode=` is written only in
  `drmModeGetConnector`, and `Boot` restarts when it doesn't match the kept format.
- **`--decoder=none` has no `PPApp` pid.** It reports -1, meaning "count any status file as
  current"; 0 would have meant the shim never looked loaded off a PLAY.
- **The tab redraws from state, never from the form.** Preset buttons set the crop and redrew,
  and the redraw read the old numbers back out of the form over the new ones. State is synced
  from the form on input events only.
- **Testing without a PLAY:** `shim/test/harness.c` plays `PPApp` against `fakedrm.c`, a fake
  libdrm, with the shim preloaded between them: forced modes, the AOI, the OSD left alone, a
  conf changed mid-stream, a broken conf passing through, the status file. 32 checks, run in a
  Debian 10 arm64 chroot (glibc 2.28) in a tart VM, on both a fresh build and the shipped
  stripped `dist/libbdout.so`. The shipped `bdout` ran there too: `--fit`, `--write-conf`, and
  `--serve` trying a format. No python3 in that chroot; bash's `/dev/tcp` does for HTTP. The tab
  was driven in a browser against `--sys` pointing at a fake `class/drm`.
- **On this Mac `cc` is a shell function**, so build scripts call `clang` by path (Go's
  `exec.LookPath("cc")` finds the real one).

**Hardware list (nothing done):** the shim loading in `PPApp` through the drop-in; `PPApp`
taking a one-mode list, and what it does on a hotplug; the plane rectangles `PPApp` really
passes (a 1088-row buffer?); what shows outside a placed area of interest; screens taking
custom and fitted formats; HDMI audio across a forced format; `Before=` ordering at boot; the
cookie reaching :8095.

Related: [bdomt](../../omt/docs/NOTES.md) (the tab, the gate and the decoder restarts),
[bdplay](../../player/docs/NOTES.md) (`BirdDogRunner`'s habits),
[the patcher](../../docs/NOTES.md).
