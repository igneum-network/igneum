// Captures of the wallet's page as the window shows it, at the real sizes, for the site's showcase (the founder's
// order of 11 October 2026 through main: real captures in place of the placeholders). A real browser (Playwright's
// Chromium, headless) against a running engine; the screens are made through the page's own paths (a wallet is
// created in the engine's data dir, a throwaway, as the page smoke does). Two sizes, light and dark:
//   1440x900 (the Mac and Windows app's content area) and 390x844 (the phone form); files named
//   wallet-<screen>-<size>-<light|dark>.png under OUT (default /tmp/wallet-captures).
// Screens: welcome, import (the key mode, as the Ember key line shows it), home, send, receive, identity, settings,
// the Pay request's review (a site's ask through the bridge) and the lock screen.
// Run from wallet/ with the engine's URL (the engine fresh: welcome phase):
//   OUT=<dir> node tools/page-captures.mjs <engine url> [import key hex]
// Playwright lives under PLAYWRIGHT_DIR (the build user's test tool on builder seven), never in the product.
import { createRequire } from 'node:module';
import { mkdirSync } from 'node:fs';
const require = createRequire(process.env.PLAYWRIGHT_DIR ? process.env.PLAYWRIGHT_DIR + '/package.json' : import.meta.url);
const { chromium } = require('playwright');

const url = process.argv[2];
const demoKey = process.argv[3] || '';
if (!url) { console.error('usage: OUT=<dir> node tools/page-captures.mjs <engine url> [import key hex]'); process.exit(2); }
const OUT = process.env.OUT || '/tmp/wallet-captures';
mkdirSync(OUT, { recursive: true });
const PW = 'correct horse battery';
// the sizes: the page's defaults, or SIZES as name:width:height pairs (the window's two sizes for the founder's
// public-ready captures: 1200:1200:760 and 700:700:620, the kept look laying out from 900 by 620 and narrow under 720)
const sizes = process.env.SIZES ? process.env.SIZES.split(',').map((p) => { const [name, w, h] = p.split(':'); return { name, width: Number(w), height: Number(h) }; }) : [{ name: '1440', width: 1440, height: 900 }, { name: '390', width: 390, height: 844 }];
const files = [];

const browser = await chromium.launch({ headless: true });
// SCALE: the device scale factor (2 for the site's hero capture, the site team's ask of 14 October 2026); 1 by default
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: Number(process.env.SCALE || 1) });
await page.goto(url, { waitUntil: 'load' });
await page.waitForTimeout(1200);

async function shoot(screen, before) {
  for (const s of sizes) {
    await page.setViewportSize({ width: s.width, height: s.height });
    await page.waitForTimeout(250);
    if (before) await before();
    for (const scheme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.evaluate((sc) => { document.documentElement.setAttribute('data-theme', sc); }, scheme);
      await page.waitForTimeout(400);
      const f = `${OUT}/wallet-${screen}-${s.name}-${scheme}.png`;
      await page.screenshot({ path: f });
      files.push(f);
    }
  }
  await page.emulateMedia({ colorScheme: 'light' });
  await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'light'); });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForTimeout(200);
}

