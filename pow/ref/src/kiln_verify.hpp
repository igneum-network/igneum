// pow/ref/src/kiln_verify.hpp: THE KILN VERIFIER (version 22, FORM-FORGE.md section 6; the Kiln verifier seat, 15 October 2026).
// Included by igneumhash.cpp inside its anonymous namespace, after namespace kiln's reference attempt(), which stays the normative
// text and is never changed here. This file is the FAST SOFTWARE RENDER of one hash's scene with the same rule, bit for bit:
//   (1) the batch programme decoded ONCE per batch (a thread-local cache keyed by the batch seed: a list from one batch shares one
//       programme, so a verifier compiles it once; the fold chains and the programme hash are not recomputed per hash);
//   (2) the raster by incremental integer edge functions (the reference's doubled-centre top-left rule, the same int64 values,
//       stepped by the constant per pixel instead of recomputed), the covered fragments listed in the reference's order;
//   (3) the row fetches (the "rows" lever, 25e76b541: R full 256-byte tile rows a fragment from its identity, folded into the state)
//       read in place from the resident dataset with the next fragments' rows prefetched (every row address is a function of the
//       fragment's identity and the scene, known before any fold), the fold as the reference's 16-byte accumulator;
//   (4) the fragment programme run EIGHT FRAGMENTS AT A TIME on AVX2 (the same 32-bit lane operations: ADD, XOR, ROTL by the
//       instruction's immediate, MUL low, MUL high by two 32 x 32 products, TEX by a gather from the resident dataset); the scalar
//       path below is the fallback on a host without AVX2 and the path of the light context (the tile rows derived on demand);
//   (5) the tiles read in place: a tile is 64 consecutive items (16 KiB contiguous in the dataset), so texel idx of tile k is the
//       dataset word tix[k] * 4096 + idx, no copy; the light context derives only the rows the fragments touch, as the reference;
//   (6) the blends applied afterwards in the reference's order (a fragment's colour depends on its registers only, never on the
//       target, so the colours may be computed in any order and the UNORM8 blends replayed in raster order).
// The reference path stays selectable (IGNH_KILN_REFERENCE=1 in the environment, or ignh_kiln_hash_path(..., 0)) for the proof:
// the two paths are compared on the document's vector sets and on random (header, nonce) draws; 0 mismatches is the condition.
namespace kiln {

struct batch_prog {
    uint8_t bseed[32] = {}, phash[32] = {};
    uint32_t words = 0;
    std::vector<uint8_t> cls;    // w0 mod 6 per instruction
    std::vector<uint32_t> imm;   // w1 per instruction
    std::vector<uint32_t> sh;    // ROTL's shift (imm & 31) per instruction
    bool valid = false;
};

// the per-thread batch cache: four slots replaced round-robin (a list spans one batch; two across a boundary)
inline const batch_prog& batch_programme(const ih_ctx& c, const uint8_t header[32], uint64_t nonce) {
    thread_local batch_prog slots[4];
    thread_local unsigned next = 0;
    uint8_t bseed[32]; { bytes in; in.tag(kTagBatch).raw(c.E, 32).raw(header, 32).le64(nonce >> c.p.keyed_rounds); b3_hash(in, bseed, 32); }
    const uint32_t words = c.p.program_ops;
    for (auto& s : slots) if (s.valid && s.words == words && std::memcmp(s.bseed, bseed, 32) == 0) return s;
    batch_prog& s = slots[next]; next = (next + 1) & 3;
    s.valid = false; s.words = words; std::memcpy(s.bseed, bseed, 32);
    s.cls.assign(words, 0); s.imm.assign(words, 0); s.sh.assign(words, 0);
    std::vector<uint8_t> pbytes(size_t(words) * 8);
    uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(bseed + 4 * k);
    for (uint32_t i = 0; i < words; ++i) for (int j = 0; j < 2; ++j) {
        const uint32_t idx = 2 * i + j; x[idx & 7] = r16f::fchain(x[idx & 7], idx);
        const uint32_t w = x[idx & 7];
        for (int b = 0; b < 4; ++b) pbytes[size_t(idx) * 4 + b] = uint8_t(w >> (8 * b));
        if (j == 0) s.cls[i] = uint8_t(w % 6u); else { s.imm[i] = w; s.sh[i] = w & 31u; }
    }
    b3_hash(pbytes.data(), pbytes.size(), s.phash, 32);
    s.valid = true;
    return s;
}

// the per-thread scratch of one hash: the fragment list (structure of arrays, padded to eight), the light tile rows, the target
struct scratch {
    std::vector<uint32_t> fxy, fcol, ftri, ftbase, fid, fxyraw, fout;  // r0, r1, r2 initial values; the tile's base word (resident) or tile index (light); the identity; x | y << 16; r7
    std::vector<uint32_t> freg[5];                             // r3..r7 after the row folds
    std::vector<uint8_t> fmode;                                // the triangle's blend mode per fragment
    std::vector<uint8_t> covered;                              // the exact count's covered map
    std::vector<uint32_t> tl;                                  // light: ntiles x 64 rows x 64 words
    std::vector<uint8_t> have;                                 // light: the rows derived so far
    std::vector<uint8_t> fb;                                   // the target
    std::vector<uint64_t> rows_seen;                           // the rows-read count (optional, ignh_kiln_count_rows)
};
inline scratch& scratch_of() { thread_local scratch s; return s; }
inline bool& count_rows_flag() { thread_local bool f = false; return f; }
inline uint64_t& rows_read_last() { thread_local uint64_t n = 0; return n; }

// the fragment programme over fragments [0, n) (n a multiple of 8 in the AVX2 path; the arrays padded); light: the rows derived on demand
inline void run_scalar(const ih_ctx& c, const batch_prog& P, scratch& s, size_t n, const uint32_t* dsw, const uint64_t* tix) {
    for (size_t f = 0; f < n; ++f) {
        uint32_t r[8] = { s.fxy[f], s.fcol[f], s.ftri[f], s.freg[0][f], s.freg[1][f], s.freg[2][f], s.freg[3][f], s.freg[4][f] };
        const uint32_t tb = s.ftbase[f];
        for (uint32_t q = 0; q < P.words; ++q) {
            const uint32_t imm = P.imm[q], a = r[(q + 3) & 7], b = r[(q + 5) & 7]; uint32_t& d = r[q & 7]; const uint32_t d0 = d;
            switch (P.cls[q]) {
                case 0: d = a + b + imm + rotl32(d0, 9); break; case 1: d = a ^ b ^ imm ^ rotl32(d0, 17); break; case 2: d = rotl32(a, P.sh[q]) ^ b ^ d0; break;
                case 3: d = a * b + imm + d0; break; case 4: d = (uint32_t((uint64_t(a) * uint64_t(b)) >> 32) ^ imm) + rotl32(d0, 13); break;
                default: {
                    const uint32_t idx = (a + imm + d0) & 4095u;
                    if (dsw) d = dsw[size_t(tb) + idx] ^ b ^ rotl32(d0, 5);
                    else {
                        const uint32_t k = tb, y = idx >> 6; uint32_t* row = &s.tl[(size_t(k) * 64 + y) * 64];
                        if (!s.have[size_t(k) * 64 + y]) { load_item(c, tix[k] * 64 + y, row); s.have[size_t(k) * 64 + y] = 1; }
                        d = row[idx & 63u] ^ b ^ rotl32(d0, 5);
                    }
                    if (count_rows_flag()) s.rows_seen.push_back(dsw ? (uint64_t(tb) + idx) >> 6 : tix[tb] * 64 + (idx >> 6));
                } break;
            }
        }
        s.fout[f] = r[7];
    }
}

#if defined(__AVX2__)
template <int N> inline __m256i rotl_x8(__m256i x) { return _mm256_or_si256(_mm256_slli_epi32(x, N), _mm256_srli_epi32(x, 32 - N)); }
inline __m256i mulhi_epu32_x8(__m256i a, __m256i b) {
    const __m256i e = _mm256_mul_epu32(a, b);                                                     // lanes 0, 2, 4, 6: the 64-bit products
    const __m256i o = _mm256_mul_epu32(_mm256_srli_epi64(a, 32), _mm256_srli_epi64(b, 32));      // lanes 1, 3, 5, 7
    return _mm256_blend_epi32(_mm256_srli_epi64(e, 32), o, 0xAA);                                 // the high words in place
}
// the resident path: eight fragments a pass, the tile texels gathered from the dataset's words (the base word per lane)
inline void run_avx2(const batch_prog& P, scratch& s, size_t n, const uint32_t* dsw) {
    const int* base = reinterpret_cast<const int*>(dsw);
    const __m256i mask = _mm256_set1_epi32(4095);
    for (size_t f = 0; f < n; f += 8) {
        __m256i r[8];
        r[0] = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(&s.fxy[f]));
        r[1] = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(&s.fcol[f]));
        r[2] = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(&s.ftri[f]));
        for (int k = 3; k < 8; ++k) r[k] = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(&s.freg[k - 3][f]));
        const __m256i tb = _mm256_loadu_si256(reinterpret_cast<const __m256i*>(&s.ftbase[f]));
        for (uint32_t q = 0; q < P.words; ++q) {
            const __m256i imm = _mm256_set1_epi32(int(P.imm[q])), a = r[(q + 3) & 7], b = r[(q + 5) & 7], d0 = r[q & 7];
            __m256i d;
            switch (P.cls[q]) {
                case 0: d = _mm256_add_epi32(_mm256_add_epi32(_mm256_add_epi32(a, b), imm), rotl_x8<9>(d0)); break;
                case 1: d = _mm256_xor_si256(_mm256_xor_si256(_mm256_xor_si256(a, b), imm), rotl_x8<17>(d0)); break;
                case 2: { const int sh = int(P.sh[q]); const __m256i rot = sh ? _mm256_or_si256(_mm256_slli_epi32(a, sh), _mm256_srli_epi32(a, 32 - sh)) : a; d = _mm256_xor_si256(_mm256_xor_si256(rot, b), d0); } break;
                case 3: d = _mm256_add_epi32(_mm256_add_epi32(_mm256_mullo_epi32(a, b), imm), d0); break;
                case 4: d = _mm256_add_epi32(_mm256_xor_si256(mulhi_epu32_x8(a, b), imm), rotl_x8<13>(d0)); break;
                default: {
                    const __m256i idx = _mm256_add_epi32(_mm256_and_si256(_mm256_add_epi32(_mm256_add_epi32(a, imm), d0), mask), tb);
                    d = _mm256_xor_si256(_mm256_xor_si256(_mm256_i32gather_epi32(base, idx, 4), b), rotl_x8<5>(d0));
                    if (count_rows_flag()) { alignas(32) uint32_t w[8]; _mm256_store_si256(reinterpret_cast<__m256i*>(w), idx); for (int l = 0; l < 8; ++l) s.rows_seen.push_back(w[l] >> 6); }
                } break;
            }
            r[q & 7] = d;
        }
        _mm256_storeu_si256(reinterpret_cast<__m256i*>(&s.fout[f]), r[7]);
    }
}
#endif

