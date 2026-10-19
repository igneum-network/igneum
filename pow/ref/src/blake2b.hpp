// BLAKE2b (RFC 7693), unkeyed, written in-house for the Autolykos control.
// Fixed-width unsigned arithmetic, little-endian words as the RFC states.
#pragma once

#include <cstddef>
#include <cstdint>
#include <cstring>

namespace ignpow {

class blake2b {
public:
    explicit blake2b(size_t outlen) : outlen_(outlen) {
        static const uint64_t iv[8] = {0x6a09e667f3bcc908ull, 0xbb67ae8584caa73bull, 0x3c6ef372fe94f82bull, 0xa54ff53a5f1d36f1ull,
                                       0x510e527fade682d1ull, 0x9b05688c2b3e6c1full, 0x1f83d9abfb41bd6bull, 0x5be0cd19137e2179ull};
        for (int i = 0; i < 8; ++i) h_[i] = iv[i];
        h_[0] ^= 0x01010000ull ^ static_cast<uint64_t>(outlen);  // no key, fanout 1, depth 1
        t_[0] = t_[1] = 0;
        buflen_ = 0;
    }
    void update(const uint8_t* in, size_t n) {
        while (n > 0) {
            if (buflen_ == 128) {  // a full block is compressed only when more input follows
                t_[0] += 128; if (t_[0] < 128) ++t_[1];
                compress(buf_, false);
                buflen_ = 0;
            }
            size_t take = 128 - buflen_;
            if (take > n) take = n;
            std::memcpy(buf_ + buflen_, in, take);
            buflen_ += take; in += take; n -= take;
        }
    }
    void finish(uint8_t* out) {
        t_[0] += buflen_; if (t_[0] < buflen_) ++t_[1];
        std::memset(buf_ + buflen_, 0, 128 - buflen_);
        compress(buf_, true);
        uint8_t full[64];
        for (int i = 0; i < 8; ++i) store64(full + 8 * i, h_[i]);
        std::memcpy(out, full, outlen_);
    }
    static void hash(uint8_t* out, size_t outlen, const uint8_t* in, size_t n) {
        blake2b b(outlen); b.update(in, n); b.finish(out);
    }

private:
    static uint64_t load64(const uint8_t* p) {
        uint64_t v = 0;
        for (int i = 7; i >= 0; --i) v = (v << 8) | p[i];
        return v;
    }
    static void store64(uint8_t* p, uint64_t v) { for (int i = 0; i < 8; ++i) p[i] = static_cast<uint8_t>(v >> (8 * i)); }
    static uint64_t rotr64(uint64_t x, unsigned c) { return (x >> c) | (x << (64 - c)); }
    static void g(uint64_t* v, int a, int b, int c, int d, uint64_t x, uint64_t y) {
        v[a] = v[a] + v[b] + x; v[d] = rotr64(v[d] ^ v[a], 32);
        v[c] = v[c] + v[d];     v[b] = rotr64(v[b] ^ v[c], 24);
        v[a] = v[a] + v[b] + y; v[d] = rotr64(v[d] ^ v[a], 16);
        v[c] = v[c] + v[d];     v[b] = rotr64(v[b] ^ v[c], 63);
    }
    void compress(const uint8_t* block, bool last) {
        static const uint8_t sigma[12][16] = {
            {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, {14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
            {11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4}, {7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
            {9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13}, {2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
            {12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11}, {13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
            {6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5}, {10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
            {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, {14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3}};
        static const uint64_t iv[8] = {0x6a09e667f3bcc908ull, 0xbb67ae8584caa73bull, 0x3c6ef372fe94f82bull, 0xa54ff53a5f1d36f1ull,
                                       0x510e527fade682d1ull, 0x9b05688c2b3e6c1full, 0x1f83d9abfb41bd6bull, 0x5be0cd19137e2179ull};
        uint64_t m[16], v[16];
        for (int i = 0; i < 16; ++i) m[i] = load64(block + 8 * i);
        for (int i = 0; i < 8; ++i) { v[i] = h_[i]; v[i + 8] = iv[i]; }
        v[12] ^= t_[0]; v[13] ^= t_[1];
        if (last) v[14] = ~v[14];
        for (int r = 0; r < 12; ++r) {
            const uint8_t* s = sigma[r];
            g(v, 0, 4, 8, 12, m[s[0]], m[s[1]]);   g(v, 1, 5, 9, 13, m[s[2]], m[s[3]]);
            g(v, 2, 6, 10, 14, m[s[4]], m[s[5]]);  g(v, 3, 7, 11, 15, m[s[6]], m[s[7]]);
            g(v, 0, 5, 10, 15, m[s[8]], m[s[9]]);  g(v, 1, 6, 11, 12, m[s[10]], m[s[11]]);
            g(v, 2, 7, 8, 13, m[s[12]], m[s[13]]); g(v, 3, 4, 9, 14, m[s[14]], m[s[15]]);
        }
        for (int i = 0; i < 8; ++i) h_[i] ^= v[i] ^ v[i + 8];
    }

    uint64_t h_[8];
    uint64_t t_[2];
    uint8_t buf_[128];
    size_t buflen_;
    size_t outlen_;
};

}  // namespace ignpow
