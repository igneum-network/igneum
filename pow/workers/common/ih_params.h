// ih_params.h: the host side of IgneumHash-R0A v0 (SPEC-R0A-v0.md section 0, 1, 5.2):
// the params document parsed with the spec's defaults and ranges, the 36-byte params
// encoding and P = BLAKE3(params), C and V, the derived sizes. The epoch seed E comes
// from the reference through ignpow_ctx_light_cache (32 bytes). GPU engineer B, 10 Oct 2026.
#pragma once
#include <cstdint>
#include <string>

namespace igm {

struct IhParams {
    uint32_t version = 1;
    uint64_t dataset_bytes = 1ull << 30;
    uint32_t scratch_bytes = 0;
    uint64_t cache_bytes = 1ull << 25;   // version 1 only
    uint32_t parents = 256;             // version 1 only
    uint32_t item_bytes = 128;          // version 3: the item width W (128, 256, 512); 128 is version 1's rule exactly
    uint32_t rounds = 64;
    uint32_t lanes = 8;
    uint64_t epoch_blocks = 4096;
    uint32_t item_chain = 1;
    uint64_t chain_id = 7310;
    uint32_t keyed_rounds = 0;   // version 8 bit 10 (SPEC 0.9): the bond-keyed rounds S in 1..rounds, mandatory with the bit, absent without; u32be at byte 64
    uint32_t keyed_steps = 0;    // versions 21 and 25 bit 10 (KEYED-RELEASED-VERSIONS-SPEC.md 2, cb73f3b33): the bond-keyed rounds as keyed-read steps S in 1..R (R = rounds / fold_every), u32be at byte 88 under the bit alone; keyed_rounds keeps the batch form's width under 21 and 25
    uint32_t program_ops = 0; uint64_t period_blocks = 0; std::string program_anchor; uint32_t program_distance = 0; uint32_t table_kib = 0, access_bytes = 0;
    // versions 12 to 14 (SPEC-R0E-v0 2c to 2e, main 2a12dee9): shared_kib the period's shared table (0 or 16..64 KB); mma_max the pinned matrix share; chains_per_nonce the
    // independent access chains; gather_lane_bank the bank-confined gather row; tile_shape 1 = 16x8x32 (the canonical operation); fold_every the topology fold's period;
    // items_per_round, registers_live, contexts_hint, layout_draw, difficulty_scale_ppm as the reference carries them
    uint32_t shared_kib = 0, mma_max = 0, chains_per_nonce = 0, gather_lane_bank = 0, tile_shape = 0, fold_every = 0, items_per_round = 0, registers_live = 0, contexts_hint = 0, layout_draw = 0, tile_layout = 0, difficulty_scale_ppm = 0;
    uint32_t table_words() const { return version >= 12 ? table_kib * 256u : version == 11 ? 1024u : 4096u; }   // the per-nonce table's words
    uint32_t shared_words() const { return version >= 12 ? shared_kib * 256u : 0u; }   // table_kib, access_bytes: version 12 (the per-nonce local table, the row's bytes)   // program_distance: revision 0.4, 0 to 8 (0 or absent: the plain draw; non-zero: be32 at byte 80, 84 bytes)   // program_anchor: a runtime input (64 hex), not in the record; absent means zeros (the test anchor)   // version 9 (SPEC-R0D-v0 0.3, 12 Oct 2026): the per-period programme's operations per lane per round and its period in blocks; features bit 13
    uint32_t blocks = 0, registers = 0, features = 0;   // version 8 (SPEC 0.8, the blended relation): BLOCKS 1, 2 or 4; REGISTERS 8 or 16; FEATURES bits (0 scratch, 1 predicated select, 2 or 3 chains, 4 binary32)
    // derived
    uint32_t n_items() const { return (uint32_t)(dataset_bytes / item_bytes); }
    uint32_t rw() const { return (version == 8 && registers > 16) ? registers : 16; }   // the state's registers per lane (the wider state, SPEC 0.8, reference 573303af); 8 and 16 leave the 16-word state
    uint32_t st_words() const { return 8 * rw() + 32; }   // the nonce's state area in words (160 at the 16-register state), the kernel's IH_ST_WORDS
    bool fastrange() const { uint64_t n = n_items(); return version >= 8 && (n & (n - 1)) != 0; }   // the multiply-high address map (SPEC 0.5's form) for version 8's non-power-of-two sizes; versions 0 to 6 mask
    uint32_t item_words() const { return item_bytes / 4; }
    uint32_t sub_blocks() const { return item_bytes / 128; }
    uint32_t slice_words() const { return scratch_bytes / (4 * lanes); }
    bool forge() const { return version == 20 || version == 21 || version == 25; }    // the Forge kernel (igm_forge.inc): 16 work-items an attempt
    bool forge2() const { return version == 21 || version == 25; }                    // forge2's engine (the period's shapes, fp_mode, pair, prefetch, lean); 25 = entry 2, the scratch steps
    uint32_t forge_local_bytes() const { return version == 25 ? 1024u + 64u + scratch_bytes : 16384u + 64u; }   // the attempt's local memory: version 20/21 the 16 KiB scratchpad and the exchange; version 25 the window's 1 KiB, the exchange and the per-hash scratchpad
    uint32_t slice_lines() const { return slice_words() / 4; }
    uint32_t n_cache() const { return (uint32_t)(cache_bytes / 128); }
    size_t params_len() const { return version == 22 ? 96 : version == 21 || version == 25 ? ((features & 1024u) ? 92 : 88) : version == 20 ? 88 : version == 17 || version == 18 ? 80 : version == 16 ? (program_distance ? 84 : 80) : version == 15 ? 80 : version == 14 ? (tile_layout ? 140 : difficulty_scale_ppm != 1000000u ? 136 : layout_draw ? 132 : (registers_live > 16 || contexts_hint) ? 128 : items_per_round > 1 ? 120 : fold_every > 1 ? 116 : 112) : version == 13 ? 108 : version == 12 ? 96 : version >= 9 ? (program_distance ? 84 : 80) : version == 8 ? (features ? ((features & 1024u) ? 68 : 64) : 60) : version >= 3 ? 52 : version >= 1 ? 48 : 36; }
    std::string variant() const { std::string b = "d" + std::to_string(dataset_bytes >> 20) + "-s" + std::to_string(scratch_bytes >> 10) + "-r" + std::to_string(rounds); return version >= 8 ? "v" + std::to_string(version) + "-" + b + "-k" + std::to_string(cache_bytes >> 20) + "-p" + std::to_string(parents) + "-w" + std::to_string(item_bytes) + "-b" + std::to_string(blocks) + "-x" + std::to_string(registers) + (features ? "-f" + std::to_string(features) : "") + ((features & 1024u) ? "-k" + std::to_string(keyed_rounds) : "") + (version >= 12 ? "-t" + std::to_string(table_kib) + "a" + std::to_string(access_bytes) + (shared_kib ? "s" + std::to_string(shared_kib) : "") : "") + (version >= 13 ? "-m" + std::to_string(mma_max) + (chains_per_nonce > 1 ? "c" + std::to_string(chains_per_nonce) : "") + (gather_lane_bank ? "b" : "") : "") + (version == 14 ? "-q" + std::to_string(tile_shape) + "f" + std::to_string(fold_every) + (layout_draw ? "l" : "") + (items_per_round > 1 ? "i" + std::to_string(items_per_round) : "") + (registers_live > 16 ? "r" + std::to_string(registers_live) : "") : "") + (version >= 9 ? "-o" + std::to_string(program_ops) + (program_distance ? "-d" + std::to_string(program_distance) : "") : "") : version >= 3 ? "v" + std::to_string(version) + "-" + b + "-k" + std::to_string(cache_bytes >> 20) + "-p" + std::to_string(parents) + "-w" + std::to_string(item_bytes) : version >= 1 ? "v1-" + b + "-k" + std::to_string(cache_bytes >> 20) + "-p" + std::to_string(parents) : b + "-c" + std::to_string(item_chain); }
};

// Parses the JSON params document (empty or "{}" = defaults). Returns false with `err` on a
// member outside the spec's ranges or an unknown member.
bool ih_parse_params(const std::string &json, IhParams &out, std::string &err);

// The params encoding of section 0.1 (36 bytes under version 0, 48 under version 1, 52 under version 3; out holds 52) and P = BLAKE3(params) (32 bytes).
void ih_params_bytes(const IhParams &p, uint8_t out[140]);
void ih_P(const IhParams &p, uint8_t out[32]);

// The AMD dataset swizzle's slot map (the convergence design; the kernels' ih_slot): item i lives at slot(i) on the device
inline uint32_t ih_swizzle_slot(uint32_t i, uint32_t mask) { return (i ^ ((i >> 8) & 0xffu) ^ (((i >> 16) & 0xffu) << 4)) & mask; }

// Full-message BLAKE3 (hash mode) on the host, incremental: the dataset and cache digests of the vectors' dataset class
// (the reference digests its dataset with BLAKE3 over the bytes in order; this hasher agrees with it or the class mismatches).
struct IhB3 {
    uint32_t cv_stack[54][8]; int stack_len = 0;
    uint32_t chunk_cv[8]; uint64_t chunk_counter = 0; uint8_t block[64]; uint32_t block_len = 0; uint32_t blocks_compressed = 0;
    IhB3();
    void update(const uint8_t *in, size_t n);
    void finalize(uint8_t out[32]);
};
void ih_blake3_full(const uint8_t *in, size_t n, uint8_t out[32]);
void ih_blake3_from_chunk_cvs(const uint32_t *cvs, size_t n, uint8_t out[32]);   // the root over n >= 2 chunk chaining values computed on the card (raster_gl_card.h)
// the final digest F = BLAKE3(tag(final) || C || V || P || S || M) with the work seed S = BLAKE3(tag(seed) || C || V || P || H || u64be(N)): a pure
// function of the document's consts, the header, the nonce and the mix, no dataset (13 Oct 2026: the Metal compiler's keyed search library
// leaves F's first word zero on the M5 and M6 while M is exact, so the Metal engine computes F here for a keyed document)
void ih_final_digest(uint64_t chain_id, uint32_t version, const uint8_t P[32], const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], uint8_t out[32]);
void ih_blake3_xof(const uint8_t *in, size_t n, uint8_t *out, size_t outlen);   // a single-block input's XOF (version 10's window draw)

// One-chunk BLAKE3 on the host (inputs up to 1024 bytes), hash mode; keyed when key is given.
void ih_blake3_host(const uint8_t *in, size_t n, const uint8_t *key32_or_null, uint8_t out[32]);

}  // namespace igm
