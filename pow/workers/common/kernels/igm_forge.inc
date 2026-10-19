// igm_forge.inc: VERSION 20, FORGE, the per-hash programme discipline (FORM-FORGE.md; the reference's namespace forge on main 9f739fef,
// the port's text its head comment). An attempt is 16 lanes (work-items) x 32 registers; each lane's 1 KiB scratchpad (256 words) in
// local memory (the attempt's 4096 words), the attempt's 16-word exchange beside it; the programme 32 words of 16 bytes by the fold
// chains over BLAKE3(LP("IgneumHash/Forge/program") || E || S), each word 32 steps; the slot k of every word writes register k and reads
// registers 7k + 1 and 11k + 5 (mod 32), the class by the QUOTA table (the fixed order) or the word's Fisher-Yates of it (drawn_order 1);
// the FP32 slots under the exponent mask with fused fma; the keyed read of the epoch dataset's item every fold_every steps; the digest
// over E, the programme bytes' hash, the 512 registers and the scratchpad. The SIMT rule: every lane's reads of a slot precede any
// write of it (the cross-lane read through the exchange, written and barriered before the slot's writes).
#if IGM_IH_VERSION == 20 || IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25
#pragma OPENCL FP_CONTRACT OFF
#define IGM_FORGE_LANES 16u
// VERSION 25, ANVIL FOR OLD CARDS (entry 2; the reference's namespace oldcard on main 6f75d586c, FORM-FORGE.md section 20): version 21's
// Anvil-R programme (fp_mode 6, lean, pair, prefetch) with four steps of five (t mod 5 != 4) SCRATCH STEPS into a PER-HASH SCRATCHPAD of
// IGM_FORGE_OLDCARD bytes (scratch_kib 4, 16 or 64) in local memory: lane l reads 8 bytes at (r0 ^ r1 x 0x9E3779B9) mod S aligned to 8
// (every lane before any write), mixes r2 = fchain(r2 ^ lo, t), r3 = (r3 + hi) ^ rotl(lo, 13), writes LE32(r2) || LE32(r3) into its own
// slice at l x S/16 + ((r2 ^ r3 x 0x85EBCA6B) mod S/16 aligned to 8); r0..r3 = R[(4t + 0..3) mod 32]. The scratchpad is seeded from the
// init chain after the window and each lane's slice folded into its lean accumulator. Under version 25 the attempt's local block is
// the window's 256 words (not the 16 KiB scratchpad of version 20), the exchange, then the scratchpad.
#ifndef IGM_FORGE_OLDCARD
#define IGM_FORGE_OLDCARD 0
#endif
#define FG_V2 (IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25)
#if IGM_FORGE_OLDCARD
#define IGM_FORGE_SCRATCH_WORDS 256u    // the window: 16 words a lane
#define IGM_FORGE_SP8_WORDS (IGM_FORGE_OLDCARD / 4u)
#define IGM_FORGE_SP8_SLICE (IGM_FORGE_SP8_WORDS / 16u)   // the lane's slice in words
#else
#define IGM_FORGE_SCRATCH_WORDS 4096u   // 16 KiB an attempt
#define IGM_FORGE_SP8_WORDS 0u
#define IGM_FORGE_SP8_SLICE 0u
#endif
#define IGM_FORGE_XCH_WORDS 16u         // the attempt's exchange (the cross-lane reads, the digest's register pull)
#define IGM_FORGE_ATTEMPT_WORDS (IGM_FORGE_SCRATCH_WORDS + IGM_FORGE_XCH_WORDS + IGM_FORGE_SP8_WORDS)
#ifndef IGM_FORGE_DRAWN
#define IGM_FORGE_DRAWN 0
#endif
#ifndef IGM_FORGE_TRACE
#define IGM_FORGE_TRACE 0
#endif
IGM_CONST uchar ih_tag_forge_program[24] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','p','r','o','g','r','a','m'};
IGM_CONST uchar ih_tag_forge_init[21] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','i','n','i','t'};
IGM_CONST uchar ih_tag_forge_mix[20] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','m','i','x'};
IGM_CONST uchar ih_tag_forge_period[23] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','p','e','r','i','o','d'};
IGM_CONST uchar ih_tag_forge_mix2[21] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','m','i','x','2'};
IGM_CONST uchar ih_tag_forge_sequence[25] = {'I','g','n','e','u','m','H','a','s','h','/','F','o','r','g','e','/','s','e','q','u','e','n','c','e'};
#ifndef IGM_FORGE_PREFETCH
#define IGM_FORGE_PREFETCH 0
#endif
#ifndef IGM_FORGE_PAIR_LOG2
#define IGM_FORGE_PAIR_LOG2 0
#endif
#ifndef IGM_FORGE_LEAN
#define IGM_FORGE_LEAN 0
#endif
#ifndef IGM_FORGE_PERIOD_TABLE
#define IGM_FORGE_PERIOD_TABLE 0
#endif
// the classes and the quota table (the reference's consensus constants)
#define FG_FMA 0u
#define FG_FADDSUB 1u
#define FG_FMUL 2u
#define FG_IALU 3u
#define FG_ROT 4u
#define FG_MUL 5u
#define FG_MULHI 6u
#define FG_I2F 7u
#define FG_F2I 8u
#define FG_XLANE 9u
#define FG_STORE 10u
#define FG_CMOV 11u
IGM_CONST uchar igm_forge_quota[32] = { 3, 0, 4, 3, 9, 1, 3, 5, 3, 2, 4, 3, 10, 0, 3, 4, 6, 1, 3, 7, 4, 3, 9, 2, 3, 0, 4, 11, 1, 3, 8, 5 };
// VERSION 21's fp_mode (the reference's forge2, main 8e1277fc5): 0 as cut; 1 INTEGER-ONLY (the ten FP slots to IALU 5, ROT 2, MUL 1, XLANE 1, STORE 1
// in the FP positions' order); 2 PRODUCER-MASKED (the FP slots on the bank r24..r31, masked once at the producer); 3 THE OCCUPANCY FORM (integer-only,
// the scratchpad 64 bytes a lane, the ILP wiring a = k + 21, b = k + 9); 4 the shallow step (a = k + 24, b = k + 16); 5 sixteen registers (d = k & 15,
// a = k + 11, b = k + 7 mod 16); 6 ANVIL-R (the scratchpad replaced by a rotating window of 16 words a lane with the shape's static offsets)
IGM_CONST uchar igm_forge_quota_int[32] = { 3, 3, 4, 3, 9, 3, 3, 5, 3, 4, 4, 3, 10, 3, 3, 4, 6, 3, 3, 5, 4, 3, 9, 9, 3, 3, 4, 11, 10, 3, 3, 5 };
#ifndef IGM_FORGE_FPMODE
#define IGM_FORGE_FPMODE 0
#endif
#if FG_V2
#define FG_FPM IGM_FORGE_FPMODE
#else
#define FG_FPM 0
#endif
#define FG_OCC (FG_FPM >= 3)
#define FG_WINDOW (FG_FPM == 6)
#define FG_NREG ((FG_FPM == 5) ? 16u : 32u)                        // the registers a lane
#define FG_SPW ((FG_FPM == 6) ? 0u : (FG_OCC ? 16u : 256u))       // the scratchpad words a lane in the digest
#define FG_SPSTORE ((FG_FPM == 6) ? 16u : FG_SPW)                  // the local words a lane (the window's 16 under mode 6)
#define FG_SPMASK (FG_OCC ? 63u : 1023u)
#if FG_FPM == 2
#define FG_OPMASK(x) (x)
#else
#define FG_OPMASK(x) igm_forge_fmask(x)
#endif
IGM_INLINE uint igm_forge_fchain(uint x, uint v) { x = rotate(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
IGM_INLINE uint igm_forge_fmask(uint u) { const uint e = (u >> 23) & 255u; const uint e2 = (e == 0u || e == 255u) ? 127u : (e < 111u ? 111u : e > 143u ? 143u : e); return (u & 0x807FFFFFu) | (e2 << 23); }
IGM_INLINE float igm_forge_f(uint u) { return IGM_U2F(u); }
IGM_INLINE uint igm_forge_u(float f) { return IGM_F2U(f); }
// the streamed BLAKE3 of the mix input (unkeyed; the total length known): the r17 streamer's text
typedef struct { uint cv[8]; uint stack[80]; uint depth; uint chunk; uint blk[16]; uint blen; uint bic; uint fed; uint total; } igm_forge_b3;
IGM_INLINE void igm_forge_b3_init(IGM_PRIV igm_forge_b3 *h, uint total) { for (uint i = 0; i < 8u; i++) h->cv[i] = igm_b3_iv[i]; for (uint i = 0; i < 16u; i++) h->blk[i] = 0u; h->depth = 0u; h->chunk = 0u; h->blen = 0u; h->bic = 0u; h->fed = 0u; h->total = total; }
IGM_INLINE void igm_forge_b3_parent(IGM_PRIV const uint *l, IGM_PRIV const uint *r, uint root, IGM_PRIV uint *out8) {
    uint m[16]; for (uint i = 0; i < 8u; i++) { m[i] = l[i]; m[8u + i] = r[i]; } uint iv[8]; for (uint i = 0; i < 8u; i++) iv[i] = igm_b3_iv[i];
    uint o[16]; ih_b3_compress(iv, m, 0u, 64u, IH_PARENT | (root ? IH_ROOT : 0u), o); for (uint i = 0; i < 8u; i++) out8[i] = o[i];
}
IGM_INLINE void igm_forge_b3_block(IGM_PRIV igm_forge_b3 *h, IGM_PRIV uint *root8) {
    const uint last_of_input = (h->fed == h->total) ? 1u : 0u, last_of_chunk = (h->bic == 15u || last_of_input) ? 1u : 0u;
    uint flags = 0u; if (h->bic == 0u) flags |= IH_CHUNK_START; if (last_of_chunk) flags |= IH_CHUNK_END;
    const uint root_here = (last_of_input && h->depth == 0u) ? 1u : 0u; if (root_here) flags |= IH_ROOT;
    uint o[16]; ih_b3_compress(h->cv, h->blk, h->chunk, h->blen, flags, o);
    for (uint i = 0; i < 16u; i++) h->blk[i] = 0u; h->blen = 0u;
    if (root_here) { for (uint i = 0; i < 8u; i++) root8[i] = o[i]; return; }
    if (!last_of_chunk) { for (uint i = 0; i < 8u; i++) h->cv[i] = o[i]; h->bic++; return; }
    uint c[8]; for (uint i = 0; i < 8u; i++) c[i] = o[i];
    h->chunk++; h->bic = 0u; for (uint i = 0; i < 8u; i++) h->cv[i] = igm_b3_iv[i];
    if (last_of_input) { while (h->depth > 0u) { h->depth--; uint p[8]; igm_forge_b3_parent(&h->stack[8u * h->depth], c, h->depth == 0u ? 1u : 0u, p); for (uint i = 0; i < 8u; i++) c[i] = p[i]; } for (uint i = 0; i < 8u; i++) root8[i] = c[i]; return; }
    uint t = h->chunk;
    while ((t & 1u) == 0u) { h->depth--; uint p[8]; igm_forge_b3_parent(&h->stack[8u * h->depth], c, 0u, p); for (uint i = 0; i < 8u; i++) c[i] = p[i]; t >>= 1u; }
    for (uint i = 0; i < 8u; i++) h->stack[8u * h->depth + i] = c[i]; h->depth++;
}
IGM_INLINE void igm_forge_b3_put8(IGM_PRIV igm_forge_b3 *h, uint b, IGM_PRIV uint *root8) { h->blk[h->blen >> 2] |= (b & 0xffu) << (8u * (h->blen & 3u)); h->blen++; h->fed++; if (h->blen == 64u || h->fed == h->total) igm_forge_b3_block(h, root8); }
IGM_INLINE void igm_forge_b3_put32(IGM_PRIV igm_forge_b3 *h, uint w, IGM_PRIV uint *root8) { for (uint bb = 0; bb < 4u; bb++) igm_forge_b3_put8(h, w >> (8u * bb), root8); }
// the slot: the class C on (a, b, d0) with imm and q; the cross-lane value xl (the other lane's register a_k, read before the slot's writes);
// returns the new d (d0 when the slot stores); the store's address and value through st (st[0] = 1 when stored)
IGM_INLINE uint igm_forge_slot(uint C, uint a, uint b, uint d0, uint imm, uint q, uint xl, uint himm, IGM_PRIV uint *st) {
    st[0] = 0u;
    switch (C) {
        case FG_FMA: return igm_forge_fmask(igm_forge_u(fma(igm_forge_f(FG_OPMASK(a)), igm_forge_f(FG_OPMASK(b)), igm_forge_f(FG_OPMASK(d0)))));
        case FG_FADDSUB: { const float fa = igm_forge_f(FG_OPMASK(a)), fb = igm_forge_f(FG_OPMASK(b)); return igm_forge_fmask(igm_forge_u((q & 1u) ? (fa - fb) : (fa + fb))); }
        case FG_FMUL: { const float fa = igm_forge_f(FG_OPMASK(a)), fb = igm_forge_f(FG_OPMASK(b)); return igm_forge_fmask(igm_forge_u(fa * fb)); }
        case FG_IALU: switch (q % 5u) { case 0u: return a + b + imm; case 1u: return a - b - imm; case 2u: return a ^ b ^ imm; case 3u: return (a & b) ^ imm; default: return (a | b) ^ imm; }
        // ROT case 1, the reverse rotate, through the prelude's IGM_ROTR_VAR (the Intel compiler folds rotate(x, (32 - n) & 31) into a left rotate: the Arc B580's F-133 at step 0 slot 15, 15 Oct 2026; the 7 Oct fault again)
        case FG_ROT: { const uint sh = (imm ^ b) & 31u; switch (q & 3u) { case 0u: return rotate(a, sh); case 1u: return IGM_ROTR_VAR(a, sh); case 2u: return (a << sh) + b; default: return (a >> sh) + b; } }
        case FG_MUL: return a * b + imm;
        case FG_MULHI: return IGM_MULHI(a, b) ^ imm;
        case FG_I2F: return igm_forge_fmask(igm_forge_u((float)((int)(a & 0x00FFFFFFu))));
        case FG_F2I: return FG_OPMASK(a) ^ imm;
        case FG_XLANE: return xl + imm;
        case FG_STORE: st[0] = 1u; st[1] = ((a + himm) & FG_SPMASK) >> 2; st[2] = b; return d0;
        default: return ((b >> (himm & 31u)) & 1u) ? a : d0;   // CMOV (the hash's predicate bit under 21, the word's under 20)
    }
}
// version 25's scratch step on the registers i0..i3: the read (every lane, then the barrier), the mix, the write into the lane's slice (then the barrier before the next step's reads)
#define IGM_FORGE_SP8_STEP(i0, i1, i2, i3) { const uint ra = ((R[i0] ^ (R[i1] * 0x9E3779B9u)) & (IGM_FORGE_OLDCARD - 8u) & ~7u) >> 2; lo = sp8[ra]; hi = sp8[ra + 1u]; IGM_FORGE_BARRIER(); \
    R[i2] = igm_forge_fchain(R[i2] ^ lo, t); R[i3] = (R[i3] + hi) ^ rotate(lo, 13u); \
    const uint wa = lane * IGM_FORGE_SP8_SLICE + ((((R[i2] ^ (R[i3] * 0x85EBCA6Bu)) & (4u * IGM_FORGE_SP8_SLICE - 8u) & ~7u)) >> 2); sp8[wa] = R[i2]; sp8[wa + 1u] = R[i3]; IGM_FORGE_BARRIER(); }
// the attempt: lane = this work-item's index in its sixteen, sp = the attempt's 4096 scratchpad words (the lane's 256 at lane * 256), xl =
// the attempt's 16 exchange words; dataset = the epoch's items (64 words each, item i at i * 64); every lane runs every step; the digest
// by lane 0 (the registers pulled through the exchange, the scratchpad read from local memory)
IGM_INLINE void igm_forge_attempt(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, uint lane, IGM_LOCAL uint *sp, IGM_LOCAL uint *xl, IGM_LOCAL uint *sp8, IGM_GLOBAL const uint *dataset, IGM_GLOBAL const uint *ptab, uint steps, uint words, uint fold_every, IGM_GLOBAL uint *trace, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);
#if FG_V2
    // VERSION 21 (forge2): the period seed BLAKE3(Forge/period || E || header || LE64 (nonce >> batch_log2)); the shapes (program_ops of them) by the chains over it, each with its drawn class order
    const uint L = k->blocks, bl = k->batch_log2; uint pseed[8];
    if (bl == 0u) { for (uint q = 0; q < 8u; q++) pseed[q] = k->sk[q]; }   // THE CHAIN FORM (period_blocks set, F-141): the period seed keyed on the period of blocks and its anchor, computed on the host per job and carried in the sk words
    else { uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u; uint pos = ih_put_tag(w, 0u, ih_tag_forge_period, 23u); pos = ih_put_bytes32(w, pos, k->e); pos = ih_put_bytes32(w, pos, hdr8);
      const uint sh_lo = bl >= 32u ? (bl == 32u ? nonce_hi : (nonce_hi >> (bl - 32u))) : ((nonce_lo >> bl) | (bl ? (nonce_hi << (32u - bl)) : 0u)), sh_hi = bl >= 32u ? 0u : (nonce_hi >> bl);
      for (uint bb = 0; bb < 4u; bb++) ih_put8(w, pos + bb, sh_lo >> (8u * bb)); for (uint bb = 0; bb < 4u; bb++) ih_put8(w, pos + 4u + bb, sh_hi >> (8u * bb)); pos += 8u;
      uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d); for (uint q = 0; q < 8u; q++) pseed[q] = d[q]; }
