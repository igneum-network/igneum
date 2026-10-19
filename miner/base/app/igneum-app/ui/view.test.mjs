// node --test app/igneum-app/ui/view.test.mjs   (no dependencies; CI runs it in the site job)
// Loads the View block of app.js (plain browser JS: the file is run with `module` defined and no `document`, so only
// the pure blocks execute) and checks the words each page shows for a given state (miner-ui-2).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

const card = (over) => ({ key: 'nvidia:0:RTX 5090', name: 'NVIDIA GeForce RTX 5090', vendor: 'nvidia', kind: 'discrete', worker: 'CUDA', vram_mb: 32768, enabled: true, state: 'mining', hash_now: 124.3, hash_avg: 120, accepted: 3, rejected: 0, identities: 8, ids: [], prepared: true, restart_in_s: 0, message: '', reason: '', power_w: 410.2, power_limit_w: 460, power_default_w: 575, power_pct: 80, power_applied: true, temp_gpu: 61, temp_mem: 72, telemetry_at: 1, ...over });

test('the six sections and their order (miner-ui-4, app-ia-26, the Cards tab back in v0.2.1, the Ledger page of 12 October 2026)', () => {
  assert.deepEqual(V.PAGES.map((p) => p.id), ['mine', 'cards', 'tune', 'fleet', 'earnings', 'pools', 'settings']); // 0.2.8: the Fleet page between Tune and Earnings, in the rail only with a console set // the Pools page (DESIGN 2.7, 13 October 2026)
  assert.equal(V.page('tune').title, 'Tune');
  assert.equal(V.page('earnings').title, 'Earnings');
  assert.equal(V.page('nonsense').id, 'mine');
});

test('a GPU row: name, kind, the numbers where known, the switch', () => {
  const r = V.cardRow(card());
  assert.equal(r.name, 'NVIDIA GeForce RTX 5090');
  assert.equal(r.kindWord, 'Discrete');
  assert.equal(r.integrated, false);
  assert.equal(r.on, true);
  assert.equal(r.word, 'mining');
  assert.equal(r.tone, 'on');
  assert.equal(r.hash, '124');
  assert.equal(r.temp, '61 °C');
  assert.equal(r.tempTone, '');
  assert.equal(r.power, '410 W');
  assert.equal(r.cap, 460);
  assert.equal(r.meta, '32 GB · 3 blocks · next program ready');
  assert.equal(r.canToggle, true);
});

test('a GPU row: temperatures turn amber then red; unknown numbers are empty', () => {
  assert.equal(V.cardRow(card({ temp_mem: 92 })).tempTone, 'warm');
  assert.equal(V.cardRow(card({ temp_mem: 96 })).tempTone, 'hot');
  assert.equal(V.cardRow(card({ temp_gpu: 93 })).tempTone, 'hot');
  const apple = V.cardRow(card({ key: 'apple::Apple M5 Max', name: 'Apple M5 Max', vendor: 'apple', kind: 'apple', worker: 'Metal', vram_mb: 131072, power_w: 0, power_default_w: 0, temp_gpu: 0, temp_mem: 0, telemetry_at: 0, hash_now: 7.4, accepted: 0, prepared: false }));
  assert.equal(apple.kindWord, 'Apple silicon');
  assert.equal(apple.hash, '7.4');
  assert.equal(apple.temp, '');
  assert.equal(apple.power, '');
  assert.equal(apple.cap, 0);
  assert.equal(apple.meta, '128 GB unified');
});

test('an Intel Arc card is a discrete Intel row: INTEL badge, no power reading sentence, measure-only tune words (intel-arc, 7 October 2026)', () => {
  const arc = card({ key: 'intel:Intel(R) Arc(TM) B580 Graphics', name: 'Intel(R) Arc(TM) B580 Graphics', vendor: 'intel', kind: 'discrete', worker: 'OpenCL', vram_mb: 12208, enabled: true, state: 'mining', hash_now: 15.2, power_w: 0, temp_gpu: 0, sweep_supported: false, sweep_state: 'unsupported', sweep_note: 'not available: Intel Arc exposes no power cap or reading through OpenCL (the driver\'s IGCL counters come next)' });
  const r = V.cardRow(arc);
  assert.equal(r.vendor, 'intel');
  assert.equal(r.kindWord, 'Discrete');
  assert.equal(r.integrated, false);
  assert.equal(r.on, true);
  assert.ok(r.meta.startsWith('12 GB'), r.meta);
  assert.equal(r.power, '', 'no watts cell without a reading');
  assert.equal(V.noReading(arc), 'No power or temperature reading on Intel Arc yet: the driver exposes them through IGCL, not OpenCL');
  assert.equal(V.tunable(arc), false, 'no cap, no clock control: not tunable');
  assert.equal(V.proveTier(arc), 'Intel(R) Arc(TM) B580 Graphics: cannot prove yet (proving needs an NVIDIA card).');
  const tm = mod.exports.TuneLine.model(arc, 1000);
  assert.equal(tm.kind, 'off');
  assert.equal(tm.text, 'tuning: measure only on Intel Arc once a power reading exists');
});

test('driver-check: the row\'s driver strip per state (offer, running, reboot, error, note, fine)', () => {
  const offer = { vendor: 'intel', status: 'missing', installed: '', wanted: '32.0.101.9034', min_version: '32.0.101.9034', size: 932631144, url: 'https://downloadmirror.intel.com/929959/gfx_win_101.9034.exe', hash_source: 'https://www.intel.com/content/www/us/en/download/785597/', text: 'Install the Intel Arc driver 32.0.101.9034 (889 MB). No driver was found, so this card cannot mine yet.', installable: true };
  const arc = card({ key: 'intel:Intel(R) Arc(TM) B580 Graphics', name: 'Intel(R) Arc(TM) B580 Graphics', vendor: 'intel', kind: 'discrete', worker: 'OpenCL', driver_os: '', driver_offer: offer });
  const idle = { drivers: { status: 'idle', vendor: '', version: '', progress: 0, message: '', error: '', reboot_required: false, dry_run: false } };
  let w = V.driverWords(arc, idle);
  assert.equal(w.kind, 'offer');
  assert.equal(w.button, 'install');
  assert.equal(w.text, offer.text);
  assert.ok(w.sub.startsWith('Downloaded from downloadmirror.intel.com, checked against the vendor'), w.sub);
  // running: the progress line names the vendor, the version and the percent while downloading
  w = V.driverWords(arc, { drivers: { status: 'downloading', vendor: 'intel', version: '32.0.101.9034', progress: 0.42, message: 'downloading 32.0.101.9034 (889 MB) from downloadmirror.intel.com', dry_run: false } });
  assert.equal(w.kind, 'running');
  assert.equal(w.text, 'Downloading the Intel Arc driver 32.0.101.9034 · 42%');
  w = V.driverWords(arc, { drivers: { status: 'installing', vendor: 'intel', version: '32.0.101.9034', progress: 0.7, message: 'installing INTEL 32.0.101.9034: Windows asks for permission once', dry_run: true } });
  assert.equal(w.text, 'Installing the Intel Arc driver 32.0.101.9034');
  assert.equal(w.dry, true);
  // reboot: the Restart now button, mining continues until the click
  w = V.driverWords(arc, { drivers: { status: 'reboot', vendor: 'intel', version: '32.0.101.9034', reboot_required: true } });
  assert.equal(w.kind, 'reboot');
  assert.equal(w.button, 'restart');
  assert.equal(w.text, 'Intel Arc driver 32.0.101.9034 installed. Restart Windows to finish.');
  // error: the reason and Install again
  w = V.driverWords(arc, { drivers: { status: 'error', vendor: 'intel', version: '32.0.101.9034', error: 'sha256 mismatch: the file is not the one the table names' } });
  assert.equal(w.kind, 'error');
  assert.equal(w.button, 'install');
  assert.ok(w.text.startsWith('The Intel Arc driver did not install: sha256 mismatch'), w.text);
  // another vendor\'s install does not touch this row
  w = V.driverWords(arc, { drivers: { status: 'downloading', vendor: 'nvidia', version: '617.42', progress: 0.1 } });
  assert.equal(w.kind, 'offer');
  // a note (macOS, Linux, HiveOS): no button
  const mac = card({ vendor: 'apple', kind: 'apple', driver_offer: { vendor: 'apple', status: 'none', text: 'Apple silicon: no driver step, macOS carries it.', installable: false } });
  w = V.driverWords(mac, idle);
  assert.equal(w.kind, 'note');
  assert.equal(w.button, undefined);
  const linux = card({ driver_offer: { vendor: 'nvidia', status: 'missing', text: 'Linux: install the package nvidia-driver-570 (the worker needs 570.00 or newer).', installable: false } });
  assert.equal(V.driverWords(linux, idle).kind, 'note');
  // fine: nothing on the row (the details panel carries the sentence)
  const fine = card({ driver_os: '617.42', driver_offer: { vendor: 'nvidia', status: 'fine', text: 'NVIDIA driver 617.42: fine.', installable: false } });
  assert.equal(V.driverWords(fine, idle).kind, 'fine');
  // no table: nothing
  assert.equal(V.driverWords(card({ driver_offer: null }), idle).kind, 'none');
  assert.equal(V.vendorWord('amd'), 'AMD');
});

test('driver-check: an install offer says every card of the vendor stops, and an eGPU card adds the machine warning (the second Windows machine, 7 October 2026; every-card rule an earlier release)', () => {
  const offer = { vendor: 'intel', status: 'old', installed: '32.0.101.6733', wanted: '32.0.101.9034', min_version: '32.0.101.9034', size: 932631144, url: 'https://downloadmirror.intel.com/929959/gfx_win_101.9034.exe', hash_source: 'https://www.intel.com/content/www/us/en/download/785597/', text: 'Install the Intel Arc driver 32.0.101.9034, 889 MB (the worker needs 32.0.101.9034 or newer; 32.0.101.6733 is installed)', installable: true };
  const idle = { drivers: { status: 'idle', vendor: '', version: '', progress: 0, message: '', error: '', reboot_required: false, dry_run: false } };
  const egpu = card({ key: 'intel:Intel(R) Arc(TM) B580 Graphics', name: 'Intel(R) Arc(TM) B580 Graphics', vendor: 'intel', kind: 'external', worker: 'OpenCL', enabled: true, state: 'mining', driver_offer: offer });
  let w = V.driverWords(egpu, idle);
  assert.equal(w.kind, 'offer');
  assert.equal(w.external, true);
  assert.ok(w.sub.includes('The worker on every Intel Arc card stops for the install and comes back after it; the other cards keep mining.'), w.sub);
  assert.ok(w.sub.includes('a card in an external enclosure can take the machine for a minute and may need a restart'), w.sub);
  // the same card read as inside the case (the second Windows machine's Arc on an earlier release read discrete in its enclosure): the stop sentence stays,
  // the enclosure warning goes
  const inside = card({ ...egpu, kind: 'discrete' });
  w = V.driverWords(inside, idle);
  assert.equal(w.external, false);
  assert.ok(w.sub.includes('The worker on every Intel Arc card stops for the install'), w.sub);
  assert.ok(w.sub.endsWith('The display resets during the install: save your work first.'), w.sub);
  assert.ok(!w.sub.includes('Mining continues.'));
  // while it installs, the running line says the vendor's workers are stopped; the eGPU row adds its enclosure
  w = V.driverWords(egpu, { drivers: { status: 'installing', vendor: 'intel', version: '32.0.101.9034', progress: 0.7, message: 'installing INTEL 32.0.101.9034: Windows asks for permission once', dry_run: false } });
  assert.equal(w.kind, 'running');
  assert.ok(w.sub.includes('The Intel Arc workers are stopped until the installer ends; the display may reset, and this card\u2019s enclosure can take the machine'), w.sub);
  w = V.driverWords(inside, { drivers: { status: 'installing', vendor: 'intel', version: '32.0.101.9034', progress: 0.7, message: 'x', dry_run: false } });
  assert.ok(w.sub.endsWith('The Intel Arc workers are stopped until the installer ends; the display may reset.'), w.sub);
});

test('a card behind a USB4 or Thunderbolt router is an eGPU on the Cards page (the second Windows machine, 7 October 2026)', () => {
  const r = V.cardRow(card({ key: 'nvidia:1:NVIDIA GeForce RTX 5060 Ti', name: 'NVIDIA GeForce RTX 5060 Ti', vendor: 'nvidia', kind: 'external', enabled: true, state: 'mining', hash_now: 40, power_w: 120, temp_gpu: 55 }));
  assert.equal(r.kindWord, 'eGPU');
  assert.equal(r.integrated, false);
  assert.equal(r.canToggle, true);
});

test('an integrated GPU is shown as integrated and off, with its reason', () => {
  const r = V.cardRow(card({ key: 'intel:1:UHD', name: 'Intel UHD Graphics 770', vendor: 'other', kind: 'integrated', enabled: false, state: 'off', hash_now: 0, reason: 'integrated GPU: slow and shares the machine memory', power_w: 0, temp_gpu: 0 }));
  assert.equal(r.integrated, true);
  assert.equal(r.kindWord, 'Integrated');
  assert.equal(r.on, false);
  assert.equal(r.word, 'off');
  assert.equal(r.tone, 'off');
  assert.equal(r.hash, '');
  assert.equal(r.sub, 'integrated GPU: slow and shares the machine memory');
  assert.equal(V.cardRow(card({ kind: 'unknown' })).canToggle, false);
  assert.equal(V.cardRow(card({ state: 'restarting', restart_in_s: 7 })).word, 'restart in 7 s');
  assert.equal(V.cardRow(card({ state: 'stopped', message: 'stopped: it reports hashing but produces nothing the reference accepts (twice in an hour); switch the card off and on to try again after checking the card and the worker' })).word, 'stopped: it reports hashing but produces nothing the reference accepts (twice in an hour); switch the card off and on to try again after checking the card and the worker');
  assert.equal(V.cardRow(card({ state: 'stopped', message: '' })).tone, 'bad');
  // no fault is permanent (7 October 2026, docs/plans/miner-faults.md): a restarting card is the bad tone, there is no faulted state
  assert.equal(V.cardRow(card({ state: 'restarting', restart_in_s: 120, message: 'hash rate 0 for 60 s while the node is synced; trying again' })).tone, 'bad');
  assert.equal(V.cardRow(card({ state: 'waiting' })).word, 'waiting for work');
});

test('the big button: start when paused, stop when mining, disabled with no card on', () => {
  const m = (over) => ({ state: 'mining', paused: false, cards: [card()], ...over });
  assert.deepEqual(V.toggle(m(), { synced: true }, {}), { label: 'Stop mining', sub: 'mining on 1 of 1 card', cls: 'stop', disabled: false, act: 'pause' });
  assert.deepEqual(V.toggle(m({ state: 'paused', paused: true }), { synced: true }, {}), { label: 'Start mining', sub: 'paused · the pool session stays open', cls: '', disabled: false, act: 'resume' });
  const none = V.toggle(m({ cards: [card({ enabled: false })] }), { synced: true }, {});
  assert.equal(none.disabled, true);
  assert.equal(none.act, '');
  assert.equal(none.sub, 'no card switched on');
  assert.equal(V.toggle(m({ cards: [] }), { synced: true }, {}).sub, 'no card this app can use');
  assert.equal(V.toggle(m({ state: 'waiting' }), { synced: false }, {}).sub, 'waiting for work from the pool');
  assert.equal(V.toggle(m(), { synced: true }, { quitting: true }).disabled, true);
  // F-E50 (14 October 2026): an idle engine never reads "Stop mining"; the button starts and its line is the engine's
  // reason (the newest bad notice, its detail first) when it has one
  const pin = { level: 'bad', text: 'The pinned coordinate is not served.', detail: 'coordinate 5 is pinned but this source serves no coordinates; nothing mines until the pin is cleared or the source serves it' };
  const idle = V.toggle(m({ state: 'idle', cards: [card({ state: 'waiting' })], notices: [pin] }), { synced: true }, {});
  assert.equal(idle.label, 'Start mining');
  assert.equal(idle.act, 'start');
  assert.equal(idle.sub, pin.detail);
  assert.equal(V.toggle(m({ state: 'idle', cards: [card({ state: 'waiting' })], notices: [] }), { synced: true }, {}).sub, 'not mining');
  assert.equal(V.toggle(m({ state: 'idle', cards: [card({ state: 'waiting' })] }), { synced: true }, {}).label, 'Start mining');
});

// F-E63 (the founder, 15 October 2026): the button reflects the engine's state and nothing else, with 'Starting' and a
// spinner between. The click-and-read: press the button on a paused engine, read it at once (Starting, the spinner,
// no act), read it again while the engine still says paused (the mini's log: the resume acknowledged 11 s after the
// click), then with the engine mining (Stop mining, the click settled), then the other way round; a click the engine
// never takes stops standing after PENDING_MS; an engine that is on with no card mining yet reads Starting too.
test('the big button follows the engine: Starting with a spinner between the click and the engine, Stop when mining', () => {
  const m = (over) => ({ state: 'mining', paused: false, cards: [card()], ...over });
  const paused = m({ state: 'paused', paused: true, cards: [card({ state: 'ready' })] });
  const n = { synced: true };
  // the click on a paused engine: the page presses what the button offered
  const before = V.toggle(paused, n, {});
  assert.equal(before.label, 'Start mining'); assert.equal(before.act, 'resume'); assert.equal(before.spin, undefined);
  const pend = V.pressToggle(before, 1000);
  assert.deepEqual(pend, { act: 'resume', at: 1000 });
  // read at once, the engine not yet acknowledging: Starting, the spinner, nothing to click
  let t = V.toggle(paused, n, {}, { ...pend, now: 1050 });
  assert.equal(t.label, 'Starting'); assert.equal(t.spin, true); assert.equal(t.act, ''); assert.equal(t.disabled, true);
  assert.equal(V.clickSettled(pend, paused, 1050), false);
  // eleven seconds on, the engine still paused (the tick): the word stands
  t = V.toggle(paused, n, {}, { ...pend, now: 12000 });
  assert.equal(t.label, 'Starting'); assert.equal(t.spin, true);
  // the engine resumed, a card configuring and none mining yet: Starting from the engine's own state, the click settled
  const starting = m({ state: 'waiting', paused: false, cards: [card({ state: 'ready' })] });
  assert.equal(V.clickSettled(pend, starting, 12500), true);
  t = V.toggle(starting, n, {});
  assert.equal(t.label, 'Starting'); assert.equal(t.spin, true); assert.equal(t.act, 'pause'); assert.equal(t.disabled, false);
  assert.equal(t.sub, 'waiting for work from the pool');
  assert.equal(V.toggle(m({ cards: [card({ state: 'preparing' })] }), n, {}).sub, 'preparing NVIDIA GeForce RTX 5090');
  // the card mines: Stop mining, no spinner
  t = V.toggle(m(), n, {});
  assert.equal(t.label, 'Stop mining'); assert.equal(t.spin, undefined); assert.equal(t.act, 'pause');
  // the other way: a click to stop on a mining engine reads Stopping until the engine is paused
  const p2 = V.pressToggle(t, 20000);
  assert.deepEqual(p2, { act: 'pause', at: 20000 });
  t = V.toggle(m(), n, {}, { ...p2, now: 20100 });
  assert.equal(t.label, 'Stopping'); assert.equal(t.spin, true); assert.equal(t.act, ''); assert.equal(t.disabled, true);
  assert.equal(V.clickSettled(p2, m(), 20100), false);
  assert.equal(V.clickSettled(p2, paused, 21000), true);
  assert.equal(V.toggle(paused, n, {}).label, 'Start mining');
  // a click the engine never takes: the word stands no longer than PENDING_MS, then the engine's state alone
  assert.equal(V.clickSettled(pend, paused, 1000 + V.PENDING_MS), true);
  assert.equal(V.toggle(paused, n, {}, { ...pend, now: 1000 + V.PENDING_MS }).label, 'Start mining');
  // an idle or stopped engine never reads Stopping after a pause click (nothing to stop): the engine's own word
  assert.equal(V.toggle(m({ state: 'idle', cards: [card({ state: 'waiting' })] }), n, {}, { act: 'pause', at: 1, now: 2 }).label, 'Start mining');
  // no click, no spinner on a paused or mining engine
  assert.equal(V.toggle(paused, n, {}, null).spin, undefined);
  assert.equal(V.pressToggle(V.toggle(m({ cards: [card({ enabled: false })] }), n, {}), 5), null);
});

