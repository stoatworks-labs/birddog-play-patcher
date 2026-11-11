# Stagetimer user guide

The Stagetimer option puts a [stagetimer.io](https://stagetimer.io) room on the BirdDog PLAY's
HDMI output: the speaker's countdown, the rundown, both side by side, or the time of day, live
from the room. Run the show from Stagetimer's Controller as usual and the PLAY's screen follows
it, messages, flash and blackout included.

> **Before you rely on this:** it has **not yet run on a PLAY.** Off the device it has talked to
> Stagetimer's real API (only far enough to be told a made-up room doesn't exist), and its
> video has gone through the PLAY's own GStreamer version inside the PLAY's factory system
> image. Whether the handover from the PLAY's decoder, the colours and the speed are right on
> the real hardware is still open. If you try it, I'd love to hear how it went.
>
> This codebase was created with AI assistance, directed and reviewed by a human author. It is
> not affiliated with Stagetimer.

---

## What you need

- A Stagetimer room, and its **API key**. In Stagetimer, open the room's **Controller**, then
  the menu → **API**: that page shows the room ID and the key.
- The PLAY on a network that reaches `api.stagetimer.io` over HTTPS. If the venue blocks
  WebSockets it falls back to asking every two seconds, which still works.
- A password on the PLAY's web UI. Without one the Stagetimer tab can't be used, because the
  API key can start and stop the room's timers.

---

## Installing

Tick **Stagetimer** in the [PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com), and
leave **Add the Stagetimer tab to the web UI** ticked: the tab is where you enter the room, so
without it you'd have to drive the API on port 8096 by hand. Install the package the way you'd
install any PLAY firmware, through the web UI's System page. It adds about 4 MB to the package.

---

## Using it

Open the PLAY's web UI, go to **AV Setup** and pick the **Stagetimer** tab.

1. **Enter the room.** Paste the room ID and the API key, and press **TEST**. It checks them
   with Stagetimer without saving anything and says what it found.
2. **Choose the screen.** **Timer** is the countdown as Stagetimer's Viewer shows it; **Agenda**
   is the rundown, with the current item counting down; **Split** is both; **Clock** is the time
   of day in the room's time zone. Tick what you want on it: the title, the speaker, the
   progress bar with the wrap-up zones, what comes next, the clock in the corner, the
   Controller's messages. **Mirror** flips it for a teleprompter.
3. Press **APPLY** to save, then **SHOW ON HDMI**.

The preview on the tab shows what the screen shows, or what it would show before you press
**SHOW ON HDMI**, so you can set it up while the PLAY is still decoding NDI.

**While the timer is on HDMI, the PLAY's own decoder is stopped.** NDI decoding, the on-screen
display and tally are gone until you press **STOP**, at which point the decoder is started
again. After a reboot or a firmware update the timer comes back on its own; untick **put the
timer back on HDMI** if you'd rather it didn't.

What it follows from the room: the selected timer and its start, finish and pause; count-up,
time-of-day and hidden timers; the yellow and red wrap-up colours, and overtime with a plus
sign; messages, in their colour (a focus message fills the screen); **Flash**, which inverts
the screen; **Blackout**; and **On Air**. What it doesn't draw: the room's logo and Stagetimer's
custom fonts and colours. The room's **Time Formatting** and **Overtime** settings aren't in
Stagetimer's API either, so the PLAY always draws the defaults: a room set to a minus prefix,
or to stop at 0:00, still counts on as +0:01, +0:02… on HDMI.

If the PLAY loses Stagetimer, the countdown carries on from what it last knew, with a small
"Reconnecting" mark in the corner until the room is back.

---

## If something's wrong

- **TEST says the key or the room was refused.** Copy both again from the room's API page.
  A refused room or key is never retried on its own; fix it and press **TEST** again.
- **SHOW ON HDMI is refused.** Something else has the screen: the USB media player or the OMT
  receiver. Stop it first.
- **The tab is missing.** The installer puts the web UI back as it was if it doesn't come back
  healthy after the edit; `/tmp/bd-custom-install.log` on the PLAY says why.
- **SHOW LOG** on the tab shows what bdstage has been doing, without needing SSH.

---

## Removing it

Press **STOP** first, so the PLAY's decoder is started again. Then, over SSH (port 9031):

```bash
/userdata/bd-stage/bdstage --unpatch-ui && systemctl restart BirdDogWebUI
systemctl disable --now bd-stage
rm -rf /userdata/bd-stage /etc/systemd/system/bd-stage.service
```

That removes the room's API key from the PLAY too: it is kept only in
`/userdata/bd-stage/config.json`, readable by root alone.
