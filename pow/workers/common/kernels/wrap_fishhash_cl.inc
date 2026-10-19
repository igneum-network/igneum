// wrap_fishhash_cl.inc: the OpenCL search kernel over igm_fishhash_one.
typedef struct { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint items1024; uint hashes; uint max_results; } igm_fh_job;
typedef struct { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; } igm_fh_result;

IGM_INLINE uint igm_bswap32(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }

__kernel void igm_fishhash_search(__constant igm_fh_job *job, __global const uint *dataset, __global igm_fh_result *results, __global uint *result_count)
{
    uint gid = get_global_id(0);
    if (gid >= job->hashes) return;
    uint nonce_lo = job->nonce_lo + gid;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    uint mix[8], fin[8];
    igm_fishhash_one(hdr, nonce_lo, nonce_hi, dataset, job->items1024, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_inc(result_count);
    if (slot >= job->max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mix[i]; results[slot].digest[i] = fin[i]; }
}
