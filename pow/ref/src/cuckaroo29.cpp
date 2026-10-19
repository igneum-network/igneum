// Candidate cuckaroo29-tari: Tari's Cuckaroo29 proof of work (docs/igneumhash/controls/SOURCES.md). The graph, the
// solver and the cycle verifier are tromp/cuckoo's vendored sources (pow/third_party/cuckaroo29-5585282/src/src:
// cuckaroo/cuckaroo.hpp sipblock and verify, cuckaroo/graph.hpp the reference cycle finder of simple.cpp, crypto
// /siphash.hpp), included here unchanged; the chain's rules are Tari's (pow/third_party/cuckaroo29-tari-463e53d:
// base_layer/core/src/proof_of_work/cuckaroo_pow.rs, siphash.rs), written here from that text: the siphash keys
// BLAKE2b-256(BE64(nonce) || mining_hash) read as four LE64 words; the proof 42 ascending edge indices packed at 29
// bits (pack_nonces); the proof value BLAKE2b-256(packed proof) compared big-endian (Tari's
// big_endian_difficulty). The verifier is tromp's verify(), which is Tari's bipartite form.
//
// ABI mapping (a harness rule, never consensus): the header is the mining hash; ignpow_hash solves the graph of
// (header, nonce): hash_out = the proof value of the first cycle in the canonical list (each cycle's edges ascending,
// the list lexicographic), or 32 bytes of 0xff when the graph has no 42-cycle (never below a target); mix_out = BLAKE3
// over the packed proofs of every cycle in that order (BLAKE3 of the empty input when none). The solver and the
// verifier sides are the side API of controls.h. No document (NULL or "{}"); no epoch; no cache.
//
// One solver (the mean trimmer's buckets, a few GiB) is kept per process and reused under a mutex.

#define SQUASH_OUTPUT 1
#include "candidate.hpp"
#include "controls.h"
#include "blake2b.hpp"
#include "blake3.h"

#if defined(__x86_64__) && !defined(_WIN32)  // MinGW: mean.hpp's MAXPATHLEN collides with sys/param.h (the GPU team, 10 Oct 2026); Windows takes the simple finder
#define IGNPOW_C29_MEAN 1
#include <cuckaroo/mean.hpp>  // tromp's mean (edge-trimming) solver: cuckaroo.hpp, graph.hpp and the trimmer; x86 intrinsics
#else
#define IGNPOW_C29_MEAN 0
#include <cuckaroo/cuckaroo.hpp>
#include <cuckaroo/graph.hpp>  // the simple finder (simple.cpp's), complete but minutes per graph
#endif
#include <algorithm>
#include <thread>

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <mutex>
#include <string>
#include <vector>