#if IGM_FORGE_PERIOD_TABLE
    // THE PERIOD TABLE (the scalar path, 15 Oct 2026 12:xx UK): the shapes' words, class orders and slot immediates computed once on the host per period and bound as a read-only buffer: words 0..4 W - 1 the programme words, then per shape i at 4 W + 96 i: 32 classes, 32 imm, 32 q; the shape index broadcast so the loads take the scalar path
    (void)pseed;
#else
    uint prog[128]; uint shcls[16 * 32];   // the shapes' words and their drawn class orders (program_ops up to 16, L up to 32 in this worker)
    { uint x[8]; for (uint q = 0; q < 8u; q++) x[q] = pseed[q]; for (uint i = 0; i < words * 4u; i++) { x[i & 7u] = igm_forge_fchain(x[i & 7u], i); prog[i] = x[i & 7u]; }
      for (uint i = 0; i < words; i++) { for (uint q = 0; q < 32u; q++) shcls[32u * i + q] = (FG_FPM == 1 || FG_OCC) ? igm_forge_quota_int[q] : igm_forge_quota[q]; for (uint qq = 31u; qq > 0u; qq--) { const uint j = igm_forge_fchain(prog[4u * i], qq) % (qq + 1u); const uint tmp = shcls[32u * i + qq]; shcls[32u * i + qq] = shcls[32u * i + j]; shcls[32u * i + j] = tmp; } } }
