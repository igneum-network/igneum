// igm_igneumhash.inc: IgneumHash-R0A versions 0 and 1, the shared body for Metal and OpenCL,
// written from docs/igneumhash/SPEC-R0A-v0.md revision 0.4 (sections 2, 4, 5, 7) and nothing
// else. Version 1: dataset items by the parent walk over the epoch cache (5.3), no scratch when
// SCRATCH_BYTES is 0 (the fill, the scratch step and the fold are absent, the state is the 512
// register bytes). Eight lanes per nonce, one SIMD group's eight consecutive threads; lane l holds
// R[l][16] in registers and its scratch slice SL[l] in device memory (the placement of
// the slices is an implementation choice the spec allows, section 4; threadgroup memory
// for the 16 KiB variant is a later measured variant). Requires igm_fishhash.inc before it
// (igm_b3_g, igm_b3_iv, igm_b3_perm) and the prelude's IGM_BCAST / IGM_XCH_PARAMS.
// GPU engineer B, 10 October 2026.

#define IH_CHUNK_START 1u
#define IH_CHUNK_END 2u
#define IH_PARENT 4u
#define IH_ROOT 8u
#define IH_KEYED 16u
#ifdef IGM_KX_NOLOOP
#define IGM_KX_LOOP_ON false
#else
#define IGM_KX_LOOP_ON true
#endif
#ifdef IGM_KX_NOINJECT
#define IGM_KX_INJECT false
#else
#define IGM_KX_INJECT true
#endif
// The share word's selection and its injection, in the forms the M5 Max bisect names (12 Oct 2026): the default is the select chain
// and a guarded XOR after the load; kx-inj-idx selects by a dynamic index into the share array; kx-inj-opnd folds the XOR into the
// loaded word as a conditional operand (the fault followed the injection site with a zero share, so the form, not the value).
#ifdef IGM_KX_INJ_IDX
#define IGM_KX_SELECT(dst, arr, ln) dst = (arr)[(ln) & 7u]
#else
#define IGM_KX_SELECT(dst, arr, ln) dst = (arr)[0]; for (uint q = 1; q < 8; q++) dst = ((ln) == q) ? (arr)[q] : dst
#endif
#if defined(IGM_KX_INJ_OPND)
#define IGM_KX_INJECT_INTO(w, sgi, ks) w = (w) ^ ((IGM_KX_INJECT && (sgi) == 0u) ? (ks) : 0u)
#elif defined(IGM_KX_INJ_MASK)
// branchless: the share word under an arithmetic mask that is all ones at sg 0 and zero elsewhere (no select, no branch)
#define IGM_KX_INJECT_INTO(w, sgi, ks) w = (w) ^ (IGM_KX_INJECT ? ((ks) & (0u - (uint)((sgi) == 0u))) : 0u)
#else
#define IGM_KX_INJECT_INTO(w, sgi, ks) if (IGM_KX_INJECT && (sgi) == 0u) (w) ^= (ks)
#endif
// kx-inj-vol: the selected share word held in a volatile register, so the compiler cannot fold its use into the loaded word's chain
#ifdef IGM_KX_INJ_VOL
#define IGM_KX_KSW_DECL volatile uint ksw = 0u
#else
#define IGM_KX_KSW_DECL uint ksw = 0u
#endif
#ifndef IGM_IH_KEYED_S
#define IGM_IH_KEYED_S 0u   // version 8 bit 10: the bond-keyed rounds S (the host compiles the document's keyed_rounds in; 0 = no keyed round)
#endif
#define IH_MUL 0x9E3779B1u
// the state's registers per lane (SPEC 0.8, the wider state, reference 573303af): 16, or version 8's REGISTERS when 32 or 64; the host
// defines IGM_IH_RW for such a document (the OpenCL and Metal hosts the same define), and every expression below reduces to the
// 16-register text when it is absent: the init draws RW / 16 XOF blocks per lane, the absorb maps word j to register (j div 8) mod RW,
// the cell runs over RW / 4 quads, the permutation is (5 k + 1) mod RW, the block runs over all RW registers, the digest covers RW
// words per lane; the scratch step, the first address and the exchange read registers 0 to 15 as before
#ifndef IGM_IH_RW
#define IGM_IH_RW 16u
#endif
#define IH_RWM (IGM_IH_RW - 1u)
#define IH_ST_WORDS (8u * IGM_IH_RW + 32u)   // the nonce's state area: RW words per lane and the four fold words per lane (160 at 16)
// the programme word W[wi] of the round's item (version 8, draw revision 2) from the lane that holds it: the generic absorb keeps
// word 8 sg + lane as wraw8[sg]; the form-1 loads keep words 32 t + 4 lane + c as wraw8[4 t + c] (steps 0 and 1), so the word's
// lane and slot differ by form (the form-1 gate cell of 11 Oct 01:18 UK mismatched because the form-1 branch skipped step 1b)
#if defined(IGM_IH_V4_FORM) && IGM_IH_V4_FORM >= 1 && IGM_IH_ITEM_WORDS >= 32u
#define IH_PW(wi) IGM_BCAST(wraw8[(((wi) >> 5) << 2) | ((wi) & 3u)], ((wi) >> 2) & 7u)
#else
#define IH_PW(wi) IGM_BCAST(wraw8[((wi) >> 3) & 7u], (wi) & 7u)
#endif
// IGM_IH_TRACE (compile-time, diagnostics only): the lane function takes a device pointer and
// nonce 0 of the batch writes S, E, P, the registers after 4.1 and 4.2, rounds 0 and 1 with their
// item index, M, F and the fold, in the layout the engine prints (ih trace line).
// IGM_IH_TRACE_FULL (with IGM_IH_TRACE): every round at 704 + r (130 + item_words): [idx, h, item[item_words], regs[8][16]]
// (the reference's trace record, strata/RIPTIDE.md section 2); the words before 704 keep the layout above.
// IGM_IH_VERSION (the host defines it from the document at build time): the round's version branch is chosen when the
// kernel is compiled, so the other version's code is absent. Found 10 Oct 2026 16:3x UK: with the version 4 branch present
// (its lane exchange is a pair of work-group barriers on the local-exchange path) pocl's work-item compiler broke the
// version 1 round on the same kernel (every vector wrong); a compiled-out branch cannot. A runtime version check remains
// for a host that defines nothing (the lab builds).
// IGM_IH_ABL (Orchard workstream 3, main's order of 10 Oct 2026 18:1x UK; DIAGNOSTIC ONLY, the hash wrong by design, never a
// cell): one bit removes one piece of the restored work from the full version 4 round so the gap between form B (119 GB/s on
// the M6) and the relation (51.6) is attributed to its component. Bit 1: the item digest (the reduction tree) replaced by a
// one-shuffle fold; 2: the cell 4.4 and the permutation skipped (h consumed into R[0]); 4: the lane exchange skipped;
// 8: the next address from h alone, the register chain cut; 16: the per-nonce tail (the lane-0 BLAKE3 of the state)
// replaced by a fold of the state area. All five bits (31) is the matched-resource dependent ceiling: the builder removed with
// the loads, the sixteen-register absorb and the state-area writes kept, so the register, threadgroup-memory and occupancy
// footprint is the relation's. The loads, the absorb and the dependent chain through memory are never removed.
#ifndef IGM_IH_ABL
#define IGM_IH_ABL 0
#endif
#ifdef IGM_IH_DIAG_SG_TG
#define IGM_IH_TGM_PARAM , IGM_LOCAL uint *tgm
#define IGM_IH_TGM_ARG , tgm
#else
#define IGM_IH_TGM_PARAM
#define IGM_IH_TGM_ARG
#endif
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
#define IGM_IH_R0E_PARAM , IGM_LOCAL uint *r0e_tg, IGM_LOCAL uint *r12_tbl, IGM_GLOBAL const uint *r12_shared   // versions 12 to 14: the octet's table of IGM_IH_TKW words in local memory, the period's shared table (IGM_IH_SHW words, global; a stand-in when 0)
#define IGM_IH_R0E_ARG , r0e_tg, r12_tbl, r12_shared
#ifndef IGM_IH_TKW
#define IGM_IH_TKW 4096u
#endif
#ifndef IGM_IH_AW
#define IGM_IH_AW 4u
#endif
#ifndef IGM_IH_SHW
#define IGM_IH_SHW 0u
#endif
#elif IGM_IH_VERSION == 10 || IGM_IH_VERSION == 11
#define IGM_IH_R0E_PARAM , IGM_LOCAL uint *r0e_tg   // version 10 and 11: the octet's 128-word tile in local memory
#define IGM_IH_R0E_ARG , r0e_tg
#else
#define IGM_IH_R0E_PARAM
#define IGM_IH_R0E_ARG
#endif
#ifdef IGM_IH_VERSION
#define IH_V4 (IGM_IH_VERSION >= 4)
#define IH_V6 (IGM_IH_VERSION == 6 || IGM_IH_VERSION >= 11)   // version 11 (R0E's T1): version 6's keyed leaves and finalised root   // version 6 (SPEC 0.6, the hash lead, 10 Oct 2026): each leaf keyed by the register its word is absorbed into, as it stands before the absorb; the root finalised; version 6's alone (the reference keys on version == 6)
#define IH_V8 (IGM_IH_VERSION >= 8)   // version 9 (R0D) is version 8's round with the period programme after the absorb
#define IH_V9 (IGM_IH_VERSION == 9)
#else
#define IH_V4 (k->version >= 4u)
#define IH_V6 (k->version == 6u || k->version >= 11u)
#define IH_V8 (k->version >= 8u)
#define IH_V9 (k->version == 9u)
#endif
// Version 8 (SPEC 0.8, the blended relation): version 4's relation with the RXE1 block schedule between the absorb and the
// tree (step 1b, below), its options as the document's FEATURES bits: 0 the per-nonce scratchpad inside the block, 1 the
// predicated select, 2 or 3 the chains, 4 the binary32 form of opcode 2 (the operands masked to positive normals, the product
// and the sum rounded separately to nearest-even: contraction off, no fast math; IGM_U2F / IGM_F2U from the prelude).
// Ported from pow/ref/src/igneumhash.cpp (v8_program_seed, v8_generate, v8op, v8fop, step 1b). The Orchard lane, 23:4x UK.
IGM_CONST uchar ih_v8_tag_nonce_program[18] = {'R','X','E','1','/','n','o','n','c','e','-','p','r','o','g','r','a','m'};
IGM_CONST uchar ih_v8_tag_op[7] = {'R','X','E','1','/','o','p'};
IGM_INLINE uint ih_v8_rotl(uint x, uint n) { n &= 31u; return n ? ((x << n) | (x >> (32u - n))) : x; }
IGM_INLINE uint ih_v8_op(uint op, uint x, uint y, uint z, uint rot) {
    if (op == 0u) return x + ih_v8_rotl(y ^ z, rot);
    if (op == 1u) return x ^ ih_v8_rotl(y + z, rot);
    if (op == 2u) return x + y * (z | 1u);
    return ih_v8_rotl(x ^ y, rot) + z;
}
IGM_INLINE uint ih_v8_fop(uint x, uint y, uint z) {
    const uint yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u, xb = (x & 0x3FFFFFFFu) | 0x20000000u;
    const float yf = IGM_U2F(yb), zf = IGM_U2F(zb), xf = IGM_U2F(xb);
    const float prod = yf * zf;   // rounded once (the prelude's fp contract pragma forbids fusing it into the add)
    const float sum = prod + xf;  // rounded once
    return IGM_F2U(sum) ^ x;
}
// bit 5 (features 32, supersedes bit 4; the hash lead's 23:13 UK text): every opcode binary32 with two roundings, the inner
// operation rounded first: 0: x' + (y' - z'); 1: x' - (y' + z'); 2: (y' * z') + x'; 3: (x' - y') + z'; a zero difference +0.0
IGM_INLINE uint ih_v8_fop_wide(uint op, uint x, uint y, uint z) {
    const uint yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u, xb = (x & 0x3FFFFFFFu) | 0x20000000u;
    const float yf = IGM_U2F(yb), zf = IGM_U2F(zb), xf = IGM_U2F(xb);
    float inner, f;
    if (op == 0u) { inner = yf - zf; f = xf + inner; }
    else if (op == 1u) { inner = yf + zf; f = xf - inner; }
    else if (op == 2u) { inner = yf * zf; f = inner + xf; }
    else { inner = xf - yf; f = inner + zf; }
    return IGM_F2U(f) ^ x;
}
// IGM_IH_ITEM_WORDS (the host defines it from the document beside the version): the item width is a compile-time constant,
// so the version 4 group and step loops, which carry the lane exchange, have constant trip counts and unroll; on the
// local-exchange path (pocl) an exchange is a pair of work-group barriers, and a barrier inside a runtime-bounded inner loop
// is what pocl's work-item compiler got wrong (16:5x UK: the cooperative form mismatched on pocl even with the version
// compiled in; the exchange-free first form had been exact there).
#ifdef IGM_IH_ITEM_WORDS
#define IH_IW (IGM_IH_ITEM_WORDS)
#else
#define IH_IW (k->item_words)
#endif
#ifdef IGM_IH_TRACE
#define IGM_IH_TRACE_PARAM , IGM_GLOBAL uint *trace
#define IGM_IH_TRACE_ARG , trace
#define IH_TR(cond, off, n, src) if (trace && (cond)) { for (uint _i = 0; _i < (n); _i++) trace[(off) + _i] = (src)[_i]; }
#define IH_TR1(cond, off, v) if (trace && (cond)) { trace[(off)] = (v); }
#else
#define IGM_IH_TRACE_PARAM
#define IGM_IH_TRACE_ARG
#define IH_TR(cond, off, n, src)
#define IH_TR1(cond, off, v)
#endif

// ---- BLAKE3, one chunk (inputs of at most 1024 bytes), hash or keyed mode, with the
// ---- counter for extendable output. words holds the input as little-endian u32, zero
// ---- padded to a block; nbytes its length (0 allowed). out receives out_blocks x 16 words.
IGM_INLINE void ih_b3_compress(IGM_PRIV const uint *cv, IGM_PRIV const uint *m_in, uint t, uint block_len, uint flags, IGM_PRIV uint *out16) {
    uint v[16], m[16];
    for (uint i = 0; i < 16; i++) m[i] = m_in[i];
    for (uint i = 0; i < 8; i++) v[i] = cv[i];
    v[8] = igm_b3_iv[0]; v[9] = igm_b3_iv[1]; v[10] = igm_b3_iv[2]; v[11] = igm_b3_iv[3];
    v[12] = t; v[13] = 0u; v[14] = block_len; v[15] = flags;
    for (uint r = 0; r < 7; r++) {
        igm_b3_g(v, 0, 4, 8, 12, m[0], m[1]);   igm_b3_g(v, 1, 5, 9, 13, m[2], m[3]);
        igm_b3_g(v, 2, 6, 10, 14, m[4], m[5]);  igm_b3_g(v, 3, 7, 11, 15, m[6], m[7]);
        igm_b3_g(v, 0, 5, 10, 15, m[8], m[9]);  igm_b3_g(v, 1, 6, 11, 12, m[10], m[11]);
        igm_b3_g(v, 2, 7, 8, 13, m[12], m[13]); igm_b3_g(v, 3, 4, 9, 14, m[14], m[15]);
        if (r < 6) { uint p[16]; for (uint i = 0; i < 16; i++) p[i] = m[igm_b3_perm[i]]; for (uint i = 0; i < 16; i++) m[i] = p[i]; }
    }
    for (uint i = 0; i < 8; i++) { out16[i] = v[i] ^ v[i + 8]; out16[i + 8] = v[i + 8] ^ cv[i]; }
}

