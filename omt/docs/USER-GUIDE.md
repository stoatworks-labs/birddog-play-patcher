# bdomt user guide

bdomt puts an **Open Media Transport** source on the BirdDog PLAY's HDMI output, with its sound.
Anything that sends OMT will do: vMix, the OMT tools, or anything built on libomt.

> **Before you rely on this:** it has **not yet run on a PLAY.** Off the device it has received a
> real OMT sender at 1080p60, found by name; its GStreamer pipelines have run under the PLAY's
> own GStreamer version (1.14.4, in a Debian 10 chroot); and the binary and codec it ships load
> and decode there. Whether four Cortex-A53s keep up with 1080p60 is modelled, not measured, so
> **720p is the safer bet** until someone has tried it on the hardware. If that's you, I'd love
> to hear how it went.
>
> This codebase was created with AI assistance, directed and reviewed by a human author.

---

## Installing

Tick **OMT receiver** in the [PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com),
and leave **Add the OMT tab to the web UI** ticked unless you'd rather drive it on port 8094.
Install the package the way you'd install any PLAY firmware, through the web UI's System page.
It adds about 7.5 MB to the package.

---

## Using it

Open the PLAY's web UI, go to **AV Setup** and pick the **OMT** tab.

1. **Pick a source.** The list fills with every OMT sender announcing itself on the network.
   **LOOK AGAIN** browses again. A sender on another subnet won't announce itself across the
   router, so type its address instead: `192.168.1.20:6400`, `omt://studio-pc:6400`, or just a
   host name, which assumes port 6400.
2. **Press SHOW ON HDMI.** The PLAY's own decoder stops, the screen goes black for a moment, and
   the source appears.
3. **Press STOP** when you're done. The PLAY's decoder starts again and you're back to NDI.

With a source showing, picking another and pressing **SWITCH** changes source without handing
the screen back in between.

**While a source is showing, the PLAY isn't a PLAY.** NDI, the OSD and tally are gone until you
press **STOP**, because the PLAY's decoder has to be stopped for anything else to draw on the
screen. The USB media player works the same way, and the two can't share the screen: stop one
before starting the other.

### Settings

| Setting | Does |
|---|---|
| **Ask for quality** | What to ask the sender for. A sender encodes at the highest quality any of its receivers asks for, so this is a request rather than an order. |
| **Sound** | Play the source's first two channels out of HDMI. |
| **Tally** | Tell the sender it's on program while it's shown, so its tally lights up. |
| **When it stops** | Ticked, the last picture stays up when the source stops; unticked, the screen goes black. Either way the receiver keeps trying to reconnect. |
| **Output** | Ticked, the HDMI output switches to the source's resolution. Unticked (the default), it stays as birdUI set it and the picture is scaled onto it, as the PLAY's own decoder does. |
| **After a restart** | If a source was showing when the PLAY lost power or restarted, put it back once the PLAY is up again. |
| **Decode threads** | How many cores decode the video. *libvmx's choice* leaves it to the codec; 4 uses them all. |

**APPLY** saves them. If a source is showing it restarts with the new settings, without handing
the screen back.

### Status

The right-hand side says what's going on: **Off** (the PLAY's own decoder has the screen),
**Connecting…** or **Connected, waiting for pictures**, **On HDMI**, or **Stopped by an error**.
Under it are the stream's size and frame rate, frames shown and dropped, CPU and temperature,
and **SHOW LOG**. When something won't show, the log is where the answer usually is.

---

## What it can and can't show

- **Progressive sources only.** An interlaced source is refused with a message saying so. Set
  the sender to progressive.
- **Up to 1080p60, probably.** 720p60 and 1080p30 should be comfortable. 1080p60 at the
  sender's low quality should be fine; at standard quality it's close, and only the hardware
  will say. 4K won't work.
- **The sound runs a little late.** The picture is shown as soon as it's decoded and the sound
  goes through a buffer, and they aren't lined up yet. Nobody has measured by how much.
- **Transparency is ignored.** HDMI has nowhere to put it.

---

## When something goes wrong

**The tab says it can't find any sources.** Make sure the sender and the PLAY are on the same
subnet, or type the sender's address. A sender behind a firewall needs its OMT ports open (they
start at 6400, one per source it sends).

**The tab says the device has no birdUI password set.** The receiver won't take orders while
birdUI has no password, because then anyone on the network could put anything on your screen.
Set one in Password Settings on the System page.

**The screen stayed black after STOP.** The PLAY's decoder should be back within a few seconds.
If it isn't, the log says why, and this brings it back over SSH (port 9031):

```bash
systemctl reset-failed BirdDogRunner && systemctl start BirdDogRunner
```

Restarting the PLAY does the same.

---

## Removing it

Press **STOP** on the tab first, so the PLAY's decoder is running, then over SSH:

```bash
systemctl disable --now bd-omt
/userdata/bd-omt/bdomt --unpatch-ui
systemctl restart BirdDogWebUI
rm -rf /userdata/bd-omt /etc/systemd/system/bd-omt.service
```

The copy of the AV Setup page as it was before the tab went in stays at
`/srv/birddog-web-ui/videoset.html.bdomt-stock`.
