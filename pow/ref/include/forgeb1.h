/* forgeb1.h: Forge F1 under B1, the faithful small reference of WorkF in
 * Forge-B1's shape (docs/igneumhash/forge/F1-B1.md, text revision 2). Research
 * only; the text is the authority and protocol B writes from it alone.
 *
 * Inputs (32 bytes each, from the protocol team's consensus objects): epoch_seed (the
 * topology), fill_seed (the labels), transcript (the challenge derivation of a
 * passing ticket). Output: root (32 bytes) and, for a passing ticket, the
 * proof. Behind the ignpow ABI (candidate "forgeb1") the heavy path only, with
 * the harness rule for the seeds in REFERENCE notes section 2.3b's spirit:
 * epoch_seed = BLAKE3(tag("B1/harness-epoch") || LE64(chain_id) || LE64(epoch)),
 * fill_seed = BLAKE3(tag("B1/harness-fill") || epoch_seed || header32 || LE64(nonce));
 * mix_out = root, hash_out = BLAKE3(tag("B1/harness-ticket") || root || LE64(nonce))
 * (a harness stand-in for the protocol team's tickets, never consensus). */
#ifndef FORGEB1_H
#define FORGEB1_H

#include "ignpow.h"

#include <stdint.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

#define IGNPOW_ALGO_FORGEB1 "forgeb1"

typedef struct b1_params {
    uint32_t schema;       /* 1 (0x54455354 in a test-only record) */
    uint32_t graph;        /* 0 DRSample, 1 DRS+BRG */
    uint64_t memory_bytes; /* M: 2^26, 2^28, 2^30, 2^31 (test_only from 2^20) */
    uint32_t label_bytes;  /* L: 128, 256, 512, 1024 */
    uint32_t challenges;   /* q: 1 to 1024 */
    uint32_t batch;        /* B: the protocol team's parameter, in the record only */
    uint64_t chain_id;     /* the ABI's chain id (harness) */
    uint64_t epoch_blocks; /* the ABI's epoch length (harness) */
    uint32_t test_only;
} b1_params;

#define B1_RECORD_BYTES 28

int b1_params_parse(const char *params_json, b1_params *out);
void b1_record(const b1_params *p, uint8_t out[B1_RECORD_BYTES]);
void b1_pid(const b1_params *p, uint8_t out[32]);
void b1_program_id(const b1_params *p, uint8_t out[32]);
void b1_variant_tag(const b1_params *p, char *buf, size_t n);
uint64_t b1_nodes(const b1_params *p);        /* N = M / L */
uint64_t b1_proof_bytes(const b1_params *p);

/* The topology: the second parent r(v) of node v >= 2 under the graph (text section 2); 0 for v < 2. */
uint64_t b1_parent(const b1_params *p, const uint8_t epoch_seed[32], uint64_t v);

/* The prover's state for one (epoch_seed, fill_seed): the labels and the tree. */
typedef struct b1_prover b1_prover;
b1_prover *b1_prover_create(const b1_params *p, const uint8_t epoch_seed[32], const uint8_t fill_seed[32],
                            uint64_t *fill_ms, uint64_t *commit_ms, int *status);
void b1_prover_free(b1_prover *pr);
void b1_prover_root(const b1_prover *pr, uint8_t root[32]);
const uint8_t *b1_prover_label(const b1_prover *pr, uint64_t v);
/* The challenge indices of a transcript (q of them) and the proof for it. */
void b1_challenges(const b1_params *p, const uint8_t transcript[32], uint64_t *indices_out);
int b1_prove(const b1_prover *pr, const uint8_t transcript[32], uint8_t *proof_out);

/* The verifier of text section 6. IGNPOW_OK; IGNPOW_E_PARAMS on another program id; IGNPOW_E_ARG on a wrong
 * length; IGNPOW_E_MIX on any other failure. Allocates nothing proportional to N. */
int b1_verify(const b1_params *p, const uint8_t epoch_seed[32], const uint8_t fill_seed[32],
              const uint8_t transcript[32], const uint8_t program_id[32], const uint8_t *proof, size_t proof_len);

/* The ABI harness seeds (never consensus). */
void b1_harness_seeds(const b1_params *p, const uint8_t header[32], uint64_t epoch, uint64_t nonce,
                      uint8_t epoch_seed[32], uint8_t fill_seed[32]);

#ifdef __cplusplus
}
#endif

#endif /* FORGEB1_H */