// a seeded wallet (VAULT_PW set, the engine's data dir carrying a vault with payments on the chain): the unlock
// screen is shot, the password entered, and the welcome, import and create screens are skipped
const VAULT_PW = process.env.VAULT_PW || '';
if (VAULT_PW) {
  await page.waitForSelector('#screen-unlock', { state: 'visible', timeout: 15000 });
  await shoot('lock');
  await page.fill('#unlock-pw', VAULT_PW);
  await page.press('#unlock-pw', 'Enter');
  await page.waitForSelector('#screen-home', { state: 'visible', timeout: 30000 });
  await page.waitForTimeout(1500);
}
// a watch-only run (WATCH_ONLY=1: the engine started with a watch.json; the home shot after the scene's first push and the
// shares layer's first document, nothing else): the proof of the shares landing on a watched address
const WATCH = !!process.env.WATCH_ONLY;
if (WATCH) {
  await page.waitForSelector('#screen-home', { state: 'visible', timeout: 30000 });
  for (let i = 0; i < 0; i++) { const c = await page.evaluate(() => { const el = document.querySelector('#scene .ig-scene-cap span'); return el ? el.textContent : ''; }); if (c && !/Reading the chain/.test(c)) break; await page.waitForTimeout(500); }
  await page.waitForTimeout(6000);
}
// welcome
if (!VAULT_PW && !WATCH) {
await page.waitForSelector('#screen-welcome', { state: 'visible' });
await shoot('welcome');

// import, the key mode (the Ember key line's screen); the key is typed when given, never imported here
await page.click('#go-import');
await page.waitForSelector('#screen-import', { state: 'visible' });
await page.click('#import-mode .seg-b[data-mode="key"]');
if (demoKey) await page.fill('#import-data', demoKey);
await page.fill('#import-pw', PW);
await page.fill('#import-pw2', PW);
await shoot('import-key');
await page.click('#import-back');
await page.waitForSelector('#screen-welcome', { state: 'visible' });

// a wallet through the page (the words checked back), then the home screen
await page.click('#go-create');
await page.fill('#create-pw', PW);
await page.fill('#create-pw2', PW);
await shoot('create-password');
await page.click('#create-next');
await page.waitForSelector('#create-2:not([hidden])', { timeout: 20000 });
const words = await page.locator('#words li').allTextContents();
// a capture never carries a real phrase (the project's rule, 16 October 2026): the words step is shot with a mock set and the
// real words put back for the check; the throwaway wallet's data dir is deleted with the run
const MOCK = ['amber', 'basalt', 'cinder', 'dune', 'ember', 'flint', 'granite', 'harbour', 'iron', 'jade', 'kiln', 'lantern', 'magma', 'north', 'ochre', 'pebble', 'quartz', 'river', 'slate', 'tide', 'umber', 'vale', 'willow', 'zinc'];
await page.evaluate((mock) => { document.querySelectorAll('#words li').forEach((li, i) => { li.dataset.real = li.innerHTML; const n = li.querySelector('i, .n, span'); li.innerHTML = (n ? n.outerHTML : '') + mock[i % mock.length]; }); }, MOCK);
await shoot('create-words');
// the step's end: the scrolling container is main (the body never scrolls); the page is scrolled inside each size
async function scrollEnd() { return page.evaluate(() => { const m = document.querySelector('main'); m.scrollTop = m.scrollHeight; return [m.scrollHeight, m.clientHeight, m.scrollTop]; }); }
console.error('words step scroll [height, client, top]: ' + JSON.stringify(await scrollEnd()));
await shoot('create-words-end', scrollEnd);
await page.evaluate(() => { document.querySelectorAll('#words li').forEach((li) => { if (li.dataset.real) li.innerHTML = li.dataset.real; }); const m = document.querySelector('main') || document.scrollingElement; m.scrollTop = 0; });
await page.click('#create-written');
await page.waitForTimeout(300);
await shoot('create-check');
const inputs = page.locator('#checks input');
const n = await inputs.count();
for (let i = 0; i < n; i++) {
  const idx = Number(await inputs.nth(i).getAttribute('data-idx'));
  await inputs.nth(i).fill(words[idx].replace(/^\d+\.\s*/, '').trim());
}
await page.click('#create-confirm');
}
if (!VAULT_PW && !WATCH) await page.waitForSelector('#screen-home', { state: 'visible', timeout: 20000 });
// the balance read lands after the first state poll; the shot waits for it (up to 15 s) so no screen reads
// "reading the balance"
for (let i = 0; i < 30; i++) {
  const t = await page.locator('#screen-home').innerText();
  if (!/reading the balance/.test(t)) break;
  await page.waitForTimeout(500);
}
await page.waitForTimeout(500);
await page.waitForTimeout(VAULT_PW ? 4000 : 500);
// the scene's first push: the caption leaves "Reading the chain" when the first answer is drawn (the project's rule of
// 12 October 2026: the tool shoots the home after the first push, never the blank stage)
// the Home's activity rows (the payouts read) land within a few seconds of the first poll
for (let i = 0; i < 30; i++) { const n = await page.evaluate(() => document.querySelectorAll('#recent .arow').length); if (n > 0) break; await page.waitForTimeout(500); }
await page.waitForTimeout(800);
await shoot('home');

