// Builds a package with the browser's own fw.js, outside a browser.
//
// The module under test is byte-identical to the one public/app.js imports — no
// reimplementation — so `tar tzvf` on the result is real evidence about what the
// generator produces rather than about a test double.
//
//   node test/build-package.mjs [outdir]
//
// Set TS_TGZ to a local tailscale_<ver>_arm64.tgz to test offline; otherwise the
// current stable arm64 release is fetched, which also exercises the real
// pkgs.tailscale.com integration end to end.
//
// Exits non-zero on any structural problem.

import { readFile, writeFile, mkdir, readdir, stat } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const ASSETS = join(REPO, 'public/assets');
const OUTDIR = process.argv[2] || join(HERE, 'out');

const fw = await import(join(REPO, 'public/fw.js'));
const { Tar, extractTailscale, sha256Hex, buildConf, validateKey, isArm64Elf, gunzip, parseTar } = fw;

let failures = 0;
const check = (ok, msg) => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${msg}`);
  if (!ok) failures++;
};

/* -------------------------------------------------------------- unit checks */

console.log('validateKey:');
check(validateKey('') !== null, 'rejects empty');
check(validateKey('-----BEGIN OPENSSH PRIVATE KEY-----') !== null, 'rejects a private key');
check(validateKey('ssh-ed25519 AAAA\nssh-rsa BBBB') !== null, 'rejects two lines');
check(validateKey('hello world') !== null, 'rejects junk');
check(validateKey('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA1b2c3d4e5f6g7h8i9j') === null,
  'accepts ed25519 with no comment');
check(validateKey('  ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQ== user@host  ') === null,
  'accepts rsa with comment and padding');

/* ----------------------------------------------------------------- assemble */

if (!existsSync(join(ASSETS, 'manifest.json'))) {
  console.error('\nassets missing — run scripts/build-assets.sh first');
  process.exit(1);
}
const manifest = JSON.parse(await readFile(join(ASSETS, 'manifest.json'), 'utf8'));

console.log('\nassets match the manifest:');
for (const [key, file] of [['update', 'update'], ['probe', 'probe.sh'], ['kvmRun', 'kvm-run.sh']]) {
  const bytes = new Uint8Array(await readFile(join(ASSETS, file)));
  check(await sha256Hex(bytes) === manifest[key].sha256, `${file}`);
}

console.log('\nthe shipped installer is the committed one:');
for (const f of ['update', 'probe.sh', 'kvm-run.sh']) {
  const a = await readFile(join(ASSETS, f));
  const b = await readFile(join(REPO, 'installer', f));
  check(Buffer.compare(a, b) === 0, `public/assets/${f} === installer/${f}`);
}

// The camera payload is not offered by the web generator, so the installer must
// treat its absence as a no-op rather than an error.
const updateSrc = await readFile(join(REPO, 'installer/update'), 'utf8');
check(/WITH_CAM="\$\{WITH_CAM:-0\}"/.test(updateSrc), 'installer defaults WITH_CAM to 0');
check(/if \[ "\$WITH_CAM" = 1 \] && \[ -d/.test(updateSrc),
  'installer guards the camera block on both the flag and the directory');

async function tailscaleTarball() {
  if (process.env.TS_TGZ) {
    console.log(`\ntailscale: using ${process.env.TS_TGZ}`);
    return readFile(process.env.TS_TGZ);
  }
  console.log('\ntailscale: fetching current stable arm64 from pkgs.tailscale.com');
  const idx = await (await fetch('https://pkgs.tailscale.com/stable/?mode=json')).json();
  const file = idx.Tarballs.arm64;
  const [tgz, sidecar] = await Promise.all([
    fetch(`https://pkgs.tailscale.com/stable/${file}`).then((r) => r.arrayBuffer()),
    fetch(`https://pkgs.tailscale.com/stable/${file}.sha256`).then((r) => r.text()),
  ]);
  const want = sidecar.trim().split(/\s+/)[0];
  check(await sha256Hex(tgz) === want, `${file} matches its published .sha256`);
  return tgz;
}

const tar = new Tar();
tar.file('./update', new Uint8Array(await readFile(join(ASSETS, 'update'))), 0o755);
tar.file('./probe.sh', new Uint8Array(await readFile(join(ASSETS, 'probe.sh'))), 0o755);
tar.text('./authorized_keys',
  'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEYFORPACKAGEBUILDONLY test@example\n', 0o600);
tar.text('./build.conf',
  buildConf({
    tag: 'citest', withTailscale: true, withTailscaleUi: true, withKvm: true,
    withPlay: true, withCam: true, withCamUi: true, doReboot: false,
  }), 0o644);

const bins = await extractTailscale(await tailscaleTarball());
check(isArm64Elf(bins.tailscaled), 'tailscaled is an aarch64 ELF');
check(isArm64Elf(bins.tailscale), 'tailscale is an aarch64 ELF');
tar.file('./userdata/tailscale/tailscaled', bins.tailscaled, 0o755);
tar.file('./userdata/tailscale/tailscale', bins.tailscale, 0o755);

