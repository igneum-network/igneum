/* The node's pi verifier (SPEC-R0A-v0.md section 0.9.1; PI-NOTES.md section 5): the C ABI of pow/pi/ffi
 * (libigneum_pi_ffi.a, built with `cargo build --release -p igneum-pi-ffi` under pow/pi; the Go binding is
 * node/consensus/igneum/pow/pi, build tag ignpi). Written by hand; the Rust side is the source of truth. */
#ifndef IGNPI_H
#define IGNPI_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* bumped on any change of a signature or a code */
#define IGNPI_ABI_VERSION 1u

/* the refusal codes of ignpi_verify, in the order the checks run */
#define IGNPI_OK 0
#define IGNPI_E_ARG 1     /* domain not 0 or 1, k 0 or over 64, or a null pointer: a malformed call */
#define IGNPI_E_BOUND 2   /* pi longer than the domain's bound (294912 block, 2048 claim), before parsing */
#define IGNPI_E_SYSTEM 3  /* pi empty or its identifier byte not 2 */
#define IGNPI_E_DECODE 4  /* the receipt does not decode */
#define IGNPI_E_KIND 5    /* the receipt's kind is not the domain's: composite of one segment (0), Groth16 (1) */
#define IGNPI_E_PROOF 6   /* the STARK or SNARK does not verify against PI_IMAGE_ID */
#define IGNPI_E_JOURNAL 7 /* the journal differs: pk, an m_i or a share is not the proof's */
#define IGNPI_E_PANIC 8   /* a panic inside the verifier, caught at the boundary */

uint32_t ignpi_abi_version(void);
uint8_t ignpi_system(void); /* 2 */
uint32_t ignpi_k_max(void); /* 64 */
size_t ignpi_max_bytes(uint8_t domain); /* 0 for a domain not 0 or 1 */
void ignpi_image_id(uint8_t out[32]); /* PI_IMAGE_ID, the pinned consensus constant */

/* Verify pi (pi_len bytes, the wire form u8(2) || receipt) under domain (0 the seal, 1 the claim) against pk (32 bytes),
 * m (k x 32 bytes, the m_i in order) and s (k x 32 bytes, the carried shares). Returns IGNPI_OK or the first failing
 * check's code. Pure over its arguments; callable from any thread. */
int32_t ignpi_verify(uint8_t domain, uint32_t k, const uint8_t *pk, const uint8_t *m, const uint8_t *s, const uint8_t *pi, size_t pi_len);

/* a static NUL-terminated name for a code (for logs) */
const char *ignpi_code_name(int32_t code);

#ifdef __cplusplus
}
#endif

#endif
