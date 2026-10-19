// igm_igneumhash_ctx.inc: T-B10a, N independent nonce contexts per eight-lane group (N = IGM_IH_CTX: 2 or 4).
// Every lane carries N nonces' lane state; every round issues the N item reads before any is consumed, so N dependent
// chains are in flight per thread at N times the registers. The hash is version 1's exactly (the same pid); this file
// repeats the setup, the round and the final of igm_igneumhash.inc as helpers over one context so the stock body stays
// untouched, and proves itself on the vectors like any kernel. No trace and no diagnostic forms here; split2 and load4
// are honoured. GPU engineer B, 10 October 2026.
#ifndef IGM_IH_CTX
#define IGM_IH_CTX 2
#endif

// the setup to the first address (sections 4.1 to 4.3): S and R out, the index returned
__attribute__((always_inline)) inline uint ihc_setup(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_GLOBAL uint *scratch_slice,
                      uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *S, IGM_PRIV uint *R) {
    uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_seed, 19u);
    pos = ih_put_be64(w, pos, k->c_hi, k->c_lo);
    for (uint i = 0; i < 4; i++) ih_put8(w, pos++, k->v >> (24u - 8u * i));
    pos = ih_put_bytes32(w, pos, k->p);
    pos = ih_put_bytes32(w, pos, hdr8);
    pos = ih_put_be64(w, pos, nonce_hi, nonce_lo);
    ih_b3_chunk(k->p, false, w, pos, 1u, S);
    for (uint j = 0; j < 48; j++) w[j] = 0u;
    pos = ih_put_tag(w, 0u, ih_tag_lanes, 20u);
    pos = ih_put_bytes32(w, pos, S);
    {
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16]; for (uint i = 0; i < 16; i++) m[i] = (4u * i < pos) ? w[i] : 0u;
        ih_b3_compress(cv, m, lane, pos, IH_CHUNK_START | IH_CHUNK_END | IH_ROOT, R);
    }
    for (uint j = 0; j < k->slice_words; j++) {
        uint a = R[j & 15u], b = R[(j + 5u) & 15u];
        a = (a ^ ((b << 13) | (b >> 19))) * IH_MUL;
        a = a + (b >> 7);
        R[j & 15u] = a;
        scratch_slice[j] = a ^ R[(j + 11u) & 15u];
    }
    uint r00 = IGM_BCAST(R[0], 0u), r48 = IGM_BCAST(R[8], 4u);
    return (r00 ^ r48) & k->n_items_mask;
}

// section 4.4, the load alone: lane l takes words 4l..4l+3 of the item
__attribute__((always_inline)) inline void ihc_load(IGM_PRIV const ih_consts *k, IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, uint idx, uint lane, IGM_PRIV uint *D) {
#ifdef IGM_IH_SPLIT2
    IGM_GLOBAL const uint *it = ((idx > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idx - (k->n_items_mask >> 1u) - 1u) * 32u : dataset + (ulong)idx * 32u) + 4u * lane;
#else
    IGM_GLOBAL const uint *it = dataset + (ulong)idx * 32u + 4u * lane;
#endif
#ifdef IGM_IH_LOAD4
    uint4 dq = IGM_LOAD4(it);
    D[0] = dq.x; D[1] = dq.y; D[2] = dq.z; D[3] = dq.w;
#else
    D[0] = it[0]; D[1] = it[1]; D[2] = it[2]; D[3] = it[3];
#endif
}

// sections 4.4 (the merge), 4.5 and 4.6 over one context: the next index returned
__attribute__((always_inline)) inline uint ihc_mix(IGM_PRIV const ih_consts *k, IGM_GLOBAL uint *scratch_slice, uint lane, IGM_XCH_PARAMS, uint r, IGM_PRIV uint *R, IGM_PRIV const uint *D) {
    for (uint kk = 0; kk < 4; kk++) {
        uint a = R[4u * kk], b = R[4u * kk + 1u], c = R[4u * kk + 2u], d = R[4u * kk + 3u], wv = D[kk];
        a = a + wv;
        b = b ^ a;
        c = c + b * IH_MUL;
        { uint n = a & 31u; d = d ^ c; d = n ? ((d >> n) | (d << (32u - n))) : d; }
        a = a ^ (d >> (wv & 31u));
        { uint s = b + d; b = (s << 7) | (s >> 25); }
        c = c ^ ((a << 17) | (a >> 15));
        d = d + b * c;
        R[4u * kk] = a; R[4u * kk + 1u] = b; R[4u * kk + 2u] = c; R[4u * kk + 3u] = d;
    }
    { uint T[16]; for (uint q = 0; q < 16; q++) T[q] = R[q]; for (uint q = 0; q < 16; q++) R[q] = T[(5u * q + 1u) & 15u]; }
    if (k->slice_words != 0u) {
        uint line = R[0] & k->slice_lines_mask;
        for (uint j = 0; j < 4; j++) {
            uint s = scratch_slice[4u * line + j];
            uint x = R[8u + j] ^ s;
            R[8u + j] = ((x << 5) | (x >> 27)) + R[j];
            scratch_slice[4u * line + j] = (s + R[12u + j]) ^ R[8u + j];
        }
    }
    {
        uint p = lane ^ (1u << (r % 3u));
        uint q = (lane + 1u + (R[1] % 7u)) & 7u;
        uint xp0 = IGM_BCAST(R[0], p), xp2 = IGM_BCAST(R[2], p);
        uint xq1 = IGM_BCAST(R[1], q), xq3 = IGM_BCAST(R[3], q);
        uint n0 = R[0] + xp0, n1 = R[1] ^ xq1, n2 = R[2] + ((xp2 << 11) | (xp2 >> 21)), n3 = R[3] ^ ((xq3 << 23) | (xq3 >> 9));
        R[0] = n0; R[1] = n1; R[2] = n2; R[3] = n3;
    }
    uint g = (r + 1u) & 7u;
    return IGM_BCAST(R[0], g) & k->n_items_mask;
}

