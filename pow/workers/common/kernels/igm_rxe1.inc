// igm_rxe1.inc: RXE1 (docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md sections 6, 7, 8), the shared body for
// Metal and OpenCL, written from the text and nothing else. One nonce = 16 logical lanes; this body
// is one logical lane's view (lane l, K registers in thread memory); the cross-lane values (the
// address fold, the end-of-round neighbour register) come through IGM_BCAST16 over the nonce's
// 16-lane group (SIMD shuffle on Metal, local memory with barriers on OpenCL: the prelude decides).
// The schedule (B blocks x K destinations x R rounds of 4-byte descriptors) is generated on the host
// from X("RXE1/op") and read from a constant buffer. Requires igm_igneumhash.inc before it (the
// BLAKE3 chunk and byte builders). GPU engineer B, 10 October 2026.

// STATUS (main, 10 Oct 2026, 09:55 Mac clock): a research branch behind v1. The body stays here unproven until the
// hash lead's IH 04 vectors exist; the K = 16 result hash spans two BLAKE3 chunks (1079 bytes) and needs the
// chunk-chaining path before K = 16 vectors run; no screening before main's written decision after v1's validation.
// The 16-lane prelude macros (IGM_BCAST16, IGM_XCH16_PARAMS, IGM_ST_SYNC16), the wrappers and the engine paths are
// not written yet.
// The hash lead's IH 04 readings (docs/igneumhash/rxe1/REFERENCE-NOTES.md with his landing), to build on: candidate
// id "rxe1"; the record = digest 32 || dataset_bytes u64 || parents u32 || cache_bytes u64 || R u32 || K u32 || B u32
// || mode u32 || schema u32 (72 bytes, little-endian), LP-framed once as the one field of H("RXE1/params"); the
// document keys dataset_bytes, parents, cache_bytes, rounds, registers, blocks, mode, schema plus chain_id (u64,
// 7310), epoch_blocks (4096), test_only; the dataset-spec digest is a binding constant, never a document key; the
// harness epoch seed = H("RXE1/harness-epoch", [LE64(chain_id), LE64(epoch_number)]) (no pid: one dataset under all
// seven configurations), the node adapter replacing it by the chain's rule; the dataset = the R0A v1 builder with
// E = epoch_seed at the record's sizes, the cache through the light cache; ABI: header_blob = the 32-byte header,
// mix_out = seed, hash_out = out, verify compares mix then out as the ABI's big-endian compare; vectors under
// pow/vectors/rxe1/<m|f|e>-b<B>-r<R>-k<K>-d<mib>-p<parents>-c<cache_mib>/ with a trace class and schedule.json.
// Schema 2 (main's decision for the stage B screening, the red team's finding that opcode 0 is a no-op when
// src1 = src2 under schema 1): the host generator's src2 rule changes and nothing else: r1 = (src1 - d - 1) mod K,
// t = q[1] mod (K - 2), if t >= r1 then t = t + 1, src2 = (d + 1 + t) mod K; "schema": 2 in the document (pids
// differ); schema 1 stays the text verbatim with its own vectors (s2-<config> sets beside them). This body reads
// descriptors and is unchanged by the schema; the host generator (not written yet) carries both rules.

#define RXE1_KMAX 16u
#define RXE1_GMAX 32u
#define IH_PARENT 4u

IGM_INLINE uint rxe1_put_lp(IGM_PRIV uint *w, uint pos, uint len) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, len >> (8u * i)); return pos; }
IGM_INLINE uint rxe1_put_u32le(IGM_PRIV uint *w, uint pos, uint x) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, x >> (8u * i)); return pos; }
IGM_INLINE uint rxe1_put_u64le(IGM_PRIV uint *w, uint pos, uint lo, uint hi) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, lo >> (8u * i)); for (uint i = 0; i < 4; i++) ih_put8(w, pos++, hi >> (8u * i)); return pos; }

