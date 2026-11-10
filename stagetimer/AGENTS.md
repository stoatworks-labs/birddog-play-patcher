# AGENTS.md — bd-play-stagetimer

A PLAY Patcher module that puts a Stagetimer room on a BirdDog PLAY's HDMI
output (RK3328, 4× Cortex-A53, stock Debian 10). `bdstage` (Go, static, this
repo) reads the room over Stagetimer's API v1, draws the screens itself and
pipes NV12 frames into `gst-launch … ! kmssink`. Start with `README.md`; this
file is the rules.

## Where this sits

Sibling of `bdcam`, `bdts`, `bd-play-usb-player`, `bd-play-stream-gateway` and
`bd-play-signage` under `~/Projects/birddog-play`. The module contract is the
patcher's `module-template/`. The display handover, the session gate and the
tab patch are bdomt's (`birddog-play-patcher/omt/`), which took them from
bdplay, bdgw and bdts; when one of those learns something on hardware, carry it
over. The reverse engineering behind every claim about the PLAY lives in the
private `birddog-re`; never copy key material, firmware or BirdDog's web UI
source into this repo (`testdata/firmware/` is gitignored for exactly that).

## Hard rules

1. **Anything bdstage stops, it starts again.** `BirdDogRunner` on Stop, on
   SIGTERM, on giving up a failing output. `/run/bdstage-took-display` is
   written before the stop, and the unit's `ExecStopPost` restarts the decoder
   when it finds it — the one exit bdstage cannot clean up after is SIGKILL.
   `reset-failed` before `start`: PPApp aborts in its own teardown often enough
   to rate-limit the unit (`display.go`).
2. **Never take the display while the updater runs.** It stops BirdDogRunner,
   runs the installer and starts it again at the end. `resume` waits for PPApp
   to be up before taking over, and gives up after three tries in ten minutes.
3. **Stop gst-launch before giving the display back.** PPApp cannot become DRM
   master while kmssink still is (`screen.go`, the failure path).
4. **The API key never leaves the PLAY.** Authorization header, never a query
   string; never logged; never returned by the API (the tab gets
   `api_key_hint`, the last four characters); `config.json` is 0600; the open
   `/api/status` names neither the room nor the key.
   `TestAPIConfigKeepsTheKeyPrivate`.
5. **Read only.** bdstage never calls an endpoint that changes the room, and
   never emits on the socket, though the key would allow both.
6. **The socket first; HTTP polling is the fallback.** The key's budget is 100
   requests a minute and is shared with whatever else uses it; polling costs
   about 56. The rundown is re-read only after `timers_changed` (debounced) and
   every five minutes.
7. **Redraw only on change.** The loop compares `Model.Key()`; every change is a
   3 MB NV12 frame down a pipe on four A53s. Anything time-based in the model
   must change at most once a second — the progress bar follows the seconds on
   screen, not the clock. `TestModelKeyChangesOncePerSecond`.
8. **kmssink takes NV12 only**, BT.709 limited range (`nv12.go`), rows padded
   to four bytes as `rawvideoparse` assumes. `TestToNV12Colours`.
9. **The birdUI tab's template edit contains no template action.** A template
   birdUI cannot parse takes the firmware upload page down with it.
   `TestPatchAddsNoTemplateActions`, `TestPatchedTemplateStillParses`; copy a
   device's `videoset.html` into `testdata/firmware/videoset.<version>.html` to
   test the genuine markup.
10. **The mock follows Stagetimer, never the other way round.** Where
    `internal/mockstage` quotes the real server it says so, with the date;
    when the real server is seen to differ, change the mock and the code.
11. **Never claim anything about the PLAY that has not run on one.** The
    README's verification table is the authority. Chroot-on-a-VM results are
    evidence about the userland and none about the RK3328.

Port **8096** (8090–8095 are taken by the patcher's modules and signage).

## Toolchain

```bash
./build.sh                       # gofmt, vet (host and arm64), tests, tab syntax, aarch64 build, check.sh, .tgz
go test -race ./...
go run ./tools/mockstage         # a mock Stagetimer; see README
sudo tools/verify-stock.sh …     # the video pipeline in the factory rootfs, on an aarch64 Linux host
```

## Layout

```
main.go          CLI: --serve, --patch-ui/--unpatch-ui/--restore-ui, --probe, --demo
stagetimer.go    API v1 over HTTP: types, the client, what counts as refused
socketio.go      just enough Socket.IO v4 / Engine.IO 4 over a WebSocket, receive only
feed.go          the room's state kept current: socket, HTTP fallback, clock offset
clock.go         countdown maths, phases, appearances, the agenda's start times
model.go         what the screen shows, as data; its Key gates redraws
render.go        drawing a Model; text.go the glyph cache and text fitting
nv12.go          RGBA to NV12
sink.go          gst-launch + kmssink (and a PNG sink off a PLAY)
display.go       the handover from PPApp
screen.go        feed + renderer + sink + handover, and the tab's preview
config.go        config.json
serve.go         the tab's API; auth.go the birdUI session gate
patch.go         the Stagetimer tab on videoset.html; web/stagetimer.js the tab
demo.go          sample screens for --demo and the tests
internal/mockstage, tools/mockstage   a mock Stagetimer
install, uninstall, module.conf, payload/run.sh   the module
```
