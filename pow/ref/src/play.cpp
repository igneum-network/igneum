// Candidate "igneum-play": WorldWork's first exact world (C-33 inside C-34), the scalar reference written to the
// memory-cryptanalysis seat's rules, docs/igneumhash/pogc/PLAY-MICROGAME-RULES.md at lead/pogc-reference 6ec69ea1e
// (sections 1 to 14: the six-stage tick, the per-period draw, the WorldWork re-base with the four coordinate tags, every
// stage a drawn programme, the chunk walks per tag, the programme lengths at the grammar's top, the readings adopted) and
// PROOF-OF-PLAY-BASELINE.md. A world of 32 objects on a 32 by 32 map advanced through T exact ticks per attempt; each tick
// six stages (the INT8 16x16x16 tile with the drawn composition and vertex programmes, the integer coverage with the drawn
// distortion, pattern and value programmes, the dataset texel with the drawn address programme and the L-chunk walk with its
// drawn reduction tree, the drawn state programme, the grid walk with the drawn navigation, particle and collision-key
// programmes, the chained fold with its drawn combining sequence); the ticket one BLAKE3 at the end. Every value is a
// two's-complement integer; the dataset is IgneumHash's (the registered documents' one dataset, SPEC-R0E-v0 section 5a),
// viewed as 8 layers of 16,384 by 4,096 R32_UINT texels. The readings this text fixes where the rules leave a choice are
// marked READING and listed in PLAY-REFERENCE.md.
#include "candidate.hpp"
#include "ignpow.h"
#include "blake3.h"
#include <algorithm>
#include <cstdio>
#include <cstring>
#include <memory>
#include <string>
#include <vector>
#include <chrono>
#include <ctime>
#if defined(__AVX512VNNI__) && defined(__AVX512BW__)
#include <immintrin.h>
#endif

