// igm_progpow.metal: the ProgPoW/KAWPOW-family search kernel for Apple Silicon.
// GPU engineer B, 9 October 2026. Source only; not built, not run.
//
// The flow follows the vendored reference byte for byte (pow/third_party/
// ravencoin-ethash-22549129, progpow.cpp hash()): first keccak-f800 over header words
// 0..7, nonce 8..9 and the candidate's 15 pad words 10..24; seed = output words 0 and 1;
// fill_mix per lane; 64 rounds of the same generated program (the reference takes the
// RNG state by value per round); per-lane fnv1a reduce; lane fold into 8 words; final
// keccak-f800 over the first keccak's output words 0..7, the mix words 8..15 and the
// candidate's 9 pad words 16..24.
//
// Lane model (DESIGN.md 3.3): L = IGM_LANES threads cooperate on one hash. When the
// pipeline's queried thread execution width W satisfies W % L == 0 the host sets
// IGM_LANE_SHUFFLE and lanes exchange through SIMD-group shuffles (no memory, no
// barrier: R24 requires barriers for memory operations, and a shuffle is not one).
// Otherwise lanes exchange through threadgroup memory with threadgroup_barrier around
// every exchange, as R24 requires. Both paths run in HW-01 on every machine.

#include <metal_stdlib>
using namespace metal;

constant uint IGM_LANES        [[function_constant(0)]];   // 16
constant uint IGM_REGS         [[function_constant(1)]];   // 32
constant uint IGM_DAG_LOADS    [[function_constant(2)]];   // 4
constant uint IGM_CACHE_WORDS  [[function_constant(3)]];   // 4096
constant uint IGM_ROUNDS       [[function_constant(4)]];   // 64
constant bool IGM_LANE_SHUFFLE [[function_constant(5)]];   // width-derived (host)
constant uint IGM_SIMD_WIDTH   [[function_constant(6)]];   // the host's threadExecutionWidth, asserted in-kernel

#define IGM_MAX_REGS 32
#define IGM_MAX_DAG_LOADS 4

struct igm_job {
    uint header[8];            // header hash as 8 little-endian words (the reference's word32s)
    uint target[8];            // 256-bit target as big-endian words (bytes 0..3 in word 0)
    uint nonce_lo, nonce_hi;   // first nonce of this batch
    uint dag_entries;          // dataset bytes / 256
    uint hashes;               // hashes in this batch (threads = hashes * lanes)
    uint max_results;
    uint pad_first[15];        // keccak words 10..24 of the first absorb
    uint pad_final[9];         // keccak words 16..24 of the final absorb
};

struct igm_result { uint nonce_lo, nonce_hi; uint mix[8]; uint digest[8]; };

inline uint fnv1a(uint h, uint d) { return (h ^ d) * 0x1000193u; }
constant uint FNV_OFFSET_BASIS = 0x811c9dc5u;

struct kiss99_t { uint z, w, jsr, jcong; };
inline uint kiss99(thread kiss99_t &st) {
    st.z = 36969u * (st.z & 65535u) + (st.z >> 16);
    st.w = 18000u * (st.w & 65535u) + (st.w >> 16);
    uint MWC = (st.z << 16) + st.w;
    st.jsr ^= (st.jsr << 17); st.jsr ^= (st.jsr >> 13); st.jsr ^= (st.jsr << 5);
    st.jcong = 69069u * st.jcong + 1234567u;
    return ((MWC ^ st.jcong) + st.jsr);
}

inline uint bswap32(uint x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }

// ---- keccak-f800, 22 rounds, as the vendored keccakf800.c ----
constant uint keccakf_rndc[22] = {
    0x00000001u, 0x00008082u, 0x0000808Au, 0x80008000u, 0x0000808Bu, 0x80000001u,
    0x80008081u, 0x00008009u, 0x0000008Au, 0x00000088u, 0x80008009u, 0x8000000Au,
    0x8000808Bu, 0x0000008Bu, 0x00008089u, 0x00008003u, 0x00008002u, 0x00000080u,
    0x0000800Au, 0x8000000Au, 0x80008081u, 0x00008080u
};
constant uint keccakf_rotc[24] = { 1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44 };
constant uint keccakf_piln[24] = { 10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1 };

