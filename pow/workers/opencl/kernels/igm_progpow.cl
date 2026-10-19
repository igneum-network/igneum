// igm_progpow.cl: the ProgPoW/KAWPOW-family search kernel, OpenCL C 1.2 baseline with
// the sub-group path behind a macro (DESIGN.md 4.1). GPU engineer B, 9 October 2026.
// IGM_ROTR_VAR: the period program's variable right-rotate (progpow_gen.cpp math_code case 5). The host defines
// IGM_INTEL_ROTR_FOLD on an Intel platform: that compiler folds rotate(x, (32u - n) & 31u) into a LEFT rotate (the
// Arc B580 bisect of 7 October 2026), so the helper is then the shift form; every other vendor keeps the builtin.
#ifndef IGM_ROTR_VAR
#if defined(IGM_INTEL_ROTR_FOLD)
static inline uint igm_rotr_var(uint x, uint n) { n &= 31u; return (x >> n) | (x << ((32u - n) & 31u)); }
#define IGM_ROTR_VAR(x, n) igm_rotr_var((x), (n))
#else
#define IGM_ROTR_VAR(x, n) rotate((x), (32u - (((n)) & 31u)) & 31u)
#endif
#endif
// Source only; not built, not run. Same flow as igm_progpow.metal, from the vendored
// reference's hash() byte for byte.
//
// Build defines from the host: IGM_LANES, IGM_REGS, IGM_DAG_LOADS, IGM_CACHE_WORDS,
// IGM_ROUNDS, IGM_WAVE (the queried preferred work-group size multiple, asserted
// in-kernel when sub-groups are available), IGM_LANE_SHUFFLE (0 or 1; 1 only when
// cl_khr_subgroups and cl_khr_subgroup_shuffle are present and IGM_WAVE % IGM_LANES == 0).

#if IGM_LANE_SHUFFLE
#pragma OPENCL EXTENSION cl_khr_subgroups : enable
#pragma OPENCL EXTENSION cl_khr_subgroup_shuffle : enable
#endif

#define IGM_MAX_REGS 32
#define IGM_MAX_DAG_LOADS 4

typedef struct {
    uint header[8];
    uint target[8];
    uint nonce_lo, nonce_hi;
    uint dag_entries;
    uint hashes;
    uint max_results;
    uint pad_first[15];
    uint pad_final[9];
} igm_job;

typedef struct { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; } igm_result;

inline uint fnv1a(uint h, uint d) { return (h ^ d) * 0x1000193u; }
#define FNV_OFFSET_BASIS 0x811c9dc5u

typedef struct { uint z, w, jsr, jcong; } kiss99_t;
inline uint kiss99(kiss99_t *st) {
    st->z = 36969u * (st->z & 65535u) + (st->z >> 16);
    st->w = 18000u * (st->w & 65535u) + (st->w >> 16);
    uint MWC = (st->z << 16) + st->w;
    st->jsr ^= (st->jsr << 17); st->jsr ^= (st->jsr >> 13); st->jsr ^= (st->jsr << 5);
    st->jcong = 69069u * st->jcong + 1234567u;
    return ((MWC ^ st->jcong) + st->jsr);
}

inline uint bswap32(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }

__constant uint keccakf_rndc[22] = {
    0x00000001u, 0x00008082u, 0x0000808Au, 0x80008000u, 0x0000808Bu, 0x80000001u,
    0x80008081u, 0x00008009u, 0x0000008Au, 0x00000088u, 0x80008009u, 0x8000000Au,
    0x8000808Bu, 0x0000008Bu, 0x00008089u, 0x00008003u, 0x00008002u, 0x00000080u,
    0x0000800Au, 0x8000000Au, 0x80008081u, 0x00008080u
};
__constant uint keccakf_rotc[24] = { 1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44 };
__constant uint keccakf_piln[24] = { 10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1 };

inline void keccak_f800(uint *st) {
    for (uint r = 0; r < 22; r++) {
        uint bc[5];
        for (uint i = 0; i < 5; i++) bc[i] = st[i] ^ st[i + 5] ^ st[i + 10] ^ st[i + 15] ^ st[i + 20];
        for (uint i = 0; i < 5; i++) {
            uint t = bc[(i + 4) % 5] ^ rotate(bc[(i + 1) % 5], 1u);
            for (uint j = 0; j < 25; j += 5) st[j + i] ^= t;
        }
        uint t = st[1];
        for (uint i = 0; i < 24; i++) {
            uint j = keccakf_piln[i];
            uint tmp = st[j];
            st[j] = rotate(t, keccakf_rotc[i] & 31u);
            t = tmp;
        }
        for (uint j = 0; j < 25; j += 5) {
            for (uint i = 0; i < 5; i++) bc[i] = st[j + i];
            for (uint i = 0; i < 5; i++) st[j + i] ^= (~bc[(i + 1) % 5]) & bc[(i + 2) % 5];
        }
        st[0] ^= keccakf_rndc[r];
    }
}

inline void fill_mix(uint seed0, uint seed1, uint lane_id, uint *mix) {
    kiss99_t st;
    st.z = fnv1a(FNV_OFFSET_BASIS, seed0);
    st.w = fnv1a(st.z, seed1);
    st.jsr = fnv1a(st.w, lane_id);
    st.jcong = fnv1a(st.jsr, lane_id);
    for (uint i = 0; i < IGM_REGS; i++) mix[i] = kiss99(&st);
}

