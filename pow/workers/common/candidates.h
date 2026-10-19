// candidates.h: the per-candidate constants the GPU workers need to generate and run a
// kernel. Every value here is pinned to the sources GPU engineer A's reference builds
// from, with the source line named, so a worker never carries a constant the reference
// does not. The ask to A: expose these through ignpow.h so this table can be deleted.
// GPU engineer B, 9 October 2026.
#pragma once
#include <cstdint>
#include <cstring>

namespace igm {

enum class LaneModel { cooperative, independent };
enum class Kind { progpow, fishhash, autolykos, igneumhash, rxe1 };

struct Candidate {
    const char *id;            // ignpow candidate id
    const char *pin;           // the source the constants come from
    LaneModel lane_model;
    Kind kind;
    // ProgPoW family
    uint32_t period;           // progpow.hpp: period_length
    uint32_t lanes;            // progpow.hpp: num_lanes
    uint32_t regs;             // progpow.hpp: num_regs
    uint32_t cache_accesses;   // progpow.hpp: num_cache_accesses
    uint32_t math_ops;         // progpow.hpp: num_math_operations
    uint32_t cache_bytes;      // progpow.hpp: l1_cache_size
    uint32_t rounds;           // progpow.cpp hash_mix: 64
    uint32_t entry_bytes;      // progpow.cpp round: sizeof(hash2048) = 256
    uint32_t dag_loads;        // entry_bytes / (4 * lanes) = 4
    uint32_t pad_first[15];    // progpow.cpp hash(): state[10..24] of the first absorb
    uint32_t pad_final[9];     // progpow.cpp hash(): state[16..24] of the final absorb
    // dataset (Ethash lineage: KAWPOW and FishHash)
    uint32_t item_bytes;       // 64 (one 512-bit item; FishHash indexes pairs as 1024-bit items)
    uint32_t dataset_parents;  // 512 in both pinned trees (ethash-internal.hpp line 27; FishHash.cpp line 24)
    uint32_t cache_item_bytes; // 64
};

// kawpow-0.9.4 as vendored at pow/third_party/ravencoin-ethash-22549129 (Ravencoin
// v4.8.0 library). First absorb: header words 0..7, nonce 8..9, then the 15 words
// "RAVENCOINKAWPOW" at 10..24. Final absorb: the first keccak's output words 0..7, the
// mix words at 8..15, then the first 9 pad words at 16..24.
inline const Candidate kKawpow094 = {
    "kawpow-0.9.4", "ravencoin-ethash-22549129", LaneModel::cooperative, Kind::progpow,
    3, 16, 32, 11, 18, 16384, 64, 256, 4,
    { 0x00000072u, 0x00000041u, 0x00000056u, 0x00000045u, 0x0000004Eu, 0x00000043u, 0x0000004Fu, 0x00000049u,
      0x0000004Eu, 0x0000004Bu, 0x00000041u, 0x00000057u, 0x00000050u, 0x0000004Fu, 0x00000057u },
    { 0x00000072u, 0x00000041u, 0x00000056u, 0x00000045u, 0x0000004Eu, 0x00000043u, 0x0000004Fu, 0x00000049u, 0x0000004Eu },
    64, 512, 64
};

// fishhash-1 as fetched at pow/third_party/fish-hash-823531c (measure-only, D-GA-04):
// one fixed dataset of 37,748,717 1024-bit items from a 1,179,641-item cache, 512 parents.
inline const Candidate kFishhash1 = {
    "fishhash-1", "fish-hash-823531c", LaneModel::independent, Kind::fishhash,
    0, 1, 0, 0, 0, 0, 32, 128, 0, {0}, {0},
    64, 512, 64
};

// autolykos-2 as reproduced in pow/ref/src/autolykos.cpp (ergo v6.0.7): the element
// table is N(height) x 32 bytes, rebuilt per height; no light cache.
inline const Candidate kAutolykos2 = {
    "autolykos-2", "ergo-v6.0.7 AutolykosPowScheme via pow/ref/src/autolykos.cpp", LaneModel::independent, Kind::autolykos,
    0, 1, 0, 0, 0, 0, 0, 32, 0, {0}, {0},
    32, 0, 0
};

// igneumhash-r0a: the research candidate of docs/igneumhash (R0A v0); its parameters are the
// ablation variables carried in the params document, never constants here; eight lanes.
inline const Candidate kIgneumhashR0a = {
    "igneumhash", "docs/igneumhash/SPEC-R0A-v0.md revision 0.1 (the hash team's reference in pow/ref)", LaneModel::independent, Kind::igneumhash,
    0, 8, 0, 0, 0, 0, 0, 0, 0, {0}, {0},
    0, 0, 0
};
inline const Candidate kRxe1 = {
    "rxe1", "docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md sections 6 to 8 (IH 06, the research branch; the hash team's IH 04 reference)", LaneModel::independent, Kind::rxe1,
    0, 8, 0, 0, 0, 0, 0, 0, 0, {0}, {0},
    0, 0, 0
};

inline const Candidate *find_candidate(const char *id) {
    if (!id) return nullptr;
    if (std::strcmp(id, kIgneumhashR0a.id) == 0) return &kIgneumhashR0a;
    if (std::strcmp(id, kRxe1.id) == 0) return &kRxe1;
    if (std::strcmp(id, kKawpow094.id) == 0) return &kKawpow094;
    if (std::strcmp(id, kFishhash1.id) == 0) return &kFishhash1;
    if (std::strcmp(id, kAutolykos2.id) == 0) return &kAutolykos2;
    return nullptr;
}

// Autolykos N(height) as pow/ref/src/autolykos.cpp calc_n: 2^26, then 5 percent growth
// per 51,200 blocks from height 614,400, capped at height 4,198,400, in Int arithmetic
// (n / 100 * 105, the division first).
inline uint32_t autolykos_n(uint64_t height) {
    const uint64_t start = 600ull * 1024ull, period = 50ull * 1024ull, hmax = 4198400ull;
    uint64_t h = height < hmax ? height : hmax;
    if (h < start) return 1u << 26;
    uint64_t iters = (h - start) / period + 1;
    int64_t n = 1 << 26;
    for (uint64_t i = 0; i < iters; i++) n = n / 100 * 105;
    return (uint32_t)n;
}


// The kernel's own logical dataset bytes per hash (SPEC-TEXT section 14: compare logical bytes read with the measured
// traffic): KAWPOW 64 DAG loads of 256 bytes; FishHash 32 rounds of one 128-byte item; Autolykos v2 k = 32 table
// elements of 32 bytes; IgneumHash and RXE1 rounds x 128 bytes (the engines report those from their params).
inline uint64_t bytes_read_per_hash(Kind k) {
    switch (k) { case Kind::progpow: return 64ull * 256; case Kind::fishhash: return 32ull * 128; case Kind::autolykos: return 32ull * 32; default: return 0; }
}

}  // namespace igm