// BLAKE3 over up to two chunks (inputs to 2048 bytes): one chunk as ih_b3_chunk; two chunks chained through a
// parent node (the root is the parent compression of the two chaining values, flags PARENT | ROOT).
// one chunk of the tree: chunk index t, its bytes [off, off + len) of the input (len 1 to 1024), its chaining value out
IGM_INLINE void rxe1_b3_chunk_cv(IGM_PRIV const uint *words, uint nbytes, uint t, uint off, uint len, IGM_PRIV uint *cv8) {
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    uint nblocks = (len + 63u) / 64u; if (nblocks == 0u) nblocks = 1u;
    for (uint b = 0; b < nblocks; b++) {
        uint m[16], o[16];
        for (uint i = 0; i < 16; i++) { uint byte = off + 64u * b + 4u * i; m[i] = (byte < nbytes) ? words[(off >> 2u) + 16u * b + i] : 0u; }
        uint blen = (b + 1u == nblocks) ? (len - 64u * b) : 64u;
        uint flags = (b == 0u ? IH_CHUNK_START : 0u) | (b + 1u == nblocks ? IH_CHUNK_END : 0u);
        ih_b3_compress(cv, m, t, blen, flags, o);
        for (uint i = 0; i < 8; i++) cv[i] = o[i];
    }
    for (uint i = 0; i < 8; i++) cv8[i] = cv[i];
}
IGM_INLINE void rxe1_b3_parent(IGM_PRIV const uint *l8, IGM_PRIV const uint *r8, uint flags, IGM_PRIV uint *out16) {
    uint m[16]; for (uint i = 0; i < 8; i++) { m[i] = l8[i]; m[8u + i] = r8[i]; }
    uint iv[8]; for (uint i = 0; i < 8; i++) iv[i] = igm_b3_iv[i];
    ih_b3_compress(iv, m, 0u, 64u, IH_PARENT | flags, out16);
}
IGM_INLINE void rxe1_b3_hash(IGM_PRIV const uint *words, uint nbytes, IGM_PRIV uint *out16) {
    if (nbytes <= 1024u) { ih_b3_chunk(words, false, words, nbytes, 1u, out16); return; }
    if (nbytes > 2048u) {   // three chunks (to 3072 bytes: the G = 32, K = 16 result input of 2103 bytes): parent(c0, c1), then the root over it and c2
        uint c0[8], c1[8], c2[8], p01[16];
        rxe1_b3_chunk_cv(words, nbytes, 0u, 0u, 1024u, c0);
        rxe1_b3_chunk_cv(words, nbytes, 1u, 1024u, 1024u, c1);
        rxe1_b3_chunk_cv(words, nbytes, 2u, 2048u, nbytes - 2048u, c2);
        rxe1_b3_parent(c0, c1, 0u, p01);
        uint p8[8]; for (uint i = 0; i < 8; i++) p8[i] = p01[i];
        rxe1_b3_parent(p8, c2, IH_ROOT, out16);
        return;
    }
    uint cv0[8], cv1[8];
    {   // chunk 0: 1024 bytes, 16 blocks, counter 0
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        for (uint b = 0; b < 16; b++) {
            uint m[16], o[16]; for (uint i = 0; i < 16; i++) m[i] = words[16u * b + i];
            uint flags = (b == 0u ? IH_CHUNK_START : 0u) | (b == 15u ? IH_CHUNK_END : 0u);
            ih_b3_compress(cv, m, 0u, 64u, flags, o);
            for (uint i = 0; i < 8; i++) cv[i] = o[i];
        }
        for (uint i = 0; i < 8; i++) cv0[i] = cv[i];
    }
    {   // chunk 1: the rest, counter 1
        uint rest = nbytes - 1024u;
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint nblocks = (rest + 63u) / 64u; if (nblocks == 0u) nblocks = 1u;
        for (uint b = 0; b < nblocks; b++) {
            uint m[16], o[16];
            for (uint i = 0; i < 16; i++) { uint byte = 1024u + 64u * b + 4u * i; m[i] = (byte < nbytes) ? words[256u + 16u * b + i] : 0u; }
            uint blen = (b + 1u == nblocks) ? (rest - 64u * b) : 64u;
            uint flags = (b == 0u ? IH_CHUNK_START : 0u) | (b + 1u == nblocks ? IH_CHUNK_END : 0u);
            ih_b3_compress(cv, m, 1u, blen, flags, o);
            for (uint i = 0; i < 8; i++) cv[i] = o[i];
        }
        for (uint i = 0; i < 8; i++) cv1[i] = cv[i];
    }
    uint m[16]; for (uint i = 0; i < 8; i++) { m[i] = cv0[i]; m[8u + i] = cv1[i]; }
    uint iv[8]; for (uint i = 0; i < 8; i++) iv[i] = igm_b3_iv[i];
    ih_b3_compress(iv, m, 0u, 64u, IH_PARENT | IH_ROOT, out16);
}

