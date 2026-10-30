// Builds the release kit (scripts/make-kit.sh) and checks it the way Burrow
// will read it: every entry deflated, assets/ exactly what the page serves,
// every manifest.json entry's size and sha256 right, kit.json's pins the ones
// in src/worker.js and public/fw.js.
//
//   node test/kit.mjs
//
// Then it packs test/out/BirdDog_PLAY-custom-citest.fw again (written by
// test/build-package.mjs, so run that first) from the kit's assets/, as
// public/app.js packs from the page's, with the same Tailscale, MediaMTX, key
// and module, and requires the uncompressed tar to come out byte-identical.
// The zip is read by Python's zipfile and the package by the system tar, never
// by code here that also wrote them.
//
// Exits non-zero on any problem.

import { readFile, readdir } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join, relative } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const ASSETS = join(REPO, 'public/assets');
const OUT = join(HERE, 'out/kit');
const CITEST = join(HERE, 'out/BirdDog_PLAY-custom-citest.fw');

const {
  PACKAGE, PART, Tar, gunzip, tarEntries, MODULE_PREFIX, RESERVED_MODULE_NAMES, MODULE_MAX_BYTES,
} = await import(join(REPO, 'public/fw.js'));

let failures = 0;
const check = (ok, msg) => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${msg}`);
  if (!ok) failures++;
};
const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

if (!existsSync(CITEST)) {
  console.error(`\n${CITEST} missing — run node test/build-package.mjs first`);
  process.exit(1);
}

async function files(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    if (e.isDirectory()) out.push(...(await files(join(dir, e.name))));
    else out.push(join(dir, e.name));
  }
  return out;
}

/* -------------------------------------------------------------------- build */

const { version } = JSON.parse(await readFile(join(REPO, 'package.json'), 'utf8'));
console.log('building the kit:');
execFileSync(join(REPO, 'scripts/make-kit.sh'), [OUT], { stdio: 'inherit' });
const zipName = `birddog-play-patcher-kit-${version}.zip`;
const zipPath = join(OUT, zipName);
check(existsSync(zipPath), `wrote ${zipName}`);

/* ------------------------------------------------------- read it, as Burrow */

// Python's zipfile is the independent reader: it lists every entry's method,
// mode and extra fields, CRC-checks the lot and extracts it.
const XDIR = join(OUT, 'x');
const listing = JSON.parse(execFileSync('python3', ['-I', '-c', `
import json, shutil, sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
shutil.rmtree(sys.argv[2], ignore_errors=True)
bad = z.testzip()
entries = [{'name': i.filename, 'method': i.compress_type, 'system': i.create_system,
            'mode': i.external_attr >> 16, 'dir': i.is_dir(), 'extra': len(i.extra)}
           for i in z.infolist()]
z.extractall(sys.argv[2])
print(json.dumps({'bad': bad, 'entries': entries}))
`, zipPath, XDIR], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 }));
const entries = listing.entries;
const names = entries.map((e) => e.name);

console.log('\nthe zip:');
check(listing.bad === null, 'every entry passes its CRC');
check(entries.every((e) => e.method === 8), 'every entry is deflated (Burrow reads deflate and nothing else)');
check(entries.every((e) => e.system === 3 && (e.mode & 0o170000) === 0o100000),
  'every entry is a regular file with a unix mode (no symlinks)');
check(entries.every((e) => !e.dir), 'no directory entries');
check(entries.every((e) => e.extra === 0), 'no extra fields (no zip64)');
check(names[0] === 'kit.json', 'kit.json is the first entry');
check(names.every((n) => n === 'kit.json' || n.startsWith('assets/') || n.startsWith('NOTICES/')),
  'nothing outside kit.json, assets/ and NOTICES/');

/* ----------------------------------------------------------------- kit.json */

console.log('\nkit.json:');
const kit = JSON.parse(await readFile(join(XDIR, 'kit.json'), 'utf8'));
check(same(Object.keys(kit), ['kit', 'version', 'commit', 'mediamtx', 'tailscale', 'reservedModuleNames', 'moduleMaxBytes']),
  'has exactly kit, version, commit, mediamtx, tailscale, reservedModuleNames, moduleMaxBytes');
check(kit.kit === 1, 'kit 1');
check(kit.version === version, `version ${kit.version} is package.json's`);
check(/^[0-9a-f]{40}$/.test(kit.commit), `commit is 40 hex (${kit.commit.slice(0, 7)})`);

const worker = await readFile(join(REPO, 'src/worker.js'), 'utf8');
const mtxPin = (worker.match(/^const MEDIAMTX_VERSION = '(v[0-9.]+)';/m) || [])[1];
const mtxBase = (worker.match(/^const MEDIAMTX_BASE = '([^']+)';/m) || [])[1];
const tsBase = (worker.match(/^const UPSTREAM = '([^']+)';/m) || [])[1];
check(same(Object.keys(kit.mediamtx), ['version', 'file', 'base', 'checksums']), 'mediamtx has version, file, base, checksums');
check(kit.mediamtx.version === mtxPin, `mediamtx.version ${kit.mediamtx.version} is src/worker.js's MEDIAMTX_VERSION`);
check((await readFile(join(REPO, 'gateway/render.go'), 'utf8')).includes(`MediaMTXVersion = "${kit.mediamtx.version}"`),
  'and gateway/render.go\'s');
