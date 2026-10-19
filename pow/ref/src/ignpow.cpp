// The C ABI of the shared reference: dispatch by candidate id.
#include "candidate.hpp"

#include <atomic>
#include <cstring>
#include <string>
#include <thread>

#ifdef IGNPOW_BUILD_COMMIT_HEADER
#include "ignpow_build_commit.h"   // written at build time by cmake/build_commit.cmake
#endif
#ifndef IGNPOW_BUILD_COMMIT
#define IGNPOW_BUILD_COMMIT "unknown"
#endif

namespace {

const ignpow::candidate* const kCandidates[] = {
    &ignpow::kawpow_094,
    &ignpow::autolykos_2,
#ifdef IGNPOW_HAVE_FISHHASH
    &ignpow::fishhash_1,
#endif
    &ignpow::igneumhash_r0a,
    &ignpow::rxe1_family,
    &ignpow::compactproof,
    &ignpow::forgeb1,
    &ignpow::progpow_epic,
    &ignpow::cuckaroo29_tari,
    &ignpow::igneum_graph,
    &ignpow::igneum_graph28,
    &ignpow::igneum_graph28k,
    &ignpow::igneum_play,
    &ignpow::qpow_quantus,
};
constexpr size_t kCount = sizeof(kCandidates) / sizeof(kCandidates[0]);

const ignpow::candidate* find(const char* algo) {
    if (algo == nullptr) return nullptr;
    for (size_t i = 0; i < kCount; ++i)
        if (std::strcmp(kCandidates[i]->id, algo) == 0) return kCandidates[i];
    return nullptr;
}

struct wrapped {
    const ignpow::candidate* cand;
    ignpow::ctx_base* inner;
};

}  // namespace

struct ignpow_ctx : wrapped {};

namespace ignpow {
const ctx_base* inner_of(const ignpow_ctx* ctx, const char* algo) {
    if (!ctx || !algo || std::strcmp(ctx->cand->id, algo) != 0) return nullptr;
    return ctx->inner;
}
// F-219: the raster pool's bound and the oracle switch (candidate.hpp)
namespace { std::atomic<uint32_t> g_parallel_width{0}; std::atomic<int> g_raster_oracle{0}; }
uint32_t parallel_width() {
    uint32_t w = g_parallel_width.load(std::memory_order_relaxed);
    if (!w) w = std::thread::hardware_concurrency();
    return w ? w : 1u;
}
bool raster_oracle() { return g_raster_oracle.load(std::memory_order_relaxed) != 0; }
}  // namespace ignpow

