// RXE1: the scalar reference of the founder's "IgneumHash engineering
// specification v0.1" (docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md) sections 6 to
// 8, as written. Harness readings beyond the text are in
// docs/igneumhash/rxe1/REFERENCE-NOTES.md and are marked NOTE below.
// Research only; an engineering hypothesis with its own algorithm id.
//
// The dataset is the pinned R0A v1 builder (r0a_dataset.hpp) run with E =
// epoch_seed at the record's sizes: no transformation (section 6, the
// dataset interface that must be bound).

#include "candidate.hpp"
#include "rxe1.h"
#include "blake3.h"
#include "r0a_dataset.hpp"

#include <chrono>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <string>
#include <thread>
#include <vector>

namespace ignpow {
namespace rxe1 {

constexpr uint32_t kMaxLanes = 32;  // the logical group G: 16 under schemas 1 and 2; 8, 16 or 32 under schema 3
constexpr uint32_t kMaxRegs = 16;
constexpr uint64_t kMaxHeight = 0x7fffffffull;
constexpr uint64_t kCacheBytes = 32ull << 20;  // the pinned dependency's cache size (section 7)
constexpr uint32_t kTestSchema = 0x54455354u;  // the record's schema of a test-only configuration (plus schema - 1)
inline uint32_t schema_version_of(uint32_t schema) { return schema >= kTestSchema ? schema - kTestSchema + 1 : schema; }

inline uint32_t rotl32(uint32_t x, uint32_t n) { n &= 31; return n ? (x << n) | (x >> (32 - n)) : x; }
inline uint32_t low32(uint64_t x) { return uint32_t(x); }
inline uint32_t mulhi32(uint32_t x, uint32_t y) { return uint32_t((uint64_t(x) * uint64_t(y)) >> 32); }

// ---- the encoding of section 6: LP, Enc, H, X ----

struct enc {
    std::vector<uint8_t> v;
    enc& lp(const uint8_t* p, size_t n) {
        for (int i = 0; i < 4; ++i) v.push_back(uint8_t(n >> (8 * i)));
        v.insert(v.end(), p, p + n);
        return *this;
    }
    enc& lp(const std::vector<uint8_t>& b) { return lp(b.data(), b.size()); }
    enc& tag(const char* t) { return lp(reinterpret_cast<const uint8_t*>(t), std::strlen(t)); }
    enc& le32(uint32_t x) { uint8_t b[4]; for (int i = 0; i < 4; ++i) b[i] = uint8_t(x >> (8 * i)); return lp(b, 4); }
    enc& le64(uint64_t x) { uint8_t b[8]; for (int i = 0; i < 8; ++i) b[i] = uint8_t(x >> (8 * i)); return lp(b, 8); }
};

void H(const enc& e, uint8_t out[32]) {
    blake3_hasher h;
    blake3_hasher_init(&h);
    blake3_hasher_update(&h, e.v.data(), e.v.size());
    blake3_hasher_finalize(&h, out, 32);
}

void X(const enc& e, uint8_t* out, size_t n) {
    blake3_hasher h;
    blake3_hasher_init(&h);
    blake3_hasher_update(&h, e.v.data(), e.v.size());
    blake3_hasher_finalize(&h, out, n);
}

// ---- parameters ----

bool is_pow2(uint64_t x) { return x && (x & (x - 1)) == 0; }

void spec_digest(uint8_t out[32]) {
    // NOTE: the binding constant; dependencies.lock.json resolves it to source and vector hashes.
    static const char* s = "IgneumHash-R0A/v1/dataset-builder/SPEC-R0A-v0.md/revision-0.6/section-5.3";
    blake3_hasher h;
    blake3_hasher_init(&h);
    blake3_hasher_update(&h, s, std::strlen(s));
    blake3_hasher_finalize(&h, out, 32);
}

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

bool unhex32(const std::string& s, uint8_t out[32]) {
    if (s.size() != 64) return false;
    for (int i = 0; i < 32; ++i) {
        auto v = [](char c) -> int { if (c >= '0' && c <= '9') return c - '0'; if (c >= 'a' && c <= 'f') return c - 'a' + 10; return -1; };
        int a = v(s[2 * i]), b = v(s[2 * i + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = uint8_t(a * 16 + b);
    }
    return true;
}

// {"key": integer | "hex", ...}; keys as rxe1.h names them. schema, mode, blocks,
// rounds, registers and dataset_bytes are mandatory; the rest have the notes' defaults.
int parse_params(const char* json, rxe1_params& p) {
    std::memset(&p, 0, sizeof p);
    spec_digest(p.dataset_spec_digest);
    p.parents = 256; p.cache_bytes = kCacheBytes; p.chain_id = 7310; p.epoch_blocks = 4096;
    p.group = 16; p.route = 1; p.chunks = 1; p.payload_bytes = 128;  // the v0.1 point; record fields under schema 3 only
    bool have_s3 = false, have_len = false, have_fam = false;
    bool have_schema = false, have_mode = false, have_blocks = false, have_rounds = false, have_regs = false, have_ds = false;
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
        std::string val;
        if (pos < body.size() && body[pos] == '"') {
            size_t e = body.find('"', pos + 1);
            if (e == std::string::npos) return IGNPOW_E_PARAMS;
            val = body.substr(pos + 1, e - pos - 1);
            pos = e + 1;
            if (pos < body.size()) { if (body[pos] != ',') return IGNPOW_E_PARAMS; ++pos; }
            if (key != "dataset_spec_digest") return IGNPOW_E_PARAMS;
            uint8_t d[32];
            if (!unhex32(val, d) || std::memcmp(d, p.dataset_spec_digest, 32) != 0) return IGNPOW_E_PARAMS;
            continue;
        }
        size_t e = body.find(',', pos);
        val = body.substr(pos, e == std::string::npos ? std::string::npos : e - pos);
        pos = e == std::string::npos ? body.size() : e + 1;
        uint64_t v = 0;
        if (!parse_u64(val, v)) return IGNPOW_E_PARAMS;
        if (key == "schema") { p.schema = uint32_t(v); have_schema = true; }
        else if (key == "mode") { p.mode = uint32_t(v); have_mode = true; }
        else if (key == "blocks") { p.blocks = uint32_t(v); have_blocks = true; }
        else if (key == "rounds") { p.rounds = uint32_t(v); have_rounds = true; }
        else if (key == "registers") { p.registers = uint32_t(v); have_regs = true; }
        else if (key == "dataset_bytes") { p.dataset_bytes = v; have_ds = true; }
        else if (key == "parents") p.parents = uint32_t(v);
        else if (key == "cache_bytes") p.cache_bytes = v;
        else if (key == "chain_id") p.chain_id = v;
        else if (key == "epoch_blocks") p.epoch_blocks = v;
        else if (key == "test_only") p.test_only = uint32_t(v);
        else if (key == "group") { p.group = uint32_t(v); have_s3 = true; }
        else if (key == "route") { p.route = uint32_t(v); have_s3 = true; }
        else if (key == "chunks") { p.chunks = uint32_t(v); have_s3 = true; }
        else if (key == "payload_bytes") { p.payload_bytes = uint32_t(v); have_s3 = true; }
        else if (key == "block_len") { p.block_len = uint32_t(v); have_len = true; }
        else if (key == "family") { p.family = uint32_t(v); have_fam = true; }
        else return IGNPOW_E_PARAMS;
    }
    if (!(have_schema && have_mode && have_blocks && have_rounds && have_regs && have_ds)) return IGNPOW_E_PARAMS;
    if (!(p.schema == 1 || p.schema == 2 || p.schema == 3 || p.schema == 4 || p.schema == 5)) return IGNPOW_E_PARAMS;
    if (p.schema < 3 && have_s3) return IGNPOW_E_PARAMS;
    if (p.schema != 4 && (have_len || have_fam)) return IGNPOW_E_PARAMS;
    if (p.mode > 4 || (p.mode == 3 && !(p.schema == 3 || p.schema == 5)) || (p.mode == 4) != (p.schema == 4)) return IGNPOW_E_PARAMS;  // mode 3 (P2) is schema 3's and 5's; mode 4 (S) and schema 4 go together
    if (p.schema == 5 && (p.mode == 0 || p.mode == 4)) return IGNPOW_E_PARAMS;  // the memory-free schema has no memory mode and no compiled blocks
    if (p.schema == 4) {
        if (!(have_len && have_fam)) return IGNPOW_E_PARAMS;
        if (!(p.block_len == 8 || p.block_len == 16 || p.block_len == 32)) return IGNPOW_E_PARAMS;
        if (!(p.family == 16 || p.family == 32 || p.family == 64)) return IGNPOW_E_PARAMS;
    }
    if (p.mode == 0 ? p.blocks != 0 : !(p.blocks == 1 || p.blocks == 2 || p.blocks == 4)) return IGNPOW_E_PARAMS;
    if (!(p.rounds == 32 || p.rounds == 64)) return IGNPOW_E_PARAMS;
    if (p.schema >= 3) {
        // SPEC-TEXT-v0.2-A2A3.md section 1
        if (!(p.registers == 4 || p.registers == 8 || p.registers == 16)) return IGNPOW_E_PARAMS;
        if (!(p.group == 8 || p.group == 16 || p.group == 32)) return IGNPOW_E_PARAMS;
        if (p.route > 3) return IGNPOW_E_PARAMS;
        if (!(p.chunks == 1 || p.chunks == 2 || p.chunks == 4)) return IGNPOW_E_PARAMS;
        if (!(p.payload_bytes == 128 || p.payload_bytes == 256)) return IGNPOW_E_PARAMS;
        const uint32_t cb = p.payload_bytes / p.chunks;
        if (!(cb == 64 || cb == 128 || cb == 256)) return IGNPOW_E_PARAMS;
        if ((p.payload_bytes / 4) % p.group != 0) return IGNPOW_E_PARAMS;
        if ((p.group * p.registers) % 8 != 0) return IGNPOW_E_PARAMS;
        if (p.schema == 4) {
            const uint32_t ops = p.blocks * p.registers;
            if (p.block_len % ops != 0 || (p.rounds * ops) % p.block_len != 0) return IGNPOW_E_PARAMS;
        }
    } else {
        if (!(p.registers == 8 || p.registers == 16)) return IGNPOW_E_PARAMS;
    }
    if (p.rounds < p.registers) return IGNPOW_E_PARAMS;
    if (p.test_only > 1) return IGNPOW_E_PARAMS;
    if (p.schema == 5) {
        // SPEC-TEXT-v0.2-A2A3.md section 11 (text revision 5): memory-free; the dataset, cache and parents are 0 and the
        // record's digest field is zero (no dataset builder is bound); a given cache_bytes or parents is refused unless 0
        if (p.dataset_bytes != 0 || p.cache_bytes != 0 || p.parents != 0) {
            if (!(p.dataset_bytes == 0 && p.cache_bytes == kCacheBytes && p.parents == 256)) return IGNPOW_E_PARAMS;  // the defaults only
            p.cache_bytes = 0; p.parents = 0;
        }
        std::memset(p.dataset_spec_digest, 0, 32);
        if (p.epoch_blocks < 1 || p.epoch_blocks > 0x100000000ull) return IGNPOW_E_PARAMS;
        if (p.test_only) p.schema = kTestSchema + (p.schema - 1);
        return IGNPOW_OK;
    }
    if (!(p.parents == 256 || p.parents == 512)) return IGNPOW_E_PARAMS;
    if (p.test_only) {
        if (!is_pow2(p.dataset_bytes) || p.dataset_bytes < (16ull << 20) || p.dataset_bytes > (4ull << 30)) return IGNPOW_E_PARAMS;
        if (!is_pow2(p.cache_bytes) || p.cache_bytes < (1ull << 20) || p.cache_bytes > kCacheBytes) return IGNPOW_E_PARAMS;
    } else {
        if (!(p.dataset_bytes == (2ull << 30) || p.dataset_bytes == (4ull << 30))) return IGNPOW_E_PARAMS;
        if (p.cache_bytes != kCacheBytes) return IGNPOW_E_PARAMS;
    }
    if (p.epoch_blocks < 1 || p.epoch_blocks > 0x100000000ull) return IGNPOW_E_PARAMS;
    // NOTE (REFERENCE-NOTES.md section 2, protocol B's form): a test-only record carries schema 0x54455354 ("TEST")
    // in its bytes, so its pid, seed and out never equal a production record's; a production validator refuses it.
    // NOTE: schema 2 under test carries 0x54455355 so that the two schemas' test records differ too.
    if (p.test_only) p.schema = kTestSchema + (p.schema - 1);
    return IGNPOW_OK;
}

size_t record_len(const rxe1_params& p) { const uint32_t v = schema_version_of(p.schema); return v == 4 ? 96 : (v == 3 || v == 5) ? 88 : 72; }

void record_bytes(const rxe1_params& p, uint8_t out[RXE1_RECORD_BYTES]) {
    size_t o = 0;
    std::memcpy(out, p.dataset_spec_digest, 32); o += 32;
    auto w64 = [&](uint64_t x) { for (int i = 0; i < 8; ++i) out[o++] = uint8_t(x >> (8 * i)); };
    auto w32 = [&](uint32_t x) { for (int i = 0; i < 4; ++i) out[o++] = uint8_t(x >> (8 * i)); };
    w64(p.dataset_bytes); w32(p.parents); w64(p.cache_bytes); w32(p.rounds); w32(p.registers); w32(p.blocks); w32(p.mode); w32(p.schema);
    if (record_len(p) >= 88) { w32(p.group); w32(p.route); w32(p.chunks); w32(p.payload_bytes); }
    if (record_len(p) == 96) { w32(p.block_len); w32(p.family); }
}

void pid_of(const rxe1_params& p, uint8_t out[32]) {
    uint8_t rec[RXE1_RECORD_BYTES];
    record_bytes(p, rec);
    enc e; e.tag("RXE1/params").lp(rec, record_len(p));
    H(e, out);
}

// ---- the context ----

struct ctx {
    rxe1_params p;
    uint8_t pid[32];
    uint8_t E[32];
    std::vector<uint8_t> cache;
    std::vector<uint8_t> dataset;  // empty in a light context
    uint64_t build_ms = 0;
    uint64_t n_items() const { return p.dataset_bytes / r0a::kItemBytes; }
};

void build_dataset(ctx& c) {
    const auto t0 = std::chrono::steady_clock::now();
    const uint64_t n = c.n_items();
    c.dataset.resize(size_t(c.p.dataset_bytes));
    unsigned nt = std::thread::hardware_concurrency();
    if (nt == 0) nt = 4;
    if (nt > 32) nt = 32;
    std::vector<std::thread> ts;
    for (unsigned t = 0; t < nt; ++t)
        ts.emplace_back([&, t]() { for (uint64_t i = t; i < n; i += nt) r0a::derive_item(c.E, c.cache, c.p.parents, i, c.dataset.data() + i * r0a::kItemBytes); });
    for (auto& th : ts) th.join();
    c.build_ms = uint64_t(std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count());
}

ctx* create(const rxe1_params& p, const uint8_t epoch_seed[32], int full, int* status) {
    auto c = std::make_unique<ctx>();
    c->p = p;
    pid_of(p, c->pid);
    std::memcpy(c->E, epoch_seed, 32);
    try {
        if (p.dataset_bytes != 0) {  // schema 5 (memory-free) builds nothing: the light and the full context are the same
            r0a::build_cache(c->E, p.cache_bytes, c->cache);
            if (full) build_dataset(*c);
        }
    } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    if (status) *status = IGNPOW_OK;
    return c.release();
}

// An aligned chunk of chunk_bytes (64, 128 or 256) at region index `region` of the dataset as a byte array; a light
// context derives the one or two items the chunk lies in.
void read_chunk(const ctx& c, uint64_t region, uint32_t chunk_bytes, uint8_t* out) {
    const uint64_t byte0 = region * chunk_bytes;
    if (!c.dataset.empty()) { std::memcpy(out, c.dataset.data() + byte0, chunk_bytes); return; }
    const uint64_t item0 = byte0 / r0a::kItemBytes;
    const uint32_t items = chunk_bytes > r0a::kItemBytes ? chunk_bytes / r0a::kItemBytes : 1;
    uint8_t buf[2 * r0a::kItemBytes];
    for (uint32_t i = 0; i < items; ++i) r0a::derive_item(c.E, c.cache, c.p.parents, item0 + i, buf + i * r0a::kItemBytes);
    std::memcpy(out, buf + (byte0 - item0 * r0a::kItemBytes), chunk_bytes);
}

// ---- the schedule of section 8 ----

struct descriptor { uint32_t src1, src2, rotate, opcode; };

// The program seed: F from pid, E from the epoch context, P (mode 3, SPEC-TEXT-v0.2-A2A3.md section 5) from the nonce's seed.
void program_seed_of(const ctx& c, const uint8_t* chain_id, size_t chain_len, uint64_t epoch_number, const uint8_t* seed, uint8_t out[32]) {
    enc e;
    if (c.p.mode == 1) { e.tag("RXE1/fixed").lp(c.pid, 32); }
    else if (c.p.mode == 3) { e.tag("RXE1/nonce-program").lp(seed, 32); }
    else { e.tag("RXE1/epoch").lp(chain_id, chain_len).lp(c.pid, 32).le64(epoch_number).lp(c.E, 32); }
    H(e, out);
}

// The schema version of a record: 1 or 2, also under the test marker.
inline uint32_t schema_version(const ctx& c) { return schema_version_of(c.p.schema); }

// descriptors[j][b][d]
void generate(const ctx& c, const uint8_t program_seed[32], std::vector<descriptor>& desc) {
    const uint32_t R = c.p.rounds, B = c.p.blocks, K = c.p.registers;
    desc.resize(size_t(R) * B * K);
    for (uint32_t j = 0; j < R; ++j)
        for (uint32_t b = 0; b < B; ++b)
            for (uint32_t d = 0; d < K; ++d) {
                enc e; e.tag("RXE1/op").lp(program_seed, 32).le32(j).le32(b).le32(d);
                uint8_t q[4];
                X(e, q, 4);
                descriptor& x = desc[(size_t(j) * B + b) * K + d];
                x.src1 = (d + 1 + q[0] % (K - 1)) % K;
                if (schema_version(c) >= 2) {  // schema 3 is schema 2 plus the A2 and A3 fields
                    // schema 2 (the project's decision of 10:5x UK on the security review's no-op finding): src2 is drawn from the K - 2
                    // registers other than d and src1: t = q[1] mod (K - 2) indexes them in ascending order of offset from d.
                    const uint32_t r1 = (x.src1 + K - d - 1) % K;  // src1's offset from d, 0..K-2
                    uint32_t t = q[1] % (K - 2);
                    if (t >= r1) ++t;
                    x.src2 = (d + 1 + t) % K;
                } else {
                    x.src2 = (d + 1 + q[1] % (K - 1)) % K;
                }
                x.rotate = 1 + q[2] % 31;
                x.opcode = (d + q[3] % 4) % 4;
            }
}

// A2 (SPEC-TEXT-v0.2-A2A3.md section 3): route 2's per-round, per-register descriptors from the epoch's routing seed.
struct route_desc { uint32_t off, src, rot, op; };

void route_seed_of(const ctx& c, const uint8_t* chain_id, size_t chain_len, uint64_t epoch_number, uint8_t out[32]) {
    enc e; e.tag("RXE1/route").lp(chain_id, chain_len).lp(c.pid, 32).le64(epoch_number).lp(c.E, 32);
    H(e, out);
}

void generate_routes(const ctx& c, const uint8_t rseed[32], std::vector<route_desc>& rd) {
    const uint32_t R = c.p.rounds, K = c.p.registers, G = c.p.group;
    rd.resize(size_t(R) * K);
    for (uint32_t j = 0; j < R; ++j)
        for (uint32_t k = 0; k < K; ++k) {
            enc e; e.tag("RXE1/route-op").lp(rseed, 32).le32(j).le32(k);
            uint8_t q[4];
            X(e, q, 4);
            route_desc& x = rd[size_t(j) * K + k];
            x.off = 1 + q[0] % (G - 1);
            x.src = q[1] % K;
            x.rot = 1 + q[2] % 31;
            x.op = q[3] % 2;
        }
}

// Mode S (section 9.3): the precompiled family, NB blocks of L descriptors from family_seed = H("RXE1/family", [pid]).
void family_seed_of(const ctx& c, uint8_t out[32]) { enc e; e.tag("RXE1/family").lp(c.pid, 32); H(e, out); }

void generate_family(const ctx& c, const uint8_t fseed[32], std::vector<descriptor>& fam) {
    const uint32_t L = c.p.block_len, NB = c.p.family, K = c.p.registers;
    fam.resize(size_t(NB) * L);
    for (uint32_t b = 0; b < NB; ++b)
        for (uint32_t i = 0; i < L; ++i) {
            enc e; e.tag("RXE1/family-op").lp(fseed, 32).le32(b).le32(i);
            uint8_t q[4];
            X(e, q, 4);
            descriptor& x = fam[size_t(b) * L + i];
            const uint32_t d = i % K;
            x.src1 = (d + 1 + q[0] % (K - 1)) % K;
            const uint32_t r1 = (x.src1 + K - d - 1) % K;
            uint32_t t = q[1] % (K - 2);
            if (t >= r1) ++t;
            x.src2 = (d + 1 + t) % K;
            x.rotate = 1 + q[2] % 31;
            x.opcode = (i + b) % 4;
        }
}

inline uint32_t op(uint32_t opcode, uint32_t x, uint32_t y, uint32_t z, uint32_t rot) {
    switch (opcode) {
        case 0: return x + rotl32(y ^ z, rot);
        case 1: return x ^ rotl32(y + z, rot);
        case 2: return x + y * (z | 1u);
        default: return rotl32(x ^ y, rot) + z;
    }
}

// ---- the loop of section 7 ----

struct state { uint32_t S[kMaxLanes][kMaxRegs]; };  // [lane][register], G of the lanes and K of the registers used

// Section 9.4: the selection fold over register (e mod K) of every lane.
inline uint32_t select_block(const state& s, uint32_t e, uint32_t K, uint32_t G, uint32_t NB) {
    uint32_t h = low32(uint64_t(e) * 0x9E3779B9ull);
    for (uint32_t l = 0; l < G; ++l) h ^= rotl32(s.S[l][e % K], l);
    h = (h ^ (h >> 16)) * 0x85EBCA6Bu;
    h = (h ^ (h >> 13)) * 0xC2B2AE35u;
    return (h ^ (h >> 16)) & (NB - 1);
}

void write_state(FILE* f, const state& s, uint32_t K, uint32_t G) {
    std::fprintf(f, "[");
    for (uint32_t l = 0; l < G; ++l) {
        std::fprintf(f, "%s[", l ? ", " : "");
        for (uint32_t k = 0; k < K; ++k) std::fprintf(f, "%s%u", k ? ", " : "", s.S[l][k]);
        std::fprintf(f, "]");
    }
    std::fprintf(f, "]");
}

std::string hexs(const uint8_t* p, size_t n) {
    static const char* d = "0123456789abcdef";
    std::string s; s.reserve(2 * n);
    for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; }
    return s;
}

// The compact trace digest of the differential campaign (protocol B's form): H("RXE1/trace-digest", [bytes]) with
// bytes = seed || initial state || per round (LE32 a || LE64 index || state after injection || the state after each
// block || the state after the exchange) || out, every state as lane-major LE32 words.
struct trace_digest {
    std::vector<uint8_t> bytes;
    void state(const struct state& s, uint32_t K, uint32_t G);
    void le32(uint32_t x) { for (int i = 0; i < 4; ++i) bytes.push_back(uint8_t(x >> (8 * i))); }
    void le64(uint64_t x) { for (int i = 0; i < 8; ++i) bytes.push_back(uint8_t(x >> (8 * i))); }
};

int eval(const ctx& c, const uint8_t* chain_id, size_t chain_len, const uint8_t* header, size_t header_len,
         uint64_t epoch_number, uint64_t nonce, uint8_t out[32], uint8_t seed_out[32], FILE* tr, trace_digest* td = nullptr,
         std::vector<uint32_t>* exec_log = nullptr) {  // mode S: (e, sel) pairs, for rxe1_block_sequence without a memory stream
    if (chain_len < 1 || chain_len > 64 || header_len > 4096) return IGNPOW_E_ARG;
    const uint32_t R = c.p.rounds, K = c.p.registers, B = c.p.blocks;
    const uint32_t sv = schema_version(c);
    const uint32_t G = sv >= 3 ? c.p.group : 16;
    const uint32_t route = sv >= 3 ? c.p.route : 1;
    const uint32_t C = sv >= 3 ? c.p.chunks : 1;
    const uint32_t payload = sv >= 3 ? c.p.payload_bytes : 128;
    const uint32_t chunk_bytes = payload / C;
    const bool memfree = sv == 5;
    const uint64_t region_mask = memfree ? 0 : (c.p.dataset_bytes / chunk_bytes) - 1;
    const uint32_t WPL = payload / 4 / G;
    uint8_t seed[32];
    { enc e; e.tag("RXE1/nonce").lp(chain_id, chain_len).lp(c.pid, 32).le64(epoch_number).lp(c.E, 32).lp(header, header_len).le64(nonce); H(e, seed); }
    if (seed_out) std::memcpy(seed_out, seed, 32);
    if (td) td->bytes.insert(td->bytes.end(), seed, seed + 32);
    state s;
    std::memset(&s, 0, sizeof s);
    {
        enc e; e.tag("RXE1/state").lp(seed, 32);
        std::vector<uint8_t> words(size_t(4) * G * K);
        X(e, words.data(), words.size());
        for (uint32_t l = 0; l < G; ++l)
            for (uint32_t k = 0; k < K; ++k) s.S[l][k] = r0a::le32(words.data() + 4 * (l * K + k));
    }
    if (td) td->state(s, K, G);
    const bool modeS = c.p.mode == 4;
    uint8_t pseed[32] = {0};
    std::vector<descriptor> desc;
    if (B > 0 && !modeS) { program_seed_of(c, chain_id, chain_len, epoch_number, seed, pseed); generate(c, pseed, desc); }
    std::vector<descriptor> fam;
    uint8_t fseed[32] = {0};
    const uint32_t L = modeS ? c.p.block_len : 0, NB = modeS ? c.p.family : 0, OPS = B * K;
    const uint32_t rounds_per_exec = modeS ? L / OPS : 0;
    if (modeS) { family_seed_of(c, fseed); generate_family(c, fseed, fam); }
    uint8_t rseed[32] = {0};
    std::vector<route_desc> routes;
    if (route == 2) { route_seed_of(c, chain_id, chain_len, epoch_number, rseed); generate_routes(c, rseed, routes); }
    if (tr) {
        std::fprintf(tr, "{\"pid\": \"%s\", \"chain_id\": \"%s\", \"epoch_seed\": \"%s\", \"epoch_number\": %llu, \"header_blob\": \"%s\", \"nonce\": \"%llu\", \"seed\": \"%s\", ",
                     hexs(c.pid, 32).c_str(), hexs(chain_id, chain_len).c_str(), hexs(c.E, 32).c_str(), (unsigned long long)epoch_number,
                     hexs(header, header_len).c_str(), (unsigned long long)nonce, hexs(seed, 32).c_str());
        if (B > 0 && !modeS) std::fprintf(tr, "\"program_seed\": \"%s\", ", hexs(pseed, 32).c_str());
        if (modeS) std::fprintf(tr, "\"family_seed\": \"%s\", ", hexs(fseed, 32).c_str());
        if (route == 2) std::fprintf(tr, "\"route_seed\": \"%s\", ", hexs(rseed, 32).c_str());
        std::fprintf(tr, "\"initial_state\": ");
        write_state(tr, s, K, G);
        std::fprintf(tr, ", \"rounds\": [");
    }
    std::vector<uint32_t> w(payload / 4);
    uint8_t chunk[256];
    for (uint32_t j = 0; j < R; ++j) {
        if (tr) std::fprintf(tr, "%s{\"j\": %u, ", j ? ", " : "", j);
        // the memory step (A3, section 2; at C = 1 and payload 128 the v0.1 address and lookup)
        for (uint32_t cc = 0; cc < C; ++cc) {
            uint32_t a = low32(uint64_t(j) * 0x9E3779B9ull) ^ low32(uint64_t(cc) * 0x7F4A7C15ull);
            for (uint32_t l = 0; l < G; ++l) a ^= rotl32(s.S[l][(j + cc) % K], l);
            a = (a ^ (a >> 16)) * 0x85EBCA6Bu;
            a = (a ^ (a >> 13)) * 0xC2B2AE35u;
            const uint64_t region = uint64_t(a ^ (a >> 16)) & region_mask;
            if (memfree) {
                // section 11: the chunk operand is a register read, word q of the payload from lane q / WPL, register
                // (j + q mod WPL + K / 2) mod K (never the register it is injected into), keyed by the round's fold a
                const uint32_t cw = chunk_bytes / 4;
                for (uint32_t k = 0; k < cw; ++k) { const uint32_t q = cc * cw + k, l = q / WPL, m = q % WPL; w[q] = s.S[l][(j + m + K / 2) % K] ^ a; }
            } else {
                read_chunk(c, region, chunk_bytes, chunk);
                for (uint32_t k = 0; k < chunk_bytes / 4; ++k) w[cc * (chunk_bytes / 4) + k] = r0a::le32(chunk + 4 * k);
            }
            if (td) { td->le32(a); if (!memfree) td->le64(region); }
            if (tr) {
                if (sv >= 3 && cc == 0) std::fprintf(tr, "\"chunks\": [");
                if (memfree) std::fprintf(tr, "%s{\"c\": %u, \"a\": %u}%s", cc ? ", " : "", cc, a, cc + 1 == C ? "], " : "");
                else if (sv >= 3) std::fprintf(tr, "%s{\"c\": %u, \"a\": %u, \"region\": %llu}%s", cc ? ", " : "", cc, a, (unsigned long long)region, cc + 1 == C ? "], " : "");
                else std::fprintf(tr, "\"a\": %u, \"index\": %llu, ", a, (unsigned long long)region);
            }
        }
        for (uint32_t l = 0; l < G; ++l)
            for (uint32_t m = 0; m < WPL; ++m) {
                if (m % 2 == 0) s.S[l][(j + m) % K] ^= w[WPL * l + m];
                else s.S[l][(j + m) % K] += w[WPL * l + m];
            }
        if (td) td->state(s, K, G);
        if (tr) {
            std::fprintf(tr, "\"%s\": [", sv >= 3 ? "payload" : "item");
            for (uint32_t k = 0; k < payload / 4; ++k) std::fprintf(tr, "%s%u", k ? ", " : "", w[k]);
            std::fprintf(tr, "], \"after_injection\": ");
            write_state(tr, s, K, G);
            std::fprintf(tr, modeS ? ", \"executions\": [" : ", \"blocks\": [");
        }
        if (modeS && (j + 1) % rounds_per_exec == 0) {
            // section 9.2 and 9.4: execution e in this round, the block from the live state, straight-line on every lane
            const uint32_t e = (j + 1) / rounds_per_exec - 1;
            const uint32_t sel = select_block(s, e, K, G, NB);
            if (tr) std::fprintf(tr, "{\"e\": %u, \"sel\": %u}], \"blocks\": [", e, sel);
            if (td) { td->le32(e); td->le32(sel); }
            if (exec_log) { exec_log->push_back(e); exec_log->push_back(sel); }
            for (uint32_t i = 0; i < L; ++i) {
                const descriptor& x = fam[size_t(sel) * L + i];
                const uint32_t d = i % K;
                for (uint32_t l = 0; l < G; ++l)
                    s.S[l][d] = op(x.opcode, s.S[l][d], s.S[l][x.src1], s.S[l][x.src2], x.rotate);
            }
            if (tr) write_state(tr, s, K, G);
            if (td) td->state(s, K, G);
        } else if (modeS) {
            if (tr) std::fprintf(tr, "], \"blocks\": [");
        }
        for (uint32_t b = 0; b < B && !modeS; ++b) {
            const state T = s;
            for (uint32_t d = 0; d < K; ++d) {
                const descriptor& x = desc[(size_t(j) * B + b) * K + d];
                for (uint32_t l = 0; l < G; ++l)
                    s.S[l][d] = op(x.opcode, T.S[l][d], T.S[l][x.src1], T.S[l][x.src2], x.rotate);
            }
            if (tr) { std::fprintf(tr, "%s", b ? ", " : ""); write_state(tr, s, K, G); }
            if (td) td->state(s, K, G);
        }
        // the exchange step (A2, section 3)
        if (route != 0) {
            const state T = s;
            for (uint32_t l = 0; l < G; ++l)
                for (uint32_t k = 0; k < K; ++k) {
                    if (route == 1) {
                        s.S[l][k] = T.S[l][k] + rotl32(T.S[(l + 1) % G][(k + 1) % K], 7);
                    } else if (route == 2) {
                        const route_desc& x = routes[size_t(j) * K + k];
                        const uint32_t v = rotl32(T.S[(l + x.off) % G][x.src], x.rot);
                        s.S[l][k] = x.op == 0 ? T.S[l][k] + v : (T.S[l][k] ^ mulhi32(T.S[l][k] | 1u, v)) + v;
                    } else {
                        const uint32_t off = 1 + ((T.S[l][(k + 1) % K] >> 16) % (G - 1));
                        s.S[l][k] = T.S[l][k] + rotl32(T.S[(l + off) % G][(k + 1) % K], 7);
                    }
                }
        }
        if (tr) { std::fprintf(tr, "], \"after_exchange\": "); write_state(tr, s, K, G); std::fprintf(tr, "}"); }
        if (td) td->state(s, K, G);
    }
    {
        std::vector<uint8_t> st(size_t(4) * G * K);
        for (uint32_t l = 0; l < G; ++l)
            for (uint32_t k = 0; k < K; ++k) r0a::put_le32(st.data() + 4 * (l * K + k), s.S[l][k]);
        enc e; e.tag("RXE1/result").lp(seed, 32).lp(st);
        H(e, out);
    }
    if (tr) std::fprintf(tr, "], \"out\": \"%s\"}", hexs(out, 32).c_str());
    if (td) td->bytes.insert(td->bytes.end(), out, out + 32);
    return IGNPOW_OK;
}

void trace_digest::state(const struct state& s, uint32_t K, uint32_t G) {
    for (uint32_t l = 0; l < G; ++l) for (uint32_t k = 0; k < K; ++k) le32(s.S[l][k]);
}

// ---- the ABI mapping (NOTE: REFERENCE-NOTES.md section 3) ----

void harness_epoch_seed(uint64_t chain_id, uint64_t epoch_number, uint8_t out[32]) {
    enc e; e.tag("RXE1/harness-epoch").le64(chain_id).le64(epoch_number);
    H(e, out);
}

struct abi_ctx final : ctx_base {
    std::unique_ptr<ctx> c;
    uint64_t ep = 0;
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_RXE1; }
};

int ab_check_params(const char* json) { rxe1_params p; return parse_params(json, p); }

uint64_t ab_epoch_of(const char* json, uint64_t height, int* status) {
    rxe1_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? height / p.epoch_blocks : 0;
}

uint64_t ab_cache_bytes(const char* json, uint64_t height, int* status) {
    rxe1_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? p.cache_bytes : 0;
}

uint64_t ab_dataset_bytes(const char* json, uint64_t height, int* status) {
    rxe1_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (status) *status = st;
    return st == IGNPOW_OK ? p.dataset_bytes : 0;
}

ctx_base* ab_create(const char* json, uint64_t height, int full, int* status) {
    rxe1_params p;
    int st = parse_params(json, p);
    if (st == IGNPOW_OK && height > kMaxHeight) st = IGNPOW_E_HEIGHT;
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    auto a = std::make_unique<abi_ctx>();
    a->ep = height / p.epoch_blocks;
    uint8_t E[32];
    harness_epoch_seed(p.chain_id, a->ep, E);
    a->c.reset(create(p, E, full, &st));
    if (!a->c) { if (status) *status = st; return nullptr; }
    if (status) *status = IGNPOW_OK;
    return a.release();
}

int ab_hash(const ctx_base* base, uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& a = *static_cast<const abi_ctx*>(base);
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height / a.c->p.epoch_blocks != a.ep) return IGNPOW_E_EPOCH;
    uint8_t cid[8];
    for (int i = 0; i < 8; ++i) cid[i] = uint8_t(a.c->p.chain_id >> (8 * i));
    return eval(*a.c, cid, 8, header, 32, a.ep, nonce, hash_out, mix_out, nullptr);
}