namespace ignpow {
ctx_base* ih_create_public(const char* json, uint64_t height, int full, int* status);
ctx_base* ih_create_shared(const char* json, uint64_t height, const ctx_base* donor, int* status);
int ih_dataset_word(const ctx_base* ctx, uint64_t word, uint32_t* out);
int ih_dataset_words(const ctx_base* ctx, uint64_t word, uint32_t n, uint32_t* out);
int ih_light_cache_public(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes);

namespace play {
constexpr const char* kTagAttempt = "Igneum/Play/attempt";
constexpr const char* kTagWorld = "Igneum/Play/world";
constexpr const char* kTagDraw = "Igneum/Play/draw";
constexpr const char* kTagTicket = "Igneum/Play/ticket";
constexpr const char* kTagMix = "Igneum/Play/mix";
constexpr uint32_t kObj = 32, kMap = 16, kCells = kMap * kMap, kScreen = 256, kMaxParticles = 256, kMaxL = 16, kDatasetWords = 1u << 29;  // section 23: FB 16, MAP 16, the cell 16 units, the screen [0, 256) in Q.4
constexpr uint64_t kMaxHeight = 0x7fffffffull;  // the ABI's height range, as the other candidates
constexpr uint32_t kMul1 = 0x9E3779B1u, kMul2 = 0x85EBCA6Bu;
enum tag_t : uint32_t { RASTER = 0, SIMULATION = 1, TERRAIN = 2, GRAPH = 3 };

inline uint32_t rotl(uint32_t x, uint32_t r) { r &= 31; return r ? (x << r) | (x >> (32 - r)) : x; }
inline uint32_t popc(uint32_t x) { return uint32_t(__builtin_popcount(x)); }
inline uint32_t clz(uint32_t x) { return x ? uint32_t(__builtin_clz(x)) : 32u; }
inline uint32_t le32(const uint8_t* p) { return uint32_t(p[0]) | (uint32_t(p[1]) << 8) | (uint32_t(p[2]) << 16) | (uint32_t(p[3]) << 24); }
inline void put32(uint8_t* p, uint32_t v) { for (int i = 0; i < 4; ++i) p[i] = uint8_t(v >> (8 * i)); }
inline void lp(blake3_hasher& h, const char* t) { const uint8_t n = uint8_t(std::strlen(t)); blake3_hasher_update(&h, &n, 1); blake3_hasher_update(&h, t, n); }
inline void le64(blake3_hasher& h, uint64_t x) { uint8_t b[8]; for (int i = 0; i < 8; ++i) b[i] = uint8_t(x >> (8 * i)); blake3_hasher_update(&h, b, 8); }
inline uint32_t sgn64(uint32_t v) { const uint32_t m = v & 127; return uint32_t(int32_t(m >= 64 ? int32_t(m) - 128 : int32_t(m))); }  // reduced to [-64, 64)

// the fold F(x; v...): the version 11 fill fold with its finaliser (SPEC-R0E-v0 section 2b)
struct fold {
    uint32_t x;
    explicit fold(uint32_t x0) : x(x0) {}
    fold& v(uint32_t w) { x = rotl(x ^ w, 5) + w; return *this; }
    uint32_t fin() const { uint32_t t = x; t ^= t >> 16; t *= kMul1; t ^= t >> 15; t *= kMul2; t ^= t >> 13; return t; }
};

// the envelope (section 15): {XOR, ROTL, ADD, SUB, MULHI, POPC, CLZ} with the weights of 256 as a cumulative byte table (XOR 0 to 63,
// ROTL 64 to 127, ADD 128 to 159, SUB 160 to 175, MULHI 176 to 215, POPC 216 to 235, CLZ 236 to 255) and the forms: XOR d, a, b;
// ROTL d, a, imm mod 32; ADD and SUB mod 2^32; MULHI the high 32 bits of the unsigned product; POPC as R0D's a XOR popcount(b) x
// 0x9E3779B1; CLZ as R0D's a + clz(b) x 0x85EBCA77
enum : uint8_t { OP_XOR = 0, OP_ROTL, OP_ADD, OP_SUB, OP_MULHI, OP_POPC, OP_CLZ };
inline uint8_t opcode_of(uint8_t q) { if (q < 64) return OP_XOR; if (q < 128) return OP_ROTL; if (q < 160) return OP_ADD; if (q < 176) return OP_SUB; if (q < 216) return OP_MULHI; if (q < 236) return OP_POPC; return OP_CLZ; }
inline uint32_t apply(uint8_t opc, uint32_t a, uint32_t b, uint32_t imm) {
    switch (opc) {
        case OP_XOR: return a ^ b;
        case OP_ROTL: return rotl(a, imm & 31);
        case OP_ADD: return a + b;
        case OP_SUB: return a - b;
        case OP_MULHI: return uint32_t((uint64_t(a) * uint64_t(b)) >> 32);
        case OP_POPC: return a ^ (popc(b) * 0x9E3779B1u);
        default: return a + clz(b) * 0x85EBCA77u;
    }
}
// a drawn programme (section 15): n operations of eight bytes each read from X in order: byte 0 the opcode, byte 1 d (mod the
// sub-grammar's register count), byte 2 a (a register, mod the register count), byte 3 b (a register or, where the sub-grammar names
// extra operands, the operand selector mod the selector count), bytes 4 to 7 the LE32 immediate. The file F holds the registers in
// [0, nr) and the named extras after them; a selector under nr is a register.
struct op_t { uint8_t opc, d, a, b; uint32_t imm; };
struct prog_t { std::vector<op_t> ops; uint8_t nr = 8, nsel = 8; };
inline void run(const prog_t& p, uint32_t* F) { for (const op_t& o : p.ops) F[o.d] = apply(o.opc, F[o.a], F[o.b], o.imm); }

// the document (section 9's grammar, sections 11 and 13's registered values per tag)
struct params_t {
    uint64_t chain_id = 7310, tag = RASTER, ticks = 0, objects = 32, map = 16, fb = 16, steps = 0, particles = UINT64_MAX, chunk_walk = UINT64_MAX, chunk_bytes = 0, prog_len = 32;
    uint64_t period_blocks = 300, epoch_blocks = 4096, dataset_mib = 2048, cache_mib = 32, parents = 256, item_bytes = 256;
    uint8_t anchor[32] = {};
    bool ok = false;
};
bool parse_u64(const std::string& s, uint64_t& v) {
    if (s.empty() || s.size() > 20) return false; v = 0;
    for (char c : s) { if (c < '0' || c > '9') return false; if (v > (UINT64_MAX - 9) / 10) return false; v = v * 10 + uint64_t(c - '0'); }
    return true;
}
params_t parse(const char* p) {
    params_t r;
    std::string t; if (p) for (const char* c = p; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') t += *c;
    if (!(t.empty() || t == "null" || t == "{}")) {
        if (t.front() != '{' || t.back() != '}') return r;
        std::string body = t.substr(1, t.size() - 2); size_t pos = 0;
        while (pos < body.size()) {
            if (body[pos] != '"') return r;
            size_t q = body.find('"', pos + 1); if (q == std::string::npos) return r;
            std::string key = body.substr(pos + 1, q - pos - 1); pos = q + 1;
            if (pos >= body.size() || body[pos] != ':') return r; ++pos;
            size_t e = body.find(',', pos);
            std::string val = body.substr(pos, e == std::string::npos ? std::string::npos : e - pos);
            pos = e == std::string::npos ? body.size() : e + 1;
            if (key == "program_anchor") {
                if (val.size() != 66 || val.front() != '"' || val.back() != '"') return r;
                for (int i = 0; i < 32; ++i) { unsigned b = 0; for (int k = 0; k < 2; ++k) { const char ch = val[1 + 2 * i + k]; unsigned d; if (ch >= '0' && ch <= '9') d = unsigned(ch - '0'); else if (ch >= 'a' && ch <= 'f') d = unsigned(ch - 'a' + 10); else if (ch >= 'A' && ch <= 'F') d = unsigned(ch - 'A' + 10); else return r; b = (b << 4) | d; } r.anchor[i] = uint8_t(b); }
                continue;
            }
            uint64_t v = 0; if (!parse_u64(val, v)) return r;
            if (key == "chain_id") r.chain_id = v; else if (key == "tag") r.tag = v; else if (key == "ticks") r.ticks = v; else if (key == "objects") r.objects = v;
            else if (key == "map") r.map = v; else if (key == "fb") r.fb = v; else if (key == "steps") r.steps = v; else if (key == "particles") r.particles = v;
            else if (key == "chunk_walk") r.chunk_walk = v; else if (key == "chunk_bytes") r.chunk_bytes = v; else if (key == "prog_len") r.prog_len = v;
            else if (key == "period_blocks") r.period_blocks = v; else if (key == "epoch_blocks") r.epoch_blocks = v;
            else if (key == "dataset_mib") r.dataset_mib = v; else if (key == "cache_mib") r.cache_mib = v;
            else if (key == "parents") r.parents = v; else if (key == "item_bytes") r.item_bytes = v;
            else return r;
        }
    }
    if (r.tag > GRAPH) return r;
    // the registered values per tag (sections 11 and 13); a document field inside the grammar's bounds overrides them
    if (r.ticks == 0) r.ticks = 4;  // section 21: T = 4 at every tag
    if (r.steps == 0) r.steps = r.tag == GRAPH ? 16 : 12;
    if (r.particles == UINT64_MAX) r.particles = 64;  // section 23: 64 particles at four 16-bit words each
    if (r.chunk_walk == UINT64_MAX) r.chunk_walk = r.tag == TERRAIN ? 4 : 1;  // section 24: Terrain's registered pair T 4 with L 4 (T 2 at L 8 the alternative)
    if (r.chunk_bytes == 0) r.chunk_bytes = r.tag == TERRAIN ? 256 : 64;
    if (r.ticks < 1 || r.ticks > 128 || (r.ticks & (r.ticks - 1))) return r;
    if (r.objects != kObj || r.map != kMap || r.fb != kMap) return r;  // fixed in this registration (map and fb 16, section 23)
    if (r.steps < 4 || r.steps > 16 || r.particles > kMaxParticles || r.chunk_walk > kMaxL || (r.chunk_bytes != 64 && r.chunk_bytes != 256)) return r;
    if (r.prog_len < 8 || r.prog_len > 32 || r.period_blocks < 1 || r.epoch_blocks < 1) return r;
    if (r.dataset_mib != 2048 || r.item_bytes != 256 || r.cache_mib < 1 || r.parents < 1) return r;  // the 8-layer texture view is the 2 GiB dataset at W256
    r.ok = true; return r;
}
// the base document: the registered k8l document's fields with the play document's dataset fields, so the play context's
// cache and dataset are the registered documents' one dataset (section 5a)
std::string base_json(const params_t& p) {
    char b[640];
    std::snprintf(b, sizeof b, "{\"version\": 14, \"dataset_mib\": %llu, \"scratch_kib\": 0, \"rounds\": 64, \"lanes\": 8, \"epoch_blocks\": %llu, \"item_chain\": 1, \"chain_id\": %llu, \"cache_mib\": %llu, \"parents\": %llu, \"item_bytes\": %llu, \"blocks\": 1, \"registers\": 8, \"features\": 8448, \"program_ops\": 128, \"period_blocks\": 1, \"table_kib\": 1, \"access_bytes\": 16, \"shared_kib\": 32, \"mma_max\": 22, \"chains_per_nonce\": 1, \"gather_lane_bank\": 1, \"tile_shape\": 1, \"fold_every\": 8, \"layout_draw\": 1}",
                  (unsigned long long)p.dataset_mib, (unsigned long long)p.epoch_blocks, (unsigned long long)p.chain_id, (unsigned long long)p.cache_mib, (unsigned long long)p.parents, (unsigned long long)p.item_bytes);
    return b;
}

// the period's world (sections 6, 9, 10 and 13)
struct world_t {
    uint8_t wall[kCells];
    int8_t field[kCells][2];    // the particles' force per cell
    int parent[kObj];
    uint8_t cls[kObj];
    uint8_t layout[8];
    uint16_t target[kObj];
    uint8_t orders[8];
    uint32_t tex[4];
    uint32_t bounce;
    prog_t comp[4], vertex, distort, pattern, value, address, state[4], navigate, particle, collkey;
    op_t red[6];                           // the chunk reduction tree's combining operation per level (a over the two children)
    op_t foldseq[64];                      // stage 6's per-word combining sequence (the running x and the word v)
    op_t tree[5];                          // section 23: the five-level coupling tree over the 32 lane chains
    uint8_t seed[32];
    uint64_t period;
};
constexpr uint8_t kOrders[6][4] = { {2, 3, 4, 5}, {2, 4, 3, 5}, {2, 4, 5, 3}, {4, 2, 3, 5}, {4, 2, 5, 3}, {4, 5, 2, 3} };  // the six admissible orders (section 14)
struct xof_t {
    blake3_hasher h; std::vector<uint8_t> buf; size_t wp = 0;
    explicit xof_t(const uint8_t seed[32]) { blake3_hasher_init(&h); lp(h, kTagDraw); blake3_hasher_update(&h, seed, 32); buf.resize(8192); blake3_hasher_finalize_seek(&h, 0, buf.data(), buf.size()); }
    uint32_t word() { if ((wp + 1) * 4 > buf.size()) { const size_t old = buf.size(); buf.resize(old * 2); blake3_hasher_finalize_seek(&h, old, buf.data() + old, old); } const uint32_t v = le32(buf.data() + 4 * wp); ++wp; return v; }
};
op_t draw_op(xof_t& X, uint8_t nr, uint8_t nsel) {
    const uint32_t w0 = X.word(), w1 = X.word();  // the eight bytes: opcode, d, a, b, then the LE32 immediate
    return { opcode_of(uint8_t(w0 & 255)), uint8_t(((w0 >> 8) & 255) % nr), uint8_t(((w0 >> 16) & 255) % nr), uint8_t(((w0 >> 24) & 255) % nsel), w1 };
}
void draw_prog(xof_t& X, prog_t& p, uint32_t n, uint8_t nr, uint8_t nsel) {
    p.nr = nr; p.nsel = nsel; p.ops.resize(n);
    for (uint32_t j = 0; j < n; ++j) p.ops[j] = draw_op(X, nr, nsel);
}
void draw_world(const params_t& p, uint64_t height, world_t& w) {
    w.period = height / p.period_blocks;
    { blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagWorld); le64(h, p.chain_id); le64(h, w.period); blake3_hasher_update(&h, p.anchor, 32); blake3_hasher_finalize(&h, w.seed, 32); }
    xof_t X(w.seed);
    uint8_t a[kCells], b[kCells];
    for (uint32_t c = 0; c < kCells; ++c) a[c] = (X.buf[c >> 3] >> (c & 7)) & 1;  // 256 bits, 32 bytes
    X.wp = kCells / 32;
    for (int step = 0; step < 2; ++step) {
        for (int y = 0; y < int(kMap); ++y) for (int x = 0; x < int(kMap); ++x) {
            int n = 0;
            for (int dy = -1; dy <= 1; ++dy) for (int dx = -1; dx <= 1; ++dx) { if (!dx && !dy) continue; const int xx = x + dx, yy = y + dy; n += (xx < 0 || yy < 0 || xx >= int(kMap) || yy >= int(kMap)) ? 1 : a[yy * kMap + xx]; }
            b[y * kMap + x] = (n >= 5 || (a[y * kMap + x] && n >= 4)) ? 1 : 0;
        }
        std::memcpy(a, b, sizeof a);
    }
    for (int y = 0; y < int(kMap); ++y) for (int x = 0; x < int(kMap); ++x) if (x == 0 || y == 0 || x == int(kMap) - 1 || y == int(kMap) - 1) a[y * kMap + x] = 0;
    std::memcpy(w.wall, a, sizeof a);
    w.parent[0] = -1;
    for (uint32_t i = 1; i < kObj; ++i) w.parent[i] = int(X.word() % i);
    auto depth = [&](int i) { int d = 0; while (w.parent[i] >= 0) { i = w.parent[i]; ++d; } return d; };
    for (uint32_t i = 1; i < kObj; ++i) while (depth(int(i)) > 4) { const int gp = w.parent[w.parent[i]]; w.parent[i] = gp >= 0 ? w.parent[gp] : -1; }
    for (uint32_t i = 0; i < kObj; ++i) w.cls[i] = uint8_t(X.word() & 3);
    // stage 4's four programmes (section 13: prog_len per object; the selectors 0 to 7 the registers under LAYOUT, 8 tex, 9 FBV,
    // 10 shf; operations 5, 10 and 15 read tex, FBV and shf, byte 3 overwritten after the draw)
    for (int k = 0; k < 4; ++k) { draw_prog(X, w.state[k], uint32_t(p.prog_len), 8, 11); for (size_t j = 0; j < w.state[k].ops.size(); ++j) { if (j == 5) w.state[k].ops[j].b = 8; else if (j == 10) w.state[k].ops[j].b = 9; else if (j == 15) w.state[k].ops[j].b = 10; } }
    for (int i = 0; i < 8; ++i) w.layout[i] = uint8_t(i);
    for (int k = 7; k > 0; --k) { const int j = int(X.word() % uint32_t(k + 1)); std::swap(w.layout[k], w.layout[j]); }
    for (uint32_t i = 0; i < kObj; ++i) { uint32_t t; do { t = X.word() % kCells; } while (w.wall[t]); w.target[i] = uint16_t(t); }
    for (int i = 0; i < 8; ++i) w.orders[i] = uint8_t(X.word() % 6);
    for (int i = 1; i < 4; ++i) w.tex[i] = X.word();
    w.tex[0] = 0;
    w.bounce = X.word();
    // section 9: the particles' field, two signed bytes per cell
    for (uint32_t c = 0; c < kCells; ++c) { const uint32_t v = X.word(); w.field[c][0] = int8_t(uint8_t(v)); w.field[c][1] = int8_t(uint8_t(v >> 8)); }
    // sections 10, 13 and 15: the drawn programmes of every stage, in this order, each programme's bytes contiguous; the register
    // count and the selector count per sub-grammar as section 15 names them (READING for the composition: the child's 32 words the
    // registers, the selectors 0 to 31 the child's and 32 to 63 the parent's words)
    for (int l = 0; l < 4; ++l) draw_prog(X, w.comp[l], 8, 32, 64);
    draw_prog(X, w.vertex, 32, 32, 32);                                      // section 26: once per object over its 32 words (word 31 the seed F(h; i)), emitting the eight live words in R[0..8)
    draw_prog(X, w.distort, 8, 8, 14);                                       // stage 2's selectors: 0 to 7 the registers, 8 to 10 the edge values, 11 px, 12 py, 13 t; section 25: the distortion 8 operations
    draw_prog(X, w.pattern, 16, 8, 14);                                      // section 25: the pattern predicate 16 operations
    draw_prog(X, w.value, 16, 8, 14);
    draw_prog(X, w.address, 16, 8, 9);                                       // 0 to 7 the registers, 8 the covered-cell fold
    for (int l = 0; l < 6; ++l) w.red[l] = draw_op(X, 2, 2);                 // the two children of the tree node
    draw_prog(X, w.navigate, 8, 8, 4);                                       // 0 to 3 (dx, dy, r0, step)
    draw_prog(X, w.particle, 16, 8, 6);                                      // 0 to 3 (px, py, pvx, pvy), 4 to 5 the field's two words
    draw_prog(X, w.collkey, 8, 8, 2);                                        // 0 id, 1 r3
    for (int j = 0; j < 64; ++j) w.foldseq[j] = draw_op(X, 2, 2);            // the running x and the word v
    for (int l = 0; l < 5; ++l) w.tree[l] = draw_op(X, 2, 2);                 // the coupling tree's levels (left and right)
}

struct play_ctx final : ctx_base {
    params_t p; uint64_t ep = 0; std::unique_ptr<ctx_base> base;
    uint64_t epoch() const override { return ep; }
    const char* algo() const override { return IGNPOW_ALGO_IGNEUM_PLAY; }
};

struct counts_t { uint64_t drawn[7] = {}, fixed[7] = {}, reads = 0, bytes = 0, steps = 0, covered = 0; };
// the profile (igneum_play_profile): the calling thread's CPU time per stage, the dataset reads inside stage 3 apart, in nanoseconds
struct prof_t { uint64_t ns[8] = {}; uint64_t ticks = 0; };  // 1 to 6 the stages, 7 the dataset reads (inside stage 3's figure), 0 the tile inside stage 1
static thread_local prof_t* g_prof = nullptr;
inline uint64_t cpu_ns() { return uint64_t(std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch()).count()); }  // the monotonic clock (vDSO) on a pinned thread: a CPU-time clock is a syscall and would be the largest term itself
struct prof_scope { int k; uint64_t t0; explicit prof_scope(int kk) : k(kk), t0(g_prof ? cpu_ns() : 0) {} ~prof_scope() { if (g_prof) g_prof->ns[k] += cpu_ns() - t0; } };  // per stage (index 1 to 6): the drawn and fixed operations; the memory reads and bytes; the walk steps and covered cells
struct state_t {
    uint32_t obj[kObj][8];
    uint32_t fb[kCells], fbv[kCells], occ[kCells];
    uint32_t tex[kObj];
    uint32_t part[kMaxParticles][4];
    uint32_t h;
};
struct tick_trace {
    uint32_t h = 0; uint8_t order = 0;
    int8_t b[256]; uint32_t w[kObj][32]; uint32_t vert[kObj][8]; uint32_t z[kObj]; uint32_t live[kObj][8];
    uint32_t texc[kObj]; uint64_t texw[kObj]; uint32_t texv[kObj]; uint64_t chunk[kObj][kMaxL]; uint32_t chunkf[kObj][kMaxL];
    uint32_t walk_cell[kObj], walk_steps[kObj], walk_blocked[kObj], collk[kObj];
    uint32_t fb[kCells], fbv[kCells], occ[kCells], obj[kObj][8], part[kMaxParticles][4];
};
inline uint32_t cell_of(uint32_t x, uint32_t y) { return ((y & (kScreen - 1)) >> 4) * kMap + ((x & (kScreen - 1)) >> 4); }

// stage 2's covered(t, p): the integer edge functions under the normalised orientation with the top-left rule
struct tri_t { int64_t ax, ay, bx, by, cx, cy; bool empty; };
inline int64_t edge(int64_t ax, int64_t ay, int64_t bx, int64_t by, int64_t px, int64_t py) { return (bx - ax) * (py - ay) - (by - ay) * (px - ax); }
tri_t make_tri(const uint32_t v[8], int c0, int c1, int c2) {
    tri_t t{int64_t(v[2 * c0]), int64_t(v[2 * c0 + 1]), int64_t(v[2 * c1]), int64_t(v[2 * c1 + 1]), int64_t(v[2 * c2]), int64_t(v[2 * c2 + 1]), false};
    const int64_t area = edge(t.ax, t.ay, t.bx, t.by, t.cx, t.cy);
    if (area == 0) t.empty = true; else if (area < 0) { std::swap(t.bx, t.cx); std::swap(t.by, t.cy); }
    return t;
}
inline bool top_left(int64_t ax, int64_t ay, int64_t bx, int64_t by) { const int64_t dx = bx - ax, dy = by - ay; return (dy == 0 && dx > 0) || dy < 0; }
inline bool covered(const tri_t& t, int64_t px, int64_t py, int64_t e[3]) {
    e[0] = edge(t.ax, t.ay, t.bx, t.by, px, py); e[1] = edge(t.bx, t.by, t.cx, t.cy, px, py); e[2] = edge(t.cx, t.cy, t.ax, t.ay, px, py);
    const bool tl0 = top_left(t.ax, t.ay, t.bx, t.by), tl1 = top_left(t.bx, t.by, t.cx, t.cy), tl2 = top_left(t.cx, t.cy, t.ax, t.ay);
    return (e[0] > 0 || (e[0] == 0 && tl0)) && (e[1] > 0 || (e[1] == 0 && tl1)) && (e[2] > 0 || (e[2] == 0 && tl2));
}

// the INT8 16x16x16 tile, exact INT32: the scalar text and the vector form (VNNI dpbusd with the +128 offset and the column-sum correction)
void tile16_scalar(const int8_t A[16][16], const int8_t B[16][16], uint32_t C[16][16]) {
    for (uint32_t m = 0; m < 16; ++m) for (uint32_t n = 0; n < 16; ++n) { int32_t t = 0; for (uint32_t k = 0; k < 16; ++k) t += int32_t(A[m][k]) * int32_t(B[k][n]); C[m][n] = uint32_t(t); }
}
#if defined(__AVX512VNNI__) && defined(__AVX512BW__)
bool tile16_vec(const int8_t A[16][16], const int8_t B[16][16], uint32_t C[16][16]) {
    alignas(64) uint32_t Bw[4][16];  // Bword[g][n] = the bytes B[4g..4g+3][n]
    for (uint32_t g = 0; g < 4; ++g) for (uint32_t n = 0; n < 16; ++n) { uint32_t w = 0; for (uint32_t j = 0; j < 4; ++j) w |= uint32_t(uint8_t(B[4 * g + j][n])) << (8 * j); Bw[g][n] = w; }
    const __m512i ones = _mm512_set1_epi32(0x01010101);
    __m512i sb = _mm512_setzero_si512();
    for (uint32_t g = 0; g < 4; ++g) sb = _mm512_dpbusd_epi32(sb, ones, _mm512_load_si512(reinterpret_cast<const void*>(Bw[g])));
    for (uint32_t m = 0; m < 16; ++m) {
        __m512i acc = _mm512_setzero_si512();
        for (uint32_t g = 0; g < 4; ++g) { uint32_t aw = 0; for (uint32_t j = 0; j < 4; ++j) aw |= uint32_t(uint8_t(A[m][4 * g + j]) ^ 0x80u) << (8 * j); acc = _mm512_dpbusd_epi32(acc, _mm512_set1_epi32(int(aw)), _mm512_load_si512(reinterpret_cast<const void*>(Bw[g]))); }
        acc = _mm512_sub_epi32(acc, _mm512_slli_epi32(sb, 7));
        _mm512_storeu_si512(reinterpret_cast<void*>(C[m]), acc);
    }
    return true;
}
#else
inline bool tile16_vec(const int8_t[16][16], const int8_t[16][16], uint32_t[16][16]) { return false; }
#endif

void stage1(const world_t& w, state_t& s, uint32_t hp, uint64_t t, uint32_t W[kObj][32], uint32_t vert[kObj][8], uint32_t z[kObj], uint32_t live[kObj][8], int8_t bout[256], counts_t& cn) {
    int8_t B[16][16];
    for (uint32_t k = 0; k < 256; ++k) { const uint32_t f = fold(hp).v(k).fin(); B[k >> 4][k & 15] = int8_t(uint8_t(f & 255) ^ 0x80); bout[k] = B[k >> 4][k & 15]; }
    for (uint32_t g = 0; g < 4; ++g) {
        int8_t A[16][16]; uint32_t C[16][16];
        for (uint32_t j = 0; j < 8; ++j) for (uint32_t q = 0; q < 8; ++q) { const uint32_t word = s.obj[8 * g + j][q]; for (int b = 0; b < 4; ++b) A[2 * j + (q >> 2)][4 * (q & 3) + b] = int8_t(uint8_t(word >> (8 * b))); }
        { prof_scope ps(0); if (!tile16_vec(A, B, C)) tile16_scalar(A, B, C); }
        for (uint32_t m = 0; m < 16; ++m) for (uint32_t n = 0; n < 16; ++n) W[8 * g + (m >> 1)][16 * (m & 1) + n] = C[m][n];
    }
    cn.fixed[1] += 4 * 4096;
    // the composition programme per scene-graph level over the child's and the parent's rows (parents precede children)
    for (uint32_t i = 0; i < kObj; ++i) if (w.parent[i] >= 0) {
        int level = 0; for (int a = w.parent[i]; a >= 0; a = w.parent[a]) ++level;
        uint32_t R[64]; std::memcpy(R, W[i], 128); std::memcpy(R + 32, W[w.parent[i]], 128);
        run(w.comp[(level - 1) & 3], R); std::memcpy(W[i], R, 128); cn.drawn[1] += w.comp[(level - 1) & 3].ops.size();
    }
    // section 26: the vertex programme CONSUMES the tile's 32 words once per object (word 31 the seed F(h; i)) and emits the eight live
    // words R[0..8): R[c] the corner c's bounded offsets (x in the low byte, y in the second, each [-4, 4) in Q.4 on section 3's half-cell
    // offsets), R[4] the depth, R[5] the coverage seed, R[6] and R[7] the two texture words; the 32 are dead after it
    (void)t;
    static const int32_t kDx[4] = { -8, 8, 8, -8 }, kDy[4] = { -8, -8, 8, 8 };
    for (uint32_t i = 0; i < kObj; ++i) {
        uint32_t R[32]; std::memcpy(R, W[i], 128); R[31] = fold(hp).v(i).fin();
        run(w.vertex, R); cn.drawn[1] += w.vertex.ops.size();
        for (uint32_t c = 0; c < 4; ++c) {
            vert[i][2 * c] = uint32_t(int32_t(s.obj[i][0]) + kDx[c] + int32_t(R[c] & 7) - 4) & (kScreen - 1);
            vert[i][2 * c + 1] = uint32_t(int32_t(s.obj[i][1]) + kDy[c] + int32_t((R[c] >> 8) & 7) - 4) & (kScreen - 1);
        }
        z[i] = R[4] & 0xFFFFFFu;
        for (int k = 0; k < 8; ++k) live[i][k] = R[k];
    }
}
void stage2(const world_t& w, state_t& s, uint32_t hp, uint64_t t, const uint32_t vert[kObj][8], const uint32_t z[kObj], const uint32_t live[kObj][8], counts_t& cn) {
    for (uint32_t c = 0; c < kCells; ++c) s.fb[c] = 0xFFFFFFFFu;  // the keys cleared every tick, the values persist (section 14)
    // stage 2's file (section 15): the registers 0 to 7, then the extras 8 to 10 the edge values, 11 px, 12 py, 13 t; the registers'
    // initial words are the sub-grammar's inputs (READING, section 26): distortion (h, i, tri, z, 0, 0, 0, 0); pattern (h, i, tri, z,
    // seed, tex0, 0, 0); value (FBV, i, seed, tex0, tex1, z, 0, 0), the seed and the texture words the object's live words
    auto test = [&](const tri_t& tr, uint32_t i, int tnum, int64_t px, int64_t py, int64_t e[3]) {
        // the distortion (section 20, the dial of 11:2x): the pixel centre moved by the drawn offset bounded to one cell, at most 16
        // units in Q.4 per axis ([-16, 16), READING), before the edge test
        uint32_t D[14] = { hp, i, uint32_t(tnum), z[i], 0, 0, 0, 0, 0, 0, 0, uint32_t(px), uint32_t(py), uint32_t(t) }; run(w.distort, D); cn.drawn[2] += w.distort.ops.size();
        const int64_t qx = px + int64_t(D[0] & 31) - 16, qy = py + int64_t(D[1] & 31) - 16;
        cn.fixed[2] += 9;
        if (!covered(tr, qx, qy, e)) return false;
        // the pattern predicate must hold too, so the rasteriser's coverage alone never decides
        uint32_t P[14] = { hp, i, uint32_t(tnum), z[i], live[i][5], live[i][6], 0, 0, uint32_t(uint64_t(e[0])), uint32_t(uint64_t(e[1])), uint32_t(uint64_t(e[2])), uint32_t(px), uint32_t(py), uint32_t(t) }; run(w.pattern, P); cn.drawn[2] += w.pattern.ops.size();
        return (P[0] & 3) != 0;  // READING: the predicate holds on three quarters of the drawn words
    };
    for (uint32_t i = 0; i < kObj; ++i) {
        const uint32_t key = (z[i] << 8) | i;
        for (int tnum = 0; tnum < 2; ++tnum) {
            const tri_t tr = tnum == 0 ? make_tri(vert[i], 0, 1, 2) : make_tri(vert[i], 0, 2, 3);
            if (tr.empty) continue;
            const int64_t x0 = std::max<int64_t>(0, std::min({tr.ax, tr.bx, tr.cx}) / 16 - 1), x1 = std::min<int64_t>(kMap - 1, std::max({tr.ax, tr.bx, tr.cx}) / 16 + 1);
            const int64_t y0 = std::max<int64_t>(0, std::min({tr.ay, tr.by, tr.cy}) / 16 - 1), y1 = std::min<int64_t>(kMap - 1, std::max({tr.ay, tr.by, tr.cy}) / 16 + 1);
            for (int64_t py = y0; py <= y1; ++py) for (int64_t px = x0; px <= x1; ++px) { int64_t e[3]; if (test(tr, i, tnum, 16 * px + 8, 16 * py + 8, e)) { uint32_t& k = s.fb[py * kMap + px]; if (key < k) k = key; } }
        }
    }
    uint8_t done[kCells]; std::memset(done, 0xFF, sizeof done);  // the object that already wrote the cell's value this tick
    for (uint32_t i = 0; i < kObj; ++i) {
        const uint32_t key = (z[i] << 8) | i;
        for (int tnum = 0; tnum < 2; ++tnum) {
            const tri_t tr = tnum == 0 ? make_tri(vert[i], 0, 1, 2) : make_tri(vert[i], 0, 2, 3);
            if (tr.empty) continue;
            const int64_t x0 = std::max<int64_t>(0, std::min({tr.ax, tr.bx, tr.cx}) / 16 - 1), x1 = std::min<int64_t>(kMap - 1, std::max({tr.ax, tr.bx, tr.cx}) / 16 + 1);
            const int64_t y0 = std::max<int64_t>(0, std::min({tr.ay, tr.by, tr.cy}) / 16 - 1), y1 = std::min<int64_t>(kMap - 1, std::max({tr.ay, tr.by, tr.cy}) / 16 + 1);
            for (int64_t py = y0; py <= y1; ++py) for (int64_t px = x0; px <= x1; ++px) {
                const uint32_t c = uint32_t(py * kMap + px); if (s.fb[c] != key || done[c] == i) continue;
                int64_t e[3]; if (!test(tr, i, tnum, 16 * px + 8, 16 * py + 8, e)) continue;
                done[c] = uint8_t(i);
                uint32_t V[14] = { s.fbv[c], i, live[i][5], live[i][6], live[i][7], z[i], 0, 0, uint32_t(uint64_t(e[0])), uint32_t(uint64_t(e[1])), uint32_t(uint64_t(e[2])), uint32_t(px), uint32_t(py), uint32_t(t) };
                run(w.value, V); cn.drawn[2] += w.value.ops.size();
                s.fbv[c] = V[0];
            }
        }
    }
}
// the chunk reduction: the chunk's words reduced by a tree whose combining operation and rotation per level are the draw's
uint32_t reduce_chunk(const world_t& w, const uint32_t* words, uint32_t n, counts_t& cn) {
    uint32_t v[64]; std::memcpy(v, words, 4 * n);
    // each level's drawn operation over the node's two children (a and b the left and right child by the draw's bytes); a ROTL level
    // takes rotl(left, imm) XOR right so the right child enters too (READING)
    for (uint32_t m = n, l = 0; m > 1; m /= 2, ++l) {
        const op_t& o = w.red[l];
        for (uint32_t i = 0; i < m / 2; ++i) { const uint32_t ch[2] = { v[2 * i], v[2 * i + 1] }; v[i] = o.opc == OP_ROTL ? (rotl(ch[o.a], o.imm) ^ ch[o.b]) : apply(o.opc, ch[o.a], ch[o.b], o.imm); }
        cn.drawn[3] += m / 2;
    }
    return v[0];
}
int stage3(const play_ctx& cx, const world_t& w, state_t& s, uint32_t hp, uint64_t t, uint32_t tex0, uint32_t texc[kObj], uint64_t texw[kObj], uint64_t chunk[kObj][kMaxL], uint32_t chunkf[kObj][kMaxL], counts_t& cn) {
    const uint32_t cw = uint32_t(cx.p.chunk_bytes / 4), L = uint32_t(cx.p.chunk_walk);
    // the covered-cell fold per object in one pass over the cells in raster order (the same fold the per-object scan gives)
    fold fo[kObj] = { fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp),
                      fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp), fold(hp) };
    bool any[kObj] = {};
    for (uint32_t c = 0; c < kCells; ++c) { const uint32_t o = s.fb[c] & 255; if (o < kObj) { fo[o].v(s.fb[c]); any[o] = true; ++cn.covered; } }
    for (uint32_t i = 0; i < kObj; ++i) {
        if (!any[i]) fo[i].v(i).v(0);
        const uint32_t ci = fo[i].fin();
        uint32_t R[9] = { hp, i, w.tex[1], w.tex[2], w.tex[3], tex0, uint32_t(t), 0, ci }; run(w.address, R); cn.drawn[3] += w.address.ops.size();  // the registers (h, i, tex1, tex2, tex3, tex0, t, 0), the selector 8 the covered-cell fold
        const uint32_t l = R[0] & 7, u = R[1] & 16383, v = R[2] & 4095;
        const uint64_t word = (uint64_t(l) << 26) + uint64_t(v) * 16384 + u;
        uint32_t tv = 0; int st; { prof_scope ps(7); st = ih_dataset_word(cx.base.get(), word, &tv); } if (st != IGNPOW_OK) return st;
        cn.reads += 1; cn.bytes += 4;
        texc[i] = ci; texw[i] = word;
        // the chunk walk: the first chunk at the texel's address, each next from the fold of the previous chunk with r1
        uint32_t last = tv; uint64_t addr = word & ~uint64_t(cw - 1);
        for (uint32_t k = 0; k < L; ++k) {
            uint32_t buf[64]; { prof_scope ps(7); st = ih_dataset_words(cx.base.get(), addr, cw, buf); } if (st != IGNPOW_OK) return st;
            cn.reads += 1; cn.bytes += cw * 4;
            const uint32_t fo = reduce_chunk(w, buf, cw, cn);
            chunk[i][k] = addr; chunkf[i][k] = fo;
            last = fo ^ tv;
            addr = (uint64_t(fo ^ s.obj[i][5]) % (kDatasetWords / cw)) * cw;
        }
        s.tex[i] = last;
    }
    return IGNPOW_OK;
}
void stage4(const world_t& w, state_t& s, uint32_t hp, counts_t& cn) {
    uint32_t S0[kObj][8]; std::memcpy(S0, s.obj, sizeof S0);
    const uint32_t d = hp % 7;
    for (uint32_t i = 0; i < kObj; ++i) {
        const uint32_t cell = cell_of(S0[i][0], S0[i][1]);
        const uint32_t j = 8 * (i >> 3) + (((i & 7) + 1 + d) & 7);  // the shuffle source within the octet (section 14)
        uint32_t R[11]; for (int k = 0; k < 8; ++k) R[k] = s.obj[i][w.layout[k]];
        R[8] = s.tex[i]; R[9] = s.fbv[cell]; R[10] = S0[j][w.layout[0]];
        run(w.state[w.cls[i]], R); cn.drawn[4] += w.state[w.cls[i]].ops.size();
        for (int k = 0; k < 8; ++k) s.obj[i][w.layout[k]] = R[k];
        s.obj[i][0] &= kScreen - 1; s.obj[i][1] &= kScreen - 1;
        s.obj[i][2] = sgn64(s.obj[i][2]); s.obj[i][3] = sgn64(s.obj[i][3]);
    }
}
void stage5(const world_t& w, const params_t& p, state_t& s, uint32_t hp, uint32_t wc[kObj], uint32_t wsteps[kObj], uint32_t wblk[kObj], uint32_t ckey[kObj], counts_t& cn) {
    uint32_t occ0[kCells]; std::memcpy(occ0, s.occ, sizeof occ0);
    uint32_t oldc[kObj], newc[kObj];
    for (uint32_t i = 0; i < kObj; ++i) {
        int cx = int((s.obj[i][0] & (kScreen - 1)) >> 4), cy = int((s.obj[i][1] & (kScreen - 1)) >> 4);
        oldc[i] = uint32_t(cy * kMap + cx);
        const int tx = int(w.target[i] % kMap), ty = int(w.target[i] / kMap);
        uint32_t steps = 0, blocked = 0;
        auto free_cell = [&](int x, int y) { cn.fixed[5] += 2; const uint32_t c = uint32_t(y * kMap + x); return !w.wall[c] && (occ0[c] == 0 || occ0[c] == i + 1); };
        for (uint64_t st = 0; st < p.steps; ++st) {
            const int dx = tx - cx, dy = ty - cy;
            if (dx == 0 && dy == 0) break;
            // the navigation programme: the axis (the larger delta or the other) and whether to retry on the other axis
            uint32_t N[8] = { uint32_t(dx), uint32_t(dy), s.obj[i][4], uint32_t(st), hp, i, 0, 0 }; run(w.navigate, N); cn.drawn[5] += w.navigate.ops.size();  // the registers (dx, dy, r0, step, h, i, 0, 0), the selectors 0 to 3 the first four
            bool xfirst = std::abs(dx) >= std::abs(dy); if (N[0] & 1) xfirst = !xfirst;
            if (xfirst && dx == 0) xfirst = false; if (!xfirst && dy == 0) xfirst = true;
            int nx = cx, ny = cy;
            if (xfirst) nx += dx > 0 ? 1 : -1; else ny += dy > 0 ? 1 : -1;
            if (!free_cell(nx, ny)) {
                nx = cx; ny = cy;
                const int od = xfirst ? dy : dx;
                if (od == 0 || (N[1] & 1)) { blocked = 1; break; }
                if (xfirst) ny += od > 0 ? 1 : -1; else nx += od > 0 ? 1 : -1;
                if (!free_cell(nx, ny)) { blocked = 1; break; }
            }
            cx = nx; cy = ny; ++steps;
        }
        newc[i] = uint32_t(cy * kMap + cx); wsteps[i] = steps; wblk[i] = blocked; cn.steps += steps;
        uint32_t K[8] = { i, s.obj[i][7], hp, 0, 0, 0, 0, 0 }; run(w.collkey, K); cn.drawn[5] += w.collkey.ops.size(); ckey[i] = K[0];  // the registers (id, r3, h, 0 ...), the selectors 0 id and 1 r3
    }
    // the particles: the field's force through the drawn programme, the wall bounce, the object hits folded into r2
    uint32_t hit[kObj] = {};
    for (uint64_t q = 0; q < p.particles; ++q) {
        uint32_t* pt = s.part[q];
        const uint32_t c0 = cell_of(pt[0], pt[1]);
        uint32_t P[8] = { pt[0], pt[1], pt[2], pt[3], uint32_t(int32_t(w.field[c0][0])), uint32_t(int32_t(w.field[c0][1])), uint32_t(q), hp }; run(w.particle, P); cn.drawn[5] += w.particle.ops.size();
        uint32_t pvx = sgn64(P[2]), pvy = sgn64(P[3]);
        uint32_t nx = (pt[0] + pvx) & (kScreen - 1), ny = (pt[1] + pvy) & (kScreen - 1);
        const uint32_t c1 = cell_of(nx, ny);
        cn.fixed[5] += 4;
        if (w.wall[c1]) {  // the bounce: the axis crossed reflects and halves, the position stays
            if ((nx >> 4) != (pt[0] >> 4)) pvx = uint32_t(-int32_t(pvx) / 2);
            if ((ny >> 4) != (pt[1] >> 4)) pvy = uint32_t(-int32_t(pvy) / 2);
            nx = pt[0]; ny = pt[1];
        } else if (occ0[c1]) hit[occ0[c1] - 1] += fold(s.obj[occ0[c1] - 1][6]).v(uint32_t(q)).fin();
        pt[0] = nx & 0xFFFFu; pt[1] = ny & 0xFFFFu; pt[2] = pvx & 0xFFFFu; pt[3] = pvy & 0xFFFFu;  // four 16-bit words (section 23)
    }
    for (uint32_t i = 0; i < kObj; ++i) s.obj[i][6] += hit[i];
    // the resolve: every object proposes its cell, the cell takes the smallest (collision key, id); a loser stays at its old cell
    uint32_t best[kCells]; for (uint32_t c = 0; c < kCells; ++c) { s.occ[c] = 0; best[c] = 0xFFFFFFFFu; }
    auto propose = [&](uint32_t i, uint32_t c) { cn.fixed[5] += 1; if (s.occ[c] == 0 || ckey[i] < best[c] || (ckey[i] == best[c] && i + 1 < s.occ[c])) { s.occ[c] = i + 1; best[c] = ckey[i]; } };
    for (uint32_t i = 0; i < kObj; ++i) propose(i, newc[i]);
    for (uint32_t i = 0; i < kObj; ++i) if (s.occ[newc[i]] != i + 1) { newc[i] = oldc[i]; wblk[i] = 1; }
    for (uint32_t i = 0; i < kObj; ++i) if (s.occ[newc[i]] != i + 1) propose(i, newc[i]);
    for (uint32_t i = 0; i < kObj; ++i) {
        wc[i] = newc[i];
        const uint32_t cx = newc[i] % kMap, cy = newc[i] / kMap;
        s.obj[i][0] = (cx * 16 + 8 + (s.obj[i][2] & 15)) & (kScreen - 1);
        s.obj[i][1] = (cy * 16 + 8 + (s.obj[i][3] & 15)) & (kScreen - 1);
        s.obj[i][7] = fold(s.obj[i][7]).v(wsteps[i]).v(wblk[i]).v(w.bounce).fin();
    }
}
uint32_t stage6(const world_t& w, const params_t& p, const state_t& s, const uint32_t occ0[kCells], uint32_t hp, counts_t& cn) {
    // the fold over the LIVE words (section 21): the 256 state words, the covered cells' index, key and value, the occupancy cells
    // that changed this tick with their index and value, the particles' words; the per-word combining operation drawn (a ROTL word
    // takes rotl(x, imm) XOR v so every word enters), the chaining and the finaliser fixed
    // section 23: 32 interleaved chains (lane l chains the live words with index congruent to l mod 32 from h XOR l), coupled by the
    // drawn five-level tree, the finaliser on the root
    uint32_t x[32]; for (uint32_t l = 0; l < 32; ++l) x[l] = hp ^ l;
    uint32_t j = 0;
    auto take = [&](uint32_t v) { const uint32_t l = j & 31; const op_t& o = w.foldseq[j & 63]; const uint32_t ch[2] = { x[l], v }; x[l] = o.opc == OP_ROTL ? (rotl(x[l], o.imm) ^ v) : apply(o.opc, ch[o.a], ch[o.b], o.imm); x[l] = rotl(x[l], 5) + v; ++j; };
    for (uint32_t i = 0; i < kObj; ++i) for (int k = 0; k < 8; ++k) take(s.obj[i][k]);
    for (uint32_t c = 0; c < kCells; ++c) if (s.fb[c] != 0xFFFFFFFFu) { take(c); take(s.fb[c]); take(s.fbv[c]); }
    for (uint32_t c = 0; c < kCells; ++c) if (s.occ[c] != occ0[c]) { take(c); take(s.occ[c]); }
    for (uint64_t q = 0; q < p.particles; ++q) for (int k = 0; k < 4; ++k) take(s.part[q][k]);
    for (uint32_t m = 32, l = 0; m > 1; m /= 2, ++l) { const op_t& o = w.tree[l]; for (uint32_t i = 0; i < m / 2; ++i) { const uint32_t ch[2] = { x[2 * i], x[2 * i + 1] }; x[i] = o.opc == OP_ROTL ? (rotl(ch[o.a], o.imm) ^ ch[o.b]) : apply(o.opc, ch[o.a], ch[o.b], o.imm); } }
    cn.drawn[6] += j + 31; cn.fixed[6] += 5;
    uint32_t r = x[0];
    r ^= r >> 16; r *= kMul1; r ^= r >> 15; r *= kMul2; r ^= r >> 13;
    return r;
}