// static: with the two-chunk branch the AMD compiler no longer inlines every call, and an OpenCL C `inline` function without
// static has no external definition (the card's linker: undefined hidden symbol ih_b3_chunk, 10 Oct 2026 16:4x UK)
IGM_INLINE void ih_b3_chunk(IGM_PRIV const uint *key8_or_null, bool keyed, IGM_PRIV const uint *words, uint nbytes, uint out_blocks, IGM_PRIV uint *out) {
    // Written without conditional expressions over pointer loads: the Metal compiler on macOS 27
    // produced a wrong chaining value from the ternary form while the same compressions written
    // out by hand were exact (the trace diagnostics of 10 October 2026).
    uint cv[8];
    if (keyed) { for (uint i = 0; i < 8; i++) cv[i] = key8_or_null[i]; }
    else { for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i]; }
    uint base = 0u; if (keyed) base = IH_KEYED;
    if (nbytes > 1024u) {
        // Two chunks and a parent (BLAKE3's tree for inputs over 1024 bytes; the W1024 item input is 1,052 bytes): chunk 0 is
        // the first sixteen blocks with counter 0 (CHUNK_START on the first, CHUNK_END on the last, never ROOT), chunk 1 the
        // rest with counter 1, each chunk's chaining value the first eight output words of its last block; the root is the
        // parent compression of cv0 || cv1 under the key (or the IV) with PARENT and ROOT, its counter the output block.
        // Three chunks for inputs over 2048 bytes (version 8's wider state at 64 registers: the 2,099-byte M input; the hash lead,
        // 11 October 01:54 UK): chunks 0 and 1 under a parent without ROOT, chunk 2's chaining value joined with it at the root.
        // Inputs over 3072 bytes do not occur in this relation. The Riptide performance seat, 10 October 2026; the third chunk GPU B.
        // Written with one fixed array per chunk and no runtime-indexed private array and no conditional expressions: the
        // earlier form (cvs[c][i] over a runtime chunk count, value ternaries) read exact on the M6 (macOS 27.0.1) and wrong on
        // every class at RW 32 and 64 on the M5 Max (macOS 26.6.2) from the same worker, while the fixed cv0 / cv1 two-chunk
        // text before it was exact on both (the Orchard lane, 11 October 02:4x UK; the first apple10 instance was the
        // data-indexed register snapshot of step 1b, the same pattern).
        uint cv0[8], cv1[8], cv2[8], o[16], m[16];
        const uint nc = (nbytes + 1023u) / 1024u;   // 2 or 3
        // chunk 0: sixteen full blocks, counter 0
        for (uint i = 0; i < 8; i++) cv0[i] = cv[i];
        for (uint b = 0; b < 16; b++) {
            for (uint i = 0; i < 16; i++) m[i] = words[16u * b + i];
            uint flags = base; if (b == 0u) flags |= IH_CHUNK_START; if (b == 15u) flags |= IH_CHUNK_END;
            ih_b3_compress(cv0, m, 0u, 64u, flags, o);
            for (uint i = 0; i < 8; i++) cv0[i] = o[i];
        }
        // chunk 1: sixteen full blocks when a third chunk follows, else the rest; counter 1
        for (uint i = 0; i < 8; i++) cv1[i] = cv[i];
        uint rest1 = nbytes - 1024u; if (rest1 > 1024u) rest1 = 1024u;
        uint nb1 = (rest1 + 63u) / 64u; if (nb1 == 0u) nb1 = 1u;
        for (uint b = 0; b < nb1; b++) {
            uint blen = 64u; if (b + 1u == nb1) blen = rest1 - 64u * b;
            for (uint i = 0; i < 16; i++) { m[i] = 0u; if (4u * i < blen) m[i] = words[256u + 16u * b + i]; }
            uint flags = base; if (b == 0u) flags |= IH_CHUNK_START; if (b + 1u == nb1) flags |= IH_CHUNK_END;
            ih_b3_compress(cv1, m, 1u, blen, flags, o);
            for (uint i = 0; i < 8; i++) cv1[i] = o[i];
        }
        if (nc == 3u) {
            // chunk 2: the rest, counter 2
            for (uint i = 0; i < 8; i++) cv2[i] = cv[i];
            const uint rest2 = nbytes - 2048u;
            uint nb2 = (rest2 + 63u) / 64u; if (nb2 == 0u) nb2 = 1u;
            for (uint b = 0; b < nb2; b++) {
                uint blen = 64u; if (b + 1u == nb2) blen = rest2 - 64u * b;
                for (uint i = 0; i < 16; i++) { m[i] = 0u; if (4u * i < blen) m[i] = words[512u + 16u * b + i]; }
                uint flags = base; if (b == 0u) flags |= IH_CHUNK_START; if (b + 1u == nb2) flags |= IH_CHUNK_END;
                ih_b3_compress(cv2, m, 2u, blen, flags, o);
                for (uint i = 0; i < 8; i++) cv2[i] = o[i];
            }
            // the parent of chunks 0 and 1 (PARENT, not ROOT), joined with chunk 2 at the root
            for (uint i = 0; i < 8; i++) { m[i] = cv0[i]; m[8 + i] = cv1[i]; }
            ih_b3_compress(cv, m, 0u, 64u, base | IH_PARENT, o);
            for (uint i = 0; i < 8; i++) { m[i] = o[i]; m[8 + i] = cv2[i]; }
        } else {
            for (uint i = 0; i < 8; i++) { m[i] = cv0[i]; m[8 + i] = cv1[i]; }
        }
        for (uint t = 0; t < out_blocks; t++) {
            ih_b3_compress(cv, m, t, 64u, base | IH_PARENT | IH_ROOT, o);
            for (uint i = 0; i < 16; i++) out[16u * t + i] = o[i];
        }
        return;
    }
    uint nblocks = (nbytes + 63u) / 64u; if (nblocks == 0u) nblocks = 1u;
    uint last = nblocks - 1u;
    for (uint b = 0; b < last; b++) {
        uint m[16], o[16];
        for (uint i = 0; i < 16; i++) m[i] = words[16u * b + i];
        uint flags = base; if (b == 0u) flags |= IH_CHUNK_START;
        ih_b3_compress(cv, m, 0u, 64u, flags, o);
        for (uint i = 0; i < 8; i++) cv[i] = o[i];
    }
    uint m[16];
    uint blen = nbytes - 64u * last;
    for (uint i = 0; i < 16; i++) { m[i] = 0u; if (4u * i < blen) m[i] = words[16u * last + i]; }
    uint flags = base | IH_CHUNK_END | IH_ROOT; if (last == 0u) flags |= IH_CHUNK_START;
    for (uint t = 0; t < out_blocks; t++) {
        uint o[16];
        ih_b3_compress(cv, m, t, blen, flags, o);
        for (uint i = 0; i < 16; i++) out[16u * t + i] = o[i];
    }
}

// ---- byte builders (little-endian words of a byte string) ----
IGM_INLINE void ih_put8(IGM_PRIV uint *w, uint pos, uint b) { w[pos >> 2] |= (b & 0xffu) << (8u * (pos & 3u)); }
IGM_INLINE uint ih_put_tag(IGM_PRIV uint *w, uint pos, IGM_CONST const uchar *tag, uint len) { ih_put8(w, pos++, len); for (uint i = 0; i < len; i++) ih_put8(w, pos++, tag[i]); return pos; }
IGM_INLINE uint ih_put_be64(IGM_PRIV uint *w, uint pos, uint hi, uint lo) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, hi >> (24u - 8u * i)); for (uint i = 0; i < 4; i++) ih_put8(w, pos++, lo >> (24u - 8u * i)); return pos; }
IGM_INLINE uint ih_put_bytes32(IGM_PRIV uint *w, uint pos, IGM_PRIV const uint *le8) { for (uint i = 0; i < 32; i++) ih_put8(w, pos++, le8[i >> 2] >> (8u * (i & 3u))); return pos; }
IGM_INLINE uint ih_v8_put_lp(IGM_PRIV uint *w, uint pos, uint len) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, len >> (8u * i)); return pos; }
IGM_INLINE uint ih_v8_put_u32le(IGM_PRIV uint *w, uint pos, uint x) { for (uint i = 0; i < 4; i++) ih_put8(w, pos++, x >> (8u * i)); return pos; }
IGM_INLINE uint ih_put_bytes32c(IGM_PRIV uint *w, uint pos, IGM_CONSTANT_U32 const uint *le8) { for (uint i = 0; i < 32; i++) ih_put8(w, pos++, le8[i >> 2] >> (8u * (i & 3u))); return pos; }