test('the node words: synced, syncing with a percentage, failed, a blocked clock', () => {
  const n = (over) => ({ state: 'synced', blocks: 135200, headers: 135200, peers: 3, daa: 140000, last_reading_age_s: 4, message: '', restart_in_s: 0, ...over });
  const ok = V.nodeWords(n(), { severity: 'none' }, '');
  assert.equal(ok.word, 'synced');
  assert.equal(ok.tone, 'ok');
  assert.match(ok.line, /every block/);
  const sy = V.nodeWords(n({ state: 'syncing', blocks: 50000, headers: 100000 }), { severity: 'none' }, 'about 3 min left at 300 blocks/s');
  assert.equal(sy.word, 'syncing');
  assert.equal(sy.line, 'about 3 min left at 300 blocks/s · 50,000 of 100,000 (50%)');
  assert.equal(V.nodeWords(n({ state: 'failed', message: 'igneumd could not start' }), { severity: 'none' }, '').tone, 'bad');
  // a build with no node inside (0.2.1): the engine's message is the line; no claim of a node holding every block
  const ro = V.nodeWords(n({ message: 'no node runs inside this build; the chain is read through http://127.0.0.1:26610' }), { severity: 'none' }, '');
  assert.equal(ro.word, 'read-only'); assert.equal(/every block/.test(ro.line), false); assert.match(ro.line, /^no node runs inside/);
  assert.equal(V.nodeLine(n({ message: 'no node runs inside this build; the chain is read through http://127.0.0.1:26610' }), { severity: 'none' }, '').text, 'Chain read through the source (no node inside this build)');
  const blocked = V.nodeWords(n(), { severity: 'block', skew_s: -75 }, '');
  assert.equal(blocked.tone, 'bad');
  assert.match(blocked.line, /clock is off by 75 s/);
  assert.equal(V.peersLine(n({ peers: 0 })), 'none yet: looking for the seed node');
  assert.equal(V.peersLine(n({ peers: 1 })), 'one other node this one talks to');
  assert.equal(V.heightLine(n({ state: 'syncing', blocks: 10, headers: 500 })), 'of 500 headers seen');
  assert.equal(V.heightLine(n()), 'blocks this node holds');
});

test('the next consensus switch is the first height above the DAA score, in plain words', () => {
  const sw = [
    { key: 'fees_v1_activation_daa', name: 'Fees v1', daa: 210000 },
    { key: 'difficulty_v2_activation_daa', name: 'Difficulty v2', daa: 33000 },
    { key: 'finality_v3_activation_daa', name: 'Finality v3', daa: 135200 }
  ];
  const next = V.nextSwitch(sw, 140000);
  assert.equal(next.name, 'Fees v1');
  assert.equal(next.away, 70000);
  assert.equal(V.switchLine(next, 140000), 'Fees v1 at block 210,000: 70,000 blocks away, about 9 d at 12-second blocks.');
  assert.equal(V.nextSwitch(sw, 20000).name, 'Difficulty v2');
  assert.equal(V.nextSwitch(sw, 300000), null);
  assert.match(V.switchLine(null, 300000), /Every planned rule change is behind this node/);
  assert.match(V.switchLine(null, 0), /^A rule change is planned ahead/);
  assert.equal(V.nextSwitch([], 5), null);
});

test('the prove words follow the switch, the setup, the node and the status', () => {
  const pv = (over) => ({ enabled: true, available: true, setup_hint: '', backend: 'cuda', status: 'idle', message: '', current: '', verifier_mode: 'command', pool_entries: 4, pool_verified: 4, pool_failed: 0, ...over });
  assert.equal(V.proveWords(pv(), false, true).word, 'off');
  assert.equal(V.proveWords(pv({ status: 'setup', available: false, setup_hint: 'proving needs the WSL2 setup' }), true, true).word, 'needs setup');
  assert.equal(V.proveWords(pv(), true, false).word, 'waiting');
  assert.equal(V.proveWords(pv({ status: 'proving', current: 'block 59199 shard 0' }), true, true).sub, 'block 59199 shard 0');
  assert.equal(V.proveWords(pv({ status: 'submitted' }), true, true).tone, 'on');
  assert.equal(V.proveWords(pv(), true, true).word, 'idle');
  assert.equal(V.verifierWords(pv()).word, 'verifying · pool 4/4');
  assert.equal(V.verifierWords(pv()).tone, 'ok');
  assert.equal(V.verifierWords(pv({ verifier_mode: 'off', pool_entries: 0 })).tone, 'warn');
  assert.equal(V.verifierWords(pv({ verifier_mode: 'off', verifier_reason: 'the WSL2 prover is not installed' })).needsSetup, true);
  assert.equal(V.verifierWords({}).word, 'not read yet');
});

test('the dev-fee lines name the share and the switch that turns it off', () => {
  const s = (on, fee) => ({ settings: { dev_fee: on }, mining: { fee_total: fee }, dev_fee: { on, percent: on ? 1 : 0, address: '0x1234567890abcdef1234567890abcdef12345678', line: '' } });
  assert.equal(V.devFeeLine(s(true, 12)), 'The Ember software takes a 1% dev fee. One block template in 100 is requested with the dev payout address instead of yours (12 so far). The fee goes to Igneum Labs LLC, the company that ships the software.');
  assert.equal(V.devFeeLine(s(false, 0)), 'The dev fee is off on this install (--dev-fee 0). Every block pays your address.');
  assert.equal(V.devFeeText(s(true, 0)), 'Dev fee 1% (1 block in 100) to 0x123456…5678');
  assert.match(V.devFeeText(s(false, 0)), /^Dev fee off/);
});

test('the Earnings dev-fee row shows the rate, the counts and the net amount, or why it is off', () => {
  // ED-05 (the founder, 11 October 2026): the fee's own line on Earnings with the net amount
  const on = { dev_fee: { on: true, percent: 1, address: '0x1234567890abcdef1234567890abcdef12345678', line: 'dev fee 1 percent', templates: 3, shares_accepted: 41 }, mining: { share_lane: true, credit_wei: '0x3635c9adc5dea00000' } };
  const r = V.devFeeEarningsRow(on);
  assert.equal(r.text, '1%');
  assert.match(r.sub, /one template in 100 to 0x123456…5678; this session 3 fee templates, 41 shares accepted on them; net to you 99 of every 100 templates/);
  // main's read of the 0.2.19 sheets (16 October 2026): the row's lines are two plain sentences and the session's counts, never bps or a settle window
  assert.equal(r.lines[0], 'Dev fee 1 percent: one block template in 100 goes to the project’s address, 99 are yours.'); assert.match(r.lines[r.lines.length - 1], /^This session: 3 templates to the project, 41 shares accepted on them/);
  const pooled = V.devFeeEarningsRow({ dev_fee: { on: true, percent: 1, address: '0x1234567890abcdef1234567890abcdef12345678', templates: 1, shares_accepted: 4, receipts_total: 239, fee_words: 'dev fee 1 percent; pool fee 100 bps (waived: once the member’s fee shares are read on the chain)' }, mining: {} });
  assert.equal(pooled.lines[1], 'The pool’s 1 percent is waived once your fee shares are read on the chain.');
  assert.equal(pooled.lines[2], 'This session: 1 template to the project, 4 shares accepted on it, 239 receipts.');
  assert.ok(pooled.lines.every(l => !/bps|settle window|fee template/.test(l)), 'no jargon in the fee lines');
  const off = V.devFeeEarningsRow({ dev_fee: { on: false, percent: 0, line: 'dev fee off (--dev-fee 0)' }, mining: {} });
  assert.equal(off.text, 'none');
  assert.equal(off.sub, 'Dev fee off (--dev-fee 0)');
  const unsigned = V.devFeeEarningsRow({ dev_fee: { on: false, percent: 0, line: 'dev fee off: no signed release manifest names the dev payout address' }, mining: {} });
  assert.match(unsigned.sub, /off: no signed release manifest/);
  const noCredit = V.devFeeEarningsRow({ dev_fee: { on: true, percent: 1, address: '', templates: 0, shares_accepted: 0 }, mining: {} });
  assert.match(noCredit.sub, /0 fee templates, 0 shares accepted on them; net to you 99 of every 100 templates/);
});

test('the remote-jobs line and the helpers', () => {
  const title = (j) => j.title || j.kind;
  assert.equal(V.jobsNote({ allowed: false }, 1000, title), 'Off. Nothing runs here until the switch above allows jobs from the team.');
  assert.equal(V.jobsNote({ allowed: true, url_set: false }, 1000, title), 'This build has no jobs address.');
  assert.equal(V.jobsNote({ allowed: true, url_set: true, active: true, id: 'job-3', kind: 'build', title: 'build' }, 1000, title), 'Running: build.');
  assert.equal(V.jobsNote({ allowed: true, url_set: true, checked_at: 940, queued: 2 }, 1000, title), 'Nothing running; checked 1 min ago; 2 queued.');
  assert.equal(V.shortHex('0x2b1a81cb413236cf063077b46ed3111628f6c41036bcf6e23ee4cbbf5679ef7a'), '0x2b1a81…79ef7a');
  assert.equal(V.shortHex('0xabc'), '0xabc');
  assert.equal(V.withCommas(1234567), '1,234,567');
  assert.equal(V.compact(2500000), '2.50M');
  assert.equal(V.rel(90), '1 min ago');
});

test('hot-plug (src/hotplug.rs): a removed card and a faulty card are shown as such, with no switch, and leave the counts', () => {
  const gone = card({ key: 'amd:gfx1201', name: 'gfx1201', vendor: 'amd', state: 'removed', removed_at: 1000, added_at: 0, gone: false, hash_now: 0 });
  const r = V.cardRow(gone);
  assert.equal(r.removed, true);
  assert.equal(r.on, false);
  assert.equal(r.canToggle, false);
  assert.equal(r.word, 'removed');
  assert.equal(r.tone, 'off');
  assert.equal(r.hash, '');
  assert.match(r.sub, /^unplugged\. Its row goes/);
  const bad = card({ key: 'amd:gfx1201#2', name: 'AMD Radeon RX 9070 XT', code: 'gfx1201', vendor: 'amd', enabled: false, state: 'unusable', problem: 'Code 43', message: 'not usable (Code 43)', reason: 'reboot with the card attached; if it persists, reinstall the driver with the card attached', added_at: 900, removed_at: 0, device: '1', platform: 'AMD Accelerated Parallel Processing', bus: '0000:03:00.0' });
  const b = V.cardRow(bad);
  assert.equal(b.unusable, true);
  assert.equal(b.canToggle, false);
  assert.equal(b.word, 'not usable (Code 43)');
  assert.equal(b.tone, 'bad');
  assert.match(b.sub, /^reboot with the card attached/);
  assert.equal(b.title, 'gfx1201 · CUDA device 1 · AMD Accelerated Parallel Processing · bus 0000:03:00.0');
  assert.equal(V.cardRow(card()).title, 'CUDA device undefined'.replace(' device undefined', '') === '' ? '' : V.cardRow(card()).title);
  // a row that is gone (five minutes after removal) is not shown at all; the big button counts only present cards
  assert.deepEqual(V.shownCards([card(), card({ key: 'x', gone: true })]).map((c) => c.key), ['nvidia:0:RTX 5090']);
  assert.equal(V.present(card()), true);
  // performance order (6 October 2026): the first Windows machine detects 5090, integrated AMD, 9070 XT; the list shows the 5090, the 9070 XT,
  // then the integrated card, whatever the detection order and whatever the integrated card's rate
  const pc1 = [
    card({ key: 'nvidia:0:RTX 5090', hash_avg: 122 }),
    card({ key: 'amd:gfx1036', name: 'AMD Radeon(TM) Graphics', vendor: 'amd', kind: 'integrated', vram_mb: 512, hash_avg: 2.1, enabled: true }),
    card({ key: 'amd:gfx1201', name: 'AMD Radeon RX 9070 XT', vendor: 'amd', kind: 'discrete', vram_mb: 16384, hash_avg: 18.2 }),
  ];
  assert.deepEqual(V.shownCards(pc1).map((c) => c.key), ['nvidia:0:RTX 5090', 'amd:gfx1201', 'amd:gfx1036']);
  // before any rate (first start): discrete by memory, integrated last; a removed card last of all; ties keep detection order
  const fresh = [
    card({ key: 'amd:gfx1036', kind: 'integrated', vram_mb: 512, hash_avg: 0, hash_now: 0, state: 'off' }),
    card({ key: 'amd:gfx1201', kind: 'discrete', vram_mb: 16384, hash_avg: 0, hash_now: 0, state: 'waiting' }),
    card({ key: 'nvidia:0:RTX 5090', hash_avg: 0, hash_now: 0, state: 'waiting' }),
    card({ key: 'nvidia:1:RTX 5090', hash_avg: 0, hash_now: 0, state: 'waiting', removed_at: 5 }),
  ];
  assert.deepEqual(V.shownCards(fresh).map((c) => c.key), ['nvidia:0:RTX 5090', 'amd:gfx1201', 'amd:gfx1036', 'nvidia:1:RTX 5090']);
  // a small rate change does not reorder (5 MH/s buckets): 122 and 124 sort as equal and keep detection order
  assert.deepEqual(V.shownCards([card({ key: 'a', hash_avg: 122 }), card({ key: 'b', hash_avg: 124 })]).map((c) => c.key), ['a', 'b']);
  assert.equal(V.present(gone), false);
  assert.equal(V.present(bad), false);
  const t = V.toggle({ state: 'mining', paused: false, cards: [gone, bad] }, { synced: true }, {});
  assert.equal(t.disabled, true);
  assert.equal(t.sub, 'no card this app can use');
  const t2 = V.toggle({ state: 'mining', paused: false, cards: [card(), gone] }, { synced: true }, {});
  assert.equal(t2.sub, 'mining on 1 of 1 card');
});

// ---- miner-ui-3: the row as the hero object, the money, the tune line, the prove tier, the node line, the asks ----
const NOW = 1_800_000_000;
const settings = (over) => ({ settings: { sweep: true, tuning_off: false, tune_period_s: 604800, power_control: true, ...over }, now: NOW });

test('the row word: every state carries a reason, and a paused miner says paused on a card that is on', () => {
  const m = { paused: true, state: 'paused' };
  assert.equal(V.rowWord(V.cardRow(card({ state: 'off', hash_now: 0 })), m), 'paused');
  assert.equal(V.rowWord(V.cardRow(card({ state: 'waiting', hash_now: 0 })), m), 'paused');
  assert.equal(V.rowWord(V.cardRow(card()), { paused: false, state: 'mining' }), 'mining');
  assert.equal(V.rowWord(V.cardRow(card({ enabled: false, state: 'off' })), m), 'off');
  assert.equal(V.cardRow(card({ state: 'held', hash_now: 0, message: 'held for a remote job: shard benchmark' })).word, 'held for a remote job: shard benchmark');
  assert.equal(V.cardRow(card({ state: 'tuning', tune_step: 3, tune_steps: 9, power_w: 300 })).word, 'tuning: step 3 of 9 · measuring 300 W');
  assert.equal(V.cardRow(card({ state: 'starting', hash_now: 0 })).word, 'starting');
  assert.equal(V.cardRow(card({ state: 'failed', hash_now: 0 })).tone, 'bad');
});

test('money: £ a day from watts at a price, nothing without one, the cells leave out what is not reported', () => {
  assert.equal(V.poundsPerDay(310, 28.5).toFixed(4), '2.1204');
  assert.equal(V.money(V.poundsPerDay(310, 28.5)), '£2.12');
  assert.equal(V.poundsPerDay(310, 0), null);
  assert.equal(V.poundsPerDay(0, 28), null);
  assert.equal(V.money(123.4), '£123');
  assert.equal(V.money(null), '');
  const c = V.cells(card(), 28);
  assert.equal(c.hash, '124'); assert.equal(c.power, '410 W'); assert.equal(c.money, '£2.76'); assert.equal(c.temp, '61 °C'); assert.equal(c.needsPrice, false); assert.equal(c.noReading, '');
  assert.equal(c.eff, '0.30 MH/W');
  const noPrice = V.cells(card(), 0);
  assert.equal(noPrice.money, ''); assert.equal(noPrice.needsPrice, true);
  const apple = V.cells(card({ vendor: 'apple', kind: 'apple', power_w: 0, temp_gpu: 0, temp_mem: 0, eff_mhw: 0 }), 28);
  assert.equal(apple.power, ''); assert.equal(apple.money, ''); assert.equal(apple.temp, ''); assert.equal(apple.needsPrice, false);
  assert.equal(apple.noReading, 'No power or temperature reading on Apple silicon');
  assert.equal(V.cells(card({ kind: 'integrated', vendor: 'other', power_w: 0, temp_gpu: 0, eff_mhw: 0 }), 28).noReading, 'No power or temperature reading on an integrated GPU');
  assert.equal(V.wattsTotal([card(), card({ key: 'b', power_w: 100 }), card({ key: 'c', power_w: 50, removed_at: 5 })]), 510.2);
  assert.equal(V.wattsTotal([card(), card({ key: 'd', power_w: 48, state: 'off', enabled: false })]), 410.2, 'an idle card’s standby draw is not counted');
  assert.equal(V.effText(V.effOf(card({ eff_mhw: 0.422 }))), '0.42 MH/W');
  assert.equal(V.effText(V.effOf(card({ eff_mhw: 0, power_w: 0 }))), '');
});

