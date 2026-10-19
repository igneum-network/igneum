// raster_gl_card.h: the version 18 card through OpenGL (the five R18Card callbacks over a RasterGl context: draw, read_boxes, restore, fill,
// dataset), shared by igneum-raster-worker (raster_worker_gl.cpp) and the GPU team's protocol binary. The card's state (the vertex and row
// buffers, the host copy of the keyed target, the reference's full context) lives in an R18GlState the callbacks share and free when
// the last R18Card copy goes. GPU engineer B, 15 October 2026.
#pragma once
#include "raster_gl_context.h"
#include "raster_worker.h"
#include <memory>
// 18c: the keyed epoch dataset from the reference library (the full context's bytes), linked on Linux; the Windows cross-build carries no reference (R18_NO_KEYED)
#ifndef R18_NO_KEYED
extern "C" void *ignh_inner_create(const char *json, uint64_t height, int full, int *status);
extern "C" const uint8_t *ignh_inner_dataset(const void *ctx, uint64_t *bytes);
extern "C" void ignh_inner_destroy(void *ctx);
#endif
namespace igm {
struct R18GlState {
    struct V { float x, y; uint8_t r, g, b, a; };
    std::vector<V> verts; std::vector<uint8_t> rowbuf;
    const uint8_t *base = nullptr;   // 18c: the host copy of the keyed target (the reference's dataset, row-major = the target's bytes)
    void *inner = nullptr;
    GLuint vbo_keyed = 0;  // F-441: the keyed shares' points
    GLuint pbo = 0;        // the pack buffer the boxes are read into (one glReadPixels a box, one map an attempt: the GPU team's reading of the NVIDIA EGL cost, 15 October 2026)
    GLuint tex_base = 0;   // 18c: the keyed target's base content as a second texture, the boxes restored by glCopyImageSubData on the card
    GLuint cprog = 0, ssbo_boxes = 0, ssbo_pre = 0, ssbo_out = 0; GLint u_side = -1;   // the digest on the card (the project's order 15:4x UK): the compute program and its three buffers
    std::vector<uint32_t> table, cvs;
    GLuint b_verts = 0, b_boxes = 0, b_pre = 0, b_out = 0;   // the batch's buffers: every attempt's vertices, box table and prefix at their own offsets, the chunk values of the whole batch read once
    ~R18GlState() {
#ifndef R18_NO_KEYED
        if (inner) ignh_inner_destroy(inner);
#endif
    }
};
// the boxes through one pack buffer: every box's glReadPixels queued into it (no round trip each), one map after the last, the rows flipped out
inline bool card_read_boxes_host(const std::shared_ptr<R18GlState> &st, RasterGl &gl, uint32_t side, const std::vector<R18Tri> &tris, std::vector<std::vector<uint8_t>> &boxes) {
        boxes.assign(tris.size(), {});
        size_t total = 0; std::vector<size_t> off(tris.size(), 0);
        for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; off[t] = total; if (w > 0 && h > 0) total += (size_t)w * h * 4; }
        if (total == 0) return glGetError() == GL_NO_ERROR;
        if (!st->pbo) gl.GenBuffers(1, &st->pbo);
        gl.BindBuffer(GL_PIXEL_PACK_BUFFER_, st->pbo); gl.BufferData(GL_PIXEL_PACK_BUFFER_, (GLsizeiptr_)total, nullptr, GL_STREAM_READ_);
        for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
            glReadPixels(tr.minx, (GLint)side - tr.maxy, w, h, GL_RGBA, GL_UNSIGNED_BYTE, (void *)(uintptr_t)off[t]); }   // GL rows from the bottom: our row j is GL row side - 1 - j
        const uint8_t *m = (const uint8_t *)gl.MapBufferRange(GL_PIXEL_PACK_BUFFER_, 0, (GLsizeiptr_)total, GL_MAP_READ_BIT_);
        if (!m) { gl.BindBuffer(GL_PIXEL_PACK_BUFFER_, 0); return false; }
        for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
            std::vector<uint8_t> &b = boxes[t]; b.resize((size_t)w * h * 4); const uint8_t *src = m + off[t];
            for (int j = 0; j < h; j++) std::memcpy(&b[(size_t)j * w * 4], src + (size_t)(h - 1 - j) * w * 4, (size_t)w * 4); }
        gl.UnmapBuffer(GL_PIXEL_PACK_BUFFER_); gl.BindBuffer(GL_PIXEL_PACK_BUFFER_, 0);
        return glGetError() == GL_NO_ERROR;
}
// the card over an opened context whose pipeline was built for r18_side(p); params = the document's JSON (the reference's context takes it); false on a refusal