// the per-variant constants: chain id (LE64 as lo, hi), pid, the epoch number, E, the sizes, and the schema 3 shape
struct rxe1_consts { uint cid_lo, cid_hi; uint pid[8]; uint ep_lo, ep_hi; uint e[8]; uint n_items_mask; uint rounds; uint regs; uint blocks; uint group; uint route; uint chunks; uint payload; uint n_regions_mask; uint wpl; uint schema; uint mode; };
typedef struct rxe1_consts rxe1_consts;

IGM_INLINE uint rxe1_rotl(uint x, uint n) { n &= 31u; return n ? ((x << n) | (x >> (32u - n))) : x; }
IGM_INLINE uint rxe1_op(uint op, uint x, uint y, uint z, uint rot) {
    if (op == 0u) return x + rxe1_rotl(y ^ z, rot);
    if (op == 1u) return x ^ rxe1_rotl(y + z, rot);
    if (op == 2u) return x + y * (z | 1u);
    return rxe1_rotl(x ^ y, rot) + z;
}

IGM_CONST uchar rxe1_tag_nonce[10] = {'R','X','E','1','/','n','o','n','c','e'};
IGM_CONST uchar rxe1_tag_nonce_program[18] = {'R','X','E','1','/','n','o','n','c','e','-','p','r','o','g','r','a','m'};
IGM_CONST uchar rxe1_tag_op[7] = {'R','X','E','1','/','o','p'};
IGM_CONST uchar rxe1_tag_state[10] = {'R','X','E','1','/','s','t','a','t','e'};
IGM_CONST uchar rxe1_tag_result[11] = {'R','X','E','1','/','r','e','s','u','l','t'};

// One logical lane of one nonce (lane 0..G-1; igm_gsz = G is the exchange's group width). sched: the instruction
// descriptors src1 | src2 << 8 | rot << 16 | op << 24 at (j * B + b) * K + d; rsched: route 2's descriptors
// off | src << 8 | rot << 16 | op << 24 at j * K + k (unused under the other routes). st_area: G * K words per nonce.
IGM_INLINE void igm_rxe1_lane(IGM_PRIV const rxe1_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                          IGM_GLOBAL const uint *dataset, IGM_CONST const uint *sched, IGM_CONST const uint *rsched, IGM_GLOBAL uint *st_area,
                          uint lane, IGM_XCH16_PARAMS, IGM_PRIV uint *seed8_out, IGM_PRIV uint *out8) {
    const uint K = k->regs, G = k->group, igm_gsz = G;
    (void)igm_gsz;
    uint seed[16];
    {
        uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
        uint pos = rxe1_put_lp(w, 0u, 10u); for (uint i = 0; i < 10; i++) ih_put8(w, pos++, rxe1_tag_nonce[i]);
        pos = rxe1_put_lp(w, pos, 8u); pos = rxe1_put_u64le(w, pos, k->cid_lo, k->cid_hi);
        pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, k->pid);
        pos = rxe1_put_lp(w, pos, 8u); pos = rxe1_put_u64le(w, pos, k->ep_lo, k->ep_hi);
        pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, k->e);
        pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, hdr8);
        pos = rxe1_put_lp(w, pos, 8u); pos = rxe1_put_u64le(w, pos, nonce_lo, nonce_hi);
        ih_b3_chunk(k->pid, false, w, pos, 1u, seed);
    }
    for (uint q = 0; q < 8; q++) seed8_out[q] = seed[q];
    // S = X("RXE1/state", [seed], 4 G K), lane-major: lane l's K words are words [l K, l K + K) of the XOF
    uint S[RXE1_KMAX];
    {
        uint w[16]; for (uint j = 0; j < 16; j++) w[j] = 0u;
        uint pos = rxe1_put_lp(w, 0u, 10u); for (uint i = 0; i < 10; i++) ih_put8(w, pos++, rxe1_tag_state[i]);
        pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, seed);
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16]; for (uint i = 0; i < 16; i++) m[i] = (4u * i < pos) ? w[i] : 0u;
        uint first = (lane * K) >> 4u, last = ((lane + 1u) * K - 1u) >> 4u;
        for (uint q = 0; q < RXE1_KMAX; q++) S[q] = 0u;
        for (uint t = first; t <= last; t++) {
            uint o[16]; ih_b3_compress(cv, m, t, pos, IH_CHUNK_START | IH_CHUNK_END | IH_ROOT, o);
            for (uint q = 0; q < 16; q++) { uint wi = t * 16u + q; if (wi >= lane * K && wi < (lane + 1u) * K) S[wi - lane * K] = o[q]; }
        }
    }
    const uint C = k->chunks, cwords = (k->payload / C) / 4u, WPL = k->wpl;
    // mode P2 (section 5): the programme seed of this nonce, ps = H("RXE1/nonce-program", [seed]); the descriptors are drawn
    // per round in the loop (lane d < K draws descriptor d of the block and every lane takes it through the exchange)
    uint ps[16];
    if (k->mode == 3u) {
        uint w[32]; for (uint q = 0; q < 32; q++) w[q] = 0u;
        uint pos = rxe1_put_lp(w, 0u, 18u); for (uint i = 0; i < 18; i++) ih_put8(w, pos++, rxe1_tag_nonce_program[i]);
        pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, seed);
        ih_b3_chunk(k->pid, false, w, pos, 1u, ps);
    }