IGM_CONST uchar ih_tag_seed[19] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','s','e','e','d'};
IGM_CONST uchar ih_tag_item[19] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','i','t','e','m'};
IGM_CONST uchar ih_tag_lanes[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','l','a','n','e','s'};
IGM_CONST uchar ih_tag_mix[18] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','m','i','x'};

// version 12 (section 2c): the row fold (the row's four words consumed by the fold), the scatter on the lane's own row, the exact INT8 octet tile
// the access macros and version 14's tile (IGM_R12_GATHER, IGM_R12_SCATTER, IGM_R12_MMA, IGM_R14_TILE, IGM_R14_FOLD) live in igm_r0e_access.inc, loaded by the
// engines BEFORE the period programme (the programme's function is compiled where it stands: the macros must precede it; the pocl read of 02:20 UK)
IGM_CONST uchar ih_tag_r0e_table[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','E','/','t','a','b','l','e'};   // version 10: the per-nonce gather table's tag
IGM_CONST uchar ih_tag_cache[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','c','a','c','h','e'};
IGM_CONST uchar ih_tag_final[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','A','/','f','i','n','a','l'};

// The per-variant constants the host passes: C (8 bytes BE as two words hi, lo), V, P (32
// bytes as 8 le words), E (32 bytes as 8 le words), sizes.

// Version 8 bit 10 (SPEC-R0A-v0.md 0.9, the bond-keyed rounds; the CUDA worker's layout, GPU A, 11 Oct 2026): round r is keyed iff
// (r S) mod ROUNDS < S, its index i = (r S) div ROUNDS; the share s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i), one 40-byte block
// (CHUNK_START | CHUNK_END | ROOT | KEYED_HASH, counter 0); m_0 = S (the work seed), m_i for i >= 1 the eight lane words R[l][0] at
// the round's entry (before the absorb). The share enters the item's words 0 .. 7 before the absorb, the programme and the tree.
// Every lane computes the compression (uniform) and takes its own words by selects, never a lane-indexed read. Returns the 32-byte
// share as eight words in out8; the caller XORs the words it holds (word j is lane j's first word on the generic path, lane j / 4's
// component j mod 4 of its first uint4 on form 1).
IGM_INLINE void ih_keyed_share(IGM_PRIV const uint *S16, IGM_PRIV const uint *R, uint ki, uint lane, IGM_PRIV const uint *sk8, IGM_XCH_PARAMS, IGM_PRIV uint *out8) {
    uint km[16]; uint kk[8];
    for (uint q = 0; q < 8; q++) kk[q] = sk8[q];
    km[0] = 0x646E6F62u;   /* "bond" */
    km[1] = ki;
    if (ki == 0u) { for (uint q = 0; q < 8; q++) km[2 + q] = S16[q]; }
    else { const uint r0 = R[0]; km[2] = IGM_BCAST(r0, 0u); km[3] = IGM_BCAST(r0, 1u); km[4] = IGM_BCAST(r0, 2u); km[5] = IGM_BCAST(r0, 3u); km[6] = IGM_BCAST(r0, 4u); km[7] = IGM_BCAST(r0, 5u); km[8] = IGM_BCAST(r0, 6u); km[9] = IGM_BCAST(r0, 7u); }
    for (uint q = 10; q < 16; q++) km[q] = 0u;
#ifdef IGM_KX_NOBCAST
    for (uint q = 0; q < 8; q++) km[2 + q] = S16[q];
#endif
    uint ko[16];
#ifdef IGM_KX_NOSHARE
    for (uint q = 0; q < 16; q++) ko[q] = 0u;
#else
    ih_b3_chunk(kk, true, km, 40u, 1u, ko);
#endif
    // one output block is sixteen words; the share is the first eight (an 8-word buffer overflowed the lane's neighbours: every keyed row wrong on the first full run, 11 Oct 2026)
    for (uint q = 0; q < 8; q++) out8[q] = ko[q];
}

struct ih_consts { uint c_hi, c_lo, v; uint p[8]; uint e[8]; uint n_items_mask; uint slice_words; uint slice_lines_mask; uint rounds; uint item_chain; uint n_cache_mask; uint parents; uint version; uint item_words; uint sub_blocks; uint blocks; uint registers; uint features; uint n_items; uint fastrange; uint program_ops; uint fold_every; uint batch_log2;  uint sk[8]; };   // the bond secret's eight words (version 8 bit 10; the test secret for the vectors and the bench, a job's sk under the keyed-job extension; zero elsewhere)
typedef struct ih_consts ih_consts;

// the address map: versions 0 to 6 mask a power of two; version 8 at a non-power-of-two size (the 10 GiB form) takes the
// multiply-high map idx = (x * N_ITEMS) >> 32 (SPEC 0.5's form, the reference's fastrange)
IGM_INLINE uint ih_map(uint x, IGM_PRIV const ih_consts *k) { return k->fastrange ? IGM_MULHI(x, k->n_items) : (x & k->n_items_mask); }
// item(i): BLAKE3kxof(E, tag(T_ITEM) || u64be(i) [|| K_(c-1)], 128), chained item_chain times
IGM_INLINE void ih_item(IGM_PRIV const ih_consts *k, uint i_hi, uint i_lo, IGM_PRIV uint *item32) {
    uint w[32]; for (uint j = 0; j < 32; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_item, 19u);
    pos = ih_put_be64(w, pos, i_hi, i_lo);
    uint base = pos;   // 28
    uint o[32];
    if (k->item_chain <= 1u) { ih_b3_chunk(k->e, true, w, base, 2u, item32); return; }
    ih_b3_chunk(k->e, true, w, base, 1u, o);                       // K_1
    for (uint j = 2; j <= k->item_chain; j++) {
        for (uint b = 0; b < 32; b++) ih_put8(w, base + b, o[b >> 2] >> (8u * (b & 3u)));   // || K_(j-1): overwrite the 32 bytes after the index
        if (j == k->item_chain) { ih_b3_chunk(k->e, true, w, base + 32u, 2u, item32); return; }
        uint o2[32]; ih_b3_chunk(k->e, true, w, base + 32u, 1u, o2); for (uint b = 0; b < 32; b++) { ih_put8(w, base + b, 0u); } for (uint b = 0; b < 8; b++) o[b] = o2[b];
        // clear the K bytes before rewriting: ih_put8 ORs, so zero the words first
        for (uint b = 0; b < 32; b++) { uint pw = (base + b) >> 2; w[pw] &= ~(0xffu << (8u * ((base + b) & 3u))); }
    }
}

IGM_INLINE uint ih_fnv(uint a, uint b) { return (a * 0x01000193u) ^ b; }
IGM_INLINE uint ih_bswap(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }   // the big-endian fields as little-endian words
// The streamed BLAKE3 inputs of the hash (the Riptide performance seat): every input built block by block as static
// word compositions of the fields, no byte array; the same bytes, compressions and flags as ih_b3_chunk (the vectors
// prove it). Shared by the eight-lane and the one-wave (Breakwater) kernels.
// S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N)): 104 bytes, blocks of 64 and 40
IGM_INLINE void ih_stream_S(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_PRIV uint *S16) {
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    uint m[16], o[16];
    m[0] = 0x6e674913u; m[1] = 0x486d7565u; m[2] = 0x2f687361u; m[3] = 0x2f413052u; m[4] = 0x64656573u;   // 0x13 "IgneumHash/R0A/seed"
    m[5] = ih_bswap(k->c_hi); m[6] = ih_bswap(k->c_lo); m[7] = ih_bswap(k->v);
    for (uint i = 0; i < 8; i++) m[8 + i] = k->p[i];
    ih_b3_compress(cv, m, 0u, 64u, IH_CHUNK_START, o);
    for (uint i = 0; i < 8; i++) cv[i] = o[i];
    for (uint i = 0; i < 8; i++) m[i] = hdr8[i];
    m[8] = ih_bswap(nonce_hi); m[9] = ih_bswap(nonce_lo);
    for (uint i = 10; i < 16; i++) m[i] = 0u;
    ih_b3_compress(cv, m, 0u, 40u, IH_CHUNK_END | IH_ROOT, S16);
}
// L = BLAKE3xof(tag(T_LANES) || S, 512): 53 bytes, one block; output block t (lane t's sixteen registers)
IGM_INLINE void ih_stream_L(IGM_PRIV const uint *S, uint t, IGM_PRIV uint *R16) {
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    uint m[16];
    m[0] = 0x6e674914u; m[1] = 0x486d7565u; m[2] = 0x2f687361u; m[3] = 0x2f413052u; m[4] = 0x656e616cu;   // 0x14 "IgneumHash/R0A/lanes"
    m[5] = 0x73u | (S[0] << 8);                                  // 's' then S from byte 21
    for (uint i = 1; i < 8; i++) m[5 + i] = (S[i - 1] >> 24) | (S[i] << 8);
    m[13] = S[7] >> 24; m[14] = 0u; m[15] = 0u;
    ih_b3_compress(cv, m, t, 53u, IH_CHUNK_START | IH_CHUNK_END | IH_ROOT, R16);
}
// F = BLAKE3(tag(T_FINAL) || C || V || P || S || M): 129 bytes, blocks of 64, 64 and 1
IGM_INLINE void ih_stream_F(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *S, IGM_PRIV const uint *M, IGM_PRIV uint *F8) {
    uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
    uint m[16], o[16];
    const uint bch = ih_bswap(k->c_hi), bcl = ih_bswap(k->c_lo), bv = ih_bswap(k->v);
    m[0] = 0x6e674914u; m[1] = 0x486d7565u; m[2] = 0x2f687361u; m[3] = 0x2f413052u; m[4] = 0x616e6966u;   // 0x14 "IgneumHash/R0A/final"
    m[5] = 0x6cu | (bch << 8); m[6] = (bch >> 24) | (bcl << 8); m[7] = (bcl >> 24) | (bv << 8);
    m[8] = (bv >> 24) | (k->p[0] << 8);
    for (uint i = 1; i < 8; i++) m[8 + i] = (k->p[i - 1] >> 24) | (k->p[i] << 8);
    ih_b3_compress(cv, m, 0u, 64u, IH_CHUNK_START, o);
    for (uint i = 0; i < 8; i++) cv[i] = o[i];
    m[0] = (k->p[7] >> 24) | (S[0] << 8);
    for (uint i = 1; i < 8; i++) m[i] = (S[i - 1] >> 24) | (S[i] << 8);
    m[8] = (S[7] >> 24) | (M[0] << 8);
    for (uint i = 1; i < 8; i++) m[8 + i] = (M[i - 1] >> 24) | (M[i] << 8);
    ih_b3_compress(cv, m, 0u, 64u, 0u, o);
    for (uint i = 0; i < 8; i++) cv[i] = o[i];
    m[0] = M[7] >> 24; for (uint i = 1; i < 16; i++) m[i] = 0u;
    ih_b3_compress(cv, m, 0u, 1u, IH_CHUNK_END | IH_ROOT, o);
    for (uint q = 0; q < 8; q++) F8[q] = o[q];
}
// the head of the M stream (block 0's first twelve words: the tag and S; word 12 on is the state): tag(T_MIX) 19 bytes, S from byte 19
#define IH_MIX_HEAD(m, S) { (m)[0] = 0x6e674912u; (m)[1] = 0x486d7565u; (m)[2] = 0x2f687361u; (m)[3] = 0x2f413052u; (m)[4] = 0x0078696du | ((S)[0] << 24); for (uint _i = 1; _i < 8; _i++) (m)[4 + _i] = ((S)[_i - 1] >> 8) | ((S)[_i] << 24); }

// Version 1 item(i) (5.3): mix = cache(i mod N_CACHE) with lo32(i), hi32(i) folded into words 0
// and 1; PARENTS steps of parent = fnv(lo32(i) ^ p, mix[p mod 32]) mod N_CACHE, mix[k] = fnv(mix[k],
// cache(parent)[k]); item = BLAKE3kxof(E, tag(T_ITEM) || u64be(i) || mix as 128 le bytes, 128).
IGM_INLINE void ih_item_v1(IGM_PRIV const ih_consts *k, uint i_hi, uint i_lo, IGM_GLOBAL const uint *cache, IGM_PRIV uint *item32) {
    uint mix[32];
    IGM_GLOBAL const uint *c0 = cache + (ulong)(i_lo & k->n_cache_mask) * 32u;
    for (uint j = 0; j < 32; j++) mix[j] = c0[j];
    mix[0] ^= i_lo; mix[1] ^= i_hi;
    for (uint p = 0; p < k->parents; p++) {
        uint parent = ih_fnv(i_lo ^ p, mix[p & 31u]) & k->n_cache_mask;
        IGM_GLOBAL const uint *cp = cache + (ulong)parent * 32u;
        for (uint j = 0; j < 32; j++) mix[j] = ih_fnv(mix[j], cp[j]);
    }
    uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_item, 19u);
    pos = ih_put_be64(w, pos, i_hi, i_lo);        // 28: word aligned, the mix words follow directly
    for (uint j = 0; j < 32; j++) w[7u + j] = mix[j];
    ih_b3_chunk(k->e, true, w, 156u, 2u, item32);
}

// T-B6: version 1 item i by eight lanes (lane l holds mix words 4l..4l+3); every cache read of the group is one
// 128-byte line; the parent index needs mix[p mod 32], held by lane (p mod 32) / 4, broadcast to all; lane 0
// gathers the 128 mix bytes through the exchange area (device memory, 32 words per group) and runs the keyed XOF.
IGM_INLINE void ih_item_v1_lanes(IGM_PRIV const ih_consts *k, uint i_hi, uint i_lo, IGM_GLOBAL const uint *cache, uint lane, IGM_XCH_PARAMS,
                             IGM_GLOBAL uint *gather32, IGM_PRIV uint *item32) {
    uint m0, m1, m2, m3;
    {
        IGM_GLOBAL const uint *c0 = cache + (ulong)(i_lo & k->n_cache_mask) * 32u + 4u * lane;
        m0 = c0[0]; m1 = c0[1]; m2 = c0[2]; m3 = c0[3];
    }
    if (lane == 0u) { m0 ^= i_lo; m1 ^= i_hi; }
    for (uint p = 0; p < k->parents; p++) {
        uint w = p & 31u, src = w >> 2u, sel = w & 3u;
        uint mine = sel == 0u ? m0 : sel == 1u ? m1 : sel == 2u ? m2 : m3;
        uint mw = IGM_BCAST(mine, src);
        uint parent = ih_fnv(i_lo ^ p, mw) & k->n_cache_mask;
        IGM_GLOBAL const uint *cp = cache + (ulong)parent * 32u + 4u * lane;
        m0 = ih_fnv(m0, cp[0]); m1 = ih_fnv(m1, cp[1]); m2 = ih_fnv(m2, cp[2]); m3 = ih_fnv(m3, cp[3]);
    }
    gather32[4u * lane] = m0; gather32[4u * lane + 1u] = m1; gather32[4u * lane + 2u] = m2; gather32[4u * lane + 3u] = m3;
    IGM_ST_SYNC();
    if (lane == 0u) {
        uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
        uint pos = ih_put_tag(w, 0u, ih_tag_item, 19u);
        pos = ih_put_be64(w, pos, i_hi, i_lo);
        for (uint j = 0; j < 32; j++) w[7u + j] = gather32[j];
        ih_b3_chunk(k->e, true, w, 156u, 2u, item32);
    }
}

// Version 3 item(i) at W bytes (the hash lead's text, 10 Oct 2026): mix = the run of W/128 consecutive 128-byte cache
// items from i mod N_CACHE (W/4 words, wrapping), mix[0] ^= lo32(i), mix[1] ^= hi32(i); PARENTS steps with
// parent = fnv(lo32(i) ^ p, mix[p mod (W/4)]) mod N_CACHE and mix[k] = fnv(mix[k], run(parent)[k]) over all W/4 words;
// item = BLAKE3kxof(E, tag(T_ITEM) || u64be(i) || mix bytes, W). W = 128 is version 1's rule exactly.
IGM_INLINE void ih_item_v3(IGM_PRIV const ih_consts *k, uint i_hi, uint i_lo, IGM_GLOBAL const uint *cache, IGM_PRIV uint *item) {
    const uint iw = k->item_words, runs = k->sub_blocks;
    uint mix[256];
    for (uint j = 0; j < runs; j++) {
        IGM_GLOBAL const uint *c0 = cache + (ulong)((i_lo + j) & k->n_cache_mask) * 32u;
        for (uint q = 0; q < 32; q++) mix[32u * j + q] = c0[q];
    }
    mix[0] ^= i_lo; mix[1] ^= i_hi;
    for (uint p = 0; p < k->parents; p++) {
        uint parent = ih_fnv(i_lo ^ p, mix[p % iw]) & k->n_cache_mask;
        for (uint j = 0; j < runs; j++) {
            IGM_GLOBAL const uint *cp = cache + (ulong)((parent + j) & k->n_cache_mask) * 32u;
            for (uint q = 0; q < 32; q++) mix[32u * j + q] = ih_fnv(mix[32u * j + q], cp[q]);
        }
    }
    uint w[264]; for (uint j = 0; j < 264; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_item, 19u);
    pos = ih_put_be64(w, pos, i_hi, i_lo);        // 28: word aligned, the mix words follow directly
    for (uint j = 0; j < iw; j++) w[7u + j] = mix[j];
    ih_b3_chunk(k->e, true, w, 28u + 4u * iw, iw / 16u, item);
}

// Version 4 at W = 64 (0.4): version 1's walk over one cache item, word for word, and only the XOF output cut to 64 bytes
IGM_INLINE void ih_item_w64(IGM_PRIV const ih_consts *k, uint i_hi, uint i_lo, IGM_GLOBAL const uint *cache, IGM_PRIV uint *item16) {
    uint mix[32];
    IGM_GLOBAL const uint *c0 = cache + (ulong)(i_lo & k->n_cache_mask) * 32u;
    for (uint j = 0; j < 32; j++) mix[j] = c0[j];
    mix[0] ^= i_lo; mix[1] ^= i_hi;
    for (uint p = 0; p < k->parents; p++) {
        uint parent = ih_fnv(i_lo ^ p, mix[p & 31u]) & k->n_cache_mask;
        IGM_GLOBAL const uint *cp = cache + (ulong)parent * 32u;
        for (uint j = 0; j < 32; j++) mix[j] = ih_fnv(mix[j], cp[j]);
    }
    uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_item, 19u);
    pos = ih_put_be64(w, pos, i_hi, i_lo);
    for (uint j = 0; j < 32; j++) w[7u + j] = mix[j];
    ih_b3_chunk(k->e, true, w, 156u, 1u, item16);
}

// The AMD logical-to-physical dataset swizzle (the convergence design, 10 Oct 2026 15:1x UK; consensus-neutral: the relation
// reads item(idx) exactly, only its slot on the device moves): slot(i) = i XOR ((i >> 8) & 0xff) XOR (((i >> 16) & 0xff) << 4),
// a bijection on every power-of-two item count of at least 2^12 (each xor term touches bits below the ones it is drawn from,
// so the map is triangular and invertible), spreading an item's low eight bits (the bank and column pattern on a 128-byte
// granularity) by its row bits. Nothing in the build or the hash changes; the vectors prove the identity.
#ifdef IGM_IH_SWIZZLE
IGM_INLINE uint ih_slot(uint i, uint mask) { return (i ^ ((i >> 8u) & 0xffu) ^ (((i >> 16u) & 0xffu) << 4u)) & mask; }
#else
IGM_INLINE uint ih_slot(uint i, uint mask) { (void)mask; return i; }
#endif

// The dataset item kernel body: item i of the resident dataset (W bytes = W/4 words; 128 under versions 0 and 1); cache is
// the version 1 epoch cache (unused under version 0).
IGM_INLINE void ih_dataset_item(IGM_PRIV const ih_consts *k, uint i, IGM_GLOBAL const uint *cache, IGM_GLOBAL uint *dst) {
    if (k->version >= 4u && k->item_words == 16u) {
        uint item[16];
        ih_item_w64(k, 0u, i, cache, item);
        { const uint sl = ih_slot(i, k->n_items_mask); for (uint j = 0; j < 16; j++) dst[(ulong)sl * 16u + j] = item[j]; }
        return;
    }
    if (k->version >= 3u) {
        uint item[256];
        ih_item_v3(k, 0u, i, cache, item);
        { const uint sl = ih_slot(i, k->n_items_mask); for (uint j = 0; j < k->item_words; j++) dst[(ulong)sl * k->item_words + j] = item[j]; }
        return;
    }
    uint item[32];
    if (k->version >= 1u) ih_item_v1(k, 0u, i, cache, item); else ih_item(k, 0u, i, item);
    { const uint sl = ih_slot(i, k->n_items_mask); for (uint j = 0; j < 32; j++) dst[(ulong)sl * 32u + j] = item[j]; }
}

// ---- the work function for one lane of one nonce ----
// hdr8: H as 8 le words; nonce (hi32, lo32) with S over u64be(N); scratch: this lane's slice
// base in device memory (slice_words words); st_area: the 160-word state area of this
// nonce in device memory (every lane writes its 20 words, lane 0 hashes M). mix_out8 = M,
// final8 = F, valid in lane 0 (every lane computes S and F identically; M needs the area).
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
#define IGM_IH_DATASET2_PARAM , IGM_GLOBAL const uint *dataset2
#define IGM_IH_DATASET2_ARG , dataset2
#else
#define IGM_IH_DATASET2_PARAM
#define IGM_IH_DATASET2_ARG
#endif
IGM_INLINE void igm_igneumhash_lane(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                                IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, IGM_GLOBAL uint *scratch_slice, IGM_GLOBAL uint *st_area,
                                uint lane, IGM_XCH_PARAMS, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8 IGM_IH_TRACE_PARAM IGM_IH_TGM_PARAM IGM_IH_R0E_PARAM IGM_R0E_OPTRACE_LANE_PARAM) {
#if defined(IGM_IH_COPIES) && IGM_IH_COPIES > 1
    // the channel-coloured allocation (the auditor's AMD solution, 10 Oct 2026 21:2x UK): IGM_IH_COPIES identical copies of the
    // dataset sit end to end in the buffer and a nonce reads the copy its low bits name; every logical address is unchanged
    // (the relation never sees the copy), so exactness holds by construction and the row asks only the memory system
    dataset += (ulong)(nonce_lo & (IGM_IH_COPIES - 1u)) * ((ulong)k->n_items_mask + 1ul) * (ulong)k->item_words;
#endif
#ifdef IGM_IH_STREAM_FINAL
    // The streamed form (the Riptide performance seat, 10 October 2026): every BLAKE3 input of the hash built block by
    // block as 16 words from static shifts of the fields (the byte offsets are the specification's: tag 20 bytes for S,
    // 21 for L and F, 19 for M; the tag words are the little-endian bytes of the length byte and the ASCII tag), no
    // byte array, no byte-wise store, so nothing lives in private (scratch) memory and the register peak of the kernel
    // is the round loop's, not the byte builder's (the gfx1201 dry run: 169 to about 110 VGPRs). The same bytes, the
    // same compressions, the same flags as the chunk function: the vectors prove it.
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);

#if IGM_IH_VERSION == 10
    {   // version 10 (R0E): the nonce's 16 KB gather table, the BLAKE3 XOF of (lp("IgneumHash/R0E/table") || S) in the nonce's scratch region; the eight lanes share the 256 output blocks
        uint tm[16]; for (uint q = 0; q < 16; q++) tm[q] = 0u;
        uint tp = ih_put_tag(tm, 0u, ih_tag_r0e_table, 20u); tp = ih_put_bytes32(tm, tp, S);
        uint iv8[8]; for (uint q = 0; q < 8; q++) iv8[q] = igm_b3_iv[q];
        for (uint t = lane; t < 256u; t += 8u) { uint o16[16]; ih_b3_compress(iv8, tm, t, tp, 1u | 2u | 8u, o16); for (uint q = 0; q < 16; q++) scratch_slice[t * 16u + q] = o16[q]; }
        IGM_R0E_GBARRIER();
    }
#endif
    // version 8's program seed on the streamed path too (the round loop's version 8 block reads it on every path; it was declared
    // on the byte-builder path only, so every wave form failed to compile for gfx1201 after d1c8da0c; the Riptide seat, 23:2x UK)
    IH_TR(lane == 0u, 0u, 8u, S); IH_TR(lane == 0u, 8u, 8u, k->e); IH_TR(lane == 0u, 16u, 8u, k->p);
#else
    // S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N)): 20 + 8 + 4 + 32 + 32 + 8 = 104 bytes
    uint w[48]; for (uint j = 0; j < 48; j++) w[j] = 0u;
    uint pos = ih_put_tag(w, 0u, ih_tag_seed, 19u);
    pos = ih_put_be64(w, pos, k->c_hi, k->c_lo);
    for (uint i = 0; i < 4; i++) ih_put8(w, pos++, k->v >> (24u - 8u * i));
    pos = ih_put_bytes32(w, pos, k->p);
    pos = ih_put_bytes32(w, pos, hdr8);
    pos = ih_put_be64(w, pos, nonce_hi, nonce_lo);
    uint S[16]; ih_b3_chunk(k->p, false, w, pos, 1u, S);          // S in S[0..8); key unused in hash mode

#if IGM_IH_VERSION == 10
    {   // version 10 (R0E): the nonce's 16 KB gather table, the BLAKE3 XOF of (lp("IgneumHash/R0E/table") || S) in the nonce's scratch region; the eight lanes share the 256 output blocks
        uint tm[16]; for (uint q = 0; q < 16; q++) tm[q] = 0u;
        uint tp = ih_put_tag(tm, 0u, ih_tag_r0e_table, 20u); tp = ih_put_bytes32(tm, tp, S);
        uint iv8[8]; for (uint q = 0; q < 8; q++) iv8[q] = igm_b3_iv[q];
        for (uint t = lane; t < 256u; t += 8u) { uint o16[16]; ih_b3_compress(iv8, tm, t, tp, 1u | 2u | 8u, o16); for (uint q = 0; q < 16; q++) scratch_slice[t * 16u + q] = o16[q]; }
        IGM_R0E_GBARRIER();
    }
#endif
    IH_TR(lane == 0u, 0u, 8u, S); IH_TR(lane == 0u, 8u, 8u, k->e); IH_TR(lane == 0u, 16u, 8u, k->p);
    IH_TR(lane == 0u, 608u, 26u, w); IH_TR1(lane == 0u, 634u, pos);   // the S input words and length (diagnostics of the hash itself)
#endif
#if defined(IGM_IH_TRACE) && !defined(IGM_IH_STREAM_FINAL)
    if (trace && lane == 0u) {   // the two compressions of S by hand: block 0 chaining value, block 1 root
        uint cv0[8], m0[16], o0[16], o1[16];
        for (uint i = 0; i < 8; i++) cv0[i] = igm_b3_iv[i];
        for (uint i = 0; i < 16; i++) m0[i] = w[i];
        ih_b3_compress(cv0, m0, 0u, 64u, IH_CHUNK_START, o0);
        for (uint i = 0; i < 16; i++) trace[640u + i] = o0[i];
        for (uint i = 0; i < 8; i++) cv0[i] = o0[i];
        for (uint i = 0; i < 16; i++) m0[i] = (64u + 4u * i < pos) ? w[16u + i] : 0u;
        ih_b3_compress(cv0, m0, 0u, pos - 64u, IH_CHUNK_END | IH_ROOT, o1);
        for (uint i = 0; i < 16; i++) trace[656u + i] = o1[i];
        for (uint i = 0; i < 16; i++) trace[672u + i] = m0[i];
    }
#endif
    // 4.1 L = BLAKE3xof(tag(T_LANES) || S, 512): 21 + 32 = 53 bytes, 8 output blocks; lane l takes block l
    uint R[IGM_IH_RW];
    // lane l takes output blocks (RW / 16) l .. (RW / 16) (l + 1) - 1 of the XOF (one block at the 16-register state; two or four under
    // the wider state: 4 RW bytes per lane in order, the reference's lb)
#ifdef IGM_IH_STREAM_FINAL
    for (uint s = 0; s < IGM_IH_RW / 16u; s++) ih_stream_L(S, (IGM_IH_RW / 16u) * lane + s, R + 16u * s);
#else
    for (uint j = 0; j < 48; j++) w[j] = 0u;
    pos = ih_put_tag(w, 0u, ih_tag_lanes, 20u);
    pos = ih_put_bytes32(w, pos, S);
    {
        // only this lane's output blocks: compress the (single) input block with t = the output block index
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16]; for (uint i = 0; i < 16; i++) m[i] = (4u * i < pos) ? w[i] : 0u;
        for (uint s = 0; s < IGM_IH_RW / 16u; s++) ih_b3_compress(cv, m, (IGM_IH_RW / 16u) * lane + s, pos, IH_CHUNK_START | IH_CHUNK_END | IH_ROOT, R + 16u * s);
    }
#endif
    IH_TR(true, 32u + 16u * lane, 16u, R);
    const bool pad = k->slice_words != 0u;   // uniform: absent under version 1 with SCRATCH_BYTES 0
    // 4.2 scratch fill on this lane's slice
#if !defined(IGM_IH_STATIC_INDEX) || !defined(IGM_IH_SLICE_WORDS) || IGM_IH_SLICE_WORDS > 0u   // the static-index form: no scratch-fill loop to index R at run time when the document has no scratch
    for (uint j = 0; j < k->slice_words; j++) {
        uint a = R[j & 15u], b = R[(j + 5u) & 15u];
        a = (a ^ ((b << 13) | (b >> 19))) * IH_MUL;
        a = a + (b >> 7);
        R[j & 15u] = a;
        scratch_slice[j] = a ^ R[(j + 11u) & 15u];
    }
#endif
    // 4.3 first address: (R[0][0] ^ R[4][8]) mod N_ITEMS
    uint r00 = IGM_BCAST(R[0], 0u), r48 = IGM_BCAST(R[8], 4u);
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14) && IGM_IH_SHW > 0
    uint nonce_off = 0u;   // the shared form (section 2c): the nonce's row offset into the period's table, the fold over the work seed's eight words
