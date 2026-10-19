// The class v5 tiers table (app/igneum-app/tiers/class-v5-tiers.json): the loader and the validator the test and the
// app's packaging read it through. The rows are in the shape src/ember.rs tier_from_json reads back from a card's state
// (id, clock_mhz, power_pct, mem_mhz, limit_w, mhs, w), so a card whose own search has not run can be set to a tier
// from this table by /api/tune/tier, and the search replaces the row when it runs. node --test class-v5-tiers.test.mjs
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

export const TIER_IDS = ['efficiency', 'balanced', 'max'];
export const LABELS = ['measured', 'estimated'];
export const SOURCES = ['measured', 'stock'];
export const VENDORS = ['nvidia', 'amd', 'apple', 'intel'];
// the fields tier_from_json reads, every one a number
export const ROW_FIELDS = ['clock_mhz', 'power_pct', 'mem_mhz', 'limit_w', 'mhs', 'w'];
// the brief's card classes (floor lane 4, 8 October 2026): every one must have an entry
export const REQUIRED_CARDS = ['5090', '5080', '5070 Ti', '5070', '5060 Ti', '5060', '4090', '4080', '4070', '3090', '3080', '3070', '3060',
  '9070 XT', '7900 XTX', '7800 XT', '7600 XT', '9060 XT', 'H100', 'L40S', 'A100', 'B580', 'A750', 'M5 Max', 'M4 Max', 'M4 Pro', 'M3 Max'];

export function loadTable(path) {
  const p = path || join(dirname(fileURLToPath(import.meta.url)), 'class-v5-tiers.json');
  return JSON.parse(readFileSync(p, 'utf8'));
}