#endif
    uint seed[8];
    { uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u; uint pos = ih_put_tag(w, 0u, ih_tag_forge_program, 24u); pos = ih_put_bytes32(w, pos, k->e); pos = ih_put_bytes32(w, pos, S); uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d); for (uint q = 0; q < 8u; q++) seed[q] = d[q]; }
    // the sequence, streamed entry by entry (L up to 512): the hash's chain y gives seq (y[i & 7] at i) and hw (y[(i + 4) & 7] at 4096 + i);
    // under (c3) the sequence comes from the pair's seed instead (tag Forge/sequence || E || header || LE64 (nonce >> pair_log2)), hw stays the hash's
    uint y[8]; for (uint q = 0; q < 8u; q++) y[q] = seed[q];
#if IGM_FORGE_PAIR_LOG2
    uint ys[8];
    { uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u; uint pos = ih_put_tag(w, 0u, ih_tag_forge_sequence, 25u); pos = ih_put_bytes32(w, pos, k->e); pos = ih_put_bytes32(w, pos, hdr8);
      const uint pl = IGM_FORGE_PAIR_LOG2; const uint q_lo = (nonce_lo >> pl) | (nonce_hi << (32u - pl)), q_hi = nonce_hi >> pl;
      for (uint bb = 0; bb < 4u; bb++) ih_put8(w, pos + bb, q_lo >> (8u * bb)); for (uint bb = 0; bb < 4u; bb++) ih_put8(w, pos + 4u + bb, q_hi >> (8u * bb)); pos += 8u;
      uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d); for (uint q = 0; q < 8u; q++) ys[q] = d[q]; }
    uint ys0[8]; for (uint q = 0; q < 8u; q++) ys0[q] = ys[q];
