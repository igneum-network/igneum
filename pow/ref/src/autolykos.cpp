// Candidate C3: Autolykos v2 as implemented in ergoplatform/ergo v6.0.7
// (ergo-core/.../AutolykosPowScheme.scala, CC0-1.0), reproduced in C++ with an
// in-house BLAKE2b (RFC 7693). Parameters are the mainnet ones: k = 32, n = 26
// (N base 2^26), the 5 percent table growth every 51,200 blocks from height
// 614,400 up to height 4,198,400, M = the 1024 big-endian longs 0..1023.
//
// Mapping onto the ABI: header = the 32-byte message m (Ergo's header digest
// without the PoW); nonce = the 8-byte big-endian nonce; mix_out = the 32-byte
// big-endian sum of the k elements (diagnostic, recomputed by verify);
// hash_out = the hit as a 32-byte big-endian integer. Ergo accepts hit < b;
// the ABI accepts hash <= boundary, so a caller mapping an Ergo target passes
// boundary = b - 1.
//
// Property worth the table: the element table depends on the height (element
// j = H(j, h, M)), so every block has its own table; "epoch" here is the height
// itself and dataset_bytes is N(height) x 32. A light context computes elements
// on the fly; a full context builds the whole table at creation on every core
// (N x 32 bytes, element j at 32 j, the 31 bytes then a pad) and reads it (F-12).

#include "candidate.hpp"
#include "blake2b.hpp"

#include <algorithm>
#include <cstring>
#include <memory>
#include <string>
#include <thread>
#include <vector>

