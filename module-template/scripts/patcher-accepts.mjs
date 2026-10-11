// Prove the PLAY patcher will accept a built module, using the patcher's own
// archive reader rather than a second copy of its rules.
//
//   node scripts/patcher-accepts.mjs dist/hello-0.1.0.tgz [path/to/birddog-play-patcher]
//
// Without a path the patcher is cloned shallowly into a temp dir (needs git and
// network). Exits non-zero, with the patcher's own message, if the module would
// be refused in the browser — the same check.sh runs statically, but this one
// is the real thing.

import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join, basename, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [tgz, given] = process.argv.slice(2);
if (!tgz) {
  console.error('usage: node scripts/patcher-accepts.mjs <module.tgz> [path/to/birddog-play-patcher]');
  process.exit(2);
}

let patcher = given && resolve(given);
let cleanup = null;
if (!patcher) {
  patcher = await mkdtemp(join(tmpdir(), 'patcher-'));
  cleanup = patcher;
  console.log('cloning birddog-play-patcher (shallow)…');
  execFileSync('git', ['clone', '--quiet', '--depth', '1',
    'https://github.com/stoatworks-labs/birddog-play-patcher', patcher], { stdio: 'inherit' });
}
const fwPath = join(patcher, 'public/fw.js');
if (!existsSync(fwPath)) {
  console.error(`no public/fw.js under ${patcher} — is that a birddog-play-patcher checkout?`);
  process.exit(2);
}

const { readModule, humanSize } = await import(pathToFileURL(fwPath).href);

let code = 0;
try {
  const mod = await readModule(await readFile(tgz), basename(tgz));
  console.log(`accepted: ${mod.name} ${mod.version} — ${mod.files.length} files, ${humanSize(mod.size)}`);
  for (const f of mod.files) console.log(`  ${f.mode.toString(8)}  modules/${mod.name}/${f.path}`);
} catch (err) {
  console.error(`REFUSED: ${err.message}`);
  code = 1;
} finally {
  if (cleanup) await rm(cleanup, { recursive: true, force: true });
}
process.exit(code);
