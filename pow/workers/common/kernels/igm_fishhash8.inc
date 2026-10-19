// igm_fishhash8.inc: the FishHash search body with eight lanes per nonce: each lane owns
// four of the 32 mix words (16 bytes), reads its 16-byte slice of each 128-byte item (the
// eight lanes read one item coalesced), merges locally (the FNV and XOR merges and the
// 64-bit multiply-add act on word pairs inside a lane), and the three fetch indexes
// (words 0, 4, 8: lanes 0, 1, 2) and the eight collapsed words are exchanged through the
// prelude's IGM_BCAST(value, src_lane). Same bytes as igm_fishhash.inc (HW-01 proves
// it); only the mapping onto the SIMD group differs. The prelude must define IGM_BCAST
// and the wrapper must pass lane (0..7) and the lane's slice. Requires igm_fishhash.inc
// before it for the BLAKE3 helpers. GPU engineer B, 10 October 2026.

// One nonce by eight lanes. mix_out8 and final8 are filled in every lane (identical).
IGM_INLINE void igm_fishhash8_one(IGM_PRIV const uint *hdr8, uint nonce_lo, uint nonce_hi, IGM_GLOBAL const uint *dataset, uint items1024,
                              uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint seed[16];
    igm_b3_40_to_64(hdr8, nonce_lo, nonce_hi, seed);          // every lane computes the seed (cheap, identical)
    // this lane's four words of the 32-word mix {seed, seed}: words 4*lane .. 4*lane+3
    uint mx[4];
    for (uint j = 0; j < 4; j++) mx[j] = seed[(4u * lane + j) & 15u];
    for (uint r = 0; r < 32; r++) {
        uint w0 = IGM_BCAST(mx[0], 0u);   // mix word 0 lives in lane 0
        uint w4 = IGM_BCAST(mx[0], 1u);   // mix word 4 in lane 1
        uint w8 = IGM_BCAST(mx[0], 2u);   // mix word 8 in lane 2
        uint p0 = w0 % items1024, p1 = w4 % items1024, p2 = w8 % items1024;
        IGM_GLOBAL const uint *f0 = dataset + (ulong)p0 * 32u + 4u * lane;
        IGM_GLOBAL const uint *f1 = dataset + (ulong)p1 * 32u + 4u * lane;
        IGM_GLOBAL const uint *f2 = dataset + (ulong)p2 * 32u + 4u * lane;
        uint a0 = f0[0], a1 = f0[1], a2 = f0[2], a3 = f0[3];
        uint b0 = f1[0], b1 = f1[1], b2 = f1[2], b3 = f1[3];
        uint c0 = f2[0], c1 = f2[1], c2 = f2[2], c3 = f2[3];
        // pair (words 0,1) and pair (words 2,3) of this lane
        {
            ulong a = ((ulong)a1 << 32) | a0;
            ulong b = ((ulong)igm_fh_fnv1(mx[1], b1) << 32) | igm_fh_fnv1(mx[0], b0);
            ulong c = ((ulong)(mx[1] ^ c1) << 32) | (mx[0] ^ c0);
            ulong m = a * b + c; mx[0] = (uint)m; mx[1] = (uint)(m >> 32);
        }
        {
            ulong a = ((ulong)a3 << 32) | a2;
            ulong b = ((ulong)igm_fh_fnv1(mx[3], b3) << 32) | igm_fh_fnv1(mx[2], b2);
            ulong c = ((ulong)(mx[3] ^ c3) << 32) | (mx[2] ^ c2);
            ulong m = a * b + c; mx[2] = (uint)m; mx[3] = (uint)(m >> 32);
        }
    }
    // collapse: this lane's four words to one; gather the eight into every lane
    uint h1 = igm_fh_fnv1(mx[0], mx[1]);
    uint h2 = igm_fh_fnv1(h1, mx[2]);
    uint h3 = igm_fh_fnv1(h2, mx[3]);
    for (uint l = 0; l < 8; l++) mix_out8[l] = IGM_BCAST(h3, l);
    igm_b3_96_to_32(seed, mix_out8, final8);
}
