// KILN on Vulkan (the Kiln seat, 15 October 2026): IgneumHash version 22 (pow/ref/src/igneumhash.cpp, namespace kiln) rendered
// through the card's real graphics pipeline, headless. THE BATCH: a fragment shader drawn from the batch seed (32 instructions
// over eight registers), emitted as GLSL and compiled by the driver (shaderc to SPIR-V, the driver to the card) while the current
// batch renders. THE HASH: the scene from the work seed (T integer-corner triangles over a side x side RGBA8 target, w = 1, the
// top-left rule the rasteriser's), the keyed 64 x 64 tile of the 1 GiB epoch dataset NEAREST-fetched (texelFetch from the dataset
// resident as one 16384 x 16384 R32_UINT image: tile T is the run of 4096 words at row T >> 2, column (T & 3) << 12; or, on the
// copy path, a compute pre-pass streams every (hash, tile) by plain coalesced loads into a frame-local cache the fragments index),
// the five blends as INTEGER operations in the fragment shader on the target's current word (the consensus chair's condition: the
// target a storage image loaded, blended and stored by the fragment, never the fixed-function blender; one pass instance a triangle
// slot so no two fragments of a pixel share a draw, a barrier between slots), the digest BLAKE3 over the target in row order with E,
// S and the programme hash, the final digest as every version. K hashes a render as a grid of tiles on one target (32 tiles a row). Two frames in flight; the seeds,
// scenes, vertices and digests on the card (BLAKE3 in compute passes before and after the draws, the host fills one 512-byte header a frame); the compile of batch n + 1 (five pipelines sharing one cache) ahead on its own threads.
// Exactness: --set <vectors.json> renders the set's rows and compares mix and hash against the file (the CPU reference's output)
// and against ignpow_hash on a light context; the throughput run compares one hash a render the same way.
#include <vulkan/vulkan.h>
#include <shaderc/shaderc.h>
#include <blake3.h>
#include "ignpow.h"
#include "igneumhash.h"
#include <algorithm>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <functional>
#include <future>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

extern "C" void* ignh_inner_create(const char* json, uint64_t height, int full, int* status);
extern "C" const uint8_t* ignh_inner_dataset(const void* ctx, uint64_t* bytes);
extern "C" void ignh_inner_destroy(void* ctx);

#define VK_CHECK(x) do { VkResult r_ = (x); if (r_ != VK_SUCCESS) { std::fprintf(stderr, "vulkan: %s = %d at %s:%d\n", #x, int(r_), __FILE__, __LINE__); std::exit(2); } } while (0)

static inline uint32_t rotl32(uint32_t x, uint32_t n) { n &= 31; return n ? (x << n) | (x >> (32 - n)) : x; }
static inline uint32_t fmix32(uint32_t h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }
static inline uint32_t fchain(uint32_t x, uint32_t v) { x = rotl32(x ^ v, 5) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
static inline void put_le32(uint8_t* p, uint32_t v) { p[0] = uint8_t(v); p[1] = uint8_t(v >> 8); p[2] = uint8_t(v >> 16); p[3] = uint8_t(v >> 24); }
static inline uint32_t le32(const uint8_t* p) { return uint32_t(p[0]) | (uint32_t(p[1]) << 8) | (uint32_t(p[2]) << 16) | (uint32_t(p[3]) << 24); }

struct bytes {
    std::vector<uint8_t> v;
    bytes& tag(const char* t) { size_t n = std::strlen(t); v.push_back(uint8_t(n)); v.insert(v.end(), t, t + n); return *this; }
    bytes& raw(const uint8_t* p, size_t n) { v.insert(v.end(), p, p + n); return *this; }
    bytes& le64(uint64_t x) { for (int i = 0; i < 8; ++i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    bytes& be32(uint32_t x) { for (int i = 3; i >= 0; --i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
    bytes& be64(uint64_t x) { for (int i = 7; i >= 0; --i) v.push_back(uint8_t(x >> (8 * i))); return *this; }
};
static void b3(const uint8_t* in, size_t n, uint8_t out[32]) { blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, in, n); blake3_hasher_finalize(&h, out, 32); }
static std::string hex(const uint8_t* p, size_t n) { static const char* d = "0123456789abcdef"; std::string s; for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; } return s; }
static bool unhex(const std::string& s, uint8_t* out, size_t n) { if (s.size() != 2 * n) return false; for (size_t i = 0; i < n; ++i) out[i] = uint8_t(std::stoul(s.substr(2 * i, 2), nullptr, 16)); return true; }

// ---- the document ----
struct Doc { bool copy_path = false; ignh_params p; uint8_t P[32]; uint8_t E[32]; uint32_t T, words, side, batch_log2, tiles, rows, spread, fcap, scn, contig = 1; bool coal = false; uint64_t items, n_tiles; std::string json; };

// ---- the host side of one hash ----
struct Tri { int32_t x0, y0, x1, y1, x2, y2; uint32_t colour, mode, minx; };
struct HashJob {
    uint64_t height, nonce; uint8_t header[32]; uint8_t S[32]; uint32_t sw[8]; uint32_t tile[64]; Tri tri[64];
    uint8_t M[32], F[32];
};
struct Batch { uint8_t seed[32]; std::vector<uint32_t> pw; uint8_t phash[32]; };

static void batch_of(const Doc& d, const uint8_t header[32], uint64_t nonce, Batch& b) {
    bytes in; in.tag("IgneumHash/Kiln/batch").raw(d.E, 32).raw(header, 32).le64(nonce >> d.batch_log2); b3(in.v.data(), in.v.size(), b.seed);
    b.pw.assign(size_t(d.words) * 2, 0); std::vector<uint8_t> pb(size_t(d.words) * 8);
    uint32_t x[8]; for (int k = 0; k < 8; ++k) x[k] = le32(b.seed + 4 * k);
    for (uint32_t i = 0; i < d.words; ++i) for (int j = 0; j < 2; ++j) { const uint32_t idx = 2 * i + j; x[idx & 7] = fchain(x[idx & 7], idx); b.pw[idx] = x[idx & 7]; for (int q = 0; q < 4; ++q) pb[size_t(idx) * 4 + q] = uint8_t(x[idx & 7] >> (8 * q)); }
    b3(pb.data(), pb.size(), b.phash);
}
static void work_seed(const Doc& d, const uint8_t header[32], uint64_t nonce, uint8_t S[32]) {
    bytes in; in.tag("IgneumHash/R0A/seed").be64(d.p.chain_id).be32(d.p.version).raw(d.P, 32).raw(header, 32).be64(nonce); b3(in.v.data(), in.v.size(), S);
}
static void scene_of(const Doc& d, HashJob& j) {
    work_seed(d, j.header, j.nonce, j.S);
    uint8_t sc[32]; { bytes in; in.tag("IgneumHash/Kiln/scene").raw(d.E, 32).raw(j.S, 32); b3(in.v.data(), in.v.size(), sc); }
    for (int k = 0; k < 8; ++k) j.sw[k] = le32(sc + 4 * k);
    for (uint32_t k = 0; k < d.tiles; ++k) j.tile[k] = uint32_t(uint64_t(fchain(j.sw[0], 1000 + k)) % d.n_tiles);   // the reference's rule at every tile count
    const int32_t side = int32_t(d.side);
    auto cl = [&](int32_t v) { return v < 0 ? 0 : v > side ? side : v; };
    for (uint32_t t = 0; t < d.T; ++t) {
        const uint32_t cw = fchain(j.sw[t & 7], t), u = fmix32(cw ^ 0x2545F491u);
        Tri& r = j.tri[t];
        r.x0 = int32_t(cw & (d.side - 1u)); r.y0 = int32_t((cw >> 8) & (d.side - 1u));
        const int32_t sp = int32_t(d.spread), span = 2 * sp + 1;  // the corner offsets in -spread .. +spread
        r.x1 = cl(r.x0 + int32_t(u % uint32_t(span)) - sp); r.y1 = cl(r.y0 + int32_t((u >> 8) % uint32_t(span)) - sp); r.x2 = cl(r.x0 + int32_t((u >> 16) % uint32_t(span)) - sp); r.y2 = cl(r.y0 + int32_t((u >> 24) % uint32_t(span)) - sp);
        r.colour = fmix32(cw ^ 0x9E3779B9u); r.mode = (u >> 16) % 5u; r.minx = uint32_t(std::min(r.x0, std::min(r.x1, r.x2)));
    }
}
static void digest_of(const Doc& d, const Batch& b, HashJob& j, const uint8_t* target /* side*side*4, row order */) {
    blake3_hasher h; blake3_hasher_init(&h);
    { bytes in; in.tag("IgneumHash/Kiln/mix").raw(d.E, 32).raw(j.S, 32).raw(b.phash, 32); blake3_hasher_update(&h, in.v.data(), in.v.size()); }
    blake3_hasher_update(&h, target, size_t(d.side) * d.side * 4);
    blake3_hasher_finalize(&h, j.M, 32);
    bytes in; in.tag("IgneumHash/R0A/final").be64(d.p.chain_id).be32(d.p.version).raw(d.P, 32).raw(j.S, 32).raw(j.M, 32); b3(in.v.data(), in.v.size(), j.F);
}

// ---- the CPU renderer (the reference's rule, for the fragment count and the named first divergent pixel) ----
static void cpu_render(const Doc& d, const Batch& b, const HashJob& j, const uint32_t* dataset_words, std::vector<uint8_t>& fb, uint64_t* fragments) {
    const uint32_t side = d.side; fb.assign(size_t(side) * side * 4, 0); uint64_t frags = 0;
    std::vector<uint8_t> covered(size_t(side) * side, 0); uint32_t nrun = 0; const uint32_t all_rows = uint32_t(d.items);
    auto coal_rows = [&](uint32_t index, uint32_t* rg) {  // the run's rows over the whole dataset, the fragment's 16-byte slice
        const uint32_t run = index >> 4, sl = (index & 15u) * 4u;
        for (uint32_t q = 0; q < d.rows; ++q) { const uint32_t row = d.contig == 1 ? fmix32(run ^ j.sw[7] ^ (q * 0x9E3779B9u)) % all_rows : fmix32(run ^ j.sw[7] ^ ((q / d.contig) * 0x9E3779B9u)) % (all_rows - d.contig) + (q % d.contig); const uint32_t* it = dataset_words + uint64_t(row) * 64; uint32_t acc[4] = { row ^ j.sw[6], row * 0x9E3779B1u, j.sw[5] ^ q, j.sw[7] + row }; for (int k = 0; k < 4; ++k) { const uint32_t v = it[sl + uint32_t(k)]; acc[k] = (acc[k] ^ v) + v; } rg[3 + (q % 5)] ^= acc[0] ^ acc[1] ^ acc[2] ^ acc[3]; }
    };
    auto run_fragment = [&](uint32_t t, uint32_t x, uint32_t y, uint32_t colour, uint32_t mode, uint32_t minx, uint32_t index) {
        const uint32_t fid = (t << (2u * d.p.registers)) | (y << d.p.registers) | x, tk = (fid >> 4) % d.tiles;
        const uint32_t* tl = dataset_words + uint64_t(j.tile[tk]) * 4096;
        const uint32_t f0 = x | (y << 16) | (t << 26), fser = x - minx;
        uint32_t rg[8];
        for (uint32_t k = 0; k < 8; ++k) rg[k] = fmix32(f0 + 0x9E3779B9u * (k + 1u) + fser * 0x85EBCA6Bu) ^ (k == 0 ? f0 : k == 1 ? colour : k == 2 ? (t ^ j.sw[1]) : j.sw[k - 1]);
        if (d.rows && d.coal) coal_rows(index, rg);
        else if (d.rows) { const uint32_t row_count = d.tiles * 64u, start = fmix32(fid ^ j.sw[7]), stride = row_count / d.rows;
            for (uint32_t q = 0; q < d.rows; ++q) { const uint32_t row = (start + q * stride) % row_count; uint32_t acc[4] = { row ^ j.sw[6], row * 0x9E3779B1u, j.sw[5] ^ q, j.sw[7] + row }; const uint32_t* rp = dataset_words + uint64_t(j.tile[row >> 6]) * 4096 + (row & 63u) * 64u; for (uint32_t w = 0; w < 64; w += 4) for (int k = 0; k < 4; ++k) { const uint32_t v = rp[w + k]; acc[k] = (acc[k] ^ v) + v; } rg[3 + (q % 5)] ^= acc[0] ^ acc[1] ^ acc[2] ^ acc[3]; } }
        if (std::getenv("KILN_REFDBG") && index == 0) std::fprintf(stderr, "port frag0: t %u x %u y %u fid %08x tk %u tile %u regs %08x %08x %08x %08x %08x %08x %08x %08x\n", t, x, y, fid, tk, j.tile[tk], rg[0], rg[1], rg[2], rg[3], rg[4], rg[5], rg[6], rg[7]);
        for (uint32_t q = 0; q < d.words; ++q) {
            const uint32_t w0 = b.pw[2 * q], imm = b.pw[2 * q + 1], a = rg[(q + 3) & 7], bb = rg[(q + 5) & 7]; uint32_t& dd = rg[q & 7]; const uint32_t d0 = dd;
            switch (w0 % 6u) { case 0: dd = a + bb + imm + rotl32(d0, 9); break; case 1: dd = a ^ bb ^ imm ^ rotl32(d0, 17); break; case 2: dd = rotl32(a, imm & 31u) ^ bb ^ d0; break; case 3: dd = a * bb + imm + d0; break; case 4: dd = (uint32_t((uint64_t(a) * uint64_t(bb)) >> 32) ^ imm) + rotl32(d0, 13); break; default: dd = tl[(a + imm + d0) & 4095u] ^ bb ^ rotl32(d0, 5); break; }
        }
        uint8_t* q = &fb[(size_t(y) * side + size_t(x)) * 4];
        for (int k = 0; k < 4; ++k) { const uint32_t dst = q[k], src = (rg[7] >> (8 * k)) & 255u; uint32_t o;
            switch (mode) { case 0: o = std::min<uint32_t>(255u, dst + src); break; case 1: o = src; break; case 2: o = dst > src ? dst - src : 0u; break; case 3: o = std::min(dst, src); break; default: o = std::max(dst, src); break; }
            q[k] = uint8_t(o); }
    };
    for (uint32_t t = 0; t < d.T; ++t) {
        const Tri& r = j.tri[t];
        const int64_t ax = r.x0, ay = r.y0, bx = r.x1, by = r.y1, cx = r.x2, cy = r.y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (area == 0) continue;
        const int64_t sg = area > 0 ? 1 : -1;
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; ++k) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; e[k].tl = dy == 0 ? ((dx > 0 ? 1 : -1) == sg) : ((-dy > 0 ? 1 : -1) == sg); }
        const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
        for (int64_t y = miny; y < maxy; ++y) for (int64_t x = minx; x < maxx; ++x) {
            const int64_t px = 2 * x + 1, py = 2 * y + 1; bool in = true;
            for (int k = 0; k < 3 && in; ++k) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * sg; if (w < 0 || (w == 0 && !e[k].tl)) in = false; }
            if (!in) continue;
            if (d.fcap && nrun >= d.fcap) continue;  // the exact count: only the first fcap covered fragments in raster order
            ++frags; ++nrun; covered[size_t(y) * side + size_t(x)] = 1;
            run_fragment(t, uint32_t(x), uint32_t(y), r.colour, r.mode, r.minx, nrun - 1u);
        }
    }
    if (d.fcap && nrun < d.fcap) {  // the fill: the uncovered pixels in raster order, triangle 0, colour 0, ADD, until exactly fcap passes
        for (uint32_t y = 0; y < side && nrun < d.fcap; ++y) for (uint32_t x = 0; x < side && nrun < d.fcap; ++x) { if (covered[size_t(y) * side + x]) continue; ++frags; ++nrun; run_fragment(0, x, y, 0, 0, 0, nrun - 1u); }
    }
    if (fragments) *fragments = frags;
}