for (const [name, nav] of [['send', 'send'], ['receive', 'receive'], ['history', 'history'], ['mining', 'mining'], ['settings', 'settings']]) {
  await page.click(`#rail .nav[data-page="${nav}"]`);
  await page.waitForTimeout(600);
  if (name === 'mining') {
    // the Mining page in its states: no identity (as the page opens), then with the identity made in the window
    // (the Desk read on it: bonded or not as the chain says); the bonded state is the same shot on a bonded identity
    for (let i = 0; i < 40; i++) { const ph = await page.evaluate(() => (document.querySelector('#holder-amount') || {}).placeholder || ''); if (/^up to [\d,]/.test(ph)) break; await page.waitForTimeout(500); }
    for (let i = 0; i < 30; i++) { const w = await page.evaluate(() => (document.querySelector('#node-pill-word, #topbar .pill, .ig-pill-node') || {}).textContent || ''); if (!/lost/i.test(w) && !/lost/i.test(await page.evaluate(() => document.body.innerText.slice(0, 400)))) break; await page.waitForTimeout(500); }
    await page.waitForTimeout(800);
    await shoot('mining-no-identity');
    const mk = await page.$('#identity-make');
    if (mk && await mk.isVisible()) {
      await mk.click();
      for (let i = 0; i < 40; i++) { const t = await page.evaluate(() => (document.querySelector('#identity-public') || {}).textContent || ''); if (/^0x[0-9a-fA-F]{64}/.test(t)) break; await page.waitForTimeout(500); }
      // the identity's eyebrow settles after the draw's read (staggered 1.5 s behind the holder's, retried under the bucket)
      for (let i = 0; i < 40; i++) { const e = await page.evaluate(() => (document.querySelector('#identity-eyebrow') || {}).textContent || ''); if (/bonded/i.test(e)) break; await page.waitForTimeout(500); }
      await page.waitForTimeout(1500);
      for (let i = 0; i < 30; i++) { if (!/\bLOST\b/i.test(await page.evaluate(() => document.body.innerText.slice(0, 400)))) break; await page.waitForTimeout(500); }
      // the holder's second read names the bond ("30 IGN buys") once the draw has read it; wait for it on a bonded key
      for (let i = 0; i < 40; i++) { const u = await page.evaluate(() => (document.querySelector('#holder-units') || {}).textContent || ''); const e = await page.evaluate(() => (document.querySelector('#identity-eyebrow') || {}).textContent || ''); if (!/Bonded on/i.test(e) || /, 30 IGN buys|, [0-9,.]+ IGN buys (?!1 unit; )/.test(u) && !/28\.76 IGN buys/.test(u)) break; await page.waitForTimeout(500); }
      // the Desk's tiles and head stamp (the desk read four seconds behind the identity) before the shot
      for (let i = 0; i < 40; i++) { const e = await page.evaluate(() => (document.querySelector('#desk-eyebrow') || {}).textContent || ''); const err = await page.evaluate(() => (document.querySelector('#desk-err') || {}).textContent || ''); if (/Read at head/.test(e) || err) break; await page.waitForTimeout(500); }
      const bonded = await page.evaluate(() => /bonded/i.test((document.querySelector('#identity-eyebrow') || {}).textContent || '') && !/not bonded|no bond/i.test((document.querySelector('#identity-eyebrow') || {}).textContent || ''));
      await shoot(bonded ? 'mining-bonded' : 'mining-identity');
    }
    continue;
  }
  if (name === 'send') {
    await page.fill('#send-to', '0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266');
    await page.fill('#send-amount', '0.25');
    await page.waitForTimeout(300);
  }
  await shoot(name);
  // the signer's prompt: the review of a send (the quote's sentences, the fee, the simulated result) when the chain answers
  if (name === 'send' && !WATCH) {
    try {
      await page.click('#send-quote');
      await page.waitForSelector('#send-review:not([hidden]), #ask-send:not([hidden])', { timeout: 20000 });
      await page.waitForTimeout(600);
      await shoot('send-review');
      const no = await page.$('#ask-send-no, #send-cancel'); if (no) await no.click();
      await page.waitForTimeout(400);
    } catch (e) { console.error('no send review: ' + String(e.message || e).split('\n')[0].slice(0, 120)); }
  }
}

