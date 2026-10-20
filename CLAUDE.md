# CLAUDE.md — BirdDog PLAY Patcher

Command reference. For the model, the invariants and the traps, read
[AGENTS.md](AGENTS.md) first.

## Commands

```bash
scripts/build-assets.sh          # assemble public/assets/ — run this first, it is gitignored
npx wrangler dev --port 8792     # serve the page + the tailscale proxy
node test/build-package.mjs      # build a package in Node, verify structure
node test/modules.mjs            # module archives written by the system tar must read and refuse right
scripts/make-kit.sh [outdir]     # the release kit Burrow builds packages from (kit/README.md)
node test/kit.mjs                # build the kit and rebuild the CI package from it (after build-package)
TS_TGZ=/path/to/tailscale_x_arm64.tgz node test/build-package.mjs   # offline
MTX_TGZ=/path/to/mediamtx_vX_linux_arm64.tar.gz node test/build-package.mjs   # offline, the gateway half
scripts/sync-from-re.sh [path]   # pull installer/agent from the research repo; exits 1 on change
agent/build.sh                   # rebuild bdkvm for linux/arm64 (needs Go + a cross toolchain)
player/build.sh                  # rebuild bdplay into player/dist/ (CGO_ENABLED=0, no zig)
converter/build.sh               # rebuild bdcam into converter/dist/ (needs zig, glibc 2.28 pin)
gateway/build.sh                 # vet, test, check the tab script, rebuild bdgw into gateway/dist/
tailscale-ui/build.sh            # vet, test, check the panel script, rebuild bdts into tailscale-ui/dist/
module-template/build.sh && node module-template/scripts/patcher-accepts.mjs module-template/dist/*.tgz
```

Each module folder has its own `AGENTS.md` — read it before changing that folder. A rebuilt
`dist/` binary is what ships, so commit one only after it has run on a unit.

## Deploy

Pushing to `main` deploys: `.github/workflows/deploy.yml` runs `npm test` (which assembles the
assets), smoke-checks the page, runs `wrangler deploy` and then checks the live `<head>` matches.
A push that only touches docs, `*.md` or a module's source doesn't redeploy; one that changes a
`*/dist/` binary does. Other branches never deploy. There is **no Cloudflare build connected**.
By hand:

```bash
scripts/build-assets.sh
cf-run npx wrangler deploy
```

Live at <https://birddog-play-patcher.stoatworks-labs.com>.

## Release

Local, with no workflow: `scripts/make-kit.sh` on a clean checkout of `main`, then
`gh release create v<package.json version>` with `kit/out/birddog-play-patcher-kit-<version>.zip`
attached. Burrow downloads that asset and checks it against the `digest` GitHub records.

## Inspecting what the generator produced

```bash
tar tzvf test/out/BirdDog_PLAY-custom-citest.fw
mkdir -p /tmp/x && tar xzf test/out/*.fw -C /tmp/x && cmp /tmp/x/update installer/update
cmp /tmp/x/modules/citest-mod/install test/fixtures/module/install
```

Local `wrangler` is older than the `compatibility_date`, so to look at the pages serve
`public/` statically (`python3 -m http.server --directory public`) and untick Tailscale.

Real `tar` disagreeing with `public/fw.js` is the failure that matters; the page's own reader
agreeing with its own writer proves nothing.