// F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 3): a keyed triangle's share XORed into the first eight pixels of its box BEFORE the triangle
// is drawn, on the target's word at that moment: eight one-pixel points drawn under GL_COLOR_LOGIC_OP XOR with the share's bytes as the colour
// (the logic op replaces the blend for the points; the fixed-point RGBA8 target takes it exactly), the blend back after; a context without
// the logic op (GL ES) reads the pixels back, XORs on the host and writes them with glTexSubImage2D (slow, exact)
#ifndef GL_COLOR_LOGIC_OP_
#define GL_COLOR_LOGIC_OP_ 0x0BF2
#define GL_XOR_ 0x1506
#endif
extern "C" void glLogicOp(unsigned int);
static inline void r18_gl_keyed_xor(RasterGl &gl, R18GlState *st, uint32_t side, float half, const R18Tri &tr) {
    std::vector<R18GlState::V> pts; pts.reserve(8);
    r18_keyed_pixels(tr, [&](int j, int32_t x, int32_t y) { R18GlState::V v; v.x = ((float)x + 0.5f) / half - 1.0f; v.y = 1.0f - ((float)y + 0.5f) / half; const uint32_t w = tr.kw[j]; v.r = (uint8_t)w; v.g = (uint8_t)(w >> 8); v.b = (uint8_t)(w >> 16); v.a = (uint8_t)(w >> 24); pts.push_back(v); });
    if (pts.empty()) return;
    if (!gl.is_es()) {
        glEnable(GL_COLOR_LOGIC_OP_); glLogicOp(GL_XOR_);
        gl.BindBuffer(GL_ARRAY_BUFFER, st->vbo_keyed); gl.BufferData(GL_ARRAY_BUFFER, (GLsizeiptr_)(pts.size() * sizeof(R18GlState::V)), pts.data(), GL_DYNAMIC_DRAW_);
        gl.VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 12, (const void *)0); gl.VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, 12, (const void *)8);
        glDrawArrays(GL_POINTS, 0, (GLsizei)pts.size());
        glDisable(GL_COLOR_LOGIC_OP_);
        return;   // the caller rebinds its own vertex buffer before its next draw
    }
    uint8_t px[4]; r18_keyed_pixels(tr, [&](int j, int32_t x, int32_t y) {
        glReadPixels(x, (GLint)side - 1 - y, 1, 1, GL_RGBA, GL_UNSIGNED_BYTE, px); const uint32_t w = tr.kw[j]; px[0] ^= (uint8_t)w; px[1] ^= (uint8_t)(w >> 8); px[2] ^= (uint8_t)(w >> 16); px[3] ^= (uint8_t)(w >> 24);
        glTexSubImage2D(GL_TEXTURE_2D, 0, x, (GLint)side - 1 - y, 1, 1, GL_RGBA, GL_UNSIGNED_BYTE, px); });
}
// the attempt's triangles drawn in segments, each keyed triangle's share XORed in before its segment (vb = the bound vertex buffer, first = the attempt's first vertex)
static inline void r18_gl_draw_keyed(RasterGl &gl, R18GlState *st, uint32_t side, float half, const std::vector<R18Tri> &tris, GLuint vb, GLint first) {
    size_t seg = 0;
    for (size_t t = 0; t < tris.size(); t++) {
        if (tris[t].keyed < 0) continue;
        if (t > seg) glDrawArrays(GL_TRIANGLES, first + (GLint)(3 * seg), (GLsizei)(3 * (t - seg)));
        r18_gl_keyed_xor(gl, st, side, half, tris[t]);
        gl.BindBuffer(GL_ARRAY_BUFFER, vb); gl.VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 12, (const void *)0); gl.VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, 12, (const void *)8);
        seg = t;
    }
    if (tris.size() > seg) glDrawArrays(GL_TRIANGLES, first + (GLint)(3 * seg), (GLsizei)(3 * (tris.size() - seg)));
}