#else
    const uint nonce_off = 0u;
#endif
#if IGM_IH_VERSION >= 11
    {   // version 11 (R0E, the auditor's eleventh reading): the nonce's 4 KB gather table by a chained scatter over the dataset; version 12: the same first 1,024 words into the LOCAL table, then the on-chip extension: 16 reads of one item each,
        // the first address from the work seed and the live registers, each next from a fold of every word of the previous item mixed with the registers
        // and the seed; every word enters the table XORed with lane (j mod 8)'s register ((j div 8) mod 16); every lane walks the chain (the fold is
        // sequential over the item's 64 words) and writes its own eighth of each item into the nonce's scratch region
        // the first address (hash/r0e-v11-fix2): the same fold as the later steps, from zero over the work seed's eight words then all 128 registers in lane-major order
        uint x = 0u;
        for (uint q = 0; q < 8u; q++) { const uint v = S[q]; x = rotate(x ^ v, 5u) + v; }
        for (uint L = 0; L < 8u; L++) for (uint q = 0; q < 16u; q++) { const uint v = IGM_BCAST(R[q], L); x = rotate(x ^ v, 5u) + v; }
        x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13;   // the fold finaliser (versions 11 and 12 of record, main 47f33324)
        for (uint kk = 0; kk < 16u; kk++) {
            const uint idxt = ih_map(x, k);
#ifdef IGM_IH_SPLIT2
            IGM_GLOBAL const uint *itp = (idxt > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idxt - (k->n_items_mask >> 1u) - 1u) * IH_IW : dataset + (ulong)idxt * IH_IW;
#else
            IGM_GLOBAL const uint *itp = dataset + (ulong)ih_slot(idxt, k->n_items_mask) * IH_IW;
#endif
            uint fold = 0u;
            for (uint w = 0; w < 64u; w++) {
                const uint wd = itp[w]; fold = rotate(fold ^ wd, 5u) + wd;
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
                if ((w & 7u) == lane && (kk * 64u + w) < IGM_IH_TKW) r12_tbl[kk * 64u + w] = wd ^ R[((kk * 64u + w) >> 3) & 15u];   // a table under 4 KB keeps the slice's first words (main 2a12dee9)
#else
                if ((w & 7u) == lane) scratch_slice[kk * 64u + w] = wd ^ R[((kk * 64u + w) >> 3) & 15u];
#endif
            }
            const uint rn = IGM_BCAST(R[(kk + 1u) & 15u], (kk + 1u) & 7u);
            x = fold ^ rn ^ S[(kk + 1u) & 7u];
        }
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
#if IGM_IH_SHW > 0
        { uint xo = 0u; for (uint q = 0; q < 8u; q++) { const uint t = S[q]; xo = rotate(xo ^ t, 5u) + t; } nonce_off = xo; }
#endif
        IGM_R0E_BARRIER();
#if IGM_IH_TKW > 1024u
        if (lane == 0u) { for (uint j = 1024u; j < IGM_IH_TKW; j++) r12_tbl[j] = rotate(r12_tbl[j - 1024u] ^ r12_tbl[j - 1u], 7u) * 0x9E3779B1u + j; }   // the on-chip extension: sequential, one lane per nonce
        IGM_R0E_BARRIER();
#endif
#else
        IGM_R0E_GBARRIER();
#endif
    }
#endif
    uint idx = ih_map(r00 ^ r48, k);
#if defined(IGM_IH_DIAG_A) || defined(IGM_IH_DIAG_B)
#ifndef IGM_IH_DIAG_WIDTH
#define IGM_IH_DIAG_WIDTH 128u
#endif
#endif
#if (defined(IGM_IH_DIAG_A) || defined(IGM_IH_DIAG_B)) && defined(IGM_IH_DIAG_WIDTH) && IGM_IH_DIAG_WIDTH > 128u && !defined(IGM_IH_DIAG_A_SPLIT)
    idx &= ~(IGM_IH_DIAG_WIDTH / 128u - 1u);   // a wide request is one W-aligned item: its first line is the stock load, the rest the steps
#endif
#ifdef IGM_IH_DIAG_A
    const uint idx0 = idx;
#endif
#if defined(IGM_IH_DIAG_B) && defined(IGM_IH_DIAG_CHAINS) && IGM_IH_DIAG_CHAINS >= 2
    // the dual-chain gate (the Riptide lead's reasoning, 10 Oct 2026 21:1x UK): a second independent dependent chain per
    // nonce, one more W-byte request in flight per round, to read whether the fabric's energy per request falls with more
    // chains in flight (the twin's property); only if it falls is a dual-chain kernel worth building. DIAGNOSTIC ONLY.
    uint idx2 = (idx ^ 0x5BD1E995u) & k->n_items_mask;
#if IGM_IH_DIAG_WIDTH > 128u
    idx2 &= ~(IGM_IH_DIAG_WIDTH / 128u - 1u);
#endif
#endif
#ifdef IGM_IH_REACH_MASK
    // DIAGNOSTIC ONLY (never a cell: the hash is wrong on purpose): every item index masked to the first half of the
    // dataset, the allocation unchanged, to tell a translation-reach cost from an allocation-size cost
    const uint reach_mask = k->n_items_mask >> 1u;
    idx &= reach_mask;
#endif
    IH_TR(true, 160u + 16u * lane, 16u, R);
#ifdef IGM_IH_XFER_DIAG
    uint xkeep[IGM_IH_XFER_KEEP]; for (uint i = 0; i < IGM_IH_XFER_KEEP; i++) xkeep[i] = R[i & 15u] ^ (i * IH_MUL);
