/* igneumhash.h: the candidate-specific side of the IgneumHash-R0A v0 reference.
 *
 * Everything a backend needs is behind ignpow.h (candidate id "igneumhash",
 * the variant in the params document). This header adds what the vector tool,
 * the kernel authors and the ablation runner need beyond the shared ABI: the
 * parsed parameter set, its canonical bytes, the variant tag, the per-round
 * trace and the dataset identity. Research only; nothing here is a consensus
 * constant (docs/igneumhash/SPEC-R0A-v0.md).
 */
#ifndef IGNEUMHASH_H
#define IGNEUMHASH_H

#include "ignpow.h"

#include <stdint.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

#define IGNPOW_ALGO_IGNEUMHASH "igneumhash"

/* The parameter set (SPEC-R0A-v0.md section 0.1). The params document is a JSON
 * object with integer members version (MANDATORY: NULL, "null", {} and any
 * document without it are refused with IGNPOW_E_PARAMS), dataset_mib,
 * scratch_kib, rounds, lanes, epoch_blocks, item_chain and, under versions 1
 * and 2, cache_mib and parents (and schedule under 2); absent members other
 * than version take the version's defaults (version 1: 1024, 0, 64, 8, 4096,
 * 1, 32, 256; version 0: 256, 16, 64, 8, 4096, 1), chain_id 7310. */
