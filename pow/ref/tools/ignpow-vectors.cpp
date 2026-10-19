// ignpow-vectors: generate, check and bench the vector corpus of a candidate.
//
//   ignpow-vectors [--params '<json>'] selftest
//   ignpow-vectors [--params '<json>'] gen <algo> <class> <out.json> [seed]
//   ignpow-vectors [--params '<json>'] addrtrace igneumhash <height> <count> <seed> <addr.jsonl> <lanes.jsonl>
//       the security review's access and lane-dependence traces over count random (header, nonce) at that height
//       classes: unseen, nonce-boundary, transitions, malformed, tails; for igneumhash also trace, dataset, keyed
//       (IGNPOW_KEYED_COUNT rows, with the messages m), and on a bit 10 document the refusal classes params-rule,
//       registers-rule and key-forms (11 Oct 2026, the outside review's closure reading: 100 or more vectors per class)
//   ignpow-vectors check <algo> <vectors.json>
//   ignpow-vectors bench <algo> <height> [n] [full]   light-verify cost at that height (full: the resident-dataset path)
//   ignpow-vectors bench-full <algo> <height> [n] light against full (resident dataset): the F-12 check
//   ignpow-vectors diff <algo> --in <jsonl> --out <jsonl>     the tests/pow-diff cpu backend
//   ignpow-vectors fixtures <algo> <tests/fixtures/pow/<algo>> [seed]   the pow-diff fixture tree
//
// JSON is written and read by hand (no dependency); the reader accepts only
// what the writer produces. Every file records the generator build string, the
// seed and the params document so it can be regenerated.
//
// --params '<json>' (or IGNPOW_PARAMS in the environment) is the candidate's
// params document, passed unchanged to every ABI call; absent means the
// candidate's defaults. Heights used by the classes are derived from the
// candidate's own epoch length (ignpow_epoch_of), so one tool serves every
// candidate.

#include "ignpow.h"
#include "igneumhash.h"
extern "C" {  // the Graph coordinate's exported calls (cuckaroo29.cpp)
void igneum_graph_key(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t key_out[32], uint64_t sip_keys_out[4]);
int igneum_graph_verify_ticket(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, const uint32_t edges[42], uint8_t ticket_hash_out[32]);
int igneum_graph_solve(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint32_t* tickets, size_t max_tickets, size_t* n_out);
void igneum_graph_ticket_bytes(const uint32_t edges[42], uint8_t out[168]);
void igneum_graph28_key(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t key_out[32], uint64_t sip_keys_out[4]);
int igneum_graph28_verify_ticket(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, const uint32_t edges[42], uint8_t ticket_hash_out[32]);
int igneum_graph28_solve(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint32_t* tickets, size_t max_tickets, size_t* n_out);
void igneum_graph28k_key(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t key_out[32], uint64_t sip_keys_out[4]);
int igneum_graph28k_verify_ticket(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, const uint32_t edges[42], uint8_t ticket_hash_out[32]);
int igneum_graph28k_solve(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint32_t* tickets, size_t max_tickets, size_t* n_out);
// the graph lane under test (29 bits, or the 4 GB form at 28): the tickets class binds these at its start
static void (*g_graph_key)(uint64_t, uint64_t, const uint8_t*, uint64_t, uint8_t*, uint64_t*) = igneum_graph_key;
static int (*g_graph_verify)(uint64_t, uint64_t, const uint8_t*, uint64_t, const uint32_t*, uint8_t*) = igneum_graph_verify_ticket;
static int (*g_graph_solve)(uint64_t, uint64_t, const uint8_t*, uint64_t, uint32_t*, size_t, size_t*) = igneum_graph_solve;
static uint32_t g_graph_edges = 1u << 29;
int igneum_play_trace(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* out);  // play.cpp
int igneum_play_counts(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* out);  // play.cpp: the counts stream per attempt
int igneum_play_profile(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, uint32_t n, FILE* out);  // play.cpp: the CPU profile per stage
}

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <iostream>
#include <sstream>
#include <string>
#include <thread>
#include <vector>
#include <algorithm>

namespace {

const char* g_params = nullptr;  // the params document, or NULL for defaults

struct rng64 {  // xorshift64*; the seed is recorded in the file
    uint64_t s;
    explicit rng64(uint64_t seed) : s(seed ? seed : 0x9E3779B97F4A7C15ull) {}
    uint64_t next() {
        s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
        return s * 0x2545F4914F6CDD1Dull;
    }
    void bytes(uint8_t* p, size_t n) {
        for (size_t i = 0; i < n; ++i) p[i] = static_cast<uint8_t>(next() >> 56);
    }
};

std::string hex(const uint8_t* p, size_t n) {
    static const char* d = "0123456789abcdef";
    std::string s;
    s.reserve(2 * n);
    for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; }
    return s;
}