#endif
    uint phash[16]; (void)phash;
#else
    // the programme: the seed, the 32 words (every lane computes them: uniform), the programme's hash
    uint seed[8];
    { uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u; uint pos = ih_put_tag(w, 0u, ih_tag_forge_program, 24u); pos = ih_put_bytes32(w, pos, k->e); pos = ih_put_bytes32(w, pos, S); uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d); for (uint q = 0; q < 8u; q++) seed[q] = d[q]; }
    uint prog[128];   // words * 4 at the registered 32 words
    { uint x[8]; for (uint q = 0; q < 8u; q++) x[q] = seed[q]; for (uint i = 0; i < words * 4u; i++) { x[i & 7u] = igm_forge_fchain(x[i & 7u], i); prog[i] = x[i & 7u]; } }
    uint phash[16]; ih_b3_chunk(k->p, false, prog, words * 16u, 1u, phash);
#endif
    // the state: lane l's 32 registers are the chain values at n = 32 l + r (chain n mod 8, its (n / 8)-th application); the scratchpad words
    // i = 256 l .. 256 l + 255 at n = 512 + i; each lane advances the eight chains from the init digest to its own positions
    uint R[32];
    {
        uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u; uint pos = ih_put_tag(w, 0u, ih_tag_forge_init, 21u); pos = ih_put_bytes32(w, pos, k->e); pos = ih_put_bytes32(w, pos, S); uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d);
        uint x[8]; for (uint q = 0; q < 8u; q++) x[q] = d[q];
        // the registers: chain c = r & 7 at application index 4 l + r / 8 (zero-based), i.e. the chain advanced (4 l + r / 8 + 1) times with v = 8 m + c
        // (fp_mode: the registers n = NREG l + r; the local words n = 16 NREG + SPSTORE l + j: the scratchpad's, or the window's 16 a lane under mode 6)
        uint xs[8]; for (uint q = 0; q < 8u; q++) xs[q] = x[q];
        for (uint m = 0; m < (FG_NREG / 8u) * lane; m++) for (uint c = 0; c < 8u; c++) xs[c] = igm_forge_fchain(xs[c], 8u * m + c);
        for (uint r = 0; r < 32u; r++) R[r] = 0u;
        IGM_UNROLL_FULL(32) for (uint r = 0; r < FG_NREG; r++) { const uint c = r & 7u, m = (FG_NREG / 8u) * lane + (r >> 3); xs[c] = igm_forge_fchain(xs[c], 8u * m + c); R[r] = ((FG_FPM == 0 && r >= 20u) || (FG_FPM == 2 && r >= 24u)) ? igm_forge_fmask(xs[c]) : xs[c]; }
        // the scratchpad: n = 16 NREG + i, chain c = i & 7, application 2 NREG + i / 8; lane l's words i = SPSTORE l .. : the chains advanced to application 2 NREG + (SPSTORE / 8) l first
        for (uint q = 0; q < 8u; q++) xs[q] = x[q];
        for (uint m = 0; m < 2u * FG_NREG + (FG_SPSTORE / 8u) * lane; m++) for (uint c = 0; c < 8u; c++) xs[c] = igm_forge_fchain(xs[c], 8u * m + c);
        for (uint i = 0; i < FG_SPSTORE; i++) { const uint n = 16u * FG_NREG + FG_SPSTORE * lane + i, c = n & 7u; xs[c] = igm_forge_fchain(xs[c], n); sp[lane * FG_SPSTORE + i] = xs[c]; }
