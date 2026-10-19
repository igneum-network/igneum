// igm_igneumhash_bw.inc: Project Breakwater (docs/igneumhash/strata/BREAKWATER.md), the native RDNA4 macro-loader family
// of section 1 and the reductions of section 2. One 512-byte macro-item per native wave: the nonce's IGM_BW_LANES lanes
// (32, or 64 for the wave64 comparison) each load 512 / IGM_BW_LANES bytes of the item from a consecutive address, so the
// whole macro-item is one load instruction of the wave. The Riptide performance seat, 10 October 2026. OpenCL only.
//
// Loaders (IGM_BW_LOADER): 1 wave32, one 128-bit load per lane (the primary fused loader); 2 wave32, two 64-bit loads;
// 3 wave32, four 32-bit loads; 4 wave64, one 64-bit load per lane (IGM_BW_LANES 64, the host builds the kernel for
// wave64); 5 wave32, 128-bit, the load straight into LDS (the VGPR-pressure escape; the lane reads its words back from
// LDS); 6 wave32, 128-bit, the non-temporal (streaming) path; 7 wave32, 128-bit, the buffer-resource path. The ISA, not
// this source, says what each one emits: pow/harness/riptide/amd-dryrun.sh prints the memory instructions of the loop.
//
// Reductions (IGM_BW_RED): 0, phase A's transfer-only screening (the loaded words fold into the next address through
// R1 and nothing else: no S, L, M, F; the hash is wrong by design, never a cell beyond phase A); 1, R1 (xor, rotate,
// add: the cost control, not consensus-eligible) inside the complete hash framing (version 4's absorb, cell,
// permutation, exchange and finalisation; only the item word h differs); 2, version 4's reduction (the whole-item
// relation of SPEC 0.4 on the wave layout: exact, the vectors prove it); 3, the hash lead's version 6 (R3), not yet.
//
// The wave layout of the relation (IGM_BW_RED 1 to 3): with G lanes per nonce and LPR = G / 8 lanes per relation lane,
// hardware lane L holds relation lane l = L div LPR and, in slot t (t = 0 to 4 / LPR - 1), quad q = (L mod LPR) + LPR t,
// that is R[l][4q .. 4q + 3] as A[t], B[t], C[t], D[t] (128 / G registers of state per lane). At G = 32 (W512) one quad per
// lane, the layout of 10 October; at G = 16 (W256, UD-B) two; at G = 8 (W128, UD-A) the whole relation lane. The
// absorbed words always land in slot 0 (the item's 4 G words fill quads 0 to LPR - 1 of every relation lane), four
// permutes at every width; the permutation's partner quads q + 2 share the lane below G = 32 and the whole permutation
// is lane-local at G = 8; the exchange and the next address read slot 0 of lane LPR l.
// The description below is the G = 32 instance. The item's word j sits on
// lane j div 4 as component j mod 4 after the load. The tree: two levels local (the lane's four words), five across the
// wave by xor-partner swizzles with the pair ordered by lane id, so every lane ends with h. The absorb (word j into
// R[j mod 8][(j div 8) mod 16]): the destination of word j = 4 Ls + i is lane 16 (Ls mod 2) + 4 i + (Ls div 8),
// register (Ls div 2) mod 4; read from the destination D, its four words come from lanes 8 (D mod 4) + 2 m + (D div 16),
// m = 0 to 3, all of component (D div 4) mod 4 into register m; four permutes, pass s reading the source with
// m = (s + i_D) mod 4, the source offering component ((Ls div 2) - s) mod 4 (one component per instruction). The cell of
// 4.4 runs on the lane's quad with w = h. The permutation perm(k) = (5 k + 1) mod 16 on quads: new a = b, new d = a,
// new b = c of quad q + 1, new c = d of quad q + 2 (two permutes). The exchange of 4.5 touches registers 0 to 3 (quad 0,
// lane 4 l): every lane reads the partners' quad-0 words, the quad-0 lanes apply. The next address is R[g][0] ^ h with
// R[g][0] the a of lane 4 g. The state word x of ST (for M) is on lane x div 4, component x mod 4.
//
// Requirements of section 1 as built: the dataset base and the item mask are kernel-uniform (scalar); the item base is
// one multiply and add per round from the index (no bounds work in the loop: the mask is the bound); no address-space
// conversion inside the loop (the item pointer is global throughout); no global store before the end; no barrier
// between the load and the reduction on the wave path.
#ifndef IGM_BW_LANES
#define IGM_BW_LANES (IGM_IH_ITEM_WORDS / 4u)   // the group: 16 bytes per lane (32 at W512, 16 at W256, 8 at W128); 64 for the wave64 loader (the host defines it)
#endif
#define IGM_BW_WPL (IGM_IH_ITEM_WORDS / IGM_BW_LANES)   // words per lane: 4, or 2 at wave64
#define IGM_BW_LPR (IGM_BW_LANES / 8u)                 // lanes per relation lane: 4, 2 or 1
#define IGM_BW_QPL (4u / IGM_BW_LPR)                   // quads (slots) per lane: 1, 2 or 4
#define IGM_BW_ROTL(x, n) (((x) << (n)) | ((x) >> (32u - (n))))
#if defined(__AMDGCN__)
#define BW_SPLIT() __asm__ __volatile__("" ::: "memory")   // keeps the narrow-load controls narrow: the compiler merges adjacent loads into one 128-bit load otherwise
#else
#define BW_SPLIT()
#endif

