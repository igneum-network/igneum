// node --test app/igneum-wallet/ui/view.test.mjs   (no dependencies)
// Loads the View block of app.js (plain browser JS: the file is run with `module` defined and no `document`, so only
// the pure block executes) and checks the words each page shows for a given state (wallet-ui-3).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'app.js'), 'utf8');
const mod = { exports: {} };
new Function('module', src)(mod);
const V = mod.exports.View;

const ME = '0x7e5f4552091a69125d5dfcb7b8c2659029395bdf', B = '0x2b5ad5c4795c026514f8317c7a215e218dccd6cf';
const NOW = 1_791_000_000;
const node = (over) => ({ source: 'miner', state: 'ok', chain_id: 7762959, chain: 'igneum-devnet-20', block: 140286, evm: 'http://127.0.0.1:26790', grpc: '127.0.0.1:26610', synced: true, message: "using the miner app's node on this machine", finality_active: true, latest_locked: 4674, ...over });
const fin = (over) => ({ verified_index: 4674, verified_hash: '0xf1', chain_number: 140246, signers: 11, voters: 12, fraction_total: 0.912, fraction_active: 0.98, weights_exact: true, verified_at: NOW - 14, message: 'checkpoint 4674 verified here', ...over });
const st = (over) => ({ version: '0.1.5', phase: 'home', balance: '31.6321', balance_known: true, scanning: false, scanned_to: 140286, node: node(), finality: fin(), settings: { network: 'devnet', start_at_login: false, auto_update: true, idle_lock_min: 5, ask_on_open: true }, update: { status: 'current', version: '0.1.5', checked_at: NOW - 780 }, now: NOW, ...over });
const entry = (over) => ({ hash: '0x' + 'c3d4'.repeat(16), kind: 'received', block: 140262, block_hash: '0xabcd', from: B, to: ME, value: '250000000000000000', fee: '25380000000000', ok: true, time: NOW - 140, finality: 'in_block', checkpoint: null, note: '', ...over });

test('the five pages and their order', () => {
  assert.deepEqual(V.PAGES.map((p) => p.id), ['home', 'send', 'receive', 'history', 'settings']);
  assert.equal(V.page('history').title, 'History');
  assert.equal(V.page('nonsense').id, 'home');
});

test('numbers: IGN from wei, short addresses, times', () => {
  assert.equal(V.ign('1500000000000000000'), '1.5');
  assert.equal(V.ign('250000000000000000', 6), '0.25');
  assert.equal(V.ign('10140000000000000000', 4), '10.14');
  assert.equal(V.ign('0'), '0');
  assert.equal(V.ign('nonsense'), '0');
  assert.equal(V.ign('76140000000000', 9), '0.00007614');
  assert.equal(V.shortHex('0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf'), '0x7E5F45\u20265Bdf');
  assert.equal(V.shortHex(''), '');
  assert.equal(V.withCommas(140286), '140,286');
  assert.equal(V.ago(14), '14 s ago'); assert.equal(V.ago(780), '13 min ago'); assert.equal(V.ago(7200), '2 h ago');
  assert.match(V.timeWord(NOW - 140, NOW), /^\d\d:\d\d$/);
  assert.match(V.timeWord(NOW - 86400 * 3, NOW), /^\d+ \w+ \d\d:\d\d$/);
});

test('the balance: the number only when known, else one line that says why (never a 0)', () => {
  assert.deepEqual(V.balanceLine(st()), { known: true, text: '' });
  assert.deepEqual(V.balanceLine(st({ balance_known: false })), { known: false, text: 'reading the balance' });
  assert.deepEqual(V.balanceLine(st({ balance_known: false, scanning: true, scanned_to: 61200, node: node({ block: 140270 }) })), { known: false, text: 'reading the chain, 61,200 of 140,270 blocks' });
  assert.deepEqual(V.balanceLine(st({ balance_known: false, node: node({ state: 'starting' }) })), { known: false, text: 'waiting for the node to start' });
  assert.deepEqual(V.balanceLine(st({ balance_known: false, node: node({ state: 'off', source: 'none' }) })), { known: false, text: 'waiting for a node' });
});

