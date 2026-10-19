// The page driven by a real browser (Playwright's Chromium, headless) against a running engine: the welcome screen,
// create a wallet through the page, the word check, the home screen with the address and the node line, lock and
// unlock, a quote with its sentences, Settings. Run from wallet/ with the engine's URL:
//   node tools/page-drive.mjs <engine url>
// Playwright lives under PLAYWRIGHT_DIR (the build user's test tool on builder seven), never in the product.
import { createRequire } from 'node:module';
const require = createRequire(process.env.PLAYWRIGHT_DIR ? process.env.PLAYWRIGHT_DIR + '/package.json' : import.meta.url);
const { chromium } = require('playwright');

const url = process.argv[2];
if (!url) { console.error('usage: node tools/page-drive.mjs <engine url>'); process.exit(2); }
let fails = 0;
function check(name, ok, why) { console.log(`PROBE ${name} ${ok ? 'PASS' : 'FAIL'} ${why || ''}`); if (!ok) fails++; }

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1000, height: 680 } });
const errors = [];
page.on('pageerror', (e) => errors.push(String(e)));
// a refused request (a wrong password, 400) is logged by the browser as a failed resource: expected, not an error
page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource.*400/.test(m.text())) errors.push(m.text()); });

await page.goto(url, { waitUntil: 'load' });
await page.waitForTimeout(1500);
check('welcome_shown', await page.locator('#screen-welcome').isVisible(), (await page.locator('#welcome-eyebrow').textContent()).trim());
check('rail_hidden_on_welcome', !(await page.locator('#rail').isVisible()));

// create a wallet through the page
await page.click('#go-create');
await page.fill('#create-pw', 'correct horse battery');
await page.fill('#create-pw2', 'correct horse battery');
await page.click('#create-next');
await page.waitForSelector('#create-2:not([hidden])', { timeout: 20000 });
const words = await page.locator('#words li').allTextContents();
check('words_shown_once', words.length === 24, `${words.length} words`);
const address = (await page.locator('#create-address').textContent()).trim();
check('address_shown', /^0x[0-9a-fA-F]{40}$/.test(address), address.slice(0, 10));
await page.click('#create-written');
check('words_gone_after_written', (await page.locator('#words li').count()) === 0);
const inputs = page.locator('#checks input');
const n = await inputs.count();
for (let i = 0; i < n; i++) {
  const idx = Number(await inputs.nth(i).getAttribute('data-idx'));
  await inputs.nth(i).fill(words[idx]);
}
await page.click('#create-confirm');
await page.waitForSelector('#screen-home:not([hidden])', { timeout: 20000 });
check('home_after_create', await page.locator('#page-home').isVisible());
check('home_address', (await page.locator('#home-address').textContent()).trim() === address);
const railBox = await page.locator('#rail').boundingBox();
const titleBox = await page.locator('#page-title-text').boundingBox();
check('content_clear_of_rail', railBox && titleBox && titleBox.x >= railBox.x + railBox.width, `rail ${railBox && railBox.width}px, title at x=${titleBox && Math.round(titleBox.x)}`);
await page.waitForTimeout(3000);
const nodeLine = (await page.locator('#node-line-text').textContent()).trim();
check('node_line', /^Node (synced|starting|lost)/.test(nodeLine), nodeLine);
// the node card's scan line (history_scan.rs): how far the chain is read for this address; the card opens on Home
await page.click('#node-toggle');
// the scan starts on the engine's next poll after the wallet exists (5 s): the line is waited for, up to 12 s
let scanLine = '';
for (let i = 0; i < 24 && !/reading the chain|read to block/.test(scanLine); i++) { await page.waitForTimeout(500); scanLine = (await page.locator('#node-scan').textContent()) || ''; }
check('node_scan_line', /reading the chain|read to block/.test(scanLine), scanLine.slice(0, 80));
await page.click('#node-toggle');
check('no_backup_nag_after_check', !(await page.locator('#backup-note').isVisible()));

// the quote and its sentences
await page.click('[data-page="send"]');
await page.fill('#send-to', '0x70997970C51812dc3A010C7d01b50e0d17dc79C8');
await page.fill('#send-amount', '0.25');
await page.click('#send-quote');
await page.waitForSelector('#ask-send:not([hidden])', { timeout: 20000 });
const lines = await page.locator('#ask-send-lines li').allTextContents();
check('quote_sentences', lines.length === 3 && lines[0].startsWith('You send 0.25 IGN to 0x709979'), lines[0]);
check('fee_line', /up to .* IGN; total up to/.test(await page.locator('#fee-line').textContent()));
await page.click('#ask-send-no');