bool unhex(const std::string& s, uint8_t* out, size_t n) {
    if (s.size() != 2 * n) return false;
    auto v = [](char c) -> int {
        if (c >= '0' && c <= '9') return c - '0';
        if (c >= 'a' && c <= 'f') return c - 'a' + 10;
        return -1;
    };
    for (size_t i = 0; i < n; ++i) {
        int a = v(s[2 * i]), b = v(s[2 * i + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = static_cast<uint8_t>(a * 16 + b);
    }
    return true;
}

const char* reason_name(int st) {
    switch (st) {
        case IGNPOW_OK: return "ok";
        case IGNPOW_E_ALGO: return "algo";
        case IGNPOW_E_PARAMS: return "params";
        case IGNPOW_E_HEIGHT: return "height";
        case IGNPOW_E_EPOCH: return "epoch";
        case IGNPOW_E_BOUNDARY: return "boundary";
        case IGNPOW_E_MIX: return "mix";
        case IGNPOW_E_ARG: return "arg";
        case IGNPOW_E_MEMORY: return "memory";
        case IGNPOW_E_VECTORS: return "vectors";
    }
    return "unknown";
}

int reason_code(const std::string& s) {
    const char* names[] = {"ok", "algo", "params", "height", "epoch", "boundary", "mix", "arg", "memory", "vectors"};
    for (int i = 0; i < 10; ++i) if (s == names[i]) return i;
    return -1;
}

struct vec {  // one vector of any class; unused fields stay empty
    uint64_t height = 0;
    std::string header, mix, hash, boundary, expect;
    uint64_t nonce = 0;
    uint64_t nonce_count = 0;       // tails
    std::string share_target;       // tails
    uint64_t min_nonce = 0;         // tails
    std::string min_hash;           // tails
    uint64_t shares = 0;            // tails
    uint64_t ctx_height = 0;        // malformed: the height the context was created for
};

// The params document as recorded in a vector file: for igneumhash the complete canonical document (every member,
// so that no reader applies a default of its own, SPEC-R0A-v0.md section 0.1); for other candidates the string given.
std::string complete_params(const std::string& algo) {
    if (algo != IGNPOW_ALGO_IGNEUMHASH) return g_params ? g_params : "null";
    ignh_params p;
    if (ignh_params_parse(g_params, &p) != IGNPOW_OK) return g_params ? g_params : "null";
    std::string s = "{\"version\": " + std::to_string(p.version) + ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) +
        ", \"scratch_kib\": " + std::to_string(p.scratch_bytes >> 10) + ", \"rounds\": " + std::to_string(p.rounds) +
        ", \"lanes\": " + std::to_string(p.lanes) + ", \"epoch_blocks\": " + std::to_string(p.epoch_blocks) +
        ", \"item_chain\": " + std::to_string(p.item_chain) + ", \"chain_id\": " + std::to_string(p.chain_id);
    if (p.version >= 1) s += ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents);
    if (p.version == 2) s += ", \"schedule\": " + std::to_string(p.schedule);
    if (p.version >= 3) s += ", \"item_bytes\": " + std::to_string(p.item_bytes);  // protocol B's F-1: the width is mandatory under versions 3 and 4
    if (p.version == 22) {  // Kiln (FORM-FORGE.md section 6)
        s = "{\"version\": 22, \"rounds\": " + std::to_string(p.rounds) + ", \"program_ops\": " + std::to_string(p.program_ops) + ", \"batch_log2\": " + std::to_string(p.keyed_rounds) + ", \"target_log2\": " + std::to_string(p.registers) + ", \"tiles\": " + std::to_string(p.fold_every) + (p.blocks ? ", \"rows\": " + std::to_string(p.blocks) : std::string()) + ", \"spread\": " + std::to_string(p.scratch_bytes) + (p.table_kib ? ", \"fragments\": " + std::to_string(p.table_kib) : std::string()) + ((p.access_bytes & 1) ? std::string(", \"coalesced\": 1") : std::string()) + ((p.access_bytes >> 8) > 1 ? ", \"contig\": " + std::to_string(p.access_bytes >> 8) : std::string()) + ", \"lanes\": 8"
          + ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256"
          + ", \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id);
    } else     if (p.version == 23) {  // Tensor, the measurement form (TENSOR-READ-FORM.md): the period form or the batch form
        std::string pa; if (p.period_blocks) { static const char* hx = "0123456789abcdef"; for (int i = 0; i < 32; ++i) { pa += hx[p.program_anchor[i] >> 4]; pa += hx[p.program_anchor[i] & 15]; } }
        s = "{\"version\": 23, \"rounds\": " + std::to_string(p.rounds) + ", \"tiles\": " + std::to_string(p.program_ops) + (p.period_blocks ? ", \"period_blocks\": " + std::to_string(p.period_blocks) + ", \"program_anchor\": \"" + pa + "\"" : ", \"batch_log2\": " + std::to_string(p.keyed_rounds)) + ", \"lanes\": 16"
          + ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256"
          + ", \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + ", \"lean\": 1";
    } else     if (p.version == 21 || p.version == 24 || p.version == 25) {  // Forge2, the two-level form (FORM-FORGE.md section 7); 24 the base control (section 19); 25 entry 2 (section 20)
        std::string pa; if (p.period_blocks) { static const char* hx = "0123456789abcdef"; for (int i = 0; i < 32; ++i) { pa += hx[p.program_anchor[i] >> 4]; pa += hx[p.program_anchor[i] & 15]; } }  // the chain form (F-141): period_blocks and the test anchor, no batch_log2
        s = "{\"version\": " + std::to_string(p.version) + ", \"rounds\": " + std::to_string(p.rounds) + ", \"program_ops\": " + std::to_string(p.program_ops) + ", \"sequence\": " + std::to_string(p.blocks) + (p.period_blocks ? ", \"period_blocks\": " + std::to_string(p.period_blocks) + ", \"program_anchor\": \"" + pa + "\"" : ", \"batch_log2\": " + std::to_string(p.keyed_rounds)) + ", \"lanes\": 16, \"registers\": " + std::to_string(p.registers) + ", \"scratch_kib\": " + std::to_string(p.scratch_bytes >> 10)
          + ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256, \"fold_every\": " + std::to_string(p.fold_every)
          + ", \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + ((((p.program_distance >> 1) & 7) | (((p.program_distance >> 16) & 1) << 3)) ? ", \"fp_mode\": " + std::to_string(((p.program_distance >> 1) & 7) | (((p.program_distance >> 16) & 1) << 3)) : std::string()) + (((p.program_distance >> 4) & 15) ? ", \"pair_log2\": " + std::to_string((p.program_distance >> 4) & 15) : std::string()) + (((p.program_distance >> 8) & 127) ? ", \"prefetch\": " + std::to_string((p.program_distance >> 8) & 127) : std::string()) + ((p.program_distance & 32768) ? std::string(", \"lean\": 1") : std::string()) + ((p.program_distance & 131072) ? std::string(", \"chain_select\": 1") : std::string()) + (((p.program_distance >> 18) & 3) ? ", \"route\": " + std::to_string(((p.program_distance >> 18) & 3) * 25u) : std::string()) + ((p.program_distance & 1048576) ? std::string(", \"mix\": 1") : std::string())
          + ((p.features & 1024) ? ", \"features\": 1024, \"keyed_rounds\": " + std::to_string(ignh_keyed_count(&p)) : std::string());  // F-441: the bond-keyed item-read steps under bit 10 (21 and 25)
    } else     if (p.version == 20) {  // Forge (FORM-FORGE.md section 2's fields)
        s = "{\"version\": 20, \"rounds\": " + std::to_string(p.rounds) + ", \"program_ops\": " + std::to_string(p.program_ops) + ", \"lanes\": 16, \"registers\": 32, \"scratch_kib\": " + std::to_string(p.scratch_bytes >> 10)
          + ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256, \"fold_every\": " + std::to_string(p.fold_every)
          + ", \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + ((p.program_distance & 1) ? std::string(", \"drawn_order\": 1") : std::string());
    } else     if (p.version == 19) {  // the raster form that draws continuously (RASTER-V19-DESIGN.md): rounds the triangles, reach, target_log2, the anchor as hex; the dataset when keyed
        char a[65]; for (int i = 0; i < 32; ++i) std::snprintf(a + 2 * i, 3, "%02x", p.program_anchor[i]);
        s = "{\"version\": 19, \"rounds\": " + std::to_string(p.rounds) + ", \"reach\": " + std::to_string(p.scratch_bytes) + ", \"target_log2\": " + std::to_string(p.registers) + ", \"lanes\": 8, \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + ", \"period_blocks\": " + std::to_string(p.period_blocks)
          + (p.dataset_bytes ? ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256" : std::string())
          + ", \"program_anchor\": \"" + a + "\"";
    } else     if (p.version == 17 || p.version == 18) {  // the texture form (17: rounds the samples) and the raster form (18: rounds the triangles): period_blocks, the anchor as hex; no dataset
        char a[65]; for (int i = 0; i < 32; ++i) std::snprintf(a + 2 * i, 3, "%02x", p.program_anchor[i]);
        s = "{\"version\": " + std::to_string(p.version) + ", \"rounds\": " + std::to_string(p.rounds) + ", \"lanes\": 8, \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + ", \"period_blocks\": " + std::to_string(p.period_blocks)
          + ((p.version == 17 || p.version == 18) && p.dataset_bytes ? ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256" : std::string())
          + (p.version == 18 && p.registers == 14 ? std::string(", \"target_log2\": 14") : std::string()) + (p.version == 18 && (p.program_distance & 2) ? std::string(", \"draw\": 1") : std::string()) + ", \"program_anchor\": \"" + a + "\""
          + ((p.features & 1024) ? ", \"features\": 1024, \"keyed_rounds\": " + std::to_string(ignh_keyed_count(&p)) : std::string());  // F-441: the bond-keyed triangles under bit 10 (18)
    } else     if (p.version == 16) {  // the static-fabric coordinate (C-36): rounds (the passes), period_blocks, the anchor as hex; no dataset
        static const char* hx = "0123456789abcdef"; std::string a; for (int i = 0; i < 32; ++i) { a += hx[p.program_anchor[i] >> 4]; a += hx[p.program_anchor[i] & 15]; }
        s = "{\"version\": 16, \"rounds\": " + std::to_string(p.rounds) + ", \"lanes\": 8, \"epoch_blocks\": " + std::to_string(p.epoch_blocks) + ", \"item_chain\": 1, \"chain_id\": " + std::to_string(p.chain_id) + (p.registers ? ", \"registers\": " + std::to_string(p.registers) : std::string()) + ((p.program_distance & 8388608) ? ", \"dataset_mib\": " + std::to_string(p.dataset_bytes >> 20) + ", \"cache_mib\": " + std::to_string(p.cache_bytes >> 20) + ", \"parents\": " + std::to_string(p.parents) + ", \"item_bytes\": 256, \"read_every\": " + std::to_string(8u << ((p.program_distance >> 24) & 3)) : std::string())
          + ", \"period_blocks\": " + std::to_string(p.period_blocks) + ((p.program_distance & 1) ? std::string(", \"tails\": 1") : std::string()) + ((p.program_distance & 2) ? std::string(", \"weights\": 1") : std::string()) + ((p.program_distance & 4) ? std::string(", \"revision\": 4") : std::string()) + ((p.program_distance & 8) ? std::string(", \"revision\": 5") : std::string()) + ((p.program_distance & 16) ? std::string(", \"revision\": 6") : std::string()) + ((p.program_distance & 32) ? std::string(", \"revision\": 7") : std::string()) + ((p.program_distance & 64) ? std::string(", \"revision\": 8") : std::string()) + ((p.program_distance & 128) ? std::string(", \"revision\": 9") : std::string()) + ((p.program_distance & 256) ? std::string(", \"revision\": 10") : std::string()) + ((p.program_distance & 8388608) ? std::string((p.program_distance & 67108864) ? ((p.program_distance & 134217728) ? ", \"revision\": 28" : (p.program_distance & 131072) ? ", \"revision\": 27" : ", \"revision\": 26") : ", \"revision\": 25") : ((p.program_distance >> 18) & 3) ? std::string(", \"revision\": ") + std::to_string(((p.program_distance >> 18) & 3) == 1 ? ((p.program_distance & 4194304) ? 24 : 19) : ((p.program_distance >> 18) & 3) == 3 ? 23 : 20 + ((p.program_distance >> 20) & 3)) : (p.program_distance & 131072) ? ((p.program_distance & 65536) ? std::string(", \"revision\": 18") : std::string(", \"revision\": 17")) : (p.program_distance & 65536) ? std::string(", \"revision\": 16") : (p.program_distance & 32768) ? std::string(", \"revision\": 15") : (p.program_distance & 2048) ? std::string(", \"revision\": 13") : (p.program_distance & 1024) ? std::string(", \"revision\": 12") : ((p.program_distance & 512) ? std::string(", \"revision\": 11") : std::string())) + (((p.program_distance >> 12) & 7) ? ", \"variant\": " + std::to_string((p.program_distance >> 12) & 7) : std::string()) + ", \"program_anchor\": \"" + a + "\"";  // tails printed only when 1, so revision 2's records stand
    } else if (p.version == 15) {  // the bandwidth-first class (C-35): its own fields, the anchor as hex
        static const char* hx = "0123456789abcdef"; std::string a; for (int i = 0; i < 32; ++i) { a += hx[p.program_anchor[i] >> 4]; a += hx[p.program_anchor[i] & 15]; }
        s += ", \"program_ops\": " + std::to_string(p.program_ops) + ", \"period_blocks\": " + std::to_string(p.period_blocks) + ((p.program_distance & 2) ? std::string(", \"read_rule\": 1") : std::string()) + ", \"program_anchor\": \"" + a + "\"";
    } else if (p.version >= 9) {  // R0D and R0E: every field the document has, the anchor as hex
        static const char* hx = "0123456789abcdef"; std::string a; for (int i = 0; i < 32; ++i) { a += hx[p.program_anchor[i] >> 4]; a += hx[p.program_anchor[i] & 15]; }
        s += ", \"blocks\": " + std::to_string(p.blocks) + ", \"registers\": " + std::to_string(p.registers) + ", \"features\": " + std::to_string(p.features) +
             ((p.features & 1024) ? ", \"keyed_rounds\": " + std::to_string(p.keyed_rounds) : std::string()) +  // the parser takes the field under bit 10 only
             ", \"program_ops\": " + std::to_string(p.program_ops) + ", \"period_blocks\": " + std::to_string(p.period_blocks) +
             (p.program_distance ? ", \"program_distance\": " + std::to_string(p.program_distance) : std::string()) +
             (p.version >= 12 && p.version <= 14 ? ", \"table_kib\": " + std::to_string(p.table_kib) + ", \"access_bytes\": " + std::to_string(p.access_bytes) + (p.shared_kib ? ", \"shared_kib\": " + std::to_string(p.shared_kib) : std::string()) : std::string()) +
             (p.version >= 13 && p.version <= 14 ? ", \"mma_max\": " + std::to_string(p.mma_max) + ", \"chains_per_nonce\": " + std::to_string(p.chains_per_nonce) + ", \"gather_lane_bank\": " + std::to_string(p.gather_lane_bank) : std::string()) +
             (p.version == 14 ? ", \"tile_shape\": " + std::to_string(p.tile_shape) + (p.fold_every > 1 ? ", \"fold_every\": " + std::to_string(p.fold_every) : std::string()) + (p.items_per_round > 1 ? ", \"items_per_round\": " + std::to_string(p.items_per_round) : std::string()) + (p.registers_live > 16 ? ", \"registers_live\": " + std::to_string(p.registers_live) : std::string()) + (p.contexts_hint ? ", \"contexts_hint\": " + std::to_string(p.contexts_hint) : std::string()) + (p.layout_draw ? ", \"layout_draw\": 1" : std::string()) + (p.difficulty_scale_ppm != 1000000 ? ", \"difficulty_scale_ppm\": " + std::to_string(p.difficulty_scale_ppm) : std::string()) + (p.tile_layout ? ", \"tile_layout\": " + std::to_string(p.tile_layout) : std::string()) : std::string()) +  // fold_every printed only above 1, so the k = 1 documents of record keep their params records
             ", \"program_anchor\": \"" + a + "\"";
    }
    if (p.version == 8) {  // the blended relation's fields (section 0.8): blocks and registers mandatory, features when non-zero
        s += ", \"blocks\": " + std::to_string(p.blocks) + ", \"registers\": " + std::to_string(p.registers);
        if (p.features) s += ", \"features\": " + std::to_string(p.features);
        if (p.features & 1024) s += ", \"keyed_rounds\": " + std::to_string(p.keyed_rounds);  // bit 10 (section 0.9): the record carries every field the document has (F-1 class; it omitted this one until 11 Oct 2026)
    }
    return s + "}";
}

void write_file(const std::string& path, const std::string& algo, const std::string& cls,
                uint64_t seed, const std::vector<vec>& vs) {
    std::ofstream o(path);
    o << "{\n  \"candidate\": \"" << algo << "\",\n  \"class\": \"" << cls << "\",\n"
      << "  \"generator\": \"" << ignpow_build_string() << "\",\n  \"seed\": \"" << seed << "\",\n"
      << "  \"params\": " << complete_params(algo) << ",\n"
      << "  \"count\": " << vs.size() << ",\n  \"vectors\": [\n";
    for (size_t i = 0; i < vs.size(); ++i) {
        const vec& v = vs[i];
        o << "    {\"height\": " << v.height << ", \"header\": \"" << v.header << "\", \"nonce\": \"" << v.nonce << "\"";
        if (cls == "tails") {
            o << ", \"nonce_count\": \"" << v.nonce_count << "\", \"share_target\": \"" << v.share_target
              << "\", \"min_nonce\": \"" << v.min_nonce << "\", \"min_hash\": \"" << v.min_hash
              << "\", \"shares\": \"" << v.shares << "\"";
        } else if (cls == "malformed") {
            o << ", \"ctx_height\": " << v.ctx_height << ", \"mix\": \"" << v.mix << "\", \"boundary\": \""
              << v.boundary << "\", \"expect\": \"" << v.expect << "\"";
            if (!v.share_target.empty()) o << ", \"keyed_shares\": \"" << v.share_target << "\"";  // a keyed document's rows: the test secret's shares for the replay
        } else if (cls == "dataset") {
            o << ", \"epoch\": " << v.ctx_height << ", \"E\": \"" << v.mix << "\", \"D\": \"" << v.hash
              << "\", \"cache_digest\": \"" << v.expect << "\", \"dataset_digest\": \"" << v.min_hash << "\", \"dataset_bytes\": \"" << v.nonce_count
              << "\", \"build_ms\": \"" << v.shares << "\"";
        } else {
            o << ", \"mix\": \"" << v.mix << "\", \"hash\": \"" << v.hash << "\"";
            if (cls == "fill" && !v.min_hash.empty()) o << ", \"key\": \"" << v.min_hash << "\"";  // the keyed document's test secret as a vector field (the harness's key)
            if (cls == "trace" || cls == "fill" || cls == "play" || cls == "counts") o << ", \"trace\": " << v.share_target;  // the trace object, written by ignh_trace (igneum_play_trace for the play class)
            if (cls == "keyed") {
                o << ", \"test_sk\": \"" << v.min_hash << "\", \"shares\": \"" << v.share_target << "\"";  // section 0.9: the test secret and the keyed_rounds shares, concatenated
                if (!v.boundary.empty()) o << ", \"m\": \"" << v.boundary << "\"";  // section 0.9.1: the keyed rounds' messages m_0..m_(S-1), concatenated (ignh_hash_keyed_m; the proof pi's input)
            }
            if (cls == "key-forms") o << ", \"form\": \"" << v.share_target << "\", \"expect\": \"" << v.expect << "\"";
            if (cls == "tickets") o << ", \"graph_key\": \"" << v.min_hash << "\", \"n_tickets\": " << v.shares << ", \"tickets\": \"" << v.share_target << "\", \"refusals\": \"" << v.expect << "\"";  // the Graph coordinate: the key, the tickets (168 bytes each, hex, concatenated), the refusals (kind:hex;...)
        }
        if (cls == "params-rule" || cls == "registers-rule") {
            // the document under test as the hex of its JSON text (the writer's line reader takes no nested object), and the
            // parse verdict expected: "params" (refused, IGNPOW_E_PARAMS) or "ok"
            o << ", \"doc_hex\": \"" << v.boundary << "\", \"expect\": \"" << v.expect << "\", \"note\": \"" << v.share_target << "\"";
        }
        o << "}" << (i + 1 < vs.size() ? "," : "") << "\n";
    }
    o << "  ]\n}\n";
}

// Minimal reader for the writer's own output: finds "key": value pairs per line.
std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\": ";
    size_t p = line.find(k);
    if (p == std::string::npos) return "";
    p += k.size();
    if (line[p] == '"') {
        size_t q = line.find('"', p + 1);
        return line.substr(p + 1, q - p - 1);
    }
    size_t q = line.find_first_of(",}", p);
    return line.substr(p, q - p);
}

uint64_t u64(const std::string& s) { return std::strtoull(s.c_str(), nullptr, 10); }

// The file's own params record (its "params" line's value, the complete document) and the reason a read refuses:
// a missing or unreadable file, no candidate line, a truncated file (no closing brace, or fewer rows than its "count"
// says). An unreadable set is never a pass (security's condition-5 finding, the project's order of 16:2x UK, 11 Oct 2026).
std::string g_file_params;
std::string g_read_reason;

bool read_file(const std::string& path, std::string& algo, std::string& cls, std::vector<vec>& vs) {
    g_file_params.clear(); g_read_reason.clear();
    std::ifstream in(path);
    if (!in) { g_read_reason = "no such file or not readable"; return false; }
    std::string line, last;
    long long count = -1;
    while (std::getline(in, line)) {
        if (!line.empty() && line.find_first_not_of(" \t\r") != std::string::npos) last = line;
        if (line.find("\"candidate\"") != std::string::npos) algo = field(line, "candidate");
        else if (line.find("\"class\"") != std::string::npos) cls = field(line, "class");
        else if (line.compare(0, 12, "  \"params\": ") == 0) { g_file_params = line.substr(12); while (!g_file_params.empty() && (g_file_params.back() == ',' || g_file_params.back() == ' ')) g_file_params.pop_back(); }
        else if (line.compare(0, 11, "  \"count\": ") == 0) count = std::strtoll(line.c_str() + 11, nullptr, 10);
        else if (line.find("{\"height\"") != std::string::npos) {
            vec v;
            v.height = u64(field(line, "height"));
            v.header = field(line, "header");
            v.nonce = u64(field(line, "nonce"));
            v.mix = field(line, "mix");
            v.hash = field(line, "hash");
            v.boundary = field(line, "boundary");
            v.expect = field(line, "expect");
            v.nonce_count = u64(field(line, "nonce_count"));
            v.share_target = field(line, "share_target");
            v.min_nonce = u64(field(line, "min_nonce"));
            v.min_hash = field(line, "min_hash");
            v.shares = u64(field(line, "shares"));
            v.ctx_height = u64(field(line, "ctx_height"));
            if (line.find("\"test_sk\"") != std::string::npos) {  // a keyed row (section 0.9): the test secret and the shares as hex
                v.min_hash = field(line, "test_sk");
                v.share_target = field(line, "shares");
                v.boundary = field(line, "m");  // the messages when the file carries them (11 Oct 2026; older files do not)
            }
            if (!field(line, "doc_hex").empty()) { v.boundary = field(line, "doc_hex"); v.share_target = field(line, "note"); }
            if (!field(line, "keyed_shares").empty()) v.share_target = field(line, "keyed_shares");
            if (!field(line, "tickets").empty()) { v.share_target = field(line, "tickets"); v.min_hash = field(line, "graph_key"); v.expect = field(line, "refusals"); v.shares = u64(field(line, "n_tickets")); }  // the Graph coordinate's tickets class
            if (!field(line, "form").empty()) v.share_target = field(line, "form");
            if (!field(line, "dataset_digest").empty()) {
                v.ctx_height = u64(field(line, "epoch")); v.mix = field(line, "E"); v.hash = field(line, "D");
                v.min_hash = field(line, "dataset_digest"); v.nonce_count = u64(field(line, "dataset_bytes")); v.shares = u64(field(line, "build_ms"));
                v.expect = field(line, "cache_digest");
            }
            vs.push_back(v);
        }
    }
    if (algo.empty()) { g_read_reason = "no candidate line (not a vector file)"; return false; }
    if (last.find('}') == std::string::npos || last.find('{') != std::string::npos) { g_read_reason = "truncated: the file does not end with its closing brace"; return false; }
    if (count >= 0 && (long long)vs.size() != count) { g_read_reason = "truncated: " + std::to_string(vs.size()) + " rows read, the file's count says " + std::to_string(count); return false; }
    return true;
}

