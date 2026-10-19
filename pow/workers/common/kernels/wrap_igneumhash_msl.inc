// wrap_igneumhash_msl.inc: the Metal kernels over igm_igneumhash.inc: the dataset item
// kernel and the search kernel (eight consecutive threads of a SIMD group per nonce).
struct ih_job { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint hashes; uint max_results; uint scratch_slots; ih_consts k; };
struct ih_result { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; };
struct ih_dataset_batch { uint first, count, pad0, pad1; ih_consts k; };

#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
// version 12's shared table (SPEC-R0E-v0 2c, main 2a12dee9): filled once per period by one thread walking the chained scatter over the dataset
kernel void igm_r12_shared_fill(constant ih_dataset_batch &b [[buffer(0)]], device const uint *dataset [[buffer(1)]], device const uint *seed8 [[buffer(2)]], device uint *out [[buffer(3)]], constant uint &words [[buffer(4)]],
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                device const uint *dataset2 [[buffer(5)]],
#endif
                                uint gid [[thread_position_in_grid]])
{
    if (gid != 0u) return;
    ih_consts k = b.k; uint S[8]; for (uint q = 0; q < 8u; q++) S[q] = seed8[q];
    uint x = 0u; for (uint w = 0; w < 8u; w++) { const uint t = S[w]; x = rotate(x ^ t, 5u) + t; }
    const uint reads = words / 64u;
    for (uint kk = 0; kk < reads; kk++) {
        const uint idxt = ih_map(x, &k);
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
        device const uint *itp = (idxt > (k.n_items_mask >> 1u)) ? dataset2 + (ulong)(idxt - (k.n_items_mask >> 1u) - 1u) * (ulong)k.item_words : dataset + (ulong)idxt * (ulong)k.item_words;
#else
        device const uint *itp = dataset + (ulong)ih_slot(idxt, k.n_items_mask) * (ulong)k.item_words;
#endif
        uint fold = 0u;
        for (uint w = 0; w < 64u; w++) { const uint wd = itp[w]; fold = rotate(fold ^ wd, 5u) + wd; out[kk * 64u + w] = wd; }
        x = fold ^ S[(kk + 1u) & 7u] ^ (kk + 1u);
    }
}
#endif
kernel void igm_igneumhash_dataset(constant ih_dataset_batch &b [[buffer(0)]], device uint *dataset [[buffer(1)]], device const uint *cache [[buffer(2)]], uint gid [[thread_position_in_grid]])
{
    if (gid >= b.count) return;
    ih_consts k = b.k;
    ih_dataset_item(&k, b.first + gid, cache, dataset - (ulong)b.pad0 * 32u);
}

kernel void igm_igneumhash_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                                  device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                  device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_TRACE
                                  device uint *trace_buf [[buffer(6)]],
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                  device const uint *dataset2 [[buffer(7)]],
#endif
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
                                  device const uint *r12_shared [[buffer(8)]],   // the period's shared table (versions 12 to 14; a 16-byte stand-in when the document has none)
#if IGM_R0E_OPTRACE
                                  device uint *optr_all [[buffer(13)]],
#endif
#endif
#ifdef IGM_IH_DIAG_SG_TG
                                  threadgroup uint *tgm_all [[threadgroup(0)]],
#endif
                                  uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]], uint tg_index [[thread_index_in_threadgroup]]
#ifdef IGM_IH_DIAG_SG_TG
                                  , uint sg_index [[simdgroup_index_in_threadgroup]]
#endif
                                  )
{
#ifdef IGM_IH_DIAG_SG_TG
    threadgroup uint *tgm = tgm_all + sg_index * (IGM_SG_LANES * 4u);   // this SIMD group's 512 bytes of staging
#endif
    uint hash_index = gid / 8u;
    uint lane = simd_lane & 7u;
    bool active = hash_index < job.hashes;
    uint slot = hash_index % job.scratch_slots;   // the batch never exceeds the slots (the host bounds it)
    uint nonce_lo = job.nonce_lo + hash_index;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8], fin[8];
    for (uint i = 0; i < 8; i++) { mix[i] = 0u; fin[i] = 0u; }
    device uint *slice = scratch + ((ulong)slot * 8u + lane) * (ulong)k.slice_words;
    #if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
#ifndef IGM_IH_TG_OCTETS
#define IGM_IH_TG_OCTETS 2u
#endif
    threadgroup uint r12_tbl_all[IGM_IH_TG_OCTETS * IGM_IH_TKW]; threadgroup uint *r12_tbl = r12_tbl_all + (tg_index >> 3) * IGM_IH_TKW; threadgroup uint *r0e_tg = nullptr;   // versions 12 to 14: every octet of the threadgroup its own copy of the document's table (the engine dispatches IGM_IH_TG_OCTETS * 8 threads; 14 Oct 2026: two copies under a 128-thread dispatch let eight octets overwrite one table, the Metal round fault)
