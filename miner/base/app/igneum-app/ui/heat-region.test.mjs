// node --test app/igneum-app/ui/heat-region.test.mjs   (no dependencies; the pre-push gate runs it)
// Ember Heat (mission item 7, docs/plans/ember-heat.md): the region prompt shows the right restricted entry for a Russian
// region and nothing for the rest; the rent line reads the bench log's measured rate; the heat words name the duty and
// the watts; the money symbol follows the region.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

const card = (over) => ({ key: 'nvidia:0:RTX 5090', name: 'NVIDIA GeForce RTX 5090', vendor: 'nvidia', kind: 'discrete', worker: 'CUDA', vram_mb: 32768, enabled: true, state: 'mining', hash_now: 124.3, hash_avg: 120, accepted: 3, rejected: 0, identities: 8, ids: [], prepared: true, restart_in_s: 0, message: '', reason: '', power_w: 410.2, power_limit_w: 460, power_default_w: 575, power_pct: 80, power_applied: true, temp_gpu: 61, temp_mem: 72, telemetry_at: 1, ...over });

test('the region prompt shows the restricted entry for a Russian region, with the standing sentence first', () => {
  const line = V.restrictedLine('ru-mow');
  assert.ok(line.startsWith(V.RESTRICTED_SENTENCE), line);
  assert.equal(V.RESTRICTED_SENTENCE, 'Mining may be restricted where you are. You are responsible for checking.');
  assert.ok(line.includes('Moscow region from 15 August 2026 to 2032'), line);
  assert.ok(V.restrictedLine('ru').includes('ten regions from 1 January 2025 to 15 March 2031'), 'the other-region entry carries the ten-region ban');
  assert.ok(V.restrictedLine('ru').includes('does not carry the list of ten'), 'the app says what it does not know');
  for (const seasonal of ['ru-irk', 'ru-bur', 'ru-zab']) assert.ok(V.restrictedLine(seasonal).includes('seasonal mining ban'), seasonal);
  assert.ok(V.restrictedLine('cn').includes('illegal in China'), 'China is the other entry of future.md 8.3 item 7');
});

test('no region outside the restricted list of future.md 8.3 gets a line', () => {
  const restricted = V.REGIONS.filter((r) => r.restricted).map((r) => r.code).sort();
  assert.deepEqual(restricted, ['cn', 'ru', 'ru-bur', 'ru-irk', 'ru-mow', 'ru-zab'], 'Russia (the ten regions, Moscow, the seasonal three) and China, nothing else');
  for (const code of ['gb', 'de', 'us', 'kz', 'py', 'ir', 'other', '']) assert.equal(V.restrictedLine(code), '', code);
});

// the research file and the bench log are internal (tools/ci/export-exclude.txt): a tree without them skips the read
const research = join(here, '../../../docs/analysis/mission/future.md');
const benchLog = join(here, '../../../docs/bench-log.md');

test('the restricted entries are the ones the research file states (future.md section 4.4 and 8.3)', (t) => {
  if (!existsSync(research)) { t.skip('docs/analysis/mission/future.md is not in this tree'); return; }
  const future = readFileSync(research, 'utf8');
  assert.ok(future.includes('mining banned in 10 regions 1 Jan 2025 to 15 Mar 2031'), 'the ten-region ban and its dates');
  assert.ok(future.includes('Moscow region from 15 Aug 2026 to 2032'), 'the Moscow region entry and its dates');
  assert.ok(future.includes('seasonal bans in Irkutsk, Buryatia, Zabaikalsky'), 'the seasonal three');
  assert.ok(/China \| illegal; joint Notice 6 Feb 2026/.test(future), 'China');
  assert.ok(future.includes('"mining may be restricted where you are; you are responsible for checking"'), 'the standing sentence');
});

