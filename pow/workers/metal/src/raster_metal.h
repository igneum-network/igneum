// raster_metal.h: VERSION 18 (the raster form, 18b/18c) on Metal: the card (MetalCard: the pipeline, the RGBA8 target, the keyed base, the
// five R18Card callbacks of raster_worker.h) shared by the raster-vectors and raster-bench verbs (main.mm) and the protocol engine
// (RasterMetalEngine over EngineIface, the GPU team's shape of raster_worker_protocol.cpp: the registered document draws P and the dataset, the
// coordinate's anchor rides inside the configure document or on the r0d_anchor knob and keys the period alone, as the sealer verifies by
// ignh_hash_anchored). The command queue and the textures are owned by the card and used from the job's thread (Metal binds nothing to a
// thread). One attempt a pass: the draw, the boxes by getBytes, the boxes restored by a blit from the base texture (18c) or zeroed by
// replaceRegion (the first rule), the mix, the final against the job's share target. GPU engineer B, 15 October 2026 (the project's order 14:3x UK;
// the product team's igneum-worker-metal-coord6 is this binary under the protocol with a version-18 document).
#pragma once
#import <Foundation/Foundation.h>
#import <Metal/Metal.h>
#include "raster_worker.h"
#include "raster19.h"
#include "engine_iface.h"
#include "json.h"
#include <array>
#include <chrono>
#include <cstring>
#include <memory>
#include <string>
#include <vector>
extern "C" void *ignh_inner_create(const char *json, uint64_t height, int full, int *status);
extern "C" const uint8_t *ignh_inner_dataset(const void *ctx, uint64_t *bytes);
extern "C" void ignh_inner_destroy(void *ctx);

namespace igm {
struct MetalCard19;   // raster_metal_v19.h (version 19)
struct MetalCard {
    id<MTLDevice> mtl = nil; id<MTLRenderPipelineState> pso = nil; id<MTLTexture> fb = nil; id<MTLCommandQueue> q = nil;
    uint32_t side = 8192; float half = 4096.0f; std::string params, device;
    struct V { float x, y; uint8_t r, g, b, a; }; std::vector<V> verts; std::vector<uint8_t> zeros;
    R18Card card; const uint8_t *base = nullptr; void *inner = nullptr;
    id<MTLComputePipelineState> cps_digest = nil, cps_zero = nil, cps_keyed = nil;   // F-441: cps_keyed the share XOR   // THE BATCH (16 October 2026, the project's order 09:1x UK for Ember 0.2.22): the digest on the card (the MSL twin of the GL digest kernel) and the first rule's restore as a compute pass, so a batch of attempts runs in ONE command buffer
    ~MetalCard() { if (inner) ignh_inner_destroy(inner); }
    // the pipeline, the target (8192 or 16384 a side by the document), the callbacks; doc = the document the reference's context takes (the registered one)

    // F-441: an attempt's triangles drawn in segments into the command buffer, each keyed triangle's share XORed into its box's first eight
    // pixels (rp_keyed, one thread a pixel) between the segments, before its own segment: a render encoder a segment (load, store), a
    // compute encoder a keyed triangle; the first-rule and the 18c forms alike (the XOR on the target's word at that moment)
    bool encode_keyed_draw(id<MTLCommandBuffer> cb, id<MTLBuffer> vb, size_t vfirst, const std::vector<R18Tri> &tris) {
        auto segment = [&](size_t a, size_t b) { if (b <= a) return;
            MTLRenderPassDescriptor *rp = [MTLRenderPassDescriptor renderPassDescriptor];
            rp.colorAttachments[0].texture = fb; rp.colorAttachments[0].loadAction = MTLLoadActionLoad; rp.colorAttachments[0].storeAction = MTLStoreActionStore;
            id<MTLRenderCommandEncoder> enc = [cb renderCommandEncoderWithDescriptor:rp];
            [enc setRenderPipelineState:pso]; [enc setCullMode:MTLCullModeNone]; [enc setViewport:(MTLViewport){0, 0, (double)side, (double)side, 0, 1}];
            [enc setVertexBuffer:vb offset:(vfirst + 3 * a) * sizeof(V) atIndex:0]; [enc drawPrimitives:MTLPrimitiveTypeTriangle vertexStart:0 vertexCount:3 * (b - a)]; [enc endEncoding]; };
        size_t seg = 0;
        for (size_t t = 0; t < tris.size(); t++) {
            if (tris[t].keyed < 0) continue;
            if (!cps_keyed) return false;
            segment(seg, t);
            std::vector<uint32_t> pw; r18_keyed_pixels(tris[t], [&](int j, int32_t x, int32_t y) { pw.push_back((uint32_t)x); pw.push_back((uint32_t)y); pw.push_back(tris[t].kw[j]); });
            if (!pw.empty()) {
                id<MTLBuffer> pb = [mtl newBufferWithBytes:pw.data() length:pw.size() * 4 options:MTLResourceStorageModeShared];
                id<MTLComputeCommandEncoder> ke = [cb computeCommandEncoder];
                [ke setComputePipelineState:cps_keyed]; [ke setTexture:fb atIndex:0]; [ke setBuffer:pb offset:0 atIndex:1];
                [ke dispatchThreads:MTLSizeMake(pw.size() / 3, 1, 1) threadsPerThreadgroup:MTLSizeMake(8, 1, 1)]; [ke endEncoding];
            }
            seg = t;
        }
        segment(seg, tris.size());
        return true;
    }

