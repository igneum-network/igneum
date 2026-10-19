// wrap_fishhash8_cl.inc: the OpenCL kernel over igm_fishhash8_one. Eight consecutive
// work-items form one nonce. With IGM_FH_SHUFFLE (sub-group shuffle present and the
// sub-group size a multiple of eight) the broadcast is sub_group_shuffle; otherwise a
// local-memory exchange with barriers (the whole work-group takes part in every
// barrier, inactive lanes included).
#define IGM_FH_LANES 8u
__kernel void igm_fishhash8_search(__constant igm_fh_job *job, __global const uint *dataset, __global igm_fh_result *results, __global uint *result_count, __local uint *xch)
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    uint hash_index = gid / IGM_FH_LANES;
    uint lane = tid % IGM_FH_LANES;
    uint group = tid / IGM_FH_LANES;
    bool active = hash_index < job->hashes;
    uint nonce_lo = job->nonce_lo + hash_index;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
#if IGM_FH_SHUFFLE
    uint simd_base = get_sub_group_local_id() & ~7u;
#else
    uint simd_base = 0u;
#endif
    igm_fishhash8_one(hdr, nonce_lo, nonce_hi, dataset, active ? job->items1024 : 1u, lane, simd_base, xch, group, mix, fin);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_inc(result_count);
    if (slot >= job->max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mix[i]; results[slot].digest[i] = fin[i]; }
}
