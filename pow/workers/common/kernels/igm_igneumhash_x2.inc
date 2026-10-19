// igm_igneumhash_x2.inc: the two-chain form of the version 4 wave kernel at W512 (the auditor's item 7, built 11 October
// 2026 00:1x UK by the Riptide performance seat): every eight-lane group carries two independent nonces, A and B, each with
// its own state R[16], address and chain; every round loads both items before either chain computes, so one chain's
// arithmetic runs while the other's request is in flight, which is the idle the stand-in rows measured (about 2.5 cycles
// per instruction with every wave waiting on memory in the same phase). The relation per nonce is unchanged: the lean
// streamed finaliser, the branchless tree and absorb of form1-wave-sel, DPP exchanges under IGM_IH_DPP. Scratch 0 and
// version 4 only (the host refuses the rest). No trace path: the vectors prove exactness; the identity tool's trace class
// does not run on this form.
#ifndef IH_NODE
#define IH_NODE(x, y) ((((x) ^ (((y) << 13) | ((y) >> 19))) * IH_MUL) + ((y) >> 7))
#endif

#define X2_ROUND(R, q, idx, h4)                                                                                   \
    {                                                                                                                \
        uint zs[4];                                                                                                  \
        __attribute__((opencl_unroll_hint))                                                                          \
        for (uint t = 0; t < 4u; t++) {                                                                              \
            const uint j0 = 32u * t + 4u * lane;                                                                     \
            const uint z0 = q[t].x ^ ((j0 + 1u) * IH_MUL), z1 = q[t].y ^ ((j0 + 2u) * IH_MUL), z2 = q[t].z ^ ((j0 + 3u) * IH_MUL), z3 = q[t].w ^ ((j0 + 4u) * IH_MUL); \
            uint b = IH_NODE(IH_NODE(z0, z1), IH_NODE(z2, z3));                                                      \
            { uint v = IGM_XCHX(b, 1u); uint lo = (lane & 1u) ? v : b; uint hi = (lane & 1u) ? b : v; b = IH_NODE(lo, hi); } \
            { uint v = IGM_XCHX(b, 2u); uint lo = (lane & 2u) ? v : b; uint hi = (lane & 2u) ? b : v; b = IH_NODE(lo, hi); } \
            { uint v = IGM_XCHX(b, 4u); uint lo = (lane & 4u) ? v : b; uint hi = (lane & 4u) ? b : v; zs[t] = IH_NODE(lo, hi); } \
        }                                                                                                            \
        zs[0] = IH_NODE(zs[0], zs[1]); zs[1] = IH_NODE(zs[2], zs[3]); h4 = IH_NODE(zs[0], zs[1]);                      \
        {   /* the absorb, branchless (form1-wave-sel) */                                                            \
            const uint rot = lane & 3u, hf = lane >> 2u, mh = lane >> 1u;                                            \
            __attribute__((opencl_unroll_hint))                                                                      \
            for (uint t = 0; t < 4u; t++) {                                                                          \
                uint o0 = q[t].x, o1 = q[t].w, o2 = q[t].z, o3 = q[t].y;                                             \
                if (mh & 1u) { uint x = o3; o3 = o2; o2 = o1; o1 = o0; o0 = x; }                                     \
                if (mh & 2u) { uint x = o2, y = o3; o2 = o0; o3 = o1; o0 = x; o1 = y; }                              \
                uint a0 = IGM_BCAST(o0, hf + 2u * (rot & 3u));                                                       \
                uint a1 = IGM_BCAST(o1, hf + 2u * ((1u + rot) & 3u));                                                \
                uint a2 = IGM_BCAST(o2, hf + 2u * ((2u + rot) & 3u));                                                \
                uint a3 = IGM_BCAST(o3, hf + 2u * ((3u + rot) & 3u));                                                \
                if (rot & 1u) { uint x = a3; a3 = a2; a2 = a1; a1 = a0; a0 = x; }                                     \
                if (rot & 2u) { uint x = a2, y = a3; a2 = a0; a3 = a1; a0 = x; a1 = y; }                              \
                R[(4u * t) & 15u] += a0; R[(4u * t + 1u) & 15u] += a1; R[(4u * t + 2u) & 15u] += a2; R[(4u * t + 3u) & 15u] += a3; \
            }                                                                                                        \
        }                                                                                                            \
        for (uint kk = 0; kk < 4; kk++) {   /* the cell of 4.4 with w = h */                                         \
            uint a = R[4u * kk], b = R[4u * kk + 1u], c = R[4u * kk + 2u], d = R[4u * kk + 3u], wv = h4;             \
            a = a + wv; b = b ^ a; c = c + b * IH_MUL;                                                                \
            { uint n = a & 31u; d = d ^ c; d = n ? ((d >> n) | (d << (32u - n))) : d; }                              \
            a = a ^ (d >> (wv & 31u));                                                                               \
            { uint s = b + d; b = (s << 7) | (s >> 25); }                                                            \
            c = c ^ ((a << 17) | (a >> 15)); d = d + b * c;                                                          \
            R[4u * kk] = a; R[4u * kk + 1u] = b; R[4u * kk + 2u] = c; R[4u * kk + 3u] = d;                           \
        }                                                                                                            \
        { uint T[16]; for (uint qq = 0; qq < 16; qq++) T[qq] = R[qq]; for (uint qq = 0; qq < 16; qq++) R[qq] = T[(5u * qq + 1u) & 15u]; } \
        {   /* the exchange of 4.5, snapshot semantics */                                                            \
            uint p = lane ^ (1u << (r % 3u));                                                                        \
            uint qd = (lane + 1u + (R[1] % 7u)) & 7u;                                                                \
            uint xp0 = IGM_BCAST(R[0], p), xp2 = IGM_BCAST(R[2], p);                                                 \
            uint xq1 = IGM_BCAST(R[1], qd), xq3 = IGM_BCAST(R[3], qd);                                               \
            uint n0 = R[0] + xp0, n1 = R[1] ^ xq1, n2 = R[2] + ((xp2 << 11) | (xp2 >> 21)), n3 = R[3] ^ ((xq3 << 23) | (xq3 >> 9)); \
            R[0] = n0; R[1] = n1; R[2] = n2; R[3] = n3;                                                              \
        }                                                                                                            \
        { uint g = (r + 1u) & 7u; idx = (IGM_BCAST(R[0], g) ^ h4) & k->n_items_mask; }   /* 4.6 */                   \
    }

