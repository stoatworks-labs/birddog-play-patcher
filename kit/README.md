# The release kit

`scripts/make-kit.sh` builds `birddog-play-patcher-kit-<version>.zip`, which each GitHub
release of this repo carries from v0.3.0. It holds every file the page packs into a `.fw` that
isn't fetched from upstream, plus `kit.json`, which says where each one goes. Burrow builds PLAY
packages from it, because the page's assets are served from its own origin and nothing outside
a browser can get at them.

What's committed here is only what the kit adds: `NOTICE.md` (the notices and the source offer
the page shows) and `licenses/`. Everything else comes out of `public/assets/` as
`scripts/build-assets.sh` stages it. `kit/out/` is the build output and is gitignored.

```bash
scripts/make-kit.sh [outdir]   # default kit/out
node test/kit.mjs              # build it, check it, and rebuild the CI package from it
```

## The zip

Every entry is deflated (Burrow's zip reader has deflate and nothing else), carries a unix mode,
and is dated 2020-01-01. No directory entries, no zip64. `kit.json` is first.

```
kit.json
NOTICE.md
installer/   update, probe.sh, kvm-run.sh, cam-run.sh, cam-api-run.sh, gw-run.sh, gw-api-run.sh
bin/         bdts-linux-arm64, bdkvm-linux-arm64, bdcam-linux-arm64, bdplay-linux-arm64,
             bdpdf-linux-arm64, libpdfium.so, mount.exfat-fuse, bdgw-linux-arm64
licenses/    birddog-play-patcher-LICENSE, fuse-exfat-COPYING, libfuse-COPYING.LIB, pdfium/*
```

Tailscale and MediaMTX are never in it, and neither is anything from a vendor firmware file.

## kit.json

Modes are the permission bits as plain integers: 493 is 0755, 420 is 0644, 384 is 0600. A
field this list doesn't name may appear in a later kit, so ignore unknown fields; `schema`
changes only when an existing field changes meaning.

| Field | Meaning |
|---|---|
| `schema` | `1` |
| `name` | `birddog-play-patcher-kit` |
| `version` | this repo's `package.json` version, and the release tag without its `v` |
| `source.repo`, `source.commit`, `source.dirty` | where and from what the kit was built |
| `package.name` | the `.fw` file name, with `{tag}` for the cleaned tag |
| `package.tag` | `default` when the tag is empty; every match of the regex `unsafe` becomes `replace_with`. The installer sources build.conf as root, so this is not optional |
| `package.compression` | `gzip`, over the whole tar |
| `package.tar` | ustar; every header has `mtime`, `uid`, `gid`, `uname`, `gname` as given; names at most `name_max` bytes; directory entries as `dirs` describes, mode `dir_mode` |
| `package.members[]` | the whole package, in tar order. Each: `id`, `option` (null: always), `member` (path in the tar), `mode`, `required`, and one of `file` (an id in `files`), `fetch` + `take` (a key in `fetch`, and the file to take from that tarball) or `input` (`ssh-key`: the user's public key plus LF; `build.conf`) |
| `package.build_conf` | `member`, `keys` in order, and `format` |
| `package.modules` | the user's own modules: `flag`, `prefix` (each goes under `<prefix><name>/`), `after`, `name_pattern`, `version_pattern`, `max_bytes`, `reserved_names` |
| `options[]` | in pack order: `id` (`ssh`, `tailscale`, `kvm`, `uvc`, `player`, `gateway`), `label`, `flag` (the build.conf key it sets to 1; null for `ssh`, whose switch is the key file being there), `ui_flag`, `ui_choice` (whether the user picks the UI flag; Tailscale's is set whenever the option is), `default` (the page's), `files` (ids in `files`), `fetch`, `inputs` |
| `files[]` | `id`, `path` (in the zip), `size`, `sha256`, `mode`, `member`, `option`, `required` |
| `fetch.tailscale` | `index` (JSON), `version_field`, `file_field`, `version_pattern`, `tarball` and `sha256` URLs with `{file}` from `file_field`, `sha256_format`, `take` |
| `fetch.mediamtx` | the pinned `version`, `asset`, `url`, `checksums` URL, `checksums_format`, `take` |
| `notices[]` | `path`, `size`, `sha256`, `mode`, `covers` (file ids, or `all`) |

Building a package from it: walk `package.members` in order and skip a member whose option is
off. A member that isn't `required` is skipped when its source is absent (only the MediaMTX
`LICENSE`, in a kit with every file). Then add each module's files under `package.modules.prefix`.
A `ui_flag` is 1 only when its option is on, and `WITH_MODULES` is 1 exactly when there are
modules. `test/kit.mjs` does this with every option on and requires the tar to be byte-identical
to the one `test/build-package.mjs` builds. The gzip layer differs between zlib builds, so
compare tars, never `.fw` files.
