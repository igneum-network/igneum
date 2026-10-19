// r0d_gen.cpp: the version 9 (R0D) per-period programme generator. See r0d_gen.h.
#include "r0d_gen.h"
#include <cstring>

namespace igm {

namespace {

// the reference's `bytes` encoder of igneumhash.cpp (not rxe1's enc): tag and lp carry a ONE-byte length, le32 and le64 are raw little-endian, raw is bytes as they are.
struct Enc {
    std::vector<uint8_t> v;
    Enc &raw(const uint8_t *p, size_t n) { v.insert(v.end(), p, p + n); return *this; }
    Enc &lp(const uint8_t *p, size_t n) { v.push_back((uint8_t)n); return raw(p, n); }
    Enc &tag(const char *t) { return lp((const uint8_t *)t, std::strlen(t)); }
    Enc &le32(uint32_t x) { for (int i = 0; i < 4; i++) v.push_back((uint8_t)(x >> (8 * i))); return *this; }
    Enc &le64(uint64_t x) { for (int i = 0; i < 8; i++) v.push_back((uint8_t)(x >> (8 * i))); return *this; }
};

bool hex32(const std::string &h, uint8_t out[32]) {
    if (h.size() != 64) return false;
    auto nib = [](char c) -> int { if (c >= '0' && c <= '9') return c - '0'; if (c >= 'a' && c <= 'f') return c - 'a' + 10; if (c >= 'A' && c <= 'F') return c - 'A' + 10; return -1; };
    for (int i = 0; i < 32; i++) { int a = nib(h[2 * i]), b = nib(h[2 * i + 1]); if (a < 0 || b < 0) return false; out[i] = (uint8_t)(a * 16 + b); }
    return true;
}

const char *kNames[13] = {"FMA", "FADD", "IXOR", "IROTL", "IADD", "IMULHI", "POPC", "CLZ", "SHFL", "GATHER", "TILE", "SCATTER", "MMA"};
// version 10 (R0E): the window's 32 opcodes at the registered counts, permuted per window; the item flag on eight FP or integer operations
const uint8_t kR0eWindow[32] = {0,0,0,0,0,0,0,0,0,0,0,0, 1,1,1,1, 2,2,2, 3,3,3, 4, 5, 8,8,8,8, 9,9, 10,10};
const uint8_t kR0eItemFlag = 0x80;

}  // namespace

void r0d_program_seed(uint64_t chain_id, uint64_t period, const uint8_t anchor[32], uint8_t out[32], bool r0e) {
    Enc e; e.tag(r0e ? "IgneumHash/R0E/program" : "IgneumHash/R0D/program").le64(chain_id).le64(period).raw(anchor, 32);   // the anchor raw, as the reference writes it
    ih_blake3_full(e.v.data(), e.v.size(), out);
}

void r0d_generate(const uint8_t seed[32], uint32_t ops, bool eight_ops, uint32_t distance, bool r0e, bool v11, bool v12, std::vector<R0dOp> &out, uint32_t version, uint32_t mma_max) {
    out.resize(ops);
    uint8_t window[32] = {0};
    // version 11 (the outside review's eleventh reading): every window (tile) draws its own composition across the registered ranges from a
    // chained tile seed (tile 0 from the period seed, tile w from tile w-1's seed): ten counts in draw order (count = lo + byte mod
    // (hi - lo + 1)), the FMA count the remainder to 32 clamped to 4..20 with the overflow and the deficit walked in draw order, the
    // item count 4..12 capped by the FP and integer count; version 10: the fixed row and eight item operations
    uint8_t row[32]; for (int k = 0; k < 32; k++) row[k] = kR0eWindow[k];
    unsigned n_items = 8; uint8_t tile_seed[32] = {0};
    static const uint8_t order[10] = {1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, lo[10] = {0, 1, 1, 1, 1, 0, 0, 1, 1, 1}, hi[10] = {6, 6, 6, 4, 4, 3, 3, 6, 4, 3};
    int64_t last_write[16]; for (int k = 0; k < 16; k++) last_write[k] = -1000000;   // revision 0.4: the dependency distance (0: the plain draw)
    for (uint32_t i = 0; i < ops; i++) {
        if (version == 14 && (i & 31) == 0) {   // version 14 (section 2e, the resource passport): MMA mma_max exactly and first, FMA the remainder (at least 1, the deficit taken from the floors in draw order), the ten floor classes inside their ranges, the items 1..3 capped by the FP and integer count
            Enc comp; comp.tag("IgneumHash/R0E/composition").lp(i == 0 ? seed : tile_seed, 32); ih_blake3_full(comp.v.data(), comp.v.size(), tile_seed);
            Enc tb; tb.tag("IgneumHash/R0E/tile").lp(tile_seed, 32); uint8_t cs[32]; ih_blake3_full(tb.v.data(), tb.v.size(), cs);
            static const uint8_t order14[10] = {2, 3, 4, 5, 1, 8, 10, 9, 11, 6}, lo14[10] = {1, 0, 0, 0, 0, 1, 0, 1, 1, 0}, hi14[10] = {2, 1, 1, 1, 1, 2, 1, 2, 2, 1};
            unsigned cnt[10], sum = 0; for (int k = 0; k < 10; k++) { cnt[k] = lo14[k] + cs[k] % (hi14[k] - lo14[k] + 1); sum += cnt[k]; }
            int fm = 32 - (int)mma_max - (int)sum;
            if (fm < 1) { unsigned deficit = (unsigned)(1 - fm); fm = 1; for (int k = 0; k < 10 && deficit; k++) { unsigned room = cnt[k] - lo14[k], g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; } }
            unsigned pos = 0; for (unsigned m = 0; m < mma_max; m++) row[pos++] = 12;
            for (int m = 0; m < fm; m++) row[pos++] = 0;
            for (int k = 0; k < 10; k++) for (unsigned m = 0; m < cnt[k]; m++) row[pos++] = order14[k];
            const unsigned items = 1 + cs[11] % 3, fpint = (unsigned)fm + cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[9];
            n_items = items < fpint ? items : fpint;
        }
        if (v12 && version != 14 && (i & 31) == 0) {   // version 12 (section 2c): twelve classes in draw order, GATHER the remainder clamped 6..14 and first in the row, the items 2..6 capped by the FP and integer count; version 13: the MMA ceiling is the document's mma_max
            Enc comp; comp.tag("IgneumHash/R0E/composition").lp(i == 0 ? seed : tile_seed, 32); ih_blake3_full(comp.v.data(), comp.v.size(), tile_seed);
            Enc tb; tb.tag("IgneumHash/R0E/tile").lp(tile_seed, 32); uint8_t cs[32]; ih_blake3_full(tb.v.data(), tb.v.size(), cs);
            static const uint8_t order12[12] = {1, 2, 3, 4, 5, 6, 7, 8, 10, 11, 12, 0}, lo12[12] = {0, 1, 1, 0, 0, 0, 0, 1, 1, 4, 0, 4}, hi12c[12] = {1, 2, 2, 1, 1, 1, 1, 2, 1, 10, 4, 10};
            uint8_t hi12[12]; for (int k = 0; k < 12; k++) hi12[k] = hi12c[k]; if (version == 13) hi12[10] = (uint8_t)mma_max;
            unsigned cnt[12], sum = 0; for (int k = 0; k < 12; k++) { cnt[k] = lo12[k] + cs[k] % (hi12[k] - lo12[k] + 1); sum += cnt[k]; }
            int ga = 32 - (int)sum;
            if (ga > 14) { unsigned over = (unsigned)(ga - 14); ga = 14; for (int k = 0; k < 12 && over; k++) { unsigned room = hi12[k] - cnt[k], g = room < over ? room : over; cnt[k] += g; over -= g; } }
            else if (ga < 6) { unsigned deficit = (unsigned)(6 - ga); ga = 6; for (int k = 0; k < 12 && deficit; k++) { unsigned room = cnt[k] - lo12[k], g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; } }
            unsigned pos = 0; for (int m = 0; m < ga; m++) row[pos++] = 9;
            for (int k = 0; k < 12; k++) for (unsigned m = 0; m < cnt[k]; m++) row[pos++] = order12[k];
            const unsigned items = 2 + cs[12] % 5, fpint = cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[5] + cnt[6] + cnt[11];
            n_items = items < fpint ? items : fpint;
        }
        if (v11 && !v12 && (i & 31) == 0) {   // version 11's composition (version 12 draws its own above)
            Enc comp; comp.tag("IgneumHash/R0E/composition").lp(i == 0 ? seed : tile_seed, 32); ih_blake3_full(comp.v.data(), comp.v.size(), tile_seed);
            Enc tb; tb.tag("IgneumHash/R0E/tile").lp(tile_seed, 32); uint8_t cs[32]; ih_blake3_full(tb.v.data(), tb.v.size(), cs);   // the first 16 bytes
            unsigned cnt[10], sum = 0; for (int k = 0; k < 10; k++) { cnt[k] = lo[k] + cs[k] % (hi[k] - lo[k] + 1); sum += cnt[k]; }
            int fma = 32 - (int)sum;
            if (fma > 20) { unsigned over = (unsigned)(fma - 20); fma = 20; for (int k = 0; k < 10 && over; k++) { unsigned room = hi[k] - cnt[k], g = room < over ? room : over; cnt[k] += g; over -= g; } }
            else if (fma < 4) { unsigned deficit = (unsigned)(4 - fma); fma = 4; for (int k = 0; k < 10 && deficit; k++) { unsigned room = cnt[k] - lo[k], g = room < deficit ? room : deficit; cnt[k] -= g; deficit -= g; } }
            unsigned pos = 0; for (int m = 0; m < fma; m++) row[pos++] = 0;
            for (int k = 0; k < 10; k++) for (unsigned m = 0; m < cnt[k]; m++) row[pos++] = order[k];
            const unsigned items = 4 + cs[10] % 9, fpint = (unsigned)fma + cnt[0] + cnt[1] + cnt[2] + cnt[3] + cnt[4] + cnt[5] + cnt[6];
            n_items = items < fpint ? items : fpint;
        }
        Enc e; e.tag("IgneumHash/R0D/op").lp(seed, 32).le32(i);
        uint8_t q[32]; ih_blake3_full(e.v.data(), e.v.size(), q);   // the XOF's first eight bytes are the hash's first eight
        const unsigned w = q[0];
        R0dOp o;
        if (r0e) {
            if ((i & 31) == 0) {   // the window: a Fisher-Yates permutation of the fixed row by the window's own 64 XOF bytes, then the item flag on eight FP or integer operations
                Enc win; win.tag("IgneumHash/R0E/window").lp(seed, 32).le32(i >> 5);
                uint8_t wq[64]; ih_blake3_xof(win.v.data(), win.v.size(), wq, 64);
                uint8_t perm[32]; for (int k = 0; k < 32; k++) perm[k] = row[k];
                for (int k = 31; k > 0; k--) { const int j = wq[k] % (k + 1); std::swap(perm[k], perm[j]); }
                uint8_t flags[32] = {0}; unsigned placed = 0;
                for (int k = 0; k < 32 && placed < n_items; k++) { const int j = (k + wq[32 + k]) % 32; if (perm[j] < 8 && !flags[j]) { flags[j] = 1; placed++; } }
                for (int k = 0; k < 32; k++) window[k] = (uint8_t)(perm[k] | (flags[k] ? kR0eItemFlag : 0));
            }
            o.opcode = window[i & 31];
        } else
        o.opcode = eight_ops ? (w < 128 ? 0 : w < 160 ? 1 : w < 184 ? 2 : w < 208 ? 3 : w < 224 ? 4 : w < 240 ? 5 : w < 248 ? 6 : 7)   // bit 14: the eight-operation table, weights 128, 32, 24, 24, 16, 16, 8, 8 of 256
                           : (w < 224 ? 0 : w < 240 ? 2 : 3);   // bit 13 alone: the 224-of-256 mix (FMA 224, IXOR 16, IROTL 16)
        o.dst = q[1] % 16; o.src1 = q[2] % 16; o.src2 = q[3] % 16;
        if (distance) {   // each register read or written was last written at least D operations back: the drawn one, else the next eligible upward mod 16
            auto settle = [&](uint8_t r) -> uint8_t { for (int k = 0; k < 16; k++) { const uint8_t t = (uint8_t)((r + k) & 15); if ((int64_t)i - last_write[t] >= (int64_t)distance) return t; } return r; };
            o.src1 = settle(o.src1); o.src2 = settle(o.src2); o.dst = settle(o.dst);
            last_write[o.dst] = (int64_t)i;
        }
        o.imm = (uint32_t)q[4] | ((uint32_t)q[5] << 8) | ((uint32_t)q[6] << 16) | ((uint32_t)q[7] << 24);
        out[i] = o;
    }
}

bool r0d_seed_of(const IhParams &p, const Knobs &k, uint64_t height, uint8_t out[32], std::string &err) {
    if (!k.r0d_seed.empty()) { if (!hex32(k.r0d_seed, out)) { err = "r0d_seed is not 64 hex characters"; return false; } return true; }
    uint8_t anchor[32] = {0};
    const std::string &anc = !k.r0d_anchor.empty() ? k.r0d_anchor : p.program_anchor;   // the knob overrides the document's field; absent means zeros
    if (!anc.empty() && !hex32(anc, anchor)) { err = "the programme anchor is not 64 hex characters"; return false; }
    const uint64_t period = p.period_blocks ? height / p.period_blocks : 0;
    r0d_program_seed(p.chain_id, period, anchor, out, p.version >= 10);
    return true;
}

std::string r0d_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &err) {
    if (p.version < 9) return "";
    const bool r0e = p.version >= 10, v11 = p.version == 11, v12 = p.version >= 12, v14 = p.version == 14;
    uint8_t seed[32]; if (!r0d_seed_of(p, k, height, seed, err)) return "";
    std::vector<R0dOp> ops; r0d_generate(seed, p.program_ops, (p.features & 16384u) != 0, p.program_distance, r0e, v11 || v12, v12, ops, p.version, p.mma_max);
    // versions 12 to 14 (sections 2c to 2e): the table's rows (table_kib x 256 words at access_bytes per row), the chains' blocks, the shared table's rows, the bank rule
    const unsigned aw = v12 ? p.access_bytes / 4 : 0, n_rows = v12 ? p.table_words() / aw : 0, kch = (p.version >= 13 && p.chains_per_nonce) ? p.chains_per_nonce : 1, cb = v12 ? n_rows / kch : 0;
    const unsigned shw = v12 ? p.shared_words() : 0, scb = shw ? (shw / aw) / kch : 0; const bool lane_bank = p.version >= 13 && p.gather_lane_bank != 0, shared = shw != 0;
    const unsigned fold_every = (v14 && p.fold_every) ? p.fold_every : 1; const bool layout_draw = v14 && p.layout_draw != 0;
    if (v12 && (cb == 0 || (cb & (cb - 1)) || (shared && (scb == 0 || (scb & (scb - 1)))))) { err = "the table's rows per chain must be a power of two"; return ""; }
    std::string s;
    s += std::string(v14 ? "// R0E v14" : p.version == 13 ? "// R0E v13" : v12 ? "// R0E v12" : r0e ? "// R0E" : "// R0D") + ": the period programme, " + std::to_string(ops.size()) + " operations per lane per round, seed ";
    { static const char *hx = "0123456789abcdef"; for (int i = 0; i < 4; i++) { s += hx[seed[i] >> 4]; s += hx[seed[i] & 15]; } }
    s += "...\n#define IGM_R0D_M(x) (((x) & 0x3FFFFFFFu) | 0x20000000u)\n";
    // version 10's function takes the lane, the exchange parameters, the fetched item, the nonce's gather table and the octet's local tile
    if (v12) {   // the chains' values from the table's first words (chain c, lane l: word (c x 8 + l) mod the table's words), the shared table's nonce offset, version 14's topology word
        s += "IGM_INLINE void igm_r0e_program(IGM_PRIV uint *R, uint lane, IGM_XCH_PARAMS, IGM_GLOBAL const uint *item, IGM_LOCAL uint *table, IGM_GLOBAL const uint *shared, uint nonce_off, IGM_LOCAL uint *tg, IGM_PRIV const uint *ks8 IGM_R0E_OPTRACE_PARAM) {\n    uint v;";
        for (unsigned c = 0; c < kch; c++) s += " uint acc" + std::to_string(c) + " = table[(" + std::to_string(c * 8) + "u + lane) % " + std::to_string(p.table_words()) + "u];";
        if (v14) s += " uint topo = 0u;";
        s += "\n";
    }
    s += v12 ? std::string()
         : r0e ? std::string("IGM_INLINE void igm_r0e_program(IGM_PRIV uint *R, uint lane, IGM_XCH_PARAMS, IGM_GLOBAL const uint *item, IGM_GLOBAL const uint *table, IGM_LOCAL uint *tg, IGM_PRIV const uint *ks8) {\n    uint v;\n")
             : std::string("IGM_INLINE void igm_r0d_program(IGM_PRIV uint *R) {\n");
    unsigned hist[13] = {0}; unsigned acc_n = 0, tiles_n = 0;
    for (size_t i = 0; i < ops.size(); i++) {
        const R0dOp &o = ops[i]; const unsigned op = o.opcode & 0x7f; const bool item = (o.opcode & kR0eItemFlag) != 0; hist[op]++;
        const std::string d0 = "R[" + std::to_string(o.dst) + "]", a0 = "R[" + std::to_string(o.src1) + "]";
        // the item-sourced operand (version 10 on): the item's word at a register-drawn index STANDS IN the src2 register for the
        // operation, so a first source or an FMA addend naming that register reads the item word too (the reference's s2 rule)
        const std::string b = item ? "igm_r0e_item_word(item, (R[" + std::to_string(o.src2) + "] ^ " + std::to_string(o.imm) + "u) & 63u, ks8)" : "R[" + std::to_string(o.src2) + "]";   // the keyed share in the item's first eight words (section 0.9: the programme reads the shared item)
        const std::string a = (item && o.src1 == o.src2) ? b : a0;
        const std::string dsrc = (item && o.dst == o.src2) ? b : d0;   // the FMA's addend; the destination written stays R[dst]
        const std::string d = d0;
        switch (op) {
            // versions 11 and up (the project's ruling on the 4090 reading; the pocl trace of 13 Oct 07:5x UK caught the v12 to v14 omission): the FP result is STORED through the range mask; the read mask stays on every FP read
            case 0: s += "    " + d + " = " + (p.version >= 11 ? "IGM_R0D_M(" : "(") + "IGM_F2U(fma(IGM_U2F(IGM_R0D_M(" + a + ")), IGM_U2F(IGM_R0D_M(" + b + ")), IGM_U2F(IGM_R0D_M(" + dsrc + ")))));\n"; break;
            case 1: s += "    " + d + " = " + (p.version >= 11 ? "IGM_R0D_M(" : "(") + "IGM_F2U(IGM_U2F(IGM_R0D_M(" + a + ")) + IGM_U2F(IGM_R0D_M(" + b + "))));\n"; break;
            case 2: s += "    " + d + " = " + a + " ^ " + b + ";\n"; break;
            case 3: s += "    " + d + " = rotate(" + a + ", " + std::to_string(o.imm & 31u) + "u);\n"; break;
            case 4: s += "    " + d + " = " + a + " + " + b + ";\n"; break;
            case 5: s += "    " + d + " = IGM_MULHI(" + a + ", " + b + ");\n"; break;
            case 6: s += "    " + d + " = " + a + " ^ ((uint)popcount(" + b + ") * 0x9E3779B1u);\n"; break;
            case 7: s += "    " + d + " = " + a + " + ((uint)clz(" + b + ") * 0x85EBCA77u);\n"; break;
            case 8:   // SHFL: the register src1 of lane (l + off) mod 8, XORed into src2 (every lane reads before any writes)
                s += "    v = IGM_BCAST(" + a + ", ((lane + " + std::to_string(1 + o.imm % 7) + "u) & 7u)); " + d + " = R[" + std::to_string(o.src2) + "] ^ v;\n"; break;
            case 9:   // GATHER: versions 12 to 14: the row at the chain's block (the register XOR the immediate XOR the chain, the row's low bits the lane's under the bank rule) XORed over its words into the chain and the destination; the shared form reads the period's table at the nonce's offset; version 10 and 11: the table word at a register-drawn index
                if (v12) {
                    const std::string acc = "acc" + std::to_string(acc_n % kch); const unsigned c = acc_n % kch; acc_n++;
                    const std::string x = "(R[" + std::to_string(o.src2) + "] ^ " + std::to_string(o.imm) + "u ^ " + acc + ")";
                    std::string r = shared ? "((" + x + " + nonce_off) & " + std::to_string(scb - 1) + "u)" : "(" + x + " & " + std::to_string(cb - 1) + "u)";
                    if (lane_bank) r = "((" + r + " & ~7u) | lane)";
                    s += "    v = " + (shared ? "IGM_R12_GATHER_G(shared, " + std::to_string(c * scb) + "u + " + r + ")" : "IGM_R12_GATHER(table, " + std::to_string(c * cb) + "u + " + r + ")") + "; " + acc + " = v; " + d + " = " + d + " ^ v;\n";
                }
                else s += "    " + d + " = " + a + " ^ table[(R[" + std::to_string(o.src2) + "] ^ " + std::to_string(o.imm) + "u) & " + (v11 ? "1023u" : "4095u") + "];\n";
                break;
            case 10:  // TILE: version 12: the skew R'[l][k] = R[(l + k) & 7][k] (sixteen shuffles); version 10 and 11: the transpose through local memory
                if (v12) { s += "   "; for (int q = 0; q < 16; q++) s += " R[" + std::to_string(q) + "] = IGM_BCAST(R[" + std::to_string(q) + "], ((lane + " + std::to_string(q) + "u) & 7u));"; s += "\n"; }
                else s += "    for (uint q = 0; q < 16; q++) tg[lane * 16u + q] = R[q]; IGM_R0E_BARRIER(); for (uint q = 0; q < 16; q++) R[q] = tg[(q & 7u) * 16u + ((q >> 3) + 2u * lane)]; IGM_R0E_BARRIER();\n";
                break;
            case 11: {  // SCATTER (versions 12 to 14): the lane's own row (the residue l) in the chain's block at a data-drawn index rewritten from the registers src1.. XOR dst (no load of the old row), the row's fold to the lane and the chain
                const std::string acc = "acc" + std::to_string(acc_n % kch); const unsigned c = acc_n % kch; acc_n++;
                s += "    v = IGM_R12_SCATTER(table, " + std::to_string(c * cb) + "u + ((((R[" + std::to_string(o.src2) + "] ^ " + std::to_string(o.imm) + "u ^ " + acc + ") & " + std::to_string(cb - 1) + "u) & ~7u) | lane), R, " + std::to_string(o.src1) + "u, " + std::to_string(o.dst) + "u); " + acc + " = v; " + d + " = v;\n"; break; }
            default:  // MMA: version 14: the exact INT8 16x8x32 tile (section 2e) with the topology word, then the fold and the register scramble every fold_every tiles; versions 12 and 13: the exact INT8 8 x 8 x 8 octet tile
                if (v14) {
                    s += "    IGM_R14_TILE(R, lane, IGM_XCH_ARGS, item, ks8, " + std::to_string(o.src1) + "u, " + std::to_string(o.src2) + "u, " + std::to_string(o.dst) + "u, " + std::to_string(o.imm) + "u, topo, " + (layout_draw ? "((topo >> 24) & 7u)" : "0u") + ");\n";
                    if (++tiles_n % fold_every == 0) s += "    IGM_R14_FOLD(R, lane, IGM_XCH_ARGS, ((" + std::to_string(o.dst) + "u + (topo >> 9)) & 15u), topo);\n";
                } else s += "    IGM_R12_MMA(R, " + std::to_string(o.src1) + "u, " + std::to_string(o.src2) + "u, " + std::to_string(o.dst) + "u);\n";
                break;
        }
        if (v12) s += "    IGM_R0E_OPTRACE_WRITE(" + std::to_string(i) + "u);\n";   // the per-operation trace (14 Oct 2026, the Metal round fault): the sixteen registers after op i
        if ((i + 1) % 32 == 0) {   // the window's end: version 13's chains combine into registers 0..k-1 (k over 1), then the range mask on every register
            s += "    "; if (kch > 1) for (unsigned c = 0; c < kch; c++) s += "R[" + std::to_string(c) + "] ^= acc" + std::to_string(c) + "; ";
            for (int j = 0; j < 16; j++) s += "R[" + std::to_string(j) + "] = IGM_R0D_M(R[" + std::to_string(j) + "]); "; s += "\n"; }
    }
    if (r0e) {   // the octet fold: every lane's programme reaches the next address
        s += "    { uint f1 = IGM_BCAST(R[1], ((lane + 3u) & 7u)); uint f2 = IGM_BCAST(R[2], ((lane + 5u) & 7u)); R[0] ^= rotate(f1 + f2, 7u); }\n";
    }
    s += "}\n// mix:"; for (int j = 0; j < 13; j++) s += std::string(" ") + kNames[j] + " " + std::to_string(hist[j]); s += "\n";
    (void)msl;   // the dialects differ only in the preludes' macros (IGM_U2F, IGM_F2U, IGM_MULHI, IGM_PRIV, IGM_INLINE); rotate, popcount and clz are both languages' builtins
    return s;
}

uint32_t r0d_key(const IhParams &p, const Knobs &k, uint64_t height) {
    if (p.version < 9) return 0;
    uint8_t seed[32]; std::string err; if (!r0d_seed_of(p, k, height, seed, err)) return 0x40000000u;
    uint32_t h = 2166136261u;
    for (int i = 0; i < 32; i++) { h ^= seed[i]; h *= 16777619u; }
    h ^= p.program_ops; h *= 16777619u; h ^= (p.features & 16384u) ? 77u : 0u; h *= 16777619u; h ^= p.program_distance * 131u; h *= 16777619u; h ^= p.version * 7919u; h *= 16777619u;
    for (uint32_t f : {p.table_kib, p.access_bytes, p.shared_kib, p.mma_max, p.chains_per_nonce, p.gather_lane_bank, p.tile_shape, p.fold_every, p.layout_draw, p.tile_layout}) { h ^= f + 17u; h *= 16777619u; }
    return (h & 0x3FFFFFFFu) | 0x40000000u;
}

bool r12_shared_seed(const IhParams &p, const Knobs &k, uint64_t height, uint8_t out[32], std::string &err) {
    uint8_t anchor[32] = {0};
    const std::string &anc = !k.r0d_anchor.empty() ? k.r0d_anchor : p.program_anchor;
    if (!anc.empty() && !hex32(anc, anchor)) { err = "the programme anchor is not 64 hex characters"; return false; }
    const uint64_t period = p.period_blocks ? height / p.period_blocks : 0;
    Enc e; e.tag("IgneumHash/R0E/shared").le64(p.chain_id).le64(period).raw(anchor, 32);
    ih_blake3_full(e.v.data(), e.v.size(), out);
    return true;
}

// VERSION 15 (the bandwidth-first class, the reference's r16 text): the period key K = BLAKE3(LP("IgneumHash/R0F/key") || LE64 chain ||
// LE64 period || anchor) as eight LE words, and the period's programme BLAKE3xof(LP("IgneumHash/R0F/program") || K) of 8 bytes an operation:
// the opcode by cand-14's weights (ADD 38, SUB 38, XOR 51, ROTL 31, ROTR 33, MAD the rest; the kernel's codes MAD 0, ADD 1, SUB 2, XOR 3,
// ROTL 4, ROTR 5), d, a, b in the op's bank (op index mod 4, eight registers each), the immediate LE32; packed as the kernel reads them
// (word 0 = opcode | d << 8 | a << 16 | b << 24, word 1 = imm)
bool r0f_period(const IhParams &p, const Knobs &k, uint64_t height, uint8_t key_out[32], std::vector<uint32_t> &prog_words, std::string &err) {
    uint8_t anchor[32] = {0};
    const std::string &anc = !k.r0d_anchor.empty() ? k.r0d_anchor : p.program_anchor;
    if (!anc.empty() && !hex32(anc, anchor)) { err = "the programme anchor is not 64 hex characters"; return false; }
    const uint64_t period = p.period_blocks ? height / p.period_blocks : 0;
    Enc e; e.tag("IgneumHash/R0F/key").le64(p.chain_id).le64(period).raw(anchor, 32);
    ih_blake3_full(e.v.data(), e.v.size(), key_out);
    Enc q; q.tag("IgneumHash/R0F/program").raw(key_out, 32);
    std::vector<uint8_t> x((size_t)p.program_ops * 8); ih_blake3_xof(q.v.data(), q.v.size(), x.data(), x.size());
    prog_words.assign((size_t)p.program_ops * 2, 0u);
    for (uint32_t j = 0; j < p.program_ops; j++) {
        const uint8_t *b = x.data() + 8 * j; const uint32_t bank = j & 3u, q0 = b[0];
        const uint32_t opc = q0 < 38 ? 1u : q0 < 76 ? 2u : q0 < 127 ? 3u : q0 < 158 ? 4u : q0 < 191 ? 5u : 0u;
        const uint32_t d = 8u * bank + (b[1] & 7u), a = 8u * bank + (b[2] & 7u), bb = 8u * bank + (b[3] & 7u);
        prog_words[2 * j] = opc | (d << 8) | (a << 16) | (bb << 24);
        prog_words[2 * j + 1] = (uint32_t)b[4] | ((uint32_t)b[5] << 8) | ((uint32_t)b[6] << 16) | ((uint32_t)b[7] << 24);
    }
    return true;
}


// VERSION 15, the static form of the period programme (13 Oct 2026, the Arc B580 reading: Intel's compiler keeps the interpreter's
// register file in 20,480 bytes of scratch per work-item and the launch returns nothing): the same 64 operations as r0f_period's words,
// emitted as the kernel function igm_r0f_program over the lane's 32 registers with CONSTANT indices, one statement an operation, in the
// reference's order; the kernel runs it once per iteration in place of the loop over the words (IGM_R0F_STATIC). The key is unchanged.
bool r0f_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &src, uint8_t key_out[32], std::string &err) {
    std::vector<uint32_t> w; if (!r0f_period(p, k, height, key_out, w, err)) return false;
    std::string s; s.reserve(16384);
    s += std::string(msl ? "inline" : "IGM_INLINE") + " void igm_r0f_program(" + (msl ? "thread uint *R" : "IGM_PRIV uint *R") + ") {\n";
    for (size_t j = 0; j + 1 < w.size(); j += 2) {
        const uint32_t w0 = w[j], imm = w[j + 1], opc = w0 & 255u, d = (w0 >> 8) & 31u, a = (w0 >> 16) & 31u, b = (w0 >> 24) & 31u;
        const std::string A = "R[" + std::to_string(a) + "]", B = "R[" + std::to_string(b) + "]", I = std::to_string(imm) + "u";
        std::string rhs;
        if (opc == 0u) rhs = A + " * " + B + " + " + I; else if (opc == 1u) rhs = A + " + " + B + " + " + I; else if (opc == 2u) rhs = A + " - " + B + " - " + I;
        else if (opc == 3u) rhs = A + " ^ " + B + " ^ " + I; else if (opc == 4u) rhs = "rotate(" + A + ", " + std::to_string(imm & 31u) + "u) ^ " + B;
        else rhs = "rotate(" + A + ", " + std::to_string((32u - (imm & 31u)) & 31u) + "u) ^ " + B;
        s += "    R[" + std::to_string(d) + "] = " + rhs + ";\n";
    }
    s += "}\n"; src = s; return true;
}
}  // namespace igm