void init_state(const params_t& p, const uint8_t key[32], state_t& s) {
    // section 21: init off BLAKE3: h_0 = F(0; the attempt key's eight words), then the running x chained once per word, x_j = F(x_(j-1); j),
    // over the 256 state words and the particles' words in order (READING: the chain's form)
    { fold f(0); for (int k = 0; k < 8; ++k) f.v(le32(key + 4 * k)); s.h = f.fin(); }
    // section 23: the expansion per lane: lane l's object words then its particles (q with q mod 32 = l) from x = F(h_0; l), chained
    // x_j = F(x_(j-1); j)
    for (uint32_t l = 0; l < 32; ++l) {
        uint32_t x = fold(s.h).v(l).fin(), j = 0;
        auto next = [&]() { x = fold(x).v(j).fin(); ++j; return x; };
        for (int k = 0; k < 8; ++k) { const uint32_t v = next(); if (k < 2) s.obj[l][k] = v & (kScreen - 1); else if (k < 4) s.obj[l][k] = sgn64(v); else s.obj[l][k] = v; }
        for (uint64_t q = l; q < kMaxParticles; q += 32) for (int k = 0; k < 4; ++k) {
            if (q < p.particles) { const uint32_t v = next(); s.part[q][k] = (k < 2 ? v & (kScreen - 1) : sgn64(v)) & 0xFFFFu; } else s.part[q][k] = 0;
        }
    }
    for (uint32_t c = 0; c < kCells; ++c) { s.fb[c] = 0xFFFFFFFFu; s.fbv[c] = s.h; s.occ[c] = 0; }
    for (uint32_t i = 0; i < kObj; ++i) { uint32_t& o = s.occ[cell_of(s.obj[i][0], s.obj[i][1])]; if (o == 0 || i + 1 < o) o = i + 1; }
    for (uint32_t i = 0; i < kObj; ++i) s.tex[i] = 0;
}