// ---- the shader text ----
static std::string glsl_vertex() {
    return "#version 450\nlayout(location=0) in vec2 p; layout(location=1) in uint c; layout(location=2) in uint t; layout(location=3) in uint h;\n"
           "layout(location=0) flat out uint vcol; layout(location=1) flat out uint vtri; layout(location=2) flat out uint vhash;\n"
           "void main() { gl_Position = vec4(p, 0.0, 1.0); vcol = c; vtri = t; vhash = h; }\n";
}
static std::string glsl_fragment(const Doc& d, const Batch& b) {
    std::ostringstream o;
    o << "#version 450\nlayout(location=0) flat in uint vcol; layout(location=1) flat in uint vtm; layout(location=2) flat in uint vhash;\n"
         "layout(set=0, binding=2, r32ui) uniform uimage2D target;\n"
      << (d.copy_path ? "layout(std430, set=0, binding=0) readonly buffer Tiles { uint tc[]; };\nlayout(std430, set=0, binding=3) readonly buffer Tiles4 { uvec4 tc4[]; };\nlayout(std430, set=0, binding=4) readonly buffer Data4 { uvec4 ds4[]; };\n" : "layout(set=0, binding=0) uniform usampler2D dset;\n")
      << "layout(std430, set=0, binding=1) readonly buffer Scenes { uint sc[]; };\n"   // per hash: s1..s6, then tiles x (tile index)
      << "uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }\n"
         "void main() {\n  ivec2 pc = ivec2(gl_FragCoord.xy); uint x = uint(pc.x) & " << (d.side - 1) << "u, y = uint(pc.y) & " << (d.side - 1) << "u; uint vtri = vtm & 255u, vmode = (vtm >> 8) & 255u, vminx = (vtm >> 16) & 0x7FFFu, vfill = vtm >> 31;\n"
      << "  uint base = vhash * " << d.scn << "u;\n"
      << "  uint s1 = sc[base + 1u], s2 = sc[base + 2u], s3 = sc[base + 3u], s4 = sc[base + 4u], s5 = sc[base + 5u], s6 = sc[base + 6u], s7 = sc[base + 7u];\n"
      << "  uint fid = (vtri << " << (2 * d.p.registers) << "u) | (y << " << d.p.registers << "u) | x;\n"
      << (d.fcap ? "  uint tb = base + " + std::to_string(8 + d.tiles) + "u, index;\n"
                   "  if (vfill == 1u) { uint m = sc[tb + " + std::to_string(d.T * d.side) + "u + y]; if (((m >> x) & 1u) == 1u) discard; uint cb = 0u; for (uint yy = 0u; yy < y; ++yy) cb += bitCount(sc[tb + " + std::to_string(d.T * d.side) + "u + yy]); cb += bitCount(m & ((1u << x) - 1u)); index = sc[tb + " + std::to_string(d.T * d.side + d.side) + "u] + (y * " + std::to_string(d.side) + "u + x) - cb; }\n"
                   "  else { uint e = sc[tb + vtri * " + std::to_string(d.side) + "u + y]; index = (e & 0xFFFFu) + (x - (e >> 16)); }\n"
                   "  if (index >= " + std::to_string(d.fcap) + "u) discard;\n" : "")
      << "  uint f0 = x | (y << 16) | (vtri << 26), fser = x - vminx;\n"
      << "  uint r0 = fmix32(f0 + 0x9E3779B9u * 1u + fser * 0x85EBCA6Bu) ^ f0, r1 = fmix32(f0 + 0x9E3779B9u * 2u + fser * 0x85EBCA6Bu) ^ vcol, r2 = fmix32(f0 + 0x9E3779B9u * 3u + fser * 0x85EBCA6Bu) ^ (vtri ^ s1);\n"
      << "  uint r3 = fmix32(f0 + 0x9E3779B9u * 4u + fser * 0x85EBCA6Bu) ^ s2, r4 = fmix32(f0 + 0x9E3779B9u * 5u + fser * 0x85EBCA6Bu) ^ s3, r5 = fmix32(f0 + 0x9E3779B9u * 6u + fser * 0x85EBCA6Bu) ^ s4, r6 = fmix32(f0 + 0x9E3779B9u * 7u + fser * 0x85EBCA6Bu) ^ s5, r7 = fmix32(f0 + 0x9E3779B9u * 8u + fser * 0x85EBCA6Bu) ^ s6;\n";
    // the fragment's tile from its own identity (main 371d68d626c7): (((t << 2 tl) | (y << tl) | x) >> 4) mod tiles
    o << "  uint tk = (fid >> 4) % " << d.tiles << "u;\n";
    if (d.copy_path) o << "  uint tcb = sc[base + 8u + tk] * 4096u;\n";   // the tile's base in the dataset buffer (plain loads)
    else o << "  uint tix = sc[base + 8u + tk]; int tx0 = int((tix & 3u) << 10), ty = int(tix >> 2);\n";   // the dataset image holds four words a texel: tile T at row T >> 2, texels (T & 3) << 10 onward
    if (d.rows && d.coal) {  // the coalesced rows: the run's rows over the whole dataset, this fragment's 16-byte slice (one warp instruction reads 512 contiguous bytes)
        // the rows are independent of one another, so U fetches are issued before any is consumed (the memory-level parallelism the serial loop lacked)
        uint32_t U = d.rows % 16 == 0 ? 16 : d.rows % 8 == 0 ? 8 : d.rows % 4 == 0 ? 4 : 1;
        if (const char* e = std::getenv("KILN_U")) { const uint32_t u = uint32_t(std::atoi(e)); if (u && d.rows % u == 0) U = u; }
        o << "  { uint run = index >> 4, sl = index & 15u; uint rr0 = 0u, rr1 = 0u, rr2 = 0u, rr3 = 0u, rr4 = 0u; uint rw[" << U << "]; uvec4 vv[" << U << "];\n"
          << "    for (uint q0 = 0u; q0 < " << d.rows << "u; q0 += " << U << "u) {\n"
          << "      for (uint u = 0u; u < " << U << "u; ++u) { uint q = q0 + u; uint row = " << (d.contig == 1 ? "fmix32(run ^ s7 ^ (q * 0x9E3779B9u)) % " + std::to_string(uint32_t(d.items)) + "u" : "fmix32(run ^ s7 ^ ((q / " + std::to_string(d.contig) + "u) * 0x9E3779B9u)) % " + std::to_string(uint32_t(d.items) - d.contig) + "u + (q % " + std::to_string(d.contig) + "u)") << "; rw[u] = row; "
          << (std::getenv("KILN_NOFETCH") ? "vv[u] = uvec4(row, row * 3u, row * 5u, row * 7u); }\n" : std::getenv("KILN_NOROWS") ? "vv[u] = uvec4(0u); row = 0u; }\n" : d.copy_path ? "vv[u] = ds4[row * 16u + sl]; }\n" : "vv[u] = texelFetch(dset, ivec2(int(((row & 255u) << 4) | sl), int(row >> 8)), 0); }\n")
          << "      for (uint u = 0u; u < " << U << "u; ++u) { uint q = q0 + u, row = rw[u]; uvec4 acc = uvec4(row ^ s6, row * 0x9E3779B1u, s5 ^ q, s7 + row); acc = (acc ^ vv[u]) + vv[u]; uint h = acc.x ^ acc.y ^ acc.z ^ acc.w; uint qm = q % 5u; rr0 ^= qm == 0u ? h : 0u; rr1 ^= qm == 1u ? h : 0u; rr2 ^= qm == 2u ? h : 0u; rr3 ^= qm == 3u ? h : 0u; rr4 ^= qm == 4u ? h : 0u; } }\n"
          << "    r3 ^= rr0; r4 ^= rr1; r5 ^= rr2; r6 ^= rr3; r7 ^= rr4; }\n";
    } else if (d.rows) {  // the row fetches, the q loop unrolled (the destination register is static per q), the 64-word row loop on the card
        const uint32_t row_count = d.tiles * 64u, stride = row_count / d.rows;
        o << "  uint start = fmix32(fid ^ s7);\n";
        for (uint32_t q = 0; q < d.rows; ++q) {
            o << "  { uint row = (start + " << (q * stride) << "u) % " << row_count << "u; uvec4 acc = uvec4(row ^ s6, row * 0x9E3779B1u, s5 ^ " << q << "u, s7 + row); ";
            if (d.copy_path) o << "uint rb = (sc[base + 8u + (row >> 6)] * 4096u + (row & 63u) * 64u) >> 2; for (uint w = 0u; w < 16u; ++w) { uvec4 v = tc4[rb + w]; acc = (acc ^ v) + v; }";
            else o << "uint rtix = sc[base + 8u + (row >> 6)]; int rx0 = int((rtix & 3u) << 10) + int((row & 63u) * 16u), ry = int(rtix >> 2); for (int w = 0; w < 16; ++w) { uvec4 v = texelFetch(dset, ivec2(rx0 + w, ry), 0); acc = (acc ^ v) + v; }";
            o << " r" << (3 + q % 5) << " ^= acc.x ^ acc.y ^ acc.z ^ acc.w; }\n";
        }
    }
    for (uint32_t q = 0; q < d.words; ++q) {
        const uint32_t w0 = b.pw[2 * q], imm = b.pw[2 * q + 1]; const std::string a = "r" + std::to_string((q + 3) & 7), bb = "r" + std::to_string((q + 5) & 7), dd = "r" + std::to_string(q & 7), im = std::to_string(imm) + "u";
        auto rot = [&](const std::string& v, uint32_t k) { return "((" + v + " << " + std::to_string(k) + "u) | (" + v + " >> " + std::to_string(32 - k) + "u))"; };
        switch (w0 % 6u) {
            case 0: o << "  " << dd << " = " << a << " + " << bb << " + " << im << " + " << rot(dd, 9) << ";\n"; break;
            case 1: o << "  " << dd << " = " << a << " ^ " << bb << " ^ " << im << " ^ " << rot(dd, 17) << ";\n"; break;
            case 2: { const uint32_t k = imm & 31u; o << "  " << dd << " = " << (k ? rot(a, k) : a) << " ^ " << bb << " ^ " << dd << ";\n"; break; }
            case 3: o << "  " << dd << " = " << a << " * " << bb << " + " << im << " + " << dd << ";\n"; break;
            case 4: o << "  { uint hi, lo; umulExtended(" << a << ", " << bb << ", hi, lo); " << dd << " = (hi ^ " << im << ") + " << rot(dd, 13) << "; }\n"; break;
            default: o << "  { uint ix = (" << a << " + " << im << " + " << dd << ") & 4095u; " << dd << " = " << (d.copy_path ? "tc[tcb + ix]" : "texelFetch(dset, ivec2(tx0 + int(ix >> 2), ty), 0)[ix & 3u]") << " ^ " << bb << " ^ " << rot(dd, 5) << "; }\n"; break;
        }
    }
    // the blend as integer operations on the target's current word (the consensus chair's condition c2d1eae93): the five modes per byte
    o << "  uint dst = imageLoad(target, pc).x, src = r7, res;\n"
         "  if (vmode == 1u) res = src;\n"
         "  else if (vmode == 0u) { res = 0u; for (int k = 0; k < 4; ++k) { uint s = (dst >> (8 * k)) & 255u, t = (src >> (8 * k)) & 255u; res |= min(s + t, 255u) << (8 * k); } }\n"
         "  else if (vmode == 2u) { res = 0u; for (int k = 0; k < 4; ++k) { uint s = (dst >> (8 * k)) & 255u, t = (src >> (8 * k)) & 255u; res |= (s > t ? s - t : 0u) << (8 * k); } }\n"
         "  else if (vmode == 3u) { res = 0u; for (int k = 0; k < 4; ++k) { uint s = (dst >> (8 * k)) & 255u, t = (src >> (8 * k)) & 255u; res |= min(s, t) << (8 * k); } }\n"
         "  else { res = 0u; for (int k = 0; k < 4; ++k) { uint s = (dst >> (8 * k)) & 255u, t = (src >> (8 * k)) & 255u; res |= max(s, t) << (8 * k); } }\n"
         "  imageStore(target, pc, uvec4(res, 0u, 0u, 0u));\n}\n";
    return o.str();
}

// the copy path's pre-pass: one workgroup a (hash, tile): 4096 words from the dataset buffer at the tile's base into the frame's tile cache by plain coalesced loads
static std::string glsl_copy(const Doc& d) {
    std::ostringstream o;
    o << "#version 450\nlayout(local_size_x = 256) in;\nlayout(std430, set=0, binding=0) readonly buffer Data { uvec4 ds[]; };\nlayout(std430, set=0, binding=1) readonly buffer Scenes { uint sc[]; };\nlayout(std430, set=0, binding=2) writeonly buffer Tiles { uvec4 tc[]; };\n"
      << "void main() { uint g = gl_WorkGroupID.x, h = g / " << d.tiles << "u, k = g % " << d.tiles << "u; uint tix = sc[h * " << d.scn << "u + 8u + k]; uint src = tix * 1024u, dst = g * 1024u;\n"
      << "  for (uint i = gl_LocalInvocationID.x; i < 1024u; i += 256u) tc[dst + i] = ds[src + i]; }\n";
    return o.str();
}


