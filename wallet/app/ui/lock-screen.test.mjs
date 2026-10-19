// node --test wallet/app/ui/lock-screen.test.mjs (carried from the previous wallet, OLD-BASE.md step 3)   (no dependencies)
// Loads ui/lock-screen.js (plain browser JS, run with `module` defined and no `document`) and checks what the lock
// screen shows, the line under the button after the host answered, and when the system sheet may be raised
// without a tap: once, on the first arrival after the person opened the app, with the setting on; never on an
// idle lock, a hand lock, the window coming back, a cancelled sheet, or the updater's relaunch.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'lock-screen.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const L = mod.exports;
const { layout, line, autoPrompt, idleLabel, shortAddress } = L;

const ADDR = '0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf';
const st = (over) => ({ phase: 'unlock', display: ADDR, biometric: { enrolled: true, available: true, kind: 'touchid' }, settings: { ask_on_open: true, idle_lock_min: 5 }, update: { updated_from: '' }, ...over });
const arrival = (over) => ({ reason: 'arrival', phase: 'unlock', touch: true, askOnOpen: true, hidden: false, busy: false, firstPhase: 'unlock', updatedFrom: '', ...over });

test('what the screen shows: the button when enrolled in the app window, else the password field first', () => {
  const a = layout(st(), true);
  assert.equal(a.touch, true); assert.equal(a.passwordPrimary, false); assert.equal(a.address, '0x7E5F45…395Bdf');
  // enrolled, but the page is open in a browser tab: no host to raise the sheet
  const b = layout(st(), false);
  assert.equal(b.touch, false); assert.equal(b.passwordPrimary, true);
  // not enrolled in the app window
  const c = layout(st({ biometric: { enrolled: false, available: true } }), true);
  assert.equal(c.touch, false); assert.equal(c.passwordPrimary, true);
  assert.deepEqual(layout(null, true), { touch: false, passwordPrimary: true, address: '' });
  assert.equal(shortAddress(''), '');
});

test('the line under the button: cancel and no-match keep the button, fallback and lockout reveal the password', () => {
  assert.deepEqual(line({ ok: true }, 'Touch ID'), { text: 'Touch ID confirmed, unlocking', cls: 'ok', password: false });
  assert.deepEqual(line({ ok: false, code: 'cancelled' }, 'Touch ID'), { text: 'Touch ID cancelled, tap to try again', cls: '', password: false });
  assert.deepEqual(line({ ok: false, code: 'failed' }, 'Windows Hello'), { text: 'Windows Hello did not match, tap to try again', cls: '', password: false });
  assert.deepEqual(line({ ok: false, code: 'timeout' }, 'Touch ID'), { text: 'Touch ID did not answer, tap to try again', cls: '', password: false });
  assert.deepEqual(line({ ok: false, code: 'fallback' }, 'Touch ID'), { text: 'Enter your password', cls: 'wait', password: true });
  assert.equal(line({ ok: false, code: 'locked' }, 'Touch ID').password, true);
  assert.equal(line({ ok: false, code: 'invalidated' }, 'Touch ID').password, true);
  assert.equal(line({ ok: false, code: 'nohost' }, 'Touch ID').text, 'Touch ID needs the Igneum Wallet app window');
  assert.equal(line({ ok: false, code: 'not_set_up', message: 'Touch ID is not set up on this Mac. Add a fingerprint in System Settings > Touch ID & Password.' }, 'Touch ID').password, true);
  assert.equal(line({ ok: false, code: 'engine', message: 'the engine did not unlock' }, 'Touch ID').text, 'the engine did not unlock');
  assert.deepEqual(line({ ok: false, code: 'weird' }, 'Touch ID'), { text: 'Touch ID did not succeed, tap to try again', cls: '', password: false });
  assert.deepEqual(line(null, 'Touch ID'), { text: '', cls: '', password: false });
  // never a full stop at the end, never a modal: one line in our words
  for (const c of ['cancelled', 'failed', 'timeout', 'fallback', 'locked', 'nohost']) assert.ok(!line({ ok: false, code: c }, 'Touch ID').text.endsWith('.'), c);
});

test('the one automatic prompt: first arrival after the person opened the app, setting on, 600 ms after drawn', () => {
  const mem = { used: false };
  assert.deepEqual(autoPrompt(arrival(), mem), { prompt: true, why: 'arrival', delay: 600 });
  assert.equal(L.AUTO_DELAY_MS, 600); assert.equal(L.ENTER_MS, 250);
  // once per launch
  assert.equal(autoPrompt(arrival(), { used: true }).why, 'once');
  // the setting off
  assert.equal(autoPrompt(arrival({ askOnOpen: false }), mem).why, 'setting-off');
  // the window is not visible at arrival (start at login, the window behind): nothing
  assert.equal(autoPrompt(arrival({ hidden: true }), mem).why, 'hidden');
  // a sheet is already up
  assert.equal(autoPrompt(arrival({ busy: true }), mem).why, 'busy');
  // not enrolled, or a browser tab: no button, no sheet
  assert.equal(autoPrompt(arrival({ touch: false }), mem).why, 'no-touch');
  // not locked
  assert.equal(autoPrompt(arrival({ phase: 'home' }), mem).why, 'not-locked');
});

test('never on an idle lock, a hand lock, the window coming back, a cancelled sheet, or the updater relaunch', () => {
  const mem = { used: false };
  // the page opened on the home screen (the wallet was unlocked at arrival) and locked later: idle or by hand
  assert.equal(autoPrompt(arrival({ reason: 'lock', firstPhase: 'home' }), mem).why, 'lock');
  assert.equal(autoPrompt(arrival({ reason: 'lock' }), mem).why, 'lock');
  // the window came back from the menu bar or the Dock
  assert.equal(autoPrompt(arrival({ reason: 'focus' }), mem).why, 'focus');
  // after a cancelled sheet the page waits for a tap
  assert.equal(autoPrompt(arrival({ reason: 'cancel' }), mem).why, 'cancel');
  // the first phase the page saw was not the lock screen (welcome, home): a later lock is not an arrival
  assert.equal(autoPrompt(arrival({ firstPhase: 'home' }), mem).why, 'not-arrival');
  assert.equal(autoPrompt(arrival({ firstPhase: 'welcome' }), mem).why, 'not-arrival');
  // the updater opened the app (first run after an update), not the person
  assert.equal(autoPrompt(arrival({ updatedFrom: '0.0.1' }), mem).why, 'updater');
  // nothing in the context is read as a prompt
  assert.equal(autoPrompt(null, null).prompt, false);
});

test('the idle lock choices and their labels', () => {
  assert.deepEqual(L.IDLE_CHOICES, [1, 5, 15, 60, 0]);
  assert.equal(L.IDLE_DEFAULT_MIN, 5);
  assert.deepEqual(L.IDLE_CHOICES.map(idleLabel), ['1 minute', '5 minutes', '15 minutes', '1 hour', 'never']);
  assert.equal(idleLabel('15'), '15 minutes'); assert.equal(idleLabel(undefined), 'never'); assert.equal(idleLabel(120), '2 hours');
});
