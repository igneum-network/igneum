// node --test app/igneum-app/ui/view.test.mjs   (no dependencies; CI runs it in the site job)
// Loads the View block of app.js (plain browser JS: the file is run with `module` defined and no `document`, so only
// the pure blocks execute) and checks the words each page shows for a given state (miner-ui-2).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

const card = (over) => ({ key: 'nvidia:0:RTX 5090', name: 'NVIDIA GeForce RTX 5090', vendor: 'nvidia', kind: 'discrete', worker: 'CUDA', vram_mb: 32768, enabled: true, state: 'mining', hash_now: 124.3, hash_avg: 120, accepted: 3, rejected: 0, identities: 8, ids: [], prepared: true, restart_in_s: 0, message: '', reason: '', power_w: 410.2, power_limit_w: 460, power_default_w: 575, power_pct: 80, power_applied: true, temp_gpu: 61, temp_mem: 72, telemetry_at: 1, ...over });

test('the four sections and their order (miner-ui-3)', () => {
  assert.deepEqual(V.PAGES.map((p) => p.id), ['mine', 'earnings', 'prove', 'settings']);
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
  assert.equal(V.cardRow(card({ state: 'faulted' })).tone, 'bad');
  assert.equal(V.cardRow(card({ state: 'waiting' })).word, 'waiting for the node');
});

test('the big button: start when paused, stop when mining, disabled with no card on', () => {
  const m = (over) => ({ state: 'mining', paused: false, cards: [card()], ...over });
  assert.deepEqual(V.toggle(m(), { synced: true }, {}), { label: 'Stop mining', sub: 'mining on 1 of 1 card', cls: 'stop', disabled: false, act: 'pause' });
  assert.deepEqual(V.toggle(m({ state: 'paused', paused: true }), { synced: true }, {}), { label: 'Start mining', sub: 'paused · the node keeps running', cls: '', disabled: false, act: 'resume' });
  const none = V.toggle(m({ cards: [card({ enabled: false })] }), { synced: true }, {});
  assert.equal(none.disabled, true);
  assert.equal(none.act, '');
  assert.equal(none.sub, 'switch a GPU on below first');
  assert.equal(V.toggle(m({ cards: [] }), { synced: true }, {}).sub, 'no GPU this app can drive');
  assert.equal(V.toggle(m({ state: 'waiting' }), { synced: false }, {}).sub, 'waiting for the node to sync');
  assert.equal(V.toggle(m(), { synced: true }, { quitting: true }).disabled, true);
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
  assert.equal(V.switchLine(next, 140000), 'Fees v1 at DAA 210,000: 70,000 blocks away, about 19 h 27 min at one block a second.');
  assert.equal(V.nextSwitch(sw, 20000).name, 'Difficulty v2');
  assert.equal(V.nextSwitch(sw, 300000), null);
  assert.match(V.switchLine(null, 300000), /Every planned switch is behind this node/);
  assert.match(V.switchLine(null, 0), /^A switch is a planned rule change/);
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
  assert.equal(V.devFeeLine(s(true, 12)), 'One block in 100 pays the miner software’s dev fee (12 so far). The switch above turns it off.');
  assert.equal(V.devFeeLine(s(false, 0)), 'The dev fee is off. Every block pays this address.');
  assert.equal(V.devFeeText(s(true, 0)), 'Dev fee 1% (1 block in 100) to 0x123456…5678');
  assert.match(V.devFeeText(s(false, 0)), /^Dev fee off/);
});

test('the remote-jobs line and the helpers', () => {
  const title = (j) => j.title || j.kind;
  assert.equal(V.jobsNote({ allowed: false }, 1000, title), 'Off: nothing runs here until the switch above allows remote jobs.');
  assert.equal(V.jobsNote({ allowed: true, url_set: false }, 1000, title), 'No jobs address in this build.');
  assert.equal(V.jobsNote({ allowed: true, url_set: true, active: true, id: 'job-3', kind: 'build', title: 'build' }, 1000, title), 'Running build (job-3).');
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
  assert.match(r.sub, /^unplugged; its worker stopped/);
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
  // performance order (6 October 2026): the desk machine with two enclosures detects 5090, integrated AMD, 9070 XT; the list shows the 5090, the 9070 XT,
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
  assert.equal(t.sub, 'no GPU this app can drive');
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
  assert.match(prior.text, /^Tuned 2 min ago from the fleet prior, confirmed · 2470 MHz at 100%/);
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
  assert.equal(V.schedule([nv()], settings({ sweep: false }), NOW), 'Ember Tune is off. Tune and Tune all still work by hand.');
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
  assert.equal(V.proveTier(card({ name: 'NVIDIA GeForce RTX 4070', vram_mb: 12288 })), 'NVIDIA GeForce RTX 4070: cannot prove a full shard (12 GB; a full shard needs 20.4 GB).');
  assert.equal(V.proveTier(card({ name: 'Apple M5 Max', vendor: 'apple' })), 'Apple M5 Max: proves on the CPU, slowly.');
  assert.equal(V.proveTier(card({ name: 'AMD Radeon RX 9070 XT', vendor: 'amd', vram_mb: 16384 })), 'AMD Radeon RX 9070 XT: cannot prove yet (the prover is CUDA only).');
  assert.equal(V.proveLine({ assigned: 0, submitted: 0, paid: 0, paid_wei: '0' }, false, true), 'Off · not proving · 0 assigned · 0 proven · 0 paid · 0.0000 IGN');
  assert.equal(V.proveLine({ status: 'idle', assigned: 3, submitted: 2, paid: 1, paid_wei: '1500000000000000000', available: true, enabled: true }, true, true), 'Idle · nothing assigned · 3 assigned · 2 proven · 1 paid · 1.5000 IGN');
});

test('the node line, the activity line, the asks', () => {
  const n = { state: 'synced', blocks: 109859, headers: 109859, peers: 6, daa: 198000, last_reading_age_s: 5, message: '' };
  const l = V.nodeLine(n, { severity: 'none' }, '');
  assert.equal(l.text, 'Node synced · 6 peers · 109,859 blocks'); assert.equal(l.tone, 'ok'); assert.match(l.sub, /every block/);
  assert.equal(V.nodeLine({ ...n, peers: 1, blocks: 0, state: 'starting' }, { severity: 'none' }, '').text, 'Node starting · 1 peer');
  assert.equal(V.nodeLine(n, { severity: 'block', skew_s: -412 }, '').tone, 'bad');
  assert.equal(V.eventLine('consensus parameters from the signed manifest: {"difficulty_v2_activation_daa":33000}'), 'consensus parameters from the signed manifest (details in the log)');
  assert.equal(V.eventLine('node synced: 151512 blocks, 6 peer(s)'), 'node synced: 151512 blocks, 6 peer(s)');
  assert.equal(V.eventLine('a '.repeat(100), 40).length <= 41, true);
  assert.equal(V.addressAsk('0x1234567890abcdef1234567890abcdef12345678'), 'Pay 0x1234…5678 from the next block? The miner restarts.');
  assert.match(V.trustAsk(true), /^Include proof records the node never checked\? Devnet only; the node restarts\.$/);
  assert.match(V.trustAsk(false), /^Verify proof records again\?/);
  assert.equal(V.ago(30), 'just now'); assert.equal(V.ago(7200), '2 h ago'); assert.equal(V.ago(200000), '2 d ago');
});