test('the tune line: never run, running with a step and a bar, tuned with the point and the next check, the prior, measured only, pinned, stopped, fleet pause, no control', () => {
  const nv = (over) => card({ sweep_state: 'idle', sweep_note: '', sweep_pct: 0, sweep_at: 0, tune_line: '', tune_source: '', tune_clock_mhz: 0, tune_control: true, pinned: false, tune_step: 0, tune_steps: 0, tune_eta_s: 0, ...over });
  const idle = V.tuneWords(nv(), settings(), NOW);
  assert.equal(idle.kind, 'idle'); assert.equal(idle.text, 'Not tuned yet: starts after 2 min of steady mining'); assert.equal(idle.button, 'tune'); assert.equal(idle.progress, -1);
  assert.equal(V.tuneWords(nv(), settings({ sweep: false }), NOW).text, 'Not tuned yet: Ember Tune is off in Settings');
  const run = V.tuneWords(nv({ state: 'tuning', tune_step: 7, tune_steps: 9, tune_eta_s: 230 }), settings(), NOW);
  assert.equal(run.kind, 'running'); assert.equal(run.text, 'Tuning: step 7 of 9 · about 4 min left'); assert.equal(run.button, 'stop'); assert.equal(run.progress.toFixed(2), '0.67');
  const runNoStep = V.tuneWords(nv({ sweep_state: 'running', sweep_note: 'tuning: waits for 120 s of steady mining' }), settings(), NOW);
  assert.equal(runNoStep.text, 'Tuning: waits for 120 s of steady mining');
  const tuned = V.tuneWords(nv({ tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'full', tune_clock_mhz: 2470, sweep_pct: 100, sweep_at: NOW - 7200 }), settings(), NOW);
  assert.equal(tuned.kind, 'tuned'); assert.equal(tuned.note, '122.3 MH/s at 290 W (0.422 MH/W)'); assert.equal(tuned.button, 'tune');
  assert.match(tuned.text, /^Tuned 2 h ago · 2470 MHz at 100% · next check /);
  const prior = V.tuneWords(nv({ tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'confirm', tune_clock_mhz: 2470, sweep_pct: 100, sweep_at: NOW - 120 }), settings(), NOW);
  assert.match(prior.text, /^Tuned 2 min ago from what other miners found, confirmed · 2470 MHz at 100%/);
  const measured = V.tuneWords(nv({ tune_line: 'Tuned: 26.7 MH/s at 38 W (0.703 MH/W)', tune_source: 'baseline', tune_control: false, sweep_at: NOW - 600, sweep_note: 'measure only on Apple silicon: the system sets the clocks and the power; no control exposed' }), settings(), NOW);
  assert.equal(measured.kind, 'measured'); assert.equal(measured.text, 'Measured 26.7 MH/s at 38 W (0.703 MH/W) as it runs, 10 min ago');
  assert.equal(measured.note, 'Measured only on Apple silicon: the system sets the clocks and the power; no control exposed');
  const off = V.tuneWords(nv({ tune_control: false, sweep_note: 'measure only until Power control is on in Settings (Windows asks for administrator rights once)' }), settings({ power_control: false }), NOW);
  assert.equal(off.kind, 'idle'); assert.equal(off.note, 'Measured only until Power control is on in Settings (Windows asks for administrator rights once)');
  const pinned = V.tuneWords(nv({ tune_line: 'Tuned: 120 MH/s at 300 W (0.400 MH/W)', tune_source: 'full', pinned: true, power_pct: 80, sweep_at: NOW - 60 }), settings(), NOW);
  assert.match(pinned.text, /^Your cap stays pinned at 80% · Tuned 1 min ago/);
  const stopped = V.tuneWords(nv({ sweep_note: 'tuning stopped: a remote job took the GPU' }), settings(), NOW);
  assert.equal(stopped.kind, 'stopped'); assert.equal(stopped.text, 'Tuning stopped: a remote job took the GPU');
  const paused = V.tuneWords(nv(), settings({ tuning_off: true, tuning_note: 'tuning paused fleet-wide by the signed manifest' }), NOW);
  assert.equal(paused.kind, 'paused'); assert.equal(paused.button, '');
  assert.equal(V.tuneWords(card({ vendor: 'other', kind: 'integrated', sweep_supported: false }), settings(), NOW).kind, 'none');
  assert.equal(V.tunable(card({ vendor: 'apple', kind: 'apple' })), true);
  assert.equal(V.tunable(card({ removed_at: 5 })), false);
});

test('the next check is a weekday inside a week, a date beyond it, due now when overdue', () => {
  assert.equal(V.nextCheck(0, 604800, NOW), '');
  assert.equal(V.nextCheck(NOW - 700000, 604800, NOW), 'next check due now');
  assert.equal(V.nextCheck(NOW - 604800 + 3600, 604800, NOW), 'next check within a day');
  const inThree = V.nextCheck(NOW - 604800 + 3 * 86400, 604800, NOW);
  assert.match(inThree, /^next check (Sunday|Monday|Tuesday|Wednesday|Thursday|Friday|Saturday)$/);
  assert.match(V.nextCheck(NOW, 604800 * 3, NOW), /^next check \d{1,2} [A-Z][a-z]{2}$/);
  assert.equal(V.nextCheck(NOW - 100, 0, NOW), V.nextCheck(NOW - 100, 604800, NOW), 'no period means seven days');
});

test('the Tuning card schedule and the goal consequence', () => {
  const nv = (over) => card({ sweep_at: 0, tune_steps: 0, ...over });
  assert.equal(V.schedule([nv()], settings(), NOW), 'Not tuned yet: the first tune starts 2 min into steady mining.');
  assert.equal(V.schedule([nv()], settings({ sweep: false }), NOW), 'Ember Tune is off. Tune and Tune all still work when you press them.');
  assert.equal(V.schedule([nv({ state: 'tuning', tune_step: 2, tune_steps: 9, tune_eta_s: 500 })], settings(), NOW), 'Tuning now: NVIDIA GeForce RTX 5090, step 2 of 9, mining again in about 8 min.');
  assert.match(V.schedule([nv({ sweep_at: NOW - 7200 }), nv({ key: 'b', name: 'B', sweep_at: NOW - 60 })], settings(), NOW), /^Last tune 1 min ago \(B\) · next check /);
  assert.equal(V.schedule([], settings(), NOW), 'No card here can be tuned.');
  assert.equal(V.schedule([nv()], settings({ tuning_off: true, tuning_note: 'tuning paused fleet-wide by the signed manifest' }), NOW), 'tuning paused fleet-wide by the signed manifest');
  const g = V.goalWords('efficiency', [card({ sweep_watts: 290, power_w: 410 })], 28);
  assert.equal(g.watts, 290); assert.equal(g.consequence, 'about £1.95 a day at 290 W');
  assert.equal(V.goalWords('rate', [card({ power_default_w: 575 })], 28).watts, 575);
  assert.equal(V.goalWords('balanced', [card()], 28).watts, 410);
  assert.equal(V.goalWords('nonsense', [card()], 0).goal, 'balanced');
  assert.equal(V.goalWords('balanced', [card()], 0).consequence, 'about 410 W');
});

test('the prove tier sentence per card, and the counts in one line', () => {
  assert.equal(V.proveTier(card()), 'NVIDIA GeForce RTX 5090: proves while it mines (32 GB; a full shard needs 20.4 GB).');
  // Igneum 2.0 (8 October 2026, the rented-card rows): under 16 GB the card does one at a time and says why (the same sentence as src/provedefault.rs)
  assert.equal(V.proveTier(card({ name: 'NVIDIA GeForce RTX 3080', vram_mb: 10240 })), 'NVIDIA GeForce RTX 3080: mining and proving together need a 16 GB card; this card does one at a time; mining continues (10 GB).');
  assert.equal(V.proveTier(card({ name: 'NVIDIA GeForce RTX 3060', vram_mb: 12288 })), 'NVIDIA GeForce RTX 3060: mining and proving together need a 16 GB card; this card does one at a time; mining continues (12 GB).');
  assert.equal(V.proveTier(card({ name: 'Apple M5 Max', vendor: 'apple' })), 'Apple M5 Max: proves on the CPU, slowly.');
  assert.equal(V.proveTier(card({ name: 'AMD Radeon RX 9070 XT', vendor: 'amd', vram_mb: 16384 })), 'AMD Radeon RX 9070 XT: cannot prove yet (proving needs an NVIDIA card).');
  assert.equal(V.proveLine({ assigned: 0, submitted: 0, paid: 0, paid_wei: '0' }, false, true), 'Off · not proving · 0 assigned · 0 proven · 0 paid · 0.0000 IGN');
  assert.equal(V.proveLine({ status: 'idle', assigned: 3, submitted: 2, paid: 1, paid_wei: '1500000000000000000', available: true, enabled: true }, true, true), 'Idle · nothing assigned · 3 assigned · 2 proven · 1 paid · 1.5000 IGN');
});

test('the node line, the activity line, the asks', () => {
  const n = { state: 'synced', blocks: 109859, headers: 109859, peers: 6, daa: 198000, last_reading_age_s: 5, message: '' };
  const l = V.nodeLine(n, { severity: 'none' }, '');
  assert.equal(l.text, 'Node synced · 6 peers'); assert.equal(l.tone, 'ok'); assert.match(l.sub, /every block/);
  assert.equal(V.nodeLine({ ...n, peers: 1, blocks: 0, state: 'starting' }, { severity: 'none' }, '').text, 'Node starting');
  assert.equal(V.nodeLine(n, { severity: 'block', skew_s: -412 }, '').tone, 'bad');
  assert.equal(V.eventLine('consensus parameters from the signed manifest: {"difficulty_v2_activation_daa":33000}'), 'consensus parameters from the signed manifest (details in the log)');
  assert.equal(V.eventLine('node synced: 151512 blocks, 6 peer(s)'), 'node synced: 151512 blocks, 6 peer(s)');
  assert.equal(V.eventLine('a '.repeat(100), 40).length <= 41, true);
  assert.equal(V.addressAsk('0x1234567890abcdef1234567890abcdef12345678'), 'Pay 0x1234…5678 from the next share? Mining restarts.');
  assert.match(V.trustAsk(true), /^Include proofs the node never checked\? Test networks only\. The node restarts\.$/);
  assert.match(V.trustAsk(false), /^Check proofs again\?/);
  assert.equal(V.ago(30), 'just now'); assert.equal(V.ago(7200), '2 h ago'); assert.equal(V.ago(200000), '2 d ago');
});

test('the activity feed and the pill speak in the user\'s voice; the engine line is kept as the tooltip', () => {
  const pe = V.plainEvent('job update-now-0313-switch-d937c69d (Exec restart: re-read the manifest (exec_restart_number 27276) and restart the node with it) done after 0 min, exit 0, report uploaded: update check and install asked');
  assert.equal(pe.text, 'A job from the team ran: update check, 0 min');
  assert.match(pe.tech, /^job update-now/);
  assert.equal(V.plainEvent('updated to Ember 0.1.14 from 0.1.13').text, 'Updated to 0.1.14');
  assert.equal(V.plainEvent('consensus parameters from the signed manifest: {"difficulty_v2_activation_daa":33000}').text, 'Rule settings read from the team');
  assert.equal(V.plainEvent('node synced: 151512 blocks, 6 peer(s)').text, 'Node synced: 151,512 blocks, 6 peers');
  assert.equal(V.plainEvent('block accepted by the node (Apple M5 Max)').text, 'Block found by Apple M5 Max');
  assert.equal(V.plainEvent('1 remote job received from Igneum').text, '1 job from the team received');
  assert.equal(V.plainEvent('remote job: update check now; a newer version installs at once').text, 'The team asked for an update check');
  assert.equal(V.plainEvent('a node already answers on 127.0.0.1:26610; using it (it is not stopped by this app)').text, 'Using the node already running on this machine');
  assert.equal(V.plainEvent('igneumd exited with code 101 after 300 s; restarting').text, 'The node stopped (code 101). Restarting');
  assert.equal(V.plainEvent('miner apple:0 exited with code 1; restarting in 8 s').text, 'A card stopped (code 1). Restarting in 8 s');
  assert.equal(V.plainEvent('NVIDIA GeForce RTX 5090 kernel race: l128w16 at 122.3 MH/s (+4.1% over base, 6 variants, 40 s)').text, 'NVIDIA GeForce RTX 5090: fastest program chosen, 122.3 MH/s');
  assert.equal(V.plainEvent('block 59199 shard 0 paid 0.0125 IGN').text, 'Proof paid: 0.0125 IGN');
  assert.equal(V.plainEvent('rewards go to 0xdd442fcbb964a3afdc90d49b408e8dd296fa86e8').text, 'Rewards go to 0xdd44…86e8');
  assert.equal(V.plainEvent('mining resumed').text, 'Mining resumed');
  assert.equal(V.plainEvent('something new the engine says').text, 'Something new the engine says');
  assert.equal(V.plainEvent('something new the engine says').tech, '');
  const base = { setup_done: true, quitting: false, mining: { state: 'mining', hash_total: 510.6, cards: [] }, node: { state: 'synced' }, source: { kind: 'node', url: 'http://127.0.0.1:26610' } };
  assert.deepEqual(V.pill(base), { text: 'Mining', tone: 'on' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'mining', hash_total: 17.04, cards: [] } }), { text: 'Mining', tone: 'on' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'paused', hash_total: 0, cards: [] } }), { text: 'Paused', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'syncing' } }), { text: 'Syncing', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'restarting' } }), { text: 'Node unreachable', tone: 'bad' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'mining', hash_total: 1, cards: [{ state: 'tuning' }] } }), { text: 'Tuning', tone: 'on' });
  // F-E14's bound on the page (16 October 2026): a block gap is not a stall; a minute without a job on a connected source is
  const gap = (w) => ({ ...base, mining: { state: 'mining', hash_total: 0.000405, cards: [{ key: 'a', state: 'mining', enabled: true, hash_now: 0.000405, lane_word: 'the raster lane', kind: 'apple' }] }, source: { kind: 'pool', connected: true, waiting_for_work_s: w } });
  assert.equal(V.WAITING_FOR_WORK_S, 60);
  assert.deepEqual(V.pill(gap(28)), { text: 'Mining', tone: 'on' });
  assert.deepEqual(V.pill(gap(60)), { text: 'Waiting for work', tone: '' });
  assert.equal(V.stripTiles(gap(28), null, 0, null).hash.sub, 'on 1 of 1 cards\non the raster lane');
  assert.equal(V.stripTiles(gap(60), null, 0, null).hash.sub, 'last measured\nwaiting for work from the pool');
  assert.equal(V.stripTiles(gap(60), null, 0, null).hash.text, '405');
  assert.equal(V.stripTiles({ ...gap(90), mining: { state: 'mining', hash_total: 0, cards: [] } }, null, 0, null).hash.sub, 'waiting for work from the pool');
  assert.deepEqual(V.pill({ ...base, setup_done: false, detecting: true }), { text: 'Detecting cards', tone: '' });
  assert.deepEqual(V.pill({ ...base, quitting: true }), { text: 'Stopping', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'idle', hash_total: 0, cards: [] } }), { text: 'Not mining', tone: '' });
});

test('Ember on the row (miner-ui-4): the flame fill, before -> after, the saving, the sparkline points, the plain label', () => {
  const curve = [{ clock_mhz: 0, power_pct: 100, watts: 374, mhs: 122.5, eff: 0.328 }, { clock_mhz: 0, power_pct: 80, watts: 302, mhs: 122.3, eff: 0.405 }, { clock_mhz: 0, power_pct: 60, watts: 228, mhs: 104.9, eff: 0.46, mark: 'hot' }, { clock_mhz: 2470, power_pct: 80, watts: 290, mhs: 122.3, eff: 0.422 }];
  const tuned = card({ tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'full', tune_control: true, tune_clock_mhz: 2470, sweep_pct: 80, sweep_at: NOW - 7200, sweep_watts: 290, sweep_mhs: 122.3, tune_curve: curve, sweep_state: 'idle', sweep_note: '' });
  const e = V.ember(tuned, settings(), NOW);
  assert.equal(e.kind, 'tuned'); assert.equal(e.fill, 1);
  assert.equal(e.label, 'Tuned 2 h ago');
  assert.match(e.sub, /^2470 MHz at 80% · next check /);
  assert.equal(e.before, 374); assert.equal(e.after, 290); assert.equal(e.savingW, 84);
  assert.equal(e.savingText, 'saves 84 W, 0.16% of rate');
  assert.equal(e.points.length, 4); assert.equal(e.points.filter((p) => p.chosen).length, 1); assert.equal(e.points.filter((p) => p.marked).length, 1);
  // Ember's own fields win over the curve (tune_before_watts / tune_before_mhs, step 0 of the last full plan)
  const withFields = V.ember(card({ ...tuned, tune_before_watts: 380, tune_before_mhs: 122.4, tune_source: 'confirm' }), settings(), NOW);
  assert.equal(withFields.before, 380); assert.equal(withFields.savingW, 90); assert.match(withFields.label, /from what other miners found$/);
  // a confirm plan without the fields claims no saving (its curve's first row is the prior, not an untuned point)
  assert.equal(V.ember(card({ ...tuned, tune_source: 'confirm' }), settings(), NOW).savingText, '');
  assert.equal(V.ember(card({ ...tuned, tune_before_watts: 0 }), settings(), NOW).savingText, '', 'zero means never measured');
  const running = V.ember(card({ state: 'tuning', tune_step: 4, tune_steps: 9, tune_eta_s: 410, sweep_state: 'running' }), settings(), NOW);
  assert.equal(running.kind, 'running'); assert.equal(running.fill.toFixed(2), '0.33'); assert.equal(running.label, 'Tuning: step 4 of 9'); assert.equal(running.sub, 'about 7 min left');
  const measured = V.ember(card({ tune_line: 'Measured: 122.3 MH/s at 410 W (0.298 MH/W)', tune_source: 'baseline', tune_control: false, sweep_at: NOW - 600, sweep_note: 'measure only until Power control is on in Settings (Windows asks for administrator rights once)', sweep_state: 'idle' }), settings({ power_control: false }), NOW);
  assert.equal(measured.kind, 'measured'); assert.equal(measured.label, 'Measured, not tuned'); assert.equal(measured.sub, 'Power control is off'); assert.equal(measured.fill, 0.5);
  const idle = V.ember(card({ sweep_state: 'idle', sweep_note: '', tune_line: '', sweep_at: 0 }), settings(), NOW);
  assert.equal(idle.kind, 'idle'); assert.equal(idle.fill, 0); assert.equal(idle.label, 'Not tuned yet'); assert.equal(idle.sub, 'starts after 2 min of steady mining');
  const off = V.ember(card({ sweep_state: 'idle', sweep_note: '', tune_line: '', sweep_at: 0 }), settings({ sweep: false }), NOW);
  assert.equal(off.label, 'Not tuned: Ember Tune is off'); assert.equal(off.sub, 'Tune starts one by hand');
  assert.equal(V.ember(card({ vendor: 'other', kind: 'integrated', sweep_supported: false }), settings(), NOW).kind, 'none');
  assert.deepEqual(V.beforeOf({ tune_before_watts: 0, tune_curve: curve }), null);
});

