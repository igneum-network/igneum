// Candidate progpow-epic: Epic Cash's ProgPoW (revision 0.9.2 over ethash revision 23 with Epic's epoch length
// 30000), written from the vendored light reference (pow/third_party/progpow-epic-7a10ab8/src/pp_light/src/progpow.rs,
// EpicCash/progpow-rust at 7a10ab82) and checked against its upstream test vectors
// (pp_light/res/progpow_testvectors.json, transcribed below). The light cache and the dataset are the chfast ethash
// tree already in the build (pow/third_party/ravencoin-ethash-22549129; the epoch number is computed here from
// Epic's length, the library's own epoch length is never used). docs/igneumhash/controls/SOURCES.md.
//
// The document: NULL, "{}" or {"version": 1, "epoch_blocks": 30000}; nothing else (the control has no free
// parameter). Tag progpow-epic-e30000; pid = BLAKE3 of the tag (progpow_epic_pid).

#include "candidate.hpp"
#include "controls.h"
#include "blake3.h"

#include <crypto/ethash/include/ethash/ethash.hpp>
#include <crypto/ethash/include/ethash/keccak.h>
#include <crypto/ethash/include/ethash/keccak.hpp>
#include <crypto/ethash/lib/ethash/bit_manipulation.h>
#include <crypto/ethash/lib/ethash/endianness.hpp>
#include <crypto/ethash/lib/ethash/ethash-internal.hpp>
#include <crypto/ethash/lib/ethash/kiss99.hpp>

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <limits>
#include <memory>
#include <string>
#include <thread>
#include <vector>