#if IGM_FORGE_OLDCARD
        // version 25: the per-hash scratchpad's words follow the window in the init chain (n = 16 NREG + 16 SPSTORE + j, word j = LE32 of the chain value);
        // lane l fills its own slice (SP8_SLICE words, a multiple of 8, so the chains stand at a whole application): the chains advanced to application (16 NREG + 16 SPSTORE) / 8 + (SP8_SLICE / 8) l first
        for (uint q = 0; q < 8u; q++) xs[q] = x[q];
        for (uint m = 0; m < 2u * FG_NREG + 2u * FG_SPSTORE + (IGM_FORGE_SP8_SLICE / 8u) * lane; m++) for (uint c = 0; c < 8u; c++) xs[c] = igm_forge_fchain(xs[c], 8u * m + c);
        for (uint i = 0; i < IGM_FORGE_SP8_SLICE; i++) { const uint n = 16u * FG_NREG + 16u * FG_SPSTORE + IGM_FORGE_SP8_SLICE * lane + i, c = n & 7u; xs[c] = igm_forge_fchain(xs[c], n); sp8[lane * IGM_FORGE_SP8_SLICE + i] = xs[c]; }
#endif
    }
    IGM_FORGE_BARRIER();
#if IGM_FORGE_TRACE
    if (trace) { if (lane == 0u) { for (uint q = 0; q < 8u; q++) trace[q] = k->e[q]; trace[8] = prog[0]; trace[9] = prog[1]; trace[10] = prog[2]; trace[11] = prog[3];
#if IGM_IH_VERSION == 20
        trace[12] = phash[0];
#endif
        for (uint r = 0; r < 32u; r++) trace[16u + r] = R[r]; for (uint i = 0; i < 4u; i++) { trace[56u + i] = sp[i]; trace[60u + i] = sp[256u + i]; } }
        if (lane == 1u) for (uint r = 0; r < 8u; r++) trace[48u + r] = R[r]; }
