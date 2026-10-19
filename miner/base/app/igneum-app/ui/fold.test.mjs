// node --test app/igneum-app/ui/fold.test.mjs   (scaling-21, the founder, 7 October 2026: "the miner needs some scaling so
// more is visible in the initial window"). Measures the key elements of each page inside the viewport on the mock with
// Playwright's Chromium, at 1280 by 800, 1440 by 900 and 1920 by 1080: the Overview's rate band, big button, ladder
// strip and chain card must end above the fold, and the node card's first row must start above it; Cards shows its
// first card row, Earnings its headline card, Prove its switch. Runs where the Playwright env is set (the mirror box:
// `. the builder's environment file`), skips elsewhere: the Mac runs no Playwright suite (the builder's notes, 7 October 2026).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
const here = dirname(fileURLToPath(import.meta.url)), root = join(here, '../../..');
const require = createRequire(import.meta.url);
let chromium = null;
try { if (process.env.IGNEUM_PLAYWRIGHT_DIR) chromium = require(join(process.env.IGNEUM_PLAYWRIGHT_DIR, 'node_modules/playwright')).chromium; } catch (e) { chromium = null; }
const SIZES = [[1280, 800], [1440, 900], [1920, 1080], [1920, 1200]];
// dash-24: the dashboard's content width the cap must allow at each window (1080 up to 1080p, 1280 at a 1440p-class window)
const MIN_DASH_W = { 1280: 1000, 1440: 1080, 1920: 1280 }; // at 1280 the window itself (less the rail and gutters) is the limit, not the cap
const ROW = 48; // the first row of a card: its head line with the padding above it

test('the first screen: the Overview above the fold at 1280 by 800, 1440 by 900 and 1920 by 1080; Cards, Earnings and Prove show their first object', { skip: chromium ? false : 'no Playwright env (IGNEUM_PLAYWRIGHT_DIR); runs on the mirror box' }, async () => {
  const port = 4480 + Math.floor(Math.random() * 400);
  const mock = spawn(process.execPath, [join(root, 'tools/ui-mock/server.mjs'), String(port)], { stdio: 'ignore' });
  try {
    for (let i = 0; i < 40; i++) { try { const r = await fetch(`http://127.0.0.1:${port}/t/mock/api/state`); if (r.ok) break; } catch (e) { } await new Promise((r) => setTimeout(r, 250)); }
    const browser = await chromium.launch();
    const bad = [];
    const rect = (page, sel) => page.evaluate((s) => { const el = document.querySelector(s); if (!el) return null; const r = el.getBoundingClientRect(); return { top: Math.round(r.top), bottom: Math.round(r.bottom), inner: window.innerHeight }; }, sel);
    for (const [w, h] of SIZES) {
      const page = await browser.newPage({ viewport: { width: w, height: h }, reducedMotion: 'reduce', colorScheme: 'dark' });
      const open = async (scenario, p) => { await page.goto(`http://127.0.0.1:${port}/t/mock/?scenario=${scenario}&page=${p}&theme=dark`, { waitUntil: 'load' }); await page.waitForTimeout(1200); };
      const fits = async (sel, label, rowOnly) => { const r = await rect(page, sel); if (!r) { bad.push(`${w}x${h} ${label}: not found`); return; } const end = rowOnly ? r.top + ROW : r.bottom; if (!(r.top >= 0 && end <= r.inner)) bad.push(`${w}x${h} ${label}: ${rowOnly ? 'row ends at ' + end : 'ends at ' + r.bottom} of ${r.inner}`); };
      await open('live', 'overview');
      { const r = await page.evaluate(() => { const el = document.querySelector('#screen-dashboard'); return el ? Math.round(el.getBoundingClientRect().width) : 0; }); if (r < (MIN_DASH_W[w] || 0)) bad.push(`${w}x${h} dashboard width ${r}, the cap must allow ${MIN_DASH_W[w]} (dash-24)`); }
      await fits('#totals', 'Overview rate band'); await fits('#btn-toggle', 'Overview big button'); await fits('#ladder-strip', 'Overview ladder strip'); await fits('#dag-card', 'Overview chain card'); await fits('#node-card', 'Overview node card first row', true);
      if (h >= 1080) await fits('#node-card', 'Overview node card whole at 1080');
      await open('rig', 'cards'); await fits('#tune-card', 'Cards tune strip'); await fits('#d-cards .gpu-line:first-child', 'Cards first card row');
      await open('ladder', 'earnings'); await fits('.earn', 'Earnings headline card');
      await open('ladder', 'prove'); await fits('#s-prove', 'Prove switch');
      await page.close();
    }
    await browser.close();
    assert.deepEqual(bad, [], 'below the fold:\n' + bad.join('\n'));
  } finally { mock.kill(); }
});

test('the type never drops below 13 px on the Overview density pass: no font-size under 13px inside the #page-overview rules', async () => {
  const { readFileSync } = await import('node:fs');
  const css = readFileSync(join(here, 'app.css'), 'utf8');
  const at = css.indexOf('/* scaling-21');
  assert.ok(at >= 0, 'the density block exists');
  const block = css.slice(at);
  for (const m of block.matchAll(/font-size:\s*(\d+(?:\.\d+)?)px/g)) assert.ok(parseFloat(m[1]) >= 13, 'font-size ' + m[1] + 'px in the density block');
});
