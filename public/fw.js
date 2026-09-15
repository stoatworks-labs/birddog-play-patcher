// Archive primitives for the PLAY package generator.
//
// Deliberately free of DOM and network calls so the exact code the browser runs
// can also be exercised from Node — see test/build-package.mjs, which builds a
// package with this module and diffs it against tools/fwbuild/build.sh output.
//
// A valid package is a gzip'd tar with an executable `update` at the top level
// (notes/01, notes/04). No signature, no encryption, no vendor key.

export const BLOCK = 512;
// Fixed mtime makes the *tar* layer reproducible: identical inputs give a
// byte-identical archive, verified equal between Chrome and Node. The gzip
// layer is not — engines pick different zlib settings, so the same tar
// compresses to a different .fw in each. Compare tar digests, not .fw digests.
// Zero would make GNU tar on the device warn about an implausibly old
// timestamp on every member.
export const MTIME = 1577836800; // 2020-01-01T00:00:00Z

const enc = new TextEncoder();

function octal(value, width) {
  // ustar numeric fields are NUL-terminated octal, zero padded.
  return value.toString(8).padStart(width - 1, '0') + '\0';
}

export function tarHeader(name, size, mode, typeflag = '0') {
  const h = new Uint8Array(BLOCK);
  const put = (str, offset, len) => {
    const bytes = enc.encode(str);
    if (bytes.length > len) throw new Error(`tar field overflow: ${str}`);
    h.set(bytes.subarray(0, len), offset);
  };

  if (enc.encode(name).length > 100) throw new Error(`tar name too long: ${name}`);
  put(name, 0, 100);
  put(octal(mode, 8), 100, 8);
  put(octal(0, 8), 108, 8); // uid
  put(octal(0, 8), 116, 8); // gid
  put(octal(size, 12), 124, 12);
  put(octal(MTIME, 12), 136, 12);
  h.fill(0x20, 148, 156); // checksum field reads as spaces while summing
  put(typeflag, 156, 1);
  put('ustar\0', 257, 6);
  put('00', 263, 2);
  put('root', 265, 32);
  put('root', 297, 32);

  let sum = 0;
  for (const b of h) sum += b;
  put(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8);
  return h;
}

export class Tar {
  constructor() {
    this.parts = [];
    this.dirs = new Set();
  }

  /** @param {string} path directory path with no trailing slash, e.g. "./userdata" */
  dir(path) {
    if (this.dirs.has(path)) return;
    const parent = path.slice(0, path.lastIndexOf('/'));
    if (parent && parent !== '.') this.dir(parent);
    this.dirs.add(path);
    this.parts.push(tarHeader(path + '/', 0, 0o755, '5'));
  }

  file(path, data, mode) {
    const parent = path.slice(0, path.lastIndexOf('/'));
    if (parent && parent !== '.') this.dir(parent);
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    this.parts.push(tarHeader(path, bytes.length, mode));
    this.parts.push(bytes);
    const pad = (BLOCK - (bytes.length % BLOCK)) % BLOCK;
    if (pad) this.parts.push(new Uint8Array(pad));
  }

  text(path, str, mode) {
    this.file(path, enc.encode(str), mode);
  }

  /** Two zero blocks terminate the archive. */
  blob() {
    return new Blob([...this.parts, new Uint8Array(BLOCK * 2)]);
  }

  async gzip() {
    const stream = this.blob().stream().pipeThrough(new CompressionStream('gzip'));
    return new Response(stream).blob();
  }
}

/**
 * Every member of a tar, as the device's GNU tar would see it:
 * { name, data, mode, type }, where type is the ustar typeflag ('0' file,
 * '5' directory, '2' symlink, …). Long names arrive intact whether the writer
 * used a POSIX ustar prefix, a pax extended header or a GNU ././@LongLink;
 * the pax/GNU carrier records themselves are consumed and never returned.
 *
 * This is what reading a user-supplied module archive needs. The Tailscale
 * extractor only wants regular files, and gets them through parseTar below.
 */