// ---- the card's host work: BLAKE3 (1.8.2's compression, byte for byte) in GLSL, the seed and scene pass, the digest pass ----
static const char* kGlslBlake3 = R"(
const uint IV0 = 0x6A09E667u, IV1 = 0xBB67AE85u, IV2 = 0x3C6EF372u, IV3 = 0xA54FF53Au, IV4 = 0x510E527Fu, IV5 = 0x9B05688Cu, IV6 = 0x1F83D9ABu, IV7 = 0x5BE0CD19u;
const uint B3_CHUNK_START = 1u, B3_CHUNK_END = 2u, B3_PARENT = 4u, B3_ROOT = 8u;
uint rotr(uint x, uint n) { return (x >> n) | (x << (32u - n)); }
void g(inout uint s[16], uint a, uint b, uint c, uint d, uint mx, uint my) {
  s[a] += s[b] + mx; s[d] = rotr(s[d] ^ s[a], 16u); s[c] += s[d]; s[b] = rotr(s[b] ^ s[c], 12u);
  s[a] += s[b] + my; s[d] = rotr(s[d] ^ s[a], 8u); s[c] += s[d]; s[b] = rotr(s[b] ^ s[c], 7u); }
void b3_round(inout uint s[16], uint m[16]) {
  g(s, 0u, 4u, 8u, 12u, m[0], m[1]); g(s, 1u, 5u, 9u, 13u, m[2], m[3]); g(s, 2u, 6u, 10u, 14u, m[4], m[5]); g(s, 3u, 7u, 11u, 15u, m[6], m[7]);
  g(s, 0u, 5u, 10u, 15u, m[8], m[9]); g(s, 1u, 6u, 11u, 12u, m[10], m[11]); g(s, 2u, 7u, 8u, 13u, m[12], m[13]); g(s, 3u, 4u, 9u, 14u, m[14], m[15]); }
void b3_permute(inout uint m[16]) { uint t[16]; t[0] = m[2]; t[1] = m[6]; t[2] = m[3]; t[3] = m[10]; t[4] = m[7]; t[5] = m[0]; t[6] = m[4]; t[7] = m[13]; t[8] = m[1]; t[9] = m[11]; t[10] = m[12]; t[11] = m[5]; t[12] = m[9]; t[13] = m[14]; t[14] = m[15]; t[15] = m[8]; m = t; }
void b3_compress(uint cv[8], uint m[16], uint ctr, uint blen, uint flags, out uint o[16]) {
  uint s[16]; for (uint i = 0u; i < 8u; ++i) s[i] = cv[i]; s[8] = IV0; s[9] = IV1; s[10] = IV2; s[11] = IV3; s[12] = ctr; s[13] = 0u; s[14] = blen; s[15] = flags;
  for (uint r = 0u; r < 7u; ++r) { b3_round(s, m); if (r < 6u) b3_permute(m); }
  for (uint i = 0u; i < 8u; ++i) { o[i] = s[i] ^ s[i + 8u]; o[i + 8u] = s[i + 8u] ^ cv[i]; } }
void b3_chunk(uint w[256], uint nbytes, uint ctr, uint endflags, out uint cv[8]) {
  uint c[8]; c[0] = IV0; c[1] = IV1; c[2] = IV2; c[3] = IV3; c[4] = IV4; c[5] = IV5; c[6] = IV6; c[7] = IV7;
  uint nblocks = (nbytes + 63u) / 64u; if (nblocks == 0u) nblocks = 1u;
  for (uint b = 0u; b < nblocks; ++b) { uint m[16]; for (uint i = 0u; i < 16u; ++i) m[i] = w[b * 16u + i];
    uint blen = (b + 1u < nblocks) ? 64u : (nbytes - b * 64u); uint fl = (b == 0u ? B3_CHUNK_START : 0u) | (b + 1u == nblocks ? (B3_CHUNK_END | endflags) : 0u);
    uint o[16]; b3_compress(c, m, ctr, blen, fl, o); for (uint i = 0u; i < 8u; ++i) c[i] = o[i]; }
  cv = c; }
void b3_parent(uint l[8], uint r[8], uint flags, out uint cv[8]) {
  uint c[8]; c[0] = IV0; c[1] = IV1; c[2] = IV2; c[3] = IV3; c[4] = IV4; c[5] = IV5; c[6] = IV6; c[7] = IV7;
  uint m[16]; for (uint i = 0u; i < 8u; ++i) { m[i] = l[i]; m[i + 8u] = r[i]; } uint o[16]; b3_compress(c, m, 0u, 64u, B3_PARENT | flags, o); for (uint i = 0u; i < 8u; ++i) cv[i] = o[i]; }
void putb(inout uint w[256], uint at, uint b) { w[at >> 2] |= (b & 255u) << (8u * (at & 3u)); }
uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }
uint rotl(uint x, uint n) { n &= 31u; return n == 0u ? x : (x << n) | (x >> (32u - n)); }
uint fchain(uint x, uint v) { x = rotl(x ^ v, 5u) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
)";
// the frame header (words): 0..15 the seed prefix (64 bytes: tag, be64 chain, be32 version, P), 16..23 the header, 24..25 the nonce base, 26..39 the scene prefix (54 bytes: tag, E), 40..52 the mix prefix (52 bytes: tag, E), 53..60 the programme hash, 61..77 the final prefix (65 bytes: tag, be64 chain, be32 version, P), 78 n (the frame's hashes), 79 the compare slot
static std::string glsl_scene_pass(const Doc& d, uint32_t W, uint32_t H) {
    std::ostringstream o;
    o << "#version 450\nlayout(local_size_x = 64) in;\n"
         "layout(std430, set=0, binding=0) readonly buffer Hdr { uint hd[]; };\nlayout(std430, set=0, binding=1) writeonly buffer Scenes { uint sc[]; };\n"
         "layout(std430, set=0, binding=2) writeonly buffer Verts { uint vb[]; };\nlayout(std430, set=0, binding=4) writeonly buffer Out { uint ob[]; };\n"
      << kGlslBlake3
      << "void main() { uint i = gl_GlobalInvocationID.x, n = hd[78]; if (i >= n) return;\n"
         "  uint w[256]; for (uint k = 0u; k < 256u; ++k) w[k] = 0u;\n"
         "  for (uint k = 0u; k < 16u; ++k) w[k] = hd[k]; for (uint k = 0u; k < 8u; ++k) w[16u + k] = hd[16u + k];\n"
         "  uint nlo = hd[24] + i, nhi = hd[25] + ((nlo < hd[24]) ? 1u : 0u);\n"
         "  uint be_hi = ((nhi & 255u) << 24) | (((nhi >> 8) & 255u) << 16) | (((nhi >> 16) & 255u) << 8) | (nhi >> 24), be_lo = ((nlo & 255u) << 24) | (((nlo >> 8) & 255u) << 16) | (((nlo >> 16) & 255u) << 8) | (nlo >> 24);\n"
         "  w[24] = be_hi; w[25] = be_lo; uint S[8]; b3_chunk(w, 104u, 0u, B3_ROOT, S);\n"
         "  for (uint k = 0u; k < 256u; ++k) w[k] = 0u; for (uint k = 0u; k < 14u; ++k) w[k] = hd[26u + k]; w[13] &= 0xFFFFu;\n"
         "  for (uint k = 0u; k < 32u; ++k) putb(w, 54u + k, S[k >> 2] >> (8u * (k & 3u)));\n"
         "  uint sw[8]; b3_chunk(w, 86u, 0u, B3_ROOT, sw);\n"
      << "  uint base = i * " << d.scn << "u; for (uint k = 0u; k < 8u; ++k) sc[base + k] = sw[k];\n"
      << "  for (uint k = 0u; k < " << d.tiles << "u; ++k) sc[base + 8u + k] = fchain(sw[0], 1000u + k) % " << uint32_t(d.n_tiles) << "u;\n"
      << "  for (uint k = 0u; k < 8u; ++k) ob[i * 24u + k] = S[k];\n"
      << "  float ox = float(" << d.side << "u * (i % 32u)), oy = float(" << d.side << "u * (i / 32u)); const float sx = 2.0 / " << W << ".0, sy = 2.0 / " << H << ".0;\n"
      << (d.fcap ? "  uint ncov = 0u; uint mask[32]; for (uint k = 0u; k < 32u; ++k) mask[k] = 0u; uint tb = base + " + std::to_string(8 + d.tiles) + "u;\n" : "")
      << "  for (uint t = 0u; t < " << d.T << "u; ++t) {\n"
      << "    uint cw = fchain(sw[t & 7u], t), u = fmix32(cw ^ 0x2545F491u), colour = fmix32(cw ^ 0x9E3779B9u), mode = (u >> 16) % 5u;\n"
      << "    int side = " << d.side << ", sp = " << d.spread << "; uint span = uint(2 * sp + 1);\n"
      << "    int x0 = int(cw & " << (d.side - 1) << "u), y0 = int((cw >> 8) & " << (d.side - 1) << "u);\n"
         "    int x1 = clamp(x0 + int(u % span) - sp, 0, side), y1 = clamp(y0 + int((u >> 8) % span) - sp, 0, side), x2 = clamp(x0 + int((u >> 16) % span) - sp, 0, side), y2 = clamp(y0 + int((u >> 24) % span) - sp, 0, side);\n"
         "    uint minx = uint(min(x0, min(x1, x2))); uint tm = t | (mode << 8) | (minx << 16);\n"
         "    int xs[3] = int[3](x0, x1, x2), ys[3] = int[3](y0, y1, y2);\n"
         "    for (uint k = 0u; k < 3u; ++k) { uint vi = ((t * n + i) * 3u + k) * 5u; vb[vi] = floatBitsToUint((float(xs[k]) + ox) * sx - 1.0); vb[vi + 1u] = floatBitsToUint((float(ys[k]) + oy) * sy - 1.0); vb[vi + 2u] = colour; vb[vi + 3u] = tm; vb[vi + 4u] = i; }\n"
      << (d.fcap ?
         "    { int area = (x1 - x0) * (y2 - y0) - (y1 - y0) * (x2 - x0);\n"
         "      if (area != 0) { int sg = area > 0 ? 1 : -1; int ex[3] = int[3](x0, x1, x2), ey[3] = int[3](y0, y1, y2); bool tl[3];\n"
         "        for (int k = 0; k < 3; ++k) { int dx = ex[(k + 1) % 3] - ex[k], dy = ey[(k + 1) % 3] - ey[k]; tl[k] = (dy == 0) ? ((dx > 0 ? 1 : -1) == sg) : ((-dy > 0 ? 1 : -1) == sg); }\n"
         "        int bminx = min(x0, min(x1, x2)), bmaxx = max(x0, max(x1, x2)), bminy = min(y0, min(y1, y2)), bmaxy = max(y0, max(y1, y2));\n"
         "        for (int jj = bminy; jj < bmaxy; ++jj) { uint xl = 0xFFFFu, cnt = 0u; int py = 2 * jj + 1;\n"
         "          for (int ii = bminx; ii < bmaxx; ++ii) { int px = 2 * ii + 1; bool inn = true;\n"
         "            for (int k = 0; k < 3 && inn; ++k) { int w = ((2 * ex[(k + 1) % 3] - 2 * ex[k]) * (py - 2 * ey[k]) - (2 * ey[(k + 1) % 3] - 2 * ey[k]) * (px - 2 * ex[k])) * sg; if (w < 0 || (w == 0 && !tl[k])) inn = false; }\n"
         "            if (!inn) continue; if (xl == 0xFFFFu) xl = uint(ii); if (ncov + cnt < " + std::to_string(d.fcap) + "u) mask[jj] |= 1u << uint(ii); ++cnt; }\n"
         "          sc[tb + t * " + std::to_string(d.side) + "u + uint(jj)] = ncov | (xl << 16); ncov += cnt; } } }\n" : "")
      << "  }\n"
      << (d.fcap ?
         "  for (uint k = 0u; k < " + std::to_string(d.side) + "u; ++k) sc[tb + " + std::to_string(d.T * d.side) + "u + k] = mask[k]; sc[tb + " + std::to_string(d.T * d.side + d.side) + "u] = ncov;\n"
         "  { float fx0 = ox * sx - 1.0, fx1 = (ox + " + std::to_string(d.side) + ".0) * sx - 1.0, fy0 = oy * sy - 1.0, fy1 = (oy + " + std::to_string(d.side) + ".0) * sy - 1.0; float qx[6] = float[6](fx0, fx1, fx0, fx1, fx1, fx0), qy[6] = float[6](fy0, fy0, fy1, fy0, fy1, fy1);\n"
         "    for (uint k = 0u; k < 6u; ++k) { uint vi = (" + std::to_string(d.T) + "u * n * 3u + i * 6u + k) * 5u; vb[vi] = floatBitsToUint(qx[k]); vb[vi + 1u] = floatBitsToUint(qy[k]); vb[vi + 2u] = 0u; vb[vi + 3u] = 0x80000000u; vb[vi + 4u] = i; } }\n" : "")
      << "}\n";
    return o.str();
}
static std::string glsl_digest_pass(const Doc& d) {
    std::ostringstream o;
    const uint32_t nwords = 29 + d.side * d.side, nbytes = nwords * 4, nchunks = (nbytes + 1023) / 1024;
    o << "#version 450\nlayout(local_size_x = 64) in;\n"
         "layout(std430, set=0, binding=0) readonly buffer Hdr { uint hd[]; };\nlayout(set=0, binding=3, r32ui) uniform readonly uimage2D target;\nlayout(std430, set=0, binding=4) buffer Out { uint ob[]; };\n"
      << kGlslBlake3
      << "uint S[8]; uint ph[8];\n"
      << "uint mixword(uint i, uint ox, uint oy) { if (i < 13u) return hd[40u + i]; if (i < 21u) return S[i - 13u]; if (i < 29u) return ph[i - 21u]; uint p = i - 29u; return imageLoad(target, ivec2(int(ox + (p & " << (d.side - 1) << "u)), int(oy + (p >> " << d.p.registers << "u)))).x; }\n"
      << "void main() { uint i = gl_GlobalInvocationID.x, n = hd[78]; if (i >= n) return;\n"
      << "  for (uint k = 0u; k < 8u; ++k) { S[k] = ob[i * 24u + k]; ph[k] = hd[53u + k]; }\n"
      << "  uint ox = " << d.side << "u * (i % 32u), oy = " << d.side << "u * (i / 32u);\n"
      << "  uint cvs[" << nchunks << "][8];\n"
      << "  for (uint c = 0u; c < " << nchunks << "u; ++c) {\n"
      << "    uint cbytes = min(1024u, " << nbytes << "u - c * 1024u); uint nb = (cbytes + 63u) / 64u;\n"
         "    uint cv[8]; cv[0] = IV0; cv[1] = IV1; cv[2] = IV2; cv[3] = IV3; cv[4] = IV4; cv[5] = IV5; cv[6] = IV6; cv[7] = IV7;\n"
         "    for (uint b = 0u; b < nb; ++b) { uint m[16]; uint blen = (b + 1u < nb) ? 64u : (cbytes - b * 64u);\n"
         "      for (uint k = 0u; k < 16u; ++k) { uint wi = c * 256u + b * 16u + k; m[k] = (k * 4u < blen) ? mixword(wi, ox, oy) : 0u; }\n"
         "      uint fl = (b == 0u ? B3_CHUNK_START : 0u) | (b + 1u == nb ? B3_CHUNK_END : 0u);\n"
      << (nchunks == 1 ? "      if (b + 1u == nb) fl |= B3_ROOT;\n" : "")
      << "      uint o[16]; b3_compress(cv, m, c, blen, fl, o); for (uint k = 0u; k < 8u; ++k) cv[k] = o[k]; }\n"
         "    for (uint k = 0u; k < 8u; ++k) cvs[c][k] = cv[k]; }\n";
    {
        std::function<std::string(uint32_t, uint32_t, bool, int&)> fold = [&](uint32_t lo, uint32_t hi, bool root, int& tmp) -> std::string {
            if (hi - lo == 1) { if (root) { o << "  uint M[8]; for (uint k = 0u; k < 8u; ++k) M[k] = cvs[" << lo << "][k];\n"; return "M"; } return "cvs[" + std::to_string(lo) + "]"; }
            uint32_t n = hi - lo, left = 1; while (left * 2 < n) left *= 2;
            std::string l = fold(lo, lo + left, false, tmp), r = fold(lo + left, hi, false, tmp);
            std::string name = root ? "M" : ("t" + std::to_string(tmp++));
            o << "  uint " << name << "[8]; b3_parent(" << l << ", " << r << ", " << (root ? "B3_ROOT" : "0u") << ", " << name << ");\n";
            return name;
        };
        int tmp = 0; fold(0, nchunks, true, tmp);
    }
    // the final digest: prefix 65 bytes, S at 65, M at 97: 129 bytes = three blocks of one chunk, each block's words assembled directly (no intermediate array)
    o << "  uint cv[8]; cv[0] = IV0; cv[1] = IV1; cv[2] = IV2; cv[3] = IV3; cv[4] = IV4; cv[5] = IV5; cv[6] = IV6; cv[7] = IV7; uint m[16]; uint o16[16];\n"
         "  for (uint k = 0u; k < 16u; ++k) m[k] = hd[61u + k]; b3_compress(cv, m, 0u, 64u, B3_CHUNK_START, o16); for (uint k = 0u; k < 8u; ++k) cv[k] = o16[k];\n"
         "  m[0] = (hd[77] & 255u) | (S[0] << 8); for (uint j = 1u; j < 8u; ++j) m[j] = (S[j - 1u] >> 24) | (S[j] << 8); m[8] = (S[7] >> 24) | (M[0] << 8); for (uint j = 1u; j < 8u; ++j) m[8u + j] = (M[j - 1u] >> 24) | (M[j] << 8);\n"
         "  b3_compress(cv, m, 0u, 64u, 0u, o16); for (uint k = 0u; k < 8u; ++k) cv[k] = o16[k];\n"
         "  for (uint k = 0u; k < 16u; ++k) m[k] = 0u; m[0] = M[7] >> 24; b3_compress(cv, m, 0u, 1u, B3_CHUNK_END | B3_ROOT, o16); uint F[8]; for (uint k = 0u; k < 8u; ++k) F[k] = o16[k];\n"
         "  for (uint k = 0u; k < 8u; ++k) { ob[i * 24u + 8u + k] = M[k]; ob[i * 24u + 16u + k] = F[k]; }\n}\n";
    return o.str();
}

