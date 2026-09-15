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
// pkgs.tailscale.com integration end to end. MTX_TGZ does the same for the
// streaming gateway's MediaMTX tarball (mediamtx_v<ver>_linux_arm64.tar.gz);
// otherwise the release the Worker pins is fetched from GitHub and checked
// against its published checksums.sha256.
//
// Exits non-zero on any structural problem.

import { readFile, writeFile, mkdir, readdir, stat } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const ASSETS = join(REPO, 'public/assets');
const OUTDIR = process.argv[2] || join(HERE, 'out');

const fw = await import(join(REPO, 'public/fw.js'));
const {
  Tar, extractTailscale, extractMediaMTX, sha256Hex, buildConf, validateKey, isArm64Elf,
  gunzip, parseTar, readModule, addModule, RESERVED_MODULE_NAMES,
} = fw;

// The MediaMTX release is pinned in the Worker; read it from there so this
// test and the page cannot disagree about which tarball is packaged.
const workerSrc = await readFile(join(REPO, 'src/worker.js'), 'utf8');
const MEDIAMTX_VERSION = (workerSrc.match(/const MEDIAMTX_VERSION = '(v[0-9.]+)'/) || [])[1];

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

// Third-party modules run as root from modules/<name>/install. What matters is
// that a broken one cannot stop the installer reaching the BirdDogRunner
// restore: guarded on flag and directory, run in a subshell, exit logged.
check(/WITH_MODULES="\$\{WITH_MODULES:-0\}"/.test(updateSrc), 'installer defaults WITH_MODULES to 0');
check(/if \[ "\$WITH_MODULES" = 1 \] && \[ -d "\$SDIR\/modules" \]/.test(updateSrc),
  'installer guards the modules block on both the flag and the directory');
check(/\( cd "\$mdir" && [\s\S]*?bash \.\/install \)/.test(updateSrc),
  'installer runs each module install in a subshell');
check(/module \$\{mname\}: install exited \$\{mrc\} — continuing/.test(updateSrc),
  'installer logs a failed module and continues');
check(updateSrc.indexOf('modules/*/') < updateSrc.indexOf('systemctl start BirdDogRunner'),
  'modules run before the BirdDogRunner restore');
check(/export -f bd_log/.test(updateSrc), 'installer exports bd_log for modules');

// The streaming gateway: guarded on flag and directory like every payload; its
// tab only ever installed when the payload was; the decoder never touched by
// the installer itself (bdgw does that, from the tab, on request).
check(/WITH_GATEWAY="\$\{WITH_GATEWAY:-0\}"/.test(updateSrc), 'installer defaults WITH_GATEWAY to 0');
check(/if \[ "\$WITH_GATEWAY" = 1 \] && \[ -d "\$SDIR\/userdata\/bd-gw" \]/.test(updateSrc),
  'installer guards the gateway block on both the flag and the directory');
check(/if \[ "\$\{GW_INSTALLED:-0\}" = 1 \] && \[ "\$WITH_GATEWAY_UI" = 1 \]/.test(updateSrc),
  'installer only patches the Streaming tab when the gateway was installed');
check(/bdgw --unpatch-ui --ui-dir \/srv\/birddog-web-ui/.test(updateSrc),
  'installer rolls the Streaming tab back with bdgw --unpatch-ui');
check(!/birddog-source1-name|birddog_srt_src|SourceSelection/.test(updateSrc),
  'installer never touches the decoder selection itself');
check(MEDIAMTX_VERSION !== undefined, `Worker pins a MediaMTX version (${MEDIAMTX_VERSION})`);
for (const n of ['gateway', 'gw', 'mtx']) {
  check(RESERVED_MODULE_NAMES.has(n), `module name "${n}" is reserved for the gateway`);
}

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