// ---- the per-period generated program (spliced by the host) ----
//   inline void igm_period_program(uint *mix, __local const uint *c_dag, const uint *dag_entry);
// IGM_PERIOD_PROGRAM

inline uint igm_leader_mix0(uint mix0, uint leader_lane, uint lane, uint group_in_wg, __local uint *bcast) {
#if IGM_LANE_SHUFFLE
    uint base = get_sub_group_local_id() & ~(IGM_LANES - 1u);
    return sub_group_shuffle(mix0, base + leader_lane);
#else
    if (lane == leader_lane) bcast[group_in_wg] = mix0;
    barrier(CLK_LOCAL_MEM_FENCE);
    uint v = bcast[group_in_wg];
    barrier(CLK_LOCAL_MEM_FENCE);
    return v;
#endif
}

__kernel void igm_progpow_search(__constant igm_job *job,
                                 __global const uint *dag,
                                 __global igm_result *results,
                                 __global uint *result_count,
                                 __global uint *fault,
                                 __local uint *lmem)
{
    uint tid = get_local_id(0);
    uint wg = get_local_size(0);
    uint gid = get_global_id(0);
#if IGM_LANE_SHUFFLE
    if (get_sub_group_size() != IGM_WAVE) { if (gid == 0) fault[0] = 1u; return; }
#endif
    __local uint *c_dag = lmem;
    __local uint *bcast = lmem + IGM_CACHE_WORDS;
    uint groups_in_wg = wg / IGM_LANES;
    __local uint *lanedg = bcast + groups_in_wg;

    for (uint i = tid; i < IGM_CACHE_WORDS; i += wg) c_dag[i] = dag[i];
    barrier(CLK_LOCAL_MEM_FENCE);

    uint lane = tid % IGM_LANES;
    uint group_in_wg = tid / IGM_LANES;
    uint hash_index = gid / IGM_LANES;
    bool active = hash_index < job->hashes;

    uint nonce_lo = job->nonce_lo + hash_index;
    uint nonce_hi = job->nonce_hi + (nonce_lo < job->nonce_lo ? 1u : 0u);

    uint st[25];
    for (uint i = 0; i < 8; i++) st[i] = job->header[i];
    st[8] = nonce_lo; st[9] = nonce_hi;
    for (uint i = 0; i < 15; i++) st[10 + i] = job->pad_first[i];
    keccak_f800(st);
    uint state2[8];
    for (uint i = 0; i < 8; i++) state2[i] = st[i];

    uint mix[IGM_MAX_REGS];
    fill_mix(state2[0], state2[1], lane, mix);

    for (uint r = 0; r < IGM_ROUNDS; r++) {
        uint leader = r % IGM_LANES;
        uint lead_mix0 = igm_leader_mix0(mix[0], leader, lane, group_in_wg, bcast);
        uint entry = lead_mix0 % job->dag_entries;
        uint offset = entry * IGM_LANES * IGM_DAG_LOADS + ((lane ^ r) % IGM_LANES) * IGM_DAG_LOADS;
        uint dag_entry[IGM_MAX_DAG_LOADS];
        for (uint i = 0; i < IGM_DAG_LOADS; i++) dag_entry[i] = active ? dag[offset + i] : 0u;
        igm_period_program(mix, c_dag, dag_entry);
    }

    uint lane_hash = FNV_OFFSET_BASIS;
    for (uint i = 0; i < IGM_REGS; i++) lane_hash = fnv1a(lane_hash, mix[i]);

    uint mixh[8];
    for (uint i = 0; i < 8; i++) mixh[i] = FNV_OFFSET_BASIS;
#if IGM_LANE_SHUFFLE
    {
        uint base = get_sub_group_local_id() & ~(IGM_LANES - 1u);
        for (uint l = 0; l < IGM_LANES; l++) { uint v = sub_group_shuffle(lane_hash, base + l); mixh[l % 8] = fnv1a(mixh[l % 8], v); }
    }
#else
    lanedg[group_in_wg * IGM_LANES + lane] = lane_hash;
    barrier(CLK_LOCAL_MEM_FENCE);
    for (uint l = 0; l < IGM_LANES; l++) mixh[l % 8] = fnv1a(mixh[l % 8], lanedg[group_in_wg * IGM_LANES + l]);
    barrier(CLK_LOCAL_MEM_FENCE);
#endif

    if (!active || lane != 0u) return;

    for (uint i = 0; i < 8; i++) st[i] = state2[i];
    for (uint i = 0; i < 8; i++) st[8 + i] = mixh[i];
    for (uint i = 0; i < 9; i++) st[16 + i] = job->pad_final[i];
    keccak_f800(st);

    bool below = true;
    for (uint i = 0; i < 8; i++) {
        uint h = bswap32(st[i]), t = job->target[i];
        if (h < t) { below = true; break; }
        if (h > t) { below = false; break; }
    }
    if (!below) return;

    uint slot = atomic_inc(result_count);
    if (slot >= job->max_results) return;
    results[slot].nonce_lo = nonce_lo;
    results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mixh[i]; results[slot].digest[i] = st[i]; }
}
