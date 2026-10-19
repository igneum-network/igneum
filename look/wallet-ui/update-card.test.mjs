// node --test app/igneum-wallet/ui/update-card.test.mjs   (no dependencies)
// Loads ui/update-card.js (plain browser JS, run with `module` defined and no `document`) and checks what the card
// says for each update state, the release-note lines, and when it shows or waits (a send, Touch ID, a wallet flow).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'update-card.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const C = mod.exports;
const { model, decide, blocked, splitNotes, sentences, size } = C;

const NOTES = 'The update card: one centred card with Install now and Later. Touch ID confirms every send on the Mac; Windows Hello is written but untested. The coin and the chain line sit on the balance card. The version shows in the header and in Settings.';
const upd = (over) => ({ status: 'ready', version: '0.1.3', url: '', notes: NOTES, file: '', error: '', checked_at: 0, available: true, downloaded: true, ready: true, applying: false, progress: 1, size: 19_479_807, auto: true, wait: '', urgent: false, urgent_text: '', unsupported: false, min_supported: '', updated_from: '', rolled_back: '', ...over });
const quiet = { phase: 'home', view: 'overview', bioBusy: false, bioLine: false, bioName: 'Touch ID', quitting: false };

test('release notes: sentences, three lines under 70 characters, the rest behind What changed', () => {
  assert.deepEqual(sentences('one. two! three? four; five'), ['One.', 'Two!', 'Three?', 'Four', 'Five']);
  assert.deepEqual(sentences('v0.1.3 ships. 24 words.'), ['V0.1.3 ships.', '24 words.']);
  const n = splitNotes(NOTES);
  assert.equal(n.lines.length, 3);
  for (const l of n.lines) assert.ok(l.length < C.LINE_MAX, `${l.length}: ${l}`);
  assert.deepEqual(n.lines, ['The update card: one centred card with Install now and Later', 'Touch ID confirms every send on the Mac', 'Windows Hello is written but untested']);
  assert.equal(n.more, true); assert.equal(n.all.length, 5);
  assert.deepEqual(splitNotes('coin on the home screen, updates install by themselves'), { lines: ['Coin on the home screen, updates install by themselves'], more: false, all: ['Coin on the home screen, updates install by themselves'] });
  assert.deepEqual(splitNotes('').lines, []);
  assert.equal(size(19_479_807), '19 MB'); assert.equal(size(0), '');
});

test('what the card says: available, downloading with the ring, ready, installing, failed, manual, waiting', () => {
  const a = model(upd({ status: 'available', downloaded: false, ready: false, progress: 0 }), {});
  assert.equal(a.name, 'Igneum Wallet 0.1.3'); assert.equal(a.line, 'is available.'); assert.equal(a.key, 'update:0.1.3:pending');
  assert.equal(a.size, '19 MB'); assert.deepEqual(a.actions.map((x) => x.label), ['Install now', 'Later']);
  const d = model(upd({ status: 'downloading', downloaded: false, ready: false, progress: 0.43 }), {});
  assert.equal(d.line, 'is downloading.'); assert.equal(d.pct, 43); assert.equal(d.ring, 'progress'); assert.equal(d.key, a.key);
  const r = model(upd(), {});
  assert.equal(r.line, 'is ready to install.'); assert.equal(r.note, 'It installs by itself when nothing is being sent.'); assert.equal(r.key, 'update:0.1.3:ready');
  assert.equal(model(upd({ auto: false, wait: 'waiting for Install now (automatic updates are off)' }), {}).note, '');
  assert.equal(model(upd({ wait: 'a send is in flight; installing after it' }), {}).note, 'It installs by itself when nothing is being sent.');
  const i = model(upd({ status: 'applying', applying: true }), {});
  assert.equal(i.stage, 'installing'); assert.equal(i.line, 'Installing. The app restarts itself.'); assert.deepEqual(i.actions, []); assert.equal(i.dismissable, false);
  assert.equal(model(upd({ wait: 'installing now' }), {}).stage, 'installing');
  const f = model(upd({ status: 'error', error: 'sha256 mismatch: the file is not what the manifest signed\nsecond line', ready: false }), {});
  assert.equal(f.line, 'did not install.'); assert.equal(f.cause, 'sha256 mismatch: the file is not what the manifest signed');
  assert.deepEqual(f.actions.map((x) => x.label), ['Try again', 'Later']);
  assert.equal(model(upd({ status: 'error', error: 'did not stay up', rolled_back: '0.1.3: did not stay up' }), {}).line, 'did not stay up and was rolled back.');
  assert.equal(model(upd({ status: 'manual', ready: false }), {}).actions[0].label, 'Open the download');
  assert.equal(model(upd({ status: 'deferred' }), {}).line, 'is waiting for permission.');
  const u = model(upd({ status: 'downloading', progress: 0.2, urgent: true, urgent_text: 'This version is no longer supported. Installing 0.1.3 now.' }), {});
  assert.equal(u.dismissable, false); assert.deepEqual(u.actions.map((x) => x.label), ['Install now']);
  assert.equal(model(upd({ status: 'current', available: false }), {}), null);
  assert.equal(model(upd({ status: 'off', version: '' }), {}), null);
  assert.equal(model(upd({ status: 'error', error: 'no update manifest configured in this build' }), {}), null);
  assert.equal(model(null, {}), null);
});

