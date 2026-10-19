// node --test app/igneum-app/ui/tune-line.test.mjs   (no dependencies; CI runs it in the site job)
// The card row's Ember Tune line (app.js TuneLine): what a user sees per state, from the card state fields.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const { model, point } = mod.exports.TuneLine;
const View = mod.exports.View;
const tuneNotice = mod.exports.tuneNotice;
const NOW = 1_800_000_000;
const card = over => ({ vendor: 'nvidia', sweep_state: 'idle', sweep_note: '', sweep_pct: 0, power_pct: 80, sweep_at: 0, tune_line: '', tune_source: '', tune_clock_mhz: 0, tune_control: true, pinned: false, ...over });

test('tuned: the line the brief asks for, with the point, the source and when', () => {
  const m = model(card({ tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'full', tune_clock_mhz: 2470, sweep_pct: 100, sweep_at: NOW - 3600 }), NOW);
  assert.equal(m.kind, 'tuned');
  assert.equal(m.text, 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)');
  assert.equal(m.note, '2470 MHz at 100%, full tune, 1 h ago');
  const c = model(card({ tune_line: 'Tuned: 17.7 MH/s at 177 W (0.100 MH/W)', tune_source: 'confirm', sweep_pct: 90, sweep_at: NOW - 120, vendor: 'amd' }), NOW);
  assert.equal(c.note, '90%, clock unlocked, from the fleet prior, confirmed, 2 min ago');
  const p = model(card({ tune_line: 'Tuned: 1 MH/s at 1 W (1.000 MH/W)', tune_source: 'full', sweep_pct: 70, pinned: true }), NOW);
  assert.match(p.note, /your setting stays pinned$/);
});

test('measure only: Apple and NVIDIA without Power control say so beside the measured line', () => {
  const a = model(card({ vendor: 'apple', tune_control: false, tune_line: 'Tuned: 26.7 MH/s at 38 W (0.703 MH/W)', tune_source: 'baseline', sweep_note: 'measure only on Apple silicon: the system sets the clocks and the power; no control exposed', sweep_at: NOW - 60 }), NOW);
  assert.equal(a.kind, 'measured');
  assert.equal(a.text, 'Tuned: 26.7 MH/s at 38 W (0.703 MH/W) (measured as it runs, 1 min ago)');
  assert.match(a.note, /^measure only on Apple silicon/);
  const n = model(card({ tune_control: false, tune_line: 'Tuned: 122.3 MH/s at 290 W (0.422 MH/W)', tune_source: 'baseline', sweep_note: 'measure only until Power control is on in Settings (Windows asks for administrator rights once)' }), NOW);
  assert.equal(n.kind, 'measured');
  assert.match(n.note, /Power control/);
});

test('running, stopped, idle and off', () => {
  assert.deepEqual(model(card({ sweep_state: 'running', sweep_note: 'tuning: holding 2472 MHz · 100% · 41 s (step 7 of 9)' }), NOW), { kind: 'running', text: 'tuning: holding 2472 MHz · 100% · 41 s (step 7 of 9)' });
  assert.equal(model(card({ sweep_note: 'tuning stopped: a remote job took the GPU' }), NOW).kind, 'stopped');
  assert.equal(model(card({}), NOW).text, 'tuning: not run yet (starts after 120 s of steady mining)');
  assert.equal(model(card({ sweep_note: 'tuning: waits for 120 s of steady mining' }), NOW).text, 'tuning: waits for 120 s of steady mining');
  assert.equal(model(card({ vendor: 'other' }), NOW).kind, 'off');
  assert.equal(point({ tune_clock_mhz: 0, sweep_pct: 0, power_pct: 0 }), '');
});

test('a card never shows 0 MH/s without a reason word: tuning, held and tuned rows, the button, the strip', () => {
  const base = { key: 'nvidia:0:x', name: 'RTX 5090', vendor: 'nvidia', kind: 'discrete', enabled: true, state: 'tuning', hash_now: 123.4, power_w: 219.6, tune_step: 7, tune_steps: 9, tune_eta_s: 230, tune_plan: 'full', sweep_note: 'tuning: holding 2472 MHz · 100% · 41 s (step 7 of 9)', vram_mb: 32768 };
  const r = View.cardRow(base);
  assert.equal(r.word, 'tuning: step 7 of 9 · measuring 220 W');
  assert.equal(r.hash, '123', 'the live rate stays in the rate column');
  assert.equal(r.tone, 'on');
  const h = View.cardRow({ ...base, state: 'held', hash_now: 0, power_w: 0, message: 'held for a remote job: Ember Tune on the desk machine', tune_steps: 0 });
  assert.equal(h.word, 'held for a remote job: Ember Tune on the desk machine');
  assert.equal(h.hash, '', 'blank, never 0');
  const off = View.cardRow({ ...base, state: 'off', enabled: false, hash_now: 0 });
  assert.equal(off.word, 'off'); assert.equal(off.hash, '');
  // the big button during a tune
  const t = View.toggle({ cards: [base], state: 'mining', paused: false }, { synced: true }, {});
  assert.equal(t.label, 'Tuning'); assert.equal(t.disabled, true);
  assert.equal(t.sub, 'mining again in about 4 min · RTX 5090');
  const held = View.toggle({ cards: [{ ...base, state: 'held' }], state: 'waiting', paused: false }, { synced: true }, {});
  assert.equal(held.label, 'Held'); assert.equal(held.disabled, true);
  // the strip
  const n = tuneNotice([base]);
  assert.equal(n.kind, 'tuning');
  assert.equal(n.text, 'Tuning RTX 5090: the full tune, step 7 of 9. Mining again in about 4 min.');
  assert.equal(tuneNotice([{ ...base, state: 'mining' }]), null);
  // the finished row: TuneLine's tuned model (the table's "Tuned: ..." line with the point)
  const done = model({ ...base, state: 'mining', sweep_state: 'idle', tune_line: 'Tuned: 123.4 MH/s at 220 W (0.561 MH/W)', tune_source: 'full', tune_clock_mhz: 2470, sweep_pct: 100, sweep_at: NOW - 60, tune_control: true }, NOW);
  assert.equal(done.kind, 'tuned');
  assert.equal(done.text, 'Tuned: 123.4 MH/s at 220 W (0.561 MH/W)');
  assert.match(done.note, /^2470 MHz at 100%, full tune, 1 min ago/);
  assert.equal(View.tuneEta(30), 'under a minute');
  assert.equal(View.tuneEta(700), 'about 12 min');
});

test('Ember 2: £ a day from watts and pence per kWh', () => {
  assert.equal(mod.exports.TuneLine.pounds(310, 28.5), '£2.12');
  assert.equal(mod.exports.TuneLine.pounds(102.7, 28.5), '£0.70');
});