inline void keccak_f800(thread uint st[25]) {
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

inline void fill_mix(uint seed0, uint seed1, uint lane_id, thread uint mix[IGM_MAX_REGS]) {
    kiss99_t st;
    st.z = fnv1a(FNV_OFFSET_BASIS, seed0);
    st.w = fnv1a(st.z, seed1);
    st.jsr = fnv1a(st.w, lane_id);
    st.jcong = fnv1a(st.jsr, lane_id);
    for (uint i = 0; i < IGM_REGS; i++) mix[i] = kiss99(st);
}

// ---- the per-period generated program (spliced by the host) ----
//   inline void igm_period_program(thread uint mix[IGM_MAX_REGS],
//                                  threadgroup const uint *c_dag,
//                                  thread const uint dag_entry[IGM_MAX_DAG_LOADS]);
// IGM_PERIOD_PROGRAM

inline uint igm_leader_mix0(uint mix0, uint leader_lane, uint lane, uint group_in_tg, uint simd_lane, threadgroup uint *bcast) {
    if (IGM_LANE_SHUFFLE) {
        uint base = simd_lane & ~(IGM_LANES - 1u);
        return simd_shuffle(mix0, (ushort)(base + leader_lane));
    } else {
        if (lane == leader_lane) bcast[group_in_tg] = mix0;
        threadgroup_barrier(mem_flags::mem_threadgroup);
        uint v = bcast[group_in_tg];
        threadgroup_barrier(mem_flags::mem_threadgroup);
        return v;
    }
}

// Grid: hashes * IGM_LANES threads. Threadgroup memory layout: cache words, then one
// broadcast slot per logical group, then IGM_LANES digest words per logical group.
kernel void igm_progpow_search(constant igm_job &job            [[buffer(0)]],
                               device const uint *dag           [[buffer(1)]],
                               device igm_result *results       [[buffer(2)]],
                               device atomic_uint *result_count [[buffer(3)]],
                               device uint *fault               [[buffer(4)]],
                               threadgroup uint *tg             [[threadgroup(0)]],
                               uint tid_in_tg  [[thread_index_in_threadgroup]],
                               uint tg_size    [[threads_per_threadgroup]],
                               uint simd_lane  [[thread_index_in_simdgroup]],
                               uint simd_width [[threads_per_simdgroup]],
                               uint gid        [[thread_position_in_grid]])
{
    if (simd_width != IGM_SIMD_WIDTH) { if (gid == 0) fault[0] = 1u; return; }   // a mismatch is a fault, never a reshaping

    threadgroup uint *c_dag = tg;
    threadgroup uint *bcast = tg + IGM_CACHE_WORDS;
    uint groups_in_tg = tg_size / IGM_LANES;
    threadgroup uint *lanedg = bcast + groups_in_tg;

    for (uint i = tid_in_tg; i < IGM_CACHE_WORDS; i += tg_size) c_dag[i] = dag[i];   // l1 cache = first 16 KB of the dataset
    threadgroup_barrier(mem_flags::mem_threadgroup);

    uint lane = tid_in_tg % IGM_LANES;
    uint group_in_tg = tid_in_tg / IGM_LANES;
    uint hash_index = gid / IGM_LANES;
    bool active = hash_index < job.hashes;   // tails: inactive lanes still join barriers

    uint nonce_lo = job.nonce_lo + hash_index;
    uint nonce_hi = job.nonce_hi + (nonce_lo < job.nonce_lo ? 1u : 0u);

    // first keccak: header 0..7, nonce 8..9, pad 10..24
    uint st[25];
    for (uint i = 0; i < 8; i++) st[i] = job.header[i];
    st[8] = nonce_lo; st[9] = nonce_hi;
    for (uint i = 0; i < 15; i++) st[10 + i] = job.pad_first[i];
    keccak_f800(st);
    uint state2[8];
    for (uint i = 0; i < 8; i++) state2[i] = st[i];

    uint mix[IGM_MAX_REGS];
    fill_mix(state2[0], state2[1], lane, mix);

    for (uint r = 0; r < IGM_ROUNDS; r++) {
        uint leader = r % IGM_LANES;
        uint lead_mix0 = igm_leader_mix0(mix[0], leader, lane, group_in_tg, simd_lane, bcast);
        uint entry = lead_mix0 % job.dag_entries;
        uint offset = entry * IGM_LANES * IGM_DAG_LOADS + ((lane ^ r) % IGM_LANES) * IGM_DAG_LOADS;
        uint dag_entry[IGM_MAX_DAG_LOADS];
        for (uint i = 0; i < IGM_DAG_LOADS; i++) dag_entry[i] = active ? dag[offset + i] : 0u;
        igm_period_program(mix, c_dag, dag_entry);
    }

    uint lane_hash = FNV_OFFSET_BASIS;
    for (uint i = 0; i < IGM_REGS; i++) lane_hash = fnv1a(lane_hash, mix[i]);

    uint mixh[8];
    for (uint i = 0; i < 8; i++) mixh[i] = FNV_OFFSET_BASIS;
    if (IGM_LANE_SHUFFLE) {
        uint base = simd_lane & ~(IGM_LANES - 1u);
        for (uint l = 0; l < IGM_LANES; l++) {
            uint v = simd_shuffle(lane_hash, (ushort)(base + l));
            mixh[l % 8] = fnv1a(mixh[l % 8], v);
        }
    } else {
        lanedg[group_in_tg * IGM_LANES + lane] = lane_hash;
        threadgroup_barrier(mem_flags::mem_threadgroup);
        for (uint l = 0; l < IGM_LANES; l++) mixh[l % 8] = fnv1a(mixh[l % 8], lanedg[group_in_tg * IGM_LANES + l]);
        threadgroup_barrier(mem_flags::mem_threadgroup);
    }

    if (!active || lane != 0u) return;

    // final keccak: state2 0..7, mix 8..15, pad 16..24
    for (uint i = 0; i < 8; i++) st[i] = state2[i];
    for (uint i = 0; i < 8; i++) st[8 + i] = mixh[i];
    for (uint i = 0; i < 9; i++) st[16 + i] = job.pad_final[i];
    keccak_f800(st);

    // final <= target as big-endian 256-bit values (the reference's le256 on bytes)
    bool below = true;
    for (uint i = 0; i < 8; i++) {
        uint h = bswap32(st[i]), t = job.target[i];
        if (h < t) { below = true; break; }
        if (h > t) { below = false; break; }
    }
    if (!below) return;

    uint slot = atomic_fetch_add_explicit(result_count, 1u, memory_order_relaxed);
    if (slot >= job.max_results) return;
    results[slot].nonce_lo = nonce_lo;
    results[slot].nonce_hi = nonce_hi;
    for (uint i = 0; i < 8; i++) { results[slot].mix[i] = mixh[i]; results[slot].digest[i] = st[i]; }
}