check(kit.mediamtx.file === `mediamtx_${mtxPin}_linux_arm64.tar.gz`, `mediamtx.file ${kit.mediamtx.file}`);
check(kit.mediamtx.base === mtxBase && mtxBase === 'https://github.com/bluenviron/mediamtx/releases/download/',
  'mediamtx.base is the Worker\'s MEDIAMTX_BASE');
check(kit.mediamtx.checksums === 'checksums.sha256' && worker.includes('${MEDIAMTX_BASE}${v}/checksums.sha256'),
  'mediamtx.checksums is the file the Worker reads');
check(same(Object.keys(kit.tailscale), ['index', 'base', 'arch']), 'tailscale has index, base, arch');
check(kit.tailscale.base === tsBase && tsBase === 'https://pkgs.tailscale.com/stable/', 'tailscale.base is the Worker\'s UPSTREAM');
check(kit.tailscale.index === `${tsBase}?mode=json` && worker.includes('${UPSTREAM}?mode=json'), 'tailscale.index is the Worker\'s');
check(kit.tailscale.arch === 'arm64' && worker.includes('data.Tarballs.arm64') && worker.includes('tailscale_${v}_arm64.tgz'),
  'tailscale.arch arm64 is what the Worker fetches');
check(same(kit.reservedModuleNames, [...RESERVED_MODULE_NAMES]), 'reservedModuleNames is fw.js\'s RESERVED_MODULE_NAMES');
check(kit.moduleMaxBytes === MODULE_MAX_BYTES, `moduleMaxBytes ${kit.moduleMaxBytes} is fw.js's MODULE_MAX_BYTES`);
const updateSrc = await readFile(join(REPO, 'installer/update'), 'utf8');
for (const n of new Set([...updateSrc.matchAll(/\/userdata\/bd-([a-z][a-z-]*)/g)].map((m) => m[1]))) {
  check(kit.reservedModuleNames.includes(n), `module name "${n}" is reserved (installer uses /userdata/bd-${n})`);
}

/* ------------------------------------------------------------------- assets */

console.log('\nassets/ is public/assets/, minus the page\'s own shim:');
const served = (await files(ASSETS)).map((f) => relative(ASSETS, f)).filter((f) => f !== 'support-footer-version.js').sort();
const inKit = names.filter((n) => n.startsWith('assets/')).map((n) => n.slice('assets/'.length)).sort();
check(same(inKit, served), `the same ${served.length} files`);
check(!inKit.includes('support-footer-version.js'), 'no support-footer-version.js');
for (const f of inKit) {
  if (!served.includes(f)) continue;
  check(Buffer.compare(await readFile(join(XDIR, 'assets', f)), await readFile(join(ASSETS, f))) === 0,
    `assets/${f} is byte-identical`);
}

const manifest = JSON.parse(await readFile(join(XDIR, 'assets/manifest.json'), 'utf8'));
console.log('\nevery manifest.json entry is right:');
for (const [key, meta] of Object.entries(manifest)) {
  if (['generated', 'source', 'rootfsOverlay'].includes(key)) continue;
  const p = PART[key];
  check(p && p.asset, `${key} is a staged asset fw.js knows`);
  check(meta !== null, `${key} is present (a release kit carries every payload)`);
  if (!p || !p.asset || !meta) continue;
  const data = await readFile(join(XDIR, 'assets', p.asset));
  check(data.length === meta.size && sha256(data) === meta.sha256, `assets/${p.asset} size ${meta.size} and sha256`);
  check(Buffer.compare(data, await readFile(join(REPO, p.src))) === 0, `  is the committed ${p.src}`);
}
for (const p of PACKAGE.filter((x) => x.asset)) check(p.id in manifest, `manifest.json has ${p.id}`);
for (const rel of manifest.rootfsOverlay || []) check(inKit.includes(`files/${rel}`), `rootfs overlay files/${rel}`);
const modeOf = new Map(entries.map((e) => [e.name, e.mode & 0o777]));
check(PACKAGE.filter((p) => p.asset).every((p) => modeOf.get(`assets/${p.asset}`) === p.mode),
  'every asset has its package mode in the zip (0755 for scripts and binaries)');

/* ------------------------------------------------------------------ notices */

console.log('\nNOTICES/:');
const text = async (n) => (existsSync(join(XDIR, n)) ? readFile(join(XDIR, n), 'utf8') : '');
check(/GNU GENERAL PUBLIC LICENSE\s+Version 2/.test(await text('NOTICES/fuse-exfat-COPYING')), 'fuse-exfat\'s GPL v2');
check(/GNU LESSER GENERAL PUBLIC LICENSE\s+Version 2\.1/.test(await text('NOTICES/libfuse-COPYING.LIB')), 'libfuse\'s LGPL v2.1');
check(/The PDFium Authors/.test(await text('NOTICES/pdfium/pdfium.txt')), 'PDFium\'s BSD-3 notice');
check(/MIT License/.test(await text('NOTICES/birddog-play-patcher-LICENSE')), 'this repo\'s MIT licence');
const notice = await text('NOTICES/NOTICE.md');
check(/source is offered/.test(notice) && notice.includes('fuse-exfat-1.3.0.tar.gz') && notice.includes('fuse-2.9.9.tar.gz'),
  'NOTICE.md carries the source offer for fuse-exfat and libfuse');