int run_attempt(const play_ctx& cx, const world_t& w, const uint8_t seed[32], uint64_t nonce, uint8_t key_out[32], state_t& s, counts_t& cn, std::vector<tick_trace>* tr) {
    { blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagAttempt); blake3_hasher_update(&h, w.seed, 32); blake3_hasher_update(&h, seed, 32); le64(h, nonce); le64(h, cx.p.tag); blake3_hasher_finalize(&h, key_out, 32); }  // the tag inside the attempt's key (section 9: the tag in the seal pre-image)
    init_state(cx.p, key_out, s);
    for (uint64_t t = 1; t <= cx.p.ticks; ++t) {
        const uint32_t hp = s.h, tex0 = hp >> 29;
        const uint8_t* order = kOrders[w.orders[hp & 7]];  // ORDERS[h mod 8], each entry an index into the six admissible orders
        tick_trace* T = nullptr; if (tr) { tr->emplace_back(); T = &tr->back(); T->order = w.orders[hp & 7]; }
        uint32_t W[kObj][32], vert[kObj][8], z[kObj], live[kObj][8]; int8_t B[256];
        uint32_t occ0[kCells]; std::memcpy(occ0, s.occ, sizeof occ0);  // the occupancy at the tick's start, for the live fold
        { prof_scope ps(1); stage1(w, s, hp, t, W, vert, z, live, B, cn); }
        uint32_t texc[kObj] = {}, wc[kObj] = {}, wsteps[kObj] = {}, wblk[kObj] = {}, ckey[kObj] = {}; uint64_t texw[kObj] = {}; uint64_t chunk[kObj][kMaxL] = {}; uint32_t chunkf[kObj][kMaxL] = {};
        for (int k = 0; k < 4; ++k) {
            switch (order[k]) {
                case 2: { prof_scope ps(2); stage2(w, s, hp, t, vert, z, live, cn); break; }
                case 3: { prof_scope ps(3); const int st = stage3(cx, w, s, hp, t, tex0, texc, texw, chunk, chunkf, cn); if (st != IGNPOW_OK) return st; break; }
                case 4: { prof_scope ps(4); stage4(w, s, hp, cn); break; }
                default: { prof_scope ps(5); stage5(w, cx.p, s, hp, wc, wsteps, wblk, ckey, cn); break; }
            }
        }
        { prof_scope ps(6); s.h = stage6(w, cx.p, s, occ0, hp, cn); }
        if (g_prof) ++g_prof->ticks;
        if (T) {
            T->h = s.h; std::memcpy(T->b, B, sizeof B); std::memcpy(T->w, W, sizeof W); std::memcpy(T->vert, vert, sizeof vert); std::memcpy(T->z, z, sizeof z); std::memcpy(T->live, live, sizeof live);
            std::memcpy(T->texc, texc, sizeof texc); std::memcpy(T->texw, texw, sizeof texw); std::memcpy(T->texv, s.tex, sizeof s.tex); std::memcpy(T->chunk, chunk, sizeof chunk); std::memcpy(T->chunkf, chunkf, sizeof chunkf);
            std::memcpy(T->walk_cell, wc, sizeof wc); std::memcpy(T->walk_steps, wsteps, sizeof wsteps); std::memcpy(T->walk_blocked, wblk, sizeof wblk); std::memcpy(T->collk, ckey, sizeof ckey);
            std::memcpy(T->fb, s.fb, sizeof s.fb); std::memcpy(T->fbv, s.fbv, sizeof s.fbv); std::memcpy(T->occ, s.occ, sizeof s.occ); std::memcpy(T->obj, s.obj, sizeof s.obj); std::memcpy(T->part, s.part, sizeof s.part);
        }
    }
    return IGNPOW_OK;
}
void ticket_of(const params_t& p, const uint8_t key[32], const state_t& s, uint8_t mix_out[32], uint8_t hash_out[32]) {
    uint8_t ob[kObj * 32 + 16 * kMaxParticles]; size_t n = 0;
    for (uint32_t i = 0; i < kObj; ++i) for (int k = 0; k < 8; ++k) { put32(ob + n, s.obj[i][k]); n += 4; }
    for (uint64_t q = 0; q < p.particles; ++q) for (int k = 0; k < 4; ++k) { put32(ob + n, s.part[q][k]); n += 4; }  // the particles' words follow the objects' (section 9)
    uint8_t hb[4]; put32(hb, s.h);
    { blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagMix); blake3_hasher_update(&h, hb, 4); blake3_hasher_update(&h, ob, n); blake3_hasher_finalize(&h, mix_out, 32); }
    { blake3_hasher h; blake3_hasher_init(&h); lp(h, kTagTicket); blake3_hasher_update(&h, key, 32); blake3_hasher_update(&h, hb, 4); blake3_hasher_update(&h, ob, n); blake3_hasher_finalize(&h, hash_out, 32); }
}
int compute(const play_ctx& cx, uint64_t height, const uint8_t seed[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32], std::vector<tick_trace>* tr, world_t* wout, uint8_t* key_out, counts_t* cnout) {
    world_t w; draw_world(cx.p, height, w);
    uint8_t key[32]; state_t s; counts_t cn;
    const int st = run_attempt(cx, w, seed, nonce, key, s, cn, tr); if (st != IGNPOW_OK) return st;
    ticket_of(cx.p, key, s, mix_out, hash_out);
    if (wout) *wout = w; if (key_out) std::memcpy(key_out, key, 32); if (cnout) *cnout = cn;
    return IGNPOW_OK;
}