// the mining identity screen (DESIGN.md 1.13): on an authority chain the draw reads and a bond quotes with the
// registry's sentence; on a chain with no registry the buttons are off and the eyebrow says so
await page.click('[data-page="mining"]');
await page.waitForTimeout(500);
const miningEyebrow = (await page.locator('#bond-eyebrow').textContent()).trim();
const hasRegistry = !/no bond registry/.test(miningEyebrow);
check('mining_eyebrow', miningEyebrow.length > 0, miningEyebrow);
if (hasRegistry) {
  await page.click('#draw-go');
  await page.waitForSelector('#draw-out:not([hidden])', { timeout: 20000 });
  const drawKeys = await page.locator('#draw-kv .ig-label').allTextContents();
  const drawShare = (await page.locator('#draw-share').textContent()).trim();
  const drawRead = (await page.locator('#draw-read').textContent()).trim();
  check('draw_rows', drawKeys.length === 6 && drawKeys[0] === 'bond' && drawKeys[5] === 'chance per height' && /^[\d,]+(\.\d+)?$/.test(drawShare) && /^read from the chain at head [\d,]+, /.test(drawRead), drawKeys.join(',') + ' | ' + drawShare + ' | ' + drawRead);
  check('mining_title', (await page.locator('#page-title-text').textContent()).trim() === 'Mining');
  check('draw_pill', /^(bonded|no bond|bonding)$/.test((await page.locator('#draw-pill-word').textContent()).trim()), (await page.locator('#draw-pill').getAttribute('class')));
  await page.screenshot({ path: (process.env.SHOT || '/tmp/igneum-wallet-page.png').replace(/\.png$/, '-mining.png') });
  // the design system's check (ADOPTION.md section 5): the identity screen at 390 px, the two cards stacked, the key
  // field not clipped, the rail a bottom tab bar; the render goes beside the screenshot
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(400);
  const keyBox = await page.locator('#draw-key-box').boundingBox();
  const cardBox = await page.locator('#mining-card-draw').boundingBox();
  check('mining_390_no_clip', keyBox && cardBox && keyBox.x + keyBox.width <= cardBox.x + cardBox.width + 1 && keyBox.width <= 390, `key ${keyBox && Math.round(keyBox.width)}px wide in a ${cardBox && Math.round(cardBox.width)}px card`);
  check('mining_390_tab_bar', (await page.locator('#rail').boundingBox()).y > 700, 'the rail is a bottom tab bar');
  // the render at the sketch's size (390 by 1500) so both cards show; the body scrolls inside main, so a full-page
  // shot would stop at the viewport
  await page.setViewportSize({ width: 390, height: 1500 });
  await page.waitForTimeout(400);
  await page.screenshot({ path: (process.env.SHOT || '/tmp/igneum-wallet-page.png').replace(/\.png$/, '-mining-390.png') });
  // the dark pair through the colour scheme the page follows (headless Chromium reports light; an attribute set on
  // the html was undone by the state poll's applyTheme('system') before the shot, so both renders came out light)
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.waitForTimeout(300);
  await page.screenshot({ path: (process.env.SHOT || '/tmp/igneum-wallet-page.png').replace(/\.png$/, '-mining-390-dark.png') });
  check('mining_390_dark_ground', await page.evaluate(() => getComputedStyle(document.body).backgroundColor === 'rgb(12, 12, 14)'), await page.evaluate(() => getComputedStyle(document.body).backgroundColor));
  await page.emulateMedia({ colorScheme: 'light' });
  // the title never truncates at 390: its box holds its words
  const titleEl = page.locator('#page-title-text');
  check('mining_390_title_whole', await titleEl.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), await titleEl.evaluate((e) => e.scrollWidth + ' of ' + e.clientWidth));
  await page.setViewportSize({ width: 1000, height: 680 });
  await page.waitForTimeout(300);
  await page.fill('#bond-key', '0x' + '4b'.repeat(32));
  await page.fill('#bond-amount', '0.001');
  await page.click('#bond-quote');
  await page.waitForSelector('#ask-bond:not([hidden])', { timeout: 20000 });
  const bondLines = await page.locator('#ask-bond-lines li').allTextContents();
  check('bond_sentence', bondLines.length === 3 && bondLines[0].startsWith('You bond 0.001 IGN to the identity key 0x4b4b'), bondLines[0]);
  await page.click('#ask-bond-no');
  await page.selectOption('#bond-action', 'set-exit');
  await page.fill('#bond-exit', '0x70997970C51812dc3A010C7d01b50e0d17dc79C8');
  await page.click('#bond-quote');
  await page.waitForSelector('#ask-bond:not([hidden])', { timeout: 20000 });
  const exitLines = await page.locator('#ask-bond-lines li').allTextContents();
  // the lab's form: the exit change is a bond-with-exit with a top-up; on a version 2 registry the chain refuses it
  // at the estimate and the quote is not sendable, on version 3 it is
  const regVersion = Number((miningEyebrow.match(/registry version (\d+)/) || [])[1] || 0);
  const exitSendable = !(await page.locator('#bond-go').isDisabled());
  // the drive's wallet is made here and never funded, so on version 3 the quote's refusal must be the balance's
  // ("The balance does not cover the total.") and never the registry's; the sendable row on version 3 is the
  // authority smoke's (a funded holder: the exit changed with a top-up, devnet5 run 6, block 192)
  const exitSim = (await page.locator('#ask-bond-sim').textContent()).trim();
  const unfunded = /The balance does not cover the total/.test(exitSim);
  const exitOk = regVersion === 3 ? (unfunded ? !exitSendable : exitSendable) : !exitSendable;
  check('set_exit_lab_form', exitLines[0].startsWith('You bond 0.001 IGN to the identity key 0x4b4b…4b4b with the exit address 0x709979…79C8') && exitOk, exitLines[0] + ' | registry version ' + regVersion + ', sendable ' + exitSendable + (unfunded ? ' (the wallet is unfunded: the balance refusal, not the registry’s)' : '') + ' | ' + exitSim.slice(0, 120));
  await page.click('#ask-bond-no');
  // the claim needs a proof file: without one the engine refuses before any quote
  await page.selectOption('#bond-action', 'claim-exit');
  await page.fill('#bond-pi', '/nonexistent/claim.bin');
  await page.fill('#bond-share', '0x' + '11'.repeat(32));
  await page.click('#bond-quote');
  await page.waitForTimeout(1500);
  check('claim_needs_proof', (await page.locator('#bond-err').textContent()).length > 0, (await page.locator('#bond-err').textContent()).slice(0, 80));
} else {
  check('mining_off_without_registry', await page.locator('#draw-go').isDisabled() && await page.locator('#bond-quote').isDisabled());
}
// the receipts card names a bad line
await page.fill('#rc-pool', '0x' + '11'.repeat(32));
await page.fill('#rc-member', '0x' + '22'.repeat(32));
await page.fill('#rc-lines', JSON.stringify({ seq: 1, share_hash: '33'.repeat(32), key: '22'.repeat(32), weight: 5, total: 5, digest: '44'.repeat(32), prev: '00'.repeat(32), pool_key: '11'.repeat(32), sig: '55'.repeat(64), job_id: 'j', nonce: 1 }));
await page.click('#rc-go');
await page.waitForSelector('#rc-summary:not([hidden])', { timeout: 10000 });
check('receipts_bad_named', (await page.locator('#rc-rows .hrow').count()) === 1 && /digest|signature/.test(await page.locator('#rc-rows').textContent()), (await page.locator('#rc-summary').textContent()).trim());