    bool open(id<MTLDevice> dev, const IhParams &p, const std::string &doc, std::string &err) {
        mtl = dev; params = doc; side = r18_side(p); half = (float)(side / 2); device = std::string([[mtl name] UTF8String]);
        NSString *src = @"#include <metal_stdlib>\nusing namespace metal;\n"
            "struct VIn { float2 p [[attribute(0)]]; float4 c [[attribute(1)]]; };\n"
            "struct VOut { float4 pos [[position]]; float4 col; };\n"
            "vertex VOut rp_vs(VIn v [[stage_in]]) { VOut o; o.pos = float4(v.p, 0.0, 1.0); o.col = v.c; return o; }\n"
            "fragment float4 rp_fs(VOut i [[stage_in]]) { return i.col; }\n"
            // THE DIGEST ON THE CARD: the MSL twin of raster_gl_card.h's kDigestSrc (one thread a 1 KiB chunk of the box stream, the chunk's
            // chaining value out; the texture read top-down, no row flip on Metal); the boxes table 5 words a box (minx, miny, w, h, byte
            // offset) ending in a sentinel; pre[0..12] the 51 prefix bytes LE, pre[13] = 51, pre[14] = the total bytes, pre[15] = the boxes
            "constant uint RP_IV[8] = {0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u};\n"
            "constant uint RP_PERM[16] = {2u, 6u, 3u, 10u, 7u, 0u, 4u, 13u, 1u, 11u, 12u, 5u, 9u, 14u, 15u, 8u};\n"
            "static inline uint rp_rotr(uint x, uint n) { return (x >> n) | (x << (32u - n)); }\n"
            "static inline void rp_g(thread uint *v, uint a, uint b, uint c, uint d, uint x, uint y) { v[a] = v[a] + v[b] + x; v[d] = rp_rotr(v[d] ^ v[a], 16u); v[c] = v[c] + v[d]; v[b] = rp_rotr(v[b] ^ v[c], 12u); v[a] = v[a] + v[b] + y; v[d] = rp_rotr(v[d] ^ v[a], 8u); v[c] = v[c] + v[d]; v[b] = rp_rotr(v[b] ^ v[c], 7u); }\n"
            "static inline void rp_compress(thread uint *cv, thread uint *m, uint t, uint blen, uint flags) {\n"
            "  uint v[16]; for (int i = 0; i < 8; i++) v[i] = cv[i]; v[8] = RP_IV[0]; v[9] = RP_IV[1]; v[10] = RP_IV[2]; v[11] = RP_IV[3]; v[12] = t; v[13] = 0u; v[14] = blen; v[15] = flags;\n"
            "  for (int r = 0; r < 7; r++) { rp_g(v,0u,4u,8u,12u,m[0],m[1]); rp_g(v,1u,5u,9u,13u,m[2],m[3]); rp_g(v,2u,6u,10u,14u,m[4],m[5]); rp_g(v,3u,7u,11u,15u,m[6],m[7]); rp_g(v,0u,5u,10u,15u,m[8],m[9]); rp_g(v,1u,6u,11u,12u,m[10],m[11]); rp_g(v,2u,7u,8u,13u,m[12],m[13]); rp_g(v,3u,4u,9u,14u,m[14],m[15]);\n"
            "    if (r < 6) { uint pm[16]; for (int i = 0; i < 16; i++) pm[i] = m[RP_PERM[i]]; for (int i = 0; i < 16; i++) m[i] = pm[i]; } }\n"
            "  for (int i = 0; i < 8; i++) cv[i] = v[i] ^ v[i + 8];\n"
            "}\n"
            "static inline uint rp_stream_byte(texture2d<float, access::read> fb, device const uint *box, device const uint *pre, uint o) {\n"
            "  uint plen = pre[13]; if (o < plen) return (pre[o >> 2] >> (8u * (o & 3u))) & 255u;\n"
            "  uint rel = o - plen; uint nb = pre[15]; uint lo = 0u, hi = nb;\n"
            "  while (hi - lo > 1u) { uint mid = (lo + hi) >> 1u; if (box[5u * mid + 4u] <= rel) lo = mid; else hi = mid; }\n"
            "  uint minx = box[5u * lo], miny = box[5u * lo + 1u], w = box[5u * lo + 2u]; uint r = rel - box[5u * lo + 4u];\n"
            "  uint row = r / (w * 4u), c = r - row * (w * 4u); uint x = minx + (c >> 2u), y = miny + row;\n"
            "  uint4 px = uint4(round(fb.read(uint2(x, y)) * 255.0));\n"
            "  uint k = c & 3u; return k == 0u ? px.r : k == 1u ? px.g : k == 2u ? px.b : px.a;\n"
            "}\n"
            "kernel void rp_digest(texture2d<float, access::read> fb [[texture(0)]], device const uint *box [[buffer(1)]], device const uint *pre [[buffer(2)]], device uint *cvout [[buffer(3)]], uint chunk [[thread_position_in_grid]]) {\n"
            "  uint total = pre[14]; uint start = chunk * 1024u; if (start >= total) return;\n"
            "  uint len = min(1024u, total - start); uint cv[8]; for (int i = 0; i < 8; i++) cv[i] = RP_IV[i];\n"
            "  uint nblk = (len + 63u) / 64u; if (nblk == 0u) nblk = 1u;\n"
            "  for (uint b = 0u; b < nblk; b++) { uint m[16]; uint bo = start + b * 64u; uint blen = min(64u, len - b * 64u);\n"
            "    for (int i = 0; i < 16; i++) { uint wv = 0u; for (uint q = 0u; q < 4u; q++) { uint o = uint(i) * 4u + q; if (o < blen) wv |= rp_stream_byte(fb, box, pre, bo + o) << (8u * q); } m[i] = wv; }\n"
            "    uint flags = (b == 0u ? 1u : 0u) | (b + 1u == nblk ? 2u : 0u); rp_compress(cv, m, chunk, blen, flags); }\n"
            "  for (int i = 0; i < 8; i++) cvout[8u * chunk + uint(i)] = cv[i];\n"
            "}\n"
            // THE FIRST RULE'S RESTORE ON THE CARD: the boxes zeroed by a compute pass (one thread a box), inside the batch's command buffer
            "kernel void rp_zero(texture2d<float, access::write> fb [[texture(0)]], device const uint *box [[buffer(1)]], constant uint &nb [[buffer(2)]], uint t [[thread_position_in_grid]]) {\n"
            "  if (t >= nb) return; uint minx = box[5u * t], miny = box[5u * t + 1u], w = box[5u * t + 2u], h = box[5u * t + 3u];\n"
            "  for (uint y = 0u; y < h; y++) for (uint x = 0u; x < w; x++) fb.write(float4(0.0), uint2(minx + x, miny + y));\n"
            "}\n"
            "kernel void rp_keyed(texture2d<float, access::read_write> fb [[texture(0)]], device const uint *pw [[buffer(1)]], uint id [[thread_position_in_grid]]) {\n"
            "  uint x = pw[3 * id], y = pw[3 * id + 1], w = pw[3 * id + 2]; float4 c = fb.read(uint2(x, y)); uint4 b = uint4(round(c * 255.0));\n"
            "  uint cur = b.r | (b.g << 8) | (b.b << 16) | (b.a << 24); cur ^= w; fb.write(float4(float(cur & 255u), float((cur >> 8) & 255u), float((cur >> 16) & 255u), float((cur >> 24) & 255u)) / 255.0, uint2(x, y));\n"
            "}\n";   // F-441: the keyed share XORed into the box's first eight pixels on the card, BEFORE the triangle is drawn (the unorm8 texel back to its byte: k/255 rounds to k exactly)
        NSError *nerr = nil; id<MTLLibrary> lib = [mtl newLibraryWithSource:src options:nil error:&nerr];
        if (!lib) { err = "shader build"; return false; }
        MTLVertexDescriptor *vd = [MTLVertexDescriptor vertexDescriptor];
        vd.attributes[0].format = MTLVertexFormatFloat2; vd.attributes[0].offset = 0; vd.attributes[0].bufferIndex = 0;
        vd.attributes[1].format = MTLVertexFormatUChar4Normalized; vd.attributes[1].offset = 8; vd.attributes[1].bufferIndex = 0;
        vd.layouts[0].stride = 12; vd.layouts[0].stepFunction = MTLVertexStepFunctionPerVertex;
        MTLRenderPipelineDescriptor *pd = [[MTLRenderPipelineDescriptor alloc] init];
        pd.vertexFunction = [lib newFunctionWithName:@"rp_vs"]; pd.fragmentFunction = [lib newFunctionWithName:@"rp_fs"]; pd.vertexDescriptor = vd;
        pd.rasterSampleCount = 1; pd.colorAttachments[0].pixelFormat = MTLPixelFormatRGBA8Unorm;
        pd.colorAttachments[0].blendingEnabled = YES; pd.colorAttachments[0].rgbBlendOperation = MTLBlendOperationAdd; pd.colorAttachments[0].alphaBlendOperation = MTLBlendOperationAdd;
        pd.colorAttachments[0].sourceRGBBlendFactor = MTLBlendFactorOne; pd.colorAttachments[0].destinationRGBBlendFactor = MTLBlendFactorOne;
        pd.colorAttachments[0].sourceAlphaBlendFactor = MTLBlendFactorOne; pd.colorAttachments[0].destinationAlphaBlendFactor = MTLBlendFactorOne;
        pso = [mtl newRenderPipelineStateWithDescriptor:pd error:&nerr];
        if (!pso) { err = std::string("pipeline: ") + (nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatRGBA8Unorm width:side height:side mipmapped:NO];
        td.usage = MTLTextureUsageRenderTarget | MTLTextureUsageShaderRead | MTLTextureUsageShaderWrite; td.storageMode = MTLStorageModeShared;
        fb = [mtl newTextureWithDescriptor:td]; if (!fb) { err = "the " + std::to_string(side) + " x " + std::to_string(side) + " render target was refused"; return false; }
        { std::vector<uint8_t> z((size_t)1024 * side * 4, 0); for (uint32_t j0 = 0; j0 < side; j0 += 1024) [fb replaceRegion:MTLRegionMake2D(0, j0, side, 1024) mipmapLevel:0 withBytes:z.data() bytesPerRow:(NSUInteger)side * 4]; }
        q = [mtl newCommandQueue]; zeros.assign((size_t)32 * 32 * 4, 0);
        { NSError *ce = nil; id<MTLFunction> fd = [lib newFunctionWithName:@"rp_digest"], fz = [lib newFunctionWithName:@"rp_zero"];
          cps_digest = fd ? [mtl newComputePipelineStateWithFunction:fd error:&ce] : nil; cps_zero = fz ? [mtl newComputePipelineStateWithFunction:fz error:&ce] : nil;
          { id<MTLFunction> fk = [lib newFunctionWithName:@"rp_keyed"]; cps_keyed = fk ? [mtl newComputePipelineStateWithFunction:fk error:&ce] : nil; if (!cps_keyed) fprintf(stderr, "raster: the keyed XOR compute pipeline was refused on Metal: a keyed document cannot run\n"); }
          if (!cps_digest || !cps_zero) fprintf(stderr, "raster: the digest or zero compute pipeline was refused on Metal (%s): the host digests, one attempt a pass\n", ce ? [[ce localizedDescription] UTF8String] : "?"); }
        card = R18Card(); card.epoch_filled = ~0ull; base = nullptr;
        card.dataset = [this](uint64_t height, uint64_t &n, std::string &e) -> const uint8_t * {
            if (inner) { ignh_inner_destroy(inner); inner = nullptr; }
            int st = 0; inner = ignh_inner_create(params.c_str(), height, 1, &st); if (!inner) { e = "the reference refused the full context (status " + std::to_string(st) + ")"; return nullptr; }
            const uint8_t *d = ignh_inner_dataset(inner, &n); if (!d) { e = "the reference gave no dataset"; return nullptr; } return d;
        };
        // 18c: the target and a base copy on the card (a second texture, the restore a blit from it), the dataset's rows the target's rows, 1024 rows a replaceRegion
        card.fill = [this](const uint8_t *d, uint64_t n) {
            if (n != (uint64_t)side * side * 4) return false; base = d;
            for (uint32_t j0 = 0; j0 < side; j0 += 1024) [fb replaceRegion:MTLRegionMake2D(0, j0, side, 1024) mipmapLevel:0 withBytes:d + (size_t)j0 * side * 4 bytesPerRow:(NSUInteger)side * 4];
            return true;   // no base texture: the blit restore (a second command buffer an attempt) read 40 attempts a second against 212 by replaceRegion from the host copy on unified memory (coord6-1, 15 October 2026 16:0x UK)
        };
        card.draw = [this](const std::vector<R18Tri> &tris) {
            verts.resize(tris.size() * 3);
            for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2};
                for (int k = 0; k < 3; k++) { V &v = verts[t * 3 + k]; v.x = (float)xs[k] / half - 1.0f; v.y = 1.0f - (float)ys[k] / half; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); } }
            id<MTLBuffer> vb = [mtl newBufferWithBytes:verts.data() length:verts.size() * sizeof(V) options:MTLResourceStorageModeShared];
            id<MTLCommandBuffer> cb = [q commandBuffer];
            if (!encode_keyed_draw(cb, vb, 0, tris)) return false;   // F-441: the segments and the keyed shares' XOR between them
            [cb commit]; [cb waitUntilCompleted];
            return [cb status] == MTLCommandBufferStatusCompleted;
        };
        card.read_boxes = [this](const std::vector<R18Tri> &tris, std::vector<std::vector<uint8_t>> &boxes) {
            boxes.assign(tris.size(), {});
            for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
                boxes[t].resize((size_t)w * h * 4); [fb getBytes:boxes[t].data() bytesPerRow:(NSUInteger)w * 4 fromRegion:MTLRegionMake2D(tr.minx, tr.miny, w, h) mipmapLevel:0]; }
            return true;
        };
        // the restore: the boxes back from the host copy by replaceRegion (unified memory: the cheapest path, 212 attempts a second on the M6); the first rule zeroes the same way
        card.restore = [this](const std::vector<R18Tri> &tris) {
            for (const R18Tri &tr : tris) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
                if (base) [fb replaceRegion:MTLRegionMake2D(tr.minx, tr.miny, w, h) mipmapLevel:0 withBytes:base + ((size_t)tr.miny * side + (size_t)tr.minx) * 4 bytesPerRow:(NSUInteger)side * 4];
                else [fb replaceRegion:MTLRegionMake2D(tr.minx, tr.miny, w, h) mipmapLevel:0 withBytes:zeros.data() bytesPerRow:(NSUInteger)w * 4]; }
            return true;
        };
        // THE DIGEST ON THE CARD and THE BATCH (16 October 2026): the GL card's forms (raster_gl_card.h) on Metal. The tables of a batch of M
        // attempts: per attempt 5 words a box plus the sentinel (padded to 256 bytes), the 16-word pre block (padded to 256), the out slice
        // by chunk count (256-byte aligned); one command buffer holds every attempt's render pass, digest pass and (the first rule) zeroing
        // pass; one wait and one read a batch; the M roots merged on the host from the chunk values. 18c (the keyed base) is not batched
        // here: its restore is replaceRegion from the host copy, a CPU write the command buffer cannot order, so r18_attempts falls back to
        // one attempt a pass on the keyed document (the GL card batches 18c through its base texture; the Metal twin of that is a later cut).
        if (cps_digest && cps_zero) {
            card.batch = [this](const std::vector<std::vector<R18Tri>> &attempts, const uint8_t *K, std::vector<std::array<uint8_t, 32>> &Ms) -> bool {
                const size_t M = attempts.size(); Ms.assign(M, std::array<uint8_t, 32>{});
                // one attempt a pass on the host path (the draw, the boxes by getBytes, the mix on the host, the restore by replaceRegion): the
                // keyed document (its restore is a host write) and a one-chunk stream (every box empty, rare; a single chunk's root is not its
                // chaining value) go this way; r18_attempts takes the batch's verdict as the window's, so the batch never refuses what it can do one a pass
                auto one = [this](const std::vector<R18Tri> &tris, const uint8_t *K, uint8_t *Mo) -> bool {
                    if (!card.draw(tris)) return false; std::vector<std::vector<uint8_t>> boxes; if (!card.read_boxes(tris, boxes)) return false; r18_mix(K, boxes, Mo); return card.restore(tris); };
                if (base) { for (size_t a = 0; a < M; a++) if (!one(attempts[a], K, Ms[a].data())) return false; return true; }
                uint8_t prefix[64] = {0}; const char *tag = "IgneumHash/R0I/mix"; const uint32_t plen = 1u + 18u + 32u; prefix[0] = 18; std::memcpy(prefix + 1, tag, 18); std::memcpy(prefix + 19, K, 32);
                std::vector<uint32_t> tables, pres; std::vector<size_t> toff(M), poff(M), ooff(M), nch(M), nbox(M); std::vector<bool> single(M, false);
                std::vector<V> vs; std::vector<size_t> voff(M), vcount(M); size_t outw = 0;
                for (size_t a = 0; a < M; a++) {
                    const std::vector<R18Tri> &tris = attempts[a]; const uint32_t nb = (uint32_t)tris.size(); nbox[a] = nb;
                    voff[a] = vs.size();
                    for (const R18Tri &tr : tris) { const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2}; for (int k = 0; k < 3; k++) { V v; v.x = (float)xs[k] / half - 1.0f; v.y = 1.0f - (float)ys[k] / half; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); vs.push_back(v); } }
                    vcount[a] = vs.size() - voff[a];
                    toff[a] = tables.size(); uint32_t off = 0;
                    for (uint32_t t = 0; t < nb; t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; tables.push_back((uint32_t)tr.minx); tables.push_back((uint32_t)tr.miny); tables.push_back((uint32_t)(w > 0 ? w : 0)); tables.push_back((uint32_t)(h > 0 ? h : 0)); tables.push_back(off); if (w > 0 && h > 0) off += (uint32_t)(w * h * 4); }
                    for (int qq = 0; qq < 4; qq++) tables.push_back(0u); tables.push_back(off);
                    while ((tables.size() * 4) % 256) tables.push_back(0u);
                    const uint32_t total = plen + off; nch[a] = (total + 1023u) / 1024u; single[a] = nch[a] < 2;
                    poff[a] = pres.size(); uint32_t pre[16] = {0}; for (uint32_t i = 0; i < plen; i++) pre[i >> 2] |= (uint32_t)prefix[i] << (8u * (i & 3u)); pre[13] = plen; pre[14] = total; pre[15] = nb;
                    for (int i = 0; i < 16; i++) pres.push_back(pre[i]); for (int i = 0; i < 48; i++) pres.push_back(0u);
                    ooff[a] = outw; outw += ((nch[a] * 8 + 63) / 64) * 64;
                }
                size_t batched = 0; for (size_t a = 0; a < M; a++) { if (single[a]) { if (!one(attempts[a], K, Ms[a].data())) return false; } else batched++; }   // the one-chunk streams first, on the host path (the target is at its base between attempts)
                if (!batched) return true;
                id<MTLBuffer> vb = [mtl newBufferWithBytes:vs.data() length:vs.size() * sizeof(V) options:MTLResourceStorageModeShared];
                id<MTLBuffer> bt = [mtl newBufferWithBytes:tables.data() length:tables.size() * 4 options:MTLResourceStorageModeShared];
                id<MTLBuffer> bp = [mtl newBufferWithBytes:pres.data() length:pres.size() * 4 options:MTLResourceStorageModeShared];
                id<MTLBuffer> bo = [mtl newBufferWithLength:(outw ? outw * 4 : 64) options:MTLResourceStorageModeShared];
                if (!vb || !bt || !bp || !bo) return false;
                id<MTLCommandBuffer> cb = [q commandBuffer];
                for (size_t a = 0; a < M; a++) { if (single[a]) continue;
                    if (!encode_keyed_draw(cb, vb, voff[a], attempts[a])) return false;   // F-441: the segments and the keyed shares' XOR between them
                    {
                        id<MTLComputeCommandEncoder> ce = [cb computeCommandEncoder];
                        [ce setComputePipelineState:cps_digest]; [ce setTexture:fb atIndex:0]; [ce setBuffer:bt offset:toff[a] * 4 atIndex:1]; [ce setBuffer:bp offset:poff[a] * 4 atIndex:2]; [ce setBuffer:bo offset:ooff[a] * 4 atIndex:3];
                        [ce dispatchThreads:MTLSizeMake(nch[a], 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [ce endEncoding];
                    }
                    {   // the first rule's restore: the boxes zeroed on the card
                        id<MTLComputeCommandEncoder> ze = [cb computeCommandEncoder]; uint32_t nb32 = (uint32_t)nbox[a];
                        [ze setComputePipelineState:cps_zero]; [ze setTexture:fb atIndex:0]; [ze setBuffer:bt offset:toff[a] * 4 atIndex:1]; [ze setBytes:&nb32 length:4 atIndex:2];
                        [ze dispatchThreads:MTLSizeMake(nb32 ? nb32 : 1, 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [ze endEncoding];
                    }
                }
                [cb commit]; [cb waitUntilCompleted]; if ([cb status] != MTLCommandBufferStatusCompleted) return false;
                const uint32_t *m = (const uint32_t *)[bo contents];
                for (size_t a = 0; a < M; a++) if (!single[a]) ih_blake3_from_chunk_cvs(m + ooff[a], nch[a], Ms[a].data());
                return true;
            };
        }
        return true;
    }
};