typedef struct ignh_params {
    uint32_t version;       /* V, 0 */
    uint64_t dataset_bytes; /* DATASET_BYTES */
    uint32_t scratch_bytes; /* SCRATCH_BYTES */
    uint32_t rounds;        /* ROUNDS = ACCESSES */
    uint32_t lanes;         /* LANES, 8 */
    uint64_t epoch_blocks;  /* EPOCH_LENGTH */
    uint32_t item_chain;    /* ITEM_CHAIN (1 under version 1) */
    uint64_t chain_id;      /* C */
    uint64_t cache_bytes;   /* CACHE_BYTES (versions 1 and 2; 0 under version 0) */
    uint32_t parents;       /* PARENTS (versions 1 and 2; 0 under version 0) */
    uint32_t schedule;      /* SCHEDULE (version 2, SPEC-R0B-schedule-v0.md; 0 otherwise) */
    uint32_t item_bytes;    /* ITEM_BYTES (version 3, the item-width variant: 128, 256, 512; 128 otherwise) */
    uint32_t blocks;        /* BLOCKS (version 8, the blended relation: 1, 2, 4; 0 otherwise) */
    uint32_t registers;     /* REGISTERS (version 8: 8 or 16; 0 otherwise) */
    uint32_t features;      /* FEATURES (version 8 options, appended to the record only when non-zero: bit 0 scratch in the block, bit 1 the predicated select, bit 2 two chains, bit 3 four chains, bit 4/5 FP, bit 7 cross-lane, bits 8/9 measurement modes, bit 10 the bond-keyed rounds) */
    uint32_t keyed_rounds;  /* KEYED_ROUNDS (version 8 bit 10, section 0.9: S in 1..ROUNDS, appended at byte 64; 0 otherwise) */
    uint32_t program_ops;   /* PROGRAM_OPS (version 9, SPEC-R0D-v0.md: the per-period programme's operations per lane per round, at byte 68; 0 otherwise) */
    uint64_t period_blocks; /* PERIOD_BLOCKS (version 9: the programme's period in blocks, at byte 72; 0 otherwise) */
    uint32_t table_kib;        /* TABLE_KIB (version 12, SPEC-R0E-v0.md section 2c: the per-nonce shared-memory table in KiB, 1 to 64; be32 at 84) */
    uint32_t shared_kib;       /* SHARED_KIB (version 12, the shared form: 0 per nonce only, or 16 to 64 KiB per period shared by every nonce; be32 at 92) */
    uint32_t mma_max;          /* MMA_MAX (version 13, SPEC-R0E-v0.md section 2d: the INT8 tile's ceiling per window, 0 to 14, the registered constant set by the oldest supported card's class; be32 at 96) */
    uint32_t chains_per_nonce; /* CHAINS_PER_NONCE (version 13: k independent access chains per nonce over disjoint row blocks, 1, 2, 4 or 8; be32 at 100) */
    uint32_t gather_lane_bank; /* GATHER_LANE_BANK (version 13: 1 = the gather's row confined to the lane's bank group, the low three row bits the lane's; be32 at 104) */
    uint32_t tile_shape;       /* TILE_SHAPE (version 14, proof of matrix work: 1 = 16x8x32, 2 = 16x16x16, 3 = 8x8x8, the card's native INT8 tile; be32 at 108) */
    uint32_t fold_every;       /* FOLD_EVERY (version 14: the topology fold once every k tiles, 1, 2, 4 or 8; be32 at 112 only when above 1) */
    uint32_t items_per_round;  /* ITEMS_PER_ROUND (version 14: the items fetched per round, 1 to 32, a power of two; be32 at 116 only when above 1, FOLD_EVERY then always at 112) */
    uint32_t registers_live;   /* REGISTERS_LIVE (version 14: the lane's live register file in words, 16, 64, 128 or 256; be32 at 120 when above 16 or with a hint, FOLD_EVERY and ITEMS_PER_ROUND then always written) */
    uint32_t contexts_hint;    /* CONTEXTS_HINT (version 14: the worker's contexts per core, 0 to 64, a hint the relation ignores; be32 at 124) */
    uint32_t layout_draw;      /* LAYOUT_DRAW (version 14: 1 = each tile's layout, the operand orientation, the accumulation route and the integer format, drawn from the previous tile's result; be32 at 128 when 1) */
    uint32_t difficulty_scale_ppm; /* DIFFICULTY_SCALE_PPM (version 14, the outside review's Q7: the document's normalised difficulty as parts per million of the chain's target, 1,000,000 the registered document's; be32 at 132 when not 1,000,000) */
    uint32_t tile_layout;      /* TILE_LAYOUT (version 14.1: 0 = the row-to-lane layout of version 14; 1 = the fragment layout: A held as the m16n8k32 fragment, lane l's register 4q+j the fragment word j of warp thread (l, q), B read as N rows of K bytes, C landing where the fragment leaves it, the rotation B's column per lane, the orientation B's k reversed, the landing register a rotation within the quad; be32 at 136) */
    uint32_t access_bytes;     /* ACCESS_BYTES (version 12: the gather's and the scatter's row width, 4, 8 or 16; be32 at 88) */
    uint32_t program_distance; /* PROGRAM_DISTANCE (versions 9 and 10, SPEC-R0D-v0.md revision 0.4: the draw's dependency distance, 0 the plain draw; at byte 80 when non-zero) */
    uint8_t program_anchor[32]; /* version 9: the period's anchor (the chain's block hash two blocks before the period, section 3 of the spec); a runtime input like the header, NOT in the canonical bytes; the document's "program_anchor" hex field, zeros when absent (the test anchor) */
    uint32_t bond_rounds;      /* BOND_ROUNDS (F-441, docs/protocol-a/KEYED-RELEASED-VERSIONS-SPEC.md, 17 October 2026): the bond-keyed count S on the released
                                * walks under features bit 10: version 18 the keyed TRIANGLES (S in 1..ROUNDS; written at byte 64, the branch's own slot, as KEYED_ROUNDS),
                                * versions 21 and 25 the keyed ITEM-READ STEPS (S in 1..ROUNDS / FOLD_EVERY; written be32 at byte 88 under bit 10 alone, the record 92 bytes,
                                * KEYED_ROUNDS at 64 keeping the batch form's width as the workers read it); version 8 mirrors KEYED_ROUNDS here; 0 otherwise. Read it
                                * through ignh_keyed_count. */
} ignh_params;