// the cross-lane primitives of the nonce's lanes: the wave intrinsics on AMD, the sub-group shuffle where the wave holds
// the lanes, local memory under barriers otherwise (pocl: a sub-group of 8 cannot hold 32 lanes)
#if defined(IGM_IH_AMD_WAVE) && defined(__AMDGCN__)
#define IGM_BW_WAVE 1
#define BW_XOR1(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (1u << 10)))
#define BW_XOR2(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (2u << 10)))
#define BW_XOR4(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (4u << 10)))
#define BW_XOR8(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (8u << 10)))
#define BW_XOR16(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (16u << 10)))
#define BW_XOR32(v) as_uint(__builtin_amdgcn_ds_bpermute((int)((bw_wl ^ 32u) << 2), as_int(v)))
#define BW_READ(v, src) as_uint(__builtin_amdgcn_ds_bpermute((int)((bw_base + (src)) << 2), as_int(v)))
#define BW_SYNC()
#elif IGM_FH_SHUFFLE && IGM_WAVE >= 32
#define BW_XOR1(v) sub_group_shuffle_xor((v), 1u)
#define BW_XOR2(v) sub_group_shuffle_xor((v), 2u)
#define BW_XOR4(v) sub_group_shuffle_xor((v), 4u)
#define BW_XOR8(v) sub_group_shuffle_xor((v), 8u)
#define BW_XOR16(v) sub_group_shuffle_xor((v), 16u)
#define BW_XOR32(v) sub_group_shuffle_xor((v), 32u)
#define BW_READ(v, src) sub_group_shuffle((v), bw_base + (src))
#define BW_SYNC()
#else
IGM_INLINE uint bw_lxch(__local uint *x, uint group, uint lane, uint v, uint src) {
    barrier(CLK_LOCAL_MEM_FENCE);
    x[group * IGM_BW_LANES + lane] = v;
    barrier(CLK_LOCAL_MEM_FENCE);
    return x[group * IGM_BW_LANES + src];
}
#define BW_XOR1(v) bw_lxch(xch, group, lane, (v), lane ^ 1u)
#define BW_XOR2(v) bw_lxch(xch, group, lane, (v), lane ^ 2u)
#define BW_XOR4(v) bw_lxch(xch, group, lane, (v), lane ^ 4u)
#define BW_XOR8(v) bw_lxch(xch, group, lane, (v), lane ^ 8u)
#define BW_XOR16(v) bw_lxch(xch, group, lane, (v), lane ^ 16u)
#define BW_XOR32(v) bw_lxch(xch, group, lane, (v), lane ^ 32u)
#define BW_READ(v, src) bw_lxch(xch, group, lane, (v), (src))
#define BW_SYNC() barrier(CLK_LOCAL_MEM_FENCE)
#endif

// R1, the control fold: the lane's words with lane-domain separation, then the butterfly over the nonce's lanes with the
// pair ordered by lane id so every lane ends with the same word; one word per round into the address state
IGM_INLINE uint bw_r1_fold(uint lane, uint w0, uint w1, uint w2, uint w3) {
    return ((w0 ^ IGM_BW_ROTL(w1, 7)) + (w2 ^ IGM_BW_ROTL(w3, 13))) ^ ((lane + 1u) * IH_MUL);
}
#define BW_R1_STAGE(x, m, Y) { uint y = Y; uint lo = (lane & (m)) ? y : (x); uint hi = (lane & (m)) ? (x) : y; (x) = (lo ^ IGM_BW_ROTL(hi, 11)) + hi; }