// The fp class (version 8's floating form): every row's `out` recomputed from (x, y, z) with the contracted-off form,
// the wide rows with their opcode; a row that differs is a mismatch. Returns the rows read, or -1 when the file cannot be read.
long long check_fp(const std::string& path, size_t& bad) {
    std::ifstream in(path);
    if (!in) return -1;
    std::string line; long long rows = 0; bool closed = false;
    while (std::getline(in, line)) {
        if (line.find("\"x\": ") == std::string::npos || line.find("\"out\": ") == std::string::npos) { if (line == "}") closed = true; continue; }
        const uint32_t x = uint32_t(u64(field(line, "x"))), y = uint32_t(u64(field(line, "y"))), z = uint32_t(u64(field(line, "z"))), want = uint32_t(u64(field(line, "out")));
        const std::string op = field(line, "op");
        const uint32_t got = op.empty() ? ignh_v8_fop(x, y, z) : ignh_v8_fop_wide(uint32_t(u64(op)), x, y, z);
        if (got != want) { ++bad; if (bad <= 5) std::printf("MISMATCH fp%s x %u y %u z %u: out %u, recomputed %u\n", op.empty() ? "" : (" op " + op).c_str(), x, y, z, want, got); }
        ++rows;
    }
    if (!closed) { g_read_reason = "truncated: the file does not end with its closing brace"; return -1; }
    return rows;
}

struct ctx_cache {  // one light context per epoch, created on demand
    std::string algo;
    std::vector<std::pair<uint64_t, ignpow_ctx*>> items;
    std::vector<ignpow_ctx*> donors;  // IGNPOW_SHARED_CTX: the donors outlive the contexts sharing them (freed last)
    ~ctx_cache() { for (auto& it : items) ignpow_ctx_free(it.second); for (auto* d : donors) ignpow_ctx_free(d); }
    ignpow_ctx* get(uint64_t height, int full = 0) {
        int st = 0;
        // IGNPOW_FULL_CTX=1: every context built with the full dataset (the full-versus-light row: a set generated under the full
        // dataset and checked under the light context must agree, the protocol team's lab reading of 21:1x)
        if (const char* e = std::getenv("IGNPOW_FULL_CTX")) { if (e[0] == '1') full = 1; }
        uint64_t e = ignpow_epoch_of(algo.c_str(), g_params, height, &st);
        if (st != IGNPOW_OK) return nullptr;
        for (auto& it : items) if (it.first == e) return it.second;
        auto t0 = std::chrono::steady_clock::now();
        ignpow_ctx* c = nullptr;
        // IGNPOW_SHARED_CTX=<document json>: the DONOR's document (the unkeyed twin); every context is then created with
        // ignpow_ctx_create_shared over a donor built from that document (the security review's cell: a keyed set checked through a
        // shared context equals the standalone context's; a donor of another seed is refused)
        if (const char* twin = std::getenv("IGNPOW_SHARED_CTX")) {
            ignpow_ctx* d = ignpow_ctx_create(algo == IGNPOW_ALGO_IGNEUM_PLAY ? IGNPOW_ALGO_IGNEUMHASH : algo.c_str(), twin, height, full, &st);  // the donor is always igneumhash
            if (!d) { std::fprintf(stderr, "donor context for height %llu: %s\n", (unsigned long long)height, reason_name(st)); return nullptr; }
            donors.push_back(d);
            c = ignpow_ctx_create_shared(algo.c_str(), g_params, height, d, &st);
            if (!c) { std::fprintf(stderr, "shared context for height %llu: %s\n", (unsigned long long)height, reason_name(st)); return nullptr; }
        } else
        c = ignpow_ctx_create(algo.c_str(), g_params, height, full, &st);
        auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count();
        if (!c) { std::fprintf(stderr, "context for height %llu: %s\n", (unsigned long long)height, reason_name(st)); return nullptr; }
        std::fprintf(stderr, "context epoch %llu (%s) in %lld ms\n", (unsigned long long)e, full ? "full" : "light", (long long)ms);
        items.emplace_back(e, c);
        return c;
    }
};

// The row's hash: the candidate's entry, or on a keyed igneumhash document (section 0.9) the prover's path under the
// test secret, as the trace and the keyed class use it
int row_hash(ignpow_ctx* c, uint64_t height, const uint8_t h[32], uint64_t nonce, uint8_t mix[32], uint8_t out[32]) {
    ignh_params kp;
    if (std::strcmp(ignpow_ctx_algo(c), IGNPOW_ALGO_IGNEUMHASH) == 0 && ignh_params_parse(g_params, &kp) == IGNPOW_OK && (kp.features & 1024)) {
        uint8_t sk[32]; std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32);
        ignh_test_sk(h, sk);
        return ignh_hash_keyed(c, height, h, nonce, sk, shares.data(), mix, out);
    }
    return ignpow_hash(c, height, h, nonce, mix, out);
}

bool fill(ctx_cache& cc, vec& v) {
    ignpow_ctx* c = cc.get(v.height);
    if (!c) return false;
    uint8_t h[32], mix[32], out[32];
    if (!unhex(v.header, h, 32)) return false;
    int st = row_hash(c, v.height, h, v.nonce, mix, out);
    if (st != IGNPOW_OK) { std::fprintf(stderr, "hash at %llu: %s\n", (unsigned long long)v.height, reason_name(st)); return false; }
    v.mix = hex(mix, 32);
    v.hash = hex(out, 32);
    return true;
}

// The candidate's epoch length: the first height whose epoch differs from height 0's.
uint64_t epoch_len(const std::string& algo) {
    int st = 0;
    const uint64_t e0 = ignpow_epoch_of(algo.c_str(), g_params, 0, &st);
    if (st != IGNPOW_OK) return 0;
    for (uint64_t h = 1; h <= (1ull << 24); ++h)  // inclusive; the controls' harness epoch is 2^16 heights
        if (ignpow_epoch_of(algo.c_str(), g_params, h, &st) != e0) return h;
    return 0;
}

bool is_kawpow(const std::string& algo) { return algo == IGNPOW_ALGO_KAWPOW_094; }