test('money first when a price exists; else the one grey line, devnet or not', () => {
  assert.deepEqual(V.moneyLine(st()), { money: '', text: 'no market price on devnet: coins have no value' });
  assert.deepEqual(V.moneyLine(st({ settings: { network: 'testnet' } })), { money: '', text: 'no market price yet' });
  assert.deepEqual(V.moneyLine(st({ price_gbp_per_ign: 0.5 })), { money: '\u00a315.82', text: "at today's price" });
  // a price with an unknown balance is no money line
  assert.equal(V.moneyLine(st({ price_gbp_per_ign: 0.5, balance_known: false })).money, '');
});

test('the node line: the state word, the source in plain words, the block, the verification', () => {
  assert.deepEqual(V.nodeLine(st()), { text: "Node synced \u00b7 the miner app's node \u00b7 block 140,286 \u00b7 verified to checkpoint 4,674", sub: '', tone: 'ok' });
  const pub = st({ node: node({ source: 'public', grpc: '', finality_active: false, latest_locked: 0, message: 'using the public RPC; no node here, so finality cannot be verified' }), finality: fin({ verified_index: 0, chain_number: null, message: 'no node here' }) });
  assert.equal(V.nodeLine(pub).text, 'Node synced \u00b7 the public RPC \u00b7 block 140,286 \u00b7 certificates cannot be verified without a node');
  const own = st({ node: node({ source: 'own', state: 'starting', block: 0, message: 'the bundled node is starting; the chain syncs from the seed' }), finality: fin({ verified_index: 0 }) });
  assert.deepEqual(V.nodeLine(own), { text: 'Node starting \u00b7 the bundled node', sub: 'the bundled node is starting; the chain syncs from the seed', tone: '' });
  const none = st({ node: node({ source: 'none', state: 'off', block: 0, message: 'no node: install Igneum Miner, or wait for the bundled node' }), finality: fin({ verified_index: 0 }) });
  assert.deepEqual(V.nodeLine(none), { text: 'Node not found', sub: 'no node: install Igneum Miner, or wait for the bundled node', tone: 'bad' });
  const lost = V.nodeLine(st({ node: node({ state: 'lost', message: 'history: connect 127.0.0.1:26790: Connection refused (os error 61)' }) }));
  assert.equal(lost.tone, 'bad'); assert.equal(lost.sub, 'the node stopped answering; trying again');
  assert.equal(V.nodeLine(st({ finality: fin({ verified_index: 0 }) })).text, "Node synced \u00b7 the miner app's node \u00b7 block 140,286 \u00b7 waiting for the first certificate");
  assert.equal(V.sourceWords('external'), 'the node named by the environment');
});

test('the node details: every old card row with a one-line meaning, no n/a', () => {
  const rows = V.nodeDetails(st(), NOW);
  const keys = rows.map((r) => r[0]);
  assert.deepEqual(keys, ['source', 'chain', 'block', 'endpoint', 'finality on the node', 'verified here', 'signed by', 'checkpoint height', 'weights', 'checked']);
  assert.equal(rows[5][1], 'checkpoint 4,674'); assert.equal(rows[5][3], 'ok');
  assert.equal(rows[6][1], '11 of 12 voters'); assert.match(rows[6][2], /^91\.2% of all weight/);
  assert.equal(rows[9][1], '14 s ago');
  for (const r of rows) assert.ok(!/n\/a/.test(r[1]) && r[1] !== '', r[0]);
  const none = V.nodeDetails(st({ node: node({ source: 'public', evm: 'https://rpc.example', finality_active: false }), finality: fin({ verified_index: 0, message: 'no node here' }) }), NOW);
  assert.equal(none.find((r) => r[0] === 'finality on the node')[1], 'not checkable without a node');
  assert.deepEqual(none.find((r) => r[0] === 'verified here').slice(1), ['nothing yet', 'no node here', 'dim']);
});

