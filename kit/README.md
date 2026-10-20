# The release kit

`scripts/make-kit.sh` builds `birddog-play-patcher-kit-<version>.zip`, which each GitHub
release of this repo carries. It holds the files the page serves for packing, as the page serves
them, so Burrow can build PLAY packages without a browser: the page's assets come from its own
origin, and nothing else can get at them.

What's committed here is only what the kit adds: `NOTICE.md` (the notices and the source offer
the page shows) and `licenses/`. Everything else comes out of `public/assets/` as
`scripts/build-assets.sh` writes it. `kit/out/` is the build output and is gitignored.

```bash
scripts/make-kit.sh [outdir]   # default kit/out
node test/kit.mjs              # build it, check it, and pack the CI package from it
```

## The zip

Every entry is a deflated regular file with a unix mode, dated 2020-01-01. No directory
entries, no symlinks, no zip64. `kit.json` is first.

```
kit.json
assets/     public/assets/ exactly, minus support-footer-version.js (the page's own):
            manifest.json, update, probe.sh, the *-run.sh scripts, the binaries,
            and files/ when there is a rootfs overlay
NOTICES/    NOTICE.md (the source offer), birddog-play-patcher-LICENSE,
            fuse-exfat-COPYING, libfuse-COPYING.LIB, pdfium/*
```

`assets/manifest.json` is build-assets.sh's, unchanged: one `{ size, sha256 }` per asset (null
when a binary was missing, which a release kit never allows), plus `generated`, `source` and
`rootfsOverlay`. Where each asset goes in a `.fw` is `public/app.js`'s business, and its table is
`PACKAGE` in `public/fw.js`. Burrow packs from `assets/` the same way.

Tailscale and MediaMTX are never in the kit, and neither is anything from a vendor firmware
file.

## kit.json

```json
{
  "kit": 1,
  "version": "<package.json version>",
  "commit": "<40-hex commit the kit was built from>",
  "mediamtx": {
    "version": "<MEDIAMTX_VERSION in src/worker.js>",
    "file": "mediamtx_<version>_linux_arm64.tar.gz",
    "base": "https://github.com/bluenviron/mediamtx/releases/download/",
    "checksums": "checksums.sha256"
  },
  "tailscale": {
    "index": "https://pkgs.tailscale.com/stable/?mode=json",
    "base": "https://pkgs.tailscale.com/stable/",
    "arch": "arm64"
  },
  "reservedModuleNames": ["<RESERVED_MODULE_NAMES in public/fw.js>"],
  "moduleMaxBytes": "<MODULE_MAX_BYTES in public/fw.js>"
}
```

`scripts/kit.mjs` reads every value from those files, and `test/kit.mjs` checks them against
the same files. The MediaMTX tarball is `base + version + "/" + file`, checked against its line
in `base + version + "/" + checksums`. The Tailscale tarball is `base` plus `Tarballs.<arch>`
from the index, checked against `<that URL>.sha256`.

`test/kit.mjs` packs the CI package again from the kit's `assets/`, following `PACKAGE` as
`app.js` does, and requires the tar to be byte-identical to the one `test/build-package.mjs`
builds. The gzip layer differs between zlib builds, so compare tars, never `.fw` files.
