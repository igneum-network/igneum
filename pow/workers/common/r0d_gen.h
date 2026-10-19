// r0d_gen.h: the host-side generator of the version 9 (R0D, SPEC-R0D-v0 revision 0.3, 12 October 2026) per-period
// programme for the OpenCL C and Metal dialects. The programme is PROGRAM_OPS operations over the lane's sixteen
// registers, the same on every lane and every nonce within a period, drawn from the period's seed by the reference's
// text (RXE1 mode E's draw with the period in place of the epoch): operation i's eight bytes q come from
// BLAKE3xof(LP("IgneumHash/R0D/op") || LP(program_seed) || LE32(i)), the opcode from q[0] by the weight table, dst,
// src1 and src2 from q[1..4) mod 16, imm from q[4..8); after every 32 operations the range mask f(x) = (x & 0x3FFFFFFF)
// | 0x20000000 on all sixteen registers. Emitted as one function igm_r0d_program(R) with constant indices, one native
// instruction per operation, the FMA as the fused intrinsic (no contraction, no fast math: the preludes' rule).
// GPU engineer B, 12 October 2026.
#pragma once
#include <cstdint>
#include <string>
#include <vector>
#include "ih_params.h"
#include "engine_iface.h"

namespace igm {

struct R0dOp { uint8_t opcode, dst, src1, src2; uint32_t imm; };   // opcode: 0 FMA, 1 FADD, 2 IXOR, 3 IROTL, 4 IADD, 5 IMULHI, 6 POPC, 7 CLZ

// program_seed = BLAKE3(LP("IgneumHash/R0D/program") || LE64(chain_id) || LE64(period) || period_anchor), the reference's encoding
// (igneumhash.cpp's bytes: LP a one-byte length, LE64 raw, the anchor raw).
void r0d_program_seed(uint64_t chain_id, uint64_t period, const uint8_t anchor[32], uint8_t out[32], bool r0e);   // r0e: version 10's seed tag

// The PROGRAM_OPS operations of the seed.
void r0d_generate(const uint8_t seed[32], uint32_t ops, bool eight_ops, uint32_t distance, bool r0e, bool v11, bool v12, std::vector<R0dOp> &out, uint32_t version = 0, uint32_t mma_max = 0);   // version 13: the MMA ceiling is mma_max; version 14: the matrix share pinned at mma_max, the ten floors drawn (SPEC-R0E-v0 2d, 2e)   // v12: the twelve-class composition, SCATTER 11, MMA 12   // v11: the composition drawn per period, the 4 KB table   // r0e: version 10's window draw (SHFL 8, GATHER 9, TILE 10; the item flag 0x80)   // distance: revision 0.4's dependency distance (0: the plain draw)   // eight_ops: features bit 14 (else the 224-of-256 mix of bit 13 alone)

// The seed the engine uses: the knob r0d_seed (64 hex) when set, else derived from the document's chain_id, the height's period
// and the knob r0d_anchor (64 hex; zeros when absent). Returns false when a hex knob is malformed.
bool r0d_seed_of(const IhParams &p, const Knobs &k, uint64_t height, uint8_t out[32], std::string &err);

// The source text of igm_r0d_program for the dialect (msl true: Metal; false: OpenCL C); empty for a document that is not version 9.
std::string r0d_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &err);

// A key of the programme in the engine's build-version word (0 for a document that is not version 9): the seed and the count.
uint32_t r0d_key(const IhParams &p, const Knobs &k, uint64_t height);

// version 12's shared table (SPEC-R0E-v0 2c, main 2a12dee9): the period's seed, BLAKE3(LP("IgneumHash/R0E/shared") || LE64(chain_id) || LE64(period) || anchor),
// from which the engines fill shared_kib x 256 words by the chained scatter over the dataset (the fill kernel igm_r12_shared_fill)
bool r12_shared_seed(const IhParams &p, const Knobs &k, uint64_t height, uint8_t out[32], std::string &err);
bool r0f_period(const IhParams &p, const Knobs &k, uint64_t height, uint8_t key_out[32], std::vector<uint32_t> &prog_words, std::string &err);
bool r0f_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &src, uint8_t key_out[32], std::string &err);   // version 15: the period programme as the kernel function igm_r0f_program with constant indices (the Intel form)   // version 15: the period key and the packed programme
bool r17_key(const IhParams &p, const Knobs &k, uint64_t height, uint8_t key_out[32], std::string &err);   // version 17: the period key (the texture form)
bool r16f_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &src, uint8_t key_out[32], std::string &err);   // version 16: the period key and the static-index programme as kernel text

}  // namespace igm
