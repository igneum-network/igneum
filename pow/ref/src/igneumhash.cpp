// Candidate "igneumhash": IgneumHash-R0A v0, the C reference of
// docs/igneumhash/SPEC-R0A-v0.md (revision 0.1). Research only.
//
// Written from the specification text; protocol B's Go implementation is
// written from the same text and never from this file. A mismatch between the
// two is a specification defect and is fixed in the text first.
//
// Mapping onto the ABI: header = H (the node's 32-byte sealing hash); nonce =
// N as a host-order uint64 (serialised u64be inside the seed); mix_out = M (the
// state digest, the header's MixDigest); hash_out = F (the final digest,
// compared as a big-endian 256-bit integer against the boundary). A light
// context holds the epoch seed only and derives every item it touches; a full
// context holds the dataset. Both give the same bytes.

#include "candidate.hpp"
#include "igneumhash.h"
#include "blake3.h"
#include "blake3_vectors.hpp"
#include "r0a_dataset.hpp"

#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <cstdlib>
#include <cfloat>
#include <cmath>
#include <cstring>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>
#include <unordered_map>
#include <array>

namespace ignpow {
namespace {

constexpr uint32_t kItemBytes = 128;
constexpr uint32_t kLaneWords = 16;
constexpr uint32_t kLineWords = 4;
constexpr uint32_t kMul = 0x9E3779B1u;
constexpr uint64_t kMaxHeight = 0x7fffffffull;
constexpr uint32_t kRotationVersion = 0xFFFF;  // the four-family rotation (section 2f) WITHDRAWN 13 Oct 2026 14:17 UK, never ported; its code kept behind this unreachable version word

const char* const kTagSeed = "IgneumHash/R0A/seed";
const char* const kTagEpoch = "IgneumHash/R0A/epoch";
const char* const kTagItem = "IgneumHash/R0A/item";
const char* const kTagDataset = "IgneumHash/R0A/dataset";
const char* const kTagLanes = "IgneumHash/R0A/lanes";
const char* const kTagMix = "IgneumHash/R0A/mix";
const char* const kTagFinal = "IgneumHash/R0A/final";
const char* const kTagCache = "IgneumHash/R0A/cache";


// ---- byte helpers ----

inline uint32_t rotl32(uint32_t x, uint32_t n) { n &= 31; return n ? (x << n) | (x >> (32 - n)) : x; }
inline uint32_t rotr32(uint32_t x, uint32_t n) { n &= 31; return n ? (x >> n) | (x << (32 - n)) : x; }
inline uint32_t le32(const uint8_t* p) { return uint32_t(p[0]) | (uint32_t(p[1]) << 8) | (uint32_t(p[2]) << 16) | (uint32_t(p[3]) << 24); }
inline void put_le32(uint8_t* p, uint32_t v) { p[0] = uint8_t(v); p[1] = uint8_t(v >> 8); p[2] = uint8_t(v >> 16); p[3] = uint8_t(v >> 24); }
inline void put_be32(uint8_t* p, uint32_t v) { for (int i = 0; i < 4; ++i) p[i] = uint8_t(v >> (8 * (3 - i))); }
inline void put_be64(uint8_t* p, uint64_t v) { for (int i = 0; i < 8; ++i) p[i] = uint8_t(v >> (8 * (7 - i))); }

// A growing byte buffer for hash inputs.
struct bytes {
    std::vector<uint8_t> v;
    bytes& tag(const char* t) { size_t n = std::strlen(t); v.push_back(uint8_t(n)); v.insert(v.end(), t, t + n); return *this; }
    bytes& raw(const uint8_t* p, size_t n) { v.insert(v.end(), p, p + n); return *this; }
    bytes& le32(uint32_t x) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    bytes& le64(uint64_t x) { for (int i = 0; i < 8; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    bytes& lp(const uint8_t* p, size_t n) { v.push_back(uint8_t(n)); return raw(p, n); }
    bytes& be32(uint32_t x) { uint8_t b[4]; put_be32(b, x); return raw(b, 4); }
    bytes& be64(uint64_t x) { uint8_t b[8]; put_be64(b, x); return raw(b, 8); }
};

void b3_hash(const uint8_t* in, size_t n, uint8_t* out, size_t outlen) {
    blake3_hasher h;
    blake3_hasher_init(&h);
    blake3_hasher_update(&h, in, n);
    blake3_hasher_finalize(&h, out, outlen);
}

void b3_hash(const bytes& b, uint8_t* out, size_t outlen = 32) { b3_hash(b.v.data(), b.v.size(), out, outlen); }

// ---- parameters ----

bool is_pow2(uint64_t x) { return x && (x & (x - 1)) == 0; }

bool parse_u64(const std::string& s, uint64_t& out) {
    if (s.empty() || s.size() > 20) return false;
    uint64_t v = 0;
    for (char c : s) {
        if (c < '0' || c > '9') return false;
        if (v > (UINT64_MAX - 9) / 10) return false;
        v = v * 10 + uint64_t(c - '0');
    }
    out = v;
    return true;
}

// Strict reader of {"key": integer, ...}: only the known keys, decimal
// non-negative integers, no nesting, no strings as values. Defaults depend on
// the version (SPEC-R0A-v0.md section 0.1).
int parse_params(const char* json, ignh_params& p) {
    uint64_t version = 0, mib = 0, kib = 0, rounds = 64, lanes = 8, epoch = 4096, chain = 1, cid = 7310, cmib = 32, parents = 256, sched = 8, ib = 128;
    uint64_t read_every_v = 0; bool have_read_every = false; uint64_t target_log2_v = 0; bool have_target_log2 = false; uint64_t drawn_order_v = 0; bool have_drawn_order = false; uint64_t sequence_v = 32, batch_log2_v = 12, fp_mode_v = 0, pair_log2_v = 0, prefetch_v = 0, tiles_v = 64, rows_v = 0, spread_v = 7, fragments_v = 0, coalesced_v = 0, contig_v = 1, lean_v = 0, chain_select_v = 0, route_v = 0, read_rule_v = 0, draw_v = 0, mix_v = 0; bool have_lean = false, have_chain_select = false, have_route = false, have_read_rule = false, have_draw = false, have_mix = false; bool have_coalesced = false, have_contig = false; bool have_sequence = false, have_batch_log2 = false, have_fp_mode = false, have_pair_log2 = false, have_prefetch = false, have_tiles = false, have_rows = false, have_spread = false, have_fragments = false;
    bool have_mib = false, have_kib = false, have_cmib = false, have_parents = false, have_chain = false, have_sched = false, have_version = false, have_ib = false, have_blk = false, have_regs = false;
    uint64_t blk = 0, regs = 0, feat = 0, keyed = 0; bool have_feat = false, have_keyed = false;
    uint64_t pops = 0, pblk = 0, pdist = 0; bool have_pops = false, have_pblk = false, have_pdist = false; uint8_t anchor[32] = {}; bool have_anchor = false;
    uint64_t reach_v = 0; bool have_reach = false;
    uint64_t tlayout = 0; bool have_tlayout = false; bool have_rounds16 = false; uint64_t tails_v = 1; bool have_tails = false; uint64_t weights_v = 0; bool have_weights = false; uint64_t rev_v = 0; bool have_rev = false; uint64_t variant_v = 0; bool have_variant = false;  // tile_layout (version 14.1): 1 = the fragment layout (the tile's operands held as the registered card's mma fragment; the project's order of 05:1x on the GPU team's rearrangement reading)
    uint64_t tkib = 16, abytes = 16, skib = 0, mmax = 14, chains = 1, glb = 0, tshape = 1; bool have_tkib = false, have_abytes = false, have_skib = false, have_mmax = false, have_chains = false, have_glb = false, have_tshape = false; uint64_t fevery = 1, ipr = 1, rlive = 16, chint = 0, ldraw = 0, dscale = 1000000; bool have_fevery = false, have_ipr = false, have_rlive = false, have_chint = false, have_ldraw = false, have_dscale = false;  // difficulty_scale_ppm (version 14, the outside review's Q7): the document's normalised difficulty, parts per million of the chain's target, 1,000,000 the registered document's  // layout_draw (version 14): 1 = each tile's layout (orientation, accumulation route, integer format) drawn from the previous tile's result  // registers_live (version 14): the lane's live register file, 16 (the programme registers alone), 64, 128 or 256 words; contexts_hint: the worker's contexts per core, a hint the relation ignores  // items_per_round (version 14): the items fetched per round, 1 to 32 (a power of two), the extra items feeding the tiles  // fold_every (version 14): the topology fold once every k tiles, 1, 2, 4 or 8; tile_shape (version 14, proof of matrix work): 1 = 16x8x32, 2 = 16x16x16, 3 = 8x8x8  // gather_lane_bank (version 13): 1 = the gather's row confined to the lane's bank group (the project's order on the GPU team's bank-conflict reading)  // chains_per_nonce (version 13): k independent access chains per nonce, 1, 2, 4 or 8 (the GPU team's parallelism lever)  // version 13: the MMA ceiling per window (0 to 14; 14 registered, the 580 outside the cohort)  // shared_kib: 0 (per nonce only) or 16 to 64 (the per-period shared table, the GPU team's occupancy reading)  // version 12: the per-nonce table in KiB (16 to 64) and the access width in bytes (4, 8, 16)
    if (json != nullptr) {
        std::string t;
        for (const char* c = json; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') t += *c;
        if (!(t.empty() || t == "null" || t == "{}")) {
            if (t.front() != '{' || t.back() != '}') return IGNPOW_E_PARAMS;
            std::string body = t.substr(1, t.size() - 2);
            size_t pos = 0;
            while (pos < body.size()) {
                if (body[pos] != '"') return IGNPOW_E_PARAMS;
                size_t q = body.find('"', pos + 1);
                if (q == std::string::npos) return IGNPOW_E_PARAMS;
                std::string key = body.substr(pos + 1, q - pos - 1);
                pos = q + 1;
                if (pos >= body.size() || body[pos] != ':') return IGNPOW_E_PARAMS;
                ++pos;
                size_t e = body.find(',', pos);
                std::string val = body.substr(pos, e == std::string::npos ? std::string::npos : e - pos);
                pos = e == std::string::npos ? body.size() : e + 1;
                uint64_t v = 0;
                if (key == "program_anchor") {
                    // version 9's period anchor: 64 hex characters in quotes (a runtime input, not a record field)
                    if (val.size() != 66 || val.front() != '"' || val.back() != '"') return IGNPOW_E_PARAMS;
                    for (int i = 0; i < 32; ++i) {
                        unsigned b = 0;
                        for (int k = 0; k < 2; ++k) {
                            const char ch = val[1 + 2 * i + k]; unsigned d;
                            if (ch >= '0' && ch <= '9') d = unsigned(ch - '0'); else if (ch >= 'a' && ch <= 'f') d = unsigned(ch - 'a' + 10); else if (ch >= 'A' && ch <= 'F') d = unsigned(ch - 'A' + 10); else return IGNPOW_E_PARAMS;
                            b = (b << 4) | d;
                        }
                        anchor[i] = uint8_t(b);
                    }
                    have_anchor = true;
                    continue;
                }
                if (!parse_u64(val, v)) return IGNPOW_E_PARAMS;
                if (key == "dataset_mib") { mib = v; have_mib = true; }
                else if (key == "scratch_kib") { kib = v; have_kib = true; }
                else if (key == "rounds") { rounds = v; have_rounds16 = true; }
                else if (key == "lanes") lanes = v;
                else if (key == "epoch_blocks") epoch = v;
                else if (key == "item_chain") { chain = v; have_chain = true; }
                else if (key == "chain_id") cid = v;
                else if (key == "version") { version = v; have_version = true; }
                else if (key == "cache_mib") { cmib = v; have_cmib = true; }
                else if (key == "parents") { parents = v; have_parents = true; }
                else if (key == "schedule") { sched = v; have_sched = true; }
                else if (key == "item_bytes") { ib = v; have_ib = true; }
                else if (key == "blocks") { blk = v; have_blk = true; }
                else if (key == "registers") { regs = v; have_regs = true; }
                else if (key == "features") { feat = v; have_feat = true; }
                else if (key == "keyed_rounds") { keyed = v; have_keyed = true; }
                else if (key == "program_ops") { pops = v; have_pops = true; }
                else if (key == "period_blocks") { pblk = v; have_pblk = true; }
                else if (key == "program_distance") { pdist = v; have_pdist = true; }
                else if (key == "table_kib") { tkib = v; have_tkib = true; }
                else if (key == "access_bytes") { abytes = v; have_abytes = true; }
                else if (key == "shared_kib") { skib = v; have_skib = true; }
                else if (key == "mma_max") { mmax = v; have_mmax = true; }
                else if (key == "chains_per_nonce") { chains = v; have_chains = true; }
                else if (key == "gather_lane_bank") { glb = v; have_glb = true; }
                else if (key == "tile_shape") { tshape = v; have_tshape = true; }
                else if (key == "fold_every") { fevery = v; have_fevery = true; }
                else if (key == "items_per_round") { ipr = v; have_ipr = true; }
                else if (key == "registers_live") { rlive = v; have_rlive = true; }
                else if (key == "contexts_hint") { chint = v; have_chint = true; }
                else if (key == "layout_draw") { ldraw = v; have_ldraw = true; }
                else if (key == "difficulty_scale_ppm") { dscale = v; have_dscale = true; }
                else if (key == "tile_layout") { tlayout = v; have_tlayout = true; }
                else if (key == "tails") { tails_v = v; have_tails = true; }
                else if (key == "weights") { weights_v = v; have_weights = true; }
                else if (key == "revision") { rev_v = v; have_rev = true; }
                else if (key == "variant") { variant_v = v; have_variant = true; }
                else if (key == "read_every") { read_every_v = v; have_read_every = true; }
                else if (key == "target_log2") { target_log2_v = v; have_target_log2 = true; }
                else if (key == "reach") { reach_v = v; have_reach = true; }
                else if (key == "drawn_order") { drawn_order_v = v; have_drawn_order = true; }
                else if (key == "sequence") { sequence_v = v; have_sequence = true; }
                else if (key == "batch_log2") { batch_log2_v = v; have_batch_log2 = true; }
                else if (key == "fp_mode") { fp_mode_v = v; have_fp_mode = true; }
                else if (key == "pair_log2") { pair_log2_v = v; have_pair_log2 = true; }
                else if (key == "prefetch") { prefetch_v = v; have_prefetch = true; }
                else if (key == "lean") { lean_v = v; have_lean = true; }
                else if (key == "chain_select") { chain_select_v = v; have_chain_select = true; }
                else if (key == "route") { route_v = v; have_route = true; }
                else if (key == "read_rule") { read_rule_v = v; have_read_rule = true; }
                else if (key == "draw") { draw_v = v; have_draw = true; }
                else if (key == "mix") { mix_v = v; have_mix = true; }
                else if (key == "tiles") { tiles_v = v; have_tiles = true; }
                else if (key == "rows") { rows_v = v; have_rows = true; }
                else if (key == "spread") { spread_v = v; have_spread = true; }
                else if (key == "fragments") { fragments_v = v; have_fragments = true; }
                else if (key == "coalesced") { coalesced_v = v; have_coalesced = true; }
                else if (key == "contig") { contig_v = v; have_contig = true; }
                else return IGNPOW_E_PARAMS;
            }
        }
    }
    // "version" is mandatory (security's F-14, SPEC section 0.1): an absent version is a refusal, never a default.
    if (!have_version || version > 25) return IGNPOW_E_PARAMS;
    // THE DRAWER (the project's order of 15 October 2026 11:5x UK, the registry's strand move): one successor form a lane, cut and exact,
    // never registered until needed: "read_rule" 1 the bandwidth lane's second keyed-read rule (version 15), "draw" 1 the raster
    // lane's second drawing rule (version 18), "mix" 1 the Anvil lane's second step mix (version 25); each 0 or 1, each refused on
    // every other version, each bound in P (the version's program_distance word)
    if ((have_read_rule && (version != 15 || read_rule_v > 1)) || (have_draw && (version != 18 || draw_v > 1)) || (have_mix && (version != 25 || mix_v > 1))) return IGNPOW_E_PARAMS;
    if (version == 23) {
        // VERSION 23, TENSOR (namespace tensor; the measurement document of the design review's priced page TENSOR-READ-FORM.md fa3cd133c, the project's
        // order of 15 October 2026 08:1x UK): the hash's work is keyed 256-byte reads feeding INT8 matrix multiplies with INT32 accumulation
        // (the one class exact on four vendors). THE STATE: "tiles" (2 to 16, 4 the document) 16 x 16 INT8 matrices S[0..tiles-1], drawn from
        // the init BLAKE3(Tensor/init || E || period_seed || S) expanded by the fold chain. THE STEP (rounds steps, 16 to 4096, 128 the
        // document): one keyed item (256 bytes) at index fchain(x, 8192 + t) mod items, x the state's read word (the init's for step 0, then
        // the fold of the step's results), the item read as the 16 x 16 INT8 matrix B_t in row order; the drawn sequence of the hash
        // (fchain(y[t & 7], t) mod tiles! over the hash's seed, a permutation index a step) routes the chain: for j in 0..tiles-1 in the
        // permutation's order, C = S[p_j] x B_t (16 x 16 x 16 INT8 x INT8 products summed into INT32 over k = 0..15 ascending; every partial
        // sum within +-262,144, so the sum is exact in any order and the stated order is the reference's), then the fixed fold on the lanes
        // (lane i the matrix row): S[p_j][i][k] = byte 3 of fmix32(uint32(C[i][k]) + (S[p_j][i][k] << 24) + (j + 1) x 0x9E3779B9), and the read
        // word x = fchain(x ^ C[0][0] ^ C[15][15], j) after each multiply (the next read depends on every multiply of the step). THE LEAN
        // FIXED COST: the init one BLAKE3, the mix BLAKE3(Tensor/mix || E || period_seed || the 16 lanes' 16-byte accumulators over their rows
        // of every tile (acc[n & 3] = (acc ^ w) + w over the row's words)). THE PERIOD SEED as version 21's (the chain form with
        // period_blocks and the anchor, or the nonce batch with batch_log2). Fields: rounds, tiles, period_blocks + program_anchor or
        // batch_log2, lanes 16, dataset_mib, cache_mib, parents, item_bytes 256, epoch_blocks, chain_id, lean 1 (the only form); no other key
        if (have_pblk && have_batch_log2) return IGNPOW_E_PARAMS;
        if (!have_pblk && !have_batch_log2) return IGNPOW_E_PARAMS;
        if (have_pblk && (pblk < 64 || pblk > epoch)) return IGNPOW_E_PARAMS;
        if (!have_pblk && have_anchor) return IGNPOW_E_PARAMS;
        if (have_sched || have_blk || have_feat || have_keyed || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist || have_target_log2 || have_drawn_order || have_fp_mode || have_sequence || have_pair_log2 || have_kib || have_regs || have_fevery || have_prefetch || have_chain_select || have_route || have_pops || have_rows || have_spread || have_fragments || have_coalesced || have_contig) return IGNPOW_E_PARAMS;
        if (!have_mib || mib < 1024 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256)) return IGNPOW_E_PARAMS;
        const uint64_t steps = have_rounds16 ? rounds : 128, nt = have_tiles ? tiles_v : 4, bl = have_batch_log2 ? batch_log2_v : 0;
        if (lanes != 16 || chain != 1 || epoch < 1 || steps < 16 || steps > 4096 || nt < 2 || nt > 16 || (have_batch_log2 && (bl < 1 || bl > 40)) || (have_lean && lean_v != 1)) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 23; p.dataset_bytes = mib << 20; p.scratch_bytes = 0; p.rounds = uint32_t(steps); p.lanes = 16; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = cmib << 20; p.parents = uint32_t(parents); p.item_bytes = 256; p.registers = 0; p.program_ops = uint32_t(nt); p.blocks = 0; p.keyed_rounds = uint32_t(bl); p.period_blocks = have_pblk ? pblk : 0; p.fold_every = 0; p.program_distance = 32768u;  // the lean bit, the one form
        if (have_pblk) std::memcpy(p.program_anchor, anchor, 32);
        return IGNPOW_OK;
    }
    if (version == 22) {
        // VERSION 22, KILN (FORM-FORGE.md section 6, the shader-batch form; the project's order of 15 October 2026 02:2x UK): per BATCH (2^batch_log2
        // nonces under one header) a fragment programme of program_ops words (32) from the batch seed, compiled by the card's driver (the
        // port emits GLSL/SPIR-V or MSL from the words); per HASH a scene from the work seed: rounds (T, 4) integer-corner triangles over a
        // 2^target_log2 square RGBA8 target (32, zero at the attempt's start, w = 1, the top-left rule), one keyed 64 x 64 R32 tile of the
        // epoch dataset (64 items, the tile index from the scene) NEAREST-fetched by the programme, a blend state a triangle from the five
        // integer-exact UNORM8 blends; the digest BLAKE3 over the target in row order with E, S and the programme hash. Fields: rounds (T, 1
        // to 64), program_ops (1 to 256), batch_log2 (1 to 40), target_log2 (5 to 8), dataset_mib, cache_mib, parents, item_bytes 256,
        // epoch_blocks, chain_id; no period, no anchor
        if (have_pblk || have_anchor || have_sched || have_blk || have_feat || have_keyed || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist || have_drawn_order || have_fp_mode || have_sequence || have_pair_log2 || have_kib || have_regs || have_fevery || have_lean || have_prefetch || have_chain_select || have_route) return IGNPOW_E_PARAMS;
        if (!have_mib || mib < 1024 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256)) return IGNPOW_E_PARAMS;
        const uint64_t T = have_rounds16 ? rounds : 4, words = have_pops ? pops : 32, bl = batch_log2_v, tl = have_target_log2 ? target_log2_v : 5;
        // "tiles" (the security review's Kiln rows 9154e439d, main 02:5x UK: the tile share the first lever): the scene draws `tiles` keyed tile
        // indices (1 to 64; 64 the default, 1 the light twin); a fragment's tile is derived from its own identity (the Kiln seat's form
        // 03:0x UK: a fragment has no serial number): tile = (((t << 2 tl) | (y << tl) | x) >> 4) mod tiles, so sixteen consecutive pixels
        // of a row share a tile and the fetch is issued the moment the fragment exists; the programme's operation count unchanged
        // "rows" (the project's order 03:4x UK: the streamed bytes equal the named share): each fragment fetches `rows` full 256-byte tile rows
        // (64 texels, 16 uvec4 NEAREST fetches a row) at row indices from its identity and the state, spaced tiles x 64 / rows apart, and
        // folds each into its state by the cheapest consuming fold (one xor and one add a uvec4 into a 16-byte accumulator, one mix a
        // row: about half an operation a byte); 0 = the TEX form alone (x1, the light twin)
        if (lanes != 8 || chain != 1 || epoch < 1 || T < 1 || T > 64 || words < 1 || words > 256 || bl < 1 || bl > 40 || tl < 5 || tl > 8 || tiles_v < 1 || tiles_v > 64 || (coalesced_v ? rows_v > 4096 : (rows_v > 64 || (rows_v && (tiles_v * 64) % rows_v))) || spread_v < 1 || spread_v > 127 || fragments_v > 4096 || coalesced_v > 1 || (coalesced_v && (!fragments_v || (fragments_v & 15) || rows_v < 1)) || contig_v < 1 || contig_v > 256 || (contig_v > 1 && (!coalesced_v || rows_v % contig_v))) return IGNPOW_E_PARAMS;
        // "contig" (the project's order 07:0x UK on the Kiln seat's 5090 row, 270 GB/s with every 256-byte row a random page activation): the
        // run's rows drawn as rows / contig random BASES of contig CONSECUTIVE items, row_q = base[q / contig] + (q mod contig), base =
        // fmix32(run ^ s7 ^ ((q / contig) x 0x9E3779B9)) mod (items - contig), so a run streams contig x 256 bytes contiguous a base; 1 = every row drawn
        // "coalesced" (the project's ruling 03:5x UK, closing the verifier seat's stride-collision hole and the Kiln seat's private-rows row):
        // the exact list of `fragments` passes forms runs of 16 (run = the pass index / 16, the slice = index mod 16); each run streams
        // `rows` rows (256-byte dataset items) whose indices are drawn PER ROW over the whole dataset, row_q = fmix32(run ^ s7 ^ q x
        // 0x9E3779B9) mod the item count, and each fragment folds its own 16-byte slice of every row, so one warp instruction reads
        // 512 contiguous bytes and a hash of 256 fragments at rows 256 reads 16 x 256 x 256 B = 1 MiB with the distinct bytes the named
        // bytes (the collisions negligible over 4 million rows); the tile fetches of the programme beside
        // "fragments" (the project's ruling 02:4x UK on the 4x coverage variance): every hash runs EXACTLY `fragments` programme passes: the
        // covered pixels in raster order (the triangles in order, each box row-major; a pixel covered by two triangles counts twice,
        // as it is drawn twice) first, and if fewer the list continues through the target's UNCOVERED pixels in raster order with the
        // triangle index 0 and the colour 0 until the count, and if more only the first count; 0 = the covered fragments alone
        // "spread" (the project's order 02:1x UK on the verifier seat's count of about 60 covered fragments at the first cut's +-7): the triangle
        // corners' offsets from the anchor in -spread .. +spread (7 the first cut's, 15 the wide cut's: about 512 covered fragments at T 4
        // over 32 x 32 by the reference's counter); carried as SCRATCH_BYTES
        p = ignh_params{};
        p.version = 22; p.fold_every = uint32_t(tiles_v); p.blocks = uint32_t(rows_v); p.scratch_bytes = uint32_t(spread_v); p.table_kib = uint32_t(fragments_v); p.access_bytes = uint32_t(coalesced_v) | (uint32_t(contig_v) << 8); p.dataset_bytes = mib << 20; p.rounds = uint32_t(T); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = cmib << 20; p.parents = uint32_t(parents); p.item_bytes = 256; p.registers = uint32_t(tl); p.program_ops = uint32_t(words); p.keyed_rounds = uint32_t(bl); p.period_blocks = 0; p.program_distance = 0;
        return IGNPOW_OK;
    }
    if (version == 21 || version == 24 || version == 25) {
        // VERSION 25, ANVIL FOR OLD CARDS, ENTRY 2 (namespace oldcard; the founder's word 15 October 2026 10:4x UK, the project's order): the
        // registered anvilr-k2-lean-p64 programme (fp_mode 6, M 16, 8 entries of 16 steps, 128 steps, the keyed read every second step
        // with prefetch 16, the per-pair sequence, the lean fixed cost, the period seed of the chain form) with the STEP MIX changed:
        // four steps of every five (t mod 5 != 4) are SCRATCH steps, data-dependent 8-byte reads and writes into a PER-HASH SCRATCHPAD of
        // "scratch_kib" (4, 16 or 64 KiB, bound in P as scratch_bytes) held in the card's shared memory, the arithmetic kept only to form
        // the next address and mix the value; the fifth step the shape's step as now. A scratch step, lane l on its four registers
        // r0..r3 = R[l][(4t + 0..3) mod 32]: the read address (r0 ^ r1 x 0x9E3779B9) mod S aligned to 8, every lane reads before any
        // lane writes; the value's words lo, hi mixed r2 = fchain(r2 ^ lo, t), r3 = (r3 + hi) ^ rotl(lo, 13); the write address in the
        // lane's own slice, l x S/16 + ((r2 ^ r3 x 0x85EBCA6B) mod S/16 aligned to 8), the 8 bytes LE32(r2) || LE32(r3) written in
        // lane order (the reads span the whole scratchpad, the writes are lane-partitioned, so a warp's load-then-store step is the
        // reference's order). The scratchpad is seeded from the init chain after the window and its bytes are folded into the lean mix
        // (lane l its slice) so a miner cannot skip it. Fields version 21's with scratch_kib 4, 16 or 64; chain_select, route refused.
        // VERSION 24, BASE (namespace base; the founder's "back to the start", the project's order of 15 October 2026 10:4x UK): THE CONTROL OF RECORD,
        // version 21's lane array, classes, keyed read, prefetch, window and lean mix with NO PER-HASH DRAW: the period seed draws the 16
        // shapes and their FIXED ORDER (a permutation by fchain over the period seed's word 7), entry e runs shape order[e] for 16 steps,
        // the slot immediates, predicates and window offsets are the shape's (the period's), and the per-hash difference is the inputs
        // alone (the init state from the work seed, the keyed items read at the state's indices). Fields as version 21's with fp_mode 6,
        // lean 1, sequence = rounds / 16; pair_log2 (two attempts a warp, the port's arrangement) carried in the slot with no effect on the
        // hash; chain_select, route, prefetch 0, select-all and (f) refused. Every increment of the day's sweeps is one field from it.
        // VERSION 21, FORGE2, the two-level form (FORM-FORGE.md section 7): version 20's shapes, generator, quota, mask and scratchpad;
        // per PERIOD (the nonce batch of 2^batch_log2 attempts under one header: period_seed = BLAKE3(Forge/period || E || header || LE64
        // (nonce >> batch_log2))) M = program_ops drawn shapes (the class order drawn per shape, the operands static); per HASH a drawn
        // SEQUENCE of L = "sequence" entries over the M shapes, each entry run rounds / L iterations, with the hash's own scratchpad
        // address and predicate words; the digest folding the period seed, the sequence bytes and the state. The miner compiles the
        // period's shapes as one kernel on the host (the GPU team's port); the verifier derives them per batch.
        // THE CHAIN FORM (F-141, the pool's stale-share fault: a seed over the header forces a recompile every block; the consensus chair's
        // five conditions 0a3d914a5, the project's ruling 05:2x UK 15 October 2026): "period_blocks" (64 to epoch_blocks) names a PERIOD OF BLOCKS;
        // period_seed = BLAKE3(Forge/period || E || LE64(chain_id) || LE64(height / period_blocks) || anchor), the anchor the hash of the
        // block at finality depth 64 below the period's start (zero before 64; the block AT the start refused: a two-deep reorganisation
        // would flip the programme), a runtime input through the anchored call, the document's "program_anchor" the test anchor (zeros);
        // period_blocks bound in P at byte 72, batch_log2 refused (unused: the programme keys on the period, not the nonce batch); a
        // share's period from its template's height; the card compiles period n + 1 during period n; the per-hash draw unchanged.
        const bool chain_form = have_pblk;
        if (chain_form && (have_batch_log2 || pblk < 64 || pblk > epoch)) return IGNPOW_E_PARAMS;
        if (!chain_form && have_anchor) return IGNPOW_E_PARAMS;
        // F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 2): the bond-keyed item-read steps on 21 and 25 under the chain form alone:
        // "features" bit 10 and nothing else, "keyed_rounds" S in 1..R with R the walk's item reads (rounds / fold_every, the read at
        // step t = fe x r), each mandatory with the other; version 24 (the control) never keyed
        // F-580 (17 October 2026): "features": 0 is the field absent (the node's unkeyed twin of a keyed document carries the
        // cleared field as 0; the record writes +60 as 0 either way), so the twin builds; any other value than the bit alone refused
        if (have_feat && feat == 0) have_feat = false;
        if (have_feat && (version == 24 || feat != 1024 || !chain_form)) return IGNPOW_E_PARAMS;
        if (have_sched || have_blk || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist || have_target_log2 || have_drawn_order) return IGNPOW_E_PARAMS;
        if (!have_mib || mib < 1024 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256)) return IGNPOW_E_PARAMS;
        const uint64_t steps = have_rounds16 ? rounds : 512, shapes = have_pops ? pops : 16, kib21 = have_kib ? kib : 16, fe = have_fevery ? fevery : 64, L = sequence_v, bl = batch_log2_v;
        // "fp_mode" (the GPU team's issued-per-useful read 00:26 UK: the exponent mask per FP32 operand is the form's floor at 8 to 9): 0 = as cut
        // (the mask on every FP operand and producer); 1 = INTEGER-ONLY (the FP32 classes removed from the quota, their slots to the integer
        // ten, the shuffle and the scratchpad classes, no mask anywhere); 2 = PRODUCER-MASKED (the FP32 slots read and write only the FP bank
        // r24..r31, masked once at the producer; the integer slots never touch the bank; I2F reads an integer register and writes the
        // bank, F2I reads the bank and writes an integer register); bits 1 and 2 of the slot
        // 3 = THE OCCUPANCY FORM (the GPU team's 00:33 read: 1,200 pJ a useful op, latency-bound at 16 KiB an attempt): integer-only inside it, the
        // scratchpad 1 KiB an attempt (64 bytes a lane, scratch_kib 1), the slot wiring for instruction-level parallelism (slot k writes
        // k and reads (k + 21) mod 32 and (k + 9) mod 32: a within-step dependency no nearer than 11 slots), the state under 3 KiB an attempt
        // 4 = (c2) THE SHALLOW STEP: the occupancy form with the wiring a = k + 24, b = k + 16 mod 32 (every slot reads only slots at least 8
        // back within the step: a depth of at most 4 a step, 8 slots overlapped); 5 = (d) THE 16-REGISTER LANE: the shallow step over 16
        // registers a lane (d = k mod 16, a = k + 11, b = k + 7 mod 16: the within-step distance at least 5), the state halved
        // (g) ANVIL-K is the occupancy form (fp_mode 3) with fold_every 8 (the keyed read's share about half the card's hash: KawPoW's shape
        // over the keyed epoch set); the document names it, the slot carries no new bit (the fp_mode field is three bits)
        // 6 = (e) ANVIL-R: the per-lane scratchpad replaced by a ROTATING REGISTER WINDOW of 16 words a lane whose indices are static per
        // shape at compile time (the window's base is the step's index within the entry, the entry unrolled: no shared-memory traffic in
        // the step); the sequence over the shapes, the keyed read, the integer-only quota and the ILP wiring as (c). 7 = (f) KAWPOW-PLUS: a
        // static period programme of program_ops words run once each in order (no shape reuse, rounds = program_ops, the sequence fixed),
        // register-resident (no scratchpad, no window: the loads read the register), compiled per batch; the per-hash variation the keyed
        // reads into the dataset (the index from the state) and the predicates, which are UNIFORM ACROSS A WARP (the cmov bits from
        // BLAKE3(Forge/warp || E || header || LE64 (nonce >> 5))), so the per-hash part costs the card nothing
        const bool occ = fp_mode_v >= 3;
        const uint32_t mix_bits = (version == 25 && mix_v) ? (1u << 20) : 0u;  // the drawer's second step mix
        if (version == 25 && (fp_mode_v != 6 || lean_v != 1 || chain_select_v || route_v || steps != 128 || shapes != 16 || L != 8 || fe != 2 || prefetch_v != 16 || pair_log2_v != 1 || (kib21 != 1 && kib21 != 2 && kib21 != 4 && kib21 != 16 && kib21 != 64))) return IGNPOW_E_PARAMS;  // 1 and 2 KiB: the occupancy sizes (the founder's 'fix it', 15 October 13:4x UK: a Turing SM holds 64 and 32 hashes)
        if (lanes != 16 || chain != 1 || epoch < 1 || (have_regs && regs != (fp_mode_v == 5 ? 16u : 32u)) || (version != 25 && (occ ? kib21 != 1 : kib21 != 16)) || steps < 64 || steps > 65536 || shapes < 1 || shapes > 256 || L < 1 || L > 1024 || (steps % L) || fe < 1 || fe > steps || (steps % fe) || bl < 1 || bl > 40 || fp_mode_v > 9 || fp_mode_v == 8 || pair_log2_v > 15 || (!chain_form && pair_log2_v >= bl)) return IGNPOW_E_PARAMS;
        if (fp_mode_v == 6 && (steps / L) != 16) return IGNPOW_E_PARAMS;  // the window's base is the step within a 16-step entry
        // "prefetch" (the project's form note 02:3x UK from the GPU team's keyed-read rows: inside the kernel the read is a dependent stall at 420 to 550
        // nJ against 47 to 62 nJ at the wall): the keyed read's item index is the designated lane's r0 as it stood K steps BEFORE the step
        // that consumes the item (K = 16 the document's lever), so a card issues the load early and hides the stall; K = 0 the current
        // state; bits 8 to 14 of the slot
        if (prefetch_v > 64 || lean_v > 1) return IGNPOW_E_PARAMS;
        // "lean" (the project's order 05:0x UK: the fixed half, the init chain, the mix and the digest, lifts the fabric's ratio on the 128-step
        // form): 1 = THE LEAN FIXED COST: the init one BLAKE3 over the seed material expanded by the fold (as cut: no hashing in the
        // chain) and the MIX DIGEST OVER THE LANE STATE ALONE: each lane's 32 registers and its window or scratchpad words folded into a
        // 16-byte accumulator (acc[k] = (acc[k] ^ w) + w over the lane's words, the cheapest fold), the digest BLAKE3 over E, the period
        // seed, the sequence bytes and the 16 accumulators (256 bytes) instead of the 3 KiB state; bit 15 of the slot
        if (fp_mode_v == 7 && (steps != shapes || L != shapes || pair_log2_v)) return IGNPOW_E_PARAMS;  // one step a word, every word once, in order
        // 9 = SELECT-ALL, THE MEASUREMENT FORM (the founder's question 15 October 2026 08:3x UK, a whole new programme every hash on a card):
        // the step's operation drawn PER HASH PER STEP from EIGHT candidate operations (add, sub, xor, and/or, rotate, mul, mulhi, cross-lane),
        // every lane computing all eight on its operands and SELECTING the drawn one by a mask with no branch; the operands and the
        // destination the static wiring of version 20, the immediates per step from the hash's seed chain; no period programme (the shapes
        // unused, the sequence a single entry), no scratchpad and no window (the state 32 registers a lane), no pair sequence (the
        // programme is the hash's own); the keyed read, the prefetch and the lean mix as the document says; fp_mode's fourth bit at bit 16
        if (fp_mode_v == 9 && (pair_log2_v || shapes != 1 || L != 1)) return IGNPOW_E_PARAMS;
        // "chain_select" (the project's order 15 October 2026 08:3x UK, the measurement sweep over the entry length): 1 = the NEXT ENTRY'S SHAPE INDEX
        // and the KEYED ITEM'S INDEX are drawn from the previous entry's final lane state (x = fchain(fold over the lanes of (x ^ r0) + r1,
        // 1 + e) at the end of entry e; the next entry's shape x mod M, the reads inside it at fchain(x, 8192 + t) mod items) rather than
        // from the seed-drawn sequence; the first entry's shape from the seed's sequence and its reads from the seed's word; the drawn
        // sequence is then the hash's own dependent chain (the card learns entry e + 1 only at the end of entry e: the lookahead is the
        // entry length, so prefetch and the pair sequence are refused beside it, as are (f) and select-all); bit 17 of the slot
        if (chain_select_v > 1 || (chain_select_v && (prefetch_v || pair_log2_v || fp_mode_v == 7 || fp_mode_v == 9))) return IGNPOW_E_PARAMS;
        // "route" (the project's order 15 October 2026 09:0x UK, the per-hash switching network): the SHARE of each step's operands whose source
        // lane is drawn per hash from the state, 0, 25, 50 or 100 percent (the slots k with k mod 4 < share / 25 read their first operand
        // from a drawn lane): the step's route word is the previous step's result (x = fchain(fold over the lanes of (x ^ r0) + r1, t) at the
        // end of step t - 1; the init's word for step 0), slot k's draw sh = fchain(x, k) for lanes 0..7 and fchain(x, 32 + k) for 8..15,
        // the source lane (l + 1 + the lane's nibble of sh) mod 16, so the graph cannot be prepared ahead of the step; a cross-lane read
        // in the trace; bits 18 and 19 of the slot (0, 1, 2, 3 = 0, 25, 50, 100 percent)
        if (have_route && route_v != 0 && route_v != 25 && route_v != 50 && route_v != 100) return IGNPOW_E_PARAMS;
        if (version == 24 && (fp_mode_v != 6 || lean_v != 1 || chain_select_v || route_v || steps != 128 || shapes != 16 || L != 8 || fe != 2 || prefetch_v != 16)) return IGNPOW_E_PARAMS;
        if (route_v && fp_mode_v == 9) return IGNPOW_E_PARAMS;
        const uint32_t route_bits = route_v == 25 ? 1u : route_v == 50 ? 2u : route_v == 100 ? 3u : 0u;
        // (c3) "pair_log2" (the security review's row a1b1e306f: the lever is the entry length; at one step an entry the fabric's reconfiguration is
        // half its time): the SEQUENCE drawn per warp pair, seq_seed = BLAKE3(Forge/sequence || E || header || LE64 (nonce >> pair_log2)),
        // shared by the 2^pair_log2 attempts of a pair (the adversary chair's cure for the divergent switch); the hash's addresses and
        // predicates stay per hash; 0 = the sequence from the hash's own seed; bits 4 to 7 of the slot
        p = ignh_params{};
        p.version = uint32_t(version); p.dataset_bytes = mib << 20; p.scratch_bytes = uint32_t(kib21 << 10); p.rounds = uint32_t(steps); p.lanes = 16; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = cmib << 20; p.parents = uint32_t(parents); p.item_bytes = 256; p.registers = fp_mode_v == 5 ? 16u : 32u; p.program_ops = uint32_t(shapes); p.blocks = uint32_t(L); p.keyed_rounds = chain_form ? 0u : uint32_t(bl); p.period_blocks = chain_form ? pblk : 0; p.fold_every = uint32_t(fe); p.program_distance = (uint32_t(fp_mode_v & 7) << 1) | (uint32_t(pair_log2_v) << 4) | (uint32_t(prefetch_v) << 8) | (uint32_t(lean_v) << 15) | (uint32_t(fp_mode_v >> 3) << 16) | (uint32_t(chain_select_v) << 17) | (route_bits << 18) | mix_bits;
        if (chain_form) std::memcpy(p.program_anchor, anchor, 32);
        {
            const uint64_t reads = (steps + fe - 1) / fe;
            if ((have_feat && (feat & 1024)) ? (!have_keyed || keyed < 1 || keyed > reads) : have_keyed) return IGNPOW_E_PARAMS;
            p.features = have_feat ? uint32_t(feat) : 0u;
            p.bond_rounds = (p.features & 1024) ? uint32_t(keyed) : 0u;
        }
        return IGNPOW_OK;
    }
    if (version == 20) {
        // VERSION 20, FORGE (FORM-FORGE.md; the founder's order of 14 October 2026 22:3x UK; the review's reading 8 ad6f5819d): the per-hash
        // programme on the lane array: 32 wide instruction words a hash from the work seed and the epoch seed, each a fused 16-lane x
        // 32-register step run 32 iterations (1,024 steps), the slot classes by the quota table (the fixed order, or DRAWN per hash per
        // word under "drawn_order" 1: the second document of record), the register operands STATIC per slot, the scratchpad 1 KiB a lane,
        // the keyed 256-byte read of the epoch dataset every fold_every steps, the FP32-RN class under the exponent mask. Fields: rounds
        // (the steps, 1024), program_ops (the words, 32), lanes 16, registers 32, scratch_kib 16 (1 KiB a lane), dataset_mib, cache_mib,
        // parents, item_bytes 256 (the keyed read's table), fold_every (64), epoch_blocks, chain_id, drawn_order (0 or 1); period_blocks
        // and program_anchor REFUSED (no period). The quota table, the fold and the mask constants are consensus constants carried by the vectors.
        if (have_pblk || have_anchor || have_sched || have_blk || have_feat || have_keyed || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist || have_target_log2 || have_chain_select || have_route) return IGNPOW_E_PARAMS;
        if (!have_mib || mib < 1024 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256)) return IGNPOW_E_PARAMS;
        const uint64_t steps = have_rounds16 ? rounds : 1024, words = have_pops ? pops : 32, kib20 = have_kib ? kib : 16, fe = have_fevery ? fevery : 64;
        if (lanes != 16 || chain != 1 || epoch < 1 || (have_regs && regs != 32) || kib20 != 16 || steps < 64 || steps > 65536 || words < 1 || words > 1024 || (steps % words) || fe < 1 || fe > steps || (steps % fe) || drawn_order_v > 1) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 20; p.dataset_bytes = mib << 20; p.scratch_bytes = uint32_t(kib20 << 10); p.rounds = uint32_t(steps); p.lanes = 16; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = cmib << 20; p.parents = uint32_t(parents); p.item_bytes = 256; p.registers = 32; p.program_ops = uint32_t(words); p.period_blocks = 0; p.fold_every = uint32_t(fe); p.program_distance = uint32_t(drawn_order_v);
        return IGNPOW_OK;
    }
    if (version == 19) {
        // VERSION 19, THE RASTER FORM THAT DRAWS CONTINUOUSLY (docs/gpu-b/RASTER-V19-DESIGN.md, the project's order of 18 October 2026 02:5x UK):
        // the seeds, the key, the triangle chain and the final digest are 18c's; three rules change so that nothing in an attempt depends
        // on a previous command: R1 every triangle's box is computed from the keyed base ALONE (two boxes that overlap on the surface never
        // see each other), R2 the surface is never written (no restore), R3 the attempt is batch-addressed (any set of attempts in any
        // order in one command); and R4 the box fold: the mix's input is one 64-bit accumulator a box in place of the box bytes.
        // Fields: rounds (the triangles t: 256 or 1024), reach (the vertex offset's range: 15 = boxes to 31 px, 31 = boxes to 63 px),
        // target_log2 (14 = a 16384 x 16384 RGBA8 base of 1 GiB, 15 = 32768, 4 GiB), epoch_blocks, period_blocks, chain_id,
        // program_anchor; the keyed base = the epoch dataset's bytes row-major as the surface (dataset_mib = 4 << (2 x target_log2 - 20),
        // cache_mib, parents, item_bytes 256); without the dataset fields the lab form over a zero base (the sets on a card without the
        // gigabyte). No bond-keyed form (features, keyed_rounds) at this cut; the drawer's second rule (draw) is not carried.
        const bool keyed_base = have_mib;
        if (have_feat || have_keyed || have_draw) return IGNPOW_E_PARAMS;
        if (have_kib || have_sched || have_blk || have_regs || have_pops || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist) return IGNPOW_E_PARAMS;
        if (!keyed_base && (have_cmib || have_parents || have_ib)) return IGNPOW_E_PARAMS;
        if (lanes != 8 || chain != 1 || epoch < 1) return IGNPOW_E_PARAMS;
        const uint64_t tris = have_rounds16 ? rounds : 256, pblk19 = have_pblk ? pblk : 300, tlog = have_target_log2 ? target_log2_v : 14, reach = have_reach ? reach_v : 15;
        if ((tris != 256 && tris != 1024) || pblk19 < 1 || pblk19 > epoch || (tlog != 14 && tlog != 15) || (reach != 15 && reach != 31)) return IGNPOW_E_PARAMS;
        if (keyed_base && ((mib << 20) != (uint64_t(4) << (2 * tlog)) || cmib < 1 || cmib > 4096 || parents < 1 || parents > 4096 || (have_ib && ib != 256))) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 19; p.dataset_bytes = keyed_base ? (mib << 20) : 0; p.rounds = uint32_t(tris); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = keyed_base ? (cmib << 20) : 0; p.parents = keyed_base ? uint32_t(parents) : 0; p.item_bytes = keyed_base ? 256 : 4; p.registers = uint32_t(tlog); p.program_ops = uint32_t(tris); p.period_blocks = pblk19;
        p.scratch_bytes = uint32_t(reach);  // the reach rides SCRATCH_BYTES (byte 12), a field every record carries and 18 leaves zero
        std::memcpy(p.program_anchor, anchor, 32);
        return IGNPOW_OK;
    }
    if (version == 18) {
        // VERSION 18, THE RASTER FORM (the founder's order of 14 October 2026 17:3x UK, the seventh discipline on the raster probe's exact
        // rule, pow/workers/common/raster_probe.h): an 8192 x 8192 RGBA8 framebuffer (256 MiB, zero at the attempt's start), "rounds"
        // small triangles (1024 registered) with integer vertex coordinates from the lane's state, drawn under the standard coverage
        // rule (the top-left rule, y down, no multisampling, no depth), integer additive blending saturating at 255, then the
        // triangles' bounding boxes digested in order. The key as version 16's. Fields: epoch_blocks, period_blocks, rounds, chain_id, program_anchor
        // 18b (the project's recut on F-129, 14 October 20:4x UK): "target_log2" 14 = a 16384 x 16384 RGBA8 target (1 GiB), the coordinates
        // 14 bits each from the state word; 13 (or absent) the 8192 form; carried as REGISTERS in the canonical bytes
        // 18c (the panel's cure on 18b, 14 October 20:47 UK): the target PRE-FILLED with keyed epoch data (the dataset fields as version
        // 15's; dataset_bytes = side^2 x 4, so 1024 MiB at target_log2 14), every covered pixel a read-modify-write against the target's
        // keyed content, the digest over the touched pixels' results
        const bool keyed_target = have_mib;
        // F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 3): the bond-keyed triangles: "features" bit 10 and nothing else, "keyed_rounds"
        // S in 1..ROUNDS (the triangles), each mandatory with the other; the count rides byte 64 (KEYED_ROUNDS, the branch's own slot)
        if (have_feat && feat == 0) have_feat = false;  // F-580: "features": 0 is the field absent (the twin's form)
        if (have_feat && feat != 1024) return IGNPOW_E_PARAMS;
        if ((have_feat && (feat & 1024)) ? (!have_keyed || keyed < 1 || keyed > (have_rounds16 ? rounds : 1024)) : have_keyed) return IGNPOW_E_PARAMS;
        if (have_kib || have_sched || have_blk || have_regs || have_pops || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist) return IGNPOW_E_PARAMS;
        if (!keyed_target && (have_cmib || have_parents || have_ib)) return IGNPOW_E_PARAMS;
        if (lanes != 8 || chain != 1 || epoch < 1) return IGNPOW_E_PARAMS;
        const uint64_t tris = have_rounds16 ? rounds : 1024, pblk18 = have_pblk ? pblk : 300, tlog = have_target_log2 ? target_log2_v : 13;
        if (tris < 64 || tris > 65536 || (tris & 7) || pblk18 < 1 || pblk18 > epoch || (tlog != 13 && tlog != 14)) return IGNPOW_E_PARAMS;
        if (keyed_target && ((mib << 20) != (uint64_t(4) << (2 * tlog)) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256))) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 18; p.dataset_bytes = keyed_target ? (mib << 20) : 0; p.rounds = uint32_t(tris); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = keyed_target ? (cmib << 20) : 0; p.parents = keyed_target ? uint32_t(parents) : 0; p.item_bytes = keyed_target ? 256 : 4; p.registers = tlog == 14 ? 14u : 0u; p.program_ops = uint32_t(tris); p.period_blocks = pblk18; p.program_distance = draw_v ? 2u : 0u;  // the drawer's second drawing rule (bit 1; bit 0 the slow-oracle bit, never set by the parser)
        std::memcpy(p.program_anchor, anchor, 32);
        p.features = have_feat ? uint32_t(feat) : 0u;
        p.keyed_rounds = p.bond_rounds = (p.features & 1024) ? uint32_t(keyed) : 0u;
        return IGNPOW_OK;
    }
    if (version == 17) {
        // VERSION 17, THE TEXTURE FORM (the founder's order of 14 October 2026 17:3x UK, the sixth discipline on the probe's exact rule,
        // docs/lab/TEXTURE-DISCIPLINE.md): a 4096 x 4096 texture of one unsigned 32-bit channel (R32, 64 MiB, no mips), texel (x, y) =
        // fmix32(y * 4096 + x) (murmur3's finaliser), sampled NEAREST at unnormalised texel centres through the texture unit; the
        // attempt's work "rounds" samples (512 registered) in eight dependent lanes, each sample's coordinates from its lane's state and
        // the sampled value folded into the state; the digest BLAKE3 over the samples in order, as the probe hashes them. The key as
        // version 16's (chain_id, period, anchor). Fields: epoch_blocks, period_blocks (300), rounds (64 to 65536), chain_id, program_anchor
        // 17b (the project's recut on F-128, 14 October 20:4x UK): the texture is KEYED DATA, the bandwidth lane's epoch dataset (dataset_mib,
        // cache_mib, parents, item_bytes 256 as version 15's; 1024 MiB or more, a power of two, so the texture is a square of R32 texels
        // of side sqrt(bytes / 4): 16384 at 1 GiB, 32768 at 4 GiB), sampled NEAREST through the texture unit; a chip must hold the set in DRAM
        const bool keyed_tex = have_mib;
        if (have_kib || have_sched || have_blk || have_regs || have_feat || have_keyed || have_pops || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout || have_rev || have_variant || have_read_every || have_pdist || have_target_log2) return IGNPOW_E_PARAMS;
        if (!keyed_tex && (have_cmib || have_parents || have_ib)) return IGNPOW_E_PARAMS;
        if (keyed_tex && (mib < 1024 || mib > 16384 || !is_pow2(mib) || (mib & (mib - 1)) || ((mib / 1024) & ((mib / 1024) - 1)) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256))) return IGNPOW_E_PARAMS;
        if (keyed_tex && (mib != 1024 && mib != 4096 && mib != 16384)) return IGNPOW_E_PARAMS;  // the square's side a power of two: 16384, 32768, 65536
        if (lanes != 8 || chain != 1 || epoch < 1) return IGNPOW_E_PARAMS;
        const uint64_t samples = have_rounds16 ? rounds : 512, pblk17 = have_pblk ? pblk : 300;
        if (samples < 64 || samples > 65536 || (samples & 7) || pblk17 < 1 || pblk17 > epoch) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 17; p.dataset_bytes = keyed_tex ? (mib << 20) : 0; p.rounds = uint32_t(samples); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = keyed_tex ? (cmib << 20) : 0; p.parents = keyed_tex ? uint32_t(parents) : 0; p.item_bytes = keyed_tex ? 256 : 4; p.registers = 0; p.program_ops = uint32_t(samples); p.period_blocks = pblk17; p.program_distance = 0;
        std::memcpy(p.program_anchor, anchor, 32);
        return IGNPOW_OK;
    }
    if (version == 16) {
        // VERSION 16 (the founder's order of 13 October 2026 15:0x UK, the STATIC-FABRIC integer coordinate, C-36): the keyed per-period
        // form as version 15's; no dataset, no tile, no shuffle, no shared memory; one thread an attempt over a STATIC live set of 224
        // registers (four banks of 56, static indices only); the period's programme of rounds x 224 operations, each round writing
        // every register once and reading every register once as its first source (the draw's liveness guarantee, k = 56 within a
        // bank); fields: epoch_blocks, period_blocks (300), rounds (the passes, 4 to 256; 16 registered, 128 the fabric row), chain_id, program_anchor
        // REVISION 3 (the founder's order of 17:1x UK): "tails" 1 = the init and the mix at one BLAKE3 compression each through the R0E fold
        // (registered); 0 = revision 2's BLAKE3 tails (the r128 and r16 sets of revision 2); carried as PROGRAM_DISTANCE's byte slot
        const bool rev19 = have_rev && (rev_v >= 25 && rev_v <= 28);  // 28 = 19d, 19b's batches at 256 bytes a read, a quarter as many  // 26 = 19b, the reads in batches of eight; 27 = 19c, 19b's reads on the in-place stream (15c)  // the project's revision 19 (memory fused); 19 in the parser's numbering is the warp fabric
        if ((!rev19 && (have_mib || have_cmib || have_parents || have_ib)) || !rev19 == have_read_every) return IGNPOW_E_PARAMS;
        if (have_kib || have_sched || have_blk || have_feat || have_keyed || have_pops || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout) return IGNPOW_E_PARAMS;
        if (lanes != 8 || chain != 1 || epoch < 1) return IGNPOW_E_PARAMS;
        const uint64_t passes = have_rounds16 ? rounds : 128, pblk16 = have_pblk ? pblk : 300, tails = have_tails ? tails_v : 0;  // tails absent = revision 2's form (the records of record); revision 3's documents carry tails 1
        // "weights" (the project's order of 20:3x UK, the "rev 3 light" variant for measurement only, not adopted): 0 = cand-14's table (registered);
        // 1 = the table shifted off MAD toward the lane-cheap classes (ADD 45, SUB 45, XOR 60, ROTL 37, ROTR 37, MAD 32 of 256); carried
        // as bit 1 of PROGRAM_DISTANCE's slot beside tails
        const uint64_t weights = have_weights ? weights_v : 0;
        // "revision" 4 (the design seat's reading of 20:4x UK on MOSAIC-VM section 26: 34 percent of single-bit flips absorbed, the
        // avalanche saturating at 7 percent of the state, the banks never mixing before the fold): MAD as a x (b | 1) + imm (a bijection
        // in a for a fixed b, no difference lost) and one cross-bank XOR per bank at the end of every pass (four operations a pass,
        // static indices from the pass number); bit 2 of the slot; "revision" absent or 3 = revision 3's form
        const uint64_t rev = have_rev ? rev_v : 3;
        // "registers" (revision 13, the project's order of 22:1x UK): the static file's size, four banks of registers / 4; 224 the form of
        // revisions 3 to 12, 144 (banks of 36) the registered file from revision 13, 160 (banks of 40) 12b; 96 to 224 in steps of 16
        const uint64_t nregs = have_regs ? regs : 224;
        if (nregs < 96 || nregs > 224 || nregs % 16 != 0) return IGNPOW_E_PARAMS;
        // "revision" 5 (the design seat's reading of 20:5x UK on revision 4, FAIL on both marks: a register written before it is read in a
        // pass loses its old value, and (b | 1) discards b's bit 0): every operation consumes its destination's old value bijectively,
        // d = d_old + rotl(g(a, b, imm), (imm >> 5) & 31) with g revision 4's result; the read permutation a derangement of the write
        // (pa[i] = pd[(i + 1) mod 56]); b stepped off the destination when it equals it; the cross-bank step stays d = d ^ b; bit 3 of the slot
        // "revision" 6 (the project's order of 21:1x UK on the GPU team's 4090 row of revision 5, 2.071 uJ against revision 3's 0.985: the accumulate's
        // read-before-write two dependent instructions an operation): the bijection of the destination as ONE fused three-input
        // instruction per class: ADD d = d_old + a + b + imm, SUB d = d_old + a - b - imm, XOR d = d_old ^ a ^ b ^ imm, ROTL d = d_old +
        // (rotl(a, imm) ^ b), ROTR d = d_old + (rotr(a, imm) ^ b), MAD d = d_old + a x (b | 1) + imm; the draw revision 5's; bit 4 of the
        // slot. "revision" 7 = 6b: the accumulate on the odd passes only, the even passes revision 4's plain write; bit 5
        // "revision" 8 = the design seat's form A (PASS on its marks at 21:07 UK): d = d_old ^ g for every class, g revision 4's result,
        // no rotate; the draw revision 5's; bit 6 of the slot
        // "revision" 9 (the project's order of 21:4x UK on revisions 5 and 8's register pressure: the accumulate's read of d_old is the term):
        // revision 4's plain one-op-per-step forms (the odd MAD, the four cross-bank XORs a pass) with the SCHEDULE fixed: within a
        // bank, the operation at position i reads as its first source the register the NEXT operation writes (pa[i] = pd[i + 1]; the
        // last position reads the next pass's first write), so every register's value is read exactly once before its next overwrite
        // and nothing is absorbed; b never the destination; no extra instruction; bit 7 of the slot
        // "revision" 10 (the design seat's reading of 21:5x UK on revision 9, FAIL at 2 of 256: one operation in 54 draws b == a and its
        // read is lossy): revision 9 with b stepped off BOTH the destination and the first source, so every operation is a bijection in a
        // and the programme a permutation under the plain write; bit 8 of the slot (the slot a be32)
        // "revision" 11 (the project's order of 22:0x UK on revision 9's 4090 row, 1.309 uJ at 255 registers: the end-of-pass XORs make every
        // value live at one point, and the schedule chains the pass): revision 4's one-op forms (the odd MAD) WITHOUT the end-of-pass
        // XORs; the cross-bank mixing inside the operations, one in four (position i with i mod 4 = 3 in its bank) reading b from another
        // bank; the read-before-write schedule within each bank (op i reads what op i + 1 of the bank writes, the last the next pass's
        // first write of the bank); b never a and never d; every register written once and read once a pass; bit 9 of the slot
        // "revision" 12 (the design seat's reading of 22:0x UK on revision 11: the rate met at 50 percent by pass 13, 2 of 256 absorbed in
        // the four registers pass 0 writes first, their init values overwritten unread): revision 11 with pass 0's position-0 operation
        // of each bank consuming its destination, d = d_old ^ g, four operations of 28,672; bit 10 of the slot
        // "revision" 13 (the project's contingency levers of 22:2x UK, free on the cards and dear on a chip): add, sub and xor take THREE register
        // operands (one IADD3 or LOP3, a third file read on the chip), the rotate counts are the low five bits of a third register (one
        // funnel shift on the card, a barrel shifter per lane on the chip); c off a, b and d; the odd MAD with its immediate, the
        // schedule and the pass-0 consume as revision 12's; bit 11 of the slot beside revision 12's bits
        // "variant" (revision 14's measurement variants on revision 12's base, the founder's order of 23:2x UK): 1 = 14a, the class of
        // one operation in four chosen by two bits of a third register among add, xor, rotl, rotr; 2 = 14b, the same on every
        // operation; 3 = 14c, the immediates of one operation in four from a per-nonce table of 64 words; 4 = 14d, a byte permute
        // (PRMT) with the selector from a third register on one operation in four; bits 12 and 13 of the slot. 14e is the document
        // with period_blocks 1.
        const uint64_t variant = have_variant ? variant_v : 0;
        // "revision" 15 (the project's order of 23:4x UK, THE STREAMING FABRIC): the programme one stream of rounds x W operations (W = registers
        // / 4, 36 at 144; "rounds" the blocks, 800 registered = 28,800), every value read exactly once as a first source by an operation
        // of the NEXT block (a drawn permutation within its lane), b from the previous block too (in-lane for three of four positions,
        // cross-lane for one), each result written to the ring register j mod registers (a dead value), so the live set is about 2W;
        // the state at the end the ring's registers; bit 15 of the slot (bits 12 to 14 are the variant). "revision" 16 = 15b, THE WIDE VOCABULARY: ten classes drawn
        // evenly, every one a single native instruction bijective in a (add, sub, xor, rotl, rotr, the odd MAD, mulhi-add, lop3 with a
        // per-op table, the byte permute of b and c carried by a, shift-and-add); bits 15 and 16. Variant 6 = 14d3 (the design seat's reading 23:48 UK): on the permute positions d = a ^ prmt(b, c, imm), the first source carried whole
        if (passes < 4 || passes > 1024 || pblk16 < 1 || pblk16 > epoch || tails > 1 || weights > 1 || rev < 3 || rev > 28 || variant > 6 || (rev19 && (!have_mib || mib < 2048 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || (have_ib && ib != 256) || (read_every_v != 8 && read_every_v != 16 && read_every_v != 32))) || (variant && rev != 12) || (have_pdist && pdist != 0)) return IGNPOW_E_PARAMS;  // 4 to 1024 passes; 128 at 224 registers, 200 at 144, 800 blocks for the stream
        p = ignh_params{};
        p.version = 16; p.dataset_bytes = rev19 ? (mib << 20) : 0; p.rounds = uint32_t(passes); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = rev19 ? (cmib << 20) : 0; p.parents = rev19 ? uint32_t(parents) : 0; p.item_bytes = 256; p.registers = nregs == 224 ? 0u : uint32_t(nregs); p.program_ops = rev >= 15 ? uint32_t(passes) * uint32_t(nregs / 4) : uint32_t(passes) * uint32_t(nregs); p.period_blocks = pblk16; p.program_distance = uint32_t(tails | (weights << 1) | (rev == 4 ? 4u : 0u) | (rev == 5 ? 8u : 0u) | (rev == 6 ? 16u : 0u) | (rev == 7 ? 32u : 0u) | (rev == 8 ? 64u : 0u) | (rev == 9 ? 128u : 0u) | (rev == 10 ? 256u : 0u) | (rev == 11 ? 512u : 0u) | (rev == 12 ? 1536u : 0u) | (rev == 13 ? 3584u : 0u) | (uint32_t(variant) << 12) | (rev == 15 ? 32768u : 0u) | (rev == 16 ? 98304u : 0u) | (rev == 17 ? 163840u : 0u) | (rev == 18 ? 229376u : 0u) | (rev == 19 ? 262144u : 0u) | (rev == 20 ? 524288u : 0u) | (rev == 21 ? 1572864u : 0u) | (rev == 22 ? 2621440u : 0u) | (rev == 23 ? 786432u : 0u) | (rev == 24 ? 4456448u : 0u) | (rev == 26 || rev == 27 || rev == 28 ? 67108864u : 0u) | (rev == 27 ? 131072u : 0u) | (rev == 28 ? 134217728u : 0u) | (rev19 ? (32768u | 8388608u | ((read_every_v == 8 ? 0u : read_every_v == 16 ? 1u : 2u) << 24)) : 0u));  // 19 = revision 15's stream with memory fused (bit 23; bits 24 and 25 the read cadence: 0 = every 8, 1 = 16, 2 = 32)  // 24 = 16b, the warp fabric with the shuffle at every second position (bit 22; the design seat's cut 01:1x UK)  // bits 18 and 19 the form: 1 = revision 16 (the warp fabric, parser 19), 2 = revision 17 (the braided stream; bits 20 and 21 the sub-stream count: 0 = 4, 1 = 8, 2 = 16; parser 20 to 22), 3 = revision 18 (the segment pool, parser 23)  // 17 = 15c (15 in place), 18 = 15d (15b in place) (bit 17: the design seat's reading 00:1x UK)  // the 224 form keeps REGISTERS zero in the canonical bytes (the records of revisions 3 to 12 stand)
        std::memcpy(p.program_anchor, anchor, 32);
        return IGNPOW_OK;
    }
    if (version == 15) {
        // VERSION 15 (the founder's order of 13 October 2026 14:17 UK, the bandwidth-first class, C-35; the rotation that held the
        // number is WITHDRAWN, superseded 13 Oct 14:17 UK, never ported, its code behind kRotationVersion): the
        // keyed memory-bound class as the governing work. Fields: dataset_mib (a power of two, 2048 to 16384; 4096 registered for the
        // 16 GB cohort), cache_mib, parents, item_bytes (256), epoch_blocks, period_blocks (the key period, 1 to EPOCH_BLOCKS), rounds
        // (the READS per attempt: 64 to 1024, 256 registered), program_ops (the drawn programme's operations between reads: 16 to 256,
        // 64 registered), chain_id, program_anchor (the key period's anchor, a runtime input); no other key
        if ((have_kib && kib != 0) || lanes != 8 || chain != 1) return IGNPOW_E_PARAMS;
        if (have_sched || have_blk || have_regs || have_feat || have_keyed || have_pdist || have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout) return IGNPOW_E_PARAMS;
        if (!have_mib || mib < 2048 || mib > 16384 || !is_pow2(mib) || cmib < 1 || cmib > 1024 || parents < 1 || parents > 4096 || epoch < 1) return IGNPOW_E_PARAMS;
        if (have_ib && ib != 256) return IGNPOW_E_PARAMS;
        const uint64_t reads = have_rounds16 ? rounds : 256, pops16 = have_pops ? pops : 64, pblk16 = have_pblk ? pblk : 300;
        if (reads < 64 || reads > 1024 || pops16 < 16 || pops16 > 256 || pblk16 < 1 || pblk16 > epoch) return IGNPOW_E_PARAMS;
        p = ignh_params{};
        p.version = 15; p.dataset_bytes = mib << 20; p.rounds = uint32_t(reads); p.lanes = 8; p.epoch_blocks = epoch; p.item_chain = 1; p.chain_id = cid;
        p.cache_bytes = cmib << 20; p.parents = uint32_t(parents); p.item_bytes = 256; p.program_ops = uint32_t(pops16); p.period_blocks = pblk16;
        p.program_distance = read_rule_v ? 2u : 0u;  // the drawer's second keyed-read rule (bit 1; the canonical bytes 84 when set)
        std::memcpy(p.program_anchor, anchor, 32);
        return IGNPOW_OK;
    }
    // version 10 (SPEC-R0E-v0.md, the whole-die form): version 9's document and rules, the grammar fixed by the text (section 2);
    // version 11: version 10 with the window's composition drawn per period across the registered ranges (section 2, the ranges)
    const uint32_t v_r0e = version >= 10 ? version : 0;  // 15: the rotation (section 2f), the document's own fields the shape's, the families' fields the registered constants
    const bool v10 = v_r0e != 0;
    if (v10) version = 9;
    // version 9 (SPEC-R0D-v0.md): version 8's document (its block, register and feature rules) with the walk-only bit 8, bit 13 (the
    // per-period programme), PROGRAM_OPS in 1..65536 and PERIOD_BLOCKS in 1..EPOCH_BLOCKS; the anchor optional (zeros: the test anchor)
    const bool v9 = version == 9;
    if (v9) {
        if (!have_pops || pops < 1 || pops > 65536 || !have_pblk || pblk < 1 || pblk > epoch) return IGNPOW_E_PARAMS;
        if (!have_feat || !(feat & 8192) || !(feat & 256)) return IGNPOW_E_PARAMS;
        if (have_pdist && pdist > 8) return IGNPOW_E_PARAMS;  // the dependency distance: 0 (the plain draw) to 8 (16 registers leave at least 8 eligible)
    } else if (have_pops || have_pblk || have_anchor || have_pdist) return IGNPOW_E_PARAMS;
    if (v9) version = 8;  // the rest of the rule is version 8's; the version word is restored below
    if (version < 3 && have_ib) return IGNPOW_E_PARAMS;  // item_bytes is the field of versions 3 and 4 (the item-width variants)
    if (version == 3 && !(ib == 128 || ib == 256 || ib == 512)) return IGNPOW_E_PARAMS;
    if (version >= 4 && (!have_ib || !(ib == 32 || ib == 64 || ib == 128 || ib == 256 || ib == 512 || ib == 1024))) return IGNPOW_E_PARAMS;  // Strata: item_bytes mandatory; 32 and 64 the narrow controls, 1024 Riptide's widest
    if (version != 8 && (have_blk || have_regs || have_feat)) return IGNPOW_E_PARAMS;  // blocks, registers and features are version 8's (the blended relation, section 0.8)
    if (version == 8 && (feat > (v9 ? 32767 : 8191) || (feat & 0x40) || (!v9 && (feat & 8192)))) return IGNPOW_E_PARAMS;  // version 9: bit 13 the programme, bit 14 the 128 mix  // bit 6 reserved (the outside review's spill cell, undefined); bit 7 the cross-lane source; bit 8 walk-only and bit 9 programme-only are measurement modes (section 0.8), never candidate features
    if (version == 8 && (feat & 0x300) == 0x300) return IGNPOW_E_PARAMS;
    if (version == 8 && (feat & 2048) && (feat & 4096)) return IGNPOW_E_PARAMS;  // the two 2c forms are exclusive
    if (version == 8 && (feat & 2048) && (feat & 12)) return IGNPOW_E_PARAMS;  // the narrow window needs the full 16 registers (no chains)
    if (version == 8 && (feat & 2048) && regs != 16) return IGNPOW_E_PARAMS;
    // bit 10 (section 0.9, the bond-keyed rounds): keyed_rounds S in 2..4 mandatory with the bit, absent without it
    if (version == 8 && ((feat & 1024) ? (!have_keyed || keyed < 1 || keyed > rounds) : have_keyed)) return IGNPOW_E_PARAMS;  // S in 1..ROUNDS (the points 4, 14, 27, 64 on the security review's route h reading)
    if (version != 8 && have_keyed) return IGNPOW_E_PARAMS;  // bit 0 scratch in the block (needs scratch_kib > 0), bit 1 the predicated select, bit 2 two chains, bit 3 four chains (not both), bit 4 IEEE binary32 in opcode 2, bit 5 binary32 on every opcode (supersedes bit 4)
    if (version == 8 && ((feat & 12) == 12 || ((feat & 1) && kib == 0))) return IGNPOW_E_PARAMS;
    if (version == 8 && (!have_blk || !have_regs || !(blk == 1 || blk == 2 || blk == 4 || blk == 8 || blk == 16 || blk == 32 || blk == 64) || !(regs == 8 || regs == 16 || regs == 32 || regs == 64))) return IGNPOW_E_PARAMS;  // REGISTERS 32 and 64 widen the state to that many registers per lane (the live-window lever, section 0.8; the project's 00:59 UK order on the security review's reconstruction)
    if (!have_mib) mib = version >= 1 ? 1024 : 256;
    if (!have_kib) kib = version >= 1 ? 0 : 16;
    // the dataset cap rose from 4096 to 16384 MiB on the project's 22:52 order (the founder withdrew the 4.5 GiB constraint); above
    // 4 GiB nothing in the rule changes: the item index is 64-bit, the mask or the multiply-high map scales, the epoch seed is the same
    if (version == 5 || version == 8) { if (mib % 16 != 0 || mib < 16 || mib > 16384) return IGNPOW_E_PARAMS; }  // version 5: any multiple of 16 MiB (the multiply-high address map, section 0.5); version 8 likewise, the map chosen by the size (a power of two: the mask; otherwise the multiply-high map)
    else if (!is_pow2(mib) || mib < 16 || mib > 16384) return IGNPOW_E_PARAMS;
    if (kib == 0) { if (version < 1) return IGNPOW_E_PARAMS; }
    else if (!is_pow2(kib) || kib < 1 || kib > 1024) return IGNPOW_E_PARAMS;  // 1 and 2 KiB admitted for the state-expansion successor (strata/P2-STATE.md)
    if (rounds < 1 || rounds > 4096) return IGNPOW_E_PARAMS;
    if (lanes != 8) return IGNPOW_E_PARAMS;
    if (epoch < 1 || epoch > 0x100000000ull) return IGNPOW_E_PARAMS;
    if (chain < 1 || chain > 256) return IGNPOW_E_PARAMS;
    if (version >= 1 && chain != 1) return IGNPOW_E_PARAMS;
    if (version == 0 && (have_cmib || have_parents)) return IGNPOW_E_PARAMS;
    if (version != 2 && have_sched) return IGNPOW_E_PARAMS;
    if (version >= 1) {
        if (!is_pow2(cmib) || cmib < 1 || cmib > 256) return IGNPOW_E_PARAMS;
        if (parents < 1 || parents > 4096) return IGNPOW_E_PARAMS;
    }
    if (version == 2 && (sched < 2 || sched > 8)) return IGNPOW_E_PARAMS;
    if ((version != 5 && version != 8 && !is_pow2(mib << 20)) || (mib << 20) < ib * 8) return IGNPOW_E_PARAMS;
    (void)have_chain;
    p.version = uint32_t(version);
    p.dataset_bytes = mib << 20;
    p.scratch_bytes = uint32_t(kib << 10);
    p.rounds = uint32_t(rounds);
    p.lanes = uint32_t(lanes);
    p.epoch_blocks = epoch;
    p.item_chain = uint32_t(chain);
    p.chain_id = cid;
    p.cache_bytes = version >= 1 ? (cmib << 20) : 0;
    p.parents = version >= 1 ? uint32_t(parents) : 0;
    p.schedule = version == 2 ? uint32_t(sched) : 0;
    p.item_bytes = version >= 3 ? uint32_t(ib) : 128;
    p.blocks = version == 8 ? uint32_t(blk) : 0;
    p.registers = version == 8 ? uint32_t(regs) : 0;
    p.features = version == 8 ? uint32_t(feat) : 0;
    p.keyed_rounds = (version == 8 && (feat & 1024)) ? uint32_t(keyed) : 0;
    p.bond_rounds = p.keyed_rounds;  // F-441: the one accessor ignh_keyed_count reads this field at every keyed version
    p.program_ops = v9 ? uint32_t(pops) : 0;
    p.period_blocks = v9 ? pblk : 0;
    p.program_distance = v9 ? uint32_t(pdist) : 0;
    if (v_r0e == 13 || v_r0e == 14) {
        if (mmax > (v_r0e == 14 ? 27u : 14u) || (v_r0e == 14 && mmax < 8) || (chains != 1 && chains != 2 && chains != 4 && chains != 8) || glb > 1) return IGNPOW_E_PARAMS;
        p.mma_max = uint32_t(mmax); p.chains_per_nonce = uint32_t(chains); p.gather_lane_bank = uint32_t(glb);
    } else { if (have_mmax || have_chains || have_glb) return IGNPOW_E_PARAMS; p.mma_max = 0; p.chains_per_nonce = 0; p.gather_lane_bank = 0; }
    if (v_r0e == 14) {
        if (tshape < 1 || tshape > 3 || (fevery != 1 && fevery != 2 && fevery != 4 && fevery != 8) || ipr < 1 || ipr > 32 || (ipr & (ipr - 1))) return IGNPOW_E_PARAMS;
        if ((rlive != 16 && rlive != 64 && rlive != 128 && rlive != 256) || chint > 64 || ldraw > 1 || dscale < 10000 || dscale > 100000000) return IGNPOW_E_PARAMS;
        if (tlayout > 1 || (tlayout == 1 && tshape != 1)) return IGNPOW_E_PARAMS;  // the fragment layout is the 16x8x32 shape's
        p.tile_layout = uint32_t(tlayout);
        p.tile_shape = uint32_t(tshape); p.fold_every = uint32_t(fevery); p.items_per_round = uint32_t(ipr); p.registers_live = uint32_t(rlive); p.contexts_hint = uint32_t(chint); p.layout_draw = uint32_t(ldraw); p.difficulty_scale_ppm = uint32_t(dscale);
    } else { if (have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale || have_tlayout) return IGNPOW_E_PARAMS; p.tile_layout = 0; p.tile_shape = 0; p.fold_every = 0; p.items_per_round = 0; p.registers_live = 0; p.contexts_hint = 0; p.layout_draw = 0; p.difficulty_scale_ppm = 0; }
    if (v_r0e == kRotationVersion) {
        // the rotation's document carries the shape, the count, the period and the anchor; the families' fields are the registered
        // constants (section 2f), so none of versions 12 to 14's fields may appear
        if (have_tkib || have_abytes || have_skib || have_mmax || have_chains || have_glb || have_tshape || have_fevery || have_ipr || have_rlive || have_chint || have_ldraw || have_dscale) return IGNPOW_E_PARAMS;
        p.table_kib = 0; p.access_bytes = 0; p.shared_kib = 0;
    }
    if (v_r0e >= 12) {
        // the sizes are powers of two so every row index is one AND (the review lead's glue count)
        if (tkib < 1 || tkib > 64 || (tkib & (tkib - 1)) || (abytes != 4 && abytes != 8 && abytes != 16) || (skib != 0 && (skib < 16 || skib > 64 || (skib & (skib - 1))))) return IGNPOW_E_PARAMS;
        p.table_kib = uint32_t(tkib); p.access_bytes = uint32_t(abytes); p.shared_kib = uint32_t(skib);
    } else { if (have_tkib || have_abytes || have_skib) return IGNPOW_E_PARAMS; p.table_kib = 0; p.access_bytes = 0; p.shared_kib = 0; }
    std::memcpy(p.program_anchor, anchor, 32);
    if (v9) p.version = v10 ? v_r0e : 9;
    return IGNPOW_OK;
}

size_t params_len(const ignh_params& p) { return p.version == 22 ? 96 : (p.version == 21 || p.version == 25) && (p.features & 1024) ? 92 : p.version == 20 || p.version == 21 || p.version == 23 || p.version == 24 || p.version == 25 ? 88 : p.version == 17 || p.version == 18 || p.version == 19 || p.version == 15 ? (p.program_distance ? 84 : 80) : p.version == 16 ? (p.program_distance ? 84 : 80) : p.version == 14 ? (p.tile_layout ? 140 : p.difficulty_scale_ppm != 1000000 ? 136 : p.layout_draw ? 132 : (p.registers_live > 16 || p.contexts_hint) ? 128 : p.items_per_round > 1 ? 120 : p.fold_every > 1 ? 116 : 112) : p.version == 13 ? 108 : p.version == 12 ? 96 : (p.version == 9 || p.version == 10 || p.version == 11) ? (p.program_distance ? 84 : 80) : p.version == 8 ? ((p.features & 1024) ? 68 : p.features ? 64 : 60) : p.version >= 2 ? 52 : p.version == 1 ? 48 : 36; }  // FEATURES appended only when non-zero, so the six feature-free pids stand

void params_bytes(const ignh_params& p, uint8_t out[IGNH_PARAMS_BYTES]) {
    put_be32(out, p.version);
    put_be64(out + 4, p.dataset_bytes);
    put_be32(out + 12, p.scratch_bytes);
    put_be32(out + 16, p.rounds);
    put_be32(out + 20, p.lanes);
    put_be64(out + 24, p.epoch_blocks);
    put_be32(out + 32, p.item_chain);
    if (p.version >= 1) { put_be64(out + 36, p.cache_bytes); put_be32(out + 44, p.parents); }
    if (p.version == 2) put_be32(out + 48, p.schedule);
    if (p.version >= 3) put_be32(out + 48, p.item_bytes);
    if (p.version == 8) { put_be32(out + 52, p.blocks); put_be32(out + 56, p.registers); if (p.features) put_be32(out + 60, p.features); if (p.features & 1024) put_be32(out + 64, p.keyed_rounds); }
    if (p.version >= 9) { put_be32(out + 52, p.blocks); put_be32(out + 56, p.registers); put_be32(out + 60, p.features); put_be32(out + 64, p.keyed_rounds); put_be32(out + 68, p.program_ops); put_be64(out + 72, p.period_blocks); if (p.program_distance) put_be32(out + 80, p.program_distance); }
    if (p.version == 20 || p.version == 21 || p.version == 22 || p.version == 23 || p.version == 24 || p.version == 25) { put_be32(out + 80, p.program_distance); put_be32(out + 84, p.fold_every); }
    if (p.version == 22) { put_be32(out + 88, p.table_kib); put_be32(out + 92, p.access_bytes); }  // the fragments and the coalesced form (with its contiguity) bound into P
    if ((p.version == 21 || p.version == 25) && (p.features & 1024)) put_be32(out + 88, p.bond_rounds);  // F-441: the keyed item-read count in a slot of its own under bit 10 (byte 64 is the batch width under 21 and 25); version 18's count rides byte 64 by the line above
    if (p.version >= 12 && p.version <= 16) { put_be32(out + 80, p.program_distance); put_be32(out + 84, p.table_kib); put_be32(out + 88, p.access_bytes); put_be32(out + 92, p.shared_kib); }
    if (p.version >= 13) { put_be32(out + 96, p.mma_max); put_be32(out + 100, p.chains_per_nonce); put_be32(out + 104, p.gather_lane_bank); }
    if (p.version == 14) {
        const bool tl = p.tile_layout != 0;
        const bool ds = p.difficulty_scale_ppm != 1000000 || tl;
        const bool ext = p.registers_live > 16 || p.contexts_hint || p.layout_draw || ds;
        put_be32(out + 108, p.tile_shape);
        if (p.fold_every > 1 || p.items_per_round > 1 || ext) put_be32(out + 112, p.fold_every);
        if (p.items_per_round > 1 || ext) put_be32(out + 116, p.items_per_round);
        if (ext) { put_be32(out + 120, p.registers_live); put_be32(out + 124, p.contexts_hint); }
        if (p.layout_draw || ds) put_be32(out + 128, p.layout_draw);
        if (ds) put_be32(out + 132, p.difficulty_scale_ppm);
        if (tl) put_be32(out + 136, p.tile_layout);
    }  // FOLD_EVERY at 112 only above 1, as PROGRAM_DISTANCE's rule, so the k = 1 documents of record keep their bytes
}

// ---- the context ----

// THE DATASET STORE (the project's order of 15 October 2026 08:5x UK, F-142's recurring OOM on a build box: the reference held a separate gigabyte
// for every version 21 document a node imports, seven served lanes, 9 to 13 GB a node): documents sharing one DATASET BASE (the same
// epoch seed E and dataset id D, so the same cache and keyed items by construction, and the same cache size) share ONE store, each
// context holding only its own tables beside it. The registry is process-wide under a mutex, weak so a store dies with its last
// context; a context created light stays light over a store that later gains the dataset (its own reads derive from the cache). The
// explicit shared context (ih_create_shared, below) keeps its refusal of a donor whose dataset is another base.
struct ds_store {
    uint8_t E[32], D[32]; uint64_t cache_bytes = 0;
    std::vector<uint8_t> cache, dataset;
    std::mutex build_mu;  // held while a context builds into the store; a joiner waits on it
    uint64_t build_ms = 0, cache_ms = 0;
};
static std::mutex g_ds_mu;
static std::vector<std::weak_ptr<ds_store>> g_ds;
std::shared_ptr<ds_store> ds_acquire(const uint8_t E[32], const uint8_t D[32], uint64_t cache_bytes, bool* joined) {
    std::lock_guard<std::mutex> lk(g_ds_mu);
    for (size_t i = 0; i < g_ds.size();) {
        auto sp = g_ds[i].lock();
        if (!sp) { g_ds[i] = g_ds.back(); g_ds.pop_back(); continue; }
        if (std::memcmp(sp->E, E, 32) == 0 && std::memcmp(sp->D, D, 32) == 0 && sp->cache_bytes == cache_bytes) { *joined = true; return sp; }
        ++i;
    }
    auto sp = std::make_shared<ds_store>(); std::memcpy(sp->E, E, 32); std::memcpy(sp->D, D, 32); sp->cache_bytes = cache_bytes;
    g_ds.push_back(sp); *joined = false; return sp;
}
static const std::vector<uint8_t> kNoBytes;

// F-219 (16 October 2026, the research chain's second growth under 0.10.37: every node 5 to 26 GB an hour, three OOM kills):
// the raster form's working buffers were thread_local (a 256 MiB or 1 GiB calloc'd target, the tiles, the cell keys and the
// digest buffer), allocated on an OS thread's first raster hash and never freed while the thread lived; a node hashes from
// many OS threads (the parallel share verification and the Go runtime's threads for calls into C: 54 to 64 on the nodes), so
// the resident set grew by a thread's buffers with every thread that ever hashed. The buffers now live in a BOUNDED POOL owned
// by the context: a slot is leased for one attempt and returned, the slots are shared across threads, their count is at most
// the node's parallel verification width (ignpow_set_parallel_width; the hardware's concurrency when unset) and the pool is
// freed with the context (ignpow_ctx_free). Never thread_local, never per call. The unkeyed form draws on 18d's tiles over a
// zero base in place of the calloc'd target (the same bytes: the target is the sparse overlay with a zero base), so a slot is
// the tiles (4 KiB a triangle), the keys and the digest buffer, about 8 MB at 1,024 triangles.
namespace r18 {
struct scratch {
    std::vector<uint32_t> keys, tiles;
    std::vector<uint8_t> dig;
};
struct pool {
    std::mutex mu;
    std::condition_variable cv;
    std::vector<std::unique_ptr<scratch>> idle;
    size_t made = 0;
    scratch* acquire(size_t cap) {
        if (!cap) cap = 1;
        std::unique_lock<std::mutex> lk(mu);
        for (;;) {
            if (!idle.empty()) { scratch* s = idle.back().release(); idle.pop_back(); return s; }
            if (made < cap) {
                scratch* s = new scratch();  // counted only once it exists: a refused allocation (bad_alloc) loses no slot of the bound (the security review's read, 16 October 2026)
                ++made;
                return s;
            }
            cv.wait(lk);  // every slot in flight: the attempt waits for one (the width is the bound on the attempts at once; an untimed wait, a waiter holding no slot: the recorded residual for 0.10.39)
        }
    }
    void release(scratch* s) { { std::lock_guard<std::mutex> lk(mu); idle.emplace_back(s); } cv.notify_one(); }
    size_t slots() { std::lock_guard<std::mutex> lk(mu); return made; }
};
// a lease: one slot for the attempt's duration, returned on every path out
struct lease {
    pool& p; scratch* s;
    lease(pool& pp, size_t cap) : p(pp), s(pp.acquire(cap)) {}
    ~lease() { p.release(s); }
    lease(const lease&) = delete; lease& operator=(const lease&) = delete;
};
}  // namespace r18

struct ih_ctx final : ctx_base {
    ignh_params p;
    uint8_t P[32];
    uint8_t Pd[32];  // the DATASET parameter hash (section 0.9, the project's ruling of 10:55 UK, 11 Oct 2026): P of the document with bit 10 cleared and KEYED_ROUNDS absent, so a keyed document and its unkeyed twin share one dataset; equal to P for an unkeyed document
    uint64_t ep = 0;
    uint8_t E[32];
    uint8_t D[32];
    std::shared_ptr<ds_store> store;  // the dataset base (the cache and, when resident, the dataset), shared by every context of the base
    bool resident = false;            // a full context reads the store's dataset; a light one derives from the cache
    // version 15 (section 2f, the rotation): the four families' contexts over this context's cache and dataset (data_owner), one
    // routed per window by the balancing period's permutation
    const ih_ctx* data_owner = nullptr;
    std::unique_ptr<ih_ctx> fam[4];
    // F-219: the raster form's bounded buffer pool, freed with the context; a context over another's memory (a family, a
    // shared context) leases from its data owner's, so a per-call shared context allocates no buffers of its own
    mutable std::unique_ptr<r18::pool> rpool = std::make_unique<r18::pool>();
    r18::pool& raster_pool() const { return data_owner ? data_owner->raster_pool() : *rpool; }
    const std::vector<uint8_t>& cache_v() const { return data_owner ? data_owner->cache_v() : store ? store->cache : kNoBytes; }      // through the owner's owner (a family over a shared context)
    const std::vector<uint8_t>& dataset_v() const { return data_owner ? data_owner->dataset_v() : (resident && store) ? store->dataset : kNoBytes; }
    // version 9 (SPEC-R0D-v0.md): the period's programme, drawn from (chain_id, period, anchor) and cached by period
    struct r0d_op { uint8_t opcode, dst, src1, src2; uint32_t imm; };
    mutable std::mutex prog_mu;
    mutable uint64_t prog_period = ~uint64_t(0);
    mutable std::shared_ptr<const std::vector<r0d_op>> prog;  // the plain calls' programme (the document's anchor), shared so a period change never regenerates into a vector a thread reads
    // the anchored calls' cache (the protocol team's two-branch verifier): four drawn programmes keyed by (period, anchor),
    // replaced round-robin; the plain calls keep the document's anchor in prog above
    struct prog_slot { uint64_t period = ~uint64_t(0); uint8_t anchor[32] = {}; std::shared_ptr<const std::vector<r0d_op>> prog; };
    mutable prog_slot prog_slots[8];
    // version 12's shared table (shared_kib): one per (period, anchor), filled once from the dataset, shared by every nonce of the period
    struct shared_slot { uint64_t period = ~uint64_t(0); uint8_t anchor[32] = {}; std::shared_ptr<const std::vector<uint32_t>> tab; };
    mutable shared_slot shared_slots[4];
    // version 16's compiled programme per (period, anchor): the drawn operations decoded once at the anchor into a flat table the
    // verifier runs without the draw (the project's order of 16:0x: the interpreted verifier read 0.85 ms a block; the draw's 397 KB XOF
    // at rounds 128 was the term); four slots as the shared table's
    struct r16f_slot { uint64_t period = ~uint64_t(0); uint8_t anchor[32] = {}; std::shared_ptr<const std::vector<uint32_t>> ops; };
    mutable r16f_slot r16f_slots[4];
    mutable unsigned r16f_next = 0;
    mutable unsigned shared_next = 0;  // eight: two periods across a boundary, two branches, and the verifier's and the pool's margin (the protocol team's lab reading)
    mutable unsigned prog_next = 0;
    mutable uint8_t r0e_window[32] = {};  // version 10: the draw's current window (scratch)
    mutable uint8_t r0e_row[32] = {};     // version 11: the period's composition as a row of 32 opcodes (scratch)
    mutable uint32_t r0e_items = 0;       // version 11: the tile's item-sourced operand count (scratch)
    mutable uint8_t r0e_tile_seed[32] = {};  // version 11: the tile chain's current seed (scratch)
    uint64_t build_ms = 0;
    uint64_t cache_ms = 0;
    uint64_t n_cache() const { return p.cache_bytes / kItemBytes; }
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_IGNEUMHASH; }
    uint64_t n_items() const { return p.dataset_bytes / p.item_bytes; }
    uint32_t item_bytes() const { return p.item_bytes; }
    uint32_t slice_words() const { return p.scratch_bytes / 4 / p.lanes; }
};

// The envelope prefix tag || C || V || P.
// The dataset parameter hash Pd (section 0.9): the document's bytes with bit 10 cleared and KEYED_ROUNDS absent (the
// unkeyed twin's document), hashed as P is; for an unkeyed document Pd = P.
void dataset_params_hash(const ignh_params& p, uint8_t out[32]) {
    ignh_params t = p;
    t.features &= ~uint32_t(1024);
    t.keyed_rounds = 0;
    t.bond_rounds = 0;  // F-441: the released walks' keyed count is outside the dataset commitment as version 8's is (one dataset for the pair)
    if (t.version >= 9) { t.version = 8; t.features &= ~uint32_t(8192 | 16384); t.table_kib = 0; t.access_bytes = 0; t.shared_kib = 0; t.mma_max = 0; t.chains_per_nonce = 0; t.gather_lane_bank = 0; t.tile_shape = 0; t.fold_every = 0; t.items_per_round = 0; t.registers_live = 0; t.contexts_hint = 0; t.layout_draw = 0; t.difficulty_scale_ppm = 0; t.tile_layout = 0; t.program_ops = 0; t.period_blocks = 0; t.program_distance = 0; }  // R0D's dataset is R0C's: the programme is outside the commitment as the keyed rounds are
    uint8_t pb[IGNH_PARAMS_BYTES];
    params_bytes(t, pb);
    b3_hash(pb, params_len(t), out, 32);
}
// tag || C || V || Pd: the pre-image prefix of the dataset commitment (the epoch seed E and the dataset id D)
bytes prefix_d(const ih_ctx& c, const char* tag) {
    bytes b;
    // version 9's dataset is the version 8 twin's (SPEC-R0D-v0.md): the commitment's version word is the twin's too
    b.tag(tag).be64(c.p.chain_id).be32((c.p.version >= 9) ? 8u : c.p.version).raw(c.Pd, 32);
    return b;
}
bytes prefix(const ih_ctx& c, const char* tag) {
    bytes b;
    b.tag(tag).be64(c.p.chain_id).be32(c.p.version).raw(c.P, 32);
    return b;
}

// Version 1 cache, section 5.3: the shared builder (r0a_dataset.hpp).
void build_cache(ih_ctx& c) {
    const auto t0 = std::chrono::steady_clock::now();
    r0a::build_cache(c.E, c.p.cache_bytes, c.store->cache);
    c.cache_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

// item(i), section 5.3.
void derive_item(const ih_ctx& c, uint64_t i, uint8_t out[kItemBytes]) {
    bytes in;
    in.tag(kTagItem).be64(i);
    blake3_hasher h;
    blake3_hasher_init_keyed(&h, c.E);
    if (c.p.version >= 3) { r0a::derive_item_w(c.E, c.cache_v(), c.p.parents, c.p.item_bytes, i, out); return; }
    if (c.p.version >= 1) { r0a::derive_item(c.E, c.cache_v(), c.p.parents, i, out); return; }
    blake3_hasher_update(&h, in.v.data(), in.v.size());
    if (c.p.item_chain == 1) { blake3_hasher_finalize(&h, out, kItemBytes); return; }
    uint8_t K[32];
    blake3_hasher_finalize(&h, K, 32);
    for (uint32_t j = 2; j <= c.p.item_chain; ++j) {
        blake3_hasher_init_keyed(&h, c.E);
        blake3_hasher_update(&h, in.v.data(), in.v.size());
        blake3_hasher_update(&h, K, 32);
        if (j == c.p.item_chain) blake3_hasher_finalize(&h, out, kItemBytes);
        else blake3_hasher_finalize(&h, K, 32);
    }
}

// The item at index i as item_bytes / 4 words (32 under versions 0 to 2; 32, 64 or 128 under version 3; 16 to 128 under version 4).
void load_item(const ih_ctx& c, uint64_t i, uint32_t* W) {
    const uint32_t ib = c.item_bytes();
    uint8_t buf[1024];
    const uint8_t* src;
    if (!c.dataset_v().empty()) src = c.dataset_v().data() + i * ib;
    else { derive_item(c, i, buf); src = buf; }
    for (uint32_t k = 0; k < ib / 4; ++k) W[k] = le32(src + 4 * k);
}

// Trace sinks: f is the per-round register JSON (ignh_trace); addr and lanes are
// the security review's access and lane-dependence lines (ignh_access_trace); any may be null.
struct tracer {
    FILE* f = nullptr;
    FILE* addr = nullptr;
    FILE* lanes = nullptr;
    uint64_t epoch = 0, nonce = 0;
    bool first_round = true;
    bool pending_block = false; uint32_t block_regs[8][64] = {}; std::string block_ops;
    bool pending_prog = false; uint32_t prog_regs[8][64] = {};  // version 9: the lanes' registers after the period's programme
    char file_hex[65] = {}; bool pending_file = false;  // version 14's live file after the programme (registers_live above 16), its digest per round
    bool pending_share = false; char share_hex[65] = {};  // section 0.9: the keyed round's share, written inside the round's record (11 Oct 2026: it was written before the record's brace, which left the keyed trace files unparsable as JSON)
    uint32_t width = 16;  // the state's registers per lane (16; 32 or 64 under version 8's wider state, section 0.8)
    void regs(const char* name, const uint32_t R[8][64], uint32_t lanes) {
        std::fprintf(f, "\"%s\": [", name);
        for (uint32_t l = 0; l < lanes; ++l) {
            std::fprintf(f, "%s[", l ? ", " : "");
            for (uint32_t k = 0; k < width; ++k) std::fprintf(f, "%s%u", k ? ", " : "", R[l][k]);
            std::fprintf(f, "]");
        }
        std::fprintf(f, "]");
    }
};

// Version 8 (section 0.8, the blended relation): the RXE1 block schedule inside version 4's round, byte for byte the
// RXE1 text's forms (SPEC-TEXT-v0.1.md section 7, schema 2's draw) with the LP framing of that text: LP(x) = LE32(len) || x.
struct lpenc {
    std::vector<uint8_t> v;
    lpenc& lp(const uint8_t* p, size_t n) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(n >> (8 * i))); v.insert(v.end(), p, p + n); return *this; }
    lpenc& tag(const char* s) { return lp(reinterpret_cast<const uint8_t*>(s), std::strlen(s)); }
    lpenc& le32(uint32_t x) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
};
struct v8desc { uint8_t src1, src2, rot, opcode, pbit, salt, shift, shift1; };  // shift: bit 7's cross-lane second-source lane offset (1..7); shift1/shift: bit 12's two source lanes (0..7)
#if defined(FLT_EVAL_METHOD) && FLT_EVAL_METHOD != 0
#error "version 8 option (b) needs separate rounding: FLT_EVAL_METHOD 0"
#endif
#pragma STDC FP_CONTRACT OFF   // contraction forbidden in this file by the standard pragma (clang honours it); FP_FAST_FMAF is not the test: Apple's math.h defines it on every arm64 build whatever -ffp-contract says (the Mac builds failed on the macro, 10 Oct 2026 23:2x UK)
// option (b): the floating form of opcode 2 (section 0.8 bit 4): operands masked to positive normals in [2^-63, 2^0),
// product and sum rounded separately to nearest-even, the result's bits XORed into the destination
inline uint32_t v8fop(uint32_t x, uint32_t y, uint32_t z) {
    const uint32_t yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u;
    float yf, zf; std::memcpy(&yf, &yb, 4); std::memcpy(&zf, &zb, 4);
    const uint32_t xb = (x & 0x3FFFFFFFu) | 0x20000000u; float xf; std::memcpy(&xf, &xb, 4);  // the addend masked to the same range, so the product's rounding is visible in the sum
    volatile float prod = yf * zf;             // rounded once (volatile: no contraction into the add)
    const float sum = prod + xf;               // rounded once
    uint32_t fb; std::memcpy(&fb, &sum, 4);
    return fb ^ x;
}
// option (b) wide, bit 5: every opcode's arithmetic as binary32 on the masked range (the xor and the rotate have no
// counterpart: the xor becomes a subtraction, the rotate is dropped); two roundings per opcode, never contracted
constexpr uint32_t kWalkConst = 0x9E3779B9u;  // bit 8: the constant standing in for the block's output
inline float v8mask(uint32_t w) { const uint32_t b = (w & 0x3FFFFFFFu) | 0x20000000u; float f; std::memcpy(&f, &b, 4); return f; }
inline uint32_t v8fop_wide(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z) {
    const float xf = v8mask(x), yf = v8mask(y), zf = v8mask(z);
    volatile float inner;
    float f;
    switch (opcode) {
        case 0: inner = yf - zf; f = xf + inner; break;       // x + rotl(y ^ z, rot)  ->  x' + (y' - z')
        case 1: inner = yf + zf; f = xf - inner; break;       // x ^ rotl(y + z, rot)  ->  x' - (y' + z')
        case 2: inner = yf * zf; f = inner + xf; break;       // x + y * (z | 1)       ->  (y' * z') + x'
        default: inner = xf - yf; f = inner + zf; break;      // rotl(x ^ y, rot) + z  ->  (x' - y') + z'
    }
    uint32_t fb; std::memcpy(&fb, &f, 4);
    return fb ^ x;
}
inline uint32_t v8op(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z, uint32_t rot) {
    switch (opcode) {
        case 0: return x + rotl32(y ^ z, rot);
        case 1: return x ^ rotl32(y + z, rot);
        case 2: return x + y * (z | 1u);
        default: return rotl32(x ^ y, rot) + z;
    }
}
// Draw revision 2 (the project's 23:31 UK order on the GPU team's 5090 reading of revision 1, which drew one BLAKE3 per descriptor
// per round, 2048 compressions per nonce at (2, 16), the generator eight times the walk, m 0.069): the programme is
// drawn from the FETCHED ITEM. Descriptor i = b K + d of the round takes its programme word p = W[i mod n] + (i div n)
// MUL (the item's words in order, a lap constant when B K exceeds the item's n words) and decodes it by ALIGNED bit
// fields (the project's 23:47 order: every extract one instruction on every vendor, a BFE or a shift and mask):
//   bits 0..3  src1 offset   src1 = base + ((dd + 1 + f) mod M)     (f = 15 gives the destination itself: x and y equal)
//   bits 4..7  src2 offset   src2 = base + ((dd + 1 + f) mod M)     (may equal src1 or d)
//   bits 8..12 rot (0..31)   bits 13..14 the BLOCK's opcode phase (read from descriptor (b, 0) only; revision 2b)
//   bits 15..19 pbit (bit 1)   bits 20..22 shift (bit 7, | 1: odd, never 0)
// Revision 2b (the outside review's verify ruling, the project's 23:49 UK order: no programme grinding): the opcode CLASS counts are
// fixed per item: opcode(b, d) = (d + phase_b) & 3 with phase_b from the block's first programme word, so every block
// runs exactly K / 4 descriptors of each opcode and every item the same class counts; the item's bits choose the
// operands, the phase (which destination gets which class) and the dependency shape, never how many of each class.
//   bits 24..31 salt (bit 0)
// Generation costs no hash: the item selects the programme and the programme's output selects the next address.
void v8_decode(const uint32_t* W, uint32_t n, uint32_t B, uint32_t K, uint32_t features, std::vector<v8desc>& desc) {
    desc.resize(size_t(B) * K);
    const uint32_t C = (features & 8) ? 4 : (features & 4) ? 2 : 1, M = K / C;  // chain count, chain size (4, 8 or 16)
    for (uint32_t b = 0; b < B; ++b) {
        const uint32_t p0 = W[(b * K) % n] + ((b * K) / n) * kMul;
        const uint32_t phase = (p0 >> 13) & 3;  // the block's opcode phase (revision 2b)
        for (uint32_t d = 0; d < K; ++d) {
            const uint32_t i = b * K + d;
            const uint32_t p = W[i % n] + (i / n) * kMul;
            v8desc& x = desc[i];
            const uint32_t base = (d / M) * M, dd = d % M;  // the chain's registers base .. base + M - 1
            if (features & 4096) {
                // revision 2c form (b), bit 12 (the project's 00:03 UK order): no register indexed by data. The registers are
                // slot-fixed, src1 = d + 1, src2 = d + 2 (mod M, in the chain), and BOTH sources are shuffles from lanes the
                // item chooses: lane (l + shift1) mod 8 for the first, (l + shift) mod 8 for the second (0 allowed: own lane)
                x.src1 = uint8_t(base + ((dd + 1) & (M - 1)));
                x.src2 = uint8_t(base + ((dd + 2) & (M - 1)));
                x.shift1 = uint8_t(p & 7);
                x.shift = uint8_t((p >> 4) & 7);
            } else if (features & 2048) {
                // revision 2c form (a), bit 11: narrow select, each source two bits among four fixed candidates of the
                // slot's rotating window, src1 in {d + 1 .. d + 4}, src2 in {d + 5 .. d + 8} (mod M; M >= 16 required)
                x.src1 = uint8_t(base + ((dd + 1 + (p & 3)) & (M - 1)));
                x.src2 = uint8_t(base + ((dd + 5 + ((p >> 4) & 3)) & (M - 1)));
                x.shift1 = 0;
            } else {
                x.src1 = uint8_t(base + ((dd + 1 + (p & 15)) & (M - 1)));
                x.src2 = uint8_t(base + ((dd + 1 + ((p >> 4) & 15)) & (M - 1)));
                x.shift1 = 0;
            }
            x.rot = uint8_t((p >> 8) & 31);
            x.opcode = uint8_t((d + phase) & 3);
            x.pbit = uint8_t((p >> 15) & 31);
            if (!(features & 4096)) x.shift = uint8_t(((p >> 20) & 7) | 1);  // bit 7's lane offset; under bit 12 the branch above set both lanes (the Orchard lead's 00:1x UK finding: this line overwrote it)
            x.salt = uint8_t(p >> 24);
        }
    }
}

// WORK(S, E) = M, sections 4.1 to 4.7.
// ---- version 9, the per-period programme (SPEC-R0D-v0.md sections 3 and 4) ----
constexpr const char* kTagR0dProgram = "IgneumHash/R0D/program";
constexpr const char* kTagR0eProgram = "IgneumHash/R0E/program";  // version 10's seed tag (DEVNET11-R0E-CHAIN-FORM.md)
constexpr const char* kTagR0dOp = "IgneumHash/R0D/op";
enum : uint8_t { R0D_FMA = 0, R0D_FADD, R0D_IXOR, R0D_IROTL, R0D_IADD, R0D_IMULHI, R0D_POPC, R0D_CLZ,
                 R0E_SHFL = 8, R0E_GATHER = 9, R0E_TILE = 10,
                 R0E_SCATTER = 11, R0E_MMA = 12 };  // version 12 (SPEC-R0E-v0 section 2c, the SRAM-bound form): the data-dependent row write into the per-nonce table; the exact INT8 matrix tile  // version 10 (SPEC-R0E-v0.md section 2): the cross-lane shuffle, the per-nonce table gather, the octet's tile transpose
constexpr uint8_t kR0eItemFlag = 0x80;  // version 10: the operation's second source is a word of the fetched item at a register-drawn index
// version 10's window: 32 opcodes at the registered counts (the first values of SPEC-R0E-v0.md section 2), drawn as a seeded permutation
constexpr uint8_t kR0eWindow[32] = { R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA, R0D_FMA,
                                     R0D_FADD, R0D_FADD, R0D_FADD, R0D_FADD, R0D_IXOR, R0D_IXOR, R0D_IXOR, R0D_IROTL, R0D_IROTL, R0D_IROTL, R0D_IADD, R0D_IMULHI,
                                     R0E_SHFL, R0E_SHFL, R0E_SHFL, R0E_SHFL, R0E_GATHER, R0E_GATHER, R0E_TILE, R0E_TILE };
constexpr uint32_t kR0eItemOps = 8;        // the item-sourced operand on 8 of the window's 32 (on the FP and integer operations only)
// version 11: every window (tile) of 32 draws ITS OWN composition across the registered ranges (SPEC-R0E-v0.md section 2b; the
// auditor's eleventh reading): ten counts drawn in class order from the tile's composition draw (count = lo + byte mod (hi - lo + 1)),
// the FMA count the remainder to 32 clamped to 4..20, the remainder's overflow given to the ten classes in the same order up to their
// ceilings and its deficit taken from them in the same order down to their floors; the item-sourced operand's count from the
// eleventh byte, 4 to 12, capped by the FP and integer count. Every security-critical class has a NONZERO floor (the shuffle, the
// gather, the tile, the item operand, the integer classes IXOR, IROTL, IADD, IMULHI), so no period and no tile is mineable without
// its units; the tiles' seeds are chained (the first from the period seed, each next from the last), so the programme is a chain
// of dependent tiles each with its own mix, each tile's operands the last tile's register output
constexpr uint8_t kR0eDrawOrder[10] = { R0D_FADD, R0D_IXOR, R0D_IROTL, R0D_IADD, R0D_IMULHI, R0D_POPC, R0D_CLZ, R0E_SHFL, R0E_GATHER, R0E_TILE };
constexpr uint8_t kR0eLo[10] = { 0, 1, 1, 1, 1, 0, 0, 1, 1, 1 };
constexpr uint8_t kR0eHi[10] = { 6, 6, 6, 4, 4, 3, 3, 6, 4, 3 };
constexpr uint32_t kR0eFmaLo = 4, kR0eFmaHi = 20;
constexpr uint32_t kR0eItemLo = 4, kR0eItemHi = 12;
// version 12 (SPEC-R0E-v0 section 2c, the founder's SRAM-bound form): GATHER and SCATTER into the per-nonce shared-memory table are the
// bulk of every tile; FMA, the integer group, SHFL and TILE stay as floors; the exact INT8 matrix tile (MMA) a class the draw may take;
// twelve counts drawn in this order, GATHER the remainder (the Formal VM seat's ranges of 22:0x, MMA added)
constexpr uint8_t kR12DrawOrder[12] = { R0D_FADD, R0D_IXOR, R0D_IROTL, R0D_IADD, R0D_IMULHI, R0D_POPC, R0D_CLZ, R0E_SHFL, R0E_TILE, R0E_SCATTER, R0E_MMA, R0D_FMA };
constexpr uint8_t kR12Lo[12] = { 0, 1, 1, 0, 0, 0, 0, 1, 1, 4, 0, 4 };
constexpr uint8_t kR12Hi[12] = { 1, 2, 2, 1, 1, 1, 1, 2, 1, 10, 4, 10 };  // MMA 0 to 4: the generator's objective puts the INT8 tile at 4 per window (the class whose edge pins the ratio)
constexpr uint32_t kR12GatherLo = 6, kR12GatherHi = 14;
constexpr uint32_t kR12ItemLo = 2, kR12ItemHi = 6;
// version 14 (SPEC-R0E-v0 section 2e, proof of matrix work): the exact INT8 tile is the bulk of every window (the remainder, clamped
// 20 to mma_max), the general lane and the table as floors; eleven counts in this order, GATHER and SCATTER 1 to 2
// the resource passport (the outside review's reading of 22:1x): the MMA count per window is the document's mma_max EXACTLY (the matrix share
// pinned, five shares cut as documents of record), the ten floor classes drawn inside their registered ranges, FMA the remainder
constexpr uint8_t kR14DrawOrder[10] = { R0D_IXOR, R0D_IROTL, R0D_IADD, R0D_IMULHI, R0D_FADD, R0E_SHFL, R0E_TILE, R0E_GATHER, R0E_SCATTER, R0D_POPC };
constexpr uint8_t kR14Lo[10] = { 1, 0, 0, 0, 0, 1, 0, 1, 1, 0 };
constexpr uint8_t kR14Hi[10] = { 2, 1, 1, 1, 1, 2, 1, 2, 2, 1 };
constexpr uint32_t kR14FmaLo = 1;
constexpr uint32_t kR14ItemLo = 1, kR14ItemHi = 3;
// the differential hooks (the memory cryptanalysis harness's T5c, T6d and T6e): one process-wide flip set by the vectors tool before
// a gen and cleared after; off (kind 0) in every other process. kind 1: the table word a's bit b flipped after the register mix;
// kind 2: bit b of word 0 of the item fetched at scatter step a flipped before the fold and the table write; kind 3: bit c of
// register R[a][b] flipped right after the lane fill, before the table fill
struct r0e_flip { int kind = 0; uint32_t a = 0, b = 0, c = 0; };
static r0e_flip g_r0e_flip;
constexpr uint32_t kR0eTableWords = 4096;      // version 10: the per-nonce 16 KB table from the XOF
constexpr uint32_t kR0eTableWordsV11 = 1024;   // version 11: the per-nonce 4 KB table from a dataset slice (G1, G3 of the memory cryptanalysis read; the GPU team's occupancy cells at 16, 4 and 2 KB decide the registered size)
constexpr uint32_t kR0eTableItemsV11 = 16;     // 16 contiguous items of 256 bytes at a nonce-drawn offset
// the weight table of section 4 (of 256): the opcode from q[0]
inline uint8_t r0d_opcode(uint8_t q) {
    // FMA 224, IXOR 16, IROTL 16 (revision 0.3's mix); the eight-op table is the 128 mix, selected by features bit 14
    if (q < 224) return R0D_FMA;
    if (q < 240) return R0D_IXOR;
    return R0D_IROTL;
}
inline uint8_t r0d_opcode_mix128(uint8_t q) {
    if (q < 128) return R0D_FMA; if (q < 160) return R0D_FADD; if (q < 184) return R0D_IXOR; if (q < 208) return R0D_IROTL;
    if (q < 224) return R0D_IADD; if (q < 240) return R0D_IMULHI; if (q < 248) return R0D_POPC; return R0D_CLZ;
}
// program_seed = BLAKE3(LP(tag) || LE64(chain_id) || LE64(period) || anchor); op i from BLAKE3xof(LP(tag) || LP(seed) || LE32(i), 8)
void r0d_generate(const ih_ctx& c, uint64_t period, std::vector<ih_ctx::r0d_op>& out, const uint8_t* anchor = nullptr) {
    uint8_t seed[32];
    if (!anchor) anchor = c.p.program_anchor;
    { bytes in; in.tag(c.p.version >= 10 ? kTagR0eProgram : kTagR0dProgram).le64(c.p.chain_id).le64(period).raw(anchor, 32); b3_hash(in, seed, 32); }
    const bool mix128 = (c.p.features & 16384) != 0;
    const uint32_t D = c.p.program_distance;  // revision 0.4: the dependency distance (0: the plain draw)
    int64_t last_write[16]; for (int k = 0; k < 16; ++k) last_write[k] = -1000000;
    out.resize(c.p.program_ops);
    for (uint32_t i = 0; i < c.p.program_ops; ++i) {
        uint8_t q[8];
        bytes in; in.tag(kTagR0dOp).lp(seed, 32).le32(i);
        b3_hash(in, q, 8);
        ih_ctx::r0d_op x;
        if (c.p.version >= 10) {
            // the window's 32 opcodes are a permutation of the period's row (version 10: kR0eWindow; version 11: the composition
            // drawn below) by a Fisher-Yates over the window's own XOF bytes, and n_items of its FP or integer operations carry
            // the item flag, chosen by a second permutation
            if ((i & 31) == 0 && c.p.version == 14) {
                if (i == 0) { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                else { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(c.r0e_tile_seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                uint8_t cs[16]; { bytes cb; cb.tag("IgneumHash/R0E/tile").lp(c.r0e_tile_seed, 32); b3_hash(cb, cs, sizeof cs); }
                const int32_t mm = int32_t(c.p.mma_max);  // the matrix share, pinned by the document
                uint32_t cnt[10]; uint32_t sum = 0;
                for (int k = 0; k < 10; ++k) { cnt[k] = kR14Lo[k] + cs[k] % (kR14Hi[k] - kR14Lo[k] + 1); sum += cnt[k]; }
                int32_t fm = 32 - mm - int32_t(sum);  // FMA the remainder (the floors sum to 4 and the ceilings to 14)
                if (fm < int32_t(kR14FmaLo)) {
                    uint32_t deficit = uint32_t(int32_t(kR14FmaLo) - fm); fm = kR14FmaLo;
                    for (int k = 0; k < 10 && deficit; ++k) { const uint32_t room = cnt[k] - kR14Lo[k]; const uint32_t g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; }
                }
                uint32_t pos = 0; for (int32_t m = 0; m < mm; ++m) c.r0e_row[pos++] = R0E_MMA;
                for (int32_t m = 0; m < fm; ++m) c.r0e_row[pos++] = R0D_FMA;
                for (int k = 0; k < 10; ++k) for (uint32_t m = 0; m < cnt[k]; ++m) c.r0e_row[pos++] = kR14DrawOrder[k];
                const uint32_t items = kR14ItemLo + cs[11] % (kR14ItemHi - kR14ItemLo + 1);
                const uint32_t fpint = uint32_t(fm) + cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[9];
                c.r0e_items = items < fpint ? items : fpint;
            }
            if ((i & 31) == 0 && (c.p.version == 12 || c.p.version == 13)) {
                if (i == 0) { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                else { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(c.r0e_tile_seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                uint8_t cs[16]; { bytes cb; cb.tag("IgneumHash/R0E/tile").lp(c.r0e_tile_seed, 32); b3_hash(cb, cs, sizeof cs); }
                uint32_t cnt[12]; uint32_t sum = 0;
                // version 13 (SPEC-R0E-v0 section 2d): the MMA ceiling is the document's mma_max (the registered constant); version 12's is 4
                uint8_t hi[12]; for (int k = 0; k < 12; ++k) hi[k] = kR12Hi[k];
                if (c.p.version == 13) hi[10] = uint8_t(c.p.mma_max);
                for (int k = 0; k < 12; ++k) { cnt[k] = kR12Lo[k] + cs[k] % (hi[k] - kR12Lo[k] + 1); sum += cnt[k]; }
                int32_t ga = 32 - int32_t(sum);
                if (ga > int32_t(kR12GatherHi)) {
                    uint32_t over = uint32_t(ga - int32_t(kR12GatherHi)); ga = kR12GatherHi;
                    for (int k = 0; k < 12 && over; ++k) { const uint32_t room = hi[k] - cnt[k]; const uint32_t g = room < over ? room : over; cnt[k] += g; over -= g; }
                } else if (ga < int32_t(kR12GatherLo)) {
                    uint32_t deficit = uint32_t(int32_t(kR12GatherLo) - ga); ga = kR12GatherLo;
                    for (int k = 0; k < 12 && deficit; ++k) { const uint32_t room = cnt[k] - kR12Lo[k]; const uint32_t g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; }
                }
                uint32_t pos = 0; for (int32_t m = 0; m < ga; ++m) c.r0e_row[pos++] = R0E_GATHER;
                for (int k = 0; k < 12; ++k) for (uint32_t m = 0; m < cnt[k]; ++m) c.r0e_row[pos++] = kR12DrawOrder[k];
                const uint32_t items = kR12ItemLo + cs[12] % (kR12ItemHi - kR12ItemLo + 1);
                const uint32_t fpint = cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[5] + cnt[6] + cnt[11];
                c.r0e_items = items < fpint ? items : fpint;
            }
            if ((i & 31) == 0 && c.p.version == 11) {
                // the tile's composition seed: tile 0 from the period seed, tile w from BLAKE3 of tile w-1's seed (the chain)
                if (i == 0) { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                else { bytes comp; comp.tag("IgneumHash/R0E/composition").lp(c.r0e_tile_seed, 32); b3_hash(comp, c.r0e_tile_seed, 32); }
                uint8_t cs[16]; { bytes cb; cb.tag("IgneumHash/R0E/tile").lp(c.r0e_tile_seed, 32); b3_hash(cb, cs, sizeof cs); }
                uint32_t cnt[10]; uint32_t sum = 0;
                for (int k = 0; k < 10; ++k) { cnt[k] = kR0eLo[k] + cs[k] % (kR0eHi[k] - kR0eLo[k] + 1); sum += cnt[k]; }
                int32_t fma = 32 - int32_t(sum);
                if (fma > int32_t(kR0eFmaHi)) {
                    uint32_t over = uint32_t(fma - int32_t(kR0eFmaHi)); fma = kR0eFmaHi;
                    for (int k = 0; k < 10 && over; ++k) { const uint32_t room = kR0eHi[k] - cnt[k]; const uint32_t g = room < over ? room : over; cnt[k] += g; over -= g; }
                } else if (fma < int32_t(kR0eFmaLo)) {
                    uint32_t deficit = uint32_t(int32_t(kR0eFmaLo) - fma); fma = kR0eFmaLo;
                    for (int k = 0; k < 10 && deficit; ++k) { const uint32_t room = cnt[k] - kR0eLo[k]; const uint32_t g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; }
                }
                uint32_t pos = 0; for (int32_t m = 0; m < fma; ++m) c.r0e_row[pos++] = R0D_FMA;
                for (int k = 0; k < 10; ++k) for (uint32_t m = 0; m < cnt[k]; ++m) c.r0e_row[pos++] = kR0eDrawOrder[k];
                const uint32_t items = kR0eItemLo + cs[10] % (kR0eItemHi - kR0eItemLo + 1);
                const uint32_t fpint = uint32_t(fma) + cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[5] + cnt[6];
                c.r0e_items = items < fpint ? items : fpint;
            }
            const uint8_t* row = c.p.version >= 11 ? c.r0e_row : kR0eWindow;
            const uint32_t n_items = c.p.version >= 11 ? c.r0e_items : kR0eItemOps;
            if ((i & 31) == 0) {
                uint8_t wq[64]; bytes win; win.tag("IgneumHash/R0E/window").lp(seed, 32).le32(i >> 5); b3_hash(win, wq, 64);
                uint8_t perm[32]; for (int k = 0; k < 32; ++k) perm[k] = row[k];
                for (int k = 31; k > 0; --k) { const int j = wq[k] % (k + 1); std::swap(perm[k], perm[j]); }
                uint8_t flags[32] = {}; uint32_t placed = 0;
                for (int k = 0; k < 32 && placed < n_items; ++k) { const int j = (k + wq[32 + k]) % 32; if (perm[j] < R0E_SHFL && !flags[j]) { flags[j] = 1; ++placed; } }
                for (int k = 0; k < 32; ++k) c.r0e_window[k] = uint8_t(perm[k] | (flags[k] ? kR0eItemFlag : 0));
            }
            x.opcode = c.r0e_window[i & 31];
        } else
        x.opcode = mix128 ? r0d_opcode_mix128(q[0]) : r0d_opcode(q[0]);
        x.dst = uint8_t(q[1] & 15); x.src1 = uint8_t(q[2] & 15); x.src2 = uint8_t(q[3] & 15);
        if (D) {
            // each register read or written was last written at least D operations back: the drawn register, else the next
            // eligible one upward (mod 16); with 16 registers and D at most 8 at least 8 are always eligible
            auto settle = [&](uint8_t r) { for (int k = 0; k < 16; ++k) { const uint8_t s = uint8_t((r + k) & 15); if (int64_t(i) - last_write[s] >= int64_t(D)) return s; } return r; };
            x.src1 = settle(x.src1); x.src2 = settle(x.src2); x.dst = settle(x.dst);
            last_write[x.dst] = int64_t(i);
        }
        x.imm = uint32_t(q[4]) | (uint32_t(q[5]) << 8) | (uint32_t(q[6]) << 16) | (uint32_t(q[7]) << 24);
        out[i] = x;
    }
}
inline uint64_t ih_map_idx(const ih_ctx& c, uint32_t x) {
    const uint64_t n_items = c.n_items();
    const bool fastrange = c.p.version == 5 || (c.p.version >= 8 && !is_pow2(c.p.dataset_bytes));
    return fastrange ? ((uint64_t(x) * n_items) >> 32) : (uint64_t(x) & (n_items - 1));
}
// version 12's SHARED table (SPEC-R0E-v0 section 2c, the shared form): shared_kib x 256 words per (period, anchor), filled once by the
// chained scatter over the dataset from the period's shared seed (no registers: the table is the period's, every nonce reads it at its
// own row offset), cached like the programme
std::shared_ptr<const std::vector<uint32_t>> r12_shared_table(const ih_ctx& c, uint64_t height, const uint8_t* anchor = nullptr) {
    const uint64_t period = height / c.p.period_blocks;
    if (!anchor) anchor = c.p.program_anchor;
    std::lock_guard<std::mutex> g(c.prog_mu);
    for (const auto& sl : c.shared_slots) if (sl.tab && sl.period == period && std::memcmp(sl.anchor, anchor, 32) == 0) return sl.tab;
    uint8_t seed[32];
    { bytes in; in.tag("IgneumHash/R0E/shared").le64(c.p.chain_id).le64(period).raw(anchor, 32); b3_hash(in, seed, 32); }
    const uint32_t words = c.p.shared_kib * 256u, wpi = c.item_bytes() / 4, reads = words / wpi;
    auto v = std::make_shared<std::vector<uint32_t>>(words);
    std::vector<uint32_t> item(wpi);
    uint32_t x = 0; for (uint32_t w = 0; w < 8; ++w) { const uint32_t t = le32(seed + 4 * w); x = rotl32(x ^ t, 5) + t; }
    for (uint32_t k = 0; k < reads; ++k) {
        load_item(c, ih_map_idx(c, x), item.data());
        uint32_t fold = 0;
        for (uint32_t w = 0; w < wpi; ++w) { fold = rotl32(fold ^ item[w], 5) + item[w]; (*v)[k * wpi + w] = item[w]; }
        x = fold ^ le32(seed + 4 * ((k + 1) & 7)) ^ (k + 1);
    }
    auto& sl = c.shared_slots[c.shared_next]; c.shared_next = (c.shared_next + 1) % 4;
    sl.tab = v; sl.period = period; std::memcpy(sl.anchor, anchor, 32);
    return sl.tab;
}
// the drawn programme for the height's period: shared ownership, so a replacement (the period moving on, or a slot evicted by the
// anchored calls' round-robin) builds a NEW vector and the threads still reading the old one keep it alive (the race the protocol team read on
// the lab nodes: an eviction regenerating into a vector another thread's work() was reading)
std::shared_ptr<const std::vector<ih_ctx::r0d_op>> r0d_program(const ih_ctx& c, uint64_t height, const uint8_t* anchor = nullptr) {
    const uint64_t period = height / c.p.period_blocks;
    std::lock_guard<std::mutex> g(c.prog_mu);
    if (!anchor) {
        if (!c.prog || c.prog_period != period) { auto v = std::make_shared<std::vector<ih_ctx::r0d_op>>(); r0d_generate(c, period, *v); c.prog = v; c.prog_period = period; }
        return c.prog;
    }
    for (const auto& sl : c.prog_slots) if (sl.prog && sl.period == period && std::memcmp(sl.anchor, anchor, 32) == 0) return sl.prog;
    auto& sl = c.prog_slots[c.prog_next]; c.prog_next = (c.prog_next + 1) % 8;
    auto v = std::make_shared<std::vector<ih_ctx::r0d_op>>(); r0d_generate(c, period, *v, anchor);
    sl.prog = v; sl.period = period; std::memcpy(sl.anchor, anchor, 32);
    return sl.prog;
}
inline uint32_t r0d_bits(float f) { uint32_t b; std::memcpy(&b, &f, 4); return b; }
// one operation on a lane's 16 registers (section 4); the FP operands masked to positive normals, the fused form one rounding
// store_masked (version 11, the project's ruling of 20:4x on the GPU team's 4090 reading): the FP result is STORED through the range mask, so a
// register last written by an FP operation already holds a masked value and the read mask is idempotent on it; a kernel elides
// the read mask where the source's last writer in the straight-line programme was FMA or FADD (or the window-end mask) and
// applies it where the writer was an integer operation, a gather, a shuffle of such, an item word or the initial state; the
// reference keeps the read mask everywhere, which is the same relation. Versions 9 and 10 store the raw result.
inline uint32_t r0d_store_mask(uint32_t w) { return (w & 0x3FFFFFFFu) | 0x20000000u; }
inline void r0d_exec(uint32_t* R, const ih_ctx::r0d_op& x, bool store_masked = false) {
    const uint32_t a = R[x.src1], b = R[x.src2], d = R[x.dst];
    switch (x.opcode) {
        case R0D_FMA: { const uint32_t v = r0d_bits(std::fma(v8mask(a), v8mask(b), v8mask(d))); R[x.dst] = store_masked ? r0d_store_mask(v) : v; break; }
        case R0D_FADD: { const uint32_t v = r0d_bits(v8mask(a) + v8mask(b)); R[x.dst] = store_masked ? r0d_store_mask(v) : v; break; }
        case R0D_IXOR: R[x.dst] = a ^ b; break;
        case R0D_IROTL: R[x.dst] = rotl32(a, x.imm & 31); break;
        case R0D_IADD: R[x.dst] = a + b; break;
        case R0D_IMULHI: R[x.dst] = uint32_t((uint64_t(a) * uint64_t(b)) >> 32); break;
        case R0D_POPC: R[x.dst] = a ^ (uint32_t(__builtin_popcount(b)) * 0x9E3779B1u); break;
        default: R[x.dst] = a + (uint32_t(b ? __builtin_clz(b) : 32) * 0x85EBCA77u); break;
    }
}
// the programme on one lane: PROGRAM_OPS operations, the range mask on the 16 registers after every 32
inline void r0d_run_lane(uint32_t* R, const std::vector<ih_ctx::r0d_op>& prog) {
    const size_t n = prog.size();
    for (size_t i = 0; i < n; ++i) {
        r0d_exec(R, prog[i]);
        if ((i & 31) == 31) for (int k = 0; k < 16; ++k) R[k] = (R[k] & 0x3FFFFFFFu) | 0x20000000u;
    }
}

// version 10 (SPEC-R0E-v0.md section 2): the programme over the lane OCTET in lock-step (the same operation on every lane at
// each step; the shuffle reads another lane's register, the tile transposes the octet, the gather reads the per-nonce table,
// the item flag takes the second source from the fetched item); the range mask on every lane's FP registers at a window's end
// the octet runner in its SIMD-8 form (SPEC-R0E-v0 section 1, the SIMD reference): the eight lanes are the vector's eight
// elements, the sixteen programme registers eight-wide (V[k][l] = R[l][k]); every lock-step operation is one loop of eight the
// compiler vectorises (IGNPOW_NATIVE adds -march=native for the verifier's timing row; the bits are the same either way: the
// FMA is std::fma per lane, exact); the same relation as the lane-major form it replaces, checked by the sets of record
// version 14's 16x8x32 tile on the vector units (the project's order of 22:3x: the node team's scalar row read 54 ms a header at 128): the
// exact INT8 x INT8 dot into INT32 with vpdpbusd (AVX-512 VNNI or AVX-VNNI; the instruction is unsigned x signed, so A's bytes are
// offset by 128 and the row's sum of B corrects it, exact) or NEON sdot (signed x signed); the scalar form in r0e_run_octet is the
// consensus reference and the self-test and the sets check this one against it bit for bit. Returns false where no unit is built.
#if defined(__AVX2__)
#include <immintrin.h>
#endif
#if defined(__AVX512VNNI__) && defined(__AVX512BW__)
// the streamed form (the project's order of 22:5x on the node team's split: the gathered form ran at 4 GMAC a second against the unit's hundred):
// the tile as a proper 16x32 by 32x8 INT8 product on zmm. A is read once per tile as eight zmm, one per 4-byte group g: the sixteen
// rows are the lanes (rows 0..7 from register g, rows 8..15 from register 8 + g, each a ymm of V rotated across the octet by rot and
// XORed with 0x80808080, the +128 offset); B's 32 x 8 bytes come from the item in place: for group g the eight dwords Bword[g][n]
// (bytes B[4 g .. 4 g + 3][n]) are one byte permutation (vpermb) of the 32 bytes at word off + 8 g of the doubled item; the column sums
// are hoisted (eight dpbusd against ones); per column n, eight dpbusd accumulate the sixteen rows against the broadcast Bword[g][n];
// the correction is subtracted and the two halves of the column XOR straight into V's rows (dbase + n) and (dbase + 8 + n), so C never
// lands in memory. 64 dpbusd and 8 vpermb per 4,096-MAC tile. Without VBMI the Bwords are built by a byte loop, the rest the same.
inline bool r14_tile_vec(uint32_t V[16][8], const uint32_t A[16][8], const uint32_t* Wd, uint32_t off, uint32_t rot, uint32_t dbase, uint32_t lay = 0) {
    const bool orient = (lay & 1) != 0, accum_add = (lay & 2) != 0, b_unsigned = (lay & 4) != 0;
    static const __m256i kRev[8] = {  // the reversed lanes rotated: lane l takes lane (7 - l + rot) mod 8
        _mm256_setr_epi32(7, 6, 5, 4, 3, 2, 1, 0), _mm256_setr_epi32(0, 7, 6, 5, 4, 3, 2, 1), _mm256_setr_epi32(1, 0, 7, 6, 5, 4, 3, 2), _mm256_setr_epi32(2, 1, 0, 7, 6, 5, 4, 3),
        _mm256_setr_epi32(3, 2, 1, 0, 7, 6, 5, 4), _mm256_setr_epi32(4, 3, 2, 1, 0, 7, 6, 5), _mm256_setr_epi32(5, 4, 3, 2, 1, 0, 7, 6), _mm256_setr_epi32(6, 5, 4, 3, 2, 1, 0, 7) };
    static const __m256i kRot[8] = {
        _mm256_setr_epi32(0, 1, 2, 3, 4, 5, 6, 7), _mm256_setr_epi32(1, 2, 3, 4, 5, 6, 7, 0), _mm256_setr_epi32(2, 3, 4, 5, 6, 7, 0, 1), _mm256_setr_epi32(3, 4, 5, 6, 7, 0, 1, 2),
        _mm256_setr_epi32(4, 5, 6, 7, 0, 1, 2, 3), _mm256_setr_epi32(5, 6, 7, 0, 1, 2, 3, 4), _mm256_setr_epi32(6, 7, 0, 1, 2, 3, 4, 5), _mm256_setr_epi32(7, 0, 1, 2, 3, 4, 5, 6) };
    // the integer format: B signed (dpbusd's unsigned side is A + 128, corrected by the column sums) or B unsigned (A signed on
    // dpbusd's signed side directly, no correction); the orientation reverses A's groups and lanes
    const __m256i x80 = _mm256_set1_epi32(int(b_unsigned ? 0u : 0x80808080u));
    const __m256i* rotv = orient ? kRev : kRot;
    __m512i Ag[8];
    for (uint32_t g = 0; g < 8; ++g) {
        const uint32_t gs = orient ? 7 - g : g;
        const __m256i lo = _mm256_xor_si256(_mm256_permutevar8x32_epi32(_mm256_loadu_si256(reinterpret_cast<const __m256i*>(A[gs])), rotv[rot]), x80);
        const __m256i hi = _mm256_xor_si256(_mm256_permutevar8x32_epi32(_mm256_loadu_si256(reinterpret_cast<const __m256i*>(A[8 + gs])), rotv[rot]), x80);
        Ag[g] = _mm512_inserti64x4(_mm512_castsi256_si512(lo), hi, 1);
    }
    alignas(32) uint32_t Bw[8][8];  // Bword[g][n]
#if defined(__AVX512VBMI__)
    // output byte p (0..31): dword n = p >> 2, byte j = p & 3 (the k offset 4 g + j) from input byte (2 j + (n >> 2)) * 4 + (n & 3)
    static const __m512i kPerm = [] { alignas(64) uint8_t idx[64]; for (int p = 0; p < 64; ++p) { const int n = (p >> 2) & 7, j = p & 3; idx[p] = uint8_t((2 * j + (n >> 2)) * 4 + (n & 3)); } return _mm512_load_si512(reinterpret_cast<const void*>(idx)); }();
    for (uint32_t g = 0; g < 8; ++g) {
        const __m512i in = _mm512_castsi256_si512(_mm256_loadu_si256(reinterpret_cast<const __m256i*>(Wd + off + 8 * g)));
        _mm256_store_si256(reinterpret_cast<__m256i*>(Bw[g]), _mm512_castsi512_si256(_mm512_permutexvar_epi8(kPerm, in)));
    }
#else
    const uint8_t* Bb = reinterpret_cast<const uint8_t*>(Wd + off);
    for (uint32_t g = 0; g < 8; ++g) for (uint32_t n = 0; n < 8; ++n) { uint32_t w = 0; for (uint32_t j = 0; j < 4; ++j) w |= uint32_t(Bb[(4 * g + j) * 8 + n]) << (8 * j); Bw[g][n] = w; }
#endif
    const __m256i ones = _mm256_set1_epi32(0x01010101);
    __m256i sb = _mm256_setzero_si256();
    for (uint32_t g = 0; g < 8; ++g) sb = _mm256_dpbusd_epi32(sb, ones, _mm256_load_si256(reinterpret_cast<const __m256i*>(Bw[g])));
    alignas(32) int32_t sumB[8]; _mm256_store_si256(reinterpret_cast<__m256i*>(sumB), sb);
    for (uint32_t n = 0; n < 8; ++n) {
        __m512i acc = _mm512_setzero_si512();
        if (b_unsigned) {
            // dpbusd(u8 = B, s8 = A): the roles swapped, exact with no correction
            for (uint32_t g = 0; g < 8; ++g) acc = _mm512_dpbusd_epi32(acc, _mm512_set1_epi32(int(Bw[g][n])), Ag[g]);
        } else {
            for (uint32_t g = 0; g < 8; ++g) acc = _mm512_dpbusd_epi32(acc, Ag[g], _mm512_set1_epi32(int(Bw[g][n])));
            acc = _mm512_sub_epi32(acc, _mm512_set1_epi32(sumB[n] * 128));
        }
        uint32_t* lo = V[(dbase + n) & 15]; uint32_t* hi = V[(dbase + 8 + n) & 15];
        const __m256i l0 = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(lo)), h0 = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(hi));
        const __m256i cl = _mm512_castsi512_si256(acc), ch = _mm512_extracti64x4_epi64(acc, 1);
        _mm256_storeu_si256(reinterpret_cast<__m256i*>(lo), accum_add ? _mm256_add_epi32(l0, cl) : _mm256_xor_si256(l0, cl));
        _mm256_storeu_si256(reinterpret_cast<__m256i*>(hi), accum_add ? _mm256_add_epi32(h0, ch) : _mm256_xor_si256(h0, ch));
    }
    return true;
}
// version 14.1's fragment layout on the vector units (the scalar block in r0e_run_octet is the consensus reference): A's sixteen
// rows are the octet's lanes in place (row l from register 4 (g mod 4) + 2 (g div 4), row l + 8 from the register one above it: no
// lane permute at all), B's column n is the item's 32 bytes at word off + 8 ((n + rot) mod 8) (reversed as a word-reversed,
// byte-swapped load under the orientation bit), and C's two halves land in the quad's registers 4 (n div 2) + ((n mod 2) + dbase)
// and + 2 modulo 4. 64 dpbusd per 4,096-MAC tile, no permutation of A or C.
inline bool r14_tile_vec_frag(uint32_t V[16][8], const uint32_t A[16][8], const uint32_t* Wd, uint32_t off, uint32_t rot, uint32_t dbase, uint32_t lay) {
    const bool orient = (lay & 1) != 0, accum_add = (lay & 2) != 0, b_unsigned = (lay & 4) != 0;
    const __m256i x80 = _mm256_set1_epi32(int(b_unsigned ? 0u : 0x80808080u));
    __m512i Ag[8];
    for (uint32_t g = 0; g < 8; ++g) {
        const uint32_t j = 4 * (g & 3) + 2 * (g >> 2);
        const __m256i lo = _mm256_xor_si256(_mm256_loadu_si256(reinterpret_cast<const __m256i*>(A[j])), x80);
        const __m256i hi = _mm256_xor_si256(_mm256_loadu_si256(reinterpret_cast<const __m256i*>(A[j + 1])), x80);
        Ag[g] = _mm512_inserti64x4(_mm512_castsi256_si512(lo), hi, 1);
    }
    alignas(32) uint32_t Bw[8][8];  // Bword[g][n]: the k bytes 4 g .. 4 g + 3 of column n
    for (uint32_t n = 0; n < 8; ++n) {
        const uint32_t* col = Wd + off + 8 * ((n + rot) & 7);
        for (uint32_t g = 0; g < 8; ++g) Bw[g][n] = orient ? __builtin_bswap32(col[7 - g]) : col[g];
    }
    const __m256i ones = _mm256_set1_epi32(0x01010101);
    __m256i sb = _mm256_setzero_si256();
    for (uint32_t g = 0; g < 8; ++g) sb = _mm256_dpbusd_epi32(sb, ones, _mm256_load_si256(reinterpret_cast<const __m256i*>(Bw[g])));
    alignas(32) int32_t sumB[8]; _mm256_store_si256(reinterpret_cast<__m256i*>(sumB), sb);
    for (uint32_t n = 0; n < 8; ++n) {
        __m512i acc = _mm512_setzero_si512();
        if (b_unsigned) { for (uint32_t g = 0; g < 8; ++g) acc = _mm512_dpbusd_epi32(acc, _mm512_set1_epi32(int(Bw[g][n])), Ag[g]); }
        else { for (uint32_t g = 0; g < 8; ++g) acc = _mm512_dpbusd_epi32(acc, Ag[g], _mm512_set1_epi32(int(Bw[g][n]))); acc = _mm512_sub_epi32(acc, _mm512_set1_epi32(sumB[n] * 128)); }
        uint32_t* lo = V[4 * (n >> 1) + (((n & 1) + dbase) & 3)]; uint32_t* hi = V[4 * (n >> 1) + ((2 + (n & 1) + dbase) & 3)];
        const __m256i l0 = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(lo)), h0 = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(hi));
        const __m256i cl = _mm512_castsi512_si256(acc), ch = _mm512_extracti64x4_epi64(acc, 1);
        _mm256_storeu_si256(reinterpret_cast<__m256i*>(lo), accum_add ? _mm256_add_epi32(l0, cl) : _mm256_xor_si256(l0, cl));
        _mm256_storeu_si256(reinterpret_cast<__m256i*>(hi), accum_add ? _mm256_add_epi32(h0, ch) : _mm256_xor_si256(h0, ch));
    }
    return true;
}
#define R14_TILE_VEC_APPLIES 1
#elif defined(__ARM_FEATURE_DOTPROD)
#include <arm_neon.h>
inline bool r14_tile_vec_frag(uint32_t[16][8], const uint32_t[16][8], const uint32_t*, uint32_t, uint32_t, uint32_t, uint32_t) { return false; }
inline bool r14_tile_vec(uint32_t V[16][8], const uint32_t A[16][8], const uint32_t* Wd, uint32_t off, uint32_t rot, uint32_t dbase, uint32_t lay = 0) {
    if (lay) return false;  // the drawn layouts take the scalar form on NEON tonight (exact; the vector form's layouts follow)
    const uint8_t* Bb = reinterpret_cast<const uint8_t*>(Wd + off);
    alignas(16) int8_t Bcol[8][32]; for (uint32_t k = 0; k < 32; ++k) for (uint32_t n = 0; n < 8; ++n) Bcol[n][k] = int8_t(Bb[k * 8 + n]);
    int32_t Cm[16][8];
    for (uint32_t m = 0; m < 16; ++m) {
        const uint32_t lane = ((m & 7) + rot) & 7, r0 = (m >> 3) * 8;
        alignas(16) uint32_t Aw[8]; for (uint32_t g = 0; g < 8; ++g) Aw[g] = A[(r0 + g) & 15][lane];
        const int8x16_t a0 = vreinterpretq_s8_u32(vld1q_u32(Aw)), a1 = vreinterpretq_s8_u32(vld1q_u32(Aw + 4));
        for (uint32_t n = 0; n < 8; ++n) { int32x4_t acc = vdotq_s32(vdupq_n_s32(0), a0, vld1q_s8(Bcol[n])); acc = vdotq_s32(acc, a1, vld1q_s8(Bcol[n] + 16)); Cm[m][n] = vaddvq_s32(acc); }
    }
    for (uint32_t m = 0; m < 16; ++m) for (uint32_t n = 0; n < 8; ++n) V[(dbase + (m >> 3) * 8 + n) & 15][m & 7] ^= uint32_t(Cm[m][n]);
    return true;
}
#define R14_TILE_VEC_APPLIES 1
#else
inline bool r14_tile_vec(uint32_t[16][8], const uint32_t[16][8], const uint32_t*, uint32_t, uint32_t, uint32_t, uint32_t = 0) { return false; }
inline bool r14_tile_vec_frag(uint32_t[16][8], const uint32_t[16][8], const uint32_t*, uint32_t, uint32_t, uint32_t, uint32_t) { return false; }
#endif

// the live file's fold (registers_live above 16): the sixteen programme registers exchanged with sixteen file words at the
// data-dependent offset woff (a multiple of 16 in 16 .. L - 1), then the file beyond the registers rotated across the octet by a
// per-word nibble of the topology word (as the programme registers are); the exchange makes every file word live within L / 16 folds
inline void r14_file_fold(uint32_t V[16][8], uint32_t* F, uint32_t L, uint32_t topo) {
    const uint32_t woff = 16 + (((topo >> 20) * 16) & (L - 1 - 15));
    for (uint32_t k = 0; k < 16; ++k) for (uint32_t l = 0; l < 8; ++l) { uint32_t* f = &F[l * L + ((woff + k) & (L - 1))]; const uint32_t t = *f; *f = V[k][l]; V[k][l] = t; }
    for (uint32_t k = 16; k < L; ++k) {
        const uint32_t sh = ((topo >> (2 * (k & 15))) + k) & 7;
        uint32_t X[8]; for (uint32_t l = 0; l < 8; ++l) X[l] = F[l * L + k];
        for (uint32_t l = 0; l < 8; ++l) F[l * L + k] = X[(l + sh) & 7];
    }
}

#ifdef IGNH_OPERAND_STATS
// the operand statistics of the registered form (the review lead's ask of 00:5x, 13 October): per tile, the byte histograms of A (by
// byte position within the word) and of B, and the fraction of A's bytes changed since the previous tile; a research build only
struct r14_stats_t { uint64_t tiles = 0; uint64_t hA[4][256] = {}; uint64_t hB[256] = {}; uint64_t changedA[4] = {}; uint64_t totalA[4] = {}; uint8_t prevA[16][8][4] = {}; bool have_prev = false; };
static r14_stats_t g_r14_stats;
extern "C" void ignh_operand_stats_dump(FILE* f) {
    auto H = [](const uint64_t* h, uint64_t n) { double e = 0; for (int i = 0; i < 256; ++i) if (h[i]) { const double p = double(h[i]) / double(n); e -= p * std::log2(p); } return e; };
    std::fprintf(f, "{\"tiles\": %llu, \"B_entropy_bits_per_byte\": %.4f, \"A_entropy_bits_per_byte_by_position\": [", (unsigned long long)g_r14_stats.tiles, H(g_r14_stats.hB, g_r14_stats.tiles * 256));
    for (int b = 0; b < 4; ++b) std::fprintf(f, "%s%.4f", b ? ", " : "", H(g_r14_stats.hA[b], g_r14_stats.totalA[b]));
    std::fprintf(f, "], \"A_fraction_changed_since_previous_tile_by_position\": [");
    for (int b = 0; b < 4; ++b) std::fprintf(f, "%s%.4f", b ? ", " : "", g_r14_stats.totalA[b] ? double(g_r14_stats.changedA[b]) / double(g_r14_stats.totalA[b]) : 0.0);
    std::fprintf(f, "]}\n");
}
#endif
// version 12's row access (access_bytes 4, 8 or 16 = 1, 2 or 4 words per gather or scatter): the row's words consumed by the MINIMAL
// glue (the review lead's ruling of 21:3x: the card runs about three instructions per 16-byte word or the form misses the line): the
// row's XOR, two LOP3 on the card for four words, three XORs on a chip; the whole row still reaches the next address
inline uint32_t r12_fold_row(const uint32_t* T, uint32_t row, uint32_t aw) {
    uint32_t v = T[row * aw];
    for (uint32_t i = 1; i < aw; ++i) v ^= T[row * aw + i];
    return v;
}
inline void r0e_run_octet(uint32_t R[8][64], const std::vector<ih_ctx::r0d_op>& prog, const uint32_t* W, uint32_t n_words, uint32_t* T, uint32_t t_words = kR0eTableWords, bool store_masked = false, uint32_t aw = 0, const uint32_t* ST = nullptr, uint32_t st_words = 0, uint32_t nonce_off = 0, uint32_t kch = 1, bool lane_bank = false, uint32_t tshape = 0, uint32_t fold_every = 1, uint32_t n_items_round = 1, uint32_t* F = nullptr, uint32_t L = 16, bool layout_draw = false, uint32_t tile_layout = 0) {
    // registers_live (the project's order of 23:5x, the security review's reading: the specialist under 2x only when every tile operand passes through
    // a GPU-sized register file): F is the lane-major live file, 8 lanes x L words, whose first 16 words per lane ARE the programme
    // registers (kept in V here and written back at the end); under L above 16 each tile's A rows are read from the file at a
    // data-dependent base from the previous accumulator (so no operand bypasses the file), and every fold also exchanges the sixteen
    // programme registers with sixteen file words at a data-dependent offset and rotates the whole file across the octet
    const bool v12 = aw != 0;
    const uint32_t n_rows = v12 ? t_words / aw : 0;
    const uint32_t st_rows = ST ? st_words / aw : 0;  // the shared form: GATHER reads the period's shared table at the nonce's row offset; SCATTER writes the per-nonce table
    // version 13's k independent access chains per nonce (chains_per_nonce; the GPU team's verdict cell of 21:36: one dependent chain per nonce
    // leaves the card's port a third full): access operation number j of the programme belongs to chain j mod k; each chain has its own
    // fold value per lane and its own DISJOINT block of rows (block c = rows c x rows/k .. (c + 1) x rows/k - 1) in both tables, so a card
    // and a chip pipeline the k chains alike; the chains' values combine at the window's end into registers 0..k-1
    const uint32_t cb = v12 ? n_rows / kch : 0, scb = ST ? st_rows / kch : 0;
    uint32_t acc_n = 0;
    // version 14's topology word (the outside review's reading of 22:1x, random topology): the fold of every lane's product selects the NEXT
    // tile's arrangement (the row rotation across the lanes, the item's offset, the register the product lands in) and the scramble
    // between tiles is a data-driven shuffle of the sixteen registers across the octet (register k from lane (l + nibble) mod 8)
    uint32_t topo = 0, tiles_n = 0;
    // the round's items concatenated and doubled (items_per_round x 64 words), so a tile's B at any word offset is one contiguous read
    const uint32_t wn = 64 * n_items_round;
    alignas(64) uint32_t Wd[2 * 64 * 32];
    if (tshape) { std::memcpy(Wd, W, 4 * wn); std::memcpy(Wd + wn, W, 4 * wn); }  // fold_every (the project's order of 22:3x): the topology word, the next arrangement and the scramble are computed from the accumulator once every k tiles; every tile still consumes the item and carries its accumulator, so no tile is skippable and the chain stays serial
    // version 12's access chain (the security review's row-reuse defence): every gather and scatter address mixes the fold of the WHOLE
    // previous row the lane touched with the live register, so no access's row is predictable from part of the last data or from the
    // programme counter; the chain starts from the lane's first table word
    uint32_t AC[8][8]; if (v12) for (uint32_t c = 0; c < kch; ++c) for (int l = 0; l < 8; ++l) AC[c][l] = T[(c * 8 + l) % t_words];
    alignas(32) uint32_t V[16][8];
    auto fileword = [&](uint32_t idx, uint32_t lane) -> uint32_t { return idx < 16 ? V[idx][lane] : F[lane * L + idx]; };  // the live file's word (the first 16 the programme registers)
    for (int k = 0; k < 16; ++k) for (int l = 0; l < 8; ++l) V[k][l] = R[l][k];
    const size_t n = prog.size();
    alignas(32) uint32_t s2[8], out[8];
    for (size_t i = 0; i < n; ++i) {
        const ih_ctx::r0d_op& x = prog[i];
        const uint8_t op = uint8_t(x.opcode & ~kR0eItemFlag);
        if (op == R0E_TILE && v12) {
            // version 12's tile is the SKEW: register k rotated across the octet by k, R'[l][k] = R[(l + k) & 7][k] (sixteen shuffles,
            // no shared memory and no sync; the GPU team's version 11 reading put the full transpose at 59 percent of the card's cost)
            uint32_t X[16][8]; std::memcpy(X, V, sizeof X);
            for (int k = 0; k < 16; ++k) for (int l = 0; l < 8; ++l) V[k][l] = X[k][(l + k) & 7];
        } else if (op == R0E_GATHER && v12) {
            // the row read at a register-drawn row, the row's words consumed by the fold
            // the glue per access on the card: one 128-bit load, two LOP3 (the row's XOR), one AND (the row mask), one XOR into the
            // destination; the address itself is the chain XOR the register (one LOP3 with the immediate): about five instructions
            const uint32_t c = acc_n++ % kch; uint32_t* A = AC[c];
            // gather_lane_bank (version 13; the GPU team's bank-conflict reading of 21:43): the row's low three bits are the lane's, the high bits
            // data-drawn, so the eight lanes of an octet read eight distinct bank groups (a chip's bank is per lane: it costs it nothing)
            if (ST) { for (int l = 0; l < 8; ++l) { uint32_t r = ((V[x.src2][l] ^ x.imm ^ A[l]) + nonce_off) & (scb - 1); if (lane_bank) r = (r & ~7u) | uint32_t(l); const uint32_t v = r12_fold_row(ST, c * scb + r, aw); A[l] = v; out[l] = V[x.dst][l] ^ v; } }
            else { for (int l = 0; l < 8; ++l) { uint32_t r = (V[x.src2][l] ^ x.imm ^ A[l]) & (cb - 1); if (lane_bank) r = (r & ~7u) | uint32_t(l); const uint32_t v = r12_fold_row(T, c * cb + r, aw); A[l] = v; out[l] = V[x.dst][l] ^ v; } }
            for (int l = 0; l < 8; ++l) V[x.dst][l] = out[l];
        } else if (op == R0E_SCATTER) {
            // the data-dependent row write: lane l owns the rows congruent to l mod 8, so the eight lanes never write one row in one
            // operation and the banks are conflict-free by construction; every word of the row is rewritten from the lane's registers
            // and the old row; the row's new fold returns to the lane
            const uint32_t c = acc_n++ % kch; uint32_t* A = AC[c];
            for (int l = 0; l < 8; ++l) {
                // the row within the chain's block is data-drawn (the high bits from the register and the chain), the residue the lane's;
                // the glue per scatter on the card: the row from four registers XOR the destination (four LOP3), one 128-bit store, the
                // chain from the written words (two LOP3); no load of the old row
                const uint32_t row = c * cb + ((((V[x.src2][l] ^ x.imm ^ A[l]) & (cb - 1)) & ~7u) | uint32_t(l));
                for (uint32_t i = 0; i < aw; ++i) T[row * aw + i] = V[(x.src1 + i) & 15][l] ^ V[x.dst][l];
                out[l] = r12_fold_row(T, row, aw); A[l] = out[l];
            }
            for (int l = 0; l < 8; ++l) V[x.dst][l] = out[l];
        } else if (op == R0E_MMA && tshape) {
            // version 14 (section 2e, proof of matrix work): the exact INT8 tile at the card's native shape. A is the octet's own state
            // read as M rows of K signed bytes: row m lives in lane ((m & 7) + src1) & 7 (src1 rotates the rows across the lanes, the drawn
            // graph), registers (m >> 3) * 8 .. + K/4 - 1, little-endian bytes; B is the fetched ITEM's 256 bytes read as K x N signed bytes
            // from word offset (R[src2] of lane (imm & 7) XOR imm) & 63, so the dataset feeds every tile; C (M x N exact INT32) is XORed
            // into the lanes' registers: C[m][n] into lane (m & 7)'s register (dst + (m >> 3) * N + n) & 15 (the 16x16x16 shape folds
            // its two N halves, C[m][n] XOR C[m][n + 8], so M x N/2 = 128 words fill the sixteen registers as 16x8x32's 128 do)
            const uint32_t M = tshape == 3 ? 8 : 16, N = tshape == 3 ? 8 : (tshape == 2 ? 16 : 8), K = tshape == 1 ? 32 : (tshape == 2 ? 16 : 8);
            const uint32_t off = (V[x.src2][x.imm & 7] ^ x.imm ^ topo) & (wn - 1);  // the window of 64 words across the round's items
            const uint32_t rot = (x.src1 + (topo >> 6)) & 7, dbase = (x.dst + (topo >> 9)) & 15;
            const uint32_t abase = L > 16 ? (((topo >> 12) & (L - 1)) & ~7u) : 0u;  // the A rows' base in the live file (a multiple of 8 words)
            // layout_draw (the security review's skip test LAYOUTS PRECOMPUTED): the tile's layout drawn from the previous tile's result inside the
            // registered set: the operand orientation (A's groups and lanes reversed), the accumulation route (XOR or ADD into the
            // registers), the integer format (B signed or unsigned); every layout exact on every vendor's shape
            const uint32_t lay = layout_draw ? (topo >> 24) & 7u : 0u;
            const bool orient = (lay & 1) != 0, accum_add = (lay & 2) != 0, b_unsigned = (lay & 4) != 0;
            int32_t Cm[16][16];
#ifdef IGNH_OPERAND_STATS
            if (tshape == 1) {
                ++g_r14_stats.tiles;
                for (uint32_t i = 0; i < 16; ++i) for (uint32_t l = 0; l < 8; ++l) { const uint32_t w = V[i][l]; for (int b = 0; b < 4; ++b) { const uint8_t by = uint8_t(w >> (8 * b)); ++g_r14_stats.hA[b][by]; ++g_r14_stats.totalA[b]; if (g_r14_stats.have_prev && g_r14_stats.prevA[i][l][b] != by) ++g_r14_stats.changedA[b]; g_r14_stats.prevA[i][l][b] = by; } }
                g_r14_stats.have_prev = true;
                for (uint32_t w = 0; w < 64; ++w) { const uint32_t v = Wd[w + off]; for (int b = 0; b < 4; ++b) ++g_r14_stats.hB[uint8_t(v >> (8 * b))]; }
            }
#endif
            alignas(32) uint32_t Af[16][8];  // the A rows' words when they come from the live file: Af[idx][lane]
            if (L > 16) for (uint32_t i = 0; i < 16; ++i) for (uint32_t l = 0; l < 8; ++l) Af[i][l] = fileword((abase + i) & (L - 1), l);
            const bool applied = tshape == 1 && (tile_layout == 0 ? r14_tile_vec(V, L > 16 ? Af : V, Wd, off, rot, dbase, lay) : r14_tile_vec_frag(V, L > 16 ? Af : V, Wd, off, rot, dbase, lay));  // the vector units' exact forms apply C themselves
            if (!applied && tile_layout == 1) {
                // version 14.1, the FRAGMENT layout (the project's order of 05:1x on the GPU team's rearrangement reading: the card's tile cost was
                // moving 128 A and 128 C words per octet into and out of the tensor core's fragment and back, every tile). The octet's
                // 8 lanes x 16 registers ARE the m16n8k32 fragment of a 32-thread warp: lane l's register 4q + j is fragment word j of
                // thread (l, q), so A row l and row l + 8 live in lane l (j & 1 the row half, j >> 1 the k half, q the k quad), B is
                // read as N rows of K bytes (lane l's column one 32-byte read), and C lands where the fragment leaves it (lane l's
                // registers 4q + j hold C[l + 8 (j >> 1)][2q + (j & 1)]). The row rotation and the orientation's lane reversal are pure
                // relabellings under this mapping (C follows A's lane), so the draw is re-expressed where the card pays nothing and a
                // chip still cannot fix it: rot rotates B's column per lane, the orientation bit reverses B's k order, the landing
                // register rotates within the thread's quad (dbase & 3; a landing across quads is a shuffle on the registered card),
                // the accumulation route and the integer format unchanged. Exact on any vendor's shape; a vendor whose fragment differs
                // pays the rearrangement the registered card paid before, which is the specialist chip's position too.
                // the whole product from the tile's INPUT state, then the landing (the fragment's semantics: A is read before C
                // is written; the registers alias, so the product is held apart)
                int32_t Cf[16][8];
                for (uint32_t m = 0; m < 16; ++m) {
                    const uint32_t lane = m & 7, hi = m >> 3;
                    for (uint32_t n = 0; n < 8; ++n) {
                        const uint32_t col = (n + rot) & 7;
                        int32_t t = 0;
                        for (uint32_t k = 0; k < 32; ++k) {
                            const uint32_t j = 4 * ((k >> 2) & 3) + hi + 2 * (k >> 4);
                            const int8_t a = int8_t(uint8_t((L > 16 ? Af[j][lane] : V[j][lane]) >> (8 * (k & 3))));
                            const uint32_t kk = orient ? 31 - k : k, bi = col * 32 + kk;  // the byte index into the item, N x K row-major
                            const uint8_t bb = uint8_t(Wd[(bi >> 2) + off] >> (8 * (bi & 3)));
                            t += int32_t(a) * (b_unsigned ? int32_t(bb) : int32_t(int8_t(bb)));
                        }
                        Cf[m][n] = t;
                    }
                }
                for (uint32_t m = 0; m < 16; ++m) for (uint32_t n = 0; n < 8; ++n) {
                    uint32_t& dst = V[4 * (n >> 1) + ((2 * (m >> 3) + (n & 1) + dbase) & 3)][m & 7];
                    if (accum_add) dst += uint32_t(Cf[m][n]); else dst ^= uint32_t(Cf[m][n]);
                }
            } else if (!applied)
            for (uint32_t m = 0; m < M; ++m) {
                const uint32_t lane = orient ? ((7 - (m & 7)) + rot) & 7 : ((m & 7) + rot) & 7, r0 = (m >> 3) * 8;
                for (uint32_t n = 0; n < N; ++n) {
                    int32_t t = 0;
                    for (uint32_t k = 0; k < K; ++k) {
                        const uint32_t g = orient ? (K / 4 - 1 - (k >> 2)) : (k >> 2);
                        const int8_t a = int8_t(uint8_t((L > 16 ? Af[(r0 + g) & 15][lane] : V[(r0 + g) & 15][lane]) >> (8 * (k & 3))));
                        const uint32_t bi = k * N + n;  // the byte index into the item, K x N row-major
                        const uint8_t bb = uint8_t(Wd[(bi >> 2) + off] >> (8 * (bi & 3)));
                        t += int32_t(a) * (b_unsigned ? int32_t(bb) : int32_t(int8_t(bb)));
                    }
                    Cm[m][n] = t;
                }
            }
            const uint32_t Nf = tshape == 2 ? 8 : N;
            if (!applied && tile_layout == 0) for (uint32_t m = 0; m < M; ++m) for (uint32_t n = 0; n < Nf; ++n) {
                const int32_t v = tshape == 2 ? (Cm[m][n] ^ Cm[m][n + 8]) : Cm[m][n];
                uint32_t& dst = V[(dbase + (m >> 3) * Nf + n) & 15][m & 7];
                if (accum_add) dst += uint32_t(v); else dst ^= uint32_t(v);
            }
            // the topology word from the product (the fold over every lane's landing register) and the scramble between tiles,
            // once every fold_every tiles
            if (++tiles_n % fold_every == 0) {
            { uint32_t t = 0; for (int l = 0; l < 8; ++l) { const uint32_t v = V[dbase][l]; t = rotl32(t ^ v, 5) + v; }
              t ^= t >> 16; t *= kMul; t ^= t >> 15; topo = t; }
#if defined(__AVX2__)
            { static const __m256i kRotS[8] = {
                _mm256_setr_epi32(0, 1, 2, 3, 4, 5, 6, 7), _mm256_setr_epi32(1, 2, 3, 4, 5, 6, 7, 0), _mm256_setr_epi32(2, 3, 4, 5, 6, 7, 0, 1), _mm256_setr_epi32(3, 4, 5, 6, 7, 0, 1, 2),
                _mm256_setr_epi32(4, 5, 6, 7, 0, 1, 2, 3), _mm256_setr_epi32(5, 6, 7, 0, 1, 2, 3, 4), _mm256_setr_epi32(6, 7, 0, 1, 2, 3, 4, 5), _mm256_setr_epi32(7, 0, 1, 2, 3, 4, 5, 6) };
              for (int k = 0; k < 16; ++k) { const uint32_t sh = ((topo >> (2 * k)) + uint32_t(k)) & 7; __m256i* row = reinterpret_cast<__m256i*>(V[k]); _mm256_storeu_si256(row, _mm256_permutevar8x32_epi32(_mm256_loadu_si256(row), kRotS[sh])); } }
            if (L > 16) r14_file_fold(V, F, L, topo);
#else
            { uint32_t X[16][8]; std::memcpy(X, V, sizeof X);
              for (int k = 0; k < 16; ++k) { const uint32_t sh = ((topo >> (2 * k)) + uint32_t(k)) & 7; for (int l = 0; l < 8; ++l) V[k][l] = X[k][(l + sh) & 7]; } }
            if (L > 16) r14_file_fold(V, F, L, topo);
#endif
            }
        } else if (op == R0E_MMA) {
            // the exact INT8 matrix tile, 8 x 8 x 8: a[l][j] the eight signed bytes of lane l's registers src1 and src1 + 1 (mod 16),
            // b[k][j] the eight signed bytes of lane k's registers src2 and src2 + 1 (mod 16); lane l's register dst + k (mod 16) for
            // k = 0..7 takes the INT32 dot product (exact: 8 products of at most 2^14 each), XORed into the old word
            int8_t a[8][8], b[8][8];
            for (int l = 0; l < 8; ++l) for (int j = 0; j < 8; ++j) {
                const uint32_t wa = V[(x.src1 + (j >> 2)) & 15][l], wb = V[(x.src2 + (j >> 2)) & 15][l];
                a[l][j] = int8_t(uint8_t(wa >> (8 * (j & 3)))); b[l][j] = int8_t(uint8_t(wb >> (8 * (j & 3))));
            }
            int32_t acc[8][8];
            for (int l = 0; l < 8; ++l) for (int k = 0; k < 8; ++k) { int32_t t = 0; for (int j = 0; j < 8; ++j) t += int32_t(a[l][j]) * int32_t(b[k][j]); acc[l][k] = t; }
            for (int k = 0; k < 8; ++k) for (int l = 0; l < 8; ++l) V[(x.dst + k) & 15][l] ^= uint32_t(acc[l][k]);
        } else if (op == R0E_TILE) {
            // the transpose: lane l's register k TAKES lane (k & 7)'s register ((k >> 3) + 2 l), R'[l][k] = R[k & 7][(k >> 3) + 2 l]: a
            // bijection on the octet's 16 x 8 and not an involution (the inverse reading is a different map; this code governs)
            uint32_t X[16][8]; std::memcpy(X, V, sizeof X);
            for (int l = 0; l < 8; ++l) for (int k = 0; k < 16; ++k) V[k][l] = X[(k >> 3) + 2 * l][k & 7];
        } else if (op == R0E_SHFL) {
            const int off = 1 + int(x.imm % 7);
            for (int l = 0; l < 8; ++l) out[l] = V[x.src2][l] ^ V[x.src1][(l + off) & 7];
            for (int l = 0; l < 8; ++l) V[x.dst][l] = out[l];
        } else if (op == R0E_GATHER) {
            for (int l = 0; l < 8; ++l) out[l] = V[x.src1][l] ^ T[(V[x.src2][l] ^ x.imm) % t_words];
            for (int l = 0; l < 8; ++l) V[x.dst][l] = out[l];
        } else {
            // the second source: the register, or under the item flag the fetched item's word at a register-drawn index; the
            // item word stands in the src2 register for the operation, so src1 or the FMA addend naming that register read it too
            const bool item = (x.opcode & kR0eItemFlag) != 0;
            if (item) { for (int l = 0; l < 8; ++l) s2[l] = W[(V[x.src2][l] ^ x.imm) % n_words]; }
            else { for (int l = 0; l < 8; ++l) s2[l] = V[x.src2][l]; }
            const uint32_t* a = (item && x.src1 == x.src2) ? s2 : V[x.src1];
            const uint32_t* d = (item && x.dst == x.src2) ? s2 : V[x.dst];
            switch (op) {
                case R0D_FMA:
                    for (int l = 0; l < 8; ++l) { const uint32_t v = r0d_bits(std::fma(v8mask(a[l]), v8mask(s2[l]), v8mask(d[l]))); out[l] = store_masked ? r0d_store_mask(v) : v; }
                    break;
                case R0D_FADD:
                    for (int l = 0; l < 8; ++l) { const uint32_t v = r0d_bits(v8mask(a[l]) + v8mask(s2[l])); out[l] = store_masked ? r0d_store_mask(v) : v; }
                    break;
                case R0D_IXOR: for (int l = 0; l < 8; ++l) out[l] = a[l] ^ s2[l]; break;
                case R0D_IROTL: { const uint32_t r = x.imm & 31; for (int l = 0; l < 8; ++l) out[l] = rotl32(a[l], r); break; }
                case R0D_IADD: for (int l = 0; l < 8; ++l) out[l] = a[l] + s2[l]; break;
                case R0D_IMULHI: for (int l = 0; l < 8; ++l) out[l] = uint32_t((uint64_t(a[l]) * uint64_t(s2[l])) >> 32); break;
                case R0D_POPC: for (int l = 0; l < 8; ++l) out[l] = a[l] ^ (uint32_t(__builtin_popcount(s2[l])) * 0x9E3779B1u); break;
                default: for (int l = 0; l < 8; ++l) out[l] = a[l] + (uint32_t(s2[l] ? __builtin_clz(s2[l]) : 32) * 0x85EBCA77u); break;
            }
            for (int l = 0; l < 8; ++l) V[x.dst][l] = out[l];
        }
        if ((i & 31) == 31) {
            // the chains' values combine into registers 0..k-1 at the window's end (version 13, k over 1), before the range mask
            if (v12 && kch > 1) for (uint32_t c = 0; c < kch; ++c) for (int l = 0; l < 8; ++l) V[c][l] ^= AC[c][l];
            for (int k = 0; k < 16; ++k) for (int l = 0; l < 8; ++l) V[k][l] = (V[k][l] & 0x3FFFFFFFu) | 0x20000000u;
        }
    }
    // the octet fold: every lane's programme reaches the next address (R[g][0] through the tree and the exchange)
    uint32_t f[8]; for (int l = 0; l < 8; ++l) f[l] = rotl32(V[1][(l + 3) & 7] + V[2][(l + 5) & 7], 7);
    for (int l = 0; l < 8; ++l) V[0][l] ^= f[l];
    for (int k = 0; k < 16; ++k) for (int l = 0; l < 8; ++l) R[l][k] = V[k][l];
}

void work(const ih_ctx& c, const uint8_t S[32], uint8_t M[32], tracer* tr, const uint8_t* sk = nullptr, uint8_t* shares = nullptr, uint8_t* m_out = nullptr, const std::vector<ih_ctx::r0d_op>* prog = nullptr, const std::vector<uint32_t>* shared = nullptr) {
    // bit 10 (section 0.9): sk the 32-byte bond secret (the prover: the shares are computed and written to `shares`,
    // keyed_rounds x 32 bytes), or sk null and `shares` the shares given with the share (the verifier: replayed);
    // m_out, when given, receives the keyed rounds' messages m_i (keyed_rounds x 32 bytes: m_0 = S, m_i the eight lane
    // words at round i's entry) on either call, which the node hands to the proof pi's verifier (section 0.9.1)
    const uint32_t lanes = c.p.lanes;  // 8
    const uint32_t sw = c.slice_words();           // 0: no scratchpad (version 1 default)
    const uint32_t slice_lines = sw / kLineWords;
    const bool pad = sw != 0;
    // SPEC-R0B-schedule-v0.md section 2: the cell constants (r1, r2, op1, op2); version 2 selects cell epoch mod SCHEDULE.
    struct cell_t { uint32_t r1, r2; bool op1_add, op2_xor; };
    static const cell_t kCells[8] = {{7, 17, false, false}, {11, 23, true, true}, {13, 5, false, true}, {19, 29, true, false},
                                     {3, 13, false, false}, {23, 7, true, true}, {29, 11, false, true}, {5, 19, true, false}};
    const cell_t cell = kCells[c.p.version == 2 ? (c.ep % c.p.schedule) : 0];
    const uint64_t n_items = c.n_items();
    const uint64_t items_mask = n_items - 1;
    const bool fastrange = c.p.version == 5 || ((c.p.version >= 8) && !is_pow2(c.p.dataset_bytes));  // section 0.5: idx = (x * N_ITEMS) >> 32 over the 32-bit x; versions 0 to 4 mask a power of two; version 8 by the size (the 10 GiB form)
    auto map_idx = [&](uint32_t x) -> uint64_t { return fastrange ? ((uint64_t(x) * n_items) >> 32) : (uint64_t(x) & items_mask); };
    uint32_t R[8][64] = {};
    uint32_t SH[256] = {};
    // the state's registers per lane: 16 (section 4.1), or version 8's REGISTERS when 32 or 64 (the wider state, section
    // 0.8: the init draws RW words per lane, the absorb, the cell's quads, the permutation and the digest run over RW)
    const uint32_t RW = ((c.p.version >= 8) && c.p.registers > 16) ? c.p.registers : 16;
    if (tr) tr->width = RW;
    const bool lanelocal = c.p.version == 7;  // v6b (section 0.7): per-position key shares, nonlinear leaf mixing, no gather
    const bool blended = c.p.version >= 8;    // section 0.8: the RXE1 block schedule between the absorb and the tree (version 9: the same round under bit 8, then the programme)
    std::vector<v8desc> v8d;  // the round's programme (draw revision 2: decoded from the fetched item)
    // 4.1
    {
        bytes in; in.tag(kTagLanes).raw(S, 32);
        uint8_t L[256 * 8 + 4 * 256];
        const uint32_t nsh = lanelocal ? c.item_bytes() / 4 : 0;
        const uint32_t lb = 4 * RW;  // bytes per lane from the XOF: 64 at the 16-register state, 128 or 256 under the wider state
        b3_hash(in, L, lb * lanes + 4 * nsh);
        for (uint32_t l = 0; l < lanes; ++l)
            for (uint32_t k = 0; k < RW; ++k) R[l][k] = le32(L + lb * l + 4 * k);
        for (uint32_t j = 0; j < nsh; ++j) SH[j] = le32(L + lb * lanes + 4 * j);  // v6b: the key shares, one per word position, from the same XOF
    }
    if (tr && tr->f) { tr->regs("init", R, lanes); }
    // version 10: the per-nonce 16 KB gather table from the seed (one XOF, the tag its own)
    std::vector<uint32_t> GT;
    uint32_t nonce_off = 0;  // version 12's shared form: the nonce's row offset into the period's shared table
    // version 14's live register file (registers_live above 16): lane-major, 8 x L words, the first 16 per lane the programme registers
    // (mirrored from R at each programme run); the rest filled once per nonce from the lane fill by the chain T[k] = rotl(T[k - 16] XOR
    // T[k - 1], 7) x kMul + k over each lane's words, then live across the rounds
    const uint32_t Lf = (c.p.version == 14 && c.p.registers_live > 16) ? c.p.registers_live : 16u;
    std::vector<uint32_t> FL;
    if (Lf > 16) {
        FL.assign(size_t(8) * Lf, 0u);
        for (uint32_t l = 0; l < 8; ++l) { uint32_t* f = &FL[size_t(l) * Lf]; for (uint32_t k = 0; k < 16; ++k) f[k] = R[l][k]; for (uint32_t k = 16; k < Lf; ++k) f[k] = rotl32(f[k - 16] ^ f[k - 1], 7) * kMul + k; }
    }
    if (c.p.version == 10) {
        GT.resize(kR0eTableWords);
        bytes in; in.tag("IgneumHash/R0E/table").raw(S, 32);
        std::vector<uint8_t> tb(4 * kR0eTableWords); b3_hash(in, tb.data(), tb.size());
        for (uint32_t j = 0; j < kR0eTableWords; ++j) GT[j] = le32(tb.data() + 4 * j);
    }
    if (c.p.version >= 11) {
        // the gather table by a CHAINED SCATTER over the dataset (the outside review's eleventh reading, ruling 3; G1 and G3 of the memory
        // cryptanalysis read): 16 reads of one item each; the first address from the work seed (the template and the nonce) and the
        // live lane registers, each next address from a fold of every word of the previous item mixed with the registers and the
        // seed; every word of every item enters the table XORed with the registers; nothing of it is reusable across nonces; 4 KB
        const uint32_t t_words = c.p.version >= 12 ? c.p.table_kib * 256u : kR0eTableWordsV11;
        GT.resize(t_words < kR0eTableWordsV11 ? kR0eTableWordsV11 : t_words);  // the slice is always read whole (16 items); a table under 4 KB keeps its first words
        const uint32_t wpi = c.item_bytes() / 4;  // 64
        std::vector<uint32_t> item(wpi);
        if (g_r0e_flip.kind == 3 && g_r0e_flip.a < 8 && g_r0e_flip.b < 16) R[g_r0e_flip.a][g_r0e_flip.b] ^= (1u << (g_r0e_flip.c & 31));
        // the first address from the same nonlinear fold as the later ones, over the seed's eight words and ALL 128 live registers
        // (the memory cryptanalysis seat's T6d reading of 21:5x: a linear first address on two registers; every register reaches it now)
        uint32_t x = 0;
        for (uint32_t w = 0; w < 8; ++w) { const uint32_t v = le32(S + 4 * w); x = rotl32(x ^ v, 5) + v; }
        for (uint32_t l = 0; l < 8; ++l) for (uint32_t k = 0; k < 16; ++k) { const uint32_t v = R[l][k]; x = rotl32(x ^ v, 5) + v; }
        // the finaliser (the memory cryptanalysis seat's after-fix sweep, 21:4x: the last inputs' diffusion decayed to 2 to 5 bits because
        // the fold has no rounds after its last input): the tree root's finaliser, so every register moves about half the index bits
        x ^= x >> 16; x *= kMul; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13;
        uint64_t addrs[kR0eTableItemsV11];
        for (uint32_t k = 0; k < kR0eTableItemsV11; ++k) {
            addrs[k] = map_idx(x);
            load_item(c, addrs[k], item.data());
            if (g_r0e_flip.kind == 2 && g_r0e_flip.a == k) item[0] ^= (1u << (g_r0e_flip.b & 31));
            uint32_t fold = 0;
            for (uint32_t w = 0; w < wpi; ++w) {
                fold = rotl32(fold ^ item[w], 5) + item[w];
                const uint32_t j = k * wpi + w;
                if (j < kR0eTableWordsV11) GT[j] = item[w] ^ R[j & 7][(j >> 3) & 15];
            }
            x = fold ^ R[(k + 1) & 7][(k + 1) & 15] ^ le32(S + 4 * ((k + 1) & 7));
        }
        // the shared form's nonce-dependent row offset: the fold over the work seed's eight words (the per-nonce table's addresses unchanged)
        if (shared) { uint32_t x = 0; for (uint32_t w = 0; w < 8; ++w) { const uint32_t t = le32(S + 4 * w); x = rotl32(x ^ t, 5) + t; } nonce_off = x; }
        // version 12: the table beyond the 4 KB slice is filled ON CHIP by a chain over the slice (no further dataset read): the fill
        // is SRAM work for the card and the chip alike, per nonce, every word depending on the slice's words
        // (at table_kib under 4 the slice is the table's first words only; the chain fills from the slice's length when the table is longer)
        for (uint32_t j = kR0eTableWordsV11; j < t_words; ++j) GT[j] = rotl32(GT[j - kR0eTableWordsV11] ^ GT[j - 1], 7) * 0x9E3779B1u + j;
        if (t_words < kR0eTableWordsV11) GT.resize(t_words);
        if (g_r0e_flip.kind == 1 && g_r0e_flip.a < t_words) GT[g_r0e_flip.a] ^= (1u << (g_r0e_flip.b & 31));
        if (tr && tr->f) {
            // the fill record for the memory cryptanalysis harness (T5, T6): the reads' item indices in order and the table's words
            std::fprintf(tr->f, ", \"gather_table\": {\"reads\": %u, \"words_total\": %u, \"addrs\": [", kR0eTableItemsV11, t_words);
            for (uint32_t k = 0; k < kR0eTableItemsV11; ++k) std::fprintf(tr->f, "%s%llu", k ? ", " : "", (unsigned long long)addrs[k]);
            std::fprintf(tr->f, "], \"words\": [");
            for (uint32_t j = 0; j < t_words; ++j) std::fprintf(tr->f, "%s%u", j ? ", " : "", GT[j]);
            std::fprintf(tr->f, "]}");
            if (shared) {
                // the shared table's digest and the nonce's row offset (the table itself is the period's; its words from r12_shared_table)
                uint8_t dg[32]; b3_hash(reinterpret_cast<const uint8_t*>(shared->data()), shared->size() * 4, dg, 32);
                char dh[65]; for (int k = 0; k < 32; ++k) std::snprintf(dh + 2 * k, 3, "%02x", dg[k]);
                std::fprintf(tr->f, ", \"shared_table\": {\"words\": %u, \"nonce_offset\": %u, \"digest\": \"%s\"}", unsigned(shared->size()), nonce_off, dh);
            }
        }
    }
    // 4.2
    std::vector<uint32_t> SL(size_t(lanes) * sw);
    for (uint32_t l = 0; pad && l < lanes; ++l) {
        uint32_t* s = SL.data() + size_t(l) * sw;
        for (uint32_t j = 0; j < sw; ++j) {
            uint32_t a = R[l][j & 15];
            const uint32_t b = R[l][(j + 5) & 15];
            a = (a ^ rotl32(b, 13)) * kMul;
            a = a + (b >> 7);
            R[l][j & 15] = a;
            s[j] = a ^ R[l][(j + 11) & 15];
        }
    }
    if (tr && tr->f) { std::fprintf(tr->f, ", "); tr->regs("fill", R, lanes); std::fprintf(tr->f, ", \"rounds\": ["); }
    // 4.3
    uint64_t idx = map_idx(R[0][0] ^ R[4][8]);
    uint32_t W[64 * 32];  // the round's item (64 words) or, under version 14's items_per_round, the round's items concatenated
    uint32_t X[8][64];
    const bool strata = c.p.version >= 4;
    const bool keyed = c.p.version == 6 || c.p.version >= 11;  // version 11 (T1 of the memory cryptanalysis read): version 6's keyed leaves, an item-only root being tableable per epoch
    const uint32_t wpl = strata ? 4 : c.item_bytes() / 32;   // words per lane: 4, 8 or 16 (version 3); the cell's w is h under version 4
    const uint32_t subblocks = strata ? 1 : wpl / 4;        // the 4.4 cell runs once per four-word sub-block (version 3), once (version 4)
    for (uint32_t r = 0; r < c.p.rounds; ++r) {
        if (tr && tr->addr)  // the dataset site of round r: the address came from lane (r mod 8) (site -1 from lanes 0 and 4)
            std::fprintf(tr->addr, "{\"epoch\": %llu, \"nonce\": \"%llu\", \"round\": %u, \"lane\": %u, \"site\": %d, \"addr\": %llu}\n",
                         (unsigned long long)tr->epoch, (unsigned long long)tr->nonce, r, r % lanes, int(r) - 1, (unsigned long long)idx);
        if (blended && (c.p.features & 512)) {
            // programme-only (bit 9, the outside review's bundle row 3): no fetch; the item's words come from the lane state itself
            // (the same absorb, block, tree, cell and exchange instruction stream, no memory), the output folded as before
            const uint32_t n = c.item_bytes() / 4;
            // the epoch seed folded in (eight register-resident words), so the mode keeps the relation's height binding by
            // the epoch (the transitions class) without a memory read
            for (uint32_t j = 0; j < n; ++j) W[j] = rotl32(R[j % lanes][(j / lanes) % RW], 7) ^ ((j + 1) * kMul) ^ le32(c.E + 4 * (j % 8));
        } else load_item(c, idx, W);
        const uint32_t ipr = c.p.version == 14 ? c.p.items_per_round : 1u;
        if (ipr > 1) {
            // version 14's items_per_round (the project's order of 23:4x, the MACs per fetched byte the decision): items 1 .. ipr - 1 at
            // addresses drawn from the lane state before the fetch (parallel reads, random rows), each bound to the tiles it feeds by
            // the tiles' data-dependent window over the concatenation; the absorb, the tree and the address stay on the first item
            for (uint32_t j = 1; j < ipr; ++j) {
                const uint32_t xj = R[j & 7][(j >> 3) & 15] ^ rotl32(uint32_t(idx), j) ^ le32(S + 4 * (j & 7));
                const uint64_t ij = map_idx(xj);
                load_item(c, ij, W + 64 * j);
                if (tr && tr->addr) std::fprintf(tr->addr, "{\"epoch\": %llu, \"nonce\": \"%llu\", \"round\": %u, \"lane\": %u, \"site\": %d, \"addr\": %llu, \"extra\": %u}\n",
                                                  (unsigned long long)tr->epoch, (unsigned long long)tr->nonce, r, j & 7, int(r) - 1, (unsigned long long)ij, j);
            }
        }
        uint32_t h = 0;
        uint32_t K[8][64];
        if (keyed) std::memcpy(K, R, sizeof K);  // the state before the absorb
        if (blended && (c.p.features & 1024) && c.p.keyed_rounds && (uint64_t(r) * c.p.keyed_rounds) % c.p.rounds < c.p.keyed_rounds) {  // the S keyed rounds spread evenly: round r is keyed iff (r S) mod R < S, its index i = (r S) div R
            // the bond-keyed round i (section 0.9): s_i = H(sk, m_i), an integer function (keyed BLAKE3) of the state's
            // eight lane words R[l][0] (m_i; i = 0 uses S alone), entering the round's item words before the absorb, the
            // programme and the tree, so the work cannot proceed without the secret; the verifier replays the shares it is given
            const uint32_t i = uint32_t((uint64_t(r) * c.p.keyed_rounds) / c.p.rounds);
            uint8_t* s = shares + size_t(i) * 32;
            // the message m_i (section 0.9.1): m_0 = S, m_i the eight lane words at the round's entry; the share through
            // the one standalone function so an oracle or a prover calling ignh_bond_share cannot drift from the walk
            uint8_t m[32];
            if (i == 0) std::memcpy(m, S, 32); else for (uint32_t l = 0; l < 8; ++l) put_le32(m + 4 * l, R[l][0]);
            if (m_out) std::memcpy(m_out + size_t(i) * 32, m, 32);
            if (sk) ignh_bond_share(sk, i, m, s);
            // the share enters the fetched item's first eight words (the tree, the absorb and the programme read them whole),
            // not a register: the cell rewrites register 0 and the masked FP forms round a register's low bits away, so a
            // register injection could vanish within the round (found by the self-test's flipped-share case, 00:0x UK)
            for (uint32_t j = 0; j < 8; ++j) W[j] ^= le32(s + 4 * j);
            if (tr && tr->f) { for (int k = 0; k < 32; ++k) std::snprintf(tr->share_hex + 2 * k, 3, "%02x", s[k]); tr->pending_share = true; }
        }
        if (strata) {
            // Version 4 (SPEC section 0.4, the whole-item rule): every word absorbed into its own register, then the
            // balanced domain-separated reduction tree over all n words to the item word h.
            const uint32_t n = c.item_bytes() / 4;
            for (uint32_t j = 0; j < n; ++j) R[j % lanes][(j / lanes) % RW] += W[j];
            if (blended) {
                // step 1b (section 0.8): B blocks of K descriptors on registers 0..K-1 of every lane, snapshot semantics per
                // block (every destination reads the block's entry state), the same descriptors on every lane
                const uint32_t K = c.p.registers, B = (c.p.features & 256) ? 0u : c.p.blocks, F = c.p.features;
                if (B) v8_decode(W, n, B, K, F, v8d);  // the round's programme from the fetched item (draw revision 2)
                if (F & 256) {
                    // walk-only (bit 8, the outside review's bundle row 2): the block's output replaced by a fixed constant on every
                    // destination, so the address path carries no programme and the fetch stream is version 4's walk under one
                    // constant perturbation; no block instruction stream runs
                    for (uint32_t d = 0; d < K; ++d) for (uint32_t l = 0; l < lanes; ++l) R[l][d] ^= kWalkConst;
                }
                for (uint32_t b = 0; b < B; ++b) {
                    uint32_t T[8][64]; std::memcpy(T, R, sizeof T);
                    for (uint32_t d = 0; d < K; ++d) {
                        const v8desc& x = v8d[size_t(b) * K + d];
                        for (uint32_t l = 0; l < lanes; ++l) {
                            uint32_t y = (F & 4096) ? T[(l + x.shift1) % lanes][x.src1] : T[l][x.src1];  // bit 12: the first source shuffled too
                            // option (a), bit 0: the lane's scratch slice read at a register-chosen line, the word mixed into the
                            // second operand, the result written back to a second register-chosen word (both lane-private)
                            uint32_t* sl = pad ? SL.data() + size_t(l) * sw : nullptr;
                            uint32_t ra = 0;
                            if ((F & 1) && sl) { ra = (T[l][x.src2] ^ (uint32_t(x.salt) * kMul)) % sw; y ^= sl[ra]; }
                            // option (c), bit 1: the predicated select, a data-dependent predicate choosing the opcode's neighbour
                            const uint32_t opc = ((F & 2) && ((y >> x.pbit) & 1)) ? (x.opcode + 1) % 4 : x.opcode;  // the predicate on the first operand as read (shuffled under bit 12)
                            // bit 7, the cross-lane source: the second source word comes from another lane of the snapshot (a shuffle)
                            const uint32_t zz = (F & (128 | 4096)) ? T[(l + x.shift) % lanes][x.src2] : T[l][x.src2];
                            const uint32_t out = (F & 32) ? v8fop_wide(opc, T[l][d], y, zz) : ((F & 16) && opc == 2) ? v8fop(T[l][d], y, zz) : v8op(opc, T[l][d], y, zz, x.rot);
                            R[l][d] = out;
                            if ((F & 1) && sl) sl[(ra + 1 + (out % (sw - 1))) % sw] = out ^ T[l][d];
                        }
                    }
                }
                if (tr && tr->f) {
                    tr->pending_block = true; std::memcpy(tr->block_regs, R, sizeof tr->block_regs);
                    tr->block_ops.clear(); for (size_t i = 0; i < v8d.size(); ++i) tr->block_ops.push_back(char('0' + v8d[i].opcode));  // the opcode census per item
                }
                // version 9 (SPEC-R0D-v0.md): the period's programme on each lane's 16 registers, the same on every lane, after the
                // absorb and the walk-only constant and before the tree; the lane's registers feed the tree and the address as before
                if (c.p.version == 9 && prog) {
                    for (uint32_t l = 0; l < lanes; ++l) r0d_run_lane(R[l], *prog);
                    if (tr && tr->f) { tr->pending_prog = true; std::memcpy(tr->prog_regs, R, sizeof tr->prog_regs); }
                }
                if (c.p.version >= 10 && prog) {
                    r0e_run_octet(R, *prog, W, n, GT.data(), c.p.version >= 12 ? uint32_t(GT.size()) : c.p.version == 11 ? kR0eTableWordsV11 : kR0eTableWords, c.p.version >= 11, c.p.version >= 12 ? c.p.access_bytes / 4 : 0,
                                  shared ? shared->data() : nullptr, shared ? uint32_t(shared->size()) : 0u, nonce_off, c.p.version >= 13 ? c.p.chains_per_nonce : 1u, c.p.version >= 13 && c.p.gather_lane_bank != 0, c.p.version == 14 ? c.p.tile_shape : 0u, c.p.version == 14 ? c.p.fold_every : 1u, c.p.version == 14 ? c.p.items_per_round : 1u, Lf > 16 ? FL.data() : nullptr, Lf, c.p.version == 14 && c.p.layout_draw != 0, c.p.version == 14 ? c.p.tile_layout : 0u);
                    if (tr && tr->f) { tr->pending_prog = true; std::memcpy(tr->prog_regs, R, sizeof tr->prog_regs);
                        if (Lf > 16) { uint8_t dg[32]; b3_hash(reinterpret_cast<const uint8_t*>(FL.data()), FL.size() * 4, dg, 32); for (int k = 0; k < 32; ++k) std::snprintf(tr->file_hex + 2 * k, 3, "%02x", dg[k]); tr->pending_file = true; } }
                }
            }
            uint32_t z[256];
            // Version 6 (section 0.6, Breakwater R3 on the security review's S9): each leaf is keyed by the register its word is
            // absorbed into (R[j mod 8][(j div 8) mod 16], the value before step 1: free on every lane layout, the word is
            // already routed there), and the root is finalised, so no digest of the item alone replaces the tree.
            for (uint32_t j = 0; j < n; ++j) {
                if (lanelocal) {
                    // v6b: the word mixed nonlinearly with its position's share on arrival (ARX: xor, a share-chosen rotation, add)
                    const uint32_t s = SH[j], tt = W[j] ^ s;
                    z[j] = (tt + rotl32(tt, 1 + ((s >> 27) & 15))) ^ ((j + 1) * kMul);
                } else {
                    z[j] = W[j] ^ (keyed ? K[j % lanes][(j / lanes) % RW] : 0u) ^ ((j + 1) * kMul);
                }
            }
            uint32_t leaf[256];
            if (lanelocal) std::memcpy(leaf, z, 4 * n);  // the leaves, kept: the tree below reduces z in place (the security review's finding of 19:3x UK)
            for (uint32_t m = n; m > 1; m /= 2)
                for (uint32_t i = 0; i < m / 2; ++i) z[i] = ((z[2 * i] ^ rotl32(z[2 * i + 1], 13)) * kMul) + (z[2 * i + 1] >> 7);
            h = z[0];
            if (keyed || lanelocal) { h ^= h >> 16; h *= kMul; h ^= h >> 15; }
            if (lanelocal) for (uint32_t j = 0; j < n; ++j) SH[j] = (SH[j] ^ rotl32(leaf[j], 11)) + h;  // the shares carried forward from the leaves (section 0.7), bound to every word of the round through the finalised h
        }
        // 4.4 (version 3: once per four-word sub-block of the lane's wpl words, in order; version 4: once, w = h)
        for (uint32_t sb = 0; sb < subblocks; ++sb)
        for (uint32_t l = 0; l < lanes; ++l) {
            for (uint32_t k = 0; k < RW / 4; ++k) {  // the cell over every quad of the lane's registers (4 quads; 8 or 16 under the wider state)
                uint32_t a = R[l][4 * k], b = R[l][4 * k + 1], cc = R[l][4 * k + 2], d = R[l][4 * k + 3];
                const uint32_t w = strata ? h : W[wpl * l + 4 * sb + k];
                a = a + w;
                b = cell.op1_add ? b + a : b ^ a;
                cc = cc + b * kMul;
                d = rotr32(d ^ cc, a & 31);
                a = a ^ (d >> (w & 31));
                b = rotl32(b + d, cell.r1);
                cc = cc ^ rotl32(a, cell.r2);
                d = cell.op2_xor ? d ^ (b * cc) : d + b * cc;
                R[l][4 * k] = a; R[l][4 * k + 1] = b; R[l][4 * k + 2] = cc; R[l][4 * k + 3] = d;
            }
            uint32_t T[64];
            std::memcpy(T, R[l], sizeof T);
            for (uint32_t k = 0; k < RW; ++k) R[l][k] = T[(5 * k + 1) & (RW - 1)];  // 5 is coprime to 16, 32 and 64: a permutation at every width
        }
        // 4.5 scratch
        for (uint32_t l = 0; pad && l < lanes; ++l) {
            uint32_t* s = SL.data() + size_t(l) * sw;
            const uint32_t line = R[l][0] & (slice_lines - 1);
            for (uint32_t j = 0; j < 4; ++j) {
                const uint32_t v = s[4 * line + j];
                R[l][8 + j] = rotl32(R[l][8 + j] ^ v, 5) + R[l][j];
                s[4 * line + j] = (v + R[l][12 + j]) ^ R[l][8 + j];
            }
        }
        // 4.5 exchange
        std::memcpy(X, R, sizeof X);
        for (uint32_t l = 0; l < lanes; ++l) {
            const uint32_t pp = l ^ (1u << (r % 3));
            const uint32_t q = (l + 1 + (X[l][1] % 7)) % lanes;
            R[l][0] = X[l][0] + X[pp][0];
            R[l][1] = X[l][1] ^ X[q][1];
            R[l][2] = X[l][2] + rotl32(X[pp][2], 11);
            R[l][3] = X[l][3] ^ rotl32(X[q][3], 23);
            if (tr && tr->lanes)  // the lanes whose state of this round flowed into lane l: itself, the fixed partner, the data partner
                std::fprintf(tr->lanes, "{\"epoch\": %llu, \"nonce\": \"%llu\", \"round\": %u, \"lane\": %u, \"from\": [%u, %u, %u]}\n",
                             (unsigned long long)tr->epoch, (unsigned long long)tr->nonce, r, l, l, pp, q);
        }
        if (tr && tr->f) {
            std::fprintf(tr->f, "%s{\"r\": %u, \"idx\": %llu, ", r ? ", " : "", r, (unsigned long long)idx);
            if (tr->pending_share) { std::fprintf(tr->f, "\"share\": \"%s\", ", tr->share_hex); tr->pending_share = false; }
            if (c.p.version >= 3) {
                if (strata) std::fprintf(tr->f, "\"h\": %u, ", h);
                if (blended && tr->pending_block) { tr->regs("block", tr->block_regs, lanes); std::fprintf(tr->f, ", \"ops\": \"%s\", ", tr->block_ops.c_str()); tr->pending_block = false; }
                if (tr->pending_prog) { tr->regs("program_regs", tr->prog_regs, lanes); std::fprintf(tr->f, ", "); tr->pending_prog = false; }
                if (tr->pending_file) { std::fprintf(tr->f, "\"file_digest\": \"%s\", ", tr->file_hex); tr->pending_file = false; }
                if (lanelocal) { std::fprintf(tr->f, "\"shares\": ["); for (uint32_t j = 0; j < c.item_bytes() / 4; ++j) std::fprintf(tr->f, "%s%u", j ? ", " : "", SH[j]); std::fprintf(tr->f, "], "); }
                std::fprintf(tr->f, "\"item\": [");
                for (uint32_t k = 0; k < c.item_bytes() / 4; ++k) std::fprintf(tr->f, "%s%u", k ? ", " : "", W[k]);
                std::fprintf(tr->f, "], ");
                if (c.p.version == 14 && c.p.items_per_round > 1) {
                    std::fprintf(tr->f, "\"items_extra\": [");
                    for (uint32_t j = 1; j < c.p.items_per_round; ++j) { std::fprintf(tr->f, "%s[", j > 1 ? ", " : ""); for (uint32_t k = 0; k < 64; ++k) std::fprintf(tr->f, "%s%u", k ? ", " : "", W[64 * j + k]); std::fprintf(tr->f, "]"); }
                    std::fprintf(tr->f, "], ");
                }
            }
            tr->regs("regs", R, lanes);
            std::fprintf(tr->f, "}");
        }
        // 4.6
        const uint32_t g = (r + 1) % lanes;
        idx = map_idx(strata ? (R[g][0] ^ h) : R[g][0]);  // version 4: the item word h reaches the next address directly
    }
    // 4.7
    uint32_t f[8][4] = {};
    for (uint32_t l = 0; pad && l < lanes; ++l) {
        const uint32_t* s = SL.data() + size_t(l) * sw;
        for (int j = 0; j < 4; ++j) f[l][j] = 0;
        for (uint32_t i = 0; i < slice_lines; ++i)
            for (uint32_t j = 0; j < 4; ++j) f[l][j] = rotl32(f[l][j] ^ s[4 * i + j], 9) + s[4 * i + ((j + 1) & 3)];
    }
    bytes st;
    st.tag(kTagMix).raw(S, 32);
    uint8_t tmp[4];
    for (uint32_t l = 0; l < lanes; ++l) for (uint32_t k = 0; k < RW; ++k) { put_le32(tmp, R[l][k]); st.raw(tmp, 4); }
    if (pad) for (uint32_t l = 0; l < lanes; ++l) for (uint32_t j = 0; j < 4; ++j) { put_le32(tmp, f[l][j]); st.raw(tmp, 4); }
    b3_hash(st, M, 32);
    if (tr && tr->f) {
        std::fprintf(tr->f, "]");
        if (pad) {  // the fold exists only with a scratchpad (4.7); without one the field is absent
            std::fprintf(tr->f, ", \"fold\": [");
            for (uint32_t l = 0; l < lanes; ++l) std::fprintf(tr->f, "%s[%u, %u, %u, %u]", l ? ", " : "", f[l][0], f[l][1], f[l][2], f[l][3]);
            std::fprintf(tr->f, "]");
        }
    }
}

void work_seed(const ih_ctx& c, const uint8_t header[32], uint64_t nonce, uint8_t S[32]) {
    bytes in = prefix(c, kTagSeed);
    in.raw(header, 32).be64(nonce);
    b3_hash(in, S, 32);
}

void final_digest(const ih_ctx& c, const uint8_t S[32], const uint8_t M[32], uint8_t F[32]) {
    bytes in = prefix(c, kTagFinal);
    in.raw(S, 32).raw(M, 32);
    b3_hash(in, F, 32);
}

int height_ok(const ignh_params& p, uint64_t height) {
    (void)p;
    return height > kMaxHeight ? IGNPOW_E_HEIGHT : IGNPOW_OK;
}

// ---- the candidate table ----

int ih_check_params(const char* json) { ignh_params p; return parse_params(json, p); }

uint64_t ih_epoch_of(const char* json, uint64_t height, int* status) {
    ignh_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK) st = height_ok(p, height);
    if (status) *status = st;
    return st == IGNPOW_OK ? height / p.epoch_blocks : 0;
}

uint64_t ih_cache_bytes(const char* json, uint64_t height, int* status) {
    ignh_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK) st = height_ok(p, height);
    if (status) *status = st;
    return st == IGNPOW_OK ? (p.version >= 1 ? p.cache_bytes : 32) : 0;
}

uint64_t ih_dataset_bytes(const char* json, uint64_t height, int* status) {
    ignh_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK) st = height_ok(p, height);
    if (status) *status = st;
    return st == IGNPOW_OK ? p.dataset_bytes : 0;
}

void build_dataset(ih_ctx& c) {
    const uint64_t n = c.n_items();
    const auto t0 = std::chrono::steady_clock::now();
    c.store->dataset.resize(size_t(c.p.dataset_bytes));
    unsigned nt = std::thread::hardware_concurrency();
    if (nt == 0) nt = 4;
    if (nt > 32) nt = 32;
    if (const char* env = std::getenv("IGNPOW_THREADS")) { const int v = std::atoi(env); if (v >= 1) nt = unsigned(v); }
    std::vector<std::thread> ts;
    for (unsigned t = 0; t < nt; ++t)
        ts.emplace_back([&, t]() {
            for (uint64_t i = t; i < n; i += nt) derive_item(c, i, c.store->dataset.data() + i * c.item_bytes());  // item_bytes, not 128: the full build of a wider item
        });
    for (auto& th : ts) th.join();
    c.build_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

// version 15 (section 2f): the four families' documents from the rotation's document (the shape, the count, the period, the anchor,
// the features and the keyed rounds shared; the families' own fields the registered constants), and the window's family from the
// balancing period's permutation
static const char* kTagRotation = "IgneumHash/R0E/rotation";
ignh_params r15_family_params(const ignh_params& r, uint32_t f) {
    ignh_params q = r;
    q.table_kib = 0; q.access_bytes = 0; q.shared_kib = 0; q.mma_max = 0; q.chains_per_nonce = 0; q.gather_lane_bank = 0; q.tile_shape = 0; q.fold_every = 0; q.items_per_round = 0; q.registers_live = 0; q.contexts_hint = 0; q.layout_draw = 0; q.difficulty_scale_ppm = 0; q.tile_layout = 0; q.program_distance = 0;
    switch (f) {
        case 0: q.version = 14; q.table_kib = 1; q.access_bytes = 16; q.shared_kib = 32; q.mma_max = 22; q.chains_per_nonce = 1; q.gather_lane_bank = 1; q.tile_shape = 1; q.fold_every = 8; q.layout_draw = 1; q.items_per_round = 1; q.registers_live = 16; q.difficulty_scale_ppm = 1000000; break;  // the tensor weave (the registered document of 00:2x: fold_every 8, the layout draw)
        case 1: q.version = 11; break;  // the bandwidth walk (the walk with the general lane and the 4 KB fill)
        case 2: q.version = 13; q.table_kib = 1; q.access_bytes = 16; q.shared_kib = 32; q.mma_max = 14; q.chains_per_nonce = 1; q.gather_lane_bank = 1; break;  // the programme lane
        default: q.version = 12; q.table_kib = 1; q.access_bytes = 16; q.shared_kib = 32; break;  // the gather and control form
    }
    return q;
}
// the permutation of the balancing period (four windows): its index the seed's first word mod 24, the seed over the balancing
// period's anchor (under version 15 the anchor of the plain and the anchored calls is the balancing period's, section 2f)
uint32_t r15_family(const ignh_params& r, uint64_t height, const uint8_t* anchor) {
    const uint64_t period = height / r.period_blocks, bp = period / 4;
    uint8_t seed[32]; { bytes in; in.tag(kTagRotation).le64(r.chain_id).le64(bp).raw(anchor ? anchor : r.program_anchor, 32); b3_hash(in, seed, 32); }
    uint32_t idx = le32(seed) % 24;
    uint8_t perm[4] = {0, 1, 2, 3};
    // the idx-th permutation in lexicographic order (the factorial number system)
    uint8_t pool[4] = {0, 1, 2, 3}; uint32_t n = 4;
    static const uint32_t fact[4] = {6, 2, 1, 1};
    for (uint32_t i = 0; i < 4; ++i) { const uint32_t k = idx / fact[i]; idx %= fact[i]; perm[i] = pool[k]; for (uint32_t j = k; j + 1 < n; ++j) pool[j] = pool[j + 1]; --n; }
    return perm[period % 4];
}

ctx_base* ih_create(const char* json, uint64_t height, int full, int* status) {
    auto c = std::make_unique<ih_ctx>();
    int st = parse_params(json, c->p);
    if (st == IGNPOW_OK) st = height_ok(c->p, height);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    c->ep = height / c->p.epoch_blocks;
    uint8_t pb[IGNH_PARAMS_BYTES];
    params_bytes(c->p, pb);
    b3_hash(pb, params_len(c->p), c->P, 32);
    // the dataset commitment excludes the keyed rounds (section 0.9, 11 Oct 2026): E and D are derived over Pd, the
    // twin's parameter hash, so the keyed document and its unkeyed twin build and share ONE dataset; S stays on P
    dataset_params_hash(c->p, c->Pd);
    { bytes in = prefix_d(*c, kTagEpoch); in.be64(c->ep); b3_hash(in, c->E, 32); }
    { bytes in = prefix_d(*c, kTagDataset); in.raw(c->E, 32).be64(c->p.dataset_bytes); b3_hash(in, c->D, 32); }
    bool joined = false; c->store = ds_acquire(c->E, c->D, c->p.cache_bytes, &joined); c->resident = full != 0;
    std::unique_lock<std::mutex> build_lk(c->store->build_mu);  // the store's build lock: a joiner waits for the builder, a builder builds once
    if (c->p.version >= 1 && (c->p.version < 16 || c->p.dataset_bytes) && c->store->cache.empty()) {  // version 16 has no cache and no dataset (a compute coordinate) except revision 19 (memory fused: version 15's dataset)
        try { build_cache(*c); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
        c->store->cache_ms = c->cache_ms;
    } else c->cache_ms = c->store->cache_ms;
    if (c->p.version == kRotationVersion) {
        // the four families over this context's cache and dataset: each its own document, its own P and programme caches, E and D
        // the same by construction (the dataset's parameter hash maps every version above 8 to the twin's)
        for (uint32_t f = 0; f < 4; ++f) {
            auto q = std::make_unique<ih_ctx>();
            q->p = r15_family_params(c->p, f); q->ep = c->ep; q->data_owner = c.get();
            uint8_t qb[IGNH_PARAMS_BYTES]; params_bytes(q->p, qb); b3_hash(qb, params_len(q->p), q->P, 32);
            dataset_params_hash(q->p, q->Pd);
            { bytes in = prefix_d(*q, kTagEpoch); in.be64(q->ep); b3_hash(in, q->E, 32); }
            { bytes in = prefix_d(*q, kTagDataset); in.raw(q->E, 32).be64(q->p.dataset_bytes); b3_hash(in, q->D, 32); }
            if (std::memcmp(q->E, c->E, 32) != 0) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
            c->fam[f] = std::move(q);
        }
    }
    if (full && (c->p.version < 16 || c->p.dataset_bytes) && c->store->dataset.empty()) {
        try { build_dataset(*c); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
        c->store->build_ms = c->build_ms;
    } else if (full) c->build_ms = c->store->build_ms;
    build_lk.unlock();
    if (status) *status = IGNPOW_OK;
    return c.release();
}

// The shared context (the node team's resident form, 13 October 2026, 08:3x UK): a context with its OWN document over a DONOR's
// cache and dataset memory, for a keyed document and its unkeyed twin (the same dataset by construction: E and D are derived over
// Pd, the twin's parameter hash) so one dataset serves the pair on a mixed chain. The donor must be an IgneumHash context of the
// same epoch whose E and D equal this document's (IGNPOW_E_PARAMS otherwise); the donor outlives every context sharing it (the
// node's rule: freeing the shared context frees nothing of the donor's). A version 15 document's families follow the sharing.
ctx_base* ih_create_shared_impl(const char* json, uint64_t height, const ctx_base* donor, int* status) {
    const auto* d = static_cast<const ih_ctx*>(donor);
    if (!d) { if (status) *status = IGNPOW_E_ARG; return nullptr; }
    auto c = std::make_unique<ih_ctx>();
    int st = parse_params(json, c->p);
    if (st == IGNPOW_OK) st = height_ok(c->p, height);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    c->ep = height / c->p.epoch_blocks;
    uint8_t pb[IGNH_PARAMS_BYTES];
    params_bytes(c->p, pb);
    b3_hash(pb, params_len(c->p), c->P, 32);
    dataset_params_hash(c->p, c->Pd);
    { bytes in = prefix_d(*c, kTagEpoch); in.be64(c->ep); b3_hash(in, c->E, 32); }
    { bytes in = prefix_d(*c, kTagDataset); in.raw(c->E, 32).be64(c->p.dataset_bytes); b3_hash(in, c->D, 32); }
    if (c->ep != d->ep || std::memcmp(c->E, d->E, 32) != 0 || std::memcmp(c->D, d->D, 32) != 0 || c->p.dataset_bytes != d->p.dataset_bytes || c->p.cache_bytes != d->p.cache_bytes) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    c->data_owner = d;
    if (c->p.version == kRotationVersion) {
        for (uint32_t f = 0; f < 4; ++f) {
            auto q = std::make_unique<ih_ctx>();
            q->p = r15_family_params(c->p, f); q->ep = c->ep; q->data_owner = c.get();
            uint8_t qb[IGNH_PARAMS_BYTES]; params_bytes(q->p, qb); b3_hash(qb, params_len(q->p), q->P, 32);
            dataset_params_hash(q->p, q->Pd);
            { bytes in = prefix_d(*q, kTagEpoch); in.be64(q->ep); b3_hash(in, q->E, 32); }
            { bytes in = prefix_d(*q, kTagDataset); in.raw(q->E, 32).be64(q->p.dataset_bytes); b3_hash(in, q->D, 32); }
            if (std::memcmp(q->E, c->E, 32) != 0) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
            c->fam[f] = std::move(q);
        }
    }
    if (status) *status = IGNPOW_OK;
    return c.release();
}

// VERSION 15, the bandwidth-first class (the founder's order of 13 October 14:17 UK; the rotation withdrawn; the lab's cand-14 as the draw's shape: MAD-heavy,
// four register banks (ILP 4), flat, 256 scattered reads). The period key K = BLAKE3(LP("IgneumHash/R0F/key") || LE64(chain_id) ||
// LE64(period) || anchor), period = height div PERIOD_BLOCKS, the anchor the chain form's (the block hash at period x P minus D; the
// document's program_anchor in the sets). The attempt: 8 lanes x 32 registers (four banks of eight) from BLAKE3xof(LP("IgneumHash/
// R0F/init") || K || S); then READS / 16 iterations, each issuing 16 independent item reads (bank b, slot q: the item at
// (R[lane 4b+q mod 8][8b] XOR Kw[(16 i + 4 b + q) mod 8]) mapped over the dataset), every item's 64 words consumed across the
// octet (lane l takes words 8 l .. 8 l + 8, XORed with K's words, into bank b's registers), then the period's programme (PROGRAM_OPS
// operations from BLAKE3xof(LP("IgneumHash/R0F/program") || K): opcode by cand-14's labw weights (below), bank = op index mod 4 (the four chains independent), d, a, b in the bank, the immediate LE32) on every lane; the mix
// M = BLAKE3(LP("IgneumHash/R0F/mix") || K || the 256 registers LE); the final digest the common one over (S, M). The reads of an
// iteration are independent (the card's read ceiling), the next iteration's addresses depend on the programme's output.
namespace r16 {
constexpr const char* kTagKey = "IgneumHash/R0F/key", *kTagInit = "IgneumHash/R0F/init", *kTagProg = "IgneumHash/R0F/program", *kTagMix = "IgneumHash/R0F/mix";
// the opcode weights are cand-14's labw0f0f140c0d0000190000 decoded (add 15, sub 15, xor 20, rotl 12, rotr 13, mul 0, mulhi 0, mad 25,
// shfl 0, dp4a 0, of 100), scaled to 256: ADD 38, SUB 38, XOR 51, ROTL 31, ROTR 33, MAD 65
enum : uint8_t { MAD = 0, ADD, SUB, XOR, ROTL, ROTR };
inline uint8_t opcode_of(uint8_t q) { if (q < 38) return ADD; if (q < 76) return SUB; if (q < 127) return XOR; if (q < 158) return ROTL; if (q < 191) return ROTR; return MAD; }
struct op_t { uint8_t opc, d, a, b; uint32_t imm; };
inline uint32_t fmix32_15(uint32_t h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }  // the murmur3 finaliser (r17's, declared later in the file)
void period_key(const ih_ctx& c, uint64_t height, const uint8_t* anchor, uint8_t K[32]) {
    bytes in; in.tag(kTagKey).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor ? anchor : c.p.program_anchor, 32);
    b3_hash(in, K, 32);
}
void draw_program(const uint8_t K[32], uint32_t n, std::vector<op_t>& prog) {
    bytes in; in.tag(kTagProg).raw(K, 32);
    std::vector<uint8_t> x(size_t(n) * 8); b3_hash(in, x.data(), x.size());
    prog.resize(n);
    for (uint32_t j = 0; j < n; ++j) { const uint8_t* q = x.data() + 8 * j; const uint8_t bank = uint8_t(j & 3); prog[j] = { opcode_of(q[0]), uint8_t(8 * bank + (q[1] & 7)), uint8_t(8 * bank + (q[2] & 7)), uint8_t(8 * bank + (q[3] & 7)), le32(q + 4) }; }
}
inline void run_lane(const std::vector<op_t>& prog, uint32_t* R) {
    for (const op_t& o : prog) {
        const uint32_t a = R[o.a], b = R[o.b];
        switch (o.opc) {
            case MAD: R[o.d] = a * b + o.imm; break;
            case ADD: R[o.d] = a + b + o.imm; break;
            case SUB: R[o.d] = a - b - o.imm; break;
            case XOR: R[o.d] = a ^ b ^ o.imm; break;
            case ROTL: R[o.d] = rotl32(a, o.imm & 31) ^ b; break;
            default: R[o.d] = rotl32(a, 32 - (o.imm & 31)) ^ b; break;  // ROTR
        }
    }
}
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32]; period_key(c, height, anchor, K);
    uint32_t Kw[8]; for (int k = 0; k < 8; ++k) Kw[k] = le32(K + 4 * k);
    std::vector<op_t> prog; draw_program(K, c.p.program_ops, prog);
    uint32_t R[8][32];
    { bytes in; in.tag(kTagInit).raw(K, 32).raw(S, 32); uint8_t x[1024]; b3_hash(in, x, sizeof x); for (int l = 0; l < 8; ++l) for (int k = 0; k < 32; ++k) R[l][k] = le32(x + 4 * (32 * l + k)); }
    const uint64_t n_items = c.n_items(), mask = n_items - 1;
    const bool rule2 = (c.p.program_distance & 2) != 0;  // version 15's second keyed-read rule (the drawer)
    uint32_t W[64];
    for (uint32_t i = 0; i < c.p.rounds / 16; ++i) {
        uint64_t addr[16];
        if (rule2) for (uint32_t b = 0; b < 4; ++b) for (uint32_t q = 0; q < 4; ++q) addr[4 * b + q] = fmix32_15(R[(4 * b + q) & 7][8 * b + 4] + Kw[(16 * i + 4 * b + q) & 7]) & mask;  // the drawer's rule: the bank's fifth register through fmix32
        else for (uint32_t b = 0; b < 4; ++b) for (uint32_t q = 0; q < 4; ++q) addr[4 * b + q] = (R[(4 * b + q) & 7][8 * b] ^ Kw[(16 * i + 4 * b + q) & 7]) & mask;  // the 16 addresses of the iteration, all from the iteration's entry state
        for (uint32_t b = 0; b < 4; ++b) for (uint32_t q = 0; q < 4; ++q) {
            load_item(c, addr[4 * b + q], W);
            if (rule2) for (int l = 0; l < 8; ++l) for (int k = 0; k < 8; ++k) R[l][8 * b + k] ^= rotl32(W[8 * l + k], 7) + Kw[(k + q) & 7];  // the drawer's rule: the item word rotated and the key added
            else for (int l = 0; l < 8; ++l) for (int k = 0; k < 8; ++k) R[l][8 * b + k] ^= W[8 * l + k] ^ Kw[(k + q) & 7];
        }
        for (int l = 0; l < 8; ++l) run_lane(prog, R[l]);
    }
    bytes in; in.tag(kTagMix).raw(K, 32); for (int l = 0; l < 8; ++l) for (int k = 0; k < 32; ++k) in.le32(R[l][k]);
    b3_hash(in, M, 32);
}
}  // namespace r16

// VERSION 16, the STATIC-FABRIC integer coordinate (the founder's order of 13 October 15:0x UK, C-36): one thread an attempt over 224
// registers in four banks of 56 (static indices only: the programme names every register by its drawn constant index, so a compiler can
// neither shrink the file nor index it by data; version 14's data-dependent register indexing is banned); the period key K as version 15's
// (tag IgneumHash/R0G/key); the init 224 registers from BLAKE3xof(LP("IgneumHash/R0G/init") || K || S); the programme from
// BLAKE3xof(LP("IgneumHash/R0G/program") || K): rounds passes, each pass 224 operations j = 0 .. 223 in bank j mod 4, writing d = the
// bank's drawn permutation of its 56 registers at position j div 4 (every register written once a pass) and reading a = a second drawn
// permutation at the same position (every register read once a pass as the first source) and b a drawn register of the bank, the opcode
// by cand-14's labw table and the LE32 immediate: the liveness guarantee, every register read and written within 56 of its bank's
// operations (224 of the thread's); the four banks are independent chains (ILP 4). The mix M = BLAKE3(LP("IgneumHash/R0G/mix") || K
// || the 224 registers LE32); the final digest the common one; the verifier runs the same programme once.
namespace r16f {
constexpr const char* kTagKey = "IgneumHash/R0G/key", *kTagInit = "IgneumHash/R0G/init", *kTagProg = "IgneumHash/R0G/program", *kTagMix = "IgneumHash/R0G/mix";
constexpr uint32_t kRegs = 224, kBank = 56;  // the widest file; the document's "registers" (four banks of registers / 4) sizes the live set from revision 13
struct op_t { uint8_t opc, d, a, b; uint32_t imm; uint8_t c = 0; };
void period_key(const ih_ctx& c, uint64_t height, const uint8_t* anchor, uint8_t K[32]) {
    bytes in; in.tag(kTagKey).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor ? anchor : c.p.program_anchor, 32);
    b3_hash(in, K, 32);
}
inline uint8_t opcode_light(uint8_t q) { if (q < 45) return r16::ADD; if (q < 90) return r16::SUB; if (q < 150) return r16::XOR; if (q < 187) return r16::ROTL; if (q < 224) return r16::ROTR; return r16::MAD; }  // the light table
void draw_program(const uint8_t K[32], uint32_t passes, std::vector<op_t>& prog, bool light = false, bool rev4 = false, bool rev5 = false, bool rev9 = false, bool rev10 = false, bool rev11 = false, uint32_t nb = kBank, bool rev13 = false, uint32_t variant = 0) {
    const uint32_t nr = 4 * nb;
    // per pass and bank: two permutations of 56 by Fisher-Yates over the stream's words (55 words each), then 56 operations of (opcode
    // byte, b byte, LE32 immediate) = 6 bytes; the stream BLAKE3xof(tag || K) read in order
    const size_t per_bank = size_t(2 * (nb - 1)) * 4 + size_t(nb) * 6, total = size_t(passes) * 4 * per_bank;  // two permutations of nb (nb - 1 words each), then nb operations of six bytes
    bytes in; in.tag(kTagProg).raw(K, 32);
    std::vector<uint8_t> x(total); b3_hash(in, x.data(), x.size());
    const uint32_t per_pass = nr + ((rev4 || rev5 || rev9) && !rev11 ? 4 : 0);  // revision 11: no end-of-pass XORs
    prog.resize(size_t(passes) * per_pass);
    size_t off = 0;
    std::vector<uint8_t> first_write((rev9 || rev11) ? size_t(passes) * 4 : 0);  // revision 9: each pass's and bank's first-written register, for the previous pass's last read
    for (uint32_t r = 0; r < passes; ++r) for (uint32_t bk = 0; bk < 4; ++bk) {
        uint8_t pd[kBank], pa[kBank];
        for (uint32_t i = 0; i < nb; ++i) { pd[i] = uint8_t(i); pa[i] = uint8_t(i); }
        for (uint32_t i = nb - 1; i > 0; --i) { const uint32_t j = le32(x.data() + off) % (i + 1); off += 4; std::swap(pd[i], pd[j]); }
        for (uint32_t i = nb - 1; i > 0; --i) { const uint32_t j = le32(x.data() + off) % (i + 1); off += 4; std::swap(pa[i], pa[j]); }
        if (rev5 || rev9 || rev11) for (uint32_t i = 0; i < nb; ++i) pa[i] = pd[(i + 1) % nb];  // revisions 5, 9 and 11: the read a derangement of the write (the second permutation's words still consumed)
        if (rev9 || rev11) first_write[size_t(r) * 4 + bk] = pd[0];
        for (uint32_t i = 0; i < nb; ++i) {
            const uint8_t* q = x.data() + off; off += 6;
            op_t& o = prog[size_t(r) * per_pass + 4 * i + bk];
            uint8_t bb = uint8_t(q[1] % nb);
            if ((rev5 || rev9) && bb == pd[i]) bb = uint8_t((bb + 1) % nb);  // revisions 5 and 9: b never the destination
            if (rev10 || rev11) while (bb == pd[i] || bb == pa[i]) bb = uint8_t((bb + 1) % nb);  // revisions 10 and 11: b off the destination and the first source (two steps at most)
            uint32_t bbank = bk;
            if (rev11 && (i & 3) == 3) bbank = (bk + 1 + ((q[1] >> 6) % 3)) & 3;  // revision 11: one operation in four reads b from another bank (the banks mix continuously)
            o.opc = light ? opcode_light(q[0]) : r16::opcode_of(q[0]); o.d = uint8_t(bk * nb + pd[i]); o.a = uint8_t(bk * nb + pa[i]); o.b = uint8_t(bbank * nb + bb); o.imm = le32(q + 2);
            if (rev13 || variant) { uint8_t cc = uint8_t((o.imm >> 24) % nb); while (cc == pd[i] || cc == pa[i] || (bbank == bk && cc == bb)) cc = uint8_t((cc + 1) % nb); o.c = uint8_t(bk * nb + cc); }  // revision 13 and the variants: the third register, in-bank, off a, b and d
        }
        if ((rev4 || rev5 || rev9) && !rev11 && bk == 3) for (uint32_t k = 0; k < 4; ++k) {  // (revision 10 passes rev9 = true)
            // revision 4: register (r mod nb) of bank k takes XOR register ((r + 7) mod nb) of bank k + 1 (mod 4): the banks mix once a pass
            op_t& o = prog[size_t(r) * per_pass + nr + k];
            o.opc = r16::XOR; o.d = uint8_t(k * nb + (r % nb)); o.a = o.d; o.b = uint8_t(((k + 1) & 3) * nb + ((r + 7) % nb)); o.imm = 0;
        }
    }
    if (rev9 || rev11) for (uint32_t r = 0; r + 1 < passes; ++r) for (uint32_t bk = 0; bk < 4; ++bk) {
        // the bank's last operation of pass r reads the register pass r + 1 writes first, so that value too is read before its overwrite
        op_t& o = prog[size_t(r) * per_pass + 4 * (nb - 1) + bk];
        o.a = uint8_t(bk * nb + first_write[size_t(r + 1) * 4 + bk]);
    }
}
inline void run(const std::vector<op_t>& prog, uint32_t* R) {
    for (const op_t& o : prog) {
        const uint32_t a = R[o.a], b = R[o.b];
        switch (o.opc) {
            case r16::MAD: R[o.d] = a * b + o.imm; break;
            case r16::ADD: R[o.d] = a + b + o.imm; break;
            case r16::SUB: R[o.d] = a - b - o.imm; break;
            case r16::XOR: R[o.d] = a ^ b ^ o.imm; break;
            case r16::ROTL: R[o.d] = rotl32(a, o.imm & 31) ^ b; break;
            default: R[o.d] = rotl32(a, 32 - (o.imm & 31)) ^ b; break;
        }
    }
}
// the compiled form: the programme as a flat table of (opcode | d << 8 | a << 16 | b << 24, imm) words, drawn once per (period, anchor)
// and held on the context; the run is a tight loop over the table (the same function as run(prog, R), the sets check it)
std::shared_ptr<const std::vector<uint32_t>> compiled(const ih_ctx& c, uint64_t height, const uint8_t* anchor, uint8_t K[32]) {
    period_key(c, height, anchor, K);
    const uint64_t period = height / c.p.period_blocks;
    const uint8_t* an = anchor ? anchor : c.p.program_anchor;
    std::lock_guard<std::mutex> g(c.prog_mu);
    for (const auto& sl : c.r16f_slots) if (sl.ops && sl.period == period && std::memcmp(sl.anchor, an, 32) == 0) return sl.ops;
    const bool r6 = (c.p.program_distance & 16) != 0, r7 = (c.p.program_distance & 32) != 0, r8 = (c.p.program_distance & 64) != 0, r10 = (c.p.program_distance & 256) != 0, r9 = (c.p.program_distance & 128) != 0 || r10, r11 = (c.p.program_distance & 512) != 0, r12 = (c.p.program_distance & 1024) != 0, r13 = (c.p.program_distance & 2048) != 0;
    const uint32_t nb = c.p.registers ? c.p.registers / 4 : kBank, nr = 4 * nb;
    const uint32_t variant = (c.p.program_distance >> 12) & 7;  // 5 = 14d with the destination consumed on every permute (the design seat's reading: prmt is not a bijection in a)
    std::vector<op_t> prog; draw_program(K, c.p.rounds, prog, (c.p.program_distance & 2) != 0, (c.p.program_distance & 4) != 0, (c.p.program_distance & 8) != 0 || r6 || r7 || r8, r9, r10, r11, nb, r13, variant);
    // the table: two words an operation (the operation word and the immediate), three under a variant (the third: c | the nonce-table
    // index << 8 | the variant's flags << 16: 1 the class from two bits of c, 2 the byte permute by c, 4 the immediate from the nonce table)
    const uint32_t stride = variant ? 3 : 2;
    auto tab = std::make_shared<std::vector<uint32_t>>(prog.size() * stride);
    const bool rev4 = (c.p.program_distance & 4) != 0, rev5 = (c.p.program_distance & 8) != 0;
    const uint32_t per_pass = nr + ((rev4 || rev5 || r6 || r7 || r8 || r9) && !r11 ? 4 : 0);
    for (size_t j = 0; j < prog.size(); ++j) {
        uint32_t opc = prog[j].opc;
        if (prog[j].opc == r16::MAD && (rev4 || rev5 || r6 || r7 || r8 || r9 || r11)) opc = 6;  // 6: the odd multiplier, a x (b | 1) + imm (revisions 9 and 11: the plain forms otherwise)
        if (rev5 && (j % per_pass) < nr) opc += 8;                     // 8 to 14: revision 5's bijective forms, d = d_old + rotl(g, (imm >> 5) & 31); the cross-bank XORs stay plain
        if ((r6 || (r7 && ((j / per_pass) & 1))) && (j % per_pass) < nr) opc += 16;  // 16 to 22: revision 6's fused forms (6b on the odd passes)
        if (r8 && (j % per_pass) < nr) opc += 24;                      // 24 to 30: revision 8's form A, d = d_old ^ g
        if (r12 && j < 4) opc += 24;                                      // revision 12: pass 0's position-0 operation of each bank (the first four of the programme) consumes its destination, d = d_old ^ g
        uint32_t imm = prog[j].imm;
        if (r13 && prog[j].opc != r16::MAD) { opc = (j < 4 ? 40 : 32) + prog[j].opc; imm = prog[j].c; }  // revision 13: the three-operand forms (32 to 37; 40 to 45 consuming the destination), the third register in the immediate's slot; the odd MAD keeps its immediate
        (*tab)[stride * j] = opc | (uint32_t(prog[j].d) << 8) | (uint32_t(prog[j].a) << 16) | (uint32_t(prog[j].b) << 24); (*tab)[stride * j + 1] = imm;
        if (variant) {
            const uint32_t pos = uint32_t(j % per_pass) / 4;  // the position in the bank (the operation's index among its bank's)
            uint32_t flags = 0;
            if (variant == 1 && (pos & 3) == 1) flags = 1;
            if (variant == 2) flags = 1;
            if (variant == 3 && (pos & 3) == 1) flags = 4;
            if ((variant == 4 || variant == 5) && (pos & 3) == 1) flags = variant == 5 ? 10 : 2;  // 8: consume the destination on the permute
            if (variant == 6 && (pos & 3) == 1) flags = 16;  // 14d3: the permute on b and c, a carried whole by the XOR
            if (prog[j].opc == r16::MAD && (flags & 1)) flags = 0;  // the odd MAD keeps its class (the selection among the four lane-cheap classes)
            (*tab)[stride * j + 2] = uint32_t(prog[j].c) | (((imm >> 26) & 63) << 8) | (flags << 16);
        }
    }
    auto& sl = c.r16f_slots[c.r16f_next]; c.r16f_next = (c.r16f_next + 1) % 4;
    sl.period = period; std::memcpy(sl.anchor, an, 32); sl.ops = tab;
    return tab;
}
inline uint32_t prmt(uint32_t a, uint32_t b, uint32_t sel) {
    // the byte permute (PRMT, v_perm): result byte k = byte (sel >> 4k) & 7 of the eight bytes {a, b}
    uint32_t r = 0;
    for (uint32_t k = 0; k < 4; ++k) { const uint32_t i = (sel >> (4 * k)) & 7; const uint32_t src = i < 4 ? a : b; r |= ((src >> (8 * (i & 3))) & 255) << (8 * k); }
    return r;
}
inline void run_compiled(const std::vector<uint32_t>& tab, uint32_t* R, uint32_t stride = 2, const uint32_t* T = nullptr) {
    const uint32_t* t = tab.data(); const size_t n = tab.size() / stride;
    for (size_t j = 0; j < n; ++j) {
        const uint32_t w = t[stride * j]; uint32_t imm = t[stride * j + 1];
        const uint32_t a = R[(w >> 16) & 255], b = R[w >> 24]; uint32_t& d = R[(w >> 8) & 255];
        const uint32_t opc = w & 255;
        if (stride == 3) {
            const uint32_t x = t[stride * j + 2], cv = R[x & 255], flags = x >> 16;
            if (flags & 4) imm = T[(x >> 8) & 63];  // 14c: the immediate from the per-nonce table
            if (flags & 1) {  // 14a and 14b: the class from two bits of c
                uint32_t g;
                switch (cv & 3) { case 0: g = a + b + imm; break; case 1: g = a ^ b ^ imm; break; case 2: g = rotl32(a, imm & 31) ^ b; break; default: g = rotl32(a, 32 - (imm & 31)) ^ b; break; }
                if (opc >= 24) d ^= g; else d = g;
                continue;
            }
            if (flags & 16) {  // 14d3 (the design seat's reading 23:48 UK): d = a ^ prmt(b, c, imm), the PRMT instruction kept, a bijective
                const uint32_t g = a ^ prmt(b, cv, imm);
                if (opc >= 24) d ^= g; else d = g;
                continue;
            }
            if (flags & 2) {  // 14d: the byte permute by c; variant 5 consumes the destination on every permute (prmt is not a bijection in a)
                const uint32_t g = prmt(a, b, cv) ^ imm;
                if (opc >= 24 || (flags & 8)) d ^= g; else d = g;
                continue;
            }
        }
        if (opc >= 32) {
            // revision 13: the three-operand forms, c = R[imm]; 40 to 45 consume the destination
            const uint32_t cv = R[imm]; uint32_t g;
            switch ((opc - 32) & 7) {
                case r16::ADD: g = a + b + cv; break;
                case r16::SUB: g = a - b - cv; break;
                case r16::XOR: g = a ^ b ^ cv; break;
                case r16::ROTL: g = rotl32(a, cv & 31) ^ b; break;
                default: g = rotl32(a, 32 - (cv & 31)) ^ b; break;  // ROTR
            }
            if (opc >= 40) d ^= g; else d = g;
            continue;
        }
        if (opc >= 24) {
            // revision 8 (form A): the destination's old value XORed with revision 4's result
            uint32_t g;
            switch (opc - 24) {
                case r16::ADD: g = a + b + imm; break;
                case r16::SUB: g = a - b - imm; break;
                case r16::XOR: g = a ^ b ^ imm; break;
                case r16::ROTL: g = rotl32(a, imm & 31) ^ b; break;
                case r16::ROTR: g = rotl32(a, 32 - (imm & 31)) ^ b; break;
                default: g = a * (b | 1u) + imm; break;
            }
            d ^= g;
            continue;
        }
        if (opc >= 16) {
            // revision 6: the bijection of the destination as one fused three-input instruction per class
            switch (opc - 16) {
                case r16::ADD: d = d + a + b + imm; break;
                case r16::SUB: d = d + a - b - imm; break;
                case r16::XOR: d = d ^ a ^ b ^ imm; break;
                case r16::ROTL: d = d + (rotl32(a, imm & 31) ^ b); break;
                case r16::ROTR: d = d + (rotl32(a, 32 - (imm & 31)) ^ b); break;
                default: d = d + a * (b | 1u) + imm; break;
            }
            continue;
        }
        if (opc >= 8) {
            // revision 5: g by the revision 4 forms, rotated by the immediate's bits 5 to 9, added to the destination's old value
            uint32_t g;
            switch (opc - 8) {
                case r16::MAD: g = a * (b | 1u) + imm; break;
                case r16::ADD: g = a + b + imm; break;
                case r16::SUB: g = a - b - imm; break;
                case r16::XOR: g = a ^ b ^ imm; break;
                case r16::ROTL: g = rotl32(a, imm & 31) ^ b; break;
                case r16::ROTR: g = rotl32(a, 32 - (imm & 31)) ^ b; break;
                default: g = a * (b | 1u) + imm; break;
            }
            d = d + rotl32(g, (imm >> 5) & 31);
            continue;
        }
        switch (opc) {
            case r16::MAD: d = a * b + imm; break;
            case r16::ADD: d = a + b + imm; break;
            case r16::SUB: d = a - b - imm; break;
            case r16::XOR: d = a ^ b ^ imm; break;
            case r16::ROTL: d = rotl32(a, imm & 31) ^ b; break;
            case r16::ROTR: d = rotl32(a, 32 - (imm & 31)) ^ b; break;
            default: d = a * (b | 1u) + imm; break;  // 6: revision 4's MAD, the odd multiplier (a bijection in a for a fixed b)
        }
    }
}
inline uint32_t fchain(uint32_t x, uint32_t v) { x = rotl32(x ^ v, 5) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }  // F(x; v) with the finaliser (the R0E fold)
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32];
    const auto tab = compiled(c, height, anchor, K);
    const uint32_t nregs = c.p.registers ? c.p.registers : kRegs;
    uint32_t R[kRegs];
    if (c.p.program_distance & 1) {
        // revision 3: the init ONE BLAKE3 of (tag || K || S) to 32 bytes, expanded over the 224 registers by eight fold chains (register
        // r = F(x; r) on chain r mod 8 from the digest's word), so every register is a function of K and S
        uint8_t d[32]; { bytes in; in.tag(kTagInit).raw(K, 32).raw(S, 32); b3_hash(in, d, 32); }
        uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(d + 4 * k);
        for (uint32_t r = 0; r < nregs; ++r) { x[r & 7] = fchain(x[r & 7], r); R[r] = x[r & 7]; }
    } else {
        bytes in; in.tag(kTagInit).raw(K, 32).raw(S, 32); uint8_t x[4 * kRegs]; b3_hash(in, x, 4 * nregs); for (uint32_t k = 0; k < nregs; ++k) R[k] = le32(x + 4 * k);
    }
    if ((c.p.program_distance >> 12) & 7) {
        // the variants: the per-nonce immediate table (14c reads it; the others carry it unread), 64 words from BLAKE3xof(LP("IgneumHash/R0G/nonce") || K || S)
        uint32_t T[64]; { bytes in; in.tag("IgneumHash/R0G/nonce").raw(K, 32).raw(S, 32); uint8_t x[256]; b3_hash(in, x, sizeof x); for (int k = 0; k < 64; ++k) T[k] = le32(x + 4 * k); }
        run_compiled(*tab, R, 3, T);
    } else run_compiled(*tab, R);
    if (c.p.program_distance & 1) {
        // revision 3: the mix the fold over the 224 registers into eight words (chain r mod 8 from K's word), then ONE BLAKE3 of
        // (tag || K || the eight words): every register binds the mix
        uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(K + 4 * k);
        for (uint32_t r = 0; r < nregs; ++r) x[r & 7] = fchain(x[r & 7], R[r]);
        bytes in; in.tag(kTagMix).raw(K, 32); for (int k = 0; k < 8; ++k) in.le32(x[k]);
        b3_hash(in, M, 32);
    } else {
        bytes in; in.tag(kTagMix).raw(K, 32); for (uint32_t k = 0; k < nregs; ++k) in.le32(R[k]);
        b3_hash(in, M, 32);
    }
}
}  // namespace r16f

// REVISION 15, THE STREAMING FABRIC, and 15b, THE WIDE VOCABULARY (the project's order of 23:4x UK). The stream: blocks of W = N / 4 operations
// (N the document's registers: 144, W 36), position i of a block in lane i mod 4 (nine positions a lane at W 36); operation j (block k,
// position i) reads a = the value block k - 1 produced at a position of the SAME lane by a drawn permutation of the lane's positions
// (every value read exactly once as a, within 2W operations of its write), b = a value of block k - 1 at a drawn position (the same lane
// for positions with i mod 4 under 3, another lane for i mod 4 = 3), off a's; the result v_j goes to ring register j mod N, whose value
// (v_(j - N)) is dead by then (N at or above 2W). Block 0 reads the init (W values from the one BLAKE3 of (R0G/init || K || S) expanded
// by the eight fold chains, held in ring registers N - W .. N - 1). The state at the end is the ring's N registers, mixed as revision 3.
// The classes: revision 4's six (bijective in a); 15b adds four, each one native instruction bijective in a: MULHI d = a + mulhi(b,
// imm | 1) (IMAD.HI.U32 / v_mad_u64_u32's high word; Metal mulhi + add; OpenCL mul_hi + add), LOP3 d = a ^ T_imm(b, c) with T the 16
// two-input tables in imm's low nibble over a third previous-block value c (LOP3 / v_lop3 with the 8-bit table 0x3C ^ (T expanded);
// Metal and OpenCL the expression), PRMT d = a ^ prmt(b, c, imm) with the selector imm's low 16 bits (PRMT / v_perm_b32; Metal and
// OpenCL a uchar8 shuffle), LEA d = a + (b << (1 + imm mod 31)) (LEA / v_lshl_add_u32; Metal and OpenCL the expression); ten
// classes at even weights (26 of 256 each, LEA 22). The design seat's rule 23:48 UK holds for every class: the first source enters
// bijectively (xor, add, subtract, rotate, odd multiply); every select, table, permute, shift or even multiply is on b and c only.
namespace r16s {
enum : uint8_t { S_ADD = 0, S_SUB, S_XOR, S_ROTL, S_ROTR, S_MAD, S_MULHI, S_LOP3, S_PRMT, S_LEA };
inline uint8_t opcode6(uint8_t q) { return r16::opcode_of(q); }  // cand-14's table over the six (ADD 0 .. ROTR 5 by r16's enum)
inline uint8_t opcode10(uint8_t q) { const uint8_t k = uint8_t(q / 26); return k > 9 ? S_MAD : k; }  // even: ADD < 26, SUB < 52, XOR < 78, ROTL < 104, ROTR < 130, MAD < 156, MULHI < 182, LOP3 < 208, PRMT < 234, LEA < 256 (the last 22 to LEA... no: q / 26 = 9 for 234..255)
inline uint8_t map6(uint8_t r16opc) { switch (r16opc) { case r16::ADD: return S_ADD; case r16::SUB: return S_SUB; case r16::XOR: return S_XOR; case r16::ROTL: return S_ROTL; case r16::ROTR: return S_ROTR; default: return S_MAD; } }
struct sop { uint8_t opc, d, a, b, c; uint32_t imm; };
std::shared_ptr<const std::vector<uint32_t>> compiled(const ih_ctx& c, uint64_t height, const uint8_t* anchor, uint8_t K[32]) {
    r16f::period_key(c, height, anchor, K);
    const uint64_t period = height / c.p.period_blocks;
    const uint8_t* an = anchor ? anchor : c.p.program_anchor;
    std::lock_guard<std::mutex> g(c.prog_mu);
    for (const auto& sl : c.r16f_slots) if (sl.ops && sl.period == period && std::memcmp(sl.anchor, an, 32) == 0) return sl.ops;
    const uint32_t N = c.p.registers ? c.p.registers : 224, W = N / 4, L = W / 4, blocks = c.p.rounds;
    const bool wide = (c.p.program_distance & 65536) != 0, inplace = (c.p.program_distance & 131072) != 0;
    // REVISIONS 17 and 18 (the design seat's reading 00:1x UK: the block map of 15 is a parallel update in which differing inputs cancel,
    // the avalanche plateaus at a fifth): the stream consumes the ring IN PLACE. Every register is initialised; block k, lane ln, slot t
    // reads a = R[ln nb + pool[t]] with pool a partial Fisher-Yates of the bank's nb registers (L distinct a block and bank, every
    // register consumed once in nb / L blocks), b from any of the N registers (16 bits mod N, not a's), c likewise (not a's or b's),
    // in program order (b and c read values already updated in the block), and writes the result over a's register: one read, one
    // overwrite, a single-register bijection an operation; the N-register file live throughout (the guarantee of revision 12 kept).
    // the stream's bytes: per block and lane, a permutation of L (L - 1 words), then per position 8 bytes (opcode, b, c, pad, LE32 imm)
    const size_t per_block = size_t(4) * (size_t(L - 1) * 4) + size_t(W) * 8;
    bytes in; in.tag(r16f::kTagProg).raw(K, 32);
    std::vector<uint8_t> x(size_t(blocks) * per_block); b3_hash(in, x.data(), x.size());
    std::vector<sop> prog(size_t(blocks) * W);
    size_t off = 0;
    for (uint32_t k = 0; k < blocks; ++k) {
        uint8_t pa[4][64], pool[4][64];
        const uint32_t nb = N / 4;
        if (inplace) { for (uint32_t ln = 0; ln < 4; ++ln) { for (uint32_t i = 0; i < nb; ++i) pool[ln][i] = uint8_t(i); for (uint32_t t = 0; t + 1 < L; ++t) { const uint32_t j = t + le32(x.data() + off) % (nb - t); off += 4; std::swap(pool[ln][t], pool[ln][j]); } } }
        else for (uint32_t ln = 0; ln < 4; ++ln) { for (uint32_t i = 0; i < L; ++i) pa[ln][i] = uint8_t(i); for (uint32_t i = L - 1; i > 0; --i) { const uint32_t j = le32(x.data() + off) % (i + 1); off += 4; std::swap(pa[ln][i], pa[ln][j]); } }
        for (uint32_t i = 0; i < W; ++i) {
            const uint8_t* q = x.data() + off; off += 8;
            const uint32_t ln = i & 3, slot = i >> 2;
            sop& o = prog[size_t(k) * W + i];
            o.opc = wide ? opcode10(q[0]) : map6(opcode6(q[0]));
            if (inplace) {
                const uint32_t ra = ln * nb + pool[ln][slot];
                uint32_t rb = (uint32_t(q[1]) | (uint32_t(q[3]) << 8)) % N; if (rb == ra) rb = (rb + 1) % N;
                uint32_t rc = uint32_t(q[2]) % N; while (rc == ra || rc == rb) rc = (rc + 1) % N;
                o.a = uint8_t(ra); o.b = uint8_t(rb); o.c = uint8_t(rc); o.d = uint8_t(ra); o.imm = le32(q + 4);
                continue;
            }
            const int64_t base = int64_t(k) * W - int64_t(W);  // the previous block's first value index (block 0: the init at -W)
            auto reg_of = [&](int64_t vj) -> uint8_t { return uint8_t(((vj % int64_t(N)) + int64_t(N)) % int64_t(N)); };
            const uint32_t apos = 4 * pa[ln][slot] + ln;  // the lane's drawn position in the previous block
            uint32_t bpos;
            if (ln == 3) { const uint32_t bl = (ln + 1 + ((q[1] >> 6) % 3)) & 3; bpos = 4 * (q[1] % L) + bl; }
            else { uint32_t bs = q[1] % L; if (bs == pa[ln][slot]) bs = (bs + 1) % L; bpos = 4 * bs + ln; }
            uint32_t cs = q[2] % L; if (cs == pa[ln][slot]) cs = (cs + 1) % L; if (ln != 3 && 4 * cs + ln == bpos) cs = (cs + 1) % L; if (cs == pa[ln][slot]) cs = (cs + 1) % L;
            o.a = reg_of(base + apos); o.b = reg_of(base + bpos); o.c = reg_of(base + 4 * cs + ln);
            o.d = reg_of(int64_t(k) * W + i);
            o.imm = le32(q + 4);
        }
    }
    auto tab = std::make_shared<std::vector<uint32_t>>(prog.size() * 3);
    for (size_t j = 0; j < prog.size(); ++j) { (*tab)[3 * j] = uint32_t(prog[j].opc) | (uint32_t(prog[j].d) << 8) | (uint32_t(prog[j].a) << 16) | (uint32_t(prog[j].b) << 24); (*tab)[3 * j + 1] = prog[j].imm; (*tab)[3 * j + 2] = prog[j].c; }
    auto& sl = c.r16f_slots[c.r16f_next]; c.r16f_next = (c.r16f_next + 1) % 4;
    sl.period = period; std::memcpy(sl.anchor, an, 32); sl.ops = tab;
    return tab;
}
inline uint32_t lop3_t(uint32_t b, uint32_t c, uint32_t t) {
    // the two-input table t (4 bits): bit (2 cbit + bbit) of t gives the result bit
    uint32_t r = 0;
    for (uint32_t bit = 0; bit < 4; ++bit) if ((t >> bit) & 1) { const uint32_t bb = bit & 1, cb = bit >> 1; r |= (bb ? b : ~b) & (cb ? c : ~c); }
    return r;
}
inline void run(const std::vector<uint32_t>& tab, uint32_t* R) {
    const uint32_t* t = tab.data(); const size_t n = tab.size() / 3;
    for (size_t j = 0; j < n; ++j) {
        const uint32_t w = t[3 * j], imm = t[3 * j + 1], cidx = t[3 * j + 2];
        const uint32_t a = R[(w >> 16) & 255], b = R[w >> 24]; uint32_t& d = R[(w >> 8) & 255];
        switch (w & 255) {
            case S_ADD: d = a + b + imm; break;
            case S_SUB: d = a - b - imm; break;
            case S_XOR: d = a ^ b ^ imm; break;
            case S_ROTL: d = rotl32(a, imm & 31) ^ b; break;
            case S_ROTR: d = rotl32(a, 32 - (imm & 31)) ^ b; break;
            case S_MAD: d = a * (b | 1u) + imm; break;
            case S_MULHI: d = a + uint32_t((uint64_t(b) * uint64_t(imm | 1u)) >> 32); break;
            case S_LOP3: d = a ^ lop3_t(b, R[cidx], imm & 15); break;
            case S_PRMT: d = a ^ r16f::prmt(b, R[cidx], imm); break;  // 14d3's form: the selector the immediate's low 16 bits
            default: d = a + (b << (1 + (imm % 31))); break;  // LEA
        }
    }
}
// REVISION 19, MEMORY FUSED INTO THE FABRIC (the founder's ask of 07:4x UK, 14 October: the class KawPoW has and the fabric had not):
// revision 15's stream, and every K operations (K = 8, 16 or 32 by the document's "read_every") one random 64-byte read from the
// bandwidth discipline's dataset (the document's dataset_mib, cache_mib, parents and item_bytes 256 as version 15's; the epoch
// dataset the card holds for coordinate 1, no new memory): the address from the state (the word the operation before produced, XOR
// the key word of the read's index, item = address >> 2 masked to the items, quarter = address & 3), the 16 words of the quarter
// folded into the next 16 operations (each XORed into its immediate). The reads of a period are a DRAM term on every attempt.
inline void run_mem(const ih_ctx& c, const std::vector<uint32_t>& tab, uint32_t* R, const uint8_t K[32]) {
    const uint32_t* t = tab.data(); const size_t n = tab.size() / 3;
    const uint32_t every = 8u << ((c.p.program_distance >> 24) & 3);
    const uint64_t mask = c.n_items() - 1;
    uint32_t Kw[8]; for (int k = 0; k < 8; ++k) Kw[k] = le32(K + 4 * k);
    // REVISION 19b (the project's reading 09:1x UK on 19's 5090 row, 34.4 uJ: 900 reads an attempt as a dependent chain of DRAM latencies):
    // the reads in BATCHES of eight: at the start of every block of 8K operations (j mod 8K = 0, j = 0 included) the eight addresses
    // are the eight most recent values of the ring (v_(j - 1 - t), t = 0..7; the init at j = 0) XOR the key words, independent of each
    // other and issued together; their 8 x 16 words queue in order and fold into the following operations one a word (128 operations;
    // a queue, so at K 8 the folds run past the block). Bit 26 of the slot.
    // REVISION 19d (the project's reading 09:4x UK on 19b's rows, the same as 19's: the card is bound by random-read throughput, a row opened
    // for 64 bytes of use): 19b's batches of eight at 256 BYTES a read (the whole item, version 15's walk access size), a quarter as
    // many (a batch every 32K operations), the 8 x 64 words queued into the following operations; the bytes an attempt as 19b's. Bit 27.
    const bool batched = (c.p.program_distance & 67108864) != 0, whole = (c.p.program_distance & 134217728) != 0;
    const uint32_t N = c.p.registers ? c.p.registers : 224, period = (whole ? 32u : 8u) * every, wpr = whole ? 64u : 16u;
    uint32_t item[64], fold[512]; uint32_t fold_left = 0, fold_at = 0, last = 0, nread = 0;
    // the eight registers the previous eight operations wrote (ring registers N - 1 - t before any write): on the stream these hold
    // v_(j - 1 - t); on the in-place form (19c) the registers rewritten last
    uint8_t lastd[8]; for (uint32_t t = 0; t < 8; ++t) lastd[t] = uint8_t(N - 1 - t);
    for (size_t j = 0; j < n; ++j) {
        if (batched) {
            if ((j % period) == 0) {
                for (uint32_t t = 0; t < 8; ++t) {
                    const uint32_t addr = R[lastd[t]] ^ Kw[(nread + t) & 7];
                    load_item(c, uint64_t(addr >> 2) & mask, item);
                    if (whole) for (int k = 0; k < 64; ++k) fold[64 * t + k] = item[k];
                    else for (int k = 0; k < 16; ++k) fold[16 * t + k] = item[16 * (addr & 3) + k];
                }
                nread += 8; fold_left = 8 * wpr; fold_at = 0;
            }
        } else if (j && (j % every) == 0) {
            const uint32_t addr = last ^ Kw[nread & 7]; ++nread;
            load_item(c, uint64_t(addr >> 2) & mask, item);
            for (int k = 0; k < 16; ++k) fold[k] = item[16 * (addr & 3) + k];
            fold_left = 16; fold_at = 0;
        }
        const uint32_t w = t[3 * j], cidx = t[3 * j + 2]; uint32_t imm = t[3 * j + 1];
        if (fold_left) { imm ^= fold[fold_at++]; --fold_left; }
        const uint32_t a = R[(w >> 16) & 255], b = R[w >> 24]; uint32_t& d = R[(w >> 8) & 255];
        switch (w & 255) {
            case S_ADD: d = a + b + imm; break;
            case S_SUB: d = a - b - imm; break;
            case S_XOR: d = a ^ b ^ imm; break;
            case S_ROTL: d = rotl32(a, imm & 31) ^ b; break;
            case S_ROTR: d = rotl32(a, 32 - (imm & 31)) ^ b; break;
            case S_MAD: d = a * (b | 1u) + imm; break;
            case S_MULHI: d = a + uint32_t((uint64_t(b) * uint64_t(imm | 1u)) >> 32); break;
            case S_LOP3: d = a ^ lop3_t(b, R[cidx], imm & 15); break;
            case S_PRMT: d = a ^ r16f::prmt(b, R[cidx], imm); break;
            default: d = a + (b << (1 + (imm % 31))); break;
        }
        last = d;
        for (uint32_t t = 7; t > 0; --t) lastd[t] = lastd[t - 1]; lastd[0] = uint8_t((w >> 8) & 255);
    }
}
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32];
    const auto tab = compiled(c, height, anchor, K);
    const uint32_t N = c.p.registers ? c.p.registers : 224, W = N / 4;
    uint32_t R[r16f::kRegs]; std::memset(R, 0, sizeof R);
    // the init: W values from the one BLAKE3 of (tag || K || S), expanded by the eight fold chains, in ring registers N - W .. N - 1
    uint8_t d[32]; { bytes in; in.tag(r16f::kTagInit).raw(K, 32).raw(S, 32); b3_hash(in, d, 32); }
    uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(d + 4 * k);
    const uint32_t ninit = (c.p.program_distance & 131072) ? N : W;  // in place: the whole file initialised
    for (uint32_t r = 0; r < ninit; ++r) { x[r & 7] = r16f::fchain(x[r & 7], r); R[N - ninit + r] = x[r & 7]; }
    if (c.p.program_distance & 8388608) run_mem(c, *tab, R, K); else run(*tab, R);
    // the mix: the fold over the ring's N registers into eight words, then one BLAKE3 (revision 3's form)
    for (int k = 0; k < 8; ++k) x[k] = le32(K + 4 * k);
    for (uint32_t r = 0; r < N; ++r) x[r & 7] = r16f::fchain(x[r & 7], R[r]);
    bytes in; in.tag(r16f::kTagMix).raw(K, 32); for (int k = 0; k < 8; ++k) in.le32(x[k]);
    b3_hash(in, M, 32);
}
}  // namespace r16s

// REVISION 16, THE WARP FABRIC (the project's idea 00:2x UK): one attempt per warp of 32 lanes, each lane its own N-register bank of the same
// attempt (the state 32 x N words), every lane executing the same programme (rounds blocks of W in-place operations, 25 x 36 = 900 a
// lane, 28,800 executed) on its own bank, and ONE OPERATION IN FOUR a cross-lane exchange whose source lane is DATA-DEPENDENT: d = a ^
// v with v = the b register of lane (R[c] & 31) of the reading lane (SHFL.IDX / ds_bpermute_b32 / simd_shuffle, one instruction on the
// card; a full 32-by-32 word crossbar on every such position for a wired chip). The reads of a shuffle see the values before the
// instruction in every lane (the SIMT semantics).
// REVISION 17, THE BRAIDED STREAM (the project's reading 00:3x UK: a correct form's cost is dependency density on an in-order-issue GPU): S
// sub-streams interleaved operation by operation (S in 4, 8, 16), each its own ring of W = N / S registers (S x W = N live), every
// register of a ring consumed in place once a cycle of W operations (a drawn permutation a cycle), b from the own ring (not a's), and
// ONE OPERATION IN FOUR reading b from ANOTHER sub-stream's newest word (the braid), c from the own ring.
// REVISION 18, THE SEGMENT POOL (the project's reading 01:0x UK: a wired chip cannot hold many programmes at once): the attempt is K = 8
// segments of 3,600 operations; for each segment slot the period draws a POOL of P = 8 programme variants (each a revision 15c
// in-place stream of 100 blocks), the nonce's init digest choosing one variant a slot (byte k of the digest, low three bits), so an
// attempt executes 8 of 64 compiled segments by a branch a segment; the state carried between the segments. THE ANCHOR ONE PERIOD
// AHEAD is the registration's rule for the caller: period N + 1's anchor is the chain's hash at the START of period N.
namespace r16w {
using r16s::sop;
std::shared_ptr<const std::vector<uint32_t>> compiled(const ih_ctx& c, uint64_t height, const uint8_t* anchor, uint8_t K[32]) {
    r16f::period_key(c, height, anchor, K);
    const uint64_t period = height / c.p.period_blocks;
    const uint8_t* an = anchor ? anchor : c.p.program_anchor;
    std::lock_guard<std::mutex> g(c.prog_mu);
    for (const auto& sl : c.r16f_slots) if (sl.ops && sl.period == period && std::memcmp(sl.anchor, an, 32) == 0) return sl.ops;
    const uint32_t N = c.p.registers ? c.p.registers : 224, W4 = N / 4, L = W4 / 4, nb = N / 4;
    const uint32_t form = (c.p.program_distance >> 18) & 3, sidx = (c.p.program_distance >> 20) & 3;
    const bool wide = (c.p.program_distance & 65536) != 0;
    std::vector<sop> prog;
    bytes in; in.tag(r16f::kTagProg).raw(K, 32);
    if (form == 2) {
        // the braid: total T = rounds x W4 operations, S sub-streams of T / S, each cycling its ring of W = N / S registers
        const uint32_t S = 4u << sidx, W = N / S, T = c.p.rounds * W4, per_stream = T / S, cycles = per_stream / W;
        const size_t per_cycle = size_t(W - 1) * 4 + size_t(W) * 8;
        std::vector<uint8_t> x(size_t(S) * cycles * per_cycle); b3_hash(in, x.data(), x.size());
        prog.resize(size_t(T));
        std::vector<std::vector<sop>> sub(S);
        std::vector<uint8_t> newest(S);
        for (uint32_t st = 0; st < S; ++st) newest[st] = uint8_t(st * W + W - 1);  // before any write: the ring's last register
        size_t off = 0;
        // drawn per sub-stream in cycles, then interleaved: operation j belongs to sub-stream j mod S
        std::vector<size_t> cursor(S, 0);
        std::vector<std::vector<uint8_t>> perm(S, std::vector<uint8_t>(W));
        std::vector<uint32_t> incycle(S, W);  // position within the current cycle (W = draw a new permutation)
        for (uint32_t j = 0; j < T; ++j) {
            const uint32_t st = j % S;
            if (incycle[st] == W) { for (uint32_t i = 0; i < W; ++i) perm[st][i] = uint8_t(i); for (uint32_t i = W - 1; i > 0; --i) { const uint32_t t = le32(x.data() + off) % (i + 1); off += 4; std::swap(perm[st][i], perm[st][t]); } incycle[st] = 0; }
            const uint8_t* q = x.data() + off; off += 8;
            const uint32_t pos = incycle[st]++;
            sop& o = prog[j];
            o.opc = wide ? r16s::opcode10(q[0]) : r16s::map6(r16s::opcode6(q[0]));
            const uint32_t ra = st * W + perm[st][pos];
            uint32_t rb;
            if ((pos & 3) == 3) { const uint32_t other = (st + 1 + (uint32_t(q[1]) % (S - 1))) % S; rb = newest[other]; }  // the braid
            else { uint32_t bs = uint32_t(q[1]) % W; if (st * W + bs == ra) bs = (bs + 1) % W; rb = st * W + bs; }
            uint32_t cs = uint32_t(q[2]) % W; while (st * W + cs == ra || st * W + cs == rb) cs = (cs + 1) % W;
            o.a = uint8_t(ra); o.b = uint8_t(rb); o.c = uint8_t(st * W + cs); o.d = uint8_t(ra); o.imm = le32(q + 4);
            newest[st] = uint8_t(ra);
        }
    } else {
        // the in-place draw (revision 15c's) over `blocks` blocks: the warp fabric's rounds (25) with a shuffle at every fourth
        // position, or the pool's 64 segments of 100 blocks in sequence
        const uint32_t blocks = form == 3 ? 64u * 100u : c.p.rounds;
        const size_t per_block = size_t(4) * (size_t(L - 1) * 4) + size_t(W4) * 8;
        std::vector<uint8_t> x(size_t(blocks) * per_block); b3_hash(in, x.data(), x.size());
        prog.resize(size_t(blocks) * W4);
        size_t off = 0;
        for (uint32_t k = 0; k < blocks; ++k) {
            uint8_t pool[4][64];
            for (uint32_t ln = 0; ln < 4; ++ln) { for (uint32_t i = 0; i < nb; ++i) pool[ln][i] = uint8_t(i); for (uint32_t t = 0; t + 1 < L; ++t) { const uint32_t j = t + le32(x.data() + off) % (nb - t); off += 4; std::swap(pool[ln][t], pool[ln][j]); } }
            for (uint32_t i = 0; i < W4; ++i) {
                const uint8_t* q = x.data() + off; off += 8;
                const uint32_t ln = i & 3, slot = i >> 2;
                sop& o = prog[size_t(k) * W4 + i];
                o.opc = wide ? r16s::opcode10(q[0]) : r16s::map6(r16s::opcode6(q[0]));
                const uint32_t ra = ln * nb + pool[ln][slot];
                uint32_t rb = (uint32_t(q[1]) | (uint32_t(q[3]) << 8)) % N; if (rb == ra) rb = (rb + 1) % N;
                uint32_t rc = uint32_t(q[2]) % N; while (rc == ra || rc == rb) rc = (rc + 1) % N;
                o.a = uint8_t(ra); o.b = uint8_t(rb); o.c = uint8_t(rc); o.d = uint8_t(ra); o.imm = le32(q + 4);
                if (form == 1 && ((c.p.program_distance & 4194304) ? (i & 1) == 1 : (i & 3) == 3)) o.opc = 255;  // the warp fabric: the shuffle position (16b: every second)
            }
        }
    }
    auto tab = std::make_shared<std::vector<uint32_t>>(prog.size() * 3);
    for (size_t j = 0; j < prog.size(); ++j) { (*tab)[3 * j] = uint32_t(prog[j].opc) | (uint32_t(prog[j].d) << 8) | (uint32_t(prog[j].a) << 16) | (uint32_t(prog[j].b) << 24); (*tab)[3 * j + 1] = prog[j].imm; (*tab)[3 * j + 2] = prog[j].c; }
    auto& sl = c.r16f_slots[c.r16f_next]; c.r16f_next = (c.r16f_next + 1) % 4;
    sl.period = period; std::memcpy(sl.anchor, an, 32); sl.ops = tab;
    return tab;
}
inline void run_range(const std::vector<uint32_t>& tab, size_t j0, size_t j1, uint32_t* R) {
    const uint32_t* t = tab.data();
    for (size_t j = j0; j < j1; ++j) {
        const uint32_t w = t[3 * j], imm = t[3 * j + 1], cidx = t[3 * j + 2];
        const uint32_t a = R[(w >> 16) & 255], b = R[w >> 24]; uint32_t& d = R[(w >> 8) & 255];
        switch (w & 255) {
            case r16s::S_ADD: d = a + b + imm; break;
            case r16s::S_SUB: d = a - b - imm; break;
            case r16s::S_XOR: d = a ^ b ^ imm; break;
            case r16s::S_ROTL: d = rotl32(a, imm & 31) ^ b; break;
            case r16s::S_ROTR: d = rotl32(a, 32 - (imm & 31)) ^ b; break;
            case r16s::S_MAD: d = a * (b | 1u) + imm; break;
            case r16s::S_MULHI: d = a + uint32_t((uint64_t(b) * uint64_t(imm | 1u)) >> 32); break;
            case r16s::S_LOP3: d = a ^ r16s::lop3_t(b, R[cidx], imm & 15); break;
            case r16s::S_PRMT: d = a ^ r16f::prmt(b, R[cidx], imm); break;
            default: d = a + (b << (1 + (imm % 31))); break;
        }
    }
}
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32];
    const auto tab = compiled(c, height, anchor, K);
    const uint32_t N = c.p.registers ? c.p.registers : 224, form = (c.p.program_distance >> 18) & 3;
    uint8_t d[32]; { bytes in; in.tag(r16f::kTagInit).raw(K, 32).raw(S, 32); b3_hash(in, d, 32); }
    uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(d + 4 * k);
    bytes out; out.tag(r16f::kTagMix).raw(K, 32);
    if (form == 1) {
        // the warp fabric: 32 lanes x N, every lane the same programme on its own bank; the shuffle positions read across the lanes
        static thread_local std::vector<uint32_t> R; R.assign(size_t(32) * N, 0u);
        for (uint32_t r = 0; r < 32 * N; ++r) { x[r & 7] = r16f::fchain(x[r & 7], r); R[r] = x[r & 7]; }
        const uint32_t* t = tab->data(); const size_t n = tab->size() / 3;
        uint32_t v[32];
        for (size_t j = 0; j < n; ++j) {
            const uint32_t w = t[3 * j];
            if ((w & 255) == 255) {
                const uint32_t ra = (w >> 16) & 255, rb = w >> 24, rc = t[3 * j + 2];
                for (uint32_t l = 0; l < 32; ++l) v[l] = R[size_t(R[size_t(l) * N + rc] & 31) * N + rb];
                for (uint32_t l = 0; l < 32; ++l) R[size_t(l) * N + ra] ^= v[l];
            } else for (uint32_t l = 0; l < 32; ++l) run_range(*tab, j, j + 1, R.data() + size_t(l) * N);
        }
        for (int k = 0; k < 8; ++k) x[k] = le32(K + 4 * k);
        for (uint32_t r = 0; r < 32 * N; ++r) x[r & 7] = r16f::fchain(x[r & 7], R[r]);
    } else {
        uint32_t R[r16f::kRegs]; std::memset(R, 0, sizeof R);
        for (uint32_t r = 0; r < N; ++r) { x[r & 7] = r16f::fchain(x[r & 7], r); R[r] = x[r & 7]; }
        if (form == 3) {
            const size_t seg = size_t(100) * (N / 4);  // 3,600 operations a segment at 144
            for (uint32_t k = 0; k < 8; ++k) { const size_t v = (size_t(k) * 8 + (d[k] & 7)) * seg; run_range(*tab, v, v + seg, R); }
        } else run_range(*tab, 0, tab->size() / 3, R);
        for (int k = 0; k < 8; ++k) x[k] = le32(K + 4 * k);
        for (uint32_t r = 0; r < N; ++r) x[r & 7] = r16f::fchain(x[r & 7], R[r]);
    }
    for (int k = 0; k < 8; ++k) out.le32(x[k]);
    b3_hash(out, M, 32);
}
}  // namespace r16w

// VERSION 17, THE TEXTURE FORM (namespace r17). The texture is static and shared by every chain and period (the probe's rule: texel
// (x, y) = fmix32(y * 4096 + x)); a card samples it through the texture unit (CUDA tex2D<unsigned int> with cudaFilterModePoint,
// cudaReadModeElementType, clamp addressing, at (x + 0.5, y + 0.5); the Metal, OpenCL, Vulkan and HIP forms the same NEAREST read),
// the reference computes the texel. The lean: a chip without integer-capable samplers computes fmix32 (six integer operations) or
// gathers from a 64 MiB copy at the bandwidth lane's cost a sample; the discipline favours the sampler path and its texture cache,
// it does not lock anyone out (the probe's verdict). Eight lanes a thread keep eight samples in flight; a lane's samples are
// dependent (the coordinates from the lane's state, the sampled value folded into it).
namespace r17 {
constexpr const char* kTagKey = "IgneumHash/R0H/key", *kTagInit = "IgneumHash/R0H/init", *kTagMix = "IgneumHash/R0H/mix";
inline uint32_t fmix32(uint32_t h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }
inline uint32_t texel(uint32_t x, uint32_t y) { return fmix32(y * 4096u + x); }
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32];
    { bytes in; in.tag(kTagKey).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor ? anchor : c.p.program_anchor, 32); b3_hash(in, K, 32); }
    uint32_t Kw[8]; for (int k = 0; k < 8; ++k) Kw[k] = le32(K + 4 * k);
    uint32_t st[8];
    { bytes in; in.tag(kTagInit).raw(K, 32).raw(S, 32); uint8_t d[32]; b3_hash(in, d, 32); for (int l = 0; l < 8; ++l) st[l] = le32(d + 4 * l); }
    // sample i in lane i mod 8: the coordinate word c = st[l] ^ Kw[(i >> 3) & 7], x = c & 4095, y = (c >> 12) & 4095 (the probe's
    // decode), t = texel(x, y); the output t in order; the lane's state st[l] = fmix32(st[l] ^ t ^ (i * 0x9E3779B1)).
    // 17b: the texture is the epoch dataset as R32 texels, side 2^b (b = 14 at 1 GiB, 15 at 4 GiB, 16 at 16 GiB): x = c & (side - 1),
    // y = (c >> b) & (side - 1) (c's top bits unused at b = 14), the texel the LE32 word at index y * side + x (item index >> 6, word
    // index & 63), through the same NEAREST sampler on the card, the item derived or read by the reference
    bytes out; out.tag(kTagMix).raw(K, 32);
    const bool keyed_tex = c.p.dataset_bytes != 0;
    uint32_t b = 14; if (keyed_tex) { uint64_t tx = c.p.dataset_bytes / 4; b = 0; while ((1ull << (2 * b)) < tx) ++b; }
    const uint32_t side_mask = keyed_tex ? ((1u << b) - 1u) : 0u;
    uint32_t item[64]; uint64_t item_at = ~uint64_t(0);
    for (uint32_t i = 0; i < c.p.rounds; ++i) {
        const uint32_t l = i & 7, cw = st[l] ^ Kw[(i >> 3) & 7];
        uint32_t t;
        if (keyed_tex) {
            const uint64_t idx = (uint64_t((cw >> b) & side_mask) << b) | uint64_t(cw & side_mask);
            const uint64_t it = idx >> 6; if (it != item_at) { load_item(c, it, item); item_at = it; }
            t = item[idx & 63];
        } else t = texel(cw & 4095u, (cw >> 12) & 4095u);
        out.le32(t);
        st[l] = fmix32(st[l] ^ t ^ (i * 0x9E3779B1u));
    }
    b3_hash(out, M, 32);
}
}  // namespace r17

// VERSION 18, THE RASTER FORM (namespace r18). The probe's rule exactly (pow/workers/common/raster_probe.h): framebuffer space x right,
// y DOWN, origin top-left, pixel (i, j) covering [i, i + 1) x [j, j + 1) with its sample at the centre; a triangle with integer vertices
// (pixel corners, 0 .. 8192) covers the pixels whose sample is inside under the top-left rule (a horizontal edge is TOP when the
// interior lies below it, a non-horizontal edge is LEFT when the interior lies to its right); a zero-area triangle draws nothing;
// the blend ONE + ONE, ADD, the UNORM8 target saturating (each byte min(255, old + new)); RGBA with R the low byte of the colour word.
// The lean: the rasteriser and the raster output units (the coverage test, the blend and the 256 MiB target's write path); a chip
// without them rasterises in software (exact integer edge functions, as the reference) at the ALU's cost a pixel and the memory
// lane's cost a byte. THE DIGEST: the triangles' bounding boxes (minx .. maxx - 1, miny .. maxy - 1 of the clamped vertices; an empty
// box contributes nothing) read AFTER every triangle is drawn, in triangle order, each box row-major, RGBA bytes, into one BLAKE3 with
// the key, so a pixel under several boxes is read once a box; the probe's full-framebuffer digest is the throughput row's, this the
// relation's (the verifier reads at most rounds x 31 x 31 pixels). The verifier holds one 256 MiB framebuffer a thread and clears
// the touched boxes after the attempt; a card holds its render target.
namespace r18 {
constexpr const char* kTagKey = "IgneumHash/R0I/key", *kTagInit = "IgneumHash/R0I/init", *kTagMix = "IgneumHash/R0I/mix";
// the target's side: 8192 (the probe's), or 16384 under 18b (REGISTERS 14 in the document, the 1 GiB target: the project's recut on F-129)
inline uint32_t side_of(const ih_ctx& c) { return c.p.registers == 14 ? 16384u : 8192u; }
inline int32_t clampv(int32_t v, uint32_t side) { return v < 0 ? 0 : v > int32_t(side) ? int32_t(side) : v; }
struct tri { int32_t x0, y0, x1, y1, x2, y2; uint32_t rgba; };
// the triangle from the state word s (the probe's derivation with s in the place of its sequence word; 18b: 14 bits a coordinate)
inline tri triangle_of(uint32_t s, uint32_t side, bool rule2 = false) {
    // the drawer's second drawing rule (rule2): the offsets and the colour from other constants, the blend a saturating SUBTRACT
    const uint32_t u = r17::fmix32(s ^ (rule2 ? 0x7F4A7C15u : 0x2545F491u)), c = r17::fmix32(s ^ (rule2 ? 0x165667B1u : 0x9E3779B9u));
    tri r;
    if (side == 16384u) { r.x0 = int32_t(s & 16383u); r.y0 = int32_t((s >> 14) & 16383u); } else { r.x0 = int32_t(s & 8191u); r.y0 = int32_t((s >> 13) & 8191u); }
    r.x1 = clampv(r.x0 + int32_t(u & 31u) - 15, side); r.y1 = clampv(r.y0 + int32_t((u >> 5) & 31u) - 15, side);
    r.x2 = clampv(r.x0 + int32_t((u >> 10) & 31u) - 15, side); r.y2 = clampv(r.y0 + int32_t((u >> 15) & 31u) - 15, side);
    r.rgba = c; return r;
}
// 18c: the target's keyed content, pixel (i, j) = the LE32 word at index j * side + i of the epoch dataset (item index >> 6, word & 63),
// R the low byte; the attempt's modified pixels kept in a sparse overlay read through to the dataset
struct keyed_target {
    const ih_ctx& c; uint32_t side; bool sub; std::unordered_map<uint32_t, uint32_t> over; uint32_t item[64]; uint64_t item_at = ~uint64_t(0);
    keyed_target(const ih_ctx& cc, uint32_t s, size_t reserve = 1u << 16) : c(cc), side(s), sub((cc.p.program_distance & 2) != 0) { if (reserve) over.reserve(reserve); }
    uint32_t base(uint32_t pix) { if (!c.p.dataset_bytes) return 0u; const uint64_t it = uint64_t(pix) >> 6; if (it != item_at) { load_item(c, it, item); item_at = it; } return item[pix & 63]; }  // the unkeyed form's target is zero at the attempt's start (F-219: drawn on the tiles)
    uint32_t get(uint32_t pix) { auto f = over.find(pix); return f == over.end() ? base(pix) : f->second; }
    void add(uint32_t pix, uint32_t rgba) {
        const uint32_t v = get(pix);
        uint32_t r = 0;
        for (int k = 0; k < 4; ++k) { const uint32_t o = (v >> (8 * k)) & 255u, n = (rgba >> (8 * k)) & 255u; r |= (sub ? (o > n ? o - n : 0u) : std::min<uint32_t>(255u, o + n)) << (8 * k); }
        over[pix] = r;
    }
};
inline void draw_keyed(keyed_target& kt, const tri& t) {
    const uint32_t kSize = kt.side;
    const int64_t ax = t.x0, ay = t.y0, bx = t.x1, by = t.y1, cx = t.x2, cy = t.y2;
    const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
    if (area == 0) return;
    const int64_t s = area > 0 ? 1 : -1;
    struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
    for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == s; else e[k].tl = (-dy > 0 ? 1 : -1) == s; }
    const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
    for (int64_t j = miny; j < maxy; ++j) for (int64_t i = minx; i < maxx; ++i) {
        const int64_t px = 2 * i + 1, py = 2 * j + 1; bool in = true;
        for (int k = 0; k < 3 && in; ++k) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * s; if (w < 0 || (w == 0 && !e[k].tl)) in = false; }
        if (!in) continue;
        kt.add(uint32_t(uint64_t(j) * kSize + uint64_t(i)), t.rgba);
    }
}
// 18d (the panel's addendum 543bed3b4, 14 October 21:xx UK: 18c's bytes unchanged, the verifier under the 250 ms list budget): the
// triangles and their boxes are known before any pixel is read (the chain does not read the target), so the verifier (1) marks
// every box's 32 x 32 cells and calls a box OVERLAPPING when a cell is shared with another box, (2) prefetches the dataset rows of
// the boxes ahead, (3) draws each box into its own 32 x 32 tile loaded from the dataset by an incremental edge-function
// rasteriser, the overlapping boxes alone reading and writing the shared overlay, and (4) digests the tiles in triangle order (an
// overlapping box's pixels from the overlay). The same bytes as the sparse overlay alone (18c's sets re-read exact).
struct box_t { int32_t minx, maxx, miny, maxy; bool overlap; };
inline box_t box_of(const tri& t) { box_t b; b.minx = std::min(t.x0, std::min(t.x1, t.x2)); b.maxx = std::max(t.x0, std::max(t.x1, t.x2)); b.miny = std::min(t.y0, std::min(t.y1, t.y2)); b.maxy = std::max(t.y0, std::max(t.y1, t.y2)); b.overlap = false; return b; }
// F-219: the buffers are the leased slot's (sc), never the thread's; the unkeyed form (dataset_bytes 0) draws over a zero base
// F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 3): the bond-keyed triangles. Triangle i is keyed iff (i x S) mod ROUNDS < S, its
// index k = (i x S) div ROUNDS (the version 8 rule's spread); m_0 = the work seed, m_k (k >= 1) = the eight state words st[0..7] as
// LE32 at the keyed triangle's turn, BEFORE its word w = st[i & 7] ^ Kw[...] is formed and before the state update; s_k through the one
// function ignh_bond_share (the prover: sk given; the verifier: the share given, replayed); the share's eight LE32 words XORed into the
// first eight pixels of the keyed triangle's box, row-major from (minx, miny) (the GPU team's precision: (minx + j, miny) while minx + j < maxx,
// continuing at (minx, miny + 1) on a narrower box, never past maxy; a box under eight pixels takes the share's first words only; an
// empty box takes none), each pixel's RGBA8 word as one LE32 (R the low byte) and the XOR on the target's word AT THAT MOMENT of the walk
// (the pre-fill, or an earlier triangle's result where boxes overlap), BEFORE the triangle is drawn, so the triangle's read-modify-write
// and the digest over the boxes carry the share; never a register, never the state words.
struct keyed_tri { const ih_ctx& c; uint32_t S; std::vector<int32_t> k; std::vector<std::array<uint8_t, 32>> share;
    keyed_tri(const ih_ctx& cc, uint32_t n) : c(cc), S((cc.p.features & 1024) ? cc.p.bond_rounds : 0u), k(n, -1), share(n) {}
    // at triangle i's turn (before w): the message, the share (made or given), the index; false for an unkeyed triangle
    bool turn(uint32_t i, const uint32_t st[8], const uint8_t Sd[32], const uint8_t* sk, uint8_t* shares, uint8_t* m_out) {
        if (!S || (uint64_t(i) * S) % c.p.rounds >= S) return false;
        const uint32_t kk = uint32_t((uint64_t(i) * S) / c.p.rounds);
        uint8_t m[32];
        if (kk == 0) std::memcpy(m, Sd, 32); else for (int l = 0; l < 8; ++l) put_le32(m + 4 * l, st[l]);
        if (m_out) std::memcpy(m_out + size_t(kk) * 32, m, 32);
        uint8_t* s = shares + size_t(kk) * 32;
        if (sk) ignh_bond_share(sk, kk, m, s);
        k[i] = int32_t(kk); std::memcpy(share[i].data(), s, 32);
        return true;
    }
    // the eight pixels of box b in the share's order: fn(j, x, y) for j = 0..7 while the box has them
    template <class F> static void pixels(const box_t& b, F fn) {
        int32_t x = b.minx, y = b.miny;
        for (int j = 0; j < 8; ++j) { if (y >= b.maxy || b.minx >= b.maxx) return; fn(j, x, y); if (++x >= b.maxx) { x = b.minx; ++y; } }
    }
    uint32_t word(uint32_t i, int j) const { return le32(share[i].data() + 4 * j); }
};

void attempt_keyed_fast(const ih_ctx& c, const uint8_t K[32], const uint32_t Kw[8], uint32_t st[8], uint8_t M[32], scratch& sc, const uint8_t Sd[32], const uint8_t* sk, uint8_t* shares, uint8_t* m_out) {
    const uint32_t kSize = side_of(c), n = c.p.rounds;
    std::vector<tri> tris(n); std::vector<box_t> boxes(n);
    keyed_tri kt_k(c, n);
    for (uint32_t i = 0; i < n; ++i) {
        kt_k.turn(i, st, Sd, sk, shares, m_out);
        const uint32_t l = i & 7, w = st[l] ^ Kw[(i >> 3) & 7];
        tris[i] = triangle_of(w, kSize, (c.p.program_distance & 2) != 0);
        st[l] = r17::fmix32(st[l] ^ tris[i].rgba ^ (i * 0x9E3779B1u));
        boxes[i] = box_of(tris[i]);
    }
    // the overlaps by 32 x 32 cells (a box spans at most two cells a side): the cells' keys sorted, a box overlapping when one of its
    // keys appears twice (the list cut of 15 October: no hash map, no per-hash allocation beyond the thread's buffers)
    std::vector<uint32_t>& keys = sc.keys; keys.clear(); keys.reserve(size_t(n) * 4);
    for (uint32_t i = 0; i < n; ++i) { const box_t& b = boxes[i]; if (b.minx >= b.maxx || b.miny >= b.maxy) continue; for (int32_t cy = b.miny >> 5; cy <= (b.maxy - 1) >> 5; ++cy) for (int32_t cx = b.minx >> 5; cx <= (b.maxx - 1) >> 5; ++cx) keys.push_back(uint32_t(cy) * (kSize >> 5) + uint32_t(cx)); }
    std::sort(keys.begin(), keys.end());
    auto shared_cell = [&](uint32_t key) { auto it = std::lower_bound(keys.begin(), keys.end(), key); return it != keys.end() && (it + 1) != keys.end() && *(it + 1) == key; };
    for (uint32_t i = 0; i < n; ++i) { box_t& b = boxes[i]; if (b.minx >= b.maxx || b.miny >= b.maxy) continue; for (int32_t cy = b.miny >> 5; cy <= (b.maxy - 1) >> 5 && !b.overlap; ++cy) for (int32_t cx = b.minx >> 5; cx <= (b.maxx - 1) >> 5; ++cx) if (shared_cell(uint32_t(cy) * (kSize >> 5) + uint32_t(cx))) { b.overlap = true; break; } }
    const uint8_t* data = c.dataset_v().empty() ? nullptr : c.dataset_v().data();
    keyed_target kt(c, kSize, 0);  // the overlay for the overlapping boxes (rare: grown on use), and the light derivation when the dataset is not resident
    // the slot's tiles, grown to THIS attempt's n x 1024 words when smaller: a slot's buffers are sized by the attempt that holds it
    // and never assumed from an earlier one (keys cleared above, dig cleared below), so a slot serves documents of any rounds or
    // target side in turn; not zeroed a hash (every pixel of a box is written before it is read)
    std::vector<uint32_t>& tiles = sc.tiles; if (tiles.size() < size_t(n) * 1024) tiles.resize(size_t(n) * 1024);
    auto base_word = [&](uint32_t pix) -> uint32_t { if (data) { const uint8_t* q = data + size_t(pix) * 4; return uint32_t(q[0]) | (uint32_t(q[1]) << 8) | (uint32_t(q[2]) << 16) | (uint32_t(q[3]) << 24); } return kt.base(pix); };
    auto prefetch_box = [&](uint32_t i) { if (!data) return; const box_t& b = boxes[i]; for (int32_t j = b.miny; j < b.maxy; ++j) { const uint8_t* q = data + (size_t(j) * kSize + size_t(b.minx)) * 4; __builtin_prefetch(q); __builtin_prefetch(q + 64); __builtin_prefetch(q + 124); } };
    for (uint32_t i = 0; i < 8 && i < n; ++i) prefetch_box(i);
    for (uint32_t i = 0; i < n; ++i) {
        if (i + 8 < n) prefetch_box(i + 8);
        const tri& t = tris[i]; const box_t& b = boxes[i];
        if (b.minx >= b.maxx || b.miny >= b.maxy) continue;
        uint32_t* tile = &tiles[size_t(i) * 1024];
        const int32_t bw = b.maxx - b.minx, bh = b.maxy - b.miny;
        // the tile's base: the dataset (or the overlay where an earlier overlapping box wrote)
        if (data && !b.overlap) { for (int32_t j = 0; j < bh; ++j) std::memcpy(&tile[j * 32], data + (size_t(b.miny + j) * kSize + size_t(b.minx)) * 4, size_t(bw) * 4); }  // the rows straight from the resident dataset (LE32 words)
        else if (!b.overlap && !c.p.dataset_bytes) { for (int32_t j = 0; j < bh; ++j) std::memset(&tile[j * 32], 0, size_t(bw) * 4); }  // the unkeyed form's zero target (F-219)
        else for (int32_t j = 0; j < bh; ++j) for (int32_t x = 0; x < bw; ++x) { const uint32_t pix = uint32_t(uint64_t(b.miny + j) * kSize + uint64_t(b.minx + x)); tile[j * 32 + x] = b.overlap ? kt.get(pix) : base_word(pix); }
        if (kt_k.k[i] >= 0) keyed_tri::pixels(b, [&](int j, int32_t x, int32_t y) {  // F-441: the share into the box's first eight pixels before the draw (the overlay too where boxes overlap)
            uint32_t& v = tile[(y - b.miny) * 32 + (x - b.minx)]; v ^= kt_k.word(i, j);
            if (b.overlap) kt.over[uint32_t(uint64_t(y) * kSize + uint64_t(x))] = v;
        });
        const int64_t ax = t.x0, ay = t.y0, bx = t.x1, by = t.y1, cx = t.x2, cy = t.y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
        if (area != 0) {
            const int64_t sg = area > 0 ? 1 : -1;
            // the three edge functions at the box's first sample (2 minx + 1, 2 miny + 1) in doubled coordinates, stepped by 2 dy / -2 dx
            struct E { int64_t w0, dx, dy; bool tl; } e[3];
            const int64_t vx[3] = { ax, bx, cx }, vy[3] = { ay, by, cy };
            for (int k = 0; k < 3; ++k) {
                const int64_t eax = vx[k], eay = vy[k], ebx = vx[(k + 1) % 3], eby = vy[(k + 1) % 3];
                const int64_t dx = ebx - eax, dy = eby - eay;
                e[k].tl = dy == 0 ? ((dx > 0 ? 1 : -1) == sg) : ((-dy > 0 ? 1 : -1) == sg);
                const int64_t px = 2 * b.minx + 1, py = 2 * b.miny + 1;
                e[k].w0 = ((2 * ebx - 2 * eax) * (py - 2 * eay) - (2 * eby - 2 * eay) * (px - 2 * eax)) * sg;
                e[k].dx = -(2 * eby - 2 * eay) * 2 * sg;  // the step in x (px + 2)
                e[k].dy = (2 * ebx - 2 * eax) * 2 * sg;   // the step in y (py + 2)
            }
            const uint32_t R = t.rgba & 255u, G = (t.rgba >> 8) & 255u, B = (t.rgba >> 16) & 255u, A = (t.rgba >> 24) & 255u;
            const bool sub = (c.p.program_distance & 2) != 0;
            auto bl = [sub](uint32_t o, uint32_t n) -> uint32_t { return sub ? (o > n ? o - n : 0u) : std::min<uint32_t>(255u, o + n); };
            for (int32_t j = 0; j < bh; ++j) {
                int64_t w0 = e[0].w0 + e[0].dy * j, w1 = e[1].w0 + e[1].dy * j, w2 = e[2].w0 + e[2].dy * j;
                for (int32_t x = 0; x < bw; ++x, w0 += e[0].dx, w1 += e[1].dx, w2 += e[2].dx) {
                    const bool in = !(w0 < 0 || (w0 == 0 && !e[0].tl)) && !(w1 < 0 || (w1 == 0 && !e[1].tl)) && !(w2 < 0 || (w2 == 0 && !e[2].tl));
                    if (!in) continue;
                    uint32_t& v = tile[j * 32 + x];
                    const uint32_t r0 = bl(v & 255u, R), g0 = bl((v >> 8) & 255u, G), b0 = bl((v >> 16) & 255u, B), a0 = bl((v >> 24) & 255u, A);
                    v = r0 | (g0 << 8) | (b0 << 16) | (a0 << 24);
                    if (b.overlap) kt.over[uint32_t(uint64_t(b.miny + j) * kSize + uint64_t(b.minx + x))] = v;
                }
            }
        }
    }
    // the digest: the boxes in triangle order, an overlapping box's pixels their final values in the overlay
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag(kTagMix).raw(K, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    // the boxes' bytes gathered into one buffer (LE32 a pixel, the box's rows in order) and digested in one update: the same bytes
    // as the row-by-row updates, fewer calls into the hasher and the SIMD paths fed whole chunks
    std::vector<uint8_t>& dig = sc.dig; dig.clear();
    for (uint32_t i = 0; i < n; ++i) {
        const box_t& b = boxes[i]; if (b.minx >= b.maxx || b.miny >= b.maxy) continue;
        const uint32_t* tile = &tiles[size_t(i) * 1024];
        const int32_t bw = b.maxx - b.minx, bh = b.maxy - b.miny;
        for (int32_t j = 0; j < bh; ++j) {
            const size_t at = dig.size(); dig.resize(at + size_t(bw) * 4);
            for (int32_t x = 0; x < bw; ++x) { const uint32_t v = b.overlap ? kt.get(uint32_t(uint64_t(b.miny + j) * kSize + uint64_t(b.minx + x))) : tile[j * 32 + x]; uint8_t* q = &dig[at + size_t(x) * 4]; q[0] = uint8_t(v); q[1] = uint8_t(v >> 8); q[2] = uint8_t(v >> 16); q[3] = uint8_t(v >> 24); }
        }
    }
    blake3_hasher_update(&h, dig.data(), dig.size());
    blake3_hasher_finalize(&h, M, 32);
}
void attempt_keyed(const ih_ctx& c, const uint8_t K[32], const uint32_t Kw[8], uint32_t st[8], uint8_t M[32], scratch& sc, const uint8_t Sd[32], const uint8_t* sk, uint8_t* shares, uint8_t* m_out) {
    if (!(c.p.program_distance & 1) && !raster_oracle()) { attempt_keyed_fast(c, K, Kw, st, M, sc, Sd, sk, shares, m_out); return; }  // the sparse-overlay form below is the oracle (ignpow_set_raster_oracle, the differential tests; bit 0 never set by the parser)
    const uint32_t kSize = side_of(c);
    keyed_target kt(c, kSize);
    std::vector<tri> tris(c.p.rounds);
    keyed_tri kt_k(c, c.p.rounds);
    for (uint32_t i = 0; i < c.p.rounds; ++i) {
        const bool keyed = kt_k.turn(i, st, Sd, sk, shares, m_out);
        const uint32_t l = i & 7, w = st[l] ^ Kw[(i >> 3) & 7];
        tris[i] = triangle_of(w, kSize, (c.p.program_distance & 2) != 0);
        st[l] = r17::fmix32(st[l] ^ tris[i].rgba ^ (i * 0x9E3779B1u));
        if (keyed) { const box_t b = box_of(tris[i]); keyed_tri::pixels(b, [&](int j, int32_t x, int32_t y) { const uint32_t pix = uint32_t(uint64_t(y) * kSize + uint64_t(x)); kt.over[pix] = kt.get(pix) ^ kt_k.word(i, j); }); }  // F-441: the share before the draw, on the target's word at this moment
        draw_keyed(kt, tris[i]);
    }
    // the digest: the boxes in order after every triangle is drawn, each pixel's result (the keyed content where untouched)
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag(kTagMix).raw(K, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    std::vector<uint8_t> row;
    for (uint32_t i = 0; i < c.p.rounds; ++i) {
        const tri& t = tris[i];
        const int32_t minx = std::min(t.x0, std::min(t.x1, t.x2)), maxx = std::max(t.x0, std::max(t.x1, t.x2)), miny = std::min(t.y0, std::min(t.y1, t.y2)), maxy = std::max(t.y0, std::max(t.y1, t.y2));
        for (int32_t j = miny; j < maxy; ++j) {
            row.resize(size_t(maxx - minx) * 4);
            for (int32_t x = minx; x < maxx; ++x) { const uint32_t v = kt.get(uint32_t(uint64_t(j) * kSize + uint64_t(x))); uint8_t* q = &row[size_t(x - minx) * 4]; q[0] = uint8_t(v); q[1] = uint8_t(v >> 8); q[2] = uint8_t(v >> 16); q[3] = uint8_t(v >> 24); }
            blake3_hasher_update(&h, row.data(), row.size());
        }
    }
    blake3_hasher_finalize(&h, M, 32);
}
inline void draw(uint8_t* fb, uint32_t kSize, const tri& t, bool sub = false) {
    const int64_t ax = t.x0, ay = t.y0, bx = t.x1, by = t.y1, cx = t.x2, cy = t.y2;
    const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
    if (area == 0) return;
    const int64_t s = area > 0 ? 1 : -1;
    struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
    for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == s; else e[k].tl = (-dy > 0 ? 1 : -1) == s; }
    const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
    const uint8_t R = uint8_t(t.rgba), G = uint8_t(t.rgba >> 8), B = uint8_t(t.rgba >> 16), A = uint8_t(t.rgba >> 24);
    for (int64_t j = miny; j < maxy; ++j) for (int64_t i = minx; i < maxx; ++i) {
        const int64_t px = 2 * i + 1, py = 2 * j + 1; bool in = true;
        for (int k = 0; k < 3 && in; ++k) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * s; if (w < 0 || (w == 0 && !e[k].tl)) in = false; }
        if (!in) continue;
        uint8_t* q = &fb[(size_t(j) * kSize + size_t(i)) * 4];
        if (sub) { q[0] = uint8_t(std::max<int>(0, q[0] - R)); q[1] = uint8_t(std::max<int>(0, q[1] - G)); q[2] = uint8_t(std::max<int>(0, q[2] - B)); q[3] = uint8_t(std::max<int>(0, q[3] - A)); }
        else { q[0] = uint8_t(std::min<int>(255, q[0] + R)); q[1] = uint8_t(std::min<int>(255, q[1] + G)); q[2] = uint8_t(std::min<int>(255, q[2] + B)); q[3] = uint8_t(std::min<int>(255, q[3] + A)); }
    }
}
// sk, shares and m_out (F-441): the bond-keyed form's inputs and outputs (null on an unkeyed document: ih_hash_a refuses a bit 10
// document before it reaches here, so the three are null together or set together)
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32], const uint8_t* sk = nullptr, uint8_t* shares = nullptr, uint8_t* m_out = nullptr) {
    uint8_t K[32];
    { bytes in; in.tag(kTagKey).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor ? anchor : c.p.program_anchor, 32); b3_hash(in, K, 32); }
    uint32_t Kw[8]; for (int k = 0; k < 8; ++k) Kw[k] = le32(K + 4 * k);
    uint32_t st[8];
    { bytes in; in.tag(kTagInit).raw(K, 32).raw(S, 32); uint8_t d[32]; b3_hash(in, d, 32); for (int l = 0; l < 8; ++l) st[l] = le32(d + 4 * l); }
    if (c.p.dataset_bytes || !raster_oracle()) {
        // 18c's keyed target, or (F-219) the unkeyed form on 18d's tiles over a zero base: one leased slot of the context's
        // bounded pool for the attempt, at most the parallel verification width of slots a context
        lease L(c.raster_pool(), parallel_width());
        attempt_keyed(c, K, Kw, st, M, *L.s, S, sk, shares, m_out);
        return;
    }
    // THE ORACLE of the unkeyed form (ignpow_set_raster_oracle, the differential tests; never a node's path): the normative
    // text's target, zero at the attempt's start (calloc), drawn and digested, freed with the attempt
    const uint32_t kSize = side_of(c);
    uint8_t* fb = static_cast<uint8_t*>(std::calloc(size_t(kSize) * kSize, 4)); if (!fb) std::abort();
    std::vector<tri> tris(c.p.rounds);
    keyed_tri kt_k(c, c.p.rounds);
    // triangle i in lane i mod 8: the word s = st[l] ^ Kw[(i >> 3) mod 8]; the lane's state st[l] = fmix32(st[l] ^ colour ^ (i * 0x9E3779B1))
    for (uint32_t i = 0; i < c.p.rounds; ++i) {
        const bool keyed = kt_k.turn(i, st, S, sk, shares, m_out);
        const uint32_t l = i & 7, w = st[l] ^ Kw[(i >> 3) & 7];
        tris[i] = triangle_of(w, kSize, (c.p.program_distance & 2) != 0);
        st[l] = r17::fmix32(st[l] ^ tris[i].rgba ^ (i * 0x9E3779B1u));
        if (keyed) { const box_t b = box_of(tris[i]); keyed_tri::pixels(b, [&](int j, int32_t x, int32_t y) { uint8_t* q = &fb[(size_t(y) * kSize + size_t(x)) * 4]; const uint32_t v = le32(q) ^ kt_k.word(i, j); put_le32(q, v); }); }  // F-441: the share before the draw
        draw(fb, kSize, tris[i], (c.p.program_distance & 2) != 0);
    }
    // the digest: the boxes in order after every triangle is drawn
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag(kTagMix).raw(K, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    for (uint32_t i = 0; i < c.p.rounds; ++i) {
        const tri& t = tris[i];
        const int32_t minx = std::min(t.x0, std::min(t.x1, t.x2)), maxx = std::max(t.x0, std::max(t.x1, t.x2)), miny = std::min(t.y0, std::min(t.y1, t.y2)), maxy = std::max(t.y0, std::max(t.y1, t.y2));
        for (int32_t j = miny; j < maxy; ++j) blake3_hasher_update(&h, &fb[(size_t(j) * kSize + size_t(minx)) * 4], size_t(maxx - minx) * 4);
    }
    blake3_hasher_finalize(&h, M, 32);
    std::free(fb);
}
}  // namespace r18

// VERSION 19 (docs/gpu-b/RASTER-V19-DESIGN.md sections 2 and 10): the card draws continuously. The key, the init digest, the
// eight-lane chain and the triangle's derivation are r18's (reach 15 at side 16384 gives r18's triangle word for word; reach 31
// takes six bits an offset, side 32768 fifteen bits a coordinate). R1: every box is computed from the keyed base alone, pixel (x, y)
// of box i = sat_add8x4(base(x, y), rgba_i) where the pixel centre lies inside triangle i by the top-left rule (r18's integer edge
// rule), base(x, y) otherwise; a zero-area triangle covers nothing and its box folds the base alone. R4: the fold over the box's
// pixels row-major, the state (a, b) carried across the whole box, a_0 = w_i (the triangle's seed word), b_0 = fmix32(w_i ^ (i x
// 0x9E3779B1)), a = fmix32(a ^ v ^ b), b = (b + a) ^ rotl(a, 13) (R4B: b enters a each step; the pair's map is a bijection of the 64-bit
// state for a fixed v, and a per-item shortcut would have to be tabled over the whole pair); acc_i = a || b as eight LE bytes; M = BLAKE3(LP(mix tag) || K ||
// acc_0 .. acc_{t-1}); H the R0A final over P, S and M as every raster version. No surface is written, no slot is leased.
namespace r19 {
inline uint32_t side_of(const ih_ctx& c) { return c.p.registers == 15 ? 32768u : 16384u; }
inline uint32_t rotl32(uint32_t x, uint32_t n) { return (x << n) | (x >> (32u - n)); }
inline r18::tri triangle_of(uint32_t s, uint32_t side, uint32_t reach) {
    const uint32_t u = r17::fmix32(s ^ 0x2545F491u), col = r17::fmix32(s ^ 0x9E3779B9u);
    r18::tri r;
    if (side == 32768u) { r.x0 = int32_t(s & 32767u); r.y0 = int32_t((s >> 15) & 32767u); } else { r.x0 = int32_t(s & 16383u); r.y0 = int32_t((s >> 14) & 16383u); }
    const uint32_t bits = reach == 31u ? 6u : 5u, mask = (1u << bits) - 1u; const int32_t rr = int32_t(reach);
    r.x1 = r18::clampv(r.x0 + int32_t(u & mask) - rr, side); r.y1 = r18::clampv(r.y0 + int32_t((u >> bits) & mask) - rr, side);
    r.x2 = r18::clampv(r.x0 + int32_t((u >> (2 * bits)) & mask) - rr, side); r.y2 = r18::clampv(r.y0 + int32_t((u >> (3 * bits)) & mask) - rr, side);
    r.rgba = col; return r;
}
inline uint32_t sat_add8x4(uint32_t v, uint32_t rgba) { uint32_t r = 0; for (int k = 0; k < 4; ++k) { const uint32_t o = (v >> (8 * k)) & 255u, n = (rgba >> (8 * k)) & 255u; r |= std::min<uint32_t>(255u, o + n) << (8 * k); } return r; }
// the box's fold: (a, b) over every pixel of the box row-major, the base word, the cover test and the saturating add as r18's draw_keyed
inline void fold_box(const ih_ctx& c, uint32_t side, const r18::tri& t, uint32_t i, uint32_t w, uint32_t* item, uint64_t& item_at, uint8_t acc[8]) {
    uint32_t a = w, b = r17::fmix32(w ^ (i * 0x9E3779B1u));
    const int64_t ax = t.x0, ay = t.y0, bx = t.x1, by = t.y1, cx = t.x2, cy = t.y2;
    const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
    const int64_t sg = area > 0 ? 1 : -1;
    struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
    for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == sg; else e[k].tl = (-dy > 0 ? 1 : -1) == sg; }
    const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
    for (int64_t j = miny; j < maxy; ++j) for (int64_t x = minx; x < maxx; ++x) {
        uint32_t v = 0;
        if (c.p.dataset_bytes) { const uint64_t pix = uint64_t(j) * side + uint64_t(x), it = pix >> 6; if (it != item_at) { load_item(c, it, item); item_at = it; } v = item[pix & 63]; }
        bool in = area != 0;
        if (in) { const int64_t px = 2 * x + 1, py = 2 * j + 1; for (int k = 0; k < 3 && in; ++k) { const int64_t ww = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * sg; if (ww < 0 || (ww == 0 && !e[k].tl)) in = false; } }
        if (in) v = sat_add8x4(v, t.rgba);
        a = r17::fmix32(a ^ v ^ b); b = (b + a) ^ rotl32(a, 13);   // R4B (the review lead's ruling 49e21b472 on the security review's 75fq): b mixed into a each step, so the tabulation argument closes on the whole 64-bit pair, not on a alone
    }
    put_le32(acc, a); put_le32(acc + 4, b);
}
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t S[32], uint8_t M[32]) {
    uint8_t K[32];
    { bytes in; in.tag(r18::kTagKey).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor ? anchor : c.p.program_anchor, 32); b3_hash(in, K, 32); }
    uint32_t Kw[8]; for (int k = 0; k < 8; ++k) Kw[k] = le32(K + 4 * k);
    uint32_t st[8];
    { bytes in; in.tag(r18::kTagInit).raw(K, 32).raw(S, 32); uint8_t d[32]; b3_hash(in, d, 32); for (int l = 0; l < 8; ++l) st[l] = le32(d + 4 * l); }
    const uint32_t side = side_of(c), n = c.p.rounds, reach = c.p.scratch_bytes;
    std::vector<uint8_t> accs(size_t(n) * 8);
    uint32_t item[64]; uint64_t item_at = ~uint64_t(0);
    for (uint32_t i = 0; i < n; ++i) {
        const uint32_t l = i & 7, w = st[l] ^ Kw[(i >> 3) & 7];
        const r18::tri t = triangle_of(w, side, reach);
        st[l] = r17::fmix32(st[l] ^ t.rgba ^ (i * 0x9E3779B1u));
        fold_box(c, side, t, i, w, item, item_at, &accs[size_t(i) * 8]);
    }
    bytes in; in.tag(r18::kTagMix).raw(K, 32).raw(accs.data(), accs.size()); b3_hash(in, M, 32);
}
}  // namespace r19

// VERSION 20, FORGE (namespace forge). The constants below are consensus constants carried by the vectors: the quota table (the 32
// slot classes a word in the fixed order), the static register wiring (d_k = k, a_k = 7k + 1 mod 32, b_k = 11k + 5 mod 32), the
// exponent mask, the fold (the R0E fchain). THE PROGRAMME: prog_seed = BLAKE3(LP("IgneumHash/Forge/program") || E || S); the 32
// words of 16 bytes by the fold chains over the seed's eight words (word i's four u32: x[(4i + j) & 7] = fchain(x[(4i + j) & 7], 4i +
// j)); the programme's hash BLAKE3 of the 512 bytes into the digest. THE SLOT, in slot order k = 0..31 with every lane's reads before
// the slot's writes (the SIMT rule): the class C_k = QUOTA[k] (fixed) or the word's drawn permutation of QUOTA (drawn_order 1: a
// Fisher-Yates over the 32 classes by fchain(w0, j)); a = R[l][a_k], b = R[l][b_k] or, when bit k of w3 is set, the lane's scratchpad
// word at ((R[l][b_k] + imm) & 1023) >> 2 (a load); imm_k = fchain(w1, k); the sub-class bits q_k = fchain(w2, k); the FP32 classes
// read ma = mask(a), mb = mask(b) and write mask(result): FMA d = fmaf(ma, mb, mask(R[d_k])), FADD/FSUB d = ma + mb or ma - mb (q_k bit
// 0 the sign), FMUL d = ma * mb; INT: ADD a + b + imm, SUB a - b - imm, XOR a ^ b ^ imm, AND (a & b) ^ imm, OR (a | b) ^ imm (q_k mod 5);
// ROT: ROTL/ROTR/SHL/SHR (q_k mod 4) by s = (imm ^ b) & 31 (reduced modulo 32 on every path), SHL (a << s) + b, SHR (a >> s) + b; MUL a *
// b + imm (low 32); MULHI mulhi_u32(a, b) ^ imm (unsigned); I2F mask(float(int32(a & 0x00FFFFFF))); F2I mask(a) ^ imm (the bit pattern);
// XLANE d = R[(l + 1 + q_k mod 15) mod 16][a_k] + imm (a static register of a drawn other lane, read at the slot's start); STORE the
// lane's scratchpad word ((a + imm) & 1023) >> 2 = b, d unchanged (the lane's own 1 KiB, no lane reads or writes another's; the loads
// and stores sequential in slot order within the lane); CMOV d = bit (imm & 31) of b ? a : d. THE MASK over the IEEE pattern u: e =
// (u >> 23) & 255; e' = 127 when e = 0 or 255 (zero, denormal, Inf, NaN map to a normal in [1, 2) with u's sign and mantissa), else
// clamp(e, 111, 143); mask(u) = (u & 0x807FFFFF) | (e' << 23); applied to every floating operand and producer and to the init's
// registers 20..31. THE KEYED READ at step t with t mod fold_every = 0: lane L = (t / fold_every) mod 16, the item index R[L][0] mod
// the items, its 64 LE32 words folded R[(L + j / 16) mod 16][j mod 16] = fchain(R, word_j). THE INIT: the 512 registers then the 16
// KiB scratchpad from BLAKE3(LP("IgneumHash/Forge/init") || E || S) expanded by the eight fold chains, the mask on registers 20..31 of
// every lane. THE DIGEST: M = BLAKE3(LP("IgneumHash/Forge/mix") || E || BLAKE3(prog) || R[0..15][0..31] LE32 || the scratchpad lanes 0..15).
namespace forge {
constexpr const char* kTagProg = "IgneumHash/Forge/program", *kTagInit = "IgneumHash/Forge/init", *kTagMix = "IgneumHash/Forge/mix";
enum : uint8_t { FMA = 0, FADDSUB, FMUL, IALU, ROT, MUL, MULHI, I2F, F2I, XLANE, STORE, CMOV };
// the quota table: FMA 3, FADD/FSUB 3, FMUL 2, IALU 10, ROT 5, MUL 2, MULHI 1, I2F 1, F2I 1, XLANE 2, STORE 1, CMOV 1 (32), interleaved
constexpr uint8_t QUOTA[32] = { IALU, FMA, ROT, IALU, XLANE, FADDSUB, IALU, MUL, IALU, FMUL, ROT, IALU, STORE, FMA, IALU, ROT, MULHI, FADDSUB, IALU, I2F, ROT, IALU, XLANE, FMUL, IALU, FMA, ROT, CMOV, FADDSUB, IALU, F2I, MUL };
inline uint32_t fmask(uint32_t u) { const uint32_t e = (u >> 23) & 255u; const uint32_t e2 = (e == 0u || e == 255u) ? 127u : (e < 111u ? 111u : e > 143u ? 143u : e); return (u & 0x807FFFFFu) | (e2 << 23); }
inline float f32(uint32_t u) { float f; std::memcpy(&f, &u, 4); return f; }
inline uint32_t u32(float f) { uint32_t u; std::memcpy(&u, &f, 4); return u; }
struct word_t { uint32_t w[4]; };
void attempt(const ih_ctx& c, const uint8_t S[32], uint8_t M[32]) {
    const uint32_t steps = c.p.rounds, words = c.p.program_ops, per_word = steps / words, fe = c.p.fold_every;
    const bool drawn = (c.p.program_distance & 1) != 0;
    // the programme
    uint8_t seed[32]; { bytes in; in.tag(kTagProg).raw(c.E, 32).raw(S, 32); b3_hash(in, seed, 32); }
    std::vector<word_t> prog(words); std::vector<uint8_t> pbytes(size_t(words) * 16);
    { uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(seed + 4 * k);
      for (uint32_t i = 0; i < words; ++i) for (int j = 0; j < 4; ++j) { const uint32_t idx = 4 * i + j; x[idx & 7] = r16f::fchain(x[idx & 7], idx); prog[i].w[j] = x[idx & 7]; for (int b = 0; b < 4; ++b) pbytes[size_t(idx) * 4 + b] = uint8_t(x[idx & 7] >> (8 * b)); } }
    uint8_t phash[32]; b3_hash(pbytes.data(), pbytes.size(), phash, 32);
    // the state
    uint32_t R[16][32]; std::vector<uint32_t> SP(size_t(16) * 256);
    { uint8_t d[32]; bytes in; in.tag(kTagInit).raw(c.E, 32).raw(S, 32); b3_hash(in, d, 32);
      uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(d + 4 * k);
      uint32_t n = 0;
      for (int l = 0; l < 16; ++l) for (int r = 0; r < 32; ++r, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); R[l][r] = r >= 20 ? fmask(x[n & 7]) : x[n & 7]; }
      for (uint32_t i = 0; i < 16 * 256; ++i, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); SP[i] = x[n & 7]; } }
    const uint64_t items = c.n_items();
    uint32_t item[64];
    uint8_t cls[32];
    // IGNPOW_TRACE: the replay counters the measurement chair reads (FORM-FORGE.md section 2), printed to stderr at the hash's end:
    // the operations by class, the scratchpad loads and stores (and bytes), the keyed items and their 64-byte lines, the register
    // reads and writes a hash (times 16 lanes), the cross-lane moves; env-gated, the bytes unchanged
    static const bool trace = std::getenv("IGNPOW_TRACE") != nullptr;
    // IGNPOW_FORGE_DUMP: a plain-text step trace of the FIRST attempt of the process (the ports' comparison points, the GPU team 15 Oct 00:0x)
    static bool dump_armed = std::getenv("IGNPOW_FORGE_DUMP") != nullptr; const bool dump = dump_armed; dump_armed = false;
    auto dump_lane = [&](const char* what, int l) { std::fprintf(stderr, "%s lane %d:", what, l); for (int r = 0; r < 32; ++r) std::fprintf(stderr, " %08x", R[l][r]); std::fprintf(stderr, "\n"); };
    if (dump) {
        std::fprintf(stderr, "forge dump: E"); for (int k = 0; k < 8; ++k) std::fprintf(stderr, " %08x", le32(c.E + 4 * k)); std::fprintf(stderr, "\nS"); for (int k = 0; k < 8; ++k) std::fprintf(stderr, " %08x", le32(S + 4 * k));
        std::fprintf(stderr, "\nprog_seed"); for (int k = 0; k < 8; ++k) std::fprintf(stderr, " %08x", le32(seed + 4 * k));
        std::fprintf(stderr, "\nprogramme word 0: %08x %08x %08x %08x\n", prog[0].w[0], prog[0].w[1], prog[0].w[2], prog[0].w[3]);
        std::fprintf(stderr, "programme hash:"); for (int k = 0; k < 32; ++k) std::fprintf(stderr, "%02x", phash[k]); std::fprintf(stderr, "\n");
        dump_lane("init", 0); dump_lane("init", 1); std::fprintf(stderr, "init scratchpad lane 0 words 0..7:"); for (int k = 0; k < 8; ++k) std::fprintf(stderr, " %08x", SP[k]); std::fprintf(stderr, "\n");
    }
    uint64_t n_cls[12] = {0}, n_load = 0, n_store = 0, n_item = 0, n_rreads = 0, n_rwrites = 0, n_xlane = 0;
    for (uint32_t t = 0; t < steps; ++t) {
        if ((t % fe) == 0) {
            const uint32_t L = (t / fe) & 15;
            if (dump) std::fprintf(stderr, "step %u keyed read: lane %u R0 %08x item index %llu\n", t, L, R[L][0], (unsigned long long)(uint64_t(R[L][0]) % items));
            load_item(c, uint64_t(R[L][0]) % items, item);
            if (dump && t == 0) { std::fprintf(stderr, "item words 0..3: %08x %08x %08x %08x\n", item[0], item[1], item[2], item[3]); }
            for (int j = 0; j < 64; ++j) { uint32_t& r = R[(L + j / 16) & 15][j & 15]; r = r16f::fchain(r, item[j]); }
            if (dump && t == 0) dump_lane("after keyed read 0", 0);
            if (trace) { ++n_item; n_rreads += 65; n_rwrites += 64; }
        }
        const word_t& W = prog[t / per_word];
        if ((t % per_word) == 0) {
            for (int k = 0; k < 32; ++k) cls[k] = QUOTA[k];
            if (drawn) for (int i = 31; i > 0; --i) { const uint32_t j = r16f::fchain(W.w[0], uint32_t(i)) % uint32_t(i + 1); std::swap(cls[i], cls[j]); }
        }
        for (int k = 0; k < 32; ++k) {
            const uint32_t ak = (7u * k + 1u) & 31u, bk = (11u * k + 5u) & 31u, dk = uint32_t(k);
            const uint32_t imm = r16f::fchain(W.w[1], uint32_t(k)), q = r16f::fchain(W.w[2], uint32_t(k));
            const bool load = (W.w[3] >> k) & 1u;
            const uint8_t C = cls[k];
            uint32_t nd[16]; bool wr[16]; uint32_t st_addr[16], st_val[16]; bool st[16];
            for (int l = 0; l < 16; ++l) {
                const uint32_t a = R[l][ak];
                const uint32_t b = load ? SP[size_t(l) * 256 + (((R[l][bk] + imm) & 1023u) >> 2)] : R[l][bk];
                const uint32_t d0 = R[l][dk];
                uint32_t d = d0; bool w = true; st[l] = false;
                switch (C) {
                    case FMA: d = fmask(u32(fmaf(f32(fmask(a)), f32(fmask(b)), f32(fmask(d0))))); break;
                    case FADDSUB: d = fmask(u32((q & 1u) ? f32(fmask(a)) - f32(fmask(b)) : f32(fmask(a)) + f32(fmask(b)))); break;
                    case FMUL: d = fmask(u32(f32(fmask(a)) * f32(fmask(b)))); break;
                    case IALU: switch (q % 5u) { case 0: d = a + b + imm; break; case 1: d = a - b - imm; break; case 2: d = a ^ b ^ imm; break; case 3: d = (a & b) ^ imm; break; default: d = (a | b) ^ imm; break; } break;
                    case ROT: { const uint32_t sh = (imm ^ b) & 31u; switch (q & 3u) { case 0: d = rotl32(a, sh); break; case 1: d = rotl32(a, (32u - sh) & 31u); break; case 2: d = (a << sh) + b; break; default: d = (a >> sh) + b; break; } } break;
                    case MUL: d = a * b + imm; break;
                    case MULHI: d = uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm; break;
                    case I2F: d = fmask(u32(float(int32_t(a & 0x00FFFFFFu)))); break;
                    case F2I: d = fmask(a) ^ imm; break;
                    case XLANE: d = R[(uint32_t(l) + 1u + (q % 15u)) & 15u][ak] + imm; break;
                    case STORE: w = false; st[l] = true; st_addr[l] = ((a + imm) & 1023u) >> 2; st_val[l] = b; break;
                    default: d = ((b >> (imm & 31u)) & 1u) ? a : d0; break;  // CMOV
                }
                nd[l] = d; wr[l] = w;
            }
            for (int l = 0; l < 16; ++l) { if (wr[l]) R[l][dk] = nd[l]; if (st[l]) SP[size_t(l) * 256 + st_addr[l]] = st_val[l]; }
            if (dump && k == 31 && (t <= 3 || t == 8 || t == 64 || t == 511)) { char nm[32]; std::snprintf(nm, sizeof nm, "after step %u", t); dump_lane(nm, 0); }
            if (dump && t == 0 && k < 4) { std::fprintf(stderr, "step 0 slot %d class %u a=r%u b=r%u d=r%u imm %08x q %08x load %d lane0 a %08x b %08x -> d %08x\n", k, C, ak, bk, dk, imm, q, load ? 1 : 0, R[0][ak], load ? SP[(((R[0][bk] + imm) & 1023u) >> 2)] : R[0][bk], nd[0]); }
            if (trace) { n_cls[C] += 16; if (load) n_load += 16; if (C == STORE) n_store += 16; if (C == XLANE) n_xlane += 16; n_rreads += 16 * (2 + (C == FMA || C == CMOV ? 1 : 0) + (C == XLANE ? 1 : 0)); n_rwrites += 16 * (C == STORE ? 0 : 1); }
        }
    }
    if (dump) { for (int l = 0; l < 2; ++l) dump_lane("final", l); std::fprintf(stderr, "digest inputs: tag %s, E, programme hash, R[0..15][0..31] LE32, scratchpad 4096 words\n", kTagMix); }
    if (trace) {
        static const char* names[12] = { "FMA", "FADDSUB", "FMUL", "IALU", "ROT", "MUL", "MULHI", "I2F", "F2I", "XLANE", "STORE", "CMOV" };
        std::fprintf(stderr, "forge trace: steps %u words %u lanes 16 slots 32;", steps, words);
        for (int k = 0; k < 12; ++k) std::fprintf(stderr, " %s %llu", names[k], (unsigned long long)n_cls[k]);
        std::fprintf(stderr, "; scratchpad loads %llu (%llu bytes) stores %llu (%llu bytes) partition 1024 bytes a lane; keyed items %llu (%llu bytes, %llu 64-byte lines); register reads %llu writes %llu; cross-lane moves %llu\n",
            (unsigned long long)n_load, (unsigned long long)(4 * n_load), (unsigned long long)n_store, (unsigned long long)(4 * n_store), (unsigned long long)n_item, (unsigned long long)(256 * n_item), (unsigned long long)(4 * n_item), (unsigned long long)n_rreads, (unsigned long long)n_rwrites, (unsigned long long)n_xlane);
    }
    bytes out; out.tag(kTagMix).raw(c.E, 32).raw(phash, 32);
    for (int l = 0; l < 16; ++l) for (int r = 0; r < 32; ++r) out.le32(R[l][r]);
    for (uint32_t i = 0; i < 16 * 256; ++i) out.le32(SP[i]);
    b3_hash(out, M, 32);
}
}  // namespace forge

// VERSION 21, FORGE2 (namespace forge2): FORM-FORGE.md section 7. THE PERIOD: period_seed = BLAKE3(LP("IgneumHash/Forge/period") || E ||
// header || LE64 (nonce >> batch_log2)); the M shapes by the fold chains over the period seed's eight words as version 20's programme
// words (16 bytes a shape), each shape's class order DRAWN (the Fisher-Yates over the quota by fchain(w0, i)), its operands static.
// THE HASH: prog_seed = BLAKE3(LP("IgneumHash/Forge/program") || E || S); the sequence e_i = fchain(y[i & 7], i) mod M for i in 0..L-1
// (y the seed's eight words, chained), and the hash's word h_i = fchain(y[(i + 4) & 7], 4096 + i) for the entry's scratchpad address
// immediates (the load's and the store's) and the cmov predicate (the arithmetic immediates the shape's). Entry i runs rounds / L
// steps of shape e_i; the keyed read, the init and the mask as version 20's. THE DIGEST: M = BLAKE3(LP("IgneumHash/Forge/mix2") || E ||
// period_seed || the L sequence bytes || R[0..15][0..31] LE32 || the scratchpad lanes 0..15). THE CHAIN FORM (period_blocks set, F-141):
// period_seed = BLAKE3(LP("IgneumHash/Forge/period") || E || LE64(chain_id) || LE64(height / period_blocks) || anchor), the anchor the
// hash of the block at finality depth 64 below the period's start (zero before 64), a runtime input; the rest unchanged.
namespace forge2 {
// F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 2, the GPU team's precision): the bond-keyed ITEM-READ STEPS. The reads are indexed r = 0..R-1
// in walk order (the read at step t = fold_every x r; R = ceil(steps / fold_every)); read r is keyed iff (r x S) mod R < S, its index
// i = (r x S) div R; m_0 = the work seed, m_i (i >= 1) = eight words, word l = lane l's r0 XOR lane (l + 8)'s r0 for l = 0..7 as LE32,
// read at the step's ENTRY (the registers as the step begins, before the item read and the lane mix); s_i through ignh_bond_share.
// WHERE THE SHARE ENTERS (F-504's ruled form, the review lead 17 October 2026 12:11 UK, section 9 of the specification): the keyed read's
// item index is XORed with the share's first LE32 word before the modulo (the item fetched is the share's choice, every later read
// dependent on it through the state at every read step whatever the drawn shape), and every share's bytes go whole into the mix's
// pre-image after the lanes' words (the replay verifies all 256 bits a share). Nothing enters a register or the item's words: the
// step's slots overwrite the folded item words before most are read, so the first form (the share into the item's first eight words,
// version 8's order) bound the digest on 48 of 64 nonces only.
struct keyed_read { uint32_t S, R; const uint8_t* shares;
    keyed_read(const ih_ctx& c, const uint8_t* sh) : S((c.p.features & 1024) ? c.p.bond_rounds : 0u), R((c.p.rounds + c.p.fold_every - 1) / c.p.fold_every), shares(sh) {}
    // F-504's cure, candidate (i)+ (the design cell of 17 October 2026): the share binds the WORK through the keyed read's ITEM INDEX
    // (the index word XOR the share's first LE32 word before the modulo, so the item fetched is the share's choice and every later
    // read depends on it through the state, at every read step whatever the drawn shape), and the whole share binds the DIGEST
    // through the mix's pre-image (every share's bytes appended after the lanes' words, so the replay verifies all 256 bits a share).
    // Nothing enters a register; m_i and s_i are as section 2.2 and 2.3 have them.
    uint32_t index(uint32_t r, uint32_t idx_word, const uint32_t (&Rg)[16][32], const uint8_t Sd[32], const uint8_t* sk, uint8_t* sh_out, uint8_t* m_out) const {
        if (!S || (uint64_t(r) * S) % R >= S) return idx_word;
        const uint32_t i = uint32_t((uint64_t(r) * S) / R);
        uint8_t m[32];
        if (i == 0) std::memcpy(m, Sd, 32); else for (int l = 0; l < 8; ++l) put_le32(m + 4 * l, Rg[l][0] ^ Rg[l + 8][0]);
        if (m_out) std::memcpy(m_out + size_t(i) * 32, m, 32);
        uint8_t* s = sh_out + size_t(i) * 32;
        if (sk) ignh_bond_share(sk, i, m, s);
        return idx_word ^ le32(s);
    }
};
constexpr const char* kTagPeriod = "IgneumHash/Forge/period", *kTagMix2 = "IgneumHash/Forge/mix2", *kTagSeq = "IgneumHash/Forge/sequence";
// fp_mode 1, the integer-only quota: the ten FP slots (FMA 3, FADD/FSUB 3, FMUL 2, I2F 1, F2I 1) to IALU 5, ROT 2, MUL 1, XLANE 1, STORE 1
// (IALU 15, ROT 7, MUL 3, MULHI 1, XLANE 3, STORE 2, CMOV 1 = 32), the FP positions of forge::QUOTA taken in order
constexpr uint8_t QUOTA_INT[32] = { forge::IALU, forge::IALU, forge::ROT, forge::IALU, forge::XLANE, forge::IALU, forge::IALU, forge::MUL, forge::IALU, forge::ROT, forge::ROT, forge::IALU, forge::STORE, forge::IALU, forge::IALU, forge::ROT, forge::MULHI, forge::IALU, forge::IALU, forge::MUL, forge::ROT, forge::IALU, forge::XLANE, forge::XLANE, forge::IALU, forge::IALU, forge::ROT, forge::CMOV, forge::STORE, forge::IALU, forge::IALU, forge::MUL };
inline bool is_fp(uint8_t C) { return C == forge::FMA || C == forge::FADDSUB || C == forge::FMUL || C == forge::I2F || C == forge::F2I; }
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32], const uint8_t* sk = nullptr, uint8_t* shares = nullptr, uint8_t* m_out = nullptr) {
    const uint32_t steps = c.p.rounds, shapes = c.p.program_ops, L = c.p.blocks, per_entry = steps / L, fe = c.p.fold_every;
    const keyed_read kr(c, shares);  // F-441 / F-504: null sk/shares/m_out on an unkeyed document (bond_rounds 0: the walk unchanged)
    const uint32_t fp_mode = ((c.p.program_distance >> 1) & 7) | (((c.p.program_distance >> 16) & 1) << 3), pair_log2 = (c.p.program_distance >> 4) & 15, prefetch = (c.p.program_distance >> 8) & 127;
    const bool occ = fp_mode >= 3, selall = fp_mode == 9, chain_sel = ((c.p.program_distance >> 17) & 1) != 0;
    const bool base = c.p.version == 24;  // the control: the period's fixed order, no per-hash draw
    const bool oldcard = c.p.version == 25, mix2 = oldcard && (c.p.program_distance & (1u << 20));  // entry 2: four of five steps scratchpad steps into the per-hash scratchpad; mix2 the drawer's second mix
    const uint32_t sp8_bytes = oldcard ? c.p.scratch_bytes : 0u, sp8_slice = sp8_bytes / 16;
    const uint32_t route_q = (c.p.program_distance >> 18) & 3;  // the routed share in quarters: slots k with (k & 3) < route_q read their first operand from a drawn lane  // 9: select-all, the measurement form (the programme per hash per step)
    const uint8_t* quota = (fp_mode == 1 || occ) ? QUOTA_INT : forge::QUOTA;
    const uint32_t sp_mask = occ ? 63u : 1023u, sp_words = (fp_mode == 6 || fp_mode == 7 || selall || c.p.version == 25) ? 0u : occ ? 16u : 256u;  // the lane's scratchpad: 64 bytes (the occupancy forms) or 1 KiB; none under (e) and (f)
    const bool window = fp_mode == 6, kawplus = fp_mode == 7;
    const uint32_t nreg = fp_mode == 5 ? 16u : 32u;
    // the period seed: the chain form (period_blocks set) keys on the period of blocks and its anchor (F-141, the review lead's conditions);
    // the batch form on the header and the nonce batch
    uint8_t pseed[32];
    if (c.p.period_blocks) { if (!anchor) anchor = c.p.program_anchor; bytes in; in.tag(kTagPeriod).raw(c.E, 32).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor, 32); b3_hash(in, pseed, 32); }
    else { bytes in; in.tag(kTagPeriod).raw(c.E, 32).raw(header, 32).le64(nonce >> c.p.keyed_rounds); b3_hash(in, pseed, 32); }
    std::vector<forge::word_t> shp(shapes); std::vector<std::array<uint8_t, 32>> scls(shapes);
    { uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(pseed + 4 * k);
      for (uint32_t i = 0; i < shapes; ++i) { for (int j = 0; j < 4; ++j) { const uint32_t idx = 4 * i + j; x[idx & 7] = r16f::fchain(x[idx & 7], idx); shp[i].w[j] = x[idx & 7]; }
        for (int k = 0; k < 32; ++k) scls[i][k] = quota[k];
        for (int q = 31; q > 0; --q) { const uint32_t j = r16f::fchain(shp[i].w[0], uint32_t(q)) % uint32_t(q + 1); std::swap(scls[i][q], scls[i][j]); } } }
    uint8_t seed[32]; { bytes in; in.tag(forge::kTagProg).raw(c.E, 32).raw(S, 32); b3_hash(in, seed, 32); }
    std::vector<uint8_t> seq(L); std::vector<uint32_t> hw(L);
    { uint32_t y[8]; for (int k = 0; k < 8; ++k) y[k] = le32(seed + 4 * k);
      for (uint32_t i = 0; i < L; ++i) { y[i & 7] = r16f::fchain(y[i & 7], i); seq[i] = uint8_t(y[i & 7] % shapes); y[(i + 4) & 7] = r16f::fchain(y[(i + 4) & 7], 4096u + i); hw[i] = y[(i + 4) & 7]; } }
    if (base) {  // version 24: the period's fixed order of the shapes (a permutation by the period seed's word 7), entry i runs order[i]
        uint8_t order[256]; for (uint32_t i = 0; i < shapes; ++i) order[i] = uint8_t(i);
        for (uint32_t i = shapes - 1; i > 0; --i) { const uint32_t r = r16f::fchain(le32(pseed + 28), 65536u + i) % (i + 1); std::swap(order[i], order[r]); }
        for (uint32_t i = 0; i < L; ++i) { seq[i] = order[i % shapes]; hw[i] = r16f::fchain(shp[seq[i]].w[2], 4096u + i); }  // the predicates the shape's (the period's), not the hash's
    }
    if (kawplus) {  // (f): the sequence fixed (word i at step i), the predicates uniform across the warp (the warp's seed)
        for (uint32_t i = 0; i < L; ++i) seq[i] = uint8_t(i % shapes);
        uint8_t ws[32]; { bytes in; in.tag("IgneumHash/Forge/warp").raw(c.E, 32).raw(header, 32).le64(nonce >> 5); b3_hash(in, ws, 32); }
        uint32_t y[8]; for (int k = 0; k < 8; ++k) y[k] = le32(ws + 4 * k);
        for (uint32_t i = 0; i < L; ++i) { y[i & 7] = r16f::fchain(y[i & 7], i); hw[i] = y[i & 7]; }
    }
    if (pair_log2 && !base) {  // (c3): the sequence from the pair's seed (the addresses and predicates hw stay the hash's)
        uint8_t qs[32]; { bytes in; in.tag(kTagSeq).raw(c.E, 32).raw(header, 32).le64(nonce >> pair_log2); b3_hash(in, qs, 32); }
        uint32_t y[8]; for (int k = 0; k < 8; ++k) y[k] = le32(qs + 4 * k);
        for (uint32_t i = 0; i < L; ++i) { y[i & 7] = r16f::fchain(y[i & 7], i); seq[i] = uint8_t(y[i & 7] % shapes); }
    }
    uint32_t R[16][32]; std::vector<uint32_t> SP(size_t(16) * sp_words); uint32_t WN[16 * 16] = {0}; std::vector<uint8_t> SP8;  // version 25: the per-hash scratchpad  // (e): the rotating window, 16 words a lane
    { uint8_t d[32]; bytes in; in.tag(forge::kTagInit).raw(c.E, 32).raw(S, 32); b3_hash(in, d, 32);
      uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(d + 4 * k);
      uint32_t n = 0;
      // the init's mask: mode 0 on r20..r31 (version 20's), mode 2 on the FP bank r24..r31, mode 1 none
      for (int l = 0; l < 16; ++l) for (uint32_t r = 0; r < nreg; ++r, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); const bool mk = fp_mode == 0 ? r >= 20 : fp_mode == 2 ? r >= 24 : false; R[l][r] = mk ? forge::fmask(x[n & 7]) : x[n & 7]; }
      for (uint32_t i = 0; i < 16 * sp_words; ++i, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); SP[i] = x[n & 7]; }
      if (window) for (uint32_t i = 0; i < 16 * 16; ++i, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); WN[i] = x[n & 7]; }
      if (oldcard) { SP8.resize(sp8_bytes); for (uint32_t i = 0; i < sp8_bytes; i += 4, ++n) { x[n & 7] = r16f::fchain(x[n & 7], n); for (int b = 0; b < 4; ++b) SP8[i + b] = uint8_t(x[n & 7] >> (8 * b)); } } }
    const uint64_t items = c.n_items();
    uint32_t item[64];
    static const bool trace = std::getenv("IGNPOW_TRACE") != nullptr;  // the replay counters (FORM-FORGE.md section 2), as version 20's
    uint64_t n_cls[12] = {0}, n_load = 0, n_store = 0, n_item = 0, n_rreads = 0, n_rwrites = 0, n_xlane = 0, n_computed = 0;
    uint32_t z[8]; for (int k = 0; k < 8; ++k) z[k] = le32(seed + 4 * k);  // select-all: the hash's step chain (the programme per hash per step)
    uint32_t xsel = r16f::fchain(le32(seed + 28), 65536u);  // chain_select: the first entry's read word from the seed; then the previous entry's final state
    uint32_t xroute = r16f::fchain(le32(seed + 24), 131072u);  // route: step 0's route word from the seed; then the previous step's result
    uint64_t n_route = 0;
    // the prefetch history: the 16 lanes' r0 at the start of each of the last K + 1 steps (step t's entry written before its slots)
    std::vector<uint32_t> hist(size_t(prefetch + 1) * 16);
    for (uint32_t t = 0; t < steps; ++t) {
        for (int l = 0; l < 16; ++l) hist[size_t(t % (prefetch + 1)) * 16 + l] = R[l][0];
        if ((t % fe) == 0) {
            const uint32_t Ln = (t / fe) & 15;
            const uint32_t tp = t >= prefetch ? t - prefetch : 0u;  // the index from the state K steps before (the init state before step K)
            const uint32_t idx_word = kr.index(t / fe, chain_sel ? r16f::fchain(xsel, 8192u + t) : hist[size_t(tp % (prefetch + 1)) * 16 + Ln], R, S, sk, shares, m_out);  // F-504 (i)+: the keyed read's index is the share's choice
            load_item(c, uint64_t(idx_word) % items, item);
            for (int j = 0; j < 64; ++j) { uint32_t& r = R[(Ln + j / 16) & 15][j & 15]; r = r16f::fchain(r, item[j]); }
            if (trace) { ++n_item; n_rreads += 65; n_rwrites += 64; }
        }
        if (oldcard && (t % 5) != (mix2 ? 0u : 4u)) {
            // the scratch step: every lane reads, then every lane writes its own slice (the warp's load-then-store order); mix2 the
            // drawer's second step mix (the shape step first in the five, the addresses and the mixing from other constants)
            const uint32_t i0 = (4u * t) & 31u, i1 = (4u * t + 1u) & 31u, i2 = (4u * t + 2u) & 31u, i3 = (4u * t + 3u) & 31u;
            uint32_t lo[16], hi[16];
            for (int l = 0; l < 16; ++l) { const uint32_t ra = (mix2 ? ((R[l][i0] * 0x85EBCA6Bu) ^ R[l][i1]) : (R[l][i0] ^ (R[l][i1] * 0x9E3779B9u))) & (sp8_bytes - 8u) & ~7u; lo[l] = le32(&SP8[ra]); hi[l] = le32(&SP8[ra + 4]); }
            for (int l = 0; l < 16; ++l) {
                if (mix2) { R[l][i2] = r17::fmix32(R[l][i2] + lo[l]); R[l][i3] = r16f::fchain(R[l][i3] ^ hi[l], t); }
                else { R[l][i2] = r16f::fchain(R[l][i2] ^ lo[l], t); R[l][i3] = (R[l][i3] + hi[l]) ^ rotl32(lo[l], 13); }
                const uint32_t wa = uint32_t(l) * sp8_slice + ((mix2 ? (R[l][i3] + rotl32(R[l][i2], 7)) : (R[l][i2] ^ (R[l][i3] * 0x85EBCA6Bu))) & (sp8_slice - 8u) & ~7u);
                for (int b = 0; b < 4; ++b) { SP8[wa + b] = uint8_t(R[l][i2] >> (8 * b)); SP8[wa + 4 + b] = uint8_t(R[l][i3] >> (8 * b)); }
            }
            if (trace) { n_load += 16; n_store += 16; n_rreads += 64; n_rwrites += 32; }
            continue;
        }
        const uint32_t ei = t / per_entry; const forge::word_t& W = shp[seq[ei]]; const uint8_t* cls = scls[seq[ei]].data(); const uint32_t hword = hw[ei];
        uint32_t sw[3] = {0, 0, 0};  // select-all: the step's three words from the hash's chain (the class draw, the immediates, the variants)
        if (route_q && t) { uint32_t x = xroute; for (int l = 0; l < 16; ++l) x = (x ^ R[l][0]) + R[l][1]; xroute = r16f::fchain(x, t); }  // the route word from the previous step's result
        if (selall) for (int j = 0; j < 3; ++j) { const uint32_t idx = 3u * t + uint32_t(j); z[idx & 7] = r16f::fchain(z[idx & 7], 8192u + idx); sw[j] = z[idx & 7]; }
        for (int k = 0; k < 32; ++k) {
            uint32_t ak, bk, dk;
            if (fp_mode == 5) { dk = uint32_t(k) & 15u; ak = (uint32_t(k) + 11u) & 15u; bk = (uint32_t(k) + 7u) & 15u; }
            else if (fp_mode == 4) { dk = uint32_t(k); ak = (uint32_t(k) + 24u) & 31u; bk = (uint32_t(k) + 16u) & 31u; }
            else if (fp_mode == 3 || fp_mode == 6) { dk = uint32_t(k); ak = (uint32_t(k) + 21u) & 31u; bk = (uint32_t(k) + 9u) & 31u; }  // (c) and (e) the ILP wiring (the GPU team's fault 02:4x UK: 6 fell through to version 20's)
            else { dk = uint32_t(k); ak = (7u * k + 1u) & 31u; bk = (11u * k + 5u) & 31u; }  // the version 20 wiring: modes 0, 1, 2 and (f) 7
            const uint32_t imm = r16f::fchain(selall ? sw[1] : W.w[1], uint32_t(k)), q = r16f::fchain(selall ? sw[2] : W.w[2], uint32_t(k)), himm = r16f::fchain(hword, uint32_t(k));
            bool load = (W.w[3] >> k) & 1u;
            const uint8_t C = cls[k];
            const uint32_t sel = selall ? (r16f::fchain(sw[0], uint32_t(k)) & 7u) : 0u;  // select-all: the slot's drawn operation, 0..7
            const bool routed = route_q && (uint32_t(k) & 3u) < route_q;
            const uint32_t rsh0 = routed ? r16f::fchain(xroute, uint32_t(k)) : 0u, rsh1 = routed ? r16f::fchain(xroute, 32u + uint32_t(k)) : 0u;
            if (kawplus || selall) load = false;  // (f) and select-all: register-resident
            const uint32_t wbase = window ? (t % per_entry) & 15u : 0u;  // (e): the window's base, the step within the entry
            const uint32_t woff = window ? (imm & 15u) : 0u, wsoff = window ? ((imm >> 4) & 15u) : 0u;  // (e): the static window offsets of the shape
            if (fp_mode == 2) {
                // the producer-masked wiring: the FP slots on the bank r24..r31 (no scratchpad load), the integer slots on r0..r23;
                // I2F reads an integer register and writes the bank, F2I reads the bank and writes an integer register
                if (is_fp(C)) { load = false; if (C == forge::I2F) { ak = ak % 24u; bk = bk % 24u; dk = 24u + (uint32_t(k) & 7u); } else if (C == forge::F2I) { ak = 24u + (ak & 7u); bk = 24u + (bk & 7u); dk = uint32_t(k) % 24u; } else { ak = 24u + (ak & 7u); bk = 24u + (bk & 7u); dk = 24u + (uint32_t(k) & 7u); } }
                else { ak = ak % 24u; bk = bk % 24u; dk = uint32_t(k) % 24u; }
            }
            uint32_t nd[16]; bool wr[16]; uint32_t st_addr[16], st_val[16]; bool st[16];
            for (int l = 0; l < 16; ++l) {
                const uint32_t a = routed ? R[(uint32_t(l) + 1u + (((l < 8 ? rsh0 : rsh1) >> (4 * (l & 7))) & 15u)) & 15u][ak] : R[l][ak];  // route: the first operand from the drawn lane
                const uint32_t b = load ? (window ? WN[16 * l + ((wbase + woff) & 15u)] : SP[size_t(l) * sp_words + (((R[l][bk] + himm) & sp_mask) >> 2)]) : R[l][bk];
                const uint32_t d0 = R[l][dk];
                uint32_t d = d0; bool w = true; st[l] = false;
                const uint32_t ma = fp_mode == 2 ? a : forge::fmask(a), mb = fp_mode == 2 ? b : forge::fmask(b), md = fp_mode == 2 ? d0 : forge::fmask(d0);  // mode 2: the bank is masked at its producers
                if (selall) {
                    // the eight candidates computed by every lane every slot, the drawn one selected by a mask (no branch): 0 add, 1 sub,
                    // 2 xor, 3 and/or (the variant's bit), 4 rotate left by (imm ^ b) mod 32, 5 mul, 6 mulhi, 7 cross-lane (lane + 1 + q mod 15)
                    const uint32_t cand[8] = { a + b + imm, a - b - imm, a ^ b ^ imm, ((q & 1u) ? (a & b) : (a | b)) ^ imm, rotl32(a, (imm ^ b) & 31u), a * b + imm, uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm, R[(uint32_t(l) + 1u + (q % 15u)) & 15u][ak] + imm };
                    d = 0; for (uint32_t cc = 0; cc < 8; ++cc) d |= (0u - uint32_t(cc == sel)) & cand[cc];
                    nd[l] = d; wr[l] = true; continue;
                }
                switch (C) {
                    case forge::FMA: d = forge::fmask(forge::u32(fmaf(forge::f32(ma), forge::f32(mb), forge::f32(md)))); break;
                    case forge::FADDSUB: d = forge::fmask(forge::u32((q & 1u) ? forge::f32(ma) - forge::f32(mb) : forge::f32(ma) + forge::f32(mb))); break;
                    case forge::FMUL: d = forge::fmask(forge::u32(forge::f32(ma) * forge::f32(mb))); break;
                    case forge::IALU: switch (q % 5u) { case 0: d = a + b + imm; break; case 1: d = a - b - imm; break; case 2: d = a ^ b ^ imm; break; case 3: d = (a & b) ^ imm; break; default: d = (a | b) ^ imm; break; } break;
                    case forge::ROT: { const uint32_t sh = (imm ^ b) & 31u; switch (q & 3u) { case 0: d = rotl32(a, sh); break; case 1: d = rotl32(a, (32u - sh) & 31u); break; case 2: d = (a << sh) + b; break; default: d = (a >> sh) + b; break; } } break;
                    case forge::MUL: d = a * b + imm; break;
                    case forge::MULHI: d = uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm; break;
                    case forge::I2F: d = forge::fmask(forge::u32(float(int32_t(a & 0x00FFFFFFu)))); break;
                    case forge::F2I: d = ma ^ imm; break;
                    case forge::XLANE: d = R[(uint32_t(l) + 1u + (q % 15u)) & 15u][ak] + imm; break;
                    case forge::STORE: if (kawplus) { d = a ^ b ^ imm; break; } w = false; st[l] = true; st_addr[l] = window ? ((wbase + wsoff) & 15u) : ((a + himm) & sp_mask) >> 2; st_val[l] = b; break;  // (f): the store slot an XOR (register-resident)
                    default: d = ((b >> (himm & 31u)) & 1u) ? a : d0; break;  // CMOV: the hash's predicate bit
                }
                nd[l] = d; wr[l] = w;
            }
            for (int l = 0; l < 16; ++l) { if (wr[l]) R[l][dk] = nd[l]; if (st[l]) { if (window) WN[16 * l + st_addr[l]] = st_val[l]; else SP[size_t(l) * sp_words + st_addr[l]] = st_val[l]; } }
            if (trace && routed) n_route += 16;
            if (trace && selall) { static const uint8_t m9[8] = { forge::IALU, forge::IALU, forge::IALU, forge::IALU, forge::ROT, forge::MUL, forge::MULHI, forge::XLANE }; n_cls[m9[sel]] += 16; n_computed += 16 * 8; if (sel == 7) n_xlane += 16; n_rreads += 16 * 3; n_rwrites += 16; continue; }
            if (trace) { n_cls[C] += 16; if (load) n_load += 16; if (C == forge::STORE) n_store += 16; if (C == forge::XLANE) n_xlane += 16; n_rreads += 16 * (2 + (C == forge::FMA || C == forge::CMOV ? 1 : 0) + (C == forge::XLANE ? 1 : 0)); n_rwrites += 16 * (C == forge::STORE ? 0 : 1); }
        }
        if (chain_sel && (t % per_entry) == per_entry - 1) {  // the entry's end: the next entry's shape and reads from this entry's final lane state
            uint32_t x = xsel; for (int l = 0; l < 16; ++l) x = (x ^ R[l][0]) + R[l][1];
            xsel = r16f::fchain(x, 1u + ei);
            if (ei + 1 < L) seq[ei + 1] = uint8_t(xsel % shapes);
        }
    }
    if (trace) {
        static const char* names[12] = { "FMA", "FADDSUB", "FMUL", "IALU", "ROT", "MUL", "MULHI", "I2F", "F2I", "XLANE", "STORE", "CMOV" };
        if (route_q) std::fprintf(stderr, "forge2 route: share %u percent, routed cross-lane operand reads %llu a hash (beside the XLANE class's)\n", route_q * 25u, (unsigned long long)n_route);
        if (selall) std::fprintf(stderr, "forge2 select-all: useful operations %llu, computed candidates %llu (eight a slot), selects %llu\n", (unsigned long long)(n_computed / 8), (unsigned long long)n_computed, (unsigned long long)(n_computed / 8));
        if (oldcard) std::fprintf(stderr, "oldcard: scratchpad %u bytes a hash (16 slices of %u), scratch steps %u of %u (8-byte reads %llu, writes %llu)\n", sp8_bytes, sp8_slice, steps - steps / 5, steps, (unsigned long long)n_load, (unsigned long long)n_store);
        std::fprintf(stderr, "forge2 trace: fp_mode %u pair_log2 %u steps %u shapes %u sequence %u lanes 16 slots 32 registers %u;", fp_mode, pair_log2, steps, shapes, L, nreg);
        for (int k = 0; k < 12; ++k) std::fprintf(stderr, " %s %llu", names[k], (unsigned long long)n_cls[k]);
        std::fprintf(stderr, "; scratchpad loads %llu (%llu bytes) stores %llu (%llu bytes) partition %u bytes a lane; keyed items %llu (%llu bytes, %llu 64-byte lines); register reads %llu writes %llu; cross-lane moves %llu\n",
            (unsigned long long)n_load, (unsigned long long)(4 * n_load), (unsigned long long)n_store, (unsigned long long)(4 * n_store), 4u * sp_words, (unsigned long long)n_item, (unsigned long long)(256 * n_item), (unsigned long long)(4 * n_item), (unsigned long long)n_rreads, (unsigned long long)n_rwrites, (unsigned long long)n_xlane);
    }
    bytes out; out.tag(kTagMix2).raw(c.E, 32).raw(pseed, 32).raw(seq.data(), L);
    if (c.p.program_distance & 32768u) {
        // the lean mix: each lane's words (its registers, then its scratchpad words, then its window words) into a 16-byte accumulator
        for (int l = 0; l < 16; ++l) {
            uint32_t acc[4] = { 0x9E3779B9u ^ uint32_t(l), 0x85EBCA6Bu, 0xC2B2AE35u, 0x27D4EB2Fu }; uint32_t n = 0;
            auto fold = [&](uint32_t w) { acc[n & 3] = (acc[n & 3] ^ w) + w; ++n; };
            for (uint32_t r = 0; r < nreg; ++r) fold(R[l][r]);
            for (uint32_t i = 0; i < sp_words; ++i) fold(SP[size_t(l) * sp_words + i]);
            if (window) for (uint32_t i = 0; i < 16; ++i) fold(WN[16 * l + i]);
            if (oldcard) for (uint32_t i = 0; i < sp8_slice; i += 4) fold(le32(&SP8[size_t(l) * sp8_slice + i]));  // the lane's slice of the scratchpad, so the digest binds every byte of it
            for (int k = 0; k < 4; ++k) out.le32(acc[k]);
        }
    } else {
        for (int l = 0; l < 16; ++l) for (uint32_t r = 0; r < nreg; ++r) out.le32(R[l][r]);
        for (uint32_t i = 0; i < 16 * sp_words; ++i) out.le32(SP[i]);
        if (window) for (uint32_t i = 0; i < 16 * 16; ++i) out.le32(WN[i]);
    }
    if (kr.S && kr.shares) out.raw(kr.shares, size_t(kr.S) * 32);  // F-504 (i)+: every share whole into the digest's pre-image
    b3_hash(out, M, 32);
}
}  // namespace forge2

// VERSION 24, BASE (namespace base): the control of record, version 21's engine with the per-hash draws removed (the parser's note).
namespace base {
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32]) { forge2::attempt(c, height, anchor, header, nonce, S, M); }
}  // namespace base

// VERSION 25, ANVIL FOR OLD CARDS (namespace oldcard): forge2's engine with the scratch steps (the parser's note).
namespace oldcard {
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32], const uint8_t* sk = nullptr, uint8_t* shares = nullptr, uint8_t* m_out = nullptr) { forge2::attempt(c, height, anchor, header, nonce, S, M, sk, shares, m_out); }  // F-441: the keyed inputs through to the walk
}  // namespace oldcard

// VERSION 23, TENSOR (namespace tensor): keyed 256-byte reads feeding INT8 x INT8 -> INT32 matrix multiplies, the parser's note above
// (the design review's priced page TENSOR-READ-FORM.md). Every operation integer; the accumulation order k = 0..15 ascending (exact in any order:
// the partial sums within +-262,144).
namespace tensor {
constexpr const char* kTagInit = "IgneumHash/Tensor/init", *kTagMix = "IgneumHash/Tensor/mix", *kTagPeriod = "IgneumHash/Forge/period";
void attempt(const ih_ctx& c, uint64_t height, const uint8_t* anchor, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32]) {
    const uint32_t steps = c.p.rounds, nt = c.p.program_ops;
    uint8_t pseed[32];
    if (c.p.period_blocks) { if (!anchor) anchor = c.p.program_anchor; bytes in; in.tag(kTagPeriod).raw(c.E, 32).le64(c.p.chain_id).le64(height / c.p.period_blocks).raw(anchor, 32); b3_hash(in, pseed, 32); }
    else { bytes in; in.tag(kTagPeriod).raw(c.E, 32).raw(header, 32).le64(nonce >> c.p.keyed_rounds); b3_hash(in, pseed, 32); }
    // the state: nt matrices of 16 x 16 INT8 from the init chain, and the read word
    std::vector<int8_t> T(size_t(nt) * 256); uint32_t x;
    { uint8_t d[32]; bytes in; in.tag(kTagInit).raw(c.E, 32).raw(pseed, 32).raw(S, 32); b3_hash(in, d, 32);
      uint32_t w[8]; for (int k = 0; k < 8; ++k) w[k] = le32(d + 4 * k);
      uint32_t n = 0;
      for (size_t i = 0; i < T.size(); i += 4, ++n) { w[n & 7] = r16f::fchain(w[n & 7], n); for (int b = 0; b < 4; ++b) T[i + b] = int8_t(uint8_t(w[n & 7] >> (8 * b))); }
      w[n & 7] = r16f::fchain(w[n & 7], n); x = w[n & 7]; }
    // the hash's sequence chain (the permutation a step)
    uint32_t y[8]; for (int k = 0; k < 8; ++k) y[k] = le32(S + 4 * k);
    const uint64_t items = c.n_items();
    uint32_t item[64]; int8_t B[256]; int32_t C[256]; uint8_t perm[16];
    static const bool trace = std::getenv("IGNPOW_TRACE") != nullptr;
    uint64_t n_mac = 0, n_item = 0, n_fold = 0;
    for (uint32_t t = 0; t < steps; ++t) {
        load_item(c, uint64_t(r16f::fchain(x, 8192u + t)) % items, item);
        for (int j = 0; j < 64; ++j) for (int b = 0; b < 4; ++b) B[4 * j + b] = int8_t(uint8_t(item[j] >> (8 * b)));
        if (trace) ++n_item;
        // the permutation of the tiles from the hash's chain: Fisher-Yates over 0..nt-1 by fchain(y, t)
        y[t & 7] = r16f::fchain(y[t & 7], t);
        for (uint32_t i = 0; i < nt; ++i) perm[i] = uint8_t(i);
        for (uint32_t i = nt - 1; i > 0; --i) { const uint32_t r = r16f::fchain(y[t & 7], 4096u + i) % (i + 1); std::swap(perm[i], perm[r]); }
        for (uint32_t j = 0; j < nt; ++j) {
            int8_t* A = &T[size_t(perm[j]) * 256];
            for (int i = 0; i < 16; ++i) for (int k = 0; k < 16; ++k) { int32_t acc = 0; for (int q = 0; q < 16; ++q) acc += int32_t(A[16 * i + q]) * int32_t(B[16 * q + k]); C[16 * i + k] = acc; }
            if (trace) n_mac += 4096;
            for (int i = 0; i < 256; ++i) A[i] = int8_t(uint8_t(r17::fmix32(uint32_t(C[i]) + (uint32_t(uint8_t(A[i])) << 24) + (j + 1u) * 0x9E3779B9u) >> 24));
            if (trace) n_fold += 256;
            x = r16f::fchain(x ^ uint32_t(C[0]) ^ uint32_t(C[255]), j);
        }
    }
    if (trace) std::fprintf(stderr, "tensor trace: steps %u tiles %u; keyed items %llu (%llu bytes); INT8 multiply-accumulates %llu (INT32 accumulation, %u x 16 x 16 x 16 a step); fold bytes %llu; lanes 16 (the matrix rows)\n", steps, nt, (unsigned long long)n_item, (unsigned long long)(256 * n_item), (unsigned long long)n_mac, nt, (unsigned long long)n_fold);
    // the lean mix: lane i (the row) folds its row of every tile into a 16-byte accumulator
    bytes out; out.tag(kTagMix).raw(c.E, 32).raw(pseed, 32);
    for (int i = 0; i < 16; ++i) {
        uint32_t acc[4] = { 0x9E3779B9u ^ uint32_t(i), 0x85EBCA6Bu, 0xC2B2AE35u, 0x27D4EB2Fu }; uint32_t n = 0;
        for (uint32_t j = 0; j < nt; ++j) for (int k = 0; k < 16; k += 4) { const uint8_t* r = reinterpret_cast<const uint8_t*>(&T[size_t(j) * 256 + size_t(16 * i + k)]); const uint32_t w = le32(r); acc[n & 3] = (acc[n & 3] ^ w) + w; ++n; }
        for (int k = 0; k < 4; ++k) out.le32(acc[k]);
    }
    b3_hash(out, M, 32);
}
}  // namespace tensor

// VERSION 22, KILN (namespace kiln). THE BATCH: batch_seed = BLAKE3(LP("IgneumHash/Kiln/batch") || E || header || LE64 (nonce >> batch_log2));
// the fragment programme's words by the fold chains over the seed's eight words (word i: w[j] = fchain(x[(2i + j) & 7], 2i + j), two
// words an instruction: w0 the class and the operands, w1 the immediate); the programme hash BLAKE3 over the 8 x program_ops bytes. THE
// FRAGMENT STATE: eight registers r0..r7 per covered pixel, each a fold of the scene word with the fragment's identity: f0 = x | (y << 16)
// | (t << 26), fser = x - minx (the serial within the row), r_k = fmix32(f0 + 0x9E3779B9 (k + 1) + fser 0x85EBCA6B) XOR (f0, the colour, t ^
// s1, s2..s6 for k = 0..7); instruction i reads a = r[(i + 3) & 7], b = r[(i + 5) & 7] and its destination d0 = r[i & 7] as the third
// operand: class = w0 mod 6 (0 ADD d = a + b + imm + rotl(d0, 9), 1 XOR d = a ^ b ^ imm ^ rotl(d0, 17), 2 ROTL d = rotl(a, imm & 31) ^ b ^ d0,
// 3 MUL d = a b + imm + d0, 4 MULHI d = (mulhi(a, b) ^ imm) + rotl(d0, 13), 5 TEX d = tile[(a + imm + d0) & 4095] ^ b ^ rotl(d0, 5): the
// NEAREST fetch of the fragment's tile at the index); the fragment's output colour r7 (RGBA, R the low byte). THE SCENE: scene = BLAKE3(LP("IgneumHash/Kiln/scene") || E || S), s its eight words; the tile index s0 mod (items / 64),
// the tile's row y = item (64 x tile + y), texel (x, y) = word x of it (NEAREST, no filter); triangle t from the chain c = fchain(s[t & 7],
// t): x0 = c & (side - 1), y0 = (c >> 8) & (side - 1), u = fmix32(c ^ 0x2545F491): x1 = clamp(x0 + u mod span - spread) with span = 2 spread + 1 (the document's "spread"), y1 = clamp(y0 + ((u >> 8)
// & 15) - 7), x2 = clamp(x0 + ((u >> 8) & 15) - 7), y2 = clamp(y0 + ((u >> 12) & 15) - 7) (the clamp to 0 .. side), the colour fmix32(c ^
// 0x9E3779B9), the blend (u >> 16) mod 5: 0 ONE + ONE ADD saturating, 1 ONE + ZERO (replace), 2 REVERSE SUBTRACT saturating (dst - src),
// 3 MIN, 4 MAX, each per byte on UNORM8. THE RASTER: version 18's rule (y down, the sample at the pixel's centre, the top-left rule,
// exact integer edge functions, a zero-area triangle draws nothing), the triangles in order, each covered pixel's fragment programme run
// and its colour blended into the target. THE DIGEST: M = BLAKE3(LP("IgneumHash/Kiln/mix") || E || S || programme hash || the target in
// row order, RGBA bytes). The verifier derives or reads the tile's 64 items and renders the scene in software.
namespace kiln {
constexpr const char* kTagBatch = "IgneumHash/Kiln/batch", *kTagScene = "IgneumHash/Kiln/scene", *kTagMix = "IgneumHash/Kiln/mix";
inline uint8_t blend_byte(uint32_t mode, uint32_t dst, uint32_t src) {
    switch (mode) { case 0: return uint8_t(std::min<uint32_t>(255u, dst + src)); case 1: return uint8_t(src); case 2: return uint8_t(dst > src ? dst - src : 0u); case 3: return uint8_t(std::min(dst, src)); default: return uint8_t(std::max(dst, src)); }
}
void attempt(const ih_ctx& c, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32]) {
    const uint32_t T = c.p.rounds, words = c.p.program_ops, side = 1u << c.p.registers;
    uint8_t bseed[32]; { bytes in; in.tag(kTagBatch).raw(c.E, 32).raw(header, 32).le64(nonce >> c.p.keyed_rounds); b3_hash(in, bseed, 32); }
    std::vector<uint32_t> pw(size_t(words) * 2); std::vector<uint8_t> pbytes(size_t(words) * 8);
    { uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(bseed + 4 * k);
      for (uint32_t i = 0; i < words; ++i) for (int j = 0; j < 2; ++j) { const uint32_t idx = 2 * i + j; x[idx & 7] = r16f::fchain(x[idx & 7], idx); pw[idx] = x[idx & 7]; for (int b = 0; b < 4; ++b) pbytes[size_t(idx) * 4 + b] = uint8_t(x[idx & 7] >> (8 * b)); } }
    uint8_t phash[32]; b3_hash(pbytes.data(), pbytes.size(), phash, 32);
    uint8_t sc[32]; { bytes in; in.tag(kTagScene).raw(c.E, 32).raw(S, 32); b3_hash(in, sc, 32); }
    uint32_t sw[8]; for (int k = 0; k < 8; ++k) sw[k] = le32(sc + 4 * k);
    // the tiles: `tiles` keyed indices (64 items each), tile k's index fchain(s0, 1000 + k) mod the tile count; the rows read on demand
    const uint64_t tile_count = c.n_items() / 64; const uint32_t ntiles = c.p.fold_every ? c.p.fold_every : 1u;
    std::vector<uint64_t> tix(ntiles); for (uint32_t k = 0; k < ntiles; ++k) tix[k] = uint64_t(r16f::fchain(sw[0], 1000u + k)) % tile_count;
    std::vector<uint32_t> tl(size_t(ntiles) * 64 * 64); std::vector<uint8_t> have(size_t(ntiles) * 64, 0);
    auto texel = [&](uint32_t k, uint32_t idx) -> uint32_t { const uint32_t y = (idx >> 6) & 63u; if (!have[size_t(k) * 64 + y]) { load_item(c, tix[k] * 64 + y, &tl[(size_t(k) * 64 + y) * 64]); have[size_t(k) * 64 + y] = 1; } return tl[(size_t(k) * 64 + y) * 64 + (idx & 63u)]; };
    const uint32_t tlog = c.p.registers, nrows = c.p.blocks, row_count = ntiles * 64u, fcap = c.p.table_kib; const bool coal = (c.p.access_bytes & 1u) != 0; const uint32_t contig = (c.p.access_bytes >> 8) ? (c.p.access_bytes >> 8) : 1u;
 // fcap: the exact fragment count (0 = the covered alone)
    std::vector<uint8_t> covered(size_t(side) * side, 0); uint32_t nrun = 0;
    static const bool trace = std::getenv("IGNPOW_TRACE") != nullptr;  // the Kiln replay counters (the measurement chair's cell)
    uint64_t n_frag = 0, n_tex = 0, n_rowfetch = 0, n_blend[5] = {0}, n_write = 0; std::unordered_map<uint64_t, uint8_t> sectors; std::string tile_list;
    const uint64_t all_rows = c.n_items();  // the coalesced rows over the whole dataset (an item a row)
    uint32_t citem[64];
    auto coal_rows = [&](uint32_t index, uint32_t* r) {  // the run's rows, the fragment's slice
        const uint32_t run = index >> 4, sl = (index & 15u) * 4u;
        for (uint32_t q = 0; q < nrows; ++q) {
            const uint64_t row = contig == 1u ? uint64_t(r17::fmix32(run ^ sw[7] ^ (q * 0x9E3779B9u))) % all_rows
                                             : uint64_t(r17::fmix32(run ^ sw[7] ^ ((q / contig) * 0x9E3779B9u))) % (all_rows - contig) + (q % contig);
            load_item(c, row, citem);
            uint32_t acc[4] = { uint32_t(row) ^ sw[6], uint32_t(row) * 0x9E3779B1u, sw[5] ^ q, sw[7] + uint32_t(row) };
            for (int k = 0; k < 4; ++k) { const uint32_t v = citem[sl + uint32_t(k)]; acc[k] = (acc[k] ^ v) + v; }
            if (trace) { n_rowfetch += 1; sectors[row * 8 + (sl >> 3)] = 1; }
            r[3 + (q % 5)] ^= acc[0] ^ acc[1] ^ acc[2] ^ acc[3];
        }
    };     std::vector<uint8_t> fb(size_t(side) * side * 4, 0);
    for (uint32_t t = 0; t < T; ++t) {
        const uint32_t cw = r16f::fchain(sw[t & 7], t), u = r17::fmix32(cw ^ 0x2545F491u), colour = r17::fmix32(cw ^ 0x9E3779B9u), mode = (u >> 16) % 5u;
        auto cl = [&](int32_t v) { return v < 0 ? 0 : v > int32_t(side) ? int32_t(side) : v; };
        const int32_t x0 = int32_t(cw & (side - 1u)), y0 = int32_t((cw >> 8) & (side - 1u));
        const int32_t sp = int32_t(c.p.scratch_bytes), span = 2 * sp + 1;  // the corner offsets in -spread .. +spread (the document's "spread")
        const int32_t x1 = cl(x0 + int32_t(u % uint32_t(span)) - sp), y1 = cl(y0 + int32_t((u >> 8) % uint32_t(span)) - sp), x2 = cl(x0 + int32_t((u >> 16) % uint32_t(span)) - sp), y2 = cl(y0 + int32_t((u >> 24) % uint32_t(span)) - sp);
        const int64_t ax = x0, ay = y0, bx = x1, by = y1, cx = x2, cy = y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
        if (area == 0) continue;
        const int64_t sg = area > 0 ? 1 : -1;
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == sg; else e[k].tl = (-dy > 0 ? 1 : -1) == sg; }
        const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
        for (int64_t j = miny; j < maxy; ++j) for (int64_t i = minx; i < maxx; ++i) {
            const int64_t px = 2 * i + 1, py = 2 * j + 1; bool in = true;
            for (int k = 0; k < 3 && in; ++k) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * sg; if (w < 0 || (w == 0 && !e[k].tl)) in = false; }
            if (!in) continue;
            if (fcap && nrun >= fcap) continue;  // the exact count: only the first fcap covered fragments in raster order
            ++nrun; covered[size_t(j) * side + size_t(i)] = 1;
            // the fragment programme; the fragment's tile from its own identity
            const uint32_t fid = (t << (2u * tlog)) | (uint32_t(j) << tlog) | uint32_t(i);
            const uint32_t tk = (fid >> 4) % ntiles;
            if (trace) { ++n_frag; ++n_blend[mode]; if (n_frag <= 64) { tile_list += std::to_string(tk); tile_list += ' '; } }
            // the fragment's registers (the Kiln verifier seat's fault, the project's ruling 04:1x UK: instructions 0 to 2 overwrote r0..r2 before
            // any read, so every fragment computed the scene's constants and the hash collapsed to once a tile): EVERY initial register is
            // a fold of the scene word with the fragment's identity (x, y, the triangle index, the fragment's serial within its row = x -
            // minx), so none is scene-constant, and every instruction reads its destination as a third operand (below)
            const uint32_t f0 = uint32_t(i) | (uint32_t(j) << 16) | (t << 26), fser = uint32_t(i - minx);
            uint32_t r[8];
            for (uint32_t k = 0; k < 8; ++k) r[k] = r17::fmix32(f0 + 0x9E3779B9u * (k + 1u) + fser * 0x85EBCA6Bu) ^ (k == 0 ? f0 : k == 1 ? colour : k == 2 ? (t ^ sw[1]) : sw[k - 1]);
            if (nrows && coal) coal_rows(nrun - 1u, r); else if (nrows) {  // the row fetches: `rows` full tile rows from the fragment's identity and the state, each folded into the state
                const uint32_t start = r17::fmix32(fid ^ sw[7]), stride = row_count / nrows;
                for (uint32_t q = 0; q < nrows; ++q) {
                    const uint32_t row = (start + q * stride) % row_count;
                    // the consuming fold (the security review's ruling a655aaa4a: the cheapest, about half an operation a byte): each uvec4 of the
                    // row folded by one xor and one add into a 16-byte accumulator, acc = (acc ^ v) + v a lane, no multiply, no rotate inside
                    // the row; one mix of the accumulator into the state per row
                    uint32_t acc[4] = { row ^ sw[6], row * 0x9E3779B1u, sw[5] ^ q, sw[7] + row };
                    for (uint32_t w = 0; w < 64; w += 4) for (int k = 0; k < 4; ++k) { const uint32_t v = texel(row >> 6, (row & 63u) * 64u + w + uint32_t(k)); acc[k] = (acc[k] ^ v) + v; }
                    if (trace) { n_rowfetch += 16; for (uint32_t w = 0; w < 64; w += 8) sectors[(uint64_t(tix[row >> 6]) * 64 + (row & 63u)) * 8 + (w >> 3)] = 1; }
                    r[3 + (q % 5)] ^= acc[0] ^ acc[1] ^ acc[2] ^ acc[3];
                }
            }
            for (uint32_t q = 0; q < words; ++q) {
                const uint32_t w0 = pw[2 * q], imm = pw[2 * q + 1], a = r[(q + 3) & 7], b = r[(q + 5) & 7]; uint32_t& d = r[q & 7];
                const uint32_t d0 = d;  // the destination read as the third operand (the RandomX way): the file never loses state
                switch (w0 % 6u) {
                    case 0: d = a + b + imm + rotl32(d0, 9); break; case 1: d = a ^ b ^ imm ^ rotl32(d0, 17); break; case 2: d = rotl32(a, imm & 31u) ^ b ^ d0; break;
                    case 3: d = a * b + imm + d0; break; case 4: d = (uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm) + rotl32(d0, 13); break;
                    default: { const uint32_t idx = (a + imm + d0) & 4095u; d = texel(tk, idx) ^ b ^ rotl32(d0, 5); if (trace) { ++n_tex; sectors[(uint64_t(tix[tk]) * 64 + (idx >> 6)) * 8 + ((idx & 63u) >> 3)] = 1; } break; }  // TEX: the NEAREST fetch of the fragment's tile at an address of a, the immediate and the destination
                }
            }
            uint8_t* q = &fb[(size_t(j) * side + size_t(i)) * 4];
            for (int k = 0; k < 4; ++k) q[k] = blend_byte(mode, q[k], (r[7] >> (8 * k)) & 255u);
            if (trace) ++n_write;
        }
    }
    if (fcap && nrun < fcap) {
        // the fill: the uncovered pixels in raster order with the triangle index 0 and the colour 0, the same programme and row
        // fetches, the result blended by mode 0 (ONE + ONE ADD saturating), until exactly fcap passes a hash
        const uint32_t t = 0, colour = 0, mode = 0; const int32_t minx = 0;
        for (uint32_t j = 0; j < side && nrun < fcap; ++j) for (uint32_t i = 0; i < side && nrun < fcap; ++i) {
            if (covered[size_t(j) * side + i]) continue;
            ++nrun;
            const uint32_t fid = (t << (2u * tlog)) | (j << tlog) | i;
            const uint32_t tk = (fid >> 4) % ntiles;
            if (trace) { ++n_frag; ++n_blend[mode]; if (n_frag <= 64) { tile_list += std::to_string(tk); tile_list += ' '; } }
            const uint32_t f0 = i | (j << 16) | (t << 26), fser = i - uint32_t(minx);
            uint32_t r[8];
            for (uint32_t k = 0; k < 8; ++k) r[k] = r17::fmix32(f0 + 0x9E3779B9u * (k + 1u) + fser * 0x85EBCA6Bu) ^ (k == 0 ? f0 : k == 1 ? colour : k == 2 ? (t ^ sw[1]) : sw[k - 1]);
            if (nrows && coal) coal_rows(nrun - 1u, r); else if (nrows) {
                const uint32_t start = r17::fmix32(fid ^ sw[7]), stride = row_count / nrows;
                for (uint32_t q = 0; q < nrows; ++q) {
                    const uint32_t row = (start + q * stride) % row_count;
                    uint32_t acc[4] = { row ^ sw[6], row * 0x9E3779B1u, sw[5] ^ q, sw[7] + row };
                    for (uint32_t w = 0; w < 64; w += 4) for (int k = 0; k < 4; ++k) { const uint32_t v = texel(row >> 6, (row & 63u) * 64u + w + uint32_t(k)); acc[k] = (acc[k] ^ v) + v; }
                    if (trace) { n_rowfetch += 16; for (uint32_t w = 0; w < 64; w += 8) sectors[(uint64_t(tix[row >> 6]) * 64 + (row & 63u)) * 8 + (w >> 3)] = 1; }
                    r[3 + (q % 5)] ^= acc[0] ^ acc[1] ^ acc[2] ^ acc[3];
                }
            }
            for (uint32_t q = 0; q < words; ++q) {
                const uint32_t w0 = pw[2 * q], imm = pw[2 * q + 1], a = r[(q + 3) & 7], b = r[(q + 5) & 7]; uint32_t& d = r[q & 7];
                const uint32_t d0 = d;
                switch (w0 % 6u) {
                    case 0: d = a + b + imm + rotl32(d0, 9); break; case 1: d = a ^ b ^ imm ^ rotl32(d0, 17); break; case 2: d = rotl32(a, imm & 31u) ^ b ^ d0; break;
                    case 3: d = a * b + imm + d0; break; case 4: d = (uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm) + rotl32(d0, 13); break;
                    default: { const uint32_t idx = (a + imm + d0) & 4095u; d = texel(tk, idx) ^ b ^ rotl32(d0, 5); if (trace) { ++n_tex; sectors[(uint64_t(tix[tk]) * 64 + (idx >> 6)) * 8 + ((idx & 63u) >> 3)] = 1; } break; }
                }
            }
            uint8_t* q = &fb[(size_t(j) * side + size_t(i)) * 4];
            for (int k = 0; k < 4; ++k) q[k] = blend_byte(mode, q[k], (r[7] >> (8 * k)) & 255u);
            if (trace) ++n_write;
        }
    }
    if (trace) std::fprintf(stderr, "kiln trace: T %u words %u target %u tiles %u rows %u; fragments %llu; NEAREST fetches %llu (TEX) + %llu (row uvec4 x 4 words); distinct 32-byte sectors %zu (%zu bytes); target writes %llu; blends add %llu replace %llu rsub %llu min %llu max %llu; the first fragments' tiles: %s\n",
        T, words, side, ntiles, nrows, (unsigned long long)n_frag, (unsigned long long)n_tex, (unsigned long long)n_rowfetch, sectors.size(), sectors.size() * 32, (unsigned long long)n_write, (unsigned long long)n_blend[0], (unsigned long long)n_blend[1], (unsigned long long)n_blend[2], (unsigned long long)n_blend[3], (unsigned long long)n_blend[4], tile_list.c_str());
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag(kTagMix).raw(c.E, 32).raw(S, 32).raw(phash, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    blake3_hasher_update(&h, fb.data(), fb.size());
    blake3_hasher_finalize(&h, M, 32);
}
}  // namespace kiln
#include "kiln_verify.hpp"  // the Kiln verifier: the fast render of the same rule, the reference above the normative text

int ih_hash_a(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
              uint8_t mix_out[32], uint8_t hash_out[32], const uint8_t* anchor) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    if (c.p.version == kRotationVersion) { if (height > kMaxHeight) return IGNPOW_E_HEIGHT; if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH; return ih_hash_a(c.fam[r15_family(c.p, height, anchor)].get(), height, header, nonce, mix_out, hash_out, anchor); }
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH;
    if ((c.p.version >= 8) && (c.p.features & 1024)) return IGNPOW_E_ARG;  // a keyed document has no keyless work (section 0.9)
    uint8_t S[32];
    work_seed(c, header, nonce, S);
    if (c.p.version == 15) { r16::attempt(c, height, anchor, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 17) { r17::attempt(c, height, anchor, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 18) { r18::attempt(c, height, anchor, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 19) { r19::attempt(c, height, anchor, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 20) { forge::attempt(c, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 21) { forge2::attempt(c, height, anchor, header, nonce, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 22) { kiln::verify(c, header, nonce, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 23) { tensor::attempt(c, height, anchor, header, nonce, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 24) { base::attempt(c, height, anchor, header, nonce, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 25) { oldcard::attempt(c, height, anchor, header, nonce, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 16) { if ((c.p.program_distance >> 18) & 3) r16w::attempt(c, height, anchor, S, mix_out); else if (c.p.program_distance & 32768) r16s::attempt(c, height, anchor, S, mix_out); else r16f::attempt(c, height, anchor, S, mix_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    const auto prog = (c.p.version >= 9) ? r0d_program(c, height, anchor) : nullptr;
    const auto sh = (c.p.version >= 12 && c.p.shared_kib) ? r12_shared_table(c, height, anchor) : nullptr;
    work(c, S, mix_out, nullptr, nullptr, nullptr, nullptr, prog ? prog.get() : nullptr, sh ? sh.get() : nullptr);
    final_digest(c, S, mix_out, hash_out);
    return IGNPOW_OK;
}

// the Kiln verifier's two paths for the exactness proof (kiln_verify.hpp): path 0 the reference render, 1 the fast render
int ih_kiln_hash_path(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32], int path) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    if (c.p.version != 22) return IGNPOW_E_ARG;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH;
    uint8_t S[32]; work_seed(c, header, nonce, S);
    if (path) kiln::attempt_fast(c, header, nonce, S, mix_out); else kiln::attempt(c, header, nonce, S, mix_out);
    final_digest(c, S, mix_out, hash_out);
    return IGNPOW_OK;
}

int ih_hash(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
            uint8_t mix_out[32], uint8_t hash_out[32]) {
    return ih_hash_a(ctx, height, header, nonce, mix_out, hash_out, nullptr);
}

// bit 10 (section 0.9): the prover's hash under the bond secret (the shares out) and the verifier's replay from the shares
int ih_hash_keyed(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t* sk,
                  uint8_t* shares, uint8_t mix_out[32], uint8_t hash_out[32], const uint8_t* anchor = nullptr) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    if (c.p.version >= 15 && c.p.version != 18 && c.p.version != 21 && c.p.version != 25) return IGNPOW_E_ARG;  // versions 15 to 17 are keyed by the period, not by a bond secret; F-441 opens 18 (under the period key), 21 and 25 (the chain form) to the bond-keyed walk
    if (c.p.version == kRotationVersion) { if (height > kMaxHeight) return IGNPOW_E_HEIGHT; if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH; return ih_hash_keyed(c.fam[r15_family(c.p, height, anchor)].get(), height, header, nonce, sk, shares, mix_out, hash_out, anchor); }
    if (!(c.p.features & 1024)) return IGNPOW_E_PARAMS;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH;
    uint8_t S[32];
    work_seed(c, header, nonce, S);
    // F-441: the released walks' bond-keyed forms (the specification's sections 2 and 3), the shares made under sk or replayed from the given
    if (c.p.version == 18) { r18::attempt(c, height, anchor, S, mix_out, sk, shares, nullptr); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 21) { forge2::attempt(c, height, anchor, header, nonce, S, mix_out, sk, shares, nullptr); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 25) { oldcard::attempt(c, height, anchor, header, nonce, S, mix_out, sk, shares, nullptr); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    const auto prog = (c.p.version >= 9) ? r0d_program(c, height, anchor) : nullptr;
    const auto sh = (c.p.version >= 12 && c.p.shared_kib) ? r12_shared_table(c, height, anchor) : nullptr;
    work(c, S, mix_out, nullptr, sk, shares, nullptr, prog ? prog.get() : nullptr, sh ? sh.get() : nullptr);
    final_digest(c, S, mix_out, hash_out);
    return IGNPOW_OK;
}

// ih_hash_keyed with the keyed rounds' messages written to m_out (keyed_rounds x 32 bytes), the node's input to pi.
int ih_hash_keyed_m(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t* sk,
                    uint8_t* shares, uint8_t mix_out[32], uint8_t hash_out[32], uint8_t* m_out, const uint8_t* anchor = nullptr) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    if (c.p.version >= 15 && c.p.version != 18 && c.p.version != 21 && c.p.version != 25) return IGNPOW_E_ARG;  // as ih_hash_keyed: the period-keyed versions have no bond secret
    if (c.p.version == kRotationVersion) { if (height > kMaxHeight) return IGNPOW_E_HEIGHT; if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH; return ih_hash_keyed_m(c.fam[r15_family(c.p, height, anchor)].get(), height, header, nonce, sk, shares, mix_out, hash_out, m_out, anchor); }
    if (!(c.p.features & 1024)) return IGNPOW_E_PARAMS;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH;
    uint8_t S[32];
    work_seed(c, header, nonce, S);
    // F-441: the released walks' bond-keyed forms with the messages m_i out (the proof pi's input)
    if (c.p.version == 18) { r18::attempt(c, height, anchor, S, mix_out, sk, shares, m_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 21) { forge2::attempt(c, height, anchor, header, nonce, S, mix_out, sk, shares, m_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    if (c.p.version == 25) { oldcard::attempt(c, height, anchor, header, nonce, S, mix_out, sk, shares, m_out); final_digest(c, S, mix_out, hash_out); return IGNPOW_OK; }
    const auto prog = (c.p.version >= 9) ? r0d_program(c, height, anchor) : nullptr;
    const auto sh = (c.p.version >= 12 && c.p.shared_kib) ? r12_shared_table(c, height, anchor) : nullptr;
    work(c, S, mix_out, nullptr, sk, shares, m_out, prog ? prog.get() : nullptr, sh ? sh.get() : nullptr);
    final_digest(c, S, mix_out, hash_out);
    return IGNPOW_OK;
}

int ih_verify(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
              const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32];
    int st = ih_hash(ctx, height, header, nonce, m, h);
    if (st != IGNPOW_OK) return st;
    const bool mix_ok = std::memcmp(m, mix, 32) == 0;
    const bool bound_ok = le256(h, boundary);
    if (!mix_ok) return IGNPOW_E_MIX;
    if (!bound_ok) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}

int ih_light_cache(const ctx_base* ctx, const void** data, uint64_t* bytes_out, uint32_t* item_bytes) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    if (c.p.version >= 1) {
        if (data) *data = c.cache_v().data();      // a shared context serves its donor's cache
        if (bytes_out) *bytes_out = c.cache_v().size();
        if (item_bytes) *item_bytes = kItemBytes;  // the cache's items are 128 bytes under every version
        return IGNPOW_OK;
    }
    if (data) *data = c.E;
    if (bytes_out) *bytes_out = 32;
    if (item_bytes) *item_bytes = 32;
    return IGNPOW_OK;
}

bool hex_eq(const uint8_t* b, size_t n, const char* hex) {
    static const char* d = "0123456789abcdef";
    for (size_t i = 0; i < n; ++i) if (d[b[i] >> 4] != hex[2 * i] || d[b[i] & 15] != hex[2 * i + 1]) return false;
    return true;
}

// Embedded self-test: the official BLAKE3 vectors (hash, keyed and derive-key
// modes with 131 bytes of extendable output), then the light path against the
// full path on a 16 MiB, 8-round experiment variant (same bytes, by
// construction), then the baseline known-answer vector once it is recorded.
#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "igneumhash self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)

int ih_vectors_check(size_t* count_out) {
    size_t n = 0;
    {
        std::vector<uint8_t> in(8192);
        for (size_t i = 0; i < in.size(); ++i) in[i] = uint8_t(i % 251);
        const uint8_t* key = reinterpret_cast<const uint8_t*>("whats the Elvish word for friend");
        const char* context = "BLAKE3 2019-12-27 16:29:52 test vectors context";
        uint8_t out[131];
        for (const b3_vec& v : kB3Vectors) {
            blake3_hasher h;
            blake3_hasher_init(&h); blake3_hasher_update(&h, in.data(), v.len); blake3_hasher_finalize(&h, out, 131);
            if (!hex_eq(out, 131, v.hash)) FAILV();
            blake3_hasher_init_keyed(&h, key); blake3_hasher_update(&h, in.data(), v.len); blake3_hasher_finalize(&h, out, 131);
            if (!hex_eq(out, 131, v.keyed)) FAILV();
            blake3_hasher_init_derive_key(&h, context); blake3_hasher_update(&h, in.data(), v.len); blake3_hasher_finalize(&h, out, 131);
            if (!hex_eq(out, 131, v.derive)) FAILV();
            ++n;
        }
    }
    {
        {
            // version 3 at item_bytes 128 is version 1's loop word for word: the same E, cache and P give the same out
            const char* v1 = "{\"version\": 1, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16}";
            const char* v3 = "{\"version\": 3, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}";
            int st = 0;
            std::unique_ptr<ctx_base> a(ih_create(v1, 1, 0, &st)), b(ih_create(v3, 1, 0, &st));
            if (!a || !b) FAILV();
            auto& ca = *static_cast<ih_ctx*>(a.get()); auto& cb = *static_cast<ih_ctx*>(b.get());
            std::memcpy(cb.P, ca.P, 32); std::memcpy(cb.E, ca.E, 32); cb.store = ca.store;  // the self-test's own aliasing of one cache (the store shared by hand)
            // the seed binds V, so the comparison is of WORK on one seed and one cache
            uint8_t S[32]; for (int i = 0; i < 32; ++i) S[i] = uint8_t(13 * i + 5);
            uint8_t m1[32], m2[32];
            work(ca, S, m1, nullptr);
            work(cb, S, m2, nullptr);
            if (std::memcmp(m1, m2, 32)) FAILV();
            ignh_params p3; if (parse_params(v3, p3) != IGNPOW_OK || params_len(p3) != 52) FAILV();
            if (parse_params("{\"version\": 1, \"item_bytes\": 256}", p3) != IGNPOW_E_PARAMS) FAILV();  // version 3's field only
            ++n;
        }
        {
            // version 4 (Strata): the four widths parse to 52 bytes with distinct P; 64 is version 4's only; the word h of a
            // 16-word item at 64 bytes is the tree over 16 leaves (the narrow control runs the same relation)
            const char* v4w[] = {"64", "128", "256", "512"};
            uint8_t prev[32] = {0};
            for (const char* w : v4w) {
                char doc[200]; std::snprintf(doc, sizeof doc, "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": %s}", w);
                ignh_params p4; if (parse_params(doc, p4) != IGNPOW_OK || params_len(p4) != 52 || p4.item_bytes != uint32_t(std::atoi(w))) FAILV();
                uint8_t pb[IGNH_PARAMS_BYTES], P[32]; params_bytes(p4, pb); b3_hash(pb, 52, P, 32);
                if (std::memcmp(P, prev, 32) == 0) FAILV();
                std::memcpy(prev, P, 32);
            }
            ignh_params px;
            if (parse_params("{\"version\": 3, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 64}", px) != IGNPOW_E_PARAMS) FAILV();
            if (parse_params("{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16}", px) != IGNPOW_E_PARAMS) FAILV();  // item_bytes mandatory under version 4
            if (parse_params("{\"version\": 4, \"dataset_mib\": 48, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}", px) != IGNPOW_E_PARAMS) FAILV();  // a non-power-of-two size is version 5's
            if (parse_params("{\"version\": 5, \"dataset_mib\": 3072, \"scratch_kib\": 0, \"rounds\": 64, \"cache_mib\": 32, \"parents\": 256, \"item_bytes\": 128}", px) != IGNPOW_OK || params_len(px) != 52) FAILV();
            if (parse_params("{\"version\": 8, \"dataset_mib\": 2048, \"scratch_kib\": 0, \"rounds\": 64, \"cache_mib\": 32, \"parents\": 256, \"item_bytes\": 128, \"blocks\": 4, \"registers\": 16}", px) != IGNPOW_OK || params_len(px) != 60 || px.blocks != 4 || px.registers != 16) FAILV();
            if (parse_params("{\"version\": 8, \"dataset_mib\": 2048, \"scratch_kib\": 0, \"rounds\": 64, \"cache_mib\": 32, \"parents\": 256, \"item_bytes\": 128}", px) != IGNPOW_E_PARAMS) FAILV();  // blocks and registers mandatory
            if (parse_params("{\"version\": 4, \"dataset_mib\": 2048, \"scratch_kib\": 0, \"rounds\": 64, \"cache_mib\": 32, \"parents\": 256, \"item_bytes\": 128, \"blocks\": 1, \"registers\": 8}", px) != IGNPOW_E_PARAMS) FAILV();  // version 8's keys only
            ++n;
        }
        const char* variants[] = {
            "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 1024}",
            "{\"version\": 5, \"dataset_mib\": 48, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 2, \"registers\": 8}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 4, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 2, \"registers\": 16, \"features\": 11}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 1, \"registers\": 8, \"features\": 6}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 2, \"registers\": 16, \"features\": 16}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 32}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 32, \"registers\": 16, \"features\": 256}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 32, \"registers\": 16, \"features\": 544}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 4, \"registers\": 16, \"features\": 160}",
            "{\"version\": 8, \"dataset_mib\": 48, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 8, \"registers\": 16, \"features\": 32}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 1056, \"keyed_rounds\": 4}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 5152, \"keyed_rounds\": 3}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 1056, \"keyed_rounds\": 8}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 4128}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 1, \"registers\": 32, \"features\": 4128}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 1, \"registers\": 64, \"features\": 4128}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 4, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 2, \"registers\": 64}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 16, \"registers\": 16, \"features\": 2080}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 8, \"registers\": 16, \"features\": 4098}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 256, \"blocks\": 16, \"registers\": 16, \"features\": 32}",
            "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": 1, \"registers\": 16}",
            "{\"version\": 7, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}",
            "{\"version\": 7, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 1024}",
            "{\"version\": 6, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}",
            "{\"version\": 6, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 256}",
            "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 32}",
            "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 64}",
            "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}",
            "{\"version\": 4, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 512}",
            "{\"version\": 3, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 256}",
            "{\"version\": 3, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 512}",
            "{\"version\": 0, \"dataset_mib\": 16, \"scratch_kib\": 4, \"rounds\": 8, \"item_chain\": 2}",
            "{\"version\": 1, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16}",
            "{\"version\": 1, \"dataset_mib\": 16, \"scratch_kib\": 4, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16}",
            "{\"version\": 2, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"schedule\": 3}",
        };
        for (const char* pj : variants) {
            int st = 0;
            std::unique_ptr<ctx_base> light(ih_create(pj, 1, 0, &st));
            if (!light || st != IGNPOW_OK) FAILV();
            std::unique_ptr<ctx_base> full(ih_create(pj, 1, 1, &st));
            if (!full || st != IGNPOW_OK) FAILV();
            uint8_t hdr[32];
            for (int i = 0; i < 32; ++i) hdr[i] = uint8_t(i * 7 + 3);
            const bool keyed_doc = static_cast<const ih_ctx*>(light.get())->p.features & 1024;
            for (uint64_t nonce = 0; nonce < 3; ++nonce) {
                uint8_t m1[32], h1[32], m2[32], h2[32];
                if (keyed_doc) {
                    // section 0.9: the keyless entry refuses; the prover's hash under a secret equals the verifier's replay
                    // from the shares on both contexts; a wrong secret or a wrong share changes the digest
                    uint8_t sk[32]; for (int i = 0; i < 32; ++i) sk[i] = uint8_t(i * 29 + 11 + nonce);
                    uint8_t shares[64 * 32], sh2[64 * 32], m3[32], h3[32];
                    if (ih_hash(light.get(), 1, hdr, nonce, m1, h1) != IGNPOW_E_ARG) FAILV();
                    if (ih_hash_keyed(light.get(), 1, hdr, nonce, sk, shares, m1, h1) != IGNPOW_OK) FAILV();
                    if (ih_hash_keyed(full.get(), 1, hdr, nonce, nullptr, shares, m2, h2) != IGNPOW_OK) FAILV();  // the replay on the full context
                    if (std::memcmp(m1, m2, 32) || std::memcmp(h1, h2, 32)) FAILV();
                    {
                        // the shared context (ih_create_shared): the keyed document over its unkeyed TWIN's full context hashes and replays
                        // the same digest, the twin's own hash runs on the donor, the keyless entry still refuses on the shared one, and a
                        // donor of another epoch seed is refused
                        const auto& kp = static_cast<const ih_ctx*>(light.get())->p;
                        char twin[320]; std::snprintf(twin, sizeof twin, "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128, \"blocks\": %u, \"registers\": %u, \"features\": %u}", kp.blocks, kp.registers, kp.features & ~1024u);
                        int st2 = 0;
                        std::unique_ptr<ctx_base> donor(ih_create(twin, 1, 1, &st2)); if (!donor || st2 != IGNPOW_OK) FAILV();
                        std::unique_ptr<ctx_base> sh(ih_create_shared_impl(pj, 1, donor.get(), &st2)); if (!sh || st2 != IGNPOW_OK) FAILV();
                        uint8_t m4[32], h4[32], sh4[64 * 32];
                        if (ih_hash(sh.get(), 1, hdr, nonce, m4, h4) != IGNPOW_E_ARG) FAILV();
                        if (ih_hash_keyed(sh.get(), 1, hdr, nonce, sk, sh4, m4, h4) != IGNPOW_OK) FAILV();
                        if (std::memcmp(m1, m4, 32) || std::memcmp(h1, h4, 32) || std::memcmp(shares, sh4, size_t(kp.keyed_rounds) * 32)) FAILV();  // the written shares alone
                        if (ih_hash_keyed(sh.get(), 1, hdr, nonce, nullptr, shares, m4, h4) != IGNPOW_OK || std::memcmp(h1, h4, 32)) FAILV();
                        if (ih_hash(donor.get(), 1, hdr, nonce, m4, h4) != IGNPOW_OK) FAILV();
                        std::unique_ptr<ctx_base> other(ih_create("{\"version\": 3, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 16, \"item_bytes\": 128}", 1, 0, &st2));
                        if (!other) FAILV();
                        std::unique_ptr<ctx_base> bad(ih_create_shared_impl(pj, 1, other.get(), &st2)); if (bad || st2 != IGNPOW_E_PARAMS) FAILV();
                        // a donor whose dataset seed differs by one field (parents 17 against 16: Pd and so E differ) is refused too
                        char near[320]; std::snprintf(near, sizeof near, "{\"version\": 8, \"dataset_mib\": 16, \"scratch_kib\": 0, \"rounds\": 8, \"cache_mib\": 1, \"parents\": 17, \"item_bytes\": 128, \"blocks\": %u, \"registers\": %u, \"features\": %u}", kp.blocks, kp.registers, kp.features & ~1024u);
                        std::unique_ptr<ctx_base> nd(ih_create(near, 1, 0, &st2)); if (!nd) FAILV();
                        std::unique_ptr<ctx_base> bad2(ih_create_shared_impl(pj, 1, nd.get(), &st2)); if (bad2 || st2 != IGNPOW_E_PARAMS) FAILV();
                    }
                    sk[0] ^= 1;
                    if (ih_hash_keyed(light.get(), 1, hdr, nonce, sk, sh2, m3, h3) != IGNPOW_OK) FAILV();
                    if (!std::memcmp(h1, h3, 32) || !std::memcmp(shares, sh2, 32)) FAILV();
                    std::memcpy(sh2, shares, sizeof shares); sh2[40] ^= 1;  // the second share's ninth byte
                    if (ih_hash_keyed(light.get(), 1, hdr, nonce, nullptr, sh2, m3, h3) != IGNPOW_OK) FAILV();
                    if (!std::memcmp(h1, h3, 32)) FAILV();
                    // section 0.9.1, the node's input to pi (ignh_hash_keyed_m): the m_i written on the prover's call equal
                    // those written on the verifier's replay; m_0 is the work seed; every share is H(sk, i, m_i); the
                    // keyed digest is unchanged by the extra output
                    {
                        sk[0] ^= 1;  // back to the secret of `shares`
                        const auto& cc = *static_cast<const ih_ctx*>(light.get());
                        const uint32_t K = cc.p.keyed_rounds;
                        uint8_t mp[64 * 32], mv[64 * 32], sp[64 * 32], S0[32], m4[32], h4[32], si[32];
                        if (ih_hash_keyed_m(light.get(), 1, hdr, nonce, sk, sp, m4, h4, mp) != IGNPOW_OK) FAILV();
                        if (std::memcmp(m4, m1, 32) || std::memcmp(h4, h1, 32) || std::memcmp(sp, shares, size_t(K) * 32)) FAILV();
                        if (ih_hash_keyed_m(full.get(), 1, hdr, nonce, nullptr, sp, m4, h4, mv) != IGNPOW_OK) FAILV();
                        if (std::memcmp(h4, h1, 32) || std::memcmp(mp, mv, size_t(K) * 32)) FAILV();
                        work_seed(cc, hdr, nonce, S0);
                        if (std::memcmp(mp, S0, 32)) FAILV();
                        for (uint32_t i = 0; i < K; ++i) { ignh_bond_share(sk, i, mp + size_t(i) * 32, si); if (std::memcmp(si, shares + size_t(i) * 32, 32)) FAILV(); }
                        for (uint32_t i = 1; i < K; ++i) if (!std::memcmp(mp + size_t(i) * 32, mp, 32)) FAILV();  // a later m_i is never the seed
                    }
                    ++n; continue;
                }
                if (ih_hash(light.get(), 1, hdr, nonce, m1, h1) != IGNPOW_OK) FAILV();
                if (ih_hash(full.get(), 1, hdr, nonce, m2, h2) != IGNPOW_OK) FAILV();
                if (std::memcmp(m1, m2, 32) || std::memcmp(h1, h2, 32)) {
                    if (std::getenv("RXE1_DEBUG")) {
                        std::fprintf(stderr, "light/full differ for %s\n", pj);
                        const auto& cl = *static_cast<const ih_ctx*>(light.get());
                        const auto& cf = *static_cast<const ih_ctx*>(full.get());
                        uint8_t buf[1024];
                        for (uint64_t k = 0; k < cf.n_items(); ++k) {
                            derive_item(cl, k, buf);
                            if (std::memcmp(buf, cf.dataset_v().data() + k * cf.item_bytes(), cf.item_bytes()) != 0) { std::fprintf(stderr, "first differing item %llu of %llu (item_bytes %u, dataset %zu bytes)\n", (unsigned long long)k, (unsigned long long)cf.n_items(), cf.item_bytes(), cf.dataset_v().size()); break; }
                        }
                    }
                    FAILV();
                }
                ++n;
            }
        }
    }
    {
        // F-441 (KEYED-RELEASED-VERSIONS-SPEC.md sections 2, 3 and 5.1): the bond-keyed form on the released walks, a version 18 raster
        // document (the zero target, no dataset: the chain's form) and a version 21 Forge2 document in the chain form (a light context),
        // each beside its unkeyed twin. The keyless entry refuses the keyed document; the prover's hash under a secret equals the
        // verifier's replay from the shares; the messages m_0 = S, s_i = H(sk, i, m_i), a later m_i never the seed; a wrong secret or a
        // flipped share changes the digest; the twin hashes keyless and its P differs from the keyed document's while Pd is one for
        // the pair (one dataset); the keyed count rides byte 64 (18) or byte 88 (21, the record 92 bytes) and every other byte of the
        // keyed record equals the twin's.
        struct row { const char* keyed; const char* twin; uint32_t S; size_t slot; size_t len_keyed; size_t len_twin; };
        const row rows[] = {
            { "{\"version\": 18, \"rounds\": 64, \"epoch_blocks\": 4096, \"period_blocks\": 300, \"chain_id\": 7358, \"program_anchor\": \"0000000000000000000000000000000000000000000000000000000000000000\", \"features\": 1024, \"keyed_rounds\": 5}",
              "{\"version\": 18, \"rounds\": 64, \"epoch_blocks\": 4096, \"period_blocks\": 300, \"chain_id\": 7358, \"program_anchor\": \"0000000000000000000000000000000000000000000000000000000000000000\"}", 5, 64, 80, 80 },
            { "{\"version\": 21, \"rounds\": 64, \"program_ops\": 16, \"sequence\": 4, \"period_blocks\": 64, \"program_anchor\": \"0000000000000000000000000000000000000000000000000000000000000000\", \"lanes\": 16, \"registers\": 32, \"scratch_kib\": 1, \"dataset_mib\": 1024, \"cache_mib\": 1, \"parents\": 1, \"item_bytes\": 256, \"fold_every\": 2, \"epoch_blocks\": 4096, \"chain_id\": 7358, \"fp_mode\": 6, \"pair_log2\": 1, \"lean\": 1, \"prefetch\": 16, \"features\": 1024, \"keyed_rounds\": 3}",
              "{\"version\": 21, \"rounds\": 64, \"program_ops\": 16, \"sequence\": 4, \"period_blocks\": 64, \"program_anchor\": \"0000000000000000000000000000000000000000000000000000000000000000\", \"lanes\": 16, \"registers\": 32, \"scratch_kib\": 1, \"dataset_mib\": 1024, \"cache_mib\": 1, \"parents\": 1, \"item_bytes\": 256, \"fold_every\": 2, \"epoch_blocks\": 4096, \"chain_id\": 7358, \"fp_mode\": 6, \"pair_log2\": 1, \"lean\": 1, \"prefetch\": 16}", 3, 88, 92, 88 },
        };
        for (const row& rw : rows) {
            ignh_params kp, tp;
            if (ignh_params_parse(rw.keyed, &kp) != IGNPOW_OK || ignh_params_parse(rw.twin, &tp) != IGNPOW_OK) FAILV();
            {
                // F-580: the node's twin carries "features": 0 in place of the absent field; it parses to the same record
                std::string tw0(rw.twin); tw0.insert(tw0.size() - 1, ", \"features\": 0");
                ignh_params t0; if (ignh_params_parse(tw0.c_str(), &t0) != IGNPOW_OK) FAILV();
                uint8_t b0[IGNH_PARAMS_BYTES] = {0}, b1[IGNH_PARAMS_BYTES] = {0}; params_bytes(t0, b0); params_bytes(tp, b1);
                if (params_len(t0) != params_len(tp) || std::memcmp(b0, b1, params_len(tp))) FAILV();
                std::string tw2(rw.twin); tw2.insert(tw2.size() - 1, ", \"features\": 2");  // any other value without the bit: refused
                if (ignh_params_parse(tw2.c_str(), &t0) == IGNPOW_OK) FAILV();
            }
            if (ignh_keyed_count(&kp) != rw.S || ignh_keyed_count(&tp) != 0 || !(kp.features & 1024) || tp.features) FAILV();
            if (params_len(kp) != rw.len_keyed || params_len(tp) != rw.len_twin) FAILV();
            uint8_t kb[IGNH_PARAMS_BYTES] = {0}, tb[IGNH_PARAMS_BYTES] = {0};
            params_bytes(kp, kb); params_bytes(tp, tb);
            // the keyed record: bit 10 at byte 60, the count at its slot, every other byte the twin's
            for (size_t i = 0; i < rw.len_keyed; ++i) {
                if (i >= 60 && i < 64) continue;  // the features word, checked below
                if (i >= rw.slot && i < rw.slot + 4) continue;
                if (i < rw.len_twin && kb[i] != tb[i]) FAILV();
            }
            if (kb[62] != 4 || kb[63] != 0 || kb[60] != 0 || kb[61] != 0) FAILV();  // be32 1024 at byte 60
            if (kb[rw.slot] != 0 || kb[rw.slot + 1] != 0 || kb[rw.slot + 2] != 0 || kb[rw.slot + 3] != uint8_t(rw.S)) FAILV();
            uint8_t pdk[32], pdt[32]; dataset_params_hash(kp, pdk); dataset_params_hash(tp, pdt);
            if (std::memcmp(pdk, pdt, 32)) FAILV();  // one dataset for the pair
            uint8_t pk[32], pt[32]; b3_hash(kb, params_len(kp), pk, 32); b3_hash(tb, params_len(tp), pt, 32);
            if (!std::memcmp(pk, pt, 32)) FAILV();  // the keyed document's P is its own
            int st = 0;
            std::unique_ptr<ctx_base> kc(ih_create(rw.keyed, 1, 0, &st)); if (!kc || st != IGNPOW_OK) FAILV();
            std::unique_ptr<ctx_base> tc(ih_create(rw.twin, 1, 0, &st)); if (!tc || st != IGNPOW_OK) FAILV();
            uint8_t hdr[32]; for (int i = 0; i < 32; ++i) hdr[i] = uint8_t(i * 13 + 5);
            for (uint64_t nonce = 0; nonce < 2; ++nonce) {
                uint8_t sk[32]; for (int i = 0; i < 32; ++i) sk[i] = uint8_t(i * 29 + 11 + nonce);
                uint8_t m1[32], h1[32], m2[32], h2[32], m3[32], h3[32];
                uint8_t shares[64 * 32], sh2[64 * 32], mp[64 * 32], mv[64 * 32], S0[32], si[32];
                if (ih_hash(kc.get(), 1, hdr, nonce, m1, h1) != IGNPOW_E_ARG) FAILV();  // no keyless work on a keyed document
                if (ih_hash(tc.get(), 1, hdr, nonce, m1, h1) != IGNPOW_OK) FAILV();      // the twin hashes keyless
                if (ih_hash_keyed(kc.get(), 1, hdr, nonce, sk, shares, m1, h1) != IGNPOW_OK) FAILV();
                if (ih_hash_keyed(kc.get(), 1, hdr, nonce, nullptr, shares, m2, h2) != IGNPOW_OK) FAILV();
                if (std::memcmp(m1, m2, 32) || std::memcmp(h1, h2, 32)) FAILV();  // the replay equals the prover's
                if (ih_hash_keyed_m(kc.get(), 1, hdr, nonce, sk, sh2, m3, h3, mp) != IGNPOW_OK) FAILV();
                if (std::memcmp(m3, m1, 32) || std::memcmp(h3, h1, 32) || std::memcmp(sh2, shares, size_t(rw.S) * 32)) FAILV();
                if (ih_hash_keyed_m(kc.get(), 1, hdr, nonce, nullptr, shares, m3, h3, mv) != IGNPOW_OK) FAILV();
                if (std::memcmp(h3, h1, 32) || std::memcmp(mp, mv, size_t(rw.S) * 32)) FAILV();
                work_seed(*static_cast<const ih_ctx*>(kc.get()), hdr, nonce, S0);
                if (std::memcmp(mp, S0, 32)) FAILV();  // m_0 is the work seed
                for (uint32_t i = 0; i < rw.S; ++i) { ignh_bond_share(sk, i, mp + size_t(i) * 32, si); if (std::memcmp(si, shares + size_t(i) * 32, 32)) FAILV(); }
                for (uint32_t i = 1; i < rw.S; ++i) if (!std::memcmp(mp + size_t(i) * 32, mp, 32)) FAILV();
                sk[0] ^= 1;
                if (ih_hash_keyed(kc.get(), 1, hdr, nonce, sk, sh2, m3, h3) != IGNPOW_OK) FAILV();
                if (!std::memcmp(h1, h3, 32) || !std::memcmp(shares, sh2, 32)) FAILV();  // a wrong secret
                // a flipped share: on the raster row the first share's first byte (the first keyed triangle's first pixel: a box under
                // eight pixels takes the share's first words only, section 3.4, so a later word may enter no pixel by the rule's own
                // words); on the Forge2 row the second share's ninth byte (under F-504's ruled form every share word binds the digest
                // through the mix's pre-image, the first through the item index as well)
                std::memcpy(sh2, shares, size_t(rw.S) * 32); sh2[kp.version == 18 ? 0 : 40] ^= 1;
                if (ih_hash_keyed(kc.get(), 1, hdr, nonce, nullptr, sh2, m3, h3) != IGNPOW_OK) FAILV();
                if (!std::memcmp(h1, h3, 32)) FAILV();  // a flipped share
                ++n;
            }
        }
    }
    {
        // The frozen parameter document (SPEC-R0A-v0.md section 0.1): the baseline's 36 bytes and P.
        ignh_params p;
        if (parse_params("{\"version\": 0}", p) != IGNPOW_OK) FAILV();
        uint8_t pb[IGNH_PARAMS_BYTES], P[32];
        params_bytes(p, pb);
        if (params_len(p) != 36 || !hex_eq(pb, 36, "000000000000000010000000000040000000004000000008000000000000100000000001")) FAILV();
        b3_hash(pb, 36, P, 32);
        if (!hex_eq(P, 32, "e6a228c9f49643c0d1edfc1be79baa11aebc9c602143a8bae28332f0f9ee06f6")) FAILV();
        ++n;
        if (parse_params("{}", p) != IGNPOW_E_PARAMS || parse_params(nullptr, p) != IGNPOW_E_PARAMS) FAILV();  // version mandatory
        if (parse_params("{\"version\": 1}", p) != IGNPOW_OK) FAILV();
        params_bytes(p, pb);
        if (params_len(p) != 48 || !hex_eq(pb, 48, "000000010000000040000000000000000000004000000008000000000000100000000001000000000200000000000100")) FAILV();
        b3_hash(pb, 48, P, 32);
        if (!hex_eq(P, 32, "de6795efbe4d1992643788e9c7a8d7e7228f28cb0302459ad2dfcffeea0f3c9b")) FAILV();
        ++n;
        if (parse_params("{\"version\": 2}", p) != IGNPOW_OK) FAILV();
        params_bytes(p, pb);
        if (params_len(p) != 52 || !hex_eq(pb, 52, "00000002000000004000000000000000000000400000000800000000000010000000000100000000020000000000010000000008")) FAILV();
        b3_hash(pb, 52, P, 32);
        if (!hex_eq(P, 32, "3693679bf874744ee3716be197dba4a69941a734322c64f2d6178c167c2b5f21")) FAILV();
        ++n;
        // The baseline known-answer vector (pow/vectors/igneumhash/d256-s16-r64-c1/unseen.json, the first vector),
        // through the light path.
        int st = 0;
        std::unique_ptr<ctx_base> light(ih_create("{\"version\": 0}", 1, 0, &st));
        if (!light || st != IGNPOW_OK) FAILV();
        static const char* hdr_hex = "6468beee66bb5264f1191b196797b748220bf33cf2072630a7f1c030c520c307";
        uint8_t hdr[32];
        for (int i = 0; i < 32; ++i) {
            auto v = [](char c) { return c <= '9' ? c - '0' : c - 'a' + 10; };
            hdr[i] = uint8_t(v(hdr_hex[2 * i]) * 16 + v(hdr_hex[2 * i + 1]));
        }
        uint8_t m[32], h[32];
        if (ih_hash(light.get(), 1, hdr, 4312426439069536300ull, m, h) != IGNPOW_OK) FAILV();
        if (!hex_eq(m, 32, "eec644f5195d87ebd8a9326580303ef03856112a6d8d751939184e89c47dc086")) FAILV();
        if (!hex_eq(h, 32, "ce6898a3ea75f0061dcdd5f6058643135ec856f79f65b6b92f7046dc390e9901")) FAILV();
        ++n;
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace

ctx_base* ih_create_shared(const char* json, uint64_t height, const ctx_base* donor, int* status) { return ih_create_shared_impl(json, height, donor, status); }
// for play.cpp (Proof of Play over IgneumHash's dataset): the context, one dataset word, the light cache
ctx_base* ih_create_public(const char* json, uint64_t height, int full, int* status) { return ih_create(json, height, full, status); }
int ih_dataset_word(const ctx_base* ctx, uint64_t word, uint32_t* out) {
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    const uint64_t wpi = c.item_bytes() / 4, items = c.p.dataset_bytes / c.item_bytes();
    if (!out || word / wpi >= items) return IGNPOW_E_ARG;
    uint32_t W[256]; load_item(c, word / wpi, W); *out = W[word % wpi];
    return IGNPOW_OK;
}
int ih_dataset_words(const ctx_base* ctx, uint64_t word, uint32_t n, uint32_t* out) {
    // n contiguous words from `word` (a chunk of 64 or 256 bytes; the play coordinate's chunk walk), within one item or across two
    const auto& c = *static_cast<const ih_ctx*>(ctx);
    const uint64_t wpi = c.item_bytes() / 4, items = c.p.dataset_bytes / c.item_bytes();
    if (!out || n == 0 || n > 256 || (word + n - 1) / wpi >= items) return IGNPOW_E_ARG;
    uint32_t W[256]; uint64_t cur = UINT64_MAX;
    for (uint32_t k = 0; k < n; ++k) { const uint64_t it = (word + k) / wpi; if (it != cur) { load_item(c, it, W); cur = it; } out[k] = W[(word + k) % wpi]; }
    return IGNPOW_OK;
}
int ih_light_cache_public(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes) { return ih_light_cache(ctx, data, bytes, item_bytes); }

const candidate igneumhash_r0a = {
    IGNPOW_ALGO_IGNEUMHASH,
    ih_check_params,
    ih_epoch_of,
    ih_cache_bytes,
    ih_dataset_bytes,
    ih_create,
    ih_hash,
    ih_verify,
    ih_light_cache,
    ih_vectors_check,
    "IgneumHash-R0A versions 0, 1, 3 (item width), 4 (Strata, the whole-item rule), 5 (the multiply-high map, proposed), 6 (the keyed reduction), 7 (v6b, the lane-local keyed reduction, the recorded alternate) and 8 (the blended relation, the RXE1 block schedule in the round; FP bits 4 and 5, the measurement modes 8 and 9) (SPEC-R0A-v0.md sections 0.1 to 0.8) and the R0B schedule version 2 (SPEC-R0B-schedule-v0.md), BLAKE3 1.8.2 portable",
};

}  // namespace ignpow

// ---- the candidate-specific C API (igneumhash.h) ----

extern "C" {

int ignh_params_parse(const char* params_json, ignh_params* out) {
    if (!out) return IGNPOW_E_ARG;
    return ignpow::parse_params(params_json, *out);
}

size_t ignh_params_len(const ignh_params* p) { return p ? ignpow::params_len(*p) : 0; }
uint32_t ignh_keyed_count(const ignh_params* p) { return (p && (p->features & 1024)) ? p->bond_rounds : 0u; }

void ignh_params_bytes(const ignh_params* p, uint8_t out[IGNH_PARAMS_BYTES]) {
    if (p && out) ignpow::params_bytes(*p, out);
}

void ignh_params_hash(const ignh_params* p, uint8_t out[32]) {
    if (!p || !out) return;
    uint8_t pb[IGNH_PARAMS_BYTES];
    ignpow::params_bytes(*p, pb);
    ignpow::b3_hash(pb, ignpow::params_len(*p), out, 32);
}
void ignh_dataset_params_hash(const ignh_params* p, uint8_t out[32]) {
    if (!p || !out) return;
    ignpow::dataset_params_hash(*p, out);
}

uint32_t ignh_v8_fop(uint32_t x, uint32_t y, uint32_t z) { return ignpow::v8fop(x, y, z); }
uint32_t ignh_v8_fop_wide(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z) { return ignpow::v8fop_wide(opcode, x, y, z); }
void ignh_bond_pk(const uint8_t sk[32], uint8_t pk_out[32]) {
    static const char kTag[] = "IgneumHash/R0A/bond-pk";  // 22 ASCII bytes under a one-byte length (section 0.9.1)
    const uint8_t len = uint8_t(sizeof kTag - 1);
    blake3_hasher hh; blake3_hasher_init(&hh); blake3_hasher_update(&hh, &len, 1); blake3_hasher_update(&hh, kTag, len); blake3_hasher_update(&hh, sk, 32); blake3_hasher_finalize(&hh, pk_out, 32);
}
void ignh_bond_share(const uint8_t sk[32], uint32_t i, const uint8_t m[32], uint8_t share_out[32]) {
    uint8_t msg[4 + 4 + 32]; std::memcpy(msg, "bond", 4);
    msg[4] = uint8_t(i); msg[5] = uint8_t(i >> 8); msg[6] = uint8_t(i >> 16); msg[7] = uint8_t(i >> 24);  // LE32(i)
    std::memcpy(msg + 8, m, 32);
    blake3_hasher hh; blake3_hasher_init_keyed(&hh, sk); blake3_hasher_update(&hh, msg, sizeof msg); blake3_hasher_finalize(&hh, share_out, 32);
}
// the texture (17) and raster (18) period keys and the two-level form's period seed (the workers' exports, the reference's own draws
// byte for byte; the GPU team's branch carried them, landed on main with the version test the GPU team named: versions 21, 24 and 25 draw the
// period seed as forge2's, the batch form and the chain form alike; the project's order 15 October 2026 13:4x UK)
int ignh_r17_key(const ignh_params* p, uint64_t height, uint8_t key_out[32]) {
    if (!p || !key_out || p->version != 17 || p->period_blocks == 0) return IGNPOW_E_ARG;
    ignpow::bytes in; in.tag(ignpow::r17::kTagKey).le64(p->chain_id).le64(height / p->period_blocks).raw(p->program_anchor, 32);
    ignpow::b3_hash(in, key_out, 32);
    return IGNPOW_OK;
}
int ignh_r18_key(const ignh_params* p, uint64_t height, uint8_t key_out[32]) {
    if (!p || !key_out || p->version != 18 || p->period_blocks == 0) return IGNPOW_E_ARG;
    ignpow::bytes in; in.tag(ignpow::r18::kTagKey).le64(p->chain_id).le64(height / p->period_blocks).raw(p->program_anchor, 32);
    ignpow::b3_hash(in, key_out, 32);
    return IGNPOW_OK;
}
static bool ignh_forge2_family(uint32_t v) { return v == 21 || v == 24 || v == 25; }  // the one-line version test: the forms on forge2's engine
int ignh_forge2_period_seed(const ignh_params* p, const uint8_t E[32], const uint8_t header[32], uint64_t batch, uint8_t seed_out[32]) {
    if (!p || !E || !header || !seed_out || !ignh_forge2_family(p->version) || p->period_blocks != 0) return IGNPOW_E_ARG;  // the batch form only
    ignpow::bytes in; in.tag(ignpow::forge2::kTagPeriod).raw(E, 32).raw(header, 32).le64(batch);
    ignpow::b3_hash(in, seed_out, 32);
    return IGNPOW_OK;
}
int ignh_forge2_period_seed_chain(const ignh_params* p, const uint8_t E[32], uint64_t height, const uint8_t anchor[32], uint8_t seed_out[32]) {
    if (!p || !E || !seed_out || !ignh_forge2_family(p->version) || p->period_blocks == 0) return IGNPOW_E_ARG;  // the chain form only
    ignpow::bytes in; in.tag(ignpow::forge2::kTagPeriod).raw(E, 32).le64(p->chain_id).le64(height / p->period_blocks).raw(anchor ? anchor : p->program_anchor, 32);
    ignpow::b3_hash(in, seed_out, 32);
    return IGNPOW_OK;
}
void ignh_test_sk(const uint8_t header[32], uint8_t sk_out[32]) {
    blake3_hasher hh; blake3_hasher_init(&hh); blake3_hasher_update(&hh, "IgneumHash/R0A/test-sk", 22); blake3_hasher_update(&hh, header, 32); blake3_hasher_finalize(&hh, sk_out, 32);
}
int ignh_hash_keyed(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32]) {
    if (!ctx || !header || !shares || !mix_out || !hash_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_hash_keyed(inner, height, header, nonce, sk, shares, mix_out, hash_out);
}
// the differential hook (the vectors tool only): kind 0 clears
void ignh_debug_flip(int kind, uint32_t a, uint32_t b, uint32_t c) { ignpow::g_r0e_flip.kind = kind; ignpow::g_r0e_flip.a = a; ignpow::g_r0e_flip.b = b; ignpow::g_r0e_flip.c = c; }
// the anchored forms (the protocol team's two-branch verifier): the period's programme anchor given per call, the drawn
// programme cached by (period, anchor) in four slots; a version under 9 ignores the anchor
int ignh_kiln_hash_path(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32], int path) {
    if (!ctx || !header || !mix_out || !hash_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_kiln_hash_path(inner, height, header, nonce, mix_out, hash_out, path);
}
void ignh_kiln_count_rows(int on) { ignpow::kiln::count_rows_flag() = on != 0; }
uint64_t ignh_kiln_rows_read(void) { return ignpow::kiln::rows_read_last(); }
int ignh_hash_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t anchor[32], uint8_t mix_out[32], uint8_t hash_out[32]) {
    if (!ctx || !header || !anchor || !mix_out || !hash_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_hash_a(inner, height, header, nonce, mix_out, hash_out, anchor);
}
int ignh_hash_keyed_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, const uint8_t anchor[32], uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32]) {
    if (!ctx || !header || !anchor || !shares || !mix_out || !hash_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_hash_keyed(inner, height, header, nonce, sk, shares, mix_out, hash_out, anchor);
}
int ignh_hash_keyed_m_anchored(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, const uint8_t anchor[32], uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32], uint8_t *m_out) {
    if (!ctx || !header || !anchor || !shares || !mix_out || !hash_out || !m_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_hash_keyed_m(inner, height, header, nonce, sk, shares, mix_out, hash_out, m_out, anchor);
}
int ignh_hash_keyed_m(const ignpow_ctx *ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t *sk, uint8_t *shares, uint8_t mix_out[32], uint8_t hash_out[32], uint8_t *m_out) {
    if (!ctx || !header || !shares || !mix_out || !hash_out || !m_out) return IGNPOW_E_ARG;
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!inner) return IGNPOW_E_ARG;
    return ignpow::ih_hash_keyed_m(inner, height, header, nonce, sk, shares, mix_out, hash_out, m_out);
}
uint32_t ignh_v8_fop_wide_contracted(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z) {
    const float xf = ignpow::v8mask(x), yf = ignpow::v8mask(y), zf = ignpow::v8mask(z);
    float f;
    switch (opcode) { case 0: f = xf + (yf - zf); break; case 1: f = xf - (yf + zf); break; case 2: f = std::fma(yf, zf, xf); break; default: f = (xf - yf) + zf; break; }
    uint32_t fb; std::memcpy(&fb, &f, 4);
    return fb ^ x;
}
uint32_t ignh_v8_fop_contracted(uint32_t x, uint32_t y, uint32_t z) {
    const uint32_t yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u;
    float yf, zf; std::memcpy(&yf, &yb, 4); std::memcpy(&zf, &zb, 4);
    const uint32_t xb = (x & 0x3FFFFFFFu) | 0x20000000u; float xf; std::memcpy(&xf, &xb, 4);
    const float sum = std::fma(yf, zf, xf);
    uint32_t fb; std::memcpy(&fb, &sum, 4);
    return fb ^ x;
}

void ignh_variant_tag(const ignh_params* p, char* buf, size_t n) {
    if (!p || !buf || n == 0) return;
    if (p->version == 8)
        std::snprintf(buf, n, p->features ? "v8-d%llu-s%u-r%u-k%llu-p%u-w%u-b%u-x%u-f%u" : "v8-d%llu-s%u-r%u-k%llu-p%u-w%u-b%u-x%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes, p->blocks, p->registers, p->features);
    else if (p->version == 7)
        std::snprintf(buf, n, "v7-d%llu-s%u-r%u-k%llu-p%u-w%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes);
    else if (p->version == 6)
        std::snprintf(buf, n, "v6-d%llu-s%u-r%u-k%llu-p%u-w%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes);
    else if (p->version == 5)
        std::snprintf(buf, n, "v5-d%llu-s%u-r%u-k%llu-p%u-w%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes);
    else if (p->version == 4)
        std::snprintf(buf, n, "v4-d%llu-s%u-r%u-k%llu-p%u-w%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes);
    else if (p->version == 3)
        std::snprintf(buf, n, "v3-d%llu-s%u-r%u-k%llu-p%u-w%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->item_bytes);
    else if (p->version == 2)
        std::snprintf(buf, n, "v2-d%llu-s%u-r%u-k%llu-p%u-q%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents, p->schedule);
    else if (p->version == 1)
        std::snprintf(buf, n, "v1-d%llu-s%u-r%u-k%llu-p%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, (unsigned long long)(p->cache_bytes >> 20), p->parents);
    else
        std::snprintf(buf, n, "d%llu-s%u-r%u-c%u", (unsigned long long)(p->dataset_bytes >> 20), p->scratch_bytes >> 10, p->rounds, p->item_chain);
}

}  // extern "C"

// The two functions below need the wrapped ignpow_ctx: declared in ignpow.cpp.
namespace ignpow { const ctx_base* inner_of(const ignpow_ctx* ctx, const char* algo); }
// the Graph lane's 28b form (cuckaroo28k.cpp) keys its edge function over an IgneumHash epoch dataset: a context created from a
// version 17b document, its resident dataset (or the light derivation of a word) and its release
extern "C" void* ignh_inner_create(const char* json, uint64_t height, int full, int* status) { return ignpow::ih_create(json, height, full, status); }
extern "C" const uint8_t* ignh_inner_dataset(const void* ctx, uint64_t* bytes) { const auto& c = *static_cast<const ignpow::ih_ctx*>(ctx); if (bytes) *bytes = c.p.dataset_bytes; return c.dataset_v().empty() ? nullptr : c.dataset_v().data(); }
extern "C" uint32_t ignh_inner_word(const void* ctx, uint64_t word_index) { const auto& c = *static_cast<const ignpow::ih_ctx*>(ctx); uint32_t W[256]; ignpow::load_item(c, word_index >> 6, W); return W[word_index & 63]; }
extern "C" void ignh_inner_destroy(void* ctx) { delete static_cast<ignpow::ctx_base*>(ctx); }

extern "C" {

int ignh_dataset_info(const ignpow_ctx* ctx, uint8_t E[32], uint8_t D[32], uint8_t cache_digest[32], uint8_t digest[32], uint64_t* build_ms, int* has_digest) {
    const auto* base = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!base) return IGNPOW_E_ARG;
    const auto& c = *static_cast<const ignpow::ih_ctx*>(base);
    if (E) std::memcpy(E, c.E, 32);
    if (D) std::memcpy(D, c.D, 32);
    if (cache_digest) { std::memset(cache_digest, 0, 32); if (!c.cache_v().empty()) ignpow::b3_hash(c.cache_v().data(), c.cache_v().size(), cache_digest, 32); }
    if (digest) std::memset(digest, 0, 32);
    if (build_ms) *build_ms = c.build_ms;
    if (has_digest) *has_digest = c.dataset_v().empty() ? 0 : 1;
    if (!c.dataset_v().empty() && digest) ignpow::b3_hash(c.dataset_v().data(), c.dataset_v().size(), digest, 32);
    return IGNPOW_OK;
}

// version 9: the period's programme as a worker compiles it (the header's record form); a stack context carrying the document alone
int ignh_r0d_program(const ignh_params* p, uint64_t height, uint8_t* out, size_t max_ops) {
    if (!p || !out || (p->version < 9 || p->version > 14) || p->period_blocks == 0) return IGNPOW_E_ARG;   // versions 9 to 14: the same draw with each version's tag, window and composition (the protocol team's reading of 22:4x: the guard had stopped at 12)
    if (p->program_ops > max_ops) return IGNPOW_E_BOUNDARY;
    ignpow::ih_ctx c; c.p = *p;
    std::vector<ignpow::ih_ctx::r0d_op> ops;
    ignpow::r0d_generate(c, height / p->period_blocks, ops);
    for (size_t i = 0; i < ops.size(); ++i) {
        out[8 * i] = ops[i].opcode; out[8 * i + 1] = ops[i].dst; out[8 * i + 2] = ops[i].src1; out[8 * i + 3] = ops[i].src2;
        out[8 * i + 4] = uint8_t(ops[i].imm); out[8 * i + 5] = uint8_t(ops[i].imm >> 8); out[8 * i + 6] = uint8_t(ops[i].imm >> 16); out[8 * i + 7] = uint8_t(ops[i].imm >> 24);
    }
    return int(ops.size());
}

// versions 15 and 16: the period's key K and its programme as a worker compiles them (a GPU port's host side, the GPU team 13 Oct 2026):
// K over the document's program_anchor (the sets' zero anchor; the chain form passes its own by the anchored calls), the records
// (opcode, d, a, b, imm LE32) as r16::draw_program (version 15, program_ops records) or r16f::draw_program (version 16, rounds x 224
// records over the 224 static registers) draws them
int ignh_r16_period(const ignh_params* p, uint64_t height, uint8_t key_out[32], uint8_t* out, size_t max_ops) { return ignh_r16_period_c(p, height, key_out, out, max_ops, nullptr); }
// the same with the third register of each record (revision 13 / the revision 14 variants) in c_out[i] when given (the GPU team, 23:3x UK 13 Oct)
int ignh_r16_period_c(const ignh_params* p, uint64_t height, uint8_t key_out[32], uint8_t* out, size_t max_ops, uint8_t* c_out) {
    if (!p || !key_out || !out || (p->version != 15 && p->version != 16) || p->period_blocks == 0) return IGNPOW_E_ARG;
    if (p->version == 16 && ((p->program_distance >> 18) & 3)) {
        // revisions 17 (the braided stream, bits 18/19 = 2, S by bits 20/21) and 18 (the segment pool, form 3: 64 x 100 blocks): the r16w table as the stream's records
        const uint32_t N = p->registers ? p->registers : ignpow::r16f::kRegs;
        ignpow::ih_ctx c; c.p = *p; const auto tab = ignpow::r16w::compiled(c, height, nullptr, key_out);
        const size_t n = tab->size() / 3; if (n > max_ops) return IGNPOW_E_BOUNDARY;
        for (size_t i = 0; i < n; ++i) {
            const uint32_t w = (*tab)[3 * i], imm = (*tab)[3 * i + 1];
            out[8 * i] = uint8_t((w & 255) == 255 ? 255 : 64 + (w & 255)); out[8 * i + 1] = uint8_t((w >> 8) & 255); out[8 * i + 2] = uint8_t((w >> 16) & 255); out[8 * i + 3] = uint8_t(w >> 24);
            out[8 * i + 4] = uint8_t(imm); out[8 * i + 5] = uint8_t(imm >> 8); out[8 * i + 6] = uint8_t(imm >> 16); out[8 * i + 7] = uint8_t(imm >> 24);
            if (c_out) c_out[i] = uint8_t((*tab)[3 * i + 2]);
        }
        (void)N; return int(n);
    }
    if (p->version == 16 && (p->program_distance & 32768)) {
        // revision 15 (the streaming fabric) and 16 (15b, the wide vocabulary): rounds x W records (W = registers / 4), the opcode 64 + the
        // r16s class (ADD 64 .. LEA 73), d the ring register, a and b the previous block's registers, c (LOP3, PRMT) in c_out
        const uint32_t N = p->registers ? p->registers : ignpow::r16f::kRegs, W = N / 4;
        const size_t n = size_t(p->rounds) * W; if (n > max_ops) return IGNPOW_E_BOUNDARY;
        ignpow::ih_ctx c; c.p = *p; const auto tab = ignpow::r16s::compiled(c, height, nullptr, key_out);
        for (size_t i = 0; i < n; ++i) {
            const uint32_t w = (*tab)[3 * i], imm = (*tab)[3 * i + 1];
            out[8 * i] = uint8_t(64 + (w & 255)); out[8 * i + 1] = uint8_t((w >> 8) & 255); out[8 * i + 2] = uint8_t((w >> 16) & 255); out[8 * i + 3] = uint8_t(w >> 24);
            out[8 * i + 4] = uint8_t(imm); out[8 * i + 5] = uint8_t(imm >> 8); out[8 * i + 6] = uint8_t(imm >> 16); out[8 * i + 7] = uint8_t(imm >> 24);
            if (c_out) c_out[i] = uint8_t((*tab)[3 * i + 2]);
        }
        return int(n);
    }
    const uint32_t v16_nregs = p->registers ? p->registers : ignpow::r16f::kRegs;   // the document's "registers" (144, 160; 0 = 224)
    const size_t n_ops = p->version == 16 ? size_t(p->rounds) * (v16_nregs + (((p->program_distance & 508) && !(p->program_distance & 3584)) ? 4 : 0)) : size_t(p->program_ops);   // bits 2 to 8 (revisions 4 to 10) add four cross-bank XORs a pass; bit 9 (revision 11) none
    if (n_ops > max_ops) return IGNPOW_E_BOUNDARY;
    ignpow::ih_ctx c; c.p = *p;
    if (p->version == 16) {
        ignpow::r16f::period_key(c, height, nullptr, key_out);
        const bool v16_rev4 = (p->program_distance & 4) != 0, v16_rev5 = (p->program_distance & 8) != 0;
        const bool v16_r6 = (p->program_distance & 16) != 0, v16_r7 = (p->program_distance & 32) != 0, v16_r8 = (p->program_distance & 64) != 0, v16_r10 = (p->program_distance & 256) != 0, v16_r9 = (p->program_distance & 128) != 0 || v16_r10, v16_r11 = (p->program_distance & 512) != 0, v16_r12 = (p->program_distance & 1024) != 0, v16_r13 = (p->program_distance & 2048) != 0;   // 12: bits 9+10, 13: bits 9+10+11   // revision 6 (fused), 7 (6b, odd passes), 8 (form A), 9 (the schedule fix, plain forms), 10 (revision 9 with b off a and d)
        const bool v16_r5draw = v16_rev5 || v16_r6 || v16_r7 || v16_r8;   // revisions 5 to 8 share revision 5's draw (the derangement, b off the destination)
        const bool v16_xors = (v16_rev4 || v16_r5draw || v16_r9) && !v16_r11;   // revision 11: no end-of-pass XORs
        const uint32_t v16_nb = v16_nregs / 4; const uint32_t per_pass = v16_nregs + (v16_xors ? 4 : 0);
        std::vector<ignpow::r16f::op_t> prog; ignpow::r16f::draw_program(key_out, p->rounds, prog, (p->program_distance >> 1) & 1, v16_rev4, v16_r5draw, v16_r9, v16_r10, v16_r11, v16_nb, v16_r13, (p->program_distance >> 12) & 3);   // bits 12/13 the revision 14 variant (732f5130); bits 10/11 revisions 12/13 (653bbe15: the sized file, pass 0's first four consume d; 13 the three-operand forms); bit 9 revision 11 (d051d879: one op in four reads b across banks, no XORs); bit 8 revision 10 (559b4b08); bit 1 the light table (1235f442), bit 2 revision 4 (7d382cf8), bit 3 revision 5 (1044247b), bits 4 to 6 revisions 6 to 8 (0bbe1d87, 8ee48541), bit 7 revision 9 (86a15709: the plain forms, opcodes 0 to 6)
        for (size_t i = 0; i < prog.size(); ++i) {
            uint8_t opc = (prog[i].opc == ignpow::r16::MAD && (v16_xors || v16_r11)) ? 6 : prog[i].opc;   // 6: the odd multiplier
            const bool fabric_op = (i % per_pass) < v16_nregs;                    // the cross-bank XORs stay plain in every revision
            if (v16_rev5 && fabric_op) opc += 8;                                             // 8..14: revision 5's bijective forms (d = d_old + rotl(g, (imm >> 5) & 31))
            if ((v16_r6 || (v16_r7 && ((i / per_pass) & 1))) && fabric_op) opc += 16;         // 16..22: revision 6's fused forms (revision 7: the odd passes only)
            if (v16_r8 && fabric_op) opc += 24;                                              // 24..30: revision 8's form A, d = d_old ^ g
            if (v16_r12 && i < 4) opc += 24;                                                 // revision 12: the programme's first four consume the destination
            uint32_t imm = prog[i].imm;
            if (v16_r13 && prog[i].opc != ignpow::r16::MAD) { opc = uint8_t((i < 4 ? 40 : 32) + prog[i].opc); imm = prog[i].c; }   // revision 13: 32..37 three-operand (c = R[imm]), 40..45 consuming d
            { const uint32_t variant = (p->program_distance >> 12) & 3, pos = uint32_t(i % per_pass) / 4;   // revision 14a (one in four) / 14b (every op): the class from two bits of R[c]; the odd MAD exempt
              if (((variant == 1 && (pos & 3) == 1) || variant == 2) && prog[i].opc != ignpow::r16::MAD) opc = uint8_t(i < 4 ? 56 : 48); }   // 48: d = g, 56: d ^= g; g by (R[c] & 3): a + b + imm, a ^ b ^ imm, rotl(a, imm) ^ b, rotr(a, imm) ^ b
            if (c_out) c_out[i] = prog[i].c;
            out[8 * i] = opc; out[8 * i + 1] = prog[i].d; out[8 * i + 2] = prog[i].a; out[8 * i + 3] = prog[i].b;
            out[8 * i + 4] = uint8_t(imm); out[8 * i + 5] = uint8_t(imm >> 8); out[8 * i + 6] = uint8_t(imm >> 16); out[8 * i + 7] = uint8_t(imm >> 24);
        }
        return int(prog.size());
    }
    ignpow::r16::period_key(c, height, nullptr, key_out);
    std::vector<ignpow::r16::op_t> prog; ignpow::r16::draw_program(key_out, p->program_ops, prog);
    for (size_t i = 0; i < prog.size(); ++i) {
        out[8 * i] = prog[i].opc; out[8 * i + 1] = prog[i].d; out[8 * i + 2] = prog[i].a; out[8 * i + 3] = prog[i].b;
        out[8 * i + 4] = uint8_t(prog[i].imm); out[8 * i + 5] = uint8_t(prog[i].imm >> 8); out[8 * i + 6] = uint8_t(prog[i].imm >> 16); out[8 * i + 7] = uint8_t(prog[i].imm >> 24);
    }
    return int(prog.size());
}

// version 12's shared-table seed as the CUDA worker fills the period table (the GPU team's export, kept across the project's merges)
int ignh_r12_shared_seed(const ignh_params* p, uint64_t height, uint8_t* out) {
    if (!p || !out || p->version < 12 || p->version > 14 || !p->shared_kib || p->period_blocks == 0) return IGNPOW_E_ARG;
    const uint64_t period = height / p->period_blocks;
    ignpow::bytes in; in.tag("IgneumHash/R0E/shared").le64(p->chain_id).le64(period).raw(p->program_anchor, 32); ignpow::b3_hash(in, out, 32);
    return IGNPOW_OK;
}

int ignh_trace(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* out) {
    const auto* base = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!base || !header || !out) return IGNPOW_E_ARG;
    const auto& c0 = *static_cast<const ignpow::ih_ctx*>(base);
    if (height > ignpow::kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c0.p.epoch_blocks != c0.ep) return IGNPOW_E_EPOCH;
    const uint32_t family = c0.p.version == ignpow::kRotationVersion ? ignpow::r15_family(c0.p, height, nullptr) : 0xFFFFFFFFu;
    const auto& c = c0.p.version == ignpow::kRotationVersion ? *c0.fam[family] : c0;  // version 15: the window's family writes the trace, named first

    static const char* d = "0123456789abcdef";
    auto hex = [&](const uint8_t* p, size_t n) { std::string s; for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; } return s; };
    uint8_t S[32], M[32], F[32];
    ignpow::work_seed(c, header, nonce, S);
    std::fprintf(out, "{");
    if (family != 0xFFFFFFFFu) std::fprintf(out, "\"family\": %u, \"family_version\": %u, ", family, c.p.version);  // version 15: the window's family
    std::fprintf(out, "\"S\": \"%s\", \"E\": \"%s\", \"P\": \"%s\", ", hex(S, 32).c_str(), hex(c.E, 32).c_str(), hex(c.P, 32).c_str());
    ignpow::tracer tr; tr.f = out;
    const auto prog_sp = (c.p.version >= 9) ? ignpow::r0d_program(c, height) : nullptr;
    const std::vector<ignpow::ih_ctx::r0d_op>* prog = prog_sp ? prog_sp.get() : nullptr;
    const auto sh_sp = (c.p.version >= 12 && c.p.shared_kib) ? ignpow::r12_shared_table(c, height) : nullptr;
    const std::vector<uint32_t>* sh = sh_sp ? sh_sp.get() : nullptr;
    if (prog) {
        // version 9: the period's programme as drawn (opcode, dst, src1, src2, imm), the security review's program record
        std::fprintf(out, "\"program_period\": %llu, \"program\": [", (unsigned long long)(height / c.p.period_blocks));
        for (size_t i = 0; i < prog->size(); ++i) { const auto& x = (*prog)[i]; std::fprintf(out, "%s[%u, %u, %u, %u, %u]", i ? ", " : "", x.opcode, x.dst, x.src1, x.src2, x.imm); }
        std::fprintf(out, "], ");
    }
    if ((c.p.version >= 8) && (c.p.features & 1024)) {
        // section 0.9: a keyed document's trace is the prover's under the test secret sk = BLAKE3("IgneumHash/R0A/test-sk" || header)
        // (a vector set's shares are public by construction; a real bond's secret never appears in a set)
        uint8_t sk[32], shares[64 * 32];
        ignh_test_sk(header, sk);
        std::fprintf(out, "\"test_sk\": \"%s\", ", hex(sk, 32).c_str());
        ignpow::work(c, S, M, &tr, sk, shares, nullptr, prog, sh);
    } else
    ignpow::work(c, S, M, &tr, nullptr, nullptr, nullptr, prog, sh);
    ignpow::final_digest(c, S, M, F);
    std::fprintf(out, ", \"M\": \"%s\", \"F\": \"%s\"}", hex(M, 32).c_str(), hex(F, 32).c_str());
    return IGNPOW_OK;
}

int ignh_access_trace(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* addr_out, FILE* lane_out) {
    const auto* base = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUMHASH);
    if (!base || !header) return IGNPOW_E_ARG;
    const auto& c = *static_cast<const ignpow::ih_ctx*>(base);
    if (height > ignpow::kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / c.p.epoch_blocks != c.ep) return IGNPOW_E_EPOCH;
    uint8_t S[32], M[32];
    ignpow::work_seed(c, header, nonce, S);
    ignpow::tracer tr; tr.addr = addr_out; tr.lanes = lane_out; tr.epoch = c.ep; tr.nonce = nonce;
    const auto prog2 = (c.p.version >= 9) ? ignpow::r0d_program(c, height) : nullptr;
    const auto sh2 = (c.p.version >= 12 && c.p.shared_kib) ? ignpow::r12_shared_table(c, height) : nullptr;
    ignpow::work(c, S, M, &tr, nullptr, nullptr, nullptr, prog2 ? prog2.get() : nullptr, sh2 ? sh2.get() : nullptr);
    return IGNPOW_OK;
}

}  // extern "C"
