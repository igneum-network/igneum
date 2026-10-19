// wrap_igneumhash_ctx_cl.inc: the OpenCL kernel over igm_igneumhash_ctx.inc (T-B10a: IGM_IH_CTX nonces per eight-lane group)
__kernel void igm_igneumhash_ctx_search(__constant ih_job *job, __global const uint *dataset, __global ih_result *results, __global uint *result_count,
                                        __global uint *scratch, __global uint *st_areas, __local uint *xch
#ifdef IGM_IH_SPLIT2
                                        , __global const uint *dataset2
#endif
                                        )
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint first = (gid / 8u) * IGM_IH_CTX;   // the group's first nonce index in the batch
    uint lane = tid & 7u;
    uint group = tid / 8u;
    uint slot0 = first % job->scratch_slots;
    if (slot0 + IGM_IH_CTX > job->scratch_slots) slot0 = job->scratch_slots - IGM_IH_CTX;
    uint nonce_lo = job->nonce_lo + first;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8 * IGM_IH_CTX], fin[8 * IGM_IH_CTX];
    for (uint i = 0; i < 8 * IGM_IH_CTX; i++) { mix[i] = 0u; fin[i] = 0u; }
#if IGM_FH_SHUFFLE
    uint simd_base = get_sub_group_local_id() & ~7u;
#else
    uint simd_base = 0u;
#endif
    // every lane runs the body (the exchange and the sync are reached by all); results past the batch are discarded
    igm_igneumhash_lane_ctx(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, scratch, st_areas, slot0, lane, simd_base, xch, group, mix, fin);
    if (lane != 0u) return;
    for (uint c = 0; c < IGM_IH_CTX; c++) {
        if (first + c >= job->hashes) break;
        bool below = true;
        for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[8u * c + i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
        if (!below) continue;
        uint rs = atomic_inc(result_count);
        if (rs >= job->max_results) return;
        uint nlo = nonce_lo + c, nhi = nonce_hi + (nlo < nonce_lo ? 1u : 0u);
        results[rs].nonce_lo = nlo; results[rs].nonce_hi = nhi;
        for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[8u * c + i]; results[rs].digest[i] = fin[8u * c + i]; }
    }
}
