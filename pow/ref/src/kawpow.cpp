// Candidate C1: the ProgPoW/KAWPOW family at revision 0.9.4, reproduced from the
// vendored Ravencoin v4.8.0 library (pow/third_party/ravencoin-ethash-22549129).
//
// What this file adds over the upstream library (the corrections of DESIGN.md
// section 2 that live in the reference; the chain-context binding is the
// caller's: the height comes from the validated chain, never from the block):
//   - the context is bound to one epoch and a hash or verify at a height of
//     another epoch is refused (IGNPOW_E_EPOCH) instead of silently using a
//     cache for the wrong epoch;
//   - verify returns the reason (mix, then boundary), both checks always run;
//   - heights outside the library's int range are refused (IGNPOW_E_HEIGHT);
//   - no global or thread-local context: the caller owns every context.
// Parameters: v0 accepts only the upstream defaults (params NULL or "{}").

#include "candidate.hpp"

#include <crypto/ethash/include/ethash/ethash.hpp>
#include <crypto/ethash/include/ethash/progpow.hpp>
#include <crypto/ethash/lib/ethash/ethash-internal.hpp>
#include <crypto/ethash/progpow_test_vectors.hpp>

#include <cstring>
#include <limits>
#include <memory>
#include <string>
#include <thread>
#include <vector>