#elif IGM_IH_VERSION == 10 || IGM_IH_VERSION == 11
    slice += (ulong)slot * (IGM_IH_VERSION == 10 ? 4096u : 1024u);   // version 10: the nonce's 16 KB gather table; version 11: 4 KB (slice_words is 0)
    threadgroup uint r0e_tg_all[4096]; threadgroup uint *r0e_tg = r0e_tg_all + (tg_index / 8u) * 128u;   // the octet's tile
    #endif
    device uint *area = st_areas + (ulong)slot * IH_ST_WORDS;   // 160 words at the 16-register state; 8 RW + 32 under version 8's wider state (the host sizes the buffer by the same count)
    // every lane runs the body (the SIMD shuffles and the SIMD-group barrier are reached by all); an inactive
    // nonce still has its own slot (the host bounds the batch to half the slots, the dispatch padding is under that)
#ifdef IGM_IH_TRACE
    device uint *trace = hash_index == 0u ? trace_buf : (device uint *)0;
#endif
#if IGM_R0E_OPTRACE
    device uint *optr_in = optr_all + ((hash_index == 0u) ? 0ul : (size_t)2u * IGM_R0E_OPS * 128u);
#endif
    igm_igneumhash_lane(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, slice, area, lane, simd_lane & ~7u, 0u, mix, fin IGM_IH_TRACE_ARG IGM_IH_TGM_ARG IGM_IH_R0E_ARG IGM_R0E_OPTRACE_LANE_ARG);
    if (!active || lane != 0u) return;
#if IGM_IH_KEYED_S > 0u
    for (uint q = 0; q < 8u; q++) fin[q] = area[8u + q];   // the keyed digest read back from the nonce's device area, where the lane function sank it (the private copy's first word reaches the host as zero through every arithmetic form; a device load into a device store cannot be dropped)
#endif
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    { volatile device uint *dm = results[rs].mix, *dd = results[rs].digest; for (uint i = 0; i < 8; i++) { dm[i] = mix[i]; dd[i] = fin[i]; } }   // volatile device stores (13 Oct 2026: on a keyed document the digest's first word reads zero on the host through every arithmetic form, the trace buffer's copy exact)
}

// T-B3: one thread per nonce (igm_igneumhash1.inc); the scratch slot holds the nonce's eight slices
kernel void igm_igneumhash1_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                                   device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                   device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
                                   uint gid [[thread_position_in_grid]])
{
    if (gid >= job.hashes) return;
    uint slot = gid % job.scratch_slots;
    uint nonce_lo = job.nonce_lo + gid;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8], fin[8];
    igm_igneumhash_one(&k, hdr, nonce_hi, nonce_lo, dataset, scratch + (ulong)slot * 8u * (ulong)k.slice_words, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}

// T-B6: eight threads per item (one SIMD group's consecutive threads); the gather area is per group in device memory
kernel void igm_igneumhash_dataset8(constant ih_dataset_batch &b [[buffer(0)]], device uint *dataset [[buffer(1)]], device const uint *cache [[buffer(2)]],
                                    device uint *gather [[buffer(3)]], uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]])
{
    uint item = gid / 8u, lane = simd_lane & 7u;
    uint i = b.first + (item < b.count ? item : b.count - 1u);   // padding threads repeat the last item (every lane reaches the sync)
    ih_consts k = b.k;
    uint out[32];
    ih_item_v1_lanes(&k, 0u, i, cache, lane, simd_lane & ~7u, 0u, gather + (ulong)(gid / 8u) * 32u, out);
    if (lane != 0u || item >= b.count) return;
    for (uint j = 0; j < 32; j++) dataset[(ulong)i * 32u + j] = out[j];
}

// Version 8 FP, the device check of the fp class (the hash lead's recorded cases): each case (x, y, z, opcode) through the same
// compiled fop as step 1b uses; mode 16 the bit 4 form (opcode 2 only), mode 32 the bit 5 wide form
kernel void igm_v8_fpcheck(device const uint4 *cases [[buffer(0)]], device uint *out [[buffer(1)]], constant uint &mode [[buffer(2)]], constant uint &count [[buffer(3)]], uint gid [[thread_position_in_grid]])
{
    if (gid >= count) return;
    const uint4 c = cases[gid];
    // c.w: 0x100 | opcode for a wide-form row (every opcode binary32), 0xFFFFFFFF for the bit 4 multiply form; the row's shape decides
    out[gid] = (c.w != 0xFFFFFFFFu) ? ih_v8_fop_wide(c.w & 3u, c.x, c.y, c.z) : ih_v8_fop(c.x, c.y, c.z);
    (void)mode;
}

