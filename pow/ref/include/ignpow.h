/* ignpow.h: the shared proof-of-work reference, C ABI.
 *
 * One consensus truth for every backend (CPU, CUDA, OpenCL, Metal) and for the
 * node's verification hook. Candidates are selected by id; the caller does not
 * know which one will be kept. Draft 0, 9 October 2026 (GPU engineer A).
 *
 * Portable integer semantics: every value crossing this boundary is a
 * fixed-width unsigned integer or a byte array; byte arrays are the raw 32-byte
 * values with no endianness of their own; integers are host order. Nothing here
 * allocates on the caller's behalf except the context.
 */
#ifndef IGNPOW_H
#define IGNPOW_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define IGNPOW_ABI_VERSION 0

/* Candidate ids known to this build (NUL-terminated strings).
 *
 * kawpow-0.9.4 constants every backend must share (the vendored Ravencoin
 * v4.8.0 library): ethash epoch length 7500; light cache items of 64 bytes
 * built with 3 rounds; dataset items of 128 bytes, each 64-byte half built
 * from 512 cache parents (IGNPOW_KAWPOW_DATASET_PARENTS; ProgPoW 0.9.3 raised
 * Ethash's 256 to 512, and a backend using 256 fails every vector); ProgPoW
 * period 3 blocks, 16 lanes, 32 registers, 64 rounds of one 256-byte item
 * load, 11 cache reads and 18 math operations per round, a 16 KiB L1 window. */
#define IGNPOW_ALGO_KAWPOW_094 "kawpow-0.9.4"
#define IGNPOW_KAWPOW_DATASET_PARENTS 512
#define IGNPOW_KAWPOW_EPOCH_LENGTH 7500
#define IGNPOW_KAWPOW_PERIOD_LENGTH 3
#define IGNPOW_ALGO_FISHHASH_1 "fishhash-1"
#define IGNPOW_ALGO_AUTOLYKOS_2 "autolykos-2"
#define IGNPOW_ALGO_PROGPOW_EPIC "progpow-epic"
#define IGNPOW_ALGO_CUCKAROO29_TARI "cuckaroo29-tari"
#define IGNPOW_ALGO_IGNEUM_GRAPH28K "igneum-graph28k"  /* the 28b form: the edge function keyed over the epoch dataset (cuckaroo28k.cpp) */
#define IGNPOW_ALGO_IGNEUM_GRAPH28 "igneum-graph28"  /* the Graph coordinate's 4 GB form: cuckaroo at EDGEBITS 28 under the same key and ticket hash (cuckaroo28.cpp) */
#define IGNPOW_ALGO_IGNEUM_GRAPH "igneum-graph"  /* VectorWork's Graph coordinate: cuckaroo29 under Igneum's graph key and ticket hash; params {"chain_id": N} */
#define IGNPOW_ALGO_IGNEUM_PLAY "igneum-play"  /* Proof of Play (C-33), the microgame reference over IgneumHash's dataset; params {"chain_id", "ticks", "steps", ...} */
#define IGNPOW_ALGO_QPOW_QUANTUS "qpow-quantus"

/* Verify and hash return codes. 0 is success; everything else names the reason.
 * A caller that wants a bool compares with IGNPOW_OK and nothing else. */
enum ignpow_status {
    IGNPOW_OK = 0,
    IGNPOW_E_ALGO = 1,          /* unknown candidate id */
    IGNPOW_E_PARAMS = 2,        /* params document rejected */
    IGNPOW_E_HEIGHT = 3,        /* height outside the candidate's schedule */
    IGNPOW_E_EPOCH = 4,         /* context prepared for another epoch than height needs */
    IGNPOW_E_BOUNDARY = 5,      /* final hash above the boundary */
    IGNPOW_E_MIX = 6,           /* mix hash does not match */
    IGNPOW_E_ARG = 7,           /* null pointer or wrong length */
    IGNPOW_E_MEMORY = 8,        /* context allocation refused */
    IGNPOW_E_VECTORS = 9        /* embedded self-test failed */
};

typedef struct ignpow_ctx ignpow_ctx;

/* Number of candidate ids in this build and the id at index i (NULL past the end). */
size_t ignpow_algo_count(void);
const char *ignpow_algo_id(size_t i);

