# bdout user guide

bdout lets the BirdDog PLAY send the HDMI format **you** choose, rather than the one it picks to
match the stream, and show **part of the picture** anywhere on the screen. The PLAY carries on
being a PLAY: NDI, the OSD, tally and birdUI all work as before.

> **Before you rely on this:** it has **not yet run on a PLAY.** Off the device, its shim has run
> inside a stand-in for the PLAY's decoder under the PLAY's own system libraries, and the service
> has tried, kept and reverted formats. Whether the PLAY's decoder lets the shim in at all is the
> first thing the hardware will answer. If you try it, I'd love to hear how it went, and the
> **Status** side of the tab says most of what I'd ask.
>
> This codebase was created with AI assistance, directed and reviewed by a human author.

---

## Installing

Tick **HDMI formats and area of interest** in the
[PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com), and leave **Add the Output tab
to the web UI** ticked unless you'd rather drive it on port 8095. Install the package the way
you'd install any PLAY firmware, through the web UI's System page. It adds about 6.5 MB.

The installer restarts the PLAY's decoder so the shim loads. If the tab later says the shim
isn't in the decoder, press **RESTART THE DECODER**.

---

## Using it

Open the PLAY's web UI, go to **AV Setup** and pick the **Output** tab.

### HDMI format

| Format | Does |
|---|---|
| **Follow the source** | What the PLAY does out of the box: picks from the screen's own list to match each stream. |
| **A standard format** | 720p, 1080p, 2160p and friends at the usual rates, VESA computer sizes, and a few fitted ones such as 1920×1200 and 3840×1080. |
| **A custom size** | Any width, height and refresh. bdout works out a timing on one of the clocks the PLAY can make. |
| **Exact timing** | Every number of the modeline, for a processor that wants its blanking just so. |

Under the choice it says what you'll get, *1920×1200 at 60.013 Hz on a 148.352 MHz pixel clock* and
the like, and warns you when the screen doesn't list that size itself. **USE THE SCREEN'S OWN**
fills in the screen's preferred size.

Press **TRY THIS FORMAT**. The PLAY's decoder restarts (a couple of seconds of black), and a
banner counts down from 30 seconds:

- **KEEP IT** if the picture is right.
- **GO BACK NOW** if it isn't.
- Do nothing, which is what happens when the screen has gone dark and you can't see the banner,
  and the last format you kept comes back on its own.

The PLAY can only make twenty pixel clocks, so not every timing exists. A custom size always gets
the nearest thing that does, and the exact refresh is shown before you try it. Anything over
340 MHz (most of 4K) needs a screen and cable that do HDMI 2.0, and the tab says so.

Picking **Follow the source** again takes effect without a countdown, since it's how the PLAY
came.

### Area of interest

Tick **Show part of the picture**, then:

1. **Measured on a picture of**: the size of the sender's canvas, the numbers you're cutting from.
   **USE 1920×1080** (or whatever's arriving) fills it in from the stream on screen. A stream of
   another size gets the same proportions.
2. **Show this part**: X, Y, width and height of the bit you want. **WHOLE**, **LEFT HALF**,
   **RIGHT HALF**, **TOP HALF** and **BOTTOM HALF** do the obvious. For a video wall, say how many
   screens across and down and which one this is (column and row, from 1 at the top left), and
   press **SET**.
3. **Put it**: over the whole screen, or untick that and give X, Y, width and height on the
   screen.
4. **Stretch to fit**, or **Keep the shape** with bars where it doesn't fit.

The drawing shows the crop on the picture and where it lands on the screen. **APPLY** puts it
on the screen within half a second, with no restart and no countdown, since it can't leave you
in the dark. The OSD stays where it is; only the NDI picture moves.

The PLAY can scale at most eight times up or down, so a tiny crop blown up to a whole 4K screen
is refused, and the whole picture shows instead.

### Status

The right-hand side says what's going on: the format kept and any being tried, whether the shim
is in the PLAY's decoder and whether it liked its settings, the format it last handed the
decoder, the screen and the name in its EDID, the size of the picture being shown, and **SHOW
LOG**.

---

## When something goes wrong

**The screen went dark after TRY THIS FORMAT.** Wait. It comes back to the kept format within 30
seconds, plus a few for the restart.

**It's dark and stays dark**, at boot, say, after a format was kept that the screen has since
stopped liking (a different screen, a different cable). Over SSH (port 9031):

```bash
/userdata/bd-out/bdout --reset && systemctl restart BirdDogRunner
```

That puts the PLAY back to following the source, with no area of interest. Untick the module in
the patcher and reinstall if you'd rather it went altogether.

**The tab says the shim isn't in the decoder.** Press **RESTART THE DECODER**. If it still says
so, the log and `journalctl -u BirdDogRunner` will say why, and I'd very much like to know.

**The tab says the PLAY's decoder isn't running.** Another module, the OMT receiver or the USB
media player, has the screen. Formats take when the PLAY's decoder starts again.

**The tab says the device has no birdUI password set.** bdout won't take orders while birdUI has
no password, because then anyone on the network could change your HDMI format. Set one in
Password Settings on the System page.

---

## Removing it

Over SSH:

```bash
/userdata/bd-out/bdout --unpatch-ui
systemctl restart BirdDogWebUI
rm /etc/systemd/system/BirdDogRunner.service.d/bd-out.conf
systemctl daemon-reload
systemctl disable --now bd-out
systemctl try-restart BirdDogRunner
rm -rf /userdata/bd-out /etc/systemd/system/bd-out.service
```

The copy of the AV Setup page as it was before the tab went in stays at
`/srv/birddog-web-ui/videoset.html.bdout-stock`.
