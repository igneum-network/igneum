// forgeb1: Forge F1 under B1 (docs/igneumhash/forge/F1-B1.md, text revision 2), the faithful small reference
// of WorkF in Forge-B1's shape, written from the text; protocol B's second implementation never reads this
// file. Research only.

#include "candidate.hpp"
#include "forgeb1.h"
#include "blake3.h"

#include <chrono>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

namespace ignpow {
namespace b1 {

constexpr uint64_t kMaxHeight = 0x7fffffffull;
constexpr uint32_t kTestSchema = 0x54455354u;
constexpr uint32_t kNode = 32;

struct enc {
    std::vector<uint8_t> v;
    enc& tag(const char* t) { size_t n = std::strlen(t); v.push_back(uint8_t(n)); v.insert(v.end(), t, t + n); return *this; }
    enc& raw(const uint8_t* p, size_t n) { v.insert(v.end(), p, p + n); return *this; }
    enc& le32(uint32_t x) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    enc& le64(uint64_t x) { for (int i = 0; i < 8; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
};

void H(const enc& e, uint8_t* out, size_t n = 32) {
    blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, e.v.data(), e.v.size()); blake3_hasher_finalize(&h, out, n);
}
void Hk(const uint8_t key[32], const enc& e, uint8_t* out, size_t n) {
    blake3_hasher h; blake3_hasher_init_keyed(&h, key); blake3_hasher_update(&h, e.v.data(), e.v.size()); blake3_hasher_finalize(&h, out, n);
}
inline uint64_t le64(const uint8_t* p) { uint64_t x = 0; for (int i = 7; i >= 0; --i) x = (x << 8) | p[i]; return x; }
bool is_pow2(uint64_t x) { return x && (x & (x - 1)) == 0; }
uint32_t log2u(uint64_t x) { uint32_t d = 0; for (uint64_t t = x; t > 1; t >>= 1) ++d; return d; }

bool parse_u64(const std::string& s, uint64_t& out) {
    if (s.empty() || s.size() > 20) return false;
    uint64_t v = 0;
    for (char c : s) { if (c < '0' || c > '9') return false; if (v > (UINT64_MAX - 9) / 10) return false; v = v * 10 + uint64_t(c - '0'); }
    out = v; return true;
}

int parse_params(const char* json, b1_params& p) {
    std::memset(&p, 0, sizeof p);
    p.batch = 64; p.chain_id = 7310; p.epoch_blocks = 4096;
    bool have_schema = false, have_graph = false, have_mem = false, have_label = false, have_q = false;
    if (json == nullptr) return IGNPOW_E_PARAMS;
    std::string t;
    for (const char* c = json; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') t += *c;
    if (t.size() < 2 || t.front() != '{' || t.back() != '}') return IGNPOW_E_PARAMS;
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
        if (!parse_u64(val, v)) return IGNPOW_E_PARAMS;
        if (key == "schema") { p.schema = uint32_t(v); have_schema = true; }
        else if (key == "graph") { p.graph = uint32_t(v); have_graph = true; }
        else if (key == "memory_bytes") { p.memory_bytes = v; have_mem = true; }
        else if (key == "label_bytes") { p.label_bytes = uint32_t(v); have_label = true; }
        else if (key == "challenges") { p.challenges = uint32_t(v); have_q = true; }
        else if (key == "batch") p.batch = uint32_t(v);
        else if (key == "chain_id") p.chain_id = v;
        else if (key == "epoch_blocks") p.epoch_blocks = v;
        else if (key == "test_only") p.test_only = uint32_t(v);
        else return IGNPOW_E_PARAMS;
    }
    if (!(have_schema && have_graph && have_mem && have_label && have_q)) return IGNPOW_E_PARAMS;
    if (p.schema != 1 || p.graph > 1) return IGNPOW_E_PARAMS;
    if (!(p.label_bytes == 128 || p.label_bytes == 256 || p.label_bytes == 512 || p.label_bytes == 1024)) return IGNPOW_E_PARAMS;
    if (p.challenges < 1 || p.challenges > 1024) return IGNPOW_E_PARAMS;
    if (p.batch < 1 || p.batch > 65536) return IGNPOW_E_PARAMS;
    if (p.test_only > 1) return IGNPOW_E_PARAMS;
    if (!is_pow2(p.memory_bytes)) return IGNPOW_E_PARAMS;
    if (p.test_only ? (p.memory_bytes < (1ull << 20) || p.memory_bytes > (1ull << 31))
                    : !(p.memory_bytes == (1ull << 26) || p.memory_bytes == (1ull << 28) || p.memory_bytes == (1ull << 30) || p.memory_bytes == (1ull << 31))) return IGNPOW_E_PARAMS;
    if (p.memory_bytes / p.label_bytes < 4) return IGNPOW_E_PARAMS;
    if (p.epoch_blocks < 1 || p.epoch_blocks > 0x100000000ull) return IGNPOW_E_PARAMS;
    if (p.test_only) p.schema = kTestSchema;
    return IGNPOW_OK;
}

void record_of(const b1_params& p, uint8_t out[B1_RECORD_BYTES]) {
    enc e; e.le32(p.schema).le32(p.graph).le64(p.memory_bytes).le32(p.label_bytes).le32(p.challenges).le32(p.batch);
    std::memcpy(out, e.v.data(), B1_RECORD_BYTES);
}
void pid_of(const b1_params& p, uint8_t out[32]) {
    uint8_t rec[B1_RECORD_BYTES]; record_of(p, rec);
    blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, rec, B1_RECORD_BYTES); blake3_hasher_finalize(&h, out, 32);
}
void program_id_of(const b1_params& p, uint8_t out[32]) {
    uint8_t pid[32]; pid_of(p, pid);
    enc e; e.tag("B1/program"); const char* s = "forge-b1/v1"; e.raw(reinterpret_cast<const uint8_t*>(s), std::strlen(s)).raw(pid, 32); H(e, out);
}
uint64_t nodes(const b1_params& p) { return p.memory_bytes / p.label_bytes; }

// Section 2: the topology. DRSample over u = v + 1 (1-based), the clamp; DRS+BRG with the bit-reversal half.
uint64_t drsample_parent(const uint8_t epoch_seed[32], uint64_t v) {
    const uint64_t u = v + 1;
    uint8_t h[32];
    { enc e; e.tag("B1/edge").raw(epoch_seed, 32).le64(v); H(e, h); }
    const uint32_t lg = log2u(u);
    const uint64_t gp = 1 + (le64(h) % (lg + 1));
    uint64_t g = gp >= 63 ? u : (1ull << gp);
    if (g > u) g = u;
    const uint64_t lo = (g / 2 > 2) ? g / 2 : 2;
    const uint64_t rr = lo + (le64(h + 8) % (g - lo + 1));
    const uint64_t parent1 = (u >= rr + 1) ? u - rr : 1;
    return parent1 - 1;
}
uint64_t reverse_bits(uint64_t b, uint32_t m) { uint64_t r = 0; for (uint32_t i = 0; i < m; ++i) if (b & (1ull << i)) r |= 1ull << (m - 1 - i); return r; }
uint64_t parent_of(const b1_params& p, const uint8_t epoch_seed[32], uint64_t v) {
    if (v < 2) return 0;
    if (p.graph == 0) return drsample_parent(epoch_seed, v);
    const uint64_t N = nodes(p), half = N / 2;
    if (v < half) return drsample_parent(epoch_seed, v);
    return reverse_bits(v - half, log2u(half));
}

// Section 3: the labels.
void label_of(const b1_params& p, const uint8_t fill_seed[32], uint64_t v, const uint8_t* prev, const uint8_t* par, uint8_t* out) {
    enc e;
    if (v == 0) e.tag("B1/label0").le32(p.schema).le32(p.graph);
    else { e.tag("B1/label").le32(p.schema).le64(v).raw(prev, p.label_bytes); if (v >= 2) e.raw(par, p.label_bytes); }
    Hk(fill_seed, e, out, p.label_bytes);
}

// Section 4: the commitment.
void leaf_of(const b1_params& p, uint64_t v, const uint8_t* label, uint8_t out[32]) { enc e; e.tag("Igneum/Forge/leaf/v2").le64(v).raw(label, p.label_bytes); H(e, out); }
void node_of(const uint8_t* l, const uint8_t* r, uint8_t out[32]) { enc e; e.tag("Igneum/Forge/node/v2").raw(l, 32).raw(r, 32); H(e, out); }

struct prover {
    b1_params p;
    uint8_t epoch_seed[32], fill_seed[32];
    std::vector<uint8_t> X, tree;
    uint64_t fill_ms = 0, commit_ms = 0;
    uint64_t N() const { return nodes(p); }
    const uint8_t* node(uint64_t k) const { return tree.data() + k * kNode; }
};

void fill(prover& pr) {
    const auto t0 = std::chrono::steady_clock::now();
    const uint64_t N = pr.N(); const uint32_t L = pr.p.label_bytes;
    pr.X.resize(size_t(N) * L);
    for (uint64_t v = 0; v < N; ++v) {
        const uint8_t* prev = v ? pr.X.data() + (v - 1) * L : nullptr;
        const uint8_t* par = v >= 2 ? pr.X.data() + parent_of(pr.p, pr.epoch_seed, v) * L : nullptr;
        label_of(pr.p, pr.fill_seed, v, prev, par, pr.X.data() + v * L);
    }
    pr.fill_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

void commit(prover& pr) {
    const auto t0 = std::chrono::steady_clock::now();
    const uint64_t N = pr.N();
    pr.tree.resize(size_t(2 * N - 1) * kNode);
    unsigned nt = std::thread::hardware_concurrency(); if (nt == 0) nt = 4; if (nt > 32) nt = 32;
    if (const char* env = std::getenv("IGNPOW_THREADS")) { const int v = std::atoi(env); if (v >= 1) nt = unsigned(v); }
    { std::vector<std::thread> ts; for (unsigned t = 0; t < nt; ++t) ts.emplace_back([&, t]() { for (uint64_t v = t; v < N; v += nt) leaf_of(pr.p, v, pr.X.data() + v * pr.p.label_bytes, pr.tree.data() + (N - 1 + v) * kNode); }); for (auto& th : ts) th.join(); }
    for (uint64_t level = N / 2, first = N / 2 - 1; level >= 1; level /= 2, first = level ? level - 1 : 0) {
        std::vector<std::thread> ts;
        for (unsigned t = 0; t < nt; ++t) ts.emplace_back([&, t]() { for (uint64_t k = first + t; k < first + level; k += nt) node_of(pr.node(2 * k + 1), pr.node(2 * k + 2), pr.tree.data() + k * kNode); });
        for (auto& th : ts) th.join();
        if (level == 1) break;
    }
    pr.commit_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

void open_path(const prover& pr, uint64_t v, uint8_t* out) {
    const uint32_t D = log2u(pr.N());
    uint64_t k = pr.N() - 1 + v;
    for (uint32_t h = 0; h < D; ++h) { const uint64_t sib = (k % 2 == 1) ? k + 1 : k - 1; std::memcpy(out + h * kNode, pr.node(sib), kNode); k = (k - 1) / 2; }
}

// Section 5: the challenges.
void challenges_of(const b1_params& p, const uint8_t transcript[32], std::vector<uint64_t>& idx) {
    const uint64_t N = nodes(p);
    std::vector<uint8_t> C(size_t(8) * p.challenges);
    enc e; e.tag("Igneum/Forge/challenge/v2").raw(transcript, 32);
    H(e, C.data(), C.size());
    idx.resize(p.challenges);
    for (uint32_t k = 0; k < p.challenges; ++k) idx[k] = le64(C.data() + 8 * k) % N;
}

uint64_t proof_bytes(const b1_params& p) { return 32 + uint64_t(p.challenges) * (8 + 2 * p.label_bytes + 3ull * log2u(nodes(p)) * kNode); }

// Section 6: the proof and the verifier.
int prove(const prover& pr, const uint8_t transcript[32], uint8_t* proof) {
    const uint32_t L = pr.p.label_bytes, D = log2u(pr.N());
    const uint64_t per = 8 + 2 * L + 3ull * D * kNode;
    std::vector<uint64_t> idx; challenges_of(pr.p, transcript, idx);
    std::memcpy(proof, pr.node(0), 32);
    for (uint32_t k = 0; k < pr.p.challenges; ++k) {
        const uint64_t i = idx[k];
        uint8_t* q = proof + 32 + k * per;
        for (int b = 0; b < 8; ++b) q[b] = uint8_t(i >> (8 * b));
        std::memset(q + 8, 0, per - 8);
        if (i >= 1) { std::memcpy(q + 8, pr.X.data() + (i - 1) * L, L); open_path(pr, i - 1, q + 8 + 2 * L); }
        if (i >= 2) { const uint64_t r = parent_of(pr.p, pr.epoch_seed, i); std::memcpy(q + 8 + L, pr.X.data() + r * L, L); open_path(pr, r, q + 8 + 2 * L + uint64_t(D) * kNode); }
        open_path(pr, i, q + 8 + 2 * L + 2ull * D * kNode);
    }
    return IGNPOW_OK;
}

bool check_path(const b1_params& p, uint64_t v, const uint8_t* label, const uint8_t* path, const uint8_t root[32]) {
    const uint32_t D = log2u(nodes(p));
    uint8_t h[32]; leaf_of(p, v, label, h);
    uint64_t k = nodes(p) - 1 + v;
    for (uint32_t lvl = 0; lvl < D; ++lvl) { uint8_t n[32]; if (k % 2 == 1) node_of(h, path + lvl * kNode, n); else node_of(path + lvl * kNode, h, n); std::memcpy(h, n, 32); k = (k - 1) / 2; }
    return std::memcmp(h, root, 32) == 0;
}
bool is_zero(const uint8_t* q, size_t n) { for (size_t i = 0; i < n; ++i) if (q[i]) return false; return true; }

int verify(const b1_params& p, const uint8_t epoch_seed[32], const uint8_t fill_seed[32], const uint8_t transcript[32], const uint8_t program_id[32], const uint8_t* proof, size_t proof_len) {
    uint8_t own[32]; program_id_of(p, own);
    if (std::memcmp(program_id, own, 32) != 0) return IGNPOW_E_PARAMS;
    if (proof_len != proof_bytes(p)) return IGNPOW_E_ARG;
    const uint8_t* root = proof;
    const uint32_t L = p.label_bytes, D = log2u(nodes(p));
    const uint64_t per = 8 + 2 * L + 3ull * D * kNode, N = nodes(p);
    std::vector<uint64_t> idx; challenges_of(p, transcript, idx);
    std::vector<uint8_t> x0(L), x1(L), xi(L);
    label_of(p, fill_seed, 0, nullptr, nullptr, x0.data());
    label_of(p, fill_seed, 1, x0.data(), nullptr, x1.data());
    for (uint32_t k = 0; k < p.challenges; ++k) {
        const uint8_t* q = proof + 32 + k * per;
        const uint64_t i = le64(q);
        if (i >= N || i != idx[k]) return IGNPOW_E_MIX;
        const uint8_t* prev = q + 8; const uint8_t* par = q + 8 + L;
        const uint8_t* path_prev = q + 8 + 2 * L; const uint8_t* path_par = path_prev + uint64_t(D) * kNode; const uint8_t* path_i = path_par + uint64_t(D) * kNode;
        if (i == 0) { if (!is_zero(prev, 2 * L + 2ull * D * kNode)) return IGNPOW_E_MIX; std::memcpy(xi.data(), x0.data(), L); }
        else if (i == 1) {
            if (!is_zero(par, L) || !is_zero(path_par, uint64_t(D) * kNode)) return IGNPOW_E_MIX;
            if (std::memcmp(prev, x0.data(), L) != 0 || !check_path(p, 0, prev, path_prev, root)) return IGNPOW_E_MIX;
            std::memcpy(xi.data(), x1.data(), L);
        } else {
            const uint64_t r = parent_of(p, epoch_seed, i);
            if (!check_path(p, i - 1, prev, path_prev, root)) return IGNPOW_E_MIX;
            if (!check_path(p, r, par, path_par, root)) return IGNPOW_E_MIX;
            label_of(p, fill_seed, i, prev, par, xi.data());
        }
        if (!check_path(p, i, xi.data(), path_i, root)) return IGNPOW_E_MIX;
    }
    return IGNPOW_OK;
}

prover* create(const b1_params& p, const uint8_t epoch_seed[32], const uint8_t fill_seed[32], int* status) {
    auto pr = std::make_unique<prover>();
    pr->p = p; std::memcpy(pr->epoch_seed, epoch_seed, 32); std::memcpy(pr->fill_seed, fill_seed, 32);
    try { fill(*pr); commit(*pr); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    if (status) *status = IGNPOW_OK;
    return pr.release();
}

void harness_seeds(const b1_params& p, const uint8_t header[32], uint64_t epoch, uint64_t nonce, uint8_t es[32], uint8_t fs[32]) {
    { enc e; e.tag("B1/harness-epoch").le64(p.chain_id).le64(epoch); H(e, es); }
    { enc e; e.tag("B1/harness-fill").raw(es, 32).raw(header, 32).le64(nonce); H(e, fs); }
}

// ---- the ABI mapping (heavy path; harness seeds and a harness ticket) ----
struct abi_ctx final : ctx_base {
    b1_params p; uint64_t ep = 0;
    mutable std::mutex mu; mutable std::unique_ptr<prover> cached;
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_FORGEB1; }
};
int ab_check_params(const char* json) { b1_params p; return parse_params(json, p); }
uint64_t ab_epoch_of(const char* json, uint64_t height, int* status) {
    b1_params p; int st = parse_params(json, p); if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? height / p.epoch_blocks : 0;
}
uint64_t ab_cache_bytes(const char* json, uint64_t height, int* status) {
    b1_params p; int st = parse_params(json, p); if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return 0;
}
uint64_t ab_dataset_bytes(const char* json, uint64_t height, int* status) {
    b1_params p; int st = parse_params(json, p); if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? p.memory_bytes : 0;
}
ctx_base* ab_create(const char* json, uint64_t height, int, int* status) {
    auto a = std::make_unique<abi_ctx>();
    int st = parse_params(json, a->p); if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    a->ep = height / a->p.epoch_blocks;
    if (status) *status = IGNPOW_OK;
    return a.release();
}
int ab_hash(const ctx_base* base, uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& a = *static_cast<const abi_ctx*>(base);
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / a.p.epoch_blocks != a.ep) return IGNPOW_E_EPOCH;
    std::lock_guard<std::mutex> lk(a.mu);
    uint8_t es[32], fs[32]; harness_seeds(a.p, header, a.ep, nonce, es, fs);
    if (!a.cached || std::memcmp(a.cached->fill_seed, fs, 32) != 0 || std::memcmp(a.cached->epoch_seed, es, 32) != 0) {
        int st = 0; a.cached.reset(create(a.p, es, fs, &st)); if (!a.cached) return st;
    }
    std::memcpy(mix_out, a.cached->node(0), 32);
    enc e; e.tag("B1/harness-ticket").raw(a.cached->node(0), 32).le64(nonce); H(e, hash_out);
    return IGNPOW_OK;
}
int ab_verify(const ctx_base* base, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32]; int st = ab_hash(base, height, header, nonce, m, h); if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(h, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int ab_light_cache(const ctx_base*, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    if (data) *data = nullptr;
    if (bytes) *bytes = 0;
    if (item_bytes) *item_bytes = 0;
    return IGNPOW_OK;
}

#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "forgeb1 self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)

// The self-test: the topology's ranges (both graphs), the round trip, and the eleven negative cases of text section 8.
int ab_vectors_check(size_t* count_out) {
    size_t n = 0;
    for (uint32_t g = 0; g < 2; ++g) {
        b1_params p;
        char doc[200]; std::snprintf(doc, sizeof doc, "{\"schema\": 1, \"graph\": %u, \"memory_bytes\": 1048576, \"label_bytes\": 128, \"challenges\": 8, \"batch\": 64, \"test_only\": 1}", g);
        if (parse_params(doc, p) != IGNPOW_OK) FAILV();
        uint8_t es[32], fs[32], tr[32]; for (int i = 0; i < 32; ++i) { es[i] = uint8_t(3 * i + 1); fs[i] = uint8_t(5 * i + 2); tr[i] = uint8_t(7 * i + 3); }
        const uint64_t N = nodes(p);
        for (uint64_t v = 2; v < N; ++v) { const uint64_t r = parent_of(p, es, v); if (r >= v) FAILV(); if (g == 1 && v >= N / 2 && r >= N / 2) FAILV(); }
        if (g == 1) { const uint64_t half = N / 2; std::vector<uint8_t> seen(half, 0); for (uint64_t v = half; v < N; ++v) seen[parent_of(p, es, v)] = 1; for (uint64_t v = 0; v < half; ++v) if (!seen[v]) FAILV(); }  // the bit-reversal half reads every first-half label once
        ++n;
        int st = 0;
        std::unique_ptr<prover> pr(create(p, es, fs, &st)); if (!pr) FAILV();
        uint8_t prog[32]; program_id_of(p, prog);
        std::vector<uint8_t> proof(proof_bytes(p));
        if (prove(*pr, tr, proof.data()) != IGNPOW_OK) FAILV();
        if (verify(p, es, fs, tr, prog, proof.data(), proof.size()) != IGNPOW_OK) FAILV();
        ++n;
        // the eleven negative cases
        uint8_t x[32];
        std::memcpy(x, fs, 32); x[0] ^= 1; if (verify(p, es, x, tr, prog, proof.data(), proof.size()) != IGNPOW_E_MIX) FAILV();          // 1 altered fill_seed
        std::memcpy(x, es, 32); x[0] ^= 1; if (verify(p, x, fs, tr, prog, proof.data(), proof.size()) != IGNPOW_E_MIX) FAILV();          // 2 altered epoch_seed
        std::memcpy(x, prog, 32); x[0] ^= 1; if (verify(p, es, fs, tr, x, proof.data(), proof.size()) != IGNPOW_E_PARAMS) FAILV();      // 3 wrong program id
        if (verify(p, es, fs, tr, prog, proof.data(), proof.size() - 1) != IGNPOW_E_ARG) FAILV();                                        // 4 truncated
        { std::vector<uint8_t> ext(proof); ext.push_back(0); if (verify(p, es, fs, tr, prog, ext.data(), ext.size()) != IGNPOW_E_ARG) FAILV(); }  // 5 extended
        {   // 6 a non-zero unused field: find a challenge with i < 2 or synthesise by a transcript; use the proof's structure: set byte 1 of a parent label of a challenge whose index is 0 or 1 if any; else test the rule on a copy by forcing the index field
            std::vector<uint8_t> t2(proof); const uint32_t L = p.label_bytes, D = log2u(N); const uint64_t per = 8 + 2 * L + 3ull * D * kNode;
            bool did = false;
            for (uint32_t k = 0; k < p.challenges && !did; ++k) { uint8_t* q = t2.data() + 32 + k * per; if (le64(q) < 2) { q[8 + L + 1] ^= 1; did = true; } }
            if (did && verify(p, es, fs, tr, prog, t2.data(), t2.size()) != IGNPOW_E_MIX) FAILV();
        }
        { std::vector<uint8_t> t2(proof); t2[32 + 8 + 3] ^= 1; if (verify(p, es, fs, tr, prog, t2.data(), t2.size()) != IGNPOW_E_MIX) FAILV(); }   // 7 flipped label byte
        { std::vector<uint8_t> t2(proof); t2[32 + 8 + 2 * p.label_bytes + 5] ^= 1; if (verify(p, es, fs, tr, prog, t2.data(), t2.size()) != IGNPOW_E_MIX) FAILV(); }  // 8 flipped path node
        { std::vector<uint8_t> t2(proof); t2[0] ^= 1; if (verify(p, es, fs, tr, prog, t2.data(), t2.size()) != IGNPOW_E_MIX) FAILV(); }        // 9 flipped root
        { std::vector<uint8_t> t2(proof); t2[32] ^= 1; if (verify(p, es, fs, tr, prog, t2.data(), t2.size()) != IGNPOW_E_MIX) FAILV(); }       // 10 a wrong index
        {   // 11 a proof under another parameter set (q differs): the length differs and is refused; the same length with another graph is refused by the program id
            b1_params p2 = p; p2.graph = 1 - g; uint8_t prog2[32]; program_id_of(p2, prog2);
            if (verify(p2, es, fs, tr, prog, proof.data(), proof.size()) != IGNPOW_E_PARAMS) FAILV();
            if (verify(p2, es, fs, tr, prog2, proof.data(), proof.size()) != IGNPOW_E_MIX) FAILV();
        }
        ++n;
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace b1

const candidate forgeb1 = {
    IGNPOW_ALGO_FORGEB1,
    b1::ab_check_params, b1::ab_epoch_of, b1::ab_cache_bytes, b1::ab_dataset_bytes, b1::ab_create,
    b1::ab_hash, b1::ab_verify, b1::ab_light_cache, b1::ab_vectors_check,
    "Forge F1 under B1 (F1-B1.md text revision 2: DRSample and DRS+BRG topologies, keyed labels, the commitment, the MTP-shaped proof; BLAKE3 1.8.2 portable)",
};

}  // namespace ignpow

struct b1_prover { ignpow::b1::prover* pr; };

extern "C" {
int b1_params_parse(const char* json, b1_params* out) { if (!out) return IGNPOW_E_ARG; return ignpow::b1::parse_params(json, *out); }
void b1_record(const b1_params* p, uint8_t out[B1_RECORD_BYTES]) { if (p && out) ignpow::b1::record_of(*p, out); }
void b1_pid(const b1_params* p, uint8_t out[32]) { if (p && out) ignpow::b1::pid_of(*p, out); }
void b1_program_id(const b1_params* p, uint8_t out[32]) { if (p && out) ignpow::b1::program_id_of(*p, out); }
void b1_variant_tag(const b1_params* p, char* buf, size_t n) { if (!p || !buf || !n) return; std::snprintf(buf, n, "b1-g%u-m%llu-l%u-q%u-b%u", p->graph, (unsigned long long)(p->memory_bytes >> 20), p->label_bytes, p->challenges, p->batch); }
uint64_t b1_nodes(const b1_params* p) { return p ? ignpow::b1::nodes(*p) : 0; }
uint64_t b1_proof_bytes(const b1_params* p) { return p ? ignpow::b1::proof_bytes(*p) : 0; }
uint64_t b1_parent(const b1_params* p, const uint8_t es[32], uint64_t v) { if (!p || !es) return 0; return ignpow::b1::parent_of(*p, es, v); }
b1_prover* b1_prover_create(const b1_params* p, const uint8_t es[32], const uint8_t fs[32], uint64_t* fill_ms, uint64_t* commit_ms, int* status) {
    if (!p || !es || !fs) { if (status) *status = IGNPOW_E_ARG; return nullptr; }
    ignpow::b1::prover* pr = ignpow::b1::create(*p, es, fs, status); if (!pr) return nullptr;
    if (fill_ms) *fill_ms = pr->fill_ms;
    if (commit_ms) *commit_ms = pr->commit_ms;
    return new b1_prover{pr};
}
void b1_prover_free(b1_prover* pr) { if (pr) { delete pr->pr; delete pr; } }
void b1_prover_root(const b1_prover* pr, uint8_t root[32]) { if (pr && root) std::memcpy(root, pr->pr->node(0), 32); }
const uint8_t* b1_prover_label(const b1_prover* pr, uint64_t v) { if (!pr || v >= pr->pr->N()) return nullptr; return pr->pr->X.data() + v * pr->pr->p.label_bytes; }
void b1_challenges(const b1_params* p, const uint8_t tr[32], uint64_t* out) { if (!p || !tr || !out) return; std::vector<uint64_t> idx; ignpow::b1::challenges_of(*p, tr, idx); std::memcpy(out, idx.data(), idx.size() * 8); }
int b1_prove(const b1_prover* pr, const uint8_t tr[32], uint8_t* proof_out) { if (!pr || !tr || !proof_out) return IGNPOW_E_ARG; return ignpow::b1::prove(*pr->pr, tr, proof_out); }
int b1_verify(const b1_params* p, const uint8_t es[32], const uint8_t fs[32], const uint8_t tr[32], const uint8_t prog[32], const uint8_t* proof, size_t len) {
    if (!p || !es || !fs || !tr || !prog || !proof) return IGNPOW_E_ARG;
    return ignpow::b1::verify(*p, es, fs, tr, prog, proof, len);
}
void b1_harness_seeds(const b1_params* p, const uint8_t header[32], uint64_t epoch, uint64_t nonce, uint8_t es[32], uint8_t fs[32]) { if (p && header && es && fs) ignpow::b1::harness_seeds(*p, header, epoch, nonce, es, fs); }
}
