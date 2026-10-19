// node --test wallet/app/ui/settlement.test.mjs (on the box)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const S = require('./settlement.js');

test('rows carry the engine words and never say final', () => {
  assert.deepEqual(S.row({ word: 'sent', why: 'waiting for a block; sent 40 s ago' }), { word: 'sent', tone: 'running', why: 'waiting for a block; sent 40 s ago' });
  assert.equal(S.row({ word: 'included', why: 'x' }).tone, 'close');
  assert.equal(S.row({ word: 'settled', why: 'x' }).tone, 'met');
  assert.equal(S.row({ word: 'failed', why: 'x' }).tone, 'bad');
  assert.equal(S.row({ word: 'dropped', why: 'x' }).tone, 'open');
  assert.equal(S.row(null).word, 'pending');
  for (const w of ['sent', 'included', 'settled', 'failed', 'dropped']) {
    assert.ok(!S.row({ word: w, why: '' }).word.includes('final'));
  }
});

test('progress line', () => {
  assert.equal(S.progress({ word: 'included', depth: 3, band_blocks: 6 }), 'included, 3 of 6 blocks');
  assert.equal(S.progress({ word: 'settled' }), 'settled by your policy');
  assert.equal(S.progress({ word: 'sent' }), 'sent');
});

test('policy lines', () => {
  const policy = { bands: [
    { up_to: '0x8ac7230489e80000', blocks: 6, work_minutes: 2, note: null },
    { up_to: null, blocks: 480, work_minutes: 120, note: 'ask the receiver what they require' },
  ] };
  const lines = S.policyLines(policy);
  assert.equal(lines.length, 2);
  assert.equal(lines[0].k, '0 to 10 IGN');
  assert.equal(lines[0].v, '6 blocks and 2 min of work');
  assert.equal(lines[1].k, 'over 10 and above');
  assert.ok(lines[1].v.endsWith('ask the receiver what they require'));
});

test('reorg line', () => {
  assert.equal(S.reorgLine({ deepest_reorg_7d: 0 }), 'no reorganisation seen by this wallet in 7 days');
  assert.equal(S.reorgLine({ deepest_reorg_7d: 1 }), 'deepest reorganisation seen in 7 days: 1 block');
  assert.equal(S.reorgLine({ deepest_reorg_7d: 4 }), 'deepest reorganisation seen in 7 days: 4 blocks');
});