// the loader: the lane's IGM_BW_WPL words of the macro-item at it (a global pointer to the item's first word)
#if IGM_BW_LOADER == 1 || IGM_BW_LOADER == 6 || IGM_BW_LOADER == 7 || IGM_BW_LOADER == 5
#define BW_LOAD_DECL uint4 q
#if IGM_BW_LOADER == 1
#define BW_LOAD(it) q = *(__global const uint4 *)((it) + 4u * lane)
#elif IGM_BW_LOADER == 6
#define BW_LOAD(it) q = __builtin_nontemporal_load((__global const uint4 *)((it) + 4u * lane))
#elif IGM_BW_LOADER == 7
#if defined(__AMDGCN__) && __has_builtin(__builtin_amdgcn_make_buffer_rsrc) && __has_builtin(__builtin_amdgcn_raw_buffer_load_b128)
#define IGM_BW_B7_NATIVE 1
// the buffer-resource path: the resource built once from the dataset base outside the loop (the host passes the
// dataset size in words through the job), the item offset in bytes per round
#define BW_LOAD(it) q = as_uint4(__builtin_amdgcn_raw_buffer_load_b128(bw_rsrc, (int)(((it) - dataset) * 4u) + (int)(16u * lane), 0, 0))   /* one buffer only: the host refuses B7 above 2 GiB */
#else
#define BW_LOAD(it) q = *(__global const uint4 *)((it) + 4u * lane)
#endif
#else   /* 5: into LDS, then the lane's own 16 bytes back; the LDS slot is the group's IGM_BW_LANES x 16 bytes */
#if defined(__AMDGCN__) && __has_builtin(__builtin_amdgcn_global_load_lds)
#define IGM_BW_B5_NATIVE 1
#define BW_LOAD(it) { __builtin_amdgcn_global_load_lds((__global void *)((it) + 4u * lane), (__local void *)bw_lds, 16u, 0, 0); __builtin_amdgcn_s_waitcnt(0); q = *(__local const uint4 *)(bw_lds + 4u * lane); }
#else
#define BW_LOAD(it) { __local uint4 *s4 = (__local uint4 *)bw_lds; s4[lane] = *(__global const uint4 *)((it) + 4u * lane); barrier(CLK_LOCAL_MEM_FENCE); q = s4[lane]; }
#endif
#endif
#define BW_W0 q.x
#define BW_W1 q.y
#define BW_W2 q.z
#define BW_W3 q.w
#elif IGM_BW_LOADER == 2
#define BW_LOAD_DECL uint2 qa, qb
#define BW_LOAD(it) { qa = *(__global const uint2 *)((it) + 4u * lane); BW_SPLIT(); qb = *(__global const uint2 *)((it) + 4u * lane + 2u); }
#define BW_W0 qa.x
#define BW_W1 qa.y
#define BW_W2 qb.x
#define BW_W3 qb.y
#elif IGM_BW_LOADER == 3
#define BW_LOAD_DECL uint w0, w1, w2, w3
#define BW_LOAD(it) { w0 = (it)[4u * lane]; BW_SPLIT(); w1 = (it)[4u * lane + 1u]; BW_SPLIT(); w2 = (it)[4u * lane + 2u]; BW_SPLIT(); w3 = (it)[4u * lane + 3u]; }
#define BW_W0 w0
#define BW_W1 w1
#define BW_W2 w2
#define BW_W3 w3
#elif IGM_BW_LOADER == 4
// wave64: 64 lanes, 8 bytes each; the two words fold as (w0, w1, 0, 0) with the lane constant separating positions
#define BW_LOAD_DECL uint2 qa
#define BW_LOAD(it) qa = *(__global const uint2 *)((it) + 2u * lane)
#define BW_W0 qa.x
#define BW_W1 qa.y
#define BW_W2 0u
#define BW_W3 0u
#else
#error "IGM_BW_LOADER must be 1 to 7"
#endif

