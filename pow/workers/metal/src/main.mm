// main.mm: igneum-worker-metal. The main thread runs the run loop for the system
// observers; the protocol loop (or the vectors run) runs on its own thread.
// GPU engineer B, 9 October 2026. Source only; not built, not run.
#import <Foundation/Foundation.h>
#import <Metal/Metal.h>
#include "device.h"
#include "engine.h"
#include "engine_x.h"
#include "multi_engine.h"
#include "raster_metal.h"
#include "raster_metal_v19.h"
#include "protocol.h"
#include "vectors_main.h"
#include "vectors.h"
#include "bench_main.h"
#include "seams.h"
#include "epochs.h"
#include "ignpow.h"
#include "igneumhash.h"
#include <algorithm>
#include <execinfo.h>
#include <csignal>
#include <unistd.h>
#include "ih_params.h"
#include "texture_probe.h"
#include "raster_probe.h"
#include "raster_worker.h"
#include <libgen.h>
#include <mach-o/dyld.h>
#include <fstream>
#include <sstream>
#include "sha256.h"
#include <string>
#include <thread>
#include <vector>
#include <cstring>
#include <array>
#include <chrono>

using namespace igm;

static std::string exe_dir() {
    char buf[4096]; uint32_t n = sizeof buf;
    if (_NSGetExecutablePath(buf, &n) != 0) return ".";
    return std::string(dirname(buf));
}
// Orchard workstream 1: the worker binary's own sha256, on every row through the fingerprint block (IGM_WORKER_SHA256)
static std::string exe_sha256() {
    char buf[4096]; uint32_t n = sizeof buf;
    if (_NSGetExecutablePath(buf, &n) != 0) return "unrecorded";
    std::ifstream f(buf, std::ios::binary); if (!f) return "unrecorded";
    std::stringstream ss; ss << f.rdbuf(); return sha256_hex(ss.str());
}


