/* The OMT tab on the BirdDog PLAY's AV Setup page.
 *
 * Served from /static/, outside the Go template system, so the patch to
 * videoset.html stays two lines and everything the tab does lives here.
 *
 * Talks to bdomt's API on __BDOMT_API_PORT__, sending the birdUI session
 * cookie with every request (credentials: 'include'): putting a source on
 * HDMI stops the PLAY's own decoder, so it is gated on the same login as the
 * rest of the web UI. Status is open, so the tab can say why it is locked.
 */
(function () {
  'use strict';

  var API_PORT = __BDOMT_API_PORT__;
  var API = location.protocol + '//' + location.hostname + ':' + API_PORT;

  var root = document.getElementById('bdomt_form');
  if (!root) return;

  var status = null;      // last /api/status
  var cfg = null;         // last /api/config
  var sources = null;     // last /api/sources
  var browsing = false;
  var picked = '';        // the chosen discovered source's name, or '' for "type an address"
  var typed = '';         // the address box
  var busy = false;
  var notice = null;
  var timer = null;
  var dirty = false;      // the settings have unsaved edits; polling must not redraw them
  var logOpen = false;

  var QUALITIES = [
    ['Default', 'Whatever the sender is set to'],
    ['Low', 'Low'],
    ['Medium', 'Medium'],
    ['High', 'High']
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

  function authed(s) {
    return !!(s && s.auth && (s.auth.authenticated || !s.auth.required));
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
        if (authed(status) && !status.unreachable) {
          var waits = [];
          if (!cfg) waits.push(loadConfig());
          if (!sources) waits.push(browse(false));
          return Promise.all(waits);
        }
      })
      .then(render)
      .then(function () {
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

  function browse(refresh) {
    browsing = true;
    renderSourceList();
    return api('/api/sources' + (refresh ? '?refresh=1' : ''))
      .then(function (b) {
        sources = b.sources || [];
        if (b.error && !sources.length) notice = { kind: 'warn', text: 'Looking for sources: ' + b.error };
      })
      .catch(function (e) { sources = sources || []; notice = { kind: 'err', text: e.message }; })
      .then(function () { browsing = false; renderLeft(); });
  }

  function chosenSource() {
    if (picked) {
      var d = (sources || []).filter(function (x) { return x.name === picked; })[0];
      return { name: picked, address: d ? d.address : '' };
    }
    return { name: '', address: typed.trim() };
  }

  function show() {
    if (busy) return;
    var src = chosenSource();
    if (!src.name && !src.address) {
      notice = { kind: 'warn', text: 'Choose a source, or type its address.' };
      renderNotice();
      return;
    }
    busy = true;
    notice = { kind: 'ok', text: 'Handing HDMI over — the PLAY’s own decoder stops for this, which takes a few seconds.' };
    renderLeft();
    api('/api/show', { method: 'POST', body: JSON.stringify({ source: src }) })
      .then(function (b) { status.session = b.session; notice = null; })
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

  function save() {
    if (busy || !cfg) return;
    readForm();
    busy = true;
    notice = null;
    renderLeft();
    api('/api/config', { method: 'POST', body: JSON.stringify(cfg) })
      .then(function (b) {
        cfg = b.config;
        dirty = false;
        notice = { kind: 'ok', text: live() ? 'Saved — the stream restarted with the new settings' : 'Saved' };
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function showLog() {
    logOpen = true;
    api('/api/log').then(function (b) {
      var pre = document.getElementById('bdomt_log');
      if (!pre) return;
      var lines = (b.lines || []).map(function (l) {
        var t = new Date(l.time);
        return t.toLocaleTimeString() + ' ' + (l.level === 'info' ? '' : l.level.toUpperCase() + ' ') + l.text;
      });
      pre.textContent = lines.length ? lines.join('\n') : '(empty)';
      pre.scrollTop = pre.scrollHeight;
    }).catch(function (e) {
      var pre = document.getElementById('bdomt_log');
      if (pre) pre.textContent = e.message;
    });
  }

  function live() {
    var s = status && status.session;
    return !!(s && s.state && s.state !== 'idle');
  }

  // ----------------------------------------------------------- form state

  function readForm() {
    if (!cfg) return;
    var g = function (k) { return document.getElementById('bdomt_' + k); };
    if (g('quality')) cfg.quality = g('quality').value;
    if (g('threads')) cfg.threads = parseInt(g('threads').value, 10) || 0;
    ['audio', 'tally', 'hold_on_loss', 'match_output', 'resume'].forEach(function (k) {
      if (g(k)) cfg[k] = g(k).checked;
    });
  }

  function readSource() {
    var sel = document.getElementById('bdomt_source');
    if (sel) picked = sel.value;
    var addr = document.getElementById('bdomt_address');
    if (addr) typed = addr.value;
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

  function check(id, on, text, disabled) {
    return '<label style="font-weight:normal;"><input type="checkbox" id="bdomt_' + id + '"' +
      (on ? ' checked' : '') + (disabled ? ' disabled' : '') +
      ' style="width:auto;margin-right:6px;vertical-align:middle;">' + text + '</label>';
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

  function banner(colour, text) {
    return '<div class="p-2 mb-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + esc(text) + '</div>';
  }

  function capsBanners(s) {
    var c = s.caps;
    if (!c) return '';
    var out = '';
    if (!c.vmx) out += banner('#c0392b', 'The VMX decoder did not load, so nothing can be shown: ' + (c.vmx_error || 'unknown error') + '. Reinstall the OMT receiver from the patcher.');
    if (!c.raw_video) out += banner('#c0392b', 'This PLAY’s GStreamer has no rawvideoparse, so decoded video has no way onto HDMI.');
    if (!c.audio) out += banner('#e67e22', 'This PLAY’s GStreamer has no rawaudioparse: sources are shown without sound.');
    return out;
  }

  function authBanner(s) {
    var a = s.auth || {};
    if (a.authenticated || !a.required) return '';
    return banner('#e67e22', a.reason || 'Log in to birdUI to use this tab.');
  }

  function sourceOptions() {
    var opts = '<option value=""' + (picked ? '' : ' selected') + '>Type an address below</option>';
    var seen = false;
    (sources || []).forEach(function (d) {
      if (d.name === picked) seen = true;
      opts += '<option value="' + esc(d.name) + '"' + (d.name === picked ? ' selected' : '') + '>' +
        esc(d.label || d.name) + ' — ' + esc(d.host) + '</option>';
    });
    // The saved source, even when the browse has not found it (yet).
    if (picked && !seen) opts += '<option value="' + esc(picked) + '" selected>' + esc(picked) + ' (not seen just now)</option>';
    return opts;
  }

  function renderSourceList() {
    var sel = document.getElementById('bdomt_source');
    if (sel) sel.innerHTML = sourceOptions();
    var r = document.getElementById('bdomt_refresh');
    if (r) { r.disabled = browsing; r.textContent = browsing ? 'LOOKING…' : 'LOOK AGAIN'; }
  }

  // The tab is two columns. The left one is the controls and is only redrawn
  // on demand — a poll must never rebuild it under someone's cursor; the right
  // one is status and is redrawn on every poll.
  function skeleton() {
    if (document.getElementById('bdomt_left')) return;
    root.innerHTML =
      '<div class="row m-0 pt-2 div_tab_contents">' +
        '<div class="col-xl-6 m-0 p-0 pr-xl-2"><div id="bdomt_left"></div></div>' +
        '<div class="col-xl-6 m-0 p-0 pl-xl-2"><div id="bdomt_right"></div></div>' +
      '</div>';
  }

  var seeded = false;

  function render() {
    skeleton();
    var s = status || {};
    // First sight of the saved source fills the picker once, never again: after
    // that the picker is the person's.
    if (!seeded && s.saved_source) {
      picked = s.saved_source.name || '';
      typed = picked ? '' : (s.saved_source.address || '');
      seeded = true;
    }
    renderRight();
    if (!dirty || busy) renderLeft();
    else renderNotice();
  }

  function renderNotice() {
    var n = document.getElementById('bdomt_notice');
    if (n) n.innerHTML = noticeHTML();
  }

  function renderLeft() {
    if (dirty) readForm(); // keep unsaved settings across a redraw for Show, Stop or the picker
    var s = status || {};
    var ok = authed(s) && !s.unreachable;
    var canShow = ok && s.caps && s.caps.vmx && s.caps.raw_video;
    var locked = !ok || busy;
    var left = authBanner(s) + capsBanners(s);
    if (s.unreachable) left += banner('#c0392b', 'The OMT service is not responding (bd-omt): ' + s.unreachable);

    left +=
      full('<b>Source</b>') +
      row('On the network', '<div style="display:flex;gap:6px;align-items:center;">' +
          '<select id="bdomt_source" style="flex:1;min-width:0;"' + (locked ? ' disabled' : '') + '>' + sourceOptions() + '</select>' +
          button('bdomt_refresh', browsing ? 'LOOKING…' : 'LOOK AGAIN', locked || browsing) + '</div>' +
          hint('OMT senders announce themselves over Bonjour (mDNS); one on another subnet or VLAN will not show up here — type its address instead')) +
      row('Or an address', '<input type="text" id="bdomt_address" value="' + esc(typed) + '"' +
          (locked || picked ? ' disabled' : '') + ' style="width:100%;" placeholder="192.168.1.50:6400">' +
          hint('host:port, or omt://host:port. Senders start at port 6400 and count up.')) +
      '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0" style="display:flex;gap:6px;">' +
        button('bdomt_show', busy ? 'Working…' : (live() ? 'SWITCH' : 'SHOW ON HDMI'), locked || !canShow) +
        button('bdomt_stop', 'STOP', locked || (!live() && !s.on_air)) +
      '</div></div>';

    if (cfg) {
      var qOpts = QUALITIES.map(function (q) {
        return '<option value="' + q[0] + '"' + (cfg.quality === q[0] ? ' selected' : '') + '>' + esc(q[1]) + '</option>';
      }).join('');
      var tOpts = '';
      for (var t = 0; t <= 4; t++) {
        tOpts += '<option value="' + t + '"' + (cfg.threads === t ? ' selected' : '') + '>' + (t === 0 ? 'libvmx’s choice' : t) + '</option>';
      }
      left +=
        full('<b>Settings</b>') +
        row('Ask for quality', '<select id="bdomt_quality"' + (locked ? ' disabled' : '') + '>' + qOpts + '</select>' +
            hint('a request, not an order: the sender encodes for whichever receiver asks for the most')) +
        row('Sound', check('audio', cfg.audio, 'play the source’s first two channels on HDMI', locked || (s.caps && !s.caps.audio))) +
        row('Tally', check('tally', cfg.tally, 'tell the sender it is on program while shown', locked)) +
        row('When it stops', check('hold_on_loss', cfg.hold_on_loss, 'hold the last frame (off: go to black)', locked)) +
        row('Output', check('match_output', cfg.match_output, 'let kmssink set the HDMI mode', locked) +
            hint('off: the picture is placed on whatever mode the PLAY is already putting out, as the PLAY’s own decoder does')) +
        row('After a restart', check('resume', cfg.resume, 'put the last source back on HDMI', locked)) +
        row('Decode threads', '<select id="bdomt_threads"' + (locked ? ' disabled' : '') + '>' + tOpts + '</select>') +
        '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
          button('bdomt_save', busy ? 'Working…' : 'APPLY', locked) + '</div></div>';
    } else if (ok) {
      left += full('Loading settings…');
    }
    left += full('<div id="bdomt_notice">' + noticeHTML() + '</div>');
    document.getElementById('bdomt_left').innerHTML = left;
    wire();
  }

  function statePill(s) {
    var se = s.session || {};
    var rx = se.receiver || {};
    switch (se.state) {
      case 'live': return pill('#22b24c', 'On HDMI');
      case 'waiting': return pill('#e67e22', rx.connected ? 'Connected, waiting for pictures' : 'Connecting…');
      case 'error': return pill('#c0392b', 'Stopped by an error');
      default: return pill('#7f8c8d', s.on_air ? 'Not running (it was on air)' : 'Off — the PLAY’s own decoder has HDMI');
    }
  }

  function line(label, text) {
    return '<div style="font-size:12px;"><span style="opacity:.7;">' + label + '</span> ' + text + '</div>';
  }

  function statusSection(s) {
    var se = s.session || {};
    var rx = se.receiver || {};
    var v = rx.video;
    var a = rx.audio;
    var out = full('<b>Status</b>') + full(statePill(s));
    if (se.error) out += full('<span style="color:#c0392b;font-size:12px;">' + esc(se.error) + '</span>');
    if (se.state && se.state !== 'idle') {
      var src = se.source || {};
      out += full(
        line('Source', esc(src.name || src.address) + (rx.connected_to ? ' at ' + esc(rx.connected_to) : '')) +
        (rx.sender && rx.sender.product ? line('Sender', esc([rx.sender.manufacturer, rx.sender.product, rx.sender.version].filter(Boolean).join(' '))) : '') +
        (v ? line('Video', v.width + '×' + v.height + (v.interlaced ? 'i' : 'p') + ' ' + esc(v.frame_rate) + ' fps, ' + esc(v.codec) +
          (v.interlaced ? ' <span style="color:#c0392b;">— interlaced is not supported</span>' : '')) : '') +
        (a ? line('Audio', esc(a.channels) + ' ch at ' + esc(a.sample_rate) + ' Hz' + (se.audio ? '' : ' (sound is off)')) : '') +
        (se.output ? line('HDMI', se.output.width + '×' + se.output.height) : '') +
        line('Showing', esc(se.fps) + ' fps, decode ' + esc(se.decode_ms) + ' ms' + (se.threads ? ' on ' + se.threads + ' threads' : '')) +
        line('Counts', esc(rx.video_frames || 0) + ' received, ' + esc(se.shown || 0) + ' shown, ' +
          esc((rx.dropped || 0) + (se.sink_dropped || 0)) + ' skipped, ' + esc(se.decode_errors || 0) + ' bad' +
          (rx.reconnects ? ', ' + rx.reconnects + ' reconnects' : '') +
          (se.sink_restarts ? ', output restarted ' + se.sink_restarts + '×' : '')) +
        (se.audio_error ? line('Sound', '<span style="color:#e67e22;">' + esc(se.audio_error) + '</span>') : '') +
        (rx.redirected_to ? line('Redirected', 'to ' + esc(rx.redirected_to)) : '') +
        (rx.last_error && !rx.connected ? line('Last error', esc(rx.last_error)) : ''));
    }
    var sys = s.sys || {};
    if (sys.cpu_busy != null) {
      var hot = sys.temp_c >= 80;
      out += full(line('PLAY', 'CPU ' + esc(sys.cpu_busy) + '% (OMT ' + esc(sys.cpu_process) + '% of a core)' +
        (sys.temp_c ? ', <span' + (hot ? ' style="color:#c0392b;"' : '') + '>' + esc(sys.temp_c) + ' °C</span>' : '')));
    }
    return out;
  }

  function renderRight() {
    var s = status || {};
    var right =
      statusSection(s) +
      full(button('bdomt_showlog', logOpen ? 'REFRESH LOG' : 'SHOW LOG', !authed(s)) +
        (logOpen ? '<pre id="bdomt_log" style="max-height:16em;overflow:auto;font-size:11px;white-space:pre-wrap;margin-top:6px;"></pre>' : '')) +
      full('<div style="font-size:11px;opacity:.75;margin-top:6px;">' +
        'Showing a source stops the PLAY’s own decoder (BirdDogRunner) until you press Stop, so NDI, the OSD and its tally are gone meanwhile. ' +
        'Beta: written against the OMT 1.0 protocol and not yet run on a PLAY. ' +
        '<a href="https://github.com/openmediatransport" target="_blank" rel="noopener">Open Media Transport</a>; VMX decoding by libvmx (MIT); tab by ' +
        '<a href="https://github.com/stoatworks-labs/birddog-play-patcher/tree/main/omt" target="_blank" rel="noopener">bdomt</a> ' + esc(s.version || '') + '.</div>');
    document.getElementById('bdomt_right').innerHTML = right;
    var b = document.getElementById('bdomt_showlog');
    if (b) b.addEventListener('click', function () { showLog(); renderRight(); });
    if (logOpen) showLog();
  }

  function wire() {
    var left = document.getElementById('bdomt_left');
    var on = function (id, ev, fn) { var e = document.getElementById(id); if (e) e.addEventListener(ev, fn); };
    left.querySelectorAll('#bdomt_quality, #bdomt_threads, input[type=checkbox]').forEach(function (e) {
      e.addEventListener('change', function () { dirty = true; });
    });
    on('bdomt_source', 'change', function () { readSource(); renderLeft(); });
    on('bdomt_address', 'input', function () { readSource(); });
    on('bdomt_refresh', 'click', function () { readSource(); browse(true); });
    on('bdomt_show', 'click', function () { readSource(); show(); });
    on('bdomt_stop', 'click', stop);
    on('bdomt_save', 'click', save);
  }

  poll();
})();