// the lane function: phase A (IGM_BW_RED 0), the transfer-only chain. acc is the per-nonce address state; the pseudo
// digest at the end is a function of acc alone (wrong by design: the bench counts its hits, never verifies them).
IGM_INLINE void igm_igneumhash_bw_lane(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                                   IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, uint lane, uint bw_base, uint bw_wl, __local uint *xch, uint group,
                                   __local uint *bw_lds, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
#if defined(IGM_BW_B7_NATIVE)
    const __amdgpu_buffer_rsrc_t bw_rsrc = __builtin_amdgcn_make_buffer_rsrc((__global void *)dataset, 0, (int)(((k->n_items_mask + 1u) * k->item_words) * 4u), 0x00020000);   // raw buffer, the whole dataset
#endif
    const uint mask = k->n_items_mask;
    const uint iw = IGM_IH_ITEM_WORDS;   // the macro-item
    uint acc = (nonce_lo * IH_MUL) ^ hdr8[0] ^ IGM_BW_ROTL(nonce_hi, 9) ^ hdr8[7];
    uint idx = acc & mask;
#ifdef IGM_BW_KEEP
    // the residency control (10 October 21:5x UK): IGM_BW_KEEP words kept alive through the walk and folded into the output, so
    // this twin runs at a chosen VGPR tier (the kernel's) instead of its own; the dry run names the tier per count
    uint xk[IGM_BW_KEEP]; for (uint i = 0; i < IGM_BW_KEEP; i++) xk[i] = acc ^ ((i + 1u) * IH_MUL);
#endif
    for (uint r = 0; r < k->rounds; r++) {
#ifdef IGM_IH_SPLIT2
        IGM_GLOBAL const uint *it = (idx > (mask >> 1u)) ? dataset2 + (ulong)(idx - (mask >> 1u) - 1u) * iw : dataset + (ulong)idx * iw;   // the two buffers above 2 GiB
#else
        IGM_GLOBAL const uint *it = dataset + (ulong)idx * iw;
#endif
        BW_LOAD_DECL;
        BW_LOAD(it);
#ifdef IGM_BW_KEEP
        for (uint i = 0; i < IGM_BW_KEEP; i++) { xk[i] += BW_W0 ^ (i + 1u); }
#if defined(__AMDGCN__)
        for (uint i = 0; i < IGM_BW_KEEP; i++) { __asm__ __volatile__("" : "+v"(xk[i])); }
#endif
#endif
        uint x = bw_r1_fold(lane, BW_W0, BW_W1, BW_W2, BW_W3);
        BW_R1_STAGE(x, 1u, BW_XOR1(x));
        BW_R1_STAGE(x, 2u, BW_XOR2(x));
        BW_R1_STAGE(x, 4u, BW_XOR4(x));
#if IGM_BW_LANES >= 16u
        BW_R1_STAGE(x, 8u, BW_XOR8(x));
#endif
#if IGM_BW_LANES >= 32u
        BW_R1_STAGE(x, 16u, BW_XOR16(x));
#endif
#if IGM_BW_LANES == 64u
        BW_R1_STAGE(x, 32u, BW_XOR32(x));
#endif
        acc = IGM_BW_ROTL(acc, 5) + x;   // every lane holds the same x: the next address is uniform across the nonce's lanes
        idx = acc & mask;
    }
#ifdef IGM_BW_KEEP
    for (uint i = 0; i < IGM_BW_KEEP; i++) acc += xk[i];
#endif
    for (uint i = 0; i < 8; i++) { mix_out8[i] = acc ^ ((i + 1u) * IH_MUL); final8[i] = IGM_BW_ROTL(acc + (i + 3u) * IH_MUL, (i * 5u + 1u) & 31u) ^ nonce_lo; }
    (void)bw_wl; (void)xch; (void)group; (void)bw_lds; (void)bw_base;
}

