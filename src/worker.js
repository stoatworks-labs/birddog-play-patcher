// birddog-play-fwgen — Worker half.
//
// The generator is a static page: it assembles the .fw in the browser and
// nothing is uploaded. The only job here is proxying pkgs.tailscale.com, which
// returns no Access-Control-Allow-Origin header, so the browser cannot fetch
// the arm64 tarball directly.
//
// Deliberately narrow: two routes, a hardcoded upstream host, and a strict
// version pattern, so this cannot be used as a general open proxy.

const UPSTREAM = 'https://pkgs.tailscale.com/stable/';
const VERSION_RE = /^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}$/;

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

    return json({ error: 'not found' }, 404);
  },
};
