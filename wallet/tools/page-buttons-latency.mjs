// page-buttons-latency.mjs <engine url>: F-W2's measure (15 October 2026): for every enabled button on every page of the
// wallet, the time from the pointer's down to the first visible change on the button (a class, the disabled state,
// its text) or on the page (an element shown or hidden, a text change), sampled by a MutationObserver in the page;
// the rule is a visible response within 100 ms, the engine's reply confirming after. Run on the builder against a
// real engine (a watch-only wallet or a seeded one); prints one row a button and a verdict; exit 1 when any button
// shows nothing within 100 ms. Env: PLAYWRIGHT_DIR (the Playwright install), PAGES (comma list, default all).
import { createRequire } from 'node:module';
const require = createRequire(process.env.PLAYWRIGHT_DIR ? process.env.PLAYWRIGHT_DIR + '/package.json' : import.meta.url);
const { chromium } = require('playwright');
const url = process.argv[2];
if (!url) { console.error('usage: page-buttons-latency.mjs <engine url>'); process.exit(2); }
const pages = (process.env.PAGES || 'home,send,receive,history,mining,settings').split(',');
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1200, height: 760 } });
await page.goto(url, { waitUntil: 'load' });
await page.waitForTimeout(2500);
// the observer: the first mutation after a mark, anywhere on the page, with the time since the mark
await page.evaluate(() => {
  window.__lat = { markAt: 0, first: null };
  const mo = new MutationObserver((ms) => {
    if (window.__lat.markAt && window.__lat.first === null) {
      const m = ms[0];
      window.__lat.first = { at: performance.now() - window.__lat.markAt, what: m.type + (m.attributeName ? ':' + m.attributeName : ''), on: (m.target.id || m.target.className || m.target.nodeName || '').toString().slice(0, 40) };
    }
  });
  mo.observe(document.body, { attributes: true, childList: true, characterData: true, subtree: true });
});
const rows = [];
let worst = 0;
for (const p of pages) {
  await page.evaluate((id) => { const b = document.querySelector('.nav[data-page="' + id + '"]'); if (b) b.click(); }, p);
  await page.waitForTimeout(600);
  const buttons = await page.$$eval('#page-' + p + ' button:not([disabled]):not([hidden])', (bs) => bs.filter((b) => b.offsetParent !== null).map((b, i) => ({ i, id: b.id || '', text: (b.textContent || '').trim().slice(0, 30) })));
  for (const b of buttons) {
    if (!b.id) continue; // the measure is by id: a button with none is a list row's, measured through its page
    const el = await page.$('[id="' + b.id + '"]');
    if (!el) continue;
    // a button behind a closed disclosure is reached as a hand reaches it: the disclosure opened first
    await el.evaluate((e) => { let d = e.closest('details'); while (d) { d.open = true; d = d.parentElement ? d.parentElement.closest('details') : null; } }).catch(() => {});
    // the pointer is put on the button the way a hand does it: scrolled into view and hovered (the browser's own
    // actionability wait), the box read after, so a page that scrolls inside its own frame reads right
    try { await el.scrollIntoViewIfNeeded({ timeout: 5000 }); await el.hover({ timeout: 5000 }); } catch (e) { rows.push({ page: p, id: b.id, text: b.text, ms: null, what: 'not reachable: ' + String(e.message || e).split('\n')[0].slice(0, 60) }); worst = 999; continue; }
    await page.waitForTimeout(150);
    const box = await el.boundingBox(); if (!box) continue;
    await page.evaluate(() => { window.__lat.markAt = performance.now(); window.__lat.first = null; });
    await page.mouse.down();
    await page.waitForTimeout(120);
    const r = await page.evaluate(() => window.__lat.first);
    // a press that shows nothing names what lay under the pointer (a cover, a scrolled-away button) so the reading is read right
    const under = r ? '' : await page.evaluate(([x, y, id]) => { const e = document.elementFromPoint(x, y); return e ? (e.id === id ? 'the button' : (e.id || e.className || e.nodeName).toString().slice(0, 40)) : 'nothing'; }, [box.x + box.width / 2, box.y + box.height / 2, b.id]);
    await page.mouse.up();
    await page.waitForTimeout(400);
    const ms = r ? Math.round(r.at) : null;
    if (ms === null || ms > 100) worst = Math.max(worst, ms === null ? 999 : ms);
    rows.push({ page: p, id: b.id, text: b.text, ms, what: r ? r.what + ' on ' + r.on : 'nothing within 120 ms; under the pointer: ' + under });
    // a page change by the button is undone by going back to the page under test
    await page.evaluate((id) => { const n = document.querySelector('.nav[data-page="' + id + '"]'); if (n) n.click(); const o = document.querySelector('#ask-remove, #bio-enrol'); if (o) o.hidden = true; }, p);
    await page.waitForTimeout(300);
  }
}
for (const r of rows) console.log(String(r.ms === null ? '   -' : r.ms).padStart(4) + ' ms  ' + r.page.padEnd(9) + ' ' + r.id.padEnd(22) + ' ' + r.text.padEnd(26) + ' ' + r.what);
const bad = rows.filter((r) => r.ms === null || r.ms > 100);
console.log(bad.length ? 'BUTTON RESPONSE: FAIL (' + bad.length + ' of ' + rows.length + ' past 100 ms; worst ' + worst + ')' : 'BUTTON RESPONSE: PASS (' + rows.length + ' buttons, the slowest ' + Math.max(...rows.map((r) => r.ms || 0)) + ' ms)');
await browser.close();
process.exit(bad.length ? 1 : 0);