test('the pill: the wallet\'s own words', () => {
  assert.deepEqual(V.pillWords(st()), { text: 'synced', cls: 'on' });
  assert.deepEqual(V.pillWords(st({ node: node({ source: 'public' }) })), { text: 'public rpc', cls: 'on' });
  assert.deepEqual(V.pillWords(st({ node: node({ state: 'starting' }) })), { text: 'starting', cls: '' });
  assert.deepEqual(V.pillWords(st({ node: node({ state: 'off', source: 'none' }) })), { text: 'no node', cls: 'warn' });
  assert.deepEqual(V.pillWords(st({ node: node({ state: 'lost' }) })), { text: 'node lost', cls: 'warn' });
  assert.deepEqual(V.pillWords(st({ phase: 'unlock' })), { text: 'locked', cls: '' });
  assert.deepEqual(V.pillWords(st({ quitting: true })), { text: 'stopping', cls: '' });
});

test('one state word per row: the wallet\'s verified final wins, failed from the receipt, else the node\'s word, else the label', () => {
  const e = entry();
  assert.deepEqual(V.stateWord(entry({ finality: 'final', checkpoint: 4673, block: 139900 }), 'executed'), { word: 'finalised', tone: 'ok', why: 'under checkpoint 4,673, verified here' });
  assert.deepEqual(V.stateWord(entry({ finality: 'failed', ok: false }), 'executed'), { word: 'failed', tone: 'bad', why: 'the execution failed; the fee was still paid' });
  assert.deepEqual(V.stateWord(e, 'pending'), { word: 'pending', tone: '', why: 'waiting for a block' });
  assert.deepEqual(V.stateWord(e, 'included'), { word: 'included', tone: '', why: 'in block 140,262, not executed yet' });
  assert.deepEqual(V.stateWord(e, 'executed'), { word: 'executed', tone: 'ink', why: 'in block 140,262' });
  assert.deepEqual(V.stateWord(e, 'proven'), { word: 'proven', tone: 'ink', why: 'in block 140,262, proof paid' });
  // the node says finalised but this wallet has not verified the certificate: the word, not the molten tone
  assert.deepEqual(V.stateWord(e, 'finalised'), { word: 'finalised', tone: 'ink', why: 'under a locked checkpoint, not yet verified here' });
  // section 9 of docs/spec/finality-guarantees.md (8 October 2026): a block under a locked checkpoint is finalised even while
  // the network's finality is paused; the pause is the network's state word, never a transaction's. Known-failed first: the
  // node's "finality not active" word was shown on the row.
  assert.deepEqual(V.stateWord(e, 'finality not active'), { word: 'finalised', tone: 'ink', why: 'under a locked checkpoint, not yet verified here; finality is paused on the network above it' });
  // review B F04 (the founder, 8 October 2026): a recovery lock is labelled "recovery", never plain "final": the node's word
  // for a transaction under one (the node lane's lockKind) reads on the row as finalised by a recovery lock. Known-failed
  // first: the word fell through to the label.
  assert.deepEqual(V.stateWord(e, 'finalised by a recovery lock'), { word: 'finalised (recovery lock)', tone: 'ink', why: 'under a recovery lock: the surviving majority’s lock after a long pause, not a certified checkpoint; not yet verified here' });
  assert.equal(V.stateWord(e, 'unknown').word, 'pending');
  // no node word yet (the public RPC, or not asked): the wallet's label
  assert.deepEqual(V.stateWord(e, ''), { word: 'included', tone: '', why: 'in block 140,262' });
  assert.deepEqual(V.stateWord(entry({ finality: 'pending', block: 0 }), ''), { word: 'pending', tone: '', why: 'waiting for a block' });
  assert.equal(V.stateWord(entry({ finality: 'pending', block: 0 }), 'included').why, 'in a block, not executed yet');
  // a reward has no transaction hash: executed in its block, finalised under a verified checkpoint
  const r = entry({ hash: 'reward-140201-0', kind: 'reward', from: '', block: 140201 });
  assert.deepEqual(V.stateWord(r, ''), { word: 'executed', tone: 'ink', why: 'in block 140,201' });
  assert.equal(V.stateWord(entry({ hash: 'proving-1-0', kind: 'proving', finality: 'final', checkpoint: 9 }), '').word, 'finalised');
  // never a stronger word than the chain's: the words the row can show are exactly the node's plus failed
  const words = new Set(['pending', 'included', 'executed', 'proven', 'finalised', 'finalised (recovery lock)', 'failed']);
  for (const w of ['pending', 'included', 'executed', 'proven', 'finalised', 'finality not active', 'finalised by a recovery lock', 'unknown', '']) assert.ok(words.has(V.stateWord(e, w).word), w);
});

