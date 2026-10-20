# Notes

Working notes for this repo: status, decisions, and the traps that have actually bitten.
Migrated out of Claude Code's memory on 2026-08-24, so they are written in the first
person and dated by when each thing was learned — that date is usually the useful part.

Cross-cutting notes that are not specific to this repo live in
[fleet-notes](https://github.com/stoatworks-labs/fleet-notes).

*PUBLIC browser tool that builds BirdDog PLAY firmware packages — live, videoed, split out of the private birddog-re*

`~/Projects/birddog-play-patcher` — **PUBLIC** (github.com/stoatworks-labs/birddog-play-patcher),
**LIVE at birddog-play-patcher.stoatworks-labs.com**, built 2026-08-12. A static page that
assembles an installable `.fw` for a BirdDog PLAY in the browser: SSH key, Tailscale, NDI KVM.

**Why it can be public when [birddog re](https://github.com/stoatworks-labs/birddog-re/blob/main/docs/NOTES.md) (`birddog-re`) cannot:** a valid package needs neither the
user's firmware file nor the AES key — the stock updater runs `./update` as root with no
signature check, so it is just a gzip'd tar. **No upload, no secret, no server work.** Adding
any `bdpff` decrypt path would end that; the test asserts a built package contains no reference
to the vendor payload, as a policy check.

- The **only** server code is a Worker proxying `pkgs.tailscale.com`, which sends no ACAO.
  Deliberately **not** connected to Workers Builds, so pushing a branch cannot deploy to prod.
- `installer/` and `agent/` are DOWNSTREAM copies of `birddog-re`; `scripts/sync-from-re.sh`
  pulls them forward and exits 1 on drift. Fix upstream, never here.
- `public/assets/` is generated + gitignored, so every shipped file has one committed copy.
- Redistributing Tailscale's binaries inside the `.fw` is a **BSD-3 notice obligation** — it is
  on the page itself, not just ATTRIBUTIONS.md.
- `?demo=1` fills a fake key and builds. That is what the thumbnail shot and the video both use.

**USB media player option added 2026-08-13** ([bdplay](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/player/docs/NOTES.md) (`player/`)) — ships bdplay +
bdpdf/libpdfium + mount.exfat-fuse, ~13.7 MB, verified building a 6.0 MB package in-browser.
- **`mutool` must NEVER reach this repo.** AGPL v3: serving it is distribution and §13 reaches
  network users. `sync-from-re.sh`, `build-assets.sh` and `test/build-package.mjs` each refuse
  it independently. PDF here is **PDFium (BSD-3)** via bdpdf.
- **Cloudflare Workers static assets cap at 25 MiB PER FILE** — that alone rules mutool (37 MB)
  out, and the test now checks every asset against it, because exceeding it means the deploy
  silently has nothing to serve.
- fuse-exfat (GPL v2) + libfuse (LGPL v2.1) are copyleft, so **the page carries their source
  offer**, pointing at bdplay's build scripts — same obligation shape as the Tailscale BSD-3
  notice already on the page.
- **Local `wrangler` (4.116.0) is too old for this repo's `compatibility_date` 2026-08-12**, so
  `npx wrangler dev` fails with "newest date supported is 2026-08-06". The page is static apart
  from the Tailscale proxy, so serve `public/` with `python3 -m http.server` to test the UI.

**birdUI Tailscale panel added 2026-08-13** ([bdts](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/tailscale-ui/docs/NOTES.md) (`tailscale-ui/`)) — the page no longer
tells people to SSH in and run `tailscale up`; the device is signed in from its own web UI.
Ships whenever Tailscale is selected rather than as its own checkbox. Binary only
(`tailscale-ui/dist/`, 6.3 MB), MIT, source linked on the page.

**Published 2026-08-12:** YouTube `z7RWEk11Egc`, Instagram Reel `Db7RBPkD5TZ`.

**Custom modules added 2026-09-15 (v0.2.0).** A user's `.tgz` (module.conf + install) is read in
the browser by `readModule`, checked structurally, and written to `modules/<name>/`; `update`
runs each install as root in a subshell under `timeout 600`, logging and skipping a failure so
the BirdDogRunner restore always runs. Contract on `public/modules.html`; starting point at
github.com/stoatworks-labs/bd-play-module-template, now `module-template/` (its CI imports this
repo's fw.js to prove acceptance). Same `--module` contract in birddog-re's `build.sh`.
- The installer change had to land upstream first: birddog-re's `tools/fwbuild/payload/update`
  had been carrying the bdts panel change UNCOMMITTED since 2026-08-13 (the patcher shipped it
  from the working tree). Committed as-is there before the modules block went on top.
- `sync-from-re.sh` downgraded `agent/go.mod`/`go.sum` (Dependabot bumped purego here, not
  upstream) — restored by hand, then upstream aligned the same day (birddog-re 191311f: go 1.25,
  purego 0.11; vet/test/cross-build pass; dist binary deliberately not rebuilt).
- Every option row now links the repo its payload comes from; bdcam had never been linked from
  the page at all.
- Verified: browser-built tar sha256 == Node-built for the same inputs, and real tar + Python
  tarfile read the Node copy; the template's install ran twice under the modules loop in a fake
  root (idempotent, config preserved, uninstall reverses it). NOT yet run on a PLAY.

**Streaming gateway added 2026-09-15 (v0.3.0, beta, NOT run on a PLAY).** MediaMTX as a protocol
hub plus bdgw from [bd-play-stream-gateway](https://github.com/stoatworks-labs/birddog-play-patcher/tree/main/gateway)
(then its own repo, binary synced into `gateway/dist/`; now `gateway/` here); the picture reaches HDMI through PPApp's own SRT receiver on
loopback, via the three files the stock AV Setup page writes. Design note and the reasoning are in
that repo's README and NOTES.
- **MediaMTX is fetched, not committed:** 62 MB unpacked is over the 25 MiB asset cap, and GitHub's
  release host sends no CORS header, so `src/worker.js` gains `api/mediamtx/latest|tgz` — one
  pinned version (`MEDIAMTX_VERSION`), the digest read from the published `checksums.sha256`, the
  tarball streamed. The test reads the pin out of the Worker source so page and test cannot
  disagree; `MTX_TGZ=` builds offline. The pin must match bd-play-stream-gateway's `render.go`
  and birddog-re's `build.sh`.
- The tab anchors where bdcam's does, so `videoset.html` now carries up to three marker-wrapped
  patches (bdplay, bdcam, bdgw); coexistence in either order is tested in the gateway repo.
- ATTRIBUTIONS gained MediaMTX via the fleet registry (stoatworks-backend ba5eb1e detects
  `bluenviron/mediamtx/releases` in packaging code, the way Tailscale is detected).
- `converter/dist/bdcam-linux-arm64` rebuilt from bdcam 859ac90 (zig 0.16, glibc floor 2.17): the
  UVC tab gained a GATEWAY button that fills in `srt://127.0.0.1:8890?streamid=publish:cam`.

**Module repos merged in 2026-10-11.** bd-play-usb-player → `player/`, bdcam → `converter/`,
bd-play-stream-gateway → `gateway/`, bdts → `tailscale-ui/`, bd-play-module-template →
`module-template/` (not a replacement for `test/fixtures/module`, which is a separate, smaller
CI fixture). Each came in as a `git subtree`-style merge with its history (commit trailers
name the split, so `git subtree split --prefix=<dir>` recovers it); the old repos stay up,
unarchived, with a "moved" notice at the top of their READMEs.
- The committed `dist/` binaries were not touched or rebuilt: the hosted page serves the same
  bytes as before the merge. Each folder's `.gitignore` lost its `dist/` line, except
  `player/dist/mutool-linux-arm64` (AGPL, never committed, never served). `build-mutool.sh`
  still writes there, for birddog-re's `--with-play-pdf` — and `build-assets.sh` refuses to run
  while it exists, so move it out before building the site.
- `sync-from-re.sh` no longer copies module binaries from sibling checkouts; it syncs only the
  installer and bdkvm. birddog-re's `fwbuild` defaults now point at a patcher checkout's
  `<dir>/dist/`.
- Their CI moved into `ci.yml`: a vet/test/compile-check job per Go folder, the gateway's
  MediaMTX `--validate-conf` (pin read from `src/worker.js`, cross-checked against
  `gateway/render.go`), and the template against this checkout's `fw.js`. bdcam's compile check
  is `CGO_ENABLED=0` (purego's fake cgo), not the zig build that ships. No `go.work`, on purpose.
- `deploy.yml` now uses `paths` with negations, so source-only pushes under the Go folders or
  the template do not redeploy; a change to any `dist/` still does.
- **Follow-up, not done:** the modules copy code between them — the birdUI session check
  (`auth.go` in tailscale-ui/ and gateway/), the marker-wrapped atomic web-UI patch (`patch.go`
  in player/, converter/, gateway/, tailscale-ui/), the CORS/serve scaffolding (`serve.go`, and
  `api.go` in player/) and near-identical `build.sh` files. Now they share a repo that could be one internal package,
  but each patch.go is unit-tested against its own page and markers — extract with care.
- **Follow-up, not done:** `module-template/check.sh` and `module.conf` still list the reserved
  names without `gateway gw mtx`, which `RESERVED_MODULE_NAMES` in fw.js and birddog-re's
  `build.sh` both refuse.
- Their own `.github/` went (funding, issue templates, workflows), except
  `module-template/.github/workflows/ci.yml`: it is part of the template, for the author's own
  repo once they copy the folder out, and GitHub never runs a workflow below the root here.
- The bd-play-module-template repo is still a GitHub *template* repository, so "Use this
  template" there copies the old tree with its "moved" notice.

**Release kit for Burrow, 2026-10-20 (v0.3.0, the first tagged release).** Burrow v0.5.0 builds
PLAY packages natively, and can only download from GitHub. The page's assets are served from
its own origin, so `scripts/make-kit.sh` now packs them as `birddog-play-patcher-kit-<v>.zip`
(format in `kit/README.md`), released locally with `gh release create`.
- The first kit, published as v0.3.0's asset, had a `kit.json` describing every member. The
  Burrow session asked the same day for `public/assets/` as-is under `assets/`, with
  `NOTICES/` and a small `kit.json` of pins, because Burrow ports `app.js`'s packing instead.
  That is the shape now.
- The member table moved out of `app.js` into `PACKAGE`/`OPTIONS` in `fw.js`, so the page and the
  kit read one copy. Checked in Chrome: the old and new page built byte-identical `.fw` files
  for KVM + UVC + player (`03e8c7d1…`) and for every option with local Tailscale 1.104.1 and
  MediaMTX v1.21.0 tarballs (`52f77a8e…`, 79.1 MB).
- `test/build-package.mjs` now packs the UVC converter too; it never had, though the page does.
- `player/dist/libpdfium.so` is byte-identical to pdfium-binaries `chromium/7999`'s
  `pdfium-linux-arm64`, which is where `kit/licenses/pdfium/` comes from. `bdpdf/build.sh`
  fetches `latest`, so a rebuild changes that.
- **Not fixed, follow-up:** `RESERVED_MODULE_NAMES` lacks `tailscaled`, and the template names
  its unit `bd-<NAME>.service`, so a module called `tailscaled` would overwrite the Tailscale
  daemon's unit. birddog-re's list needs the same change.

Related: [compressionstream gzip engine variance](https://github.com/stoatworks-labs/fleet-notes/blob/main/notes/reference_compressionstream_gzip_engine_variance.md), **release workflow** (working-practice note, kept in Claude memory).