inline bool r18_gl_card(RasterGl &gl, const IhParams &p, const std::string &params, R18Card &card) {
    const uint32_t side = r18_side(p); const float half = (float)(side / 2);
    if (gl.side != side) { printf("{\"raster_worker\": {\"refused\": \"the context's target is %u, the document's %u\"}}\n", gl.side, side); return false; }
    auto st = std::make_shared<R18GlState>();
    typedef R18GlState::V V;
    card.dataset = [st, params](uint64_t height, uint64_t &n, std::string &e) -> const uint8_t * {
#ifdef R18_NO_KEYED
        (void)height; n = 0; e = "this build carries no reference library for the keyed target (18c)"; return nullptr;
#else
        if (st->inner) { ignh_inner_destroy(st->inner); st->inner = nullptr; }
        int rc = 0; st->inner = ignh_inner_create(params.c_str(), height, 1, &rc); if (!st->inner) { e = "the reference refused the full context (status " + std::to_string(rc) + ")"; return nullptr; }
        const uint8_t *d = ignh_inner_dataset(st->inner, &n); if (!d) { e = "the reference gave no dataset"; return nullptr; } return d;
#endif
    };
    // the fill: the target's rows are the dataset's rows; GL stores rows bottom-up, so our row j goes to GL row side - 1 - j, in blocks of 256 rows
    card.fill = [&gl, st, side](const uint8_t *d, uint64_t n) {
        if (n != (uint64_t)side * side * 4) return false; st->base = d;
        const uint32_t blk = 256; std::vector<uint8_t> stage((size_t)blk * side * 4);
        auto upload = [&]() { for (uint32_t j0 = 0; j0 < side; j0 += blk) {
            for (uint32_t j = 0; j < blk; j++) std::memcpy(&stage[(size_t)j * side * 4], &d[(size_t)(j0 + blk - 1 - j) * side * 4], (size_t)side * 4);   // stage row j = our row j0 + blk - 1 - j
            glTexSubImage2D(GL_TEXTURE_2D, 0, 0, (GLint)(side - j0 - blk), (GLsizei)side, (GLsizei)blk, GL_RGBA, GL_UNSIGNED_BYTE, stage.data()); } };
        upload();   // the target itself
        if (gl.CopyImageSubData) {   // the base copy on the card: the restore then copies box by box without a host upload
            if (!st->tex_base) { glGenTextures(1, &st->tex_base); glBindTexture(GL_TEXTURE_2D, st->tex_base); gl.make_target_storage((GLsizei)side); glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST); glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST); }
            else glBindTexture(GL_TEXTURE_2D, st->tex_base);
            if (glGetError() != GL_NO_ERROR) { glDeleteTextures(1, &st->tex_base); st->tex_base = 0; glBindTexture(GL_TEXTURE_2D, gl.tex); glFinish(); return glGetError() == GL_NO_ERROR; }   // no room for the second texture: the host-upload restore stays
            upload(); glBindTexture(GL_TEXTURE_2D, gl.tex);
        }
        glFinish(); { const GLenum e = glGetError(); if (e != GL_NO_ERROR) fprintf(stderr, "raster: the fill left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR; }
    };
    // THE DIGEST ON THE CARD: M = BLAKE3(LP(tag) || K || the boxes' bytes in order) is a tree over 1 KiB chunks, so one compute thread a chunk
    // gathers its 1,024 stream bytes (the 51-byte prefix, then the boxes row-major RGBA from the target by imageLoad, the box found by a
    // binary search over the boxes' cumulative byte offsets), compresses its 16 blocks with CHUNK_START and CHUNK_END, and writes the
    // chunk's chaining value; the host reads the chaining values (32 bytes a chunk, about 22 KB an attempt) and merges the tree
    // (ih_blake3_from_chunk_cvs). A one-chunk stream (every box empty) is hashed on the host from the pack buffer path.
    static const char *kDigestHeadGL = "#version 430\n#define HP\n";
    static const char *kDigestHeadES = "#version 310 es\nprecision highp float; precision highp int; precision highp image2D;\n#define HP highp\n";
    static const char *kDigestSrc =
        "layout(local_size_x = 64) in;\n"
        "layout(rgba8, binding = 0) readonly uniform HP image2D fb;\n"
        "layout(std430, binding = 1) readonly buffer Boxes { uint box[]; };\n"   // 5 words a box: minx, miny (ours), w, h, byte offset in the box stream; the table ends with a sentinel offset
        "layout(std430, binding = 2) readonly buffer Pre { uint pre[]; };\n"      // pre[0..12] the 51 prefix bytes LE, pre[13] = 51, pre[14] = total bytes, pre[15] = boxes
        "layout(std430, binding = 3) writeonly buffer Out { uint cvout[]; };\n"
        "uniform uint side;\n"
        "const uint IV[8] = uint[8](0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u);\n"
        "const uint PERM[16] = uint[16](2u, 6u, 3u, 10u, 7u, 0u, 4u, 13u, 1u, 11u, 12u, 5u, 9u, 14u, 15u, 8u);\n"
        "uint rotr(uint x, uint n) { return (x >> n) | (x << (32u - n)); }\n"
        "uint v[16];\n"
        "void g(uint a, uint b, uint c, uint d, uint x, uint y) { v[a] = v[a] + v[b] + x; v[d] = rotr(v[d] ^ v[a], 16u); v[c] = v[c] + v[d]; v[b] = rotr(v[b] ^ v[c], 12u); v[a] = v[a] + v[b] + y; v[d] = rotr(v[d] ^ v[a], 8u); v[c] = v[c] + v[d]; v[b] = rotr(v[b] ^ v[c], 7u); }\n"
        "void compress(inout uint cv[8], uint m[16], uint t, uint blen, uint flags) {\n"
        "  for (int i = 0; i < 8; i++) v[i] = cv[i]; v[8] = IV[0]; v[9] = IV[1]; v[10] = IV[2]; v[11] = IV[3]; v[12] = t; v[13] = 0u; v[14] = blen; v[15] = flags;\n"
        "  for (int r = 0; r < 7; r++) { g(0u,4u,8u,12u,m[0],m[1]); g(1u,5u,9u,13u,m[2],m[3]); g(2u,6u,10u,14u,m[4],m[5]); g(3u,7u,11u,15u,m[6],m[7]); g(0u,5u,10u,15u,m[8],m[9]); g(1u,6u,11u,12u,m[10],m[11]); g(2u,7u,8u,13u,m[12],m[13]); g(3u,4u,9u,14u,m[14],m[15]);\n"
        "    if (r < 6) { uint p[16]; for (int i = 0; i < 16; i++) p[i] = m[PERM[i]]; for (int i = 0; i < 16; i++) m[i] = p[i]; } }\n"
        "  for (int i = 0; i < 8; i++) cv[i] = v[i] ^ v[i + 8];\n"
        "}\n"
        "uint stream_byte(uint o) {\n"
        "  uint plen = pre[13]; if (o < plen) return (pre[o >> 2] >> (8u * (o & 3u))) & 255u;\n"
        "  uint rel = o - plen; uint nb = pre[15]; uint lo = 0u, hi = nb;\n"   // the box with off[i] <= rel < off[i + 1]
        "  while (hi - lo > 1u) { uint mid = (lo + hi) >> 1u; if (box[5u * mid + 4u] <= rel) lo = mid; else hi = mid; }\n"
        "  uint minx = box[5u * lo], miny = box[5u * lo + 1u], w = box[5u * lo + 2u]; uint r = rel - box[5u * lo + 4u];\n"
        "  uint row = r / (w * 4u), c = r - row * (w * 4u); uint x = minx + (c >> 2u), y = miny + row;\n"
        "  uvec4 px = uvec4(round(imageLoad(fb, ivec2(int(x), int(side - 1u - y))) * 255.0));\n"   // the unorm8 texel back to its byte (k/255 rounds to k exactly in fp32)
        "  uint k = c & 3u; return k == 0u ? px.r : k == 1u ? px.g : k == 2u ? px.b : px.a;\n"
        "}\n"
        "void main() {\n"
        "  uint chunk = gl_GlobalInvocationID.x; uint total = pre[14]; uint start = chunk * 1024u; if (start >= total) return;\n"
        "  uint len = min(1024u, total - start); uint cv[8]; for (int i = 0; i < 8; i++) cv[i] = IV[i];\n"
        "  uint nblk = (len + 63u) / 64u; if (nblk == 0u) nblk = 1u;\n"
        "  for (uint b = 0u; b < nblk; b++) { uint m[16]; uint bo = start + b * 64u; uint blen = min(64u, len - b * 64u);\n"
        "    for (int i = 0; i < 16; i++) { uint wv = 0u; for (uint q = 0u; q < 4u; q++) { uint o = uint(i) * 4u + q; if (o < blen) wv |= stream_byte(bo + o) << (8u * q); } m[i] = wv; }\n"
        "    uint flags = (b == 0u ? 1u : 0u) | (b + 1u == nblk ? 2u : 0u); compress(cv, m, chunk, blen, flags); }\n"
        "  for (int i = 0; i < 8; i++) cvout[8u * chunk + uint(i)] = cv[i];\n"
        "}\n";
    if (gl.has_compute()) {
        rgl_stage("raster_worker", "compiling the digest compute program");
        const std::string csrc = std::string(gl.is_es() ? kDigestHeadES : kDigestHeadGL) + kDigestSrc;
        st->cprog = gl.compute_program(csrc.c_str());
        rgl_stage("raster_worker", st->cprog ? "the digest program is built" : "no digest program (the boxes digest on the host)");
        if (st->cprog) { gl.GenBuffers(1, &st->ssbo_boxes); gl.GenBuffers(1, &st->ssbo_pre); gl.GenBuffers(1, &st->ssbo_out); gl.UseProgram(st->cprog); st->u_side = gl.GetUniformLocation(st->cprog, "side"); gl.Uniform1ui(st->u_side, side); gl.UseProgram(gl.prog); }
    }
    if (st->cprog) card.digest = [&gl, st, side](const std::vector<R18Tri> &tris, const uint8_t *K, uint8_t *M) {
        const uint32_t nb = (uint32_t)tris.size(); st->table.assign((size_t)5 * nb + 5, 0u); uint32_t off = 0;
        for (uint32_t t = 0; t < nb; t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; uint32_t *e = &st->table[(size_t)5 * t];
            e[0] = (uint32_t)tr.minx; e[1] = (uint32_t)tr.miny; e[2] = (uint32_t)(w > 0 ? w : 0); e[3] = (uint32_t)(h > 0 ? h : 0); e[4] = off; if (w > 0 && h > 0) off += (uint32_t)w * h * 4; }
        st->table[(size_t)5 * nb + 4] = off;   // the sentinel: an empty box shares its offset with the next, so the search lands on the box that owns the bytes
        // the stream: LP("IgneumHash/R0I/mix") (19 bytes) || K (32) || the boxes (off bytes)
        uint8_t prefix[64] = {0}; const char *tag = "IgneumHash/R0I/mix"; const uint32_t plen = 1u + 18u + 32u; prefix[0] = 18; std::memcpy(prefix + 1, tag, 18); std::memcpy(prefix + 19, K, 32);
        const uint32_t total = plen + off, nchunks = (total + 1023u) / 1024u;
        if (nchunks < 2) { std::vector<std::vector<uint8_t>> boxes; if (!card_read_boxes_host(st, gl, side, tris, boxes)) return false; r18_mix(K, boxes, M); return true; }   // one chunk: its own root
        uint32_t pre[16] = {0}; for (uint32_t i = 0; i < plen; i++) pre[i >> 2] |= (uint32_t)prefix[i] << (8u * (i & 3u)); pre[13] = plen; pre[14] = total; pre[15] = nb;
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_boxes); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(st->table.size() * 4), st->table.data(), GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_pre); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, 64, pre, GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_out); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)nchunks * 32, nullptr, GL_DYNAMIC_DRAW_);
        gl.MemoryBarrier(GL_FRAMEBUFFER_BARRIER_BIT_ | GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_ | GL_TEXTURE_FETCH_BARRIER_BIT_);
        gl.UseProgram(st->cprog); gl.BindImageTexture(0, gl.tex, 0, 0, 0, GL_READ_ONLY_, GL_RGBA8_);
        gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 1, st->ssbo_boxes); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 2, st->ssbo_pre); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 3, st->ssbo_out);
        gl.DispatchCompute((nchunks + 63u) / 64u, 1, 1);
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | GL_BUFFER_UPDATE_BARRIER_BIT_);
        st->cvs.resize((size_t)nchunks * 8); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_out);
        { const void *m = gl.MapBufferRange(GL_SHADER_STORAGE_BUFFER_, 0, (GLsizeiptr_)nchunks * 32, GL_MAP_READ_BIT_); if (!m) { gl.UseProgram(gl.prog); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0); return false; } std::memcpy(st->cvs.data(), m, (size_t)nchunks * 32); gl.UnmapBuffer(GL_SHADER_STORAGE_BUFFER_); }
        gl.UseProgram(gl.prog); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
        { const GLenum e = glGetError(); if (e != GL_NO_ERROR) { fprintf(stderr, "raster: the digest pass left GL error 0x%x\n", (unsigned)e); return false; } }
        ih_blake3_from_chunk_cvs(st->cvs.data(), nchunks, M); return true;
    };
    // THE BATCH (the founder's order 18:0x UK): M attempts queued back to back, each its draw, its digest pass into its own slice of one output
    // buffer and its restore on the card, the host neither mapping nor finishing between them; one glMapBufferRange after the last; the M trees
    // merged on the host. Needs the compute digest and the on-card restore (the 18c base texture); else the card has no batch and the
    // attempts run one by one. The vertex data of every attempt in one buffer (glDrawArrays by offset), the box tables and prefixes in
    // one SSBO each by BindBufferRange at 256-byte aligned offsets.
    if (st->cprog && (gl.CopyImageSubData || !st->base)) card.batch = [&gl, st, side, half](const std::vector<std::vector<R18Tri>> &attempts, const uint8_t *K, std::vector<std::array<uint8_t, 32>> &Ms) {
        const size_t M = attempts.size(); Ms.assign(M, std::array<uint8_t, 32>{});
        if (st->base && !st->tex_base) return false;   // the keyed base without a base texture (a GL below 4.3) is not batched: the restore would be a host upload a box
        // the first rule (no keyed base: st->base null) IS batched from 19:5x UK 15 October 2026 (the GPU team's read on the research chain's lane 20, the registered coordinate 20 document = version 18 rounds 1024 without a dataset): its restore is the scissored clear a box, queued on the card like the copies
        // the prefix: LP("IgneumHash/R0I/mix") || K, the same for every attempt of a job
        uint8_t prefix[64] = {0}; const char *tag = "IgneumHash/R0I/mix"; const uint32_t plen = 1u + 18u + 32u; prefix[0] = 18; std::memcpy(prefix + 1, tag, 18); std::memcpy(prefix + 19, K, 32);
        // the tables: per attempt 5 words a box + the sentinel, padded to 256 bytes; the pre blocks 64 bytes each padded to 256; the out slices by chunk count
        std::vector<uint32_t> tables; std::vector<uint32_t> pres; std::vector<size_t> toff(M), poff(M), ooff(M), nch(M); std::vector<bool> single(M, false);
        std::vector<R18GlState::V> verts; std::vector<size_t> voff(M), vcount(M);
        size_t outw = 0;
        for (size_t a = 0; a < M; a++) {
            const std::vector<R18Tri> &tris = attempts[a]; const uint32_t nb = (uint32_t)tris.size();
            voff[a] = verts.size(); for (const R18Tri &tr : tris) { const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2}; for (int k = 0; k < 3; k++) { R18GlState::V v; v.x = (float)xs[k] / half - 1.0f; v.y = 1.0f - (float)ys[k] / half; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); verts.push_back(v); } }
            vcount[a] = verts.size() - voff[a];
            toff[a] = tables.size(); uint32_t off = 0;
            for (uint32_t t = 0; t < nb; t++) { const R18Tri &tr = tris[t]; const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; tables.push_back((uint32_t)tr.minx); tables.push_back((uint32_t)tr.miny); tables.push_back((uint32_t)(w > 0 ? w : 0)); tables.push_back((uint32_t)(h > 0 ? h : 0)); tables.push_back(off); if (w > 0 && h > 0) off += (uint32_t)w * h * 4; }
            for (int q = 0; q < 4; q++) tables.push_back(0u); tables.push_back(off);
            while ((tables.size() * 4) % 256) tables.push_back(0u);
            const uint32_t total = plen + off; nch[a] = (total + 1023u) / 1024u; single[a] = nch[a] < 2;
            poff[a] = pres.size(); uint32_t pre[16] = {0}; for (uint32_t i = 0; i < plen; i++) pre[i >> 2] |= (uint32_t)prefix[i] << (8u * (i & 3u)); pre[13] = plen; pre[14] = total; pre[15] = nb;
            for (int i = 0; i < 16; i++) pres.push_back(pre[i]); for (int i = 0; i < 48; i++) pres.push_back(0u);   // 256 bytes an attempt
            ooff[a] = outw; outw += ((nch[a] * 8 + 63) / 64) * 64;   // the slice in words, 256-byte aligned
        }
        if (!st->b_verts) { gl.GenBuffers(1, &st->b_verts); gl.GenBuffers(1, &st->b_boxes); gl.GenBuffers(1, &st->b_pre); gl.GenBuffers(1, &st->b_out); }
        if (!st->vbo_keyed) gl.GenBuffers(1, &st->vbo_keyed);
        gl.BindBuffer(GL_ARRAY_BUFFER, st->b_verts); gl.BufferData(GL_ARRAY_BUFFER, (GLsizeiptr_)(verts.size() * sizeof(R18GlState::V)), verts.data(), GL_STATIC_DRAW_);
        gl.VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 12, (const void *)0); gl.VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, 12, (const void *)8);   // the batch buffer in place of the card's one-attempt vbo for this batch
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->b_boxes); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(tables.size() * 4), tables.data(), GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->b_pre); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(pres.size() * 4), pres.data(), GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->b_out); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(outw ? outw * 4 : 64), nullptr, GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
        for (size_t a = 0; a < M; a++) {
            // the draw
            gl.UseProgram(gl.prog); r18_gl_draw_keyed(gl, st.get(), side, half, attempts[a], st->b_verts, (GLint)voff[a]);   // F-441: the keyed shares in before their triangles
            if (!single[a]) {
                gl.MemoryBarrier(GL_FRAMEBUFFER_BARRIER_BIT_ | GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_ | GL_TEXTURE_FETCH_BARRIER_BIT_);
                gl.UseProgram(st->cprog); gl.BindImageTexture(0, gl.tex, 0, 0, 0, GL_READ_ONLY_, GL_RGBA8_);
                gl.BindBufferRange(GL_SHADER_STORAGE_BUFFER_, 1, st->b_boxes, (ptrdiff_t)(toff[a] * 4), (GLsizeiptr_)((attempts[a].size() * 5 + 5) * 4));
                gl.BindBufferRange(GL_SHADER_STORAGE_BUFFER_, 2, st->b_pre, (ptrdiff_t)(poff[a] * 4), 64);
                gl.BindBufferRange(GL_SHADER_STORAGE_BUFFER_, 3, st->b_out, (ptrdiff_t)(ooff[a] * 4), (GLsizeiptr_)(nch[a] * 32));
                gl.DispatchCompute((GLuint)((nch[a] + 63u) / 64u), 1, 1);
                gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_);
            } else {   // a one-chunk stream (every box empty): its own root, read through the pack buffer here (rare)
                std::vector<std::vector<uint8_t>> boxes; if (!card_read_boxes_host(st, gl, side, attempts[a], boxes)) return false; r18_mix(K, boxes, Ms[a].data());
            }
            // the restore on the card: the keyed bytes back from the base texture (18c), or the boxes cleared to zero (the first rule)
            if (st->tex_base) {
                for (const R18Tri &tr : attempts[a]) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
                    gl.CopyImageSubData(st->tex_base, GL_TEXTURE_2D, 0, tr.minx, (GLint)side - tr.maxy, 0, gl.tex, GL_TEXTURE_2D, 0, tr.minx, (GLint)side - tr.maxy, 0, w, h, 1); }
            } else {
                glEnable(GL_SCISSOR_TEST);
                for (const R18Tri &tr : attempts[a]) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue; glScissor(tr.minx, (GLint)side - tr.maxy, w, h); glClear(GL_COLOR_BUFFER_BIT); }
                glDisable(GL_SCISSOR_TEST);
            }
        }
        gl.UseProgram(gl.prog);
        // the one readback of the batch
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | GL_BUFFER_UPDATE_BARRIER_BIT_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->b_out);
        const uint32_t *m = outw ? (const uint32_t *)gl.MapBufferRange(GL_SHADER_STORAGE_BUFFER_, 0, (GLsizeiptr_)(outw * 4), GL_MAP_READ_BIT_) : nullptr;
        if (outw && !m) { gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0); return false; }
        for (size_t a = 0; a < M; a++) if (!single[a]) ih_blake3_from_chunk_cvs(m + ooff[a], nch[a], Ms[a].data());
        if (m) gl.UnmapBuffer(GL_SHADER_STORAGE_BUFFER_); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
        // the card's own vbo back for the one-attempt path
        gl.BindBuffer(GL_ARRAY_BUFFER, gl.vbo); gl.VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 12, (const void *)0); gl.VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, 12, (const void *)8);
        const GLenum e = glGetError(); if (e != GL_NO_ERROR) { fprintf(stderr, "raster: the batch left GL error 0x%x\n", (unsigned)e); return false; }
        return true;
    };
    card.draw = [&gl, st, side, half](const std::vector<R18Tri> &tris) {
        st->verts.resize(tris.size() * 3);
        for (size_t t = 0; t < tris.size(); t++) { const R18Tri &tr = tris[t]; const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2};
            for (int k = 0; k < 3; k++) { V &v = st->verts[t * 3 + k]; v.x = (float)xs[k] / half - 1.0f; v.y = 1.0f - (float)ys[k] / half; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); } }
        gl.BufferData(GL_ARRAY_BUFFER, (GLsizeiptr_)(st->verts.size() * sizeof(V)), st->verts.data(), GL_STATIC_DRAW_);
        if (!st->vbo_keyed) gl.GenBuffers(1, &st->vbo_keyed);
        r18_gl_draw_keyed(gl, st.get(), side, half, tris, gl.vbo, 0); glFinish(); const GLenum e = glGetError();   // F-441: the keyed shares in before their triangles
        if (e != GL_NO_ERROR) fprintf(stderr, "raster: the draw left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR;
    };
    // the boxes through one pack buffer: every box's glReadPixels queued into it (no round trip each), one map after the last, the rows flipped out
    card.read_boxes = [&gl, st, side](const std::vector<R18Tri> &tris, std::vector<std::vector<uint8_t>> &boxes) { return card_read_boxes_host(st, gl, side, tris, boxes); };
    // the restore: the boxes back to zero (scissored clears) under the first rule, or to the keyed bytes (glTexSubImage2D from the host copy, rows flipped) under 18c
    card.restore = [&gl, st, side](const std::vector<R18Tri> &tris) {
        if (st->base && st->tex_base && gl.CopyImageSubData) {   // the keyed bytes back from the base texture on the card, one copy a box
            for (const R18Tri &tr : tris) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
                gl.CopyImageSubData(st->tex_base, GL_TEXTURE_2D, 0, tr.minx, (GLint)side - tr.maxy, 0, gl.tex, GL_TEXTURE_2D, 0, tr.minx, (GLint)side - tr.maxy, 0, w, h, 1); }
            { const GLenum e = glGetError(); if (e != GL_NO_ERROR) fprintf(stderr, "raster: the copy-image restore left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR; }
        }
        if (st->base) {
            for (const R18Tri &tr : tris) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue;
                st->rowbuf.resize((size_t)w * h * 4); for (int j = 0; j < h; j++) std::memcpy(&st->rowbuf[(size_t)j * w * 4], &st->base[((size_t)(tr.maxy - 1 - j) * side + (size_t)tr.minx) * 4], (size_t)w * 4);
                glTexSubImage2D(GL_TEXTURE_2D, 0, tr.minx, (GLint)side - tr.maxy, w, h, GL_RGBA, GL_UNSIGNED_BYTE, st->rowbuf.data()); }
            glFinish(); { const GLenum e = glGetError(); if (e != GL_NO_ERROR) fprintf(stderr, "raster: the upload restore left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR; }
        }
        glEnable(GL_SCISSOR_TEST);
        for (const R18Tri &tr : tris) { const int w = tr.maxx - tr.minx, h = tr.maxy - tr.miny; if (w <= 0 || h <= 0) continue; glScissor(tr.minx, (GLint)side - tr.maxy, w, h); glClear(GL_COLOR_BUFFER_BIT); }
        glDisable(GL_SCISSOR_TEST); glFinish(); { const GLenum e = glGetError(); if (e != GL_NO_ERROR) fprintf(stderr, "raster: the clear restore left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR; }
    };
    return true;
}
}