// one row's fold: the reference's acc[k] = (acc[k] ^ v) + v over the row's 64 words, four accumulators (the compiler vectorises the
// four lanes; the row is 256 contiguous bytes)
inline uint32_t fold_row(const uint32_t* row, uint32_t rowi, uint32_t q, const uint32_t* sw) {
    uint32_t acc[4] = { rowi ^ sw[6], rowi * 0x9E3779B1u, sw[5] ^ q, sw[7] + rowi };
    for (uint32_t w = 0; w < 64; w += 4) for (int k = 0; k < 4; ++k) { const uint32_t v = row[w + k]; acc[k] = (acc[k] ^ v) + v; }
    return acc[0] ^ acc[1] ^ acc[2] ^ acc[3];
}

// the fast attempt: the same M as attempt() above for every (header, nonce); the fragment list holds the passes in the reference's
// order (the covered fragments triangle by triangle in raster order, then the fill's), its index the pass index
void attempt_fast(const ih_ctx& c, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32]) {
    const uint32_t T = c.p.rounds, side = 1u << c.p.registers, tlog = c.p.registers, nrows = c.p.blocks, fcap = c.p.table_kib;  // fcap: the exact pass count (0 = the covered alone)
    const bool coal = (c.p.access_bytes & 1u) != 0;  // the coalesced rows (124124cc3): runs of 16 passes stream the same `rows` items drawn over the whole dataset, each pass its 16-byte slice
    const uint32_t contig = (c.p.access_bytes >> 8) ? (c.p.access_bytes >> 8) : 1u;  // "contig" (328f93091): the run's rows as rows / contig bases of contig consecutive items
    const batch_prog& P = batch_programme(c, header, nonce);
    uint8_t sc[32]; { bytes in; in.tag(kTagScene).raw(c.E, 32).raw(S, 32); b3_hash(in, sc, 32); }
    uint32_t sw[8]; for (int k = 0; k < 8; ++k) sw[k] = le32(sc + 4 * k);
    const uint64_t tile_count = c.n_items() / 64; const uint32_t ntiles = c.p.fold_every ? c.p.fold_every : 1u, row_count = ntiles * 64u;
    uint64_t tix[64]; for (uint32_t k = 0; k < ntiles; ++k) tix[k] = uint64_t(r16f::fchain(sw[0], 1000u + k)) % tile_count;
    // the resident dataset's words (the gather's 32-bit index holds a dataset of up to 4 GiB; a larger one takes the scalar path)
    const bool resident = !c.dataset_v().empty();
    const uint32_t* dsw = (resident && c.p.dataset_bytes <= (uint64_t(1) << 32)) ? reinterpret_cast<const uint32_t*>(c.dataset_v().data()) : nullptr;
    const bool light_rows = !dsw;  // the rows through the per-hash buffers: the light context, or a resident dataset too wide for the gather
    scratch& s = scratch_of();
    s.fxy.clear(); s.fcol.clear(); s.ftri.clear(); s.ftbase.clear(); s.fid.clear(); s.fxyraw.clear(); s.fmode.clear(); for (auto& v : s.freg) v.clear();
    if (light_rows) { s.tl.resize(size_t(ntiles) * 64 * 64); s.have.assign(size_t(ntiles) * 64, 0); }
    if (count_rows_flag()) s.rows_seen.clear();
    s.covered.assign(fcap ? size_t(side) * side : 0, 0);
    uint32_t nrun = 0;
    // one pass's entry: the registers' identity folds, the tile, the blend mode (the raster's covered fragments, then the fill)
    auto emit = [&](uint32_t t, uint32_t i, uint32_t j, uint32_t colour, uint32_t mode, int64_t minx) {
        const uint32_t fid = (t << (2u * tlog)) | (j << tlog) | i, tk = (fid >> 4) % ntiles;
        // every initial register a fold of the scene word with the fragment's identity (a1d8a5a8a): r_k = fmix32(f0 + 0x9E3779B9 (k + 1) + fser 0x85EBCA6B) ^ (f0, colour, t ^ s1, s2..s6)
        const uint32_t f0 = i | (j << 16) | (t << 26), fser = uint32_t(int64_t(i) - minx);
        s.fxy.push_back(r17::fmix32(f0 + 0x9E3779B9u * 1u + fser * 0x85EBCA6Bu) ^ f0); s.fcol.push_back(r17::fmix32(f0 + 0x9E3779B9u * 2u + fser * 0x85EBCA6Bu) ^ colour); s.ftri.push_back(r17::fmix32(f0 + 0x9E3779B9u * 3u + fser * 0x85EBCA6Bu) ^ (t ^ sw[1]));
        for (uint32_t k = 3; k < 8; ++k) s.freg[k - 3].push_back(r17::fmix32(f0 + 0x9E3779B9u * (k + 1u) + fser * 0x85EBCA6Bu) ^ sw[k - 1]);
        s.fxyraw.push_back(i | (j << 16)); s.fid.push_back(fid);
        s.ftbase.push_back(dsw ? uint32_t(tix[tk] * 4096u) : tk); s.fmode.push_back(uint8_t(mode));
        ++nrun;
    };
    // the raster: the reference's rule, the edge functions stepped; the fragments listed in the reference's order
    for (uint32_t t = 0; t < T; ++t) {
        const uint32_t cw = r16f::fchain(sw[t & 7], t), u = r17::fmix32(cw ^ 0x2545F491u), colour = r17::fmix32(cw ^ 0x9E3779B9u), mode = (u >> 16) % 5u;
        auto cl = [&](int32_t v) { return v < 0 ? 0 : v > int32_t(side) ? int32_t(side) : v; };
        const int32_t x0 = int32_t(cw & (side - 1u)), y0 = int32_t((cw >> 8) & (side - 1u));
        const int32_t sp = int32_t(c.p.scratch_bytes), span = 2 * sp + 1;  // the corner offsets in -spread .. +spread (the document's "spread")
        const int32_t x1 = cl(x0 + int32_t(u % uint32_t(span)) - sp), y1 = cl(y0 + int32_t((u >> 8) % uint32_t(span)) - sp), x2 = cl(x0 + int32_t((u >> 16) % uint32_t(span)) - sp), y2 = cl(y0 + int32_t((u >> 24) % uint32_t(span)) - sp);
        const int64_t ax = x0, ay = y0, bx = x1, by = y1, cx = x2, cy = y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);
        if (area == 0) continue;
        const int64_t sg = area > 0 ? 1 : -1;
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == sg; else e[k].tl = (-dy > 0 ? 1 : -1) == sg; }
        const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
        // w(i, j) = sg * (A (2j + 1 - 2ay) - B (2i + 1 - 2ax)), A = 2 (bx - ax), B = 2 (by - ay): the reference's expression, affine in i and j
        int64_t w0[3], di[3], dj[3];
        for (int k = 0; k < 3; ++k) {
            const int64_t A = 2 * (e[k].bx - e[k].ax), B = 2 * (e[k].by - e[k].ay);
            w0[k] = (A * (2 * miny + 1 - 2 * e[k].ay) - B * (2 * minx + 1 - 2 * e[k].ax)) * sg;
            di[k] = -2 * B * sg; dj[k] = 2 * A * sg;
        }
        for (int64_t j = miny; j < maxy; ++j) {
            int64_t w[3] = { w0[0] + (j - miny) * dj[0], w0[1] + (j - miny) * dj[1], w0[2] + (j - miny) * dj[2] };
            for (int64_t i = minx; i < maxx; ++i) {
                bool in = true;
                for (int k = 0; k < 3; ++k) if (w[k] < 0 || (w[k] == 0 && !e[k].tl)) { in = false; break; }
                if (in && !(fcap && nrun >= fcap)) {  // the exact count: only the first fcap covered fragments in raster order
                    if (fcap) s.covered[size_t(j) * side + size_t(i)] = 1;
                    emit(t, uint32_t(i), uint32_t(j), colour, mode, minx);
                }
                w[0] += di[0]; w[1] += di[1]; w[2] += di[2];
            }
        }
    }
    // the fill (the exact count): the uncovered pixels in raster order with the triangle index 0 and the colour 0, mode 0, until fcap passes
    if (fcap && nrun < fcap) for (uint32_t j = 0; j < side && nrun < fcap; ++j) for (uint32_t i = 0; i < side && nrun < fcap; ++i) if (!s.covered[size_t(j) * side + i]) emit(0, i, j, 0, 0, 0);
    const size_t n = s.fxy.size();
    // the coalesced rows: run r (passes 16r .. 16r + 15) streams `rows` items drawn over the whole dataset, item q of the run read once
    // and its sixteen 16-byte slices folded for the run's passes (slice l to pass 16r + l); resident the item in place with the run's
    // next items prefetched; light the item derived
    if (nrows && coal) {
        const uint64_t all_rows = c.n_items();
        uint32_t citem[64];
        for (size_t base = 0; base < n; base += 16) {
            const uint32_t run = uint32_t(base >> 4), lanes = uint32_t(std::min<size_t>(16, n - base));
            auto row_of = [&](uint32_t q) -> uint64_t {
                return contig == 1u ? uint64_t(r17::fmix32(run ^ sw[7] ^ (q * 0x9E3779B9u))) % all_rows
                                    : uint64_t(r17::fmix32(run ^ sw[7] ^ ((q / contig) * 0x9E3779B9u))) % (all_rows - contig) + (q % contig);
            };
            for (uint32_t q = 0; q < nrows; ++q) {
                const uint64_t row = row_of(q);
                const uint32_t* item;
                if (dsw) {
                    if (q + 8 < nrows) { const char* p = reinterpret_cast<const char*>(dsw + row_of(q + 8) * 64); for (int l = 0; l < 256; l += 64) __builtin_prefetch(p + l, 0, 0); }
                    item = dsw + row * 64;
                } else { load_item(c, row, citem); item = citem; }
                if (count_rows_flag()) s.rows_seen.push_back(row);
                const uint32_t a0 = uint32_t(row) ^ sw[6], a1 = uint32_t(row) * 0x9E3779B1u, a2 = sw[5] ^ q, a3 = sw[7] + uint32_t(row);
                uint32_t* dst = s.freg[q % 5].data() + base;
                for (uint32_t l = 0; l < lanes; ++l) {
                    const uint32_t* v = item + 4 * l;
                    const uint32_t b0 = (a0 ^ v[0]) + v[0], b1 = (a1 ^ v[1]) + v[1], b2 = (a2 ^ v[2]) + v[2], b3 = (a3 ^ v[3]) + v[3];
                    dst[l] ^= b0 ^ b1 ^ b2 ^ b3;
                }
            }
        }
    }
    // the row folds: r3..r7 per fragment from the scene words and the fragment's R rows; resident: the rows read in place with the
    // rows of the fragment four ahead prefetched (its addresses from its identity alone); light: the rows derived on demand
    if (nrows && !coal) {
        const uint32_t stride = row_count / nrows;
        auto row_words = [&](uint32_t row) -> const uint32_t* {
            const uint32_t k = row >> 6, y = row & 63u;
            if (dsw) return dsw + size_t(tix[k]) * 4096u + size_t(y) * 64u;
            uint32_t* p = &s.tl[(size_t(k) * 64 + y) * 64];
            if (!s.have[size_t(k) * 64 + y]) { load_item(c, tix[k] * 64 + y, p); s.have[size_t(k) * 64 + y] = 1; }
            return p;
        };
        constexpr size_t kAhead = 4;
        for (size_t f = 0; f < n; ++f) {
            if (dsw && f + kAhead < n) {
                const uint32_t st = r17::fmix32(s.fid[f + kAhead] ^ sw[7]);
                for (uint32_t q = 0; q < nrows; ++q) { const uint32_t row = (st + q * stride) % row_count; const char* p = reinterpret_cast<const char*>(dsw + size_t(tix[row >> 6]) * 4096u + size_t(row & 63u) * 64u); for (int l = 0; l < 256; l += 64) __builtin_prefetch(p + l, 0, 0); }
            }
            const uint32_t start = r17::fmix32(s.fid[f] ^ sw[7]);
            for (uint32_t q = 0; q < nrows; ++q) {
                const uint32_t row = (start + q * stride) % row_count;
                s.freg[q % 5][f] ^= fold_row(row_words(row), row, q, sw);
                if (count_rows_flag()) s.rows_seen.push_back(tix[row >> 6] * 64 + (row & 63u));
            }
        }
    }
    size_t np = n;
