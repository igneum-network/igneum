// wrap_igneumhash_bucket_cl.inc: T-B14, the solver's organisation (the project's second priority, 10 Oct 2026 12:1x UK).
// Version 1's hash exactly (the same pid), computed as a pipeline over a bounded batch of independent nonces instead of
// one resident thread per nonce: the chain state lives in global memory between rounds; every round (1) has each
// nonce's next item index as a request, (2) optionally groups the requests by address bucket (a counting sort over the
// top bits of the index: histogram, scan, scatter), (3) serves the reads in that order into a per-nonce item slot, and
// (4) resumes every nonce's chain from its state with the item. The energy bill of a row includes the queueing, the
// sort, the state movement and the temporary storage, because the row is the whole pipeline. bucket_bits 0 skips the
// sort (the same pipeline served in nonce order), which separates the state-movement cost from the locality gain.
// The expectation stated before the run: random 128-byte addresses over 1 to 4 GiB offer little locality, so the pass
// costs more than it saves and a chip can do the same; this is an experiment, not a protection. GPU engineer B.
// The setup, the merge and the final run one work-item per nonce (igm_igneumhash_bucket.inc): no barrier anywhere in
// the pipeline; the fetch runs eight work-items per request. The merge's lane form is the T-B3 n1 form, named on the row.
typedef struct { uint header[8]; uint target[8]; uint nonce_lo, nonce_hi; uint hashes; uint max_results; uint bucket_shift; uint n_buckets; uint round; uint pad; ih_consts k; } ihb_job;

__kernel void igm_ihb_setup(__constant ihb_job *job, __global uint *state, __global uint *scratch, __global uint *req)
{
    uint n = get_global_id(0);
    if (n >= job->hashes) return;
    uint nonce_lo = job->nonce_lo + n, nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    ih_consts k = job->k;
    uint hdr[8]; for (uint i = 0; i < 8; i++) hdr[i] = job->header[i];
    __global uint *st = state + (ulong)n * IHB_STATE_WORDS;
    ihb1_setup(&k, hdr, nonce_hi, nonce_lo, scratch + (ulong)n * 8u * (ulong)k.slice_words, st);
    req[n] = st[IHB_IDX];
}

__kernel void igm_ihb_hist(__constant ihb_job *job, __global const uint *req, __global uint *hist)
{
    uint n = get_global_id(0);
    if (n >= job->hashes) return;
    atomic_inc(hist + (req[n] >> job->bucket_shift));
}

__kernel void igm_ihb_scan(__constant ihb_job *job, __global uint *hist, __global uint *cursor, __local uint *tmp)
{
    uint tid = get_local_id(0), wg = get_local_size(0);
    uint per = (job->n_buckets + wg - 1u) / wg;
    uint s = 0u;
    for (uint i = 0; i < per; i++) { uint b = tid * per + i; if (b < job->n_buckets) s += hist[b]; }
    tmp[tid] = s;
    barrier(CLK_LOCAL_MEM_FENCE);
    for (uint off = 1u; off < wg; off <<= 1u) { uint v = tid >= off ? tmp[tid - off] : 0u; barrier(CLK_LOCAL_MEM_FENCE); tmp[tid] += v; barrier(CLK_LOCAL_MEM_FENCE); }
    uint run = tmp[tid] - s;
    for (uint i = 0; i < per; i++) { uint b = tid * per + i; if (b < job->n_buckets) { uint h = hist[b]; cursor[b] = run; run += h; } }
}

__kernel void igm_ihb_scatter(__constant ihb_job *job, __global const uint *req, __global uint *cursor, __global uint *order)
{
    uint n = get_global_id(0);
    if (n >= job->hashes) return;
    uint pos = atomic_inc(cursor + (req[n] >> job->bucket_shift));
    order[pos] = n;
}

// the fetch in the served order: eight work-items per request, each its 16 bytes, into the nonce's item slot
__kernel void igm_ihb_fetch(__constant ihb_job *job, __global const uint *dataset, __global const uint *req, __global const uint *order, __global uint *items
#ifdef IGM_IH_SPLIT2
                            , __global const uint *dataset2
#endif
                            )
{
    uint gid = get_global_id(0);
    uint p = gid / 8u, lane = gid & 7u;
    if (p >= job->hashes) return;
    uint n = job->n_buckets ? order[p] : p;
    uint idx = req[n];
    ih_consts k = job->k;
#ifdef IGM_IH_SPLIT2
    __global const uint *it = ((idx > (k.n_items_mask >> 1u)) ? dataset2 + (ulong)(idx - (k.n_items_mask >> 1u) - 1u) * 32u : dataset + (ulong)idx * 32u) + 4u * lane;
#else
    __global const uint *it = dataset + (ulong)idx * 32u + 4u * lane;
#endif
    __global uint *o = items + (ulong)n * 32u + 4u * lane;
    o[0] = it[0]; o[1] = it[1]; o[2] = it[2]; o[3] = it[3];
}

__kernel void igm_ihb_mix(__constant ihb_job *job, __global uint *state, __global uint *scratch, __global const uint *items, __global uint *req)
{
    uint n = get_global_id(0);
    if (n >= job->hashes) return;
    ih_consts k = job->k;
    req[n] = ihb1_round(&k, scratch + (ulong)n * 8u * (ulong)k.slice_words, state + (ulong)n * IHB_STATE_WORDS, items + (ulong)n * 32u, job->round);
}

__kernel void igm_ihb_final(__constant ihb_job *job, __global const uint *state, __global const uint *scratch, __global ih_result *results, __global uint *result_count)
{
    uint n = get_global_id(0);
    if (n >= job->hashes) return;
    ih_consts k = job->k;
    uint mix[8], fin[8];
    ihb1_final(&k, scratch + (ulong)n * 8u * (ulong)k.slice_words, state + (ulong)n * IHB_STATE_WORDS, mix, fin);
    bool below = true;
    for (uint i = 0; i < 8; i++) { uint h = igm_bswap32(fin[i]), t = job->target[i]; if (h < t) { below = true; break; } if (h > t) { below = false; break; } }
    if (!below) return;
    uint rs = atomic_inc(result_count);
    if (rs >= job->max_results) return;
    uint nonce_lo = job->nonce_lo + n, nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);
    results[rs].nonce_lo = nonce_lo; results[rs].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[rs].mix[i] = mix[i]; results[rs].digest[i] = fin[i]; }
}