#if IGM_BW_RED >= 1
#if IGM_BW_LANES != 32u && IGM_BW_LANES != 16u && IGM_BW_LANES != 8u
#error "the wave layout of the relation takes 32, 16 or 8 lanes per nonce"
#endif
#define BW_NODE(x, y) ((((x) ^ (((y) << 13) | ((y) >> 19))) * IH_MUL) + ((y) >> 7))
#define BW_SEL4(v0, v1, v2, v3, i) ((i) == 0u ? (v0) : (i) == 1u ? (v1) : (i) == 2u ? (v2) : (v3))
#if IGM_BW_QPL == 1u
#define BW_SELQ(arr, t) ((arr)[0])
#elif IGM_BW_QPL == 2u
#define BW_SELQ(arr, t) ((t) == 0u ? (arr)[0] : (arr)[1])
#else
#define BW_SELQ(arr, t) ((t) == 0u ? (arr)[0] : (t) == 1u ? (arr)[1] : (t) == 2u ? (arr)[2] : (arr)[3])
#endif
#define BW_UNROLL __attribute__((opencl_unroll_hint(IGM_BW_QPL)))
// the complete hash on the wave layout; trace: the reference's layout (IGM_IH_TRACE and IGM_IH_TRACE_FULL)
IGM_INLINE void igm_igneumhash_bw_hash(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo,
                                   IGM_GLOBAL const uint *dataset IGM_IH_DATASET2_PARAM, uint lane, uint bw_base, uint bw_wl, __local uint *xch, uint group,
                                   __local uint *bw_lds, IGM_GLOBAL uint *st_area, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8 IGM_IH_TRACE_PARAM) {
#if defined(IGM_BW_B7_NATIVE)
    const __amdgpu_buffer_rsrc_t bw_rsrc = __builtin_amdgcn_make_buffer_rsrc((__global void *)dataset, 0, (int)(((k->n_items_mask + 1u) * k->item_words) * 4u), 0x00020000);
#endif
    const uint mask = k->n_items_mask;
    const uint iw = IGM_IH_ITEM_WORDS;
    const uint l = lane / IGM_BW_LPR, p = lane % IGM_BW_LPR;   // relation lane, and the lane's place in it (slot t holds quad p + LPR t)
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);
    IH_TR(lane == 0u, 0u, 8u, S); IH_TR(lane == 0u, 8u, 8u, k->e); IH_TR(lane == 0u, 16u, 8u, k->p);
    uint A[IGM_BW_QPL], B[IGM_BW_QPL], C[IGM_BW_QPL], D[IGM_BW_QPL];
    { uint R16[16]; ih_stream_L(S, l, R16);
      BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint q = p + IGM_BW_LPR * t; A[t] = R16[4u * q]; B[t] = R16[4u * q + 1u]; C[t] = R16[4u * q + 2u]; D[t] = R16[4u * q + 3u]; } }
#ifdef IGM_IH_TRACE
    if (trace) BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint o = 16u * l + 4u * (p + IGM_BW_LPR * t);
        trace[32u + o] = A[t]; trace[33u + o] = B[t]; trace[34u + o] = C[t]; trace[35u + o] = D[t];
        trace[160u + o] = A[t]; trace[161u + o] = B[t]; trace[162u + o] = C[t]; trace[163u + o] = D[t]; }
