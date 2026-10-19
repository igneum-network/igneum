// wrap_igneumhash_cl.inc: the OpenCL kernels over igm_igneumhash.inc.
typedef struct { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint hashes; uint max_results; uint scratch_slots; ih_consts k; } ih_job;
typedef struct { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; } ih_result;
typedef struct { uint first, count, pad0, pad1; ih_consts k; } ih_dataset_batch;

#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
// version 12's shared table (SPEC-R0E-v0 2c, main 2a12dee9): shared_kib x 256 words per period filled once by the chained scatter over the dataset from
// the period's seed (the host's eight words): one work-item walks the chain (the reads depend on one another); every nonce of the period reads it
__kernel void igm_r12_shared_fill(IGM_JOB_SPACE ih_dataset_batch *b, __global const uint *dataset, __global const uint *seed8, __global uint *out, uint words
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                  , __global const uint *dataset2
#endif
                                  )
{
    if (get_global_id(0) != 0u) return;
    ih_consts k = b->k; uint S[8]; for (uint q = 0; q < 8u; q++) S[q] = seed8[q];
    uint x = 0u; for (uint w = 0; w < 8u; w++) { const uint t = S[w]; x = rotate(x ^ t, 5u) + t; }
    const uint reads = words / 64u;
    for (uint kk = 0; kk < reads; kk++) {
        const uint idxt = ih_map(x, &k);
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
        __global const uint *itp = (idxt > (k.n_items_mask >> 1u)) ? dataset2 + (ulong)(idxt - (k.n_items_mask >> 1u) - 1u) * (ulong)k.item_words : dataset + (ulong)idxt * (ulong)k.item_words;
#else
        __global const uint *itp = dataset + (ulong)ih_slot(idxt, k.n_items_mask) * (ulong)k.item_words;
#endif
        uint fold = 0u;
        for (uint w = 0; w < 64u; w++) { const uint wd = itp[w]; fold = rotate(fold ^ wd, 5u) + wd; out[kk * 64u + w] = wd; }
        x = fold ^ S[(kk + 1u) & 7u] ^ (kk + 1u);
    }
}
#endif
__kernel void igm_igneumhash_dataset(IGM_JOB_SPACE ih_dataset_batch *b, __global uint *dataset, __global const uint *cache)
{
    uint gid = get_global_id(0);
    if (gid >= b->count) return;
    ih_consts k = b->k;
    ih_dataset_item(&k, b->first + gid, cache, dataset - (ulong)b->pad0 * (ulong)k.item_words);   // the second buffer above 2 GiB: pad0 items before it, of the document's width (32 words was the version 1 item: every version 3 and 4 item of the upper half was built at the wrong place and past the buffer's end; found 16:57Z on the RX-F pair)
}

__kernel IGM_KERNEL_ATTR void igm_igneumhash_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                    __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                    , __global uint *trace_buf
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                    , __global const uint *dataset2
#endif
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
                                    , __global const uint *r12_shared   // the period's shared table (versions 12 to 14; a 16-byte stand-in when the document has none)
#if IGM_R0E_OPTRACE
                                    , __global uint *optr_all
#endif
#endif
                                    )
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint hash_index = gid / 8u;
    uint lane = tid & 7u;
    uint group = tid / 8u;
    bool active = hash_index < job->hashes;
    uint slot = hash_index % job->scratch_slots;
    uint nonce_lo = job->nonce_lo + hash_index;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
    for (uint i = 0; i < 8; i++) { mix[i] = 0u; fin[i] = 0u; }
    __global uint *slice = scratch + ((ulong)slot * 8u + lane) * (ulong)k.slice_words + (IGM_IH_VERSION == 10 ? (ulong)slot * 4096u : IGM_IH_VERSION == 11 ? (ulong)slot * 1024u : 0ul);   // version 10: the nonce's 16 KB gather table; version 11: 4 KB (slice_words is 0)
#if (IGM_IH_VERSION >= 12 && IGM_IH_VERSION <= 14)
    __local uint r12_tbl_all[2u * IGM_IH_TKW]; __local uint *r12_tbl = r12_tbl_all + (group & 1u) * IGM_IH_TKW; __local uint *r0e_tg = (__local uint *)0;   // versions 12 to 14: two octets per work-group of 16 lanes, the document's table each (4 KB at table_kib 1 x 16 lanes... the host sizes the group)