/** Every fault in the table as a list of strings; an empty list is a valid table. */
export function validate(t) {
  const faults = [];
  const f = (s) => faults.push(s);
  if (t.class !== 'v5') f(`class is ${t.class}, not v5`);
  if (!Array.isArray(t.tier_ids) || t.tier_ids.join() !== TIER_IDS.join()) f('tier_ids must be efficiency, balanced, max');
  for (const k of ['stale_when', 'on_flip', 'measured_flip', 'period']) if (!t.remeasure_rule || typeof t.remeasure_rule[k] !== 'string' || !t.remeasure_rule[k]) f(`remeasure_rule.${k} missing`);
  if (!t.v5_over_v4 || typeof t.v5_over_v4.watts_pct !== 'number') f('v5_over_v4.watts_pct missing');
  if (!Array.isArray(t.cards) || t.cards.length === 0) { f('cards missing'); return faults; }
  const seen = new Set();
  for (const c of t.cards) {
    const name = c.card || '(unnamed)';
    if (seen.has(name)) f(`${name}: listed twice`);
    seen.add(name);
    if (!VENDORS.includes(c.vendor)) f(`${name}: vendor ${c.vendor}`);
    if (!LABELS.includes(c.label)) f(`${name}: label ${c.label}`);
    if (!Array.isArray(c.match) || c.match.length === 0) f(`${name}: no match list`);
    if (typeof c.src !== 'string' || !c.src) f(`${name}: no src`);
    if (!c.stock || typeof c.stock.uj !== 'number' || c.stock.uj <= 0) f(`${name}: stock.uj missing`);
    if (!Array.isArray(c.tiers) || c.tiers.length === 0) { f(`${name}: no tiers`); continue; }
    const ids = c.tiers.map((r) => r.id);
    for (const id of ids) if (!TIER_IDS.includes(id)) f(`${name}: tier id ${id}`);
    if (new Set(ids).size !== ids.length) f(`${name}: a tier id repeats`);
    const lever = c.vendor === 'nvidia' || c.vendor === 'amd';
    if (lever && ids.join() !== TIER_IDS.join()) f(`${name}: a card with a lever carries all three tiers in order`);
    if (!lever && ids.join() !== 'max') f(`${name}: a card with no lever carries the max tier only`);
    for (const r of c.tiers) {
      for (const k of ROW_FIELDS) if (typeof r[k] !== 'number' || !Number.isFinite(r[k])) f(`${name}/${r.id}: ${k} is not a number`);
      if (typeof r.power_pct === 'number' && (r.power_pct < 50 || r.power_pct > 100)) f(`${name}/${r.id}: power_pct ${r.power_pct} outside 50 to 100`);
      if (typeof r.clock_mhz === 'number' && (r.clock_mhz < 0 || r.clock_mhz > 4000)) f(`${name}/${r.id}: clock_mhz ${r.clock_mhz}`);
      if (!LABELS.includes(r.label)) f(`${name}/${r.id}: label ${r.label}`);
      if (!SOURCES.includes(r.source)) f(`${name}/${r.id}: source ${r.source}`);
      if (typeof r.uj !== 'number' || r.uj <= 0) f(`${name}/${r.id}: uj missing`);
      if (typeof r.note !== 'string' || !r.note) f(`${name}/${r.id}: no note`);
      if (r.mhs > 0 && r.w > 0 && r.uj > 0 && Math.abs(r.w / r.mhs - r.uj) / r.uj > 0.025) f(`${name}/${r.id}: uj ${r.uj} is not w over mhs (${(r.w / r.mhs).toFixed(2)})`);
      if (r.mhs > 0 && r.w > 0 && typeof r.mhw === 'number' && Math.abs(r.mhs / r.w - r.mhw) / r.mhw > 0.025) f(`${name}/${r.id}: mhw ${r.mhw} is not mhs over w`);
      if (r.id === 'max' && r.source !== 'stock') f(`${name}/max: the max tier is the stock row`);
      if (r.id === 'max' && (r.clock_mhz !== 0 || r.power_pct !== 100)) f(`${name}/max: stock is unlocked at 100 percent`);
      if (c.vendor === 'amd' && (typeof r.core_offset_mhz !== 'number' || typeof r.power_offset_pct !== 'number')) f(`${name}/${r.id}: an AMD row carries core_offset_mhz and power_offset_pct`);
      if (c.vendor === 'amd' && r.core_offset_mhz > 0) f(`${name}/${r.id}: an AMD core offset never raises the clock`);
    }
    const by = Object.fromEntries(c.tiers.map((r) => [r.id, r]));
    if (by.efficiency && by.max && by.efficiency.uj > by.max.uj) f(`${name}: efficiency costs more per hash than stock`);
    if (by.efficiency && by.balanced && by.balanced.uj + 1e-9 < by.efficiency.uj) f(`${name}: balanced is cheaper per hash than efficiency`);
    if (by.balanced && by.max && by.balanced.mhs > 0 && by.max.mhs > 0 && by.balanced.mhs < by.max.mhs * 0.98) f(`${name}: balanced gives up over 2 percent of the top rate`);
    if (c.label === 'measured' && !c.tiers.some((r) => r.label === 'measured' && r.source === 'measured') && lever) f(`${name}: a measured card has no measured tuned row`);
  }
  for (const want of REQUIRED_CARDS) if (!t.cards.some((c) => c.card.includes(want))) f(`no entry for ${want}`);
  return faults;
}

/** The entry a card name matches (the first whose match list hits; the longer match wins, so "5070 Ti" beats "5070"). */
export function entryFor(t, cardName) {
  const n = cardName.toUpperCase();
  let best = null, bestLen = 0;
  for (const c of t.cards) for (const m of c.match) if (n.includes(m.toUpperCase()) && m.length > bestLen) { best = c; bestLen = m.length; }
  return best;
}

/** The tier row a card starts from, in the shape tier_from_json reads; null when the table has none. */
export function tierRow(t, cardName, id) {
  const c = entryFor(t, cardName);
  if (!c) return null;
  return c.tiers.find((r) => r.id === id) || null;
}

/** The re-measure verdict the app applies on a class flip (src/ember.rs tiers_stale): stale when both classes are known and differ. */
export function stale(tiersClass, programClass) {
  return Boolean(tiersClass) && Boolean(programClass) && tiersClass !== programClass;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const t = loadTable();
  const faults = validate(t);
  if (faults.length) { console.error(faults.join('\n')); process.exit(1); }
  console.log(`class ${t.class}: ${t.cards.length} card classes, ${t.cards.filter((c) => c.label === 'measured').length} measured`);
}