/* The canonical bytes: 36 under version 0, 48 under version 1, 52 under versions 2 to 7, 60 under version 8, 64 with a non-zero FEATURES, 68 with bit 10's KEYED_ROUNDS; 80 under version 9 (PROGRAM_OPS be32 at 68, PERIOD_BLOCKS be64 at 72), 84 with a non-zero PROGRAM_DISTANCE (be32 at 80); 96 under version 12 (PROGRAM_DISTANCE at 80, TABLE_KIB at 84, ACCESS_BYTES at 88, SHARED_KIB at 92); 104 under version 13 (MMA_MAX at 96, CHAINS_PER_NONCE at 100, GATHER_LANE_BANK at 104); 112 under version 14 (TILE_SHAPE at 108), 116 with a FOLD_EVERY above 1 (be32 at 112), 120 with an ITEMS_PER_ROUND above 1 (be32 at 116, FOLD_EVERY then always written), 128 with a REGISTERS_LIVE above 16 or a CONTEXTS_HINT (be32 at 120 and 124, the two before then always written), 132 with LAYOUT_DRAW (be32 at 128), 136 with a DIFFICULTY_SCALE_PPM other than 1,000,000 (be32 at 132, LAYOUT_DRAW then always written), 140 with TILE_LAYOUT (be32 at 136, the two before then always written) (IGNH_PARAMS_BYTES is the buffer size). */
#define IGNH_PARAMS_BYTES 140
size_t ignh_params_len(const ignh_params *p);
/* The bond-keyed count S of a bit 10 document at any version that admits one (8, 18, 21, 25): KEYED_ROUNDS under version 8, BOND_ROUNDS
 * on the released walks; 0 for an unkeyed document. The one accessor the tools and the workers read, so no slot name binds them. */
uint32_t ignh_keyed_count(const ignh_params *p);

/* Parse a params document; IGNPOW_OK or IGNPOW_E_PARAMS. */
int ignh_params_parse(const char *params_json, ignh_params *out);
/* The canonical parameter bytes V || DATASET_BYTES || SCRATCH_BYTES || ROUNDS || LANES || EPOCH_LENGTH || ITEM_CHAIN
 * [|| CACHE_BYTES || PARENTS under version 1] (big-endian); ignh_params_len(p) of them are written. */
void ignh_params_bytes(const ignh_params *p, uint8_t out[IGNH_PARAMS_BYTES]);
/* P = BLAKE3(params bytes). */
void ignh_params_hash(const ignh_params *p, uint8_t out[32]);
/* The DATASET parameter hash Pd (section 0.9, 11 Oct 2026): P of the document with bit 10 cleared and keyed_rounds absent, the
   unkeyed twin's P; the epoch seed E and the dataset id D derive from Pd, so a keyed document and its twin share one dataset.
   Equal to ignh_params_hash for an unkeyed document. The work seed S stays on P (the work identity). */
void ignh_dataset_params_hash(const ignh_params *p, uint8_t out[32]);
/* The variant tag "d<mib>-s<kib>-r<rounds>-c<chain>" (version 0) or "v1-d<mib>-s<kib>-r<rounds>-k<cache_mib>-p<parents>" (NUL-terminated; n is the buffer size). */
void ignh_variant_tag(const ignh_params *p, char *buf, size_t n);
/* Version 8 option (b): the floating form of opcode 2 (SPEC 0.8 bit 4) on raw words; and the same with the product and sum
 * contracted (std::fma), which a contracting build would produce: the fp-contract class records where they differ. */
uint32_t ignh_v8_fop(uint32_t x, uint32_t y, uint32_t z);
uint32_t ignh_v8_fop_contracted(uint32_t x, uint32_t y, uint32_t z);
uint32_t ignh_v8_fop_wide(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z);
uint32_t ignh_v8_fop_wide_contracted(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z);
/* Section 0.9, bit 10 (the bond-keyed rounds): the prover's hash under the 32-byte secret sk (the keyed_rounds shares of 32
   bytes each are written to `shares`, at most ROUNDS x 32 bytes), or with sk NULL the verifier's replay from the shares given with the share. A keyed
   document refuses the keyless ignpow_hash (IGNPOW_E_ARG). */
int ignh_hash_keyed(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32]);
/* ignh_hash_keyed with the keyed rounds' messages m_i written to m_out (keyed_rounds x 32 bytes: m_0 = the work seed S,
   m_i for i >= 1 the eight lane words LE32(R[l][0]) at keyed round i's entry), on the prover's call (sk given) and on the
   verifier's replay (sk NULL) alike: the node's input to the proof pi's verifier (section 0.9.1; the protocol team's ask, 11 Oct
   2026). The existing entry is untouched. */
