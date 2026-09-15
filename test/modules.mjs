// readModule and tarEntries against archives written by REAL tar programs —
// bsdtar on a Mac, GNU tar in CI — never by fw.js's own writer. The page's
// reader agreeing with the page's writer would prove nothing; what matters is
// that it reads what people's tar actually emits (pax headers, GNU long names,
// ustar prefixes) and refuses what the package format or the device cannot
// carry, with a message that says what to change.
//
//   node test/modules.mjs

import { execSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, symlinkSync, rmSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const { readModule, tarEntries, gunzip, parseModuleConf, paxRecords, RESERVED_MODULE_NAMES } =
  await import(join(resolve(HERE, '..'), 'public/fw.js'));

let failures = 0;
const check = (ok, msg) => { console.log(`${ok ? '  ok  ' : ' FAIL '} ${msg}`); if (!ok) failures++; };
const refuses = async (p, re, msg) => {
  try { await p; check(false, `${msg} (accepted)`); }
  catch (e) { check(re.test(e.message), `${msg}: ${e.message}`); }
};

const T = mkdtempSync(join(tmpdir(), 'bdmod-'));
const sh = (cmd) => execSync(cmd, { cwd: T, stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, COPYFILE_DISABLE: '1' } });
const has = (cmd) => { try { execSync(`command -v ${cmd}`, { stdio: 'ignore' }); return true; } catch { return false; } };
const tarIsGnu = /GNU/.test(execSync('tar --version').toString());
console.log(`system tar: ${tarIsGnu ? 'GNU' : 'bsdtar'}`);

function mkmod(dir, name = 'demo', extra = () => {}) {
  mkdirSync(join(T, dir, 'payload'), { recursive: true });
  writeFileSync(join(T, dir, 'module.conf'), `NAME=${name}\nVERSION="1.0.0"\nDESCRIPTION='A demo'\n`);
  writeFileSync(join(T, dir, 'install'), '#!/bin/bash\necho hi\n', { mode: 0o755 });
  writeFileSync(join(T, dir, 'payload/run.sh'), '#!/bin/bash\n', { mode: 0o755 });
  writeFileSync(join(T, dir, 'payload/data.txt'), 'x');
  extra(join(T, dir));
}
const read = (f) => readModule(readFileSync(join(T, f)), f);

console.log('\nmodule.conf:');
const conf = parseModuleConf('# c\nNAME=a\n VERSION = "1.2" \nDESCRIPTION=\'q\'\nlower=ignored\nHOMEPAGE=https://x/y=z\n');
check(conf.NAME === 'a' && conf.VERSION === undefined, 'KEY=value only; spaces around = are not a key');
check(conf.DESCRIPTION === 'q' && conf.HOMEPAGE === 'https://x/y=z', 'quotes stripped, = inside a value kept');
check(conf.lower === undefined, 'lower-case keys ignored');

console.log('\nlayouts:');
mkmod('demo');
sh('tar czf wrapped.tgz demo');
const wrapped = await read('wrapped.tgz');
check(wrapped.name === 'demo' && wrapped.version === '1.0.0', 'tar of the directory: wrapper stripped, name and version read');
check(wrapped.description === 'A demo', 'single-quoted DESCRIPTION unquoted');
check(wrapped.files.map((f) => f.path).join(',') === 'install,module.conf,payload/data.txt,payload/run.sh',
  'files sorted, directory members dropped');
check(wrapped.files.find((f) => f.path === 'payload/run.sh').mode === 0o755, 'exec bit kept');
check(wrapped.files.find((f) => f.path === 'payload/data.txt').mode === 0o644, 'non-exec normalised to 0644');
sh('cd demo && tar cf ../flat.tar .');
const flat = await read('flat.tar');
check(flat.name === 'demo' && flat.files.length === 4, 'uncompressed tar of the contents (./-prefixed)');
sh('cd demo && tar czf ../noprefix.tgz module.conf install payload');
check((await read('noprefix.tgz')).files.length === 4, 'tar of named members, no ./ prefix');