// The texture determinism probe (14 Oct 2026, the project's gate for the texture discipline; texture_probe.h holds the five shared lines):
// a 4096 x 4096 R32Uint texture sampled 1,000,000 times with filter::nearest at the texel centre (coord::pixel) through the
// texture unit, one thread per sample; the card's digest beside the host's.
static int metal_texture_probe(id<MTLDevice> mtl) {
    using namespace igm;
    std::vector<uint32_t> tex = tp_texture();
    MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatR32Uint width:kTpWidth height:kTpHeight mipmapped:NO];
    td.usage = MTLTextureUsageShaderRead; td.storageMode = MTLStorageModeShared;
    id<MTLTexture> t = [mtl newTextureWithDescriptor:td];
    if (!t) { printf("{\"texture_probe\": {\"backend\": \"metal\", \"refused\": \"the 64 MiB R32Uint texture was refused\"}}\n"); return 2; }
    [t replaceRegion:MTLRegionMake2D(0, 0, kTpWidth, kTpHeight) mipmapLevel:0 withBytes:tex.data() bytesPerRow:(NSUInteger)kTpWidth * 4];
    NSString *src = @"#include <metal_stdlib>\nusing namespace metal;\n"
        "uint tp_fmix(uint h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }\n"
        "kernel void tp_probe(texture2d<uint, access::sample> img [[texture(0)]], device uint *out [[buffer(0)]], constant uint &n [[buffer(1)]], uint i [[thread_position_in_grid]]) {\n"
        "    if (i >= n) return;\n"
        "    constexpr sampler tp_smp(coord::pixel, filter::nearest, address::clamp_to_edge);\n"
        "    const uint s = tp_fmix(i * 0x9E3779B1u + 1u);\n"
        "    const float2 c = float2(float(s & 4095u) + 0.5f, float((s >> 12) & 4095u) + 0.5f);\n"
        "    out[i] = img.sample(tp_smp, c).x;\n"
        "}\n";
    NSError *err = nil; id<MTLLibrary> lib = [mtl newLibraryWithSource:src options:nil error:&err];
    if (!lib) { printf("{\"texture_probe\": {\"backend\": \"metal\", \"refused\": \"shader build: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return 2; }
    id<MTLFunction> fn = [lib newFunctionWithName:@"tp_probe"]; id<MTLComputePipelineState> pso = [mtl newComputePipelineStateWithFunction:fn error:&err];
    if (!pso) { printf("{\"texture_probe\": {\"backend\": \"metal\", \"refused\": \"pipeline: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return 2; }
    id<MTLBuffer> out = [mtl newBufferWithLength:(NSUInteger)kTpSamples * 4 options:MTLResourceStorageModeShared];
    uint32_t ns = kTpSamples; id<MTLBuffer> nb = [mtl newBufferWithBytes:&ns length:4 options:MTLResourceStorageModeShared];
    id<MTLCommandQueue> q = [mtl newCommandQueue]; id<MTLCommandBuffer> cb = [q commandBuffer]; id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
    [enc setComputePipelineState:pso]; [enc setTexture:t atIndex:0]; [enc setBuffer:out offset:0 atIndex:0]; [enc setBuffer:nb offset:0 atIndex:1];
    [enc dispatchThreads:MTLSizeMake(kTpSamples, 1, 1) threadsPerThreadgroup:MTLSizeMake(256, 1, 1)]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
    if ([cb status] != MTLCommandBufferStatusCompleted) { printf("{\"texture_probe\": {\"backend\": \"metal\", \"refused\": \"the command buffer did not complete\"}}\n"); return 2; }
    std::vector<uint32_t> card(kTpSamples); std::memcpy(card.data(), [out contents], (size_t)kTpSamples * 4);
    return tp_report("metal", std::string([[mtl name] UTF8String]), card, "texture2d<uint, access::sample>.sample, sampler(coord::pixel, filter::nearest, address::clamp_to_edge), float2 texel centre, 256 threads a group");
}

// The raster determinism probe (14 Oct 2026, the project's RASTER gate; raster_probe.h holds the shared rule): an 8192 x 8192 RGBA8 render target,
// the 1,000,000 triangles in one draw (positions the NDC float2 of the integer corners with y = 1 - y / 4096, the colour four normalised
// bytes), blend ONE + ONE ADD, no depth, no multisampling, cull none; the texture read back row 0 the top; then the cost loop.
static int metal_raster_probe(id<MTLDevice> mtl, double loop_s) {
    using namespace igm;
    NSString *src = @"#include <metal_stdlib>\nusing namespace metal;\n"
        "struct VIn { float2 p [[attribute(0)]]; float4 c [[attribute(1)]]; };\n"
        "struct VOut { float4 pos [[position]]; float4 col; };\n"
        "vertex VOut rp_vs(VIn v [[stage_in]]) { VOut o; o.pos = float4(v.p, 0.0, 1.0); o.col = v.c; return o; }\n"
        "fragment float4 rp_fs(VOut i [[stage_in]]) { return i.col; }\n";
    NSError *err = nil; id<MTLLibrary> lib = [mtl newLibraryWithSource:src options:nil error:&err];
    if (!lib) { printf("{\"raster_probe\": {\"backend\": \"metal\", \"refused\": \"shader build: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return 2; }
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
    id<MTLRenderPipelineState> pso = [mtl newRenderPipelineStateWithDescriptor:pd error:&err];
    if (!pso) { printf("{\"raster_probe\": {\"backend\": \"metal\", \"refused\": \"pipeline: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return 2; }
    MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatRGBA8Unorm width:kRpSize height:kRpSize mipmapped:NO];
    td.usage = MTLTextureUsageRenderTarget | MTLTextureUsageShaderRead; td.storageMode = MTLStorageModeShared;
    id<MTLTexture> fb = [mtl newTextureWithDescriptor:td];
    if (!fb) { printf("{\"raster_probe\": {\"backend\": \"metal\", \"refused\": \"the 8192 x 8192 RGBA8 render target was refused\"}}\n"); return 2; }
    struct V { float x, y; uint8_t r, g, b, a; };
    std::vector<V> verts((size_t)kRpTriangles * 3);
    for (uint32_t t = 0; t < kRpTriangles; t++) { const RpTri tr = rp_triangle(t); const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2};
        for (int k = 0; k < 3; k++) { V &v = verts[(size_t)t * 3 + k]; v.x = (float)xs[k] / 4096.0f - 1.0f; v.y = 1.0f - (float)ys[k] / 4096.0f; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); } }
    id<MTLBuffer> vb = [mtl newBufferWithBytes:verts.data() length:verts.size() * sizeof(V) options:MTLResourceStorageModeShared];
    id<MTLCommandQueue> q = [mtl newCommandQueue];
    auto draw = [&](bool clear) {
        MTLRenderPassDescriptor *rp = [MTLRenderPassDescriptor renderPassDescriptor];
        rp.colorAttachments[0].texture = fb; rp.colorAttachments[0].loadAction = clear ? MTLLoadActionClear : MTLLoadActionLoad; rp.colorAttachments[0].storeAction = MTLStoreActionStore;
        rp.colorAttachments[0].clearColor = MTLClearColorMake(0, 0, 0, 0);
        id<MTLCommandBuffer> cb = [q commandBuffer]; id<MTLRenderCommandEncoder> enc = [cb renderCommandEncoderWithDescriptor:rp];
        [enc setRenderPipelineState:pso]; [enc setCullMode:MTLCullModeNone]; [enc setViewport:(MTLViewport){0, 0, (double)kRpSize, (double)kRpSize, 0, 1}];
        [enc setVertexBuffer:vb offset:0 atIndex:0]; [enc drawPrimitives:MTLPrimitiveTypeTriangle vertexStart:0 vertexCount:verts.size()]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
        return [cb status] == MTLCommandBufferStatusCompleted;
    };
    if (!draw(true)) { printf("{\"raster_probe\": {\"backend\": \"metal\", \"refused\": \"the draw did not complete\"}}\n"); return 2; }
    std::vector<uint8_t> card((size_t)kRpSize * kRpSize * 4);
    [fb getBytes:card.data() bytesPerRow:(NSUInteger)kRpSize * 4 fromRegion:MTLRegionMake2D(0, 0, kRpSize, kRpSize) mipmapLevel:0];
    double tri_s = 0, secs = 0;
    if (loop_s > 0) { auto t0 = std::chrono::steady_clock::now(); uint64_t draws = 0; while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < loop_s) { if (!draw(false)) break; draws++; } secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(); tri_s = (double)draws * kRpTriangles / secs; }
    return rp_report("metal", std::string([[mtl name] UTF8String]), card, "one draw of 3,000,000 vertices, blend ONE ONE ADD, RGBA8Unorm render target (shared), rasterSampleCount 1, cull none, getBytes row 0 the top", tri_s, secs, "the mini's plug beside the job (not read by the probe)");
}

// VERSION 18, the raster form, on Metal: raster_worker.h's host logic with the drawing here (the probe's pipeline: RGBA8Unorm render target,
// blend ONE ONE ADD, cull none); the boxes by getBytes per box (the texture shared), the boxes cleared by replaceRegion with zeros.
static int metal_raster_vectors(id<MTLDevice> mtl, const std::vector<std::string> &files, const std::string &params, double bench_s) {
    using namespace igm;
    IhParams p; std::string err; if (!ih_parse_params(params, p, err)) { printf("{\"raster_worker\": {\"refused\": \"params: %s\"}}\n", err.c_str()); return 2; }
    if (p.version == 19) return metal_raster_vectors_v19(mtl, files, p, params, bench_s);   // VERSION 19 (raster_metal_v19.h): the four passes on the card
    if (p.version != 18) { printf("{\"raster_worker\": {\"refused\": \"version %u is not a raster form (18 or 19)\"}}\n", p.version); return 2; }
    MetalCard mc; if (!mc.open(mtl, p, params, err)) { printf("{\"raster_worker\": {\"backend\": \"metal\", \"refused\": \"%s\"}}\n", err.c_str()); return 2; }
    R18Card &card = mc.card; const std::string device = mc.device; const uint32_t side = mc.side;
    if (bench_s > 0) {
        uint8_t P[32]; ih_P(p, P); uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); uint8_t K[32]; r18_key(p, 1, K, err);
        if (!r18_prepare(p, card, 1, err)) { printf("{\"raster_worker\": {\"refused\": \"%s\"}}\n", err.c_str()); return 2; }
        auto t0 = std::chrono::steady_clock::now(); uint64_t attempts = 0;
        const size_t B = card.batch ? (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 16) : 1;   // attempts in flight a batch (the GL worker's knob; 16 October 2026)
        while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s) { @autoreleasepool {
            std::vector<std::vector<R18Tri>> batch(B); for (size_t b = 0; b < B; b++) { uint8_t S[32]; r18_seed(p, P, header, attempts + b, S); batch[b] = r18_triangles(p, K, S); }
            std::vector<std::array<uint8_t, 32>> Ms; if (!r18_attempts(card, batch, K, Ms)) break; attempts += B; } }
        const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        printf("{\"bench\": {\"device\": \"%s\", \"backend\": \"metal\", \"version\": 18, \"rounds\": %u, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.4f, \"side\": %u, \"keyed\": %u, \"batch\": %zu, \"note\": \"attempts queued on the card a batch at a time where the card has a batch (the first rule on Metal: the draw, the digest on the card, the boxes zeroed on the card, one command buffer a batch), else one a pass (the draw, the boxes by getBytes, the boxes restored by replaceRegion)\"}}\n", device.c_str(), p.rounds, secs, (unsigned long long)attempts, attempts / secs / 1e6, side, p.dataset_bytes ? 1u : 0u, B);
        fflush(stdout); return 0;
    }
    return r18_vectors("metal", device, p, files, card, "one render pass an attempt, the boxes by getBytes, the boxes restored by replaceRegion (the keyed bytes under 18c, zero under the first rule)");
}

// VERSION 22, KILN on Metal (the hash team's document and sets on main, 15 Oct 2026 05:xx UK; the reference's namespace kiln, the Kiln seat's
// Vulkan port the sibling): per BATCH (nonce >> batch_log2) the fragment programme (program_ops words from the batch seed) emitted as MSL
// and compiled by Metal; per HASH the scene from the work seed (T integer-corner triangles over a side x side target, the corners'
// offsets in -spread .. +spread, the colour and one of five blends a triangle), `tiles` keyed 64 x 64 tiles of the epoch dataset
// (the dataset resident as one buffer; a texel = one word), every fragment's eight registers folds of the scene with its identity,
// `rows` full tile rows folded into the state, the programme with the destination as the third operand, the blend as INTEGER operations
// in the fragment on the target's current word (the consensus chair's condition: never the fixed-function blender; the target an R32Uint
// texture in a raster order group, so overlapping fragments of a pixel apply in primitive order), K hashes a render as a column of
// tiles on one buffer-backed target; the digest BLAKE3 over the target in row order with E, S and the programme hash on the host, the
// final digest the R0A final. The dataset and E from the reference library (ignh_inner_create: the full context's bytes).
extern "C" void *ignh_inner_create(const char *json, uint64_t height, int full, int *status);
extern "C" const uint8_t *ignh_inner_dataset(const void *ctx, uint64_t *bytes);
extern "C" void ignh_inner_destroy(void *ctx);
struct K22Tri { int32_t x0, y0, x1, y1, x2, y2, minx; uint32_t colour, mode; };
struct K22Hash { uint64_t height, nonce; uint8_t header[32], S[32]; uint32_t sw[8]; uint32_t tile[64]; K22Tri tri[64]; uint8_t M[32], F[32]; };
struct K22Batch { uint8_t seed[32], phash[32]; std::vector<uint32_t> pw; };
static inline uint32_t k22_rotl(uint32_t x, uint32_t n) { n &= 31u; return n ? (x << n) | (x >> (32u - n)) : x; }
static inline uint32_t k22_fchain(uint32_t x, uint32_t v) { x = k22_rotl(x ^ v, 5) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; }
static inline uint32_t k22_le32(const uint8_t *p) { return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24); }
static void k22_batch(const igm::IhParams &p, const uint8_t E[32], const uint8_t header[32], uint64_t nonce, K22Batch &b) {
    // plain arrays (the mini's stack-protector abort in this function with the vector form, 15 Oct 2026 09:xx UK; the same code clean under ASan on the builder)
    uint8_t w[128]; size_t n = 0; const char *tag = "IgneumHash/Kiln/batch"; w[n++] = 21; memcpy(w + n, tag, 21); n += 21; memcpy(w + n, E, 32); n += 32; memcpy(w + n, header, 32); n += 32;
    const uint64_t bn = nonce >> p.keyed_rounds; for (int i = 0; i < 8; i++) w[n++] = (uint8_t)(bn >> (8 * i));
    igm::ih_blake3_full(w, n, b.seed);
    const uint32_t words = p.program_ops > 256 ? 256 : p.program_ops; b.pw.assign((size_t)words * 2, 0u); uint8_t pb[2048];
    uint32_t x[8]; for (int k = 0; k < 8; k++) x[k] = k22_le32(b.seed + 4 * k);
    for (uint32_t i = 0; i < words; i++) for (uint32_t j = 0; j < 2; j++) { const uint32_t idx = 2 * i + j; x[idx & 7] = k22_fchain(x[idx & 7], idx); b.pw[idx] = x[idx & 7]; for (uint32_t q = 0; q < 4; q++) pb[idx * 4 + q] = (uint8_t)(x[idx & 7] >> (8 * q)); }
    igm::ih_blake3_full(pb, (size_t)words * 8, b.phash);
}
static void k22_scene(const igm::IhParams &p, const uint8_t P[32], const uint8_t E[32], uint64_t tile_count, K22Hash &h) {
    igm::r18_seed(p, P, h.header, h.nonce, h.S);
    uint8_t sc[32]; { std::vector<uint8_t> w; igm::r18_lp(w, "IgneumHash/Kiln/scene"); w.insert(w.end(), E, E + 32); w.insert(w.end(), h.S, h.S + 32); igm::ih_blake3_full(w.data(), w.size(), sc); }
    for (int k = 0; k < 8; k++) h.sw[k] = k22_le32(sc + 4 * k);
    const uint32_t ntiles = p.fold_every; for (uint32_t k = 0; k < ntiles; k++) h.tile[k] = (uint32_t)((uint64_t)k22_fchain(h.sw[0], 1000u + k) % tile_count);
    const int32_t side = 1 << p.registers, sp = (int32_t)p.scratch_bytes, span = 2 * sp + 1;
    auto cl = [&](int32_t v) { return v < 0 ? 0 : v > side ? side : v; };
    for (uint32_t t = 0; t < p.rounds; t++) {
        const uint32_t cw = k22_fchain(h.sw[t & 7], t), u = igm::rp_fmix32(cw ^ 0x2545F491u); K22Tri &r = h.tri[t];
        r.x0 = (int32_t)(cw & (uint32_t)(side - 1)); r.y0 = (int32_t)((cw >> 8) & (uint32_t)(side - 1));
        r.x1 = cl(r.x0 + (int32_t)(u % (uint32_t)span) - sp); r.y1 = cl(r.y0 + (int32_t)((u >> 8) % (uint32_t)span) - sp); r.x2 = cl(r.x0 + (int32_t)((u >> 16) % (uint32_t)span) - sp); r.y2 = cl(r.y0 + (int32_t)((u >> 24) % (uint32_t)span) - sp);
        r.colour = igm::rp_fmix32(cw ^ 0x9E3779B9u); r.mode = (u >> 16) % 5u; r.minx = std::min(r.x0, std::min(r.x1, r.x2));
    }
}
static void k22_digest(const igm::IhParams &p, const uint8_t P[32], const uint8_t E[32], const K22Batch &b, K22Hash &h, const uint8_t *fb, size_t fb_bytes) {
    std::vector<uint8_t> w; igm::r18_lp(w, "IgneumHash/Kiln/mix"); w.insert(w.end(), E, E + 32); w.insert(w.end(), h.S, h.S + 32); w.insert(w.end(), b.phash, b.phash + 32); w.insert(w.end(), fb, fb + fb_bytes);
    igm::ih_blake3_full(w.data(), w.size(), h.M); igm::ih_final_digest(p.chain_id, p.version, P, h.header, h.nonce, h.M, h.F);
}
// THE FRAGMENTS FORM ("fragments" N: exactly N programme passes a hash; the reference's rule): the covered fragments in triangle then
// raster order, only the first N, then the fill of the uncovered pixels in raster order (triangle 0, colour 0, mode 0) until N; every
// pass carries its index (the coalesced rows' run = index / 16, the slice = index mod 16). The passes run as a compute kernel (one
// thread a pass, the programme's output word), the blends then applied on the host in pass order; the rasterised forms keep the render.
struct K22Pass { uint8_t x, y, t, mode; uint32_t colour; uint32_t minx, index; };
static void k22_passes(const igm::IhParams &p, const K22Hash &h, std::vector<K22Pass> &out) {
    const uint32_t side = 1u << p.registers, fcap = p.table_kib; out.clear(); std::vector<uint8_t> covered((size_t)side * side, 0); uint32_t nrun = 0;
    for (uint32_t t = 0; t < p.rounds; t++) {
        const K22Tri &r = h.tri[t]; const int64_t ax = r.x0, ay = r.y0, bx = r.x1, by = r.y1, cx = r.x2, cy = r.y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (area == 0) continue; const int64_t sg = area > 0 ? 1 : -1;
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; k++) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; e[k].tl = dy == 0 ? ((dx > 0 ? 1 : -1) == sg) : ((-dy > 0 ? 1 : -1) == sg); }
        const int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
        for (int64_t j = miny; j < maxy; j++) for (int64_t i = minx; i < maxx; i++) {
            const int64_t px = 2 * i + 1, py = 2 * j + 1; bool in = true;
            for (int k = 0; k < 3 && in; k++) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * sg; if (w < 0 || (w == 0 && !e[k].tl)) in = false; }
            if (!in) continue;
            if (fcap && nrun >= fcap) continue;
            K22Pass ps; ps.x = (uint8_t)i; ps.y = (uint8_t)j; ps.t = (uint8_t)t; ps.mode = (uint8_t)r.mode; ps.colour = r.colour; ps.minx = (uint32_t)minx; ps.index = nrun; out.push_back(ps); nrun++; covered[(size_t)j * side + i] = 1;
        }
    }
    if (fcap && nrun < fcap) for (uint32_t j = 0; j < side && nrun < fcap; j++) for (uint32_t i = 0; i < side && nrun < fcap; i++) {
        if (covered[(size_t)j * side + i]) continue;
        K22Pass ps; ps.x = (uint8_t)i; ps.y = (uint8_t)j; ps.t = 0; ps.mode = 0; ps.colour = 0; ps.minx = 0; ps.index = nrun; out.push_back(ps); nrun++;
    }
}
// the batch's compute kernel for the fragments form: one thread a pass; the pass words pl[4 i] = x | y << 8 | t << 16 | mode << 24, pl[4 i + 1] = colour, pl[4 i + 2] = minx, pl[4 i + 3] = index | hash slot << 16
static std::string k22_msl_compute(const igm::IhParams &p, const K22Batch &b) {
    const uint32_t tl = p.registers, tiles = p.fold_every, rows = p.blocks, words = p.program_ops; const bool coal = (p.access_bytes & 1u) != 0; const uint32_t contig = (p.access_bytes >> 8) ? (p.access_bytes >> 8) : 1u; const uint64_t all_rows = p.dataset_bytes / 256;
    std::string o = "#include <metal_stdlib>\nusing namespace metal;\n"
        "static inline uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }\n"
        "kernel void k_cs(device const uint *ds [[buffer(0)]], device const uint *sc [[buffer(1)]], device const uint *pl [[buffer(2)]], device uint *out [[buffer(3)]], uint gid [[thread_position_in_grid]]) {\n"
        "  uint w0 = pl[4u * gid], colour = pl[4u * gid + 1u], mx = pl[4u * gid + 2u], w3 = pl[4u * gid + 3u]; uint x = w0 & 255u, y = (w0 >> 8) & 255u, t = (w0 >> 16) & 255u, index = w3 & 65535u, hs = w3 >> 16;\n"
        "  uint base = hs * " + std::to_string(8 + tiles) + "u;\n"
        "  uint fid = (t << " + std::to_string(2 * tl) + "u) | (y << " + std::to_string(tl) + "u) | x; uint fser = x - mx; uint f0 = x | (y << 16) | (t << 26);\n"
        "  uint r0 = fmix32(f0 + 0x9E3779B9u * 1u + fser * 0x85EBCA6Bu) ^ f0, r1 = fmix32(f0 + 0x9E3779B9u * 2u + fser * 0x85EBCA6Bu) ^ colour, r2 = fmix32(f0 + 0x9E3779B9u * 3u + fser * 0x85EBCA6Bu) ^ (t ^ sc[base + 1u]);\n"
        "  uint r3 = fmix32(f0 + 0x9E3779B9u * 4u + fser * 0x85EBCA6Bu) ^ sc[base + 2u], r4 = fmix32(f0 + 0x9E3779B9u * 5u + fser * 0x85EBCA6Bu) ^ sc[base + 3u], r5 = fmix32(f0 + 0x9E3779B9u * 6u + fser * 0x85EBCA6Bu) ^ sc[base + 4u], r6 = fmix32(f0 + 0x9E3779B9u * 7u + fser * 0x85EBCA6Bu) ^ sc[base + 5u], r7 = fmix32(f0 + 0x9E3779B9u * 8u + fser * 0x85EBCA6Bu) ^ sc[base + 6u];\n"
        "  uint tk = (fid >> 4) % " + std::to_string(tiles) + "u; uint tb = sc[base + 8u + tk] * 4096u; uint sw5 = sc[base + 5u], sw6 = sc[base + 6u], sw7 = sc[base + 7u];\n";
    if (rows && coal) {
        o += "  { uint run = index >> 4, sl = (index & 15u) * 4u;\n    for (uint q = 0u; q < " + std::to_string(rows) + "u; q++) {\n";
        if (contig == 1) o += "      uint row = fmix32(run ^ sw7 ^ (q * 0x9E3779B9u)) % " + std::to_string(all_rows) + "u;\n";
        else o += "      uint row = fmix32(run ^ sw7 ^ ((q / " + std::to_string(contig) + "u) * 0x9E3779B9u)) % " + std::to_string(all_rows - contig) + "u + (q % " + std::to_string(contig) + "u);\n";
        o += "      uint a0 = row ^ sw6, a1 = row * 0x9E3779B1u, a2 = sw5 ^ q, a3 = sw7 + row; uint rb = row * 64u + sl; uint v0 = ds[rb], v1 = ds[rb + 1u], v2 = ds[rb + 2u], v3 = ds[rb + 3u]; a0 = (a0 ^ v0) + v0; a1 = (a1 ^ v1) + v1; a2 = (a2 ^ v2) + v2; a3 = (a3 ^ v3) + v3; uint f = a0 ^ a1 ^ a2 ^ a3;\n"
             "      switch (q % 5u) { case 0u: r3 ^= f; break; case 1u: r4 ^= f; break; case 2u: r5 ^= f; break; case 3u: r6 ^= f; break; default: r7 ^= f; break; }\n    } }\n";
    } else if (rows) {
        const uint32_t row_count = tiles * 64u, stride = row_count / rows;
        o += "  uint start = fmix32(fid ^ sw7);\n";
        for (uint32_t q = 0; q < rows; q++)
            o += "  { uint row = (start + " + std::to_string(q * stride) + "u) % " + std::to_string(row_count) + "u; uint a0 = row ^ sw6, a1 = row * 0x9E3779B1u, a2 = sw5 ^ " + std::to_string(q) + "u, a3 = sw7 + row; uint rb = sc[base + 8u + (row >> 6)] * 4096u + (row & 63u) * 64u;"
                 " for (uint w = 0u; w < 64u; w += 4u) { uint v0 = ds[rb + w], v1 = ds[rb + w + 1u], v2 = ds[rb + w + 2u], v3 = ds[rb + w + 3u]; a0 = (a0 ^ v0) + v0; a1 = (a1 ^ v1) + v1; a2 = (a2 ^ v2) + v2; a3 = (a3 ^ v3) + v3; } r" + std::to_string(3 + q % 5) + " ^= a0 ^ a1 ^ a2 ^ a3; }\n";
    }
    for (uint32_t q = 0; q < words; q++) {
        const uint32_t w0 = b.pw[2 * q], imm = b.pw[2 * q + 1]; const std::string a = "r" + std::to_string((q + 3) & 7), bb = "r" + std::to_string((q + 5) & 7), d = "r" + std::to_string(q & 7), im = std::to_string(imm) + "u";
        switch (w0 % 6u) {
            case 0: o += "  " + d + " = " + a + " + " + bb + " + " + im + " + rotate(" + d + ", 9u);\n"; break;
            case 1: o += "  " + d + " = " + a + " ^ " + bb + " ^ " + im + " ^ rotate(" + d + ", 17u);\n"; break;
            case 2: o += "  " + d + " = rotate(" + a + ", " + std::to_string(imm & 31u) + "u) ^ " + bb + " ^ " + d + ";\n"; break;
            case 3: o += "  " + d + " = " + a + " * " + bb + " + " + im + " + " + d + ";\n"; break;
            case 4: o += "  " + d + " = (mulhi(" + a + ", " + bb + ") ^ " + im + ") + rotate(" + d + ", 13u);\n"; break;
            default: o += "  " + d + " = ds[tb + ((" + a + " + " + im + " + " + d + ") & 4095u)] ^ " + bb + " ^ rotate(" + d + ", 5u);\n"; break;
        }
    }
    o += "  out[gid] = r7;\n}\n"; return o;
}
static inline uint8_t k22_blend(uint32_t mode, uint32_t dst, uint32_t src) { switch (mode) { case 0: return (uint8_t)std::min<uint32_t>(255u, dst + src); case 1: return (uint8_t)src; case 2: return (uint8_t)(dst > src ? dst - src : 0u); case 3: return (uint8_t)std::min(dst, src); default: return (uint8_t)std::max(dst, src); } }
// the batch's shader: the vertex carries the colour, the triangle index with its blend mode, the hash slot and the triangle's minx; the fragment the whole attempt
static std::string k22_msl(const igm::IhParams &p, const K22Batch &b) {
    const uint32_t tl = p.registers, side = 1u << tl, tiles = p.fold_every, rows = p.blocks, words = p.program_ops;
    std::string o = "#include <metal_stdlib>\nusing namespace metal;\nstruct VIn { float2 p [[attribute(0)]]; uint c [[attribute(1)]]; uint tm [[attribute(2)]]; uint h [[attribute(3)]]; uint mx [[attribute(4)]]; };\n"
        "struct VOut { float4 pos [[position]]; uint c [[flat]]; uint tm [[flat]]; uint h [[flat]]; uint mx [[flat]]; };\n"
        "vertex VOut k_vs(VIn v [[stage_in]]) { VOut o; o.pos = float4(v.p, 0.0, 1.0); o.c = v.c; o.tm = v.tm; o.h = v.h; o.mx = v.mx; return o; }\n"
        "static inline uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }\n"
        "fragment void k_fs(VOut i [[stage_in]], texture2d<uint, access::read_write> target [[texture(0), raster_order_group(0)]], device const uint *ds [[buffer(0)]], device const uint *sc [[buffer(1)]]) {\n"
        "  uint2 pc = uint2(i.pos.xy); uint x = pc.x & " + std::to_string(side - 1) + "u, y = pc.y & " + std::to_string(side - 1) + "u; uint t = i.tm & 255u, mode = i.tm >> 8;\n"
        "  uint base = i.h * " + std::to_string(8 + tiles) + "u;\n"
        "  uint fid = (t << " + std::to_string(2 * tl) + "u) | (y << " + std::to_string(tl) + "u) | x; uint fser = x - i.mx; uint f0 = x | (y << 16) | (t << 26);\n"
        "  uint r0 = fmix32(f0 + 0x9E3779B9u * 1u + fser * 0x85EBCA6Bu) ^ f0, r1 = fmix32(f0 + 0x9E3779B9u * 2u + fser * 0x85EBCA6Bu) ^ i.c, r2 = fmix32(f0 + 0x9E3779B9u * 3u + fser * 0x85EBCA6Bu) ^ (t ^ sc[base + 1u]);\n"
        "  uint r3 = fmix32(f0 + 0x9E3779B9u * 4u + fser * 0x85EBCA6Bu) ^ sc[base + 2u], r4 = fmix32(f0 + 0x9E3779B9u * 5u + fser * 0x85EBCA6Bu) ^ sc[base + 3u], r5 = fmix32(f0 + 0x9E3779B9u * 6u + fser * 0x85EBCA6Bu) ^ sc[base + 4u], r6 = fmix32(f0 + 0x9E3779B9u * 7u + fser * 0x85EBCA6Bu) ^ sc[base + 5u], r7 = fmix32(f0 + 0x9E3779B9u * 8u + fser * 0x85EBCA6Bu) ^ sc[base + 6u];\n"
        "  uint tk = (fid >> 4) % " + std::to_string(tiles) + "u; uint tb = sc[base + 8u + tk] * 4096u;\n";
    if (rows) {
        const uint32_t row_count = tiles * 64u, stride = row_count / rows;
        o += "  uint start = fmix32(fid ^ sc[base + 7u]); uint sw5 = sc[base + 5u], sw6 = sc[base + 6u], sw7 = sc[base + 7u];\n";
        for (uint32_t q = 0; q < rows; q++)
            o += "  { uint row = (start + " + std::to_string(q * stride) + "u) % " + std::to_string(row_count) + "u; uint a0 = row ^ sw6, a1 = row * 0x9E3779B1u, a2 = sw5 ^ " + std::to_string(q) + "u, a3 = sw7 + row; uint rb = sc[base + 8u + (row >> 6)] * 4096u + (row & 63u) * 64u;"
                 " for (uint w = 0u; w < 64u; w += 4u) { uint v0 = ds[rb + w], v1 = ds[rb + w + 1u], v2 = ds[rb + w + 2u], v3 = ds[rb + w + 3u]; a0 = (a0 ^ v0) + v0; a1 = (a1 ^ v1) + v1; a2 = (a2 ^ v2) + v2; a3 = (a3 ^ v3) + v3; } r" + std::to_string(3 + q % 5) + " ^= a0 ^ a1 ^ a2 ^ a3; }\n";
    }
    for (uint32_t q = 0; q < words; q++) {
        const uint32_t w0 = b.pw[2 * q], imm = b.pw[2 * q + 1]; const std::string a = "r" + std::to_string((q + 3) & 7), bb = "r" + std::to_string((q + 5) & 7), d = "r" + std::to_string(q & 7), im = std::to_string(imm) + "u";
        switch (w0 % 6u) {
            case 0: o += "  " + d + " = " + a + " + " + bb + " + " + im + " + rotate(" + d + ", 9u);\n"; break;
            case 1: o += "  " + d + " = " + a + " ^ " + bb + " ^ " + im + " ^ rotate(" + d + ", 17u);\n"; break;
            case 2: o += "  " + d + " = rotate(" + a + ", " + std::to_string(imm & 31u) + "u) ^ " + bb + " ^ " + d + ";\n"; break;
            case 3: o += "  " + d + " = " + a + " * " + bb + " + " + im + " + " + d + ";\n"; break;
            case 4: o += "  " + d + " = (mulhi(" + a + ", " + bb + ") ^ " + im + ") + rotate(" + d + ", 13u);\n"; break;
            default: o += "  " + d + " = ds[tb + ((" + a + " + " + im + " + " + d + ") & 4095u)] ^ " + bb + " ^ rotate(" + d + ", 5u);\n"; break;
        }
    }
    o += "  uint dst = target.read(pc).x, src = r7, res = 0u;\n"
         "  if (mode == 1u) res = src;\n"
         "  else for (uint k = 0u; k < 4u; k++) { uint s = (dst >> (8u * k)) & 255u, v = (src >> (8u * k)) & 255u; uint r = mode == 0u ? min(s + v, 255u) : mode == 2u ? (s > v ? s - v : 0u) : mode == 3u ? min(s, v) : max(s, v); res |= r << (8u * k); }\n"
         "  target.write(uint4(res, 0u, 0u, 0u), pc);\n}\n";
    return o;
}
static void k22_abort_handler(int sig) { void *fr[64]; const int n = backtrace(fr, 64); const char *m = "kiln22: SIGABRT, the aborting thread's frames:\n"; (void)!write(2, m, strlen(m)); backtrace_symbols_fd(fr, n, 2); _exit(134); (void)sig; }
static int metal_kiln22(id<MTLDevice> mtl, const std::vector<std::string> &files, const std::string &params, double bench_s) {
    using namespace igm;
    signal(SIGABRT, k22_abort_handler);   // the silent abort on the mini (15 Oct 2026): the frames of whichever thread aborts
    IhParams p; std::string err; if (!ih_parse_params(params, p, err)) { printf("{\"kiln22\": {\"refused\": \"params: %s\"}}\n", err.c_str()); return 2; }
    if (p.version != 22) { printf("{\"kiln22\": {\"refused\": \"version %u is not the Kiln form (22)\"}}\n", p.version); return 2; }
    setvbuf(stdout, nullptr, _IONBF, 0); const bool k22_trace = getenv("IGM_KILN22_TRACE") != nullptr;   // the stage marks on stderr (a silent exit on the mini, 15 Oct 2026 02:5x UK)
    auto mark = [&](const char *what) { if (k22_trace) { fprintf(stderr, "kiln22: %s\n", what); fflush(stderr); } };
    const uint32_t side = 1u << p.registers, tiles = p.fold_every, T = p.rounds; const uint64_t items = p.dataset_bytes / 256, tile_count = items / 64; const size_t fb_bytes = (size_t)side * side * 4;
    const uint32_t K = std::min<uint32_t>(16384u / side, 1u << p.keyed_rounds);   // hashes a render: a column of K tiles on one target (Metal's 16384 texture edge)
    uint8_t P[32]; ih_P(p, P);
    const std::string device = std::string([[mtl name] UTF8String]);
    // the epoch's dataset and E from the reference (a context a height, kept)
    struct Epoch { uint64_t height = 0; uint8_t E[32] = {0}; void *inner = nullptr; id<MTLBuffer> ds = nil; };
    Epoch epochs[8]; int n_epochs = 0;   // a fixed array (the silent abort on the mini sat at a vector of these under ARC)
    auto epoch_for = [&](uint64_t height, Epoch *&out) -> bool {
        for (int q = 0; q < n_epochs; q++) if (epochs[q].height == height) { out = &epochs[q]; return true; }
        if (n_epochs == 8) { printf("{\"kiln22\": {\"refused\": \"more than eight epochs in one run\"}}\n"); return false; }
        Epoch &e = epochs[n_epochs];
        int st = 0; ignpow_ctx *light = ignpow_ctx_create("igneumhash", params.c_str(), height, 0, &st); if (!light) { printf("{\"kiln22\": {\"refused\": \"the reference refused a light context (status %d)\"}}\n", st); return false; }
        e.height = height; { uint8_t D[32], cd[32], dg[32]; uint64_t bm = 0; int hd = 0; if (ignh_dataset_info(light, e.E, D, cd, dg, &bm, &hd) != IGNPOW_OK) { printf("{\"kiln22\": {\"refused\": \"no E from the reference\"}}\n"); ignpow_ctx_free(light); return false; } }
        ignpow_ctx_free(light);
        auto t0 = std::chrono::steady_clock::now();
        e.inner = ignh_inner_create(params.c_str(), height, 1, &st); if (!e.inner) { printf("{\"kiln22\": {\"refused\": \"the reference refused the full context (status %d)\"}}\n", st); return false; }
        uint64_t db = 0; const uint8_t *d = ignh_inner_dataset(e.inner, &db); if (!d || db != p.dataset_bytes) { printf("{\"kiln22\": {\"refused\": \"the reference's dataset is %llu bytes\"}}\n", (unsigned long long)db); return false; }
        // the reference's pages as the Metal buffer, no copy (one gigabyte resident, not two: the worker died at the second gigabyte on the mini); the copy path when the pages are not page-aligned, the reference's context then dropped
        const bool aligned = (((uintptr_t)d) & 16383u) == 0 && (db & 16383u) == 0;
        e.ds = aligned ? [mtl newBufferWithBytesNoCopy:(void *)d length:(NSUInteger)db options:MTLResourceStorageModeShared deallocator:nil] : nil;
        if (!e.ds) { e.ds = [mtl newBufferWithBytes:d length:(NSUInteger)db options:MTLResourceStorageModeShared]; if (!e.ds) { printf("{\"kiln22\": {\"refused\": \"the dataset buffer (%llu bytes) was refused\"}}\n", (unsigned long long)db); return false; } ignh_inner_destroy(e.inner); e.inner = nullptr; }
        if (k22_trace) fprintf(stderr, "kiln22: the dataset buffer %s\n", aligned && e.inner ? "shares the reference's pages" : "is a copy, the reference's context dropped");
        fprintf(stderr, "kiln22: the epoch dataset of height %llu built by the reference in %.1f s (E %s)\n", (unsigned long long)height, std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(), hex_of(e.E, 32).c_str());
        n_epochs++; out = &e; mark("epoch: kept"); return true;
    };
    // the target: K tiles of side x side R32Uint on one shared buffer (zeroed by the blit encoder, read back from the buffer)
    const NSUInteger row_bytes = side * 4; id<MTLBuffer> tbuf = [mtl newBufferWithLength:(NSUInteger)fb_bytes * K options:MTLResourceStorageModeShared];
    MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatR32Uint width:side height:(NSUInteger)side * K mipmapped:NO]; td.usage = MTLTextureUsageShaderRead | MTLTextureUsageShaderWrite; td.storageMode = MTLStorageModeShared;
    id<MTLTexture> target = [tbuf newTextureWithDescriptor:td offset:0 bytesPerRow:row_bytes]; if (!target) { printf("{\"kiln22\": {\"refused\": \"the buffer-backed target was refused\"}}\n"); return 2; }
    mark("target made");
    MTLVertexDescriptor *vd = [MTLVertexDescriptor vertexDescriptor];
    vd.attributes[0].format = MTLVertexFormatFloat2; vd.attributes[0].offset = 0; vd.attributes[0].bufferIndex = 2;
    for (int a = 1; a <= 4; a++) { vd.attributes[a].format = MTLVertexFormatUInt; vd.attributes[a].offset = 8 + 4 * (a - 1); vd.attributes[a].bufferIndex = 2; }
    vd.layouts[2].stride = 24; vd.layouts[2].stepFunction = MTLVertexStepFunctionPerVertex;
    id<MTLCommandQueue> q = [mtl newCommandQueue];
    struct V { float x, y; uint32_t c, tm, h, mx; };
    const uint32_t scw = 8 + tiles;   // the scene words a hash
    std::vector<V> verts; std::vector<uint32_t> scene;
    double compile_ms = 0; uint64_t batches = 0;
    // one render of n hashes (one batch, one epoch) with a compiled pipeline: the scenes to the buffer, the target zeroed, one draw, the digests
    auto render = [&](id<MTLRenderPipelineState> pso, Epoch &ep, const K22Batch &b, K22Hash *hs, uint32_t n) -> bool {
        verts.resize((size_t)n * T * 3); scene.assign((size_t)n * scw, 0);
        for (uint32_t h = 0; h < n; h++) { K22Hash &hh = hs[h]; for (int k = 0; k < 8; k++) scene[(size_t)h * scw + k] = hh.sw[k]; for (uint32_t k = 0; k < tiles; k++) scene[(size_t)h * scw + 8 + k] = hh.tile[k];
            for (uint32_t t = 0; t < T; t++) { const K22Tri &tr = hh.tri[t]; const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2};
                for (int k = 0; k < 3; k++) { V &v = verts[((size_t)h * T + t) * 3 + k]; v.x = (float)xs[k] / (float)(side / 2) - 1.0f; v.y = 1.0f - (float)(ys[k] + (int)(side * h)) / (float)((side * K) / 2); v.c = tr.colour; v.tm = t | (tr.mode << 8); v.h = h; v.mx = (uint32_t)tr.minx; } } }
        id<MTLBuffer> vb = [mtl newBufferWithBytes:verts.data() length:verts.size() * sizeof(V) options:MTLResourceStorageModeShared];
        id<MTLBuffer> sb = [mtl newBufferWithBytes:scene.data() length:scene.size() * 4 options:MTLResourceStorageModeShared];
        mark("render: buffers made"); id<MTLCommandBuffer> cb = [q commandBuffer];
        { id<MTLBlitCommandEncoder> bl = [cb blitCommandEncoder]; [bl fillBuffer:tbuf range:NSMakeRange(0, (NSUInteger)fb_bytes * n) value:0]; [bl endEncoding]; }
        MTLRenderPassDescriptor *rp = [MTLRenderPassDescriptor renderPassDescriptor]; rp.renderTargetWidth = side; rp.renderTargetHeight = (NSUInteger)side * K; rp.defaultRasterSampleCount = 1;
        id<MTLRenderCommandEncoder> enc = [cb renderCommandEncoderWithDescriptor:rp]; [enc setRenderPipelineState:pso]; [enc setCullMode:MTLCullModeNone]; [enc setViewport:(MTLViewport){0, 0, (double)side, (double)(side * K), 0, 1}];
        [enc setVertexBuffer:vb offset:0 atIndex:2]; [enc setFragmentTexture:target atIndex:0]; [enc setFragmentBuffer:ep.ds offset:0 atIndex:0]; [enc setFragmentBuffer:sb offset:0 atIndex:1];
        mark("render: encoded"); [enc drawPrimitives:MTLPrimitiveTypeTriangle vertexStart:0 vertexCount:verts.size()]; [enc endEncoding]; [cb commit]; mark("render: committed"); [cb waitUntilCompleted]; mark("render: completed");
        if ([cb status] != MTLCommandBufferStatusCompleted) { fprintf(stderr, "kiln22: the render did not complete: %s\n", [[[cb error] localizedDescription] UTF8String]); return false; }
        const uint8_t *fb = (const uint8_t *)[tbuf contents];
        for (uint32_t h = 0; h < n; h++) k22_digest(p, P, ep.E, b, hs[h], fb + (size_t)h * fb_bytes, fb_bytes);
        return true;
    };
    const bool frag_form = p.table_kib != 0;   // the fragments form: the compute path
    id<MTLComputePipelineState> cpso = nil; std::vector<K22Pass> passes; std::vector<uint32_t> plw; id<MTLBuffer> plb = nil, outb = nil;
    auto cpipeline = [&](const K22Batch &b) -> bool {
        auto c0 = std::chrono::steady_clock::now(); NSError *nerr = nil; const std::string src = k22_msl_compute(p, b); if (k22_trace && batches == 0) fprintf(stderr, "kiln22: the first batch's compute kernel:\n%s\n", src.c_str());
        id<MTLLibrary> lib = [mtl newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:nil error:&nerr];
        if (!lib) { printf("{\"kiln22\": {\"refused\": \"compute shader compile: %s\"}}\n", nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        cpso = [mtl newComputePipelineStateWithFunction:[lib newFunctionWithName:@"k_cs"] error:&nerr]; if (!cpso) { printf("{\"kiln22\": {\"refused\": \"compute pipeline: %s\"}}\n", nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        compile_ms += std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - c0).count(); batches++; return true;
    };
    // the fragments form's run of n hashes: the pass lists on the host, one dispatch, the blends in pass order on the host, the digests
    auto crender = [&](Epoch &ep, const K22Batch &b, K22Hash *hs, uint32_t n) -> bool {
        const uint32_t fcap = p.table_kib; scene.assign((size_t)n * scw, 0); plw.assign((size_t)n * fcap * 4, 0);
        for (uint32_t h = 0; h < n; h++) { K22Hash &hh = hs[h]; for (int k = 0; k < 8; k++) scene[(size_t)h * scw + k] = hh.sw[k]; for (uint32_t k = 0; k < tiles; k++) scene[(size_t)h * scw + 8 + k] = hh.tile[k];
            k22_passes(p, hh, passes); if (passes.size() != fcap) { fprintf(stderr, "kiln22: hash %u has %zu passes, not %u\n", h, passes.size(), fcap); return false; }
            for (uint32_t i = 0; i < fcap; i++) { const K22Pass &ps = passes[i]; uint32_t *w = &plw[((size_t)h * fcap + i) * 4]; w[0] = (uint32_t)ps.x | ((uint32_t)ps.y << 8) | ((uint32_t)ps.t << 16) | ((uint32_t)ps.mode << 24); w[1] = ps.colour; w[2] = ps.minx; w[3] = ps.index | (h << 16); } }
        id<MTLBuffer> sb = [mtl newBufferWithBytes:scene.data() length:scene.size() * 4 options:MTLResourceStorageModeShared];
        plb = [mtl newBufferWithBytes:plw.data() length:plw.size() * 4 options:MTLResourceStorageModeShared]; outb = [mtl newBufferWithLength:(NSUInteger)n * fcap * 4 options:MTLResourceStorageModeShared];
        id<MTLCommandBuffer> cb = [q commandBuffer]; id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder]; [enc setComputePipelineState:cpso]; [enc setBuffer:ep.ds offset:0 atIndex:0]; [enc setBuffer:sb offset:0 atIndex:1]; [enc setBuffer:plb offset:0 atIndex:2]; [enc setBuffer:outb offset:0 atIndex:3];
        const NSUInteger total = (NSUInteger)n * fcap, tg = 256; [enc dispatchThreads:MTLSizeMake(total, 1, 1) threadsPerThreadgroup:MTLSizeMake(tg, 1, 1)]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { fprintf(stderr, "kiln22: the dispatch did not complete: %s\n", [[[cb error] localizedDescription] UTF8String]); return false; }
        const uint32_t *ow = (const uint32_t *)[outb contents]; std::vector<uint8_t> fb(fb_bytes);
        for (uint32_t h = 0; h < n; h++) { std::fill(fb.begin(), fb.end(), 0); const uint32_t *pw = &plw[(size_t)h * fcap * 4];
            for (uint32_t i = 0; i < fcap; i++) { const uint32_t w0 = pw[4 * i], x = w0 & 255u, y = (w0 >> 8) & 255u, mode = w0 >> 24, src = ow[(size_t)h * fcap + i]; uint8_t *qd = &fb[((size_t)y * side + x) * 4]; for (int k = 0; k < 4; k++) qd[k] = k22_blend(mode, qd[k], (src >> (8 * k)) & 255u); }
            k22_digest(p, P, ep.E, b, hs[h], fb.data(), fb_bytes); }
        return true;
    };
    auto pipeline = [&](const K22Batch &b, id<MTLRenderPipelineState> __strong &pso) -> bool {
        mark("pipeline: entered"); auto c0 = std::chrono::steady_clock::now(); NSError *nerr = nil; const std::string src = k22_msl(p, b); mark("pipeline: shader text made"); if (k22_trace && batches == 0) fprintf(stderr, "kiln22: the first batch's shader:\n%s\n", src.c_str()); mark("pipeline: compiling");
        id<MTLLibrary> lib = [mtl newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:nil error:&nerr];
        if (!lib) { printf("{\"kiln22\": {\"refused\": \"shader compile: %s\"}}\n", nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        MTLRenderPipelineDescriptor *pd = [[MTLRenderPipelineDescriptor alloc] init]; pd.vertexFunction = [lib newFunctionWithName:@"k_vs"]; pd.fragmentFunction = [lib newFunctionWithName:@"k_fs"]; pd.vertexDescriptor = vd; pd.rasterSampleCount = 1;
        pso = [mtl newRenderPipelineStateWithDescriptor:pd error:&nerr]; if (!pso) { printf("{\"kiln22\": {\"refused\": \"pipeline: %s\"}}\n", nerr ? [[nerr localizedDescription] UTF8String] : "?"); return false; }
        mark("pipeline: made"); compile_ms += std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - c0).count(); batches++; return true;
    };
    if (bench_s > 0) {   // the rate row: a fixed header at height 1, the nonces from 0 in batches of 2^batch_log2, K hashes a render; the hashes counted on the clock
        uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); Epoch *ep = nullptr; if (!epoch_for(1, ep)) return 2;
        std::vector<K22Hash> hs(K); uint64_t hashes = 0, nonce = 0; const uint64_t per_batch = 1ull << p.keyed_rounds;
        auto t0 = std::chrono::steady_clock::now();
        while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s) {
            K22Batch b; k22_batch(p, ep->E, header, nonce, b); id<MTLRenderPipelineState> pso; if (frag_form ? !cpipeline(b) : !pipeline(b, pso)) return 2;
            for (uint64_t done = 0; done < per_batch && std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s; done += K) {
                const uint32_t n = (uint32_t)std::min<uint64_t>(K, per_batch - done);
                for (uint32_t h = 0; h < n; h++) { hs[h].height = 1; hs[h].nonce = nonce + done + h; memcpy(hs[h].header, header, 32); k22_scene(p, P, ep->E, tile_count, hs[h]); }
                if (frag_form ? !crender(*ep, b, hs.data(), n) : !render(pso, *ep, b, hs.data(), n)) return 2; hashes += n;
            }
            nonce += per_batch;
        }
        const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        printf("{\"bench\": {\"device\": \"%s\", \"backend\": \"metal\", \"version\": 22, \"rounds\": %u, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.4f, \"batches\": %llu, \"compile_ms_per_batch\": %.2f, \"hashes_per_render\": %u, \"note\": \"the fragment programme compiled a batch; K hashes a render on one buffer-backed R32Uint target in a raster order group; the digests on the host\"}}\n",
               device.c_str(), T, secs, (unsigned long long)hashes, hashes / secs / 1e6, (unsigned long long)batches, batches ? compile_ms / batches : 0.0, K);
        fflush(stdout); return 0;
    }
    // the vectors: every record rendered one hash a render (the batch's pipeline rebuilt when the batch changes), mix and hash against the file
    size_t total = 0, mism = 0, unread = 0;
    for (const std::string &f : files) {
        std::string txt; { FILE *fp = fopen(f.c_str(), "rb"); if (!fp) { fprintf(stderr, "vectors: cannot read %s\n", f.c_str()); unread++; continue; } char buf[65536]; size_t n; while ((n = fread(buf, 1, sizeof buf, fp)) > 0) txt.append(buf, n); fclose(fp); }
        Json j; if (!Json::parse(txt, j)) { fprintf(stderr, "vectors: %s: not JSON\n", f.c_str()); unread++; continue; }
        const std::string cls = j.has("class") ? j["class"].as_string() : "?"; size_t n = 0, m = 0; const Json &vs = j["vectors"];
        uint8_t cur_seed[32] = {0}; bool have_pso = false; id<MTLRenderPipelineState> pso = nil; K22Batch b;
        for (size_t i = 0; i < vs.size(); i++) {
            const Json &v = vs.at(i); auto num = [](const Json &x) -> uint64_t { return x.type() == Json::String ? strtoull(x.as_string().c_str(), nullptr, 10) : x.as_u64(); };
            K22Hash h; h.height = num(v["height"]); h.nonce = num(v["nonce"]); uint8_t want_mix[32], want_hash[32];
            if (!r18_hex(v["header"].as_string(), h.header, 32) || !r18_hex(v["mix"].as_string(), want_mix, 32) || !r18_hex(v["hash"].as_string(), want_hash, 32)) { fprintf(stderr, "vectors: a malformed record in %s\n", f.c_str()); m++; n++; continue; }
            Epoch *ep = nullptr; if (!epoch_for(h.height, ep)) return 2;
            mark("vector: batch"); K22Batch nb; k22_batch(p, ep->E, h.header, h.nonce, nb); mark("vector: batch made");
            if (!have_pso || memcmp(nb.seed, cur_seed, 32) != 0) { b = nb; mark("vector: pipeline"); if (frag_form ? !cpipeline(b) : !pipeline(b, pso)) return 2; memcpy(cur_seed, b.seed, 32); have_pso = true; }
            k22_scene(p, P, ep->E, tile_count, h); if (frag_form ? !crender(*ep, b, &h, 1) : !render(pso, *ep, b, &h, 1)) return 2;
            n++; if (memcmp(h.M, want_mix, 32) != 0 || memcmp(h.F, want_hash, 32) != 0) { m++; if (m <= 4) fprintf(stderr, "vectors: MISMATCH %zu height %llu nonce %llu\n  want mix %s hash %s\n  got  mix %s hash %s\n", m, (unsigned long long)h.height, (unsigned long long)h.nonce, hex_of(want_mix, 32).c_str(), hex_of(want_hash, 32).c_str(), hex_of(h.M, 32).c_str(), hex_of(h.F, 32).c_str()); }
        }
        fprintf(stderr, "vectors: igneumhash class %s: %zu vectors, %zu mismatches, 0 unavailable (open refused)\n", cls.c_str(), n, m);
        printf("{\"class\": \"%s\", \"vectors\": %zu, \"mismatches\": %zu, \"refused\": 0}\n", cls.c_str(), n, m); total += n; mism += m;
    }
    printf("{\"summary\": {\"device\": \"%s\", \"arch\": \"%s\", \"vectors\": %zu, \"mismatches\": %zu, \"refused\": 0, \"refused_room\": 0, \"verdict\": \"%s\", \"notes\": \"metal kiln22: the fragment programme compiled a batch, one hash a render, the blend in the fragment on an R32Uint target in a raster order group, %llu pipelines at %.1f ms each\"}}\n",
           device.c_str(), device.c_str(), total, mism, mism == 0 && unread == 0 ? "exact" : "mismatch", (unsigned long long)batches, batches ? compile_ms / batches : 0.0);
    for (int q = 0; q < n_epochs; q++) if (epochs[q].inner) ignh_inner_destroy(epochs[q].inner);
    fflush(stdout); return mism == 0 && unread == 0 ? 0 : 3;
}