#endif
    const uint n_items = k->n_items_mask + 1u;
    uint cls[32]; for (uint q = 0; q < 32u; q++) cls[q] = igm_forge_quota[q];
    uint imm[32], qv[32], himm[32]; uint w3 = 0u; const uint per_word = steps / words;
#if FG_V2
    const uint per_entry = steps / L; (void)per_word;
#endif
#if IGM_FORGE_PREFETCH
    uint hist[IGM_FORGE_PREFETCH + 1];   // "prefetch" K: this lane's r0 at the start of each of the last K + 1 steps; the keyed read's index is the designated lane's r0 as it stood K steps before the consuming step (the init state before step K)
#endif
    for (uint t = 0; t < steps; t++) {
#if IGM_FORGE_PREFETCH
        hist[t % (IGM_FORGE_PREFETCH + 1u)] = R[0];
#endif
        if ((t % fold_every) == 0u) {   // the keyed read: lane L's R[0] names the item; lanes L .. L + 3 fold its 64 words sixteen each
            const uint L = (t / fold_every) & 15u;
#if IGM_FORGE_PREFETCH
            { const uint tp = t >= IGM_FORGE_PREFETCH ? t - IGM_FORGE_PREFETCH : 0u; xl[lane] = hist[tp % (IGM_FORGE_PREFETCH + 1u)]; }
#else
            xl[lane] = R[0];
#endif
            IGM_FORGE_BARRIER(); const uint idx = xl[L] % n_items; IGM_FORGE_BARRIER();
            const uint j0 = ((lane + 16u - L) & 15u);   // this lane's word block: (lane - L) mod 16 in 0..3 takes words 16 j0 .. 16 j0 + 15
            if (j0 < 4u) { IGM_GLOBAL const uint *it = dataset + (ulong)idx * 64u + 16u * j0; IGM_UNROLL_FULL(16) for (uint j = 0; j < 16u; j++) R[j] = igm_forge_fchain(R[j], it[j]); }
#if IGM_FORGE_TRACE
            if (trace && t == 0u) { IGM_FORGE_BARRIER(); if (lane == 0u) { trace[64] = idx; trace[65] = dataset[(ulong)idx * 64u]; trace[66] = dataset[(ulong)idx * 64u + 1u]; trace[67] = k->n_items_mask; trace[68] = xl[L]; trace[69] = dataset[0]; trace[70] = dataset[(ulong)926986u * 64u]; trace[71] = fold_every; for (uint r = 0; r < 32u; r++) trace[72u + r] = R[r]; } }
#endif
        }
#if FG_V2
        if ((t % per_entry) == 0u) {   // a new entry: the shape's words and class order, the hash's address and predicate word
            const uint ei = t / per_entry;
            y[ei & 7u] = igm_forge_fchain(y[ei & 7u], ei); uint si = y[ei & 7u] % words; y[(ei + 4u) & 7u] = igm_forge_fchain(y[(ei + 4u) & 7u], 4096u + ei); const uint hword = y[(ei + 4u) & 7u];
#if IGM_FORGE_PAIR_LOG2
            ys[ei & 7u] = igm_forge_fchain(ys[ei & 7u], ei); si = ys[ei & 7u] % words;
#endif
#if IGM_FORGE_PERIOD_TABLE
            const uint si0 = IGM_FORGE_BCAST0(si);   // the shape index is one across the wave when the wave's attempts share the pair (even job starts, the inactive attempt on the active nonce): then the table loads take the scalar path; otherwise each lane's own index (the vector path, exact either way)
            if (IGM_FORGE_ALL(si == si0)) { w3 = ptab[4u * si0 + 3u]; const uint tb = 4u * words + 96u * si0; IGM_UNROLL_FULL(32) for (uint q = 0; q < 32u; q++) { cls[q] = ptab[tb + q]; imm[q] = ptab[tb + 32u + q]; qv[q] = ptab[tb + 64u + q]; } }
            else { w3 = ptab[4u * si + 3u]; const uint tb = 4u * words + 96u * si; IGM_UNROLL_FULL(32) for (uint q = 0; q < 32u; q++) { cls[q] = ptab[tb + q]; imm[q] = ptab[tb + 32u + q]; qv[q] = ptab[tb + 64u + q]; } }
            IGM_UNROLL_FULL(32) for (uint q = 0; q < 32u; q++) himm[q] = igm_forge_fchain(hword, q);
#else
            const uint w1 = prog[4u * si + 1u], w2 = prog[4u * si + 2u]; w3 = prog[4u * si + 3u];
            IGM_UNROLL_FULL(32) for (uint q = 0; q < 32u; q++) { imm[q] = igm_forge_fchain(w1, q); qv[q] = igm_forge_fchain(w2, q); himm[q] = igm_forge_fchain(hword, q); cls[q] = shcls[32u * si + q]; }
#endif
        }
#else
        if ((t % per_word) == 0u) {   // a new word: the slot immediates, the sub-class bits, the load bits; the drawn order's permutation
            const uint wi = t / per_word; const uint w0 = prog[4u * wi], w1 = prog[4u * wi + 1u], w2 = prog[4u * wi + 2u]; w3 = prog[4u * wi + 3u];
            IGM_UNROLL_FULL(32) for (uint q = 0; q < 32u; q++) { imm[q] = igm_forge_fchain(w1, q); qv[q] = igm_forge_fchain(w2, q); himm[q] = imm[q]; }
#if IGM_FORGE_DRAWN
            for (uint q = 0; q < 32u; q++) cls[q] = igm_forge_quota[q];
            for (uint i = 31u; i > 0u; i--) { const uint j = igm_forge_fchain(w0, i) % (i + 1u); const uint tmp = cls[i]; cls[i] = cls[j]; cls[j] = tmp; }
#else
            (void)w0;
#endif
        }
#endif
#if IGM_FORGE_OLDCARD
        if ((t % 5u) != 4u) {   // THE SCRATCH STEP (version 25): r0..r3 = R[(4t + 0..3) mod 32], the indices by the step's residue mod 8 (constant per case, so R stays in registers)
            uint lo, hi;
            switch (t & 7u) {
                case 0u: IGM_FORGE_SP8_STEP(0u, 1u, 2u, 3u) break;
                case 1u: IGM_FORGE_SP8_STEP(4u, 5u, 6u, 7u) break;
                case 2u: IGM_FORGE_SP8_STEP(8u, 9u, 10u, 11u) break;
                case 3u: IGM_FORGE_SP8_STEP(12u, 13u, 14u, 15u) break;
                case 4u: IGM_FORGE_SP8_STEP(16u, 17u, 18u, 19u) break;
                case 5u: IGM_FORGE_SP8_STEP(20u, 21u, 22u, 23u) break;
                case 6u: IGM_FORGE_SP8_STEP(24u, 25u, 26u, 27u) break;
                default: IGM_FORGE_SP8_STEP(28u, 29u, 30u, 31u) break;
            }
            continue;
        }
#endif
#if FG_WINDOW
        const uint wbase = (t % per_entry) & 15u;   // (e): the window's base, the step within the entry
#endif
        IGM_UNROLL_FULL(32) for (uint kk = 0; kk < 32u; kk++) {
#if FG_FPM == 5
            uint dk = kk & 15u, ak = (kk + 11u) & 15u, bk = (kk + 7u) & 15u;
#elif FG_FPM == 4
            uint dk = kk, ak = (kk + 24u) & 31u, bk = (kk + 16u) & 31u;
#elif FG_FPM == 3 || FG_FPM == 6
            uint dk = kk, ak = (kk + 21u) & 31u, bk = (kk + 9u) & 31u;   // (c) and (e) the ILP wiring (the reference 389945b42: Anvil-R had fallen through to version 20's; the Anvil-R set re-cut)
#else
            uint dk = kk, ak = (7u * kk + 1u) & 31u, bk = (11u * kk + 5u) & 31u;
#endif
            const uint C = cls[kk], im = imm[kk], q = qv[kk];
            uint load = (w3 >> kk) & 1u;
#if FG_FPM == 2
            // the producer-masked wiring: the FP slots on the bank r24..r31 (no scratchpad load), the integer slots on r0..r23; I2F integer in, bank out; F2I bank in, integer out
            if (C == FG_FMA || C == FG_FADDSUB || C == FG_FMUL || C == FG_I2F || C == FG_F2I) { load = 0u; if (C == FG_I2F) { ak = ak % 24u; bk = bk % 24u; dk = 24u + (kk & 7u); } else if (C == FG_F2I) { ak = 24u + (ak & 7u); bk = 24u + (bk & 7u); dk = kk % 24u; } else { ak = 24u + (ak & 7u); bk = 24u + (bk & 7u); dk = 24u + (kk & 7u); } }
            else { ak = ak % 24u; bk = bk % 24u; dk = kk % 24u; }
#endif
            const uint a = R[ak];
            const uint hm = himm[kk];
#if FG_WINDOW
            const uint b = load ? sp[lane * 16u + ((wbase + (im & 15u)) & 15u)] : R[bk];
#else
            const uint b = load ? sp[lane * FG_SPW + (((R[bk] + hm) & FG_SPMASK) >> 2)] : R[bk];
#endif
            uint xv = 0u;
            if (C == FG_XLANE) { xl[lane] = R[ak]; IGM_FORGE_BARRIER(); xv = xl[(lane + 1u + (q % 15u)) & 15u]; IGM_FORGE_BARRIER(); }
            uint st[3]; const uint d = igm_forge_slot(C, a, b, R[dk], im, q, xv, hm, st);
            R[dk] = d;
#if FG_WINDOW
            if (st[0]) sp[lane * 16u + ((wbase + ((im >> 4) & 15u)) & 15u)] = st[2];
#else
            if (st[0]) sp[lane * FG_SPW + st[1]] = st[2];
#endif
        }
#if IGM_FORGE_TRACE
        if (trace && t == 0u && lane == 0u) for (uint r = 0; r < 24u; r++) trace[104u + r] = R[r];
#endif
    }
    IGM_FORGE_BARRIER();
    // the digest by lane 0: tag || E || phash || the 512 registers (lane-major, pulled through the exchange) || the 4096 scratchpad words
    uint M[8]; for (uint q = 0; q < 8u; q++) M[q] = 0u;
