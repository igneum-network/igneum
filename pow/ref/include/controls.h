/* controls.h: the side APIs of the decisive round's controls beyond the ignpow ABI
 * (docs/igneumhash/controls/SOURCES.md). Research only. */
#ifndef IGNPOW_CONTROLS_H
#define IGNPOW_CONTROLS_H

#include "ignpow.h"

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Tari Cuckaroo29 (candidate "cuckaroo29-tari"): the siphash keys of (mining hash, nonce); the solver (every 42-cycle
 * of the graph, each 42 ascending edge indices; sols holds max_sols x 42; n_out the number found); the verifier
 * (tromp's verify codes: 0 POW_OK, 2 edge too big, 3 not ascending, 4 endpoints, 5 branch, 6 dead end, 7 short
 * cycle); Tari's 29-bit packing (153 bytes) and unpacking (IGNPOW_E_ARG on a non-zero extra); the proof value
 * BLAKE2b-256(packed), compared big-endian. */
void cuckaroo29_keys(const uint8_t header[32], uint64_t nonce, uint64_t keys[4]);
int cuckaroo29_solve(const uint8_t header[32], uint64_t nonce, uint32_t *sols, size_t max_sols, size_t *n_out);
int cuckaroo29_verify(const uint8_t header[32], uint64_t nonce, const uint32_t edges[42]);
void cuckaroo29_pack(const uint32_t edges[42], uint8_t out[153]);
int cuckaroo29_unpack(const uint8_t in[153], uint32_t edges[42]);
void cuckaroo29_proof_hash(const uint8_t packed[153], uint8_t out[32]);

/* Quantus QPoW (candidate "qpow-quantus"): the 64-byte nonce hash of block_hash(32) || nonce(64) (read big-endian by
 * the chain), the field hash of arbitrary bytes (hash_bytes, 32 bytes), and the ABI harness nonce (LE64(nonce) || 56
 * zero bytes). */
void qpow_quantus_hash(const uint8_t block_hash[32], const uint8_t nonce[64], uint8_t out[64]);
void qpow_quantus_hash_bytes(const uint8_t *in, size_t n, uint8_t out[32]);
void qpow_quantus_harness_nonce(uint64_t nonce, uint8_t out[64]);

/* Epic ProgPoW (candidate "progpow-epic"): the tag and pid (BLAKE3 of the tag). */
void progpow_epic_tag(char *buf, size_t n);
void progpow_epic_pid(uint8_t out[32]);

#ifdef __cplusplus
}
#endif

#endif /* IGNPOW_CONTROLS_H */