test('the price prompt defaults from the public table and says it is typed, never fetched', () => {
  assert.equal(V.regionOf('gb').price, 26.32);
  assert.ok(V.regionOf('gb').source.includes('Ofgem'));
  assert.equal(V.regionOf('de').price, 38.69);
  assert.equal(V.regionOf('us').price, 17.7);
  assert.equal(V.regionOf('ru-mow').price, 0, 'no table price for a Russian region: the miner types one');
  assert.ok(V.priceNote('gb').includes('Typical: 26.32 p per kWh'));
  assert.ok(V.priceNote('gb').endsWith('Nothing is fetched.'));
  assert.ok(V.priceNote('ru').startsWith('No table price for this region.'));
  assert.ok(V.priceNote('').includes('Nothing is fetched.'));
  assert.ok(!src.includes('fetch(\'https://') && !src.includes('exchangerate'), 'the UI fetches no price and no rate');
});

test('the money symbol follows the region: pounds, euros, dollars', () => {
  V.setRegion('gb'); assert.equal(V.money(1.5), '£1.50');
  V.setRegion('de'); assert.equal(V.money(1.5), '€1.50'); assert.equal(V.currencyOf('de').minor, 'c');
  V.setRegion('us'); assert.equal(V.money(1.5), '$1.50');
  V.setRegion('ru-mow'); assert.equal(V.currencyOf('ru-mow').code, 'USD', 'a region without a table currency types US cents');
  V.setRegion(''); assert.equal(V.money(1.5), '£1.50', 'no region chosen: pounds, as before');
});