#ifdef IGM_RX_PROBE
    // The lane microbenchmarks of Mosaic gate 1 (DIAGNOSTIC ONLY, never a cell: the hash is wrong on purpose): the loop at the
    // lane's shape (R, B, K, G) with one resource's work alone, the memory step, the blocks and the exchange otherwise removed,
    // so the rate is that resource's ceiling in the lane's own units (nonce loops per second at this shape).
    //   1 probe-alu:     the integer ALU issue: the instruction blocks under constant descriptors (the lane's op mix), no hashing
    //   2 probe-shuffle: the register file and the cross-lane shuffle network: route 3's state-dependent exchange alone
    //   3 probe-chain:   the dependent-issue latency: K x B dependent operations in one serial chain per lane
    //   4 probe-decode:  the per-nonce programme: the mode P2 descriptor draw and its broadcast, applied, nothing else
    for (uint j = 0; j < k->rounds; j++) {
#if IGM_RX_PROBE == 1
        for (uint b = 0; b < k->blocks; b++) {
            uint T[RXE1_KMAX]; for (uint q = 0; q < K; q++) T[q] = S[q];
            for (uint d = 0; d < K; d++) { uint x = ((d + 1u) % K) | (((d + 2u) % K) << 8) | ((1u + (j % 31u)) << 16) | ((d & 3u) << 24); S[d] = rxe1_op(x >> 24, T[d], T[x & 255u], T[(x >> 8) & 255u], (x >> 16) & 255u); }
        }
#elif IGM_RX_PROBE == 2
        {
            uint T[RXE1_KMAX]; for (uint q = 0; q < K; q++) T[q] = S[q];
            for (uint q = 0; q < K; q++) {
                uint off = 1u + ((T[(q + 1u) % K] >> 16) % (G - 1u));
                uint nv = IGM_BCAST16(T[(q + 1u) % K], (lane + off) % G);
                S[q] = T[q] + rxe1_rotl(nv, 7u);
            }
        }
#elif IGM_RX_PROBE == 5
        // the primitive escape search (main's order of 22:4x UK, the auditor's ten-hour plan): deterministic FP16 matrix work on
        // the SIMD-group matrix unit, Metal only: per block K multiply-accumulates of 8 x 8 x 8 halves (1024 FLOP each) seeded
        // from the state, the result folded back into the state; the rate is nonce loops per second at the lane's shape, so
        // FLOP/s = rate x rounds x blocks x K x 1024 (the native ceiling is not documented by Apple for this part: absent)
#ifndef IGM_SGBCAST
#error "probe-fp16mat is Metal only (simdgroup_matrix)"
#endif
        for (uint b = 0; b < k->blocks; b++) {
            simdgroup_half8x8 ma = simdgroup_half8x8((half)((S[0] & 255u) + 1u) * (half)0.001h);
            simdgroup_half8x8 mb = simdgroup_half8x8((half)((S[1] & 255u) + 1u) * (half)0.001h);
            simdgroup_half8x8 mc = simdgroup_half8x8((half)0.0h);
            for (uint d = 0; d < K; d++) simdgroup_multiply_accumulate(mc, ma, mb, mc);
            const auto e = mc.thread_elements();   // the lane's elements of the result (a vec<half, 2> or the whole 8 x 8 on this SDK: the first two taken either way)
            S[j % K] ^= as_type<uint>((float)e[0]) + (uint)((float)e[1] * 1024.0f);
        }
#elif IGM_RX_PROBE == 6
        // the packed-INT8 dot (no INT8 matrix unit on Apple GPUs: a scalar byte dot, four products per word, labelled so):
        // per block K words of four byte products accumulated; INT8 MAC/s = rate x rounds x blocks x K x 4
        for (uint b = 0; b < k->blocks; b++) {
            uint acc = 0u;
            for (uint d = 0; d < K; d++) {
                const uint x = S[d], y = S[(d + 1u) % K];
                acc += (x & 255u) * (y & 255u) + ((x >> 8) & 255u) * ((y >> 8) & 255u) + ((x >> 16) & 255u) * ((y >> 16) & 255u) + (x >> 24) * (y >> 24);
            }
            S[j % K] += acc;
        }
#elif IGM_RX_PROBE == 7
        // intround at the CUDA probe's shape (docs/ladder/PRIM-PROBE.md): 4 independent chains x 64 passes per round of
        // x = rotl(x * 0x9E3779B1, 13) ^ c, 64 rounds per nonce loop: 16,384 multiply-rotate-xor steps per loop
        {
            uint x0 = S[0], x1 = S[1], x2 = S[2], x3 = S[3];
            for (uint d = 0; d < 64u; d++) {
                x0 = rxe1_rotl(x0 * 0x9E3779B1u, 13u) ^ S[4]; x1 = rxe1_rotl(x1 * 0x9E3779B1u, 13u) ^ S[5];
                x2 = rxe1_rotl(x2 * 0x9E3779B1u, 13u) ^ S[6]; x3 = rxe1_rotl(x3 * 0x9E3779B1u, 13u) ^ S[7];
            }
            S[0] += x0; S[1] += x1; S[2] += x2; S[3] += x3; S[j % K] ^= x0 ^ x1 ^ x2 ^ x3;
        }
#elif IGM_RX_PROBE == 8
        // shuffle at the CUDA probe's shape: a 5-stage butterfly over 8 words per lane as one 32-byte permutation per round
        // (the xor partners 1, 2, 4, 8, 16 across the 32-lane SIMD group; Metal only)
#ifndef IGM_SGBCAST
#error "probe-butterfly is Metal only"
#endif
        for (uint st = 0; st < 5u; st++) {
            const ushort m = (ushort)(1u << st);
            for (uint q = 0; q < 8u; q++) { const uint v = simd_shuffle_xor(S[q], m); S[q] = S[q] + rxe1_rotl(v, 7u); }
        }
#elif IGM_RX_PROBE == 9
        // byteperm at the CUDA probe's shape: 8 byte-permutes per 32-byte permutation per round, the selectors data-dependent
        // (each output byte picks one of the eight bytes of a word pair by a nibble of the selector word)
        for (uint q = 0; q < 8u; q++) {
            const uint x = S[q], y = S[(q + 2u) % K], sel = S[(q + 1u) % K];
            uint o = 0u;
            for (uint b = 0; b < 4u; b++) { const uint n = (sel >> (4u * b)) & 15u; const uint src = (n & 4u) ? y : x; o |= ((src >> (8u * (n & 3u))) & 255u) << (8u * b); }
            S[q] = o + x;
        }
#elif IGM_RX_PROBE == 3
        for (uint b = 0; b < k->blocks; b++) {
            uint acc = S[j % K];
            for (uint d = 0; d < K; d++) { acc = rxe1_op(d & 3u, acc, S[d], S[(d + 1u) % K], 1u + ((j + d) % 31u)); S[d] = acc; }
        }
#else
        for (uint b = 0; b < k->blocks; b++) {
            uint T[RXE1_KMAX]; for (uint q = 0; q < K; q++) T[q] = S[q];
            uint mine = 0u;
            if (lane < K) {
                uint w[32]; for (uint q = 0; q < 32; q++) w[q] = 0u;
                uint pos = rxe1_put_lp(w, 0u, 7u); for (uint i = 0; i < 7; i++) ih_put8(w, pos++, rxe1_tag_op[i]);
                pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, seed);
                pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, j);
                pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, b);
                pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, lane);
                uint h[16]; ih_b3_chunk(k->pid, false, w, pos, 1u, h);
                uint h0 = h[0] & 255u, h1 = (h[0] >> 8) & 255u, h2 = (h[0] >> 16) & 255u, h3 = (h[0] >> 24) & 255u;
                uint d = lane;
                uint src1 = (d + 1u + h0 % (K - 1u)) % K;
                uint r1 = (src1 + K - d - 1u) % K; uint t = h1 % (K - 2u); if (t >= r1) t++; uint src2 = (d + 1u + t) % K;
                uint rot = 1u + h2 % 31u, op = (d + h3 % 4u) % 4u;
                mine = src1 | (src2 << 8) | (rot << 16) | (op << 24);
            }
            for (uint d = 0; d < K; d++) { uint x = IGM_BCAST16(mine, d); S[d] = rxe1_op(x >> 24, T[d], T[x & 255u], T[(x >> 8) & 255u], (x >> 16) & 255u); }
        }
