/* rxe1.h: the RXE1 experimental family, scalar reference (IH 04).
 *
 * The algorithm is the founder's "IgneumHash engineering specification v0.1"
 * (docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md) sections 6 to 8, implemented as
 * written; where the text leaves a harness choice, docs/igneumhash/rxe1/
 * REFERENCE-NOTES.md records the reading. Research only: an engineering
 * hypothesis with its own algorithm id ("rxe1"), vectors and devnet
 * namespace (section 5). The ABI side is behind ignpow.h; this header is the
 * harness side of section 6: the explicit inputs (chain_id bytes, header_blob,
 * epoch_seed, epoch_number, nonce, the parameter record) and the trace.
 */
#ifndef RXE1_H
#define RXE1_H

#include "ignpow.h"

#include <stdint.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

#define IGNPOW_ALGO_RXE1 "rxe1"

/* The parameter record (section 6), in its order; the JSON document keys of
 * the ABI are dataset_bytes, parents, cache_bytes, rounds, registers, blocks,
 * mode, schema (all mandatory except where the notes name a default), plus
 * chain_id, epoch_blocks and test_only, which are not record fields. */
typedef struct rxe1_params {
    uint8_t dataset_spec_digest[32]; /* a constant of the binding (REFERENCE-NOTES.md) */
    uint64_t dataset_bytes;          /* 2 GiB or 4 GiB; smaller only with test_only */
    uint32_t parents;                /* 256 or 512 */
    uint64_t cache_bytes;            /* 32 MiB, the pinned dependency's */
    uint32_t rounds;                 /* R: 32 or 64 */
    uint32_t registers;              /* K: 8 or 16 */
    uint32_t blocks;                 /* B: 0 (M), or 1, 2, 4 (F, E) */
    uint32_t mode;                   /* 0 M, 1 F, 2 E; 3 P (a program per nonce, schema 3); 4 S (state-selected compiled blocks, schema 4) */
    uint32_t schema;                 /* 1 (the text verbatim) or 2 (the no-op fix, the project's decision); a test-only record carries 0x54455354 + (schema - 1) */
    uint64_t chain_id;               /* the ABI's chain id; chain_id_bytes = LE64 of it */
    uint64_t epoch_blocks;           /* the ABI's epoch length */
    uint32_t test_only;              /* 1 permits dataset_bytes below 2 GiB (never a cell) */
    /* schema 3 (SPEC-TEXT-v0.2-A2A3.md): four trailing record fields; under schemas 1 and 2 they hold the v0.1 point and are not in the record */
    uint32_t group;                  /* G: 8, 16, 32 logical lanes per nonce */
    uint32_t route;                  /* 0 local, 1 fixed exchange, 2 epoch exchange, 3 state-dependent exchange */
    uint32_t chunks;                 /* C: 1, 2, 4 memory requests per round */
    uint32_t payload_bytes;          /* 128 or 256 nominal bytes per round */
    /* schema 4 (mode S, SPEC-TEXT-v0.2-A2A3.md section 9): two more record fields */
    uint32_t block_len;              /* L: 8, 16, 32 operations per block */
    uint32_t family;                 /* NB: 16, 32, 64 precompiled blocks */
} rxe1_params;

/* The record: 72 bytes under schemas 1 and 2, 88 under schema 3, 96 under schema 4 (RXE1_RECORD_BYTES is the buffer size). */
#define RXE1_RECORD_BYTES 96
size_t rxe1_record_len(const rxe1_params *p);

int rxe1_params_parse(const char *params_json, rxe1_params *out);
void rxe1_record_bytes(const rxe1_params *p, uint8_t out[RXE1_RECORD_BYTES]);
void rxe1_pid(const rxe1_params *p, uint8_t out[32]);
/* The variant tag "<m|f|e>-b<B>-r<R>-k<K>-d<mib>-p<parents>-c<cache_mib>" (s2- prefixed under schema 2;
 * "s3-<m|f|e>-b<B>-r<R>-k<K>-g<G>-u<route>-x<chunks>-y<payload>-d<mib>-p<parents>-c<cache_mib>" under schema 3). */