#endif
    for (uint r = 0; r < k->rounds; r++) {
        uint kshare[8]; for (uint q = 0; q < 8; q++) kshare[q] = 0u;   // the keyed share for the programme's item reads (section 0.9): set on a keyed round, zero otherwise
        IH_TR1(r < 2u && lane == 0u, 288u + 136u * r, idx);
        // 4.4 load: lane l takes words 4l..4l+3 of the item
        // version 3 (the item width W): the item is W/4 words and lane l takes words (W/32) l .. (W/32) l + W/32 - 1,
        // folded in four-word sub-blocks in order; W = 128 is one sub-block, versions 0 and 1 exactly
        const uint iw = IH_IW, lw = iw >> 3u;
        uint h4 = 0u;   // version 4: the item word h (the reduction tree's root)
        IGM_GLOBAL const uint *r0e_item = dataset;   // version 10: the fetched item, read whole by the programme's item-sourced operands
        uint wraw8[8]; for (uint q = 0; q < 8; q++) wraw8[q] = 0u;   // version 8: the raw item words this lane holds for the programme decode (the layout per form: IH_PW)
#ifdef IGM_IH_TRACE_FULL
        const uint trb = 704u + r * (130u + iw);   // the full trace: [idx, h, item[iw], regs[128]] per round (round scope: both branches and the round's tail write it)
        IH_TR1(lane == 0u, trb, idx);   // the round's address, every version
#endif
#ifdef IGM_IH_OVERFETCH
        // the Turing twin (main, 10 Oct 14:5x UK; GPU A's experiment on AMD): the W128 relation unchanged, the request
        // widened to the IGM_IH_OVERFETCH-byte aligned span holding the item; the unused half is read and kept alive in an
        // unread state word (never in the hash: the traces and proofs are version 4 at 128 bytes exactly)
        {
            const uint span_items = IGM_IH_OVERFETCH / 128u;
            const uint base_idx = idx & ~(span_items - 1u);
            uint of = 0u;
            for (uint oi = 0; oi < span_items; oi++) {
                if (base_idx + oi == idx) continue;
                IGM_GLOBAL const uint *o = dataset + (ulong)(base_idx + oi) * iw + lw * lane;
                of ^= o[0] + o[1] + o[2] + o[3];
            }
            st_area[159u] = of;   // a state word the finalisation never reads (M covers 128 words without a scratchpad)
        }
#endif
        if (IH_V4) {
            // Version 4 (0.4, Strata): the whole item consumed before the next address. Lane l reads words l + 8 s (one
            // coalesced 32-byte step per s across the group) and absorbs each into R[s mod 16] (word j into R[(j div 8) mod 16]
            // of lane j mod 8, the text's rule); the balanced domain-separated tree over all n words is computed
            // cooperatively: three levels through the lane exchange (pairs 2i, 2i + 1 sit on lanes l, l + 1; then l, l + 2;
            // then l, l + 4), leaving every lane the result of each eight-word group, and the remaining levels locally over
            // the groups; h is the root. Then the cell of 4.4 once with w = h for every quad, the permutation, the exchange,
            // and idx = (R[g][0] ^ h) mod N_ITEMS below. (The first form read every word on every lane: 2 MH/s on the M6
            // against 12 for version 1; this form is the same relation with eight times fewer loads.)
#ifdef IGM_IH_SPLIT2
            // the dataset above 2 GiB sits in two buffers (T-B8: the OpenCL rule), the index's top bit selecting the buffer, as
            // the stock round does; the version 4 forms read one buffer before this (every item of the upper half past the end
            // of the first: a segfault on pocl at 4 GiB, found on the RX-F pair, 10 October 2026 16:54Z)
            IGM_GLOBAL const uint *it4 = (idx > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idx - (k->n_items_mask >> 1u) - 1u) * iw : dataset + (ulong)idx * iw;
            r0e_item = it4;
#else
            IGM_GLOBAL const uint *it4 = dataset + (ulong)ih_slot(idx, k->n_items_mask) * iw;
            r0e_item = it4;
#endif
            const uint ng = iw >> 3u;   // eight-word groups: 2, 4, 8, 16, 32 at W = 64, 128, 256, 512, 1024
#define IH_NODE(x, y) ((((x) ^ (((y) << 13) | ((y) >> 19))) * IH_MUL) + ((y) >> 7))
#if defined(IGM_IH_V4_FORM) && IGM_IH_V4_FORM >= 1
            if (iw >= 32u) {
                // The Riptide fused forms (main's specification, 10 Oct 2026 15:3x UK): the macro-item loaded straight into
                // registers as 16 bytes per lane per 128-byte step (lane l takes words 32 t + 4 l .. + 3 of step t: the width
                // table's request shape, 405 GB/s at 512 bytes on the 9070 XT), every step issued before any arithmetic (the
                // whole W-byte request in flight per nonce); the tree computed on the loaded layout (two levels local, three
                // through the lane exchange, the steps local: 27 node evaluations per lane at W = 512 against 63 sequentially
                // on the relation's layout); and the absorb (word j into R[(j div 8) mod 16] of lane j mod 8) routed to the
                // relation's lanes: form 1 through the exchange (one exchange per register, the component chosen per lane),
                // form 2 through local memory (one 16-byte store per step, one strided read per register, OpenCL only).
                // The same relation at every width from 128 (the vectors prove it; the identity tool, strata/RIPTIDE.md 2).
                const uint T = iw >> 5u;   // 128-byte steps: 1, 2, 4, 8 at W = 128, 256, 512, 1024
                uint4 q[8];
                __attribute__((opencl_unroll_hint))
                for (uint t = 0; t < T; t++) q[t] = IGM_LOAD4A(it4 + 32u * t + 4u * lane);
#if IGM_IH_KEYED_S > 0u
                if (IGM_KX_LOOP_ON && (r * IGM_IH_KEYED_S) % k->rounds < IGM_IH_KEYED_S) {   // a keyed round (uniform): the share into words 0 .. 7, which lanes 0 and 1 hold as their first uint4
                    uint ksh[8]; uint kk8[8]; for (uint q = 0; q < 8; q++) kk8[q] = k->sk[q];
                    ih_keyed_share(S, R, (r * IGM_IH_KEYED_S) / k->rounds, lane, kk8, IGM_XCH_ARGS, ksh);
                    const uint4 kv = (lane == 0u) ? (uint4)(ksh[0], ksh[1], ksh[2], ksh[3]) : (uint4)(ksh[4], ksh[5], ksh[6], ksh[7]);
                    if (IGM_KX_INJECT && lane < 2u) q[0] ^= kv;
                }
#endif
                if (IH_V8) { wraw8[0] = q[0].x; wraw8[1] = q[0].y; wraw8[2] = q[0].z; wraw8[3] = q[0].w; if (T > 1u) { wraw8[4] = q[1].x; wraw8[5] = q[1].y; wraw8[6] = q[1].z; wraw8[7] = q[1].w; } }   // version 8: words 32 t + 4 lane + c of steps 0 and 1 (the first 64 words)
#ifdef IGM_IH_XFER_DIAG
                // DIAGNOSTIC ONLY (the auditor's matched-resource dependent ceiling, 10 Oct 2026 18:2x UK; the hash wrong by design,
                // never a cell): form 1's loads at the relation's count and the next address from the loaded words through the one
                // broadcast the relation's address takes, with no absorb, tree, cell, exchange or finalisation; the footprint pinned
                // by a kept-alive register array of IGM_IH_XFER_KEEP words folded into the output (the dry run states the match to
                // form1-wave's VGPR count), the LDS allocation the wrapper's own
                {
                    uint f = 0u;
                    for (uint t = 0; t < T; t++) f ^= (q[t].x + q[t].y) ^ (q[t].z + q[t].w) ^ ((t + 1u) * IH_MUL);
                    for (uint i = 0; i < IGM_IH_XFER_KEEP; i++) { xkeep[i] ^= f + (i + 1u) * IH_MUL; }
#if defined(__AMDGCN__)
                    for (uint i = 0; i < IGM_IH_XFER_KEEP; i++) { __asm__ __volatile__("" : "+v"(xkeep[i])); }
#endif
                    R[r & 15u] += f;
                    { uint g = (r + 1u) & 7u; idx = (IGM_BCAST(f, g) ^ R[0]) & k->n_items_mask; }
                    continue;
                }
#endif
#ifdef IGM_IH_TRACE_FULL
                if (trace) for (uint t = 0; t < T; t++) { const uint j0 = 32u * t + 4u * lane; trace[trb + 2u + j0] = q[t].x; trace[trb + 3u + j0] = q[t].y; trace[trb + 4u + j0] = q[t].z; trace[trb + 5u + j0] = q[t].w; }
#endif
#if (IGM_IH_ABL & 1)
                { uint fz = 0u; for (uint t = 0; t < T; t++) fz ^= q[t].x ^ q[t].y ^ q[t].z ^ q[t].w; h4 = IGM_BCAST(fz, r & 7u); }   // ablation: form 1's tree replaced by a one-shuffle fold
#else
                uint zs[8];
#ifdef IGM_IH_V6_HOIST
                // version 6, the hoisted key gather (the Riptide performance seat, 19:0x UK): every step's four keys gathered before any
                // loaded word is consumed, so the sixteen permutes of a W512 round sit under the load latency instead of behind it on the
                // dependent chain (the card's pair read version 6 at 1.19 of the transfer against version 4's 1.007); the cost is the
                // sixteen live key words, which the gfx1201 dry run priced at a residency tier (123 VGPRs against 108); the row decides
                uint4 kq6[8];
                if (IH_V6) {
                    const uint mh6 = lane >> 1u;
                    __attribute__((opencl_unroll_hint))
                    for (uint t = 0; t < T; t++) {
                        uint o0 = R[(4u * t) & 15u], o1 = R[(4u * t + 1u) & 15u], o2 = R[(4u * t + 2u) & 15u], o3 = R[(4u * t + 3u) & 15u];
                        if (lane & 1u) { uint x = o0; o0 = o1; o1 = o2; o2 = o3; o3 = x; }
                        if (lane & 2u) { uint x = o0, y = o1; o0 = o2; o1 = o3; o2 = x; o3 = y; }
                        uint k0 = 0u, k1 = 0u, k2 = 0u, k3 = 0u;
                        for (uint p = 0; p < 4; p++) {
                            const uint offer = (p == 0u) ? o0 : (p == 1u) ? o1 : (p == 2u) ? o2 : o3;
                            const uint c = (mh6 - p) & 3u;
                            const uint got = IGM_BCAST(offer, 4u * (lane & 1u) + c);
                            if (c == 0u) k0 = got; else if (c == 1u) k1 = got; else if (c == 2u) k2 = got; else k3 = got;
                        }
                        kq6[t] = (uint4)(k0, k1, k2, k3);
                    }
                }
#endif
                __attribute__((opencl_unroll_hint))
                for (uint t = 0; t < T; t++) {
                    const uint j0 = 32u * t + 4u * lane;
                    uint z0 = q[t].x ^ ((j0 + 1u) * IH_MUL), z1 = q[t].y ^ ((j0 + 2u) * IH_MUL), z2 = q[t].z ^ ((j0 + 3u) * IH_MUL), z3 = q[t].w ^ ((j0 + 4u) * IH_MUL);
#ifdef IGM_IH_V6_HOIST
                    if (IH_V6) { z0 ^= kq6[t].x; z1 ^= kq6[t].y; z2 ^= kq6[t].z; z3 ^= kq6[t].w; }
#else
                    if (IH_V6) {
                        // version 6: the key of word j is R[j mod 8][(j div 8) mod 16] before the absorb, which lives on the destination
                        // lane 4 (l mod 2) + c in register 4 t + (l div 2); four permutes per step: in pass p every lane offers its register
                        // 4 t + ((p + (lane mod 4)) mod 4) and the reader takes component c = ((lane div 2) - p) mod 4 from lane 4 (lane mod 2) + c,
                        // folded into the leaf as it arrives (no key array), and the step's gathers kept behind the previous step's leaves by a
                        // scheduling barrier so the sixteen keys of a round are never live together (security's TP-139: the residency tier)
                        // The offered register rotates by (lane mod 4): written as two conditional swaps on a copy of the step's quad,
                        // formed in place (the AMD compiler otherwise materialises every step's rotation ahead of the loads: 123 VGPRs and
                        // 8 waves per SIMD against 108 and 9 on the gfx1201 dry run, one residency tier, security's TP-139)
                        const uint mh6 = lane >> 1u;
                        uint o0 = R[(4u * t) & 15u], o1 = R[(4u * t + 1u) & 15u], o2 = R[(4u * t + 2u) & 15u], o3 = R[(4u * t + 3u) & 15u];
                        if (lane & 1u) { uint x = o0; o0 = o1; o1 = o2; o2 = o3; o3 = x; }
                        if (lane & 2u) { uint x = o0, y = o1; o0 = o2; o1 = o3; o2 = x; o3 = y; }
#if defined(__AMDGCN__)
                        __asm__ __volatile__("" : "+v"(o0), "+v"(o1), "+v"(o2), "+v"(o3));
#endif
                        for (uint p = 0; p < 4; p++) {
                            const uint offer = (p == 0u) ? o0 : (p == 1u) ? o1 : (p == 2u) ? o2 : o3;
                            const uint c = (mh6 - p) & 3u;
                            const uint got = IGM_BCAST(offer, 4u * (lane & 1u) + c);
                            if (c == 0u) z0 ^= got; else if (c == 1u) z1 ^= got; else if (c == 2u) z2 ^= got; else z3 ^= got;
                        }
                    }
#endif
                    uint b = IH_NODE(IH_NODE(z0, z1), IH_NODE(z2, z3));
#ifdef IGM_IH_SEL
                    // branchless (the auditor's issue accounting, 10 October 22:0x UK: the gfx1201 loop carried 48 exec branches and
                    // 294 exec-mask scalar ops per wave-round from lane-dependent selects): the pair's operands chosen by two selects,
                    // then one node, instead of two nodes under a lane-dependent choice
                    { uint v1 = IGM_XCHX(b, 1u);  uint lo = (lane & 1u) ? v1 : b;  uint hi = (lane & 1u) ? b : v1;  b = IH_NODE(lo, hi); }
                    { uint v2 = IGM_XCHX(b, 2u);  uint lo = (lane & 2u) ? v2 : b;  uint hi = (lane & 2u) ? b : v2;  b = IH_NODE(lo, hi); }
                    { uint v3 = IGM_XCHX(b, 4u);  uint lo = (lane & 4u) ? v3 : b;  uint hi = (lane & 4u) ? b : v3;  zs[t] = IH_NODE(lo, hi); }
#else
                    uint v1 = IGM_XCHX(b, 1u);  uint a1 = (lane & 1u) ? IH_NODE(v1, b) : IH_NODE(b, v1);
                    uint v2 = IGM_XCHX(a1, 2u); uint a2 = (lane & 2u) ? IH_NODE(v2, a1) : IH_NODE(a1, v2);
                    uint v3 = IGM_XCHX(a2, 4u); zs[t] = (lane & 4u) ? IH_NODE(v3, a2) : IH_NODE(a2, v3);
#endif
                }
                for (uint m = T; m >= 2u; m >>= 1u)
                    IGM_UNROLL(16)
                    for (uint i2 = 0; i2 < (m >> 1u); i2++) zs[i2] = IH_NODE(zs[2u * i2], zs[2u * i2 + 1u]);
                h4 = zs[0];
#endif
#if IGM_IH_V4_FORM == 1
                {
                    // exchange (t, kk): lane d takes from lane (d div 4) + 2 ((kk + d mod 4) mod 4) that lane's word of component
                    // ((source div 2) - kk) mod 4 of step t, which is word j = d mod 8 of register 4 t + ((kk + d mod 4) mod 4)
                    const uint rot = lane & 3u, hf = lane >> 2u, mh = lane >> 1u;   // (half is a reserved type name in OpenCL C: the AMD compiler refuses it, pocl does not)
#ifdef IGM_IH_SEL
                    // branchless routing: the offered component of pass kk is q[(mh - kk) mod 4], so the quad reversed (x, w, z, y)
                    // and rotated right by mh (two conditional rotations, eight selects) gives it as o[kk] with kk a constant; the
                    // gathered word A[(mm - rot) mod 4] goes to register 4 t + mm, so A rotated right by rot (eight selects) gives it as
                    // Ar[mm]. Sixteen v_cndmask per step in place of eight lane-dependent four-way selects.
                    __attribute__((opencl_unroll_hint))
                    for (uint t = 0; t < T; t++) {
                        uint o0 = q[t].x, o1 = q[t].w, o2 = q[t].z, o3 = q[t].y;
                        if (mh & 1u) { uint x = o3; o3 = o2; o2 = o1; o1 = o0; o0 = x; }
                        if (mh & 2u) { uint x = o2, y = o3; o2 = o0; o3 = o1; o0 = x; o1 = y; }
                        uint a0 = IGM_BCAST(o0, hf + 2u * (rot & 3u));
                        uint a1 = IGM_BCAST(o1, hf + 2u * ((1u + rot) & 3u));
                        uint a2 = IGM_BCAST(o2, hf + 2u * ((2u + rot) & 3u));
                        uint a3 = IGM_BCAST(o3, hf + 2u * ((3u + rot) & 3u));
                        if (rot & 1u) { uint x = a3; a3 = a2; a2 = a1; a1 = a0; a0 = x; }
                        if (rot & 2u) { uint x = a2, y = a3; a2 = a0; a3 = a1; a0 = x; a1 = y; }
                        R[(4u * t) & 15u] += a0; R[(4u * t + 1u) & 15u] += a1; R[(4u * t + 2u) & 15u] += a2; R[(4u * t + 3u) & 15u] += a3;
                    }
#else
                    __attribute__((opencl_unroll_hint))
                    for (uint t = 0; t < T; t++) {
                        uint A[4];
                        for (uint kk = 0; kk < 4; kk++) {
                            const uint ci = (mh - kk) & 3u;
                            const uint sv = (ci == 0u) ? q[t].x : (ci == 1u) ? q[t].y : (ci == 2u) ? q[t].z : q[t].w;
                            A[kk] = IGM_BCAST(sv, hf + 2u * ((kk + rot) & 3u));
                        }
                        for (uint mm = 0; mm < 4; mm++) {
                            const uint kk = (mm - rot) & 3u;
                            R[(4u * t + mm) & 15u] += (kk == 0u) ? A[0] : (kk == 1u) ? A[1] : (kk == 2u) ? A[2] : A[3];
                        }
                    }
#endif
                }
#else
                {
                    IGM_LOCAL uint *stg = IGM_STAGE_BASE + (ulong)group * iw;   // this group's W bytes of local memory past the exchange words
                    for (uint t = 0; t < T; t++) IGM_STORE4(q[t], stg + 32u * t + 4u * lane);
                    IGM_LOCAL_SYNC();
                    IGM_UNROLL(16)
                    for (uint s = 0; s < ng; s++) R[s & IH_RWM] += stg[lane + 8u * s];
                    IGM_LOCAL_SYNC();
                }
#endif
            } else
#endif
            {
            uint zs[32];
            if (ng > 16u) {
                // W1024 (the only width where a register takes two words, sg and sg + 16): the two words are absorbed together,
                // both keyed on the register's value as the round begins, read once (the hash lead's pin of 17:0x UK: K is the file
                // before step 1 runs at all); R += wa + wb is the same sum as the two adds in turn. No array of first words: the
                // earlier form kept sixteen of them live beside zs[32] and R[16], the largest live set of any kernel here, and on
                // the M5 Max's compiler (macOS 26.6.2, apple10) that form produced F with a zero first word while M was exact
                // (mb-first-1, mb-or0b-1, 19:1x to 19:3x UK; exact on the M6); the Orchard lead, 19:4x UK.
#if IGM_IH_KEYED_S > 0u
                IGM_KX_KSW_DECL;
                for (uint q = 0; q < 8; q++) kshare[q] = 0u;
                if (IGM_KX_LOOP_ON && (r * IGM_IH_KEYED_S) % k->rounds < IGM_IH_KEYED_S) { uint ksh[8]; uint kk8[8]; for (uint q = 0; q < 8; q++) kk8[q] = k->sk[q]; ih_keyed_share(S, R, (r * IGM_IH_KEYED_S) / k->rounds, lane, kk8, IGM_XCH_ARGS, ksh); IGM_KX_SELECT(ksw, ksh, lane); for (uint q = 0; q < 8; q++) kshare[q] = ksh[q]; }
#endif
                IGM_UNROLL(16)
                for (uint sg = 0; sg < 16u; sg++) {
                    const uint ja = 8u * sg + lane, jb = ja + 128u;
                    uint wa = it4[ja]; const uint wb = it4[jb];
#if IGM_IH_KEYED_S > 0u
                    IGM_KX_INJECT_INTO(wa, sg, ksw);   // word j = lane j's first word
#endif
#if IGM_IH_RW == 16u
                    const uint key = IH_V6 ? R[sg] : 0u;
                    zs[sg] = wa ^ key ^ ((ja + 1u) * IH_MUL);
                    zs[sg + 16u] = wb ^ key ^ ((jb + 1u) * IH_MUL);
                    R[sg] += wa + wb;
#else
                    // the wider state: words sg and sg + 16 land in two registers (word j to register (j div 8) mod RW)
                    const uint key_a = IH_V6 ? R[sg] : 0u, key_b = IH_V6 ? R[sg + 16u] : 0u;
                    zs[sg] = wa ^ key_a ^ ((ja + 1u) * IH_MUL);
                    zs[sg + 16u] = wb ^ key_b ^ ((jb + 1u) * IH_MUL);
                    R[sg] += wa; R[sg + 16u] += wb;
#endif
                    if (IH_V8 && sg < 8u) wraw8[sg] = wa;
#ifdef IGM_IH_TRACE_FULL
                    IH_TR1(true, trb + 2u + ja, wa); IH_TR1(true, trb + 2u + jb, wb);
#endif
                }
            } else {
#if IGM_IH_KEYED_S > 0u
                IGM_KX_KSW_DECL;
                for (uint q = 0; q < 8; q++) kshare[q] = 0u;
                if (IGM_KX_LOOP_ON && (r * IGM_IH_KEYED_S) % k->rounds < IGM_IH_KEYED_S) { uint ksh[8]; uint kk8[8]; for (uint q = 0; q < 8; q++) kk8[q] = k->sk[q]; ih_keyed_share(S, R, (r * IGM_IH_KEYED_S) / k->rounds, lane, kk8, IGM_XCH_ARGS, ksh); IGM_KX_SELECT(ksw, ksh, lane); for (uint q = 0; q < 8; q++) kshare[q] = ksh[q]; }
#endif
            IGM_UNROLL(16)
            for (uint sg = 0; sg < ng; sg++) {
                uint j = 8u * sg + lane, wj = it4[j];
#if IGM_IH_KEYED_S > 0u
                IGM_KX_INJECT_INTO(wj, sg, ksw);
#endif
                // version 6: keyed by the register the word enters, as it stands before step 1 runs at all (the hash lead's pin)
                zs[sg] = wj ^ (IH_V6 ? R[sg & IH_RWM] : 0u) ^ ((j + 1u) * IH_MUL);
                R[sg & IH_RWM] += wj;
                if (IH_V8 && sg < 8u) wraw8[sg] = wj;   // version 8 draw revision 2: the item's first 64 words (lane l holds words l + 8 sg) decode the round's programme
#ifdef IGM_IH_TRACE_FULL
                IH_TR1(true, trb + 2u + j, wj);
#endif
            }
            }
#if (IGM_IH_ABL & 1)
            { uint fz = 0u; for (uint sg = 0; sg < ng; sg++) fz ^= zs[sg]; h4 = IGM_BCAST(fz, r & 7u); }   // ablation: the tree replaced by a one-shuffle fold
#else
            __attribute__((opencl_unroll_hint))
            IGM_UNROLL(16)
            for (uint sg = 0; sg < ng; sg++) {
                uint v1 = IGM_XCHX(zs[sg], 1u); uint a1 = (lane & 1u) ? IH_NODE(v1, zs[sg]) : IH_NODE(zs[sg], v1);
                uint v2 = IGM_XCHX(a1, 2u);     uint a2 = (lane & 2u) ? IH_NODE(v2, a1) : IH_NODE(a1, v2);
                uint v3 = IGM_XCHX(a2, 4u);     zs[sg] = (lane & 4u) ? IH_NODE(v3, a2) : IH_NODE(a2, v3);
            }
            for (uint m = ng; m >= 2u; m >>= 1u)
                IGM_UNROLL(16)
                for (uint i2 = 0; i2 < (m >> 1u); i2++) zs[i2] = IH_NODE(zs[2u * i2], zs[2u * i2 + 1u]);
            h4 = zs[0];
#endif
            }
#if defined(IGM_IH_V8S) && defined(IGM_IH_V8_K)
            if (IH_V8) {
                // The snapshot form of step 1b (the Riptide performance seat, 11 October 2026 02:3x UK; main's order: the Loom programme
                // kernel's per-descriptor cost on the wave form). The generic block below indexes the lane's register arrays by per-lane
                // values (T8[src1], T8[src2]) and walks K at run time (R[d] dynamic), which the AMD compiler can only serve from private
                // memory: 513 scratch ops and 80 bytes of scratch in the W128 version 8 text, 162 VGPRs, 5 waves. Here K, B and F are the
                // document's at build time (the loops unrolled, every R[d] static, every feature test folded), and the per-block snapshot
                // sits in local memory per lane at a conflict-free stride (IGM_IH_RW + 1 words), so each source read is one ds_load at a
                // per-lane address and the cross-lane variants read the loaded value as the generic block does. The same descriptors,
                // the same words, the same operations: the vectors prove it. No barrier is needed: a lane reads only its own slot.
                const uint K8 = IGM_IH_V8_K, F8 = IGM_IH_V8_F, B8 = (F8 & 256u) ? 0u : IGM_IH_V8_B;
                const uint C8 = (F8 & 8u) ? 4u : (F8 & 4u) ? 2u : 1u, M8 = K8 / C8;
                const uint n8 = (iw < 64u) ? iw : 64u;
                // the snapshot as a register copy with constant indices (the first form of 11 October held it in local memory behind
                // a barrier: exact on pocl under 27,371 recomputed hits and on the card's single-nonce vectors, yet 7 to 10 percent of the
                // card's bench hits failed, a card-side fault of that local copy; with K static and the class's slot-fixed sources the
                // copy indexes statically in registers, as the generic block's copy did at 95 VGPRs)
                uint snap[IGM_IH_RW];
                if (F8 & 256u) { for (uint d = 0; d < K8; d++) R[d] ^= 0x9E3779B9u; }
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
                igm_r0e_program(R, lane, IGM_XCH_ARGS, r0e_item, r12_tbl, r12_shared, nonce_off, r0e_tg, kshare IGM_R0E_OPTRACE_CALL);   // versions 12 to 14: the local table, the period's shared table
#elif IGM_IH_VERSION == 10 || IGM_IH_VERSION == 11
                igm_r0e_program(R, lane, IGM_XCH_ARGS, r0e_item, scratch_slice, r0e_tg, kshare);   // R0E: the octet's lock-step programme (the shuffle, the gather table, the tile, the item operand, the fold)
#elif IGM_IH_VERSION == 9
                igm_r0d_program(R);   // R0D (SPEC-R0D-v0 0.3): the period's programme over the lane's sixteen registers, after the absorb and the walk-only constant (the trace's "block" record) and before the tree; the host generates it per period (r0d_gen)
#endif
                __attribute__((opencl_unroll_hint))
                for (uint b8 = 0; b8 < B8; b8++) {
                    __attribute__((opencl_unroll_hint))
                    for (uint q = 0; q < IGM_IH_RW; q++) snap[q] = R[q];
                    const uint wi0 = (b8 * K8) % n8;
                    const uint phase8 = ((IH_PW(wi0) + ((b8 * K8) / n8) * IH_MUL) >> 13) & 3u;
                    __attribute__((opencl_unroll_hint))
                    for (uint d = 0; d < K8; d++) {
                        const uint i8 = b8 * K8 + d, wi = i8 % n8;
                        const uint p8 = IH_PW(wi) + (i8 / n8) * IH_MUL;
                        const uint base = (d / M8) * M8, dd = d % M8;
                        uint src1, src2, shift1 = 0u;
                        if (F8 & 4096u) { src1 = base + ((dd + 1u) & (M8 - 1u)); src2 = base + ((dd + 2u) & (M8 - 1u)); shift1 = p8 & 7u; }
                        else if (F8 & 2048u) { src1 = base + ((dd + 1u + (p8 & 3u)) & (M8 - 1u)); src2 = base + ((dd + 5u + ((p8 >> 4) & 3u)) & (M8 - 1u)); }
                        else { src1 = base + ((dd + 1u + (p8 & 15u)) & (M8 - 1u)); src2 = base + ((dd + 1u + ((p8 >> 4) & 15u)) & (M8 - 1u)); }
                        const uint rot = (p8 >> 8) & 31u; uint opc = (d + phase8) & 3u;
                        const uint pbit = (p8 >> 15) & 31u, salt = p8 >> 24;
                        const uint shift = (F8 & 4096u) ? ((p8 >> 4) & 7u) : (((p8 >> 20) & 7u) | 1u);
                        const uint t1 = snap[src1], t2 = snap[src2], td = snap[d];
                        uint y8 = (F8 & 4096u) ? IGM_BCAST(t1, (lane + shift1) & 7u) : t1;
                        uint ra8 = 0u;
                        if (F8 & 1u) { ra8 = (t2 ^ (salt * IH_MUL)) % k->slice_words; y8 ^= scratch_slice[ra8]; }
                        if (F8 & 2u) { if ((y8 >> pbit) & 1u) opc = (opc + 1u) & 3u; }
                        const uint z8 = (F8 & (128u | 4096u)) ? IGM_BCAST(t2, (lane + shift) & 7u) : t2;
                        uint out8;
                        if (F8 & 32u) {
                            // the four binary32 classes evaluated and one selected (the opcode is the group's phase plus d, data-dependent
                            // per group, so the generic helper's branch is an exec-masked four-way dispatch: 121 branches per round loop);
                            // the same roundings as ih_v8_fop_wide, the inner operation first
                            const uint yb = (y8 & 0x3FFFFFFFu) | 0x20000000u, zb = (z8 & 0x3FFFFFFFu) | 0x20000000u, xb = (td & 0x3FFFFFFFu) | 0x20000000u;
                            const float yf = IGM_U2F(yb), zf = IGM_U2F(zb), xf = IGM_U2F(xb);
                            const float f0 = xf + (yf - zf), f1 = xf - (yf + zf), f2 = (yf * zf) + xf, f3 = (xf - yf) + zf;
                            const float fs = (opc == 0u) ? f0 : (opc == 1u) ? f1 : (opc == 2u) ? f2 : f3;
                            out8 = IGM_F2U(fs) ^ td;
                        } else out8 = ((F8 & 16u) && opc == 2u) ? ih_v8_fop(td, y8, z8) : ih_v8_op(opc, td, y8, z8, rot);
                        if (F8 & 1u) scratch_slice[(ra8 + 1u + (out8 % (k->slice_words - 1u))) % k->slice_words] = out8 ^ td;
                        R[d] = out8;
                    }
                }
            }
#else
            if (IH_V8) {
                // step 1b (SPEC 0.8, draw revision 2, main e87f491d): B blocks of K descriptors on registers 0 .. K - 1 of every lane,
                // snapshot semantics per block, the same descriptors on every lane. Descriptor i = b K + d takes its programme word
                // p = W[i mod n] + (i div n) MUL from the round's item words (n = the item's words, at most 32 used: i mod 32), decoded
                // by fixed bit fields q[k] = byte k of p under schema 2's rules; no hash, no programme seed. Bit 7 (features 128): the
                // second source from lane (l + shift) mod 8 of the snapshot, shift = 1 + ((p >> 26) mod 7). Bit 8 (256): walk-only, the
                // block's output a constant; bit 9 (512): programme-only, the item from the lane state (the host's mode, not here).
                const uint K8 = k->registers, F8 = k->features, B8 = (F8 & 256u) ? 0u : k->blocks;
                const uint C8 = (F8 & 8u) ? 4u : (F8 & 4u) ? 2u : 1u, M8 = K8 / C8;
                const uint n8 = (iw < 64u) ? iw : 64u;   // the programme words the decode walks (W64: 16; W128: 32; W256 and above: 64)
                if (F8 & 256u) { for (uint d = 0; d < K8; d++) R[d] ^= 0x9E3779B9u; }   // walk-only: the constant standing in for the block's output
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
                igm_r0e_program(R, lane, IGM_XCH_ARGS, r0e_item, r12_tbl, r12_shared, nonce_off, r0e_tg, kshare IGM_R0E_OPTRACE_CALL);   // versions 12 to 14: the local table, the period's shared table
#elif IGM_IH_VERSION == 10 || IGM_IH_VERSION == 11
                igm_r0e_program(R, lane, IGM_XCH_ARGS, r0e_item, scratch_slice, r0e_tg, kshare);   // R0E: the octet's lock-step programme (the shuffle, the gather table, the tile, the item operand, the fold)
#elif IGM_IH_VERSION == 9
                igm_r0d_program(R);   // R0D (SPEC-R0D-v0 0.3): the period's programme over the lane's sixteen registers, after the absorb and the walk-only constant (the trace's "block" record) and before the tree; the host generates it per period (r0d_gen)
#endif
                for (uint b8 = 0; b8 < B8; b8++) {
                    uint T8[IGM_IH_RW]; for (uint q = 0; q < IGM_IH_RW; q++) T8[q] = R[q];   // the snapshot over the whole state (K = RW under the wider state)
                    // revision 2b (the auditor's verify ruling, main's 23:49 UK order): the opcode class counts are fixed per item: the
                    // block's phase from its first programme word, opcode(d) = (d + phase) & 3, so every block runs K / 4 of each class
                    const uint wi0 = (b8 * K8) % n8;
                    const uint phase8 = ((IH_PW(wi0) + ((b8 * K8) / n8) * IH_MUL) >> 13) & 3u;
                    for (uint d = 0; d < K8; d++) {
                        const uint i8 = b8 * K8 + d, wi = i8 % n8;
                        const uint p8 = IH_PW(wi) + (i8 / n8) * IH_MUL;   // W[i mod n] from the lane that holds it
                        // the aligned decode (SPEC 0.8 at 53235aca, then 6d73b612): every field one extract of p; the forms of revision 2c:
                        // bit 12, form (b): slot-fixed sources d + 1 and d + 2 in the chain, BOTH read cross-lane from item-chosen lanes
                        // (shift1 = p & 7 for the first, shift = (p >> 4) & 7 for the second, 0 the own lane; reference c4e62414);
                        // bit 11, form (a): a narrow select among four fixed candidates per source; else the full 4-bit offsets
                        const uint base = (d / M8) * M8, dd = d % M8;
                        uint src1, src2, shift1 = 0u;
                        if (F8 & 4096u) { src1 = base + ((dd + 1u) & (M8 - 1u)); src2 = base + ((dd + 2u) & (M8 - 1u)); shift1 = p8 & 7u; }
                        else if (F8 & 2048u) { src1 = base + ((dd + 1u + (p8 & 3u)) & (M8 - 1u)); src2 = base + ((dd + 5u + ((p8 >> 4) & 3u)) & (M8 - 1u)); }
                        else { src1 = base + ((dd + 1u + (p8 & 15u)) & (M8 - 1u)); src2 = base + ((dd + 1u + ((p8 >> 4) & 15u)) & (M8 - 1u)); }
                        const uint rot = (p8 >> 8) & 31u; uint opc = (d + phase8) & 3u;
                        const uint pbit = (p8 >> 15) & 31u, salt = p8 >> 24;
                        const uint shift = (F8 & 4096u) ? ((p8 >> 4) & 7u) : (((p8 >> 20) & 7u) | 1u);   // bit 12: the second source's lane from bits 4..6 (0 the own lane, SPEC form (b), reference c4e62414); bit 7: the odd offset from bits 20..22
                        uint y8 = (F8 & 4096u) ? IGM_BCAST(T8[src1], (lane + shift1) & 7u) : T8[src1];   // bit 12: the first source shuffled too
                        uint ra8 = 0u;
                        if (F8 & 1u) { ra8 = (T8[src2] ^ (salt * IH_MUL)) % k->slice_words; y8 ^= scratch_slice[ra8]; }
                        if (F8 & 2u) { if ((y8 >> pbit) & 1u) opc = (opc + 1u) & 3u; }   // the predicate on the first operand as read
                        const uint z8 = (F8 & (128u | 4096u)) ? IGM_BCAST(T8[src2], (lane + shift) & 7u) : T8[src2];   // bits 7 and 12: the second source from another lane of the snapshot
                        const uint out8 = (F8 & 32u) ? ih_v8_fop_wide(opc, T8[d], y8, z8) : ((F8 & 16u) && opc == 2u) ? ih_v8_fop(T8[d], y8, z8) : ih_v8_op(opc, T8[d], y8, z8, rot);
                        if (F8 & 1u) scratch_slice[(ra8 + 1u + (out8 % (k->slice_words - 1u))) % k->slice_words] = out8 ^ T8[d];
                        R[d] = out8;
                    }
                }
            }
#endif
#undef IH_NODE
            if (IH_V6) { h4 ^= h4 >> 16; h4 *= IH_MUL; h4 ^= h4 >> 15; }   // version 6: the root finalised
#ifdef IGM_IH_TRACE_FULL
            IH_TR1(lane == 0u, trb + 1u, h4);
#endif
#if (IGM_IH_ABL & 2)
            R[0] ^= h4;   // ablation: the cell and the permutation skipped, h consumed
            if (false)
#endif
            IGM_UNROLL(4)
            for (uint kk = 0; kk < IGM_IH_RW / 4u; kk++) {   // the cell over every quad of the lane's registers (4; 8 or 16 under the wider state)
                uint a = R[4u * kk], b = R[4u * kk + 1u], c = R[4u * kk + 2u], d = R[4u * kk + 3u], wv = h4;
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
#if !(IGM_IH_ABL & 2)
            { uint T[IGM_IH_RW]; for (uint q = 0; q < IGM_IH_RW; q++) T[q] = R[q]; for (uint q = 0; q < IGM_IH_RW; q++) R[q] = T[(5u * q + 1u) & IH_RWM]; }   // 5 is coprime to 16, 32 and 64
#endif
        } else {
#ifdef IGM_IH_SPLIT2
        // T-B8 (the allocation test): two buffers of half the dataset; the index's top bit selects the buffer
        IGM_GLOBAL const uint *it = ((idx > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idx - (k->n_items_mask >> 1u) - 1u) * iw : dataset + (ulong)idx * iw) + lw * lane;
#else
        IGM_GLOBAL const uint *it = dataset + (ulong)ih_slot(idx, k->n_items_mask) * iw + lw * lane;
#endif
#ifdef IGM_IH_DUPLEX
        {
            // R1 Thermal Duplex (the auditor's deep-research item, main's order of 23:03 UK; DIAGNOSTIC rows, the hash wrong by
            // design): 1 memory-only, the dependent random read of the item with a write-back of one word (a read-write stream
            // over the dataset, 4 GiB in the job); 2 compute-only, sixteen register-resident multiply-rotate-xor steps with a
            // permutation through the exchange per round, no load; 3 sequential, the load then the compute seeded from the loaded
            // word (the compute waits for the load) then the write-back; 4 simultaneous, the same load and the same compute with
            // the compute seeded before the load (the compiler may overlap them), then the write-back: the same useful work as 3.
#if IGM_IH_DUPLEX != 2
            uint D0 = 0u, D1 = 0u, D2 = 0u, D3 = 0u;
#if IGM_IH_DUPLEX == 5
            if ((nonce_lo & 1u) == 0u)
#endif
            { D0 = it[0]; D1 = it[1]; D2 = it[2]; D3 = it[3]; }
#endif
#if IGM_IH_DUPLEX == 3
            uint cx = R[4] ^ D0 ^ (r * IH_MUL);
#else
            uint cx = R[4] ^ (r * IH_MUL);
#endif
#if IGM_IH_DUPLEX == 5
            // simultaneous, half the groups each (the CUDA probe's form): even nonces run the memory unit, odd nonces the compute unit
            const bool mem_half = (nonce_lo & 1u) == 0u;
#endif
#if IGM_IH_DUPLEX != 1
            // the compute unit at the CUDA probe's shape: 4 chains x 64 passes of multiply-rotate-xor plus one permutation of the
            // sixteen registers through the exchange
#if IGM_IH_DUPLEX == 5
            if (!mem_half)
#endif
            {
                uint x0 = cx, x1 = cx ^ R[5], x2 = cx ^ R[6], x3 = cx ^ R[7];
                for (uint d = 0; d < 64u; d++) {
                    uint m0 = x0 * IH_MUL, m1 = x1 * IH_MUL, m2 = x2 * IH_MUL, m3 = x3 * IH_MUL;
                    x0 = ((m0 << 13) | (m0 >> 19)) ^ R[8]; x1 = ((m1 << 13) | (m1 >> 19)) ^ R[9]; x2 = ((m2 << 13) | (m2 >> 19)) ^ R[10]; x3 = ((m3 << 13) | (m3 >> 19)) ^ R[11];
                }
                cx = x0 ^ x1 ^ x2 ^ x3;
                for (uint s = 0; s < 16u; s++) R[s] = R[s] + IGM_BCAST(cx ^ R[(s + 1u) & 15u], (lane + s) & 7u);
            }
#endif
#if IGM_IH_DUPLEX != 2
            R[0] ^= D0; R[1] += D1; R[2] ^= D2; R[3] += D3;
#if IGM_IH_DUPLEX == 5
            if (mem_half)
#endif
            ((IGM_GLOBAL uint *)it)[0] = D0 ^ R[0] ^ cx;   // the write-back of the item's first word on this lane (the stream is read-write)
            { uint f = D0 ^ D1 ^ D2 ^ D3 ^ cx; uint g = (r + 1u) & 7u; idx = IGM_BCAST(f, g) & k->n_items_mask; }
#else
            idx = (idx ^ ((r + 1u) * IH_MUL) ^ cx) & k->n_items_mask;   // no load: the address is computed and unused
#endif
            continue;
        }
#endif
#if (defined(IGM_IH_DIAG_A) || defined(IGM_IH_DIAG_B)) && defined(IGM_IH_DIAG_WIDTH) && IGM_IH_DIAG_WIDTH == 64u
        // the 64-byte request: lanes 0 to 3 read the first 64 bytes of the item, lanes 4 to 7 read nothing
        uint D0 = 0u, D1 = 0u, D2 = 0u, D3 = 0u;
        if (lane < 4u) { D0 = it[0]; D1 = it[1]; D2 = it[2]; D3 = it[3]; }
#elif defined(IGM_IH_DIAG_SG)
#ifndef IGM_SGBCAST
#error "the SIMD-group-wide macro-item loader (diag-sg) is Metal only: the OpenCL prelude has no group shuffle"
#endif
        // Orchard workstream 2 (ORCHARD.md; the Orchard lane, 10 Oct 2026): one macro-item per SIMD group per load instruction.
        // The four nonces of a 32-wide SIMD group take turns: at step g every lane L of the group loads words 4 L .. 4 L + 3 of
        // nonce g's W-byte item (W / 16 lanes active: 8 at W128, 16 at W256, all 32 at W512, so at W512 one instruction is one
        // whole contiguous 512-byte item, against the stock diag shape of four nonces' 128-byte lines per instruction). The
        // bytes per nonce per round are the same W as diag-a / diag-b at that width; the row asks the memory system whether
        // fewer, wider contiguous requests per instruction change the ceiling. Form B: nonce g's next address comes from the
        // first words of its own item (lane 0 of the group holds them; four shuffles route each nonce its own fold).
        // IGM_IH_DIAG_SG_PAIR (ORCHARD-v1.0 OR-2, two macro-items per SIMD group): at W128 and W256 the group's lanes split in two
        // halves of W/16 lanes and one load instruction carries two nonces' whole items (two steps instead of four); at W512 one
        // item needs all 32 lanes and the pair form is the single form. IGM_IH_DIAG_SG_TG (the threadgroup-memory control): each
        // lane's 16 bytes go through threadgroup memory (a store, a SIMD-group barrier, a load back) before the fold, so the row
        // prices the staging a cooperative loader would pay against direct register consumption.
        uint D0 = 0u, D1 = 0u, D2 = 0u, D3 = 0u, acc_sg = 0u;
        {
            const uint sgl = simd_base + lane;   // this lane's index in the SIMD group (0 .. 31)
            const uint lpi = IGM_IH_DIAG_WIDTH / 16u;   // lanes per item: 8, 16, 32
#ifdef IGM_IH_DIAG_SG_PAIR
            const uint per = (2u * lpi <= IGM_SG_LANES) ? 2u : 1u;   // items per instruction
#else
            const uint per = 1u;
#endif
            const uint slot = (per == 2u && sgl >= lpi) ? 1u : 0u;
            uint fs0 = 0u, fs1 = 0u, fs2 = 0u, fs3 = 0u;
            // W1024 needs 64 lanes of 16 bytes per item and a SIMD group has 32: the item is taken in halves (the first
            // form fetched the first 512 bytes only and the row's logical bytes read double; the M6 operator, 22:0x UK)
            const uint halves = (lpi > IGM_SG_LANES) ? lpi / IGM_SG_LANES : 1u;
            const uint lpi_h = (lpi > IGM_SG_LANES) ? IGM_SG_LANES : lpi;
            for (uint p = 0; p < 4u / per; p++) {
                const uint g = per * p + slot;
                const uint idx_g = IGM_SGBCAST(idx, 8u * g);   // nonce g's W-aligned item index (every nonce's idx is W-aligned)
                for (uint hh = 0; hh < halves; hh++)
                if (sgl < lpi_h * per) {
                    uint4 q = IGM_LOAD4(dataset + (ulong)idx_g * 32u + 4u * (sgl - slot * lpi_h) + hh * (4u * IGM_SG_LANES));
#ifdef IGM_IH_DIAG_SG_TG
                    tgm[4u * sgl] = q.x; tgm[4u * sgl + 1u] = q.y; tgm[4u * sgl + 2u] = q.z; tgm[4u * sgl + 3u] = q.w;
                    simdgroup_barrier(mem_flags::mem_threadgroup);
                    q.x = tgm[4u * sgl]; q.y = tgm[4u * sgl + 1u]; q.z = tgm[4u * sgl + 2u]; q.w = tgm[4u * sgl + 3u];
#endif
                    const uint f = q.x ^ q.y ^ q.z ^ q.w;
                    acc_sg ^= q.x + q.y + q.z + q.w;
                    if (g == 0u) fs0 = f; else if (g == 1u) fs1 = f; else if (g == 2u) fs2 = f; else fs3 = f;
                }
            }
            const uint mine = simd_base >> 3u;
            const uint odd = (per == 2u) ? lpi : 0u;   // where an odd nonce's fold sits under the pair form
            const uint v0 = IGM_SGBCAST(fs0, 0u), v1 = IGM_SGBCAST(fs1, odd), v2 = IGM_SGBCAST(fs2, 0u), v3 = IGM_SGBCAST(fs3, odd);
            D0 = (mine == 0u) ? v0 : (mine == 1u) ? v1 : (mine == 2u) ? v2 : v3;
        }
#elif defined(IGM_IH_LOAD4)
        uint4 dq = IGM_LOAD4(it);   // T-B2: one 16-byte load per lane
        uint D0 = dq.x, D1 = dq.y, D2 = dq.z, D3 = dq.w;
#else
        uint D0 = it[0], D1 = it[1], D2 = it[2], D3 = it[3];
#endif
#ifdef IGM_IH_TRACE_FULL
        if (trace) { trace[trb + 2u + lw * lane] = D0; trace[trb + 3u + lw * lane] = D1; trace[trb + 4u + lw * lane] = D2; trace[trb + 5u + lw * lane] = D3; }
#endif
#if defined(IGM_IH_DIAG_A) || defined(IGM_IH_DIAG_B)
        // DIAGNOSTIC ONLY (never a cell: the hash is wrong on purpose; the project's ceiling order, 10 Oct 2026 12:1x UK):
        // v1's request size (128 bytes, eight lanes by 16), dataset size and address distribution, with the mix, the
        // scratch access and the exchange removed so the row measures the memory system alone.
        //   form A: every address independent of the data read (the nonce's first address walked by a round constant):
        //           the memory system's attainable ceiling for this pattern;
        //   form B: every address from the previous read's words (a 64-deep dependent chain per nonce, many nonces in
        //           flight): the latency-hiding ceiling. Form C is v1 itself (no define).
        R[0] ^= D0; R[1] += D1; R[2] ^= D2; R[3] += D3;   // the loads are consumed so the compiler keeps them
#ifndef IGM_IH_DIAG_WIDTH
#define IGM_IH_DIAG_WIDTH 128u
#endif
        uint acc = 0u;
#ifdef IGM_IH_DIAG_SG
        acc ^= acc_sg; R[4] ^= acc;   // the whole-item loads consumed (the fold feeds form B's address through D0)
#elif IGM_IH_DIAG_WIDTH > 128u
        // the request-width sweep (main, 10 Oct 12:4x UK; the Strata contract: both ceilings at every width): the same
        // address distribution, the request widened to IGM_IH_DIAG_WIDTH bytes (256 to 4096) as one W-aligned item; the
        // eight lanes read it in W/128 coalesced 128-byte steps (lane l takes words 4 l .. 4 l + 3 of each step), so every
        // load instruction touches one 128-byte line; the fold of the span is consumed, and under form B it feeds the address
        {
#if defined(IGM_IH_DIAG_A_SPLIT)
            // the Apple auditor's one-variable test (11 Oct 2026 04:3x UK, on the M5 Max's W128 request-rate term): the same bytes per
            // round as the W-wide request, issued as W / 128 independent 128-byte requests, the first the item's own (unaligned, as at
            // W128) and the rest at hash-derived addresses over the whole dataset; against the one W-byte request it splits the fabric's
            // byte rate from the GPU's issue of 128-byte requests. DIAGNOSTIC ONLY; 2 GiB and under (one buffer).
            for (uint j = 1u; j < IGM_IH_DIAG_WIDTH / 128u; j++) {
                uint x = idx + j * 0x9E3779B1u + (simd_base >> 3u) * 0x632BE59Bu; x ^= x >> 16; x *= 0x7FEB352Du; x ^= x >> 15; x *= 0x846CA68Bu; x ^= x >> 16;
                IGM_GLOBAL const uint *q = dataset + (ulong)(x & k->n_items_mask) * 32u + 4u * lane;
                acc ^= q[0] + q[1] + q[2] + q[3];
            }
            R[4] ^= acc;
#else
#ifdef IGM_IH_SPLIT2
            // above 2 GiB the dataset sits in two buffers (T-B8): the wide request's item is addressed in the buffer that holds it (the
            // raw index past the first buffer read garbage on the 4 GiB W512 ceiling row, 10 Oct 2026 23:4x UK: 8101 GB/s, accepted 0)
            const uint idxw = idx & ~(IGM_IH_DIAG_WIDTH / 128u - 1u);
            IGM_GLOBAL const uint *wb = ((idxw > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idxw - (k->n_items_mask >> 1u) - 1u) * 32u : dataset + (ulong)idxw * 32u) + 4u * lane;
#else
            IGM_GLOBAL const uint *wb = dataset + (((ulong)idx * 32u) & ~((ulong)IGM_IH_DIAG_WIDTH / 4u - 1u)) + 4u * lane;
#endif
            for (uint j = 1u; j < IGM_IH_DIAG_WIDTH / 128u; j++) { IGM_GLOBAL const uint *q = wb + 32u * j; acc ^= q[0] + q[1] + q[2] + q[3]; }
            R[4] ^= acc;
#endif
        }
#endif
#if defined(IGM_IH_DIAG_B) && defined(IGM_IH_DIAG_CHAINS) && IGM_IH_DIAG_CHAINS >= 2
        {   // the second chain's request this round (the same width and shape as the first), its fold consumed, its next address its own
            IGM_GLOBAL const uint *it2 = dataset + (ulong)ih_slot(idx2, k->n_items_mask) * iw + lw * lane;
            uint E0 = it2[0], E1 = it2[1], E2 = it2[2], E3 = it2[3]; uint acc2 = 0u;
#if IGM_IH_DIAG_WIDTH > 128u
#ifdef IGM_IH_SPLIT2
            const uint idxw2 = idx2 & ~(IGM_IH_DIAG_WIDTH / 128u - 1u);
            IGM_GLOBAL const uint *wb2 = ((idxw2 > (k->n_items_mask >> 1u)) ? dataset2 + (ulong)(idxw2 - (k->n_items_mask >> 1u) - 1u) * 32u : dataset + (ulong)idxw2 * 32u) + 4u * lane;
#else
            IGM_GLOBAL const uint *wb2 = dataset + (((ulong)idx2 * 32u) & ~((ulong)IGM_IH_DIAG_WIDTH / 4u - 1u)) + 4u * lane;
#endif
            for (uint j = 1u; j < IGM_IH_DIAG_WIDTH / 128u; j++) { IGM_GLOBAL const uint *q = wb2 + 32u * j; acc2 ^= q[0] + q[1] + q[2] + q[3]; }
#endif
            const uint f2 = E0 ^ E1 ^ E2 ^ E3 ^ acc2; R[5] ^= f2;
            { uint g2 = (r + 3u) & 7u; idx2 = IGM_BCAST(f2, g2) & k->n_items_mask; }
#if IGM_IH_DIAG_WIDTH > 128u
            idx2 &= ~(IGM_IH_DIAG_WIDTH / 128u - 1u);
#endif
        }
#endif
#ifdef IGM_IH_DIAG_A
#ifdef IGM_IH_DIAG_A_ORDERED
        // the auditor's matched-concurrency test (20:4x UK): the walk's address, independent of the data in value, but not
        // issued before this round's load returns: a select on the loaded line the compiler must honour (the address moves
        // by one only if the line's fold equals a constant, probability 2^-32 per round), so one request is in flight per
        // nonce as in form B, and the difference to form A is the loads a nonce may have in flight across rounds
        { const uint f = D0 ^ D1 ^ D2 ^ D3 ^ acc; idx = ((idx0 ^ ((r + 1u) * 0x9E3779B1u)) ^ ((f == 0x9E3779B1u) ? 1u : 0u)) & k->n_items_mask; }
#elif defined(IGM_IH_DIAG_A_RAND)
        // the auditor's test (20:2x UK): the independent form's address a hash of the nonce's start index, the round and the lane
        // group (random over the dataset, independent of the loaded data), against the structured walk below (the same xor
        // stride for every nonce, which an interleaved multi-channel memory may serve differently from hash-derived addresses)
        { uint x = idx0 + (r + 1u) * 0x9E3779B1u + (simd_base >> 3u) * 0x632BE59Bu; x ^= x >> 16; x *= 0x7FEB352Du; x ^= x >> 15; x *= 0x846CA68Bu; x ^= x >> 16; idx = x & k->n_items_mask; }
#else
        idx = (idx0 ^ ((r + 1u) * 0x9E3779B1u)) & k->n_items_mask;
#endif
#else
        { uint f = D0 ^ D1 ^ D2 ^ D3 ^ acc; uint g = (r + 1u) & 7u; idx = IGM_BCAST(f, g) & k->n_items_mask; }
#endif
#if IGM_IH_DIAG_WIDTH > 128u && !defined(IGM_IH_DIAG_A_SPLIT)
        idx &= ~(IGM_IH_DIAG_WIDTH / 128u - 1u);
#endif
        continue;
#endif
        for (uint sb = 0; sb < k->sub_blocks; sb++) {
        if (sb != 0u) { D0 = it[4u * sb]; D1 = it[4u * sb + 1u]; D2 = it[4u * sb + 2u]; D3 = it[4u * sb + 3u];
#ifdef IGM_IH_TRACE_FULL
            if (trace) { trace[trb + 2u + lw * lane + 4u * sb] = D0; trace[trb + 3u + lw * lane + 4u * sb] = D1; trace[trb + 4u + lw * lane + 4u * sb] = D2; trace[trb + 5u + lw * lane + 4u * sb] = D3; }
#endif
        }
        uint Dk[4] = {D0, D1, D2, D3};
        for (uint kk = 0; kk < 4; kk++) {
            uint a = R[4u * kk], b = R[4u * kk + 1u], c = R[4u * kk + 2u], d = R[4u * kk + 3u], wv = Dk[kk];
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
        }   // sub-blocks
        }   // version 4 / the stock round
        // 4.5 scratch access on the own slice
        if (pad) {
            uint line = R[0] & k->slice_lines_mask;
            for (uint j = 0; j < 4; j++) {
                uint s = scratch_slice[4u * line + j];
                uint x = R[8u + j] ^ s;
                R[8u + j] = ((x << 5) | (x >> 27)) + R[j];
                scratch_slice[4u * line + j] = (s + R[12u + j]) ^ R[8u + j];
            }
        }
        // exchange with snapshot semantics
#if !(IGM_IH_ABL & 4)
        {
            uint p = lane ^ (1u << (r % 3u));
            uint q = (lane + 1u + (R[1] % 7u)) & 7u;
            uint xp0 = IGM_BCAST(R[0], p), xp2 = IGM_BCAST(R[2], p);
            uint xq1 = IGM_BCAST(R[1], q), xq3 = IGM_BCAST(R[3], q);
            uint n0 = R[0] + xp0, n1 = R[1] ^ xq1, n2 = R[2] + ((xp2 << 11) | (xp2 >> 21)), n3 = R[3] ^ ((xq3 << 23) | (xq3 >> 9));
            R[0] = n0; R[1] = n1; R[2] = n2; R[3] = n3;
        }
#endif
        // 4.6 next address from the rotating leader (version 4: XOR the item word h)
#if (IGM_IH_ABL & 8)
        idx = (h4 ^ ((r + 1u) * IH_MUL)) & k->n_items_mask;   // ablation: the address from the item word alone, the register chain cut (the memory chain kept)
#else
        { uint g = (r + 1u) & 7u; idx = ih_map(IGM_BCAST(R[0], g) ^ h4, k); }
#endif
#ifdef IGM_IH_REACH_MASK
        idx &= reach_mask;
#endif
        IH_TR(r < 2u, 296u + 136u * r + 16u * lane, 16u, R);
#ifdef IGM_IH_TRACE_FULL
        IH_TR(true, trb + 2u + iw + 16u * lane, 16u, R);
#endif
    }
    // 4.7 fold of the own slice (absent without a scratchpad)
    uint f[4] = {0u, 0u, 0u, 0u};
    if (pad) {
        for (uint i = 0; i < (k->slice_lines_mask + 1u); i++)
            for (uint j = 0; j < 4; j++) { uint x = f[j] ^ scratch_slice[4u * i + j]; f[j] = ((x << 9) | (x >> 23)) + scratch_slice[4u * i + ((j + 1u) & 3u)]; }
    }
#ifdef IGM_IH_XFER_DIAG
    // the diagnostic's output: the registers and the kept array folded, no BLAKE3 (wrong by design)
    for (uint i = 0; i < 8; i++) { uint v = R[i] ^ R[8u + i]; for (uint j = i; j < IGM_IH_XFER_KEEP; j += 8u) v += xkeep[j]; mix_out8[i] = v; final8[i] = v * IH_MUL ^ S[i]; }
    (void)st_area; (void)pad;
    return;
#endif
#ifdef IGM_IH_STREAM_FINAL
    // M = BLAKE3(tag(T_MIX) || S || ST) with ST the 512 register bytes (scratch 0 only: the host sets this form only
    // then): 563 bytes, nine blocks; the state words gathered from the lanes by the exchange as the stream needs them
    // (word 16 l + q of ST is R[q] of lane l), no state area in global memory, every lane computing the same M (an
    // instruction issues once per wave whether one lane or eight take it, so the redundancy is free and there is no
    // divergence); byte 51 is where ST starts, so stream word 13 + x is (ST[x] >> 8) | (ST[x + 1] << 24).
    IH_TR(true, 576u + 4u * lane, 4u, f);
#ifdef IGM_IH_LEAN
    // the lean walk (the auditor's "stream the finaliser's temporaries", 10 October 21:4x UK): the eight S words the finaliser
    // reads are not carried through the sixteen rounds but recomputed here from the header and the nonce (one compression per
    // nonce, the same bytes: the vectors prove it); the register pin on the nonce keeps the AMD compiler from merging the two
    // calls into one live S, which is the whole point. The form1-wave tier was 104 VGPRs (12 waves); this is the route to 96 (16).
    {
        uint nl = nonce_lo, nh = nonce_hi;
#if defined(__AMDGCN__)
        __asm__ __volatile__("" : "+v"(nl), "+v"(nh));
#endif
        ih_stream_S(k, hdr8, nh, nl, S);
    }
#endif
    uint M[16];
    {
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16], o[16];
        // block 0: the tag, S from byte 19, ST from byte 51 (stream word 12 holds S[7]'s top three bytes and ST[0]'s low byte)
        IH_MIX_HEAD(m, S);
        uint prev = IGM_BCAST(R[0], 0u);   // ST[0]
        m[12] = (S[7] >> 8) | (prev << 24);
        for (uint i = 13; i < 16; i++) { uint stn = IGM_BCAST(R[i - 12u], 0u); m[i] = (prev >> 8) | (stn << 24); prev = stn; }   // ST[1..3]
        ih_b3_compress(cv, m, 0u, 64u, IH_CHUNK_START, o);
        for (uint i = 0; i < 8; i++) cv[i] = o[i];
        // blocks 1 to 8: stream word 16 b + i carries ST[16 b + i - 13] (in prev) and ST[16 b + i - 12], which is register
        // (i + 4) mod 16 of lane b - 1 (i < 12) or lane b (i >= 12): the register index is static whatever the compiler
        // does with the block loop; ST[128] (block 8, i = 12 on) is past the end and zero; the last block is 51 bytes
#if IGM_IH_RW == 16u
        for (uint b = 1; b < 9; b++) {
            __attribute__((opencl_unroll_hint))   /* the bare hint: a full unroll of this sixteen-step loop, the same as (16), and the form the MSL prelude defines away (the M6 refused "(16)" on stream-final at W1024, 19:57 UK) */
            for (uint i = 0; i < 16; i++) {
                const uint sx = 16u * b + i - 12u;
                uint stn = (sx < 128u) ? IGM_BCAST(R[(i + 4u) & 15u], (i < 12u) ? b - 1u : b) : 0u;
                m[i] = (prev >> 8) | (stn << 24); prev = stn;
            }
            const uint flags = (b == 8u) ? (IH_CHUNK_END | IH_ROOT) : 0u;
            ih_b3_compress(cv, m, 0u, b == 8u ? 51u : 64u, flags, o);
            if (b < 8u) for (uint i = 0; i < 8; i++) cv[i] = o[i];
        }
#else
        // the wider state: ST is 8 RW words (word RW l + q is R[q] of lane l), 51 + 32 RW bytes in RW / 2 + 1 blocks, the last of
        // 51 bytes: 1,075 bytes at RW 32 (two BLAKE3 chunks), 2,099 at RW 64 (three); the standard tree (the hash lead, 01:54 UK):
        // chunk c is blocks 16 c .. 16 c + 15 under counter c (CHUNK_START on its first, CHUNK_END on its last, never ROOT), its
        // chaining value the first eight words of its last block; two chunks: the root is the parent of cv0 || cv1 (PARENT | ROOT);
        // three: the parent of cv0 || cv1 without ROOT, then the root parent of that || cv2. Both loops unrolled so every register
        // index is a constant (the block index b decides the lane and the register). Block 0 above opened chunk 0 under CHUNK_START.
        // Written by the shared body's rule (the Orchard lead's second apple10 instance, 02:3x UK): one fixed array per chunk, no
        // private array indexed by a runtime value, no conditional expression in the hash routine, the tree by if/else.
        uint cv0[8], cv1[8], cv2[8];
        for (uint i = 0; i < 8; i++) { cv0[i] = 0u; cv1[i] = 0u; cv2[i] = 0u; }
        __attribute__((opencl_unroll_hint))
        for (uint b = 1; b < IGM_IH_RW / 2u + 1u; b++) {
            const uint chunk = b >> 4u, bi = b & 15u;
            if (bi == 0u) {   // the chunk before closed on its block 15: keep its chaining value, open the next under the IV
                if (chunk == 1u) { for (uint i = 0; i < 8; i++) cv0[i] = cv[i]; }
                else { for (uint i = 0; i < 8; i++) cv1[i] = cv[i]; }
                for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
            }
            __attribute__((opencl_unroll_hint))
            for (uint i = 0; i < 16; i++) {
                const uint sx = 16u * b + i - 12u;
                uint stn = 0u;
                if (sx < 8u * IGM_IH_RW) stn = IGM_BCAST(R[sx & IH_RWM], sx / IGM_IH_RW);
                m[i] = (prev >> 8) | (stn << 24); prev = stn;
            }
            const bool last = b == IGM_IH_RW / 2u;
            uint flags = 0u; if (bi == 0u) flags |= IH_CHUNK_START; if (bi == 15u || last) flags |= IH_CHUNK_END;
            uint blen = 64u; if (last) blen = 51u;
            ih_b3_compress(cv, m, chunk, blen, flags, o);
            for (uint i = 0; i < 8; i++) cv[i] = o[i];
            if (last) {
                if (chunk == 1u) { for (uint i = 0; i < 8; i++) cv1[i] = o[i]; }
                else { for (uint i = 0; i < 8; i++) cv2[i] = o[i]; }
            }
        }
        for (uint i = 0; i < 8; i++) { cv[i] = igm_b3_iv[i]; m[i] = cv0[i]; m[8 + i] = cv1[i]; }
#if IGM_IH_RW == 32u
        ih_b3_compress(cv, m, 0u, 64u, IH_PARENT | IH_ROOT, o);   // two chunks: the root parent of cv0 || cv1
#else
        ih_b3_compress(cv, m, 0u, 64u, IH_PARENT, o);               // three chunks: the parent of chunks 0 and 1, then the root with chunk 2
        for (uint i = 0; i < 8; i++) { m[i] = o[i]; m[8 + i] = cv2[i]; }
        ih_b3_compress(cv, m, 0u, 64u, IH_PARENT | IH_ROOT, o);
#endif
#endif
        for (uint i = 0; i < 16; i++) M[i] = o[i];
    }
    for (uint q = 0; q < 8; q++) mix_out8[q] = M[q];
    IH_TR(lane == 0u, 560u, 8u, M);
    { uint Fo[8]; ih_stream_F(k, S, M, Fo);
#if IGM_IH_KEYED_S > 0u
      IGM_FINAL_SINK(st_area + 8u, Fo);   // a keyed document on Metal: the digest's eight words to the nonce's device area (words 8..15), the wrapper's result store reads them back from device memory (13 Oct 2026: the private digest's first word reaches the host as zero through every arithmetic form, the trace buffer's copy exact)
#endif
      for (uint q = 0; q < 8; q++) IGM_FINAL_STORE(final8, q, Fo[q]); IH_TR(lane == 0u, 568u, 8u, Fo); }
    (void)st_area; (void)pad;
#else
    // state bytes into the nonce's area: R[l] at words 16l.., f[l] at 128 + 4l
    for (uint q = 0; q < IGM_IH_RW; q++) st_area[IGM_IH_RW * lane + q] = R[q];
    if (pad) for (uint j = 0; j < 4; j++) st_area[8u * IGM_IH_RW + 4u * lane + j] = f[j];
    IH_TR(true, 576u + 4u * lane, 4u, f);
    IGM_ST_SYNC();
    // M = BLAKE3(tag(T_MIX) || S || ST): 19 + 32 + 640 = 691 bytes (563 without a scratchpad); lane 0 only
#if (IGM_IH_ABL & 16)
    if (lane == 0u) {   // ablation: the tail replaced by a fold of the state area (the writes and the sync kept)
        uint M[16]; for (uint q = 0; q < 16; q++) M[q] = S[q & 7u];
        for (uint q = 0; q < 8u * IGM_IH_RW; q++) M[q & 15u] = (M[q & 15u] ^ st_area[q]) * IH_MUL;
        for (uint q = 0; q < 8; q++) { mix_out8[q] = M[q]; final8[q] = M[q] ^ M[q + 8u]; }
    }
    if (false)
#endif
    if (lane == 0u) {
        uint st_words = pad ? IH_ST_WORDS : 8u * IGM_IH_RW;
        uint mw[16u + IH_ST_WORDS]; for (uint j = 0; j < 16u + IH_ST_WORDS; j++) mw[j] = 0u;   // 176 at the 16-register state
        uint mp = ih_put_tag(mw, 0u, ih_tag_mix, 18u);
        mp = ih_put_bytes32(mw, mp, S);
        for (uint q = 0; q < st_words; q++) { uint v = st_area[q]; for (uint b = 0; b < 4; b++) ih_put8(mw, mp++, v >> (8u * b)); }
        uint M[16]; ih_b3_chunk(k->p, false, mw, mp, 1u, M);
        for (uint q = 0; q < 8; q++) mix_out8[q] = M[q];
        IH_TR(true, 560u, 8u, M);
        // F = BLAKE3(tag(T_FINAL) || C || V || P || S || M): 21 + 8 + 4 + 32 + 32 + 32 = 129 bytes
        uint fw[48]; for (uint j = 0; j < 48; j++) fw[j] = 0u;
        uint fp = ih_put_tag(fw, 0u, ih_tag_final, 20u);
        fp = ih_put_be64(fw, fp, k->c_hi, k->c_lo);
        for (uint i = 0; i < 4; i++) ih_put8(fw, fp++, k->v >> (24u - 8u * i));
        fp = ih_put_bytes32(fw, fp, k->p);
        fp = ih_put_bytes32(fw, fp, S);
        fp = ih_put_bytes32(fw, fp, M);
        uint F[16]; ih_b3_chunk(k->p, false, fw, fp, 1u, F);
#if IGM_IH_KEYED_S > 0u
        IGM_FINAL_SINK(st_area + 8u, F);   // a keyed document on Metal: the digest's eight words to the nonce's device area (words 8..15, the state words consumed), the wrapper's result store reads them back from device memory
#endif
        for (uint q = 0; q < 8; q++) IGM_FINAL_STORE(final8, q, F[q]);
        IH_TR(true, 568u, 8u, F);
    }
#endif
}
