// wrap_igneumhash_bw_cl.inc: the Breakwater kernel over igm_igneumhash_bw.inc, the search kernel's argument list (the
// engine sets the same seven arguments; scratch and the state areas are unused here). One nonce per IGM_BW_LANES lanes.
__kernel IGM_KERNEL_ATTR void igm_igneumhash_bw_search(__constant ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                       __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_TRACE
                                       , __global uint *trace_buf
#endif
#ifdef IGM_IH_SPLIT2
                                       , __global const uint *dataset2
#endif
                                       )
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint hash_index = gid / IGM_BW_LANES;
    uint lane = tid & (IGM_BW_LANES - 1u);
    uint group = tid / IGM_BW_LANES;
    bool active = hash_index < job->hashes;
    uint nonce_lo = job->nonce_lo + hash_index;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
#if defined(IGM_BW_WAVE)
    const uint bw_wl = igm_wave_lane();
    const uint bw_base = bw_wl & ~(IGM_BW_LANES - 1u);
#elif IGM_FH_SHUFFLE && IGM_WAVE >= 32
    const uint bw_wl = get_sub_group_local_id();
    const uint bw_base = bw_wl & ~(IGM_BW_LANES - 1u);
#else
    const uint bw_wl = lane, bw_base = 0u;
#endif
    // the LDS slot of loader 5: IGM_BW_LANES x 16 bytes per group past the exchange words (the host sizes the local argument)
    __local uint *bw_lds = xch + get_local_size(0) + group * (IGM_BW_LANES * 4u);
#ifdef IGM_IH_TRACE
    __global uint *trace = hash_index == 0u ? trace_buf : (__global uint *)0;   // the trace of the batch's first nonce
#endif
#if IGM_BW_RED >= 1
    uint slot = hash_index % job->scratch_slots;   // the state area of the old builder (IGM_BW_OLDFIN); unused otherwise
    igm_igneumhash_bw_hash(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, lane, bw_base, bw_wl, xch, group, bw_lds, st_areas + (ulong)slot * 160u, mix, fin IGM_IH_TRACE_ARG);
#else
    igm_igneumhash_bw_lane(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, lane, bw_base, bw_wl, xch, group, bw_lds, mix, fin);
#endif
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
    (void)scratch; (void)st_areas;
}