// the protocol engine: igneumhash version 18 alone; the hello's device is the Metal device (backend "metal", vendor "apple", id = the registryID)
class RasterMetalEngine : public EngineIface {
public:
    explicit RasterMetalEngine(id<MTLDevice> dev) : mtl_(dev) {}
    std::string backend() const override { return "metal"; }
    Json device_json() const override {
        const std::string name = std::string([[mtl_ name] UTF8String]), rid = std::to_string((unsigned long long)[mtl_ registryID]);
        return Json::object().set("index", 0).set("backend", "metal").set("name", name).set("vendor", "apple").set("id", rid)
            .set("memory_total", (double)[mtl_ recommendedMaxWorkingSetSize]).set("memory_free", (double)metal_free_bytes()).set("compute", "raster").set("driver", "metal").set("fingerprint", "metal-raster").set("identity", "metal-raster:" + rid).set("intrinsic_id", rid);
    }
    Json knob_decls() const override { return Json::array(); }
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t, Refusal &r) override {
        close();
        if (algo != "igneumhash") { r.code = "algo"; r.explain = "the raster engine runs igneumhash versions 18 and 19 only"; return false; }
        std::string err; if (!ih_parse_params(k.params, p_, err)) { r.code = "params"; r.explain = "params: " + err; return false; }
        if (p_.version == 19) return open19(height, k, r);
        if (p_.version != 18) { r.code = "params"; r.explain = "version " + std::to_string(p_.version) + " is not a raster form (18 or 19)"; return false; }
        // the REGISTERED document (its anchor field zero) draws the dataset and P, as the sealer does; the coordinate's anchor keys the period alone
        std::string doc0 = k.params; { Json j; if (Json::parse(k.params, j) && j.is_object()) { j.set("program_anchor", std::string(64, '0')); doc0 = j.dump(); } }
        card_.reset(new MetalCard()); if (!card_->open(mtl_, p_, doc0, err)) { r.code = "device"; r.explain = err; card_.reset(); return false; }
        knobs_ = k; { IhParams p0 = p_; p0.program_anchor = std::string(64, '0'); ih_P(p0, P_); } apply_anchor();
        if (!r18_prepare(p_, card_->card, height, err)) { r.code = "dataset"; r.explain = err; card_.reset(); return false; }
        open_ = true; return true;
    }
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override {
        if (!open_) { r.code = "state"; r.explain = "not configured"; return false; }
        if (!params.empty() && params != knobs_.params) { r.code = "params"; r.explain = "a parameter change ahead is not warmed by the raster engine; reconfigure at the height"; return false; }
        if (card19_) return prepare19(height, r);
        std::string err; if (!r18_prepare(p_, card_->card, height, err)) { r.code = "dataset"; r.explain = err; return false; } return true;
    }
    void close() override { card_.reset(); close19(); open_ = false; }
    bool open19(uint64_t height, const Knobs &k, Refusal &r);   // raster_metal_v19.h: version 19 onto MetalCard19 (the type is complete there)
    bool prepare19(uint64_t height, Refusal &r); void close19(); uint64_t side19() const;
    void run_job19(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop);
    void apply_anchor() { std::string a = knobs_.r0d_anchor; if (a.rfind("0x", 0) == 0) a = a.substr(2); if (a.size() == 64) p_.program_anchor = a; }
    void set_knobs(const Knobs &k) override { knobs_ = k; apply_anchor(); }
    Knobs knobs() const override { return knobs_; }
    // F-438 (17 October 2026 01:3x UK): the hello and the budget reported memory_free and free_bytes as the literal 0, so Ember printed
    // "ready, 0.00 GB free of 11.84 GB" for a worker that had allocated and was mining; the figure is the device's recommended working
    // set less what it has allocated now (floored at zero), the same reading the compute worker gives
    uint64_t metal_free_bytes() const { const uint64_t t = (uint64_t)[mtl_ recommendedMaxWorkingSetSize], a = (uint64_t)[mtl_ currentAllocatedSize]; return t > a ? t - a : 0; }
    uint64_t allocated_bytes() const override { return open_ ? (card19_ ? side19() * side19() * 4 : (uint64_t)card_->side * card_->side * 4) + p_.dataset_bytes : 0; }
    uint64_t dataset_bytes() const override { return p_.dataset_bytes; }
    Json budget_json() const override { return Json::object().set("total_bytes", (double)[mtl_ recommendedMaxWorkingSetSize]).set("free_bytes", (double)metal_free_bytes()).set("fit_cold_bytes", 0).set("fit_warm_bytes", 0).set("can_warm", false); }
    void telemetry(Json &) const override {}
    bool paused() const override { return false; }
    bool keyed_document() const override { return (p_.features & 1024u) != 0; }   // F-441: a keyed raster document (bit 10): the job needs sk, the loop's custody guard, self-check and seal verb engage
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) override {
        if (!open_) return;
        if (card19_) { run_job19(job, on_result, on_progress, should_stop); return; }
        std::string err; if (!r18_prepare(p_, card_->card, job.height, err)) { fprintf(stderr, "raster: %s\n", err.c_str()); return; }
        uint8_t K[32]; if (!r18_key(p_, job.height, K, err)) { fprintf(stderr, "raster: key: %s\n", err.c_str()); return; }
        const uint64_t end = job.nonce_start + job.nonce_count; uint64_t done = 0; auto t0 = std::chrono::steady_clock::now(), tp = t0;
        const uint64_t B = card_->card.batch ? (uint64_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 16) : 1;   // attempts in flight a batch (the GL worker's knob), one a pass where the card has no batch
        for (uint64_t nonce = job.nonce_start; nonce < end && !should_stop(); nonce += B) { @autoreleasepool {
            const uint64_t n = (end - nonce) < B ? (end - nonce) : B;
            std::vector<std::vector<R18Tri>> batch(n); for (uint64_t b = 0; b < n; b++) { uint8_t S[32]; r18_seed(p_, P_, job.header, nonce + b, S); batch[b] = r18_triangles(p_, K, S, (keyed_document() && job.has_sk) ? job.sk : nullptr); }   // F-441: the secret into the keyed walk
            std::vector<std::array<uint8_t, 32>> Ms; if (!r18_attempts(card_->card, batch, K, Ms)) { fprintf(stderr, "raster: the card refused an attempt\n"); return; }
            for (uint64_t b = 0; b < n; b++) {
                uint8_t H[32]; ih_final_digest(p_.chain_id, p_.version, P_, job.header, nonce + b, Ms[b].data(), H);
                if (std::memcmp(H, job.target, 32) <= 0) { Result rr; rr.nonce = nonce + b; std::memcpy(rr.mix, Ms[b].data(), 32); std::memcpy(rr.digest, H, 32); on_result(rr); if (job.seal) return; }
            }
            done += n;
            const auto now = std::chrono::steady_clock::now();
            if (std::chrono::duration<double>(now - tp).count() >= 1.0) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(now - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); tp = now; done = 0; t0 = now; }
        } }
        if (done) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); }
    }
    std::string describe() const override { if (card19_) return "metal raster, version 19: the seed, the chain, one instanced draw a batch into the atlas, the fold, the mix and the final on the card (one command buffer a batch)"; return std::string(card_ && card_->card.batch && !p_.dataset_bytes ? "metal raster: attempts in flight a batch (one command buffer a batch: the draw, the digest on the card, the boxes zeroed on the card)" : "metal raster: one attempt a pass, the draw, the boxes by getBytes, the boxes restored by replaceRegion from the host copy") + std::string(p_.dataset_bytes ? " (18c, the keyed target)" : " (the first rule)"); }
private:
    id<MTLDevice> mtl_; std::unique_ptr<MetalCard> card_; std::unique_ptr<MetalCard19> card19_; IhParams p_; Knobs knobs_; uint8_t P_[32] = {0}; bool open_ = false;
    friend struct MetalCard19;
};
}  // namespace igm