void rxe1_variant_tag(const rxe1_params *p, char *buf, size_t n);
/* The binding constant: dataset_spec_digest. */
void rxe1_dataset_spec_digest(uint8_t out[32]);

/* A harness context: the dataset of (parameter record, epoch_seed) through the
 * pinned R0A v1 builder; light holds the cache and derives items on demand,
 * full builds every item once. */
typedef struct rxe1_ctx rxe1_ctx;
rxe1_ctx *rxe1_ctx_create(const rxe1_params *p, const uint8_t epoch_seed[32], int full, int *status);
void rxe1_ctx_free(rxe1_ctx *ctx);
/* The cache bytes (cache_bytes long) and, in a full context, the dataset. */
const uint8_t *rxe1_ctx_cache(const rxe1_ctx *ctx, uint64_t *bytes_out);
const uint8_t *rxe1_ctx_dataset(const rxe1_ctx *ctx, uint64_t *bytes_out);
/* BLAKE3 over the cache and (full contexts) the dataset, in item order; diagnostic. */
void rxe1_ctx_digests(const rxe1_ctx *ctx, uint8_t cache_digest[32], uint8_t dataset_digest[32], int *has_dataset);

/* One evaluation with the explicit harness inputs of section 6. chain_id is
 * 1 to 64 bytes, header_blob 0 to 4096 bytes (IGNPOW_E_ARG otherwise). out is
 * the 32-byte result of H("RXE1/result"); seed_out (may be NULL) the seed;
 * trace (may be NULL) receives one JSON object with every intermediate of
 * section 12: pid, seed, program_seed, the initial state, and per round a,
 * index, the item words, the state after injection, after each block and
 * after the exchange. */
int rxe1_eval(const rxe1_ctx *ctx, const uint8_t *chain_id, size_t chain_id_len,
              const uint8_t *header_blob, size_t header_len, uint64_t epoch_number, uint64_t nonce,
              uint8_t out[32], uint8_t seed_out[32], FILE *trace);

/* The compact trace digest of the differential campaign (REFERENCE-NOTES.md
 * section 5): H("RXE1/trace-digest", [seed || initial state || per round
 * (LE32 a || LE64 index || state after injection || state after each block ||
 * state after the exchange) || out]), states as lane-major LE32 words. */
int rxe1_trace_digest(const rxe1_ctx *ctx, const uint8_t *chain_id, size_t chain_id_len,
                      const uint8_t *header_blob, size_t header_len, uint64_t epoch_number, uint64_t nonce,
                      uint8_t out[32], uint8_t digest_out[32]);

/* The schedule of (chain_id, epoch_number, epoch_seed) under the context's
 * record as one JSON object: program_seed and, per round, block and
 * destination, src1, src2, rotate, opcode. Mode M writes an empty schedule. */
int rxe1_schedule(const rxe1_ctx *ctx, const uint8_t *chain_id, size_t chain_id_len, uint64_t epoch_number, FILE *out);

/* The schedule of one nonce (mode P, section 5 of the v0.2 text): the nonce's seed, its program seed and the
 * descriptors; for the other modes the same as rxe1_schedule. */
int rxe1_schedule_for_nonce(const rxe1_ctx *ctx, const uint8_t *chain_id, size_t chain_id_len,
                            const uint8_t *header_blob, size_t header_len, uint64_t epoch_number, uint64_t nonce, FILE *out);

/* Mode S dumps (section 9.6): the family (family_seed and every block's descriptors) and a nonce's block
 * sequence (seed, then per execution e, h, sel and the round). */
int rxe1_family(const rxe1_ctx *ctx, FILE *out);
int rxe1_block_sequence(const rxe1_ctx *ctx, const uint8_t *chain_id, size_t chain_id_len,
                        const uint8_t *header_blob, size_t header_len, uint64_t epoch_number, uint64_t nonce, FILE *out);

/* The ABI harness epoch seed (REFERENCE-NOTES.md): H("RXE1/harness-epoch",
 * [LE64(chain_id), LE64(epoch_number)]). The node adapter replaces it by the
 * chain's rule. */
void rxe1_harness_epoch_seed(uint64_t chain_id, uint64_t epoch_number, uint8_t out[32]);

#ifdef __cplusplus
}
#endif

#endif /* RXE1_H */
