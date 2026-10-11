# bd-play-module-template

> **AI-assisted project.** This codebase was created with [Claude](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author.

A starting point for your own **BirdDog PLAY module** — something the
[PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com) packs into the
firmware package it builds, and the package's installer runs as root on the
device, alongside the patcher's own payloads (Tailscale, the NDI KVM endpoint,
the USB media player, the UVC converter).

This template is a complete, working module. It installs a tiny service,
`bd-hello`, that writes a heartbeat file every minute — enough to prove the
whole path from "Use this template" to "it is running on my PLAY", and to be
replaced with what you actually want.

> **You are packaging code that will run as root on your device.** The patcher
> checks a module's *structure* — the archive, the names, the paths, that any
> binary is aarch64 — and nothing about what `install` does. Read what you
> package, and treat your first install as an experiment on a unit you can
> afford to have offline. The full contract, with the reasoning behind each
> rule, is at **[birddog-play-patcher.stoatworks-labs.com/modules.html](https://birddog-play-patcher.stoatworks-labs.com/modules.html)**.

## What a module is

A directory, shipped as a `.tgz`, containing:

```
module.conf        NAME, VERSION, DESCRIPTION, HOMEPAGE — read, never sourced
install            bash; runs as root with this directory as cwd
uninstall          optional but expected; copied onto the device for later
payload/           whatever install copies to /userdata/bd-<name>/
  run.sh             the unit's ExecStart: rotates a log, execs the program
  hello.sh           the example service — replace with yours
  hello.conf.default settings the user may edit on the device; kept across reinstalls
  bin/               a compiled program goes here (aarch64 — build.sh checks)
```

The patcher unpacks the `.tgz` in the browser and writes it into the `.fw` at
`modules/<NAME>/`. On the device, after its own payloads are installed, the
package's `update` script runs each `modules/*/install` in turn.

## Use it

1. **Use this template** on GitHub (or clone it), and rename things: `NAME`
   in `module.conf` is the one name everything else derives from —
   `modules/<NAME>/` in the package, `/userdata/bd-<NAME>` and
   `bd-<NAME>.service` on the device. `install`, `uninstall` and `hello.sh`
   read it from `module.conf`, so change it in one place.
2. Put your program in `payload/` and make `install` copy and start it. The
   example `install` is commented line by line; keep its shape.
3. Build:

   ```bash
   ./build.sh          # runs ./check.sh, then writes dist/<NAME>-<VERSION>.tgz
   ```

4. Open the [patcher](https://birddog-play-patcher.stoatworks-labs.com), choose
   the `.tgz` under **Custom modules**, build the package, and upload the `.fw`
   through the PLAY's normal firmware page. The build log on the page names
   your module; the install log on the device is `/tmp/bd-custom-install.log`,
   and `/userdata/bd-modules.log` keeps one line per module ever installed.
5. Prove it: `ssh -p 9031 root@<play-ip> cat /userdata/bd-hello/heartbeat`.

To take it off again: `ssh -p 9031 root@<play-ip> bash /userdata/bd-hello/uninstall`.

## What `install` gets, and what it must do

The installer runs `bash ./install` with these in the environment:

| Variable | Value |
|---|---|
| `BD_MODULE` | `NAME` from `module.conf` |
| `BD_MODULE_VERSION` | `VERSION` from `module.conf` |
| `BD_MODULE_DIR` | absolute path of the module directory (also the cwd) |
| `BD_BUILD_TAG` | the package's build tag |
| `BD_HARDWARE` | `BirdDog PLAY` or `BirdDog Pod` — anything else never reaches a module |
| `BD_INSTALL_LOG` | `/tmp/bd-custom-install.log`, where stdout and stderr go |
| `bd_log` | a function: `bd_log "text"` shows in the web UI's live update log as `[module:<name>] text` |

Around it, the installer has already checked the hardware ID, made the rootfs
writable and confirmed `/userdata` exists. It runs the module in a subshell,
kills it after ten minutes (with Debian's `timeout`, which stock firmware has),
logs a non-zero exit as a warning, and carries on — so a module that fails,
exits non-zero or hangs cannot stop the installer restarting `BirdDogRunner`,
which is what brings the HDMI output back. It cannot contain a module that
deliberately reboots, stops the display or kills the updater; nothing can, which
is why the rules below exist.

What nothing enforces, and what keeps a unit recoverable:

- **Keep everything under `/userdata/bd-<name>`.** The rootfs gets a systemd
  unit at most. `/userdata` survives vendor firmware updates; the rootfs does not.
- **Never touch `sshd`, `sshd_config`, `birddog-update-wrapper`,
  `BirdDogUpdateRunner` or `birddog-web-ui`.** They are how you get back in
  when something is wrong. `check.sh` warns if `install` names them.
- **Be idempotent.** `install` runs again on every reinstall. Keep a config the
  user edited on the device (`hello.conf` shows how) rather than overwriting it.
- **Do not reboot, and do not stop `BirdDogRunner` unless you start it again.**
  The package has its own reboot option; a stopped `BirdDogRunner` is a dark
  HDMI output that looks like a bricked unit.
- **Exit 0 on success.** Report a service that failed to start with `bd_log`
  and still exit 0 — that is a problem for `journalctl`, not a failed firmware
  install.
- **Pick a port nobody has.** 80 and 8080 are BirdDog's, 8090 `bd-cam-api`,
  8091 `bd-play`, 8092 `bd-tailscale-ui`, 9031 sshd. A collision is silent:
  the second service fails to bind and its page simply never answers.

## Limits the format imposes

- **Paths are at most 100 bytes** including the `./modules/<NAME>/` prefix,
  because the package is plain ustar and the writer refuses longer names
  rather than truncating. `check.sh` reports the exact overrun.
- **No symlinks or hard links** in the archive; create them from `install`.
- **Binaries must be aarch64 ELF**, built for Debian 10's glibc 2.28 or fully
  static. Go with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` is the easy path;
  the patcher's own [bdplay](https://github.com/stoatworks-labs/bd-play-usb-player)
  is built that way, and [bdcam](https://github.com/stoatworks-labs/bdcam) and
  [bdts](https://github.com/stoatworks-labs/bdts) show cgo with zig.
- **Keep it small.** The whole package is extracted into a temp dir on the
  device before anything runs. Tens of megabytes are proven (Tailscale is
  68 MB unpacked); the patcher refuses a module over 256 MB.
- The device has bash, coreutils, systemd, curl and GStreamer — **no python,
  no perl, no package manager**.

## Checks

```bash
./check.sh                                  # static: names, paths, links, ELF arch, parse
./build.sh                                  # check + dist/<NAME>-<VERSION>.tgz, listed by real tar
node scripts/patcher-accepts.mjs dist/*.tgz # the patcher's own reader must accept it
```

The last one imports `fw.js` from a shallow clone of the patcher, so a module
that passes it here will be accepted in the browser, and refused here with the
same message it would get there. CI runs all three on every push.

## Worked examples

The patcher's own payloads are modules in all but packaging, and each is a
public repo you can read: [bdts](https://github.com/stoatworks-labs/bdts)
(patches a web-UI page, with a health check and rollback — the pattern to copy
if you must touch the UI), [bdcam](https://github.com/stoatworks-labs/bdcam)
(a self-supervising unit that exits 0 when its device is absent, which turns
`Restart=always` into a hotplug poller), and
[bd-play-usb-player](https://github.com/stoatworks-labs/bd-play-usb-player).
The installer that runs them all is
[`installer/update`](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/installer/update)
in the patcher; its `modules` section is the code that runs yours.

## About this template

Written as part of
[Stoatworks Labs](https://stoatworks-labs.com)' BirdDog PLAY tooling. MIT
licensed — see [LICENSE](LICENSE); relicense your own module as you like. Not
affiliated with or endorsed by BirdDog.