int pcheck_params(const char* p) { return parse(p).ok ? IGNPOW_OK : IGNPOW_E_PARAMS; }
uint64_t pepoch_of(const char* p, uint64_t h, int* status) { const params_t r = parse(p); int st = r.ok ? IGNPOW_OK : IGNPOW_E_PARAMS; if (st == IGNPOW_OK && h > kMaxHeight) st = IGNPOW_E_HEIGHT; if (status) *status = st; return st == IGNPOW_OK ? h / r.epoch_blocks : 0; }
uint64_t pcache_bytes(const char* p, uint64_t, int* status) { const params_t r = parse(p); if (status) *status = r.ok ? IGNPOW_OK : IGNPOW_E_PARAMS; return r.ok ? r.cache_mib << 20 : 0; }
uint64_t pdataset_bytes(const char* p, uint64_t, int* status) { const params_t r = parse(p); if (status) *status = r.ok ? IGNPOW_OK : IGNPOW_E_PARAMS; return r.ok ? r.dataset_mib << 20 : 0; }
ctx_base* pcreate(const char* p, uint64_t h, int full, int* status) {
    const params_t r = parse(p);
    if (!r.ok) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    if (h > kMaxHeight) { if (status) *status = IGNPOW_E_HEIGHT; return nullptr; }
    auto c = std::make_unique<play_ctx>(); c->p = r; c->ep = h / r.epoch_blocks;
    int st = IGNPOW_OK; c->base.reset(ih_create_public(base_json(r).c_str(), h, full, &st));
    if (!c->base) { if (status) *status = st; return nullptr; }
    if (status) *status = IGNPOW_OK; return c.release();
}
// the play context over an IgneumHash context's dataset (ignpow_ctx_create_shared with algo "igneum-play" and an igneumhash donor):
// one resident 2 GiB dataset serves IgneumHash and Play on the node (the donor's E and D must equal the base document's)
ctx_base* pcreate_shared(const char* p, uint64_t h, const ctx_base* donor, int* status) {
    const params_t r = parse(p);
    if (!r.ok) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; }
    if (h > kMaxHeight) { if (status) *status = IGNPOW_E_HEIGHT; return nullptr; }
    auto c = std::make_unique<play_ctx>(); c->p = r; c->ep = h / r.epoch_blocks;
    int st = IGNPOW_OK; c->base.reset(ih_create_shared(base_json(r).c_str(), h, donor, &st));
    if (!c->base) { if (status) *status = st; return nullptr; }
    if (status) *status = IGNPOW_OK; return c.release();
}
int phash(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& cx = *static_cast<const play_ctx*>(c);
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT; if (h / cx.p.epoch_blocks != cx.ep) return IGNPOW_E_EPOCH;
    return compute(cx, h, header, nonce, mix_out, hash_out, nullptr, nullptr, nullptr, nullptr);
}
int pverify(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    const auto& cx = *static_cast<const play_ctx*>(c);
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT; if (h / cx.p.epoch_blocks != cx.ep) return IGNPOW_E_EPOCH;
    uint8_t m[32], hh[32]; const int st = compute(cx, h, header, nonce, m, hh, nullptr, nullptr, nullptr, nullptr); if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(hh, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int plight_cache(const ctx_base* c, const void** data, uint64_t* bytes, uint32_t* item_bytes) { return ih_light_cache_public(static_cast<const play_ctx*>(c)->base.get(), data, bytes, item_bytes); }
int pvectors_check(size_t* count_out) { if (count_out) *count_out = 0; return IGNPOW_OK; }

void hex32(FILE* f, const uint8_t* b, size_t n) { for (size_t i = 0; i < n; ++i) std::fprintf(f, "%02x", b[i]); }
template <class T> void arr(FILE* f, const T* v, size_t n) { std::fputc('[', f); for (size_t i = 0; i < n; ++i) std::fprintf(f, "%s%llu", i ? "," : "", (unsigned long long)v[i]); std::fputc(']', f); }
void op_json(FILE* f, const op_t& o) { std::fprintf(f, "[%u,%u,%u,%u,%u]", o.opc, o.d, o.a, o.b, o.imm); }
void prog_json(FILE* f, const prog_t& p) { std::fputc('[', f); for (size_t j = 0; j < p.ops.size(); ++j) { if (j) std::fputc(',', f); op_json(f, p.ops[j]); } std::fputc(']', f); }
// the counts stream (section 15): 28 LE32 words per attempt: the six stages' drawn and fixed counts, the memory bytes, the particle
// count, the walk steps, the covered cells, the ticks, the drawn share of operations in parts per 65,536, the energy share (zero
// until bound), the rest reserved as zero
void counts_json(FILE* out, const counts_t& cn, const params_t& p) {
    uint64_t d = 0, x = 0; for (int k = 1; k < 7; ++k) { d += cn.drawn[k]; x += cn.fixed[k]; }
    uint32_t st[28] = {};
    for (int k = 0; k < 6; ++k) { st[2 * k] = uint32_t(cn.drawn[k + 1]); st[2 * k + 1] = uint32_t(cn.fixed[k + 1]); }
    st[12] = uint32_t(cn.bytes); st[13] = uint32_t(p.particles); st[14] = uint32_t(cn.steps); st[15] = uint32_t(cn.covered); st[16] = uint32_t(p.ticks);
    st[17] = uint32_t((d * 65536) / (d + x ? d + x : 1)); st[18] = 0;
    std::fprintf(out, "{\"drawn\": "); arr(out, cn.drawn + 1, 6); std::fprintf(out, ", \"fixed\": "); arr(out, cn.fixed + 1, 6);
    std::fprintf(out, ", \"drawn_total\": %llu, \"fixed_total\": %llu, \"reads\": %llu, \"bytes\": %llu, \"stream\": ", (unsigned long long)d, (unsigned long long)x, (unsigned long long)cn.reads, (unsigned long long)cn.bytes);
    arr(out, st, 28); std::fputc('}', out);
}
// the trace: the world, every tick's records (the rules' classes as fields: geometry, coverage and values, texel and chunks, state,
// walk, particles, occupancy, the tick hash) and the counts
int trace(const play_ctx& cx, uint64_t height, const uint8_t seed[32], uint64_t nonce, FILE* out, bool counts_only) {
    if (!out) return IGNPOW_E_ARG;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT; if (height / cx.p.epoch_blocks != cx.ep) return IGNPOW_E_EPOCH;
    std::vector<tick_trace> tr; world_t w; uint8_t key[32], m[32], hh[32]; counts_t cn;
    const int st = compute(cx, height, seed, nonce, m, hh, counts_only ? nullptr : &tr, &w, key, &cn); if (st != IGNPOW_OK) return st;
    if (counts_only) { counts_json(out, cn, cx.p); return IGNPOW_OK; }
    const uint32_t L = uint32_t(cx.p.chunk_walk), NP = uint32_t(cx.p.particles);
    std::fprintf(out, "{\"tag\": %llu, \"world_seed\": \"", (unsigned long long)cx.p.tag); hex32(out, w.seed, 32); std::fprintf(out, "\", \"period\": %llu, \"attempt_key\": \"", (unsigned long long)w.period); hex32(out, key, 32);
    std::fprintf(out, "\", \"world\": {\"wall\": "); arr(out, w.wall, kCells);
    std::fprintf(out, ", \"parent\": ["); for (uint32_t i = 0; i < kObj; ++i) std::fprintf(out, "%s%d", i ? "," : "", w.parent[i]); std::fprintf(out, "], \"object_class\": "); arr(out, w.cls, kObj);
    std::fprintf(out, ", \"layout\": "); arr(out, w.layout, 8); std::fprintf(out, ", \"target\": "); arr(out, w.target, kObj); std::fprintf(out, ", \"orders\": "); arr(out, w.orders, 8);
    std::fprintf(out, ", \"tex\": "); arr(out, w.tex + 1, 3); std::fprintf(out, ", \"bounce\": %u", w.bounce);
    std::fprintf(out, ", \"field\": ["); for (uint32_t c = 0; c < kCells; ++c) std::fprintf(out, "%s[%d,%d]", c ? "," : "", w.field[c][0], w.field[c][1]); std::fputc(']', out);
    std::fprintf(out, ", \"programs\": {\"composition\": ["); for (int l = 0; l < 4; ++l) { if (l) std::fputc(',', out); prog_json(out, w.comp[l]); }
    std::fprintf(out, "], \"vertex\": "); prog_json(out, w.vertex); std::fprintf(out, ", \"distortion\": "); prog_json(out, w.distort); std::fprintf(out, ", \"pattern\": "); prog_json(out, w.pattern);
    std::fprintf(out, ", \"value\": "); prog_json(out, w.value); std::fprintf(out, ", \"address\": "); prog_json(out, w.address);
    std::fprintf(out, ", \"reduction\": ["); for (int l = 0; l < 6; ++l) { if (l) std::fputc(',', out); op_json(out, w.red[l]); }
    std::fprintf(out, "], \"state\": ["); for (int k = 0; k < 4; ++k) { if (k) std::fputc(',', out); prog_json(out, w.state[k]); }
    std::fprintf(out, "], \"navigation\": "); prog_json(out, w.navigate); std::fprintf(out, ", \"particle\": "); prog_json(out, w.particle); std::fprintf(out, ", \"collision_key\": "); prog_json(out, w.collkey);
    std::fprintf(out, ", \"fold\": ["); for (int j = 0; j < 64; ++j) { if (j) std::fputc(',', out); op_json(out, w.foldseq[j]); }
    std::fprintf(out, "], \"tree\": ["); for (int l = 0; l < 5; ++l) { if (l) std::fputc(',', out); op_json(out, w.tree[l]); } std::fprintf(out, "]}}");
    std::fprintf(out, ", \"ticks\": [");
    for (size_t t = 0; t < tr.size(); ++t) {
        const tick_trace& T = tr[t];
        std::fprintf(out, "%s{\"h\": %u, \"order\": %u, \"b\": [", t ? "," : "", T.h, T.order); for (int k = 0; k < 256; ++k) std::fprintf(out, "%s%d", k ? "," : "", T.b[k]);
        std::fprintf(out, "], \"geometry\": ["); for (uint32_t i = 0; i < kObj; ++i) { std::fprintf(out, "%s{\"w\": ", i ? "," : ""); arr(out, T.w[i], 32); std::fprintf(out, ", \"vertices\": "); arr(out, T.vert[i], 8); std::fprintf(out, ", \"z\": %u, \"live\": ", T.z[i]); arr(out, T.live[i], 8); std::fputc('}', out); }
        std::fprintf(out, "], \"coverage\": "); arr(out, T.fb, kCells); std::fprintf(out, ", \"values\": "); arr(out, T.fbv, kCells);
        std::fprintf(out, ", \"texel\": ["); for (uint32_t i = 0; i < kObj; ++i) std::fprintf(out, "%s[%u,%llu,%u]", i ? "," : "", T.texc[i], (unsigned long long)T.texw[i], T.texv[i]);
        std::fprintf(out, "], \"chunks\": ["); for (uint32_t i = 0; i < kObj; ++i) { std::fprintf(out, "%s[", i ? "," : ""); for (uint32_t k = 0; k < L; ++k) std::fprintf(out, "%s[%llu,%u]", k ? "," : "", (unsigned long long)T.chunk[i][k], T.chunkf[i][k]); std::fputc(']', out); }
        std::fprintf(out, "], \"state\": ["); for (uint32_t i = 0; i < kObj; ++i) { std::fprintf(out, "%s", i ? "," : ""); arr(out, T.obj[i], 8); }
        std::fprintf(out, "], \"walk\": ["); for (uint32_t i = 0; i < kObj; ++i) std::fprintf(out, "%s[%u,%u,%u,%u]", i ? "," : "", T.walk_cell[i], T.walk_steps[i], T.walk_blocked[i], T.collk[i]);
        std::fprintf(out, "], \"particles\": ["); for (uint32_t q = 0; q < NP; ++q) { std::fprintf(out, "%s", q ? "," : ""); arr(out, T.part[q], 4); }
        std::fprintf(out, "], \"occupancy\": "); arr(out, T.occ, kCells); std::fputc('}', out);
    }
    std::fprintf(out, "], \"counts\": "); counts_json(out, cn, cx.p);
    std::fprintf(out, ", \"M\": \""); hex32(out, m, 32); std::fprintf(out, "\", \"F\": \""); hex32(out, hh, 32); std::fprintf(out, "\"}");
    return IGNPOW_OK;
}
}  // namespace play

const candidate igneum_play = {
    IGNPOW_ALGO_IGNEUM_PLAY,
    play::pcheck_params, play::pepoch_of, play::pcache_bytes, play::pdataset_bytes, play::pcreate, play::phash, play::pverify, play::plight_cache, play::pvectors_check,
    "igneum-play = WorldWork's first exact world (C-33 inside C-34): PLAY-MICROGAME-RULES.md at 6ec69ea1e (the four coordinate tags; the INT8 16x16x16 tile with the drawn composition and vertex programmes; integer coverage with the drawn distortion, pattern and value programmes; dataset texels, the L-chunk walk and its drawn reduction; the drawn state programme; the grid walk with the drawn navigation, particle and collision-key programmes; the chained fold with its drawn sequence; the ticket one BLAKE3); the dataset IgneumHash's at W256; BLAKE3 1.8.2 portable",
};
const ctx_base* inner_of(const ignpow_ctx* ctx, const char* algo);
ctx_base* play_create_shared(const char* json, uint64_t height, const ctx_base* donor, int* status) { return play::pcreate_shared(json, height, donor, status); }
}  // namespace ignpow