int gen(const std::string& algo, const std::string& cls, const std::string& out, uint64_t seed) {
    rng64 r(seed);
    ctx_cache cc; cc.algo = algo;
    std::vector<vec> vs;
    auto rand_header = [&]() { uint8_t h[32]; r.bytes(h, 32); return hex(h, 32); };
    // Heights used across classes: epoch 0 (entry), the first epoch boundary,
    // epoch 448 (KAWPOW's 4.5 GiB matched point at 7500 blocks) and epoch 598
    // (the live Ravencoin epoch; the exploit height 4,487,776 lies in it for
    // KAWPOW, and the same offset is used for every other candidate).
    const uint64_t L = epoch_len(algo);
    if (L == 0) { std::fprintf(stderr, "cannot derive the epoch length of %s\n", algo.c_str()); return 2; }
    const uint64_t kMatched = 448ull * L;
    const uint64_t kLive = is_kawpow(algo) ? 4487776ull : 598ull * L + 2776ull % L;
    if (cls == "unseen") {
        const uint64_t heights[] = {1, 1000 % L, L - 1, L, 2 * L, kMatched, kMatched + 77 % L, kLive};
        for (uint64_t h : heights)
            for (int i = 0; i < 8; ++i) {
                vec v; v.height = h; v.header = rand_header(); v.nonce = r.next();
                if (!fill(cc, v)) return 2;
                vs.push_back(v);
            }
    } else if (cls == "nonce-boundary") {
        const uint64_t nonces[] = {0, 1, 0xFFFFFFFFull, 0x100000000ull, 0x7FFFFFFFFFFFFFFFull,
                                   0x8000000000000000ull, 0xFFFFFFFFFFFFFFFEull, 0xFFFFFFFFFFFFFFFFull};
        const uint64_t heights[] = {1, kMatched};
        for (uint64_t h : heights) {
            std::string hdr = rand_header();
            for (uint64_t n : nonces) { vec v; v.height = h; v.header = hdr; v.nonce = n; if (!fill(cc, v)) return 2; vs.push_back(v); }
        }
    } else if (cls == "transitions") {
        // Both sides of an epoch boundary and of the 3-block program period, same header.
        std::vector<uint64_t> heights = {0, 1, 2, 3, 5, 6, L - 1, L, L + 1, kMatched - 1, kMatched, kLive - 1, kLive};
        if (g_params && (std::string(g_params).find("\"version\": 21") != std::string::npos || std::string(g_params).find("\"version\": 23") != std::string::npos || std::string(g_params).find("\"version\": 24") != std::string::npos || std::string(g_params).find("\"version\": 25") != std::string::npos)) {  // the chain form (F-141, the review lead's condition 3): both sides of the first period boundary
            const std::string gp(g_params); const size_t k = gp.find("\"period_blocks\":");
            if (k != std::string::npos) { const uint64_t pb = std::strtoull(gp.c_str() + k + 16, nullptr, 10); if (pb >= 2) { heights.push_back(pb - 1); heights.push_back(pb); heights.push_back(pb + 1); heights.push_back(2 * pb - 1); heights.push_back(2 * pb); } }
        }
        std::string hdr = rand_header();
        uint64_t n = r.next();
        for (uint64_t h : heights) { vec v; v.height = h; v.header = hdr; v.nonce = n; if (!fill(cc, v)) return 2; vs.push_back(v); }
        // Same header and nonce at two heights of one period must agree and across periods differ: checked by the reader.
    } else if (cls == "fp") {
        // version 8 option (b): the floating form's unit vectors (fp-edge: rounding ties, the range's ends, cancellation;
        // fp-contract: rows where a contracting build differs; fp-denormal: the operand mask's floor, never a denormal)
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "fp is an igneumhash class\n"); return 2; }
        std::ofstream o(out);
        o << "{\n  \"candidate\": \"igneumhash\",\n  \"class\": \"fp\",\n  \"generator\": \"" << ignpow_build_string() << "\",\n  \"seed\": \"" << seed << "\",\n  \"params\": " << complete_params(algo)
          << ",\n  \"rule\": \"y' = (y & 0x3FFFFFFF) | 0x20000000 as binary32, z' likewise; x' = (x & 0x3FFFFFFF) | 0x20000000 as binary32; f = round(round(y' * z') + x'); out = bits(f) ^ x; nearest-even, no contraction\",\n  \"wide_rule\": \"bit 5: x', y', z' masked likewise; op 0 f = x' + (y' - z'); op 1 f = x' - (y' + z'); op 2 f = (y' * z') + x'; op 3 f = (x' - y') + z'; the inner operation rounded first; out = bits(f) ^ x; nearest-even, no contraction\",\n  \"edge\": [\n";
        const uint32_t edges[] = {0x20000000u, 0x20000001u, 0x3FFFFFFFu, 0x3F7FFFFFu, 0x3F800000u, 0x3F800001u, 0x30000000u, 0x2FFFFFFFu, 0x3B800000u, 0x3C000000u, 0x00000000u, 0xFFFFFFFFu, 0x7F800000u, 0x7FC00000u, 0x80000000u, 0x00000001u};
        const uint32_t xs[] = {0u, 1u, 0xFFFFu, 0x8000u, 0x12345678u, 0xFFFFFFFFu};
        bool first = true; size_t ne = 0, nc = 0;
        for (uint32_t y : edges) for (uint32_t z : edges) for (uint32_t x : xs) {
            const uint32_t a = ignh_v8_fop(x, y, z), b = ignh_v8_fop_contracted(x, y, z);
            o << (first ? "" : ",\n") << "    {\"x\": " << x << ", \"y\": " << y << ", \"z\": " << z << ", \"out\": " << a << (a != b ? ", \"contracted\": " : "") << (a != b ? std::to_string(b) : std::string()) << "}";
            first = false; ++ne; if (a != b) ++nc;
        }
        o << "\n  ],\n  \"random\": [\n";
        first = true; size_t nr = 0;
        for (int i = 0; i < 4096; ++i) {
            const uint32_t x = uint32_t(r.next()), y = uint32_t(r.next()), z = uint32_t(r.next());
            const uint32_t a = ignh_v8_fop(x, y, z), b = ignh_v8_fop_contracted(x, y, z);
            o << (first ? "" : ",\n") << "    {\"x\": " << x << ", \"y\": " << y << ", \"z\": " << z << ", \"out\": " << a << (a != b ? ", \"contracted\": " + std::to_string(b) : std::string()) << "}";
            first = false; if (a != b) ++nc; ++nr;
        }
        o << "\n  ],\n  \"wide\": [\n";  // bit 5: every opcode's form on the edge operands and 1024 random rows
        first = true; size_t nw = 0, ncw = 0;
        for (uint32_t op = 0; op < 4; ++op) {
            for (uint32_t y : edges) for (uint32_t z : edges) for (uint32_t x : xs) {
                const uint32_t a = ignh_v8_fop_wide(op, x, y, z), b = ignh_v8_fop_wide_contracted(op, x, y, z);
                o << (first ? "" : ",\n") << "    {\"op\": " << op << ", \"x\": " << x << ", \"y\": " << y << ", \"z\": " << z << ", \"out\": " << a << (a != b ? ", \"contracted\": " + std::to_string(b) : std::string()) << "}";
                first = false; ++nw; if (a != b) ++ncw;
            }
            for (int i = 0; i < 1024; ++i) {
                const uint32_t x = uint32_t(r.next()), y = uint32_t(r.next()), z = uint32_t(r.next());
                const uint32_t a = ignh_v8_fop_wide(op, x, y, z), b = ignh_v8_fop_wide_contracted(op, x, y, z);
                o << ",\n    {\"op\": " << op << ", \"x\": " << x << ", \"y\": " << y << ", \"z\": " << z << ", \"out\": " << a << (a != b ? ", \"contracted\": " + std::to_string(b) : std::string()) << "}";
                ++nw; if (a != b) ++ncw;
            }
        }
        o << "\n  ],\n  \"contract_differs\": " << nc << ",\n  \"wide_rows\": " << nw << ",\n  \"wide_contract_differs\": " << ncw << ",\n  \"denormal_reachable\": false\n}\n";
        std::printf("wrote %zu edge and %zu random fp vectors (%zu differ under contraction) to %s\n", ne, nr, nc, out.c_str());
        return 0;
    } else if (cls == "malformed") {
        // verify-reason vectors: the context is created for ctx_height; the
        // reader expects the named reason.
        std::string hdr = rand_header();
        uint64_t n = r.next();
        vec base; base.height = 1; base.ctx_height = 1; base.header = hdr; base.nonce = n;
        if (!fill(cc, base)) return 2;
        {
            // a keyed igneumhash document (section 0.9): ignpow_verify is keyless and refuses it, so the rows carry the test
            // secret's shares (keyed_shares) and the check replays them through ignh_hash_keyed before the mix and boundary
            // comparisons (11 Oct 2026, the launch document's admission run)
            ignh_params kp;
            if (algo == IGNPOW_ALGO_IGNEUMHASH && ignh_params_parse(g_params, &kp) == IGNPOW_OK && (kp.features & 1024)) {
                ignpow_ctx* c = cc.get(1);
                uint8_t h[32], sk[32], mix[32], out[32]; unhex(hdr, h, 32); ignh_test_sk(h, sk);
                std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32);
                if (!c || ignh_hash_keyed(c, 1, h, n, sk, shares.data(), mix, out) != IGNPOW_OK) return 2;
                base.share_target = hex(shares.data(), shares.size());
            }
        }
        std::string zeros(64, '0'), ones(64, 'f');
        { vec v = base; v.boundary = ones; v.expect = "ok"; vs.push_back(v); }
        { vec v = base; v.boundary = zeros; v.expect = "boundary"; vs.push_back(v); }
        { vec v = base; v.boundary = ones; v.mix = zeros; v.expect = "mix"; vs.push_back(v); }
        { vec v = base; v.boundary = zeros; v.mix = zeros; v.expect = "mix"; vs.push_back(v); }  // mix first, both wrong
        { vec v = base; v.boundary = ones; v.height = L; v.expect = "epoch"; vs.push_back(v); }
        { vec v = base; v.boundary = ones; v.height = 0x80000000ull; v.expect = "height"; vs.push_back(v); }
        { vec v = base; v.boundary = v.hash; v.expect = "ok"; vs.push_back(v); }  // equal to the boundary passes
        {
            // one below the hash fails: decrement the hash as a 256-bit big-endian number
            uint8_t b[32]; unhex(base.hash, b, 32);
            for (int i = 31; i >= 0; --i) { if (b[i]-- != 0) break; }
            vec v = base; v.boundary = hex(b, 32); v.expect = "boundary"; vs.push_back(v);
        }
    } else if (cls == "tails") {
        // Search vectors over nonce ranges that are not a multiple of any launch
        // size; a full context at epoch 0. share_target = 2^256 / 4096.
        ignpow_ctx* c = cc.get(1, 1);
        if (!c) return 2;
        uint8_t target[32] = {0}; target[1] = 0x10;  // 0x0010... = 2^244 = 2^256/4096
        const uint64_t counts[] = {4099, 65537, 1000003};
        for (uint64_t cnt : counts) {
            vec v; v.height = 1; v.header = rand_header(); v.nonce = r.next(); v.nonce_count = cnt;
            v.share_target = hex(target, 32);
            uint8_t h[32]; unhex(v.header, h, 32);
            uint8_t best[32]; std::memset(best, 0xff, 32); uint64_t best_n = 0; uint64_t shares = 0;
            for (uint64_t i = 0; i < cnt; ++i) {
                uint64_t n = v.nonce + i;  // wraps by design when the range crosses 2^64
                uint8_t mix[32], out[32];
                if (row_hash(c, 1, h, n, mix, out) != IGNPOW_OK) return 2;  // a keyed document: the sealer's search under the test secret (row_hash)
                if (std::memcmp(out, target, 32) <= 0) ++shares;
                if (std::memcmp(out, best, 32) < 0) { std::memcpy(best, out, 32); best_n = n; }
            }
            v.min_nonce = best_n; v.min_hash = hex(best, 32); v.shares = shares;
            vs.push_back(v);
            std::fprintf(stderr, "tails %llu: shares %llu min nonce %llu\n", (unsigned long long)cnt, (unsigned long long)shares, (unsigned long long)best_n);
        }
    } else if (cls == "trace") {
        // Two full traces at height 1 (igneumhash only): every intermediate for kernel authors.
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "trace is an igneumhash class\n"); return 2; }
        ignpow_ctx* c = cc.get(1);
        if (!c) return 2;
        // IGNPOW_TRACE_COUNT (default 2): security's related-nonce reading (TP-151) wants 16 or more per document
        int ntr = 2; if (const char* e = std::getenv("IGNPOW_TRACE_COUNT")) { ntr = std::atoi(e); if (ntr < 1) ntr = 1; if (ntr > 64) ntr = 64; }
        for (int i = 0; i < ntr; ++i) {
            vec v; v.height = 1; v.header = rand_header(); v.nonce = r.next();
            if (!fill(cc, v)) return 2;
            uint8_t h[32]; unhex(v.header, h, 32);
            // the trace through a temporary file (open_memstream is POSIX only; the Windows build has none)
            FILE* mem = std::tmpfile();
            if (!mem || ignh_trace(c, 1, h, v.nonce, mem) != IGNPOW_OK) return 2;
            std::fflush(mem);
            const long len = std::ftell(mem);
            std::rewind(mem);
            std::string text(len > 0 ? size_t(len) : 0, '\0');
            if (len > 0 && std::fread(&text[0], 1, size_t(len), mem) != size_t(len)) { std::fclose(mem); return 2; }
            std::fclose(mem);
            v.share_target = text;
            vs.push_back(v);
        }
    } else if (cls == "profile") {
        // igneum-play's CPU profile per stage per tick (IGNPOW_TRACE_COUNT attempts, 16 by default) on the calling thread, to stdout; no file
        if (algo != IGNPOW_ALGO_IGNEUM_PLAY) { std::fprintf(stderr, "profile is an igneum-play class\n"); return 2; }
        ignpow_ctx* c = cc.get(1); if (!c) return 2;
        int ntr = 16; if (const char* e = std::getenv("IGNPOW_TRACE_COUNT")) { ntr = std::atoi(e); if (ntr < 1) ntr = 1; }
        uint8_t h[32]; unhex(rand_header(), h, 32);
        return igneum_play_profile(c, 1, h, r.next(), uint32_t(ntr), stdout) == IGNPOW_OK ? 0 : 2;
    } else if (cls == "play" || cls == "counts") {
        // Proof of Play (igneum-play only): IGNPOW_TRACE_COUNT attempts at height 1 with the world and every tick's records (play), or
        // the counts stream per attempt (counts; 16 attempts by default)
        if (algo != IGNPOW_ALGO_IGNEUM_PLAY) { std::fprintf(stderr, "play and counts are igneum-play classes\n"); return 2; }
        ignpow_ctx* c = cc.get(1);
        if (!c) return 2;
        int ntr = cls == "counts" ? 16 : 2; if (const char* e = std::getenv("IGNPOW_TRACE_COUNT")) { ntr = std::atoi(e); if (ntr < 1) ntr = 1; if (ntr > 64) ntr = 64; }
        for (int i = 0; i < ntr; ++i) {
            vec v; v.height = 1; v.header = rand_header(); v.nonce = r.next();
            if (!fill(cc, v)) return 2;
            uint8_t h[32]; unhex(v.header, h, 32);
            FILE* mem = std::tmpfile();
            if (!mem || (cls == "counts" ? igneum_play_counts(c, 1, h, v.nonce, mem) : igneum_play_trace(c, 1, h, v.nonce, mem)) != IGNPOW_OK) return 2;
            std::fflush(mem);
            const long len = std::ftell(mem);
            std::rewind(mem);
            std::string text(len > 0 ? size_t(len) : 0, '\0');
            if (len > 0 && std::fread(&text[0], 1, size_t(len), mem) != size_t(len)) { std::fclose(mem); return 2; }
            std::fclose(mem);
            v.share_target = text;
            vs.push_back(v);
        }
    } else if (cls == "fill") {
        // the memory cryptanalysis harness's pairs (T5, T6; versions 11 and later carry the gather_table record in the trace): FIXED
        // headers and nonces so the keyed document's set and its unkeyed twin's align row for row: four fixed headers (the
        // byte pattern below) x four adjacent nonces from a fixed base (the pool's search order), 16 traces at
        // height 1; the harness pairs adjacent nonces under one header, two headers at one nonce, and the keyed row with the twin's
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "fill is an igneumhash class\n"); return 2; }
        ignpow_ctx* c = cc.get(1);
        if (!c) return 2;
        ignh_params fp; const bool fkeyed = ignh_params_parse(g_params, &fp) == IGNPOW_OK && (fp.features & 1024);
        for (uint32_t i = 0; i < 5; ++i) {
            uint8_t h[32]; for (uint32_t j = 0; j < 32; ++j) h[j] = uint8_t((0xA5u + 37u * i + 11u * j) ^ (j * (i + 1)));  // the fixed header i, a stated pattern
            // headers 0 to 3: four adjacent nonces each; header 4: one row at header 0's first nonce (two headers at one nonce and key)
            const uint64_t n0 = i < 4 ? 0x1000000000ull + 16 * i : 0x1000000000ull, n1 = i < 4 ? n0 + 4 : n0 + 1;
            for (uint64_t n = n0; n < n1; ++n) {
                vec v; v.height = 1; v.header = hex(h, 32); v.nonce = n;
                if (fkeyed) { uint8_t sk[32]; ignh_test_sk(h, sk); v.min_hash = hex(sk, 32); }
                if (!fill(cc, v)) return 2;
                FILE* mem = std::tmpfile();
                if (!mem || ignh_trace(c, 1, h, v.nonce, mem) != IGNPOW_OK) return 2;
                std::fflush(mem);
                const long len = std::ftell(mem);
                std::rewind(mem);
                std::string text(len > 0 ? size_t(len) : 0, '\0');
                if (len > 0 && std::fread(&text[0], 1, size_t(len), mem) != size_t(len)) { std::fclose(mem); return 2; }
                std::fclose(mem);
                v.share_target = text;
                vs.push_back(v);
            }
        }
    } else if (cls == "tickets") {
        // VectorWork's Graph coordinate (igneum-graph only; GRAPH-COORDINATE.md section 2): rows at height 1 whose nonce yields at least one
        // ticket (the solver searched from a seeded nonce until a graph with a 42-cycle), every ticket as its 168 canonical bytes with
        // the graph key beside it, and four refusals built from the first ticket (unsorted, a duplicated edge, one edge changed, an edge
        // out of range) that the verifier must refuse; the row's mix and hash are the ABI's (fill)
        if (algo != IGNPOW_ALGO_IGNEUM_GRAPH && algo != IGNPOW_ALGO_IGNEUM_GRAPH28 && algo != IGNPOW_ALGO_IGNEUM_GRAPH28K) { std::fprintf(stderr, "tickets is an igneum-graph class\n"); return 2; }
        if (algo == IGNPOW_ALGO_IGNEUM_GRAPH28) { g_graph_key = igneum_graph28_key; g_graph_verify = igneum_graph28_verify_ticket; g_graph_solve = igneum_graph28_solve; g_graph_edges = 1u << 28; }
        if (algo == IGNPOW_ALGO_IGNEUM_GRAPH28K) { g_graph_key = igneum_graph28k_key; g_graph_verify = igneum_graph28k_verify_ticket; g_graph_solve = igneum_graph28k_solve; g_graph_edges = 1u << 28; }
        uint64_t chain_id = 0; { std::string ps = g_params ? g_params : ""; const size_t i = ps.find("\"chain_id\":"); if (i == std::string::npos) { std::fprintf(stderr, "tickets needs chain_id\n"); return 2; } chain_id = std::strtoull(ps.c_str() + i + 11, nullptr, 10); }
        int nt = 4; if (const char* e = std::getenv("IGNPOW_TICKET_COUNT")) { nt = std::atoi(e); if (nt < 1) nt = 1; if (nt > 64) nt = 64; }
        for (int i = 0; i < nt; ++i) {
            vec v; v.height = 1; v.header = rand_header(); uint64_t n0 = r.next() & 0xFFFFFFFFull;
            uint8_t h[32]; unhex(v.header, h, 32);
            std::vector<uint32_t> tk(42 * 8); size_t found = 0; uint64_t nonce = n0;
            for (uint32_t tries = 0; tries < 400 && found == 0; ++tries, ++nonce) { if (g_graph_solve(chain_id, 1, h, nonce, tk.data(), 8, &found) != IGNPOW_OK) return 2; }
            if (found == 0) { std::fprintf(stderr, "tickets: no cycle in 400 graphs from nonce %llu\n", (unsigned long long)n0); return 2; }
            v.nonce = nonce - 1; if (found > 8) found = 8; v.shares = found;
            uint8_t key[32]; g_graph_key(chain_id, 1, h, v.nonce, key, nullptr); v.min_hash = hex(key, 32);
            std::string th;
            for (size_t s = 0; s < found; ++s) { uint8_t tb[168]; igneum_graph_ticket_bytes(tk.data() + 42 * s, tb); th += hex(tb, 168); }
            v.share_target = th;
            std::vector<uint32_t> e0(tk.begin(), tk.begin() + 42); std::string rf;
            { std::vector<uint32_t> e = e0; std::swap(e[0], e[41]); uint8_t tb[168]; igneum_graph_ticket_bytes(e.data(), tb); rf += "unsorted:" + hex(tb, 168) + ";"; }
            { std::vector<uint32_t> e = e0; e[1] = e[0]; uint8_t tb[168]; igneum_graph_ticket_bytes(e.data(), tb); rf += "duplicate:" + hex(tb, 168) + ";"; }
            { std::vector<uint32_t> e = e0; e[20] ^= 1u; if (e[20] <= e[19] || e[20] >= e[21]) e[20] = e0[20] ^ 2u; uint8_t tb[168]; igneum_graph_ticket_bytes(e.data(), tb); rf += "wrong-edge:" + hex(tb, 168) + ";"; }
            { std::vector<uint32_t> e = e0; e[41] = g_graph_edges; uint8_t tb[168]; igneum_graph_ticket_bytes(e.data(), tb); rf += "out-of-range:" + hex(tb, 168); }
            if (algo == IGNPOW_ALGO_IGNEUM_GRAPH28K) {
                // the negative of the keyed lane (the panel's hold on 28c): an UNKEYED solver's cycle (the plain 28-bit graph at the same key,
                // searched from the row's seed nonce) must be refused by the keyed verifier; the item names its own nonce (unkeyed@nonce)
                std::vector<uint32_t> uk(42 * 8); size_t uf = 0; uint64_t un = n0;
                for (uint32_t tries = 0; tries < 400 && uf == 0; ++tries, ++un) { if (igneum_graph28_solve(chain_id, 1, h, un, uk.data(), 8, &uf) != IGNPOW_OK) return 2; }
                if (uf == 0) { std::fprintf(stderr, "tickets: no unkeyed cycle in 400 graphs from nonce %llu\n", (unsigned long long)n0); return 2; }
                uint8_t tb[168]; igneum_graph_ticket_bytes(uk.data(), tb); rf += ";unkeyed@" + std::to_string(un - 1) + ":" + hex(tb, 168);
            }
            v.expect = rf;
            if (!fill(cc, v)) return 2;
            vs.push_back(v);
            std::fprintf(stderr, "tickets: row %d at nonce %llu, %zu tickets\n", i, (unsigned long long)v.nonce, found);
        }
    } else if (cls == "keyed") {
        // Section 0.9 (igneumhash bit 10 only): 32 rows at height 1 under the test secret sk = BLAKE3("IgneumHash/R0A/test-sk" ||
        // header) (the trace's secret), each with its keyed_rounds shares, so a worker proves its keyed path and a verifier its replay
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "keyed is an igneumhash class\n"); return 2; }
        ignh_params kp; if (ignh_params_parse(g_params, &kp) != IGNPOW_OK || !(kp.features & 1024)) { std::fprintf(stderr, "keyed needs a bit 10 document\n"); return 2; }
        ignpow_ctx* c = cc.get(1);
        if (!c) return 2;
        // IGNPOW_KEYED_COUNT (default 32, at most 256): the outside review's closure reading (11 Oct 2026) wants 100 or more rows per
        // class; every row carries the messages m_i as well (ignh_hash_keyed_m), the proof pi's input
        int nk = 32; if (const char* e = std::getenv("IGNPOW_KEYED_COUNT")) { nk = std::atoi(e); if (nk < 1) nk = 1; if (nk > 256) nk = 256; }
        for (int i = 0; i < nk; ++i) {
            vec v; v.height = 1; v.header = rand_header(); v.nonce = r.next();
            uint8_t h[32], sk[32], mix[32], out[32]; unhex(v.header, h, 32);
            std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32), m(size_t(ignh_keyed_count(&kp)) * 32);
            ignh_test_sk(h, sk);
            if (ignh_hash_keyed_m(c, 1, h, v.nonce, sk, shares.data(), mix, out, m.data()) != IGNPOW_OK) return 2;
            v.min_hash = hex(sk, 32); v.share_target = hex(shares.data(), shares.size()); v.mix = hex(mix, 32); v.hash = hex(out, 32); v.boundary = hex(m.data(), m.size());
            vs.push_back(v);
        }
    } else if (cls == "params-rule" || cls == "registers-rule") {
        // The keyed document's deterministic refusals at the parameter rule (igneumhash, a bit 10 document as the base; the
        // auditor's closure reading of 11 Oct 2026: at least 100 vectors per rejection class). params-rule: keyed_rounds 0 and
        // 65 to 163 under bit 10 (101 refused), bit 10 without keyed_rounds, keyed_rounds without bit 10, bits 8 and 9
        // together; registers-rule: registers 1 to 128 outside {8, 16, 32, 64} (124 refused), form (a) bit 11 at registers 8
        // and 32 (the narrow select needs 16). Each class carries its ok controls (the base and the admitted values).
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "%s is an igneumhash class\n", cls.c_str()); return 2; }
        ignh_params kp; if (ignh_params_parse(g_params, &kp) != IGNPOW_OK || !(kp.features & 1024)) { std::fprintf(stderr, "%s needs a bit 10 document\n", cls.c_str()); return 2; }
        auto doc = [&](uint32_t blocks, uint32_t registers, uint32_t features, long keyed_rounds) {
            std::string s = "{\"version\": 8, \"dataset_mib\": " + std::to_string(kp.dataset_bytes >> 20) + ", \"scratch_kib\": " + std::to_string(kp.scratch_bytes >> 10) +
                ", \"rounds\": " + std::to_string(kp.rounds) + ", \"cache_mib\": " + std::to_string(kp.cache_bytes >> 20) + ", \"parents\": " + std::to_string(kp.parents) +
                ", \"item_bytes\": " + std::to_string(kp.item_bytes) + ", \"blocks\": " + std::to_string(blocks) + ", \"registers\": " + std::to_string(registers) +
                ", \"features\": " + std::to_string(features);
            if (keyed_rounds >= 0) s += ", \"keyed_rounds\": " + std::to_string(keyed_rounds);
            return s + "}";
        };
        auto push = [&](const std::string& d, const char* expect, const std::string& note) {
            vec v; v.height = 0; v.boundary = hex(reinterpret_cast<const uint8_t*>(d.data()), d.size()); v.expect = expect; v.share_target = note; vs.push_back(v);
        };
        const uint32_t fb = kp.features;  // the base's features (bit 10 set)
        if (cls == "params-rule") {
            push(doc(kp.blocks, kp.registers, fb, kp.keyed_rounds), "ok", "the base document");
            for (long s : {2L, 4L, 14L, 64L}) push(doc(kp.blocks, kp.registers, fb, s), "ok", "keyed_rounds " + std::to_string(s) + " admitted");
            push(doc(kp.blocks, kp.registers, fb, 0), "params", "keyed_rounds 0 under bit 10");
            for (long s = 65; s <= 163; ++s) push(doc(kp.blocks, kp.registers, fb, s), "params", "keyed_rounds " + std::to_string(s) + " over 64");
            push(doc(kp.blocks, kp.registers, fb, -1), "params", "bit 10 without keyed_rounds");
            push(doc(kp.blocks, kp.registers, fb & ~1024u, kp.keyed_rounds), "params", "keyed_rounds without bit 10");
            push(doc(kp.blocks, kp.registers, fb | 256 | 512, kp.keyed_rounds), "params", "bits 8 and 9 together");
        } else {
            for (uint32_t x : {8u, 16u, 32u, 64u}) push(doc(kp.blocks, x, fb, kp.keyed_rounds), "ok", "registers " + std::to_string(x) + " admitted");
            for (uint32_t x = 1; x <= 128; ++x) {
                if (x == 8 || x == 16 || x == 32 || x == 64) continue;
                push(doc(kp.blocks, x, fb, kp.keyed_rounds), "params", "registers " + std::to_string(x) + " not 8, 16, 32 or 64");
            }
            push(doc(kp.blocks, 8, (fb & ~4096u) | 2048, kp.keyed_rounds), "params", "form (a) bit 11 at registers 8 (needs 16)");
            push(doc(kp.blocks, 32, (fb & ~4096u) | 2048, kp.keyed_rounds), "params", "form (a) bit 11 at registers 32 (needs 16)");
            push(doc(kp.blocks, 16, (fb & ~4096u) | 2048, kp.keyed_rounds), "ok", "form (a) bit 11 at registers 16");
        }
    } else if (cls == "key-forms") {
        // The keyed entry's deterministic refusals on the document (the outside review's closure reading): for each of 100 nonces at
        // height 1, four forms: keyless (ignpow_hash on a keyed document: IGNPOW_E_ARG and the outputs untouched), null-shares
        // (ignh_hash_keyed with sk and shares NULL: IGNPOW_E_ARG), wrong-sk (the prover's path under sk with its first byte
        // flipped: the digest differs, a verify against the row's mix reads IGNPOW_E_MIX), flipped-share (the replay with the
        // second share's ninth byte flipped: the digest differs). The row carries the true mix and hash.
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "key-forms is an igneumhash class\n"); return 2; }
        ignh_params kp; if (ignh_params_parse(g_params, &kp) != IGNPOW_OK || !(kp.features & 1024)) { std::fprintf(stderr, "key-forms needs a bit 10 document\n"); return 2; }
        ignpow_ctx* c = cc.get(1);
        if (!c) return 2;
        int nk = 100; if (const char* e = std::getenv("IGNPOW_KEYED_COUNT")) { nk = std::atoi(e); if (nk < 1) nk = 1; if (nk > 256) nk = 256; }
        for (int i = 0; i < nk; ++i) {
            vec b; b.height = 1; b.header = rand_header(); b.nonce = r.next();
            uint8_t h[32], sk[32], mix[32], out[32]; unhex(b.header, h, 32);
            std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32);
            ignh_test_sk(h, sk);
            if (ignh_hash_keyed(c, 1, h, b.nonce, sk, shares.data(), mix, out) != IGNPOW_OK) return 2;
            b.mix = hex(mix, 32); b.hash = hex(out, 32);
            { vec v = b; v.share_target = "keyless"; v.expect = "arg"; vs.push_back(v); }
            { vec v = b; v.share_target = "null-shares"; v.expect = "arg"; vs.push_back(v); }
            { vec v = b; v.share_target = "wrong-sk"; v.expect = "mix"; vs.push_back(v); }
            { vec v = b; v.share_target = "flipped-share"; v.expect = "mix"; vs.push_back(v); }
        }
    } else if (cls == "dataset") {
        // Per epoch the classes use: E, D, the diagnostic digest over the whole dataset and the build time (full contexts).
        if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "dataset is an igneumhash class\n"); return 2; }
        const uint64_t heights[] = {0, L, 2 * L, kMatched, kLive};
        for (uint64_t h : heights) {
            int st = 0;
            ignpow_ctx* c = ignpow_ctx_create(algo.c_str(), g_params, h, 1, &st);
            if (!c) { std::fprintf(stderr, "full context at %llu: %s\n", (unsigned long long)h, reason_name(st)); return 2; }
            uint8_t E[32], D[32], cd[32], dg[32]; uint64_t ms = 0; int has = 0;
            ignh_dataset_info(c, E, D, cd, dg, &ms, &has);
            vec v; v.height = h; v.ctx_height = ignpow_ctx_epoch(c); v.mix = hex(E, 32); v.hash = hex(D, 32); v.min_hash = hex(dg, 32); v.expect = hex(cd, 32);
            v.nonce_count = ignpow_dataset_bytes(algo.c_str(), g_params, h, &st); v.shares = ms;
            std::fprintf(stderr, "dataset epoch %llu: %llu bytes, dataset build %llu ms\n", (unsigned long long)v.ctx_height, (unsigned long long)v.nonce_count, (unsigned long long)ms);
            ignpow_ctx_free(c);
            vs.push_back(v);
        }
    } else {
        std::fprintf(stderr, "unknown class %s\n", cls.c_str());
        return 2;
    }
    write_file(out, algo, cls, seed, vs);
    std::printf("wrote %zu %s vectors to %s\n", vs.size(), cls.c_str(), out.c_str());
    return 0;
}

