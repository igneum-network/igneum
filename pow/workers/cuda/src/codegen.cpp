#include "codegen.hpp"
#include "embedded.hpp"

#include <array>
#include <stdexcept>
#include <utility>

namespace igneum {
namespace {

constexpr uint32_t kFnvPrime = 0x01000193u;
constexpr uint32_t kFnvOffset = 0x811c9dc5u;
constexpr uint32_t kRegs = 32;
constexpr int kCacheOps = 11;   // ProgPoW 0.9.4 (KAWPOW)
constexpr int kMathOps = 18;
constexpr int kCacheOpsEpic = 12;  // ProgPoW 0.9.2 (Epic): period 50, cache 12, math 20, dataset nodes of 256 parents, plain keccak-f800 seeding
constexpr int kMathOpsEpic = 20;

inline uint32_t fnv1a(uint32_t u, uint32_t v) { return (u ^ v) * kFnvPrime; }

struct kiss99 {
    uint32_t z, w, jsr, jcong;
    uint32_t operator()() {
        z = 36969u * (z & 0xffffu) + (z >> 16);
        w = 18000u * (w & 0xffffu) + (w >> 16);
        jcong = 69069u * jcong + 1234567u;
        jsr ^= (jsr << 17); jsr ^= (jsr >> 13); jsr ^= (jsr << 5);
        return (((z << 16) + w) ^ jcong) + jsr;
    }
};

struct rng_state {
    kiss99 rng;
    std::array<uint32_t, kRegs> dst_seq, src_seq;
    size_t dst_counter = 0, src_counter = 0;
    explicit rng_state(uint64_t number) {
        const uint32_t seed_lo = static_cast<uint32_t>(number);
        const uint32_t seed_hi = static_cast<uint32_t>(number >> 32);
        const uint32_t z = fnv1a(kFnvOffset, seed_lo);
        const uint32_t w = fnv1a(z, seed_hi);
        const uint32_t jsr = fnv1a(w, seed_lo);
        const uint32_t jcong = fnv1a(jsr, seed_hi);
        rng = kiss99{z, w, jsr, jcong};
        for (uint32_t i = 0; i < kRegs; ++i) { dst_seq[i] = i; src_seq[i] = i; }
        for (uint32_t i = kRegs; i > 1; --i) {
            std::swap(dst_seq[i - 1], dst_seq[rng() % i]);
            std::swap(src_seq[i - 1], src_seq[rng() % i]);
        }
    }
    uint32_t next_dst() { return dst_seq[(dst_counter++) % kRegs]; }
    uint32_t next_src() { return src_seq[(src_counter++) % kRegs]; }
};

std::string math_expr(const std::string& a, const std::string& b, uint32_t sel) {
    switch (sel % 11) {
        default:
        case 0: return a + " + " + b;
        case 1: return a + " * " + b;
        case 2: return "mul_hi32(" + a + ", " + b + ")";
        case 3: return "umin32(" + a + ", " + b + ")";
        case 4: return "rotl32(" + a + ", " + b + ")";
        case 5: return "rotr32(" + a + ", " + b + ")";
        case 6: return a + " & " + b;
        case 7: return a + " | " + b;
        case 8: return a + " ^ " + b;
        case 9: return "clz32(" + a + ") + clz32(" + b + ")";
        case 10: return "popcount32(" + a + ") + popcount32(" + b + ")";
    }
}

std::string merge_stmt(const std::string& a, const std::string& b, uint32_t sel) {
    const uint32_t x = (sel >> 16) % 31 + 1;
    switch (sel % 4) {
        case 0: return a + " = (" + a + " * 33u) + " + b + ";";
        case 1: return a + " = (" + a + " ^ " + b + ") * 33u;";
        case 2: return a + " = rotl32(" + a + ", " + std::to_string(x) + "u) ^ " + b + ";";
        default: return a + " = rotr32(" + a + ", " + std::to_string(x) + "u) ^ " + b + ";";
    }
}

std::string mixreg(uint32_t i) { return "mix[" + std::to_string(i) + "]"; }

}  // namespace

std::string progpow_program_source(uint64_t period_number, bool epic) {
    rng_state st(period_number);
    std::string s;
    s.reserve(4096);
    s += "        // period " + std::to_string(period_number) + (epic ? " (progpow-epic 0.9.2)" : "") + "\n";
    const int cache_ops = epic ? kCacheOpsEpic : kCacheOps, math_ops = epic ? kMathOpsEpic : kMathOps;
    const int max_ops = cache_ops > math_ops ? cache_ops : math_ops;
    for (int i = 0; i < max_ops; ++i) {
        if (i < cache_ops) {
            const uint32_t src = st.next_src();
            const uint32_t dst = st.next_dst();
            const uint32_t sel = st.rng();
            s += "        off = " + mixreg(src) + " % L1_WORDS; " + merge_stmt(mixreg(dst), "c_dag[off]", sel) + "\n";
        }
        if (i < math_ops) {
            const uint32_t src_rnd = st.rng() % (kRegs * (kRegs - 1));
            const uint32_t src1 = src_rnd % kRegs;
            uint32_t src2 = src_rnd / kRegs;
            if (src2 >= src1) ++src2;
            const uint32_t sel1 = st.rng();
            const uint32_t dst = st.next_dst();
            const uint32_t sel2 = st.rng();
            s += "        data32 = " + math_expr(mixreg(src1), mixreg(src2), sel1) + "; " + merge_stmt(mixreg(dst), "data32", sel2) + "\n";
        }
    }
    uint32_t dsts[4], sels[4];
    for (int i = 0; i < 4; ++i) {
        dsts[i] = i == 0 ? 0 : st.next_dst();
        sels[i] = st.rng();
    }
    const char* comp[4] = {"data.x", "data.y", "data.z", "data.w"};
    for (int i = 0; i < 4; ++i) s += "        " + merge_stmt(mixreg(dsts[i]), comp[i], sels[i]) + "\n";
    return s;
}

std::string device_source_fishhash() {
    // the KAWPOW source at period 0 (dag_gen, fnv1, bswap32, the keccak blocks) plus the FishHash kernel
    return device_source(0, false) + "\n" + kEmbeddedKernelsFishhash;
}

std::string device_source_igneumhash(const std::string& variant_defines) {
    return variant_defines + device_source_fishhash() + "\n" + kEmbeddedKernelsIgneumhash;
}

std::string device_source_rxe1(const std::string& defines) {
    return defines + device_source_fishhash() + "\n" + kEmbeddedKernelsIgneumhash + "\n" + kEmbeddedKernelsRxe1 + "\n" + kEmbeddedKernelsRxe3;
}

std::string device_source_autolykos() {
    std::string src;
    src += "typedef unsigned int uint32_t;\ntypedef unsigned long long uint64_t;\ntypedef unsigned char uint8_t;\n";
    src += kEmbeddedKernelsAutolykos;
    return src;
}

std::string device_source(uint64_t period_number, bool epic) {
    std::string src;
    src += "typedef unsigned int uint32_t;\ntypedef unsigned long long uint64_t;\ntypedef unsigned char uint8_t;\n";
    src += epic ? "#define DATASET_PARENTS 256\n#define PROGPOW_EPIC 1\n" : "#define DATASET_PARENTS 512\n#define PROGPOW_EPIC 0\n";
    src += "#define static __device__ static\n";
    src += "namespace k800 {\n#define ethash_keccakf800 __device__ ethash_keccakf800\n";
    src += kEmbeddedKeccakf800;
    src += "\n}\n#undef ethash_keccakf800\nnamespace k1600 {\n#define ethash_keccakf1600 __device__ ethash_keccakf1600\n";
    src += kEmbeddedKeccakf1600;
    src += "\n}\n#undef ethash_keccakf1600\n#undef static\n";
    std::string kernels = kEmbeddedKernels;
    const std::string marker = "PROGPOW_PROGRAM";
    // The marker's last occurrence is the one inside the kernel body (the file's
    // header comment names the marker too; a `find` once put the program at file scope).
    const size_t p = kernels.rfind(marker);
    if (p == std::string::npos) throw std::runtime_error("kernel source has no " + marker + " marker");
    kernels.replace(p, marker.size(), "\n" + progpow_program_source(period_number, epic));
    src += kernels;
    return src;
}

}  // namespace igneum
