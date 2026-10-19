// igm_fishhash.inc: the FishHash search body, dialect-neutral (the host prepends a
// Metal or OpenCL prelude defining IGM_GLOBAL, IGM_PRIV, IGM_CONST, and appends the
// dialect's kernel wrapper). Reproduces cpp/FishHash.cpp of iron-fish/fish-hash at
// 823531c (measure-only, D-GA-04): seed = BLAKE3-64(header ++ nonce LE), mix = {seed,
// seed}, 32 rounds of three 1024-bit fetches with the FNV and XOR merges and the 64-bit
// multiply-add, the FNV collapse to 8 words, final = BLAKE3-32(seed ++ mix).
// BLAKE3 is written from the specification for the two shapes this needs (40 bytes,
// one block; 96 bytes, two blocks of one chunk), unkeyed. GPU engineer B, 9 Oct 2026.

IGM_CONST uint igm_b3_iv[8] = {0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u};
IGM_CONST uint igm_b3_perm[16] = {2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8};
#define IGM_B3_CHUNK_START 1u
#define IGM_B3_CHUNK_END 2u
#define IGM_B3_ROOT 8u

IGM_INLINE uint igm_b3_rotr(uint x, uint c) { return (x >> c) | (x << (32u - c)); }

IGM_INLINE void igm_b3_g(IGM_PRIV uint *v, uint a, uint b, uint c, uint d, uint x, uint y) {
    v[a] = v[a] + v[b] + x; v[d] = igm_b3_rotr(v[d] ^ v[a], 16u);
    v[c] = v[c] + v[d];     v[b] = igm_b3_rotr(v[b] ^ v[c], 12u);
    v[a] = v[a] + v[b] + y; v[d] = igm_b3_rotr(v[d] ^ v[a], 8u);
    v[c] = v[c] + v[d];     v[b] = igm_b3_rotr(v[b] ^ v[c], 7u);
}

// One compression (counter 0: everything here is inside chunk 0); out16 is the full
// output state: words 0..7 the chaining value, 8..15 the rest of the root output.
IGM_INLINE void igm_b3_compress(IGM_PRIV const uint *cv, IGM_PRIV const uint *m_in, uint block_len, uint flags, IGM_PRIV uint *out16) {
    uint v[16], m[16];
    for (uint i = 0; i < 16; i++) m[i] = m_in[i];
    for (uint i = 0; i < 8; i++) v[i] = cv[i];
    v[8] = igm_b3_iv[0]; v[9] = igm_b3_iv[1]; v[10] = igm_b3_iv[2]; v[11] = igm_b3_iv[3];
    v[12] = 0u; v[13] = 0u; v[14] = block_len; v[15] = flags;
    for (uint r = 0; r < 7; r++) {
        igm_b3_g(v, 0, 4, 8, 12, m[0], m[1]);   igm_b3_g(v, 1, 5, 9, 13, m[2], m[3]);
        igm_b3_g(v, 2, 6, 10, 14, m[4], m[5]);  igm_b3_g(v, 3, 7, 11, 15, m[6], m[7]);
        igm_b3_g(v, 0, 5, 10, 15, m[8], m[9]);  igm_b3_g(v, 1, 6, 11, 12, m[10], m[11]);
        igm_b3_g(v, 2, 7, 8, 13, m[12], m[13]); igm_b3_g(v, 3, 4, 9, 14, m[14], m[15]);
        if (r < 6) { uint p[16]; for (uint i = 0; i < 16; i++) p[i] = m[igm_b3_perm[i]]; for (uint i = 0; i < 16; i++) m[i] = p[i]; }
    }
    for (uint i = 0; i < 8; i++) { out16[i] = v[i] ^ v[i + 8]; out16[i + 8] = v[i + 8] ^ cv[i]; }
}

