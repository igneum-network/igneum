// forge3_shapes.hpp: Anvil's (version 21, pow/ref namespace forge2) per-batch shapes as WHOLE-ENTRY device bodies, generated on the
// host (the GPU team, 15 October 2026; the compiled form of forge2_shapes.hpp with the entry unrolled). The batch's M shapes are drawn as the
// reference draws them (the quota by fp_mode, each shape's class order a Fisher-Yates by fchain(w0, q)); for each shape the text
// emits fg3_entry_<i>: the entry's PERWORD steps straight-line, every slot's immediates, sub-class bits and load flags baked, the
// register operands static (the document's wiring), so that
//   (a) the hash's own words H_k = fchain(hword, k) are computed once an entry and ONLY for the slots that read them (the loads and
//       the store under fp_mode 3 and 4; the cmov predicate alone under fp_mode 6);
//   (b) under fp_mode 6 (ANVIL-R, the rotating register window) the window's base is the step within the entry, so every window
//       index is a compile-time constant and the window lives in registers: no scratchpad traffic in the step;
//   (c) under fp_mode 3 and 4 the lane's 16-word scratchpad is in shared memory laid out [word][thread] (consecutive lanes on
//       consecutive banks: no bank conflict whatever the word);
//   (d) the keyed read is emitted only at the steps where it can fire.
// The dispatch fg3_dispatch(shape, R, SP, WN, hword, t0, l, ds) runs one entry. fp_mode 3, 4 and 6 are generated here; the other
// documents take the interpretive kernel.
#pragma once
#include <cstdint>
#include <cstdio>
#include <string>
#include <stdexcept>
#include <initializer_list>