int check(const std::string& algo_arg, const std::string& path) {
    std::string algo, cls; std::vector<vec> vs;
    if (!read_file(path, algo, cls, vs)) { std::fprintf(stderr, "cannot read %s: %s\n", path.c_str(), g_read_reason.c_str()); return 2; }
    if (algo != algo_arg) { std::fprintf(stderr, "cannot check %s: the file is for %s, the check was asked for %s\n", path.c_str(), algo.c_str(), algo_arg.c_str()); return 2; }
    // the set's own params record is the document the check runs on: a file whose record differs from the --params
    // given is another tag's set (the ladder's chain_id lesson of 16:16 UK) and is refused, never read as a pass
    if (algo == IGNPOW_ALGO_IGNEUMHASH && !g_file_params.empty() && g_file_params != "null") {
        const std::string mine = complete_params(algo);
        if (mine != g_file_params) { std::fprintf(stderr, "cannot check %s: the file's params record is %s, the check was given %s\n", path.c_str(), g_file_params.c_str(), mine.c_str()); return 2; }
    }
    ctx_cache cc; cc.algo = algo;
    size_t bad = 0;
    if (cls == "fp") {
        const long long rows = check_fp(path, bad);
        if (rows < 0) { std::fprintf(stderr, "cannot read %s: %s\n", path.c_str(), g_read_reason.c_str()); return 2; }
        if (rows == 0) { std::fprintf(stderr, "cannot check %s: an fp file with no rows\n", path.c_str()); return 2; }
        std::printf("%s fp: %lld rows, %zu mismatches\n", algo.c_str(), rows, bad);
        return bad ? 1 : 0;
    }
    if (vs.empty()) { std::fprintf(stderr, "cannot check %s: a %s file with no rows is not a pass\n", path.c_str(), cls.c_str()); return 2; }
    if (cls == "malformed") {
        for (const vec& v : vs) {
            ignpow_ctx* c = cc.get(v.ctx_height);
            uint8_t h[32], mix[32], b[32];
            unhex(v.header, h, 32); unhex(v.mix, mix, 32); unhex(v.boundary, b, 32);
            int st;
            if (!v.share_target.empty()) {
                // a keyed document: the verifier's replay from the row's shares, then the same order as ignpow_verify
                // (the height and epoch refusals from the replay, the mix before the boundary)
                std::vector<uint8_t> shares(v.share_target.size() / 2);
                uint8_t mix2[32], out2[32];
                if (!unhex(v.share_target, shares.data(), shares.size())) { ++bad; std::printf("MISMATCH keyed_shares\n"); continue; }
                st = ignh_hash_keyed(c, v.height, h, v.nonce, nullptr, shares.data(), mix2, out2);
                if (st == IGNPOW_OK) st = std::memcmp(mix2, mix, 32) != 0 ? IGNPOW_E_MIX : (std::memcmp(out2, b, 32) > 0 ? IGNPOW_E_BOUNDARY : IGNPOW_OK);
            } else {
                st = ignpow_verify(c, v.height, h, v.nonce, mix, b);
            }
            if (st != reason_code(v.expect)) { ++bad; std::printf("MISMATCH height %llu expect %s got %s\n", (unsigned long long)v.height, v.expect.c_str(), reason_name(st)); }
        }
    } else if (cls == "tickets") {
        if (algo != IGNPOW_ALGO_IGNEUM_GRAPH && algo != IGNPOW_ALGO_IGNEUM_GRAPH28 && algo != IGNPOW_ALGO_IGNEUM_GRAPH28K) { std::fprintf(stderr, "tickets is an igneum-graph class\n"); return 2; }
        if (algo == IGNPOW_ALGO_IGNEUM_GRAPH28) { g_graph_key = igneum_graph28_key; g_graph_verify = igneum_graph28_verify_ticket; g_graph_solve = igneum_graph28_solve; g_graph_edges = 1u << 28; }
        if (algo == IGNPOW_ALGO_IGNEUM_GRAPH28K) { g_graph_key = igneum_graph28k_key; g_graph_verify = igneum_graph28k_verify_ticket; g_graph_solve = igneum_graph28k_solve; g_graph_edges = 1u << 28; }
        uint64_t chain_id = 0; { std::string ps = g_file_params.empty() || g_file_params == "null" ? (g_params ? g_params : "") : g_file_params; const size_t i = ps.find("\"chain_id\":"); if (i == std::string::npos) { std::fprintf(stderr, "tickets: no chain_id\n"); return 2; } chain_id = std::strtoull(ps.c_str() + i + 11, nullptr, 10); }
        size_t rows = 0, tickets = 0, refusals = 0;
        for (const vec& v : vs) {
            ++rows; uint8_t h[32]; unhex(v.header, h, 32);
            uint8_t key[32]; g_graph_key(chain_id, v.height, h, v.nonce, key, nullptr);
            if (hex(key, 32) != v.min_hash) { ++bad; std::printf("MISMATCH graph_key at nonce %llu\n", (unsigned long long)v.nonce); }
            const size_t n = v.share_target.size() / 336;
            if (n != v.shares || n == 0) { ++bad; std::printf("MISMATCH n_tickets at nonce %llu\n", (unsigned long long)v.nonce); continue; }
            for (size_t s = 0; s < n; ++s) {
                uint8_t tb[168]; unhex(v.share_target.substr(336 * s, 336), tb, 168); uint32_t e[42]; for (int i = 0; i < 42; ++i) e[i] = uint32_t(tb[4 * i]) | (uint32_t(tb[4 * i + 1]) << 8) | (uint32_t(tb[4 * i + 2]) << 16) | (uint32_t(tb[4 * i + 3]) << 24);
                uint8_t th[32]; const int st = g_graph_verify(chain_id, v.height, h, v.nonce, e, th); ++tickets;
                if (st != IGNPOW_OK) { ++bad; std::printf("MISMATCH ticket %zu at nonce %llu refused (%d)\n", s, (unsigned long long)v.nonce, st); }
                if (s == 0 && hex(th, 32) != v.hash) { ++bad; std::printf("MISMATCH ticket_hash at nonce %llu\n", (unsigned long long)v.nonce); }
            }
            size_t p = 0; while (p < v.expect.size()) { size_t q = v.expect.find(';', p); if (q == std::string::npos) q = v.expect.size(); const std::string item = v.expect.substr(p, q - p); p = q + 1; const size_t c = item.find(':'); if (c == std::string::npos) continue;
                uint8_t tb[168]; if (!unhex(item.substr(c + 1), tb, 168)) { ++bad; continue; } uint32_t e[42]; for (int i = 0; i < 42; ++i) e[i] = uint32_t(tb[4 * i]) | (uint32_t(tb[4 * i + 1]) << 8) | (uint32_t(tb[4 * i + 2]) << 16) | (uint32_t(tb[4 * i + 3]) << 24);
                uint64_t rn = v.nonce; { const std::string nm = item.substr(0, c); const size_t at = nm.find('@'); if (at != std::string::npos) rn = std::strtoull(nm.c_str() + at + 1, nullptr, 10); }  // unkeyed@nonce: the item's own nonce
                ++refusals; if (g_graph_verify(chain_id, v.height, h, rn, e, nullptr) == IGNPOW_OK) { ++bad; std::printf("MISMATCH refusal %s accepted at nonce %llu\n", item.substr(0, c).c_str(), (unsigned long long)rn); } }
            ignpow_ctx* c = cc.get(v.height); uint8_t mix[32], out[32];
            if (!c || ignpow_hash(c, v.height, h, v.nonce, mix, out) != IGNPOW_OK || hex(mix, 32) != v.mix || hex(out, 32) != v.hash) { ++bad; std::printf("MISMATCH abi hash at nonce %llu\n", (unsigned long long)v.nonce); }
        }
        std::printf("%s tickets: %zu rows, %zu tickets, %zu refusals, %zu mismatches\n", algo.c_str(), rows, tickets, refusals, bad);
        return bad ? 1 : 0;
    } else if (cls == "keyed") {
        // the prover's path under the row's test secret must give the row's shares, mix and hash; the verifier's replay from the
        // row's shares alone must give the same mix and hash (section 0.9)
        ignh_params kp; if (ignh_params_parse(g_params, &kp) != IGNPOW_OK || !(kp.features & 1024)) { std::fprintf(stderr, "keyed needs a bit 10 document\n"); return 2; }
        for (const vec& v : vs) {
            ignpow_ctx* c = cc.get(v.height);
            if (!c) return 2;
            uint8_t h[32], sk[32], mix[32], out[32]; unhex(v.header, h, 32); unhex(v.min_hash, sk, 32);
            std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32), given(size_t(ignh_keyed_count(&kp)) * 32);
            if (!unhex(v.share_target, given.data(), given.size())) { ++bad; std::printf("MISMATCH nonce %llu: shares field\n", (unsigned long long)v.nonce); continue; }
            int st = ignh_hash_keyed(c, v.height, h, v.nonce, sk, shares.data(), mix, out);
            if (st != IGNPOW_OK || shares != given || hex(mix, 32) != v.mix || hex(out, 32) != v.hash) { ++bad; std::printf("MISMATCH prover nonce %llu\n", (unsigned long long)v.nonce); continue; }
            st = ignh_hash_keyed(c, v.height, h, v.nonce, nullptr, given.data(), mix, out);
            if (st != IGNPOW_OK || hex(mix, 32) != v.mix || hex(out, 32) != v.hash) { ++bad; std::printf("MISMATCH replay nonce %llu\n", (unsigned long long)v.nonce); continue; }
            if (!v.boundary.empty()) {  // the messages m_i, when the row carries them: the replay's m_out must equal them byte for byte
                std::vector<uint8_t> m(size_t(ignh_keyed_count(&kp)) * 32);
                st = ignh_hash_keyed_m(c, v.height, h, v.nonce, nullptr, given.data(), mix, out, m.data());
                if (st != IGNPOW_OK || hex(m.data(), m.size()) != v.boundary) { ++bad; std::printf("MISMATCH m nonce %llu\n", (unsigned long long)v.nonce); }
            }
        }
    } else if (cls == "params-rule" || cls == "registers-rule") {
        for (const vec& v : vs) {
            std::vector<uint8_t> d(v.boundary.size() / 2);
            if (!unhex(v.boundary, d.data(), d.size())) { ++bad; std::printf("MISMATCH doc_hex\n"); continue; }
            std::string text(d.begin(), d.end());
            ignh_params p; int st = ignh_params_parse(text.c_str(), &p);
            if (st == IGNPOW_OK) { int st2 = 0; ignpow_dataset_bytes(algo.c_str(), text.c_str(), 1, &st2); if (st2 != IGNPOW_OK) st = st2; }  // a rule applied past the parse
            const int want = v.expect == "ok" ? IGNPOW_OK : IGNPOW_E_PARAMS;
            if (st != want) { ++bad; std::printf("MISMATCH %s: expect %s got %s\n", v.share_target.c_str(), v.expect.c_str(), reason_name(st)); }
        }
    } else if (cls == "key-forms") {
        ignh_params kp; if (ignh_params_parse(g_params, &kp) != IGNPOW_OK || !(kp.features & 1024)) { std::fprintf(stderr, "key-forms needs a bit 10 document\n"); return 2; }
        for (const vec& v : vs) {
            ignpow_ctx* c = cc.get(v.height);
            if (!c) return 2;
            uint8_t h[32], sk[32], mix[32], out[32]; unhex(v.header, h, 32);
            std::vector<uint8_t> shares(size_t(ignh_keyed_count(&kp)) * 32);
            ignh_test_sk(h, sk);
            const std::string& form = v.share_target;
            bool pass = false;
            if (form == "keyless") {
                std::memset(mix, 0xA5, 32); std::memset(out, 0xA5, 32);
                int st = ignpow_hash(c, v.height, h, v.nonce, mix, out);
                bool untouched = true; for (int i = 0; i < 32; ++i) untouched &= (mix[i] == 0xA5 && out[i] == 0xA5);
                pass = st == IGNPOW_E_ARG && untouched;
            } else if (form == "null-shares") {
                pass = ignh_hash_keyed(c, v.height, h, v.nonce, nullptr, nullptr, mix, out) == IGNPOW_E_ARG;
            } else if (form == "wrong-sk") {
                sk[0] ^= 1;
                pass = ignh_hash_keyed(c, v.height, h, v.nonce, sk, shares.data(), mix, out) == IGNPOW_OK && hex(mix, 32) != v.mix && hex(out, 32) != v.hash;
            } else if (form == "flipped-share") {
                if (ignh_hash_keyed(c, v.height, h, v.nonce, sk, shares.data(), mix, out) != IGNPOW_OK || hex(mix, 32) != v.mix) { ++bad; std::printf("MISMATCH true row nonce %llu\n", (unsigned long long)v.nonce); continue; }
                shares[std::min<size_t>(40, shares.size() - 1)] ^= 1;
                pass = ignh_hash_keyed(c, v.height, h, v.nonce, nullptr, shares.data(), mix, out) == IGNPOW_OK && hex(mix, 32) != v.mix && hex(out, 32) != v.hash;
            }
            if (!pass) { ++bad; std::printf("MISMATCH %s nonce %llu\n", form.c_str(), (unsigned long long)v.nonce); }
        }
    } else if (cls == "dataset") {
        for (const vec& v : vs) {
            int st = 0;
            ignpow_ctx* c = ignpow_ctx_create(algo.c_str(), g_params, v.height, 1, &st);
            if (!c) return 2;
            uint8_t E[32], D[32], cd[32], dg[32]; uint64_t ms = 0; int has = 0;
            ignh_dataset_info(c, E, D, cd, dg, &ms, &has);
            std::fprintf(stderr, "dataset epoch %llu: dataset build %llu ms\n", (unsigned long long)v.ctx_height, (unsigned long long)ms);
            // files older than the version 1 landing record no cache_digest; the digest is compared only when recorded
            if (hex(E, 32) != v.mix || hex(D, 32) != v.hash || hex(dg, 32) != v.min_hash || (!v.expect.empty() && hex(cd, 32) != v.expect)) { ++bad; std::printf("MISMATCH dataset epoch %llu\n", (unsigned long long)v.ctx_height); }
            ignpow_ctx_free(c);
        }
    } else if (cls == "tails") {
        ignpow_ctx* c = cc.get(1, 1);
        if (!c) return 2;
        for (const vec& v : vs) {
            uint8_t h[32], target[32]; unhex(v.header, h, 32); unhex(v.share_target, target, 32);
            uint8_t best[32]; std::memset(best, 0xff, 32); uint64_t best_n = 0, shares = 0;
            for (uint64_t i = 0; i < v.nonce_count; ++i) {
                uint64_t n = v.nonce + i; uint8_t mix[32], out[32];
                if (row_hash(c, v.height, h, n, mix, out) != IGNPOW_OK) { ++bad; std::printf("MISMATCH tails: hash refused at nonce %llu\n", (unsigned long long)n); break; }  // the keyed document's path under the test secret
                if (std::memcmp(out, target, 32) <= 0) ++shares;
                if (std::memcmp(out, best, 32) < 0) { std::memcpy(best, out, 32); best_n = n; }
            }
            if (best_n != v.min_nonce || hex(best, 32) != v.min_hash || shares != v.shares) { ++bad; std::printf("MISMATCH tails count %llu\n", (unsigned long long)v.nonce_count); }
        }
    } else {
        // transitions: the KAWPOW family binds the height only through the
        // program period (height / 3) and the epoch (height / 7500); inside one
        // period the hash of (header, nonce) is the same at every height, and
        // across a period or epoch boundary it changes. Height binding of a
        // block is the sealing hash's job (it commits to number and parent);
        // the reference asserts the property so no backend and no node relies
        // on the hash function for it. This is the gap the Ravencoin v4.8.0
        // fix closed (DESIGN.md section 2, correction 1).
        std::string prev_hash; uint64_t prev_height = 0; bool have_prev = false;
        for (const vec& v : vs) {
            ignpow_ctx* c = cc.get(v.height);
            if (!c) return 2;
            uint8_t h[32], mix[32], out[32]; unhex(v.header, h, 32);
            int st = row_hash(c, v.height, h, v.nonce, mix, out);
            if (st != IGNPOW_OK || hex(mix, 32) != v.mix || hex(out, 32) != v.hash) { ++bad; std::printf("MISMATCH height %llu nonce %llu\n", (unsigned long long)v.height, (unsigned long long)v.nonce); }
            if (cls == "transitions" && have_prev) {
                // the candidate's own height binding: KAWPOW by the 3-block period and the epoch; FishHash none
                // (one dataset for ever); Autolykos every height (the height is in the element); every other
                // candidate by the epoch alone (IgneumHash: SPEC-R0A-v0.md section 2)
                int st1 = 0, st2 = 0;
                const bool same_epoch = ignpow_epoch_of(algo.c_str(), g_params, v.height, &st1) == ignpow_epoch_of(algo.c_str(), g_params, prev_height, &st2);
                bool same_period;
                if (algo == IGNPOW_ALGO_KAWPOW_094) same_period = (v.height / 3 == prev_height / 3) && same_epoch;
                else if (algo == IGNPOW_ALGO_PROGPOW_EPIC) same_period = (v.height / 50 == prev_height / 50) && same_epoch;  // ProgPoW 0.9.2's period 50
                else if (algo == IGNPOW_ALGO_FISHHASH_1) same_period = true;
                else if (algo == IGNPOW_ALGO_CUCKAROO29_TARI || algo == IGNPOW_ALGO_QPOW_QUANTUS) same_period = true;  // the hash reads no height; the 2^24 epoch is the harness's
                else if (algo == IGNPOW_ALGO_AUTOLYKOS_2) same_period = v.height == prev_height;
                else if (algo == IGNPOW_ALGO_IGNEUM_GRAPH || algo == IGNPOW_ALGO_IGNEUM_GRAPH28 || algo == IGNPOW_ALGO_IGNEUM_GRAPH28K) same_period = v.height == prev_height;  // the graph key binds the height (GRAPH-COORDINATE.md section 2); the rule was missing since the 29-bit cut (its transitions check read BROKEN, owned 14 Oct)
                else if (algo == IGNPOW_ALGO_IGNEUMHASH && g_params && (std::string(g_params).find("\"version\": 21") != std::string::npos || std::string(g_params).find("\"version\": 23") != std::string::npos || std::string(g_params).find("\"version\": 24") != std::string::npos || std::string(g_params).find("\"version\": 25") != std::string::npos) && std::string(g_params).find("\"period_blocks\":") != std::string::npos) {
                    // version 21's chain form (F-141): the period of blocks binds the programme beside the epoch's dataset
                    const std::string gp(g_params); const uint64_t pb = std::strtoull(gp.c_str() + gp.find("\"period_blocks\":") + 16, nullptr, 10);
                    same_period = same_epoch && (v.height / (pb ? pb : 1) == prev_height / (pb ? pb : 1));
                }
                else if (algo == IGNPOW_ALGO_IGNEUMHASH && g_params && (std::string(g_params).find("\"version\": 16") != std::string::npos || std::string(g_params).find("\"version\": 17") != std::string::npos || std::string(g_params).find("\"version\": 18") != std::string::npos)) {
                    // version 16 (the static-fabric coordinate): no dataset, so the hash binds to the key period alone, not the epoch
                    uint64_t pb = 300;
                    const std::string gp(g_params);
                    const size_t k = gp.find("\"period_blocks\":");
                    if (k != std::string::npos) pb = std::strtoull(gp.c_str() + k + 16, nullptr, 10);
                    if (pb == 0) pb = 1;
                    same_period = v.height / pb == prev_height / pb;
                    if (gp.find("\"dataset_mib\":") != std::string::npos) same_period = same_period && same_epoch;  // revision 19 (memory fused) and 17b (the keyed texture): the dataset binds the hash to the epoch too
                }
                else if (algo == IGNPOW_ALGO_IGNEUMHASH && g_params && (std::string(g_params).find("\"version\": 9") != std::string::npos || std::string(g_params).find("\"version\": 10") != std::string::npos || std::string(g_params).find("\"version\": 11") != std::string::npos || std::string(g_params).find("\"version\": 12") != std::string::npos || std::string(g_params).find("\"version\": 13") != std::string::npos || std::string(g_params).find("\"version\": 14") != std::string::npos || std::string(g_params).find("\"version\": 15") != std::string::npos)) {
                    // version 9 (SPEC-R0D-v0.md): the programme's period binds the hash to the height beside the epoch
                    uint64_t pb = 1;
                    const std::string gp(g_params);
                    const size_t k = gp.find("\"period_blocks\":");
                    if (k != std::string::npos) pb = std::strtoull(gp.c_str() + k + 16, nullptr, 10);
                    if (pb == 0) pb = 1;
                    same_period = same_epoch && (v.height / pb == prev_height / pb);
                } else same_period = same_epoch;
                const bool same_hash = prev_hash == v.hash;
                if (same_period != same_hash) {
                    ++bad;
                    std::printf("PERIOD PROPERTY BROKEN between heights %llu and %llu: same period %d, same hash %d\n",
                                (unsigned long long)prev_height, (unsigned long long)v.height, same_period, same_hash);
                }
            }
            prev_hash = v.hash; prev_height = v.height; have_prev = true;
        }
    }
    std::printf("%s %s: %zu vectors, %zu mismatches\n", algo.c_str(), cls.c_str(), vs.size(), bad);
    return bad ? 1 : 0;
}