// the lean streamed finaliser of one chain: M = BLAKE3(tag || S || ST) from the registers by broadcast, then F
IGM_INLINE void ih_x2_final(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_PRIV const uint *R,
                            uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint S[16];
    {
        uint nl = nonce_lo, nh = nonce_hi;
#if defined(__AMDGCN__)
        __asm__ __volatile__("" : "+v"(nl), "+v"(nh));
#endif
        ih_stream_S(k, hdr8, nh, nl, S);
    }
    uint M[16];
    {
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16], o[16];
        IH_MIX_HEAD(m, S);
        uint prev = IGM_BCAST(R[0], 0u);
        m[12] = (S[7] >> 8) | (prev << 24);
        for (uint i = 13; i < 16; i++) { uint stn = IGM_BCAST(R[i - 12u], 0u); m[i] = (prev >> 8) | (stn << 24); prev = stn; }
        ih_b3_compress(cv, m, 0u, 64u, IH_CHUNK_START, o);
        for (uint i = 0; i < 8; i++) cv[i] = o[i];
        for (uint b = 1; b < 9; b++) {
            __attribute__((opencl_unroll_hint))
            for (uint i = 0; i < 16; i++) {
                const uint sx = 16u * b + i - 12u;
                uint stn = (sx < 128u) ? IGM_BCAST(R[(i + 4u) & 15u], (i < 12u) ? b - 1u : b) : 0u;
                m[i] = (prev >> 8) | (stn << 24); prev = stn;
            }
            const uint flags = (b == 8u) ? (IH_CHUNK_END | IH_ROOT) : 0u;
            ih_b3_compress(cv, m, 0u, b == 8u ? 51u : 64u, flags, o);
            if (b < 8u) for (uint i = 0; i < 8; i++) cv[i] = o[i];
        }
        for (uint i = 0; i < 16; i++) M[i] = o[i];
    }
    for (uint i = 0; i < 8; i++) mix_out8[i] = M[i];
    { uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint i = 0; i < 8; i++) final8[i] = Fo[i]; }
}

IGM_INLINE void igm_igneumhash_lane_x2(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nhA, uint nlA, uint nhB, uint nlB,
                                       IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, uint lane, IGM_XCH_PARAMS,
                                       IGM_PRIV uint *mixA, IGM_PRIV uint *finA, IGM_PRIV uint *mixB, IGM_PRIV uint *finB) {
    const uint iw = IGM_IH_ITEM_WORDS;
    uint RA[16], RB[16];
    { uint S[16]; ih_stream_S(k, hdr8, nhA, nlA, S); ih_stream_L(S, lane, RA); }
    { uint S[16]; ih_stream_S(k, hdr8, nhB, nlB, S); ih_stream_L(S, lane, RB); }
    uint idxA = (IGM_BCAST(RA[0], 0u) ^ IGM_BCAST(RA[8], 4u)) & k->n_items_mask;
    uint idxB = (IGM_BCAST(RB[0], 0u) ^ IGM_BCAST(RB[8], 4u)) & k->n_items_mask;
    for (uint r = 0; r < k->rounds; r++) {
#ifdef IGM_IH_SPLIT2
        IGM_GLOBAL const uint *itA = (idxA > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idxA - (k->n_items_mask >> 1u) - 1u) * iw : dataset + (ulong)idxA * iw;
        IGM_GLOBAL const uint *itB = (idxB > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idxB - (k->n_items_mask >> 1u) - 1u) * iw : dataset + (ulong)idxB * iw;
#else
        IGM_GLOBAL const uint *itA = dataset + (ulong)ih_slot(idxA, k->n_items_mask) * iw;
        IGM_GLOBAL const uint *itB = dataset + (ulong)ih_slot(idxB, k->n_items_mask) * iw;
#endif
        uint4 qA[4], qB[4];
        __attribute__((opencl_unroll_hint))
        for (uint t = 0; t < 4u; t++) qA[t] = IGM_LOAD4A(itA + 32u * t + 4u * lane);
        __attribute__((opencl_unroll_hint))
        for (uint t = 0; t < 4u; t++) qB[t] = IGM_LOAD4A(itB + 32u * t + 4u * lane);
        uint hA, hB;
        X2_ROUND(RA, qA, idxA, hA)
        X2_ROUND(RB, qB, idxB, hB)
    }
    ih_x2_final(k, hdr8, nhA, nlA, RA, lane, IGM_XCH_ARGS, mixA, finA);
    ih_x2_final(k, hdr8, nhB, nlB, RB, lane, IGM_XCH_ARGS, mixB, finB);
}