namespace forge3 {

inline uint32_t rotl32(uint32_t x, uint32_t n) { n &= 31u; return n ? ((x << n) | (x >> (32u - n))) : x; }
inline uint32_t fchain(uint32_t x, uint32_t v) { x = rotl32(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
enum : uint8_t { FMA = 0, FADDSUB, FMUL, IALU, ROT, MUL, MULHI, I2F, F2I, XLANE, STORE, CMOV };
// fp_mode 1, 3, 4, 6: the integer-only quota (the reference's QUOTA_INT)
static const uint8_t QUOTA_INT[32] = { IALU, IALU, ROT, IALU, XLANE, IALU, IALU, MUL, IALU, ROT, ROT, IALU, STORE, IALU, IALU, ROT, MULHI, IALU, IALU, MUL, ROT, IALU, XLANE, XLANE, IALU, IALU, ROT, CMOV, STORE, IALU, IALU, MUL };

struct shape { uint32_t w[4]; uint8_t cls[32]; };

inline bool supported(uint32_t fp_mode) { return fp_mode == 3 || fp_mode == 4 || fp_mode == 6 || fp_mode == 9; }

// the batch's shapes from the period seed (forge2::attempt's draw, byte for byte)
inline void draw_shapes(const uint8_t pseed[32], uint32_t n, shape* out) {
    uint32_t x[8];
    for (int k = 0; k < 8; ++k) x[k] = uint32_t(pseed[4 * k]) | (uint32_t(pseed[4 * k + 1]) << 8) | (uint32_t(pseed[4 * k + 2]) << 16) | (uint32_t(pseed[4 * k + 3]) << 24);
    for (uint32_t i = 0; i < n; ++i) {
        for (int j = 0; j < 4; ++j) { const uint32_t idx = 4 * i + j; x[idx & 7] = fchain(x[idx & 7], idx); out[i].w[j] = x[idx & 7]; }
        for (int k = 0; k < 32; ++k) out[i].cls[k] = QUOTA_INT[k];
        for (int q = 31; q > 0; --q) { const uint32_t j = fchain(out[i].w[0], uint32_t(q)) % uint32_t(q + 1); const uint8_t t = out[i].cls[q]; out[i].cls[q] = out[i].cls[j]; out[i].cls[j] = t; }
    }
}

inline std::string u(uint32_t v) { char b[16]; std::snprintf(b, sizeof b, "%uu", v); return b; }
inline std::string si(int v) { return std::to_string(v); }

// the wiring: slot k writes d, reads a and b
// the reference as cut (main 8e1277fc5) selects the ILP wiring on fp_mode 3 alone, so fp_mode 6 falls through to version 20's
// a = 7k + 1, b = 11k + 5 (the fault reported to the hash team 15 Oct 02:3x UK); ilp6 = 1 generates (e) with the ILP wiring
inline void wiring(uint32_t fp_mode, int k, int& ak, int& bk, int& dk, bool ilp6) {
    if (fp_mode == 4) { dk = k; ak = (k + 24) & 31; bk = (k + 16) & 31; }
    else if (fp_mode == 3 || (fp_mode == 6 && ilp6)) { dk = k; ak = (k + 21) & 31; bk = (k + 9) & 31; }
    else { dk = k; ak = (7 * k + 1) & 31; bk = (11 * k + 5) & 31; }
}

inline int sp_shift(uint32_t stride) { int n = 0; while ((1u << n) < stride) ++n; return n; }   // log2 of the [word][thread] row stride (a power of two)
inline bool needs_h(uint32_t fp_mode, uint8_t C, bool load) {
    if (C == CMOV) return true;
    if (fp_mode == 6) return false;
    return load || C == STORE;
}

// one slot's statement at step s of the entry (s is the window base under fp_mode 6)
inline std::string slot(uint32_t fp_mode, int s, int k, uint8_t C, uint32_t imm, uint32_t q, bool load, uint32_t sp_stride, bool ilp6, bool noxl) {
    int ak, bk, dk; wiring(fp_mode, k, ak, bk, dk, ilp6);
    const std::string a = "R[" + si(ak) + "]", d = "R[" + si(dk) + "]", H = "H" + si(k);
    std::string b;
    if (load) {
        if (fp_mode == 6) b = "WN[" + si((s + int(imm & 15u)) & 15) + "]";
        else b = "fg3_lds(spb + (((R[" + si(bk) + "] + " + H + ") & 60u) << " + si(sp_shift(sp_stride)) + "))";   // the word's row: ((x & 63) >> 2) * (4 * stride) bytes = (x & 60) << log2(stride)
    } else b = "R[" + si(bk) + "]";
    switch (C) {
        case IALU: switch (q % 5u) {
            case 0: return d + " = " + a + " + " + b + " + " + u(imm) + ";";
            case 1: return d + " = " + a + " - " + b + " - " + u(imm) + ";";
            case 2: return d + " = " + a + " ^ " + b + " ^ " + u(imm) + ";";
            case 3: return d + " = (" + a + " & " + b + ") ^ " + u(imm) + ";";
            default: return d + " = (" + a + " | " + b + ") ^ " + u(imm) + ";"; }
        case ROT: { const std::string sh = "((" + u(imm) + " ^ " + b + ") & 31u)"; switch (q & 3u) {
            case 0: return d + " = fg3_rotl(" + a + ", " + sh + ");";
            case 1: return d + " = fg3_rotr(" + a + ", " + sh + ");";   // rotl(a, (32 - sh) & 31) == rotr(a, sh)
            case 2: return d + " = (" + a + " << " + sh + ") + " + b + ";";
            default: return d + " = (" + a + " >> " + sh + ") + " + b + ";"; } }
        case MUL: return d + " = " + a + " * " + b + " + " + u(imm) + ";";
        case MULHI: return d + " = __umulhi(" + a + ", " + b + ") ^ " + u(imm) + ";";
        case XLANE: if (noxl) return d + " = " + a + " + " + u(imm) + ";";   // a figure experiment (inexact): the cross-lane read as the lane's own register
            return d + " = fg3_shfl(" + a + ", (l + " + u(1u + (q % 15u)) + ") & 15u, wmask) + " + u(imm) + ";";
        case STORE:
            if (fp_mode == 6) return "WN[" + si((s + int((imm >> 4) & 15u)) & 15) + "] = " + b + ";";
            return "fg3_sts(spb + (((" + a + " + " + H + ") & 60u) << " + si(sp_shift(sp_stride)) + "), " + b + ");";
        case CMOV: return d + " = ((" + b + " >> (" + H + " & 31u)) & 1u) ? " + a + " : " + d + ";";
        default: return "/* class " + si(C) + " is not an integer-only class */";
    }
}

// the device text: fg3_rotl, fg3_rotr, fg3_fold, fg3_keyed, the M entry bodies, fg3_dispatch
//   per_entry: the steps an entry runs (rounds / sequence); fold_every: the keyed read's stride; n_items: the dataset's items;
//   apw: attempts a warp (1: lanes 16..31 idle, the shuffle mask 0xFFFF; 2: both halves, the shuffles segmented at 16)
inline std::string source(const uint8_t pseed[32], uint32_t n_shapes, uint32_t fp_mode, uint32_t per_entry, uint32_t fold_every, uint64_t n_items, uint32_t apw, uint32_t sp_stride, bool ilp6, bool noxl, uint32_t unroll, uint32_t minb, uint32_t prefetch, uint32_t pfd, bool lean, bool init8, bool pfa, bool chain_form, bool csel, bool base, uint32_t sp8_bytes = 0, bool xfold = false, bool onephase = false, uint32_t keyed_s = 0, uint32_t keyed_r = 0, uint32_t kprobe = 0) {
    shape* sh = new shape[n_shapes];
    draw_shapes(pseed, n_shapes, sh);
    std::string s;
    s += "// Anvil (version 21) batch shapes as whole-entry bodies, generated on the host from the period seed (forge3_shapes.hpp)\n";
    if (fp_mode == 6 && per_entry < 16u) unroll = per_entry;   // an entry shorter than 16 steps: the window base restarts at the entry, one block, no rotation
    if (fp_mode == 6 && (unroll == 0 || 16u % unroll || per_entry % unroll)) unroll = 16u;
    if (fp_mode != 6 && (unroll == 0 || per_entry % unroll)) unroll = per_entry;
    // the prefetch form ("prefetch" K: the item's index the designated lane's r0 as it stood K steps before the consuming step): the port
    // issues the read P steps ahead (tune pf), P a multiple of fold_every dividing the entry, at most K; no prefetch, no early issue
    const uint32_t D = (prefetch && fold_every && prefetch % fold_every == 0) ? prefetch / fold_every : 0u;   // the lanes back
    if (prefetch && (D == 0 || D > 16)) throw std::runtime_error("fg3: prefetch must be a multiple of fold_every at most 16 lanes back");
    uint32_t P = prefetch ? pfd : 0u;
    if (P) { if (P > prefetch) P = prefetch; while (P && (P % fold_every || per_entry % P)) --P; }
    if (fold_every >= per_entry) P = 0;
    if (fp_mode == 9 && P > 4u) P = 4u;   // the select-all block is 4 steps
    if (!P) pfa = false;   // the asynchronous form (cp.async into a shared ring of P reads a lane, no registers held): only under the pipeline
    const uint32_t PB = P > 3u ? P : 3u;   // the ring's slots (at least three: the init chain's 192-word window aliases the ring)
    // F-441 / F-504 (KEYED-RELEASED-VERSIONS-SPEC.md sections 2 and 9, the review lead's ruled form 17 October 2026): the bond-keyed item-read
    // steps on version 21 in the compiled form. Read r = t / fold_every is keyed iff (r x S) mod R < S (R the walk's reads), i = (r x S) div R;
    // m_0 = the work seed, m_i = eight words (lane k's r0 XOR lane (k + 8)'s r0) at the step's entry; s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i).
    // The share enters in two places and nowhere else: (a) the keyed read's INDEX WORD (the saved prefetch word, the lane's r0, or the chain-select
    // draw) XOR s_i[0] before the modulo, the read issued AT ITS OWN STEP (the pipeline's slot for a keyed step is never issued: its index is
    // unknown until the step's entry); (b) the share's eight words into the attempt's state for the mix (the shares whole after the lanes' words).
    // Nothing enters a register and nothing enters the item's words. The branch on the step is warp-uniform (t is). Unkeyed: keyed_s 0, the text as today.
    if (keyed_s && (!keyed_r || keyed_s > keyed_r)) throw std::runtime_error("fg3: the keyed-read steps S outside 1..R");
    if (keyed_s && pfa) throw std::runtime_error("fg3: the asynchronous prefetch ring carries no keyed read (pfa under features bit 10)");
    if (keyed_s && xfold) throw std::runtime_error("fg3: the spread fold's shuffles cannot sit under the keyed branch (xfold under features bit 10)");
    auto kstep = [&](uint32_t t) { return keyed_s != 0u && ((uint64_t(t / fold_every) * keyed_s) % keyed_r) < keyed_s; };
    if (keyed_s) s += "#define IH_FG3_KEYED " + u(keyed_s) + "\n";
    // kprobe (a PROBE, inexact by construction, never a row of record): 1 = the share's compression replaced by a constant word (the read
    // and the stores stand: the cost of the compression alone), 2 = the compression stands but the read's index is not XORed and the
    // pipelined issue is not skipped (the cost of the synchronous read alone); the module key carries -kp<n>
    if (keyed_s && kprobe) s += "#define IH_FG3_KPROBE " + u(kprobe) + "\n";
    // kprobe 3 = the entry function's unconditional work alone (the message gather and the index shuffles stand, the keyed branch never
    // taken: the read as the unkeyed step's): the cost of the hoisted shuffles and the branch's presence, the floor of the keyed text
    s += "#define IH_FG3 1\n#define IH_FG3_FPMODE " + u(fp_mode) + "\n#define IH_FG3_APW " + u(apw) + "\n#define IH_FG3_HOIST " + std::string(fold_every >= per_entry ? "1" : "0") + "\n#define IH_FG3_MINB " + u(minb ? minb : 1u) + "\n#define IH_FG3_PF " + u(P) + "\n#define IH_FG3_PFK " + u(prefetch) + "\n#define IH_FG2_LEAN " + u(lean ? 1u : 0u) + "\n#define IH_FG3_INIT8 " + u(init8 ? 1u : 0u) + "\n#define IH_FG3_PFA " + u(pfa ? 1u : 0u) + "\n#define IH_FG2_CHAIN " + u(chain_form ? 1u : 0u) + "\n#define IH_FG3_CSEL " + u(csel ? 1u : 0u) + "\n#define IH_FG2_BASE " + u(base ? 1u : 0u) + "\n";
    if (base) {   // version 24: the period's fixed order (a permutation by the period seed's word 7) and the shapes' own predicate words, constants of the module
        uint8_t order[256]; for (uint32_t i = 0; i < n_shapes; ++i) order[i] = uint8_t(i);
        const uint32_t w7 = uint32_t(pseed[28]) | (uint32_t(pseed[29]) << 8) | (uint32_t(pseed[30]) << 16) | (uint32_t(pseed[31]) << 24);
        for (uint32_t i = n_shapes - 1; i > 0; --i) { const uint32_t r = fchain(w7, 65536u + i) % (i + 1); const uint8_t t = order[i]; order[i] = order[r]; order[r] = t; }
        const uint32_t L = (per_entry ? (128u / per_entry) : 8u);   // the entries (the document's sequence = rounds / 16; rounds is 128 under version 24)
        s += "__device__ const unsigned int fg3_base_seq[" + u(L) + "] = {"; for (uint32_t i = 0; i < L; ++i) s += (i ? ", " : " ") + u(order[i % n_shapes]); s += " };\n";
        s += "__device__ const unsigned int fg3_base_hw[" + u(L) + "] = {"; for (uint32_t i = 0; i < L; ++i) s += (i ? ", " : " ") + u(fchain(sh[order[i % n_shapes]].w[2], 4096u + i)); s += " };\n";
    }
    {   // the period seed as the host derived it (the chain form's prep kernel reads it; the batch form recomputes it from the nonce)
        s += "__device__ const unsigned int fg3_pseed[8] = {";
        for (int k = 0; k < 8; ++k) s += (k ? ", " : " ") + u(uint32_t(pseed[4 * k]) | (uint32_t(pseed[4 * k + 1]) << 8) | (uint32_t(pseed[4 * k + 2]) << 16) | (uint32_t(pseed[4 * k + 3]) << 24));
        s += " };\n";
    }
    s += "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n";
    s += "struct fg3_kx { uint32_t h; uint4 pf[" + u((P && !pfa) ? P : 1u) + "][4]; };   // the prefetch state: the lane's saved r0 and the reads in flight (registers)\n";
    if (!sp8_bytes || pfa) s += "__shared__ uint4 fg3_pfb[2][" + u(PB) + "][4][4];   // the asynchronous ring a half: P slots of the four consuming lanes' 64 bytes (one warp a block); the init chain's window aliases it\n";
    if (fp_mode != 6) {
        s += "__shared__ uint32_t fg3_sp[16][" + u(sp_stride) + "];   // the lane's 16-word scratchpad as [word][thread]: consecutive lanes on consecutive banks\n";
        // the shared window's 32-bit address, read and written by ld.shared / st.shared (a generic pointer costs a 64-bit add a load)
        s += "__device__ __forceinline__ uint32_t fg3_lds(uint32_t a) { uint32_t v; asm volatile(\"ld.shared.u32 %0, [%1];\" : \"=r\"(v) : \"r\"(a) : \"memory\"); return v; }\n";
        s += "__device__ __forceinline__ void fg3_sts(uint32_t a, uint32_t v) { asm volatile(\"st.shared.u32 [%0], %1;\" : : \"r\"(a), \"r\"(v) : \"memory\"); }\n";
    }
    s += "__device__ __forceinline__ uint32_t fg3_rotl(uint32_t x, uint32_t n) { return __funnelshift_l(x, x, n); }\n";
    // the cross-lane read as the bare shfl.sync (width 16: the segment mask 0x101f); every lane of the member mask runs the same body
    // under apw 1 (the upper half in duplicate); under apw 2 each half names its own mask and the halves may run different bodies
    s += "__device__ __forceinline__ uint32_t fg3_shfl(uint32_t v, uint32_t src, uint32_t mask) { uint32_t r; asm volatile(\"shfl.sync.idx.b32 %0, %1, %2, 0x101f, %3;\" : \"=r\"(r) : \"r\"(v), \"r\"(src), \"r\"(mask)); return r; }\n";
    s += "__device__ __forceinline__ uint32_t fg3_rotr(uint32_t x, uint32_t n) { return __funnelshift_r(x, x, n); }\n";
    s += "__device__ __forceinline__ uint32_t fg3_fold(uint32_t x, uint32_t v) { x = fg3_rotl(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }\n";
    // the keyed read at step t: lane Ln = (t / fold_every) & 15 names the item (by its r0, or under prefetch by its r0 saved K steps
    // before); lanes Ln .. Ln+3 take 16 words each into R[0..15]. Under the pipeline (P > 0) the read for step t + P is issued at
    // step t into pf[slot] and folded at step t + P; the lane saves its r0 when the designated lane is D lanes back from it.
    // THE SPREAD FOLD (the GPU team, 15 Oct 2026; tune xfold=1, OFF by default: on the 4090 the 32 shuffles a read cost more than the 192 folds they
    // replace, version 21's k2-lean-p64 fell from 34 to 11.6 MH/s and version 25 gained nothing): the keyed read's 64 folds (lanes Ln..Ln+3, 16 registers each) were computed by
    // every lane and kept by four (192 instructions a read for 64 useful folds); now lane l with off = (l - Ln) & 15 folds the four words
    // k = (off >> 2) + 4i of lane T = Ln + (off & 3): the register in by one shuffle a k (kept where (off >> 2) == (k & 3)), the four folds,
    // the result back by one shuffle a k (kept by the four target lanes); the same folds, the same values, 16 lanes busy instead of 4
    s += "__device__ __forceinline__ uint32_t fg3_sel4(const uint4& v, uint32_t i) { return i == 0u ? v.x : i == 1u ? v.y : i == 2u ? v.z : v.w; }\n";
    s += "__device__ __forceinline__ void fg3_fold16x(uint32_t (&R)[32], const uint4 (&v)[4], uint32_t off, uint32_t Ln, uint32_t wmask) {\n";
    s += "    const uint32_t q = off & 3u, ki = off >> 2;   // the target lane's offset and this lane's register residue\n";
    s += "    const uint32_t w0 = fg3_sel4(v[0], ki), w1 = fg3_sel4(v[1], ki), w2 = fg3_sel4(v[2], ki), w3 = fg3_sel4(v[3], ki);   // the quarter's words ki, ki + 4, ki + 8, ki + 12\n";
    s += "    const uint32_t T = (Ln + q) & 15u; uint32_t r0 = 0u, r1 = 0u, r2 = 0u, r3 = 0u;\n";
    for (int k = 0; k < 16; ++k) s += "    { const uint32_t x = fg3_shfl(R[" + si(k) + "], T, wmask); " + std::string(k < 4 ? "r0" : k < 8 ? "r1" : k < 12 ? "r2" : "r3") + " = (ki == " + u(uint32_t(k & 3)) + ") ? x : " + std::string(k < 4 ? "r0" : k < 8 ? "r1" : k < 12 ? "r2" : "r3") + "; }\n";
    s += "    const uint32_t f0 = fg3_fold(r0, w0), f1 = fg3_fold(r1, w1), f2 = fg3_fold(r2, w2), f3 = fg3_fold(r3, w3);\n";
    s += "    const bool take = off < 4u;\n";
    for (int k = 0; k < 16; ++k) s += "    { const uint32_t x = fg3_shfl(" + std::string(k < 4 ? "f0" : k < 8 ? "f1" : k < 12 ? "f2" : "f3") + ", (Ln + " + u(uint32_t(4 * (k & 3))) + " + q) & 15u, wmask); R[" + si(k) + "] = take ? x : R[" + si(k) + "]; }\n";
    s += "}\n";
    s += "__device__ __forceinline__ void fg3_fold16(uint32_t (&R)[32], const uint4 (&v)[4], bool take) {\n";
    s += "    const uint32_t w[16] = { v[0].x, v[0].y, v[0].z, v[0].w, v[1].x, v[1].y, v[1].z, v[1].w, v[2].x, v[2].y, v[2].z, v[2].w, v[3].x, v[3].y, v[3].z, v[3].w };\n";
    s += "    #pragma unroll\n    for (int k = 0; k < 16; ++k) { const uint32_t f = fg3_fold(R[k], w[k]); R[k] = take ? f : R[k]; }\n}\n";
    s += "__device__ __forceinline__ void fg3_issue(uint32_t r0, uint32_t Ln, uint32_t l, const uint4* __restrict__ ds, uint4 (&v)[4]) {\n";
    s += "    const uint64_t idx = (uint64_t)r0 % " + std::to_string((unsigned long long)n_items) + "ull;\n";
    s += "    const uint32_t off = (l - Ln) & 15u;\n";
    // every lane loads (lanes 4..15 the same sectors as lanes 0..3: one fetch a warp instruction) and the fold is predicated: a branch here
    // left the compiler unable to prove convergence at the next shuffle, which then cost a MATCH, REDUX, VOTEU and a CALL a keyed read
    s += "    const uint4* it = ds + idx * 16u + 4u * (off & 3u); v[0] = __ldg(it); v[1] = __ldg(it + 1); v[2] = __ldg(it + 2); v[3] = __ldg(it + 3);\n}\n";
    if (pfa) {
        s += "__device__ __forceinline__ void fg3_issue_async(uint32_t r0, uint32_t Ln, uint32_t l, const uint4* __restrict__ ds, uint4* slotp) {\n";
        s += "    const uint64_t idx = (uint64_t)r0 % " + std::to_string((unsigned long long)n_items) + "ull;\n";
        s += "    const uint32_t off = (l - Ln) & 15u;\n";
        s += "    { const uint4* it = ds + idx * 16u + 4u * (off & 3u); const uint32_t sa = (uint32_t)__cvta_generic_to_shared(slotp + 4u * (off & 3u));\n";
        s += "        asm volatile(\"cp.async.cg.shared.global [%0], [%1], 16;\" :: \"r\"(sa), \"l\"(it)); asm volatile(\"cp.async.cg.shared.global [%0], [%1], 16;\" :: \"r\"(sa + 16u), \"l\"(it + 1));\n";
        s += "        asm volatile(\"cp.async.cg.shared.global [%0], [%1], 16;\" :: \"r\"(sa + 32u), \"l\"(it + 2)); asm volatile(\"cp.async.cg.shared.global [%0], [%1], 16;\" :: \"r\"(sa + 48u), \"l\"(it + 3)); }\n";
        s += "    asm volatile(\"cp.async.commit_group;\");\n}\n";
    }
    if (keyed_s) {
        s += "__device__ __forceinline__ void b3_compress_ctr(const uint32_t cv[8], const uint32_t m_in[16], uint64_t counter, uint32_t block_len, uint32_t flags, uint32_t out16[16]);   // the kernel text's one BLAKE3 compression (defined after this text)\n";
        s += "__device__ __forceinline__ bool fg3_keyed_step(uint32_t t) { return (((t / " + u(fold_every) + ") * " + u(keyed_s) + ") % " + u(keyed_r) + ") < " + u(keyed_s) + "; }   // read r = t / fold_every keyed iff (r x S) mod R < S (warp-uniform)\n";
        s += "__device__ __forceinline__ uint32_t fg3_keyed_index(uint32_t t) { return ((t / " + u(fold_every) + ") * " + u(keyed_s) + ") / " + u(keyed_r) + "; }   // i = (r x S) div R\n";
        // s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i): one 40-byte block, CHUNK_START | CHUNK_END | ROOT | KEYED_HASH = 1 | 2 | 8 | 16, counter 0 (the reference's ignh_bond_share)
        // s_i = BLAKE3_keyed(sk, "bond" || LE32(i) || m_i): one 40-byte block, CHUNK_START | CHUNK_END | ROOT | KEYED_HASH = 1 | 2 | 8 | 16, counter 0 (the reference's ignh_bond_share).
        // Two copies: inlined at the entry's one site (an out-of-line call there cost the kernel 24 registers and a 96-byte frame through the ABI: 152 against 128),
        // out of line for the mid-entry sites (a document whose S keys a mid-entry position; the registered S = 4 keys none)
        for (int copy = 0; copy < 2; ++copy) {
            s += std::string(copy ? "__device__ __noinline__ void fg3_bond_share" : "__device__ __forceinline__ void fg3_bond_share_inl") + "(const uint32_t* __restrict__ sk, uint32_t i, const uint32_t* __restrict__ m, uint32_t* __restrict__ sh) {\n";
            s += "    uint32_t kk[8], km[16], ko[16];\n    #pragma unroll\n    for (int k = 0; k < 8; ++k) kk[k] = sk[k];\n";
            s += "    km[0] = 0x646E6F62u; km[1] = i;\n    #pragma unroll\n    for (int k = 0; k < 8; ++k) km[2 + k] = m[k];\n    #pragma unroll\n    for (int k = 10; k < 16; ++k) km[k] = 0u;\n";
            s += "    b3_compress_ctr(kk, km, 0ull, 40u, 27u, ko);\n    #pragma unroll\n    for (int k = 0; k < 8; ++k) sh[k] = ko[k];\n}\n";
        }
    }
    if (keyed_s) {
        // THE SHARE ACROSS FOUR LANES (the 4090 probe of 17 October: the serial compression in every lane was 4.4 of the 7.6 ms a launch the
        // keyed search carried over the twin's). BLAKE3's round is four column G's then four diagonal G's, each independent: lane j of a quad
        // (j = l & 3; the four quads of a half run the same words) holds column j (v[j], v[4 + j], v[8 + j], v[12 + j]); the diagonal phase
        // borrows b, c and d from lanes j + 1, j + 2, j + 3 of the quad and hands them back after (six shuffles a round); the message words
        // a lane takes each round are the permuted schedule's, baked as a select on j; the output's eight words (v[k] ^ v[k + 8]) lie two a
        // lane and reach every lane by eight shuffles. About 350 instructions a share against the serial 800; the same bytes.
        static const int sigma[7][16] = {
            {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
            {2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8},
            {3, 4, 10, 12, 13, 2, 7, 14, 6, 5, 9, 0, 11, 15, 8, 1},
            {10, 7, 12, 9, 14, 3, 13, 15, 4, 0, 11, 2, 5, 8, 1, 6},
            {12, 13, 9, 11, 15, 10, 14, 8, 7, 2, 5, 3, 0, 1, 6, 4},
            {9, 14, 11, 5, 8, 12, 15, 1, 13, 3, 0, 10, 2, 6, 4, 7},
            {11, 15, 5, 0, 1, 9, 8, 6, 14, 10, 2, 12, 3, 4, 7, 13}};
        auto msel = [&](int r, int base) {   // the word m[sigma[r][base + 2 j]] for this lane's j, as a select chain
            std::string e;
            for (int j = 0; j < 4; ++j) { const int idx = sigma[r][base + 2 * j]; const std::string w = idx == 0 ? "0x646E6F62u" : idx == 1 ? "i" : idx <= 9 ? "m[" + si(idx - 2) + "]" : "0u"; e += (j < 3) ? ("j == " + u(uint32_t(j)) + " ? " + w + " : ") : w; }
            return e;
        };
        s += "__device__ __forceinline__ void fg3_bond_share4(const uint32_t* __restrict__ sk, uint32_t i, const uint32_t* __restrict__ m, uint32_t* __restrict__ sh, uint32_t l, uint32_t wmask) {\n";
        s += "    const uint32_t j = l & 3u, q = l & 12u;\n";
        s += "    uint32_t a = sk[j], b = sk[4u + j], c = j == 0u ? 0x6A09E667u : j == 1u ? 0xBB67AE85u : j == 2u ? 0x3C6EF372u : 0xA54FF53Au, d = j == 2u ? 40u : j == 3u ? 27u : 0u;\n";
        s += "    #define FG3_G(mx, my) { a += b + (mx); d = fg3_rotr(d ^ a, 16u); c += d; b = fg3_rotr(b ^ c, 12u); a += b + (my); d = fg3_rotr(d ^ a, 8u); c += d; b = fg3_rotr(b ^ c, 7u); }\n";
        for (int r = 0; r < 7; ++r) {
            s += "    { const uint32_t mx = " + msel(r, 0) + ", my = " + msel(r, 1) + "; FG3_G(mx, my) }\n";
            s += "    b = fg3_shfl(b, q | ((j + 1u) & 3u), wmask); c = fg3_shfl(c, q | ((j + 2u) & 3u), wmask); d = fg3_shfl(d, q | ((j + 3u) & 3u), wmask);\n";
            s += "    { const uint32_t mx = " + msel(r, 8) + ", my = " + msel(r, 9) + "; FG3_G(mx, my) }\n";
            s += "    b = fg3_shfl(b, q | ((j + 3u) & 3u), wmask); c = fg3_shfl(c, q | ((j + 2u) & 3u), wmask); d = fg3_shfl(d, q | ((j + 1u) & 3u), wmask);\n";
        }
        s += "    #undef FG3_G\n";
        s += "    const uint32_t lo = a ^ c, hi = b ^ d;\n";
        s += "    #pragma unroll\n    for (int k = 0; k < 4; ++k) { sh[k] = fg3_shfl(lo, q | (uint32_t)k, wmask); sh[4 + k] = fg3_shfl(hi, q | (uint32_t)k, wmask); }\n}\n";
    }
    // THE READ STEP'S TEXT. Under the keyed document (F-441, F-504's ruled form; the 4090 pod's rate finding of 17 October (0.36) the rate cure) three functions carry it:
    //   fg3_keyed       the unkeyed read step as today (keyed off: byte-identical); keyed on: the same text with a `live` predicate on
    //                   the save and the fold (false at the entry's first step, whose read fg3_keyed_entry has taken) and the pipelined
    //                   issue skipping a keyed target step (its index is the share's);
    //   fg3_keyed_k     the read step that MAY be keyed (a mid-entry position the document's S can key: every shuffle of the step
    //                   before the branch, under it only ALU, the share's stores and the read; emitted only at the sites that need it);
    //   fg3_keyed_entry the entry's first read, run by the kernel's entry loop before the body (one branch an entry, eight a hash on the
    //                   registered document, whose keyed reads all fall on an entry's first step: the bodies then carry no keyed branch).
    // The first text (fa63ce914) put the branch and its collectives in every body site: a MATCH/REDUX/VOTEU/BRA.CONV reconvergence before the
    // shuffles that follow any branch ptxas cannot prove warp-uniform, 191 against 48 static, and the 4090 pod read 0.36 of the unkeyed rate.
    auto emit_read = [&](const std::string& name, bool may_key, bool entry) {
        const std::string live = "";   // the live predicate withdrawn: the entry function folds nothing (the 2060 split of 17 October: the duplicated fold at the body's first site was the keyed text's floor)
        const std::string kp = may_key ? ", uint32_t* __restrict__ ks_, const uint32_t* __restrict__ s8_, const uint32_t* __restrict__ sk_" : "";   // the attempt's shares in the state, the work seed, the secret's eight words (the kernel text names the offsets)
        s += "__device__ __forceinline__ void " + name + "(uint32_t (&R)[32], uint32_t l, uint32_t t, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, int slot, uint32_t half, uint32_t xsel" + live + kp + ") {\n";
        s += "    const uint32_t Ln = (t / " + u(fold_every) + ") & 15u;\n";
        const std::string lv = "";
        if (prefetch && !entry) s += "    if (l == ((Ln - " + u(D) + ") & 15u)) kx.h = R[0];   // the save: this lane's r0 at the top of this step, read " + u(prefetch) + " steps on\n";
        const std::string take = std::string("off < 4u");
        if (may_key) {
            s += "    const uint32_t kxr = R[0] ^ fg3_shfl(R[0], l ^ 8u, wmask);\n    uint32_t km8[8];\n    #pragma unroll\n    for (int k = 0; k < 8; ++k) km8[k] = fg3_shfl(kxr, (uint32_t)k, wmask);   // m_i: lane k's r0 XOR lane (k + 8)'s, every lane holding the eight\n";
            if (csel) s += "    const uint32_t kr0 = fg3_fold(xsel, 8192u + t);   // chain_select: the entry chain's draw\n";
            else s += "    const uint32_t kr0 = fg3_shfl(" + std::string(prefetch ? "kx.h" : "R[0]") + ", Ln, wmask);   // the step's index word\n";
            if (P && !entry) s += "    const uint32_t tn = t + " + u(P) + ", Ln2 = (tn / " + u(fold_every) + ") & 15u; const uint32_t kr0n = fg3_shfl(kx.h, Ln2, wmask);   // the next read's index word, shuffled before the branch\n";
            s += "    if (" + std::string(kprobe == 3 ? "false && " : "") + "fg3_keyed_step(t)) {\n";
            s += "        const uint32_t ki = fg3_keyed_index(t);\n        uint32_t sh[8];\n";
            s += "        #pragma unroll\n        for (int k = 0; k < 8; ++k) km8[k] = ki == 0u ? s8_[k] : km8[k];   // m_0 the work seed\n";
            if (kprobe == 1) s += "        #pragma unroll\n        for (int k = 0; k < 8; ++k) sh[k] = 0x9E3779B9u ^ km8[k] ^ sk_[0];   // PROBE 1: no compression\n";
            else if (entry) s += "        fg3_bond_share4(sk_, ki, km8, sh, l, wmask);   // the share across the quad's four lanes\n";
            else s += "        fg3_bond_share(sk_, ki, km8, sh);   // the secret's eight words where the version 8 kernel reads them (the custody guard's offset)\n";
            s += "        const uint32_t kw = l == 0u ? sh[0] : l == 1u ? sh[1] : l == 2u ? sh[2] : l == 3u ? sh[3] : l == 4u ? sh[4] : l == 5u ? sh[5] : l == 6u ? sh[6] : sh[7];\n";
            s += "        if (l < 8u) ks_[8u * ki + l] = kw;   // the share's words into the state by lanes 0..7 (a duplicate half stores the same bytes to the same words)\n";
            if (kprobe == 2) {
                // PROBE 2: the share computed and stored, the read as the unkeyed step's (the pipelined slot, issued at the previous step)
                s += "        (void)kr0; (void)sh;\n";
                if (P) s += "        { const uint32_t off = (l - Ln) & 15u; fg3_fold16(R, kx.pf[slot], off < 4u); }\n";
                else s += "        { uint4 v[4]; fg3_issue(kr0, Ln, l, ds, v); const uint32_t off = (l - Ln) & 15u; fg3_fold16(R, v, off < 4u); }\n";
            } else {
                s += "        const uint32_t r0 = kr0 ^ sh[0];   // the index word, the share's choice\n";
                if (entry && P) s += "        fg3_issue(r0, Ln, l, ds, kx.pf[slot]);   // into the pipeline's slot: the body's first site folds it as the twin folds its slot (the latency exposed, nothing duplicated)\n";
                else s += "        uint4 v[4]; fg3_issue(r0, Ln, l, ds, v); { const uint32_t off = (l - Ln) & 15u; fg3_fold16(R, v, off < 4u); }\n";
            }
            s += "    }" + std::string((entry && P) ? "\n" : " else {\n");
        }
        if (P && pfa) {
            s += "    uint4* slotp = &fg3_pfb[half][(t / " + u(fold_every) + ") % " + u(P) + "][0][0];\n";
            s += "    asm volatile(\"cp.async.wait_group " + std::to_string(P / fold_every - 1) + ";\" ::: \"memory\");\n";   // one group a keyed step: P / fold_every groups ahead of the consumed one
            s += "    { const uint32_t off = (l - Ln) & 15u; { uint4 v[4]; v[0] = slotp[4u * (off & 3u)]; v[1] = slotp[4u * (off & 3u) + 1u]; v[2] = slotp[4u * (off & 3u) + 2u]; v[3] = slotp[4u * (off & 3u) + 3u]; fg3_fold16(R, v, off < 4u); } }\n";
            s += "    const uint32_t tn = t + " + u(P) + ";\n";
            s += "    { const uint32_t Ln2 = (tn / " + u(fold_every) + ") & 15u; const uint32_t r0 = fg3_shfl(kx.h, Ln2, wmask); if (tn < (uint32_t)IH_ROUNDS) fg3_issue_async(r0, Ln2, l, ds, slotp); else asm volatile(\"cp.async.commit_group;\"); }\n";
        } else if (P && entry) {
            s += "    // the body's first site folds the slot and issues the next read\n";
        } else if (P) {
            if (xfold) s += "    { const uint32_t off = (l - Ln) & 15u; fg3_fold16x(R, kx.pf[slot], off, Ln, wmask); }\n";
            else s += "    { const uint32_t off = (l - Ln) & 15u; fg3_fold16(R, kx.pf[slot], " + take + "); }\n";
            if (!may_key) s += "    const uint32_t tn = t + " + u(P) + ";\n";
            // the shuffle outside the (uniform, but unprovable) step bound: a collective under a condition the compiler cannot prove warp-uniform
            // cost a MATCH, REDUX, VOTEU and a CALL (sm_89) or a WARPSYNC and ENDCOLLECTIVE (sm_120) a keyed read
            const std::string skip = (kprobe == 2 || kprobe == 3) ? "" : " && !fg3_keyed_step(tn)";
            if (may_key) s += "    }\n    if (tn < (uint32_t)IH_ROUNDS" + skip + ") fg3_issue(kr0n, Ln2, l, ds, kx.pf[slot]);   // a keyed target step takes no early read: its index is the share's\n";
            else s += "    { const uint32_t Ln2 = (tn / " + u(fold_every) + ") & 15u; const uint32_t r0 = fg3_shfl(kx.h, Ln2, wmask); if (tn < (uint32_t)IH_ROUNDS" + std::string(keyed_s ? skip : "") + ") fg3_issue(r0, Ln2, l, ds, kx.pf[slot]); }\n";
        } else {
            if (may_key) s += "    const uint32_t r0 = kr0;\n";
            else if (csel) s += "    const uint32_t r0 = fg3_fold(xsel, 8192u + t);   // chain_select: the read's index from the entry chain, warp-uniform\n";
            else s += "    const uint32_t r0 = fg3_shfl(" + std::string(prefetch ? "kx.h" : "R[0]") + ", Ln, wmask);\n";
            if (xfold) s += "    uint4 v[4]; fg3_issue(r0, Ln, l, ds, v); { const uint32_t off = (l - Ln) & 15u; fg3_fold16x(R, v, off, Ln, wmask); }\n";
            else s += "    uint4 v[4]; fg3_issue(r0, Ln, l, ds, v); { const uint32_t off = (l - Ln) & 15u; fg3_fold16(R, v, " + take + "); }\n";
            if (may_key) s += "    }\n";
        }
        s += "    (void)slot; (void)half; (void)xsel;\n}\n";
    };
    emit_read("fg3_keyed", false, false);
    // the sites that may be keyed: the read positions within an entry that the document's S keys at some entry, position 0 apart
    bool mid_keyed[64] = {false}; const uint32_t rpe = fold_every ? per_entry / fold_every : 0u;
    if (keyed_s) {
        if (!rpe || (per_entry % fold_every)) throw std::runtime_error("fg3: the compiled keyed form wants the entry a multiple of fold_every (the entry's first step a read)");
        if (rpe > 64u) throw std::runtime_error("fg3: the compiled keyed form wants at most 64 reads an entry");
        for (uint32_t r = 0; r < keyed_r; ++r) if (kstep(r * fold_every) && (r % rpe) != 0u) mid_keyed[r % rpe] = true;
        emit_read("fg3_keyed_k", true, false);
        emit_read("fg3_keyed_entry", true, true);
        s += "#define IH_FG3_KEYED_ENTRY 1\n";   // the kernel's entry loop runs the entry's first read through fg3_keyed_entry before the body
    }
    // a body site: the read at step st (static within the block) of block blk; under the keyed document the function that may key (only
    // where the document's S can key that position) and the live predicate (false at the entry's first step, blk 0 st 0, whose read the
    // kernel's entry loop has taken through fg3_keyed_entry)
    auto site = [&](uint32_t U_, uint32_t st, const std::string& t_expr, int slot_v, const std::string& xsel_expr) {
        std::string call = "fg3_keyed", live;
        if (keyed_s) {
            bool m = false; for (uint32_t b = 0; b * U_ + st < per_entry; ++b) { const uint32_t p2 = (b * U_ + st) / fold_every; if (p2 != 0u && p2 < 64u && mid_keyed[p2]) m = true; }
            if (m) call = "fg3_keyed_k";
            live = "";
        }
        return "    " + call + "(R, l, " + t_expr + ", ds, wmask, kx, " + si(slot_v) + ", half, " + xsel_expr + live + (call == "fg3_keyed_k" ? ", ks_, s8_, sk_" : "") + ");\n";
    };
    // the prologue: the saved r0 is the init's; the first P steps' reads issued ahead
    s += "__device__ __forceinline__ void fg3_prologue(uint32_t (&R)[32], uint32_t l, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, uint32_t half) {\n    kx.h = R[0];\n";
    for (uint32_t st = 0; st < P; ++st) if ((st % fold_every) == 0 && (kprobe == 2 || kprobe == 3 || !kstep(st))) {
        if (pfa) s += "    { const uint32_t Ln2 = " + u((st / fold_every) & 15u) + "; const uint32_t r0 = fg3_shfl(kx.h, Ln2, wmask); fg3_issue_async(r0, Ln2, l, ds, &fg3_pfb[half][" + u((st / fold_every) % P) + "][0][0]); }\n";
        else s += "    { const uint32_t Ln2 = " + u((st / fold_every) & 15u) + "; const uint32_t r0 = fg3_shfl(kx.h, Ln2, wmask); fg3_issue(r0, Ln2, l, ds, kx.pf[" + u(st % P) + "]); }\n";
    }
    s += "    (void)R; (void)l; (void)ds; (void)wmask; (void)kx; (void)half;\n}\n";
    if (sp8_bytes) {
        // VERSION 25, ANVIL FOR OLD CARDS (pow/ref namespace oldcard, main 6f75d586c; the GPU team's port, 15 October 2026): the steps t with
        // t mod 5 != 4 are SCRATCH steps into the hash's scratchpad of S bytes in shared memory (lane l on R[(4t + 0..3) mod 32]: the read
        // of 8 bytes at (r0 ^ r1 x 0x9E3779B9) mod S, r2 = fold(r2 ^ lo, t), r3 = (r3 + hi) ^ rotl(lo, 13), the write of r2 || r3 into the
        // lane's own slice at l x S/16 + (r2 ^ r3 x 0x85EBCA6B) mod S/16); the other steps the shape's. The hash's 128 steps run as
        // 13 iterations of 10 (the last two steps of the last iteration do not exist): the shape steps at steps 4 and 9 of the iteration
        // (t mod 5 = 4), whose window base (t mod 16) is static after the window is rotated by 10 an iteration; the scratch steps' register
        // group (t mod 8) is static inside a 4-way uniform switch on the iteration mod 4 (10 = 2 mod 8); the keyed read at every even step.
        // THE LAYOUT of the pad in shared memory (the port's own, the reference's bytes under a bijection of its 8-byte cells): cell c =
        // slice x W + w (W = S/128 cells a slice) lives at byte (w << 7) + (slice << 3), so a lane's own slice sits on its own 8-byte bank
        // pair: the lane-partitioned writes and the final fold are conflict-free, the data-dependent reads are what they are.
        if (fold_every != 2u || per_entry != 16u || (P && P != 2u && P != 4u && P != 8u) || fp_mode != 6u) throw std::runtime_error("fg3: version 25 wants fold_every 2, 16-step entries, fp_mode 6 and pf 0, 2, 4 or 8");
        const uint32_t NC = sp8_bytes / 8u, W = NC / 16u; const int logW = sp_shift(W);
        s += "#define IH_FG3_SP8 " + u(sp8_bytes) + "\n#define IH_FG3_SP8_W " + u(W) + "\n";
        s += "#ifndef IH_FG3_STOP\n#define IH_FG3_STOP 1024\n#endif\n";
        s += "#ifndef IH_FG3_SCR\n#define IH_FG3_SCR 0\n#endif\n";   // a debugging form (tune stop=N): the steps from N on are skipped
        s += "__shared__ __align__(16) uint32_t fg3_sp8[2][" + u(sp8_bytes / 4u) + "];   // the two attempts' scratchpads (one warp a block)\n";
        s += "__device__ __forceinline__ uint2 fg3_lds64(uint32_t a) { uint2 v; asm volatile(\"ld.shared.v2.u32 {%0, %1}, [%2];\" : \"=r\"(v.x), \"=r\"(v.y) : \"r\"(a) : \"memory\"); return v; }\n";
        s += "__device__ __forceinline__ void fg3_sts64(uint32_t a, uint32_t x, uint32_t y) { asm volatile(\"st.shared.v2.u32 [%0], {%1, %2};\" : : \"r\"(a), \"r\"(x), \"r\"(y) : \"memory\"); }\n";
        s += "__device__ __forceinline__ void fg3_sts32(uint32_t a, uint32_t x) { asm volatile(\"st.shared.u32 [%0], %1;\" : : \"r\"(a), \"r\"(x) : \"memory\"); }\n";
        s += "__device__ __forceinline__ uint32_t fg3_sp8_phys(uint32_t c) { return ((c & " + u(W - 1u) + ") << 7) | ((c >> " + si(logW) + ") << 3); }   // the cell's byte in the pad\n";
        auto scratch = [&](uint32_t g, const std::string& t) {
            const std::string r0 = "R[" + si(int(4u * g)) + "]", r1 = "R[" + si(int(4u * g + 1u)) + "]", r2 = "R[" + si(int(4u * g + 2u)) + "]", r3 = "R[" + si(int(4u * g + 3u)) + "]";
            // IH_FG3_SCR (tune scr, a PROBE, inexact, never a row): 1 the read from the lane's own slice (conflict-free, the same bytes moved),
            // 2 the read from registers (no ld.shared), 3 no read and no write (the pad still allocated: the same occupancy)
            return "        { const uint32_t c = ((" + r0 + " ^ (" + r1 + " * 0x9E3779B9u)) >> 3) & " + u(NC - 1u) + ";\n"
                   "#if IH_FG3_SCR == 1\n          const uint2 v = fg3_lds64(spb + ((c & " + u(W - 1u) + ") << 7) + (l << 3));\n"
                   "#elif IH_FG3_SCR == 2\n          uint2 v; v.x = " + r0 + " ^ c; v.y = " + r1 + " + c;\n"
                   "#elif IH_FG3_SCR == 3\n          uint2 v; v.x = " + r0 + " ^ c; v.y = " + r1 + " + c;\n"
                   "#else\n          const uint2 v = fg3_lds64(spb + fg3_sp8_phys(c));\n#endif\n          "
                   + r2 + " = fg3_fold(" + r2 + " ^ v.x, " + t + "); " + r3 + " = (" + r3 + " + v.y) ^ fg3_rotl(v.x, 13u); "
                   "const uint32_t w = ((" + r2 + " ^ (" + r3 + " * 0x85EBCA6Bu)) >> 3) & " + u(W - 1u) + ";\n#if IH_FG3_SCR != 3\n          fg3_sts64(spb + (w << 7) + (l << 3), " + r2 + ", " + r3 + ");\n#else\n          (void)w;\n#endif\n        }\n";
        };
        // the keyed read at step t = 10 it + j: its pipeline slot is t mod P, static inside the phase case (it = p mod 4, so 10 it = 2 p mod 8)
        auto keyed = [&](const std::string& t, uint32_t p, uint32_t j) { return "        fg3_keyed(R, l, " + t + ", ds, wmask, kx, " + si(int(P ? (2u * p + j) % P : 0u)) + ", half, 0u);\n"; };
        auto site = [&](int b, const std::string& t) { return "        { const uint32_t ei = (" + t + ") >> 4; const uint32_t wi = (ei < 4u ? (sq0 >> (8u * ei)) : (sq1 >> (8u * (ei - 4u)))) & 255u; fg3_site" + si(b) + "(wi, R, WN, hw[ei], l, wmask); }\n"; };
        // the shape step bodies at the two sites (window base 4 and 9 after the rotation)
        for (uint32_t i = 0; i < n_shapes; ++i) for (int b = 4; b <= 9; b += 5) {
            s += "__device__ __forceinline__ void fg3_step_" + si(int(i)) + "_" + si(b) + "(uint32_t (&R)[32], uint32_t (&WN)[16], uint32_t hword, uint32_t l, uint32_t wmask) {\n";
            for (int k = 0; k < 32; ++k) if (needs_h(fp_mode, sh[i].cls[k], ((sh[i].w[3] >> k) & 1u) != 0)) s += "    const uint32_t H" + si(k) + " = fg3_fold(hword, " + u(uint32_t(k)) + ");\n";
            for (int k = 0; k < 32; ++k) s += "    " + slot(fp_mode, b, k, sh[i].cls[k], fchain(sh[i].w[1], uint32_t(k)), fchain(sh[i].w[2], uint32_t(k)), ((sh[i].w[3] >> k) & 1u) != 0, sp_stride, ilp6, noxl) + "\n";
            s += "}\n";
        }
        for (int b = 4; b <= 9; b += 5) {
            s += "__device__ __forceinline__ void fg3_site" + si(b) + "(uint32_t wi, uint32_t (&R)[32], uint32_t (&WN)[16], uint32_t hword, uint32_t l, uint32_t wmask) {\n    switch (wi) {\n";
            for (uint32_t i = 0; i < n_shapes; ++i) s += "        case " + si(int(i)) + ": fg3_step_" + si(int(i)) + "_" + si(b) + "(R, WN, hword, l, wmask); break;\n";
            s += "        default: break;\n    }\n}\n";
        }
        // the whole hash: sq0, sq1 the sequence's two words (eight entries), hw the entries' words in the state
        s += "__device__ __forceinline__ void fg3_run25(uint32_t (&R)[32], uint32_t (&WN)[16], uint32_t sq0, uint32_t sq1, const uint32_t* __restrict__ hw, uint32_t l, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, uint32_t half, uint32_t spb) {\n";
        s += "    uint32_t it = 0;\n    #pragma unroll 1\n    for (; it < 12u; ++it) {\n        const uint32_t tb = 10u * it;\n";
        s += "#if IH_FG3_STOP < 1024\n        if (tb >= (uint32_t)IH_FG3_STOP) break;\n#endif\n";
        // the scratch steps in phase switches of one or two steps, the keyed reads BETWEEN the switches (one copy of each 192-instruction
        // fold, not four: the 2060 is instruction-fetch-bound, one phase for every iteration read 4.75 against 3.61 MH/s); the keyed
        // read's slot is phase-free when P is 2 (t mod 2 = 0) and phase-bound at P 4 or 8, so those depths keep the reads inside the cases
        const bool kout = (P <= 2u) && !onephase;
        auto sw = [&](std::initializer_list<uint32_t> js, const std::string& ktail) {   // the phase switch over the scratch steps j in js
            std::string o = onephase ? "        switch (0u) {\n" : "        switch (it & 3u) {\n";
            for (uint32_t p = 0; p < (onephase ? 1u : 4u); ++p) {
                o += "        case " + u(p) + ": {\n";
                for (uint32_t j : js) { if (!kout && (j % 2u) == 0u && j != 0u && j != 4u) o += keyed("tb + " + u(j), p, j); o += scratch((j + 2u * p) & 7u, j ? "tb + " + u(j) : "tb"); }
                o += "        } break;\n";
            }
            o += "        default: break;\n        }\n" + ktail; return o;
        };
        if (kout) { s += keyed("tb", 0, 0) + sw({0u, 1u}, keyed("tb + 2u", 0, 2)) + sw({2u, 3u}, keyed("tb + 4u", 0, 4)); }
        else {
            s += onephase ? "        switch (0u) {\n" : "        switch (it & 3u) {\n";
            for (uint32_t p = 0; p < (onephase ? 1u : 4u); ++p) {
                s += "        case " + u(p) + ": {\n" + keyed("tb", p, 0) + scratch((0u + 2u * p) & 7u, "tb") + scratch((1u + 2u * p) & 7u, "tb + 1u") + keyed("tb + 2u", p, 2) + scratch((2u + 2u * p) & 7u, "tb + 2u") + scratch((3u + 2u * p) & 7u, "tb + 3u") + keyed("tb + 4u", p, 4) + "        } break;\n";
            }
            s += "        default: break;\n        }\n";
        }
        s += site(4, "tb + 4u");
        if (kout) { s += sw({5u}, keyed("tb + 6u", 0, 6)) + sw({6u, 7u}, keyed("tb + 8u", 0, 8)) + sw({8u}, ""); }
        else {
            s += onephase ? "        switch (0u) {\n" : "        switch (it & 3u) {\n";
            for (uint32_t p = 0; p < (onephase ? 1u : 4u); ++p) {
                s += "        case " + u(p) + ": {\n" + scratch((5u + 2u * p) & 7u, "tb + 5u") + keyed("tb + 6u", p, 6) + scratch((6u + 2u * p) & 7u, "tb + 6u") + scratch((7u + 2u * p) & 7u, "tb + 7u") + keyed("tb + 8u", p, 8) + scratch((8u + 2u * p) & 7u, "tb + 8u") + "        } break;\n";
            }
            s += "        default: break;\n        }\n";
        }
        s += site(9, "tb + 9u");
        // the window rotated by 10: new[j] = old[(j + 10) & 15]
        s += "        { ";
        for (int j = 0; j < 10; ++j) s += "const uint32_t t" + si(j) + " = WN[" + si(j) + "]; ";
        for (int j = 0; j < 6; ++j) s += "WN[" + si(j) + "] = WN[" + si(j + 10) + "]; ";
        for (int j = 0; j < 10; ++j) s += "WN[" + si(6 + j) + "] = t" + si(j) + "; ";
        s += "}\n    }\n";
        s += "#if IH_FG3_STOP >= 1024\n    { const uint32_t tb = 120u;\n" + keyed("tb", 0, 0) + scratch(0u, "tb") + scratch(1u, "tb + 1u") + keyed("tb + 2u", 0, 2) + scratch(2u, "tb + 2u") + scratch(3u, "tb + 3u") + keyed("tb + 4u", 0, 4) + site(4, "tb + 4u")
           + scratch(5u, "tb + 5u") + keyed("tb + 6u", 0, 6) + scratch(6u, "tb + 6u") + scratch(7u, "tb + 7u") + "    }\n";
        // the window back to its own order for the fold: twelve rotations by 10 leave physical WN[i] = logical WN[(i + 8) & 15], so new[j] = old[(j + 8) & 15]
        s += "    { ";
        for (int j = 0; j < 8; ++j) s += "const uint32_t t" + si(j) + " = WN[" + si(j) + "]; ";
        for (int j = 0; j < 8; ++j) s += "WN[" + si(j) + "] = WN[" + si(j + 8) + "]; ";
        for (int j = 0; j < 8; ++j) s += "WN[" + si(j + 8) + "] = t" + si(j) + "; ";
        s += "}\n#else\n    { uint32_t o[16]; for (int j = 0; j < 16; ++j) o[j] = WN[j]; const uint32_t rot = (10u * it) & 15u; for (uint32_t j = 0; j < 16u; ++j) WN[j] = o[(j + rot) & 15u]; }   // debugging (stop=N under 120): the iterations run, no tail\n#endif\n}\n";
        // the entry dispatch of the other forms, empty here (the regrouping kernels and the entry loop still name it)
        s += "__device__ __forceinline__ void fg3_dispatch(uint32_t, uint32_t (&)[32], uint32_t, uint32_t (&)[16], uint32_t, uint32_t, uint32_t, const uint4* __restrict__, uint32_t, fg3_kx&, uint32_t, const uint32_t* __restrict__, uint32_t) {}\n";
        delete[] sh;
        return s;
    }
    if (fp_mode == 9) {
        // SELECT-ALL (fp_mode 9, the measurement form): the programme per hash per step from the hash's seed chain z (three words a step:
        // the class draw, the immediates, the variants), every lane computing the eight candidates on the version 20 wiring and selecting
        // the drawn one by a mask; no shapes, no window, no scratchpad; the step loop in blocks of U steps so the keyed slot is static
        const uint32_t U9 = (P && (4u % P) == 0) ? 4u : (P ? P : 4u);
        s += "__device__ __forceinline__ void fg3_sel_step(uint32_t (&R)[32], uint32_t w0, uint32_t w1, uint32_t w2, uint32_t l, uint32_t wmask) {\n";
        for (int k = 0; k < 32; ++k) {
            const int ak = (7 * k + 1) & 31, bk = (11 * k + 5) & 31;
            s += "    { const uint32_t a = R[" + si(ak) + "], b = R[" + si(bk) + "]; const uint32_t imm = fg3_fold(w1, " + u(uint32_t(k)) + "), q = fg3_fold(w2, " + u(uint32_t(k)) + "), sel = fg3_fold(w0, " + u(uint32_t(k)) + ") & 7u;\n";
            s += "      const uint32_t x = fg3_shfl(a, (l + 1u + (q % 15u)) & 15u, wmask) + imm;\n";
            s += "      const uint32_t c0 = a + b + imm, c1 = a - b - imm, c2 = a ^ b ^ imm, c3 = ((q & 1u) ? (a & b) : (a | b)) ^ imm, c4 = fg3_rotl(a, (imm ^ b) & 31u), c5 = a * b + imm, c6 = __umulhi(a, b) ^ imm;\n";
            s += "      R[" + si(k) + "] = ((0u - (uint32_t)(sel == 0u)) & c0) | ((0u - (uint32_t)(sel == 1u)) & c1) | ((0u - (uint32_t)(sel == 2u)) & c2) | ((0u - (uint32_t)(sel == 3u)) & c3) | ((0u - (uint32_t)(sel == 4u)) & c4) | ((0u - (uint32_t)(sel == 5u)) & c5) | ((0u - (uint32_t)(sel == 6u)) & c6) | ((0u - (uint32_t)(sel == 7u)) & x); }\n";
        }
        s += "}\n";
        s += "__device__ __forceinline__ void fg3_dispatch(uint32_t shape, uint32_t (&R)[32], uint32_t tid, uint32_t (&WN)[16], uint32_t hword, uint32_t t0, uint32_t l, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, uint32_t half, const uint32_t* __restrict__ zseed, uint32_t xsel) {\n";
        s += "    (void)shape; (void)tid; (void)WN; (void)hword; (void)t0; (void)xsel; uint32_t z[8];\n    #pragma unroll\n    for (int k = 0; k < 8; ++k) z[k] = zseed[k];\n";
        s += "    #pragma unroll 1\n    for (uint32_t blk = 0; blk < " + u(per_entry / U9) + "; ++blk) {\n";
        for (uint32_t st = 0; st < U9; ++st) {
            if (keyed_s) throw std::runtime_error("fg3: the compiled keyed form is laid out for the entry bodies (fp_mode 3, 4 and 6), not the select-all form");
            if (fold_every < per_entry && (st % fold_every) == 0) s += "    fg3_keyed(R, l, blk * " + u(U9) + " + " + u(st) + ", ds, wmask, kx, " + si(int(P ? st % P : 0)) + ", half, 0u);\n";
            s += "    { const uint32_t t = blk * " + u(U9) + " + " + u(st) + "; uint32_t sw[3];\n    #pragma unroll\n    for (int j = 0; j < 3; ++j) { const uint32_t idx = 3u * t + (uint32_t)j; z[idx & 7u] = fg3_fold(z[idx & 7u], 8192u + idx); sw[j] = z[idx & 7u]; }\n";
            s += "      fg3_sel_step(R, sw[0], sw[1], sw[2], l, wmask); }\n";
        }
        s += "    }\n}\n";
        delete[] sh;
        return s;
    }
    for (uint32_t i = 0; i < n_shapes; ++i) {
        s += "__device__ __forceinline__ void fg3_entry_" + si(int(i)) + "(uint32_t (&R)[32], uint32_t tid, uint32_t (&WN)[16], uint32_t hword, uint32_t t0, uint32_t l, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, uint32_t half, uint32_t xsel" + std::string(keyed_s ? ", uint32_t* __restrict__ ks_, const uint32_t* __restrict__ s8_, const uint32_t* __restrict__ sk_" : "") + ") {\n";
        if (fp_mode != 6) s += "    const uint32_t spb = (uint32_t)__cvta_generic_to_shared(&fg3_sp[0][tid]);\n"; else s += "    (void)tid;\n";
        if (keyed_s) s += "    (void)ks_; (void)s8_; (void)sk_;\n";
        // the hash's words, only where a slot reads them
        for (int k = 0; k < 32; ++k) {
            const bool load = ((sh[i].w[3] >> k) & 1u) != 0;
            if (needs_h(fp_mode, sh[i].cls[k], load)) s += "    const uint32_t H" + si(k) + " = fg3_fold(hword, " + u(uint32_t(k)) + ");\n";
        }
        // the entry as per_entry / U blocks of U straight-line steps: the window's index at in-block step s is (s + off) & 15 because
        // the window is ROTATED by U registers after every block (WN[i] <- WN[(i + U) & 15]: the step's base folded into the data,
        // 16 moves a block), so the code is U steps a shape, not 16, and the batch's sixteen bodies fit the instruction cache
        const uint32_t U = (fp_mode == 6) ? unroll : 1u;
        s += "    #pragma unroll 1\n    for (uint32_t blk = 0; blk < " + u(per_entry / U) + "; ++blk) {\n";
        for (uint32_t st = 0; st < U; ++st) {
            if (fold_every < per_entry) {
                // the step within the entry is blk * U + st; its keyed step and pipeline slot are static when U is a multiple of fold_every and P
                const bool stat = (U % fold_every) == 0 && (P == 0 || (U % P) == 0);
                if (stat) { if ((st % fold_every) == 0) s += site(U, st, "t0 + blk * " + u(U) + " + " + u(st), int(P ? st % P : 0), "xsel"); }
                else if (keyed_s) throw std::runtime_error("fg3: the compiled keyed form wants the block a multiple of fold_every and of the pipeline depth (a static read site)");
                else s += "    if (((blk * " + u(U) + " + " + u(st) + ") % " + u(fold_every) + ") == 0u) fg3_keyed(R, l, t0 + blk * " + u(U) + " + " + u(st) + ", ds, wmask, kx, " + si(int(P ? (st % P) : 0)) + ", half, xsel);\n";
            }   // fold_every >= per_entry: the kernel's entry loop issues the read before the body
            s += "    {\n";
            for (int k = 0; k < 32; ++k) s += "        " + slot(fp_mode, int(st & 15u), k, sh[i].cls[k], fchain(sh[i].w[1], uint32_t(k)), fchain(sh[i].w[2], uint32_t(k)), ((sh[i].w[3] >> k) & 1u) != 0, sp_stride, ilp6, noxl) + "\n";
            s += "    }\n";
        }
        if (fp_mode == 6 && U < 16u && per_entry == 16u) {
            s += "    { ";
            for (uint32_t j = 0; j < U; ++j) s += "const uint32_t t" + si(int(j)) + " = WN[" + si(int(j)) + "]; ";
            for (uint32_t j = 0; j < 16u - U; ++j) s += "WN[" + si(int(j)) + "] = WN[" + si(int(j + U)) + "]; ";
            for (uint32_t j = 0; j < U; ++j) s += "WN[" + si(int(16u - U + j)) + "] = t" + si(int(j)) + "; ";
            s += "}\n";
        }
        s += "    }\n}\n";
    }
    s += "__device__ __forceinline__ void fg3_dispatch(uint32_t shape, uint32_t (&R)[32], uint32_t tid, uint32_t (&WN)[16], uint32_t hword, uint32_t t0, uint32_t l, const uint4* __restrict__ ds, uint32_t wmask, fg3_kx& kx, uint32_t half, const uint32_t* __restrict__ zseed, uint32_t xsel" + std::string(keyed_s ? ", uint32_t* __restrict__ ks_, const uint32_t* __restrict__ s8_, const uint32_t* __restrict__ sk_" : "") + ") {\n    (void)zseed;\n    switch (shape) {\n";
    for (uint32_t i = 0; i < n_shapes; ++i) s += "        case " + si(int(i)) + ": fg3_entry_" + si(int(i)) + "(R, tid, WN, hword, t0, l, ds, wmask, kx, half, xsel" + std::string(keyed_s ? ", ks_, s8_, sk_" : "") + "); break;\n";
    s += "        default: break;\n    }\n}\n";
    delete[] sh;
    return s;
}

}  // namespace forge3