#endif
    // 4.3 first address: (R[0][0] ^ R[4][8]) mod N_ITEMS; R[4][8] is quad 2 of relation lane 4: lane 4 LPR + (2 mod LPR), slot 2 / LPR, component a
    uint idx = (BW_READ(A[0], 0u) ^ BW_READ(A[2u / IGM_BW_LPR], 4u * IGM_BW_LPR + (2u % IGM_BW_LPR))) & mask;
    for (uint r = 0; r < k->rounds; r++) {
#ifdef IGM_IH_SPLIT2
        IGM_GLOBAL const uint *it = (idx > (mask >> 1u)) ? dataset2 + (ulong)(idx - (mask >> 1u) - 1u) * iw : dataset + (ulong)idx * iw;   // the two buffers above 2 GiB
#else
        IGM_GLOBAL const uint *it = dataset + (ulong)idx * iw;
#endif
        BW_LOAD_DECL;
        BW_LOAD(it);
        uint w0 = BW_W0, w1 = BW_W1, w2 = BW_W2, w3 = BW_W3;
#if defined(__AMDGCN__)
        __asm__ __volatile__("" : "+v"(w0), "+v"(w1), "+v"(w2), "+v"(w3));   // the loaded words stay registers: without this the absorb's select of w folds back into indexed 32-bit re-loads of the item
#endif
#ifdef IGM_IH_TRACE_FULL
        const uint trb = 704u + r * (130u + iw);
        if (trace) { if (lane == 0u) trace[trb] = idx; trace[trb + 2u + 4u * lane] = w0; trace[trb + 3u + 4u * lane] = w1; trace[trb + 4u + 4u * lane] = w2; trace[trb + 5u + 4u * lane] = w3; }
#endif
        // the item word h
        uint h;
#if IGM_BW_RED >= 2
        {
            const uint j0 = 4u * lane;
            uint k0 = 0u, k1 = 0u, k2 = 0u, k3 = 0u;
            if (IH_V6) {
                // version 6 on the wave layout: the key of word 4 L + c is register ((L div 2) mod 4) of slot 0 on lane
                // LPR (4 (L mod 2) + c) + (L div 8); in pass p6 every lane offers register ((p6 + (l mod 4)) mod 4) of
                // slot 0 and the reader takes it for c = (((L div 2) mod 4) - p6) mod 4
                const uint m6 = (lane >> 1u) & 3u;
                for (uint p6 = 0; p6 < 4; p6++) {
                    const uint oc = (p6 + (l & 3u)) & 3u;
                    const uint offer = BW_SEL4(A[0], B[0], C[0], D[0], oc);
                    const uint cc = (m6 - p6) & 3u;
                    const uint got = BW_READ(offer, IGM_BW_LPR * (4u * (lane & 1u) + cc) + (lane >> 3u));
                    if (cc == 0u) k0 = got; else if (cc == 1u) k1 = got; else if (cc == 2u) k2 = got; else k3 = got;
                }
            }
            uint z0 = w0 ^ k0 ^ ((j0 + 1u) * IH_MUL), z1 = w1 ^ k1 ^ ((j0 + 2u) * IH_MUL), z2 = w2 ^ k2 ^ ((j0 + 3u) * IH_MUL), z3 = w3 ^ k3 ^ ((j0 + 4u) * IH_MUL);
            uint x = BW_NODE(BW_NODE(z0, z1), BW_NODE(z2, z3));
            { uint y = BW_XOR1(x);  x = (lane & 1u) ? BW_NODE(y, x) : BW_NODE(x, y); }
            { uint y = BW_XOR2(x);  x = (lane & 2u) ? BW_NODE(y, x) : BW_NODE(x, y); }
            { uint y = BW_XOR4(x);  x = (lane & 4u) ? BW_NODE(y, x) : BW_NODE(x, y); }
#if IGM_BW_LANES >= 16u
            { uint y = BW_XOR8(x);  x = (lane & 8u) ? BW_NODE(y, x) : BW_NODE(x, y); }
#endif
#if IGM_BW_LANES >= 32u
            { uint y = BW_XOR16(x); x = (lane & 16u) ? BW_NODE(y, x) : BW_NODE(x, y); }
#endif
            h = x;
            if (IH_V6) { h ^= h >> 16; h *= IH_MUL; h ^= h >> 15; }   // version 6: the root finalised
        }
#else
        {   // R1, the control: the butterfly of bw_r1_fold (not consensus-eligible)
            uint x = bw_r1_fold(lane, w0, w1, w2, w3);
            BW_R1_STAGE(x, 1u, BW_XOR1(x)); BW_R1_STAGE(x, 2u, BW_XOR2(x)); BW_R1_STAGE(x, 4u, BW_XOR4(x));
#if IGM_BW_LANES >= 16u
            BW_R1_STAGE(x, 8u, BW_XOR8(x));
#endif
#if IGM_BW_LANES >= 32u
            BW_R1_STAGE(x, 16u, BW_XOR16(x));
#endif
            h = x;
        }
#endif
#ifdef IGM_IH_TRACE_FULL
        if (trace && lane == 0u) trace[trb + 1u] = h;
#endif
        // the absorb into slot 0: word 4 Ls + i goes to relation lane 4 (Ls mod 2) + i, quad Ls div 8 (= place p of its
        // lane, slot 0), register (Ls div 2) mod 4; the destination (relation lane l, place p) reads its register m from lane
        // 8 p + 2 m + (l div 4), the source offering register ((Ls mod 8) div 2 - s) mod 4 in pass s; four permutes
        {
            const uint iD = l & 3u;
            for (uint s = 0; s < 4; s++) {
                const uint off = (((lane & 7u) >> 1u) - s) & 3u;
                const uint v = BW_SEL4(w0, w1, w2, w3, off);
                const uint m = (s + iD) & 3u;
                const uint src = 8u * p + 2u * m + (l >> 2u);
                const uint got = BW_READ(v, src);
                A[0] += (m == 0u) ? got : 0u; B[0] += (m == 1u) ? got : 0u; C[0] += (m == 2u) ? got : 0u; D[0] += (m == 3u) ? got : 0u;
            }
        }
        // the cell of 4.4 on every quad with w = h
        BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) {
            const uint wv = h;
            uint a = A[t], b = B[t], c = C[t], d = D[t];
            a = a + wv;
            b = b ^ a;
            c = c + b * IH_MUL;
            { uint n = a & 31u; d = d ^ c; d = n ? ((d >> n) | (d << (32u - n))) : d; }
            a = a ^ (d >> (wv & 31u));
            { uint sum = b + d; b = (sum << 7) | (sum >> 25); }
            c = c ^ ((a << 17) | (a >> 15));
            d = d + b * c;
            A[t] = a; B[t] = b; C[t] = c; D[t] = d;
        }
        // the permutation (5 k + 1) mod 16 on quads: new a = b, new d = a, new b = c of quad q + 1, new c = d of quad q + 2
        {
            uint nb[IGM_BW_QPL], nc[IGM_BW_QPL];
#if IGM_BW_LPR == 4u
            nb[0] = BW_READ(C[0], (lane & ~3u) | ((p + 1u) & 3u));
            nc[0] = BW_READ(D[0], (lane & ~3u) | ((p + 2u) & 3u));
#elif IGM_BW_LPR == 2u
            // quad q + 1 sits on the partner lane (lane ^ 1): slot t for the reader at place 0, slot t + 1 for the reader at
            // place 1; the partner offers accordingly. quad q + 2 is the lane's own next slot.
            BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint offer = (p == 1u) ? C[t] : C[(t + 1u) & 1u]; nb[t] = BW_READ(offer, lane ^ 1u); nc[t] = D[(t + 1u) & 1u]; }
