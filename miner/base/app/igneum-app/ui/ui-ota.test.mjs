// node --test app/igneum-app/ui/ui-ota.test.mjs   (no dependencies; the pre-push gate runs it)
// The interface over the air (docs/plans/ui-ota.md): Settings > Interface's words and the gentle reload rule.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

test('the embedded interface says built in; an over-the-air one says so with its date', () => {
  assert.deepEqual(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true }), { line: 'Interface 1.0.0, built in', eyebrow: 'built in', help: '' });
  const w = V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.1', source: 'ota', installed_at: Date.UTC(2026, 9, 7, 12) / 1000, confirmed: true });
  assert.equal(w.line, 'Interface 1.0.1, over the air, 7 Oct 2026');
  assert.equal(w.eyebrow, 'over the air');
  assert.equal(w.help, 'Built in: 1.0.0.');
  assert.equal(V.interfaceWords({}).line, '');
  assert.equal(readFileSync(join(here, 'VERSION'), 'utf8').trim(), '1.0.2', 'the embedded interface version is three-part and in ui/VERSION (1.0.2: the 0.1.22 tree, 7 October 2026)');
});

test('the help line says what is pending, refused, held back by the switch, or skipped', () => {
  assert.equal(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.1', source: 'ota', confirmed: false }).help, 'Waiting for this window to confirm the new interface.');
  assert.ok(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true, error: 'the page did not answer within 10 s of loading it.' }).help.startsWith('The last interface over the air was refused:'));
  assert.equal(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true, builtin: true, published_version: '1.0.2' }).help, 'Interface 1.0.2 is published over the air; switch this off to take it.');
  assert.equal(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true, busy: true, published_version: '1.0.2' }).help, 'Interface 1.0.2 is downloading.');
  assert.equal(V.interfaceWords({ embedded_version: '1.0.0', active_version: '1.0.0', source: 'embedded', confirmed: true, note: 'interface 1.0.2 needs engine 0.1.20 or newer (this is 0.1.19)' }).help, 'Interface 1.0.2 needs engine 0.1.20 or newer (this is 0.1.19).');
});

test('the page reloads only when the served interface changed and nobody is mid-task', () => {
  const u = { active_version: '1.0.1' };
  assert.equal(V.shouldReload('1.0.0', u, { phase: 'dashboard' }), true);
  assert.equal(V.shouldReload('1.0.1', u, { phase: 'dashboard' }), false, 'same version: nothing');
  assert.equal(V.shouldReload('', u, { phase: 'dashboard' }), false, 'a page that never pinged does not reload');
  assert.equal(V.shouldReload('1.0.0', u, { phase: 'dashboard', typing: true }), false);
  assert.equal(V.shouldReload('1.0.0', u, { phase: 'dashboard', sheetOpen: true }), false);
  assert.equal(V.shouldReload('1.0.0', u, { phase: 'address' }), false, 'never mid-setup');
  assert.equal(V.shouldReload('1.0.0', null, { phase: 'dashboard' }), false);
});

test('the page pings health once after its first paint and reports a first-paint error (the DOM block carries both)', () => {
  assert.ok(src.includes("api('api/ui/health', {})"), 'the health ping');
  assert.ok(src.includes("api('api/ui/health', { error: uiFirstError })"), 'the first-paint error');
  assert.ok(src.indexOf("window.addEventListener('error'") < src.indexOf('function poll()'), 'the error listener is installed before the first poll');
  assert.ok(!/<script[^>]+src=["']https?:/.test(readFileSync(join(here, 'index.html'), 'utf8')), 'no remote script in the page');
});