test('the rent line reads the bench log: the measured USD per MH/s-hour in the app is the bench log\'s number', (t) => {
  if (!existsSync(benchLog)) { t.skip('docs/bench-log.md is not in this tree'); return; }
  const log = readFileSync(benchLog, 'utf8');
  const section = log.slice(log.lastIndexOf('## Rental cost of hash'));
  assert.ok(section.length > 0, 'the bench log carries a Rental cost of hash entry');
  const m = section.match(/USD ([0-9.]+) per MH\/s-hour/);
  assert.ok(m, 'the entry states USD x per MH/s-hour');
  assert.equal(V.RENT.usd_per_mhs_hour, parseFloat(m[1]), 'the app carries the bench log\'s measured rate; update RENT when the log moves');
  const date = section.match(/## Rental cost of hash, ([0-9]+ [A-Za-z]+ [0-9]{4})/);
  assert.ok(date, 'the entry is dated');
  assert.equal(V.RENT.measured, date[1]);
});

test('the cost-against-rent line: a UK card at the Ofgem price is about 10x cheaper than rented hash (future.md 3.4)', () => {
  V.setRegion('gb');
  // 3.27 W per MH/s at 26.32 p: 0.0861 p per MH/s-hour, about USD 0.00114, 10x under 0.0117
  const r = V.rentLine([card({ power_w: 327, hash_now: 100 })], 26.32, 'gb');
  assert.equal(r.kind, 'line');
  assert.ok(Math.abs(r.cost - 0.0861) < 0.001, r.cost);
  assert.ok(Math.abs(r.usd - 0.00114) < 0.0001, r.usd);
  assert.equal(r.text, '0.086 p per MH/s-hour');
  assert.ok(r.ratio > 9.5 && r.ratio < 11, r.ratio);
  assert.ok(r.sub.includes('Rented hash: USD 0.0117 per MH/s-hour'), r.sub);
  assert.ok(r.sub.includes('6 October 2026'), r.sub);
  assert.ok(r.verdict.startsWith('Yours is 10'), r.verdict);
  assert.ok(r.sub.includes('about USD 0.0011'), r.sub);
});

test('the cost-against-rent line says when rented hash undercuts the card, and what it needs when it cannot say', () => {
  // a 3080-class card at 12 GB and a dear tariff: 6 W per MH/s at 60 c (euro) = 0.36 c = USD 0.0039: still under the rent
  const under = V.rentLine([card({ power_w: 300, hash_now: 50 })], 60, 'de');
  assert.equal(under.kind, 'line');
  assert.ok(under.ratio > 2.5 && under.ratio < 3.5, under.ratio);
  // the day idle datacentre hash sets the rent at 0.00016 the line flips (the table in future.md 3.4): here, a card at
  // 20 W per MH/s on 100 c power = 2 c = USD 0.0216, dearer than the rent
  const dear = V.rentLine([card({ power_w: 400, hash_now: 20 })], 100, 'us');
  assert.equal(dear.kind, 'line');
  assert.ok(dear.ratio < 1, dear.ratio);
  assert.ok(dear.verdict.startsWith('Rented hash undercuts your card by'), dear.verdict);
  assert.equal(dear.sub.includes('about USD'), false, 'a dollar region needs no conversion');
  // no price: the W per MH/s and the ask
  const ask = V.rentLine([card()], 0, 'gb');
  assert.equal(ask.kind, 'price');
  assert.ok(ask.sub.includes('W per MH/s'), ask.sub);
  // not mining and never tuned: nothing to say, the rent still stated
  const none = V.rentLine([card({ state: 'off', hash_now: 0, power_w: 0 })], 26.32, 'gb');
  assert.equal(none.kind, 'none');
  assert.ok(none.sub.includes('USD 0.0117'));
  // not mining but tuned: the tune's point stands in, and the line says so
  const tuned = V.rentLine([card({ state: 'off', hash_now: 0, power_w: 0, sweep_watts: 290, sweep_mhs: 122.3 })], 26.32, 'gb');
  assert.equal(tuned.kind, 'line');
  assert.ok(tuned.sub.includes('(the last tune)'), tuned.sub);
});

test('the heat words: the strip line names the set point, the room, the share of the hour and the watts', () => {
  const heating = { on: true, phase: 'heating', set_c: 20, room_c: 19.6, room_source: 'typed', duty: 0.6, duty_hour: 0.55, heat_w: 410, full_w: 410, until_s: 300 };
  assert.deepEqual(V.heatLine(heating), { text: 'Heat mode: holding 20.0 °C · room 19.6 °C · heating 55% of the time · 410 W of heat', tone: 'heat' });
  const resting = { ...heating, phase: 'resting', heat_w: 0, room_source: 'card', room_c: 19.2 };
  assert.deepEqual(V.heatLine(resting), { text: 'Heat mode: holding 20.0 °C · room about 19 °C · heating 55% of the time · resting, 410 W when heating', tone: 'rest' });
  assert.equal(V.heatLine({ on: false }).text, '');
  assert.ok(V.heatLine({ ...heating, room_source: 'none', duty_hour: 0 }).text.includes('no room reading yet · heating 60% of the time'));
  assert.equal(V.heatRowWords(heating), 'heat 55% · 410 W of heat');
  assert.equal(V.heatRowWords(resting), 'heat 55% · resting');
  assert.equal(V.heatRowWords({ on: false }), '');
});

test('a resting card and the big button say heat mode, never a bare off', () => {
  const r = V.cardRow(card({ state: 'resting', hash_now: 0, message: 'resting: the room is 20.1 °C, holding 20.0 °C (heat mode)' }));
  assert.equal(r.word, 'resting (heat mode)');
  assert.equal(r.sub, 'resting: the room is 20.1 °C, holding 20.0 °C (heat mode)', 'the engine\'s line is the row\'s second line');
  assert.equal(r.hash, '');
  const m = { state: 'waiting', paused: false, cards: [card({ state: 'resting', hash_now: 0 })], found: [] };
  const t = V.toggle(m, { synced: true }, { heat: { on: true, phase: 'resting', set_c: 20, until_s: 240 } });
  assert.equal(t.label, 'Stop mining');
  assert.equal(t.sub, 'resting · heat mode holds 20.0 °C, heats again in 4 min');
  assert.equal(t.act, 'pause');
  assert.deepEqual(V.pill({ setup_done: true, mining: m, node: { state: 'synced' }, heat: { on: true, phase: 'resting' } }), { text: 'Resting · heat mode', tone: '' });
  const e = V.ember(card({ tune_before_watts: 0 }), { settings: { sweep: true }, heat: { on: true, phase: 'heating', duty: 0.5, duty_hour: 0.5, heat_w: 410 } }, 1);
  assert.equal(e.heat, 'heat 50% · 410 W of heat');
});