extern "C" int igneum_play_trace(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* out) {
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUM_PLAY);
    if (!inner || !header) return IGNPOW_E_ARG;
    return ignpow::play::trace(*static_cast<const ignpow::play::play_ctx*>(inner), height, header, nonce, out, false);
}
// the profile: n attempts from (header, nonce..nonce+n), the calling thread's CPU time per stage per tick in microseconds (and the
// dataset reads inside stage 3, the tile inside stage 1), one JSON line
extern "C" int igneum_play_profile(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, uint32_t n, FILE* out) {
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUM_PLAY);
    if (!inner || !header || !out || n == 0) return IGNPOW_E_ARG;
    const auto& cx = *static_cast<const ignpow::play::play_ctx*>(inner);
    ignpow::play::prof_t pr; ignpow::play::g_prof = &pr;
    const uint64_t t0 = ignpow::play::cpu_ns();
    for (uint32_t k = 0; k < n; ++k) { uint8_t m[32], h[32]; const int st = ignpow::play::phash(inner, height, header, nonce + k, m, h); if (st != IGNPOW_OK) { ignpow::play::g_prof = nullptr; return st; } }
    const uint64_t total = ignpow::play::cpu_ns() - t0;
    ignpow::play::g_prof = nullptr;
    const double tk = double(pr.ticks ? pr.ticks : 1);
    std::fprintf(out, "{\"attempts\": %u, \"ticks\": %llu, \"us_per_tick\": {\"total\": %.2f, \"stage1\": %.2f, \"tile\": %.2f, \"stage2\": %.2f, \"stage3\": %.2f, \"dataset_reads\": %.2f, \"stage4\": %.2f, \"stage5\": %.2f, \"stage6\": %.2f}, \"us_per_attempt\": %.2f}\n",
                 n, (unsigned long long)pr.ticks, total / tk / 1000.0, pr.ns[1] / tk / 1000.0, pr.ns[0] / tk / 1000.0, pr.ns[2] / tk / 1000.0, pr.ns[3] / tk / 1000.0, pr.ns[7] / tk / 1000.0, pr.ns[4] / tk / 1000.0, pr.ns[5] / tk / 1000.0, pr.ns[6] / tk / 1000.0, total / double(n) / 1000.0);
    return IGNPOW_OK;
}
extern "C" int igneum_play_counts(const ignpow_ctx* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce, FILE* out) {
    const ignpow::ctx_base* inner = ignpow::inner_of(ctx, IGNPOW_ALGO_IGNEUM_PLAY);
    if (!inner || !header) return IGNPOW_E_ARG;
    return ignpow::play::trace(*static_cast<const ignpow::play::play_ctx*>(inner), height, header, nonce, out, true);
}