console.log('\nlong names:');
mkmod('longp', 'longp', (d) => {
  const deep = join(d, 'payload/this-directory-name-is-deliberately-long-enough/to-force-a-pax-extended-header-to-carry-it');
  mkdirSync(deep, { recursive: true });
  writeFileSync(join(deep, 'f.txt'), 'x');
});
sh('tar czf longp.tgz longp'); // pax (bsdtar) or GNU ././@LongLink (GNU tar)
const names = tarEntries(await gunzip(readFileSync(join(T, 'longp.tgz')))).map((e) => e.name);
check(names.some((n) => n.endsWith('to-force-a-pax-extended-header-to-carry-it/f.txt')),
  `${tarIsGnu ? 'GNU long name' : 'pax path'} reassembled`);
check(!names.some((n) => /PaxHeader|@LongLink/.test(n)), 'carrier records consumed, not listed');
await refuses(read('longp.tgz'), /path too long.*f\.txt.*Shorten it by \d+/, 'over-budget path refused, naming it');
if (tarIsGnu) {
  sh('tar --format=pax -czf longpax.tgz longp');
  await refuses(read('longpax.tgz'), /path too long.*f\.txt/, 'GNU tar --format=pax long path refused, naming it');
}
mkmod('pfx', 'pfx', (d) => {
  const deep = join(d, 'payload/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb');
  mkdirSync(deep, { recursive: true });
  writeFileSync(join(deep, 'c.txt'), 'x');
});
sh(tarIsGnu ? 'tar --format=ustar -czf pfx.tgz pfx' : 'tar --format ustar -czf pfx.tgz pfx');
const pfx = tarEntries(await gunzip(readFileSync(join(T, 'pfx.tgz')))).map((e) => e.name);
check(pfx.includes('pfx/payload/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/c.txt'),
  'POSIX ustar prefix + name joined');

// GNU tar's own long-name carrier (typeflag 'L', name ././@LongLink) — built by
// hand here because a Mac has no GNU tar; CI's ubuntu runs the real thing above.
{
  const gnuHeader = (name, size, type, mode = 0o644) => {
    const h = new Uint8Array(512);
    const put = (str, off) => h.set(new TextEncoder().encode(str), off);
    put(name, 0); put(mode.toString(8).padStart(7, '0') + '\0', 100);
    put('0000000\0', 108); put('0000000\0', 116);
    put(size.toString(8).padStart(11, '0') + '\0', 124); put('00000000000\0', 136);
    h.fill(0x20, 148, 156); put(type, 156); put('ustar  \0', 257);
    let sum = 0; for (const b of h) sum += b;
    put(sum.toString(8).padStart(6, '0') + '\0 ', 148);
    return h;
  };
  const member = (name, data, type = '0', mode = 0o644) => {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const pad = new Uint8Array((512 - (bytes.length % 512)) % 512);
    const parts = [];
    if (name.length > 100) {
      const nm = new TextEncoder().encode(name + '\0');
      parts.push(gnuHeader('././@LongLink', nm.length, 'L'), nm, new Uint8Array((512 - (nm.length % 512)) % 512));
    }
    parts.push(gnuHeader(name.length > 100 ? name.slice(0, 100) : name, bytes.length, type, mode), bytes, pad);
    return parts;
  };
  const longName = 'gnu/payload/' + 'q'.repeat(95) + '/f.txt';
  const archive = new Uint8Array(await new Blob([
    ...member('gnu/module.conf', 'NAME=gnu\nVERSION=1\n'),
    ...member('gnu/install', '#!/bin/bash\n', '0', 0o755),
    ...member(longName, 'x'),
    new Uint8Array(1024),
  ]).arrayBuffer());
  const got = tarEntries(archive).map((e) => e.name);
  check(got.includes(longName), 'hand-built GNU ././@LongLink reassembled');
  check(!got.some((n) => n.includes('@LongLink')), 'GNU carrier record consumed');
  check(got.length === 3, 'three members, not four');
  await refuses(readModule(archive, 'gnu.tar'), /path too long.*f\.txt/, 'long GNU path refused, naming it');
}