namespace ignpow {
namespace c29 {

constexpr uint32_t kEdgeBits = EDGEBITS, kProofSize = PROOFSIZE;
// The harness epoch (never consensus): 2^24 heights per epoch, so the ABI's epoch rules (a context bound to one epoch,
// IGNPOW_E_EPOCH at another height) are exercised by the shared classes; the hash itself reads no height.
constexpr uint64_t kEpochLen = 1ull << 16;  // 65536 heights: the shared classes' epoch multiples (448 and 598 epochs) stay inside the height range
constexpr uint64_t kMaxHeight = 0x7fffffffull;  // the ABI's height range (IGNPOW_E_HEIGHT beyond it), as the other candidates
constexpr size_t kPackedBytes = (kProofSize * kEdgeBits + 7) / 8;  // 153
static_assert(kEdgeBits == 29 && kProofSize == 42, "Cuckaroo29");

bool params_default(const char* p) {
    if (p == nullptr) return true;
    std::string s; for (const char* c = p; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') s += *c;
    return s == "{}";
}

// Tari determine_sip_hash: BLAKE2b-256 over BE64(nonce) || mining_hash; the keys are the four LE64 words.
void keys_of(const uint8_t header[32], uint64_t nonce, siphash_keys& k) {
    uint8_t be[8]; for (int i = 0; i < 8; ++i) be[i] = static_cast<uint8_t>(nonce >> (56 - 8 * i));
    uint8_t h[32];
    blake2b b(32); b.update(be, 8); b.update(header, 32); b.finish(h);
    k.setkeys(reinterpret_cast<const char*>(h));
}

// Tari pack_nonces at bit_width 29 (its loop ported line for line).
void pack(const uint32_t* edges, size_t count, uint32_t bw, uint8_t* out, size_t out_len) {
    std::memset(out, 0, out_len);
    size_t pos = 0; uint64_t mini = 0; uint32_t remaining = 64;
    for (size_t i = 0; i < count; ++i) {
        const uint64_t el = edges[i];
        mini |= el << (64 - remaining);
        if (bw < remaining) remaining -= bw;
        else {
            for (int b = 0; b < 8; ++b) out[pos + b] = static_cast<uint8_t>(mini >> (8 * b));
            pos += 8;
            mini = el >> remaining;
            remaining = 64 + remaining - bw;
        }
    }
    size_t rem = (out_len - pos) % 8; if (rem == 0) rem = 8;
    if (mini > 0) for (size_t b = 0; b < rem && pos + b < out_len; ++b) out[pos + b] = static_cast<uint8_t>(mini >> (8 * b));
}

// Tari unpack_nonces: returns false on a non-zero extra (PowDataContainsNonZeroPadding).
bool unpack(const uint8_t* in, size_t in_len, uint32_t bw, size_t expected, std::vector<uint64_t>& out) {
    out.clear();
    const uint64_t mask = (1ull << bw) - 1;
    uint64_t mini = 0; uint32_t remaining = 64;
    for (size_t i = 0; i < in_len; ++i) {
        mini |= static_cast<uint64_t>(in[i]) << (64 - remaining);
        remaining = remaining >= 8 ? remaining - 8 : 0;
        while (remaining <= 64 - bw) { out.push_back(mini & mask); mini >>= bw; remaining += bw; }
    }
    for (size_t i = expected; i < out.size(); ++i) if (out[i] != 0) return false;
    if (out.size() > expected) out.resize(expected);
    return true;
}

void proof_hash(const uint8_t* packed, size_t n, uint8_t out[32]) { blake2b b(32); b.update(packed, n); b.finish(out); }

// Every 42-cycle of the graph of keys, each verified by tromp's verify(), the cycles sorted (each cycle's edges
// ascending, the list lexicographic) so that any complete solver reports the same list. On x86 the solver is tromp's
// mean solver (the edge trimmer with the matching phase recovering the edge indices; complete, since trimming removes
// only edges that lie on no cycle); elsewhere the simple finder over the whole graph (12 GiB, minutes per graph).
std::mutex g_mu;
#if IGNPOW_C29_MEAN
std::unique_ptr<solver_ctx> g_solver;
#else
std::unique_ptr<graph<word_t>> g_graph;
#endif

int solve(const siphash_keys& keys, std::vector<std::vector<uint32_t>>& sols) {
    sols.clear();
    std::lock_guard<std::mutex> lk(g_mu);
    siphash_keys k = keys;
#if IGNPOW_C29_MEAN
    if (!g_solver) {
        unsigned nt = std::thread::hardware_concurrency(); if (nt == 0) nt = 4; if (nt > 16) nt = 16;
        try { g_solver.reset(new solver_ctx(nt, 68, false, true, false)); } catch (...) { return IGNPOW_E_MEMORY; }
        if (!g_solver) return IGNPOW_E_MEMORY;
    }
    g_solver->trimmer.sip_keys = k;
    g_solver->sols.clear();
    const u32 n = g_solver->solve();
    for (u32 s = 0; s < n; ++s) {
        std::vector<uint32_t> c(g_solver->sols.begin() + s * kProofSize, g_solver->sols.begin() + (s + 1) * kProofSize);
        std::sort(c.begin(), c.end());
        word_t e[PROOFSIZE]; for (uint32_t i = 0; i < kProofSize; ++i) e[i] = c[i];
        if (verify(e, k) != POW_OK) continue;
        sols.push_back(c);
    }
#else
    if (!g_graph) { try { g_graph.reset(new graph<word_t>(NEDGES, NEDGES, MAX_SOLS)); } catch (...) { return IGNPOW_E_MEMORY; } }
    graph<word_t>& cg = *g_graph;
    cg.reset();
    u64 sips[EDGE_BLOCK_SIZE];
    for (word_t block = 0; block < NEDGES; block += EDGE_BLOCK_SIZE) {
        sipblock(k, block, sips);
        for (u32 i = 0; i < EDGE_BLOCK_SIZE; ++i) {
            const u64 edge = sips[i];
            cg.add_edge(static_cast<word_t>(edge & EDGEMASK), static_cast<word_t>((edge >> 32) & EDGEMASK));
        }
    }
    for (u32 s = 0; s < cg.nsols; ++s) {
        if (verify(cg.sols[s], k) != POW_OK) continue;
        sols.emplace_back(cg.sols[s], cg.sols[s] + kProofSize);
    }
#endif
    std::sort(sols.begin(), sols.end());
    sols.erase(std::unique(sols.begin(), sols.end()), sols.end());
    return IGNPOW_OK;
}

struct c29_ctx final : ctx_base {
    uint64_t ep = 0;
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_CUCKAROO29_TARI; }
};

int ab_check_params(const char* p) { return params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }
uint64_t ab_epoch_of(const char* p, uint64_t h, int* status) { int st = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; if (st == IGNPOW_OK && h > kMaxHeight) st = IGNPOW_E_HEIGHT; if (status) *status = st; return st == IGNPOW_OK ? h / kEpochLen : 0; }
uint64_t ab_cache_bytes(const char* p, uint64_t, int* status) { int st = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; if (status) *status = st; return 0; }
uint64_t ab_dataset_bytes(const char* p, uint64_t, int* status) { int st = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; if (status) *status = st; return 0; }
ctx_base* ab_create(const char* p, uint64_t h, int, int* status) {
    if (!params_default(p)) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    if (h > kMaxHeight) { if (status) *status = IGNPOW_E_HEIGHT; return nullptr; }
    if (status) *status = IGNPOW_OK;
    auto* c = new c29_ctx(); c->ep = h / kEpochLen; return c;
}
int compute(const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    siphash_keys k; keys_of(header, nonce, k);
    std::vector<std::vector<uint32_t>> sols;
    int st = solve(k, sols); if (st != IGNPOW_OK) return st;
    blake3_hasher h; blake3_hasher_init(&h);
    for (size_t s = 0; s < sols.size(); ++s) {
        uint8_t packed[kPackedBytes]; pack(sols[s].data(), kProofSize, kEdgeBits, packed, kPackedBytes);
        blake3_hasher_update(&h, packed, kPackedBytes);
        if (s == 0) proof_hash(packed, kPackedBytes, hash_out);
    }
    blake3_hasher_finalize(&h, mix_out, 32);
    if (sols.empty()) std::memset(hash_out, 0xff, 32);
    return IGNPOW_OK;
}
int ab_hash(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) { if (h > kMaxHeight) return IGNPOW_E_HEIGHT; if (h / kEpochLen != static_cast<const c29_ctx*>(c)->ep) return IGNPOW_E_EPOCH; return compute(header, nonce, mix_out, hash_out); }
int ab_verify(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (h / kEpochLen != static_cast<const c29_ctx*>(c)->ep) return IGNPOW_E_EPOCH;
    uint8_t m[32], hh[32]; int st = compute(header, nonce, m, hh); if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(hh, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int ab_light_cache(const ctx_base*, const void** data, uint64_t* bytes, uint32_t* item_bytes) { if (data) *data = nullptr; if (bytes) *bytes = 0; if (item_bytes) *item_bytes = 0; return IGNPOW_OK; }

#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "cuckaroo29 self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)
bool unhex(const char* s, uint8_t* out, size_t n) {
    if (std::strlen(s) != 2 * n) return false;
    for (size_t i = 0; i < n; ++i) { int a = s[2 * i], b = s[2 * i + 1]; auto v = [](int c) { return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : -1; }; if (v(a) < 0 || v(b) < 0) return false; out[i] = static_cast<uint8_t>(v(a) * 16 + v(b)); }
    return true;
}

// Tari's unit vectors (cuckaroo_pow.rs and siphash.rs tests) and tromp's sipblock agreement with Tari's siphash_block.
int ab_vectors_check(size_t* count_out) {
    size_t n = 0;
    {   // siphash24 and siphash_block (siphash.rs tests)
        siphash_keys k; k.k0 = 1; k.k1 = 2; k.k2 = 3; k.k3 = 4;
        if (k.siphash24(10) != 928382149599306901ull || k.siphash24(111) != 10524991083049122233ull) FAILV();
        siphash_keys k2; k2.k0 = 9; k2.k1 = 7; k2.k2 = 6; k2.k3 = 7;
        if (k2.siphash24(12) != 1305683875471634734ull || k2.siphash24(10) != 11589833042187638814ull) FAILV();
        u64 buf[EDGE_BLOCK_SIZE];
        if (sipblock(k, 10, buf) != 1182162244994096396ull || sipblock(k, 123, buf) != 11303676240481718781ull || sipblock(k2, 12, buf) != 4886136884237259030ull) FAILV();
        ++n;
    }
    {   // determine_sip_hash (test_header_hash)
        uint8_t hdr[32], want[32]; if (!unhex("4dbfee3eb7b9a6a27d2a4a8d754eb77cc5493006945c1246e50e9beb4de5ffa5", hdr, 32) || !unhex("49b48f77df94943cf3a422c5a0b528c737cc38a7b6c36076e81abcede5b2be3a", want, 32)) FAILV();
        siphash_keys k; keys_of(hdr, 0x9d589ed597ed42d1ull, k);
        siphash_keys w; w.setkeys(reinterpret_cast<const char*>(want));
        if (k.k0 != w.k0 || k.k1 != w.k1 || k.k2 != w.k2 || k.k3 != w.k3) FAILV();
        ++n;
    }
    {   // pack_nonces and unpack_nonces (the 3-bit and 29-bit tests)
        const uint32_t e3[4] = {0, 1, 2, 3}; uint8_t p3[2]; pack(e3, 4, 3, p3, 2);
        if (p3[0] != 0x88 || p3[1] != 0x06) FAILV();
        std::vector<uint64_t> u; if (!unpack(p3, 2, 3, 4, u) || u != std::vector<uint64_t>({0, 1, 2, 3})) FAILV();
        const uint8_t bad[3] = {0x88, 0x06, 0xc0}; if (unpack(bad, 3, 3, 4, u)) FAILV();
        const uint32_t e29[1] = {(1u << 29) - 1}; uint8_t p29[4]; pack(e29, 1, 29, p29, 4);
        if (p29[0] != 0xff || p29[1] != 0xff || p29[2] != 0xff || p29[3] != 0x1f) FAILV();
        if (!unpack(p29, 4, 29, 1, u) || u.size() != 1 || u[0] != (1u << 29) - 1) FAILV();
        const uint32_t e29b[4] = {(1u << 29) - 1, 0, (1u << 29) - 1, 0}; uint8_t p29b[15]; pack(e29b, 4, 29, p29b, 15);
        const uint8_t want[15] = {0xff, 0xff, 0xff, 0x1f, 0, 0, 0, 0xfc, 0xff, 0xff, 0x7f, 0, 0, 0, 0};
        if (std::memcmp(p29b, want, 15) != 0) FAILV();
        if (!unpack(p29b, 15, 29, 4, u) || u != std::vector<uint64_t>({(1u << 29) - 1, 0, (1u << 29) - 1, 0})) FAILV();
        ++n;
    }
    {   // test_unpack_example and test_edge_generation_example1/2
        const char* packed_hex = "ab3c742104de5808220f0ebd1d24e2a279489c9fa8c9264f754181433226655286c69a08f166e523283813e5eceabbec042598193013a34cd966601e064d5f3dbb911efe39536e7847f3180071865023e18c6c1c627696aece2ff401938abacc8ed2f446b8ba71785b074a086d36d1d61d638dc0eab156b8883214cbff9962f199b96c92349df3d1d7b647ed0cdf6dfde1e49077bcfb74b503";
        uint8_t packed[kPackedBytes]; if (!unhex(packed_hex, packed, kPackedBytes)) FAILV();
        std::vector<uint64_t> u; if (!unpack(packed, kPackedBytes, 29, 42, u) || u.size() != 42) FAILV();
        const uint64_t want[42] = {24394923, 46592033, 58968194, 71842682, 75995694, 81022926, 97860763, 105410600, 106063142, 138729012, 150559164, 170291280, 197045966, 202539638, 206356582, 215689620, 218504800, 232385274, 243238820, 250666150, 268537652, 296239928, 296891268, 315542595, 338677422, 341088271, 346272558, 359697897, 364349211, 377758979, 391857369, 403811427, 408334826, 413242437, 413564914, 426980322, 433277222, 460056825, 473150750, 473935291, 477597924, 497788893};
        for (int i = 0; i < 42; ++i) if (u[i] != want[i]) FAILV();
        uint32_t e[42]; for (int i = 0; i < 42; ++i) e[i] = static_cast<uint32_t>(want[i]);
        uint8_t repacked[kPackedBytes]; pack(e, 42, 29, repacked, kPackedBytes);
        if (std::memcmp(repacked, packed, kPackedBytes) != 0) FAILV();
        uint8_t kb[32]; if (!unhex("a216826b5d2752ccf129eef73e1e02a6b56d13195d7998e6b04d02a136b88f4f", kb, 32)) FAILV();
        siphash_keys k; k.setkeys(reinterpret_cast<const char*>(kb));
        u64 buf[EDGE_BLOCK_SIZE]; const u64 edge = sipblock(k, 24394923, buf);
        if ((edge & EDGEMASK) != 259523165 || ((edge >> 32) & EDGEMASK) != 501211281) FAILV();
        ++n;
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace c29

const candidate cuckaroo29_tari = {
    IGNPOW_ALGO_CUCKAROO29_TARI,
    c29::ab_check_params, c29::ab_epoch_of, c29::ab_cache_bytes, c29::ab_dataset_bytes, c29::ab_create,
    c29::ab_hash, c29::ab_verify, c29::ab_light_cache, c29::ab_vectors_check,
    "cuckaroo29-tari = tromp/cuckoo@55852825 (cuckaroo, EDGEBITS 29, PROOFSIZE 42) under tari-project/tari@463e53d3's keys, packing and proof value",
};

// ---- VectorWork's Graph coordinate (docs/igneumhash/vectorwork/GRAPH-COORDINATE.md, designer A, 13 Oct 2026; the project's order of 01:1x) ----
// The same construction (Cuckaroo29: EDGEBITS 29, PROOFSIZE 42, the sipblock-64 edge function over siphash-2-4, tromp's verify)
// under Igneum's binding: graph_key = BLAKE3(LP("Igneum/Graph/key") || LE64(chain_id) || LE64(height) || seed || LE64(nonce)), the
// first 32 bytes the siphash key as four LE64 words, where seed is the 32-byte sealing pre-image the ABI passes as the header; the
// TICKET is one 42-cycle's edge indices sorted ascending and distinct, each LE32, 168 bytes; ticket_hash = BLAKE3(LP("Igneum/Graph/
// ticket") || graph_key || ticket); the ticket meets a target when ticket_hash is at or under it. No dataset is read.
namespace graph {
constexpr const char* kTagKey = "Igneum/Graph/key";
constexpr const char* kTagTicket = "Igneum/Graph/ticket";
constexpr size_t kTicketBytes = 42 * 4;
constexpr uint64_t kEpochLen = c29::kEpochLen, kMaxHeight = c29::kMaxHeight;
struct params_t { uint64_t chain_id = 0; bool ok = false; };
params_t parse(const char* p) {
    params_t r;
    // the document: {"chain_id": N}; the empty document ({}, or none) is chain 0, so the harnesses that enumerate every candidate with
    // the empty document (the miner's supervisor test) read the coordinate, and the sets of record name their chain
    if (!p || c29::params_default(p)) { r.chain_id = 0; r.ok = true; return r; }
    std::string s; for (const char* c = p; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') s += *c;
    const std::string key = "\"chain_id\":";
    const size_t i = s.find(key); if (s.size() < 2 || s[0] != '{' || s.back() != '}' || i == std::string::npos) return r;
    size_t j = i + key.size(); uint64_t v = 0; bool any = false;
    while (j < s.size() && s[j] >= '0' && s[j] <= '9') { v = v * 10 + uint64_t(s[j] - '0'); ++j; any = true; }
    if (!any || (s[j] != '}' && s[j] != ',')) return r;
    r.chain_id = v; r.ok = true; return r;
}
void lp(blake3_hasher& h, const char* t) { const uint8_t n = uint8_t(std::strlen(t)); blake3_hasher_update(&h, &n, 1); blake3_hasher_update(&h, t, n); }
void le64(blake3_hasher& h, uint64_t x) { uint8_t b[8]; for (int i = 0; i < 8; ++i) b[i] = uint8_t(x >> (8 * i)); blake3_hasher_update(&h, b, 8); }
void key_of(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t key[32], siphash_keys& k) {
    blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagKey); le64(h, chain_id); le64(h, height); blake3_hasher_update(&h, seed, 32); le64(h, nonce); blake3_hasher_finalize(&h, key, 32);
    k.setkeys(reinterpret_cast<const char*>(key));  // the four LE64 words, as tromp's setkeys reads them
}
void ticket_bytes(const uint32_t edges[42], uint8_t out[kTicketBytes]) { for (int i = 0; i < 42; ++i) for (int b = 0; b < 4; ++b) out[4 * i + b] = uint8_t(edges[i] >> (8 * b)); }
void ticket_hash(const uint8_t key[32], const uint8_t ticket[kTicketBytes], uint8_t out[32]) {
    blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagTicket); blake3_hasher_update(&h, key, 32); blake3_hasher_update(&h, ticket, kTicketBytes); blake3_hasher_finalize(&h, out, 32);
}
// the ticket's check: sorted ascending and distinct, every edge under 2^29, and one 42-cycle of the graph under the key (tromp's verify)
int check_ticket(const siphash_keys& k, const uint32_t edges[42]) {
    for (int i = 0; i < 42; ++i) { if (edges[i] >= (1u << 29)) return IGNPOW_E_ARG; if (i && edges[i] <= edges[i - 1]) return IGNPOW_E_ARG; }
    word_t e[PROOFSIZE]; for (int i = 0; i < PROOFSIZE; ++i) e[i] = edges[i];
    siphash_keys kk = k;
    return verify(e, kk) == POW_OK ? IGNPOW_OK : IGNPOW_E_MIX;
}
struct graph_ctx final : ctx_base {
    uint64_t ep = 0, chain_id = 0;
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_IGNEUM_GRAPH; }
};
int gcheck_params(const char* p) { return parse(p).ok ? IGNPOW_OK : IGNPOW_E_PARAMS; }
uint64_t gepoch_of(const char* p, uint64_t h, int* status) { int st = parse(p).ok ? IGNPOW_OK : IGNPOW_E_PARAMS; if (st == IGNPOW_OK && h > kMaxHeight) st = IGNPOW_E_HEIGHT; if (status) *status = st; return st == IGNPOW_OK ? h / kEpochLen : 0; }
uint64_t gcache_bytes(const char* p, uint64_t, int* status) { int st = parse(p).ok ? IGNPOW_OK : IGNPOW_E_PARAMS; if (status) *status = st; return 0; }
uint64_t gdataset_bytes(const char* p, uint64_t, int* status) { int st = parse(p).ok ? IGNPOW_OK : IGNPOW_E_PARAMS; if (status) *status = st; return 0; }
ctx_base* gcreate(const char* p, uint64_t h, int, int* status) {
    const params_t r = parse(p);
    if (!r.ok) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    if (h > kMaxHeight) { if (status) *status = IGNPOW_E_HEIGHT; return nullptr; }
    if (status) *status = IGNPOW_OK;
    auto* c = new graph_ctx(); c->ep = h / kEpochLen; c->chain_id = r.chain_id; return c;
}
// the ABI's hash: the solver finds every 42-cycle of the attempt's graph; mix = BLAKE3 over the tickets in order (empty when none),
// hash = the ticket_hash of the smallest ticket (0xff.. when none), so a search over nonces meets a target exactly as a hash does
int gcompute(const graph_ctx& c, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    uint8_t key[32]; siphash_keys k; key_of(c.chain_id, height, seed, nonce, key, k);
    std::vector<std::vector<uint32_t>> sols; int st = c29::solve(k, sols); if (st != IGNPOW_OK) return st;
    blake3_hasher h; blake3_hasher_init(&h);
    for (size_t s = 0; s < sols.size(); ++s) { uint8_t t[kTicketBytes]; ticket_bytes(sols[s].data(), t); blake3_hasher_update(&h, t, kTicketBytes); if (s == 0) ticket_hash(key, t, hash_out); }
    blake3_hasher_finalize(&h, mix_out, 32);
    if (sols.empty()) {
        // no cycle: the hash is above every target (the top byte 0xff) and still a function of the attempt, so distinct heights,
        // headers and nonces never collide on it (the transitions class's period rule): 0xff || BLAKE3(LP("Igneum/Graph/none") || key)[1..]
        blake3_hasher n; blake3_hasher_init(&n); lp(n, "Igneum/Graph/none"); blake3_hasher_update(&n, key, 32); blake3_hasher_finalize(&n, hash_out, 32);
        hash_out[0] = 0xff;
    }
    return IGNPOW_OK;
}
int ghash(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& g = *static_cast<const graph_ctx*>(c);
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT; if (h / kEpochLen != g.ep) return IGNPOW_E_EPOCH;
    return gcompute(g, h, header, nonce, mix_out, hash_out);
}
int gverify(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    const auto& g = *static_cast<const graph_ctx*>(c);
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT; if (h / kEpochLen != g.ep) return IGNPOW_E_EPOCH;
    uint8_t m[32], hh[32]; int st = gcompute(g, h, header, nonce, m, hh); if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(hh, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int glight_cache(const ctx_base*, const void** data, uint64_t* bytes, uint32_t* item_bytes) { if (data) *data = nullptr; if (bytes) *bytes = 0; if (item_bytes) *item_bytes = 0; return IGNPOW_OK; }
int gvectors_check(size_t* count_out) { if (count_out) *count_out = 0; return IGNPOW_OK; }  // the sets of record in the tree are the vectors (ignpow-vectors check)
}  // namespace graph

const candidate igneum_graph = {
    IGNPOW_ALGO_IGNEUM_GRAPH,
    graph::gcheck_params, graph::gepoch_of, graph::gcache_bytes, graph::gdataset_bytes, graph::gcreate,
    graph::ghash, graph::gverify, graph::glight_cache, graph::gvectors_check,
    "igneum-graph = cuckaroo29 (tromp/cuckoo@55852825, EDGEBITS 29, PROOFSIZE 42, the sipblock-64 edge function) under Igneum's graph key and ticket hash (GRAPH-COORDINATE.md section 2)",
};

}  // namespace ignpow

extern "C" {
// the Graph coordinate's exported calls (the cgo path): the key, the ticket's check with its hash, the canonical ticket's bytes
void igneum_graph_key(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t key_out[32], uint64_t sip_keys_out[4]) {
    siphash_keys k; ignpow::graph::key_of(chain_id, height, seed, nonce, key_out, k);
    if (sip_keys_out) { sip_keys_out[0] = k.k0; sip_keys_out[1] = k.k1; sip_keys_out[2] = k.k2; sip_keys_out[3] = k.k3; }
}
int igneum_graph_verify_ticket(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, const uint32_t edges[42], uint8_t ticket_hash_out[32]) {
    uint8_t key[32]; siphash_keys k; ignpow::graph::key_of(chain_id, height, seed, nonce, key, k);
    const int st = ignpow::graph::check_ticket(k, edges); if (st != IGNPOW_OK) return st;
    if (ticket_hash_out) { uint8_t t[ignpow::graph::kTicketBytes]; ignpow::graph::ticket_bytes(edges, t); ignpow::graph::ticket_hash(key, t, ticket_hash_out); }
    return IGNPOW_OK;
}
int igneum_graph_solve(uint64_t chain_id, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint32_t* tickets, size_t max_tickets, size_t* n_out) {
    uint8_t key[32]; siphash_keys k; ignpow::graph::key_of(chain_id, height, seed, nonce, key, k);
    std::vector<std::vector<uint32_t>> v; int st = ignpow::c29::solve(k, v); if (st != IGNPOW_OK) return st;
    size_t n = v.size() < max_tickets ? v.size() : max_tickets;
    for (size_t s = 0; s < n; ++s) std::memcpy(tickets + 42 * s, v[s].data(), 42 * sizeof(uint32_t));
    if (n_out) *n_out = v.size();
    return IGNPOW_OK;
}
void igneum_graph_ticket_bytes(const uint32_t edges[42], uint8_t out[168]) { ignpow::graph::ticket_bytes(edges, out); }
void cuckaroo29_keys(const uint8_t header[32], uint64_t nonce, uint64_t keys[4]) { siphash_keys k; ignpow::c29::keys_of(header, nonce, k); keys[0] = k.k0; keys[1] = k.k1; keys[2] = k.k2; keys[3] = k.k3; }
int cuckaroo29_solve(const uint8_t header[32], uint64_t nonce, uint32_t* sols, size_t max_sols, size_t* n_out) {
    siphash_keys k; ignpow::c29::keys_of(header, nonce, k);
    std::vector<std::vector<uint32_t>> v; int st = ignpow::c29::solve(k, v); if (st != IGNPOW_OK) return st;
    size_t n = v.size() < max_sols ? v.size() : max_sols;
    for (size_t s = 0; s < n; ++s) std::memcpy(sols + 42 * s, v[s].data(), 42 * sizeof(uint32_t));
    if (n_out) *n_out = v.size();
    return IGNPOW_OK;
}
int cuckaroo29_verify(const uint8_t header[32], uint64_t nonce, const uint32_t edges[42]) {
    siphash_keys k; ignpow::c29::keys_of(header, nonce, k);
    word_t e[PROOFSIZE]; for (int i = 0; i < PROOFSIZE; ++i) e[i] = edges[i];
    return verify(e, k);
}
void cuckaroo29_pack(const uint32_t edges[42], uint8_t out[153]) { ignpow::c29::pack(edges, 42, 29, out, 153); }
int cuckaroo29_unpack(const uint8_t in[153], uint32_t edges[42]) {
    std::vector<uint64_t> u; if (!ignpow::c29::unpack(in, 153, 29, 42, u) || u.size() != 42) return IGNPOW_E_ARG;
    for (int i = 0; i < 42; ++i) edges[i] = static_cast<uint32_t>(u[i]);
    return IGNPOW_OK;
}
void cuckaroo29_proof_hash(const uint8_t packed[153], uint8_t out[32]) { ignpow::c29::proof_hash(packed, 153, out); }
}
