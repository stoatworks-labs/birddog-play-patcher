// UI layer for the PLAY package generator. All archive logic lives in fw.js so
// it can be tested outside a browser; this file is DOM, fetch and progress only.

import {
  Tar, extractTailscale, sha256Hex, buildConf, validateKey, humanSize,
} from './fw.js';

const els = {};
let manifest = null;
let built = null; // { blob, name, sha256 }

function log(msg, cls = '') {
  const line = document.createElement('div');
  line.className = `line ${cls}`;
  line.textContent = msg;
  els.log.appendChild(line);
  els.log.scrollTop = els.log.scrollHeight;
}

async function asset(path) {
  const res = await fetch(path, { cache: 'no-cache' });
  if (!res.ok) throw new Error(`missing build asset ${path} (${res.status}) — run build-assets.sh`);
  return new Uint8Array(await res.arrayBuffer());
}

async function fetchTailscale() {
  log('querying pkgs.tailscale.com for the current stable release…');
  const meta = await (await fetch('api/tailscale/latest')).json();
  if (meta.error) throw new Error(`Tailscale index: ${meta.error}`);
  log(`  latest stable is ${meta.version} (${meta.file})`);

  log('downloading the arm64 tarball…');
  const res = await fetch(`api/tailscale/tgz?v=${encodeURIComponent(meta.version)}`);
  if (!res.ok) throw new Error(`Tailscale download failed (${res.status})`);
  const buf = await res.arrayBuffer();
  log(`  got ${humanSize(buf.byteLength)}`);

  if (meta.sha256) {
    const got = await sha256Hex(buf);
    if (got !== meta.sha256) {
      throw new Error(
        `Tailscale tarball SHA-256 mismatch — refusing to package it. ` +
        `expected ${meta.sha256}, got ${got}`,
      );
    }
    log('  SHA-256 matches the published .sha256 sidecar', 'ok');
  } else {
    log('  WARNING: upstream published no .sha256 sidecar; tarball unverified', 'warn');
  }
  return { version: meta.version, buf };
}

async function readLocalTailscale(file) {
  log(`reading ${file.name} (${humanSize(file.size)})…`);
  const m = file.name.match(/tailscale_([0-9.]+)_arm64\.tgz/);
  return { version: m ? m[1] : 'local', buf: await file.arrayBuffer() };
}