#if FG_V2
#if IGM_FORGE_LEAN
    // THE LEAN MIX (the reference 6e182e23a): each lane folds its registers, then its scratchpad or window words, into a 16-byte accumulator (acc[n & 3] = (acc ^ w) + w); the digest over E, the period seed, the sequence bytes and the 16 accumulators
    uint acc[4] = { 0x9E3779B9u ^ lane, 0x85EBCA6Bu, 0xC2B2AE35u, 0x27D4EB2Fu }; uint an = 0u;
    for (uint r = 0; r < FG_NREG; r++) { acc[an & 3u] = (acc[an & 3u] ^ R[r]) + R[r]; an++; }
    for (uint i = 0; i < FG_SPSTORE; i++) { const uint w = sp[lane * FG_SPSTORE + i]; acc[an & 3u] = (acc[an & 3u] ^ w) + w; an++; }
#if IGM_FORGE_OLDCARD
    for (uint i = 0; i < IGM_FORGE_SP8_SLICE; i++) { const uint w = sp8[lane * IGM_FORGE_SP8_SLICE + i]; acc[an & 3u] = (acc[an & 3u] ^ w) + w; an++; }   // version 25: the lane's slice of the scratchpad (the digest binds every byte)
#endif
    igm_forge_b3 h; igm_forge_b3_init(&h, 22u + 32u + 32u + L + 256u);
