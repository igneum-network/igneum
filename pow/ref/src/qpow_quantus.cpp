// Candidate qpow-quantus: Quantus Network's QPoW (docs/igneumhash/controls/SOURCES.md), written from the vendored
// texts: qpow-math/src/lib.rs (pow/third_party/qpow-quantus-f1176ce, chain v1.0.1) and the crate qp-poseidon-core
// 3.1.0 (pow/third_party/qp-poseidon-core-3.1.0: goldilocks.rs, poseidon2.rs, lib.rs, serialization.rs). The hash
// is Poseidon2 over the Goldilocks field (width 12, rate 8, 4 initial and 4 terminal external rounds, 22 internal
// rounds, x^7, the crate's constants) in the crate's sponge: the input bytes as 4-byte little-endian elements with
// a terminator element (the remainder padded with a 1 byte), the 1 || 0* padding to the block, the 4-element digest
// as 32 little-endian bytes, squeezed twice with a permutation between (hash_squeeze_twice); QPoW hashes
// block_hash(32) || nonce(64) and reads the 64 bytes as a big-endian 512-bit integer, valid when below
// 2^512 - 1 / difficulty.
//
// ABI mapping (a harness rule, never consensus): the header is the block hash; the 64-byte nonce is LE64(nonce)
// followed by 56 zero bytes; hash_out is the first 32 bytes of the 64 (the most significant, compared big-endian
// like the chain's), mix_out the last 32. No document (NULL or "{}"); no epoch; no cache. The side API of
// controls.h exposes the 64-byte hash and the field hash of arbitrary bytes.

