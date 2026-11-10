# Notes

What was learned building bd-play-stagetimer, and what is still open. Dated
where it is about someone else's system, because those change.

## Stagetimer's API (as of 2026-11-10)

Documented at <https://stagetimer.io/docs/api-v1/>. Every endpoint is a GET,
the changing ones included; authentication is the room's API key, as a Bearer
header or an `api_key` query parameter, plus `room_id`. The budget is 100
requests a minute per key. bdstage uses `test_auth`, `get_status`, `get_room`,
`get_all_timers` and `get_all_messages`, nothing else.

There is no countdown in the API. `get_status` and the socket's
`playback_status` give `start`, `finish` and `pause` as Unix milliseconds on
the server's clock, and the client counts: remaining is `finish − now` while
running, `finish − pause` while stopped. A countdown reads whole seconds
rounded up, 0:00 for the second at zero, then the overtime with a minus.

The real-time feed is Socket.IO v4 at `/v1/socket.io`. The polling handshake
answered `0{"sid":…,"upgrades":["websocket"],"pingInterval":25000,"pingTimeout":20000,"maxPayload":1000000}`.
bdstage skips polling and opens the WebSocket directly
(`?EIO=4&transport=websocket`), which avoids the load balancer's sticky cookie;
api.stagetimer.io accepted that. The auth object goes in the CONNECT packet:
`40{"room_id":…,"api_key":…}`.

Seen from the real server, with a made-up room and key:

| | |
|---|---|
| HTTP, wrong key in the header | `400 {"ok":false,"message":"authorization: Not a valid Bearer token"}` — so a 400 has to count as *refused*; the first version only did that when the message said "key" |
| Socket, made-up room | `44{"message":"Access Denied: Room not found"}` |

Not yet seen, because there is no real room to read: the events' payloads, what
the socket says to a real room with a wrong key, the room ID format, and these
specifically —

- **`focus` on a message.** Not in the documented message object. `show_message`
  takes `focus=true` and the Viewer then shows the message full screen, so
  bdstage honours `focus` if the server sends it. If it never does, focus
  messages are drawn as ordinary ones.
- **`server_time`** in the playback status: if it is the server's clock when
  the status was sent, it is the best clock-offset sample there is. If it is
  when the status last *changed*, it is useless for that. bdstage only trusts it
  while it agrees with the HTTP `Date` header to within 1.5 s.
- **`flash`'s payload** (`count`), and whether `timers_changed` fires on every
  rundown edit.

`STAGETIMER_API_KEY=… bdstage --probe --room <id>` prints what a room sends for
15 s; the first run against a real room should settle all of these.

## Design

- **Draw it ourselves rather than show Stagetimer's Viewer page.** The API
  gives data, not pictures, and the PLAY has no browser. A browser runtime on
  the PLAY exists (WebLinked, as bd-play-signage uses) but is 340 MB unpacked
  and renders on four A53s with no GPU; a Go renderer that redraws once a second
  is 9 MB and costs almost nothing. The price is that logos, custom fonts and
  colours are not reproduced.
- **Redraw only on change**, by comparing the model, not on a timer. The first
  end-to-end run drew four frames a second for a five-minute timer — the
  progress bar in thousandths moved every 0.3 s — and that is now pinned to the
  seconds on screen (36 frames instead of 95 over the same 50 s).
- **The countdown runs locally** between events, from the last status and the
  clock offset, so a dropped connection does not freeze the screen. After four
  seconds without the socket (or ten without a successful poll) a small
  "Reconnecting" mark says it is not being confirmed.
- **TLS roots are compiled in** (`golang.org/x/crypto/x509roots/fallback`), used
  only if the PLAY has none; the time zone database likewise (`time/tzdata`).

## Verification log

- 2026-11-10, laptop: `go test -race ./...` (45 tests); an end-to-end run of
  the daemon against `tools/mockstage -script` with the PNG sink, frames looked
  at; the tab driven in a browser against the daemon in a page with the stock
  AV Setup structure and birdUI's own stylesheets (local only). That found the
  key field emptied by a redraw during TEST, so TEST then APPLY would have
  saved without the key — fixed — and birdUI styling every `<label>` as a
  toggle button and leaving password fields unstyled — worked around.
- 2026-11-10, tart VM `wlos-build` (aarch64): `tools/verify-stock.sh` in a
  read-only overlay of the factory `PLAY_1.0.30.img` rootfs. GStreamer 1.14.4
  there has `fdsrc`, `rawvideoparse`, `queue` and `kmssink`. The pipeline took
  all 34 frames of the scripted show as 1080p NV12, and `videoconvert` (BT.709)
  gave back every palette colour within 4/255.
- 2026-11-10: the archive passes the patcher's own reader
  (`scripts/patcher-accepts.mjs`).

## On a PLAY

Nothing yet. In order:

1. Install through the patcher; the tab appears and the web UI still answers
   (the health check would roll it back otherwise).
2. `bdstage --probe` over SSH with a real room: TLS from the PLAY, the socket,
   the events' payloads (above).
3. SHOW ON HDMI: PPApp stops, the timer appears, at 1080p60 and 720p outputs.
   Colours: if yellow looks orange or white looks pink, the VOP reads NV12 as
   BT.601 and `nv12.go` changes.
4. STOP, `systemctl stop bd-stagetimer`, `kill -9` of bdstage, a reboot while
   showing, a firmware update while showing: PPApp comes back every time, and
   the timer comes back after the last two.
5. A 4K output: drawn at 1080p, does kmssink scale it?
6. `top` while a timer runs: how long a frame takes to draw and convert on the
   A53s.
7. The login gate against the real birdUI: cookie reaches :8096, a logged-out
   browser is refused, a unit with no password refuses everything.
8. With the USB player or the OMT receiver showing: SHOW is refused, not fought.
