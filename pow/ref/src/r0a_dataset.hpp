// The IgneumHash-R0A version 1 dataset builder (SPEC-R0A-v0.md section 5.3,
// revision 0.6 text): the sequential per-epoch cache and the parent walk, as
// a function of the epoch seed E and the sizes alone. Shared by the R0A v1
// reference (igneumhash.cpp) and the RXE1 experimental family (rxe1.cpp),
// which binds to it by this call and by nothing else (SPEC-TEXT-v0.1.md
// section 6, "Dataset interface that must be bound"). Any change here
// changes R0A's vectors, which are immutable: the vector check is the guard.
#pragma once

#include "blake3.h"

#include <cstdint>
#include <cstring>
#include <vector>

namespace ignpow {
namespace r0a {

constexpr uint32_t kItemBytes = 128;
inline const char* const kTagItem = "IgneumHash/R0A/item";
inline const char* const kTagCache = "IgneumHash/R0A/cache";

inline uint32_t le32(const uint8_t* p) { return uint32_t(p[0]) | (uint32_t(p[1]) << 8) | (uint32_t(p[2]) << 16) | (uint32_t(p[3]) << 24); }
inline void put_le32(uint8_t* p, uint32_t v) { p[0] = uint8_t(v); p[1] = uint8_t(v >> 8); p[2] = uint8_t(v >> 16); p[3] = uint8_t(v >> 24); }
inline uint32_t fnv1(uint32_t a, uint32_t b) { return (a * 0x01000193u) ^ b; }

// tag(T) || u64be(i): the envelope's tag framing (SPEC-R0A-v0.md section 1).
inline void tag_be64(std::vector<uint8_t>& v, const char* tag, uint64_t i) {
    const size_t n = std::strlen(tag);
    v.push_back(uint8_t(n));
    v.insert(v.end(), tag, tag + n);
    for (int k = 0; k < 8; ++k) v.push_back(uint8_t(i >> (8 * (7 - k))));
}

// The cache: N_CACHE = cache_bytes / 128 items of 128 bytes, cache(0) keyed
// over tag || u64be(0), cache(j) over tag || u64be(j) || cache(j-1)[0..32).
inline void build_cache(const uint8_t E[32], uint64_t cache_bytes, std::vector<uint8_t>& cache) {
    const uint64_t n = cache_bytes / kItemBytes;
    cache.resize(size_t(cache_bytes));
    for (uint64_t j = 0; j < n; ++j) {
        std::vector<uint8_t> in;
        tag_be64(in, kTagCache, j);
        if (j > 0) in.insert(in.end(), cache.data() + (j - 1) * kItemBytes, cache.data() + (j - 1) * kItemBytes + 32);
        blake3_hasher h;
        blake3_hasher_init_keyed(&h, E);
        blake3_hasher_update(&h, in.data(), in.size());
        blake3_hasher_finalize(&h, cache.data() + j * kItemBytes, kItemBytes);
    }
}

// item(i): mix = cache(i mod N_CACHE) with the index folded in, PARENTS steps
// of parent = fnv(lo32(i) ^ p, mix[p mod 32]) mod N_CACHE and mix[k] =
// fnv(mix[k], cache(parent)[k]), then the keyed XOF over tag || u64be(i) || mix.
inline void derive_item(const uint8_t E[32], const std::vector<uint8_t>& cache, uint32_t parents, uint64_t i, uint8_t out[kItemBytes]) {
    const uint64_t nc = cache.size() / kItemBytes;
    const uint32_t lo = uint32_t(i), hi = uint32_t(i >> 32);
    uint32_t mix[32];
    const uint8_t* base = cache.data() + (i % nc) * kItemBytes;
    for (int k = 0; k < 32; ++k) mix[k] = le32(base + 4 * k);
    mix[0] ^= lo; mix[1] ^= hi;
    for (uint32_t pp = 0; pp < parents; ++pp) {
        const uint64_t parent = fnv1(lo ^ pp, mix[pp & 31]) % nc;
        const uint8_t* src = cache.data() + parent * kItemBytes;
        for (int k = 0; k < 32; ++k) mix[k] = fnv1(mix[k], le32(src + 4 * k));
    }
    uint8_t mb[kItemBytes];
    for (int k = 0; k < 32; ++k) put_le32(mb + 4 * k, mix[k]);
    std::vector<uint8_t> in;
    tag_be64(in, kTagItem, i);
    blake3_hasher h;
    blake3_hasher_init_keyed(&h, E);
    blake3_hasher_update(&h, in.data(), in.size());
    blake3_hasher_update(&h, mb, kItemBytes);
    blake3_hasher_finalize(&h, out, kItemBytes);
}

// A W-byte dataset item (the item-width variant, SPEC-R0A-v0.md section 0.1 version 3; W = 128 is item(i) above
// word for word): the walk runs over runs of W / 128 consecutive cache items (wrapping mod N_CACHE), parents of the
// same width, the fold over all W / 4 words, the keyed XOF of W bytes.
inline void derive_item_w(const uint8_t E[32], const std::vector<uint8_t>& cache, uint32_t parents, uint32_t W, uint64_t i, uint8_t* out) {
    const uint64_t nc = cache.size() / kItemBytes;
    const uint32_t run_items = W < kItemBytes ? 1 : W / kItemBytes;  // W = 64 (version 4's narrow control): the version 1 walk, the output cut to W
    const uint32_t words = 32 * run_items;
    const uint32_t lo = uint32_t(i), hi = uint32_t(i >> 32);
    std::vector<uint32_t> mix(words);
    auto load_run = [&](uint64_t first, uint32_t* dst) {
        for (uint32_t r = 0; r < run_items; ++r) {
            const uint8_t* src = cache.data() + ((first + r) % nc) * kItemBytes;
            for (int k = 0; k < 32; ++k) dst[r * 32 + k] = le32(src + 4 * k);
        }
    };
    load_run(i % nc, mix.data());
    mix[0] ^= lo; mix[1] ^= hi;
    std::vector<uint32_t> par(words);
    for (uint32_t pp = 0; pp < parents; ++pp) {
        const uint64_t parent = fnv1(lo ^ pp, mix[pp % words]) % nc;
        load_run(parent, par.data());
        for (uint32_t k = 0; k < words; ++k) mix[k] = fnv1(mix[k], par[k]);
    }
    std::vector<uint8_t> mb(4 * words);
    for (uint32_t k = 0; k < words; ++k) put_le32(mb.data() + 4 * k, mix[k]);
    std::vector<uint8_t> in;
    tag_be64(in, kTagItem, i);
    blake3_hasher h;
    blake3_hasher_init_keyed(&h, E);
    blake3_hasher_update(&h, in.data(), in.size());
    blake3_hasher_update(&h, mb.data(), 4 * words);
    blake3_hasher_finalize(&h, out, W);
}

}  // namespace r0a
}  // namespace ignpow