async function mediamtxTarball() {
  if (process.env.MTX_TGZ) {
    console.log(`\nmediamtx: using ${process.env.MTX_TGZ}`);
    return readFile(process.env.MTX_TGZ);
  }
  const base = `https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}`;
  const file = `mediamtx_${MEDIAMTX_VERSION}_linux_arm64.tar.gz`;
  console.log(`\nmediamtx: fetching ${file} from GitHub`);
  const [tgz, sums] = await Promise.all([
    fetch(`${base}/${file}`).then((r) => r.arrayBuffer()),
    fetch(`${base}/checksums.sha256`).then((r) => r.text()),
  ]);
  const line = sums.split('\n').map((l) => l.trim()).find((l) => l.endsWith(file));
  const want = line && line.split(/\s+/)[0];
  check(/^[0-9a-f]{64}$/.test(want || ''), `${file} has a published SHA-256`);
  check(await sha256Hex(tgz) === want, `${file} matches its published checksums.sha256`);
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
    withPlay: true, withCam: true, withCamUi: true, withGateway: true, withGatewayUi: true,
    withModules: true, doReboot: false,
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

// The streaming gateway: MediaMTX out of its release tarball, the panel from
// the assets, and the two run scripts.
const mtx = await extractMediaMTX(await mediamtxTarball());
check(isArm64Elf(mtx.mediamtx), 'mediamtx is an aarch64 ELF');
check(mtx.mediamtx.length > 20 * 1024 * 1024, `mediamtx is a full binary (${(mtx.mediamtx.length / 1048576).toFixed(1)} MB)`);
check(mtx.license !== null && /MIT License/.test(new TextDecoder().decode(mtx.license)),
  'the tarball carries MediaMTX\'s MIT licence, which travels with the binary');
tar.file('./userdata/bd-gw/mediamtx', mtx.mediamtx, 0o755);
tar.file('./userdata/bd-gw/LICENSE.mediamtx', mtx.license, 0o644);
const bdgw = new Uint8Array(await readFile(join(ASSETS, 'bdgw-linux-arm64')));
check(isArm64Elf(bdgw), 'bdgw is an aarch64 ELF');
tar.file('./userdata/bd-gw/bdgw', bdgw, 0o755);
for (const [asset, dest] of [['gw-run.sh', './userdata/bd-gw/run.sh'], ['gw-api-run.sh', './userdata/bd-gw/api-run.sh']]) {
  const data = await readFile(join(ASSETS, asset));
  check(Buffer.compare(data, await readFile(join(REPO, 'installer', asset))) === 0,
    `public/assets/${asset} === installer/${asset}`);
  tar.file(dest, new Uint8Array(data), 0o755);
}

// A third-party module, packed by the SYSTEM tar from test/fixtures/module —
// GNU tar in CI, bsdtar on a Mac — so readModule is exercised against a real
// tar program's output rather than against fw.js writing for itself.
await mkdir(OUTDIR, { recursive: true });
const fixtureTgz = join(OUTDIR, 'citest-mod.tgz');
execFileSync('tar', ['czf', fixtureTgz, '-C', join(HERE, 'fixtures'), 'module'],
  { env: { ...process.env, COPYFILE_DISABLE: '1' } });
console.log('\nfixture module (packed by the system tar):');
const mod = await readModule(await readFile(fixtureTgz), 'citest-mod.tgz');
check(mod.name === 'citest-mod' && mod.version === '0.0.1', `read as ${mod.name} ${mod.version}`);
check(mod.files.map((f) => f.path).join(' ') === 'install module.conf payload/hello.txt',
  'wrapping directory stripped, files sorted');
check(mod.files.find((f) => f.path === 'install').mode === 0o755, 'install is 0755');
check(mod.files.find((f) => f.path === 'payload/hello.txt').mode === 0o644, 'payload file is 0644');
addModule(tar, mod);

const blob = await tar.gzip();
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
  './modules/citest-mod/install', './modules/citest-mod/module.conf',
  './modules/citest-mod/payload/hello.txt',
  './userdata/bd-gw/mediamtx', './userdata/bd-gw/bdgw', './userdata/bd-gw/run.sh',
  './userdata/bd-gw/api-run.sh', './userdata/bd-gw/LICENSE.mediamtx',
]) {
  check(byName.has(n), `contains ${n}`);
}
check(Buffer.compare(Buffer.from(byName.get('./modules/citest-mod/install').data),
  await readFile(join(HERE, 'fixtures/module/install'))) === 0,
  './modules/citest-mod/install is byte-identical to the fixture');

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

// The Streaming tab is served by bdgw; asking for it without the payload would
// install a page with nothing behind it.
check(!/^WITH_GATEWAY_UI=1$/m.test(conf) || /^WITH_GATEWAY=1$/m.test(conf),
  'build.conf never ships the Streaming tab without the gateway');
const gwOnly = buildConf({ tag: 't', withGateway: false, withGatewayUi: true, doReboot: false });
check(/^WITH_GATEWAY_UI=0$/m.test(gwOnly) && /^WITH_GATEWAY=0$/m.test(gwOnly),
  'buildConf refuses the Streaming tab when the gateway is off');
// The gateway's config is never shipped: it is the device's own settings, and
// a packaged one would overwrite them on every reinstall.
check(!members.some((m) => /bd-gw\/config\.json$/.test(m.name)),
  'package carries no gateway config.json');

// The modules flag and directory travel together, in both directions.
check(/^WITH_MODULES=1$/m.test(conf) === members.some((m) => m.name.startsWith('./modules/')),
  'build.conf WITH_MODULES matches whether the package carries modules/');
check(/^WITH_MODULES=0$/m.test(buildConf({ tag: 't', doReboot: false })),
  'buildConf defaults WITH_MODULES to 0');

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed');
process.exit(failures ? 1 : 0);
