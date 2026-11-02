# AGENTS.md — bdout

HDMI formats and an area of interest for the **BirdDog PLAY** (Rockchip RK3328, Debian 10
aarch64, glibc 2.28, kernel 4.4.194): `libbdout.so`, a C shim preloaded into the PLAY's own
decoder `PPApp`, and `bdout`, a static Go service with the Output tab's API. The shim is built by
zig against glibc 2.28; the service cross-compiles with CGO off.
**Public**: `output/` of the birddog-play-patcher repo.

Start with [`README.md`](README.md) for the design and the hardware list. This file is the
operating rules.

## Where this sits

`output/` is the canonical home of `bdout`, beside `player/` (bdplay), `converter/` (bdcam),
`gateway/` (bdgw), `omt/` (bdomt) and `tailscale-ui/` (bdts). Unlike those, it never takes the
display: it changes what `PPApp` sees and does from inside it. `birddog-re`, the private research
repo, must never have key material or vendor firmware copied out of it into here.

The patcher serves the committed `dist/bdout-linux-arm64` and `dist/libbdout.so`; `./build.sh`
rebuilds both. `birddog-re`'s `tools/fwbuild` takes them from a patcher checkout (`BDOUT=`,
`LIBBDOUT=` override, or `PATCHER=` points at the checkout). The binary is `bdout`, the directory
`/userdata/bd-out`, the unit `bd-out`, the drop-in `BirdDogRunner.service.d/bd-out.conf`, the
port 8095. `output` and `out` are in `RESERVED_MODULE_NAMES` in `public/fw.js` and in fwbuild's
copy.

## Hard rules

1. **When in doubt, the shim passes through.** A missing, empty or unparseable `shim.conf`, a
   framebuffer it didn't see created, a plane the kernel refuses with our rectangles: `PPApp`
   gets exactly the call it made. A change that makes the shim fail closed is a dark-HDMI bug.
2. **Never a pixel clock outside the PHY's table.** `BDOUT_CLOCKS_KHZ` in `shim/bdout.h` and
   `ClocksKHz` in `timings.go` are the same list (`TestClockListsAgree` parses the header), and
   `Mode.Check` is `bdout_mode_check`. Anything else is a dark screen with no error.
3. **A format is tried, not set.** Only `Keep` writes a forced format to `config.json`, and the
   unit's `ExecStartPre` writes `shim.conf` from `config.json`, so a power cut mid-try boots on
   the kept format. After `bdout` itself restarts mid-try, `Boot` compares the shim's `mode=`
   (what `PPApp` was last handed, set only in `drmModeGetConnector`) with the kept format.
   Keep `mode=` meaning exactly that.
4. **The shim never links libdrm, and exports only the four wrappers.** It is preloaded into
   `birddog-runner`'s bash and everything it runs; `dlsym(RTLD_NEXT)` finds the real functions
   when `PPApp` first calls them. `build.sh` checks the exports.
5. **Nothing per frame touches the disk.** `shim.conf` is looked at no more than twice a second
   (a `stat`, and a read only when it changed); the status file is written only when what it
   says changes.
6. **One parser, one set of sums.** `shim/core.c` is the only `shim.conf` parser; `bdout` writes
   nothing it would refuse (`TestShimReadsWhatWeWrite` runs `shim/test/confcheck.c`). The AOI
   arithmetic is in `bdout_apply_aoi` and in `aoi.go`'s `Preview`; change both together, and
   `TestPreviewMatchesTheShim` holds them to each other.
7. **Restart `BirdDogRunner` through systemctl, never by killing `PPApp`**: `reset-failed`, then
   `try-restart` (never `start`: stopped means another module has HDMI, or someone meant it),
   then wait for a new `PPApp` pid. bdplay learned each of those on hardware.
8. **The template edit must never contain a template action**, and it anchors where bdcam, bdgw
   and bdomt anchor. `patch_test.go` checks both, and coexistence in every install order.
9. **Keep the auth gate differential.** Only `/api/status` and `/api/formats` are open, and they
   carry nothing a stranger could use. CORS echoes the origin, never `*`.
10. **Build only through `./build.sh`.** It pins the shim to glibc 2.28 through zig. A shim
    built for a newer glibc fails to load into `PPApp`, and says so only in the journal.

## Toolchain

```bash
go test ./...      # timings, shim.conf against the shim's parser, AOI against the shim's sums, runner, API, patch
./build.sh         # host core_test, the above, the tab's syntax, then dist/ (zig, glibc 2.28)
CC="zig cc -target aarch64-linux-gnu.2.28" NO_RUN=1 shim/test/run-linux.sh OUT   # the shim's end-to-end test, built
shim/test/run-linux.sh --run OUT                                                  # run on aarch64 Linux
```

## Layout

```
main.go          CLI: --serve, --write-conf, --reset, --fit, --formats, --patch-ui/--unpatch-ui/--restore-ui
timings.go       the PHY's clocks, Mode and its checks, the standard formats, Fit for custom sizes
config.go        config.json: the format and the AOI, validated and written atomically
shimconf.go      shim.conf out, /run/bdout-shim.status in
aoi.go           the shim's AOI sums in Go, for the tab's preview
runner.go        trying, keeping and reverting formats; applying the AOI; Boot
decoder.go       restarting BirdDogRunner and waiting for PPApp
screen.go        the HDMI connector from sysfs and debugfs: modes, EDID name and preferred mode
auth.go          the birdUI session gate (bdgw's, unchanged in reasoning)
serve.go         the API
patch.go         the videoset.html tab: markers, anchors, backup
web/output.js    the tab, embedded and written to the web UI's static dir
shim/bdout.h     the shim's pure half: libdrm's structures, the clocks, conf and AOI
shim/core.c      parsing shim.conf, checking modes, the AOI sums; no I/O, no libdrm
shim/shim.c      the four wrappers, the conf re-read, the status file
shim/test/       core_test (host), confcheck (Go tests), harness + fakedrm (Linux end to end)
dist/            COMMITTED: bdout-linux-arm64 and libbdout.so, what the patcher ships
```

## Not yet true

**Nothing here has run on a PLAY.** The README's hardware list is the outstanding work, and
whether the shim loads in `PPApp` at all is the item that decides whether the design stands.
Until it's done, every mention of this module says beta.

## Notes

`docs/NOTES.md` carries this module's working notes: status, decisions already made, and the
traps that have actually bitten. Read it before changing anything non-obvious.
