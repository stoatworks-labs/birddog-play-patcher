// Stage the release kit and write its kit.json. scripts/make-kit.sh runs this
// after build-assets.sh and zips what it leaves in <stage>.
//
//   node scripts/kit.mjs <stage-dir>
//
// The kit is what Burrow builds .fw packages from, because the page's own
// assets are served from its origin and not from any GitHub release. Every
// member path, mode and option below comes from PACKAGE and OPTIONS in
// public/fw.js, the table app.js packs from, and the upstream pins come from
// src/worker.js, so the kit cannot describe a different package from the page.
// test/kit.mjs builds a package from the kit and checks it against the page's.

import { readFile, writeFile, mkdir, copyFile, chmod, readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';

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

const {
  PACKAGE, PART, OPTIONS, partsOf, MTIME, buildConf, packageName, TAG_DEFAULT, TAG_UNSAFE_RE,
  MODULE_PREFIX, MODULE_NAME_RE, MODULE_VERSION_RE, MODULE_MAX_BYTES, RESERVED_MODULE_NAMES,
} = await import(join(REPO, 'public/fw.js'));

const { version } = JSON.parse(await readFile(join(REPO, 'package.json'), 'utf8'));
const manifest = JSON.parse(await readFile(join(ASSETS, 'manifest.json'), 'utf8'));

// The upstream hosts and pins, read out of the Worker the page fetches through.
const worker = await readFile(join(REPO, 'src/worker.js'), 'utf8');
const pin = (re, what) => (worker.match(re) || die(`src/worker.js: no ${what}`))[1];
const TS_BASE = pin(/^const UPSTREAM = '([^']+)';$/m, 'UPSTREAM');
const TS_VERSION_RE = pin(/^const VERSION_RE = \/(.+)\/;$/m, 'VERSION_RE');
const MTX_VERSION = pin(/^const MEDIAMTX_VERSION = '(v[0-9.]+)';/m, 'MEDIAMTX_VERSION');
const MTX_BASE = pin(/^const MEDIAMTX_BASE = '([^']+)';$/m, 'MEDIAMTX_BASE');
if (!worker.includes('const mediamtxFile = (v) => `mediamtx_${v}_linux_arm64.tar.gz`;')) {
  die('src/worker.js: mediamtxFile no longer names mediamtx_<v>_linux_arm64.tar.gz — update scripts/kit.mjs');
}
const MTX_ASSET = `mediamtx_${MTX_VERSION}_linux_arm64.tar.gz`;

const git = (...args) => execFileSync('git', ['-C', REPO, ...args], { encoding: 'utf8' }).trim();
const commit = git('rev-parse', 'HEAD');
const dirty = git('status', '--porcelain', '--untracked-files=no') !== '';

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');

// Copy one file into the stage at `path` with `mode`, and describe it.
async function stage(from, path, mode) {
  const to = join(STAGE, path);
  await mkdir(dirname(to), { recursive: true });
  await copyFile(from, to);
  await chmod(to, mode);
  const data = await readFile(to);
  return { path, size: data.length, sha256: sha256(data), mode };
}

// Installer scripts keep their folder name; everything else is a binary.
const kitPath = (p) => (p.src.startsWith('installer/') ? `installer/${p.asset}` : `bin/${p.asset}`);

const files = [];
for (const p of PACKAGE.filter((x) => x.asset)) {
  // A release kit carries every payload. build-assets.sh tolerates a missing
  // binary (the page disables that option); a kit missing one is a broken kit.
  const meta = manifest[p.id];
  if (!meta) die(`public/assets/manifest.json has no ${p.id} — is ${p.src} committed?`);
  const f = await stage(join(ASSETS, p.asset), kitPath(p), p.mode);
  if (meta.size !== f.size || meta.sha256 !== f.sha256) {
    die(`${p.asset} does not match public/assets/manifest.json — rerun scripts/build-assets.sh`);
  }
  if (sha256(await readFile(join(REPO, p.src))) !== f.sha256) {
    die(`public/assets/${p.asset} is not ${p.src} — rerun scripts/build-assets.sh`);
  }
  files.push({ id: p.id, ...f, member: p.member, option: p.option, required: !!p.required });
}

