/* The Stagetimer tab on the BirdDog PLAY's AV Setup page.
 *
 * Served from /static/, outside the Go template system, so the patch to
 * videoset.html stays two lines and everything the tab does lives here.
 *
 * Talks to bdstage's API on __BDSTAGE_API_PORT__, sending the birdUI session
 * cookie with every request (credentials: 'include'): putting the timer on
 * HDMI stops the PLAY's own decoder, and the settings hold a Stagetimer key,
 * so both are gated on the same login as the rest of the web UI. Status is
 * open, so the tab can say why it is locked.
 */
(function () {
  'use strict';

  var API_PORT = __BDSTAGE_API_PORT__;
  var API = location.protocol + '//' + location.hostname + ':' + API_PORT;
  var P = 'bdstage_';

  var root = document.getElementById('bdstage_form');
  if (!root) return;

  var status = null;   // last /api/status
  var cfg = null;      // last /api/config
  var busy = false;
  var notice = null;
  var timer = null;
  var dirty = false;   // unsaved edits; polling must not redraw them
  var logOpen = false;
  var previewURL = null;
  var previewAt = 0;
  var previewBusy = false;

  var VIEWS = [
    ['timer', 'Timer — the speaker’s countdown, like Stagetimer’s Viewer'],
    ['agenda', 'Agenda — the rundown, the current item counting down'],
    ['split', 'Timer and agenda side by side'],
    ['clock', 'Clock — the time of day in the room’s time zone']
  ];
  var MIRRORS = [['', 'No'], ['h', 'Left–right (for a teleprompter)'], ['v', 'Upside down'], ['hv', 'Both']];

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

  function authed(s) {
    return !!(s && s.auth && (s.auth.authenticated || !s.auth.required));
  }

  function visible() {
    return root.offsetParent !== null;
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
        if (authed(status) && !status.unreachable && !cfg) return loadConfig();
      })
      .then(render)
      .then(function () {
        if (visible() && authed(status) && !status.unreachable && cfg && cfg.api_key_set &&
            Date.now() - previewAt > 2500) refreshPreview();
        clearTimeout(timer);
        timer = setTimeout(poll, 2000);
      });
  }

  function loadConfig() {
    return api('/api/config').then(function (b) {
      cfg = b.config;
      dirty = false;
    }).catch(function (e) {
      notice = { kind: 'err', text: e.message };
    });
  }

  function refreshPreview() {
    if (previewBusy) return;
    previewBusy = true;
    previewAt = Date.now();
    var view = (document.getElementById(P + 'view') || {}).value || '';
    fetch(API + '/api/preview.png' + (view ? '?view=' + encodeURIComponent(view) : ''), { credentials: 'include' })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.blob(); })
      .then(function (b) {
        var img = document.getElementById(P + 'preview');
        if (!img) return;
        if (previewURL) URL.revokeObjectURL(previewURL);
        previewURL = URL.createObjectURL(b);
        img.src = previewURL;
      })
      .catch(function () { /* the status panel says why */ })
      .then(function () { previewBusy = false; });
  }

  function showing() { return !!(status && status.showing); }

  function show() {
    if (busy) return;
    if (dirty) { save(true); return; }
    busy = true;
    notice = { kind: 'ok', text: 'Handing HDMI over — the PLAY’s own decoder stops for this, which takes a few seconds.' };
    renderLeft();
    api('/api/show', { method: 'POST', body: '{}' })
      .then(function () { notice = null; })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function stop() {
    if (busy) return;
    busy = true;
    notice = { kind: 'ok', text: 'Giving HDMI back to the PLAY’s own decoder…' };
    renderLeft();
    api('/api/stop', { method: 'POST', body: '{}' })
      .then(function () { notice = null; })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function save(thenShow) {
    if (busy || !cfg) return;
    readForm();
    busy = true;
    notice = null;
    renderLeft();
    api('/api/config', { method: 'POST', body: JSON.stringify(cfg) })
      .then(function (b) {
        cfg = b.config;
        dirty = false;
        notice = { kind: 'ok', text: showing() ? 'Saved — the screen has the new settings' : 'Saved' };
        previewAt = 0;
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; thenShow = false; })
      .then(function () {
        busy = false;
        if (thenShow === true) { show(); return; }
        return poll();
      });
  }

  function test() {
    if (busy || !cfg) return;
    readForm();
    busy = true;
    notice = { kind: 'ok', text: 'Asking Stagetimer…' };
    renderLeft();
    api('/api/test', { method: 'POST', body: JSON.stringify({ room_id: cfg.room_id, api_key: cfg.api_key || '', api_base: cfg.api_base || '' }) })
      .then(function (b) {
        if (b.ok) {
          notice = { kind: 'ok', text: 'That works: “' + (b.room || 'unnamed room') + '”, ' + b.timers + ' timer' + (b.timers === 1 ? '' : 's') +
            (b.timezone ? ', ' + b.timezone : '') + '.' + (dirty ? ' Press APPLY to keep it.' : '') };
        } else {
          notice = { kind: 'err', text: b.error || 'Stagetimer said no' };
        }
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; renderLeft(); });
  }

  function showLog() {
    logOpen = true;
    api('/api/log').then(function (b) {
      var pre = document.getElementById(P + 'log');
      if (!pre) return;
      var lines = (b.lines || []).map(function (l) {
        var t = new Date(l.time);
        return t.toLocaleTimeString() + ' ' + (l.level === 'info' ? '' : l.level.toUpperCase() + ' ') + l.text;
      });
      pre.textContent = lines.length ? lines.join('\n') : '(empty)';
      pre.scrollTop = pre.scrollHeight;
    }).catch(function (e) {
      var pre = document.getElementById(P + 'log');
      if (pre) pre.textContent = e.message;
    });
  }

  // ----------------------------------------------------------- form state

  function readForm() {
    if (!cfg) return;
    var g = function (k) { return document.getElementById(P + k); };
    if (g('room')) cfg.room_id = g('room').value.trim();
    if (g('key')) cfg.api_key = g('key').value.trim();
    if (g('base')) cfg.api_base = g('base').value.trim();
    var d = cfg.display = cfg.display || {};
    if (g('view')) d.view = g('view').value;
    if (g('mirror')) d.mirror = g('mirror').value;
    ['show_title', 'show_speaker', 'show_progress', 'show_next', 'show_clock', 'show_messages', 'clock_24h'].forEach(function (k) {
      if (g(k)) d[k] = g(k).checked;
    });
    if (g('resume')) cfg.resume = g('resume').checked;
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
    return '<button type="button" class="restart" id="' + P + id + '"' +
      (disabled ? ' disabled' : '') + ' style="white-space:nowrap;padding:4px 10px;">' + esc(text) + '</button>';
  }

  // birdUI styles every <label> as an upper-case toggle button; these are
  // plain checkboxes, so the label is put back to plain text.
  function check(id, on, text, disabled) {
    return '<label style="display:block;margin:0;padding:3px 0;background:none;border-radius:0;' +
      'text-transform:none;text-align:left;font-weight:normal;font-family:inherit;"><input type="checkbox" id="' + P + id + '"' +
      (on ? ' checked' : '') + (disabled ? ' disabled' : '') +
      ' style="width:auto;margin-right:6px;vertical-align:middle;">' + text + '</label>';
  }

  // Wrapped as the stock page wraps its selects, for birdUI's dropdown look.
  function select(id, options, value, disabled) {
    return '<span class="custom-dropdown" style="display:block;position:relative;">' +
      '<select id="' + P + id + '" style="width:100%;"' + (disabled ? ' disabled' : '') + '>' +
      options.map(function (o) {
        return '<option value="' + esc(o[0]) + '"' + (o[0] === value ? ' selected' : '') + '>' + esc(o[1]) + '</option>';
      }).join('') + '</select></span>';
  }

  function pill(colour, text) {
    return '<span style="display:inline-block;width:.6em;height:.6em;border-radius:50%;background:' +
      colour + ';margin-right:.5em;vertical-align:middle;"></span>' + esc(text);
  }

  function banner(colour, text) {
    return '<div class="p-2 mb-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + esc(text) + '</div>';
  }

  function noticeHTML() {
    if (!notice) return '';
    var colour = notice.kind === 'ok' ? '#22b24c' : notice.kind === 'warn' ? '#e67e22' : '#c0392b';
    return '<div class="p-2 mt-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + esc(notice.text) + '</div>';
  }

  function authBanner(s) {
    var a = s.auth || {};
    if (a.authenticated || !a.required) return '';
    return banner('#e67e22', a.reason || 'Log in to birdUI to use this tab.');
  }

  // Two columns. The left one is the controls and is only redrawn on demand —
  // a poll must never rebuild it under someone's cursor; the right one is
  // status and the preview, redrawn on every poll.
  function skeleton() {
    if (document.getElementById(P + 'left')) return;
    root.innerHTML =
      '<div class="row m-0 pt-2 div_tab_contents">' +
        '<div class="col-xl-6 m-0 p-0 pr-xl-2"><div id="' + P + 'left"></div></div>' +
        '<div class="col-xl-6 m-0 p-0 pl-xl-2">' +
          '<div id="' + P + 'right"></div>' +
          '<div class="row m-0 p-1"><div class="col-xl-12 m-0 p-0">' +
            '<img id="' + P + 'preview" alt="" style="width:100%;aspect-ratio:16/9;background:#000;border:1px solid rgba(127,127,127,.35);border-radius:4px;">' +
            '<div style="font-size:11px;opacity:.7;margin-top:3px;">Preview: what the PLAY shows on HDMI — or would, if you press Show.</div>' +
          '</div></div>' +
          '<div id="' + P + 'logbox"></div>' +
        '</div>' +
      '</div>';
  }

  function render() {
    skeleton();
    renderRight();
    if (!dirty || busy) renderLeft();
    else renderNotice();
  }

  function renderNotice() {
    var n = document.getElementById(P + 'notice');
    if (n) n.innerHTML = noticeHTML();
  }

  function renderLeft() {
    if (dirty) readForm(); // keep unsaved settings across a redraw
    var s = status || {};
    var ok = authed(s) && !s.unreachable;
    var locked = !ok || busy;
    var left = authBanner(s);
    if (s.unreachable) left += banner('#c0392b', 'The Stagetimer service is not responding (bd-stagetimer): ' + s.unreachable);
    if (s.caps && !s.caps.raw_video) left += banner('#c0392b', 'This PLAY’s GStreamer has no rawvideoparse, so nothing can be drawn on HDMI.');

    if (cfg) {
      var d = cfg.display || {};
      left +=
        full('<b>Room</b>') +
        row('Room ID', '<input type="text" id="' + P + 'room" value="' + esc(cfg.room_id) + '"' + (locked ? ' disabled' : '') +
            ' style="width:100%;" autocomplete="off" spellcheck="false" placeholder="e.g. A1B2C3D4">' +
            hint('the part of the room’s Stagetimer address after /r/ — or paste the whole address')) +
        // cfg.api_key is only ever what was typed here (the API never sends
        // the key back), so a redraw while TEST runs must not drop it.
        row('API key', '<input type="password" id="' + P + 'key" value="' + esc(cfg.api_key || '') + '"' + (locked ? ' disabled' : '') +
            ' style="width:100%;" autocomplete="new-password" spellcheck="false" placeholder="' +
            (cfg.api_key_set ? 'saved (' + esc(cfg.api_key_hint) + ') — leave empty to keep it' : 'paste the room’s API key') + '">' +
            hint('in Stagetimer: the room’s Controller page › menu › API. The key can also start and stop the room’s timers, so it stays on the PLAY and is never shown again.')) +
        '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
          button('test', 'TEST', locked) + '</div></div>' +
        full('<b>Screen</b>') +
        row('Show', select('view', VIEWS, d.view || 'timer', locked)) +
        row('Timer view', check('show_title', d.show_title, 'the timer’s title above it', locked) +
            check('show_speaker', d.show_speaker, 'the speaker below it', locked) +
            check('show_progress', d.show_progress, 'a progress bar with the wrap-up zones', locked) +
            check('show_next', d.show_next, 'what comes next', locked)) +
        row('Everywhere', check('show_clock', d.show_clock, 'the time of day in the corner', locked) +
            check('clock_24h', d.clock_24h, '24-hour clock', locked) +
            check('show_messages', d.show_messages, 'messages sent from the Controller', locked)) +
        row('Mirror', select('mirror', MIRRORS, d.mirror || '', locked)) +
        row('After a restart', check('resume', cfg.resume, 'put the timer back on HDMI', locked)) +
        row('API address', '<input type="text" id="' + P + 'base" value="' + esc(cfg.api_base || '') + '"' + (locked ? ' disabled' : '') +
            ' style="width:100%;" placeholder="https://api.stagetimer.io">' + hint('leave empty unless you are testing')) +
        '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0" style="display:flex;gap:6px;flex-wrap:wrap;">' +
          button('save', busy ? 'Working…' : 'APPLY', locked) +
          button('show', showing() ? 'ON HDMI' : 'SHOW ON HDMI', locked || showing() || !cfg.api_key_set && !(document.getElementById(P + 'key') || {}).value) +
          button('stop', 'STOP', locked || !showing()) +
        '</div></div>';
    } else if (ok) {
      left += full('Loading settings…');
    }
    left += full('<div id="' + P + 'notice">' + noticeHTML() + '</div>');
    document.getElementById(P + 'left').innerHTML = left;
    matchTextInput(P + 'key', P + 'room');
    wire();
  }

  // birdUI styles text inputs but not password ones; borrow the room field's
  // computed look, so the key field follows the dark or light theme.
  function matchTextInput(id, like) {
    var el = document.getElementById(id), ref = document.getElementById(like);
    if (!el || !ref || !window.getComputedStyle) return;
    var cs = getComputedStyle(ref);
    ['backgroundColor', 'color', 'borderTop', 'borderRight', 'borderBottom', 'borderLeft', 'borderRadius',
      'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft', 'fontSize', 'fontFamily', 'textAlign', 'outline'].forEach(function (k) {
      el.style[k] = cs[k];
    });
  }

  function feedLine(f) {
    if (!f) return '';
    var names = {
      live: ['#22b24c', 'Connected to Stagetimer'],
      polling: ['#e67e22', 'Connected over HTTP (the WebSocket is blocked)'],
      connecting: ['#e67e22', 'Connecting to Stagetimer…'],
      offline: ['#c0392b', 'Lost Stagetimer — retrying'],
      refused: ['#c0392b', 'Stagetimer refused the room ID or key'],
      idle: ['#7f8c8d', 'Not connected']
    };
    var n = names[f.state] || ['#7f8c8d', f.state];
    var out = pill(n[0], n[1]);
    if (f.detail) out += '<div style="font-size:12px;opacity:.8;margin-top:2px;">' + esc(f.detail) + '</div>';
    if (f.clock_offset_ms && Math.abs(f.clock_offset_ms) >= 1000) {
      out += '<div style="font-size:12px;opacity:.8;margin-top:2px;">This PLAY’s clock is ' + (Math.abs(f.clock_offset_ms) / 1000).toFixed(1) +
        ' s ' + (f.clock_offset_ms > 0 ? 'behind' : 'ahead of') + ' Stagetimer’s; the countdown corrects for it.</div>';
    }
    return out;
  }

  function renderRight() {
    var s = status || {};
    var sc = s.screen || {};
    var out = full('<b>Status</b>');
    switch (sc.phase) {
      case 'showing': out += full(pill('#22b24c', 'On HDMI') + (sc.output ? ' <span style="font-size:12px;opacity:.7;">' + esc(sc.output) + ', drawn at ' + esc(sc.raster) + '</span>' : '')); break;
      case 'starting': out += full(pill('#e67e22', 'Taking HDMI…')); break;
      case 'failed': out += full(pill('#c0392b', 'Stopped by an error') + '<div style="font-size:12px;color:#c0392b;">' + esc(sc.detail) + '</div>'); break;
      default: out += full(pill('#7f8c8d', 'Off — the PLAY’s own decoder has HDMI'));
    }
    if (sc.feed) out += full(feedLine(sc.feed));
    else if (s.configured === false) out += full('<span style="font-size:12px;opacity:.8;">Enter a room ID and API key, TEST, then APPLY.</span>');
    out += full('<div style="font-size:11px;opacity:.75;margin-top:6px;">' +
      'Showing the timer stops the PLAY’s own decoder (BirdDogRunner) until you press Stop, so NDI, the OSD and tally are gone meanwhile. ' +
      'Beta: written against Stagetimer’s documented API and not yet run on a PLAY. Not affiliated with Stagetimer. ' +
      'Tab by bd-play-stagetimer ' + esc(s.version || '') + '.</div>');
    document.getElementById(P + 'right').innerHTML = out;

    var lb = document.getElementById(P + 'logbox');
    if (lb && !document.getElementById(P + 'showlog')) {
      lb.innerHTML = full(button('showlog', 'SHOW LOG', false) +
        '<pre id="' + P + 'log" style="display:none;max-height:16em;overflow:auto;font-size:11px;white-space:pre-wrap;margin-top:6px;"></pre>');
      document.getElementById(P + 'showlog').addEventListener('click', function () {
        document.getElementById(P + 'log').style.display = 'block';
        this.textContent = 'REFRESH LOG';
        showLog();
      });
    }
    var sl = document.getElementById(P + 'showlog');
    if (sl) sl.disabled = !authed(s);
    if (logOpen) showLog();
  }

  function wire() {
    var left = document.getElementById(P + 'left');
    var on = function (id, ev, fn) { var e = document.getElementById(P + id); if (e) e.addEventListener(ev, fn); };
    left.querySelectorAll('input, select').forEach(function (e) {
      e.addEventListener(e.tagName === 'SELECT' || e.type === 'checkbox' ? 'change' : 'input', function () { dirty = true; });
    });
    on('view', 'change', function () { previewAt = 0; refreshPreview(); });
    on('test', 'click', test);
    on('save', 'click', function () { save(false); });
    on('show', 'click', show);
    on('stop', 'click', stop);
  }

  poll();
})();
