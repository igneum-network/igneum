// ih_params.cpp: see ih_params.h.
#include "ih_params.h"
#include "json.h"
#include <cstring>

namespace igm {

namespace {
const uint32_t IV[8] = {0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u};
const uint8_t PERM[16] = {2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8};
inline uint32_t rotr(uint32_t x, int c) { return (x >> c) | (x << (32 - c)); }
void g(uint32_t *v, int a, int b, int c, int d, uint32_t x, uint32_t y) {
    v[a] = v[a] + v[b] + x; v[d] = rotr(v[d] ^ v[a], 16); v[c] = v[c] + v[d]; v[b] = rotr(v[b] ^ v[c], 12);
    v[a] = v[a] + v[b] + y; v[d] = rotr(v[d] ^ v[a], 8); v[c] = v[c] + v[d]; v[b] = rotr(v[b] ^ v[c], 7);
}
void compress(const uint32_t cv[8], const uint32_t m_in[16], uint64_t t, uint32_t len, uint32_t flags, uint32_t out[16]) {
    uint32_t v[16], m[16];
    for (int i = 0; i < 16; i++) m[i] = m_in[i];
    for (int i = 0; i < 8; i++) v[i] = cv[i];
    v[8] = IV[0]; v[9] = IV[1]; v[10] = IV[2]; v[11] = IV[3]; v[12] = (uint32_t)t; v[13] = (uint32_t)(t >> 32); v[14] = len; v[15] = flags;
    for (int r = 0; r < 7; r++) {
        g(v, 0, 4, 8, 12, m[0], m[1]); g(v, 1, 5, 9, 13, m[2], m[3]); g(v, 2, 6, 10, 14, m[4], m[5]); g(v, 3, 7, 11, 15, m[6], m[7]);
        g(v, 0, 5, 10, 15, m[8], m[9]); g(v, 1, 6, 11, 12, m[10], m[11]); g(v, 2, 7, 8, 13, m[12], m[13]); g(v, 3, 4, 9, 14, m[14], m[15]);
        if (r < 6) { uint32_t p[16]; for (int i = 0; i < 16; i++) p[i] = m[PERM[i]]; for (int i = 0; i < 16; i++) m[i] = p[i]; }
    }
    for (int i = 0; i < 8; i++) { out[i] = v[i] ^ v[i + 8]; out[i + 8] = v[i + 8] ^ cv[i]; }
}
bool pow2(uint64_t x) { return x && !(x & (x - 1)); }
}  // namespace

// ---- the full-message hasher (BLAKE3 reference structure: chunk state, the chaining-value stack, the root) ----
namespace {
const uint32_t F_CHUNK_START = 1u, F_CHUNK_END = 2u, F_PARENT = 4u, F_ROOT = 8u;
void words_of(const uint8_t *b, uint32_t m[16]) { for (int i = 0; i < 16; i++) m[i] = (uint32_t)b[4 * i] | ((uint32_t)b[4 * i + 1] << 8) | ((uint32_t)b[4 * i + 2] << 16) | ((uint32_t)b[4 * i + 3] << 24); }
}
IhB3::IhB3() { for (int i = 0; i < 8; i++) chunk_cv[i] = IV[i]; std::memset(block, 0, 64); }
namespace {
void b3_push_cv(uint32_t cv_stack[54][8], int &stack_len, const uint32_t cv_in[8], uint64_t total_chunks) {
    // the reference's add_chunk_chaining_value: merge while the chunk count's trailing zero bits say two complete subtrees meet
    uint32_t cv[8]; for (int i = 0; i < 8; i++) cv[i] = cv_in[i];
    while ((total_chunks & 1) == 0) {
        uint32_t m[16]; for (int i = 0; i < 8; i++) { m[i] = cv_stack[stack_len - 1][i]; m[8 + i] = cv[i]; }
        uint32_t o[16]; compress(IV, m, 0, 64, F_PARENT, o);
        stack_len--; for (int i = 0; i < 8; i++) cv[i] = o[i];
        total_chunks >>= 1;
    }
    for (int i = 0; i < 8; i++) cv_stack[stack_len][i] = cv[i]; stack_len++;
}
}
void IhB3::update(const uint8_t *in, size_t n) {
    while (n > 0) {
        if (block_len == 64) {
            uint32_t m[16]; words_of(block, m); uint32_t o[16];
            const uint32_t start = blocks_compressed == 0 ? F_CHUNK_START : 0u;
            if (blocks_compressed == 15) {   // the chunk's 16th block is full and more input follows: the chunk is complete
                compress(chunk_cv, m, chunk_counter, 64, start | F_CHUNK_END, o);
                uint32_t cv[8]; for (int i = 0; i < 8; i++) cv[i] = o[i];
                b3_push_cv(cv_stack, stack_len, cv, chunk_counter + 1);
                chunk_counter++; for (int i = 0; i < 8; i++) chunk_cv[i] = IV[i]; blocks_compressed = 0; block_len = 0;
            } else {   // an inner block of the chunk
                compress(chunk_cv, m, chunk_counter, 64, start, o);
                for (int i = 0; i < 8; i++) chunk_cv[i] = o[i]; blocks_compressed++; block_len = 0;
            }
        }
        size_t take = 64 - block_len; if (take > n) take = n;
        std::memcpy(block + block_len, in, take); block_len += (uint32_t)take; in += take; n -= take;
    }
}
void IhB3::finalize(uint8_t out[32]) {
    // the current chunk's last block (possibly short, possibly the whole input) carries CHUNK_END, and ROOT when it is the only
    // chunk; else the stack's parents merge up to the root
    uint32_t m[16]; uint8_t b[64]; std::memset(b, 0, 64); std::memcpy(b, block, block_len); words_of(b, m);
    const uint32_t flags = (blocks_compressed == 0 ? F_CHUNK_START : 0u) | F_CHUNK_END;
    uint32_t o[16];
    if (stack_len == 0) {
        compress(chunk_cv, m, chunk_counter, block_len, flags | F_ROOT, o);
        for (int i = 0; i < 8; i++) { out[4 * i] = (uint8_t)o[i]; out[4 * i + 1] = (uint8_t)(o[i] >> 8); out[4 * i + 2] = (uint8_t)(o[i] >> 16); out[4 * i + 3] = (uint8_t)(o[i] >> 24); }
        return;
    }
    compress(chunk_cv, m, chunk_counter, block_len, flags, o);
    uint32_t cv[8]; for (int i = 0; i < 8; i++) cv[i] = o[i];
    while (stack_len > 0) {
        uint32_t pm[16]; for (int i = 0; i < 8; i++) { pm[i] = cv_stack[stack_len - 1][i]; pm[8 + i] = cv[i]; }
        stack_len--;
        compress(IV, pm, 0, 64, F_PARENT | (stack_len == 0 ? F_ROOT : 0u), o);
        for (int i = 0; i < 8; i++) cv[i] = o[i];
    }
    for (int i = 0; i < 8; i++) { out[4 * i] = (uint8_t)cv[i]; out[4 * i + 1] = (uint8_t)(cv[i] >> 8); out[4 * i + 2] = (uint8_t)(cv[i] >> 16); out[4 * i + 3] = (uint8_t)(cv[i] >> 24); }
}
void ih_blake3_full(const uint8_t *in, size_t n, uint8_t out[32]) { IhB3 h; h.update(in, n); h.finalize(out); }
// the root from the chunks' chaining values computed elsewhere (the card: each chunk's 16 blocks with CHUNK_START and CHUNK_END, no ROOT;
// the last chunk possibly short): the reference's tree over them, ROOT on the final parent. n >= 2 (a one-chunk input is its own root
// with the ROOT flag on the chunk, so the caller hashes it whole).
void ih_blake3_from_chunk_cvs(const uint32_t *cvs, size_t n, uint8_t out[32]) {
    uint32_t cv_stack[54][8]; int stack_len = 0;
    for (size_t i = 0; i + 1 < n; i++) b3_push_cv(cv_stack, stack_len, cvs + 8 * i, (uint64_t)i + 1);
    uint32_t cv[8]; for (int i = 0; i < 8; i++) cv[i] = cvs[8 * (n - 1) + i];
    while (stack_len > 0) {
        uint32_t pm[16]; for (int i = 0; i < 8; i++) { pm[i] = cv_stack[stack_len - 1][i]; pm[8 + i] = cv[i]; }
        stack_len--; uint32_t o[16]; compress(IV, pm, 0, 64, F_PARENT | (stack_len == 0 ? F_ROOT : 0u), o);
        for (int i = 0; i < 8; i++) cv[i] = o[i];
    }
    for (int i = 0; i < 8; i++) { out[4 * i] = (uint8_t)cv[i]; out[4 * i + 1] = (uint8_t)(cv[i] >> 8); out[4 * i + 2] = (uint8_t)(cv[i] >> 16); out[4 * i + 3] = (uint8_t)(cv[i] >> 24); }
}
void ih_final_digest(uint64_t chain_id, uint32_t version, const uint8_t P[32], const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], uint8_t out[32]) {
    uint8_t w[160]; size_t n = 0;
    auto tag = [&](const char *t) { const size_t L = strlen(t); w[n++] = (uint8_t)L; memcpy(w + n, t, L); n += L; };
    auto be64 = [&](uint64_t v) { for (int i = 7; i >= 0; --i) w[n++] = (uint8_t)(v >> (8 * i)); };
    auto be32 = [&](uint32_t v) { for (int i = 3; i >= 0; --i) w[n++] = (uint8_t)(v >> (8 * i)); };
    uint8_t S[32];
    tag("IgneumHash/R0A/seed"); be64(chain_id); be32(version); memcpy(w + n, P, 32); n += 32; memcpy(w + n, header, 32); n += 32; be64(nonce);
    ih_blake3_full(w, n, S);
    n = 0; tag("IgneumHash/R0A/final"); be64(chain_id); be32(version); memcpy(w + n, P, 32); n += 32; memcpy(w + n, S, 32); n += 32; memcpy(w + n, mix, 32); n += 32;
    ih_blake3_full(w, n, out);
}
void ih_blake3_xof(const uint8_t *in, size_t n, uint8_t *out, size_t outlen) {   // the XOF of a single-block input (n <= 64): output block j is the root compression with counter j
    uint32_t m[16] = {0}; uint8_t b[64] = {0}; for (size_t i = 0; i < n && i < 64; i++) b[i] = in[i]; words_of(b, m);
    for (size_t j = 0; 16 * 4 * j < outlen; j++) {
        uint32_t o[16]; compress(IV, m, (uint64_t)j, (uint32_t)n, F_CHUNK_START | F_CHUNK_END | F_ROOT, o);
        for (int w = 0; w < 16; w++) for (int k = 0; k < 4; k++) { size_t at = 64 * j + 4 * w + k; if (at < outlen) out[at] = (uint8_t)(o[w] >> (8 * k)); }
    }
}

