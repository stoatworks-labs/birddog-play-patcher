// birddog-play-fwgen — Worker half.
//
// The generator is a static page: it assembles the .fw in the browser and
// nothing is uploaded. The only job here is proxying two release hosts that
// return no Access-Control-Allow-Origin header, so the browser cannot fetch
// the arm64 tarballs directly: pkgs.tailscale.com, and GitHub's release
// downloads for MediaMTX (the streaming gateway's hub, 62 MB unpacked — far
// over the 25 MiB static-asset cap, which is why it is fetched at all).
//
// Deliberately narrow: four routes, two hardcoded upstream hosts, a strict
// version pattern for Tailscale and a single pinned version for MediaMTX, so
// this cannot be used as a general open proxy.

const UPSTREAM = 'https://pkgs.tailscale.com/stable/';
const VERSION_RE = /^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}$/;

// MediaMTX is pinned, not "latest": bd-play-stream-gateway renders a
// configuration in MediaMTX's own key names and validates it in CI against
// exactly this release, and birddog-re's fwbuild stages the same one. Bump
// all three together.
const MEDIAMTX_VERSION = 'v1.21.0';
const MEDIAMTX_BASE = 'https://github.com/bluenviron/mediamtx/releases/download/';
const mediamtxFile = (v) => `mediamtx_${v}_linux_arm64.tar.gz`;

const CORS = {
  'access-control-allow-origin': '*',
  'access-control-allow-methods': 'GET, HEAD, OPTIONS',
};

const json = (obj, status = 200) =>
  new Response(JSON.stringify(obj, null, 1), {
    status,
    headers: { 'content-type': 'application/json; charset=utf-8', ...CORS },
  });

async function latest() {
  const idx = await fetch(`${UPSTREAM}?mode=json`, {
    cf: { cacheTtl: 900, cacheEverything: true },
  });
  if (!idx.ok) return json({ error: `upstream index ${idx.status}` }, 502);

  const data = await idx.json();
  const version = data.TarballsVersion;
  const file = data.Tarballs && data.Tarballs.arm64;
  if (!VERSION_RE.test(version || '') || !file) {
    return json({ error: 'upstream index missing an arm64 tarball' }, 502);
  }

  // Sidecar digest, so the browser can verify what it assembles into the
  // package rather than trusting this Worker or the transport.
  let sha256 = null;
  const sc = await fetch(`${UPSTREAM}${file}.sha256`, {
    cf: { cacheTtl: 900, cacheEverything: true },
  });
  if (sc.ok) {
    const t = (await sc.text()).trim().split(/\s+/)[0];
    if (/^[0-9a-f]{64}$/.test(t)) sha256 = t;
  }

  return json({ version, file, sha256 });
}

async function tarball(url) {
  const v = url.searchParams.get('v') || '';
  if (!VERSION_RE.test(v)) return json({ error: 'bad version' }, 400);

  const upstream = `${UPSTREAM}tailscale_${v}_arm64.tgz`;
  const res = await fetch(upstream, { cf: { cacheTtl: 86400, cacheEverything: true } });
  if (!res.ok) return json({ error: `upstream ${res.status}` }, 502);

  // Streamed straight through — the body is ~34 MB and never buffered here.
  return new Response(res.body, {
    headers: {
      'content-type': 'application/x-compressed-tar',
      'content-length': res.headers.get('content-length') || '',
      'cache-control': 'public, max-age=86400',
      ...CORS,
    },
  });
}

// The pinned MediaMTX release and the SHA-256 GitHub publishes beside it, so
// the browser verifies what it packages rather than trusting this Worker or
// the transport — the same arrangement as the Tailscale sidecar.
async function mediamtxLatest() {
  const v = MEDIAMTX_VERSION;
  const file = mediamtxFile(v);
  let sha256 = null;
  const sc = await fetch(`${MEDIAMTX_BASE}${v}/checksums.sha256`, {
    cf: { cacheTtl: 86400, cacheEverything: true },
  });
  if (sc.ok) {
    for (const line of (await sc.text()).split('\n')) {
      const m = line.trim().match(/^([0-9a-f]{64})\s+\*?(\S+)$/);
      if (m && m[2] === file) sha256 = m[1];
    }
  }
  return json({ version: v, file, sha256 });
}

async function mediamtxTarball(url) {
  const v = url.searchParams.get('v') || '';
  // Only the pinned release is ever served: there is nothing else the page
  // could use, and a version parameter that reached upstream would be an
  // open proxy over GitHub.
  if (v !== MEDIAMTX_VERSION) return json({ error: 'unsupported version' }, 400);

  const res = await fetch(`${MEDIAMTX_BASE}${v}/${mediamtxFile(v)}`, {
    cf: { cacheTtl: 86400, cacheEverything: true },
    redirect: 'follow',
  });
  if (!res.ok) return json({ error: `upstream ${res.status}` }, 502);

  // Streamed straight through — ~29 MB compressed, never buffered here.
  return new Response(res.body, {
    headers: {
      'content-type': 'application/x-compressed-tar',
      'content-length': res.headers.get('content-length') || '',
      'cache-control': 'public, max-age=86400',
      ...CORS,
    },
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (!url.pathname.startsWith('/api/')) {
      return env.ASSETS.fetch(request);
    }
    if (request.method === 'OPTIONS') {
      return new Response(null, { status: 204, headers: CORS });
    }
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return json({ error: 'method not allowed' }, 405);
    }

    if (url.pathname === '/api/tailscale/latest') return latest();
    if (url.pathname === '/api/tailscale/tgz') return tarball(url);
    if (url.pathname === '/api/mediamtx/latest') return mediamtxLatest();
    if (url.pathname === '/api/mediamtx/tgz') return mediamtxTarball(url);

    return json({ error: 'not found' }, 404);
  },
};
