// wrap_fishhash8_msl.inc: the Metal kernel over igm_fishhash8_one. Eight consecutive
// threads of a SIMD group form one nonce; the broadcast is a SIMD shuffle within the
// eight (the host asserts the queried width is a multiple of eight).
#define IGM_FH_LANES 8u
kernel void igm_fishhash8_search(constant igm_fh_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                                 device igm_fh_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                 uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]])
{
    uint hash_index = gid / IGM_FH_LANES;
    uint lane = simd_lane % IGM_FH_LANES;
    bool active = hash_index < job.hashes;
    uint nonce_lo = job.nonce_lo + hash_index;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8], fin[8];
    igm_fishhash8_one(hdr, nonce_lo, nonce_hi, dataset, active ? job.items1024 : 1u, lane, simd_lane & ~7u, 0u, mix, fin);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (slot >= job.max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mix[i]; results[slot].digest[i] = fin[i]; }
}