#else
    igm_forge_b3 h; igm_forge_b3_init(&h, 22u + 32u + 32u + L + 64u * FG_NREG + 64u * FG_SPSTORE);   // LP("IgneumHash/Forge/mix2"): the length byte and the 21 characters; the registers, then the scratchpad (or the window under mode 6)
#endif
    if (lane == 0u) { igm_forge_b3_put8(&h, 21u, M); for (uint q = 0; q < 21u; q++) igm_forge_b3_put8(&h, ih_tag_forge_mix2[q], M); for (uint q = 0; q < 8u; q++) igm_forge_b3_put32(&h, k->e[q], M); for (uint q = 0; q < 8u; q++) igm_forge_b3_put32(&h, pseed[q], M);
#if IGM_FORGE_PAIR_LOG2
        { uint z[8]; for (uint q = 0; q < 8u; q++) z[q] = ys0[q]; for (uint i = 0; i < L; i++) { z[i & 7u] = igm_forge_fchain(z[i & 7u], i); igm_forge_b3_put8(&h, z[i & 7u] % words, M); } }
#else
        { uint z[8]; for (uint q = 0; q < 8u; q++) z[q] = seed[q]; for (uint i = 0; i < L; i++) { z[i & 7u] = igm_forge_fchain(z[i & 7u], i); igm_forge_b3_put8(&h, z[i & 7u] % words, M); z[(i + 4u) & 7u] = igm_forge_fchain(z[(i + 4u) & 7u], 4096u + i); } }
#endif
    }
#else
    igm_forge_b3 h; igm_forge_b3_init(&h, 21u + 32u + 32u + 2048u + 16384u);   // LP("IgneumHash/Forge/mix"): the length byte and the 20 characters (GPU A's reading of 15 Oct 00:2x UK: the digest was over a truncated tag)
    if (lane == 0u) { igm_forge_b3_put8(&h, 20u, M); for (uint q = 0; q < 20u; q++) igm_forge_b3_put8(&h, ih_tag_forge_mix[q], M); for (uint q = 0; q < 8u; q++) igm_forge_b3_put32(&h, k->e[q], M); for (uint q = 0; q < 8u; q++) igm_forge_b3_put32(&h, phash[q], M); }
#endif
#if FG_V2 && IGM_FORGE_LEAN
    for (uint l = 0; l < 16u; l++) { for (uint q = 0; q < 4u; q++) { if (lane == l) xl[0] = acc[q]; IGM_FORGE_BARRIER(); if (lane == 0u) igm_forge_b3_put32(&h, xl[0], M); IGM_FORGE_BARRIER(); } }
#else
    for (uint l = 0; l < 16u; l++) { IGM_UNROLL_FULL(32) for (uint r = 0; r < FG_NREG; r++) { if (lane == l) xl[0] = R[r]; IGM_FORGE_BARRIER(); if (lane == 0u) igm_forge_b3_put32(&h, xl[0], M); IGM_FORGE_BARRIER(); } }
    if (lane == 0u) { for (uint i = 0; i < 16u * FG_SPSTORE; i++) igm_forge_b3_put32(&h, sp[i], M); }
#endif
    for (uint q = 0; q < 8u; q++) mix_out8[q] = M[q];
    uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint q = 0; q < 8u; q++) final8[q] = Fo[q];
}
#endif
