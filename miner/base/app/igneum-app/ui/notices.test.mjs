// node --test app/igneum-app/ui/notices.test.mjs   (no dependencies; CI runs it in the site job)
// Loads the Notices block at the top of app.js (plain browser JS: the file is run with `module` defined and no
// `document`, so only the pure block executes) and checks the ordering, the keys, the wording and the timers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const N = mod.exports;
const { updateNotice, jobNotice, clockNotice, cardNotices, engineNotice, gather, pick } = N;

const NOW = 1_800_000_000;
const upd = (over) => ({ available: true, version: '0.1.6', notes: '', checked_at: NOW - 60, error: '', status: 'ready', downloaded: true, ready: true, applying: false, progress: 1, size: 20_588_331, auto: true, wait: 'installs at the next safe moment', urgent: false, urgent_text: '', activation_height: 0, unsupported: false, min_supported: '', channel: 'devnet', published_at: '', file: '', updated_from: '', rolled_back: '', ...over });
const run = (over) => ({ allowed: true, active: true, id: 'job-7', kind: 'shard-benchmark', title: 'shard benchmark', stage: 'proving shard 3 of 8', message: 'some output line', started_at: NOW - 400, results: ['STAGE prove', 'RESULT shard=1 prove_s=41.2', 'RESULT shard=2 prove_s=39.8'], last: {}, last_results: [], history: [], ...over });
const fin = (status, over) => ({ allowed: true, active: false, id: '', last: { id: 'job-6', kind: 'build', title: 'build', status, started_at: NOW - 2000, finished_at: NOW - 60, exit: status === 'done' ? 0 : 1, run_id: 'r', uploaded: status === 'done', summary: status === 'done' ? 'build finished, exit 0' : 'build exited with code 1' }, last_results: status === 'done' ? ['RESULT ok'] : ['STAGE build node', 'BUILD FAILED: error[E0425]: cannot find value `foo`'], history: [], ...over });
const s = (over) => ({ version: '0.1.5', uptime_s: 5000, now: NOW, quitting: false, update: upd({ status: 'current', available: false, ready: false, downloaded: false }), jobs: { active: false, last: {} }, clock: { severity: 'none' }, ...over });

test('ordering: installing > job failed > clock > job running > update available > job done > updated', () => {
  const all = [
    updateNotice(upd({ status: 'applying', applying: true }), s()),
    jobNotice(fin('failed'), NOW),
    clockNotice({ severity: 'warn', message: 'Clock 40 s ahead.', hint: 'Settings' }),
    jobNotice(run(), NOW),
    updateNotice(upd(), s()),
    jobNotice(fin('done'), NOW),
    updateNotice(upd({ status: 'current', updated_from: '0.1.4' }), s({ uptime_s: 10 })),
  ];
  const kinds = ['update-installing', 'job-failed', 'clock', 'job-running', 'update-available', 'job-done', 'update-installed'];
  assert.deepEqual(all.map((n) => n.kind), kinds);
  // the levels climb, and shuffling the list changes nothing: the lowest level wins
  for (let i = 1; i < all.length; i++) assert.ok(all[i].level > all[i - 1].level, `${all[i].kind} after ${all[i - 1].kind}`);
  const shuffled = [all[5], all[2], all[6], all[0], all[3], all[1], all[4]];
  for (let i = 0; i < kinds.length; i++) {
    const rest = shuffled.filter((n) => kinds.indexOf(n.kind) >= i);
    assert.equal(pick(rest, {}).kind, kinds[i]);
  }
  // urgent and failed updates sit with installing at the top
  assert.equal(updateNotice(upd({ urgent: true, urgent_text: 'Consensus upgrade at height 120000.' }), s()).level, 0);
  assert.equal(updateNotice(upd({ status: 'error', error: 'sha256 mismatch' }), s()).level, 0);
  // no manifest published yet is not a failure: no banner, nothing to try (the mini's first screen, 12 October 2026)
  assert.equal(updateNotice(upd({ status: 'error', error: 'no manifest at the update URL yet' }), s()), null);
});

test('a closed key hides that notice and the next one waits its turn', () => {
  const list = [jobNotice(fin('failed'), NOW), jobNotice(run({ id: 'job-8' }), NOW)];
  assert.equal(pick(list, {}).kind, 'job-failed');
  const dismissed = { 'job:failed:job-6': true };
  assert.equal(pick(list, dismissed).kind, 'job-running');
  assert.equal(pick([list[0]], dismissed), null);
  // a new job id is a new key, so it shows again
  const again = jobNotice(fin('failed', { last: { ...fin('failed').last, id: 'job-9' } }), NOW);
  assert.equal(pick([again], dismissed).kind, 'job-failed');
});