namespace ignpow {
namespace {

constexpr uint64_t kMaxHeight = static_cast<uint64_t>(std::numeric_limits<int>::max());

bool params_default(const char* p) {
    if (p == nullptr) return true;
    std::string s(p);
    size_t a = s.find_first_not_of(" \t\r\n");
    size_t b = s.find_last_not_of(" \t\r\n");
    if (a == std::string::npos) return true;
    return s.substr(a, b - a + 1) == "{}";
}

int kp_check_params(const char* p) { return params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }

int height_ok(const char* p, uint64_t height) {
    if (!params_default(p)) return IGNPOW_E_PARAMS;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    return IGNPOW_OK;
}

uint64_t kp_epoch_of(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    if (st != IGNPOW_OK) return 0;
    return static_cast<uint64_t>(ethash::get_epoch_number(static_cast<int>(height)));
}

uint64_t kp_cache_bytes(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    if (st != IGNPOW_OK) return 0;
    int e = ethash::get_epoch_number(static_cast<int>(height));
    return ethash::get_light_cache_size(ethash::calculate_light_cache_num_items(e));
}

uint64_t kp_dataset_bytes(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    if (st != IGNPOW_OK) return 0;
    int e = ethash::get_epoch_number(static_cast<int>(height));
    return ethash::get_full_dataset_size(ethash::calculate_full_dataset_num_items(e));
}

struct kp_ctx final : ctx_base {
    int epoch_number = 0;
    bool full = false;
    ethash::epoch_context_ptr light{nullptr, ethash_destroy_epoch_context};
    ethash::epoch_context_full_ptr fullctx{nullptr, ethash_destroy_epoch_context_full};
    uint64_t epoch() const override { return static_cast<uint64_t>(epoch_number); }
    const char* algo() const override { return IGNPOW_ALGO_KAWPOW_094; }
};

ctx_base* kp_create(const char* p, uint64_t height, int full, int* status) {
    int st = height_ok(p, height);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    auto c = std::make_unique<kp_ctx>();
    c->epoch_number = ethash::get_epoch_number(static_cast<int>(height));
    c->full = full != 0;
    if (c->full) {
        c->fullctx = ethash::create_epoch_context_full(c->epoch_number);
        if (!c->fullctx) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
        // F-12: the vendored library fills the dataset lazily (an item is computed on first touch), so a
        // full context hashed like a light one. Build every item once here, on every core, so the full
        // path reads resident memory as a miner does.
        {
            auto* dag = reinterpret_cast<ethash::hash2048*>(c->fullctx->full_dataset);
            const uint32_t n = static_cast<uint32_t>(c->fullctx->full_dataset_num_items / 2);
            unsigned nt = std::thread::hardware_concurrency();
            if (nt == 0) nt = 4;
            std::vector<std::thread> ts;
            for (unsigned t = 0; t < nt; ++t)
                ts.emplace_back([&, t]() {
                    for (uint32_t i = t; i < n; i += nt) dag[i] = ethash::calculate_dataset_item_2048(*c->fullctx, i);
                });
            for (auto& th : ts) th.join();
        }
    } else {
        c->light = ethash::create_epoch_context(c->epoch_number);
        if (!c->light) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    }
    if (status) *status = IGNPOW_OK;
    return c.release();
}

int compute(const kp_ctx& c, uint64_t height, const uint8_t header[32], uint64_t nonce,
            progpow::result& out) {
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    const int h = static_cast<int>(height);
    if (ethash::get_epoch_number(h) != c.epoch_number) return IGNPOW_E_EPOCH;
    const ethash::hash256 hh = ethash::hash256_from_bytes(header);
    out = c.full ? progpow::hash(*c.fullctx, h, hh, nonce) : progpow::hash(*c.light, h, hh, nonce);
    return IGNPOW_OK;
}

int kp_hash(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
            uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& c = *static_cast<const kp_ctx*>(ctx);
    progpow::result r;
    int st = compute(c, height, header, nonce, r);
    if (st != IGNPOW_OK) return st;
    std::memcpy(mix_out, r.mix_hash.bytes, 32);
    std::memcpy(hash_out, r.final_hash.bytes, 32);
    return IGNPOW_OK;
}

int kp_verify(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
              const uint8_t mix[32], const uint8_t boundary[32]) {
    const auto& c = *static_cast<const kp_ctx*>(ctx);
    progpow::result r;
    int st = compute(c, height, header, nonce, r);
    if (st != IGNPOW_OK) return st;
    const bool mix_ok = std::memcmp(r.mix_hash.bytes, mix, 32) == 0;
    const bool bound_ok = le256(r.final_hash.bytes, boundary);
    if (!mix_ok) return IGNPOW_E_MIX;
    if (!bound_ok) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}

// The light cache as the worker consumes it: hash512 items (64 bytes, 16
// little-endian words). The dataset the worker must build from it: 512-bit
// items k = 0, 1, 2, ... where item k = keccak512 of (cache[k mod n] with word 0
// xor k, then 512 parent rounds j: mix = fnv1(mix, cache[fnv1(k xor j, mix[j mod
// 16]) mod n])), laid out consecutively; the ProgPoW 2048-bit item i is 512-bit
// items 4i to 4i+3 (the vendored ethash.cpp, calculate_dataset_item_2048).
int kp_light_cache(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    const auto& c = *static_cast<const kp_ctx*>(ctx);
    const void* cache = c.full ? static_cast<const void*>(c.fullctx->light_cache) : static_cast<const void*>(c.light->light_cache);
    const int items = c.full ? c.fullctx->light_cache_num_items : c.light->light_cache_num_items;
    if (data) *data = cache;
    if (bytes) *bytes = static_cast<uint64_t>(items) * 64;
    if (item_bytes) *item_bytes = 64;
    return IGNPOW_OK;
}

int hexval(char ch) {
    if (ch >= '0' && ch <= '9') return ch - '0';
    if (ch >= 'a' && ch <= 'f') return ch - 'a' + 10;
    if (ch >= 'A' && ch <= 'F') return ch - 'A' + 10;
    return -1;
}

bool hex32(const char* s, uint8_t out[32]) {
    if (std::strlen(s) != 64) return false;
    for (int i = 0; i < 32; ++i) {
        int a = hexval(s[2 * i]), b = hexval(s[2 * i + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = static_cast<uint8_t>(a * 16 + b);
    }
    return true;
}

// The upstream KAWPOW vectors (progpow_test_vectors.hpp of the vendored tree),
// run with light contexts. Every binary runs this at start.
int kp_vectors_check(size_t* count_out) {
    size_t n = 0;
    int last_epoch = -1;
    ethash::epoch_context_ptr ctx{nullptr, ethash_destroy_epoch_context};
    for (const auto& t : progpow_hash_test_cases) {
        uint8_t header[32], mix[32], fin[32];
        if (!hex32(t.header_hash_hex, header) || !hex32(t.mix_hash_hex, mix) ||
            !hex32(t.final_hash_hex, fin))
            return IGNPOW_E_VECTORS;
        const uint64_t nonce = std::strtoull(t.nonce_hex, nullptr, 16);
        const int e = ethash::get_epoch_number(t.block_number);
        if (e != last_epoch) {
            ctx = ethash::create_epoch_context(e);
            if (!ctx) return IGNPOW_E_VECTORS;
            last_epoch = e;
        }
        const progpow::result r = progpow::hash(*ctx, t.block_number,
                                                ethash::hash256_from_bytes(header), nonce);
        if (std::memcmp(r.mix_hash.bytes, mix, 32) != 0 ||
            std::memcmp(r.final_hash.bytes, fin, 32) != 0)
            return IGNPOW_E_VECTORS;
        ++n;
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace

const candidate kawpow_094 = {
    IGNPOW_ALGO_KAWPOW_094,
    kp_check_params,
    kp_epoch_of,
    kp_cache_bytes,
    kp_dataset_bytes,
    kp_create,
    kp_hash,
    kp_verify,
    kp_light_cache,
    kp_vectors_check,
    "ravencoin-ethash@22549129 (v4.8.0, progpow 0.9.4, epoch 7500)",
};

}  // namespace ignpow