int ignh_hash_keyed_m(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32], uint8_t *m_out);
/* The anchored forms (versions 9 and 10; DEVNET11-R0E-CHAIN-FORM.md): the period's programme anchor is given per call
   instead of the document's program_anchor, so one context serves two branches with different anchors or two periods
   inside one epoch; the period is height / period_blocks as in the plain calls, the seed BLAKE3(tag || chain_id ||
   period || anchor) as in the plain calls, the drawn programme cached inside the context by (period, anchor) in four
   slots. The plain calls keep the document's anchor (the vector sets and the tools are unchanged). On a version under
   9 the anchor is ignored. */
/* The differential hook for the vectors tool's fill class (the memory cryptanalysis harness's T5c, T6d and T6e; versions 11
   and later): a process-wide flip applied inside the gather table's fill, kind 0 off (the default in every process), 1 = table
   word a bit b flipped after the register mix, 2 = bit b of word 0 of the item fetched at scatter step a flipped before the fold
   and the table write, 3 = bit c of register [a][b] flipped after the lane fill. A node never calls it. */
void ignh_debug_flip(int kind, uint32_t a, uint32_t b, uint32_t c);
/* The Kiln verifier (version 22, pow/ref/src/kiln_verify.hpp): the hash by one named path, 0 the reference render (kiln::attempt, the
   normative text) or 1 the fast render; ignpow_hash takes the fast render unless IGNH_KILN_REFERENCE=1 is in the environment.
   ignh_kiln_count_rows(1) makes the fast render count the distinct dataset rows (256-byte items) its TEX fetches touched, read
   back by ignh_kiln_rows_read after each hash (a measurement aid, off by default). */
int ignh_kiln_hash_path(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32], int path);
void ignh_kiln_count_rows(int on);
uint64_t ignh_kiln_rows_read(void);
int ignh_hash_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t anchor[32], uint8_t mix_out[32], uint8_t hash_out[32]);
int ignh_hash_keyed_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, const uint8_t anchor[32], uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32]);
int ignh_hash_keyed_m_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, const uint8_t anchor[32], uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32], uint8_t *m_out);
/* The test secret of the vector sets and the trace, sk = BLAKE3("IgneumHash/R0A/test-sk" || header): public by construction, never a bond's. */
/* the texture (17) and raster (18) period keys and the two-level form's period seed (the workers' exports; the reference's own draws,
 * byte for byte): versions 21, 24 and 25 draw the period seed as forge2's, the batch form (period_blocks 0) through
 * ignh_forge2_period_seed and the chain form (period_blocks set) through ignh_forge2_period_seed_chain; IGNPOW_E_ARG otherwise */
int ignh_r17_key(const ignh_params *p, uint64_t height, uint8_t key_out[32]);
int ignh_r18_key(const ignh_params *p, uint64_t height, uint8_t key_out[32]);
int ignh_forge2_period_seed(const ignh_params *p, const uint8_t E[32], const uint8_t header[32], uint64_t batch, uint8_t seed_out[32]);
int ignh_forge2_period_seed_chain(const ignh_params *p, const uint8_t E[32], uint64_t height, const uint8_t anchor[32], uint8_t seed_out[32]);
void ignh_test_sk(const uint8_t header[32], uint8_t sk_out[32]);
/* Section 0.9.1: the two keyed functions on their own, for an oracle, a prover or a test (11 Oct 2026, security's ask):
   pk = G(sk) = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk) with tag(T) = u8(len(T)) || T, and the keyed share
   s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i) exactly as the walk computes it (m_0 = the work seed S; m_i for i >= 1 the
   eight lane words LE32(R[l][0]) at the keyed round's entry). */
void ignh_bond_pk(const uint8_t sk[32], uint8_t pk_out[32]);
void ignh_bond_share(const uint8_t sk[32], uint32_t i, const uint8_t m[32], uint8_t share_out[32]);