async function build() {
  els.build.disabled = true;
  els.download.hidden = true;
  els.result.hidden = true;
  els.log.innerHTML = '';
  built = null;

  try {
    const keyText = els.key.value.trim();
    const keyErr = validateKey(keyText);
    if (keyErr) throw new Error(keyErr);

    const withTailscale = els.optTailscale.checked;
    // The birdUI panel is not a separate choice: it ships whenever Tailscale
    // does, because a Tailscale install that still needs SSH to finish is the
    // problem it solves. It drops out only when the asset is missing.
    const withTailscaleUi = withTailscale && !!manifest.bdts;
    const withKvm = els.optKvm.checked && !!manifest.bdkvm;
    const withPlay = els.optPlay.checked && !!manifest.bdplay;
    const doReboot = els.optReboot.checked;
    const tag = (els.tag.value.trim() || 'web').replace(/[^A-Za-z0-9._-]/g, '-');

    const tar = new Tar();

    log('adding installer from tools/fwbuild/payload…');
    tar.file('./update', await asset('assets/update'), 0o755);
    tar.file('./probe.sh', await asset('assets/probe.sh'), 0o755);
    log(`  update   sha256 ${manifest.update.sha256.slice(0, 16)}…`);
    log(`  probe.sh sha256 ${manifest.probe.sha256.slice(0, 16)}…`);

    tar.text('./authorized_keys', keyText + '\n', 0o600);
    tar.text('./build.conf', buildConf({ tag, withTailscale, withTailscaleUi, withKvm, withPlay, doReboot }), 0o644);

    for (const rel of manifest.rootfsOverlay || []) {
      tar.file(`./files/${rel}`, await asset(`assets/files/${rel}`), 0o644);
      log(`  rootfs overlay: /${rel}`);
    }

    if (withTailscale) {
      const src = els.tsFile.files[0]
        ? await readLocalTailscale(els.tsFile.files[0])
        : await fetchTailscale();
      const bins = await extractTailscale(src.buf);
      log(`  tailscaled ${humanSize(bins.tailscaled.length)} + tailscale ` +
          `${humanSize(bins.tailscale.length)}, aarch64 ELF`, 'ok');
      tar.file('./userdata/tailscale/tailscaled', bins.tailscaled, 0o755);
      tar.file('./userdata/tailscale/tailscale', bins.tailscale, 0o755);
      log(`tailscale ${src.version} staged`, 'ok');

      if (withTailscaleUi) {
        const bdts = await asset('assets/bdts-linux-arm64');
        if (bdts.length !== manifest.bdts.size) throw new Error('bdts asset size mismatch');
        if ((await sha256Hex(bdts)) !== manifest.bdts.sha256) {
          throw new Error('bdts asset SHA-256 mismatch');
        }
        tar.file('./userdata/bd-tailscale-ui/bdts', bdts, 0o755);
        log(`  birdUI panel ${humanSize(bdts.length)}, sha256 verified — ` +
            'sign in from the System page, no SSH needed', 'ok');
      } else {
        log('  birdUI panel unavailable — signing in will need SSH', 'warn');
      }
    } else {
      log('tailscale: skipped');
    }

    if (withKvm) {
      log('adding the NDI KVM endpoint…');
      const bdkvm = await asset('assets/bdkvm-linux-arm64');
      if (bdkvm.length !== manifest.bdkvm.size) throw new Error('bdkvm asset size mismatch');
      if ((await sha256Hex(bdkvm)) !== manifest.bdkvm.sha256) {
        throw new Error('bdkvm asset SHA-256 mismatch');
      }
      tar.file('./userdata/birddog-kvm/bdkvm', bdkvm, 0o755);
      tar.file('./userdata/birddog-kvm/run.sh', await asset('assets/kvm-run.sh'), 0o755);
      log(`  bdkvm ${humanSize(bdkvm.length)}, sha256 verified`, 'ok');
    } else {
      log('NDI KVM: skipped');
    }

    if (withPlay) {
      log('adding the USB media player…');
      // The player itself is required; the two helpers are optional, and the
      // installer reports which capabilities it ended up with. A package
      // without them still plays video and stills off FAT32/NTFS.
      const parts = [
        ['bdplay', 'assets/bdplay-linux-arm64', './userdata/bd-play/bdplay', manifest.bdplay, true],
        ['bdpdf', 'assets/bdpdf-linux-arm64', './userdata/bd-play/bdpdf', manifest.bdpdf, false],
        ['libpdfium.so', 'assets/libpdfium.so', './userdata/bd-play/libpdfium.so', manifest.pdfium, false],
        ['exFAT helper', 'assets/mount.exfat-fuse', './userdata/bd-play/mount.exfat-fuse', manifest.exfat, false],
      ];
      for (const [label, url, dest, meta, required] of parts) {
        if (!meta) {
          if (required) throw new Error(`${label} asset is missing from this build`);
          log(`  ${label}: not available — skipped`);
          continue;
        }
        const data = await asset(url);
        if (data.length !== meta.size) throw new Error(`${label} asset size mismatch`);
        if ((await sha256Hex(data)) !== meta.sha256) {
          throw new Error(`${label} asset SHA-256 mismatch`);
        }
        tar.file(dest, data, 0o755);
        log(`  ${label} ${humanSize(data.length)}, sha256 verified`, 'ok');
      }
      if (!manifest.bdpdf || !manifest.pdfium) log('  PDF playback will be unavailable');
      if (!manifest.exfat) log('  exFAT sticks will not mount');
    } else {
      log('USB media player: skipped');
    }

    log('writing tar and gzipping…');
    const blob = await tar.gzip();
    const digest = await sha256Hex(blob);

    built = { blob, name: `BirdDog_PLAY-custom-${tag}.fw`, sha256: digest };

    log(`built ${built.name} — ${humanSize(blob.size)}`, 'ok');
    els.resultName.textContent = built.name;
    els.resultSize.textContent = humanSize(blob.size);
    els.resultSha.textContent = digest;
    els.result.hidden = false;
    els.download.hidden = false;
  } catch (err) {
    log(String(err.message || err), 'err');
  } finally {
    els.build.disabled = false;
  }
}

