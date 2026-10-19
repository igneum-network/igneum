/* Igneum Miner dashboard (miner-ui-3). Talks to the engine on the same origin: api/state every second, api/log while
   the drawer is open. Screens: welcome, cards, address (plus the one-time key sheet), then the dashboard with four
   pages behind a rail (Mine, Earnings, Prove, Settings). ?screen=<name> forces a screen, ?page=<name> a page, and
   ?update=, ?job=, ?tune= show sample states (screenshots). Everything is relative to the token path the page was
   opened on. The pure blocks (Notices, UpdateCard, View, TuneLine) run under node for the tests. */

var Notices = (function () {
  'use strict';
  var LEVEL = { 'update-installing': 0, 'update-urgent': 0, 'update-failed': 0, 'job-failed': 1, clock: 2, 'job-running': 3, tuning: 3, 'update-available': 4, 'job-done': 5, 'card-added': 5, 'card-removed': 5, 'update-installed': 6 };
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
    var ver = u.version || '', v = 'Igneum Miner ' + ver, pct = u.progress > 0 ? Math.round(u.progress * 100) : 0;
    if (u.status === 'error') {
      if (/no update manifest configured/.test(u.error || '')) return null; // a build without a manifest: nothing to try, Settings says so
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
      return notice('update-installed', 'update:installed:' + now, 'Updated to Igneum Miner ' + now + ' from ' + u.updated_from + '.');
    }
    return null;
  }

  function jobTitle(j) { return j.title && j.title !== j.kind ? j.title : (j.kind === 'shard-benchmark' ? 'shard benchmark' : j.kind === 'build' ? 'build (mining continues)' : j.kind); }
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
      return notice('job-running', 'job:run:' + j.id, 'Job: ' + jobTitle(j) + ' running, ' + m + ' min.' + (j.stage ? ' ' + endDot(cap(j.stage)) : ''), { detail: results.length ? results[results.length - 1] : '', title: j.message || '' });
    }
    var l = j.last;
    if (!l || !l.id) return null;
    var mins = Math.max(0, Math.round((l.finished_at - l.started_at) / 60)), t = l.title || l.kind;
    var report = l.uploaded ? 'Report uploaded.' : 'Report not uploaded.';
    if (l.status === 'done') {
      if (l.finished_at && now - l.finished_at > JOB_DONE_S) return null;
      return notice('job-done', 'job:done:' + l.id, 'Job: ' + t + ' done after ' + mins + ' min. ' + report, { title: l.summary || '' });
    }
    var word = l.status === 'timeout' ? 'hit its time cap' : l.status === 'aborted' ? 'was stopped' : 'failed';
    return notice('job-failed', 'job:failed:' + l.id, 'Job: ' + t + ' ' + word + ' after ' + mins + ' min, exit ' + l.exit + '. ' + report, { tone: 'bad', detail: firstErrorLine(j.last_results || [], l.summary), title: l.summary || '' });
  }

  // c = state.clock: the engine's words, Sync clock, the manual hint underneath
  function clockNotice(c) {
    if (!c || !(c.severity === 'block' || c.severity === 'warn')) return null;
    return notice('clock', 'clock:' + c.severity, c.message || '', { tone: c.severity === 'block' ? 'bad' : 'warn', detail: c.hint || '', actions: [{ act: 'sync', label: c.syncing ? 'Syncing' : 'Sync clock', primary: true, disabled: !!c.syncing }] });
  }

  // cards = state.mining.cards; now = state.now: every card a re-detection added or lost in the last 5 minutes
  // (added_at / removed_at are 0 for the cards found at start). The newest first, so pick() shows it.
  function kindWord(k) { return k === 'apple' ? 'Apple silicon' : k === 'discrete' ? 'Discrete' : k === 'integrated' ? 'Integrated' : k === 'external' ? 'External' : 'Unknown'; }
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
  return { tuneNotice: tuneNotice, LEVEL: LEVEL, JOB_DONE_S: JOB_DONE_S, INSTALLED_S: INSTALLED_S, CARD_S: CARD_S, updateNotice: updateNotice, jobNotice: jobNotice, clockNotice: clockNotice, cardNotices: cardNotices, gather: gather, pick: pick, jobTitle: jobTitle, cap: cap, endDot: endDot, kindWord: kindWord };
})();

