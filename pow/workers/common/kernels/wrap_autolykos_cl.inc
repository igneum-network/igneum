// wrap_autolykos_cl.inc: the OpenCL table and search kernels over igm_autolykos.inc.
typedef struct { uchar header[32]; uchar target[32]; uint nonce_lo, nonce_hi; uint N; uint hashes; uint max_results; } igm_al_job;
typedef struct { uint nonce_lo, nonce_hi; uchar mix[32]; uchar digest[32]; } igm_al_result;

__kernel void igm_autolykos_table(uint first, uint count, uint height, __global uchar *table)
{
    uint gid = get_global_id(0);
    if (gid >= count) return;
    uint j = first + gid;
    ulong h[8];
    igm_al_element(j, height, h);
    for (uint i = 0; i < 32; i++) table[(ulong)j * 32u + i] = igm_h_byte(h, i);
}

__kernel void igm_autolykos_search(__constant igm_al_job *job, __global const uchar *table, __global igm_al_result *results, __global uint *result_count)
{
    uint gid = get_global_id(0);
    if (gid >= job->hashes) return;
    uint nonce_lo = job->nonce_lo + gid;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    uchar hdr[32]; for (uint i = 0; i < 32; i++) hdr[i] = job->header[i];
    uchar sum[32], hit[32];
    igm_autolykos_one(hdr, nonce_lo, nonce_hi, table, job->N, sum, hit);
    bool below = true;
    for (uint i = 0; i < 32; i++) { if (hit[i] < job->target[i]) { below = true; break; } if (hit[i] > job->target[i]) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_inc(result_count);
    if (slot >= job->max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 32; i++) { results[slot].mix[i] = sum[i]; results[slot].digest[i] = hit[i]; }
}