#endif
    }
    if (false)
#endif
    for (uint j = 0; j < k->rounds; j++) {
        // the memory step (section 2): C chunk addresses, each folding register (j + c) mod K of every lane
        uint region[4], afold[4];
        for (uint c = 0; c < C; c++) {
            uint a = (j * 0x9E3779B9u) ^ (c * 0x7F4A7C15u);
            uint mine = S[(j + c) % K];
            for (uint l = 0; l < G; l++) { uint v = IGM_BCAST16(mine, l); a ^= rxe1_rotl(v, l); }
            a = (a ^ (a >> 16)) * 0x85EBCA6Bu;
            a = (a ^ (a >> 13)) * 0xC2B2AE35u;
            afold[c] = a;   // section 11's a_c: the fold after the two multiply-shift steps, before the region's final xor-shift (the reference, rxe1.cpp)
            region[c] = (a ^ (a >> 16)) & k->n_regions_mask;
        }
        // lane l takes words [WPL l, WPL l + WPL) of chunk[0] || ... || chunk[C-1]; under schema 5 (section 11) the word is a
        // register read keyed by the chunk's fold, w[q] = S[l][(j + m + K/2) mod K] ^ a_c, every word from the snapshot before
        // the injection (the folds and the words read the same snapshot)
        if (k->schema == 5u) {
            uint wv[8];
            for (uint m = 0; m < WPL; m++) { uint q = WPL * lane + m, c = q / cwords; wv[m] = S[(j + m + (K >> 1u)) % K] ^ afold[c]; }
            for (uint m = 0; m < WPL; m++) { if ((m & 1u) == 0u) S[(j + m) % K] ^= wv[m]; else S[(j + m) % K] += wv[m]; }
        } else
        for (uint m = 0; m < WPL; m++) {
            uint q = WPL * lane + m, c = q / cwords, wi = q - c * cwords;
            uint v = dataset[(ulong)region[c] * (ulong)cwords + wi];
            if ((m & 1u) == 0u) S[(j + m) % K] ^= v; else S[(j + m) % K] += v;
        }
        // the instruction blocks (section 8 of v0.1): lane-local
        for (uint b = 0; b < k->blocks; b++) {
            uint T[RXE1_KMAX]; for (uint q = 0; q < K; q++) T[q] = S[q];
            if (k->mode == 3u) {
                // the per-nonce programme: lane d (d < K) draws descriptor d of block b of round j, q = X("RXE1/op", [ps, LE32 j, LE32 b, LE32 d], 4),
                // the schema 2 src2 rule; every lane takes descriptor d through the exchange and applies it to its own registers
                uint mine = 0u;
                if (lane < K) {
                    uint w[32]; for (uint q = 0; q < 32; q++) w[q] = 0u;
                    uint pos = rxe1_put_lp(w, 0u, 7u); for (uint i = 0; i < 7; i++) ih_put8(w, pos++, rxe1_tag_op[i]);
                    pos = rxe1_put_lp(w, pos, 32u); pos = ih_put_bytes32(w, pos, ps);
                    pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, j);
                    pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, b);
                    pos = rxe1_put_lp(w, pos, 4u); pos = rxe1_put_u32le(w, pos, lane);
                    uint h[16]; ih_b3_chunk(k->pid, false, w, pos, 1u, h);
                    uint h0 = h[0] & 255u, h1 = (h[0] >> 8) & 255u, h2 = (h[0] >> 16) & 255u, h3 = (h[0] >> 24) & 255u;
                    uint d = lane;
                    uint src1 = (d + 1u + h0 % (K - 1u)) % K;
                    uint r1 = (src1 + K - d - 1u) % K; uint t = h1 % (K - 2u); if (t >= r1) t++; uint src2 = (d + 1u + t) % K;
                    uint rot = 1u + h2 % 31u, op = (d + h3 % 4u) % 4u;
                    mine = src1 | (src2 << 8) | (rot << 16) | (op << 24);
                }
                for (uint d = 0; d < K; d++) { uint x = IGM_BCAST16(mine, d); S[d] = rxe1_op(x >> 24, T[d], T[x & 255u], T[(x >> 8) & 255u], (x >> 16) & 255u); }
            } else {
            IGM_CONST const uint *desc = sched + ((ulong)(j * k->blocks + b)) * K;
            for (uint d = 0; d < K; d++) { uint x = desc[d]; S[d] = rxe1_op(x >> 24, T[d], T[x & 255u], T[(x >> 8) & 255u], (x >> 16) & 255u); }
            }
        }
        // the exchange step (section 3) by route, from one snapshot T, every lane present
        if (k->route != 0u) {
            uint T[RXE1_KMAX]; for (uint q = 0; q < K; q++) T[q] = S[q];
            if (k->route == 1u) {
                uint nl = (lane + 1u) % G;
                for (uint q = 0; q < K; q++) { uint nv = IGM_BCAST16(T[(q + 1u) % K], nl); S[q] = T[q] + rxe1_rotl(nv, 7u); }
            } else if (k->route == 2u) {
                for (uint q = 0; q < K; q++) {
                    uint r = rsched[j * K + q];
                    uint off = r & 255u, src = (r >> 8) & 255u, rot = (r >> 16) & 255u, op = (r >> 24) & 1u;
                    uint v = rxe1_rotl(IGM_BCAST16(T[src], (lane + off) % G), rot);
                    S[q] = op == 0u ? T[q] + v : ((T[q] ^ mulhi(T[q] | 1u, v)) + v);   // the text at 4b43dcf: the xor first, the add last
                }
            } else {
                for (uint q = 0; q < K; q++) {
                    uint off = 1u + ((T[(q + 1u) % K] >> 16) % (G - 1u));
                    uint nv = IGM_BCAST16(T[(q + 1u) % K], (lane + off) % G);
                    S[q] = T[q] + rxe1_rotl(nv, 7u);
                }
            }
        }
    }
    for (uint q = 0; q < K; q++) st_area[lane * K + q] = S[q];
    IGM_ST_SYNC16();
    if (lane == 0u) {
        // out = H("RXE1/result", [seed, LE32_lane_major(S)]): 55 + 4 G K bytes (to 2103 for G = 32, K = 16: two chunks and a parent)
        uint mw[544]; for (uint j = 0; j < 544; j++) mw[j] = 0u;
        uint pos = rxe1_put_lp(mw, 0u, 11u); for (uint i = 0; i < 11; i++) ih_put8(mw, pos++, rxe1_tag_result[i]);
        pos = rxe1_put_lp(mw, pos, 32u); pos = ih_put_bytes32(mw, pos, seed);
        pos = rxe1_put_lp(mw, pos, 4u * G * K);
        for (uint q = 0; q < G * K; q++) { uint v = st_area[q]; for (uint b = 0; b < 4; b++) ih_put8(mw, pos++, v >> (8u * b)); }
        uint O[16]; rxe1_b3_hash(mw, pos, O);
        for (uint q = 0; q < 8; q++) out8[q] = O[q];
    }
}
