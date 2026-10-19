// wrap_rxe1_cl.inc: the OpenCL search kernel over igm_rxe1.inc: sixteen work-items per nonce, nonce groups isolated
// inside the workgroup (group = local id / 16), the exchange through local memory under workgroup barriers where the
// runtime has no sub-group shuffle; every work-item runs the body so every barrier is reached.
typedef struct { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint hashes; uint max_results; uint slots; rxe1_consts k; } rxe1_job;
typedef struct { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; } rxe1_result;

__kernel void igm_rxe1_search(__constant rxe1_job *job, __global const uint *dataset, __global rxe1_result *results, __global uint *result_count,
                              __constant uint *sched, __global uint *st_areas, __local uint *xch, __constant uint *rsched)
{
    uint gid = get_global_id(0), tid = get_local_id(0);
    const uint G = job->k.group;
    uint hash_index = gid / G;
    uint lane = tid % G;
    uint group = tid / G;
    bool active = hash_index < job->hashes;
    uint slot = hash_index % job->slots;
    uint nonce_lo = job->nonce_lo + hash_index;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    rxe1_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint seed[8], fin[8];
    for (uint i = 0; i < 8; i++) { seed[i] = 0u; fin[i] = 0u; }
    __global uint *area = st_areas + (ulong)slot * 512u;
#if IGM_FH_SHUFFLE
    uint simd_base16 = get_sub_group_local_id() - (get_sub_group_local_id() % G);
#else
    uint simd_base16 = 0u;
#endif
    igm_rxe1_lane(&k, hdr, nonce_hi, nonce_lo, dataset, sched, rsched, area, lane, simd_base16, xch, group, seed, fin);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = seed[i]; results[rs].digest[i] = fin[i]; }
}
