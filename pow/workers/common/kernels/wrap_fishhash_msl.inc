// wrap_fishhash_msl.inc: the Metal search kernel over igm_fishhash_one. One thread per
// hash; the job carries the header words, the big-endian target words, the nonce base,
// the 1024-bit item count and the hash count.
struct igm_fh_job { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint items1024; uint hashes; uint max_results; };
struct igm_fh_result { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; };

inline uint igm_bswap32(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }

kernel void igm_fishhash_search(constant igm_fh_job &job [[buffer(0)]], device const uint *dataset [[buffer(1)]],
                                device igm_fh_result *results [[buffer(2)]], device atomic_uint *result_count [[buffer(3)]],
                                uint gid [[thread_position_in_grid]])
{
    if (gid >= job.hashes) return;
    uint nonce_lo = job.nonce_lo + gid;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job.header[i];
    uint mix[8], fin[8];
    igm_fishhash_one(hdr, nonce_lo, nonce_hi, dataset, job.items1024, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job.target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint slot = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (slot >= job.max_results) return;
    results[slot].nonce_lo = nonce_lo; results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mix[i]; results[slot].digest[i] = fin[i]; }
}
