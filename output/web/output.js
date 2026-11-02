/* The Output tab on the BirdDog PLAY's AV Setup page.
 *
 * Served from /static/, outside the Go template system, so the patch to
 * videoset.html stays two lines and everything the tab does lives here.
 *
 * Talks to bdout's API on __BDOUT_API_PORT__, sending the birdUI session
 * cookie with every request (credentials: 'include'): a format change restarts
 * the PLAY's own decoder and can leave the screen dark, so it is gated on the
 * same login as the rest of the web UI. Status is open, so the tab can say why
 * it is locked.
 *
 * Crops and placements are typed in pixels and sent in millionths of the
 * picture and the screen (toRect), which the shim turns back into the same
 * pixels whatever size the stream turns out to be.
 */
(function () {
  'use strict';

  var API_PORT = __BDOUT_API_PORT__;
  var API = location.protocol + '//' + location.hostname + ':' + API_PORT;
  var UNIT = 1000000;

  var root = document.getElementById('bdout_form');
  if (!root) return;

  var status = null;      // last /api/status
  var formats = null;     // /api/formats, once
  var busy = false;
  var notice = null;
  var timer = null;
  var logOpen = false;
  var seeded = false;     // the forms have been filled from the kept settings
  var dirty = false;      // the forms have edits; polling must not redraw them

  // The format form.
  var fmt = { kind: 'source', standard: '1080p60', width: 1920, height: 1200, refresh: 60, timing: null };
  var resolved = null;    // /api/resolve for fmt, or { error }
  // The AOI form, in pixels.
  var aoi = { on: false, canvasW: 1920, canvasH: 1080, src: [0, 0, 1920, 1080], fill: true, dst: [0, 0, 1920, 1080], aspect: false };
  var preview = null;     // /api/preview for aoi, or { error }
  var wall = { cols: 2, rows: 1, col: 1, row: 1 };

  var RATES = ['23.976', '24', '25', '29.97', '30', '50', '59.94', '60'];
  var POLARITY = [[5, '+H +V'], [9, '+H −V'], [6, '−H +V'], [10, '−H −V']];
  var TIMING_FIELDS = [
    ['clock_khz', 'Pixel clock (kHz)'],
    ['hdisplay', 'Width'], ['hsync_start', 'H sync start'], ['hsync_end', 'H sync end'], ['htotal', 'H total'],
    ['vdisplay', 'Height'], ['vsync_start', 'V sync start'], ['vsync_end', 'V sync end'], ['vtotal', 'V total']
  ];

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = 'include';
    if (opts.body) opts.headers = { 'Content-Type': 'application/json' };
    return fetch(API + path, opts).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (body) {
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
        return body;
      });
    });
  }

  function post(path, body) {
    return api(path, { method: 'POST', body: JSON.stringify(body || {}) });
  }

  function authed(s) {
    return !!(s && s.auth && (s.auth.authenticated || !s.auth.required));
  }

  function runner() { return (status && status.runner) || {}; }

  // ------------------------------------------------------------- sizes

  function size(text) {
    var m = /(\d+)x(\d+)/.exec(text || '');
    return m ? [parseInt(m[1], 10), parseInt(m[2], 10)] : null;
  }

  // What the shim last saw: [picture w, h, screen w, h].
  function shimPicture() {
    var m = /(\d+)x(\d+) on (\d+)x(\d+)/.exec((status && status.shim && status.shim.picture) || '');
    return m ? [+m[1], +m[2], +m[3], +m[4]] : null;
  }

  // The screen the placement is measured on: what the shim saw, else the
  // mode in force, else the kept format, else 1080p.
  function screenSize() {
    var p = shimPicture();
    if (p) return [p[2], p[3]];
    var s = status && status.screen && size(status.screen.current);
    if (s) return s;
    var k = runner().kept_mode;
    if (k) return [k.hdisplay, k.vdisplay];
    return [1920, 1080];
  }

  function toM(px, of) { return Math.round(px * UNIT / of); }

  function toRect(r, W, H) {
    var x = toM(r[0], W), y = toM(r[1], H);
    return { x: x, y: y, w: Math.min(toM(r[2], W), UNIT - x), h: Math.min(toM(r[3], H), UNIT - y) };
  }

  function fromRect(r, W, H) {
    return [Math.round(W * r.x / UNIT), Math.round(H * r.y / UNIT), Math.round(W * r.w / UNIT), Math.round(H * r.h / UNIT)];
  }

  function hz(v) {
    for (var d = 0; d <= 3; d++) {
      var p = Math.pow(10, d);
      if (Math.abs(Math.round(v * p) / p - v) < 0.0005) return v.toFixed(d);
    }
    return v.toFixed(3);
  }

  // ------------------------------------------------------------- data flow

  function poll() {
    return api('/api/status')
      .then(function (s) { status = s; status.unreachable = null; })
      .catch(function (e) {
        status = status || {};
        status.unreachable = e.message || 'no response';
      })
      .then(function () {
        if (!formats && !status.unreachable) {
          return api('/api/formats').then(function (f) { formats = f; }).catch(function () {});
        }
      })
      .then(function () {
        if (!seeded && status.runner) seed();
        render();
        clearTimeout(timer);
        timer = setTimeout(poll, runner().trial || runner().busy ? 1000 : 2000);
      });
  }

  // Fill the forms from the kept settings: once, and after each change.
  function seed() {
    var k = runner().kept || {};
    var f = k.format || { kind: 'source' };
    fmt.kind = f.kind || 'source';
    if (f.standard) fmt.standard = f.standard;
    if (f.width) { fmt.width = f.width; fmt.height = f.height; fmt.refresh = f.refresh; }
    if (f.timing) fmt.timing = f.timing;
    var t = runner().trial;
    if (t && t.format) {
      fmt.kind = t.format.kind;
      if (t.format.standard) fmt.standard = t.format.standard;
      if (t.format.width) { fmt.width = t.format.width; fmt.height = t.format.height; fmt.refresh = t.format.refresh; }
      if (t.format.timing) fmt.timing = t.format.timing;
    }

    var a = k.aoi || {};
    var pic = shimPicture();
    var scr = screenSize();
    aoi.on = !!a.on;
    aoi.canvasW = a.canvas_w || (pic ? pic[0] : scr[0]);
    aoi.canvasH = a.canvas_h || (pic ? pic[1] : scr[1]);
    aoi.src = a.src ? fromRect(a.src, aoi.canvasW, aoi.canvasH) : [0, 0, aoi.canvasW, aoi.canvasH];
    aoi.fill = !a.dst || (a.dst.x === 0 && a.dst.y === 0 && a.dst.w === UNIT && a.dst.h === UNIT);
    aoi.dst = a.dst ? fromRect(a.dst, scr[0], scr[1]) : [0, 0, scr[0], scr[1]];
    aoi.aspect = !!a.keep_aspect;
    seeded = true;
    dirty = false;
    resolve();
    previewAOI();
  }

  function formatBody() {
    var f = { kind: fmt.kind };
    if (fmt.kind === 'standard') f.standard = fmt.standard;
    if (fmt.kind === 'custom') { f.width = +fmt.width; f.height = +fmt.height; f.refresh = parseFloat(fmt.refresh); }
    if (fmt.kind === 'timing') f.timing = fmt.timing || defaultTiming();
    return f;
  }

  function defaultTiming() {
    var k = runner().kept_mode || (status && status.screen && status.screen.native);
    if (k) return JSON.parse(JSON.stringify(k));
    return { name: '1920x1080', clock_khz: 148500, hdisplay: 1920, hsync_start: 2008, hsync_end: 2052, htotal: 2200,
      vdisplay: 1080, vsync_start: 1084, vsync_end: 1089, vtotal: 1125, flags: 5 };
  }

  function aoiBody() {
    var scr = screenSize();
    return {
      on: aoi.on,
      src: toRect(aoi.src, aoi.canvasW, aoi.canvasH),
      dst: aoi.fill ? { x: 0, y: 0, w: UNIT, h: UNIT } : toRect(aoi.dst, scr[0], scr[1]),
      keep_aspect: aoi.aspect,
      canvas_w: aoi.canvasW,
      canvas_h: aoi.canvasH
    };
  }

  var resolveTimer = null;
  function resolve() {
    clearTimeout(resolveTimer);
    resolveTimer = setTimeout(function () {
      if (!authed(status)) return;
      if (fmt.kind === 'source') { resolved = null; renderResolved(); return; }
      post('/api/resolve', { format: formatBody() })
        .then(function (r) { resolved = r; })
        .catch(function (e) { resolved = { error: e.message }; })
        .then(renderResolved);
    }, 250);
  }

  var previewTimer = null;
  function previewAOI() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(function () {
      if (!authed(status)) return;
      var scr = screenSize();
      post('/api/preview', { aoi: aoiBody(), picture: [aoi.canvasW, aoi.canvasH], screen: scr })
        .then(function (r) { preview = r.plane; preview.screen = scr; })
        .catch(function (e) { preview = { error: e.message }; })
        .then(renderPreview);
    }, 250);
  }

  function act(path, body, working, done) {
    if (busy) return;
    busy = true;
    notice = working ? { kind: 'ok', text: working } : null;
    renderLeft();
    post(path, body)
      .then(function (b) {
        if (b.runner) status.runner = b.runner;
        notice = done ? { kind: 'ok', text: done } : null;
        seed();
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function tryFormat() {
    readForm();
    if (fmt.kind === 'source') {
      act('/api/format', { format: formatBody() }, 'Restarting the PLAY’s decoder to follow the source again…', 'Following the source');
    } else {
      act('/api/format', { format: formatBody() },
        'Restarting the PLAY’s decoder with the new format — a few seconds of black, then look at the screen.', null);
    }
  }

  function applyAOI() {
    readForm();
    act('/api/aoi', { aoi: aoiBody() }, null, aoi.on ? 'Applied — the shim takes it within half a second' : 'Off — the whole picture on the whole screen');
  }

  function showLog() {
    logOpen = true;
    api('/api/log').then(function (b) {
      var pre = document.getElementById('bdout_log');
      if (!pre) return;
      var lines = (b.lines || []).map(function (l) {
        var t = new Date(l.time);
        return t.toLocaleTimeString() + ' ' + (l.level === 'info' ? '' : l.level.toUpperCase() + ' ') + l.text;
      });
      pre.textContent = lines.length ? lines.join('\n') : '(empty)';
      pre.scrollTop = pre.scrollHeight;
    }).catch(function (e) {
      var pre = document.getElementById('bdout_log');
      if (pre) pre.textContent = e.message;
    });
  }

  // ----------------------------------------------------------- form state

  function g(id) { return document.getElementById('bdout_' + id); }
  function num(id, def) { var e = g(id); if (!e) return def; var v = parseInt(e.value, 10); return isNaN(v) ? def : v; }

  function readForm() {
    if (g('kind')) fmt.kind = g('kind').value;
    if (g('standard')) fmt.standard = g('standard').value;
    if (g('width')) { fmt.width = num('width', fmt.width); fmt.height = num('height', fmt.height); fmt.refresh = g('refresh').value; }
    if (g('t_clock_khz')) {
      var t = fmt.timing || defaultTiming();
      TIMING_FIELDS.forEach(function (f) { t[f[0]] = num('t_' + f[0], t[f[0]]); });
      t.flags = num('t_flags', t.flags);
      t.name = t.hdisplay + 'x' + t.vdisplay;
      fmt.timing = t;
    }
    if (g('aoi_on')) aoi.on = g('aoi_on').checked;
    if (g('canvas_w')) { aoi.canvasW = Math.max(16, num('canvas_w', aoi.canvasW)); aoi.canvasH = Math.max(16, num('canvas_h', aoi.canvasH)); }
    ['x', 'y', 'w', 'h'].forEach(function (k, i) {
      if (g('src_' + k)) aoi.src[i] = num('src_' + k, aoi.src[i]);
      if (g('dst_' + k)) aoi.dst[i] = num('dst_' + k, aoi.dst[i]);
    });
    if (g('fill')) aoi.fill = g('fill').checked;
    if (g('aspect')) aoi.aspect = g('aspect').value === '1';
    if (g('wall_cols')) {
      wall.cols = Math.max(1, num('wall_cols', 1)); wall.rows = Math.max(1, num('wall_rows', 1));
      wall.col = Math.min(wall.cols, Math.max(1, num('wall_col', 1))); wall.row = Math.min(wall.rows, Math.max(1, num('wall_row', 1)));
    }
  }

  function setCrop(x, y, w, h) {
    var W = aoi.canvasW, H = aoi.canvasH;
    aoi.src = [Math.round(x * W) & ~1, Math.round(y * H) & ~1, 0, 0];
    aoi.src[2] = (Math.round((x + w) * W) & ~1) - aoi.src[0];
    aoi.src[3] = (Math.round((y + h) * H) & ~1) - aoi.src[1];
    aoi.on = true;
    dirty = true;
    renderLeft();
    previewAOI();
  }

  function useNative() {
    var n = status && status.screen && status.screen.native;
    if (!n) return;
    if (status.screen.native_ok) {
      fmt.kind = 'timing';
      fmt.timing = JSON.parse(JSON.stringify(n));
    } else {
      fmt.kind = 'custom';
      fmt.width = n.hdisplay;
      fmt.height = n.vdisplay;
      fmt.refresh = hz(n.clock_khz * 1000 / (n.htotal * n.vtotal));
    }
    dirty = true;
    renderLeft();
    resolve();
  }

  // ------------------------------------------------------------ rendering

  function row(label, controlHTML) {
    return '<div class="row m-0 p-1">' +
      '<div class="col-xl-4 m-0 p-0 my-auto">' + label + '</div>' +
      '<div class="col-xl-8 m-0 p-0">' + controlHTML + '</div>' +
      '</div>';
  }

  function full(html) {
    return '<div class="row m-0 p-1"><div class="col-xl-12 m-0 p-0">' + html + '</div></div>';
  }

  function hint(text) {
    return '<div style="font-size:11px;opacity:.7;margin-top:3px;">' + text + '</div>';
  }

  function button(id, text, disabled) {
    return '<button type="button" class="restart" id="' + id + '"' +
      (disabled ? ' disabled' : '') + ' style="white-space:nowrap;padding:4px 10px;">' + esc(text) + '</button>';
  }

  function numInput(id, value, disabled, width) {
    return '<input type="number" id="bdout_' + id + '" value="' + esc(value) + '"' + (disabled ? ' disabled' : '') +
      ' style="width:' + (width || '5.5em') + ';">';
  }

  function pill(colour, text) {
    return '<span style="display:inline-block;width:.6em;height:.6em;border-radius:50%;background:' +
      colour + ';margin-right:.5em;vertical-align:middle;"></span>' + esc(text);
  }

  function noticeHTML() {
    if (!notice) return '';
    var colour = notice.kind === 'ok' ? '#22b24c' : notice.kind === 'warn' ? '#e67e22' : '#c0392b';
    return '<div class="p-2 mt-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + esc(notice.text) + '</div>';
  }

  function banner(colour, html) {
    return '<div class="p-2 mb-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + html + '</div>';
  }

  function authBanner(s) {
    var a = s.auth || {};
    if (a.authenticated || !a.required) return '';
    return banner('#e67e22', esc(a.reason || 'Log in to birdUI to use this tab.'));
  }

  function stateBanners(s) {
    var out = '';
    if (s.unreachable) out += banner('#c0392b', 'The output service is not responding (bd-out): ' + esc(s.unreachable));
    if (!s.runner) return out;
    if (s.runner.conf_error) out += banner('#c0392b', 'The saved settings did not load, so the PLAY follows the source: ' + esc(s.runner.conf_error));
    if (!s.decoder_running) {
      out += banner('#7f8c8d', 'The PLAY’s own decoder is not running — another module may have HDMI. Formats take when it starts again.');
    } else if (s.shim && !s.shim.loaded) {
      out += banner('#e67e22', 'The shim is not in the PLAY’s decoder yet, so formats and the area of interest do nothing. ' +
        'It loads when the decoder starts. ' + button('bdout_restart', 'RESTART THE DECODER', busy || !authed(s) || s.runner.busy));
    }
    return out;
  }

  function trialHTML() {
    var r = runner();
    if (r.trial) {
      var t = r.trial;
      var going = r.busy ? 'Restarting the PLAY’s decoder…' :
        'Going back to <b>' + esc(r.kept_label) + '</b> in <b>' + t.seconds_left + ' s</b> unless you keep it.';
      return banner('#e67e22', 'Trying <b>' + esc(t.label) + '</b>. ' + going +
        '<div style="display:flex;gap:6px;margin-top:6px;">' +
        button('bdout_keep', 'KEEP IT', busy || r.busy) + button('bdout_revert', 'GO BACK NOW', busy || r.busy) + '</div>');
    }
    if (r.busy) return banner('#e67e22', 'Restarting the PLAY’s decoder…');
    return '';
  }

  // The tab is two columns. The left one is the controls and is only redrawn
  // on demand — a poll must never rebuild it under someone's cursor — except
  // for the countdown, which has its own box; the right one is status and is
  // redrawn on every poll.
  function skeleton() {
    if (document.getElementById('bdout_left')) return;
    root.innerHTML =
      '<div class="row m-0 pt-2 div_tab_contents">' +
        '<div class="col-xl-6 m-0 p-0 pr-xl-2"><div id="bdout_trial"></div><div id="bdout_left"></div></div>' +
        '<div class="col-xl-6 m-0 p-0 pl-xl-2"><div id="bdout_right"></div></div>' +
      '</div>';
  }

  var lastBanners = null;
  var lastLeft = null;

  function render() {
    skeleton();
    var t = document.getElementById('bdout_trial');
    t.innerHTML = trialHTML();
    wireTrial();
    renderRight();
    // The banners change with the state (logged in, shim loaded, decoder up);
    // the forms only when nobody is editing them.
    var banners = authBanner(status || {}) + stateBanners(status || {});
    if (!dirty || busy || banners !== lastBanners) renderLeft();
    else renderNotice();
  }

  function renderNotice() {
    var n = document.getElementById('bdout_notice');
    if (n) n.innerHTML = noticeHTML();
  }

  function standardOptions() {
    if (!formats) return '<option>' + esc(fmt.standard) + '</option>';
    var groups = [], by = {};
    formats.standards.forEach(function (s) {
      if (!by[s.group]) { by[s.group] = []; groups.push(s.group); }
      by[s.group].push(s);
    });
    return groups.map(function (gname) {
      return '<optgroup label="' + esc(gname) + '">' + by[gname].map(function (s) {
        return '<option value="' + esc(s.id) + '"' + (s.id === fmt.standard ? ' selected' : '') + '>' + esc(s.label) + '</option>';
      }).join('') + '</optgroup>';
    }).join('');
  }

  function formatSection(locked) {
    var kinds = [['source', 'Follow the source (the PLAY’s own choice)'], ['standard', 'A standard format'],
      ['custom', 'A custom size'], ['timing', 'Exact timing']];
    var out = full('<b>HDMI format</b>') +
      row('Format', '<select id="bdout_kind"' + (locked ? ' disabled' : '') + '>' + kinds.map(function (k) {
        return '<option value="' + k[0] + '"' + (fmt.kind === k[0] ? ' selected' : '') + '>' + esc(k[1]) + '</option>';
      }).join('') + '</select>');

    if (fmt.kind === 'source') {
      out += full(hint('The PLAY picks from the screen’s own list to match each stream, as BirdDog ships it.'));
    } else if (fmt.kind === 'standard') {
      out += row('Standard', '<select id="bdout_standard"' + (locked ? ' disabled' : '') + '>' + standardOptions() + '</select>' +
        hint('Only the formats whose pixel clock the PLAY’s HDMI chip can make. Fitted ones have no standard timing on those clocks.'));
    } else if (fmt.kind === 'custom') {
      out += row('Size', numInput('width', fmt.width, locked) + ' × ' + numInput('height', fmt.height, locked)) +
        row('Refresh (Hz)', '<input type="text" id="bdout_refresh" list="bdout_rates" value="' + esc(fmt.refresh) + '"' +
          (locked ? ' disabled' : '') + ' style="width:5.5em;"><datalist id="bdout_rates">' +
          RATES.map(function (r) { return '<option value="' + r + '">'; }).join('') + '</datalist>' +
          hint('Fitted onto the nearest of the PLAY’s 20 pixel clocks by padding the blanking; the refresh lands within 0.05%.'));
    } else {
      var t = fmt.timing || defaultTiming();
      fmt.timing = t;
      out += full('<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(9em,1fr));gap:4px 8px;">' +
        TIMING_FIELDS.map(function (f) {
          return '<label style="font-weight:normal;font-size:12px;">' + esc(f[1]) + '<br>' + numInput('t_' + f[0], t[f[0]], locked, '100%') + '</label>';
        }).join('') +
        '<label style="font-weight:normal;font-size:12px;">Sync<br><select id="bdout_t_flags"' + (locked ? ' disabled' : '') + ' style="width:100%;">' +
        POLARITY.map(function (p) {
          return '<option value="' + p[0] + '"' + (t.flags === p[0] ? ' selected' : '') + '>' + p[1] + '</option>';
        }).join('') + '</select></label></div>' +
        hint('The clock must be one of the PLAY’s: ' + esc(((formats && formats.clocks_khz) || []).join(', ')) + ' kHz.'));
    }
    var sc = status && status.screen;
    if (sc && sc.native && fmt.kind !== 'source') {
      out += row('', button('bdout_native', 'USE THE SCREEN’S OWN ' + sc.native.hdisplay + '×' + sc.native.vdisplay, locked));
    }
    out += full('<div id="bdout_resolved"></div>');
    out += '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
      button('bdout_try', busy ? 'Working…' : (fmt.kind === 'source' ? 'FOLLOW THE SOURCE' : 'TRY THIS FORMAT'),
        locked || runner().busy || !!runner().trial) +
      hint(fmt.kind === 'source' ? 'Restarts the PLAY’s decoder: a few seconds of black.' :
        'Restarts the PLAY’s decoder with it, then goes back unless you keep it within ' + esc((status && status.revert_seconds) || 30) + ' s.') +
      '</div></div>';
    return out;
  }

  function renderResolved() {
    var e = document.getElementById('bdout_resolved');
    if (!e) return;
    if (fmt.kind === 'source' || !resolved) { e.innerHTML = ''; return; }
    if (resolved.error) { e.innerHTML = '<span style="color:#c0392b;font-size:12px;">' + esc(resolved.error) + '</span>'; return; }
    var m = resolved.mode;
    e.innerHTML = '<div style="font-size:12px;">' + m.hdisplay + '×' + m.vdisplay + ' at ' + hz(resolved.refresh) + ' Hz on a ' +
      (m.clock_khz / 1000).toFixed(3).replace(/\.?0+$/, '') + ' MHz pixel clock</div>' +
      '<div style="font-size:11px;opacity:.75;font-family:monospace;word-break:break-all;">' + esc(resolved.modeline) + '</div>' +
      (resolved.notes || []).map(function (n) {
        return '<div style="font-size:12px;color:#e67e22;">' + esc(n) + '</div>';
      }).join('');
  }

  function rectInputs(prefix, r, locked) {
    return ['x', 'y', 'w', 'h'].map(function (k, i) {
      return '<label style="font-weight:normal;font-size:12px;margin-right:4px;">' + (k === 'w' ? 'W' : k === 'h' ? 'H' : k.toUpperCase()) + ' ' +
        numInput(prefix + '_' + k, r[i], locked, '5em') + '</label>';
    }).join('');
  }

  function aoiSection(locked) {
    var scr = screenSize();
    var pic = shimPicture();
    var off = locked || !aoi.on;
    var out = full('<b>Area of interest</b>') +
      row('Show part of the picture', '<label style="font-weight:normal;"><input type="checkbox" id="bdout_aoi_on"' +
        (aoi.on ? ' checked' : '') + (locked ? ' disabled' : '') + ' style="width:auto;margin-right:6px;vertical-align:middle;">on</label>' +
        hint('Takes at once, no restart. NDI’s own picture only: the PLAY’s OSD stays where it is.')) +
      row('Measured on a picture of', numInput('canvas_w', aoi.canvasW, off) + ' × ' + numInput('canvas_h', aoi.canvasH, off) +
        (pic && (pic[0] !== aoi.canvasW || pic[1] !== aoi.canvasH) ? ' ' + button('bdout_canvas_now', 'USE ' + pic[0] + '×' + pic[1], off) : '') +
        hint('The size you are cutting from — the sender’s canvas. A stream of another size gets the same proportions.')) +
      row('Show this part', rectInputs('src', aoi.src, off) +
        '<div style="display:flex;flex-wrap:wrap;gap:4px;margin-top:4px;">' +
        button('bdout_p_whole', 'WHOLE', off) + button('bdout_p_left', 'LEFT HALF', off) + button('bdout_p_right', 'RIGHT HALF', off) +
        button('bdout_p_top', 'TOP HALF', off) + button('bdout_p_bottom', 'BOTTOM HALF', off) + '</div>') +
      row('Or one screen of a wall', numInput('wall_cols', wall.cols, off, '3.5em') + ' across × ' + numInput('wall_rows', wall.rows, off, '3.5em') +
        ' down; this is ' + numInput('wall_col', wall.col, off, '3.5em') + ', ' + numInput('wall_row', wall.row, off, '3.5em') + ' ' +
        button('bdout_wall', 'SET', off) + hint('Column and row, counting from 1 at the top left.')) +
      row('Put it', '<label style="font-weight:normal;"><input type="checkbox" id="bdout_fill"' + (aoi.fill ? ' checked' : '') +
        (off ? ' disabled' : '') + ' style="width:auto;margin-right:6px;vertical-align:middle;">over the whole screen</label>' +
        (aoi.fill ? '' : '<div style="margin-top:4px;">' + rectInputs('dst', aoi.dst, off) + '</div>' +
          hint('In pixels of the ' + scr[0] + '×' + scr[1] + ' screen. Outside it the PLAY shows black.'))) +
      row('Shape', '<select id="bdout_aspect"' + (off ? ' disabled' : '') + '>' +
        '<option value="0"' + (aoi.aspect ? '' : ' selected') + '>Stretch to fit</option>' +
        '<option value="1"' + (aoi.aspect ? ' selected' : '') + '>Keep the shape (bars where it does not fit)</option></select>') +
      full('<div id="bdout_preview"></div>') +
      '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
        button('bdout_aoi_apply', busy ? 'Working…' : 'APPLY', locked) + '</div></div>';
    return out;
  }

  // Two small boxes: the picture with the part shown, and the screen with
  // where it goes.
  function diagram(p) {
    var pw = aoi.canvasW, ph = aoi.canvasH, sw = p.screen[0], sh = p.screen[1];
    var box = function (W, H, x, y, w, h, label) {
      var s = 150 / Math.max(W, H * 16 / 9);
      return '<svg width="' + Math.round(W * s + 2) + '" height="' + Math.round(H * s + 2) + '" style="margin-right:12px;vertical-align:top;">' +
        '<rect x="1" y="1" width="' + (W * s) + '" height="' + (H * s) + '" fill="none" stroke="currentColor" stroke-opacity=".5"/>' +
        '<rect x="' + (1 + x * s) + '" y="' + (1 + y * s) + '" width="' + (w * s) + '" height="' + (h * s) + '" fill="#22b24c" fill-opacity=".55"/>' +
        '<text x="5" y="13" font-size="10" fill="currentColor" fill-opacity=".8">' + label + '</text></svg>';
    };
    return box(pw, ph, p.sx, p.sy, p.sw, p.sh, 'picture') + box(sw, sh, p.dx, p.dy, p.dw, p.dh, 'screen');
  }

  function renderPreview() {
    var e = document.getElementById('bdout_preview');
    if (!e) return;
    if (!aoi.on || !preview) { e.innerHTML = ''; return; }
    if (preview.error) { e.innerHTML = '<span style="color:#c0392b;font-size:12px;">' + esc(preview.error) + '</span>'; return; }
    var p = preview;
    e.innerHTML = '<div style="margin:4px 0;">' + diagram(p) + '</div>' +
      '<div style="font-size:12px;">Shows ' + p.sw + '×' + p.sh + ' at ' + p.sx + ',' + p.sy + ' of the ' + aoi.canvasW + '×' + aoi.canvasH +
      ' picture, as ' + p.dw + '×' + p.dh + ' at ' + p.dx + ',' + p.dy + ' of the ' + p.screen[0] + '×' + p.screen[1] + ' screen.</div>';
  }

  // Every edit is read into fmt and aoi as it happens (wire), so a redraw
  // draws the state, never the other way round: a preset button sets aoi.src
  // and redraws, and reading the fields first would put the old crop back.
  function renderLeft() {
    var s = status || {};
    var ok = authed(s) && !s.unreachable && !!s.runner;
    var locked = !ok || busy;
    lastBanners = authBanner(s) + stateBanners(s);
    var left = lastBanners;
    if (ok) {
      left += formatSection(locked) + aoiSection(locked);
    }
    left += full('<div id="bdout_notice">' + noticeHTML() + '</div>');
    // A poll that changes nothing leaves the controls alone, so an open
    // drop-down is not closed under someone's cursor.
    if (left === lastLeft) return;
    lastLeft = left;
    document.getElementById('bdout_left').innerHTML = left;
    renderResolved();
    renderPreview();
    wire();
  }

  function line(label, html) {
    return '<div style="font-size:12px;"><span style="opacity:.7;">' + label + '</span> ' + html + '</div>';
  }

  function statusSection(s) {
    var r = s.runner || {};
    var sc = s.screen || {};
    var sh = s.shim || {};
    var out = full('<b>Status</b>');
    if (!s.runner) return out + full(pill('#7f8c8d', 'Waiting for bd-out…'));

    var state;
    if (r.trial) state = pill('#e67e22', 'Trying ' + r.trial.label);
    else if (!sh.loaded && s.decoder_running) state = pill('#e67e22', 'Shim not loaded: the PLAY’s own format and picture');
    else if (r.kept && r.kept.format && r.kept.format.kind !== 'source') state = pill('#22b24c', 'Forcing ' + r.kept_label);
    else state = pill('#7f8c8d', 'Following the source');
    out += full(state);

    var native = '';
    if (sc.native) {
      var n = sc.native;
      native = ', native ' + n.hdisplay + '×' + n.vdisplay + ' at ' + hz(n.clock_khz * 1000 / (n.htotal * n.vtotal)) + ' Hz' +
        (sc.native_ok ? '' : ' <span style="color:#e67e22;">(on a clock the PLAY cannot make)</span>');
    }
    out += full(
      line('Screen', sc.connected ? esc(sc.name || 'connected') + native : '<span style="color:#e67e22;">none connected</span>') +
      (sc.current ? line('HDMI now', esc(sc.current)) : '') +
      line('Kept format', esc(r.kept_label || '')) +
      line('Decoder', s.decoder_running ? 'running' : 'stopped') +
      line('Shim', sh.present ? (sh.loaded ? 'loaded' : 'not loaded (status from an earlier run)') : 'no word from it yet') +
      (sh.present && sh.conf && sh.conf !== 'ok' ? line('Its settings', '<span style="color:#c0392b;">' + esc(sh.conf) + '</span>') : '') +
      (sh.mode ? line('Its format', esc(sh.mode)) : '') +
      (sh.aoi ? line('Its area', esc(sh.aoi) + (sh.aoi_geometry ? ': ' + esc(sh.aoi_geometry) : '')) : '') +
      (sh.picture ? line('Picture', esc(sh.picture)) : '') +
      (s.aoi_preview_error ? line('Area', '<span style="color:#c0392b;">' + esc(s.aoi_preview_error) + '</span>') : '') +
      (r.last ? line('Last', '<span style="color:' + (r.last.kind === 'err' ? '#c0392b' : r.last.kind === 'warn' ? '#e67e22' : 'inherit') + ';">' +
        esc(new Date(r.last.time).toLocaleTimeString() + ' ' + r.last.text) + '</span>') : ''));
    if (sc.modes && sc.modes.length) {
      out += full('<div style="font-size:11px;opacity:.75;">The screen offers the PLAY: ' + esc(sc.modes.join(', ')) + '</div>');
    }
    return out;
  }

  function renderRight() {
    var s = status || {};
    var right =
      statusSection(s) +
      full(button('bdout_showlog', logOpen ? 'REFRESH LOG' : 'SHOW LOG', !authed(s)) +
        (logOpen ? '<pre id="bdout_log" style="max-height:16em;overflow:auto;font-size:11px;white-space:pre-wrap;margin-top:6px;"></pre>' : '')) +
      full('<div style="font-size:11px;opacity:.75;margin-top:6px;">' +
        'A format the screen cannot show leaves it black, which is why a new one is tried first and goes back on its own. ' +
        'Beta: built from the PLAY’s firmware and the RK3328’s kernel source, and not yet run on a PLAY. ' +
        'Tab and shim by <a href="https://github.com/stoatworks-labs/birddog-play-patcher/tree/main/output" target="_blank" rel="noopener">bdout</a> ' +
        esc(s.version || '') + '.</div>');
    document.getElementById('bdout_right').innerHTML = right;
    var b = document.getElementById('bdout_showlog');
    if (b) b.addEventListener('click', function () { showLog(); renderRight(); });
    if (logOpen) showLog();
  }

  function on(id, ev, fn) { var e = document.getElementById(id); if (e) e.addEventListener(ev, fn); }

  function wireTrial() {
    on('bdout_keep', 'click', function () { act('/api/keep', {}, null, 'Kept'); });
    on('bdout_revert', 'click', function () { act('/api/revert', {}, 'Going back…', null); });
  }

  function wire() {
    var left = document.getElementById('bdout_left');
    var formatEdit = function () { dirty = true; readForm(); resolve(); };
    var aoiEdit = function () { dirty = true; readForm(); previewAOI(); };
    left.querySelectorAll('#bdout_width, #bdout_height, #bdout_refresh, [id^="bdout_t_"]').forEach(function (e) {
      e.addEventListener('input', formatEdit);
      e.addEventListener('change', formatEdit);
    });
    on('bdout_standard', 'change', formatEdit);
    on('bdout_kind', 'change', function () { formatEdit(); renderLeft(); });
    left.querySelectorAll('[id^="bdout_src_"], [id^="bdout_dst_"], #bdout_canvas_w, #bdout_canvas_h, #bdout_aspect').forEach(function (e) {
      e.addEventListener('input', aoiEdit);
      e.addEventListener('change', aoiEdit);
    });
    left.querySelectorAll('[id^="bdout_wall_"]').forEach(function (e) {
      e.addEventListener('input', function () { dirty = true; readForm(); });
    });
    on('bdout_aoi_on', 'change', function () { aoiEdit(); renderLeft(); });
    on('bdout_fill', 'change', function () { aoiEdit(); renderLeft(); });
    on('bdout_canvas_now', 'click', function () {
      var p = shimPicture();
      if (!p) return;
      var r = toRect(aoi.src, aoi.canvasW, aoi.canvasH);
      aoi.canvasW = p[0]; aoi.canvasH = p[1];
      aoi.src = fromRect(r, p[0], p[1]);
      dirty = true; renderLeft(); previewAOI();
    });
    on('bdout_p_whole', 'click', function () { setCrop(0, 0, 1, 1); });
    on('bdout_p_left', 'click', function () { setCrop(0, 0, 0.5, 1); });
    on('bdout_p_right', 'click', function () { setCrop(0.5, 0, 0.5, 1); });
    on('bdout_p_top', 'click', function () { setCrop(0, 0, 1, 0.5); });
    on('bdout_p_bottom', 'click', function () { setCrop(0, 0.5, 1, 0.5); });
    on('bdout_wall', 'click', function () {
      readForm();
      setCrop((wall.col - 1) / wall.cols, (wall.row - 1) / wall.rows, 1 / wall.cols, 1 / wall.rows);
    });
    on('bdout_native', 'click', useNative);
    on('bdout_try', 'click', tryFormat);
    on('bdout_aoi_apply', 'click', applyAOI);
    on('bdout_restart', 'click', function () {
      act('/api/restart', {}, 'Restarting the PLAY’s decoder — a few seconds of black…', 'Restarted');
    });
  }

  poll();
})();
