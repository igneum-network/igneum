/* compactproof.h: the compact-proof memory construction as a research
 * reference (the founder's decision-6 priority 3): Blocki and Smearsoll
 * (2025), "Provably Memory-Hard Proofs of Work With Memory-Easy
 * Verification" (ePrint 2025/1456, TCC 2025), the MTP framework (Biryukov
 * and Khovratovich 2016) over the data-independent memory-hard function on
 * the DRSample graph (Alwen, Blocki and Harsha, CCS 2017).
 *
 * Status: research only; experimental points, never claimed secure
 * settings. docs/igneumhash/research/COMPACTPROOF-NOTES.md records which
 * parts come from which source and marks UNREAD the parts of the 2025 text
 * that could not be fetched (ePrint's challenge wall); they are replaced
 * when the exact text is in hand.
 *
 * Shape: the prover fills T labels (the working memory) from the work
 * challenge I, commits to them by a Merkle root R, then searches nonces
 * cheaply: Y_0 = H(I, R, nonce), L dependent lookups Y_j = H(Y_{j-1},
 * X[i_j]) with i_j from Y_{j-1}, and the final Y_L against the target. The
 * proof opens, per lookup, the challenged label's parents with their Merkle
 * paths; a verifier recomputes the L labels from their parents and checks
 * the paths: polylog(T) work and memory, no fill.
 *
 * Behind the ignpow ABI (candidate "compactproof") the heavy path only:
 * ignpow_hash is the prover (the fill of I is cached per context), mix_out
 * = R, hash_out = Y_L; ignpow_verify recomputes everything. The light
 * verifier, the proof and the measurements are the functions below.
 */
#ifndef COMPACTPROOF_H
#define COMPACTPROOF_H

#include "ignpow.h"

#include <stdint.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

#define IGNPOW_ALGO_COMPACTPROOF "compactproof"

typedef struct cp_params {
    uint32_t schema;        /* 1 */
    uint64_t memory_bytes;  /* the working memory: 2^26 (Forge F1's first point), 2^28, 2^30, 2^31 (test_only admits from 2^20) */
    uint32_t label_bytes;   /* 32, 64 or 128 bytes per label */
    uint32_t challenges;    /* L: the dependent lookups per nonce, 1 to 1024 */
    uint64_t chain_id;      /* bound into the challenge I with the header */
    uint64_t epoch_blocks;  /* the ABI's epoch length (the construction has no epoch; the context is per epoch for the ABI) */
    uint32_t test_only;     /* 1 permits memory_bytes below 2^28; never a cell */
    uint32_t graph;         /* 0 DRSample (the registered row; the 20-byte record), 1 DRS+BRG (F-23's variant; GRAPH appended, 24 bytes) */
} cp_params;

int cp_params_parse(const char *params_json, cp_params *out);
/* pid = BLAKE3 over the canonical record: schema u32 || memory_bytes u64 || label_bytes u32 || challenges u32 (little-endian, 20 bytes; a test-only record carries schema 0x54455354); with graph 1, GRAPH u32 appended (24 bytes). */
void cp_pid(const cp_params *p, uint8_t out[32]);
void cp_variant_tag(const cp_params *p, char *buf, size_t n);
/* T = memory_bytes / label_bytes; the Merkle depth log2(T). */
uint64_t cp_labels(const cp_params *p);
/* The proof size in bytes for a parameter set. */
uint64_t cp_proof_bytes(const cp_params *p);

/* The prover's state for one attempt: the labels and the Merkle tree of one seed. Under F0 (FORGE.md,
 * F0-RELATION.md) the seed is H_seed(x) with the nonce inside x, so one attempt is one fill; the ABI's harness seed
 * (cp_abi_seed) is BLAKE3(tag || LE64(chain_id) || header32 || LE64(nonce)) until the node supplies F0's. */
typedef struct cp_prover cp_prover;
cp_prover *cp_prover_create(const cp_params *p, const uint8_t seed[32], uint64_t *fill_ms, uint64_t *commit_ms, int *status);
void cp_prover_free(cp_prover *pr);
void cp_prover_root(const cp_prover *pr, uint8_t root[32]);
/* Label i of the fill (label_bytes), for intermediate vectors; NULL past T. */
const uint8_t *cp_prover_label(const cp_prover *pr, uint64_t i);
/* y = Y_L; proof_out (cp_proof_bytes long: the root, then per challenge the index, two parent labels and three
 * Merkle paths) receives the proof. */
int cp_prove(const cp_prover *pr, uint8_t y_out[32], uint8_t *proof_out);
/* The approved program id of a parameter set (F0 section 5): BLAKE3(tag("CP/program") || "compactproof/v1" || pid). */
void cp_program_id(const cp_params *p, uint8_t out[32]);
void cp_abi_seed(const cp_params *p, const uint8_t header[32], uint64_t nonce, uint8_t out[32]);

/* The light verifier of the statement "WorkF(seed; params) = y under program id": IGNPOW_OK with y_out = y when
 * the proof's root, openings and recomputed labels agree and reach y; IGNPOW_E_PARAMS on a program id that is not
 * this parameter set's; IGNPOW_E_ARG on a malformed length; IGNPOW_E_MIX on any other failure (an altered seed,
 * an altered y, a bad opening or label, a non-zero unused field, a wrong index). Allocates nothing proportional
 * to the memory. */
int cp_verify(const cp_params *p, const uint8_t seed[32], const uint8_t program_id[32], const uint8_t y[32],
              const uint8_t *proof, size_t proof_len, uint8_t y_out[32]);

/* The DRSample parent of 1-based vertex v (the second parent; the first is v - 1), per Alwen, Blocki and Harsha
 * Algorithm 3 with the randomness from BLAKE3 over the pid and v. v >= 3; returns a 1-based vertex. */
uint64_t cp_parent(const cp_params *p, uint64_t v);

#ifdef __cplusplus
}
#endif

#endif /* COMPACTPROOF_H */
