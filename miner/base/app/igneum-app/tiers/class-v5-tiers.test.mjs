// node --test app/igneum-app/tiers/class-v5-tiers.test.mjs
// The class v5 tiers table: the shipped file validates; the measured rows carry the record's numbers; the match rule
// picks the longer name; a class flip reads stale; and the known-failed cases (a bad power rung, a missing field, a
// tuned row dearer than stock, a card class left out) are refused, so a stale or broken table cannot pass.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadTable, validate, entryFor, tierRow, stale, REQUIRED_CARDS, ROW_FIELDS } from './class-v5-tiers.mjs';

const clone = (t) => JSON.parse(JSON.stringify(t));

test('the shipped table validates with no faults', () => {
  const t = loadTable();
  assert.deepEqual(validate(t), []);
  assert.equal(t.class, 'v5');
  assert.ok(t.cards.length >= REQUIRED_CARDS.length);
});

test('the measured rows are the record\'s (the 5090 at its 1,300 MHz knee, the 5080 at 1,100, the 4070 at its tune, the 9070 XT grid, the M5 Max meter)', () => {
  const t = loadTable();
  const r5090 = tierRow(t, 'NVIDIA GeForce RTX 5090', 'balanced');
  assert.equal(r5090.clock_mhz, 1300);
  assert.equal(r5090.label, 'measured');
  assert.ok(Math.abs(r5090.uj - 2.38) < 0.01, `the 5090 balanced row reads 2.38 microjoules under class v5: ${r5090.uj}`);
  const e5090 = tierRow(t, 'NVIDIA GeForce RTX 5090', 'efficiency');
  assert.equal(e5090.clock_mhz, 1200);
  const r5080 = tierRow(t, 'NVIDIA GeForce RTX 5080', 'efficiency');
  assert.equal(r5080.clock_mhz, 1100);
  assert.ok(Math.abs(r5080.uj - 2.06) < 0.01);
  const r4070 = tierRow(t, 'NVIDIA GeForce RTX 4070', 'efficiency');
  assert.equal(r4070.clock_mhz, 1860);
  assert.equal(r4070.power_pct, 50);
  const amd = tierRow(t, 'AMD Radeon RX 9070 XT', 'efficiency');
  assert.equal(amd.core_offset_mhz, -500);
  assert.equal(amd.power_offset_pct, -30);
  assert.ok(Math.abs(amd.w - 149.3) < 0.01);
  const apple = entryFor(t, 'Apple M5 Max');
  assert.equal(apple.tiers.length, 1);
  assert.equal(apple.tiers[0].id, 'max');
  assert.ok(Math.abs(apple.tiers[0].uj - 1.40) < 0.01);
});

test('every entry carries the three tiers where the card has a lever, and only max where it has none', () => {
  const t = loadTable();
  for (const c of t.cards) {
    const ids = c.tiers.map((r) => r.id).join();
    if (c.vendor === 'nvidia' || c.vendor === 'amd') assert.equal(ids, 'efficiency,balanced,max', c.card);
    else assert.equal(ids, 'max', c.card);
    for (const r of c.tiers) for (const k of ROW_FIELDS) assert.equal(typeof r[k], 'number', `${c.card}/${r.id}.${k}`);
  }
});

test('the match rule takes the longer name: a 5070 Ti is not a 5070, a 4060 Ti is not a 4060, a 9060 XT is not a 9070 XT', () => {
  const t = loadTable();
  assert.equal(entryFor(t, 'NVIDIA GeForce RTX 5070 Ti').card, 'NVIDIA GeForce RTX 5070 Ti');
  assert.equal(entryFor(t, 'NVIDIA GeForce RTX 5070').card, 'NVIDIA GeForce RTX 5070');
  assert.equal(entryFor(t, 'NVIDIA GeForce RTX 4060 Ti').card, 'NVIDIA GeForce RTX 4060 Ti');
  assert.equal(entryFor(t, 'NVIDIA GeForce RTX 4060').card, 'NVIDIA GeForce RTX 4060');
  assert.equal(entryFor(t, 'AMD Radeon RX 9060 XT').card, 'AMD Radeon RX 9060 XT');
  assert.equal(entryFor(t, 'amd:gfx1201').card, 'AMD Radeon RX 9070 XT');
  assert.equal(entryFor(t, 'NVIDIA GeForce GTX 1080 Ti'), null);
  assert.equal(tierRow(t, 'NVIDIA GeForce GTX 1080 Ti', 'max'), null);
});

test('a class flip reads stale exactly as src/ember.rs tiers_stale does', () => {
  assert.equal(stale('v4', 'v5'), true);
  assert.equal(stale('v5', 'v5'), false);
  assert.equal(stale('', 'v5'), false);
  assert.equal(stale('v4', ''), false);
  const t = loadTable();
  assert.ok(t.remeasure_rule.measured_flip.includes('0.0 percent of rate'));
  assert.equal(t.v5_over_v4.watts_pct, 2.0);
});

test('known-failed: a power rung under 50 is refused', () => {
  const t = clone(loadTable());
  t.cards[0].tiers[0].power_pct = 40;
  assert.ok(validate(t).some((s) => s.includes('power_pct 40')));
});

test('known-failed: a row missing a field tier_from_json reads is refused', () => {
  const t = clone(loadTable());
  delete t.cards[1].tiers[1].limit_w;
  assert.ok(validate(t).some((s) => s.includes('limit_w is not a number')));
});

test('known-failed: a tuned row dearer per hash than stock is refused, and a max tier that is not stock', () => {
  const t = clone(loadTable());
  const c = t.cards.find((x) => x.card.includes('4080'));
  c.tiers[0].uj = c.tiers[2].uj + 1; c.tiers[0].w = c.tiers[0].uj * c.tiers[0].mhs; c.tiers[0].mhw = c.tiers[0].mhs / c.tiers[0].w;
  assert.ok(validate(t).some((s) => s.includes('efficiency costs more per hash than stock')));
  const u = clone(loadTable());
  u.cards[0].tiers[2].clock_mhz = 1500;
  assert.ok(validate(u).some((s) => s.includes('stock is unlocked at 100 percent')));
});

test('known-failed: a card class of the brief left out is refused, and a stale uj (not w over mhs) is refused', () => {
  const t = clone(loadTable());
  t.cards = t.cards.filter((c) => !c.card.includes('3060'));
  assert.ok(validate(t).some((s) => s === 'no entry for 3060'));
  const u = clone(loadTable());
  u.cards[0].tiers[1].uj = 9.99;
  assert.ok(validate(u).some((s) => s.includes('is not w over mhs')));
});

test('known-failed: a table under another class is refused', () => {
  const t = clone(loadTable());
  t.class = 'v4';
  assert.ok(validate(t).some((s) => s.includes('not v5')));
});