/* ---------- the update card (pure; update-card.test.mjs loads this block) ----------
   A centred card over the window for one update: the mark with a progress ring, "Igneum Miner 0.3.14", one line
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
  var NAME = 'Igneum Miner', LINE_MAX = 70, LINES = 3, FIRST_S = 60;
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
    if (u.status === 'error') {
      if (/no update manifest configured/.test(u.error || '')) return null;
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
  var PAGES = [
    { id: 'mine', title: 'Mine', sub: 'your cards, the rate, what it costs' },
    { id: 'earnings', title: 'Earnings', sub: 'what it made, where it goes' },
    { id: 'prove', title: 'Prove', sub: 'turn blocks into proofs, paid in IGN' },
    { id: 'settings', title: 'Settings', sub: 'tuning, this machine, updates, logs' }
  ];
  var DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  var SHARD_GB = 20.4, PROVE_GB = 24;
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

  // one GPU row on the Mine page: cd = state.mining.cards[i]
  function cardRow(cd) {
    var removed = cd.removed_at > 0, unusable = !!cd.problem;
    var on = !!cd.enabled && !removed && !unusable, st = removed ? 'removed' : unusable ? 'unusable' : (cd.state || 'off');
    // the rule (docs/plans/ember-tune.md, 6 October 2026): a card never shows 0 MH/s without a reason word
    var tuning = st === 'tuning', held = st === 'held';
    var word = removed ? 'removed' : unusable ? ('not usable (' + cd.problem + ')') : !on ? 'off' : st === 'mining' ? 'mining' : tuning ? tuneWord(cd) : held ? (cd.message || 'held for a remote job') : st === 'restarting' ? 'restart in ' + (cd.restart_in_s || 0) + ' s' : st === 'waiting' ? 'waiting for the node' : st === 'faulted' ? 'stopped after repeated faults' : st === 'failed' ? 'failed' : st === 'starting' ? 'starting' : st === 'ready' ? 'ready' : st;
    var tone = removed ? 'off' : unusable ? 'bad' : !on ? 'off' : (st === 'mining' || tuning) ? 'on' : (st === 'failed' || st === 'faulted' || st === 'restarting') ? 'bad' : '';
    var hash = on && (st === 'mining' || tuning) ? (cd.hash_now >= 100 ? cd.hash_now.toFixed(0) : (cd.hash_now || 0).toFixed(1)) : '';
    var temp = cd.temp_gpu > 0 ? Math.round(cd.temp_gpu) + ' °C' : '';
    var tempTone = cd.temp_mem > 95 || cd.temp_gpu > 92 ? 'hot' : cd.temp_mem > 90 || cd.temp_gpu > 86 ? 'warm' : '';
    var power = cd.power_w > 0 ? Math.round(cd.power_w) + ' W' : '';
    var cap = cd.power_default_w > 0 ? Math.round(cd.power_default_w * (cd.power_pct || 80) / 100) : 0;
    var sub = unusable ? (cd.reason || '') : removed ? 'unplugged; its worker stopped. The row goes in five minutes.' : cd.message || (!on && cd.reason ? cd.reason : '') || '';
    var meta = [];
    if (cd.vram_mb) { var gb = cd.vram_mb / 1024; meta.push((gb >= 10 ? Math.round(gb) : gb.toFixed(gb < 4 ? 1 : 0)) + ' GB' + (cd.kind === 'apple' ? ' unified' : '')); }
    if (on && cd.accepted) meta.push(cd.accepted + ' block' + (cd.accepted === 1 ? '' : 's'));
    if (on && cd.prepared) meta.push('next program ready');
    return { key: cd.key, name: cd.name, vendor: cd.vendor, kind: cd.kind, kindWord: kindWord(cd.kind), integrated: cd.kind === 'integrated', on: on, removed: removed, unusable: unusable, canToggle: cd.kind !== 'unknown' && !removed && !unusable, word: word, tone: tone, hash: hash, temp: temp, tempTone: tempTone, power: power, cap: cap, sub: sub, meta: meta.join(' · '), title: cardTitle(cd) };
  }

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
  function money(v) { if (v === null || v === undefined || isNaN(v)) return ''; return '£' + (v >= 100 ? String(Math.round(v)) : v.toFixed(2)); }
  // MH per watt: the engine's live figure, else rate over draw
  function effOf(cd) { if (cd.eff_mhw > 0) return cd.eff_mhw; return cd.power_w > 0 && cd.hash_now > 0 ? cd.hash_now / cd.power_w : 0; }
  function effText(e) { return e > 0 ? (e >= 10 ? e.toFixed(1) : e.toFixed(2)) + ' MH/W' : ''; }
  // the watts drawn now by the present cards
  function wattsTotal(cards) { return (cards || []).filter(present).reduce(function (a, c) { return a + (c.power_w > 0 ? c.power_w : 0); }, 0); }
  // why a row has no power or temperature cell: one sentence, once, instead of three n/a
  function noReading(cd) {
    if (cd.power_w > 0 || cd.temp_gpu > 0) return '';
    if (cd.vendor === 'apple') return 'No power or temperature reading on Apple silicon';
    if (cd.kind === 'integrated') return 'No power or temperature reading on an integrated GPU';
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
      var nums = String(cd.tune_line).replace(/^Tuned:\s*/, '');
      if (cd.tune_source === 'baseline' || !cd.tune_control) return { kind: 'measured', text: 'Measured ' + nums + ' as it runs' + (when ? ', ' + when : ''), note: reasonOf(cd.sweep_note), button: 'tune', progress: -1 };
      var src = cd.tune_source === 'confirm' ? ' from the fleet prior, confirmed' : '';
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
    return st.sweep === false ? 'Ember Tune is off. Tune and Tune all still work by hand.' : 'Not tuned yet: the first tune starts 2 min into steady mining.';
  }
  // the goal control's consequence: goal = efficiency|balanced|rate, cards, pence
  var GOALS = { efficiency: 'the most hashes per watt', balanced: 'hashes per watt within 1% of the top rate', rate: 'the top rate, whatever the watts' };
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
  function toggle(m, n, s) {
    var cards = shownCards(m.cards).filter(present);
    var on = cards.filter(function (c) { return c.enabled; }).length;
    var mining = cards.filter(function (c) { return c.state === 'mining'; }).length;
    if (s && s.quitting) return { label: 'Stopping', sub: 'miners first, then the node', cls: 'stop', disabled: true, act: '' };
    var tc = tuningCard(cards);
    if (tc) return { label: 'Tuning', sub: 'mining again in ' + tuneEta(tc.tune_eta_s) + ' · ' + tc.name, cls: 'stop', disabled: true, act: '' };
    var heldCards = cards.filter(function (c) { return c.state === 'held'; }).length;
    if (heldCards && !mining) return { label: 'Held', sub: 'a remote job holds the cards; mining restarts when it ends', cls: 'stop', disabled: true, act: '' };
    if (!on) return { label: 'Start mining', sub: cards.length ? 'switch a GPU on below first' : 'no GPU this app can drive', cls: '', disabled: true, act: '' };
    if (m.paused) return { label: 'Start mining', sub: 'paused · the node keeps running', cls: '', disabled: false, act: 'resume' };
    if (m.state === 'mining') return { label: 'Stop mining', sub: 'mining on ' + mining + ' of ' + on + ' card' + (on === 1 ? '' : 's'), cls: 'stop', disabled: false, act: 'pause' };
    if (m.state === 'waiting') return { label: 'Stop mining', sub: n && n.synced ? 'worker starting' : 'waiting for the node to sync', cls: 'stop', disabled: false, act: 'pause' };
    return { label: 'Stop mining', sub: m.state || 'starting', cls: 'stop', disabled: false, act: 'pause' };
  }
  // the row's state word while the miner is paused: the card is on, the miner is not
  function rowWord(r, m) { return r.on && m && m.paused && (r.word === 'off' || r.word === 'ready' || r.word === 'waiting for the node') ? 'paused' : r.word; }

  // the Node page's plain words: n = state.node, clock = state.clock, eta = the sync ETA sentence (may be '')
  function nodeWords(n, clock, eta) {
    var bad = clock && clock.severity === 'block';
    if (bad) return { word: n.state === 'synced' ? 'synced' : n.state, line: 'Your clock is off by ' + skewWord(clock.skew_s) + '. Mining waits until it is synced.', tone: 'bad' };
    switch (n.state) {
      case 'synced': return { word: 'synced', line: 'This node has every block the network has made.' + (n.last_reading_age_s >= 0 ? ' Read ' + Math.round(n.last_reading_age_s) + ' s ago.' : ''), tone: 'ok' };
      case 'syncing':
        var pct = n.headers > n.blocks && n.headers > 0 ? Math.floor(n.blocks / n.headers * 100) : 0;
        var line = n.headers > n.blocks ? (eta || n.message || 'downloading blocks') + ' · ' + withCommas(n.blocks) + ' of ' + withCommas(n.headers) + ' (' + pct + '%)' : (n.message || 'caught up, waiting for the next block');
        return { word: 'syncing', line: line, tone: '' };
      case 'starting': return { word: 'starting', line: n.message || 'opening the database', tone: '' };
      case 'restarting': return { word: 'restarting', line: 'restart in ' + (n.restart_in_s || 0) + ' s' + (n.message ? ' · ' + n.message : ''), tone: 'bad' };
      case 'failed': return { word: 'failed', line: n.message || 'the node could not start', tone: 'bad' };
      case 'stopped': return { word: 'stopped', line: n.message || 'the node is not running', tone: 'bad' };
    }
    return { word: n.state || 'starting', line: n.message || '', tone: '' };
  }
  // the one node line on Mine: "Node synced · 6 peers · 109,859 blocks"
  function nodeLine(n, clock, eta) {
    var w = nodeWords(n, clock, eta), parts = ['Node ' + w.word];
    if (n.peers > 0) parts.push(n.peers + ' peer' + (n.peers === 1 ? '' : 's'));
    if (n.blocks > 0) parts.push(withCommas(n.blocks) + ' blocks');
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
    if (!sw) return (daa > 0 ? 'Every planned switch is behind this node. ' : '') + 'A switch is a planned rule change. The node applies it by itself when the chain reaches that height.';
    var blocks = daa > 0 ? withCommas(sw.away) + ' blocks away, about ' + (sw.away < 3600 ? Math.max(1, Math.round(sw.away / 60)) + ' min' : sw.away < 86400 ? Math.floor(sw.away / 3600) + ' h ' + Math.round(sw.away % 3600 / 60) + ' min' : Math.floor(sw.away / 86400) + ' d') + ' at one block a second.' : 'the node applies it by itself at that height.';
    return sw.name + ' at DAA ' + withCommas(sw.daa) + ': ' + blocks;
  }

  // the Prove page: pv = state.proving, enabled = state.settings.prove, synced = state.node.synced
  function proveWords(pv, enabled, synced) {
    pv = pv || {};
    if (!enabled) return { word: 'off', sub: 'not proving', tone: 'off', note: 'Off. Switch it on and this machine proves the pieces the chain assigns to its keys.' };
    if (pv.status === 'setup' || (pv.enabled && !pv.available && pv.setup_hint)) return { word: 'needs setup', sub: 'one-time install', tone: 'bad', note: pv.setup_hint || pv.message || 'The prover is not installed yet.' };
    if (!synced || pv.status === 'waiting') return { word: 'waiting', sub: 'for the node to sync', tone: '', note: pv.message || 'Proving starts once the node has every block.' };
    if (pv.status === 'proving') return { word: 'proving', sub: pv.current || 'a shard', tone: 'on', note: pv.message || 'A proof takes minutes on a CPU and seconds on a CUDA card.' };
    if (pv.status === 'submitted') return { word: 'submitted', sub: 'waiting to be paid', tone: 'on', note: pv.message || 'The node checks the proof, then the chain pays it.' };
    if (pv.status === 'idle') return { word: 'idle', sub: 'nothing assigned', tone: 'ok', note: pv.message || 'On. Nothing is assigned to your keys right now; the next shard arrives within a minute.' };
    return { word: pv.status || 'on', sub: pv.backend ? pv.backend.toUpperCase() : '', tone: '', note: pv.message || '' };
  }
  function verifierWords(pv) {
    var vm = (pv && pv.verifier_mode) || 'unknown';
    var word = { command: 'verifying', trust: 'trusting (devnet only)', off: 'off: relay only', unknown: 'not read yet' }[vm] || vm;
    if (vm === 'command' && pv.pool_entries) word += ' · pool ' + pv.pool_verified + '/' + pv.pool_entries + (pv.pool_failed ? ', ' + pv.pool_failed + ' rejected' : '');
    return { word: word, tone: vm === 'command' ? 'ok' : (vm === 'off' || vm === 'trust') ? 'warn' : 'dim', needsSetup: vm === 'off' && /WSL2 prover is not installed/.test((pv && pv.verifier_reason) || '') };
  }
  // the tier sentence per card: can this card prove, and how (the 24 GB rule and the 20.4 GB shard are the app's own
  // words since miner-ui-2; the engine's proving.available wins when it disagrees)
  function proveTier(cd) {
    var gb = cd.vram_mb ? Math.round(cd.vram_mb / 1024) : 0;
    if (cd.vendor === 'apple') return cd.name + ': proves on the CPU, slowly.';
    if (cd.vendor === 'nvidia') return gb >= PROVE_GB ? cd.name + ': proves while it mines (' + gb + ' GB; a full shard needs ' + SHARD_GB + ' GB).' : cd.name + ': cannot prove a full shard (' + gb + ' GB; a full shard needs ' + SHARD_GB + ' GB).';
    return cd.name + ': cannot prove yet (the prover is CUDA only).';
  }
  // the counts in one sentence: "idle · nothing assigned · 0 assigned · 0 proven · 0 paid · 0.0000 IGN"
  function proveLine(pv, enabled, synced) {
    pv = pv || {};
    var w = proveWords(pv, enabled, synced), ign = pv.paid_wei && Number(pv.paid_wei) ? (Number(pv.paid_wei) / 1e18).toFixed(4) : '0.0000';
    return cap(w.word) + (w.sub ? ' · ' + w.sub : '') + ' · ' + (pv.assigned || 0) + ' assigned · ' + (pv.submitted || 0) + ' proven · ' + (pv.paid || 0) + ' paid · ' + ign + ' IGN';
  }

  // the miner's own dev-fee line, next to the rewards address
  function devFeeText(s) {
    var d = s.dev_fee || {};
    var addr = d.address ? ' to ' + d.address.slice(0, 8) + '…' + d.address.slice(-4) : '';
    if (!s.settings.dev_fee) return 'Dev fee off (--dev-fee 0); the default is 1% (1 block in 100)' + addr;
    if (d.line && !d.on && !d.address) return 'Dev fee: ' + d.line;
    return 'Dev fee ' + (d.percent || 1) + '% (1 block in 100)' + addr + (d.address ? '' : '; the miner names the address when it starts');
  }
  function devFeeLine(s) {
    return s.settings.dev_fee ? 'One block in 100 pays the miner software’s dev fee' + (s.mining.fee_total ? ' (' + withCommas(s.mining.fee_total) + ' so far)' : '') + '. The switch above turns it off.' : 'The dev fee is off. Every block pays this address.';
  }

  // the remote-jobs line on Settings: j = state.jobs, now = state.now
  function jobsNote(j, now, jobTitle) {
    var parts = [];
    if (j.account) parts.push(j.account);
    if (!j.allowed) parts.push('Off: nothing runs here until the switch above allows remote jobs.');
    else if (!j.url_set) parts.push('No jobs address in this build.');
    else if (j.error) parts.push(j.error + '.');
    else if (j.active) parts.push('Running ' + jobTitle(j) + ' (' + j.id + ').');
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
  // the confirmations' words (in place, never a dialog)
  function addressAsk(a) { return 'Pay ' + shortHex(a, 6, 4) + ' from the next block? The miner restarts.'; }
  function trustAsk(on) { return on ? 'Include proof records the node never checked? Devnet only; the node restarts.' : 'Verify proof records again? The node restarts.'; }
  return { PAGES: PAGES, page: page, withCommas: withCommas, compact: compact, rel: rel, ago: ago, shortHex: shortHex, kindWord: kindWord, shownCards: shownCards, present: present, cardTitle: cardTitle, cardRow: cardRow, rowWord: rowWord, tuneWord: tuneWord, tuneEta: tuneEta, tuningCard: tuningCard, poundsPerDay: poundsPerDay, money: money, effOf: effOf, effText: effText, wattsTotal: wattsTotal, noReading: noReading, cells: cells, nextCheck: nextCheck, tunePoint: tunePoint, tunable: tunable, tuneWords: tuneWords, schedule: schedule, GOALS: GOALS, goalWords: goalWords, toggle: toggle, nodeWords: nodeWords, nodeLine: nodeLine, skewWord: skewWord, peersLine: peersLine, heightLine: heightLine, nextSwitch: nextSwitch, switchLine: switchLine, proveWords: proveWords, verifierWords: verifierWords, proveTier: proveTier, proveLine: proveLine, devFeeText: devFeeText, devFeeLine: devFeeLine, jobsNote: jobsNote, eventLine: eventLine, addressAsk: addressAsk, trustAsk: trustAsk };
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
  if (params.get('screen')) forced = params.get('screen');
  if (params.get('page')) page = params.get('page'); // ?page=mine|earnings|prove|settings (screenshots)
  var forcedUpdate = params.get('update'); // a sample update state for screenshots: available|downloading|ready|waiting|applying|urgent|manual|error|updated
  var forcedJob = params.get('job'); // a sample remote job for screenshots: running|done|failed
  if (params.get('host') === 'mac') document.body.classList.add('mac');
  // ?debug=1 prints a budget line every 5 s (ms of JS per second in the canvas, the state render and the log) and
  // exposes the counters as window.__igneumPerf
  var debug = params.get('debug') === '1';
  // the hosts hide the window to the menu bar or the tray: nothing draws or polls fast while document.hidden.
  // ?visible=1 ignores that (screenshots from a hidden pane).
  var forceVisible = params.get('visible') === '1';
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
  function esc(s) { return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
  var compact = View.compact, withCommas = View.withCommas, rel = View.rel, kindWord = View.kindWord, shownCards = View.shownCards, cardTitle = View.cardTitle, money = View.money;
  function hms(s) { s = Math.max(0, Math.round(s)); var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60; return (h ? h + ':' : '') + (h ? String(m).padStart(2, '0') : m) + ':' + String(x).padStart(2, '0'); }
  function uptime(s) { var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60); return h ? h + ' h ' + m + ' min' : m + ' min'; }
  function clock(t) { var d = new Date(t * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0') + ':' + String(d.getSeconds()).padStart(2, '0'); }
  function setText(id, text) { var el = $(id); if (el && el.textContent !== text) el.textContent = text; }
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
    store('igneum.theme', t === 'system' ? null : t);
  }
  applyTheme(stored('igneum.theme') || 'system');
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
    if (dash) showPage(page);
    api('api/phase', { phase: p });
  }
  function showPage(id) {
    var meta = View.page(id);
    page = meta.id;
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
  $('btn-begin').addEventListener('click', function () { show('cards'); });
  $('btn-cards-next').addEventListener('click', function () {
    var choices = readCardRows($('cards-list'));
    if (choices.length) api('api/cards', { cards: choices }).then(function () { show('address'); }); else show('address');
  });
  $('btn-cards-retry').addEventListener('click', function () { cardsSeen = false; $('cards-detecting').hidden = false; api('api/detect', {}); });
  $('btn-address-back').addEventListener('click', function () { show('cards'); });

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
    }).catch(function () { $('btn-start').disabled = false; toast('The engine did not answer'); });
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
  disclosure('node-toggle', 'node-details');
  disclosure('pv-toggle', 'pv-details');
  disclosure('addr-toggle', 'addr-details', function () { $('s-address-input').focus(); });
  disclosure('key-toggle', 'key-details');
  $('s-jobs-history-btn').addEventListener('click', function () { setOpen(this, $('s-jobs-history'), $('s-jobs-history').hidden); $('s-jobs-key').hidden = $('s-jobs-history').hidden; });

  // ---------- Mine: the big button, the rows, the tune controls ----------
  $('btn-toggle').addEventListener('click', function () {
    if (!state) return;
    var t = View.toggle(state.mining, state.node, state);
    if (!t.act) return;
    api('api/' + t.act, {}).then(function () { toast(t.act === 'pause' ? 'Mining stops; the node keeps running' : 'Mining starts'); });
  });
  // a row's switch, slider or stepper sends the whole list to the engine (pending[key] holds a flipped switch until
  // the engine agrees, so the row does not flicker back)
  var pending = {}, cardsT = null, openCards = {};
  function sendCards(msg) {
    clearTimeout(cardsT);
    cardsT = setTimeout(function () {
      var choices = readCardRows($('d-cards'));
      if (choices.length) api('api/cards', { cards: choices }).then(function (r) { if (r.ok) toast(msg || 'Applied; only the changed worker restarts'); else toast(r.error || 'could not change'); });
    }, 400);
  }
  $('d-cards').addEventListener('change', function (e) {
    if (!state) return;
    if (e.target.matches('.gpu-line > .gl-main .switch input')) {
      var key = e.target.closest('.gpu-line').dataset.key, on = e.target.checked;
      pending[key] = { on: on, at: Date.now() };
      var choices = shownCards(state.mining.cards).filter(View.present).map(function (c) { var it = { key: c.key, enabled: c.key === key ? on : c.enabled, identities: c.identities || 1 }; if (c.vendor === 'nvidia' && c.power_default_w > 0) it.power_pct = c.power_pct || 80; return it; });
      api('api/cards', { cards: choices }).then(function (r) { if (r.ok) toast(on ? 'Worker starting on that card' : 'Worker stopped on that card'); else { delete pending[key]; toast(r.error || 'could not change'); } });
    } else if (e.target.matches('.ids input')) sendCards('Identities set; that card’s worker restarts');
  });
  $('d-cards').addEventListener('input', function (e) {
    if (e.target.matches('input[type=range]')) {
      var row = e.target.closest('.gpu-line'), cd = cardOf(row.dataset.key), pct = parseInt(e.target.value, 10);
      row.querySelector('.pv').textContent = pct + '% · ' + (cd && cd.power_default_w ? Math.round(cd.power_default_w * pct / 100) + ' W' : '');
      sendCards('Power cap set; pinned until you unpin it');
    }
  });
  $('d-cards').addEventListener('click', function (e) {
    var b = e.target.closest('button'); if (!b) return;
    var row = b.closest('.gpu-line'), key = row ? row.dataset.key : '';
    if (b.dataset.open !== undefined) { openCards[key] = !openCards[key]; applyOpen(row); }
    else if (b.dataset.tune !== undefined) api('api/sweep/start', { key: key }).then(function (r) { toast(r && r.ok === false ? (r.error || 'could not start the tune') : 'Tune queued for ' + (cardOf(key) || {}).name); });
    else if (b.dataset.stop !== undefined) api('api/sweep/stop', {}).then(function () { toast('Tune stopped; the card is back where it was'); });
    else if (b.dataset.d) { var inp = b.parentNode.querySelector('input'), v = parseInt(inp.value, 10) || 1; inp.value = Math.max(1, Math.min(64, v + parseInt(b.dataset.d, 10))); sendCards('Identities set; that card’s worker restarts'); }
    else if (b.dataset.pin !== undefined) api('api/sweep/pin', { key: key, pinned: b.dataset.pin === '1' }).then(function () { toast(b.dataset.pin === '1' ? 'Cap pinned' : 'The tune chooses the cap again'); });
    else if (b.dataset.powerRetry !== undefined) api('api/power/apply', {}).then(function () { toast('Administrator prompt: allow it to set the cap'); });
    else if (b.dataset.price !== undefined) { showPage('settings'); $('s-price').focus(); }
  });
  function applyOpen(row) { var open = !!openCards[row.dataset.key], det = row.querySelector('.gl-details'), chev = row.querySelector('[data-open]'); if (det) det.hidden = !open; if (chev) { chev.setAttribute('aria-expanded', open ? 'true' : 'false'); chev.classList.toggle('open', open); } row.classList.toggle('open', open); }
  function cardOf(key) { return state ? state.mining.cards.filter(function (c) { return c.key === key; })[0] : null; }
  $('btn-tune-all').addEventListener('click', function () {
    if (!state) return;
    var list = shownCards(state.mining.cards).filter(function (c) { return View.tunable(c) && c.enabled; });
    if (!list.length) { toast('No card to tune'); return; }
    var n = 0;
    list.reduce(function (p, c) { return p.then(function () { return api('api/sweep/start', { key: c.key }).then(function (r) { if (!r || r.ok !== false) n++; }); }); }, Promise.resolve()).then(function () { toast(n === 1 ? 'Tune queued for 1 card' : 'Tune queued for ' + n + ' cards, one at a time'); });
  });
  // the goal (ember-tune: POST api/tune/goal); a build without the route says so and keeps the old value
  function setGoal(goal) {
    api('api/tune/goal', { goal: goal }).then(function (r) { if (r && r.ok) { store('igneum.goal', goal); toast('Goal: ' + View.GOALS[goal]); } else toast('The goal needs the Ember Tune update'); if (state) renderTuneCard(state); }).catch(function () { toast('The goal needs the Ember Tune update'); });
  }
  ['goal-seg', 'goal-seg-2'].forEach(function (id) { $(id).addEventListener('click', function (e) { var b = e.target.closest('.seg-b'); if (b) setGoal(b.dataset.goal); }); });
  function powerControl(on) { api('api/power/control', { on: on }).then(function (r) { if (r.ok) toast(on ? 'Power control on: Windows asks for administrator rights once' : 'Power control off; nothing asks'); }); }
  $('m-power-control').addEventListener('change', function () { powerControl(this.checked); });
  $('s-power-control').addEventListener('change', function () { powerControl(this.checked); });
  $('m-clock-sync').addEventListener('click', function () { api('api/clock/sync', {}); });
  $('btn-open-log').addEventListener('click', function () { setDrawer(true); });

  // ---------- Earnings ----------
  $('s-devfee').addEventListener('change', function () { api('api/settings', { dev_fee: this.checked }).then(function (r) { if (r.ok) toast(r.restart ? 'Applied; the miner restarts' : 'Applied'); }); });
  $('e-price-btn').addEventListener('click', function () { showPage('settings'); $('s-price').focus(); });
  $('t-money-cell').addEventListener('click', function () { if ($('t-money').classList.contains('ask-price')) { showPage('settings'); $('s-price').focus(); } });
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
  $('r-wallet').addEventListener('click', function () { api('api/open', { url: 'https://igneum.network/wallet' }); });

  // ---------- Prove ----------
  $('s-prove').addEventListener('change', function () { var on = this.checked; api('api/prove', { on: on }).then(function (r) { if (r.ok) toast(on ? 'Proving on; the first piece arrives within a minute' : 'Proving off'); else { toast(r.error || 'could not change'); $('s-prove').checked = !on; } }); });
  $('pv-setup').addEventListener('click', function () { api('api/prove/setup', {}).then(function (r) { toast(r.ok ? 'Setup started in its own window' : (r.error || 'could not start')); }); });

  // ---------- Settings ----------
  $('s-update').addEventListener('click', function () { api('api/update/check', {}); setText('s-update-note', 'Checking.'); });
  $('s-install').addEventListener('click', function () { api('api/update/install', {}); toast('Installing at once'); });
  $('s-auto-update').addEventListener('change', function () { api('api/update/auto', { on: this.checked }); });
  $('s-jobs-check').addEventListener('click', function () { api('api/jobs/check', {}); setText('s-jobs-note', 'Checking.'); });
  $('s-name-save').addEventListener('click', function () { api('api/settings', { display_name: $('s-name').value }).then(function (r) { if (r.ok) toast('Renamed'); }); });
  $('s-vote').addEventListener('change', function () { api('api/settings', { vote: this.checked }).then(function (r) { if (r.ok) toast(r.restart ? 'Applied; the miner restarts' : 'Applied'); }); });
  $('s-login').addEventListener('change', function () { var on = this.checked; api('api/settings', { start_at_login: on }).then(function (r) { if (!r.ok) { toast(r.error || 'could not change'); $('s-login').checked = !on; } }); });
  $('s-jobs-allow').addEventListener('change', function () { api('api/jobs/allow', { on: this.checked }); });
  $('s-sweep').addEventListener('change', function () { api('api/sweep/enable', { on: this.checked }).then(function (r) { if (r.ok) toast($('s-sweep').checked ? 'Ember Tune on: once after install, then every 7 days' : 'Ember Tune off'); }); });
  $('s-climb').addEventListener('change', function () { var on = this.checked; api('api/tune/goal', { climb: on }).then(function (r) { if (!r || !r.ok) { toast('Needs the Ember Tune update'); $('s-climb').checked = !on; } }); });
  function savePrice() {
    var p = parseFloat($('s-price').value);
    if (!(p >= 0)) { toast('A price in pence per kWh, for example 28'); return; }
    store('igneum.price', p > 0 ? p : null);
    api('api/tune/goal', { price_pence: p }).then(function () { toast(p > 0 ? 'Price saved: ' + p + 'p per kWh' : 'Price cleared'); if (state) renderPage(state); }).catch(function () { toast(p > 0 ? 'Price saved on this window: ' + p + 'p per kWh' : 'Price cleared'); if (state) renderPage(state); });
  }
  $('s-price-save').addEventListener('click', savePrice);
  $('s-price').addEventListener('keydown', function (e) { if (e.key === 'Enter') savePrice(); });
  $('s-trust').addEventListener('change', function () { var on = this.checked; $('s-trust').checked = !on; $('ask-trust-text').textContent = View.trustAsk(on); $('ask-trust').hidden = false; $('ask-trust').dataset.on = on ? '1' : ''; });
  $('ask-trust-no').addEventListener('click', function () { $('ask-trust').hidden = true; });
  $('ask-trust-yes').addEventListener('click', function () {
    var on = $('ask-trust').dataset.on === '1'; $('ask-trust').hidden = true;
    api('api/settings', { proof_verify_trust: on }).then(function (r) { if (r.ok) { $('s-trust').checked = on; toast(on ? 'Trust mode on (devnet only); the node restarts' : 'Trust mode off; the node restarts'); } else toast(r.error || 'could not change'); });
  });
  $('s-live').addEventListener('click', function () { if (state && state.live_page) api('api/open', { url: state.live_page }); });
  $('s-log-open').addEventListener('click', function () { setDrawer(true); });
  // the log export: the last lines the engine holds (the ring keeps 3,000; one call returns up to 2,000), to the clipboard
  $('s-log-copy').addEventListener('click', function () {
    api('api/log?after=0&limit=2000').then(function (r) {
      var lines = (r.lines || []).map(function (l) { return clock(l.t) + ' ' + l.src.padEnd(6) + ' ' + l.text; });
      if (!lines.length) { toast('Nothing logged yet'); return; }
      copyText('Igneum Miner ' + (state ? state.version : '') + ' log, ' + lines.length + ' lines\n' + lines.join('\n'), 'Copied ' + lines.length + ' lines');
    }).catch(function () { toast('The engine did not answer'); });
  });

  // ---------- rail: logs and quit (the quit question is a strip, never a dialog) ----------
  $('btn-logs').addEventListener('click', function () { setDrawer(!document.body.classList.contains('drawer-open')); });
  $('log-close').addEventListener('click', function () { setDrawer(false); });
  $('btn-quit').addEventListener('click', function () { $('ask-quit').hidden = false; $('ask-quit-yes').focus(); });
  $('ask-quit-no').addEventListener('click', function () { $('ask-quit').hidden = true; });
  $('ask-quit-yes').addEventListener('click', function () { $('ask-quit').hidden = true; api('api/quit', {}); $('pill-text').textContent = 'stopping'; $('btn-quit').disabled = true; });

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
    $('btn-logs-text').textContent = open ? 'Close logs' : 'Logs';
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
      var msg = !state ? 'waiting for the engine' : state.mining.state === 'mining' ? 'mining: a block this machine finds appears here' : state.mining.state === 'paused' ? 'paused: no blocks while mining is stopped' : state.node.synced ? 'waiting for the worker' : 'waiting for the node to sync';
      x.fillText(msg, W / 2, rail - 10);
    }
    return animating && !reduce;
  }
  var stateAt = performance.now(), drawTimer = null, drawRaf = 0;
  function frame(now) {
    drawRaf = 0; clearTimeout(drawTimer); drawTimer = null;
    if (hiddenNow()) return;
    var fast = false;
    if (phase === 'dashboard' && page === 'mine') { var a = performance.now(); fast = draw(now); perf.draw += performance.now() - a; perf.frames++; }
    if (fast) drawRaf = requestAnimationFrame(frame);
    else drawTimer = setTimeout(function () { drawTimer = null; drawRaf = requestAnimationFrame(frame); }, reduce ? 1000 : 500);
  }
  function requestDraw() { if (hiddenNow() || drawRaf) return; clearTimeout(drawTimer); drawTimer = null; drawRaf = requestAnimationFrame(frame); }
  document.addEventListener('visibilitychange', function () {
    if (hiddenNow()) { clearTimeout(drawTimer); drawTimer = null; if (drawRaf) { cancelAnimationFrame(drawRaf); drawRaf = 0; } clearTimeout(logTimer); logTimer = null; }
    else { poll(); requestDraw(); if (drawerOpen()) { pollLog(); scheduleLogPoll(); } }
  });
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', function () { size(); });
  requestDraw();

  // ---------- render ----------
  function badgeHtml(cd) {
    if (cd.vendor === 'apple') return '<svg viewBox="0 0 24 24" width="22" height="22" fill="currentColor" aria-hidden="true"><path d="M16.4 12.6c0-2.5 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-2-1.5-.2-3 .9-3.8.9-.8 0-2-.9-3.3-.8-1.7 0-3.2 1-4.1 2.5-1.8 3-.5 7.6 1.3 10.1.9 1.2 1.9 2.6 3.2 2.5 1.3 0 1.8-.8 3.3-.8 1.6 0 2 .8 3.3.8 1.4 0 2.3-1.2 3.1-2.5 1-1.4 1.4-2.8 1.4-2.9 0 0-2.7-1-2.9-4.1zM13.9 5.3c.7-.8 1.2-2 1-3.2-1 0-2.2.7-2.9 1.5-.6.7-1.2 1.9-1 3 1.1.1 2.2-.5 2.9-1.3z"/></svg>';
    return cd.vendor === 'nvidia' ? 'NV' : cd.vendor === 'amd' ? 'AMD' : 'GPU';
  }
  function vramText(cd) { if (!cd.vram_mb) return ''; var gb = cd.vram_mb / 1024; return (gb >= 10 ? Math.round(gb) : gb.toFixed(gb < 4 ? 1 : 0)) + ' GB' + (cd.kind === 'apple' ? ' unified' : ''); }
  // ---------- the first-run GPU rows: one per card, with a switch ----------
  function renderCardRows(container, cards) {
    container.querySelectorAll('.gpu-row').forEach(function (n) { n.remove(); });
    shownCards(cards).forEach(function (cd) {
      var d = document.createElement('div');
      var removed = cd.removed_at > 0, unusable = !!cd.problem;
      d.className = 'gpu-row' + (cd.enabled && !removed && !unusable ? '' : ' off') + (removed ? ' removed' : '') + (unusable ? ' unusable' : '');
      d.dataset.key = cd.key;
      var meta = (vramText(cd) ? '<span>' + esc(vramText(cd)) + '</span>' : '') + (cd.detail && cd.kind !== 'apple' ? '<span>' + esc(cd.detail) + '</span>' : '') + (cd.detail && cd.kind === 'apple' ? '<span>' + esc(cd.detail.split(',')[0]) + '</span>' : '');
      d.innerHTML = '<div class="badge ' + esc(cd.vendor) + '">' + badgeHtml(cd) + '</div>' +
        '<div class="info"><div class="name" title="' + esc(cardTitle(cd)) + '">' + esc(cd.name) + (unusable ? ': ' + esc(cd.message || 'not usable (' + cd.problem + ')') : removed ? ': removed' : '') + ' <span class="kind ' + esc(cd.kind) + '">' + kindWord(cd.kind) + '</span></div><div class="meta">' + meta + '</div>' +
        (cd.reason ? '<div class="reason">' + esc(cd.reason) + '</div>' : '') + (cd.message && !unusable && !removed ? '<div class="msg">' + esc(cd.message) + '</div>' : '') + '</div>' +
        '<label class="switch" title="' + (unusable ? 'no worker can drive this card' : removed ? 'the card was removed' : cd.enabled ? 'mining on this card' : 'not mining on this card') + '"><input type="checkbox" aria-label="Mine on ' + esc(cd.name) + '"' + (cd.enabled && !removed && !unusable ? ' checked' : '') + (cd.kind === 'unknown' || unusable || removed ? ' disabled' : '') + '><span class="track"></span></label>';
      container.appendChild(d);
    });
  }
  // what a container's rows say now, for api/cards: a row without a stepper or a slider keeps the engine's value
  function readCardRows(container) {
    var out = [];
    container.querySelectorAll('.gpu-row, .gpu-line').forEach(function (r) {
      if (r.classList.contains('removed') || r.classList.contains('unusable')) return;
      var cd = cardOf(r.dataset.key) || {};
      var idsInput = r.querySelector('.ids input'), n = idsInput ? parseInt(idsInput.value, 10) : (cd.identities || 1);
      if (!(n >= 1)) n = 1; if (n > 64) n = 64;
      var sw = r.querySelector('.gl-main .switch input, :scope > .switch input'), pr = r.querySelector('input[type=range]');
      var item = { key: r.dataset.key, enabled: sw ? sw.checked : !!cd.enabled, identities: n };
      if (pr) item.power_pct = parseInt(pr.value, 10); else if (cd.vendor === 'nvidia' && cd.power_default_w > 0) item.power_pct = cd.power_pct || 80;
      out.push(item);
    });
    return out;
  }
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
      $('cards-sub').textContent = 'No graphics card this app can drive was found.';
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
  function numCell(cls, v, unit, k, tone) { return '<div class="num ' + cls + '"><span class="v' + (tone ? ' ' + tone : '') + '">' + esc(v) + (unit ? '<span class="unit">' + unit + '</span>' : '') + '</span>' + (k ? '<span class="k">' + esc(k) + '</span>' : '') + '</div>'; }
  function gpuLineHtml(cd, s, r) {
    var cells = View.cells(cd, priceNow()), tw = View.tuneWords(cd, s, s.now), word = View.rowWord(r, s.mining);
    var nums = '';
    if (r.hash) nums += numCell('hash', r.hash, 'MH/s', '', '');
    if (cells.power) nums += numCell('power', cells.power.replace(' W', ''), 'W', cells.eff, '');
    if (cells.money) nums += numCell('money', cells.money, '', 'a day', '');
    else if (cells.needsPrice) nums += '<div class="num money"><button class="btn tiny ghost" data-price="1">Set a price</button><span class="k">£ a day</span></div>';
    if (r.temp) nums += numCell('temp', r.temp.replace(' °C', ''), '°C', 'GPU', r.tempTone);
    var tuneBtn = tw.button === 'stop' ? '<button class="btn small" data-stop="1">Stop</button>' : tw.button === 'tune' ? '<button class="btn small ghost" data-tune="1">Tune</button>' : '';
    var sw = '<label class="switch" title="' + (r.on ? 'mining on this card' : 'not mining on this card') + '"><input type="checkbox" aria-label="Mine on ' + esc(r.name) + '"' + (r.on ? ' checked' : '') + (r.canToggle ? '' : ' disabled') + '><span class="track"></span></label>';
    var chev = '<button class="chev-btn" data-open="1" aria-expanded="false" aria-label="Details for ' + esc(r.name) + '"><span class="chev"></span></button>';
    var subs = [];
    if (r.sub) subs.push('<div class="msg' + (r.tone === 'bad' ? '' : ' dim') + '">' + esc(r.sub) + '</div>');
    if (cells.noReading && r.on) subs.push('<div class="msg dim">' + esc(cells.noReading) + '</div>');
    var tune = tw.kind === 'none' ? '' : '<div class="gl-tune ' + tw.kind + '"><span class="t">' + esc(tw.text) + '</span>' + (tw.note ? '<span class="n dim">' + esc(tw.note) + '</span>' : '') + (tw.progress >= 0 ? '<i class="bar"><b style="width:' + Math.round(tw.progress * 100) + '%"></b></i>' : '') + '</div>';
    return '<div class="gpu-line' + (r.on ? ' on' : ' off') + (r.removed ? ' removed' : '') + (r.unusable ? ' unusable' : '') + '" data-key="' + esc(r.key) + '">' +
      '<div class="gl-main"><div class="badge ' + esc(r.vendor) + '">' + badgeHtml({ vendor: r.vendor }) + '</div>' +
      '<div class="who"><div class="name" title="' + esc(r.title) + '">' + esc(r.name) + ' <span class="kind ' + esc(r.kind) + '">' + esc(r.kindWord) + '</span></div>' +
      '<span class="st ' + r.tone + '"><i></i>' + esc(word) + (r.meta ? ' · ' + esc(r.meta) : '') + '</span>' + subs.join('') + '</div>' +
      '<div class="nums">' + nums + '</div>' +
      '<div class="acts">' + tuneBtn + sw + chev + '</div></div>' + tune +
      '<div class="gl-details" hidden>' + detailsHtml(cd, s) + '</div></div>';
  }
  // the card's details (one chevron): the cap slider, identities, pin, the telemetry, the facts, the last tune's table
  function detailsHtml(cd, s) {
    var nv = cd.vendor === 'nvidia' && cd.power_default_w > 0, pct = cd.power_pct || 80, h = '';
    if (cd.removed_at > 0) return '<p class="help">The card was unplugged; its worker stopped. The row goes in five minutes.</p>';
    if (cd.problem) return '<p class="help">' + esc(cd.reason || 'No worker can drive this card.') + ' Reboot with the card attached; if it persists, reinstall the driver with the card attached.</p>';
    if (nv) {
      var want = Math.round(cd.power_default_w * pct / 100);
      h += '<div class="ctl"><span class="k">Power cap</span><input type="range" min="50" max="100" step="5" value="' + pct + '" aria-label="Power cap for ' + esc(cd.name) + '"><span class="pv mono">' + pct + '% · ' + want + ' W</span>' +
        '<p class="help">Lower is cooler and quieter; most cards hash nearly as fast at 70%. ' + (cd.pinned ? 'Set by hand, so the tune leaves it. <button class="btn tiny ghost" data-pin="0">Unpin</button>' : 'Ember Tune may move it.') + '</p></div>';
      h += cd.power_applied ? '<div class="line ok">cap applied: <b>' + want + ' W</b>' + (cd.sweep_pct === pct && cd.sweep_pct ? ' <span class="pin">chosen by the tune</span>' : '') + '</div>' : '<div class="line hot">cap not applied: Windows needs the administrator prompt <button class="btn tiny" data-power-retry="1">Retry</button></div>';
    } else if (cd.vendor === 'apple') h += '<p class="help">Apple silicon manages its own power; there is no cap to set.</p>';
    var tl = [];
    if (cd.temp_mem > 0) tl.push('memory <b>' + Math.round(cd.temp_mem) + ' °C</b>');
    if (cd.gclk_mhz > 0) tl.push('core clock <b>' + Math.round(cd.gclk_mhz) + ' MHz</b>' + (cd.clock_cap_mhz > 0 ? ' (cap ' + cd.clock_cap_mhz + ')' : ''));
    if (cd.mclk_mhz > 0) tl.push('memory clock <b>' + Math.round(cd.mclk_mhz) + ' MHz</b>' + (cd.mem_cap_mhz > 0 ? ' (set ' + cd.mem_cap_mhz + ')' : ''));
    if (cd.fan_pct > 0) tl.push('fan <b>' + Math.round(cd.fan_pct) + '%</b>');
    if (cd.util_pct > 0) tl.push('busy <b>' + Math.round(cd.util_pct) + '%</b>');
    if (tl.length) h += '<div class="line">' + tl.join('<span class="sep"></span>') + '</div>';
    var tm = TuneLine.model(cd, s.now);
    if (tm.kind === 'tuned' || tm.kind === 'measured') h += '<div class="line"><b>' + esc(tm.text) + '</b>' + (tm.note ? ' <span class="dim">' + esc(tm.note) + '</span>' : '') + '</div>';
    if (cd.tune_curve && cd.tune_curve.length) {
      h += '<table class="curve"><thead><tr><th>step</th><th>clock</th><th>power</th><th>MH/s</th><th>W</th><th>MH/W</th><th>note</th></tr></thead><tbody>' + cd.tune_curve.map(function (r, i) { return '<tr' + (r.mark ? ' class="marked"' : '') + '><td>' + (i + 1) + '</td><td>' + (r.clock_mhz ? r.clock_mhz + ' MHz' : 'unlocked') + '</td><td>' + (r.power_pct || 0) + '%</td><td>' + (r.mhs || 0).toFixed(1) + '</td><td>' + Math.round(r.watts || 0) + '</td><td>' + (r.eff || 0).toFixed(3) + '</td><td>' + esc(r.mark || '') + '</td></tr>'; }).join('') + '</tbody></table>';
    }
    h += '<div class="ctl"><span class="k">Identities</span><p class="help">Each identity votes and is paid on its own. 8 suits a big card, 2 a small one, 1 an integrated GPU. Changing it restarts that card’s worker.</p><div class="ids"><button type="button" data-d="-1" aria-label="fewer identities">&minus;</button><input type="number" min="1" max="64" value="' + (cd.identities || 1) + '" aria-label="Identities on ' + esc(cd.name) + '"><button type="button" data-d="1" aria-label="more identities">+</button></div></div>';
    var facts = [cd.worker ? 'worker ' + cd.worker : '', cd.detail || '', cardTitle(cd), cd.driver ? 'driver ' + cd.driver : ''].filter(Boolean);
    if (facts.length) h += '<p class="note mono small">' + esc(facts.join(' · ')) + '</p>';
    return h;
  }
  function renderMine(s) {
    var m = s.mining, n = s.node, p = s.program;
    var t = View.toggle(m, n, s), tb = $('btn-toggle');
    tb.className = 'toggle-big' + (t.cls ? ' ' + t.cls : ''); tb.disabled = t.disabled;
    setText('btn-toggle-text', t.label); setText('btn-toggle-sub', t.sub);
    var shown = shownCards(m.cards), presentCards = shown.filter(View.present);
    var mining = presentCards.filter(function (c) { return c.state === 'mining'; }).length;
    // the live total: rate, watts, £ a day, blocks
    setText('t-hash', m.hash_total >= 100 ? m.hash_total.toFixed(0) : m.hash_total.toFixed(1));
    setText('t-hash-sub', m.state === 'paused' ? 'paused' : mining ? (mining + ' of ' + presentCards.length + ' card' + (presentCards.length === 1 ? '' : 's') + ' mining') : m.state === 'waiting' ? (n.synced ? 'worker starting' : 'waiting for the node') : m.state === 'idle' ? 'no GPU worker' : m.state);
    var watts = View.wattsTotal(m.cards), price = priceNow(), eff = watts > 0 && m.hash_total > 0 ? View.effText(m.hash_total / watts) : '';
    setHidden('t-power-cell', !(watts > 0)); setText('t-power', String(Math.round(watts))); setText('t-power-sub', eff || 'drawn now');
    var ppd = View.poundsPerDay(watts, price);
    setHidden('t-money-cell', !(watts > 0)); setText('t-money', ppd === null ? 'Set a price' : money(ppd)); $('t-money').classList.toggle('ask-price', ppd === null); setText('t-money-sub', ppd === null ? 'in Settings, pence per kWh' : 'electricity at ' + price + 'p/kWh');
    setText('t-blocks', withCommas(m.accepted_session)); setText('t-blocks-sub', 'this run · ' + withCommas(m.accepted_total) + ' lifetime' + (m.rejected_session ? ' · ' + m.rejected_session + ' rejected' : ''));
    // the rows: every card, on or off; rebuilt only when their words change and never under a hand on a slider
    var rows = shown.map(View.cardRow), now = Date.now();
    rows.forEach(function (r) { var pd = pending[r.key]; if (pd) { if (pd.on === r.on || now - pd.at > 8000) delete pending[r.key]; else { r.on = pd.on; r.word = pd.on ? 'starting' : 'stopping'; r.tone = ''; } } });
    var on = rows.filter(function (r) { return r.on; }).length;
    setText('d-cards-eyebrow', s.detecting ? 'detecting' : !shown.length ? 'none found' : on + ' of ' + presentCards.length + ' on');
    var tunables = presentCards.filter(function (c) { return View.tunable(c) && c.enabled; });
    setHidden('btn-tune-all', !(tunables.length > 1) || !!(s.settings && s.settings.tuning_off) || !!View.tuningCard(presentCards));
    var box = $('d-cards'), busy = box.querySelector('input[type=range]:active') || (box.contains(document.activeElement) && document.activeElement.tagName === 'INPUT');
    var sig = JSON.stringify([rows, shown.map(function (c) { return [View.tuneWords(c, s, s.now), View.cells(c, price), c.identities, c.power_pct, c.pinned, c.power_applied, Math.round(c.temp_mem || 0), Math.round(c.gclk_mhz || 0), Math.round(c.mclk_mhz || 0), (c.tune_curve || []).length]; })]);
    if (sig !== mineRowsSig && !busy) {
      mineRowsSig = sig;
      box.innerHTML = rows.length ? shown.map(function (cd, i) { return gpuLineHtml(cd, s, rows[i]); }).join('') : '<div class="empty' + (s.detecting ? '' : ' err') + '">' + (s.detecting ? 'Asking the graphics cards to report in.' : 'No graphics card this app can drive was found. The node runs on its own.') + '</div>';
      box.querySelectorAll('.gpu-line').forEach(applyOpen);
    }
    var note = !shown.length && s.detect_message ? s.detect_message : '';
    $('d-cards-note').hidden = !note; setText('d-cards-note', note);
    renderTuneCard(s);
    renderNode(s);
    // activity: the counts, the strip, the newest 8 events in a user's words
    var nowU = s.now, f10 = m.found.filter(function (x) { return nowU - x <= 600; }).length;
    setText('d-stats', f10 + ' block' + (f10 === 1 ? '' : 's') + ' in 10 min · ' + m.found.length + ' in the last hour' + (m.fee_total ? ' · ' + withCommas(m.fee_total) + ' to the dev fee' : ''));
    var key = s.events.length ? s.events[0].t + ':' + s.events.length : '';
    if (key !== lastEventsKey) {
      lastEventsKey = key;
      $('d-events').innerHTML = s.events.length ? s.events.slice(0, 8).map(function (e) { return '<div><span><span class="k ' + esc(e.kind) + '">' + esc(e.kind) + '</span>' + esc(View.eventLine(e.text)) + '</span><span class="t">' + clock(e.t) + '</span></div>'; }).join('') : '<div class="empty">No events yet. The node and the miner report here as they start.</div>';
    }
  }
  // the Tuning card on Mine and the Tuning rows on Settings share the goal, the schedule and the Power control switch
  function renderTuneCard(s) {
    var st = s.settings || {}, goal = goalNow(), cards = shownCards(s.mining.cards), gw = View.goalWords(goal, cards, priceNow());
    ['goal-seg', 'goal-seg-2'].forEach(function (id) { document.querySelectorAll('#' + id + ' .seg-b').forEach(function (b) { var on = b.dataset.goal === goal; b.classList.toggle('on', on); b.setAttribute('aria-checked', on ? 'true' : 'false'); }); });
    setText('goal-line', gw.consequence); setText('goal-line-2', gw.consequence);
    var sched = View.schedule(s.mining.cards, s, s.now);
    setText('tune-schedule', sched); setText('s-tune-schedule', sched);
    setText('tune-eyebrow', st.tuning_off ? 'paused by Igneum' : st.sweep === false ? 'Ember Tune off' : 'Ember Tune');
    var nvOff = cards.some(function (c) { return c.vendor === 'nvidia' && View.present(c); }) && !st.power_control;
    setHidden('m-power-row', !nvOff);
    if (document.activeElement !== $('m-power-control')) $('m-power-control').checked = !!st.power_control;
  }

  // ---------- the node line and its details ----------
  var switchesSig = '';
  function renderNode(s) {
    var n = s.node, f = s.finality, p = s.program, eta = syncEta(n, s.now);
    var nl = View.nodeLine(n, s.clock, eta);
    setText('node-line-text', nl.text); setText('node-sub', nl.sub);
    $('node-dot').className = 'dot' + (nl.tone === 'ok' ? ' live' : nl.tone === 'bad' ? ' bad' : '');
    $('node-card').classList.toggle('bad', nl.tone === 'bad');
    setText('n-state-v', nl.text.replace(/^Node /, '')); setText('n-state-m', nl.sub + (n.override_restart_wait ? ' ' + n.override_restart_wait + '.' : ''));
    setText('n-blocks', withCommas(n.blocks)); setText('n-blocks-sub', View.heightLine(n));
    setText('n-peers', String(n.peers)); setText('n-peers-sub', View.peersLine(n));
    setText('n-version', n.version ? n.version.replace(/^igneumd\s*/, '') : '--');
    setText('d-node-net', s.chain || s.network || '');
    setText('n-headers', withCommas(n.headers)); setText('n-daa', withCommas(n.daa)); setText('n-diff', compact(n.difficulty)); setText('n-tips', String(n.tips)); setText('n-blue', withCommas(n.blue));
    if (n.daa > 0 && p && p.eta_s >= 0) { setText('d-eta', hms(p.eta_s)); setText('d-eta-sub', 'until the next hourly mining program' + (p.epoch_index ? ' (epoch ' + p.epoch_index + ')' : '') + (p.message ? ' · ' + p.message : '')); }
    else { setText('d-eta', '--:--'); setText('d-eta-sub', n.state === 'syncing' ? 'known after the node is synced' : 'known once the node runs'); }
    setText('n-digest', n.consensus_digest || (n.state === 'stopped' || n.state === 'failed' ? 'the node is not running' : 'not printed yet'));
    var sw = View.nextSwitch(n.consensus_switches, n.daa);
    setText('n-switch', sw ? sw.name + ' at ' + withCommas(sw.daa) : (n.consensus_switch_daa > 0 && n.consensus_switch_daa > n.daa ? 'DAA ' + withCommas(n.consensus_switch_daa) : (n.consensus_switches && n.consensus_switches.length ? 'none ahead' : 'none planned')));
    setText('n-switch-help', sw ? View.switchLine(sw, n.daa) : (n.consensus_switch_daa > 0 && n.consensus_switch_daa > n.daa ? 'The update manifest announces a switch at DAA ' + withCommas(n.consensus_switch_daa) + ' (' + withCommas(n.consensus_switch_daa - n.daa) + ' blocks away).' : View.switchLine(null, n.daa)));
    var sig = JSON.stringify([n.consensus_switches, sw && sw.key]);
    if (sig !== switchesSig) {
      switchesSig = sig;
      $('n-switches').innerHTML = (n.consensus_switches || []).map(function (x) { return '<span class="' + (sw && x.key === sw.key ? 'next' : x.daa <= n.daa ? 'past' : '') + '"><span>' + esc(x.name) + (x.daa <= n.daa ? ' (applied)' : '') + '</span><span>' + withCommas(x.daa) + '</span></span>'; }).join('');
    }
    if (f.last_lock > 0) { setText('f-lock', '#' + withCommas(f.last_lock) + ' · ' + rel(f.age_s)); setText('f-note', 'a point the miners agreed can never be undone; locks at 2/3 of the 30-day weight'); }
    else { setText('f-lock', 'none yet'); setText('f-note', f.message || 'locks appear once the miner votes on checkpoints'); }
    setText('f-votes', withCommas(f.votes));
  }

  // ---------- Earnings ----------
  function renderEarnings(s) {
    var a = s.address || {}, m = s.mining, pv = s.proving || {}, price = priceNow();
    var ign = pv.paid_wei && Number(pv.paid_wei) ? (Number(pv.paid_wei) / 1e18).toFixed(4) : '0.0000';
    setText('e-ign', ign + ' IGN'); setText('e-ign-sub', 'from ' + (pv.paid || 0) + ' proof' + (pv.paid === 1 ? '' : 's') + (pv.segments_paid ? ' and ' + pv.segments_paid + ' segment' + (pv.segments_paid === 1 ? '' : 's') : ''));
    setText('e-blocks', withCommas(m.accepted_total) + ' block' + (m.accepted_total === 1 ? '' : 's')); setText('e-blocks-sub', 'lifetime · ' + withCommas(m.accepted_session) + ' this run, ' + uptime(s.uptime_s));
    var watts = View.wattsTotal(m.cards), ppd = View.poundsPerDay(watts, price);
    setText('e-cost', ppd === null ? (watts > 0 ? Math.round(watts) + ' W' : 'no draw') : money(ppd) + ' a day');
    setText('e-cost-sub', ppd === null ? (watts > 0 ? 'electricity: set a price to see the cost' : 'electricity: no card reports its draw') : 'electricity at ' + price + 'p/kWh for ' + Math.round(watts) + ' W');
    setHidden('e-price-btn', !(watts > 0 && ppd === null));
    if (document.activeElement !== $('s-devfee')) $('s-devfee').checked = !!(s.settings && s.settings.dev_fee);
    setText('s-devfee-text', s.settings.dev_fee ? 'Dev fee: 1 block in 100 pays the miner software’s author' : 'Dev fee off: every block pays your address');
    setText('r-devfee-line', s.settings.dev_fee ? 'The protocol takes nothing; this is the miner software’s fee, the same way every GPU miner takes one' + (m.fee_total ? ' (' + withCommas(m.fee_total) + ' block' + (m.fee_total === 1 ? '' : 's') + ' so far)' : '') + '.' : 'The protocol takes nothing either.');
    setText('r-address', a.display || 'not set');
    setText('r-source', a.source === 'generated' ? 'made on this machine' : a.source === 'pasted' ? 'your own address' : '');
    setText('r-address-help', a.display ? 'Every block this machine finds pays this address.' : 'No address yet. Finish the setup to choose one.');
    var gen = a.source === 'generated';
    setHidden('key-toggle', !gen); if (!gen) { $('key-details').hidden = true; hideKey(); }
    setText('r-key-help', 'The key for this address was made on this machine and is stored in the app folder, readable by your user only. Keep a copy somewhere safe.');
    setText('r-key-file', gen && a.wallet_file ? a.wallet_file : '');
  }

  // ---------- Prove ----------
  var tiersSig = '';
  function renderProve(s) {
    var pv = s.proving || {}, enabled = !!(s.settings && s.settings.prove);
    if (document.activeElement !== $('s-prove')) $('s-prove').checked = enabled;
    var w = View.proveWords(pv, enabled, s.node.synced);
    var cards = shownCards(s.mining.cards).filter(View.present), tiers = cards.map(View.proveTier);
    var sig = tiers.join('|');
    if (sig !== tiersSig) { tiersSig = sig; $('pv-tiers').innerHTML = tiers.map(function (t) { return '<li>' + esc(t) + '</li>'; }).join(''); }
    setText('pv-line', View.proveLine(pv, enabled, s.node.synced));
    $('pv-line').className = 'line-text ' + (w.tone === 'on' || w.tone === 'ok' ? 'ok' : w.tone === 'bad' ? 'bad' : '');
    setText('pv-note', (!enabled && pv.default_note) ? pv.default_note + '.' : w.note);
    var v = View.verifierWords(pv);
    setText('pv-verifier', v.word); $('pv-verifier').className = 'v ' + v.tone;
    setText('pv-verifier-help', pv.verifier_note || 'the node checks a proof before it counts');
    $('pv-setup-row').hidden = !((pv.enabled && !pv.available && pv.setup_hint) || v.needsSetup);
    var seg = pv.segments_submitted ? pv.segments_submitted + ' proven whole, ' + (pv.segments_paid || 0) + ' paid' + (pv.segment_paid_wei && Number(pv.segment_paid_wei) ? ' (' + (Number(pv.segment_paid_wei) / 1e18).toFixed(4) + ' IGN)' : '') + (pv.segment_last_s ? ', the last in ' + Math.round(pv.segment_last_s) + ' s' : '') + (pv.segments_held ? ', ' + pv.segments_held + ' held for the chain rule' : '') : 'none yet';
    setText('pv-seg', seg); setText('pv-seg-note', pv.segment_note && enabled ? pv.segment_note : 'whole segments this machine proved');
    setText('pv-program', pv.program_id || 'not read yet');
    setText('pv-aggregator', pv.aggregator_id || 'not read yet');
  }

  // ---------- Settings ----------
  var jobsSig = '';
  function renderSettings(s) {
    var st = s.settings || {}, sw = function (id, on) { if (document.activeElement !== $(id)) $(id).checked = !!on; };
    sw('s-vote', st.vote); sw('s-login', st.start_at_login); sw('s-jobs-allow', s.jobs && s.jobs.allowed); sw('s-sweep', st.sweep); sw('s-trust', st.proof_verify_trust); sw('s-power-control', st.power_control); sw('s-climb', st.tune_climb);
    setHidden('s-climb-row', st.tune_climb === undefined);
    setText('s-power-note', st.tuning_off ? ' ' + (st.tuning_note || '') : (st.power_note ? ' ' + st.power_note : ''));
    if (document.activeElement !== $('s-price')) { var p = priceNow(); $('s-price').value = p > 0 ? String(p) : ''; }
    renderTuneCard(s);
    if (document.activeElement !== $('s-name')) $('s-name').value = s.display_name || '';
    setText('s-mid', 'machine id ' + (s.machine_id || '').slice(0, 8));
    setText('s-node-dir', 'chain data: ' + s.node_dir);
    setText('s-log-dir', 'log files: ' + s.log_dir);
    $('s-live').hidden = !s.live_page;
    var u = s.update || {};
    setText('s-version', 'Igneum Miner ' + s.version);
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
    if (page === 'mine') renderMine(s);
    else if (page === 'earnings') renderEarnings(s);
    else if (page === 'prove') renderProve(s);
    else if (page === 'settings') renderSettings(s);
  }
  function renderPill(s) {
    var pill = $('pill'), text;
    pill.classList.remove('on', 'warn');
    if (s.quitting) text = 'stopping';
    else if (!s.setup_done) text = s.detecting ? 'detecting' : 'set up';
    else if (View.tuningCard(s.mining.cards)) { text = 'tuning'; pill.classList.add('on'); }
    else if (s.mining.state === 'mining') { text = s.mining.hash_total.toFixed(1) + ' MH/s'; pill.classList.add('on'); }
    else if (s.mining.state === 'paused') text = 'paused';
    else if (s.node.state === 'syncing' || s.node.state === 'starting') text = 'node ' + s.node.state;
    else if (s.node.state === 'failed' || s.node.state === 'restarting') { text = 'node ' + s.node.state; pill.classList.add('warn'); }
    else if (s.mining.state === 'idle' && s.node.state === 'synced') { text = 'node synced'; pill.classList.add('on'); }
    else text = s.mining.state;
    $('pill-text').textContent = text;
    var foot = (s.display_name ? '<b title="' + esc(s.display_name) + '">' + esc(s.display_name) + '</b><br>' : '') + (s.address.display ? 'pays ' + esc(s.address.display.slice(0, 6) + '…' + s.address.display.slice(-4)) : 'no address yet') + '<br>up ' + uptime(s.uptime_s);
    if (foot !== lastFoot) { lastFoot = foot; $('rail-status').innerHTML = foot; }
    var fv = 'v' + esc(s.version) + '<span class="chain"> · ' + esc(s.chain || s.network || '') + '</span>';
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
      $('notice-detail').textContent = n.detail; $('notice-detail').hidden = !n.detail; $('notice-detail').title = n.detail;
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
    else if (u.status === 'current') parts.push('Up to date' + (u.checked_at ? ', checked ' + rel(state.now - u.checked_at) : '') + '.');
    else if (/no update manifest configured/.test(u.error || '')) parts.push('This build has no update address; updates come as a new download.');
    else if (u.error) parts.push(u.error);
    else if (!u.status || u.status === 'unknown') parts.push('Not checked yet. The engine checks by itself every hour.');
    if (u.activation_height && !u.urgent && state && state.node && u.activation_height > state.node.daa) parts.push('A rule switch comes at height ' + withCommas(u.activation_height) + '.');
    return parts.join(' ');
  }
  function sampleUpdate(kind) {
    // ?update=<kind>[&auto=0]: the card and the strip for each state, without an engine
    var u = { available: true, version: '0.3.14', notes: 'The miner has four sections: Mine, Earnings, Prove, Settings. Every card is a row with its rate, watts, £ a day and a Tune button. The node is one line. Updates are one card.', status: 'ready', downloaded: true, ready: true, applying: false, progress: 1, size: 20588331, auto: params.get('auto') !== '0', wait: 'installs at the next safe moment', urgent: false, urgent_text: '', activation_height: 0, error: '', updated_from: '', rolled_back: '' };
    if (!u.auto) u.wait = 'waiting for Install now (automatic updates are off)';
    if (kind === 'available') { u.status = 'available'; u.downloaded = false; u.ready = false; u.progress = 0; }
    if (kind === 'downloading') { u.status = 'downloading'; u.downloaded = false; u.ready = false; u.progress = Math.min(0.97, 0.43 + ((Date.now() / 1000) % 60) / 110); }
    if (kind === 'staging') { u.status = 'staging'; u.ready = false; u.progress = 1; }
    if (kind === 'waiting') { u.wait = 'hourly program boundary in 97 s; installing after it'; }
    if (kind === 'applying') { u.status = 'applying'; u.applying = true; }
    if (kind === 'deferred') { u.status = 'deferred'; u.wait = 'waits for the next time someone is at this PC (Windows asks for permission); mining continues'; }
    if (kind === 'urgent') { u.status = 'downloading'; u.ready = false; u.downloaded = false; u.progress = 0.27; u.urgent = true; u.activation_height = 120000; u.urgent_text = 'Consensus upgrade at height 120000 (difficulty v2): the node is 1,240 blocks away. Installing 0.3.14 now.'; }
    if (kind === 'manual') { u.status = 'manual'; u.ready = false; u.wait = '/Applications is not writable; open the downloaded disk image and drag the app over the old one'; }
    if (kind === 'error') { u.status = 'error'; u.error = 'sha256 mismatch: the file is not what the manifest signed'; u.downloaded = false; u.ready = false; }
    if (kind === 'updated') { u.status = 'current'; u.available = false; u.ready = false; u.downloaded = false; u.updated_from = '0.3.13'; u.version = '0.3.14'; }
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
    if (kind === 'tuned') { c.tune_line = 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)'; c.tune_source = 'full'; c.tune_control = true; c.tune_clock_mhz = 2470; c.sweep_pct = 100; c.sweep_at = s.now - 7200; c.sweep_watts = 290; }
    if (kind === 'measured') { c.tune_line = 'Tuned: 26.7 MH/s at 38 W (0.703 MH/W)'; c.tune_source = 'baseline'; c.tune_control = false; c.sweep_at = s.now - 600; c.sweep_note = 'measure only on Apple silicon: the system sets the clocks and the power; no control exposed'; }
  }
  function render(s) {
    state = s; stateAt = performance.now();
    if (forcedUpdate) { s.update = sampleUpdate(forcedUpdate); if (forcedUpdate === 'updated') { s.version = '0.3.14'; s.uptime_s = 20; } }
    if (forcedJob) s.jobs = sampleJob(forcedJob, s.now || 0);
    if (params.get('tune')) sampleTune(s, params.get('tune'));
    if (params.get('uptime')) s.uptime_s = parseInt(params.get('uptime'), 10) || 0; // the card's first-minute rule, for screenshots
    renderPill(s);
    renderClock(s);
    renderNotices(s);
    renderUpdateCard(s);
    if (phase === 'cards') renderCards(s);
    if (phase === 'dashboard') renderPage(s);
    if (s.quitting && !$('btn-quit').disabled) { $('btn-quit').disabled = true; }
  }
  var failures = 0, first = true, pollTick = 0;
  function poll() {
    // hidden windows poll every 5 s (the pill and the tray text still need a recent state), visible ones every second
    if (hiddenNow() && (pollTick++ % 5)) return;
    api('api/state').then(function (s) {
      var a = performance.now();
      failures = 0;
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
      perf.render += performance.now() - a; perf.polls++;
      if (failures === 0 && $('pill').classList.contains('away')) { $('pill').classList.remove('away', 'warn'); }
    }).catch(function () {
      failures++;
      if (failures >= 4) {
        $('pill').classList.add('warn', 'away'); $('pill-text').textContent = state && state.quitting ? 'stopped' : 'engine away';
        if (phase === 'dashboard') {
          setText('node-line-text', 'Node: no answer'); setText('node-sub', 'the engine is not answering; the window reconnects by itself'); $('node-card').classList.add('bad');
          setText('btn-toggle-sub', 'the engine is not answering'); $('btn-toggle').disabled = true;
          setText('t-hash-sub', 'no answer from the engine');
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
