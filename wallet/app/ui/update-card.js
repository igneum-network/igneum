/* The update card (pure; update-card.test.mjs loads this file): the same card, rules and words as the miner's
   (app/igneum-app/ui/app.js, UpdateCard), with the wallet's name and the wallet's reasons to wait.
   A centred card over the window for one update: the mark with a progress ring, "Igneum Wallet 0.0.1", one line
   (is available, is downloading, is ready to install, Installing, did not install), up to three lines of release
   notes with the rest behind "What changed", the size, Install now and Later.
   model() turns state.update into what the card says. decide() says whether it shows now, given the window
   (ctx) and what the card already did (mem: open, later, seen):
     deferred  while the send screen is open, while Touch ID (or Windows Hello) is on screen, while a wallet is
               being created or imported, while the app quits
     later     Later, Escape or the backdrop hides this version at this stage; the banner keeps it
     back      a newer version, or the download ready while automatic updates are off (with them on it installs
               by itself, so a dismissed download stays dismissed); an open card follows its update to the end
     installing and failed show only on an open card (Install now was pressed here); the banner carries the rest */
var UpdateCard = (function () {
  'use strict';
  var NAME = 'Igneum Wallet', LINE_MAX = 70, LINES = 3;
  var INSTALL = { act: 'install', label: 'Install now', primary: true }, LATER = { act: 'later', label: 'Later' };
  var RETRY = { act: 'retry', label: 'Try again', primary: true }, OPEN = { act: 'open', label: 'Open the download', primary: true };
  function cap(t) { return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''; }
  function firstLine(t, max) { t = String(t || '').split('\n')[0].trim(); max = max || 160; return t.length > max ? t.slice(0, max - 1).trim() + '…' : t; }
  // the manifest's notes as sentences: split on line breaks, then after . ! ? ; (no lookbehind: the Mac WebView)
  function sentences(text) {
    var out = [], parts = String(text || '').split(/\n+/);
    for (var i = 0; i < parts.length; i++) {
      var p = parts[i], at = 0;
      for (var j = 0; j < p.length; j++) {
        var ch = p.charAt(j), next = p.charAt(j + 1);
        if ((ch === '.' || ch === '!' || ch === '?' || ch === ';') && (next === ' ' || next === '')) { push(p.slice(at, j + 1)); at = j + 1; }
      }
      push(p.slice(at));
    }
    function push(s) { s = s.trim().replace(/[;:,]+$/, ''); if (s) out.push(cap(s)); }
    return out;
  }
  // at most LINES lines, each under LINE_MAX characters (cut at a word); more = something is left for "What changed"
  function splitNotes(text) {
    var all = sentences(text), lines = [], more = all.length > LINES;
    for (var i = 0; i < all.length && lines.length < LINES; i++) {
      var s = all[i].replace(/\.$/, '');
      if (s.length >= LINE_MAX) { var cut = s.lastIndexOf(' ', LINE_MAX - 2); s = s.slice(0, cut > 20 ? cut : LINE_MAX - 2).replace(/[,;:]$/, '') + '…'; more = true; }
      lines.push(s);
    }
    return { lines: lines, more: more, all: all };
  }
  function size(bytes) { if (!(bytes > 0)) return ''; return bytes < 1e6 ? (bytes / 1e6).toFixed(1) + ' MB' : Math.round(bytes / 1e6) + ' MB'; }
  function card(stage, u, over) {
    var ver = u.version || '';
    var m = { stage: stage, version: ver, key: 'update:' + ver + ':' + (stage === 'available' || stage === 'downloading' || stage === 'staging' ? 'pending' : stage), name: NAME + ' ' + ver, line: '', note: '', cause: '', size: size(u.size), pct: -1, ring: 'none', actions: [], dismissable: true, auto: !!u.auto };
    var n = splitNotes(u.notes); m.lines = n.lines; m.more = n.more; m.all = n.all;
    if (over) for (var k in over) m[k] = over[k];
    return m;
  }
  // u = state.update; s = { version, quitting }
  function model(u, s) {
    if (!u || !u.version) return null;
    s = s || {};
    var pct = u.progress > 0 ? Math.min(100, Math.round(u.progress * 100)) : 0;
    if (u.status === 'error') {
      if (/no update manifest configured/.test(u.error || '')) return null;
      return card('failed', u, { line: u.rolled_back ? 'did not stay up and was rolled back.' : 'did not install.', cause: firstLine(u.error, 120), ring: 'failed', actions: [RETRY, LATER], lines: [], more: false });
    }
    if (u.applying || u.status === 'applying' || (u.status === 'ready' && u.wait === 'installing now')) {
      return card('installing', u, { line: 'Installing. The app restarts itself.', note: s.quitting ? 'A moment.' : 'Your key stays where it is.', ring: 'busy', dismissable: false, lines: [], more: false });
    }
    var m = null;
    switch (u.status) {
      case 'available': m = card('available', u, { line: 'is available.', actions: [INSTALL, LATER] }); break;
      case 'downloading': m = card('downloading', u, { line: 'is downloading.', pct: pct, ring: 'progress', actions: [INSTALL, LATER] }); break;
      case 'staging': m = card('staging', u, { line: 'is being checked.', pct: 100, ring: 'progress', actions: [INSTALL, LATER] }); break;
      case 'ready':
        var why = /failed to install before/.test(u.wait || '') ? 'It failed to install before.' : u.auto ? 'It installs by itself when nothing is being sent.' : '';
        m = card('ready', u, { line: 'is ready to install.', note: why, ring: 'full', actions: [INSTALL, LATER] }); break;
      case 'deferred': m = card('deferred', u, { line: 'is waiting for permission.', note: 'It installs the next time someone is at this PC.', ring: 'full', actions: [INSTALL, LATER] }); break;
      case 'manual': m = card('manual', u, { line: 'is downloaded.', note: 'Open the disk image and drag the app over the old one.', ring: 'full', actions: [OPEN, LATER] }); break;
    }
    if (!m) return null;
    if (u.urgent && u.urgent_text) { m.note = u.urgent_text; m.dismissable = false; m.actions = m.actions.filter(function (a) { return a.act !== 'later'; }); }
    return m;
  }
  // why the card may not open now: ctx = { phase, view, bioBusy, bioLine, bioName, quitting }
  function blocked(ctx) {
    ctx = ctx || {};
    if (ctx.quitting) return 'the app is quitting';
    if (ctx.phase === 'create' || ctx.phase === 'import') return 'a wallet is being ' + (ctx.phase === 'create' ? 'created' : 'imported');
    if (ctx.view === 'send') return 'the send screen is open';
    if (ctx.bioBusy || ctx.bioLine) return (ctx.bioName || 'Touch ID') + ' is on screen';
    return '';
  }
  // mem = { open: the card is up, later: the key Later was pressed on, seen: the version the card was shown for }
  function decide(m, ctx, mem) {
    mem = mem || {};
    if (!m) return { show: false, why: 'none' };
    var b = blocked(ctx);
    if (b && m.stage !== 'installing') return { show: false, why: 'deferred', reason: b };
    if (m.stage === 'installing' || m.stage === 'failed') return mem.open ? { show: true, why: 'open' } : { show: false, why: 'strip' };
    if (!m.dismissable) return { show: true, why: 'urgent' };
    if (mem.later === m.key) return { show: false, why: 'later' };
    if (mem.open) return { show: true, why: 'open' };
    if (m.stage === 'ready' && m.auto && mem.seen === m.version) return { show: false, why: 'auto' };
    return { show: true, why: m.stage === 'ready' ? 'ready' : 'new' };
  }
  return { NAME: NAME, LINE_MAX: LINE_MAX, LINES: LINES, sentences: sentences, splitNotes: splitNotes, size: size, model: model, blocked: blocked, decide: decide };
})();
if (typeof module === 'object' && module && module.exports) module.exports = UpdateCard;