/* The dataset identity of the context's epoch: the epoch seed E, the
 * commitment D, the diagnostic BLAKE3 over the version 1 cache in item order
 * (zero under version 0), and (full contexts only) the diagnostic BLAKE3 over
 * the whole dataset in item order with the build time in milliseconds; a light
 * context leaves digest zeroed and *build_ms 0 and sets *has_digest 0. */
int ignh_dataset_info(const ignpow_ctx *ctx, uint8_t E[32], uint8_t D[32], uint8_t cache_digest[32],
                      uint8_t digest[32], uint64_t *build_ms, int *has_digest);

/* Write the complete trace of one hash as a JSON object on out (one line, no
 * trailing newline): S, E, the lane registers after initialisation, after the
 * fill and after every round with that round's item index, the scratch fold,
 * M and F. For kernel authors; not a gate class. */
int ignh_trace(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE *out);

/* Version 9 (SPEC-R0D-v0.md section 3): the period's programme for a document and a height, as a worker compiles it into its
   kernel (GPU engineer A's compiled form, 12 Oct 2026): PROGRAM_OPS records of 8 bytes in draw order, each
   opcode (0 FMA, 1 FADD, 2 IXOR, 3 IROTL, 4 IADD, 5 IMULHI, 6 POPC, 7 CLZ), dst, src1, src2 (one byte each) and imm (LE32).
   The period is height div PERIOD_BLOCKS, the anchor the params' field. Returns the count written, IGNPOW_E_ARG when the
   document is not version 9 (or a pointer is null), IGNPOW_E_BOUNDARY when PROGRAM_OPS exceeds max_ops. No dataset is built. */
int ignh_r0d_program(const ignh_params *p, uint64_t height, uint8_t *out, size_t max_ops);
/* versions 15 and 16: the period's key (32 bytes) and programme (version 15 program_ops records, version 16 rounds x 224 records, each
   8 bytes: opcode 0 MAD 1 ADD 2 SUB 3 XOR 4 ROTL 5 ROTR, d, a, b, imm LE32) at height, over the document's program_anchor; returns the count */
int ignh_r16_period(const ignh_params *p, uint64_t height, uint8_t key_out[32], uint8_t *out, size_t max_ops);
/* the same with each record's third register (revision 13, the revision 14 variants) written to c_out[i] when c_out is given */
int ignh_r16_period_c(const ignh_params *p, uint64_t height, uint8_t key_out[32], uint8_t *out, size_t max_ops, uint8_t *c_out);
/* Version 17 (the texture form): the period key K = BLAKE3(LP("IgneumHash/R0H/key") || LE64 chain_id || LE64 (height / period_blocks) || program_anchor), 32 bytes. */
int ignh_r17_key(const ignh_params *p, uint64_t height, uint8_t key_out[32]);
/* Version 18 (the raster form): the period key K = BLAKE3(LP("IgneumHash/R0I/key") || LE64 chain_id || LE64 (height / period_blocks) || program_anchor), 32 bytes. */
int ignh_r18_key(const ignh_params *p, uint64_t height, uint8_t key_out[32]);
/* versions 12 to 14, the shared form: the period's shared-table seed (BLAKE3 of the tag, the chain id, the period and the anchor), so a
 * worker fills the table on its own device by the reference's chained walk; IGNPOW_E_ARG unless the document has shared_kib */
int ignh_r12_shared_seed(const ignh_params *p, uint64_t height, uint8_t out[32]);

/* the security review's access trace of one hash: on addr_out one line per dataset
 * read {"epoch", "nonce", "round", "lane", "site", "addr"} (site r is round
 * r's item index, produced by lane r mod 8 in the previous round; site -1 is
 * the first address of SPEC section 4.3), on lane_out one line per lane and
 * round {"epoch", "nonce", "round", "lane", "from": [l, p, q]} naming the lanes
 * whose state flowed into lane l at that round's exchange. Either FILE may be
 * NULL. The caller writes the trace's meta line. */
int ignh_access_trace(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
                      FILE *addr_out, FILE *lane_out);

#ifdef __cplusplus
}
#endif

#endif /* IGNEUMHASH_H */
