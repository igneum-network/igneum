// igm_r0f.inc: VERSION 15, the bandwidth-first class (the founder's order of 13 October 2026 14:17 UK; the reference pow/ref's
// r16 text, hash/v16-bandwidth 6e5c0eda5). One nonce per octet of eight lanes: the work seed S as every version (ih_stream_S);
// the period key K (host: BLAKE3(LP("IgneumHash/R0F/key") || LE64 chain || LE64 period || anchor)) as eight words; the attempt's
// 8 x 32 registers from BLAKE3xof(LP("IgneumHash/R0F/init") || K || S) (lane l output blocks 2l and 2l + 1); READS / 16 iterations,
// each 16 independent 256-byte item reads at (R[lane (4b + q) mod 8][8b] XOR Kw[(16i + 4b + q) mod 8]) mod n_items, lane l taking
// words 8l..8l+8 of each item XOR Kw[(k + q) mod 8] into bank b's registers, then the period's programme (host-drawn, PROGRAM_OPS
// operations of eight bytes: opcode, d, a, b in the op's bank, the immediate) on every lane; M = BLAKE3(LP("IgneumHash/R0F/mix") ||
// K || the 256 registers LE32) (1,074 bytes, two chunks); F the common final over (S, M). Both dialects through the preludes' macros.
// The GPU B and Apple seat, 13 October 2026.
IGM_CONST uchar ih_tag_r0f_init[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','F','/','i','n','i','t'};
IGM_CONST uchar ih_tag_r0f_mix[19] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','F','/','m','i','x'};
#if defined(IGM_IH_SPLIT4)
#define IGM_R0F_DATASET4_PARAM , IGM_GLOBAL const uint *dataset3, IGM_GLOBAL const uint *dataset4
#define IGM_R0F_DATASET4_ARG , dataset3, dataset4
#else
#define IGM_R0F_DATASET4_PARAM
#define IGM_R0F_DATASET4_ARG
#endif
#define R0F_MAD 0u
#define R0F_ADD 1u
#ifndef IGM_R0F_STATIC
#define IGM_R0F_STATIC 0   // 1: the programme is the generated function igm_r0f_program (Intel; or --knob clopts=-DIGM_R0F_STATIC=1)
#endif
#ifndef IGM_R0F_AHEAD
#define IGM_R0F_AHEAD 1   // the reads-ahead form (two 16-byte loads per read, the sixteen issued together); 0 restores the scalar loop (--knob clopts=-DIGM_R0F_AHEAD=0)
#endif
#define R0F_SUB 2u
#define R0F_XOR 3u
#define R0F_ROTL 4u
#define R0F_ROTR 5u
#ifndef IGM_R0F_TRACE
#define IGM_R0F_TRACE 0   // 1: the staged trace (13 Oct 2026, the Arc B580 no-result fault): markers and stage words to a global buffer as the lane runs
#endif
#if IGM_R0F_TRACE
#define IGM_R0F_TRACE_PARAM , IGM_GLOBAL uint *tr
#define IGM_R0F_TRACE_ARG , r0f_trace
#define R0F_TR(stage) do { if (lane == 0u) tr[0] = (stage); } while (0)
#else
#define IGM_R0F_TRACE_PARAM
#define IGM_R0F_TRACE_ARG
#define R0F_TR(stage) do { } while (0)
#endif
IGM_INLINE void igm_r0f_lane(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM IGM_R0F_DATASET4_PARAM,
                             IGM_PRIV const uint *kw, IGM_GLOBAL const uint *prog, uint n_ops, uint reads, uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8 IGM_R0F_TRACE_PARAM) {
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);
#if IGM_R0F_TRACE
    if (lane == 0u) { for (uint q = 0; q < 8u; q++) tr[8u + q] = S[q]; } R0F_TR(1u);   // stage 1: the work seed
#endif
    uint Kw[8]; for (uint q = 0; q < 8u; q++) Kw[q] = kw[q];
    // the init: BLAKE3xof(lp(init) || K || S), 1024 bytes = 16 output blocks; lane l takes blocks 2l and 2l + 1 (its 32 registers)
    uint R[32];
    {
        uint w[32]; for (uint j = 0; j < 32; j++) w[j] = 0u;
        uint pos = ih_put_tag(w, 0u, ih_tag_r0f_init, 19u); pos = ih_put_bytes32(w, pos, Kw); pos = ih_put_bytes32(w, pos, S);   // 20 + 32 + 32 = 84 bytes: two blocks of one chunk
        uint cv[8]; for (uint q = 0; q < 8u; q++) cv[q] = igm_b3_iv[q];
        uint m0[16]; for (uint q = 0; q < 16u; q++) m0[q] = w[q];
        uint o[16]; ih_b3_compress(cv, m0, 0u, 64u, IH_CHUNK_START, o); for (uint q = 0; q < 8u; q++) cv[q] = o[q];
        uint m1[16]; for (uint q = 0; q < 16u; q++) { m1[q] = 0u; if (4u * q < pos - 64u) m1[q] = w[16u + q]; }
        const uint blen = pos - 64u;
        ih_b3_compress(cv, m1, (uint)(2u * lane), blen, IH_CHUNK_END | IH_ROOT, o); for (uint q = 0; q < 16u; q++) R[q] = o[q];
        ih_b3_compress(cv, m1, (uint)(2u * lane + 1u), blen, IH_CHUNK_END | IH_ROOT, o); for (uint q = 0; q < 16u; q++) R[16u + q] = o[q];
    }
#if IGM_R0F_TRACE
    for (uint q = 0; q < 32u; q++) tr[16u + 32u * lane + q] = R[q]; R0F_TR(2u);   // stage 2: the init registers, 32 per lane
#endif
    const uint mask = k->n_items_mask;
#if defined(IGM_IH_SPLIT4)
    const uint q_mask = mask >> 2u; uint q_shift = 0u; { uint t = q_mask + 1u; while (t > 1u) { t >>= 1u; q_shift++; } }
#endif
    for (uint i = 0; i < reads / 16u; i++) {
        // the sixteen addresses of the iteration from its entry state: slot s = 4b + q takes lane (s & 7)'s register 8b
        uint addr[16];
        for (uint s = 0; s < 16u; s++) { const uint b = s >> 2; const uint v = IGM_BCAST(R[8u * b], (s & 7u)); addr[s] = (v ^ Kw[(16u * i + s) & 7u]) & mask; }
#if IGM_R0F_TRACE
        if (i < 2u && lane == 0u) { for (uint s = 0; s < 16u; s++) tr[272u + 272u * i + s] = addr[s]; } R0F_TR(3u + 2u * i);   // stage 3 or 5: the iteration's sixteen addresses
#endif
#if IGM_R0F_AHEAD == 1
        // the memory-path cut (main's reading of 13 October, the 9060 XT at 24 percent of its bandwidth): the sixteen reads of the iteration
        // issued together as two 16-byte loads per lane each, before any register is touched; the XORs follow once every load is in flight
        uint4 ld[32];
        for (uint s = 0; s < 16u; s++) {
#if defined(IGM_IH_SPLIT4)
            const uint qn = addr[s] >> q_shift, qi = addr[s] & q_mask;
            IGM_GLOBAL const uint *it = (qn == 0u ? dataset : qn == 1u ? dataset2 : qn == 2u ? dataset3 : dataset4) + (ulong)qi * 64u + 8u * lane;
#elif defined(IGM_IH_SPLIT2)
            IGM_GLOBAL const uint *it = ((addr[s] > (mask >> 1u)) ? dataset2 + (ulong)(addr[s] - (mask >> 1u) - 1u) * 64u : dataset + (ulong)addr[s] * 64u) + 8u * lane;
#else
            IGM_GLOBAL const uint *it = dataset + (ulong)ih_slot(addr[s], mask) * 64u + 8u * lane;
#endif
            ld[2u * s] = IGM_LOAD4(it); ld[2u * s + 1u] = IGM_LOAD4(it + 4u);
        }
        for (uint s = 0; s < 16u; s++) {
            const uint b = s >> 2, q = s & 3u;
            const uint4 lo = ld[2u * s], hi = ld[2u * s + 1u];
            R[8u * b + 0u] ^= lo.x ^ Kw[(0u + q) & 7u]; R[8u * b + 1u] ^= lo.y ^ Kw[(1u + q) & 7u]; R[8u * b + 2u] ^= lo.z ^ Kw[(2u + q) & 7u]; R[8u * b + 3u] ^= lo.w ^ Kw[(3u + q) & 7u];
            R[8u * b + 4u] ^= hi.x ^ Kw[(4u + q) & 7u]; R[8u * b + 5u] ^= hi.y ^ Kw[(5u + q) & 7u]; R[8u * b + 6u] ^= hi.z ^ Kw[(6u + q) & 7u]; R[8u * b + 7u] ^= hi.w ^ Kw[(7u + q) & 7u];
        }
#elif IGM_R0F_AHEAD == 2
        // the two-group form (14 Oct 2026): eight loads issued and folded, then eight; read FLAT against the one-group form on the 9060 XT (1.39
        // against 1.38 MH/s on the loop-shaped worker). The one-group text above stays verbatim: its loop-shaped rewrite (c46feee4) cost the
        // 9060 XT 1.8x (2.52 to 1.38 MH/s), the compiler no longer keeping the landing registers in place.
#define R0F_G 8u
        for (uint g0 = 0; g0 < 16u; g0 += R0F_G) {
        uint4 ld[2u * R0F_G];
        for (uint s = g0; s < g0 + R0F_G; s++) {
#if defined(IGM_IH_SPLIT4)
            const uint qn = addr[s] >> q_shift, qi = addr[s] & q_mask;
            IGM_GLOBAL const uint *it = (qn == 0u ? dataset : qn == 1u ? dataset2 : qn == 2u ? dataset3 : dataset4) + (ulong)qi * 64u + 8u * lane;
#elif defined(IGM_IH_SPLIT2)
            IGM_GLOBAL const uint *it = ((addr[s] > (mask >> 1u)) ? dataset2 + (ulong)(addr[s] - (mask >> 1u) - 1u) * 64u : dataset + (ulong)addr[s] * 64u) + 8u * lane;
#else
            IGM_GLOBAL const uint *it = dataset + (ulong)ih_slot(addr[s], mask) * 64u + 8u * lane;
#endif
            ld[2u * (s - g0)] = IGM_LOAD4(it); ld[2u * (s - g0) + 1u] = IGM_LOAD4(it + 4u);
        }
        for (uint s = g0; s < g0 + R0F_G; s++) {
            const uint b = s >> 2, q = s & 3u;
            const uint4 lo = ld[2u * (s - g0)], hi = ld[2u * (s - g0) + 1u];
            R[8u * b + 0u] ^= lo.x ^ Kw[(0u + q) & 7u]; R[8u * b + 1u] ^= lo.y ^ Kw[(1u + q) & 7u]; R[8u * b + 2u] ^= lo.z ^ Kw[(2u + q) & 7u]; R[8u * b + 3u] ^= lo.w ^ Kw[(3u + q) & 7u];
            R[8u * b + 4u] ^= hi.x ^ Kw[(4u + q) & 7u]; R[8u * b + 5u] ^= hi.y ^ Kw[(5u + q) & 7u]; R[8u * b + 6u] ^= hi.z ^ Kw[(6u + q) & 7u]; R[8u * b + 7u] ^= hi.w ^ Kw[(7u + q) & 7u];
        }
        }
#else
        for (uint s = 0; s < 16u; s++) {
            const uint b = s >> 2, q = s & 3u;
#if defined(IGM_IH_SPLIT4)
            const uint qn = addr[s] >> q_shift, qi = addr[s] & q_mask;   // the quarter and the index within it (n_items a power of two)
            IGM_GLOBAL const uint *it = (qn == 0u ? dataset : qn == 1u ? dataset2 : qn == 2u ? dataset3 : dataset4) + (ulong)qi * 64u;
#elif defined(IGM_IH_SPLIT2)
            IGM_GLOBAL const uint *it = (addr[s] > (mask >> 1u)) ? dataset2 + (ulong)(addr[s] - (mask >> 1u) - 1u) * 64u : dataset + (ulong)addr[s] * 64u;
#else
            IGM_GLOBAL const uint *it = dataset + (ulong)ih_slot(addr[s], mask) * 64u;
#endif
            for (uint j = 0; j < 8u; j++) R[8u * b + j] ^= it[8u * lane + j] ^ Kw[(j + q) & 7u];
        }
#endif
#if IGM_R0F_STATIC
        igm_r0f_program(R);   // the period's programme as constant-index statements (the host generates it into the source per period)
        (void)prog; (void)n_ops;
#else
        for (uint p = 0; p < n_ops; p++) {
            const uint w0 = prog[2u * p], imm = prog[2u * p + 1u];
            const uint opc = w0 & 255u, d = (w0 >> 8) & 31u, a = (w0 >> 16) & 31u, bb = (w0 >> 24) & 31u;
            const uint av = R[a], bv = R[bb]; uint r;
            if (opc == R0F_MAD) r = av * bv + imm; else if (opc == R0F_ADD) r = av + bv + imm; else if (opc == R0F_SUB) r = av - bv - imm; else if (opc == R0F_XOR) r = av ^ bv ^ imm;
            else if (opc == R0F_ROTL) r = rotate(av, imm & 31u) ^ bv; else r = rotate(av, (32u - (imm & 31u)) & 31u) ^ bv;
            R[d] = r;
        }
#endif
#if IGM_R0F_TRACE
        if (i < 2u) { for (uint q = 0; q < 32u; q++) tr[272u + 272u * i + 16u + 32u * lane + q] = R[q]; } R0F_TR(4u + 2u * i);   // stage 4 or 6: the registers after the iteration's programme
#endif
    }
#if IGM_R0F_TRACE
    for (uint q = 0; q < 32u; q++) tr[816u + 32u * lane + q] = R[q]; R0F_TR(10u);   // stage 10: the registers after the last iteration
#endif
    // the mix: BLAKE3(lp(mix) || K || the 256 registers LE32) = 19 + 32 + 1024 = 1,075 bytes, two chunks; every lane gathers the octet's registers by broadcast (the same M on all)
    uint mw[288]; for (uint j = 0; j < 288u; j++) mw[j] = 0u;
    uint mp = ih_put_tag(mw, 0u, ih_tag_r0f_mix, 18u); mp = ih_put_bytes32(mw, mp, Kw);   // 19 + 32 = 51 bytes: the registers follow at byte 51 (not word aligned)
    for (uint l = 0; l < 8u; l++) for (uint q = 0; q < 32u; q++) { const uint v = IGM_BCAST(R[q], l); for (uint bb = 0; bb < 4u; bb++) ih_put8(mw, mp + 4u * (32u * l + q) + bb, v >> (8u * bb)); }
    uint M[16]; ih_b3_chunk(k->p, false, mw, mp + 1024u, 1u, M);
    for (uint q = 0; q < 8u; q++) mix_out8[q] = M[q];
#if IGM_R0F_TRACE
    if (lane == 0u) { for (uint q = 0; q < 8u; q++) tr[1072u + q] = M[q]; } R0F_TR(11u);   // stage 11: the mix
#endif
    uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint q = 0; q < 8u; q++) final8[q] = Fo[q];
#if IGM_R0F_TRACE
    if (lane == 0u) { for (uint q = 0; q < 8u; q++) tr[1080u + q] = Fo[q]; } R0F_TR(12u);   // stage 12: the final digest (1,088 words in all)
#endif
}
