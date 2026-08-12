# BirdDog PLAY Patcher

> **AI-assisted project.** This codebase was created with [Claude Code](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author. The package format was derived by static
> analysis of the stock updater, and the resulting package has been installed on a real BirdDog
> PLAY (firmware 1.0.30): SSH, Tailscale and the NDI KVM endpoint are all confirmed working on
> hardware, and the archive writer is checked byte-for-byte against `tar` in CI. That is **one
> unit on one firmware version** — it has not been tested across the range of PLAY and Pod
> firmwares in the field.

Builds an installable `.fw` for a [BirdDog PLAY](https://birddog.tv) that adds an SSH key,
[Tailscale](https://tailscale.com), and an NDI KVM endpoint — so a PLAY can be reached and
managed remotely, and can drive the machine it is displaying.

**Live: <https://birddog-play-patcher.stoatworks-labs.com>**

The package is assembled entirely in your browser. Nothing is uploaded, no account is needed,
and **you do not need your existing firmware file** — the generated package is a standalone
overlay installer, not a modified copy of BirdDog's firmware.

---

## Why no upload, and no decryption

BirdDog's updater extracts the uploaded archive and runs `./update` from it as root:

```bash
tar xf "$UPDATE_PACKAGE" -C "$UPDATE_DIR"
pushd "$UPDATE_DIR"
if ! [[ -e "./update" ]]; then ... fail ... fi
./update
```

There is **no signature check anywhere in that chain**. So a valid package is simply a gzip'd
tar with an executable `update` at the top level, and ours is a readable bash script rather
than a vendor binary.

That is the whole reason this tool can be a static page: there is no payload to decrypt and no
key involved, so there is nothing to upload and nothing to protect server-side. The only
server-side component is a proxy for `pkgs.tailscale.com`, which serves no CORS header.

**This repository contains no BirdDog firmware, no vendor keys, and no way to decrypt one.**
It ships only code written for this project.

## What gets installed

| Path | What | Survives a vendor firmware update? |
|---|---|---|
| `/root/.ssh/authorized_keys` | your key, appended, mode 600 | yes — the vendor installs with `cp -rf`, which is additive |
| `/userdata/tailscale/` | `tailscaled` + `tailscale`, 68 MB | expected, but not guaranteed |
| `/userdata/bd-kvm/` | `bdkvm` + `run.sh` | yes — deliberately not `/userdata/birddog-kvm`, which the vendor updater deletes |
| `/etc/systemd/system/bd-{tailscaled,kvm}.service` | units | yes |
| `/userdata/bd-probe.txt` | first-boot hardware report | yes |

Everything substantial lives on the `/userdata` partition. The installer:

- **refuses to run** unless `/etc/birddog-hardware-version` reads `BirdDog PLAY` or `BirdDog Pod`;
- never touches `sshd`, `sshd_config`, `birddog-update-wrapper`, `BirdDogUpdateRunner` or
  `birddog-web-ui` — those are how you get back in if something goes wrong;
- **cannot reach the kernel or bootloader**, which are not in this package format at all, so a
  bad install cannot break the boot chain;
- is idempotent, and does not reboot unless you ask it to.

Read [`installer/update`](installer/update) before you run it. It is 200 lines of commented
bash, it runs as root on your device, and you should not take anyone's word for what it does.

## Tailscale

Installed but **not authenticated** — no auth key is baked into the package, because it would
sit in cleartext both in the archive and on the device. After installing:

```bash
ssh -p 9031 root@<play-ip>
/userdata/tailscale/tailscale --socket=/run/bd-tailscaled.sock up
```

Stock PLAY firmware has no `/dev/net/tun` and ships no kernel modules at all, so Tailscale runs
in userspace-networking mode. **Inbound still works** — SSH, the web UI and the `:8080` API are
all reachable over the tailnet, because tailscaled's netstack proxies inbound TCP to local
listeners. Measured throughput is ~195 Mbps against a 920 Mbps wired baseline: comfortable for
NDI|HX and SRT, not enough for full-bandwidth NDI.

> **Before you put a PLAY on a tailnet:** its REST API on `:8080` has **no authentication of any
> kind** and sets `Access-Control-Allow-Origin: *`. Joining a tailnet does not change that —
> anyone who can reach the device on the tailnet can reconfigure it without credentials. Scope
> an ACL for the node; the device has no access control of its own to fall back on.

## NDI KVM

[`agent/`](agent) is `bdkvm`, a small Go program that reads the keyboard and mouse on the PLAY's
USB-A port and forwards them to the NDI source the PLAY is displaying, as `<ndi_kvm>` metadata.
It opens its own receiver at metadata-only bandwidth, so it costs nothing on the wire and
coexists with the PLAY's own receiver.

It uses the **free NDI SDK only**, and `dlopen`s the device's existing `libndi` at runtime
rather than linking it — so no NDI code is redistributed here.

## Development

```bash
scripts/build-assets.sh      # assemble public/assets/ from installer/ and agent/dist/
npx wrangler dev             # serve locally
node test/build-package.mjs  # build a package in Node and verify its structure
```

`public/fw.js` holds the archive logic and is deliberately free of DOM and network calls, so
the Node test exercises the exact module the browser runs rather than a reimplementation. The
test asserts that the `update` inside a generated package is byte-identical to
[`installer/update`](installer/update) in this repo.

**The tar layer is reproducible; the `.fw` is not.** Identical inputs produce a byte-identical
tar in both Chrome and Node, but the two engines configure zlib differently, so the same tar
gzips to different bytes. Compare tar digests across engines, never `.fw` digests.

## Recovering a unit

Nothing here writes to the kernel, bootloader or partition table. If an install goes wrong,
reflash stock firmware through the normal web UI, or use Rockchip recovery mode — note that
recovery restores *factory* state, not the unit's provisioned serial, hostname or `/userdata`.

To remove everything this installs:

```bash
systemctl disable --now bd-tailscaled bd-kvm
rm -rf /userdata/tailscale /userdata/bd-kvm /etc/systemd/system/bd-{tailscaled,kvm}.service
```

## Licence

MIT — see [LICENSE](LICENSE). Not affiliated with, endorsed by, or supported by BirdDog.
Installing this will not do your warranty any favours.
