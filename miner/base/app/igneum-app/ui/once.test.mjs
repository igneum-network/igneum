// Nothing twice (app-ia-26, the founder's audit of 8 October 2026, docs/design/app-audit-2026-10-08.md section 5.4): the
// static markup of index.html, read as text and counted. `once(html, needle)` counts a string in the whole page;
// `inPage(html, pageId, needle)` counts inside one <section class="page" id="page-…">. One assertion per repeat in the
// audit's register (T-R1 to T-R21) and per readout that misled (T-A1 to T-A20), each written known-failed first.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, 'index.html'), 'utf8');
const src = readFileSync(join(here, 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

export function once(text, needle) { let n = 0, i = -1; while ((i = text.indexOf(needle, i + 1)) >= 0) n++; return n; }
export function section(text, pageId) {
  const open = text.indexOf('id="page-' + pageId + '"'); if (open < 0) return '';
  const start = text.lastIndexOf('<section', open);
  let depth = 0, i = start;
  const tag = /<\/?section\b/g; tag.lastIndex = start;
  for (let m; (m = tag.exec(text));) { if (m[0] === '<section') depth++; else depth--; if (depth === 0) return text.slice(start, m.index + 10); }
  return text.slice(start);
}
export function inPage(text, pageId, needle) { return once(section(text, pageId), needle); }

test('the counters count', () => {
  assert.equal(once('a b a', 'a'), 2); assert.equal(once('', 'a'), 0);
  assert.equal(inPage('<section class="page" id="page-x">one two one</section><section class="page" id="page-y">one</section>', 'x', 'one'), 2);
  assert.equal(inPage(html, 'settings', 'id="page-settings"'), 1);
});

// item 1: the four pages
test('T-R15 (as the founder amended it for v0.2.1, the Ledger page added for the Live Work Ledger): six pages, mine, cards, tune, earnings, ledger, settings; Prove is a Settings section, not a page', () => {
  assert.deepEqual(V.PAGES.map((p) => p.id), ['mine', 'cards', 'tune', 'fleet', 'earnings', 'pools', 'settings']); // the Pools page (DESIGN 2.7, 13 October 2026); the Fleet page (0.2.8, docs/fleet/DESIGN.md 3.6: in the rail only with a console set)
  assert.equal(V.page('nonsense').id, 'mine');
  assert.equal(once(html, 'id="page-prove"'), 0);
  assert.equal(once(html, 'id="s-prove"'), 1); assert.equal(inPage(html, 'settings', 'id="s-prove"'), 1);
  assert.equal(once(html, 'data-page="prove"'), 0);
  for (const id of ['mine', 'cards', 'tune', 'fleet', 'earnings', 'pools', 'settings']) { assert.equal(once(html, 'id="page-' + id + '"'), 1, id); assert.equal(once(html, '<button class="nav' + (id === 'mine' ? ' on' : '') + '" data-page="' + id + '"'), 1, 'rail ' + id); }
  assert.equal(once(html, 'id="page-overview"'), 0);
  assert.equal((html.match(/<button class="nav[^"]*" data-page="/g) || []).length, 7, 'seven rail entries: the Pools page (DESIGN 2.7, 13 October 2026) and the Fleet page (0.2.8), the latter hidden until a console is set');
  assert.match(html, /<button class="nav" data-page="fleet" id="nav-fleet" hidden>/, 'the Fleet entry starts hidden');
  assert.equal(V.page('mine').sub, 'What this machine is doing now'); assert.equal(V.page('tune').sub, 'How Ember drives each card');
  assert.equal(V.page('earnings').sub, 'What this machine earned'); assert.equal(V.page('settings').sub, 'Set once, left alone');
  assert.equal(/renderProve\b/.test(src), false, 'renderProve is renderProvingSection under Settings');
  assert.equal(/'overview'|"overview"|page-overview/.test(src), false, 'no overview page id left in the page code');
});
test('T-R8: the pill is a state word, the rate lives on Mine', () => {
  const base = { setup_done: true, mining: { state: 'mining', hash_total: 511, cards: [] }, node: { state: 'ok' }, source: { kind: 'node', url: 'http://127.0.0.1:26610' } };
  assert.deepEqual(V.pill(base), { text: 'Mining', tone: 'on' });
  assert.equal(/MH\/s/.test(V.pill({ ...base, mining: { state: 'mining', hash_total: 17.04, cards: [] } }).text), false);
  assert.deepEqual(V.pill({ ...base, mining: { state: 'paused', hash_total: 0, cards: [] } }), { text: 'Paused', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'syncing' } }), { text: 'Syncing', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'starting' } }), { text: 'Syncing', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: 'ok' } }), { text: 'Waiting', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'held', hash_total: 0, cards: [] }, jobs: { active: true } }), { text: 'Job running', tone: '' });
  assert.deepEqual(V.pill({ ...base, mining: { state: 'mining', hash_total: 100, cards: [] }, jobs: { active: true } }), { text: 'Job running', tone: '' });
  for (const st of ['restarting', 'behind', 'no peers', 'failed', 'stopped', 'stub']) assert.deepEqual(V.pill({ ...base, mining: { state: 'waiting', hash_total: 0, cards: [] }, node: { state: st } }), { text: 'Node unreachable', tone: 'bad' }, st);
  assert.deepEqual(V.pill({ ...base, mining: { state: 'mining', hash_total: 1, cards: [{ state: 'tuning' }] } }), { text: 'Tuning', tone: 'on' });
  assert.deepEqual(V.pill({ ...base, heat: { on: true, phase: 'resting' }, mining: { state: 'paused', hash_total: 0, cards: [] } }), { text: 'Resting · heat mode', tone: '' }, 'a rest names heat mode, never a bare word (heat-region.test.mjs)');
  assert.deepEqual(V.pill({ ...base, mining: { state: 'idle', hash_total: 0, cards: [] } }), { text: 'Not mining', tone: '' });
  assert.deepEqual(V.pill({ ...base, setup_done: false, detecting: true }), { text: 'Detecting cards', tone: '' });
  assert.deepEqual(V.pill({ ...base, quitting: true }), { text: 'Stopping', tone: '' });
  // an earlier release (the second Windows machine, 21:24 UK): the quit's stage stands on the pill and the big button while the node is being stopped
  assert.deepEqual(V.pill({ ...base, quitting: true, quit_stage: 'quitting: the node is being stopped' }), { text: 'Stopping · the node is being stopped', tone: '' });
  assert.equal(V.toggle({ state: 'mining', cards: [] }, { state: 'syncing' }, { quitting: true, quit_stage: 'quitting: the node is being stopped' }).sub, 'quitting: the node is being stopped');
  assert.equal(V.toggle({ state: 'mining', cards: [] }, { state: 'syncing' }, { quitting: true }).sub, 'the miners stop, then the app closes');
});
// the toggle's sub-line (T-R8's second half) goes with item 3, Mine


// item 4: Tune. Fixtures: a measured 5090 with its three tiers and a lock, the fleet of two
const cardFx = (over) => ({ key: 'nvidia:0:RTX 5090', name: 'NVIDIA GeForce RTX 5090', vendor: 'nvidia', kind: 'discrete', worker: 'CUDA', vram_mb: 32768, enabled: true, state: 'mining', hash_now: 126.8, hash_avg: 126.5, power_w: 372, power_default_w: 450, power_pct: 90, power_applied: true, temp_gpu: 59, temp_mem: 68, identities: 8, sweep_supported: true, sweep_state: 'idle', sweep_note: '', sweep_at: 1_800_000_000 - 3600, sweep_pct: 90, sweep_watts: 372, sweep_mhs: 126.8, tune_line: 'Tuned: 126.8 MH/s at 372 W (0.341 MH/W)', tune_source: 'full', tune_control: true, tune_clock_mhz: 1950, lock_at: 1_800_000_000 - 3600, lock_mhz: 1950, lock_w: 372, lock_mhs: 126.8, lock_mhw: 0.341, lock_note: 'rate fell 2.3 percent at 1200 MHz', unlocked_w: 405, unlocked_mhs: 128.1, gclk_mhz: 1950, mclk_mhz: 13801, fan_pct: 62, detail: '21760 cores', driver_os: '617.42', tiers: [{ id: 'efficiency', clock_mhz: 1200, power_pct: 80, w: 305, mhs: 121.9, mhw: 0.4, source: 'measured' }, { id: 'balanced', clock_mhz: 1950, power_pct: 90, w: 372, mhs: 126.8, mhw: 0.341, source: 'measured' }, { id: 'max', clock_mhz: 0, power_pct: 100, w: 450, mhs: 128.4, mhw: 0.285, source: 'stock' }], tier: 'balanced', tier_note: '', tier_at: 1_800_000_000 - 3600, tune_curve: [{ watts: 450, mhs: 128.4, eff: 0.285, clock_mhz: 0 }, { watts: 405, mhs: 128.1, eff: 0.316, clock_mhz: 2400 }, { watts: 372, mhs: 126.8, eff: 0.341, clock_mhz: 1950 }, { watts: 305, mhs: 121.9, eff: 0.4, clock_mhz: 1200 }], ...over });
const NOW = 1_800_000_000;
const stateFx = (over) => ({ now: NOW, settings: { sweep: true, tuning_off: false, tune_period_s: 604800, power_control: true, tune_tier: 'balanced', power_price_pence: 28 }, mining: { state: 'mining', cards: [cardFx()] }, heat: { on: false }, ...over });