#include "candidate.hpp"
#include "controls.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace ignpow {
namespace qp {

typedef unsigned __int128 u128;
constexpr uint64_t P = 0xFFFFFFFF00000001ull;
constexpr int W = 12, RATE = 8, OUT = 4, RI = 22, RE_HALF = 4;

inline uint64_t add(uint64_t a, uint64_t b) { u128 s = (u128)a + b; return (uint64_t)(s >= P ? s - P : s); }
inline uint64_t mul(uint64_t a, uint64_t b) { return (uint64_t)(((u128)a * b) % P); }
inline uint64_t exp7(uint64_t x) { uint64_t x2 = mul(x, x), x4 = mul(x2, x2); return mul(mul(x4, x2), x); }
inline uint64_t canon(uint64_t x) { return x >= P ? x - P : x; }

const uint64_t IC[RI] = {0x97f7798a784ad863ull, 0xd1d2bf082f60d4f0ull, 0x69a377a79f9ad206ull, 0xa9d06906a3858e24ull, 0x295275001eede5b5ull, 0x5874e441117bd746ull, 0x8a084bbba8ed86ccull, 0x3defd7645cde6425ull, 0x3998cfe6871cc137ull, 0x3e52ef8bca48314aull, 0x964a209f85dc9eccull, 0x3fcc9ee82cc4577eull, 0x8e79b4a5d0096d6dull, 0x8492362ad2392556ull, 0xee72f470262574d6ull, 0x1e0e18496da2444aull, 0x0f3a74bf215eaac6ull, 0x1b061b76a1c0ded3ull, 0x192c42d86803d7a6ull, 0xf6d49ff997ae0260ull, 0x3ec372e7a0fa3786ull, 0x5538cdf4f23445d3ull};
const uint64_t MD[W] = {0xc3b6c08e23ba9300ull, 0xd84b5de94a324fb6ull, 0x0d0c371c5b35b84full, 0x7964f570e7188037ull, 0x5daf18bbd996604bull, 0x6743bc47b9595257ull, 0x5528b9362c59bb70ull, 0xac45e25b7127b68bull, 0xa2077d7dfbb606b5ull, 0xf3faac6faee378aeull, 0x0c6388b51545e883ull, 0xd27dbb6944917b60ull};
const uint64_t IE[RE_HALF][W] = {
    {0xc002e770975b1607ull, 0xbca51a8dfe14593aull, 0x72938dfbe774f7f9ull, 0xe4f2fe29e03234acull, 0xd5e0ba2f541b6449ull, 0xec33b868f3cc46c1ull, 0x486dcb55419d475aull, 0x6c1cb2a358cc24f1ull, 0xe3f30d509a1436bbull, 0xd9a64f068dca7c29ull, 0xe59b3f57aabba1aeull, 0x2a3dd4505b478fdcull},
    {0xada1f8dc7676ed25ull, 0x2711aa8b5509d516ull, 0x4ae6acd0c9c92897ull, 0x56eb3d6b5256d67aull, 0x1f7a9d55923bf51eull, 0x3600427d397a7f68ull, 0xe5076df75b72c3d0ull, 0xfcd59aa12c6090adull, 0xcd895e8c68b57a9eull, 0x41df7ef9d730ae3eull, 0xee3e2b889abe977dull, 0xd29bb7edbeb9c405ull},
    {0x7d5c08eef608e382ull, 0x89ae889caaf0802cull, 0xb35a8e976d2af617ull, 0xdb14234eafaf5173ull, 0x78f04462d48b1c98ull, 0x265293b0e47ce88aull, 0x999a649b69b9d32full, 0x64b0a186698e01d3ull, 0xee0b22d0dfae8bb8ull, 0x4fd53e50ca04a7eeull, 0x5762bfe181f25047ull, 0xf51593e2beb5e3bdull},
    {0x1e5e2b5760e32477ull, 0x622462a1f9aaaeedull, 0xaa284b3ecdb222aeull, 0x63c8e72f542bf3fcull, 0x3ba588cacb43b5e0ull, 0x23eda6f3c99150ddull, 0xaad3bea4baac9a5aull, 0xe9da8d699b94184aull, 0xcdb13f4cd93e024cull, 0x902cbd0956f655e3ull, 0x5b4e40ffc759532full, 0xde795c20a2357af7ull}
};
const uint64_t TE[RE_HALF][W] = {
    {0x7b72c539e0ea4c6eull, 0x144573dae2ce9976ull, 0x802028b68f35fc88ull, 0x6d36c5022c4fe7c2ull, 0xa205d0ffa9b9def3ull, 0xf6e7e38b1ea6ba2full, 0x34f7909ae5258d64ull, 0xb0464d9d77b97fcaull, 0x64ddb9d5de7e00a6ull, 0x0ed0d75c27975d97ull, 0x1cbb36f11127338bull, 0x6673e505cfd0b6baull},
    {0x605f902830872e01ull, 0x3fd5eb927e95fe4full, 0xe81025b5a24c69cdull, 0xf7d0ce75de23f74eull, 0xf39942b6a8585089ull, 0x6d808a08f7b71df6ull, 0xf8806b6588f49a8bull, 0x57df2d8c2a32107aull, 0x16e7c2074d654a2dull, 0x213de241fcf33835ull, 0xb0f2b8905a0976f6ull, 0xd8e3cf2bbd355417ull},
    {0xe498691679d9330full, 0x763b45d2a3821b28ull, 0x0908bf65eb0a1f0dull, 0x7691eb2d194b24f4ull, 0x0e43551233ae13b2ull, 0x93c393dbfc2fe76full, 0x98f607485d48cdeaull, 0xe3d95f30309819c0ull, 0x1ef581a93eaf6acfull, 0x0b24c1b7a030fca4ull, 0x624370be5670b327ull, 0x5f1e28615a11e486ull},
    {0xfe04051f909e042bull, 0x7257e5b147fd3803ull, 0xe6ae134bb82f2e78ull, 0x5711fd5cf4784511ull, 0xf83a42660c08c0bcull, 0x2cd8c96d9a3ce855ull, 0x7d2ffb1bb0e17271ull, 0x85ae1528caea3811ull, 0x52a345d5c7adb0b8ull, 0x504c4c51f3faee94ull, 0xbce34a649cfccaf9ull, 0xe0a3389266fb6dc9ull}
};

void mat4(uint64_t* x) {
    const uint64_t t01 = add(x[0], x[1]), t23 = add(x[2], x[3]), t0123 = add(t01, t23), t01123 = add(t0123, x[1]), t01233 = add(t0123, x[3]);
    const uint64_t x0 = x[0], x2 = x[2];
    x[3] = add(t01233, add(x0, x0));
    x[1] = add(t01123, add(x2, x2));
    x[0] = add(t01123, t01);
    x[2] = add(t01233, t23);
}
void external_layer(uint64_t s[W]) {
    for (int c = 0; c < W; c += 4) mat4(s + c);
    uint64_t sums[4];
    for (int k = 0; k < 4; ++k) { sums[k] = 0; for (int j = 0; j < W; j += 4) sums[k] = add(sums[k], s[j + k]); }
    for (int i = 0; i < W; ++i) s[i] = add(s[i], sums[i % 4]);
}
void internal_layer(uint64_t s[W]) {
    uint64_t sum = 0; for (int i = 0; i < W; ++i) sum = add(sum, s[i]);
    for (int i = 0; i < W; ++i) s[i] = add(sum, mul(s[i], MD[i]));
}
void permute(uint64_t s[W]) {
    external_layer(s);
    for (int r = 0; r < RE_HALF; ++r) { for (int i = 0; i < W; ++i) s[i] = exp7(add(s[i], IE[r][i])); external_layer(s); }
    for (int r = 0; r < RI; ++r) { s[0] = exp7(add(s[0], IC[r])); internal_layer(s); }
    for (int r = 0; r < RE_HALF; ++r) { for (int i = 0; i < W; ++i) s[i] = exp7(add(s[i], TE[r][i])); external_layer(s); }
}

struct sponge {
    uint64_t state[W] = {0}, buf[RATE] = {0}; int buf_len = 0;
    void push(uint64_t x) { buf[buf_len++] = x; if (buf_len == RATE) { for (int i = 0; i < RATE; ++i) state[i] = add(state[i], buf[i]); permute(state); for (int i = 0; i < RATE; ++i) buf[i] = 0; buf_len = 0; } }
    // the injective 4-bytes-per-element encoding with the terminator element (serialization.rs bytes_to_u64s_iter)
    void append_bytes(const uint8_t* b, size_t n) {
        size_t pos = 0;
        for (; pos + 4 <= n; pos += 4) push((uint64_t)b[pos] | ((uint64_t)b[pos + 1] << 8) | ((uint64_t)b[pos + 2] << 16) | ((uint64_t)b[pos + 3] << 24));
        uint8_t last[4] = {0, 0, 0, 0}; size_t rem = n - pos; for (size_t i = 0; i < rem; ++i) last[i] = b[pos + i]; last[rem] = 1;
        push((uint64_t)last[0] | ((uint64_t)last[1] << 8) | ((uint64_t)last[2] << 16) | ((uint64_t)last[3] << 24));
    }
    void finalize_state() { push(1); while (buf_len != 0) push(0); }
    void digest_bytes(uint8_t out[32]) const { for (int i = 0; i < OUT; ++i) { const uint64_t v = canon(state[i]); for (int b = 0; b < 8; ++b) out[8 * i + b] = (uint8_t)(v >> (8 * b)); } }
};

void hash_bytes(const uint8_t* b, size_t n, uint8_t out[32]) { sponge s; s.append_bytes(b, n); s.finalize_state(); s.digest_bytes(out); }
void hash_squeeze_twice(const uint8_t* b, size_t n, uint8_t out[64]) { sponge s; s.append_bytes(b, n); s.finalize_state(); s.digest_bytes(out); permute(s.state); s.digest_bytes(out + 32); }

// qpow-math get_nonce_hash: block_hash(32) || nonce(64), the 64 bytes read big-endian.
void nonce_hash(const uint8_t block_hash[32], const uint8_t nonce[64], uint8_t out[64]) {
    uint8_t in[96]; std::memcpy(in, block_hash, 32); std::memcpy(in + 32, nonce, 64);
    hash_squeeze_twice(in, 96, out);
}

bool params_default(const char* p) { if (!p) return true; std::string s; for (const char* c = p; *c; ++c) if (*c != ' ' && *c != '\t' && *c != '\r' && *c != '\n') s += *c; return s == "{}"; }
// The harness epoch (never consensus): 2^24 heights per epoch, so the ABI's epoch rules are exercised by the shared
// classes; the hash itself reads no height.
constexpr uint64_t kEpochLen = 1ull << 16;  // 65536 heights: the shared classes' epoch multiples (448 and 598 epochs) stay inside the height range
constexpr uint64_t kMaxHeight = 0x7fffffffull;  // the ABI's height range (IGNPOW_E_HEIGHT beyond it), as the other candidates
struct qp_ctx final : ctx_base { uint64_t ep = 0; uint64_t epoch() const override { return ep; } const char* algo() const override { return IGNPOW_ALGO_QPOW_QUANTUS; } };
int ab_check_params(const char* p) { return params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; }
uint64_t ab_epoch_of(const char* p, uint64_t h, int* status) { int st = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; if (st == IGNPOW_OK && h > kMaxHeight) st = IGNPOW_E_HEIGHT; if (status) *status = st; return st == IGNPOW_OK ? h / kEpochLen : 0; }
uint64_t ab_zero(const char* p, uint64_t, int* status) { int st = params_default(p) ? IGNPOW_OK : IGNPOW_E_PARAMS; if (status) *status = st; return 0; }
ctx_base* ab_create(const char* p, uint64_t h, int, int* status) { if (!params_default(p)) { if (status) *status = IGNPOW_E_PARAMS; return nullptr; } if (h > kMaxHeight) { if (status) *status = IGNPOW_E_HEIGHT; return nullptr; } if (status) *status = IGNPOW_OK; auto* c = new qp_ctx(); c->ep = h / kEpochLen; return c; }
void harness_nonce(uint64_t nonce, uint8_t out[64]) { std::memset(out, 0, 64); for (int b = 0; b < 8; ++b) out[b] = (uint8_t)(nonce >> (8 * b)); }
int ab_hash(const ctx_base* c, uint64_t ht, const uint8_t header[32], uint64_t nonce, uint8_t mix_out[32], uint8_t hash_out[32]) {
    if (ht > kMaxHeight) return IGNPOW_E_HEIGHT;
    if (ht / kEpochLen != static_cast<const qp_ctx*>(c)->ep) return IGNPOW_E_EPOCH;
    uint8_t n64[64], h[64]; harness_nonce(nonce, n64); nonce_hash(header, n64, h);
    std::memcpy(hash_out, h, 32); std::memcpy(mix_out, h + 32, 32); return IGNPOW_OK;
}
int ab_verify(const ctx_base* c, uint64_t h, const uint8_t header[32], uint64_t nonce, const uint8_t mix[32], const uint8_t boundary[32]) {
    uint8_t m[32], o[32]; int st = ab_hash(c, h, header, nonce, m, o); if (st != IGNPOW_OK) return st;
    if (std::memcmp(m, mix, 32) != 0) return IGNPOW_E_MIX;
    if (!le256(o, boundary)) return IGNPOW_E_BOUNDARY;
    return IGNPOW_OK;
}
int ab_light_cache(const ctx_base*, const void** data, uint64_t* bytes, uint32_t* item_bytes) { if (data) *data = nullptr; if (bytes) *bytes = 0; if (item_bytes) *item_bytes = 0; return IGNPOW_OK; }

#define FAILV() do { if (std::getenv("RXE1_DEBUG")) std::fprintf(stderr, "qpow-quantus self-test failed at line %d\n", __LINE__); return IGNPOW_E_VECTORS; } while (0)
struct bv { int n; uint8_t b[64]; const char* hex; };
const bv kByteVectors[] = {
    {0, {0}, "4d8d22af81f6c27a005a07028590ef4ee480f6c4b93f813daf9de47a07c8ae86"},
    {1, {0}, "8f5b42e350ff5a12788210c86c2bcd49243b8f9350de818b3b0c56839a42ebad"},
    {2, {0, 0}, "3e6ee24fb61a22f4d825b72fc8ebd359e3b3b9566e246c71c3e450ebe3262f9c"},
    {3, {0, 0, 0}, "34f4338a6f1b671062a3ac00b37ca05a47b43e16e589ccaa5b063416ba42356b"},
    {4, {0, 0, 0, 0}, "7bac8c6bc49b0b750f2ce0912b815a2cb4ae20c75ac430850257882d9d321afa"},
    {5, {0, 0, 0, 0, 0}, "c95cfddb573adf4070b3d7c8d2dfbbee48b4b973d80cbda2b458abe7bb6f0def"},
    {6, {0, 0, 0, 0, 0, 0}, "a4dc08d0a8c5ea44007462fe1fd8e45962d4ea85c420eab4140fbb30b5b5e111"},
    {7, {0, 0, 0, 0, 0, 0, 0}, "b01975012df91d9f9f040c34655f23f3ec1f6d1738d85679e9848143756637c9"},
    {8, {0, 0, 0, 0, 0, 0, 0, 0}, "eacd9e48d2e968131e48c8e69f2a211cc06c7778db6c5467348b45418fc7f585"},
    {9, {0, 0, 0, 0, 0, 0, 0, 0, 0}, "00df670e8ec0751d3fb9b5f0281d0af9a7a82f62ad35a21247a9d6117daec151"},
    {14, {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, "d6182896f274c5d9640972e2bf2a5e893e516a21adfdd8ebd39969128d619934"},
    {15, {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, "15fc2f3c3bc51c96797b889d4fecfcd3535b959f510c007598a87f099e356303"},
    {4, {0, 0, 0, 1}, "6ffff0c97262139567c426e916c1fd70c924010153c366bb2a8957ea89902942"},
    {8, {1, 2, 3, 4, 5, 6, 7, 8}, "131020b2e74819343f8568258ae2e9717e9b2253d57baabab78a518bc7499a8b"},
    {32, {255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}, "41260a4322e97dc3dda2b5f70b5ffb1b43071ad5510e101f34209721042c0987"},
    {11, {104, 101, 108, 108, 111, 32, 119, 111, 114, 108, 100}, "fd1f5d7d4701c25bbdd5dd6e3be6abb474fffbaa402f814dce95f8283abbf3e7"},
    {32, {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}, "36884f9093be80632397f5736dce2fece627a4182daf3cdbf8bf12c8e3e02668"},
    {64, {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}, "dd0d06fbe4e7575d0eeac53706482cbbe592e269a35bcd5591a495814371724e"},
};
const uint64_t kZero[W] = {0xc9bc9432e1686884ull, 0x03ecbab0dcdd2189ull, 0x5e7ac885b3dc1215ull, 0x6ac07513801d191full, 0xca5c593fb184dcfcull, 0x414dec5f3e455287ull, 0x1a17df170127ae41ull, 0xe7e592bd0af9b0a5ull, 0xc71a9b27edc66a4cull, 0x2728671759ac43c2ull, 0xb9969c20f7f672f9ull, 0xc5140b586823b92full};
const uint64_t kSeq[W] = {0x7e9574e2a3d6c48bull, 0x9d7bc16d282d2f2bull, 0x798826626d94a498ull, 0x0831011bb22304c7ull, 0xbdccb5931fffd16cull, 0xe98687714dacbefcull, 0xc6a1ed29dd75e027ull, 0x1aec96681d15f765ull, 0xc74b2c710b170a23ull, 0x5fb4aff45e9c24fbull, 0x1fb3d228db0127ebull, 0xe201a7e214b16e74ull};
std::string hexs(const uint8_t* p, size_t n) { static const char* d = "0123456789abcdef"; std::string s; for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; } return s; }

// The crate's tests: the permutation on the zero and the sequential state (plonky3 agreement), the eighteen
// hash_bytes vectors, hash_squeeze_twice's first half equal to hash_bytes.
int ab_vectors_check(size_t* count_out) {
    size_t n = 0;
    { uint64_t s[W] = {0}; permute(s); for (int i = 0; i < W; ++i) if (canon(s[i]) != kZero[i]) FAILV(); ++n; }
    { uint64_t s[W]; for (int i = 0; i < W; ++i) s[i] = (uint64_t)(i + 1); permute(s); for (int i = 0; i < W; ++i) if (canon(s[i]) != kSeq[i]) FAILV(); ++n; }
    for (const bv& v : kByteVectors) { uint8_t out[32]; hash_bytes(v.b, (size_t)v.n, out); if (hexs(out, 32) != v.hex) FAILV(); ++n; }
    { uint8_t in[96]; for (int i = 0; i < 96; ++i) in[i] = (uint8_t)(7 * i + 3); uint8_t a[32], b[64]; hash_bytes(in, 96, a); hash_squeeze_twice(in, 96, b); if (std::memcmp(a, b, 32) != 0 || std::memcmp(a, b + 32, 32) == 0) FAILV(); ++n; }
    if (count_out) *count_out = n;
    return IGNPOW_OK;
}

}  // namespace qp

const candidate qpow_quantus = {
    IGNPOW_ALGO_QPOW_QUANTUS,
    qp::ab_check_params, qp::ab_epoch_of, qp::ab_zero, qp::ab_zero, qp::ab_create,
    qp::ab_hash, qp::ab_verify, qp::ab_light_cache, qp::ab_vectors_check,
    "qpow-quantus = Quantus-Network/chain@f1176cea qpow-math over qp-poseidon-core 3.1.0 (Poseidon2 Goldilocks width 12, double squeeze)",
};

}  // namespace ignpow

extern "C" {
void qpow_quantus_hash(const uint8_t block_hash[32], const uint8_t nonce[64], uint8_t out[64]) { ignpow::qp::nonce_hash(block_hash, nonce, out); }
void qpow_quantus_hash_bytes(const uint8_t* in, size_t n, uint8_t out[32]) { ignpow::qp::hash_bytes(in, n, out); }
void qpow_quantus_harness_nonce(uint64_t nonce, uint8_t out[64]) { ignpow::qp::harness_nonce(nonce, out); }
}