export function tarEntries(buf) {
  const out = [];
  const view = new Uint8Array(buf);
  const dec = new TextDecoder();
  let off = 0;
  let pending = {}; // name / linkname overrides for the next real member

  const cstr = (bytes) => {
    const nul = bytes.indexOf(0);
    return dec.decode(nul === -1 ? bytes : bytes.subarray(0, nul));
  };

  while (off + BLOCK <= view.length) {
    const header = view.subarray(off, off + BLOCK);
    if (header.every((b) => b === 0)) break; // end-of-archive

    const str = (o, l) => cstr(header.subarray(o, o + l)).trim();
    const num = (o, l) => parseInt(str(o, l), 8) || 0;

    let name = str(0, 100);
    const mode = num(100, 8);
    const size = num(124, 12);
    const type = str(156, 1) || '0';
    const magic = cstr(header.subarray(257, 263));
    // POSIX ustar splits a long name across prefix + name. GNU's own format
    // reuses those bytes for other fields, so only honour it for real ustar.
    if (magic === 'ustar' && header[263] === 0x30) {
      const prefix = str(345, 155);
      if (prefix) name = `${prefix}/${name}`;
    }
    off += BLOCK;
    const data = view.subarray(off, off + size);
    off += Math.ceil(size / BLOCK) * BLOCK;

    if (type === 'L') { // GNU long name: the data is the next member's name
      pending.name = cstr(data);
      continue;
    }
    if (type === 'K') { // GNU long link target
      pending.linkname = cstr(data);
      continue;
    }
    if (type === 'x' || type === 'g') {
      // pax header: "<len> key=value\n" records, where <len> counts BYTES of
      // the whole record — so walk the raw bytes and decode each record on
      // its own, or a non-ASCII path shifts every record after it.
      const recs = paxRecords(data);
      if (type === 'g') {
        // POSIX lets a global header override path for every later member.
        // No tar in use writes one, and honouring it silently would be
        // worse than refusing: a reader that ignored it would stage members
        // under the wrong names. Refuse, and say what to repack with.
        if ('path' in recs || 'linkpath' in recs) {
          throw new Error('archive uses a global pax path override — repack it as plain ustar (tar --format=ustar)');
        }
        continue;
      }
      if ('path' in recs) pending.name = recs.path;
      if ('linkpath' in recs) pending.linkname = recs.linkpath;
      continue;
    }

    if (pending.name) name = pending.name;
    const linkname = pending.linkname || str(157, 100);
    pending = {};
    out.push({ name, data, mode, type, linkname });
  }
  return out;
}

/**
 * The key=value records of one pax header, split on byte lengths. Each record
 * is "<len> key=value\n" with <len> the byte count of the whole record, newline
 * included. Anything that does not frame that way is refused: this is the
 * archive gate, and a record that misdeclares its length would rename the
 * member after it.
 */
export function paxRecords(bytes) {
  const dec = new TextDecoder();
  const out = {};
  let p = 0;
  while (p < bytes.length) {
    let sp = p;
    while (sp < bytes.length && bytes[sp] !== 0x20) sp++;
    const lenText = dec.decode(bytes.subarray(p, sp));
    const len = /^[1-9][0-9]*$/.test(lenText) ? parseInt(lenText, 10) : 0;
    if (!len || len <= sp - p + 1 || p + len > bytes.length || bytes[p + len - 1] !== 0x0a) {
      throw new Error('malformed pax header record');
    }
    const rec = dec.decode(bytes.subarray(sp + 1, p + len - 1)); // drop the trailing \n
    const eq = rec.indexOf('=');
    if (eq > 0) out[rec.slice(0, eq)] = rec.slice(eq + 1);
    p += len;
  }
  return out;
}

/** Regular files only — what the Tailscale extractor and the tests want. */
export function parseTar(buf) {
  return tarEntries(buf)
    .filter((e) => e.type === '0' || e.type === '7')
    .map(({ name, data, mode }) => ({ name, data, mode }));
}

export async function gunzip(buf) {
  const stream = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
}

/**
 * gunzip that gives up as soon as the output passes `limit` bytes, instead of
 * materialising the whole thing first. A few hundred KB of gzip'd zeros
 * unpack to gigabytes; a user-supplied module must be refused before that
 * fills the tab, not after.
 */
export async function gunzipBounded(buf, limit) {
  const reader = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
  const chunks = [];
  let total = 0;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > limit) {
      await reader.cancel();
      throw new Error(`unpacks to more than ${humanSize(limit)} — refusing to read further`);
    }
    chunks.push(value);
  }
  const out = new Uint8Array(total);
  let off = 0;
  for (const c of chunks) { out.set(c, off); off += c.byteLength; }
  return out;
}