test('the row model: kind word, who, sign, amount, the rows that need the node\'s word', () => {
  const m = V.rowModel(entry(), 'included', NOW);
  assert.equal(m.kind, 'Received'); assert.equal(m.who, 'from 0x2b5ad5\u2026d6cf'); assert.equal(m.amount, '+0.25'); assert.equal(m.in, true); assert.equal(m.synthetic, false); assert.equal(m.nodeWord, 'included');
  const s = V.rowModel(entry({ kind: 'sent', from: ME, to: B, value: '1500000000000000000' }), '', NOW);
  assert.equal(s.kind, 'Sent'); assert.equal(s.who, 'to 0x2b5ad5\u2026d6cf'); assert.equal(s.amount, '\u22121.5'); assert.equal(s.in, false);
  assert.equal(V.rowModel(entry({ kind: 'self', from: ME, to: ME }), '', NOW).who, 'to yourself');
  assert.equal(V.rowModel(entry({ hash: 'reward-1-0', kind: 'reward' }), '', NOW).who, 'block reward');
  assert.equal(V.rowModel(entry({ hash: 'proving-1-0', kind: 'proving' }), '', NOW).kind, 'Proving payout');
  const list = [entry({ hash: '0x01', finality: 'pending' }), entry({ hash: '0x02' }), entry({ hash: 'reward-1-0', kind: 'reward' }), entry({ hash: '0x03', finality: 'final', checkpoint: 1 }), entry({ hash: '0x04', finality: 'failed' }), entry({ hash: '0x05' })];
  assert.deepEqual(V.needsNodeWord(list, 12), ['0x01', '0x02', '0x05']);
  assert.deepEqual(V.needsNodeWord(list, 2), ['0x01', '0x02']);
});

test('send: the fee line, the gas words behind Details, the question in place', () => {
  const q = { to: B, display_to: '0x2B5AD5c4795c026514f8317c7a215E218DCCD6cF', value_ign: '1.5', fee_max_ign: '0.00007614', total_max_ign: '1.50007614', gas: 25380, base_fee_gwei: '1', tip_gwei: '1', max_fee: '3000000000' };
  assert.deepEqual(V.feeWords(null), { line: 'shown when you review', detail: '' });
  const f = V.feeWords(q);
  assert.equal(f.line, 'at most 0.00007614 IGN');
  assert.equal(f.detail, 'Gas 25,380 at a base fee of 1 gwei (burned) plus a 1 gwei tip (to the miner), capped at 3 gwei; what is not used comes back.');
  assert.equal(V.gwei('1500000000'), '1.5 gwei'); assert.equal(V.gwei('25000000000'), '25 gwei'); assert.equal(V.gwei('250000'), '0.00025 gwei'.replace('0.00025', '0'));
  assert.deepEqual(V.sendAsk(q, true, 'Touch ID'), { text: 'Send 1.5 IGN to 0x2B5AD5\u2026D6cF? Fee at most 0.00007614 IGN; 1.50007614 IGN leaves the wallet at most.', button: 'Confirm with Touch ID' });
  assert.equal(V.sendAsk(q, false).button, 'Send now');
  assert.equal(V.sendAsk(q, true, 'Windows Hello').button, 'Confirm with Windows Hello');
});