test('update keys: Later on the download hides it until it is ready; Later on ready hides that version', () => {
  const avail = updateNotice(upd({ status: 'available', ready: false, downloaded: false, progress: 0 }), s());
  const down = updateNotice(upd({ status: 'downloading', ready: false, downloaded: false, progress: 0.43 }), s());
  const stage = updateNotice(upd({ status: 'staging', ready: false }), s());
  const ready = updateNotice(upd(), s());
  assert.equal(avail.key, down.key); assert.equal(down.key, stage.key); assert.notEqual(stage.key, ready.key);
  const dismissed = { [avail.key]: true };
  assert.equal(pick([down], dismissed), null);
  assert.equal(pick([ready], dismissed).kind, 'update-available');
  // a different version is a different key
  assert.notEqual(updateNotice(upd({ version: '0.1.7' }), s()).key, ready.key);
  // percent changes do not change the key
  assert.equal(updateNotice(upd({ status: 'downloading', progress: 0.9 }), s()).key, down.key);
});

test('update wording: available, downloading with percent, installing, failed with one line of cause, updated', () => {
  const avail = updateNotice(upd({ status: 'available' }), s());
  assert.equal(avail.text, 'Ember 0.1.6 is available.');
  assert.deepEqual(avail.actions.map((a) => a.label), ['Install now', 'Later']);
  const down = updateNotice(upd({ status: 'downloading', progress: 0.43 }), s());
  assert.equal(down.text, 'Downloading Ember 0.1.6: 43%.');
  assert.equal(down.progress, 0.43);
  assert.equal(updateNotice(upd({ status: 'downloading', progress: 0 }), s()).text, 'Downloading Ember 0.1.6.');
  const inst = updateNotice(upd({ status: 'applying', applying: true }), s());
  assert.equal(inst.text, 'Installing Ember 0.1.6. The app restarts itself. Mining continues until then.');
  assert.equal(updateNotice(upd({ status: 'applying', applying: true }), s({ quitting: true })).text, 'Installing Ember 0.1.6. The app restarts itself in a moment.');
  // Install now on a ready update: the engine says "installing now" for up to 3 s before it flips to applying
  assert.equal(updateNotice(upd({ wait: 'installing now' }), s()).kind, 'update-installing');
  const err = updateNotice(upd({ status: 'error', error: 'sha256 mismatch: the file is not what the manifest signed\nsecond line' }), s());
  assert.equal(err.text, 'The update to 0.1.6 failed.');
  assert.equal(err.detail, 'sha256 mismatch: the file is not what the manifest signed');
  assert.deepEqual(err.actions.map((a) => a.label), ['Try again']);
  assert.equal(err.tone, 'bad');
  const rb = updateNotice(upd({ status: 'error', error: 'did not stay up', rolled_back: '0.1.6: did not stay up' }), s());
  assert.equal(rb.text, 'Ember 0.1.6 did not stay up and was rolled back.');
  const ready = updateNotice(upd(), s());
  assert.equal(ready.text, 'Ember 0.1.6 is ready. It installs by itself at a quiet moment.');
  assert.equal(updateNotice(upd({ auto: false, wait: 'waiting for Install now (automatic updates are off)' }), s()).text, 'Ember 0.1.6 is ready.');
  assert.equal(updateNotice(upd({ status: 'manual', wait: '/Applications is not writable; open the downloaded disk image and drag the app over the old one' }), s()).actions[0].label, 'Open the download');
  assert.equal(updateNotice(upd({ status: 'deferred' }), s()).text, 'Ember 0.1.6 is waiting for permission. It installs the next time someone is at this PC. Mining continues.');
  // updated: gone within 60 s of the new version starting
  const cur = upd({ status: 'current', available: false, ready: false, downloaded: false, updated_from: '0.1.4' });
  assert.match(updateNotice(cur, s({ version: '0.1.6', uptime_s: 59, now: 1_800_000_000 })).text, /^Updated to 0\.1\.6 at \d\d:\d\d\.$/);
  assert.equal(updateNotice(cur, s({ version: '0.1.6', uptime_s: 60 })), null);
  assert.equal(updateNotice(upd({ status: 'current', available: false }), s()), null);
  // a build with no manifest reports an error the user cannot act on: no notice (the Settings note still says it)
  assert.equal(updateNotice(upd({ status: 'error', error: 'no update manifest configured in this build' }), s()), null);
  assert.equal(updateNotice(upd({ status: 'checking', available: false }), s()), null);
});

