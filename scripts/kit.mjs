// Stage the release kit and write its kit.json. scripts/make-kit.sh runs this
// after build-assets.sh and zips what it leaves in <stage>.
//
//   node scripts/kit.mjs <stage-dir>
//
// The kit is what Burrow builds .fw packages from, because the page's own
// assets are served from its origin and not from any GitHub release. It is:
//
//   assets/    public/assets/ as build-assets.sh wrote it, manifest.json
//              included, minus the page-only support-footer-version.js
//   NOTICES/   the licence texts and source offer the page shows
//   kit.json   the upstream pins and module rules, read from src/worker.js
//              and public/fw.js, never typed in here
//
// Burrow packs from assets/ the way public/app.js does, so where each file goes
// is app.js's business (its table is PACKAGE in fw.js), not kit.json's.
// test/kit.mjs builds the kit and checks it.

import { readFile, writeFile, mkdir, copyFile, chmod, readdir, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join, relative } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '..');
const ASSETS = join(REPO, 'public/assets');
const STAGE = process.argv[2];
if (!STAGE) {
  console.error('usage: node scripts/kit.mjs <stage-dir>');
  process.exit(2);
}

const die = (msg) => {
  console.error(`error: ${msg}`);
  process.exit(1);
};

// The one generated asset that belongs to the page, not to a package.
const PAGE_ONLY = ['support-footer-version.js'];

const { PACKAGE, RESERVED_MODULE_NAMES, MODULE_MAX_BYTES } = await import(join(REPO, 'public/fw.js'));
const { version } = JSON.parse(await readFile(join(REPO, 'package.json'), 'utf8'));
const manifest = JSON.parse(await readFile(join(ASSETS, 'manifest.json'), 'utf8'));

// The upstream hosts and pins, read out of the Worker the page fetches through.
const worker = await readFile(join(REPO, 'src/worker.js'), 'utf8');
const pin = (re, what) => (worker.match(re) || die(`src/worker.js: no ${what} — update scripts/kit.mjs`))[1];
const TS_BASE = pin(/^const UPSTREAM = '([^']+)';$/m, 'UPSTREAM');
const TS_ARCH = pin(/data\.Tarballs && data\.Tarballs\.([a-z0-9]+);/, 'Tarballs.<arch>');
const MTX_VERSION = pin(/^const MEDIAMTX_VERSION = '(v[0-9.]+)';/m, 'MEDIAMTX_VERSION');
const MTX_BASE = pin(/^const MEDIAMTX_BASE = '([^']+)';$/m, 'MEDIAMTX_BASE');
const MTX_SUMS = pin(/\$\{MEDIAMTX_BASE\}\$\{v\}\/([A-Za-z0-9._-]+)`/, 'MediaMTX checksums file');
const MTX_FILE = pin(/^const mediamtxFile = \(v\) => `(mediamtx_\$\{v\}_[a-z0-9_]+\.tar\.gz)`;$/m, 'mediamtxFile')
  .replace('${v}', MTX_VERSION);
if (!worker.includes(`fetch(\`\${UPSTREAM}?mode=json\``)) die('src/worker.js: the Tailscale index is no longer UPSTREAM?mode=json');

const commit = execFileSync('git', ['-C', REPO, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');

// A release kit carries every payload. build-assets.sh tolerates a missing
// binary (the page then disables that option); a kit missing one is broken.
for (const p of PACKAGE.filter((x) => x.asset)) {
  const meta = manifest[p.id];
  if (!meta) die(`public/assets/manifest.json has no ${p.id} — is ${p.src} committed?`);
  const data = await readFile(join(ASSETS, p.asset));
  if (data.length !== meta.size || sha256(data) !== meta.sha256) {
    die(`public/assets/${p.asset} does not match manifest.json — rerun scripts/build-assets.sh`);
  }
}

// Copy a file into the stage. Modes come out as 0755 or 0644, from the
// source's exec bit, so the zip says the same whatever the umask was.
async function stage(from, path) {
  const to = join(STAGE, path);
  await mkdir(dirname(to), { recursive: true });
  await copyFile(from, to);
  await chmod(to, (await stat(from)).mode & 0o111 ? 0o755 : 0o644);
}

async function walk(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, e.name);
    if (e.isSymbolicLink()) die(`${full} is a symlink — the kit carries none`);
    if (e.isDirectory()) out.push(...(await walk(full)));
    else if (e.isFile()) out.push(full);
  }
  return out;
}

let assets = 0;
for (const full of await walk(ASSETS)) {
  const rel = relative(ASSETS, full);
  if (PAGE_ONLY.includes(rel)) continue;
  await stage(full, `assets/${rel}`);
  assets++;
}

// Licence notices and the source offer. They travel with the kit, not in a
// .fw: the page's packages carry none of them either, apart from MediaMTX's
// LICENSE, which comes out of the MediaMTX tarball.
await stage(join(REPO, 'kit/NOTICE.md'), 'NOTICES/NOTICE.md');
await stage(join(REPO, 'LICENSE'), 'NOTICES/birddog-play-patcher-LICENSE');
let notices = 2;
for (const full of await walk(join(REPO, 'kit/licenses'))) {
  await stage(full, `NOTICES/${relative(join(REPO, 'kit/licenses'), full)}`);
  notices++;
}

const kit = {
  kit: 1,
  version,
  commit,
  mediamtx: { version: MTX_VERSION, file: MTX_FILE, base: MTX_BASE, checksums: MTX_SUMS },
  tailscale: { index: `${TS_BASE}?mode=json`, base: TS_BASE, arch: TS_ARCH },
  reservedModuleNames: [...RESERVED_MODULE_NAMES],
  moduleMaxBytes: MODULE_MAX_BYTES,
};

await writeFile(join(STAGE, 'kit.json'), JSON.stringify(kit, null, 2) + '\n');
await chmod(join(STAGE, 'kit.json'), 0o644);
console.log(`kit.json: ${assets} assets, ${notices} notices, commit ${commit.slice(0, 7)}`);