// pax records are length-prefixed in BYTES. A non-ASCII path is longer in
// bytes than in characters, so a reader that walks characters loses the
// record boundary and every record after it.
{
  const rec = (k, v) => {
    const body = `${k}=${v}\n`;
    const bodyLen = new TextEncoder().encode(body).length;
    let len = bodyLen + 2; // "<len> " + body; widen if the number itself is longer
    while (String(len).length + 1 + bodyLen !== len) len = String(len).length + 1 + bodyLen;
    return new TextEncoder().encode(`${len} ${body}`);
  };
  const paxBytes = new Uint8Array(await new Blob([rec('path', 'héllo/wörld/ünïcode.txt'), rec('mtime', '1577836800')]).arrayBuffer());
  const recs = paxRecords(paxBytes);
  check(recs.path === 'héllo/wörld/ünïcode.txt' && recs.mtime === '1577836800', 'pax records split on byte lengths (non-ASCII path)');
}
mkmod('utf8', 'utf8', (d) => writeFileSync(join(d, 'payload/héllo-wörld.txt'), 'x'));
sh('tar czf utf8.tgz utf8');
const utf8 = await read('utf8.tgz');
check(utf8.files.some((f) => f.path === 'payload/héllo-wörld.txt'), `non-ASCII file name read back intact (${tarIsGnu ? 'GNU' : 'bsdtar'})`);
{
  // A global pax header carrying `path` is refused rather than silently ignored.
  const gnuHdr = (name, size, type) => {
    const h = new Uint8Array(512);
    const put = (str, off) => h.set(new TextEncoder().encode(str), off);
    put(name, 0); put('0000644\0', 100); put('0000000\0', 108); put('0000000\0', 116);
    put(size.toString(8).padStart(11, '0') + '\0', 124); put('00000000000\0', 136);
    h.fill(0x20, 148, 156); put(type, 156); put('ustar\0', 257); put('00', 263);
    let sum = 0; for (const b of h) sum += b;
    put(sum.toString(8).padStart(6, '0') + '\0 ', 148);
    return h;
  };
  const pad = (n) => new Uint8Array((512 - (n % 512)) % 512);
  const g = new TextEncoder().encode('15 path=module\n'); // 15 bytes, newline included
  const body = new TextEncoder().encode('NAME=g\nVERSION=1\n');
  const archive = new Uint8Array(await new Blob([
    gnuHdr('pax_global_header', g.length, 'g'), g, pad(g.length),
    gnuHdr('module.conf', body.length, '0'), body, pad(body.length),
    new Uint8Array(1024),
  ]).arrayBuffer());
  await refuses(readModule(archive, 'global.tar'), /global pax path override/, 'global pax path override refused');

  // Mis-framed records are refused, never applied: a declared length past the
  // payload, a record without its newline, a length that is not a number.
  const enc = (s) => new TextEncoder().encode(s);
  for (const [label, bad] of [
    ['length past the payload', enc('99 path=x\n')],
    ['no terminating newline', enc('14 path=module')],
    ['non-numeric length', enc('ab path=module\n')],
    ['length one over the record', enc('16 path=module\n')],
  ]) {
    let threw = false;
    try { paxRecords(bad); } catch { threw = true; }
    check(threw, `pax record refused: ${label}`);
  }
  const x = enc('16 path=module\n'); // 15 bytes declared as 16
  const arch = new Uint8Array(await new Blob([
    gnuHdr('PaxHeader/x', x.length, 'x'), x, pad(x.length),
    gnuHdr('module.conf', body.length, '0'), body, pad(body.length),
    new Uint8Array(1024),
  ]).arrayBuffer());
  await refuses(readModule(arch, 'badpax.tar'), /badpax\.tar: malformed pax header record/, 'mis-framed pax record refuses the archive');
}

