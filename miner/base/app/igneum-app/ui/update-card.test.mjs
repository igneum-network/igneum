// node --test app/igneum-app/ui/update-card.test.mjs   (no dependencies; CI runs it in the site job)
// Loads the UpdateCard block at the top of app.js (plain browser JS, run with `module` defined and no `document`)
// and checks what the card says for each update state, the release-note lines, and when it shows or waits.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const C = mod.exports.UpdateCard;
const { model, decide, blocked, splitNotes, sentences, size } = C;

const NOTES = 'Difficulty rule v2 from DAA score 33,000 (about 18:35 UTC on 4 October); every node must carry it before then. The log drawer wraps long lines. Touch ID on the Mac. Windows Hello is written but untested.';
const upd = (over) => ({ available: true, version: '0.1.7', notes: NOTES, checked_at: 0, error: '', status: 'ready', downloaded: true, ready: true, applying: false, progress: 1, size: 20_588_331, auto: true, wait: '', urgent: false, urgent_text: '', activation_height: 0, unsupported: false, min_supported: '', channel: 'devnet', published_at: '', file: '', updated_from: '', rolled_back: '', ...over });
const quiet = { jobActive: false, uptime_s: 5000, quitting: false, sheetOpen: false };

test('release notes: sentences, three lines under 70 characters, the rest behind What changed', () => {
  assert.deepEqual(sentences('one. two! three? four; five'), ['One.', 'Two!', 'Three?', 'Four', 'Five']);
  assert.deepEqual(sentences('DAA score 33,000 is the switch. v2.1 ships.'), ['DAA score 33,000 is the switch.', 'V2.1 ships.']);
  assert.deepEqual(sentences('line one\nline two\n\n'), ['Line one', 'Line two']);
  assert.deepEqual(sentences(''), []);
  const n = splitNotes(NOTES);
  assert.equal(n.lines.length, 3);
  for (const l of n.lines) assert.ok(l.length < C.LINE_MAX, `${l.length}: ${l}`);
  assert.equal(n.lines[0], 'Difficulty rule v2 from DAA score 33,000 (about 18:35 UTC on 4…');
  assert.equal(n.lines[1], 'Every node must carry it before then');
  assert.equal(n.lines[2], 'The log drawer wraps long lines');
  assert.equal(n.more, true);
  assert.equal(n.all.length, 5);
  const short = splitNotes('Faster sync. Fewer restarts.');
  assert.deepEqual(short.lines, ['Faster sync', 'Fewer restarts']); assert.equal(short.more, false);
  assert.deepEqual(splitNotes('').lines, []); assert.equal(splitNotes('').more, false);
  assert.equal(size(20_588_331), '21 MB'); assert.equal(size(612_000), '0.6 MB'); assert.equal(size(0), '');
});

test('what the card says: available, downloading with the ring, ready, installing, failed, manual, waiting', () => {
  const a = model(upd({ status: 'available', downloaded: false, ready: false, progress: 0 }), {});
  assert.equal(a.name, 'Ember 0.1.7'); assert.equal(a.line, 'is available.'); assert.equal(a.stage, 'available');
  assert.equal(a.key, 'update:0.1.7:pending'); assert.equal(a.ring, 'none'); assert.equal(a.size, '21 MB');
  assert.deepEqual(a.actions.map((x) => x.label), ['Install now', 'Later']); assert.equal(a.dismissable, true);
  const d = model(upd({ status: 'downloading', downloaded: false, ready: false, progress: 0.43 }), {});
  assert.equal(d.line, 'is downloading.'); assert.equal(d.pct, 43); assert.equal(d.ring, 'progress'); assert.equal(d.key, a.key);
  assert.equal(model(upd({ status: 'staging', ready: false }), {}).key, a.key);
  const r = model(upd(), {});
  assert.equal(r.line, 'is ready to install.'); assert.equal(r.note, 'It installs by itself at a quiet moment.'); assert.equal(r.key, 'update:0.1.7:ready'); assert.equal(r.ring, 'full');
  assert.equal(model(upd({ auto: false, wait: 'waiting for Install now (automatic updates are off)' }), {}).note, '');
  assert.equal(model(upd({ wait: 'this version failed to install before; it waits for Install now' }), {}).note, 'It failed to install before.');
  const i = model(upd({ status: 'applying', applying: true }), {});
  assert.equal(i.stage, 'installing'); assert.equal(i.line, 'Installing. The app restarts itself.'); assert.deepEqual(i.actions, []); assert.equal(i.dismissable, false); assert.equal(i.ring, 'busy');
  assert.equal(model(upd({ wait: 'installing now' }), {}).stage, 'installing');
  const f = model(upd({ status: 'error', error: 'sha256 mismatch: the file is not what the manifest signed\nsecond line', ready: false }), {});
  assert.equal(f.stage, 'failed'); assert.equal(f.line, 'did not install.'); assert.equal(f.cause, 'sha256 mismatch: the file is not what the manifest signed');
  assert.deepEqual(f.actions.map((x) => x.label), ['Try again', 'Later']); assert.equal(f.key, 'update:0.1.7:failed');
  assert.equal(model(upd({ status: 'error', error: 'did not stay up', rolled_back: '0.1.7: did not stay up' }), {}).line, 'did not stay up and was rolled back.');
  const m = model(upd({ status: 'manual', ready: false, wait: '/Applications is not writable' }), {});
  assert.equal(m.line, 'is downloaded.'); assert.equal(m.actions[0].label, 'Open the download');
  assert.equal(model(upd({ status: 'deferred' }), {}).line, 'is waiting for permission.');
  // urgent: no Later, the engine's words as the note
  const u = model(upd({ status: 'downloading', progress: 0.2, urgent: true, urgent_text: 'Consensus upgrade at height 120000. Installing now.' }), {});
  assert.equal(u.dismissable, false); assert.deepEqual(u.actions.map((x) => x.label), ['Install now']); assert.equal(u.note, 'Consensus upgrade at height 120000. Installing now.');
  // nothing to show
  assert.equal(model(upd({ status: 'current', available: false }), {}), null);
  assert.equal(model(upd({ status: 'checking' }), {}), null);
  assert.equal(model(upd({ status: 'error', error: 'no update manifest configured in this build' }), {}), null);
  // a release before its update pointer is published (the mini's 0.2.1 install, 12 October 2026): nothing failed
  assert.equal(model(upd({ status: 'error', error: 'no manifest at the update URL yet' }), {}), null);
  assert.equal(model(upd({ status: 'error', version: '', error: 'manifest: connection refused' }), {}), null);
  assert.equal(model(null, {}), null);
});