int bench_full(const std::string& algo, uint64_t height, int n) {
    int st = 0;
    auto t0 = std::chrono::steady_clock::now();
    ignpow_ctx* light = ignpow_ctx_create(algo.c_str(), g_params, height, 0, &st);
    if (!light) { std::fprintf(stderr, "light context: %s\n", reason_name(st)); return 2; }
    auto t1 = std::chrono::steady_clock::now();
    ignpow_ctx* full = ignpow_ctx_create(algo.c_str(), g_params, height, 1, &st);
    if (!full) { std::fprintf(stderr, "full context: %s\n", reason_name(st)); return 2; }
    auto t2 = std::chrono::steady_clock::now();
    rng64 r(height ^ 0x1234);
    double us_light = 0, us_full = 0;
    size_t mism = 0;
    for (int i = 0; i < n; ++i) {
        uint8_t h[32], m1[32], o1[32], m2[32], o2[32]; r.bytes(h, 32); uint64_t nonce = r.next();
        auto a = std::chrono::steady_clock::now(); ignpow_hash(light, height, h, nonce, m1, o1);
        auto b = std::chrono::steady_clock::now(); ignpow_hash(full, height, h, nonce, m2, o2);
        auto c = std::chrono::steady_clock::now();
        us_light += std::chrono::duration<double, std::micro>(b - a).count();
        us_full += std::chrono::duration<double, std::micro>(c - b).count();
        if (std::memcmp(m1, m2, 32) || std::memcmp(o1, o2, 32)) ++mism;
    }
    const double ratio = us_full > 0 ? us_light / us_full : 0;
    std::printf("bench-full %s height %llu: light %.1f us/hash, full %.1f us/hash (dataset resident, built in %lld ms), light/full ratio %.1f, light-full mismatches %zu (n=%d)%s\n",
                algo.c_str(), (unsigned long long)height, us_light / n, us_full / n,
                (long long)std::chrono::duration_cast<std::chrono::milliseconds>(t2 - t1).count(), ratio, mism, n,
                (mism == 0 && ratio >= 4.0) ? "" : "  F-12 CHECK FAILED: the full path must read the resident dataset and agree with the light path");
    (void)t0;
    ignpow_ctx_free(light); ignpow_ctx_free(full);
    return (mism == 0 && ratio >= 4.0) ? 0 : 1;
}