test('the an earlier release engine fields (ember-tune fd03f35): the balance row, the fleet totals, the chain scene note', () => {
  assert.deepEqual(V.balanceWords({}, NOW), { text: '', sub: '', pounds: null }, 'an older engine has no balance field: the row stays hidden');
  const unread = V.balanceWords({ balance_wei: null, balance_age_s: -1, balance_note: '', price_gbp_per_ign: null }, NOW);
  assert.equal(unread.text, 'not read yet'); assert.equal(unread.pounds, null);
  const read = V.balanceWords({ balance_wei: '12345600000000000000', balance_age_s: 20, balance_note: '', price_gbp_per_ign: null }, NOW);
  assert.equal(read.text, '12.3456 IGN'); assert.equal(read.sub, 'in the wallet · read just now'); assert.equal(read.pounds, null);
  const priced = V.balanceWords({ balance_wei: '12345600000000000000', balance_age_s: 90, balance_note: '', price_gbp_per_ign: 0.5 }, NOW);
  assert.equal(priced.pounds.toFixed(2), '6.17'); assert.equal(priced.sub, 'in the wallet · read 1 min ago');
  assert.equal(V.balanceWords({ balance_wei: '5000000000000000000000', balance_age_s: 3, balance_note: 'the node answered 0x for the balance, not a quantity' }, NOW).sub, 'in the wallet · read just now · the node answered 0x for the balance, not a quantity');
  assert.equal(V.balanceWords({ balance_wei: '5000000000000000000000', balance_age_s: 3 }, NOW).text, '5,000 IGN');
  assert.equal(V.ign('1500000000000000000'), 1.5); assert.equal(V.ign('x'), null);
  const cards = [card(), card({ key: 'b', power_w: 100 })];
  assert.equal(V.fleetWatts({ watts_total: 0 }, cards), 510.2, 'no engine figure: the UI sums the cards');
  assert.equal(V.fleetWatts({ watts_total: 629 }, cards), 629, 'the engine figure wins');
  assert.equal(V.fleetPounds({ watts_total: 629, pounds_per_day: 4.23 }, cards, 28), 4.23);
  assert.equal(V.fleetPounds({ watts_total: 629, pounds_per_day: 0 }, cards, 28).toFixed(2), '4.23', 'no engine £: computed from the watts and the price');
  assert.equal(V.fleetPounds({}, cards, 0), null);
  assert.equal(V.liveNote({ ok: true, source: 'node', blocks: [] }), 'From your node. Your blocks are ringed.');
  assert.equal(V.liveNote({ ok: true, source: 'site', blocks: [] }), 'From Igneum’s observer. Your blocks are ringed.');
  assert.equal(V.liveNote({ ok: true, state: { source: 'site' } }), 'From Igneum’s observer. Your blocks are ringed.', 'the 0.1.15 engine carries the source under state');
  assert.equal(V.liveNote({ ok: false, pending: true, source: 'site' }), 'connecting');
  assert.equal(V.liveNote(null), '');
  const mineCards = [card({ ids: ['8fafda27', '1b2c3d4e'] })];
  assert.equal(V.isMine('8fafda27', mineCards), true, 'the site: the first 8 hex of the vote key hash');
  assert.equal(V.isMine('8fafda..e01f22', mineCards), true, 'the 0.1.16 engine: head6..tail6 of the same hash');
  assert.equal(V.isMine('8fafda27a9c1', mineCards), true, 'a longer prefix of the same hash');
  assert.equal(V.isMine('0x1B2C3D4E', mineCards), true);
  assert.equal(V.isMine('8fafdb..e01f22', mineCards), false, 'one hex off');
  assert.equal(V.isMine('deadbeef', mineCards), false);
  assert.equal(V.isMine('8faf', mineCards), false, 'under 6 hex never matches');
  assert.equal(V.isMine('', mineCards), false);
});

test('N4 (6 October 2026): a frozen tip reads behind, never synced; the tip age is on the node line', () => {
  const n = (over) => ({ state: 'synced', blocks: 133000, headers: 133000, peers: 1, daa: 140000, last_reading_age_s: 4, tip_age_s: 3, message: '', restart_in_s: 0, ...over });
  const ok = V.nodeLine(n(), { severity: 'none' }, '');
  assert.equal(ok.text, 'Node synced · 1 peer'); assert.equal(ok.tone, 'ok');
  assert.match(ok.sub, /Last block 3 s ago/);
  const behind = V.nodeLine(n({ state: 'behind', synced: false, tip_age_s: 3180, sync_cause: 'frozen' }), { severity: 'none' }, '');
  assert.equal(behind.text, 'Node behind · last block 53 min ago'); assert.equal(behind.tone, 'bad');
  assert.match(behind.sub, /^No new block for 53 min\. The chain may have moved on without this node\./);
  const none = V.nodeLine(n({ state: 'no peers', synced: false, peers: 0, tip_age_s: 500 }), { severity: 'none' }, '');
  assert.equal(none.text, 'Node: no peers, dialling the seeds'); assert.equal(none.tone, 'bad');
  assert.match(none.sub, /^No other node on our chain is connected/);
  // an older node without the age keeps the old words
  assert.equal(V.nodeLine(n({ tip_age_s: -1 }), { severity: 'none' }, '').text, 'Node synced · 1 peer');
  assert.equal(V.tipAge(12), '12 s'); assert.equal(V.tipAge(240), '4 min'); assert.equal(V.tipAge(3180), '53 min'); assert.equal(V.tipAge(7300), '2 h');
  const base = { setup_done: true, quitting: false, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'behind' }, source: { kind: 'node', url: 'http://127.0.0.1:26610' } };
  assert.deepEqual(V.pill(base), { text: 'Node unreachable', tone: 'bad' });
  assert.deepEqual(V.pill({ ...base, node: { state: 'no peers' } }), { text: 'Node unreachable', tone: 'bad' });
  assert.equal(V.toggle({ state: 'waiting', paused: false, cards: [card({ state: 'waiting' })] }, { synced: false, state: 'behind' }, { source: { kind: 'node', problem: 'the node is behind the chain' } }).sub, 'node unreachable');
});

test('not merging (an earlier release design note): behind with a moving tip, the miner held', () => {
  const w = V.nodeWords({ state: 'behind', sync_cause: 'not merging', tip_age_s: 3, message: 'behind: your blocks are not merging into the network', blocks: 1, headers: 1, peers: 2 }, { severity: 'none' }, '');
  assert.equal(w.word, 'behind'); assert.equal(w.tone, 'bad'); assert.match(w.line, /not merging into the network/); assert.doesNotMatch(w.line, /No new block/);
});

test('finality paused (ember-tune 1357d280): the words, no lock called final while paused', () => {
  const p = V.finalityWords({ paused: true, last_lock: 412, age_s: 1200, line: 'Finality paused since 18:39 UTC: under two thirds of the weight is signing' });
  assert.equal(p.paused, true); assert.equal(p.age, 'paused'); assert.equal(p.note, 'Finality paused since 18:39 UTC: under two thirds of the weight is signing');
  assert.equal(/\bfinal\b/i.test(p.note), false);
  assert.equal(V.finalityWords({ paused: true, last_lock: 0, age_s: 0 }).note, 'Finality paused: under two thirds of the weight is signing');
  const ok = V.finalityWords({ paused: false, last_lock: 412, age_s: 90 });
  assert.equal(ok.paused, false); assert.equal(ok.age, '1 min ago'); assert.match(ok.note, /never be undone/);
  const none = V.finalityWords({ paused: false, last_lock: 0, message: 'waiting for the miner' });
  assert.equal(none.age, 'n/a'); assert.equal(none.note, 'waiting for the miner');
  // N5: a node built without the mining engine is a fault
  assert.equal(V.nodeWords({ state: 'stub', blocks: 0, headers: 0, peers: 0 }, { severity: 'none' }, '').word, 'stub engine');
  assert.deepEqual(V.pill({ setup_done: true, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'stub' }, source: { kind: 'node' } }), { text: 'Node unreachable', tone: 'bad' });
});

test('whose node (publish 2): the app’s own, another node on this machine checked or unchecked, or none when the ports are taken by other rules', () => {
  assert.deepEqual(V.nodeSource({ source: 'app' }), { text: 'this app’s own node', sub: '' });
  assert.deepEqual(V.nodeSource({ source: 'external', rules_check: 'match' }), { text: 'another node on this machine', sub: 'same network and rules, checked' });
  assert.deepEqual(V.nodeSource({ source: 'external', rules_check: 'unknown' }), { text: 'another node on this machine', sub: 'its rules could not be checked (an older node)' });
  assert.deepEqual(V.nodeSource({ source: 'none', port_note: 'Node not started: port 26611 is taken by a node on another network (chain id 7778, ours 7777)' }), { text: 'no node', sub: 'Node not started: port 26611 is taken by a node on another network (chain id 7778, ours 7777)' });
  assert.deepEqual(V.nodeSource({}), { text: '', sub: '' });
  // 7 October 2026: the mode is re-decided; after the other node leaves for 60 s the app runs its own and says why
  assert.deepEqual(V.nodeSource({ source: 'app', mode: 'own', mode_reason: 'the node this app was reading left port 26611 for 60 s, so the app started its own node' }), { text: 'this app’s own node', sub: 'the node this app was reading left port 26611 for 60 s, so the app started its own node' });
  const gone = V.nodeWords({ state: 'stopped', source: 'external', message: 'the other node on port 26611 went away; this app starts its own node in 45 s unless it comes back', blocks: 0, headers: 0, peers: 0 }, { severity: 'none' }, '');
  assert.equal(gone.word, 'stopped'); assert.match(gone.line, /starts its own node in 45 s/);
  const taken = V.nodeWords({ state: 'stopped', source: 'none', message: 'Node not started: port 26611 is taken by a node on other rules (fees_v1_activation_daa 200000, ours 210000)', blocks: 0, headers: 0, peers: 0 }, { severity: 'none' }, '');
  assert.equal(taken.word, 'not started'); assert.equal(taken.tone, 'bad'); assert.match(taken.line, /^Node not started: port 26611 is taken/);
  assert.deepEqual(V.pill({ setup_done: true, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'stopped', source: 'none' }, source: { kind: 'node' } }), { text: 'Node unreachable', tone: 'bad' });
});

// ---- miner-ui-5 (7 October 2026): the miner's first month ----
// The chain facts as GET api/ladder shapes them (src/chainfacts.rs) for one key, 6ad0e117 (the example key of the founder's
// chain-scene reference), under the finality params the devnet node reports (api/live params, 7 Oct 2026: dust 5,
// weightWindow 7,200 s, presenceWindow 20, checkpointInterval 30; tools/fleet/devnet2-override.json sets none of them,
// so Devnet 2 runs the same values). Every number the rungs show is a field of getFinalityWeights,
// getFinalityCheckpoints, /api/stats or this app's own ladder.json, and the rung names it on hover.
const DN2 = { dust: 5, weight_window: 7200, presence_window: 20, checkpoint_interval: 30, min_daa: 7200 };
const MAINNET = { dust: 100, weight_window: 2592000, presence_window: 240, checkpoint_interval: 30, min_daa: 7200 };
const chainOf = (over, params) => ({ ok: true, source: 'node', rpc: { weights: 'getFinalityWeights', checkpoints: 'getFinalityCheckpoints', stats: '/api/stats', relay: '' }, params: params || DN2, checkpoint_index: 8495, latest_locked_index: 8495, next_index: 8496, finality_active: true, total_weight: 6000, active_weight: 5900, voters: 14, keys_listed: 4, keys_cap: 0, network: { hashrate_hps: 100e9, miner_ign_per_block: 80, ign_per_block: 100, ramp_factor: 1, daa: 1284117, blocks_per_day_measured: 86400, bps: 1, stale: false }, mine: [], best: null, ...over });
const key = (over) => ({ id: '6ad0e117', blocks: 0, voter: false, participation: 0, stripped_until_daa: 0, revealed: false, rank: 4, ...over });
const T5 = 1791362116;
const stateOf = (over) => ({ now: T5, uptime_s: 1800, version: '0.1.19', installed_at: T5 - 3000, setup_done: true, address: { display: '0xdd442fCbb964A3aFDc90D49B408e8DD296FA86E8' }, settings: { prove: false, dev_fee: true, power_price_pence: 28 }, node: { synced: true }, proving: { paid: 0, paid_wei: 0 }, mining: { state: 'mining', hash_total: 100, accepted_total: 0, accepted_session: 0, found: [], cards: [card({ key: 'nvidia:0:RTX 4070', name: 'NVIDIA GeForce RTX 4070', hash_now: 100, hash_avg: 100, power_w: 150, ids: ['6ad0e117'] })] }, ladder: { first_block_at: 0, first_block_card: '', first_block_hash: '', first_block_daa: 0, days_mined_30: 0, blocks_30d: 0, blocks_today: 0, days: [], cards_first: {}, blocks: [], votes_signed: 0, last_vote_index: 0, last_vote_at: 0, last_signed_locked: 0, signed_locked_count: 0, streak_s: 0, shards: [], milestone: null, first_synced_at: 0, first_mining_at: 0 }, ...over });
const RPC = /getFinalityWeights|getFinalityCheckpoints|igneum_getProofRecords|\/api\/stats|this app/;

test('the Poisson count-up: the gap from the card’s rate and the network’s, the chance from the time waited', () => {
  const a = V.poisson(100, 100e9, 0, 1);
  assert.equal(Math.round(a.gap_s), 1000);
  assert.equal(a.gapText, '17 minutes');
  assert.equal(a.chanceText, '0%');
  assert.equal(a.line, 'A card like yours finds a block about every 17 minutes on today’s network. Chance so far: 0%.');
  assert.equal(a.pool, '');
  const b = V.poisson(17, 100e9, 1800, 1);
  assert.equal(b.gapText, '1.6 hours');
  assert.equal(b.chanceText, '26%');
  const c = V.poisson(17, 1e12, 3600, 1);
  assert.equal(c.gapText, '16 hours');
  assert.match(c.pool, /^Above an 8-hour gap a pool pays by the share/);
  assert.equal(V.poisson(100, 100e9, 1000, 1).chanceText, '63%');
  assert.equal(V.poisson(17, 10e12, 0, 1).gapText, '6.8 days');
  assert.equal(V.poisson(100, 100e9, 10, 1, true).line.indexOf('Your cards find'), 0);
  assert.equal(V.poisson(0, 100e9, 10, 1), null);
  assert.equal(V.poisson(100, 0, 10, 1), null);
  // IGN a day: 86,400 blocks a day, the share of them, 80 producer points each (the litepaper's 6,912 example)
  assert.equal(Math.round(V.ignPerDay(100, 100e9, 80, 1)), 6912);
  assert.equal(V.ignPerDay(100, 0, 80, 1), null);
});

test('a Devnet 2 key walks every rung on screen, and every rung names its RPC field (the gate)', () => {
  // 0. mining, nothing found: the first rung is "now", the rest wait; the count-up line is on
  let s = stateOf(), c = chainOf();
  let r = V.ladderRungs(s, c, T5);
  assert.deepEqual(r.map((x) => x.id), ['first', 'vote', 'signed', 'window', 'rank', 'streak', 'shard']);
  assert.deepEqual(r.map((x) => x.state), ['now', 'next', 'next', 'next', 'next', 'next', 'off']);
  assert.equal(r[0].line, 'Waiting for your first block.');
  assert.equal(V.currentRung(r).id, 'first');
  r.forEach((x) => assert.match(x.field, RPC, x.id + ' names no RPC field: ' + x.field));
  assert.equal(V.blockCard(null, s, c, T5), null);
  // 1. the first block: the card raises, the key enters the table with one block under the dust line
  const m1 = { kind: 'first', count: 1, card: 'nvidia:0:RTX 4070', card_name: 'NVIDIA GeForce RTX 4070', hash: '9f3a' + 'ab'.repeat(28) + 'c21e', daa: 1284117, at: T5 - 60 };
  s = stateOf({ mining: { ...stateOf().mining, accepted_total: 1, found: [T5 - 60] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 60, first_block_card: 'nvidia:0:RTX 4070', first_block_hash: m1.hash, first_block_daa: 1284117, days_mined_30: 1, blocks_30d: 1, blocks_today: 1, milestone: m1 } });
  c = chainOf({ mine: [key({ blocks: 1, rank: 4 })], best: key({ blocks: 1, rank: 4 }) });
  r = V.ladderRungs(s, c, T5);
  assert.deepEqual(r.map((x) => x.state), ['done', 'now', 'next', 'now', 'done', 'next', 'off']);
  assert.equal(r[0].line, 'First block 1 min ago, NVIDIA GeForce RTX 4070.');
  assert.equal(r[0].sub, '1 block with your key in the window');
  assert.equal(r[1].line, '1 of 5 blocks to a vote.');
  assert.equal(r[1].progress, 0.2);
  assert.equal(r[3].line, '1 of 2 hours.');
  assert.equal(r[4].line, 'Rank 4 of 4 keys.');
  assert.equal(V.currentRung(r).id, 'vote');
  const bc = V.blockCard(m1, s, c, T5);
  assert.equal(bc.eyebrow, 'your first block');
  assert.equal(bc.title, 'Block 1,284,117 is yours.');
  assert.equal(bc.cardLine, 'NVIDIA GeForce RTX 4070 · 100 MH/s · 0.100% of the network');
  assert.equal(bc.ignLine, '80.00 IGN to 0xdd44…86E8 (72 producer points, 8 for signing)');
  assert.equal(bc.rankLine, 'rank 4 of 4 keys · hour 1 of 2');
  assert.equal(bc.url, 'https://igneum.network/block/' + m1.hash);
  assert.equal(bc.hashShort, '9f3aabab…abc21e');
  assert.match(bc.fields.ign, /block_reward\.miner_ign/);
  // 2. five blocks: the vote
  c = chainOf({ mine: [key({ blocks: 5, voter: true, revealed: true, rank: 4 })], best: key({ blocks: 5, voter: true, revealed: true, rank: 4 }) });
  r = V.ladderRungs(s, c, T5);
  assert.equal(r[1].state, 'done');
  assert.equal(r[1].line, 'Your key has a vote.');
  assert.equal(r[1].sub, '5 blocks in the window, the line is 5');
  assert.equal(r[2].state, 'now');
  assert.equal(r[2].line, 'Signing: the next locked checkpoint carries your vote.');
  assert.equal(r[5].state, 'now');
  // 3. the signature in a locked checkpoint, the streak
  s = stateOf({ ...s, ladder: { ...s.ladder, votes_signed: 31, last_vote_index: 8496, last_vote_at: T5 - 5, last_signed_locked: 8496, signed_locked_count: 30, streak_s: 900 } });
  c = chainOf({ latest_locked_index: 8496, mine: [key({ blocks: 5, voter: true, participation: 1, rank: 4 })], best: key({ blocks: 5, voter: true, participation: 1, rank: 4 }) });
  r = V.ladderRungs(s, c, T5);
  assert.equal(r[2].state, 'done');
  assert.equal(r[2].line, 'Your signature is in checkpoint 8,496.');
  assert.equal(r[2].sub, '30 signed and locked · presence 100% · latest lock 8,496');
  assert.equal(r[5].state, 'done');
  assert.equal(r[5].line, 'Signing 8 of 80 points, 15 min unbroken.');
  // 4. the full window: two hours on the devnet; the rank
  s = stateOf({ ...s, ladder: { ...s.ladder, first_block_at: T5 - 7300, blocks_30d: 61, blocks_today: 61 } });
  c = chainOf({ latest_locked_index: 8700, mine: [key({ blocks: 61, voter: true, participation: 1, rank: 3 })], best: key({ blocks: 61, voter: true, participation: 1, rank: 3 }) });
  r = V.ladderRungs(s, c, T5);
  assert.equal(r[3].state, 'done');
  assert.equal(r[3].line, '2 of 2 hours. Full weight.');
  assert.equal(r[4].line, 'Rank 3 of 4 keys.');
  assert.equal(r[4].sub, '14 keys vote · weight, never hashrate: a card that arrived today sits at the bottom');
  // P. a paid shard
  s = stateOf({ ...s, settings: { ...s.settings, prove: true }, proving: { paid: 1, paid_wei: '1150000000000000000' }, ladder: { ...s.ladder, shards: [{ block: 1284117, shard: 3, wei: '1150000000000000000', at: T5 - 30 }] } });
  r = V.ladderRungs(s, c, T5);
  assert.equal(r[6].state, 'done');
  assert.equal(r[6].line, 'Your card proved shard 3 of block 1,284,117.');
  assert.equal(r[6].sub, '1.15 IGN · 1 paid');
  assert.deepEqual(r.map((x) => x.state), ['done', 'done', 'done', 'done', 'done', 'done', 'done']);
  r.forEach((x) => assert.match(x.field, RPC));
  assert.equal(V.currentRung(r).id, 'shard');
  // proving off: the rung reads off, never a fault
  assert.equal(V.ladderRungs(stateOf(), chainOf(), T5)[6].line, 'Proving is off.');
});

