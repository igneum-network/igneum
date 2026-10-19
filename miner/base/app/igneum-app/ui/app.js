var CAPTURE = false; // ?capture=1: the site's capture mode (no machine name, no file path); set when the page reads its query
/* Ember dashboard (miner-ui-3). Talks to the engine on the same origin: api/state every second, api/log while
   the drawer is open. Screens: welcome, cards, address (plus the one-time key sheet), then the dashboard with four
   pages behind a rail (Mine, Earnings, Prove, Settings). ?screen=<name> forces a screen, ?page=<name> a page, and
   ?update=, ?job=, ?tune= show sample states (screenshots). Everything is relative to the token path the page was
   opened on. The pure blocks (Notices, UpdateCard, View, TuneLine) run under node for the tests. */

var Notices = (function () {
  'use strict';
  var LEVEL = { 'update-installing': 0, 'update-urgent': 0, 'update-failed': 0, 'job-failed': 1, 'engine-bad': 1, 'engine-warn': 3, clock: 2, 'job-running': 3, tuning: 3, 'update-available': 4, 'job-done': 5, 'card-added': 5, 'card-removed': 5, 'update-installed': 6 };
  var JOB_DONE_S = 300, INSTALLED_S = 60, CARD_S = 300;
  function cap(t) { return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''; }
  function firstLine(t, max) { t = String(t || '').split('\n')[0].trim(); max = max || 160; return t.length > max ? t.slice(0, max - 1).trim() + '\u2026' : t; }
  function endDot(t) { t = (t || '').trim(); return !t || /[.!?]$/.test(t) ? t : t + '.'; }
  function notice(kind, key, text, extra) {
    var n = { kind: kind, level: LEVEL[kind], key: key, text: text, tone: '', detail: '', actions: [], progress: -1, title: '' };
    if (extra) for (var k in extra) n[k] = extra[k];
    return n;
  }
  var INSTALL = { act: 'install', label: 'Install now', primary: true }, LATER = { act: 'close', label: 'Later' };
  var RETRY = { act: 'retry', label: 'Try again', primary: true }, OPEN = { act: 'open', label: 'Open the download', primary: true };

  // u = state.update; s = { version, uptime_s, quitting }
  function updateNotice(u, s) {
    if (!u) return null;
    s = s || {};
    var ver = u.version || '', v = 'Ember ' + ver, pct = u.progress > 0 ? Math.round(u.progress * 100) : 0;
    if (u.status === 'retrying') {
      // the check could not be made (the store dark, no network): not a failed update; tried again on its own
      var mins = Math.max(1, Math.round((u.retry_s || 600) / 60));
      return notice('update-retry', 'update:retry', 'Could not reach the update server; checking again in ' + mins + ' minutes.', { tone: 'warn', detail: firstLine(u.error), title: u.error || '' });
    }
    if (u.status === 'error' && /running from its disk image/.test(u.error || '')) {
      // the engine runs from the mounted disk image: the plain words, the copy to Applications is the hand
      return notice('update-disk-image', 'update:disk-image', u.error, { tone: 'warn', title: u.error });
    }
    if (u.status === 'error') {
      if (/no update manifest configured/.test(u.error || '')) return null; // a build without a manifest: nothing to try, Settings says so
      // no manifest published yet (a fresh release before its update pointer): nothing failed and nothing to try;
      // the first screen of the mini's 0.2.1 install (12 October 2026) carried "The update failed. Try again" for it
      if (/no manifest at the update URL yet/.test(u.error || '')) return null;
      var text = u.rolled_back ? (v + ' did not stay up and was rolled back.') : ('The update' + (ver ? ' to ' + ver : '') + ' failed.');
      return notice('update-failed', 'update:error:' + ver, text, { tone: 'bad', detail: firstLine(u.error), actions: [RETRY], title: u.error || '' });
    }
    if (u.applying || u.status === 'applying' || (u.status === 'ready' && u.wait === 'installing now')) {
      var t = s.quitting ? 'Installing ' + v + '. The app restarts itself in a moment.' : 'Installing ' + v + '. The app restarts itself. Mining continues until then.';
      return notice('update-installing', 'update:applying:' + ver, t, { title: u.wait || '' });
    }
    if (u.urgent && u.urgent_text) {
      var dl = u.status === 'downloading';
      return notice('update-urgent', 'update:urgent:' + ver, u.urgent_text + (dl ? ' Downloading' + (pct ? ': ' + pct + '%' : '') + '.' : ''), { tone: 'bad', progress: dl ? (u.progress || 0) : -1 });
    }
    switch (u.status) {
      case 'available': return notice('update-available', 'update:' + ver + ':pending', v + ' is available.', { actions: [INSTALL, LATER] });
      case 'downloading': return notice('update-available', 'update:' + ver + ':pending', 'Downloading ' + v + (pct ? ': ' + pct + '%' : '') + '.', { progress: u.progress || 0, actions: [INSTALL, LATER], title: u.size ? Math.round(u.size / 1e6) + ' MB' : '' });
      case 'staging': return notice('update-available', 'update:' + ver + ':pending', 'Checking ' + v + '.', { actions: [LATER] });
      case 'ready':
        var why = /failed to install before/.test(u.wait || '') ? ' It failed to install before.' : u.auto ? ' It installs by itself at a quiet moment.' : '';
        return notice('update-available', 'update:' + ver + ':ready', v + ' is ready.' + why, { actions: [INSTALL, LATER], title: u.wait || '' });
      case 'deferred': return notice('update-available', 'update:' + ver + ':deferred', v + ' is waiting for permission. It installs the next time someone is at this PC. Mining continues.', { actions: [INSTALL, LATER] });
      case 'manual': return notice('update-available', 'update:' + ver + ':manual', v + ' is downloaded. Open it and drag the app over the old one.', { actions: [OPEN, LATER], title: u.wait || '' });
    }
    if (u.updated_from && s.uptime_s >= 0 && s.uptime_s < INSTALLED_S) {
      var now = s.version || ver;
      var at = s.now > 0 && s.uptime_s >= 0 ? ' at ' + hhmm(s.now - s.uptime_s) : '';
      return notice('update-installed', 'update:installed:' + now, 'Updated to ' + now + at + '.', { title: 'Ember ' + now + ', from ' + u.updated_from });
    }
    return null;
  }

  function jobTitle(j) { return j.title && j.title !== j.kind ? j.title : (j.kind === 'shard-benchmark' ? 'shard benchmark' : j.kind === 'build' ? 'build (mining continues)' : j.kind); }
  // the job in a user's words (miner-ui-3 polish, 6 October 2026): from its kind, else from its id's prefix
  function jobWord(kind, id) {
    var k = String(kind || ''), i = String(id || '');
    var t = k || i;
    if (/^update-now/.test(t) || /^update-now/.test(i)) return 'update check';
    if (/^restart/.test(t) || /^restart/.test(i)) return 'restart';
    if (/^collect/.test(t) || /^collect/.test(i)) return 'log collection';
    if (/shard-benchmark|^shard/.test(t) || /shard/.test(i)) return 'shard benchmark';
    if (/^build/.test(t) || /^build/.test(i)) return 'build';
    if (/ember|tune|sweep/.test(t) || /ember|tune|sweep/.test(i)) return 'tuning check';
    if (/script|playbook|run/.test(t)) return 'a script';
    return k || 'a task';
  }
  function hhmm(t) { var d = new Date(t * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0'); }
  function firstErrorLine(results, summary) {
    for (var i = 0; i < results.length; i++) if (/BUILD FAILED|error|failed|panic/i.test(results[i])) return firstLine(results[i]);
    return firstLine(summary);
  }
  // j = state.jobs; now = state.now (unix seconds)
  function jobNotice(j, now) {
    if (!j) return null;
    if (j.active) {
      var m = Math.max(0, Math.floor((now - j.started_at) / 60));
      var results = (j.results || []).filter(function (x) { return x.indexOf('RESULT') === 0; });
      var tech = [jobTitle(j), results.length ? results[results.length - 1] : ''].filter(Boolean).join(' · ');
      return notice('job-running', 'job:run:' + j.id, 'A job from the team is running: ' + jobWord(j.kind, j.id) + ', ' + m + ' min.' + (j.stage ? ' ' + endDot(cap(j.stage)) : ''), { detail: tech, title: j.message || '' });
    }
    var l = j.last;
    if (!l || !l.id) return null;
    var mins = Math.max(0, Math.round((l.finished_at - l.started_at) / 60)), t = l.title || l.kind;
    var report = l.uploaded ? '' : ' The report did not upload.', word = jobWord(l.kind, l.id), tech = t + (l.summary ? ': ' + l.summary : '');
    if (l.status === 'done') {
      if (l.finished_at && now - l.finished_at > JOB_DONE_S) return null;
      return notice('job-done', 'job:done:' + l.id, 'A job from the team ran: ' + word + ', ' + mins + ' min.' + report, { detail: tech, title: l.summary || '' });
    }
    var verb = l.status === 'timeout' ? 'ran out of time' : l.status === 'aborted' ? 'was stopped' : 'failed';
    var cause = firstErrorLine(j.last_results || [], l.summary);
    return notice('job-failed', 'job:failed:' + l.id, 'A job from the team ' + verb + ': ' + word + ', after ' + mins + ' min.' + report, { tone: 'bad', detail: [tech, cause !== l.summary ? cause : ''].filter(Boolean).join(' · ') + ' · exit ' + l.exit, title: l.summary || '' });
  }

  // c = state.clock: the engine's words, Sync clock, the manual hint underneath
  function clockNotice(c) {
    if (!c || !(c.severity === 'block' || c.severity === 'warn')) return null;
    return notice('clock', 'clock:' + c.severity, c.message || '', { tone: c.severity === 'block' ? 'bad' : 'warn', detail: c.hint || '', actions: [{ act: 'sync', label: c.syncing ? 'Syncing' : 'Sync clock', primary: true, disabled: !!c.syncing }] });
  }

  // cards = state.mining.cards; now = state.now: every card a re-detection added or lost in the last 5 minutes
  // (added_at / removed_at are 0 for the cards found at start). The newest first, so pick() shows it.
  function kindWord(k) { return k === 'apple' ? 'Apple silicon' : k === 'discrete' ? 'Discrete' : k === 'integrated' ? 'Integrated' : k === 'external' ? 'eGPU' : 'Unknown'; }
  // F-E50 (main's read of 14 October 2026, 21:3x UK: a card pinned to a coordinate the pool does not serve left the
  // Mine page reading "Not mining" with no reason): the engine's newest notice of weight (bad or warn) as a strip
  // line, its text as the words and its detail behind "the technical line"; the key is the text, so a closed one
  // stays closed until the words change
  function engineNotice(notices) {
    var list = notices || [], n = null;
    for (var i = list.length - 1; i >= 0; i--) { if (list[i] && (list[i].level === 'bad' || list[i].level === 'warn')) { n = list[i]; break; } }
    if (!n) return null;
    var kind = n.level === 'bad' ? 'engine-bad' : 'engine-warn';
    return notice(kind, 'engine:' + n.text, endDot(firstLine(n.text, 200)), { tone: n.level === 'bad' ? 'bad' : 'warn', detail: firstLine(n.detail, 400) });
  }
  function cardNotices(cards, now) {
    var out = [];
    (cards || []).forEach(function (c) {
      if (c.removed_at > 0) {
        if (now - c.removed_at <= CARD_S) out.push(notice('card-removed', 'card:removed:' + c.key + ':' + Math.floor(c.removed_at), 'Card removed: ' + c.name + '. Its worker stopped.', { tone: 'warn', at: c.removed_at }));
        return;
      }
      if (!(c.added_at > 0) || now - c.added_at > CARD_S) return;
      // a new card with a problem, or a card that was mining and now reports one (both carry added_at): the row's words
      if (c.problem) out.push(notice('card-added', 'card:added:' + c.key + ':' + Math.floor(c.added_at), c.name + ': not usable (' + c.problem + '). No worker runs on it.', { tone: 'warn', detail: c.reason || '', at: c.added_at }));
      else if (c.enabled) out.push(notice('card-added', 'card:added:' + c.key + ':' + Math.floor(c.added_at), 'New card: ' + c.name + ', mining.', { at: c.added_at }));
      else out.push(notice('card-added', 'card:added:' + c.key + ':' + Math.floor(c.added_at), 'New card: ' + c.name + ', off (' + kindWord(c.kind).toLowerCase() + '). Settings switches it on.', { at: c.added_at }));
    });
    out.sort(function (a, b) { return b.at - a.at; });
    return out;
  }

  // every notice the state carries for this screen; pick() chooses the one to show
  function gather(s, where) {
    where = where || {};
    var out = [], u = updateNotice(s.update, s);
    if (u) out.push(u);
    if (where.dashboard) { var j = jobNotice(s.jobs, s.now || 0); if (j) out.push(j); }
    else { var c = clockNotice(s.clock); if (c) out.push(c); }
    if (s.mining && s.mining.cards) out = out.concat(cardNotices(s.mining.cards, s.now || 0));
    var en = engineNotice(s.mining && s.mining.notices);
    if (en) out.push(en);
    var tn = tuneNotice(s.mining && s.mining.cards);
    if (tn) out.push(tn);
    return out;
  }
  // the one line while a card is being tuned: which card, which plan, the step, when mining is back
  function tuneNotice(cards) {
    var l = (cards || []).filter(function (c) { return c.state === 'tuning'; });
    if (!l.length) return null;
    var c = l[0], plan = c.tune_plan === 'confirm' ? 'confirming the fleet prior' : c.tune_plan === 'baseline' ? 'measuring as it runs' : 'the full tune';
    var step = c.tune_steps > 0 ? ', step ' + (c.tune_step || 1) + ' of ' + c.tune_steps : '';
    var eta = c.tune_eta_s > 0 ? ' Mining again in ' + (c.tune_eta_s < 60 ? 'under a minute' : 'about ' + Math.round(c.tune_eta_s / 60) + ' min') + '.' : '';
    return notice('tuning', 'tuning:' + c.key, 'Tuning ' + c.name + ': ' + plan + step + '.' + eta, { tone: 'info', title: 'Ember Tune' });
  }
  // the lowest level wins; a dismissed key is skipped, so the next one shows in its place
  function pick(list, dismissed) {
    var best = null;
    for (var i = 0; i < list.length; i++) {
      var n = list[i];
      if (!n || (dismissed && dismissed[n.key])) continue;
      if (!best || n.level < best.level) best = n;
    }
    return best;
  }
  return { engineNotice: engineNotice, tuneNotice: tuneNotice, jobWord: jobWord, hhmm: hhmm, LEVEL: LEVEL, JOB_DONE_S: JOB_DONE_S, INSTALLED_S: INSTALLED_S, CARD_S: CARD_S, updateNotice: updateNotice, jobNotice: jobNotice, clockNotice: clockNotice, cardNotices: cardNotices, gather: gather, pick: pick, jobTitle: jobTitle, cap: cap, endDot: endDot, kindWord: kindWord };
})();

/* ---------- the update card (pure; update-card.test.mjs loads this block) ----------
   A centred card over the window for one update: the mark with a progress ring, "Ember 0.1.14", one line
   (is available, is downloading, is ready to install, Installing, did not install), up to three lines of release
   notes with the rest behind "What changed", the size, Install now and Later. The same card, the same rules and the
   same words sit in the wallet (app/igneum-wallet/ui/update-card.js).
   model() turns state.update into what the card says. decide() says whether it shows now, given the window
   (ctx) and what the card already did (mem: open, later, seen):
     deferred  while a job runs, in the engine's first 60 s, while the key sheet is up, while the app quits
     later     Later, Escape or the backdrop hides this version at this stage; the strip (Notices) keeps it
     back      a newer version, or the download ready while automatic updates are off (with them on it installs
               by itself, so a dismissed download stays dismissed); an open card follows its update to the end
     installing and failed show only on an open card (Install now was pressed here); the strip carries the rest */
var UpdateCard = (function () {
  'use strict';
  var NAME = 'Ember', LINE_MAX = 70, LINES = 3, FIRST_S = 60;
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
  // u = state.update; s = { version, uptime_s, quitting }
  function model(u, s) {
    if (!u || !u.version) return null;
    s = s || {};
    var pct = u.progress > 0 ? Math.min(100, Math.round(u.progress * 100)) : 0;
    if (u.status === 'retrying') {
      return card('retrying', u, { line: 'The update server could not be reached; checking again in ' + Math.max(1, Math.round((u.retry_s || 600) / 60)) + ' minutes.', cause: firstLine(u.error, 120), ring: 'busy', actions: [RETRY], lines: [] });
    }
    if (u.status === 'error' && /running from its disk image/.test(u.error || '')) {
      return card('disk-image', u, { line: u.error, ring: 'failed', actions: [], lines: [] });
    }
    if (u.status === 'error') {
      if (/no update manifest configured/.test(u.error || '')) return null;
      if (/no manifest at the update URL yet/.test(u.error || '')) return null; // nothing published yet: no card
      return card('failed', u, { line: u.rolled_back ? 'did not stay up and was rolled back.' : 'did not install.', cause: firstLine(u.error, 120), ring: 'failed', actions: [RETRY, LATER], lines: [], more: false });
    }
    if (u.applying || u.status === 'applying' || (u.status === 'ready' && u.wait === 'installing now')) {
      return card('installing', u, { line: 'Installing. The app restarts itself.', note: s.quitting ? 'A moment.' : 'Mining continues until then.', ring: 'busy', dismissable: false, lines: [], more: false });
    }
    var m = null;
    switch (u.status) {
      case 'available': m = card('available', u, { line: 'is available.', actions: [INSTALL, LATER] }); break;
      case 'downloading': m = card('downloading', u, { line: 'is downloading.', pct: pct, ring: 'progress', actions: [INSTALL, LATER] }); break;
      case 'staging': m = card('staging', u, { line: 'is being checked.', pct: 100, ring: 'progress', actions: [INSTALL, LATER] }); break;
      case 'ready':
        var why = /failed to install before/.test(u.wait || '') ? 'It failed to install before.' : u.auto ? 'It installs by itself at a quiet moment.' : '';
        m = card('ready', u, { line: 'is ready to install.', note: why, ring: 'full', actions: [INSTALL, LATER] }); break;
      case 'deferred': m = card('deferred', u, { line: 'is waiting for permission.', note: 'It installs the next time someone is at this PC. Mining continues.', ring: 'full', actions: [INSTALL, LATER] }); break;
      case 'manual': m = card('manual', u, { line: 'is downloaded.', note: 'Open the disk image and drag the app over the old one.', ring: 'full', actions: [OPEN, LATER] }); break;
    }
    if (!m) return null;
    if (u.urgent && u.urgent_text) { m.note = u.urgent_text; m.dismissable = false; m.actions = m.actions.filter(function (a) { return a.act !== 'later'; }); }
    return m;
  }
  // why the card may not open now: ctx = { jobActive, uptime_s, quitting, sheetOpen }
  function blocked(ctx) {
    ctx = ctx || {};
    if (ctx.quitting) return 'the app is quitting';
    if (ctx.sheetOpen) return 'the key sheet is open';
    if (ctx.jobActive) return 'a job is running';
    if (ctx.uptime_s >= 0 && ctx.uptime_s < FIRST_S) return 'the engine started under a minute ago';
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
  return { NAME: NAME, LINE_MAX: LINE_MAX, LINES: LINES, FIRST_S: FIRST_S, sentences: sentences, splitNotes: splitNotes, size: size, model: model, blocked: blocked, decide: decide };
})();
/* ---------- the pages' words (pure; view.test.mjs loads this block) ----------
   What each section says for a given state: the card row (every state word, the numbers, the money, the tune line),
   the big start/stop button, the node line, the prove tier sentence per card, the goal's consequence, the dev-fee
   line, the confirmations' words. No DOM, no time of day: the caller passes `now`. The sections (miner-ui-3): Mine,
   Earnings, Prove, Settings. */
var View = (function () {
  'use strict';
  var BLOCK_S = 12; // the chain's block interval in seconds (the founder's word, 16 October 2026): every figure made from a block count uses it
  // app-ia-26 (the founder's audit, 8 October 2026): four pages, one job each. Mine: what is happening now. Tune: how each
  // card is driven. Earnings: what this machine earned, measured. Settings: everything you set once (proving included).
  var PAGES = [
    { id: 'mine', title: 'Mine', sub: 'What this machine is doing now' },
    { id: 'cards', title: 'Cards', sub: 'Every card, its state and its switch' }, // v0.2.1: the founder's call, the Cards tab back as its own page
    { id: 'tune', title: 'Tune', sub: 'How Ember drives each card' },
    { id: 'fleet', title: 'Fleet', sub: 'Every rig of yours, through your console' }, // 0.2.8: shown only with a console URL set
    { id: 'earnings', title: 'Earnings', sub: 'What this machine earned' },
    { id: 'pools', title: 'Pools', sub: 'Where this machine sends its work' },
    { id: 'settings', title: 'Settings', sub: 'Set once, left alone' }
  ];
  // the Live Work Ledger's miner view (the settlement seat's form, 12 October 2026; served by reliability from
  // build.igneum.network every minute, the chain's figures only): per address, every figure with its source named
  // and nothing estimated; a figure moves only when its source file does
  var LEDGER_URL = 'https://build.igneum.network/ledger/miner-view/';
  // the sealers' refusals by reason (main's audit of 22:1x UK, 13 October 2026): the node's reason strings fold into
  // five short words for the page's face; the raw strings go behind a details disclosure with every host address and
  // port blanked (an IP is never shown)
  var REFUSAL_WORDS = [[/carried twice/i, 'carried twice'], [/stale parent|not the head|recency window/i, 'stale parent'], [/address at cap|holds more unpaid|at cap/i, 'address at cap'], [/does not verify|invalid mix|invalid/i, 'work does not verify'], [/rpc|dial tcp|connection|deadline|timeout|unreachable|EOF/i, 'node unreachable']];
  function refusalSummary(by) {
    var counts = {}, raw = [];
    Object.keys(by).forEach(function (k) {
      var n = Number(by[k]) || 0, word = 'other';
      for (var i = 0; i < REFUSAL_WORDS.length; i++) if (REFUSAL_WORDS[i][0].test(k)) { word = REFUSAL_WORDS[i][1]; break; }
      counts[word] = (counts[word] || 0) + n;
      raw.push(withCommas(n) + ' ' + k.replace(/^igneum shares: /, '').replace(/https?:\/\/[^\s"\/]+/g, 'http://[a node]').replace(/\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b/g, '[a node]').replace(/\[a node\]->\[a node\]/g, '[a node]'));
    });
    var order = ['carried twice', 'stale parent', 'address at cap', 'work does not verify', 'node unreachable', 'other'];
    return { list: order.filter(function (w) { return counts[w]; }).map(function (w) { return { word: w, n: counts[w] }; }), details: raw };
  }
  function ledgerWords(d, s) {
    s = s || {}; var a = (s.address || {}).display || '', m = s.mining || {}, df = s.dev_fee || {};
    var lc = d && d.chain_id ? chainWord(d.chain_id, d.network) : 'the chain';
    // main's F-E4 (13 October 2026): the ledger and this panel follow the pool's chain; a miner view of another chain
    // shows none of its payments, only that no ledger exists yet for the mined chain
    var mined = Number(((s.source || {}).chain_id) || 0);
    if (d && d.chain_id && mined > 0 && Number(d.chain_id) !== mined) {
      var why4 = 'no ledger yet for chain ' + mined + ': the ledger’s miner view names ' + lc + ', not the chain this machine mines; its payments are not shown';
      return { address: a, asof: '', rows: [{ id: 'pool', k: 'shares the pool accepted', v: m.accepted_session >= 0 ? withCommas(m.accepted_session) + ' this session' : 'none yet', s: 'the pool’s statement to this machine' }, { id: 'paid', k: 'paid on chain ' + mined, v: 'no ledger yet', s: why4 }], line: why4, chain: 'chain ' + mined };
    }
    var rows = [];
    var rc = df.receipts_total || 0, rv = df.receipts_v1_verified || 0;
    rows.push({ id: 'receipts', k: 'receipts verified', v: rc ? withCommas(rv) + ' of ' + withCommas(rc) : 'none yet', s: 'this machine, from the pool’s signed receipts' + (df.receipts_file && !(typeof CAPTURE !== 'undefined' && CAPTURE) ? ' (' + df.receipts_file + ')' : '') });
    if (!d || !d.schema) {
      var why = d && d.error ? d.error : 'the ledger’s miner view is not read yet';
      rows.push({ id: 'pool', k: 'shares the pool accepted', v: m.accepted_session >= 0 ? withCommas(m.accepted_session) + ' this session' : 'none yet', s: 'the pool’s statement to this machine' });
      rows.push({ id: 'paid', k: 'paid on ' + lc, v: 'not read', s: 'the ledger’s paid and in-carry columns: ' + why });
      return { address: a, asof: '', rows: rows, line: why };
    }
    var pc = d.pool_claims || {}, na = pc.node_answers || {}, t = d.totals || {};
    var paid = d.paid_wei ? Number(BigInt(d.paid_wei) / 1000000000000n) / 1e6 : 0;
    var rb = refusalSummary(na.refused_by || {}), refusedBy = rb.list.map(function (x) { return withCommas(x.n) + ' ' + x.word; }).join(', ');
    rows.push({ id: 'pool', k: 'shares the pool accepted', v: withCommas(pc.pool_accepted || 0), s: 'the pool’s statement, from its journal' });
    rows.push({ id: 'sealers', k: 'the sealers’ answers', v: withCommas(na.accepted || 0) + ' accepted, ' + withCommas(na.refused || 0) + ' refused', s: refusedBy ? 'refused: ' + refusedBy : 'no refusal named', details: rb.details });
    rows.push({ id: 'paid', k: 'paid on ' + lc, v: fmtIgn(paid) + ' in ' + withCommas((d.blocks || []).length) + ' block' + ((d.blocks || []).length === 1 ? '' : 's'), s: lc + '’s balance steps at this address; ' + withCommas(t.paid || 0) + ' shares paid' });
    rows.push({ id: 'carry', k: 'in the carry', v: withCommas(t.in_carry || 0), s: 'accepted by a sealer, not yet in a block' });
    rows.push({ id: 'expired', k: 'expired by name', v: withCommas(t.expired_reorg || 0) + ' by a reorganisation, ' + withCommas(t.expired_window || 0) + ' at the window', s: 'shares the chain will not pay' });
    rows.push({ id: 'open', k: 'open within the recency', v: withCommas(pc.unjoined_within_window || 0), s: 'the pool’s claims not yet joined to a sealer’s answer' });
    var readUnix = d.read_at ? Math.floor(Date.parse(d.read_at) / 1000) : 0, asof = d.read_at ? 'as of ' + (readUnix > 0 ? clockWords(readUnix) : d.read_at) + ', ' + (d.chain_id ? lc + ' ' : '') + 'head ' + withCommas(d.head || 0) : '';
    return { address: a, asof: asof, rows: rows, line: '', chain: lc };
  }
  var DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  var SHARD_GB = 20.4, PROVE_GB = 24, PROVE_MIN_GB = 16;   // PROVE_MIN_GB: main's rule, 7 October 2026 (src/provedefault.rs MIN_VRAM_MB_PROVE_ANY)
  function page(id) { for (var i = 0; i < PAGES.length; i++) if (PAGES[i].id === id) return PAGES[i]; return PAGES[0]; }
  function withCommas(n) { return String(Math.round(n || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
  function compact(n) { if (n === null || n === undefined || isNaN(n)) return '0'; var a = Math.abs(n); if (a >= 1e12) return (n / 1e12).toFixed(2) + 'T'; if (a >= 1e9) return (n / 1e9).toFixed(2) + 'G'; if (a >= 1e6) return (n / 1e6).toFixed(2) + 'M'; if (a >= 1e4) return (n / 1e3).toFixed(1) + 'k'; return String(Math.round(n)); }
  function rel(s) { s = Math.max(0, Math.round(s)); if (s < 60) return s + ' s ago'; var m = Math.floor(s / 60); if (m < 60) return m + ' min ago'; var h = Math.floor(m / 60); if (h < 24) return h + ' h ' + (m % 60) + ' min ago'; return Math.floor(h / 24) + ' d ago'; }
  // a coarser "ago" for a line that is read, not measured: "just now", "12 min ago", "2 h ago", "3 d ago"
  function ago(s) { s = Math.max(0, Math.round(s)); if (s < 60) return 'just now'; if (s < 3600) return Math.floor(s / 60) + ' min ago'; if (s < 86400) return Math.floor(s / 3600) + ' h ago'; return Math.floor(s / 86400) + ' d ago'; }
  function shortHex(h, head, tail) { h = String(h || ''); head = head || 8; tail = tail || 6; return h.length > head + tail + 2 ? h.slice(0, head) + '…' + h.slice(-tail) : h; }
  function cap(t) { return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''; }
  var kindWord = Notices.kindWord;
  // hot-plug (src/hotplug.rs): a removed card's row hides after five minutes (gone); a faulty one has no switch
  // The list is ordered by performance (the founder, 6 October 2026): usable cards first, then by the measured rate since the
  // start (5 MH/s buckets so the order does not flicker), then discrete, external and Apple before integrated, then
  // memory; removed and unusable cards last. Ties keep the detection order.
  function perfRank(c) {
    var usable = !(c.removed_at > 0) && !c.problem ? 0 : 1;
    var integrated = c.kind === 'integrated' ? 1 : 0;
    var bucket = -Math.floor(((c.enabled ? (c.hash_avg || c.hash_now || 0) : 0)) / 5);
    return [usable, integrated, bucket, -(c.vram_mb || 0)];
  }
  function shownCards(cards) {
    return (cards || []).map(function (c, i) { return { c: c, i: i, r: perfRank(c) }; }).sort(function (a, b) {
      for (var k = 0; k < a.r.length; k++) { if (a.r[k] !== b.r[k]) return a.r[k] - b.r[k]; }
      return a.i - b.i;
    }).map(function (x) { return x.c; }).filter(function (c) { return !c.gone; });
  }
  // listed, driver fine, not unplugged: a worker can run on it
  function present(c) { return !(c.removed_at > 0) && !c.problem; }
  // the tooltip on a card's name: what the tool calls it (gfx1201), its device index, the OpenCL platform, the PCI address
  function cardTitle(cd) { var t = []; if (cd.code && cd.code !== cd.name) t.push(cd.code); if (cd.device !== undefined && cd.device !== '') t.push(cd.worker + ' device ' + cd.device); if (cd.platform) t.push(cd.platform); if (cd.bus) t.push('bus ' + cd.bus); return t.join(' · '); }

  // ---------- the network step (Igneum 2.0, 8 October 2026) ----------
  // One network, pinned in the engine (docs/product-a/OLD-BASE.md step 3): the pinned chain (0.2.1: Igneum devnet9, chain id 7319). The card is the
  // fresh-install default and the only choice; the work comes from the source the user names (a node by URL or a pool
  // by host:port), never from a node inside this app.
  var NETWORKS = [
    { id: 'chain-7322', label: 'the public test network', name: 'chain 7322', sub: 'it may reset, coins have no value' } // the naming rule of 15 October 2026: never the feed's internal name on a page; the card's id is the chain id (F-522), an older stored id reads as the one network
  ];
  // the work source as the engine holds it (settings.source_kind: "node" | "pool" | ""; settings.source_url): the words
  // for the Node card's source line
  // the project's pool is the default work source (Ember 0.2.1, the founder's order of 11 October 2026): a user who
  // names nothing mines through it; a node of their own is the alternative
  var DEFAULT_POOL = 'pool.igneum.network:3342 notls';
  // the public test network's RPC for the solo path (the chains file's 7322 entry: its own public host from step one of the
  // swap); never a mesh or tunnel address
  var PUBLIC_RPC = 'https://rpc.igneum.network';
  // the founder's word of 12 October 2026: the test network's coins are development-network tokens
  var NO_VALUE_LABEL = 'development-network tokens; no monetary value';
  function sourceWords(s) {
    var st = (s && s.settings) || {}, kind = st.source_kind || '', url = st.source_url || '';
    // the relation mined, as the served document names it (the founder's order of 14:17 UK, 13 October 2026)
    var rel = (s && s.source && s.source.relation) ? ' The work is ' + s.source.relation + (s.source.chain_id > 0 ? ' on chain ' + s.source.chain_id : '') + '.' : '';
    if (kind === 'pool' && url) return { kind: kind, url: url, line: 'Mining through the pool ' + url + '.' + rel, set: true };
    if (kind === 'node' && url) return { kind: kind, url: url, line: 'Mining solo through the node at ' + url + '.' + rel, set: true };
    return { kind: 'pool', url: DEFAULT_POOL, line: 'Mining through the project’s pool ' + DEFAULT_POOL.replace(' notls', '') + ' (the default). Name a pool (host:port) or a node of your own (URL; the public test network’s is ' + PUBLIC_RPC + ') to change it.', set: false, isDefault: true };
  }
  // every balance, height and payout figure carries its chain's name as its source states it (main's read of the
  // 0.2.5 mini set, 13 October 2026: two chains shown without a name)
  // the chain naming rule (main, 15 October 2026, the founder's capture): a chain the page names reads by its public
  // words, "the public test network, chain 7320"; a feed's or a node's internal name never shows on a page
  // the naming rule of 15 October 2026: chain 7322 "the public test network", chain 7320 "the old public test network", chain 7358
  // "the research chain"; any other chain reads by its id alone
  var CHAIN_NAMES = { 7322: 'the public test network', 7320: 'the old public test network', 7358: 'the research chain' };
  function chainWord(id, name) { id = Number(id || 0); if (!(id > 0)) return 'the chain'; var w = CHAIN_NAMES[id]; return w ? w + ', chain ' + id : 'chain ' + id; }
  function networkIdOf(name) { for (var i = 0; i < NETWORKS.length; i++) if (NETWORKS[i].name === name) return NETWORKS[i].id; return NETWORKS[0].id; }
  function networkWords(s) {
    s = s || {}; var st = s.settings || {}, current = networkIdOf(s.network_name || ''), pending = s.network_pending || '';
    var selected = s.setup_done ? (pending || current) : (st.network || NETWORKS[0].id);
    if (!NETWORKS.some(function (n) { return n.id === selected; })) selected = NETWORKS[0].id; // an older stored id (F-522) reads as the one network
    var cards = NETWORKS.map(function (n) { return { id: n.id, label: n.label, name: n.name, line: n.name, sub: n.sub, open: true, selected: n.id === selected, current: !!s.setup_done && n.id === current }; });
    var pend = NETWORKS.filter(function (n) { return n.id === pending; })[0];
    var srcChain = Number((s.source || {}).chain_id || 0), nodeChain = Number(String(s.network_name || NETWORKS[0].name).replace(/^chain\s+/, '')) || 0;
    var line = pend ? 'Switching to ' + pend.label + ' (' + pend.name + ') at the next start: quit Ember and open it again.' : !s.setup_done ? '' : (srcChain > 0 && srcChain !== nodeChain) ? 'This machine mines chain ' + srcChain + ' through its work source; the built-in node runs ' + NETWORKS[0].label + ' (' + (s.network_name || NETWORKS[0].name) + ').' : 'This machine mines ' + NETWORKS[0].label + ' (' + (s.network_name || NETWORKS[0].name) + ').';
    return { cards: cards, selected: selected, current: current, pending: pending, line: line, error: s.network_error || '' };
  }
  function networkAsk(want, runningName) { var n = NETWORKS.filter(function (x) { return x.id === want; })[0]; return 'Switch this machine to ' + (n ? n.name : want) + '? The node’s data and the mining state on ' + runningName + ' reset at the next start. Your key and your address stay.'; }

  // review B F14 (8 October 2026): two builds from one tree; the window names the one it runs in
  function productName(edition) { return edition === 'lab' ? 'Ember Lab' : 'Ember'; }
  // review B F07: the engine's per-device mode (state.proving.modes[] {key, name, mode, line}); the window's own tier
  // sentence stands for an engine without the field
  function proveModeLine(cd, pv) {
    var m = ((pv && pv.modes) || []).filter(function (x) { return x.key === cd.key; })[0];
    return m && m.line ? m.line : proveTier(cd);
  }
  // review B F04 (the founder, 8 October 2026): a recovery lock is labelled "recovery" on every surface, never "final";
  // f.lock_kind comes from igneum_getFinalityCheckpoints' lockKind once the node carries it (the node lane); until then
  // the pause word stands. {recovery, word, why}
  function lockKindWords(f) {
    f = f || {};
    if (f.lock_kind === 'recovery') return { recovery: true, word: 'recovery lock', why: f.lock_why || 'the majority-continuity recovery after a long pause: a recovery lock is a lock the surviving majority took, not a certified checkpoint' };
    return { recovery: false, word: 'lock', why: '' };
  }
  // v0.2.1 (the founder, 8 October 2026): the Cards page's row carries the tune state and the 16 GB proving note beside the
  // state, the rate, the watts, the degrees and the switch. {tune, proving}
  function cardsRowWords(cd, s, now) {
    cd = cd || {}; var gb = cd.vram_mb ? Math.round(cd.vram_mb / 1024) : 0;
    return { tune: tunable(cd) ? tuneLine(cd, s, now) : '', proving: cd.vendor === 'nvidia' && gb > 0 && gb < PROVE_MIN_GB ? proveTier(cd) : '' };
  }
  // the four interface words (docs/spec/finality-guarantees.md section 9) for one block of the scene: included (blue) or
  // excluded (red) or pending; executed as chain block N; proven; finalised (under the lock, which holds even while the
  // network's finality is paused: the pause is the network's word, said after the block's own)
  function blockState(b, finalityPaused, lockKind) {
    b = b || {}; var bits = [b.color === 'blue' ? 'Included' : b.color === 'red' ? 'Excluded' : 'Pending'];
    if (b.chain && b.number >= 0 && isFinite(b.number)) bits.push('executed as chain block ' + withCommas(b.number));
    if (b.proven) bits.push('proven');
    if (b.final) bits.push(lockKind === 'recovery' ? 'finalised by a recovery lock' : 'finalised');
    return bits.join(' / ') + (!b.final && finalityPaused && b.color === 'blue' ? ' · finality paused on the network above the lock' : '');
  }

  // ---------- vendor marks (gpu-logos, 7 October 2026) ----------
  // One simplified monochrome glyph per GPU vendor, drawn by hand from the public mark's geometry (never a copied logo
  // file, never a raster): a nominative use that names the hardware. Each glyph is under 2 KB and takes currentColor,
  // so the per-vendor token tints it; the ember accent is reserved for state and never tints a brand. The light hex of
  // every vendor reads at 3:1 or better on its well (the vendor colour at WELL_ALPHA_LIGHT over the graphite card);
  // view.test.mjs computes the ratio and checks app.css carries the same hex values.
  var WELL_ALPHA_LIGHT = 0.10, WELL_ALPHA_DARK = 0.14;
  var VENDORS = {
    nvidia: { label: 'NVIDIA', dark: '#8BE37A', light: '#2F8A22' },
    amd: { label: 'AMD Radeon', dark: '#FF5A5A', light: '#C41E2A' },
    intel: { label: 'Intel', dark: '#7CC4FF', light: '#1C6FD6' },
    apple: { label: 'Apple', dark: '#E6E3DD', light: '#4A4A50' },
    gpu: { label: 'GPU', dark: '#9A9A9E', light: '#6B6B70' }
  };
  var SVG_OPEN = '<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true" focusable="false"';
  var MARKS = {
    // the eye: a lens with a three-quarter swirl and its centre
    nvidia: SVG_OPEN + ' fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2.6 12C5 7.9 8.3 5.8 12 5.8s7 2.1 9.4 6.2c-2.4 4.1-5.7 6.2-9.4 6.2S5 16.1 2.6 12z"/><path d="M15.6 12A3.6 3.6 0 1 0 12 15.6"/><circle cx="12" cy="12" r="1.2" fill="currentColor" stroke="none"/></svg>',
    // the arrow: the corner bracket pointing up and right, the bracket below it
    amd: SVG_OPEN + ' fill="currentColor"><path d="M8 3h13v13l-4-4V7h-5z"/><path d="M3 8l4 4v5h5l4 4H3z"/></svg>',
    // the swoosh: an open tilted ring around the dot of the i
    intel: SVG_OPEN + ' fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M7.4 7.6C4.9 8.8 3.4 10.5 3.4 12.3c0 3.4 4.3 5.9 9.8 5.9 4.5 0 7.6-1.6 7.6-3.9 0-1.4-1.3-2.6-3.5-3.3"/><path d="M11.2 9.6v6.2"/><circle cx="11.2" cy="6.6" r="1.1" fill="currentColor" stroke="none"/></svg>',
    // the apple: the bitten fruit with its leaf
    apple: SVG_OPEN + ' fill="currentColor"><path d="M16.4 12.6c0-2.5 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-2-1.5-.2-3 .9-3.8.9-.8 0-2-.9-3.3-.8-1.7 0-3.2 1-4.1 2.5-1.8 3-.5 7.6 1.3 10.1.9 1.2 1.9 2.6 3.2 2.5 1.3 0 1.8-.8 3.3-.8 1.6 0 2 .8 3.3.8 1.4 0 2.3-1.2 3.1-2.5 1-1.4 1.4-2.8 1.4-2.9 0 0-2.7-1-2.9-4.1zM13.9 5.3c.7-.8 1.2-2 1-3.2-1 0-2.2.7-2.9 1.5-.6.7-1.2 1.9-1 3 1.1.1 2.2-.5 2.9-1.3z"/></svg>',
    // the neutral fallback: a chip with its pins
    gpu: SVG_OPEN + ' fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><rect x="5.5" y="5.5" width="13" height="13" rx="2.2"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/><path d="M9 2.5v3M12 2.5v3M15 2.5v3M9 18.5v3M12 18.5v3M15 18.5v3M2.5 9h3M2.5 12h3M2.5 15h3M18.5 9h3M18.5 12h3M18.5 15h3"/></svg>'
  };
  function escH(s) { return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
  // the vendor a row is drawn as: the engine's word when it is one of the four, else the name decides (an OpenCL
  // integrated row arrives as vendor "other" with "Intel UHD Graphics 770"), else the neutral chip
  function vendorOf(cd) {
    var v = (cd && cd.vendor || '').toLowerCase();
    if (VENDORS[v] && v !== 'gpu') return v;
    var n = (cd && cd.name || '') + ' ' + (cd && cd.code || '');
    if (/nvidia|geforce|quadro|\bRTX\b|\bGTX\b/i.test(n)) return 'nvidia';
    if (/\bamd\b|radeon|\bgfx\d/i.test(n)) return 'amd';
    if (/intel|\barc\b|iris|\buhd\b/i.test(n)) return 'intel';
    if (/apple|\bM\d (max|pro|ultra)\b/i.test(n)) return 'apple';
    return 'gpu';
  }
  // the series line under the name, in mono: "RTX 50 series · Blackwell", "RX 9000 series · RDNA 4", "Arc B series ·
  // Battlemage", "M5 series · integrated"; an integrated row always ends in "integrated" (the same glyph, never a second mark)
  function seriesLine(cd) {
    var v = vendorOf(cd), n = cd.name || '', code = cd.code || '', m, out = '';
    if (v === 'nvidia') {
      if ((m = /\b(RTX|GTX)\s*(\d)(\d)(\d{2})\b/i.exec(n))) { var tens = m[2] + m[3], fam = m[1].toUpperCase(), gen = { RTX50: 'Blackwell', RTX40: 'Ada Lovelace', RTX30: 'Ampere', RTX20: 'Turing', GTX16: 'Turing', GTX10: 'Pascal', GTX90: 'Maxwell' }[fam + tens]; out = fam + ' ' + tens + ' series' + (gen ? ' · ' + gen : ''); }
      else if (/\b(H100|H200|A100|B200|B100|L40|L4|A30|A10)\b/i.test(n)) out = 'data centre';
      else if (/\bRTX\b|quadro/i.test(n)) out = 'RTX workstation';
      else out = 'GeForce';
    } else if (v === 'amd') {
      if ((m = /\bRX\s*(\d)(\d{3})\b/i.exec(n))) { var g = { 9: 'RDNA 4', 7: 'RDNA 3', 6: 'RDNA 2', 5: 'RDNA' }[m[1]]; out = 'RX ' + m[1] + '000 series' + (g ? ' · ' + g : ''); }
      else if (/\bgfx12/i.test(code + n)) out = 'RDNA 4'; else if (/\bgfx11/i.test(code + n)) out = 'RDNA 3'; else if (/\bgfx103/i.test(code + n)) out = 'RDNA 2'; else if (/\bgfx10/i.test(code + n)) out = 'RDNA';
      else if (/\bMI\d{2,3}/i.test(n)) out = 'Instinct · data centre';
      else out = 'Radeon';
    } else if (v === 'intel') {
      if ((m = /\bArc\b.*?\b([ABC])(\d{3})\b/i.exec(n)) || (m = /\b([ABC])(\d{3})\b.*\bArc\b/i.exec(n))) { var L = m[1].toUpperCase(); out = 'Arc ' + L + ' series · ' + ({ A: 'Alchemist', B: 'Battlemage', C: 'Celestial' })[L]; }
      else if (/iris/i.test(n)) out = 'Iris Xe'; else if (/\buhd\b/i.test(n)) out = 'UHD Graphics'; else out = 'Intel graphics';
    } else if (v === 'apple') {
      out = (m = /\bM(\d)\b/.exec(n)) ? 'M' + m[1] + ' series' : 'Apple silicon';
    } else out = cd.worker ? String(cd.worker) : '';
    if (cd.kind === 'integrated' || cd.kind === 'apple') out = (out ? out + ' · ' : '') + 'integrated';
    return out;
  }
  // the mark for a row: the vendor word, the glyph, the label for the title, the series line
  function vendorMark(cd) { var v = vendorOf(cd); return { vendor: v, svg: MARKS[v], label: VENDORS[v].label, series: seriesLine(cd) }; }
  function markHtml(cd, size) { var mk = vendorMark(cd); return '<div class="badge ' + (size ? size + ' ' : '') + mk.vendor + '" data-mark="' + mk.vendor + '" title="' + escH(mk.label) + '">' + mk.svg + '</div>'; }
  // the name block beside the mark: the name, the kind pill, the series line; shared by the first-run rows and the Cards page
  function nameHtml(cd, extra) { var mk = vendorMark(cd); return '<div class="name" title="' + escH(cardTitle(cd)) + '">' + escH(cd.name) + (extra || '') + ' <span class="kind ' + escH(cd.kind) + '">' + escH(kindWord(cd.kind)) + '</span></div>' + (mk.series ? '<div class="gen">' + escH(mk.series) + '</div>' : ''); }
  // the two together: one mark, one name block
  function rowHead(cd, extra) { return markHtml(cd) + nameHtml(cd, extra); }
  // sRGB relative luminance and the WCAG contrast ratio, for the test and for anyone picking a new vendor hex
  function hexRgb(h) { h = h.replace('#', ''); return [0, 2, 4].map(function (i) { return parseInt(h.substr(i, 2), 16); }); }
  function luminance(rgb) { var c = rgb.map(function (v) { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]; }
  function contrast(a, b) { var la = luminance(a), lb = luminance(b), hi = Math.max(la, lb), lo = Math.min(la, lb); return (hi + 0.05) / (lo + 0.05); }
  // the well's colour: the vendor hex at alpha over the card ground (graphite)
  function wellRgb(hex, alpha, ground) { var v = hexRgb(hex), g = hexRgb(ground); return v.map(function (c, i) { return Math.round(c * alpha + g[i] * (1 - alpha)); }); }
  function vendorContrast(v, theme) { var hex = VENDORS[v][theme], ground = theme === 'light' ? '#FFFFFF' : '#16161A', alpha = theme === 'light' ? WELL_ALPHA_LIGHT : WELL_ALPHA_DARK; return contrast(hexRgb(hex), wellRgb(hex, alpha, ground)); }

  // one GPU row on the Mine page: cd = state.mining.cards[i]
  function cardRow(cd) {
    var removed = cd.removed_at > 0, unusable = !!cd.problem;
    var on = !!cd.enabled && !removed && !unusable, st = removed ? 'removed' : unusable ? 'unusable' : (cd.state || 'off');
    // the rule (docs/plans/ember-tune.md, 6 October 2026): a card never shows 0 MH/s without a reason word
    var tuning = st === 'tuning', held = st === 'held';
    var word = removed ? 'removed' : unusable ? ('not usable (' + cd.problem + ')') : !on ? 'off' : st === 'mining' ? 'mining' : tuning ? tuneWord(cd) : held ? (cd.message || 'paused for a job from the team') : st === 'resting' ? 'resting (heat mode)' : st === 'restarting' ? 'restart in ' + (cd.restart_in_s || 0) + ' s' : st === 'stopped' ? (cd.message || cd.problem || 'stopped') : st === 'waiting' ? 'waiting for work' : st === 'failed' ? 'failed' : st === 'starting' ? 'starting' : st === 'ready' ? 'ready' : (st === 'off' || st === 'idle' || !st) ? (cd.reason || cd.message || 'starting') : st;
    // the design system's meaning (the old base, step 4): mining is on (ember); tuning and starting are running (blue);
    // a fault is bad (red) with its word; the rest carry no tone
    var tone = removed ? 'off' : unusable ? 'bad' : !on ? 'off' : st === 'mining' ? 'on' : (tuning || st === 'starting') ? 'running' : (st === 'failed' || st === 'restarting' || st === 'stopped') ? 'bad' : '';
    var rw0 = on && (st === 'mining' || tuning) && cd.hash_now > 0 ? rateWords(cd.hash_now * 1e6, cd.lane_word) : null, hash = rw0 ? rw0.text : '', hashUnit = rw0 ? rw0.unit : 'MH/s';
    var temp = cd.temp_gpu > 0 ? Math.round(cd.temp_gpu) + ' °C' : '';
    var tempTone = cd.temp_mem > 95 || cd.temp_gpu > 92 ? 'hot' : cd.temp_mem > 90 || cd.temp_gpu > 86 ? 'warm' : '';
    var power = cd.power_w > 0 ? Math.round(cd.power_w) + ' W' : '';
    var cap = cd.power_default_w > 0 ? Math.round(cd.power_default_w * (cd.power_pct || 80) / 100) : 0;
    var sub = unusable ? (cd.reason || '') : removed ? 'unplugged. Its row goes in five minutes.' : cd.message || (!on && cd.reason ? cd.reason : '') || '';
    // VectorWork: the coordinate the card mines, from the router (docs/product-a/ROUTER.md)
    var laneW = cd.lane_word || ''; if (laneW) sub = laneW + (sub ? ' · ' + sub : ''); // the lane by its name (main's read of 16 October 2026: never a coordinate number, a version or a worker's name on the page)
    var meta = [];
    if (cd.vram_mb) { var gb = cd.vram_mb / 1024; meta.push((gb >= 10 ? Math.round(gb) : gb.toFixed(gb < 4 ? 1 : 0)) + ' GB' + (cd.kind === 'apple' ? ' unified' : '')); }
    if (on && cd.accepted) meta.push(cd.accepted + ' block' + (cd.accepted === 1 ? '' : 's'));
    if (on && cd.prepared) meta.push('next program ready');
    return { hashUnit: hashUnit, key: cd.key, name: cd.name, vendor: cd.vendor, kind: cd.kind, kindWord: kindWord(cd.kind), integrated: cd.kind === 'integrated', on: on, removed: removed, unusable: unusable, idle: on && (st === 'off' || st === 'idle' || st === 'ready' || st === 'waiting' || !cd.state), canToggle: cd.kind !== 'unknown' && !removed && !unusable, word: word, tone: tone, hash: hash, temp: temp, tempTone: tempTone, power: power, cap: cap, sub: sub, meta: meta.join(' · '), title: cardTitle(cd) };
  }

  // driver-check (7 October 2026): the row's driver strip. {kind: none|fine|offer|note|running|reboot|error, text, button, sub}
  // offer = a missing or old driver with an Install button (Windows); note = the macOS, Linux or HiveOS sentence, no
  // button; running = this card's vendor is installing (the progress line); reboot = installed, Restart now;
  // error = the last install failed, Install again. The install is one at a time, so a second card of the same vendor
  // reads the same running line.
  function driverWords(cd, s) {
    var o = cd.driver_offer, d = (s && s.drivers) || {};
    if (!o) return { kind: 'none', text: '' };
    var mine = d.vendor && d.vendor === o.vendor, ext = cd.kind === 'external';
    // the second Windows machine, 7 October 2026: every card of the vendor has its worker stopped for the install (an earlier release; the Arc in the Razer
    // Core X V2 read as discrete, so the kind alone missed it), and the installer's display reset can take a machine with an
    // enclosure; the row says both before the click and while it runs
    var extRun = '. The ' + vendorWord(o.vendor) + ' workers are stopped until the installer ends; the display may reset' + (ext ? ', and this card\u2019s enclosure can take the machine for a minute' : '') + '.';
    if (mine && (d.status === 'downloading' || d.status === 'verifying' || d.status === 'installing')) {
      var pct = d.status === 'downloading' ? Math.round((d.progress || 0) * 100) + '%' : '';
      return { kind: 'running', text: (d.status === 'downloading' ? 'Downloading the ' : d.status === 'verifying' ? 'Checking the ' : 'Installing the ') + vendorWord(o.vendor) + ' driver ' + d.version + (pct ? ' · ' + pct : ''), sub: (d.message || '') + extRun, progress: d.progress || 0, dry: !!d.dry_run, external: ext };
    }
    if (mine && d.status === 'reboot') return { kind: 'reboot', text: vendorWord(o.vendor) + ' driver ' + d.version + ' installed. Restart Windows to finish.', button: 'restart', sub: 'Mining continues until you restart.' };
    if (mine && d.status === 'error') return { kind: 'error', text: 'The ' + vendorWord(o.vendor) + ' driver did not install: ' + (d.error || d.message || 'unknown error'), button: o.installable ? 'install' : '', sub: o.hash_source ? 'Checked against ' + o.hash_source : '' };
    if (o.status === 'fine') return { kind: 'fine', text: o.text };
    if (o.status === 'none') return { kind: 'note', text: o.text };
    if (!o.installable) return { kind: 'note', text: o.text };
    var sub = 'Downloaded from ' + hostOf(o.url) + ', checked against the vendor\u2019s published hash and signature, then installed with one administrator prompt. ';
    sub += 'The worker on every ' + vendorWord(o.vendor) + ' card stops for the install and comes back after it; the other cards keep mining. The display resets during the install' + (ext ? ', and a card in an external enclosure can take the machine for a minute and may need a restart' : '') + ': save your work first.';
    return { kind: 'offer', text: o.text, button: 'install', sub: sub, external: ext };
  }
  function vendorWord(v) { return v === 'nvidia' ? 'NVIDIA' : v === 'amd' ? 'AMD' : v === 'intel' ? 'Intel Arc' : v === 'apple' ? 'Apple' : v; }
  function hostOf(u) { var m = /^https?:\/\/([^\/]+)/.exec(u || ''); return m ? m[1] : 'the vendor'; }

  // "tuning: step 7 of 9 · measuring 220 W" (the live draw; the plan's words when there is no draw yet)
  function tuneWord(cd) {
    var step = cd.tune_steps > 0 ? 'step ' + (cd.tune_step || 1) + ' of ' + cd.tune_steps : '';
    var w = cd.power_w > 0 ? 'measuring ' + Math.round(cd.power_w) + ' W' : '';
    var rest = [step, w].filter(Boolean).join(' · ');
    return rest ? 'tuning: ' + rest : (cd.sweep_note || 'tuning');
  }
  // "about 12 min" / "under a minute" from a tune's seconds left
  function tuneEta(s) { s = Math.max(0, Math.round(s || 0)); return s < 60 ? 'under a minute' : 'about ' + Math.round(s / 60) + ' min'; }
  // the card a tune is running on, or null
  function tuningCard(cards) { var l = (cards || []).filter(function (c) { return c.state === 'tuning' || c.sweep_state === 'running'; }); return l.length ? l[0] : null; }

  // ---- money (miner-ui-3): £ a day from watts at pence per kWh; null when either is unknown ----
  function poundsPerDay(watts, pence) { return watts > 0 && pence > 0 ? watts * 24 / 1000 * pence / 100 : null; }
  // the currency symbol follows the region (Ember Heat, setRegion); £ until a region is chosen
  var currency = { code: 'GBP', symbol: '£', minor: 'p', usd: 1.32 }, locale = 'en-GB', override = '', regionCode = '';
  // the ONE money formatter (currency-21, 7 October 2026): Intl places the symbol as the locale does; whole units from 100
  function fmtMoney(v, cur, loc) {
    if (v === null || v === undefined || isNaN(v)) return '';
    var d = v >= 100 ? 0 : minorOf(cur).digits;
    try { return new Intl.NumberFormat(loc || locale, { style: 'currency', currency: cur, currencyDisplay: 'narrowSymbol', minimumFractionDigits: d, maximumFractionDigits: d }).format(v).replace(/[\u00a0\u202f]/g, ' '); }
    catch (e) { return (CURRENCIES[cur] || CURRENCIES.GBP).symbol + (d ? v.toFixed(d) : String(Math.round(v))); }
  }
  function money(v) { return fmtMoney(v, currency.code, locale); }
  // MH per watt: the engine's live figure, else rate over draw
  function effOf(cd) { if (cd.eff_mhw > 0) return cd.eff_mhw; return cd.power_w > 0 && cd.hash_now > 0 ? cd.hash_now / cd.power_w : 0; }
  // the efficiency scaled to its unit so it never reads 0.02 (the founder's word of 10:5x UK, 14 October 2026): 1.72 MH/W,
  // 17.2 kH/W, 172 H/W; effParts gives the figure and the unit apart for the tile
  function effParts(e) {
    if (!(e > 0)) return null;
    var u = 'MH/W', v = e;
    if (v < 1) { v = v * 1000; u = 'kH/W'; if (v < 1) { v = v * 1000; u = 'H/W'; } }
    return { v: v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2), u: u };
  }
  // the card rows and the Tune head keep the MH/W form main audited (T-R16: the unit once in the head); the stat row's tile
  // alone takes effParts
  function effText(e) { return e > 0 ? (e >= 10 ? e.toFixed(1) : e.toFixed(2)) + ' MH/W' : ''; }
  // the watts drawn now by the cards that are mining or tuning (an idle card's standby draw is not the fleet's cost;
  // the engine's mining.watts_total counts the same way)
  function wattsTotal(cards) { return (cards || []).filter(function (c) { return present(c) && (c.state === 'mining' || c.state === 'tuning'); }).reduce(function (a, c) { return a + (c.power_w > 0 ? c.power_w : 0); }, 0); }
  // why a row has no power or temperature cell: one sentence, once, instead of three n/a
  function noReading(cd) {
    if (cd.power_w > 0 || cd.temp_gpu > 0) return '';
    if (cd.vendor === 'apple') return 'No power or temperature reading on Apple silicon';
    if (cd.kind === 'integrated') return 'No power or temperature reading on an integrated GPU';
    if (cd.vendor === 'intel') return 'No power or temperature reading on Intel Arc yet: the driver exposes them through IGCL, not OpenCL';
    return '';
  }
  // the row's cells: rate, watts with MH/W, £ a day, temperature; a cell with no number is left out, never n/a
  function cells(cd, pence) {
    var r = cardRow(cd), out = {};
    out.hash = r.hash; out.power = r.power; out.temp = r.temp; out.tempTone = r.tempTone;
    out.eff = effText(effOf(cd));
    var ppd = poundsPerDay(cd.power_w, pence);
    out.money = ppd === null ? '' : money(ppd);
    out.needsPrice = cd.power_w > 0 && !(pence > 0);
    out.noReading = noReading(cd);
    return out;
  }

  // ---- the tune line under a row (miner-ui-3 section 4): cd, s = state, now ----
  function nextCheck(at, period, now) {
    if (!(at > 0) || !now) return '';
    period = period > 0 ? period : 604800;
    var next = at + period, d = next - now;
    if (d <= 0) return 'next check due now';
    if (d < 86400) return 'next check within a day';
    var dt = new Date(next * 1000);
    if (d < 6 * 86400) return 'next check ' + DAYS[dt.getDay()];
    return 'next check ' + dt.getDate() + ' ' + MONTHS[dt.getMonth()];
  }
  function tunePoint(cd) {
    var pct = cd.sweep_pct || cd.power_pct || 0;
    if (cd.tune_clock_mhz > 0) return cd.tune_clock_mhz + ' MHz at ' + pct + '%';
    return pct ? pct + '%, clock unlocked' : '';
  }
  function reasonOf(note) { return cap(String(note || '').replace(/^measure only /, 'measured only ')); }
  function tunable(cd) { return (cd.vendor === 'nvidia' || cd.vendor === 'amd' || cd.vendor === 'apple' || !!cd.sweep_supported) && present(cd) && cd.kind !== 'integrated'; }
  // {kind: none|paused|running|measured|tuned|stopped|idle, text, note, button: ''|'tune'|'stop', progress: -1 or 0..1}
  function tuneWords(cd, s, now) {
    cd = cd || {}; var st = (s && s.settings) || {};
    if (!tunable(cd)) return { kind: 'none', text: '', note: '', button: '', progress: -1 };
    if (st.tuning_off) return { kind: 'paused', text: st.tuning_note || 'Tuning paused fleet-wide by the signed manifest', note: '', button: '', progress: -1 };
    if (cd.state === 'tuning' || cd.sweep_state === 'running') {
      var step = cd.tune_steps > 0 ? 'step ' + (cd.tune_step || 1) + ' of ' + cd.tune_steps : (cd.sweep_note ? cd.sweep_note.replace(/^tuning:\s*/, '') : 'running');
      var eta = cd.tune_eta_s > 0 ? ' · ' + tuneEta(cd.tune_eta_s) + ' left' : '';
      var prog = cd.tune_steps > 0 ? Math.min(1, Math.max(0, ((cd.tune_step || 1) - 1) / cd.tune_steps)) : 0;
      return { kind: 'running', text: 'Tuning: ' + step + eta, note: '', button: 'stop', progress: prog };
    }
    var when = cd.sweep_at > 0 && now ? ago(now - cd.sweep_at) : '';
    var nc = nextCheck(cd.sweep_at, st.tune_period_s, now);
    if (cd.tune_line) {
      var nums = String(cd.tune_line).replace(/^(Tuned|Measured):\s*/, '');
      if (cd.tune_source === 'baseline' || !cd.tune_control) return { kind: 'measured', text: 'Measured ' + nums + ' as it runs' + (when ? ', ' + when : ''), note: reasonOf(cd.sweep_note), button: 'tune', progress: -1 };
      var src = cd.tune_source === 'confirm' ? ' from what other miners found, confirmed' : '';
      var pin = cd.pinned ? 'Your cap stays pinned at ' + (cd.power_pct || 0) + '% · ' : '';
      var pt = tunePoint(cd);
      var stopped = cd.sweep_note && /^tuning stopped/i.test(cd.sweep_note) ? ' · ' + cap(cd.sweep_note) : '';
      return { kind: 'tuned', text: pin + 'Tuned ' + (when || 'before') + src + (pt ? ' · ' + pt : '') + (nc ? ' · ' + nc : '') + stopped, note: nums, button: 'tune', progress: -1 };
    }
    if (cd.sweep_note && /^tuning stopped/i.test(cd.sweep_note)) return { kind: 'stopped', text: cap(cd.sweep_note), note: '', button: 'tune', progress: -1 };
    var why = !cd.tune_control && cd.sweep_note ? reasonOf(cd.sweep_note) : '';
    var text = st.sweep === false ? 'Not tuned yet: Ember Tune is off in Settings' : 'Not tuned yet: starts after 2 min of steady mining';
    return { kind: 'idle', text: text, note: why, button: 'tune', progress: -1 };
  }
  // the Tuning card's one line: what runs, what ran last, what comes next
  function schedule(cards, s, now) {
    var st = (s && s.settings) || {};
    if (st.tuning_off) return st.tuning_note || 'Tuning paused fleet-wide by the signed manifest.';
    var list = shownCards(cards).filter(tunable), tc = tuningCard(list);
    if (tc) { var step = tc.tune_steps > 0 ? ', step ' + (tc.tune_step || 1) + ' of ' + tc.tune_steps : ''; return 'Tuning now: ' + tc.name + step + (tc.tune_eta_s > 0 ? ', mining again in ' + tuneEta(tc.tune_eta_s) : '') + '.'; }
    var last = null;
    list.forEach(function (c) { if (c.sweep_at > 0 && (!last || c.sweep_at > last.sweep_at)) last = c; });
    if (last) return 'Last tune ' + ago(now - last.sweep_at) + ' (' + last.name + ') · ' + nextCheck(last.sweep_at, st.tune_period_s, now) + '.';
    if (!list.length) return 'No card here can be tuned.';
    return st.sweep === false ? 'Ember Tune is off. Tune and Tune all still work when you press them.' : 'Not tuned yet: the first tune starts 2 min into steady mining.';
  }
  // the goal control's consequence: goal = efficiency|balanced|rate, cards, pence
  var GOALS = { efficiency: 'the most hashes per watt', balanced: 'hashes per watt within 1% of the top rate', rate: 'the top rate, whatever the watts' };
  var GOAL_NAMES = { efficiency: 'Efficiency', balanced: 'Balanced', rate: 'Maximum' };
  function goalWords(goal, cards, pence) {
    goal = GOALS[goal] ? goal : 'balanced';
    var list = (cards || []).filter(present), w = 0;
    list.forEach(function (c) {
      if (goal === 'efficiency') w += c.sweep_watts > 0 ? c.sweep_watts : (c.power_w > 0 ? c.power_w * 0.9 : 0);
      else if (goal === 'rate') w += c.power_default_w > 0 ? c.power_default_w : (c.power_w || 0);
      else w += c.power_w > 0 ? c.power_w : 0;
    });
    var ppd = poundsPerDay(w, pence);
    return { goal: goal, label: GOALS[goal], watts: Math.round(w), consequence: ppd === null ? (w > 0 ? 'about ' + Math.round(w) + ' W' : '') : 'about ' + money(ppd) + ' a day at ' + Math.round(w) + ' W' };
  }

  // the big button on Mine: m = state.mining, n = state.node, s = { quitting }
  // one truth for the state words (main's ruling of 18:1x UK, 12 October 2026, from the founder's photo of the mini):
  // a worker running on any card is what "Mining", "Stop mining" and "Searching" mean; none running is "Not mining"
  // and "Start mining" whatever the engine's own state word says
  function workerUp(m) {
    var cards = shownCards((m || {}).cards).filter(present);
    if (!cards.length) return true; // no card detected yet: nothing contradicts the engine's own word
    return cards.some(function (c) { return c.enabled && /^(mining|tuning|starting|preparing|waiting|ready)$/.test(c.state || ''); });
  }
  // F-E63 (the founder, 15 October 2026; main's rule): the big button reflects the engine's state and nothing else:
  // 'Start mining' when the engine is paused, idle or stopped, 'Stop mining' when a card mines, 'Starting' with a
  // spinner between (the engine resumed and no card mines yet; or the click's own word while the engine has not yet
  // acknowledged it: the mini's log of 12:12 UK showed a resume acknowledged 11 s after the click, the tick 4.7 s).
  // `pend` is the last click (pressToggle); its word stands until the engine's state reflects it, never past PENDING_MS.
  var PENDING_MS = 45000;
  function startingSub(cards, s) {
    var c = cards.filter(function (x) { return x.enabled && /^(starting|preparing|ready|waiting|configuring)$/.test(x.state || ''); })[0];
    if (c && c.state === 'preparing') return 'preparing ' + (c.name || 'the card');
    if (c && (c.state === 'waiting' || c.state === 'ready')) return (s && s.source && s.source.problem) ? ((s.source.kind === 'node' ? 'node' : 'pool') + ' unreachable') : 'waiting for work from the ' + (s && s.source && s.source.kind === 'node' ? 'node' : 'pool');
    return 'the cards start';
  }
  function engineOff(m) { return !!m.paused || m.state === 'paused' || m.state === 'idle' || m.state === 'stopped'; }
  function pressToggle(t, now) { return t && t.act ? { act: t.act, at: now } : null; }
  // the click's word has been taken by the engine (or it is too old to trust): the page drops it
  function clickSettled(pend, m, now) {
    if (!pend) return true;
    if (now - pend.at >= PENDING_MS) return true;
    if (pend.act === 'pause') return engineOff(m);
    return !engineOff(m);
  }
  function toggle(m, n, s, pend) {
    var cards = shownCards(m.cards).filter(present);
    var on = cards.filter(function (c) { return c.enabled; }).length;
    var mining = cards.filter(function (c) { return c.state === 'mining'; }).length;
    var now = pend && pend.now !== undefined ? pend.now : (s && s.now_ms) || 0;
    if (s && s.quitting) return { label: 'Stopping', sub: s.quit_stage || 'the miners stop, then the app closes', cls: 'stop', disabled: true, act: '' };
    var tc = tuningCard(cards);
    if (tc) return { label: 'Tuning', sub: 'mining again in ' + tuneEta(tc.tune_eta_s) + ' · ' + tc.name, cls: 'stop', disabled: true, act: '' };
    var heldCards = cards.filter(function (c) { return c.state === 'held'; }).length;
    if (heldCards && !mining) return { label: 'Held', sub: 'a job from the team is using the cards. Mining restarts when it ends', cls: 'stop', disabled: true, act: '' };
    if (s && s.heat && s.heat.on && s.heat.phase === 'resting' && !mining && !m.paused) return { label: 'Stop mining', sub: 'resting · heat mode holds ' + s.heat.set_c.toFixed(1) + ' °C, heats again in ' + (s.heat.until_s < 45 ? 'under a minute' : Math.round(s.heat.until_s / 60) + ' min'), cls: 'stop', disabled: false, act: 'pause' };
    if (!on) return { label: 'Start mining', sub: cards.length ? 'no card switched on' : 'no card this app can use', cls: '', disabled: true, act: '' };
    // the click's own word until the engine takes it (F-E63)
    if (pend && !clickSettled(pend, m, now)) {
      if (pend.act === 'pause') return { label: 'Stopping', sub: 'the cards stop', cls: 'stop busy', disabled: true, act: '', spin: true };
      return { label: 'Starting', sub: 'the engine starts the cards', cls: 'busy', disabled: true, act: '', spin: true };
    }
    if (m.paused) return { label: 'Start mining', sub: 'paused · the pool session stays open', cls: '', disabled: false, act: 'resume' };
    if (m.state === 'mining' && !workerUp(m)) return { label: 'Start mining', sub: 'no worker running on a card', cls: '', disabled: false, act: 'start' };
    // the engine is on and no card mines yet: starting (a card configuring, preparing, ready or waiting), the click stops it
    if ((m.state === 'mining' || m.state === 'waiting') && !mining && workerUp(m)) return { label: 'Starting', sub: startingSub(cards, s), cls: 'stop busy', disabled: false, act: 'pause', spin: true };
    if (m.state === 'mining') return { label: 'Stop mining', sub: 'mining on ' + mining + ' of ' + on + ' card' + (on === 1 ? '' : 's'), cls: 'stop', disabled: false, act: 'pause' };
    if (m.state === 'waiting') return { label: 'Stop mining', sub: (s && s.source && s.source.problem) ? ((s.source.kind === 'node' ? 'node' : 'pool') + ' unreachable') : 'waiting for work from the ' + (s && s.source && s.source.kind === 'node' ? 'node' : 'pool'), cls: 'stop', disabled: false, act: 'pause' };
    if (false) return { label: 'Stop mining', sub: n && n.synced ? 'card starting' : n && n.state === 'behind' ? 'waiting: the node is behind the chain' : n && n.state === 'no peers' ? 'waiting: no peers on our chain' : 'waiting for work from the pool', cls: 'stop', disabled: false, act: 'pause' };
    // F-E50: an idle engine never reads "Stop mining"; the button starts, and its line is the engine's reason
    // when it has one (the newest bad notice), else the state word
    if (m.state === 'idle' || m.state === 'stopped') {
      var why = idleReason(m);
      return { label: 'Start mining', sub: why || (m.state === 'stopped' ? 'stopped' : 'not mining'), cls: '', disabled: false, act: 'start' };
    }
    return { label: 'Stop mining', sub: m.state || 'starting', cls: 'stop', disabled: false, act: 'pause' };
  }
  // the engine's newest bad notice as one short line (F-E50); empty when it has none
  function idleReason(m) {
    var list = (m && m.notices) || [];
    // the founder's M5 Max capture of 15 October 2026 (main's cell): the reason's sentence showed cut at "nothing....";
    // the whole first line is returned and the page wraps it, never cuts it
    for (var i = list.length - 1; i >= 0; i--) { if (list[i] && list[i].level === 'bad') { return String(list[i].detail || list[i].text || '').split('\n')[0].trim(); } }
    return '';
  }
  // the row's state word while the miner is paused: the card is on, the miner is not
  function rowWord(r, m) { return r.on && m && m.paused && (r.idle || r.word === 'off' || r.word === 'ready' || r.word === 'waiting for work') ? 'paused' : r.word; }

  // the Node page's plain words: n = state.node, clock = state.clock, eta = the sync ETA sentence (may be '')
  function nodeWords(n, clock, eta) {
    var bad = clock && clock.severity === 'block';
    if (bad) return { word: n.state === 'synced' ? 'synced' : n.state, line: 'Your clock is off by ' + skewWord(clock.skew_s) + '. Mining waits until it is synced.', tone: 'bad' };
    // a build with no node inside (Ember 0.2.1: the chain is read through the pool or the node named): the engine's
    // message is the line, and nothing claims a node that holds every block (the first real captures, 12 October 2026)
    if (/^no node runs inside/.test(n.message || '')) return { word: 'read-only', line: n.message, tone: 'ok' };
    switch (n.state) {
      case 'synced': return { word: 'synced', line: 'This node has every block the network has made.' + (n.tip_age_s >= 0 ? ' Last block ' + Math.round(n.tip_age_s) + ' s ago.' : n.last_reading_age_s >= 0 ? ' Read ' + Math.round(n.last_reading_age_s) + ' s ago.' : ''), tone: 'ok' };
      // N4 (6 October 2026): a tip that stopped moving is never "synced"; the node says why
      case 'behind':
        // 7 October 2026: a node whose blocks never merge into the network (src/merge.rs) is behind with a tip that still moves
        if (n.sync_cause === 'not merging') return { word: 'behind', line: 'Your blocks are not merging into the network: every other node is more than the merge depth ahead. Mining pauses until this node merges again; a slow link is the usual cause.', tone: 'bad' };
        return { word: 'behind', line: (n.tip_age_s >= 0 ? 'No new block for ' + tipAge(n.tip_age_s) + '. ' : '') + 'The chain may have moved on without this node. Mining pauses until it catches up; the node re-dials its peers by itself.', tone: 'bad' };
      case 'no peers': return { word: 'no peers', line: 'No other node on our chain is connected. The node keeps dialling the seed list; mining waits for block templates.', tone: 'bad' };
      case 'syncing':
        var pct = n.headers > n.blocks && n.headers > 0 ? Math.floor(n.blocks / n.headers * 100) : 0;
        var line = n.headers > n.blocks ? (eta || n.message || 'downloading blocks') + ' · ' + withCommas(n.blocks) + ' of ' + withCommas(n.headers) + ' (' + pct + '%)' : (n.message || 'caught up, waiting for the next block');
        return { word: 'syncing', line: line, tone: '' };
      case 'starting': return { word: 'starting', line: n.message || 'opening the database', tone: '' };
      case 'restarting': return { word: 'restarting', line: 'restart in ' + (n.restart_in_s || 0) + ' s' + (n.message ? ' · ' + n.message : ''), tone: 'bad' };
      case 'failed': return { word: 'failed', line: n.message || 'the node could not start', tone: 'bad' };
      case 'stopped': return { word: n.source === 'none' ? 'not started' : 'stopped', line: n.message || 'the node is not running', tone: 'bad' };
      case 'stub': return { word: 'stub engine', line: 'This node was built without the mining engine, so it refuses every real block. Reinstall the app.', tone: 'bad' };
    }
    return { word: n.state || 'starting', line: n.message || '', tone: '' };
  }
  // "12 s" / "4 min" / "53 min" / "2 h" for a tip age
  function tipAge(s) { s = Math.max(0, Math.round(s)); return s < 90 ? s + ' s' : s < 5400 ? Math.round(s / 60) + ' min' : Math.round(s / 3600) + ' h'; }
  // the one node line on Mine: "Node synced · 6 peers · 109,859 blocks · last block 4 s ago"
  // app-ia-26 (A14): one line, the state word and what matters for it; the facts live in Settings > Node
  // 0.2.3: the Mine page's source line reads the work source (the pool, or the node a user named), never the old
  // embedded node: "Pool pool.igneum.network:3341 · connected · head N", or "Pool unreachable · <the words>"
  var lastHead = { height: 0, at: 0 };
  function lastSeenClock(height) {
    var now = Date.now();
    if (height !== lastHead.height) lastHead = { height: height, at: now };
    var d = new Date(lastHead.at);
    return (d.getHours() < 10 ? '0' : '') + d.getHours() + ':' + (d.getMinutes() < 10 ? '0' : '') + d.getMinutes();
  }
  function sourceLine(src, m) {
    src = src || {}; var isNode = src.kind === 'node', what = isNode ? 'Node' : 'Pool', where = (src.url || '').replace(/\s+notls$/, '').replace(/^https?:\/\//, '') || (isNode ? 'the node you named' : 'pool.igneum.network:3341');
    if (m && m.state === 'idle') return { text: what + ' ' + where, sub: 'Press Start mining to connect.', tone: '' };
    // a paused app reads no new head from its source (main's read of 0.2.3 on the founder's laptop, 17:4x UK, 12 October
    // 2026: the pool line's head stale while paused): the head is named with the minute it was last seen
    if (m && m.state === 'paused' && src.connected && src.height > 0) return { text: what + ' ' + where + ' · paused · head ' + withCommas(src.height) + ' last seen ' + lastSeenClock(src.height), sub: 'The head moves again when mining resumes.', tone: '' };
    if (src.connected && src.height > 0) lastSeenClock(src.height);
    if (src.connected) return { text: what + ' ' + where + ' · connected' + (src.relation ? ' · ' + src.relation : '') + (src.chain_id > 0 ? ' · chain ' + src.chain_id : '') + (src.height > 0 ? ' · head ' + withCommas(src.height) : ''), sub: isNode ? 'Work comes from the node you named.' : 'Work comes from the project’s pool; every share is receipted.', tone: 'ok' };
    if (src.problem) return { text: what + ' unreachable', sub: src.problem, tone: 'bad' };
    return { text: what + ' ' + where + ' · connecting', sub: '', tone: '' };
  }
  function nodeLine(n, clock, eta, src, m) {
    if (src) return sourceLine(src, m);
    var w = nodeWords(n, clock, eta), parts = ['Node ' + w.word], bad = clock && clock.severity === 'block';
    if (w.word === 'read-only') parts = ['Chain read through the source (no node inside this build)'];
    else if (bad) parts.push('clock off by ' + skewWord(clock.skew_s));
    else if (n.state === 'synced') { if (n.peers > 0) parts.push(n.peers + ' peer' + (n.peers === 1 ? '' : 's')); }
    else if (n.state === 'syncing') { if (n.headers > n.blocks) parts.push(withCommas(n.blocks) + ' of ' + withCommas(n.headers) + ' blocks'); var left = /^(about [^·]+? left)/.exec(eta || n.message || ''); if (left) parts.push(left[1]); }
    else if (n.state === 'behind') { if (n.sync_cause === 'not merging') parts.push('your blocks are not merging'); else if (n.tip_age_s >= 0) parts.push('last block ' + tipAge(n.tip_age_s) + ' ago'); }
    else if (n.state === 'no peers') parts = ['Node: no peers, dialling the seeds']; // v0.2.2: the founder's words
    else if (n.state === 'restarting') parts.push(n.message || ('restart in ' + (n.restart_in_s || 0) + ' s'));
    else if (n.state === 'failed' || n.state === 'stopped' || n.state === 'starting') { if (n.message) parts.push(n.message); }
    return { text: parts.join(' · '), sub: w.line, tone: w.tone };
  }
  function skewWord(sk) { sk = Math.round(Math.abs(sk || 0)); return sk < 120 ? sk + ' s' : sk < 7200 ? Math.floor(sk / 60) + ' min ' + (sk % 60) + ' s' : Math.floor(sk / 3600) + ' h ' + Math.floor(sk % 3600 / 60) + ' min'; }
  function peersLine(n) { return n.peers === 0 ? 'none yet: looking for the seed node' : n.peers === 1 ? 'one other node this one talks to' : 'other nodes this one talks to'; }
  function heightLine(n) { return n.headers > n.blocks + 2 ? 'of ' + withCommas(n.headers) + ' headers seen' : 'blocks this node holds'; }

  // the next planned rule change: switches = state.node.consensus_switches, daa = state.node.daa
  function nextSwitch(switches, daa) {
    var list = (switches || []).slice().sort(function (a, b) { return a.daa - b.daa; });
    for (var i = 0; i < list.length; i++) if (list[i].daa > daa) return { name: list[i].name, daa: list[i].daa, away: list[i].daa - daa, key: list[i].key };
    return null;
  }
  function switchLine(sw, daa) {
    if (!sw) return (daa > 0 ? 'Every planned rule change is behind this node. ' : '') + 'A rule change is planned ahead. The node applies it by itself when the chain reaches that height.';
    var s = sw.away * BLOCK_S, blocks = daa > 0 ? withCommas(sw.away) + ' blocks away, about ' + (s < 3600 ? Math.max(1, Math.round(s / 60)) + ' min' : s < 86400 ? Math.floor(s / 3600) + ' h ' + Math.round(s % 3600 / 60) + ' min' : Math.floor(s / 86400) + ' d') + ' at ' + BLOCK_S + '-second blocks.' : 'the node applies it by itself at that height.';
    return sw.name + ' at block ' + withCommas(sw.daa) + ': ' + blocks;
  }

  // the Prove page: pv = state.proving, enabled = state.settings.prove, synced = state.node.synced
  // the "Prove instead of mining" row: shown only on a machine whose every NVIDIA card is under 12 GB (pv.under_12gb),
  // the switch's state from settings.prove_instead; the sentence names the choice in force
  function proveInsteadWords(pv, settings) {
    pv = pv || {}; settings = settings || {};
    if (!pv.under_12gb) return { show: false, on: false, help: '' };
    if (settings.prove_instead) return { show: true, on: true, help: 'On: the miner is held off on the small card while it proves; switch off to mine again.' };
    return { show: true, on: false, help: 'A card under 16 GB holds the prover or the miner, never both (the miner holds 6 GB, a proof 8). On, the miner stops on that card while it proves and comes back when proving stops.' };
  }
  function proveWords(pv, enabled, synced) {
    pv = pv || {};
    if (!enabled) return { word: 'off', sub: 'not proving', tone: 'off', note: 'Off. Switch it on and this machine proves the shards it is given.' };
    if (pv.status === 'setup' || (pv.enabled && !pv.available && pv.setup_hint)) return { word: 'needs setup', sub: 'one-time install', tone: 'bad', note: pv.setup_hint || pv.message || 'The prover is not installed yet.' };
    if (!synced || pv.status === 'waiting') return { word: 'waiting', sub: 'for the node to sync', tone: '', note: pv.message || 'Proving starts once the node has every block.' };
    if (pv.status === 'proving') return { word: 'proving', sub: pv.current || 'a shard', tone: 'on', note: pv.message || 'A proof takes minutes on a CPU and seconds on an NVIDIA card.' };
    if (pv.status === 'submitted') return { word: 'submitted', sub: 'waiting to be paid', tone: 'on', note: pv.message || 'The node checks the proof, then the chain pays it.' };
    if (pv.status === 'idle') return { word: 'idle', sub: 'nothing assigned', tone: 'ok', note: pv.message || 'On. Nothing to prove right now. The next shard arrives within a minute.' };
    return { word: pv.status || 'on', sub: pv.backend ? pv.backend.toUpperCase() : '', tone: '', note: pv.message || '' };
  }
  function verifierWords(pv) {
    var vm = (pv && pv.verifier_mode) || 'unknown';
    var word = { command: 'verifying', trust: 'trusting without verifying (test networks only)', off: 'off, passing proofs along', unknown: 'not read yet' }[vm] || vm;
    if (vm === 'command' && pv.pool_entries) word += ' · pool ' + pv.pool_verified + '/' + pv.pool_entries + (pv.pool_failed ? ', ' + pv.pool_failed + ' rejected' : '');
    return { word: word, tone: vm === 'command' ? 'ok' : (vm === 'off' || vm === 'trust') ? 'warn' : 'dim', needsSetup: vm === 'off' && /WSL2 prover is not installed/.test((pv && pv.verifier_reason) || '') };
  }
  // the tier sentence per card: can this card prove, and how (the 24 GB rule and the 20.4 GB shard are the app's own
  // words since miner-ui-2; the engine's proving.available wins when it disagrees)
  function proveTier(cd) {
    var gb = cd.vram_mb ? Math.round(cd.vram_mb / 1024) : 0;
    if (cd.vendor === 'apple') return cd.name + ': proves on the CPU, slowly.';
    if (cd.vendor === 'nvidia' && gb > 0 && gb < PROVE_MIN_GB) return cd.name + ': mining and proving together need a 16 GB card; this card does one at a time; mining continues (' + gb + ' GB).';
    if (cd.vendor === 'nvidia') return gb >= PROVE_GB ? cd.name + ': proves while it mines (' + gb + ' GB; a full shard needs ' + SHARD_GB + ' GB).' : cd.name + ': cannot prove a full shard (' + gb + ' GB; a full shard needs ' + SHARD_GB + ' GB).';
    return cd.name + ': cannot prove yet (proving needs an NVIDIA card).';
  }
  // the counts in one sentence: "idle · nothing assigned · 0 assigned · 0 proven · 0 paid · 0.0000 IGN"
  function proveLine(pv, enabled, synced) {
    pv = pv || {};
    var w = proveWords(pv, enabled, synced), ign = weiNonZero(pv.paid_wei) ? ignText(pv.paid_wei, 4) : '0.0000';
    return cap(w.word) + (w.sub ? ' · ' + w.sub : '') + ' · ' + (pv.assigned || 0) + ' assigned · ' + (pv.submitted || 0) + ' proven · ' + (pv.paid || 0) + ' paid · ' + ign + ' IGN';
  }

  // ED-05 (the founder, 11 October 2026): the dev fee's Earnings row: the rate and the form, the fee's own counts, and
  // the net amount: what the chain credited to YOUR address (the fee's templates credit the dev address, never yours),
  // so the credited figure is already net; the row says so with the rate kept by you
  function devFeeEarningsRow(s) {
    var d = s.dev_fee || {}, m = s.mining || {};
    var credit = m.share_lane && m.credit_wei ? ignText(m.credit_wei, 4) : null;
    if (!d.on) {
      var why = d.line && d.line.indexOf('no signed') >= 0 ? 'off: no signed release manifest names the dev payout address' : 'off (--dev-fee 0)';
      return { text: 'none', sub: 'Dev fee ' + why + (credit !== null ? '; credited to you ' + credit + ' IGN' : '') };
    }
    var pct = d.percent || 1, per = Math.round(100 / pct);
    var addr = d.address ? d.address.slice(0, 8) + '…' + d.address.slice(-4) : 'the manifest address';
    var counts = 'one template in ' + per + ' to ' + addr + '; this session ' + withCommas(d.templates || 0) + ' fee template' + ((d.templates || 0) === 1 ? '' : 's') + ', ' + withCommas(d.shares_accepted || 0) + ' share' + ((d.shares_accepted || 0) === 1 ? '' : 's') + ' accepted on them';
    var net = credit !== null ? '; net to you ' + credit + ' IGN (credited to your address; the fee templates credit the dev address, never yours)' : '; net to you ' + (100 - pct) + ' of every 100 templates';
    // the fee-waiver handshake (16.3): on a pool session the pool's answer stands beside the dev fee, and the receipts
    var pool = d.fee_words && d.fee_words.indexOf('pool fee') >= 0 ? '; ' + d.fee_words.slice(d.fee_words.indexOf('pool fee')) : '';
    var rc = d.receipts_total ? '; receipts ' + withCommas(d.receipts_total) + ' (verified ' + withCommas(d.receipts_v1_verified || 0) + (d.receipts_v1_failed ? ', failed ' + withCommas(d.receipts_v1_failed) : '') + ')' : '';
    // main's audit of 22:1x UK, 13 October 2026: three short lines (the rate and address; this session's fee templates
    // and shares; the pool's fee answer), the ED-17 clause on a line of its own; never one paragraph
    var l1 = 'one template in ' + per + ' to ' + addr + (credit !== null ? '; net to you ' + credit + ' IGN, credited to your address' : '; net to you ' + (100 - pct) + ' of every 100 templates');
    var l2 = 'this session: ' + withCommas(d.templates || 0) + ' fee template' + ((d.templates || 0) === 1 ? '' : 's') + ', ' + withCommas(d.shares_accepted || 0) + ' share' + ((d.shares_accepted || 0) === 1 ? '' : 's') + ' accepted on them' + (rc ? rc.replace(/^; /, '; ') : '');
    var pw = pool.replace(/^; /, ''), m17 = pw.match(/^(.*?)\s*\((waived:.*?)\)\s*$/), l3 = pw ? (m17 ? 'the pool’s answer: ' + m17[1].trim() + ', waived' : 'the pool’s answer: ' + pw) : '', l4 = m17 ? m17[2].replace(/^waived:\s*/, '').replace(/^(.)/, function (c) { return c.toUpperCase(); }) : '';
    // main's read of the 0.2.19 sheets (16 October 2026): never a wall of "bps", "settle window" and "fee template"; two plain
    // sentences and the session's counts on one line under them
    var plain1 = 'Dev fee ' + pct + ' percent: one block template in ' + per + ' goes to the project\u2019s address, ' + (per - 1) + ' are yours.';
    var plain2 = pw ? 'The pool\u2019s 1 percent is waived once your fee shares are read on the chain.' : '';
    var nt = d.templates || 0, plain3 = 'This session: ' + withCommas(nt) + ' template' + (nt === 1 ? '' : 's') + ' to the project, ' + withCommas(d.shares_accepted || 0) + ' share' + ((d.shares_accepted || 0) === 1 ? '' : 's') + ' accepted on ' + (nt === 1 ? 'it' : 'them') + (d.receipts_total ? ', ' + withCommas(d.receipts_total) + ' receipt' + (d.receipts_total === 1 ? '' : 's') : '') + (credit !== null ? '; net to you ' + credit + ' IGN' : '') + '.';
    return { text: pct + '%', sub: counts + net + pool + rc, lines: [plain1, plain2, plain3].filter(Boolean), detail: [l1, l2, l3, l4].filter(Boolean) };
  }

  // the miner's own dev-fee line, next to the rewards address
  function devFeeText(s) {
    var d = s.dev_fee || {};
    var addr = d.address ? ' to ' + d.address.slice(0, 8) + '…' + d.address.slice(-4) : '';
    if (!s.settings.dev_fee) return 'Dev fee off (--dev-fee 0); the default is 1% (1 block in 100)' + addr;
    if (d.line && !d.on && !d.address) return 'Dev fee: ' + d.line;
    return 'Dev fee ' + (d.percent || 1) + '% (1 block in 100)' + addr + (d.address ? '' : '; the miner names the address when it starts');
  }
  // earnings-26 (the founder, 8 October 2026): the fee's one place in the app is the last section of Settings: the rate, what it
  // funds, the site's wording (site/miner.html, the dev fee block), no control. Nothing about the fee on Earnings or anywhere else.
  var FEE_RATE = '1%';
  function devFeeLine(s) {
    var fee = (s.mining || {}).fee_total || 0;
    return s.settings.dev_fee ? 'The Ember software takes a ' + FEE_RATE + ' dev fee. One block template in 100 is requested with the dev payout address instead of yours' + (fee ? ' (' + withCommas(fee) + ' so far)' : '') + '. The fee goes to Igneum Labs LLC, the company that ships the software.' : 'The dev fee is off on this install (--dev-fee 0). Every block pays your address.';
  }

  // the remote-jobs line on Settings: j = state.jobs, now = state.now
  function jobsNote(j, now, jobTitle) {
    var parts = [];
    if (j.account) parts.push(j.account);
    if (!j.allowed) parts.push('Off. Nothing runs here until the switch above allows jobs from the team.');
    else if (!j.url_set) parts.push('This build has no jobs address.');
    else if (j.error) parts.push(j.error + '.');
    else if (j.active) parts.push('Running: ' + jobTitle(j) + '.');
    else parts.push('Nothing running' + (j.checked_at ? '; checked ' + rel((now || 0) - j.checked_at) : '') + (j.queued ? '; ' + j.queued + ' queued' : '') + ((j.history || []).length ? '; ' + j.history.length + ' job' + (j.history.length === 1 ? '' : 's') + ' ran here' : '') + '.');
    return parts.join(' ');
  }
  // an activity line for a user: a JSON tail goes to the log, a long line is cut at a word
  function eventLine(text, max) {
    text = String(text || ''); max = max || 140;
    var j = text.indexOf('{'); if (j > 0) text = text.slice(0, j).replace(/[:\s]+$/, '') + ' (details in the log)';
    if (text.length > max) { var cut = text.lastIndexOf(' ', max - 2); text = text.slice(0, cut > 40 ? cut : max - 2) + '…'; }
    return text;
  }
  // the engine's event line in a user's voice (src/engine.rs event strings, 6 October 2026): {text, tech}; tech is
  // the engine's own line when it differs, for the tooltip. Unknown lines keep their words with a JSON tail cut.
  var EVENT_MAP = [
    [/^updated to Ember (\S+) from (\S+)/, function (m) { return 'Updated to ' + m[1]; }],
    [/^Ember (\S+) is available: downloading/, function (m) { return 'Downloading ' + m[1]; }],
    [/^Ember (\S+) is ready/, function (m) { return m[1] + ' is ready to install'; }],
    [/^installing Ember (\S+)/, function (m) { return 'Installing ' + m[1]; }],
    [/^update to (\S+) failed/, function (m) { return 'The update to ' + m[1] + ' failed'; }],
    [/^the update to (\S+) did not take/, function (m) { return 'The update to ' + m[1] + ' did not take'; }],
    [/^Ember (\S+) did not stay up twice; restoring (\S+)/, function (m) { return m[1] + ' did not stay up; going back to ' + m[2]; }],
    [/^update downloaded/, function () { return 'Update downloaded'; }],
    [/^OTA: administrator approval not given/, function () { return 'The update waits for the next time someone is at this PC'; }],
    [/^job (\S+) \((.+)\) starts: /, function (m) { return 'A job from the team started: ' + Notices.jobWord(m[2], m[1]); }],
    [/^job (\S+) \((.+)\) (done|failed|timeout|aborted) after (\d+) min/, function (m) { return 'A job from the team ' + (m[3] === 'done' ? 'ran' : m[3] === 'timeout' ? 'ran out of time' : m[3] === 'aborted' ? 'was stopped' : 'failed') + ': ' + Notices.jobWord('', m[1]) + ', ' + m[4] + ' min'; }],
    [/^(\d+) remote jobs? received from Igneum/, function (m) { return m[1] + ' job' + (m[1] === '1' ? '' : 's') + ' from the team received'; }],
    [/^remote job: update check now/, function () { return 'The team asked for an update check'; }],
    [/^remote job: the app restarts/, function () { return 'The team asked the app to restart'; }],
    [/^remote job asked for a node restart, but the node is external/, function () { return 'The team asked for a node restart. This node is not the app’s, so nothing was done'; }],
    [/^remote jobs allowed/, function () { return 'Jobs from the team can run here'; }],
    [/^remote jobs switched off/, function () { return 'Jobs from the team are off'; }],
    [/^job finished; the miners restart/, function () { return 'The job finished; mining restarts'; }],
    [/^node synced: (\d+) blocks, (\d+) peer/, function (m) { return 'Node synced: ' + withCommas(+m[1]) + ' blocks, ' + m[2] + ' peer' + (m[2] === '1' ? '' : 's'); }],
    [/^a node already answers on/, function () { return 'Using the node already running on this machine'; }],
    [/^consensus parameters from the signed manifest/, function () { return 'Rule settings read from the team'; }],
    [/^this node build does not understand the consensus parameters/, function () { return 'This node is too old for the team’s rule settings. An app update carries the newer node'; }],
    [/^restarting the node with the new consensus parameters/, function () { return 'Restarting the node with the new rule settings'; }],
    [/^kernel tuning from the signed manifest/, function () { return 'Tuning starting points read from the team'; }],
    [/^block accepted by the node \((.+)\)/, function (m) { return 'Block found by ' + m[1]; }],
    [/^first block: found by (.+) \(block card\)/, function (m) { return 'Your first block, found by ' + m[1]; }],
    [/^block ([\d,]+) of this machine: found by (.+) \(block card\)/, function (m) { return 'Block ' + m[1] + ' of this machine, found by ' + m[2]; }],
    [/^first block on (.+) \(block card\)/, function (m) { return 'First block on ' + m[1]; }],
    [/^public profile on:/, function () { return 'Your address page is public'; }],
    [/^public profile off:/, function () { return 'Your address page is unlisted'; }],
    [/^igneumd exited with code (\d+) after (\d+) s; restarting/, function (m) { return 'The node stopped (code ' + m[1] + '). Restarting'; }],
    [/^igneumd exited at once \(code (\d+)\)/, function (m) { return 'The node stopped at once (code ' + m[1] + ')'; }],
    [/^igneumd could not start/, function () { return 'The node could not start'; }],
    [/^miner (\S+) exited with code (\d+); restarting in (\d+) s/, function (m) { return 'A card stopped (code ' + m[2] + '). Restarting in ' + m[3] + ' s'; }],
    [/^no reading from the node for (\d+) s/, function (m) { return 'No reading from the node for ' + m[1] + ' s. Restarting it'; }],
    [/^no peer after 90 s/, function () { return 'No other node found after 90 s. Still looking'; }],
    [/^no payout address/, function () { return 'No rewards address yet: finish the setup'; }],
    [/^mining paused; the (node keeps running|chain stays connected)/, function () { return 'Mining paused. The chain stays connected'; }],
    [/^mining resumed/, function () { return 'Mining resumed'; }],
    [/^GPU power cap in force: (.+)/, function (m) { return 'Power cap in force: ' + m[1]; }],
    [/^GPU power cap NOT applied/, function () { return 'Power cap not set. The administrator prompt was refused (Retry on the card)'; }],
    [/^power control on:/, function () { return 'Power control on'; }],
    [/^rewards go to (\S+)/, function (m) { return 'Rewards go to ' + shortHex(m[1], 6, 4); }],
    [/^settings changed \((.+)\); the miners restart/, function (m) { return 'Settings changed (' + m[1] + '). Mining restarts'; }],
    [/^(.+): tune queued; it starts once/, function (m) { return m[1] + ': tune queued'; }],
    [/^(.+): tuning started: /, function (m) { return m[1] + ': tuning started'; }],
    [/^(.+): best point (.+?) \((.+?)\); your setting stays pinned/, function (m) { return m[1] + ': best point ' + m[2] + ' (' + m[3] + '). Your cap stays pinned'; }],
    [/^(.+): measured as it runs: (.+)/, function (m) { return m[1] + ': measured ' + m[2]; }],
    [/^(.+): no tune: (.+)/, function (m) { return m[1] + ': no tune (' + m[2] + ')'; }],
    [/^(.+): worker fault: (.+); the miner restarts the worker/, function (m) { return m[1] + ': an error. Restarting it'; }],
    [/^(.+): the worker could not compile the next program/, function (m) { return m[1] + ': the next program is built at the top of the hour instead'; }],
    [/^(.+) worker ready/, function (m) { return m[1] + ': ready'; }],
    [/^(.+) kernel race: (.+?) at ([\d.]+) MH\/s/, function (m) { return m[1] + ': fastest program chosen, ' + m[3] + ' MH/s'; }],
    [/^(.+): switched on, (\d+) identit/, function (m) { return m[1] + ': on'; }],
    [/^(.+): switched off/, function (m) { return m[1] + ': off'; }],
    [/^(.+): (\d+) identit(y|ies); its worker restarts/, function (m) { return m[1] + ': ' + m[2] + ' miner' + (m[2] === '1' ? '' : 's') + ' on it. The card restarts'; }],
    [/^next hourly program compiled and ready/, function () { return 'Next hourly program ready'; }],
    [/^the hourly program changed; the worker restarts/, function () { return 'New hourly program. The card restarts on it'; }],
    [/^hourly program changed: /, function () { return 'New hourly program'; }],
    [/^program pack out of date, rebuilding/, function () { return 'Rebuilding the mining programs'; }],
    [/^GPU worker built from source/, function () { return 'Mining program built'; }],
    [/^block (\d+) shard (\d+) paid (.+) IGN/, function (m) { return 'Proof paid: ' + m[3] + ' IGN'; }],
    [/^block (\d+) shard (\d+) proven and submitted in (\d+) s/, function (m) { return 'Proof sent in ' + m[3] + ' s'; }],
    [/^segment .+ paid (.+) IGN to the aggregator/, function (m) { return 'Segment paid: ' + m[1] + ' IGN'; }],
    [/^segment (\S+)\.\.(\S+) aggregated and submitted in (\d+) s/, function (m) { return 'Segment proven in ' + m[3] + ' s'; }],
    [/^segment (\S+)\.\.(\S+): (\d+) shards proven, aggregated and submitted in (\d+) s/, function (m) { return 'Segment proven: ' + m[3] + ' shards in ' + m[4] + ' s'; }],
    [/^segment (\S+)\.\.(\S+): (\d+) shards proven and submitted in (\d+) s; the segment record is held/, function (m) { return 'Segment proven: ' + m[3] + ' shards in ' + m[4] + ' s. Its record waits for the chain'; }],
    [/^segment (\S+)\.\.(\S+) record accepted on retry/, function () { return 'Segment record accepted'; }],
    [/^WSL2 prover setup started/, function () { return 'Prover setup started in its own window'; }],
    [/^Card removed: (.+); its worker stopped/, function (m) { return 'Card removed: ' + m[1]; }],
    [/^(.+): not usable \((.+?)\); its worker stopped/, function (m) { return m[1] + ': not usable (' + m[2] + ')'; }],
    [/^clock agrees with the network again/, function () { return 'Clock agrees with the network again'; }],
    [/^asking the system to set its clock/, function () { return 'Setting the clock from a time server'; }],
    [/^clock: (.+)/, function (m) { return 'Clock: ' + m[1]; }],
    [/^devnet only: the node trusts proof records/, function () { return 'Trust mode: proofs are not checked (Settings)'; }],
    [/^a sweep is setting the caps right now/, function () { return 'A tune is setting the caps now. Retry when it is done'; }],
    [/^updates are off for this engine/, function () { return 'Updates are off for this copy (a measurement run)'; }],
    [/^(.+); the node restarts$/, function (m) { return cap(m[1]) + '; the node restarts'; }],
    [/^(.+); the node is external, so the app cannot restart it/, function (m) { return cap(m[1]) + '; the node is not the app’s, so it is not restarted'; }]
  ];
  function plainEvent(text) {
    text = String(text || '');
    for (var i = 0; i < EVENT_MAP.length; i++) { var m = EVENT_MAP[i][0].exec(text); if (m) { var out = EVENT_MAP[i][1](m); return { text: out, tech: out === text ? '' : text }; } }
    var cut = eventLine(text);
    return { text: cap(cut), tech: cut === text ? '' : text };
  }
  // the pill in the top bar: a verb a user understands, and its tone (on = molten, bad = ember, '' = grey)
  function pill(s) {
    if (s.quitting) return { text: s.quit_stage ? 'Stopping · ' + s.quit_stage.replace(/^quitting: /, '') : 'Stopping', tone: '' };
    if (!s.setup_done) return { text: s.detecting ? 'Detecting cards' : 'Setting up', tone: '' };
    var m = s.mining || {}, n = s.node || {};
    // app-ia-26: the state word only; the rate lives on Mine, the reason on the node line and the card rows
    if ((s.jobs && s.jobs.active) || m.state === 'held') return { text: 'Job running', tone: '' };
    if (tuningCard(m.cards)) return { text: 'Tuning', tone: 'on' };
    // F-E14 (13 October 2026): a connected source sending no work is said, never "Mining"
    if (waitingForWork(s)) return { text: 'Waiting for work', tone: '' };
    if (m.state === 'mining') return workerUp(m) ? { text: 'Mining', tone: 'on' } : { text: 'Not mining', tone: '' };
    if (s.heat && s.heat.on && s.heat.phase === 'resting') return { text: 'Resting · heat mode', tone: '' }; // never a bare rest word (the heat lane's rule)
    if (m.state === 'paused') return { text: 'Paused', tone: '' };
    // 0.2.3 (the founder's order of 12 October 2026): the pill reads the SOURCE (the pool, or the node a user named),
    // never the old embedded node; "Node" words only when a node is the source
    var src = s.source || {};
    if (src.kind === 'node') {
      if (n.state === 'syncing' || n.state === 'starting') return { text: 'Syncing', tone: '' };
      if (src.problem || n.state === 'restarting' || n.state === 'behind' || n.state === 'no peers' || n.state === 'failed' || n.state === 'stopped' || n.state === 'stub') return { text: 'Node unreachable', tone: 'bad' };
    } else if (src.problem && !src.connected && m.state !== 'idle') return { text: 'Pool unreachable', tone: 'bad' };;
    if (m.state === 'waiting') return { text: 'Waiting', tone: '' };
    if (m.state === 'idle') return { text: 'Not mining', tone: '' };
    return { text: cap(m.state || 'starting'), tone: '' };
  }
  // ---- Ember on the card row (miner-ui-4, 6 October 2026): the flame mark's fill, the before -> after watts, the saving,
  //      the sparkline points and the plain label. cd = the card, s = state, now = unix s ----
  // the "before" point (Ember, 6 October 2026): card.tune_before_watts / tune_before_mhs, step 0 of the last FULL plan,
  // kept across confirm plans; until a build carries them, the full plan's step 0 in tune_curve (unlocked, 100%); a
  // confirm or baseline plan without the fields has no before, so no saving is claimed
  function beforeOf(cd) {
    if (cd.tune_before_watts > 0) return { watts: cd.tune_before_watts, mhs: cd.tune_before_mhs || 0 };
    if (cd.tune_before_watts !== undefined) return null;
    var r = (cd.tune_curve || [])[0];
    if (cd.tune_source === 'full' && r && r.watts > 0 && (r.power_pct || 0) >= 100 && !(r.clock_mhz > 0)) return { watts: r.watts, mhs: r.mhs || 0 };
    return null;
  }
  // ---- the core-clock knob's display (knob-24, 7 October 2026; src/ember.rs lock_result, state.rs lock_*): the lock
  //      against the unlocked row at the same cap, the knee in words, the search step, no lever. Display only: the
  //      knob is the engine's; the panel states the rule and never offers a lock below the knee ----
  function mhzOf(note) { var m = /at (\d+) MHz/.exec(note || ''); return m ? parseInt(m[1], 10) : 0; }
  // the stop reason in the panel's words: the knee (rate fell), a faulted row (clocks reset), the floor (no knee), or the bare stop
  function kneeWords(note) {
    note = String(note || ''); var m;
    if ((m = /^rate fell ([\d.]+) percent at (\d+) MHz/.exec(note))) return 'the knee at ' + m[2] + ' MHz, where the rate fell ' + m[1] + '%';
    if ((m = /^fingerprint mismatch at (\d+) MHz/.exec(note))) return 'stopped at ' + m[1] + ' MHz on a hash mismatch, clocks reset to the last good step';
    if ((m = /^the floor at (\d+) MHz/.exec(note))) return 'no knee down to the floor at ' + m[1] + ' MHz';
    return note;
  }
  // {kind: none|nolever|searching|locked|unlocked, text, against, knee, row, savingW, ratePct, kneeMhz, mhz}
  function lockWords(cd) {
    cd = cd || {}; var out = { kind: 'none', text: '', against: '', knee: '', row: '', savingW: 0, ratePct: 0, kneeMhz: 0, mhz: 0 };
    if (cd.lock_note === 'no lever') { out.kind = 'nolever'; out.text = 'No clock lock on this card: the system sets its clocks'; return out; }
    if (cd.lock_steps > 0) { out.kind = 'searching'; out.text = 'Locking clocks: step ' + (cd.lock_step || 1) + ' of ' + cd.lock_steps; out.row = 'locking clocks: step ' + (cd.lock_step || 1) + ' of ' + cd.lock_steps; return out; }
    if (!(cd.lock_at > 0)) return out;
    var note = String(cd.lock_note || ''), fell = /^rate fell ([\d.]+) percent at (\d+) MHz/.exec(note);
    out.kneeMhz = fell ? parseInt(fell[2], 10) : 0; out.knee = kneeWords(note);
    if (!(cd.lock_mhz > 0)) {
      out.kind = 'unlocked';
      out.text = 'Clock left unlocked' + (fell ? ': the first lock step fell ' + fell[1] + '% at ' + fell[2] + ' MHz' : note ? ': ' + kneeWords(note) : '');
      out.row = 'clock unlocked' + (fell ? ', the knee came at ' + fell[2] + ' MHz' : '');
      return out;
    }
    out.kind = 'locked'; out.mhz = cd.lock_mhz;
    out.text = 'Locked at ' + cd.lock_mhz + ' MHz: ' + (cd.lock_mhs || 0).toFixed(1) + ' MH/s at ' + Math.round(cd.lock_w || 0) + ' W (' + (cd.lock_mhw || 0).toFixed(3) + ' MH/W)';
    if (cd.unlocked_w > 0) {
      out.against = 'unlocked at the same cap: ' + (cd.unlocked_mhs || 0).toFixed(1) + ' MH/s at ' + Math.round(cd.unlocked_w) + ' W';
      out.savingW = Math.max(0, Math.round(cd.unlocked_w - (cd.lock_w || 0)));
      out.ratePct = cd.unlocked_mhs > 0 ? Math.max(0, (cd.unlocked_mhs - (cd.lock_mhs || 0)) / cd.unlocked_mhs * 100) : 0;
      out.row = 'the lock: ' + out.savingW + ' W less' + (out.ratePct < 0.05 ? ', no rate lost' : ' for ' + out.ratePct.toFixed(1) + '% of rate') + (out.kneeMhz ? ', knee at ' + out.kneeMhz + ' MHz' : '');
    } else out.row = 'locked at ' + cd.lock_mhz + ' MHz' + (out.kneeMhz ? ', knee at ' + out.kneeMhz + ' MHz' : '');
    return out;
  }
  // ---- the three tuning tiers (tiers-25, 8 October 2026; the founder's definition): Ember tunes a card once (the search at first steady
  //      mining, weekly, or on Tune) and the tiers are rows of that search: efficiency = the best MH/W row outright, balanced = the
  //      best MH/W row within 1% of the top rate (the default, on from install), max = the top-rate row (stock when stock is the top).
  //      A tap applies the known row at once, no re-search. The engine's fields: card.tiers [{id, clock_mhz, power_pct, w, mhs, mhw,
  //      source}], card.tier (in force), card.tier_note (why only stock exists), card.tier_at; settings.tune_tier (the fleet's) ----
  var TIERS = [{ id: 'efficiency', name: 'Efficiency', word: 'the best hashes per watt' }, { id: 'balanced', name: 'Balanced', word: 'the best hashes per watt within 1% of the top rate' }, { id: 'max', name: 'Maximum', word: 'the top rate' }];
  var DEFAULT_PRICE_MINOR = 25; // 0.25 in the currency in force, until the user sets a price (main's order, 8 October 2026)
  function tierOf(id) { id = String(id || ''); if (id === 'rate' || id === 'max_rate' || id === 'maximum') return 'max'; for (var i = 0; i < TIERS.length; i++) if (TIERS[i].id === id) return id; return 'balanced'; }
  function tierName(id) { for (var i = 0; i < TIERS.length; i++) if (TIERS[i].id === id) return TIERS[i].name; return 'Balanced'; }
  function priceOrDefault(pence) { return pence > 0 ? pence : DEFAULT_PRICE_MINOR; }
  // the class flip (8 October 2026): the stored tiers were measured under one program class and the card now runs another
  // (card.tiers_class against card.program_class); the engine re-runs the search within ten minutes (card.tiers_remeasure_at)
  function classFlip(cd) { return !!(cd && cd.tiers_class && cd.program_class && String(cd.tiers_class) !== String(cd.program_class)); }
  function minuteOf(t) { var d = new Date(t * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0'); }
  function remeasureWords(cd) { return 'Re-measuring for class ' + cd.program_class + ', ' + (cd.tiers_remeasure_at > 0 ? 'started ' + minuteOf(cd.tiers_remeasure_at) : 'queued (within ten minutes of the crossing)') + '; the class ' + cd.tiers_class + ' points are out of date'; }
  // the knee note under the table: the measured loss at the same lock under the new class, shown over 2 percent
  function kneeNote(cd) { return cd && cd.knee_loss_pct > 2 && cd.program_class ? 'The knee is being re-found under class ' + cd.program_class + ': ' + Number(cd.knee_loss_pct).toFixed(1) + '% less rate at the same lock than under class ' + (cd.tiers_class || 'the last one') + '.' : ''; }
  function rateText(mhs) { return mhs >= 100 ? mhs.toFixed(1) : mhs.toFixed(1); }
  // app-ia-26 (A7): the saving reads against stock, in watts, never as money and never as "saves" (which read as a saving against now)
  function savingText(saveW) { return saveW > 0 ? Math.round(saveW) + ' W under stock' : 'stock'; }
  // {kind: none|nolever|measuring|unmeasured|ready, note, inForce, lock, stock: {w, mhs}|null, tiers: [{id, name, word, line, sub, on, mhz, w, mhs, mhw, saveW, source}]}
  function tierSet(cd, s, pence) {
    cd = cd || {}; var st = (s && s.settings) || {};
    var out = { kind: 'none', note: '', inForce: '', lock: '', stock: null, tiers: [], knee: '', classFlip: false };
    if (!tunable(cd) || !Array.isArray(cd.tiers)) return out;
    out.knee = kneeNote(cd);
    var fleetTier = tierOf(st.tune_tier || st.tune_goal || 'balanced'), inForce = cd.tier ? tierOf(cd.tier) : '';
    if (cd.tier_note) {
      out.kind = 'nolever'; out.note = cap(String(cd.tier_note)); out.inForce = 'max';
      var se = cd.tiers.filter(function (t) { return t && tierOf(t.id) === 'max'; })[0] || null;
      var sw = se && se.w > 0 ? se.w : 0, sm = se && se.mhs > 0 ? se.mhs : 0, smw = se && se.mhw > 0 ? se.mhw : (sw > 0 && sm > 0 ? sm / sw : 0);
      out.tiers = [{ id: 'max', name: 'Stock', word: 'as the card runs', line: sw > 0 && sm > 0 ? rateText(sm) + ' MH/s · ' + Math.round(sw) + ' W' : 'as it runs', sub: (smw > 0 ? smw.toFixed(3) + ' MH/W' : '') + (se && se.table === true ? (smw > 0 ? ' · table' : 'table') : ''), on: true, mhz: 0, w: sw, mhs: sm, mhw: smw, saveW: 0, source: 'stock' }];
      out.stock = sw > 0 ? { w: sw, mhs: sm } : null;
      return out;
    }
    if (classFlip(cd)) {
      // never the old class's watts as current: every button reads the re-measure, the tier in force stays marked
      out.kind = 'remeasuring'; out.classFlip = true; out.note = remeasureWords(cd); out.inForce = inForce || fleetTier;
      out.tiers = TIERS.map(function (t) { return { id: t.id, name: t.name, word: t.word, line: 're-measuring for class ' + cd.program_class, sub: '', on: t.id === out.inForce, mhz: 0, w: 0, mhs: 0, mhw: 0, saveW: 0, source: '' }; });
      return out;
    }
    if (!cd.tiers.length) {
      var measuring = cd.state === 'tuning' || cd.sweep_state === 'running';
      out.kind = measuring ? 'measuring' : 'unmeasured';
      out.note = measuring ? 'Measuring' + (cd.tune_steps > 0 ? ': step ' + (cd.tune_step || 1) + ' of ' + cd.tune_steps : '') : (st.worker_layer || (s && s.worker_layer)) && cd.vendor === 'apple' ? 'Nothing to tune: macOS drives the card' : (st.sweep === false ? 'Not measured yet: Ember Tune is off in Settings' : 'Not measured yet: the search runs 2 min into steady mining');
      out.tiers = TIERS.map(function (t) { return { id: t.id, name: t.name, word: t.word, line: measuring ? 'measuring' : 'not measured yet', sub: '', on: t.id === (inForce || fleetTier), mhz: 0, w: 0, mhs: 0, mhw: 0, saveW: 0, source: '' }; });
      out.inForce = inForce || fleetTier;
      return out;
    }
    var by = {}; cd.tiers.forEach(function (t) { if (t && t.id) by[tierOf(t.id)] = t; });
    var stock = by.max || null; out.stock = stock ? { w: stock.w || 0, mhs: stock.mhs || 0 } : null;
    // tiers-table-26: rows the team's table seeded (table: true) until this card's own search runs
    var fromTable = cd.tiers.some(function (t) { return t && t.table === true; });
    out.kind = fromTable ? 'table' : 'ready'; out.inForce = inForce || fleetTier;
    if (fromTable) {
      out.tableLabel = cd.tiers.some(function (t) { return t && t.label === 'estimated'; }) ? 'estimated' : 'measured';
      out.note = 'From the team’s table, ' + (out.tableLabel === 'measured' ? 'measured on this card class' : 'estimated for this card class') + '; Ember measures this card ' + (st.sweep === false ? 'when you press Tune' : '2 min into steady mining') + '.';
    }
    out.tiers = TIERS.map(function (t) {
      var r = by[t.id]; if (!r) return { id: t.id, name: t.name, word: t.word, line: 'not in this search', sub: '', on: t.id === out.inForce, mhz: 0, w: 0, mhs: 0, mhw: 0, saveW: 0, source: '' };
      var w = r.w || 0, mhs = r.mhs || 0, mhw = r.mhw > 0 ? r.mhw : (w > 0 ? mhs / w : 0), saveW = stock && stock.w > w ? Math.round(stock.w - w) : 0;
      return { id: t.id, name: t.name, word: t.word, line: rateText(mhs) + ' MH/s · ' + Math.round(w) + ' W', sub: mhw.toFixed(3) + ' MH/W · ' + savingText(saveW, pence) + (r.table === true ? ' · table' : ''), on: t.id === out.inForce, mhz: r.clock_mhz || 0, w: w, mhs: mhs, mhw: mhw, saveW: saveW, source: r.source || '', note: r.note || '', label: r.label || '' };
    });
    if (fromTable) {
      var rowIn = by[out.inForce];
      out.lock = rowIn ? 'the ' + out.inForce + ' row: ' + (rowIn.clock_mhz > 0 ? rowIn.clock_mhz + ' MHz, ' : 'unlocked, ') + Math.round(rowIn.w || 0) + ' W' + (stock && stock.w > 0 ? '; stock ' + Math.round(stock.w) + ' W' : '') : '';
      return out;
    }
    var capW = cd.lock_w > 0 ? cd.lock_w : (by[out.inForce] ? by[out.inForce].w : 0);
    out.lock = (cd.lock_mhz > 0 ? 'locked at ' + cd.lock_mhz + ' MHz' : 'unlocked') + (capW > 0 ? ', ' + Math.round(capW) + ' W' : '') + (stock && stock.w > 0 ? '; stock ' + Math.round(stock.w) + ' W' : '');
    return out;
  }
  // ---- app-ia-26 (the founder's audit, section 4.2): the Tune page. One line per card in place of the Tuned, Locked and tune
  //      lines; the cap as one sentence; the power sentence when NVIDIA is measure-only; the card's html, built here so the
  //      once-tests read it with no DOM ----
  function tuneLine(cd, s, now) {
    cd = cd || {}; var st = (s && s.settings) || {};
    if (st.tuning_off) return endDot(st.tuning_note || 'Tuning paused fleet-wide by the signed manifest');
    if (!tunable(cd)) return cd.kind === 'integrated' ? 'A built-in GPU is not tuned: it mines as the system runs it.' : 'Ember cannot tune this card.';
    if (cd.state === 'tuning' || cd.sweep_state === 'running') return 'Measuring' + (cd.tune_steps > 0 ? ': step ' + (cd.tune_step || 1) + ' of ' + cd.tune_steps : '') + (cd.tune_eta_s > 0 ? ', ' + tuneEta(cd.tune_eta_s).replace(/^about /, '') + ' left' : '') + '.';
    if (classFlip(cd)) return 'Re-measuring for class ' + cd.program_class + ', ' + (cd.tiers_remeasure_at > 0 ? 'started ' + minuteOf(cd.tiers_remeasure_at) : 'queued within ten minutes') + '.';
    if (cd.tier_note || cd.lock_note === 'no lever') return 'No lever on ' + (cd.vendor === 'apple' ? 'Apple silicon' : 'this card') + ': the system sets the clocks. Ember measures and reports.';
    var ts = tierSet(cd, s, 0);
    if (ts.kind === 'table') return 'From the team’s table, ' + (ts.tableLabel === 'measured' ? 'measured on this card class' : 'estimated for this card class') + '.';
    // the cap is the slider's want (power_default_w × pct), never the draw (cd.lock_w is what the card draws at the lock);
    // F-E18: it reads as set only when the driver read it back, else the request and the reason (capWords)
    var lk = lockWords(cd), capW = capWords(cd), knee = lk.kneeMhz > 0 ? ' Knee at ' + withCommas(lk.kneeMhz) + ' MHz.' : '';
    if (lk.kind === 'searching') return 'Locking clocks: step ' + (cd.lock_step || 1) + ' of ' + cd.lock_steps + '.';
    if (lk.kind === 'locked') return 'Locked at ' + withCommas(cd.lock_mhz) + ' MHz' + capW + '.' + knee;
    if (lk.kind === 'unlocked') return 'Clock unlocked' + capW + '.' + knee;
    if (cd.tune_line && (cd.tune_source === 'baseline' || !cd.tune_control)) return 'Measured as it runs' + (cd.sweep_note ? ': ' + String(cd.sweep_note).replace(/^measure only /, '') : '') + '.';
    if (cd.tune_line) return 'Tuned' + (cd.sweep_at > 0 && now ? ' ' + ago(now - cd.sweep_at) : '') + capW + '.';
    if (cd.sweep_note && /^tuning stopped/i.test(cd.sweep_note)) return endDot(cap(cd.sweep_note));
    // the worker layer tunes NVIDIA and AMD from the card's own stint (0.2.22); Apple silicon reports no power, so nothing to tune there
    if ((st.worker_layer || (s && s.worker_layer)) && cd.vendor === 'apple') return 'Nothing to tune: macOS drives the card.' + (cd.hash_now > 0 ? ' It mines at ' + laneRateText(cd.hash_now * 1e6, cd.lane_word) + '.' : '');
    return st.sweep === false ? 'Not measured yet: Ember Tune is off in Settings.' : 'Not measured yet: the search runs 2 min into steady mining.';
  }
  function endDot(t) { t = String(t || ''); return /[.!?]$/.test(t) ? t : t + '.'; }
  // F-E35: the chain card's eyebrow: the mined chain without a public feed, an unreadable feed with its error, or live
  function dagStateWords(fc, sc, feedName, feedErr) {
    fc = Number(fc || 0); sc = Number(sc || 0);
    if (sc > 0 && fc > 0 && fc !== sc) return 'No public chain feed for ' + chainWord(sc) + ', the chain this machine mines; the stage shows nothing';
    if (feedErr) return 'Chain feed not read: ' + String(feedErr).replace(/^the chain feed is not read: /, '');
    return 'Live: ' + chainWord(fc, feedName) + (fc > 0 && sc > 0 && fc === sc ? ', the chain this machine mines' : '');
  }
  // F-E21: one line of the ledger's totals for the Mine row: "N accepted · P paid · C carried · R lost to a race";
  // "carried" is in the carry or pooled (accepted by a sealer, not yet in a block); "lost to a race" is a share
  // carried in a block that lost its race (the chain never pays it), the pool's "expired by reorganisation";
  // "expired at the window" is named only when there are any. Empty until the ledger's totals are read.
  function mineShareWords(tot, lastPaid) {
    tot = tot || {};
    var n = tot.accepted || 0;
    if (!(n > 0) || tot.paid == null || tot.expired_reorg == null) return '';
    var carried = (tot.in_carry || 0) + (tot.pooled || 0), lost = tot.expired_reorg || 0, win = tot.expired_window || 0;
    var parts = [withCommas(n) + ' accepted', withCommas(tot.paid || 0) + ' paid' + (lastPaid && lastPaid.height ? ' (last in block ' + withCommas(lastPaid.height) + ')' : '')];
    if (carried > 0) parts.push(withCommas(carried) + ' carried');
    parts.push(withCommas(lost) + ' lost to a race' + (n > 0 && lost > 0 ? ' (' + Math.round(lost / n * 100) + '%)' : ''));
    if (win > 0) parts.push(withCommas(win) + ' expired at the window');
    return parts.join(' · ');
  }
  // F-E18 (14 October 2026): the row's cap words. ", cap N W" only when the driver read the cap back (power_applied);
  // otherwise ", cap N W requested, not applied: <the reason>" from the engine's note, so a cap the box never set is
  // never read as set (the E1 study's silent no-op). Empty with no cap slider.
  function capReason(cd) {
    var n = String(cd.power_note || '').trim();
    if (!n) return cd.vendor === 'nvidia' && isWindowsHost() ? 'Windows needs the administrator prompt' : 'the driver has not read it back';
    var m = /^power cap[^:(]*\(([^)]*)\):?\s*(.*)$/i.exec(n);
    if (m) return m[1] + (m[2] ? '; ' + m[2] : '');
    return n.replace(/^power cap (not applied|not set|NOT applied):?\s*/i, '').replace(/^power cap:?\s*/i, '');
  }
  function isWindowsHost() { try { return /windows/i.test(String((window.navigator && window.navigator.userAgent) || '')); } catch (e) { return false; } }
  function capWords(cd) {
    cd = cd || {};
    var capW = cd.power_default_w > 0 ? Math.round(cd.power_default_w * (cd.power_pct || 80) / 100) : 0;
    if (!(capW > 0)) return '';
    if (cd.power_applied) return ', cap ' + capW + ' W';
    return ', cap ' + capW + ' W requested, not applied: ' + capReason(cd);
  }
  // A11: one sentence for the cap and the draw (NVIDIA; the slider sits beside it)
  function capSentence(cd) {
    cd = cd || {}; if (!(cd.power_default_w > 0)) return '';
    var pct = cd.power_pct || 80, capW = Math.round(cd.power_default_w * pct / 100);
    if (!cd.power_applied) return 'Cap ' + capW + ' W (' + pct + '%), not set yet: Windows needs the administrator prompt.';
    var draw = cd.lock_mhz > 0 && cd.lock_w > 0 ? Math.round(cd.lock_w) : (cd.power_w > 0 ? Math.round(cd.power_w) : 0);
    return 'Cap ' + capW + ' W (' + pct + '%).' + (draw > 0 ? ' The card draws ' + draw + ' W' + (cd.lock_mhz > 0 && cd.lock_w > 0 ? ' at the lock' : '') + '.' : '');
  }
  // R3: the one sentence on Tune when NVIDIA cards are measured and never set
  function powerNote(s) {
    var st = (s && s.settings) || {}, cards = shownCards(((s && s.mining) || {}).cards);
    return !st.power_control && cards.some(function (c) { return c.vendor === 'nvidia' && present(c); }) ? 'Power control is off, so NVIDIA cards are measured and never set. Turn it on in Settings.' : '';
  }
  var MINERS_SENTENCE = 'Each one votes and is paid on its own. 8 for a big card, 2 for a small one, 1 for a built-in\u00a0GPU.'; // the last two words glued (copy law 18)
  var CURVE_CAPTION = 'Hash rate against power for every step of the last measurement. The ringed point is the one Ember kept; a hollow point was rejected; the dashed point is the knee.';
  function tierButtonsHtml(tiers, label) {
    return (tiers || []).map(function (t) { return '<button class="tier-b' + (t.on ? ' on' : '') + '" data-tier="' + escH(t.id) + '" role="radio" aria-checked="' + (t.on ? 'true' : 'false') + '" title="' + escH(t.note ? t.word + '. ' + t.note : t.word) + '"><span class="n">' + escH(t.name) + '</span><span class="l">' + escH(t.line) + '</span>' + (t.sub ? '<span class="s">' + escH(t.sub) + '</span>' : '') + '</button>'; }).join('');
  }
  function curveHtml(points) {
    points = points || []; if (points.length < 2) return '';
    var ws = points.map(function (p) { return p.w; }), ms = points.map(function (p) { return p.mhs; });
    var w0 = Math.min.apply(null, ws), w1 = Math.max.apply(null, ws), m0 = Math.min.apply(null, ms), m1 = Math.max.apply(null, ms), W = 320, H = 110, L = 34, B = 18;
    var X = function (w) { return L + (w1 > w0 ? (w - w0) / (w1 - w0) : 0.5) * (W - L - 8); }, Y = function (m) { return 6 + (m1 > m0 ? 1 - (m - m0) / (m1 - m0) : 0.5) * (H - B - 6); };
    var sorted = points.slice().sort(function (a, b) { return a.w - b.w; });
    var path = sorted.map(function (p, i) { return (i ? 'L' : 'M') + X(p.w).toFixed(1) + ' ' + Y(p.mhs).toFixed(1); }).join(' ');
    var dots = sorted.map(function (p) { return '<circle class="' + (p.chosen ? 'chosen' : p.marked ? 'marked' : p.knee ? 'knee' : '') + '" cx="' + X(p.w).toFixed(1) + '" cy="' + Y(p.mhs).toFixed(1) + '" r="' + (p.chosen ? 5 : p.knee ? 4 : 3) + '"/>'; }).join('');
    return '<div class="curve-wrap"><svg class="curve-svg" viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="The measurement\'s curve: watts across, hash rate up"><path class="ln" d="' + path + '"/>' + dots +
      '<text x="' + L + '" y="' + (H - 4) + '">' + Math.round(w0) + ' W</text><text x="' + (W - 8) + '" y="' + (H - 4) + '" text-anchor="end">' + Math.round(w1) + ' W</text>' +
      '<text x="2" y="' + (H - B - 2) + '">' + m0.toFixed(0) + '</text><text x="2" y="12">' + m1.toFixed(0) + '</text></svg><span class="note">' + CURVE_CAPTION + '</span></div>';
  }
  // the driver strip on a card (as today): {offer|running|reboot|error|note} with its button
  function driverStripHtml(cd, s) {
    var dw = driverWords(cd, s);
    if (dw.kind === 'none' || dw.kind === 'fine') return '';
    return '<div class="gl-driver ' + dw.kind + '">' + (dw.kind === 'running' ? '<i class="bar"><b style="width:' + Math.round((dw.progress || 0) * 100) + '%"></b></i>' : '') + '<span class="t">' + escH(dw.text) + '</span>' +
      (dw.button === 'install' ? '<button class="btn small primary" data-driver-install="' + escH(cd.driver_offer.vendor) + '">Install</button>' : dw.button === 'restart' ? '<button class="btn small primary" data-driver-restart="1">Restart now</button>' : '') +
      (dw.sub ? '<span class="n dim">' + escH(dw.sub) + '</span>' : '') + (dw.dry ? '<span class="n dim">dry run: the installer is not run</span>' : '') + '</div>';
  }
  // one card per graphics card on Tune: the head (mark, name, the live numbers, the one button), the three tiers, the one
  // line, a closed Details (the curve, the cap, the miners, the telemetry, the facts), the driver strip
  function tuneCardHtml(cd, s, now) {
    cd = cd || {}; var r = cardRow(cd), e = effOf(cd), live = [];
    if (r.hash) live.push(r.hash + ' ' + (r.hashUnit || 'MH/s')); if (r.power) live.push(r.power); if (e > 0) live.push(effText(e));
    var measuring = cd.state === 'tuning' || cd.sweep_state === 'running', can = tunable(cd) && !((s && s.settings) || {}).tuning_off;
    var btn = !can ? '' : measuring ? '<button class="btn small" data-stop="1">Stop</button>' : '<button class="btn small ghost" data-tune="1">' + (cd.sweep_at > 0 || cd.tune_line ? 'Retune' : 'Tune') + '</button>';
    var ts = tierSet(cd, s, 0), tiers = ts.kind === 'none' ? '' : '<div class="tc-tiers"><div class="tiers small" data-card-tier="1" role="radiogroup" aria-label="Tier for ' + escH(cd.name) + '">' + tierButtonsHtml(ts.tiers) + '</div></div>';
    var line = '<p class="tc-line">' + escH(tuneLine(cd, s, now)) + (ts.knee ? ' <span class="knee">' + escH(ts.knee) + '</span>' : '') + '</p>';
    // Details
    var d = '', em = ember(cd, s, now);
    d += curveHtml(em.points);
    if (cd.vendor === 'nvidia' && cd.power_default_w > 0) {
      var pct = cd.power_pct || 80;
      d += '<div class="ctl"><span class="k">Power cap</span><input type="range" min="50" max="100" step="5" value="' + pct + '" aria-label="Power cap for ' + escH(cd.name) + '"><span class="pv mono">' + pct + '%</span>' +
        '<p class="help">' + escH(capSentence(cd)) + ' Lower is cooler and quieter.' + (cd.pinned ? ' <button class="btn tiny ghost" data-pin="0">Back to Ember’s choice</button>' : '') + (!cd.power_applied ? ' <button class="btn tiny" data-power-retry="1">Retry</button>' : '') + '</p></div>';
    } else if (cd.vendor === 'apple') d += '<p class="help">Apple silicon manages its own power. There is no cap to set.</p>';
    else if (cd.vendor === 'intel') d += '<p class="help">Intel Arc exposes no power cap through OpenCL; the card runs at the driver’s default limit.</p>';
    d += '<div class="ctl"><span class="k">Miners on this card</span><span></span><div class="ids"><button type="button" data-d="-1" aria-label="fewer miners">&minus;</button><input type="number" min="1" max="64" value="' + (cd.identities || 1) + '" aria-label="Miners on ' + escH(cd.name) + '"><button type="button" data-d="1" aria-label="more miners">+</button></div><p class="help">' + MINERS_SENTENCE + '</p></div>';
    var tl = [];
    if (cd.temp_mem > 0) tl.push('memory ' + Math.round(cd.temp_mem) + ' °C');
    if (cd.gclk_mhz > 0) tl.push('core ' + withCommas(cd.gclk_mhz) + ' MHz' + (cd.clock_cap_mhz > 0 ? ' (cap ' + withCommas(cd.clock_cap_mhz) + ')' : ''));
    if (cd.mclk_mhz > 0) tl.push('memory clock ' + withCommas(cd.mclk_mhz) + ' MHz' + (cd.mem_cap_mhz > 0 ? ' (set ' + withCommas(cd.mem_cap_mhz) + ')' : ''));
    if (cd.fan_pct > 0) tl.push('fan ' + Math.round(cd.fan_pct) + '%');
    if (tl.length) d += '<p class="line mono">' + escH(tl.join(' · ')) + '</p>';
    var facts = [cd.worker ? 'runs on ' + cd.worker : '', cd.detail ? String(cd.detail).replace(/^(\d+) cores$/, function (m, n) { return withCommas(n) + ' cores'; }) : '', cardTitle(cd), cd.driver_os ? 'driver ' + cd.driver_os : cd.driver ? 'driver ' + cd.driver : ''].filter(Boolean);
    if (cd.driver_offer && cd.driver_offer.status === 'fine') d += '<p class="help">' + escH(cd.driver_offer.text) + '</p>';
    if (facts.length) d += '<p class="note mono small">' + escH(facts.join(' · ')) + '</p>';
    var details = '<details class="tc-details"><summary>Details</summary><div class="gl-details">' + d + '</div></details>';
    return '<div class="gpu-line tune-card' + (r.on ? ' on' : ' off') + (r.removed ? ' removed' : '') + (r.unusable ? ' unusable' : '') + '" data-key="' + escH(r.key) + '">' +
      '<div class="gl-main"><div class="badge-slot">' + markHtml(cd) + '</div><div class="who">' + nameHtml(cd) + '<span class="st ' + r.tone + '"><i></i>' + escH(r.word) + (live.length ? ' · ' + escH(live.join(' · ')) : '') + '</span></div><div class="acts">' + btn + '</div></div>' +
      tiers + line + details + driverStripHtml(cd, s) + '</div>';
  }
  // the strip: the measured cards summed per tier, the tier in force fleet-wide, how many cards are measured
  // {inForce, measured, tunable, count, tiers: [{id, name, word, line, sub, on, w, mhs, saveW}]}
  function fleetTiers(cards, s, pence) {
    var st = (s && s.settings) || {}, inForce = tierOf(st.tune_tier || st.tune_goal || 'balanced');
    var list = shownCards(cards).filter(tunable), sets = list.map(function (c) { return tierSet(c, s, pence); }), ready = sets.filter(function (t) { return t.kind === 'ready' || t.kind === 'table'; });
    var measured = sets.filter(function (t) { return t.kind === 'ready' || (t.kind === 'nolever' && t.stock && !(t.tiers[0] && /table/.test(t.tiers[0].sub))); }).length;
    var fromTable = sets.filter(function (t) { return t.kind === 'table' || (t.kind === 'nolever' && t.tiers[0] && /table/.test(t.tiers[0].sub)); }).length;
    var out = { inForce: inForce, measured: measured, fromTable: fromTable, tunable: list.length, count: measured + ' of ' + list.length + ' card' + (list.length === 1 ? '' : 's') + ' measured' + (fromTable ? ', ' + fromTable + ' from the team’s table' : ''), tiers: [] };
    out.tiers = TIERS.map(function (t) {
      var w = 0, mhs = 0, saveW = 0, n = 0;
      ready.forEach(function (ts) { ts.tiers.forEach(function (x) { if (x.id === t.id && x.w > 0) { w += x.w; mhs += x.mhs; saveW += x.saveW; n++; } }); });
      var flip = sets.filter(function (x) { return x.kind === 'remeasuring'; })[0] || null;
      return { id: t.id, name: t.name, word: t.word, line: n ? rateText(mhs) + ' MH/s · ' + Math.round(w) + ' W' : (flip ? flip.tiers[0].line : sets.some(function (x) { return x.kind === 'measuring'; }) ? 'measuring' : 'not measured yet'), sub: n ? savingText(saveW, pence) : '', on: t.id === inForce, w: w, mhs: mhs, saveW: saveW };
    });
    return out;
  }
  // the strip's one sentence: what Ember Tune is doing, did, or will do
  function tierSentence(cards, s, now) {
    var st = (s && s.settings) || {}, list = shownCards(cards).filter(tunable), tc = tuningCard(list);
    if (st.tuning_off) return st.tuning_note || 'Tuning paused fleet-wide by the signed manifest.';
    if (!list.length) return 'No card here can be tuned.';
    var flipped = list.filter(classFlip);
    if (flipped.length) {
      var startAt = 0; flipped.forEach(function (c) { if (c.tiers_remeasure_at > 0 && (!startAt || c.tiers_remeasure_at < startAt)) startAt = c.tiers_remeasure_at; });
      return 'Class ' + flipped[0].program_class + ' arrived: re-measuring ' + (flipped.length === 1 ? flipped[0].name : flipped.length + ' cards') + ' for ' + (flipped.length === 1 ? 'its' : 'their') + ' new points, ' + (startAt ? 'started ' + minuteOf(startAt) : 'queued within ten minutes') + '. The class ' + flipped[0].tiers_class + ' tiers are out of date until then.';
    }
    if (tc) { var step = tc.tune_steps > 0 ? ': step ' + (tc.tune_step || 1) + ' of ' + tc.tune_steps : ''; return 'Measuring ' + tc.name + step + (tc.tune_eta_s > 0 ? ', mining again in ' + tuneEta(tc.tune_eta_s) : '') + '.'; }
    if (st.sweep === false) return 'Ember Tune is off. The tiers still apply when you tap them.';
    if (list.every(function (c) { return !!c.tier_note; })) return 'No lever on this machine: ' + String(list[0].tier_note).replace(/^no lever:\s*/i, '') + '. Ember measures and reports.';
    var tier = tierOf(st.tune_tier || st.tune_goal || 'balanced'), last = null, n = 0;
    list.forEach(function (c) { if (c.sweep_at > 0) { n++; if (!last || c.sweep_at > last.sweep_at) last = c; } });
    if (last) return tierName(tier) + ' on ' + n + ' card' + (n === 1 ? '' : 's') + '. Measured ' + ago(now - last.sweep_at) + ', ' + nextCheck(last.sweep_at, st.tune_period_s, now) + '.';
    var tabled = list.some(function (c) { return (c.tiers || []).some(function (t) { return t && t.table === true; }); });
    if (tabled) return 'Ember Tune is on at ' + tierName(tier) + ', set from the team’s table for this card class. The first tune runs 2 min into steady mining and measures this card’s own points.';
    return 'Ember Tune is on at ' + tierName(tier) + '. The first tune runs 2 min into steady mining and sets each card’s ' + tierName(tier).toLowerCase() + ' point.';
  }
  // {kind: none|idle|measured|running|tuned|stopped|paused, fill: 0..1, label, sub, before, after, savingW, savingPct, savingText, next, points, lock}
  function ember(cd, s, now) {
    var tw = tuneWords(cd, s, now), out = { kind: tw.kind, fill: 0, label: '', sub: '', before: 0, after: 0, savingW: 0, savingPct: 0, savingText: '', next: '', points: [], lock: lockWords(cd) };
    if (tw.kind === 'none') return out;
    var st = (s && s.settings) || {};
    out.next = nextCheck(cd.sweep_at, st.tune_period_s, now).replace(/^next check /, '');
    var kneeMhz = out.lock.kneeMhz;
    out.points = (cd.tune_curve || []).filter(function (r) { return r.watts > 0 && r.mhs > 0; }).map(function (r) { return { w: r.watts, mhs: r.mhs, eff: r.eff || (r.mhs / r.watts), marked: !!r.mark, chosen: cd.sweep_watts > 0 && Math.abs(r.watts - cd.sweep_watts) < 0.5 && Math.abs((r.mhs || 0) - (cd.sweep_mhs || 0)) < 0.05, knee: kneeMhz > 0 && r.clock_mhz === kneeMhz }; });
    if (cd.tune_floor) out.floor = true;
    out.heat = heatRowWords(s && s.heat);
    if (tw.kind === 'running') { out.fill = tw.progress >= 0 ? tw.progress : 0.1; out.label = cd.tune_steps > 0 ? 'Tuning: step ' + (cd.tune_step || 1) + ' of ' + cd.tune_steps : 'Tuning'; out.sub = [out.lock.kind === 'searching' ? out.lock.row : '', cd.tune_eta_s > 0 ? tuneEta(cd.tune_eta_s) + ' left' : ''].filter(Boolean).join(' · '); return out; }
    if (tw.kind === 'paused') { out.label = 'Tuning paused by Igneum'; out.sub = ''; return out; }
    if (tw.kind === 'stopped') { out.label = tw.text; return out; }
    if (tw.kind === 'measured') { out.fill = 0.5; out.label = 'Measured, not tuned'; out.sub = /Power control/i.test(cd.sweep_note || '') ? 'Power control is off' : cd.vendor === 'apple' ? 'Apple silicon sets its own power' : (tw.note || '').replace(/^Measured only /, ''); return out; }
    if (tw.kind === 'tuned') {
      out.fill = 1; var when = cd.sweep_at > 0 && now ? ago(now - cd.sweep_at) : '';
      out.label = 'Tuned' + (when ? ' ' + when : '') + (cd.tune_source === 'confirm' ? ' from what other miners found' : '');
      out.sub = tunePoint(cd) + (cd.tune_floor ? ' (the lowest clock Ember will set)' : '') + (out.lock.row ? ' · ' + out.lock.row : '') + (out.next ? ' · next check ' + out.next : '');
      var b = beforeOf(cd); out.after = cd.sweep_watts > 0 ? cd.sweep_watts : 0; out.before = b ? b.watts : 0;
      if (out.before > 0 && out.after > 0 && out.before > out.after) {
        out.savingW = Math.round(out.before - out.after);
        out.savingPct = b.mhs > 0 && cd.sweep_mhs > 0 ? Math.max(0, (b.mhs - cd.sweep_mhs) / b.mhs * 100) : 0;
        out.savingText = 'saves ' + out.savingW + ' W' + (b.mhs > 0 ? ', ' + (out.savingPct < 0.05 ? 'no' : out.savingPct.toFixed(2) + '% of') + ' rate' : '');
      }
      return out;
    }
    out.label = st.sweep === false ? 'Not tuned: Ember Tune is off' : 'Not tuned yet'; out.sub = st.sweep === false ? 'Tune starts one by hand' : 'starts after 2 min of steady mining'; return out;
  }
  // ---- Ember Heat (mission item 7, 7 October 2026; docs/plans/ember-heat.md): the region table and the price prompt,
  //      the restricted line, the cost-against-rent line, the heat words ----
  // The prices are the public table's rows (docs/analysis/mission/future.md 4.1: Ofgem, Eurostat, EIA), typed into the
  // app as defaults the miner edits; nothing is fetched. The dollar rates are the ones future.md 3.4 implies (USD 1.32 per
  // pound, 1.08 per euro), approximate. The restricted entries are future.md 4.4 and 8.3 item 7: Russia (ten regions, the
  // Moscow region, the seasonal three) and China. Nothing is added to that list here.
  var RESTRICTED_SENTENCE = 'Mining may be restricted where you are. You are responsible for checking.';
  var CURRENCIES = { GBP: { code: 'GBP', symbol: '£', minor: 'p', usd: 1.32 }, EUR: { code: 'EUR', symbol: '€', minor: 'c', usd: 1.08 }, USD: { code: 'USD', symbol: '$', minor: 'c', usd: 1 } };
  var EUROSTAT = 'Eurostat, second half of 2025';
  var REGIONS = [
    { code: 'gb', name: 'United Kingdom', cur: 'GBP', price: 26.32, source: 'Ofgem cap, 1 October to 31 December 2026' },
    { code: 'ie', name: 'Ireland', cur: 'EUR', price: 40.42, source: EUROSTAT },
    { code: 'de', name: 'Germany', cur: 'EUR', price: 38.69, source: EUROSTAT },
    { code: 'be', name: 'Belgium', cur: 'EUR', price: 34.99, source: EUROSTAT },
    { code: 'eu', name: 'European Union, other', cur: 'EUR', price: 28.96, source: EUROSTAT + ', the EU average' },
    { code: 'hu', name: 'Hungary', cur: 'EUR', price: 10.82, source: EUROSTAT + ', in euro cents' },
    { code: 'mt', name: 'Malta', cur: 'EUR', price: 12.82, source: EUROSTAT },
    { code: 'bg', name: 'Bulgaria', cur: 'EUR', price: 13.55, source: EUROSTAT + ', in euro cents' },
    { code: 'us', name: 'United States', cur: 'USD', price: 17.7, source: 'EIA average, second half of 2025' },
    { code: 'kz', name: 'Kazakhstan', cur: 'USD', price: 4, source: 'about 4 c, a secondary source, approximate' },
    { code: 'py', name: 'Paraguay', cur: 'USD', price: 4.4, source: 'ANDE crypto tariff, 4.4 to 6 c; every crypto contract ends 31 December 2027' },
    { code: 'ir', name: 'Iran', cur: 'USD', price: 1, source: 'licensed mining tariff, about 1 c, a secondary source, approximate' },
    { code: 'et', name: 'Ethiopia', cur: 'USD', price: 2, source: '2 to 5.3 c, a secondary source' },
    { code: 'ng', name: 'Nigeria', cur: 'USD', price: 4.8, source: 'hosted, 4.8 c, a secondary source' },
    { code: 'ru-mow', name: 'Russia, Moscow region', cur: 'USD', price: 0, restricted: 'Mining is banned in the Moscow region from 15 August 2026 to 2032.' },
    { code: 'ru-irk', name: 'Russia, Irkutsk', cur: 'USD', price: 0, restricted: 'Irkutsk has a seasonal mining ban.' },
    { code: 'ru-bur', name: 'Russia, Buryatia', cur: 'USD', price: 0, restricted: 'Buryatia has a seasonal mining ban.' },
    { code: 'ru-zab', name: 'Russia, Zabaikalsky', cur: 'USD', price: 0, restricted: 'Zabaikalsky has a seasonal mining ban.' },
    { code: 'ru', name: 'Russia, other region', cur: 'USD', price: 0, restricted: 'Mining is banned in ten regions from 1 January 2025 to 15 March 2031, in the Moscow region from 15 August 2026, and seasonally in Irkutsk, Buryatia and Zabaikalsky. This app does not carry the list of ten.' },
    { code: 'cn', name: 'China', cur: 'USD', price: 0, restricted: 'Mining is illegal in China. The joint Notice of 6 February 2026 reaffirmed it.' },
    { code: 'other', name: 'Somewhere else', cur: 'USD', price: 0 }
  ];
  var synthRegions = {};
  function regionOf(code) {
    for (var i = 0; i < REGIONS.length; i++) if (REGIONS[i].code === code) return REGIONS[i];
    if (!/^[a-z]{2}$/.test(code || '')) return null;
    if (synthRegions[code]) return synthRegions[code];
    var cur = ISO_CURRENCY[code] || '', eu = EU_MEMBERS.indexOf(code) >= 0 && cur === 'EUR', avg = null;
    for (var j = 0; j < REGIONS.length; j++) if (REGIONS[j].code === 'eu') avg = REGIONS[j];
    return (synthRegions[code] = { code: code, name: regionName(code), cur: cur || 'USD', unknown: !cur, price: eu && avg ? avg.price : 0, source: eu && avg ? avg.source : '' });
  }
  function currencyOf(code) { var r = regionOf(code); return currencyInfo(r ? r.cur : 'GBP') || CURRENCIES.GBP; }
  // the money symbol follows the region, unless an override is set (every money figure on every page)
  function setRegion(code) { regionCode = code || ''; currency = (override && currencyInfo(override)) || currencyOf(regionCode); return currency; }
  function setCurrency(code) { override = code && currencyInfo(code) ? String(code).toUpperCase() : ''; return setRegion(regionCode); }
  function setLocale(l) { locale = l || 'en-GB'; return locale; }
  function currencyNow() { return currency; }

  // ---------- currency by locale (currency-21, the founder, 7 October 2026: "£/day is for UK; we need other currencies") ----------
  // No IP lookups: the OS locale's region (navigator.language in the app's web view) picks the region row, its currency
  // and its default tariff at first run; a Settings override (settings.currency, in the engine's settings file) wins
  // and survives restart; the user's own tariff is the number, so no exchange rate exists anywhere. The long tail of
  // regions is synthesised from ISO_CURRENCY with Intl.DisplayNames for the name; an EU member without its own row
  // takes the EU average (euro cents) when it uses the euro; a region with no currency row falls back to USD and asks.
  var ISO_CURRENCY = { at: 'EUR', be: 'EUR', cy: 'EUR', de: 'EUR', ee: 'EUR', es: 'EUR', fi: 'EUR', fr: 'EUR', gr: 'EUR', hr: 'EUR', ie: 'EUR', it: 'EUR', lt: 'EUR', lu: 'EUR', lv: 'EUR', mt: 'EUR', nl: 'EUR', pt: 'EUR', si: 'EUR', sk: 'EUR',
    gb: 'GBP', us: 'USD', ca: 'CAD', au: 'AUD', nz: 'NZD', ch: 'CHF', li: 'CHF', se: 'SEK', no: 'NOK', dk: 'DKK', is: 'ISK', pl: 'PLN', cz: 'CZK', hu: 'HUF', ro: 'RON', bg: 'BGN', rs: 'RSD', tr: 'TRY', ua: 'UAH', ge: 'GEL', kz: 'KZT', ru: 'RUB',
    jp: 'JPY', kr: 'KRW', cn: 'CNY', hk: 'HKD', tw: 'TWD', sg: 'SGD', my: 'MYR', th: 'THB', vn: 'VND', ph: 'PHP', id: 'IDR', in: 'INR', pk: 'PKR', bd: 'BDT', ae: 'AED', sa: 'SAR', il: 'ILS', ir: 'IRR',
    za: 'ZAR', ng: 'NGN', ke: 'KES', eg: 'EGP', ma: 'MAD', et: 'ETB', br: 'BRL', mx: 'MXN', ar: 'ARS', cl: 'CLP', co: 'COP', pe: 'PEN', py: 'PYG' };
  var EU_MEMBERS = ['at', 'be', 'bg', 'hr', 'cy', 'cz', 'dk', 'ee', 'fi', 'fr', 'de', 'gr', 'hu', 'ie', 'it', 'lv', 'lt', 'lu', 'mt', 'nl', 'pl', 'pt', 'ro', 'sk', 'si', 'es', 'se'];
  var CURRENCY_CODES = ['GBP', 'EUR', 'USD', 'CAD', 'AUD'];
  (function () { var seen = {}; CURRENCY_CODES.forEach(function (c) { seen[c] = 1; }); var rest = []; for (var k in ISO_CURRENCY) if (!seen[ISO_CURRENCY[k]]) { seen[ISO_CURRENCY[k]] = 1; rest.push(ISO_CURRENCY[k]); } rest.sort(); CURRENCY_CODES = CURRENCY_CODES.concat(rest); })();
  // the minor unit's word and short form, and the currency's decimal digits (0 for yen or won: the price is typed in the unit itself)
  var MINOR = { GBP: ['pence', 'p'], EUR: ['euro cents', 'c'], USD: ['cents', 'c'], CAD: ['cents', 'c'], AUD: ['cents', 'c'], NZD: ['cents', 'c'], CHF: ['rappen', 'rp'], SEK: ['öre', 'öre'], NOK: ['øre', 'øre'], DKK: ['øre', 'øre'], PLN: ['grosze', 'gr'], CZK: ['haléře', 'h'], INR: ['paise', 'p'], BRL: ['centavos', 'c'], MXN: ['centavos', 'c'], ZAR: ['cents', 'c'], JPY: ['yen', '¥'], KRW: ['won', '₩'] };
  function minorOf(cur) {
    cur = String(cur || '').toUpperCase(); var digits = 2;
    try { digits = new Intl.NumberFormat('en', { style: 'currency', currency: cur }).resolvedOptions().maximumFractionDigits; } catch (e) { }
    var m = MINOR[cur] || (digits === 0 ? [cur.toLowerCase(), cur] : ['cents', 'c']);
    return { word: m[0], abbr: m[1], digits: digits };
  }
  // the engine stores the price in hundredths of the unit; a zero-decimal currency is typed in the unit, so x100 on the way in
  function priceFactor(cur) { return minorOf(cur).digits === 0 ? 100 : 1; }
  function symbolOf(cur) { try { var parts = new Intl.NumberFormat('en', { style: 'currency', currency: cur, currencyDisplay: 'narrowSymbol' }).formatToParts(1); for (var i = 0; i < parts.length; i++) if (parts[i].type === 'currency') return parts[i].value; } catch (e) { } return cur; }
  // a currency's row: the three the heat lane carried (with their approximate USD rates for the rent comparison), CAD and
  // AUD (approximate rates, October 2026), the long tail without a rate (the rent line says so)
  function currencyInfo(cur) {
    cur = String(cur || '').toUpperCase(); if (CURRENCIES[cur]) return CURRENCIES[cur];
    if (CURRENCY_CODES.indexOf(cur) < 0) return null;
    return (CURRENCIES[cur] = { code: cur, symbol: symbolOf(cur), minor: minorOf(cur).abbr, usd: cur === 'CAD' ? 0.72 : cur === 'AUD' ? 0.65 : 0 });
  }
  function regionName(code) { try { var n = new Intl.DisplayNames(['en'], { type: 'region' }).of(code.toUpperCase()); if (n && n !== code.toUpperCase()) return n; } catch (e) { } return code.toUpperCase(); }
  // the region of a BCP 47 tag: "de-DE" and "fr-CA" carry it, "de" alone is filled in by Intl.Locale; a language with no region ("eo") gives ''
  function regionFromLocale(lang) {
    lang = String(lang || '').trim(); if (!lang) return ''; var reg = '';
    try { var l = new Intl.Locale(lang); reg = l.region || (l.maximize ? l.maximize().region : '') || ''; } catch (e) { var m = /^[A-Za-z]{2,3}[-_]([A-Za-z]{2})\b/.exec(lang); reg = m ? m[1] : ''; }
    return /^[A-Za-z]{2}$/.test(reg) ? reg.toLowerCase() : '';
  }
  // what the money shows as: the override, else the chosen region, else the locale's region, else USD with the prompt
  function currencyChoice(settings, lang) {
    settings = settings || {}; var ov = String(settings.currency || '').toUpperCase(), region = settings.region || '', loc = regionFromLocale(lang);
    if (ov && currencyInfo(ov)) return { code: ov, source: 'override', region: region || loc, prompt: false };
    var r = region ? regionOf(region) : null;
    if (r && !(r.unknown || r.code === 'other')) return { code: r.cur, source: 'region', region: region, prompt: false };
    if (r) return { code: 'USD', source: 'fallback', region: region, prompt: true };
    var lr = loc ? regionOf(loc) : null;
    if (lr && !lr.unknown) return { code: lr.cur, source: 'locale', region: loc, prompt: false };
    return { code: 'USD', source: 'fallback', region: loc, prompt: true };
  }
  function currencyAsk(ch) { return ch && ch.prompt ? 'This app could not tell your currency from this machine (' + (ch.region ? 'region ' + ch.region.toUpperCase() : 'no region in its locale') + '). Choose one and type what you pay per kWh. Figures show in US dollars until you do.' : ''; }
  // the render path: set the module's currency and locale from the engine's settings and the web view's language
  function applyMoney(settings, lang) { var ch = currencyChoice(settings, lang); override = ch.source === 'override' ? ch.code : ''; regionCode = ch.region || ''; currency = currencyInfo(ch.code) || CURRENCIES.USD; return ch; }
  // the words for the price field: the minor unit of the currency in force (the override, else the region's)
  function unitWords(code) { var c = (override && currencyInfo(override)) || currencyOf(code); return minorOf(c.code).word + ' per kWh'; }
  // the restricted line for a region: the standing sentence plus the entry, or '' for a region with no entry
  function restrictedLine(code) { var r = regionOf(code); return r && r.restricted ? RESTRICTED_SENTENCE + ' ' + r.restricted : ''; }
  // the price note under the field: where the default came from, or that the miner types their own
  function priceNote(code) {
    var r = regionOf(code), c = currencyOf(code);
    if (!r) return 'Choose a region to see a typical price. Every ' + c.symbol + ' figure uses what you type here. Nothing is fetched.';
    return (r.price > 0 ? 'Typical: ' + r.price + ' ' + c.minor + ' per kWh (' + r.source + '). ' : 'No table price for this region. ') + 'Type what you pay, in ' + minorOf(((override && currencyInfo(override)) || c).code).word + ' per kWh. Nothing is fetched.';
  }
  // the measured rental rate of hash (docs/bench-log.md, "Rental cost of hash, 6 October 2026"); the test
  // heat-region.test.mjs reads the bench log and fails when this number drifts from it
  var RENT = { usd_per_mhs_hour: 0.0117, measured: '6 October 2026', source: 'rented community pods, 38 pods at list price, 1,748 MH/s for USD 20.44 an hour' };
  // the miner's own cost of hash: watts per MH/s at the price, in the region's minor unit per MH/s-hour, against the rent.
  // Live numbers while mining; the last tune's point otherwise; nothing without a draw reading or a price.
  function rentLine(cards, price, code) {
    var c = (override && currencyInfo(override)) || currencyOf(code), w = 0, h = 0, live = false;
    (cards || []).filter(present).forEach(function (cd) {
      if (cd.state === 'mining' && cd.power_w > 0 && cd.hash_now > 0) { w += cd.power_w; h += cd.hash_now; live = true; }
    });
    if (!(h > 0)) { w = 0; (cards || []).filter(present).forEach(function (cd) { if (cd.sweep_watts > 0 && cd.sweep_mhs > 0) { w += cd.sweep_watts; h += cd.sweep_mhs; } }); }
    var out = { kind: 'none', text: '', sub: '', cost: 0, usd: 0, ratio: 0, live: live, rentUsd: RENT.usd_per_mhs_hour };
    if (!(h > 0 && w > 0)) { out.text = 'Your cost of hash: no reading yet'; out.sub = 'needs a card mining with a power reading, or a finished tune. Rented hash costs USD ' + RENT.usd_per_mhs_hour + ' per MH/s-hour (' + RENT.source + ', ' + RENT.measured + ').'; return out; }
    if (!(price > 0)) { out.kind = 'price'; out.text = 'Your cost of hash: set a price'; out.sub = Math.round(w) + ' W for ' + (h >= 100 ? h.toFixed(0) : h.toFixed(1)) + ' MH/s is ' + (w / h).toFixed(2) + ' W per MH/s. Set your electricity price to compare it with rented hash at USD ' + RENT.usd_per_mhs_hour + ' per MH/s-hour.'; return out; }
    var cost = w / h / 1000 * price; // minor units per MH/s-hour
    if (!(c.usd > 0)) { out.kind = 'nocur'; out.cost = cost; out.text = 'Your cost of hash: ' + fmtMoney(cost / 100, c.code, locale) + ' per MH/s-hour'; out.sub = 'no USD rate is carried for ' + c.code + ', so no comparison with rented hash (USD ' + RENT.usd_per_mhs_hour + ' per MH/s-hour). Nothing is fetched.'; return out; }
    var usd = cost / 100 * c.usd, ratio = RENT.usd_per_mhs_hour / usd;
    out.kind = 'line'; out.cost = cost; out.usd = usd; out.ratio = ratio;
    var costText = cost < 0.1 ? cost.toFixed(3) : cost.toFixed(2);
    out.text = costText + ' ' + c.minor + ' per MH/s-hour';
    var verdict = ratio >= 1.05 ? 'Yours is ' + (ratio >= 10 ? Math.round(ratio) : ratio.toFixed(1)) + 'x cheaper than rented hash.' : ratio <= 0.95 ? 'Rented hash undercuts your card by ' + ((1 / ratio) >= 10 ? Math.round(1 / ratio) : (1 / ratio).toFixed(1)) + 'x.' : 'Level with rented hash.';
    out.verdict = verdict;
    out.sub = 'your cost of hash at ' + price + ' ' + c.minor + '/kWh and ' + (w / h).toFixed(2) + ' W per MH/s' + (live ? '' : ' (the last tune)') + (c.code === 'USD' ? '' : ', about USD ' + usd.toFixed(4)) + '. Rented hash: USD ' + RENT.usd_per_mhs_hour + ' per MH/s-hour (' + RENT.source + ', ' + RENT.measured + '). ' + verdict;
    return out;
  }
  // heat mode on the Ember strip and the card rows: h = state.heat
  function heatLine(h) {
    if (!h || !h.on) return { text: '', tone: '' };
    var room = h.room_source === 'typed' ? 'room ' + h.room_c.toFixed(1) + ' °C' : h.room_source === 'card' ? 'room about ' + Math.round(h.room_c) + ' °C' : 'no room reading yet';
    var heat = h.heat_w > 0 ? Math.round(h.heat_w) + ' W of heat' : h.phase === 'resting' ? 'resting' + (h.full_w > 0 ? ', ' + Math.round(h.full_w) + ' W when heating' : '') : h.full_w > 0 ? Math.round(h.full_w) + ' W when heating' : 'no draw reading';
    var duty = Math.round((h.duty_hour > 0 ? h.duty_hour : h.duty) * 100);
    return { text: 'Heat mode: holding ' + h.set_c.toFixed(1) + ' °C · ' + room + ' · heating ' + duty + '% of the time · ' + heat, tone: h.phase === 'resting' ? 'rest' : h.phase === 'heating' ? 'heat' : '' };
  }
  // the short form on a card row's Ember line
  function heatRowWords(h) { if (!h || !h.on) return ''; return 'heat ' + Math.round((h.duty_hour > 0 ? h.duty_hour : h.duty) * 100) + '% · ' + (h.phase === 'resting' ? 'resting' : h.heat_w > 0 ? Math.round(h.heat_w) + ' W of heat' : 'heating'); }

  // ---- the engine's an earlier release fields (ember-tune fd03f35): mining.watts_total, mining.pounds_per_day, address.balance_wei
  //      (string | null), address.balance_age_s (-1 until read), address.balance_note, address.price_gbp_per_ign
  //      (null until a signed manifest carries a price), the chain scene's reply.source ("node" | "site") ----
  // wei never sits in a Number (rule 25, 8 October 2026: a 2.0 block subsidy, 9.5e18 wei, is past 2^53): the engine
  // sends every wei value as a decimal string and the IGN figure is cut from that string, whole units and the first
  // `dp` decimals; only the IGN figure (a small number) is ever a Number, for the pounds line and the thousands check
  function weiParts(wei) {
    var s = String(wei === null || wei === undefined ? '' : wei).trim();
    if (!/^\d+$/.test(s)) return null;
    s = s.replace(/^0+(?=\d)/, '');
    var whole = s.length > 18 ? s.slice(0, -18) : '0', frac = s.length > 18 ? s.slice(-18) : ('000000000000000000' + s).slice(-18);
    return { whole: whole, frac: frac, zero: !/[1-9]/.test(s) };
  }
  function ignText(wei, dp) { var p = weiParts(wei); if (!p) return null; return p.whole + '.' + p.frac.slice(0, dp === undefined ? 4 : dp); }
  function ign(wei) { var p = weiParts(wei); return p ? Number(p.whole) + Number(p.frac.slice(0, 9)) / 1e9 : null; }
  function weiNonZero(wei) { var p = weiParts(wei); return !!p && !p.zero; }
  // the Earnings balance row: {text, sub, pounds} ; the UI shows "not read yet" until the first read
  function balanceWords(a, now) {
    a = a || {};
    if (a.balance_wei === undefined) return { text: '', sub: '', pounds: null };
    if (a.balance_wei === null || a.balance_wei === '') return { text: 'not read yet', sub: a.balance_note || 'the node answers the balance once it is synced', pounds: null };
    var v = ign(a.balance_wei); if (v === null) return { text: 'not read yet', sub: a.balance_note || '', pounds: null };
    var age = a.balance_age_s >= 0 ? ' · read ' + ago(a.balance_age_s) : '';
    var price = a.price_gbp_per_ign;
    return { text: (v >= 1000 ? withCommas(v) : ignText(a.balance_wei, 4)) + ' IGN', sub: 'in the wallet' + age + (a.balance_note ? ' · ' + a.balance_note : ''), pounds: price > 0 ? v * price : null };
  }
  // the fleet watts and £ a day: the engine's figures when it carries them, else the UI's own sums
  function fleetWatts(m, cards) { return m && m.watts_total > 0 ? m.watts_total : wattsTotal(cards); }
  function fleetPounds(m, cards, pence) { if (m && m.pounds_per_day > 0) return m.pounds_per_day; return poundsPerDay(fleetWatts(m, cards), pence); }
  // is a lane key this machine's? The cards' ids are the first 8 hex of each vote key hash; a source may shorten the
  // hash to another length (the an earlier release engine keeps 12), so either prefix of the other counts
  function isMine(miner, cards) {
    var m = String(miner || '').replace(/^0x/, '').toLowerCase(); if (!m) return false;
    var head = m.split('..')[0]; // the 0.1.16 engine shortens a long hash to "head6..tail6"; the site keeps 8 hex
    return (cards || []).some(function (c) { return (c.ids || []).some(function (id) {
      id = String(id).replace(/^0x/, '').toLowerCase(); if (!id) return false;
      var n = Math.min(head.length, id.length); return n >= 6 && head.slice(0, n) === id.slice(0, n);
    }); });
  }
  // the chain scene's note from the reply's source
  function liveNote(reply) {
    if (!reply) return '';
    if (reply.pending) return 'connecting';
    var src = reply.source || ((reply.state || {}).source);
    if (src === 'node') return 'From your node. Your blocks are ringed.';
    if (src === 'site' || src === 'observer') return 'From Igneum’s observer. Your blocks are ringed.';
    return '';
  }
  // finality (ember-tune 1357d280, folded in at the rebase onto a9eb58f1): f = state.finality. While the network's finality is paused
  // (under two thirds of the weight signing) the node line's sub-text and the lock row say so, and no surface calls a
  // lock "final" (main, Q83/Q84). {paused, age, note}
  function finalityWords(f) {
    f = f || {};
    if (f.paused) return { paused: true, age: 'paused', note: f.line || 'Finality paused: under two thirds of the weight is signing' };
    if (f.lock_kind === 'recovery') return { paused: false, recovery: true, age: rel(f.age_s), note: 'Recovery lock: ' + lockKindWords(f).why + '.' };
    if (f.last_lock > 0) return { paused: false, age: rel(f.age_s), note: 'A lock is a point the miners have agreed can never be undone. Checkpoints lock at 2/3 of the voting weight.' };
    return { paused: false, age: 'n/a', note: f.message || 'Locks appear once the miner votes on checkpoints.' };
  }
  // whose node the app reads (publish 2's read-back, 6 October 2026): n.source app | external | none | ""
  function nodeSource(n) {
    n = n || {};
    if (n.source === 'app') return { text: 'this app’s own node', sub: n.mode_reason || n.port_note || '' };
    if (n.source === 'external') return { text: 'another node on this machine', sub: n.rules_check === 'match' ? 'same network and rules, checked' : n.rules_check === 'unknown' ? 'its rules could not be checked (an older node)' : (n.port_note || '') };
    if (n.source === 'none') return { text: 'no node', sub: n.port_note || n.mode_reason || 'the ports are taken by a node on other rules' };
    return { text: '', sub: '' };
  }
  // ---- miner-ui-5 (7 October 2026): the miner's first month. The Poisson count-up before the first block, the block
  //      card, Earnings in IGN first with a typed price, the ladder rungs as chain facts, the first-hour timeline.
  //      Every number names its RPC field on hover: FIELDS holds the one sentence per number, the renderers put it in
  //      the title attribute. `chain` is GET api/ladder (src/chainfacts.rs): the node's getFinalityWeights table with
  //      this machine's keys ranked, the params, the locked index and the public /api/stats numbers. ----
  var FIELDS = {
    hashrate: '/api/stats hashrate: the node’s estimateNetworkHashesPerSecond over 1,000 blocks (H/s)',
    reward: '/api/stats block_reward.miner_ign: the 80 producer points of E(daa) (spec 2.5) at the newest DAA score, after the launch ramp',
    ramp: '/api/stats block_reward.ramp_factor: the launch ramp, 10% on day one to 100% at day 30',
    blocks: 'getFinalityWeights keys[].blocks: blue blocks with your key in the weight window (params.weightWindow)',
    voter: 'getFinalityWeights keys[].voter: blocks at or above params.dust',
    dust: 'getFinalityWeights params.dust: the blocks a key needs in the window to vote',
    window: 'getFinalityWeights params.weightWindow: the window in DAA steps (one a block, ' + BLOCK_S + ' s)',
    participation: 'getFinalityWeights keys[].participation: the share of the last params.presenceWindow checkpoints your key signed',
    rank: 'getFinalityWeights keys[] sorted by blocks, your key’s place; "of" = keys_listed (the observer relays at most 64)',
    voters: 'getFinalityWeights voters: keys at or above dust',
    total: 'getFinalityWeights totalWeight: every key’s blocks in the window',
    locked: 'getFinalityCheckpoints latestLockedIndex: the newest checkpoint locked by two thirds of all weight',
    signed: 'this app’s miner log: VOTE index=N lines, read as locked once a LOCK checkpoint line reaches N; the per-key signer list is owed from the node (getFinalityCheckpoints checkpoints[].signers)',
    shards: 'igneum_getProofRecords(block).paid[shard] with your payout address (src/prover.rs)',
    found: 'this app: the miner’s ACCEPTED block lines, each matched to the node’s PoW accepted line by nonce (ladder.json)',
    found_hour: 'this app: mining.found, the accepted blocks of the last hour',
    days: 'this app: distinct UTC days with a block in the last 30 days (ladder.json); per-key days are owed from the node (getFinalityWeights keys[].daysMined)',
    streak: 'this app: unbroken VOTE lines inside the presence window (ladder.json); the chain fact is keys[].participation',
    watts: 'the cards’ power draw now (nvidia-smi, the AMD telemetry, or the engine’s mining.watts_total)',
    share: 'your rate over /api/stats hashrate'
  };
  function fmtIgn(v) { if (v === null || v === undefined || isNaN(v)) return ''; var a = Math.abs(v); return (a >= 1000 ? withCommas(v) : a >= 100 ? v.toFixed(1) : a >= 1 ? v.toFixed(2) : v.toFixed(4)) + ' IGN'; }
  function pct(v, digits) { if (!(v >= 0) || !isFinite(v)) return ''; var p = v * 100; return (p >= 10 ? p.toFixed(1) : p >= 1 ? p.toFixed(2) : p >= 0.01 ? p.toFixed(3) : p.toPrecision(2)) + '%'; }
  // "16 minutes" / "1.6 hours" / "16 hours" / "6.8 days" for an expected gap
  function gapWords(s) {
    if (!(s > 0) || !isFinite(s)) return '';
    if (s < 90) return Math.max(1, Math.round(s)) + ' seconds';
    if (s < 5400) return Math.round(s / 60) + ' minutes';
    if (s < 36 * 3600) { var h = s / 3600; return (h < 10 ? h.toFixed(1) : Math.round(h)) + ' hours'; }
    var d = s / 86400; return (d < 10 ? d.toFixed(1) : Math.round(d)) + ' days';
  }
  // "41 days" / "3 h 12 min" / "48 min" for a streak or an age
  var hhmm = Notices.hhmm;
  var NETWORK_PAUSED_S = 600; // the projection reads "Network paused" when the chain has no block for ten minutes
  function spanWords(s) { s = Math.max(0, Math.round(s || 0)); if (s < 60) return s + ' s'; if (s < 3600) return Math.floor(s / 60) + ' min'; if (s < 86400) return Math.floor(s / 3600) + ' h ' + Math.floor(s % 3600 / 60) + ' min'; var d = Math.floor(s / 86400); return d + ' day' + (d === 1 ? '' : 's'); }
  // the chain's numbers the lines need, with nulls where the reply has none: c = GET api/ladder
  function chainNums(c) {
    var ok = !!(c && c.ok), n = (ok && c.network) || {}, p = (ok && c.params) || {};
    return { ok: ok, hps: n.hashrate_hps > 0 ? n.hashrate_hps : null, minerIgn: n.miner_ign_per_block > 0 ? n.miner_ign_per_block : null, ramp: n.ramp_factor > 0 ? n.ramp_factor : null, bps: n.bps > 0 ? n.bps : 1 / BLOCK_S, dust: p.dust > 0 ? p.dust : null, windowS: p.weight_window > 0 ? p.weight_window : null, presence: p.presence_window > 0 ? p.presence_window : null, interval: p.checkpoint_interval > 0 ? p.checkpoint_interval : 30, best: ok && c.best ? c.best : null, mine: (ok && c.mine) || [], listed: ok ? (c.keys_listed || 0) : 0, cap: ok ? (c.keys_cap || 0) : 0, voters: ok ? (c.voters || 0) : 0, locked: ok ? (c.latest_locked_index || 0) : 0, active: ok ? !!c.finality_active : false, source: ok ? c.source : '', stale: !ok || !!n.stale, error: !ok && c ? (c.error || '') : '' };
  }
  // the Poisson count-up: a card at `mhs` on a network at `hps` finds a block every hps / (mhs * 1e6 * bps) seconds on
  // average; after `elapsed` seconds the chance of at least one is 1 - exp(-elapsed / gap). {gap_s, gapText, chance,
  // chanceText, line, pool, share}
  function poisson(mhs, hps, elapsed, bps, fleet) {
    if (!(mhs > 0) || !(hps > 0)) return null;
    bps = bps > 0 ? bps : 1 / BLOCK_S;
    var share = mhs * 1e6 / hps, gap = 1 / (share * bps), chance = 1 - Math.exp(-Math.max(0, elapsed || 0) / gap);
    var who = fleet ? 'Your cards find' : 'A card like yours finds';
    var line = who + ' a block about every ' + gapWords(gap) + ' on today’s network. Chance so far: ' + Math.round(chance * 100) + '%.';
    var pool = gap > 8 * 3600 ? 'Above an 8-hour gap a pool pays by the share, every minute. The pool setting arrives with pool-0.' : '';
    return { gap_s: gap, gapText: gapWords(gap), chance: chance, chanceText: Math.round(chance * 100) + '%', line: line, pool: pool, share: share };
  }
  // the stat strip (15 October 2026, the founder's "make it beautiful"): five tiles, each {text, ph (placeholder), sub, tone};
  // a figure unknown is the dash, never 0 and never empty; the strip never hides a tile
  // the card's short name for a tile: "NVIDIA GeForce RTX 5090" reads "RTX 5090", "AMD Radeon RX 9070 XT" "RX 9070 XT",
  // "Intel Arc B580" "Arc B580", "Apple M5 Max" as it is (main's audit of 15 October 2026: the temperature tile cut the name at 1200 wide)
  function shortCardName(name) {
    var n = String(name || '').replace(/^NVIDIA\s+GeForce\s+/i, '').replace(/^NVIDIA\s+/i, '').replace(/^AMD\s+Radeon\s+/i, '').replace(/^AMD\s+/i, '').replace(/^Intel\(R\)\s+/i, '').replace(/^Intel\s+/i, '').replace(/\s+Graphics$/i, '').trim();
    return n || String(name || '');
  }
  // the pool's host and port alone for a tile line ("pool.igneum.network:3342"), from the engine's source url; TLS is the tile's title
  // the chain tile names the pool the engine DIALS, from the supervisor's own source name ("pool <label> (<host>:<port>, no TLS)"),
  // never the stored setting's text: the two-card Windows desktop's 0.2.20 capture (04:42 UK, 16 October 2026) read
  // "pool.igneum.network:3341" from its stored string while the engine mined :3342 by the pin's move (0.2.17's rule)
  function hostPort(src) { var m = /\(([A-Za-z0-9.-]+:\d+)(?:,[^()]*)?\)\s*$/.exec(String((src && src.name) || '')); if (m) return m[1]; var u = String((src && src.url) || '').trim().replace(/^https?:\/\//, '').replace(/\s+(notls|tls)$/, '').replace(/\/.*$/, ''); return u; }
  // the founder's audit (15 October 2026): Apple silicon and an integrated GPU report neither power nor temperature: the tile names the cause once
  function noReadingWords(cards) { var c = (cards || [])[0] || {}; return c.vendor === 'apple' ? 'not read on Apple silicon' : c.kind === 'integrated' ? 'not read on an integrated GPU' : 'no reading on this card'; }
  // main's cell of 23:2x UK, 15 October 2026 (the audit): a tile with nothing to read collapses to one quiet line under the live
  // tiles, so the first screen holds only figures that move: when both the power and the temperature tiles are placeholders
  // for want of a reading on the cards, both go and the foot says "power and temperature: not read on Apple silicon"
  function deadTiles(tiles, cards) {
    cards = cards || []; var dead = cards.length > 0 && cards.every(function (c) { return !(c.power_w > 0) && !(c.temp_gpu > 0) && !!noReading(c); });
    var both = !!(tiles && tiles.power && tiles.power.ph && tiles.temp && tiles.temp.ph && dead);
    return { hidePower: both, hideTemp: both, note: both ? ('power and temperature: ' + noReadingWords(cards)) : '' };
  }
  // F-E14's bound on the page (16 October 2026): a connected source that has sent no job for a minute is a stalled stream, the
  // engine's own NO_WORK_AFTER; the pool's jobs come a block apart (12 s a block on the public test network), so the page's earlier
  // 20 s read every long block gap as "waiting for work" beside a live rate (main's audit of 0.2.21's raster sheets, cell 4)
  var WAITING_FOR_WORK_S = 60;
  function waitingForWork(s) { s = s || {}; var m = s.mining || {}, src = s.source || {}; return m.state === 'mining' && (src.waiting_for_work_s || 0) >= WAITING_FOR_WORK_S; }
  function stripTiles(s, chain, now, credit) {
    s = s || {}; var m = s.mining || {}, src = s.source || {}, cards = shownCards(m.cards).filter(present), c = chainNums(chain); credit = credit || null;
    var mining = m.state === 'mining', rate = m.hash_total > 0 ? m.hash_total : 0;
    var lanes = machineRate(cards), first = lanes[0] || null;
    var onWords = 'on ' + cards.filter(function (x) { return x.state === 'mining'; }).length + ' of ' + cards.filter(function (x) { return x.enabled; }).length + ' cards';
    var hash = first ? { text: first.text, unit: first.unit, ph: false, sub: (mining ? onWords : (m.state || 'waiting')) + (lanes.length > 1 ? '\n' + lanes.slice(1).map(function (l) { return l.text + ' ' + l.unit + ' on ' + l.lane; }).join('\n') + '\n' + first.text + ' ' + first.unit + ' on ' + first.lane : (lanes.length === 1 && laneUnit(first.lane) !== 'hashes' ? '\non ' + first.lane : '')) }
      : rate > 0 ? { text: rate >= 100 ? rate.toFixed(0) : rate.toFixed(1), unit: 'MH/s', ph: false, sub: mining ? onWords : (m.state || 'waiting') }
      : { text: '\u2014', unit: 'MH/s', ph: true, sub: m.state === 'paused' ? 'paused' : m.state === 'idle' ? 'not mining' : mining ? 'starting' : 'waiting' };
    // no job for a minute on a connected source: the figure is the last measured, said so over the wait (never a live-reading word beside it)
    if (waitingForWork(s)) hash = { text: hash.text, unit: hash.unit, ph: hash.ph, sub: (hash.ph ? '' : 'last measured\n') + 'waiting for work from the ' + (src.kind === 'node' ? 'node' : 'pool') };
    // the network share reads the hash lanes alone (attempts are not hashes): the raster-only machine's earnings sub names its lane
    var hashRate = lanes.filter(function (l) { return laneUnit(l.lane) === 'hashes'; }).reduce(function (a, l) { return a + l.value; }, 0) / 1e6;
    if (lanes.length) rate = hashRate;
    var watts = fleetWatts(m, cards), onCards = cards.some(function (x) { return x.power_w > 0; });
    var power = watts > 0 ? { text: String(Math.round(watts)), ph: false, sub: onCards ? 'on the cards' : 'reported', label: 'Power' }
      : { text: '\u2014', ph: true, sub: cards.some(noReading) ? noReadingWords(cards.filter(noReading)) : (mining ? 'reading' : 'no reading yet'), label: 'Power' };
    var expected = ignPerDay(rate, c.hps, c.minerIgn, c.bps);
    // main's audit of the two-card Windows host's captures (15 October 2026): on a pool source with no observer address the ladder answers not
    // ok for the whole session, so the tile read "the network's rate not read yet" for ever; the engine's own estimate
    // (F-E20: the feed's network rate, the chain's blocks a day and the pool's payout per block, from its own poll) is
    // the second source, named so in the sub-line
    var est = m.estimate || null, estIgn = est && est.receipts_ign > 0 && est.share_of_network > 0 ? est.receipts_ign : null;
    var earn = expected !== null && mining && rate > 0 ? { text: fmtIgn(expected).replace(' IGN', ''), ph: false, sub: 'at ' + (rate >= 100 ? rate.toFixed(0) : rate.toFixed(1)) + ' MH/s, ' + pct(rate * 1e6 / c.hps) + ' of the network' }
      : estIgn !== null && mining ? { text: fmtIgn(estIgn).replace(' IGN', ''), ph: false, sub: 'at ' + (rate >= 100 ? rate.toFixed(0) : rate.toFixed(1)) + ' MH/s, ' + pct(est.share_of_network) + ' of the network (the engine\u2019s estimate)' }
      // the founder's dopamine cell (15 October 2026): when neither the ladder nor the engine's estimate can be read, the tile
      // shows what the chain holds for the address and the last pay's time, ticking as the chain pays (earnCreditTile)
      : (credit && credit.ign != null ? earnCreditTile(credit, now) : { text: '\u2014', ph: true, sub: !c.ok ? 'the network\u2019s rate not read yet' : mining ? 'reading' : 'while mining' });
    var hot = cards.filter(function (x) { return x.temp_gpu > 0; }).sort(function (a, b) { return b.temp_gpu - a.temp_gpu; })[0];
    var temp = hot ? { text: String(Math.round(hot.temp_gpu)), ph: false, sub: shortCardName(hot.name), tone: hot.temp_mem > 95 || hot.temp_gpu > 92 ? 'hot' : hot.temp_mem > 90 || hot.temp_gpu > 86 ? 'warm' : '' }
      : { text: '\u2014', ph: true, sub: cards.length ? noReadingWords(cards) : 'no card' };
    var cid = Number(src.chain_id || (s.network && s.network.chain_id) || 0), cw = cid ? chainWord(cid, s.network) : '';
    var chainTile;
    if (src.problem && !src.connected) chainTile = { text: (src.kind === 'node' ? 'node' : 'pool') + ' unreachable', ph: false, tone: 'bad', sub: String(src.problem).slice(0, 60) };
    // main's audit of the two-card Windows host's captures (15 October 2026): at 1200 wide the tile cut the chain words and the host; the figure
    // is the short state, the chain by the naming rule and the host each on their own line of the sub
    else if (src.connected) chainTile = { text: (src.kind === 'node' ? 'node' : 'pool') + ' connected', ph: false, tone: 'good', sub: (cw ? cw + '\n' : '') + (hostPort(src) || src.host || src.name || ''), title: src.name || '' };
    else chainTile = { text: '\u2014', ph: true, sub: src.kind ? 'connecting to the ' + (src.kind === 'node' ? 'node' : 'pool') : 'no work source set' };
    return { hash: hash, power: power, earn: earn, temp: temp, chain: chainTile };
  }
  // the earnings tile from the chain's credit (the founder's cell): the balance the chain holds for the address, the last pay's
  // time beside it; {ign, head, lastPaid: {height, at_unix} | null, behind}
  function earnCreditTile(credit, now) {
    var ign = Number(credit.ign || 0), lp = credit.lastPaid || null, nowS = now > 1e12 ? now / 1000 : now;
    var when = lp && lp.at_unix > 0 && nowS > 0 ? ('last paid ' + rel(Math.max(0, nowS - lp.at_unix)) + ' ago at block ' + withCommas(lp.height)) : lp && lp.height > 0 ? ('last paid at block ' + withCommas(lp.height)) : (credit.head > 0 ? ('on the chain, read at block ' + withCommas(credit.head)) : 'on the chain');
    return { text: ign > 0 ? fmtIgn(ign).replace(' IGN', '') : '0', ph: false, sub: when, unit: 'IGN on the chain', tone: ign > 0 ? 'good' : '' };
  }
  // the share strip shows whole squares alone (the founder's audit at 390 wide: the last square cut): the squares that fit
  // the track's width, the door kept, the newest shares kept; 14 px a square, 4 px of gap, the door 22 px with its 6 px margin
  // a unix time in the reader's own clock: HH:MM today, else the day and HH:MM (the founder's audit: no ISO stamps on a page)
  function clockWords(unix, now) { var d = new Date(unix * 1000), n = new Date(((now || Date.now() / 1000)) * 1000); var hm = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0'); return d.toDateString() === n.toDateString() ? hm + ' today' : d.getDate() + ' ' + ['January','February','March','April','May','June','July','August','September','October','November','December'][d.getMonth()] + ' at ' + hm; }
  // the founder's order of 23:4x UK, 15 October 2026: Ember is dark on its first run and on every run without a saved choice,
  // whatever the system's theme; a saved choice (light, dark or system) is kept; ?theme= serves the screenshots
  // the fleet console's address off focus: the host and the path, the token directory to its last four (the secrets order of
  // record; product A's fault of 16 October 2026: the whole token stood in the Settings captures sent for the audit)
  // the masked form is short enough to read whole in a phone-width input (main's read of the 0.2.21-2 Settings sheet at 390,
  // 16 October 2026: the input showed the address's start and looked unmasked): the host, the directory's parent, the mask
  function fleetUrlShown(url) { url = String(url || ''); if (!url) return ''; var t = url.replace(/\/+$/, ''), i = t.lastIndexOf('/'); if (i < 0 || t.slice(0, i).indexOf('://') < 0) return '...'; var tok = t.slice(i + 1), tail = tok.length >= 9 ? tok.slice(-4) : ''; return t.slice(0, i).replace(/^[a-z]+:\/\//i, '') + '/...' + tail + (/\/$/.test(url) ? '/' : ''); }
  // the Cards page's lane rows (main's read of the 0.2.19 sheets, 16 October 2026): each card's lanes this session, the
  // current one first, with the rate read on it, the shares accepted on it and the time on it, so a one-card machine reads full
  function laneRows(cd) {
    var rows = ((cd || {}).lanes || []).slice().sort(function (a, b) { return (b.current ? 1 : 0) - (a.current ? 1 : 0) || a.coord - b.coord; });
    return rows.map(function (l) {
      // the raster lane's unit is attempts a second (main's read of 16 October 2026), never MH/s; the hash lanes read in hashes
      var raster = /raster/.test(l.word || '');
      var rate = !(l.hps > 0) ? 'no rate read' : raster ? withCommas(Math.round(l.hps)) + ' attempts a second' : (l.hps >= 1e6 ? (l.hps / 1e6).toFixed(l.hps >= 1e8 ? 0 : 1) + ' MH/s' : l.hps >= 1e3 ? (l.hps / 1e3).toFixed(0) + ' kH/s' : Math.round(l.hps) + ' H/s');
      var shares = withCommas(l.accepted || 0) + ' share' + (l.accepted === 1 ? '' : 's'), mins = Math.round((l.seconds || 0) / 60);
      var time = mins >= 60 ? (Math.floor(mins / 60) + ' h ' + (mins % 60) + ' min') : mins + ' min';
      // the card's own figure is its last minute's; the lane's is the stint meter's (reset at the switch). Once the stint is a
      // minute old they are one window: the lane row shows the card's figure; younger, the stint's figure says so (main's
      // read of the 0.2.21-2 Cards sheet, 16 October 2026: 1.6 MH/s on the card and 1.7 MH/s on its lane in one frame)
      if (l.current && cd.hash_now > 0) rate = (cd.stint_s || 0) >= 60 ? laneRateText(cd.hash_now * 1e6, l.word) : rate + (l.hps > 0 ? ' this stint' : '');
      return { word: l.word || ('lane ' + l.coord), rate: rate, shares: shares, time: time, current: !!l.current };
    });
  }
  // main's read of the 0.2.19 sheets (16 October 2026): a pool on another chain than this build's pin (the old public test
  // network's pool.igneum.network:3341, chain 7320) is not offered; the foot names how many are off the list and why
  var PIN_CHAIN_ID = 7322;
  var KNEE_RULE = 'Ember never locks below the knee by itself. Going lower is your choice.';
  // one pool filed on two ports is one listing (main's read of the 0.2.21-2 Pools sheet, 16 October 2026: the same name
  // twice, one probe "not published" and the other 100 percent): the listings whose name, host, project flag, chain and fee
  // agree fold into one with every port named, the probe and the stated figure from whichever port publishes them, and
  // the address of the port this machine dials (else the first filed) as the one the switch uses
  function poolsMerged(pools, current) {
    var out = [], byKey = {};
    (pools || []).forEach(function (p) {
      var host = String(p.address || '').replace(/:\d+$/, ''), key = [String(p.name || ''), host, p.project ? 1 : 0, Number(p.chain_id || 0), p.fee_bps == null ? '' : p.fee_bps, p.document_error ? 1 : 0].join('|');
      var m = byKey[key];
      if (!m) { m = Object.assign({}, p, { ports: [], addresses: [] }); byKey[key] = m; out.push(m); }
      m.addresses.push(p.address); var port = (String(p.address || '').match(/:(\d+)$/) || [])[1]; if (port) m.ports.push(port);
      var pr = (p.reliability || {}).probe || {}, mpr = (m.reliability || {}).probe || {};
      if (!Object.keys(mpr).length && Object.keys(pr).length) m.reliability = Object.assign({}, m.reliability || {}, { probe: pr });
      if ((m.reliability || {}).statement_30d_pct == null && (p.reliability || {}).statement_30d_pct != null) m.reliability = Object.assign({}, m.reliability || {}, { statement_30d_pct: p.reliability.statement_30d_pct });
      if (m.concentration == null && p.concentration != null) m.concentration = p.concentration;
    });
    out.forEach(function (m) {
      var cur = current ? m.addresses.filter(function (a) { return a && current.indexOf(a) === 0; })[0] : null;
      m.address = cur || m.addresses[0];
      m.address_line = m.addresses.length > 1 ? (m.address.replace(/:\d+$/, '') + ':' + m.ports.join(' and :')) : m.address;
    });
    return out;
  }
  function poolsOffered(pools, pin) {
    pin = Number(pin || PIN_CHAIN_ID); var shown = [], off = [];
    (pools || []).forEach(function (p) { var c = Number(p.chain_id || 0); if (c > 0 && c !== pin) off.push(p); else shown.push(p); });
    var words = off.length ? off.length + ' pool' + (off.length === 1 ? '' : 's') + ' on ' + (off.every(function (p) { return Number(p.chain_id) === 7320; }) ? 'the old public test network' : 'another chain') + ' ' + (off.length === 1 ? 'is' : 'are') + ' not listed: this version mines ' + chainWord(pin, '') + '.' : '';
    return { shown: shown, off: off, words: words };
  }
  // the founder's own report from his MacBook on 0.2.20 (main, 09:0x UK, 16 October 2026): the M5 Max on the raster lane at
  // 314 attempts a second read "0.0 MH/s" on the Mine tile; every rate shown for a card or the machine takes the lane's own
  // unit and word (attempts a second on the raster lane; the hash lanes in kH/s under a million hashes a second, MH/s above)
  function laneUnit(laneWord) { return /raster/.test(String(laneWord || '')) ? 'attempts a second' : 'hashes'; }
  function rateWords(hps, laneWord) {
    hps = Number(hps) || 0;
    if (!(hps > 0)) return { text: '\u2014', unit: laneUnit(laneWord) === 'hashes' ? 'MH/s' : 'attempts a second', value: 0 };
    if (laneUnit(laneWord) !== 'hashes') return { text: withCommas(Math.round(hps)), unit: 'attempts a second', value: hps };
    // the bandwidth lane reads in MH/s (its rates are millions); the programme lane in kH/s under a million a second (the
    // interpreter's form on Apple silicon: 32 kH/s) and MH/s above (a 5090's 63 MH/s); a card with no lane word in MH/s
    if (/programme/.test(String(laneWord || '')) && hps < 1e6) return { text: hps >= 1e3 ? String(Math.round(hps / 1e3)) : (hps / 1e3).toFixed(1), unit: 'kH/s', value: hps };
    var m = hps / 1e6; return { text: m >= 100 ? m.toFixed(0) : m.toFixed(1), unit: 'MH/s', value: hps };
  }
  function laneRateText(hps, laneWord) { var r = rateWords(hps, laneWord); return r.text + ' ' + r.unit; }
  // the machine's rate by lane: the mining cards grouped by the lane they are on (their hash_now is the engine's figure in
  // millions a second, attempts on the raster lane), each lane summed in its own unit, never across units; the first the
  // fastest hash lane, the raster lane after; a card with no lane word counts as a hash lane
  function machineRate(cards) {
    var by = {}, order = [];
    (cards || []).forEach(function (cd) {
      if (!cd || cd.state !== 'mining' || !(cd.hash_now > 0)) return;
      var w = String(cd.lane_word || '').replace(/,\s*probing$/, '') || 'the hash lanes';
      if (!(w in by)) { by[w] = 0; order.push(w); }
      by[w] += cd.hash_now * 1e6;
    });
    return order.map(function (w) { var r = rateWords(by[w], w); r.lane = w; return r; }).sort(function (a, b) { var ra = laneUnit(a.lane) === 'hashes', rb = laneUnit(b.lane) === 'hashes'; if (ra !== rb) return ra ? -1 : 1; return b.value - a.value; });
  }
  function themeChoice(param, storedChoice) { var p = param === 'light' || param === 'dark' || param === 'system' ? param : ''; var s = storedChoice === 'light' || storedChoice === 'dark' || storedChoice === 'system' ? storedChoice : ''; return p || s || 'dark'; }
  function stripCapacity(trackWidth) { var w = Number(trackWidth || 0) - 28; return w <= 0 ? 0 : Math.max(1, Math.floor((w + 4) / 18)); }
  // the first accepted share marked ONCE per install (the founder's cell): the strip pulses and says so when the count first
  // rises above zero and no mark is stored; a stored mark (the install's own memory) never marks again
  function firstShareMark(accepted, markedBefore) { return !markedBefore && Number(accepted || 0) > 0; }
  // IGN a day for a rate on a network: 86,400 / BLOCK_S blocks a day (7,200 at 12 seconds), your share of them, the producer's points of each
  function ignPerDay(mhs, hps, minerIgn, bps) { if (!(mhs > 0) || !(hps > 0) || !(minerIgn > 0)) return null; bps = bps > 0 ? bps : 1 / BLOCK_S; return 86400 * bps * (mhs * 1e6 / hps) * minerIgn; }
  // the window in the user's words: "30 days" on mainnet, "2 hours" on the devnet (params.weightWindow in seconds)
  function windowWords(windowS) { if (!(windowS > 0)) return { n: 30, unit: 'days', text: '30 days' }; if (windowS >= 86400) { var d = Math.round(windowS / 86400); return { n: d, unit: 'days', text: d + ' day' + (d === 1 ? '' : 's') }; } var h = Math.max(1, Math.round(windowS / 3600)); return { n: h, unit: 'hours', text: h + ' hour' + (h === 1 ? '' : 's') }; }
  // the days (or hours on the devnet) this key has mined inside the window: the ladder's day ring, or the hours
  // spanned by its blocks when the window is under a day
  function daysIn(ladder, windowS, now) {
    var w = windowWords(windowS); ladder = ladder || {};
    if (w.unit === 'days') return { have: Math.min(w.n, ladder.days_mined_30 || 0), of: w.n, unit: 'days' };
    var hours = 0;
    if (ladder.first_block_at > 0 && now) hours = Math.min(w.n, Math.floor((now - ladder.first_block_at) / 3600) + ((ladder.blocks_today || 0) > 0 || (ladder.blocks_30d || 0) > 0 ? 1 : 0));
    return { have: Math.max(0, hours), of: w.n, unit: 'hours' };
  }
  // the ladder's rungs for a state: s = state, chain = api/ladder, now = unix s. Each rung: {id, name, state (done | now |
  // next | off), line, sub, field, progress (-1 or 0..1)}. The gate (mission 2.6): a key walks every rung on screen.
  function ladderRungs(s, chain, now) {
    s = s || {}; var m = s.mining || {}, ld = s.ladder || {}, pv = s.proving || {}, c = chainNums(chain), rungs = [];
    var best = c.best, inTable = !!best, blocks = inTable ? best.blocks : 0, found = m.accepted_total || 0;
    // 0. first block (first-block-21: a machine whose first block came, on any run, never reads "waiting" again,
    //    whatever the lifetime count says after a settings reset)
    var first = found > 0 || blocks > 0 || ld.first_block_at > 0 || !!ld.first_block_shown;
    rungs.push({ id: 'first', name: 'First block', state: first ? 'done' : 'now', line: first ? 'First block' + (ld.first_block_at > 0 && now ? ' ' + ago(now - ld.first_block_at) : '') + (ld.first_block_card ? ', ' + cardName(s, ld.first_block_card) : '') + '.' : 'Waiting for your first block.', sub: inTable ? withCommas(blocks) + ' block' + (blocks === 1 ? '' : 's') + ' with your key in the window' : (found ? withCommas(found) + ' found by this machine' : ''), field: inTable ? FIELDS.blocks : FIELDS.found, progress: -1 });
    // 1. the vote
    var dust = c.dust, voter = inTable && best.voter;
    rungs.push({ id: 'vote', name: 'Your key has a vote', state: voter ? 'done' : first ? 'now' : 'next', line: voter ? 'Your key has a vote.' : (dust ? withCommas(blocks) + ' of ' + withCommas(dust) + ' blocks to a vote.' : 'A vote needs the dust line’s blocks in the window.'), sub: voter ? withCommas(blocks) + ' blocks in the window, the line is ' + withCommas(dust || 0) : (inTable ? 'your key is in the table' : c.ok ? 'your key is not in the table yet' : (c.error || 'the chain facts are not read yet')), field: FIELDS.voter + ' · ' + FIELDS.dust, progress: voter ? 1 : dust ? Math.min(1, blocks / dust) : -1 });
    // 2. the signature in a locked checkpoint
    var signedIdx = ld.last_signed_locked || 0, part = inTable ? best.participation : 0;
    rungs.push({ id: 'signed', name: 'Your signature is in a checkpoint', state: signedIdx > 0 ? 'done' : voter ? 'now' : 'next', line: signedIdx > 0 ? 'Your signature is in checkpoint ' + withCommas(signedIdx) + '.' : voter ? 'Signing: the next locked checkpoint carries your vote.' : 'Comes with the vote.', sub: signedIdx > 0 ? withCommas(ld.signed_locked_count || 0) + ' signed and locked' + (inTable ? ' · presence ' + Math.round(part * 100) + '%' : '') + (c.locked ? ' · latest lock ' + withCommas(c.locked) : '') : (c.locked ? 'latest lock ' + withCommas(c.locked) : ''), field: FIELDS.signed + ' · ' + FIELDS.participation + ' · ' + FIELDS.locked, progress: -1 });
    // 3. the full window
    var d = daysIn(ld, c.windowS, now), full = first && d.have >= d.of;
    rungs.push({ id: 'window', name: 'Full window', state: full ? 'done' : first ? 'now' : 'next', line: full ? d.of + ' of ' + d.of + ' ' + d.unit + '. Full weight.' : d.have + ' of ' + d.of + ' ' + d.unit + '.', sub: 'weight is your blocks over the last ' + windowWords(c.windowS).text + '; a key that mined the whole window weighs what it mined', field: FIELDS.days + ' · ' + FIELDS.window, progress: d.of ? Math.min(1, d.have / d.of) : -1 });
    // 4. rank by weight
    var rankLine = inTable ? 'Rank ' + withCommas(best.rank) + ' of ' + withCommas(c.listed) + ' keys.' : c.ok ? (c.cap ? 'Below the top ' + c.cap + ' keys.' : 'Not in the table yet.') : 'Rank: not read yet.';
    rungs.push({ id: 'rank', name: 'Rank by weight', state: inTable ? 'done' : first ? 'now' : 'next', line: rankLine, sub: c.ok ? withCommas(c.voters) + ' keys vote' + (c.cap && c.listed >= c.cap ? ' · the top ' + c.cap + ' are listed' : '') + ' · weight, never hashrate: a card that arrived today sits at the bottom' : '', field: FIELDS.rank + ' · ' + FIELDS.voters, progress: -1 });
    // 5. the signing streak
    var streak = ld.streak_s || 0;
    rungs.push({ id: 'streak', name: 'Signing streak', state: streak > 0 ? 'done' : voter ? 'now' : 'next', line: streak > 0 ? 'Signing 8 of 80 points, ' + spanWords(streak) + ' unbroken.' : 'The 8 signing points start with the vote.', sub: streak > 0 ? 'miss two hours of signing and the next blocks pay 72 until you sign again' + (inTable ? ' · presence ' + Math.round(part * 100) + '%' : '') : '', field: FIELDS.streak + ' · ' + FIELDS.participation, progress: -1 });
    // P. a paid shard
    var sh = (ld.shards || [])[0], prove = !!(s.settings && s.settings.prove);
    rungs.push({ id: 'shard', name: 'Your card proved a shard', state: sh ? 'done' : prove ? 'now' : 'off', line: sh ? 'Your card proved shard ' + sh.shard + ' of block ' + withCommas(sh.block) + '.' : prove ? 'Proving: the first paid shard lands here.' : 'Proving is off.', sub: sh ? fmtIgn(ign(sh.wei)) + ' · ' + withCommas(pv.paid || 0) + ' paid' : prove ? (pv.message || '') : 'The Prove page switches it on; 20 points of every block pay the provers', field: FIELDS.shards, progress: -1 });
    return rungs;
  }
  function cardName(s, key) { var l = ((s.mining || {}).cards || []).filter(function (c) { return c.key === key; }); return l.length ? l[0].name : ''; }
  // the rung to put on the Overview strip: the first that is "now", else the last done
  function currentRung(rungs) { for (var i = 0; i < rungs.length; i++) if (rungs[i].state === 'now') return rungs[i]; for (var j = rungs.length - 1; j >= 0; j--) if (rungs[j].state === 'done') return rungs[j]; return rungs[0]; }
  // the block card: m = state.ladder.milestone, s = state, chain, now. {kind, eyebrow, title, when, cardLine, ignLine,
  // rankLine, hash, url, fields}
  // the Poisson count-up card shows only while this machine waits for its first ever block (first-block-21): never
  // once the ladder records one, whatever the lifetime count says after a settings reset
  function firstWait(s) {
    s = s || {}; var m = s.mining || {}, ld = s.ladder || {};
    // on the share lane the wait is for a share, not a block: the shares tile carries it, never the count-up
    return m.state === 'mining' && !m.share_lane && !(m.accepted_total > 0) && !(ld.first_block_at > 0) && !ld.first_block_shown;
  }
  // a card is seen once (first-block-21, the founder, 7 October 2026: "the 'you got your first block' situation only shows
  // for the miner's first ever block"; main's reading: seen once). An engine of an earlier release or later owns the rule: it
  // keeps a card no window has shown across restarts and updates (a block found overnight greets the miner at the
  // first open) and drops a seen one at its next start (ladder.rs start_run, the page's POST api/card/seen), so
  // whatever such an engine sends is shown. A an earlier release engine (its ladder has no first_block_shown field) re-sends the
  // persisted card on every run: against it a milestone raised before this run began is refused here (a minute of
  // slack; uptime_s stands in when the engine has no started_at).
  function staleCard(m, s, now) {
    if (!m || !(m.at > 0) || !s) return false;
    if (s.ladder && s.ladder.first_block_shown !== undefined) return false;
    var started = s.started_at > 0 ? s.started_at : (s.uptime_s >= 0 && now > 0 ? now - s.uptime_s : 0);
    return started > 0 && m.at + 60 < started;
  }
  // the body the page sends to api/card/seen for a card it displays or the user dismisses (null for no card)
  function cardSeenBody(bc) { return bc && bc.count > 0 ? { count: bc.count, at: bc.when || 0 } : null; }
  function blockCard(m, s, chain, now) {
    if (!m || !(m.count > 0)) return null;
    if (staleCard(m, s, now)) return null;
    var c = chainNums(chain), cards = (s.mining || {}).cards || [], cd = cards.filter(function (x) { return x.key === m.card; })[0] || {};
    var name = m.card_name || cd.name || 'your card', mhs = cd.hash_avg > 0 ? cd.hash_avg : cd.hash_now || 0;
    var eyebrow = m.kind === 'first' ? 'your first block' : m.kind === 'hundred' ? 'block 100 of this machine' : m.kind === 'thousand' ? 'block 1,000 of this machine' : m.kind === 'ten_thousand' ? 'block ' + withCommas(m.count) + ' of this machine' : 'first block on ' + name;
    var title = m.daa > 0 ? 'Block ' + withCommas(m.daa) + ' is yours.' : 'A block is yours.';
    var share = mhs > 0 && c.hps ? ' · ' + pct(mhs * 1e6 / c.hps) + ' of the network' : '';
    var cardLine = name + (mhs > 0 ? ' · ' + laneRateText(mhs * 1e6, cd.lane_word) : '') + (laneUnit(cd.lane_word) === 'hashes' ? share : '');
    var addr = (s.address || {}).display || '';
    var ignLine = c.minerIgn ? fmtIgn(c.minerIgn) + (addr ? ' to ' + shortHex(addr, 6, 4) : '') + ' (72 producer points, 8 for signing' + (c.ramp && c.ramp < 1 ? ', ramp ' + Math.round(c.ramp * 100) + '%' : '') + ')' : (addr ? 'Paid to ' + shortHex(addr, 6, 4) : '');
    var d = daysIn(s.ladder, c.windowS, now), rankLine = (c.best ? 'rank ' + withCommas(c.best.rank) + ' of ' + withCommas(c.listed) + ' keys' : 'rank: not in the table yet') + ' · ' + (d.unit === 'days' ? 'day ' : 'hour ') + Math.max(1, d.have) + ' of ' + d.of;
    return { kind: m.kind, count: m.count, eyebrow: eyebrow, title: title, when: m.at, cardLine: cardLine, card: { name: name, vendor: cd.vendor, kind: cd.kind, code: cd.code }, ignLine: ignLine, rankLine: rankLine, hash: m.hash || '', hashShort: m.hash ? shortHex(m.hash, 8, 6) : 'hash: waiting for the miner’s line', url: m.hash ? 'https://igneum.network/block/' + m.hash : '', key: 'card:' + m.count + ':' + Math.floor(m.at || 0), fields: { card: FIELDS.found, ign: FIELDS.reward, rank: FIELDS.rank + ' · ' + FIELDS.days } };
  }
  // Earnings (reinvent 3.4): IGN first, the window, a typed price, the share and rank, IGN per kWh. price = £ per IGN
  // typed by the user (null = none), pence = electricity, now = unix s
  function uptimeWords(sec) { sec = sec || 0; var h = Math.floor(sec / 3600), mi = Math.floor(sec % 3600 / 60); return h ? h + ' h ' + mi + ' min' : mi + ' min'; }
  // the Earnings card (earnings-tidy, 7 October 2026): the day rate with its reason, the run line, then weight,
  // electricity and lifetime, and the dev-fee sentence. IGN only: no typed price, no money but the electricity cell.
  // app-ia-26 (the founder's audit, 4.3): what this machine earned, measured. The windows come from the engine's earned
  // sums (state.ladder.earned, earned-windows-26: what the chain paid at the time, never today's reward applied to old
  // blocks); an engine without them reads "reading" with the block counts. Electricity is one row, Payouts one line,
  // Standing one row (the rungs sit behind Details on the page), and the projection is one dim foot line that hides,
  // with its reason, under a remote job, a pause, a still chain, no card mining or an unread network rate.
  function windowOf(w, blocksOnly, pool, allTime) {
    w = w || null;
    var blocks = w ? (w.blocks || 0) : (blocksOnly || 0), shards = w ? (w.shards || 0) : 0, ign = w && w.ign >= 0 ? w.ign : null;
    var parts = [];
    // on a pool session the counted units are the shares the pool accepted, never blocks (the first real captures,
    // 12 October 2026: "1,630 blocks" on the mini's Earnings page under the pool); the chain's credit is the IGN figure;
    // the 24 h and 7 d windows are the node's block windows and are not kept for shares, so they say so
    if (pool) { parts.push(!allTime ? 'the window is not kept on a pool session; the pool keeps the count' : blocks ? withCommas(blocks) + ' share' + (blocks === 1 ? '' : 's') + ' the pool accepted' : 'no share yet'); }
    else if (!blocks && !shards) parts.push('no block yet');
    else { parts.push(withCommas(blocks) + ' block' + (blocks === 1 ? '' : 's')); if (shards) parts.push(withCommas(shards) + ' shard' + (shards === 1 ? '' : 's')); }
    if (!pool && w && w.priced === false) parts.push('one block not priced yet');
    return { ign: ign, blocks: blocks, shards: shards, text: ign === null ? 'reading' : (ign > 0 ? fmtIgn(ign) : '0 IGN'), sub: parts.join(' · '), field: 'ladder.earned: the coinbase and shard records the node credited, at the reward each paid' };
  }
  function earningsLines(s, chain, pence, now, minor) {
    s = s || {}; var m = s.mining || {}, ld = s.ladder || {}, n = s.node || {}, j = s.jobs || {}, a = s.address || {}, c = chainNums(chain), ea = ld.earned || null;
    var out = {};
    // an engine before the windows: the counts come from the ladder's own record, never zero (blocks_24h, else the block
    // list filtered to the day; the day ring for 7 days; the lifetime count for all time)
    var day24 = ld.blocks_24h >= 0 ? ld.blocks_24h : (ld.blocks || []).filter(function (b) { return b.at > now - 86400; }).length;
    var day7 = (ld.days || []).filter(function (d) { return (d.day + 1) * 86400 > now - 7 * 86400; }).reduce(function (a, d) { return a + (d.blocks || 0); }, 0);
    // a pool session as the state says it: the source set to a pool, or nothing set on a build with no node inside
    // (the engine's default is the project's pool)
    var sk = (s.settings || {}).source_kind || '';
    var pool = sk === 'pool' || (sk === '' && /^no node runs inside/.test(n.message || ''));
    out.h24 = windowOf(ea && ea.h24, day24, pool, false); out.d7 = windowOf(ea && ea.d7, day7, pool, false); out.all = windowOf(ea && ea.all, m.accepted_total || 0, pool, true);
    if (ea && ea.estimated && out.all.blocks) out.all.sub += ' · estimated';
    // the founder's word of 12 October 2026: devnet9 is a public test network; where Ember shows a balance it says so
    out.headline = { text: out.h24.text, eyebrow: 'last 24 hours', sub: (out.h24.sub ? out.h24.sub + ' · ' : '') + NO_VALUE_LABEL, field: out.h24.field };
    // electricity: one row (A4: one sentence for the absence)
    var watts = fleetWatts(m, m.cards), ppd = fleetPounds(m, m.cards, pence), cards = (m.cards || []).filter(present);
    if (!(watts > 0)) out.cost = { text: cards.some(function (x) { return x.vendor === 'apple'; }) ? 'No power reading on Apple silicon.' : 'No power reading.', sub: '', field: FIELDS.watts };
    else if (ppd === null) out.cost = { text: Math.round(watts) + ' W', sub: 'Set a price in Settings for the cost.', field: FIELDS.watts };
    else out.cost = { text: money(ppd) + ' a day', sub: 'at ' + (pence / priceFactor(currency.code)) + (minor || minorOf(currency.code).abbr) + '/kWh for ' + Math.round(watts) + ' W', field: FIELDS.watts + ' × 24 × the price in Settings' };
    // payouts
    out.payouts = { text: a.display ? 'Pays ' + a.display.slice(0, 6) + '…' + a.display.slice(-4) : 'No address yet', full: a.display || '', sub: a.display ? 'Your balance is in the wallet. Change the address in Settings.' : 'Finish the setup to choose one.' };
    // standing: the rank and the signing points, the window in the sub
    var d = daysIn(ld, c.windowS, now), ww = windowWords(c.windowS), streak = ld.streak_s || 0;
    var rank = c.best ? 'Rank ' + withCommas(c.best.rank) + ' of ' + withCommas(c.listed) + ' keys' : c.ok ? (c.cap ? 'Below the top ' + c.cap + ' keys' : 'Not in the weight table yet') : 'Standing: not read';
    out.standing = { text: c.best ? rank + ' · ' + (streak > 0 ? 'signing 8 of 80 points' : 'no signing points yet') : rank, sub: c.ok ? 'weight is your blocks over the last ' + ww.text + ', ' + d.have + ' of ' + d.of + ' ' + d.unit + ' mined' : (c.error || ''), field: FIELDS.rank + ' · ' + FIELDS.days + (c.source === 'observer' ? ' · through igneum.network’s observer until this node answers igneum_getFinalityWeights' : '') };
    // the projection: one line, or its reason
    var mhs = m.hash_total || 0, expected = ignPerDay(mhs, c.hps, c.minerIgn, c.bps), held = j.active || m.state === 'held' || (m.cards || []).some(function (cd) { return cd.state === 'held'; });
    out.projection = null; out.projectionNote = '';
    if (held) out.projectionNote = 'Projection paused: ' + (j.active && j.title ? j.title + ' running since ' + hhmm(j.started_at) : 'a job from the team running') + '.';
    else if (n.tip_age_s >= NETWORK_PAUSED_S) out.projectionNote = 'Network paused: no block on the chain for ' + spanWords(n.tip_age_s) + '.';
    else if (m.paused || m.state === 'paused') out.projectionNote = '';
    else if (m.state !== 'mining' || !(mhs > 0)) out.projectionNote = '';
    else if (expected === null || !c.hps) out.projectionNote = '';
    else out.projection = 'About ' + fmtIgn(expected) + ' a day at ' + (mhs >= 100 ? mhs.toFixed(0) : mhs.toFixed(1)) + ' MH/s, ' + pct(mhs * 1e6 / c.hps) + ' of the network.';
    out.projectionField = FIELDS.share + ' × 86,400 × ' + FIELDS.reward;
    return out;
  }
  // the rail foot (T-R11): the machine's name and its uptime; the address lives in Settings and on the Payouts line
  // ?capture=1 (main's capture mode, 14 October 2026): the rail's foot names no machine ("this machine"), so a capture from any
  // PC can serve the site; the receipts row carries no file path in that mode either
  function railFoot(s, capture) { s = s || {}; return { name: capture ? 'this machine' : (s.display_name || ''), up: 'up ' + uptimeFoot(s.uptime_s) }; }
  function uptimeFoot(u) { u = Math.max(0, Math.round(u || 0)); var h = Math.floor(u / 3600), mi = Math.floor(u % 3600 / 60); return h ? h + ' h ' + mi + ' min' : mi + ' min'; }
  // the first hour as this machine lived it: steps with unix times (0 = not yet), from the settings' install stamp,
  // the ladder's marks and the engine's events
  function timeline(s, now) {
    s = s || {}; var ld = s.ladder || {}, st = s.settings || {}, n = s.node || {}, m = s.mining || {};
    var installed = s.installed_at > 0 ? s.installed_at : 0, started = s.uptime_s >= 0 && now ? now - s.uptime_s : 0;
    var synced = ld.first_synced_at > 0 ? ld.first_synced_at : (n.synced && started ? started : 0);
    var mining = ld.first_mining_at > 0 ? ld.first_mining_at : (m.state === 'mining' && started ? started : 0);
    var steps = [
      { id: 'install', name: 'Installed', at: installed, note: installed ? '' : 'not recorded on this install' },
      { id: 'started', name: 'App started', at: started, note: '' },
      { id: 'synced', name: 'Node synced', at: synced, note: synced ? (ld.first_synced_at > 0 ? '' : 'the moment is not recorded; synced now') : 'syncing' },
      { id: 'mining', name: 'Card mining', at: mining, note: mining ? (ld.first_mining_at > 0 ? '' : 'the moment is not recorded; mining now') : 'waiting for work' },
      { id: 'block', name: 'First block', at: ld.first_block_at || 0, note: ld.first_block_at ? '' : 'waiting' },
      { id: 'payout', name: 'First payout', at: ld.first_block_at || 0, note: ld.first_block_at ? 'the coinbase of the first block, yours when the block is blue' : 'the first block pays, seconds later' }
    ];
    var base = installed || started;
    steps.forEach(function (x) { x.rel = x.at > 0 && base > 0 && x.at >= base ? '+' + spanWords(x.at - base) : ''; });
    return { base: base, steps: steps, field: 'settings.installed_at, uptime_s, ladder.first_synced_at, ladder.first_mining_at, ladder.first_block_at' };
  }
  // the page's address line in Settings (main's order of 11:0x UK, 12 October 2026): the fixed port, the LAN bind,
  // and the step when the port wanted was in use; nothing until the engine names its port
  function pageLine(p) {
    if (!p || !p.port) return '';
    var s = 'Page http://127.0.0.1:' + p.port + '/';
    if (p.lan) s += ' · also on every address of this machine';
    if (p.wanted && p.wanted !== p.port) s += ' · port ' + p.wanted + ' was in use';
    return s;
  }
  // the pool's admission bounds in plain words (the case word is the pool's reply's second field; 0.2.4)
  function heldWords(c) {
    c = String(c || '').toLowerCase();
    if (c === 'rate') return 'by the pool’s rate bound; not sent again';
    if (c === 'pool at carriage') return 'while the pool is at carriage; they clear as the chain carries';
    if (c === 'address at cap') return 'while your address is at its cap; they clear as blocks seal';
    if (c.indexOf('capacity') >= 0) return 'while the pool’s node is at capacity';
    if (c.indexOf('list bound') >= 0) return 'at the pool’s list bound';
    return 'by the pool (' + c + ')';
  }
  function profileWords(on) { return on ? 'Your address page on igneum.network is public: your shares the pool accepted and the blocks that carry them, weight rank, signing presence and card model are shown. The site reads it from the chain and from this switch.' : 'Off: igneum.network/address keeps your page unlisted (noindex). Nothing about this machine is published.'; }
  // the confirmations' words (in place, never a dialog)
  // ui-ota (src/uiota.rs): Settings > Interface. u = state.ui. {line, eyebrow, help}
  function interfaceWords(u) {
    u = u || {};
    var ver = u.active_version || u.embedded_version || '';
    if (!ver) return { line: '', eyebrow: '', help: '' };
    var ota = u.source === 'ota';
    var when = ota && u.installed_at > 0 ? dateWords(u.installed_at) : '';
    var line = 'Interface ' + ver + (ota ? ', over the air' + (when ? ', ' + when : '') : ', built in');
    var help = '';
    if (ota && !u.confirmed) help = 'Waiting for this window to confirm the new interface.';
    else if (u.error) help = 'The last interface over the air was refused: ' + u.error + ' The built-in one serves.';
    else if (u.builtin && u.published_version && u.published_version !== u.embedded_version) help = 'Interface ' + u.published_version + ' is published over the air; switch this off to take it.';
    else if (u.busy) help = 'Interface ' + u.published_version + ' is downloading.';
    else if (u.note) help = cap(u.note) + '.';
    else if (ota) help = 'Built in: ' + (u.embedded_version || '') + '.';
    return { line: line, eyebrow: ota ? 'over the air' : 'built in', help: help };
  }
  function dateWords(unix) { var d = new Date(unix * 1000); return d.getDate() + ' ' + ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][d.getMonth()] + ' ' + d.getFullYear(); }
  // the page reloads itself when the served interface changed under it and nobody is mid-task:
  // loaded = the version this page came up with, u = state.ui, ctx = {typing, sheetOpen, phase}
  function shouldReload(loaded, u, ctx) {
    if (!loaded || !u || !u.active_version || u.active_version === loaded) return false;
    ctx = ctx || {};
    if (ctx.typing || ctx.sheetOpen) return false;
    if (ctx.phase && ctx.phase !== 'dashboard') return false;
    return true;
  }
  function addressAsk(a) { return 'Pay ' + shortHex(a, 6, 4) + ' from the next share? Mining restarts.'; }
  function trustAsk(on) { return on ? 'Include proofs the node never checked? Test networks only. The node restarts.' : 'Check proofs again? The node restarts.'; }
  // 0.2.8, the Fleet item: the rail's Fleet entry follows the settings (a console URL set); the token is never on the page
  function fleetWords(st) {
    var f = (st && st.settings) || {}; var url = f.fleet_console_url || '', tok = !!f.fleet_token_set;
    return { on: !!url, url: url, tokenSet: tok, eyebrow: url ? (tok ? 'set, a token held' : 'set, no token') : 'off',
      note: url ? (tok ? 'Fleet is in the rail; the console answers through Ember’s window.' : 'Set a window token: the console refuses reads without one.') : 'Not set: the rail shows no Fleet item.' };
  }
  // F-E39 (the pool seat's feed of 09:08 UK, 14 October 2026): pools.json carries concentration.share_of_work_7d (a
  // fraction) with status "partial window" while the writer reads the chain back, "published" once the window is
  // whole and "not published" for a chain without a public archive; the older share_7d_pct (a percentage) still reads
  function poolSharePct(c) {
    c = c || {};
    if (c.share_of_work_7d != null && isFinite(Number(c.share_of_work_7d))) return Number(c.share_of_work_7d) * 100;
    if (c.share_7d_pct != null && isFinite(Number(c.share_7d_pct))) return Number(c.share_7d_pct);
    return null;
  }
  function poolShareTile(p) {
    var c = p.concentration || {}, v = poolSharePct(c), status = String(c.status || '');
    if (v == null || status === 'not published') return { v: 'not published', k: 'share of network' + (status === 'not published' && c.measure ? ' · ' + c.measure : ''), dim: true, warn: false };
    var warn = v > (c.warn_above_pct || 33), partial = status === 'partial window';
    var k = 'share of network, 7 days' + (partial ? ' · partial window' + (c.blocks_read && c.window_blocks ? ' (' + withCommas(c.blocks_read) + ' of ' + withCommas(c.window_blocks) + ' blocks read)' : '') : '') + (warn ? ' · above a third' : '');
    return { v: v.toFixed(1) + '%', k: k, dim: partial, warn: warn };
  }
  return { BLOCK_S: BLOCK_S, stripTiles: stripTiles, idleReason: idleReason, poolSharePct: poolSharePct, poolShareTile: poolShareTile, fleetWords: fleetWords, dagStateWords: dagStateWords, mineShareWords: mineShareWords, capWords: capWords, hhmm: hhmm, railFoot: railFoot, pageLine: pageLine, heldWords: heldWords, cardsRowWords: cardsRowWords, workerUp: workerUp, blockState: blockState, productName: productName, proveModeLine: proveModeLine, lockKindWords: lockKindWords, NETWORK_PAUSED_S: NETWORK_PAUSED_S, tuneLine: tuneLine, capSentence: capSentence, powerNote: powerNote, tuneCardHtml: tuneCardHtml, tierButtonsHtml: tierButtonsHtml, curveHtml: curveHtml, MINERS_SENTENCE: MINERS_SENTENCE, CURVE_CAPTION: CURVE_CAPTION, NETWORKS: NETWORKS, networkWords: networkWords, networkAsk: networkAsk, sourceWords: sourceWords, networkIdOf: networkIdOf, ISO_CURRENCY: ISO_CURRENCY, EU_MEMBERS: EU_MEMBERS, CURRENCY_CODES: CURRENCY_CODES, minorOf: minorOf, priceFactor: priceFactor, currencyInfo: currencyInfo, regionFromLocale: regionFromLocale, currencyChoice: currencyChoice, currencyAsk: currencyAsk, applyMoney: applyMoney, fmtMoney: fmtMoney, setLocale: setLocale, setCurrency: setCurrency, currencyNow: currencyNow, unitWords: unitWords, VENDORS: VENDORS, MARKS: MARKS, WELL_ALPHA_LIGHT: WELL_ALPHA_LIGHT, WELL_ALPHA_DARK: WELL_ALPHA_DARK, vendorOf: vendorOf, seriesLine: seriesLine, vendorMark: vendorMark, markHtml: markHtml, nameHtml: nameHtml, rowHead: rowHead, vendorContrast: vendorContrast, contrast: contrast, hexRgb: hexRgb, PAGES: PAGES, page: page, withCommas: withCommas, compact: compact, rel: rel, ago: ago, shortHex: shortHex, kindWord: kindWord, shownCards: shownCards, present: present, cardTitle: cardTitle, cardRow: cardRow, rowWord: rowWord, driverWords: driverWords, vendorWord: vendorWord, tuneWord: tuneWord, tuneEta: tuneEta, tuningCard: tuningCard, poundsPerDay: poundsPerDay, money: money, effOf: effOf, effText: effText, effParts: effParts, wattsTotal: wattsTotal, noReading: noReading, cells: cells, nextCheck: nextCheck, tunePoint: tunePoint, tunable: tunable, tuneWords: tuneWords, schedule: schedule, GOALS: GOALS, GOAL_NAMES: GOAL_NAMES, goalWords: goalWords, ign: ign, balanceWords: balanceWords, fleetWatts: fleetWatts, fleetPounds: fleetPounds, isMine: isMine, liveNote: liveNote, CHAIN_NAMES: CHAIN_NAMES, shortCardName: shortCardName, toggle: toggle, pressToggle: pressToggle, clickSettled: clickSettled, engineOff: engineOff, PENDING_MS: PENDING_MS, nodeWords: nodeWords, nodeLine: nodeLine, skewWord: skewWord, peersLine: peersLine, heightLine: heightLine, tipAge: tipAge, nodeSource: nodeSource, finalityWords: finalityWords, nextSwitch: nextSwitch, switchLine: switchLine, proveWords: proveWords, verifierWords: verifierWords, proveTier: proveTier, proveInsteadWords: proveInsteadWords, proveLine: proveLine, devFeeText: devFeeText, devFeeLine: devFeeLine, devFeeEarningsRow: devFeeEarningsRow, NO_VALUE_LABEL: NO_VALUE_LABEL, jobsNote: jobsNote, eventLine: eventLine, plainEvent: plainEvent, pill: pill, beforeOf: beforeOf, ember: ember, lockWords: lockWords, kneeWords: kneeWords, TIERS: TIERS, DEFAULT_PRICE_MINOR: DEFAULT_PRICE_MINOR, tierOf: tierOf, tierName: tierName, priceOrDefault: priceOrDefault, tierSet: tierSet, fleetTiers: fleetTiers, tierSentence: tierSentence, classFlip: classFlip, minuteOf: minuteOf, kneeNote: kneeNote, addressAsk: addressAsk, trustAsk: trustAsk, interfaceWords: interfaceWords, shouldReload: shouldReload, FIELDS: FIELDS, fmtIgn: fmtIgn, ignText: ignText, pct: pct, gapWords: gapWords, spanWords: spanWords, chainNums: chainNums, chainWord: chainWord, poisson: poisson, ignPerDay: ignPerDay, earnCreditTile: earnCreditTile, deadTiles: deadTiles, clockWords: clockWords, themeChoice: themeChoice, fleetUrlShown: fleetUrlShown, laneUnit: laneUnit, rateWords: rateWords, laneRateText: laneRateText, machineRate: machineRate, laneRows: laneRows, poolsOffered: poolsOffered, poolsMerged: poolsMerged, PIN_CHAIN_ID: PIN_CHAIN_ID, KNEE_RULE: KNEE_RULE, stripCapacity: stripCapacity, firstShareMark: firstShareMark, WAITING_FOR_WORK_S: WAITING_FOR_WORK_S, waitingForWork: waitingForWork, hostPort: hostPort, noReadingWords: noReadingWords, windowWords: windowWords, daysIn: daysIn, ladderRungs: ladderRungs, currentRung: currentRung, firstWait: firstWait, staleCard: staleCard, cardSeenBody: cardSeenBody, blockCard: blockCard, earningsLines: earningsLines, ledgerWords: ledgerWords, LEDGER_URL: LEDGER_URL, timeline: timeline, profileWords: profileWords, REGIONS: REGIONS, CURRENCIES: CURRENCIES, RESTRICTED_SENTENCE: RESTRICTED_SENTENCE, regionOf: regionOf, currencyOf: currencyOf, setRegion: setRegion, restrictedLine: restrictedLine, priceNote: priceNote, RENT: RENT, rentLine: rentLine, heatLine: heatLine, heatRowWords: heatRowWords };
})();
/* ---------- Ember Tune: the card row's tuning line (pure; tune-line.test.mjs loads this block) ----------
   One line per card from the card state (src/state.rs, src/ember.rs): running (the phase and the step), tuned
   ("Tuned: 122.3 MH/s at 290 W (0.422 MH/W)" plus the point and when), measure only (Apple, NVIDIA without Power
   control: the measured line and why nothing is set), stopped (the reason), or not run yet. The Settings-page block
   of miner-ui-2; miner-ui-3 reads View.tuneWords for the row and keeps this for the details and ember-tune's test. */
var TuneLine = (function () {
  'use strict';
  function point(cd) {
    var pct = cd.sweep_pct || cd.power_pct || 0;
    if (cd.tune_clock_mhz > 0) return cd.tune_clock_mhz + ' MHz at ' + pct + '%';
    return pct ? pct + '%, clock unlocked' : '';
  }
  // the model: {kind: running|tuned|measured|stopped|idle|off, text, note}
  function model(cd, now) {
    cd = cd || {};
    if (cd.vendor === 'intel') return { kind: 'off', text: 'tuning: measure only on Intel Arc once a power reading exists', note: 'no cap through OpenCL; IGCL next' };
    if (cd.vendor !== 'nvidia' && cd.vendor !== 'amd' && cd.vendor !== 'apple') return { kind: 'off', text: cd.sweep_note || 'tuning: no power or clock control for this card' };
    if (cd.sweep_state === 'running') return { kind: 'running', text: cd.sweep_note || 'tuning: running' };
    var when = cd.sweep_at && now ? ', ' + rel(now - cd.sweep_at) + ' ago' : '';
    if (cd.tune_line) {
      if (cd.tune_source === 'baseline' || !cd.tune_control) return { kind: 'measured', text: cd.tune_line + ' (measured as it runs' + when + ')', note: cd.tune_control ? '' : (cd.sweep_note || '') };
      var src = cd.tune_source === 'confirm' ? 'from the fleet prior, confirmed' : 'full tune';
      return { kind: 'tuned', text: cd.tune_line, note: point(cd) + ', ' + src + when + (cd.pinned ? '; your setting stays pinned' : '') + (cd.sweep_note && cd.sweep_note.indexOf('stopped') === 0 ? '; ' + cd.sweep_note : '') };
    }
    if (cd.sweep_note && cd.sweep_note.indexOf('tuning stopped') === 0) return { kind: 'stopped', text: cd.sweep_note };
    return { kind: 'idle', text: cd.sweep_note || 'tuning: not run yet (starts after 120 s of steady mining)' };
  }
  function rel(s) { s = Math.max(0, Math.floor(s)); return s < 60 ? s + ' s' : s < 3600 ? Math.floor(s / 60) + ' min' : s < 86400 ? Math.floor(s / 3600) + ' h' : Math.floor(s / 86400) + ' d'; }
  // Ember 2: £ a day for a draw at a price in pence per kWh (the same formula as View.poundsPerDay and ember::pounds_per_day)
  function pounds(watts, pence) { return View.money(View.poundsPerDay(watts, pence)); }
  return { model: model, point: point, rel: rel, pounds: pounds };
})();
if (typeof module === 'object' && module && module.exports) { module.exports = Notices; module.exports.UpdateCard = UpdateCard; module.exports.View = View; module.exports.TuneLine = TuneLine; module.exports.tuneNotice = Notices.tuneNotice; }

if (typeof document !== 'undefined') (function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var dpr = Math.min(2, window.devicePixelRatio || 1);
  var state = null, phase = 'welcome', page = 'mine', forced = null, cardsSeen = false, keyShownThisRun = false;
  var params = new URLSearchParams(location.search);
  // the web view's language is the OS locale; ?lang=de-DE overrides it for screenshots and tests
  var lang = params.get('lang') || navigator.language || 'en-GB'; View.setLocale(lang);
  if (params.get('screen')) forced = params.get('screen');
  if (params.get('page')) page = params.get('page'); // ?page=mine|tune|earnings|settings (screenshots)
  var forcedUpdate = params.get('update'); // a sample update state for screenshots: available|downloading|ready|waiting|applying|urgent|manual|error|updated
  var forcedJob = params.get('job'); // a sample remote job for screenshots: running|done|failed
  if (params.get('host') === 'mac') document.body.classList.add('mac');
  // ?settle=1 (captures): the pages' rise animation is off; a web view whose animations never advance (the window
  // host's snapshot mode on a machine with its display asleep, the mini on 13 October 2026) held every body at opacity 0
  if (params.get('settle') === '1') document.body.classList.add('settle');
  CAPTURE = params.get('capture') === '1';
  // ?debug=1 prints a budget line every 5 s (ms of JS per second in the canvas, the state render and the log) and
  // exposes the counters as window.__igneumPerf
  var debug = params.get('debug') === '1';
  // the hosts hide the window to the menu bar or the tray: nothing draws or polls fast while document.hidden.
  // ?visible=1 ignores that (screenshots from a hidden pane).
  // ?settle=1 (captures) counts as visible too: a snapshot web view on a machine with its display asleep reads
  // document.hidden, which gated the log and chain polls off (the mini's capture set of 13 October 2026)
  var forceVisible = params.get('visible') === '1' || params.get('settle') === '1';
  function hiddenNow() { return document.hidden && !forceVisible; }
  var perf = { frames: 0, draw: 0, render: 0, log: 0, poll: 0, polls: 0, since: performance.now() };
  if (debug) {
    window.__igneumPerf = perf;
    setInterval(function () {
      var s = (performance.now() - perf.since) / 1000;
      console.log('[perf] ' + (hiddenNow() ? 'hidden' : 'visible') + ' ' + (perf.frames / s).toFixed(1) + ' fps · js ms/s: canvas ' + (perf.draw / s).toFixed(2) + ' render ' + (perf.render / s).toFixed(2) + ' log ' + (perf.log / s).toFixed(2) + ' poll ' + (perf.poll / s).toFixed(2) + ' · ' + (perf.polls / s).toFixed(2) + ' polls/s · total ' + ((perf.draw + perf.render + perf.log + perf.poll) / s / 10).toFixed(2) + '% of one core');
      perf.frames = 0; perf.draw = 0; perf.render = 0; perf.log = 0; perf.poll = 0; perf.polls = 0; perf.since = performance.now();
    }, 5000);
  }

  // ---------- helpers ----------
  function api(path, body) {
    var opt = body === undefined ? { cache: 'no-store' } : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
    return fetch(path, opt).then(function (r) { return r.json(); });
  }
  // 0.2.8, the Fleet item: the rail's Fleet entry follows the settings (a console URL set), the frame loads the console
  // under the engine's own origin when the section opens, and the Settings card shows the URL and whether a token is held
  var fleetLoadedFor = '';
  function renderFleetSettings(st) {
    var w = fleetWords(st), nav = $('nav-fleet');
    if (nav) nav.hidden = !w.on;
    if ($('s-fleet-eyebrow')) setText('s-fleet-eyebrow', w.eyebrow);
    if ($('s-fleet-note')) setText('s-fleet-note', w.note);
    var u = $('s-fleet-url'); if (u && document.activeElement !== u) { u.dataset.real = w.url; var shown = View.fleetUrlShown(w.url); if (u.value !== shown) u.value = shown; } // the token directory masked off focus (the secrets order)
    if (!w.on && page === 'fleet') showPage('mine');
  }
  function showFleet() {
    var w = fleetWords(state), fr = $('fleet-frame');
    if (!fr) return;
    if (!w.on) { setText('fleet-eyebrow', 'no console set'); setText('fleet-foot', 'Set the console address under Settings, Fleet console.'); return; }
    setText('fleet-eyebrow', w.tokenSet ? 'your console' : 'no token held');
    setText('fleet-foot', 'Served through api/fleet/ on this window’s own origin; the console’s own rail hides there.');
    if (fleetLoadedFor !== w.url) { fleetLoadedFor = w.url; fr.src = 'api/fleet/console.html'; }
  }
  function esc(s) { return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
  var compact = View.compact, withCommas = View.withCommas, rel = View.rel, kindWord = View.kindWord, shownCards = View.shownCards, cardTitle = View.cardTitle, money = View.money;
  // the 0.2.8 Fleet and pool-share words live in the module (their tests read them there); the app part calls them by these
  // names. A bare name here threw "fleetWords is not defined" on the served b6321390 and every render after it stopped at the
  // Settings' Fleet card (the Mine page's rate tile stayed at its load words while the pill updated; read 10:36 UK, 14 October 2026)
  var fleetWords = View.fleetWords, poolShareTile = View.poolShareTile, poolSharePct = View.poolSharePct, ignText = View.ignText;
  function hms(s) { s = Math.max(0, Math.round(s)); var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60; return (h ? h + ':' : '') + (h ? String(m).padStart(2, '0') : m) + ':' + String(x).padStart(2, '0'); }
  function uptime(s) { var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60); return h ? h + ' h ' + m + ' min' : m + ' min'; }
  function clock(t) { var d = new Date(t * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0') + ':' + String(d.getSeconds()).padStart(2, '0'); }
  function setText(id, text) { var el = $(id); if (el && el.textContent !== text) { el.textContent = text; var p = el.parentElement; if (p && p.classList && p.classList.contains('fig')) p.classList.toggle('nomark', !/^[\d]/.test(String(text || ''))); } }
  // the brand marks beside figures (main's design, 22:0x UK, 13 October 2026): the ember bolt before a hash rate, the
  // Igneum coin before an IGN amount; one sprite symbol each (index.html), currentColor, hidden while the figure is a word
  function glyph(kind) { return '<svg class="ig-glyph ' + kind + '" aria-hidden="true"><use href="#g-' + kind + '"/></svg>'; }
  function markedIgn(text) { text = String(text || ''); return /^\d/.test(text) && /\bIGN\b/.test(text) ? glyph('coin') + esc(text) : esc(text); }
  function setHidden(id, hidden) { var el = $(id); if (el && el.hidden !== !!hidden) el.hidden = !!hidden; }
  function store(key, val) { try { if (val === null) localStorage.removeItem(key); else localStorage.setItem(key, String(val)); } catch (e) { } }
  function stored(key) { try { return localStorage.getItem(key); } catch (e) { return null; } }
  var toastT = null;
  function toast(text) { var t = $('toast'); t.textContent = text; t.hidden = false; clearTimeout(toastT); toastT = setTimeout(function () { t.hidden = true; }, 2200); }
  function copyText(text, msg) {
    msg = msg || 'Copied';
    if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(text).then(function () { toast(msg); }, function () { fallbackCopy(text, msg); }); }
    else fallbackCopy(text, msg);
  }
  function fallbackCopy(text, msg) { var ta = document.createElement('textarea'); ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'; document.body.appendChild(ta); ta.select(); try { document.execCommand('copy'); toast(msg || 'Copied'); } catch (e) { toast('Select and copy it by hand'); } document.body.removeChild(ta); }
  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-copy]'); if (!b) return;
    var t = $(b.getAttribute('data-copy')).textContent;
    if (!t || /not (set|read|printed)/.test(t)) { toast('Nothing to copy yet'); return; }
    copyText(t);
  });
  // the electricity price: the engine's setting (ember-tune), else what this page stored; pence per kWh
  function priceNow() { var p = state && state.settings && state.settings.power_price_pence; if (p > 0) return p; var s = parseFloat(stored('igneum.price') || ''); return s > 0 ? s : 0; }
  function goalNow() { var g = state && state.settings && state.settings.tune_goal; return View.GOALS[g] ? g : (View.GOALS[stored('igneum.goal') || ''] ? stored('igneum.goal') : 'balanced'); }

  // ---------- appearance: system, light or dark (the page's own storage) ----------
  function applyTheme(t) {
    t = t === 'light' || t === 'dark' ? t : 'system';
    if (t === 'system') document.documentElement.removeAttribute('data-theme'); else document.documentElement.setAttribute('data-theme', t);
    document.querySelectorAll('#theme-seg .seg-b').forEach(function (b) { var on = b.dataset.theme === t; b.classList.toggle('on', on); b.setAttribute('aria-checked', on ? 'true' : 'false'); });
    store('igneum.theme', t); // every choice stored, system too (the dark default applies only with no saved choice)
  }
  applyTheme(View.themeChoice(params.get('theme'), stored('igneum.theme'))); // ?theme=light|dark for screenshots; the dark default (the founder's order of 15 October 2026)
  $('theme-seg').addEventListener('click', function (e) { var b = e.target.closest('.seg-b'); if (b) applyTheme(b.dataset.theme); });

  // ---------- screens (setup) and pages (the dashboard) ----------
  function show(p) {
    phase = p;
    document.body.setAttribute('data-phase', p);
    var dash = p === 'dashboard';
    document.body.classList.toggle('has-rail', dash);
    $('rail').hidden = !dash;
    $('top-brand').hidden = dash;
    $('page-title').hidden = !dash;
    if (!dash && drawerOpen()) setDrawer(false);
    if (p === 'cards' && !cardsSeen) { cardsSeen = true; api('api/detect', {}); }
    // the region step; ?region=<code> preselects one for screenshots
    if (p === 'network' && state) renderNetworkStep(state);
    if (p === 'region') { fillRegions($('region-select')); fillCurrencies($('region-currency'), regionNow() || View.regionFromLocale(lang)); var rc = params.get('region') || regionNow() || View.regionFromLocale(lang); $('region-select').value = rc; $('region-currency').value = currencyNowCode(); var ask0 = View.currencyAsk(View.currencyChoice({ region: rc, currency: currencyNowCode() }, lang)); setText('region-currency-ask', ask0); setHidden('region-currency-ask', !ask0); regionWords(rc, $('region-price'), $('region-unit'), $('region-note'), $('region-restricted'), !(priceNow() > 0)); if (priceNow() > 0) $('region-price').value = String(priceNow()); }
    if (dash) { showPage(page); mountDag(); }
    api('api/phase', { phase: p });
  }
  function showPage(id) {
    var meta = View.page(id);
    page = meta.id;
    if (page === 'pools') fetchPools(); // the page opened by the rail, by ?page=pools or at the dashboard's start alike
    if (page === 'fleet') showFleet(); // 0.2.8: the console loads when the section opens, never before
    document.body.setAttribute('data-page', page);
    document.querySelectorAll('.page').forEach(function (el) { el.hidden = el.dataset.page !== page; });
    document.querySelectorAll('#rail-nav .nav').forEach(function (b) {
      var on = b.dataset.page === page;
      b.classList.toggle('on', on);
      if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    });
    setText('page-title-text', meta.title); setText('page-sub', meta.sub);
    $('main').scrollTop = 0;
    if (state) { renderPage(state); if (page === 'mine') size(); }
    if (typeof pollChain === 'function') pollChain(true);
  }
  $('rail-nav').addEventListener('click', function (e) { var b = e.target.closest('.nav[data-page]'); if (b) showPage(b.dataset.page); });
  // the rail answers the arrow keys and Home/End, so the sections are reachable without a mouse
  $('rail-nav').addEventListener('keydown', function (e) {
    var items = Array.prototype.slice.call(document.querySelectorAll('#rail-nav .nav')), i = items.indexOf(document.activeElement);
    if (i < 0) return;
    var j = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : -1;
    if (j < 0 || j >= items.length) return;
    e.preventDefault(); items[j].focus(); showPage(items[j].dataset.page);
  });
  // the first run is one screen (the founder's order of 12 October 2026): the payout address is the only field; the
  // cards, the region and the network take their defaults (every supported card on, the public pool, the system's
  // region) and live under Settings
  $('btn-begin').addEventListener('click', function () { show('address'); });
  $('w-auto-update').addEventListener('change', function () { api('api/update/auto', { on: this.checked }); }); // F14: the choice at install, the same route as the Settings switch
  $('btn-cards-next').addEventListener('click', function () {
    var choices = readCardRows($('cards-list'));
    if (choices.length) api('api/cards', { cards: choices }).then(function () { show('region'); }); else show('region');
  });
  // ---------- the region and price step (Ember Heat): the list, a typed price never fetched, the restricted line ----------
  // the currency picker: "Follow the region" first, then the five, then the long tail, each with its English name
  function fillCurrencies(sel, regionCodeNow) {
    var follow = View.currencyOf(regionCodeNow || '').code, names = null; try { names = new Intl.DisplayNames(['en'], { type: 'currency' }); } catch (e) { }
    var want = 'follow:' + follow; if (sel.dataset.filled === want) return; sel.dataset.filled = want;
    var v = sel.value;
    sel.innerHTML = '<option value="">Follow the region (' + esc(follow) + ')</option>' + View.CURRENCY_CODES.map(function (c) { var n = names ? names.of(c) : c; return '<option value="' + c + '">' + esc(c + (n && n !== c ? ' · ' + n : '')) + '</option>'; }).join('');
    sel.value = v;
  }
  function currencyNowCode() { return (state && state.settings && state.settings.currency) || stored('igneum.currency') || ''; }
  // the region list: the table, plus the locale's region when it is not in the table (synthesised from ISO_CURRENCY)
  function fillRegions(sel) {
    var extra = View.regionFromLocale(lang), rows = View.REGIONS.slice();
    if (extra && !View.REGIONS.some(function (r) { return r.code === extra; })) rows.splice(1, 0, View.regionOf(extra));
    if (sel.dataset.filled === String(rows.length)) return; sel.dataset.filled = String(rows.length);
    sel.innerHTML = '<option value="">Choose a region</option>' + rows.map(function (r) { return '<option value="' + esc(r.code) + '">' + esc(r.name) + '</option>'; }).join('');
  }
  // the price field, unit, note and restricted line follow the chosen region; a typed price is kept unless `prefill`
  function regionWords(code, priceEl, unitEl, noteEl, restrictedEl, prefill) {
    var r = View.regionOf(code);
    if (prefill && priceEl) priceEl.value = r && r.price > 0 ? String(r.price) : '';
    if (priceEl) priceEl.placeholder = r && r.price > 0 ? String(r.price) : '';
    if (unitEl) setText(unitEl.id, View.unitWords(code));
    if (noteEl) setText(noteEl.id, View.priceNote(code));
    var line = View.restrictedLine(code);
    if (restrictedEl) { restrictedEl.hidden = !line; setText(restrictedEl.id, line); }
  }
  function regionNow() { return (state && state.settings && state.settings.region) || stored('igneum.region') || ''; }
  // ---------- the network step (an earlier release): the cards on the first-run step and in Settings ----------
  var netPick = '';
  function renderNetCards(el, w, pick) {
    el.innerHTML = w.cards.map(function (c) { var on = (pick || w.selected) === c.id; return '<button type="button" class="net-card" role="radio" data-net="' + esc(c.id) + '" aria-checked="' + (on ? 'true' : 'false') + '"' + (c.open ? '' : ' disabled') + '>' + (c.current ? '<span class="nc-cur">running now</span>' : '') + '<span class="nc-name">' + esc(c.label) + '</span><span class="nc-line">' + esc(c.line) + '</span><span class="nc-sub">' + esc(c.sub) + '</span></button>'; }).join('');
  }
  function renderNetworkStep(s) {
    var w = View.networkWords(s); renderNetCards($('network-cards'), w, netPick);
    var note = w.error || '';
    setText('network-note', note); setHidden('network-note', !note);
  }
  $('network-cards').addEventListener('click', function (e) { var b = e.target.closest('.net-card'); if (!b || b.disabled) return; netPick = b.getAttribute('data-net'); if (state) renderNetworkStep(state); });
  $('btn-network-back').addEventListener('click', function () { show('region'); });
  $('btn-network-next').addEventListener('click', function () { var w = View.networkWords(state || {}), pick = netPick || w.selected; api('api/network', { network: pick, confirm: true }).then(function () { show('address'); }).catch(function () { show('address'); }); });
  function renderNetworkCard(s) {
    var w = View.networkWords(s); renderNetCards($('s-network-cards'), w, '');
    setText('s-network-line', w.line); setText('s-network-eyebrow', w.pending ? 'next start' : 'running');
    setText('s-network-note', w.error); setHidden('s-network-note', !w.error);
    // the work source (the old base, step 3): the line names it; the field shows the saved choice unless it is being typed
    var so = View.sourceWords(s);
    setText('s-source-line', so.line);
    if (document.activeElement !== $('s-source-url') && document.activeElement !== $('s-source-kind')) {
      if (so.kind) $('s-source-kind').value = so.kind;
      $('s-source-url').value = so.url;
    }
  }

  // ---- the Pools page (DESIGN 2.7, PA-06; the founder's order of 20:4x UK, 13 October 2026) ----
  var poolsDoc = null, poolsAt = 0, poolsBusy = false;
  function fetchPools() {
    if (poolsBusy || (poolsDoc && Date.now() - poolsAt < 30000)) { renderPools(); return; }
    poolsBusy = true;
    fetch('api/pools', { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) { poolsDoc = d; poolsAt = Date.now(); poolsBusy = false; renderPools(); }).catch(function (e) { poolsBusy = false; poolsDoc = { error: String(e), pools: [] }; renderPools(); });
  }
  // the Pools page rows (main's audit of 21:0x UK, 13 October 2026): one compact card per pool at Mine's density, the
  // name on one line with its parenthetical turned into a badge, four stat tiles (fee, payout, uptime, share of
  // network) with the value large and the label small; a missing fact is one muted word, never a sentence; a pool with
  // no disclosure document folds to a single line; the selected pool carries a filled state tag, the others "Switch here"
  var POOL_REGION = { 'europe': 'EU', 'eu': 'EU', 'north-america': 'NA', 'na': 'NA', 'asia-oceania': 'APAC', 'asia': 'APAC', 'apac': 'APAC', 'south-america': 'SA', 'africa': 'AF' };
  function poolNameParts(p) {
    var label = String(p.label || p.address || ''), m = label.match(/^(.*?)\s*\((.*)\)\s*$/), name = m ? m[1] : label, paren = m ? m[2] : '';
    return { name: name || label, badge: /public/i.test(paren) ? 'public pool' : '', note: paren };
  }
  function poolFeeTile(p) { return p.fee_bps == null ? { v: 'not filed', dim: true } : { v: (p.fee_bps / 100).toFixed(p.fee_bps % 100 ? 2 : 0) + '%', dim: false }; }
  function poolPayoutTile(p) {
    if (!p.scheme && !p.payout_schedule) return { v: 'not filed', k: 'payout', dim: true };
    var bits = [];
    if (p.window_n) bits.push(p.window_n + ' window' + (p.window_n === 1 ? '' : 's'));
    if (p.maturity_blocks != null) bits.push(p.maturity_blocks + ' block' + (p.maturity_blocks === 1 ? '' : 's') + ' to mature');
    if (p.min_payout_wei != null) bits.push(p.min_payout_wei === '0' ? 'no minimum' : 'minimum ' + View.fmtIgn(Number(p.min_payout_wei) / 1e18));
    if (p.payout_gas) bits.push(p.payout_gas === 'operator' ? 'pool pays the fee' : 'fee deducted');
    return { v: p.scheme ? String(p.scheme).toUpperCase() : 'by the chain', k: 'payout', s: bits.join(' · '), dim: false };
  }
  var POOL_REGION_ORDER = ['EU', 'NA', 'APAC', 'SA', 'AF'];
  function poolRegionFigures(pr) {
    if (!(pr && pr.published && pr.by_region && typeof pr.by_region === 'object')) return [];
    var rows = Object.keys(pr.by_region).map(function (k) { var w = POOL_REGION[k] || k.replace(/-/g, ' '); return { w: w, t: w + '\u00a0' + Number(pr.by_region[k]).toFixed(1) + '%', o: POOL_REGION_ORDER.indexOf(w) < 0 ? 99 : POOL_REGION_ORDER.indexOf(w) }; });
    rows.sort(function (a, b) { return a.o - b.o || (a.w < b.w ? -1 : 1); });
    return rows.map(function (r) { return r.t; });
  }
  function poolUptimeTile(p) {
    var r = p.reliability || {}, pr = r.probe || {}, regs = poolRegionFigures(pr), st = r.statement_30d_pct;
    var out = [];
    if (regs.length) out.push({ lines: regs, k: '7-day probe' + (pr.state && pr.state !== 'operational' ? ' · ' + pr.state : ''), dim: false, mid: true, cls: 't-probe' });
    else out.push({ v: 'not published', k: '7-day probe', dim: true, cls: 't-probe' });
    if (st != null) out.push({ v: Number(st).toFixed(2) + '%', k: 'stated by the pool, 30 days', dim: false, cls: 't-stated' });
    return out;
  }
  function tileHtml(t, extraClass) {
    var v = t.lines ? '<span class="v lines">' + t.lines.map(esc).join('<br>') + '</span>' : '<span class="v">' + esc(t.v) + '</span>';
    return '<div class="pool-tile' + (t.dim ? ' dim' : '') + (t.warn ? ' warn' : '') + (t.mid ? ' mid' : '') + (t.cls ? ' ' + t.cls : '') + (extraClass ? ' ' + extraClass : '') + '">' + v + '<span class="k">' + esc(t.k) + '</span>' + (t.s ? '<span class="s">' + esc(t.s) + '</span>' : '') + '</div>';
  }
  function poolActHtml(p, isCur) {
    return '<div class="pool-act">' + (isCur ? '<span class="pool-state"><span class="dot"></span>Mining here</span>' : '<button class="btn small primary" data-use="' + esc(p.address) + '">Switch here</button>') + '</div>';
  }
  function poolRowHtml(p, current) {
    var isCur = current && p.address && current.indexOf(p.address) === 0, np = poolNameParts(p);
    var addr = esc(p.address_line || p.address) + (p.tls ? ' · TLS' : (p.ports && p.ports.length > 1 ? ' · plain ports' : ' · plain port')) + (p.chain_id ? ' · chain ' + p.chain_id : '');
    var open = '<div class="pool-row' + (isCur ? ' on' : '') + (p.document_error ? ' folded' : '') + '" data-address="' + esc(p.address) + '" data-tls="' + (p.tls ? '1' : '0') + '">';
    if (p.document_error) {
      // item 4: no disclosure document, one line: name, endpoint and chain, the project words, the button
      return open + '<div class="pool-main"><div class="pool-name">' + esc(np.name) + '</div><div class="pool-addr mono">' + addr + '</div></div>' +
        '<div class="pool-fold">' + esc(p.project ? 'project test pool, no terms filed' : 'no terms filed') + '</div>' + poolActHtml(p, isCur) + '</div>';
    }
    var tags = [];
    if (p.project) tags.push('<span class="pool-tag">operated by the project</span>');
    if (np.badge) tags.push('<span class="pool-tag">' + esc(np.badge) + '</span>');
    var fee = poolFeeTile(p); fee.k = 'fee'; fee.cls = 't-fee';
    var up = poolUptimeTile(p), upHtml = up.map(function (t) { return tileHtml(t); }).join('');
    return open +
      '<div class="pool-main"><div class="pool-name" title="' + esc(np.name) + '">' + esc(np.name) + '</div><div class="pool-addr mono">' + addr + '</div><div class="pool-tags">' + tags.join('') + '</div></div>' +
      '<div class="pool-stats' + (up.length > 1 ? ' five' : '') + '">' + tileHtml(fee) + tileHtml(poolPayoutTile(p), 't-payout') + upHtml + tileHtml(poolShareTile(p), 't-share') + '</div>' +
      poolActHtml(p, isCur) +
    '</div>';
  }
  function renderPools() {
    var host = $('pool-list'); if (!host) return;
    var d = poolsDoc;
    if (!d) { return; }
    var cur = (state && state.settings && state.settings.source_kind === 'pool') ? String(state.settings.source_url || '') : '';
    setText('pools-eyebrow', d.error ? 'not read' : (d.pools || []).length + ' listed');
    if (d.error) { host.innerHTML = '<div class="pool-empty err">The pool list could not be read: ' + esc(d.error) + '</div>'; return; }
    if (!(d.pools || []).length) { host.innerHTML = '<div class="pool-empty">No pool has filed a listing yet. Add one by host and port below, or mine solo.</div>'; setText('pools-foot', ''); return; }
    var offered = View.poolsOffered(d.pools, View.PIN_CHAIN_ID), merged = View.poolsMerged(offered.shown, cur);
    setText('pools-eyebrow', merged.length + ' listed');
    var html = merged.length ? merged.map(function (p) { return poolRowHtml(p, cur); }).join('') : '<div class="pool-empty">No pool on ' + esc(View.chainWord(View.PIN_CHAIN_ID, '')) + ' has filed a listing yet. Add one by host and port below, or mine solo.</div>';
    if (host.innerHTML !== html) host.innerHTML = html;
    var noShare = d.pools.some(function (p) { return !p.document_error && poolSharePct(p.concentration) == null; });
    // item 2: the explorer's missing share figure explained once under the list, not per row
    setText('pools-foot', (offered.words ? offered.words + ' ' : '') + 'The list: ' + (d.list_source || '') + '. Each pool’s fee and terms are its own document; the probe is the project’s status feed; the share of network is the explorer’s' + (noShare ? ', which does not publish it yet (n/a)' : '') + '.');
  }
  $('pool-list').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-use]'); if (!b || b.disabled) return;
    var row = b.closest('.pool-row'), url = b.getAttribute('data-use') + (row && row.getAttribute('data-tls') === '1' ? '' : ' notls');
    api('api/source', { kind: 'pool', url: url }).then(function (r) { if (r.ok) { toast('Work source set: pool ' + url); poolsAt = 0; fetchPools(); } }).catch(function (err) { toast(String(err)); });
  });
  $('pool-add-use').addEventListener('click', function () {
    var url = ($('pool-add-url').value || '').trim();
    if (!url) { setText('pool-add-note', 'Name the pool as host:port first.'); setHidden('pool-add-note', false); return; }
    setHidden('pool-add-note', true);
    api('api/source', { kind: 'pool', url: url }).then(function (r) { if (r.ok) { toast('Work source set: pool ' + url); $('pool-add-url').value = ''; poolsAt = 0; fetchPools(); } }).catch(function (err) { setText('pool-add-note', 'Ember refused it: ' + String(err).replace(/^Error:\s*/, '')); setHidden('pool-add-note', false); });
  });
  $('solo-use').addEventListener('click', function () {
    var url = ($('solo-url').value || '').trim();
    if (!url) { setText('solo-note', 'Name your node’s RPC URL first.'); setHidden('solo-note', false); return; }
    setHidden('solo-note', true);
    api('api/source', { kind: 'node', url: url }).then(function (r) { if (r.ok) toast('Work source set: node ' + url); }).catch(function (err) { setText('solo-note', 'Ember refused it: ' + String(err).replace(/^Error:\s*/, '')); setHidden('solo-note', false); });
  });
  $('s-source-apply').addEventListener('click', function () {
    var kind = $('s-source-kind').value, url = ($('s-source-url').value || '').trim();
    if (!url) { setText('s-source-note', 'Name the pool (host:port) or the node (URL) first.'); setHidden('s-source-note', false); return; }
    setHidden('s-source-note', true);
    api('api/source', { kind: kind, url: url }).then(function (r) { if (r.ok) toast('Work source set: ' + kind + ' ' + url); }).catch(function (e) { setText('s-source-note', String(e)); setHidden('s-source-note', false); });
  });
  $('s-network-cards').addEventListener('click', function (e) { var b = e.target.closest('.net-card'); if (!b || b.disabled || !state) return; var want = b.getAttribute('data-net'), w = View.networkWords(state); if (want === w.selected) return; $('ask-network-text').textContent = View.networkAsk(want, state.network_name || 'chain 7317'); $('ask-network').dataset.want = want; $('ask-network').hidden = false; });
  $('ask-network-no').addEventListener('click', function () { $('ask-network').hidden = true; });
  $('ask-network-yes').addEventListener('click', function () { var want = $('ask-network').dataset.want; $('ask-network').hidden = true; api('api/network', { network: want, confirm: true }).then(function (r) { if (r.ok) toast('Network saved: it takes effect at the next start'); }); });
  function saveRegion(code, price, cur) {
    store('igneum.region', code || null); store('igneum.price', price > 0 ? price : null); if (cur !== undefined) store('igneum.currency', cur || null);
    var body = { region: code, price_pence: price }; if (cur !== undefined) body.currency = cur || '';
    return api('api/region', body).catch(function () { return api('api/tune/goal', { price_pence: price }); });
  }
  // the override: picked, the price field's unit follows at once; "" follows the region again
  function saveCurrency(sel, priceEl, unitEl, noteEl, regionSel) {
    var cur = sel.value; View.setCurrency(cur); store('igneum.currency', cur || null);
    api('api/region', { currency: cur }).catch(function () { });
    regionWords(regionSel.value, null, unitEl, noteEl, null, false);
    if (state) renderPage(state);
  }
  $('region-select').addEventListener('change', function () { regionWords(this.value, $('region-price'), $('region-unit'), $('region-note'), $('region-restricted'), true); });
  $('region-currency').addEventListener('change', function () { saveCurrency(this, $('region-price'), $('region-unit'), $('region-note'), $('region-select')); var a = View.currencyAsk(View.currencyChoice({ region: $('region-select').value, currency: this.value }, lang)); setText('region-currency-ask', a); setHidden('region-currency-ask', !a); });
  $('btn-region-back').addEventListener('click', function () { show('cards'); });
  $('btn-region-skip').addEventListener('click', function () { show('network'); });
  $('btn-region-next').addEventListener('click', function () {
    var code = $('region-select').value, p = parseFloat($('region-price').value) * View.priceFactor(View.currencyNow().code);
    if (!(p >= 0)) p = 0;
    saveRegion(code, p, $('region-currency').value).then(function () { show('network'); }).catch(function () { show('network'); });
  });
  $('btn-cards-retry').addEventListener('click', function () { cardsSeen = false; $('cards-detecting').hidden = false; api('api/detect', {}); });
  $('btn-address-back').addEventListener('click', function () { show('welcome'); });

  // address options
  var opts = document.querySelectorAll('.option');
  function pickOption(el) { opts.forEach(function (o) { o.classList.toggle('on', o === el); }); el.querySelector('input').checked = true; if (el.id === 'opt-paste') $('addr-input').focus(); setText('btn-start', el.id === 'opt-paste' ? 'Start mining' : 'Continue'); }
  opts.forEach(function (o) { o.addEventListener('click', function (e) { if (e.target.tagName !== 'INPUT' || e.target.type === 'radio') pickOption(o); }); });
  $('addr-input').addEventListener('focus', function () { pickOption($('opt-paste')); });
  function validAddr(a) { return /^0x[0-9a-fA-F]{40}$/.test((a || '').trim()); }
  $('btn-start').addEventListener('click', function () {
    var mode = document.querySelector('input[name=mode]:checked').value;
    var body = { mode: mode };
    if (mode === 'paste') {
      var a = $('addr-input').value.trim();
      if (!validAddr(a)) { $('addr-err').hidden = false; return; }
      $('addr-err').hidden = true; body.address = a;
    }
    $('btn-start').disabled = true;
    api('api/setup', body).then(function (r) {
      $('btn-start').disabled = false;
      if (!r.ok) { $('addr-err').textContent = r.error || 'could not save'; $('addr-err').hidden = false; return; }
      if (mode === 'generate' && r.private_key) { showKeySheet(r); }
      else { api('api/start', {}).then(function () { show('dashboard'); }); }
    }).catch(function () { $('btn-start').disabled = false; toast('The app did not answer'); });
  });
  function showKeySheet(r) {
    $('key-address').textContent = r.display || r.address;
    $('key-private').textContent = r.private_key;
    $('key-file').textContent = r.wallet_file || '';
    $('key-ack').checked = false; $('btn-key-done').disabled = true;
    $('sheet-key').hidden = false; keyShownThisRun = true;
  }
  $('key-ack').addEventListener('change', function () { $('btn-key-done').disabled = !this.checked; });
  $('btn-key-done').addEventListener('click', function () {
    api('api/key/saved', {}).then(function () { return api('api/start', {}); }).then(function () { $('sheet-key').hidden = true; $('key-private').textContent = ''; show('dashboard'); });
  });

  // ---------- disclosures: one chevron per details block ----------
  function setOpen(btn, det, open) { btn.setAttribute('aria-expanded', open ? 'true' : 'false'); btn.classList.toggle('open', open); det.hidden = !open; }
  function disclosure(btnId, detId, onOpen) {
    $(btnId).addEventListener('click', function () { var open = $(detId).hidden; setOpen($(btnId), $(detId), open); if (open && onOpen) onOpen(); });
  }
  // the node line on Mine opens Settings > Node with its facts open
  $('node-go').addEventListener('click', function () { showPage('settings'); var d = $('node-details'); if (d) { d.open = true; d.scrollIntoView({ block: 'start' }); } });
  disclosure('pv-toggle', 'pv-details');
  disclosure('addr-toggle', 'addr-details', function () { $('s-address-input').focus(); });
  disclosure('key-toggle', 'key-details');
  $('s-jobs-history-btn').addEventListener('click', function () { setOpen(this, $('s-jobs-history'), $('s-jobs-history').hidden); $('s-jobs-key').hidden = $('s-jobs-history').hidden; });

  // ---------- Mine: the big button, the rows, the tune controls ----------
  // F-E63: the click's word (Starting or Stopping with a spinner) shows at once and stands until the engine's state
  // reflects it; the engine acknowledges on its tick, seconds on a loaded host (the mini's log: 11 s)
  var toggleClick = null;
  $('btn-toggle').addEventListener('click', function () {
    if (!state) return;
    var t = View.toggle(state.mining, state.node, state, toggleClick && { act: toggleClick.act, at: toggleClick.at, now: Date.now() });
    if (!t.act) return;
    toggleClick = View.pressToggle(t, Date.now());
    renderMine(state);
    api('api/' + t.act, t.act === 'pause' ? { by: 'user' } : {}).then(function () { toast(t.act === 'pause' ? 'Mining stops. The pool session stays open' : 'Mining starts'); }).catch(function () { toggleClick = null; if (state) renderMine(state); });
  });
  // a row's switch, slider or stepper sends the whole list to the engine (pending[key] holds a flipped switch until
  // the engine agrees, so the row does not flicker back)
  var pending = {}, cardsT = null, openCards = {};
  function sendCards(msg) {
    clearTimeout(cardsT);
    cardsT = setTimeout(function () {
      var choices = readCardRows();
      if (choices.length) api('api/cards', { cards: choices }).then(function (r) { if (r.ok) toast(msg || 'Applied. Only that card restarts'); else toast(r.error || 'could not change'); });
    }, 400);
  }
  $('d-cards').addEventListener('change', function (e) {
    if (!state) return;
    if (e.target.matches('.gpu-line > .gl-main .switch input')) {
      var key = e.target.closest('.gpu-line').dataset.key, on = e.target.checked;
      pending[key] = { on: on, at: Date.now() };
      var choices = shownCards(state.mining.cards).filter(View.present).map(function (c) { var it = { key: c.key, enabled: c.key === key ? on : c.enabled, identities: c.identities || 1 }; if (c.vendor === 'nvidia' && c.power_default_w > 0) it.power_pct = c.power_pct || 80; return it; });
      api('api/cards', { cards: choices }).then(function (r) { if (r.ok) toast(on ? 'Starting that card' : 'That card is off'); else { delete pending[key]; toast(r.error || 'could not change'); } });
    }
  });
  $('t-cards').addEventListener('change', function (e) { if (e.target.matches('.ids input')) sendCards('Set. The card restarts'); });
  $('t-cards').addEventListener('input', function (e) {
    if (e.target.matches('input[type=range]')) {
      var row = e.target.closest('.gpu-line'), cd = cardOf(row.dataset.key), pct = parseInt(e.target.value, 10);
      row.querySelector('.pv').textContent = pct + '%'; var help = row.querySelector('.ctl .help'); if (help && cd) help.firstChild.textContent = View.capSentence({ ...cd, power_pct: pct }) + ' Lower is cooler and quieter.';
      sendCards('Power cap set. It stays until you choose Default again');
    }
  });
  ['cards-list', 's-cards'].forEach(function (id) {
    var el = $(id); if (!el) return;
    el.addEventListener('click', function (e) {
      var b = e.target.closest('button'); if (!b) return;
      if (b.dataset.driverInstall !== undefined) api('api/drivers/install', { vendor: b.dataset.driverInstall }).then(function (r) { toast(r && r.ok === false ? (r.error || 'could not start the driver install') : 'Driver download started. Windows asks for permission once before the install.'); });
      else if (b.dataset.driverRestart !== undefined) { if (window.confirm('Restart Windows now to finish the driver install?')) api('api/drivers/restart', {}).then(function () { toast('Restarting in 20 seconds'); }); }
    });
  });
  $('t-cards').addEventListener('click', function (e) {
    var b = e.target.closest('button'); if (!b) return;
    var row = b.closest('.gpu-line'), key = row ? row.dataset.key : '';
    if (b.dataset.tune !== undefined) api('api/sweep/start', { key: key }).then(function (r) { toast(r && r.ok === false ? (r.error || 'could not start the tune') : 'Tune queued for ' + (cardOf(key) || {}).name); });
    else if (b.dataset.stop !== undefined) api('api/sweep/stop', {}).then(function () { toast('Tune stopped. The card is back where it was'); });
    else if (b.dataset.d) { var inp = b.parentNode.querySelector('input'), v = parseInt(inp.value, 10) || 1; inp.value = Math.max(1, Math.min(64, v + parseInt(b.dataset.d, 10))); sendCards('Identities set; that card’s worker restarts'); }
    else if (b.dataset.pin !== undefined) api('api/sweep/pin', { key: key, pinned: b.dataset.pin === '1' }).then(function () { toast(b.dataset.pin === '1' ? 'Cap pinned' : 'Default: Ember chooses the cap again'); });
    else if (b.dataset.powerRetry !== undefined) api('api/power/apply', {}).then(function () { toast('Allow the administrator prompt to set the cap'); });
    else if (b.dataset.driverInstall !== undefined) api('api/drivers/install', { vendor: b.dataset.driverInstall }).then(function (r) { toast(r && r.ok === false ? (r.error || 'could not start the driver install') : 'Driver download started. Windows asks for permission once before the install; the workers on this vendor\u2019s cards stop for it and come back after.'); });
    else if (b.dataset.driverRestart !== undefined) { if (window.confirm('Restart Windows now to finish the driver install? Mining stops until the app is back.')) api('api/drivers/restart', {}).then(function () { toast('Restarting in 20 seconds'); }); }
    else if (b.dataset.price !== undefined) { showPage('settings'); $('s-price').focus(); }
    else if (b.closest('[data-card-tier]') && b.dataset.tier) setTier(b.dataset.tier, key);
  });
  // a card's Details stays open across re-renders
  $('t-cards').addEventListener('toggle', function (e) { var row = e.target.closest('.gpu-line'); if (row) openCards[row.dataset.key] = e.target.open; }, true);
  $('power-note-go').addEventListener('click', function () { showPage('settings'); });
  function cardOf(key) { return state ? state.mining.cards.filter(function (c) { return c.key === key; })[0] : null; }
  $('btn-tune-all').addEventListener('click', function () {
    if (!state) return;
    var list = shownCards(state.mining.cards).filter(function (c) { return View.tunable(c) && c.enabled; });
    if (!list.length) { toast('No card to tune'); return; }
    var n = 0;
    list.reduce(function (p, c) { return p.then(function () { return api('api/sweep/start', { key: c.key }).then(function (r) { if (!r || r.ok !== false) n++; }); }); }, Promise.resolve()).then(function () { toast(n === 1 ? 'Tune queued for 1 card' : 'Tune queued for ' + n + ' cards, one after another'); });
  });
  // the goal (ember-tune: POST api/tune/goal); a build without the route says so and keeps the old value
  // tiers-25: a tap applies the known row at once (POST api/tune/tier); an engine before an earlier release has no route, so the goal route
  // takes the tier as a goal and the toast says the numbers wait for the update
  function setTier(tier, key) {
    var body = key ? { key: key, tier: tier } : { tier: tier };
    api('api/tune/tier', body).then(function (r) {
      if (r && r.ok) { if (!key) store('igneum.goal', tier === 'max' ? 'rate' : tier); toast(View.tierName(tier) + (key ? ' applied to ' + ((cardOf(key) || {}).name || 'the card') : ' applied to ' + ((r.applied || []).length || 'every') + ' card' + ((r.applied || []).length === 1 ? '' : 's'))); }
      else if (r && r.error && !/not found|unknown|404/i.test(r.error)) toast(r.error);
      else api('api/tune/goal', key ? { key: key, goal: tier === 'max' ? 'rate' : tier } : { goal: tier === 'max' ? 'rate' : tier }).then(function (g) { toast(g && g.ok ? View.tierName(tier) + ' set as the goal; the one-tap tiers need the 0.1.25 engine' : 'The tiers need the 0.1.25 engine'); });
      if (state) renderTune(state);
    }).catch(function () { toast('The engine did not answer'); });
  }
  $('tier-seg').addEventListener('click', function (e) { var b = e.target.closest('.tier-b'); if (b && b.dataset.tier) setTier(b.dataset.tier); });
  function powerControl(on) { api('api/power/control', { on: on }).then(function (r) { if (r.ok) toast(on ? 'Power control on. Windows asks for administrator rights once' : 'Power control off. Nothing asks'); }); }
  $('s-power-control').addEventListener('change', function () { powerControl(this.checked); });
  $('m-clock-sync').addEventListener('click', function () { api('api/clock/sync', {}); });

  // ---------- Earnings ----------
  // 0.2.8, the Fleet item: save the console URL and the window token (the token field empties after a save; an empty
  // token with a URL leaves the held one); Clear removes both
  if ($('s-fleet-url')) { var fu = $('s-fleet-url'); fu.addEventListener('focus', function () { if (fu.dataset.real && fu.value === View.fleetUrlShown(fu.dataset.real)) fu.value = fu.dataset.real; }); fu.addEventListener('blur', function () { if (fu.dataset.real && fu.value.trim() === fu.dataset.real) fu.value = View.fleetUrlShown(fu.dataset.real); }); }
  if ($('s-fleet-save')) $('s-fleet-save').addEventListener('click', function () {
    var uf = $('s-fleet-url'), url = uf.value.trim(), tok = $('s-fleet-token').value; if (url === View.fleetUrlShown(uf.dataset.real || '')) url = uf.dataset.real || ''; // the masked form saves the held address
    var body = { fleet_console_url: url }; if (tok) body.fleet_console_token = tok;
    api('api/fleet', body).then(function (r) {
      if (!r.ok) { $('s-fleet-err').hidden = false; setText('s-fleet-err', r.error || 'not saved'); return; }
      $('s-fleet-err').hidden = true; $('s-fleet-token').value = ''; fleetLoadedFor = '';
      if (state) { state.settings.fleet_console_url = r.fleet_console_url; state.settings.fleet_token_set = r.fleet_token_set; renderFleetSettings(state); }
    }).catch(function (e) { $('s-fleet-err').hidden = false; setText('s-fleet-err', String(e)); });
  });
  if ($('s-fleet-clear')) $('s-fleet-clear').addEventListener('click', function () {
    api('api/fleet', { fleet_console_url: '', fleet_console_token: '' }).then(function (r) {
      $('s-fleet-url').value = ''; $('s-fleet-token').value = ''; fleetLoadedFor = ''; var fr = $('fleet-frame'); if (fr) fr.src = 'about:blank';
      if (state) { state.settings.fleet_console_url = ''; state.settings.fleet_token_set = false; renderFleetSettings(state); }
    });
  });
  $('s-address-save').addEventListener('click', function () {
    var a = $('s-address-input').value.trim();
    if (!validAddr(a)) { $('s-address-err').textContent = 'An address is 0x followed by 40 hex characters.'; $('s-address-err').hidden = false; return; }
    $('s-address-err').hidden = true;
    $('ask-address-text').textContent = View.addressAsk(a); $('ask-address').hidden = false;
  });
  $('ask-address-no').addEventListener('click', function () { $('ask-address').hidden = true; });
  $('ask-address-yes').addEventListener('click', function () {
    var a = $('s-address-input').value.trim();
    api('api/settings', { address: a }).then(function (r) { $('ask-address').hidden = true; if (r.ok) { $('s-address-input').value = ''; setOpen($('addr-toggle'), $('addr-details'), false); toast('Rewards address changed'); } else { $('s-address-err').textContent = r.error; $('s-address-err').hidden = false; } });
  });
  $('s-reveal').addEventListener('click', function () {
    api('api/key/reveal', {}).then(function (r) { if (r.ok) { $('s-key').textContent = r.private_key; $('s-key-box').hidden = false; $('ask-key').hidden = true; } else toast(r.error || 'no key'); });
  });
  function hideKey() { $('s-key-box').hidden = true; $('s-key').textContent = ''; $('ask-key').hidden = false; }
  $('s-hide').addEventListener('click', hideKey);
  $('ask-key-no').addEventListener('click', function () { setOpen($('key-toggle'), $('key-details'), false); });
  $('s-wallet').addEventListener('click', function () { api('api/open', { url: 'https://igneum.network/wallet' }); });
  $('r-wallet').addEventListener('click', function () { api('api/open', { url: 'https://igneum.network/wallet' }); });

  // ---------- Prove ----------
  $('s-prove').addEventListener('change', function () { var on = this.checked; api('api/prove', { on: on }).then(function (r) { if (r.ok) toast(on ? 'Proving on. The first shard arrives within a minute' : 'Proving off'); else { toast(r.error || 'could not change'); $('s-prove').checked = !on; } }); });
  $('s-prove-instead').addEventListener('change', function () { var on = this.checked; api('api/prove/instead', { on: on }).then(function (r) { if (r.ok === false) { $('s-prove-instead').checked = !on; } }); });
  $('pv-setup').addEventListener('click', function () { api('api/prove/setup', {}).then(function (r) { toast(r.ok ? 'Setup started in its own window' : (r.error || 'could not start')); }); });

  // ---------- Settings ----------
  $('s-update').addEventListener('click', function () { api('api/update/check', {}); setText('s-update-note', 'Checking.'); });
  // the founder's order of 11:0x UK, 12 October 2026: Reset to recommended, with a plain confirm first
  $('s-reset-recommended').addEventListener('click', function () {
    if (!window.confirm('Put every setting back to the recommended default? Your address, names and counters stay.')) return;
    api('api/settings/recommended', {}).then(function () { toast('Settings reset to the recommended defaults.'); }).catch(function () { toast('The reset did not go through. Try again.'); });
  });
  $('s-install').addEventListener('click', function () { api('api/update/install', {}); toast('Installing at once'); });
  $('s-auto-update').addEventListener('change', function () { api('api/update/auto', { on: this.checked }); });
  $('s-jobs-check').addEventListener('click', function () { api('api/jobs/check', {}); setText('s-jobs-note', 'Checking.'); });
  $('s-name-save').addEventListener('click', function () { api('api/settings', { display_name: $('s-name').value }).then(function (r) { if (r.ok) toast('Renamed'); }); });
  $('s-vote').addEventListener('change', function () { api('api/settings', { vote: this.checked }).then(function (r) { if (r.ok) toast(r.restart ? 'Applied; the miner restarts' : 'Applied'); }); });
  $('s-login').addEventListener('change', function () { var on = this.checked; api('api/settings', { start_at_login: on }).then(function (r) { if (!r.ok) { toast(r.error || 'could not change'); $('s-login').checked = !on; } }); });
  $('s-jobs-allow').addEventListener('change', function () { api('api/jobs/allow', { on: this.checked }); });
  $('s-sweep').addEventListener('change', function () { api('api/sweep/enable', { on: this.checked }).then(function (r) { if (r.ok) toast($('s-sweep').checked ? 'Ember Tune on. Once after install, then every 7 days' : 'Ember Tune off'); }); });
  $('s-climb').addEventListener('change', function () { var on = this.checked; api('api/tune/goal', { climb: on }).then(function (r) { if (!r || !r.ok) { toast('Needs the Ember Tune update'); $('s-climb').checked = !on; } }); });
  function savePrice() {
    var typed = parseFloat($('s-price').value), code = $('s-region').value, c = View.currencyNow(), f = View.priceFactor(c.code), p = typed * f;
    if (!(p >= 0)) { toast('Type a price in ' + View.minorOf(c.code).word + ' per kWh, for example ' + (View.regionOf(code) && View.regionOf(code).price || 26.32)); return; }
    saveRegion(code, p, $('s-currency').value).then(function () { toast(p > 0 ? 'Price saved: ' + p + ' ' + c.minor + ' per kWh' : 'Price cleared'); if (state) renderPage(state); }).catch(function () { toast(p > 0 ? 'Price saved on this window: ' + p + ' ' + c.minor + ' per kWh' : 'Price cleared'); if (state) renderPage(state); });
  }
  $('s-price-save').addEventListener('click', savePrice);
  $('s-price').addEventListener('keydown', function (e) { if (e.key === 'Enter') savePrice(); });
  $('s-region').addEventListener('change', function () { regionWords(this.value, $('s-price'), $('s-price-unit'), $('s-price-note'), $('s-region-restricted'), true); });
  $('s-currency').addEventListener('change', function () { saveCurrency(this, $('s-price'), $('s-price-unit'), $('s-price-note'), $('s-region')); });
  // ---------- Heat mode (Ember Heat) ----------
  $('s-heat').addEventListener('change', function () { var on = this.checked; api('api/heat', { on: on }).then(function (r) { if (r.ok) toast(on ? 'Heat mode on. The cards heat for a share of every 10 minutes' : 'Heat mode off'); else { toast(r.error || 'could not change'); $('s-heat').checked = !on; } }); });
  function saveHeat() {
    var set = parseFloat($('s-heat-set').value), body = { schedule: $('s-heat-schedule').value.trim(), tz_min: -new Date().getTimezoneOffset() };
    if ($('s-heat-set').value !== '') { if (!(set >= 5 && set <= 30)) { toast('The set point is 5 to 30 degrees'); return; } body.set_c = set; }
    api('api/heat', body).then(function (r) { toast(r.ok ? 'Heat settings saved' : (r.error || 'could not save')); if (state) renderPage(state); });
  }
  $('s-heat-save').addEventListener('click', saveHeat);
  $('s-heat-schedule').addEventListener('keydown', function (e) { if (e.key === 'Enter') saveHeat(); });
  $('s-heat-set').addEventListener('keydown', function (e) { if (e.key === 'Enter') saveHeat(); });
  function saveRoom() {
    var c = parseFloat($('s-heat-room').value);
    if (!(c >= -20 && c <= 50)) { toast('A room reading is -20 to 50 degrees'); return; }
    api('api/heat', { room_c: c }).then(function (r) { toast(r.ok ? 'Room reading used: ' + c + ' °C for the next two hours' : (r.error || 'could not save')); $('s-heat-room').value = ''; if (state) renderPage(state); });
  }
  $('s-heat-room-save').addEventListener('click', saveRoom);
  $('s-heat-room').addEventListener('keydown', function (e) { if (e.key === 'Enter') saveRoom(); });
  $('s-trust').addEventListener('change', function () { var on = this.checked; $('s-trust').checked = !on; $('ask-trust-text').textContent = View.trustAsk(on); $('ask-trust').hidden = false; $('ask-trust').dataset.on = on ? '1' : ''; });
  $('ask-trust-no').addEventListener('click', function () { $('ask-trust').hidden = true; });
  $('ask-trust-yes').addEventListener('click', function () {
    var on = $('ask-trust').dataset.on === '1'; $('ask-trust').hidden = true;
    api('api/settings', { proof_verify_trust: on }).then(function (r) { if (r.ok) { $('s-trust').checked = on; toast(on ? 'Trust mode on (test networks only). The node restarts' : 'Trust mode off. The node restarts'); } else toast(r.error || 'could not change'); });
  });
  $('s-live').addEventListener('click', function () { if (state && state.live_page) api('api/open', { url: state.live_page }); });
  $('s-log-open').addEventListener('click', function () { setDrawer(true); });
  // the log export: the last lines the engine holds (the ring keeps 3,000; one call returns up to 2,000), to the clipboard
  $('s-log-copy').addEventListener('click', function () {
    api('api/log?after=0&limit=2000').then(function (r) {
      var lines = (r.lines || []).map(function (l) { return clock(l.t) + ' ' + l.src.padEnd(6) + ' ' + l.text; });
      if (!lines.length) { toast('Nothing logged yet'); return; }
      copyText('Ember ' + (state ? state.version : '') + ' log, ' + lines.length + ' lines\n' + lines.join('\n'), 'Copied ' + lines.length + ' lines');
    }).catch(function () { toast('The engine did not answer'); });
  });

  // ---------- rail: logs and quit (the quit question is a strip, never a dialog) ----------
  $('btn-logs').addEventListener('click', function () { setDrawer(!document.body.classList.contains('drawer-open')); });
  $('log-close').addEventListener('click', function () { setDrawer(false); });
  $('btn-quit').addEventListener('click', function () { $('ask-quit').hidden = false; $('ask-quit-yes').focus(); });
  // main's audit of 21:2x UK, 13 October 2026, item 3: at phone width the bar carries the six pages; Logs and Quit sit at the foot of Settings
  $('s-logs').addEventListener('click', function () { setDrawer(!document.body.classList.contains('drawer-open')); });
  $('s-quit').addEventListener('click', function () { $('ask-quit').hidden = false; $('ask-quit-yes').focus(); });
  $('ask-quit-no').addEventListener('click', function () { $('ask-quit').hidden = true; });
  $('ask-quit-yes').addEventListener('click', function () { $('ask-quit').hidden = true; api('api/quit', {}); $('pill-text').textContent = 'stopping'; $('btn-quit').disabled = true; $('s-quit').disabled = true; });

  // ---------- log drawer ----------
  // logLines: everything fetched this session (newest last, capped at LOG_CAP). view: the lines after the source chip
  // and the search box. The list is virtualised: rows have a fixed height (RH) and only the rows in the viewport are in
  // the DOM, so 20,000 lines cost the same as 20. Wrap mode renders the whole view with wrapped lines when it is small
  // enough. Polling runs only while the drawer is open and the window is visible.
  var LOG_CAP = 20000, WRAP_MAX = 5000, RH = 19, OVERSCAN = 8;
  var logAfter = 0, logFilter = '', logQuery = '', logLines = [], view = [], viewDirty = true, wrap = false, follow = true;
  var logView = $('log-view'), logPre = $('log'), logPad = $('log-pad');
  var winStart = -1, winEnd = -1, autoScroll = false, scrollRaf = 0, rulerAt = 0, rulerCount = -1, searchT = null, logTimer = null;
  var ERR_RE = /\[error\]|\berror\b|\bfailed\b|\bpanic\b|exited with code/i, SWAP_RE = /hourly program changed|program changed|seed change|hot swap|new program|exit-on-seed-change/i;
  function isErr(l) { return l.err || ERR_RE.test(l.text); }
  function isSwap(l) { return SWAP_RE.test(l.text); }
  function drawerOpen() { return document.body.classList.contains('drawer-open'); }
  function setDrawer(open) {
    document.body.classList.toggle('drawer-open', open);
    var b = $('btn-logs'); b.classList.toggle('on', open); b.classList.toggle('logs', open); b.setAttribute('aria-expanded', open ? 'true' : 'false');
    $('btn-logs-text').textContent = open ? 'Close logs' : 'Logs'; setText('s-logs', open ? 'Close the logs' : 'Open the logs');
    if (open) { applyDrawerHeight(); pollLog(); scheduleLogPoll(); viewDirty = true; renderLog(true); }
    else if (logTimer) { clearTimeout(logTimer); logTimer = null; }
  }
  // height: dragged by the grip, clamped to [140, 70% of the window]; a short window gets a smaller default
  var drawerH = 0;
  function defaultDrawerH() { return window.innerHeight < 700 ? 180 : 260; }
  function applyDrawerHeight() {
    var max = Math.floor(window.innerHeight * 0.7), min = 140;
    if (!drawerH) { try { drawerH = parseInt(localStorage.getItem('igneum.drawer-h'), 10) || 0; } catch (e) { } }
    var h = Math.max(min, Math.min(max, drawerH || defaultDrawerH()));
    document.body.style.setProperty('--drawer-h', h + 'px');
    if (drawerOpen()) renderLog(true);
  }
  (function () {
    var grip = $('drawer-grip'), startY = 0, startH = 0;
    grip.addEventListener('pointerdown', function (e) {
      startY = e.clientY; startH = parseInt(getComputedStyle(document.body).getPropertyValue('--drawer-h'), 10) || defaultDrawerH();
      grip.setPointerCapture(e.pointerId); document.body.classList.add('drawer-drag'); e.preventDefault();
    });
    grip.addEventListener('pointermove', function (e) {
      if (!document.body.classList.contains('drawer-drag')) return;
      drawerH = startH + (startY - e.clientY); applyDrawerHeight();
    });
    function end(e) { if (!document.body.classList.contains('drawer-drag')) return; document.body.classList.remove('drawer-drag'); try { localStorage.setItem('igneum.drawer-h', String(parseInt(getComputedStyle(document.body).getPropertyValue('--drawer-h'), 10))); } catch (x) { } }
    grip.addEventListener('pointerup', end); grip.addEventListener('pointercancel', end);
    grip.addEventListener('dblclick', function () { drawerH = defaultDrawerH(); applyDrawerHeight(); try { localStorage.removeItem('igneum.drawer-h'); } catch (x) { } });
  })();
  window.addEventListener('resize', function () { if (drawerOpen()) applyDrawerHeight(); });
  document.querySelectorAll('#log-chips .chip').forEach(function (c) { c.addEventListener('click', function () { document.querySelectorAll('#log-chips .chip').forEach(function (x) { x.classList.toggle('on', x === c); }); logFilter = c.getAttribute('data-src'); viewDirty = true; renderLog(true); }); });
  $('log-search').addEventListener('input', function () { clearTimeout(searchT); var v = this.value; searchT = setTimeout(function () { logQuery = v.trim().toLowerCase(); viewDirty = true; renderLog(true); }, 120); });
  $('log-search').addEventListener('keydown', function (e) { if (e.key === 'Escape') { this.value = ''; logQuery = ''; viewDirty = true; renderLog(true); } });
  $('log-wrap').addEventListener('click', function () { wrap = !wrap; this.classList.toggle('on', wrap); logView.classList.toggle('wrap', wrap); renderLog(true); });
  $('log-follow').addEventListener('change', function () { setFollow(this.checked); if (follow) renderLog(true); });
  $('log-jump').addEventListener('click', function () { setFollow(true); renderLog(true); });
  $('log-copy').addEventListener('click', function () {
    var r = visibleRange(), rows = view.slice(r[0], r[1]);
    if (!rows.length) { toast('Nothing to copy'); return; }
    copyText(rows.map(function (l) { return clock(l.t) + ' ' + l.src.padEnd(6) + ' ' + l.text; }).join('\n'), 'Copied ' + rows.length + ' line' + (rows.length === 1 ? '' : 's'));
  });
  $('log-last-error').addEventListener('click', function () { jumpToLast(isErr, 'No error in the log'); });
  $('log-last-swap').addEventListener('click', function () { jumpToLast(isSwap, 'No program swap in the log yet'); });
  $('log-time').addEventListener('keydown', function (e) { if (e.key === 'Enter') { jumpToTime(this.value); } });
  $('log-time').addEventListener('change', function () { jumpToTime(this.value); });
  $('log-ruler').addEventListener('click', function (e) {
    if (!view.length) return;
    var r = this.getBoundingClientRect(), f = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
    var t0 = view[0].t, t1 = view[view.length - 1].t;
    jumpToIndex(lowerBound(view, t0 + f * (t1 - t0)));
  });
  function setFollow(on) { follow = on; $('log-follow').checked = on; $('log-jump').hidden = on; }
  function lowerBound(arr, t) { var lo = 0, hi = arr.length; while (lo < hi) { var mid = (lo + hi) >> 1; if (arr[mid].t < t) lo = mid + 1; else hi = mid; } return lo; }
  function jumpToTime(v) {
    var m = /^(\d{1,2}):(\d{2})(?::(\d{2}))?$/.exec((v || '').trim());
    if (!m) { toast('Type a time as HH:MM:SS'); return; }
    if (!view.length) { toast('Nothing in the log'); return; }
    var last = new Date(view[view.length - 1].t * 1000), d = new Date(last.getTime());
    d.setHours(+m[1], +m[2], +(m[3] || 0), 0);
    if (d.getTime() > last.getTime() + 1000) d.setDate(d.getDate() - 1); // a time later than the newest line means yesterday
    var target = d.getTime() / 1000;
    if (target < view[0].t - 1) { toast('The log starts at ' + clock(view[0].t)); return; }
    jumpToIndex(lowerBound(view, target));
  }
  function jumpToLast(test, missing) {
    for (var i = view.length - 1; i >= 0; i--) if (test(view[i])) { jumpToIndex(i); return; }
    toast(missing);
  }
  var hitSeq = 0, hitT = 0;
  function jumpToIndex(i) {
    if (!view.length) return;
    i = Math.max(0, Math.min(view.length - 1, i));
    setFollow(false);
    hitSeq = view[i].seq; hitT = performance.now();
    autoScroll = true;
    logView.scrollTop = Math.max(0, rowTop(i) - Math.floor(logView.clientHeight / 3));
    renderLog(true);
    setTimeout(function () { hitSeq = 0; renderLog(true); }, 1600);
  }
  function rowTop(i) {
    if (!wrap) return i * RH;
    var kids = logPre.children; if (i < kids.length) return kids[i].offsetTop; return logPre.scrollHeight;
  }
  function visibleRange() {
    if (!view.length) return [0, 0];
    if (wrap) { // find by offsetTop (the rows are all in the DOM)
      var kids = logPre.children, top = logView.scrollTop, bottom = top + logView.clientHeight, a = -1, b = kids.length;
      for (var k = 0; k < kids.length; k++) { var y = kids[k].offsetTop; if (a < 0 && y + kids[k].offsetHeight > top) a = k; if (y >= bottom) { b = k; break; } }
      return [Math.max(0, a), b];
    }
    var s = Math.floor(logView.scrollTop / RH), n = Math.ceil(logView.clientHeight / RH) + 1;
    return [Math.max(0, s), Math.min(view.length, s + n)];
  }
  function rebuildView() {
    var q = logQuery;
    view = logLines.filter(function (l) { return (!logFilter || l.src.indexOf(logFilter) === 0) && (!q || l.text.toLowerCase().indexOf(q) >= 0 || l.src.indexOf(q) >= 0); });
    viewDirty = false; winStart = winEnd = -1;
  }
  function appendView(lines) {
    var q = logQuery;
    for (var i = 0; i < lines.length; i++) { var l = lines[i]; if ((!logFilter || l.src.indexOf(logFilter) === 0) && (!q || l.text.toLowerCase().indexOf(q) >= 0 || l.src.indexOf(q) >= 0)) view.push(l); }
  }
  function rowHtml(l) {
    var text = esc(l.text), q = logQuery;
    if (q) { // highlight every match (case-insensitive, on the escaped text; the query itself is escaped too)
      var eq = esc(q), lower = text.toLowerCase(), out = '', at = 0, idx;
      while ((idx = lower.indexOf(eq, at)) >= 0) { out += text.slice(at, idx) + '<mark>' + text.slice(idx, idx + eq.length) + '</mark>'; at = idx + eq.length; }
      text = out + text.slice(at);
    }
    return '<div class="ln' + (isErr(l) ? ' e' : '') + (hitSeq && l.seq === hitSeq ? ' hit' : '') + '" title="' + clock(l.t) + '"><span class="src ' + esc(l.src) + '">' + clock(l.t) + ' ' + esc(l.src.padEnd(6)) + '</span> ' + text + '</div>';
  }
  function renderLog(force) {
    if (!drawerOpen()) return;
    var a = performance.now();
    if (viewDirty) rebuildView();
    var n = view.length;
    $('log-empty').hidden = n > 0;
    $('log-empty').textContent = logLines.length ? 'No line matches.' : 'Nothing logged yet. Lines appear as the node and the miner write them.';
    $('log-count').textContent = (logQuery || logFilter) ? (n + ' of ' + logLines.length) : (n ? String(n) : '');
    var canWrap = n <= WRAP_MAX; $('log-wrap').disabled = !canWrap; $('log-wrap').title = canWrap ? 'Wrap long lines' : 'Wrap is off above 5,000 lines; filter or search first';
    if (wrap && canWrap) {
      logPad.style.height = ''; logPre.style.transform = '';
      if (force || winStart !== 0 || winEnd !== n) { logPre.innerHTML = view.map(rowHtml).join(''); winStart = 0; winEnd = n; }
    } else {
      if (wrap && !canWrap) { wrap = false; $('log-wrap').classList.remove('on'); logView.classList.remove('wrap'); }
      logPad.style.height = (n * RH) + 'px';
      if (follow) { autoScroll = true; logView.scrollTop = Math.max(0, n * RH - logView.clientHeight); }
      else if (logView.scrollTop > n * RH) { autoScroll = true; logView.scrollTop = Math.max(0, n * RH - logView.clientHeight); }
      var r = visibleRange(), s = Math.max(0, r[0] - OVERSCAN), e = Math.min(n, r[1] + OVERSCAN);
      if (force || s !== winStart || e !== winEnd) {
        var h = ''; for (var i = s; i < e; i++) h += rowHtml(view[i]);
        logPre.innerHTML = h; logPre.style.transform = 'translateY(' + (s * RH) + 'px)'; winStart = s; winEnd = e;
      }
    }
    if (follow && wrap) { autoScroll = true; logView.scrollTop = logView.scrollHeight; }
    updateAt(); updateRuler(force);
    perf.log += performance.now() - a;
  }
  function updateAt() {
    var r = visibleRange();
    $('log-at').textContent = view.length ? clock(view[Math.min(view.length - 1, r[0])].t) : '';
  }
  // the ruler: the span of the view, error (ember) and swap (molten) marks, and the window in view; rebuilt when the
  // count changes, at most every 2 s, from a sample of at most 4,000 lines
  function updateRuler(force) {
    var ruler = $('log-ruler'), n = view.length;
    if (!n) { ruler.classList.add('empty'); return; }
    ruler.classList.remove('empty');
    var t0 = view[0].t, t1 = view[n - 1].t, span = Math.max(1, t1 - t0);
    var r = visibleRange(), win = $('log-ruler-win'), i0 = Math.min(n - 1, r[0]), i1 = Math.max(i0, Math.min(n - 1, r[1] - 1));
    win.style.left = ((view[i0].t - t0) / span * 100) + '%';
    win.style.width = Math.max(0.5, (view[i1].t - view[i0].t) / span * 100) + '%';
    var now = performance.now();
    if (n === rulerCount && now - rulerAt < 2000) return;
    rulerAt = now; rulerCount = n;
    $('log-ruler-0').textContent = clock(t0); $('log-ruler-1').textContent = clock(t1);
    ruler.querySelectorAll('.mk').forEach(function (m) { m.remove(); });
    var step = Math.max(1, Math.floor(n / 4000)), h = '';
    for (var i = 0; i < n; i += step) { var l = view[i]; var sw = isSwap(l); if (sw || isErr(l)) h += '<i class="mk ' + (sw ? 'swap' : 'err') + '" style="left:' + ((l.t - t0) / span * 100).toFixed(2) + '%" title="' + clock(l.t) + (sw ? ' program swap' : ' error') + '"></i>'; }
    win.insertAdjacentHTML('beforebegin', h);
  }
  logView.addEventListener('scroll', function () {
    if (autoScroll) { autoScroll = false; return; }
    var atEnd = logView.scrollTop + logView.clientHeight >= logView.scrollHeight - RH;
    if (atEnd !== follow) setFollow(atEnd);
    if (!scrollRaf) scrollRaf = requestAnimationFrame(function () { scrollRaf = 0; renderLog(false); });
  });
  function pollLog() {
    if (!drawerOpen() || hiddenNow()) return;
    api('api/log?after=' + logAfter + '&limit=' + (logAfter ? 400 : 5000)).then(function (r) {
      perf.polls++;
      if (r.lines && r.lines.length) {
        logLines = logLines.concat(r.lines); logAfter = r.lines[r.lines.length - 1].seq;
        if (logLines.length > LOG_CAP) { logLines = logLines.slice(-LOG_CAP); viewDirty = true; } else if (!viewDirty) appendView(r.lines);
        var a = performance.now(); renderLog(false); perf.poll += performance.now() - a;
      }
    }).catch(function () { });
  }
  function scheduleLogPoll() { clearTimeout(logTimer); logTimer = setTimeout(function () { pollLog(); if (drawerOpen()) scheduleLogPoll(); }, 1500); }



  // ---------- the blocks strip (your blocks over the last 10 minutes) ----------
  // The minute grid never moves (it hangs off "now", which is always the right edge), so it is drawn once per size
  // into an offscreen layer. A frame is clear + the layer + the rail + the blocks. Blocks drift 1.7 px/s, so the
  // strip redraws twice a second; while a block flashes (1.2 s after it appears) it runs at the display rate. Nothing
  // draws while the window is hidden (the hosts hide it to the menu bar or the tray).
  var c = $('dag'), x = c.getContext('2d'), W = 0, H = 0, born = {}, grid = document.createElement('canvas'), gx = grid.getContext('2d');
  var SPAN = 600, S = 12, PAD = 10, RIGHT_PAD = 26;
  function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function size() {
    W = c.clientWidth; H = c.clientHeight; if (!W || !H) return;
    c.width = W * dpr; c.height = H * dpr; x.setTransform(dpr, 0, 0, dpr, 0, 0);
    grid.width = W * dpr; grid.height = H * dpr; gx.setTransform(dpr, 0, 0, dpr, 0, 0);
    gx.clearRect(0, 0, W, H);
    var right = W - RIGHT_PAD, left = PAD, pxs = (right - left) / SPAN, line = css('--line') || '#2A2A30', ash = css('--ash') || '#9A9A9E', molten = css('--molten') || '#FFB35C';
    gx.font = '500 10px IBM Plex Mono, monospace'; gx.textAlign = 'center';
    for (var m = 0; m <= 10; m++) {
      var px = right - m * 60 * pxs;
      gx.strokeStyle = m === 0 ? molten : line; gx.lineWidth = 1; gx.setLineDash(m === 0 ? [] : [3, 6]);
      gx.beginPath(); gx.moveTo(px, 10); gx.lineTo(px, H - 18); gx.stroke(); gx.setLineDash([]);
      if (px > 30 && (m === 0 || m % 2 === 0)) { gx.fillStyle = ash; gx.fillText(m === 0 ? 'now' : '-' + m + ' min', px, H - 5); }
    }
    var rail = (H - 12) * 0.5;
    gx.strokeStyle = line; gx.lineWidth = 1; gx.beginPath(); gx.moveTo(left, rail); gx.lineTo(right, rail); gx.stroke();
    requestDraw();
  }
  window.addEventListener('resize', function () { size(); });
  function rrect(px, py, w, h, r) { x.beginPath(); x.moveTo(px + r, py); x.lineTo(px + w - r, py); x.quadraticCurveTo(px + w, py, px + w, py + r); x.lineTo(px + w, py + h - r); x.quadraticCurveTo(px + w, py + h, px + w - r, py + h); x.lineTo(px + r, py + h); x.quadraticCurveTo(px, py + h, px, py + h - r); x.lineTo(px, py + r); x.quadraticCurveTo(px, py, px + r, py); x.closePath(); }
  // returns true while something is animating (a flash), so the caller keeps the fast rate
  function draw(now) {
    if (!W || !H) size();
    if (!W) return false;
    x.clearRect(0, 0, W, H);
    x.drawImage(grid, 0, 0, W, H);
    var right = W - RIGHT_PAD, pxs = (right - PAD) / SPAN;
    var nowU = state ? (state.now + (performance.now() - stateAt) / 1000) : Date.now() / 1000;
    var found = state ? state.mining.found : [];
    var rail = (H - 12) * 0.5, shown = 0, animating = false;
    for (var i = 0; i < found.length; i++) {
      var t = found[i], age = nowU - t; if (age < 0 || age > SPAN) continue; shown++;
      var key = String(t);
      if (!born[key]) born[key] = age < 5 ? now : now - 2000; // only a block that just arrived flashes, not the whole strip on first paint
      var fl = Math.max(0, 1 - (now - born[key]) / 1200);
      if (fl > 0) animating = true;
      var px = right - age * pxs, py = rail + ((i % 3) - 1) * 15;
      if (fl > 0) { var g = x.createRadialGradient(px, py, 0, px, py, S * 2.2); g.addColorStop(0, 'rgba(255,179,92,' + (0.6 * fl) + ')'); g.addColorStop(1, 'rgba(255,179,92,0)'); x.fillStyle = g; x.beginPath(); x.arc(px, py, S * 2.2, 0, Math.PI * 2); x.fill(); }
      x.fillStyle = '#F2541B'; rrect(px - S / 2, py - S / 2, S, S, 3); x.fill();
      if (fl > 0) { x.strokeStyle = 'rgba(255,179,92,' + fl + ')'; x.lineWidth = 2; rrect(px - S / 2 - 2, py - S / 2 - 2, S + 4, S + 4, 4); x.stroke(); }
    }
    // forget flashes older than the strip, so the map does not grow for a day-long run
    if (found.length && Object.keys(born).length > found.length + 50) { var keep = {}; for (var k = 0; k < found.length; k++) if (born[String(found[k])]) keep[String(found[k])] = born[String(found[k])]; born = keep; }
    if (!shown) {
      x.fillStyle = css('--ash') || '#9A9A9E'; x.font = '500 12px IBM Plex Mono, monospace'; x.textAlign = 'center';
      var msg = !state ? 'waiting for the engine' : state.mining.state === 'mining' ? 'Mining. A block this machine finds appears here' : state.mining.state === 'paused' ? 'Paused. No blocks while mining is stopped' : state.node.synced ? 'waiting for the card' : 'waiting for work from the pool';
      x.fillText(msg, W / 2, rail - 10);
    }
    return animating && !reduce;
  }
  var stateAt = performance.now(), drawTimer = null, drawRaf = 0;
  function frame(now) {
    drawRaf = 0; clearTimeout(drawTimer); drawTimer = null;
    if (hiddenNow()) return;
    var fast = false;
    if (phase === 'dashboard' && page === 'mine' && !$('dag').hidden) { var a = performance.now(); fast = draw(now); perf.draw += performance.now() - a; perf.frames++; }
    if (fast) drawRaf = requestAnimationFrame(frame);
    else drawTimer = setTimeout(function () { drawTimer = null; drawRaf = requestAnimationFrame(frame); }, reduce ? 1000 : 500);
  }
  function requestDraw() { if (hiddenNow() || drawRaf) return; clearTimeout(drawTimer); drawTimer = null; drawRaf = requestAnimationFrame(frame); }
  document.addEventListener('visibilitychange', function () {
    if (hiddenNow()) { clearTimeout(drawTimer); drawTimer = null; if (drawRaf) { cancelAnimationFrame(drawRaf); drawRaf = 0; } clearTimeout(logTimer); logTimer = null; if (typeof liveTimer !== 'undefined') { clearTimeout(liveTimer); liveTimer = null; } }
    else { poll(); requestDraw(); if (drawerOpen()) { pollLog(); scheduleLogPoll(); } if (typeof pollLive === 'function') pollLive(); }
  });
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', function () { size(); });
  requestDraw();

  // ---------- the network scene (your shares landing in blocks) ----------
  // The showcase seat's component (chain.js, site/scenes/CHAIN-SCENE.md) in the chain card, fed by the engine's
  // api/scene (this session's accepted shares with their heights and weights, and the blocks that carry them from
  // the chain's share account) once per block, or sooner when the head moves; the founder's brief of 12 October
  // 2026: a person watches their own shares go into the chain. Nothing draws while the window is hidden.
  var sceneEl = $('scene'), scene = null, sceneNet = null, sceneTimer = null, sceneHead = -1, sceneInterval = View.BLOCK_S;
  // F-E35: the stage under a pool on a chain the public feed does not carry: the network source stopped, the stage hidden
  var dagForeign = false;
  function minedChainId() { var sc = ((state || {}).source || {}).chain_id; return sc > 0 ? Number(sc) : undefined; }
  function setDagForeign(f) {
    if (f === dagForeign) return;
    dagForeign = f;
    if (f) {
      if (sceneNet && sceneNet.stop) { try { sceneNet.stop(); } catch (e) {} }
      sceneNet = null;
      if (sceneEl) sceneEl.hidden = true;
    } else {
      if (sceneEl) sceneEl.hidden = false;
      if (scene && window.IgneumChainScene.sources && window.IgneumChainScene.sources.network && !sceneNet) {
        try { sceneNet = window.IgneumChainScene.sources.network({ rpcUrl: 'api/rpc', statusUrl: 'api/feed/status', feedUrl: 'api/feed/devnet', sharesUrl: null, name: 'the chain this machine mines', chain_id: minedChainId() }).start(scene); } catch (e) { sceneNet = null; }
      }
    }
  }
  // 0.2.5 (the founder's order of 18:5x UK, 12 October 2026: the work between blocks): this machine's shares as sparks
  // on the newest block's row and the tally, from the engine's api/shares (the live-shares feed, cached 2 s) every 2 s
  var sharesTimer = null;
  function pollShares() {
    if (!scene || !scene.pushShares || hiddenNow()) return;
    fetch('api/shares', { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) { if (d && !d.error && scene && scene.pushShares) scene.pushShares(d); }).catch(function () {});
  }
  function startShares() { if (sharesTimer) return; pollShares(); sharesTimer = setInterval(pollShares, 2000); }
  // the share row (the founder's ask, 12 October 2026): the last shares of the session as lit blocks, the pool's next
  // block as the door at the row's end; a block the ledger carries for this miner closes the row with the slice named
  var sceneDoc = null, shareKeys = '', nbTimer = null;
  function renderShareRow(doc) {
    var row = $('share-row'), track = $('share-track'); if (!row || !track) return;
    var fit = View.stripCapacity(track.clientWidth || 0) || 40, shares = (doc.shares || []).slice(-Math.min(40, fit)), blocks = doc.blocks || [], tot = doc.totals || {};
    row.hidden = !(shares.length || (tot.accepted > 0));
    // the first accepted share marked once per install (the founder's cell): the ember pulse on the row and its line for a minute
    var marked = stored('igneum.first-share-at');
    if (View.firstShareMark(tot.accepted, marked)) { store('igneum.first-share-at', String(Date.now())); marked = String(Date.now()); row.classList.add('first'); setTimeout(function () { row.classList.remove('first'); }, 1500); }
    var firstFresh = marked && (Date.now() - Number(marked)) < 60000;
    // the strip is laid again when the track's width changes (the squares fit the track at each render: on 0.2.21 a
    // strip laid for a wider window, or for 40 before the row was shown, kept its squares and the door past the track's
    // edge at 586 to 720 wide, hidden by the track's overflow, until the next share re-laid it)
    var keys = fit + '#' + shares.map(function (s) { return s.height + ':' + s.at_unix + ':' + s.job; }).join('|');
    // F-E38 (the pool-b2 capture of 08:36 UK, 14 October 2026): after a source switch the strip kept the previous
    // session's lit squares under "no share yet"; the strip follows the session: no accepted share, no lit square
    if (!(tot.accepted > 0)) { shares = []; keys = fit + '#'; }
    if (keys !== shareKeys) {
      shareKeys = keys;
      var last = blocks.length ? blocks[blocks.length - 1] : null;
      track.innerHTML = shares.map(function (s) { return '<i class="' + (s.fee ? 'fee' : '') + '" title="height ' + withCommas(s.height) + (s.weight ? ' · weight ' + s.weight : '') + '"></i>'; }).join('') + '<i class="door' + (last && last.state === 'paid' ? ' paid' : '') + '" title="the pool\u2019s next block"></i>';
    }
    setText('share-count', tot.accepted > 0 ? withCommas(tot.accepted) + ' share' + (tot.accepted === 1 ? '' : 's') + ' accepted this session' + (tot.rejected > 0 ? ' · ' + withCommas(tot.rejected) + ' rejected' : '') : 'no share yet');
    var carried = blocks.filter(function (b) { return b.my_shares > 0; }), paid = carried.filter(function (b) { return b.state === 'paid'; });
    // the two spans named (main's audit of 14 October 2026): the strip counts this session, this line the address over the scene's last 64 blocks
    setText('share-slice', firstFresh ? 'your first share, paid at the next window' : carried.length ? ('your address’s shares sit in ' + withCommas(carried.length) + ' of the last 64 blocks' + (paid.length ? ', ' + withCommas(paid.length) + ' paid' : '')) : '');
  }
  // the expected next block: the feed's head time against the hour's mean; past the mean it reads late by N s (the ruled words)
  function renderNextBlock(doc) {
    var el = $('next-block'); if (!el) return;
    // 0.2.5 (the founder's order of 18:5x UK, 12 October 2026): the scene draws the countdown on the chain's clock; Ember's own bar stays hidden
    el.hidden = true; clearInterval(nbTimer); nbTimer = null; return;
    var head = doc.head || {}; el.hidden = !(head.height > 0 && head.at_unix > 0);
    clearInterval(nbTimer); nbTimer = null;
    if (el.hidden) return;
    function tick() {
      var mean = head.mean_s || head.block_interval_s || View.BLOCK_S, age = Math.max(0, Date.now() / 1000 - head.at_unix), frac = Math.min(1, age / mean), fill = $('nb-fill');
      fill.style.width = Math.round(frac * 100) + '%'; fill.classList.toggle('any', age >= mean);
      setText('nb-text', age >= mean ? 'late by ' + Math.max(1, Math.round(age - mean)) + ' s · ' + withCommas(head.height + 1) : 'next block in ' + Math.max(1, Math.round(mean - age)) + ' s · ' + withCommas(head.height + 1));
    }
    tick(); nbTimer = setInterval(tick, 1000);
  }
  // the IGN tile on a pool session (the founder's order of 10:4x UK, 12 October 2026): the ledger's paid total for the
  // payout address, and the share state as its line; never the solo words no block yet
  function wei(s) { var n = Number(s || 0); return isFinite(n) ? n / 1e18 : 0; }
  function renderIgnTile(s, doc) {
    var tot = (doc && doc.totals) || {}, blocks = (doc && doc.blocks) || [], paidIgn = wei(tot.paid_wei), ch = (doc && doc.chain) || {}, lg = (doc && doc.ledger) || {};
    var paid = blocks.filter(function (b) { return b.state === 'paid' && b.my_shares > 0; }), lastPaid = paid.length ? paid[paid.length - 1] : null;
    // the headline is the chain's balance of the payout address (main's order of 11:1x UK, 12 October 2026, the
    // founder's "not seeing any coins"): read through the public RPC every block; the ledger's rows are the detail
    var onChain = ch.balance_wei != null ? wei(ch.balance_wei) : null, headIgn = onChain != null ? onChain : paidIgn;
    // never a 0 that is not read: until the chain answers the first time the tile says reading (the three-card test
    // machine's 0.2.5 set, 22:4x UK, 12 October 2026: "0 IGN read at block 0" three minutes in)
    setText('t-ign', (onChain == null || ((ch.balance_head || 0) === 0 && ((doc.head && doc.head.height) || 0) > 0)) && !(paidIgn > 0) ? 'reading' : headIgn > 0 ? View.fmtIgn(headIgn).replace(' IGN', '') : '0');
    $('t-ign').title = onChain != null ? (View.chainWord(ch.chain_id) + '’s balance of ' + (doc.address || 'the payout address') + ' at block ' + withCommas(ch.balance_head || 0) + (lg.behind > 2 ? ' · the ledger’s detail is ' + withCommas(lg.behind) + ' blocks behind' : '')) : (ch.error || '');
    // both modes pay the same way (the founder's addendum of 10:5x UK): a share is carried in the next block and paid
    // in it, exact to the wei; through the pool the pool carries it under its session, less the fee; no lottery words
    var solo = !!(s.source && s.source.kind === 'node'), n = tot.accepted || 0, shares = withCommas(n) + ' share' + (n === 1 ? '' : 's');
    // main's read of 14 October 2026 (22:3x UK, 0.2.14): on an idle engine the caption is the strip's reason or "Not
    // mining", never "Searching"; "Searching" is for a worker running on a mining engine
    var idleWhy = (s.mining && s.mining.state !== 'mining' && s.mining.state !== 'waiting') ? (View.idleReason(s.mining) || 'Not mining') : '';
    var line = n === 0 ? (idleWhy ? idleWhy : View.workerUp(s.mining) ? ('Searching, no share accepted yet' + (View.machineRate((s.mining || {}).cards).length ? ' · ' + View.machineRate(s.mining.cards).map(function (l) { return l.text + ' ' + l.unit; }).join(', ') : (s.mining && s.mining.hash_total > 0) ? ' · ' + Number(s.mining.hash_total).toFixed(1) + ' MH/s' : '')) : 'Not mining')
      : lastPaid ? (shares + ' · last paid in block ' + withCommas(lastPaid.height))
      : (shares + ' accepted, ' + (solo ? 'first pay in the next block' : 'the pool\u2019s next block pays'));
    if (n > 0 && !lastPaid && lg.behind > 2 && lg.head > 0) line = shares + ' accepted · ledger ' + withCommas(lg.behind) + ' blocks behind';
    // F-E21 (E2, 14 October 2026): the owner sees the work the chain will not pay. With the ledger's totals in, the
    // line carries paid, carried and lost to a race (expired by a reorganisation) beside the accepted count; the
    // identity is the ledger's: accepted = paid + in the carry + pooled + expired at the window + expired by a reorg
    // the founder's word of 10:5x UK, 14 October 2026: the tile's sub-line is ONE line that never wraps, so the ledger's
    // words go under the shares strip (the held line's place, beside a held line when there is one)
    var lost = View.mineShareWords(tot, lastPaid);
    // main's ruling of 18:2x UK, 12 October 2026: one short line under the figure; the held shares as one quiet line
    // under the shares strip; the no-value words as the card's footer once
    var held = (s.mining && s.mining.held_session) || 0, heldEl = $('share-held');
    if (heldEl) { var hw = held > 0 && s.mining && s.mining.held_case ? withCommas(held) + ' share' + (held === 1 ? '' : 's') + ' held ' + View.heldWords(s.mining.held_case) : ''; if (lost) hw = hw ? lost + ' · ' + hw : lost; heldEl.hidden = !hw; setText('share-held', hw); }
    setText('t-ign-sub', line.replace(' · last paid in block ', ' · paid at block '));
    $('t-ign').classList.toggle('is-met', headIgn > 0); $('t-ign').classList.remove('is-estimate');
  }
  function mountDag() {
    if (scene || !sceneEl || !window.IgneumChainScene) return;
    // settle_first (the showcase seat's 634c27f4; main's ruling of 13:2x UK, 12 October 2026): the opening picture
    // whole at the first push, the arrival animated on the pushes after; ?settle=1 settles every push (captures)
    // the app form (main's read of the 0.2.4 capture, 14:3x UK, 12 October 2026; the showcase seat's d34e4388): rows of
    // 48 px, three named sealer rows and one "others" row, the card about 140 px so the whole card with its bar fits a
    // 900 px window; Ember keeps its own next-block bar under the card (next_block: false)
    scene = window.IgneumChainScene.mount(sceneEl, { interval_s: sceneInterval, label: 'Your shares landing in blocks as the chain seals them', form: 'app', settle_first: true });
    // 0.2.8: the mount reachable by a reader (the showcase's watcher reads state().edge from the page); the pool's
    // chain id rides the network source so the component's own gate (main bc745d3f) draws no other chain's blocks
    try { window.emberScene = scene; } catch (e) {}
    // the chain's own clock, matched with the site (the founder's order of 21:5x UK, 13 October 2026): the component's
    // network source reads the chain's latest header every second and the band's 64 headers through the engine's own
    // origin (api/rpc, api/feed/*); this page's miner document still lands this machine's shares on the same stage
    if (window.IgneumChainScene.sources && window.IgneumChainScene.sources.network && !sceneNet) {
      try { sceneNet = window.IgneumChainScene.sources.network({ rpcUrl: 'api/rpc', statusUrl: 'api/feed/status', feedUrl: 'api/feed/devnet', sharesUrl: null, name: 'the chain this machine mines', chain_id: minedChainId() }).start(scene); } catch (e) { sceneNet = null; }
    }
      startShares();
    pollLive();
  }
  function pollLive() {
    clearTimeout(sceneTimer); sceneTimer = null;
    if (phase !== 'dashboard' || hiddenNow()) return;
    fetch('api/scene', { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (doc) {
      if (!scene) mountDag();
      if (doc && doc.head) {
        sceneInterval = doc.head.block_interval_s || sceneInterval;
        sceneDoc = doc; renderShareRow(doc); renderNextBlock(doc); if (state) renderIgnTile(state, doc);
        if (scene) {
          var sdoc = window.IgneumChainScene.sources.miner(doc); if (sdoc && params.get('settle') === '1') sdoc.settle = true;
          // the run of record of 21:34Z (13 October 2026): two anchors fought on Ember's stage (this document's head from
          // the feed through the engine's cache beside the network source's header every second), so the countdown
          // jumped 55, 25, 24 against the site's 13, 17, 16; the network source alone owns the head and the clock and
          // this document brings the blocks' own figures (this machine's shares) and the particles only
          if (sdoc && sceneNet) sdoc.head = { block_interval_s: sdoc.head && sdoc.head.block_interval_s };
          if (!dagForeign) scene.push(sdoc);
        }
        // the eyebrow names a feed fault in the card (the founder's rule of 11:0x UK, 12 October 2026: the card is
        // never blank and never silent about why); live otherwise
        // the eyebrow names only a feed fault (main's read of the 0.2.4 capture, 13:5x UK, 12 October 2026: the stage
        // carries its own LIVE tag, so a lagging feed never reads "waiting" beside it); live otherwise
        var fc = (doc.feed || {}).chain_id || 0, sc = ((state || {}).source || {}).chain_id || 0;
        // F-E35 (main's audit of the 0.2.7 captures, 07:0x UK, 14 October 2026): the stage never draws another chain's
        // blocks under the pool this machine mines. When the public feed is another chain's (the engine's F-E4 word:
        // "the chain feed is chain 7320's, not the mined chain 7357's") the network source stops, the stage is
        // cleared and the eyebrow says which chain has no feed; a feed that is unreadable says so with its error;
        // otherwise "live · chain N, the chain this machine mines", as 0.2.5 read
        var foreign = sc > 0 && fc > 0 && fc !== sc;
        setDagForeign(foreign);
        var feedErr = doc.feed && doc.feed.error && !foreign ? String(doc.feed.error) : '';
        setText('dag-state', View.dagStateWords(fc, sc, (doc.feed || {}).name, feedErr));
        $('dag-card').title = feedErr ? feedErr : (doc.ledger && doc.ledger.error) || '';
      }
    }).catch(function () { setText('dag-state', 'reading the chain'); }).then(function () {
      if (!hiddenNow()) sceneTimer = setTimeout(pollLive, Math.max(5, sceneInterval) * 1000);
    });
  }
  // ---------- miner-ui-5: the miner's first month ----------
  // the chain facts (GET api/ladder, src/chainfacts.rs) every 15 s while the dashboard shows; `chain` feeds the
  // Poisson line, the block card, Earnings and the ladder; every number names its field on hover (View.FIELDS)
  var chain = null, chainAt = 0, chainBusy = false, miningSince = 0, cardDismissed = stored('igneum.card.dismissed') || '', cardSaving = false, chainSig = '';
  function pollChain(force) {
    if (phase !== 'dashboard' || hiddenNow() || chainBusy) return;
    if (!force && Date.now() - chainAt < 15000) return;
    chainBusy = true;
    api('api/ladder').then(function (r) { chain = r; chainAt = Date.now(); chainBusy = false; var sig = JSON.stringify(r); if (sig !== chainSig) { chainSig = sig; if (state) renderPage(state); } }).catch(function () { chainBusy = false; chainAt = Date.now(); });
  }
  $('s-profile').addEventListener('change', function () { var on = this.checked; api('api/settings', { profile_public: on }).then(function (r) { if (r.ok) toast(on ? 'Your page is public from the next site read' : 'Your page is unlisted again'); else { toast(r.error || 'could not change'); $('s-profile').checked = !on; } }).catch(function () { toast('This engine has no profile switch yet'); $('s-profile').checked = !on; }); });
  // the block card's buttons
  // first-block-21: the card is spent once a window has shown it. The first render in a visible window reports it to
  // the engine (api/card/seen: the lifetime flag, the drop at the engine's next start); the dismiss reports it too; and
  // the window hiding to the tray or menu bar with the card on screen counts as the dismiss, so a second open in the
  // same run shows nothing either.
  var cardSeenKey = '';
  function reportCardSeen(bc) { if (!bc || cardSeenKey === bc.key) return; cardSeenKey = bc.key; var b = View.cardSeenBody(bc); if (b) api('api/card/seen', b).catch(function () { }); }
  function dismissCard() { cardDismissed = $('block-card').dataset.key || ''; store('igneum.card.dismissed', cardDismissed); $('block-card').hidden = true; }
  $('bc-close').addEventListener('click', function () { if (state && state.ladder) reportCardSeen(View.blockCard(state.ladder.milestone, state, chain, state.now)); dismissCard(); });
  document.addEventListener('visibilitychange', function () { if (document.hidden && !$('block-card').hidden && $('block-card').dataset.key) dismissCard(); });
  $('bc-open').addEventListener('click', function () { var u = $('block-card').dataset.url; if (u) api('api/open', { url: u }); else toast('The hash is not known yet'); });
  $('bc-copy').addEventListener('click', function () { var u = $('block-card').dataset.url; if (u) copyText(u, 'Link copied'); else toast('The hash is not known yet'); });
  $('bc-save').addEventListener('click', function () {
    if (!state || cardSaving) return;
    var m = View.blockCard(state.ladder.milestone, state, chain, state.now); if (!m) return;
    cardSaving = true; setText('bc-saved', 'drawing');
    drawCard($('bc-canvas'), m, state).then(function (png) {
      return api('api/card', { name: 'block-' + (m.count || 0), png: png }).then(function (r) { cardSaving = false; if (r.ok) { setText('bc-saved', 'saved: ' + r.path); toast('Card saved. The folder is open'); } else { setText('bc-saved', ''); toast(r.error || 'could not save'); } });
    }).catch(function (e) { cardSaving = false; setText('bc-saved', ''); toast('Could not draw the card: ' + (e && e.message || e)); });
  });
  // the PNG, 1200x630, drawn from the card's own words with the local fonts: no network call. The miner's object,
  // never the project's number: the card, the rate, the IGN, the rank and the day, the hash and the link.
  function drawCard(cv, m, s) {
    var loads = ['900 72px Unbounded', '700 28px Unbounded', '500 30px "IBM Plex Sans"', '400 22px "IBM Plex Mono"', '500 18px "IBM Plex Mono"'].map(function (f) { return document.fonts && document.fonts.load ? document.fonts.load(f).catch(function () { }) : Promise.resolve(); });
    return Promise.all(loads).then(function () {
      var W = 1200, H = 630, x = cv.getContext('2d');
      cv.width = W; cv.height = H;
      x.fillStyle = '#0C0C0E'; x.fillRect(0, 0, W, H);
      var g = x.createRadialGradient(W - 140, H + 60, 20, W - 140, H + 60, 620); g.addColorStop(0, 'rgba(255,179,92,.22)'); g.addColorStop(1, 'rgba(255,179,92,0)'); x.fillStyle = g; x.fillRect(0, 0, W, H);
      x.fillStyle = '#F2541B'; x.fillRect(0, 0, 14, H);
      // the mark (ui/mark.svg's two polygons) at 64 px
      var pts = [[50, 4], [74, 34], [67, 58], [80, 54], [61, 96], [39, 96], [20, 54], [33, 58], [26, 34]], inner = [[50, 42], [59, 58], [50, 82], [41, 58]], ox = 72, oy = 56, k = 0.64;
      x.fillStyle = '#F2541B'; x.beginPath(); pts.forEach(function (p, i) { x[i ? 'lineTo' : 'moveTo'](ox + p[0] * k, oy + p[1] * k); }); x.closePath(); x.fill();
      x.fillStyle = '#0C0C0E'; x.beginPath(); inner.forEach(function (p, i) { x[i ? 'lineTo' : 'moveTo'](ox + p[0] * k, oy + p[1] * k); }); x.closePath(); x.fill();
      x.fillStyle = '#F4F1EC'; x.font = '900 28px Unbounded'; x.textBaseline = 'alphabetic'; x.fillText('IGNEUM', 152, 100);
      x.fillStyle = '#9A9A9E'; x.font = '500 18px "IBM Plex Mono"'; x.textAlign = 'right'; x.fillText(whenWords(m.when) + ' · Ember ' + (s.version || ''), W - 56, 100); x.textAlign = 'left';
      x.fillStyle = '#F2541B'; x.font = '500 20px "IBM Plex Mono"'; x.fillText(m.eyebrow.toUpperCase().split('').join(' '), 72, 196);
      x.fillStyle = '#F4F1EC'; x.font = '900 72px Unbounded';
      var t = m.title.replace(/\.$/, ''), parts = t.split(' is yours');
      if (x.measureText(t).width <= W - 144) x.fillText(t, 72, 286);
      else { x.fillText(parts[0], 72, 266); x.fillStyle = '#FFB35C'; x.fillText('is yours', 72, 346); }
      var y = parts.length > 1 && x.measureText(t).width > W - 144 ? 420 : 370;
      x.fillStyle = '#C9C7C2'; x.font = '500 30px "IBM Plex Sans"'; x.fillText(m.cardLine.split(' · ').slice(0, 2).join(' · '), 72, y);
      x.fillStyle = '#FFB35C'; x.fillText(m.ignLine, 72, y + 50);
      x.fillStyle = '#9A9A9E'; x.font = '500 26px "IBM Plex Sans"'; x.fillText(m.rankLine, 72, y + 98);
      x.fillStyle = '#9A9A9E'; x.font = '400 22px "IBM Plex Mono"';
      var ids = ((s.mining || {}).cards || []).filter(function (c) { return c.key === (s.ladder.milestone || {}).card; }).map(function (c) { return (c.ids || [])[0]; }).filter(Boolean);
      x.fillText((ids.length ? 'Miner key ' + ids[0] + ' · ' : '') + 'hash ' + (m.hash ? m.hashShort : 'pending'), 72, 560);
      x.fillStyle = '#F4F1EC'; x.font = '400 20px "IBM Plex Mono"'; x.fillText(m.url ? m.url.replace(/^https:\/\//, '') : 'igneum.network/explorer', 72, 598);
      return cv.toDataURL('image/png');
    });
  }
  function whenWords(t) { if (!(t > 0)) return ''; var d = new Date(t * 1000); return d.getDate() + ' ' + ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][d.getMonth()] + ' ' + d.getFullYear() + ' ' + clock(t); }
  var rungsSig = '', stripSig = '';
  function rungHtml(r) {
    return '<li class="rung ' + esc(r.state) + '" title="' + esc(r.field) + '"><span class="rg-dot" aria-hidden="true"></span><span class="rg-body"><span class="rg-name">' + esc(r.name) + '</span><span class="rg-line">' + esc(r.line) + '</span>' + (r.sub ? '<span class="rg-sub">' + esc(r.sub) + '</span>' : '') + (r.progress >= 0 && r.state !== 'done' ? '<i class="bar"><b style="width:' + Math.round(r.progress * 100) + '%"></b></i>' : '') + '</span></li>';
  }
  // the Overview's first-month surfaces: the count-up or the block card, then the ladder strip
  function renderFirstMonth(s) {
    var m = s.mining, now = s.now, ld = s.ladder || {};
    if (m.state === 'mining' && m.hash_total > 0) { if (!miningSince) miningSince = now; } else miningSince = 0;
    var c = View.chainNums(chain), p = View.poisson(m.hash_total, c.hps, miningSince ? now - miningSince : 0, c.bps, (m.cards || []).filter(function (x) { return x.state === 'mining'; }).length > 1);
    var waiting = View.firstWait(s);
    setHidden('first-wait', !waiting);
    if (waiting) {
      if (p) { setText('first-wait-line', p.line); setText('first-wait-sub', p.pool || 'The chance counts up from the first job. The line gives way to the block card.'); setText('first-wait-pct', p.chanceText); $('first-wait-arc').style.strokeDashoffset = (97.4 * (1 - p.chance)).toFixed(1); }
      else { setText('first-wait-line', 'Waiting for your first block.'); setText('first-wait-sub', m.hash_total > 0 ? (c.error || 'The expected gap needs the network rate from igneum.network/api/stats.') : 'The cards are starting.'); setText('first-wait-pct', ''); $('first-wait-arc').style.strokeDashoffset = '97.4'; }
      $('first-wait').title = View.FIELDS.share + ' · ' + View.FIELDS.hashrate + ' · gap = 1 / (share × blocks per second); chance = 1 − e^(−elapsed / gap)';
    }
    var bc = View.blockCard(ld.milestone, s, chain, now), card = $('block-card');
    var show = !!bc && bc.key !== cardDismissed;
    setHidden('block-card', !show);
    if (show) {
      if (!hiddenNow()) reportCardSeen(bc);
      card.dataset.key = bc.key; card.dataset.url = bc.url;
      setText('bc-eyebrow', bc.eyebrow); setText('bc-when', bc.when > 0 ? clock(bc.when) : ''); setText('bc-title', bc.title);
      $('bc-card').innerHTML = View.markHtml(bc.card, 'mini') + '<span>' + esc(bc.cardLine) + '</span>'; $('bc-card').title = bc.fields.card;
      setText('bc-ign', bc.ignLine); $('bc-ign').title = bc.fields.ign;
      setText('bc-rank', bc.rankLine); $('bc-rank').title = bc.fields.rank;
      setText('bc-hash', 'hash ' + bc.hashShort); $('bc-hash').title = bc.hash;
      $('bc-open').disabled = !bc.url; $('bc-copy').disabled = !bc.url;
    }
  }
  // the rungs behind the Standing row's Details (A17: no intro; the source on hover)
  function renderStanding(s) {
    var rungs = View.ladderRungs(s, chain, s.now), c = View.chainNums(chain);
    $('standing-details').title = c.source === 'observer' ? 'getFinalityWeights relayed by igneum.network/api/live until this node answers igneum_getFinalityWeights; keys cut to 64' : c.source === 'node' ? 'igneum_getFinalityWeights on this machine’s node' : (c.error || '');
    var sig = JSON.stringify(rungs);
    if (sig !== rungsSig) { rungsSig = sig; $('rungs').innerHTML = rungs.map(rungHtml).join(''); }
  }
  // ---------- render ----------
  // the vendor mark and the name block come from View.rowHead (one mark per row, never two)
  function vramText(cd) { if (!cd.vram_mb) return ''; var gb = cd.vram_mb / 1024; return (gb >= 10 ? Math.round(gb) : gb.toFixed(gb < 4 ? 1 : 0)) + ' GB' + (cd.kind === 'apple' ? ' unified' : ''); }
  // driver-check: the first-run row's driver line (the same words as the dashboard strip, the same buttons)
  function setupDriverHtml(cd) {
    var dw = View.driverWords(cd, state || {});
    if (dw.kind === 'none' || dw.kind === 'fine') return '';
    return '<div class="driver ' + dw.kind + '">' + (dw.kind === 'running' ? '<i class="bar"><b style="width:' + Math.round((dw.progress || 0) * 100) + '%"></b></i>' : '') + '<span class="t">' + esc(dw.text) + '</span>' +
      (dw.button === 'install' ? ' <button class="btn tiny primary" data-driver-install="' + esc(cd.driver_offer.vendor) + '">Install</button>' : dw.button === 'restart' ? ' <button class="btn tiny primary" data-driver-restart="1">Restart now</button>' : '') +
      (dw.sub ? '<div class="n">' + esc(dw.sub) + '</div>' : '') + '</div>';
  }
  // ---------- the first-run GPU rows: one per card, with a switch ----------
  function renderCardRows(container, cards) {
    container.querySelectorAll('.gpu-row').forEach(function (n) { n.remove(); });
    shownCards(cards).forEach(function (cd) {
      var d = document.createElement('div');
      var removed = cd.removed_at > 0, unusable = !!cd.problem;
      d.className = 'gpu-row' + (cd.enabled && !removed && !unusable ? '' : ' off') + (removed ? ' removed' : '') + (unusable ? ' unusable' : '');
      d.dataset.key = cd.key;
      var meta = (vramText(cd) ? '<span>' + esc(vramText(cd)) + '</span>' : '') + (cd.detail && cd.kind !== 'apple' ? '<span>' + esc(cd.detail) + '</span>' : '') + (cd.detail && cd.kind === 'apple' ? '<span>' + esc(cd.detail.split(',')[0]) + '</span>' : '');
      d.innerHTML = View.markHtml(cd) +
        '<div class="info">' + View.nameHtml(cd, unusable ? ': ' + esc(cd.message || 'not usable (' + cd.problem + ')') : removed ? ': removed' : '') + '<div class="meta">' + meta + '</div>' +
        (cd.reason ? '<div class="reason">' + esc(cd.reason) + '</div>' : '') + (cd.message && !unusable && !removed ? '<div class="msg">' + esc(cd.message) + '</div>' : '') + setupDriverHtml(cd) + '</div>' +
        '<label class="switch" title="' + (unusable ? 'no worker can drive this card' : removed ? 'the card was removed' : cd.enabled ? 'mining on this card' : 'not mining on this card') + '"><input type="checkbox" aria-label="Mine on ' + esc(cd.name) + '"' + (cd.enabled && !removed && !unusable ? ' checked' : '') + (cd.kind === 'unknown' || unusable || removed ? ' disabled' : '') + '><span class="track"></span></label>';
      container.appendChild(d);
    });
  }
  // what a container's rows say now, for api/cards: a row without a stepper or a slider keeps the engine's value
  // the choices the engine takes (api/cards): every present card, the switch as Mine shows it, the cap and the miners count as
  // Tune shows them, the state's own values where a page has not rendered the control (app-ia-26: two pages, one list)
  function readCardRows() {
    var out = [];
    shownCards(state ? state.mining.cards : []).forEach(function (cd) {
      if (!View.present(cd)) return;
      var mine = document.querySelector('#d-cards .gpu-line[data-key="' + cssEsc(cd.key) + '"]'), tune = document.querySelector('#t-cards .gpu-line[data-key="' + cssEsc(cd.key) + '"]');
      var sw = mine ? mine.querySelector('.gl-main .switch input') : null, idsInput = tune ? tune.querySelector('.ids input') : null, pr = tune ? tune.querySelector('input[type=range]') : null;
      var n = idsInput ? parseInt(idsInput.value, 10) : (cd.identities || 1); if (!(n >= 1)) n = 1; if (n > 64) n = 64;
      var item = { key: cd.key, enabled: sw ? sw.checked : !!cd.enabled, identities: n };
      if (pr) item.power_pct = parseInt(pr.value, 10); else if (cd.vendor === 'nvidia' && cd.power_default_w > 0) item.power_pct = cd.power_pct || 80;
      out.push(item);
    });
    return out;
  }
  function cssEsc(k) { return String(k).replace(/["\\]/g, '\\$&'); }
  document.addEventListener('change', function (e) {
    if (e.target.matches('.gpu-row .switch input')) { var r = e.target.closest('.gpu-row'); r.classList.toggle('off', !e.target.checked); }
  });
  var cardsRendered = '';
  function renderCards(s) {
    var list = $('cards-list'), det = $('cards-detecting');
    if (s.detecting) { det.hidden = false; $('detect-text').textContent = 'asking the graphics cards to report in'; $('btn-cards-next').disabled = true; $('cards-sub').textContent = 'Asking the graphics cards to report in.'; cardsRendered = ''; return; }
    det.hidden = true;
    var cards = shownCards(s.mining.cards);
    var sig = cards.map(function (c) { return c.key + ':' + (c.problem || '') + ':' + (c.removed_at > 0 ? 'r' : '') + ':' + (c.enabled ? 'on' : 'off'); }).join('|');
    if (sig !== cardsRendered) { cardsRendered = sig; renderCardRows(list, cards); }
    if (!cards.length) {
      $('cards-sub').textContent = 'No graphics card this app can use was found.';
      $('cards-note').textContent = (s.detect_message || '') + ' The node can still run on its own; mining needs a supported card.';
      $('cards-note').hidden = false; $('cards-help').hidden = true; $('btn-cards-retry').hidden = false; $('btn-cards-next').disabled = false; $('btn-cards-next').textContent = 'Continue without mining';
      return;
    }
    $('cards-sub').textContent = cards.length === 1 ? 'One card found. It mines when it is on.' : cards.length + ' cards found. Switch on the ones you want to mine with.';
    $('cards-note').hidden = !s.detect_message; $('cards-note').textContent = s.detect_message || '';
    $('cards-help').hidden = !cards.some(function (c) { return c.kind === 'integrated'; });
    var nv = cards.filter(function (c) { return c.vendor === 'nvidia' && c.power_default_w > 0; }).length;
    $('cards-power').hidden = !nv;
    $('btn-cards-retry').hidden = true; $('btn-cards-next').disabled = false; $('btn-cards-next').textContent = 'Continue';
  }

  // sync ETA from the block rate over the last minute of readings: "about 2 h 10 min left"
  var syncSamples = [];
  function syncEta(n, now) {
    if (n.state !== 'syncing' || !(n.headers > n.blocks)) { syncSamples = []; return ''; }
    if (!syncSamples.length || now - syncSamples[syncSamples.length - 1].t >= 1) syncSamples.push({ t: now, b: n.blocks });
    while (syncSamples.length > 2 && now - syncSamples[0].t > 90) syncSamples.shift();
    var a = syncSamples[0], b = syncSamples[syncSamples.length - 1];
    if (b.t - a.t < 5 || b.b <= a.b) return syncSamples.length > 15 ? 'no progress in the last minute' : 'measuring the sync rate';
    var rate = (b.b - a.b) / (b.t - a.t), left = (n.headers - n.blocks) / rate;
    var word = left < 60 ? 'under a minute' : left < 3600 ? 'about ' + Math.round(left / 60) + ' min' : left < 86400 ? 'about ' + Math.floor(left / 3600) + ' h ' + Math.round(left % 3600 / 60) + ' min' : 'over a day';
    return word + ' left at ' + Math.round(rate) + ' blocks/s';
  }

  // ---------- Mine ----------
  var mineRowsSig = '', lastEventsKey = '';
  function numCell(cls, v, unit, k, tone, title, mark) { return '<div class="num ' + cls + '"' + (title ? ' title="' + esc(title) + '"' : '') + '><span class="v' + (tone ? ' ' + tone : '') + '">' + (mark || '') + esc(v) + (unit ? '<span class="unit">' + unit + '</span>' : '') + '</span>' + (k ? '<span class="k">' + esc(k) + '</span>' : '') + '</div>'; }
  // app-ia-26 (4.1 item 5): one row per card on Mine: mark, name, the kind tag, the state word (a fault's reason on the row),
  // then MH/s, W, °C, then the switch. No chevron, no buttons, no money, no IGN a day, no arrow; tuning lives on Tune.
  function gpuLineHtml(cd, s, r) {
    var cells = View.cells(cd, 0), word = View.rowWord(r, s.mining), nums = '';
    if (r.hash) nums += numCell('hash', r.hash, r.hashUnit || 'MH/s', '', '', '', glyph('rate'));
    if (cells.power) nums += numCell('power', cells.power.replace(' W', ''), 'W', '', '');
    if (r.temp) nums += numCell('temp', r.temp.replace(' °C', ''), '°C', '', r.tempTone);
    var sw = '<label class="switch" title="' + (r.on ? 'mining on this card' : 'not mining on this card') + '"><input type="checkbox" aria-label="Mine on ' + esc(r.name) + '"' + (r.on ? ' checked' : '') + (r.canToggle ? '' : ' disabled') + '><span class="track"></span></label>';
    var subs = [], rw = View.cardsRowWords(cd, s, s.now);
    if (r.sub && r.sub !== word) subs.push('<div class="msg' + (r.tone === 'bad' ? '' : ' dim') + '">' + esc(r.sub) + '</div>');
    if (cells.noReading && r.on) subs.push('<div class="msg dim">' + esc(cells.noReading) + '</div>');
    if (rw.tune && r.on && r.word === 'mining') subs.push('<div class="msg dim tune-state">' + esc(rw.tune) + '</div>');
    if (rw.proving) subs.push('<div class="msg dim">' + esc(rw.proving) + '</div>');
    var lanes = r.on ? View.laneRows(cd) : [];
    if (lanes.length) subs.push('<div class="lanes">' + lanes.map(function (l) { return '<div class="lane' + (l.current ? ' now' : '') + '"><span class="lw">' + esc(l.word) + (l.current ? ' · mining now' : '') + '</span><span class="lv mono">' + esc(l.rate) + '</span><span class="lv mono">' + esc(l.shares) + '</span><span class="lv mono">' + esc(l.time) + '</span></div>'; }).join('') + '</div>');
    return '<div class="gpu-line' + (r.on ? ' on' : ' off') + (r.removed ? ' removed' : '') + (r.unusable ? ' unusable' : '') + '" data-key="' + esc(r.key) + '">' +
      '<div class="gl-main">' + View.markHtml(cd) +
      '<div class="who">' + View.nameHtml(cd) +
      '<span class="st ' + r.tone + '"><i></i>' + esc(word) + (r.meta ? ' · ' + esc(r.meta) : '') + '</span>' + subs.join('') + '</div>' +
      '<div class="nums">' + nums + '</div>' +
      '<div class="acts">' + sw + '</div></div></div>';
  }
  // v0.2.1: the Cards page, every card row (the founder's call of 8 October 2026 reversing the audit's four pages on this point)
  function renderCardsPage(s) {
    var m = s.mining, shown = shownCards(m.cards), presentCards = shown.filter(View.present);
    // the rows: every card, on or off; rebuilt only when their words change and never under a hand on a slider
    var rows = shown.map(View.cardRow), now = Date.now();
    rows.forEach(function (r) { var pd = pending[r.key]; if (pd) { if (pd.on === r.on || now - pd.at > 8000) delete pending[r.key]; else { r.on = pd.on; r.word = pd.on ? 'starting' : 'stopping'; r.tone = ''; } } });
    var on = rows.filter(function (r) { return r.on; }).length;
    setText('d-cards-eyebrow', s.detecting ? 'detecting' : !shown.length ? 'none found' : on + ' of ' + presentCards.length + ' on');
    var tunables = presentCards.filter(function (c) { return View.tunable(c) && c.enabled; });
    setHidden('btn-tune-all', !(tunables.length > 0) || !!(s.settings && s.settings.tuning_off) || !!View.tuningCard(presentCards));
    var box = $('d-cards'), busy = box.querySelector('input[type=range]:active') || (box.contains(document.activeElement) && document.activeElement.tagName === 'INPUT');
    var sig = JSON.stringify([rows, shown.map(function (c) { return [View.cells(c, 0), c.message, c.reason]; })]);
    if (sig !== mineRowsSig && !busy) {
      mineRowsSig = sig;
      box.innerHTML = rows.length ? shown.map(function (cd, i) { return gpuLineHtml(cd, s, rows[i]); }).join('') : '<div class="empty' + (s.detecting ? '' : ' err') + '">' + (s.detecting ? 'Asking the graphics cards to report in.' : 'No graphics card this app can use was found. ') + '</div>';
    }
    var note = !shown.length && s.detect_message ? s.detect_message : '';
    $('d-cards-note').hidden = !note; setText('d-cards-note', note);
  }
  function renderMine(s) {
    var m = s.mining, n = s.node, p = s.program;
    if (toggleClick && View.clickSettled(toggleClick, m, Date.now())) toggleClick = null;
    var t = View.toggle(m, n, s, toggleClick && { act: toggleClick.act, at: toggleClick.at, now: Date.now() }), tb = $('btn-toggle');
    tb.className = 'toggle-big' + (t.cls ? ' ' + t.cls : ''); tb.disabled = t.disabled;
    setText('btn-toggle-text', t.label); tb.title = t.sub;
    var sp = $('btn-toggle-spin'); if (sp) sp.hidden = !t.spin;
    tb.setAttribute('aria-busy', t.spin ? 'true' : 'false');
    var shown = shownCards(m.cards), presentCards = shown.filter(View.present);
    var mining = presentCards.filter(function (c) { return c.state === 'mining'; }).length;
    // the stat strip: five tiles from View.stripTiles, a placeholder dash wherever a figure is unknown
    // the chain's credit for the earnings tile (the founder's cell): the balance, its block, the last paid block and its time
    var sdch = (sceneDoc && sceneDoc.chain) || {}, sdblocks = (sceneDoc && sceneDoc.blocks) || [], sdpaid = sdblocks.filter(function (b) { return b.state === 'paid' && b.my_shares > 0; }), sdlast = sdpaid.length ? sdpaid[sdpaid.length - 1] : null;
    var credit = sceneDoc && sdch.balance_wei != null && (sdch.balance_head || 0) > 0 ? { ign: wei(sdch.balance_wei), head: sdch.balance_head || 0, lastPaid: sdlast ? { height: sdlast.height, at_unix: sdlast.at_unix || sdlast.sealed_unix || 0 } : null, behind: ((sceneDoc && sceneDoc.ledger) || {}).behind || 0 } : null;
    var tiles = View.stripTiles(s, chain, s.now, credit);
    function tile(id, t, subId) { var el = $(id); setText(id, t.text); el.classList.toggle('ph', !!t.ph); el.classList.toggle('is-warm', t.tone === 'warm'); el.classList.toggle('is-hot', t.tone === 'hot'); el.classList.toggle('good', t.tone === 'good'); el.classList.toggle('bad', t.tone === 'bad'); if (subId) setText(subId, t.sub || ''); if (t.title !== undefined) el.parentNode.title = t.title || ''; var u = $(id + '-unit'); if (u && t.unit) setText(id + '-unit', t.unit); else if (u && u.dataset.k) setText(id + '-unit', u.dataset.k); }
    var dead = View.deadTiles(tiles, presentCards);
    setHidden('t-power-cell', dead.hidePower); setHidden('t-temp-cell', dead.hideTemp); $('totals').classList.toggle('three', dead.hidePower && dead.hideTemp);
    setText('totals-foot', dead.note ? ('development-network tokens, no monetary value · ' + dead.note) : 'development-network tokens, no monetary value');
    tile('t-hash', tiles.hash, 't-hash-sub'); tile('t-power', tiles.power, 't-power-sub'); tile('t-earn', tiles.earn, 't-earn-sub'); tile('t-temp', tiles.temp, 't-temp-sub'); tile('t-chain', tiles.chain, 't-chain-sub');
    // the four tiles (4.1 item 2): MH/s, W, MH/W, IGN in the last 24 hours (the engine's earned window)
    var watts = View.fleetWatts(m, m.cards), effP = watts > 0 && m.hash_total > 0 ? View.effParts(m.hash_total / watts) : null, eff = effP ? effP.v : '';
    // the efficiency figure stays on the hidden tile for the tests and the fold; the strip shows the five tiles alone
    setText('t-eff', eff || '0'); if (effP) setText('t-eff-unit', effP.u);
    // the share lane: the node's accepted shares this run, the refusals by its reason on the sub-line
    setHidden('t-shares-cell', !m.share_lane);
    if (m.share_lane) {
      var sr = m.share_reasons || {}, srk = Object.keys(sr);
      // the design seat's read (11 October 2026): the count changes in place, green from the first accepted share,
      // ash while zero
      setText('t-shares', String(m.shares_accepted || 0));
      setText('t-shares-sub', m.shares_refused ? (m.shares_refused + ' refused: ' + srk.map(function (k) { return k + ' ' + sr[k]; }).join(', ')) : 'none refused');
      $('t-shares').classList.toggle('is-met', (m.shares_accepted || 0) > 0);
      $('t-shares').classList.toggle('is-ash', !((m.shares_accepted || 0) > 0));
    }
    var ew = View.earningsLines(s, chain, 0, s.now).h24;
    // on the share lane the IGN tile is the chain's credit to the address (the design seat's read, 11 October 2026):
    // the figure with the block it was seen at once the balance rises; "credit pending on the chain" in the pending
    // grade while shares are accepted and nothing has risen, never a figure; the 24-hour reading off the lane
    var credit = m.share_lane && m.credit_wei ? m.credit_wei : '';
    if (credit) { ew = { ign: Number(View.ignText(credit, 4)), blocks: 1, shards: 0, field: 'this app: the payout address’s balance rise since this session’s first reading (eth_getBalance), credited by the chain with a carrying block' }; }
    else if (m.share_lane && m.credit_pending) { ew = { ign: 0, blocks: 0, shards: 0, field: 'this app: shares accepted, the chain’s credit not yet on the address (it lands with a carrying block)' }; }
    if (sceneDoc) { renderIgnTile(s, sceneDoc); } else { setText('t-ign', ew.ign === null ? 'reading' : ew.ign > 0 ? View.fmtIgn(ew.ign).replace(' IGN', '') : '0'); setText('t-ign-sub', (credit ? ('credited at block ' + (m.credit_block || 0)) : (m.share_lane && m.credit_pending) ? 'credit pending on the chain' : ew.ign === null ? 'last 24 hours' : ew.blocks || ew.shards ? 'last 24 hours' : 'no block yet') + ' · ' + View.NO_VALUE_LABEL); } $('t-ign').title = ew.field;
    // the design system's grade (the old base, step 4): a figure read from the chain's own blocks is met (green); one
    // without a block behind it is an estimate (molten); "reading" is neither
    $('t-ign').classList.toggle('is-met', ew.ign !== null && ew.ign > 0 && !!(ew.blocks || ew.shards));
    $('t-ign').classList.toggle('is-estimate', ew.ign !== null && !(ew.blocks || ew.shards));
    renderNode(s);
    if (page === 'mine') renderFirstMonth(s);
    // activity: the newest five events in a person's words (A15: no counts line)
    var key = s.events.length ? s.events[0].t + ':' + s.events.length : '';
    if (key !== lastEventsKey) {
      lastEventsKey = key;
      $('d-events').innerHTML = s.events.length ? s.events.slice(0, 5).map(function (e) { var pe = View.plainEvent(e.text); return '<div' + (pe.tech ? ' title="' + esc(pe.tech) + '"' : '') + '><span><i class="k ' + esc(e.kind) + '"></i>' + esc(pe.text) + '</span><span class="t">' + clock(e.t) + '</span></div>'; }).join('') : '<div class="empty">No events yet. The node and the miner report here as they start.</div>';
    }
  }
  // the Tuning card on Mine and the Tuning rows on Settings share the goal, the schedule and the Power control switch
  // ---------- Tune (app-ia-26, 4.2): the strip, one card per GPU, the rule once at the foot ----------
  var tuneCardsSig = '';
  function renderTune(s) {
    var st = s.settings || {}, cards = shownCards(s.mining.cards), presentCards = cards.filter(View.present);
    // the strip (the tiers, Tune all, the sentence) stays out of sight only where nothing tunes: Apple silicon under the worker layer
    $('tune-card').hidden = !!s.worker_layer && presentCards.length > 0 && presentCards.every(function (c) { return c.vendor === 'apple'; });
    var ft = View.fleetTiers(s.mining.cards, s, 0), ftHtml = View.tierButtonsHtml(ft.tiers);
    if ($('tier-seg').innerHTML !== ftHtml) $('tier-seg').innerHTML = ftHtml;
    var tunables = presentCards.filter(function (c) { return View.tunable(c) && c.enabled; });
    setHidden('btn-tune-all', !(tunables.length > 0) || !!st.tuning_off || !!View.tuningCard(presentCards));
    setText('tune-schedule', View.tierSentence(s.mining.cards, s, s.now));
    setText('tier-rule', View.KNEE_RULE);
    if (document.activeElement !== $('s-sweep')) $('s-sweep').checked = !!st.sweep;
    setHidden('s-climb-row', st.tune_climb === undefined); if (document.activeElement !== $('s-climb')) $('s-climb').checked = !!st.tune_climb;
    var hl = View.heatLine(s.heat);
    setHidden('heat-line', !hl.text); setText('heat-line', hl.text); $('heat-line').className = 'line-text heat-line' + (hl.tone ? ' ' + hl.tone : '');
    var pn = View.powerNote(s); setHidden('power-note', !pn); setText('power-note-text', pn);
    // the cards: rebuilt only when their words change and never under a hand on a slider or in the miners box
    var box = $('t-cards'), busy = box.querySelector('input[type=range]:active') || (box.contains(document.activeElement) && document.activeElement.tagName === 'INPUT');
    var html = cards.length ? cards.map(function (cd) { return View.tuneCardHtml(cd, s, s.now); }).join('') : '<div class="empty' + (s.detecting ? '' : ' err') + '">' + (s.detecting ? 'Asking the graphics cards to report in.' : 'No graphics card found.') + '</div>';
    if (html !== tuneCardsSig && !busy) {
      tuneCardsSig = html; box.innerHTML = html;
      box.querySelectorAll('.gpu-line').forEach(function (row) { var det = row.querySelector('details'); if (det && openCards[row.dataset.key]) det.open = true; });
    }
  }

  // ---------- the node line and its details ----------
  var switchesSig = '';
  function renderNode(s) {
    var n = s.node, f = s.finality, p = s.program, eta = syncEta(n, s.now);
    var nl = View.nodeLine(n, s.clock, eta, s.source, s.mining), fw = View.finalityWords(f);
    setText('node-line-text', nl.text); $('node-go').title = fw.paused && nl.tone !== 'bad' ? fw.note : nl.sub;
    $('node-dot').className = 'dot' + (nl.tone === 'ok' ? ' live' : nl.tone === 'bad' ? ' bad' : '');
    $('node-go').classList.toggle('bad', nl.tone === 'bad');
    setText('s-node-line', nl.text + (n.blocks > 0 ? ' · ' + withCommas(n.blocks) + ' blocks' : '')); setText('s-node-sub', fw.paused && nl.tone !== 'bad' ? fw.note : nl.sub);
    setText('n-state-v', nl.text.replace(/^Node /, '')); setText('n-state-m', nl.sub + (n.override_restart_wait ? ' ' + n.override_restart_wait + '.' : ''));
    var ns = View.nodeSource(n); setText('n-source', ns.text); setText('n-source-m', ns.sub); setHidden('n-source', !ns.text);
    setText('n-blocks', withCommas(n.blocks)); setText('n-blocks-sub', View.heightLine(n));
    setText('n-peers', String(n.peers)); setText('n-peers-sub', View.peersLine(n));
    setText('n-version', n.version ? n.version.replace(/^igneumd\s*/, '') : '--');
    setText('d-node-net', s.chain || s.network || '');
    setText('n-headers', withCommas(n.headers)); setText('n-daa', withCommas(n.daa)); setText('n-diff', compact(n.difficulty)); setText('n-tips', String(n.tips)); setText('n-blue', withCommas(n.blue));
    if (n.daa > 0 && p && p.eta_s >= 0) { setText('d-eta', hms(p.eta_s)); setText('d-eta-sub', 'until the next hourly program' + (p.epoch_index ? ' (epoch ' + p.epoch_index + ')' : '') + (p.message ? ' · ' + p.message : '')); }
    else { setText('d-eta', '--:--'); setText('d-eta-sub', n.state === 'syncing' ? 'known after the node is synced' : 'known once the node is running'); }
    setText('n-digest', n.consensus_digest || (n.state === 'stopped' || n.state === 'failed' ? 'the node is not running' : 'not read yet'));
    var sw = View.nextSwitch(n.consensus_switches, n.daa);
    setText('n-switch', sw ? sw.name + ' at ' + withCommas(sw.daa) : (n.consensus_switch_daa > 0 && n.consensus_switch_daa > n.daa ? 'DAA ' + withCommas(n.consensus_switch_daa) : (n.consensus_switches && n.consensus_switches.length ? 'none ahead' : 'none planned')));
    setText('n-switch-help', sw ? View.switchLine(sw, n.daa) : (n.consensus_switch_daa > 0 && n.consensus_switch_daa > n.daa ? 'A rule change is announced at block ' + withCommas(n.consensus_switch_daa) + ' (' + withCommas(n.consensus_switch_daa - n.daa) + ' blocks away).' : View.switchLine(null, n.daa)));
    var sig = JSON.stringify([n.consensus_switches, sw && sw.key]);
    if (sig !== switchesSig) {
      switchesSig = sig;
      $('n-switches').innerHTML = (n.consensus_switches || []).map(function (x) { return '<span class="' + (sw && x.key === sw.key ? 'next' : x.daa <= n.daa ? 'past' : '') + '"><span>' + esc(x.name) + (x.daa <= n.daa ? ' (applied)' : '') + '</span><span>' + withCommas(x.daa) + '</span></span>'; }).join('');
    }
    setText('f-lock', (f.last_lock > 0 ? '#' + withCommas(f.last_lock) : 'none yet') + ' · ' + fw.age + (fw.recovery ? ' · recovery lock' : ''));
    setText('f-note', fw.paused ? fw.note : f.last_lock > 0 ? 'a point the miners agreed can never be undone. It locks at two thirds of the last 30 days of mining' : (f.message || 'checkpoints lock once this machine and the others vote'));
    $('f-lock').classList.toggle('warn', !!fw.paused);
    setText('f-votes', withCommas(f.votes));
  }

  // ---------- Earnings ----------
  // the Live Work Ledger's first screen: the miner view fetched by address every minute while the page shows
  // (CORS open on build.igneum.network); the words from View.ledgerWords, every figure with its source
  var ledgerView = null, ledgerAt = 0, ledgerAddr = '';
  function fetchLedger(s) {
    var a = ((s.address || {}).display || '').toLowerCase();
    if (!a || !/^0x[0-9a-f]{40}$/.test(a)) return;
    var now = Date.now();
    if (a === ledgerAddr && now - ledgerAt < 60000) return;
    ledgerAt = now; ledgerAddr = a;
    // 0.2.3: the engine reads the ledger and serves it as api/ledger (the window fetches no other origin)
    fetch('api/ledger', { cache: 'no-store' }).then(function (r) { if (!r.ok) throw new Error('the ledger answered ' + r.status + (r.status === 404 ? ' (no shares from this address in the window yet)' : '')); return r.json(); })
      .then(function (d) { ledgerView = d; renderLedger(state || s); })
      .catch(function (e) { ledgerView = { error: String(e && e.message || e) }; renderLedger(state || s); });
  }
  function renderLedger(s) {
    fetchLedger(s);
    var w = View.ledgerWords(ledgerView, s);
    setText('l-address', w.address || 'no address yet'); setText('l-asof', w.asof || (w.line ? w.line : '')); setText('l-chain-head', 'This address on ' + (w.chain || 'the chain'));
    var host = $('l-rows'); if (!host) return;
    var html = w.rows.map(function (r) {
      var det = r.details && r.details.length ? '<details class="row-details"><summary>details</summary><ul>' + r.details.map(function (x) { return '<li>' + esc(x) + '</li>'; }).join('') + '</ul></details>' : '';
      return '<div class="earn-row"><span class="k">' + esc(r.k) + '</span><span class="v fig">' + markedIgn(r.v) + '</span><span class="s">' + esc(r.s) + det + '</span></div>';
    }).join('');
    if (host.innerHTML !== html) host.innerHTML = html;
  }
  function renderEarnings(s) {
    renderLedger(s);
    var el = View.earningsLines(s, chain, priceNow(), s.now);
    setText('e-head', el.headline.text); setText('e-head-sub', el.headline.sub); $('e-head').title = el.headline.field;
    // the design system's grade (the old base, step 4): the headline is met (green) when the chain's own blocks stand
    // behind it, an estimate (molten) when it is projected, ember while it reads
    var ewh = View.earnedWindow ? View.earnedWindow(s) : null;
    $('e-head').classList.toggle('is-met', !!(ewh && ewh.ign !== null && ewh.ign > 0 && (ewh.blocks || ewh.shards)));
    $('e-head').classList.toggle('is-estimate', !!(ewh && ewh.ign !== null && !(ewh.blocks || ewh.shards)));
    ['d7', 'all'].forEach(function (k) { setText('e-' + k, el[k].text); setText('e-' + k + '-sub', el[k].sub); $('e-' + k).title = el[k].field; });
    // the chain's balance first (main's order of 11:1x UK, 12 October 2026): "All time" is what the payout address
    // holds on the chain, read through the public RPC; the ledger's periods stand under it
    var ch = (sceneDoc && sceneDoc.chain) || {}, lg = (sceneDoc && sceneDoc.ledger) || {}, tt = (sceneDoc && sceneDoc.totals) || {};
    var feedHead = (sceneDoc && sceneDoc.head && sceneDoc.head.height) || 0;
    if (ch.balance_wei != null && (ch.balance_head || 0) === 0 && feedHead > 0) {
      // the node behind the read is at block 0 (a fresh install syncing): a balance read there is not the chain's
      setText('e-head', 'reading'); setText('e-head-sub', 'the node is syncing (block 0 of ' + withCommas(feedHead) + '); the balance follows the sync');
      $('e-head').classList.remove('is-met'); $('e-head').classList.add('is-estimate');
    } else if (ch.balance_wei != null) {
      var onChain = wei(ch.balance_wei);
      setText('e-head', onChain > 0 ? View.fmtIgn(onChain) : '0 IGN');
      setText('e-head-sub', 'what the payout address holds on ' + View.chainWord(ch.chain_id, ((sceneDoc.feed || {}).chain_id === ch.chain_id) ? (sceneDoc.feed || {}).name : '') + ', read at block ' + withCommas(ch.balance_head || 0) + ' · ' + View.NO_VALUE_LABEL);
      setText('e-head-eyebrow', 'on ' + View.chainWord(ch.chain_id));
      $('e-head').title = 'eth_getBalance of the payout address through ' + (ch.url || 'the chain’s RPC');
      $('e-head').classList.add('is-met'); $('e-head').classList.remove('is-estimate');
    } else if (ch.error) { setText('e-head', 'reading'); setText('e-head-sub', ch.error); }
    if (sceneDoc && lg.other_chain) {
      var noLedger = 'no ledger yet for chain ' + (lg.mined_chain_id || 0) + ': the ledger’s miner view names ' + View.chainWord(lg.chain_id, lg.network) + ', not the chain this machine mines';
      setText('e-d7', 'no ledger yet'); setText('e-d7-sub', noLedger); $('e-d7').title = lg.error || noLedger;
      setText('e-all', 'no ledger yet'); setText('e-all-sub', noLedger); $('e-all').title = lg.error || noLedger;
      $('e-all').classList.remove('is-met');
    } else if (sceneDoc && lg.head > 0) {
      var p24 = wei(tt.paid_24h_wei), pall = wei(tt.paid_wei), lag = (lg.other_chain ? ' · ' + View.chainWord(lg.chain_id, lg.network) + ', not the chain this machine mines' : ' · ' + View.chainWord(lg.chain_id, lg.network)) + (lg.behind > 2 ? ' · ledger ' + withCommas(lg.behind) + ' blocks behind' : '');
      setText('e-d7', p24 > 0 ? View.fmtIgn(p24) : '0 IGN'); setText('e-d7-sub', 'paid in the last day’s blocks, from the ledger' + lag); $('e-d7').title = 'the ledger miner view: paid_wei over the last 4,320 blocks';
      setText('e-all', pall > 0 ? View.fmtIgn(pall) : '0 IGN'); setText('e-all-sub', (!(pall > 0) && (ch.balance_wei != null && wei(ch.balance_wei) > 0)) ? ('the ledger has not carried this address’s pays yet' + (lg.behind > 0 ? ' (' + withCommas(lg.behind) + ' blocks behind)' : '') + '; the figure above is the chain’s balance') : (withCommas(tt.paid_shares || 0) + ' shares paid in ' + withCommas(tt.paid_blocks || 0) + ' blocks' + lag)); $('e-all').title = 'the ledger miner view: paid_wei over its block rows';
      $('e-all').classList.add('is-met');
    }
    setText('e-cost', el.cost.text); setText('e-cost-sub', el.cost.sub); $('e-cost').title = el.cost.field;
    setText('e-pays', el.payouts.text); setText('e-pays-sub', el.payouts.sub); setText('e-pays-full', el.payouts.full || 'not set');
    setText('e-standing', el.standing.text); setText('e-standing-sub', el.standing.sub); $('e-standing').title = el.standing.field;
    var fee = View.devFeeEarningsRow(s); setText('e-fee', fee.text); var feeHtml = (fee.lines || [fee.sub]).map(function (l) { return '<span class="ln">' + esc(l) + '</span>'; }).join(''); if ($('e-fee-sub') && fee.detail) $('e-fee-sub').title = fee.detail.join('\n'); if ($('e-fee-sub').innerHTML !== feeHtml) $('e-fee-sub').innerHTML = feeHtml; $('e-fee').title = (s.dev_fee || {}).line || '';
    var pj = el.projection || el.projectionNote; setHidden('e-projection', !pj); setText('e-projection', pj || ''); $('e-projection').title = el.projection ? el.projectionField : '';
    renderStanding(s);
  }
  // Settings > Wallet: the address, its source, the key (app-ia-26, 4.4 item 1; the card moved from Earnings)
  function renderWallet(s) {
    var a = s.address || {};
    setText('r-address', a.display || 'not set');
    setText('r-source', a.source === 'generated' ? 'made on this machine' : a.source === 'pasted' ? 'your own address' : '');
    setText('r-address-help', a.display ? 'The chain credits this address for this machine’s shares.' : 'No address yet. Finish the setup to choose one.');
    var gen = a.source === 'generated';
    setHidden('key-toggle', !gen); if (!gen) { $('key-details').hidden = true; hideKey(); }
    setText('r-key-help', 'The key for this address was made on this machine and is stored in the app folder. Only your user can read it. Keep a copy somewhere safe.');
    setText('r-key-file', gen && a.wallet_file ? a.wallet_file : '');
  }

  // ---------- Prove ----------
  var tiersSig = '';
  // the Proving section of Settings (app-ia-26: the Prove page folded under Settings; D1 of the audit)
  function renderProvingSection(s) {
    // the worker layer (the old base, step 2): the old chain's proving round is not carried; the section and the
    // shard card stay out of sight while the engine says so (the markup stays for the audit tests' page order)
    setHidden('s-proving-card', !!s.worker_layer);
    if (s.worker_layer) { setHidden('shard-card', true); return; }
    var pv = s.proving || {}, enabled = !!(s.settings && s.settings.prove);
    // miner-ui-5: "your card proved shard N of block M" from the newest paid record (ladder.shards)
    var sh = ((s.ladder || {}).shards || [])[0];
    setHidden('shard-card', !sh);
    if (sh) { setText('sc-when', clock(sh.at)); setText('sc-title', 'Your card proved shard ' + sh.shard + ' of block ' + withCommas(sh.block) + '.'); setText('sc-line', View.fmtIgn(View.ign(sh.wei)) + ' paid for it \u00b7 ' + withCommas(pv.paid || 0) + ' shard' + (pv.paid === 1 ? '' : 's') + ' paid on this machine'); setText('sc-sub', '20 points of every block pay the provers, split by shard. A paid record is a chain fact: anyone can read it.'); $('sc-title').title = View.FIELDS.shards; $('sc-line').title = 'proving.paid_wei, proving.paid'; }
    if (document.activeElement !== $('s-prove')) $('s-prove').checked = enabled;
    var pi = View.proveInsteadWords(pv, s.settings);
    $('pv-instead-row').hidden = !pi.show;
    if (pi.show) { $('pv-instead-help').textContent = pi.help; if (document.activeElement !== $('s-prove-instead')) $('s-prove-instead').checked = pi.on; }
    var w = View.proveWords(pv, enabled, s.node.synced);
    var cards = shownCards(s.mining.cards).filter(View.present), tiers = cards.map(function (cd) { return View.proveModeLine(cd, pv); });
    var sig = tiers.join('|');
    if (sig !== tiersSig) { tiersSig = sig; $('pv-tiers').innerHTML = tiers.map(function (t) { return '<li>' + esc(t) + '</li>'; }).join(''); }
    setText('pv-line', View.proveLine(pv, enabled, s.node.synced));
    $('pv-line').className = 'line-text ' + (w.tone === 'on' || w.tone === 'ok' ? 'ok' : w.tone === 'bad' ? 'bad' : '');
    setText('pv-note', (!enabled && pv.default_note) ? pv.default_note + '.' : w.note);
    var v = View.verifierWords(pv);
    setText('pv-verifier', v.word); $('pv-verifier').className = 'v ' + v.tone;
    setText('pv-verifier-help', pv.verifier_note || 'the verifier: the node checks a proof before it counts');
    $('pv-setup-row').hidden = !((pv.enabled && !pv.available && pv.setup_hint) || v.needsSetup);
    var seg = pv.segments_submitted ? pv.segments_submitted + ' proven whole, ' + (pv.segments_paid || 0) + ' paid' + (weiNonZero(pv.segment_paid_wei) ? ' (' + ignText(pv.segment_paid_wei, 4) + ' IGN)' : '') + (pv.segment_last_s ? ', the last took ' + Math.round(pv.segment_last_s) + ' s' : '') + (pv.segments_held ? ', ' + pv.segments_held + ' waiting for the chain' : '') : 'none yet';
    setText('pv-seg', seg); setText('pv-seg-note', pv.segment_note && enabled ? pv.segment_note : 'whole segments (runs of 8 blocks) this machine proved');
    setText('pv-program', pv.program_id || 'not read yet');
    setText('pv-aggregator', pv.aggregator_id || 'not read yet');
  }

  // ---------- Settings ----------
  var jobsSig = '';
  function renderSettings(s) {
    renderProvingSection(s);
    renderWallet(s);
    renderNode(s); // the Node card's line and facts live here now (app-ia-26, 4.4 item 2)
    var st = s.settings || {}, sw = function (id, on) { if (document.activeElement !== $(id)) $(id).checked = !!on; };
    sw('s-vote', st.vote); sw('s-login', st.start_at_login); sw('s-jobs-allow', s.jobs && s.jobs.allowed); sw('s-trust', st.proof_verify_trust); sw('s-power-control', st.power_control); sw('s-profile', st.profile_public);
    setText('s-profile-text', View.profileWords(!!st.profile_public));
    setText('s-page-line', View.pageLine(s.page || {}));
    setText('s-power-note', st.tuning_off ? ' ' + (st.tuning_note || '') : (st.power_note ? ' ' + st.power_note : ''));
    renderNetworkCard(s);
    fillRegions($('s-region'));
    var rc = regionNow();
    if (document.activeElement !== $('s-region') && $('s-region').value !== rc) { $('s-region').value = rc; }
    fillCurrencies($('s-currency'), rc); if (document.activeElement !== $('s-currency')) $('s-currency').value = currencyNowCode();
    var askS = View.currencyAsk(View.currencyChoice({ region: rc, currency: currencyNowCode() }, lang)); setText('s-currency-ask', askS); setHidden('s-currency-ask', !askS);
    if (document.activeElement !== $('s-price')) { var p = priceNow() / View.priceFactor(View.currencyNow().code); $('s-price').value = p > 0 ? String(p) : ''; }
    if (document.activeElement !== $('s-region')) regionWords($('s-region').value, null, $('s-price-unit'), $('s-price-note'), $('s-region-restricted'), false);
    var h = s.heat || {};
    sw('s-heat', st.heat_on);
    setText('s-heat-line', h.note || '');
    $('s-heat-line').classList.toggle('ok', h.phase === 'heating'); $('s-heat-line').classList.toggle('rest', h.phase === 'resting');
    if (document.activeElement !== $('s-heat-set')) $('s-heat-set').value = st.heat_set_c > 0 ? String(st.heat_set_c) : '';
    if (document.activeElement !== $('s-heat-schedule')) $('s-heat-schedule').value = st.heat_schedule || '';
    setText('s-heat-room-note', (st.heat_room_at > 0 ? 'Your reading ' + st.heat_room_c + ' °C, ' + View.ago(s.now - st.heat_room_at) + (s.now - st.heat_room_at > 7200 ? ' (older than two hours, so the card reads the room now)' : '') + '. ' : '') + 'A reading you type is the room for two hours. Without one the card\'s own sensor reads the room after 3 minutes of rest, within about 3 degrees' + (h.offset_learned ? ' (idle offset learned: ' + h.offset_c.toFixed(1) + ' degrees)' : '') + '. Typing a reading while the cards rest teaches the app the card\'s idle offset.');
    if (document.activeElement !== $('s-name')) $('s-name').value = s.display_name || '';
    setText('s-mid', 'machine id ' + (s.machine_id || '').slice(0, 8));
    setText('s-node-dir', 'chain data: ' + s.node_dir);
    setText('s-log-dir', 'log files: ' + s.log_dir);
    setText('s-fee-line', View.devFeeLine(s));
    $('s-live').hidden = !s.live_page;
    var iw = View.interfaceWords(s.ui);
    setText('s-ui-line', iw.line || 'Interface'); setText('s-ui-help', iw.help || ''); setHidden('s-ui-help', !iw.help);
    sw('s-ui-builtin', st.ui_builtin);
    var u = s.update || {};
    setText('s-version', View.productName(s.edition) + ' ' + s.version + (s.preview ? ' ' + s.preview : ''));
    setHidden('s-jobs-row', s.edition !== 'lab'); // F14: the public miner runs no remote jobs; the switch is the lab build's
    setText('s-update-note', settingsUpdateNote(u));
    sw('s-auto-update', st.auto_update);
    $('s-install').hidden = !(u.version && (u.ready || u.downloaded || u.status === 'error') && !/no update manifest configured/.test(u.error || '') && !u.applying);
    $('nav-updates-dot').hidden = !(u.available && !u.applying);
    fillJobs(s.jobs || {}, s.now);
  }
  function fillJobs(j, now) {
    setText('s-jobs-key', j.key_fingerprint ? 'signing key sha256:' + j.key_fingerprint : '');
    setText('s-jobs-note', View.jobsNote(j, now, jobTitle));
    var h = j.history || [], sig = JSON.stringify(h);
    if (sig === jobsSig) return;
    jobsSig = sig;
    $('s-jobs-history-btn').hidden = !h.length;
    $('s-jobs-history').innerHTML = h.length ? '<table><thead><tr><th>job</th><th>kind</th><th>started</th><th>exit</th><th>report</th></tr></thead><tbody>' + h.map(function (r) {
      return '<tr class="' + esc(r.status) + '"><td class="mono" title="' + esc(r.summary || '') + '">' + esc(r.id) + '</td><td>' + esc(r.kind) + '</td><td>' + (r.started_at ? clock(r.started_at) : '') + '</td><td>' + esc(r.status) + (r.status !== 'running' ? ' (' + r.exit + ')' : '') + '</td><td>' + (r.uploaded ? 'uploaded' : (r.status === 'running' ? 'pending' : 'not uploaded')) + '</td></tr>';
    }).join('') + '</tbody></table>' : '<p class="empty">No job has run on this machine yet.</p>';
  }

  function renderPage(s) {
    renderFleetSettings(s); // 0.2.8: the rail's Fleet entry and the Settings card follow the state on every render
    if (page === 'fleet') showFleet(s && s.settings ? s : state);
    if (page === 'mine') renderMine(s);
    else if (page === 'cards') renderCardsPage(s);
    else if (page === 'tune') renderTune(s);
    else if (page === 'earnings') renderEarnings(s);
    else if (page === 'settings') renderSettings(s);
  }
  function renderPill(s) {
    var pill = $('pill'), pw = View.pill(s);
    pill.classList.remove('on', 'warn');
    if (pw.tone === 'on') pill.classList.add('on'); else if (pw.tone === 'bad') pill.classList.add('warn');
    $('pill-text').textContent = pw.text;
    var rf = View.railFoot(s, CAPTURE), foot = (rf.name ? '<b title="' + esc(rf.name) + '">' + esc(rf.name) + '</b><br>' : '') + esc(rf.up);
    if (foot !== lastFoot) { lastFoot = foot; $('rail-status').innerHTML = foot; }
    // F-E1 again (main's audit item 7, 13 October 2026): the footer names the chain the source mines, from the source's
    // chain id and the feed's name when the feed reads the same chain; never the fixed string of the build
    var sc = Number((s.source || {}).chain_id || 0), fd = (sceneDoc && sceneDoc.feed) || {}, chainName = sc > 0 ? View.chainWord(sc, Number(fd.chain_id || 0) === sc ? (fd.name || '') : '') : (View.idleReason(s.mining || {}) ? 'not mining' : 'connecting'); // F-191: never "no chain yet" as if waiting
    var fv = '<span class="name">Ember </span>' + esc(s.version) + (s.preview ? ' ' + esc(s.preview) : '') + '<span class="chain"> · ' + esc(chainName) + '</span>';
    if (fv !== lastVersion) { lastVersion = fv; $('foot-version').innerHTML = fv; }
  }
  var lastFoot = '', lastVersion = '';
  // the clock card on Mine (the strip carries it on the setup screens, see Notices)
  function renderClock(s) {
    var c = s.clock || { severity: 'none' }, bad = c.severity === 'block', warn = c.severity === 'warn';
    var card = $('m-clock');
    card.hidden = !(bad || warn);
    card.classList.toggle('warn', warn);
    setText('m-clock-msg', c.message || '');
    setText('m-clock-hint', c.hint ? 'By hand: ' + c.hint : '');
    setText('m-clock-result', c.sync_result || '');
    $('m-clock-sync').disabled = !!c.syncing; setText('m-clock-sync', c.syncing ? 'Syncing' : 'Sync clock');
    $('btn-start').disabled = bad;
    $('start-blocked').hidden = !bad;
    setText('start-blocked', bad ? c.message + ' Use "Sync clock" above.' : '');
    $('btn-key-done').disabled = bad || !$('key-ack').checked;
  }

  // ---------- the status strip (Notices, top of this file): one notice, its actions, the close control ----------
  // dismissed: the keys closed this session; a notice with a closed key stays hidden until its key changes
  var dismissed = {}, noticeSig = '', stripH = 0;
  function renderNotices(s) {
    var n = Notices.pick(Notices.gather(s, { dashboard: phase === 'dashboard' }), dismissed);
    var strip = $('notices'), el = $('notice');
    if (!n) {
      if (noticeSig) { noticeSig = ''; strip.hidden = true; layoutStrip(); }
      return;
    }
    var sig = [n.key, n.kind, n.tone, n.text, n.detail, n.progress >= 0 ? 'p' : '', JSON.stringify(n.actions)].join('|');
    if (sig !== noticeSig) {
      noticeSig = sig;
      el.className = 'notice ' + n.kind + (n.tone ? ' ' + n.tone : '');
      el.dataset.key = n.key;
      $('notice-text').textContent = n.text; $('notice-text').title = n.title || '';
      $('notice-detail').textContent = n.detail; $('notice-detail').hidden = true; $('notice-detail').title = n.detail;
      $('notice-more').hidden = !n.detail; $('notice-more').setAttribute('aria-expanded', 'false'); $('notice-more').classList.remove('open');
      $('notice-actions').innerHTML = n.actions.map(function (a) { return '<button class="btn small ' + (a.primary ? 'primary' : 'ghost') + '" data-act="' + esc(a.act) + '"' + (a.disabled ? ' disabled' : '') + '>' + esc(a.label) + '</button>'; }).join('');
      $('notice-prog').hidden = !(n.progress >= 0);
      strip.hidden = false;
    }
    if (n.progress >= 0) $('notice-prog').firstElementChild.style.width = Math.round(n.progress * 100) + '%';
    layoutStrip();
  }
  // main sits under the strip: its top is set once per height change and moves with the 150 ms transition in the
  // CSS; an empty strip reserves nothing
  function layoutStrip() {
    var strip = $('notices'), h = strip.hidden ? 0 : strip.offsetHeight;
    if (h === stripH) return;
    stripH = h;
    var top = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--top'), 10) || 60;
    $('main').style.top = h ? (top + h) + 'px' : '';
  }
  window.addEventListener('resize', layoutStrip);
  $('notice').addEventListener('click', function (e) {
    var b = e.target.closest('[data-act]'); if (!b || b.disabled) return;
    var act = b.dataset.act, key = $('notice').dataset.key;
    if (act === 'close') { dismissed[key] = true; renderNotices(state || {}); }
    else if (act === 'more') { var open = $('notice-detail').hidden; $('notice-detail').hidden = !open; b.setAttribute('aria-expanded', open ? 'true' : 'false'); b.classList.toggle('open', open); layoutStrip(); }
    else if (act === 'install') { api('api/update/install', {}); toast('Installing at once'); }
    else if (act === 'retry') { api('api/update/install', {}); toast('Trying the update again'); }
    else if (act === 'open') api('api/update/open', {});
    else if (act === 'sync') api('api/clock/sync', {});
  });

  // ---------- the update card (UpdateCard, top of this file): one update, centred, over everything ----------
  // mem: open (the card is up), later (the key Later was pressed on), seen (the version it was shown for);
  // asked: Install now was pressed on this card and the engine has not flipped to applying yet
  var updMem = { open: false, later: '', seen: '' }, updSig = '', updAsked = '', updMore = false;
  if (forcedUpdate && params.get('card') === '1') updMem.open = true; // screenshots: installing and failed only show on an open card
  function cardCtx(s) { return { jobActive: !!(s.jobs && s.jobs.active), uptime_s: s.uptime_s, quitting: !!s.quitting, sheetOpen: !$('sheet-key').hidden }; }
  function renderUpdateCard(s) {
    var m = UpdateCard.model(s.update, s), d = UpdateCard.decide(m, cardCtx(s), updMem), wrap = $('upd');
    if (!d.show) {
      if (updMem.open || !wrap.hidden) { updMem.open = false; wrap.hidden = true; updSig = ''; }
      return;
    }
    if (!updMem.open) { updMem.open = true; updMem.seen = m.version; updMore = false; if (updAsked !== m.key) updAsked = ''; }
    var asked = updAsked === m.key && m.stage !== 'installing' && m.stage !== 'failed';
    var sig = [m.key, m.stage, m.line, m.note, m.cause, m.ring, asked ? 'asked' : '', m.lines.join('|')].join('\u0001');
    if (sig !== updSig) {
      updSig = sig;
      $('upd-name').textContent = m.name;
      $('upd-line').textContent = m.line;
      $('upd-cause').textContent = m.cause; $('upd-cause').hidden = !m.cause;
      $('upd-notes').innerHTML = m.lines.map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
      $('upd-all').innerHTML = m.all.map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
      $('upd-more').hidden = !m.more; if (!m.more) updMore = false;
      $('upd-more').textContent = updMore ? 'Less' : 'What changed'; $('upd-more').setAttribute('aria-expanded', updMore ? 'true' : 'false');
      $('upd-all').hidden = !updMore;
      $('upd-mark').className = 'upd-mark ' + m.ring;
      var acts = asked ? [{ act: 'install', label: m.stage === 'ready' || m.stage === 'deferred' ? 'Installing' : 'Installs when ready', primary: true, disabled: true }, { act: 'later', label: 'Later' }] : m.actions;
      $('upd-actions').innerHTML = acts.map(function (a) { return '<button class="btn ' + (a.primary ? 'primary big' : 'ghost') + '" data-act="' + esc(a.act) + '"' + (a.disabled ? ' disabled' : '') + '>' + esc(a.label) + '</button>'; }).join('');
      $('upd').dataset.dismiss = m.dismissable ? '1' : '';
    }
    // the ring and the percent move every poll; the words above do not
    if (m.ring === 'progress') { $('upd-arc').style.strokeDashoffset = (276.5 * (1 - m.pct / 100)).toFixed(1); $('upd-pct').textContent = m.pct + '%'; $('upd-pct').hidden = m.stage !== 'downloading'; }
    else { $('upd-arc').style.strokeDashoffset = ''; $('upd-pct').hidden = true; }
    var meta = [];
    if (m.stage === 'downloading' && m.size) meta.push(m.pct + '% of ' + m.size);
    else if (m.size && (m.stage === 'available')) meta.push(m.size + ' download');
    else if (m.size && (m.stage === 'ready' || m.stage === 'manual' || m.stage === 'deferred' || m.stage === 'staging')) meta.push(m.size + ', downloaded');
    if (m.note) meta.push(m.note);
    $('upd-meta').textContent = meta.join(' · ');
    if (wrap.hidden) { wrap.hidden = false; $('upd-card').focus({ preventScroll: true }); }
  }
  function cardLater() {
    if (!updMem.open || !$('upd').dataset.dismiss) return;
    var m = UpdateCard.model(state && state.update, state || {});
    updMem.later = m ? m.key : updMem.later; updMem.open = false; $('upd').hidden = true; updSig = '';
  }
  $('upd').addEventListener('click', function (e) {
    if (e.target === $('upd')) { cardLater(); return; }
    var b = e.target.closest('[data-act]'); if (!b || b.disabled) return;
    var act = b.dataset.act, m = UpdateCard.model(state && state.update, state || {});
    if (act === 'later') cardLater();
    else if (act === 'install' || act === 'retry') { api('api/update/install', {}); updAsked = m ? m.key : ''; updSig = ''; toast(act === 'retry' ? 'Trying the update again' : m && m.stage === 'ready' ? 'Installing at once' : 'Installs as soon as it is downloaded'); if (state) renderUpdateCard(state); }
    else if (act === 'open') api('api/update/open', {});
  });
  $('upd-more').addEventListener('click', function () { updMore = !updMore; $('upd-all').hidden = !updMore; this.textContent = updMore ? 'Less' : 'What changed'; this.setAttribute('aria-expanded', updMore ? 'true' : 'false'); });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') { if (!$('upd').hidden) { e.preventDefault(); cardLater(); } else if (!$('ask-quit').hidden) $('ask-quit').hidden = true; } });

  // ---------- over-the-air updates (src/ota.rs): the Settings card's line; the strip is Notices.updateNotice ----------
  function settingsUpdateNote(u) {
    var n = Notices.updateNotice(u, state || {}), parts = [];
    if (u.updated_from) parts.push('Updated from ' + u.updated_from + '.');
    if (u.rolled_back) parts.push('Rolled back: ' + u.rolled_back + '.');
    if (n && n.kind !== 'update-installed' && !(u.rolled_back && u.status === 'error')) parts.push(n.text + (n.detail ? ' ' + Notices.endDot(n.detail) : ''));
    else if (u.status === 'checking') parts.push('Checking.');
    else if (u.status === 'retrying') parts.push('The update server could not be reached; checking again in ' + Math.max(1, Math.round((u.retry_s || 600) / 60)) + ' minutes.');
    else if (u.status === 'error' && /running from its disk image/.test(u.error || '')) parts.push(u.error + '.');
    else if (u.status === 'current') parts.push('Up to date' + (u.checked_at ? '. Checked ' + rel(state.now - u.checked_at) : '') + '.');
    else if (/no update manifest configured/.test(u.error || '')) parts.push('This build has no update address; updates come as a new download.');
    else if (u.error) parts.push(u.error);
    else if (!u.status || u.status === 'unknown') parts.push('Not checked yet. The app checks by itself every hour.');
    if (u.activation_height && !u.urgent && state && state.node && u.activation_height > state.node.daa) parts.push('A rule change comes at block ' + withCommas(u.activation_height) + '.');
    return parts.join(' ');
  }
  function sampleUpdate(kind) {
    // ?update=<kind>[&auto=0]: the card and the strip for each state, without an engine
    var u = { available: true, version: '0.2.0', notes: 'The miner has four sections: Mine, Earnings, Prove, Settings. Every card is a row with its rate, watts, £ a day and a Tune button. The node is one line. Updates are one card.', status: 'ready', downloaded: true, ready: true, applying: false, progress: 1, size: 20588331, auto: params.get('auto') !== '0', wait: 'installs at the next safe moment', urgent: false, urgent_text: '', activation_height: 0, error: '', updated_from: '', rolled_back: '' };
    if (!u.auto) u.wait = 'waiting for Install now (automatic updates are off)';
    if (kind === 'available') { u.status = 'available'; u.downloaded = false; u.ready = false; u.progress = 0; }
    if (kind === 'downloading') { u.status = 'downloading'; u.downloaded = false; u.ready = false; u.progress = Math.min(0.97, 0.43 + ((Date.now() / 1000) % 60) / 110); }
    if (kind === 'staging') { u.status = 'staging'; u.ready = false; u.progress = 1; }
    if (kind === 'waiting') { u.wait = 'hourly program boundary in 97 s; installing after it'; }
    if (kind === 'applying') { u.status = 'applying'; u.applying = true; }
    if (kind === 'deferred') { u.status = 'deferred'; u.wait = 'waits for the next time someone is at this PC (Windows asks for permission); mining continues'; }
    if (kind === 'urgent') { u.status = 'downloading'; u.ready = false; u.downloaded = false; u.progress = 0.27; u.urgent = true; u.activation_height = 120000; u.urgent_text = 'Consensus upgrade at height 120000 (difficulty v2): the node is 1,240 blocks away. Installing 0.1.14 now.'; }
    if (kind === 'manual') { u.status = 'manual'; u.ready = false; u.wait = '/Applications is not writable; open the downloaded disk image and drag the app over the old one'; }
    if (kind === 'error') { u.status = 'error'; u.error = 'sha256 mismatch: the file is not what the manifest signed'; u.downloaded = false; u.ready = false; }
    if (kind === 'updated') { u.status = 'current'; u.available = false; u.ready = false; u.downloaded = false; u.updated_from = '0.1.13'; u.version = '0.1.14'; }
    return u;
  }
  // ---------- remote jobs (src/jobrun.rs): the Settings row; the strip is Notices.jobNotice ----------
  var jobTitle = Notices.jobTitle;
  function sampleJob(kind, now) {
    var j = { allowed: true, key_fingerprint: '8f186e37', url_set: true, checked_at: now - 30, error: '', queued: 0, account: '', active: false, id: '', kind: '', title: '', stage: '', message: '', started_at: 0, run_id: '', results: [], last: {}, last_results: [], history: [] };
    if (kind === 'running') { j.active = true; j.id = 'job-2026-10-05-03'; j.kind = 'shard-benchmark'; j.title = 'shard benchmark'; j.stage = 'proving shard 3 of 8'; j.started_at = now - 400; j.results = ['RESULT shard=1 prove_s=41.2', 'RESULT shard=2 prove_s=39.8']; j.history = [{ id: j.id, kind: j.kind, title: j.title, status: 'running', started_at: j.started_at, finished_at: 0, exit: 0, run_id: 'r2', uploaded: false, summary: '' }]; }
    else if (kind === 'done') { j.last = { id: 'job-2026-10-05-02', kind: 'shard-benchmark', title: 'shard benchmark', status: 'done', started_at: now - 800, finished_at: now - 60, exit: 0, run_id: 'r1', uploaded: true, summary: 'shard benchmark finished, exit 0; 8 RESULT lines, 1 result file uploaded' }; j.history = [j.last]; }
    else if (kind === 'failed') { j.last = { id: 'job-2026-10-05-01', kind: 'build', title: 'build', status: 'failed', started_at: now - 2000, finished_at: now - 900, exit: 1, run_id: 'r0', uploaded: false, summary: 'build exited with code 1' }; j.last_results = ['STAGE build node', 'BUILD FAILED: error[E0425]: cannot find value `foo` in this scope (src/engine.rs:2082)']; j.history = [j.last]; }
    return j;
  }
  // ?tune=running|tuned|measured : a sample tune state on the first card (screenshots, no engine)
  function sampleTune(s, kind) {
    var c = s.mining.cards[0]; if (!c) return;
    if (kind === 'running') { c.state = 'tuning'; c.sweep_state = 'running'; c.tune_step = 7; c.tune_steps = 9; c.tune_eta_s = 230; c.tune_plan = 'full'; c.sweep_note = 'tuning: holding 2472 MHz · 100% · 41 s (step 7 of 9)'; if (!(c.power_w > 0)) c.power_w = 219.6; if (!(c.hash_now > 0)) c.hash_now = 123.4; }
    var curve = [{ clock_mhz: 0, power_pct: 100, watts: 374, mhs: 122.5, eff: 0.328 }, { clock_mhz: 0, power_pct: 90, watts: 340, mhs: 122.4, eff: 0.36 }, { clock_mhz: 0, power_pct: 80, watts: 302, mhs: 122.3, eff: 0.405 }, { clock_mhz: 0, power_pct: 70, watts: 264, mhs: 118.1, eff: 0.447 }, { clock_mhz: 0, power_pct: 60, watts: 228, mhs: 104.9, eff: 0.46, mark: 'hot' }, { clock_mhz: 2780, power_pct: 80, watts: 298, mhs: 122.4, eff: 0.411 }, { clock_mhz: 2470, power_pct: 80, watts: 290, mhs: 122.3, eff: 0.422 }, { clock_mhz: 2160, power_pct: 80, watts: 281, mhs: 117.0, eff: 0.416 }, { clock_mhz: 1850, power_pct: 80, watts: 270, mhs: 109.2, eff: 0.404 }];
    if (kind === 'tuned' || kind === 'mix') { c.tune_line = 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)'; c.tune_source = 'full'; c.tune_control = true; c.tune_clock_mhz = 2470; c.sweep_pct = 80; c.sweep_at = s.now - 7200; c.sweep_watts = 290; c.sweep_mhs = 122.3; c.tune_curve = curve; c.power_default_w = c.power_default_w || 374; c.lock_mhz = 2470; c.lock_mhs = 122.3; c.lock_w = 290; c.lock_mhw = 0.422; c.unlocked_mhs = 122.3; c.unlocked_w = 302; c.lock_step = 0; c.lock_steps = 0; c.lock_at = s.now - 7200; c.lock_note = 'rate fell 4.3 percent at 2160 MHz'; c.tiers = [{ id: 'efficiency', clock_mhz: 1850, power_pct: 80, w: 270, mhs: 109.2, mhw: 0.404, source: 'measured' }, { id: 'balanced', clock_mhz: 2470, power_pct: 80, w: 290, mhs: 122.3, mhw: 0.422, source: 'measured' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 374, mhs: 122.5, mhw: 0.328, source: 'stock' }]; c.tier = 'balanced'; c.tier_note = ''; c.tier_at = s.now - 7200; }
    if (kind === 'running') { c.lock_step = 2; c.lock_steps = 4; }
    if (kind === 'mix' && s.mining.cards[1]) { var d = s.mining.cards[1]; d.state = 'tuning'; d.sweep_state = 'running'; d.tune_step = 4; d.tune_steps = 9; d.tune_eta_s = 410; d.tune_plan = 'full'; d.tune_control = true; d.hash_now = d.hash_now || 98.1; }
    if (kind === 'measured') { c.tune_line = 'Tuned: 26.7 MH/s at 38 W (0.703 MH/W)'; c.tune_source = 'baseline'; c.tune_control = false; c.sweep_at = s.now - 600; c.sweep_note = 'measure only on Apple silicon: the system sets the clocks and the power; no control exposed'; }
  }
  function render(s) {
    state = s; stateAt = performance.now();
    View.applyMoney({ region: (s.settings && s.settings.region) || stored('igneum.region') || '', currency: (s.settings && s.settings.currency) || stored('igneum.currency') || '' }, lang);
    if (forcedUpdate) { s.update = sampleUpdate(forcedUpdate); if (forcedUpdate === 'updated') { s.version = '0.2.0'; s.uptime_s = 20; } }
    if (forcedJob) s.jobs = sampleJob(forcedJob, s.now || 0);
    if (params.get('tune')) sampleTune(s, params.get('tune'));
    if (params.get('uptime')) s.uptime_s = parseInt(params.get('uptime'), 10) || 0; // the card's first-minute rule, for screenshots
    renderPill(s);
    renderClock(s);
    renderNotices(s);
    renderUpdateCard(s);
    if (phase === 'cards') renderCards(s);
    if (phase === 'network') renderNetworkStep(s); // the step renders on every state, not only on the click that opens it (a forced ?screen=network start read empty)
    if (phase === 'dashboard') renderPage(s);
    if (s.quitting && !$('btn-quit').disabled) { $('btn-quit').disabled = true; $('s-quit').disabled = true; }
  }
  // ---------- ui-ota: the health ping after the first paint, the first-paint error, the gentle reload ----------
  var uiLoaded = '', uiPinged = false, uiFirstError = '';
  window.addEventListener('error', function (e) { if (!uiPinged && !uiFirstError) { uiFirstError = String(e && e.message || 'script error'); api('api/ui/health', { error: uiFirstError }).catch(function () { }); } });
  function uiHealth(s) {
    if (uiPinged) return;
    uiPinged = true;
    uiLoaded = (s.ui && s.ui.active_version) || '';
    if (!uiFirstError) api('api/ui/health', {}).catch(function () { });
  }
  function uiReloadIfChanged(s) {
    var typing = !!(document.activeElement && (document.activeElement.tagName === 'INPUT' || document.activeElement.tagName === 'TEXTAREA' || document.activeElement.tagName === 'SELECT'));
    if (View.shouldReload(uiLoaded, s.ui, { typing: typing, sheetOpen: !$('sheet-key').hidden || !$('upd').hidden, phase: phase })) { uiLoaded = ''; location.reload(); }
  }
  $('s-ui-builtin').addEventListener('change', function () { var on = this.checked; api('api/ui/builtin', { on: on }).then(function (r) { if (r.ok) toast(on ? 'The built-in interface serves from the next load' : 'The interface over the air serves again'); else { toast(r.error || 'could not change'); $('s-ui-builtin').checked = !on; } }); });
  var failures = 0, first = true, pollTick = 0;
  function poll() {
    // hidden windows poll every 5 s (the pill and the tray text still need a recent state), visible ones every second
    if (hiddenNow() && (pollTick++ % 5)) return;
    api('api/state').then(function (s) {
      var a = performance.now();
      failures = 0;
      if (!$('restart-cover').hidden) $('restart-cover').hidden = true;
      if (first) {
        first = false;
        var start = forced || (s.setup_done ? 'dashboard' : 'welcome');
        if (start === 'settings') { page = 'settings'; show('dashboard'); }
        else if (start === 'key') { show('address'); showKeySheet({ address: s.address.value || '0x0000000000000000000000000000000000000000', display: s.address.display || '0x0000000000000000000000000000000000000000', private_key: '0x' + '0'.repeat(64), wallet_file: s.address.wallet_file || (s.node_dir.replace(/[^\/\\]+$/, '') + 'app/wallet.json') }); }
        else show(start);
        if (start === 'cards') renderCards(s);
        if (params.get('logs') === '1' && start === 'dashboard') setDrawer(true); // screenshots: the log drawer open
      }
      render(s);
      pollChain(false);
      uiHealth(s);
      uiReloadIfChanged(s);
      perf.render += performance.now() - a; perf.polls++;
      if (failures === 0 && $('pill').classList.contains('away')) { $('pill').classList.remove('away', 'warn'); }
    }).catch(function (e) {
      // a render that throws is a bug in this window, never the engine's silence: say so in the console (app-ia-26)
      if (e && e.stack && typeof console !== 'undefined') console.error('render failed: ' + e.stack);
      failures++;
      if (failures >= 4) {
        $('pill').classList.add('warn', 'away'); $('pill-text').textContent = state && state.quitting ? 'Stopped' : 'App not answering';
        // the restart cover: the engine keeps its address across a restart or an install, so the window reconnects by
        // itself; past a minute the words say the app may have been quit
        if (!(state && state.quitting)) { $('restart-cover').hidden = false; setText('rc-title', failures >= 60 ? 'Ember is not answering' : 'Ember is restarting'); setText('rc-sub', failures >= 60 ? 'The window reconnects by itself the moment the app is back. If Ember was quit, open it again.' : 'The window reconnects by itself.'); }
        if (phase === 'dashboard') {
          setText('node-line-text', 'Node: no answer'); $('node-go').title = 'the app is not answering. The window reconnects by itself'; $('node-go').classList.add('bad');
          $('btn-toggle').title = 'the app is not answering'; $('btn-toggle').disabled = true;
          setText('t-hash-sub', 'no answer from the app');
        }
      }
    });
  }
  poll(); setInterval(poll, 1000);
  size();
  // debug: time N synchronous calls of each hot path (ms per call), independent of the host's frame rate
  if (debug) window.__igneumBench = function (n) {
    n = n || 100; var out = {}, a, i;
    a = performance.now(); for (i = 0; i < n; i++) draw(performance.now()); out.drawMsPerFrame = +((performance.now() - a) / n).toFixed(3);
    if (state) { a = performance.now(); for (i = 0; i < n; i++) render(state); out.renderMsPerPoll = +((performance.now() - a) / n).toFixed(3); }
    if (drawerOpen()) { a = performance.now(); for (i = 0; i < n; i++) renderLog(true); out.logMsPerRender = +((performance.now() - a) / n).toFixed(3); }
    return out;
  };
})();
