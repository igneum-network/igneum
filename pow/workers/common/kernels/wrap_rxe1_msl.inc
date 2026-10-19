// wrap_rxe1_msl.inc: the Metal search kernel over igm_rxe1.inc: sixteen consecutive threads of a SIMD group per nonce.
struct rxe1_job { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint hashes; uint max_results; uint slots; rxe1_consts k; };
struct rxe1_result { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; };

kernel void igm_rxe1_search(constant rxe1_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                            device rxe1_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                            constant uint *sched [[buffer(4)]], device uint *st_areas [[buffer(5)]],
                            constant uint *rsched [[buffer(6)]],
                            uint gid [[thread_position_in_grid]], uint simd_lane [[thread_index_in_simdgroup]])
{
    const uint G = job.k.group;
    uint hash_index = gid / G;
    uint lane = simd_lane & (G - 1u);
    bool active = hash_index < job.hashes;
    uint slot = hash_index % job.slots;
    uint nonce_lo = job.nonce_lo + hash_index;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    rxe1_consts k = job.k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint seed[8], fin[8];
    for (uint i = 0; i < 8; i++) { seed[i] = 0u; fin[i] = 0u; }
    device uint *area = st_areas + (ulong)slot * 512u;
    igm_rxe1_lane(&k, hdr, nonce_hi, nonce_lo, dataset, sched, rsched, area, lane, simd_lane & ~(G - 1u), 0u, seed, fin);
    if (!active || lane != 0u) return;
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (rs >= job.max_results) return;
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = seed[i]; results[rs].digest[i] = fin[i]; }
}