namespace igm {
// VERSION 16 (the static-fabric integer coordinate, the reference's r16f text, main 89811d62): the period key K = BLAKE3(LP("IgneumHash/R0G/key")
// || LE64 chain || LE64 period || anchor); the programme BLAKE3xof(LP("IgneumHash/R0G/program") || K) read in order per pass and bank: two
// Fisher-Yates permutations of 56 (55 LE32 words each), then 56 operations of (opcode byte, b byte, LE32 immediate); op j = 4 i + bank of
// pass r writes d = bank * 56 + pd[i], reads a = bank * 56 + pa[i] and b = bank * 56 + (q1 mod 56); the opcode by cand-14's table (MAD 0,
// ADD 1, SUB 2, XOR 3, ROTL 4, ROTR 5 in the kernel's codes). The programme is emitted as the kernel function igm_r16f_program over a
// private array of 224 words with CONSTANT indices (the spec's static live set: no data-dependent register index, nothing the compiler
// can shrink), one statement an operation, in the order the reference runs them.
// VERSION 17 (the texture form, the reference's r17 text): the period key K = BLAKE3(LP("IgneumHash/R0H/key") || LE64 chain || LE64 period || anchor); the
// texture is static (texel (x, y) = fmix32(y * 4096 + x)), so the key is the period's only input
bool r17_key(const IhParams &p, const Knobs &k, uint64_t height, uint8_t key_out[32], std::string &err) {
    uint8_t anchor[32] = {0};
    const std::string &anc = !k.r0d_anchor.empty() ? k.r0d_anchor : p.program_anchor;
    if (!anc.empty() && !hex32(anc, anchor)) { err = "the programme anchor is not 64 hex characters"; return false; }
    const uint64_t period = p.period_blocks ? height / p.period_blocks : 0;
    Enc e; e.tag("IgneumHash/R0H/key").le64(p.chain_id).le64(period).raw(anchor, 32);
    ih_blake3_full(e.v.data(), e.v.size(), key_out);
    return true;
}
bool r16f_program_source(const IhParams &p, const Knobs &k, uint64_t height, bool msl, std::string &src, uint8_t key_out[32], std::string &err) {
    uint8_t anchor[32] = {0};
    const std::string &anc = !k.r0d_anchor.empty() ? k.r0d_anchor : p.program_anchor;
    if (!anc.empty() && !hex32(anc, anchor)) { err = "the programme anchor is not 64 hex characters"; return false; }
    const uint64_t period = p.period_blocks ? height / p.period_blocks : 0;
    Enc e; e.tag("IgneumHash/R0G/key").le64(p.chain_id).le64(period).raw(anchor, 32);
    ih_blake3_full(e.v.data(), e.v.size(), key_out);
    const uint32_t passes = p.rounds, per_bank = 110 * 4 + 56 * 6;
    const size_t total = (size_t)passes * 4 * per_bank;
    Enc q; q.tag("IgneumHash/R0G/program").raw(key_out, 32);
    std::vector<uint8_t> x(total); ih_blake3_xof(q.v.data(), q.v.size(), x.data(), x.size());
    auto le32 = [](const uint8_t *b) { return (uint32_t)b[0] | ((uint32_t)b[1] << 8) | ((uint32_t)b[2] << 16) | ((uint32_t)b[3] << 24); };
    std::string s; s.reserve(total * 2);
    s += std::string(msl ? "inline" : "IGM_INLINE") + " void igm_r16f_program(" + (msl ? "thread uint *R" : "IGM_PRIV uint *R") + ") {\n";
    size_t off = 0;
    for (uint32_t r = 0; r < passes; r++) {
        // the four banks' tables of the pass (the reference draws bank by bank, then runs op j = 4 i + bank in j order)
        uint8_t opc[4][56], dd[4][56], aa[4][56], bb[4][56]; uint32_t imm[4][56];
        for (uint32_t bk = 0; bk < 4; bk++) {
            uint8_t pd[56], pa[56];
            for (uint32_t i = 0; i < 56; i++) { pd[i] = (uint8_t)i; pa[i] = (uint8_t)i; }
            for (uint32_t i = 55; i > 0; --i) { const uint32_t j = le32(x.data() + off) % (i + 1); off += 4; std::swap(pd[i], pd[j]); }
            for (uint32_t i = 55; i > 0; --i) { const uint32_t j = le32(x.data() + off) % (i + 1); off += 4; std::swap(pa[i], pa[j]); }
            for (uint32_t i = 0; i < 56; i++) {
                const uint8_t *w = x.data() + off; off += 6;
                const uint8_t q0 = w[0];
                opc[bk][i] = q0 < 38 ? 1 : q0 < 76 ? 2 : q0 < 127 ? 3 : q0 < 158 ? 4 : q0 < 191 ? 5 : 0;
                dd[bk][i] = (uint8_t)(bk * 56 + pd[i]); aa[bk][i] = (uint8_t)(bk * 56 + pa[i]); bb[bk][i] = (uint8_t)(bk * 56 + (w[1] % 56)); imm[bk][i] = le32(w + 2);
            }
        }
        for (uint32_t j = 0; j < 224; j++) {
            const uint32_t bk = j & 3, i = j >> 2;
            const std::string d = "R[" + std::to_string(dd[bk][i]) + "]", a = "R[" + std::to_string(aa[bk][i]) + "]", b = "R[" + std::to_string(bb[bk][i]) + "]", im = std::to_string(imm[bk][i]) + "u";
            switch (opc[bk][i]) {
                case 0: s += "    " + d + " = " + a + " * " + b + " + " + im + ";\n"; break;
                case 1: s += "    " + d + " = " + a + " + " + b + " + " + im + ";\n"; break;
                case 2: s += "    " + d + " = " + a + " - " + b + " - " + im + ";\n"; break;
                case 3: s += "    " + d + " = " + a + " ^ " + b + " ^ " + im + ";\n"; break;
                case 4: s += "    " + d + " = rotate(" + a + ", " + std::to_string(imm[bk][i] & 31) + "u) ^ " + b + ";\n"; break;
                default: s += "    " + d + " = rotate(" + a + ", " + std::to_string((32 - (imm[bk][i] & 31)) & 31) + "u) ^ " + b + ";\n"; break;
            }
        }
    }
    s += "}\n";
    src = s; return true;
}
}  // namespace igm
