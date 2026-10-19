// compactproof: the compact-proof memory construction as a research reference
// (compactproof.h; docs/igneumhash/research/COMPACTPROOF-NOTES.md). In-house
// from the published texts: the MTP framework of Biryukov and Khovratovich
// (arXiv 1606.03588, Algorithms 1 and 2) over the DRSample graph of Alwen,
// Blocki and Harsha (CCS 2017, Algorithm 3 as reproduced in Blocki et al.
// 2019); the Blocki and Smearsoll 2025 modifications are marked UNREAD in the
// notes until the ePrint text is in hand. Research only.

#include "candidate.hpp"
#include "compactproof.h"
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
namespace cp {

constexpr uint64_t kMaxHeight = 0x7fffffffull;
constexpr uint32_t kTestSchema = 0x54455354u;
constexpr uint32_t kNode = 32;  // Merkle node bytes

const char* const kTagI = "CP/challenge";
const char* const kTagParent = "CP/parent";
const char* const kTagLabel0 = "CP/label0";
const char* const kTagLabel = "CP/label";
const char* const kTagLeaf = "CP/leaf";
const char* const kTagNodeH = "CP/node";
const char* const kTagY0 = "CP/y0";
const char* const kTagProgram = "CP/program";
const char* const kTagY = "CP/y";

struct enc {
    std::vector<uint8_t> v;
    enc& tag(const char* t) { size_t n = std::strlen(t); v.push_back(uint8_t(n)); v.insert(v.end(), t, t + n); return *this; }
    enc& raw(const uint8_t* p, size_t n) { v.insert(v.end(), p, p + n); return *this; }
    enc& le64(uint64_t x) { for (int i = 0; i < 8; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    enc& le32(uint32_t x) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
};

void H(const enc& e, uint8_t* out, size_t n = 32) {
    blake3_hasher h;
    blake3_hasher_init(&h);
    blake3_hasher_update(&h, e.v.data(), e.v.size());
    blake3_hasher_finalize(&h, out, n);
}

inline uint64_t le64(const uint8_t* p) { uint64_t x = 0; for (int i = 7; i >= 0; --i) x = (x << 8) | p[i]; return x; }
bool is_pow2(uint64_t x) { return x && (x & (x - 1)) == 0; }

bool parse_u64(const std::string& s, uint64_t& out) {
    if (s.empty() || s.size() > 20) return false;
    uint64_t v = 0;
    for (char c : s) { if (c < '0' || c > '9') return false; if (v > (UINT64_MAX - 9) / 10) return false; v = v * 10 + uint64_t(c - '0'); }
    out = v;
    return true;
}

int parse_params(const char* json, cp_params& p) {
    std::memset(&p, 0, sizeof p);
    p.label_bytes = 32; p.challenges = 70; p.chain_id = 7310; p.epoch_blocks = 4096;
    bool have_schema = false, have_mem = false;
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
        else if (key == "memory_bytes") { p.memory_bytes = v; have_mem = true; }
        else if (key == "label_bytes") p.label_bytes = uint32_t(v);
        else if (key == "graph") p.graph = uint32_t(v);
        else if (key == "challenges") p.challenges = uint32_t(v);
        else if (key == "chain_id") p.chain_id = v;
        else if (key == "epoch_blocks") p.epoch_blocks = v;
        else if (key == "test_only") p.test_only = uint32_t(v);
        else return IGNPOW_E_PARAMS;
    }
    if (!(have_schema && have_mem) || p.schema != 1) return IGNPOW_E_PARAMS;
    if (!(p.label_bytes == 32 || p.label_bytes == 64 || p.label_bytes == 128)) return IGNPOW_E_PARAMS;
    if (p.graph > 1) return IGNPOW_E_PARAMS;  // 0 DRSample (the registered row), 1 DRS+BRG (F-23's variant)
    if (p.challenges < 1 || p.challenges > 1024) return IGNPOW_E_PARAMS;
    if (p.test_only > 1) return IGNPOW_E_PARAMS;
    if (!is_pow2(p.memory_bytes)) return IGNPOW_E_PARAMS;
    if (p.test_only ? (p.memory_bytes < (1ull << 20) || p.memory_bytes > (1ull << 31))
                    : !(p.memory_bytes == (1ull << 26) || p.memory_bytes == (1ull << 28) || p.memory_bytes == (1ull << 30) || p.memory_bytes == (1ull << 31))) return IGNPOW_E_PARAMS;
    if (p.memory_bytes / p.label_bytes < 8) return IGNPOW_E_PARAMS;
    if (p.epoch_blocks < 1 || p.epoch_blocks > 0x100000000ull) return IGNPOW_E_PARAMS;
    if (p.test_only) p.schema = kTestSchema;
    return IGNPOW_OK;
}

// 20 bytes for graph 0 (every registered pid unchanged); 24 with GRAPH appended for graph 1.
size_t record_len(const cp_params& p) { return p.graph ? 24 : 20; }
void record(const cp_params& p, uint8_t out[24]) {
    enc e; e.le32(p.schema).le64(p.memory_bytes).le32(p.label_bytes).le32(p.challenges); if (p.graph) e.le32(p.graph);
    std::memcpy(out, e.v.data(), record_len(p));
}

void pid_of(const cp_params& p, uint8_t out[32]) {
    uint8_t rec[24];
    record(p, rec);
    blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, rec, record_len(p)); blake3_hasher_finalize(&h, out, 32);
}

uint64_t labels(const cp_params& p) { return p.memory_bytes / p.label_bytes; }
uint32_t depth(const cp_params& p) { uint32_t d = 0; for (uint64_t t = labels(p); t > 1; t >>= 1) ++d; return d; }

// DRSample (Alwen, Blocki and Harsha, Algorithm 3), 1-based: for v >= 3, g' uniform in [1, floor(log2 v) + 1],
// g = min(v, 2^g'), r uniform in [max(g/2, 2), g], the parent is v - r. The randomness is BLAKE3 over the pid and v
// (data-independent: the graph is a function of the parameter set alone). The uniform draws are by remainder.
uint64_t drsample_parent1(const uint8_t pid[32], uint64_t v);
// DRS+BRG (graph 1, F-23's variant; the forgeb1 reference's section 2 form on the 1-based graph): the first half,
// vertices 1 .. T/2, is DRSample on the half; a vertex v > T/2 has parents v - 1 and 1 + reverse_m(v - T/2 - 1) with
// m = log2(T/2) (the bit-reversal permutation of the first half), so the second half's pass reads every first-half
// label exactly once.
uint64_t parent1(const cp_params& p, const uint8_t pid[32], uint64_t v) {
    if (p.graph == 0) return drsample_parent1(pid, v);
    const uint64_t T = labels(p), half = T / 2;
    if (v <= half) return drsample_parent1(pid, v);
    uint32_t m = 0; for (uint64_t x = half; x > 1; x >>= 1) ++m;
    const uint64_t b = v - half - 1; uint64_t r = 0;
    for (uint32_t i = 0; i < m; ++i) if (b & (1ull << i)) r |= 1ull << (m - 1 - i);
    return 1 + r;
}
uint64_t drsample_parent1(const uint8_t pid[32], uint64_t v) {
    if (v < 3) return 1;
    uint8_t r[32];
    { enc e; e.tag(kTagParent).raw(pid, 32).le64(v); H(e, r); }
    uint32_t lg = 0; for (uint64_t t = v; t > 1; t >>= 1) ++lg;  // floor(log2 v)
    const uint64_t gp = 1 + (le64(r) % (lg + 1));
    uint64_t g = (gp >= 63) ? v : (1ull << gp);
    if (g > v) g = v;
    const uint64_t lo = (g / 2 > 2) ? g / 2 : 2;
    const uint64_t rr = lo + (le64(r + 8) % (g - lo + 1));
    // NOTE (COMPACTPROOF-NOTES.md 2.1): when g = v the draw admits r = v and v - r = 0, which is not a vertex of the
    // 1-based graph; the reference clamps that parent to vertex 1 (the reading of the text's [1, n] vertex set).
    return v - rr >= 1 ? v - rr : 1;
}

// Labels, 0-based index i = v - 1: X[0] = XOF(tag || I); X[1] = XOF(tag || I || 1 || X[0]); X[i >= 2] =
// XOF(tag || I || LE64(i) || X[i-1] || X[parent1(i+1) - 1]), label_bytes each.
void label_of(const cp_params& p, const uint8_t I[32], uint64_t i, const uint8_t* prev, const uint8_t* par, uint8_t* out) {
    enc e;
    if (i == 0) { e.tag(kTagLabel0).raw(I, 32); }
    else { e.tag(kTagLabel).raw(I, 32).le64(i).raw(prev, p.label_bytes); if (i >= 2) e.raw(par, p.label_bytes); }
    H(e, out, p.label_bytes);
}

void leaf_of(const cp_params& p, uint64_t i, const uint8_t* label, uint8_t out[32]) {
    enc e; e.tag(kTagLeaf).le64(i).raw(label, p.label_bytes); H(e, out);
}

void node_of(const uint8_t* l, const uint8_t* r, uint8_t out[32]) {
    enc e; e.tag(kTagNodeH).raw(l, 32).raw(r, 32); H(e, out);
}

void challenge_of(const cp_params& p, const uint8_t header[32], uint8_t I[32]) {
    enc e; e.tag(kTagI).le64(p.chain_id).raw(header, 32); H(e, I);
}

// The program id (F0 section 5): the approved program's 32-byte id for the parameter id, one per parameter set:
// BLAKE3(tag("CP/program") || "compactproof/v1" || pid).
void program_id_of(const uint8_t pid[32], uint8_t out[32]) {
    enc e; e.tag(kTagProgram); const char* v = "compactproof/v1"; e.raw(reinterpret_cast<const uint8_t*>(v), std::strlen(v)).raw(pid, 32); H(e, out);
}

struct prover {
    cp_params p;
    uint8_t pid[32];
    uint8_t program_id[32];
    uint8_t I[32];  // the seed (F0: the work function's only input)
    std::vector<uint8_t> X;     // T labels
    std::vector<uint8_t> tree;  // 2T - 1 nodes of 32 bytes, heap order: node k has children 2k+1, 2k+2; leaves at T-1 .. 2T-2
    uint64_t fill_ms = 0, commit_ms = 0;
    uint64_t T() const { return labels(p); }
    const uint8_t* node(uint64_t k) const { return tree.data() + k * kNode; }
};

void fill(prover& pr) {
    const auto t0 = std::chrono::steady_clock::now();
    const uint64_t T = pr.T();
    const uint32_t lb = pr.p.label_bytes;
    pr.X.resize(size_t(T) * lb);
    for (uint64_t i = 0; i < T; ++i) {
        const uint8_t* prev = i ? pr.X.data() + (i - 1) * lb : nullptr;
        const uint8_t* par = i >= 2 ? pr.X.data() + (parent1(pr.p, pr.pid, i + 1) - 1) * lb : nullptr;
        label_of(pr.p, pr.I, i, prev, par, pr.X.data() + i * lb);
    }
    pr.fill_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

void commit(prover& pr) {
    const auto t0 = std::chrono::steady_clock::now();
    const uint64_t T = pr.T();
    pr.tree.resize(size_t(2 * T - 1) * kNode);
    unsigned nt = std::thread::hardware_concurrency(); if (nt == 0) nt = 4; if (nt > 32) nt = 32;
    {
        std::vector<std::thread> ts;
        for (unsigned t = 0; t < nt; ++t)
            ts.emplace_back([&, t]() { for (uint64_t i = t; i < T; i += nt) leaf_of(pr.p, i, pr.X.data() + i * pr.p.label_bytes, pr.tree.data() + (T - 1 + i) * kNode); });
        for (auto& th : ts) th.join();
    }
    // internal nodes, level by level from the leaves up (k from T-2 down to 0; each level in parallel)
    for (uint64_t level_size = T / 2, first = T / 2 - 1; level_size >= 1; level_size /= 2, first = level_size ? level_size - 1 : 0) {
        std::vector<std::thread> ts;
        for (unsigned t = 0; t < nt; ++t)
            ts.emplace_back([&, t]() { for (uint64_t k = first + t; k < first + level_size; k += nt) node_of(pr.node(2 * k + 1), pr.node(2 * k + 2), pr.tree.data() + k * kNode); });
        for (auto& th : ts) th.join();
        if (level_size == 1) break;
    }
    pr.commit_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

// An opening of leaf i: the depth sibling nodes from the leaf up.
void open_path(const prover& pr, uint64_t i, uint8_t* out) {
    const uint32_t d = depth(pr.p);
    uint64_t k = pr.T() - 1 + i;
    for (uint32_t h = 0; h < d; ++h) {
        const uint64_t sib = (k % 2 == 1) ? k + 1 : k - 1;
        std::memcpy(out + h * kNode, pr.node(sib), kNode);
        k = (k - 1) / 2;
    }
}

// Proof layout per challenge j: LE64(i_j) || X[i_j - 1] (label) || X[par] (label) || path(i_j - 1) || path(par) ||
// path(i_j), each path depth x 32 bytes; for i_j < 2 the parent fields are present and zero (the verifier derives
// X[0] from I and ignores them) so the proof length is fixed.
// The proof: the root (32) then per challenge j: LE64(i_j) || X[i_j - 1] || X[par] || path(i_j - 1) || path(par) || path(i_j).
uint64_t proof_bytes(const cp_params& p) { return 32 + uint64_t(p.challenges) * (8 + 2 * p.label_bytes + 3 * uint64_t(depth(p)) * kNode); }

// Y_0 binds the seed, the program id and the commitment; there is no nonce step (F0 puts the nonce inside x, so an
// attempt is one fill; see COMPACTPROOF-NOTES.md section 2.4).
void y0_of(const uint8_t I[32], const uint8_t program_id[32], const uint8_t root[32], uint8_t out[32]) { enc e; e.tag(kTagY0).raw(I, 32).raw(program_id, 32).raw(root, 32); H(e, out); }
void y_of(const uint8_t* prev, const uint8_t* label, uint32_t lb, uint8_t out[32]) { enc e; e.tag(kTagY).raw(prev, 32).raw(label, lb); H(e, out); }

int prove(const prover& pr, uint8_t y_out[32], uint8_t* proof) {
    const uint64_t T = pr.T();
    const uint32_t lb = pr.p.label_bytes, d = depth(pr.p);
    uint8_t Y[32];
    y0_of(pr.I, pr.program_id, pr.node(0), Y);
    const uint64_t per = 8 + 2 * lb + 3 * uint64_t(d) * kNode;
    if (proof) std::memcpy(proof, pr.node(0), 32);
    for (uint32_t j = 0; j < pr.p.challenges; ++j) {
        const uint64_t i = le64(Y) % T;
        if (proof) {
            uint8_t* q = proof + 32 + j * per;
            for (int b = 0; b < 8; ++b) q[b] = uint8_t(i >> (8 * b));
            std::memset(q + 8, 0, 2 * lb + 3 * uint64_t(d) * kNode);
            if (i >= 1) std::memcpy(q + 8, pr.X.data() + (i - 1) * lb, lb);
            if (i >= 2) std::memcpy(q + 8 + lb, pr.X.data() + (parent1(pr.p, pr.pid, i + 1) - 1) * lb, lb);
            if (i >= 1) open_path(pr, i - 1, q + 8 + 2 * lb);
            if (i >= 2) open_path(pr, parent1(pr.p, pr.pid, i + 1) - 1, q + 8 + 2 * lb + uint64_t(d) * kNode);
            open_path(pr, i, q + 8 + 2 * lb + 2 * uint64_t(d) * kNode);
        }
        y_of(Y, pr.X.data() + i * lb, lb, Y);
    }
    std::memcpy(y_out, Y, 32);
    return IGNPOW_OK;
}

bool check_path(const cp_params& p, uint64_t i, const uint8_t* label, const uint8_t* path, const uint8_t root[32]) {
    const uint32_t d = depth(p);
    uint8_t h[32];
    leaf_of(p, i, label, h);
    uint64_t k = labels(p) - 1 + i;
    for (uint32_t lvl = 0; lvl < d; ++lvl) {
        uint8_t n[32];
        if (k % 2 == 1) node_of(h, path + lvl * kNode, n); else node_of(path + lvl * kNode, h, n);
        std::memcpy(h, n, 32);
        k = (k - 1) / 2;
    }
    return std::memcmp(h, root, 32) == 0;
}

bool is_zero(const uint8_t* q, size_t n) { for (size_t i = 0; i < n; ++i) if (q[i]) return false; return true; }

// The light verifier: seed, the program id the verifier holds (F0: from the chain's table), the claimed y, the proof.
int verify(const cp_params& p, const uint8_t seed[32], const uint8_t program_id[32], const uint8_t y[32], const uint8_t* proof, size_t proof_len, uint8_t y_out[32]) {
    if (proof_len != proof_bytes(p)) return IGNPOW_E_ARG;
    uint8_t pid[32], own_pid[32];
    pid_of(p, pid);
    program_id_of(pid, own_pid);
    if (std::memcmp(program_id, own_pid, 32) != 0) return IGNPOW_E_PARAMS;  // a proof for another program
    const uint8_t* root = proof;
    const uint8_t* I = seed;
    const uint64_t T = labels(p);
    const uint32_t lb = p.label_bytes, d = depth(p);
    const uint64_t per = 8 + 2 * lb + 3 * uint64_t(d) * kNode;
    uint8_t Y[32];
    y0_of(I, program_id, root, Y);
    std::vector<uint8_t> x0(lb), x1(lb), xi(lb);
    label_of(p, I, 0, nullptr, nullptr, x0.data());
    label_of(p, I, 1, x0.data(), nullptr, x1.data());
    for (uint32_t j = 0; j < p.challenges; ++j) {
        const uint8_t* q = proof + 32 + j * per;
        const uint64_t i = le64(q);
        if (i >= T || i != le64(Y) % T) return IGNPOW_E_MIX;
        const uint8_t* prev = q + 8;
        const uint8_t* par = q + 8 + lb;
        const uint8_t* path_prev = q + 8 + 2 * lb;
        const uint8_t* path_par = path_prev + uint64_t(d) * kNode;
        const uint8_t* path_i = path_par + uint64_t(d) * kNode;
        // one valid encoding per proof (FORGE.md section 5; protocol B's R-3): unused parent fields are zero
        if (i == 0 && !is_zero(prev, 2 * lb + 2 * uint64_t(d) * kNode)) return IGNPOW_E_MIX;
        if (i == 1 && !is_zero(par, lb) && true) return IGNPOW_E_MIX;
        if (i == 1 && !is_zero(path_par, uint64_t(d) * kNode)) return IGNPOW_E_MIX;
        if (i == 0) std::memcpy(xi.data(), x0.data(), lb);
        else if (i == 1) { if (!check_path(p, 0, prev, path_prev, root) || std::memcmp(prev, x0.data(), lb)) return IGNPOW_E_MIX; std::memcpy(xi.data(), x1.data(), lb); }
        else {
            const uint64_t pi = parent1(p, pid, i + 1) - 1;
            if (!check_path(p, i - 1, prev, path_prev, root)) return IGNPOW_E_MIX;
            if (!check_path(p, pi, par, path_par, root)) return IGNPOW_E_MIX;
            label_of(p, I, i, prev, par, xi.data());
        }
        if (!check_path(p, i, xi.data(), path_i, root)) return IGNPOW_E_MIX;
        y_of(Y, xi.data(), lb, Y);
    }
    if (std::memcmp(Y, y, 32) != 0) return IGNPOW_E_MIX;  // the proof's statement is WorkF(seed) = y
    std::memcpy(y_out, Y, 32);
    return IGNPOW_OK;
}

prover* create_seed(const cp_params& p, const uint8_t seed[32], int* status) {
    auto pr = std::make_unique<prover>();
    pr->p = p;
    pid_of(p, pr->pid);
    program_id_of(pr->pid, pr->program_id);
    std::memcpy(pr->I, seed, 32);
    try { fill(*pr); commit(*pr); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    if (status) *status = IGNPOW_OK;
    return pr.release();
}

// The ABI's harness seed from a header and nonce (F0's seed = H_seed(x) replaces it in the node: x carries the nonce).
void abi_seed(const cp_params& p, const uint8_t header[32], uint64_t nonce, uint8_t out[32]) {
    enc e; e.tag(kTagI).le64(p.chain_id).raw(header, 32).le64(nonce); H(e, out);
}

// ---- the ABI mapping: the heavy path ----

struct abi_ctx final : ctx_base {
    cp_params p;
    uint64_t ep = 0;
    mutable std::mutex mu;
    mutable std::unique_ptr<prover> cached;  // the last header's fill
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_COMPACTPROOF; }
};

int ab_check_params(const char* json) { cp_params p; return parse_params(json, p); }
uint64_t ab_epoch_of(const char* json, uint64_t height, int* status) {
    cp_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? height / p.epoch_blocks : 0;
}
uint64_t ab_cache_bytes(const char* json, uint64_t height, int* status) {
    cp_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return 0;
}
uint64_t ab_dataset_bytes(const char* json, uint64_t height, int* status) {
    cp_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? p.memory_bytes : 0;  // the per-challenge working memory
}
ctx_base* ab_create(const char* json, uint64_t height, int /*full*/, int* status) {
    auto a = std::make_unique<abi_ctx>();
    int st = parse_params(json, a->p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
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
    uint8_t seed[32];
    abi_seed(a.p, header, nonce, seed);
    if (!a.cached || std::memcmp(a.cached->I, seed, 32) != 0) {
        int st = 0;
        a.cached.reset(create_seed(a.p, seed, &st));  // one fill per attempt (F0 puts the nonce inside x)
        if (!a.cached) return st;
    }
    std::memcpy(mix_out, a.cached->node(0), 32);
    return prove(*a.cached, hash_out, nullptr);
}
int ab_verify(const ctx_base* base, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32];
    int st = ab_hash(base, height, header, nonce, m, h);
    if (st != IGNPOW_OK) return st;
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

// Self-test: DRSample's parent ranges, the proof round trip (prove then verify) on a 1 MiB test-only point, a
// tampered proof refused, and the ABI's mix equal to the root.
#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "compactproof self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)

int ab_vectors_check(size_t* count_out) {
    size_t n = 0;
    cp_params p;
    if (parse_params("{\"schema\": 1, \"memory_bytes\": 1048576, \"label_bytes\": 32, \"challenges\": 8, \"test_only\": 1}", p) != IGNPOW_OK) FAILV();
    {   // graph 1 (DRS+BRG): its own record and pid; the second half reads every first-half vertex once
        cp_params g1; if (parse_params("{\"schema\": 1, \"memory_bytes\": 1048576, \"label_bytes\": 32, \"challenges\": 8, \"graph\": 1, \"test_only\": 1}", g1) != IGNPOW_OK || record_len(g1) != 24) FAILV();
        uint8_t pa[32], pb[32]; pid_of(p, pa); pid_of(g1, pb); if (std::memcmp(pa, pb, 32) == 0) FAILV();
        const uint64_t T = labels(g1), half = T / 2; std::vector<uint8_t> seen(half + 1, 0);
        for (uint64_t v = half + 1; v <= T; ++v) { const uint64_t u = parent1(g1, pb, v); if (u < 1 || u > half) FAILV(); seen[u] = 1; }
        for (uint64_t u = 1; u <= half; ++u) if (!seen[u]) FAILV();
        if (parse_params("{\"schema\": 1, \"memory_bytes\": 1048576, \"label_bytes\": 32, \"challenges\": 8, \"graph\": 2, \"test_only\": 1}", g1) != IGNPOW_E_PARAMS) FAILV();
    }
    uint8_t pid[32]; pid_of(p, pid);
    for (uint64_t v = 3; v < 5000; ++v) {
        const uint64_t u = parent1(p, pid, v);
        uint32_t lg = 0; for (uint64_t t = v; t > 1; t >>= 1) ++lg;
        if (u < 1 || u >= v || (v - u) < 2 || (v - u) > (uint64_t(1) << (lg + 1))) FAILV();
    }
    ++n;
    uint8_t seed[32]; for (int i = 0; i < 32; ++i) seed[i] = uint8_t(11 * i + 2);
    int st = 0;
    std::unique_ptr<prover> pr(create_seed(p, seed, &st));
    if (!pr) FAILV();
    std::vector<uint8_t> proof(proof_bytes(p));
    uint8_t y[32], y2[32];
    if (prove(*pr, y, proof.data()) != IGNPOW_OK) FAILV();
    if (verify(p, seed, pr->program_id, y, proof.data(), proof.size(), y2) != IGNPOW_OK || std::memcmp(y, y2, 32)) FAILV();
    ++n;
    // the first implementation brief: altered x (seed), altered y, a wrong program id and malformed proofs are rejected
    proof[32 + 8 + 3] ^= 1;  // a parent label bit
    if (verify(p, seed, pr->program_id, y, proof.data(), proof.size(), y2) != IGNPOW_E_MIX) FAILV();
    proof[32 + 8 + 3] ^= 1;
    uint8_t seed2[32]; std::memcpy(seed2, seed, 32); seed2[0] ^= 1;
    if (verify(p, seed2, pr->program_id, y, proof.data(), proof.size(), y2) != IGNPOW_E_MIX) FAILV();  // altered x
    uint8_t yb[32]; std::memcpy(yb, y, 32); yb[31] ^= 1;
    if (verify(p, seed, pr->program_id, yb, proof.data(), proof.size(), y2) != IGNPOW_E_MIX) FAILV();  // altered y
    uint8_t prog2[32]; std::memcpy(prog2, pr->program_id, 32); prog2[5] ^= 1;
    if (verify(p, seed, prog2, y, proof.data(), proof.size(), y2) != IGNPOW_E_PARAMS) FAILV();  // wrong program id
    if (verify(p, seed, pr->program_id, y, proof.data(), proof.size() - 1, y2) != IGNPOW_E_ARG) FAILV();  // malformed
    proof[0] ^= 1;  // the root
    if (verify(p, seed, pr->program_id, y, proof.data(), proof.size(), y2) != IGNPOW_E_MIX) FAILV();
    proof[0] ^= 1;
    ++n;
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace cp

const candidate compactproof = {
    IGNPOW_ALGO_COMPACTPROOF,
    cp::ab_check_params, cp::ab_epoch_of, cp::ab_cache_bytes, cp::ab_dataset_bytes, cp::ab_create,
    cp::ab_hash, cp::ab_verify, cp::ab_light_cache, cp::ab_vectors_check,
    "compact-proof research reference (MTP framework over DRSample; Blocki and Smearsoll 2025 modifications UNREAD; BLAKE3 1.8.2 portable)",
};

}  // namespace ignpow

// ---- the C API ----

struct cp_prover { ignpow::cp::prover* pr; };

extern "C" {

int cp_params_parse(const char* params_json, cp_params* out) { if (!out) return IGNPOW_E_ARG; return ignpow::cp::parse_params(params_json, *out); }
void cp_pid(const cp_params* p, uint8_t out[32]) { if (p && out) ignpow::cp::pid_of(*p, out); }
void cp_variant_tag(const cp_params* p, char* buf, size_t n) {
    if (!p || !buf || !n) return;
    if (p->graph) std::snprintf(buf, n, "cp-m%llu-l%u-c%u-g%u", (unsigned long long)(p->memory_bytes >> 20), p->label_bytes, p->challenges, p->graph);
    else std::snprintf(buf, n, "cp-m%llu-l%u-c%u", (unsigned long long)(p->memory_bytes >> 20), p->label_bytes, p->challenges);
}
uint64_t cp_labels(const cp_params* p) { return p ? ignpow::cp::labels(*p) : 0; }
uint64_t cp_proof_bytes(const cp_params* p) { return p ? ignpow::cp::proof_bytes(*p) : 0; }

cp_prover* cp_prover_create(const cp_params* p, const uint8_t seed[32], uint64_t* fill_ms, uint64_t* commit_ms, int* status) {
    if (!p || !seed) { if (status) *status = IGNPOW_E_ARG; return nullptr; }
    ignpow::cp::prover* pr = ignpow::cp::create_seed(*p, seed, status);
    if (!pr) return nullptr;
    if (fill_ms) *fill_ms = pr->fill_ms;
    if (commit_ms) *commit_ms = pr->commit_ms;
    return new cp_prover{pr};
}
void cp_prover_free(cp_prover* pr) { if (pr) { delete pr->pr; delete pr; } }
void cp_prover_root(const cp_prover* pr, uint8_t root[32]) { if (pr && root) std::memcpy(root, pr->pr->node(0), 32); }
const uint8_t* cp_prover_label(const cp_prover* pr, uint64_t i) { if (!pr || i >= pr->pr->T()) return nullptr; return pr->pr->X.data() + i * pr->pr->p.label_bytes; }
int cp_prove(const cp_prover* pr, uint8_t y_out[32], uint8_t* proof_out) { if (!pr || !y_out) return IGNPOW_E_ARG; return ignpow::cp::prove(*pr->pr, y_out, proof_out); }
void cp_program_id(const cp_params* p, uint8_t out[32]) { if (!p || !out) return; uint8_t pid[32]; ignpow::cp::pid_of(*p, pid); ignpow::cp::program_id_of(pid, out); }
void cp_abi_seed(const cp_params* p, const uint8_t header[32], uint64_t nonce, uint8_t out[32]) { if (p && header && out) ignpow::cp::abi_seed(*p, header, nonce, out); }
int cp_verify(const cp_params* p, const uint8_t seed[32], const uint8_t program_id[32], const uint8_t y[32], const uint8_t* proof, size_t proof_len, uint8_t y_out[32]) {
    if (!p || !seed || !program_id || !y || !proof || !y_out) return IGNPOW_E_ARG;
    return ignpow::cp::verify(*p, seed, program_id, y, proof, proof_len, y_out);
}
uint64_t cp_parent(const cp_params* p, uint64_t v) { if (!p) return 0; uint8_t pid[32]; ignpow::cp::pid_of(*p, pid); return ignpow::cp::parent1(*p, pid, v); }

}  // extern "C"
