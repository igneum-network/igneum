// The settlement words on the page (docs/product-b/DESIGN.md, section 2). Pure: the engine's view for a
// transaction comes in, the row's word, tone and why-line come out. Loaded by settlement.test.mjs under node with
// `module` defined; in the page it defines window.Settlement. Nothing here ever says "final".
'use strict';

var Settlement = (function () {
  // the engine's view: {word, why, depth, met, band_blocks, band_work_minutes}
  function row(view) {
    if (!view) return { word: 'pending', tone: '', why: 'waiting for the node' };
    switch (view.word) {
      // the tones are the design system's (ADOPTION.md 5.3): sent is running (blue), included is close (molten:
      // the policy is not yet met), settled is met (green, the only green on the page), dropped is open (ash),
      // failed is bad (red); the word and the why line are unchanged
      case 'sent': return { word: 'sent', tone: 'running', why: view.why };
      case 'included': return { word: 'included', tone: 'close', why: view.why };
      case 'settled': return { word: 'settled', tone: 'met', why: view.why };
      case 'failed': return { word: 'failed', tone: 'bad', why: view.why };
      case 'dropped': return { word: 'dropped', tone: 'open', why: view.why };
    }
    return { word: view.word || 'unknown', tone: '', why: view.why || '' };
  }

  // the short progress line on the sent screen: "included, 3 of 6 blocks" or "settled"
  function progress(view) {
    if (!view) return '';
    if (view.word === 'included' && view.band_blocks) return 'included, ' + view.depth + ' of ' + view.band_blocks + ' blocks';
    if (view.word === 'settled') return 'settled by your policy';
    return view.word;
  }

  // the policy table for Settings: one line per band
  function policyLines(policy) {
    if (!policy || !policy.bands) return [];
    var lines = [], lower = '0';
    for (var i = 0; i < policy.bands.length; i++) {
      var b = policy.bands[i];
      var upper = b.up_to == null ? 'and above' : 'to ' + ignOf(b.up_to) + ' IGN';
      lines.push({ k: lower + ' ' + upper, v: b.blocks + ' blocks and ' + b.work_minutes + ' min of work' + (b.note ? '; ' + b.note : '') });
      if (b.up_to != null) lower = 'over ' + ignOf(b.up_to);
    }
    return lines;
  }

  // a hex quantity of wei to whole IGN for the band labels (bands are whole numbers)
  function ignOf(hexWei) {
    var w; try { w = BigInt(hexWei); } catch (e) { return '?'; }
    return (w / 1000000000000000000n).toString();
  }

  // the node card's reorganisation line
  function reorgLine(node) {
    if (!node) return '';
    var d = Number(node.deepest_reorg_7d || 0);
    if (d === 0) return 'no reorganisation seen by this wallet in 7 days';
    return 'deepest reorganisation seen in 7 days: ' + d + ' block' + (d === 1 ? '' : 's');
  }

  return { row: row, progress: progress, policyLines: policyLines, reorgLine: reorgLine };
})();

if (typeof module !== 'undefined') module.exports = Settlement;
