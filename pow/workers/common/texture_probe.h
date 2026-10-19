// The texture determinism probe (14 Oct 2026, the project's gate for the texture discipline): a 4096 x 4096 R32_UINT texture whose texel
// (x, y) is fmix32(y * 4096 + x), sampled 1,000,000 times with point filtering at the texel centre under the fixed sequence
// s_i = fmix32(i * 0x9E3779B1 + 1), x = s & 4095, y = (s >> 12) & 4095, through the texture unit; the digest is BLAKE3 of the
// samples as little-endian u32 in order. The same five lines on every vendor (the GPU team's CUDA probe shares them); the host computes
// the expected samples beside the card's, so a line reads "card <digest> host <digest> equal/differs".
#pragma once
#include <cstdint>
#include <cstdio>
#include <string>
#include <vector>
#include "ih_params.h"   // ih_blake3_full
#include "hex.h"

namespace igm {
inline uint32_t tp_fmix32(uint32_t h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }
constexpr uint32_t kTpWidth = 4096, kTpHeight = 4096, kTpSamples = 1000000u;
inline std::vector<uint32_t> tp_texture() { std::vector<uint32_t> t((size_t)kTpWidth * kTpHeight); for (uint32_t y = 0; y < kTpHeight; y++) for (uint32_t x = 0; x < kTpWidth; x++) t[(size_t)y * kTpWidth + x] = tp_fmix32(y * kTpWidth + x); return t; }
inline void tp_coord(uint32_t i, uint32_t &x, uint32_t &y) { const uint32_t s = tp_fmix32(i * 0x9E3779B1u + 1u); x = s & 4095u; y = (s >> 12) & 4095u; }
inline std::vector<uint32_t> tp_expected() { std::vector<uint32_t> e(kTpSamples); for (uint32_t i = 0; i < kTpSamples; i++) { uint32_t x, y; tp_coord(i, x, y); e[i] = tp_fmix32(y * kTpWidth + x); } return e; }
inline std::string tp_digest(const std::vector<uint32_t> &v) { std::vector<uint8_t> b(v.size() * 4); for (size_t i = 0; i < v.size(); i++) { b[4 * i] = (uint8_t)v[i]; b[4 * i + 1] = (uint8_t)(v[i] >> 8); b[4 * i + 2] = (uint8_t)(v[i] >> 16); b[4 * i + 3] = (uint8_t)(v[i] >> 24); } uint8_t d[32]; ih_blake3_full(b.data(), b.size(), d); return hex_of(d, 32); }
// the one line and the JSON record; the first differing sample named when they differ
inline int tp_report(const char *backend, const std::string &device, const std::vector<uint32_t> &card, const std::string &note) {
    const std::vector<uint32_t> host = tp_expected(); const std::string dc = tp_digest(card), dh = tp_digest(host); const bool eq = dc == dh;
    size_t first = card.size(); uint32_t cv = 0, hv = 0; if (!eq) for (size_t i = 0; i < card.size() && i < host.size(); i++) if (card[i] != host[i]) { first = i; cv = card[i]; hv = host[i]; break; }
    fprintf(stderr, "texture probe: %s %s: card %s host %s %s\n", backend, device.c_str(), dc.c_str(), dh.c_str(), eq ? "equal" : "DIFFERS");
    printf("{\"texture_probe\": {\"backend\": \"%s\", \"device\": \"%s\", \"width\": %u, \"height\": %u, \"format\": \"R32_UINT\", \"filter\": \"nearest\", \"coords\": \"unnormalised, texel centre\", \"samples\": %u, \"card_digest\": \"%s\", \"host_digest\": \"%s\", \"equal\": %s, \"first_differing_sample\": %lld, \"card_value\": %u, \"host_value\": %u, \"note\": \"%s\"}}\n",
           backend, device.c_str(), kTpWidth, kTpHeight, kTpSamples, dc.c_str(), dh.c_str(), eq ? "true" : "false", eq ? -1ll : (long long)first, cv, hv, note.c_str());
    fflush(stdout); return eq ? 0 : 3;
}
}