test('settings: the Touch ID sentence, the idle sentence, the update card note', () => {
  assert.equal(V.bioSentence({ enrolled: true }, 'Touch ID', true), 'Touch ID is on. It unlocks the wallet, confirms each send and shows the backup; the password still works everywhere.');
  assert.equal(V.bioSentence({ enrolled: false, available: true }, 'Touch ID', false), 'Touch ID needs the Igneum Wallet app window; this page is open in a browser.');
  assert.equal(V.bioSentence({ enrolled: false, available: false }, 'Touch ID', true), 'Touch ID is not set up on this Mac.');
  assert.equal(V.bioSentence({ enrolled: false, available: false, message: 'Windows Hello is not configured.' }, 'Windows Hello', true), 'Windows Hello is not configured.');
  assert.equal(V.bioSentence({ enrolled: false, available: true }, 'Windows Hello', true), 'Unlock, confirm each send and show the backup with Windows Hello. The password still works everywhere.');
  assert.equal(V.idleSentence(5, 'Touch ID'), 'The key is cleared after 5 minutes without you; Touch ID or the password opens it again.');
  assert.equal(V.idleSentence(0, 'Touch ID'), 'The wallet stays open until you lock it.');
  assert.equal(V.updateNote({ status: 'current', version: '0.1.5', checked_at: NOW - 780 }, NOW), 'Up to date, checked 13 min ago.');
  assert.equal(V.updateNote({ status: 'unknown', version: '' }, NOW), 'Not checked yet.');
  assert.equal(V.updateNote({ status: 'ready', version: '0.1.6', wait: 'installs as soon as nothing is being sent' }, NOW), 'Igneum Wallet 0.1.6 is ready. Installs as soon as nothing is being sent.');
  assert.equal(V.updateNote({ status: 'current', version: '0.1.5', updated_from: '0.1.4', checked_at: NOW - 60 }, NOW), 'Updated from 0.1.4. Up to date, checked 1 min ago.');
  assert.equal(V.updateNote({ status: 'off', version: '' }, NOW), 'Updates are off in this build.');
  assert.equal(V.updateLine({ status: 'downloading', version: '0.1.6', size: 20080717, progress: 0.43 }).text, 'Downloading Igneum Wallet 0.1.6 (20 MB): 43%');
  assert.equal(V.updateLine({ status: 'current', version: '0.1.5' }), null);
});