// KILN, the proof of concept (the founder's line of 14 Oct 2026 23:0x UK): per batch a fragment shader drawn from a seed (a few dozen integer
// operations and nearest texel fetches in drawn order over a 4 MiB keyed R32Uint tile), compiled by Metal at runtime (the compile timed);
// per hash a scene from the seed (eight integer-corner triangles over a 64 x 64 R32Uint target, the top-left rule the rasteriser's, no
// blending: the last triangle wins, named below), the tile BLAKE3-digested; 30 s of batches; sixteen hashes re-rendered in software from the
// same operation list and scene and compared. NOT in this harness: integer blending (R32Uint targets do not blend), the keyed read of the
// gigabyte, the power reading, the share target, any claim of exactness beyond the sixteen compared.
struct KilnOp { uint32_t kind, a, b, imm; };
static uint32_t kiln_mix(uint32_t h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }
static std::vector<KilnOp> kiln_program(uint32_t seed, int n) { std::vector<KilnOp> p(n); for (int i = 0; i < n; i++) { const uint32_t s = kiln_mix(seed ^ (uint32_t)(i * 0x9E3779B1u)); p[i] = { s % 7, (s >> 3) & 3, (s >> 5) & 3, kiln_mix(s) }; } return p; }
static std::string kiln_msl(const std::vector<KilnOp> &p) {
    std::string b = "#include <metal_stdlib>\nusing namespace metal;\nstruct VIn { float2 p [[attribute(0)]]; uint c [[attribute(1)]]; };\nstruct VOut { float4 pos [[position]]; uint c [[flat]]; };\n"
        "vertex VOut k_vs(VIn v [[stage_in]]) { VOut o; o.pos = float4(v.p, 0.0, 1.0); o.c = v.c; return o; }\n"
        "uint kmix(uint h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }\n"
        "fragment uint k_fs(VOut i [[stage_in]], texture2d<uint, access::read> t [[texture(0)]]) {\n    uint r[4]; uint px = uint(i.pos.x), py = uint(i.pos.y) & 63u; r[0] = i.c; r[1] = px * 64u + py; r[2] = kmix(i.c ^ px); r[3] = py;\n";
    for (const KilnOp &o : p) { const std::string a = "r[" + std::to_string(o.a) + "]", bb = "r[" + std::to_string(o.b) + "]", im = std::to_string(o.imm) + "u";
        switch (o.kind) { case 0: b += "    " + a + " = " + a + " + " + bb + ";\n"; break; case 1: b += "    " + a + " = " + a + " ^ " + im + ";\n"; break; case 2: b += "    " + a + " = " + a + " * " + bb + ";\n"; break;
            case 3: b += "    " + a + " = rotate(" + a + ", " + std::to_string(o.imm & 31u) + "u);\n"; break; case 4: b += "    " + a + " = " + a + " - " + bb + ";\n"; break;
            case 5: b += "    { uint v = kmix(" + bb + " ^ " + im + "); " + a + " = t.read(uint2(v & 1023u, (v >> 10) & 1023u)).x; }\n"; break; default: b += "    " + a + " = kmix(" + a + " ^ " + bb + ");\n"; break; } }
    b += "    return r[0] ^ r[1] ^ r[2] ^ r[3];\n}\n"; return b;
}
static uint32_t kiln_cpu(const std::vector<KilnOp> &p, const std::vector<uint32_t> &tex, uint32_t c, uint32_t px, uint32_t py) {
    uint32_t r[4] = { c, px * 64u + py, kiln_mix(c ^ px), py };
    for (const KilnOp &o : p) { uint32_t &a = r[o.a]; const uint32_t bb = r[o.b];
        switch (o.kind) { case 0: a = a + bb; break; case 1: a = a ^ o.imm; break; case 2: a = a * bb; break; case 3: { const uint32_t k = o.imm & 31u; a = k ? ((a << k) | (a >> (32 - k))) : a; break; } case 4: a = a - bb; break;
            case 5: { const uint32_t v = kiln_mix(bb ^ o.imm); a = tex[(size_t)((v >> 10) & 1023u) * 1024 + (v & 1023u)]; } break; default: a = kiln_mix(a ^ bb); break; } }
    return r[0] ^ r[1] ^ r[2] ^ r[3];
}
struct KilnTri { int x0, y0, x1, y1, x2, y2; uint32_t c; };
static std::vector<KilnTri> kiln_scene(uint32_t hseed) { std::vector<KilnTri> t(8); for (int i = 0; i < 8; i++) { const uint32_t s = kiln_mix(hseed + (uint32_t)i * 0x2545F491u), u = kiln_mix(s ^ 0x9E3779B9u); KilnTri q; q.x0 = s & 63; q.y0 = (s >> 6) & 63; q.x1 = std::min(64, std::max(0, q.x0 + (int)(u & 31) - 15)); q.y1 = std::min(64, std::max(0, q.y0 + (int)((u >> 5) & 31) - 15)); q.x2 = std::min(64, std::max(0, q.x0 + (int)((u >> 10) & 31) - 15)); q.y2 = std::min(64, std::max(0, q.y0 + (int)((u >> 15) & 31) - 15)); q.c = kiln_mix(u); t[i] = q; } return t; }
static void kiln_cpu_tile(const std::vector<KilnOp> &p, const std::vector<uint32_t> &tex, const std::vector<KilnTri> &sc, std::vector<uint32_t> &tile) {
    tile.assign(64 * 64, 0);
    for (const KilnTri &t : sc) { const int64_t ax = t.x0, ay = t.y0, bx = t.x1, by = t.y1, cx = t.x2, cy = t.y2; const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (!area) continue; const int64_t s = area > 0 ? 1 : -1;
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; k++) { const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay; e[k].tl = dy == 0 ? ((dx > 0 ? 1 : -1) == s) : ((-dy > 0 ? 1 : -1) == s); }
        for (int64_t j = std::min(ay, std::min(by, cy)); j < std::max(ay, std::max(by, cy)); j++) for (int64_t i = std::min(ax, std::min(bx, cx)); i < std::max(ax, std::max(bx, cx)); i++) { bool in = true; for (int k = 0; k < 3 && in; k++) { const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (2 * j + 1 - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (2 * i + 1 - 2 * e[k].ax)) * s; if (w < 0 || (w == 0 && !e[k].tl)) in = false; } if (in) tile[(size_t)j * 64 + i] = kiln_cpu(p, tex, t.c, (uint32_t)i, (uint32_t)j); } }
}
static int metal_kiln(id<MTLDevice> mtl, double seconds) {
    using namespace igm;
    const uint32_t K = 256; std::vector<uint32_t> tex((size_t)1024 * 1024); for (size_t i = 0; i < tex.size(); i++) tex[i] = kiln_mix((uint32_t)i * 0x9E3779B1u + 7u);
    MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatR32Uint width:1024 height:1024 mipmapped:NO]; td.usage = MTLTextureUsageShaderRead; td.storageMode = MTLStorageModeShared;
    id<MTLTexture> t = [mtl newTextureWithDescriptor:td]; [t replaceRegion:MTLRegionMake2D(0, 0, 1024, 1024) mipmapLevel:0 withBytes:tex.data() bytesPerRow:4096];
    MTLTextureDescriptor *rd = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatR32Uint width:64 height:64 * K mipmapped:NO]; rd.usage = MTLTextureUsageRenderTarget | MTLTextureUsageShaderRead; rd.storageMode = MTLStorageModeShared;
    id<MTLTexture> target = [mtl newTextureWithDescriptor:rd]; id<MTLCommandQueue> q = [mtl newCommandQueue];
    MTLVertexDescriptor *vd = [MTLVertexDescriptor vertexDescriptor]; vd.attributes[0].format = MTLVertexFormatFloat2; vd.attributes[0].offset = 0; vd.attributes[0].bufferIndex = 0; vd.attributes[1].format = MTLVertexFormatUInt; vd.attributes[1].offset = 8; vd.attributes[1].bufferIndex = 0; vd.layouts[0].stride = 12;
    struct V { float x, y; uint32_t c; }; std::vector<V> verts; std::vector<uint32_t> tiles((size_t)64 * 64 * K);
    uint64_t hashes = 0, batches = 0; double compile_ms = 0; size_t cmp = 0, mism = 0; std::string first_digest;
    auto t0 = std::chrono::steady_clock::now();
    while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < seconds) {
        const uint32_t bseed = kiln_mix((uint32_t)batches * 0x27d4eb2fu + 1u); const std::vector<KilnOp> prog = kiln_program(bseed, 32); const std::string msl = kiln_msl(prog);
        auto c0 = std::chrono::steady_clock::now(); NSError *err = nil; id<MTLLibrary> lib = [mtl newLibraryWithSource:[NSString stringWithUTF8String:msl.c_str()] options:nil error:&err];
        if (!lib) { printf("{\"kiln\": {\"refused\": \"shader compile: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return 2; }
        MTLRenderPipelineDescriptor *pd = [[MTLRenderPipelineDescriptor alloc] init]; pd.vertexFunction = [lib newFunctionWithName:@"k_vs"]; pd.fragmentFunction = [lib newFunctionWithName:@"k_fs"]; pd.vertexDescriptor = vd; pd.colorAttachments[0].pixelFormat = MTLPixelFormatR32Uint;
        id<MTLRenderPipelineState> pso = [mtl newRenderPipelineStateWithDescriptor:pd error:&err]; if (!pso) { printf("{\"kiln\": {\"refused\": \"pipeline\"}}\n"); return 2; }
        compile_ms += std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - c0).count();
        // the batch's K scenes into K tiles of one tall target, one render pass each hash (its scissor its tile)
        std::vector<std::vector<KilnTri>> scenes(K); verts.clear();
        for (uint32_t h = 0; h < K; h++) { scenes[h] = kiln_scene(bseed ^ kiln_mix(h + 1)); for (const KilnTri &tr : scenes[h]) { const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2}; for (int k = 0; k < 3; k++) verts.push_back({ (float)xs[k] / 32.0f - 1.0f, 1.0f - (float)(ys[k] + 64 * (int)h) / (32.0f * K), tr.c }); } }
        id<MTLBuffer> vb = [mtl newBufferWithBytes:verts.data() length:verts.size() * sizeof(V) options:MTLResourceStorageModeShared];
        MTLRenderPassDescriptor *rp = [MTLRenderPassDescriptor renderPassDescriptor]; rp.colorAttachments[0].texture = target; rp.colorAttachments[0].loadAction = MTLLoadActionClear; rp.colorAttachments[0].storeAction = MTLStoreActionStore; rp.colorAttachments[0].clearColor = MTLClearColorMake(0, 0, 0, 0);
        id<MTLCommandBuffer> cb = [q commandBuffer]; id<MTLRenderCommandEncoder> enc = [cb renderCommandEncoderWithDescriptor:rp]; [enc setRenderPipelineState:pso]; [enc setCullMode:MTLCullModeNone]; [enc setFragmentTexture:t atIndex:0]; [enc setVertexBuffer:vb offset:0 atIndex:0];
        [enc drawPrimitives:MTLPrimitiveTypeTriangle vertexStart:0 vertexCount:verts.size()]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { printf("{\"kiln\": {\"refused\": \"the batch did not complete\"}}\n"); return 2; }
        [target getBytes:tiles.data() bytesPerRow:256 fromRegion:MTLRegionMake2D(0, 0, 64, 64 * K) mipmapLevel:0];
        auto fold = [](const uint32_t *tile, uint32_t out[64]) { for (uint32_t i = 0; i < 64; i++) { uint32_t x = 0; for (uint32_t j = i; j < 4096; j += 64) x = kiln_mix(x ^ tile[j]); out[i] = x; } };   // the 256-byte fold of the 16 KiB tile (the security review's row 60f4e8d1)
        for (uint32_t h = 0; h < K; h++) { uint32_t fo[64]; fold(&tiles[(size_t)h * 4096], fo); uint8_t d[32]; ih_blake3_full((const uint8_t *)fo, 256, d); if (first_digest.empty()) first_digest = hex_of(d, 32); }
        if (cmp < 16) { for (uint32_t h = 0; h < K && cmp < 16; h++, cmp++) { std::vector<uint32_t> cpu; kiln_cpu_tile(prog, tex, scenes[h], cpu); uint32_t fa[64], fb[64]; fold(cpu.data(), fa); fold(&tiles[(size_t)h * 4096], fb); if (std::memcmp(fa, fb, 256) != 0) mism++; } }
        hashes += K; batches++;
    }
    const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    printf("{\"kiln\": {\"device\": \"%s\", \"seconds\": %.1f, \"batches\": %llu, \"hashes\": %llu, \"hashes_per_s\": %.0f, \"compile_ms_per_batch\": %.2f, \"hashes_per_batch\": %u, \"ops_per_shader\": 32, \"cpu_compared\": %zu, \"cpu_mismatches\": %zu, \"first_digest\": \"%s\", \"power\": \"not read\", \"digest\": \"BLAKE3 of a 256-byte fold of the 64 x 64 tile (64 words, each the kmix fold of 64 tile words)\", \"reads\": \"texel reads at drawn random addresses over the whole 4 MiB tile (kmix of the drawn immediate and the register)\", \"not\": \"no integer blend (R32Uint targets do not blend: the last triangle wins), no keyed gigabyte read, no share target, no power, sixteen compared only\"}}\n",
           [[mtl name] UTF8String], secs, (unsigned long long)batches, (unsigned long long)hashes, hashes / secs, batches ? compile_ms / batches : 0.0, K, cmp, mism, first_digest.c_str());
    fflush(stdout); return mism == 0 ? 0 : 3;
}