#if IGM_IH_VERSION == 15
// VERSION 15 (igm_r0f.inc): one nonce per octet; the key words (buffer 9) and the period's programme (buffer 10) drawn by the host per period
kernel void igm_r0f_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                           device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                           device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_TRACE
                           device uint *trace_buf [[buffer(6)]],
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                           device const uint *dataset2 [[buffer(7)]],
#endif
                           device const uint *r0f_key [[buffer(9)]], device const uint *r0f_prog [[buffer(10)]], constant uint &r0f_ops [[buffer(11)]],
                           uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]], uint tg_index [[thread_index_in_threadgroup]])
{
    uint hash_index = gid / 8u;
    uint lane = simd_lane & 7u;
    bool active = hash_index < job.hashes;
    uint nonce_lo = job.nonce_lo + hash_index;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
    (void)scratch; (void)st_areas; (void)tg_index;
    igm_r0f_lane(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, kw, r0f_prog, r0f_ops, k.rounds, lane, simd_lane & ~7u, 0u, mix, fin IGM_R0F_TRACE_ARG);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif

#if IGM_IH_VERSION == 16
// VERSION 16 (igm_r16f.inc): one thread an attempt, no dataset (a dummy buffer at 1); the period key words at buffer 9, the programme in
// the library's text (igm_r16f_program, generated per period by the host); the buffer layout as version 15's
kernel void igm_r16f_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                            device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                            device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_TRACE
                            device uint *trace_buf [[buffer(6)]],
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                            device const uint *dataset2 [[buffer(7)]],
#endif
                            device const uint *r0f_key [[buffer(9)]], device const uint *r0f_prog [[buffer(10)]], constant uint &r0f_ops [[buffer(11)]],
                            uint gid [[thread_position_in_grid]])
{
    (void)dataset; (void)scratch; (void)st_areas; (void)r0f_prog; (void)r0f_ops;
    if (gid >= job.hashes) return;
    const uint nonce_lo = job.nonce_lo + gid;
    const uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
    igm_r16f_attempt(&k, hdr, nonce_hi, nonce_lo, kw, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                            device const uint *dataset2 [[buffer(7)]],
#endif
#endif
#if IGM_IH_VERSION == 17
// VERSION 17 (igm_r17.inc): the texture at [[texture(0)]], the sampler the prelude's: one thread an attempt, no dataset (a dummy buffer at 1); the period key words at buffer 9, the programme in
// the library's text (igm_r16f_program, generated per period by the host); the buffer layout as version 15's
kernel void igm_r17_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                            device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                            device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_TRACE
                            device uint *trace_buf [[buffer(6)]],
#endif
                            device const uint *r0f_key [[buffer(9)]], device const uint *r0f_prog [[buffer(10)]], constant uint &r0f_ops [[buffer(11)]], texture2d<uint, access::sample> tex [[texture(0)]],
                            uint gid [[thread_position_in_grid]])
{
    (void)dataset; (void)scratch; (void)st_areas; (void)r0f_prog; (void)r0f_ops;
    if (gid >= job.hashes) return;
    const uint nonce_lo = job.nonce_lo + gid;
    const uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
    igm_r17_attempt(&k, hdr, nonce_hi, nonce_lo, kw, tex, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif

#if IGM_IH_VERSION == 20 || IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25
// VERSION 20 (igm_forge.inc): sixteen threads an attempt (lane = the index in the threadgroup mod 16), the attempt's 16 KiB scratchpad in
// threadgroup memory (the host sets the length at 4096 words an attempt), the keyed gigabyte as the dataset
kernel void igm_forge_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                             device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                             device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_TRACE
                             device uint *trace_buf [[buffer(6)]],
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                             device const uint *dataset2 [[buffer(7)]],
#endif
                             threadgroup uint *xch [[threadgroup(0)]],
                             uint gid [[thread_position_in_grid]], uint tg_index [[thread_index_in_threadgroup]])
{
    (void)st_areas;
    const uint hash_index = gid / IGM_FORGE_LANES, lane = tg_index & (IGM_FORGE_LANES - 1u);
    threadgroup uint *sp = xch + (tg_index / IGM_FORGE_LANES) * IGM_FORGE_ATTEMPT_WORDS; threadgroup uint *xl = sp + IGM_FORGE_SCRATCH_WORDS; threadgroup uint *sp8 = xl + IGM_FORGE_XCH_WORDS;   // version 25: the per-hash scratchpad past the exchange
    const uint active = hash_index < job.hashes ? 1u : 0u;
    const uint nonce_lo = job.nonce_lo + hash_index;
    const uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8], fin[8];
    igm_forge_attempt(&k, hdr, nonce_hi, nonce_lo, lane, sp, xl, sp8, dataset, (device const uint *)0, k.rounds, k.program_ops, k.fold_every, (hash_index == 0u) ? scratch : (device uint *)0, mix, fin);
    if (lane != 0u || !active) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif
