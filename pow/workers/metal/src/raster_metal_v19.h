// raster_metal_v19.h: VERSION 19 on Metal, the twin of raster_gl_v19.h and raster_gl_v19_card.h (the GPU team, 18 October 2026). The same four
// passes in MSL: G one thread an attempt (the seed, the init digest, the chain's records and S), D one instanced draw of a quad an
// instance into the atlas (the fragment computing each pixel from the keyed base alone: the base texel at the surface position, the
// integer cover test relative to the box, the saturating add where covered), F one thread a box folding its cell into (a, b), H one
// thread an attempt (the mix over the accumulators with the chunk merge, the final, the target compare, M, F and the hit). The base is
// the card's side x side RGBA8 texture filled top-down by replaceRegion (Metal's row 0 is the top, so no row flip), the atlas a render
// target read by the fold, every buffer shared on unified memory. One command buffer a batch, four encoders, one wait.
#pragma once
#include "raster_metal.h"
#import <Metal/Metal.h>
namespace igm {
static const char *kM19Src = R"MSL(
#include <metal_stdlib>
using namespace metal;
constant uint IV[8] = {0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u};
constant uint PERM[16] = {2u, 6u, 3u, 10u, 7u, 0u, 4u, 13u, 1u, 11u, 12u, 5u, 9u, 14u, 15u, 8u};
constant uint TAG_SEED[20] = {19u, 73u,103u,110u,101u,117u,109u,72u,97u,115u,104u,47u,82u,48u,65u,47u,115u,101u,101u,100u};
constant uint TAG_INIT[20] = {19u, 73u,103u,110u,101u,117u,109u,72u,97u,115u,104u,47u,82u,48u,73u,47u,105u,110u,105u,116u};
constant uint TAG_MIX[19] = {18u, 73u,103u,110u,101u,117u,109u,72u,97u,115u,104u,47u,82u,48u,73u,47u,109u,105u,120u};
constant uint TAG_FINAL[21] = {20u, 73u,103u,110u,101u,117u,109u,72u,97u,115u,104u,47u,82u,48u,65u,47u,102u,105u,110u,97u,108u};
static inline uint rotr32(uint x, uint n) { return (x >> n) | (x << (32u - n)); }
static inline uint rotl32(uint x, uint n) { return (x << n) | (x >> (32u - n)); }
static inline uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }
static inline void g(thread uint *v, uint a, uint b, uint c, uint d, uint x, uint y) { v[a] = v[a] + v[b] + x; v[d] = rotr32(v[d] ^ v[a], 16u); v[c] = v[c] + v[d]; v[b] = rotr32(v[b] ^ v[c], 12u); v[a] = v[a] + v[b] + y; v[d] = rotr32(v[d] ^ v[a], 8u); v[c] = v[c] + v[d]; v[b] = rotr32(v[b] ^ v[c], 7u); }
static inline void compress(thread uint *cv, thread uint *m, uint t, uint blen, uint flags) {
  uint v[16]; for (int i = 0; i < 8; i++) v[i] = cv[i]; v[8] = IV[0]; v[9] = IV[1]; v[10] = IV[2]; v[11] = IV[3]; v[12] = t; v[13] = 0u; v[14] = blen; v[15] = flags;
  for (int r = 0; r < 7; r++) { g(v,0u,4u,8u,12u,m[0],m[1]); g(v,1u,5u,9u,13u,m[2],m[3]); g(v,2u,6u,10u,14u,m[4],m[5]); g(v,3u,7u,11u,15u,m[6],m[7]); g(v,0u,5u,10u,15u,m[8],m[9]); g(v,1u,6u,11u,12u,m[10],m[11]); g(v,2u,7u,8u,13u,m[12],m[13]); g(v,3u,4u,9u,14u,m[14],m[15]);
    if (r < 6) { uint p[16]; for (int i = 0; i < 16; i++) p[i] = m[PERM[i]]; for (int i = 0; i < 16; i++) m[i] = p[i]; } }
  for (int i = 0; i < 8; i++) cv[i] = v[i] ^ v[i + 8];
}
static inline void hash_chunk(thread const uint *sb, uint len, uint t, bool root, thread uint *cv) {
  for (int i = 0; i < 8; i++) cv[i] = IV[i]; uint nblk = (len + 63u) / 64u; if (nblk == 0u) nblk = 1u;
  for (uint b = 0u; b < nblk; b++) { uint m[16]; uint bo = b * 64u; uint blen = min(64u, len - bo);
    for (int i = 0; i < 16; i++) { uint wv = 0u; for (uint q = 0u; q < 4u; q++) { uint o = uint(i) * 4u + q; if (o < blen) wv |= sb[bo + o] << (8u * q); } m[i] = wv; }
    uint flags = (b == 0u ? 1u : 0u) | (b + 1u == nblk ? 2u : 0u) | ((root && b + 1u == nblk) ? 8u : 0u); compress(cv, m, t, blen, flags); }
}
static inline void put32be(thread uint *sb, uint o, uint v) { sb[o] = v >> 24; sb[o + 1u] = (v >> 16) & 255u; sb[o + 2u] = (v >> 8) & 255u; sb[o + 3u] = v & 255u; }
static inline void put_words_le(thread uint *sb, uint o, thread const uint *w) { for (uint i = 0u; i < 8u; i++) { sb[o + 4u * i] = w[i] & 255u; sb[o + 4u * i + 1u] = (w[i] >> 8) & 255u; sb[o + 4u * i + 2u] = (w[i] >> 16) & 255u; sb[o + 4u * i + 3u] = (w[i] >> 24) & 255u; } }
struct Consts { uint n_att, t, side, reach, cs, cpr, aw, ah, chain_lo, chain_hi, version, pad0; uint P[8]; uint K[8]; uint header[8]; uint target[8]; };
static inline int clampv(int v, uint side) { return v < 0 ? 0 : v > int(side) ? int(side) : v; }
kernel void r19_gen(device uint4 *rec [[buffer(1)]], device const uint2 *nonce [[buffer(4)]], device uint *seedw [[buffer(5)]], constant Consts &c [[buffer(0)]], uint a [[thread_position_in_grid]]) {
  if (a >= c.n_att) return; uint sb[256];
  uint o = 0u; for (int i = 0; i < 20; i++) sb[o++] = TAG_SEED[i]; put32be(sb, o, c.chain_hi); put32be(sb, o + 4u, c.chain_lo); o += 8u; put32be(sb, o, c.version); o += 4u;
  uint pw[8], kw[8], hw[8]; for (int i = 0; i < 8; i++) { pw[i] = c.P[i]; kw[i] = c.K[i]; hw[i] = c.header[i]; }
  put_words_le(sb, o, pw); o += 32u; put_words_le(sb, o, hw); o += 32u; uint2 nn = nonce[a]; put32be(sb, o, nn.y); put32be(sb, o + 4u, nn.x); o += 8u;
  uint S[8]; hash_chunk(sb, o, 0u, true, S); for (int i = 0; i < 8; i++) seedw[8u * a + uint(i)] = S[i];
  o = 0u; for (int i = 0; i < 20; i++) sb[o++] = TAG_INIT[i]; put_words_le(sb, o, kw); o += 32u; put_words_le(sb, o, S); o += 32u;
  uint st[8]; hash_chunk(sb, o, 0u, true, st);
  uint bits = c.reach == 31u ? 6u : 5u, mask = (1u << bits) - 1u; int rr = int(c.reach);
  for (uint i = 0u; i < c.t; i++) { uint l = i & 7u; uint w = st[l] ^ kw[(i >> 3) & 7u];
    uint u = fmix32(w ^ 0x2545F491u), col = fmix32(w ^ 0x9E3779B9u);
    int x0, y0; if (c.side == 32768u) { x0 = int(w & 32767u); y0 = int((w >> 15) & 32767u); } else { x0 = int(w & 16383u); y0 = int((w >> 14) & 16383u); }
    int x1 = clampv(x0 + int(u & mask) - rr, c.side), y1 = clampv(y0 + int((u >> bits) & mask) - rr, c.side), x2 = clampv(x0 + int((u >> (2u * bits)) & mask) - rr, c.side), y2 = clampv(y0 + int((u >> (3u * bits)) & mask) - rr, c.side);
    int minx = min(x0, min(x1, x2)), miny = min(y0, min(y1, y2)), maxx = max(x0, max(x1, x2)), maxy = max(y0, max(y1, y2));
    uint b = a * c.t + i; rec[4u * b] = uint4(uint(x0), uint(y0), uint(x1), uint(y1)); rec[4u * b + 1u] = uint4(uint(x2), uint(y2), uint(minx), uint(miny));
    rec[4u * b + 2u] = uint4(uint(maxx - minx), uint(maxy - miny), col, w); rec[4u * b + 3u] = uint4(fmix32(w ^ (i * 0x9E3779B1u)), 0u, 0u, 0u);
    st[l] = fmix32(st[l] ^ col ^ (i * 0x9E3779B1u)); }
}
struct VOut { float4 pos [[position]]; uint box [[flat]]; int2 cell [[flat]]; };
vertex VOut r19_vs(uint vid [[vertex_id]], uint iid [[instance_id]], device const uint4 *rec [[buffer(1)]], constant Consts &c [[buffer(0)]]) {
  VOut o; o.box = iid; uint4 r2 = rec[4u * iid + 2u]; uint w = r2.x, h = r2.y;
  o.cell = int2(int((iid % c.cpr) * c.cs), int((iid / c.cpr) * c.cs));
  float2 corner = float2(float((vid & 1u) != 0u ? w : 0u), float((vid & 2u) != 0u ? h : 0u));
  float2 px = float2(o.cell) + corner; o.pos = float4(px.x / float(c.aw) * 2.0f - 1.0f, 1.0f - px.y / float(c.ah) * 2.0f, 0.0f, 1.0f);   // the atlas row 0 at the top
  return o;
}
static inline bool covered(int2 l, uint4 r0, uint4 r1) {
  int ax = int(r0.x) - int(r1.z), ay = int(r0.y) - int(r1.w), bx = int(r0.z) - int(r1.z), by = int(r0.w) - int(r1.w), cx = int(r1.x) - int(r1.z), cy = int(r1.y) - int(r1.w);
  int area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (area == 0) return false; int s = area > 0 ? 1 : -1;
  int px = 2 * l.x + 1, py = 2 * l.y + 1;
  int ex[3] = {ax, bx, cx}, ey[3] = {ay, by, cy}, fx[3] = {bx, cx, ax}, fy[3] = {by, cy, ay};
  for (int k = 0; k < 3; k++) { int dx = fx[k] - ex[k], dy = fy[k] - ey[k]; bool tl = dy == 0 ? ((dx > 0 ? 1 : -1) == s) : ((-dy > 0 ? 1 : -1) == s);
    int w = ((2 * fx[k] - 2 * ex[k]) * (py - 2 * ey[k]) - (2 * fy[k] - 2 * ey[k]) * (px - 2 * ex[k])) * s; if (w < 0 || (w == 0 && !tl)) return false; }
  return true;
}
fragment float4 r19_fs(VOut in [[stage_in]], device const uint4 *rec [[buffer(1)]], texture2d<float, access::read> base [[texture(0)]]) {
  uint4 r0 = rec[4u * in.box], r1 = rec[4u * in.box + 1u], r2 = rec[4u * in.box + 2u];
  int2 l = int2(in.pos.xy) - in.cell; int2 sp = int2(int(r1.z) + l.x, int(r1.w) + l.y);
  uint4 b = uint4(round(base.read(uint2(sp)) * 255.0f));   // the base's row y is our row y (replaceRegion fills top-down)
  if (covered(l, r0, r1)) { uint cc = r2.z; b = min(b + uint4(cc & 255u, (cc >> 8) & 255u, (cc >> 16) & 255u, (cc >> 24) & 255u), uint4(255u)); }
  return float4(b) / 255.0f;
}
kernel void r19_fold(texture2d<float, access::read> atlas [[texture(0)]], device const uint4 *rec [[buffer(1)]], device uint *acc [[buffer(2)]], constant Consts &c [[buffer(0)]], uint box [[thread_position_in_grid]]) {
  uint nbox = c.n_att * c.t; if (box >= nbox) return;
  uint4 r2 = rec[4u * box + 2u], r3 = rec[4u * box + 3u]; uint w = r2.x, h = r2.y; uint a = r2.w, b = r3.x;
  uint2 cell = uint2((box % c.cpr) * c.cs, (box / c.cpr) * c.cs);
  for (uint y = 0u; y < h; y++) for (uint x = 0u; x < w; x++) {
    uint4 p = uint4(round(atlas.read(cell + uint2(x, y)) * 255.0f)); uint v = p.x | (p.y << 8) | (p.z << 16) | (p.w << 24);
    a = fmix32(a ^ v ^ b); b = (b + a) ^ rotl32(a, 13u);
  }
  acc[2u * box] = a; acc[2u * box + 1u] = b;
}
kernel void r19_hash(device const uint *acc [[buffer(2)]], device const uint *seedw [[buffer(5)]], device uint *outw [[buffer(6)]], device atomic_uint *hits [[buffer(7)]], constant Consts &c [[buffer(0)]], uint a [[thread_position_in_grid]]) {
  if (a >= c.n_att) return; uint sb[1088];
  uint total = 51u + 8u * c.t; uint nch = (total + 1023u) / 1024u;
  uint stackv[8][8]; uint sl = 0u; uint cv[8];
  for (uint ch = 0u; ch < nch; ch++) { uint start = ch * 1024u; uint len = min(1024u, total - start);
    for (uint i = 0u; i < len; i++) { uint o = start + i; uint by; if (o < 19u) by = TAG_MIX[o]; else if (o < 51u) { uint k = o - 19u; by = (c.K[k >> 2] >> (8u * (k & 3u))) & 255u; } else { uint r = o - 51u; uint wv = acc[2u * a * c.t + (r >> 2)]; by = (wv >> (8u * (r & 3u))) & 255u; } sb[i] = by; }
    hash_chunk(sb, len, ch, nch == 1u, cv);
    if (ch + 1u < nch) { uint n = ch + 1u; while ((n & 1u) == 0u) { uint pm[16]; for (int i = 0; i < 8; i++) { pm[i] = stackv[sl - 1u][i]; pm[8 + i] = cv[i]; } sl--; uint iv[8]; for (int i = 0; i < 8; i++) iv[i] = IV[i]; compress(iv, pm, 0u, 64u, 4u); for (int i = 0; i < 8; i++) cv[i] = iv[i]; n >>= 1u; }
      for (int i = 0; i < 8; i++) stackv[sl][i] = cv[i]; sl++; } }
  while (sl > 0u) { uint pm[16]; for (int i = 0; i < 8; i++) { pm[i] = stackv[sl - 1u][i]; pm[8 + i] = cv[i]; } sl--; uint iv[8]; for (int i = 0; i < 8; i++) iv[i] = IV[i]; compress(iv, pm, 0u, 64u, 4u | (sl == 0u ? 8u : 0u)); for (int i = 0; i < 8; i++) cv[i] = iv[i]; }
  uint M[8]; for (int i = 0; i < 8; i++) M[i] = cv[i];
  uint o = 0u; for (int i = 0; i < 21; i++) sb[o++] = TAG_FINAL[i]; put32be(sb, o, c.chain_hi); put32be(sb, o + 4u, c.chain_lo); o += 8u; put32be(sb, o, c.version); o += 4u;
  uint pw[8]; for (int i = 0; i < 8; i++) pw[i] = c.P[i]; put_words_le(sb, o, pw); o += 32u;
  uint S[8]; for (int i = 0; i < 8; i++) S[i] = seedw[8u * a + uint(i)]; put_words_le(sb, o, S); o += 32u; put_words_le(sb, o, M); o += 32u;
  uint F[8]; hash_chunk(sb, o, 0u, true, F);
  bool le = true, decided = false;
  for (int i = 0; i < 8 && !decided; i++) for (uint k = 0u; k < 4u && !decided; k++) { uint fb = (F[i] >> (8u * k)) & 255u, tb = (c.target[i] >> (8u * k)) & 255u; if (fb < tb) { decided = true; le = true; } else if (fb > tb) { decided = true; le = false; } }
  for (int i = 0; i < 8; i++) { outw[17u * a + uint(i)] = M[i]; outw[17u * a + 8u + uint(i)] = F[i]; } outw[17u * a + 16u] = le ? 1u : 0u;
  if (le) atomic_fetch_add_explicit(hits, 1u, memory_order_relaxed);
}
)MSL";
struct MetalCard19 {
    id<MTLDevice> mtl = nil; id<MTLCommandQueue> q = nil; id<MTLRenderPipelineState> pso = nil; id<MTLComputePipelineState> cps_gen = nil, cps_fold = nil, cps_hash = nil;
    id<MTLTexture> base = nil, atlas = nil; id<MTLBuffer> b_rec = nil, b_nonce = nil, b_seed = nil, b_acc = nil, b_out = nil, b_hits = nil, b_consts = nil;
    uint32_t side = 16384, cs = 32, cpr = 256, aw = 8192, ah = 0, t = 256, cells = 0; std::string params, device; R19Card card; void *inner = nullptr; const uint8_t *basep = nullptr;
    struct Consts { uint32_t n_att, t, side, reach, cs, cpr, aw, ah, chain_lo, chain_hi, version, pad0; uint32_t P[8]; uint32_t K[8]; uint32_t header[8]; uint32_t target[8]; };
    ~MetalCard19() { if (inner) ignh_inner_destroy(inner); }
    bool open(id<MTLDevice> dev, const IhParams &p, const std::string &doc, size_t batch_n, std::string &err) {
        mtl = dev; params = doc; device = [[mtl name] UTF8String]; side = r19_side(p); cs = r19_cell(p); t = p.rounds; cpr = aw / cs; cells = (uint32_t)batch_n * t; ah = ((cells + cpr - 1) / cpr) * cs;
        NSError *nerr = nil; id<MTLLibrary> lib = [mtl newLibraryWithSource:[NSString stringWithUTF8String:kM19Src] options:nil error:&nerr];
        if (!lib) { err = std::string("shader build: ") + (nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        MTLRenderPipelineDescriptor *pd = [[MTLRenderPipelineDescriptor alloc] init];
        pd.vertexFunction = [lib newFunctionWithName:@"r19_vs"]; pd.fragmentFunction = [lib newFunctionWithName:@"r19_fs"]; pd.rasterSampleCount = 1; pd.colorAttachments[0].pixelFormat = MTLPixelFormatRGBA8Unorm; pd.colorAttachments[0].blendingEnabled = NO;
        pso = [mtl newRenderPipelineStateWithDescriptor:pd error:&nerr]; if (!pso) { err = std::string("pipeline: ") + (nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        { NSError *ce = nil; id<MTLFunction> fg = [lib newFunctionWithName:@"r19_gen"], ff = [lib newFunctionWithName:@"r19_fold"], fh = [lib newFunctionWithName:@"r19_hash"];
          cps_gen = fg ? [mtl newComputePipelineStateWithFunction:fg error:&ce] : nil; cps_fold = ff ? [mtl newComputePipelineStateWithFunction:ff error:&ce] : nil; cps_hash = fh ? [mtl newComputePipelineStateWithFunction:fh error:&ce] : nil;
          if (!cps_gen || !cps_fold || !cps_hash) { err = std::string("compute pipeline: ") + (ce ? [[ce localizedDescription] UTF8String] : "?"); return false; } }
        MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatRGBA8Unorm width:side height:side mipmapped:NO]; td.usage = MTLTextureUsageShaderRead; td.storageMode = MTLStorageModeShared;
        base = [mtl newTextureWithDescriptor:td]; if (!base) { err = "the " + std::to_string(side) + " x " + std::to_string(side) + " base was refused"; return false; }
        { std::vector<uint8_t> z((size_t)1024 * side * 4, 0); for (uint32_t j0 = 0; j0 < side; j0 += 1024) [base replaceRegion:MTLRegionMake2D(0, j0, side, 1024) mipmapLevel:0 withBytes:z.data() bytesPerRow:(NSUInteger)side * 4]; }
        MTLTextureDescriptor *ta = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatRGBA8Unorm width:aw height:ah mipmapped:NO]; ta.usage = MTLTextureUsageRenderTarget | MTLTextureUsageShaderRead; ta.storageMode = MTLStorageModePrivate;
        atlas = [mtl newTextureWithDescriptor:ta]; if (!atlas) { err = "the atlas was refused"; return false; }
        q = [mtl newCommandQueue];
        b_rec = [mtl newBufferWithLength:(NSUInteger)cells * 64 options:MTLResourceStorageModeShared]; b_nonce = [mtl newBufferWithLength:(NSUInteger)batch_n * 8 options:MTLResourceStorageModeShared]; b_seed = [mtl newBufferWithLength:(NSUInteger)batch_n * 32 options:MTLResourceStorageModeShared];
        b_acc = [mtl newBufferWithLength:(NSUInteger)cells * 8 options:MTLResourceStorageModeShared]; b_out = [mtl newBufferWithLength:(NSUInteger)batch_n * 68 options:MTLResourceStorageModeShared]; b_hits = [mtl newBufferWithLength:64 options:MTLResourceStorageModeShared]; b_consts = [mtl newBufferWithLength:sizeof(Consts) options:MTLResourceStorageModeShared];
        card = R19Card(); card.epoch_filled = ~0ull;
        card.dataset = [this](uint64_t height, uint64_t &n, std::string &e) -> const uint8_t * {
            if (inner) { ignh_inner_destroy(inner); inner = nullptr; } int rc = 0; inner = ignh_inner_create(params.c_str(), height, 1, &rc); if (!inner) { e = "the reference refused the full context (status " + std::to_string(rc) + ")"; return nullptr; }
            const uint8_t *d = ignh_inner_dataset(inner, &n); if (!d) { e = "the reference gave no dataset"; return nullptr; } return d; };
        card.fill = [this](const uint8_t *d, uint64_t n) { if (n != (uint64_t)side * side * 4) return false; basep = d; for (uint32_t j0 = 0; j0 < side; j0 += 1024) [base replaceRegion:MTLRegionMake2D(0, j0, side, 1024) mipmapLevel:0 withBytes:d + (size_t)j0 * side * 4 bytesPerRow:(NSUInteger)side * 4]; return true; };
        const uint32_t reach = r19_reach(p), side_ = side, cs_ = cs, cpr_ = cpr, aw_ = aw, ah_ = ah, t_ = t; const uint64_t cid = p.chain_id; const uint32_t ver = p.version;
        card.full = [this, reach, side_, cs_, cpr_, aw_, ah_, t_, cid, ver, batch_n](const uint8_t *K, const uint8_t *P, const uint8_t *header, const std::vector<uint64_t> &nonces, const uint8_t *target, std::vector<std::array<uint8_t, 32>> &Ms, std::vector<std::array<uint8_t, 32>> &Fs, uint32_t &hits) -> bool {
            const uint32_t n = (uint32_t)nonces.size(); if (n == 0 || n > batch_n) { fprintf(stderr, "raster: a batch of %u attempts against %zu cells\n", n, batch_n); return false; }
            const uint32_t nbox = n * t_;
            Consts *c = (Consts *)[b_consts contents]; c->n_att = n; c->t = t_; c->side = side_; c->reach = reach; c->cs = cs_; c->cpr = cpr_; c->aw = aw_; c->ah = ah_; c->chain_lo = (uint32_t)cid; c->chain_hi = (uint32_t)(cid >> 32); c->version = ver; c->pad0 = 0;
            auto words = [](const uint8_t *b, uint32_t *w) { for (int i = 0; i < 8; i++) w[i] = (uint32_t)b[4 * i] | ((uint32_t)b[4 * i + 1] << 8) | ((uint32_t)b[4 * i + 2] << 16) | ((uint32_t)b[4 * i + 3] << 24); };
            words(P, c->P); words(K, c->K); words(header, c->header); words(target, c->target);
            uint32_t *nn = (uint32_t *)[b_nonce contents]; for (uint32_t i = 0; i < n; i++) { nn[2 * i] = (uint32_t)nonces[i]; nn[2 * i + 1] = (uint32_t)(nonces[i] >> 32); }
            *(uint32_t *)[b_hits contents] = 0;
            id<MTLCommandBuffer> cb = [q commandBuffer];
            { id<MTLComputeCommandEncoder> e = [cb computeCommandEncoder]; [e setComputePipelineState:cps_gen]; [e setBuffer:b_consts offset:0 atIndex:0]; [e setBuffer:b_rec offset:0 atIndex:1]; [e setBuffer:b_nonce offset:0 atIndex:4]; [e setBuffer:b_seed offset:0 atIndex:5];
              [e dispatchThreads:MTLSizeMake(n, 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [e endEncoding]; }
            { MTLRenderPassDescriptor *rp = [MTLRenderPassDescriptor renderPassDescriptor]; rp.colorAttachments[0].texture = atlas; rp.colorAttachments[0].loadAction = MTLLoadActionDontCare; rp.colorAttachments[0].storeAction = MTLStoreActionStore;
              id<MTLRenderCommandEncoder> e = [cb renderCommandEncoderWithDescriptor:rp]; [e setRenderPipelineState:pso]; [e setVertexBuffer:b_consts offset:0 atIndex:0]; [e setVertexBuffer:b_rec offset:0 atIndex:1]; [e setFragmentBuffer:b_rec offset:0 atIndex:1]; [e setFragmentTexture:base atIndex:0];
              [e drawPrimitives:MTLPrimitiveTypeTriangleStrip vertexStart:0 vertexCount:4 instanceCount:nbox]; [e endEncoding]; }
            { id<MTLComputeCommandEncoder> e = [cb computeCommandEncoder]; [e setComputePipelineState:cps_fold]; [e setBuffer:b_consts offset:0 atIndex:0]; [e setBuffer:b_rec offset:0 atIndex:1]; [e setBuffer:b_acc offset:0 atIndex:2]; [e setTexture:atlas atIndex:0];
              [e dispatchThreads:MTLSizeMake(nbox, 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [e endEncoding]; }
            { id<MTLComputeCommandEncoder> e = [cb computeCommandEncoder]; [e setComputePipelineState:cps_hash]; [e setBuffer:b_consts offset:0 atIndex:0]; [e setBuffer:b_acc offset:0 atIndex:2]; [e setBuffer:b_seed offset:0 atIndex:5]; [e setBuffer:b_out offset:0 atIndex:6]; [e setBuffer:b_hits offset:0 atIndex:7];
              [e dispatchThreads:MTLSizeMake(n, 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [e endEncoding]; }
            [cb commit]; [cb waitUntilCompleted]; if (cb.status != MTLCommandBufferStatusCompleted) { fprintf(stderr, "raster: the version 19 batch's command buffer did not complete (%ld)\n", (long)cb.status); return false; }
            Ms.resize(n); Fs.resize(n); const uint32_t *m = (const uint32_t *)[b_out contents];
            for (uint32_t a = 0; a < n; a++) for (int i = 0; i < 8; i++) { const uint32_t mw = m[17 * a + i], fw = m[17 * a + 8 + i]; for (int k = 0; k < 4; k++) { Ms[a][4 * i + k] = (uint8_t)(mw >> (8 * k)); Fs[a][4 * i + k] = (uint8_t)(fw >> (8 * k)); } }
            hits = *(const uint32_t *)[b_hits contents]; return true;
        };
        return true;
    }
};
// the raster-vectors and raster-bench verbs for version 19 (main.mm dispatches here on the document's version)
inline int metal_raster_vectors_v19(id<MTLDevice> mtl, const std::vector<std::string> &files, const IhParams &p, const std::string &params, double bench_s) {
    std::string err; const size_t B = (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 16);
    MetalCard19 mc; if (!mc.open(mtl, p, params, B, err)) { printf("{\"raster_worker\": {\"backend\": \"metal\", \"refused\": \"%s\"}}\n", err.c_str()); return 2; }
    if (bench_s > 0) {
        uint8_t P[32]; ih_P(p, P); uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); uint8_t K[32]; r18_key(p, 1, K, err); static const uint8_t zt[32] = {0};
        if (!r19_prepare(p, mc.card, 1, err)) { printf("{\"raster_worker\": {\"refused\": \"%s\"}}\n", err.c_str()); return 2; }
        auto t0 = std::chrono::steady_clock::now(); uint64_t attempts = 0; uint32_t hits_total = 0;
        while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s) {
            std::vector<uint64_t> nonces(B); for (size_t b = 0; b < B; b++) nonces[b] = attempts + b;
            std::vector<std::array<uint8_t, 32>> Ms, Fs; uint32_t hits = 0; if (!mc.card.full(K, P, header, nonces, zt, Ms, Fs, hits)) break; attempts += B; hits_total += hits;
        }
        const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        printf("{\"bench\": {\"device\": \"%s\", \"backend\": \"metal\", \"version\": 19, \"rounds\": %u, \"reach\": %u, \"side\": %u, \"keyed\": %u, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.4f, \"batch\": %zu, \"hits\": %u, \"note\": \"version 19 on Metal: the seed, the chain, the draw, the fold, the mix and the final on the card; one command buffer a batch\"}}\n", mc.device.c_str(), p.rounds, r19_reach(p), mc.side, p.dataset_bytes ? 1u : 0u, secs, (unsigned long long)attempts, attempts / secs / 1e6, B, hits_total); fflush(stdout);
        return 0;
    }
    return r19_vectors("metal", mc.device, p, files, mc.card, "Metal, version 19: the seed, the chain, one instanced draw a batch into the atlas, the fold, the mix and the final on the card", B);
}
}  // namespace igm

namespace igm {
// the protocol engine's version 19 (RasterMetalEngine::open19 and run_job19): the registered document (its anchor zero) draws P as the sealer
// does, the coordinate's anchor keys the period alone (the knob or the document's field), the job's header, target and nonce range through
// the full card path in batches, every hit a result with its M and F
inline bool RasterMetalEngine::open19(uint64_t height, const Knobs &k, Refusal &r) {
    std::string err; std::string doc0 = k.params; { Json j; if (Json::parse(k.params, j) && j.is_object()) { j.set("program_anchor", std::string(64, '0')); doc0 = j.dump(); } }
    const size_t B = (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 64);
    card19_.reset(new MetalCard19()); if (!card19_->open(mtl_, p_, doc0, B, err)) { r.code = "device"; r.explain = err; card19_.reset(); return false; }
    knobs_ = k; { IhParams p0 = p_; p0.program_anchor = std::string(64, '0'); ih_P(p0, P_); } apply_anchor();
    if (!r19_prepare(p_, card19_->card, height, err)) { r.code = "dataset"; r.explain = err; card19_.reset(); return false; }
    open_ = true; return true;
}
inline bool RasterMetalEngine::prepare19(uint64_t height, Refusal &r) { std::string err; if (!r19_prepare(p_, card19_->card, height, err)) { r.code = "dataset"; r.explain = err; return false; } return true; }
inline void RasterMetalEngine::close19() { card19_.reset(); }
inline uint64_t RasterMetalEngine::side19() const { return r19_side(p_); }
inline void RasterMetalEngine::run_job19(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    std::string err; if (!r19_prepare(p_, card19_->card, job.height, err)) { fprintf(stderr, "raster: %s\n", err.c_str()); return; }
    uint8_t K[32]; if (!r18_key(p_, job.height, K, err)) { fprintf(stderr, "raster: key: %s\n", err.c_str()); return; }
    const uint64_t end = job.nonce_start + job.nonce_count; uint64_t done = 0; auto t0 = std::chrono::steady_clock::now(), tp = t0;
    const uint64_t B = (uint64_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 64);
    for (uint64_t nonce = job.nonce_start; nonce < end && !should_stop(); nonce += B) { @autoreleasepool {
        const uint64_t n = (end - nonce) < B ? (end - nonce) : B; std::vector<uint64_t> nonces(n); for (uint64_t b = 0; b < n; b++) nonces[b] = nonce + b;
        std::vector<std::array<uint8_t, 32>> Ms, Fs; uint32_t hits = 0;
        if (!card19_->card.full(K, P_, job.header, nonces, job.target, Ms, Fs, hits)) { fprintf(stderr, "raster: the card refused a batch\n"); return; }
        if (hits) for (uint64_t b = 0; b < n; b++) if (std::memcmp(Fs[b].data(), job.target, 32) <= 0) { Result rr; rr.nonce = nonce + b; std::memcpy(rr.mix, Ms[b].data(), 32); std::memcpy(rr.digest, Fs[b].data(), 32); on_result(rr); if (job.seal) return; }
        done += n;
        const auto now = std::chrono::steady_clock::now();
        if (std::chrono::duration<double>(now - tp).count() >= 1.0) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(now - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); tp = now; done = 0; t0 = now; }
    } }
    if (done) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); }
}
}  // namespace igm