test('T-R1: the tier buttons live on Tune alone: the fleet strip and one row per card', () => {
  assert.equal(once(html, 'aria-label="Tuning tier"'), 1); assert.equal(inPage(html, 'tune', 'aria-label="Tuning tier"'), 1);
  assert.equal(inPage(html, 'settings', 'tier-seg'), 0); assert.equal(once(src, 'tier-seg-2'), 0); assert.equal(once(src, 'goal-seg'), 0);
  assert.equal(inPage(html, 'mine', 'tier-seg'), 0);
});
test('T-R2: the Ember sentence once, on the Tune strip; the measure switch with it', () => {
  assert.equal(once(html, 'id="tune-schedule"'), 1); assert.equal(inPage(html, 'tune', 'id="tune-schedule"'), 1); assert.equal(once(html, 's-tune-schedule'), 0);
  assert.equal(once(html, 'id="s-sweep"'), 1); assert.equal(inPage(html, 'tune', 'id="s-sweep"'), 1);
  assert.equal(inPage(html, 'tune', 'Measure cards by itself'), 1);
});
test('T-R3: one Power control switch, in Settings; Tune says one sentence when it matters', () => {
  assert.equal(once(html, 'id="s-power-control"'), 1); assert.equal(inPage(html, 'settings', 'id="s-power-control"'), 1); assert.equal(once(html, 'm-power-control'), 0);
  assert.equal(V.powerNote(stateFx({ settings: { ...stateFx().settings, power_control: false } })), 'Power control is off, so NVIDIA cards are measured and never set. Turn it on in Settings.');
  assert.equal(V.powerNote(stateFx()), '');
  assert.equal(V.powerNote(stateFx({ settings: { ...stateFx().settings, power_control: false }, mining: { state: 'mining', cards: [cardFx({ vendor: 'amd' })] } })), '', 'no NVIDIA card, no sentence');
});
test('T-R4: the electricity price is a figure in Settings > Electricity only', () => {
  for (const p of ['mine', 'tune', 'earnings']) assert.equal(inPage(html, p, 'per kWh'), 0, p);
  assert.equal(V.priceFigure, undefined); assert.equal(once(src, 'priceFigure'), 0);
});
test('T-R5: the knee rule once, at the foot of Tune, in the markup', () => {
  assert.equal(once(html, 'Ember never locks below the knee by itself. Going lower is your choice.'), 1); assert.equal(inPage(html, 'tune', 'Ember never locks below the knee'), 1);
  assert.equal(once(src, 'TIER_RULE'), 0); assert.equal(once(src, 'LOCK_RULE'), 0);
});
test('T-R6 and T-A10: one lock line per card, MHz and watts only, one baseline', () => {
  const l = V.tuneLine(cardFx(), stateFx(), NOW);
  assert.equal(l, 'Locked at 1,950 MHz, cap 405 W. Knee at 1,200 MHz.');
  assert.equal(/cap (\d+) W/.exec(l)[1], /^Cap (\d+) W/.exec(V.capSentence(cardFx()))[1], 'the lock line and the cap sentence carry the same cap figure');
  assert.equal(/Tuned:|MH\/s|of rate|saves/.test(l), false);
  assert.equal(V.tuneLine(cardFx({ lock_mhz: 0, lock_note: 'rate fell 2.3 percent at 2400 MHz' }), stateFx(), NOW), 'Clock unlocked, cap 405 W. Knee at 2,400 MHz.');
  assert.equal(V.tuneLine(cardFx({ vendor: 'amd', power_default_w: 0 }), stateFx(), NOW), 'Locked at 1,950 MHz. Knee at 1,200 MHz.', 'no cap slider, no cap figure');
  // F-E18 (14 October 2026): a cap the driver never read back is never read as set; the row carries the request and the reason
  assert.equal(V.tuneLine(cardFx({ power_applied: false, power_note: 'power cap NOT applied (needs the administrator prompt): card reports 450 W, wanted 405 W' }), stateFx(), NOW),
    'Locked at 1,950 MHz, cap 405 W requested, not applied: needs the administrator prompt; card reports 450 W, wanted 405 W. Knee at 1,200 MHz.');
  assert.equal(V.tuneLine(cardFx({ power_applied: false, power_note: 'power cap not set: Power control is off in Settings' }), stateFx(), NOW),
    'Locked at 1,950 MHz, cap 405 W requested, not applied: Power control is off in Settings. Knee at 1,200 MHz.');
  assert.equal(V.capWords(cardFx({ power_applied: false, power_note: '' })), ', cap 405 W requested, not applied: the driver has not read it back');
  assert.equal(V.capWords(cardFx({ power_applied: true })), ', cap 405 W');
  // 0.2.8, the Fleet item (docs/fleet/DESIGN.md 3.6): the rail's Fleet entry follows a console URL; the token is never read back
  assert.deepEqual(V.fleetWords({ settings: {} }).on, false);
  assert.equal(V.fleetWords({ settings: {} }).eyebrow, 'off');
  const fw = V.fleetWords({ settings: { fleet_console_url: 'https://build.igneum.network/_fleet/abc/', fleet_token_set: true } });
  assert.equal(fw.on, true); assert.equal(fw.eyebrow, 'set, a token held'); assert.match(fw.note, /Fleet is in the rail/);
  assert.equal(V.fleetWords({ settings: { fleet_console_url: 'https://h/_fleet/t/', fleet_token_set: false } }).eyebrow, 'set, no token');
  assert.equal(V.PAGES.some((p) => p.id === 'fleet'), true, 'the Fleet page sits in the rail list, shown only with a console set');
  assert.equal(V.PAGES.findIndex((p) => p.id === 'fleet'), V.PAGES.findIndex((p) => p.id === 'tune') + 1, 'Fleet sits between Tune and Earnings');
  // F-E39: the feed's concentration field (a fraction with a status) beside the older percentage
  assert.equal(V.poolSharePct({ share_of_work_7d: 0.6723, status: 'partial window' }), 67.23);
  assert.equal(V.poolSharePct({ share_7d_pct: 12.5 }), 12.5);
  assert.equal(V.poolSharePct({}), null);
  const partial = V.poolShareTile({ concentration: { share_of_work_7d: 0.6723, status: 'partial window', blocks_read: 1200, window_blocks: 30240 } });
  assert.equal(partial.v, '67.2%'); assert.match(partial.k, /partial window \(1,200 of 30,240 blocks read\)/); assert.equal(partial.warn, true); assert.equal(partial.dim, true);
  const pub = V.poolShareTile({ concentration: { share_of_work_7d: 0.21, status: 'published' } });
  assert.equal(pub.v, '21.0%'); assert.equal(pub.warn, false); assert.equal(pub.dim, false);
  assert.equal(V.poolShareTile({ concentration: { status: 'not published', measure: 'the carried shares over 30,240 blocks' } }).v, 'not published');
  assert.equal(V.tuneLine(cardFx({ state: 'tuning', sweep_state: 'running', tune_step: 3, tune_steps: 9, tune_eta_s: 230 }), stateFx(), NOW), 'Measuring: step 3 of 9, 4 min left.');
  assert.equal(V.tuneLine(cardFx({ vendor: 'apple', kind: 'apple', tiers: [], tier: '', tier_note: 'no lever: Apple silicon sets its own clocks and power', lock_note: 'no lever', lock_at: 0, lock_mhz: 0 }), stateFx(), NOW), 'No lever on Apple silicon: the system sets the clocks. Ember measures and reports.');
  assert.equal(V.tuneLine(cardFx({ tiers: cardFx().tiers.map((t) => ({ ...t, table: true, label: 'measured' })), tiers_table: true, lock_at: 0, lock_mhz: 0, tune_line: '', sweep_at: 0 }), stateFx(), NOW), 'From the team’s table, measured on this card class.');
  assert.equal(V.tuneLine(cardFx({ tiers_class: 'v4', program_class: 'v5', tiers_remeasure_at: NOW - 120 }), stateFx(), NOW), 'Re-measuring for class v5, started ' + V.hhmm(NOW - 120) + '.');
  assert.equal(V.tuneLine(cardFx({ tiers: [], lock_at: 0, lock_mhz: 0, tune_line: '', sweep_at: 0 }), stateFx(), NOW), 'Not measured yet: the search runs 2 min into steady mining.');
  assert.equal(V.tuneLine(cardFx(), stateFx({ settings: { ...stateFx().settings, tuning_off: true, tuning_note: 'Tuning paused fleet-wide by the signed manifest' } }), NOW), 'Tuning paused fleet-wide by the signed manifest.');
  assert.equal((l.match(/under stock/g) || []).length <= 1, true);
});
test('T-R7: no fleet saving line; the button carries the saving', () => {
  assert.equal(V.fleetSaving, undefined); assert.equal(once(html, 'fleet-saving'), 0); assert.equal(once(src, 'fleetSaving'), 0);
});
test('T-A7: a tier button reads "N W under stock", no "saves", no currency', () => {
  const t = V.tierSet(cardFx(), stateFx(), 28);
  assert.equal(t.tiers[0].sub, '0.400 MH/W · 145 W under stock'); assert.equal(t.tiers[1].sub, '0.341 MH/W · 78 W under stock'); assert.equal(t.tiers[2].sub, '0.285 MH/W · stock');
  for (const x of t.tiers) assert.equal(/saves|£|\$|€/.test(x.sub + x.line), false);
  const f = V.fleetTiers([cardFx(), cardFx({ key: 'b' })], stateFx(), 28);
  assert.equal(f.tiers[0].sub, '290 W under stock'); assert.equal(f.tiers[2].sub, 'stock');
});
test('T-A9: the strip sentence for two measured cards is one sentence with one "ago"', () => {
  const s = V.tierSentence([cardFx(), cardFx({ key: 'b', sweep_at: NOW - 7200 })], stateFx(), NOW);
  assert.match(s, /^Balanced on 2 cards\. Measured 1 h ago, next check \d+ \w+\.$/);
  assert.equal((s.match(/ago/g) || []).length, 1);
});
test('T-A11: the cap sentence: one figure for the cap, one for the draw', () => {
  assert.equal(V.capSentence(cardFx()), 'Cap 405 W (90%). The card draws 372 W at the lock.');
  assert.equal(V.capSentence(cardFx({ lock_mhz: 0, lock_at: 0, power_w: 380 })), 'Cap 405 W (90%). The card draws 380 W.');
  assert.equal(V.capSentence(cardFx({ power_applied: false })), 'Cap 405 W (90%), not set yet: Windows needs the administrator prompt.');
});
test('T-R16 and T-R17: the Tune card carries one Tune button and the MH/W once in its head; the row word stays on Mine', () => {
  const h = V.tuneCardHtml(cardFx(), stateFx(), NOW);
  assert.equal((h.match(/data-tune=|data-stop=/g) || []).length, 1);
  const head = h.slice(0, h.indexOf('class="tc-tiers"'));
  assert.equal((head.match(/MH\/W/g) || []).length, 1); assert.equal((head.match(/MH\/s/g) || []).length, 1);
  assert.equal(once(h, 'Retune'), 1);
  assert.equal(once(h, 'Locked at 1,950 MHz, cap 405 W. Knee at 1,200 MHz.'), 1);
  assert.equal(once(h, '<details'), 1, 'one closed Details disclosure'); assert.equal(once(h, '<details open'), 0);
  assert.equal(once(h, 'Cap 405 W (90%). The card draws 372 W at the lock. Lower is cooler and quieter.'), 1);
  assert.equal(once(h, 'Each one votes and is paid on its own. 8 for a big card, 2 for a small one, 1 for a built-in\u00a0GPU.'), 1, 'the last two words glued');
  assert.equal(once(h, 'memory 68 °C · core 1,950 MHz · memory clock 13,801 MHz · fan 62%'), 1);
  assert.equal(once(h, 'runs on CUDA · 21,760 cores · driver 617.42'), 1);
  assert.equal(once(h, 'The ringed point is the one Ember kept; a hollow point was rejected; the dashed point is the knee.'), 1);
  assert.equal(/Ember never locks|saves \d|£|a day|IGN/.test(h), false, 'no rule, no money, no IGN on the Tune card');
  const m = V.tuneCardHtml(cardFx({ state: 'tuning', sweep_state: 'running', tune_step: 3, tune_steps: 9, tune_eta_s: 230 }), stateFx(), NOW);
  assert.equal(once(m, 'data-stop='), 1); assert.equal(once(m, 'data-tune='), 0);
});
test('T-R20: the heat line once, on Tune, only while heat mode is on', () => {
  assert.equal(once(html, 'id="heat-line"'), 1); assert.equal(inPage(html, 'tune', 'id="heat-line"'), 1);
  assert.equal(V.heatLine({ on: false }).text, '');
});