// the security review's traces (tests/igneumhash/me01.py and pr03.py): a meta line, then one line per access and per lane-round.
int addrtrace(const std::string& algo, uint64_t height, uint64_t count, uint64_t seed, const std::string& addr_path, const std::string& lane_path) {
    if (algo != IGNPOW_ALGO_IGNEUMHASH) { std::fprintf(stderr, "addrtrace is an igneumhash class\n"); return 2; }
    ctx_cache cc; cc.algo = algo;
    ignpow_ctx* c = cc.get(height);
    if (!c) return 2;
    int st = 0;
    const uint64_t items = ignpow_dataset_bytes(algo.c_str(), g_params, height, &st) / 128;
    ignh_params p; ignh_params_parse(g_params, &p);
    FILE* fa = std::fopen(addr_path.c_str(), "w");
    FILE* fl = std::fopen(lane_path.c_str(), "w");
    if (!fa || !fl) { std::fprintf(stderr, "cannot open the trace files\n"); return 2; }
    std::fprintf(fa, "{\"meta\": {\"algo\": \"%s\", \"dataset_items\": %llu, \"seed\": \"%llu\", \"height\": %llu, \"rounds\": %u, \"lanes\": %u, \"params\": %s, \"generator\": \"%s\"}}\n",
                 algo.c_str(), (unsigned long long)items, (unsigned long long)seed, (unsigned long long)height, p.rounds, p.lanes, g_params ? g_params : "null", ignpow_build_string());
    std::fprintf(fl, "{\"meta\": {\"algo\": \"%s\", \"lanes\": %u, \"rounds\": %u, \"seed\": \"%llu\", \"height\": %llu, \"params\": %s, \"generator\": \"%s\"}}\n",
                 algo.c_str(), p.lanes, p.rounds, (unsigned long long)seed, (unsigned long long)height, g_params ? g_params : "null", ignpow_build_string());
    rng64 r(seed);
    for (uint64_t i = 0; i < count; ++i) {
        uint8_t h[32]; r.bytes(h, 32);
        const uint64_t nonce = r.next();
        if (ignh_access_trace(c, height, h, nonce, fa, fl) != IGNPOW_OK) return 2;
    }
    std::fclose(fa); std::fclose(fl);
    std::printf("wrote traces of %llu hashes at height %llu to %s and %s\n", (unsigned long long)count, (unsigned long long)height, addr_path.c_str(), lane_path.c_str());
    return 0;
}

int bench(const std::string& algo, uint64_t height, int n, bool full) {
    int st = 0;
    auto t0 = std::chrono::steady_clock::now();
    ignpow_ctx* c = ignpow_ctx_create(algo.c_str(), g_params, height, full ? 1 : 0, &st);
    auto build_ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count();
    if (!c) { std::fprintf(stderr, "context: %s\n", reason_name(st)); return 2; }
    uint64_t cache = ignpow_cache_bytes(algo.c_str(), g_params, height, &st);
    uint64_t dataset = ignpow_dataset_bytes(algo.c_str(), g_params, height, &st);
    rng64 r(height);
    std::vector<double> us;
    uint8_t ones[32]; std::memset(ones, 0xff, 32);
    // a keyed igneumhash document (section 0.9): the proof is made under the test secret and the timed verify is the
    // node's form, the replay from the carried shares (ignh_hash_keyed with sk NULL) then the mix and boundary compares;
    // the keyless ignpow_verify refuses a keyed document by rule (11 Oct 2026, the launch document's verifier row)
    ignh_params kp; const bool keyed = algo == IGNPOW_ALGO_IGNEUMHASH && ignh_params_parse(g_params, &kp) == IGNPOW_OK && (kp.features & 1024);
    std::vector<uint8_t> shares(keyed ? size_t(ignh_keyed_count(&kp)) * 32 : 0);
    for (int i = 0; i < n; ++i) {
        uint8_t h[32], mix[32], out[32]; r.bytes(h, 32); uint64_t nonce = r.next();
        if (keyed) {
            uint8_t sk[32]; ignh_test_sk(h, sk);
            if (ignh_hash_keyed(c, height, h, nonce, sk, shares.data(), mix, out) != IGNPOW_OK) { std::fprintf(stderr, "keyed hash refused\n"); ignpow_ctx_free(c); return 2; }
            uint8_t mix2[32], out2[32];
            auto a = std::chrono::steady_clock::now();
            const int vs = ignh_hash_keyed(c, height, h, nonce, nullptr, shares.data(), mix2, out2);
            const bool ok = vs == IGNPOW_OK && std::memcmp(mix2, mix, 32) == 0 && std::memcmp(out2, ones, 32) <= 0;
            auto b = std::chrono::steady_clock::now();
            if (!ok) { std::fprintf(stderr, "keyed replay refused its own proof\n"); ignpow_ctx_free(c); return 2; }
            us.push_back(std::chrono::duration<double, std::micro>(b - a).count());
            continue;
        }
        ignpow_hash(c, height, h, nonce, mix, out);
        auto a = std::chrono::steady_clock::now();
        ignpow_verify(c, height, h, nonce, mix, ones);
        auto b = std::chrono::steady_clock::now();
        us.push_back(std::chrono::duration<double, std::micro>(b - a).count());
    }
    std::sort(us.begin(), us.end());
    std::printf("bench %s height %llu epoch %llu: cache %llu bytes, dataset %llu bytes, %s build %lld ms, "
                "%s verify median %.1f us p99 %.1f us max %.1f us (n=%d, single thread)\n",
                algo.c_str(), (unsigned long long)height, (unsigned long long)ignpow_ctx_epoch(c),
                (unsigned long long)cache, (unsigned long long)dataset, full ? "full" : "cache", (long long)build_ms,
                full ? "full" : "light", us[us.size() / 2], us[(us.size() * 99) / 100], us.back(), n);
    ignpow_ctx_free(c);
    return 0;
}

// ---- the pow-diff adapter (tests/pow-diff/run.py contract) and the fixture writer ----

// Extract "range": [a, b] from a line; returns false when absent.
bool range_of(const std::string& line, uint64_t& start, uint64_t& count) {
    size_t p = line.find("\"range\"");
    if (p == std::string::npos) return false;
    p = line.find('[', p);
    if (p == std::string::npos) return false;
    start = std::strtoull(line.c_str() + p + 1, nullptr, 10);
    size_t q = line.find(',', p);
    if (q == std::string::npos) return false;
    count = std::strtoull(line.c_str() + q + 1, nullptr, 10);
    return true;
}

uint64_t g_epoch_len = 0;  // set by diff() from the candidate

// Height of a vector line: "height" when present, else the first block of "epoch" (epoch x the candidate's epoch length).
uint64_t height_of(const std::string& line) {
    std::string h = field(line, "height");
    if (!h.empty()) return u64(h);
    std::string e = field(line, "epoch");
    return u64(e) * g_epoch_len;
}

// Malformed raw case: [height u64 BE][header 32][nonce u64 BE][mix 32][boundary 32] = 112 bytes,
// evaluated through verify; any other length is reject code arg (7).
int raw_case(ctx_cache& cc, const std::string& rawhex, std::string& mix_out, std::string& hash_out) {
    std::vector<uint8_t> raw(rawhex.size() / 2);
    if (rawhex.size() % 2 || !unhex(rawhex, raw.data(), raw.size())) return IGNPOW_E_ARG;
    if (raw.size() != 112) return IGNPOW_E_ARG;
    uint64_t height = 0, nonce = 0;
    for (int i = 0; i < 8; ++i) { height = (height << 8) | raw[i]; nonce = (nonce << 8) | raw[40 + i]; }
    int st = 0;
    uint64_t epoch = ignpow_epoch_of(cc.algo.c_str(), g_params, height, &st);
    if (st != IGNPOW_OK) return st;
    (void)epoch;
    ignpow_ctx* c = cc.get(height);
    if (!c) return IGNPOW_E_MEMORY;
    uint8_t mix[32], out[32];
    st = ignpow_hash(c, height, raw.data() + 8, nonce, mix, out);
    if (st != IGNPOW_OK) return st;
    st = ignpow_verify(c, height, raw.data() + 8, nonce, raw.data() + 48, raw.data() + 80);
    mix_out = hex(mix, 32); hash_out = hex(out, 32);
    return st;
}

// One vector line evaluated against a (per-thread) context cache; ranges may be
// split: [k0, k1) of the range is this call's share, the caller merges bests.
struct line_result { std::string i; int code = IGNPOW_OK; uint8_t best[32]; uint8_t best_mix[32]; uint64_t best_n = 0; bool ranged = false; uint64_t start = 0, count = 0; std::string evaluated; bool have = false; };

void eval_line(ctx_cache& cc, const std::string& line, uint64_t k0, uint64_t k1, line_result& r) {
    r.i = field(line, "i");
    if (line.find("\"raw\"") != std::string::npos) {
        std::string mix, h;
        r.code = raw_case(cc, field(line, "raw"), mix, h);
        if (r.code == IGNPOW_OK) { unhex(mix, r.best_mix, 32); unhex(h, r.best, 32); r.have = true; }
        return;
    }
    const uint64_t height = height_of(line);
    uint8_t header[32];
    if (!unhex(field(line, "header"), header, 32)) { r.code = IGNPOW_E_ARG; return; }
    r.ranged = range_of(line, r.start, r.count);
    if (!r.ranged) { r.start = u64(field(line, "nonce")); r.count = 1; }
    int st = 0;
    ignpow_epoch_of(cc.algo.c_str(), g_params, height, &st);
    if (st != IGNPOW_OK) { r.code = st; return; }
    ignpow_ctx* c = cc.get(height, 0);  // light contexts only: shared across threads, split ranges stay exact
    if (!c) { r.code = IGNPOW_E_MEMORY; return; }
    if (k1 > r.count) k1 = r.count;
    std::memset(r.best, 0xff, 32);
    for (uint64_t k = k0; k < k1; ++k) {
        const uint64_t n = r.start + k;
        uint8_t mix[32], o[32];
        r.code = ignpow_hash(c, height, header, n, mix, o);
        if (r.code != IGNPOW_OK) return;
        if (!r.have || std::memcmp(o, r.best, 32) < 0) { std::memcpy(r.best, o, 32); std::memcpy(r.best_mix, mix, 32); r.best_n = n; r.have = true; }
        if (r.ranged && r.count <= 4096) r.evaluated += (k ? "," : "") + std::to_string(n);
    }
}

void write_result(std::ofstream& out, const line_result& r) {
    out << "{\"i\": " << r.i << ", \"code\": " << r.code;
    if (r.code == IGNPOW_OK && r.have) {
        out << ", \"mix\": \"" << hex(r.best_mix, 32) << "\", \"digest\": \"" << hex(r.best, 32) << "\"";
        if (r.ranged) {
            out << ", \"best_nonce\": " << r.best_n << ", \"evaluated_count\": " << r.count;
            if (r.count <= 4096) out << ", \"evaluated\": [" << r.evaluated << "]";
            else out << ", \"evaluated\": [" << r.start << ", " << (r.start + r.count - 1) << "]";
        }
    }
    out << "}\n";
}

// The cpu backend runs on every core: lines are dealt round-robin to threads
// (each with its own context cache), and a range over 4096 nonces is split
// across all threads with the bests merged. The output keeps the input order.
int diff(const std::string& algo, const std::string& in_path, const std::string& out_path) {
    g_epoch_len = epoch_len(algo);
    std::ifstream in(in_path);
    if (!in) { std::fprintf(stderr, "cannot read %s\n", in_path.c_str()); return 2; }
    std::vector<std::string> lines;
    std::string line;
    while (std::getline(in, line)) if (line.find('{') != std::string::npos) lines.push_back(line);
    unsigned nt = std::thread::hardware_concurrency();
    if (nt == 0) nt = 4;
    if (nt > 64) nt = 64;
    const char* env = std::getenv("IGNPOW_THREADS");
    if (env) nt = std::max(1u, (unsigned)std::atoi(env));
    std::vector<line_result> results(lines.size());
    std::vector<size_t> singles, bigs;
    for (size_t k = 0; k < lines.size(); ++k) {
        uint64_t s = 0, c = 0;
        if (range_of(lines[k], s, c) && c > 4096) bigs.push_back(k); else singles.push_back(k);
    }
    // Light contexts are read-only once built, so one shared pool serves every
    // thread: the distinct epochs are built first, in parallel, one per thread.
    ctx_cache shared; shared.algo = algo;
    {
        std::vector<uint64_t> heights;
        for (const auto& l : lines) {
            if (l.find("\"raw\"") != std::string::npos) continue;
            int st = 0;
            const uint64_t h = height_of(l);
            const uint64_t e = ignpow_epoch_of(algo.c_str(), g_params, h, &st);
            if (st != IGNPOW_OK) continue;
            bool seen = false;
            for (uint64_t x : heights) if (ignpow_epoch_of(algo.c_str(), g_params, x, &st) == e) { seen = true; break; }
            if (!seen) heights.push_back(h);
        }
        std::vector<ignpow_ctx*> built(heights.size(), nullptr);
        std::vector<std::thread> ts;
        for (size_t k = 0; k < heights.size(); ++k)
            ts.emplace_back([&, k]() { int st = 0; built[k] = ignpow_ctx_create(algo.c_str(), g_params, heights[k], 0, &st); });
        for (auto& th : ts) th.join();
        for (size_t k = 0; k < heights.size(); ++k) {
            int st = 0;
            if (built[k]) shared.items.emplace_back(ignpow_epoch_of(algo.c_str(), g_params, heights[k], &st), built[k]);
        }
    }
    std::vector<ctx_cache*> caches(nt, &shared);
    {
        std::vector<std::thread> ts;
        for (unsigned t = 0; t < nt; ++t)
            ts.emplace_back([&, t]() {
                for (size_t j = t; j < singles.size(); j += nt) eval_line(*caches[t], lines[singles[j]], 0, UINT64_MAX, results[singles[j]]);
            });
        for (auto& th : ts) th.join();
    }
    for (size_t k : bigs) {
        uint64_t s = 0, c = 0;
        range_of(lines[k], s, c);
        std::vector<line_result> parts(nt);
        std::vector<std::thread> ts;
        const uint64_t share = (c + nt - 1) / nt;
        for (unsigned t = 0; t < nt; ++t)
            ts.emplace_back([&, t]() { eval_line(*caches[t], lines[k], t * share, (t + 1) * share, parts[t]); });
        for (auto& th : ts) th.join();
        line_result& r = results[k];
        r = parts[0];
        for (unsigned t = 1; t < nt; ++t) {
            if (parts[t].code != IGNPOW_OK) { r.code = parts[t].code; break; }
            if (parts[t].have && (!r.have || std::memcmp(parts[t].best, r.best, 32) < 0)) { std::memcpy(r.best, parts[t].best, 32); std::memcpy(r.best_mix, parts[t].best_mix, 32); r.best_n = parts[t].best_n; r.have = true; }
        }
    }
    std::ofstream out(out_path);
    for (const auto& r : results) write_result(out, r);
    return 0;
}