int main(int argc, char **argv) {
    @autoreleasepool {
        std::string shader_dir = exe_dir() + "/shaders";
        std::vector<std::string> vectors; std::string trace_ref, trace_out, trace_prefix; bool probe = false, allow_battery = false, allow_lpm = false, texture_verb = false, raster_verb = false, raster_vectors = false, kiln_verb = false, kiln22 = false; std::vector<std::string> kiln22_files; double kiln22_s = 0; double kiln_s = 30.0; double raster_loop_s = 0.0, raster_bench_s = 0.0; std::vector<std::string> raster_files;
        uint64_t bench_height = 0; double bench_seconds = 0; bool bench = false; std::string transitions;
        std::string fpcheck_path;
        bool boundary = false; uint32_t boundary_nonces = 64; std::string dataset_vectors; uint32_t epochs = 0; bool cold = false; double epoch_s = 120.0; double release_after = 0.0;   // Orchard OR-0 and OR-8 (the memory seat)
        double peak_gbs = 0.0, ceiling_gbs = 0.0; int share_bits = 32, verify_every = 0; std::string algo = "kawpow-0.9.4"; Json knob_json = Json::object();
        for (int a = 1; a < argc; a++) {
            std::string s = argv[a];
            if (s == "--vectors") { while (a + 1 < argc && argv[a + 1][0] != '-') vectors.push_back(argv[++a]); }
            else if (s == "vectors" && a + 1 < argc) { std::string d = argv[++a]; for (const char *c : {"nonce-boundary", "unseen", "transitions", "malformed", "tails"}) vectors.push_back(d + "/" + c + ".json"); }
            else if (s == "trace" && a + 2 < argc) { trace_ref = argv[++a]; trace_out = argv[++a]; if (a + 1 < argc && argv[a + 1][0] != '-') trace_prefix = argv[++a]; }   // the identity set: the worker's own full trace of a reference set (the OpenCL the project's verb; the M5 Max keyed-kernel reading, 12 Oct 2026)
            else if (s == "bench" && a + 2 < argc) { bench = true; bench_height = strtoull(argv[++a], nullptr, 10); bench_seconds = atof(argv[++a]); }
            else if (s == "--transitions") transitions = "warm,cold";
            else if (s == "boundary") { boundary = true; if (a + 1 < argc && argv[a + 1][0] != '-') boundary_nonces = (uint32_t)atoi(argv[++a]); }   // OR-0: the guard and 2 GiB seam test (seams.h); the height from `bench <h>` when given, else 1
            else if (s == "--dataset-vectors" && a + 1 < argc) dataset_vectors = argv[++a];   // the vector set's dataset.json as the digest ground truth for `boundary` (else the reference builds the full dataset on the host)
            else if (s == "--epochs" && a + 1 < argc) epochs = (uint32_t)atoi(argv[++a]);   // OR-8: N complete epoch transitions (epochs.h); `bench <h> <s>` gives the start height and the window per epoch
            else if (s == "--cold") cold = true;   // OR-8: the single-buffer cold form (no prepare; the rebuild is the downtime)
            else if (s == "--epoch-s" && a + 1 < argc) epoch_s = atof(argv[++a]);
            else if (s == "--release-after" && a + 1 < argc) release_after = atof(argv[++a]);   // the memory-pressure release proved on the bench (seconds into the run)
            else if (s == "--peak-gbs" && a + 1 < argc) peak_gbs = atof(argv[++a]);
            else if (s == "--ceiling-gbs" && a + 1 < argc) ceiling_gbs = atof(argv[++a]);
            else if (s == "--ceiling-form" && a + 1 < argc) setenv("IGM_CEILING_FORM", argv[++a], 1);   // A or B (the default B: the dependent chain, the outside review's rule; security's TP-147)
            else if (s == "--independent-ceiling-gbs" && a + 1 < argc) setenv("IGM_INDEPENDENT_CEILING_GBS", argv[++a], 1);   // form A beside the row   // the attainable ceiling for the row's request width (form A at that width, measured on this configuration)
            else if (s == "--share-bits" && a + 1 < argc) share_bits = atoi(argv[++a]);   // bench: the hit target 2^(256 - bits)
            else if (s == "--verify-every" && a + 1 < argc) verify_every = atoi(argv[++a]);   // bench: every n-th hit recomputed by the reference   // the configuration row's documented peak memory bandwidth, for the bandwidth fraction on the bench line
            else if (s == "--variant" && a + 1 < argc) knob_json.set("variant", Json(std::string(argv[++a])));   // the tuning variant (TUNING-R0A.md), not a consensus value   // bench: three phases, a warm then a cold epoch move, each timed
            else if (s == "--algo" && a + 1 < argc) algo = argv[++a];
            else if (s == "--params" && a + 1 < argc) knob_json.set("params", Json(std::string(argv[++a])));   // the candidate's params document (JSON text)
            else if (s == "--param" && a + 1 < argc) { std::string kv = argv[++a]; size_t eq = kv.find('='); if (eq == std::string::npos) { fprintf(stderr, "igneum-worker-metal: --param wants key=value\n"); return 2; } std::string cur = knob_json.has("params") ? knob_json["params"].as_string() : std::string(); std::string body = cur.size() > 2 ? cur.substr(1, cur.size() - 2) : std::string(); knob_json.set("params", Json("{" + body + (body.empty() ? "" : ",") + "\"" + kv.substr(0, eq) + "\":" + kv.substr(eq + 1) + "}")); }
            else if (s == "--knob" && a + 1 < argc) { std::string kv = argv[++a]; size_t e = kv.find('='); if (e != std::string::npos) { const char *v = kv.c_str() + e + 1; char *end = nullptr; double d = strtod(v, &end); if (*v && end && *end == 0) knob_json.set(kv.substr(0, e), Json(d)); else knob_json.set(kv.substr(0, e), Json(std::string(v))); } }   // a numeric value is a number; any other text is the knob's word (storage=private, pretouch=cpu, persist=pool, alloc=split2)
            else if (s == "--shaders" && a + 1 < argc) shader_dir = argv[++a];
            else if (s == "--probe" || s == "devices") probe = true;
            else if (s == "texture") texture_verb = true;   // the texture determinism probe (14 Oct 2026)
            else if (s == "raster-vectors") { raster_vectors = true; while (a + 1 < argc && argv[a + 1][0] != '-') raster_files.push_back(argv[++a]); }   // VERSION 18: the raster form's vectors (raster_worker.h)
            else if (s == "raster-bench" && a + 1 < argc) { raster_vectors = true; raster_bench_s = atof(argv[++a]); }
            else if (s == "kiln") { kiln_verb = true; if (a + 1 < argc && argv[a + 1][0] != '-') kiln_s = atof(argv[++a]); }
            else if (s == "kiln22-vectors") { kiln22 = true; while (a + 1 < argc && argv[a + 1][0] != '-') kiln22_files.push_back(argv[++a]); }   // VERSION 22: the Kiln form's vectors (kiln22-vectors <files...> --params <document>)
            else if (s == "kiln22-bench" && a + 1 < argc) { kiln22 = true; kiln22_s = atof(argv[++a]); }   // KILN, the proof of concept (14 Oct 2026 23:0x UK)
            else if (s == "raster") { raster_verb = true; if (a + 1 < argc && argv[a + 1][0] != '-') raster_loop_s = atof(argv[++a]); }   // the raster determinism probe (14 Oct 2026): raster [loop seconds]
            else if (s == "fpcheck" && a + 1 < argc) { fpcheck_path = argv[++a]; }   // version 8 FP: the fp class on this GPU; exit 0 only on 0 mismatches
            else if (s == "--fingerprint-dir" && a + 1 < argc) setenv("IGM_FINGERPRINT_DIR", argv[++a], 1);   // Orchard workstream 1: where the serialised binary archive lands (a job passes {out})
            else if (s == "protocol" || s == "--device") { if (s == "--device") a++; }   // A's command words, accepted
            else if (s == "--allow-battery") allow_battery = true;
            else if (s == "--allow-low-power-mode") allow_lpm = true;
        }
        Knobs bench_knobs; bench_knobs.from_json(knob_json);
        setenv("IGM_WORKER_SHA256", exe_sha256().c_str(), 1);
        id<MTLDevice> mtl = MTLCreateSystemDefaultDevice();
        if (!mtl) { fprintf(stderr, "igneum-worker-metal: no Metal device\n"); return 1; }
        if (texture_verb) { int trc = metal_texture_probe(mtl); fflush(stdout); return trc; }
        if (raster_verb) { int rrc = metal_raster_probe(mtl, raster_loop_s); fflush(stdout); return rrc; }
        if (kiln_verb) { int krc = metal_kiln(mtl, kiln_s); fflush(stdout); return krc; }
        if (kiln22) { int krc = metal_kiln22(mtl, kiln22_files, bench_knobs.params, kiln22_s); fflush(stdout); return krc; }
        if (raster_vectors) { int rrc = metal_raster_vectors(mtl, raster_files, bench_knobs.params, raster_bench_s); fflush(stdout); return rrc; }
        Device dev(mtl);
        MetalEngine eng_p(dev, shader_dir);
        MetalEngineX eng_x(dev, shader_dir);
        eng_p.allow_battery(allow_battery); eng_x.allow_battery(allow_battery);
        eng_p.allow_low_power_mode(allow_lpm); eng_x.allow_low_power_mode(allow_lpm);
        RasterMetalEngine eng_r(mtl);   // VERSION 18 (the raster form) under the protocol: the configure document's version picks it (igneum-worker-metal-coord6 is this binary)
        MultiEngine eng(&eng_p, &eng_x, "metal", &eng_r);
        dev.start_observers([](const SystemState &) {});

        int rc = 0;
        std::thread t([&] { @autoreleasepool {   // the worker thread's own pool (the batch loops drain their own per batch; the per-epoch leak of or-0-guard-1c)
            if (!fpcheck_path.empty()) { uint32_t feat = 16; { IhParams ip; std::string perr; if (ih_parse_params(bench_knobs.params, ip, perr)) feat = ip.features; } int m = eng_x.fp_check(fpcheck_path, feat); rc = m == 0 ? 0 : m < 0 ? 0 : 3; }   // a refusal (the class file absent or unreadable) is recorded on its line and is not a mismatch; a mismatch exits 3
            else if (!trace_ref.empty()) { g_vector_knobs = bench_knobs; int n = write_identity_set(eng, trace_ref, trace_out, trace_prefix); rc = n >= 1 ? 0 : 1; }   // a keyed set carries the trace class alone (the other four are the gate set's): one written file is the verb's success
            else if (!vectors.empty()) { g_vector_knobs = bench_knobs; rc = vectors_main(eng, vectors); }
            else if (boundary) { g_vector_knobs = bench_knobs; rc = seam_check(eng, bench_height ? bench_height : 1, boundary_nonces, dataset_vectors) == 0 ? 0 : 1; }
            else if (epochs) rc = epochs_main(eng, algo, bench_height ? bench_height : 1, epochs, cold, bench_seconds > 0 ? bench_seconds : epoch_s, "metal; thermal state and battery from the system, no power or fan reading", bench_knobs, share_bits, verify_every);
            else if (bench) rc = bench_main(eng, algo, bench_height, bench_seconds, "metal; thermal state and battery from the system, no power or fan reading", bench_knobs, transitions, peak_gbs, ceiling_gbs, share_bits, verify_every, release_after);
            else {
                size_t n = 0; bool vectors_ok = ignpow_vectors_check(nullptr, &n) == IGNPOW_OK;   // the start-up self-test the hello line reports
                eng.set_knobs(bench_knobs); rc = run_worker_loop(eng, "igneum-worker-metal", "0.0.1", algo, vectors_ok, probe);
            }
            CFRunLoopStop(CFRunLoopGetMain());
        } });
        CFRunLoopRun();
        t.join();
        dev.stop_observers();
        return rc;
    }
}
