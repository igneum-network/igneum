/* The lock screen (pure; lock-screen.test.mjs loads this file): what the screen shows for a wallet state, the line
   under the button after the host answered, and when the system Touch ID (or Windows Hello) sheet may be raised
   without a tap. 5 October 2026, for 0.1.4.

   The sheet appears when the person taps the button or presses Return or Space on it. One exception: the first
   arrival after the person opened the app may raise it once, AUTO_DELAY_MS after the lock screen is fully drawn,
   when the setting "Ask for Touch ID when the wallet opens" is on. Never on an idle lock, on a hand lock, when the
   window comes back, after a cancelled sheet, or when the updater relaunched the app. */
const LockScreen = (function () {
  'use strict';
  /** after the lock screen is fully drawn */
  const AUTO_DELAY_MS = 600;
  /** the transition from the home screen to the lock screen, and the lock screen's own entry */
  const ENTER_MS = 250;
  /** the idle lock's choices in Settings, minutes; 0 is never */
  const IDLE_CHOICES = [1, 5, 15, 60, 0];
  const IDLE_DEFAULT_MIN = 5;

  function idleLabel(min) {
    const m = Number(min) || 0;
    if (m <= 0) return 'never';
    if (m === 60) return '1 hour';
    if (m % 60 === 0) return (m / 60) + ' hours';
    return m === 1 ? '1 minute' : m + ' minutes';
  }

  function shortAddress(a) {
    return a ? a.slice(0, 8) + '…' + a.slice(-6) : '';
  }

  /** What the lock screen shows: the fingerprint button when Touch ID or Windows Hello is enrolled and the app
      window is the host (a browser tab cannot raise the sheet); else the password field is the primary control. */
  function layout(s, hostHere) {
    const b = (s && s.biometric) || {};
    const touch = !!(b.enrolled && hostHere);
    return { touch: touch, passwordPrimary: !touch, address: shortAddress(s && s.display) };
  }

  /** The line under the button after the host answered: {text, cls, password}. cls is '' (ember), 'ok' or
      'wait'; password says the password field should be revealed (the sheet cannot unlock this time). */
  function line(r, name) {
    if (!r) return { text: '', cls: '', password: false };
    if (r.ok) return { text: name + ' confirmed, unlocking', cls: 'ok', password: false };
    const c = r.code;
    if (c === 'cancelled') return { text: name + ' cancelled, tap to try again', cls: '', password: false };
    if (c === 'failed') return { text: name + ' did not match, tap to try again', cls: '', password: false };
    if (c === 'timeout') return { text: name + ' did not answer, tap to try again', cls: '', password: false };
    if (c === 'fallback') return { text: 'Enter your password', cls: 'wait', password: true };
    if (c === 'locked') return { text: name + ' is locked, use your password', cls: '', password: true };
    if (c === 'invalidated') return { text: name + ' was turned off (a fingerprint changed): use your password, then turn it on again in Settings', cls: '', password: true };
    if (c === 'not_set_up' || c === 'unavailable' || c === 'not_enrolled') return { text: r.message || (name + ' is not set up on this Mac'), cls: '', password: true };
    if (c === 'nohost') return { text: name + ' needs the Igneum Wallet app window', cls: '', password: true };
    if (c === 'engine') return { text: r.message || ('the wallet did not unlock'), cls: '', password: true };
    return { text: r.message || (name + ' did not succeed, tap to try again'), cls: '', password: false };
  }

  /** Whether the sheet may be raised now without a tap. ctx: reason (arrival | lock | focus | cancel), phase,
      touch (the button is on screen), askOnOpen (the setting), hidden (the window is not visible), busy (a sheet is
      up), firstPhase (the first phase this page saw: 'unlock' means the app opened locked), updatedFrom (set on
      the first run after an update: the updater opened the app, not the person). mem.used: the one chance went. */
  function autoPrompt(ctx, mem) {
    const c = ctx || {}, m = mem || {};
    const no = function (why) { return { prompt: false, why: why, delay: 0 }; };
    if (c.phase !== 'unlock') return no('not-locked');
    if (!c.touch) return no('no-touch');
    if (m.used) return no('once');
    if (c.reason !== 'arrival') return no(c.reason || 'not-arrival');
    if (c.firstPhase !== 'unlock') return no('not-arrival');
    if (c.updatedFrom) return no('updater');
    if (!c.askOnOpen) return no('setting-off');
    if (c.hidden) return no('hidden');
    if (c.busy) return no('busy');
    return { prompt: true, why: 'arrival', delay: AUTO_DELAY_MS };
  }

  return { AUTO_DELAY_MS: AUTO_DELAY_MS, ENTER_MS: ENTER_MS, IDLE_CHOICES: IDLE_CHOICES, IDLE_DEFAULT_MIN: IDLE_DEFAULT_MIN, idleLabel: idleLabel, shortAddress: shortAddress, layout: layout, line: line, autoPrompt: autoPrompt };
})();
if (typeof module === 'object' && module && module.exports) module.exports = LockScreen;