#elif IGM_IH_VERSION == 10 || IGM_IH_VERSION == 11
    __local uint r0e_tg_all[4096]; __local uint *r0e_tg = r0e_tg_all + group * 128u;   // the octet's tile (32 nonces per work-group at most)
#endif
    __global uint *area = st_areas + (ulong)slot * IH_ST_WORDS;   // 160 words at the 16-register state; 8 RW + 32 under version 8's wider state (the host sizes the buffer by the same count)
#if defined(IGM_WAVE_INTRINSICS)
    uint simd_base = igm_wave_lane() & ~7u;   // the nonce's eight lanes inside the wave (the wave path)
#elif IGM_FH_SHUFFLE
    uint simd_base = get_sub_group_local_id() & ~7u;
#else
    uint simd_base = 0u;
#endif
    // inactive lanes still run the body on a scratch slot so every barrier is reached; their results are discarded
#ifdef IGM_IH_TRACE
    __global uint *trace = hash_index == 0u ? trace_buf : (__global uint *)0;   // the trace of the batch's first nonce
#endif
#ifdef IGM_IH_X2
    // the two-chain form: this group carries nonces 2 h and 2 h + 1 (the host launches four work-items per nonce)
    {
        uint nlA = job->nonce_lo + 2u * hash_index, nhA = job->nonce_hi + (nlA < job->nonce_lo ? 1u : 0u);
        uint nlB = nlA + 1u, nhB = nhA + (nlB < nlA ? 1u : 0u);
        uint mixB[8], finB[8];
        igm_igneumhash_lane_x2(&k, hdr, nhA, nlA, nhB, nlB, dataset IGM_IH_DATASET2_ARG, lane, simd_base, xch, group, mix, fin, mixB, finB);
        if (lane != 0u) return;
        for (uint c = 0; c < 2u; c++) {
            if (2u * hash_index + c >= job->hashes) break;
            IGM_PRIV const uint *fc = c ? finB : fin, *mc = c ? mixB : mix;
            bool below2 = true;
            for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fc[i]), t = job->target[i]; if (h < t) { below2 = true; break; } if (h > t) { below2 = false; break; } }
            if (!below2) continue;
            uint rs2 = atomic_inc(result_count);
            if (rs2 >= job->max_results) continue;
            results[rs2].nonce_lo = c ? nlB : nlA; results[rs2].nonce_hi = c ? nhB : nhA;
            for (uint i = 0; i < 8; i++) { results[rs2].mix[i] = mc[i]; results[rs2].digest[i] = fc[i]; }
        }
        (void)nonce_lo; (void)nonce_hi; (void)active; (void)slice; (void)area;
        return;
    }
#endif
#if IGM_R0E_OPTRACE
    __global uint *optr_in = optr_all + ((hash_index == 0u) ? 0ul : (ulong)2u * IGM_R0E_OPS * 128u);   // the first octet writes the trace, every other octet the sink half
#endif
    igm_igneumhash_lane(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, slice, area, lane, simd_base, xch, group, mix, fin IGM_IH_TRACE_ARG IGM_IH_R0E_ARG IGM_R0E_OPTRACE_LANE_ARG);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}

// T-B3: one work-item per nonce (igm_igneumhash1.inc); no local memory, no barrier
__kernel void igm_igneumhash1_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                     __global uint *scratch, __global uint *st_areas, __local uint *xch)
{
    uint gid = get_global_id(0);
    if (gid >= job->hashes) return;
    uint slot = gid % job->scratch_slots;
    uint nonce_lo = job->nonce_lo + gid;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
    igm_igneumhash_one(&k, hdr, nonce_hi, nonce_lo, dataset, scratch + (ulong)slot * 8u * (ulong)k.slice_words, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}

// T-B6: eight work-items per item; the gather area per group in global memory; xch is the local exchange when no shuffle
__kernel void igm_igneumhash_dataset8(IGM_JOB_SPACE ih_dataset_batch *b, __global uint *dataset, __global const uint *cache, __global uint *gather, __local uint *xch)
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint item = gid / 8u, lane = tid & 7u, group = tid / 8u;
    uint i = b->first + (item < b->count ? item : b->count - 1u);
    ih_consts k = b->k;
#if defined(IGM_WAVE_INTRINSICS)
    uint simd_base = igm_wave_lane() & ~7u;
#elif IGM_FH_SHUFFLE
    uint simd_base = get_sub_group_local_id() & ~7u;
#else
    uint simd_base = 0u;
#endif
    uint out[32];
    ih_item_v1_lanes(&k, 0u, i, cache, lane, simd_base, xch, group, gather + (ulong)(gid / 8u) * 32u, out);
    if (lane != 0u || item >= b->count) return;
    for (uint j = 0; j < 32; j++) dataset[(ulong)i * 32u + j] = out[j];
}