// item 5: Earnings. The windows from state.ladder.earned (item 2), the Electricity row, the Payouts line, the Standing
// row with the rungs behind Details, the projection as one dim foot line that hides with its reason
const earnedFx = { h24: { wei: '14640000000000000000', ign: 14.64, blocks: 3, shards: 0, priced: true }, d7: { wei: '89200000000000000000', ign: 89.2, blocks: 18, shards: 1, priced: true }, all: { wei: '393070000000000000000', ign: 393.07, blocks: 80, shards: 6, priced: true }, estimated: false };
const chainFx = (over) => ({ ok: true, source: 'node', params: { dust: 5, weight_window: 7200 }, network: { hashrate_hps: 100e9, miner_ign_per_block: 4.88, ign_per_block: 6.1, ramp_factor: 0.8, daa: 1, blocks_per_day_measured: 86400, bps: 1, stale: false }, keys_listed: 4, keys_cap: 0, voters: 14, mine: [], best: { id: '8fafda27', blocks: 251, voter: true, participation: 1, rank: 3 }, ...over });
const earnState = (over) => ({ now: NOW, uptime_s: 1800, setup_done: true, address: { display: '0xdd442fCbb964A3aFDc90D49B408e8DD296FA86E8', source: 'generated' }, settings: { prove: false, dev_fee: true, power_price_pence: 28 }, node: { synced: true, state: 'synced', tip_age_s: 2 }, proving: { paid: 0, paid_wei: 0 }, jobs: { active: false }, mining: { state: 'mining', hash_total: 17, watts_total: 622, accepted_total: 80, accepted_session: 12, found: [], cards: [cardFx({ hash_now: 17, power_w: 622 })] }, ladder: { first_block_at: NOW - 86000, blocks_30d: 251, blocks_today: 2, blocks_24h: 3, days: [], blocks: [], streak_s: 3600, last_signed_locked: 8490, signed_locked_count: 40, shards: [], earned: earnedFx, first_synced_at: NOW - 86400, first_mining_at: NOW - 86400 }, ...over });

test('T-A1 and T-A2: the three windows read the engine\'s earned sums; no "day", no "this run", no reward applied to old blocks', () => {
  const e = V.earningsLines(earnState(), chainFx(), 28, NOW);
  assert.equal('day' in e, false); assert.equal('run' in e, false); assert.equal('lifetime' in e, false); assert.equal('weight' in e, false);
  assert.deepEqual([e.h24.ign, e.h24.blocks, e.h24.shards], [14.64, 3, 0]);
  assert.deepEqual([e.d7.ign, e.d7.blocks, e.d7.shards], [89.2, 18, 1]);
  assert.deepEqual([e.all.ign, e.all.blocks, e.all.shards], [393.07, 80, 6]);
  assert.equal(e.h24.text, '14.64 IGN'); assert.equal(e.h24.sub, '3 blocks');
  assert.equal(e.d7.text, '89.20 IGN'); assert.equal(e.d7.sub, '18 blocks · 1 shard');
  assert.equal(e.all.text, '393.1 IGN'); assert.equal(e.all.sub, '80 blocks · 6 shards');
  assert.equal(e.headline.text, '14.64 IGN'); assert.equal(e.headline.eyebrow, 'last 24 hours'); assert.equal(e.headline.sub, '3 blocks · development-network tokens; no monetary value');
  assert.equal(/this run|at today|a day/.test(JSON.stringify([e.headline, e.h24, e.d7, e.all])), false);
  // the sums come from the engine, never count × today's reward: a different reward changes nothing
  assert.equal(V.earningsLines(earnState(), chainFx({ network: { ...chainFx().network, miner_ign_per_block: 99 } }), 28, NOW).h24.text, '14.64 IGN');
  // before the first block
  const z = V.earningsLines(earnState({ ladder: { ...earnState().ladder, earned: { ...earnedFx, h24: { wei: '0', ign: 0, blocks: 0, shards: 0, priced: true }, d7: { wei: '0', ign: 0, blocks: 0, shards: 0, priced: true }, all: { wei: '0', ign: 0, blocks: 0, shards: 0, priced: true } } } }), chainFx(), 28, NOW);
  assert.equal(z.headline.text, '0 IGN'); assert.equal(z.headline.sub, 'no block yet · development-network tokens; no monetary value'); assert.equal(z.h24.sub, 'no block yet');
  // an engine before the windows: the IGN reads "reading", the windows show blocks only, counted from the ladder's own
  // record and never zero (blocks_24h, else the block list filtered to the day; the day ring for 7 days; the lifetime count)
  const old = V.earningsLines(earnState({ ladder: { ...earnState().ladder, earned: undefined } }), chainFx(), 28, NOW);
  assert.equal(old.headline.text, 'reading'); assert.equal(old.h24.text, 'reading'); assert.equal(old.h24.sub, '3 blocks'); assert.equal(old.all.sub, '80 blocks');
  const dayOf = Math.floor(NOW / 86400);
  const older = V.earningsLines(earnState({ ladder: { ...earnState().ladder, earned: undefined, blocks_24h: undefined, blocks: [{ at: NOW - 100 }, { at: NOW - 5000 }, { at: NOW - 90000 }], days: [{ day: dayOf, blocks: 2 }, { day: dayOf - 1, blocks: 5 }, { day: dayOf - 6, blocks: 4 }, { day: dayOf - 9, blocks: 7 }] } }), chainFx(), 28, NOW);
  assert.equal(older.headline.sub, '2 blocks · development-network tokens; no monetary value', 'blocks_24h absent: the block list filtered to the day, never zero');
  assert.equal(older.h24.sub, '2 blocks'); assert.equal(older.d7.sub, '11 blocks'); assert.equal(older.d7.text, 'reading'); assert.equal(older.all.sub, '80 blocks');
  // the page shows the 24 h figure once, as the headline; the row is 7 days and all time
  assert.equal(once(html, 'id="e-h24"'), 0); assert.equal(inPage(html, 'earnings', 'id="e-d7"'), 1); assert.equal(inPage(html, 'earnings', 'id="e-all"'), 1); assert.equal(inPage(html, 'earnings', 'id="e-head"'), 1);
  assert.equal((section(html, 'earnings').match(/class="earn-cell"/g) || []).length, 2, 'two columns under the headline');
  // an estimated all-time figure says so; an unpriced window says so
  assert.equal(V.earningsLines(earnState({ ladder: { ...earnState().ladder, earned: { ...earnedFx, estimated: true } } }), chainFx(), 28, NOW).all.sub, '80 blocks · 6 shards · estimated');
  assert.equal(V.earningsLines(earnState({ ladder: { ...earnState().ladder, earned: { ...earnedFx, h24: { ...earnedFx.h24, priced: false } } } }), chainFx(), 28, NOW).h24.sub, '3 blocks · one block not priced yet');
});
test('a pool session counts the shares the pool accepted, never blocks', () => {
  // the first real captures (12 October 2026): "1,630 blocks" on the mini's Earnings page under the pool
  const p = V.earningsLines(earnState({ settings: { ...earnState().settings, source_kind: 'pool', source_url: 'pool.igneum.network:3341 notls' }, node: { state: 'synced', message: 'no node runs inside this build; the chain is read through http://127.0.0.1:26610' }, ladder: { ...earnState().ladder, earned: null } }), chainFx(), 28, NOW);
  assert.equal(p.all.sub, '80 shares the pool accepted'); assert.equal(/block/.test(p.all.sub + p.d7.sub + p.h24.sub), false);
  assert.equal(p.h24.sub, 'the window is not kept on a pool session; the pool keeps the count', 'the 24 h window never says "no share yet" beside an all-time count');
  assert.equal(p.d7.sub, p.h24.sub);
  const d = V.earningsLines(earnState({ settings: { ...earnState().settings, source_kind: '' }, node: { state: 'synced', message: 'no node runs inside this build; the chain is read through http://127.0.0.1:26610' }, ladder: { ...earnState().ladder, earned: null } }), chainFx(), 28, NOW);
  assert.equal(d.all.sub, '80 shares the pool accepted', 'nothing set on a build with no node inside is the pool, the default');
  assert.equal(V.earningsLines(earnState(), chainFx(), 28, NOW).all.sub.indexOf('block') >= 0, true, 'a node session keeps its blocks');
});
test('T-A3 and A17: one Standing row; the rungs behind Details with no intro', () => {
  const e = V.earningsLines(earnState(), chainFx(), 28, NOW);
  assert.equal(e.standing.text, 'Rank 3 of 4 keys · signing 8 of 80 points');
  assert.equal(e.standing.sub, 'weight is your blocks over the last 2 hours, 2 of 2 hours mined');
  assert.equal(V.earningsLines(earnState({ ladder: { ...earnState().ladder, streak_s: 0 } }), chainFx(), 28, NOW).standing.text, 'Rank 3 of 4 keys · no signing points yet');
  assert.equal(V.earningsLines(earnState(), chainFx({ best: null, mine: [] }), 28, NOW).standing.text, 'Not in the weight table yet');
  assert.equal(V.earningsLines(earnState(), { ok: false, error: 'neither answered' }, 28, NOW).standing.text, 'Standing: not read');
  assert.equal(once(html, 'id="rungs"'), 1); assert.equal(inPage(html, 'earnings', 'id="rungs"'), 1);
  const sec = section(html, 'earnings'), d = sec.indexOf('id="standing-details"');
  assert.ok(d > 0 && sec.indexOf('id="rungs"') > d, 'the rungs sit behind the Standing details');
  assert.equal(inPage(html, 'earnings', 'No points server'), 0, 'no ladder intro'); assert.equal(once(html, 'id="ladder-card"'), 0);
  assert.equal(once(html, 'timeline-card'), 0, 'no first-hour timeline (A18)'); assert.equal(once(html, 'id="tl-toggle"'), 0);
});
test('T-R9, T-R11, T-R18 and A4: Electricity once, Payouts once, no lifetime cell, no run line, the address in Settings', () => {
  const e = V.earningsLines(earnState(), chainFx(), 28, NOW);
  assert.equal(e.cost.text, '£4.18 a day'); assert.equal(e.cost.sub, 'at 28p/kWh for 622 W');
  const noPrice = V.earningsLines(earnState(), chainFx(), 0, NOW);
  assert.equal(noPrice.cost.text, '622 W'); assert.equal(noPrice.cost.sub, 'Set a price in Settings for the cost.');
  const mac = V.earningsLines(earnState({ mining: { ...earnState().mining, watts_total: 0, cards: [cardFx({ vendor: 'apple', kind: 'apple', power_w: 0, temp_gpu: 0 })] } }), chainFx(), 28, NOW);
  assert.equal(mac.cost.text, 'No power reading on Apple silicon.'); assert.equal(mac.cost.sub, '');
  assert.equal(e.payouts.text, 'Pays 0xdd44…86E8'); assert.equal(e.payouts.sub, 'Your balance is in the wallet. Change the address in Settings.');
  assert.equal(V.earningsLines(earnState({ address: {} }), chainFx(), 28, NOW).payouts.text, 'No address yet');
  assert.equal(once(html, 'e-run'), 0); assert.equal(once(html, 'id="e-blocks"'), 0); assert.equal(once(html, 'id="e-rung"'), 0);
  assert.equal(once(html, 'id="r-address"'), 1); assert.equal(inPage(html, 'settings', 'id="r-address"'), 1);
  assert.equal(inPage(html, 'earnings', 's-address-input'), 0); assert.equal(inPage(html, 'earnings', 'key-toggle'), 0);
  assert.equal(inPage(html, 'earnings', 'id="e-cost"'), 1); assert.equal(inPage(html, 'earnings', 'id="e-pays"'), 1);
  assert.equal(inPage(html, 'settings', '<h3>Wallet</h3>'), 1);
  const f = V.railFoot(earnState({ display_name: 'study-pc' }));
  assert.equal(f.name, 'study-pc'); assert.equal(f.up, 'up 30 min'); assert.equal('pays' in f, false); assert.equal(/pays|0x/.test(JSON.stringify(f)), false);
});
test('T-R19: one projection, a dim foot line, hidden with its reason under a job, a pause, a still chain, no card mining or an unread rate', () => {
  const e = V.earningsLines(earnState(), chainFx(), 28, NOW);
  assert.equal(e.projection, 'About 71.68 IGN a day at 17.0 MH/s, 0.017% of the network.');
  assert.equal(V.cardIgnDay, undefined);
  const held = earnState({ jobs: { active: true, title: 'class v6 floor lane 2 (the 5090 grid)', started_at: NOW - 7260 }, mining: { ...earnState().mining, state: 'held' } });
  const h = V.earningsLines(held, chainFx(), 28, NOW);
  assert.equal(h.projection, null); assert.equal(h.projectionNote, 'Projection paused: class v6 floor lane 2 (the 5090 grid) running since ' + V.hhmm(NOW - 7260) + '.');
  assert.equal(/IGN/.test(h.projectionNote), false);
  const still = V.earningsLines(earnState({ node: { ...earnState().node, tip_age_s: 900 } }), chainFx(), 28, NOW);
  assert.equal(still.projection, null); assert.equal(still.projectionNote, 'Network paused: no block on the chain for 15 min.');
  assert.equal(V.earningsLines(earnState({ mining: { ...earnState().mining, state: 'paused', paused: true } }), chainFx(), 28, NOW).projection, null);
  assert.equal(V.earningsLines(earnState({ mining: { ...earnState().mining, state: 'waiting' } }), chainFx(), 28, NOW).projection, null);
  assert.equal(V.earningsLines(earnState(), chainFx({ network: { ...chainFx().network, hashrate_hps: 0 } }), 28, NOW).projection, null);
  assert.equal(V.earningsLines(earnState(), { ok: false, error: 'neither answered' }, 28, NOW).projection, null);
  // the earned figures hold whatever the projection does
  assert.equal(h.headline.text, '14.64 IGN'); assert.equal(still.h24.text, '14.64 IGN');
  assert.equal(once(html, 'id="e-projection"'), 1); assert.equal(inPage(html, 'earnings', 'id="e-projection"'), 1);
  const sec = section(html, 'earnings');
  assert.ok(sec.indexOf('id="e-projection"') > sec.indexOf('id="standing-details"'), 'the projection is the last thing on the page');
});