/* Epoch and sizes are pure functions of (algo, params, height). params_json may
 * be NULL for the candidate's upstream defaults. Sizes are bytes; 0 with a
 * non-OK status in *status when the inputs are rejected. */
uint64_t ignpow_epoch_of(const char *algo, const char *params_json, uint64_t height, int *status);
uint64_t ignpow_cache_bytes(const char *algo, const char *params_json, uint64_t height, int *status);
uint64_t ignpow_dataset_bytes(const char *algo, const char *params_json, uint64_t height, int *status);

/* A context holds the epoch state for the epoch that contains height: the light
 * cache always; the full dataset when full != 0. Creating a full context may take
 * minutes and gigabytes; the node uses light contexts only. */
ignpow_ctx *ignpow_ctx_create(const char *algo, const char *params_json, uint64_t height, int full, int *status);
/* The shared context: a context with its own document over DONOR's cache and dataset memory; the donor an igneumhash
 * context, the new context igneumhash (a keyed document over its unkeyed twin's dataset, the same dataset by
 * construction) or igneum-play (the play world over the registered documents' one dataset), so one resident
 * dataset serves every coordinate on the node.
 * The donor must be an igneumhash context of the same epoch whose dataset seed equals the document's
 * (IGNPOW_E_PARAMS otherwise; IGNPOW_E_ARG for another algorithm or a null donor). The donor must outlive every context
 * sharing it; freeing the shared context frees nothing of the donor's. */
ignpow_ctx *ignpow_ctx_create_shared(const char *algo, const char *params_json, uint64_t height, const ignpow_ctx *donor, int *status);
void ignpow_ctx_free(ignpow_ctx *ctx);
/* F-219 (16 October 2026): the raster form (IgneumHash version 18) hashes on buffers leased from a bounded pool owned by
 * the context and freed with it; the pool's bound is the node's parallel verification width, set here (0 = the hardware's
 * concurrency). Read at every lease, so it may be set at any time; a pool never gives back slots already made. */
void ignpow_set_parallel_width(uint32_t width);
uint32_t ignpow_parallel_width(void);
/* The raster form's oracle paths for the differential tests (the calloc'd target of the normative text for the unkeyed
 * form, the sparse overlay for the keyed): the same bytes, slower and allocated per attempt. Never a node's setting. */
void ignpow_set_raster_oracle(int on);
uint64_t ignpow_ctx_epoch(const ignpow_ctx *ctx);
const char *ignpow_ctx_algo(const ignpow_ctx *ctx);

/* Compute mix and final hash for (height, header, nonce). The height must lie in
 * the context's epoch (IGNPOW_E_EPOCH otherwise); the caller derives nothing
 * about the epoch or the program period itself. header is the 32-byte sealing
 * hash the node defines. */
int ignpow_hash(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                uint8_t mix_out[32], uint8_t hash_out[32]);

/* Verify a sealed block: recompute the mix, compare it, compare the final hash
 * with the boundary (final <= boundary passes). Returns IGNPOW_OK or the reason.
 * Both comparisons always run; the reason reports the first failure in the
 * order mix, boundary. */
int ignpow_verify(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                  const uint8_t mix[32], const uint8_t boundary[32]);

/* The light cache of the context's epoch, for a worker that generates the
 * dataset on the device: *data_out points at bytes owned by the context,
 * *bytes_out its length, *item_bytes_out the item size (64 for the ethash
 * family). Candidates without a cache (table-based designs) return 0 bytes.
 * The dataset item layout a worker must produce from it is documented beside
 * the candidate's reference. */
int ignpow_ctx_light_cache(const ignpow_ctx *ctx, const void **data_out, uint64_t *bytes_out,
                           uint32_t *item_bytes_out);

/* Run the vectors embedded in this build for one candidate (or all when algo is
 * NULL). Returns IGNPOW_OK or IGNPOW_E_VECTORS; count_out receives the number
 * of vectors checked. Every worker and the node run this at start. */
int ignpow_vectors_check(const char *algo, size_t *count_out);

/* Build identification for cell records and the node log: a NUL-terminated
 * string naming the ABI version, the candidates, their upstream pins and the
 * build's commit. */
const char *ignpow_build_string(void);

#ifdef __cplusplus
}
#endif

#endif /* IGNPOW_H */