extern "C" {

void ignpow_set_parallel_width(uint32_t width) { ignpow::g_parallel_width.store(width, std::memory_order_relaxed); }
uint32_t ignpow_parallel_width(void) { return ignpow::parallel_width(); }
void ignpow_set_raster_oracle(int on) { ignpow::g_raster_oracle.store(on ? 1 : 0, std::memory_order_relaxed); }

size_t ignpow_algo_count(void) { return kCount; }

const char* ignpow_algo_id(size_t i) { return i < kCount ? kCandidates[i]->id : nullptr; }

uint64_t ignpow_epoch_of(const char* algo, const char* params_json, uint64_t height, int* status) {
    const auto* c = find(algo);
    if (!c) { if (status) *status = IGNPOW_E_ALGO; return 0; }
    return c->epoch_of(params_json, height, status);
}

uint64_t ignpow_cache_bytes(const char* algo, const char* params_json, uint64_t height, int* status) {
    const auto* c = find(algo);
    if (!c) { if (status) *status = IGNPOW_E_ALGO; return 0; }
    return c->cache_bytes(params_json, height, status);
}

uint64_t ignpow_dataset_bytes(const char* algo, const char* params_json, uint64_t height, int* status) {
    const auto* c = find(algo);
    if (!c) { if (status) *status = IGNPOW_E_ALGO; return 0; }
    return c->dataset_bytes(params_json, height, status);
}

ignpow_ctx* ignpow_ctx_create(const char* algo, const char* params_json, uint64_t height, int full,
                              int* status) {
    const auto* c = find(algo);
    if (!c) { if (status) *status = IGNPOW_E_ALGO; return nullptr; }
    int st = c->check_params(params_json);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    ignpow::ctx_base* inner = c->create(params_json, height, full, &st);
    if (status) *status = st;
    if (!inner) return nullptr;
    auto* w = new ignpow_ctx();
    w->cand = c;
    w->inner = inner;
    return w;
}

ignpow_ctx* ignpow_ctx_create_shared(const char* algo, const char* params_json, uint64_t height, const ignpow_ctx* donor, int* status) {
    const auto* c = find(algo);
    if (!c) { if (status) *status = IGNPOW_E_ALGO; return nullptr; }
    // the donor is an igneumhash context; the new context is igneumhash (a keyed document over its twin's dataset) or igneum-play
    // (the play world over the registered documents' one dataset)
    const bool play = std::strcmp(algo, "igneum-play") == 0;
    if (!donor || std::strcmp(donor->cand->id, "igneumhash") != 0 || (!play && std::strcmp(algo, "igneumhash") != 0)) { if (status) *status = IGNPOW_E_ARG; return nullptr; }
    int st = c->check_params(params_json);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    ignpow::ctx_base* inner = play ? ignpow::play_create_shared(params_json, height, donor->inner, &st) : ignpow::ih_create_shared(params_json, height, donor->inner, &st);
    if (status) *status = st;
    if (!inner) return nullptr;
    auto* w = new ignpow_ctx();
    w->cand = c;
    w->inner = inner;
    return w;
}

void ignpow_ctx_free(ignpow_ctx* ctx) {
    if (!ctx) return;
    delete ctx->inner;
    delete ctx;
}

uint64_t ignpow_ctx_epoch(const ignpow_ctx* ctx) { return ctx ? ctx->inner->epoch() : 0; }

const char* ignpow_ctx_algo(const ignpow_ctx* ctx) { return ctx ? ctx->cand->id : nullptr; }

int ignpow_hash(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                uint8_t mix_out[32], uint8_t hash_out[32]) {
    if (!ctx || !header || !mix_out || !hash_out) return IGNPOW_E_ARG;
    return ctx->cand->hash(ctx->inner, height, header, nonce, mix_out, hash_out);
}

int ignpow_verify(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                  const uint8_t mix[32], const uint8_t boundary[32]) {
    if (!ctx || !header || !mix || !boundary) return IGNPOW_E_ARG;
    return ctx->cand->verify(ctx->inner, height, header, nonce, mix, boundary);
}

int ignpow_ctx_light_cache(const ignpow_ctx* ctx, const void** data_out, uint64_t* bytes_out,
                           uint32_t* item_bytes_out) {
    if (!ctx) return IGNPOW_E_ARG;
    return ctx->cand->light_cache(ctx->inner, data_out, bytes_out, item_bytes_out);
}

int ignpow_vectors_check(const char* algo, size_t* count_out) {
    size_t total = 0;
    for (size_t i = 0; i < kCount; ++i) {
        if (algo && std::strcmp(kCandidates[i]->id, algo) != 0) continue;
        size_t n = 0;
        int st = kCandidates[i]->vectors_check(&n);
        if (st != IGNPOW_OK) { if (count_out) *count_out = total; return st; }
        total += n;
    }
    if (algo && !find(algo)) return IGNPOW_E_ALGO;
    if (count_out) *count_out = total;
    return IGNPOW_OK;
}

const char* ignpow_build_string(void) {
    static std::string s;
    if (s.empty()) {
        s = "ignpow abi " + std::to_string(IGNPOW_ABI_VERSION) + " build " IGNPOW_BUILD_COMMIT;
        for (size_t i = 0; i < kCount; ++i)
            s += std::string("; ") + kCandidates[i]->id + " = " + kCandidates[i]->pin;
    }
    return s.c_str();
}

}  // extern "C"