// item 3: Mine. The toggle without its sub, four tiles, the card rows, the chain scene, one node line, five activity
// lines; the ladder strip, the money and blocks tiles, the node details, the stats line and the log button leave
test('T-R8 (the toggle), T-A5, T-R9, T-R18 and T-R10: the hero is the stat strip with its units; no money, no blocks, no ladder strip on Mine', () => {
  assert.equal(once(html, 'btn-toggle-sub'), 0);
  assert.equal(once(html, 'btn-toggle-spin'), 1); // F-E63: the spinner beside the word while the engine starts or stops
  const sec = section(html, 'mine'), tiles = sec.match(/<div class="tot"[^>]*>[\s\S]*?<span class="k"[^>]*>([^<]+)<\/span>/g) || [];
  // the stat strip of 15 October 2026: five tiles shown (hash rate, power, earnings a day, temperature, the chain and
  // pool state), then the earned-IGN, efficiency and share-lane tiles hidden for the fold and the node lane
  assert.deepEqual(tiles.map((t) => /<span class="k"[^>]*>([^<]+)<\/span>/.exec(t)[1]), ['MH/s', 'W', 'IGN a day', '&#176;C', '&#160;', 'IGN', 'MH/W', 'shares']);
  // every shown tile carries its label in caps above the figure and a placeholder dash in the figure's place
  assert.equal((sec.match(/<div class="tot"[^>]*>\s*<span class="l"[^>]*>[^<]+<\/span>\s*<span class="v[^"]*ph"[^>]*data-ph="&#8212;">&#8212;<\/span>/g) || []).length, 5, 'five placeholder tiles from the first paint');
  assert.deepEqual(V.effParts(0.0172), { v: '17.2', u: 'kH/W' }); assert.deepEqual(V.effParts(1.72), { v: '1.72', u: 'MH/W' }); assert.deepEqual(V.effParts(0.000303), { v: '303', u: 'H/W' }); assert.equal(V.effParts(0), null);
  assert.equal(inPage(html, 'mine', 'id="t-ign"'), 1); assert.equal(inPage(html, 'mine', 'id="t-eff"'), 1);
  assert.equal(inPage(html, 'mine', 'id="t-shares-cell" hidden'), 1); assert.equal(inPage(html, 'mine', 'id="t-shares"'), 1);
  // the design seat's two reads on the lane (11 October 2026): the count is ash at zero and green from the first
  // accepted share; the IGN tile names the credit's block once the balance rises and says "credit pending on the
  // chain" in the pending grade before, never a figure
  assert.equal(once(src, "classList.toggle('is-ash', !((m.shares_accepted || 0) > 0))"), 1);
  assert.equal(once(src, "'credited at block ' + (m.credit_block || 0)"), 1);
  assert.equal(once(src, "'credit pending on the chain'"), 1);
  assert.equal(typeof V.ignText, 'function'); assert.equal(V.ignText('1378000000000000000', 4), '1.3780');
  assert.equal(inPage(html, 'mine', 't-money'), 0); assert.equal(inPage(html, 'mine', 't-blocks'), 0);
  assert.equal(once(html, 'ladder-strip'), 0); assert.equal(once(html, 'ls-more'), 0); assert.equal(once(src, "'ls-"), 0);
  assert.equal(/£|\$|€/.test(sec.slice(sec.indexOf('class="totals"'), sec.indexOf('id="first-wait"'))), false, 'no currency in the hero');
  assert.equal(V.toggle({ state: 'waiting', paused: false, cards: [{ key: 'a', enabled: false, state: 'off', vendor: 'nvidia', kind: 'discrete' }], found: [] }, { synced: true }, {}).sub, 'no card switched on');
});
test('T-A14 and T-R14: one node line on Mine that opens Settings; the node facts live in Settings', () => {
  const n = (over) => ({ state: 'synced', synced: true, blocks: 39587, headers: 39587, peers: 4, daa: 40000, last_reading_age_s: 4, tip_age_s: 3, message: '', restart_in_s: 0, ...over });
  assert.equal(V.nodeLine(n(), { severity: 'none' }, '').text, 'Node synced · 4 peers');
  assert.equal(V.nodeLine(n({ peers: 1 }), { severity: 'none' }, '').text, 'Node synced · 1 peer');
  assert.equal(V.nodeLine(n({ state: 'syncing', synced: false, blocks: 41000, headers: 52000 }), { severity: 'none' }, 'about 12 min left at 300 blocks/s').text, 'Node syncing · 41,000 of 52,000 blocks · about 12 min left');
  assert.equal(V.nodeLine(n({ state: 'restarting', synced: false, message: 'igneumd exited; restart in 20 s' }), { severity: 'none' }, '').text, 'Node restarting · igneumd exited; restart in 20 s');
  assert.equal(V.nodeLine(n({ state: 'behind', synced: false, tip_age_s: 3180 }), { severity: 'none' }, '').text, 'Node behind · last block 53 min ago');
  assert.equal(once(html, 'id="node-details"'), 1); assert.equal(inPage(html, 'settings', 'id="node-details"'), 1);
  assert.equal(inPage(html, 'mine', 'n-digest'), 0); assert.equal(inPage(html, 'mine', 'node-toggle'), 0);
  assert.equal(inPage(html, 'mine', 'id="node-go"'), 1); assert.equal(inPage(html, 'settings', '<h3>Work source</h3>'), 1);
});
test('T-R13, T-A15 and T-R12 (the stats): Activity is five lines, no stats line, no log button; the log opens from the rail and from Settings', () => {
  assert.equal(once(html, 'btn-open-log'), 0); assert.equal(once(html, 'id="btn-logs"'), 1); assert.equal(once(html, 'id="s-log-open"'), 1);
  assert.equal(once(html, 'd-stats'), 0); assert.equal(V.activityStats, undefined); assert.equal(once(src, 'to the dev fee'), 0);
  assert.equal(once(src, 's.events.slice(0, 5)'), 1, 'five events');
});

// items 6 and 7: Settings in 4.4's order, Updates with the Interface row, the Heat mode label, the unit labels, the copy law
test('T-R21, T-A19, T-A20 and the order of 4.4: eleven Settings sections, Updates holds the Interface row, the fee last', () => {
  const sec = section(html, 'settings'), heads = (sec.match(/<h3(?: [^>]*)?>([^<]+)<\/h3>/g) || []).map((h) => />([^<]+)</.exec(h)[1]).filter((t) => t !== 'Prove instead of mining');
  assert.deepEqual(heads, ['Fleet console', 'Wallet', 'Work source', 'Ember', 'Power control', 'Electricity', 'Heat mode', 'Proving', 'This machine', 'Logs', 'Advanced', 'This machine', 'The dev fee']); // 0.2.8: the Fleet console card first (docs/fleet/DESIGN.md 3.6) // the second This machine is the phone-only Logs and Quit card (main's audit of 21:2x UK, 13 October 2026), above the fee, which stays last
  assert.equal(once(html, 's-interface-card'), 0); assert.equal(once(html, 'id="s-ui-line"'), 1);
  const u = sec.indexOf('id="s-updates-card"'), uEnd = sec.indexOf('\n      </div>', u);
  assert.ok(u > 0 && sec.indexOf('id="s-ui-line"') > u && sec.indexOf('id="s-ui-line"') < uEnd, 'the Interface row sits inside the Updates card');
  assert.ok(sec.indexOf('id="s-ui-builtin"') > u && sec.indexOf('id="s-ui-builtin"') < uEnd);
  const w = V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true });
  assert.equal(w.line, 'Interface 1.0.0, built in');
  const helpStatic = /id="s-ui-help-static"[^>]*>([^<]+)</.exec(sec)[1];
  assert.equal(helpStatic, 'Small changes to this window arrive over the air, signed by Igneum.');
  const heat = /<label class="switch"><input type="checkbox" id="s-heat">[\s\S]*?<span class="sw-text">([\s\S]*?)<\/span><\/label>/.exec(sec)[1].replace(/<[^>]+>/g, '').trim();
  assert.ok(heat.length < 30, 'the Heat mode label is five words: ' + heat);
  assert.equal(inPage(html, 'settings', 'The cards heat for a share of every 10 minutes and rest for the rest. Your card is an electric heater that also earns.'), 1);
  assert.equal(once(html, 'Settings can show it again.'), 1); assert.equal(once(html, 'Earnings can show it again.'), 0);
});
test('the hash tile\'s sub shows the cards line and one line a lane (the first desktop\'s two-lane frame, 16 October 2026)', () => {
  const css = readFileSync(join(here, 'app.css'), 'utf8');
  const rule = css.split('\n').find((l) => l.startsWith('#t-hash-sub{'));
  assert.ok(rule && /white-space:pre-line/.test(rule) && /-webkit-line-clamp:4/.test(rule), 'the sub keeps its lines under a four-line clamp: ' + rule);
});
test('T-A8 and the copy law: no uppercase on a unit label; no em dash in the window', () => {
  const css = readFileSync(join(here, 'app.css'), 'utf8');
  for (const sel of ['.tot .k', '.num .v .unit', '.tier-b .l']) {
    const rules = css.split('\n').filter((l) => l.startsWith(sel + '{') || l.startsWith(sel + ','));
    for (const r of rules) assert.equal(/text-transform:\s*uppercase/.test(r), false, sel + ' is a unit label: ' + r);
  }
  assert.equal(once(html, '\u2014'), 0, 'no em dash in index.html'); assert.equal(once(src, '\u2014'), 0, 'no em dash in app.js');
});

// One network (the old base, step 3): the pinned chain, Igneum devnet9 (0.2.1; 0.2.0 pinned devnet7). The network step
// and the Settings Node card show one card; no test-network card, no "opens when" note, none of the old chain labels
// anywhere in the window (the first real capture of the Windows window, 12 October 2026: the welcome eyebrow still
// read devnet7 under 0.2.1).
test('one network, the pinned chain; none of the old chain labels anywhere in the window', () => {
  assert.equal(V.NETWORKS.length, 1); assert.equal(V.NETWORKS[0].id, 'chain-7322'); assert.equal(V.NETWORKS[0].name, 'chain 7322'); assert.equal(V.NETWORKS[0].label, 'the public test network');
  const fresh = V.networkWords({ setup_done: false, network_name: '', network_pending: '', settings: { network: '' }, update: {} });
  assert.equal(fresh.cards.length, 1); assert.equal(fresh.selected, 'chain-7322'); assert.equal(fresh.cards[0].open, true); assert.equal(fresh.cards[0].selected, true);
  assert.equal(fresh.cards[0].line, 'chain 7322'); assert.equal(fresh.cards[0].sub, V.NETWORKS[0].sub);
  const running = V.networkWords({ setup_done: true, network_name: 'chain 7322', network_pending: '', settings: { network: '' }, update: {} });
  assert.equal(running.line, 'This machine mines the public test network (chain 7322).'); assert.equal(running.cards[0].current, true);
  assert.equal(V.networkIdOf('chain 7320'), 'chain-7322', 'an older name reads as the one network');
  assert.equal(V.networkWords({ settings: { network: 'dev' + 'net12' } }).selected, 'chain-7322', 'an older stored id reads as the one network (F-522)');
  for (const text of [html, src]) { assert.equal(/test\x6eet/i.test(text), false, 'no test network'); assert.equal(/Devnet [34]|devnet-[34]|devnet v4|Igneum 2\.0 devnet|devnet7/.test(text), false, 'no old chain label'); }
  assert.equal(once(html, 'Igneum dev' + 'net12'), 0, 'the naming rule: never the internal name on the page'); assert.equal(once(html, 'he public test network'), 2, 'the welcome eyebrow and the node facts default'); assert.equal(once(html, 'Igneum dev' + 'net10'), 0, 'the old chain is not named on the page');
  assert.equal(once(src, 'opens when the network does'), 0);
});