check(!/`(bin|licenses|installer)\//.test(notice), 'NOTICE.md names only paths the kit has');

/* ------------------------------------------------------------------- policy */

console.log('\nwhat the kit must never carry:');
check(!names.some((n) => /(^|\/)(tailscaled?|mediamtx|mutool)[^/]*$/i.test(n) && !n.startsWith('NOTICES/')),
  'no Tailscale, MediaMTX or mutool binary');
check(!names.some((n) => /\.(fw|img|key)$/i.test(n)), 'no firmware file, image or key');
let vendor = false;
for (const n of names) if ((await readFile(join(XDIR, n))).includes('bdpff')) vendor = true;
check(!vendor, 'no reference to the vendor payload');

/* ------------------------------------------------- the package the page packs */

console.log('\npacking from the kit as app.js packs from the page:');

// app.js packs from fw.js's table: no member path or asset name of its own,
// and the table's entries in the table's order. That table is what Burrow ports.
const appSrc = await readFile(join(REPO, 'public/app.js'), 'utf8');
check(!/'\.\/userdata\/|`\.\/userdata\/|'\.\/update'|'\.\/probe\.sh'|'\.\/authorized_keys'|'\.\/build\.conf'/.test(appSrc),
  'app.js names no member path of its own');
check(!/assets\/[a-z.-]+-linux-arm64|assets\/(kvm|cam|cam-api|gw|gw-api)-run\.sh|assets\/(libpdfium\.so|mount\.exfat-fuse)/.test(appSrc),
  'app.js names no asset of its own');
const at = (p) => {
  const i = appSrc.search(new RegExp(`PART\\.${p.id}\\b`));
  return i !== -1 ? i : appSrc.indexOf(`partsOf('${p.option}')`);
};
const positions = PACKAGE.map(at);
check(positions.every((i) => i !== -1), 'app.js packs every member in the table');
check(positions.every((i, n) => n === 0 || i >= positions[n - 1]), 'app.js packs them in the table\'s order');

// The CI package, as the system tar reads it.
const tarList = execFileSync('tar', ['-tvzf', CITEST], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
  .trim().split('\n').map((l) => {
    const t = l.trim().split(/\s+/);
    const perm = t[0].slice(1);
    let mode = 0;
    for (let i = 0; i < 9; i++) if (perm[i] !== '-') mode |= 1 << (8 - i);
    return { name: t[t.length - 1].replace(/\/$/, ''), dir: t[0][0] === 'd', mode };
  });
const citestTar = new Uint8Array(await gunzip(await readFile(CITEST)));
const pkg = new Map(tarEntries(citestTar).filter((e) => e.type !== '5').map((e) => [e.name, e]));

// Every option on: each staged asset from the kit's assets/, and the package's
// own copies of what the kit leaves to the builder (Tailscale, MediaMTX, the
// key, build.conf, the module).
const tar = new Tar();
const expect = [];
const seen = new Set();
const expectDirs = (path) => {
  const parent = path.slice(0, path.lastIndexOf('/'));
  if (!parent || parent === '.' || seen.has(parent)) return;
  expectDirs(parent);
  seen.add(parent);
  expect.push({ name: parent, dir: true, mode: 0o755 });
};
for (const p of PACKAGE) {
  const data = p.asset ? await readFile(join(XDIR, 'assets', p.asset)) : pkg.get(p.member)?.data;
  check(data !== undefined, `${p.member} (${p.asset ? `assets/${p.asset}` : p.fetch || p.input})`);
  if (data === undefined) continue;
  tar.file(p.member, new Uint8Array(data), p.mode);
  expectDirs(p.member);
  expect.push({ name: p.member, dir: false, mode: p.mode });
}
const mods = [...pkg.values()].filter((e) => e.name.startsWith(MODULE_PREFIX));
check(mods.length > 0, `the CI package carries a module under ${MODULE_PREFIX}`);
for (const e of mods) {
  tar.file(e.name, e.data, e.mode);
  expectDirs(e.name);
  expect.push({ name: e.name, dir: false, mode: e.mode });
}

check(same(tarList.map((e) => e.name), expect.map((e) => e.name)),
  `the system tar lists the CI package's ${tarList.length} entries in fw.js's order`);
check(same(tarList.map((e) => [e.dir, e.mode]), expect.map((e) => [e.dir, e.mode])), 'with fw.js\'s types and modes');
const rebuilt = new Uint8Array(await tar.blob().arrayBuffer());
check(rebuilt.length === citestTar.length && Buffer.compare(rebuilt, citestTar) === 0,
  `a package packed from the kit is byte-identical to the CI package (${(rebuilt.length / 1048576).toFixed(1)} MB tar)`);

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed');
process.exit(failures ? 1 : 0);