// ---- Vulkan ----
struct Vk {
    VkInstance inst; VkPhysicalDevice phys; VkDevice dev; VkQueue queue; uint32_t qfam; VkPhysicalDeviceMemoryProperties mem; std::string name;
    VkCommandPool pool; double ts_period_ns = 1.0; VkDescriptorSetLayout dsl; VkPipelineLayout layout; VkDescriptorPool dpool; VkDescriptorSet dset; VkRenderPass rp; VkPipelineCache cache; VkShaderModule vs;
    VkImage dimg = VK_NULL_HANDLE; VkDeviceMemory dmem; VkImageView dview; VkSampler sampler;
    VkDescriptorSetLayout kdsl; VkPipelineLayout klayout; VkPipeline scene_pipe = VK_NULL_HANDLE, digest_pipe = VK_NULL_HANDLE; VkDescriptorPool kdpool; VkDescriptorSet kdset[2];
    VkBuffer dbuf = VK_NULL_HANDLE; VkDeviceMemory dbmem; VkDescriptorSetLayout cdsl; VkPipelineLayout clayout; VkPipeline cpipe = VK_NULL_HANDLE; VkDescriptorPool cdpool; VkDescriptorSet cdset[2];
    uint32_t find_mem(uint32_t bits, VkMemoryPropertyFlags want) { for (uint32_t i = 0; i < mem.memoryTypeCount; ++i) if ((bits & (1u << i)) && (mem.memoryTypes[i].propertyFlags & want) == want) return i; std::fprintf(stderr, "no memory type\n"); std::exit(2); }
    void buffer(VkDeviceSize sz, VkBufferUsageFlags use, VkMemoryPropertyFlags props, VkBuffer& b, VkDeviceMemory& m, void** map) {
        VkBufferCreateInfo bi{VK_STRUCTURE_TYPE_BUFFER_CREATE_INFO}; bi.size = sz; bi.usage = use; bi.sharingMode = VK_SHARING_MODE_EXCLUSIVE; VK_CHECK(vkCreateBuffer(dev, &bi, nullptr, &b));
        VkMemoryRequirements mr; vkGetBufferMemoryRequirements(dev, b, &mr); VkMemoryAllocateInfo ai{VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO}; ai.allocationSize = mr.size; ai.memoryTypeIndex = find_mem(mr.memoryTypeBits, props); VK_CHECK(vkAllocateMemory(dev, &ai, nullptr, &m)); VK_CHECK(vkBindBufferMemory(dev, b, m, 0));
        if (map) VK_CHECK(vkMapMemory(dev, m, 0, sz, 0, map));
    }
    VkCommandBuffer begin_once() { VkCommandBufferAllocateInfo ai{VK_STRUCTURE_TYPE_COMMAND_BUFFER_ALLOCATE_INFO}; ai.commandPool = pool; ai.level = VK_COMMAND_BUFFER_LEVEL_PRIMARY; ai.commandBufferCount = 1; VkCommandBuffer cb; VK_CHECK(vkAllocateCommandBuffers(dev, &ai, &cb)); VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO}; bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT; VK_CHECK(vkBeginCommandBuffer(cb, &bi)); return cb; }
    void end_once(VkCommandBuffer cb) { VK_CHECK(vkEndCommandBuffer(cb)); VkSubmitInfo si{VK_STRUCTURE_TYPE_SUBMIT_INFO}; si.commandBufferCount = 1; si.pCommandBuffers = &cb; VK_CHECK(vkQueueSubmit(queue, 1, &si, VK_NULL_HANDLE)); VK_CHECK(vkQueueWaitIdle(queue)); vkFreeCommandBuffers(dev, pool, 1, &cb); }
};

