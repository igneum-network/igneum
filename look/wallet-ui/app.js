// Igneum Wallet window (wallet-ui-3, on the miner's system). Talks to the engine on the same origin (the token is in
// the path); polls /api/state every 2 s; never holds a key: words and keys only pass through when the engine shows
// them once. The first block, View, is pure (the words every page shows for a state) and is what view.test.mjs loads:
// the file is run with `module` defined and no `document`, so only View executes there. The DOM block follows.
'use strict';

var View = (function () {
  var PAGES = [
    { id: 'home', title: 'Home', sub: 'your balance, your address, the node' },
    { id: 'send', title: 'Send', sub: 'one payment, confirmed here' },
    { id: 'receive', title: 'Receive', sub: 'your address, two ways' },
    { id: 'history', title: 'History', sub: "every transaction, with the node's word" },
    { id: 'settings', title: 'Settings', sub: 'security, backup, this machine, updates, the node' }
  ];
  function page(id) { for (var i = 0; i < PAGES.length; i++) if (PAGES[i].id === id) return PAGES[i]; return PAGES[0]; }
  function withCommas(n) { return (Number(n) || 0).toLocaleString('en-GB'); }
  function shortHex(a, n, m) { n = n || 6; m = m || 4; return a && a.length > n + m + 2 ? a.slice(0, 2 + n) + '…' + a.slice(-m) : (a || ''); }
  // wei (a decimal string) to IGN with up to `places` decimals, trailing zeros cut; never scientific
  function ign(wei, places) {
    places = places == null ? 6 : places;
    var w; try { w = BigInt(String(wei || '0').trim() || '0'); } catch (e) { return '0'; }
    var neg = w < 0n; if (neg) w = -w;
    var whole = w / 1000000000000000000n, frac = (w % 1000000000000000000n).toString().padStart(18, '0').slice(0, places).replace(/0+$/, '');
    return (neg ? '-' : '') + (frac ? whole + '.' + frac : String(whole));
  }
  function ago(s) { s = Math.max(0, Math.round(s || 0)); return s < 60 ? s + ' s ago' : s < 3600 ? Math.floor(s / 60) + ' min ago' : s < 86400 ? Math.floor(s / 3600) + ' h ago' : Math.floor(s / 86400) + ' d ago'; }
  function timeWord(t, now) {
    if (!t) return '';
    var d = new Date(t * 1000), n = new Date((now || Date.now() / 1000) * 1000);
    var hm = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
    var same = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
    return same ? hm : d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) + ' ' + hm;
  }

  // ---------- the balance and its money line ----------
  // the hero shows the number only when it is known; otherwise one line says why (never a 0 for an unknown)
  function balanceLine(s) {
    var n = s.node || {};
    if (s.balance_known) return { known: true, text: '' };
    if (s.scanning) return { known: false, text: 'reading the chain, ' + withCommas(s.scanned_to || 0) + ' of ' + withCommas(n.block || 0) + ' blocks' };
    if (n.state === 'ok') return { known: false, text: 'reading the balance' };
    if (n.state === 'starting' || n.state === 'connecting') return { known: false, text: 'waiting for the node to start' };
    return { known: false, text: 'waiting for a node' };
  }
  // money first when a market price exists (price_gbp_per_ign on the state, owed by the engine); else one grey line
  function moneyLine(s) {
    var p = Number(s.price_gbp_per_ign || 0);
    if (p > 0 && s.balance_known) {
      var v = parseFloat(s.balance || '0') * p;
      return { money: '£' + v.toLocaleString('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 }), text: "at today's price" };
    }
    var net = (s.settings && s.settings.network) || 'devnet';
    return { money: '', text: net === 'devnet' ? 'no market price on devnet: coins have no value' : 'no market price yet' };
  }

  // ---------- the node line ----------
  function nodeWords(n) {
    switch (n.state) {
      case 'ok': return { word: 'synced', tone: 'ok' };
      case 'starting': return { word: 'starting', tone: '' };
      case 'connecting': return { word: 'connecting', tone: '' };
      case 'lost': return { word: 'lost', tone: 'bad' };
    }
    return { word: n.source === 'none' || !n.source ? 'not found' : 'off', tone: 'bad' };
  }
  function sourceWords(src) {
    switch (src) {
      case 'miner': return "the miner app's node";
      case 'own': return 'the bundled node';
      case 'public': return 'the public RPC';
      case 'external': return 'the node named by the environment';
    }
    return 'no node';
  }
  function verifyWords(s) {
    var f = s.finality || {}, n = s.node || {};
    if (f.verified_index > 0) return 'verified to checkpoint ' + withCommas(f.verified_index);
    if (n.source === 'public') return 'certificates cannot be verified without a node';
    if (n.state !== 'ok') return '';
    return 'waiting for the first certificate';
  }
  // "Node synced · the miner app's node · block 140,286 · verified to checkpoint 4674"
  function nodeLine(s) {
    var n = s.node || {}, w = nodeWords(n), parts = ['Node ' + w.word];
    if (n.source && n.source !== 'none') parts.push(sourceWords(n.source));
    if (n.block > 0) parts.push('block ' + withCommas(n.block));
    var v = verifyWords(s); if (v) parts.push(v);
    var sub = n.state === 'ok' ? '' : n.state === 'lost' ? 'the node stopped answering; trying again' : (n.message || '');
    return { text: parts.join(' · '), sub: sub, tone: w.tone };
  }
  // the old Node and Finality cards as rows with one-line meanings: [key, value, meaning, class]
  function nodeDetails(s, now) {
    var n = s.node || {}, f = s.finality || {}, rows = [];
    rows.push(['source', sourceWords(n.source), n.message || '', n.state === 'ok' ? 'ok' : 'warn']);
    rows.push(['chain', n.chain || (n.chain_id ? 'chain id ' + n.chain_id : 'not read yet'), n.chain_id ? 'chain id ' + n.chain_id : 'the chain the node follows', n.chain ? '' : 'dim']);
    rows.push(['block', n.block > 0 ? withCommas(n.block) : 'none yet', 'the newest block this node holds', n.block > 0 ? '' : 'dim']);
    rows.push(['endpoint', n.evm || 'none yet', 'where this wallet reads balances and sends', n.evm ? '' : 'dim']);
    rows.push(['finality on the node', n.source === 'public' ? 'not checkable without a node' : n.finality_active ? 'active, locked ' + withCommas(n.latest_locked) : 'paused', n.source === 'public' ? 'the public RPC carries no certificates' : 'what the node says; the wallet checks for itself below', n.finality_active ? 'ok' : 'dim']);
    if (f.verified_index > 0) {
      rows.push(['verified here', 'checkpoint ' + withCommas(f.verified_index), 'the newest certificate this wallet checked itself', 'ok']);
      rows.push(['signed by', f.signers + ' of ' + f.voters + ' voters', ((f.fraction_total || 0) * 100).toFixed(1) + '% of all weight; two thirds is the rule', '']);
      rows.push(['checkpoint height', f.chain_number == null ? 'pending' : withCommas(f.chain_number), 'blocks at or under it are final', f.chain_number == null ? 'dim' : '']);
      rows.push(['weights', f.weights_exact ? 'taken at the checkpoint' : 'the latest table', 'the voter weights the signature was checked against', '']);
      rows.push(['checked', f.verified_at ? ago((now || Date.now() / 1000) - f.verified_at) : 'not yet', '', '']);
    } else {
      rows.push(['verified here', 'nothing yet', f.message || 'waiting', 'dim']);
    }
    return rows;
  }
  // the pill: the wallet's own words
  function pillWords(s) {
    if (s.quitting) return { text: 'stopping', cls: '' };
    if (s.phase === 'unlock') return { text: 'locked', cls: '' };
    var n = s.node || {};
    if (n.state === 'ok') return { text: n.source === 'public' ? 'public rpc' : 'synced', cls: 'on' };
    if (n.state === 'starting' || n.state === 'connecting') return { text: n.state, cls: '' };
    if (n.state === 'lost') return { text: 'node lost', cls: 'warn' };
    return { text: 'no node', cls: 'warn' };
  }

  // ---------- the history row ----------
  function kindWord(e) { return { sent: 'Sent', received: 'Received', self: 'To yourself', reward: 'Reward', proving: 'Proving payout' }[e.kind] || e.kind; }
  function incoming(e) { return e.kind === 'received' || e.kind === 'reward' || e.kind === 'proving'; }
  function synthetic(e) { return /^(reward|proving)-/.test(e.hash || ''); }
  function whoLine(e) {
    if (e.kind === 'reward') return 'block reward';
    if (e.kind === 'proving') return 'shard payout';
    if (e.kind === 'self') return 'to yourself';
    return incoming(e) ? 'from ' + shortHex(e.from) : 'to ' + shortHex(e.to);
  }
  // ONE state word per row, never stronger than the chain's own (ledger P17): the wallet's own verified "final" wins
  // as finalised; failed from the receipt; else the node's word (igneum_getTransactionStatus); else the wallet's label
  function stateWord(e, nodeState) {
    var blk = e.block > 0 ? 'block ' + withCommas(e.block) : 'a block';
    if (e.finality === 'final') return { word: 'finalised', tone: 'ok', why: 'under checkpoint ' + withCommas(e.checkpoint || 0) + ', verified here' };
    if (e.finality === 'failed') return { word: 'failed', tone: 'bad', why: 'the execution failed; the fee was still paid' };
    if (synthetic(e)) return e.finality === 'in_block' ? { word: 'executed', tone: 'ink', why: 'in ' + blk } : { word: 'pending', tone: '', why: 'waiting for a block' };
    switch (nodeState) {
      case 'pending': return { word: 'pending', tone: '', why: 'waiting for a block' };
      case 'included': return { word: 'included', tone: '', why: 'in ' + blk + ', not executed yet' };
      case 'executed': return { word: 'executed', tone: 'ink', why: 'in ' + blk };
      case 'proven': return { word: 'proven', tone: 'ink', why: 'in ' + blk + ', proof paid' };
      case 'finalised': return { word: 'finalised', tone: 'ink', why: 'under a locked checkpoint, not yet verified here' };
      // section 9 of docs/spec/finality-guarantees.md: a block under a locked checkpoint is finalised even while the network's
      // finality is paused; the pause is the network's word, never a transaction's
      case 'finality not active': return { word: 'finalised', tone: 'ink', why: 'under a locked checkpoint, not yet verified here; finality is paused on the network above it' };
      // review B F04: a recovery lock is labelled "recovery", never plain "final" (the node lane's lockKind word)
      case 'finalised by a recovery lock': return { word: 'finalised (recovery lock)', tone: 'ink', why: 'under a recovery lock: the surviving majority’s lock after a long pause, not a certified checkpoint; not yet verified here' };
      case 'unknown': return { word: 'pending', tone: '', why: 'not in any block the node holds' };
    }
    if (e.finality === 'in_block') return { word: 'included', tone: '', why: 'in ' + blk };
    return { word: 'pending', tone: '', why: 'waiting for a block' };
  }
  function rowModel(e, nodeState, now) {
    var inn = incoming(e), st = stateWord(e, nodeState);
    return { hash: e.hash, kind: kindWord(e), who: whoLine(e), when: timeWord(e.time, now), amount: (inn ? '+' : '−') + ign(e.value, 6), in: inn, state: st, synthetic: synthetic(e), nodeWord: nodeState || '' };
  }
  // the rows the page asks the node about: not final, not failed, not a reward; newest first, at most `max`
  function needsNodeWord(list, max) {
    var out = [];
    for (var i = 0; i < (list || []).length && out.length < (max || 12); i++) {
      var e = list[i];
      if (e.finality === 'final' || e.finality === 'failed' || synthetic(e)) continue;
      out.push(e.hash);
    }
    return out;
  }

  // ---------- send ----------
  function gwei(weiPerGas) { var v = Number(weiPerGas || 0) / 1e9; return (v >= 10 ? Math.round(v) : +v.toFixed(v >= 1 ? 1 : 3)) + ' gwei'; }
  function feeWords(q) {
    if (!q) return { line: 'shown when you review', detail: '' };
    return {
      line: 'at most ' + q.fee_max_ign + ' IGN',
      detail: 'Gas ' + withCommas(q.gas) + ' at a base fee of ' + q.base_fee_gwei + ' gwei (burned) plus a ' + q.tip_gwei + ' gwei tip (to the miner), capped at ' + gwei(q.max_fee) + '; what is not used comes back.'
    };
  }
  function sendAsk(q, touch, bioName) {
    return {
      text: 'Send ' + q.value_ign + ' IGN to ' + shortHex(q.display_to || q.to) + '? Fee at most ' + q.fee_max_ign + ' IGN; ' + q.total_max_ign + ' IGN leaves the wallet at most.',
      button: touch ? 'Confirm with ' + (bioName || 'Touch ID') : 'Send now'
    };
  }

  // ---------- settings ----------
  function bioSentence(b, what, host) {
    b = b || {};
    if (b.enrolled) return what + ' is on. It unlocks the wallet, confirms each send and shows the backup; the password still works everywhere.';
    if (!host) return what + ' needs the Igneum Wallet app window; this page is open in a browser.';
    if (!b.available) return b.message || (what + (what === 'Touch ID' ? ' is not set up on this Mac.' : ' is not set up on this PC.'));
    return 'Unlock, confirm each send and show the backup with ' + what + '. The password still works everywhere.';
  }
  function idleSentence(min, what) { return Number(min) > 0 ? 'The key is cleared after ' + LockScreenIdle(min) + ' without you; ' + what + ' or the password opens it again.' : 'The wallet stays open until you lock it.'; }
  function LockScreenIdle(min) { var m = Number(min) || 0; return m <= 0 ? 'never' : m === 60 ? '1 hour' : m % 60 === 0 ? (m / 60) + ' hours' : m === 1 ? '1 minute' : m + ' minutes'; }

  // ---------- updates: the strip line and the card's note ----------
  function cap(t) { return t ? t.charAt(0).toUpperCase() + t.slice(1) : ''; }
  // ---- the page bridge (0.1.6, 8 October 2026): what the window says for a page's request; the facts come from the
  //      engine (src/bridge.rs pending_json), never from the page ----
  function hostOf(origin) { return String(origin || '').replace(/^https?:\/\//, '').replace(/\/.*$/, ''); }
  function bridgeWords(p, bioName) {
    p = p || {}; var host = hostOf(p.origin), out = { id: p.id, kind: p.kind, host: host, title: '', amount: '', message: '', lines: [], yes: '', no: 'Decline', confirm: !!p.confirm_needed, nonce: p.confirm_nonce || '' };
    var withBio = p.confirm_needed && bioName ? ' with ' + bioName : '';
    if (p.kind === 'connect') {
      out.title = host + ' wants to connect';
      out.lines = ['It will see your address and may ask you to sign or send. Nothing leaves without your word here.', 'You can disconnect it any time in Settings.'];
      out.yes = 'Connect';
    } else if (p.kind === 'send') {
      out.title = host + ' asks you to send';
      out.amount = (p.value_ign !== undefined && p.value_ign !== null ? p.value_ign : ign(p.value || '0', 18)) + ' IGN';
      var call = p.data_len > 0 ? 'a contract call (' + withCommas(p.data_len) + ' bytes' + (p.selector ? ', ' + p.selector : '') + ')' : 'a plain transfer';
      out.lines = ['To ' + shortHex(p.to_display || p.to || '', 6, 4) + ', ' + call + '.'];
      out.lines.push(p.fee_max_ign ? 'Fee up to ' + p.fee_max_ign + ' IGN (' + withCommas(p.gas) + ' gas). Total up to ' + p.total_max_ign + ' IGN.' : 'Pricing the fee with the node.');
      out.yes = 'Send' + withBio;
    } else if (p.kind === 'sign') {
      out.title = host + ' asks you to sign a message';
      out.message = p.text !== null && p.text !== undefined ? p.text : (withCommas(p.bytes || 0) + ' bytes: ' + (p.hex || ''));
      out.lines = ['Signing proves this wallet is yours. It moves no coins.'];
      out.yes = 'Sign' + withBio;
    } else if (p.kind === 'typed') {
      out.title = host + ' asks you to sign typed data';
      out.message = p.message || '';
      out.lines = [(p.primary_type || 'Data') + (p.domain ? ' for ' + p.domain : '') + '. Read it before you sign: a permit can let a contract spend tokens.'];
      out.yes = 'Sign' + withBio;
    }
    return out;
  }
  // the lock screen's line while a page waits
  function bridgeWaiting(pending) { var l = pending || []; if (!l.length) return ''; var hosts = []; l.forEach(function (p) { var h = hostOf(p.origin); if (hosts.indexOf(h) < 0) hosts.push(h); }); return (hosts.length === 1 ? hosts[0] + ' is' : hosts.length + ' sites are') + ' waiting: unlock to answer ' + (hosts.length === 1 ? 'it' : 'them'); }
  // the Settings card's rows
  function siteRows(sites, now) { return (sites || []).map(function (x) { return { origin: x.origin, host: hostOf(x.origin), line: 'connected ' + ago(now - (x.approved_at || now)) + (x.last_seen ? ' · last call ' + ago(now - x.last_seen) : '') }; }); }
  function bridgeStatus(b) {
    b = b || {};
    if (b.on === false) return 'Off. No website can connect; the switch turns it on.';
    if (b.listening === false) return 'Not listening: ' + (b.reason || 'the port is not free') + '. Quit whatever holds the port and open the wallet again.';
    return 'On. Websites you approve can connect through port ' + (b.port || 26811) + ' on this machine only.';
  }
  function updateLine(u) {
    var v = 'Igneum Wallet ' + u.version;
    if (u.urgent && u.urgent_text) return { text: u.urgent_text + (u.status === 'downloading' ? ' Downloading.' : ''), urgent: true, prog: u.status === 'downloading' };
    switch (u.status) {
      case 'available': return { text: v + ' is available. Downloading it.' };
      case 'downloading': return { text: 'Downloading ' + v + (u.size ? ' (' + Math.round(u.size / 1e6) + ' MB)' : '') + ': ' + Math.round((u.progress || 0) * 100) + '%', prog: true };
      case 'staging': return { text: v + ' downloaded and verified. Preparing it.' };
      case 'ready': return { text: v + ' is ready. ' + (u.wait ? cap(u.wait) + '.' : 'It installs as soon as nothing is being sent.'), install: true };
      case 'applying': return { text: 'Installing ' + v + ': ' + (u.wait ? u.wait + '.' : 'the app closes and opens again by itself.') };
      case 'deferred': return { text: v + ' is downloaded. Windows asked for permission and nobody answered; it installs the next time someone is at this PC.', install: true };
      case 'manual': return { text: v + ' is downloaded. ' + cap(u.wait || 'open the download and drag the app over the old one.'), open: true };
      case 'error': return { text: 'Update: ' + (u.error || 'failed') + '.', install: !!(u.ready || u.downloaded) };
    }
    return null;
  }
  // the update card on Settings: "up to date, checked 13 min ago"
  function updateNote(u, now) {
    var l = updateLine(u), parts = [];
    if (u.updated_from) parts.push('Updated from ' + u.updated_from + '.');
    if (u.rolled_back) parts.push('Rolled back: ' + u.rolled_back + '.');
    if (u.status === 'off') parts.push('Updates are off in this build.');
    else if (l && !(u.rolled_back && u.status === 'error')) parts.push(l.text);
    else if (u.status === 'checking') parts.push('Checking.');
    else if (u.status === 'current') parts.push('Up to date' + (u.checked_at ? ', checked ' + ago((now || Date.now() / 1000) - u.checked_at) : '') + '.');
    else if (u.error) parts.push(u.error);
    else parts.push('Not checked yet.');
    return parts.join(' ');
  }

  return { PAGES: PAGES, page: page, withCommas: withCommas, shortHex: shortHex, ign: ign, ago: ago, timeWord: timeWord, balanceLine: balanceLine, moneyLine: moneyLine, nodeWords: nodeWords, sourceWords: sourceWords, verifyWords: verifyWords, nodeLine: nodeLine, nodeDetails: nodeDetails, pillWords: pillWords, kindWord: kindWord, whoLine: whoLine, stateWord: stateWord, rowModel: rowModel, needsNodeWord: needsNodeWord, gwei: gwei, feeWords: feeWords, sendAsk: sendAsk, bioSentence: bioSentence, idleSentence: idleSentence, updateLine: updateLine, updateNote: updateNote, cap: cap, hostOf: hostOf, bridgeWords: bridgeWords, bridgeWaiting: bridgeWaiting, siteRows: siteRows, bridgeStatus: bridgeStatus };
})();
if (typeof module === 'object' && module && module.exports) { module.exports = { View: View }; }

if (typeof document !== 'undefined') (function () {
  var $ = function (id) { return document.getElementById(id); };
  var base = location.pathname.replace(/\/$/, '');
  var api = async function (path, body) {
    var r = await fetch(base + path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    var j = await r.json().catch(function () { return { ok: false, error: 'bad answer' }; });
    if (!r.ok || j.ok === false) throw new Error(j.error || ('http ' + r.status));
    return j;
  };
  var post = function (path, body) { return api(path, body || {}); };
  var esc = function (s) { return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); };
  var state = null, quote = null, sentHash = null, lastPhase = null, page = 'home', firstHome = true;
  var params = new URLSearchParams(location.search);
  if (params.get('host') === 'mac') document.body.classList.add('mac');
  function stored(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function store(k, v) { try { if (v == null) localStorage.removeItem(k); else localStorage.setItem(k, v); } catch (e) { /* private mode */ } }
  function setText(id, t) { var el = $(id); if (el && el.textContent !== t) el.textContent = t; }
  function toast(text) { var t = $('toast'); t.textContent = text; t.hidden = false; clearTimeout(t._h); t._h = setTimeout(function () { t.hidden = true; }, 1800); }
  function copyText(text, what) {
    if (!text) { toast('Nothing to copy yet'); return; }
    var done = function () { toast((what || 'Copied') + (what ? ' copied' : '')); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text, done); }); else fallbackCopy(text, done);
  }
  function fallbackCopy(text, done) { var ta = document.createElement('textarea'); ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'; document.body.appendChild(ta); ta.select(); try { document.execCommand('copy'); done(); } catch (e) { toast('Select and copy it by hand'); } document.body.removeChild(ta); }
  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-copy],[data-copy-text]'); if (!b) return;
    if (b.dataset.copyText != null) { copyText(b.dataset.copyText, b.dataset.copyWhat || ''); return; }
    var el = $(b.getAttribute('data-copy')), t = el ? el.textContent : '';
    if (!t || /^(not set|none yet|not read)/.test(t)) { toast('Nothing to copy yet'); return; }
    copyText(t, b.dataset.copyWhat || '');
  });

  // ---------- Touch ID / Windows Hello: the window host's bridge (unchanged from 0.1.4) ----------
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
    else if (c === 'not_set_up' || c === 'unavailable') t = r.message || (w + ' is not set up on this Mac');
    else if (c === 'nohost') t = w + ' needs the Igneum Wallet app window';
    else t = r.message || (w + ' did not succeed');
    return '<span class="bio-state">' + FP + esc(t) + '</span>';
  }
  function setLine(el, html) { el.innerHTML = html; }
  var forcedBio = params.get('bio') === 'touch';
  function hostHere() { return !!bio.host || forcedBio; }

  // ---------- the lock screen (ui/lock-screen.js, 0.1.4, unchanged) ----------
  var firstPhase = null, locking = false, autoTimer = 0, pwRevealed = false;
  var autoMem = { used: false };
  var reducedMotion = function () { return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches); };
  function lockLayout() {
    var s = forcedBio && state ? Object.assign({}, state, { biometric: Object.assign({}, state.biometric, { enrolled: true, available: true, kind: 'touchid' }) }) : state;
    return LockScreen.layout(s, hostHere());
  }
  function renderLock() {
    var lay = lockLayout();
    $('unlock-address').textContent = lay.address;
    $('unlock-touch-text').textContent = 'Unlock with ' + what();
    $('unlock-bio').hidden = !lay.touch;
    document.querySelector('.lock-controls').classList.toggle('touch', lay.touch);
    var showForm = !lay.touch || pwRevealed;
    $('unlock-form').hidden = !showForm;
    $('unlock-use-pw').hidden = !lay.touch || pwRevealed;
  }
  function showPasswordField() { pwRevealed = true; renderLock(); $('unlock-pw').focus({ preventScroll: true }); }
  function hidePasswordField() { if (!lockLayout().touch) return; pwRevealed = false; $('unlock-pw').value = ''; $('unlock-err').textContent = ''; renderLock(); $('unlock-touch').focus({ preventScroll: true }); }
  function focusLock() { var lay = lockLayout(); if (lay.touch && !pwRevealed) $('unlock-touch').focus({ preventScroll: true }); else $('unlock-pw').focus({ preventScroll: true }); }
  function lockTransition() {
    locking = true; document.body.classList.add('locking'); $('main').scrollTop = 0;
    pwRevealed = false; $('unlock-pw').value = ''; $('unlock-err').textContent = ''; setLine($('unlock-bio-note'), ''); renderLock();
    setTimeout(function () { locking = false; document.body.classList.remove('locking'); setPhase('unlock'); onLockScreen('lock'); }, reducedMotion() ? 0 : LockScreen.ENTER_MS);
  }
  function onLockScreen(reason) {
    clearTimeout(autoTimer);
    if (reason !== 'lock') { pwRevealed = false; $('unlock-pw').value = ''; $('unlock-err').textContent = ''; setLine($('unlock-bio-note'), ''); }
    renderLock(); focusLock();
    var lay = lockLayout();
    var d = LockScreen.autoPrompt({ reason: reason, phase: 'unlock', touch: lay.touch, askOnOpen: !!(state.settings && state.settings.ask_on_open), hidden: document.hidden, busy: bio.busy, firstPhase: firstPhase, updatedFrom: state.update && state.update.updated_from }, autoMem);
    if (reason === 'arrival') autoMem.used = true;
    if (d.prompt) autoTimer = setTimeout(function () { if (state && state.phase === 'unlock' && !document.hidden && !bio.busy && !pwRevealed) unlockWithTouch(); }, (reducedMotion() ? 0 : LockScreen.ENTER_MS) + d.delay);
  }
  async function unlockWithTouch() {
    if (bio.busy) return;
    clearTimeout(autoTimer);
    $('unlock-touch').disabled = true; setLine($('unlock-bio-note'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
    var r = await bio.call('unlock');
    $('unlock-touch').disabled = false;
    var l = LockScreen.line(r, what());
    setLine($('unlock-bio-note'), l.text ? '<span class="bio-state ' + l.cls + '">' + FP + esc(l.text) + '</span>' : '');
    if (r.ok) { await poll(); return; }
    if (l.password) showPasswordField(); else $('unlock-touch').focus({ preventScroll: true });
  }
  $('unlock-use-pw').onclick = showPasswordField;
  $('unlock-pw').addEventListener('keydown', function (e) { if (e.key === 'Escape') { e.preventDefault(); hidePasswordField(); } });
  $('unlock-touch').onclick = function () { unlockWithTouch(); };
  $('unlock-form').onsubmit = async function (ev) {
    ev.preventDefault(); $('unlock-err').textContent = '';
    try { await post('/api/unlock', { password: $('unlock-pw').value }); $('unlock-pw').value = ''; await poll(); }
    catch (e) { $('unlock-err').textContent = e.message; }
  };
  // the idle lock: the window tells the engine a person is here, every 20 s while there is input
  var active = false;
  ['mousemove', 'keydown', 'mousedown', 'wheel', 'touchstart'].forEach(function (e) { document.addEventListener(e, function () { active = true; }, { passive: true }); });
  setInterval(function () { if (active && state && state.phase === 'home') { active = false; post('/api/activity').catch(function () {}); } }, 20000);

  // ---------- appearance: system, light or dark (the page's own storage) ----------
  function applyTheme(t, keep) {
    t = t === 'light' || t === 'dark' ? t : 'system';
    if (t === 'system') document.documentElement.removeAttribute('data-theme'); else document.documentElement.setAttribute('data-theme', t);
    document.querySelectorAll('#theme-seg .seg-b').forEach(function (b) { var on = b.dataset.theme === t; b.classList.toggle('on', on); b.setAttribute('aria-checked', on ? 'true' : 'false'); });
    if (keep) store('igneum.wallet.theme', t === 'system' ? null : t);
  }
  // ?theme=light|dark shows a scheme without storing it (screenshots); otherwise the page's own stored choice
  applyTheme(params.get('theme') || stored('igneum.wallet.theme') || 'system', false);
  $('theme-seg').addEventListener('click', function (e) { var b = e.target.closest('.seg-b'); if (b) applyTheme(b.dataset.theme, true); });

  // ---------- phases (welcome, create, import, unlock, home) and the pages behind the rail ----------
  function setPhase(p) {
    document.body.dataset.phase = p;
    var home = p === 'home';
    document.body.classList.toggle('has-rail', home);
    $('rail').hidden = !home;
    $('top-brand').hidden = home;
    $('page-title').hidden = !home;
    $('btn-lock-top').hidden = !home;
    if (home) showPage(page);
  }
  function showPage(id) {
    var meta = View.page(id);
    page = meta.id;
    document.body.dataset.page = page;
    document.querySelectorAll('.page').forEach(function (el) { el.hidden = el.dataset.page !== page; });
    document.querySelectorAll('#rail-nav .nav').forEach(function (b) {
      var on = b.dataset.page === page;
      b.classList.toggle('on', on);
      if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    });
    setText('page-title-text', meta.title); setText('page-sub', meta.sub);
    $('main').scrollTop = 0;
    if (page === 'receive') loadReceive();
    if (page === 'send' && !$('send-form').dataset.locked && $('send-done').hidden) $('send-to').focus({ preventScroll: true });
    if (state) renderPage(state);
  }
  $('rail-nav').addEventListener('click', function (e) { var b = e.target.closest('.nav[data-page]'); if (b) showPage(b.dataset.page); });
  $('rail-nav').addEventListener('keydown', function (e) {
    var items = Array.prototype.slice.call(document.querySelectorAll('#rail-nav .nav')), i = items.indexOf(document.activeElement);
    if (i < 0) return;
    var j = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : -1;
    if (j < 0 || j >= items.length) return;
    e.preventDefault(); items[j].focus(); showPage(items[j].dataset.page);
  });
  function lockNow() { post('/api/lock').then(poll).catch(function (e) { toast(e.message); }); }
  $('btn-lock').onclick = lockNow;
  $('btn-lock-top').onclick = lockNow;
  // quit: a strip in place of a dialog
  $('btn-quit').onclick = function () { $('ask-quit').hidden = false; $('ask-quit-yes').focus(); };
  $('ask-quit-no').onclick = function () { $('ask-quit').hidden = true; };
  $('ask-quit-yes').onclick = function () { $('ask-quit').hidden = true; post('/api/quit').catch(function () {}); toast('Quitting'); };
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    if (!$('bridge').hidden) { e.preventDefault(); if (bridgeDoneId) $('bridge-close').click(); else bridgeDecide(false); return; }
    if (!$('upd').hidden) { e.preventDefault(); cardLater(); return; }
    if (!$('ask-quit').hidden) { $('ask-quit').hidden = true; return; }
    if (!$('ask-send').hidden) { cancelAsk(); return; }
    if (!$('ask-remove').hidden) { $('ask-remove').hidden = true; }
  });
  // disclosures: aria-expanded flips, the target shows
  document.addEventListener('click', function (e) {
    var b = e.target.closest('.disclose[aria-controls]'); if (!b) return;
    var open = b.getAttribute('aria-expanded') !== 'true'; b.setAttribute('aria-expanded', open ? 'true' : 'false');
    var t = $(b.getAttribute('aria-controls')); if (t) t.hidden = !open;
    if (!open && b.id === 'backup-toggle') resetBackup();
    if (!open && b.id === 'export-toggle') resetExport();
  });

  // ---------- state ----------
  async function poll() {
    try { state = await api('/api/state'); render(); } catch (e) { setText('pill-text', 'engine gone'); $('pill').className = 'pill warn'; }
  }
  function render() {
    var s = state, phase = s.phase;
    var inFlow = ['create', 'import'].indexOf(document.body.dataset.phase) >= 0;
    if (firstPhase === null) firstPhase = phase;
    if ((!inFlow || phase === 'home') && lastPhase !== phase && !locking) {
      if (lastPhase === 'home' && phase === 'unlock') lockTransition();
      else { setPhase(phase); if (phase === 'unlock') onLockScreen(lastPhase === null ? 'arrival' : 'lock'); }
    }
    lastPhase = phase;
    if (phase === 'home' && firstHome) { firstHome = false; if (params.get('page')) showPage(params.get('page')); }
    var pw = View.pillWords(s);
    setText('pill-text', pw.text); $('pill').className = 'pill ' + pw.cls;
    setText('welcome-eyebrow', (s.settings.network === 'devnet' ? 'devnet v4' : s.settings.network) + ' · nothing is bought or sold');
    if (forcedUpdate) s.update = sampleUpdate(forcedUpdate);
    renderNotices(s);
    renderUpdateCard(s);
    renderBridge(s);
    if (phase === 'unlock' || locking) renderLock();
    $('seg-miner').disabled = !s.miner_wallet_present;
    if (phase === 'home') {
      $('rail-status').innerHTML = '<b>' + esc(View.shortHex(s.display, 6, 4)) + '</b><br>' + esc(s.settings.network === 'devnet' ? 'devnet v4' : s.settings.network);
      setText('foot-version', 'v' + s.version);
      $('nav-updates-dot').hidden = !(s.update && (s.update.status === 'ready' || s.update.status === 'deferred' || s.update.status === 'manual'));
      renderPage(s);
    }
  }
  function renderPage(s) {
    if (page === 'home') renderHome(s);
    else if (page === 'send') renderSend(s);
    else if (page === 'receive') renderReceive(s);
    else if (page === 'history') renderRows($('history'), s.history, 0, s);
    else if (page === 'settings') renderSettings(s);
    refreshNodeWords(s);
  }

  // ---------- Home ----------
  function renderHome(s) {
    var bl = View.balanceLine(s);
    $('h-bal').hidden = !bl.known;
    setText('h-balance', bl.known ? s.balance : '');
    setText('h-balance-line', bl.text);
    var ml = View.moneyLine(s);
    $('h-money').innerHTML = (ml.money ? '<b>' + esc(ml.money) + '</b>' : '') + esc(ml.text);
    setText('home-address', s.display || 'not set');
    $('backup-note').hidden = !!s.backed_up;
    var nl = View.nodeLine(s);
    setText('node-line-text', nl.text); setText('node-sub', nl.sub);
    $('node-dot').className = 'dot' + (nl.tone === 'ok' ? ' live' : nl.tone === 'bad' ? ' bad' : '');
    $('node-card').classList.toggle('bad', nl.tone === 'bad');
    renderKv($('node-kv'), View.nodeDetails(s, s.now));
    renderRows($('recent'), s.history, 5, s);
    setText('hist-eyebrow', '');
  }
  function renderKv(el, rows) {
    el.innerHTML = rows.map(function (r) { return '<div><span class="k">' + esc(r[0]) + '</span><span class="v mono ' + esc(r[3] || '') + '">' + esc(r[1]) + '</span><span class="m">' + esc(r[2] || '') + '</span></div>'; }).join('');
  }
  $('go-send').onclick = function () { resetSend(); showPage('send'); };
  $('go-receive').onclick = function () { showPage('receive'); };
  $('recent-all').onclick = function () { showPage('history'); };
  $('backup-link').onclick = function (ev) { ev.preventDefault(); showPage('settings'); var t = $('backup-toggle'); if (t.getAttribute('aria-expanded') !== 'true') t.click(); t.scrollIntoView({ block: 'center' }); };

  // ---------- the history rows (Home shows five, History every one) ----------
  var nodeWord = {}, openRows = {}, fetching = false;
  function renderRows(el, list, limit, s) {
    list = list || [];
    if (limit) list = list.slice(0, limit);
    if (!list.length) { el.innerHTML = '<div class="empty">' + esc(s.scanning ? 'Reading the chain.' : 'Nothing yet. Receive IGN, or point the miner app at this address.') + '</div>'; return; }
    el.innerHTML = list.map(function (e) {
      var nw = nodeWord[e.hash.toLowerCase()], m = View.rowModel(e, nw && nw.word, s.now), open = !!openRows[e.hash.toLowerCase()];
      return '<div class="hrow' + (m.in ? ' in' : '') + (open ? ' open' : '') + '" data-hash="' + esc(e.hash) + '">' +
        '<div class="hr-main"><span class="glyph" aria-hidden="true">' + (m.in ? '↓' : '↑') + '</span>' +
        '<span class="hr-who"><span class="kind">' + esc(m.kind) + '</span><span class="sub">' + esc(m.who) + (m.when ? ' · ' + esc(m.when) : '') + '</span></span>' +
        '<span class="hr-amt">' + esc(m.amount) + '<span class="unit">IGN</span></span>' +
        '<span class="hr-state"><span class="st ' + esc(m.state.tone) + '"><i></i>' + esc(m.state.word) + '</span><span class="why">' + esc(m.state.why) + '</span></span>' +
        '<button class="chev-btn' + (open ? ' open' : '') + '" aria-expanded="' + (open ? 'true' : 'false') + '" aria-label="Details"><span class="chev"></span></button></div>' +
        (open ? rowDetails(e, m) : '') + '</div>';
    }).join('');
  }
  function rowDetails(e, m) {
    var rows = [];
    if (!m.synthetic) rows.push(['hash', e.hash, 'hash']);
    if (e.from) rows.push(['from', e.from, 'address']);
    if (e.to) rows.push(['to', e.to, 'address']);
    if (!m.synthetic) rows.push(['fee paid', e.fee && e.fee !== '0' ? View.ign(e.fee, 9) + ' IGN' : e.block ? 'none' : 'pending', '']);
    rows.push(['block', e.block ? View.withCommas(e.block) + (e.block_hash ? ' · ' + View.shortHex(e.block_hash, 6, 4) : '') : 'none yet', '']);
    if (e.checkpoint) rows.push(['checkpoint', View.withCommas(e.checkpoint), 'verified by this wallet']);
    if (e.note) rows.push(['note', e.note, '']);
    rows.push(['the node says', m.nodeWord || (m.synthetic ? 'a block reward has no transaction' : 'not asked yet'), 'igneum_getTransactionStatus, one word']);
    return '<div class="hr-details"><div class="kv">' + rows.map(function (r) {
      var copy = r[2] === 'hash' || r[2] === 'address' ? ' <button class="btn tiny" data-copy-text="' + esc(r[1]) + '">Copy</button>' : '';
      return '<div><span class="k">' + esc(r[0]) + '</span><span class="v mono">' + esc(r[1]) + copy + '</span>' + (copy ? '' : '<span class="m">' + esc(r[2] || '') + '</span>') + '</div>';
    }).join('') + '</div></div>';
  }
  document.addEventListener('click', function (e) {
    if (e.target.closest('[data-copy-text]')) return;
    var main = e.target.closest('.hrow:not(.sent-row) .hr-main'); if (!main) return;
    var h = main.parentNode.dataset.hash.toLowerCase();
    openRows[h] = !openRows[h];
    if (state) renderPage(state);
  });
  // the node's one word per transaction, from GET /api/tx/<hash> (node_status.state), for the rows that are not final
  async function refreshNodeWords(s) {
    if (fetching) return;
    var want = View.needsNodeWord(s.history, 12), now = Date.now() / 1000;
    var due = want.filter(function (h) { var w = nodeWord[h.toLowerCase()]; return !w || now - w.at > (w.word ? 4 : 30); });
    if (!due.length) return;
    fetching = true;
    try {
      for (var i = 0; i < due.length; i++) {
        var h = due[i].toLowerCase(), word = '';
        try { var r = await api('/api/tx/' + due[i]); word = (r.node_status && r.node_status.state) || ''; } catch (e) { word = ''; }
        nodeWord[h] = { word: word, at: Date.now() / 1000 };
      }
    } finally { fetching = false; }
    if (state) renderRowsOnly(state);
  }
  function renderRowsOnly(s) {
    if (page === 'home') renderRows($('recent'), s.history, 5, s);
    else if (page === 'history') renderRows($('history'), s.history, 0, s);
    else if (page === 'send') renderSentRow(s);
  }

  // ---------- Send: the form, the question in place, the pending row ----------
  function renderSend(s) {
    var bl = View.balanceLine(s);
    setText('send-balance', bl.known ? 'of ' + s.balance + ' IGN' : '');
    setText('send-eyebrow', s.node && s.node.state === 'ok' ? '' : 'needs a node');
    renderSentRow(s);
  }
  function resetSend() {
    quote = null; sentHash = null;
    $('send-form').hidden = false; $('send-form').dataset.locked = '';
    $('send-to').value = ''; $('send-amount').value = '';
    $('send-err').textContent = ''; $('confirm-send-err').textContent = ''; setLine($('send-bio-line'), '');
    setText('fee-line', View.feeWords(null).line); $('fee-toggle').hidden = true; $('fee-details').hidden = true; $('fee-toggle').setAttribute('aria-expanded', 'false');
    $('send-cta').hidden = false; $('ask-send').hidden = true; $('send-done').hidden = true;
  }
  function cancelAsk() { $('ask-send').hidden = true; $('send-cta').hidden = false; $('send-form').dataset.locked = ''; quote = null; $('send-to').focus({ preventScroll: true }); }
  $('ask-send-no').onclick = cancelAsk;
  $('send-quote').onclick = async function () {
    $('send-err').textContent = '';
    try {
      quote = await post('/api/send/quote', { to: $('send-to').value.trim(), amount: $('send-amount').value.trim() });
      var fw = View.feeWords(quote);
      setText('fee-line', fw.line); setText('fee-details', fw.detail); $('fee-toggle').hidden = false;
      var needs = !!quote.confirm_needed && !!bio.host;
      var ask = View.sendAsk(quote, needs, what());
      setText('ask-send-text', ask.text); setText('send-go-text', ask.button);
      $('send-go').querySelector('.fp-ico').hidden = !needs;
      $('confirm-send-err').textContent = quote.confirm_needed && !bio.host ? what() + ' is on for this wallet, and only the Igneum Wallet app window can show it.' : '';
      setLine($('send-bio-line'), '');
      $('send-form').dataset.locked = '1'; $('send-cta').hidden = true; $('ask-send').hidden = false; $('send-go').disabled = false;
      $('send-go').focus({ preventScroll: true });
    } catch (e) { $('send-err').textContent = e.message; }
  };
  $('fee-toggle').onclick = function () { var open = this.getAttribute('aria-expanded') !== 'true'; this.setAttribute('aria-expanded', open ? 'true' : 'false'); $('fee-details').hidden = !open; this.textContent = open ? 'Less' : 'Details'; };
  ['send-to', 'send-amount'].forEach(function (id) { $(id).addEventListener('keydown', function (e) { if (e.key === 'Enter' && !$('send-form').dataset.locked) { e.preventDefault(); $('send-quote').click(); } }); });
  $('send-go').onclick = async function () {
    if (!quote) return;
    $('confirm-send-err').textContent = ''; $('send-go').disabled = true;
    try {
      var q = Object.assign({}, quote);
      ['ok', 'value_ign', 'fee_max_ign', 'total_max_ign', 'base_fee_gwei', 'tip_gwei', 'display_to', 'confirm_nonce', 'confirm_reason', 'confirm_needed'].forEach(function (k) { delete q[k]; });
      if (quote.confirm_needed) {
        setLine($('send-bio-line'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
        var c = await bio.call('confirm', { nonce: quote.confirm_nonce });
        setLine($('send-bio-line'), bioLine(c, 'confirmed, sending'));
        if (!c.ok) { $('send-go').disabled = false; return; }
      }
      var r = await post('/api/send', { quote: q, nonce: quote.confirm_nonce });
      sentHash = r.hash; setText('sent-hash', r.hash);
      $('ask-send').hidden = true; $('send-form').hidden = true; $('send-done').hidden = false;
      $('send-to').value = ''; $('send-amount').value = ''; $('send-form').dataset.locked = ''; quote = null;
      await poll();
    } catch (e) { $('confirm-send-err').textContent = e.message; }
    $('send-go').disabled = false;
  };
  function renderSentRow(s) {
    if (!sentHash || $('send-done').hidden) return;
    var e = (s.history || []).filter(function (x) { return x.hash.toLowerCase() === sentHash.toLowerCase(); })[0];
    var el = $('sent-row');
    if (!e) { el.innerHTML = '<div class="hr-main"><span class="glyph" aria-hidden="true">↑</span><span class="hr-who"><span class="kind">Sent</span><span class="sub">handed to the node</span></span><span class="hr-amt"></span><span class="hr-state"><span class="st"><i></i>pending</span><span class="why">waiting for a block</span></span></div>'; return; }
    var nw = nodeWord[e.hash.toLowerCase()], m = View.rowModel(e, nw && nw.word, s.now);
    el.className = 'hrow sent-row' + (m.in ? ' in' : '');
    el.innerHTML = '<div class="hr-main"><span class="glyph" aria-hidden="true">↑</span><span class="hr-who"><span class="kind">' + esc(m.kind) + '</span><span class="sub">' + esc(m.who) + (m.when ? ' · ' + esc(m.when) : '') + '</span></span><span class="hr-amt">' + esc(m.amount) + '<span class="unit">IGN</span></span><span class="hr-state"><span class="st ' + esc(m.state.tone) + '"><i></i>' + esc(m.state.word) + '</span><span class="why">' + esc(m.state.why) + '</span></span></div>';
  }
  $('sent-view').onclick = function () { if (sentHash) openRows[sentHash.toLowerCase()] = true; showPage('history'); };
  $('sent-again').onclick = function () { resetSend(); $('send-to').focus({ preventScroll: true }); };

  // ---------- Receive ----------
  var receiveFor = '';
  function renderReceive(s) { setText('receive-address', s.display || 'not set'); }
  async function loadReceive() {
    if (!state || !state.display || receiveFor === state.display) return;
    try { var r = await post('/api/receive'); $('qr').innerHTML = r.svg; receiveFor = state.display; }
    catch (e) { toast(e.message); }
  }

  // ---------- create ----------
  $('go-create').onclick = function () { setPhase('create'); $('create-1').hidden = false; $('create-2').hidden = true; $('create-3').hidden = true; $('create-pw').focus(); };
  $('create-back').onclick = function () { setPhase('welcome'); };
  var words = [];
  $('create-next').onclick = async function () {
    $('create-err').textContent = '';
    var a = $('create-pw').value, b = $('create-pw2').value;
    if (a.length < 8) { $('create-err').textContent = 'at least 8 characters'; return; }
    if (a !== b) { $('create-err').textContent = 'the two passwords differ'; return; }
    try {
      var r = await post('/api/create', { password: a });
      words = r.words;
      $('words').innerHTML = words.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('');
      $('create-address').textContent = r.display;
      $('create-1').hidden = true; $('create-2').hidden = false;
    } catch (e) { $('create-err').textContent = e.message; }
  };
  $('create-written').onclick = function () {
    var picks = []; while (picks.length < 3) { var p = 1 + Math.floor(Math.random() * 24); if (picks.indexOf(p) < 0) picks.push(p); }
    picks.sort(function (a, b) { return a - b; });
    $('checks').innerHTML = picks.map(function (p) { return '<label>word ' + p + '<input type="text" data-pos="' + p + '" autocomplete="off" autocapitalize="none" spellcheck="false"></label>'; }).join('');
    $('words').innerHTML = ''; words = [];
    $('create-2').hidden = true; $('create-3').hidden = false;
    $('checks').querySelector('input').focus();
  };
  $('create-again').onclick = function () { $('create-3').hidden = true; $('create-1').hidden = false; $('confirm-err').textContent = ''; $('create-err').textContent = 'start again: the words are made fresh each time'; };
  $('create-confirm').onclick = async function () {
    $('confirm-err').textContent = '';
    var checks = Array.prototype.map.call($('checks').querySelectorAll('input'), function (i) { return { position: Number(i.dataset.pos), word: i.value.trim() }; });
    try { await post('/api/create/confirm', { checks: checks }); $('checks').innerHTML = ''; await poll(); setPhase('home'); showPage('home'); toast('Wallet ready'); }
    catch (e) { $('confirm-err').textContent = e.message; }
  };

  // ---------- import ----------
  var importMode = 'seed';
  $('go-import').onclick = function () { setPhase('import'); };
  $('import-back').onclick = function () { setPhase('welcome'); };
  $('import-mode').addEventListener('click', function (e) {
    var b = e.target.closest('.seg-b'); if (!b || b.disabled) return;
    importMode = b.dataset.mode;
    document.querySelectorAll('#import-mode .seg-b').forEach(function (x) { var on = x === b; x.classList.toggle('on', on); x.setAttribute('aria-checked', on ? 'true' : 'false'); });
    $('import-data-field').hidden = importMode === 'miner';
    $('import-miner-note').hidden = importMode !== 'miner';
    setText('import-data-label', importMode === 'seed' ? 'The words, in order' : 'The private key, 64 hex characters');
  });
  $('import-go').onclick = async function () {
    $('import-err').textContent = '';
    var a = $('import-pw').value, b = $('import-pw2').value;
    if (a.length < 8) { $('import-err').textContent = 'at least 8 characters'; return; }
    if (a !== b) { $('import-err').textContent = 'the two passwords differ'; return; }
    try { await post('/api/import', { mode: importMode, data: $('import-data').value, password: a }); $('import-data').value = ''; await poll(); setPhase('home'); showPage('home'); toast('Imported'); }
    catch (e) { $('import-err').textContent = e.message; }
  };

  // ---------- Settings ----------
  // ---------- the page bridge (0.1.6): a website's request as a card, the Settings card ----------
  var bridgeOpen = null, bridgeBusy = false, bridgeDoneId = null, bridgeSig = '';
  function renderBridge(s) {
    var br = s.bridge || {}, list = (br.pending || []), wrap = $('bridge');
    var ul = $('unlock-bridge'); if (ul) { var wl = View.bridgeWaiting(list); ul.textContent = wl; ul.hidden = !wl; }
    if (s.phase !== 'home') { if (!wrap.hidden) { wrap.hidden = true; bridgeOpen = null; bridgeSig = ''; } return; }
    if (bridgeDoneId) return; // the sent view stays until Done
    var p = list[0] || null;
    if (!p) { if (!wrap.hidden) { wrap.hidden = true; bridgeOpen = null; bridgeSig = ''; } return; }
    var words = View.bridgeWords(p, p.confirm_needed ? what() : ''), sig = JSON.stringify([p.id, words, p.confirm_nonce]);
    if (sig === bridgeSig) return;
    bridgeSig = sig; bridgeOpen = p;
    setText('bridge-eyebrow', p.kind === 'connect' ? 'a website asks to connect' : p.kind === 'send' ? 'a website asks you to send' : 'a website asks you to sign');
    setText('bridge-title', words.title);
    $('bridge-amount').hidden = !words.amount; setText('bridge-amount', words.amount || '');
    $('bridge-message').hidden = !words.message; $('bridge-message').textContent = words.message || '';
    $('bridge-lines').innerHTML = words.lines.map(function (l) { return '<li>' + esc(l) + '</li>'; }).join('');
    setText('bridge-origin', p.origin);
    setText('bridge-yes-text', words.yes); $('bridge-yes').querySelector('.fp-ico').hidden = !(p.confirm_needed && bio.host);
    $('bridge-yes').disabled = p.kind === 'send' && !p.fee_max_ign; $('bridge-no').textContent = words.no;
    $('bridge-err').textContent = p.confirm_needed && !bio.host ? what() + ' is on for this wallet, and only the Igneum Wallet app window can show it.' : '';
    setLine($('bridge-bio-line'), '');
    $('bridge-done').hidden = true; $('bridge-actions').hidden = false;
    if (wrap.hidden) { wrap.hidden = false; $('bridge-card').focus({ preventScroll: true }); }
  }
  async function bridgeDecide(ok) {
    var p = bridgeOpen; if (!p || bridgeBusy) return;
    bridgeBusy = true; $('bridge-yes').disabled = true; $('bridge-no').disabled = true; $('bridge-err').textContent = '';
    try {
      var nonce = p.confirm_nonce || '';
      if (ok && p.confirm_needed) {
        setLine($('bridge-bio-line'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
        var c = await bio.call('confirm', { nonce: nonce });
        setLine($('bridge-bio-line'), bioLine(c, 'confirmed'));
        if (!c.ok) { bridgeBusy = false; $('bridge-yes').disabled = false; $('bridge-no').disabled = false; return; }
      }
      var r = await post('/api/bridge/decide', { id: p.id, ok: ok, nonce: nonce });
      if (ok && p.kind === 'send' && r.result && r.result.hash) {
        bridgeDoneId = r.result.hash; $('bridge-actions').hidden = true; $('bridge-done').hidden = false;
        setText('bridge-done-line', 'Sent for ' + View.hostOf(p.origin) + '. The page has the hash; the chain\u2019s word follows here.');
        $('bridge-hash-box').hidden = false; setText('bridge-hash', r.result.hash); $('bridge-final').innerHTML = '';
        bridgeFinality(r.result.hash);
      } else {
        $('bridge').hidden = true; bridgeOpen = null; bridgeSig = '';
        toast(ok ? (p.kind === 'connect' ? View.hostOf(p.origin) + ' connected' : 'Signed for ' + View.hostOf(p.origin)) : 'Declined');
      }
    } catch (e) { $('bridge-err').textContent = e.message; }
    bridgeBusy = false; $('bridge-yes').disabled = false; $('bridge-no').disabled = false;
    poll();
  }
  // the finality certificate's line after a page's transaction: the node's word, then the checkpoint this wallet verified
  async function bridgeFinality(hash) {
    for (var i = 0; i < 90 && bridgeDoneId === hash; i++) {
      try {
        var r = await api('/api/tx/' + hash), st = (r.node_status && r.node_status.state) || 'pending', e = (state && state.history || []).filter(function (x) { return x.hash.toLowerCase() === hash.toLowerCase(); })[0];
        var rows = [['the node says', st], ['block', e && e.block ? View.withCommas(e.block) : 'none yet']];
        if (e && e.checkpoint) rows.push(['checkpoint', View.withCommas(e.checkpoint) + ', verified by this wallet']);
        else if (r.verified && r.verified.verified_index) rows.push(['finality', 'the wallet verified checkpoint ' + View.withCommas(r.verified.verified_index) + '; this transaction is final once a locked checkpoint covers its block']);
        $('bridge-final').innerHTML = rows.map(function (x) { return '<div><span class="k">' + esc(x[0]) + '</span><span class="v mono">' + esc(x[1]) + '</span></div>'; }).join('');
        if (e && e.finality === 'final') break;
      } catch (x) { }
      await new Promise(function (res) { setTimeout(res, 2000); });
    }
  }
  $('bridge-yes').onclick = function () { bridgeDecide(true); };
  $('bridge-no').onclick = function () { bridgeDecide(false); };
  $('bridge-close').onclick = function () { bridgeDoneId = null; $('bridge').hidden = true; bridgeOpen = null; bridgeSig = ''; if (state) renderBridge(state); };
  $('bridge-on').addEventListener('change', function () { var on = this.checked; post('/api/bridge/on', { on: on }).then(function () { toast(on ? 'Websites can connect after your approval' : 'Websites cannot connect'); poll(); }).catch(function (e) { toast(e.message); }); });
  $('sites').addEventListener('click', function (e) { var b = e.target.closest('[data-site]'); if (!b) return; var o = b.dataset.site; post('/api/bridge/sites/remove', { origin: o }).then(function () { toast(View.hostOf(o) + ' disconnected'); poll(); }).catch(function (x) { toast(x.message); }); });
  function renderSites(s) {
    var br = s.bridge || {};
    if (document.activeElement !== $('bridge-on')) $('bridge-on').checked = br.on !== false;
    setText('bridge-status', View.bridgeStatus(br));
    var rows = View.siteRows(br.sites, s.now);
    $('sites').innerHTML = rows.length ? rows.map(function (r) { return '<div class="srow"><div class="sw-text"><b>' + esc(r.host) + '</b> <span class="dim">' + esc(r.line) + '</span></div><div class="row"><button class="btn small ghost" data-site="' + esc(r.origin) + '">Disconnect</button></div></div>'; }).join('') : '<p class="note">No website is connected.</p>';
  }
  function renderSettings(s) {
    renderSites(s);
    var b = s.biometric || {}, w = what(), host = hostHere();
    setText('bio-title', w);
    setText('bio-sentence', View.bioSentence(b, w, host));
    setText('bio-toggle-btn', b.enrolled ? 'Turn off' : 'Turn on');
    $('bio-toggle-btn').disabled = !b.enrolled && (!host || !b.available);
    $('bio-toggle-btn').classList.toggle('danger', !!b.enrolled);
    if (b.enrolled) $('bio-enrol').hidden = true;
    $('bio-off-note').hidden = true;
    setText('bio-enrol-text', 'Turn on ' + w);
    $('ask-on-open-row').hidden = !b.enrolled; $('idle-row').hidden = !b.enrolled;
    setText('ask-on-open-text', 'Ask for ' + w + ' when the wallet opens');
    var idleMin = s.settings && s.settings.idle_lock_min != null ? s.settings.idle_lock_min : (b.idle_lock ? (b.idle_lock_min || 5) : 0);
    if (document.activeElement !== $('idle-lock')) $('idle-lock').value = String(LockScreen.IDLE_CHOICES.indexOf(Number(idleMin)) >= 0 ? idleMin : LockScreen.IDLE_DEFAULT_MIN);
    setText('idle-lock-note', View.idleSentence(idleMin, w));
    if (document.activeElement !== $('ask-on-open')) $('ask-on-open').checked = !!(s.settings && s.settings.ask_on_open);
    if (b.needs_enrol && !$('bio-err').textContent) $('bio-err').textContent = 'The password changed, so ' + w + ' was turned off. Turn it on again here.';
    var touch = !!(b.enrolled && bio.host);
    $('backup-touch').hidden = !touch; $('backup-touch').querySelector('span').textContent = 'Show with ' + w;
    $('export-touch').hidden = !touch; $('export-touch').querySelector('span').textContent = 'Show with ' + w;
    setText('backup-eyebrow', s.backed_up ? 'written down' : 'not written down yet');
    if (document.activeElement !== $('start-login')) $('start-login').checked = !!s.settings.start_at_login;
    // the update card
    var u = s.update || {};
    setText('s-version', 'Igneum Wallet ' + s.version);
    setText('s-update-note', View.updateNote(u, s.now));
    var ul = View.updateLine(u);
    $('s-install').hidden = !(ul && ul.install) || u.applying;
    if (document.activeElement !== $('auto-update')) $('auto-update').checked = !!s.settings.auto_update;
    // the node
    var n = s.node || {};
    setText('node-endpoint', n.evm || 'none yet');
    setText('node-source-line', View.cap(n.message || ''));
    setText('node-eyebrow', View.sourceWords(n.source));
    setText('node-ids', 'chain id ' + (n.chain_id || '…') + ' · gRPC ' + (n.grpc || 'none') + ' · public RPC ' + (s.public_rpc || 'none in this build'));
    // advanced
    setText('s-mid', 'machine id ' + (s.machine_id || '').slice(0, 8));
    setText('s-dirs', 'logs ' + s.log_dir + ' · wallet ' + s.app_dir);
    setText('s-miner-file', s.miner_wallet_present ? "The miner app's key file is on this machine; Import can read it." : "No miner key file on this machine.");
  }
  $('bio-toggle-btn').onclick = async function () {
    var b = (state && state.biometric) || {};
    if (b.enrolled) { try { await post('/api/biometric/remove'); $('bio-err').textContent = ''; toast(what() + ' is off'); await poll(); } catch (e) { $('bio-err').textContent = e.message; } return; }
    $('bio-enrol').hidden = false; $('bio-pw').focus({ preventScroll: true });
  };
  $('bio-enrol-cancel').onclick = function () { $('bio-enrol').hidden = true; $('bio-pw').value = ''; $('bio-err').textContent = ''; };
  $('bio-enrol-go').onclick = async function () {
    $('bio-err').textContent = '';
    var pw = $('bio-pw').value;
    if (!pw) { $('bio-err').textContent = 'type the password first'; return; }
    $('bio-enrol-go').disabled = true;
    try {
      var r = await post('/api/biometric/enrol/begin', { password: pw });
      setLine($('bio-err'), '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
      var h = await bio.call('enrol', { token: r.token });
      setLine($('bio-err'), bioLine(h, 'is on'));
      if (!h.ok) { post('/api/biometric/enrol/cancel').catch(function () {}); $('bio-enrol-go').disabled = false; return; }
      $('bio-pw').value = ''; $('bio-enrol').hidden = true; toast(what() + ' is on'); await poll();
    } catch (e) { $('bio-err').textContent = e.message; }
    $('bio-enrol-go').disabled = false;
  };
  $('idle-lock').onchange = async function (ev) { try { await post('/api/settings', { idle_lock_min: Number(ev.target.value) }); await poll(); } catch (e) { toast(e.message); } };
  $('ask-on-open').onchange = async function (ev) { try { await post('/api/settings', { ask_on_open: ev.target.checked }); } catch (e) { toast(e.message); } };
  $('pw-change').onclick = async function () {
    $('pw-err').textContent = '';
    try { await post('/api/password', { old: $('pw-old').value, new: $('pw-new').value }); $('pw-old').value = ''; $('pw-new').value = ''; toast('Password changed'); await poll(); }
    catch (e) { $('pw-err').textContent = e.message; }
  };
  // backup and export: the ask in place, then the password field or the prompt
  function resetBackup() { $('backup-pw-row').hidden = true; $('backup-pw').value = ''; $('backup-err').textContent = ''; $('backup-out').hidden = true; $('backup-words').innerHTML = ''; $('backup-key').textContent = ''; $('ask-backup').hidden = false; }
  function resetExport() { $('export-pw-row').hidden = true; $('export-pw').value = ''; $('export-err').textContent = ''; $('export-out').hidden = true; $('export-key').textContent = ''; $('ask-export').hidden = false; }
  function closeDisclosure(id) { var t = $(id); t.setAttribute('aria-expanded', 'false'); $(t.getAttribute('aria-controls')).hidden = true; }
  $('backup-cancel').onclick = function () { resetBackup(); closeDisclosure('backup-toggle'); };
  $('export-cancel').onclick = function () { resetExport(); closeDisclosure('export-toggle'); };
  $('backup-use-pw').onclick = function () { $('backup-pw-row').hidden = false; $('backup-pw').focus({ preventScroll: true }); };
  $('export-use-pw').onclick = function () { $('export-pw-row').hidden = false; $('export-pw').focus({ preventScroll: true }); };
  function showBackup(r) {
    $('backup-words').innerHTML = (r.words || []).map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('');
    $('backup-key').textContent = r.private_key; $('backup-out').hidden = false; $('ask-backup').hidden = true; $('backup-pw-row').hidden = true;
    if (state && !state.backed_up) post('/api/backed-up').then(poll).catch(function () {});
  }
  $('backup-show').onclick = async function () {
    $('backup-err').textContent = '';
    try { var r = await post('/api/reveal', { password: $('backup-pw').value }); $('backup-pw').value = ''; showBackup(r); }
    catch (e) { $('backup-err').textContent = e.message; }
  };
  $('backup-pw').addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); $('backup-show').click(); } });
  $('export-pw').addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); $('export-show').click(); } });
  async function revealWithTouch(errEl) {
    errEl.textContent = '';
    var c = await post('/api/biometric/challenge', { purpose: 'reveal' });
    setLine(errEl, '<span class="bio-state wait">' + FP + esc('Waiting for ' + what()) + '</span>');
    var h = await bio.call('confirm', { nonce: c.nonce });
    setLine(errEl, bioLine(h, 'confirmed'));
    if (!h.ok) return null;
    var r = await post('/api/reveal', { nonce: c.nonce });
    errEl.textContent = '';
    return r;
  }
  $('backup-touch').onclick = async function () { try { var r = await revealWithTouch($('backup-err')); if (r) showBackup(r); } catch (e) { $('backup-err').textContent = e.message; } };
  $('export-touch').onclick = async function () { try { var r = await revealWithTouch($('export-err')); if (r) { $('export-key').textContent = r.private_key; $('export-out').hidden = false; $('ask-export').hidden = true; } } catch (e) { $('export-err').textContent = e.message; } };
  $('export-show').onclick = async function () {
    $('export-err').textContent = '';
    try { var r = await post('/api/reveal', { password: $('export-pw').value }); $('export-pw').value = ''; $('export-key').textContent = r.private_key; $('export-out').hidden = false; $('ask-export').hidden = true; $('export-pw-row').hidden = true; }
    catch (e) { $('export-err').textContent = e.message; }
  };
  $('backup-hide').onclick = function () { resetBackup(); };
  $('export-hide').onclick = function () { resetExport(); };
  // MetaMask
  var netCache = null;
  async function network() { if (!netCache) netCache = await api('/api/network'); return netCache; }
  async function renderNetwork() {
    try {
      var n = await network();
      setText('net-line', n.chain_name + ' · chain id ' + n.chain_id + ' · ' + (n.rpc_url || 'no RPC yet'));
      $('net-add').disabled = !n.add_network_page;
      $('net-add').title = n.add_network_page ? '' : 'the site page is not set in this build; copy the settings instead';
    } catch (e) { setText('net-line', e.message); }
  }
  $('net-add').onclick = async function () { var n = await network(); if (n.add_network_page) post('/api/open', { url: n.add_network_page }); };
  $('net-copy').onclick = async function () { var n = await network(); copyText('Network name: ' + n.chain_name + '\nRPC URL: ' + n.rpc_url + '\nChain ID: ' + n.chain_id + '\nCurrency symbol: IGN\nDecimals: 18', 'Network settings'); };
  $('start-login').onchange = async function (ev) { try { await post('/api/settings', { start_at_login: ev.target.checked }); } catch (e) { toast(e.message); } };
  $('auto-update').onchange = async function (ev) { try { await post('/api/update/auto', { on: ev.target.checked }); } catch (e) { toast(e.message); } };
  $('s-update').onclick = function () { post('/api/update/check').catch(function () {}); toast('Checking for updates'); };
  $('s-install').onclick = function () { post('/api/update/install').catch(function () {}); toast('Installing now'); };
  // remove: the ask in place, never a dialog
  $('remove-start').onclick = function () { $('ask-remove').hidden = false; $('remove-pw').focus({ preventScroll: true }); };
  $('ask-remove-no').onclick = function () { $('ask-remove').hidden = true; $('remove-pw').value = ''; $('remove-err').textContent = ''; };
  $('remove-go').onclick = async function () {
    $('remove-err').textContent = '';
    try { await post('/api/remove', { password: $('remove-pw').value }); $('remove-pw').value = ''; $('ask-remove').hidden = true; await poll(); }
    catch (e) { $('remove-err').textContent = e.message; }
  };
  $('remove-pw').addEventListener('keydown', function (e) { if (e.key === 'Enter') { e.preventDefault(); $('remove-go').click(); } });
  new MutationObserver(function () { if (document.body.dataset.page === 'settings' && document.body.dataset.phase === 'home') renderNetwork(); }).observe(document.body, { attributes: true, attributeFilter: ['data-page', 'data-phase'] });

  // ---------- the update strip (one notice) and the update card ----------
  var stripH = 0, noticeSig = '';
  function renderNotices(s) {
    var u = s.update, l = View.updateLine(u), strip = $('notices');
    var key = u.status + ':' + u.version;
    var show = !!l && (u.urgent || u.applying || sessionStorage.getItem('update-later') !== key);
    if (!show) { if (!strip.hidden) { strip.hidden = true; noticeSig = ''; layoutStrip(); } return; }
    var acts = [];
    if (l.install && !u.applying) acts.push({ act: 'install', label: 'Install now', primary: true });
    if (l.open) acts.push({ act: 'open', label: 'Open the download', primary: true });
    var sig = [key, l.text, l.urgent ? 'u' : '', l.prog ? 'p' : '', JSON.stringify(acts)].join('|');
    if (sig !== noticeSig) {
      noticeSig = sig;
      $('notice').className = 'notice' + (l.urgent ? ' urgent' : '');
      $('notice').dataset.key = key;
      setText('notice-text', l.text);
      $('notice-actions').innerHTML = acts.map(function (a) { return '<button class="btn small ' + (a.primary ? 'primary' : 'ghost') + '" data-act="' + a.act + '">' + esc(a.label) + '</button>'; }).join('');
      $('notice-close').hidden = !!l.urgent || !!u.applying;
      $('notice-prog').hidden = !l.prog;
      strip.hidden = false;
    }
    if (l.prog) $('notice-prog').firstElementChild.style.width = Math.round((u.progress || 0) * 100) + '%';
    layoutStrip();
  }
  function layoutStrip() {
    var strip = $('notices'), h = strip.hidden ? 0 : strip.offsetHeight;
    if (h === stripH) return;
    stripH = h;
    var top = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--top'), 10) || 60;
    $('main').style.top = h ? (top + h) + 'px' : '';
  }
  window.addEventListener('resize', layoutStrip);
  $('notice').addEventListener('click', function (e) {
    var b = e.target.closest('[data-act]'); if (!b) return;
    var act = b.dataset.act;
    if (act === 'install') { post('/api/update/install').catch(function () {}); toast('Installing now'); }
    else if (act === 'open') post('/api/update/open').catch(function () {});
  });
  $('notice-close').onclick = function () { sessionStorage.setItem('update-later', $('notice').dataset.key || ''); $('notices').hidden = true; noticeSig = ''; layoutStrip(); };

  // the update card (update-card.js): one update, centred, over every screen; mem: open, later, seen; asked: Install
  // now was pressed on this card and the engine has not flipped to applying yet
  var updMem = { open: false, later: '', seen: '' };
  var updSig = '', updAsked = '', updMore = false;
  var forcedUpdate = params.get('update');
  if (forcedUpdate && params.get('card') === '1') updMem.open = true;
  function sampleUpdate(kind) {
    var u = { status: 'ready', version: '0.1.6', url: '', notes: "The wallet on the miner's design: money first, one row per transaction with the node's state word, an Appearance setting, a bottom tab bar on a narrow window. Touch ID unchanged.", file: '', error: '', checked_at: Date.now() / 1000 - 40, available: true, downloaded: true, ready: true, applying: false, progress: 1, size: 20080717, auto: params.get('auto') !== '0', wait: 'installs as soon as nothing is being sent', urgent: false, urgent_text: '', unsupported: false, min_supported: '', updated_from: '', rolled_back: '' };
    if (!u.auto) u.wait = 'waiting for Install now (automatic updates are off)';
    if (kind === 'available') { u.status = 'available'; u.downloaded = false; u.ready = false; u.progress = 0; }
    if (kind === 'downloading') { u.status = 'downloading'; u.downloaded = false; u.ready = false; u.progress = Math.min(0.97, 0.43 + ((Date.now() / 1000) % 60) / 110); }
    if (kind === 'staging') { u.status = 'staging'; u.ready = false; }
    if (kind === 'applying') { u.status = 'applying'; u.applying = true; }
    if (kind === 'deferred') { u.status = 'deferred'; u.wait = 'waits for the next time someone is at this PC (Windows asks for permission)'; }
    if (kind === 'urgent') { u.status = 'downloading'; u.ready = false; u.downloaded = false; u.progress = 0.27; u.urgent = true; u.urgent_text = 'Igneum Wallet 0.1.4 is no longer supported by the network. Installing 0.1.6 now.'; }
    if (kind === 'manual') { u.status = 'manual'; u.ready = false; u.wait = '/Applications is not writable; open the downloaded disk image and drag the app over the old one'; }
    if (kind === 'error') { u.status = 'error'; u.error = 'sha256 mismatch: the file is not what the manifest signed'; u.downloaded = false; u.ready = false; }
    return u;
  }
  function cardCtx(s) {
    var line = Array.prototype.some.call(document.querySelectorAll('.bio-state'), function (e) { return e.offsetParent !== null; });
    return { phase: document.body.dataset.phase, view: document.body.dataset.phase === 'home' ? page : '', bioBusy: bio.busy, bioLine: line, bioName: what(), quitting: !!s.quitting };
  }
  function renderUpdateCard(s) {
    var m = UpdateCard.model(s.update, s), d = UpdateCard.decide(m, cardCtx(s), updMem), wrap = $('upd');
    if (!d.show) { if (updMem.open || !wrap.hidden) { updMem.open = false; wrap.hidden = true; updSig = ''; } return; }
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
      wrap.dataset.dismiss = m.dismissable ? '1' : '';
    }
    if (m.ring === 'progress') { $('upd-arc').style.strokeDashoffset = (276.5 * (1 - m.pct / 100)).toFixed(1); $('upd-pct').textContent = m.pct + '%'; $('upd-pct').hidden = m.stage !== 'downloading'; }
    else { $('upd-arc').style.strokeDashoffset = ''; $('upd-pct').hidden = true; }
    var meta = [];
    if (m.stage === 'downloading' && m.size) meta.push(m.pct + '% of ' + m.size);
    else if (m.size && m.stage === 'available') meta.push(m.size + ' download');
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
    else if (act === 'install' || act === 'retry') { post('/api/update/install').catch(function () {}); updAsked = m ? m.key : ''; updSig = ''; toast(act === 'retry' ? 'Trying the update again' : m && m.stage === 'ready' ? 'Installing now' : 'Installs as soon as it is downloaded'); if (state) renderUpdateCard(state); }
    else if (act === 'open') post('/api/update/open').catch(function () {});
  });
  $('upd-more').onclick = function () { updMore = !updMore; $('upd-all').hidden = !updMore; this.textContent = updMore ? 'Less' : 'What changed'; this.setAttribute('aria-expanded', updMore ? 'true' : 'false'); };

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) return;
    poll();
    if (state && state.phase === 'unlock' && !locking) focusLock();
  });

  resetSend();
  poll();
  setInterval(poll, 2000);
})();
