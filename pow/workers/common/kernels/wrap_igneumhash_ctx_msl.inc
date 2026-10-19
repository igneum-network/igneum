// wrap_igneumhash_ctx_msl.inc: the Metal kernel over igm_igneumhash_ctx.inc (T-B10a: IGM_IH_CTX nonces per eight-lane group)
kernel void igm_igneumhash_ctx_search(constant ih_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                                      device ih_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                      device uint *scratch [[buffer(4)]], device uint *st_areas [[buffer(5)]],
#ifdef IGM_IH_SPLIT2
                                      device const uint *dataset2 [[buffer(7)]],
#endif
                                      uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]])
{
    uint first = (gid / 8u) * IGM_IH_CTX;
    uint lane = simd_lane & 7u;
    uint slot0 = first % job.scratch_slots;
    if (slot0 + IGM_IH_CTX > job.scratch_slots) slot0 = job.scratch_slots - IGM_IH_CTX;
    uint nonce_lo = job.nonce_lo + first;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    ih_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8 * IGM_IH_CTX], fin[8 * IGM_IH_CTX];
    for (uint i = 0; i < 8 * IGM_IH_CTX; i++) { mix[i] = 0u; fin[i] = 0u; }
    igm_igneumhash_lane_ctx(&k, hdr, nonce_hi, nonce_lo, dataset IGM_IH_DATASET2_ARG, scratch, st_areas, slot0, lane, simd_lane & ~7u, 0u, mix, fin);
    if (lane != 0u) return;
    for (uint c = 0; c < IGM_IH_CTX; c++) {
        if (first + c >= job.hashes) break;
        bool below = true;
        for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[8u * c + i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
        if (!below) continue;
        uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
        if (rs >= job.max_results) return;
        uint nlo = nonce_lo + c, nhi = nonce_hi + (nlo < nonce_lo ? 1u : 0u);
        results[rs].nonce_lo = nlo; results[rs].nonce_hi = nhi;
        for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[8u * c + i]; results[rs].digest[i] = fin[8u * c + i]; }
    }
}