// the Pay request's review (the founder's four screens of 12 October 2026: the lock screen, the home with the scene,
// the send, the Pay request): a site's eth_sendTransaction with payRequest calldata to IgneumPay on devnet9 arrives
// through the bridge as an ask in the page's bridge card, which is the screen; the site connects first (the page's
// Connect answered here), the ask is shot, then declined so nothing is sent
if (WATCH) { await browser.close(); for (const f of files) console.log('CAPTURE ' + f); console.log(`DONE ${files.length} captures under ${OUT}`); process.exit(0); }
const BRIDGE = process.env.BRIDGE || 'http://127.0.0.1:26811';
const PAY = '0xbdb2f735a78c9aaf190f3010a70d654245050140';
const SHOP = '70997970C51812dc3A010C7d01b50e0d17dc79C8';
const word = (h) => h.replace(/^0x/, '').toLowerCase().padStart(64, '0');
const num = (n) => BigInt(n).toString(16).padStart(64, '0');
// the shop's request 150 of the demo's two hundred (nonce 1 was paid by the rehearsal and IgneumPay refuses a paid request)
const SIG = 'e9f385f49845d8be8dc05966ca063827c1510577af5517e768b524f6641c1de92ca7cf9c404b8ecf459b2ff8b035effed028fd283a08ce23bff590e8f07101f61b';
const payData = '0x98faa047' + [word(SHOP), word('0'), num(1500000000000000000n), num(0), num(0), word('0'), num(150), num(256), num(65), SIG.padEnd(128, '0')].join('');
let siteToken = '';
async function site(method, params) {
  const r = await fetch(BRIDGE + '/rpc', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Origin': 'https://shop.example', 'Sec-Fetch-Site': 'cross-site', 'Sec-Fetch-Mode': 'cors', 'X-Igneum-Site': siteToken }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }) });
  const t = r.headers.get('x-igneum-site'); if (t) siteToken = t;
  return r.json();
}
await page.click('#rail .nav[data-page="home"]');
await page.waitForTimeout(400);
const base = url.replace(/\/$/, '');
const t0 = Date.now();
page.on('console', (m) => { if (m.text().startsWith('DECIDE')) console.error('page: ' + m.text().replace(/\n\s*/g, ' <- ').slice(0, 900)); });
await page.evaluate(() => {
  const f = window.fetch;
  window.fetch = function (u, o) { if (String(u).indexOf('bridge/decide') >= 0) console.log('DECIDE ' + (o && o.body) + ' ' + new Error().stack); return f.apply(this, arguments); };
});
console.error('page state before the connect: ' + JSON.stringify(await fetch(base + '/api/state').then((r) => r.json()).then((s) => ({ phase: s.phase, pending: (s.bridge || {}).pending, sites: ((s.bridge || {}).sites || []).length }))));
const connect = site('eth_requestAccounts', []).then((r) => { console.error('bridge connect answered at +' + (Date.now() - t0) + ' ms: ' + JSON.stringify(r).slice(0, 200)); return r; });
let lastPending = '';
const watch = setInterval(async () => {
  try {
    const p = await fetch(base + '/api/bridge/pending').then((r) => r.text());
    if (p !== lastPending) { lastPending = p; console.error('pending at +' + (Date.now() - t0) + ' ms: ' + p.slice(0, 160)); }
  } catch (e) {}
}, 200);
try {
  await page.waitForSelector('#bridge', { state: 'visible', timeout: 15000 });
} catch (e) {
  const pending = await fetch(url.replace(/\/$/, '') + '/api/bridge/pending').then((r) => r.text()).catch((e2) => String(e2));
  console.error('the bridge card did not show; api/bridge/pending: ' + pending.slice(0, 300));
  clearInterval(watch);
  throw e;
}
clearInterval(watch);
await page.click('#bridge-yes');
await connect;
await page.waitForTimeout(500);
const me = await fetch(base + '/api/state').then((r) => r.json()).then((s) => s.address);
await page.waitForTimeout(3000); // the public host's bucket refills after the scene's and the page's reads
const t1 = Date.now();
const pay = site('eth_sendTransaction', [{ from: me, to: PAY, value: '0x' + (1500000000000000000n).toString(16), data: payData }]).then((r) => { console.error('bridge pay answered at +' + (Date.now() - t1) + ' ms: ' + JSON.stringify(r).slice(0, 300)); return r; });
let lastPay = '';
const watch2 = setInterval(async () => {
  try {
    const p = await fetch(base + '/api/bridge/pending').then((r) => r.text());
    if (p !== lastPay) { lastPay = p; console.error('pay pending at +' + (Date.now() - t1) + ' ms: ' + p.slice(0, 220)); }
  } catch (e) {}
}, 200);
try {
  await page.waitForSelector('#bridge', { state: 'visible', timeout: 15000 });
} catch (e) {
  clearInterval(watch2);
  console.error('the Pay request card did not show; the site answer so far: ' + lastPay.slice(0, 300));
  throw e;
}
clearInterval(watch2);
await page.waitForTimeout(600);
await shoot('pay-request');
await page.click('#bridge-no');
await pay;
await page.waitForTimeout(400);

// the lock screen: the page's own Lock, the window as a person finds it after walking away
await page.click('#btn-lock');
await page.waitForTimeout(800);
await shoot('lock');

await browser.close();
for (const f of files) console.log('CAPTURE ' + f);
console.log(`DONE ${files.length} captures under ${OUT}`);