static void vk_init(Vk& v, int device_index, bool copy_path) {
    VkApplicationInfo app{VK_STRUCTURE_TYPE_APPLICATION_INFO}; app.pApplicationName = "igneum-kiln"; app.apiVersion = VK_API_VERSION_1_2;
    VkInstanceCreateInfo ici{VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO}; ici.pApplicationInfo = &app; VK_CHECK(vkCreateInstance(&ici, nullptr, &v.inst));
    uint32_t n = 0; VK_CHECK(vkEnumeratePhysicalDevices(v.inst, &n, nullptr)); if (!n) { std::fprintf(stderr, "no vulkan device\n"); std::exit(2); }
    std::vector<VkPhysicalDevice> pd(n); VK_CHECK(vkEnumeratePhysicalDevices(v.inst, &n, pd.data()));
    v.phys = pd[std::min<uint32_t>(uint32_t(device_index), n - 1)];
    VkPhysicalDeviceProperties pr; vkGetPhysicalDeviceProperties(v.phys, &pr); v.name = pr.deviceName;
    char drv[64]; std::snprintf(drv, sizeof drv, " (driver %u.%u.%u, max image %u)", VK_VERSION_MAJOR(pr.driverVersion), VK_VERSION_MINOR(pr.driverVersion) >> 4, VK_VERSION_PATCH(pr.driverVersion), pr.limits.maxImageDimension2D); v.name += drv;
    v.ts_period_ns = pr.limits.timestampPeriod;
    if (pr.limits.maxImageDimension2D < 16384) { std::fprintf(stderr, "max image dimension %u under 16384\n", pr.limits.maxImageDimension2D); std::exit(2); }
    uint32_t qn = 0; vkGetPhysicalDeviceQueueFamilyProperties(v.phys, &qn, nullptr); std::vector<VkQueueFamilyProperties> qp(qn); vkGetPhysicalDeviceQueueFamilyProperties(v.phys, &qn, qp.data());
    v.qfam = ~0u; for (uint32_t i = 0; i < qn; ++i) if (qp[i].queueFlags & VK_QUEUE_GRAPHICS_BIT) { v.qfam = i; break; }
    if (v.qfam == ~0u) { std::fprintf(stderr, "no graphics queue\n"); std::exit(2); }
    const float prio = 1.0f; VkDeviceQueueCreateInfo qci{VK_STRUCTURE_TYPE_DEVICE_QUEUE_CREATE_INFO}; qci.queueFamilyIndex = v.qfam; qci.queueCount = 1; qci.pQueuePriorities = &prio;
    VkDeviceCreateInfo dci{VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO}; dci.queueCreateInfoCount = 1; dci.pQueueCreateInfos = &qci; VK_CHECK(vkCreateDevice(v.phys, &dci, nullptr, &v.dev));
    vkGetDeviceQueue(v.dev, v.qfam, 0, &v.queue); vkGetPhysicalDeviceMemoryProperties(v.phys, &v.mem);
    VkCommandPoolCreateInfo pci{VK_STRUCTURE_TYPE_COMMAND_POOL_CREATE_INFO}; pci.queueFamilyIndex = v.qfam; pci.flags = VK_COMMAND_POOL_CREATE_RESET_COMMAND_BUFFER_BIT; VK_CHECK(vkCreateCommandPool(v.dev, &pci, nullptr, &v.pool));
    VkDescriptorSetLayoutBinding b[5]{}; b[4].binding = 4; b[4].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; b[4].descriptorCount = 1; b[4].stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT; b[3].binding = 3; b[3].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; b[3].descriptorCount = 1; b[3].stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT; b[2].binding = 2; b[2].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_IMAGE; b[2].descriptorCount = 1; b[2].stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT; b[0].binding = 0; b[0].descriptorType = copy_path ? VK_DESCRIPTOR_TYPE_STORAGE_BUFFER : VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER; b[0].descriptorCount = 1; b[0].stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT; b[1].binding = 1; b[1].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; b[1].descriptorCount = 1; b[1].stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT;
    VkDescriptorSetLayoutCreateInfo dl{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO}; dl.bindingCount = 5; dl.pBindings = b; VK_CHECK(vkCreateDescriptorSetLayout(v.dev, &dl, nullptr, &v.dsl));
    VkPipelineLayoutCreateInfo pl{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO}; pl.setLayoutCount = 1; pl.pSetLayouts = &v.dsl; VK_CHECK(vkCreatePipelineLayout(v.dev, &pl, nullptr, &v.layout));
    VkDescriptorPoolSize ps[3]{{VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER, 1}, {VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 4}, {VK_DESCRIPTOR_TYPE_STORAGE_IMAGE, 1}}; VkDescriptorPoolCreateInfo dp{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO}; dp.maxSets = 1; dp.poolSizeCount = 3; dp.pPoolSizes = ps; VK_CHECK(vkCreateDescriptorPool(v.dev, &dp, nullptr, &v.dpool));
    VkDescriptorSetAllocateInfo da{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO}; da.descriptorPool = v.dpool; da.descriptorSetCount = 1; da.pSetLayouts = &v.dsl; VK_CHECK(vkAllocateDescriptorSets(v.dev, &da, &v.dset));
    // the render pass has no attachments: the target is a storage image the fragment shader loads, blends in integers and stores (one pass instance a triangle slot, a barrier between)
    VkSubpassDescription sp{}; sp.pipelineBindPoint = VK_PIPELINE_BIND_POINT_GRAPHICS;
    VkRenderPassCreateInfo rpi{VK_STRUCTURE_TYPE_RENDER_PASS_CREATE_INFO}; rpi.subpassCount = 1; rpi.pSubpasses = &sp; VK_CHECK(vkCreateRenderPass(v.dev, &rpi, nullptr, &v.rp));
    if (copy_path) {
        VkDescriptorSetLayoutBinding cbnd[3]{}; for (uint32_t i = 0; i < 3; ++i) { cbnd[i].binding = i; cbnd[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; cbnd[i].descriptorCount = 1; cbnd[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT; }
        VkDescriptorSetLayoutCreateInfo cdl{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO}; cdl.bindingCount = 3; cdl.pBindings = cbnd; VK_CHECK(vkCreateDescriptorSetLayout(v.dev, &cdl, nullptr, &v.cdsl));
        VkPipelineLayoutCreateInfo cpl{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO}; cpl.setLayoutCount = 1; cpl.pSetLayouts = &v.cdsl; VK_CHECK(vkCreatePipelineLayout(v.dev, &cpl, nullptr, &v.clayout));
        VkDescriptorPoolSize cps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 6}; VkDescriptorPoolCreateInfo cdp{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO}; cdp.maxSets = 2; cdp.poolSizeCount = 1; cdp.pPoolSizes = &cps; VK_CHECK(vkCreateDescriptorPool(v.dev, &cdp, nullptr, &v.cdpool));
        VkDescriptorSetLayout two[2] = {v.cdsl, v.cdsl}; VkDescriptorSetAllocateInfo cda{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO}; cda.descriptorPool = v.cdpool; cda.descriptorSetCount = 2; cda.pSetLayouts = two; VK_CHECK(vkAllocateDescriptorSets(v.dev, &cda, v.cdset));
    }
    {   // the card's host-work set: 0 the frame header, 1 the scenes, 2 the vertices, 3 the target image, 4 the output (S, M, F a hash)
        VkDescriptorSetLayoutBinding kb[5]{}; for (uint32_t i = 0; i < 5; ++i) { kb[i].binding = i; kb[i].descriptorType = i == 3 ? VK_DESCRIPTOR_TYPE_STORAGE_IMAGE : VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; kb[i].descriptorCount = 1; kb[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT; }
        VkDescriptorSetLayoutCreateInfo kdl{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO}; kdl.bindingCount = 5; kdl.pBindings = kb; VK_CHECK(vkCreateDescriptorSetLayout(v.dev, &kdl, nullptr, &v.kdsl));
        VkPipelineLayoutCreateInfo kpl{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO}; kpl.setLayoutCount = 1; kpl.pSetLayouts = &v.kdsl; VK_CHECK(vkCreatePipelineLayout(v.dev, &kpl, nullptr, &v.klayout));
        VkDescriptorPoolSize kps[2]{{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 8}, {VK_DESCRIPTOR_TYPE_STORAGE_IMAGE, 2}}; VkDescriptorPoolCreateInfo kdp{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO}; kdp.maxSets = 2; kdp.poolSizeCount = 2; kdp.pPoolSizes = kps; VK_CHECK(vkCreateDescriptorPool(v.dev, &kdp, nullptr, &v.kdpool));
        VkDescriptorSetLayout two[2] = {v.kdsl, v.kdsl}; VkDescriptorSetAllocateInfo kda{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO}; kda.descriptorPool = v.kdpool; kda.descriptorSetCount = 2; kda.pSetLayouts = two; VK_CHECK(vkAllocateDescriptorSets(v.dev, &kda, v.kdset));
    }
    VkPipelineCacheCreateInfo pc{VK_STRUCTURE_TYPE_PIPELINE_CACHE_CREATE_INFO}; VK_CHECK(vkCreatePipelineCache(v.dev, &pc, nullptr, &v.cache));
    VkSamplerCreateInfo sci{VK_STRUCTURE_TYPE_SAMPLER_CREATE_INFO}; sci.magFilter = VK_FILTER_NEAREST; sci.minFilter = VK_FILTER_NEAREST; sci.mipmapMode = VK_SAMPLER_MIPMAP_MODE_NEAREST; sci.addressModeU = sci.addressModeV = sci.addressModeW = VK_SAMPLER_ADDRESS_MODE_CLAMP_TO_EDGE; sci.unnormalizedCoordinates = VK_TRUE; sci.maxLod = 0.0f; VK_CHECK(vkCreateSampler(v.dev, &sci, nullptr, &v.sampler));
}

static VkShaderModule compile(Vk& v, shaderc_compiler_t sc, const std::string& src, shaderc_shader_kind kind, const char* name) {
    shaderc_compile_options_t opt = shaderc_compile_options_initialize(); shaderc_compile_options_set_optimization_level(opt, shaderc_optimization_level_performance); shaderc_compile_options_set_target_env(opt, shaderc_target_env_vulkan, shaderc_env_version_vulkan_1_2);
    shaderc_compilation_result_t res = shaderc_compile_into_spv(sc, src.c_str(), src.size(), kind, name, "main", opt);
    if (shaderc_result_get_compilation_status(res) != shaderc_compilation_status_success) { std::fprintf(stderr, "glsl: %s\n%s\n", shaderc_result_get_error_message(res), src.c_str()); std::exit(2); }
    VkShaderModuleCreateInfo mi{VK_STRUCTURE_TYPE_SHADER_MODULE_CREATE_INFO}; mi.codeSize = shaderc_result_get_length(res); mi.pCode = reinterpret_cast<const uint32_t*>(shaderc_result_get_bytes(res));
    VkShaderModule m; VK_CHECK(vkCreateShaderModule(v.dev, &mi, nullptr, &m)); shaderc_result_release(res); shaderc_compile_options_release(opt); return m;
}

struct Pipes { VkShaderModule fs = VK_NULL_HANDLE; VkPipeline p = VK_NULL_HANDLE; double compile_ms = 0; Batch batch; };
static void build_pipes(Vk& v, shaderc_compiler_t sc, const Doc& d, uint32_t W, uint32_t H, Pipes& out) {
    const auto t0 = std::chrono::steady_clock::now();
    out.fs = compile(v, sc, glsl_fragment(d, out.batch), shaderc_glsl_fragment_shader, "kiln.frag");
    VkPipelineShaderStageCreateInfo st[2]{}; st[0].sType = st[1].sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO; st[0].stage = VK_SHADER_STAGE_VERTEX_BIT; st[0].module = v.vs; st[0].pName = "main"; st[1].stage = VK_SHADER_STAGE_FRAGMENT_BIT; st[1].module = out.fs; st[1].pName = "main";
    VkVertexInputBindingDescription vb{0, 20, VK_VERTEX_INPUT_RATE_VERTEX};
    VkVertexInputAttributeDescription va[4]{{0, 0, VK_FORMAT_R32G32_SFLOAT, 0}, {1, 0, VK_FORMAT_R32_UINT, 8}, {2, 0, VK_FORMAT_R32_UINT, 12}, {3, 0, VK_FORMAT_R32_UINT, 16}};
    VkPipelineVertexInputStateCreateInfo vi{VK_STRUCTURE_TYPE_PIPELINE_VERTEX_INPUT_STATE_CREATE_INFO}; vi.vertexBindingDescriptionCount = 1; vi.pVertexBindingDescriptions = &vb; vi.vertexAttributeDescriptionCount = 4; vi.pVertexAttributeDescriptions = va;
    VkPipelineInputAssemblyStateCreateInfo ia{VK_STRUCTURE_TYPE_PIPELINE_INPUT_ASSEMBLY_STATE_CREATE_INFO}; ia.topology = VK_PRIMITIVE_TOPOLOGY_TRIANGLE_LIST;
    VkViewport vp{0.0f, 0.0f, float(W), float(H), 0.0f, 1.0f}; VkRect2D scis{{0, 0}, {W, H}};
    VkPipelineViewportStateCreateInfo vps{VK_STRUCTURE_TYPE_PIPELINE_VIEWPORT_STATE_CREATE_INFO}; vps.viewportCount = 1; vps.pViewports = &vp; vps.scissorCount = 1; vps.pScissors = &scis;
    VkPipelineRasterizationStateCreateInfo rs{VK_STRUCTURE_TYPE_PIPELINE_RASTERIZATION_STATE_CREATE_INFO}; rs.polygonMode = VK_POLYGON_MODE_FILL; rs.cullMode = VK_CULL_MODE_NONE; rs.frontFace = VK_FRONT_FACE_COUNTER_CLOCKWISE; rs.lineWidth = 1.0f;
    VkPipelineMultisampleStateCreateInfo ms{VK_STRUCTURE_TYPE_PIPELINE_MULTISAMPLE_STATE_CREATE_INFO}; ms.rasterizationSamples = VK_SAMPLE_COUNT_1_BIT;
    VkPipelineColorBlendStateCreateInfo cb{VK_STRUCTURE_TYPE_PIPELINE_COLOR_BLEND_STATE_CREATE_INFO};
    VkGraphicsPipelineCreateInfo gp{VK_STRUCTURE_TYPE_GRAPHICS_PIPELINE_CREATE_INFO}; gp.stageCount = 2; gp.pStages = st; gp.pVertexInputState = &vi; gp.pInputAssemblyState = &ia; gp.pViewportState = &vps; gp.pRasterizationState = &rs; gp.pMultisampleState = &ms; gp.pColorBlendState = &cb; gp.layout = v.layout; gp.renderPass = v.rp;
    VK_CHECK(vkCreateGraphicsPipelines(v.dev, v.cache, 1, &gp, nullptr, &out.p));
    out.compile_ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
}
static void free_pipes(Vk& v, Pipes& p) { if (p.p) vkDestroyPipeline(v.dev, p.p, nullptr); if (p.fs) vkDestroyShaderModule(v.dev, p.fs, nullptr); p = Pipes{}; }

// ---- a frame: K hash slots on one target ----
struct Vtx { float x, y; uint32_t c, t, h; };
struct Frame {
    VkImage img; VkDeviceMemory imem; VkImageView view; VkFramebuffer fb; VkBuffer vbuf, rbuf, sbuf, tbuf = VK_NULL_HANDLE, hbuf, obuf; VkDeviceMemory vmem, rmem, smem, tmem, hmem, omem; void* hmap; void* omap; uint32_t cmp_slot = 0; void* vmap; void* rmap; void* smap; VkCommandBuffer cb; VkFence fence; VkQueryPool qp; double gpu_ms = 0;
    std::vector<HashJob> jobs; uint32_t n = 0; Batch batch; const void* pipes = nullptr; uint32_t draws[65][2]; uint32_t ndraws = 0; bool pending = false;
};
static void frame_init(Vk& v, const Doc& d, uint32_t K, uint32_t W, uint32_t H, Frame& f) {
    VkImageCreateInfo ii{VK_STRUCTURE_TYPE_IMAGE_CREATE_INFO}; ii.imageType = VK_IMAGE_TYPE_2D; ii.format = VK_FORMAT_R32_UINT; ii.extent = {W, H, 1}; ii.mipLevels = 1; ii.arrayLayers = 1; ii.samples = VK_SAMPLE_COUNT_1_BIT; ii.tiling = VK_IMAGE_TILING_OPTIMAL; ii.usage = VK_IMAGE_USAGE_STORAGE_BIT | VK_IMAGE_USAGE_TRANSFER_SRC_BIT | VK_IMAGE_USAGE_TRANSFER_DST_BIT; ii.initialLayout = VK_IMAGE_LAYOUT_UNDEFINED;
    VK_CHECK(vkCreateImage(v.dev, &ii, nullptr, &f.img)); VkMemoryRequirements mr; vkGetImageMemoryRequirements(v.dev, f.img, &mr); VkMemoryAllocateInfo ai{VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO}; ai.allocationSize = mr.size; ai.memoryTypeIndex = v.find_mem(mr.memoryTypeBits, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT); VK_CHECK(vkAllocateMemory(v.dev, &ai, nullptr, &f.imem)); VK_CHECK(vkBindImageMemory(v.dev, f.img, f.imem, 0));
    VkImageViewCreateInfo vi{VK_STRUCTURE_TYPE_IMAGE_VIEW_CREATE_INFO}; vi.image = f.img; vi.viewType = VK_IMAGE_VIEW_TYPE_2D; vi.format = VK_FORMAT_R32_UINT; vi.subresourceRange = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; VK_CHECK(vkCreateImageView(v.dev, &vi, nullptr, &f.view));
    { VkCommandBuffer cb = v.begin_once(); VkImageMemoryBarrier b{VK_STRUCTURE_TYPE_IMAGE_MEMORY_BARRIER}; b.oldLayout = VK_IMAGE_LAYOUT_UNDEFINED; b.newLayout = VK_IMAGE_LAYOUT_GENERAL; b.image = f.img; b.subresourceRange = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; vkCmdPipelineBarrier(cb, VK_PIPELINE_STAGE_TOP_OF_PIPE_BIT, VK_PIPELINE_STAGE_ALL_COMMANDS_BIT, 0, 0, nullptr, 0, nullptr, 1, &b); v.end_once(cb); }
    VkFramebufferCreateInfo fi{VK_STRUCTURE_TYPE_FRAMEBUFFER_CREATE_INFO}; fi.renderPass = v.rp; fi.attachmentCount = 0; fi.width = W; fi.height = H; fi.layers = 1; VK_CHECK(vkCreateFramebuffer(v.dev, &fi, nullptr, &f.fb));
    v.buffer(VkDeviceSize(K) * (d.T * 3 + 6) * sizeof(Vtx), VK_BUFFER_USAGE_VERTEX_BUFFER_BIT | VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, f.vbuf, f.vmem, nullptr); f.vmap = nullptr;
    v.buffer(VkDeviceSize(d.side) * d.side * 4, VK_BUFFER_USAGE_TRANSFER_DST_BIT, VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT | VK_MEMORY_PROPERTY_HOST_CACHED_BIT, f.rbuf, f.rmem, &f.rmap);
    v.buffer(VkDeviceSize(K) * d.scn * 4, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, f.sbuf, f.smem, nullptr); f.smap = nullptr;
    v.buffer(128 * 4, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT, f.hbuf, f.hmem, &f.hmap);
    v.buffer(VkDeviceSize(K) * 24 * 4, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT | VK_MEMORY_PROPERTY_HOST_CACHED_BIT, f.obuf, f.omem, &f.omap);
    VkCommandBufferAllocateInfo ca{VK_STRUCTURE_TYPE_COMMAND_BUFFER_ALLOCATE_INFO}; ca.commandPool = v.pool; ca.level = VK_COMMAND_BUFFER_LEVEL_PRIMARY; ca.commandBufferCount = 1; VK_CHECK(vkAllocateCommandBuffers(v.dev, &ca, &f.cb));
    VkFenceCreateInfo fc{VK_STRUCTURE_TYPE_FENCE_CREATE_INFO}; VK_CHECK(vkCreateFence(v.dev, &fc, nullptr, &f.fence));
    VkQueryPoolCreateInfo qi{VK_STRUCTURE_TYPE_QUERY_POOL_CREATE_INFO}; qi.queryType = VK_QUERY_TYPE_TIMESTAMP; qi.queryCount = 2; VK_CHECK(vkCreateQueryPool(v.dev, &qi, nullptr, &f.qp));
    f.jobs.resize(K);
}

// ---- the thread pool ----
struct Pool {
    std::vector<std::thread> th; std::mutex mu; std::condition_variable cv; std::vector<std::function<void()>> q; bool stop = false;
    explicit Pool(unsigned n) { for (unsigned i = 0; i < n; ++i) th.emplace_back([this] { for (;;) { std::function<void()> f; { std::unique_lock<std::mutex> l(mu); cv.wait(l, [&] { return stop || !q.empty(); }); if (stop && q.empty()) return; f = std::move(q.back()); q.pop_back(); } f(); } }); }
    ~Pool() { { std::lock_guard<std::mutex> l(mu); stop = true; } cv.notify_all(); for (auto& t : th) t.join(); }
    void run(uint32_t n, const std::function<void(uint32_t, uint32_t)>& f) {  // f(begin, end) over n items in chunks
        const uint32_t chunks = uint32_t(th.size()) * 4, per = (n + chunks - 1) / chunks; std::atomic<uint32_t> done{0}; uint32_t issued = 0; std::mutex dm; std::condition_variable dc;
        for (uint32_t b = 0; b < n; b += per) { const uint32_t e = std::min(n, b + per); ++issued; { std::lock_guard<std::mutex> l(mu); q.push_back([&, b, e] { f(b, e); { std::lock_guard<std::mutex> l2(dm); ++done; } dc.notify_one(); }); } cv.notify_one(); }
        std::unique_lock<std::mutex> l(dm); dc.wait(l, [&] { return done == issued; });
    }
};

int main(int argc, char** argv) {
    std::string doc_path, set_path, header_hex; double seconds = 60.0; uint32_t K = 1024; uint64_t height = 1; int device = 0; unsigned threads = std::max(1u, std::thread::hardware_concurrency()); uint32_t ahead = 8; uint64_t nonce0 = 0; bool dump_glsl = false, copy_path = false;
    for (int i = 1; i < argc; ++i) { std::string a = argv[i]; auto nx = [&] { return std::string(argv[++i]); };
        if (a == "--doc") doc_path = nx(); else if (a == "--set") set_path = nx(); else if (a == "--seconds") seconds = std::stod(nx()); else if (a == "--k") K = uint32_t(std::stoul(nx())); else if (a == "--height") height = std::stoull(nx()); else if (a == "--header") header_hex = nx(); else if (a == "--device") device = std::stoi(nx()); else if (a == "--threads") threads = unsigned(std::stoul(nx())); else if (a == "--ahead") ahead = uint32_t(std::stoul(nx())); else if (a == "--nonce0") nonce0 = std::stoull(nx()); else if (a == "--dump-glsl") dump_glsl = true; else if (a == "--tile-path") { const std::string t = nx(); if (t == "copy") copy_path = true; else if (t != "fetch") { std::fprintf(stderr, "--tile-path fetch|copy\n"); return 2; } }
        else { std::fprintf(stderr, "kiln_vk --doc DOC.json [--set unseen.json] [--seconds 60] [--k 1024] [--height 1] [--header hex] [--device 0] [--threads n] [--ahead 2]\n"); return 2; } }
    if (doc_path.empty()) { std::fprintf(stderr, "--doc required\n"); return 2; }
    Doc d; { std::ifstream f(doc_path); std::stringstream ss; ss << f.rdbuf(); d.json = ss.str(); }
    if (ignh_params_parse(d.json.c_str(), &d.p) != IGNPOW_OK || d.p.version != 22) { std::fprintf(stderr, "the document is not a version 22 document\n"); return 2; }
    ignh_params_hash(&d.p, d.P);
    d.T = d.p.rounds; d.words = d.p.program_ops; d.side = 1u << d.p.registers; d.batch_log2 = d.p.keyed_rounds; d.items = d.p.dataset_bytes / d.p.item_bytes; d.n_tiles = d.items / 64;
    d.tiles = d.p.fold_every ? d.p.fold_every : 1; d.rows = d.p.blocks; d.spread = d.p.scratch_bytes; d.fcap = d.p.table_kib; d.coal = (d.p.access_bytes & 1u) != 0; d.contig = (d.p.access_bytes >> 8) ? (d.p.access_bytes >> 8) : 1u;   // "fragments" in table_kib, "coalesced" in access_bytes bit 0, "contig" in its high bits (the 96-byte canonical form, main 328f930)
    d.scn = 8 + d.tiles + (d.fcap ? d.T * d.side + d.side + 1 : 0);   // the scene words a hash: s0..s7, the tile indices, then (exact count) the per-row table, the coverage masks, the covered total
    if (d.fcap && d.side > 32) { std::fprintf(stderr, "the exact-count form holds a 32-wide target at most in this port\n"); return 2; }   // the parser keeps "tiles" in fold_every and "rows" in blocks
    if (d.tiles < 1 || d.tiles > 64 || d.T > 64 || K % 32 || (K & (K - 1)) != 0) { std::fprintf(stderr, "tiles 1 to 64, T to 64, K a power of two and a multiple of 32\n"); return 2; }
    if (K == 32) K = 32; const uint32_t COLS = 32, W = COLS * d.side, H = (K / COLS) * d.side;

    // the contexts: the light one for E and the reference hash, the inner full one for the dataset bytes
    int st = 0; auto tc0 = std::chrono::steady_clock::now();
    ignpow_ctx* light = nullptr; void* inner = nullptr; uint64_t dbytes = 0; const uint8_t* dataset = nullptr; const uint32_t* dwords = nullptr; double dataset_s = 0, upload_s = 0; uint64_t loaded_epoch = ~0ull;
    auto load_contexts = [&](uint64_t h) {  // the light context (E, the reference hash) and the inner full one (the dataset bytes) for h's epoch
        if (light) ignpow_ctx_free(light); if (inner) ignh_inner_destroy(inner); tc0 = std::chrono::steady_clock::now();
        light = ignpow_ctx_create(IGNPOW_ALGO_IGNEUMHASH, d.json.c_str(), h, 0, &st); if (!light) { std::fprintf(stderr, "light context: %d\n", st); std::exit(2); }
        { uint8_t D[32], cd[32], dg[32]; uint64_t bm; int hd; if (ignh_dataset_info(light, d.E, D, cd, dg, &bm, &hd) != IGNPOW_OK) { std::fprintf(stderr, "dataset info\n"); std::exit(2); } }
        inner = ignh_inner_create(d.json.c_str(), h, 1, &st); if (!inner) { std::fprintf(stderr, "full context: %d\n", st); std::exit(2); }
        dataset = ignh_inner_dataset(inner, &dbytes); if (!dataset || dbytes != d.p.dataset_bytes) { std::fprintf(stderr, "dataset bytes %llu\n", (unsigned long long)dbytes); std::exit(2); }
        dataset_s = std::chrono::duration<double>(std::chrono::steady_clock::now() - tc0).count(); dwords = reinterpret_cast<const uint32_t*>(dataset); loaded_epoch = h / d.p.epoch_blocks;
    };
    load_contexts(height);

    d.copy_path = copy_path;
    Vk v{}; vk_init(v, device, copy_path);
    shaderc_compiler_t sc = shaderc_compiler_initialize();
    v.vs = compile(v, sc, glsl_vertex(), shaderc_glsl_vertex_shader, "kiln.vert");
    { VkShaderModule m1 = compile(v, sc, glsl_scene_pass(d, W, H), shaderc_glsl_compute_shader, "kiln-scene.comp"), m2 = compile(v, sc, glsl_digest_pass(d), shaderc_glsl_compute_shader, "kiln-digest.comp");
      VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO}; ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO; ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.pName = "main"; ci.layout = v.klayout;
      ci.stage.module = m1; VK_CHECK(vkCreateComputePipelines(v.dev, v.cache, 1, &ci, nullptr, &v.scene_pipe)); ci.stage.module = m2; VK_CHECK(vkCreateComputePipelines(v.dev, v.cache, 1, &ci, nullptr, &v.digest_pipe)); vkDestroyShaderModule(v.dev, m1, nullptr); vkDestroyShaderModule(v.dev, m2, nullptr); }
    uint32_t hdr_const[128] = {}; auto pack = [&](uint32_t at, const std::vector<uint8_t>& b) { for (size_t k = 0; k < b.size(); ++k) hdr_const[at + k / 4] |= uint32_t(b[k]) << (8 * (k & 3)); };
    auto refresh_hdr_const = [&]() {
        std::memset(hdr_const, 0, sizeof hdr_const);
        { bytes in; in.tag("IgneumHash/R0A/seed").be64(d.p.chain_id).be32(d.p.version).raw(d.P, 32); pack(0, in.v); }
        { bytes in; in.tag("IgneumHash/Kiln/scene").raw(d.E, 32); pack(26, in.v); }
        { bytes in; in.tag("IgneumHash/Kiln/mix").raw(d.E, 32); pack(40, in.v); }
        { bytes in; in.tag("IgneumHash/R0A/final").be64(d.p.chain_id).be32(d.p.version).raw(d.P, 32); pack(61, in.v); }
    };
    refresh_hdr_const();
    // the dataset image: 16384 x 16384 R32_UINT, uploaded in 64 MiB rows of 1024 lines
    const uint32_t DS = 16384; if (dbytes != uint64_t(DS) * DS * 4) { std::fprintf(stderr, "the port holds a 1 GiB dataset only\n"); return 2; }
    VkBuffer stg; VkDeviceMemory sm; void* map; const VkDeviceSize PIECE = 64u << 20; v.buffer(PIECE, VK_BUFFER_USAGE_TRANSFER_SRC_BIT, VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT, stg, sm, &map);
    auto upload_dataset = [&]() {  // the resident copy refreshed from the loaded epoch's bytes (64 MiB pieces)
        const auto tu = std::chrono::steady_clock::now();
        if (copy_path) { for (VkDeviceSize off = 0; off < dbytes; off += PIECE) { std::memcpy(map, dataset + off, PIECE); VkCommandBuffer cb = v.begin_once(); VkBufferCopy c{0, off, PIECE}; vkCmdCopyBuffer(cb, stg, v.dbuf, 1, &c); v.end_once(cb); } }
        else {
            const uint32_t LINES = 1024;
            { VkCommandBuffer cb = v.begin_once(); VkImageMemoryBarrier b{VK_STRUCTURE_TYPE_IMAGE_MEMORY_BARRIER}; b.oldLayout = VK_IMAGE_LAYOUT_UNDEFINED; b.newLayout = VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL; b.image = v.dimg; b.subresourceRange = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; b.dstAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT; vkCmdPipelineBarrier(cb, VK_PIPELINE_STAGE_TOP_OF_PIPE_BIT, VK_PIPELINE_STAGE_TRANSFER_BIT, 0, 0, nullptr, 0, nullptr, 1, &b); v.end_once(cb); }
            for (uint32_t y = 0; y < DS; y += LINES) { std::memcpy(map, dataset + size_t(y) * DS * 4, size_t(DS) * LINES * 4); VkCommandBuffer cb = v.begin_once(); VkBufferImageCopy c{}; c.imageSubresource = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 0, 1}; c.imageOffset = {0, int32_t(y), 0}; c.imageExtent = {DS / 4, LINES, 1}; vkCmdCopyBufferToImage(cb, stg, v.dimg, VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL, 1, &c); v.end_once(cb); }
            { VkCommandBuffer cb = v.begin_once(); VkImageMemoryBarrier b{VK_STRUCTURE_TYPE_IMAGE_MEMORY_BARRIER}; b.oldLayout = VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL; b.newLayout = VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL; b.image = v.dimg; b.subresourceRange = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; b.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT; b.dstAccessMask = VK_ACCESS_SHADER_READ_BIT; vkCmdPipelineBarrier(cb, VK_PIPELINE_STAGE_TRANSFER_BIT, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, 0, 0, nullptr, 0, nullptr, 1, &b); v.end_once(cb); }
        }
        upload_s = std::chrono::duration<double>(std::chrono::steady_clock::now() - tu).count();
    };
    if (copy_path) {  // the dataset as one storage buffer; the pre-pass pipeline
        v.buffer(dbytes, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_DST_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, v.dbuf, v.dbmem, nullptr);
        VkShaderModule cs = compile(v, sc, glsl_copy(d), shaderc_glsl_compute_shader, "kiln-copy.comp");
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO}; ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO; ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = cs; ci.stage.pName = "main"; ci.layout = v.clayout; VK_CHECK(vkCreateComputePipelines(v.dev, v.cache, 1, &ci, nullptr, &v.cpipe)); vkDestroyShaderModule(v.dev, cs, nullptr);
    } else { VkImageCreateInfo ii{VK_STRUCTURE_TYPE_IMAGE_CREATE_INFO}; ii.imageType = VK_IMAGE_TYPE_2D; ii.format = VK_FORMAT_R32G32B32A32_UINT; ii.extent = {DS / 4, DS, 1}; ii.mipLevels = 1; ii.arrayLayers = 1; ii.samples = VK_SAMPLE_COUNT_1_BIT; ii.tiling = VK_IMAGE_TILING_OPTIMAL; ii.usage = VK_IMAGE_USAGE_SAMPLED_BIT | VK_IMAGE_USAGE_TRANSFER_DST_BIT; ii.initialLayout = VK_IMAGE_LAYOUT_UNDEFINED;
      VK_CHECK(vkCreateImage(v.dev, &ii, nullptr, &v.dimg)); VkMemoryRequirements mr; vkGetImageMemoryRequirements(v.dev, v.dimg, &mr); VkMemoryAllocateInfo ai{VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO}; ai.allocationSize = mr.size; ai.memoryTypeIndex = v.find_mem(mr.memoryTypeBits, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT); VK_CHECK(vkAllocateMemory(v.dev, &ai, nullptr, &v.dmem)); VK_CHECK(vkBindImageMemory(v.dev, v.dimg, v.dmem, 0));
      VkImageViewCreateInfo vi{VK_STRUCTURE_TYPE_IMAGE_VIEW_CREATE_INFO}; vi.image = v.dimg; vi.viewType = VK_IMAGE_VIEW_TYPE_2D; vi.format = VK_FORMAT_R32G32B32A32_UINT; vi.subresourceRange = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; VK_CHECK(vkCreateImageView(v.dev, &vi, nullptr, &v.dview)); }
    upload_dataset();

    Frame fr[2]; for (auto& f : fr) frame_init(v, d, K, W, H, f);
    // the descriptor set: the dataset and frame 0's scene buffer; frames share one set by re-pointing binding 1 before each render (the frames never overlap on the queue: one submit at a time, two frames pipelined on the host)
    auto bind_scene = [&](Frame& f, uint32_t fi) {
        VkDescriptorImageInfo di{v.sampler, v.dview, VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL}; VkDescriptorBufferInfo bi{f.sbuf, 0, VK_WHOLE_SIZE}, ti{v.dbuf, 0, VK_WHOLE_SIZE}, dbi{v.dbuf, 0, VK_WHOLE_SIZE};   // the copy path's bindings 0, 3 and 4 are the dataset buffer (uint, uvec4, uvec4 views)
        VkDescriptorImageInfo si{VK_NULL_HANDLE, f.view, VK_IMAGE_LAYOUT_GENERAL};
        VkWriteDescriptorSet w[5]{}; w[3].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; w[3].dstSet = v.dset; w[3].dstBinding = 3; w[3].descriptorCount = 1; w[3].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[3].pBufferInfo = copy_path ? &ti : &bi;
        w[4].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; w[4].dstSet = v.dset; w[4].dstBinding = 4; w[4].descriptorCount = 1; w[4].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[4].pBufferInfo = copy_path ? &dbi : &bi;
        w[2].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; w[2].dstSet = v.dset; w[2].dstBinding = 2; w[2].descriptorCount = 1; w[2].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_IMAGE; w[2].pImageInfo = &si;
        w[0].sType = w[1].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; w[0].dstSet = w[1].dstSet = v.dset; w[0].dstBinding = 0; w[0].descriptorCount = 1;
        if (copy_path) { w[0].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[0].pBufferInfo = &ti; } else { w[0].descriptorType = VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER; w[0].pImageInfo = &di; }
        w[1].dstBinding = 1; w[1].descriptorCount = 1; w[1].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[1].pBufferInfo = &bi; vkUpdateDescriptorSets(v.dev, 5, w, 0, nullptr);
        { VkDescriptorBufferInfo hb{f.hbuf, 0, VK_WHOLE_SIZE}, vbi{f.vbuf, 0, VK_WHOLE_SIZE}, obi{f.obuf, 0, VK_WHOLE_SIZE}; VkDescriptorImageInfo ti2{VK_NULL_HANDLE, f.view, VK_IMAGE_LAYOUT_GENERAL};
          VkWriteDescriptorSet kw[5]{}; const VkDescriptorBufferInfo* kbs[5] = {&hb, &bi, &vbi, nullptr, &obi};
          for (uint32_t k = 0; k < 5; ++k) { kw[k].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; kw[k].dstSet = v.kdset[fi]; kw[k].dstBinding = k; kw[k].descriptorCount = 1; if (k == 3) { kw[k].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_IMAGE; kw[k].pImageInfo = &ti2; } else { kw[k].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; kw[k].pBufferInfo = kbs[k]; } }
          vkUpdateDescriptorSets(v.dev, 5, kw, 0, nullptr); }
        if (copy_path) { VkWriteDescriptorSet c[3]{}; const VkDescriptorBufferInfo* src[3] = {&dbi, &bi, &ti}; for (uint32_t i = 0; i < 3; ++i) { c[i].sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET; c[i].dstSet = v.cdset[fi]; c[i].dstBinding = i; c[i].descriptorCount = 1; c[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; c[i].pBufferInfo = src[i]; } vkUpdateDescriptorSets(v.dev, 3, c, 0, nullptr); }
    };
    Pool pool(threads);

    // ---- the work list ----
    struct Row { uint64_t height, nonce; uint8_t header[32]; uint8_t mix[32], hash[32]; };
    std::vector<Row> rows; uint8_t header[32] = {};
    if (!set_path.empty()) {
        std::ifstream f(set_path); std::stringstream ss; ss << f.rdbuf(); const std::string s = ss.str();
        // the set: {"version": ..., "vectors": [{"height": n, "header": hex, "nonce": n, "mix": hex, "hash": hex}, ...]} (a flat reader over the keys in order)
        size_t p = 0; while ((p = s.find("\"header\"", p)) != std::string::npos) {
            Row r{}; auto val = [&](const char* key, size_t from) -> std::string { size_t k = s.find(key, from); if (k == std::string::npos) return ""; k = s.find(':', k) + 1; while (k < s.size() && (s[k] == ' ' || s[k] == '"')) ++k; size_t e = k; while (e < s.size() && s[e] != '"' && s[e] != ',' && s[e] != '}') ++e; return s.substr(k, e - k); };
            const size_t obj = s.rfind('{', p);
            r.height = std::stoull(val("\"height\"", obj)); r.nonce = std::stoull(val("\"nonce\"", obj)); if (!unhex(val("\"header\"", obj), r.header, 32) || !unhex(val("\"mix\"", obj), r.mix, 32) || !unhex(val("\"hash\"", obj), r.hash, 32)) { std::fprintf(stderr, "the set's row could not be read\n"); return 2; }
            rows.push_back(r); p += 8; }
        if (rows.empty()) { std::fprintf(stderr, "no rows in the set\n"); return 2; }
    } else { if (!header_hex.empty() && !unhex(header_hex, header, 32)) { std::fprintf(stderr, "--header is 64 hex\n"); return 2; } if (header_hex.empty()) { bytes in; in.tag("IgneumHash/Kiln/bench-header").le64(height); b3(in.v.data(), in.v.size(), header); } }

    // ---- the render of one frame: the jobs' scenes are in f.jobs[0..n), the batch in f.batch; the pipelines given ----
    auto render = [&](Frame& f, Pipes& pp, uint32_t fi) {
        // the host's part: the frame header (the prefixes, the batch's header and programme hash, the nonce base, n, the compare slot); the card draws the seeds, scenes and vertices
        { uint32_t* hw = static_cast<uint32_t*>(f.hmap); std::memcpy(hw, hdr_const, sizeof hdr_const); for (int k = 0; k < 8; ++k) { hw[16 + k] = le32(f.jobs[0].header + 4 * k); hw[53 + k] = le32(pp.batch.phash + 4 * k); }
          hw[24] = uint32_t(f.jobs[0].nonce); hw[25] = uint32_t(f.jobs[0].nonce >> 32); hw[78] = f.n; hw[79] = f.cmp_slot; }
        f.ndraws = d.T; for (uint32_t t = 0; t < d.T; ++t) { f.draws[t][0] = t * f.n * 3; f.draws[t][1] = f.n * 3; }
        if (d.fcap) { f.draws[d.T][0] = d.T * f.n * 3; f.draws[d.T][1] = f.n * 6; f.ndraws = d.T + 1; }   // the fill quads, after every triangle slot   // a slot's draw holds one triangle a hash (a zero-area one draws nothing): no two fragments of one pixel inside a draw
        bind_scene(f, fi);
        VK_CHECK(vkResetCommandBuffer(f.cb, 0)); VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO}; bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT; VK_CHECK(vkBeginCommandBuffer(f.cb, &bi));
        vkCmdResetQueryPool(f.cb, f.qp, 0, 2); vkCmdWriteTimestamp(f.cb, VK_PIPELINE_STAGE_TOP_OF_PIPE_BIT, f.qp, 0);
        vkCmdBindPipeline(f.cb, VK_PIPELINE_BIND_POINT_COMPUTE, v.scene_pipe); vkCmdBindDescriptorSets(f.cb, VK_PIPELINE_BIND_POINT_COMPUTE, v.klayout, 0, 1, &v.kdset[fi], 0, nullptr); vkCmdDispatch(f.cb, (f.n + 63) / 64, 1, 1);
        { VkMemoryBarrier gb{VK_STRUCTURE_TYPE_MEMORY_BARRIER}; gb.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT; gb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_VERTEX_ATTRIBUTE_READ_BIT; vkCmdPipelineBarrier(f.cb, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT | VK_PIPELINE_STAGE_VERTEX_INPUT_BIT | VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, 0, 1, &gb, 0, nullptr, 0, nullptr); }
        // (the copy path's fragments read the dataset buffer directly by plain loads: no pre-pass; the per-frame tile cache was retired when it passed 32-bit addressing at K 8192)
        // the target zeroed, then one pass instance a triangle slot (every hash's slot-t triangle), a fragment-to-fragment barrier on the image between slots, then the copy out
        const VkImageSubresourceRange whole{VK_IMAGE_ASPECT_COLOR_BIT, 0, 1, 0, 1}; VkClearColorValue zero{}; vkCmdClearColorImage(f.cb, f.img, VK_IMAGE_LAYOUT_GENERAL, &zero, 1, &whole);
        VkImageMemoryBarrier ib{VK_STRUCTURE_TYPE_IMAGE_MEMORY_BARRIER}; ib.oldLayout = ib.newLayout = VK_IMAGE_LAYOUT_GENERAL; ib.image = f.img; ib.subresourceRange = whole; ib.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT; ib.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_SHADER_WRITE_BIT;
        vkCmdPipelineBarrier(f.cb, VK_PIPELINE_STAGE_TRANSFER_BIT, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, 0, 0, nullptr, 0, nullptr, 1, &ib);
        VkRenderPassBeginInfo rb{VK_STRUCTURE_TYPE_RENDER_PASS_BEGIN_INFO}; rb.renderPass = v.rp; rb.framebuffer = f.fb; rb.renderArea = {{0, 0}, {W, H}};
        const VkDeviceSize off = 0;
        for (uint32_t k = 0; k < f.ndraws; ++k) {
            if (k) { ib.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT; ib.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_SHADER_WRITE_BIT; vkCmdPipelineBarrier(f.cb, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, 0, 0, nullptr, 0, nullptr, 1, &ib); }
            if (!f.draws[k][1]) continue;
            vkCmdBeginRenderPass(f.cb, &rb, VK_SUBPASS_CONTENTS_INLINE); vkCmdBindVertexBuffers(f.cb, 0, 1, &f.vbuf, &off); vkCmdBindDescriptorSets(f.cb, VK_PIPELINE_BIND_POINT_GRAPHICS, v.layout, 0, 1, &v.dset, 0, nullptr);
            vkCmdBindPipeline(f.cb, VK_PIPELINE_BIND_POINT_GRAPHICS, pp.p); vkCmdDraw(f.cb, f.draws[k][1], 1, f.draws[k][0], 0); vkCmdEndRenderPass(f.cb);
        }
        ib.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT; ib.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_TRANSFER_READ_BIT; vkCmdPipelineBarrier(f.cb, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT | VK_PIPELINE_STAGE_TRANSFER_BIT, 0, 0, nullptr, 0, nullptr, 1, &ib);
        vkCmdBindPipeline(f.cb, VK_PIPELINE_BIND_POINT_COMPUTE, v.digest_pipe); vkCmdBindDescriptorSets(f.cb, VK_PIPELINE_BIND_POINT_COMPUTE, v.klayout, 0, 1, &v.kdset[fi], 0, nullptr); vkCmdDispatch(f.cb, (f.n + 63) / 64, 1, 1);
        VkBufferImageCopy c{}; c.imageSubresource = {VK_IMAGE_ASPECT_COLOR_BIT, 0, 0, 1}; c.imageOffset = {int32_t(d.side * (f.cmp_slot % COLS)), int32_t(d.side * (f.cmp_slot / COLS)), 0}; c.imageExtent = {d.side, d.side, 1}; vkCmdCopyImageToBuffer(f.cb, f.img, VK_IMAGE_LAYOUT_GENERAL, f.rbuf, 1, &c);
        VkBufferMemoryBarrier mb[2]{}; for (int k = 0; k < 2; ++k) { mb[k].sType = VK_STRUCTURE_TYPE_BUFFER_MEMORY_BARRIER; mb[k].dstAccessMask = VK_ACCESS_HOST_READ_BIT; mb[k].size = VK_WHOLE_SIZE; } mb[0].srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT; mb[0].buffer = f.rbuf; mb[1].srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT; mb[1].buffer = f.obuf;
        vkCmdPipelineBarrier(f.cb, VK_PIPELINE_STAGE_TRANSFER_BIT | VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT, VK_PIPELINE_STAGE_HOST_BIT, 0, 0, nullptr, 2, mb, 0, nullptr);
        vkCmdWriteTimestamp(f.cb, VK_PIPELINE_STAGE_BOTTOM_OF_PIPE_BIT, f.qp, 1);
        VK_CHECK(vkEndCommandBuffer(f.cb)); VK_CHECK(vkResetFences(v.dev, 1, &f.fence)); VkSubmitInfo si{VK_STRUCTURE_TYPE_SUBMIT_INFO}; si.commandBufferCount = 1; si.pCommandBuffers = &f.cb; VK_CHECK(vkQueueSubmit(v.queue, 1, &si, f.fence)); f.pending = true; f.batch = pp.batch; f.pipes = &pp;
    };
    auto finish = [&](Frame& f) {  // wait, then every hash's digest from its tile in the readback (row order)
        VK_CHECK(vkWaitForFences(v.dev, 1, &f.fence, VK_TRUE, UINT64_MAX)); f.pending = false;
        { uint64_t ts[2] = {0, 0}; if (vkGetQueryPoolResults(v.dev, f.qp, 0, 2, sizeof ts, ts, 8, VK_QUERY_RESULT_64_BIT) == VK_SUCCESS) f.gpu_ms = double(ts[1] - ts[0]) * v.ts_period_ns * 1e-6; else f.gpu_ms = 0; }
        const uint32_t* ob = static_cast<const uint32_t*>(f.omap);
        for (uint32_t i = 0; i < f.n; ++i) { HashJob& j = f.jobs[i]; for (int k = 0; k < 8; ++k) { put_le32(j.S + 4 * k, ob[i * 24 + k]); put_le32(j.M + 4 * k, ob[i * 24 + 8 + k]); put_le32(j.F + 4 * k, ob[i * 24 + 16 + k]); } }
    };
    auto tile_of = [&](const Frame& f, uint32_t i, std::vector<uint8_t>& tile) { if (i != f.cmp_slot) { std::fprintf(stderr, "tile_of: slot %u is not the frame's compare slot %u\n", i, f.cmp_slot); std::exit(2); } tile.assign(static_cast<const uint8_t*>(f.rmap), static_cast<const uint8_t*>(f.rmap) + size_t(d.side) * d.side * 4); };

    // ---- the set: every row rendered and compared ----
    if (!rows.empty()) {
        size_t ok = 0, bad = 0; std::string first_fault; double compile_ms = 0; uint64_t frags = 0; double epochs_s = 0;
        std::vector<size_t> order(rows.size()); for (size_t r = 0; r < rows.size(); ++r) order[r] = r;
        std::stable_sort(order.begin(), order.end(), [&](size_t a, size_t b) { return rows[a].height / d.p.epoch_blocks < rows[b].height / d.p.epoch_blocks; });
        for (size_t oi = 0; oi < order.size(); ++oi) {
            const size_t r = order[oi];
            if (rows[r].height / d.p.epoch_blocks != loaded_epoch) { load_contexts(rows[r].height); upload_dataset(); refresh_hdr_const(); epochs_s += dataset_s + upload_s; }
            Frame& f = fr[0]; f.n = 1; HashJob& j = f.jobs[0]; j.height = rows[r].height; j.nonce = rows[r].nonce; std::memcpy(j.header, rows[r].header, 32);
            Pipes pp; batch_of(d, j.header, j.nonce, pp.batch); build_pipes(v, sc, d, W, H, pp); compile_ms += pp.compile_ms;
            if (dump_glsl && r == 0) std::fprintf(stderr, "%s\n", glsl_fragment(d, pp.batch).c_str());
            f.cmp_slot = 0; render(f, pp, 0); finish(f);
            std::vector<uint8_t> cpu; uint64_t fg = 0; scene_of(d, j); cpu_render(d, pp.batch, j, dwords, cpu, &fg); frags += fg;
            std::vector<uint8_t> gpu; tile_of(f, 0, gpu);
            uint8_t mref[32], href[32]; const int rc = ignpow_hash(light, j.height, j.header, j.nonce, mref, href);
            const bool m_ok = std::memcmp(j.M, rows[r].mix, 32) == 0, h_ok = std::memcmp(j.F, rows[r].hash, 32) == 0, ref_ok = rc == IGNPOW_OK && std::memcmp(mref, rows[r].mix, 32) == 0;
            if (m_ok && h_ok) ++ok; else { ++bad; if (first_fault.empty()) { size_t px = 0; for (; px < cpu.size() / 4; ++px) if (std::memcmp(&cpu[px * 4], &gpu[px * 4], 4) != 0) break;
                char buf[400]; if (px < cpu.size() / 4) std::snprintf(buf, sizeof buf, "row %zu nonce %llu: first divergent pixel (%u, %u): cpu %s gpu %s", r, (unsigned long long)j.nonce, unsigned(px % d.side), unsigned(px / d.side), hex(&cpu[px * 4], 4).c_str(), hex(&gpu[px * 4], 4).c_str()); else std::snprintf(buf, sizeof buf, "row %zu nonce %llu: the target equals the port's CPU render, the digest differs (mix %s against the set's %s; the reference library's mix %s the set)", r, (unsigned long long)j.nonce, hex(j.M, 8).c_str(), hex(rows[r].mix, 8).c_str(), ref_ok ? "equals" : "differs from"); first_fault = buf; } }
            free_pipes(v, pp);
        }
        std::printf("{\"kiln_vk\": {\"mode\": \"set\", \"device\": \"%s\", \"doc\": \"%s\", \"set\": \"%s\", \"rows\": %zu, \"exact\": %zu, \"mismatches\": %zu, \"first_fault\": \"%s\", \"compile_ms_per_batch\": %.2f, \"fragments_per_hash\": %.1f, \"epochs_s\": %.1f, \"dataset_upload_s\": %.1f, \"tiles\": %u, \"rows\": %u, \"fragments\": %u, \"coalesced\": %d, \"contig\": %u, \"T\": %u, \"side\": %u, \"words\": %u, \"tile_path\": \"%s\"}}\n",
                    v.name.c_str(), doc_path.c_str(), set_path.c_str(), rows.size(), ok, bad, first_fault.c_str(), compile_ms / double(rows.size()), double(frags) / double(rows.size()), epochs_s, upload_s, d.tiles, d.rows, d.fcap, int(d.coal), d.contig, d.T, d.side, d.words, copy_path ? "copy" : "fetch");
        std::fflush(stdout); std::fflush(stderr); vkDeviceWaitIdle(v.dev); std::_Exit(bad ? 3 : 0);   // the figure is out; the driver's teardown once hung a run at exit
    }

    // ---- the throughput window: one header, nonces from nonce0, batches of 2^batch_log2 nonces, K hashes a render; the compile of the next batches ahead ----
    const uint64_t per_batch = 1ull << d.batch_log2; if (per_batch < K) { std::fprintf(stderr, "K %u above the batch's %llu nonces\n", K, (unsigned long long)per_batch); return 2; }
    std::vector<std::future<Pipes*>> ahead_q; uint64_t next_batch = nonce0 >> d.batch_log2;
    auto launch = [&](uint64_t b) { ahead_q.push_back(std::async(std::launch::async, [&, b] { Pipes* pp = new Pipes; batch_of(d, header, b << d.batch_log2, pp->batch); build_pipes(v, sc, d, W, H, *pp); return pp; })); };
    for (uint32_t i = 0; i < ahead + 1; ++i) launch(next_batch++);
    Pipes* cur = ahead_q.front().get(); ahead_q.erase(ahead_q.begin()); launch(next_batch++);
    std::vector<Pipes*> retired; double gpu_ms_window = 0; uint64_t frames = 0; uint64_t nonce = nonce0, hashes = 0, hashes_window = 0, batches = 0, frags_sampled = 0, frags_hashes = 0; double compile_total = cur->compile_ms; size_t compared = 0, mism = 0; std::string first_fault;
    const auto t0 = std::chrono::steady_clock::now(); double last_line = 0; uint64_t hashes_at_line = 0; const double warm = 10.0; bool in_window = false; double window_start = 0, window_end = 0;
    uint32_t cur_frame = 0; std::vector<uint8_t> cpu, gpu;
    for (;;) {
        const double now = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (now >= seconds) break;
        if (!in_window && now >= warm) { in_window = true; window_start = now; hashes_window = 0; }
        Frame& f = fr[cur_frame];
        if (f.pending) { finish(f); if (in_window) { hashes_window += f.n; gpu_ms_window += f.gpu_ms; } hashes += f.n;
            // the check: one hash of the frame just finished against the port's CPU render and the reference library, once a second
            if (now - last_line >= 1.0) {
                const uint32_t i = f.cmp_slot; uint64_t fg = 0; uint8_t cardS[32]; std::memcpy(cardS, f.jobs[i].S, 32); scene_of(d, f.jobs[i]); cpu_render(d, f.batch, f.jobs[i], dwords, cpu, &fg);
                if (std::getenv("KILN_TRACE") && compared < 2) { std::vector<uint8_t> hostM(32), hostF(32); HashJob hj = f.jobs[i]; digest_of(d, f.batch, hj, cpu.data()); std::fprintf(stderr, "S card %s host %s\nM card %s host %s\nF card %s host %s\n", hex(cardS, 32).c_str(), hex(f.jobs[i].S, 32).c_str(), hex(f.jobs[i].M, 32).c_str(), hex(hj.M, 32).c_str(), hex(f.jobs[i].F, 32).c_str(), hex(hj.F, 32).c_str());
                    bytes in; in.tag("IgneumHash/R0A/final").be64(d.p.chain_id).be32(d.p.version).raw(d.P, 32).raw(f.jobs[i].S, 32).raw(f.jobs[i].M, 32); uint8_t t[32];
                    std::fprintf(stderr, "host final bytes 0..31 %s\nhost final bytes 56..87 %s\n", hex(in.v.data(), 32).c_str(), hex(in.v.data() + 56, 32).c_str());
                    for (size_t cut = 120; cut <= 129; ++cut) { b3(in.v.data(), cut, t); if (!std::memcmp(t, f.jobs[i].F, 32)) std::fprintf(stderr, "card F = BLAKE3 of the first %zu bytes\n", cut); }
                    { std::vector<uint8_t> z(in.v); z.resize(192, 0); for (size_t cut = 130; cut <= 192; ++cut) { b3(z.data(), cut, t); if (!std::memcmp(t, f.jobs[i].F, 32)) std::fprintf(stderr, "card F = BLAKE3 of %zu bytes zero-padded\n", cut); } }
                    { uint8_t hb[132]; for (int k = 0; k < 33; ++k) put_le32(hb + 4 * k, hdr_const[k]); uint8_t hw2[8]; for (int k = 0; k < 8; ++k) put_le32(hw2 + 4 * (k & 1), 0); (void)hw2; uint32_t* hwp = static_cast<uint32_t*>(f.hmap); for (int k = 0; k < 33; ++k) put_le32(hb + 4 * k, hwp[k]); b3(hb, 129, t); std::fprintf(stderr, "host BLAKE3(hdr 129) %s\n", hex(t, 32).c_str()); b3(hb, 128, t); std::fprintf(stderr, "host BLAKE3(hdr 128) %s\n", hex(t, 32).c_str()); }
                    { std::vector<uint8_t> z(in.v); z[128] = 0; b3(z.data(), 129, t); if (!std::memcmp(t, f.jobs[i].F, 32)) std::fprintf(stderr, "card F = last byte zero\n"); } } tile_of(f, i, gpu); frags_sampled += fg; ++frags_hashes;
                uint8_t mref[32], href[32]; const bool lib_ok = ignpow_hash(light, f.jobs[i].height, f.jobs[i].header, f.jobs[i].nonce, mref, href) == IGNPOW_OK && std::memcmp(mref, f.jobs[i].M, 32) == 0 && std::memcmp(href, f.jobs[i].F, 32) == 0;
                if (std::getenv("KILN_TRACE") && compared == 0) { std::fprintf(stderr, "port: S %s E %s P %s M %s F %s\n", hex(f.jobs[i].S, 32).c_str(), hex(d.E, 32).c_str(), hex(d.P, 32).c_str(), hex(f.jobs[i].M, 32).c_str(), hex(f.jobs[i].F, 32).c_str()); std::fprintf(stderr, "lib: mix %s hash %s\n", hex(mref, 32).c_str(), hex(href, 32).c_str()); }
                if (std::getenv("KILN_TRACE")) std::fprintf(stderr, "compare %zu: nonce %llu slot %u lib_ok %d target_equal %d M %s mix %s\n", compared, (unsigned long long)f.jobs[i].nonce, i, int(lib_ok), int(cpu == gpu), hex(f.jobs[i].M, 8).c_str(), hex(mref, 8).c_str());
                ++compared; if (cpu != gpu || !lib_ok) { ++mism; if (first_fault.empty()) { size_t px = 0; for (; px < cpu.size() / 4; ++px) if (std::memcmp(&cpu[px * 4], &gpu[px * 4], 4) != 0) break; char buf[300]; std::snprintf(buf, sizeof buf, "nonce %llu: %s (%u, %u) cpu %s gpu %s", (unsigned long long)f.jobs[i].nonce, px < cpu.size() / 4 ? "first divergent pixel" : "target equal, the library's digest differs", unsigned(px % d.side), unsigned(px / d.side), px < cpu.size() / 4 ? hex(&cpu[px * 4], 4).c_str() : "", px < cpu.size() / 4 ? hex(&gpu[px * 4], 4).c_str() : ""); first_fault = buf; } }
                std::printf("{\"t\": %.1f, \"hashes\": %llu, \"hs\": %.0f, \"batches\": %llu, \"compared\": %zu, \"mismatches\": %zu}\n", now, (unsigned long long)hashes, (hashes - hashes_at_line) / (now - last_line), (unsigned long long)batches, compared, mism); std::fflush(stdout); last_line = now; hashes_at_line = hashes;
            }
        }
        // the batch for this nonce run
        const uint64_t b = nonce >> d.batch_log2;
        if (b != ((nonce - 1) >> d.batch_log2) && nonce != nonce0 && (nonce & (per_batch - 1)) == 0) { retired.push_back(cur); cur = ahead_q.front().get(); ahead_q.erase(ahead_q.begin()); launch(next_batch++); compile_total += cur->compile_ms; ++batches; }
        // a retired batch's pipelines are destroyed only once no frame in flight was recorded with them
        for (size_t r = 0; r < retired.size();) { Pipes* pp = retired[r]; bool used = false; for (auto& q : fr) if (q.pending && q.pipes == pp) used = true; if (used) { ++r; continue; } free_pipes(v, *pp); delete pp; retired.erase(retired.begin() + long(r)); }
        f.n = K; for (uint32_t i = 0; i < K; ++i) { HashJob& j = f.jobs[i]; j.height = height; j.nonce = nonce + i; std::memcpy(j.header, header, 32); }
        f.cmp_slot = uint32_t((frames * 977u + 13u) % K); ++frames;
        render(f, *cur, cur_frame); nonce += K;
        cur_frame ^= 1; window_end = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    }
    for (auto& f : fr) if (f.pending) { finish(f); hashes += f.n; }
    window_end = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    const double win = window_end - window_start;
    std::printf("{\"kiln_vk\": {\"mode\": \"window\", \"device\": \"%s\", \"doc\": \"%s\", \"seconds\": %.1f, \"warm_s\": %.0f, \"window_s\": %.2f, \"window_hashes\": %llu, \"hashes_per_s\": %.0f, \"gpu_busy_s\": %.2f, \"card_ceiling_hashes_per_s_derived\": %.0f, \"hashes_total\": %llu, \"batches\": %llu, \"k\": %u, \"nonces_per_batch\": %llu, \"compile_ms_per_batch\": %.2f, \"compared\": %zu, \"mismatches\": %zu, \"first_fault\": \"%s\", \"fragments_per_hash\": %.1f, \"words\": %u, \"T\": %u, \"side\": %u, \"tiles\": %u, \"rows\": %u, \"fragments\": %u, \"coalesced\": %d, \"contig\": %u, \"threads\": %u, \"dataset_build_s\": %.1f, \"dataset_upload_s\": %.1f, \"tile_path\": \"%s\"}}\n",
                v.name.c_str(), doc_path.c_str(), seconds, warm, win, (unsigned long long)hashes_window, win > 0 ? hashes_window / win : 0.0, gpu_ms_window * 1e-3, gpu_ms_window > 0 ? hashes_window / (gpu_ms_window * 1e-3) : 0.0, (unsigned long long)hashes, (unsigned long long)batches + 1, K, (unsigned long long)per_batch, compile_total / double(batches + 1), compared, mism, first_fault.c_str(), frags_hashes ? double(frags_sampled) / double(frags_hashes) : 0.0, d.words, d.T, d.side, d.tiles, d.rows, d.fcap, int(d.coal), d.contig, threads, dataset_s, upload_s, copy_path ? "copy" : "fetch");
    std::fflush(stdout); std::fflush(stderr); vkDeviceWaitIdle(v.dev); std::_Exit(mism ? 3 : 0);   // the figure is out; the driver's teardown once hung a run at exit
}
