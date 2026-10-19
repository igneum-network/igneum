// Internal candidate interface of the shared reference. Each candidate
// implements this table; ignpow.cpp dispatches the C ABI to it.
#pragma once

#include "ignpow.h"

#include <cstdint>
#include <cstddef>

namespace ignpow {

struct ctx_base {
    virtual ~ctx_base() = default;
    virtual uint64_t epoch() const = 0;
    virtual const char* algo() const = 0;
};

// F-219 (16 October 2026): the bound of the raster form's buffer pool per context (ignpow_set_parallel_width: the node's parallel
// verification width; the hardware's concurrency when unset, never 0) and the oracle switch of the differential tests
// (ignpow_set_raster_oracle: the normative target and the sparse overlay in place of the tiles, the same bytes, per attempt).
uint32_t parallel_width();
bool raster_oracle();

struct candidate {
    const char* id;
    // params_json may be NULL. Returns IGNPOW_OK or IGNPOW_E_PARAMS.
    int (*check_params)(const char* params_json);
    // Pure functions of height. Return 0 and set *status on rejection.
    uint64_t (*epoch_of)(const char* params_json, uint64_t height, int* status);
    uint64_t (*cache_bytes)(const char* params_json, uint64_t height, int* status);
    uint64_t (*dataset_bytes)(const char* params_json, uint64_t height, int* status);
    ctx_base* (*create)(const char* params_json, uint64_t height, int full, int* status);
    int (*hash)(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                uint8_t mix_out[32], uint8_t hash_out[32]);
    int (*verify)(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                  const uint8_t mix[32], const uint8_t boundary[32]);
    // The light cache (NULL and 0 bytes when the candidate has none).
    int (*light_cache)(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes);
    // Runs the candidate's embedded vectors; returns IGNPOW_OK or IGNPOW_E_VECTORS.
    int (*vectors_check)(size_t* count_out);
    const char* pin;  // upstream pin string for the build string
};

// IgneumHash's shared context (ignpow_ctx_create_shared): the document's own context over a donor's cache and dataset memory.
ctx_base* ih_create_shared(const char* params_json, uint64_t height, const ctx_base* donor, int* status);
// Proof of Play's context over an IgneumHash donor's dataset (play.cpp)
ctx_base* play_create_shared(const char* params_json, uint64_t height, const ctx_base* donor, int* status);

// Compare two 32-byte values as big-endian 256-bit integers: a <= b.
inline bool le256(const uint8_t a[32], const uint8_t b[32]) {
    for (int i = 0; i < 32; ++i) {
        if (a[i] < b[i]) return true;
        if (a[i] > b[i]) return false;
    }
    return true;
}

extern const candidate kawpow_094;
extern const candidate autolykos_2;
extern const candidate fishhash_1;  // present only when built with IGNPOW_FISHHASH_SRC (D-GA-04)
extern const candidate igneumhash_r0a;
extern const candidate rxe1_family;
extern const candidate compactproof;
extern const candidate forgeb1;
extern const candidate progpow_epic;
extern const candidate cuckaroo29_tari;
extern const candidate igneum_graph28k;  // the 28b form (cuckaroo28k.cpp)
extern const candidate igneum_graph28;  // the Graph coordinate's 4 GB form (cuckaroo28.cpp)
extern const candidate igneum_graph;  // VectorWork's Graph coordinate (cuckaroo29.cpp)
extern const candidate igneum_play;   // Proof of Play, the microgame reference (play.cpp)
extern const candidate qpow_quantus;

// The candidate context behind a wrapped ABI context when its id is algo (NULL otherwise).
const ctx_base* inner_of(const ignpow_ctx* ctx, const char* algo);

}  // namespace ignpow
