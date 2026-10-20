// Builds the release kit (scripts/make-kit.sh) and checks it the way Burrow
// will read it: every entry deflated, every byte matching kit.json's sha256,
// and kit.json's layout the one the page packs.
//
//   node test/kit.mjs
//
// The layout check rebuilds test/out/BirdDog_PLAY-custom-citest.fw (written by
// test/build-package.mjs, so run that first) by following kit.json alone, with
// the kit's files and the same Tailscale, MediaMTX, key and module, and
// requires the uncompressed tar to come out byte-identical. The zip is read by
// Python's zipfile and the package by the system tar, never by code here that
// also wrote them.
//
// Exits non-zero on any problem.

import { readFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const OUT = join(HERE, 'out/kit');
const CITEST = join(HERE, 'out/BirdDog_PLAY-custom-citest.fw');

const {
  PACKAGE, PART, OPTIONS, Tar, gunzip, tarEntries, buildConf, packageName, TAG_UNSAFE_RE,
  MTIME, MODULE_PREFIX, RESERVED_MODULE_NAMES,
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

console.log('\nthe zip:');
check(listing.bad === null, 'every entry passes its CRC');
check(entries.every((e) => e.method === 8), 'every entry is deflated (Burrow reads deflate and nothing else)');
check(entries.every((e) => e.system === 3), 'every entry carries a unix mode');
check(entries.every((e) => !e.dir), 'no directory entries');
check(entries.every((e) => e.extra === 0), 'no extra fields (no zip64)');
check(entries[0]?.name === 'kit.json', 'kit.json is the first entry');

const kit = JSON.parse(await readFile(join(XDIR, 'kit.json'), 'utf8'));
check(kit.schema === 1 && kit.name === 'birddog-play-patcher-kit', 'kit.json schema 1');
check(kit.version === version, `kit version ${kit.version} is package.json's ${version}`);
check(/^[0-9a-f]{40}$/.test(kit.source.commit), `names its commit (${kit.source.commit.slice(0, 7)})`);

const listed = ['kit.json', ...kit.files.map((f) => f.path), ...kit.notices.map((f) => f.path)];
check(same([...listed].sort(), entries.map((e) => e.name).sort()),
  `the zip holds exactly kit.json, its ${kit.files.length} files and ${kit.notices.length} notices`);

console.log('\nevery file is what kit.json says:');
const modeOf = new Map(entries.map((e) => [e.name, e.mode]));
const kitBytes = new Map();
for (const f of [...kit.files, ...kit.notices]) {
  const data = await readFile(join(XDIR, f.path));
  kitBytes.set(f.path, data);
  check(data.length === f.size && sha256(data) === f.sha256, `${f.path} size and sha256`);
  check(modeOf.get(f.path) === (0o100000 | f.mode), `${f.path} is mode ${f.mode.toString(8)} in the zip`);
}

/* ------------------------------------------------ the table it was made from */

console.log('\nkit.json follows public/fw.js:');
const assets = PACKAGE.filter((p) => p.asset);
check(same(kit.files.map((f) => f.id), assets.map((p) => p.id)), 'one file per staged asset, in pack order');
for (const f of kit.files) {
  const p = PART[f.id];
  check(f.member === p.member && f.mode === p.mode && f.option === p.option && f.required === !!p.required,
    `${f.id} → ${f.member}`);
  check(Buffer.compare(kitBytes.get(f.path), await readFile(join(REPO, p.src))) === 0,
    `${f.path} is byte-identical to the committed ${p.src}`);
}
check(same(kit.package.members.map((m) => [m.id, m.option, m.member, m.mode]),
  PACKAGE.map((p) => [p.id, p.option, p.member, p.mode])), 'package.members is PACKAGE, in order');
check(kit.package.members.every((m) => (m.file ? 1 : 0) + (m.fetch ? 1 : 0) + (m.input ? 1 : 0) === 1),
  'every member comes from exactly one of file, fetch and input');
check(kit.package.members.filter((m) => m.file).every((m) => kit.files.some((f) => f.id === m.file)),
  'every member file is in files');
check(kit.package.members.filter((m) => m.fetch).every((m) => kit.fetch[m.fetch]),
  'every fetched member names a fetch source');
check(same(kit.options.map((o) => o.id), ['ssh', 'tailscale', 'kvm', 'uvc', 'player', 'gateway']),
  'options are ssh, tailscale, kvm, uvc, player, gateway, in pack order');
check(same(kit.options.map((o) => o.id), OPTIONS.map((o) => o.id)), 'options are OPTIONS');
for (const o of kit.options) {
  check(same(o.files, kit.files.filter((f) => f.option === o.id).map((f) => f.id)),
    `${o.id} needs ${o.files.join(', ') || 'no kit files'}`);
}
check(kit.package.name === packageName('{tag}'), `package name ${kit.package.name}`);
check(kit.package.tag.unsafe === TAG_UNSAFE_RE.source && kit.package.tag.default === 'web',
  'tag rule is cleanTag\'s');
check(kit.package.tar.mtime === MTIME, `tar mtime ${MTIME}`);

// Every flag the kit sets is one the installer reads, defaulting to off.
const updateSrc = await readFile(join(REPO, 'installer/update'), 'utf8');
const flags = [...kit.options.flatMap((o) => [o.flag, o.ui_flag]).filter(Boolean), kit.package.modules.flag];
for (const f of flags) {
  check(kit.package.build_conf.keys.includes(f), `${f} is a build.conf key`);
  check(new RegExp(`${f}="\\$\\{${f}:-0\\}"`).test(updateSrc), `installer/update reads ${f}, default 0`);
}
check(same(kit.package.build_conf.keys, buildConf({ tag: 'x' }).trim().split('\n').map((l) => l.split('=')[0])),
  'build_conf.keys is buildConf\'s order');

// The reserved names cover every /userdata/bd-* path the installer itself uses.
check(same(kit.package.modules.reserved_names, [...RESERVED_MODULE_NAMES]), 'reserved names are fw.js\'s');
check(kit.package.modules.prefix === MODULE_PREFIX, `modules go under ${MODULE_PREFIX}`);
for (const n of new Set([...updateSrc.matchAll(/\/userdata\/bd-([a-z][a-z-]*)/g)].map((m) => m[1]))) {
  check(RESERVED_MODULE_NAMES.has(n), `module name "${n}" is reserved (installer uses /userdata/bd-${n})`);
}

/* ------------------------------------------------------------- the upstreams */

console.log('\nupstream pins:');
const worker = await readFile(join(REPO, 'src/worker.js'), 'utf8');
const mtxPin = worker.match(/^const MEDIAMTX_VERSION = '(v[0-9.]+)';/m)[1];
const mtx = kit.fetch.mediamtx;
check(mtx.version === mtxPin, `MediaMTX ${mtx.version} is the Worker's pin`);
check((await readFile(join(REPO, 'gateway/render.go'), 'utf8')).includes(`MediaMTXVersion = "${mtx.version}"`),
  'and gateway/render.go\'s');
check(mtx.asset === `mediamtx_${mtx.version}_linux_arm64.tar.gz`, `asset ${mtx.asset}`);
check(mtx.url === `https://github.com/bluenviron/mediamtx/releases/download/${mtx.version}/${mtx.asset}`, 'asset URL');
check(mtx.checksums === `https://github.com/bluenviron/mediamtx/releases/download/${mtx.version}/checksums.sha256`,
  'checksums.sha256 URL');
const ts = kit.fetch.tailscale;
check(ts.index === 'https://pkgs.tailscale.com/stable/?mode=json', 'Tailscale index is pkgs.tailscale.com/stable');
check(ts.tarball === 'https://pkgs.tailscale.com/stable/{file}' && ts.sha256 === `${ts.tarball}.sha256`,
  'Tailscale tarball and its .sha256 sidecar');
check(new RegExp(ts.version_pattern).test('1.90.6') && !new RegExp(ts.version_pattern).test('1.90.6/../x'),
  'Tailscale version pattern');

/* ------------------------------------------------------------------- policy */

console.log('\nwhat the kit must never carry:');
check(!listed.some((p) => /(^|\/)(tailscaled?|mediamtx|mutool)[^/]*$/i.test(p) && !p.startsWith('licenses/')),
  'no Tailscale, MediaMTX or mutool binary');
check(!listed.some((p) => /\.(fw|img|key)$/i.test(p)), 'no firmware file, image or key');
check(![...kitBytes.values()].some((b) => b.includes('bdpff')), 'no reference to the vendor payload');
for (const n of ['NOTICE.md', 'licenses/fuse-exfat-COPYING', 'licenses/libfuse-COPYING.LIB', 'licenses/pdfium/pdfium.txt']) {
  check(kitBytes.has(n), `carries ${n}`);
}
check(/GNU GENERAL PUBLIC LICENSE\s+Version 2/.test(kitBytes.get('licenses/fuse-exfat-COPYING')), 'fuse-exfat\'s licence is GPL v2');
check(/GNU LESSER GENERAL PUBLIC LICENSE\s+Version 2\.1/.test(kitBytes.get('licenses/libfuse-COPYING.LIB')), 'libfuse\'s is LGPL v2.1');
check(/The PDFium Authors/.test(kitBytes.get('licenses/pdfium/pdfium.txt')), 'PDFium\'s notice names The PDFium Authors');
const notice = kitBytes.get('NOTICE.md').toString();
check(/source is offered/.test(notice) && notice.includes('fuse-exfat-1.3.0.tar.gz') && notice.includes('fuse-2.9.9.tar.gz'),
  'NOTICE.md carries the source offer for fuse-exfat and libfuse');
const covered = new Set(kit.notices.flatMap((n) => n.covers));
check(kit.files.every((f) => covered.has(f.id)), 'every file is covered by a notice');

/* ------------------------------------------------- the layout the page packs */

console.log('\nkit.json packs the page\'s package:');

// app.js packs from the same table: no member path or asset name of its own,
// and the table's entries in the table's order.
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

// Rebuild it from kit.json alone: every option on, the kit's own files, and
// the package's own copies of what the kit leaves to the builder.
const tar = new Tar();
const expect = [];
const seen = new Set();
const expectDirs = (path) => {
  const parent = path.slice(0, path.lastIndexOf('/'));
  if (!parent || parent === '.' || seen.has(parent)) return;
  expectDirs(parent);
  seen.add(parent);
  expect.push({ name: parent, dir: true, mode: kit.package.tar.dir_mode });
};
for (const m of kit.package.members) {
  const data = m.file
    ? kitBytes.get(kit.files.find((f) => f.id === m.file).path)
    : pkg.get(m.member)?.data;
  check(data !== undefined, `${m.member} (${m.file ? 'kit' : m.fetch || m.input})`);
  if (data === undefined) continue;
  tar.file(m.member, new Uint8Array(data), m.mode);
  expectDirs(m.member);
  expect.push({ name: m.member, dir: false, mode: m.mode });
}
const mods = [...pkg.values()].filter((e) => e.name.startsWith(kit.package.modules.prefix));
check(mods.length > 0, `the CI package carries a module under ${kit.package.modules.prefix}`);
for (const e of mods) {
  tar.file(e.name, e.data, e.mode);
  expectDirs(e.name);
  expect.push({ name: e.name, dir: false, mode: e.mode });
}

check(same(tarList.map((e) => e.name), expect.map((e) => e.name)),
  `the system tar lists the CI package's ${tarList.length} entries in kit.json's order`);
check(same(tarList.map((e) => [e.dir, e.mode]), expect.map((e) => [e.dir, e.mode])),
  'with kit.json\'s types and modes');
const conf = new TextDecoder().decode(pkg.get(PART.buildConf.member).data);
check(same(conf.trim().split('\n').map((l) => l.split('=')[0]), kit.package.build_conf.keys),
  'its build.conf has build_conf.keys, in order');
const rebuilt = new Uint8Array(await tar.blob().arrayBuffer());
check(rebuilt.length === citestTar.length && Buffer.compare(rebuilt, citestTar) === 0,
  `a package built from kit.json is byte-identical to the CI package (${(rebuilt.length / 1048576).toFixed(1)} MB tar)`);

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed');
process.exit(failures ? 1 : 0);
