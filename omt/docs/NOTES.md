# Notes

Working notes for `omt/`: status, decisions, and the traps that have actually bitten. First
person, dated by when each thing was learned.

*bdomt: an OMT-to-HDMI receiver for the BirdDog PLAY; part of the public birddog-play-patcher
repo; built 2026-10-30, NOT YET RUN ON A PLAY*

**Why direct DRM and not the PLAY's own decoder (decided 2026-10-26, in the spike).** `PPApp`
decodes NDI, SRT and RTSP, and nothing of OMT. The alternative to taking the display was to
re-encode OMT to H.264 on the VEPU and feed `PPApp` SRT, as the gateway does for its protocols.
That caps at 720p30 to 1080p30, has no bitrate control and adds 200 to 300 ms, so I took the
display instead, the bdplay way.

- **No stock libomt.** It's .NET, it decodes on two threads at 1080p, and on Linux it browses
  through Avahi, which the PLAY doesn't run: with no libavahi it returned a NULL receiver, and
  with the library but no daemon it hit SIGABRT. The receiver, the framing and the mDNS
  querier here are Go, written against libomtnet's source (pinned in `vmx/UPSTREAM`, read
  only).
- **The VOP takes NV12, NV16 and NV24, not UYVY, and the RGA2-Lite1 can't read packed 4:2:2.**
  The spike patched in `VMX_DecodeNV16`; this module uses NV12 instead, because GStreamer
  1.14's `kmssink` on the PLAY only negotiates NV12. The patch converts per row from libvmx's
  own 4:2:2 planes, so it costs a vertical chroma average and no second pass.
- **The modelled A53 figures** (valgrind instruction counts at IPC 1.0 / 0.7 / 0.55, 1.392 GHz):
  1080p30 standard quality about 1.3 of 4 cores, 720p60 about 1.4, 1080p60 low quality about 1.7,
  1080p60 standard quality camera content about 2.6 to 2.8 for decode alone. 4K is out.
- **The sender's quality is the highest any receiver asks for**, so the PLAY can suggest *Low*
  and nothing more. The tab offers it anyway, since a single-receiver sender does listen.

**Built 2026-10-30.**

- **Two TCP connections per source, as libomt opens them.** The video one sends Metadata,
  Video, Quality and Tally subscriptions; the audio one sends Audio. A sender that sends
  `OMTRedirect` gets followed; an empty `NewAddress` means go back to the original.
- **The control strings are compared byte for byte at the sender.** `Program==` is libomtnet's
  typo and it stays, and there's no NUL on the end, though `OMTInfo` from a sender does carry
  one, so incoming metadata is trimmed.
- **Discovery has two routes** because `PPApp`'s NDI may already hold port 5353: a legacy
  unicast query from an ephemeral port (RFC 6762 §6.7) needs no shared socket, and a multicast
  query from 5353 is added when the bind is allowed. Tested with a builder of real mDNS answers,
  and against a real libomt sender on the LAN, found by name.
- **Size changes aren't failures.** A sender changing resolution restarts the video pipeline,
  but that doesn't count towards the five-in-thirty-seconds failure budget or the restart
  counter the tab shows. The first version counted them, and the tab said "restarted 2×" for a
  source that had done nothing wrong.
- **Resume waits for `PPApp`.** During an install, `bd-omt` starts before `BirdDogRunner` is
  back; a resume then would take the display from a decoder that hasn't started, and the
  installer's restore would take it back. So resume waits up to 60 s for `PPApp` and gives up
  with a warning if it never appears.
- **Testing without a PLAY:** `--display=none` with `fakesink` runs the whole service on a Mac.
  The GStreamer pipelines were run under GStreamer 1.14.4 in a Debian 10 arm64 chroot (a tart
  VM, with `gstreamer1.0-plugins-bad` for `rawvideoparse` and `rawaudioparse`, both in its
  `rawparse` plugin), and the shipped binary and `libvmx.so` loaded and decoded there. No
  python3 in that chroot; parse output on the host.

**Hardware list (nothing done):** 1080p60 decode headroom; `kmssink` scaling 720p onto a 1080p
plane; the handover and the marker with this pipeline, including a reboot while on air;
discovery from the PLAY with `PPApp` running; how late the sound is; the cookie reaching :8094.

Related: [bdplay](../../player/docs/NOTES.md) (the handover),
[bdgw](../../gateway/docs/NOTES.md) (the tab and the gate),
[the patcher](../../docs/NOTES.md).