test('deferred: a running job, the first 60 s, the key sheet, quitting; installing is never hidden', () => {
  assert.equal(blocked(quiet), '');
  assert.equal(blocked({ ...quiet, jobActive: true }), 'a job is running');
  assert.equal(blocked({ ...quiet, uptime_s: 59 }), 'the engine started under a minute ago');
  assert.equal(blocked({ ...quiet, uptime_s: 60 }), '');
  assert.equal(blocked({ ...quiet, sheetOpen: true }), 'the key sheet is open');
  assert.equal(blocked({ ...quiet, quitting: true }), 'the app is quitting');
  const a = model(upd({ status: 'available' }), {});
  assert.deepEqual(decide(a, { ...quiet, jobActive: true }, {}), { show: false, why: 'deferred', reason: 'a job is running' });
  assert.deepEqual(decide(a, { ...quiet, uptime_s: 10 }, {}), { show: false, why: 'deferred', reason: 'the engine started under a minute ago' });
  assert.equal(decide(a, quiet, {}).show, true);
  // the block lifts: the card comes (it was queued, not dismissed)
  assert.equal(decide(a, { ...quiet, uptime_s: 61 }, { open: false, later: '', seen: '' }).show, true);
  const i = model(upd({ status: 'applying', applying: true }), {});
  assert.equal(decide(i, { ...quiet, jobActive: true }, { open: true }).show, true);
});

test('Later: hides this version at this stage; back for a newer version or a ready download with auto off', () => {
  const pend = model(upd({ status: 'downloading', progress: 0.5, ready: false }), {});
  const ready = model(upd(), {});
  const readyOff = model(upd({ auto: false }), {});
  assert.deepEqual(decide(pend, quiet, {}), { show: true, why: 'new' });
  const later = { open: false, later: pend.key, seen: '0.1.7' };
  assert.deepEqual(decide(pend, quiet, later), { show: false, why: 'later' });
  // auto on: the ready download installs by itself, the strip says so, no second card
  assert.deepEqual(decide(ready, quiet, later), { show: false, why: 'auto' });
  // auto off: the ready download needs a click, so the card returns once
  assert.deepEqual(decide(readyOff, quiet, later), { show: true, why: 'ready' });
  assert.deepEqual(decide(readyOff, quiet, { open: false, later: readyOff.key, seen: '0.1.7' }), { show: false, why: 'later' });
  // a newer version is a new key
  const newer = model(upd({ status: 'available', version: '0.1.8' }), {});
  assert.deepEqual(decide(newer, quiet, later), { show: true, why: 'new' });
  // ready first seen with auto on (the download happened while the card could not open): shown once
  assert.deepEqual(decide(ready, quiet, { open: false, later: '', seen: '' }), { show: true, why: 'ready' });
  // an open card follows its update: downloading, ready, installing, failed
  const open = { open: true, later: '', seen: '0.1.7' };
  assert.equal(decide(pend, quiet, open).why, 'open'); assert.equal(decide(ready, quiet, open).why, 'open');
  assert.equal(decide(model(upd({ status: 'applying', applying: true }), {}), quiet, open).show, true);
  assert.equal(decide(model(upd({ status: 'error', error: 'x' }), {}), quiet, open).show, true);
  // installing and failed without an open card: the strip has them
  assert.deepEqual(decide(model(upd({ status: 'applying', applying: true }), {}), quiet, {}), { show: false, why: 'strip' });
  assert.deepEqual(decide(model(upd({ status: 'error', error: 'x' }), {}), quiet, {}), { show: false, why: 'strip' });
  // urgent cannot be dismissed
  const urgent = model(upd({ status: 'downloading', progress: 0.2, urgent: true, urgent_text: 'Consensus upgrade.' }), {});
  assert.deepEqual(decide(urgent, quiet, { later: urgent.key }), { show: true, why: 'urgent' });
  assert.deepEqual(decide(null, quiet, {}), { show: false, why: 'none' });
});