// Fixture tree for tests/pow-diff: tests/fixtures/pow/<candidate>/ with the README's formats.
int fixtures(const std::string& algo, const std::string& dir, uint64_t seed) {
    rng64 r(seed);
    ctx_cache cc; cc.algo = algo;
    auto rand_header = [&]() { uint8_t h[32]; r.bytes(h, 32); return hex(h, 32); };
    auto line_for = [&](std::ofstream& o, uint64_t height, const std::string& hdr, uint64_t nonce, const char* extra, const char* source) -> bool {
        ignpow_ctx* c = cc.get(height);
        if (!c) return false;
        uint8_t h[32], mix[32], out[32]; unhex(hdr, h, 32);
        if (ignpow_hash(c, height, h, nonce, mix, out) != IGNPOW_OK) return false;
        o << "{\"header\": \"" << hdr << "\", \"nonce\": " << nonce << ", \"epoch\": " << ignpow_ctx_epoch(c) << ", \"height\": " << height
          << ", \"mix\": \"" << hex(mix, 32) << "\", \"digest\": \"" << hex(out, 32) << "\"" << (extra ? extra : "")
          << (source ? std::string(", \"source\": \"") + source + "\"" : "") << "}\n";
        return true;
    };
    const bool kaw = is_kawpow(algo);
    const uint64_t L = epoch_len(algo);
    if (L == 0) { std::fprintf(stderr, "cannot derive the epoch length of %s\n", algo.c_str()); return 2; }
    // epochs: 0, 1, the matched point, the live epoch and the upstream horizon (24 months of Ravencoin's schedule past epoch 598)
    const uint64_t kLive = kaw ? 4487776ull : 598ull * L + 2776ull % L;
    const uint64_t heights[] = {1, L, 448ull * L, kLive, 738ull * L};
    {
        std::ofstream o(dir + "/known-vectors.jsonl");
        if (kaw) {
            // the upstream KAWPOW vectors, unchanged (progpow_test_vectors.hpp of the vendored tree)
            struct kv { int h; const char* hdr; const char* n; };
            const kv up[] = {
                {0, "0000000000000000000000000000000000000000000000000000000000000000", "0000000000000000"},
                {49, "63155f732f2bf556967f906155b510c917e48e99685ead76ea83f4eca03ab12b", "0000000007073c07"},
                {50, "9e7248f20914913a73d80a70174c331b1d34f260535ac3631d770e656b5dd922", "00000000076e482e"},
                {99, "de37e1824c86d35d154cf65a88de6d9286aec4f7f10c3fc9f0fa1bcc2687188d", "000000003917afab"},
                {29950, "ac7b55e801511b77e11d52e9599206101550144525b5679f2dab19386f23dcce", "005d409dbc23a62a"},
                {29999, "e43d7e0bdc8a4a3f6e291a5ed790b9fa1a0948a2b9e33c844888690847de19f5", "005db5fa4c2a3d03"},
                {30000, "d34519f72c97cae8892c277776259db3320820cb5279a299d0ef1e155e5c6454", "005db8607994ff30"},
                {30049, "8b6ce5da0b06d18db7bd8492d9e5717f8b53e7e098d9fef7886d58a6e913ef64", "005e2e215a8ca2e7"},
                {30050, "c2c46173481b9ced61123d2e293b42ede5a1b323210eb2a684df0874ffe09047", "005e30899481055e"},
                {30099, "ea42197eb2ba79c63cb5e655b8b1f612c5f08aae1a49ff236795a3516d87bc71", "005ea6aef136f88b"},
                {59950, "49e15ba4bf501ce8fe8876101c808e24c69a859be15de554bf85dbc095491bd6", "02ebe0503bd7b1da"},
                {59999, "f5c50ba5c0d6210ddb16250ec3efda178de857b2b1703d8d5403bd0f848e19cf", "02edb6275bd221e3"},
            };
            for (const kv& v : up) if (!line_for(o, v.h, v.hdr, std::strtoull(v.n, nullptr, 16), nullptr, "ravencoin-v4.8.0 progpow_test_vectors.hpp")) return 2;
        }
        // KAWPOW spreads each epoch's lines over seven consecutive heights (every program period in
        // the span); a per-height design (Autolykos) keeps one height per point, since every distinct
        // height is a table rebuild on a device backend
        for (uint64_t h : heights)
            for (int k = 0; k < 210; ++k)
                if (!line_for(o, h + (kaw ? (k % 7) : 0), rand_header(), r.next(), nullptr, nullptr)) return 2;
    }
    {
        std::ofstream o(dir + "/nonce-rollover.jsonl");
        const uint64_t starts[] = {0xFFFFFFFFull - 40, 0x7FFFFFFFFFFFFFFFull - 40, 0xFFFFFFFFFFFFFFFFull - 40};
        for (uint64_t s : starts) {
            std::string hdr = rand_header();
            std::string extra = ", \"range\": [" + std::to_string(s) + ", 80], \"batch\": 16";
            if (!line_for(o, 1, hdr, s, extra.c_str(), nullptr)) return 2;
        }
        const uint64_t singles[] = {0, 1, 0xFFFFFFFFull, 0x100000000ull, 0x8000000000000000ull, 0xFFFFFFFFFFFFFFFFull};
        std::string hdr = rand_header();
        for (uint64_t n : singles) if (!line_for(o, 1, hdr, n, nullptr, nullptr)) return 2;
    }
    {
        std::ofstream o(dir + "/tails.jsonl");
        const uint64_t counts[] = {17, 4099, 65537};
        for (uint64_t cnt : counts) {
            std::string extra = ", \"range\": [" + std::to_string(r.next()) + ", " + std::to_string(cnt) + "], \"batch\": 256";
            uint64_t s = std::strtoull(extra.c_str() + 11, nullptr, 10);
            if (!line_for(o, 1, rand_header(), s, extra.c_str(), nullptr)) return 2;
        }
    }
    {
        std::ofstream o(dir + "/transitions.jsonl");
        const uint64_t hs[] = {0, 1, 2, 3, 5, 6, L - 1, L, L + 1, 448ull * L - 1, 448ull * L, kLive - 1, kLive, 738ull * L - 1, 738ull * L};
        std::string hdr = rand_header();
        uint64_t n = r.next();
        for (uint64_t h : hs) if (!line_for(o, h, hdr, n, nullptr, nullptr)) return 2;
    }
    {
        std::ofstream o(dir + "/header-binding.jsonl");
        for (int k = 0; k < 16; ++k) if (!line_for(o, 1 + k, rand_header(), r.next(), nullptr, nullptr)) return 2;
    }
    {
        // <dir>/malformed/: raw 112-byte cases (candidate-specific: the ok case carries this candidate's mix) and
        // the reject code per file beside the candidate trees (the codes are the ABI's, shared by every candidate).
        const std::string md = dir + "/malformed";
        std::string hdr = rand_header();
        uint8_t h[32]; unhex(hdr, h, 32);
        uint64_t nonce = r.next();
        ignpow_ctx* c = cc.get(1);
        uint8_t mix[32], out[32];
        ignpow_hash(c, 1, h, nonce, mix, out);
        auto write_case = [&](const char* name, uint64_t height, const uint8_t* mixp, const uint8_t* bound, size_t len) {
            std::vector<uint8_t> raw(112);
            for (int i = 0; i < 8; ++i) { raw[i] = static_cast<uint8_t>(height >> (8 * (7 - i))); raw[40 + i] = static_cast<uint8_t>(nonce >> (8 * (7 - i))); }
            std::memcpy(raw.data() + 8, h, 32); std::memcpy(raw.data() + 48, mixp, 32); std::memcpy(raw.data() + 80, bound, 32);
            raw.resize(len);
            std::ofstream f(md + "/" + name, std::ios::binary);
            f.write(reinterpret_cast<const char*>(raw.data()), (std::streamsize)raw.size());
        };
        uint8_t ones[32], zeros[32] = {0}; std::memset(ones, 0xff, 32);
        write_case("ok-all-ones-boundary.bin", 1, mix, ones, 112);
        write_case("boundary-zero.bin", 1, mix, zeros, 112);
        write_case("mix-zero.bin", 1, zeros, ones, 112);
        write_case("mix-zero-and-boundary-zero.bin", 1, zeros, zeros, 112);
        write_case("height-past-int.bin", 0x80000000ull, mix, ones, 112);
        write_case("short-111-bytes.bin", 1, mix, ones, 111);
        write_case("long-113-bytes.bin", 1, mix, ones, 113);
        write_case("empty.bin", 1, mix, ones, 0);
        std::ofstream rc(dir + "/../reject-codes.json");
        rc << "{\n  \"_codes\": {\"0\": \"ok\", \"1\": \"algo\", \"2\": \"params\", \"3\": \"height\", \"4\": \"epoch\", \"5\": \"boundary\", \"6\": \"mix\", \"7\": \"arg\", \"8\": \"memory\", \"9\": \"vectors\"},\n"
           << "  \"ok-all-ones-boundary.bin\": 0,\n  \"boundary-zero.bin\": 5,\n  \"mix-zero.bin\": 6,\n  \"mix-zero-and-boundary-zero.bin\": 6,\n"
           << "  \"height-past-int.bin\": 3,\n  \"short-111-bytes.bin\": 7,\n  \"long-113-bytes.bin\": 7,\n  \"empty.bin\": 7\n}\n";
    }
    std::printf("fixtures written under %s (seed %llu, %s)\n", dir.c_str(), (unsigned long long)seed, ignpow_build_string());
    return 0;
}

}  // namespace

int main(int argc_in, char** argv_in) {
    // --params '<json>' anywhere (IGNPOW_PARAMS in the environment as the fallback): the candidate's parameter
    // document for every ABI call (ablation variables)
    g_params = std::getenv("IGNPOW_PARAMS");
    if (g_params && !*g_params) g_params = nullptr;
    std::vector<char*> args;
    for (int i = 0; i < argc_in; ++i) {
        if (std::strcmp(argv_in[i], "--params") == 0 && i + 1 < argc_in) { g_params = argv_in[i + 1]; ++i; continue; }
        // the fill class's differential hooks (versions 11 and later): --flip-fill word:bit, --flip-fill-fetched step:bit, --flip-reg lane:reg:bit
        if (std::strcmp(argv_in[i], "--flip-fill") == 0 && i + 1 < argc_in) { unsigned a = 0, b = 0; if (std::sscanf(argv_in[i + 1], "%u:%u", &a, &b) == 2) ignh_debug_flip(1, a, b, 0); ++i; continue; }
        if (std::strcmp(argv_in[i], "--flip-fill-fetched") == 0 && i + 1 < argc_in) { unsigned a = 0, b = 0; if (std::sscanf(argv_in[i + 1], "%u:%u", &a, &b) == 2) ignh_debug_flip(2, a, b, 0); ++i; continue; }
        if (std::strcmp(argv_in[i], "--flip-reg") == 0 && i + 1 < argc_in) { unsigned a = 0, b = 0, cc2 = 0; if (std::sscanf(argv_in[i + 1], "%u:%u:%u", &a, &b, &cc2) == 3) ignh_debug_flip(3, a, b, cc2); ++i; continue; }
        args.push_back(argv_in[i]);
    }
    int argc = static_cast<int>(args.size());
    char** argv = args.data();
    if (argc < 2) { std::fprintf(stderr, "usage: see file header\n"); return 2; }
    std::string cmd = argv[1];
    size_t n = 0;
    int st = ignpow_vectors_check(nullptr, &n);
    std::fprintf(stderr, "%s\nself-test: %s (%zu embedded vectors)\n", ignpow_build_string(), reason_name(st), n);
    if (st != IGNPOW_OK) return 3;
    if (cmd == "selftest") return 0;
    // a set of record is cut by the REFERENCE render: version 22's ignpow_hash takes the fast render (kiln_verify.hpp) unless
    // IGNH_KILN_REFERENCE=1 is set, and a generator on a fast render that lags the reference writes a wrong set (the Kiln verifier
    // seat's fault of 15 Oct 2026: four sets cut through a stale fast path); gen sets it before the first hash
    if (cmd == "gen") setenv("IGNH_KILN_REFERENCE", "1", 1);
    if (cmd == "gen" && argc >= 5) return gen(argv[2], argv[3], argv[4], argc >= 6 ? std::strtoull(argv[5], nullptr, 10) : 20261009ull);
    if (cmd == "check" && argc >= 4) return check(argv[2], argv[3]);
    if (cmd == "addrtrace" && argc >= 8) return addrtrace(argv[2], std::strtoull(argv[3], nullptr, 10), std::strtoull(argv[4], nullptr, 10), std::strtoull(argv[5], nullptr, 10), argv[6], argv[7]);
    if (cmd == "bench" && argc >= 4) return bench(argv[2], std::strtoull(argv[3], nullptr, 10), argc >= 5 ? std::atoi(argv[4]) : 200, argc >= 6 && std::strcmp(argv[5], "full") == 0);
    if (cmd == "diff" && argc >= 7) return diff(argv[2], argv[4], argv[6]);      // diff <algo> --in X --out Y
    if (cmd == "bench-full" && argc >= 4) return bench_full(argv[2], std::strtoull(argv[3], nullptr, 10), argc >= 5 ? std::atoi(argv[4]) : 200);
    if (cmd == "fixtures" && argc >= 4) return fixtures(argv[2], argv[3], argc >= 5 ? std::strtoull(argv[4], nullptr, 10) : 20261009ull);
    std::fprintf(stderr, "usage: see file header\n");
    return 2;
}
