// igm_autolykos.inc: Autolykos v2 bodies, dialect-neutral (the host prepends the
// dialect prelude and appends its wrappers). Reproduces pow/ref/src/autolykos.cpp
// (ergo v6.0.7 AutolykosPowScheme): the element table of a height, table[j] =
// BLAKE2b-256(j BE32 || height BE32 || M) with M the 1024 big-endian longs 0..1023,
// and a nonce's hit = BLAKE2b-256 of the 32-byte big-endian sum of the k = 32 elements
// (bytes 1..31 of each) picked by the seed. BLAKE2b from RFC 7693, 256-bit output,
// no key, written for the exact message lengths this needs. GPU engineer B, 9 Oct 2026.

IGM_CONST ulong igm_b2b_iv[8] = {0x6a09e667f3bcc908ul, 0xbb67ae8584caa73bul, 0x3c6ef372fe94f82bul, 0xa54ff53a5f1d36f1ul,
                                 0x510e527fade682d1ul, 0x9b05688c2b3e6c1ful, 0x1f83d9abfb41bd6bul, 0x5be0cd19137e2179ul};
IGM_CONST uchar igm_b2b_sigma[12][16] = {
    {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, {14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
    {11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4}, {7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
    {9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13}, {2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
    {12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11}, {13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
    {6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5}, {10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
    {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, {14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3}};

IGM_INLINE ulong igm_rotr64(ulong x, uint c) { return (x >> c) | (x << (64u - c)); }

IGM_INLINE void igm_b2b_g(IGM_PRIV ulong *v, uint a, uint b, uint c, uint d, ulong x, ulong y) {
    v[a] = v[a] + v[b] + x; v[d] = igm_rotr64(v[d] ^ v[a], 32u);
    v[c] = v[c] + v[d];     v[b] = igm_rotr64(v[b] ^ v[c], 24u);
    v[a] = v[a] + v[b] + y; v[d] = igm_rotr64(v[d] ^ v[a], 16u);
    v[c] = v[c] + v[d];     v[b] = igm_rotr64(v[b] ^ v[c], 63u);
}

// One compression of a 128-byte block given as 16 little-endian ulongs; t is the byte counter.
IGM_INLINE void igm_b2b_compress(IGM_PRIV ulong *h, IGM_PRIV const ulong *m, ulong t, bool last) {
    ulong v[16];
    for (uint i = 0; i < 8; i++) { v[i] = h[i]; v[i + 8] = igm_b2b_iv[i]; }
    v[12] ^= t;
    if (last) v[14] = ~v[14];
    for (uint r = 0; r < 12; r++) {
        igm_b2b_g(v, 0, 4, 8, 12, m[igm_b2b_sigma[r][0]], m[igm_b2b_sigma[r][1]]);
        igm_b2b_g(v, 1, 5, 9, 13, m[igm_b2b_sigma[r][2]], m[igm_b2b_sigma[r][3]]);
        igm_b2b_g(v, 2, 6, 10, 14, m[igm_b2b_sigma[r][4]], m[igm_b2b_sigma[r][5]]);
        igm_b2b_g(v, 3, 7, 11, 15, m[igm_b2b_sigma[r][6]], m[igm_b2b_sigma[r][7]]);
        igm_b2b_g(v, 0, 5, 10, 15, m[igm_b2b_sigma[r][8]], m[igm_b2b_sigma[r][9]]);
        igm_b2b_g(v, 1, 6, 11, 12, m[igm_b2b_sigma[r][10]], m[igm_b2b_sigma[r][11]]);
        igm_b2b_g(v, 2, 7, 8, 13, m[igm_b2b_sigma[r][12]], m[igm_b2b_sigma[r][13]]);
        igm_b2b_g(v, 3, 4, 9, 14, m[igm_b2b_sigma[r][14]], m[igm_b2b_sigma[r][15]]);
    }
    for (uint i = 0; i < 8; i++) h[i] ^= v[i] ^ v[i + 8];
}

IGM_INLINE void igm_b2b_init256(IGM_PRIV ulong *h) {
    for (uint i = 0; i < 8; i++) h[i] = igm_b2b_iv[i];
    h[0] ^= 0x01010000ul ^ 32ul;   // digest length 32, no key, fanout 1, depth 1
}

// Byte helpers on a 128-byte block held as 16 little-endian ulongs.
IGM_INLINE void igm_blk_clear(IGM_PRIV ulong *m) { for (uint i = 0; i < 16; i++) m[i] = 0ul; }
IGM_INLINE void igm_blk_set(IGM_PRIV ulong *m, uint pos, uchar b) { m[pos >> 3] |= (ulong)b << (8u * (pos & 7u)); }
IGM_INLINE uchar igm_h_byte(IGM_PRIV const ulong *h, uint pos) { return (uchar)(h[pos >> 3] >> (8u * (pos & 7u))); }

IGM_INLINE ulong igm_al_bswap64(ulong x) {
    x = ((x & 0x00FF00FF00FF00FFul) << 8) | ((x >> 8) & 0x00FF00FF00FF00FFul);
    x = ((x & 0x0000FFFF0000FFFFul) << 16) | ((x >> 16) & 0x0000FFFF0000FFFFul);
    return (x << 32) | (x >> 32);
}
IGM_INLINE uint igm_al_bswap32(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }

// table[j] = BLAKE2b-256(j BE32 || height BE32 || M): 8200 bytes = 64 full blocks + 8 bytes.
// The message stream has a closed form: M is the 1024 big-endian longs 0..1023, so the
// little-endian 64-bit word w of block blk (bytes 8 onward) is the byte-swapped long
// index 16*blk + w - 1, and block 0 word 0 is the index and height; the last block is
// the byte-swapped 1023 and zeros. No byte assembly.
IGM_INLINE void igm_al_element(uint index, uint height, IGM_PRIV ulong *h) {
    igm_b2b_init256(h);
    ulong m[16];
    for (uint blk = 0; blk < 64; blk++) {
        for (uint w = 0; w < 16; w++) m[w] = igm_al_bswap64((ulong)(16u * blk + w) - 1ul);
        if (blk == 0) m[0] = (ulong)igm_al_bswap32(index) | ((ulong)igm_al_bswap32(height) << 32);
        igm_b2b_compress(h, m, (ulong)(blk + 1) * 128ul, false);
    }
    for (uint w = 0; w < 16; w++) m[w] = 0ul;
    m[0] = igm_al_bswap64(1023ul);
    igm_b2b_compress(h, m, 8200ul, true);
}

// BLAKE2b-256 of header(32) || nonce BE(8): 40 bytes, one final block.
IGM_INLINE void igm_al_h1(IGM_PRIV const uchar *hdr, uint nonce_lo, uint nonce_hi, IGM_PRIV ulong *h) {
    igm_b2b_init256(h);
    ulong m[16]; igm_blk_clear(m);
    for (uint i = 0; i < 32; i++) igm_blk_set(m, i, hdr[i]);
    for (uint i = 0; i < 4; i++) { igm_blk_set(m, 32 + i, (uchar)(nonce_hi >> (8u * (3u - i)))); igm_blk_set(m, 36 + i, (uchar)(nonce_lo >> (8u * (3u - i)))); }
    igm_b2b_compress(h, m, 40ul, true);
}

// BLAKE2b-256 of f(31) || header(32) || nonce BE(8): 71 bytes, one final block.
IGM_INLINE void igm_al_hs(IGM_PRIV const uchar *f31, IGM_PRIV const uchar *hdr, uint nonce_lo, uint nonce_hi, IGM_PRIV ulong *h) {
    igm_b2b_init256(h);
    ulong m[16]; igm_blk_clear(m);
    for (uint i = 0; i < 31; i++) igm_blk_set(m, i, f31[i]);
    for (uint i = 0; i < 32; i++) igm_blk_set(m, 31 + i, hdr[i]);
    for (uint i = 0; i < 4; i++) { igm_blk_set(m, 63 + i, (uchar)(nonce_hi >> (8u * (3u - i)))); igm_blk_set(m, 67 + i, (uchar)(nonce_lo >> (8u * (3u - i)))); }
    igm_b2b_compress(h, m, 71ul, true);
}

// BLAKE2b-256 of 32 bytes.
IGM_INLINE void igm_al_h32(IGM_PRIV const uchar *b32, IGM_PRIV ulong *h) {
    igm_b2b_init256(h);
    ulong m[16]; igm_blk_clear(m);
    for (uint i = 0; i < 32; i++) igm_blk_set(m, i, b32[i]);
    igm_b2b_compress(h, m, 32ul, true);
}

// One nonce: the table holds 32-byte elements (the full digest; bytes 1..31 are used).
// sum_out and hit_out are 32 bytes each, big-endian as the reference writes them.
IGM_INLINE void igm_autolykos_one(IGM_PRIV const uchar *hdr, uint nonce_lo, uint nonce_hi, IGM_GLOBAL const uchar *table, uint N,
                              IGM_PRIV uchar *sum_out, IGM_PRIV uchar *hit_out) {
    ulong h1[8];
    igm_al_h1(hdr, nonce_lo, nonce_hi, h1);
    ulong prei8 = 0ul;
    for (uint i = 24; i < 32; i++) prei8 = (prei8 << 8) | (ulong)igm_h_byte(h1, i);
    uint i0 = (uint)(prei8 % (ulong)N);
    uchar f[31];
    for (uint i = 0; i < 31; i++) f[i] = table[(ulong)i0 * 32u + 1u + i];
    ulong hs[8];
    igm_al_hs(f, hdr, nonce_lo, nonce_hi, hs);
    uchar ext[35];
    for (uint i = 0; i < 32; i++) ext[i] = igm_h_byte(hs, i);
    for (uint i = 0; i < 3; i++) ext[32 + i] = ext[i];
    uchar sum[32];
    for (uint i = 0; i < 32; i++) sum[i] = 0;
    for (uint i = 0; i < 32; i++) {
        uint w = ((uint)ext[i] << 24) | ((uint)ext[i + 1] << 16) | ((uint)ext[i + 2] << 8) | (uint)ext[i + 3];
        uint idx = w % N;
        IGM_GLOBAL const uchar *e = table + (ulong)idx * 32u + 1u;
        uint carry = 0;
        for (int b = 30; b >= 0; b--) { uint s = (uint)sum[b + 1] + (uint)e[b] + carry; sum[b + 1] = (uchar)s; carry = s >> 8; }
        sum[0] = (uchar)(sum[0] + carry);
    }
    for (uint i = 0; i < 32; i++) sum_out[i] = sum[i];
    ulong hh[8];
    igm_al_h32(sum, hh);
    for (uint i = 0; i < 32; i++) hit_out[i] = igm_h_byte(hh, i);
}