test('first-block-21 (the founder, 7 October 2026): the first-block card is seen once in the app’s life, never on a later run', () => {
  const m1 = { kind: 'first', count: 1, card: 'nvidia:0:RTX 4070', card_name: 'NVIDIA GeForce RTX 4070', hash: '9f3a' + 'ab'.repeat(28) + 'c21e', daa: 1284117, at: T5 - 60 };
  const c = chainOf({ mine: [key({ blocks: 1, rank: 4 })], best: key({ blocks: 1, rank: 4 }) });
  const afterFirst = (over, ladderOver) => stateOf({ mining: { ...stateOf().mining, accepted_total: 1, accepted_session: 0, found: [] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 7200, first_block_card: 'nvidia:0:RTX 4070', first_block_hash: m1.hash, first_block_daa: 1284117, days_mined_30: 1, blocks_30d: 1, blocks_today: 1, first_block_shown: true, ...ladderOver }, ...over });
  const stale = { ...m1, at: T5 - 7200 };
  // KNOWN-FAILED FIRST: a second run against a an earlier release engine (its ladder has no first_block_shown field; the engine up
  // 30 s, the lifetime count already 1, the card raised two hours ago on the first run and re-sent from its record)
  // showed "your first block" again before this change
  let s = afterFirst({ uptime_s: 30, started_at: T5 - 30 }, { milestone: stale });
  delete s.ladder.first_block_shown;
  assert.equal(V.blockCard(stale, s, c, T5), null, 'a second run with the count at 1 shows no first-block card');
  assert.equal(V.staleCard(stale, s, T5), true);
  // the first ever block: the card, in the run that raised it, and the body the page reports when it displays it
  s = stateOf({ started_at: T5 - 1800, mining: { ...stateOf().mining, accepted_total: 1, accepted_session: 1, found: [T5 - 60] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 60, first_block_card: 'nvidia:0:RTX 4070', first_block_hash: m1.hash, first_block_daa: 1284117, days_mined_30: 1, blocks_30d: 1, blocks_today: 1, first_block_shown: false, milestone: m1 } });
  const bc = V.blockCard(m1, s, c, T5);
  assert.equal(bc.eyebrow, 'your first block');
  assert.equal(bc.title, 'Block 1,284,117 is yours.');
  assert.deepEqual(V.cardSeenBody(bc), { count: 1, at: T5 - 60 });
  assert.equal(V.cardSeenBody(null), null);
  assert.equal(V.firstWait(s), false, 'the count-up gives way to the card');
  // seen once (main's reading): a block found overnight with no window, the engine restarted since (an auto-update):
  // a an earlier release engine keeps the unseen card and the first open shows it, however old it is
  s = afterFirst({ uptime_s: 30, started_at: T5 - 30 }, { first_block_shown: false, milestone: stale });
  assert.equal(V.staleCard(stale, s, T5), false, 'a 0.1.21 engine owns the rule');
  assert.equal(V.blockCard(stale, s, c, T5).eyebrow, 'your first block');
  // a second open after it was seen: the engine dropped the card at its start (ladder.rs start_run); the first rung
  // stays done and the count-up never comes back
  s = afterFirst({ uptime_s: 12, started_at: T5 - 12 }, { milestone: null });
  assert.equal(V.blockCard(s.ladder.milestone, s, c, T5), null);
  assert.equal(V.ladderRungs(s, c, T5)[0].state, 'done');
  assert.equal(V.firstWait(s), false);
  // a restart with the count at 1 and the card seen: nothing to show
  s = afterFirst({ uptime_s: 45, started_at: T5 - 45 }, { milestone: null });
  assert.equal(V.blockCard(s.ladder.milestone, s, c, T5), null);
  // an update with the count at 1 from an earlier release: the engine takes the flag from the old record and sends no card; a
  // an earlier release engine without started_at re-sending its card is refused through uptime_s
  s = afterFirst({ uptime_s: 45, started_at: T5 - 45, version: '0.1.21' }, { milestone: null });
  assert.equal(V.blockCard(s.ladder.milestone, s, c, T5), null);
  s = afterFirst({ uptime_s: 45, started_at: 0 }, { milestone: stale });
  delete s.ladder.first_block_shown;
  assert.equal(V.blockCard(stale, s, c, T5), null);
  // a kept data directory with the count at 0 and the flag unset: the count-up shows, then the first block raises the card
  s = stateOf({ uptime_s: 300, started_at: T5 - 300, ladder: { ...stateOf().ladder, first_block_shown: false, first_synced_at: T5 - 86400 * 3, first_mining_at: T5 - 86400 * 3, votes_signed: 40 } });
  assert.equal(V.firstWait(s), true);
  assert.equal(V.ladderRungs(s, chainOf(), T5)[0].line, 'Waiting for your first block.');
  // on the share lane (LAUNCH-GUARDS.md 2.9) the wait is for a share: the shares tile carries it, never the count-up
  assert.equal(V.firstWait({ ...s, mining: { ...s.mining, share_lane: true } }), false);
  s = stateOf({ uptime_s: 400, started_at: T5 - 400, mining: { ...stateOf().mining, accepted_total: 1, accepted_session: 1, found: [T5 - 5] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 5, first_block_shown: false, first_synced_at: T5 - 86400 * 3, first_mining_at: T5 - 86400 * 3, votes_signed: 40, milestone: { ...m1, at: T5 - 5 } } });
  assert.equal(V.blockCard(s.ladder.milestone, s, c, T5).eyebrow, 'your first block');
  assert.equal(V.firstWait(s), false);
  // a count that started over (settings.json lost, the ladder kept with the flag): no "waiting", no card
  s = stateOf({ mining: { ...stateOf().mining, accepted_total: 0, found: [] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 86400, first_block_shown: true, milestone: null } });
  assert.equal(V.firstWait(s), false);
  assert.equal(V.ladderRungs(s, chainOf(), T5)[0].state, 'done');
  assert.equal(V.blockCard(null, s, c, T5), null);
  // block 2: the ordinary accepted pulse only (the block on the strip's list, the activity line, the run count), no card
  s = afterFirst({ uptime_s: 1800, started_at: T5 - 1800, mining: { ...stateOf().mining, accepted_total: 2, accepted_session: 1, found: [T5 - 2] } }, { milestone: null, blocks_30d: 2, blocks_today: 2, blocks_24h: 1 });
  assert.equal(V.blockCard(s.ladder.milestone, s, c, T5), null);
  assert.equal(V.plainEvent('block accepted by the node (NVIDIA GeForce RTX 4070)').text, 'Block found by NVIDIA GeForce RTX 4070');
  assert.equal(V.earningsLines(s, c, 28, T5).all.sub, '2 blocks', 'an engine without the earned windows: the lifetime count alone');
  assert.equal(V.earningsLines(s, c, 28, T5).all.text, 'reading');
  assert.deepEqual(s.mining.found, [T5 - 2], 'the strip flashes the block that just arrived');
  assert.equal(V.ladderRungs(s, c, T5)[0].state, 'done');
});

test('the rungs under mainnet-sized params: 100 blocks to a vote, 30 days to full weight; the relay and the cap are named', () => {
  const s = stateOf({ mining: { ...stateOf().mining, accepted_total: 61, found: [] }, ladder: { ...stateOf().ladder, first_block_at: T5 - 23 * 86400, days_mined_30: 23, blocks_30d: 61 } });
  const c = chainOf({ source: 'observer', keys_listed: 64, keys_cap: 64, voters: 1204, mine: [key({ blocks: 61, rank: 41 })], best: key({ blocks: 61, rank: 41 }) }, MAINNET);
  const r = V.ladderRungs(s, c, T5);
  assert.equal(r[1].line, '61 of 100 blocks to a vote.');
  assert.equal(r[3].line, '23 of 30 days.');
  assert.equal(r[3].progress, 23 / 30);
  assert.equal(r[4].line, 'Rank 41 of 64 keys.');
  assert.equal(r[4].sub, '1,204 keys vote · the top 64 are listed · weight, never hashrate: a card that arrived today sits at the bottom');
  // a key below the observer's 64 says so instead of inventing a rank
  const below = V.ladderRungs(s, chainOf({ source: 'observer', keys_listed: 64, keys_cap: 64 }, MAINNET), T5);
  assert.equal(below[4].line, 'Below the top 64 keys.');
  assert.equal(below[1].sub, 'your key is not in the table yet');
  // no chain facts at all: the words say so, nothing is invented
  const none = V.ladderRungs(s, { ok: false, error: 'neither the node nor the observer answered' }, T5);
  assert.equal(none[1].line, 'A vote needs the dust line’s blocks in the window.');
  assert.equal(none[1].sub, 'neither the node nor the observer answered');
  assert.equal(none[4].line, 'Rank: not read yet.');
  assert.deepEqual(V.windowWords(2592000), { n: 30, unit: 'days', text: '30 days' });
  assert.deepEqual(V.windowWords(7200), { n: 2, unit: 'hours', text: '2 hours' });
  assert.deepEqual(V.windowWords(0), { n: 30, unit: 'days', text: '30 days' });
});

test('the IGN formatter', () => {
  assert.equal(V.fmtIgn(0.5), '0.5000 IGN'); assert.equal(V.fmtIgn(4.88044084), '4.88 IGN'); assert.equal(V.fmtIgn(207360), '207,360 IGN');
});
test('the block card on the four thresholds and on a new card; the first-hour timeline; the profile words', () => {
  const s = stateOf(), c = chainOf({ mine: [key({ blocks: 100, voter: true, rank: 2 })], best: key({ blocks: 100, voter: true, rank: 2 }) });
  const m = (kind, count, over) => ({ kind, count, card: 'nvidia:0:RTX 4070', card_name: 'NVIDIA GeForce RTX 4070', hash: 'ab'.repeat(32), daa: 1284117, at: T5 - 10, ...over });
  assert.equal(V.blockCard(m('hundred', 100), s, c, T5).eyebrow, 'block 100 of this machine');
  assert.equal(V.blockCard(m('thousand', 1000), s, c, T5).eyebrow, 'block 1,000 of this machine');
  assert.equal(V.blockCard(m('ten_thousand', 20000), s, c, T5).eyebrow, 'block 20,000 of this machine');
  assert.equal(V.blockCard(m('card', 7), s, c, T5).eyebrow, 'first block on NVIDIA GeForce RTX 4070');
  assert.equal(V.blockCard(m('first', 1, { hash: '', daa: 0 }), s, c, T5).title, 'A block is yours.');
  assert.equal(V.blockCard(m('first', 1, { hash: '', daa: 0 }), s, c, T5).url, '');
  assert.equal(V.blockCard(m('first', 1), s, chainOf({ network: { ...chainOf().network, ramp_factor: 0.19, miner_ign_per_block: 4.88044084 } }), T5).ignLine, '4.88 IGN to 0xdd44…86E8 (72 producer points, 8 for signing, ramp 19%)');
  assert.equal(V.blockCard(m('first', 1), s, c, T5).key, 'card:1:' + (T5 - 10));
  const t = V.timeline(stateOf({ ladder: { ...stateOf().ladder, first_synced_at: T5 - 2800, first_mining_at: T5 - 2700, first_block_at: T5 - 2400 } }), T5);
  assert.deepEqual(t.steps.map((x) => x.id), ['install', 'started', 'synced', 'mining', 'block', 'payout']);
  assert.deepEqual(t.steps.map((x) => x.rel), ['+0 s', '+20 min', '+3 min', '+5 min', '+10 min', '+10 min']);
  assert.match(t.steps[5].note, /coinbase of the first block/);
  const u = V.timeline(stateOf({ installed_at: 0, uptime_s: 100 }), T5);
  assert.equal(u.steps[0].note, 'not recorded on this install');
  assert.equal(u.steps[2].note, 'the moment is not recorded; synced now');
  assert.equal(V.pageLine({ port: 18080, wanted: 18080, lan: false }), 'Page http://127.0.0.1:18080/');
  assert.equal(V.pageLine({ port: 18081, wanted: 18080, lan: true }), 'Page http://127.0.0.1:18081/ · also on every address of this machine · port 18080 was in use');
  assert.equal(V.pageLine({}), '');
  assert.match(V.profileWords(true), /^Your address page on igneum\.network is public/);
  assert.match(V.profileWords(false), /unlisted/);
  assert.equal(V.gapWords(45), '45 seconds'); assert.equal(V.spanWords(3700), '1 h 1 min'); assert.equal(V.spanWords(3 * 86400), '3 days');
  // the Activity lines for the milestones
  assert.equal(V.plainEvent('first block: found by NVIDIA GeForce RTX 4070 (block card)').text, 'Your first block, found by NVIDIA GeForce RTX 4070');
  assert.equal(V.plainEvent('block 1,000 of this machine: found by RTX 4070 (block card)').text, 'Block 1,000 of this machine, found by RTX 4070');
  assert.equal(V.plainEvent('first block on Apple M5 Max (block card)').text, 'First block on Apple M5 Max');
});