// The birdUI Tailscale panel. It ships with the Tailscale payload rather than
// as its own option, so a package that carries Tailscale and NOT this is the
// regression to catch: the device installs fine and then silently still needs
// SSH to sign in, which is the whole problem the panel exists to remove.
const bdts = new Uint8Array(await readFile(join(ASSETS, 'bdts-linux-arm64')));
check(isArm64Elf(bdts), 'bdts is an aarch64 ELF');
tar.file('./userdata/bd-tailscale-ui/bdts', bdts, 0o755);

const bdkvm = new Uint8Array(await readFile(join(ASSETS, 'bdkvm-linux-arm64')));
check(isArm64Elf(bdkvm), 'bdkvm is an aarch64 ELF');
tar.file('./userdata/birddog-kvm/bdkvm', bdkvm, 0o755);
tar.file('./userdata/birddog-kvm/run.sh',
  new Uint8Array(await readFile(join(ASSETS, 'kvm-run.sh'))), 0o755);

// USB media player. libpdfium.so is a shared object rather than an executable,
// but isArm64Elf only asserts the ELF header and machine type, which is what
// matters — a host-built x86 file reaching the device is the failure to catch.
for (const [asset, dest] of [
  ['bdplay-linux-arm64', './userdata/bd-play/bdplay'],
  ['bdpdf-linux-arm64', './userdata/bd-play/bdpdf'],
  ['libpdfium.so', './userdata/bd-play/libpdfium.so'],
  ['mount.exfat-fuse', './userdata/bd-play/mount.exfat-fuse'],
]) {
  const data = new Uint8Array(await readFile(join(ASSETS, asset)));
  check(isArm64Elf(data), `${asset} is an aarch64 ELF`);
  tar.file(dest, data, 0o755);
}

const blob = await tar.gzip();
await mkdir(OUTDIR, { recursive: true });
const out = join(OUTDIR, 'BirdDog_PLAY-custom-citest.fw');
await writeFile(out, Buffer.from(await blob.arrayBuffer()));
console.log(`\nwrote ${out} (${(blob.size / 1024 / 1024).toFixed(1)} MB)`);

/* --------------------------------------------------- structural self-checks */

const members = parseTar(await gunzip(await blob.arrayBuffer()));
const byName = new Map(members.map((m) => [m.name, m]));

console.log('\nstructure:');
for (const n of [
  './update', './probe.sh', './build.conf', './authorized_keys',
  './userdata/tailscale/tailscaled', './userdata/tailscale/tailscale',
  './userdata/bd-tailscale-ui/bdts',
  './userdata/birddog-kvm/bdkvm', './userdata/birddog-kvm/run.sh',
  './userdata/bd-play/bdplay', './userdata/bd-play/bdpdf',
  './userdata/bd-play/libpdfium.so', './userdata/bd-play/mount.exfat-fuse',
]) {
  check(byName.has(n), `contains ${n}`);
}

// Policy, not a bug hunt: mutool is AGPL v3. Serving it from this page would be
// distribution, and §13 reaches users interacting over a network — which a
// web-driven media player plainly has. PDF here is PDFium (BSD-3) via bdpdf.
// This asserts the mistake cannot happen quietly.
check(!members.some((m) => /mutool|mupdf/i.test(m.name)),
  'package contains no AGPL PDF renderer (mutool/MuPDF)');
check(!existsSync(join(ASSETS, 'mutool')),
  'no mutool staged as a web asset');

// Every asset must fit Cloudflare Workers' 25 MiB per-file static asset limit,
// or the deploy silently has nothing to serve.
const LIMIT = 25 * 1024 * 1024;
for (const f of await readdir(ASSETS)) {
  const { size } = await stat(join(ASSETS, f));
  check(size <= LIMIT, `asset ${f} is within Cloudflare's 25 MiB limit (${(size / 1048576).toFixed(1)} MB)`);
}

const updateMember = byName.get('./update');
check(new TextDecoder().decode(updateMember.data.subarray(0, 11)) === '#!/bin/bash',
  './update begins with a bash shebang');
check(updateMember.data.length === manifest.update.size, './update survives the round trip intact');
check(Buffer.compare(Buffer.from(updateMember.data), await readFile(join(REPO, 'installer/update'))) === 0,
  './update in the package is byte-identical to installer/update');

// Nothing derived from the vendor's encrypted payload may ever appear here.
const all = Buffer.concat(members.map((m) => Buffer.from(m.data)));
check(!all.includes('bdpff'), 'package contains no reference to the vendor payload');

// The panel and the Tailscale binaries are one feature; shipping either alone
// is a packaging mistake rather than a choice.
const conf = new TextDecoder().decode(byName.get('./build.conf').data);
check(/^WITH_TAILSCALE=1$/m.test(conf) === /^WITH_TAILSCALE_UI=1$/m.test(conf),
  'build.conf ships the Tailscale panel with the Tailscale payload');

// The converter's web UI tab is served by its binary; asking for the tab
// without the payload would install a page with nothing behind it.
check(!/^WITH_CAM_UI=1$/m.test(conf) || /^WITH_CAM=1$/m.test(conf),
  'build.conf never ships the UVC tab without the converter');
const camOnly = buildConf({ tag: 't', withCam: false, withCamUi: true, doReboot: false });
check(/^WITH_CAM_UI=0$/m.test(camOnly),
  'buildConf refuses the UVC tab when the converter is off');

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed');
process.exit(failures ? 1 : 0);