#if defined(__AVX2__)
    if (dsw) {  // the last pass padded with copies of fragment 0 (valid addresses; the padded lanes' results are dropped)
        np = (n + 7) & ~size_t(7);
        const uint32_t p0 = n ? s.fxy[0] : 0, p1 = n ? s.fcol[0] : 0, p2 = n ? s.ftri[0] : 0, p3 = n ? s.ftbase[0] : 0;
        uint32_t pr[5]; for (int k = 0; k < 5; ++k) pr[k] = n ? s.freg[k][0] : 0;
        for (size_t f = n; f < np; ++f) { s.fxy.push_back(p0); s.fcol.push_back(p1); s.ftri.push_back(p2); s.ftbase.push_back(p3); for (int k = 0; k < 5; ++k) s.freg[k].push_back(pr[k]); }
    }
#endif
    s.fout.resize(np);
#if defined(__AVX2__)
    if (dsw) run_avx2(P, s, np, dsw); else run_scalar(c, P, s, n, nullptr, tix);
#else
    run_scalar(c, P, s, n, dsw, tix);
#endif
    if (count_rows_flag()) { std::sort(s.rows_seen.begin(), s.rows_seen.end()); rows_read_last() = uint64_t(std::unique(s.rows_seen.begin(), s.rows_seen.end()) - s.rows_seen.begin()); }
    // the blends in the reference's order
    s.fb.assign(size_t(side) * side * 4, 0);
    for (size_t f = 0; f < n; ++f) {
        const uint32_t xy = s.fxyraw[f], i = xy & 0xFFFFu, j = xy >> 16, col = s.fout[f], mode = s.fmode[f];
        uint8_t* q = &s.fb[(size_t(j) * side + size_t(i)) * 4];
        for (int k = 0; k < 4; ++k) q[k] = blend_byte(mode, q[k], (col >> (8 * k)) & 255u);
    }
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag(kTagMix).raw(c.E, 32).raw(S, 32).raw(P.phash, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    blake3_hasher_update(&h, s.fb.data(), s.fb.size());
    blake3_hasher_finalize(&h, M, 32);
}

inline bool reference_path() {
    static const bool ref = [] { const char* e = std::getenv("IGNH_KILN_REFERENCE"); return e && e[0] == '1'; }();
    return ref;
}
// the verifier's entry: the fast render unless the reference is asked for
inline void verify(const ih_ctx& c, const uint8_t header[32], uint64_t nonce, const uint8_t S[32], uint8_t M[32]) {
    if (reference_path()) attempt(c, header, nonce, S, M); else attempt_fast(c, header, nonce, S, M);
}
}  // namespace kiln