// every setup screen can be shown (8 October 2026, found by the net-20 capture): .screen is display:none and only the ids in
// app.css's phase rule display; screen-network was never in it, so the network step (step 3 of 4, since an earlier release) was a blank
// page with no button on a fresh install. Known-failed first: the rule lists five screens and index.html has six.
test('every screen in index.html has its display rule in app.css', () => {
  const css = readFileSync(join(here, 'app.css'), 'utf8');
  const screens = [...html.matchAll(/<section class="screen" id="screen-([a-z-]+)"/g)].map((m) => m[1]);
  assert.ok(screens.length >= 6, 'the setup screens and the dashboard: ' + screens.join(','));
  for (const id of screens) assert.ok(css.includes('body[data-phase="' + id + '"] #screen-' + id), 'app.css shows #screen-' + id);
});


// v0.2.1 (the founder's call at 18:1x UK, 8 October 2026, reversing the audit's four-page layout on this point): the Cards
// tab is back in the rail as its own page with every card row (the state, the hash rate, the power, the temperature, the
// tune state, the enable switch, the 16 GB proving note); the home page keeps the headline rate and the chain status.
// Known-failed first on an earlier release 95f3068e: the rows on Mine, no Cards page.
test('v0.2.1: the Cards page holds every card row; the home page keeps the headline rate and the chain status', () => {
  assert.equal(V.page('cards').title, 'Cards'); assert.equal(V.page('cards').sub, 'Every card, its state and its switch');
  assert.equal(inPage(html, 'cards', 'id="d-cards"'), 1); assert.equal(inPage(html, 'mine', 'id="d-cards"'), 0); assert.equal(inPage(html, 'mine', 'cards-card'), 0);
  assert.equal(inPage(html, 'mine', 'id="totals"'), 1); assert.equal(inPage(html, 'mine', 'id="dag-card"'), 1); assert.equal(inPage(html, 'mine', 'id="node-go"'), 1);
  assert.equal(once(src, "page === 'cards'"), 1, 'the Cards page has its own render');
  // the row's words: the tune state and the proving note come from the View (the row builder is page-side)
  const r = V.cardsRowWords(cardFx(), stateFx(), NOW);
  assert.equal(r.tune, 'Locked at 1,950 MHz, cap 405 W. Knee at 1,200 MHz.');
  assert.equal(r.proving, '', 'a 32 GB card carries no proving note');
  const small = V.cardsRowWords(cardFx({ vram_mb: 12288 }), stateFx(), NOW);
  assert.equal(small.proving, 'NVIDIA GeForce RTX 5090: mining and proving together need a 16 GB card; this card does one at a time; mining continues (12 GB).');
  assert.equal(V.cardsRowWords(cardFx({ vendor: 'apple', kind: 'apple', tiers: [], tier_note: 'no lever: Apple silicon sets its own clocks and power', lock_note: 'no lever', lock_at: 0, lock_mhz: 0 }), stateFx(), NOW).proving, '');
});

// the four interface words (docs/spec/finality-guarantees.md section 9): the window's block inspector reads included,
// executed, proven, finalised; a block under the lock is finalised even while the network's finality is paused (the pause
// sits on the status strip), and no block ever reads "not active". Known-failed first: "Final (reported)" / "Not marked final".
test('the block inspector reads the four words; a locked block is finalised even in a pause', () => {
  const b = { color: 'blue', chain: true, number: 28175, proven: true, final: true, locked: false };
  assert.equal(V.blockState(b, false), 'Included / executed as chain block 28,175 / proven / finalised');
  assert.equal(V.blockState(b, true), 'Included / executed as chain block 28,175 / proven / finalised', 'a lock holds in a pause');
  assert.equal(V.blockState({ ...b, final: false }, true), 'Included / executed as chain block 28,175 / proven · finality paused on the network above the lock');
  assert.equal(V.blockState({ ...b, final: false, proven: false }, false), 'Included / executed as chain block 28,175');
  assert.equal(V.blockState({ color: 'red', chain: false, proven: false, final: false }, false), 'Excluded');
  assert.equal(V.blockState({ color: '', chain: false }, false), 'Pending');
  assert.equal(/not active|Not marked final|\(reported\)/.test(src), false);
  assert.equal(once(html, 'id="s-positioning"'), 0, 'the positioning line once, on the hero'); assert.equal(once(html, 'id="positioning"'), 1);
});


// v0.2.2 (the founder, 8 October 2026, 19:25 on the mini): with the node at 0 peers the worker idles for lack of templates;
// the node line says so ("Node: no peers, dialling the seeds") and the engine's card message names the cause, never a worker
// fault. Known-failed first: "Node no peers · looking for the seed node" and no such message in the engine.
test('v0.2.2: no peers reads as the node dialling the seeds on the node line; the engine names the missing templates', () => {
  const n = { state: 'no peers', synced: false, blocks: 39000, headers: 39000, peers: 0, daa: 40000, last_reading_age_s: 4, tip_age_s: 500, message: 'waiting for a peer on our chain', restart_in_s: 0 };
  const l = V.nodeLine(n, { severity: 'none' }, '');
  assert.equal(l.text, 'Node: no peers, dialling the seeds'); assert.equal(l.tone, 'bad');
  assert.equal(V.nodeWords(n, { severity: 'none' }, '').line, 'No other node on our chain is connected. The node keeps dialling the seed list; mining waits for block templates.');
  // the old base (docs/product-a/OLD-BASE.md step 2): the engine runs no node and no old miner slot; the worker layer's
  // supervisor names a source without work on the card itself (its slot's message), so the engine-side read is gone
  const watchdog = readFileSync(join(here, '..', 'src', 'watchdog.rs'), 'utf8');
  assert.ok(once(watchdog, 'no block templates: the node has no peers') >= 1, 'the watchdog carries the cause');
});

// review B, 8 October 2026, for v0.2.2 (the founder's rulings through the shipper). F14: two builds from one tree; the
// window names the build it runs in, shows the remote-jobs switch on the lab build only, and asks the update choice at
// install. F07: the per-device proving mode from the engine (simultaneous, time-share, mining-only). F04: a recovery lock
// is labelled "recovery" on every surface, never "final". Written with the code (the read-back is the assertion).
test('F14: the window names the build, hides the jobs switch on the public miner, asks the update choice at install', () => {
  assert.equal(V.productName('public'), 'Ember'); assert.equal(V.productName('lab'), 'Ember Lab'); assert.equal(V.productName(''), 'Ember');
  assert.equal(once(src, "View.productName(s.edition)"), 1, 'the About line reads the build');
  assert.equal(once(html, 'id="s-jobs-row"'), 1); assert.equal(once(src, "setHidden('s-jobs-row', s.edition !== 'lab')"), 1, 'the jobs switch is the lab build\'s');
  const ws = html.indexOf('id="screen-welcome"'), w = html.slice(ws, html.indexOf('</section>', ws));
  assert.ok(/<input type="checkbox" id="w-auto-update" checked>/.test(w), 'the choice at install, on by default');
  assert.ok(w.indexOf('id="w-auto-update"') < w.indexOf('id="btn-begin"'), 'the choice sits before Get started');
  assert.equal(once(src, "$('w-auto-update').addEventListener('change', function () { api('api/update/auto', { on: this.checked }); })"), 1, 'the welcome switch posts the choice by the Settings switch\'s route');
});
test('F07: the engine\'s per-device mode line stands on the Proving section; the window\'s own tier sentence stands for an older engine', () => {
  const cd = cardFx();
  assert.equal(V.proveModeLine(cd, { modes: [{ key: cd.key, name: cd.name, mode: 'time-share', line: 'NVIDIA GeForce RTX 5090: one at a time (32 GB): the miner steps off the card, the memory is confirmed free, the proof runs, the miner rebuilds its dataset and resumes.' }] }), 'NVIDIA GeForce RTX 5090: one at a time (32 GB): the miner steps off the card, the memory is confirmed free, the proof runs, the miner rebuilds its dataset and resumes.');
  assert.equal(V.proveModeLine(cd, {}), V.proveTier(cd));
  assert.equal(V.proveModeLine(cd, { modes: [{ key: 'other', line: 'x' }] }), V.proveTier(cd));
  assert.equal(once(src, 'View.proveModeLine(cd, pv)'), 1);
});
test('F04: a recovery lock reads "recovery" on the strip and the inspector, never "final"', () => {
  const f = { last_lock: 8495, age_s: 40, lock_kind: 'recovery', paused: false };
  const fw = V.finalityWords(f);
  assert.equal(fw.recovery, true); assert.equal(fw.paused, false); assert.match(fw.note, /^Recovery lock: /); assert.equal(/final\b/i.test(fw.note), false);
  assert.equal(V.lockKindWords(f).word, 'recovery lock'); assert.equal(V.lockKindWords({ lock_kind: 'certified' }).recovery, false);
  const b = { color: 'blue', chain: true, number: 28175, proven: true, final: true };
  assert.equal(V.blockState(b, false, 'recovery'), 'Included / executed as chain block 28,175 / proven / finalised by a recovery lock');
  assert.equal(V.blockState(b, false, ''), 'Included / executed as chain block 28,175 / proven / finalised');
  // 0.2.3: the block inspector left with the live-dag scene (the chain card draws the network scene); its two source
  // strings with it; the View words above are the ones that remain
  // the pause word stands until the node carries lockKind
  assert.equal(V.finalityWords({ paused: true, line: 'Finality paused since 18:39 UTC: under two thirds of the weight is signing' }).note, 'Finality paused since 18:39 UTC: under two thirds of the weight is signing');
});