// Licence notices and the source offer. They travel with the kit, not in the
// .fw: the page's packages carry none of them either, apart from MediaMTX's
// LICENSE, which comes out of the MediaMTX tarball.
const notices = [
  { ...(await stage(join(REPO, 'kit/NOTICE.md'), 'NOTICE.md', 0o644)), covers: ['all'] },
  {
    ...(await stage(join(REPO, 'LICENSE'), 'licenses/birddog-play-patcher-LICENSE', 0o644)),
    covers: ['update', 'probe', 'kvmRun', 'camRun', 'camApiRun', 'gwRun', 'gwApiRun',
      'bdts', 'bdkvm', 'bdcam', 'bdplay', 'bdpdf', 'bdgw'],
  },
  { ...(await stage(join(REPO, 'kit/licenses/fuse-exfat-COPYING'), 'licenses/fuse-exfat-COPYING', 0o644)), covers: ['exfat'] },
  { ...(await stage(join(REPO, 'kit/licenses/libfuse-COPYING.LIB'), 'licenses/libfuse-COPYING.LIB', 0o644)), covers: ['exfat'] },
];
for (const name of (await readdir(join(REPO, 'kit/licenses/pdfium'))).sort()) {
  notices.push({
    ...(await stage(join(REPO, 'kit/licenses/pdfium', name), `licenses/pdfium/${name}`, 0o644)),
    covers: ['pdfium'],
  });
}

const fetchOf = (option) => [...new Set(partsOf(option).filter((p) => p.fetch).map((p) => p.fetch))];

const kit = {
  schema: 1,
  name: 'birddog-play-patcher-kit',
  version,
  source: { repo: 'https://github.com/stoatworks-labs/birddog-play-patcher', commit, dirty },

  // How to put a .fw together. `members` is the whole package in order; skip
  // the ones whose option is off, and an asset that is not `required` only
  // if it is absent. Modes are the permission bits as plain integers (493 is
  // 0755).
  package: {
    name: packageName('{tag}'),
    tag: { default: TAG_DEFAULT, unsafe: TAG_UNSAFE_RE.source, replace_with: '-' },
    compression: 'gzip',
    tar: {
      format: 'ustar',
      mtime: MTIME,
      uid: 0,
      gid: 0,
      uname: 'root',
      gname: 'root',
      name_max: 100,
      dir_mode: 0o755,
      dirs: 'each parent directory of a member gets its own entry, "<path>/" with typeflag 5, ' +
        'once, just before the first member under it, parents first; "." never',
    },
    members: PACKAGE.map((p) => ({
      id: p.id,
      option: p.option,
      member: p.member,
      mode: p.mode,
      required: !!p.required,
      ...(p.asset ? { file: p.id } : p.fetch ? { fetch: p.fetch, take: p.take } : { input: p.input }),
    })),
    build_conf: {
      member: PART.buildConf.member,
      keys: buildConf({ tag: 'x' }).trim().split('\n').map((l) => l.split('=')[0]),
      format: 'KEY=value lines in this order, each ending in LF. BUILD_TAG is the cleaned tag; ' +
        'every other value is 1 or 0. A ui_flag is 1 only when its option\'s flag is. ' +
        'WITH_MODULES is 1 exactly when the package carries modules. The installer sources this ' +
        'file as root.',
    },
    modules: {
      flag: 'WITH_MODULES',
      prefix: MODULE_PREFIX,
      after: 'every option member',
      name_pattern: MODULE_NAME_RE.source,
      version_pattern: MODULE_VERSION_RE.source,
      max_bytes: MODULE_MAX_BYTES,
      reserved_names: [...RESERVED_MODULE_NAMES],
    },
  },

  options: OPTIONS.map((o) => ({
    id: o.id,
    label: o.label,
    flag: o.flag,
    ui_flag: o.uiFlag || null,
    ui_choice: !!o.uiChoice,
    default: o.default,
    files: partsOf(o.id).filter((p) => p.asset).map((p) => p.id),
    fetch: fetchOf(o.id),
    inputs: partsOf(o.id).filter((p) => p.input).map((p) => p.input),
  })),

  files,

  // Fetched by whoever builds the package, never shipped here.
  fetch: {
    tailscale: {
      index: `${TS_BASE}?mode=json`,
      version_field: 'TarballsVersion',
      file_field: 'Tarballs.arm64',
      version_pattern: TS_VERSION_RE,
      tarball: `${TS_BASE}{file}`,
      sha256: `${TS_BASE}{file}.sha256`,
      sha256_format: 'the first whitespace-separated token, 64 lowercase hex digits',
      take: 'by path below the tarball\'s top-level directory; tailscaled must be an aarch64 ELF',
    },
    mediamtx: {
      version: MTX_VERSION,
      asset: MTX_ASSET,
      url: `${MTX_BASE}${MTX_VERSION}/${MTX_ASSET}`,
      checksums: `${MTX_BASE}${MTX_VERSION}/checksums.sha256`,
      checksums_format: '"<sha256> <file>" lines, the name optionally starting with *; the asset\'s line is required',
      take: 'by basename anywhere in the tarball; mediamtx must be an aarch64 ELF, LICENSE is optional',
    },
  },

  notices,
};

await writeFile(join(STAGE, 'kit.json'), JSON.stringify(kit, null, 2) + '\n');
await chmod(join(STAGE, 'kit.json'), 0o644);
console.log(`kit.json: ${files.length} files, ${notices.length} notices, commit ${commit.slice(0, 7)}${dirty ? ' (dirty)' : ''}`);
