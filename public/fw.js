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

export function parseTar(buf) {
  const out = [];
  const view = new Uint8Array(buf);
  const dec = new TextDecoder();
  let off = 0;

  while (off + BLOCK <= view.length) {
    const header = view.subarray(off, off + BLOCK);
    if (header.every((b) => b === 0)) break; // end-of-archive

    const str = (o, l) => {
      const raw = dec.decode(header.subarray(o, o + l));
      const nul = raw.indexOf('\0');
      return (nul === -1 ? raw : raw.slice(0, nul)).trim();
    };

    const name = str(0, 100);
    const size = parseInt(str(124, 12), 8) || 0;
    const typeflag = str(156, 1) || '0';
    off += BLOCK;

    if (typeflag === '0' || typeflag === '') {
      out.push({ name, data: view.subarray(off, off + size) });
    }
    off += Math.ceil(size / BLOCK) * BLOCK;
  }
  return out;
}

export async function gunzip(buf) {
  const stream = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
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

/** ELF magic, EI_CLASS=2 (64-bit), e_machine 0xB7 (AArch64) little-endian at +18. */
export function isArm64Elf(d) {
  return (
    d.length > 20 &&
    d[0] === 0x7f && d[1] === 0x45 && d[2] === 0x4c && d[3] === 0x46 &&
    d[4] === 2 && d[18] === 0xb7 && d[19] === 0x00
  );
}

export function buildConf({ tag, withTailscale, withKvm, doReboot }) {
  return (
    `BUILD_TAG=${tag}\n` +
    `WITH_TAILSCALE=${withTailscale ? 1 : 0}\n` +
    `WITH_KVM=${withKvm ? 1 : 0}\n` +
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
