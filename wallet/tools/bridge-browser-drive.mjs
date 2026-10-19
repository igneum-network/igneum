// The page bridge from a real website in a real browser: a test site on its own origin loads provider.js from the
// bridge, asks to connect and to sign, and the wallet page (a second tab, same browser) shows each ask and answers
// it. Run from wallet/ with the engine URL, the bridge port and the site URL:
//   node tools/bridge-browser-drive.mjs <engine url> <bridge port> <site url>
import { createRequire } from 'node:module';
const require = createRequire(process.env.PLAYWRIGHT_DIR ? process.env.PLAYWRIGHT_DIR + '/package.json' : import.meta.url);
const { chromium } = require('playwright');
const [engineUrl, bridgePort, siteUrl] = process.argv.slice(2);
if (!siteUrl) { console.error('usage: node tools/bridge-browser-drive.mjs <engine url> <bridge port> <site url>'); process.exit(2); }
let fails = 0;
function check(name, ok, why) { console.log(`PROBE ${name} ${ok ? 'PASS' : 'FAIL'} ${why || ''}`); if (!ok) fails++; }

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ viewport: { width: 1000, height: 680 } });
const wallet = await ctx.newPage();
await wallet.goto(engineUrl, { waitUntil: 'load' });
await wallet.waitForTimeout(1500);
// a wallet already exists in this engine's data dir (the smoke imported one); unlock if asked
if (await wallet.locator('#screen-unlock').isVisible()) {
  await wallet.fill('#unlock-pw', 'correct horse battery');
  await wallet.click('#unlock-submit');
}
await wallet.waitForSelector('#screen-home:not([hidden])', { timeout: 20000 });

const site = await ctx.newPage();
await site.goto(`${siteUrl}?bridge=${bridgePort}`, { waitUntil: 'load' });
await site.waitForFunction(() => document.getElementById('status').textContent !== 'loading the provider', null, { timeout: 10000 });
check('provider_loaded', (await site.locator('#status').textContent()) === 'provider loaded', await site.locator('#status').textContent());
check('eip6963_announced', (await site.evaluate(() => window.__announced)) === 'Igneum Wallet');
check('chain_id_without_connection', (await site.evaluate(() => window.igneum.request({ method: 'eth_chainId' }))) === '0x1c8e');
check('accounts_empty_before_connect', JSON.stringify(await site.evaluate(() => window.igneum.request({ method: 'eth_accounts' }))) === '[]');

// the site asks to connect; the wallet tab shows the ask; decline, then accept
const declined = site.evaluate(() => window.igneum.request({ method: 'eth_requestAccounts' }).then(() => 'ok', (e) => e.message));
await wallet.waitForSelector('#bridge:not([hidden])', { timeout: 10000 });
check('ask_shown', (await wallet.locator('#bridge-title').textContent()).includes('asks to see your address'), await wallet.locator('#bridge-title').textContent());
check('ask_names_the_site', (await wallet.locator('#bridge-origin').textContent()).startsWith('http://127.0.0.1'), await wallet.locator('#bridge-origin').textContent());
await wallet.click('#bridge-no');
check('decline_reaches_the_site', (await declined) === 'the person declined', await declined);
const accepted = site.evaluate(() => window.igneum.request({ method: 'eth_requestAccounts' }));
await wallet.waitForSelector('#bridge:not([hidden])', { timeout: 10000 });
await wallet.click('#bridge-yes');
const accounts = await accepted;
check('accept_gives_the_address', Array.isArray(accounts) && /^0x[0-9a-fA-F]{40}$/.test(accounts[0]), JSON.stringify(accounts));

// personal_sign: the message is shown in full, signed on the yes
const signing = site.evaluate((a) => window.igneum.request({ method: 'personal_sign', params: ['0x68656c6c6f2066726f6d2074686520736869', a] }), accounts[0]);
await wallet.waitForSelector('#bridge:not([hidden])', { timeout: 10000 });
const lines = await wallet.locator('#bridge-lines li').allTextContents();
check('message_shown_in_full', lines.some((l) => l === 'Message: hello from the shi'), lines[0]);
await wallet.click('#bridge-yes');
const sig = await signing;
check('signature_returned', /^0x[0-9a-f]{130}$/.test(sig), String(sig).slice(0, 12));

// eth_sign is refused without an ask
const refused = await site.evaluate(() => window.igneum.request({ method: 'eth_sign', params: ['0x', '0x00'] }).then(() => 'ok', (e) => e.message));
check('eth_sign_refused', refused === 'this wallet never signs a bare hash', refused);
await wallet.waitForTimeout(500);
check('no_ask_for_eth_sign', !(await wallet.locator('#bridge').isVisible()));

// the site is listed in Settings and can be disconnected
await wallet.click('[data-page="settings"]');
await wallet.waitForTimeout(2500);
check('site_listed', (await wallet.locator('#sites').textContent()).includes('http://127.0.0.1'), (await wallet.locator('#sites').textContent()).trim().slice(0, 60));
await wallet.click('[data-disconnect]');
await wallet.waitForTimeout(2500);
const after = await site.evaluate(() => window.igneum.request({ method: 'eth_accounts' }));
check('disconnected', JSON.stringify(after) === '[]', JSON.stringify(after));

await browser.close();
console.log(fails === 0 ? 'BRIDGE-BROWSER PASS' : `BRIDGE-BROWSER FAIL (${fails})`);
process.exit(fails === 0 ? 0 : 1);