export async function sha256Hex(input) {
  let buf = input;
  if (input instanceof Blob) buf = await input.arrayBuffer();
  else if (ArrayBuffer.isView(input)) {
    buf = input.buffer.slice(input.byteOffset, input.byteOffset + input.byteLength);
  }
  const digest = await crypto.subtle.digest('SHA-256', buf);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Pull the two binaries out of an official tailscale_<ver>_arm64.tgz.
 * Equivalent to build.sh's `tar xzf --strip-components=1` plus `rm -rf systemd`.
 */
export async function extractTailscale(buf) {
  const wanted = {};
  for (const e of parseTar(await gunzip(buf))) {
    const rel = e.name.replace(/^\.\//, '').split('/').slice(1).join('/');
    if (rel === 'tailscaled' || rel === 'tailscale') wanted[rel] = e.data;
  }
  if (!wanted.tailscaled || !wanted.tailscale) {
    throw new Error('that tarball does not contain both tailscaled and tailscale');
  }
  if (!isArm64Elf(wanted.tailscaled)) {
    throw new Error('tailscaled in that tarball is not an aarch64 ELF — wrong architecture');
  }
  return wanted;
}

/**
 * Pull the `mediamtx` binary out of a MediaMTX release tarball. The tarball is
 * flat — mediamtx, mediamtx.yml and LICENSE at the top level — but a leading
 * "./" or a version directory is tolerated, the same way extractTailscale
 * tolerates the tarball's own top-level directory.
 * @returns {Promise<{ mediamtx: Uint8Array, license: Uint8Array|null }>}
 */
export async function extractMediaMTX(buf) {
  let mediamtx = null;
  let license = null;
  for (const e of parseTar(await gunzip(buf))) {
    const base = e.name.replace(/^\.\//, '').split('/').pop();
    if (base === 'mediamtx') mediamtx = e.data;
    if (base === 'LICENSE') license = e.data;
  }
  if (!mediamtx) throw new Error('that tarball does not contain a mediamtx binary');
  if (!isArm64Elf(mediamtx)) {
    throw new Error('mediamtx in that tarball is not an aarch64 ELF — wrong architecture');
  }
  return { mediamtx, license };
}

/** ELF magic, EI_CLASS=2 (64-bit), e_machine 0xB7 (AArch64) little-endian at +18. */
export function isArm64Elf(d) {
  return (
    d.length > 20 &&
    d[0] === 0x7f && d[1] === 0x45 && d[2] === 0x4c && d[3] === 0x46 &&
    d[4] === 2 && d[18] === 0xb7 && d[19] === 0x00
  );
}

export function isElf(d) {
  return d.length > 4 && d[0] === 0x7f && d[1] === 0x45 && d[2] === 0x4c && d[3] === 0x46;
}

/* ------------------------------------------------------------------ modules */

// Third-party modules: a directory the installer runs `install` from, carried
// in the package at ./modules/<name>/. The contract is documented on
// modules.html and implemented on the device by installer/update; the template
// at github.com/stoatworks-labs/bd-play-module-template builds archives that
// satisfy it. Everything below is structural — what `install` does as root is
// the author's business and cannot be checked from a browser.

export const MODULE_PREFIX = './modules/';
export const MODULE_NAME_RE = /^[a-z0-9][a-z0-9-]{0,31}$/;
export const MODULE_VERSION_RE = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$/;
// Names whose /userdata/bd-<name> or unit would collide with a built-in payload.
export const RESERVED_MODULE_NAMES = new Set([
  'tailscale', 'tailscale-ui', 'kvm', 'cam', 'cam-api', 'play', 'probe', 'modules',
  'gateway', 'gw', 'mtx',
]);
// The updater extracts the whole package into a temp dir on the device before
// running anything. 68 MB of Tailscale is proven; this is a ceiling, not a budget.
export const MODULE_MAX_BYTES = 256 * 1024 * 1024;

/** Shell-style KEY=value, one per line. Read, never sourced — same as the installer. */
export function parseModuleConf(text) {
  const conf = {};
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const m = line.match(/^([A-Z][A-Z0-9_]*)=(.*)$/);
    if (!m) continue;
    let v = m[2].trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
      v = v.slice(1, -1);
    }
    conf[m[1]] = v;
  }
  return conf;
}

/**
 * Read a module archive — a .tar, .tgz or .tar.gz — the way the installer
 * will see it, and refuse anything the package format or the device cannot
 * carry. Resolves to
 *   { name, version, description, homepage, files: [{ path, data, mode }], size }
 * or rejects with a message written for the build log.
 *
 * Accepted layouts: module.conf and install at the archive root, or the whole
 * module inside one top-level directory (what `tar czf x.tgz mymodule/` makes).
 */
export async function readModule(buf, label = 'module', { maxBytes = MODULE_MAX_BYTES } = {}) {
  let bytes = new Uint8Array(buf);
  if (bytes.length > 1 && bytes[0] === 0x50 && bytes[1] === 0x4b) {
    throw new Error(`${label}: that is a zip file — a module is a .tar.gz (tar czf …)`);
  }
  if (bytes.length > maxBytes) {
    throw new Error(`${label}: ${humanSize(bytes.length)} is more than the ${humanSize(maxBytes)} ceiling`);
  }
  if (bytes.length > 1 && bytes[0] === 0x1f && bytes[1] === 0x8b) {
    // The tar can be a little larger than its files (headers, padding); the
    // precise check on the files' own bytes comes after parsing.
    try {
      bytes = await gunzipBounded(bytes, maxBytes + 1024 * 1024);
    } catch (err) {
      throw new Error(`${label}: ${err.message}`);
    }
  }
  if (bytes.length < BLOCK * 2) throw new Error(`${label}: too small to be a tar archive`);
  const magic = new TextDecoder().decode(bytes.subarray(257, 262));
  if (magic !== 'ustar') throw new Error(`${label}: not a tar archive (no ustar magic)`);

  // Normalise names and drop what a Mac leaves in a tarball. A leading "/"
  // (an archive made with -P) is stripped the way tar itself strips it on
  // extraction; every member is re-rooted under ./modules/<name>/ below, so
  // an absolute name can never place anything outside the module.
  const junk = (p) => /(^|\/)(\._[^/]*|\.DS_Store|__MACOSX(\/.*)?|PaxHeaders?(\.\d+)?\/.*)$/.test(p);
  const entries = [];
  let members;
  try {
    members = tarEntries(bytes);
  } catch (err) {
    throw new Error(`${label}: ${err.message}`);
  }
  for (const e of members) {
    let p = e.name.replace(/^(\.\/|\/)+/, '').replace(/\/+$/, '');
    if (!p || p === '.') continue;
    if (p.split('/').includes('..')) throw new Error(`${label}: path escapes the archive: ${e.name}`);
    if (junk(p)) continue;
    if (e.type === '1' || e.type === '2') {
      throw new Error(
        `${label}: ${p} is a ${e.type === '2' ? 'symlink' : 'hard link'} — the package format does ` +
        'not carry links; create it from install instead',
      );
    }
    if (e.type !== '0' && e.type !== '7' && e.type !== '5') {
      throw new Error(`${label}: ${p} has unsupported tar member type '${e.type}'`);
    }
    entries.push({ ...e, path: p });
  }
  if (!entries.length) throw new Error(`${label}: the archive is empty`);

  // A single top-level directory wrapping everything is stripped.
  const files = entries.filter((e) => e.type !== '5');
  if (!files.some((e) => e.path === 'module.conf')) {
    const tops = new Set(entries.map((e) => e.path.split('/')[0]));
    if (tops.size === 1 && files.some((e) => e.path.split('/').length > 1)) {
      const top = [...tops][0];
      for (const e of entries) e.path = e.path.split('/').slice(1).join('/');
      if (!files.some((e) => e.path === 'module.conf')) {
        throw new Error(`${label}: no module.conf at the root (looked at the top level and inside ${top}/)`);
      }
    } else {
      throw new Error(`${label}: no module.conf at the root of the archive`);
    }
  }
  const byPath = new Map(files.filter((e) => e.path).map((e) => [e.path, e]));
  if (!byPath.has('install')) {
    throw new Error(`${label}: no install script at the root — the installer runs modules/<name>/install`);
  }
  if (!byPath.get('install').data.length) throw new Error(`${label}: install is empty`);

  const conf = parseModuleConf(new TextDecoder().decode(byPath.get('module.conf').data));
  const name = conf.NAME || '';
  if (!MODULE_NAME_RE.test(name)) {
    throw new Error(
      `${label}: module.conf NAME="${name}" — needs 1–32 characters of a-z, 0-9 and -, ` +
      'starting with a letter or digit (it becomes /userdata/bd-<name>)',
    );
  }
  if (RESERVED_MODULE_NAMES.has(name)) {
    throw new Error(`${label}: NAME="${name}" collides with a payload this generator already ships`);
  }
  const version = conf.VERSION || '';
  if (!MODULE_VERSION_RE.test(version)) {
    throw new Error(`${label}: module.conf VERSION="${version}" — needs 1–32 characters of A-Z, a-z, 0-9, . _ + -`);
  }

  // The device's tar reads ustar names of at most 100 bytes, and tarHeader
  // refuses longer ones rather than truncating; say which path, and by how much.
  let size = 0;
  const out = [];
  for (const e of [...byPath.values()].sort((a, b) => (a.path < b.path ? -1 : 1))) {
    const full = `${MODULE_PREFIX}${name}/${e.path}`;
    const len = enc.encode(full).length;
    if (len > 100) {
      throw new Error(
        `${label}: path too long for the package format — "${full}" is ${len} bytes, ` +
        `the limit is 100. Shorten it by ${len - 100}.`,
      );
    }
    if (isElf(e.data) && !isArm64Elf(e.data)) {
      throw new Error(`${label}: ${e.path} is an ELF binary but not aarch64 — it cannot run on a PLAY`);
    }
    const exec = e.path === 'install' || e.path === 'uninstall' || (e.mode & 0o111) !== 0;
    out.push({ path: e.path, data: e.data, mode: exec ? 0o755 : 0o644 });
    size += e.data.length;
  }
  if (size > maxBytes) {
    throw new Error(
      `${label}: ${humanSize(size)} unpacked is more than the ${humanSize(maxBytes)} ceiling — ` +
      'the updater extracts the whole package into a temp dir on the device',
    );
  }

  return {
    name,
    version,
    description: conf.DESCRIPTION || '',
    homepage: conf.HOMEPAGE || '',
    files: out,
    size,
  };
}

/** Stage a module read by readModule under ./modules/<name>/ in the package. */
export function addModule(tar, mod) {
  for (const f of mod.files) tar.file(`${MODULE_PREFIX}${mod.name}/${f.path}`, f.data, f.mode);
}

export function buildConf({
  tag, withTailscale, withTailscaleUi, withKvm, withPlay, withCam, withCamUi,
  withGateway, withGatewayUi, withModules, doReboot,
}) {
  return (
    `BUILD_TAG=${tag}\n` +
    `WITH_TAILSCALE=${withTailscale ? 1 : 0}\n` +
    `WITH_TAILSCALE_UI=${withTailscale && withTailscaleUi ? 1 : 0}\n` +
    `WITH_KVM=${withKvm ? 1 : 0}\n` +
    `WITH_PLAY=${withPlay ? 1 : 0}\n` +
    `WITH_CAM=${withCam ? 1 : 0}\n` +
    `WITH_CAM_UI=${withCam && withCamUi ? 1 : 0}\n` +
    `WITH_GATEWAY=${withGateway ? 1 : 0}\n` +
    `WITH_GATEWAY_UI=${withGateway && withGatewayUi ? 1 : 0}\n` +
    `WITH_MODULES=${withModules ? 1 : 0}\n` +
    `DO_REBOOT=${doReboot ? 1 : 0}\n`
  );
}

// The OpenSSH single-line public key formats sshd will actually load.
const KEY_RE =
  /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+[A-Za-z0-9+/]+={0,3}(\s+\S.*)?$/;

/** @returns {string|null} an error message, or null if the key is usable. */
export function validateKey(text) {
  const line = (text || '').trim();
  if (!line) return 'Paste the public key you want to authorise for root.';
  if (line.includes('\n')) return 'That is more than one line — paste a single public key.';
  if (line.startsWith('-----BEGIN')) return 'That is a PRIVATE key. Paste the .pub file instead.';
  if (!KEY_RE.test(line)) return 'That does not look like an OpenSSH public key (ssh-ed25519 AAAA…).';
  return null;
}

export function humanSize(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
