# CLAUDE.md — BirdDog PLAY Patcher

Command reference. For the model, the invariants and the traps, read
[AGENTS.md](AGENTS.md) first.

## Commands

```bash
scripts/build-assets.sh          # assemble public/assets/ — run this first, it is gitignored
npx wrangler dev --port 8792     # serve the page + the tailscale proxy
node test/build-package.mjs      # build a package in Node, verify structure
TS_TGZ=/path/to/tailscale_x_arm64.tgz node test/build-package.mjs   # offline
scripts/sync-from-re.sh [path]   # pull installer/agent from the research repo; exits 1 on change
agent/build.sh                   # rebuild bdkvm for linux/arm64 (needs Go + a cross toolchain)
```

## Deploy

```bash
scripts/build-assets.sh
cf-run npx wrangler deploy
```

Live at <https://birddog-play-patcher.stoatworks-labs.com>. There is **no Cloudflare build
connected** — deploys are local, so pushing a branch cannot publish to production.

## Inspecting what the generator produced

```bash
tar tzvf test/out/BirdDog_PLAY-custom-citest.fw
mkdir -p /tmp/x && tar xzf test/out/*.fw -C /tmp/x && cmp /tmp/x/update installer/update
```

Real `tar` disagreeing with `public/fw.js` is the failure that matters; the page's own reader
agreeing with its own writer proves nothing.