namespace ignpow {
namespace pe {

constexpr uint64_t kEpochLength = 30000;
constexpr uint64_t kMaxHeight = static_cast<uint64_t>(std::numeric_limits<int>::max());
constexpr uint32_t kPeriod = 50, kLanes = 16, kRegs = 32, kDagLoads = 4, kCacheWords = 16 * 1024 / 4, kCntDag = 64, kCntCache = 12, kCntMath = 20;

bool params_ok(const char* p) {
    if (p == nullptr) return true;
    std::string s;
    for (const char* c = p; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') s += *c;
    return s == "{}" || s == "{\"version\":1,\"epoch_blocks\":30000}" || s == "{\"epoch_blocks\":30000,\"version\":1}" || s == "{\"version\":1}";
}
int height_ok(const char* p, uint64_t height) { if (!params_ok(p)) return IGNPOW_E_PARAMS; if (height > kMaxHeight) return IGNPOW_E_HEIGHT; return IGNPOW_OK; }
int epoch_number(uint64_t height) { return static_cast<int>(height / kEpochLength); }

inline uint32_t bswap32(uint32_t x) { return (x >> 24) | ((x >> 8) & 0xff00u) | ((x << 8) & 0xff0000u) | (x << 24); }
inline uint32_t rotl(uint32_t a, uint32_t b) { b &= 31; return b ? (a << b) | (a >> (32 - b)) : a; }
inline uint32_t rotr(uint32_t a, uint32_t b) { b &= 31; return b ? (a >> b) | (a << (32 - b)) : a; }
inline uint32_t clz(uint32_t x) { return x ? static_cast<uint32_t>(__builtin_clz(x)) : 32u; }
inline uint32_t popc(uint32_t x) { return static_cast<uint32_t>(__builtin_popcount(x)); }

// pp_light merge() and math() (ProgPoW 0.9.2)
inline uint32_t merge(uint32_t a, uint32_t b, uint32_t r) {
    switch (r % 4) {
    case 0: return a * 33u + b;
    case 1: return (a ^ b) * 33u;
    case 2: return rotl(a, ((r >> 16) % 31) + 1) ^ b;
    default: return rotr(a, ((r >> 16) % 31) + 1) ^ b;
    }
}
inline uint32_t math(uint32_t a, uint32_t b, uint32_t r) {
    switch (r % 11) {
    case 0: return a + b;
    case 1: return a * b;
    case 2: return static_cast<uint32_t>((static_cast<uint64_t>(a) * b) >> 32);
    case 3: return a < b ? a : b;
    case 4: return rotl(a, b);
    case 5: return rotr(a, b);
    case 6: return a & b;
    case 7: return a | b;
    case 8: return a ^ b;
    case 9: return clz(a) + clz(b);
    default: return popc(a) + popc(b);
    }
}

// keccak_f800 over header words, a 64-bit word and 8 result words (pp_light keccak_f800)
void keccak_f800(const uint32_t header[8], uint64_t word, const uint32_t result[8], uint32_t st[25]) {
    for (int i = 0; i < 25; ++i) st[i] = 0;
    for (int i = 0; i < 8; ++i) st[i] = header[i];
    st[8] = static_cast<uint32_t>(word);
    st[9] = static_cast<uint32_t>(word >> 32);
    for (int i = 0; i < 8; ++i) st[10 + i] = result[i];
    ethash_keccakf800(st);
}

struct mix_state {
    kiss99 rng;
    uint32_t dst_seq[kRegs], cache_seq[kRegs];
};
mix_state init(uint64_t period) {
    mix_state s;
    const uint32_t z = fnv1a(fnv_offset_basis, static_cast<uint32_t>(period));
    const uint32_t w = fnv1a(z, static_cast<uint32_t>(period >> 32));
    const uint32_t jsr = fnv1a(w, static_cast<uint32_t>(period));
    const uint32_t jcong = fnv1a(jsr, static_cast<uint32_t>(period >> 32));
    s.rng = kiss99{z, w, jsr, jcong};
    for (uint32_t i = 0; i < kRegs; ++i) { s.dst_seq[i] = i; s.cache_seq[i] = i; }
    for (uint32_t i = kRegs - 1; i > 0; --i) {
        uint32_t j = s.rng() % (i + 1); std::swap(s.dst_seq[i], s.dst_seq[j]);
        j = s.rng() % (i + 1); std::swap(s.cache_seq[i], s.cache_seq[j]);
    }
    return s;
}

// The dataset node (ethash revision 23: 256 parents, keccak-512, FNV-1) from the light cache. The chfast tree in the
// build computes KAWPOW's nodes (512 parents, Ravencoin's change), so Epic's nodes are computed here from the same
// light cache (whose construction, seed and sizes are revision 23's in both).
constexpr uint32_t kParents = 256;
inline uint32_t fnv1(uint32_t h, uint32_t d) { return (h * 0x01000193u) ^ d; }
void node(const ethash::epoch_context& ctx, uint32_t index, uint32_t out[16]) {
    const uint32_t n = static_cast<uint32_t>(ctx.light_cache_num_items);
    ethash::hash512 mix = ctx.light_cache[index % n];
    mix.word32s[0] = ethash::le::uint32(ethash::le::uint32(mix.word32s[0]) ^ index);
    mix = ethash::keccak512(mix);
    uint32_t w[16];
    for (int k = 0; k < 16; ++k) w[k] = ethash::le::uint32(mix.word32s[k]);
    for (uint32_t j = 0; j < kParents; ++j) {
        const uint32_t p = fnv1(index ^ j, w[j % 16]) % n;
        const ethash::hash512& par = ctx.light_cache[p];
        for (int k = 0; k < 16; ++k) w[k] = fnv1(w[k], ethash::le::uint32(par.word32s[k]));
    }
    for (int k = 0; k < 16; ++k) mix.word32s[k] = ethash::le::uint32(w[k]);
    mix = ethash::keccak512(mix);
    for (int k = 0; k < 16; ++k) out[k] = ethash::le::uint32(mix.word32s[k]);
}

// The 256-byte item g = nodes 4g .. 4g + 3 as 64 words; the full path reads a resident array.
struct dag_source {
    const ethash::epoch_context* ctx;
    const uint32_t* full;  // num_items x 64 words, or null
    uint32_t l1[kCacheWords];
    void item(uint32_t g, uint32_t out[64]) const {
        if (full) { std::memcpy(out, full + size_t(g) * 64, 256); return; }
        for (uint32_t k = 0; k < 4; ++k) node(*ctx, 4 * g + k, out + 16 * k);
    }
};

void progpow_loop(const dag_source& dag, uint64_t period, uint32_t r, uint32_t mix[kLanes][kRegs], uint32_t num_items) {
    const uint32_t g = mix[r % kLanes][0] % num_items;
    uint32_t item[64];
    dag.item(g, item);
    const mix_state base = init(period);
    for (uint32_t l = 0; l < kLanes; ++l) {
        mix_state s = base;
        uint32_t dst_cnt = 0, cache_cnt = 0;
        auto next_dst = [&]() { return s.dst_seq[(dst_cnt++) % kRegs]; };
        auto next_cache = [&]() { return s.cache_seq[(cache_cnt++) % kRegs]; };
        for (uint32_t i = 0; i < (kCntCache > kCntMath ? kCntCache : kCntMath); ++i) {
            if (i < kCntCache) {
                const uint32_t offset = mix[l][next_cache()] % kCacheWords;
                const uint32_t data = dag.l1[offset];
                const uint32_t dst = next_dst();
                mix[l][dst] = merge(mix[l][dst], data, s.rng());
            }
            if (i < kCntMath) {
                const uint32_t src_rnd = s.rng() % (kRegs * (kRegs - 1));
                const uint32_t src1 = src_rnd % kRegs;
                uint32_t src2 = src_rnd / kRegs;
                if (src2 >= src1) ++src2;
                const uint32_t data = math(mix[l][src1], mix[l][src2], s.rng());
                const uint32_t dst = next_dst();
                mix[l][dst] = merge(mix[l][dst], data, s.rng());
            }
        }
        const uint32_t index = ((l ^ r) % kLanes) * kDagLoads;
        uint32_t data_g[kDagLoads];
        for (uint32_t i = 0; i < kDagLoads; ++i) data_g[i] = item[index + i];
        mix[l][0] = merge(mix[l][0], data_g[0], s.rng());
        for (uint32_t i = 1; i < kDagLoads; ++i) { const uint32_t dst = next_dst(); mix[l][dst] = merge(mix[l][dst], data_g[i], s.rng()); }
    }
}

struct result { uint8_t mix[32], final[32]; };

void compute(const dag_source& dag, uint64_t height, const uint8_t header_bytes[32], uint64_t nonce, result& out) {
    uint32_t header[8];
    for (int i = 0; i < 8; ++i) header[i] = static_cast<uint32_t>(header_bytes[4 * i]) | (static_cast<uint32_t>(header_bytes[4 * i + 1]) << 8) | (static_cast<uint32_t>(header_bytes[4 * i + 2]) << 16) | (static_cast<uint32_t>(header_bytes[4 * i + 3]) << 24);
    uint32_t zero[8] = {0}, st[25];
    keccak_f800(header, nonce, zero, st);
    const uint64_t seed = (static_cast<uint64_t>(bswap32(st[0])) << 32) | bswap32(st[1]);
    uint32_t mix[kLanes][kRegs];
    const uint32_t z = fnv1a(fnv_offset_basis, static_cast<uint32_t>(seed));
    const uint32_t w = fnv1a(z, static_cast<uint32_t>(seed >> 32));
    for (uint32_t l = 0; l < kLanes; ++l) {
        const uint32_t jsr = fnv1a(w, l), jcong = fnv1a(jsr, l);
        kiss99 rng{z, w, jsr, jcong};
        for (uint32_t i = 0; i < kRegs; ++i) mix[l][i] = rng();
    }
    const uint32_t num_items = static_cast<uint32_t>(dag.ctx->full_dataset_num_items / 2);  // 256-byte items
    const uint64_t period = height / kPeriod;
    const bool dbg = std::getenv("PE_DEBUG") != nullptr;
    if (dbg) std::fprintf(stderr, "pe: l1[0..4] %u %u %u %u; num_items %u; seed %016llx; fill mix[0][0..4] %u %u %u %u\n", dag.l1[0], dag.l1[1], dag.l1[2], dag.l1[3], num_items, (unsigned long long)seed, mix[0][0], mix[0][1], mix[0][2], mix[0][3]);
    for (uint32_t r = 0; r < kCntDag; ++r) {
        progpow_loop(dag, period, r, mix, num_items);
        if (dbg && r < 2) std::fprintf(stderr, "pe: after loop %u mix[0][0..4] %u %u %u %u\n", r, mix[0][0], mix[0][1], mix[0][2], mix[0][3]);
    }
    uint32_t lane_hash[kLanes];
    for (uint32_t l = 0; l < kLanes; ++l) { lane_hash[l] = fnv_offset_basis; for (uint32_t i = 0; i < kRegs; ++i) lane_hash[l] = fnv1a(lane_hash[l], mix[l][i]); }
    uint32_t res[8];
    for (int i = 0; i < 8; ++i) res[i] = fnv_offset_basis;
    for (uint32_t l = 0; l < kLanes; ++l) res[l % 8] = fnv1a(res[l % 8], lane_hash[l]);
    keccak_f800(header, seed, res, st);
    for (int i = 0; i < 8; ++i) for (int b = 0; b < 4; ++b) { out.mix[4 * i + b] = static_cast<uint8_t>(res[i] >> (8 * b)); out.final[4 * i + b] = static_cast<uint8_t>(st[i] >> (8 * b)); }
}

struct pe_ctx final : ctx_base {
    int epoch_num = 0; bool full = false;
    ethash::epoch_context_ptr light{nullptr, ethash_destroy_epoch_context};
    std::vector<uint32_t> dataset;  // the full path: num_items x 64 words
    dag_source dag{};
    uint64_t epoch() const override { return static_cast<uint64_t>(epoch_num); }
    const char* algo() const override { return IGNPOW_ALGO_PROGPOW_EPIC; }
};

int ab_check_params(const char* p) { return params_ok(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }
uint64_t ab_epoch_of(const char* p, uint64_t h, int* status) { int st = height_ok(p, h); if (status) *status = st; return st == IGNPOW_OK ? h / kEpochLength : 0; }
uint64_t ab_cache_bytes(const char* p, uint64_t h, int* status) { int st = height_ok(p, h); if (status) *status = st; return st == IGNPOW_OK ? ethash::get_light_cache_size(ethash::calculate_light_cache_num_items(epoch_number(h))) : 0; }
uint64_t ab_dataset_bytes(const char* p, uint64_t h, int* status) { int st = height_ok(p, h); if (status) *status = st; return st == IGNPOW_OK ? ethash::get_full_dataset_size(ethash::calculate_full_dataset_num_items(epoch_number(h))) : 0; }
ctx_base* ab_create(const char* p, uint64_t h, int full, int* status) {
    int st = height_ok(p, h); if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    auto c = std::make_unique<pe_ctx>();
    c->epoch_num = epoch_number(h); c->full = full != 0;
    c->light = ethash::create_epoch_context(c->epoch_num);
    if (!c->light) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
    c->dag.ctx = c->light.get(); c->dag.full = nullptr;
    for (uint32_t k = 0; k < kCacheWords / 16; ++k) node(*c->light, k, c->dag.l1 + 16 * k);
    if (c->full) {
        const uint32_t n = static_cast<uint32_t>(c->light->full_dataset_num_items / 2);
        try { c->dataset.resize(size_t(n) * 64); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
        unsigned nt = std::thread::hardware_concurrency(); if (nt == 0) nt = 4;
        std::vector<std::thread> ts;
        for (unsigned t = 0; t < nt; ++t) ts.emplace_back([&, t]() { for (uint32_t g = t; g < n; g += nt) for (uint32_t k = 0; k < 4; ++k) node(*c->light, 4 * g + k, c->dataset.data() + size_t(g) * 64 + 16 * k); });
        for (auto& th : ts) th.join();
        c->dag.full = c->dataset.data();
    }
    if (status) *status = IGNPOW_OK;
    return c.release();
}
int run(const pe_ctx& c, uint64_t h, const uint8_t header[32], uint64_t nonce, result& r) {
    if (h > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (epoch_number(h) != c.epoch_num) return IGNPOW_E_EPOCH;
    compute(c.dag, h, header, nonce, r);
    return IGNPOW_OK;
}
int ab_hash(const ctx_base* ctx, uint64_t h, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    result r; int st = run(*static_cast<const pe_ctx*>(ctx), h, header, nonce, r); if (st != IGNPOW_OK) return st;
    std::memcpy(mix_out, r.mix, 32); std::memcpy(hash_out, r.final, 32); return IGNPOW_OK;
}
int ab_verify(const ctx_base* ctx, uint64_t h, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    result r; int st = run(*static_cast<const pe_ctx*>(ctx), h, header, nonce, r); if (st != IGNPOW_OK) return st;
    if (std::memcmp(r.mix, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(r.final, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int ab_light_cache(const ctx_base* ctx, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    const auto& c = *static_cast<const pe_ctx*>(ctx);
    if (data) *data = c.light->light_cache;
    if (bytes) *bytes = static_cast<uint64_t>(c.light->light_cache_num_items) * 64;
    if (item_bytes) *item_bytes = 64;
    return IGNPOW_OK;
}

// The upstream vectors (pp_light/res/progpow_testvectors.json, transcribed: block, header, nonce, mix, final).
struct tv { int block; const char* header; const char* nonce; const char* mix; const char* final; };
const tv kVectors[] = {
    {0, "0000000000000000000000000000000000000000000000000000000000000000", "0000000000000000", "faeb1be51075b03a4ff44b335067951ead07a3b078539ace76fd56fc410557a3", "63155f732f2bf556967f906155b510c917e48e99685ead76ea83f4eca03ab12b"},
    {49, "63155f732f2bf556967f906155b510c917e48e99685ead76ea83f4eca03ab12b", "0000000006ff2c47", "c789c1180f890ec555ff42042913465481e8e6bc512cb981e1c1108dc3f2227d", "9e7248f20914913a73d80a70174c331b1d34f260535ac3631d770e656b5dd922"},
    {50, "9e7248f20914913a73d80a70174c331b1d34f260535ac3631d770e656b5dd922", "00000000076e482e", "c7340542c2a06b3a7dc7222635f7cd402abf8b528ae971ddac6bbe2b0c7cb518", "de37e1824c86d35d154cf65a88de6d9286aec4f7f10c3fc9f0fa1bcc2687188d"},
    {99, "de37e1824c86d35d154cf65a88de6d9286aec4f7f10c3fc9f0fa1bcc2687188d", "000000003917afab", "f5e60b2c5bfddd136167a30cbc3c8dbdbd15a512257dee7964e0bc6daa9f8ba7", "ac7b55e801511b77e11d52e9599206101550144525b5679f2dab19386f23dcce"},
    {29950, "ac7b55e801511b77e11d52e9599206101550144525b5679f2dab19386f23dcce", "005d409dbc23a62a", "07393d15805eb08ee6fc6cb3ad4ad1010533bd0ff92d6006850246829f18fd6e", "e43d7e0bdc8a4a3f6e291a5ed790b9fa1a0948a2b9e33c844888690847de19f5"},
    {29999, "e43d7e0bdc8a4a3f6e291a5ed790b9fa1a0948a2b9e33c844888690847de19f5", "005db5fa4c2a3d03", "7551bddf977491da2f6cfc1679299544b23483e8f8ee0931c4c16a796558a0b8", "d34519f72c97cae8892c277776259db3320820cb5279a299d0ef1e155e5c6454"},
    {30000, "d34519f72c97cae8892c277776259db3320820cb5279a299d0ef1e155e5c6454", "005db8607994ff30", "f1c2c7c32266af9635462e6ce1c98ebe4e7e3ecab7a38aaabfbf2e731e0fbff4", "8b6ce5da0b06d18db7bd8492d9e5717f8b53e7e098d9fef7886d58a6e913ef64"},
    {30049, "8b6ce5da0b06d18db7bd8492d9e5717f8b53e7e098d9fef7886d58a6e913ef64", "005e2e215a8ca2e7", "57fe6a9fbf920b4e91deeb66cb0efa971e08229d1a160330e08da54af0689add", "c2c46173481b9ced61123d2e293b42ede5a1b323210eb2a684df0874ffe09047"},
    {30050, "c2c46173481b9ced61123d2e293b42ede5a1b323210eb2a684df0874ffe09047", "005e30899481055e", "ba30c61cc5a2c74a5ecaf505965140a08f24a296d687e78720f0b48baf712f2d", "ea42197eb2ba79c63cb5e655b8b1f612c5f08aae1a49ff236795a3516d87bc71"},
    {30099, "ea42197eb2ba79c63cb5e655b8b1f612c5f08aae1a49ff236795a3516d87bc71", "005ea6aef136f88b", "cfd5e46048cd133d40f261fe8704e51d3f497fc14203ac6a9ef6a0841780b1cd", "49e15ba4bf501ce8fe8876101c808e24c69a859be15de554bf85dbc095491bd6"},
    {59950, "49e15ba4bf501ce8fe8876101c808e24c69a859be15de554bf85dbc095491bd6", "02ebe0503bd7b1da", "21511fbaa31fb9f5fc4998a754e97b3083a866f4de86fa7500a633346f56d773", "f5c50ba5c0d6210ddb16250ec3efda178de857b2b1703d8d5403bd0f848e19cf"},
    {59999, "f5c50ba5c0d6210ddb16250ec3efda178de857b2b1703d8d5403bd0f848e19cf", "02edb6275bd221e3", "653eda37d337e39d311d22be9bbd3458d3abee4e643bee4a7280a6d08106ef98", "341562d10d4afb706ec2c8d5537cb0c810de02b4ebb0a0eea5ae335af6fb2e88"},
};
int hexval(char ch) { if (ch >= '0' && ch <= '9') return ch - '0'; if (ch >= 'a' && ch <= 'f') return ch - 'a' + 10; if (ch >= 'A' && ch <= 'F') return ch - 'A' + 10; return -1; }
bool hex32(const char* s, uint8_t out[32]) { if (std::strlen(s) != 64) return false; for (int i = 0; i < 32; ++i) { int a = hexval(s[2 * i]), b = hexval(s[2 * i + 1]); if (a < 0 || b < 0) return false; out[i] = static_cast<uint8_t>(a * 16 + b); } return true; }

int ab_vectors_check(size_t* count_out) {
    size_t n = 0; int last = -1;
    ethash::epoch_context_ptr ctx{nullptr, ethash_destroy_epoch_context};
    dag_source dag{};
    for (const tv& t : kVectors) {
        uint8_t header[32], mix[32], fin[32];
        if (!hex32(t.header, header) || !hex32(t.mix, mix) || !hex32(t.final, fin)) return IGNPOW_E_VECTORS;
        const uint64_t nonce = std::strtoull(t.nonce, nullptr, 16);
        const int e = epoch_number(static_cast<uint64_t>(t.block));
        if (e != last) { ctx = ethash::create_epoch_context(e); if (!ctx) return IGNPOW_E_VECTORS; last = e; dag.ctx = ctx.get(); dag.full = nullptr; for (uint32_t k = 0; k < kCacheWords / 16; ++k) node(*ctx, k, dag.l1 + 16 * k); }
        result r; compute(dag, static_cast<uint64_t>(t.block), header, nonce, r);
        if (std::memcmp(r.mix, mix, 32) != 0 || std::memcmp(r.final, fin, 32) != 0) {
            if (std::getenv("RXE1_DEBUG")) {
                char a[65], b[65]; for (int i = 0; i < 32; ++i) { std::snprintf(a + 2 * i, 3, "%02x", r.mix[i]); std::snprintf(b + 2 * i, 3, "%02x", r.final[i]); }
                std::fprintf(stderr, "progpow-epic vector block %d: computed mix %s final %s; expected mix %s final %s\n", t.block, a, b, t.mix, t.final);
            }
            return IGNPOW_E_VECTORS;
        }
        ++n;
    }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace pe

const candidate progpow_epic = {
    IGNPOW_ALGO_PROGPOW_EPIC,
    pe::ab_check_params, pe::ab_epoch_of, pe::ab_cache_bytes, pe::ab_dataset_bytes, pe::ab_create,
    pe::ab_hash, pe::ab_verify, pe::ab_light_cache, pe::ab_vectors_check,
    "progpow-epic = EpicCash/progpow-rust@7a10ab82 (ProgPoW 0.9.2 over ethash revision 23, epoch 30000; the chfast ethash tree for the cache and dataset)",
};

}  // namespace ignpow

extern "C" {
void progpow_epic_tag(char* buf, size_t n) { if (buf && n) std::snprintf(buf, n, "progpow-epic-e30000"); }
void progpow_epic_pid(uint8_t out[32]) {
    const char* tag = "progpow-epic-e30000";
    blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, tag, std::strlen(tag)); blake3_hasher_finalize(&h, out, 32);
}
}
