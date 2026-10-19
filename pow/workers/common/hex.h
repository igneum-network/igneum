// hex.h: hex helpers shared by the workers. Header only.
#pragma once
#include <cstdint>
#include <string>

namespace igm {

inline std::string hex_of(const uint8_t *p, size_t n) {
    static const char *hx = "0123456789abcdef";
    std::string s; s.reserve(n * 2);
    for (size_t i = 0; i < n; i++) { s.push_back(hx[p[i] >> 4]); s.push_back(hx[p[i] & 15]); }
    return s;
}

inline int hex_nib(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

// exact length required
inline bool hex_to(const std::string &hex, uint8_t *out, size_t n) {
    if (hex.size() != n * 2) return false;
    for (size_t i = 0; i < n; i++) {
        int a = hex_nib(hex[2 * i]), b = hex_nib(hex[2 * i + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = (uint8_t)((a << 4) | b);
    }
    return true;
}

inline std::string hex64(uint64_t v) {
    uint8_t b[8];
    for (int i = 0; i < 8; i++) b[i] = (uint8_t)(v >> (56 - 8 * i));
    return hex_of(b, 8);
}

inline bool hex64_to(const std::string &hex, uint64_t *out) {
    uint8_t b[8];
    if (!hex_to(hex, b, 8)) return false;
    uint64_t v = 0;
    for (int i = 0; i < 8; i++) v = (v << 8) | b[i];
    *out = v;
    return true;
}

}  // namespace igm