console.log('\nsize ceiling:');
{
  // 8 MiB of zeros gzip to ~8 KB. Corrupt the trailer: a reader that unpacks
  // the whole stream before checking size hits zlib's error; one that stops
  // at the ceiling reports the ceiling and never gets that far.
  const zeros = new ReadableStream({
    pull(c) { c.enqueue(new Uint8Array(1 << 20)); if (++this.n >= 8) c.close(); }, n: 0,
  });
  const gz = new Uint8Array(await new Response(zeros.pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
  gz[gz.length - 3] ^= 0xff; gz[gz.length - 6] ^= 0xff; // CRC32 and ISIZE, both wrong now
  await refuses(readModule(gz, 'bomb.tgz', { maxBytes: 1 << 20 }), /unpacks to more than .* — refusing to read further/,
    'oversize gzip refused at the ceiling, before the corrupt trailer');
  await refuses(readModule(new Uint8Array(3 << 20), 'big.tar', { maxBytes: 1 << 20 }), /is more than the 1\.0 MB ceiling/,
    'oversize input refused before parsing');
}

console.log('\nrefusals:');
mkmod('lnk', 'lnk', (d) => symlinkSync('run.sh', join(d, 'payload/link.sh')));
sh('tar czf lnk.tgz lnk');
await refuses(read('lnk.tgz'), /symlink/, 'symlink');
if (has('zip')) {
  sh('cd demo && zip -qr ../demo.zip .');
  await refuses(read('demo.zip'), /zip/, 'zip file');
} else {
  console.log('  skip  zip (no zip command here)');
}
await refuses(readModule(new Uint8Array(4096), 'zeros'), /not a tar/, 'not a tar');
await refuses(readModule(new Uint8Array(10), 'tiny'), /too small/, 'too small');
mkmod('noinst', 'noinst'); rmSync(join(T, 'noinst/install'));
sh('tar czf noinst.tgz noinst');
await refuses(read('noinst.tgz'), /no install script/, 'missing install');
mkmod('noconf', 'noconf'); rmSync(join(T, 'noconf/module.conf'));
sh('tar czf noconf.tgz noconf');
await refuses(read('noconf.tgz'), /no module\.conf/, 'missing module.conf');
mkmod('empty', 'empty', (d) => writeFileSync(join(d, 'install'), ''));
sh('tar czf empty.tgz empty');
await refuses(read('empty.tgz'), /install is empty/, 'empty install');
mkmod('badname', 'Bad_Name');
sh('tar czf badname.tgz badname');
await refuses(read('badname.tgz'), /NAME="Bad_Name"/, 'NAME outside the pattern');
mkmod('longname', 'a'.repeat(33));
sh('tar czf longname.tgz longname');
await refuses(read('longname.tgz'), /NAME=/, 'NAME over 32 characters');
mkmod('badver', 'badver', (d) => writeFileSync(join(d, 'module.conf'), 'NAME=badver\nVERSION=1 0\n'));
sh('tar czf badver.tgz badver');
await refuses(read('badver.tgz'), /VERSION="1 0"/, 'VERSION with a space');
for (const r of RESERVED_MODULE_NAMES) {
  mkmod(`r-${r}`, r);
  sh(`tar czf r-${r}.tgz r-${r}`);
  await refuses(read(`r-${r}.tgz`), /collides/, `reserved name ${r}`);
}
mkmod('twotop', 'twotop'); mkdirSync(join(T, 'twotop2')); writeFileSync(join(T, 'twotop2/x'), 'x');
sh('tar czf twotop.tgz twotop twotop2');
await refuses(read('twotop.tgz'), /no module\.conf at the root/, 'two top-level dirs, module.conf in neither root');

console.log('\nbinaries:');
const elf = (machine) => { const b = new Uint8Array(64); b.set([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1]); b[18] = machine; return b; };
mkmod('x86', 'x86', (d) => writeFileSync(join(d, 'payload/tool'), elf(0x3e)));
sh('tar czf x86.tgz x86');
await refuses(read('x86.tgz'), /not aarch64/, 'x86-64 ELF');
mkmod('a64', 'a64', (d) => writeFileSync(join(d, 'payload/tool'), elf(0xb7), { mode: 0o755 }));
sh('tar czf a64.tgz a64');
const a64 = await read('a64.tgz');
check(a64.files.find((f) => f.path === 'payload/tool')?.mode === 0o755, 'aarch64 ELF accepted, executable');

console.log('\nmac junk:');
mkmod('junk', 'junk', (d) => {
  writeFileSync(join(d, 'payload/.DS_Store'), 'x');
  writeFileSync(join(d, 'payload/._run.sh'), 'x');
  mkdirSync(join(d, '__MACOSX'));
  writeFileSync(join(d, '__MACOSX/._install'), 'x');
});
sh('tar czf junk.tgz junk');
const junk = await read('junk.tgz');
check(!junk.files.some((f) => /\.DS_Store|\._|__MACOSX/.test(f.path)), '._*, .DS_Store and __MACOSX dropped');
check(junk.files.length === 4, 'and nothing else lost');

rmSync(T, { recursive: true, force: true });
console.log(failures ? `\n${failures} FAILED` : '\nall module checks passed');
process.exit(failures ? 1 : 0);