// section 4.7 and the state bytes into the nonce's area (every lane), before the group's sync
IGM_INLINE void ihc_state(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *R, IGM_GLOBAL const uint *scratch_slice, IGM_GLOBAL uint *st_area, uint lane) {
    const bool pad = k->slice_words != 0u;
    uint f[4] = {0u, 0u, 0u, 0u};
    if (pad) {
        for (uint i = 0; i < (k->slice_lines_mask + 1u); i++)
            for (uint j = 0; j < 4; j++) { uint x = f[j] ^ scratch_slice[4u * i + j]; f[j] = ((x << 9) | (x >> 23)) + scratch_slice[4u * i + ((j + 1u) & 3u)]; }
    }
    for (uint q = 0; q < 16; q++) st_area[16u * lane + q] = R[q];
    if (pad) for (uint j = 0; j < 4; j++) st_area[128u + 4u * lane + j] = f[j];
}

// M and F for one context (lane 0 only, after the sync)
IGM_INLINE void ihc_mf(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *S, IGM_GLOBAL const uint *st_area, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint st_words = (k->slice_words != 0u) ? 160u : 128u;
    uint mw[176]; for (uint j = 0; j < 176; j++) mw[j] = 0u;
    uint mp = ih_put_tag(mw, 0u, ih_tag_mix, 18u);
    mp = ih_put_bytes32(mw, mp, S);
    for (uint q = 0; q < st_words; q++) { uint v = st_area[q]; for (uint b = 0; b < 4; b++) ih_put8(mw, mp++, v >> (8u * b)); }
    uint M[16]; ih_b3_chunk(k->p, false, mw, mp, 1u, M);
    for (uint q = 0; q < 8; q++) mix_out8[q] = M[q];
    uint fw[48]; for (uint j = 0; j < 48; j++) fw[j] = 0u;
    uint fp = ih_put_tag(fw, 0u, ih_tag_final, 20u);
    fp = ih_put_be64(fw, fp, k->c_hi, k->c_lo);
    for (uint i = 0; i < 4; i++) ih_put8(fw, fp++, k->v >> (24u - 8u * i));
    fp = ih_put_bytes32(fw, fp, k->p);
    fp = ih_put_bytes32(fw, fp, S);
    fp = ih_put_bytes32(fw, fp, M);
    uint F[16]; ih_b3_chunk(k->p, false, fw, fp, 1u, F);
    for (uint q = 0; q < 8; q++) final8[q] = F[q];
}

// the N contexts of one lane group: nonce_lo..+N-1 (with carry), slots slot0..+N-1; mix_out and fin hold N x 8 words
IGM_INLINE void igm_igneumhash_lane_ctx(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                                    IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, IGM_GLOBAL uint *scratch, IGM_GLOBAL uint *st_areas, uint slot0,
                                    uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *mix_out, IGM_PRIV uint *fin) {
    uint S[IGM_IH_CTX][16], R[IGM_IH_CTX][16], idx[IGM_IH_CTX];
    for (uint c = 0; c < IGM_IH_CTX; c++) {
        uint nlo = nonce_lo + c, nhi = nonce_hi + (nlo < nonce_lo ? 1u : 0u);
        IGM_GLOBAL uint *slice = scratch + ((ulong)(slot0 + c) * 8u + lane) * (ulong)k->slice_words;
        idx[c] = ihc_setup(k, hdr8, nhi, nlo, slice, lane, IGM_XCH_ARGS, S[c], R[c]);
    }
    for (uint r = 0; r < k->rounds; r++) {
        uint D[IGM_IH_CTX][4];
        for (uint c = 0; c < IGM_IH_CTX; c++) ihc_load(k, dataset IGM_IH_DATASET2_ARG, idx[c], lane, D[c]);
        for (uint c = 0; c < IGM_IH_CTX; c++) {
            IGM_GLOBAL uint *slice = scratch + ((ulong)(slot0 + c) * 8u + lane) * (ulong)k->slice_words;
            idx[c] = ihc_mix(k, slice, lane, IGM_XCH_ARGS, r, R[c], D[c]);
        }
    }
    for (uint c = 0; c < IGM_IH_CTX; c++) {
        IGM_GLOBAL uint *slice = scratch + ((ulong)(slot0 + c) * 8u + lane) * (ulong)k->slice_words;
        ihc_state(k, R[c], slice, st_areas + (ulong)(slot0 + c) * 160u, lane);
    }
    IGM_ST_SYNC();
    if (lane == 0u) for (uint c = 0; c < IGM_IH_CTX; c++) ihc_mf(k, S[c], st_areas + (ulong)(slot0 + c) * 160u, mix_out + 8u * c, fin + 8u * c);
}
