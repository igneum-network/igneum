// igm_r0e_access.inc: the table access macros of versions 12 to 14 and version 14's tile and fold (SPEC-R0E-v0 2c to 2e, main 2a12dee9),
// loaded by the engines before the generated period programme (igm_r0e_program) and before igm_igneumhash.inc; both dialects through the
// preludes' macros (IGM_LOCAL, IGM_GLOBAL, IGM_PRIV, IGM_INLINE, IGM_BCAST, IGM_DOT4S/U). GPU engineer B, 13 October 2026.
#ifndef IGM_IH_AW
#define IGM_IH_AW 4u
#endif
// the row's words consumed by the MINIMAL glue (main 2a12dee9: the row's XOR, about three instructions per 16-byte word); the scatter writes the row from the
// registers src1.. XOR dst with no load of the old row and returns the written row's fold; the shared gather reads the period's table in global memory
#define IGM_R12_GATHER(T, row) igm_r12_fold(T, row)
#define IGM_R12_GATHER_G(T, row) igm_r12_fold_g(T, row)
IGM_INLINE uint igm_r12_fold(IGM_LOCAL const uint *T, uint row) { uint v = T[row * IGM_IH_AW]; for (uint i = 1u; i < IGM_IH_AW; i++) v ^= T[row * IGM_IH_AW + i]; return v; }
IGM_INLINE uint igm_r12_fold_g(IGM_GLOBAL const uint *T, uint row) { uint v = T[row * IGM_IH_AW]; for (uint i = 1u; i < IGM_IH_AW; i++) v ^= T[row * IGM_IH_AW + i]; return v; }
IGM_INLINE uint igm_r12_scatter(IGM_LOCAL uint *T, uint row, IGM_PRIV const uint *R, uint s1, uint dst) { uint v = 0u; for (uint i = 0; i < IGM_IH_AW; i++) { const uint w = R[(s1 + i) & 15u] ^ R[dst & 15u]; T[row * IGM_IH_AW + i] = w; v ^= w; } return v; }
#define IGM_R12_SCATTER(T, row, R, s1, dst) igm_r12_scatter(T, row, R, s1, dst)
// version 14 (SPEC-R0E-v0 2e, main 2a12dee9): the exact INT8 16x8x32 tile. A is the octet's state read as 16 rows of 32 signed bytes: row m lives in lane
// ((m & 7) + rot) & 7 (reversed under the orientation layout), registers (m >> 3) * 8 .. + 7 little-endian; B is the item's 256 bytes read as 32 x 8
// signed (or unsigned, the format layout) bytes row-major from the word offset (R[src2] of lane (imm & 7) XOR imm XOR topo) & 63; C[m][n], the exact
// INT32 product, lands in lane (m & 7)'s register (dst + (topo >> 9) + (m >> 3) * 8 + n) & 15 by XOR (or ADD, the accumulation layout). This lane
// computes its own two rows (m & 7 = lane): the A words come from lane la by sixteen octet broadcasts, each dot over a quad of k by the signed
// (or mixed) 4-byte dot (the builtin where the compiler has it, the scalar form otherwise: the same integers)
#ifndef IGM_DOT4S
// a byte of a word as a signed value by arithmetic (the narrowing cast (char) of an out-of-range uint is implementation-defined in MSL;
// the M6 read the k8l light classes wrong where the tile's dot products carry it; 14 Oct 2026)
IGM_INLINE int igm_sbyte(uint w, uint j) { const int v = (int)((w >> (8u * j)) & 255u); return v - ((v >> 7) << 8); }
IGM_INLINE int igm_dot4s_scalar(uint a, uint b, int c) { for (uint j = 0; j < 4u; j++) c += igm_sbyte(a, j) * igm_sbyte(b, j); return c; }
IGM_INLINE int igm_dot4u_scalar(uint a, uint b, int c) { for (uint j = 0; j < 4u; j++) c += igm_sbyte(a, j) * (int)((b >> (8u * j)) & 255u); return c; }
#define IGM_DOT4S(a, b, c) igm_dot4s_scalar((a), (b), (c))
#define IGM_DOT4U(a, b, c) igm_dot4u_scalar((a), (b), (c))
#endif
// the fetched item as the programme reads it (SPEC-R0A section 0.9): on a keyed round the share's eight words sit in the item's first eight
// (the reference XORs them into W before the absorb, the programme and the tree); ks8 is zero on every other round; a select chain, no
// runtime index into the private share (13 Oct 2026: the keyed k8l class read 32 of 32 wrong on OpenCL and Metal with the item and the
// tile reading the unshared words)
IGM_INLINE uint igm_r0e_item_word(IGM_GLOBAL const uint *item, uint idx, IGM_PRIV const uint *ks8) {
    uint sv = ks8[0]; sv = (idx == 1u) ? ks8[1] : sv; sv = (idx == 2u) ? ks8[2] : sv; sv = (idx == 3u) ? ks8[3] : sv; sv = (idx == 4u) ? ks8[4] : sv; sv = (idx == 5u) ? ks8[5] : sv; sv = (idx == 6u) ? ks8[6] : sv; sv = (idx == 7u) ? ks8[7] : sv;
    return item[idx] ^ ((idx < 8u) ? sv : 0u);
}
IGM_INLINE void igm_r14_tile(IGM_PRIV uint *R, uint lane, IGM_XCH_PARAMS, IGM_GLOBAL const uint *item, IGM_PRIV const uint *ks8, uint s1, uint s2, uint d, uint imm, uint topo, uint lay) {
    const uint off = (IGM_BCAST(R[s2 & 15u], (imm & 7u)) ^ imm ^ topo) & 63u;
    const uint rot = (s1 + (topo >> 6)) & 7u, dbase = (d + (topo >> 9)) & 15u;
    const uint orient = lay & 1u, accum_add = (lay >> 1) & 1u, b_unsigned = (lay >> 2) & 1u;
#if defined(IGM_IH_TILE_LAYOUT) && IGM_IH_TILE_LAYOUT == 1
    // version 14.1 (C-21.1, the reference's fragment layout): the octet's 8 lanes x 16 registers ARE the m16n8k32 fragment: A row l and row
    // l + 8 live in lane l (register 4q + j: j & 1 the row half, j >> 1 the k half, q the k quad), B is read N x K (column n's 32 bytes at
    // item words off + col * 8 .. + 7, the orientation reversing the k order), C[m][n] lands in lane (m & 7)'s register 4 (n >> 1) +
    // ((2 (m >> 3) + (n & 1) + dbase) & 3); rot rotates B's column. Nothing crosses lanes: the registered card's tile cost is gone.
    (void)lane;
    uint A[16]; for (uint g = 0; g < 16u; g++) A[g] = R[g];   // the tile's input state (A is read before C is written; the registers alias)
    for (uint n = 0; n < 8u; n++) {
        const uint col = (n + rot) & 7u;
        for (uint h = 0; h < 2u; h++) {
            int t = 0;
            for (uint q = 0; q < 8u; q++) {
                const uint a = A[4u * (q & 3u) + h + 2u * (q >> 2)];
                const uint w = orient ? igm_bswap32(igm_r0e_item_word(item, (off + col * 8u + 7u - q) & 63u, ks8)) : igm_r0e_item_word(item, (off + col * 8u + q) & 63u, ks8);
                t = b_unsigned ? IGM_DOT4U(a, w, t) : IGM_DOT4S(a, w, t);
            }
            const uint di = 4u * (n >> 1) + ((2u * h + (n & 1u) + dbase) & 3u);
            R[di] = accum_add ? R[di] + (uint)t : R[di] ^ (uint)t;
        }
    }
#else
    const uint la = ((orient ? (7u - lane) : lane) + rot) & 7u;   // the lane whose registers hold this lane's two rows of A
    uint A[16]; for (uint g = 0; g < 16u; g++) A[g] = IGM_BCAST(R[g], la);
    for (uint n = 0; n < 8u; n++) {
        // B's column n over k = 0..31 as eight packed quads: byte (n & 3) of the item words (off + 2 k + (n >> 2)) & 63
        uint bq[8];
        for (uint q = 0; q < 8u; q++) { uint w = 0u; for (uint j = 0; j < 4u; j++) { const uint kk = 4u * q + j; w |= ((igm_r0e_item_word(item, (off + 2u * kk + (n >> 2)) & 63u, ks8) >> (8u * (n & 3u))) & 255u) << (8u * j); } bq[q] = w; }
        for (uint h = 0; h < 2u; h++) {
            int t = 0;
            for (uint q = 0; q < 8u; q++) { const uint g = orient ? (7u - q) : q; const uint a = A[h * 8u + g]; t = b_unsigned ? IGM_DOT4U(a, bq[q], t) : IGM_DOT4S(a, bq[q], t); }
            const uint di = (dbase + h * 8u + n) & 15u;
            R[di] = accum_add ? R[di] + (uint)t : R[di] ^ (uint)t;
        }
    }
#endif
}
#define IGM_R14_TILE(R, lane, xa, item, ks8, s1, s2, d, imm, topo, lay) igm_r14_tile(R, lane, xa, item, ks8, s1, s2, d, imm, topo, lay)
// the topology word from the product (the fold over every lane's landing register, finalised) and the register scramble: register q of lane l takes
// lane (l + ((topo >> 2q) + q) mod 8)'s; every fold_every tiles
IGM_INLINE uint igm_r14_fold(IGM_PRIV uint *R, uint lane, IGM_XCH_PARAMS, uint dbase) {
    uint t = 0u; for (uint l = 0; l < 8u; l++) { const uint v = IGM_BCAST(R[dbase], l); t = rotate(t ^ v, 5u) + v; }
    t ^= t >> 16; t *= 0x9E3779B1u; t ^= t >> 15;
    for (uint q = 0; q < 16u; q++) { const uint sh = ((t >> (2u * q)) + q) & 7u; R[q] = IGM_BCAST(R[q], ((lane + sh) & 7u)); }
    return t;
}
#define IGM_R14_FOLD(R, lane, xa, dbase, topo) do { (topo) = igm_r14_fold(R, lane, xa, (dbase)); } while (0)
// the tile: a from this lane's registers src1 and src1 + 1, b from lane k's src2 and src2 + 1 (two octet broadcasts per k), register dst + k ^= the INT32 dot product of the eight signed bytes
#define IGM_R12_MMA(R, s1, s2, d) do { const uint a0_ = R[(s1) & 15u], a1_ = R[((s1) + 1u) & 15u], b0_ = R[(s2) & 15u], b1_ = R[((s2) + 1u) & 15u]; uint kb0_[8], kb1_[8]; \
    for (uint k_ = 0; k_ < 8u; k_++) { kb0_[k_] = IGM_BCAST(b0_, k_); kb1_[k_] = IGM_BCAST(b1_, k_); } \
    int acc_[8]; for (uint k_ = 0; k_ < 8u; k_++) { int t_ = 0; for (uint j_ = 0; j_ < 4u; j_++) { t_ += igm_sbyte(a0_, j_) * igm_sbyte(kb0_[k_], j_); t_ += igm_sbyte(a1_, j_) * igm_sbyte(kb1_[k_], j_); } acc_[k_] = t_; } \
    for (uint k_ = 0; k_ < 8u; k_++) R[((d) + k_) & 15u] ^= (uint)acc_[k_]; } while (0)
