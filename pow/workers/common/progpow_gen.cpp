// progpow_gen.cpp: see progpow_gen.h.
#include "progpow_gen.h"
#include <sstream>
#include <stdexcept>
#include <vector>

namespace igm {
namespace {

constexpr uint32_t FNV_OFFSET_BASIS = 0x811c9dc5u;
inline uint32_t fnv1a(uint32_t h, uint32_t d) { return (h ^ d) * 0x1000193u; }

// kiss99 as in the vendored kiss99.hpp
struct Kiss99 {
    uint32_t z, w, jsr, jcong;
    uint32_t next() {
        z = 36969u * (z & 65535u) + (z >> 16);
        w = 18000u * (w & 65535u) + (w >> 16);
        uint32_t mwc = (z << 16) + w;
        jsr ^= (jsr << 17); jsr ^= (jsr >> 13); jsr ^= (jsr << 5);
        jcong = 69069u * jcong + 1234567u;
        return (mwc ^ jcong) + jsr;
    }
};

// mix_rng_state(seed): z, w, jsr, jcong from fnv1a over (lo, hi, lo, hi), then the two
// Fisher-Yates permutations exactly as the vendored constructor orders its draws
// (dst first, then src, for i from regs down to 2).
struct ProgState {
    Kiss99 rng; std::vector<uint32_t> dst, src; uint32_t dst_cnt = 0, src_cnt = 0; uint32_t regs;
    ProgState(uint64_t seed, uint32_t r) : regs(r) {
        uint32_t lo = (uint32_t)seed, hi = (uint32_t)(seed >> 32);
        rng.z = fnv1a(FNV_OFFSET_BASIS, lo);
        rng.w = fnv1a(rng.z, hi);
        rng.jsr = fnv1a(rng.w, lo);
        rng.jcong = fnv1a(rng.jsr, hi);
        dst.resize(regs); src.resize(regs);
        for (uint32_t i = 0; i < regs; i++) { dst[i] = i; src[i] = i; }
        for (uint32_t i = regs; i > 1; i--) {
            std::swap(dst[i - 1], dst[rng.next() % i]);
            std::swap(src[i - 1], src[rng.next() % i]);
        }
    }
    uint32_t next_dst() { return dst[(dst_cnt++) % regs]; }
    uint32_t next_src() { return src[(src_cnt++) % regs]; }
};

std::string merge_code(const std::string &a, const std::string &b, uint32_t r) {
    uint32_t rot = ((r >> 16) % 31u) + 1u;
    switch (r % 4u) {
        case 0: return "((" + a + ") * 33u) + (" + b + ")";
        case 1: return "((" + a + ") ^ (" + b + ")) * 33u";
        case 2: return "rotate((" + a + "), " + std::to_string(rot) + "u) ^ (" + b + ")";
        default: return "rotate((" + a + "), " + std::to_string(32u - rot) + "u) ^ (" + b + ")";
    }
}

// random_math as in the vendored progpow.cpp; rotl32/rotr32 there mask the count to 31.
// The high-multiply is spelt mul_hi in OpenCL C and mulhi in the Metal Shading Language
// (seen on the mini: every period whose draw included it failed to compile).
std::string math_code(const std::string &a, const std::string &b, uint32_t r, Dialect d) {
    switch (r % 11u) {
        case 0:  return "(" + a + ") + (" + b + ")";
        case 1:  return "(" + a + ") * (" + b + ")";
        case 2:  return (d == Dialect::msl ? "mulhi(" : "mul_hi(") + a + ", " + b + ")";
        case 3:  return "min(" + a + ", " + b + ")";
        case 4:  return "rotate(" + a + ", (" + b + ") & 31u)";
        case 5:  return "IGM_ROTR_VAR(" + a + ", " + b + ")";   // the variable right-rotate: the preludes define it (the OpenCL one as the shift form on Intel platforms, where the compiler folds the rotate with a negated count into a left rotate; 7 October 2026)
        case 6:  return "(" + a + ") & (" + b + ")";
        case 7:  return "(" + a + ") | (" + b + ")";
        case 8:  return "(" + a + ") ^ (" + b + ")";
        case 9:  return "clz(" + a + ") + clz(" + b + ")";
        default: return "popcount(" + a + ") + popcount(" + b + ")";
    }
}

}  // namespace

std::string generate_period_program(const Candidate &c, uint64_t prog_seed, Dialect d) {
    if (c.lane_model != LaneModel::cooperative) throw std::runtime_error("not a lane-cooperative candidate");
    ProgState st(prog_seed, c.regs);
    std::ostringstream o;
    o << "// generated for prog_seed " << prog_seed << " (" << c.id << ", pin " << c.pin << ")\n";
    // The program's variable right-rotate defines itself (guarded) so a base source without the preludes' definition still
    // builds: on an Intel platform the host prefixes IGM_INTEL_ROTR_FOLD and the shift form is compiled (the Arc fold of
    // 7 October 2026); elsewhere the rotate builtin, the same text as before. The pocl proof of 11 October 2026 failed on a
    // stale base kernel ("use of undeclared identifier 'IGM_ROTR_VAR'"), which this closes by construction.
    o << "#ifndef IGM_ROTR_VAR\n";
    if (d == Dialect::msl) o << "#define IGM_ROTR_VAR(x, n) rotate((x), (32u - (((n)) & 31u)) & 31u)\n";
    else o << "#if defined(IGM_INTEL_ROTR_FOLD)\nstatic inline uint igm_rotr_var(uint x, uint n) { n &= 31u; return (x >> n) | (x << ((32u - n) & 31u)); }\n#define IGM_ROTR_VAR(x, n) igm_rotr_var((x), (n))\n#else\n#define IGM_ROTR_VAR(x, n) rotate((x), (32u - (((n)) & 31u)) & 31u)\n#endif\n";
    o << "#endif\n";
    if (d == Dialect::msl)
        o << "inline void igm_period_program(thread uint mix[IGM_MAX_REGS], threadgroup const uint *c_dag, thread const uint dag_entry[IGM_MAX_DAG_LOADS]) {\n";
    else
        o << "inline void igm_period_program(uint *mix, __local const uint *c_dag, const uint *dag_entry) {\n";
    o << "    uint data;\n";
    uint32_t max_i = c.cache_accesses > c.math_ops ? c.cache_accesses : c.math_ops;
    for (uint32_t i = 0; i < max_i; i++) {
        if (i < c.cache_accesses) {
            uint32_t s = st.next_src();
            uint32_t dst = st.next_dst();
            uint32_t sel = st.rng.next();
            std::string b = "c_dag[mix[" + std::to_string(s) + "] % IGM_CACHE_WORDS]";
            o << "    mix[" << dst << "] = " << merge_code("mix[" + std::to_string(dst) + "]", b, sel) << ";\n";
        }
        if (i < c.math_ops) {
            uint32_t src_rnd = st.rng.next() % (c.regs * (c.regs - 1));
            uint32_t s1 = src_rnd % c.regs;
            uint32_t s2 = src_rnd / c.regs;
            if (s2 >= s1) ++s2;
            uint32_t sel1 = st.rng.next();
            uint32_t dst = st.next_dst();
            uint32_t sel2 = st.rng.next();
            o << "    data = " << math_code("mix[" + std::to_string(s1) + "]", "mix[" + std::to_string(s2) + "]", sel1, d) << ";\n";
            o << "    mix[" << dst << "] = " << merge_code("mix[" + std::to_string(dst) + "]", "data", sel2) << ";\n";
        }
    }
    // DAG access pattern: dsts and sels drawn in one pass, then applied
    std::vector<uint32_t> dsts(c.dag_loads), sels(c.dag_loads);
    for (uint32_t i = 0; i < c.dag_loads; i++) { dsts[i] = i == 0 ? 0 : st.next_dst(); sels[i] = st.rng.next(); }
    for (uint32_t i = 0; i < c.dag_loads; i++)
        o << "    mix[" << dsts[i] << "] = " << merge_code("mix[" + std::to_string(dsts[i]) + "]", "dag_entry[" + std::to_string(i) + "]", sels[i]) << ";\n";
    o << "}\n";
    return o.str();
}

std::string splice_period_program(const std::string &base_source, const std::string &program) {
    const std::string marker = "// IGM_PERIOD_PROGRAM";
    size_t pos = base_source.find(marker);
    if (pos == std::string::npos) throw std::runtime_error("IGM_PERIOD_PROGRAM marker missing from the base source");
    std::string out = base_source;
    out.replace(pos, marker.size(), program);
    return out;
}

}  // namespace igm