#else
            BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { nb[t] = C[(t + 1u) & 3u]; nc[t] = D[(t + 2u) & 3u]; }
#endif
            BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint na = B[t], nd = A[t]; A[t] = na; B[t] = nb[t]; C[t] = nc[t]; D[t] = nd; }
        }
        // the exchange of 4.5 on registers 0 to 3 (quad 0 of each relation lane: lane LPR l, slot 0), snapshot semantics: every read before any write
        {
            const uint pe = l ^ (1u << (r % 3u));
            const uint b0 = BW_READ(B[0], IGM_BW_LPR * l);                  // X[l][1]
            const uint qd = (l + 1u + (b0 % 7u)) & 7u;
            const uint xp0 = BW_READ(A[0], IGM_BW_LPR * pe), xq1 = BW_READ(B[0], IGM_BW_LPR * qd), xp2 = BW_READ(C[0], IGM_BW_LPR * pe), xq3 = BW_READ(D[0], IGM_BW_LPR * qd);
            if (p == 0u) { A[0] = A[0] + xp0; B[0] = B[0] ^ xq1; C[0] = C[0] + ((xp2 << 11) | (xp2 >> 21)); D[0] = D[0] ^ ((xq3 << 23) | (xq3 >> 9)); }
        }
        // 4.6 the next address: R[g][0] ^ h, R[g][0] the a of lane LPR g, slot 0
        { const uint g = (r + 1u) & 7u; idx = (BW_READ(A[0], IGM_BW_LPR * g) ^ h) & mask; }
#ifdef IGM_IH_TRACE_FULL
        if (trace) BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint o = trb + 2u + iw + 16u * l + 4u * (p + IGM_BW_LPR * t); trace[o] = A[t]; trace[o + 1u] = B[t]; trace[o + 2u] = C[t]; trace[o + 3u] = D[t]; }
#endif
    }