// ---------- vendor marks (gpu-logos, 7 October 2026) ----------
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.css'), 'utf8');
const marks = (html) => (html.match(/data-mark="/g) || []).length;

test('every vendor renders its own mark: one inline SVG glyph per row, tinted by its vendor class, under 2 KB each', () => {
  const rows = {
    nvidia: card(),
    amd: card({ key: 'amd:gfx1201', name: 'AMD Radeon RX 9070 XT', code: 'gfx1201', vendor: 'amd', vram_mb: 16384 }),
    intel: card({ key: 'intel:Intel(R) Arc(TM) B580 Graphics', name: 'Intel(R) Arc(TM) B580 Graphics', vendor: 'intel', worker: 'OpenCL', vram_mb: 12208 }),
    apple: card({ key: 'apple::Apple M5 Max', name: 'Apple M5 Max', vendor: 'apple', kind: 'apple', worker: 'Metal', vram_mb: 131072 }),
  };
  for (const [v, cd] of Object.entries(rows)) {
    const mk = V.vendorMark(cd);
    assert.equal(mk.vendor, v);
    assert.match(mk.svg, /^<svg viewBox="0 0 24 24"/);
    assert.ok(Buffer.byteLength(mk.svg) < 2048, v + ' glyph is ' + Buffer.byteLength(mk.svg) + ' bytes');
    assert.ok(!/<image|href=|data:/.test(mk.svg), v + ' glyph is drawn, never a raster or a file');
    assert.ok(/currentColor/.test(mk.svg), v + ' glyph takes the vendor token through currentColor');
    const html = V.markHtml(cd);
    assert.ok(html.startsWith('<div class="badge ' + v + '" data-mark="' + v + '"'), html.slice(0, 60));
    assert.equal((html.match(/<svg/g) || []).length, 1);
  }
  assert.notEqual(V.MARKS.nvidia, V.MARKS.amd); assert.notEqual(V.MARKS.intel, V.MARKS.apple);
});

test('an unknown vendor renders the neutral fallback glyph; a vendor "other" row whose name says Intel keeps the Intel glyph', () => {
  const mali = card({ key: 'other:0:Mali', name: 'Mali-G78 MP20', vendor: 'other', kind: 'integrated', worker: 'OpenCL', vram_mb: 0 });
  const mk = V.vendorMark(mali);
  assert.equal(mk.vendor, 'gpu'); assert.equal(mk.label, 'GPU'); assert.equal(mk.svg, V.MARKS.gpu);
  assert.ok(V.markHtml(mali).startsWith('<div class="badge gpu" data-mark="gpu"'));
  assert.equal(V.vendorOf(card({ name: 'Intel UHD Graphics 770', vendor: 'other', kind: 'integrated' })), 'intel');
  assert.equal(V.vendorOf(card({ name: 'AMD Radeon(TM) Graphics', vendor: 'other', kind: 'integrated' })), 'amd');
  assert.equal(V.vendorOf({}), 'gpu');
});

test('the mark never appears twice on one row: the head of a first-run row and of a Cards row carries one data-mark, and only View draws a badge', () => {
  const amd = card({ key: 'amd:gfx1201', name: 'AMD Radeon RX 9070 XT', code: 'gfx1201', vendor: 'amd' });
  assert.equal(marks(V.rowHead(amd)), 1);
  assert.equal(marks(V.rowHead(amd, ': removed')), 1);
  assert.equal(marks(V.markHtml(amd) + V.nameHtml(amd)), 1);
  assert.equal(marks(V.nameHtml(amd)), 0, 'the name block carries no mark of its own');
  // the renderers: the first-run rows, the Cards rows and the block card all go through View.markHtml; no other
  // string in app.js opens a badge
  assert.equal((src.match(/class="badge /g) || []).length, 1, 'one place in app.js opens a badge');
  assert.equal((src.match(/View\.markHtml\(/g) || []).length, 3, 'first-run rows, Cards rows, the block card');
  assert.equal(src.includes('badgeHtml('), false);
});

test('the series line under the name is mono text per generation; an integrated row (Intel iGPU, Apple) ends in "integrated" and keeps one mark', () => {
  assert.equal(V.seriesLine(card()), 'RTX 50 series · Blackwell');
  assert.equal(V.seriesLine(card({ name: 'NVIDIA GeForce RTX 4070' })), 'RTX 40 series · Ada Lovelace');
  assert.equal(V.seriesLine(card({ name: 'NVIDIA GeForce RTX 3080' })), 'RTX 30 series · Ampere');
  assert.equal(V.seriesLine(card({ name: 'NVIDIA GeForce GTX 1660 Super' })), 'GTX 16 series · Turing');
  assert.equal(V.seriesLine(card({ name: 'AMD Radeon RX 9070 XT', vendor: 'amd' })), 'RX 9000 series · RDNA 4');
  assert.equal(V.seriesLine(card({ name: 'AMD Radeon RX 7800 XT', vendor: 'amd' })), 'RX 7000 series · RDNA 3');
  assert.equal(V.seriesLine(card({ name: 'gfx1201', code: 'gfx1201', vendor: 'amd' })), 'RDNA 4');
  assert.equal(V.seriesLine(card({ name: 'Intel(R) Arc(TM) B580 Graphics', vendor: 'intel' })), 'Arc B series · Battlemage');
  assert.equal(V.seriesLine(card({ name: 'Intel Arc A770', vendor: 'intel' })), 'Arc A series · Alchemist');
  assert.equal(V.seriesLine(card({ name: 'Intel UHD Graphics 770', vendor: 'other', kind: 'integrated' })), 'UHD Graphics · integrated');
  assert.equal(V.seriesLine(card({ name: 'Intel Iris Xe Graphics', vendor: 'other', kind: 'integrated' })), 'Iris Xe · integrated');
  assert.equal(V.seriesLine(card({ name: 'AMD Radeon(TM) Graphics', vendor: 'amd', kind: 'integrated' })), 'Radeon · integrated');
  assert.equal(V.seriesLine(card({ name: 'Apple M5 Max', vendor: 'apple', kind: 'apple' })), 'M5 series · integrated');
  const igpu = card({ name: 'Intel UHD Graphics 770', vendor: 'other', kind: 'integrated' });
  const head = V.rowHead(igpu);
  assert.equal(marks(head), 1); assert.match(head, /data-mark="intel"/); assert.match(head, /<div class="gen">UHD Graphics · integrated<\/div>/);
  assert.equal(V.vendorMark(igpu).svg, V.MARKS.intel, 'an integrated Intel row keeps the Intel glyph, never a second mark');
  assert.equal(V.nameHtml(card({ name: '<b>x</b>', vendor: 'zzz', kind: 'unknown' })).includes('<b>'), false, 'the name is escaped');
});

test('the light theme contrast ratio of every vendor colour on its well is at least 3:1, and app.css carries the same hex values and wells', () => {
  for (const v of Object.keys(V.VENDORS)) {
    const light = V.vendorContrast(v, 'light'), dark = V.vendorContrast(v, 'dark');
    assert.ok(light >= 3, v + ' light: ' + light.toFixed(2) + ':1 on its well');
    assert.ok(dark >= 3, v + ' dark: ' + dark.toFixed(2) + ':1 on its well');
    const [r, g, b] = V.hexRgb(V.VENDORS[v].light), [dr, dg, db] = V.hexRgb(V.VENDORS[v].dark);
    assert.ok(css.includes('--mark-' + v + ':' + V.VENDORS[v].dark + ';'), v + ' dark hex in app.css');
    assert.equal((css.match(new RegExp('--mark-' + v + ':' + V.VENDORS[v].light + ';', 'g')) || []).length, 2, v + ' light hex in both light blocks');
    assert.ok(css.includes('--mark-' + v + '-well:rgba(' + dr + ',' + dg + ',' + db + ',' + String(V.WELL_ALPHA_DARK).replace(/^0/, '') + ')'), v + ' dark well');
    assert.equal((css.match(new RegExp('--mark-' + v + '-well:rgba\\(' + r + ',' + g + ',' + b + ',' + String(V.WELL_ALPHA_LIGHT).replace(/^0/, '') + '\\)', 'g')) || []).length, 2, v + ' light well in both light blocks');
  }
  // the ember accent is for state, never a brand
  for (const v of Object.keys(V.VENDORS)) { assert.notEqual(V.VENDORS[v].dark, '#F2541B'); assert.notEqual(V.VENDORS[v].light, '#E04A14'); }
  assert.equal(/\.badge\.(nvidia|amd|intel|apple|gpu)\{[^}]*var\(--ember/.test(css), false, 'no badge rule tints with ember');
  assert.equal(/\.badge[^{]*\{[^}]*animation/.test(css), false, 'nothing on the mark animates');
});

// ---------- the custom switch (the Prove page fix, 7 October 2026) ----------
// the founder's screenshot from the live an earlier release Mac app: a thin white rectangle above-left of the "Prove on this machine"
// switch and the knob drawn outside the track's left edge at off. Two generic classes reached the switch (the DAG
// legend swatch .lg hit label.switch.lg, the shard track .track hit .switch .track) and the native input was an
// unpositioned absolute. This test reads app.css: no bare .lg or .track rule may exist (every use is scoped), the
// native input is hidden the accessible way (1 px clip, still focusable), and the knob's box sits inside the track's
// box at off and at on for both sizes, in the base rules and untouched by both theme blocks.
const cssRules = (text) => { const out = []; const re = /([^{}]+)\{([^{}]*)\}/g; let m; text = text.replace(/\/\*[\s\S]*?\*\//g, ''); while ((m = re.exec(text))) out.push({ sel: m[1].trim(), body: m[2] }); return out; };
const ruleOf = (sel) => cssRules(css).filter((r) => r.sel.split(',').map((x) => x.trim()).includes(sel)).map((r) => r.body).join(';');
const px = (body, prop) => { const m = new RegExp('(?:^|;)\\s*' + prop + ':\\s*(-?[\\d.]+)px').exec(body); return m ? parseFloat(m[1]) : null; };
const translate = (body) => { const m = /transform:\s*translateX\((-?[\d.]+)px\)/.exec(body); return m ? parseFloat(m[1]) : 0; };

test('no generic .lg or .track rule reaches a custom switch: every use of those class names is scoped (known-failed on the an earlier release CSS)', () => {
  const bare = cssRules(css).flatMap((r) => r.sel.split(',').map((x) => x.trim())).filter((sel) => /^\.(lg|track)(\.[\w-]+)*(\s+[\w.:-]+)*$/.test(sel) && !/^\.switch\b/.test(sel));
  assert.deepEqual(bare, [], 'selectors that start with a bare .lg or .track: ' + bare.join(' | '));
  assert.equal(/<label class="switch lg"/.test(readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'index.html'), 'utf8')), false, 'the Prove switch no longer shares the legend swatch class');
});

test('the native input of every custom switch is hidden the accessible way: 1 px clipped, still in the tree and focusable, the label kept', () => {
  const sw = ruleOf('.switch'), input = ruleOf('.switch input');
  assert.match(sw, /position:\s*relative/, '.switch is the positioned ancestor of its input');
  assert.match(input, /position:\s*absolute/);
  assert.equal(px(input, 'width'), 1); assert.equal(px(input, 'height'), 1);
  assert.match(input, /clip:\s*rect\(0 0 0 0\)|clip-path:\s*inset\(50%\)/, 'the clip pattern');
  assert.match(input, /overflow:\s*hidden/);
  assert.equal(/display:\s*none|visibility:\s*hidden/.test(input), false, 'display none would take the input out of the tab order');
  assert.match(ruleOf('.switch input:focus-visible+.track'), /outline/, 'focus stays visible on the track');
});

test('the knob sits inside the track at off and at on, both sizes, and neither theme block touches the switch geometry', () => {
  const track = ruleOf('.switch .track'), knob = ruleOf('.switch .track::after'), on = ruleOf('.switch input:checked+.track::after');
  const big = ruleOf('.switch.big .track'), bigKnob = ruleOf('.switch.big .track::after'), bigOn = ruleOf('.switch.big input:checked+.track::after');
  const inside = (tw, th, kw, kh, left, top, dx, label) => {
    assert.ok(left >= 0 && top >= 0, label + ': the knob starts inside (left ' + left + ', top ' + top + ')');
    assert.ok(left + kw <= tw && top + kh <= th, label + ' off: knob ' + kw + 'x' + kh + ' at ' + left + ',' + top + ' in a ' + tw + 'x' + th + ' track');
    assert.ok(left + dx + kw <= tw, label + ' on: knob right edge ' + (left + dx + kw) + ' in a ' + tw + ' track');
    assert.ok(left + dx >= 0, label + ' on: knob left edge ' + (left + dx));
  };
  assert.match(track, /position:\s*relative/, 'the track positions its knob');
  inside(px(track, 'width'), px(track, 'height'), px(knob, 'width'), px(knob, 'height'), px(knob, 'left'), px(knob, 'top'), translate(on), 'the 40 px switch');
  inside(px(big, 'width'), px(big, 'height'), px(bigKnob, 'width'), px(bigKnob, 'height'), px(knob, 'left'), px(knob, 'top'), translate(bigOn), 'the big switch');
  // the two theme blocks (prefers-color-scheme light, data-theme light) only set tokens: no .switch or .track rule inside them
  for (const m of css.matchAll(/@media \(prefers-color-scheme:light\)\{:root:not\(\[data-theme="dark"\]\)\{[^}]*\}\}|:root\[data-theme="light"\]\{[^}]*\}/g)) assert.equal(/\.switch|\.track/.test(m[0]), false, 'a theme block touches the switch');
  assert.equal(cssRules(css).some((r) => /\.switch|\.track/.test(r.sel) && /@media \(prefers-color-scheme/.test(r.sel)), false);
});

// ---------- the shared marks module (brand/marks/vendor-marks.mjs, for the site) ----------
test('brand/marks/vendor-marks.mjs carries the app\'s marks and tokens verbatim, plus the Windows mark in the same treatment', async () => {
  const mod = await import(join(dirname(fileURLToPath(import.meta.url)), '../../../brand/marks/vendor-marks.mjs'));
  assert.deepEqual(mod.MARKS, V.MARKS, 'the glyph strings (regenerate with node brand/marks/regen.mjs)');
  assert.deepEqual(mod.VENDORS, V.VENDORS);
  assert.deepEqual(mod.WELL_ALPHA, { dark: V.WELL_ALPHA_DARK, light: V.WELL_ALPHA_LIGHT });
  assert.equal(mod.OS_MARKS.macos, V.MARKS.apple);
  assert.match(mod.OS_MARKS.windows, /^<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true" focusable="false" fill="currentColor"><path /);
  assert.ok(Buffer.byteLength(mod.OS_MARKS.windows) < 2048);
  assert.equal(mod.markHtml('amd'), V.markHtml({ vendor: 'amd' }));
  assert.equal(mod.markHtml('zzz'), V.markHtml({ vendor: 'zzz' }));
  // the tokens the module prints are the ones app.css carries
  for (const line of mod.tokensCss().split('\n')) { const inner = /\{([^{}]*)\}\}?$/.exec(line)[1]; assert.ok(css.includes(inner), 'app.css carries: ' + inner.slice(0, 60)); }
});

// ---------- the Earnings card, tidied (the founder, 7 October 2026: "remove all the type a price stuff") ----------
const indexHtml = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'index.html'), 'utf8');
const earningsCard = () => { const a = indexHtml.indexOf('id="page-earnings"'); return indexHtml.slice(a, indexHtml.indexOf('</section>', a)); };

test('no price input exists: no £ per IGN field, no "Use it", no remembered price, no money line derived from a typed price (known-failed on an earlier release)', () => {
  const ec = earningsCard();
  assert.equal(/e-price|Type a price|Use it|price-row|per IGN/.test(ec), false, 'the markup');
  assert.equal(/<input[^>]*type="number"/.test(ec), false, 'no number input on the card');
  assert.equal(/ign_price|ignPrice|saveIgnPrice|e-price/.test(src), false, 'the setting and its handlers');
  const e = V.earningsLines(stateOf(), chainOf(), 28, T5);
  assert.equal('price' in e, false); assert.equal(V.FIELDS.price, undefined);
  for (const k of Object.keys(e)) assert.equal(/£|GBP|pounds?\b/.test((e[k] && e[k].text) || ''), k === 'cost', k + ' carries money only on the electricity row');
});

test('the "Prove instead of mining" row shows only on an under-12 GB machine and names the choice (main, 7 October 2026)', () => {
  assert.equal(V.proveInsteadWords({ under_12gb: false }, { prove_instead: false }).show, false);
  const off = V.proveInsteadWords({ under_12gb: true }, { prove_instead: false });
  assert.equal(off.show, true); assert.equal(off.on, false); assert.match(off.help, /never both/);
  const on = V.proveInsteadWords({ under_12gb: true }, { prove_instead: true });
  assert.equal(on.show, true); assert.equal(on.on, true); assert.match(on.help, /held off/);
});

// ---------- currency (currency-21, the founder, 7 October 2026: "£/day is for UK; we need other currencies") ----------
// No IP lookups: the OS locale's region picks the currency and the default tariff at first run; a Settings override
// (currency plus the price per kWh in it) wins and lives in the engine's settings file; every money line goes through
// one Intl formatter; the user's own tariff is the number, so no exchange rate exists anywhere.
const uiDir = dirname(fileURLToPath(import.meta.url));
const pageHtml = readFileSync(join(uiDir, 'index.html'), 'utf8');
const rustSrc = (f) => readFileSync(join(uiDir, '../src/' + f), 'utf8');

test('a de-DE machine reads EUR with the EU tariff from its locale, before any choice is made (known-failed on an earlier release)', () => {
  const ch = V.currencyChoice({ region: '', currency: '' }, 'de-DE');
  assert.equal(ch.code, 'EUR'); assert.equal(ch.region, 'de'); assert.equal(ch.source, 'locale'); assert.equal(ch.prompt, false);
  assert.equal(V.regionOf('de').price, 38.69); assert.match(V.regionOf('de').source, /Eurostat/);
  assert.equal(V.regionFromLocale('de-DE'), 'de'); assert.equal(V.regionFromLocale('en-GB'), 'gb'); assert.equal(V.regionFromLocale('de'), 'de'); assert.equal(V.regionFromLocale('fr-CA'), 'ca'); assert.equal(V.regionFromLocale(''), '');
  const ca = V.currencyChoice({}, 'fr-CA'); assert.equal(ca.code, 'CAD'); assert.equal(V.regionOf('ca').name, 'Canada'); assert.equal(V.regionOf('ca').cur, 'CAD');
  assert.equal(V.currencyChoice({}, 'en-AU').code, 'AUD');
  assert.equal(V.currencyChoice({}, 'fr-FR').code, 'EUR'); assert.ok(V.regionOf('fr').price > 0, 'an EU member without its own row takes the EU average'); assert.match(V.regionOf('fr').source, /EU average/);
  assert.equal(V.currencyChoice({}, 'sv-SE').code, 'SEK'); assert.equal(V.regionOf('se').price, 0, 'no table price outside the euro: the miner types one');
  // one formatter, the symbol placed as the locale places it, no hand-built money string left
  assert.equal(V.fmtMoney(1.5, 'EUR', 'de-DE'), '1,50 €'); assert.equal(V.fmtMoney(1.5, 'EUR', 'en-IE'), '€1.50');
  assert.equal(V.fmtMoney(1234.5, 'USD', 'en-US'), '$1,235'); assert.equal(V.fmtMoney(1.5, 'CAD', 'en-CA'), '$1.50'); assert.equal(V.fmtMoney(1.5, 'JPY', 'en-US'), '¥2'); assert.equal(V.fmtMoney(null, 'GBP', 'en-GB'), '');
  V.setLocale('de-DE'); V.setRegion('de'); assert.equal(V.money(1.5), '1,50 €'); V.setLocale('en-GB'); V.setRegion('');
  assert.equal((src.match(/currency\.symbol \+ \(/g) || []).length, 0, 'money() no longer concatenates a symbol');
});

test('a user override wins over the locale and the region, and survives restart through the engine settings file', () => {
  const ch = V.currencyChoice({ region: 'gb', currency: 'USD' }, 'de-DE');
  assert.equal(ch.code, 'USD'); assert.equal(ch.source, 'override'); assert.equal(ch.prompt, false);
  assert.equal(V.currencyChoice({ region: 'gb', currency: '' }, 'de-DE').code, 'GBP', 'a chosen region beats the locale');
  assert.equal(V.currencyChoice({ region: 'gb', currency: 'zzz' }, 'en-GB').code, 'GBP', 'an unknown override is ignored');
  V.setLocale('en-GB'); V.setCurrency('USD'); assert.equal(V.money(1.5), '$1.50'); assert.equal(V.currencyNow().code, 'USD'); V.setCurrency(''); V.setRegion('gb'); assert.equal(V.money(1.5), '£1.50'); V.setRegion('');
  // survives restart: a field of the engine's settings file, carried in the state the UI reads, set through api/region
  assert.match(rustSrc('config.rs'), /pub currency: String/); assert.match(rustSrc('state.rs'), /pub currency: String/);
  assert.match(rustSrc('server.rs'), /body\.get\("currency"\)/); assert.match(rustSrc('engine.rs'), /Cmd::Region\(region, price, currency\)/);
  assert.match(rustSrc('config.rs'), /fn currency_survives_a_round_trip|currency_round_trip/, 'the Rust round-trip test exists');
  // the Settings card: a currency picker with "follow the region" first; the price unit follows the pick
  assert.ok(pageHtml.includes('id="s-currency"'), 'the picker');
  assert.deepEqual(V.minorOf('GBP'), { word: 'pence', abbr: 'p', digits: 2 }); assert.equal(V.minorOf('EUR').word, 'euro cents'); assert.equal(V.minorOf('CAD').abbr, 'c'); assert.equal(V.minorOf('JPY').digits, 0); assert.equal(V.minorOf('JPY').word, 'yen');
  assert.ok(V.CURRENCY_CODES.indexOf('GBP') === 0 && V.CURRENCY_CODES.slice(0, 5).join() === 'GBP,EUR,USD,CAD,AUD' && V.CURRENCY_CODES.length > 20, 'the five first, then the long tail');
});

test('a locale with no currency row falls back to USD with the override prompt visible', () => {
  const eo = V.currencyChoice({}, 'eo');
  assert.equal(eo.code, 'USD'); assert.equal(eo.prompt, true); assert.equal(eo.source, 'fallback');
  const aq = V.currencyChoice({}, 'en-AQ'); assert.equal(aq.code, 'USD'); assert.equal(aq.prompt, true);
  assert.equal(V.currencyChoice({}, 'en-GB').prompt, false);
  assert.equal(V.currencyChoice({ region: 'other', currency: '' }, 'eo').prompt, true, '"Somewhere else" with no currency still asks');
  assert.equal(V.currencyChoice({ region: 'other', currency: 'USD' }, 'eo').prompt, false, 'an answer closes the prompt');
  assert.match(V.currencyAsk(eo), /could not tell your currency/);
  assert.equal(V.currencyAsk(V.currencyChoice({}, 'en-GB')), '');
  assert.ok(pageHtml.includes('id="s-currency-ask"') && pageHtml.includes('id="region-currency-ask"'), 'the prompt element on Settings and on the first-run step');
});

// ---------- the two check buttons on Settings (7 October 2026, main's ruling after a "Check now" pressed for the wrong check) ----------
// The update check and the remote-jobs check sat as "Check" and "Check now"; a reader (and a lane) took the jobs one for
// the update one. Each button now names its check, and each handler posts its own route.
test('Settings: "Check for an update" posts api/update/check and "Check for jobs" posts api/jobs/check (known-failed on the old labels)', () => {
  const html = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'index.html'), 'utf8');
  assert.match(html, /<button class="btn small" id="s-update">Check now<\/button>/, 'the update button names its check (the founder\'s words of 12 October 2026)');
  assert.match(html, /id="s-update-channel">Updates: automatic, over the air</, 'the channel is named above the note');
  assert.match(html, /id="s-reset-recommended"[^>]*>Reset to recommended</, 'the reset sits on This machine');
  assert.match(html, /<button class="btn tiny ghost" id="s-jobs-check">Check for jobs<\/button>/, 'the jobs button names its check');
  // the founder's words of 12 October 2026: the Updates card's button is "Check now", under the line that names the
  // channel ("Updates: automatic, over the air"); no other bare "Check now" on the page
  assert.equal((html.match(/>Check now</g) || []).length, 1, 'one "Check now", the Updates card\'s, under the channel line');
  const upd = /\$\('s-update'\)\.addEventListener\('click', function \(\) \{ api\('api\/update\/check', \{\}\);/.exec(src);
  const jobs = /\$\('s-jobs-check'\)\.addEventListener\('click', function \(\) \{ api\('api\/jobs\/check', \{\}\);/.exec(src);
  assert.ok(upd, 'the update button posts api/update/check'); assert.ok(jobs, 'the jobs button posts api/jobs/check');
  assert.equal(/\$\('s-update'\)[^\n]*api\/jobs\/check/.test(src), false); assert.equal(/\$\('s-jobs-check'\)[^\n]*api\/update\/check/.test(src), false);});

// ---------- the network step (an earlier release, main's design with two corrections, 7 October 2026) ----------
// Igneum 2.0 (8 October 2026): one network card, the Igneum 2.0 devnet; the once-test holds the words. An existing install
// keeps what it runs and the card it runs is marked current; nothing switches a running miner without the click and the confirm.
test('network words: one card, selected on a fresh install, current on an existing one; the pending state after a confirmed switch', () => {
  const fresh = { setup_done: false, network_name: '', network_pending: '', settings: { network: '' }, update: {} };
  const w = V.networkWords(fresh);
  assert.deepEqual(w.cards.map((c) => c.id), ['chain-7322']); assert.equal(w.selected, 'chain-7322');
  const old = V.networkWords({ setup_done: true, network_name: 'chain 7322', network_pending: '', settings: { network: '' }, update: {} });
  assert.equal(old.selected, 'chain-7322'); assert.equal(old.current, 'chain-7322'); assert.equal(old.cards[0].current, true);
  const pend = V.networkWords({ setup_done: true, network_name: 'chain 7322', network_pending: 'chain-7322', settings: { network: 'chain-7322' }, update: {} });
  assert.equal(pend.line, 'Switching to the public test network (chain 7322) at the next start: quit Ember and open it again.');
  // the work source (the old base, step 3): the Node card's line names the pool or the node, or asks for one
  assert.equal(V.sourceWords({ settings: { source_kind: '', source_url: '' } }).set, false);
  // the project's pool is the default (0.2.1): the words name it and the field shows it
  assert.equal(V.sourceWords({ settings: { source_kind: '', source_url: '' } }).isDefault, true);
  assert.equal(V.sourceWords({ settings: { source_kind: '', source_url: '' } }).url, 'pool.igneum.network:3342 notls');
  assert.match(V.sourceWords({ settings: {} }).line, /^Mining through the project’s pool pool\.igneum\.network:3342 \(the default\)/);
  assert.match(V.sourceWords({ settings: {} }).line, /https:\/\/rpc\.igneum\.network/);
  // the public test network's coins: the label beside every balance (the founder's word of 12 October 2026)
  assert.equal(V.NO_VALUE_LABEL, 'development-network tokens; no monetary value');
  assert.match(V.earningsLines({ mining: {}, ladder: {}, settings: {} }, null, null, 1791760000).headline.sub, /development-network tokens; no monetary value$/);
  assert.equal(V.sourceWords({ settings: { source_kind: 'pool', source_url: 'pool.igneum.network:3334' } }).line, 'Mining through the pool pool.igneum.network:3334.');
  assert.equal(V.sourceWords({ settings: { source_kind: 'node', source_url: 'http://127.0.0.1:8608' } }).line, 'Mining solo through the node at http://127.0.0.1:8608.');
  assert.match(rustSrc('server.rs'), /"\/api\/source" =>/);
  for (const s of [w, old, pend]) for (const c of s.cards) assert.equal(/\u2014|–/.test(c.label + c.line + c.sub), false);
});

test('the switch is refused without the confirm, and the confirm names what resets', () => {
  assert.equal(V.networkAsk('chain-7322', 'chain 7320'), 'Switch this machine to chain 7322? The node’s data and the mining state on chain 7320 reset at the next start. Your key and your address stay.');
  // the old base (step 3): the chain is pinned in the engine (config::CHAIN_ID) and the old switch rule and route are gone;
  // the work source is the setting the server takes
  assert.match(rustSrc('config.rs'), /pub const CHAIN_ID: u64 = 7322;/);
  assert.match(rustSrc('server.rs'), /"\/api\/source" =>/);
  // the markup: the step sits before the address step, four steps now; the Settings card exists
  const idx = pageHtml;
  assert.ok(idx.indexOf('id="screen-network"') > idx.indexOf('id="screen-region"') && idx.indexOf('id="screen-network"') < idx.indexOf('id="screen-address"'), 'the network step comes after the region step and before the address step');
  // the founder's order of 12 October 2026: the first run is one screen, the address the only field; the other
  // steps stay in the markup for Settings' sheets but Get started opens the address screen
  assert.equal((idx.match(/step [1-4] of 4/g) || []).length, 3, 'the three folded steps keep their markup');
  assert.match(idx, /id="screen-address">[\s\S]*?<div class="eyebrow">the one step<\/div>/, 'the address screen is the one step');
  assert.match(src, /\$\('btn-begin'\)\.addEventListener\('click', function \(\) \{ show\('address'\); \}\);/, 'Get started opens the address screen');
  assert.ok(idx.includes('id="s-network-cards"') && idx.includes('id="ask-network"'), 'the Settings card and its confirm');
});

// ---------- the dashboard's width cap (dash-24, 7 October 2026: gutters inside the app at its 1440p and 4K opening sizes) ----------
// .screen capped the dashboard at 1080 px at every window width; above 1080p the cap scales (1280 at a 1440p-class window,
// 1440 at a 4K-class one) and Settings gains a second column where its cards allow, never stretching a card past 640 px.
test('the dashboard cap scales with the window above 1080p and Settings gains a column (known-failed on the 1080 px cap)', () => {
  const rules = cssRules(css);
  const base = rules.filter((r) => r.sel === '.screen').map((r) => r.body).join(';');
  assert.match(base, /max-width:\s*1080px/, 'the 1080p cap stays the base');
  const block = css.slice(css.indexOf('/* dash-24'));
  assert.ok(block.length > 0, 'the dash-24 block exists');
  assert.match(block, /@media \(min-width:1700px\)\{[^}]*\.screen\{max-width:1280px\}/, '1440p-class window: 1280');
  assert.match(block, /@media \(min-width:2500px\)\{[^}]*\.screen\{max-width:1440px\}/, '4K-class window: 1440');
  assert.match(block, /#page-settings\{display:grid;grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/, 'Settings two columns at the wide cap');
  assert.match(block, /#page-settings>\.card\{max-width:640px/, 'a Settings card never past its design width');
});

// the core-clock knob's display (knob-24, 7 October 2026; the engine's lock_* fields from src/ember.rs lock_result):
// the lock against the unlocked point at the same cap, the knee, the search step while the clock ladder runs, no lever
test('the core-clock knob on the row and the panel: the lock, the knee, the search step, no lever', () => {
  const curve = [{ clock_mhz: 0, power_pct: 100, watts: 374, mhs: 122.5, eff: 0.328 }, { clock_mhz: 0, power_pct: 80, watts: 302, mhs: 122.3, eff: 0.405 },
    { clock_mhz: 2472, power_pct: 80, watts: 290, mhs: 122.3, eff: 0.422 }, { clock_mhz: 1854, power_pct: 80, watts: 227, mhs: 121.6, eff: 0.536 }, { clock_mhz: 1545, power_pct: 80, watts: 206, mhs: 121.1, eff: 0.588 },
    { clock_mhz: 1300, power_pct: 80, watts: 190, mhs: 120.7, eff: 0.635 }, { clock_mhz: 1200, power_pct: 80, watts: 182, mhs: 116.0, eff: 0.637 }];
  const locked = card({ tune_line: 'Tuned: 120.7 MH/s at 190 W (0.635 MH/W)', tune_source: 'full', tune_control: true, tune_clock_mhz: 1300, sweep_pct: 80, sweep_at: NOW - 7200, sweep_watts: 190, sweep_mhs: 120.7, tune_curve: curve, sweep_state: 'idle', sweep_note: '',
    lock_mhz: 1300, lock_mhs: 120.7, lock_w: 190, lock_mhw: 0.635, unlocked_mhs: 122.3, unlocked_w: 302, lock_step: 0, lock_steps: 0, lock_at: NOW - 7200, lock_note: 'rate fell 5.2 percent at 1200 MHz' });
  const lw = V.lockWords(locked);
  assert.equal(lw.kind, 'locked');
  assert.equal(lw.text, 'Locked at 1300 MHz: 120.7 MH/s at 190 W (0.635 MH/W)');
  assert.equal(lw.against, 'unlocked at the same cap: 122.3 MH/s at 302 W');
  assert.equal(lw.knee, 'the knee at 1200 MHz, where the rate fell 5.2%');
  assert.equal(lw.row, 'the lock: 112 W less for 1.3% of rate, knee at 1200 MHz');
  assert.equal(lw.savingW, 112); assert.equal(lw.ratePct.toFixed(1), '1.3'); assert.equal(lw.kneeMhz, 1200);
  const e = V.ember(locked, settings(), NOW);
  assert.equal(e.lock.kind, 'locked');
  assert.match(e.sub, /^1300 MHz at 80% · the lock: 112 W less for 1\.3% of rate, knee at 1200 MHz · next check /);
  assert.deepEqual(e.points.filter((p) => p.knee).map((p) => p.w), [182], 'the knee row is flagged on the curve');
  assert.equal(e.points.filter((p) => p.chosen).length, 1);
  // a lock that lost no rate says so
  assert.equal(V.lockWords({ ...locked, lock_mhs: 122.4 }).row, 'the lock: 112 W less, no rate lost, knee at 1200 MHz');
  // the search ended on a faulted row: the clocks were reset to the last good step
  assert.equal(V.lockWords({ ...locked, lock_mhz: 2472, lock_note: 'fingerprint mismatch at 2163 MHz, clocks reset' }).knee, 'stopped at 2163 MHz on a hash mismatch, clocks reset to the last good step');
  assert.equal(V.lockWords({ ...locked, lock_note: 'the floor at 700 MHz' }).knee, 'no knee down to the floor at 700 MHz');
  assert.equal(V.lockWords({ ...locked, lock_note: 'stopped at 1400 MHz' }).knee, 'stopped at 1400 MHz');
  assert.equal(V.ember({ ...locked, lock_note: 'the floor at 700 MHz' }, settings(), NOW).points.filter((p) => p.knee).length, 0);
  // the first lock step was already past the knee: the clock stays unlocked and the row says why
  const un = V.lockWords({ ...locked, lock_mhz: 0, tune_clock_mhz: 0, lock_mhs: 122.3, lock_w: 302, lock_mhw: 0.405, lock_note: 'rate fell 3.0 percent at 2472 MHz' });
  assert.equal(un.kind, 'unlocked');
  assert.equal(un.text, 'Clock left unlocked: the first lock step fell 3.0% at 2472 MHz');
  assert.equal(un.row, 'clock unlocked, the knee came at 2472 MHz');
  // while the clock ladder runs: the row's sub names the search step inside the tune's step
  const searching = V.ember(card({ state: 'tuning', sweep_state: 'running', tune_step: 9, tune_steps: 14, tune_eta_s: 410, lock_step: 4, lock_steps: 9 }), settings(), NOW);
  assert.equal(searching.label, 'Tuning: step 9 of 14'); assert.equal(searching.sub, 'locking clocks: step 4 of 9 · about 7 min left');
  assert.equal(searching.lock.kind, 'searching'); assert.equal(searching.lock.text, 'Locking clocks: step 4 of 9');
  // no lever (Apple silicon, or a card without a maximum clock): the panel says so, the row adds nothing
  const nl = V.lockWords(card({ vendor: 'apple', lock_note: 'no lever', lock_at: 0, lock_mhz: 0 }));
  assert.equal(nl.kind, 'nolever'); assert.equal(nl.text, 'No clock lock on this card: the system sets its clocks'); assert.equal(nl.row, '');
  // a build without the fields, or a card not yet tuned: nothing is claimed
  assert.equal(V.lockWords(card()).kind, 'none');
  assert.equal(V.lockWords(card({ lock_at: 0, lock_note: '' })).kind, 'none');
  assert.equal(V.ember(card({ tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'full', tune_control: true, tune_clock_mhz: 2470, sweep_pct: 80, sweep_at: NOW - 7200, sweep_state: 'idle', sweep_note: '' }), settings(), NOW).sub.indexOf('the lock'), -1);
  // the rule the panel states: the knob never sets a lock below the knee without the user's own choice
});

// the three tuning tiers (tiers-25, 8 October 2026, main's order after the founder's "these settings need to give the miner user everything
// they need"): each tier's rate, watts and MH/W before the tap from the card's own search (the engine's tiers[] in the agreed
// shape), the saving against stock in watts and money a day, the tier in force per card and fleet-wide, the lock in words, a card
// with no lever as "stock" alone with the reason, the first-run sentence. Known-failed first: V.tierSet is not a function on an earlier release.
test('the tiers on a measured card: three buttons with their numbers, the saving against stock, the one in force, the lock words', () => {
  const tiers = [{ id: 'efficiency', clock_mhz: 1200, power_pct: 80, w: 305, mhs: 121.9, mhw: 0.4, source: 'measured' }, { id: 'balanced', clock_mhz: 1950, power_pct: 90, w: 372, mhs: 126.8, mhw: 0.341, source: 'midpoint' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 450, mhs: 128.4, mhw: 0.285, source: 'stock' }];
  const cd = card({ tiers, tier: 'balanced', tier_note: '', tier_at: NOW - 7200, lock_mhz: 1950, lock_w: 372, tune_control: true });
  const t = V.tierSet(cd, settings(), 28);
  assert.equal(t.kind, 'ready'); assert.equal(t.tiers.length, 3); assert.equal(t.inForce, 'balanced');
  assert.deepEqual(t.tiers.map((x) => x.name), ['Efficiency', 'Balanced', 'Maximum']);
  assert.equal(t.tiers[0].line, '121.9 MH/s · 305 W'); assert.equal(t.tiers[0].sub, '0.400 MH/W · 145 W under stock');
  assert.equal(t.tiers[1].on, true); assert.equal(t.tiers[1].line, '126.8 MH/s · 372 W'); assert.equal(t.tiers[1].sub, '0.341 MH/W · 78 W under stock');
  assert.equal(t.tiers[2].line, '128.4 MH/s · 450 W'); assert.equal(t.tiers[2].sub, '0.285 MH/W · stock');
  assert.equal(t.tiers[0].saveW, 145); assert.equal(t.tiers[2].saveW, 0);
  assert.equal(t.lock, 'locked at 1950 MHz, 372 W; stock 450 W');
  assert.equal(t.note, '');
  // the saving reads against stock in watts, never as money (A7)
  assert.equal(V.tierSet(cd, settings(), 0).tiers[0].sub, '0.400 MH/W · 145 W under stock');
  // the lock words without a lock: the cap alone
  assert.equal(V.tierSet(card({ tiers, tier: 'max', lock_mhz: 0, lock_w: 450 }), settings(), 28).lock, 'unlocked, 450 W; stock 450 W');
});
test('a card with no lever shows one tier, "stock", and says why; measuring and unmeasured cards say so', () => {
  const nl = V.tierSet(card({ vendor: 'apple', tiers: [], tier: '', tier_note: 'no lever: Apple silicon sets its own clocks and power', tier_at: 0 }), settings(), 28);
  assert.equal(nl.kind, 'nolever'); assert.equal(nl.tiers.length, 1); assert.equal(nl.tiers[0].name, 'Stock'); assert.equal(nl.tiers[0].on, true);
  assert.equal(nl.note, 'No lever: Apple silicon sets its own clocks and power');
  // the engine's one stock entry beside the note carries the numbers the button shows
  const nl2 = V.tierSet(card({ vendor: 'apple', tiers: [{ id: 'max', clock_mhz: 0, power_pct: 100, w: 38, mhs: 26.7, mhw: 0.703, source: 'stock' }], tier: 'max', tier_note: 'no lever: Apple silicon sets its own clocks and power', tier_at: NOW - 60 }), settings(), 28);
  assert.equal(nl2.kind, 'nolever'); assert.equal(nl2.tiers[0].line, '26.7 MH/s · 38 W'); assert.equal(nl2.tiers[0].sub, '0.703 MH/W');
  const m = V.tierSet(card({ tiers: [], tier: '', tier_note: '', state: 'tuning', sweep_state: 'running', tune_step: 3, tune_steps: 9 }), settings(), 28);
  assert.equal(m.kind, 'measuring'); assert.equal(m.note, 'Measuring: step 3 of 9'); assert.equal(m.tiers.length, 3); assert.equal(m.tiers[0].line, 'measuring');
  const u = V.tierSet(card({ tiers: [], tier: '', tier_note: '' }), settings(), 28);
  assert.equal(u.kind, 'unmeasured'); assert.equal(u.note, 'Not measured yet: the search runs 2 min into steady mining'); assert.equal(u.tiers[0].line, 'not measured yet');
  assert.equal(V.tierSet(card({ tiers: [], tier: '', tier_note: '' }), settings({ sweep: false }), 28).note, 'Not measured yet: Ember Tune is off in Settings');
  assert.equal(V.tierSet(card({ tiers: [], tier: '', tier_note: '' }), { ...settings({ sweep: false }), worker_layer: true }, 28).note, 'Not measured yet: Ember Tune is off in Settings');
  assert.equal(V.tierSet(card({ tiers: [], tier: '', tier_note: '' }), { ...settings({ sweep: true }), worker_layer: true }, 28).note, 'Not measured yet: the search runs 2 min into steady mining');
  assert.equal(V.tierSet(card({ tiers: [], tier: '', tier_note: '', vendor: 'apple' }), { ...settings({ sweep: false }), worker_layer: true }, 28).note, 'Nothing to tune: macOS drives the card');
  // an engine before the fields: nothing is claimed
  assert.equal(V.tierSet(card(), settings(), 28).kind, 'none');
  assert.equal(V.tierSet(card({ vendor: 'other', kind: 'integrated', sweep_supported: false, tiers: [] }), settings(), 28).kind, 'none');
});
test('the fleet strip sums the measured cards per tier and names the tier in force; the price shows as a figure', () => {
  const t5090 = [{ id: 'efficiency', clock_mhz: 1200, power_pct: 80, w: 305, mhs: 121.9, mhw: 0.4, source: 'measured' }, { id: 'balanced', clock_mhz: 1950, power_pct: 90, w: 372, mhs: 126.8, mhw: 0.341, source: 'midpoint' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 450, mhs: 128.4, mhw: 0.285, source: 'stock' }];
  const t5080 = [{ id: 'efficiency', clock_mhz: 1100, power_pct: 80, w: 147, mhs: 71.2, mhw: 0.484, source: 'measured' }, { id: 'balanced', clock_mhz: 1800, power_pct: 90, w: 250, mhs: 76.1, mhw: 0.304, source: 'midpoint' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 360, mhs: 78.0, mhw: 0.217, source: 'stock' }];
  const cards = [card({ tiers: t5090, tier: 'balanced', tier_note: '' }), card({ key: 'b', name: 'NVIDIA GeForce RTX 5080', tiers: t5080, tier: 'balanced', tier_note: '' }), card({ key: 'c', vendor: 'apple', tiers: [], tier: '', tier_note: 'no lever: Apple silicon sets its own clocks and power' })];
  const f = V.fleetTiers(cards, settings({ tune_tier: 'balanced' }), 28);
  assert.equal(f.inForce, 'balanced'); assert.equal(f.measured, 2); assert.equal(f.tunable, 3);
  assert.equal(f.tiers[0].line, '193.1 MH/s · 452 W'); assert.equal(f.tiers[0].sub, '358 W under stock');
  assert.equal(f.tiers[1].line, '202.9 MH/s · 622 W'); assert.equal(f.tiers[1].on, true);
  assert.equal(f.tiers[2].line, '206.4 MH/s · 810 W'); assert.equal(f.tiers[2].sub, 'stock');
  assert.equal(f.count, '2 of 3 cards measured');
  // the legacy goal name maps to the tier
  assert.equal(V.fleetTiers(cards, settings({ tune_goal: 'rate' }), 28).inForce, 'max');
  assert.equal(V.tierOf('rate'), 'max'); assert.equal(V.tierOf('efficiency'), 'efficiency'); assert.equal(V.tierOf('nonsense'), 'balanced');
  // nothing measured yet: the buttons carry the words, not numbers
  const g = V.fleetTiers([card({ tiers: [], tier: '', tier_note: '' })], settings(), 28);
  assert.equal(g.measured, 0); assert.equal(g.tiers[0].line, 'not measured yet'); assert.equal(g.count, '0 of 1 card measured');
  assert.equal(V.priceOrDefault(0), 25); assert.equal(V.priceOrDefault(28), 28);
});
test('the strip sentence: on at Balanced before the first tune, tuned with the next check, off with the tiers still one tap', () => {
  const nv = (over) => card({ sweep_state: 'idle', sweep_note: '', sweep_at: 0, tune_line: '', tune_control: true, tiers: [], tier: '', tier_note: '', ...over });
  assert.equal(V.tierSentence([nv()], settings({ sweep: true, tune_tier: 'balanced' }), NOW), 'Ember Tune is on at Balanced. The first tune runs 2 min into steady mining and sets each card’s balanced point.');
  assert.match(V.tierSentence([nv({ sweep_at: NOW - 7200, tier: 'balanced', tier_at: NOW - 7200 })], settings({ tune_tier: 'balanced' }), NOW), /^Balanced on 1 card\. Measured 2 h ago, next check /);
  assert.equal(V.tierSentence([nv()], settings({ sweep: false }), NOW), 'Ember Tune is off. The tiers still apply when you tap them.');
  assert.equal(V.tierSentence([nv({ state: 'tuning', sweep_state: 'running', tune_step: 2, tune_steps: 9, tune_eta_s: 500 })], settings(), NOW), 'Measuring NVIDIA GeForce RTX 5090: step 2 of 9, mining again in about 8 min.');
  assert.equal(V.tierSentence([], settings(), NOW), 'No card here can be tuned.');
  assert.equal(V.tierSentence([nv({ vendor: 'apple', tier_note: 'no lever: Apple silicon sets its own clocks and power', tiers: [{ id: 'max', w: 38, mhs: 26.7, mhw: 0.703, source: 'stock' }], sweep_at: NOW - 600 })], settings(), NOW), 'No lever on this machine: Apple silicon sets its own clocks and power. Ember measures and reports.');
  assert.equal(V.fleetTiers([nv({ vendor: 'apple', tier_note: 'no lever: Apple silicon sets its own clocks and power', tiers: [{ id: 'max', w: 38, mhs: 26.7, mhw: 0.703, source: 'stock' }] })], settings(), 28).count, '1 of 1 card measured');
});

// the class flip (Counter ASIC coordinator's order of 10:4x UK, 8 October 2026): a program-class crossing (v4 to v5 at 68,400 on
// an earlier chain) can move a knee-locked card's rate by a few percent, so the engine invalidates the stored tiers and re-runs the search
// within ten minutes, keyed by class (card.tiers_class against card.program_class, card.tiers_remeasure_at). The table must read
// "re-measuring for class v5" with the minute the re-run started and never show the v4-measured watts as current; a knee note
// appears when the measured loss at the same lock (card.knee_loss_pct) is over 2 percent. Known-failed first: on d3d0704a the
// v4 tiers stayed on the buttons after the flip.
test('after a class flip the tiers table reads re-measuring with the start minute, never the old watts; the knee note over 2 percent', () => {
  const tiers = [{ id: 'efficiency', clock_mhz: 1200, power_pct: 80, w: 305, mhs: 121.9, mhw: 0.4, source: 'measured' }, { id: 'balanced', clock_mhz: 1950, power_pct: 90, w: 372, mhs: 126.8, mhw: 0.341, source: 'measured' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 450, mhs: 128.4, mhw: 0.285, source: 'stock' }];
  const startedAt = NOW - 180;
  const flipped = card({ tiers, tier: 'balanced', tier_note: '', tier_at: NOW - 7200, tiers_class: 'v4', program_class: 'v5', tiers_remeasure_at: startedAt, knee_loss_pct: 0, lock_mhz: 1950, lock_w: 372 });
  const t = V.tierSet(flipped, settings(), 28);
  assert.equal(t.kind, 'remeasuring');
  assert.equal(t.note, 'Re-measuring for class v5, started ' + V.minuteOf(startedAt) + '; the class v4 points are out of date');
  assert.equal(t.classFlip, true);
  for (const x of t.tiers) { assert.equal(x.line, 're-measuring for class v5'); assert.equal(x.sub, ''); assert.equal(x.w, 0); }
  assert.equal(t.tiers.some((x) => /305 W|372 W|450 W/.test(x.line + x.sub)), false, 'the v4 watts never read as current');
  assert.equal(t.tiers[1].on, true, 'the tier in force stays marked');
  assert.equal(t.knee, '');
  // the re-run not yet started: queued, with the ten-minute promise
  assert.equal(V.tierSet({ ...flipped, tiers_remeasure_at: 0 }, settings(), 28).note, 'Re-measuring for class v5, queued (within ten minutes of the crossing); the class v4 points are out of date');
  // the knee note: over 2 percent, with the measured figure; 2 or under, nothing
  assert.equal(V.tierSet({ ...flipped, knee_loss_pct: 5.2 }, settings(), 28).knee, 'The knee is being re-found under class v5: 5.2% less rate at the same lock than under class v4.');
  assert.equal(V.tierSet({ ...flipped, knee_loss_pct: 2.0 }, settings(), 28).knee, '');
  assert.equal(V.tierSet({ ...flipped, knee_loss_pct: 1.3 }, settings(), 28).knee, '');
  // the same class: the measured tiers read as before; an engine without the fields reads as before
  assert.equal(V.tierSet({ ...flipped, tiers_class: 'v5' }, settings(), 28).kind, 'ready');
  assert.equal(V.tierSet({ ...flipped, tiers_class: '', program_class: 'v5' }, settings(), 28).kind, 'ready', 'no stored class means no flip to read');
  assert.equal(V.tierSet(card({ tiers, tier: 'balanced', tier_note: '' }), settings(), 28).kind, 'ready');
  // the strip: a flipped card counts as not measured and the fleet buttons say so; the sentence names the re-measure
  const f = V.fleetTiers([flipped, card({ key: 'b', tiers, tier: 'balanced', tier_note: '', tiers_class: 'v5', program_class: 'v5' })], settings(), 28);
  assert.equal(f.measured, 1); assert.equal(f.count, '1 of 2 cards measured'); assert.equal(f.tiers[1].line, '126.8 MH/s · 372 W');
  assert.equal(V.fleetTiers([flipped], settings(), 28).tiers[0].line, 're-measuring for class v5');
  assert.equal(V.tierSentence([flipped], settings(), NOW), 'Class v5 arrived: re-measuring NVIDIA GeForce RTX 5090 for its new points, started ' + V.minuteOf(startedAt) + '. The class v4 tiers are out of date until then.');
  assert.equal(V.tierSentence([flipped, { ...flipped, key: 'b', name: 'B' }], settings(), NOW), 'Class v5 arrived: re-measuring 2 cards for their new points, started ' + V.minuteOf(startedAt) + '. The class v4 tiers are out of date until then.');
  // the minute reads as the machine's clock, HH:MM
  assert.match(V.minuteOf(startedAt), /^\d\d:\d\d$/);
});

// the tiers from the team's table (tiers-table-26, 8 October 2026, main's order: the denominator lane's measured table,
// app/igneum-app/tiers/class-v5-tiers.json, seeds a card's three tiers before its own search runs; a tap applies the table's
// row at once; the card's own search replaces them). The rows carry table: true and label measured|estimated. Known-failed
// first: on tiers-class-25 a table row read as a measured one with no word about where it came from.
test('the tiers from the team\'s table read as the table\'s until the card is measured, and say whether the class was measured or estimated', () => {
  const rows = [{ id: 'efficiency', clock_mhz: 1200, power_pct: 100, w: 311.2, mhs: 133.8, mhw: 0.4299, source: 'measured', label: 'measured', table: true, note: 'class v4 best MH per watt' }, { id: 'balanced', clock_mhz: 1300, power_pct: 100, w: 318.8, mhs: 134.76, mhw: 0.4227, source: 'measured', label: 'measured', table: true, note: 'the knee' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 475.5, mhs: 136.8, mhw: 0.2877, source: 'stock', label: 'measured', table: true, note: 'stock' }];
  const cd = card({ tiers: rows, tier: 'balanced', tier_note: '', tier_at: 0, tiers_class: 'v5', program_class: 'v5', tiers_table: true, tune_control: true });
  const t = V.tierSet(cd, settings(), 28);
  assert.equal(t.kind, 'table'); assert.equal(t.tableLabel, 'measured');
  assert.equal(t.note, 'From the team’s table, measured on this card class; Ember measures this card 2 min into steady mining.');
  assert.equal(t.tiers[1].line, '134.8 MH/s · 319 W'); assert.equal(t.tiers[1].sub, '0.423 MH/W · 157 W under stock · table'); assert.equal(t.tiers[1].on, true);
  assert.equal(t.tiers[2].sub, '0.288 MH/W · stock · table');
  assert.equal(t.tiers[0].note, 'class v4 best MH per watt');
  const est = V.tierSet(card({ ...cd, tiers: rows.map((r) => ({ ...r, label: 'estimated' })) }), settings(), 28);
  assert.equal(est.tableLabel, 'estimated'); assert.match(est.note, /^From the team’s table, estimated for this card class; /);
  // the card's own search replaces the table: the rows carry no table flag and the kind is ready
  assert.equal(V.tierSet(card({ ...cd, tiers: rows.map((r) => { const x = { ...r }; delete x.table; return x; }), tiers_table: false }), settings(), 28).kind, 'ready');
  // the lock words from a table row: the tier in force's clock and watts
  assert.equal(t.lock, 'the balanced row: 1300 MHz, 319 W; stock 476 W');
  // the fleet strip counts a table-seeded card apart
  const f = V.fleetTiers([cd, card({ key: 'b', tiers: [], tier: '', tier_note: '' })], settings(), 28);
  assert.equal(f.measured, 0); assert.equal(f.fromTable, 1); assert.equal(f.count, '0 of 2 cards measured, 1 from the team’s table');
  assert.equal(f.tiers[1].line, '134.8 MH/s · 319 W');
  // the sentence names the table before the first tune
  assert.equal(V.tierSentence([cd], settings({ tune_tier: 'balanced' }), NOW), 'Ember Tune is on at Balanced, set from the team’s table for this card class. The first tune runs 2 min into steady mining and measures this card’s own points.');
  // a no-lever card from the table: stock alone, the table named
  const mac = V.tierSet(card({ vendor: 'apple', tiers: [{ id: 'max', clock_mhz: 0, power_pct: 100, w: 38, mhs: 27.1, mhw: 0.713, source: 'stock', label: 'measured', table: true, note: 'the meter' }], tier: 'max', tier_note: 'no lever: Apple silicon sets its own clocks and power', tiers_table: true }), settings(), 28);
  assert.equal(mac.kind, 'nolever'); assert.equal(mac.tiers[0].line, '27.1 MH/s · 38 W'); assert.equal(mac.tiers[0].sub, '0.713 MH/W · table');
});

// the Earnings tab's two truths of earnings-26 (the paused projection, the measured 24 hours) live in once.test.mjs (T-R19, T-A1)
test('the dev fee lives at the bottom of Settings: the rate, what it funds, the site\'s wording; Earnings carries its own row since ED-05', () => {
  // ED-05 (the founder, 11 October 2026): the fee's own row on Earnings (devFeeEarningsRow); the old clause that
  // Earnings says nothing of it ended with that row
  const ec = earningsCard();
  assert.equal(/id="e-devfee"|Dev fee/i.test(ec), true, 'the fee row on Earnings');
  assert.equal(/s-devfee/.test(src), false, 'no fee switch handler or render in the page code');
  const a = indexHtml.indexOf('id="page-settings"'), settings = indexHtml.slice(a, indexHtml.indexOf('</section>', a));
  const fee = settings.indexOf('id="s-fee-card"');
  assert.ok(fee > 0, 'the fee section exists on Settings');
  assert.equal((settings.slice(fee + 10).match(/<(div|details) class="card[ "]/g) || []).length, 0, 'the fee section is the last card on Settings');
  const sec = settings.slice(fee);
  assert.equal(/<input|<button|class="switch"/.test(sec), false, 'no control in the fee section');
  assert.match(sec, /1% dev fee/); assert.match(sec, /Igneum Labs LLC/);
  // the old site's page is not part of this base (docs/product-a/OLD-BASE.md); the sentence is checked on the site
  // when its page is in the tree, and the window's own line below in every tree
  const sitePage = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'site', 'miner.html');
  if (existsSync(sitePage)) {
    assert.ok(readFileSync(sitePage, 'utf8').includes('One block template in 100 is requested with the dev payout address instead of yours'), 'the site carries the sentence');
  }
  assert.match(V.devFeeLine({ settings: { dev_fee: true }, mining: { fee_total: 0 } }), /One block template in 100 is requested with the dev payout address instead of yours/);
  assert.match(V.devFeeLine({ settings: { dev_fee: true }, mining: { fee_total: 0 } }), /Igneum Labs LLC, the company that ships the software/);
  assert.equal(/switch|turn it off|turns it off/i.test(V.devFeeLine({ settings: { dev_fee: true }, mining: { fee_total: 0 } })), false, 'no control named');
  assert.equal(/dev.?fee/i.test(JSON.stringify(V.earningsLines(stateOf(), chainOf(), 28, T5))), false, 'the Earnings lines carry no fee');
});

// the stat strip of 15 October 2026 (the founder's "make it beautiful"; main's form): five tiles, a placeholder dash
// wherever a figure is unknown (never 0, never an empty tile), the figures and the words when they are known
test('the stat strip: placeholders before any figure, the figures when known, the chain tile by the source', () => {
  const dash = '\u2014';
  const empty = V.stripTiles({ mining: { state: 'idle', cards: [] }, source: {} }, null, 0);
  for (const k of ['hash', 'power', 'earn', 'temp', 'chain']) { assert.equal(empty[k].text, dash, k); assert.equal(empty[k].ph, true, k); assert.ok(empty[k].sub.length > 0, k + ' has a sub-line'); }
  assert.equal(empty.hash.sub, 'not mining');
  assert.equal(empty.chain.sub, 'no work source set');
  const c = card({ state: 'mining', hash_now: 48.2, power_w: 300, temp_gpu: 71, temp_mem: 80, name: 'RTX 5090' });
  const s = { mining: { state: 'mining', hash_total: 48.2, watts_total: 300, cards: [c] }, source: { kind: 'pool', connected: true, chain_id: 7320, name: 'pool.igneum.network' }, network: { name: 'chain 7320' } };
  const chain = { ok: true, network: { hashrate_hps: 1e9, miner_ign_per_block: 2, bps: 1 }, params: {} };
  const t = V.stripTiles(s, chain, 0);
  assert.deepEqual([t.hash.text, t.hash.ph], ['48.2', false]);
  assert.deepEqual([t.power.text, t.power.ph, t.power.sub], ['300', false, 'on the cards']);
  assert.equal(t.earn.ph, false); assert.ok(/IGN|\d/.test(t.earn.text), t.earn.text); assert.ok(t.earn.sub.startsWith('at 48.2 MH/s'), t.earn.sub);
  assert.deepEqual([t.temp.text, t.temp.ph, t.temp.sub, t.temp.tone], ['71', false, 'RTX 5090', '']);
  assert.equal(V.stripTiles({ mining: { state: 'mining', hash_total: 1, cards: [card({ temp_gpu: 93, temp_mem: 80 })] }, source: {} }, null, 0).temp.tone, 'hot');
  assert.deepEqual([t.chain.ph, t.chain.tone], [false, 'good']); assert.ok(t.chain.text.includes('pool connected'), t.chain.text);
  const down = V.stripTiles({ mining: { state: 'mining', hash_total: 1, cards: [c] }, source: { kind: 'pool', connected: false, problem: 'connection refused' } }, null, 0).chain;
  assert.deepEqual([down.text, down.tone, down.ph], ['pool unreachable', 'bad', false]);
  // a Mac with no power reading: the dash stays and the sub-line says so; the figure never reads 0
  const mac = V.stripTiles({ mining: { state: 'mining', hash_total: 0.98, cards: [card({ state: 'mining', hash_now: 0.98, power_w: 0, vendor: 'apple', backend: 'metal' })] }, source: { kind: 'pool', connected: true, chain_id: 7320 } }, null, 0);
  assert.equal(mac.power.text, dash); assert.equal(mac.power.ph, true);
  assert.equal(mac.hash.text, '1.0');
});
