// qr.js: a QR code encoder for the Receive page (0.0.19, 16 October 2026): byte mode, error correction level M,
// versions 1 to 10 (up to 213 bytes), the mask chosen by the standard's four penalty rules; ISO/IEC 18004 as the
// public standard describes it, written here so the page runs only its own files (the content security policy).
// QR.encode(text) returns { size, modules } (modules[y][x] true = dark); QR.draw(canvas, text, px) paints it.
var QR = (function () {
  'use strict';
  // the byte-mode capacity at level M, the error codewords a block, and the blocks (count, data codewords) per version
  var TABLE = [
    null,
    { cap: 14, ec: 10, blocks: [[1, 16]], align: [] },
    { cap: 26, ec: 16, blocks: [[1, 28]], align: [6, 18] },
    { cap: 42, ec: 26, blocks: [[1, 44]], align: [6, 22] },
    { cap: 62, ec: 18, blocks: [[2, 32]], align: [6, 26] },
    { cap: 84, ec: 24, blocks: [[2, 43]], align: [6, 30] },
    { cap: 106, ec: 16, blocks: [[4, 27]], align: [6, 34] },
    { cap: 122, ec: 18, blocks: [[4, 31]], align: [6, 22, 38] },
    { cap: 152, ec: 22, blocks: [[2, 38], [2, 39]], align: [6, 24, 42] },
    { cap: 180, ec: 22, blocks: [[3, 36], [2, 37]], align: [6, 26, 46] },
    { cap: 213, ec: 26, blocks: [[4, 43], [1, 44]], align: [6, 28, 50] }
  ];
  // GF(256) with the standard's primitive polynomial x^8 + x^4 + x^3 + x^2 + 1
  var EXP = new Array(512), LOG = new Array(256);
  (function () {
    var x = 1;
    for (var i = 0; i < 255; i++) { EXP[i] = x; LOG[x] = i; x <<= 1; if (x & 0x100) x ^= 0x11d; }
    for (var j = 255; j < 512; j++) EXP[j] = EXP[j - 255];
  })();
  function mul(a, b) { return a === 0 || b === 0 ? 0 : EXP[LOG[a] + LOG[b]]; }
  function generator(n) {
    var g = [1];
    for (var i = 0; i < n; i++) {
      var next = new Array(g.length + 1).fill(0);
      for (var j = 0; j < g.length; j++) { next[j] ^= g[j]; next[j + 1] ^= mul(g[j], EXP[i]); }
      g = next;
    }
    return g;
  }
  function ecCodewords(data, n) {
    var g = generator(n), r = new Array(n).fill(0);
    for (var i = 0; i < data.length; i++) {
      var f = data[i] ^ r[0];
      r.shift(); r.push(0);
      if (f !== 0) for (var j = 0; j < n; j++) r[j] ^= mul(g[j + 1], f);
    }
    return r;
  }
  function utf8(text) {
    var out = [], s = unescape(encodeURIComponent(text));
    for (var i = 0; i < s.length; i++) out.push(s.charCodeAt(i));
    return out;
  }
  function bchFormat(bits5) { // 15-bit format information, BCH(15,5) with the generator 10100110111, masked with 101010000010010
    var d = bits5 << 10;
    for (var i = 14; i >= 10; i--) if (d & (1 << i)) d ^= 0x537 << (i - 10);
    return ((bits5 << 10) | d) ^ 0x5412;
  }
  function bchVersion(v) { // 18-bit version information, BCH(18,6) with the generator 1111100100101
    var d = v << 12;
    for (var i = 17; i >= 12; i--) if (d & (1 << i)) d ^= 0x1f25 << (i - 12);
    return (v << 12) | d;
  }
  function encode(text) {
    var bytes = utf8(text), v = 1;
    while (v <= 10 && TABLE[v].cap < bytes.length) v++;
    if (v > 10) throw new Error('too long for this encoder');
    var t = TABLE[v], size = 17 + 4 * v;
    var totalData = 0; t.blocks.forEach(function (b) { totalData += b[0] * b[1]; });
    // the data bit stream: mode 0100, the count (8 bits up to version 9, 16 from version 10), the bytes, the terminator, the pad bytes
    var bits = [];
    function put(val, n) { for (var i = n - 1; i >= 0; i--) bits.push((val >> i) & 1); }
    put(4, 4); put(bytes.length, v >= 10 ? 16 : 8); bytes.forEach(function (b) { put(b, 8); });
    var limit = totalData * 8;
    put(0, Math.min(4, limit - bits.length));
    while (bits.length % 8) bits.push(0);
    var words = [];
    for (var i = 0; i < bits.length; i += 8) { var w = 0; for (var k = 0; k < 8; k++) w = (w << 1) | bits[i + k]; words.push(w); }
    for (var p = 0; words.length < totalData; p++) words.push(p % 2 === 0 ? 0xec : 0x11);
    // the blocks, each with its error codewords, interleaved
    var dataBlocks = [], ecBlocks = [], at = 0;
    t.blocks.forEach(function (b) { for (var n = 0; n < b[0]; n++) { var d = words.slice(at, at + b[1]); at += b[1]; dataBlocks.push(d); ecBlocks.push(ecCodewords(d, t.ec)); } });
    var out = [], maxLen = 0;
    dataBlocks.forEach(function (d) { if (d.length > maxLen) maxLen = d.length; });
    for (var c = 0; c < maxLen; c++) dataBlocks.forEach(function (d) { if (c < d.length) out.push(d[c]); });
    for (var e = 0; e < t.ec; e++) ecBlocks.forEach(function (d) { out.push(d[e]); });
    // the matrix: null = free, true/false = fixed dark/light
    var m = [], fixed = [];
    for (var y = 0; y < size; y++) { m.push(new Array(size).fill(false)); fixed.push(new Array(size).fill(false)); }
    function set(x, y, dark) { m[y][x] = !!dark; fixed[y][x] = true; }
    function finder(cx, cy) {
      for (var dy = -4; dy <= 4; dy++) for (var dx = -4; dx <= 4; dx++) {
        var x = cx + dx, y = cy + dy; if (x < 0 || y < 0 || x >= size || y >= size) continue;
        var r = Math.max(Math.abs(dx), Math.abs(dy));
        set(x, y, r <= 1 || r === 3);
      }
    }
    finder(3, 3); finder(size - 4, 3); finder(3, size - 4);
    for (var i2 = 8; i2 < size - 8; i2++) { set(i2, 6, i2 % 2 === 0); set(6, i2, i2 % 2 === 0); }
    var al = t.align;
    for (var a = 0; a < al.length; a++) for (var b2 = 0; b2 < al.length; b2++) {
      var cx = al[a], cy = al[b2];
      if ((cx <= 8 && cy <= 8) || (cx >= size - 9 && cy <= 8) || (cx <= 8 && cy >= size - 9)) continue; // the three finders' corners (a centre on the timing pattern stays)
      for (var dy2 = -2; dy2 <= 2; dy2++) for (var dx2 = -2; dx2 <= 2; dx2++) { var r2 = Math.max(Math.abs(dx2), Math.abs(dy2)); set(cx + dx2, cy + dy2, r2 !== 1); }
    }
    // the format areas reserved (filled after the mask), the dark module, the version areas
    for (var f = 0; f < 9; f++) { if (f !== 6) { fixed[8][f] = true; fixed[f][8] = true; } }
    for (var f2 = 0; f2 < 8; f2++) { fixed[8][size - 1 - f2] = true; fixed[size - 1 - f2][8] = true; }
    set(8, size - 8, true);
    if (v >= 7) for (var q = 0; q < 18; q++) { var vx = Math.floor(q / 3), vy = q % 3; fixed[vy + size - 11][vx] = true; fixed[vx][vy + size - 11] = true; }
    // the data placed in the zigzag, two columns at a time, column six skipped
    var bitIndex = 0, total = out.length * 8, up = true;
    function dataBit(i) { return i < total ? (out[i >> 3] >> (7 - (i & 7))) & 1 : 0; }
    for (var col = size - 1; col > 0; col -= 2) {
      if (col === 6) col = 5;
      for (var row = 0; row < size; row++) {
        var yy = up ? size - 1 - row : row;
        for (var cc = 0; cc < 2; cc++) {
          var xx = col - cc;
          if (fixed[yy][xx]) continue;
          m[yy][xx] = dataBit(bitIndex) === 1; bitIndex++;
        }
      }
      up = !up;
    }
    // the eight masks, the best by the standard's penalties
    var MASKS = [
      function (x, y) { return (x + y) % 2 === 0; }, function (x, y) { return y % 2 === 0; }, function (x, y) { return x % 3 === 0; },
      function (x, y) { return (x + y) % 3 === 0; }, function (x, y) { return (Math.floor(y / 2) + Math.floor(x / 3)) % 2 === 0; },
      function (x, y) { return ((x * y) % 2) + ((x * y) % 3) === 0; }, function (x, y) { return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0; },
      function (x, y) { return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0; }
    ];
    function applyMask(k) {
      var mm = m.map(function (r) { return r.slice(); });
      for (var y = 0; y < size; y++) for (var x = 0; x < size; x++) if (!fixed[y][x] && MASKS[k](x, y)) mm[y][x] = !mm[y][x];
      var fi = bchFormat((0 << 3) | k); // level M is 00
      for (var i = 0; i < 15; i++) {
        var bit = ((fi >> i) & 1) === 1;
        // the first copy round the top-left finder, the second split between the other two
        if (i < 6) mm[i][8] = bit; else if (i < 8) mm[i + 1][8] = bit; else mm[8][14 - i] = bit;
        if (i < 8) mm[8][size - 1 - i] = bit; else mm[size - 15 + i][8] = bit;
      }
      mm[size - 8][8] = true;
      if (v >= 7) { var vi = bchVersion(v); for (var q = 0; q < 18; q++) { var vb = ((vi >> q) & 1) === 1, vx = Math.floor(q / 3), vy = q % 3; mm[vy + size - 11][vx] = vb; mm[vx][vy + size - 11] = vb; } }
      return mm;
    }
    function penalty(mm) {
      var score = 0, x, y;
      function runs(get) { for (var i = 0; i < size; i++) { var run = 1; for (var j = 1; j < size; j++) { if (get(i, j) === get(i, j - 1)) { run++; if (run === 5) score += 3; else if (run > 5) score += 1; } else run = 1; } } }
      runs(function (i, j) { return mm[i][j]; }); runs(function (i, j) { return mm[j][i]; });
      for (y = 0; y < size - 1; y++) for (x = 0; x < size - 1; x++) { var c = mm[y][x]; if (mm[y][x + 1] === c && mm[y + 1][x] === c && mm[y + 1][x + 1] === c) score += 3; }
      var P1 = [true, false, true, true, true, false, true, false, false, false, false], P2 = [false, false, false, false, true, false, true, true, true, false, true];
      function finderLike(get) {
        for (var i = 0; i < size; i++) for (var j = 0; j <= size - 11; j++) {
          var a = true, b = true;
          for (var k = 0; k < 11; k++) { var val = get(i, j + k); if (val !== P1[k]) a = false; if (val !== P2[k]) b = false; }
          if (a || b) score += 40;
        }
      }
      finderLike(function (i, j) { return mm[i][j]; }); finderLike(function (i, j) { return mm[j][i]; });
      var dark = 0; for (y = 0; y < size; y++) for (x = 0; x < size; x++) if (mm[y][x]) dark++;
      var pct = dark * 100 / (size * size), prev = Math.floor(pct / 5) * 5, next = prev + 5;
      score += Math.min(Math.abs(prev - 50) / 5, Math.abs(next - 50) / 5) * 10;
      return score;
    }
    var best = null, bestScore = Infinity;
    for (var k2 = 0; k2 < 8; k2++) { var cand = applyMask(k2), sc = penalty(cand); if (sc < bestScore) { bestScore = sc; best = cand; } }
    return { size: size, version: v, modules: best };
  }
  // the canvas: dark modules on white with a four-module quiet zone, drawn at the device's pixel ratio
  function draw(canvas, text, px) {
    var q = encode(text), quiet = 4, n = q.size + quiet * 2, scale = window.devicePixelRatio || 1;
    px = px || 200;
    var cell = Math.max(1, Math.floor(px * scale / n)), side = cell * n;
    canvas.width = side; canvas.height = side; canvas.style.width = (side / scale) + 'px'; canvas.style.height = (side / scale) + 'px';
    var ctx = canvas.getContext('2d');
    ctx.fillStyle = '#ffffff'; ctx.fillRect(0, 0, side, side);
    ctx.fillStyle = '#111111';
    for (var y = 0; y < q.size; y++) for (var x = 0; x < q.size; x++) if (q.modules[y][x]) ctx.fillRect((x + quiet) * cell, (y + quiet) * cell, cell, cell);
    return q;
  }
  return { encode: encode, draw: draw };
})();
if (typeof module !== 'undefined') module.exports = QR;