test('job wording: running with minutes and stage, done goes after 5 minutes, failed stays with the first error line', () => {
  const r = jobNotice(run(), NOW);
  assert.equal(r.text, 'A job from the team is running: shard benchmark, 6 min. Proving shard 3 of 8.');
  assert.equal(r.detail, 'shard benchmark · RESULT shard=2 prove_s=39.8', 'the technical line goes behind the chevron');
  assert.equal(jobNotice(run({ stage: '' }), NOW).text, 'A job from the team is running: shard benchmark, 6 min.');
  const d = jobNotice(fin('done'), NOW);
  assert.equal(d.text, 'A job from the team ran: build, 32 min.');
  assert.equal(d.key, 'job:done:job-6');
  assert.ok(jobNotice(fin('done'), NOW - 60 + N.JOB_DONE_S));
  assert.equal(jobNotice(fin('done'), NOW - 60 + N.JOB_DONE_S + 1), null);
  const f = jobNotice(fin('failed'), NOW);
  assert.equal(f.text, 'A job from the team failed: build, after 32 min. The report did not upload.');
  assert.match(f.detail, /BUILD FAILED: error\[E0425\]: cannot find value `foo` · exit 1$/);
  assert.equal(f.tone, 'bad');
  assert.ok(jobNotice(fin('failed'), NOW + 86400 * 7), 'a failed job stays until closed');
  // no error line among the results: the summary is the cause
  assert.match(jobNotice(fin('failed', { last_results: ['STAGE build'] }), NOW).detail, /^build: build exited with code 1 · exit 1$/);
  assert.equal(jobNotice(fin('timeout'), NOW).text, 'A job from the team ran out of time: build, after 32 min. The report did not upload.');
  assert.equal(N.jobWord('', 'update-now-0313-switch-d937c69d'), 'update check');
  assert.equal(N.jobWord('restart', 'restart-miners-0312'), 'restart');
  assert.equal(N.jobWord('collect', ''), 'log collection');
  assert.equal(N.jobWord('', 'ember-tune-pc1-5'), 'tuning check');
  assert.equal(jobNotice({ active: false, last: {} }, NOW), null);
  assert.equal(jobNotice(null, NOW), null);
});

test('clock: the engine words, Sync clock, the hint; gather() keeps it off the dashboard and the job off the setup screens', () => {
  const c = clockNotice({ severity: 'block', message: 'This clock is 7 min behind.', hint: 'Date & Time', syncing: false });
  assert.equal(c.text, 'This clock is 7 min behind.'); assert.equal(c.detail, 'Date & Time'); assert.equal(c.tone, 'bad');
  assert.equal(c.actions[0].label, 'Sync clock'); assert.equal(c.actions[0].disabled, false);
  assert.equal(clockNotice({ severity: 'warn', message: 'x', syncing: true }).actions[0].label, 'Syncing');
  assert.equal(clockNotice({ severity: 'none' }), null);
  const st = s({ clock: { severity: 'warn', message: 'Clock 40 s ahead.' }, jobs: run(), update: upd({ status: 'available' }) });
  assert.deepEqual(gather(st, { dashboard: true }).map((n) => n.kind), ['update-available', 'job-running']);
  assert.deepEqual(gather(st, { dashboard: false }).map((n) => n.kind), ['update-available', 'clock']);
  assert.equal(pick(gather(st, { dashboard: true }), {}).kind, 'job-running');
  assert.equal(pick(gather(st, { dashboard: false }), {}).kind, 'clock');
  assert.deepEqual(gather({}, { dashboard: true }), []);
});