// the page bridge (0.1.6, 8 October 2026): what the window says for a page's request (connect, a transaction, a message,
// typed data), the Settings card's site rows, the lock-screen line while a page waits. Known-failed first on 0.1.5:
// View.bridgeWords is not a function.
test('the page bridge: the words for each request kind, the site rows, the waiting line', () => {
  const connect = V.bridgeWords({ id: '1-ab', kind: 'connect', origin: 'https://igneum.network', created_at: 1 });
  assert.equal(connect.title, 'igneum.network wants to connect');
  assert.equal(connect.lines[0], 'It will see your address and may ask you to sign or send. Nothing leaves without your word here.');
  assert.equal(connect.yes, 'Connect'); assert.equal(connect.no, 'Decline');
  const send = V.bridgeWords({ id: '2-cd', kind: 'send', origin: 'https://igneum.network', to: '0x9a6fa842c4e58a87aef1f3ad15233d99283002b7', to_display: '0x9a6fA842C4e58A87AEF1F3aD15233d99283002B7', value: '1000000000000000000', value_ign: '1', data_len: 36, selector: '0x38ed1739', gas: 184000, fee_max_ign: '0.000368', total_max_ign: '1.000368', confirm_needed: false });
  assert.equal(send.title, 'igneum.network asks you to send');
  assert.equal(send.amount, '1 IGN');
  assert.equal(send.lines[0], 'To 0x9a6fA8…02B7, a contract call (36 bytes, 0x38ed1739).');
  assert.equal(send.lines[1], 'Fee up to 0.000368 IGN (184,000 gas). Total up to 1.000368 IGN.');
  assert.equal(send.yes, 'Send'); assert.equal(send.no, 'Decline');
  const plain = V.bridgeWords({ id: '3', kind: 'send', origin: 'https://igneum.network', to_display: '0x9a6fA842C4e58A87AEF1F3aD15233d99283002B7', value_ign: '0.5', data_len: 0, selector: '', gas: 21000, fee_max_ign: '0.000042', total_max_ign: '0.500042' });
  assert.equal(plain.lines[0], 'To 0x9a6fA8…02B7, a plain transfer.');
  const unpriced = V.bridgeWords({ id: '4', kind: 'send', origin: 'https://igneum.network', to_display: '0x9a6fA842C4e58A87AEF1F3aD15233d99283002B7', value: '0', data_len: 4, selector: '0xd0e30db0' });
  assert.equal(unpriced.amount, '0 IGN'); assert.equal(unpriced.lines[1], 'Pricing the fee with the node.');
  const touch = V.bridgeWords({ id: '5', kind: 'send', origin: 'https://igneum.network', to_display: '0x9a6fA842C4e58A87AEF1F3aD15233d99283002B7', value_ign: '1', data_len: 0, selector: '', gas: 21000, fee_max_ign: '0.00004', total_max_ign: '1.00004', confirm_needed: true }, 'Touch ID');
  assert.equal(touch.yes, 'Send with Touch ID');
  const sign = V.bridgeWords({ id: '6', kind: 'sign', origin: 'https://igneum.network', text: 'Sign in to Igneum Swap\nnonce: 42', bytes: 31 });
  assert.equal(sign.title, 'igneum.network asks you to sign a message');
  assert.equal(sign.message, 'Sign in to Igneum Swap\nnonce: 42');
  assert.equal(sign.lines[0], 'Signing proves this wallet is yours. It moves no coins.');
  assert.equal(sign.yes, 'Sign');
  const bin = V.bridgeWords({ id: '7', kind: 'sign', origin: 'https://igneum.network', text: null, bytes: 32, hex: '0x0102' });
  assert.equal(bin.message, '32 bytes: 0x0102');
  const typed = V.bridgeWords({ id: '8', kind: 'typed', origin: 'https://igneum.network', domain: 'Igneum Swap', primary_type: 'Permit', message: '{\n  "owner": "0x1"\n}' });
  assert.equal(typed.title, 'igneum.network asks you to sign typed data');
  assert.equal(typed.lines[0], 'Permit for Igneum Swap. Read it before you sign: a permit can let a contract spend tokens.');
  assert.equal(typed.message, '{\n  "owner": "0x1"\n}');
  // the lock-screen line and the site rows
  assert.equal(V.bridgeWaiting([connect, send].map((x) => ({ origin: 'https://igneum.network' }))), 'igneum.network is waiting: unlock to answer it');
  assert.equal(V.bridgeWaiting([]), '');
  const rows = V.siteRows([{ origin: 'https://igneum.network', approved_at: 1_800_000_000 - 3600, last_seen: 1_800_000_000 - 30 }], 1_800_000_000);
  assert.equal(rows[0].host, 'igneum.network'); assert.equal(rows[0].line, 'connected 1 h ago · last call 30 s ago');
  assert.equal(V.siteRows([], 1).length, 0);
  assert.equal(V.bridgeStatus({ on: true, listening: true, port: 26811, sites: [] }), 'On. Websites you approve can connect through port 26811 on this machine only.');
  assert.equal(V.bridgeStatus({ on: true, listening: false, port: 26811, reason: 'port 26811 is not free: in use', sites: [] }), 'Not listening: port 26811 is not free: in use. Quit whatever holds the port and open the wallet again.');
  assert.equal(V.bridgeStatus({ on: false, listening: true, port: 26811, sites: [] }), 'Off. No website can connect; the switch turns it on.');
});