// BLAKE3 of 40 bytes (header words 0..7 little-endian, nonce low, nonce high) to 64 bytes.
IGM_INLINE void igm_b3_40_to_64(IGM_PRIV const uint *hdr8, uint nonce_lo, uint nonce_hi, IGM_PRIV uint *out16) {
    uint m[16];
    for (uint i = 0; i < 8; i++) m[i] = hdr8[i];
    m[8] = nonce_lo; m[9] = nonce_hi;
    for (uint i = 10; i < 16; i++) m[i] = 0u;
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    igm_b3_compress(cv, m, 40u, IGM_B3_CHUNK_START | IGM_B3_CHUNK_END | IGM_B3_ROOT, out16);
}

// BLAKE3 of 96 bytes (seed 16 words, mix 8 words) to 32 bytes: block 0 = the 64-byte seed
// (CHUNK_START), block 1 = the 32-byte mix (CHUNK_END | ROOT), both at counter 0.
IGM_INLINE void igm_b3_96_to_32(IGM_PRIV const uint *seed16, IGM_PRIV const uint *mix8, IGM_PRIV uint *out8) {
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    uint o[16];
    igm_b3_compress(cv, seed16, 64u, IGM_B3_CHUNK_START, o);
    for (uint i = 0; i < 8; i++) cv[i] = o[i];
    uint m[16];
    for (uint i = 0; i < 8; i++) m[i] = mix8[i];
    for (uint i = 8; i < 16; i++) m[i] = 0u;
    igm_b3_compress(cv, m, 32u, IGM_B3_CHUNK_END | IGM_B3_ROOT, o);
    for (uint i = 0; i < 8; i++) out8[i] = o[i];
}

IGM_INLINE uint igm_fh_fnv1(uint u, uint v) { return (u * 0x01000193u) ^ v; }

// One hash: mix_out 8 words, final 8 words (both little-endian word order of the bytes).
IGM_INLINE void igm_fishhash_one(IGM_PRIV const uint *hdr8, uint nonce_lo, uint nonce_hi, IGM_GLOBAL const uint *dataset, uint items1024,
                             IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint seed[16];
    igm_b3_40_to_64(hdr8, nonce_lo, nonce_hi, seed);
    uint mix[32];
    for (uint i = 0; i < 16; i++) { mix[i] = seed[i]; mix[16 + i] = seed[i]; }
    for (uint r = 0; r < 32; r++) {
        uint p0 = mix[0] % items1024, p1 = mix[4] % items1024, p2 = mix[8] % items1024;
        IGM_GLOBAL const uint *f0 = dataset + (ulong)p0 * 32u;
        IGM_GLOBAL const uint *f1 = dataset + (ulong)p1 * 32u;
        IGM_GLOBAL const uint *f2 = dataset + (ulong)p2 * 32u;
        for (uint j = 0; j < 16; j++) {
            uint a_lo = f0[2 * j], a_hi = f0[2 * j + 1];
            uint b_lo = igm_fh_fnv1(mix[2 * j], f1[2 * j]), b_hi = igm_fh_fnv1(mix[2 * j + 1], f1[2 * j + 1]);
            uint c_lo = mix[2 * j] ^ f2[2 * j], c_hi = mix[2 * j + 1] ^ f2[2 * j + 1];
            ulong a = ((ulong)a_hi << 32) | a_lo, b = ((ulong)b_hi << 32) | b_lo, c = ((ulong)c_hi << 32) | c_lo;
            ulong m = a * b + c;
            mix[2 * j] = (uint)m; mix[2 * j + 1] = (uint)(m >> 32);
        }
    }
    for (uint i = 0; i < 32; i += 4) {
        uint h1 = igm_fh_fnv1(mix[i], mix[i + 1]);
        uint h2 = igm_fh_fnv1(h1, mix[i + 2]);
        uint h3 = igm_fh_fnv1(h2, mix[i + 3]);
        mix_out8[i / 4] = h3;
    }
    igm_b3_96_to_32(seed, mix_out8, final8);
}
