#include "engine.hpp"
#include "codegen.hpp"
#include "forge2_shapes.hpp"
#include "forge3_shapes.hpp"
#include <thread>
#include <chrono>
#include "ignpow.h"
#include "igneumhash.h"
#include "../../common/ih_params.h"   // the worker's own parser: the keyed-read steps under 21 and 25 (keyed_steps), beside the reference's parse
#include "rxe1.h"

#include <nvrtc.h>
#include <vector>
#include "cuda_dyn.hpp"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cctype>
#include <cstring>
#ifndef _WIN32
#include <sys/mman.h>   // F-53: mlock of the secret's host copy
#endif
#include <fstream>
#include <sstream>
#include <stdexcept>
#include <sys/stat.h>
#ifdef _WIN32
#include <direct.h>
#define ign_mkdir(p) _mkdir(p)
#else
#define ign_mkdir(p) mkdir(p, 0755)
#endif

namespace igneum {
namespace {

constexpr uint32_t kResultWords = 18;
constexpr uint32_t kMaxResults = 256;
constexpr uint32_t kParamsWords = 24;   // header 8, target 8, the bond secret 8 (version 8 bit 10: ignh_test_sk(header) for the vectors and the bench)
constexpr uint32_t kBlock = 256;  // threads per block = 16 nonces

// Version 9 (SPEC-R0D-v0.md sections 3 and 4, GPU engineer A's compiled form, 12 Oct 2026): the period's programme as a device
// function of straight-line code over a lane's 16 registers, one native instruction per operation (the fused FMA as __fmaf_rn,
// the FADD as __fadd_rn: intrinsics NVRTC never contracts or reorders), the range mask on all 16 after every 32 operations as the
// reference's r0d_run_lane does; the records are ignh_r0d_program's (opcode, dst, src1, src2, imm LE32). No interpreter runs.
// Version 10 (SPEC-R0E-v0.md section 2, the octet form): the programme over the lane octet in lock-step as straight-line code:
// SHFL a warp shuffle at the op's lane offset, GATHER a read of the per-nonce table, TILE the octet's transpose through shared
// memory, the item flag the second source from the staged item at a register-drawn word; the range mask at every window's end;
// the octet fold after the programme. n_words = the item's words (64 at W256). The text lands before the kernel's typedefs.
// the ablation forms (SPEC-R0E-v0.md section 2a, the attribution method; the hash team's cells, 20:5x UK 12 Oct 2026): --tune ablate=N
// replaces one class by its cheapest equivalent: 1 the gather by a register read, 2 the tile by the identity, 3 the shuffle by the
// lane's own register, 4 the item-sourced operand by the register, 5 the FP32 and integer operations by a move (6, the walk's reads
// from the cache, is the engine's: the search runs over the cache buffer; 7 the version 14 mma tile skipped). The class's energy is the difference per hash.
// version 12 (SPEC-R0E-v0 section 2c, the SRAM-bound form; aw = ACCESS_BYTES / 4 words per row, n_rows = the table's rows): the per-lane
// access chain A (the fold of the whole last row the lane touched, from the lane's first table word at the round's start); GATHER the
// row read at (R[src2] ^ imm ^ A) mod n_rows, its words folded, A and R[dst] = R[src1] ^ fold; SCATTER the lane-owned row
// (((...) mod n_rows) & ~7) | lane rewritten word by word from R[src1], R[dst] and the old row, the new fold to A and R[dst]; TILE the
// SKEW (register k from lane (l + k) & 7, sixteen shuffles, no shared memory); MMA the exact 8 x 8 x 8 INT8 tile by dp4a (the b rows
// from the other lanes by shuffle), the dot products XORed into dst .. dst + 7
// versions 12 to 14 (SPEC-R0E-v0 2c to 2e): kch independent access chains per nonce (version 13's chains_per_nonce), lane_bank the
// gather's row confined to the lane's bank group, st_rows the period's SHARED table rows (the shared form: GATHER reads it at the
// nonce's row offset, SCATTER writes the per-nonce table), tshape version 14's tile (1 = 16x8x32 on the tensor pipe), fold_every the
// topology fold once every k tiles (the hash team's rule: after tile n when n mod k = 0 counting from 1)
static bool ih_trace_ops_emit = false;   // the trace mode's per-op dump (the GPU team's port diagnosis, 13 Oct 2026)
std::string ih_r0e_source(const uint8_t* ops, uint32_t n, uint32_t n_words, bool store_masked, uint32_t ablate, uint32_t aw = 0, uint32_t n_rows = 0, uint32_t kch = 1, bool lane_bank = false, uint32_t st_rows = 0, uint32_t tshape = 0, uint32_t fold_every = 1, bool layout_draw = false, bool tile_frag = false) {
    std::string s;
    if (kch < 1) kch = 1;
    if (fold_every < 1) fold_every = 1;
    // the octet form's macros (the generated text precedes the kernel file, which defines the same under IH_OCTET32)
    s += tile_frag ? "#define IH_OCTET32 1\n#define IH_OCT_ONE ((threadIdx.x & 3u) == 0u)\n#define IH_SRC8(k) (4u * (unsigned int)(k) + (threadIdx.x & 3u))\n#define IH_W8 32\n#define IH_OCT_SHIFT 5\n" : "#define IH_OCTET32 0\n#define IH_OCT_ONE true\n#define IH_SRC8(k) (k)\n#define IH_W8 8\n#define IH_OCT_SHIFT 3\n";
    if (ih_trace_ops_emit) s += "__device__ volatile unsigned int ih_tr_round = 99u;\n__device__ unsigned int* ih_tr_ops = 0;\n#define IH_TR_DUMP(I) if (ih_tr_ops && ih_tr_round == 0u && blockIdx.x == 0u && (threadIdx.x >> IH_OCT_SHIFT) == 0u && IH_OCT_ONE) { for (int k_ = 0; k_ < 16; ++k_) ih_tr_ops[(I) * 128u + lane * 16u + (unsigned int)k_] = R[k_]; }\n";
    s += "__device__ __forceinline__ unsigned int ih_r0d_m(unsigned int x) { return (x & 0x3FFFFFFFu) | 0x20000000u; }\n";
    s += "__device__ __forceinline__ float ih_r0d_f(unsigned int x) { return __uint_as_float((x & 0x3FFFFFFFu) | 0x20000000u); }\n";
    if (tshape) s += "__device__ __forceinline__ void ih_r14_tile(unsigned int (&R)[16], const unsigned int* item, unsigned int* tile, unsigned int lane, unsigned int mask, unsigned int off, unsigned int rot, unsigned int dbase, unsigned int lay);\n";
    if (tshape && tile_frag) s += "__device__ __forceinline__ void ih_r14_tile_frag32(unsigned int (&R)[16], const unsigned int* item, unsigned int lane, unsigned int mask, unsigned int off, unsigned int rot, unsigned int dbase, unsigned int lay);\n";
    s += "__device__ __forceinline__ void ih_r0e_octet(unsigned int (&R)[16], const unsigned int (&wl)[" + std::to_string(n_words / 8) + "], unsigned int lane, unsigned int mask, unsigned int* item, unsigned int* tile, " + std::string(aw ? "unsigned int* table" : "const unsigned int* table") + (st_rows ? ", const unsigned int* __restrict__ st, unsigned int nonce_off" : "") + ") {\n";
    s += "#pragma unroll\n    for (int k = 0; k < " + std::to_string(n_words / 8) + "; ++k) item[lane + 8u * k] = wl[k];\n    __syncwarp(mask);\n";
    // version 11 (the project's ruling of 20:4x UK 12 Oct 2026 on the 4090 reading): the FP result is STORED through the range mask, so the
    // read mask is the identity on a register whose last writer in the straight-line programme was FMA, FADD or the window-end mask
    // and is elided exactly there; it is applied where the last writer was an integer operation, a GATHER, a SHFL or TILE whose moved
    // value came from such a writer (lock-step, so the writer class is static through the shuffle's offset and the tile's
    // permutation), the item word (raw) or the initial state. Version 10 stores unmasked, so nothing is elided there.
    const bool v12 = aw != 0;
    const uint32_t cb = v12 ? n_rows / kch : 0, scb = st_rows ? st_rows / kch : 0;   // the chain's block of rows in each table
    if (v12) { for (uint32_t c = 0; c < kch; ++c) s += "    unsigned int A" + std::to_string(c) + " = table[" + std::to_string((c * 8) % (n_rows * aw)) + "u + lane];\n"; }
    uint32_t acc_n = 0, tiles_n = 0;
    if (tshape) s += "    unsigned int topo = 0;\n";
    auto fold_row = [&](const std::string& tab, const std::string& base) {   // the fold of a row's aw words: the row's XOR (the reference's r12_fold_row)
        std::string f = tab + "[" + base + "]";
        for (uint32_t i = 1; i < aw; ++i) f += " ^ " + tab + "[" + base + " + " + std::to_string(i) + "u]";
        return "(" + f + ")";
    };
    uint32_t masked = 0;
    auto F = [&](const std::string& expr, int r) { return (store_masked && r >= 0 && ((masked >> r) & 1u)) ? "__uint_as_float(" + expr + ")" : "ih_r0d_f(" + expr + ")"; };
    const std::string SM = store_masked ? "ih_r0d_m(" : "(", SE = ")";
    for (uint32_t i = 0; i < n; ++i) {
        if (ih_trace_ops_emit) s += "    IH_TR_DUMP(" + std::to_string(i) + "u)\n";   // slot i: the state before op i (slot 0 the pre-state)
        const uint8_t op = ops[8 * i] & 0x7f, flag = ops[8 * i] & 0x80, d = ops[8 * i + 1] & 15, a = ops[8 * i + 2] & 15, b = ops[8 * i + 3] & 15;
        const uint32_t imm = uint32_t(ops[8 * i + 4]) | (uint32_t(ops[8 * i + 5]) << 8) | (uint32_t(ops[8 * i + 6]) << 16) | (uint32_t(ops[8 * i + 7]) << 24);
        const std::string D = "R[" + std::to_string(d) + "]", IMM = std::to_string(imm) + "u";
        std::string A = "R[" + std::to_string(a) + "]", B = "R[" + std::to_string(b) + "]", Dr = D;
        int ra = a, rb = b, rd = d;   // the register behind each operand expression, or -1 for the item word
        // the item flag (the reference overwrites register src2 with the item word before the operation, so every read of that
        // register in the operation, as src1, src2 or the FMA's addend, sees the item word; the register itself is restored unless
        // it is the destination, which the write below covers)
        const bool item_op = flag && op < 8 && ablate != 4;
        if (item_op) { s += "    { const unsigned int w = item[(" + B + " ^ " + IMM + ") % " + std::to_string(n_words) + "u];\n"; if (a == b) { A = "w"; ra = -1; } if (d == b) { Dr = "w"; rd = -1; } B = "w"; rb = -1; }
        auto window_end = [&]() { if ((i & 31u) == 31u) { if (v12 && kch > 1) for (uint32_t c = 0; c < kch; ++c) s += "    R[" + std::to_string(c) + "] ^= A" + std::to_string(c) + ";\n"; s += "#pragma unroll\n    for (int k = 0; k < 16; ++k) R[k] = ih_r0d_m(R[k]);\n"; masked = 0xFFFFu; } };
        if (v12 && op == 9) {   // GATHER (version 12): the row read at the chain's block (the shared form: the period's table at the nonce's offset), the row's XOR into dst, the chain
            const std::string AC = "A" + std::to_string(acc_n % kch), blk = std::to_string((acc_n % kch) * (st_rows ? scb : cb)); ++acc_n;
            const std::string r0 = st_rows ? "((" + B + " ^ " + IMM + " ^ " + AC + ") + nonce_off) & " + std::to_string(scb - 1) + "u" : "(" + B + " ^ " + IMM + " ^ " + AC + ") & " + std::to_string(cb - 1) + "u";
            s += "    { unsigned int row = " + r0 + "; " + (lane_bank ? "row = (row & ~7u) | lane; " : "") + "const unsigned int rb = (row + " + blk + "u) * " + std::to_string(aw) + "u; const unsigned int v = " + fold_row(st_rows ? "st" : "table", "rb") + "; " + AC + " = v; " + D + " ^= v; }\n";
            masked &= ~(1u << d); window_end(); continue;
        }
        if (v12 && op == 11) {   // SCATTER: the lane-owned row of the chain's block rewritten from registers src1 + i XOR dst, its XOR to the chain and dst
            const std::string AC = "A" + std::to_string(acc_n % kch), blk = std::to_string((acc_n % kch) * cb); ++acc_n;
            s += "    { const unsigned int row = " + blk + "u + ((((" + B + " ^ " + IMM + " ^ " + AC + ") & " + std::to_string(cb - 1) + "u) & ~7u) | lane); const unsigned int rb = row * " + std::to_string(aw) + "u; const unsigned int d0 = " + D + ";\n";
            for (uint32_t w = 0; w < aw; ++w) s += "      table[rb + " + std::to_string(w) + "u] = R[" + std::to_string((a + w) & 15) + "] ^ d0;\n";
            s += "      const unsigned int v = " + fold_row("table", "rb") + "; " + AC + " = v; " + D + " = v; }\n";
            masked &= ~(1u << d); window_end(); continue;
        }
        if (ablate == 7 && v12 && op == 12 && tshape) { window_end(); continue; }   // ablation 7 (the GPU team, 13 Oct 2026): the mma tile skipped (the version 14 class the forms 1 to 6 pre-date)
        if (v12 && op == 12 && tshape) {   // version 14: the exact 16 x 8 x 32 tile (the registered shape; the other two shapes are refused at admission)
            s += "    { const unsigned int off = (__shfl_sync(mask, " + B + ", IH_SRC8(" + std::to_string(imm & 7) + "u), IH_W8) ^ " + IMM + " ^ topo) & 63u; const unsigned int rot = (" + std::to_string(a) + "u + (topo >> 6)) & 7u; const unsigned int dbase = (" + std::to_string(d) + "u + (topo >> 9)) & 15u;\n";
            s += tile_frag ? "      ih_r14_tile_frag32(R, item, lane, mask, off, rot, dbase, " + std::string(layout_draw ? "(topo >> 24) & 7u" : "0u") + ");\n" : "      ih_r14_tile(R, item, tile, lane, mask, off, rot, dbase, " + std::string(layout_draw ? "(topo >> 24) & 7u" : "0u") + ");\n";
            if (++tiles_n % fold_every == 0) {
                // the topology word: the fold over the eight lanes of register dbase (a runtime index: a select chain), then the scramble (register k from lane (l + sh_k) & 7)
                s += "      unsigned int vd = R[0];\n#pragma unroll\n      for (int k = 1; k < 16; ++k) vd = (dbase == (unsigned int)k) ? R[k] : vd;\n      unsigned int t = 0;\n#pragma unroll\n      for (int l = 0; l < 8; ++l) { const unsigned int v = __shfl_sync(mask, vd, IH_SRC8((unsigned int)l), IH_W8); t = __funnelshift_l(t ^ v, t ^ v, 5u) + v; }\n";
                s += "      t ^= t >> 16; t *= 0x9E3779B1u; t ^= t >> 15; topo = t;\n#pragma unroll\n      for (int k = 0; k < 16; ++k) R[k] = __shfl_sync(mask, R[k], IH_SRC8((lane + ((topo >> (2 * k)) + (unsigned int)k)) & 7u), IH_W8);\n";
            }
            s += "    }\n";
            masked = 0; window_end(); continue;
        }
        if (v12 && op == 12) {   // MMA: the exact 8 x 8 x 8 INT8 tile by dp4a
            const std::string A1 = "R[" + std::to_string((a + 1) & 15) + "]", B1 = "R[" + std::to_string((b + 1) & 15) + "]";
            s += "    { const unsigned int wa0 = " + A + ", wa1 = " + A1 + ", wb0 = " + B + ", wb1 = " + B1 + ";\n";
            for (uint32_t k = 0; k < 8; ++k) s += "      { const int acc = __dp4a((int)wa1, (int)__shfl_sync(mask, wb1, IH_SRC8(" + std::to_string(k) + "u), IH_W8), __dp4a((int)wa0, (int)__shfl_sync(mask, wb0, IH_SRC8(" + std::to_string(k) + "u), IH_W8), 0)); R[" + std::to_string((d + k) & 15) + "] ^= (unsigned int)acc; }\n";
            s += "    }\n";
            for (uint32_t k = 0; k < 8; ++k) masked &= ~(1u << ((d + k) & 15));
            window_end(); continue;
        }
        if (v12 && op == 10) {   // TILE = the SKEW: register k from lane (l + k) & 7; the masked set (lane-uniform) is unchanged
            s += "#pragma unroll\n    for (int k = 1; k < 16; ++k) R[k] = __shfl_sync(mask, R[k], IH_SRC8((lane + (unsigned int)k) & 7u), IH_W8);\n";
            window_end(); continue;
        }
        if (ablate == 5 && op < 8) { s += "    " + D + " = " + A + ";\n"; if (item_op) s += "    }\n"; masked = ((masked >> a) & 1u) ? (masked | (1u << d)) : (masked & ~(1u << d)); window_end(); continue; }
        if (ablate == 2 && op == 10) { window_end(); continue; }
        switch (op) {
            case 0: s += "    " + D + " = " + SM + "__float_as_uint(__fmaf_rn(" + F(A, ra) + ", " + F(B, rb) + ", " + F(Dr, rd) + "))" + SE + ";\n"; break;
            case 1: s += "    " + D + " = " + SM + "__float_as_uint(__fadd_rn(" + F(A, ra) + ", " + F(B, rb) + "))" + SE + ";\n"; break;
            case 2: s += "    " + D + " = " + A + " ^ " + B + ";\n"; break;
            case 3: s += "    " + D + " = __funnelshift_l(" + A + ", " + A + ", " + std::to_string(imm & 31u) + "u);\n"; break;
            case 4: s += "    " + D + " = " + A + " + " + B + ";\n"; break;
            case 5: s += "    " + D + " = __umulhi(" + A + ", " + B + ");\n"; break;
            case 6: s += "    " + D + " = " + A + " ^ ((unsigned int)__popc(" + B + ") * 0x9E3779B1u);\n"; break;
            case 7: s += "    " + D + " = " + A + " + ((unsigned int)(" + B + " ? __clz(" + B + ") : 32) * 0x85EBCA77u);\n"; break;
            case 8: s += "    " + D + " = " + B + " ^ " + (ablate == 3 ? A : "__shfl_sync(mask, " + A + ", IH_SRC8((lane + " + std::to_string(1u + imm % 7u) + "u) & 7u), IH_W8)") + ";\n"; break;   // SHFL (ablation 3: the lane's own register)
            case 9: s += "    " + D + " = " + A + " ^ " + (ablate == 1 ? B : "table[(" + B + " ^ " + IMM + ") % IH_R0E_TABLE_WORDS]") + ";\n"; break;   // GATHER (the words a define: the project's table-size cells)
            default:   // TILE: lane l's register k becomes lane (k & 7)'s register ((k >> 3) + 2 l)
                s += "    __syncwarp(mask);\n#pragma unroll\n    for (int k = 0; k < 16; ++k) tile[lane * 16u + k] = R[k];\n    __syncwarp(mask);\n#pragma unroll\n    for (int k = 0; k < 16; ++k) R[k] = tile[(k & 7) * 16 + (k >> 3) + 2 * lane];\n"; break;
        }
        if (item_op) s += "    }\n";
        if (op <= 1) masked |= 1u << d;
        else if (op == 10) { uint32_t m2 = 0; for (int k = 0; k < 16; ++k) { bool all = true; for (int l = 0; l < 8; ++l) if (!((masked >> (((k >> 3) + 2 * l) & 15)) & 1u)) all = false; if (all) m2 |= 1u << k; } masked = m2; }   // the tile: register k takes register (k >> 3) + 2 l of lane k & 7
        else masked &= ~(1u << d);
        window_end();
    }
    if (ih_trace_ops_emit) s += "    IH_TR_DUMP(" + std::to_string(n) + "u)\n";   // slot n: the state after the last op
    s += "    { const unsigned int f = __funnelshift_l(__shfl_sync(mask, R[1], IH_SRC8((lane + 3u) & 7u), IH_W8) + __shfl_sync(mask, R[2], IH_SRC8((lane + 5u) & 7u), IH_W8), __shfl_sync(mask, R[1], IH_SRC8((lane + 3u) & 7u), IH_W8) + __shfl_sync(mask, R[2], IH_SRC8((lane + 5u) & 7u), IH_W8), 7u); R[0] ^= f; }\n";
    s += "}\n";
    return s;
}
// version 15 (the bandwidth-first class, pow/ref r16): the period's key words as defines and its programme as straight-line code
// on the lane's 32 registers (the records ignh_r16_period returns: opcode 0 MAD 1 ADD 2 SUB 3 XOR 4 ROTL 5 ROTR, d, a, b, imm);
// a and b are read before the write as r16::run_lane reads them
std::string ih_r16_source(const uint8_t key[32], const uint8_t* ops, uint32_t n, uint32_t regs = 32, const uint8_t* cs = nullptr, uint32_t pool_seg = 0, uint32_t mem_k = 0, bool batched = false) {
    std::string s = "#define IH_OCTET32 0\n#define IH_OCT_ONE true\n#define IH_SRC8(k) (k)\n#define IH_W8 8\n#define IH_OCT_SHIFT 3\n";   // the octet macros the version 4 helpers read (the generated text precedes the kernel file)
    for (int k = 0; k < 8; ++k) s += "#define IH_R16_K" + std::to_string(k) + " " + std::to_string(uint32_t(key[4 * k]) | (uint32_t(key[4 * k + 1]) << 8) | (uint32_t(key[4 * k + 2]) << 16) | (uint32_t(key[4 * k + 3]) << 24)) + "u\n";
    // the segment pool (revision 18): 64 compiled segments of pool_seg operations (slot k's variants at segments 8k .. 8k + 7), the attempt
    // running 8 of them by the init digest's byte k (its low three bits): one switch a slot, the segments inlined (the file stays in registers)
    const uint32_t nq = batched ? 128u : 16u;   // the fold queue: 16 words a read (revision 19), eight reads' 128 words a batch (19b)
    std::string qdecl;
    if (mem_k) { qdecl = "    unsigned int"; for (uint32_t i = 0; i < nq; ++i) qdecl += std::string(i ? ", q" : " q") + std::to_string(i) + " = 0u"; qdecl += ";\n"; }
    s += pool_seg ? "__device__ __forceinline__ void ih_r16_prog(unsigned int (&R)[" + std::to_string(regs) + "], const unsigned int* dg) {\n" : mem_k ? "__device__ __forceinline__ void ih_r16_prog(unsigned int (&R)[" + std::to_string(regs) + "], const uint4* __restrict__ dag) {\n" + qdecl : "__device__ __forceinline__ void ih_r16_prog(unsigned int (&R)[" + std::to_string(regs) + "]) {\n";   // unsigned int: the module's typedefs come after the variant defines
    uint32_t nread = 0;
    for (uint32_t j = 0; j < n; ++j) {
        // revision 19b (the reads in batches of eight): at j mod 8K = 0 (j = 0 included) the eight addresses are the ring's eight most recent
        // values (ring register (j + N - 1 - t) mod N, the init at j = 0) XOR the key words (nread + t) mod 8, issued together; their 128 words
        // fold into the next 128 operations one a word (r16s::run_mem, batched)
        if (mem_k && batched && (j % (8 * mem_k)) == 0) {
            s += "    {";
            for (uint32_t t = 0; t < 8; ++t) {
                const uint32_t ridx = (j % regs + regs - 1 - t) % regs;
                s += " const unsigned int a" + std::to_string(t) + " = R[" + std::to_string(ridx) + "] ^ IH_R16_K" + std::to_string((nread + t) & 7) + "; const uint4* i" + std::to_string(t) + " = dag + (unsigned long long)((a" + std::to_string(t) + " >> 2) & (IH_N_ITEMS - 1u)) * (IH_ITEM_BYTES / 16u) + (a" + std::to_string(t) + " & 3u) * 4u;";
            }
            for (uint32_t t = 0; t < 8; ++t) for (uint32_t k = 0; k < 4; ++k) s += " const uint4 v" + std::to_string(4 * t + k) + " = __ldg(i" + std::to_string(t) + " + " + std::to_string(k) + ");";
            for (uint32_t w = 0; w < 32; ++w) s += " q" + std::to_string(4 * w) + " = v" + std::to_string(w) + ".x; q" + std::to_string(4 * w + 1) + " = v" + std::to_string(w) + ".y; q" + std::to_string(4 * w + 2) + " = v" + std::to_string(w) + ".z; q" + std::to_string(4 * w + 3) + " = v" + std::to_string(w) + ".w;";
            s += " }\n";
            nread += 8;
        }
        // revision 19 (the memory-fused stream): before op j (j > 0, j mod K = 0) the address is the previous op's result XOR the key word nread mod 8,
        // the item (addr >> 2) & (items - 1), the quarter addr & 3; the quarter's 16 words XOR the immediates of ops j .. j + 15 (r16s::run_mem)
        if (mem_k && !batched && j && (j % mem_k) == 0) {
            const uint8_t* pq = ops + 8 * (j - 1);
            s += "    { const unsigned int addr = R[" + std::to_string(pq[1] % regs) + "] ^ IH_R16_K" + std::to_string(nread & 7) + "; const uint4* it = dag + (unsigned long long)((addr >> 2) & (IH_N_ITEMS - 1u)) * (IH_ITEM_BYTES / 16u) + (addr & 3u) * 4u; const uint4 v0 = __ldg(it), v1 = __ldg(it + 1), v2 = __ldg(it + 2), v3 = __ldg(it + 3); q0 = v0.x; q1 = v0.y; q2 = v0.z; q3 = v0.w; q4 = v1.x; q5 = v1.y; q6 = v1.z; q7 = v1.w; q8 = v2.x; q9 = v2.y; q10 = v2.z; q11 = v2.w; q12 = v3.x; q13 = v3.y; q14 = v3.z; q15 = v3.w; }\n";
            ++nread;
        }
        const bool folded = mem_k && (batched ? (j % (8 * mem_k)) < 128 : (j && (j % mem_k) < 16));
        const uint32_t qidx = mem_k ? (batched ? (j % (8 * mem_k)) : (j % mem_k)) : 0u;   // version 15 (no keyed reads, mem_k 0) divided by zero here: the SIGFPE of 15 Oct on pool-b2's bandwidth document
        if (pool_seg && j % pool_seg == 0) {
            const uint32_t v = j / pool_seg, k = v / 8, pv = v % 8;
            if (pv == 0) s += "    switch ((dg[" + std::to_string(k / 4) + "] >> " + std::to_string(8 * (k % 4)) + "u) & 7u) {\n";
            s += "    case " + std::to_string(pv) + ": {\n";
        }
        const uint8_t* q = ops + 8 * j;
        const std::string a = "R[" + std::to_string(q[2] % regs) + "]", b = "R[" + std::to_string(q[3] % regs) + "]", d = "R[" + std::to_string(q[1] % regs) + "]";
        const uint32_t imm = uint32_t(q[4]) | (uint32_t(q[5]) << 8) | (uint32_t(q[6]) << 16) | (uint32_t(q[7]) << 24);
        const std::string I = std::to_string(imm) + "u";
        s += "    { const unsigned int a = " + a + ", b = " + b + "; ";
        switch (q[0]) {
            case 0: s += d + " = a * b + " + I + ";"; break;
            case 1: s += d + " = a + b + " + I + ";"; break;
            case 2: s += d + " = a - b - " + I + ";"; break;
            case 3: s += d + " = a ^ b ^ " + I + ";"; break;
            case 4: case 5: {   // ROTL by r; ROTR by r = ROTL by 32 - r (r = 0: the identity, as the reference's rotl32 masks); the rotate inline (the module's helpers come later)
                const uint32_t r = q[0] == 4 ? (imm & 31) : ((32 - (imm & 31)) & 31);
                s += d + " = " + (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b;"; break;
            }
            case 6: s += d + " = a * (b | 1u) + " + I + ";"; break;   // revision 4's MAD: the odd multiplier (a bijection in a)
            case 8: case 9: case 10: case 11: case 12: case 13: case 14: {   // revision 5: d = d_old + rotl(g, (imm >> 5) & 31), g the revision 4 result of opcode (q - 8)
                std::string g;
                switch (q[0] - 8) {
                    case 0: g = "a * b + " + I; break;
                    case 1: g = "a + b + " + I; break;
                    case 2: g = "a - b - " + I; break;
                    case 3: g = "a ^ b ^ " + I; break;
                    case 4: case 5: { const uint32_t r = (q[0] - 8) == 4 ? (imm & 31) : ((32 - (imm & 31)) & 31); g = (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b"; break; }
                    default: g = "a * (b | 1u) + " + I; break;
                }
                const uint32_t rr = (imm >> 5) & 31;
                s += "const unsigned int g = " + g + "; " + d + " += " + (rr ? "((g << " + std::to_string(rr) + "u) | (g >> " + std::to_string(32 - rr) + "u))" : std::string("g")) + ";"; break;
            }
            case 16: case 17: case 18: case 19: case 20: case 21: case 22: {   // revision 6: the bijection of the destination as one fused three-input instruction per class (opcode q - 16)
                switch (q[0] - 16) {
                    case 0: s += d + " += a * b + " + I + ";"; break;
                    case 1: s += d + " += a + b + " + I + ";"; break;
                    case 2: s += d + " += a - b - " + I + ";"; break;
                    case 3: s += d + " ^= a ^ b ^ " + I + ";"; break;
                    case 4: case 5: { const uint32_t r = (q[0] - 16) == 4 ? (imm & 31) : ((32 - (imm & 31)) & 31); s += d + " += " + (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b;"; break; }
                    default: s += d + " += a * (b | 1u) + " + I + ";"; break;
                }
                break;
            }
            case 24: case 25: case 26: case 27: case 28: case 29: case 30: {   // revision 8 (form A): d = d_old ^ g, g the revision 4 result of opcode (q - 24), no rotate
                std::string g;
                switch (q[0] - 24) {
                    case 0: g = "a * b + " + I; break;
                    case 1: g = "a + b + " + I; break;
                    case 2: g = "a - b - " + I; break;
                    case 3: g = "a ^ b ^ " + I; break;
                    case 4: case 5: { const uint32_t r = (q[0] - 24) == 4 ? (imm & 31) : ((32 - (imm & 31)) & 31); g = (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b"; break; }
                    default: g = "a * (b | 1u) + " + I; break;
                }
                s += d + " ^= " + g + ";"; break;
            }
            case 32: case 33: case 34: case 35: case 36: case 37: case 40: case 41: case 42: case 43: case 44: case 45: {   // revision 13: the three-operand forms, c = R[imm]; 40..45 consume the destination
                const std::string cv = "R[" + std::to_string(imm % regs) + "]";
                std::string g;
                switch ((q[0] - 32) & 7) {
                    case 1: g = "a + b + " + cv; break;
                    case 2: g = "a - b - " + cv; break;
                    case 3: g = "a ^ b ^ " + cv; break;
                    case 4: g = "__funnelshift_l(a, a, " + cv + " & 31u) ^ b"; break;   // rotl by the register's low five bits
                    case 5: g = "__funnelshift_r(a, a, " + cv + " & 31u) ^ b"; break;   // rotr
                    default: g = "a + b + " + cv; break;   // (the MAD keeps opcode 6)
                }
                s += d + (q[0] >= 40 ? " ^= " : " = ") + g + ";"; break;
            }
            case 48: case 56: {   // revision 14a/14b: the class from two bits of R[c] among add, xor, rotl, rotr (the rotate by the immediate's low five bits); 56 consumes the destination
                const std::string cv = "R[" + std::to_string((cs ? cs[j] : 0) % regs) + "]";
                const uint32_t r = imm & 31;
                const std::string rl = r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a");
                const std::string rr = r ? "((a >> " + std::to_string(r) + "u) | (a << " + std::to_string(32 - r) + "u))" : std::string("a");
                s += "const unsigned int cv = " + cv + "; const unsigned int g = (cv & 2u) ? ((cv & 1u) ? (" + rr + " ^ b) : (" + rl + " ^ b)) : ((cv & 1u) ? (a ^ b ^ " + I + ") : (a + b + " + I + ")); " + d + (q[0] >= 56 ? " ^= g;" : " = g;"); break;
            }
            case 64: case 65: case 66: case 67: case 68: case 69: case 70: case 71: case 72: case 73: {   // revisions 15/15b (r16s): ADD SUB XOR ROTL ROTR MAD MULHI LOP3 PRMT LEA, bijective in a; c = R[cs[j]]
                const std::string cv = "R[" + std::to_string((cs ? cs[j] : 0) % regs) + "]";
                if (folded) {   // revision 19: the immediate XOR the quarter word, a runtime value: the runtime forms of every class
                    const std::string m = "(" + I + " ^ q" + std::to_string(qidx) + ")";
                    switch (q[0] - 64) {
                        case 0: s += d + " = a + b + " + m + ";"; break;
                        case 1: s += d + " = a - b - " + m + ";"; break;
                        case 2: s += d + " = a ^ b ^ " + m + ";"; break;
                        case 3: s += d + " = __funnelshift_l(a, a, " + m + " & 31u) ^ b;"; break;
                        case 4: s += d + " = __funnelshift_r(a, a, " + m + " & 31u) ^ b;"; break;
                        case 5: s += d + " = a * (b | 1u) + " + m + ";"; break;
                        case 6: s += d + " = a + __umulhi(b, " + m + " | 1u);"; break;
                        case 7: s += "const unsigned int c = " + cv + ", t = " + m + " & 15u; " + d + " = a ^ (((t & 1u) ? (~b & ~c) : 0u) | ((t & 2u) ? (b & ~c) : 0u) | ((t & 4u) ? (~b & c) : 0u) | ((t & 8u) ? (b & c) : 0u));"; break;
                        case 8: s += "const unsigned int c = " + cv + "; " + d + " = a ^ __byte_perm(b, c, " + m + " & 0x7777u);"; break;
                        default: s += d + " = a + (b << (1u + (" + m + " % 31u)));"; break;
                    }
                    break;
                }
                switch (q[0] - 64) {
                    case 0: s += d + " = a + b + " + I + ";"; break;
                    case 1: s += d + " = a - b - " + I + ";"; break;
                    case 2: s += d + " = a ^ b ^ " + I + ";"; break;
                    case 3: case 4: { const uint32_t r = (q[0] - 64) == 3 ? (imm & 31) : ((32 - (imm & 31)) & 31); s += d + " = " + (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b;"; break; }
                    case 5: s += d + " = a * (b | 1u) + " + I + ";"; break;
                    case 6: s += d + " = a + __umulhi(b, " + std::to_string(imm | 1u) + "u);"; break;
                    case 7: {   // LOP3 with the two-input table t = imm & 15 over (b, c): the OR of the set minterms
                        const uint32_t t = imm & 15; std::string g;
                        for (uint32_t bit = 0; bit < 4; ++bit) if ((t >> bit) & 1) g += (g.empty() ? "" : " | ") + std::string("(") + ((bit & 1) ? "b" : "~b") + " & " + ((bit >> 1) ? "c" : "~c") + ")";
                        s += "const unsigned int c = " + cv + "; " + d + " = a ^ (" + (g.empty() ? std::string("0u") : g) + ");"; break;
                    }
                    case 8: s += "const unsigned int c = " + cv + "; " + d + " = a ^ __byte_perm(b, c, " + std::to_string(imm & 0x7777u) + "u);"; break;   // the selector's nibbles & 7 (the reference's prmt)
                    default: s += d + " = a + (b << " + std::to_string(1u + (imm % 31u)) + "u);"; break;   // LEA
                }
                break;
            }
        }
        s += " }\n";
        if (pool_seg && (j + 1) % pool_seg == 0) { s += "    break; }\n"; if (((j + 1) / pool_seg) % 8 == 0) s += "    }\n"; }
    }
    s += "}\n";
    return s;
}

// version 16's quad form (the GPU team, 15:3x UK 13 Oct 2026): the four banks' programmes as four functions over 56 local registers (bank bk's
// operations are the sequence's indices j with j mod 4 == bk, in order; d, a, b rebased by 56 bk), the key words as defines
std::string ih_r16q_source(const uint8_t key[32], const uint8_t* ops, uint32_t n) {
    std::string s = "#define IH_OCTET32 0\n#define IH_OCT_ONE true\n#define IH_SRC8(k) (k)\n#define IH_W8 8\n#define IH_OCT_SHIFT 3\n";
    for (int k = 0; k < 8; ++k) s += "#define IH_R16_K" + std::to_string(k) + " " + std::to_string(uint32_t(key[4 * k]) | (uint32_t(key[4 * k + 1]) << 8) | (uint32_t(key[4 * k + 2]) << 16) | (uint32_t(key[4 * k + 3]) << 24)) + "u\n";
    for (uint32_t bk = 0; bk < 4; ++bk) {
        s += "__device__ __forceinline__ void ih_r16_prog_b" + std::to_string(bk) + "(unsigned int (&R)[56]) {\n";
        for (uint32_t j = bk; j < n; j += 4) {
            const uint8_t* q = ops + 8 * j;
            if ((q[1] / 56) != bk || (q[2] / 56) != bk || (q[3] / 56) != bk) throw std::runtime_error("igneumhash: a version 16 operation crosses its bank (the quad form needs the bank rule)");
            const std::string a = "R[" + std::to_string(q[2] % 56) + "]", b = "R[" + std::to_string(q[3] % 56) + "]", d = "R[" + std::to_string(q[1] % 56) + "]";
            const uint32_t imm = uint32_t(q[4]) | (uint32_t(q[5]) << 8) | (uint32_t(q[6]) << 16) | (uint32_t(q[7]) << 24);
            const std::string I = std::to_string(imm) + "u";
            s += "    { const unsigned int a = " + a + ", b = " + b + "; ";
            switch (q[0]) {
                case 0: s += d + " = a * b + " + I + ";"; break;
                case 1: s += d + " = a + b + " + I + ";"; break;
                case 2: s += d + " = a - b - " + I + ";"; break;
                case 3: s += d + " = a ^ b ^ " + I + ";"; break;
                case 4: case 5: {
                    const uint32_t r = q[0] == 4 ? (imm & 31) : ((32 - (imm & 31)) & 31);
                    s += d + " = " + (r ? "((a << " + std::to_string(r) + "u) | (a >> " + std::to_string(32 - r) + "u))" : std::string("a")) + " ^ b;"; break;
                }
                case 6: s += d + " = a * (b | 1u) + " + I + ";"; break;
            }
            s += " }\n";
        }
        s += "}\n";
    }
    return s;
}

std::string ih_r0d_source(const uint8_t* ops, uint32_t n) {
    std::string s;
    // the text lands before the kernel file's typedefs, so the builtin unsigned int stands for the kernel's uint32_t
    s += "__device__ __forceinline__ unsigned int ih_r0d_m(unsigned int x) { return (x & 0x3FFFFFFFu) | 0x20000000u; }\n";
    s += "__device__ __forceinline__ float ih_r0d_f(unsigned int x) { return __uint_as_float((x & 0x3FFFFFFFu) | 0x20000000u); }\n";
    s += "__device__ __forceinline__ void ih_r0d_lane(unsigned int (&R)[16]) {\n";
    for (uint32_t i = 0; i < n; ++i) {
        const uint8_t op = ops[8 * i], d = ops[8 * i + 1] & 15, a = ops[8 * i + 2] & 15, b = ops[8 * i + 3] & 15;
        const uint32_t imm = uint32_t(ops[8 * i + 4]) | (uint32_t(ops[8 * i + 5]) << 8) | (uint32_t(ops[8 * i + 6]) << 16) | (uint32_t(ops[8 * i + 7]) << 24);
        const std::string D = "R[" + std::to_string(d) + "]", A = "R[" + std::to_string(a) + "]", B = "R[" + std::to_string(b) + "]";
        switch (op) {
            case 0: s += "    " + D + " = __float_as_uint(__fmaf_rn(ih_r0d_f(" + A + "), ih_r0d_f(" + B + "), ih_r0d_f(" + D + ")));\n"; break;
            case 1: s += "    " + D + " = __float_as_uint(__fadd_rn(ih_r0d_f(" + A + "), ih_r0d_f(" + B + ")));\n"; break;
            case 2: s += "    " + D + " = " + A + " ^ " + B + ";\n"; break;
            case 3: s += "    " + D + " = __funnelshift_l(" + A + ", " + A + ", " + std::to_string(imm & 31u) + "u);\n"; break;
            case 4: s += "    " + D + " = " + A + " + " + B + ";\n"; break;
            case 5: s += "    " + D + " = __umulhi(" + A + ", " + B + ");\n"; break;
            case 6: s += "    " + D + " = " + A + " ^ ((unsigned int)__popc(" + B + ") * 0x9E3779B1u);\n"; break;
            default: s += "    " + D + " = " + A + " + ((unsigned int)(" + B + " ? __clz(" + B + ") : 32) * 0x85EBCA77u);\n"; break;
        }
        if ((i & 31u) == 31u) s += "#pragma unroll\n    for (int k = 0; k < 16; ++k) R[k] = ih_r0d_m(R[k]);\n";
    }
    s += "}\n";
    return s;
}

// a number from a flat JSON document by key (the params document carries only numbers)
uint64_t json_num(const std::string& j, const char* key, uint64_t dflt) {
    const std::string k = std::string("\"") + key + "\"";
    size_t p = j.find(k);
    if (p == std::string::npos) return dflt;
    p = j.find(':', p);
    if (p == std::string::npos) return dflt;
    return std::strtoull(j.c_str() + p + 1, nullptr, 10);
}

uint64_t fnv64(const std::string& s) {
    uint64_t h = 0xcbf29ce484222325ull;
    for (unsigned char c : s) { h ^= c; h *= 0x100000001b3ull; }
    return h;
}

std::string hex8(uint64_t v) {
    char b[32];
    std::snprintf(b, sizeof b, "%016llx", (unsigned long long)v);
    return b;
}

bool file_read(const std::string& path, std::string& out) {
    std::ifstream in(path, std::ios::binary);
    if (!in) return false;
    std::ostringstream ss;
    ss << in.rdbuf();
    out = ss.str();
    return true;
}

void file_write(const std::string& path, const std::string& data) {
    std::ofstream o(path + ".tmp", std::ios::binary);
    o.write(data.data(), (std::streamsize)data.size());
    o.close();
    std::rename((path + ".tmp").c_str(), path.c_str());
}

}  // namespace

engine::engine() = default;

engine::~engine() {
    for (auto& a : fg3_ahead_) if (a.second.second.valid()) a.second.second.wait();   // the compile threads finish before the engine they read goes (a heap fault at exit otherwise)
    fg3_ahead_.clear();
    if (d_params_ && ctx_) wipe_secret();   // the seal verb's custody: the secret's words zeroed before the buffer is freed
    release_dataset();
    if (d_seed_) cuMemFree(d_seed_);
    if (d_env_) cuMemFree(d_env_);
    if (d_scratch_) cuMemFree(d_scratch_);
    if (d_r0e_st_) cuMemFree(d_r0e_st_);
    if (d_r0e_st_seed_) cuMemFree(d_r0e_st_seed_);
    if (d_params_) cuMemFree(d_params_);
    if (d_results_) cuMemFree(d_results_);
    for (auto& m : mods_) cuModuleUnload(m.mod);
    if (static_mod_) cuModuleUnload(static_mod_);
    if (ctx_) cuCtxDestroy(ctx_);
}

void engine::check(CUresult r, const char* what) {
    if (r == CUDA_SUCCESS) return;
    const char* s = nullptr;
    cuGetErrorString(r, &s);
    throw std::runtime_error(std::string(what) + ": " + (s ? s : "cuda error"));
}

int engine::device_count() {
    try { IGN_DYN_LOAD(); } catch (...) { return 0; }
    if (cuInit(0) != CUDA_SUCCESS) return 0;
    int n = 0;
    if (cuDeviceGetCount(&n) != CUDA_SUCCESS) return 0;
    return n;
}

int engine::find_device(const std::string& name_part) {
    const int n = device_count();
    std::string want = name_part;
    for (auto& c : want) c = static_cast<char>(std::tolower(c));
    for (int i = 0; i < n; ++i) {
        CUdevice d;
        if (cuDeviceGet(&d, i) != CUDA_SUCCESS) continue;
        char name[256] = {0};
        if (cuDeviceGetName(name, sizeof name, d) != CUDA_SUCCESS) continue;
        std::string s(name);
        for (auto& c : s) c = static_cast<char>(std::tolower(c));
        if (s.find(want) != std::string::npos) return i;
    }
    return -1;
}

void engine::init(int ordinal, const std::string& algo, uint64_t margin_bytes, const std::string& params_json, bool defer_static) {
    algo_ = algo;
    static_deferred_ = defer_static;
    params_ = params_json;
    margin_ = margin_bytes;
    IGN_DYN_LOAD();
    check(cuInit(0), "cuInit");
    check(cuDeviceGet(&dev_, ordinal), "cuDeviceGet");
    char name[256] = {0};
    check(cuDeviceGetName(name, sizeof name, dev_), "cuDeviceGetName");
    info_.ordinal = ordinal;
    info_.name = name;
    check(cuDeviceGetAttribute(&info_.cc_major, CU_DEVICE_ATTRIBUTE_COMPUTE_CAPABILITY_MAJOR, dev_), "cc major");
    check(cuDeviceGetAttribute(&info_.cc_minor, CU_DEVICE_ATTRIBUTE_COMPUTE_CAPABILITY_MINOR, dev_), "cc minor");
    size_t total = 0;
    check(cuDeviceTotalMem(&total, dev_), "cuDeviceTotalMem");
    info_.total_mem = total;
    char pci[64] = {0};
    check(cuDeviceGetPCIBusId(pci, sizeof pci, dev_), "pci bus id");
    info_.pci = pci;
    check(cuDriverGetVersion(&info_.driver_version), "driver version");
    cuDeviceGetAttribute(&sm_count_, CU_DEVICE_ATTRIBUTE_MULTIPROCESSOR_COUNT, dev_);
    check(cuCtxCreate(&ctx_, 0, dev_), "cuCtxCreate");

    const char* cd = std::getenv("IGNEUM_WORKER_CACHE");
    if (cd) cache_dir_ = cd;
    else {
        const char* home = std::getenv("HOME");
        cache_dir_ = std::string(home ? home : ".") + "/.cache/igneum-worker";
        ign_mkdir((std::string(home ? home : ".") + "/.cache").c_str());
    }
    ign_mkdir(cache_dir_.c_str());

    // NVRTC architecture choice: a cubin for the exact SM when NVRTC knows it,
    // else PTX for the highest known architecture below it (the stable
    // fallback: it costs a JIT at load and some tuning, never correctness).
    int n_arch = 0;
    nvrtcGetNumSupportedArchs(&n_arch);
    std::vector<int> archs(n_arch);
    if (n_arch) nvrtcGetSupportedArchs(archs.data());
    const int want = info_.cc_major * 10 + info_.cc_minor;
    int best = 0;
    bool exact = false;
    for (int a : archs) { if (a == want) exact = true; if (a <= want && a > best) best = a; }
    if (exact) { info_.arch_option = "-arch=sm_" + std::to_string(want); info_.ptx_fallback = false; }
    else if (best) { info_.arch_option = "-arch=compute_" + std::to_string(best); info_.ptx_fallback = true; }
    else throw std::runtime_error("no NVRTC architecture at or below sm_" + std::to_string(want));

    // NVML by PCI bus id (telemetry is diagnostic only).
    if (IGN_NVML_PRESENT() && nvmlInit_v2() == NVML_SUCCESS) {
        if (nvmlDeviceGetHandleByPciBusId_v2(info_.pci.c_str(), &nvml_) == NVML_SUCCESS) nvml_ok_ = true;
    }
    if (nvml_ok_) {
        unsigned t = 0;
        if (nvmlDeviceGetTemperatureThreshold(nvml_, NVML_TEMPERATURE_THRESHOLD_SLOWDOWN, &t) == NVML_SUCCESS) info_.slowdown_c = t;
    }
    int integrated = 0;
    if (cuDeviceGetAttribute(&integrated, CU_DEVICE_ATTRIBUTE_INTEGRATED, dev_) == CUDA_SUCCESS) info_.integrated = integrated != 0;

    check(cuMemAlloc(&d_params_, kParamsWords * sizeof(uint32_t)), "alloc params");   // header 8, target 8, the bond secret 8 (version 8 bit 10)
    check(cuMemAlloc(&d_results_, (1 + kMaxResults * kResultWords + 1) * sizeof(uint32_t)), "alloc results");  // + the device-side nonce counter (family 3);
    // protocol mode defers the static module (the default candidate's kernels, compiled by NVRTC) past the hello: Ember waits
    // ten seconds for the hello and the compile plus the self-test took 13 s on an A2000 (10 Oct 2026 21:5x UK); the first
    // prepare loads it, and a configure naming another candidate rebuilds the engine anyway
    if (!static_deferred_) load_static_module();
}

// version 8 option (b)'s floating form on the device for the fp class (SPEC 0.8 bit 4): out = bits(round(round(y' * z') + x')) ^ x
// with __fmul_rn and __fadd_rn (no contraction), the same text as the kernel's ih_v8_fop; the rows checked against fp.json
void engine::fp_check(const std::vector<uint32_t>& xyz, std::vector<uint32_t>& out, bool wide) {
    static const char* src =
        "typedef unsigned int uint32_t;\n"
        "extern \"C\" __global__ void fp_rows(const uint32_t* __restrict__ xyz, uint32_t* __restrict__ out, uint32_t n) {\n"
        "  const uint32_t i = blockIdx.x * blockDim.x + threadIdx.x; if (i >= n) return;\n"
        "  const uint32_t x = xyz[3 * i], y = xyz[3 * i + 1], z = xyz[3 * i + 2];\n"
        "  const uint32_t yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u, xb = (x & 0x3FFFFFFFu) | 0x20000000u;\n"
        "  const float prod = __fmul_rn(__uint_as_float(yb), __uint_as_float(zb));\n"
        "  const float sum = __fadd_rn(prod, __uint_as_float(xb));\n"
        "  out[i] = __float_as_uint(sum) ^ x; }\n"
        "__device__ __forceinline__ float mk(uint32_t v) { return __uint_as_float((v & 0x3FFFFFFFu) | 0x20000000u); }\n"
        "extern \"C\" __global__ void fp_wide(const uint32_t* __restrict__ oxyz, uint32_t* __restrict__ out, uint32_t n) {\n"
        "  const uint32_t i = blockIdx.x * blockDim.x + threadIdx.x; if (i >= n) return;\n"
        "  const uint32_t op = oxyz[4 * i], x = oxyz[4 * i + 1], y = oxyz[4 * i + 2], z = oxyz[4 * i + 3];\n"
        "  const float xf = mk(x), yf = mk(y), zf = mk(z);\n"
        "  const float f0 = __fadd_rn(xf, __fsub_rn(yf, zf)), f1 = __fsub_rn(xf, __fadd_rn(yf, zf)), f2 = __fadd_rn(__fmul_rn(yf, zf), xf), f3 = __fadd_rn(__fsub_rn(xf, yf), zf);\n"
        "  const float f = op == 0u ? f0 : (op == 1u ? f1 : (op == 2u ? f2 : f3));\n"
        "  out[i] = __float_as_uint(f) ^ x; }\n";
    bool ptx = false;
    const std::string image = compile(src, "fp-rows", ptx);
    CUmodule mod = nullptr; CUfunction f = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load fp-rows");
    check(cuModuleGetFunction(&f, mod, wide ? "fp_wide" : "fp_rows"), "fp rows");
    const uint32_t n = static_cast<uint32_t>(xyz.size() / (wide ? 4 : 3));
    CUdeviceptr din = 0, dout = 0;
    check(cuMemAlloc(&din, xyz.size() * 4), "fp in"); check(cuMemAlloc(&dout, n * 4 + 4), "fp out");
    check(cuMemcpyHtoD(din, xyz.data(), xyz.size() * 4), "fp upload");
    void* args[] = {&din, &dout, (void*)&n};
    check(cuLaunchKernel(f, (n + 255) / 256, 1, 1, 256, 1, 1, 0, nullptr, args, nullptr), "fp launch");
    check(cuCtxSynchronize(), "fp sync");
    out.resize(n); check(cuMemcpyDtoH(out.data(), dout, n * 4), "fp download");
    cuMemFree(din); cuMemFree(dout); cuModuleUnload(mod);
}

void engine::grind_read(std::vector<uint32_t>& out) {
    out.assign(grind_words_, 0u);
    if (d_scratch_ && grind_words_) check(cuMemcpyDtoH(out.data(), d_scratch_, grind_words_ * 4), "grind download");
}

void engine::precompile_arch(int sm) {
    const std::string saved = info_.arch_option; const bool saved_ptx = info_.ptx_fallback;
    info_.arch_option = "-arch=sm_" + std::to_string(sm); info_.ptx_fallback = false; compile_only_ = true;
    try { ih_load(ih_cell_); } catch (...) { compile_only_ = false; info_.arch_option = saved; info_.ptx_fallback = saved_ptx; throw; }
    compile_only_ = false; info_.arch_option = saved; info_.ptx_fallback = saved_ptx;
}

std::string engine::compile(const std::string& src, const std::string& key, bool& used_ptx) {
    int major = 0, minor = 0;
    nvrtcVersion(&major, &minor);
    // tune jit=N (the compile-time study, the GPU team 15 Oct 2026): NVRTC stops at PTX (-arch=compute_XX) and the driver's ptxas assembles it at
    // level N on load (cuModuleLoadDataEx, CU_JIT_OPTIMIZATION_LEVEL): the front end's and the assembler's shares of the period compile
    const long jit = tune("jit", -1); const bool ptx_out = info_.ptx_fallback || jit >= 0;
    std::string arch = info_.arch_option; if (jit >= 0 && arch.find("sm_") != std::string::npos) arch.replace(arch.find("sm_"), 3, "compute_");
    const std::string opts_key = arch + " nvrtc " + std::to_string(major) + "." + std::to_string(minor) + " --fmad=false" + (jit >= 0 ? " ptx" : "");   // F-66: the option set is part of the cache key
    const std::string file = cache_dir_ + "/" + key + "-" + hex8(fnv64(src) ^ fnv64(opts_key)) + (ptx_out ? ".ptx" : ".cubin");
    used_ptx = ptx_out;
    std::string image;
    if (file_read(file, image) && !image.empty()) return image;

    // the handshake's progress (the product team, 12 Oct 2026: Ember reads a worker that says nothing for 120 s as dead): a line on stderr
    // before and after the compile; F-66 (the security review): no fast math, no contraction (--fmad=false asserted; the FMA is the fused
    // intrinsic in the kernel text), the option set recorded in the build notes
    std::fprintf(stderr, "compiling the program for %s (nvrtc %d.%d, key %s, --fmad=false, no fast math)\n", info_.arch_option.c_str(), major, minor, key.c_str());
    const auto tc0 = std::chrono::steady_clock::now();
    if (const char* dd = std::getenv("IGNEUM_SRC_DUMP")) file_write(std::string(dd) + "/" + key + ".cu", src);   // a debugging form: the module's text as compiled
    nvrtcProgram prog;
    if (nvrtcCreateProgram(&prog, src.c_str(), "igneum.cu", 0, nullptr, nullptr) != NVRTC_SUCCESS)
        throw std::runtime_error("nvrtcCreateProgram");
    const char* opts[] = {arch.c_str(), "-std=c++17", "-default-device", "--fmad=false"};
    nvrtcResult r = nvrtcCompileProgram(prog, 4, opts);
    std::fprintf(stderr, "compiled the program for %s in %.1f s\n", info_.arch_option.c_str(), std::chrono::duration<double>(std::chrono::steady_clock::now() - tc0).count());
    size_t log_size = 0;
    nvrtcGetProgramLogSize(prog, &log_size);
    std::string log(log_size, '\0');
    if (log_size) nvrtcGetProgramLog(prog, &log[0]);
    if (r != NVRTC_SUCCESS) {
        nvrtcDestroyProgram(&prog);
        throw std::runtime_error("nvrtc compile failed:\n" + log);
    }
    size_t size = 0;
    if (ptx_out) {
        nvrtcGetPTXSize(prog, &size);
        image.resize(size);
        nvrtcGetPTX(prog, &image[0]);
    } else {
        nvrtcGetCUBINSize(prog, &size);
        image.resize(size);
        nvrtcGetCUBIN(prog, &image[0]);
    }
    nvrtcDestroyProgram(&prog);
    file_write(file, image);
    { std::lock_guard<std::mutex> lk(notes_mu_); notes_ += "compiled " + key + " (" + info_.arch_option + " -std=c++17 -default-device --fmad=false; no fast math; the FMA the fused intrinsic)\n"; }   // F-66: the options on the record (the compile thread appends too)
    return image;
}

void engine::load_static_module() {
    bool ptx = false;
    al_ = algo_ == IGNPOW_ALGO_AUTOLYKOS_2;
    if (al_) {
        const std::string image = compile(device_source_autolykos(), "autolykos", ptx);
        check(cuModuleLoadData(&static_mod_, image.data()), "load autolykos module");
        check(cuModuleGetFunction(&f_al_gen_, static_mod_, "al_table_gen"), "al_table_gen"); record_kernel(f_al_gen_, "al_table_gen");
        check(cuModuleGetFunction(&f_al_search_, static_mod_, "al_search"), "al_search"); record_kernel(f_al_search_, "al_search");
        // M: the 1024 big-endian longs 0..1023, into the module's constant memory
        std::vector<uint8_t> M(8192);
        for (uint64_t i = 0; i < 1024; ++i)
            for (int b = 0; b < 8; ++b) M[i * 8 + b] = static_cast<uint8_t>(i >> (8 * (7 - b)));
        CUdeviceptr d_M = 0;
        size_t sz = 0;
        check(cuModuleGetGlobal(&d_M, &sz, static_mod_, "c_M"), "c_M");
        check(cuMemcpyHtoD(d_M, M.data(), 8192), "upload M");
        return;
    }
    fh_ = algo_ == IGNPOW_ALGO_FISHHASH_1;
    rx_ = algo_ == IGNPOW_ALGO_RXE1;
    if (rx_) {
        if (rxe1_params_parse(params(), &rxp_) != IGNPOW_OK) throw std::runtime_error("rxe1: the reference refused the params document");
        rxe1_pid(&rxp_, rx_pid_);
        ih_items_ = rxp_.dataset_bytes / 128;
        ih_rounds_ = rxp_.rounds;
        ih_scratch_words_ = 0;
        rx_load();
        check(cuMemAlloc(&d_seed_, 32), "alloc seed");
        check(cuMemAlloc(&d_env_, 80), "alloc rx context");
        return;
    }
    ih_ = algo_ == "igneumhash";
    if (ih_) {
        // the variant from the reference's own parser (defaults by version), never a second reading of the document
        if (ignh_params_parse(params(), &ihp_) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the params document");
        { igm::IhParams wp; std::string werr; ih_keyed_steps_ = 0; if (igm::ih_parse_params(params(), wp, werr) && wp.version >= 20 && (wp.features & 1024u)) ih_keyed_steps_ = wp.keyed_steps; }   // bit 10 on 21 and 25: the keyed-read steps (cb73f3b33 section 2); 0 for every unkeyed document
        ih_rounds_ = ihp_.rounds;
        ih_lanes_ = ihp_.lanes;
        if (ih_lanes_ != 8 && ihp_.version != 20 && ihp_.version != 21 && ihp_.version != 24 && ihp_.version != 25) throw std::runtime_error("igneumhash: lanes must be 8");
        if (ihp_.version == 20 || ihp_.version == 21 || ihp_.version == 24 || ihp_.version == 25) ih_lanes_ = 32;   // forge (pow/ref namespace forge): one attempt per warp, lanes 0..15 the 16 program lanes (16..31 idle)
        if (ihp_.version == 14 && ihp_.tile_layout == 1) ih_lanes_ = 32;
        if (ihp_.version == 16 && tune("quad", 0)) ih_lanes_ = 4;   // version 16's quad form: four threads (in four warps) an attempt
        if (ihp_.version == 16 && !tune("quad", 0)) ih_lanes_ = 1;
        if (ihp_.version == 17 || ihp_.version == 18) ih_lanes_ = 1;   // version 17 (the texture form): one attempt per thread, no dataset   // the one-thread form   // version 14.1: one octet per warp (the quad-replicated form): 32 physical lanes per nonce
        // the wider state (version 8 REGISTERS 32 or 64, SPEC-R0A-v0.md 0.8): the reference widens the lane state to RW registers
        // and this worker's kernel keeps the 16-register state, so the digest would be wrong on a document it accepted (the
        // ladder's pre-gate read MISMATCH 96 of 96 on b1-x32 and b1-x64, 11 Oct 2026 09:38 UK); rejected for the gate relation
        // (K 16 and K 8), so the document is REFUSED here with its reason, never computed
        if (ihp_.version == 8 && ihp_.registers > 16) throw std::runtime_error("igneumhash: registers over 16 (the wider state, K 32 and K 64) are not laid out in this worker; the gate relation is K 16 or K 8: refused");
        ih_scratch_words_ = (ihp_.version == 20 || ihp_.version == 21 || ihp_.version == 24 || ihp_.version == 25) ? 0 : ihp_.scratch_bytes / 4;   // version 25's per-hash scratchpad lives in the fg3 search kernel's shared memory  // words per nonce (0: no scratchpad); forge keeps its 1 KiB-a-lane scratchpad inside the kernel (local memory), never the generic slice, so IH_PAD stays 0 and no per-slot scratch is allocated
        int st = 0;
        // version 3 (the item-width record, SPEC-R0A-v0.md 0.3): item_bytes from the reference's own parse (128 under the other versions)
        ih_item_bytes_ = (ihp_.version == 17 || ihp_.version == 18) ? 128 : (ihp_.item_bytes ? ihp_.item_bytes : 128);   // version 17 reads no dataset: the item width is moot (128 keeps the common paths well-formed)
        if (ih_item_bytes_ != 32 && ih_item_bytes_ != 64 && ih_item_bytes_ != 128 && ih_item_bytes_ != 256 && ih_item_bytes_ != 512 && ih_item_bytes_ != 1024) throw std::runtime_error("igneumhash: item_bytes must be 32, 64, 128, 256, 512 or 1024");
        if (ih_item_bytes_ == 1024 && ihp_.version != 4 && ihp_.version != 6) throw std::runtime_error("igneumhash: a 1024-byte item is version 4 or 6 only");
        if (ih_item_bytes_ < 128 && ihp_.version != 4 && ihp_.version != 6) throw std::runtime_error("igneumhash: a 32- or 64-byte item is version 4 or 6 only");
        ih_items_ = ((ihp_.version == 16 && !(ihp_.program_distance & 8388608)) || ihp_.version == 17 || ihp_.version == 18) ? 0 : ignpow_dataset_bytes(algo_.c_str(), params(), 0, &st) / ih_item_bytes_;
        if (ihp_.version != 16 && ihp_.version != 17 && ihp_.version != 18 && (st != IGNPOW_OK || ih_items_ == 0)) throw std::runtime_error("igneumhash: the reference refused the params document");   // versions 16 (static fabric) and 17 (texture) carry no dataset
        if (ih_items_ != ihp_.dataset_bytes / ih_item_bytes_) throw std::runtime_error("igneumhash: the reference's dataset size disagrees with the params");
        if (ihp_.version != 5 && ihp_.version != 8 && (ih_items_ & (ih_items_ - 1))) throw std::runtime_error("igneumhash: the item count must be a power of two (versions 5 and 8 take any multiple of 16 MiB)");
        if (ih_items_ > 0xFFFFFFFFull) throw std::runtime_error("igneumhash: the item count is outside the kernel's 32-bit index");
        if (ihp_.version >= 1 && ihp_.version != 16 && ihp_.version != 17 && ihp_.version != 18 && (ihp_.cache_bytes == 0 || ihp_.cache_bytes % 128 || ihp_.cache_bytes / 128 > 0xFFFFFFFFull))
            throw std::runtime_error("igneumhash: the cache size is outside the kernel's range");
        // the envelope: the canonical params bytes (36, 48 or 52; section 0), then C (u64be) and V (u32be)
        uint8_t env[IGNH_PARAMS_BYTES + 12];
        ignh_params_bytes(&ihp_, env);
        const size_t plen = ignh_params_len(&ihp_);
        for (int i = 0; i < 8; ++i) env[plen + i] = static_cast<uint8_t>(ihp_.chain_id >> (8 * (7 - i)));
        for (int i = 0; i < 4; ++i) env[plen + 8 + i] = static_cast<uint8_t>(ihp_.version >> (8 * (3 - i)));
        ih_load(0);  // version 2 reloads at prepare when the epoch's cell differs
        check(cuMemAlloc(&d_seed_, 32), "alloc seed");
        check(cuMemAlloc(&d_env_, plen + 12 + 32), "alloc envelope");   // + the epoch seed E (32 bytes) for version 8 bit 9, written at each prepare
        check(cuMemcpyHtoD(d_env_, env, plen + 12), "upload envelope");
        env_len_ = plen + 12;
        return;
    }
    pe_ = algo_ == IGNPOW_ALGO_PROGPOW_EPIC;
    const std::string image = compile(fh_ ? device_source_fishhash() : device_source(0, pe_), fh_ ? "fishhash" : (pe_ ? "static-epic" : "static"), ptx);
    check(cuModuleLoadData(&static_mod_, image.data()), "load static module");
    check(cuModuleGetFunction(&f_dag_gen_, static_mod_, "dag_gen"), "dag_gen"); record_kernel(f_dag_gen_, "dag_gen");
    if (fh_) check(cuModuleGetFunction(&f_fh_search_, static_mod_, "fishhash_search"), "fishhash_search"); record_kernel(f_fh_search_, "fishhash_search");
}

CUmodule engine::module_for_period(uint64_t period) {
    for (auto& m : mods_) if (m.period == period) return m.mod;
    bool ptx = false;
    const std::string image = compile(device_source(period, pe_), (pe_ ? "epic-period-" : "period-") + std::to_string(period), ptx);
    period_mod pm{period, nullptr, nullptr};
    check(cuModuleLoadData(&pm.mod, image.data()), "load period module");
    check(cuModuleGetFunction(&pm.fn, pm.mod, "progpow_search"), "progpow_search"); record_kernel(pm.fn, "progpow_search");
    if (mods_.size() >= 3) { cuModuleUnload(mods_.front().mod); mods_.erase(mods_.begin()); }
    mods_.push_back(pm);
    return pm.mod;
}

uint64_t engine::free_bytes() {
    size_t f = 0, t = 0;
    check(cuMemGetInfo(&f, &t), "cuMemGetInfo");
    return f;
}

budget engine::budget_for(uint64_t height) {
    make_current();
    budget b;
    int st = 0;
    b.cache = ignpow_cache_bytes(algo_.c_str(), params(), height, &st);
    b.dataset = ignpow_dataset_bytes(algo_.c_str(), params(), height, &st);
    b.scratch = (16 + 1 + kMaxResults * kResultWords) * sizeof(uint32_t) + (64ull << 20);  // params, results, modules and runtime
    if (ih_ && ih_scratch_words_) b.scratch += static_cast<uint64_t>(ih_slots_) * ih_scratch_words_ * 4;  // the scratchpads of the lane groups in flight
    if (ih_ && ihp_.version >= 10 && ihp_.version <= 14 && !tune("r0eshared", 0)) b.scratch += static_cast<uint64_t>(ih_slots_) * tune("r0etable", ihp_.version >= 12 ? ihp_.table_kib * 256u : ihp_.version == 11 ? 1024 : 4096) * 4;   // version 10: the per-nonce gather tables of the groups in flight (16 KB each)
    b.margin = margin_;
    size_t f = 0, t = 0;
    check(cuMemGetInfo(&f, &t), "cuMemGetInfo");
    b.total = t;
    b.free_before = f;
    b.fit_cold = b.cache + b.dataset + b.scratch + b.margin;
    b.fit_warm = 2 * (b.cache + b.dataset) + b.scratch + b.margin;
    // free_before counts the current dataset as used; a cold transition frees it first.
    const uint64_t free_after_release = f + cache_bytes_ + dag_bytes_;
    b.can_cold = b.fit_cold <= free_after_release;
    b.can_warm = b.fit_warm <= free_after_release;
    return b;
}

// The igneumhash module for the variant and the round-arithmetic cell (SPEC-R0B-schedule-v0.md section 2;
// cell 0 is the versions 0 and 1 arithmetic). The compile key names everything the source depends on.
uint64_t engine::tune(const char* key, uint64_t dflt) const {
    size_t pos = 0;
    while (pos < tune_.size()) {
        size_t end = tune_.find(',', pos); if (end == std::string::npos) end = tune_.size();
        const std::string kv = tune_.substr(pos, end - pos);
        const size_t eq = kv.find('=');
        if (eq != std::string::npos && kv.substr(0, eq) == key) {
            const std::string v = kv.substr(eq + 1);
            if (v == "fused") return 512;       // the Riptide fused form (loads=512)
            if (v == "split") return 128;       // the Riptide split form: W / 128 pipelined 128-byte reads (loads=128)
            if (v == "overfetch") return 256;   // the Turing overfetch form at W = 128
            return std::strtoull(v.c_str(), nullptr, 10);
        }
        pos = end + 1;
    }
    return dflt;
}

// The register cap of the ladder's family 2: --tune regs=N asks for a cap of N registers per thread and is realised through
// __launch_bounds__(128, minblocks) with minblocks = 65536 / (128 N) (the NVRTC -maxrregcount option was accepted and ignored
// on sm_120, 10 Oct 2026: regs=64 compiled to 120 registers; minblocks=8 compiled to 64); an explicit minblocks wins.
uint64_t engine::ih_minblocks() const {
    const uint64_t mb = tune("minblocks", 0);
    if (mb) return mb;
    const uint64_t regs = tune("regs", 0);
    return regs ? (65536 / (128 * regs)) : 0;
}

void engine::ih_load(uint32_t cell) {
    struct cell_t { uint32_t r1, r2; int op1_add, op2_xor; };
    static const cell_t kCells[8] = {{7, 17, 0, 0}, {11, 23, 1, 1}, {13, 5, 0, 1}, {19, 29, 1, 0}, {3, 13, 0, 0}, {23, 7, 1, 1}, {29, 11, 0, 1}, {5, 19, 1, 0}};
    const cell_t c = kCells[cell & 7];
    const uint32_t slice_words = ih_scratch_words_ / 8;
    const uint64_t n_cache = ihp_.version >= 1 ? ihp_.cache_bytes / 128 : 0;
    auto def = [](const char* name, uint64_t v) { return std::string("#define ") + name + " " + std::to_string(v) + "u\n"; };
    // version 9: the period's programme drawn by the reference's own rule and compiled in (the kernel runs version 8's round with
    // bit 13 set; the module is rebuilt when the period changes, see prepare)
    std::string r0d;
    const bool v9 = ihp_.version >= 9 && ihp_.version <= 14;
    const bool v15 = ihp_.version == 15;   // the bandwidth-first class: the module carries the period's key and programme (ih_r16_source) on version 4's dataset paths
    const bool v17 = ihp_.version == 17;   // the texture form (pow/ref r17): one attempt per thread, the period key K, the static texture sampled through the texture unit
    if (v17) {
        if (ih_r0d_period_ == ~0ull) { ih_r0d_period_ = 0; ih_r0d_height_ = 0; }
        uint8_t key[32];
        if (ignh_r17_key(&ihp_, ih_r0d_height_, key) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the version 17 period key");
        for (int k = 0; k < 8; ++k) r0d += "#define IH_R16_K" + std::to_string(k) + " " + std::to_string(uint32_t(key[4 * k]) | (uint32_t(key[4 * k + 1]) << 8) | (uint32_t(key[4 * k + 2]) << 16) | (uint32_t(key[4 * k + 3]) << 24)) + "u\n";
    }
    const bool v18 = ihp_.version == 18;   // the raster form (pow/ref r18): one attempt per thread, the period key K, the software rasteriser
    if (v18) {
        if (ih_r0d_period_ == ~0ull) { ih_r0d_period_ = 0; ih_r0d_height_ = 0; }
        uint8_t key[32];
        if (ignh_r18_key(&ihp_, ih_r0d_height_, key) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the version 18 period key");
        for (int k = 0; k < 8; ++k) r0d += "#define IH_R16_K" + std::to_string(k) + " " + std::to_string(uint32_t(key[4 * k]) | (uint32_t(key[4 * k + 1]) << 8) | (uint32_t(key[4 * k + 2]) << 16) | (uint32_t(key[4 * k + 3]) << 24)) + "u\n";
    }
    const bool v16 = ihp_.version == 16;   // the static-fabric integer coordinate (pow/ref r16f): one attempt per thread over 224 static registers, the period's programme (rounds x 224 ops) compiled in; no dataset
    const bool v21 = ihp_.version == 21 || ihp_.version == 24 || ihp_.version == 25;   // version 25 (oldcard, entry 2): the fg3 form alone (the scratch steps are generated there)   // version 24 (base): version 21's engine with the per-hash draws off   // Anvil, forge2 (pow/ref namespace forge2): the two-level form, the batch's shapes and the hash's sequence over them
    const bool v20 = ihp_.version == 20 || v21;   // forge (pow/ref namespace forge): one attempt per warp, the per-hash programme interpreter with static register operands
    if (v16) {
        if (ih_r0d_period_ == ~0ull) { ih_r0d_period_ = 0; ih_r0d_height_ = 0; }
        // the queue, chains, lanes, tail, words and trace tunes are version 4's forms and have no meaning here: IGNORED (Ember configures every igneumhash worker with its defaults; a refusal closed the card, 16:39 UK 13 Oct)
        const uint32_t nregs = ihp_.registers ? ihp_.registers : 224u;   // the document's "registers" (revisions 12/13: 144 or 160; 0 = 224)
        const bool stream = (ihp_.program_distance & 32768) != 0;   // revision 15 (the streaming fabric) and 15b: rounds x W operations, W = registers / 4
        const uint32_t wform = (ihp_.program_distance >> 18) & 3;   // 2 the braid (revision 17), 3 the segment pool (revision 18: 64 segments x 100 blocks compiled, 8 run an attempt)
        const size_t n_ops = wform == 3 ? size_t(6400) * (nregs / 4) : (stream || wform) ? size_t(ihp_.rounds) * (nregs / 4) : size_t(ihp_.rounds) * (nregs + (((ihp_.program_distance & 508) && !(ihp_.program_distance & 3584)) ? 4 : 0));   // revisions 4 to 10 (bits 2 to 8) add four cross-bank XORs a pass; revision 11 (bit 9) none
        std::vector<uint8_t> ops(n_ops * 8), cs(n_ops); uint8_t key[32];
        const int n = ignh_r16_period_c(&ihp_, ih_r0d_height_, key, ops.data(), n_ops, cs.data());
        if (n < 0 || size_t(n) != n_ops) throw std::runtime_error("igneumhash: the reference refused the version 16 period draw");
        const uint32_t n_live = tune("ablate", 0) == 1 ? 0u : uint32_t(n);   // ablate=1: the programme off (the BLAKE3 tails alone), the fabric's cost by difference
        r0d = tune("quad", 0) ? ih_r16q_source(key, ops.data(), n_live) : ih_r16_source(key, ops.data(), n_live, nregs, cs.data(), wform == 3 ? 100u * (nregs / 4) : 0u, (ihp_.program_distance & 8388608) ? (8u << ((ihp_.program_distance >> 24) & 3)) : 0u, (ihp_.program_distance & 67108864) != 0);   // quad=0 (the DEFAULT since 18:3x UK 13 Oct: the better port, and the only revision 3 form): the one-thread form; quad=1 the four-warp form
    }
    if (v15) {
        if (ih_r0d_period_ == ~0ull) { ih_r0d_period_ = 0; ih_r0d_height_ = 0; }
        // the queue, chains, lanes, tail, words and trace tunes are version 4's forms and have no meaning here: IGNORED (Ember configures every igneumhash worker with its defaults; a refusal closed the card, 16:39 UK 13 Oct)
        std::vector<uint8_t> ops(size_t(ihp_.program_ops) * 8); uint8_t key[32];
        const int n = ignh_r16_period(&ihp_, ih_r0d_height_, key, ops.data(), ihp_.program_ops);
        if (n < 0 || uint32_t(n) != ihp_.program_ops) throw std::runtime_error("igneumhash: the reference refused the version 15 period draw");
        r0d = ih_r16_source(key, ops.data(), uint32_t(n));
        { static const char* hx = "0123456789abcdef"; std::string a, k; for (int i = 0; i < 32; ++i) { a += hx[ihp_.program_anchor[i] >> 4]; a += hx[ihp_.program_anchor[i] & 15]; } for (int i = 0; i < 8; ++i) { k += hx[key[i] >> 4]; k += hx[key[i] & 15]; }
          std::fprintf(stderr, "version 15 period %llu (height %llu, period_blocks %u) keyed on anchor %s: key %s..\n", (unsigned long long)ih_r0d_period_, (unsigned long long)ih_r0d_height_, ihp_.period_blocks, a.c_str(), k.c_str()); }
    }
    if (v9) {
        if (ih_r0d_period_ == ~0ull) { ih_r0d_period_ = 0; ih_r0d_height_ = 0; }
        std::vector<uint8_t> ops(size_t(ihp_.program_ops) * 8);
        const int n = ignh_r0d_program(&ihp_, ih_r0d_height_, ops.data(), ihp_.program_ops);
        if (n < 0 || uint32_t(n) != ihp_.program_ops) throw std::runtime_error("igneumhash: the reference refused the R0D programme draw");
        const uint32_t t_words = ihp_.version >= 12 ? ihp_.table_kib * 256u : ihp_.version == 11 ? 1024u : 4096u, aw = ihp_.version >= 12 ? ihp_.access_bytes / 4 : 0;
        if (ihp_.version == 14 && ihp_.tile_shape != 1) throw std::runtime_error("igneumhash: version 14 tile shapes 2 and 3 are not in this worker (the registered 16x8x32 only)");
        if (ihp_.version == 14 && ihp_.tile_layout > 1) throw std::runtime_error("igneumhash: version 14 tile_layout above 1 is not in this worker");
        if (ihp_.version == 14 && (ihp_.items_per_round > 1 || ihp_.registers_live > 16)) throw std::runtime_error("igneumhash: version 14 items_per_round and registers_live are not in this worker (the registered document has neither)");
                if (ihp_.version == 14 && tune("queue", 0)) throw std::runtime_error("igneumhash: version 14 and the queue tune do not combine (the tile is warp-collective)");
        if (ihp_.version >= 12 && ihp_.shared_kib && tune("r0eshared", 0)) throw std::runtime_error("igneumhash: the shared form and the r0eshared tune do not combine");
        ih_trace_ops_emit = tune("trace", 0) != 0; r0d = ihp_.version >= 10 ? ih_r0e_source(ops.data(), uint32_t(n), ih_item_bytes_ / 4, ihp_.version >= 11, uint32_t(tune("ablate", 0)), aw, aw ? t_words / aw : 0,
                                                 ihp_.version >= 13 ? ihp_.chains_per_nonce : 1u, ihp_.version >= 13 && ihp_.gather_lane_bank != 0, (ihp_.version >= 12 && ihp_.shared_kib && aw) ? ihp_.shared_kib * 256u / aw : 0u,
                                                 ihp_.version == 14 ? ihp_.tile_shape : 0u, ihp_.version == 14 && ihp_.fold_every > 1 ? ihp_.fold_every : 1u, ihp_.version == 14 && ihp_.layout_draw != 0, ihp_.version == 14 && ihp_.tile_layout == 1) : ih_r0d_source(ops.data(), uint32_t(n));
    }
    std::string defs = def("IH_VERSION", v9 ? 8 : (v15 || v16) ? 4 : ihp_.version) + def("IH_R16", v15 ? 1 : 0) + def("IH_R16_SPLIT", (v15 && tune("split15", 1)) ? 1 : 0) + def("IH_R0H", v17 ? 1 : 0) + def("IH_R0I", v18 ? 1 : 0) + def("IH_R17", (v16 && !tune("quad", 0)) ? 1 : 0) + def("IH_R17Q", (v16 && tune("quad", 0)) ? 1 : 0) + def("IH_R17_TAILS", (v16 && (ihp_.program_distance & 1)) ? 1 : 0) + def("IH_R17_REGS", v16 ? (ihp_.registers ? ihp_.registers : 224u) : 224u) + def("IH_R17_STREAM", (v16 && (ihp_.program_distance & 32768)) ? 1 : 0) + def("IH_R17_INPLACE", (v16 && ((ihp_.program_distance & 131072) || ((ihp_.program_distance >> 18) & 3))) ? 1 : 0) + def("IH_R17_POOL", (v16 && ((ihp_.program_distance >> 18) & 3) == 3) ? 1 : 0) + def("IH_R17_MEM", (v16 && (ihp_.program_distance & 8388608)) ? (8u << ((ihp_.program_distance >> 24) & 3)) : 0u) + def("IH_R0E", (ihp_.version >= 10 && ihp_.version <= 14) ? 1 : 0) + def("IH_TRACE", tune("trace", 0) ? 1 : 0) + def("IH_R0E_V11", (ihp_.version >= 11 && ihp_.version <= 14) ? 1 : 0) + def("IH_R0E_V12", (ihp_.version >= 12 && ihp_.version <= 14) ? 1 : 0) + def("IH_R0E_V14", ihp_.version == 14 ? 1 : 0) + def("IH_R0E_ST", (ihp_.version >= 12 && ihp_.shared_kib) ? 1 : 0) + def("IH_R0E_ST_WORDS", ihp_.version >= 12 ? ihp_.shared_kib * 256u : 0u) + def("IH_R0E_TABLE_WORDS", tune("r0etable", ihp_.version >= 12 ? ihp_.table_kib * 256u : ihp_.version == 11 ? 1024 : 4096)) + def("IH_R0E_SHARED", tune("r0eshared", 0)) + def("IH_N_ITEMS", tune("ablate", 0) == 6 ? ihp_.cache_bytes / ih_item_bytes_ : ih_items_) + def("IH_SLICE_WORDS", slice_words) + def("IH_ROUNDS", ih_rounds_) +
                       def("IH_ITEM_CHAIN", ihp_.item_chain) + def("IH_N_CACHE", n_cache) + def("IH_PARENTS", ihp_.parents) +
                       def("IH_PARAMS_LEN", ignh_params_len(&ihp_)) + def("IH_R1", c.r1) + def("IH_R2", c.r2) +
                       std::string("#define IH_OP1_ADD ") + std::to_string(c.op1_add) + "\n#define IH_OP2_XOR " + std::to_string(c.op2_xor) + "\n#define IH_LANES 8u\n" +
                       "#define IH_TUNE_READ " + std::to_string(tune("read", 0)) + "\n#define IH_TUNE_MINBLOCKS " + std::to_string(ih_minblocks()) + "\n" +
                       "#define IH_TUNE_WORDS " + std::to_string(tune("words", 0)) + "\n" + def("IH_ITEM_BYTES", ih_item_bytes_) +
                       "#define IH_LOADS " + std::to_string(tune("loads", 32)) + "\n" + "#define IH_BLOCK " + std::to_string(v20 ? 64 : tune("block", 128)) + "\n" + "#define IH_QUEUE " + std::to_string(tune("queue", 0)) + "\n" + "#define IH_CHAINS " + std::to_string(tune("chains", 1)) + "\n" + "#define IH_LANES16 " + std::to_string(tune("lanes", 8) == 16 ? 1 : 0) + "\n" + "#define IH_V8_B " + std::to_string(ihp_.blocks) + "\n" + "#define IH_V8_K " + std::to_string(ihp_.registers) + "\n" + "#define IH_V8_F " + std::to_string(ihp_.features) + "\n" + "#define IH_NOFETCH " + std::to_string(tune("nofetch", 0)) + "\n" + "#define IH_GRIND " + std::to_string(tune("grind", 0)) + "\n" + "#define IH_KEYED_S " + std::to_string(ihp_.keyed_rounds) + "u\n" + "#define IH_SPLIT " + std::to_string(tune("split", 0)) + "\n" +
                       // the ladder's family 2 (10 Oct 2026): tail = the batched per-nonce tail (N finished states per lane group staged
                       // in shared memory, N lanes hashing N nonces' M and F at once; 0 = the lockstep tail of T7/T8)
                       "#define IH_TUNE_TAIL " + std::to_string(tune("tail", 0)) + "\n" +
                       // the ladder's families 2 and 6 (10 Oct 2026): cache = the item loads' path (0 plain, 1 read-only __ldg as every
                       // earlier row, 2 __ldcg past L1, 3 __ldcs streaming); cold = the per-thread constant words staged in shared memory
                       "#define IH_TUNE_CACHE " + std::to_string(tune("cache", ihp_.version == 15 ? 3 : 1)) + "\n#define IH_TUNE_COLD " + std::to_string(tune("cold", 0)) + "\n";
    if (v15 && tune("nomix", 0)) defs += def("IH_R16_NOMIX", 1);   // a figure experiment (inexact): the mix skipped
    if (v20 && ih_keyed_steps_) defs += def("IH_KEYED_S", ih_keyed_steps_) + def("IH_KEYED_R", ihp_.rounds / ihp_.fold_every);   // the bond-keyed rounds as keyed-read steps (cb73f3b33 section 2)
    if (v20) defs += def("IH_FORGE", v21 ? 0 : 1) + def("IH_FORGE2", v21 ? 1 : 0) + def("IH_FG_WORDS", ihp_.program_ops) + def("IH_FG_FOLD", ihp_.fold_every) + def("IH_FG_DRAWN", ihp_.program_distance & 1u) + def("IH_FG2_SEQ", v21 ? ihp_.blocks : 1u) + def("IH_FG2_BL", v21 ? ihp_.keyed_rounds : 0u) + def("IH_FG2_FPMODE", v21 ? ((ihp_.program_distance >> 1) & 7u) : 0u) + def("IH_FG2_APW2", (v21 && tune("apw2", 0)) ? 1 : 0) + def("IH_FG_SKIP", (v21 && tune("fgskip", 0)) ? 1 : 0) + def("IH_FG2_PAIR", v21 ? ((ihp_.program_distance >> 4) & 15u) : 0u) + def("IH_FG2_SPSHARED", (v21 && tune("spsh", 0)) ? 1 : 0);   // the occupancy port: two attempts a warp, the scratchpad in shared (tunes apw2, spsh)   // forge2: the document's fp_mode (0 as cut, 1 integer-only, 2 producer-masked, 3 the occupancy form)   // forge: the programme's word count, the keyed-read stride, and the drawn-order flag (the second document)
    ih_block_ = static_cast<unsigned>(v20 ? 64 : tune("block", 128));
    if (ihp_.version == 16 && tune("quad", 0) && ih_block_ != 128) throw std::runtime_error("igneumhash: version 16's quad form runs at block 128 (four warps, one bank each)");
    if (ih_block_ != 64 && ih_block_ != 128 && ih_block_ != 256 && ih_block_ != 512) throw std::runtime_error("igneumhash: block must be 64, 128, 256 or 512");
    // the batched tail computes S for the batch's nonces ahead from the grid stride (nonce start + gid0 + i slots); under the
    // device-side queue a group's nonces are not a stride, so the two are refused together (the exactness step caught the
    // mismatch on fam5, 10 Oct 2026 17:30 UK; nothing stored)
    if (tune("tail", 0) && tune("queue", 0)) throw std::runtime_error("igneumhash: tail=N (the batched tail, S computed ahead on the grid stride) is not laid out for queue=1");
    // bit 10 (the bond-keyed rounds) is laid out on the single-chain eight-lane fetching path alone (the knee's path)
    if (ihp_.keyed_rounds && (tune("chains", 1) == 2 || tune("lanes", 8) == 16 || tune("nofetch", 0))) throw std::runtime_error("igneumhash: the bond-keyed rounds (bit 10) are not laid out for chains=2, lanes=16 or nofetch=1");
    if (tune("loads", 32) == 256 && ih_item_bytes_ != 128) throw std::runtime_error("igneumhash: loads=256 (the overfetch form) is for 128-byte items only");
    if (tune("loads", 32) == 512 && ih_item_bytes_ < 256) throw std::runtime_error("igneumhash: loads=512 (the fused form) is for items of 256 bytes and above");
    ih_slots_ = static_cast<uint32_t>(tune("slots", 4096));
    if (tune("batch", 0)) batch_ = static_cast<uint32_t>(tune("batch", 0));   // the launch's nonce count (a throughput window wants a launch under a second: the per-second lines)
    const std::string key = "igneumhash-v" + std::to_string(ihp_.version) + "-" + std::to_string(ih_items_) + "-i" + std::to_string(ih_item_bytes_) + "-" + std::to_string(slice_words) + "-" +
                            std::to_string(ih_rounds_) + "-" + std::to_string(ihp_.item_chain) + "-" + std::to_string(n_cache) + "-" + std::to_string(ihp_.parents) +
                            "-c" + std::to_string(cell & 7) + "-t" + std::to_string(tune("read", 0)) + "m" + std::to_string(tune("minblocks", 0)) + "w" + std::to_string(tune("words", 0)) + "l" + std::to_string(tune("loads", 32)) + "b" + std::to_string(tune("block", 128)) + "q" + std::to_string(tune("queue", 0)) + "Q" + std::to_string(tune("quad", 0)) + "A" + std::to_string(tune("ablate", 0)) + "c" + std::to_string(tune("chains", 1)) +
                            (tune("nomix", 0) ? "nm1" : "") + ((v15 && tune("split15", 1)) ? "s15" : "") + (ih_keyed_steps_ ? "k" + std::to_string(ih_keyed_steps_) : "") + "v8b" + std::to_string(ihp_.blocks) + "x" + std::to_string(ihp_.registers) + "f" + std::to_string(ihp_.features) + "nf" + std::to_string(tune("nofetch", 0)) + "g" + std::to_string(tune("grind", 0)) + "r" + std::to_string(ih_minblocks()) + "h" + std::to_string(tune("cache", ihp_.version == 15 ? 3 : 1)) + "k" + std::to_string(tune("cold", 0)) + "n" + std::to_string(tune("tail", 0)) + (ihp_.keyed_rounds ? "q" + std::to_string(ihp_.keyed_rounds) : std::string()) + (tune("split", 0) ? "x1" : "") + (v9 ? "p" + std::to_string(ihp_.program_ops) + "d" + std::to_string(ihp_.program_distance) + "e" + std::to_string(ih_r0d_period_) + "v" + std::to_string(ihp_.version) + "t" + std::to_string(tune("r0etable", ihp_.version >= 12 ? ihp_.table_kib * 256u : ihp_.version == 11 ? 1024 : 4096)) + "w" + std::to_string(ihp_.access_bytes) + "s" + std::to_string(tune("r0eshared", 0)) + "a" + std::to_string(tune("ablate", 0)) + "tr" + std::to_string(tune("trace", 0)) + "an" + hex8(fnv64(std::string(reinterpret_cast<const char*>(ihp_.program_anchor), 32))) + (ihp_.version >= 12 ? "sk" + std::to_string(ihp_.shared_kib) + "m" + std::to_string(ihp_.mma_max) + "ch" + std::to_string(ihp_.chains_per_nonce) + "lb" + std::to_string(ihp_.gather_lane_bank) + "ts" + std::to_string(ihp_.tile_shape) + "fe" + std::to_string(ihp_.fold_every) + "ld" + std::to_string(ihp_.layout_draw) + "tl" + std::to_string(ihp_.tile_layout) : std::string()) : std::string());   // bit 10: S is not in FEATURES, so the key carries it; x1 the split (renter) form; v9: the programme's size, distance and period
    bool ptx = false;
    if (compile_only_) { compile(device_source_igneumhash(defs + r0d), key, ptx); return; }   // the precompile verb: the cache file alone
    if (static_mod_) { cuModuleUnload(static_mod_); static_mod_ = nullptr; }
    const std::string image = compile(device_source_igneumhash(defs + r0d), key, ptx);
    ih_src_defs_ = defs + r0d;   // the compiled per-batch form (version 21, fg2c=1) prepends the batch's generated shapes to these
    for (auto& m : fg2_mods_) cuModuleUnload(m.mod); fg2_mods_.clear();
    for (auto& a : fg3_ahead_) if (a.second.second.valid()) a.second.second.wait();
    fg3_ahead_.clear();
    for (auto& m : fg3_mods_) cuModuleUnload(m.mod); fg3_mods_.clear();
    check(cuModuleLoadData(&static_mod_, image.data()), "load igneumhash module");
    if (tune("trace", 0)) {   // the trace mode's per-op buffer (129 slots x 128 words) handed to the module's global
        CUdeviceptr g = 0; size_t gsz = 0;
        if (cuModuleGetGlobal(&g, &gsz, static_mod_, "ih_tr_ops") == CUDA_SUCCESS) { if (!d_trace_ops_) check(cuMemAlloc(&d_trace_ops_, 129 * 128 * sizeof(uint32_t)), "trace ops buffer"); check(cuMemcpyHtoD(g, &d_trace_ops_, sizeof d_trace_ops_), "trace ops global"); }
    }
    code_bytes_ = image.size();
    check(cuModuleGetFunction(&f_ih_gen_, static_mod_, "ih_dataset_gen"), "ih_dataset_gen"); record_kernel(f_ih_gen_, "ih_dataset_gen");
    check(cuModuleGetFunction(&f_ih_search_, static_mod_, "ih_search"), "ih_search"); record_kernel(f_ih_search_, "ih_search");
    f_r16_mix_ = nullptr; r16_state_global_ = 0; r16_state_written_ = 0;
    if (v15 && tune("split15", 1)) {   // the split form: the mix kernel and the state pointer's global (ANVIL-V25-RECORD.md 8g)
        check(cuModuleGetFunction(&f_r16_mix_, static_mod_, "ih_r16_mix"), "ih_r16_mix"); record_kernel(f_r16_mix_, "ih_r16_mix");
        size_t gsz = 0; check(cuModuleGetGlobal(&r16_state_global_, &gsz, static_mod_, "ih_r16_state"), "ih_r16_state global");
    }
    ih_cell_ = cell & 7;
}

// Anvil's compiled per-batch form: the module whose ih_search carries the batch's shapes straight-line (forge2_shapes.hpp), keyed
// by the period seed pseed = BLAKE3(LP("IgneumHash/Forge/period") || E || header || LE64(batch)); the last four kept
CUfunction engine::fg2_batch_fn(const uint8_t header[32], uint64_t batch) {
    uint8_t pseed[32];
    if ((ihp_.period_blocks ? ignh_forge2_period_seed_chain(&ihp_, ih_E_, batch * ihp_.period_blocks, nullptr, pseed) : ignh_forge2_period_seed(&ihp_, ih_E_, header, batch, pseed)) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the period seed");   // the chain form through the project's own export (batch is the period index; the anchor the document's, set_anchor writes the template's into it)
    std::string key; { static const char* hx = "0123456789abcdef"; for (int i = 0; i < 16; ++i) { key += hx[pseed[i] >> 4]; key += hx[pseed[i] & 15]; } }
    for (auto& m : fg2_mods_) if (m.key == key) return m.fn;
    bool ptx = false;
    const std::string image = compile(device_source_igneumhash(ih_src_defs_ + forge2::source(pseed, ihp_.program_ops)), "fg2-" + key, ptx);
    fg2_mod m{key, nullptr, nullptr};
    check(cuModuleLoadData(&m.mod, image.data()), "load the batch's module");
    check(cuModuleGetFunction(&m.fn, m.mod, "ih_search"), "ih_search (batch)"); record_kernel(m.fn, "ih_search");
    if (fg2_mods_.size() >= 4) { cuModuleUnload(fg2_mods_.front().mod); fg2_mods_.erase(fg2_mods_.begin()); }
    fg2_mods_.push_back(m);
    return m.fn;
}

// the whole-entry compiled form (the GPU team, 15 Oct 2026): the batch's module with ih_fg3_prep, ih_search (the step work) and ih_fg3_mix
void engine::fg3_source(const uint8_t header[32], uint64_t batch, std::string& key, std::string& src) {
    uint8_t pseed[32];
    if ((ihp_.period_blocks ? ignh_forge2_period_seed_chain(&ihp_, ih_E_, batch * ihp_.period_blocks, nullptr, pseed) : ignh_forge2_period_seed(&ihp_, ih_E_, header, batch, pseed)) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the period seed");   // the chain form through the project's own export (batch is the period index; the anchor the document's, set_anchor writes the template's into it)
    key.clear(); { static const char* hx = "0123456789abcdef"; for (int i = 0; i < 16; ++i) { key += hx[pseed[i] >> 4]; key += hx[pseed[i] & 15]; } }
    const bool v25 = ihp_.version == 25;   // entry 2 (oldcard): the per-hash scratchpad in shared memory, two attempts a warp by default
    const uint32_t apw = static_cast<uint32_t>(tune("apw", (v25 || ihp_.version == 21) ? 2 : 1));   // two attempts a warp the default for version 21 as for 25: the rows of record (the 4090's 34.5 MH/s, the owned 2060's curve) ran fg3=1,apw=2
    const bool ilp6 = tune("ilp6", 1) != 0;   // (e) carries the ILP wiring (the hash team, main 9c75468fc); ilp6=0 the superseded cut
    const bool noxl = tune("noxl", 0) != 0;   // a figure experiment (inexact), never a row
    const uint32_t unroll = static_cast<uint32_t>(tune("unroll", 4));   // fp_mode 6: the steps a block of the body (the window rotated by it)
    const uint32_t minb = static_cast<uint32_t>(tune("minb", 0));   // the search kernel's __launch_bounds__ minimum blocks (warps) an SM (version 25: 12 gave the 5090 eleven warps an SM and nothing over ten minutes; ANVIL-V25-RECORD.md 3b)
    const bool pfa = tune("pfa", 0) != 0;   // the asynchronous prefetch (cp.async into a shared ring, no registers held) under a prefetch document: no gain, the keyed read every step is bandwidth-bound (15 Oct 06:0x UK)
    const uint32_t pfd = static_cast<uint32_t>(tune("pf", pfa ? 8 : 2));   // the keyed read issued this many steps ahead under a prefetch document (at most the document's prefetch; version 25 takes 2, 4 or 8, none moves a card: ANVIL-V25-RECORD.md section 5)
    const uint32_t prefetch = (ihp_.program_distance >> 8) & 127u; const bool lean = ((ihp_.program_distance >> 15) & 1u) != 0;
    const bool init8 = tune("init8", 1) != 0;
    const bool divlane = tune("divlane", 0) != 0;   // the screen's per-thread interpreter cell (a figure, inexact)   // the init chain on the search kernel's lanes (eight chains, four passes through shared) instead of the prep thread
    key += apw == 2 ? "-a2" : "-a1"; if (ilp6) key += "-i"; if (noxl) key += "-x"; if (v25) key += "-s" + std::to_string(ihp_.scratch_bytes >> 10); if (tune("stop", 0)) key += "-st" + std::to_string(tune("stop", 0)); if (tune("fg3dump", 0)) key += "-dbg"; if (ih_keyed_steps_) key += "-k" + std::to_string(ih_keyed_steps_); if (ih_keyed_steps_ && tune("kprobe", 0)) key += "-kp" + std::to_string(tune("kprobe", 0)); if (tune("noseed", 0)) key += "-ns"; if (tune("xfold", 0)) key += "-xf"; if (tune("scr", 0)) key += "-scr" + std::to_string(tune("scr", 0)); if (tune("pad", 0)) key += "-pad" + std::to_string(tune("pad", 0)); if (tune("onephase", 0)) key += "-1p"; key += "-u" + std::to_string(unroll) + "-m" + std::to_string(minb) + "-p" + std::to_string(pfd) + (init8 ? "-i8" : "") + (pfa ? "-pa" : "") + (divlane ? "-dl" : "");
    src.clear();
    for (auto& m : fg3_mods_) if (m.key == key) return;   // a loaded module: the key alone (generating the text costs about 2 ms a call, once a launch under fg2fix)
    const uint32_t fp_mode = ((ihp_.program_distance >> 1) & 7u) | (((ihp_.program_distance >> 16) & 1u) << 3);
    if (!forge3::supported(fp_mode)) throw std::runtime_error("igneumhash: fg3 generates fp_mode 3, 4, 6 and 9 only");
    if (ihp_.blocks > 128 || (ihp_.rounds % ihp_.blocks)) throw std::runtime_error("igneumhash: fg3 wants a sequence of at most 128 entries dividing the steps");
    if (v25 && ih_keyed_steps_) throw std::runtime_error("igneumhash: version 25 under features bit 10 (the bond-keyed rounds) has no card path yet: the compiled keyed form is not laid out and version 25 has no interpreted walk (KEYED-RELEASED-VERSIONS-SPEC.md 5.5)");
    if (ih_keyed_steps_ && (pfa || divlane || tune("regroup", 0) || tune("xfold", 0))) throw std::runtime_error("igneumhash: the compiled keyed form (features bit 10) runs the pipelined read in registers alone: no pfa, no divlane, no regroup, no xfold");   // F-504: the probe forms carry no keyed read
    if (v25 && (!init8 || divlane || !tune("fg3", 1))) throw std::runtime_error("igneumhash: version 25 runs the fg3 form with the init chain on the search kernel's lanes (init8=1) and no divlane");
    src = device_source_igneumhash(ih_src_defs_ + (divlane ? std::string("#define IH_FG3_DIVLANE 1\n") : std::string()) + (tune("stop", 0) ? "#define IH_FG3_STOP " + std::to_string(tune("stop", 0)) + "\n" : std::string()) + (tune("fg3dump", 0) ? std::string("#define IH_FG3_DEBUG 1\n") : std::string()) + (tune("noseed", 0) ? std::string("#define IH_FG3_NOSEED 1\n") : std::string()) + (tune("scr", 0) ? "#define IH_FG3_SCR " + std::to_string(tune("scr", 0)) + "\n" : std::string()) + forge3::source(pseed, ihp_.program_ops, fp_mode, ihp_.rounds / ihp_.blocks, ihp_.fold_every, ih_items_, apw, 32u, ilp6, noxl, unroll, minb, prefetch, pfd, lean, init8, pfa, ihp_.period_blocks != 0, ((ihp_.program_distance >> 17) & 1u) != 0, ihp_.version == 24, v25 ? (tune("pad", 0) ? (uint32_t)tune("pad", 0) : ihp_.scratch_bytes) : 0u, tune("xfold", 0) != 0, tune("onephase", 0) != 0, ih_keyed_steps_, ihp_.fold_every ? ihp_.rounds / ihp_.fold_every : 0u, ih_keyed_steps_ ? (uint32_t)tune("kprobe", 0) : 0u));   // kprobe: a PROBE of the keyed step's cost (1 no compression, 2 no synchronous read), inexact, never a row   // F-504: the keyed-read steps S and the walk's reads R (0 unkeyed)   // pad=<bytes>: a PROBE (inexact), the pad and its masks at another size under the document (the occupancy of sp1 and sp2 before their sets); onephase=1: a PROBE (inexact), one register-group phase for every iteration (the code size lever)   // xfold=1: the spread keyed fold (a figure: the shuffles cost more MIO cycles than the folds cost issue slots; off)
}

const engine::fg3_mod& engine::fg3_batch(const uint8_t header[32], uint64_t batch) {
    std::string key, src; fg3_source(header, batch, key, src);
    for (auto& m : fg3_mods_) if (m.key == key) return m;
    std::string image;
    auto it = fg3_ahead_.find(batch);
    if (it != fg3_ahead_.end() && it->second.first == key) { image = it->second.second.get(); fg3_ahead_.erase(it); }   // the compile thread's image for this batch
    else { bool ptx = false; image = compile(src, "fg3-" + key, ptx); }
    fg3_mod m{key, nullptr, nullptr, nullptr, nullptr, nullptr, nullptr, nullptr};
    if (tune("jit", -1) >= 0) {   // the PTX form: the driver's ptxas at the tuned level, its time on stderr (the assembler's share of the period compile)
        const auto tl0 = std::chrono::steady_clock::now();
        CUjit_option jo[1] = {CU_JIT_OPTIMIZATION_LEVEL}; unsigned int lvl = (unsigned int)tune("jit", -1); void* jv[1] = {(void*)(uintptr_t)lvl};
        check(cuModuleLoadDataEx(&m.mod, image.data(), 1, jo, jv), "load the batch's module (fg3, ptx jit)");
        std::fprintf(stderr, "assembled the period module in the driver (ptxas -O%u) in %.1f s\n", lvl, std::chrono::duration<double>(std::chrono::steady_clock::now() - tl0).count());
    } else check(cuModuleLoadData(&m.mod, image.data()), "load the batch's module (fg3)");
    check(cuModuleGetFunction(&m.prep, m.mod, "ih_fg3_prep"), "ih_fg3_prep"); record_kernel(m.prep, "ih_fg3_prep");
    check(cuModuleGetFunction(&m.search, m.mod, "ih_search"), "ih_search (fg3)");
    if (ihp_.version == 25) cuFuncSetAttribute(m.search, CU_FUNC_ATTRIBUTE_PREFERRED_SHARED_MEMORY_CARVEOUT, 100);   // version 25: the scratchpads want the whole carveout (8 KiB a warp); a refusal leaves the driver's choice
    record_kernel(m.search, "ih_search", 32);   // one warp a block: the occupancy read at 32 (resident warps an SM = resident attempts / apw)
    check(cuModuleGetFunction(&m.mix, m.mod, "ih_fg3_mix"), "ih_fg3_mix"); record_kernel(m.mix, "ih_fg3_mix");
    m.queue = nullptr; m.entry = nullptr; m.acc = nullptr;
    if (cuModuleGetFunction(&m.queue, m.mod, "ih_fg3_queue") == CUDA_SUCCESS) { check(cuModuleGetFunction(&m.entry, m.mod, "ih_fg3_entry"), "ih_fg3_entry"); check(cuModuleGetFunction(&m.acc, m.mod, "ih_fg3_acc"), "ih_fg3_acc"); record_kernel(m.entry, "ih_fg3_entry"); }
    if (fg3_mods_.size() >= 4) { cuModuleUnload(fg3_mods_.front().mod); fg3_mods_.erase(fg3_mods_.begin()); }
    fg3_mods_.push_back(m);
    return fg3_mods_.back();
}

void engine::record_kernel(CUfunction f, const char* name, int block) {
    kfact k; k.name = name; k.block = block;
    for (auto& e : kfacts_) if (e.name == k.name) { e = kfact(); e.name = name; }
    cuFuncGetAttribute(&k.regs, CU_FUNC_ATTRIBUTE_NUM_REGS, f);
    cuFuncGetAttribute(&k.local_bytes, CU_FUNC_ATTRIBUTE_LOCAL_SIZE_BYTES, f);
    cuFuncGetAttribute(&k.shared_bytes, CU_FUNC_ATTRIBUTE_SHARED_SIZE_BYTES, f);
    cuFuncGetAttribute(&k.max_threads, CU_FUNC_ATTRIBUTE_MAX_THREADS_PER_BLOCK, f);
    cuOccupancyMaxActiveBlocksPerMultiprocessor(&k.blocks_per_sm, f, block, 0);  // resident blocks of the launch's size per SM: resident warps = block / 32 x this
    for (auto& e : kfacts_) if (e.name == k.name) { e = k; return; }
    kfacts_.push_back(k);
}

std::string engine::kernels_json() const {
    std::string o = "[";
    for (size_t i = 0; i < kfacts_.size(); ++i) {
        const kfact& k = kfacts_[i];
        o += std::string(i ? ", " : "") + "{\"name\": \"" + k.name + "\", \"registers\": " + std::to_string(k.regs) + ", \"local_bytes\": " + std::to_string(k.local_bytes) +
             ", \"shared_bytes\": " + std::to_string(k.shared_bytes) + ", \"max_threads_per_block\": " + std::to_string(k.max_threads) +
             ", \"block\": " + std::to_string(k.block) + ", \"resident_blocks_per_sm\": " + std::to_string(k.blocks_per_sm) + ", \"resident_warps_per_sm\": " + std::to_string((k.block / 32) * k.blocks_per_sm) + "}";
    }
    // the brief's counters the runtime does not expose are named, never inferred (BRIEF-R1.md section 12)
    o += "], \"sm_count\": " + std::to_string(sm_count_) + ", \"allocation\": {\"dataset_buffers\": 1, \"dataset_bytes\": " + std::to_string(dag_bytes_) + ", \"cache_bytes\": " + std::to_string(cache_bytes_) +
         ", \"form\": \"one cuMemAlloc for the dataset, one for the cache\"}, \"profiler\": {\"memory_requests_per_byte\": \"unavailable\", \"cache_hit_rates\": \"unavailable\", \"stall_reasons\": \"unavailable\", \"integer_throughput\": \"unavailable\", \"lane_exchange_cost\": \"unavailable\", \"note\": \"no profiler on the host; a profiled cell names its tool\"}";
    return "{\"functions\": " + o + "}";
}

uint64_t engine::bytes_per_hash() const {
    // the kernel's own count of dataset bytes per hash: IgneumHash R rounds of one 128-byte item; KAWPOW 64 loads of
    // 256 bytes (progpow 0.9.4: PROGPOW_CNT_DAG x PROGPOW_DAG_LOADS x 16 lanes x 4 bytes... the 2048-bit item per load is
    // read once per lane group: 64 x 256); FishHash 32 rounds of one 128-byte item; Autolykos 32 + 1 elements of 32 bytes
    if (ih_ && ihp_.version == 16) return (ihp_.program_distance & 8388608) ? static_cast<uint64_t>(ihp_.rounds) * ((ihp_.registers ? ihp_.registers : 224u) / 4) / (8u << ((ihp_.program_distance >> 24) & 3)) * 64u : 0;   // revision 19: one 64-byte quarter every K ops; the other version 16 forms read no dataset
    if (ih_) return static_cast<uint64_t>(ih_rounds_) * ih_item_bytes_;
    if (rx_) return static_cast<uint64_t>(ih_rounds_) * 128;
    if (fh_) return 32ull * 128;
    if (al_) return 33ull * 32;
    return 64ull * 256;
}

void engine::bwprobe(uint64_t bytes, double& linear_gbs, double& random_gbs) {
    // compiled once: a linear sum and a random 128-byte-line gather over a buffer, each a uint4 per thread
    static const char* src =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void probe_linear(const uint4* __restrict__ p, uint64_t n16, uint32_t* out) {\n"
        "  uint32_t acc = 0; for (uint64_t i = blockIdx.x * (uint64_t)blockDim.x + threadIdx.x; i < n16; i += (uint64_t)gridDim.x * blockDim.x) { uint4 v = p[i]; acc ^= v.x ^ v.y ^ v.z ^ v.w; }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n"
        "extern \"C\" __global__ void probe_random(const uint4* __restrict__ p, uint64_t lines, uint32_t reads, uint32_t* out) {\n"
        "  const uint32_t lane = threadIdx.x & 7u; uint64_t x = 0x9E3779B97F4A7C15ull * (1 + blockIdx.x * (uint64_t)blockDim.x + (threadIdx.x >> 3)); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t line = x % lines; uint4 v = p[line * 8 + lane]; acc ^= v.x ^ v.y ^ v.z ^ v.w; x += acc; }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n";
    bool ptx = false;
    const std::string image = compile(src, "bwprobe", ptx);
    CUmodule mod = nullptr; CUfunction fl = nullptr, fr = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load bwprobe");
    check(cuModuleGetFunction(&fl, mod, "probe_linear"), "probe_linear");
    check(cuModuleGetFunction(&fr, mod, "probe_random"), "probe_random");
    CUdeviceptr buf = 0, out = 0;
    check(cuMemAlloc(&buf, bytes), "probe buffer");
    check(cuMemAlloc(&out, 4), "probe out");
    check(cuMemsetD32(buf, 0x01020304u, bytes / 4), "probe fill");
    const uint64_t n16 = bytes / 16, lines = bytes / 128;
    auto run = [&](CUfunction f, bool random, double& gbs) {
        const unsigned grid = 2048, block = 256;
        const uint32_t reads = 64;
        void* al[] = {&buf, (void*)&n16, &out};
        void* ar[] = {&buf, (void*)&lines, (void*)&reads, &out};
        // one warm launch, then timed launches for at least half a second
        check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, random ? ar : al, nullptr), "probe warm");
        check(cuCtxSynchronize(), "probe sync");
        const auto t0 = std::chrono::steady_clock::now();
        uint64_t moved = 0; int launches = 0;
        do {
            check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, random ? ar : al, nullptr), "probe");
            check(cuCtxSynchronize(), "probe sync");
            moved += random ? (uint64_t)grid * block / 8 * reads * 128 : bytes;
            ++launches;
        } while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < 0.5);
        const double s = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        gbs = moved / s / 1e9;
    };
    run(fl, false, linear_gbs);
    run(fr, true, random_gbs);
    cuMemFree(buf); cuMemFree(out); cuModuleUnload(mod);
}

// The attainable-bandwidth diagnostic (founder's order, 10 Oct 2026 12:1x UK): v1's request shape (128-byte items read
// by 8 lanes at 16 bytes each, uniform over the dataset) in two forms at a chosen number of independent contexts (nonces)
// in flight. Form A: independent requests, the address stream from a per-context xorshift that never touches the data
// (the memory system's attainable ceiling for this pattern). Form B: dependent chains of `depth` reads per context, the
// next address drawn from the loaded item (the latency-hiding ceiling at this concurrency). Form C is the v1 bench itself
// at the same slots. Each form runs about three seconds with the board power sampled once a second.
void engine::ceiling(uint64_t bytes, uint32_t depth, uint32_t contexts, bool dependent, double& gbs, double& req_per_s, double& power_w, uint32_t& regs) {
    static const char* src =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void probe_a(const uint4* __restrict__ p, uint64_t lines, uint32_t reads, uint32_t* out) {\n"
        "  const uint32_t lane = threadIdx.x & 7u; uint64_t x = 0x9E3779B97F4A7C15ull * (1 + blockIdx.x * (uint64_t)blockDim.x + (threadIdx.x >> 3)); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t line = x % lines; uint4 v = p[line * 8 + lane]; acc ^= v.x ^ v.y ^ v.z ^ v.w; }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n"
        "extern \"C\" __global__ void probe_b(const uint4* __restrict__ p, uint64_t lines, uint32_t reads, uint32_t* out) {\n"
        "  const uint32_t lane = threadIdx.x & 7u; uint64_t x = 0x9E3779B97F4A7C15ull * (1 + blockIdx.x * (uint64_t)blockDim.x + (threadIdx.x >> 3)); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t line = x % lines; uint4 v = p[line * 8 + lane]; acc ^= v.x ^ v.y ^ v.z ^ v.w; x += acc; }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n";
    bool ptx = false;
    const std::string image = compile(src, "ceiling", ptx);
    CUmodule mod = nullptr; CUfunction f = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load ceiling");
    check(cuModuleGetFunction(&f, mod, dependent ? "probe_b" : "probe_a"), "ceiling kernel");
    int r = 0; cuFuncGetAttribute(&r, CU_FUNC_ATTRIBUTE_NUM_REGS, f); regs = (uint32_t)r;
    CUdeviceptr buf = 0, out = 0;
    check(cuMemAlloc(&buf, bytes), "ceiling buffer");
    check(cuMemAlloc(&out, 4), "ceiling out");
    check(cuMemsetD32(buf, 0x01020304u, bytes / 4), "ceiling fill");
    const uint64_t lines = bytes / 128;
    const unsigned block = 256, grid = (contexts * 8 + block - 1) / block;
    void* args[] = {&buf, (void*)&lines, (void*)&depth, &out};
    check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, args, nullptr), "ceiling warm");
    check(cuCtxSynchronize(), "ceiling sync");
    const auto t0 = std::chrono::steady_clock::now();
    uint64_t requests = 0; double wsum = 0; int wn = 0; double next_sample = 0.5;
    do {
        check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, args, nullptr), "ceiling");
        check(cuCtxSynchronize(), "ceiling sync");
        requests += (uint64_t)grid * block / 8 * depth;
        const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (el >= next_sample) { telemetry t = read_telemetry(); if (t.ok && t.power_w > 0) { wsum += t.power_w; ++wn; } next_sample += 1.0; }
    } while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < 3.0);
    const double sec = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    req_per_s = requests / sec; gbs = requests * 128.0 / sec / 1e9; power_w = wn ? wsum / wn : 0;
    cuMemFree(buf); cuMemFree(out); cuModuleUnload(mod);
}

// Form A at a request width: a request of `width` bytes (64 to 4096) is served by G = min(width / 16, 32) lanes reading
// width / (16 G) consecutive uint4 each from a uniformly random `width`-aligned address over the buffer; independent
// requests from a per-group xorshift; the group's k-th load covers G consecutive uint4 (lane-interleaved, so every load
// instruction is coalesced; the first layout gave each lane a contiguous run and lost half the sectors at 1024 bytes).
// Achieved bytes per second and requests per second at `contexts` groups in flight.
void engine::ceiling_width(uint64_t bytes, uint32_t width, uint32_t contexts, uint32_t reads, double& gbs, double& req_per_s, double& power_w, uint32_t& regs) {
    static const char* src =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void probe_w(const uint4* __restrict__ p, uint64_t items, uint32_t width, uint32_t reads, uint32_t* out) {\n"
        "  const uint32_t G = width / 16u < 32u ? width / 16u : 32u; const uint32_t per = width / 16u / G;\n"
        "  const uint32_t lane = threadIdx.x % G; const uint32_t group = (blockIdx.x * (uint64_t)blockDim.x + threadIdx.x) / G;\n"
        "  uint64_t x = 0x9E3779B97F4A7C15ull * (1 + group); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t item = x % items; const uint4* q = p + item * (width / 16u);\n"
        "    for (uint32_t k = 0; k < per; ++k) { uint4 v = q[k * G + lane]; acc ^= v.x ^ v.y ^ v.z ^ v.w; } }\n"  // the group's k-th load covers G consecutive uint4: every load instruction fully coalesced\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n";
    bool ptx = false;
    const std::string image = compile(src, "ceiling-width", ptx);
    CUmodule mod = nullptr; CUfunction f = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load ceiling-width");
    check(cuModuleGetFunction(&f, mod, "probe_w"), "probe_w");
    int r = 0; cuFuncGetAttribute(&r, CU_FUNC_ATTRIBUTE_NUM_REGS, f); regs = (uint32_t)r;
    CUdeviceptr buf = 0, out = 0;
    check(cuMemAlloc(&buf, bytes), "ceiling buffer");
    check(cuMemAlloc(&out, 4), "ceiling out");
    check(cuMemsetD32(buf, 0x01020304u, bytes / 4), "ceiling fill");
    const uint64_t items = bytes / width;
    const uint32_t G = width / 16 < 32 ? width / 16 : 32;
    const unsigned block = 256, grid = (unsigned)(((uint64_t)contexts * G + block - 1) / block);
    void* args[] = {&buf, (void*)&items, (void*)&width, (void*)&reads, &out};
    check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, args, nullptr), "ceiling warm");
    check(cuCtxSynchronize(), "ceiling sync");
    const auto t0 = std::chrono::steady_clock::now();
    uint64_t requests = 0; double wsum = 0; int wn = 0; double next_sample = 0.5;
    do {
        check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, 0, nullptr, args, nullptr), "ceiling");
        check(cuCtxSynchronize(), "ceiling sync");
        requests += (uint64_t)grid * block / G * reads;
        const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (el >= next_sample) { telemetry t = read_telemetry(); if (t.ok && t.power_w > 0) { wsum += t.power_w; ++wn; } next_sample += 1.0; }
    } while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < 3.0);
    const double sec = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    req_per_s = requests / sec; gbs = requests * (double)width / sec / 1e9; power_w = wn ? wsum / wn : 0;
    cuMemFree(buf); cuMemFree(out); cuModuleUnload(mod);
}

// The shared probe shape (the GPU team's, agreed 13:4x UK): eight lanes per request always (the kernel's lane group), a W-aligned
// item read in W / 128 coalesced 128-byte steps (step j: lane l takes words 32 j + 4 l .. + 3), 64 bytes as lanes 0 to 3 on
// the line's first half with 4 to 7 idle. Form A: the next item from a per-group xorshift; form B: from the loaded words.
void engine::ceiling_lanes(uint64_t bytes, uint32_t width, uint32_t contexts, uint32_t reads, bool dependent, double& gbs, double& req_per_s, double& power_w, uint32_t& regs, unsigned block, unsigned shared_bytes, unsigned lanes) {
    static const char* src =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void probe_l(const uint4* __restrict__ p, uint64_t items, uint32_t width, uint32_t reads, uint32_t dep, uint32_t* out) {\n"
        "  const uint32_t lane = threadIdx.x & 7u; const uint32_t group = (blockIdx.x * (uint64_t)blockDim.x + threadIdx.x) >> 3;\n"
        "  const uint32_t steps = width >= 128u ? width / 128u : 1u; const bool active = width >= 128u || lane < 4u;\n"
        "  uint64_t x = 0x9E3779B97F4A7C15ull * (1 + group); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t item = x % items; const uint4* q = p + item * (width / 16u);\n"
        "    for (uint32_t j = 0; j < steps; ++j) { if (active) { uint4 v = q[j * 8u + lane]; acc ^= v.x ^ v.y ^ v.z ^ v.w; } }\n"
        "    if (dep) { acc = __shfl_xor_sync(0xFFFFFFFFu, acc, 1) ^ acc; x += acc; } }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n";
    // Crosscurrent CC-B's transfer-only twin: 16 lanes per request, each lane eight bytes (W / 128 steps of one 128-byte line
    // per instruction); the same request stream, the same dependence, nothing else of the kernel
    static const char* src16 =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void probe_l(const uint4* __restrict__ p, uint64_t items, uint32_t width, uint32_t reads, uint32_t dep, uint32_t* out) {\n"
        "  const uint32_t lane = threadIdx.x & 15u; const uint32_t group = (blockIdx.x * (uint64_t)blockDim.x + threadIdx.x) >> 4;\n"
        "  const uint32_t steps = width >= 128u ? width / 128u : 1u; const bool active = width >= 128u || lane < 8u;\n"
        "  uint64_t x = 0x9E3779B97F4A7C15ull * (1 + group); uint32_t acc = 0;\n"
        "  for (uint32_t r = 0; r < reads; ++r) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; const uint64_t item = x % items; const uint2* q = (const uint2*)(p + item * (width / 16u));\n"
        "    for (uint32_t j = 0; j < steps; ++j) { if (active) { uint2 v = q[j * 16u + lane]; acc ^= v.x ^ v.y; } }\n"
        "    if (dep) { acc = __shfl_xor_sync(0xFFFFFFFFu, acc, 1) ^ acc; x += acc; } }\n"
        "  if (acc == 0x12345678u) out[0] = acc; }\n";
    const bool l16 = lanes == 16;
    bool ptx = false;
    const std::string image = compile(l16 ? src16 : src, l16 ? "ceiling-lanes16" : "ceiling-lanes", ptx);
    CUmodule mod = nullptr; CUfunction f = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load ceiling-lanes");
    check(cuModuleGetFunction(&f, mod, "probe_l"), "probe_l");
    int r = 0; cuFuncGetAttribute(&r, CU_FUNC_ATTRIBUTE_NUM_REGS, f); regs = (uint32_t)r;
    if (shared_bytes > 48u * 1024u) check(cuFuncSetAttribute(f, CU_FUNC_ATTRIBUTE_MAX_DYNAMIC_SHARED_SIZE_BYTES, (int)shared_bytes), "ceiling shared opt-in");
    CUdeviceptr buf = 0, out = 0;
    check(cuMemAlloc(&buf, bytes), "ceiling buffer");
    check(cuMemAlloc(&out, 4), "ceiling out");
    check(cuMemsetD32(buf, 0x01020304u, bytes / 4), "ceiling fill");
    const uint64_t items = bytes / width;
    const uint32_t dep = dependent ? 1u : 0u;
    if (block == 0 || block % 16 != 0 || block > 1024) block = 256;
    const unsigned grid = (unsigned)(((uint64_t)contexts * (l16 ? 16 : 8) + block - 1) / block);
    void* args[] = {&buf, (void*)&items, (void*)&width, (void*)&reads, (void*)&dep, &out};
    check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, shared_bytes, nullptr, args, nullptr), "ceiling warm");
    check(cuCtxSynchronize(), "ceiling sync");
    const auto t0 = std::chrono::steady_clock::now();
    uint64_t requests = 0; double wsum = 0; int wn = 0; double next_sample = 0.5;
    do {
        check(cuLaunchKernel(f, grid, 1, 1, block, 1, 1, shared_bytes, nullptr, args, nullptr), "ceiling");
        check(cuCtxSynchronize(), "ceiling sync");
        requests += (uint64_t)grid * block / (l16 ? 16 : 8) * reads;   // one request per lane group (16 threads under the CC-B twin)
        const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (el >= next_sample) { telemetry t = read_telemetry(); if (t.ok && t.power_w > 0) { wsum += t.power_w; ++wn; } next_sample += 1.0; }
    } while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < 3.0);
    const double sec = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    req_per_s = requests / sec; gbs = requests * (double)width / sec / 1e9; power_w = wn ? wsum / wn : 0;
    cuMemFree(buf); cuMemFree(out); cuModuleUnload(mod);
}

// The dependent-read latency of the card over a buffer of the dataset's size: a pure pointer chase (the next line's index
// stored in each line, a full-period affine permutation over the lines) by a single thread, no address arithmetic between
// reads; nanoseconds per read. Little's law then turns an achieved rate into the outstanding requests it exposes.
double engine::latency_ns(uint64_t bytes, uint32_t chases) {
    static const char* src =
        "typedef unsigned int uint32_t; typedef unsigned long long uint64_t;\n"
        "extern \"C\" __global__ void fill_chain(uint4* p, uint64_t lines) {\n"
        "  for (uint64_t i = blockIdx.x * (uint64_t)blockDim.x + threadIdx.x; i < lines; i += (uint64_t)gridDim.x * blockDim.x) {\n"
        "    const uint64_t nxt = (i * 0x9E3779B1ull + 1ull) % lines; p[i * 8] = make_uint4((uint32_t)nxt, (uint32_t)(nxt >> 32), 0u, 0u); } }\n"
        "extern \"C\" __global__ void chase(const uint4* __restrict__ p, uint32_t n, uint32_t* out) {\n"
        "  uint64_t idx = 0; for (uint32_t i = 0; i < n; ++i) { const uint4 v = p[idx * 8]; idx = (uint64_t)v.x | ((uint64_t)v.y << 32); }\n"
        "  out[0] = (uint32_t)idx; }\n";
    bool ptx = false;
    const std::string image = compile(src, "latency", ptx);
    CUmodule mod = nullptr; CUfunction ff = nullptr, fc = nullptr;
    check(cuModuleLoadData(&mod, image.data()), "load latency");
    check(cuModuleGetFunction(&ff, mod, "fill_chain"), "fill_chain");
    check(cuModuleGetFunction(&fc, mod, "chase"), "chase");
    CUdeviceptr buf = 0, out = 0;
    check(cuMemAlloc(&buf, bytes), "latency buffer");
    check(cuMemAlloc(&out, 4), "latency out");
    const uint64_t lines = bytes / 128;
    void* fa[] = {&buf, (void*)&lines};
    check(cuLaunchKernel(ff, 4096, 1, 1, 256, 1, 1, 0, nullptr, fa, nullptr), "fill_chain");
    check(cuCtxSynchronize(), "fill sync");
    uint32_t warm = 1000;
    void* ca[] = {&buf, (void*)&warm, &out};
    check(cuLaunchKernel(fc, 1, 1, 1, 1, 1, 1, 0, nullptr, ca, nullptr), "chase warm");
    check(cuCtxSynchronize(), "chase sync");
    void* cb[] = {&buf, (void*)&chases, &out};
    const auto t0 = std::chrono::steady_clock::now();
    check(cuLaunchKernel(fc, 1, 1, 1, 1, 1, 1, 0, nullptr, cb, nullptr), "chase");
    check(cuCtxSynchronize(), "chase sync");
    const double sec = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    cuMemFree(buf); cuMemFree(out); cuModuleUnload(mod);
    return sec * 1e9 / chases;
}

// The RXE1 module: the dataset builder of IgneumHash v1 (the pinned R0A builder) at the record's cache and parents, the
// RXE1 search at R, K, B. The tune document's read, minblocks and slots apply as for v1.
void engine::rx_load() {
    const uint64_t n_cache = rxp_.cache_bytes / 128;
    auto def = [](const char* name, uint64_t v) { return std::string("#define ") + name + " " + std::to_string(v) + "u\n"; };
    std::string defs = def("IH_VERSION", 1) + def("IH_N_ITEMS", ih_items_) + def("IH_SLICE_WORDS", 0) + def("IH_ROUNDS", 64) + def("IH_ITEM_CHAIN", 1) +
                       def("IH_N_CACHE", n_cache) + def("IH_PARENTS", rxp_.parents) + def("IH_PARAMS_LEN", 48) + def("IH_R1", 7) + def("IH_R2", 17) +
                       "#define IH_OP1_ADD 0\n#define IH_OP2_XOR 0\n#define IH_LANES 8u\n" +
                       def("IH_TUNE_READ", tune("read", 0)) + def("IH_TUNE_MINBLOCKS", tune("minblocks", 0)) +
                       def("RX_R", rxp_.rounds) + def("RX_K", rxp_.registers) + def("RX_B", rxp_.blocks) + def("RX_N_ITEMS", ih_items_) +
                       def("RX_SCHEMA", rxp_.schema >= 3 ? rxp_.schema : 2) + def("RX_G", rxp_.group) + def("RX_ROUTE", rxp_.route) +
                       def("RX_C", rxp_.chunks) + def("RX_PAYLOAD", rxp_.payload_bytes) + def("RX_MODE", rxp_.mode);
    ih_slots_ = static_cast<uint32_t>(tune("slots", 4096));
    const std::string key = "rxe1-s" + std::to_string(rxp_.schema) + "m" + std::to_string(rxp_.mode) + "-g" + std::to_string(rxp_.group) + "u" + std::to_string(rxp_.route) + "x" + std::to_string(rxp_.chunks) + "y" + std::to_string(rxp_.payload_bytes) +
                            "-r" + std::to_string(rxp_.rounds) + "-k" + std::to_string(rxp_.registers) + "-b" + std::to_string(rxp_.blocks) + "-n" + std::to_string(ih_items_) +
                            "-c" + std::to_string(n_cache) + "-p" + std::to_string(rxp_.parents) + "-t" + std::to_string(tune("read", 0)) + "m" + std::to_string(tune("minblocks", 0));
    if (static_mod_) { cuModuleUnload(static_mod_); static_mod_ = nullptr; }
    bool ptx = false;
    const std::string image = compile(device_source_rxe1(defs), key, ptx);
    check(cuModuleLoadData(&static_mod_, image.data()), "load rxe1 module");
    code_bytes_ = image.size();
    check(cuModuleGetFunction(&f_ih_gen_, static_mod_, "ih_dataset_gen"), "ih_dataset_gen"); record_kernel(f_ih_gen_, "ih_dataset_gen");
    check(cuModuleGetFunction(&f_rx_search_, static_mod_, "rx_search"), "rx_search"); record_kernel(f_rx_search_, "rx_search");
}

// The epoch's schedule from the reference's dump (section 8, host-generated), one u32 per (j, b, d): src1 | src2 << 8 |
// rotate << 16 | opcode << 24, into the module's constant memory; under M (B = 0) nothing is uploaded.
void engine::rx_upload_schedule(uint64_t epoch, const uint8_t epoch_seed[32]) {
    if (rxp_.blocks == 0) return;
    if (rxp_.mode == 3 && rxp_.route != 2) return;  // P2: the programme is per nonce, generated on the device; nothing epoch-bound to upload
    int st = 0;
    rxe1_ctx* c = rxe1_ctx_create(&rxp_, epoch_seed, 0, &st);
    if (!c) throw std::runtime_error("rxe1: schedule context failed");
    uint8_t cid[8]; for (int i = 0; i < 8; ++i) cid[i] = static_cast<uint8_t>(rxp_.chain_id >> (8 * i));
    std::string text;
    {
        FILE* f = std::tmpfile();
        if (!f) { rxe1_ctx_free(c); throw std::runtime_error("rxe1: no temporary file for the schedule"); }
        rxe1_schedule(c, cid, 8, epoch, f);
        std::fflush(f); const long len = std::ftell(f); std::rewind(f);
        text.resize(len > 0 ? static_cast<size_t>(len) : 0);
        if (len > 0 && std::fread(&text[0], 1, static_cast<size_t>(len), f) != static_cast<size_t>(len)) { std::fclose(f); rxe1_ctx_free(c); throw std::runtime_error("rxe1: schedule read"); }
        std::fclose(f);
    }
    rxe1_ctx_free(c);
    const size_t entries = static_cast<size_t>(rxp_.rounds) * rxp_.blocks * rxp_.registers;
    std::vector<uint32_t> sched(entries, 0);
    size_t pos = 0, filled = 0;
    auto num = [&](const char* key) -> long {  // the next "key": number after pos
        const std::string k = std::string("\"") + key + "\": ";
        const size_t at = text.find(k, pos);
        if (at == std::string::npos) return -1;
        pos = at + k.size();
        return std::strtol(text.c_str() + pos, nullptr, 10);
    };
    while (true) {
        const long j = num("j"); if (j < 0) break;
        const long b = num("b"), d = num("d"), s1 = num("src1"), s2 = num("src2"), rot = num("rotate"), op = num("opcode");
        if (b < 0 || d < 0 || s1 < 0 || s2 < 0 || rot < 0 || op < 0) break;
        const size_t idx = (static_cast<size_t>(j) * rxp_.blocks + b) * rxp_.registers + d;
        if (idx >= entries) throw std::runtime_error("rxe1: schedule index outside the record");
        sched[idx] = static_cast<uint32_t>(s1) | (static_cast<uint32_t>(s2) << 8) | (static_cast<uint32_t>(rot) << 16) | (static_cast<uint32_t>(op) << 24);
        ++filled;
    }
    if (rxp_.mode != 3) {
        if (filled != entries) throw std::runtime_error("rxe1: the schedule dump has " + std::to_string(filled) + " descriptors, the record needs " + std::to_string(entries));
        CUdeviceptr d_s = 0; size_t sz = 0;
        check(cuModuleGetGlobal(&d_s, &sz, static_mod_, "c_rx_sched"), "c_rx_sched");
        if (sz < entries * 4) throw std::runtime_error("rxe1: the constant schedule buffer is smaller than the record");
        check(cuMemcpyHtoD(d_s, sched.data(), entries * 4), "upload schedule");
    }
    if (rxp_.schema >= 3 && rxp_.route == 2) {
        // "routes": [{"j", "k", "off", "src", "rot", "op"}, ...] after the descriptors
        const size_t rn = static_cast<size_t>(rxp_.rounds) * rxp_.registers;
        std::vector<uint32_t> routes(rn, 0); size_t rf = 0;
        pos = text.find("\"routes\"", 0); if (pos == std::string::npos) throw std::runtime_error("rxe1: the schedule dump carries no routes under route 2");
        while (true) {
            const long j = num("j"); if (j < 0) break;
            const long k = num("k"), off = num("off"), src = num("src"), rot = num("rot"), op = num("op");
            if (k < 0 || off < 0 || src < 0 || rot < 0 || op < 0) break;
            const size_t idx = static_cast<size_t>(j) * rxp_.registers + k;
            if (idx >= rn) throw std::runtime_error("rxe1: route index outside the record");
            routes[idx] = static_cast<uint32_t>(off) | (static_cast<uint32_t>(src) << 8) | (static_cast<uint32_t>(rot) << 16) | (static_cast<uint32_t>(op) << 24);
            ++rf;
        }
        if (rf != rn) throw std::runtime_error("rxe1: the dump has " + std::to_string(rf) + " routes, the record needs " + std::to_string(rn));
        CUdeviceptr d_r = 0; size_t rsz = 0;
        check(cuModuleGetGlobal(&d_r, &rsz, static_mod_, "c_rx_route"), "c_rx_route");
        if (rsz < rn * 4) throw std::runtime_error("rxe1: the constant route buffer is smaller than the record");
        check(cuMemcpyHtoD(d_r, routes.data(), rn * 4), "upload routes");
    }
}

void engine::release_dataset() {
    make_current();
    if (d_dag_) { cuMemFree(d_dag_); d_dag_ = 0; }
    if (d_cache_) { cuMemFree(d_cache_); d_cache_ = 0; }
    cache_bytes_ = dag_bytes_ = 0;
    cur_epoch_ = UINT64_MAX;
}

bool engine::prepare(uint64_t height, std::string& reason, double* ms_out, uint64_t* bytes_out) {
    make_current();
    if (static_deferred_ && !static_mod_ && !ih_ && !rx_) load_static_module();
    // version 9: a new period means a new programme, so the module is rebuilt with it compiled in (the dataset is untouched)
    if (ih_ && ihp_.version >= 9 && ihp_.version <= 18 && ihp_.period_blocks && height / ihp_.period_blocks != ih_r0d_period_) {   // version 17 bakes its period key in like 15 and 16
        ih_r0d_period_ = height / ihp_.period_blocks; ih_r0d_height_ = height;
        ih_load(ih_cell_);
    }
    int st = 0;
    const uint64_t epoch = ignpow_epoch_of(algo_.c_str(), params(), height, &st);
    if (st != IGNPOW_OK) { reason = "height rejected by the reference"; return false; }
    if (ih_ && ((ihp_.version == 16 && !(ihp_.program_distance & 8388608)) || ihp_.version == 17 || ihp_.version == 18)) { cur_epoch_ = epoch; if (ms_out) *ms_out = 0; if (bytes_out) *bytes_out = 0; return true; }   // version 16: no cache, no dataset (the period module above is the whole prepare)
    if (epoch == cur_epoch_ && (d_dag_ || (rx_ && rxp_.dataset_bytes == 0))) { ih_st_fill(); if (ms_out) *ms_out = 0; if (bytes_out) *bytes_out = cache_bytes_ + dag_bytes_; return true; }
    const auto t0 = std::chrono::steady_clock::now();
    budget b = budget_for(height);
    if (!b.can_cold) {
        reason = "dataset does not fit: need " + std::to_string(b.fit_cold) + " bytes (cache " + std::to_string(b.cache) +
                 ", dataset " + std::to_string(b.dataset) + ", scratch " + std::to_string(b.scratch) + ", margin " +
                 std::to_string(b.margin) + "), free " + std::to_string(b.free_before + cache_bytes_ + dag_bytes_) +
                 " of " + std::to_string(b.total);
        return false;
    }
    release_dataset();  // v0: cold transitions only; warm is a later policy switch
    if (rx_) {
        // the harness epoch seed (REFERENCE-NOTES.md: H("RXE1/harness-epoch", [LE64(chain_id), LE64(epoch)])), the cache
        // from the reference's light context, the dataset by the R0A v1 builder on the device keyed with that seed
        uint8_t E[32];
        rxe1_harness_epoch_seed(rxp_.chain_id, epoch, E);
        ignpow_ctx* ref = ignpow_ctx_create(algo_.c_str(), params(), height, 0, &st);
        if (!ref) { reason = "reference context failed"; return false; }
        const void* lc = nullptr; uint64_t lc_bytes = 0; uint32_t item = 0;
        const uint64_t dataset = rxp_.dataset_bytes;
        if (dataset) {
            ignpow_ctx_light_cache(ref, &lc, &lc_bytes, &item);
            if (lc_bytes != rxp_.cache_bytes || item != 128) { ignpow_ctx_free(ref); reason = "the reference's light cache has another shape than the record"; return false; }
        }
        try {
            check(cuMemcpyHtoD(d_seed_, E, 32), "upload seed");
            if (env_len_) check(cuMemcpyHtoD(d_env_ + env_len_, E, 32), "upload seed to the envelope (bit 9)");
            if (dataset) {  // schema 5 (dataset_bytes 0, section 11): no cache, no dataset; E still binds the seed and the programme
            check(cuMemAlloc(&d_cache_, lc_bytes), "alloc cache");
            cache_bytes_ = lc_bytes;
            check(cuMemcpyHtoD(d_cache_, lc, lc_bytes), "upload cache");
            check(cuMemAlloc(&d_dag_, dataset), "alloc dataset");
            dag_bytes_ = dataset;
            const uint32_t n = static_cast<uint32_t>(dataset / ih_item_bytes_);
            const uint32_t chunk = 1u << 17;
            std::fprintf(stderr, "building the dataset, 0 of %u items (%llu bytes) on %s\n", n, (unsigned long long)dataset, info_.arch_option.c_str());
            uint32_t launched = 0;
            for (uint32_t start = 0; start < n; start += chunk) {
                const uint32_t count = std::min(chunk, n - start);
                void* args[] = {&d_seed_, &d_cache_, &d_dag_, (void*)&start, (void*)&count};
                check(cuLaunchKernel(f_ih_gen_, (count * 8 + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch ih_dataset_gen");
                if ((++launched & 7u) == 0) { check(cuCtxSynchronize(), "ih_dataset_gen sync"); std::fprintf(stderr, "building the dataset, %u of %u items\n", std::min(start + count, n), n); }   // the launches are queued asynchronously: a sync every eighth so the line speaks (the product team's handshake, 12 Oct 2026)
            }
            check(cuCtxSynchronize(), "ih_dataset_gen sync");
            std::fprintf(stderr, "built the dataset, %u of %u items\n", n, n);
            }  // dataset
            uint8_t ctx[80];  // chain_id LE64 || pid || epoch_number LE64 || epoch_seed
            for (int i = 0; i < 8; ++i) ctx[i] = static_cast<uint8_t>(rxp_.chain_id >> (8 * i));
            std::memcpy(ctx + 8, rx_pid_, 32);
            for (int i = 0; i < 8; ++i) ctx[40 + i] = static_cast<uint8_t>(epoch >> (8 * i));
            std::memcpy(ctx + 48, E, 32);
            check(cuMemcpyHtoD(d_env_, ctx, 80), "upload rx context");
            rx_upload_schedule(epoch, E);
        } catch (...) { ignpow_ctx_free(ref); release_dataset(); throw; }
        ignpow_ctx_free(ref);
        cur_epoch_ = epoch;
        const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
        if (ms_out) *ms_out = ms;
        if (bytes_out) *bytes_out = cache_bytes_ + dag_bytes_;
        return true;
    }
    if (ih_) {
        std::fprintf(stderr, "building the epoch's cache on the host (the reference) for height %llu\n", (unsigned long long)height);
        ignpow_ctx* ref = ignpow_ctx_create(algo_.c_str(), params(), height, 0, &st);
        if (!ref) { reason = "reference context failed"; return false; }
        std::fprintf(stderr, "built the epoch's cache\n");
        // E (the epoch seed, the key of every item) from the reference; under versions 1 and 2 the light cache is
        // the epoch's cache (CACHE_BYTES of 128-byte items, built sequentially on the host), under version 0 it is E
        uint8_t E[32], D[32], cdig[32], ddig[32]; uint64_t bms = 0; int hasd = 0;
        if (ignh_dataset_info(ref, E, D, cdig, ddig, &bms, &hasd) != IGNPOW_OK) { ignpow_ctx_free(ref); reason = "the reference gives no dataset identity"; return false; }
        { static const char* hx = "0123456789abcdef"; dataset_digest_.clear(); for (int i = 0; i < 32; ++i) { dataset_digest_ += hx[D[i] >> 4]; dataset_digest_ += hx[D[i] & 15]; } }
        const void* lc = nullptr; uint64_t lc_bytes = 0; uint32_t item = 0;
        ignpow_ctx_light_cache(ref, &lc, &lc_bytes, &item);
        const bool walk = ihp_.version >= 1;
        if (walk ? (lc_bytes != ihp_.cache_bytes || item != 128) : (lc_bytes != 32 || item != 32)) { ignpow_ctx_free(ref); reason = "the reference's light cache has another shape than the variant"; return false; }
        const uint64_t dataset = ignpow_dataset_bytes(algo_.c_str(), params(), height, &st);
        try {
            if (ihp_.version == 2 && static_cast<uint32_t>(epoch % ihp_.schedule) != ih_cell_) ih_load(static_cast<uint32_t>(epoch % ihp_.schedule));
            check(cuMemcpyHtoD(d_seed_, E, 32), "upload seed");
            if (env_len_) check(cuMemcpyHtoD(d_env_ + env_len_, E, 32), "upload seed to the envelope (bit 9)");
            std::memcpy(ih_E_, E, 32);   // the period seed of Anvil's compiled form is drawn on the host from E
            if (walk) {
                check(cuMemAlloc(&d_cache_, lc_bytes), "alloc cache");
                cache_bytes_ = lc_bytes;
                check(cuMemcpyHtoD(d_cache_, lc, lc_bytes), "upload cache");
            }
            check(cuMemAlloc(&d_dag_, dataset), "alloc dataset");
            dag_bytes_ = dataset;
            if (!d_scratch_ && ih_scratch_words_) check(cuMemAlloc(&d_scratch_, static_cast<uint64_t>(ih_slots_) * ih_scratch_words_ * 4), "alloc scratch");
            if (!d_scratch_ && ihp_.version >= 10 && ihp_.version <= 14 && !tune("r0eshared", 0)) check(cuMemAlloc(&d_scratch_, static_cast<uint64_t>(ih_slots_) * tune("r0etable", ihp_.version >= 12 ? ihp_.table_kib * 256u : ihp_.version == 11 ? 1024 : 4096) * 4), "alloc the R0E gather tables");
            if (!d_scratch_ && tune("grind", 0)) { grind_words_ = 1u << 20; check(cuMemAlloc(&d_scratch_, grind_words_ * 4), "alloc grind"); }   // one cycle count per nonce of a 1 M launch
            const uint32_t n = static_cast<uint32_t>(dataset / ih_item_bytes_);  // items of the record's width (the /128 count overran the buffer at W > 128)
            const uint32_t chunk = walk ? (1u << 17) : (1u << 20);  // the parent walk runs eight lanes per item
            for (uint32_t start = 0; start < n; start += chunk) {
                const uint32_t count = std::min(chunk, n - start);
                void* args[] = {&d_seed_, &d_cache_, &d_dag_, (void*)&start, (void*)&count};
                const uint32_t threads = walk ? count * 8 : count;
                check(cuLaunchKernel(f_ih_gen_, (threads + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch ih_dataset_gen");
            }
            check(cuCtxSynchronize(), "ih_dataset_gen sync");
        } catch (...) { ignpow_ctx_free(ref); release_dataset(); throw; }
        ignpow_ctx_free(ref);
        cur_epoch_ = epoch;
        ih_st_fill();   // the shared form's period table, once the dataset stands (the igneumhash path returns here, not at prepare's end: the 4090 fault of 23:10)
        const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
        if (ms_out) *ms_out = ms;
        if (bytes_out) *bytes_out = cache_bytes_ + dag_bytes_;
        return true;
    }
    if (al_) {
        // the height's element table: N x 32 bytes, generated on the device
        const uint64_t dataset = ignpow_dataset_bytes(algo_.c_str(), params(), height, &st);
        const uint32_t n = static_cast<uint32_t>(dataset / 32);
        try {
            check(cuMemAlloc(&d_dag_, dataset), "alloc table");
            dag_bytes_ = dataset;
            const uint32_t h32 = static_cast<uint32_t>(height);
            const uint32_t chunk = 1u << 20;
            for (uint32_t start = 0; start < n; start += chunk) {
                const uint32_t count = std::min(chunk, n - start);
                void* args[] = {&d_dag_, (void*)&n, (void*)&h32, (void*)&start, (void*)&count};
                check(cuLaunchKernel(f_al_gen_, (count + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch al_table_gen");
            }
            check(cuCtxSynchronize(), "al_table_gen sync");
        } catch (...) { release_dataset(); throw; }
        al_n_ = n;
        cur_epoch_ = epoch;
        const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
        if (ms_out) *ms_out = ms;
        if (bytes_out) *bytes_out = dag_bytes_;
        return true;
    }
    ignpow_ctx* ref = ignpow_ctx_create(algo_.c_str(), params(), height, 0, &st);
    if (!ref) { reason = "reference light context failed"; return false; }
    const void* cache = nullptr;
    uint64_t cache_bytes = 0;
    uint32_t item = 0;
    ignpow_ctx_light_cache(ref, &cache, &cache_bytes, &item);
    const uint64_t dataset = ignpow_dataset_bytes(algo_.c_str(), params(), height, &st);
    const uint32_t items_512 = static_cast<uint32_t>(dataset / 64);
    try {
        check(cuMemAlloc(&d_cache_, cache_bytes), "alloc cache");
        cache_bytes_ = cache_bytes;
        check(cuMemcpyHtoD(d_cache_, cache, cache_bytes), "upload cache");
        check(cuMemAlloc(&d_dag_, dataset), "alloc dataset");
        dag_bytes_ = dataset;
        const uint32_t cache_items = static_cast<uint32_t>(cache_bytes / 64);
        const uint32_t chunk = 1u << 20;  // 512-bit items per launch (64 MiB)
        for (uint32_t start = 0; start < items_512; start += chunk) {
            const uint32_t count = std::min(chunk, items_512 - start);
            void* args[] = {&d_cache_, (void*)&cache_items, &d_dag_, (void*)&start, (void*)&count};
            const unsigned grid = (count + 255) / 256;
            check(cuLaunchKernel(f_dag_gen_, grid, 1, 1, 256, 1, 1, 0, nullptr, args, nullptr), "launch dag_gen");
        }
        check(cuCtxSynchronize(), "dag_gen sync");
    } catch (...) {
        ignpow_ctx_free(ref);
        release_dataset();
        throw;
    }
    ignpow_ctx_free(ref);
    dag_items_2048_ = static_cast<uint32_t>(dataset / 256);
    fh_items_1024_ = static_cast<uint32_t>(dataset / 128);
    cur_epoch_ = epoch;
    ih_st_fill();
    const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
    if (ms_out) *ms_out = ms;
    if (bytes_out) *bytes_out = cache_bytes_ + dag_bytes_;
    return true;
}

// versions 12 to 14, the shared form: the period's shared table (shared_kib x 256 words) filled on the device by the chained walk over
// the dataset from the period's seed (the reference's r12_shared_table), once per period after the dataset and the period's module stand
void engine::ih_st_fill() {
    if (!(ih_ && ihp_.version >= 12 && ihp_.shared_kib) || !d_dag_ || !static_mod_) return;
    if (d_r0e_st_ && ih_st_period_ == ih_r0d_period_) return;
    const uint64_t words = uint64_t(ihp_.shared_kib) * 256u;
    if (!d_r0e_st_) check(cuMemAlloc(&d_r0e_st_, words * 4), "alloc shared table");
    if (!d_r0e_st_seed_) check(cuMemAlloc(&d_r0e_st_seed_, 32), "alloc shared seed");
    uint8_t seed[32];
    if (ignh_r12_shared_seed(&ihp_, ih_r0d_height_, seed) != IGNPOW_OK) throw std::runtime_error("igneumhash: the reference refused the shared table's seed");
    check(cuMemcpyHtoD(d_r0e_st_seed_, seed, 32), "shared seed upload");
    CUfunction f = nullptr;
    check(cuModuleGetFunction(&f, static_mod_, "ih_r0e_st_fill"), "ih_r0e_st_fill");
    void* args[] = {&d_dag_, &d_r0e_st_seed_, &d_r0e_st_};
    check(cuLaunchKernel(f, 1, 1, 1, 8, 1, 1, 0, nullptr, args, nullptr), "launch ih_r0e_st_fill");
    check(cuCtxSynchronize(), "shared table fill");
    ih_st_period_ = ih_r0d_period_;
}

// Version 8 bit 10 (SPEC-R0A-v0.md 0.9): the bond secret's eight little-endian words behind the header and target in the params
// buffer. The test secret sk = BLAKE3("IgneumHash/R0A/test-sk" || header) is the one the keyed vector class and the bench run
// under (a vector set's shares are public by construction); a real bond's secret would take the same eight words. Zero without
// the bit, so every other document's buffer reads as before.
void engine::set_secret(const uint8_t* sk32) {
    ih_sk_set_ = sk32 != nullptr;
#ifndef _WIN32
    // F-53 (12 Oct 2026): the host copy is locked in memory for as long as it holds a secret, so it never reaches swap on a rented host
    // (mlock takes the page under it; a failure to lock is not raised: the copy is still wiped by wipe_secret and the core limit is 0)
    if (sk32) mlock(ih_sk_, sizeof ih_sk_);
#endif
    if (sk32) std::memcpy(ih_sk_, sk32, 32); else std::memset(ih_sk_, 0, 32);
}

void engine::wipe_secret() {
    // the host copy first (volatile so the stores are not elided), then the device's eight words (params + 16): the card holds
    // no secret between jobs; a failure to reach the device is not raised here (the exit path calls this with the context going)
    volatile uint8_t* p = ih_sk_;
    for (int i = 0; i < 32; ++i) p[i] = 0;
    ih_sk_set_ = false;
#ifndef _WIN32
    munlock(ih_sk_, sizeof ih_sk_);   // F-53: the lock released after the zeroing, never before
#endif
    if (d_params_ && ctx_) {
        make_current();
        cuMemsetD32(d_params_ + 16 * sizeof(uint32_t), 0, 8);
        cuCtxSynchronize();
    }
}

void engine::ih_secret_words(const uint8_t header[32], uint32_t out[8]) const {
    uint8_t sk[32] = {};
    if (keyed_document()) { if (ih_sk_set_) std::memcpy(sk, ih_sk_, 32); else ignh_test_sk(header, sk); }   // the job's secret (keyed-job) first, else the test secret; bit 10 on 21 and 25 too
    std::memcpy(out, sk, 32);
}

uint64_t engine::search(uint64_t height, const uint8_t header[32], const uint8_t target[32], uint64_t start,
                        uint64_t count, std::vector<hit>& out, const std::function<bool()>& should_stop) {
    make_current();
    if (!d_dag_ && !(rx_ && rxp_.dataset_bytes == 0) && !(ih_ && ((ihp_.version == 16 && !(ihp_.program_distance & 8388608)) || ihp_.version == 17 || ihp_.version == 18))) throw std::runtime_error("search without a dataset");  // schema 5 and version 16 search without one
    int st = 0;
    if (ignpow_epoch_of(algo_.c_str(), params(), height, &st) != cur_epoch_) throw std::runtime_error("search at another epoch than prepared");
    CUfunction fn = nullptr;
    if (al_) fn = f_al_search_;
    else if (fh_) fn = f_fh_search_;
    else if (rx_) fn = f_rx_search_;
    else if (ih_) fn = f_ih_search_;
    else {
        CUmodule mod = module_for_period(height / (pe_ ? 50 : 3));
        check(cuModuleGetFunction(&fn, mod, "progpow_search"), "progpow_search"); record_kernel(fn, "progpow_search");
    }
    const uint32_t h32 = static_cast<uint32_t>(height);

    uint32_t params[kParamsWords];
    std::memcpy(params, header, 32);
    for (int i = 0; i < 8; ++i)
        params[8 + i] = (uint32_t(target[4 * i]) << 24) | (uint32_t(target[4 * i + 1]) << 16) | (uint32_t(target[4 * i + 2]) << 8) | uint32_t(target[4 * i + 3]);
    ih_secret_words(header, params + 16);
    check(cuMemcpyHtoD(d_params_, params, sizeof params), "upload params");

    uint64_t done = 0;
    std::vector<uint32_t> res(1 + kMaxResults * kResultWords);
    // Work items: [nonce, n) ranges. A launch whose hits overflow the result buffer is not read as partial: the range
    // is split in halves and both are searched again, so every share of the range reaches the caller (tails class).
    std::vector<std::pair<uint64_t, uint32_t>> pending;
    while (done < count || !pending.empty()) {
        if (should_stop && should_stop()) break;
        uint64_t nonce; uint32_t n;
        if (!pending.empty()) { nonce = pending.back().first; n = pending.back().second; pending.pop_back(); }
        else { n = static_cast<uint32_t>(std::min<uint64_t>(batch_, count - done)); nonce = start + done; done += n; }  // wraps by design
        const bool fg3_on = ih_ && (ihp_.version == 21 || ihp_.version == 24 || ihp_.version == 25) && tune("fg3", (ihp_.version == 25 || ihp_.version == 21) ? 1 : 0);   // bit 10 on version 21: the compiled keyed form (F-504's ruled form in forge3_shapes.hpp); the interpreted walk carries no keyed read
        if (ih_keyed_steps_ && !fg3_on) throw std::runtime_error("igneumhash: features bit 10 (the bond-keyed item-read steps) runs the compiled form alone on version 21 (fg3=1)");   // versions 25 and 21 run the whole-entry compiled form by default (version 25's only port; version 21's other path reads MISMATCH 98/98 on the live k2-lean-p64 document, 15 Oct 2026 22:3x UK, and Ember launches the worker bare with no tune)
        const engine::fg3_mod* fg3m = nullptr;
        if (fg3_on && ihp_.period_blocks) {
            // the chain form (F-141): the module keyed on the period of blocks (height / period_blocks) and the anchor in ihp_.program_anchor
            // (set_anchor); the next period's module compiles on a thread while this one runs (tune ahead, default 1); no nonce splitting
            const uint64_t per = height / ihp_.period_blocks;
            fg3m = &fg3_batch(header, per);
            const uint64_t ahead = tune("ahead", 1);
            for (uint64_t k = 1; k <= ahead; ++k) {
                const uint64_t nb = per + k;
                if (fg3_ahead_.count(nb)) continue;
                bool busy = false; for (auto& a : fg3_ahead_) if (a.second.second.wait_for(std::chrono::seconds(0)) != std::future_status::ready) busy = true;
                if (busy) break;
                std::string nkey, nsrc; fg3_source(header, nb, nkey, nsrc);
                bool cached = false; for (auto& m : fg3_mods_) if (m.key == nkey) cached = true;
                if (!cached && !nsrc.empty()) fg3_ahead_.emplace(nb, std::make_pair(nkey, std::async(std::launch::async, [this, nsrc, nkey]() { bool ptx = false; return compile(nsrc, "fg3-" + nkey, ptx); })));
            }
            for (auto it = fg3_ahead_.begin(); it != fg3_ahead_.end();) { if (it->first <= per) { it->second.second.wait(); it = fg3_ahead_.erase(it); } else ++it; }
        } else if (fg3_on) {
            const uint64_t bl = ihp_.keyed_rounds, bend = ((nonce >> bl) + 1) << bl, room = bend - nonce;
            if (tune("fg2fix", 0)) fg3m = &fg3_batch(header, 0);
            else {
                if (n > room) { pending.emplace_back(nonce + room, n - static_cast<uint32_t>(room)); n = static_cast<uint32_t>(room); }
                fg3m = &fg3_batch(header, nonce >> bl);
                // the full form as mined: the next batch's module compiles on its own thread while this batch runs (the first batch of a
                // header waits one compile; a product miner compiles the next template's first batch ahead from the announced parent)
                // the batches ahead on compile threads (tune ahead, default 1: one compile at a time beside the run; the vectors verb's
                // one-nonce searches never prefetch, a launch under the batch's width is not the mined form)
                const uint64_t ahead = (count >= (1ull << bl)) ? (tune("ahead", 0) ? tune("ahead", 0) : 1u) : 0u;
                for (uint64_t k = 1; k <= ahead; ++k) {   // across search() calls too: the bench's calls walk the nonce range in launch-sized steps
                    const uint64_t nb = (nonce >> bl) + k;
                    if (fg3_ahead_.count(nb)) continue;
                    bool busy = false; for (auto& a : fg3_ahead_) if (a.second.second.wait_for(std::chrono::seconds(0)) != std::future_status::ready) busy = true;
                    if (busy && ahead == 1) break;   // one compile thread at a time
                    std::string nkey, nsrc; fg3_source(header, nb, nkey, nsrc);
                    bool cached = false; for (auto& m : fg3_mods_) if (m.key == nkey) cached = true;
                    if (!cached) fg3_ahead_.emplace(nb, std::make_pair(nkey, std::async(std::launch::async, [this, nsrc, nkey]() { bool ptx = false; return compile(nsrc, "fg3-" + nkey, ptx); })));
                }
                for (auto it = fg3_ahead_.begin(); it != fg3_ahead_.end();) { if (it->first <= (nonce >> bl)) { it->second.second.wait(); it = fg3_ahead_.erase(it); } else ++it; }   // batches already passed
            }
        } else if (ih_ && ihp_.version == 21 && tune("fg2c", 0)) {
            // the compiled per-batch form: a launch stays inside one period (nonce >> batch_log2); the remainder returns to the queue.
            // fg2fix=1 is the FIGURE form (the GPU team, 15 Oct 2026): every launch runs the shapes of batch 0 (the first period's module) over
            // the whole range, so a throughput window measures the step work without a compile every 4,096 nonces; its digests are not
            // the document's (a row under fg2fix is DERIVED; exactness is read on the vectors verb, which never sets it)
            const uint64_t bl = ihp_.keyed_rounds, bend = ((nonce >> bl) + 1) << bl, room = bend - nonce;
            if (tune("fg2fix", 0)) fn = fg2_batch_fn(header, 0);
            else { if (n > room) { pending.emplace_back(nonce + room, n - static_cast<uint32_t>(room)); n = static_cast<uint32_t>(room); } fn = fg2_batch_fn(header, nonce >> bl); }
        }
        check(cuMemsetD32(d_results_, 0, 1), "clear results");
        check(cuMemsetD32(d_results_ + (1 + kMaxResults * kResultWords) * sizeof(uint32_t), 0, 1), "clear nonce counter");
        const uint32_t max_results = kMaxResults;
        if (al_) {
            void* args[] = {(void*)&nonce, (void*)&n, &d_dag_, (void*)&al_n_, (void*)&h32, &d_params_, &d_results_, (void*)&max_results};
            check(cuLaunchKernel(fn, (n + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch al_search");
        } else if (fh_) {
            void* args[] = {(void*)&nonce, (void*)&n, &d_dag_, (void*)&fh_items_1024_, &d_params_, &d_results_, (void*)&max_results};
            check(cuLaunchKernel(fn, (n * 8 + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch fishhash_search");  // eight lanes per nonce
        } else if (rx_) {
            const uint32_t items = static_cast<uint32_t>(ih_items_);
            CUdeviceptr walk_buf = tune("ablate", 0) == 6 ? d_cache_ : d_dag_;   // ablation 6: the walk's reads from the cache (SPEC-R0E-v0.md 2a)
            void* args[] = {(void*)&nonce, (void*)&n, &walk_buf, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_results_, (void*)&max_results};
            check(cuLaunchKernel(fn, (ih_slots_ * 16) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch rx_search");  // sixteen lanes per nonce
        } else if (fg3_on) {
            // the whole-entry compiled form: prep (one thread an attempt), the step work (one or two attempts a warp), the mix (four lanes an attempt)
            const uint32_t fp_mode = ((ihp_.program_distance >> 1) & 7u) | (((ihp_.program_distance >> 16) & 1u) << 3);
            const uint32_t spw = (fp_mode == 6 || fp_mode == 9) ? 0u : 16u, wnw = fp_mode == 6 ? 16u : 0u;
            const uint32_t L = ihp_.blocks, seqw4 = (((L + 3u) / 4u) + 3u) & ~3u, hww = ((L + 8u) + 3u) & ~3u;
            fg3_state_words_ = 16u + seqw4 + hww + 512u + 16u * spw + 16u * wnw + 16u + 8u * ih_keyed_steps_;   // F-504: the shares, eight words each, after the saved prefetch words
            const size_t need = static_cast<size_t>(n) * fg3_state_words_ * sizeof(uint32_t);
            if (need > fg3_state_bytes_) { if (d_fg3_state_) cuMemFree(d_fg3_state_); check(cuMemAlloc(&d_fg3_state_, need), "fg3 state buffer"); fg3_state_bytes_ = need; }
            const bool timed = tune("fg3time", 0) != 0;   // a debugging form: each kernel's milliseconds on stderr
            CUevent ev[4]; if (timed) for (auto& e : ev) check(cuEventCreate(&e, CU_EVENT_DEFAULT), "fg3 event");
            if (timed) cuEventRecord(ev[0], nullptr);
            void* pargs[] = {(void*)&nonce, (void*)&n, &d_params_, &d_env_, &d_fg3_state_};
            check(cuLaunchKernel(fg3m->prep, (n + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, pargs, nullptr), "launch ih_fg3_prep");
            if (timed) cuEventRecord(ev[1], nullptr);
            if (tune("regroup", 0) && fg3m->queue) {
                // the regrouping form: one entry a launch, the attempts queued by their next shape before each
                const size_t qbytes = (size_t)n * ihp_.program_ops * sizeof(uint32_t);
                if (qbytes > fg3_queue_bytes_) { if (d_fg3_queue_) cuMemFree(d_fg3_queue_); check(cuMemAlloc(&d_fg3_queue_, qbytes), "fg3 queues"); fg3_queue_bytes_ = qbytes; if (!d_fg3_counts_) check(cuMemAlloc(&d_fg3_counts_, 256 * sizeof(uint32_t)), "fg3 counts"); }
                for (uint32_t ei = 0; ei < ihp_.blocks; ++ei) {
                    check(cuMemsetD32(d_fg3_counts_, 0, 256), "fg3 counts clear");
                    void* qargs[] = {(void*)&n, (void*)&ei, &d_fg3_state_, &d_fg3_queue_, &d_fg3_counts_};
                    check(cuLaunchKernel(fg3m->queue, (n + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, qargs, nullptr), "launch ih_fg3_queue");
                    void* eargs[] = {(void*)&n, (void*)&ei, &d_dag_, &d_fg3_state_, &d_fg3_queue_, &d_fg3_counts_};
                    check(cuLaunchKernel(fg3m->entry, ih_slots_, 1, 1, 32, 1, 1, 0, nullptr, eargs, nullptr), "launch ih_fg3_entry");
                }
                void* aargs[] = {(void*)&n, &d_fg3_state_};
                check(cuLaunchKernel(fg3m->acc, (n * 16 + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, aargs, nullptr), "launch ih_fg3_acc");
            } else {
            void* sargs[] = {(void*)&nonce, (void*)&n, &d_dag_, &d_fg3_state_, &d_params_};   // the params buffer: the secret's words at a keyed step (F-504)
            check(cuLaunchKernel(fg3m->search, ih_slots_, 1, 1, 32, 1, 1, 0, nullptr, sargs, nullptr), "launch ih_search (fg3)");   // one warp a block, slots warps
            }
            if (timed) cuEventRecord(ev[2], nullptr);
            const uint32_t lanes_mix = 4;   // FG3_G at the 16-word scratchpad or the window (four chunks)
            void* margs[] = {(void*)&nonce, (void*)&n, &d_params_, &d_env_, &d_fg3_state_, &d_results_, (void*)&max_results};
            check(cuLaunchKernel(fg3m->mix, (n * lanes_mix + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, margs, nullptr), "launch ih_fg3_mix");
            if (timed) { cuEventRecord(ev[3], nullptr); cuEventSynchronize(ev[3]); float a = 0, b = 0, c = 0; cuEventElapsedTime(&a, ev[0], ev[1]); cuEventElapsedTime(&b, ev[1], ev[2]); cuEventElapsedTime(&c, ev[2], ev[3]); std::fprintf(stderr, "fg3time n %u prep %.2f ms search %.2f ms mix %.2f ms\n", n, a, b, c); for (auto& e : ev) cuEventDestroy(e); }
            if (tune("fg3dump", 0)) {   // a debugging form: the first attempt's state after the step work, one hex line on stderr
                check(cuCtxSynchronize(), "fg3 dump sync");
                std::vector<uint32_t> w(fg3_state_words_); check(cuMemcpyDtoH(w.data(), d_fg3_state_, w.size() * sizeof(uint32_t)), "fg3 dump read");
                std::string o = "fg3state E="; char b[12]; for (int i = 0; i < 32; ++i) { std::snprintf(b, sizeof b, "%02x", ih_E_[i]); o += b; } for (uint32_t x : w) { std::snprintf(b, sizeof b, " %08x", x); o += b; }
                std::fprintf(stderr, "%s\n", o.c_str());
            }
        } else if (ih_) {
            const uint32_t items = static_cast<uint32_t>(ih_items_);
            CUdeviceptr walk_buf = tune("ablate", 0) == 6 ? d_cache_ : d_dag_;   // ablation 6: the walk's reads from the cache (SPEC-R0E-v0.md 2a)
            if (ihp_.version >= 12 && ihp_.shared_kib) { ih_st_fill(); if (!d_r0e_st_) throw std::runtime_error("igneumhash: the shared form's period table is not on the device"); }
            if (tune("trace", 0) && !d_trace_) check(cuMemAlloc(&d_trace_, 16640 * sizeof(uint32_t)), "trace buffer");
            if (ihp_.version == 17 && !ih_tex17_) {
                // the static texture: 4096 x 4096 R32_UINT, texel (x, y) = fmix32(y * 4096 + x), built once on the host (64 MiB)
                std::vector<uint32_t> tx(size_t(4096) * 4096);
                for (uint32_t y = 0; y < 4096u; ++y) for (uint32_t x = 0; x < 4096u; ++x) { uint32_t h = y * 4096u + x; h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; tx[size_t(y) * 4096 + x] = h; }
                CUDA_ARRAY_DESCRIPTOR ad = {}; ad.Width = 4096; ad.Height = 4096; ad.Format = CU_AD_FORMAT_UNSIGNED_INT32; ad.NumChannels = 1;
                check(cuArrayCreate(&ih_tex17_arr_, &ad), "v17 texture array");
                CUDA_MEMCPY2D cp = {}; cp.srcMemoryType = CU_MEMORYTYPE_HOST; cp.srcHost = tx.data(); cp.srcPitch = 4096 * 4; cp.dstMemoryType = CU_MEMORYTYPE_ARRAY; cp.dstArray = ih_tex17_arr_; cp.WidthInBytes = 4096 * 4; cp.Height = 4096;
                check(cuMemcpy2D(&cp), "v17 texture upload");
                CUDA_RESOURCE_DESC rd = {}; rd.resType = CU_RESOURCE_TYPE_ARRAY; rd.res.array.hArray = ih_tex17_arr_;
                CUDA_TEXTURE_DESC td = {}; td.addressMode[0] = CU_TR_ADDRESS_MODE_CLAMP; td.addressMode[1] = CU_TR_ADDRESS_MODE_CLAMP; td.addressMode[2] = CU_TR_ADDRESS_MODE_CLAMP; td.filterMode = CU_TR_FILTER_MODE_POINT; td.flags = CU_TRSF_READ_AS_INTEGER;   // unnormalised coordinates; the integer texel read as its integer (the driver form of cudaReadModeElementType)
                check(cuTexObjectCreate(&ih_tex17_, &rd, &td, nullptr), "v17 texture object");
            }
            unsigned long long tex17 = static_cast<unsigned long long>(ih_tex17_);
            void* args17[] = {(void*)&nonce, (void*)&n, &walk_buf, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_results_, (void*)&max_results, (void*)&tex17};
            void* args[] = {(void*)&nonce, (void*)&n, &walk_buf, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_results_, (void*)&max_results, &d_r0e_st_, &d_trace_};
            if (f_r16_mix_) {   // version 15's split form: the state buffer (264 words an attempt) sized to the launch, its pointer in the module's global when it changes
                const size_t need = static_cast<size_t>(n) * 264u * sizeof(uint32_t);
                if (need > fg3_state_bytes_) { if (d_fg3_state_) cuMemFree(d_fg3_state_); check(cuMemAlloc(&d_fg3_state_, need), "r16 state buffer"); fg3_state_bytes_ = need; }
                if (r16_state_written_ != d_fg3_state_) { check(cuMemcpyHtoD(r16_state_global_, &d_fg3_state_, sizeof d_fg3_state_), "r16 state pointer"); r16_state_written_ = d_fg3_state_; }
            }
            check(cuLaunchKernel(fn, (ih_slots_ * ih_lanes_) / ih_block_, 1, 1, ih_block_, 1, 1, 0, nullptr, (ihp_.version == 17 || ihp_.version == 18 || ihp_.version == 20 || ihp_.version == 21 || ihp_.version == 24) ? args17 : args, nullptr), "launch ih_search");
            if (f_r16_mix_) { void* margs[] = {(void*)&nonce, (void*)&n, &d_params_, &d_env_, &d_results_, (void*)&max_results}; check(cuLaunchKernel(f_r16_mix_, (n + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, margs, nullptr), "launch ih_r16_mix"); }
            if (tune("trace", 0)) {   // the trace mode: nonce 0's lane state printed as one JSON line (compared with the reference's trace vectors)
                check(cuCtxSynchronize(), "trace sync");
                std::vector<uint32_t> t(16640); check(cuMemcpyDtoH(t.data(), d_trace_, t.size() * sizeof(uint32_t)), "trace read");
                auto regs = [&](size_t base) { std::string o = "["; for (int l = 0; l < 8; ++l) { o += (l ? ",[" : "["); for (int k = 0; k < 16; ++k) o += (k ? "," : "") + std::to_string(t[base + l * 16 + k]); o += "]"; } return o + "]"; };
                std::string o = "{\"ktrace\": {\"nonce\": \"" + std::to_string((unsigned long long)nonce) + "\", \"init\": " + regs(0) + ", \"rounds\": [";
                for (uint32_t r = 0; r < ihp_.rounds && r < 64; ++r) { const size_t b = 128 + r * 258; o += (r ? "," : "") + std::string("{\"r\": ") + std::to_string(r) + ", \"idx\": " + std::to_string(t[b]) + ", \"program_regs\": " + regs(b + 2) + ", \"regs\": " + regs(b + 130) + "}"; }
                std::printf("%s]}}\n", o.c_str()); std::fflush(stdout);
                if (d_trace_ops_) {
                    std::vector<uint32_t> q(129 * 128); check(cuMemcpyDtoH(q.data(), d_trace_ops_, q.size() * sizeof(uint32_t)), "trace ops read");
                    std::string p = "{\"ktrace_ops\": {\"nonce\": \"" + std::to_string((unsigned long long)nonce) + "\", \"slots\": [";
                    for (size_t sl = 0; sl < 129; ++sl) { p += sl ? ",[" : "["; for (size_t w = 0; w < 128; ++w) p += (w ? "," : "") + std::to_string(q[sl * 128 + w]); p += "]"; }
                    std::printf("%s]}}\n", p.c_str()); std::fflush(stdout);
                }
            }  // a persistent grid of ih_slots_ lane groups (family 7: the workgroup size)
        } else {
            void* args[] = {(void*)&nonce, (void*)&n, &d_dag_, (void*)&dag_items_2048_, &d_params_, &d_results_, (void*)&max_results};
            const unsigned grid = (n + 15) / 16;
            check(cuLaunchKernel(fn, grid, 1, 1, kBlock, 1, 1, 0, nullptr, args, nullptr), "launch search");
        }
        check(cuMemcpyDtoH(res.data(), d_results_, sizeof(uint32_t)), "read count");
        if (res[0] > kMaxResults && n > 1) {
            notes_ += "results overflow in one launch (" + std::to_string(res[0]) + " of " + std::to_string(n) + " nonces): split and searched again\n";
            const uint32_t h1 = n / 2;
            pending.emplace_back(nonce + h1, n - h1);
            pending.emplace_back(nonce, h1);
            continue;
        }
        const uint32_t hits = std::min<uint32_t>(res[0], kMaxResults);
        if (hits) {
            check(cuMemcpyDtoH(res.data(), d_results_, (1 + hits * kResultWords) * sizeof(uint32_t)), "read hits");
            for (uint32_t i = 0; i < hits; ++i) {
                const uint32_t* e = res.data() + 1 + i * kResultWords;
                hit h;
                h.nonce = (uint64_t(e[1]) << 32) | e[0];
                std::memcpy(h.mix, e + 2, 32);
                std::memcpy(h.hash, e + 10, 32);
                out.push_back(h);
            }
        }
    }
    return done;
}

void engine::search_all(uint64_t height, const uint8_t header[32], uint64_t start, uint32_t count, std::vector<hit>& out) {
    if (!d_dag_ && !(rx_ && rxp_.dataset_bytes == 0) && !(ih_ && ((ihp_.version == 16 && !(ihp_.program_distance & 8388608)) || ihp_.version == 17 || ihp_.version == 18))) throw std::runtime_error("search without a dataset");  // schema 5 and version 16 search without one
    if (count > (1u << 20)) throw std::runtime_error("search_all over 2^20 nonces");
    int st = 0;
    if (ignpow_epoch_of(algo_.c_str(), params(), height, &st) != cur_epoch_) throw std::runtime_error("search at another epoch than prepared");
    CUfunction fn = nullptr;
    if (al_) fn = f_al_search_;
    else if (fh_) fn = f_fh_search_;
    else if (rx_) fn = f_rx_search_;
    else if (ih_) fn = f_ih_search_;
    else {
        CUmodule mod = module_for_period(height / (pe_ ? 50 : 3));
        check(cuModuleGetFunction(&fn, mod, "progpow_search"), "progpow_search"); record_kernel(fn, "progpow_search");
    }
    const uint32_t h32 = static_cast<uint32_t>(height);
    uint32_t params[kParamsWords];
    std::memcpy(params, header, 32);
    for (int i = 8; i < 16; ++i) params[i] = 0xFFFFFFFFu;  // every hash passes
    ih_secret_words(header, params + 16);
    check(cuMemcpyHtoD(d_params_, params, sizeof params), "upload params");
    CUdeviceptr d_all = 0;
    const size_t words = 1 + size_t(count) * kResultWords;
    check(cuMemAlloc(&d_all, (words + 1) * sizeof(uint32_t)), "alloc all-results");  // + the device-side nonce counter (family 3)
    try {
        check(cuMemsetD32(d_all, 0, 1), "clear");
        check(cuMemsetD32(d_all + words * sizeof(uint32_t), 0, 1), "clear nonce counter");
        if (al_) {
            void* args[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&al_n_, (void*)&h32, &d_params_, &d_all, (void*)&count};
            check(cuLaunchKernel(fn, (count + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch al_search_all");
        } else if (fh_) {
            void* args[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&fh_items_1024_, &d_params_, &d_all, (void*)&count};
            check(cuLaunchKernel(fn, (count * 8 + 127) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch fishhash_search_all");
        } else if (rx_) {
            const uint32_t items = static_cast<uint32_t>(ih_items_);
            void* args[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_all, (void*)&count};
            check(cuLaunchKernel(fn, (ih_slots_ * 16) / 128, 1, 1, 128, 1, 1, 0, nullptr, args, nullptr), "launch rx_search_all");
        } else if (ih_) {
            const uint32_t items = static_cast<uint32_t>(ih_items_);
            void* args[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_all, (void*)&count, &d_r0e_st_};
            check(cuLaunchKernel(fn, (ih_slots_ * (tune("lanes", 8) == 16 ? 16u : ih_lanes_)) / ih_block_, 1, 1, ih_block_, 1, 1, 0, nullptr, args, nullptr), "launch ih_search_all");  // lanes=16 (CC-B): a nonce group is 16 threads
        } else {
            void* args[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&dag_items_2048_, &d_params_, &d_all, (void*)&count};
            check(cuLaunchKernel(fn, (count + 15) / 16, 1, 1, kBlock, 1, 1, 0, nullptr, args, nullptr), "launch search_all");
        }
        std::vector<uint32_t> res(words);
        check(cuMemcpyDtoH(res.data(), d_all, words * sizeof(uint32_t)), "read all-results");
        const uint32_t hits = std::min<uint32_t>(res[0], count);
        std::vector<hit> tmp;
        tmp.reserve(hits);
        for (uint32_t i = 0; i < hits; ++i) {
            const uint32_t* e = res.data() + 1 + i * kResultWords;
            hit h;
            h.nonce = (uint64_t(e[1]) << 32) | e[0];
            std::memcpy(h.mix, e + 2, 32);
            std::memcpy(h.hash, e + 10, 32);
            tmp.push_back(h);
        }
        std::sort(tmp.begin(), tmp.end(), [start](const hit& a, const hit& b) { return (a.nonce - start) < (b.nonce - start); });
        out.insert(out.end(), tmp.begin(), tmp.end());
    } catch (...) { cuMemFree(d_all); throw; }
    cuMemFree(d_all);
}

// Split execution (GUARD-C-CLOSURE.md 1.4, the renter's side): keyed_rounds + 1 launches of the split kernel over the same grid,
// the oracle between them. Slot layout on the device: state 144 words per nonce; shares (gid * S + i) * 8 words, the request
// m_i written by phase i and the share written back by the oracle before phase i + 1.
void engine::split_search(uint64_t height, const uint8_t header[32], const uint8_t target[32], uint64_t start, uint32_t count,
                          const std::function<void(uint32_t, uint64_t, uint32_t, uint32_t*)>& oracle, std::vector<hit>& out) {
    if (!ih_ || !tune("split", 0) || !ihp_.keyed_rounds) throw std::runtime_error("split_search needs tune split=1 on a bit 10 document");
    if (!d_dag_) throw std::runtime_error("search without a dataset");
    if (count > (1u << 20)) throw std::runtime_error("split_search over 2^20 nonces");
    int st = 0;
    if (ignpow_epoch_of(algo_.c_str(), params(), height, &st) != cur_epoch_) throw std::runtime_error("search at another epoch than prepared");
    const uint32_t S = ihp_.keyed_rounds;
    uint32_t params[kParamsWords];
    std::memcpy(params, header, 32);
    for (int i = 0; i < 8; ++i)
        params[8 + i] = (uint32_t(target[4 * i]) << 24) | (uint32_t(target[4 * i + 1]) << 16) | (uint32_t(target[4 * i + 2]) << 8) | uint32_t(target[4 * i + 3]);
    for (int i = 16; i < 24; ++i) params[i] = 0;   // the renter holds no secret
    check(cuMemcpyHtoD(d_params_, params, sizeof params), "upload params");
    const size_t words = 1 + size_t(count) * kResultWords;
    const size_t state_words = size_t(count) * 144, share_words = size_t(count) * S * 8;
    CUdeviceptr d_all = 0, d_state = 0, d_shares = 0;
    check(cuMemAlloc(&d_all, (words + 1) * sizeof(uint32_t)), "alloc all-results");
    try {
        check(cuMemAlloc(&d_state, state_words * sizeof(uint32_t)), "alloc split state");
        check(cuMemAlloc(&d_shares, share_words * sizeof(uint32_t)), "alloc split shares");
        check(cuMemsetD32(d_all, 0, 1), "clear");
        check(cuMemsetD32(d_all + words * sizeof(uint32_t), 0, 1), "clear nonce counter");
        const uint32_t items = static_cast<uint32_t>(ih_items_);
        std::vector<uint32_t> sh(share_words), slot(size_t(count) * 8);
        for (uint32_t phase = 0; phase <= S; ++phase) {
            void* args_st[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_all, (void*)&count, &d_r0e_st_, &d_state, &d_shares, (void*)&phase};
            void* args_plain[] = {(void*)&start, (void*)&count, &d_dag_, (void*)&items, &d_scratch_, &d_params_, &d_env_, &d_all, (void*)&count, &d_state, &d_shares, (void*)&phase};
            void** args = (ihp_.version >= 12 && ihp_.shared_kib) ? args_st : args_plain;
            check(cuLaunchKernel(f_ih_search_, (ih_slots_ * ih_lanes_) / ih_block_, 1, 1, ih_block_, 1, 1, 0, nullptr, args, nullptr), "launch ih_search (split phase)");
            check(cuCtxSynchronize(), "split phase sync");
            if (phase == S) break;
            // slot `phase` to the oracle: the requests m_phase, the shares back in place, then up to the device
            check(cuMemcpyDtoH(sh.data(), d_shares, share_words * sizeof(uint32_t)), "read split requests");
            for (uint32_t g = 0; g < count; ++g) std::memcpy(slot.data() + size_t(g) * 8, sh.data() + (size_t(g) * S + phase) * 8, 32);
            oracle(phase, start, count, slot.data());
            for (uint32_t g = 0; g < count; ++g) std::memcpy(sh.data() + (size_t(g) * S + phase) * 8, slot.data() + size_t(g) * 8, 32);
            check(cuMemcpyHtoD(d_shares, sh.data(), share_words * sizeof(uint32_t)), "upload split shares");
        }
        std::vector<uint32_t> res(words);
        check(cuMemcpyDtoH(res.data(), d_all, words * sizeof(uint32_t)), "read all-results");
        const uint32_t hits = std::min<uint32_t>(res[0], count);
        std::vector<hit> tmp;
        tmp.reserve(hits);
        for (uint32_t i = 0; i < hits; ++i) {
            const uint32_t* e = res.data() + 1 + i * kResultWords;
            hit h;
            h.nonce = (uint64_t(e[1]) << 32) | e[0];
            std::memcpy(h.mix, e + 2, 32);
            std::memcpy(h.hash, e + 10, 32);
            tmp.push_back(h);
        }
        std::sort(tmp.begin(), tmp.end(), [start](const hit& a, const hit& b) { return (a.nonce - start) < (b.nonce - start); });
        out.insert(out.end(), tmp.begin(), tmp.end());
    } catch (...) { cuMemFree(d_all); if (d_state) cuMemFree(d_state); if (d_shares) cuMemFree(d_shares); throw; }
    cuMemFree(d_all); cuMemFree(d_state); cuMemFree(d_shares);
}

telemetry engine::read_telemetry() {
    telemetry t;
    if (!nvml_ok_) return t;
    unsigned mw = 0;
    if (nvmlDeviceGetPowerUsage(nvml_, &mw) == NVML_SUCCESS) t.power_w = mw / 1000.0;
    unsigned temp = 0;
    if (nvmlDeviceGetTemperature(nvml_, NVML_TEMPERATURE_GPU, &temp) == NVML_SUCCESS) t.temp_c = temp;
    unsigned c = 0;
    if (nvmlDeviceGetClockInfo(nvml_, NVML_CLOCK_SM, &c) == NVML_SUCCESS) t.core_mhz = c;
    if (nvmlDeviceGetClockInfo(nvml_, NVML_CLOCK_MEM, &c) == NVML_SUCCESS) t.mem_mhz = c;
    nvmlMemory_t m;
    if (nvmlDeviceGetMemoryInfo(nvml_, &m) == NVML_SUCCESS) { t.mem_used = m.used; t.mem_total = m.total; }
    unsigned lim = 0;
    if (nvmlDeviceGetPowerManagementLimit(nvml_, &lim) == NVML_SUCCESS) t.power_limit_w = lim / 1000.0;
    unsigned long long reasons = 0;
    if (nvmlDeviceGetCurrentClocksThrottleReasons(nvml_, &reasons) == NVML_SUCCESS) t.throttle = reasons;
    t.ok = true;
    return t;
}

void engine::make_current() { if (ctx_) cuCtxSetCurrent(ctx_); }

}  // namespace igneum

