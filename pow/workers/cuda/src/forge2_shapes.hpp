// forge2_shapes.hpp: Anvil's (version 21, pow/ref namespace forge2) per-batch shapes as device source, generated on the host.
// The batch (the period) is the nonce range under one header with one value of nonce >> batch_log2; its seed is
// pseed = BLAKE3(LP("IgneumHash/Forge/period") || E || header || LE64(nonce >> batch_log2)), its M shapes the fold chains over
// the seed (shape i's four words at indices 4i .. 4i+3), each shape's class order a Fisher-Yates of the quota by fchain(w0, q).
// The text emitted is self-contained (its own rotl, mask and fold) and defines fg2_dispatch(shape, R, SP, H, l): one step of the
// named shape, the 32 slots straight-line with the immediates, the sub-class bits and the load flags baked as constants; the
// hash's own words H[k] = fchain(hword, k) (the scratchpad addresses and the cmov predicate) stay runtime values, hoisted per
// sequence entry by the kernel. Slot k writes R[k] and reads R[7k+1 mod 32] and R[11k+5 mod 32]: the operands are static.
#pragma once
#include <cstdint>
#include <cstdio>
#include <string>

namespace forge2 {

inline uint32_t rotl32(uint32_t x, uint32_t n) { n &= 31u; return n ? ((x << n) | (x >> (32u - n))) : x; }
inline uint32_t fchain(uint32_t x, uint32_t v) { x = rotl32(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
enum : uint8_t { FMA = 0, FADDSUB, FMUL, IALU, ROT, MUL, MULHI, I2F, F2I, XLANE, STORE, CMOV };
static const uint8_t QUOTA[32] = { IALU, FMA, ROT, IALU, XLANE, FADDSUB, IALU, MUL, IALU, FMUL, ROT, IALU, STORE, FMA, IALU, ROT, MULHI, FADDSUB, IALU, I2F, ROT, IALU, XLANE, FMUL, IALU, FMA, ROT, CMOV, FADDSUB, IALU, F2I, MUL };

struct shape { uint32_t w[4]; uint8_t cls[32]; };

// the batch's shapes from the period seed (forge2::attempt's draw, byte for byte)
inline void draw_shapes(const uint8_t pseed[32], uint32_t n, shape* out) {
    uint32_t x[8];
    for (int k = 0; k < 8; ++k) x[k] = uint32_t(pseed[4 * k]) | (uint32_t(pseed[4 * k + 1]) << 8) | (uint32_t(pseed[4 * k + 2]) << 16) | (uint32_t(pseed[4 * k + 3]) << 24);
    for (uint32_t i = 0; i < n; ++i) {
        for (int j = 0; j < 4; ++j) { const uint32_t idx = 4 * i + j; x[idx & 7] = fchain(x[idx & 7], idx); out[i].w[j] = x[idx & 7]; }
        for (int k = 0; k < 32; ++k) out[i].cls[k] = QUOTA[k];
        for (int q = 31; q > 0; --q) { const uint32_t j = fchain(out[i].w[0], uint32_t(q)) % uint32_t(q + 1); const uint8_t t = out[i].cls[q]; out[i].cls[q] = out[i].cls[j]; out[i].cls[j] = t; }
    }
}

inline std::string u(uint32_t v) { char b[16]; std::snprintf(b, sizeof b, "%uu", v); return b; }

// one slot's statement: a = R[ak], b = R[bk] or the scratchpad word at ((R[bk] + H[k]) & 1023) >> 2 under the load flag
inline std::string slot(int k, uint8_t C, uint32_t imm, uint32_t q, bool load) {
    const int ak = (7 * k + 1) & 31, bk = (11 * k + 5) & 31;
    const std::string a = "R[" + std::to_string(ak) + "]";
    const std::string b = load ? "SP[((R[" + std::to_string(bk) + "] + H[" + std::to_string(k) + "]) & 1023u) >> 2]" : "R[" + std::to_string(bk) + "]";
    const std::string d = "R[" + std::to_string(k) + "]", H = "H[" + std::to_string(k) + "]";
    const std::string m = "fg2_mask", f = "__uint_as_float", uu = "__float_as_uint";
    switch (C) {
        case FMA: return d + " = " + m + "(" + uu + "(fmaf(" + f + "(" + m + "(" + a + ")), " + f + "(" + m + "(" + b + ")), " + f + "(" + m + "(" + d + ")))));";
        case FADDSUB: return d + " = " + m + "(" + uu + "(" + f + "(" + m + "(" + a + ")) " + ((q & 1u) ? "-" : "+") + " " + f + "(" + m + "(" + b + "))));";
        case FMUL: return d + " = " + m + "(" + uu + "(" + f + "(" + m + "(" + a + ")) * " + f + "(" + m + "(" + b + "))));";
        case IALU: switch (q % 5u) {
            case 0: return d + " = " + a + " + " + b + " + " + u(imm) + ";";
            case 1: return d + " = " + a + " - " + b + " - " + u(imm) + ";";
            case 2: return d + " = " + a + " ^ " + b + " ^ " + u(imm) + ";";
            case 3: return d + " = (" + a + " & " + b + ") ^ " + u(imm) + ";";
            default: return d + " = (" + a + " | " + b + ") ^ " + u(imm) + ";"; }
        case ROT: { const std::string sh = "((" + u(imm) + " ^ " + b + ") & 31u)"; switch (q & 3u) {
            case 0: return d + " = fg2_rotl(" + a + ", " + sh + ");";
            case 1: return d + " = fg2_rotl(" + a + ", (32u - " + sh + ") & 31u);";
            case 2: return d + " = (" + a + " << " + sh + ") + " + b + ";";
            default: return d + " = (" + a + " >> " + sh + ") + " + b + ";"; } }
        case MUL: return d + " = " + a + " * " + b + " + " + u(imm) + ";";
        case MULHI: return d + " = __umulhi(" + a + ", " + b + ") ^ " + u(imm) + ";";
        case I2F: return d + " = " + m + "(" + uu + "((float)(int)(" + a + " & 0x00FFFFFFu)));";
        case F2I: return d + " = " + m + "(" + a + ") ^ " + u(imm) + ";";
        case XLANE: return d + " = __shfl_sync(0xFFFFu, " + a + ", (l + " + u(1u + (q % 15u)) + ") & 15u, 16) + " + u(imm) + ";";
        case STORE: return "SP[((" + a + " + " + H + ") & 1023u) >> 2] = " + b + ";";
        default: return d + " = ((" + b + " >> (" + H + " & 31u)) & 1u) ? " + a + " : " + d + ";";   // CMOV
    }
}

// the device text: fg2_rotl, fg2_mask, the M shape steps, fg2_dispatch
inline std::string source(const uint8_t pseed[32], uint32_t n_shapes) {
    shape* sh = new shape[n_shapes];
    draw_shapes(pseed, n_shapes, sh);
    std::string s;
    s += "// Anvil (version 21) batch shapes, generated on the host from the period seed: the 32 slots of each shape straight-line, the immediates baked\n";
    s += "typedef unsigned int uint32_t;   // NVRTC carries no stdint; the same typedef as the kernel preamble\n";
    s += "__device__ __forceinline__ uint32_t fg2_rotl(uint32_t x, uint32_t n) { n &= 31u; return n ? ((x << n) | (x >> (32 - n))) : x; }\n";
    s += "__device__ __forceinline__ uint32_t fg2_mask(uint32_t u) { const uint32_t e = (u >> 23) & 255u; const uint32_t e2 = (e == 0u || e == 255u) ? 127u : (e < 111u ? 111u : (e > 143u ? 143u : e)); return (u & 0x807FFFFFu) | (e2 << 23); }\n";
    for (uint32_t i = 0; i < n_shapes; ++i) {
        s += "__device__ __forceinline__ void fg2_shape_" + std::to_string(i) + "(uint32_t (&R)[32], uint32_t* SP, const uint32_t (&H)[32], uint32_t l) {\n";
        for (int k = 0; k < 32; ++k) s += "    " + slot(k, sh[i].cls[k], fchain(sh[i].w[1], uint32_t(k)), fchain(sh[i].w[2], uint32_t(k)), ((sh[i].w[3] >> k) & 1u) != 0) + "\n";
        s += "}\n";
    }
    s += "__device__ __forceinline__ void fg2_dispatch(uint32_t shape, uint32_t (&R)[32], uint32_t* SP, const uint32_t (&H)[32], uint32_t l) {\n    switch (shape) {\n";
    for (uint32_t i = 0; i < n_shapes; ++i) s += "        case " + std::to_string(i) + ": fg2_shape_" + std::to_string(i) + "(R, SP, H, l); break;\n";
    s += "        default: break;\n    }\n}\n#define IH_FORGE2_COMPILED 1\n";
    delete[] sh;
    return s;
}

}  // namespace forge2