void ih_blake3_host(const uint8_t *in, size_t n, const uint8_t *key, uint8_t out[32]) {
    uint32_t cv[8];
    for (int i = 0; i < 8; i++) cv[i] = key ? ((uint32_t)key[4 * i] | ((uint32_t)key[4 * i + 1] << 8) | ((uint32_t)key[4 * i + 2] << 16) | ((uint32_t)key[4 * i + 3] << 24)) : IV[i];
    uint32_t base = key ? 16u : 0u;
    size_t nblocks = n == 0 ? 1 : (n + 63) / 64;
    uint32_t o[16];
    for (size_t b = 0; b < nblocks; b++) {
        uint32_t m[16] = {0}; size_t len = (b + 1 == nblocks) ? n - 64 * b : 64;
        for (size_t i = 0; i < len; i++) m[i / 4] |= (uint32_t)in[64 * b + i] << (8 * (i % 4));
        uint32_t flags = base | (b == 0 ? 1u : 0u) | (b + 1 == nblocks ? (2u | 8u) : 0u);
        compress(cv, m, 0, (uint32_t)len, flags, o);
        for (int i = 0; i < 8; i++) cv[i] = o[i];
    }
    for (int i = 0; i < 8; i++) for (int b = 0; b < 4; b++) out[4 * i + b] = (uint8_t)(o[i] >> (8 * b));
}

