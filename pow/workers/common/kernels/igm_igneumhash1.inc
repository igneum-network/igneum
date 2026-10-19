// igm_igneumhash1.inc: IgneumHash-R0A versions 0 and 1 with the eight lanes of one nonce in ONE
// thread (tuning point T-B3, TUNING-R0A.md): no lane exchange through shuffles or local memory,
// no barrier; the item read is one 128-byte line per round; the exchange with snapshot semantics
// reads the pre-exchange registers of the partner lanes directly. The same WORK bytes as
// igm_igneumhash.inc (the vectors decide). Requires igm_igneumhash.inc before it (the BLAKE3
// chunk, the byte builders, the tags, ih_consts). GPU engineer B, 10 October 2026.

IGM_INLINE void igm_igneumhash_one(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                               IGM_GLOBAL const uint *dataset, IGM_GLOBAL uint *scratch, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    // S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N))
    uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_seed, 19u);
    pos = ih_put_be64(w, pos, k->c_hi, k->c_lo);
    for (uint i = 0; i < 4; i++) ih_put8(w, pos++, k->v >> (24u - 8u * i));
    pos = ih_put_bytes32(w, pos, k->p);
    pos = ih_put_bytes32(w, pos, hdr8);
    pos = ih_put_be64(w, pos, nonce_hi, nonce_lo);
    uint S[16]; ih_b3_chunk(k->p, false, w, pos, 1u, S);
    // 4.1 L = BLAKE3xof(tag(T_LANES) || S, 512): block l for lane l
    for (uint j = 0; j < 48; j++) w[j] = 0u;
    pos = ih_put_tag(w, 0u, ih_tag_lanes, 20u);
    pos = ih_put_bytes32(w, pos, S);
    uint R[8][16];
    {
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16]; for (uint i = 0; i < 16; i++) m[i] = (4u * i < pos) ? w[i] : 0u;
        for (uint l = 0; l < 8; l++) ih_b3_compress(cv, m, l, pos, IH_CHUNK_START | IH_CHUNK_END | IH_ROOT, R[l]);
    }
    const bool pad = k->slice_words != 0u;
    const uint sw = k->slice_words;
    // 4.2 scratch fill, each lane on its slice
    if (pad) for (uint l = 0; l < 8; l++) {
        IGM_GLOBAL uint *sl = scratch + l * sw;
        for (uint j = 0; j < sw; j++) {
            uint a = R[l][j & 15u], b = R[l][(j + 5u) & 15u];
            a = (a ^ ((b << 13) | (b >> 19))) * IH_MUL;
            a = a + (b >> 7);
            R[l][j & 15u] = a;
            sl[j] = a ^ R[l][(j + 11u) & 15u];
        }
    }
    // 4.3 first address
    uint idx = (R[0][0] ^ R[4][8]) & k->n_items_mask;
    for (uint r = 0; r < k->rounds; r++) {
        IGM_GLOBAL const uint *it = dataset + (ulong)idx * 32u;
        for (uint l = 0; l < 8; l++) {
            // 4.4 load and the sequential cell on this lane's four words
            uint D0 = it[4u * l], D1 = it[4u * l + 1u], D2 = it[4u * l + 2u], D3 = it[4u * l + 3u];
            uint Dk[4] = {D0, D1, D2, D3};
            for (uint kk = 0; kk < 4; kk++) {
                uint a = R[l][4u * kk], b = R[l][4u * kk + 1u], c = R[l][4u * kk + 2u], d = R[l][4u * kk + 3u], wv = Dk[kk];
                a = a + wv;
                b = b ^ a;
                c = c + b * IH_MUL;
                { uint n = a & 31u; d = d ^ c; d = n ? ((d >> n) | (d << (32u - n))) : d; }
                a = a ^ (d >> (wv & 31u));
                { uint s = b + d; b = (s << 7) | (s >> 25); }
                c = c ^ ((a << 17) | (a >> 15));
                d = d + b * c;
                R[l][4u * kk] = a; R[l][4u * kk + 1u] = b; R[l][4u * kk + 2u] = c; R[l][4u * kk + 3u] = d;
            }
            { uint T[16]; for (uint q = 0; q < 16; q++) T[q] = R[l][q]; for (uint q = 0; q < 16; q++) R[l][q] = T[(5u * q + 1u) & 15u]; }
            // 4.5 scratch access on the own slice
            if (pad) {
                IGM_GLOBAL uint *sl = scratch + l * sw;
                uint line = R[l][0] & k->slice_lines_mask;
                for (uint j = 0; j < 4; j++) {
                    uint s = sl[4u * line + j];
                    uint x = R[l][8u + j] ^ s;
                    R[l][8u + j] = ((x << 5) | (x >> 27)) + R[l][j];
                    sl[4u * line + j] = (s + R[l][12u + j]) ^ R[l][8u + j];
                }
            }
        }
        // exchange with snapshot semantics: every lane reads the pre-exchange values
        {
            uint n0[8], n1[8], n2[8], n3[8];
            for (uint l = 0; l < 8; l++) {
                uint p = l ^ (1u << (r % 3u));
                uint q = (l + 1u + (R[l][1] % 7u)) & 7u;
                uint xp0 = R[p][0], xp2 = R[p][2], xq1 = R[q][1], xq3 = R[q][3];
                n0[l] = R[l][0] + xp0; n1[l] = R[l][1] ^ xq1; n2[l] = R[l][2] + ((xp2 << 11) | (xp2 >> 21)); n3[l] = R[l][3] ^ ((xq3 << 23) | (xq3 >> 9));
            }
            for (uint l = 0; l < 8; l++) { R[l][0] = n0[l]; R[l][1] = n1[l]; R[l][2] = n2[l]; R[l][3] = n3[l]; }
        }
        // 4.6 next address from the rotating leader
        idx = R[(r + 1u) & 7u][0] & k->n_items_mask;
    }
    // 4.7 fold per lane, then the state bytes and M
    uint st_words = pad ? 160u : 128u;
    uint mw[176]; for (uint j = 0; j < 176; j++) mw[j] = 0u;
    uint mp = ih_put_tag(mw, 0u, ih_tag_mix, 18u);
    mp = ih_put_bytes32(mw, mp, S);
    for (uint l = 0; l < 8; l++) for (uint q = 0; q < 16; q++) { uint v = R[l][q]; for (uint b = 0; b < 4; b++) ih_put8(mw, mp++, v >> (8u * b)); }
    if (pad) for (uint l = 0; l < 8; l++) {
        IGM_GLOBAL const uint *sl = scratch + l * sw;
        uint f[4] = {0u, 0u, 0u, 0u};
        for (uint i = 0; i < (k->slice_lines_mask + 1u); i++)
            for (uint j = 0; j < 4; j++) { uint x = f[j] ^ sl[4u * i + j]; f[j] = ((x << 9) | (x >> 23)) + sl[4u * i + ((j + 1u) & 3u)]; }
        for (uint j = 0; j < 4; j++) for (uint b = 0; b < 4; b++) ih_put8(mw, mp++, f[j] >> (8u * b));
    }
    (void)st_words;
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