// hot-plug (src/hotplug.rs): the strip says what appeared or went, for 5 minutes, on every screen
const cardOf = (over) => ({ index: 0, key: 'nvidia:0:NVIDIA GeForce RTX 5090', kind: 'discrete', name: 'NVIDIA GeForce RTX 5090', vendor: 'nvidia', enabled: true, state: 'mining', problem: '', reason: '', message: '', added_at: 0, removed_at: 0, gone: false, ...over });
test('card notices: new card mining, new card not usable with the hint, removed card, nothing for the cards found at start', () => {
  const start = cardOf();
  assert.deepEqual(cardNotices([start], NOW), []);
  const added = cardOf({ key: 'amd:1:gfx1201', name: 'gfx1201', vendor: 'amd', added_at: NOW - 30, state: 'starting' });
  const [a] = cardNotices([start, added], NOW);
  assert.equal(a.kind, 'card-added'); assert.equal(a.level, N.LEVEL['job-done']); assert.equal(a.text, 'New card: gfx1201, mining.'); assert.equal(a.key, 'card:added:amd:1:gfx1201:' + (NOW - 30));
  const bad = cardOf({ key: 'amd::AMD Radeon RX 9070 XT', name: 'AMD Radeon RX 9070 XT', vendor: 'amd', enabled: false, state: 'unusable', problem: 'Code 43', message: 'not usable (Code 43)', reason: 'reboot with the card attached; if it persists, reinstall the driver with the card attached', added_at: NOW - 10 });
  const [b] = cardNotices([bad], NOW);
  assert.equal(b.text, 'AMD Radeon RX 9070 XT: not usable (Code 43). No worker runs on it.'); assert.equal(b.tone, 'warn'); assert.match(b.detail, /^reboot with the card attached/);
  const igpu = cardOf({ key: 'amd:0:gfx1036', name: 'gfx1036', vendor: 'amd', kind: 'integrated', enabled: false, state: 'off', added_at: NOW - 5 });
  assert.equal(cardNotices([igpu], NOW)[0].text, 'New card: gfx1036, off (integrated). Settings switches it on.');
  const removed = cardOf({ key: 'amd:1:gfx1201', name: 'gfx1201', vendor: 'amd', state: 'removed', removed_at: NOW - 60 });
  const [r] = cardNotices([removed], NOW);
  assert.equal(r.kind, 'card-removed'); assert.equal(r.text, 'Card removed: gfx1201. Its worker stopped.'); assert.equal(r.tone, 'warn');
  // the newest first; both go after CARD_S
  const two = cardNotices([added, removed], NOW);
  assert.equal(two[0].kind, 'card-added');
  assert.deepEqual(cardNotices([added, removed, bad], NOW + N.CARD_S + 1), []);
});

test('card notices sit under a running job and show on the setup screens too', () => {
  const added = cardOf({ key: 'amd:1:gfx1201', name: 'gfx1201', vendor: 'amd', added_at: NOW - 30 });
  const withJob = s({ jobs: run(), mining: { cards: [added] } });
  assert.equal(pick(gather(withJob, { dashboard: true }), {}).kind, 'job-running');
  const quiet = s({ mining: { cards: [added] } });
  assert.equal(pick(gather(quiet, { dashboard: true }), {}).kind, 'card-added');
  assert.equal(pick(gather(quiet, {}), {}).kind, 'card-added');
  // closed: stays closed for that key; a later event on the same card has a new key
  assert.equal(pick(gather(quiet, { dashboard: true }), { ['card:added:amd:1:gfx1201:' + (NOW - 30)]: true }), null);
});

test("F-E50: the engine's newest bad or warn notice is a strip line with its detail, beside a failed job, and gather carries it", () => {
  const pin = { level: 'bad', text: 'The pinned coordinate is not served.', detail: 'coordinate 5 is pinned but this source serves no coordinates; nothing mines until the pin is cleared or the source serves it' };
  const n = engineNotice([{ level: 'info', text: 'ember 0.2.13 started', detail: '' }, pin]);
  assert.equal(n.kind, 'engine-bad');
  assert.equal(n.tone, 'bad');
  assert.equal(n.text, 'The pinned coordinate is not served.');
  assert.equal(n.detail, pin.detail);
  assert.equal(n.key, 'engine:' + pin.text);
  assert.equal(n.level, N.LEVEL['job-failed']);
  assert.equal(engineNotice([{ level: 'info', text: 'a line', detail: '' }]), null);
  assert.equal(engineNotice([]), null);
  assert.equal(engineNotice(undefined), null);
  const w = engineNotice([{ level: 'warn', text: 'The template is under another bond\'s key.', detail: 'x' }]);
  assert.equal(w.kind, 'engine-warn');
  assert.equal(w.level, N.LEVEL['job-running']);
  // gather: the engine line rides s.mining.notices and wins over a running job
  const st = s({ mining: { cards: [], notices: [pin] }, jobs: run() });
  const got = gather(st, { dashboard: true });
  assert.ok(got.some((x) => x.kind === 'engine-bad'));
  assert.equal(pick(got, {}).kind, 'engine-bad');
  // a closed key hides it until the words change
  assert.notEqual(pick(got, { ['engine:' + pin.text]: true }).kind, 'engine-bad');
});