bool ih_parse_params(const std::string &json, IhParams &out, std::string &err) {
    out = IhParams();
    // "version" is mandatory (security F-14, SPEC-R0A-v0.md 0.1): an empty, null or versionless document is refused
    std::string t = json; size_t a = t.find_first_not_of(" \t\r\n"); if (a == std::string::npos || t.substr(a, 2) == "{}" || t.substr(a, 4) == "null") { err = "params document names no version"; return false; }
    Json j; if (!Json::parse(json, j) || !j.is_object()) { err = "params document is not a JSON object"; return false; }
    if (!j.has("version")) { err = "params document names no version"; return false; }
    { uint64_t v = j["version"].as_u64(); if (v == 2) { err = "version 2 is the R0B schedule (the rxe1 candidate)"; return false; } if (v == 5) { err = "version 5 (the multiply-high address map) is not in this worker"; return false; } if (v == 22) {   // VERSION 22, KILN, the shader-batch form (FORM-FORGE.md section 6; the reference's parse on main): per batch a fragment programme of program_ops words, per hash T integer-corner triangles over a 2^target_log2 square RGBA8 target, `tiles` keyed 64 x 64 tiles of the dataset, `rows` full tile rows a fragment, `spread` the corners' offsets; the reference's slots: tiles in FOLD_EVERY, rows in BLOCKS, spread in SCRATCH_BYTES, target_log2 in REGISTERS, batch_log2 in KEYED_ROUNDS, T in ROUNDS
            out.version = 22; out.table_kib = 0; out.access_bytes = 1u << 8; out.lanes = 8; out.item_chain = 1; out.item_bytes = 256; out.rounds = 4; out.program_ops = 32; out.keyed_rounds = 12; out.registers = 5; out.fold_every = 64; out.blocks = 0; out.scratch_bytes = 7; out.period_blocks = 0; out.program_distance = 0; out.dataset_bytes = 1024ull << 20; out.cache_bytes = 64ull << 20; out.parents = 256; out.features = 0;
            for (const char *k : {"schedule", "features", "program_distance", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails", "period_blocks", "program_anchor", "drawn_order", "blocks", "keyed_rounds", "fp_mode", "sequence", "pair_log2", "prefetch", "scratch_kib", "registers", "fold_every"}) if (j.has(k)) { err = std::string(k) + " is not a version 22 member"; return false; }
            if (j.has("dataset_mib")) { uint64_t m = j["dataset_mib"].as_u64(); if (m < 1024 || m > 16384 || !pow2(m)) { err = "dataset_mib outside 1024..16384 or not a power of two (version 22)"; return false; } out.dataset_bytes = m << 20; }
            if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 1024) { err = "cache_mib outside 1..1024"; return false; } out.cache_bytes = c << 20; }
            if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096"; return false; } out.parents = (uint32_t)c; }
            if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 22"; return false; }
            if (j.has("lanes") && j["lanes"].as_u64() != 8) { err = "lanes is 8 under version 22"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 1 || r > 64) { err = "rounds (T, the triangles) outside 1..64 (version 22)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("program_ops")) { uint64_t o = j["program_ops"].as_u64(); if (o < 1 || o > 256) { err = "program_ops outside 1..256 (version 22)"; return false; } out.program_ops = (uint32_t)o; }
            if (j.has("batch_log2")) { uint64_t b = j["batch_log2"].as_u64(); if (b < 1 || b > 40) { err = "batch_log2 outside 1..40 (version 22)"; return false; } out.keyed_rounds = (uint32_t)b; }
            if (j.has("target_log2")) { uint64_t t = j["target_log2"].as_u64(); if (t < 5 || t > 8) { err = "target_log2 outside 5..8 (version 22)"; return false; } out.registers = (uint32_t)t; }
            if (j.has("tiles")) { uint64_t t = j["tiles"].as_u64(); if (t < 1 || t > 64) { err = "tiles outside 1..64 (version 22)"; return false; } out.fold_every = (uint32_t)t; }
            if (j.has("rows")) { uint64_t r = j["rows"].as_u64(); if (r > 4096) { err = "rows outside 0..4096 (version 22)"; return false; } out.blocks = (uint32_t)r; }
            if (j.has("spread")) { uint64_t sp = j["spread"].as_u64(); if (sp < 1 || sp > 127) { err = "spread outside 1..127 (version 22)"; return false; } out.scratch_bytes = (uint32_t)sp; }
            // "fragments" (the exact pass count, 0 = the covered alone), "coalesced" (0/1) and "contig" (1 = every row drawn) as the reference's 328f93091 and after: fragments in TABLE_KIB, coalesced | contig << 8 in ACCESS_BYTES, both bound into P (bytes 88 and 92)
            uint64_t frag = 0, coal = 0, contig = 1;
            if (j.has("fragments")) { frag = j["fragments"].as_u64(); if (frag > 4096) { err = "fragments outside 0..4096 (version 22)"; return false; } }
            if (j.has("coalesced")) { coal = j["coalesced"].as_u64(); if (coal > 1) { err = "coalesced outside 0..1 (version 22)"; return false; } }
            if (j.has("contig")) { contig = j["contig"].as_u64(); if (contig < 1 || contig > 255) { err = "contig outside 1..255 (version 22)"; return false; } }
            if (coal && (!frag || (frag & 15) || out.blocks < 1)) { err = "coalesced needs fragments (a multiple of 16) and rows (version 22)"; return false; }
            out.table_kib = (uint32_t)frag; out.access_bytes = (uint32_t)(coal | (contig << 8));
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (!(out.access_bytes & 1u) && (out.blocks > 64 || (out.blocks && (out.fold_every * 64u) % out.blocks))) { err = "rows outside 0..64 or not dividing tiles x 64 (version 22, the tile-rows form)"; return false; }
            return true;
        } if (v == 21 || v == 25) {   // VERSION 25, ANVIL FOR OLD CARDS (entry 2; the reference 6f75d586c, FORM-FORGE.md section 20): version 21's registered Anvil-R programme (fp_mode 6, lean, pair_log2 1, prefetch 16, 128 steps, 16 shapes, sequence 8, fold_every 2) with the scratch steps into a per-hash scratchpad of scratch_kib 4, 16 or 64   // VERSION 21, FORGE2, the two-level form (FORM-FORGE.md section 7; the reference's namespace forge2 on main 349908ba): the period's shapes from the header and the batch nonce, the hash's sequence and address words from the work seed; sequence in BLOCKS, batch_log2 in KEYED_ROUNDS (the reference's slots)
            out.version = (uint32_t)v; out.lanes = 16; out.item_chain = 1; out.item_bytes = 256; out.scratch_bytes = 16u << 10; out.rounds = 512; out.program_ops = 16; out.registers = 32; out.fold_every = 64; out.blocks = 32; out.keyed_rounds = 12; out.period_blocks = 0; out.program_distance = 0; out.dataset_bytes = 1024ull << 20; out.cache_bytes = 64ull << 20; out.parents = 256;
            for (const char *k : {"schedule", "program_distance", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails", "drawn_order", "blocks"}) if (j.has(k)) { err = std::string(k) + " is not a version 21 member"; return false; }
            // THE BOND-KEYED ROUNDS ON THE RELEASED VERSIONS (KEYED-RELEASED-VERSIONS-SPEC.md section 2, cb73f3b33; the review lead's F-441): features with bit 10 alone,
            // keyed_rounds the count of keyed-read steps S in 1..R (R = rounds / fold_every), mandatory with the bit and refused without it, under the chain form
            // alone (the batch form's width keeps keyed_rounds' field: batch_log2 below); the count lives in keyed_steps, never in the +64 slot
            if (j.has("features")) { uint64_t f = j["features"].as_u64(); if (f & ~1024ull) { err = "features: only bit 10 (the bond-keyed rounds) is a version 21 and 25 member"; return false; } out.features = (uint32_t)f; if ((f & 1024u) && !j.has("keyed_rounds")) { err = "features bit 10 needs keyed_rounds (the keyed-read steps)"; return false; } }
            if (j.has("keyed_rounds")) { if (!(out.features & 1024u)) { err = "keyed_rounds needs features bit 10 (version 21 and 25)"; return false; } uint64_t ks = j["keyed_rounds"].as_u64(); out.keyed_steps = (uint32_t)ks; }
            if (j.has("dataset_mib")) { uint64_t m = j["dataset_mib"].as_u64(); if (m < 1024 || m > 16384 || !pow2(m)) { err = "dataset_mib outside 1024..16384 or not a power of two (version 21)"; return false; } out.dataset_bytes = m << 20; }
            if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 1024) { err = "cache_mib outside 1..1024"; return false; } out.cache_bytes = c << 20; }
            if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096"; return false; } out.parents = (uint32_t)c; }
            if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 21"; return false; }
            if (j.has("lanes") && j["lanes"].as_u64() != 16) { err = "lanes is 16 under version 21"; return false; }
            uint64_t fpm = 0; if (j.has("fp_mode")) { fpm = j["fp_mode"].as_u64(); if (fpm > 6) { err = "fp_mode outside 0..6 in this worker (version 21; 7 kawplus is not ported)"; return false; } }
            uint64_t pl2 = 0; if (j.has("pair_log2")) { pl2 = j["pair_log2"].as_u64(); if (pl2 > 15) { err = "pair_log2 outside 0..15 (version 21)"; return false; } }
            uint64_t pf = 0; if (j.has("prefetch")) { pf = j["prefetch"].as_u64(); if (pf > 64) { err = "prefetch outside 0..64 (version 21)"; return false; } }
            uint64_t lean = 0; if (j.has("lean")) { lean = j["lean"].as_u64(); if (lean > 1) { err = "lean outside 0..1 (version 21)"; return false; } }
            out.program_distance = (uint32_t)((fpm << 1) | (pl2 << 4) | (pf << 8) | (lean << 15)); if (fpm == 5) out.registers = 16; if (fpm >= 3) out.scratch_bytes = 1u << 10;   // the reference's: occ = fp_mode >= 3 carries scratch_kib 1 (the window under 6), mode 5 sixteen registers
            if (j.has("registers") && j["registers"].as_u64() != out.registers) { err = fpm == 5 ? "registers is 16 under version 21 fp_mode 5" : "registers is 32 under version 21"; return false; }
            if (v == 25) { uint64_t kib = 4; if (j.has("scratch_kib")) kib = j["scratch_kib"].as_u64(); if (kib != 4 && kib != 16 && kib != 64) { err = "scratch_kib is 4, 16 or 64 under version 25"; return false; } out.scratch_bytes = (uint32_t)(kib << 10); }
            else if (j.has("scratch_kib") && (j["scratch_kib"].as_u64() << 10) != out.scratch_bytes) { err = fpm >= 3 ? "scratch_kib is 1 under version 21 fp_mode 3..6" : "scratch_kib is 16 under version 21"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 64 || r > 65536) { err = "rounds (the steps) outside 64..65536 (version 21)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("program_ops")) { uint64_t o = j["program_ops"].as_u64(); if (o < 1 || o > 16) { err = "program_ops (the shapes) outside 1..16 in this worker (version 21)"; return false; } out.program_ops = (uint32_t)o; }
            if (j.has("sequence")) { uint64_t L = j["sequence"].as_u64(); if (L < 1 || L > 1024) { err = "sequence outside 1..1024 (version 21)"; return false; } out.blocks = (uint32_t)L; }
            if (j.has("batch_log2")) { uint64_t b = j["batch_log2"].as_u64(); if (b < 1 || b > 40) { err = "batch_log2 outside 1..40 (version 21)"; return false; } out.keyed_rounds = (uint32_t)b; }
            // THE CHAIN FORM (F-141, the reference 4acef1ff1): period_blocks (64..epoch_blocks) keys the programme on the period of blocks and its anchor; batch_log2 refused with it; the anchor the document's test anchor (64 hex)
            if (j.has("period_blocks")) { uint64_t pb = j["period_blocks"].as_u64(); if (j.has("batch_log2")) { err = "batch_log2 is refused under the chain form (period_blocks)"; return false; } if (pb < 64 || pb > out.epoch_blocks) { err = "period_blocks outside 64..epoch_blocks (version 21)"; return false; } out.period_blocks = pb; out.keyed_rounds = 0; }
            if (j.has("program_anchor")) { if (!out.period_blocks) { err = "program_anchor needs period_blocks (version 21)"; return false; } std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor must be 64 hex characters"; return false; } out.program_anchor = a; }
            if (j.has("fold_every")) { uint64_t fe = j["fold_every"].as_u64(); if (fe < 1 || fe > out.rounds) { err = "fold_every outside 1..rounds (version 21)"; return false; } out.fold_every = (uint32_t)fe; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (out.rounds % out.blocks || out.rounds % out.fold_every) { err = "rounds must be a multiple of sequence and of fold_every (version 21)"; return false; }
            if (out.features & 1024u) { if (!out.period_blocks) { err = "features bit 10 (the bond-keyed rounds) needs the chain form (period_blocks) under version 21 and 25"; return false; } const uint32_t R = out.rounds / out.fold_every; if (out.keyed_steps < 1 || out.keyed_steps > R) { err = "keyed_rounds outside 1..R (R = rounds / fold_every, the keyed-read steps; version 21 and 25)"; return false; } }
            if (fpm == 6 && out.rounds / out.blocks != 16) { err = "fp_mode 6 needs rounds / sequence = 16 (the window's base is the step within a 16-step entry)"; return false; }
            if (v == 25 && (fpm != 6 || lean != 1 || pl2 != 1 || pf != 16 || out.rounds != 128 || out.program_ops != 16 || out.blocks != 8 || out.fold_every != 2)) { err = "version 25 is the registered Anvil-R programme (fp_mode 6, lean 1, pair_log2 1, prefetch 16, rounds 128, program_ops 16, sequence 8, fold_every 2)"; return false; }
            return true;
        } if (v == 20) {   // VERSION 20, FORGE, the per-hash programme discipline (FORM-FORGE.md section 2, the founder's order of 14 October 2026 22:3x UK): 16 lanes x 32 registers an attempt, one 16 KiB scratchpad an attempt in shared memory, 32 instruction words run 32 iterations each, the keyed read of 17b's gigabyte every 64 steps. THE FRAME: the fields as the form names them; the canonical bytes follow the reference (pow/ref, namespace forge) on the review's PASS
            out.version = 20; out.lanes = 16; out.item_chain = 1; out.item_bytes = 256; out.scratch_bytes = 16u << 10; out.rounds = 1024; out.program_ops = 32; out.registers = 32; out.fold_every = 64; out.period_blocks = 0; out.program_distance = 0; out.dataset_bytes = 1024ull << 20; out.cache_bytes = 64ull << 20; out.parents = 256;
            for (const char *k : {"schedule", "blocks", "features", "keyed_rounds", "program_distance", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails", "period_blocks", "program_anchor"}) if (j.has(k)) { err = std::string(k) + " is not a version 20 member"; return false; }
            if (j.has("drawn_order")) { uint64_t d = j["drawn_order"].as_u64(); if (d > 1) { err = "drawn_order must be 0 or 1 (version 20)"; return false; } out.program_distance = (uint32_t)d; }   // the drawn class order per word, carried in program_distance's slot
            if (j.has("dataset_mib")) { uint64_t m = j["dataset_mib"].as_u64(); if (m < 1024 || m > 16384 || !pow2(m)) { err = "dataset_mib outside 1024..16384 or not a power of two (version 20)"; return false; } out.dataset_bytes = m << 20; }
            if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 1024) { err = "cache_mib outside 1..1024"; return false; } out.cache_bytes = c << 20; }
            if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096"; return false; } out.parents = (uint32_t)c; }
            if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 20"; return false; }
            if (j.has("lanes") && j["lanes"].as_u64() != 16) { err = "lanes is 16 under version 20"; return false; }
            if (j.has("registers") && j["registers"].as_u64() != 32) { err = "registers is 32 under version 20"; return false; }
            if (j.has("scratch_kib") && j["scratch_kib"].as_u64() != 16) { err = "scratch_kib is 16 under version 20"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 64 || r > 65536) { err = "rounds (the steps) outside 64..65536 (version 20)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("program_ops")) { uint64_t o = j["program_ops"].as_u64(); if (o < 1 || o > 256) { err = "program_ops (the instruction words) outside 1..256 (version 20)"; return false; } out.program_ops = (uint32_t)o; }
            if (j.has("fold_every")) { uint64_t fe = j["fold_every"].as_u64(); if (fe < 1 || fe > out.rounds) { err = "fold_every outside 1..rounds (version 20)"; return false; } out.fold_every = (uint32_t)fe; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (out.rounds % out.program_ops || out.rounds % out.fold_every || out.fold_every > out.rounds) { err = "rounds must be a multiple of program_ops and of fold_every (version 20)"; return false; }
            return true;
        } if (v == 19) {   // VERSION 19, THE RASTER FORM THAT DRAWS CONTINUOUSLY (docs/gpu-b/RASTER-V19-DESIGN.md; the reference's block at igneumhash.cpp before 18's): rounds the
            // triangles (256 or 1024), reach (15 or 31, carried as SCRATCH_BYTES at byte 12), target_log2 (14 or 15, REGISTERS), the keyed base's dataset fields tied to the side
            // (dataset_mib = 4 << (2 target_log2 - 20)); the lab form over a zero base without them; no bond-keyed form, no second drawing rule
            out.version = 19; out.lanes = 8; out.item_chain = 1; out.item_bytes = 4; out.scratch_bytes = 15; out.dataset_bytes = 0; out.cache_bytes = 0; out.parents = 0; out.rounds = 256; out.period_blocks = 300; out.program_distance = 0; out.registers = 14;
            if (j.has("target_log2")) { uint64_t tl = j["target_log2"].as_u64(); if (tl != 14 && tl != 15) { err = "target_log2 is 14 or 15 (version 19)"; return false; } out.registers = (uint32_t)tl; }
            if (j.has("reach")) { uint64_t r = j["reach"].as_u64(); if (r != 15 && r != 31) { err = "reach is 15 or 31 (version 19)"; return false; } out.scratch_bytes = (uint32_t)r; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r != 256 && r != 1024) { err = "rounds (the triangles) is 256 or 1024 (version 19)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("dataset_mib")) { uint64_t m = j["dataset_mib"].as_u64(); if ((m << 20) != (4ull << (2 * out.registers))) { err = "dataset_mib must be 4 << (2 target_log2) MiB, the base's bytes (version 19)"; return false; } out.dataset_bytes = m << 20; out.item_bytes = 256; out.cache_bytes = 64ull << 20; out.parents = 256;
                if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 4096) { err = "cache_mib outside 1..4096 (version 19)"; return false; } out.cache_bytes = c << 20; }
                if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096 (version 19)"; return false; } out.parents = (uint32_t)c; }
                if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 19"; return false; } }
            else if (j.has("cache_mib") || j.has("parents") || j.has("item_bytes")) { err = "cache_mib, parents and item_bytes need dataset_mib (version 19)"; return false; }
            for (const char *k : {"features", "keyed_rounds", "draw", "scratch_kib", "lanes", "item_chain", "schedule", "blocks", "registers", "program_distance", "program_ops", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails"}) if (j.has(k)) { err = std::string(k) + " is not a version 19 member"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("period_blocks")) { uint64_t q = j["period_blocks"].as_u64(); if (q < 1 || q > out.epoch_blocks) { err = "period_blocks outside 1..epoch_blocks (version 19)"; return false; } out.period_blocks = q; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor is not 64 hex characters"; return false; } out.program_anchor = a; }
            out.program_ops = out.rounds;
            return true;
        } if (v == 18) {   // VERSION 18, THE RASTER FORM (the hash team's line of 14 October 2026 17:38 UK, main a82b8a30): no dataset; the reference's r18 text: "rounds" small integer-corner triangles through the rasteriser, the boxes digested
            // 18b: "target_log2" 14 = the 16384 target (registers 14); 18c: the keyed target (dataset_mib = 4 << (2 target_log2) MiB, cache_mib, parents, item_bytes 256 as version 15's)
            out.version = 18; out.lanes = 8; out.item_chain = 1; out.item_bytes = 4; out.scratch_bytes = 0; out.dataset_bytes = 0; out.cache_bytes = 0; out.parents = 0; out.rounds = 1024; out.period_blocks = 300; out.program_distance = 0;
            uint64_t tlog = 13; if (j.has("target_log2")) { tlog = j["target_log2"].as_u64(); if (tlog != 13 && tlog != 14) { err = "target_log2 is 13 or 14 (version 18b)"; return false; } } out.registers = tlog == 14 ? 14u : 0u;
            const bool keyed18 = j.has("dataset_mib");
            if (keyed18) { uint64_t m = j["dataset_mib"].as_u64(); if ((m << 20) != (4ull << (2 * tlog))) { err = "dataset_mib must be 4 << (2 target_log2) MiB, the target's bytes (version 18c)"; return false; } out.dataset_bytes = m << 20; out.item_bytes = 256; out.cache_bytes = 64ull << 20; out.parents = 256;
                if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 1024) { err = "cache_mib outside 1..1024 (version 18c)"; return false; } out.cache_bytes = c << 20; }
                if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096 (version 18c)"; return false; } out.parents = (uint32_t)c; }
                if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 18c"; return false; } }
            else if (j.has("cache_mib") || j.has("parents") || j.has("item_bytes")) { err = "cache_mib, parents and item_bytes need dataset_mib (version 18c)"; return false; }
            // F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 3, cb73f3b33; the review lead's +64 ruling): the bond-keyed TRIANGLES under 18: "features" bit 10 and
            // nothing else, "keyed_rounds" S in 1..rounds (the triangles), each mandatory with the other; the count rides byte 64 (the branch's own
            // KEYED_ROUNDS slot, zero on every version 18 document of record, so no released digest moves); the reference's parse at igneumhash.cpp 437
            if (j.has("features")) { uint64_t f = j["features"].as_u64(); if (f != 1024) { err = "features: only bit 10 (the bond-keyed triangles) is a version 18 member"; return false; } out.features = (uint32_t)f; }
            if (j.has("keyed_rounds")) { if (!(out.features & 1024u)) { err = "keyed_rounds needs features bit 10 (version 18)"; return false; } uint64_t s18 = j["keyed_rounds"].as_u64(); if (s18 < 1 || s18 > (j.has("rounds") ? j["rounds"].as_u64() : 1024)) { err = "keyed_rounds outside 1..rounds (version 18)"; return false; } out.keyed_rounds = (uint32_t)s18; }
            if ((out.features & 1024u) && !out.keyed_rounds) { err = "features bit 10 needs keyed_rounds (version 18)"; return false; }
            for (const char *k : {"scratch_kib", "lanes", "item_chain", "schedule", "blocks", "registers", "program_distance", "program_ops", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails"}) if (j.has(k)) { err = std::string(k) + " is not a version 18 member"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 64 || r > 65536 || (r & 7)) { err = "rounds (the triangles) outside 64..65536 or not a multiple of 8 (version 18)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("period_blocks")) { uint64_t q = j["period_blocks"].as_u64(); if (q < 1 || q > out.epoch_blocks) { err = "period_blocks outside 1..epoch_blocks (version 18)"; return false; } out.period_blocks = q; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor is not 64 hex characters"; return false; } out.program_anchor = a; }
            out.program_ops = out.rounds;
            return true;
        } if (v == 17) {   // VERSION 17, THE TEXTURE FORM (the hash team's line of 14 October 2026 16:34 UK, main 8a84c57a): no dataset; the reference's r17 text: a static 4096 x 4096 R32 uint texture sampled NEAREST at texel centres, "rounds" samples in eight dependent lanes
            out.version = 17; out.lanes = 8; out.item_chain = 1; out.item_bytes = 4; out.scratch_bytes = 0; out.dataset_bytes = 0; out.cache_bytes = 0; out.parents = 0; out.rounds = 512; out.period_blocks = 300; out.program_distance = 0;
            for (const char *k : {"dataset_mib", "cache_mib", "parents", "item_bytes", "scratch_kib", "lanes", "item_chain", "schedule", "blocks", "registers", "features", "keyed_rounds", "program_distance", "program_ops", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout", "tails"}) if (j.has(k)) { err = std::string(k) + " is not a version 17 member"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 64 || r > 65536 || (r & 7)) { err = "rounds (the samples) outside 64..65536 or not a multiple of 8 (version 17)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("period_blocks")) { uint64_t q = j["period_blocks"].as_u64(); if (q < 1 || q > out.epoch_blocks) { err = "period_blocks outside 1..epoch_blocks (version 17)"; return false; } out.period_blocks = q; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor is not 64 hex characters"; return false; } out.program_anchor = a; }
            out.program_ops = out.rounds;   // the reference carries the sample count in PROGRAM_OPS's slot of the 80 canonical bytes
            return true;
        } if (v == 16) {   // VERSION 16, the static-fabric integer coordinate (the founder's order of 13 October 2026 15:0x UK, C-36): no dataset; the reference's r16f text
            out.version = 16; out.lanes = 8; out.item_chain = 1; out.item_bytes = 256; out.scratch_bytes = 0; out.dataset_bytes = 0; out.cache_bytes = 0; out.parents = 0; out.rounds = 128; out.period_blocks = 300; out.program_distance = 0;
            for (const char *k : {"dataset_mib", "cache_mib", "parents", "item_bytes", "scratch_kib", "lanes", "item_chain", "schedule", "blocks", "registers", "features", "keyed_rounds", "program_distance", "program_ops", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout"}) if (j.has(k) && !(std::string(k) == "scratch_kib" && j[k].as_u64() == 0) && !(std::string(k) == "lanes" && j[k].as_u64() == 8) && !(std::string(k) == "item_chain" && j[k].as_u64() == 1)) { err = std::string("version 16 carries no ") + k; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 4 || r > 256) { err = "rounds (the passes) outside 4..256 (version 16)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("period_blocks")) { uint64_t q = j["period_blocks"].as_u64(); if (q < 1 || q > out.epoch_blocks) { err = "period_blocks outside 1..epoch_blocks (version 16)"; return false; } out.period_blocks = q; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (j.has("tails")) { uint64_t t = j["tails"].as_u64(); if (t > 1) { err = "tails outside 0..1 (version 16)"; return false; } out.program_distance = (uint32_t)t; }   // REVISION 3: tails 1 = the init and the mix at one BLAKE3 each through the fold chains; carried in program_distance's slot
            if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor is not 64 hex characters"; return false; } out.program_anchor = a; }
            out.program_ops = out.rounds * 224u;
            return true;
        }
        if (v == 15) {   // VERSION 15, the bandwidth-first class (the founder's order of 13 October 2026 14:17 UK; the rotation withdrawn): the reference's r16 text
            out.version = 15; out.lanes = 8; out.item_chain = 1; out.item_bytes = 256; out.scratch_bytes = 0; out.rounds = 256; out.program_ops = 64; out.period_blocks = 300; out.cache_bytes = 32ull << 20; out.parents = 256; out.dataset_bytes = 4096ull << 20;
            for (const char *k : {"scratch_kib", "lanes", "item_chain", "schedule", "blocks", "registers", "features", "keyed_rounds", "program_distance", "table_kib", "access_bytes", "shared_kib", "mma_max", "chains_per_nonce", "gather_lane_bank", "tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout"}) if (j.has(k) && !(std::string(k) == "scratch_kib" && j[k].as_u64() == 0) && !(std::string(k) == "lanes" && j[k].as_u64() == 8) && !(std::string(k) == "item_chain" && j[k].as_u64() == 1)) { err = std::string("version 15 carries no ") + k; return false; }
            if (!j.has("dataset_mib")) { err = "dataset_mib is mandatory under version 15"; return false; }
            { uint64_t m = j["dataset_mib"].as_u64(); if (m < 2048 || m > 16384 || !pow2(m)) { err = "dataset_mib outside 2048..16384 or not a power of two (version 15)"; return false; } out.dataset_bytes = m << 20; }
            if (j.has("cache_mib")) { uint64_t c = j["cache_mib"].as_u64(); if (c < 1 || c > 1024) { err = "cache_mib outside 1..1024"; return false; } out.cache_bytes = c << 20; }
            if (j.has("parents")) { uint64_t c = j["parents"].as_u64(); if (c < 1 || c > 4096) { err = "parents outside 1..4096"; return false; } out.parents = (uint32_t)c; }
            if (j.has("item_bytes") && j["item_bytes"].as_u64() != 256) { err = "item_bytes is 256 under version 15"; return false; }
            if (j.has("epoch_blocks")) { uint64_t e = j["epoch_blocks"].as_u64(); if (e < 1) { err = "epoch_blocks outside 1.."; return false; } out.epoch_blocks = e; }
            if (j.has("rounds")) { uint64_t r = j["rounds"].as_u64(); if (r < 64 || r > 1024) { err = "rounds (the reads) outside 64..1024 (version 15)"; return false; } out.rounds = (uint32_t)r; }
            if (j.has("program_ops")) { uint64_t o = j["program_ops"].as_u64(); if (o < 16 || o > 256) { err = "program_ops outside 16..256 (version 15)"; return false; } out.program_ops = (uint32_t)o; }
            if (j.has("period_blocks")) { uint64_t q = j["period_blocks"].as_u64(); if (q < 1 || q > out.epoch_blocks) { err = "period_blocks outside 1..epoch_blocks (version 15)"; return false; } out.period_blocks = q; }
            if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
            if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor is not 64 hex characters"; return false; } out.program_anchor = a; }
            return true;
        } if (v == 7 || v > 14) { err = "version must be 0, 1, 3, 4, 6, 8, 9, 10, 11, 12, 13 or 14"; return false; } out.version = (uint32_t)v; }
    if (out.version == 0) { out.dataset_bytes = 1ull << 28; out.scratch_bytes = 16384; }
    if (j.has("dataset_mib")) { uint64_t v = j["dataset_mib"].as_u64(); const bool anysize = out.version >= 8; if (v < 16 || v > 16384 || (anysize ? (v % 16 != 0) : !pow2(v))) { err = anysize ? "dataset_mib outside 16..16384 or not a multiple of 16 (version 8)" : "dataset_mib outside 16..16384 or not a power of two"; return false; } out.dataset_bytes = v << 20; }   // the range to 16 GiB (the project's 00:02 UK order: the 10 GiB fit on the 16 GB Macs); version 8 admits any multiple of 16 MiB with the multiply-high map (the reference's rule)
    if (j.has("scratch_kib")) { uint64_t v = j["scratch_kib"].as_u64(); if (v == 0 ? out.version == 0 : (v < 4 || v > 1024 || !pow2(v))) { err = "scratch_kib outside 4..1024 or not a power of two (0 under version 1 only)"; return false; } out.scratch_bytes = (uint32_t)(v << 10); }
    if (j.has("rounds")) { uint64_t v = j["rounds"].as_u64(); if (v < 1 || v > 4096) { err = "rounds outside 1..4096"; return false; } out.rounds = (uint32_t)v; }
    if (j.has("lanes")) { if (j["lanes"].as_u64() != 8) { err = "lanes must be 8"; return false; } }
    if (j.has("epoch_blocks")) { uint64_t v = j["epoch_blocks"].as_u64(); if (v < 1 || v > 0x100000000ull) { err = "epoch_blocks outside 1..2^32"; return false; } out.epoch_blocks = v; }
    if (j.has("item_chain")) { uint64_t v = j["item_chain"].as_u64(); if (v < 1 || v > 256 || (out.version >= 1 && v != 1)) { err = "item_chain outside 1..256 (1 under version 1)"; return false; } out.item_chain = (uint32_t)v; }
    if (j.has("cache_mib")) { if (out.version == 0) { err = "cache_mib is a version 1 member"; return false; } uint64_t v = j["cache_mib"].as_u64(); if (v < 1 || v > 256 || !pow2(v)) { err = "cache_mib outside 1..256 or not a power of two"; return false; } out.cache_bytes = v << 20; }
    if (j.has("parents")) { if (out.version == 0) { err = "parents is a version 1 member"; return false; } uint64_t v = j["parents"].as_u64(); if (v < 1 || v > 4096) { err = "parents outside 1..4096"; return false; } out.parents = (uint32_t)v; }
    if (j.has("item_bytes")) { if (out.version < 3) { err = "item_bytes is a version 3 member"; return false; } uint64_t v = j["item_bytes"].as_u64(); if (v != 128 && v != 256 && v != 512 && !((v == 64 || v == 1024) && out.version >= 4)) { err = "item_bytes must be 128, 256 or 512 (64 and 1024 under version 4)"; return false; } out.item_bytes = (uint32_t)v; }
    if (out.version >= 4 && !j.has("item_bytes")) { err = "item_bytes is mandatory under version 4"; return false; }
    if (j.has("blocks") || j.has("registers") || j.has("features")) { if (out.version < 8) { err = "blocks, registers and features are version 8, 9 and 10 members"; return false; } }
    if (out.version >= 8) {   // SPEC 0.8: BLOCKS and REGISTERS mandatory, FEATURES optional (appended only when non-zero)
        if (!j.has("blocks") || !j.has("registers")) { err = "blocks and registers are mandatory under version 8"; return false; }
        uint64_t b = j["blocks"].as_u64(), k = j["registers"].as_u64(); if (!(b == 1 || b == 2 || b == 4 || b == 8 || b == 16 || b == 32)) { err = "blocks must be 1, 2, 4, 8, 16 or 32"; return false; } if (!(k == 8 || k == 16 || k == 32 || k == 64)) { err = "registers must be 8, 16, 32 or 64 (32 and 64 widen the lane state, SPEC 0.8 on the project's 00:59 UK order)"; return false; }
        out.blocks = (uint32_t)b; out.registers = (uint32_t)k;
        if (j.has("features")) { uint64_t f = j["features"].as_u64(); if (f > (out.version >= 9 ? 32767u : 8191u) || (out.version >= 9 && !(f & 8192)) || (out.version < 9 && (f & 8192)) || ((f & 16384) && !(f & 8192)) || (f & 64) || ((f & 1024) && !j.has("keyed_rounds")) || ((f & 0x1800) == 0x1800) || ((f & 2048) && ((f & 12) || out.registers != 16)) || ((f & 0x300) == 0x300) || ((f & 4) && (f & 8)) || ((f & 1) && out.scratch_bytes == 0)) { err = "features: bits 0 to 12 without bit 6 (the reference's range, revision 2c; bit 10 needs keyed_rounds, SPEC 0.9); bits 11 and 12 exclusive; bits 2 and 3 exclusive; bit 0 needs scratch_kib"; return false; } out.features = (uint32_t)f; }
        if (j.has("keyed_rounds")) {   // SPEC 0.9: the bond-keyed rounds, mandatory with bit 10 and absent without it; S in 1..rounds
            uint64_t s = j["keyed_rounds"].as_u64(); if (!(out.features & 1024u)) { err = "keyed_rounds needs features bit 10"; return false; } if (s < 1 || s > out.rounds) { err = "keyed_rounds outside 1..rounds"; return false; } out.keyed_rounds = (uint32_t)s;
        }
    }
    if (j.has("table_kib") || j.has("access_bytes") || j.has("shared_kib")) { if (out.version < 12) { err = "table_kib, access_bytes and shared_kib are version 12 to 14 members"; return false; } }
    if (j.has("mma_max") || j.has("chains_per_nonce") || j.has("gather_lane_bank")) { if (out.version < 13) { err = "mma_max, chains_per_nonce and gather_lane_bank are version 13 and 14 members"; return false; } }
    for (const char *f : {"tile_shape", "fold_every", "items_per_round", "registers_live", "contexts_hint", "layout_draw", "difficulty_scale_ppm", "tile_layout"}) if (j.has(f) && out.version != 14) { err = std::string(f) + " is a version 14 member"; return false; }
    if (j.has("program_ops") || j.has("period_blocks") || j.has("program_anchor") || j.has("program_distance")) { if (out.version < 9) { err = "program_ops, period_blocks, program_anchor and program_distance are version 9 and 10 members"; return false; } }
    if (out.version >= 9) {   // SPEC-R0D-v0 0.3 (and R0E, version 10): the per-period programme on the lane's sixteen registers; W256, bit 13 mandatory, PROGRAM_OPS a multiple of 32
        if (out.item_bytes != 256) { err = "version 9 is the W256 walk (item_bytes 256)"; return false; }
        if (!j.has("program_ops") || !j.has("period_blocks")) { err = "program_ops and period_blocks are mandatory under version 9"; return false; }
        uint64_t o = j["program_ops"].as_u64(), q = j["period_blocks"].as_u64();
        if (o < 1 || o > 65536) { err = "program_ops outside 1..65536"; return false; }   // the reference: any count; the mask fires after every 32 and the tail runs unmasked
        if (q < 1 || q > 0x100000000ull) { err = "period_blocks outside 1..2^32"; return false; }
        out.program_ops = (uint32_t)o; out.period_blocks = q;
        if (out.version >= 12) {   // section 2c: the per-nonce table (a power of two of KiB, 1 to 64) and the row's bytes (4, 8 or 16), both mandatory; the shared table 0 or 16..64 KiB (main 2a12dee9)
            if (!j.has("table_kib") || !j.has("access_bytes")) { err = "table_kib and access_bytes are mandatory under versions 12 to 14"; return false; }
            uint64_t t = j["table_kib"].as_u64(), ab = j["access_bytes"].as_u64(), sk = j.has("shared_kib") ? j["shared_kib"].as_u64() : 0;
            if (t < 1 || t > 64 || (t & (t - 1))) { err = "table_kib must be a power of two in 1..64"; return false; } if (!(ab == 4 || ab == 8 || ab == 16)) { err = "access_bytes must be 4, 8 or 16"; return false; }
            if (sk != 0 && (sk < 16 || sk > 64 || (sk & (sk - 1)))) { err = "shared_kib must be 0, 16, 32 or 64"; return false; }
            out.table_kib = (uint32_t)t; out.access_bytes = (uint32_t)ab; out.shared_kib = (uint32_t)sk;
            if (out.table_words() / (out.access_bytes / 4) < 8) { err = "the per-nonce table must hold at least eight rows"; return false; }
        }
        if (out.version >= 13) {   // section 2d: the pinned matrix share, the access chains, the bank-confined gather
            uint64_t mm = j.has("mma_max") ? j["mma_max"].as_u64() : 14, ch = j.has("chains_per_nonce") ? j["chains_per_nonce"].as_u64() : 1, glb = j.has("gather_lane_bank") ? j["gather_lane_bank"].as_u64() : 0;
            if (mm > (out.version == 14 ? 27u : 14u) || (out.version == 14 && mm < 8)) { err = out.version == 14 ? "mma_max outside 8..27" : "mma_max outside 0..14"; return false; }
            if (!(ch == 1 || ch == 2 || ch == 4 || ch == 8)) { err = "chains_per_nonce must be 1, 2, 4 or 8"; return false; } if (glb > 1) { err = "gather_lane_bank must be 0 or 1"; return false; }
            out.mma_max = (uint32_t)mm; out.chains_per_nonce = (uint32_t)ch; out.gather_lane_bank = (uint32_t)glb;
        }
        if (out.version == 14) {   // section 2e: the tile shape (1, the canonical 16x8x32, is the one this worker runs), the fold period, the layout draw; the documents of record this worker refuses are named
            uint64_t ts = j.has("tile_shape") ? j["tile_shape"].as_u64() : 1, fe = j.has("fold_every") ? j["fold_every"].as_u64() : 1, ipr = j.has("items_per_round") ? j["items_per_round"].as_u64() : 1;
            uint64_t rl = j.has("registers_live") ? j["registers_live"].as_u64() : 16, chh = j.has("contexts_hint") ? j["contexts_hint"].as_u64() : 0, ld = j.has("layout_draw") ? j["layout_draw"].as_u64() : 0, ds = j.has("difficulty_scale_ppm") ? j["difficulty_scale_ppm"].as_u64() : 1000000;
            uint64_t tl = j.has("tile_layout") ? j["tile_layout"].as_u64() : 0;   // version 14.1 (C-21.1): 1 = the fragment layout of the INT8 tile (A as the m16n8k32 fragment, B read N x K, C landing in the fragment's place)
            if (tl > 1) { err = "tile_layout must be 0 or 1"; return false; }
            if (ts < 1 || ts > 3) { err = "tile_shape must be 1, 2 or 3"; return false; } if (ts != 1) { err = "tile_shape " + std::to_string(ts) + " (a document of record for the backends' exactness reads) is not in this worker; the canonical operation is shape 1"; return false; }
            if (!(fe == 1 || fe == 2 || fe == 4 || fe == 8)) { err = "fold_every must be 1, 2, 4 or 8"; return false; }
            if (ipr < 1 || ipr > 32 || (ipr & (ipr - 1))) { err = "items_per_round must be a power of two in 1..32"; return false; } if (ipr > 1) { err = "items_per_round above 1 (a document of record) is not in this worker"; return false; }
            if (!(rl == 16 || rl == 64 || rl == 128 || rl == 256)) { err = "registers_live must be 16, 64, 128 or 256"; return false; } if (rl > 16) { err = "registers_live above 16 (a document of record, never registered) is not in this worker"; return false; }
            if (chh > 64) { err = "contexts_hint outside 0..64"; return false; } if (ld > 1) { err = "layout_draw must be 0 or 1"; return false; } if (ds < 10000 || ds > 100000000) { err = "difficulty_scale_ppm outside 10000..100000000"; return false; }
            out.tile_shape = (uint32_t)ts; out.fold_every = (uint32_t)fe; out.items_per_round = (uint32_t)ipr; out.registers_live = (uint32_t)rl; out.contexts_hint = (uint32_t)chh; out.layout_draw = (uint32_t)ld; out.difficulty_scale_ppm = (uint32_t)ds; out.tile_layout = (uint32_t)tl;
        }
        if (j.has("program_distance")) { uint64_t d = j["program_distance"].as_u64(); if (d > 8) { err = "program_distance outside 0..8"; return false; } out.program_distance = (uint32_t)d; }
        if (j.has("program_anchor")) { std::string a = j["program_anchor"].as_string(); if (a.size() != 64 || a.find_first_not_of("0123456789abcdefABCDEF") != std::string::npos) { err = "program_anchor must be 64 hex characters"; return false; } out.program_anchor = a; }
    }
    if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
    return true;
}

void ih_params_bytes(const IhParams &p, uint8_t out[140]) {
    int k = 0;
    auto be32 = [&](uint32_t v) { for (int i = 3; i >= 0; i--) out[k++] = (uint8_t)(v >> (8 * i)); };
    auto be64 = [&](uint64_t v) { for (int i = 7; i >= 0; i--) out[k++] = (uint8_t)(v >> (8 * i)); };
    be32(p.version); be64(p.dataset_bytes); be32(p.scratch_bytes); be32(p.rounds); be32(p.lanes); be64(p.epoch_blocks); be32(p.item_chain);
    if (p.version >= 1) { be64(p.cache_bytes); be32(p.parents); }
    if (p.version >= 3) be32(p.item_bytes);   // ITEM_BYTES u32be at byte 48 (the lead, 10 Oct 2026)
    if (p.version == 8) { be32(p.blocks); be32(p.registers); if (p.features) be32(p.features); if (p.features & 1024u) be32(p.keyed_rounds); }
    if (p.version >= 9 && p.version < 12) { be32(p.blocks); be32(p.registers); be32(p.features); be32(p.keyed_rounds); be32(p.program_ops); be64(p.period_blocks); if (p.program_distance) be32(p.program_distance); }
    if (p.version == 15) { be32(0); be32(0); be32(0); be32(0); be32(p.program_ops); be64(p.period_blocks); }   // version 15: the version 9 layout with blocks, registers, features and keyed rounds zero: 80 bytes
    if (p.version == 16) { be32(0); be32(0); be32(0); be32(0); be32(p.program_ops); be64(p.period_blocks); if (p.program_distance) be32(p.program_distance); }
    if (p.version == 17 || p.version == 18 || p.version == 19) { be32(0); be32(p.registers); be32(p.features); be32(p.keyed_rounds); be32(p.program_ops); be64(p.period_blocks); }   // 19: the reach at byte 12 (SCRATCH_BYTES, written for every version above), the side at 56   // F-441: +60 features (bit 10) and +64 the keyed triangles under 18, both zero on every unkeyed document   // 18b: REGISTERS 14 at byte 56 (the 16384 target); 17 and 18's first rule carry 0
    if (p.version == 20) { be32(0); be32(p.registers); be32(0); be32(0); be32(p.program_ops); be64(0); be32(p.program_distance); be32(p.fold_every); }
    if (p.version == 22) { be32(p.blocks); be32(p.registers); be32(0); be32(p.keyed_rounds); be32(p.program_ops); be64(0); be32(p.program_distance); be32(p.fold_every); be32(p.table_kib); be32(p.access_bytes); }   // 96 bytes: the fragments and the coalesced form (with its contiguity) bound into P (the reference 328f93091)   // VERSION 22 (the reference's params_bytes): rows, target_log2, batch_log2, program_ops, tiles; 88 bytes
    if (p.version == 21 || p.version == 25) { be32(p.blocks); be32(p.registers); be32(p.features); be32(p.keyed_rounds); be32(p.program_ops); be64(p.period_blocks); be32(p.program_distance); be32(p.fold_every); if (p.features & 1024u) be32(p.keyed_steps); }   // bit 10 at +60 (zero for every unkeyed document, so its P is unchanged); the batch width at +64; the keyed-read steps at +88 under the bit alone (the specification cb73f3b33 section 2.5)   // version 25: the same 88 bytes, scratch_bytes (4, 16 or 64 KiB) at byte 12 as every version   // the chain form: period_blocks at 72, keyed_rounds (batch_log2) zero   // VERSION 21: the sequence in BLOCKS and batch_log2 in KEYED_ROUNDS (the reference's slots), 88 bytes   // VERSION 20 (the reference's params_bytes): the version 9 layout to byte 80, then PROGRAM_DISTANCE (drawn_order) at 80 and FOLD_EVERY at 84: 88 bytes   // version 17: the version 15 layout, 80 bytes (the samples in PROGRAM_OPS, item_bytes 4 at 48)   // version 16: the same layout, 80 bytes (dataset and cache bytes zero, item bytes 256)
    if (p.version >= 12 && p.version <= 14) { be32(p.blocks); be32(p.registers); be32(p.features); be32(p.keyed_rounds); be32(p.program_ops); be64(p.period_blocks); be32(p.program_distance); be32(p.table_kib); be32(p.access_bytes); be32(p.shared_kib); }   // versions 12 to 14: 96 bytes to here (SHARED_KIB at 92, main 2a12dee9)
    if (p.version >= 13 && p.version <= 14) { be32(p.mma_max); be32(p.chains_per_nonce); be32(p.gather_lane_bank); }   // 108
    if (p.version == 14) {   // the reference's rule: the fields after TILE_SHAPE only when needed, so the k = 1 documents keep their 112 bytes
        const bool tl = p.tile_layout != 0, ds = p.difficulty_scale_ppm != 1000000u || tl, ext = p.registers_live > 16 || p.contexts_hint || p.layout_draw || ds;   // the reference's rule: TILE_LAYOUT at 136 pulls the fields before it in
        be32(p.tile_shape);
        if (p.fold_every > 1 || p.items_per_round > 1 || ext) be32(p.fold_every);
        if (p.items_per_round > 1 || ext) be32(p.items_per_round);
        if (ext) { be32(p.registers_live); be32(p.contexts_hint); }
        if (p.layout_draw || ds) be32(p.layout_draw);
        if (ds) be32(p.difficulty_scale_ppm);
        if (tl) be32(p.tile_layout);   // 140 bytes
    }   // version 12: 92 bytes, the distance always present   // revision 0.4: PROGRAM_DISTANCE be32 at 80 when non-zero   // version 9: FEATURES and KEYED_ROUNDS always present (the reference's text, the hash team 19:3x UK)
}

void ih_P(const IhParams &p, uint8_t out[32]) { uint8_t b[140] = {0}; ih_params_bytes(p, b); ih_blake3_host(b, p.params_len(), nullptr, out); }

}  // namespace igm