int ab_verify(const ctx_base* base, uint64_t height, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32];
    int st = ab_hash(base, height, header, nonce, m, h);
    if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(h, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}

int ab_light_cache(const ctx_base* base, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    const auto& a = *static_cast<const abi_ctx*>(base);
    if (data) *data = a.c->cache.data();
    if (bytes) *bytes = a.c->cache.size();
    if (item_bytes) *item_bytes = r0a::kItemBytes;
    return IGNPOW_OK;
}

bool hex_eq(const uint8_t* b, size_t n, const char* hex) {
    static const char* d = "0123456789abcdef";
    for (size_t i = 0; i < n; ++i) if (d[b[i] >> 4] != hex[2 * i] || d[b[i] & 15] != hex[2 * i + 1]) return false;
    return true;
}

// Self-test: the four opcodes on fixed operands (section 8's table by hand),
// LP framing, a document without a mandatory field refused, and the light
// path against the full path on a test-only 16 MiB variant of each mode.
// A failing self-test names its line on stderr when RXE1_DEBUG is set (the verdict is the return code either way).
#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "rxe1 self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)

int ab_vectors_check(size_t* count_out) {
    size_t n = 0;
    // opcodes: x = 0x01234567, y = 0x89ABCDEF, z = 0x0F0F0F0F, rotate 5
    const uint32_t x = 0x01234567u, y = 0x89ABCDEFu, z = 0x0F0F0F0Fu;
    if (op(0, x, y, z, 5) != x + rotl32(y ^ z, 5)) FAILV();
    if (op(1, x, y, z, 5) != (x ^ rotl32(y + z, 5))) FAILV();
    if (op(2, x, y, z, 5) != x + y * (z | 1u)) FAILV();
    if (op(3, x, y, z, 5) != rotl32(x ^ y, 5) + z) FAILV();
    if (rotl32(x, 0) != x || rotl32(x, 32) != x || rotl32(0x80000000u, 1) != 1u) FAILV();
    ++n;
    { enc e; e.tag("ab").le32(1); if (!hex_eq(e.v.data(), e.v.size(), "02000000" "6162" "04000000" "01000000")) FAILV(); ++n; }
    {
        rxe1_params p;
        if (parse_params("{\"schema\": 1, \"mode\": 0, \"blocks\": 0, \"rounds\": 32, \"registers\": 8}", p) != IGNPOW_E_PARAMS) FAILV();  // dataset_bytes missing
        if (parse_params("{\"schema\": 1, \"mode\": 0, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 2147483648}", p) != IGNPOW_E_PARAMS) FAILV();  // M with blocks
        if (parse_params("{\"schema\": 1, \"mode\": 1, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 1073741824}", p) != IGNPOW_E_PARAMS) FAILV();  // 1 GiB without test_only
        if (parse_params("{\"schema\": 1, \"mode\": 0, \"blocks\": 0, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 2147483648}", p) != IGNPOW_OK) FAILV();
        uint8_t rec[RXE1_RECORD_BYTES];
        record_bytes(p, rec);
        if (!hex_eq(rec + 32, 40, "0000008000000000" "00010000" "0000000200000000" "20000000" "08000000" "00000000" "00000000" "01000000")) FAILV();
        if (!hex_eq(rec, 32, "ead585e08733f3b891d3f4f9d77197d448a289c6334e0f274411832f62fc7109")) FAILV();  // the binding constant
        uint8_t pid[32];
        pid_of(p, pid);
        if (!hex_eq(pid, 32, "853b090471c7f6ef9562e0f5c7acc7fb7b66c4ed4d71860d091098705d1b6e9d")) FAILV();  // the M screening baseline
        ++n;
    }
    {
        // schema 2: no descriptor has src1 == src2 (the security review's no-op finding against v0.1), schema 1 keeps the text's draw
        rxe1_params p;
        if (parse_params("{\"schema\": 2, \"mode\": 2, \"blocks\": 4, \"rounds\": 64, \"registers\": 8, \"dataset_bytes\": 2147483648}", p) != IGNPOW_OK) FAILV();
        ctx c; c.p = p; pid_of(p, c.pid); std::memset(c.E, 7, 32);
        uint8_t ps[32]; const uint8_t cid[8] = {1, 2, 3, 4, 5, 6, 7, 8};
        program_seed_of(c, cid, 8, 3, nullptr, ps);
        std::vector<descriptor> d; generate(c, ps, d);
        for (const descriptor& x : d) if (x.src1 == x.src2 || x.src1 >= 8 || x.src2 >= 8 || x.rotate < 1 || x.rotate > 31 || x.opcode > 3) FAILV();
        uint8_t rec[RXE1_RECORD_BYTES]; record_bytes(p, rec);
        if (rec[68] != 2) FAILV();
        ++n;
    }
    {
        // schema 3 at the v0.1 point reproduces schema 2's loop word for word: same inputs and the same pid give the same out
        const char* s2 = "{\"schema\": 2, \"mode\": 2, \"blocks\": 2, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}";
        const char* s3 = "{\"schema\": 3, \"mode\": 2, \"blocks\": 2, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128}";
        rxe1_params p2, p3;
        if (parse_params(s2, p2) != IGNPOW_OK || parse_params(s3, p3) != IGNPOW_OK) FAILV();
        if (record_len(p3) != 88 || p3.schema != kTestSchema + 2) FAILV();
        uint8_t E[32]; std::memset(E, 9, 32);
        int st = 0;
        std::unique_ptr<ctx> c2(create(p2, E, 0, &st));
        std::unique_ptr<ctx> c3(create(p3, E, 0, &st));
        if (!c2 || !c3) FAILV();
        std::memcpy(c3->pid, c2->pid, 32);  // the only difference between the two is the record
        const uint8_t cid[8] = {1, 2, 3, 4, 5, 6, 7, 8};
        uint8_t hdr[40]; for (int i = 0; i < 40; ++i) hdr[i] = uint8_t(3 * i + 1);
        uint8_t o2[32], o3[32];
        if (eval(*c2, cid, 8, hdr, 40, 5, 77, o2, nullptr, nullptr) != IGNPOW_OK) FAILV();
        if (eval(*c3, cid, 8, hdr, 40, 5, 77, o3, nullptr, nullptr) != IGNPOW_OK) FAILV();
        if (std::memcmp(o2, o3, 32) != 0) FAILV();
        ++n;
        // route 2's descriptors: off never 0, in range
        rxe1_params pr; if (parse_params("{\"schema\": 3, \"mode\": 1, \"blocks\": 1, \"rounds\": 64, \"registers\": 4, \"group\": 8, \"route\": 2, \"chunks\": 2, \"payload_bytes\": 256, \"dataset_bytes\": 2147483648}", pr) != IGNPOW_OK) FAILV();
        ctx cr; cr.p = pr; pid_of(pr, cr.pid); std::memset(cr.E, 4, 32);
        uint8_t rs[32]; route_seed_of(cr, cid, 8, 2, rs);
        std::vector<route_desc> rd; generate_routes(cr, rs, rd);
        if (rd.size() != 256) FAILV();
        for (const route_desc& x : rd) if (x.off < 1 || x.off > 7 || x.src > 3 || x.rot < 1 || x.rot > 31 || x.op > 1) FAILV();
        if (mulhi32(0xFFFFFFFFu, 0xFFFFFFFFu) != 0xFFFFFFFEu || mulhi32(0x10000u, 0x10000u) != 1u) FAILV();
        ++n;
    }
    {
        // mode S: every block covers every opcode L/4 times and every destination L/K times; src1, src2 never d; src1 never src2
        rxe1_params p;
        if (parse_params("{\"schema\": 4, \"mode\": 4, \"blocks\": 1, \"rounds\": 64, \"registers\": 8, \"group\": 16, \"route\": 0, \"chunks\": 1, \"payload_bytes\": 256, \"block_len\": 16, \"family\": 32, \"dataset_bytes\": 2147483648}", p) != IGNPOW_OK) FAILV();
        if (record_len(p) != 96) FAILV();
        ctx c; c.p = p; pid_of(p, c.pid); std::memset(c.E, 5, 32);
        uint8_t fs[32]; family_seed_of(c, fs);
        std::vector<descriptor> fam; generate_family(c, fs, fam);
        if (fam.size() != 32 * 16) FAILV();
        for (uint32_t b = 0; b < 32; ++b) {
            uint32_t opc[4] = {0, 0, 0, 0};
            for (uint32_t i = 0; i < 16; ++i) {
                const descriptor& x = fam[b * 16 + i];
                const uint32_t d = i % 8;
                if (x.src1 == d || x.src2 == d || x.src1 == x.src2 || x.src1 >= 8 || x.src2 >= 8 || x.rotate < 1 || x.rotate > 31 || x.opcode > 3) FAILV();
                opc[x.opcode]++;
            }
            if (opc[0] != 4 || opc[1] != 4 || opc[2] != 4 || opc[3] != 4) FAILV();
        }
        if (parse_params("{\"schema\": 3, \"mode\": 4, \"blocks\": 1, \"rounds\": 64, \"registers\": 8, \"group\": 16, \"route\": 0, \"chunks\": 1, \"payload_bytes\": 256, \"dataset_bytes\": 2147483648}", p) != IGNPOW_E_PARAMS) FAILV();  // mode 4 needs schema 4
        {
            // schema 5 (section 11): memory-free; dataset_bytes 0 mandatory, no cache, no parents, a zero digest, 88 bytes, modes F, E, P2 only
            const char* s5 = "{\"schema\": 5, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 0}";
            if (parse_params(s5, p) != IGNPOW_OK || record_len(p) != 88 || p.cache_bytes != 0 || p.parents != 0) FAILV();
            for (int i = 0; i < 32; ++i) if (p.dataset_spec_digest[i]) FAILV();
            if (parse_params("{\"schema\": 5, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 2147483648}", p) != IGNPOW_E_PARAMS) FAILV();
            if (parse_params("{\"schema\": 5, \"mode\": 0, \"blocks\": 0, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 0}", p) != IGNPOW_E_PARAMS) FAILV();
            if (parse_params("{\"schema\": 5, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 0, \"cache_bytes\": 1048576}", p) != IGNPOW_E_PARAMS) FAILV();
            if (parse_params("{\"schema\": 5, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 0, \"test_only\": 1}", p) != IGNPOW_OK || p.schema != kTestSchema + 4) FAILV();
            ++n;
        }
        ++n;
        // mode S takes the schema 3 record's memory step (protocol B's finding: the reference read the v0.1 128-byte item at payload 256):
        // the trace of a test-only mode S point at payload 256 carries a 64-word payload and a chunks array
        // (not on Windows: MinGW has no open_memstream and the Windows worker never evaluates mode S)
#if !defined(_WIN32)
        {
            rxe1_params ps;
            if (parse_params("{\"schema\": 4, \"mode\": 4, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 3, \"chunks\": 2, \"payload_bytes\": 256, \"block_len\": 8, \"family\": 16, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}", ps) != IGNPOW_OK) FAILV();
            int st = 0;
            std::unique_ptr<ctx> cs(create(ps, c.E, 0, &st));
            if (!cs) FAILV();
            // the trace through a temporary file (std::tmpfile is portable; open_memstream is not on MinGW, the GPU team's finding)
            FILE* mem = std::tmpfile();
            if (!mem) FAILV();
            uint8_t o[32];
            const uint8_t cid[8] = {1, 2, 3, 4, 5, 6, 7, 8};
            if (eval(*cs, cid, 8, nullptr, 0, 1, 3, o, nullptr, mem) != IGNPOW_OK) { std::fclose(mem); FAILV(); }
            std::fflush(mem);
            const long len = std::ftell(mem);
            std::rewind(mem);
            std::string t(size_t(len), '\0');
            if (len > 0 && std::fread(&t[0], 1, size_t(len), mem) != size_t(len)) { std::fclose(mem); FAILV(); }
            std::fclose(mem);
            size_t pcount = 0;
            const size_t pp = t.find("\"payload\": [");
            if (pp == std::string::npos || t.find("\"chunks\": [{\"c\": 0") == std::string::npos || t.find("{\"c\": 1") == std::string::npos) FAILV();
            for (size_t q = pp + 12; q < t.size() && t[q] != ']'; ++q) if (t[q] == ',') ++pcount;
            if (pcount != 63) FAILV();  // 64 words = 256 bytes
        }
#endif
        ++n;
    }
    const char* variants[] = {
        "{\"schema\": 5, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 1, \"chunks\": 1, \"payload_bytes\": 128, \"dataset_bytes\": 0, \"test_only\": 1}",
        "{\"schema\": 5, \"mode\": 3, \"blocks\": 1, \"rounds\": 64, \"registers\": 16, \"group\": 32, \"route\": 3, \"chunks\": 2, \"payload_bytes\": 256, \"dataset_bytes\": 0, \"test_only\": 1}",
        "{\"schema\": 5, \"mode\": 1, \"blocks\": 4, \"rounds\": 64, \"registers\": 4, \"group\": 8, \"route\": 0, \"chunks\": 4, \"payload_bytes\": 256, \"dataset_bytes\": 0, \"test_only\": 1}",
        "{\"schema\": 4, \"mode\": 4, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"group\": 16, \"route\": 0, \"chunks\": 1, \"payload_bytes\": 256, \"block_len\": 8, \"family\": 32, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
        "{\"schema\": 4, \"mode\": 4, \"blocks\": 2, \"rounds\": 32, \"registers\": 8, \"group\": 8, \"route\": 3, \"chunks\": 2, \"payload_bytes\": 256, \"block_len\": 32, \"family\": 16, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
        "{\"schema\": 3, \"mode\": 1, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1, \"group\": 16, \"route\": 3, \"chunks\": 4, \"payload_bytes\": 256}",
        "{\"schema\": 3, \"mode\": 2, \"blocks\": 1, \"rounds\": 32, \"registers\": 4, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1, \"group\": 32, \"route\": 2, \"chunks\": 1, \"payload_bytes\": 256}",
        "{\"schema\": 3, \"mode\": 0, \"blocks\": 0, \"rounds\": 32, \"registers\": 16, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1, \"group\": 8, \"route\": 0, \"chunks\": 2, \"payload_bytes\": 128}",
        "{\"schema\": 3, \"mode\": 3, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1, \"group\": 16, \"route\": 3, \"chunks\": 1, \"payload_bytes\": 256}",
        "{\"schema\": 2, \"mode\": 2, \"blocks\": 2, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
        "{\"schema\": 1, \"mode\": 0, \"blocks\": 0, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
        "{\"schema\": 1, \"mode\": 1, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
        "{\"schema\": 1, \"mode\": 2, \"blocks\": 4, \"rounds\": 32, \"registers\": 16, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}",
    };
    for (const char* pj : variants) {
        int st = 0;
        std::unique_ptr<ctx_base> light(ab_create(pj, 1, 0, &st));
        if (!light || st != IGNPOW_OK) FAILV();
        std::unique_ptr<ctx_base> full(ab_create(pj, 1, 1, &st));
        if (!full || st != IGNPOW_OK) FAILV();
        uint8_t hdr[32];
        for (int i = 0; i < 32; ++i) hdr[i] = uint8_t(i * 5 + 1);
        for (uint64_t nonce = 0; nonce < 2; ++nonce) {
            uint8_t m1[32], h1[32], m2[32], h2[32];
            if (ab_hash(light.get(), 1, hdr, nonce, m1, h1) != IGNPOW_OK) FAILV();
            if (ab_hash(full.get(), 1, hdr, nonce, m2, h2) != IGNPOW_OK) FAILV();
            if (std::memcmp(m1, m2, 32) || std::memcmp(h1, h2, 32)) FAILV();
            ++n;
        }
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace rxe1

const candidate rxe1_family = {
    IGNPOW_ALGO_RXE1,
    rxe1::ab_check_params,
    rxe1::ab_epoch_of,
    rxe1::ab_cache_bytes,
    rxe1::ab_dataset_bytes,
    rxe1::ab_create,
    rxe1::ab_hash,
    rxe1::ab_verify,
    rxe1::ab_light_cache,
    rxe1::ab_vectors_check,
    "RXE1 experimental family (SPEC-TEXT-v0.1.md sections 6 to 8; dataset: the R0A v1 builder; BLAKE3 1.8.2 portable)",
};

}  // namespace ignpow

// ---- the harness C API (rxe1.h) ----

struct rxe1_ctx { ignpow::rxe1::ctx* c; };

extern "C" {

int rxe1_params_parse(const char* params_json, rxe1_params* out) {
    if (!out) return IGNPOW_E_ARG;
    return ignpow::rxe1::parse_params(params_json, *out);
}

void rxe1_record_bytes(const rxe1_params* p, uint8_t out[RXE1_RECORD_BYTES]) { if (p && out) ignpow::rxe1::record_bytes(*p, out); }
size_t rxe1_record_len(const rxe1_params* p) { return p ? ignpow::rxe1::record_len(*p) : 0; }
void rxe1_pid(const rxe1_params* p, uint8_t out[32]) { if (p && out) ignpow::rxe1::pid_of(*p, out); }
void rxe1_dataset_spec_digest(uint8_t out[32]) { if (out) ignpow::rxe1::spec_digest(out); }

void rxe1_variant_tag(const rxe1_params* p, char* buf, size_t n) {
    if (!p || !buf || n == 0) return;
    const char m = p->mode == 0 ? 'm' : p->mode == 1 ? 'f' : p->mode == 2 ? 'e' : p->mode == 3 ? 'p' : 's';
    const uint32_t sv = p->schema >= 0x54455354u ? p->schema - 0x54455354u + 1 : p->schema;
    if (sv == 5)
        std::snprintf(buf, n, "s5-%c-b%u-r%u-k%u-g%u-u%u-x%u-y%u", m, p->blocks, p->rounds, p->registers, p->group, p->route, p->chunks, p->payload_bytes);
    else if (sv == 4)
        std::snprintf(buf, n, "s4-%c-b%u-r%u-k%u-g%u-u%u-x%u-y%u-l%u-n%u-d%llu-p%u-c%llu", m, p->blocks, p->rounds, p->registers, p->group, p->route, p->chunks, p->payload_bytes,
                      p->block_len, p->family, (unsigned long long)(p->dataset_bytes >> 20), p->parents, (unsigned long long)(p->cache_bytes >> 20));
    else if (sv == 3)
        std::snprintf(buf, n, "s3-%c-b%u-r%u-k%u-g%u-u%u-x%u-y%u-d%llu-p%u-c%llu", m, p->blocks, p->rounds, p->registers, p->group, p->route, p->chunks, p->payload_bytes,
                      (unsigned long long)(p->dataset_bytes >> 20), p->parents, (unsigned long long)(p->cache_bytes >> 20));
    else if (sv == 1)
        std::snprintf(buf, n, "%c-b%u-r%u-k%u-d%llu-p%u-c%llu", m, p->blocks, p->rounds, p->registers,
                      (unsigned long long)(p->dataset_bytes >> 20), p->parents, (unsigned long long)(p->cache_bytes >> 20));
    else
        std::snprintf(buf, n, "s%u-%c-b%u-r%u-k%u-d%llu-p%u-c%llu", sv, m, p->blocks, p->rounds, p->registers,
                      (unsigned long long)(p->dataset_bytes >> 20), p->parents, (unsigned long long)(p->cache_bytes >> 20));
}

rxe1_ctx* rxe1_ctx_create(const rxe1_params* p, const uint8_t epoch_seed[32], int full, int* status) {
    if (!p || !epoch_seed) { if (status) *status = IGNPOW_E_ARG; return nullptr; }
    ignpow::rxe1::ctx* c = ignpow::rxe1::create(*p, epoch_seed, full, status);
    if (!c) return nullptr;
    return new rxe1_ctx{c};
}

void rxe1_ctx_free(rxe1_ctx* ctx) { if (ctx) { delete ctx->c; delete ctx; } }

const uint8_t* rxe1_ctx_cache(const rxe1_ctx* ctx, uint64_t* bytes_out) {
    if (!ctx) return nullptr;
    if (bytes_out) *bytes_out = ctx->c->cache.size();
    return ctx->c->cache.data();
}

const uint8_t* rxe1_ctx_dataset(const rxe1_ctx* ctx, uint64_t* bytes_out) {
    if (!ctx) return nullptr;
    if (bytes_out) *bytes_out = ctx->c->dataset.size();
    return ctx->c->dataset.empty() ? nullptr : ctx->c->dataset.data();
}

void rxe1_ctx_digests(const rxe1_ctx* ctx, uint8_t cache_digest[32], uint8_t dataset_digest[32], int* has_dataset) {
    if (!ctx) return;
    blake3_hasher h;
    if (cache_digest) { blake3_hasher_init(&h); blake3_hasher_update(&h, ctx->c->cache.data(), ctx->c->cache.size()); blake3_hasher_finalize(&h, cache_digest, 32); }
    if (dataset_digest) {
        std::memset(dataset_digest, 0, 32);
        if (!ctx->c->dataset.empty()) { blake3_hasher_init(&h); blake3_hasher_update(&h, ctx->c->dataset.data(), ctx->c->dataset.size()); blake3_hasher_finalize(&h, dataset_digest, 32); }
    }
    if (has_dataset) *has_dataset = ctx->c->dataset.empty() ? 0 : 1;
}

int rxe1_eval(const rxe1_ctx* ctx, const uint8_t* chain_id, size_t chain_id_len, const uint8_t* header_blob, size_t header_len,
              uint64_t epoch_number, uint64_t nonce, uint8_t out[32], uint8_t seed_out[32], FILE* trace) {
    if (!ctx || !chain_id || (!header_blob && header_len) || !out) return IGNPOW_E_ARG;
    static const uint8_t empty = 0;
    return ignpow::rxe1::eval(*ctx->c, chain_id, chain_id_len, header_blob ? header_blob : &empty, header_len, epoch_number, nonce, out, seed_out, trace);
}

int rxe1_trace_digest(const rxe1_ctx* ctx, const uint8_t* chain_id, size_t chain_id_len, const uint8_t* header_blob, size_t header_len,
                      uint64_t epoch_number, uint64_t nonce, uint8_t out[32], uint8_t digest_out[32]) {
    if (!ctx || !chain_id || (!header_blob && header_len) || !out || !digest_out) return IGNPOW_E_ARG;
    static const uint8_t empty = 0;
    ignpow::rxe1::trace_digest td;
    int st = ignpow::rxe1::eval(*ctx->c, chain_id, chain_id_len, header_blob ? header_blob : &empty, header_len, epoch_number, nonce, out, nullptr, nullptr, &td);
    if (st != IGNPOW_OK) return st;
    ignpow::rxe1::enc e; e.tag("RXE1/trace-digest").lp(td.bytes);
    ignpow::rxe1::H(e, digest_out);
    return IGNPOW_OK;
}

int rxe1_schedule(const rxe1_ctx* ctx, const uint8_t* chain_id, size_t chain_id_len, uint64_t epoch_number, FILE* out) {
    if (!ctx || !chain_id || !out || chain_id_len < 1 || chain_id_len > 64) return IGNPOW_E_ARG;
    const auto& c = *ctx->c;
    if (c.p.mode == 3) return IGNPOW_E_ARG;  // a P2 program is a nonce's: rxe1_schedule_for_nonce
    if (c.p.mode == 4) return rxe1_family(ctx, out);  // mode S: the family is the schedule
    uint8_t ps[32] = {0};
    std::vector<ignpow::rxe1::descriptor> desc;
    if (c.p.blocks > 0) { ignpow::rxe1::program_seed_of(c, chain_id, chain_id_len, epoch_number, nullptr, ps); ignpow::rxe1::generate(c, ps, desc); }
    if (c.p.blocks > 0)
        std::fprintf(out, "{\"mode\": %u, \"program_seed\": \"%s\", \"rounds\": %u, \"blocks\": %u, \"registers\": %u, \"descriptors\": [",
                     c.p.mode, ignpow::rxe1::hexs(ps, 32).c_str(), c.p.rounds, c.p.blocks, c.p.registers);
    else
        std::fprintf(out, "{\"mode\": 0, \"program_seed\": null, \"descriptors\": [");
    size_t i = 0;
    for (uint32_t j = 0; j < c.p.rounds; ++j)
        for (uint32_t b = 0; b < c.p.blocks; ++b)
            for (uint32_t d = 0; d < c.p.registers; ++d, ++i) {
                const auto& x = desc[i];
                std::fprintf(out, "%s{\"j\": %u, \"b\": %u, \"d\": %u, \"src1\": %u, \"src2\": %u, \"rotate\": %u, \"opcode\": %u}",
                             i ? ", " : "", j, b, d, x.src1, x.src2, x.rotate, x.opcode);
            }
    std::fprintf(out, "]");
    if (ignpow::rxe1::schema_version(c) == 3 && c.p.route == 2) {
        uint8_t rs[32];
        ignpow::rxe1::route_seed_of(c, chain_id, chain_id_len, epoch_number, rs);
        std::vector<ignpow::rxe1::route_desc> rd;
        ignpow::rxe1::generate_routes(c, rs, rd);
        std::fprintf(out, ", \"route_seed\": \"%s\", \"routes\": [", ignpow::rxe1::hexs(rs, 32).c_str());
        size_t i = 0;
        for (uint32_t j = 0; j < c.p.rounds; ++j)
            for (uint32_t k = 0; k < c.p.registers; ++k, ++i)
                std::fprintf(out, "%s{\"j\": %u, \"k\": %u, \"off\": %u, \"src\": %u, \"rot\": %u, \"op\": %u}", i ? ", " : "", j, k, rd[i].off, rd[i].src, rd[i].rot, rd[i].op);
        std::fprintf(out, "]");
    }
    std::fprintf(out, "}");
    return IGNPOW_OK;
}

int rxe1_schedule_for_nonce(const rxe1_ctx* ctx, const uint8_t* chain_id, size_t chain_id_len, const uint8_t* header_blob, size_t header_len,
                            uint64_t epoch_number, uint64_t nonce, FILE* out) {
    if (!ctx || !chain_id || !out || chain_id_len < 1 || chain_id_len > 64 || header_len > 4096 || (!header_blob && header_len)) return IGNPOW_E_ARG;
    const auto& c = *ctx->c;
    if (c.p.mode != 3) return rxe1_schedule(ctx, chain_id, chain_id_len, epoch_number, out);
    static const uint8_t empty = 0;
    uint8_t seed[32];
    { ignpow::rxe1::enc e; e.tag("RXE1/nonce").lp(chain_id, chain_id_len).lp(c.pid, 32).le64(epoch_number).lp(c.E, 32).lp(header_blob ? header_blob : &empty, header_len).le64(nonce); ignpow::rxe1::H(e, seed); }
    uint8_t ps[32];
    ignpow::rxe1::program_seed_of(c, chain_id, chain_id_len, epoch_number, seed, ps);
    std::vector<ignpow::rxe1::descriptor> desc;
    ignpow::rxe1::generate(c, ps, desc);
    std::fprintf(out, "{\"mode\": 3, \"nonce\": \"%llu\", \"seed\": \"%s\", \"program_seed\": \"%s\", \"rounds\": %u, \"blocks\": %u, \"registers\": %u, \"descriptors\": [",
                 (unsigned long long)nonce, ignpow::rxe1::hexs(seed, 32).c_str(), ignpow::rxe1::hexs(ps, 32).c_str(), c.p.rounds, c.p.blocks, c.p.registers);
    size_t i = 0;
    for (uint32_t j = 0; j < c.p.rounds; ++j)
        for (uint32_t b = 0; b < c.p.blocks; ++b)
            for (uint32_t d = 0; d < c.p.registers; ++d, ++i)
                std::fprintf(out, "%s{\"j\": %u, \"b\": %u, \"d\": %u, \"src1\": %u, \"src2\": %u, \"rotate\": %u, \"opcode\": %u}",
                             i ? ", " : "", j, b, d, desc[i].src1, desc[i].src2, desc[i].rotate, desc[i].opcode);
    std::fprintf(out, "]}");
    return IGNPOW_OK;
}

int rxe1_family(const rxe1_ctx* ctx, FILE* out) {
    if (!ctx || !out) return IGNPOW_E_ARG;
    const auto& c = *ctx->c;
    if (c.p.mode != 4) return IGNPOW_E_ARG;
    uint8_t fs[32];
    ignpow::rxe1::family_seed_of(c, fs);
    std::vector<ignpow::rxe1::descriptor> fam;
    ignpow::rxe1::generate_family(c, fs, fam);
    std::fprintf(out, "{\"mode\": 4, \"family_seed\": \"%s\", \"block_len\": %u, \"family\": %u, \"registers\": %u, \"blocks\": [", ignpow::rxe1::hexs(fs, 32).c_str(), c.p.block_len, c.p.family, c.p.registers);
    for (uint32_t b = 0; b < c.p.family; ++b) {
        std::fprintf(out, "%s[", b ? ", " : "");
        for (uint32_t i = 0; i < c.p.block_len; ++i) {
            const auto& x = fam[size_t(b) * c.p.block_len + i];
            std::fprintf(out, "%s{\"i\": %u, \"d\": %u, \"src1\": %u, \"src2\": %u, \"rotate\": %u, \"opcode\": %u}", i ? ", " : "", i, i % c.p.registers, x.src1, x.src2, x.rotate, x.opcode);
        }
        std::fprintf(out, "]");
    }
    std::fprintf(out, "]}");
    return IGNPOW_OK;
}

int rxe1_block_sequence(const rxe1_ctx* ctx, const uint8_t* chain_id, size_t chain_id_len, const uint8_t* header_blob, size_t header_len, uint64_t epoch_number, uint64_t nonce, FILE* out) {
    if (!ctx || !chain_id || !out || (!header_blob && header_len)) return IGNPOW_E_ARG;
    if (ctx->c->p.mode != 4) return IGNPOW_E_ARG;
    // the sequence is logged by the evaluation itself (no memory stream: the workers cross-build for Windows, the GPU team's finding)
    uint8_t o[32], sd[32];
    static const uint8_t empty = 0;
    std::vector<uint32_t> log;
    const int st = ignpow::rxe1::eval(*ctx->c, chain_id, chain_id_len, header_blob ? header_blob : &empty, header_len, epoch_number, nonce, o, sd, nullptr, nullptr, &log);
    if (st != IGNPOW_OK) return st;
    std::fprintf(out, "{\"mode\": 4, \"nonce\": \"%llu\", \"seed\": \"%s\", \"out\": \"%s\", \"executions\": [", (unsigned long long)nonce, ignpow::rxe1::hexs(sd, 32).c_str(), ignpow::rxe1::hexs(o, 32).c_str());
    for (size_t i = 0; i + 1 < log.size(); i += 2) std::fprintf(out, "%s{\"e\": %u, \"sel\": %u}", i ? ", " : "", log[i], log[i + 1]);
    std::fprintf(out, "]}");
    return IGNPOW_OK;
}

void rxe1_harness_epoch_seed(uint64_t chain_id, uint64_t epoch_number, uint8_t out[32]) { if (out) ignpow::rxe1::harness_epoch_seed(chain_id, epoch_number, out); }

}  // extern "C"