function download() {
  if (!built) return;
  const url = URL.createObjectURL(built.blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = built.name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

async function init() {
  for (const id of [
    'key', 'tag', 'optTailscale', 'optKvm', 'optPlay', 'optReboot', 'tsFile',
    'build', 'download', 'log', 'result', 'resultName', 'resultSize',
    'resultSha', 'kvmRow', 'playRow', 'playExtras', 'payloadInfo', 'keyHint',
  ]) {
    els[id] = document.getElementById(id);
  }

  els.build.addEventListener('click', build);
  els.download.addEventListener('click', download);
  els.key.addEventListener('input', () => {
    const err = els.key.value.trim() ? validateKey(els.key.value) : null;
    els.key.classList.toggle('bad', !!err);
    els.keyHint.textContent =
      err || 'Installed to /root/.ssh/authorized_keys. SSH on the PLAY listens on port 9031.';
  });

  try {
    manifest = await (await fetch('assets/manifest.json', { cache: 'no-cache' })).json();
  } catch {
    els.build.disabled = true;
    els.payloadInfo.textContent =
      'Build assets are missing — run tools/fwweb/build-assets.sh, then reload.';
    els.payloadInfo.className = 'note err';
    return;
  }

  els.payloadInfo.textContent =
    `Installer payload synced from ${manifest.source} on ${manifest.generated}.`;
  if (!manifest.bdkvm) {
    els.optKvm.checked = false;
    els.optKvm.disabled = true;
    els.kvmRow.classList.add('disabled');
    els.kvmRow.title = 'bdkvm-linux-arm64 was not present when assets were built';
  }

  if (!manifest.bdplay) {
    els.optPlay.checked = false;
    els.optPlay.disabled = true;
    els.playRow.classList.add('disabled');
    els.playRow.title = 'bdplay-linux-arm64 was not present when assets were built';
  } else {
    // Say up front which capabilities this particular build carries, rather
    // than letting the user find out on the device that PDFs do not play or an
    // exFAT stick will not mount.
    const have = [];
    if (manifest.bdpdf && manifest.pdfium) have.push('PDF (PDFium)');
    if (manifest.exfat) have.push('exFAT');
    const size = humanSize(
      manifest.bdplay.size +
      (manifest.bdpdf?.size || 0) +
      (manifest.pdfium?.size || 0) +
      (manifest.exfat?.size || 0));
    els.playExtras.textContent = have.length
      ? `Includes ${have.join(' and ')} support. Adds about ${size} to the package.`
      : `Video and stills only in this build — no PDF or exFAT support. Adds about ${size}.`;
  }

  // ?demo=1 fills in an obviously-fake key and builds, so screenshots and video
  // are of the real tool doing real work rather than an empty form. It never
  // downloads — the visitor still has to ask for the file.
  if (new URLSearchParams(location.search).has('demo')) {
    els.key.value =
      'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForTheDemoOnly you@laptop';
    els.optKvm.checked = !!manifest.bdkvm;
    els.optPlay.checked = !!manifest.bdplay;
    els.tag.value = 'demo';
    build();
  }
}

init();
