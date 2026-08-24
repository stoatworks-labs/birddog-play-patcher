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

**USB media player option added 2026-08-13** ([bdplay](https://github.com/stoatworks-labs/bd-play-usb-player/blob/main/docs/NOTES.md) (`bd-play-usb-player`)) — ships bdplay +
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

**birdUI Tailscale panel added 2026-08-13** ([bdts](https://github.com/stoatworks-labs/bdts/blob/main/docs/NOTES.md) (`bdts`)) — the page no longer
tells people to SSH in and run `tailscale up`; the device is signed in from its own web UI.
Ships whenever Tailscale is selected rather than as its own checkbox. Binary only
(`tailscale-ui/dist/`, 6.3 MB), MIT, source linked on the page.

**Published 2026-08-12:** YouTube `z7RWEk11Egc`, Instagram Reel `Db7RBPkD5TZ`.

Related: [compressionstream gzip engine variance](https://github.com/stoatworks-labs/fleet-notes/blob/main/notes/reference_compressionstream_gzip_engine_variance.md), **release workflow** (working-practice note, kept in Claude memory).