// lock and unlock
await page.click('#btn-lock');
await page.waitForSelector('#screen-unlock:not([hidden])', { timeout: 10000 });
check('lock_screen', await page.locator('#unlock-form').isVisible());
// the lock screen in a browser (no window host): the password is the primary control and the prompt's block stays
// hidden (lock-screen.js layout; OLD-BASE.md step 3, the page)
check('lock_password_primary', !(await page.locator('#unlock-bio').isVisible()) && await page.locator('#unlock-form').isVisible());
await page.fill('#unlock-pw', 'wrong password');
await page.click('#unlock-submit');
await page.waitForTimeout(1500);
check('wrong_password_refused', (await page.locator('#unlock-err').textContent()).includes('wrong password'));
await page.fill('#unlock-pw', 'correct horse battery');
await page.click('#unlock-submit');
await page.waitForSelector('#screen-home:not([hidden])', { timeout: 10000 });
check('unlocked', await page.locator('#page-home').isVisible());

// settings: the policy table and the websites card
await page.click('[data-page="settings"]');
await page.waitForTimeout(500);
check('policy_table', (await page.locator('#policy-kv .k').count()) === 4);
// the Touch ID row in a browser: no host, so the words say the app window is needed and "Turn on" is off; the
// Updates card carries the check's line (the host's 404 while latest waits for its signature, or the version);
// the node card's scan line reads how far the chain is read for this address
check('bio_row_browser', (await page.locator('#bio-sentence').textContent()).includes('needs the Igneum Wallet app window') && await page.locator('#bio-toggle-btn').isDisabled(), (await page.locator('#bio-sentence').textContent()).slice(0, 80));
check('updates_card', (await page.locator('#s-update-line').textContent()).length > 0 && await page.locator('#auto-update').isChecked(), (await page.locator('#s-update-line').textContent()).slice(0, 100));
check('websites_card', await page.locator('#bridge-on-row').isVisible() && (await page.locator('#bridge-status').textContent()).includes('127.0.0.1'), (await page.locator('#bridge-status').textContent()).trim());

// the backup reveal needs the password
await page.click('#backup-toggle');
await page.fill('#backup-pw', 'correct horse battery');
await page.click('#backup-show');
await page.waitForSelector('#backup-out:not([hidden])', { timeout: 10000 });
check('backup_words_match', (await page.locator('#backup-words li').allTextContents()).join(' ') === words.join(' '));

check('no_page_errors', errors.length === 0, errors.slice(0, 2).join(' | '));
await page.screenshot({ path: process.env.SHOT || '/tmp/igneum-wallet-page.png' });
await browser.close();
console.log(fails === 0 ? 'PAGE-DRIVE PASS' : `PAGE-DRIVE FAIL (${fails})`);
process.exit(fails === 0 ? 0 : 1);