#ifdef IGM_BW_OLDFIN
    // The old builder on the one-wave loader (decision D's bw-p-new-old cell): the state written to the nonce's area in
    // global memory (word x of ST from lane x div 4, component x mod 4), then lane 0 builds M and F through the byte
    // arrays exactly as the eight-lane kernel's byte-builder path does. The same bytes; the vectors prove it.
    BW_UNROLL for (uint t = 0; t < IGM_BW_QPL; t++) { const uint o = 16u * l + 4u * (p + IGM_BW_LPR * t); st_area[o] = A[t]; st_area[o + 1u] = B[t]; st_area[o + 2u] = C[t]; st_area[o + 3u] = D[t]; }
    IGM_ST_SYNC();
    if (lane == 0u) {
        uint M[16];
        uint mw[176]; for (uint j = 0; j < 176; j++) mw[j] = 0u;
        uint mp = ih_put_tag(mw, 0u, ih_tag_mix, 18u);
        mp = ih_put_bytes32(mw, mp, S);
        for (uint qd = 0; qd < 128u; qd++) { uint v = st_area[qd]; for (uint bb = 0; bb < 4; bb++) ih_put8(mw, mp++, v >> (8u * bb)); }
        ih_b3_chunk(k->p, false, mw, mp, 1u, M);
        for (uint qd = 0; qd < 8; qd++) mix_out8[qd] = M[qd];
        IH_TR(true, 560u, 8u, M);
        uint fw[48]; for (uint j = 0; j < 48; j++) fw[j] = 0u;
        uint fp = ih_put_tag(fw, 0u, ih_tag_final, 20u);
        fp = ih_put_be64(fw, fp, k->c_hi, k->c_lo);
        for (uint i = 0; i < 4; i++) ih_put8(fw, fp++, k->v >> (24u - 8u * i));
        fp = ih_put_bytes32(fw, fp, k->p);
        fp = ih_put_bytes32(fw, fp, S);
        fp = ih_put_bytes32(fw, fp, M);
        uint F[16]; ih_b3_chunk(k->p, false, fw, fp, 1u, F);
        for (uint qd = 0; qd < 8; qd++) final8[qd] = F[qd];
        IH_TR(true, 568u, 8u, F);
    }
    (void)bw_wl; (void)xch; (void)group; (void)bw_lds; (void)bw_base;
    return;
#endif
    // M = BLAKE3(tag(T_MIX) || S || ST), ST the 512 register bytes: word x of ST is R[x div 16][x mod 16], quad (x mod 16) div 4,
    // on lane LPR (x div 16) + (quad mod LPR), slot quad div LPR, register x mod 4; every lane computes M (an instruction
    // issues once per wave whichever lanes take it)
    uint M[16];
    {
        uint cv[8]; for (uint i = 0; i < 8; i++) cv[i] = igm_b3_iv[i];
        uint m[16], o[16];
        IH_MIX_HEAD(m, S);
        uint prev = BW_READ(A[0], 0u);   // ST[0]
        m[12] = (S[7] >> 8) | (prev << 24);
        { uint stn = BW_READ(B[0], 0u); m[13] = (prev >> 8) | (stn << 24); prev = stn; }
        { uint stn = BW_READ(C[0], 0u); m[14] = (prev >> 8) | (stn << 24); prev = stn; }
        { uint stn = BW_READ(D[0], 0u); m[15] = (prev >> 8) | (stn << 24); prev = stn; }
        ih_b3_compress(cv, m, 0u, 64u, IH_CHUNK_START, o);
        for (uint i = 0; i < 8; i++) cv[i] = o[i];
        for (uint bb = 1; bb < 9; bb++) {
            __attribute__((opencl_unroll_hint(16)))
            for (uint i = 0; i < 16; i++) {
                const uint sx = 16u * bb + i - 12u;   // ST[sx] enters here; ST[128] is past the end (zero)
                const uint qx = (sx & 15u) >> 2u, tx = qx / IGM_BW_LPR;   // the quad of ST[sx] and its slot (uniform)
                const uint comp = (i & 3u) == 0u ? BW_SELQ(A, tx) : (i & 3u) == 1u ? BW_SELQ(B, tx) : (i & 3u) == 2u ? BW_SELQ(C, tx) : BW_SELQ(D, tx);
                uint stn = (sx < 128u) ? BW_READ(comp, IGM_BW_LPR * (sx >> 4u) + (qx % IGM_BW_LPR)) : 0u;
                m[i] = (prev >> 8) | (stn << 24); prev = stn;
            }
            const uint flags = (bb == 8u) ? (IH_CHUNK_END | IH_ROOT) : 0u;
            ih_b3_compress(cv, m, 0u, bb == 8u ? 51u : 64u, flags, o);
            if (bb < 8u) for (uint i = 0; i < 8; i++) cv[i] = o[i];
        }
        for (uint i = 0; i < 16; i++) M[i] = o[i];
    }
    for (uint i = 0; i < 8; i++) mix_out8[i] = M[i];
    IH_TR(lane == 0u, 560u, 8u, M);
    { uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint i = 0; i < 8; i++) final8[i] = Fo[i]; IH_TR(lane == 0u, 568u, 8u, Fo); }
    (void)bw_wl; (void)xch; (void)group; (void)bw_lds; (void)bw_base;
}
#endif