test('deferred: the send screen, Touch ID on screen, a wallet being created or imported, quitting', () => {
  assert.equal(blocked(quiet), '');
  assert.equal(blocked({ ...quiet, view: 'send' }), 'the send screen is open');
  assert.equal(blocked({ ...quiet, bioBusy: true }), 'Touch ID is on screen');
  assert.equal(blocked({ ...quiet, bioLine: true, bioName: 'Windows Hello' }), 'Windows Hello is on screen');
  assert.equal(blocked({ ...quiet, phase: 'create' }), 'a wallet is being created');
  assert.equal(blocked({ ...quiet, phase: 'import' }), 'a wallet is being imported');
  assert.equal(blocked({ ...quiet, phase: 'unlock' }), '');
  assert.equal(blocked({ ...quiet, quitting: true }), 'the app is quitting');
  const a = model(upd({ status: 'available' }), {});
  assert.deepEqual(decide(a, { ...quiet, view: 'send' }, {}), { show: false, why: 'deferred', reason: 'the send screen is open' });
  assert.deepEqual(decide(a, { ...quiet, bioBusy: true }, {}), { show: false, why: 'deferred', reason: 'Touch ID is on screen' });
  // the block lifts: the card comes (it was queued, not dismissed)
  assert.equal(decide(a, quiet, { open: false, later: '', seen: '' }).show, true);
  assert.equal(decide(model(upd({ status: 'applying', applying: true }), {}), { ...quiet, view: 'send' }, { open: true }).show, true);
});

test('Later: hides this version at this stage; back for a newer version or a ready download with auto off', () => {
  const pend = model(upd({ status: 'downloading', progress: 0.5, ready: false }), {});
  const ready = model(upd(), {});
  const readyOff = model(upd({ auto: false }), {});
  assert.deepEqual(decide(pend, quiet, {}), { show: true, why: 'new' });
  const later = { open: false, later: pend.key, seen: '0.1.3' };
  assert.deepEqual(decide(pend, quiet, later), { show: false, why: 'later' });
  assert.deepEqual(decide(ready, quiet, later), { show: false, why: 'auto' });
  assert.deepEqual(decide(readyOff, quiet, later), { show: true, why: 'ready' });
  assert.deepEqual(decide(readyOff, quiet, { open: false, later: readyOff.key, seen: '0.1.3' }), { show: false, why: 'later' });
  assert.deepEqual(decide(model(upd({ status: 'available', version: '0.1.4' }), {}), quiet, later), { show: true, why: 'new' });
  assert.deepEqual(decide(ready, quiet, { open: false, later: '', seen: '' }), { show: true, why: 'ready' });
  const open = { open: true, later: '', seen: '0.1.3' };
  assert.equal(decide(pend, quiet, open).why, 'open'); assert.equal(decide(ready, quiet, open).why, 'open');
  assert.equal(decide(model(upd({ status: 'applying', applying: true }), {}), quiet, open).show, true);
  assert.equal(decide(model(upd({ status: 'error', error: 'x' }), {}), quiet, open).show, true);
  assert.deepEqual(decide(model(upd({ status: 'applying', applying: true }), {}), quiet, {}), { show: false, why: 'strip' });
  assert.deepEqual(decide(model(upd({ status: 'error', error: 'x' }), {}), quiet, {}), { show: false, why: 'strip' });
  const urgent = model(upd({ status: 'downloading', progress: 0.2, urgent: true, urgent_text: 'Unsupported.' }), {});
  assert.deepEqual(decide(urgent, quiet, { later: urgent.key }), { show: true, why: 'urgent' });
});