test('the Ledger page: every figure with its source named, nothing estimated; the unread case says so', () => {
  // the Live Work Ledger's first screen (the settlement seat's miner view v1, 12 October 2026)
  const s = { address: { display: '0x8e796a02714eBf0C48De7cCAb5e71254089d3215' }, mining: { accepted_session: 312 }, dev_fee: { receipts_total: 312, receipts_v1_verified: 312, receipts_file: '/tmp/r/pool.igneum.network-1791770000.jsonl' } };
  const un = V.ledgerWords(null, s);
  assert.equal(un.rows[0].v, '312 of 312'); assert.match(un.rows[0].s, /this machine, from the pool/); assert.match(un.rows[0].s, /pool\.igneum\.network-1791770000\.jsonl/);
  assert.equal(un.rows[1].v, '312 this session'); assert.match(un.rows[1].s, /the pool/);
  assert.equal(un.rows[2].v, 'not read'); assert.match(un.rows[2].s, /not read yet/); assert.equal(un.asof, '');
  const err = V.ledgerWords({ error: 'the ledger answered 404 (no shares from this address in the window yet)' }, s);
  assert.match(err.rows[2].s, /answered 404/);
  const d = { schema: 'igneum/ledger/miner-view/v1', head: 341, read_at: '2026-10-12T02:56:54Z', ledger: { record: '20261012T025654Z' }, paid_wei: '466890346822085197775', blocks: new Array(194).fill({}),
    totals: { paid: 8306, in_carry: 0, expired_reorg: 157, expired_window: 0 },
    pool_claims: { pool_accepted: 10230, unjoined_within_window: 3, node_answers: { accepted: 8715, refused: 2713, refused_by: { 'igneum shares: address at cap': 2082, 'igneum shares: stale parent': 631 } } } };
  const w = V.ledgerWords(d, s);
  const by = Object.fromEntries(w.rows.map((r) => [r.id, r]));
  assert.equal(by.pool.v, '10,230'); assert.match(by.pool.s, /statement/);
  assert.equal(by.sealers.v, '8,715 accepted, 2,713 refused'); assert.match(by.sealers.s, /631 stale parent, 2,082 address at cap/);
  assert.equal(by.paid.v, '466.9 IGN in 194 blocks'); assert.match(by.paid.s, /8,306 shares paid/);
  assert.equal(by.carry.v, '0'); assert.equal(by.expired.v, '157 by a reorganisation, 0 at the window'); assert.equal(by.open.v, '3');
  assert.match(w.asof, /^as of 12 October at \d\d:\d\d, head 341$/); // the founder's audit of 15 October 2026: the reader's clock, never an ISO stamp on the page (the record in the title)
  assert.equal(/estimat|about|roughly/.test(JSON.stringify(w)), false, 'nothing estimated');
  // F-E21 (E2, 14 October 2026): the Mine row's line carries paid, carried and lost to a race from the same totals
  assert.equal(V.mineShareWords({ accepted: 295944, paid: 198446, in_carry: 0, pooled: 15, expired_window: 126, expired_reorg: 98770 }, { height: 5083 }),
    '295,944 accepted · 198,446 paid (last in block 5,083) · 15 carried · 98,770 lost to a race (33%) · 126 expired at the window');
  assert.equal(V.mineShareWords({ accepted: 12, paid: 12, in_carry: 0, pooled: 0, expired_window: 0, expired_reorg: 0 }, null), '12 accepted · 12 paid · 0 lost to a race');
  assert.equal(V.mineShareWords({ paid: 3 }, null), '', 'no accepted count, no line');
  assert.equal(V.mineShareWords({ accepted: 5 }, null), '', 'totals not read, no line');
  // F-E35 (main's audit of the 0.2.7 captures): the chain card never says "not read" for a feed that is another chain's
  assert.equal(V.dagStateWords(7320, 7357, 'igneum-dev' + 'net10', "the chain feed is chain 7320's, not the mined chain 7357's"), 'No public chain feed for chain 7357, the chain this machine mines; the stage shows nothing');
  assert.equal(V.dagStateWords(7322, 7322, 'igneum-dev' + 'net12', ''), 'Live: the public test network, chain 7322, the chain this machine mines'); // the founder's audit of 15 October 2026: the chain card's line a plain sentence, never caps
  // the naming rule of 15 October 2026: the old public test network and the research chain by their public words, never a feed's internal name
  assert.equal(V.chainWord(7320, 'igneum-dev' + 'net10'), 'the old public test network, chain 7320'); assert.equal(V.chainWord(7358, 'vector' + 'work8'), 'the research chain, chain 7358'); assert.equal(V.chainWord(7310, ''), 'chain 7310');
  assert.equal(V.dagStateWords(0, 7320, '', 'the chain feed is not read: 503 from the feed'), 'Chain feed not read: 503 from the feed');
  // the founder's cells of 15 October 2026 (the pixel audit): the earnings tile from the chain's credit with the last pay's time,
  // the share strip's whole squares, the first share marked once per install, the pool's host and port alone, the Apple words
  const ct = V.earnCreditTile({ ign: 13.4, head: 635, lastPaid: { height: 633, at_unix: 1000 }, behind: 8 }, 1130);
  assert.equal(ct.text, '13.40'); assert.equal(ct.unit, 'IGN on the chain'); assert.equal(ct.ph, false); assert.equal(ct.tone, 'good');
  assert.match(ct.sub, /^last paid .* ago at block 633$/);
  assert.equal(V.earnCreditTile({ ign: 0, head: 635, lastPaid: null }, 1130).sub, 'on the chain, read at block 635');
  assert.equal(V.earnCreditTile({ ign: 2.5, head: 0, lastPaid: { height: 12, at_unix: 0 } }, 1130).sub, 'last paid at block 12');
  assert.equal(V.stripCapacity(0), 0); assert.equal(V.stripCapacity(100), 4); assert.equal(V.stripCapacity(748), 40); assert.equal(V.stripCapacity(30), 1);
  assert.equal(V.firstShareMark(0, null), false); assert.equal(V.firstShareMark(1, null), true); assert.equal(V.firstShareMark(50, '1792000000000'), false); assert.equal(V.firstShareMark(50, ''), true);
  assert.equal(V.hostPort({ url: 'pool.igneum.network:3342 notls' }), 'pool.igneum.network:3342'); assert.equal(V.hostPort({ url: 'https://node:8545/' }), 'node:8545'); assert.equal(V.hostPort({}), '');
  assert.equal(V.noReadingWords([{ vendor: 'apple' }]), 'not read on Apple silicon'); assert.equal(V.noReadingWords([{ vendor: 'amd', kind: 'integrated' }]), 'not read on an integrated GPU'); assert.equal(V.noReadingWords([{ vendor: 'nvidia' }]), 'no reading on this card');
  // main's cell: the two dead tiles collapse to one quiet line on a machine with no power or temperature reading
  const appleCards = [{ name: 'Apple M6', vendor: 'apple', backend: 'metal', enabled: true, state: 'mining', hash_now: 1.6 }];
  const deadT = V.deadTiles({ power: { ph: true }, temp: { ph: true } }, appleCards);
  assert.deepEqual(deadT, { hidePower: true, hideTemp: true, note: 'power and temperature: not read on Apple silicon' });
  assert.equal(V.deadTiles({ power: { ph: false, text: '410' }, temp: { ph: true } }, [{ vendor: 'nvidia', power_w: 410 }]).hidePower, false);
  assert.equal(V.deadTiles({ power: { ph: true }, temp: { ph: true } }, []).note, '');
  // the founder's order of 15 October 2026: dark on every run without a saved choice; a saved choice kept; the screenshot param first
  assert.equal(V.themeChoice(null, null), 'dark'); assert.equal(V.themeChoice('', ''), 'dark'); assert.equal(V.themeChoice(null, 'light'), 'light'); assert.equal(V.themeChoice(null, 'system'), 'system'); assert.equal(V.themeChoice('light', 'dark'), 'light'); assert.equal(V.themeChoice('odd', 'odd'), 'dark');
  // the secrets order: the fleet console's token directory is shown to its last four off focus
  assert.equal(V.fleetUrlShown('https://build.igneum.network/_fleet/08605bedda365cd2b9f6ef496a67952e/'), 'build.igneum.network/_fleet/...952e/');
  assert.equal(V.fleetUrlShown('https://host/_fleet/abcd/'), 'host/_fleet/.../'); assert.equal(V.fleetUrlShown(''), ''); assert.equal(V.fleetUrlShown('nonsense'), '...');
  // main's read of the 0.2.19 sheets (16 October 2026): the lane rows under a card, the current lane first; a pool on another chain than the pin is not offered
  const lr = V.laneRows({ lanes: [{ coord: 3, word: 'the bandwidth lane', accepted: 41, hps: 1.55e6, seconds: 1500, current: false }, { coord: 4, word: 'the programme lane', accepted: 2, hps: 32000, seconds: 4000, current: true }] });
  assert.equal(lr[0].word, 'the programme lane'); assert.equal(lr[0].rate, '32 kH/s'); assert.equal(lr[0].shares, '2 shares'); assert.equal(lr[0].time, '1 h 7 min'); assert.equal(lr[0].current, true);
  assert.equal(lr[1].rate, '1.6 MH/s'); assert.equal(lr[1].time, '25 min'); assert.deepEqual(V.laneRows({}), []);
  assert.equal(V.laneRows({ lanes: [{ coord: 2, word: 'the raster lane', accepted: 0, hps: 390.4, seconds: 60, current: true }] })[0].rate, '390 attempts a second');
  // the current lane's figure is the card's once the stint is a minute old; younger, the stint's figure says so
  assert.equal(V.laneRows({ hash_now: 1.6, stint_s: 90, lanes: [{ coord: 3, word: 'the bandwidth lane', accepted: 1, hps: 1.7e6, seconds: 90, current: true }] })[0].rate, '1.6 MH/s');
  assert.equal(V.laneRows({ hash_now: 1.6, stint_s: 30, lanes: [{ coord: 3, word: 'the bandwidth lane', accepted: 1, hps: 1.7e6, seconds: 30, current: true }] })[0].rate, '1.7 MH/s this stint');
  assert.equal(V.laneRows({ hash_now: 1.6, stint_s: 90, lanes: [{ coord: 3, word: 'the bandwidth lane', accepted: 1, hps: 1.7e6, seconds: 90, current: false }] })[0].rate, '1.7 MH/s');
  const po = V.poolsOffered([{ address: 'pool.igneum.network:3342', chain_id: 7322 }, { address: 'pool.igneum.network:3341', chain_id: 7320 }, { address: 'x:1' }], 7322);
  assert.equal(po.shown.length, 2); assert.equal(po.off.length, 1); assert.equal(po.words, '1 pool on the old public test network is not listed: this version mines the public test network, chain 7322.');
  assert.equal(V.poolsOffered([], 7322).words, '');
  // one pool on two ports is one listing: both ports named, the probe from the port that publishes it, the dialled port the switch's
  const two = [{ name: 'Igneum Labs pool', address: 'pool.igneum.network:3341', project: true, chain_id: 7322, fee_bps: 100, reliability: {} }, { name: 'Igneum Labs pool', address: 'pool.igneum.network:3342', project: true, chain_id: 7322, fee_bps: 100, reliability: { probe: { state: 'operational', regions: { eu: 100 } } } }];
  const pm = V.poolsMerged(two, 'pool.igneum.network:3342 notls');
  assert.equal(pm.length, 1); assert.equal(pm[0].address, 'pool.igneum.network:3342'); assert.equal(pm[0].address_line, 'pool.igneum.network:3341 and :3342'); assert.deepEqual(pm[0].reliability.probe.regions, { eu: 100 });
  assert.equal(V.poolsMerged(two, '')[0].address, 'pool.igneum.network:3341');
  assert.equal(V.poolsMerged([two[0], { ...two[1], name: 'Another pool' }], '').length, 2);
  assert.equal(V.PIN_CHAIN_ID, 7322); assert.equal(V.KNEE_LATER, undefined, 'the knee runs under the worker layer (0.2.22); no later-version promise');
  // the pool the engine dials, from the supervisor's source name; the stored text only when the engine names none
  assert.equal(V.hostPort({ url: 'pool.igneum.network:3341 notls', name: 'pool Igneum Labs pool (pool.igneum.network:3342, no TLS)' }), 'pool.igneum.network:3342');
  assert.equal(V.hostPort({ url: 'pool.igneum.network:3341 notls', name: '' }), 'pool.igneum.network:3341');
  assert.equal(V.hostPort({ url: 'https://rpc.igneum.network/', name: 'solo through https://rpc.igneum.network:443' }), 'rpc.igneum.network');
  // the founder's M5 Max on the raster lane (16 October 2026): every rate in the lane's own unit; the machine's tile by lane, never a sum across units
  assert.deepEqual(V.rateWords(314, 'the raster lane'), { text: '314', unit: 'attempts a second', value: 314 });
  assert.deepEqual(V.rateWords(32000, 'the programme lane'), { text: '32', unit: 'kH/s', value: 32000 });
  assert.deepEqual(V.rateWords(0.98e6, ''), { text: '1.0', unit: 'MH/s', value: 0.98e6 }); assert.deepEqual(V.rateWords(63e6, 'the programme lane').unit, 'MH/s');
  assert.deepEqual(V.rateWords(94.3e6, 'the bandwidth lane'), { text: '94.3', unit: 'MH/s', value: 94.3e6 });
  assert.equal(V.rateWords(0, 'the raster lane').text, '\u2014');
  const mr1 = V.machineRate([{ state: 'mining', hash_now: 0.000314, lane_word: 'the raster lane' }]);
  assert.equal(mr1.length, 1); assert.equal(mr1[0].text, '314'); assert.equal(mr1[0].unit, 'attempts a second'); assert.equal(mr1[0].lane, 'the raster lane');
  const mr2 = V.machineRate([{ state: 'mining', hash_now: 53.6, lane_word: 'the bandwidth lane' }, { state: 'mining', hash_now: 0.000379, lane_word: 'the raster lane' }, { state: 'mining', hash_now: 40.7, lane_word: 'the bandwidth lane, probing' }]);
  assert.equal(mr2.length, 2); assert.equal(mr2[0].lane, 'the bandwidth lane'); assert.equal(mr2[0].text, '94.3'); assert.equal(mr2[1].unit, 'attempts a second');
  const rasterTile = V.stripTiles({ mining: { state: 'mining', hash_total: 0.000314, cards: [{ key: 'a', state: 'mining', enabled: true, hash_now: 0.000314, lane_word: 'the raster lane', kind: 'apple', vendor: 'apple' }] }, source: {} }, null, 0, null).hash;
  assert.equal(rasterTile.text, '314'); assert.equal(rasterTile.unit, 'attempts a second'); assert.match(rasterTile.sub, /on the raster lane/);
  const credTiles = V.stripTiles({ mining: { state: 'mining', hash_total: 1.6, cards: [{ name: 'Apple M6', vendor: 'apple', backend: 'metal', enabled: true, state: 'mining', hash_now: 1.6 }] }, source: { kind: 'pool', connected: true, chain_id: 7322, url: 'pool.igneum.network:3342 notls', name: 'pool pool.igneum.network (pool.igneum.network:3342, no TLS)' } }, null, 1130, { ign: 13.4, head: 635, lastPaid: null });
  assert.equal(credTiles.earn.text, '13.40'); assert.equal(credTiles.earn.unit, 'IGN on the chain'); assert.equal(credTiles.power.sub, 'not read on Apple silicon'); assert.equal(credTiles.temp.sub, 'not read on Apple silicon');
  assert.equal(credTiles.chain.sub, 'the public test network, chain 7322\npool.igneum.network:3342'); assert.equal(credTiles.chain.title, 'pool pool.igneum.network (pool.igneum.network:3342, no TLS)');
  // 0.2.3: the ledger's rows live under Earnings (the kept skin's five pages); no Ledger page
  assert.equal(V.PAGES.some((p) => p.id === 'ledger'), false, 'no ledger page in the rail');
});