namespace ignpow {
namespace {

constexpr int kK = 32;
constexpr int kNBase = 1 << 26;
constexpr uint64_t kIncreaseStart = 600ull * 1024ull;
constexpr uint64_t kIncreasePeriod = 50ull * 1024ull;
constexpr uint64_t kIncreaseHeightMax = 4198400ull;
constexpr uint64_t kMaxHeight = 0x7fffffffull;  // Scala Int height

bool params_default(const char* p) {
    if (p == nullptr) return true;
    std::string s(p);
    size_t a = s.find_first_not_of(" \t\r\n");
    if (a == std::string::npos) return true;
    size_t b = s.find_last_not_of(" \t\r\n");
    return s.substr(a, b - a + 1) == "{}";
}

// calcN as in the Scala: iterated 5 percent growth in Int arithmetic (N * 1.05 as double, truncated).
uint32_t calc_n(uint64_t height) {
    const uint64_t h = height < kIncreaseHeightMax ? height : kIncreaseHeightMax;
    if (h < kIncreaseStart) return kNBase;
    const uint64_t iters = (h - kIncreaseStart) / kIncreasePeriod + 1;
    int64_t n = kNBase;
    for (uint64_t i = 0; i < iters; ++i) n = static_cast<int64_t>(n / 100 * 105);  // the Scala: (step / 100 * 105), Int division first
    return static_cast<uint32_t>(n);
}

int height_ok(const char* p, uint64_t height) {
    if (!params_default(p)) return IGNPOW_E_PARAMS;
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    return IGNPOW_OK;
}

uint64_t al_epoch_of(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    return st == IGNPOW_OK ? height : 0;
}

uint64_t al_cache_bytes(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    return 0;
}

uint64_t al_dataset_bytes(const char* p, uint64_t height, int* status) {
    int st = height_ok(p, height);
    if (status) *status = st;
    return st == IGNPOW_OK ? static_cast<uint64_t>(calc_n(height)) * 32 : 0;
}

struct al_ctx final : ctx_base {
    uint64_t height = 0;
    uint32_t n = 0;
    std::vector<uint8_t> table;  // full contexts: N x 32 bytes, built at creation
    uint64_t epoch() const override { return height; }
    const char* algo() const override { return IGNPOW_ALGO_AUTOLYKOS_2; }
};

void element(uint32_t index, uint32_t height, uint8_t out31[31]);

ctx_base* al_create(const char* p, uint64_t height, int full, int* status) {
    int st = height_ok(p, height);
    if (st != IGNPOW_OK) { if (status) *status = st; return nullptr; }
    auto c = std::make_unique<al_ctx>();
    c->height = height;
    c->n = calc_n(height);
    if (full) {
        try { c->table.assign(static_cast<size_t>(c->n) * 32, 0); } catch (...) { if (status) *status = IGNPOW_E_MEMORY; return nullptr; }
        const unsigned nt = std::max(1u, std::thread::hardware_concurrency());
        std::vector<std::thread> ts;
        uint8_t* t = c->table.data();
        const uint32_t n = c->n;
        const uint32_t h = static_cast<uint32_t>(height);
        for (unsigned k = 0; k < nt; ++k)
            ts.emplace_back([=]() { for (uint32_t j = k; j < n; j += nt) element(j, h, t + size_t(j) * 32); });
        for (auto& th : ts) th.join();
    }
    if (status) *status = IGNPOW_OK;
    return c.release();
}

// element j of the context: the resident table of a full context, else computed
inline void element_of(const al_ctx& c, uint32_t j, uint8_t out31[31]) {
    if (!c.table.empty()) std::memcpy(out31, c.table.data() + size_t(j) * 32, 31);
    else element(j, static_cast<uint32_t>(c.height), out31);
}

struct M_table {
    uint8_t bytes[8192];
    M_table() {
        for (uint64_t i = 0; i < 1024; ++i)
            for (int b = 0; b < 8; ++b) bytes[i * 8 + b] = static_cast<uint8_t>(i >> (8 * (7 - b)));
    }
};
const M_table& M() { static const M_table t; return t; }

void be32(uint8_t out[4], uint32_t v) { for (int i = 0; i < 4; ++i) out[i] = static_cast<uint8_t>(v >> (8 * (3 - i))); }
void be64(uint8_t out[8], uint64_t v) { for (int i = 0; i < 8; ++i) out[i] = static_cast<uint8_t>(v >> (8 * (7 - i))); }

// H(indexBytes ++ heightBytes ++ M) with the first byte dropped: a 31-byte big-endian unsigned.
void element(uint32_t index, uint32_t height, uint8_t out31[31]) {
    blake2b b(32);
    uint8_t buf[8];
    be32(buf, index); b.update(buf, 4);
    be32(buf, height); b.update(buf, 4);
    b.update(M().bytes, 8192);
    uint8_t h[32];
    b.finish(h);
    std::memcpy(out31, h + 1, 31);
}

// Big-endian add of a 31-byte value into a 32-byte accumulator.
void add31(uint8_t acc[32], const uint8_t v[31]) {
    uint32_t carry = 0;
    for (int i = 30; i >= 0; --i) {
        uint32_t s = static_cast<uint32_t>(acc[i + 1]) + v[i] + carry;
        acc[i + 1] = static_cast<uint8_t>(s);
        carry = s >> 8;
    }
    acc[0] = static_cast<uint8_t>(acc[0] + carry);
}

int al_hash(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
            uint8_t mix_out[32], uint8_t hash_out[32]) {
    const auto& c = *static_cast<const al_ctx*>(ctx);
    if (height > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (height != c.height) return IGNPOW_E_EPOCH;
    const uint32_t N = c.n;
    uint8_t nonce_be[8];
    be64(nonce_be, nonce);
    // i = (last 8 bytes of H(m ++ nonce) as unsigned) mod N
    uint8_t h1[32];
    { blake2b b(32); b.update(header, 32); b.update(nonce_be, 8); b.finish(h1); }
    uint64_t prei8 = 0;
    for (int i = 24; i < 32; ++i) prei8 = (prei8 << 8) | h1[i];
    const uint32_t i0 = static_cast<uint32_t>(prei8 % N);
    // f = H(i ++ h ++ M) dropping the first byte
    uint8_t f[31];
    element_of(c, i0, f);
    // seed = f ++ m ++ nonce; indexes from H(seed) extended by its first 3 bytes
    uint8_t hs[32];
    { blake2b b(32); b.update(f, 31); b.update(header, 32); b.update(nonce_be, 8); b.finish(hs); }
    uint8_t ext[35];
    std::memcpy(ext, hs, 32);
    std::memcpy(ext + 32, hs, 3);
    uint8_t sum[32] = {0};
    for (int i = 0; i < kK; ++i) {
        const uint32_t w = (uint32_t(ext[i]) << 24) | (uint32_t(ext[i + 1]) << 16) | (uint32_t(ext[i + 2]) << 8) | uint32_t(ext[i + 3]);
        const uint32_t idx = w % N;
        uint8_t e[31];
        element_of(c, idx, e);
        add31(sum, e);
    }
    std::memcpy(mix_out, sum, 32);
    blake2b::hash(hash_out, 32, sum, 32);
    return IGNPOW_OK;
}

int al_verify(const ctx_base* ctx, uint64_t height, const uint8_t header[32], uint64_t nonce,
              const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], h[32];
    int st = al_hash(ctx, height, header, nonce, m, h);
    if (st != IGNPOW_OK) return st;
    const bool mix_ok = std::memcmp(m, mix, 32) == 0;
    const bool bound_ok = le256(h, boundary);
    if (!mix_ok) return IGNPOW_E_MIX;
    if (!bound_ok) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}

int al_light_cache(const ctx_base*, const void** data, uint64_t* bytes, uint32_t* item_bytes) {
    if (data) *data = nullptr;
    if (bytes) *bytes = 0;
    if (item_bytes) *item_bytes = 0;
    return IGNPOW_OK;
}

bool hex_eq(const uint8_t* b, size_t n, const char* hex) {
    static const char* d = "0123456789abcdef";
    for (size_t i = 0; i < n; ++i) if (d[b[i] >> 4] != hex[2 * i] || d[b[i] & 15] != hex[2 * i + 1]) return false;
    return true;
}

// Embedded vectors: the BLAKE2b digests the control rests on (RFC 7693 appendix
// A for the 512-bit "abc"; the 256-bit "abc" and empty-string digests are the
// published reference values, cross-checked against Python hashlib on the build
// box), and the N schedule at its published steps.
int al_vectors_check(size_t* count_out) {
    size_t n = 0;
    uint8_t out[64];
    blake2b::hash(out, 64, reinterpret_cast<const uint8_t*>("abc"), 3);
    if (!hex_eq(out, 64, "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923")) return IGNPOW_E_VECTORS;
    ++n;
    blake2b::hash(out, 32, reinterpret_cast<const uint8_t*>("abc"), 3);
    if (!hex_eq(out, 32, "bddd813c634239723171ef3fee98579b94964e3bb1cb3e427262c8c068d52319")) return IGNPOW_E_VECTORS;
    ++n;
    blake2b::hash(out, 32, reinterpret_cast<const uint8_t*>(""), 0);
    if (!hex_eq(out, 32, "0e5751c026e543b2e8ab2eb06099daa1d1e5df47778f7787faab45cdf12fe3a8")) return IGNPOW_E_VECTORS;
    ++n;
    // a 200-byte input crosses one block boundary
    uint8_t in[200];
    for (int i = 0; i < 200; ++i) in[i] = static_cast<uint8_t>(i);
    blake2b::hash(out, 32, in, 200);
    // cross-checked against Python hashlib.blake2b(bytes(range(200)), digest_size=32) on a build box, 9 Oct 2026
    if (!hex_eq(out, 32, "63c3d97a9f8894d5e043a707b0fee7f7ec4c049a23bbf1079df20b4165f9e22d")) return IGNPOW_E_VECTORS;
    ++n;
    if (calc_n(0) != 67108864u || calc_n(614399) != 67108864u || calc_n(614400) != 70464240u) return IGNPOW_E_VECTORS;
    ++n;
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

int al_check_params(const char* p) { return params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }

}  // namespace

const candidate autolykos_2 = {
    IGNPOW_ALGO_AUTOLYKOS_2,
    al_check_params,
    al_epoch_of,
    al_cache_bytes,
    al_dataset_bytes,
    al_create,
    al_hash,
    al_verify,
    al_light_cache,
    al_vectors_check,
    "ergo v6.0.7 AutolykosPowScheme (k 32, n 26, in-house BLAKE2b)",
};

}  // namespace ignpow
