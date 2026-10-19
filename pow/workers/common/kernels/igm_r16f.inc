// igm_r16f.inc: VERSION 16, the static-fabric integer coordinate (the founder's order of 13 October 2026 15:0x UK, C-36; the reference
// pow/ref's r16f text, main 89811d62). One work-item an attempt, no dataset: the work seed S as every version (ih_stream_S), the period
// key K as eight words (host), the 224 registers from BLAKE3xof(LP("IgneumHash/R0G/init") || K || S) (14 output blocks of the one 84-byte
// chunk), the period's programme as the generated function igm_r16f_program (static indices over the private array, emitted before this
// file), the mix M = BLAKE3(LP("IgneumHash/R0G/mix") || K || the 224 registers LE32) (19 + 32 + 896 = 947 bytes, one chunk), the final
// digest the common one over (S, M). Both dialects through the preludes' macros. The GPU B and Apple seat, 13 October 2026.
#ifndef IGM_R16F_TAILS
#define IGM_R16F_TAILS 0
#endif
// REVISION 3's fold chain F(x; v) with the finaliser (the R0E fold): x = rotl(x ^ v, 5) + v; x ^= x >> 16; x *= 0x9E3779B1; x ^= x >> 15; x *= 0x85EBCA6B; x ^= x >> 13
IGM_INLINE uint igm_r16f_fchain(uint x, uint v) { x = rotate(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
IGM_CONST uchar ih_tag_r16f_init[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','G','/','i','n','i','t'};
IGM_CONST uchar ih_tag_r16f_mix[19] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','G','/','m','i','x'};
IGM_INLINE void igm_r16f_attempt(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_PRIV const uint *Kw, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);
    uint R[224];
    {
        uint w[32]; for (uint j = 0; j < 32; j++) w[j] = 0u;
        uint pos = ih_put_tag(w, 0u, ih_tag_r16f_init, 19u); pos = ih_put_bytes32(w, pos, Kw); pos = ih_put_bytes32(w, pos, S);   // 20 + 32 + 32 = 84 bytes: two blocks of one chunk
#if IGM_R16F_TAILS
        // REVISION 3 (tails 1): ONE BLAKE3 of the 84 bytes to 32 bytes, expanded over the 224 registers by eight fold chains (register r = F(x; r) on chain r mod 8)
        uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d);
        uint x[8]; for (uint q = 0; q < 8u; q++) x[q] = d[q];
        IGM_UNROLL_FULL(224) for (uint r = 0; r < 224u; r++) { x[r & 7u] = igm_r16f_fchain(x[r & 7u], r); R[r] = x[r & 7u]; }   // unrolled: a runtime index into R sends the whole 224-register array to scratch (the 9060 XT port reading of 19:4x)
#else
        uint cv[8]; for (uint q = 0; q < 8u; q++) cv[q] = igm_b3_iv[q];
        uint m0[16]; for (uint q = 0; q < 16u; q++) m0[q] = w[q];
        uint o[16]; ih_b3_compress(cv, m0, 0u, 64u, IH_CHUNK_START, o); for (uint q = 0; q < 8u; q++) cv[q] = o[q];
        const uint blen = pos - 64u;
        uint m1[16]; for (uint q = 0; q < 16u; q++) { m1[q] = 0u; if (4u * q < blen) m1[q] = w[16u + q]; }
        IGM_UNROLL_FULL(14) for (uint t = 0; t < 14u; t++) { ih_b3_compress(cv, m1, t, blen, IH_CHUNK_END | IH_ROOT, o); IGM_UNROLL_FULL(16) for (uint q = 0; q < 16u; q++) R[16u * t + q] = o[q]; }
#endif
    }
    igm_r16f_program(R);
#if IGM_R16F_TAILS
    // REVISION 3 (tails 1): the fold over the 224 registers into eight words (chain r mod 8 from K's word), then ONE BLAKE3 of (tag || K || the eight words): 83 bytes
    uint mw[32]; for (uint j = 0; j < 32u; j++) mw[j] = 0u;
    uint mp = ih_put_tag(mw, 0u, ih_tag_r16f_mix, 18u); mp = ih_put_bytes32(mw, mp, Kw);
    { uint x[8]; for (uint q = 0; q < 8u; q++) x[q] = Kw[q];
      IGM_UNROLL_FULL(224) for (uint r = 0; r < 224u; r++) x[r & 7u] = igm_r16f_fchain(x[r & 7u], R[r]);
      for (uint q = 0; q < 8u; q++) for (uint bb = 0; bb < 4u; bb++) ih_put8(mw, mp + 4u * q + bb, x[q] >> (8u * bb)); }
    uint M[16]; ih_b3_chunk(k->p, false, mw, mp + 32u, 1u, M);
#else
    uint mw[240]; for (uint j = 0; j < 240u; j++) mw[j] = 0u;
    uint mp = ih_put_tag(mw, 0u, ih_tag_r16f_mix, 18u); mp = ih_put_bytes32(mw, mp, Kw);   // 51 bytes, the registers follow at byte 51
    IGM_UNROLL_FULL(224) for (uint q = 0; q < 224u; q++) for (uint bb = 0; bb < 4u; bb++) ih_put8(mw, mp + 4u * q + bb, R[q] >> (8u * bb));
    uint M[16]; ih_b3_chunk(k->p, false, mw, mp + 896u, 1u, M);
#endif
    for (uint q = 0; q < 8u; q++) mix_out8[q] = M[q];
    uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint q = 0; q < 8u; q++) final8[q] = Fo[q];
}
