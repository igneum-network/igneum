// raster_gl_v19.h: VERSION 19 through the graphics pipeline (RASTER-V19-DESIGN.md section 11 in its first exact form, the GPU team, 18 October
// 2026). A batch of N attempts: the host builds the attempts' boxes (raster19.h) and uploads one record a box; pass D is ONE instanced
// draw of a quad an instance (a box), the vertex shader placing the quad on the box's cell of the atlas (an R32UI integer texture on its own
// framebuffer) and the fragment shader computing each pixel's word from the keyed base alone: the base texel at the surface position,
// the integer cover test (the reference's edge functions and top-left rule in coordinates relative to the box) and the saturating add
// where covered (R1, R2: the base is only read); pass F is one compute thread a box folding its cell's pixels row-major into (a, b)
// (R4); the accumulators come back to the host, which digests them (this cut; the digest on the card is the rate cut). No fixed-function
// fill rule, no blend state and no restore: the atlas is overwritten whole by the next batch. GL 4.3 core (the compute and the SSBO in
// the vertex shader); the ES 3.1 dialect is the ANGLE cut.
#pragma once
#include "raster_gl_card.h"
#include "../common/raster19.h"
namespace igm {
#ifndef GL_WRITE_ONLY_
#define GL_WRITE_ONLY_ 0x88B9
#endif
#ifndef GL_READ_WRITE_
#define GL_READ_WRITE_ 0x88BA
#endif
typedef void (*PF_DrawArraysInstanced)(GLenum, GLint, GLsizei, GLsizei); typedef void (*PF_ActiveTexture)(GLenum); typedef void (*PF_Uniform1i)(GLint, GLint);
typedef void (*PF_VertexAttribIPointer)(GLuint, GLint, GLenum, GLsizei, const void *); typedef void (*PF_VertexAttribDivisor)(GLuint, GLuint);
struct R19GlState {
    GLuint prog = 0, fprog = 0, ssbo_boxes = 0, ssbo_acc = 0, atlas = 0, fbo = 0, vao = 0;
    GLuint ssbo_sorted = 0, ssbo_hist = 0, vao_sorted = 0, sprog_hist = 0, sprog_scan = 0, sprog_scatter = 0; GLint sh_n = -1, ss_n = -1; bool sorted = false;   // pass S (raster_gl_v19_card.h): the records in surface-row order
    GLuint gprog = 0, hprog = 0, ssbo_nonce = 0, ssbo_seed = 0, ssbo_out = 0, ssbo_hits = 0;   // the full card path (raster_gl_v19_card.h)
    GLint g_n = -1, g_t = -1, g_side = -1, g_reach = -1, g_clo = -1, g_chi = -1, g_ver = -1, g_P = -1, g_K = -1, g_hdr = -1, h_n = -1, h_t = -1, h_clo = -1, h_chi = -1, h_ver = -1, h_P = -1, h_K = -1, h_tg = -1;
    void (*Uniform1uiv)(GLint, GLsizei, const GLuint *) = nullptr;
    GLint u_cs = -1, u_cpr = -1, u_aw = -1, u_ah = -1, u_side = -1, u_base = -1, f_cs = -1, f_cpr = -1, f_n = -1;
    PF_DrawArraysInstanced DrawArraysInstanced = nullptr; PF_ActiveTexture ActiveTexture = nullptr; PF_Uniform1i Uniform1i = nullptr; PF_VertexAttribIPointer VertexAttribIPointer = nullptr; PF_VertexAttribDivisor VertexAttribDivisor = nullptr;
    uint32_t aw = 0, ah = 0, cells = 0; const uint8_t *base = nullptr;
    std::vector<uint32_t> recs; std::vector<uint8_t> accs;
};
// the shaders: a box record is four uvec4s (x0 y0 x1 y1 | x2 y2 minx miny | w h rgba a0 | b0 0 0 0); the draw takes the records as INSTANCED
// ATTRIBUTES (the record buffer bound as the array buffer, four uvec4 attributes a box with divisor 1) so the vertex and fragment stages
// carry no storage block: the one text serves GL 4.3 core and GL ES 3.1 under ANGLE on Direct3D 11, where a vertex shader may have none.
// The ES dialect: the version line, highp, and the image qualifiers of ES 3.1
static const char *kV19VertBody =
    "layout(location = 0) in uvec4 r0; layout(location = 1) in uvec4 r1; layout(location = 2) in uvec4 r2;\n"
    "uniform uint cs, cpr, aw, ah;\n"
    "flat out uvec4 f0; flat out uvec4 f1; flat out uint fcol; flat out ivec2 cell;\n"
    "void main() {\n"
    "  uint box = uint(gl_InstanceID); uint w = r2.x, h = r2.y; f0 = r0; f1 = r1; fcol = r2.z;\n"
    "  cell = ivec2(int((box % cpr) * cs), int((box / cpr) * cs));\n"
    "  vec2 corner = vec2(float((gl_VertexID & 1) != 0 ? w : 0u), float((gl_VertexID & 2) != 0 ? h : 0u));\n"
    "  vec2 px = vec2(cell) + corner; gl_Position = vec4(px.x / float(aw) * 2.0 - 1.0, px.y / float(ah) * 2.0 - 1.0, 0.0, 1.0);\n"
    "}\n";
static const char *kV19FragBody =
    "uniform HP sampler2D base; uniform uint side;\n"
    "flat in uvec4 f0; flat in uvec4 f1; flat in uint fcol; flat in ivec2 cell; out HP uint o;\n"
    "bool covered(ivec2 l, uvec4 r0, uvec4 r1) {\n"   // the reference's rule in coordinates relative to the box: 2 x (vertex - min), the sample at 2 x local + 1
    "  int ax = int(r0.x) - int(r1.z), ay = int(r0.y) - int(r1.w), bx = int(r0.z) - int(r1.z), by = int(r0.w) - int(r1.w), cx = int(r1.x) - int(r1.z), cy = int(r1.y) - int(r1.w);\n"
    "  int area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (area == 0) return false; int s = area > 0 ? 1 : -1;\n"
    "  int px = 2 * l.x + 1, py = 2 * l.y + 1;\n"
    "  int ex[3] = int[3](ax, bx, cx), ey[3] = int[3](ay, by, cy), fx[3] = int[3](bx, cx, ax), fy[3] = int[3](by, cy, ay);\n"
    "  for (int k = 0; k < 3; k++) { int dx = fx[k] - ex[k], dy = fy[k] - ey[k]; bool tl = dy == 0 ? ((dx > 0 ? 1 : -1) == s) : ((-dy > 0 ? 1 : -1) == s);\n"
    "    int w = ((2 * fx[k] - 2 * ex[k]) * (py - 2 * ey[k]) - (2 * fy[k] - 2 * ey[k]) * (px - 2 * ex[k])) * s; if (w < 0 || (w == 0 && !tl)) return false; }\n"
    "  return true;\n"
    "}\n"
    "void main() {\n"
    "  uvec4 r0 = f0, r1 = f1;\n"
    "  ivec2 l = ivec2(gl_FragCoord.xy) - cell; ivec2 sp = ivec2(int(r1.z) + l.x, int(r1.w) + l.y);\n"   // the surface pixel
    // THE DRAW PROBE (a tool build's compile flag, a figure form, never a row; the digests wrong by construction): 1 = no base fetch (the base word a constant),
    // 2 = no cover test (every pixel covered), 3 = neither (the write alone), to split pass D's time into the fetch, the test and the raster
    "#if DPROBE == 1 || DPROBE == 3\n  uvec4 b = uvec4(uint(sp.x) & 255u, uint(sp.y) & 255u, 7u, 9u);\n#else\n"
    "  uvec4 b = uvec4(round(texelFetch(base, ivec2(sp.x, int(side) - 1 - sp.y), 0) * 255.0));\n#endif\n"   // our row y is the base texture's GL row side - 1 - y (the fill's order)
    "#if DPROBE == 2 || DPROBE == 3\n  if (true) {\n#else\n  if (covered(l, r0, r1)) {\n#endif\n"
    "    uint c = fcol; b = min(b + uvec4(c & 255u, (c >> 8) & 255u, (c >> 16) & 255u, (c >> 24) & 255u), uvec4(255u)); }\n"
    "  o = b.x | (b.y << 8) | (b.z << 16) | (b.w << 24);\n"
    "}\n";
static const char *kV19HeadGL = "#version 430 core\n#define HP\n";
static const char *kV19HeadES = "#version 310 es\nprecision highp float; precision highp int; precision highp sampler2D;\n#define HP highp\n";
static const char *kV19HeadCompGL = "#version 430\n#define HP\n";
static const char *kV19HeadCompES = "#version 310 es\nprecision highp float; precision highp int; precision highp image2D;\n#define HP highp\n";
// the fold: one thread a box; the pair carried across the cell's rows in order (R4B: b mixed into a each step, the review lead's ruling 49e21b472)
static const char *kV19Fold =
    "layout(local_size_x = 64) in;\n"
    "layout(r32ui, binding = 0) readonly uniform HP uimage2D atlas;\n"
    "layout(std430, binding = 1) readonly buffer Boxes { uvec4 rec[]; };\n"
    "layout(std430, binding = 2) writeonly buffer Acc { uint acc[]; };\n"
    "uniform uint cs, cpr, nbox, sorted;\n"
    "uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }\n"
    "uint rotl(uint x, uint n) { return (x << n) | (x >> (32u - n)); }\n"
    "void main() {\n"
    "  uint box = gl_GlobalInvocationID.x; if (box >= nbox) return;\n"
    "  uvec4 r2 = rec[4u * box + 2u], r3 = rec[4u * box + 3u]; uint w = r2.x, h = r2.y; uint a = r2.w, b = r3.x;\n"
    "  ivec2 cell = ivec2(int((box % cpr) * cs), int((box / cpr) * cs));\n"
    "  for (uint y = 0u; y < h; y++) for (uint x = 0u; x < w; x++) {\n"
    "    uint v = imageLoad(atlas, cell + ivec2(int(x), int(y))).x;\n"
    "    a = fmix32(a ^ v ^ b); b = (b + a) ^ rotl(a, 13u);\n"
    "  }\n"
    "  uint ob = sorted != 0u ? r3.y : box; acc[2u * ob] = a; acc[2u * ob + 1u] = b;\n"
    "}\n";
inline GLuint r19_gl_program(RasterGl &gl, const std::string &vss, const std::string &fss) {
    const char *vs = vss.c_str(), *fs = fss.c_str();
    GLuint sv = gl.CreateShader(GL_VERTEX_SHADER_), sf = gl.CreateShader(GL_FRAGMENT_SHADER_); gl.ShaderSource(sv, 1, &vs, nullptr); gl.CompileShader(sv); gl.ShaderSource(sf, 1, &fs, nullptr); gl.CompileShader(sf);
    GLint ok = 0, ok2 = 0; gl.GetShaderiv(sv, GL_COMPILE_STATUS_, &ok); gl.GetShaderiv(sf, GL_COMPILE_STATUS_, &ok2);
    if (!ok || !ok2) { char log[2048] = {0}; gl.GetShaderInfoLog(ok ? sf : sv, sizeof log - 1, nullptr, log); fprintf(stderr, "raster: the version 19 %s shader was refused: %.600s\n", ok ? "fragment" : "vertex", log); return 0; }
    GLuint pr = gl.CreateProgram(); gl.AttachShader(pr, sv); gl.AttachShader(pr, sf); gl.LinkProgram(pr); gl.GetProgramiv(pr, GL_LINK_STATUS_, &ok);
    if (!ok) { fprintf(stderr, "raster: the version 19 draw program did not link\n"); return 0; }
    return pr;
}
// the card: the base texture is the context's target (gl.tex, side x side; filled with the dataset under the keyed form, zero under the lab form)
inline bool r19_gl_card(RasterGl &gl, const IhParams &p, const std::string &params, R19Card &card, size_t batch_n, std::shared_ptr<R19GlState> &st_out) {
    const uint32_t side = r19_side(p), cs = r19_cell(p), t = p.rounds;
    if (gl.side != side) { printf("{\"raster_worker\": {\"refused\": \"the context's target is %u, the document's base %u\"}}\n", gl.side, side); return false; }
    if (!gl.has_compute()) { printf("{\"raster_worker\": {\"refused\": \"version 19 needs the compute path (GL 4.3)\"}}\n"); return false; }
    auto st = std::make_shared<R19GlState>();
    st->DrawArraysInstanced = (PF_DrawArraysInstanced)gl_proc("glDrawArraysInstanced"); st->ActiveTexture = (PF_ActiveTexture)gl_proc("glActiveTexture"); st->Uniform1i = (PF_Uniform1i)gl_proc("glUniform1i");
    st->VertexAttribIPointer = (PF_VertexAttribIPointer)gl_proc("glVertexAttribIPointer"); st->VertexAttribDivisor = (PF_VertexAttribDivisor)gl_proc("glVertexAttribDivisor");
    if (!st->DrawArraysInstanced || !st->ActiveTexture || !st->Uniform1i || !st->VertexAttribIPointer || !st->VertexAttribDivisor) { printf("{\"raster_worker\": {\"refused\": \"glDrawArraysInstanced, glActiveTexture, glUniform1i, glVertexAttribIPointer or glVertexAttribDivisor missing\"}}\n"); return false; }
    // the atlas: cells of cs x cs, cpr a row, batch_n x t cells
    st->cells = (uint32_t)batch_n * t; st->aw = 8192; const uint32_t cpr = st->aw / cs; st->ah = ((st->cells + cpr - 1) / cpr) * cs;
    rgl_stage("raster_worker", "compiling the version 19 draw and fold programs");
    const bool es = gl.is_es();
    // THE DRAW PROBE is a TOOL BUILD's compile flag alone (-DIGM_R19_DPROBE_BUILD=1, 2 or 3 on the g++ line of a probe binary that never ships;
    // F-662: the shipped worker reads no switch to an inexact fold or draw from its environment); the probe binary says so on every start
#ifdef IGM_R19_DPROBE_BUILD
    const int dprobe = IGM_R19_DPROBE_BUILD; fprintf(stderr, "raster: THE DRAW PROBE %d is built in: the digests are wrong by construction (a figure form, never a row; this binary never ships)\n", dprobe);
#else
    const int dprobe = 0;
#endif
    st->prog = r19_gl_program(gl, std::string(es ? kV19HeadES : kV19HeadGL) + kV19VertBody, std::string(es ? kV19HeadES : kV19HeadGL) + "#define DPROBE " + std::to_string(dprobe) + "\n" + kV19FragBody); if (!st->prog) return false;
    { const std::string fsrc = std::string(es ? kV19HeadCompES : kV19HeadCompGL) + kV19Fold; st->fprog = gl.compute_program(fsrc.c_str()); if (!st->fprog) return false; }
    gl.UseProgram(st->prog); st->u_cs = gl.GetUniformLocation(st->prog, "cs"); st->u_cpr = gl.GetUniformLocation(st->prog, "cpr"); st->u_aw = gl.GetUniformLocation(st->prog, "aw"); st->u_ah = gl.GetUniformLocation(st->prog, "ah"); st->u_side = gl.GetUniformLocation(st->prog, "side"); st->u_base = gl.GetUniformLocation(st->prog, "base");
    gl.Uniform1ui(st->u_cs, cs); gl.Uniform1ui(st->u_cpr, cpr); gl.Uniform1ui(st->u_aw, st->aw); gl.Uniform1ui(st->u_ah, st->ah); gl.Uniform1ui(st->u_side, side); st->Uniform1i(st->u_base, 0);
    gl.UseProgram(st->fprog); st->f_cs = gl.GetUniformLocation(st->fprog, "cs"); st->f_cpr = gl.GetUniformLocation(st->fprog, "cpr"); st->f_n = gl.GetUniformLocation(st->fprog, "nbox");
    gl.Uniform1ui(st->f_cs, cs); gl.Uniform1ui(st->f_cpr, cpr); gl.Uniform1ui(gl.GetUniformLocation(st->fprog, "sorted"), 0u);
    rgl_stage("raster_worker", "allocating the atlas", std::to_string(st->aw) + " x " + std::to_string(st->ah) + " R32UI");
    // the atlas one 32-bit integer a pixel (the packed word the fragment writes, the fold's one integer load without the RGBA8 float conversion); immutable under ES as an image binding needs
    glGenTextures(1, &st->atlas); glBindTexture(GL_TEXTURE_2D, st->atlas);
    if (gl.is_es() && gl.TexStorage2D) gl.TexStorage2D(GL_TEXTURE_2D, 1, GL_R32UI_, (GLsizei)st->aw, (GLsizei)st->ah); else glTexImage2D(GL_TEXTURE_2D, 0, GL_R32UI_, (GLsizei)st->aw, (GLsizei)st->ah, 0, GL_RED_INTEGER_, GL_UNSIGNED_INT, nullptr);
    glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST); glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
    gl.GenFramebuffers(1, &st->fbo); gl.BindFramebuffer(GL_FRAMEBUFFER_, st->fbo); gl.FramebufferTexture2D(GL_FRAMEBUFFER_, GL_COLOR_ATTACHMENT0_, GL_TEXTURE_2D, st->atlas, 0);
    if (gl.CheckFramebufferStatus(GL_FRAMEBUFFER_) != GL_FRAMEBUFFER_COMPLETE_ || glGetError() != GL_NO_ERROR) { printf("{\"raster_worker\": {\"refused\": \"the atlas framebuffer is not complete\"}}\n"); return false; }
    gl.GenVertexArrays(1, &st->vao); gl.GenBuffers(1, &st->ssbo_boxes); gl.GenBuffers(1, &st->ssbo_acc);
    gl.BindVertexArray(st->vao); gl.BindBuffer(GL_ARRAY_BUFFER, st->ssbo_boxes);
    for (GLuint a = 0; a < 3; a++) { gl.EnableVertexAttribArray(a); st->VertexAttribIPointer(a, 4, GL_UNSIGNED_INT, 64, (const void *)(uintptr_t)(16 * a)); st->VertexAttribDivisor(a, 1); }
    gl.BindBuffer(GL_ARRAY_BUFFER, 0);
    glBindTexture(GL_TEXTURE_2D, gl.tex); gl.BindFramebuffer(GL_FRAMEBUFFER_, gl.fbo); gl.UseProgram(gl.prog);
    // the base: the dataset provider and the fill as the version 18 card's (the context's target, rows bottom-up)
    card.dataset = [st, params](uint64_t height, uint64_t &n, std::string &e) -> const uint8_t * {
#ifdef R18_NO_KEYED
        (void)height; n = 0; e = "this build carries no reference library for the keyed base"; return nullptr;
#else
        static void *inner = nullptr; if (inner) { ignh_inner_destroy(inner); inner = nullptr; }
        int rc = 0; inner = ignh_inner_create(params.c_str(), height, 1, &rc); if (!inner) { e = "the reference refused the full context (status " + std::to_string(rc) + ")"; return nullptr; }
        const uint8_t *d = ignh_inner_dataset(inner, &n); if (!d) { e = "the reference gave no dataset"; return nullptr; } return d;
#endif
    };
    card.fill = [&gl, st, side](const uint8_t *d, uint64_t n) {
        if (n != (uint64_t)side * side * 4) return false; st->base = d;
        const uint32_t blk = 256; std::vector<uint8_t> stage((size_t)blk * side * 4); glBindTexture(GL_TEXTURE_2D, gl.tex);
        for (uint32_t j0 = 0; j0 < side; j0 += blk) { for (uint32_t j = 0; j < blk; j++) std::memcpy(&stage[(size_t)j * side * 4], &d[(size_t)(j0 + blk - 1 - j) * side * 4], (size_t)side * 4);
            glTexSubImage2D(GL_TEXTURE_2D, 0, 0, (GLint)(side - j0 - blk), (GLsizei)side, (GLsizei)blk, GL_RGBA, GL_UNSIGNED_BYTE, stage.data()); }
        glFinish(); { const GLenum e = glGetError(); if (e != GL_NO_ERROR) fprintf(stderr, "raster: the fill left GL error 0x%x\n", (unsigned)e); return e == GL_NO_ERROR; }
    };
    // the batch: the records up, one instanced draw into the atlas, the fold, the accumulators back
    card.batch = [&gl, st, t, cs](const std::vector<std::vector<R19Box>> &attempts, std::vector<uint8_t> &accs) -> bool {
        const uint32_t nbox = (uint32_t)(attempts.size() * t); if (nbox > st->cells) { fprintf(stderr, "raster: the batch of %zu attempts exceeds the atlas (%u cells)\n", attempts.size(), st->cells); return false; }
        st->recs.assign((size_t)nbox * 16, 0u);
        for (size_t a = 0; a < attempts.size(); a++) for (uint32_t i = 0; i < t; i++) { const R19Box &b = attempts[a][i]; uint32_t *r = &st->recs[((size_t)a * t + i) * 16];
            r[0] = (uint32_t)b.x0; r[1] = (uint32_t)b.y0; r[2] = (uint32_t)b.x1; r[3] = (uint32_t)b.y1; r[4] = (uint32_t)b.x2; r[5] = (uint32_t)b.y2; r[6] = (uint32_t)b.minx; r[7] = (uint32_t)b.miny; r[8] = b.w; r[9] = b.h; r[10] = b.rgba; r[11] = b.a0; r[12] = b.b0; }
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_boxes); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(st->recs.size() * 4), st->recs.data(), GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_acc); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)nbox * 8, nullptr, GL_DYNAMIC_DRAW_); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
        // pass D: the draw into the atlas (no blend: every cell pixel is written once by its own box's quad)
        gl.BindFramebuffer(GL_FRAMEBUFFER_, st->fbo); glViewport(0, 0, (GLsizei)st->aw, (GLsizei)st->ah); glDisable(GL_BLEND);
        gl.UseProgram(st->prog); gl.BindVertexArray(st->vao);
        st->ActiveTexture(0x84C0 /* GL_TEXTURE0 */); glBindTexture(GL_TEXTURE_2D, gl.tex);
        st->DrawArraysInstanced(GL_TRIANGLE_STRIP, 0, 4, (GLsizei)nbox);
        gl.MemoryBarrier(GL_FRAMEBUFFER_BARRIER_BIT_ | GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_ | GL_TEXTURE_FETCH_BARRIER_BIT_);
        // pass F: the fold
        gl.UseProgram(st->fprog); gl.Uniform1ui(st->f_n, nbox); gl.BindImageTexture(0, st->atlas, 0, 0, 0, GL_READ_ONLY_, GL_R32UI_);
        gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 1, st->ssbo_boxes); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 2, st->ssbo_acc);
        gl.DispatchCompute((nbox + 63u) / 64u, 1, 1);
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | GL_BUFFER_UPDATE_BARRIER_BIT_);
        accs.resize((size_t)nbox * 8); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_acc);
        { const void *m = gl.MapBufferRange(GL_SHADER_STORAGE_BUFFER_, 0, (GLsizeiptr_)nbox * 8, GL_MAP_READ_BIT_); if (!m) { fprintf(stderr, "raster: the accumulators could not be mapped\n"); return false; } std::memcpy(accs.data(), m, (size_t)nbox * 8); gl.UnmapBuffer(GL_SHADER_STORAGE_BUFFER_); }
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0); gl.BindFramebuffer(GL_FRAMEBUFFER_, gl.fbo); glViewport(0, 0, (GLsizei)gl.side, (GLsizei)gl.side); gl.UseProgram(gl.prog); gl.BindVertexArray(gl.vao); glEnable(GL_BLEND);
        { const GLenum e = glGetError(); if (e != GL_NO_ERROR) { fprintf(stderr, "raster: the version 19 batch left GL error 0x%x\n", (unsigned)e); return false; } }
        return true;
    };
    st_out = st; return true;
}
}  // namespace igm
