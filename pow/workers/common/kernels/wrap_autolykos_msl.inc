// wrap_autolykos_msl.inc: the Metal table and search kernels over igm_autolykos.inc.
struct igm_al_job { uchar header[32]; uchar target[32]; uint nonce_lo, nonce_hi; uint N; uint hashes; uint max_results; };
struct igm_al_result { uint nonce_lo, nonce_hi; uchar mix[32]; uchar digest[32]; };
struct igm_al_table_batch { uint first; uint count; uint height; uint pad; };

kernel void igm_autolykos_table(constant igm_al_table_batch &b [[buffer(0)]], device uchar *table [[buffer(1)]], uint gid [[thread_position_in_grid]])
{
    if (gid >= b.count) return;
    uint j = b.first + gid;
    ulong h[8];
    igm_al_element(j, b.height, h);
    for (uint i = 0; i < 32; i++) table[(ulong)j * 32u + i] = igm_h_byte(h, i);
}

kernel void igm_autolykos_search(constant igm_al_job &job [[buffer(0)]], device const uchar *table [[buffer(1)]],
                                 device igm_al_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                 uint gid [[thread_position_in_grid]])
{
    if (gid >= job.hashes) return;
    uint nonce_lo = job.nonce_lo + gid;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    uchar hdr[32]; for (uint i = 0; i < 32; i++) hdr[i] = job.header[i];
    uchar sum[32], hit[32];
    igm_autolykos_one(hdr, nonce_lo, nonce_hi, table, job.N, sum, hit);
    bool below = true;
    for (uint i = 0; i < 32; i++) { if (hit[i] < job.target[i]) { below = true; break; } if (hit[i] > job.target[i]) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (slot >= job.max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 32; i++) { results[slot].mix[i] = sum[i]; results[slot].digest[i] = hit[i]; }
}
