// Candidate C2: FishHash as implemented in iron-fish/fish-hash at commit
// 823531c (cpp/FishHash.cpp), measured but not vendored (D-GA-04): the build
// includes the upstream translation unit from the manifest-fetched tree
// (pow/third_party/fish-hash-823531c/src/cpp) when IGNPOW_FISHHASH_SRC is set,
// so its inline kernel, cache builder and bundled BLAKE3 and keccak are the
// ones measured. This file adds only the ABI mapping:
//   input = header (32 bytes) ++ nonce (8 bytes little-endian), the "header"
//   the upstream hash() takes; seed = BLAKE3-64(input); mix = the upstream
//   fishhash_kernel(ctx, seed); final = BLAKE3-32(seed ++ mix), exactly the
//   upstream hash() with the mix exposed. No height dependence: one dataset
//   (4,831,835,776 bytes) and one light cache (75,497,024 bytes) for ever;
//   epoch is 0 at every height.
// The upstream context is a process-wide shared object (get_context); the
// reference wraps the pointer and never frees it. A full context is lazy (items
// computed on first touch) unless prebuilt by the caller.

#include "candidate.hpp"

#ifdef IGNPOW_HAVE_FISHHASH

#include "cpp/FishHash.cpp"  // the upstream translation unit through its own directory (IGNPOW_FISHHASH_SRC is the fetched src root), so no name collides with this file on a case-insensitive filesystem

#include <crypto/ethash/include/ethash/keccak.hpp>

#include <cstring>
#include <memory>
#include <string>

namespace ignpow {
namespace {

bool params_default(const char* p) {
    if (p == nullptr) return true;
    std::string s(p);
    size_t a = s.find_first_not_of(" \t\r\n");
    if (a == std::string::npos) return true;
    size_t b = s.find_last_not_of(" \t\r\n");
    return s.substr(a, b - a + 1) == "{}";
}

int fh_check_params(const char* p) { return params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }

uint64_t fh_epoch_of(const char* p, uint64_t, int* status) { if (status) *status = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; return 0; }
uint64_t fh_cache_bytes(const char* p, uint64_t, int* status) { if (status) *status = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; return 1179641ull * 64; }
uint64_t fh_dataset_bytes(const char* p, uint64_t, int* status) { if (status) *status = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; return 37748717ull * 128; }

struct fh_ctx final : ctx_base {
    FishHash::fishhash_context* ctx = nullptr;
    uint64_t epoch() const override { return 0; }
    const char* algo() const override { return IGNPOW_ALGO_FISHHASH_1; }
};

ctx_base* fh_create(const char* p, uint64_t, int full, int* status) {
    if (!params_default(p)) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    auto c = std::make_unique<fh_ctx>();
    c->ctx = FishHash::get_context(full != 0);
    if (!c->ctx) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    if (status) *status = IGNPOW_OK;
    return c.release();
}

void fh_compute(const FishHash::fishhash_context& ctx, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    uint8_t input[40];
    std::memcpy(input, header, 32);
    for (int i = 0; i < 8; ++i) input[32 + i] = static_cast<uint8_t>(nonce >> (8 * i));
    FishHash::hash512 seed;
    blake3_hasher hasher;
    blake3_hasher_init(&hasher);
    blake3_hasher_update(&hasher, input, sizeof input);
    blake3_hasher_finalize(&hasher, seed.bytes, 64);
    const FishHash::hash256 mix = FishHash::fishhash_kernel(ctx, seed);
    uint8_t final_data[64 + 32];
    std::memcpy(final_data, seed.bytes, 64);
    std::memcpy(final_data + 64, mix.bytes, 32);
    blake3_hasher_init(&hasher);
    blake3_hasher_update(&hasher, final_data, sizeof final_data);
    blake3_hasher_finalize(&hasher, hash_out, 32);
    std::memcpy(mix_out, mix.bytes, 32);
}

int fh_hash(const ctx_base* ctx, uint64_t, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    fh_compute(*static_cast<const fh_ctx*>(ctx)->ctx, header, nonce, mix_out, hash_out);
    return IGNPOW_OK;
}

int fh_verify(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32];
    fh_hash(ctx, height, header, nonce, m, h);
    const bool mix_ok = std::memcmp(m, mix, 32) == 0;
    const bool bound_ok = le256(h, boundary);
    if (!mix_ok) return IGNPOW_E_MIX;
    if (!bound_ok) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}

int fh_light_cache(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    const auto& c = *static_cast<const fh_ctx*>(ctx);
    if (data) *data = c.ctx->light_cache;
    if (bytes) *bytes = static_cast<uint64_t>(c.ctx->light_cache_num_items) * 64;
    if (item_bytes) *item_bytes = 64;
    return IGNPOW_OK;
}

// Embedded checks: (1) the upstream's keccak-512 agrees with the vendored
// Ravencoin keccak on the cache seed (two independent keccak codes, one
// result); (2) the upstream hash() and this file's mapping agree on a fixed
// input (the mix-exposing path must be the upstream path). No upstream test
// vector file exists at the pinned commit; a self-recorded digest would prove
// nothing and is not embedded.
int fh_vectors_check(size_t* count_out) {
    size_t n = 0;
    FishHash::hash512 item;
    keccak(item.word64s, 512, FishHash::seed.bytes, 32);  // the upstream's bundled keccak (global, from its 3rdParty/keccak.c)
    const ethash::hash512 ref = ethash::keccak512(FishHash::seed.bytes, 32);
    if (std::memcmp(item.bytes, ref.bytes, 64) != 0) return IGNPOW_E_VECTORS;
    ++n;
    FishHash::fishhash_context* ctx = FishHash::get_context(false);
    if (!ctx) return IGNPOW_E_VECTORS;
    uint8_t header[32];
    for (int i = 0; i < 32; ++i) header[i] = static_cast<uint8_t>(i);
    uint8_t mix[32], ours[32], theirs[32];
    fh_compute(*ctx, header, 0x0102030405060708ull, mix, ours);
    uint8_t input[40];
    std::memcpy(input, header, 32);
    const uint64_t nonce = 0x0102030405060708ull;
    for (int i = 0; i < 8; ++i) input[32 + i] = static_cast<uint8_t>(nonce >> (8 * i));
    FishHash::hash(theirs, ctx, input, sizeof input);
    if (std::memcmp(ours, theirs, 32) != 0) return IGNPOW_E_VECTORS;
    ++n;
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace

const candidate fishhash_1 = {
    IGNPOW_ALGO_FISHHASH_1,
    fh_check_params,
    fh_epoch_of,
    fh_cache_bytes,
    fh_dataset_bytes,
    fh_create,
    fh_hash,
    fh_verify,
    fh_light_cache,
    fh_vectors_check,
    "iron-fish/fish-hash@823531c (measure-only, D-GA-04; 4.50 GiB fixed dataset, blake3 seed and final)",
};

}  // namespace ignpow

#endif  // IGNPOW_HAVE_FISHHASH