#if IGM_IH_VERSION == 15
// VERSION 15 (igm_r0f.inc): one nonce per octet; the key words and the period's programme as buffers (the host draws them per period)
__kernel IGM_KERNEL_ATTR void igm_r0f_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                             __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                             , __global uint *trace_buf
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                             , __global const uint *dataset2
#endif
#ifdef IGM_IH_SPLIT4
                                             , __global const uint *dataset3, __global const uint *dataset4
#endif
                                             , __global const uint *r0f_key, __global const uint *r0f_prog, uint r0f_ops, uint job_nonce_lo, uint job_nonce_hi, uint job_hashes
#if IGM_R0F_TRACE
                                             , __global uint *r0f_trace_in
#endif
                                             )
{
    // the job's nonce and count by VALUE (14 Oct 2026, the Arc B580 reading: Intel's runtime served the version 15 kernel a stale view of the
    // __constant job struct across launches, every nonce of a run reading the first one's; the by-value arguments are fresh per launch)
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint hash_index = gid / 8u;
    uint lane = tid & 7u;
    uint group = tid / 8u;
    bool active = hash_index < job_hashes;
    uint nonce_lo = job_nonce_lo + hash_index;
    uint nonce_hi = job_nonce_hi + (nonce_lo < job_nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
#if defined(IGM_WAVE_INTRINSICS)
    uint simd_base = igm_wave_lane() & ~7u;
#elif IGM_FH_SHUFFLE
    uint simd_base = get_sub_group_local_id() & ~7u;
#else
    uint simd_base = 0u;
#endif
    (void)scratch; (void)st_areas;
#if IGM_R0F_TRACE
    __global uint *r0f_trace = r0f_trace_in + ((hash_index == 0u) ? 0u : 1088u);   // the first octet writes the trace, every other octet the sink half
#endif
    igm_r0f_lane(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG IGM_R0F_DATASET4_ARG, kw, r0f_prog, r0f_ops, k.rounds, lane, simd_base, xch, group, mix, fin IGM_R0F_TRACE_ARG);
#if IGM_R0F_TRACE
    if (hash_index == 0u && lane == 0u) { r0f_trace[1] = active ? 1u : 0u; r0f_trace[2] = job_hashes; r0f_trace[3] = gid; r0f_trace[4] = nonce_lo; r0f_trace[5] = nonce_hi; r0f_trace[6] = get_local_size(0);
#if IGM_FH_SHUFFLE
        r0f_trace[7] = get_sub_group_size();
#else
        r0f_trace[7] = 0u;
#endif
    }   // the wrapper's tail: words 1 to 7
#endif
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
#if IGM_R0F_TRACE
    if (hash_index == 0u) r0f_trace[0] = below ? 13u : 14u;   // stage 13: below the target (a result follows); 14: not below
#endif
    if (!below) return;
    uint rs = atomic_inc(result_count);
#if IGM_R0F_TRACE
    if (hash_index == 0u) { r0f_trace[0] = 15u; r0f_trace[1] = rs; r0f_trace[2] = job->max_results; }   // stage 15: the result slot taken (rs in word 1, the cap in word 2)
#endif
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif

#if IGM_IH_VERSION == 16
// VERSION 16 (igm_r16f.inc): one work-item an attempt, no dataset (a dummy buffer in the slot); the period key words as a buffer, the
// programme in the kernel's text (igm_r16f_program, generated per period by the host); the argument layout as version 15's
__kernel IGM_KERNEL_ATTR void igm_r16f_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                              __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                              , __global uint *trace_buf
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                              , __global const uint *dataset2
#endif
#ifdef IGM_IH_SPLIT4
                                              , __global const uint *dataset3, __global const uint *dataset4
#endif
                                              , __global const uint *r0f_key, __global const uint *r0f_prog, uint r0f_ops, uint job_nonce_lo, uint job_nonce_hi, uint job_hashes)
{
    const uint gid = get_global_id(0);
    (void)dataset; (void)scratch; (void)st_areas; (void)xch; (void)r0f_prog; (void)r0f_ops;
    if (gid >= job_hashes) return;   // the job's nonce and count by value (the Intel stale-constant reading of 14 Oct 2026)
    const uint nonce_lo = job_nonce_lo + gid;
    const uint nonce_hi = job_nonce_hi + (nonce_lo < job_nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
    igm_r16f_attempt(&k, hdr, nonce_hi, nonce_lo, kw, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                              , __global const uint *dataset2
#endif
#endif
#if IGM_IH_VERSION == 17
// VERSION 17 (igm_r17.inc): one work-item an attempt, no dataset (a dummy buffer in the slot); the period key words as a buffer, the
// programme in the kernel's text (igm_r16f_program, generated per period by the host); the argument layout as version 15's
__kernel IGM_KERNEL_ATTR void igm_r17_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                              __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                              , __global uint *trace_buf
#endif
#ifdef IGM_IH_SPLIT4
                                              , __global const uint *dataset3, __global const uint *dataset4
#endif
                                              , __global const uint *r0f_key, __global const uint *r0f_prog, uint r0f_ops, uint job_nonce_lo, uint job_nonce_hi, uint job_hashes, __read_only image2d_t tex)
{
    const uint gid = get_global_id(0);
    (void)dataset; (void)scratch; (void)st_areas; (void)xch; (void)r0f_prog; (void)r0f_ops;
    if (gid >= job_hashes) return;   // the job's nonce and count by value (the Intel stale-constant reading of 14 Oct 2026)
    const uint nonce_lo = job_nonce_lo + gid;
    const uint nonce_hi = job_nonce_hi + (nonce_lo < job_nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint kw[8]; for (uint i = 0; i < 8; i++) kw[i] = r0f_key[i];
    uint mix[8], fin[8];
    igm_r17_attempt(&k, hdr, nonce_hi, nonce_lo, kw, tex, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif

#if IGM_IH_VERSION == 20 || IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25
// VERSION 20 (igm_forge.inc): sixteen work-items an attempt (lane = local id mod 16), the attempt's 16 KiB scratchpad in the local
// exchange buffer (the host sizes it at 4096 words an attempt, two attempts a 32-wide group), the keyed gigabyte as the dataset
__kernel IGM_KERNEL_ATTR void igm_forge_search(IGM_JOB_SPACE ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                               __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                               , __global uint *trace_buf
#endif
#if defined(IGM_IH_SPLIT2) || defined(IGM_IH_SPLIT4)
                                               , __global const uint *dataset2
#endif
#ifdef IGM_IH_SPLIT4
                                               , __global const uint *dataset3, __global const uint *dataset4
#endif
                                               , __global const uint *r0f_key, __global const uint *r0f_prog, uint r0f_ops, uint job_nonce_lo, uint job_nonce_hi, uint job_hashes)   // the version 15 tail the host binds for every version from 15 (the key, programme and count unused here)
{
    const uint gid = get_global_id(0), lid = get_local_id(0); (void)r0f_key; (void)r0f_prog; (void)r0f_ops;
    const uint hash_index = gid / IGM_FORGE_LANES, lane = lid & (IGM_FORGE_LANES - 1u);
    (void)st_areas;
    __local uint *sp = xch + (lid / IGM_FORGE_LANES) * IGM_FORGE_ATTEMPT_WORDS; __local uint *xl = sp + IGM_FORGE_SCRATCH_WORDS; __local uint *sp8 = xl + IGM_FORGE_XCH_WORDS;   // version 25: the per-hash scratchpad past the exchange
    const uint active = hash_index < job_hashes ? 1u : 0u;   // every work-item runs the barriers; an inactive attempt computes on the last nonce and writes nothing
    const uint nonce_lo = job_nonce_lo + (active ? hash_index : (job_hashes ? job_hashes - 1u : 0u));   // the inactive attempt on the LAST ACTIVE nonce (the period table's wave-uniform shape index needs the wave on one pair)
    const uint nonce_hi = job_nonce_hi + (nonce_lo < job_nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
    igm_forge_attempt(&k, hdr, nonce_hi, nonce_lo, lane, sp, xl, sp8, dataset, r0f_prog, k.rounds, k.program_ops, k.fold_every, (hash_index == 0u) ? scratch : (__global uint *)0, mix, fin);
    if (lane != 0u || !active) return;   // one result an attempt
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
#endif
