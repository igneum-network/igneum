// Igneum Wallet window (the kept look, wallet-ui-3, on the new engine). Talks to the engine on the same origin
// (the token is in the path); polls /api/state every 2 s; never holds a key: words and keys only pass through when
// the signer shows them once. The first block, View, is pure (the words every page shows for a state) and can be
// loaded under node; the DOM block follows.
'use strict';

var View = (function () {
  var PAGES = [
    { id: 'home', title: 'Home', sub: 'Your balance, your address, the node' },
    { id: 'send', title: 'Send', sub: 'One payment, read in full before it is signed' },
    { id: 'receive', title: 'Receive', sub: 'Your address, two ways' },
    { id: 'history', title: 'History', sub: 'Every transaction, with how far it has settled' },
    { id: 'mining', title: 'Mining', sub: 'Your identity, your share of the draw, your pool receipts' },
    { id: 'settings', title: 'Settings', sub: 'Security, backup, settlement policy, the node' }
  ];
  function page(id) { for (var i = 0; i < PAGES.length; i++) if (PAGES[i].id === id) return PAGES[i]; return PAGES[0]; }
  function withCommas(n) { return (Number(n) || 0).toLocaleString('en-GB'); }
  // an IGN amount as the engine prints it (a plain decimal string) with its whole part grouped by thousands
  function groupIgn(a) { a = String(a || ''); var m = a.match(/^(-?)(\d+)(\.\d+)?$/); if (!m) return a; return m[1] + m[2].replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (m[3] || ''); }
  function shortHex(a, n, m) { n = n || 6; m = m || 4; return a && a.length > n + m + 2 ? a.slice(0, 2 + n) + '…' + a.slice(-m) : (a || ''); }
  function ago(s) { s = Math.max(0, Math.round(s || 0)); return s < 60 ? s + ' s ago' : s < 3600 ? Math.floor(s / 60) + ' min ago' : s < 86400 ? Math.floor(s / 3600) + ' h ago' : Math.floor(s / 86400) + ' d ago'; }
  function timeWord(t, now) {
    if (!t) return '';
    var d = new Date(t * 1000), n = new Date((now || Date.now() / 1000) * 1000);
    var hm = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    var same = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
    return same ? hm : d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) + ' ' + hm;
  }
  // the hero shows the number only when it is known; otherwise one line says why (never a 0 for an unknown)
  function balanceLine(s) {
    var n = s.node || {};
    if (s.balance_known) return { known: true, text: '' };
    if (n.state === 'ok') return { known: false, text: 'reading the balance' };
    return { known: false, text: 'waiting for a node' };
  }
  function moneyLine(s) {
    var net = s.network || {};
    return { money: '', text: '' };
  }
  // the node's state in a word and a tone (the design system, ADOPTION.md 5.2): met (green) when the head is fresh,
  // running (blue) while the node syncs or starts, bad (red) when the node is behind or gone; the words say which
  var FRESH_SECS = 180; // a dev chain under the sortition rule can wait out 17 rounds; past this the head is "behind"
  function nodeWords(n, nowUnix) {
    if (n.state === 'ok' && n.head_unix === 0) return { word: 'synced', tone: 'met' };
    if (n.state === 'ok') {
      var age = (nowUnix || Date.now() / 1000) - (n.head_unix || 0);
      if (!n.head_unix || age < FRESH_SECS) return { word: 'synced', tone: 'met' };
      return { word: 'behind', tone: 'bad' };
    }
    // "not connected", never "lost" (main, 12 October 2026: "lost" reads as a lost key)
    return { word: n.error ? 'not connected' : 'starting', tone: n.error ? 'bad' : 'live' };
  }
  function nodeLine(s) {
    var n = s.node || {}, w = nodeWords(n), net = s.network || {};
    if (n.state === 'ok') return { text: (n.own ? 'Your node' : 'Public endpoint') + ', block ' + withCommas(n.block), sub: netWord(net.name), tone: w.tone };
    return { text: 'Node ' + w.word, sub: n.error || '', tone: w.tone };
  }
  function nodeRows(s) {
    var n = s.node || {}, rows = [];
    rows.push(['endpoint', n.endpoint || '', n.trust || '', 'dim']);
    rows.push(['chain id', String(n.chain_id || ''), netWord((s.network || {}).name), 'dim']);
    rows.push(['head', withCommas(n.block || 0), n.head_unix ? 'seen ' + ago(Date.now() / 1000 - n.head_unix) : '', 'dim']);
    rows.push(['reorganisations', Settlement.reorgLine(n), 'what this wallet saw itself; the settlement policy reads it', 'dim']);
    return rows;
  }
  // the chain's word in prose (the direction of record, 15 October 2026: no chain names in prose): the wallet's pinned
  // default chain reads "the public test network"; a node on another chain keeps its name, a row of record
  // the home balance on ONE line at every width (the site lead's read of 0.0.21 at 390: the decimals had moved to a
  // second line as one piece): the figure keeps the sheet's size while it fits its box and shrinks by steps until it
  // does, down to 18 px; re-run on resize. The sheet's own sizes stay the ceiling (the style is cleared first).
  function fitBalance() {
    var b = document.getElementById('h-balance'); if (!b) return;
    var box = b.parentNode; b.style.fontSize = ''; b.style.whiteSpace = 'nowrap';
    var size = parseFloat(getComputedStyle(b).fontSize) || 30, unit = box.querySelector('.home-unit'), room = box.clientWidth - (unit ? unit.offsetWidth + 12 : 0);
    while (size > 18 && b.scrollWidth > room) { size -= 2; b.style.fontSize = size + 'px'; }
  }
  window.addEventListener('resize', function () { fitBalance(); });
  function netWord(name) { var n = String(name || ''); return /^Igneum public test network$/.test(n) ? 'the public test network' : /^Igneum old public test network$/.test(n) ? 'the old public test network' : n; }
  function welcomeEyebrow(s) {
    var net = s.network || {};
    // the re-seal mark belongs to a NAMED chain seen under another genesis (devnet5 after a re-seal); the nameless
    // entry ("chain 7310", a dev node with no pin) carries no mark (the page smoke on the dev node showed it there)
    var named = net.name && !/^chain \d+$/.test(net.name);
    var mark = named && net.genesis && net.genesis_pinned === false ? ' · re-sealed: its genesis is not this build’s pin' : (!named && net.name ? ' · not this build’s pin: a development node the wallet does not vouch for' : '');
    return (netWord(net.name) === 'the public test network' ? 'Igneum' : (net.name || 'Igneum')) + mark + ' · the public test network';
  }
  return { PAGES: PAGES, page: page, netWord: netWord, fitBalance: fitBalance, withCommas: withCommas, groupIgn: groupIgn, shortHex: shortHex, ago: ago, timeWord: timeWord, balanceLine: balanceLine, moneyLine: moneyLine, nodeLine: nodeLine, nodeRows: nodeRows, welcomeEyebrow: welcomeEyebrow };
})();

if (typeof module !== 'undefined') module.exports = View;
if (typeof document === 'undefined') { /* under node: only View */ } else (function () {
  var base = location.pathname.replace(/\/[^/]*$/, '/');
  function $(id) { return document.getElementById(id); }
  function esc(s) { return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }
  async function api(path, body) {
    var r = await fetch(base + path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    var j; try { j = await r.json(); } catch (e) { throw new Error('the engine did not answer'); }
    if (!r.ok) throw new Error(j.error || ('error ' + r.status));
    return j;
  }
  function post(path, body) { return api(path, body || {}); }

  var state = {}, page = 'home', phase = 'welcome', quote = null, txViews = {};

  // ---------- Touch ID and Windows Hello: the window host's bridge (the previous wallet's, OLD-BASE.md step 3) ----------
  // The page never confirms its own challenge: it hands a nonce to the host (the Mac's WKWebView message handler, the
  // PC's WebView2 bridge), the host shows the system prompt and posts the confirm to the engine with its own token,
  // and the page reads the outcome. A browser tab has no host: the controls say so and the password works.
  var bio = (function () {
    var pending = new Map(), seq = 0;
    var mac = !!(window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.biometric);
    var win = !!(window.chrome && window.chrome.webview);
    window.__igneumBiometric = function (id, r) { var p = pending.get(id); if (p) { pending.delete(id); p(r || { ok: false, code: 'bad', message: 'no answer' }); } };
    if (win) window.chrome.webview.addEventListener('message', function (ev) { var d = ev.data; if (typeof d === 'string') { try { d = JSON.parse(d); } catch (e) { return; } } if (d && d.id != null) window.__igneumBiometric(d.id, d); });
    return {
      host: mac ? 'mac' : win ? 'windows' : null,
      busy: false,
      call: function (op, p) {
        var self = this;
        return new Promise(function (res) {
          if (!self.host) return res({ ok: false, code: 'nohost', message: what() + ' needs the Igneum Wallet app window.' });
          var id = ++seq; pending.set(id, res); self.busy = true;
          var m = Object.assign({ id: id, op: op }, p || {});
          if (mac) window.webkit.messageHandlers.biometric.postMessage(m); else window.chrome.webview.postMessage(JSON.stringify(m));
          setTimeout(function () { if (pending.has(id)) { pending.delete(id); res({ ok: false, code: 'timeout', message: what() + ' did not answer.' }); } }, 180000);
        }).then(function (r) { self.busy = false; return r; });
      }
    };
  })();
  function what() { var k = state && state.biometric && state.biometric.kind; return k === 'hello' || (!k && /Win/.test(navigator.platform)) ? 'Windows Hello' : 'Touch ID'; }
  var FP = '<svg class="fp-glyph" width="16" height="16" aria-hidden="true"><use href="#i-fp"></use></svg>';
  function bioLine(r, okText) {
    if (!r) return '';
    if (r.ok) return '<span class="bio-state ok">' + FP + esc(what() + ' ' + (okText || 'confirmed')) + '</span>';
    var w = what(), c = r.code, t;
    if (c === 'cancelled' || c === 'fallback') t = w + ' cancelled, enter your password';
    else if (c === 'failed') t = w + ' did not match, try again or enter your password';
    else if (c === 'locked') t = w + ' is locked, enter your password';
    else if (c === 'invalidated') t = w + ' was turned off (a fingerprint changed), enter your password and turn it on again in Settings';
    else if (c === 'not_set_up' || c === 'unavailable') t = r.message || (w + ' is not set up on this machine');
    else if (c === 'nohost') t = w + ' needs the Igneum Wallet app window';
    else t = r.message || (w + ' did not succeed');
    return '<span class="bio-state">' + FP + esc(t) + '</span>';
  }
  function setLine(el, html) { el.innerHTML = html; }
  var params = new URLSearchParams(location.search);
  var forcedBio = params.get('bio') === 'touch';
  function hostHere() { return !!bio.host || forcedBio; }
  function bioSentence(b, w, host) {
    b = b || {};
    if (b.enrolled) return w + ' is on. It unlocks the wallet, confirms each send and shows the backup; the password still works everywhere.';
    if (!host) return w + ' needs the Igneum Wallet app window; this page is open in a browser.';
    if (!b.available) return b.message || (w + ' is not set up on this machine.');
    return 'Unlock, confirm each send and show the backup with ' + w + '. The password still works everywhere.';
  }
  function idleSentence(min, w) { return Number(min) > 0 ? 'Your key is cleared after ' + LockScreen.idleLabel(min) + ' without you; ' + w + ' or your password opens it again.' : 'The wallet stays open until you lock it.'; }

  // ---------- the lock screen (lock-screen.js: the layout, the line, the one automatic sheet on arrival) ----------
  var firstPhase = null, autoTimer = 0, pwRevealed = false, autoMem = { used: false };
  function lockLayout() {
    var s = forcedBio && state ? Object.assign({}, state, { biometric: Object.assign({}, state.biometric, { enrolled: true, available: true, kind: 'touchid' }) }) : state;
    return LockScreen.layout(Object.assign({}, s, { display: s.address }), hostHere());
  }
  function renderLock() {
    var lay = lockLayout();
    $('unlock-touch-text').textContent = 'Unlock with ' + what();
    $('unlock-bio').hidden = !lay.touch;
    document.querySelector('.lock-controls').classList.toggle('touch', lay.touch);
    $('unlock-form').hidden = !(!lay.touch || pwRevealed);
    $('unlock-use-pw').hidden = !lay.touch || pwRevealed;
  }
  function showPasswordField() { pwRevealed = true; renderLock(); $('unlock-pw').focus({ preventScroll: true }); }
  function hidePasswordField() { if (!lockLayout().touch) return; pwRevealed = false; $('unlock-pw').value = ''; $('unlock-err').textContent = ''; renderLock(); $('unlock-touch').focus({ preventScroll: true }); }
  function onLockScreen(reason) {
    clearTimeout(autoTimer);
    if (reason !== 'lock') { pwRevealed = false; $('unlock-pw').value = ''; $('unlock-err').textContent = ''; setLine($('unlock-bio-note'), ''); }
    renderLock();
    var lay = lockLayout();
    if (lay.touch && !pwRevealed) $('unlock-touch').focus({ preventScroll: true }); else setTimeout(function () { $('unlock-pw').focus(); }, 50);
    var d = LockScreen.autoPrompt({ reason: reason, phase: 'unlock', touch: lay.touch, askOnOpen: !!(state.settings && state.settings.ask_on_open), hidden: document.hidden, busy: bio.busy, firstPhase: firstPhase, updatedFrom: '' }, autoMem);
    if (reason === 'arrival') autoMem.used = true;
    if (d.prompt) autoTimer = setTimeout(function () { if (state && state.phase === 'unlock' && !document.hidden && !bio.busy && !pwRevealed) unlockWithTouch(); }, LockScreen.ENTER_MS + d.delay);
  }
  async function unlockWithTouch() {
    if (bio.busy) return;
    clearTimeout(autoTimer);
    $('unlock-touch').disabled = true; setLine($('unlock-bio-note'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
    var r = await bio.call('unlock');
    $('unlock-touch').disabled = false;
    var l = LockScreen.line(r, what());
    setLine($('unlock-bio-note'), l.text ? '<span class="bio-state ' + l.cls + '">' + FP + esc(l.text) + '</span>' : '');
    if (r.ok) { await poll(); showPage('home'); return; }
    if (l.clear) $('unlock-pw').value = '';
    if (l.password) showPasswordField(); else $('unlock-touch').focus({ preventScroll: true });
  }
  // the host's own password sheet (wallet-security's G1): in the app window "Use password" opens the host's native
  // secure field and the host posts the unlock itself, so the page never holds the password; a browser tab, with
  // no host, keeps the renderer's field
  async function unlockWithHostSheet() {
    if (bio.busy) return;
    clearTimeout(autoTimer);
    var r = await bio.call('password', { reason: 'Unlock your wallet' });
    if (r.ok) { await poll(); showPage('home'); return; }
    if (r.code === 'bad_op') { showPasswordField(); return; } // an older host without the sheet
    if (r.code !== 'cancelled') $('unlock-err').textContent = r.message || 'The window did not unlock.';
  }
  $('unlock-use-pw').onclick = function () { if (hostHere()) unlockWithHostSheet(); else showPasswordField(); };
  $('unlock-pw').addEventListener('keydown', function (e) { if (e.key === 'Escape') { e.preventDefault(); hidePasswordField(); } });
  $('unlock-touch').onclick = function () { unlockWithTouch(); };

  // ---------- phases and pages ----------
  function setPhase(p) {
    phase = p; document.body.dataset.phase = p;
    if (p === 'home') { setTimeout(sceneStart, 50); setTimeout(payoutsStart, 60); setTimeout(applyLaunchPage, 120); } else { sceneStop(); payoutsStop(); }
    ['welcome', 'create', 'import', 'unlock', 'home'].forEach(function (id) { var el = $('screen-' + id); if (el) el.hidden = id !== p; });
    $('rail').hidden = p !== 'home';
    document.body.classList.toggle('has-rail', p === 'home'); // the stylesheet offsets the top bar and the page by the rail on this class
    $('page-title').hidden = p !== 'home';
    $('top-brand').hidden = p === 'home';
    if (p === 'unlock') {
      $('unlock-address').textContent = state.address_short || '';
      // the first phase this page saw: 'unlock' means the app opened locked, the one case the sheet may rise by itself
      var reason = firstPhase === null ? 'arrival' : 'lock';
      if (firstPhase === null) firstPhase = p;
      onLockScreen(reason);
    } else if (firstPhase === null) {
      firstPhase = p;
    }
    if (p !== 'home') { $('bridge').hidden = true; }
  }
  // ---------- the scene: the address's payments arriving in blocks (api/payments, once a block) ----------
  var scene = null, sceneSince = null, sceneTimer = null;
  // the address's block payouts (api/payments: the last twenty blocks first, then each new block) kept for the
  // Home's activity rows now the scene is out of the wallet (the founder's design word, 14 October 2026)
  var payouts = [], payoutsHead = null;
  async function payoutsTick() {
    if (phase !== 'home') return;
    try {
      // a read by GET: the person's hand marks activity, a timer's read never (the updater's idle apply waits on it)
      var doc = await api('api/payments' + (payoutsHead == null ? '' : '?since=' + payoutsHead));
      if (doc && doc.head != null) {
        payoutsHead = doc.head;
        (doc.payments || []).forEach(function (p) { if (p.kind !== 'none' && Number(p.amount_ign) > 0 && !payouts.some(function (q) { return q.block === p.block; })) payouts.push(p); });
        payouts.sort(function (a, b) { return b.block - a.block; }); payouts = payouts.slice(0, 40);
        renderHistory();
      }
    } catch (e) {}
  }
  async function sceneTick() {
    refreshChainHistory();
    if (!scene || phase !== 'home') return;
    try {
      var doc = await api('api/payments' + (sceneSince == null ? '' : '?since=' + sceneSince));
      if (doc && doc.head != null) {
        var first = sceneSince == null;
        sceneSince = doc.head;
        var d = IgneumChainScene.sources.wallet(doc, doc.block_interval_s || 12);
        // the countdown's anchor and the clock's correction (the showcase's contract at its 18:40 sha, 12 October 2026)
        if (d && d.head) { if (doc.head_unix) d.head.at_unix = doc.head_unix; if (doc.server_unix) d.head.server_unix = doc.server_unix; }
        // the first answer with nothing for this address still draws the chain's last heights (main's read of the
        // 0.0.5 captures, 12 October 2026: an empty stage under "catching up" is the blank state the founder refused)
        if (first && d && !d.blocks.length) for (var h = Math.max(1, doc.head - 20); h <= doc.head; h++) d.blocks.push({ height: h, hash: '', unix: null, payments: [] });
        scene.push(d);
        sceneCaption(doc);
      }
    } catch (e) { if (sceneSince == null) sceneCaption(null, e.message); }
  }
  // the wallet's own caption (data-fixed on the scene's caption, so chain.js leaves it): "Reading the chain" until the
  // first answer, then the newest block in the wallet's voice
  // the caption's amount to three decimals (main's ordered form: "paid 0.070 IGN")
  // the activity row's amount to four decimals (main's audit of the 0.0.10 Home, 14 October 2026); the exact amount
  // and the wei on the row's hover and in History
  function ign4(a) { var s = String(a || '0'); var m = s.match(/^(-?\d+)(?:\.(\d+))?$/); if (!m) return s; var f = (m[2] || '').slice(0, 4).replace(/0+$/, ''); return m[1] + (f ? '.' + f : ''); }
  function ign3(a) { var n = Number(a); return isFinite(n) ? View.groupIgn(n.toFixed(3)) : String(a || ''); }
  function sceneCaption(doc, err) {
    var cap = $('scene') && $('scene').querySelector('.ig-scene-cap'); if (!cap) return;
    cap.dataset.fixed = '1';
    var span = cap.querySelector('span'); if (!span) return;
    // an unreachable or refusing endpoint is said in words, never as the transport's own message (the founder's rule)
    if (!doc) { span.textContent = err ? (/rate/i.test(err) ? 'The public endpoint is rate limited; reading again' : 'The public endpoint is not answering; reading again') : 'Reading the chain'; return; }
    var ps = (doc.payments || []).slice().sort(function (a, b) { return (b.block || 0) - (a.block || 0); });
    var paid = ps.filter(function (p) { return p.kind !== 'none' && Number(p.amount_ign) > 0; })[0];
    var newest = ps[0];
    if (paid && (!newest || paid.block === newest.block)) span.innerHTML = '<b>Block ' + View.withCommas(paid.block) + '</b> paid ' + esc(ign3(paid.amount_ign)) + ' IGN to this wallet';
    else if (newest) span.innerHTML = '<b>Block ' + View.withCommas(newest.block) + '</b> sealed, nothing for this wallet' + (paid ? '; block ' + View.withCommas(paid.block) + ' paid ' + esc(ign3(paid.amount_ign)) + ' IGN' : '');
    else span.innerHTML = '<b>Block ' + View.withCommas(doc.head) + '</b> sealed, nothing for this wallet yet';
  }
  // the watched address's live shares (ecosystem's per-address feed through the engine, api/shares, every 2 s): the
  // stage draws them landing on the newest block's row and tallies "N shares this block"; a 404 reads as none landing
  var sharesTimer = null;
  async function sharesTick() {
    if (!scene || phase !== 'home' || typeof scene.pushShares !== 'function') return;
    try { var doc = await api('api/shares'); if (doc && !doc.none && doc.head_height != null) scene.pushShares(doc); } catch (e) {}
  }
  function sceneStart() {
    if (scene || !window.IgneumChainScene || !$('scene')) return;
    // settle_first: the opening picture is drawn whole at once (the showcase's 0f0263c8; main's ruling for the apps)
    // the three-lane form of the site's stage (the founder's choice, 12 October 2026 evening; the showcase's app-lanes)
    scene = IgneumChainScene.mount($('scene'), { interval_s: 12, form: 'app', settle_first: true, label: 'Your payments arriving in blocks' });
    sceneCaption(null);
    sceneSince = null; sceneTick(); sceneTimer = setInterval(sceneTick, 12000);
    sharesTick(); sharesTimer = setInterval(sharesTick, 2000);
  }
  var payoutsTimer = null;
  function payoutsStart() { if (payoutsTimer) return; payoutsHead = null; payouts = []; payoutsTick(); payoutsTimer = setInterval(payoutsTick, 12000); refreshChainHistory(); }
  function payoutsStop() { if (payoutsTimer) clearInterval(payoutsTimer); payoutsTimer = null; }
  function sceneStop() {
    if (sceneTimer) clearInterval(sceneTimer); sceneTimer = null;
    if (sharesTimer) clearInterval(sharesTimer); sharesTimer = null;
    if (scene) { try { scene.destroy(); } catch (e) {} } scene = null; if ($('scene')) $('scene').innerHTML = '';
  }
  function showPage(id) {
    if (id === 'mining') setTimeout(readHolder, 50);
    page = id; document.body.dataset.page = id;
    var p = View.page(id);
    $('page-title-text').textContent = p.title; $('page-sub').textContent = p.sub;
    document.querySelectorAll('#rail-nav .nav').forEach(function (b) { var on = b.dataset.page === id; b.classList.toggle('on', on); if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current'); });
    document.querySelectorAll('#screen-home .page').forEach(function (s) { s.hidden = s.dataset.page !== id; });
    if (id === 'history') refreshHistoryViews();
  }
  document.querySelectorAll('#rail-nav .nav').forEach(function (b) { b.onclick = function () { showPage(b.dataset.page); }; });
  $('go-send').onclick = function () { showPage('send'); };
  $('go-receive').onclick = function () { showPage('receive'); };
  $('recent-all').onclick = function () { showPage('history'); };
  // the Live Work Ledger's wallet side: the miner view read again against the chain (api/ledger)
  $('ledger-go').onclick = async function () {
    $('ledger-err').textContent = ''; $('ledger-rows').innerHTML = ''; $('ledger-sum').hidden = true; $('ledger-identity').hidden = true;
    try {
      var L = await post('api/ledger', { view: $('ledger-view').value });
      $('ledger-sum').textContent = L.arrived_ign + ' IGN arrived over ' + L.rows.length + ' block' + (L.rows.length === 1 ? '' : 's') + ' for ' + L.address + (L.mine ? ' (this wallet)' : ' (another address)') + ': ' + L.reproduced + ' reproduced, ' + L.differs + ' differ; the view read at head ' + View.withCommas(L.view_head || 0) + ' (' + (L.read_at || '') + ').';
      $('ledger-sum').hidden = false;
      $('ledger-rows').innerHTML = L.rows.map(function (r) { return '<div class="hrow ' + (r.reproduced ? 'sent-row' : 'bad-row') + '"><span class="k">' + View.withCommas(r.height) + '</span><span class="v">' + esc(r.words) + '</span></div>'; }).join('') || '<p class="help">No block paid this address in the view.</p>';
      $('ledger-identity').textContent = L.identity + '. ' + L.source; $('ledger-identity').hidden = false;
    } catch (e) { $('ledger-err').textContent = e.message; }
  };
  $('backup-link').onclick = function (e) { e.preventDefault(); showPage('settings'); $('backup-details').hidden = false; };
  $('btn-lock').onclick = lock; $('btn-lock-top').onclick = lock;
  async function lock() { try { await post('api/lock'); await poll(); } catch (e) { toast(e.message); } }
  $('btn-quit').onclick = function () { $('ask-quit').hidden = false; };
  $('ask-quit-no').onclick = function () { $('ask-quit').hidden = true; };
  $('ask-quit-yes').onclick = async function () { try { await post('api/quit'); } catch (e) { /* the engine is going */ } $('ask-quit').hidden = true; };

  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-copy]'); if (!b) return;
    var t = $(b.dataset.copy); if (!t) return;
    navigator.clipboard.writeText(t.textContent).then(function () { toast('Copied'); }, function () { toast('Could not copy'); });
  });
  var toastTimer;
  function toast(text) { var t = $('toast'); t.textContent = text; t.hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(function () { t.hidden = true; }, 2200); }

  // ---------- state ----------
  async function poll() {
    try { state = await api('api/state'); } catch (e) { $('pill-text').textContent = 'Engine lost'; return; }
    render();
  }
  // the launch parameters (main's audit of 12 October 2026: the host passes IGNEUM_WALLET_OPEN and IGNEUM_WALLET_THEME as
  // ?open=<page>&theme=<dark|light> so an agent's capture of the running window shows each page in each theme)
  var launchQ = (function () { var q = {}; try { location.search.replace(/^\?/, '').split('&').forEach(function (kv) { var p = kv.split('='); if (p[0]) q[decodeURIComponent(p[0])] = decodeURIComponent(p[1] || ''); }); } catch (e) {} return q; })();
  var launchApplied = false, appliedTheme = null;
  function applyLaunch() {
    if (launchApplied || !state || !state.phase) return;
    launchApplied = true;
    if (launchQ.theme === 'dark' || launchQ.theme === 'light') document.documentElement.setAttribute('data-theme', launchQ.theme);
  }
  // the launch page opens once the home phase is on screen (setPhase), never before it: applied from render, the
  // first poll's setPhase('home') came after it and the page read home on every capture (the audit of 12 October 2026)
  var launchPageApplied = false;
  function applyLaunchPage() {
    if (launchPageApplied || phase !== 'home') return;
    launchPageApplied = true;
    if (launchQ.open && launchQ.open !== 'home' && View.page(launchQ.open).id === launchQ.open) showPage(launchQ.open);
  }
  function render() {
    if (state.identity && !identityKey && $('identity-make')) showIdentity(state.identity);
    applyLaunch();
    var s = state;
    // the poll never kicks a person out of a flow they are in: create and import run while the engine still says
    // welcome (or already says home), and the page moves on when the flow ends
    var inFlow = (phase === 'create' || phase === 'import') && (s.phase === 'welcome' || s.phase === 'home');
    if (phase !== s.phase && !inFlow) setPhase(s.phase);
    $('welcome-eyebrow').textContent = View.welcomeEyebrow(s);
    $('foot-version').textContent = 'v' + (s.version || '');
    $('s-version').textContent = 'engine v' + (s.version || '') + (s.signer_pid ? ', signer pid ' + s.signer_pid : '') + ', ' + (s.kind || '');
    var nl = View.nodeLine(s);
    $('pill-text').textContent = nl.text.replace(/^Node /, ''); $('pill').className = 'pill ' + nl.tone;
    // the short form at phone width: the block alone with the dot (the title keeps its row)
    var blk = (s.node || {}).block; $('pill-short').textContent = (s.node || {}).state === 'ok' && blk ? 'block ' + View.withCommas(blk) : nl.text.replace(/^Node /, '').split(',')[0];
    $('node-line-text').textContent = nl.text; $('node-sub').textContent = nl.sub; $('node-dot').className = 'dot ' + nl.tone;
    $('faucet-row').hidden = !(s.network && s.network.faucet) || s.phase !== 'home';
    $('faucet-go-home').hidden = $('faucet-row').hidden;
    var bf = !!(s.network && s.network.bond_faucet && s.network.faucet_relay) && s.phase === 'home';
    $('desk-faucet').hidden = !bf; $('desk-faucet-note').hidden = !bf;
    // the chain's explorer, when the chain names one: the address's page, a link the person opens in the browser
    var ex = s.network && s.network.explorer;
    $('explorer-row').hidden = !ex;
    if (ex) { $('explorer-link').href = ex.replace(/\/$/, '') + (s.address ? '/address/' + s.address : ''); $('explorer-link').textContent = ex.replace(/^https?:\/\//, ''); }
    $('node-kv').innerHTML = View.nodeRows(s).map(function (r) { return '<div class="k">' + esc(r[0]) + '</div><div class="v ' + esc(r[3]) + '">' + esc(r[1]) + '<span class="dim"> ' + esc(r[2]) + '</span></div>'; }).join('');
    $('rail-status').textContent = (s.node || {}).state === 'ok' ? 'block ' + View.withCommas(s.node.block) : '';
    var bl = View.balanceLine(s);
    // the hero figure to four decimals (the founder's ruling for 0.0.10, main's audit 14 October 2026); the full
    // figure on the figure's hover and in the Receive page's line
    // the integer part and the decimals as two spans, so the decimals shrink at phone width and the figure never clips
    var fig = bl.known ? View.groupIgn(ign4(s.balance_ign)) : '\u00b7', dot = fig.indexOf('.');
    $('h-balance').innerHTML = dot > 0 ? esc(fig.slice(0, dot)) + '<span class="frac">' + esc(fig.slice(dot)) + '</span>' : esc(fig); View.fitBalance(); $('h-balance').title = bl.known ? View.groupIgn(s.balance_ign) + ' IGN' : ''; $('h-balance-line').textContent = bl.text;
    var ml = View.moneyLine(s); $('h-money').textContent = ml.text;
    $('home-address').textContent = s.address || 'not set'; $('receive-address').textContent = s.address || 'not set';
    $('home-watch').hidden = !s.watch_only;
    $('backup-note').hidden = s.watch_only || !!s.backed_up;
    $('backup-card').hidden = !!s.watch_only;
    $('go-send').textContent = s.watch_only ? 'Prepare' : 'Send';
    $('nav-send-word').textContent = s.watch_only ? 'Prepare' : 'Send';
    // one word on every surface (main's 0.0.9 note): the page's own title follows the rail's word
    var sp = View.page('send'); if (sp) { sp.title = s.watch_only ? 'Prepare' : 'Send'; if (page === 'send') $('page-title-text').textContent = sp.title; }
    $('send-title').textContent = s.watch_only ? 'Prepare a payment' : 'Send IGN';
    $('send-eyebrow').textContent = s.watch_only ? 'watch-only: signed elsewhere' : '';
    $('send-balance').textContent = bl.known ? 'of ' + View.groupIgn(s.balance_ign) : '';
    $('ask-remove-text').textContent = s.watch_only ? 'Stop watching this address on this machine?' : 'Remove this wallet from this machine? The vault file is deleted. Only your 24 words or the key bring it back.';
    $('remove-pw').hidden = !!s.watch_only;
    if (s.settings) {
      $('idle-lock').value = String(s.settings.idle_lock_min);
      if (document.activeElement !== $('node-endpoint')) $('node-endpoint').value = s.settings.rpc || '';
      // a launch theme (the audit's capture) holds over the settings' choice for this run; the theme is applied when
      // it changes, not on every poll, so a capture's own switch of the theme stands between polls
      var wantTheme = launchQ.theme === 'dark' || launchQ.theme === 'light' ? launchQ.theme : (s.settings.theme || 'dark');
      if (wantTheme !== appliedTheme) { appliedTheme = wantTheme; applyTheme(wantTheme); }
    }
    var nsrc = (s.node || {}).source;
    $('node-eyebrow').textContent = ((s.node || {}).state === 'ok' ? 'connected' : 'not connected') + (nsrc ? ', ' + (nsrc === 'local' ? 'a node on this machine' : nsrc === 'public' ? 'the public endpoint' : 'the node you named') : '');
    $('node-source-line').textContent = (s.node || {}).trust || '';
    $('node-scan').textContent = (s.node || {}).scan || ''; $('node-scan').hidden = !(s.node || {}).scan;
    // Touch ID and Windows Hello: the row's words, the controls, the one-arrival switch, the idle sentence
    var b = s.biometric || {}, w = what(), host = hostHere();
    $('bio-title').textContent = w;
    $('bio-sentence').textContent = bioSentence(b, w, host);
    $('bio-toggle-btn').textContent = b.enrolled ? 'Turn off' : 'Turn on';
    $('bio-toggle-btn').disabled = !b.enrolled && (!host || !b.available);
    $('bio-toggle-btn').classList.toggle('danger', !!b.enrolled);
    if (b.enrolled) $('bio-enrol').hidden = true;
    $('bio-enrol-text').textContent = 'Turn on ' + w;
    $('ask-on-open-row').hidden = !b.enrolled;
    $('ask-on-open-text').textContent = 'Ask for ' + w + ' when the wallet opens';
    if (s.settings && document.activeElement !== $('ask-on-open')) $('ask-on-open').checked = !!s.settings.ask_on_open;
    $('idle-lock-note').textContent = s.settings ? idleSentence(s.settings.idle_lock_min, w) : '';
    if (b.needs_enrol && !$('bio-err').textContent) $('bio-err').textContent = 'The password changed, so ' + w + ' was turned off. Turn it on again here.';
    var touch = !!(b.enrolled && bio.host);
    $('backup-touch').hidden = !touch; $('backup-touch').querySelector('span').textContent = 'Show with ' + w;
    // the Updates card: the host's latest, the download, the open control
    var u = s.update || {};
    var uv = 'Igneum Wallet ' + (u.version || '');
    var ul = u.status === 'available' ? uv + ' is available.' + (u.auto ? ' Downloading it.' : '')
      : u.status === 'downloading' ? 'Downloading ' + uv + (u.size ? ' (' + Math.round(u.size / 1e6) + ' MB)' : '') + ': ' + Math.round((u.progress || 0) * 100) + '%'
      : u.applying ? 'Installing ' + uv + '. The wallet restarts.'
      : u.status === 'ready' ? uv + ' is downloaded and checked. ' + (u.auto ? 'It installs at the next idle moment.' : 'Open the download to install it.')
      : u.status === 'error' ? 'Update check failed: ' + (u.error || 'no reason given') + '. This install is untouched.'
      : u.status === 'current' && u.checked_unix ? 'Up to date (' + (u.current || s.version) + ').' : 'Not checked yet.';
    $('s-update-line').textContent = ul;
    $('s-update-sub').textContent = u.checked_unix ? 'Last check ' + View.ago(Date.now() / 1000 - u.checked_unix) + (u.signer ? ', signed by ' + u.signer.slice(0, 15) : '') + '.' : '';
    $('update-eyebrow').textContent = u.applying ? 'Installing' : u.status === 'ready' ? 'Ready' : u.status === 'downloading' ? 'Downloading' : u.status === 'available' ? 'Available' : u.status === 'error' ? 'Check failed' : '';
    var lg = (u.log || []).filter(function (l) { return /applied|apply failed|start /.test(l); });
    $('s-update-log').textContent = lg.length ? 'Log: ' + lg.slice(-2).map(logWords).join('; ') : '';
    $('s-open').hidden = u.status !== 'ready';
    if (s.settings && document.activeElement !== $('auto-update')) $('auto-update').checked = !!s.settings.auto_update;
    if (s.policy) {
      $('policy-eyebrow').textContent = s.policy.name || '';
      $('policy-kv').innerHTML = Settlement.policyLines(s.policy).map(function (l) { return '<div class="k">' + esc(l.k) + '</div><div class="v">' + esc(l.v) + '</div>'; }).join('');
      $('policy-source').textContent = (s.policy.source || '') + '. The words on every row come from this table and from what the node shows; nothing here is a network rule.';
      $('hist-policy').textContent = 'Rows read against the ' + (s.policy.name || '') + ' policy (Settings).';
    }
    renderHistory();
    drawQr(s.address);
    renderBridge(s);
    renderMining(s);
    renderUpdateCard(s);
  }

  // ---------- the update card (update-card.js: the model, when it shows, what it offers) ----------
  var updMem = { open: false, later: '', seen: '' }, updSig = '', updMore = false;
  function cardCtx(s) {
    return { phase: document.body.dataset.phase, view: document.body.dataset.phase === 'home' ? page : '', bioBusy: bio.busy, bioLine: '', bioName: what(), quitting: false };
  }
  function cardModel(s) {
    // this build downloads and offers the file; the apply comes with the hosts, so a ready download reads as the
    // model's "manual" stage (Open the download) and never as installing
    var u = Object.assign({}, s.update || {});
    if (u.status === 'ready') u.status = 'manual';
    if (u.status === 'current' || u.status === 'error') return null;
    u.notes = u.notes || '';
    return UpdateCard.model(u, { version: s.version, quitting: false });
  }
  function renderUpdateCard(s) {
    var m = cardModel(s), d = UpdateCard.decide(m, cardCtx(s), updMem), wrap = $('upd');
    if (!d.show) { if (updMem.open || !wrap.hidden) { updMem.open = false; wrap.hidden = true; updSig = ''; } return; }
    if (!updMem.open) { updMem.open = true; updMem.seen = m.version; updMore = false; }
    var sig = [m.key, m.stage, m.line, m.note, m.cause, m.ring, m.lines.join('|')].join('\u0001');
    if (sig !== updSig) {
      updSig = sig;
      $('upd-name').textContent = m.name;
      $('upd-line').textContent = m.line;
      $('upd-cause').textContent = m.cause || ''; $('upd-cause').hidden = !m.cause;
      $('upd-notes').innerHTML = m.lines.map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
      $('upd-all').innerHTML = m.all.map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
      $('upd-more').hidden = !m.more; if (!m.more) updMore = false;
      $('upd-more').textContent = updMore ? 'Less' : 'What changed'; $('upd-more').setAttribute('aria-expanded', updMore ? 'true' : 'false');
      $('upd-all').hidden = !updMore;
      $('upd-mark').className = 'upd-mark ' + m.ring;
      // "Install now" on an available update means the download in this build; the ready stage offers the file
      var acts = m.actions.map(function (a) { return a.act === 'install' ? { act: 'download', label: 'Download', primary: true } : a; });
      $('upd-actions').innerHTML = acts.map(function (a) { return '<button class="btn ' + (a.primary ? 'primary big' : 'ghost') + '" data-act="' + esc(a.act) + '"' + (a.disabled ? ' disabled' : '') + '>' + esc(a.label) + '</button>'; }).join('');
      wrap.dataset.dismiss = m.dismissable === false ? '' : '1';
    }
    if (m.ring === 'progress') { $('upd-arc').style.strokeDashoffset = (276.5 * (1 - m.pct / 100)).toFixed(1); $('upd-pct').textContent = m.pct + '%'; $('upd-pct').hidden = m.stage !== 'downloading'; }
    else { $('upd-arc').style.strokeDashoffset = ''; $('upd-pct').hidden = true; }
    var meta = [];
    if (m.stage === 'downloading' && m.size) meta.push(m.pct + '% of ' + m.size);
    else if (m.size && m.stage === 'available') meta.push(m.size + ' download');
    else if (m.size && m.stage === 'manual') meta.push(m.size + ', downloaded and checked against the release key');
    if (m.note) meta.push(m.note);
    $('upd-meta').textContent = meta.join(' · ');
    if (wrap.hidden) { wrap.hidden = false; $('upd-card').focus({ preventScroll: true }); }
  }
  function cardLater() {
    if (!updMem.open || !$('upd').dataset.dismiss) return;
    var m = cardModel(state || {});
    updMem.later = m ? m.key : updMem.later; updMem.open = false; $('upd').hidden = true; updSig = '';
  }
  $('upd').addEventListener('click', function (e) {
    if (e.target === $('upd')) { cardLater(); return; }
    var b = e.target.closest('[data-act]'); if (!b || b.disabled) return;
    var act = b.dataset.act;
    if (act === 'later') cardLater();
    else if (act === 'download' || act === 'retry') { post('api/update/download', {}).catch(function () {}); toast('Downloading the update'); updSig = ''; }
    else if (act === 'open') post('api/update/open', {}).catch(function (err) { toast(err.message); });
  });
  $('upd-more').onclick = function () { updMore = !updMore; $('upd-all').hidden = !updMore; this.textContent = updMore ? 'Less' : 'What changed'; this.setAttribute('aria-expanded', updMore ? 'true' : 'false'); };
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !$('upd').hidden) cardLater(); });

  // ---------- the page bridge: one ask at a time, answered here ----------
  var shownAsk = 0;
  function renderBridge(s) {
    var b = s.bridge || {}, pending = b.pending || [];
    $('bridge-on').checked = !!b.on;
    $('bridge-status').textContent = b.on ? ('on 127.0.0.1:' + b.port + (b.sites && b.sites.length ? ', ' + b.sites.length + ' site' + (b.sites.length === 1 ? '' : 's') : ', no site yet')) : 'off: every request is refused';
    $('sites').innerHTML = (b.sites || []).map(function (site) { return '<div class="srow"><div class="sw-text"><b>' + esc(site.origin) + '</b> <span class="dim">since ' + esc(View.timeWord(site.connected_unix)) + '</span></div><div class="row"><button class="btn small ghost" data-disconnect="' + esc(site.origin) + '">Disconnect</button></div></div>'; }).join('');
    // the asks the cap refused before the window saw them (one open ask per site, the whole queue bounded)
    var refused = b.refused || [];
    $('sites-refused').hidden = !refused.length;
    $('sites-refused').textContent = refused.length ? 'Refused by the cap: ' + refused.map(function (r) { return r.host + ' ' + r.count; }).join(', ') + ' (a site gets one open ask at a time; the rest read a refusal at once).' : '';
    var waiting = pending.length ? pending[0].host + ' is waiting for an answer' : '';
    $('unlock-bridge').textContent = waiting; $('unlock-bridge').hidden = !waiting;
    if (phase !== 'home' || !pending.length) { $('bridge').hidden = true; shownAsk = 0; return; }
    var a = pending[0];
    if (shownAsk === a.id && !$('bridge').hidden) return;
    shownAsk = a.id;
    $('bridge-title').textContent = a.title;
    $('bridge-lines').innerHTML = (a.lines || []).map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
    $('bridge-origin').textContent = a.origin + (pending.length > 1 ? ' (' + (pending.length - 1) + ' more waiting)' : '');
    $('bridge-yes-text').textContent = a.yes || 'Yes';
    $('bridge-note').textContent = a.ask && a.ask.kind === 'send_transaction' ? 'Nothing is signed until you press ' + (a.yes || 'Yes') + '.' : '';
    $('bridge').hidden = false; $('bridge-card').focus();
  }
  async function decide(yes) {
    if (!shownAsk) return;
    var id = shownAsk; shownAsk = 0; $('bridge').hidden = true;
    try { await post('api/bridge/decide', { id: id, yes: yes }); } catch (e) { toast(e.message); }
    poll();
  }
  $('bridge-yes').onclick = function () { decide(true); };
  $('bridge-no').onclick = function () { decide(false); };
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !$('bridge').hidden) decide(false); });
  $('bridge-on').onchange = async function () { try { await post('api/bridge/on', { on: $('bridge-on').checked }); } catch (e) { toast(e.message); } poll(); };
  document.addEventListener('click', async function (e) {
    var b = e.target.closest('[data-disconnect]'); if (!b) return;
    try { await post('api/bridge/sites/remove', { origin: b.dataset.disconnect }); toast('Disconnected'); } catch (err) { toast(err.message); }
    poll();
  });

  // the simulation's sentences with their stops: "Simulation: expected to succeed. The node serves no trace, so ..."
  function simWords(sim) {
    var cap = function (t) { t = String(t || '').trim(); return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''; };
    var stop = function (t) { return /[.!?]$/.test(t) ? t : t + '.'; };
    var parts = [stop(cap(sim.text))].concat((sim.lines || []).map(function (l) { return stop(cap(l)); }));
    return (sim.ok ? 'Simulation: ' : '') + parts.join(' ');
  }
  // an updater log line in the app's clock words: the ISO stamp becomes the local time (and the date when not today)
  function logWords(line) {
    var m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}):\d{2}Z\s+(.*)$/.exec(line);
    if (!m) return line;
    var t = Date.parse(m[1] + 'T' + m[2] + ':00Z'); if (isNaN(t)) return line;
    var d = new Date(t), now = new Date();
    // the page's clock form (the History rows' 24-hour time), the date before it when not today
    var hm = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    var same = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
    return (same ? hm : d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) + ' ' + hm) + ', ' + m[3];
  }
  // ---------- history with the settlement words ----------
  function histRow(h) {
    var v = txViews[h.hash], r = Settlement.row(v);
    return '<div class="hrow"><div class="hl"><span class="hw ' + esc(r.tone) + '">' + esc(r.word) + '</span> <span class="dim">' + esc(View.timeWord(h.sent_unix)) + '</span></div>' +
      '<div class="hm"><span class="mono" title="' + esc(h.value_ign) + ' IGN">' + esc(View.groupIgn(ign4(h.value_ign))) + ' IGN</span> to <span class="mono">' + esc(View.shortHex(h.to)) + '</span></div>' +
      '<div class="hy dim">' + esc(r.why) + '</div><div class="hh mono dim">' + esc(View.shortHex(h.hash, 10, 6)) + '</div></div>';
  }
  // the chain's ledger for this address (api/history/chain: the explorer's transaction list), read every 20 s and
  // right after a send; merged with the engine's own records by hash, newest first
  var chainHist = [], chainHistAt = 0, chainHistErr = '';
  async function refreshChainHistory(force) {
    if (!state.address || state.phase !== 'home') return;
    if (!force && Date.now() - chainHistAt < 20000) return;
    chainHistAt = Date.now();
    try { var r = await post('api/history/chain', {}); chainHist = r.entries || []; chainHistErr = ''; renderHistory(); }
    catch (e) { chainHistErr = e.message; renderHistory(); }
  }
  function chainRow(c) {
    var word = c.kind === 'sent' ? 'Sent' : c.kind === 'received' ? 'Received' : 'To itself', tone = c.ok ? (c.kind === 'received' ? 'ok' : 'done') : 'bad';
    var other = (c.kind === 'received' ? 'from ' + View.shortHex(c.from) : 'to ' + View.shortHex(c.to)) + (c.internal && c.words ? ', ' + c.words : '');
    return '<div class="hrow"><div class="hl"><span class="hw ' + tone + '">' + esc(word) + '</span> <span class="dim">' + esc(View.timeWord(c.unix)) + '</span></div>' +
      '<div class="hm"><span class="mono" title="' + esc(c.value_ign) + ' IGN">' + esc(View.groupIgn(ign4(c.value_ign))) + ' IGN</span> ' + esc(other) + '</div>' +
      '<div class="hy dim">' + (c.ok ? 'Included in block ' + Number(c.block).toLocaleString('en-GB') + ', read from the chain' : 'Failed on the chain in block ' + Number(c.block).toLocaleString('en-GB')) + '</div><div class="hh mono dim">' + esc(View.shortHex(c.hash, 10, 6)) + '</div></div>';
  }
  function renderHistory() {
    var local = state.history || [], seen = {};
    local.forEach(function (h) { seen[String(h.hash).toLowerCase()] = true; });
    var fromLedger = chainHist.filter(function (c) { return !seen[String(c.hash).toLowerCase()]; });
    var rows = local.map(function (h) { return { unix: h.sent_unix || 0, html: histRow(h) }; }).concat(fromLedger.map(function (c) { return { unix: c.unix || 0, html: chainRow(c) }; }));
    rows.sort(function (a, b) { return b.unix - a.unix; });
    var hist = local;
    var empty = '<p class="help">Nothing sent from or to this address yet.' + (chainHistErr ? ' The chain could not be read: ' + esc(chainHistErr) + '.' : '') + '</p>';
    // the Home's activity: the last five movements in a quiet row each (the word, the other side, the time, the amount
    // exact to the wei as the chain gives it; the founder's design word, 14 October 2026)
    var acts = local.map(function (h) { var v = txViews[h.hash], r = Settlement.row(v); return { unix: h.sent_unix || 0, word: r.word, tone: r.tone, other: 'to ' + View.shortHex(h.to), amount: h.value_ign, wei: h.value_wei, out: true }; })
      .concat(fromLedger.map(function (c) { return { unix: c.unix || 0, word: c.kind === 'sent' ? 'Sent' : c.kind === 'received' ? 'Received' : 'To itself', tone: c.ok ? (c.kind === 'received' ? 'ok' : 'done') : 'bad', other: c.kind === 'received' ? 'from ' + View.shortHex(c.from) : 'to ' + View.shortHex(c.to), amount: c.value_ign, wei: c.value_wei, out: c.kind !== 'received' }; }));
    payouts.forEach(function (p) { acts.push({ unix: p.unix || 0, word: p.kind === 'seal' ? 'Sealed' : p.kind === 'work' ? 'Paid for work' : p.kind === 'transfer' ? 'Received' : 'Block reward', tone: 'ok', other: 'block ' + Number(p.block).toLocaleString('en-GB'), amount: p.amount_ign, wei: p.amount_wei, out: false }); });
    acts.sort(function (a, b) { return (b.unix || 0) - (a.unix || 0) || (b.block || 0) - (a.block || 0); });
    $('recent').innerHTML = acts.slice(0, 5).map(function (a) {
      return '<div class="arow' + (a.out ? '' : ' in') + '"><span class="ar-word ' + esc(a.tone) + '">' + esc(a.word) + '</span><span class="ar-other mono">' + esc(a.other) + '</span><span class="ar-when dim">' + esc(View.timeWord(a.unix)) + '</span><span class="ar-amt" title="' + esc((a.amount || '') + ' IGN' + (a.wei ? ' = ' + a.wei + ' wei' : '')) + '">' + (a.out ? '\u2212' : '+') + esc(View.groupIgn(ign4(a.amount))) + '<span class="unit">IGN</span></span></div>';
    }).join('') || '<p class="help">Nothing sent from or to this address yet.' + (chainHistErr ? ' The chain could not be read.' : '') + '</p>';
    $('history').innerHTML = rows.map(function (r) { return r.html; }).join('') || empty;
    var sentHere = hist.filter(function (h) { return !h.from_scan; }).length, fromChain = hist.length - sentHere + fromLedger.length;
    // the scan's own line while the chain is still being read for this address (a restore reads it from the start;
    // nothing else said so until the node line did, the study's first participant, 12 October 2026)
    var scanning = state.scan && /^reading the chain/.test(state.scan) ? state.scan : '';
    $('hist-eyebrow').textContent = scanning || (hist.length ? (sentHere + ' sent from here' + (fromChain ? ', ' + fromChain + ' read from the chain' : '')) : '');
  }
  async function refreshHistoryViews() {
    var hist = state.history || [];
    for (var i = 0; i < Math.min(hist.length, 20); i++) {
      try { var v = await api('api/tx/' + hist[i].hash); txViews[hist[i].hash] = v.view; } catch (e) { /* the row keeps its last words */ }
    }
    renderHistory();
  }

  // ---------- create ----------
  $('go-create').onclick = function () { setPhase('create'); $('create-1').hidden = false; $('create-2').hidden = true; $('create-3').hidden = true; $('create-pw').focus(); };
  $('create-back').onclick = function () { setPhase('welcome'); };
  // the password meter (ST-4, main's ruling of 12 October 2026): the same arithmetic as the engine's
  // igneum_wallet_core::password (the pool from the classes seen, the length with repeats and steps counted a
  // quarter, a doubled half halved; under 40 bits weak, under 60 fair, else strong), the minimum of twelve with the
  // count, the common list by name from common-passwords.txt beside the page (empty until the file ships), and
  // the one sentence on what the password protects
  var PW_MIN = 12, PW_SENTENCE = 'The password protects the file on this machine only and never the coins on the chain.';
  var commonPasswords = null;
  fetch('common-passwords.txt').then(function (r) { return r.ok ? r.text() : ''; }).then(function (t) {
    commonPasswords = {}; t.split('\n').forEach(function (l) { l = l.trim().toLowerCase(); if (l && l[0] !== '#') commonPasswords[l] = true; });
  }).catch(function () { commonPasswords = {}; });
  // the signer's memory lock as the state reports it (main's rule of 12 October 2026: a refused lock degrades and
  // names itself): one sentence on the review when it is not the full lock
  function memoryLockSentence() {
    var ml = state && state.signer_process && state.signer_process.memory_lock;
    if (!ml || ml.mode === 'all') return '';
    var lim = ml.limit_kib == null ? 'no limit read' : "the host's limit " + Math.round(ml.limit_kib / 1024) + ' MiB';
    return (ml.mode === 'current' ? ' Memory lock: the key pages only (' : ' Memory lock: none (') + lim + '); the key can be paged out under pressure on this machine.';
  }
  function passwordMeter(pw) {
    var chars = Array.from(pw);
    if (!chars.length) return { bits: 0, grade: 'weak', count: 0 };
    var lower = false, upper = false, digit = false, other = false;
    chars.forEach(function (c) { if (/[a-z]/.test(c)) lower = true; else if (/[A-Z]/.test(c)) upper = true; else if (/[0-9]/.test(c)) digit = true; else other = true; });
    var pool = Math.max(10, (lower ? 26 : 0) + (upper ? 26 : 0) + (digit ? 10 : 0) + (other ? 33 : 0));
    var eff = 1;
    for (var i = 1; i < chars.length; i++) { var a = chars[i - 1].codePointAt(0), b = chars[i].codePointAt(0); eff += (b === a || b === a + 1 || b === a - 1) ? 0.25 : 1; }
    var n = chars.length;
    if (n >= 4 && n % 2 === 0 && chars.slice(0, n / 2).join('') === chars.slice(n / 2).join('')) eff /= 2;
    var bits = Math.max(0, Math.round(eff * Math.log2(pool)));
    return { bits: bits, grade: bits < 40 ? 'weak' : bits < 60 ? 'fair' : 'strong', count: n };
  }
  function passwordWords(pw) {
    var m = passwordMeter(pw);
    if (!m.count) return PW_SENTENCE;
    if (m.count < PW_MIN) return m.count + ' of at least ' + PW_MIN + ' characters. ' + PW_SENTENCE;
    if (oneRepeated(pw)) return 'That password is one character repeated; choose a less guessable one. ' + PW_SENTENCE;
    if (commonPasswords && commonPasswords[pw.trim().toLowerCase()]) return 'That password is on a list of common passwords. Choose another. ' + PW_SENTENCE;
    return 'Strength: ' + m.grade + ' (about ' + m.bits + ' bits). ' + PW_SENTENCE;
  }
  function oneRepeated(pw) { var a = Array.from(pw); return a.length > 0 && a.every(function (c) { return c === a[0]; }); }
  function passwordRefusal(pw) {
    var m = passwordMeter(pw);
    if (m.count < PW_MIN) return 'The password is ' + m.count + ' characters; at least ' + PW_MIN + '.';
    if (oneRepeated(pw)) return 'That password is one character repeated; choose a less guessable one.';
    if (commonPasswords && commonPasswords[pw.trim().toLowerCase()]) return 'That password is on a list of common passwords. Choose another.';
    return '';
  }
  $('create-pw').oninput = function () { $('create-meter').textContent = passwordWords($('create-pw').value); };
  $('import-pw').oninput = function () { $('import-meter').textContent = passwordWords($('import-pw').value); };
  $('create-meter').textContent = PW_SENTENCE; $('import-meter').textContent = PW_SENTENCE;
  $('create-next').onclick = async function () {
    var a = $('create-pw').value, b = $('create-pw2').value; $('create-err').textContent = '';
    var refusal = passwordRefusal(a);
    if (refusal) { $('create-err').textContent = refusal; return; }
    if (a !== b) { $('create-err').textContent = 'The two passwords differ.'; return; }
    try {
      var r = await post('api/create', { password: a });
      $('create-pw').value = ''; $('create-pw2').value = '';
      $('words').innerHTML = r.words.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('');
      $('create-address').textContent = r.address;
      $('create-1').hidden = true; $('create-2').hidden = false;
    } catch (e) { $('create-err').textContent = e.message; }
  };
  var checkIdx = [];
  $('create-written').onclick = function () {
    var words = Array.prototype.map.call($('words').children, function (li) { return li.textContent; });
    checkIdx = []; while (checkIdx.length < 3) { var i = Math.floor(Math.random() * words.length); if (checkIdx.indexOf(i) < 0) checkIdx.push(i); }
    checkIdx.sort(function (x, y) { return x - y; });
    $('checks').innerHTML = checkIdx.map(function (i) { return '<label class="field"><span class="k">word ' + (i + 1) + '</span><input type="text" data-idx="' + i + '" autocomplete="off" autocapitalize="off" spellcheck="false"></label>'; }).join('');
    $('words').innerHTML = ''; // the words are gone from the screen now
    $('create-2').hidden = true; $('create-3').hidden = false;
    $('checks').querySelector('input').focus();
  };
  $('create-confirm').onclick = async function () {
    $('confirm-err').textContent = '';
    var checks = Array.prototype.map.call($('checks').querySelectorAll('input'), function (i) { return { index: Number(i.dataset.idx), word: i.value.trim() }; });
    try { await post('api/create/confirm', { checks: checks }); $('checks').innerHTML = ''; await poll(); setPhase('home'); showPage('home'); toast('Wallet ready'); }
    catch (e) { $('confirm-err').textContent = e.message + '. The words are gone from the screen: if the paper is wrong, remove this wallet in Settings and start again.'; }
  };

  // ---------- import and watch ----------
  var importMode = 'seed';
  $('go-import').onclick = function () { setPhase('import'); setImportMode('seed'); };
  $('import-back').onclick = function () { setPhase('welcome'); };
  document.querySelectorAll('#import-mode .seg-b').forEach(function (b) { b.onclick = function () { setImportMode(b.dataset.mode); }; });
  function setImportMode(m) {
    importMode = m;
    document.querySelectorAll('#import-mode .seg-b').forEach(function (b) { var on = b.dataset.mode === m; b.classList.toggle('on', on); b.setAttribute('aria-checked', on ? 'true' : 'false'); });
    $('import-data-label').textContent = m === 'seed' ? 'The words, in order' : m === 'key' ? 'The private key (hex)' : 'The address to watch';
    $('import-watch-note').hidden = m !== 'watch';
    $('import-pw-field').hidden = m === 'watch'; $('import-pw2-field').hidden = m === 'watch';
    $('import-go').textContent = m === 'watch' ? 'Watch this address' : 'Import';
    $('import-err').textContent = '';
  }
  $('import-go').onclick = async function () {
    $('import-err').textContent = '';
    var data = $('import-data').value.trim(), a = $('import-pw').value, b = $('import-pw2').value;
    if (!data) { $('import-err').textContent = 'Nothing to import.'; return; }
    if (importMode !== 'watch') {
      var refusal = passwordRefusal(a);
      if (refusal) { $('import-err').textContent = refusal; return; }
      if (a !== b) { $('import-err').textContent = 'The two passwords differ.'; return; }
    }
    try {
      await post('api/import', { mode: importMode, data: data, password: a });
      $('import-data').value = ''; $('import-pw').value = ''; $('import-pw2').value = '';
      // the words or the key may still sit on the system clipboard from the paste (wallet-security's CL-2): the
      // clipboard is cleared where the webview allows it, and the screen said so before the paste
      if (importMode !== 'watch' && navigator.clipboard && navigator.clipboard.writeText) { try { await navigator.clipboard.writeText(''); } catch (e) { /* the clipboard is the browser's */ } }
      await poll(); setPhase('home'); showPage('home'); toast(importMode === 'watch' ? 'Watching' : 'Imported');
    } catch (e) { $('import-err').textContent = e.message; }
  };

  // F-W2: the pressed state on every button from the pointer's down, within one frame and before any handler or
  // engine reply; a button a handler disables for a request shows the spinner (the busy mark) at once
  var lastPressed = null, lastPressedAt = 0;
  document.addEventListener('pointerdown', function (ev) {
    var b = ev.target && ev.target.closest ? ev.target.closest('.btn, .nav, .chev-btn, .disclose') : null;
    if (!b) return; lastPressed = b; lastPressedAt = Date.now();
    b.classList.add('pressed'); setTimeout(function () { b.classList.remove('pressed'); }, 180);
  }, true);
  // the busy mark only on the button the person pressed, when its handler disables it for a request (a button
  // disabled by the page's state, like Bond with no identity, carries no spinner)
  new MutationObserver(function (ms) {
    ms.forEach(function (m) {
      var b = m.target;
      if (b.disabled && b === lastPressed && Date.now() - lastPressedAt < 1500) b.classList.add('busy');
      else if (!b.disabled) b.classList.remove('busy');
    });
  }).observe(document.body, { attributes: true, attributeFilter: ['disabled'], subtree: true });
  // ---------- unlock ----------
  $('unlock-form').onsubmit = async function (e) {
    e.preventDefault(); $('unlock-err').textContent = '';
    try {
      var r = await post('api/unlock', { password: $('unlock-pw').value }); $('unlock-pw').value = '';
      // F-W1: the engine asks for a re-seal when the host's sealed password no longer matched (one prompt, then on)
      if (r && r.reseal && r.token && bio.host) { try { var h = await bio.call('enrol', { token: r.token }); if (!h.ok) post('api/biometric/enrol/cancel', {}).catch(function () {}); } catch (e2) {} }
      await poll(); showPage('home');
    }
    catch (err) { $('unlock-err').textContent = err.message; }
  };

  // ---------- send, with the sentences shown before anything is signed ----------
  // the quote's review: the sentences, the simulation, the fee line, the prompt's glyph; one path for a send and
  // for the faucet's drip (the one-tap "Get test IGN" on the node card: the same review, the same send)
  function showQuote() {
      $('ask-send-lines').innerHTML = quote.sentences.map(function (s) { return '<li>' + esc(s) + '</li>'; }).join('');
      $('ask-send-chain').textContent = (quote.chain_note || '') + memoryLockSentence();
      // a short balance says so in one line; the node's own words only when the balance covers it
      $('ask-send-sim').textContent = !quote.enough ? 'The balance does not cover the amount and the fee (up to ' + quote.total_max_ign + ' IGN).' : simWords(quote.simulation);
      $('fee-line').textContent = 'up to ' + quote.fee_max_ign + ' IGN; total up to ' + quote.total_max_ign + ' IGN';
      $('send-go').hidden = !!quote.watch_only; $('send-export').hidden = !quote.watch_only;
      $('send-go').disabled = !quote.simulation.ok || !quote.enough || !!quote.unlock_first;
      if (quote.unlock_first) $('confirm-send-err').textContent = 'Unlock first. The wallet locked while you were away; the review stands, the send waits for the password.';
      // the prompt's glyph on the button while the prompt is enrolled: the host confirms before the engine signs
      $('send-go').querySelector('.fp-ico').hidden = !quote.confirm_needed;
      setLine($('send-bio-line'), '');
      $('send-form').hidden = true; $('ask-send').hidden = false; $('envelope-box').hidden = true; $('send-done').hidden = true;
  }
  $('send-quote').onclick = async function () {
    $('send-err').textContent = ''; $('confirm-send-err').textContent = '';
    try {
      quote = await post('api/send/quote', { to: $('send-to').value.trim(), amount: $('send-amount').value.trim() });
      showQuote();
    } catch (e) { $('send-err').textContent = e.message; }
  };
  $('faucet-go-home').onclick = function () { showPage('settings'); $('faucet-go').click(); };
  $('faucet-go').onclick = async function () {
    $('faucet-err').textContent = ''; $('confirm-send-err').textContent = ''; $('faucet-go').disabled = true;
    try {
      var r = await post('api/faucet', {});
      if (r && r.relayed) {
        // the chain's relay dripped (or refused with the next allowed time): no review, nothing signed here
        var when = r.next_at ? new Date(r.next_at * 1000).toLocaleString() : '';
        if (r.refused) $('faucet-err').textContent = (r.error || 'The faucet refused.') + (when ? ' Next drip after ' + when + '.' : '');
        else { toast(r.amount_ign + ' test IGN on their way'); $('faucet-note').textContent = r.amount_ign + ' IGN sent by the faucet' + (r.hash ? ', ' + String(r.hash).slice(0, 12) : '') + '. Next drip after ' + when + '.'; $('faucet-note').hidden = false; }
      } else { quote = r; showPage('send'); showQuote(); }
    } catch (e) { $('faucet-err').textContent = e.message; }
    $('faucet-go').disabled = false;
  };
  $('ask-send-no').onclick = function () { quote = null; $('ask-send').hidden = true; $('envelope-box').hidden = true; $('send-form').hidden = false; $('fee-line').textContent = 'Shown when you review'; };
  $('send-go').onclick = async function () {
    if (!quote) return; $('confirm-send-err').textContent = ''; $('send-go').disabled = true;
    try {
      // with the prompt enrolled the host confirms the quote's nonce first; the engine takes it once, bound to
      // this exact quote (the gate); a browser tab has no host and says so
      var nonce = '';
      if (quote.confirm_needed) {
        if (!hostHere()) { $('confirm-send-err').textContent = what() + ' is on for this wallet, and only the Igneum Wallet app window can show it.'; $('send-go').disabled = false; return; }
        setLine($('send-bio-line'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
        var c = await bio.call('confirm', { nonce: quote.confirm_nonce });
        setLine($('send-bio-line'), bioLine(c, 'confirmed, sending'));
        if (!c.ok) { $('send-go').disabled = false; return; }
        nonce = quote.confirm_nonce;
      }
      var r = await post('api/send', { quote_id: quote.quote_id, nonce: nonce });
      setLine($('send-bio-line'), '');
      sentDone(r.hash);
      setTimeout(function () { refreshChainHistory(true); }, 25000);
    } catch (e) { $('confirm-send-err').textContent = e.message; $('send-go').disabled = false; }
  };
  $('send-export').onclick = async function () {
    if (!quote) return;
    try { var env = await post('api/envelope/export', { quote_id: quote.quote_id }); $('envelope-out').textContent = JSON.stringify(env); $('envelope-box').hidden = false; $('envelope-in').value = ''; $('envelope-err').textContent = ''; }
    catch (e) { $('confirm-send-err').textContent = e.message; }
  };
  $('envelope-broadcast').onclick = async function () {
    $('envelope-err').textContent = '';
    try { var r = await post('api/envelope/import', { signed: $('envelope-in').value.trim() }); sentDone(r.hash); }
    catch (e) { $('envelope-err').textContent = e.message; }
  };
  function sentDone(hash) {
    quote = null;
    $('ask-send').hidden = true; $('envelope-box').hidden = true; $('send-done').hidden = false;
    $('sent-hash').textContent = hash;
    $('sent-row').innerHTML = '<span class="hw">sent</span> <span class="dim">waiting for a block</span>';
    $('send-to').value = ''; $('send-amount').value = ''; $('fee-line').textContent = 'Shown when you review';
    var tries = 0, timer = setInterval(async function () {
      tries++;
      try { var v = await api('api/tx/' + hash); txViews[hash] = v.view; var r = Settlement.row(v.view); $('sent-row').innerHTML = '<span class="hw ' + esc(r.tone) + '">' + esc(Settlement.progress(v.view) || r.word) + '</span> <span class="dim">' + esc(r.why) + '</span>'; if (v.view.word === 'settled' || v.view.word === 'failed' || v.view.word === 'dropped' || tries > 200) clearInterval(timer); }
      catch (e) { if (tries > 200) clearInterval(timer); }
    }, 5000);
    poll();
  }
  $('sent-view').onclick = function () { showPage('history'); };
  $('sent-again').onclick = function () { $('send-done').hidden = true; $('send-form').hidden = false; };

  // ---------- the mining identity: the draw, the bond, the receipts (DESIGN.md 1.13; the design system's identity
  // screen, site/design/sketches/wallet-identity-after.html) ----------
  var bondSentKey = null; // the identity a bond was just sent for, so the pill says "bonding" until the chain shows it
  function renderMining(s) {
    var n = s.network || {};
    var has = !!n.registry;
    $('bond-eyebrow').textContent = has ? ('frozen rules; ' + View.netWord(n.name) + ', registry version ' + ((n.authority || {}).registry_version || '')) : (View.netWord(n.name) + ': no bond registry on this chain');
    $('bond-quote').disabled = !has; $('draw-go').disabled = !has; $('holder-bond').disabled = !has;
    if (!has) { setPill('is-open', 'no registry'); }
  }
  // three significant figures, thousands with a comma, no trailing zeros past the point (section 6)
  function sig3(x) {
    if (!isFinite(x) || x === 0) return '0';
    var s = Number(x.toPrecision(3));
    return s.toLocaleString('en-GB', { maximumFractionDigits: 6 });
  }
  function setPill(cls, word) {
    $('draw-pill').className = 'ig-pill ' + cls; $('draw-pill-word').textContent = word;
  }
  // a span of seconds in words for a tile's line
  function durWords(secs) { var s = Math.max(0, Math.round(secs || 0)); return s < 3600 ? Math.max(1, Math.round(s / 60)) + ' minutes' : s < 86400 * 2 ? Math.round(s / 3600) + ' hours' : Math.round(s / 86400) + ' days'; }
  // a tile: the figure and, when given, the line beneath it
  function tile(id, figure, sub) { $(id).textContent = figure; if (sub !== undefined) $(id + '-s').textContent = sub; }
  // the pace of draws as words a holder reads at a glance (the engine's times_words, for the tiles)
  function drawPace(perDay) {
    if (!(perDay > 0)) return { t: '·', s: 'no bond in the draw' };
    if (perDay >= 2) return { t: 'about ' + sig3(perDay) + ' a day', s: 'predicted at the chain\'s pace' };
    var days = 1 / perDay;
    if (days < 1.5) return { t: 'about once a day', s: 'predicted at the chain\'s pace' };
    if (days < 60) return { t: 'about every ' + Math.round(days) + ' days', s: 'predicted at the chain\'s pace' };
    return { t: 'about every ' + sig3(days / 30.44) + ' months', s: 'predicted at the chain\'s pace' };
  }
  // the identity made in the window (api/identity): the key short with a copy control, the draw and the Desk read on
  // it, the hidden form's key filled; the pill reads "No identity yet", then the bond's state on the chain
  var identityKey = '', currentBondIgn = '';
  function showIdentity(key) {
    identityKey = key || '';
    // the identity's public key hash (the registry's value for every bonded identity, never a secret): the copy control's source
    $('identity-public').textContent = identityKey;
    $('identity-short').textContent = View.shortHex(identityKey, 10, 8);
    $('identity-short').hidden = !identityKey; $('identity-copy').hidden = !identityKey;
    $('identity-make').hidden = !!identityKey || !!(state && state.watch_only); $('identity-watch').hidden = !!identityKey || !(state && state.watch_only);
    if (identityKey) {
      $('identity-eyebrow').textContent = 'reading the chain for this key';
      setPill('is-open', 'Identity');
      if (!$('bond-key').value.trim()) $('bond-key').value = identityKey;
      if (!$('desk-key').value.trim()) $('desk-key').value = identityKey;
      if (!$('draw-key').value.trim()) $('draw-key').value = identityKey;
      setTimeout(function () { $('draw-go').onclick(); }, 1500); setTimeout(function () { $('desk-go').onclick(); }, 4000);
    } else {
      $('identity-eyebrow').textContent = ''; setPill('is-open', 'No identity yet');
      $('draw-share').textContent = 'No identity yet'; $('draw-share-unit').textContent = 'make one to bond';
    }
  }
  $('identity-make').onclick = async function () {
    $('identity-err').textContent = ''; $('identity-make').disabled = true;
    try { var r = await post('api/identity', {}); showIdentity(r.key); toast('Identity made'); }
    catch (e) { $('identity-err').textContent = e.message; }
    $('identity-make').disabled = false;
  };
  $('draw-go').onclick = async function () {
    $('draw-err').textContent = '';
    try {
      var d = await post('api/authority/draw', { key: $('draw-key').value.trim() });
      // the bond's state as a pill whose colour is its meaning: none ash, bonding blue, bonded green
      var bonded = !!d.bond_now.bonded;
      var mine = identityKey && identityKey.toLowerCase() === String(d.key).toLowerCase();
      if (bonded) setPill('is-met', 'Bonded');
      else if (bondSentKey && bondSentKey.toLowerCase() === d.key.toLowerCase()) setPill('is-running', 'Bonding');
      else setPill('is-open', mine ? 'Identity, no bond' : 'No bond');
      if (mine) $('identity-eyebrow').textContent = bonded ? 'bonded on the chain' : 'not bonded yet';
      if (bonded) bondSentKey = null;
      var share = Number(d.draw.share_percent) || 0;
      // the big number: the share of the draw to three significant figures, or the words when there is no bond
      if (bonded) {
        $('draw-share').textContent = sig3(share);
        $('draw-share-unit').textContent = share > 0 ? 'percent of the draw' : ((d.enters_draw_block && d.enters_draw_block > d.head) ? 'percent until block ' + View.withCommas(d.enters_draw_block) : 'percent at this epoch\'s snapshot');
      } else {
        $('draw-share').textContent = 'Not bonded yet'; $('draw-share-unit').textContent = '';
      }
      var age = Math.max(0, Math.round((d.read_unix || Date.now() / 1000) - (d.head_unix || 0)));
      $('draw-read').textContent = 'Read from the chain at head ' + View.withCommas(d.head) + ', ' + (d.head_unix ? View.ago(age) : 'time unknown') + (d.snapshot && d.snapshot.block ? '; the draw from the snapshot at block ' + View.withCommas(d.snapshot.block) : '');
      currentBondIgn = bonded && d.bond_now && Number(d.bond_now.amount_ign) > 0 ? String(Number(d.bond_now.amount_ign)) : '';
      // the three tiles beneath the number
      tile('tile-units', bonded ? View.withCommas(d.draw.units) : '0', 'of 65,536 in the draw\'s table');
      tile('tile-bonded', bonded ? View.groupIgn(ign4(d.bond_now.amount_ign)) + ' IGN' : 'none', bonded ? 'locked to your identity' : 'bond IGN below to enter the draw');
      var chance = Number(d.draw.round0_chance) || 0;
      var pace = drawPace(chance * (86400 / Math.max(1, Number(d.cadence_secs) || 12)));
      tile('tile-next', pace.t, bonded && chance > 0 ? 'one in ' + View.withCommas(Math.round(1 / chance)) + ' of every draw' : pace.s);
      if (bonded) setTimeout(readHolder, 2000);
      $('draw-key-box').textContent = d.key;
      var rows = [
        ['bond', bonded ? d.bond_now.amount_ign + ' IGN' : 'none'],
        ['account', bonded ? View.shortHex(d.bond_now.account) : 'none'],
        ['signing key', bonded ? (d.bond_now.signer && d.bond_now.signer.toLowerCase() !== d.key.toLowerCase() ? View.shortHex(d.bond_now.signer, 8, 6) : 'the identity key itself') : 'none'],
        ['exit address', d.bond_now.exit ? View.shortHex(d.bond_now.exit) : (bonded ? 'the bond\'s account (no other exit set)' : 'none')],
        ['units at the snapshot', View.withCommas(d.draw.units) + ' of 65,536' + (d.snapshot && d.snapshot.block ? ' (the draw\'s table at block ' + View.withCommas(d.snapshot.block) + ')' : '')],
        ['chance per height', sig3(chance * 100) + ' percent']
      ];
      $('draw-kv').innerHTML = rows.map(function (r) { return '<span class="ig-label">' + esc(r[0]) + '</span><b>' + esc(r[1]) + '</b>'; }).join('');
      $('draw-words').textContent = 'Height ' + View.withCommas(d.height) + ': ' + d.eligible_words + '. A bond posted now enters the draw two epochs on.';
      $('draw-out').hidden = false;
    } catch (e) {
      // the chain did not answer the draw: the figure says so where the eye is, the reason beneath it
      $('draw-share').textContent = 'Not read'; $('draw-share-unit').textContent = 'the chain did not answer';
      $('draw-err').textContent = e.message;
      if (identityKey) $('identity-eyebrow').textContent = 'the chain did not answer';
    }
  };
  var bondQuote = null, quoteAction = '';
  // the words of each action (wallet-security's RECOVERY.md sections 4 and 7, from security A's reading of the
  // rotation and exit rows on devnet8; W-2 and W-3 read them here)
  var BOND_WORDS = {
    'bond': 'The bond belongs to this account and is locked by the chain\'s rule from the post height; the most you can lose is all of it, by a seal or endorsement equivocation of your signing key or by a thief of the bond secret. A missed draw costs nothing.',
    'release': 'The whole bond returns to the exit address you registered, and the key leaves the table for good; the chain refuses this before the lock (the post height plus its lock in blocks). Nothing is kept by anyone.',
    'set-signer': 'Rotating the signing key has a cost: until your sealer holds the new key, every draw of this identity is a missed draw. Change the key in the sealer first, or at once after.',
    'set-exit': 'If you set the exit address to a key you keep offline, a thief of this wallet\'s key can release the bond only to that address. A thief of the bond secret cannot release it at all: the only door is the claim, which burns a quarter and removes the key.',
    'claim-exit': 'A claim with the proof moves the whole bond at once: your own release to the exit address, or a hostile claim to any other address with a quarter burnt and the key removed. Development chains only.'
  };
  $('bond-action').onchange = function () {
    var a = $('bond-action').value;
    $('bond-action-words').textContent = BOND_WORDS[a] || '';
    $('bond-amount-field').hidden = a !== 'set-exit';
    $('bond-signer-field').hidden = a !== 'set-signer';
    $('bond-exit-field').hidden = a !== 'set-exit' && a !== 'claim-exit';
    $('bond-exit-note').hidden = true;
    $('bond-pi-field').hidden = a !== 'claim-exit';
    $('bond-share-field').hidden = a !== 'claim-exit';
  };
  $('bond-action').dispatchEvent(new Event('change'));
  $('bond-clear').onclick = function () {
    ['bond-key', 'bond-amount', 'bond-signer', 'bond-exit', 'bond-pi', 'bond-share'].forEach(function (id) { $(id).value = ''; });
    $('bond-err').textContent = '';
  };
  // one review for every action: the quote's sentences, the screen before it (W-2, W-3) and the simulation, shown in
  // the first card; the send waits for "I have read these"
  async function reviewBond(q, errId) {
    $(errId).textContent = ''; $('confirm-bond-err').textContent = '';
    try {
      quoteAction = q.action;
      bondQuote = await post('api/authority/quote', q);
      $('ask-bond-lines').innerHTML = bondQuote.sentences.map(function (s) { return '<li>' + esc(s) + '</li>'; }).join('');
      var screen = bondQuote.risk_screen ? bondQuote.risk_screen.statements.map(function (t) { return '<li>' + esc(t.text) + ' <span class="dim">(read at head ' + View.withCommas(t.head) + ')</span></li>'; })
        : bondQuote.exit_screen ? ['<li>' + esc(bondQuote.exit_screen.words) + ' <span class="dim">(read at head ' + View.withCommas(bondQuote.exit_screen.head) + ')</span></li>'] : null;
      $('bond-screen').hidden = !screen;
      $('bond-screen-read').checked = false;
      if (screen) {
        $('bond-screen-eyebrow').textContent = bondQuote.risk_screen ? 'What you must understand before bonding (read at head ' + View.withCommas(bondQuote.risk_screen.head) + ', ' + bondQuote.risk_screen.chain + ')' : 'The exit, after the lock';
        $('bond-screen-lines').innerHTML = screen.join('');
      }
      $('ask-bond-sim').textContent = simWords(bondQuote.simulation) + (bondQuote.enough ? '' : ' The balance does not cover the total.') + ' Fee up to ' + bondQuote.fee_max_ign + ' IGN.';
      $('bond-go').disabled = !bondQuote.sendable || !bondQuote.simulation.ok || !bondQuote.enough || !!bondQuote.watch_only || !!screen;
      $('bond-screen-read').onchange = function () { $('bond-go').disabled = !bondQuote.sendable || !bondQuote.simulation.ok || !bondQuote.enough || !!bondQuote.watch_only || !$('bond-screen-read').checked; };
      $('bond-action-row').hidden = true; $('ask-bond').hidden = false; $('bond-done').hidden = true;
      $('mining-card-draw').scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (e) { $(errId).textContent = e.message; }
  }
  $('bond-quote').onclick = function () {
    reviewBond({ action: $('bond-action').value, key: $('bond-key').value.trim(), amount: $('bond-amount').value.trim(), signer: $('bond-signer').value.trim(), exit: $('bond-exit').value.trim(), pi_file: $('bond-pi').value.trim(), share: $('bond-share').value.trim() }, 'bond-err');
  };
  $('ask-bond-no').onclick = function () { bondQuote = null; $('ask-bond').hidden = true; $('bond-action-row').hidden = false; };
  // the holder's system: the chain's own numbers for a bond of this size, read at this moment (api/holder); the rule
  // under its disclosure, the unit price as the bond field's line, the Desk's pay and pace tiles
  var holderTries = 0;
  async function readHolder() {
    $('holder-err').textContent = '';
    try {
      var h = await post('api/holder', { amount: $('holder-amount').value.trim() || currentBondIgn || '' });
      $('holder-words').textContent = h.words;
      // two sentences each for the forfeiture rule and the exit (the founder's polish, 15 October 2026); the chain's
      // whole rule as the engine words it beneath them, the chair's words unchanged
      $('holder-risk').textContent = h.registry_version >= 4
        ? 'Your whole bond is forfeited if your signing key seals or endorses two different blocks at one height. Anyone holding your bond secret can take three quarters of it at once. The bond returns only by a release after the lock, to the exit address you set. Set the exit to a key you keep offline and a thief of this wallet\'s key can release it only there.'
        : h.risk;
      $('holder-rule-full').textContent = h.registry_version >= 4 ? h.risk : '';
      $('holder-eyebrow').textContent = 'Read at head ' + View.withCommas(h.head) + ', ' + View.netWord(h.chain);
      var bal = Number(h.balance_ign) - 0.05; if (bal > 0) $('holder-amount').placeholder = 'up to ' + View.groupIgn(bal.toFixed(2)) + ' IGN';
      // the unit price as the field's line; what a typed amount buys beside it only when an amount is typed
      var typed = $('holder-amount').value.trim();
      $('holder-units').textContent = h.unit_price_ign ? View.groupIgn(h.unit_price_ign) + ' IGN a unit, one in 65,536 of every draw' + (typed && Number(h.bond_ign) > 0 ? '; ' + View.groupIgn(Number(h.bond_ign).toFixed(2)) + ' IGN buys ' + (h.units_words || View.withCommas(h.units) + ' units') : '') : '';
      if (h.paid_per_seal_ign) tile('tile-pay', View.groupIgn(ign4(String(h.paid_per_seal_ign))) + ' IGN', 'the sealer\'s quarter of the block\'s subsidy, ' + View.groupIgn(ign4(String(h.subsidy_per_block_ign || '0'))) + ' IGN a block');
      if (currentBondIgn) { var p = drawPace(Number(h.drawn_per_day) || 0); tile('tile-draws', p.t, p.s); }
      else tile('tile-draws', '·', 'no bond in the draw');
      holderTries = 0;
    } catch (e) { $('holder-words').textContent = 'The chain did not answer. Reading again.'; $('holder-risk').textContent = ''; $('holder-err').textContent = /not defined|undefined|TypeError|ReferenceError/.test(e.message) || e.message === $('draw-err').textContent ? '' : e.message; if (holderTries++ < 3) setTimeout(readHolder, 3000 + 1500 * holderTries); }
  }
  // the Desk: a key's numbers from the chain (api/authority/desk) as tiles with the head stamp; the full rows under Advanced
  $('desk-go').onclick = async function () {
    $('desk-err').textContent = ''; $('desk-out').hidden = true;
    try {
      var d = await post('api/authority/desk', { key: $('desk-key').value.trim() });
      $('desk-eyebrow').textContent = 'Read at head ' + View.withCommas(d.head) + ', ' + View.netWord(d.chain) + ', registry version ' + d.registry_version;
      var bonded = d.bonded !== false && Number(d.locked_ign) > 0;
      var cadence = Math.max(1, Number(d.cadence_secs) || 12);
      tile('tile-posted', bonded && d.posted_height ? 'block ' + View.withCommas(d.posted_height) : '·', bonded ? 'the lock of ' + View.withCommas(d.lock_blocks) + ' blocks counts from here' : 'no bond posted for this key');
      if (bonded && d.earliest_exit_height) {
        var left = Number(d.blocks_to_exit) || 0;
        tile('tile-exit', 'block ' + View.withCommas(d.earliest_exit_height), left > 0 ? 'about ' + durWords(left * cadence) + ' from now at the chain\'s pace' : 'the lock has passed; the release is open');
      } else tile('tile-exit', '·', 'after the lock, to the exit address');
      if (bonded && d.window_blocks && d.predicted_round0 !== undefined) { var pr = drawPace(Number(d.predicted_round0) * 86400 / (Number(d.window_blocks) * cadence)); tile('tile-draws', pr.t, pr.s); }
      tile('tile-loss', bonded ? View.groupIgn(ign4(String(d.locked_ign))) + ' IGN' : '·', bonded ? 'all of it, by an equivocation or a thief of the bond secret' : 'nothing is locked for this key');
      var kv = [['key', d.key], ['locked', d.locked_ign + ' IGN' + (d.bonded === false ? ' (not bonded)' : '')], ['units at head', View.withCommas(d.units) + ' of 65,536, ' + d.fraction_percent + ' percent of the table at head (' + d.table_total_ign + ' IGN, ' + View.withCommas(d.keys_in_table) + ' keys)'], ['account', d.account || 'none'], ['signing key', d.signer_words || d.signer || 'none'], ['posted at', d.posted_height ? View.withCommas(d.posted_height) + ' (lock ' + View.withCommas(d.lock_blocks) + ' blocks)' : 'none']];
      $('desk-kv').innerHTML = kv.map(function (r) { return '<div class="k">' + esc(r[0]) + '</div><div class="v mono">' + esc(r[1]) + '</div>'; }).join('');
      $('desk-draws').textContent = d.stale ? d.stale_words : d.predicted_words + '; ' + d.observed_words + (d.observed.heights && d.observed.heights.length ? ' (heights ' + d.observed.heights.slice(-8).map(View.withCommas).join(', ') + ')' : '') + '. ' + (d.observed.note || '');
      $('desk-endorse').textContent = d.endorse_words || '';
      $('desk-exit').textContent = d.exit_words;
      // the release: offered when the lock has passed and this wallet is the bond's account
      var mine = d.account && state && state.address && String(d.account).toLowerCase() === String(state.address).toLowerCase();
      $('desk-release').hidden = !(d.release_open && mine && d.registry_version >= 4);
      $('desk-release').onclick = function () { $('bond-action').value = 'release'; $('bond-key').value = d.key; $('bond-action').dispatchEvent(new Event('change')); $('draw-advanced').open = true; $('bond-form').scrollIntoView({ behavior: 'smooth', block: 'start' }); };
      $('desk-penalties').innerHTML = (d.penalties.table || []).map(function (r) { return '<div class="k">' + esc(r.action) + '</div><div class="v">' + esc(r.loss) + (r.this_key ? ' <b>' + esc(r.this_key) + '</b>' : '') + '</div>'; }).join('');
      $('desk-loss').textContent = d.penalties.max_loss_words + '; burnt by penalties on this chain to date: ' + d.penalties.burnt_to_date_ign + ' IGN' + (d.penalties.enforced ? '' : '; on this version-3 chain the penalties are not enforced');
      $('desk-source').textContent = d.source; $('desk-source-line').hidden = !d.source;
      $('desk-out').hidden = false;
    } catch (e) { $('desk-err').textContent = e.message; }
  };
  // the faucet's line: a bond's worth for this key, then the bond action filled with it
  $('desk-faucet').onclick = async function () {
    $('holder-err').textContent = ''; $('desk-faucet').disabled = true;
    try {
      var r = await post('api/bond-faucet', { key_hash: identityKey || $('desk-key').value.trim() });
      toast('87 test IGN on their way: ' + (r.hash || '').slice(0, 12));
      $('holder-amount').value = '86.95'; // the drip is 87: a bond's worth at this epoch's price plus the fee
      $('holder-amount').focus();
    } catch (e) { $('holder-err').textContent = e.message; }
    $('desk-faucet').disabled = false;
  };
  $('holder-read').onclick = readHolder;
  $('holder-amount').addEventListener('change', readHolder);
  // the one bond action: this amount to this wallet's identity, the exit this wallet's address; the review follows
  $('holder-bond').onclick = function () {
    var amount = $('holder-amount').value.trim() || (($('holder-amount').placeholder.match(/[\d,]+\.?\d*/) || [''])[0].replace(/,/g, ''));
    if (!identityKey) { $('holder-err').textContent = 'Make an identity first; the bond is locked to it.'; return; }
    reviewBond({ action: 'bond', key: identityKey, amount: amount, signer: '', exit: (state && state.address) || '', pi_file: '', share: '' }, 'holder-err');
  };
  $('bond-go').onclick = async function () {
    if (!bondQuote) return; $('confirm-bond-err').textContent = ''; $('bond-go').disabled = true;
    try {
      var r = await post('api/send', { quote_id: bondQuote.quote_id });
      if (quoteAction === 'bond') { bondSentKey = identityKey || $('bond-key').value.trim(); setPill('is-running', 'Bonding'); }
      bondQuote = null; $('ask-bond').hidden = true; $('bond-done').hidden = false; $('bond-hash').textContent = r.hash;
      $('bond-row').innerHTML = '<span class="hw running">sent</span> <span class="dim">waiting for a block</span>';
      var tries = 0, timer = setInterval(async function () {
        tries++;
        try { var v = await api('api/tx/' + r.hash); var row = Settlement.row(v.view); $('bond-row').innerHTML = '<span class="hw ' + esc(row.tone) + '">' + esc(Settlement.progress(v.view) || row.word) + '</span> <span class="dim">' + esc(row.why) + '</span>'; if (v.view.word === 'settled' || v.view.word === 'failed' || v.view.word === 'dropped' || tries > 200) clearInterval(timer); }
        catch (e) { if (tries > 200) clearInterval(timer); }
      }, 5000);
      poll();
    } catch (e) { $('confirm-bond-err').textContent = e.message; $('bond-go').disabled = false; }
  };
  $('bond-view').onclick = function () { showPage('history'); };
  $('bond-again').onclick = function () { $('bond-done').hidden = true; $('bond-action-row').hidden = false; $('holder-amount').value = ''; setTimeout(function () { $('draw-go').onclick(); }, 500); };
  $('rc-go').onclick = async function () {
    $('rc-err').textContent = ''; $('rc-rows').innerHTML = ''; $('rc-summary').hidden = true;
    try {
      var r = await post('api/receipts/verify', { lines: $('rc-lines').value, pool_key: $('rc-pool').value.trim(), member_key: $('rc-member').value.trim() });
      var agree = r.bad === 0 && r.weight_counted === r.pool_total;
      $('rc-summary').innerHTML = '<span class="hw ' + (agree ? 'met' : 'bad') + '">' + (agree ? 'they agree' : 'they do not agree') + '</span> <span class="dim">' + esc(r.verified + ' of ' + r.receipts + ' receipts verified; your count ' + r.weight_counted + ', the pool’s running total ' + r.pool_total) + '</span>';
      $('rc-summary').hidden = false;
      $('rc-rows').innerHTML = r.verdicts.map(function (v) {
        return '<div class="hrow"><span class="hw ' + (v.ok ? 'met' : 'bad') + '">' + (v.ok ? 'ok' : 'BAD') + '</span> <span class="mono">#' + esc(String(v.seq)) + ' weight ' + esc(String(v.weight)) + ' total ' + esc(String(v.total)) + '</span> <span class="dim">' + esc(v.ok ? View.shortHex('0x' + v.share_hash, 8, 4) : v.reason) + '</span></div>';
      }).join('');
    } catch (e) { $('rc-err').textContent = e.message; }
  };

  // ---------- backup ----------
  $('backup-toggle').onclick = function () { var d = $('backup-details'); d.hidden = !d.hidden; $('backup-toggle').setAttribute('aria-expanded', String(!d.hidden)); };
  $('backup-cancel').onclick = function () { $('backup-details').hidden = true; $('backup-out').hidden = true; $('backup-pw').value = ''; };
  $('backup-show').onclick = async function () {
    $('backup-err').textContent = '';
    try {
      var r = await post('api/reveal', { password: $('backup-pw').value }); $('backup-pw').value = '';
      $('backup-words').innerHTML = (r.words || []).map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('');
      $('backup-key').textContent = r.key; $('backup-out').hidden = false;
      if (!state.backed_up) { await post('api/backed-up'); poll(); }
    } catch (e) { $('backup-err').textContent = e.message; }
  };
  $('backup-hide').onclick = function () { $('backup-out').hidden = true; $('backup-words').innerHTML = ''; $('backup-key').textContent = ''; };
  // the backup through the prompt: a reveal challenge the host confirms; the words come from the key already
  // unlocked in the signer, no password typed (the engine's gate takes the nonce once)
  $('backup-touch').onclick = async function () {
    $('backup-err').textContent = '';
    try {
      var c = await post('api/biometric/challenge', { purpose: 'reveal' });
      setLine($('backup-err'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
      var h = await bio.call('confirm', { nonce: c.nonce });
      setLine($('backup-err'), bioLine(h, 'confirmed'));
      if (!h.ok) return;
      var r = await post('api/reveal', { nonce: c.nonce });
      $('backup-err').textContent = '';
      $('backup-words').innerHTML = (r.words || []).map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('');
      $('backup-key').textContent = r.key; $('backup-out').hidden = false;
      if (!state.backed_up) { await post('api/backed-up'); poll(); }
    } catch (e) { $('backup-err').textContent = e.message; }
  };

  // ---------- Touch ID and Windows Hello on the Settings page ----------
  $('bio-toggle-btn').onclick = async function () {
    var b = (state && state.biometric) || {};
    if (b.enrolled) { try { await post('api/biometric/remove', {}); $('bio-err').textContent = ''; toast(what() + ' is off'); await poll(); } catch (e) { $('bio-err').textContent = e.message; } return; }
    $('bio-enrol').hidden = false; $('bio-pw').focus({ preventScroll: true });
  };
  $('bio-enrol-cancel').onclick = function () { $('bio-enrol').hidden = true; $('bio-pw').value = ''; $('bio-err').textContent = ''; post('api/biometric/enrol/cancel', {}).catch(function () {}); };
  $('bio-enrol-go').onclick = async function () {
    $('bio-err').textContent = '';
    var pw = $('bio-pw').value;
    if (!pw) { $('bio-err').textContent = 'Type the password first.'; return; }
    $('bio-enrol-go').disabled = true;
    try {
      var r = await post('api/biometric/enrol/begin', { password: pw });
      setLine($('bio-err'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
      var h = await bio.call('enrol', { token: r.token });
      setLine($('bio-err'), bioLine(h, 'is on'));
      if (!h.ok) { post('api/biometric/enrol/cancel', {}).catch(function () {}); $('bio-enrol-go').disabled = false; return; }
      $('bio-pw').value = ''; $('bio-enrol').hidden = true; toast(what() + ' is on'); await poll();
    } catch (e) { $('bio-err').textContent = e.message; }
    $('bio-enrol-go').disabled = false;
  };
  $('ask-on-open').onchange = async function (ev) { try { await post('api/settings', { ask_on_open: ev.target.checked }); } catch (e) { toast(e.message); } };
  $('auto-update').onchange = async function (ev) { try { await post('api/settings', { auto_update: ev.target.checked }); toast(ev.target.checked ? 'Updates download by themselves' : 'Updates wait for you'); } catch (e) { toast(e.message); } };
  $('s-update').onclick = function () { post('api/update/check', {}).catch(function () {}); toast('Checking for an update'); };
  $('s-open').onclick = function () { post('api/update/open', {}).then(function () { toast('The download is in its folder'); }).catch(function (e) { toast(e.message); }); };

  // ---------- settings ----------
  $('idle-lock').onchange = async function () { try { await post('api/settings', { idle_lock_min: Number($('idle-lock').value) }); toast('Saved'); } catch (e) { toast(e.message); } };
  $('node-save').onclick = async function () { try { await post('api/settings', { rpc: $('node-endpoint').value.trim() }); toast('Node set'); poll(); } catch (e) { toast(e.message); } };
  document.querySelectorAll('#theme-seg .seg-b').forEach(function (b) { b.onclick = async function () { applyTheme(b.dataset.theme); try { await post('api/settings', { theme: b.dataset.theme }); } catch (e) { /* kept locally */ } }; });
  function applyTheme(t) {
    if (t === 'system') document.documentElement.removeAttribute('data-theme'); else document.documentElement.setAttribute('data-theme', t);
    document.querySelectorAll('#theme-seg .seg-b').forEach(function (b) { var on = b.dataset.theme === t; b.classList.toggle('on', on); b.setAttribute('aria-checked', on ? 'true' : 'false'); });
  }
  $('remove-start').onclick = function () { $('ask-remove').hidden = false; };
  $('ask-remove-no').onclick = function () { $('ask-remove').hidden = true; $('remove-err').textContent = ''; };
  $('remove-go').onclick = async function () {
    $('remove-err').textContent = '';
    try { await post('api/remove', { password: $('remove-pw').value }); $('remove-pw').value = ''; $('ask-remove').hidden = true; txViews = {}; await poll(); }
    catch (e) { $('remove-err').textContent = e.message; }
  };
  $('node-toggle').onclick = function () { var d = $('node-details'); d.hidden = !d.hidden; $('node-toggle').setAttribute('aria-expanded', String(!d.hidden)); };

  // ---------- the QR: drawn here, nothing fetched ----------
  var lastQr = '';
  function drawQr(address) {
    if (!address || address === lastQr) return;
    lastQr = address;
    // the address as a QR code on the page's own canvas (qr.js, byte mode, level M); the box's text form only when
    // the canvas cannot draw
    try { QR.draw($('qr-canvas'), address, 220); }
    catch (e) { $('qr').innerHTML = '<div class="mono small dim qr-fallback">' + esc(address) + '</div>'; }
  }

  // ---------- the activity ping for the idle lock ----------
  var active = false;
  ['mousemove', 'keydown', 'pointerdown'].forEach(function (ev) { document.addEventListener(ev, function () { active = true; }, { passive: true }); });
  setInterval(function () { if (active && phase === 'home') { active = false; post('api/activity').catch(function () {}); } }, 20000);

  // ---------- go ----------
  // the first page: the launch's (?open=) when the home phase is up, else home
  poll().then(function () { if (state.phase) setPhase(state.phase); var first = (phase === 'home' && launchQ.open && View.page(launchQ.open).id === launchQ.open) ? launchQ.open : 'home'; launchPageApplied = true; showPage(first); });
  setInterval(poll, 2000);
})();