// T-R22 (14 October 2026, the served b6321390): the app part calls the module's names only through the module or an alias.
// A bare call of a module-only name threw "fleetWords is not defined" in the render loop and every render after it stopped;
// the harness above binds nothing extra, but no test rendered the Settings card, so the fault reached the mini's page.
test('T-R22: every module name the app part calls bare is defined in the app part or aliased from View', () => {
  const close = src.indexOf('\n})();', src.indexOf('var View = (function'));
  const moduleEnd = src.indexOf('\n', close + 1);
  const exportsLine = src.slice(src.lastIndexOf('\n  return {', close), close);
  const names = [...new Set([...exportsLine.matchAll(/\b([A-Za-z_]\w*)\s*:/g)].map(m => m[1]))];
  const app = src.slice(moduleEnd).replace(/\/\/[^\n]*/g, '');  // comments carry no calls ("the Pools page (DESIGN" read as one)
  const missing = [];
  for (const n of names) {
    const bare = new RegExp('(?<![.\\w])' + n + '\\s*\\(', 'g');
    const defined = new RegExp('(function ' + n + '\\b|\\b(var|let|const) ' + n + '\\b|[\\s,]' + n + ' = View\\.' + n + '\\b)');
    if (bare.test(app.replace(new RegExp('function ' + n + '\\s*\\(', 'g'), '')) && !defined.test(app)) missing.push(n);
  }
  assert.deepEqual(missing, [], 'bare calls of module-only names: ' + missing.join(', '));
});

// the words audit of 14 October 2026 (main's class: a state named, never an instruction; one word for one thing on every
// surface): the Start button's sub-line names the state; the next-block bar carries the founder's one number
test('the words: the toggle names a state; the next-block bar one number', () => {
  assert.equal(V.toggle({ state: 'idle', cards: [cardFx({ enabled: false, state: 'off' })] }, null, stateFx()).sub, 'no card switched on');
  const chain = readFileSync(join(here, 'chain.js'), 'utf8');
  // the scene of 65a3856b (the site lead's cut of 15 October 2026, the seven faults): the bar names the chain's target and
  // the hour's mean, the shares line names this machine; the timer's wording is main's to rule on
  assert.ok(chain.includes("'last block '") && chain.includes("' s ago'") && chain.includes("the chain aims at ") && chain.includes("this hour ") && chain.includes(" s on average"), "the scene's timer words of 15 October 2026");
  assert.ok(chain.includes(" from this machine since the page opened"), "the app's shares line names this machine");
  assert.equal(once(chain, "one every "), 0, "the median wording left with 65a3856b");
  assert.equal(once(chain, "'next block likely in '"), 0); assert.equal(once(chain, "'late by '"), 0); assert.equal(once(chain, "'next block in '"), 0);
});
