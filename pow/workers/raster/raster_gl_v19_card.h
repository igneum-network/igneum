// raster_gl_v19_card.h: VERSION 19's passes G and H on the card (the rate cut after the first exact form in raster_gl_v19.h). Pass G: one
// compute thread an attempt makes the work seed S = BLAKE3(LP(seed tag) || C || V || P || header || nonce), the init digest st =
// BLAKE3(LP(init tag) || K || S) and the eight-lane chain's t boxes (the triangle with the document's reach and side, the colour stepping
// the lane), writing the box records the draw and the fold read and S for the final. Pass H: one thread an attempt digests M =
// BLAKE3(LP(mix tag) || K || the t accumulators) over its chunks (the chunk chaining values merged as the host merges them), then the
// final F = BLAKE3(LP(final tag) || C || V || P || S || M), compares F against the target byte by byte and writes M, F and the hit. Nothing
// crosses the bus an attempt: the batch's nonces in, the hits (or M and F for the sets) out. GL 4.3 core. the GPU team, 18 October 2026.
#pragma once
#include "raster_gl_v19.h"
namespace igm {
inline std::string r19_glsl_bytes(const char *name, const uint8_t *b, size_t n) { std::string s = "const uint " + std::string(name) + "[" + std::to_string(n) + "] = uint[" + std::to_string(n) + "]("; for (size_t i = 0; i < n; i++) s += (i ? ", " : "") + std::to_string((unsigned)b[i]) + "u"; return s + ");\n"; }
inline std::string r19_glsl_tag(const char *name, const char *tag) { std::vector<uint8_t> v; r18_lp(v, tag); return r19_glsl_bytes(name, v.data(), v.size()); }
// the BLAKE3 text shared by G and H: the compression, a one-chunk hash over a byte array, the chunk-stack merge
static const char *kV19B3 =
    "const uint IV[8] = uint[8](0x6A09E667u, 0xBB67AE85u, 0x3C6EF372u, 0xA54FF53Au, 0x510E527Fu, 0x9B05688Cu, 0x1F83D9ABu, 0x5BE0CD19u);\n"
    "const uint PERM[16] = uint[16](2u, 6u, 3u, 10u, 7u, 0u, 4u, 13u, 1u, 11u, 12u, 5u, 9u, 14u, 15u, 8u);\n"
    "uint rotr(uint x, uint n) { return (x >> n) | (x << (32u - n)); }\n"
    "uint fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }\n"
    "uint vv[16];\n"
    "void g(uint a, uint b, uint c, uint d, uint x, uint y) { vv[a] = vv[a] + vv[b] + x; vv[d] = rotr(vv[d] ^ vv[a], 16u); vv[c] = vv[c] + vv[d]; vv[b] = rotr(vv[b] ^ vv[c], 12u); vv[a] = vv[a] + vv[b] + y; vv[d] = rotr(vv[d] ^ vv[a], 8u); vv[c] = vv[c] + vv[d]; vv[b] = rotr(vv[b] ^ vv[c], 7u); }\n"
    "void compress(inout uint cv[8], uint m[16], uint t, uint blen, uint flags) {\n"
    "  for (int i = 0; i < 8; i++) vv[i] = cv[i]; vv[8] = IV[0]; vv[9] = IV[1]; vv[10] = IV[2]; vv[11] = IV[3]; vv[12] = t; vv[13] = 0u; vv[14] = blen; vv[15] = flags;\n"
    "  for (int r = 0; r < 7; r++) { g(0u,4u,8u,12u,m[0],m[1]); g(1u,5u,9u,13u,m[2],m[3]); g(2u,6u,10u,14u,m[4],m[5]); g(3u,7u,11u,15u,m[6],m[7]); g(0u,5u,10u,15u,m[8],m[9]); g(1u,6u,11u,12u,m[10],m[11]); g(2u,7u,8u,13u,m[12],m[13]); g(3u,4u,9u,14u,m[14],m[15]);\n"
    "    if (r < 6) { uint p[16]; for (int i = 0; i < 16; i++) p[i] = m[PERM[i]]; for (int i = 0; i < 16; i++) m[i] = p[i]; } }\n"
    "  for (int i = 0; i < 8; i++) cv[i] = vv[i] ^ vv[i + 8];\n"
    "}\n"
    // one chunk (at most 1024 bytes) from a byte array sb[0..len): the root when root is set, else the chunk's chaining value with counter t
    "uint sb[1088];\n"
    "void hash_chunk(uint len, uint t, bool root, out uint cv[8]) {\n"
    "  for (int i = 0; i < 8; i++) cv[i] = IV[i]; uint nblk = (len + 63u) / 64u; if (nblk == 0u) nblk = 1u;\n"
    "  for (uint b = 0u; b < nblk; b++) { uint m[16]; uint bo = b * 64u; uint blen = min(64u, len - bo);\n"
    "    for (int i = 0; i < 16; i++) { uint wv = 0u; for (uint q = 0u; q < 4u; q++) { uint o = uint(i) * 4u + q; if (o < blen) wv |= sb[bo + o] << (8u * q); } m[i] = wv; }\n"
    "    uint flags = (b == 0u ? 1u : 0u) | (b + 1u == nblk ? 2u : 0u) | ((root && b + 1u == nblk) ? 8u : 0u); compress(cv, m, t, blen, flags); }\n"
    "}\n";
// pass G: the records and S an attempt
static const char *kV19Gen =
    "layout(local_size_x = 64) in;\n"   // the dialect's head is prepended (430 or 310 es with highp)
    "layout(std430, binding = 1) writeonly buffer Boxes { uvec4 rec[]; };\n"
    "layout(std430, binding = 4) readonly buffer Nonces { uvec2 nonce[]; };\n"   // lo, hi
    "layout(std430, binding = 5) writeonly buffer Seeds { uint seedw[]; };\n"   // 8 words an attempt
    "uniform uint n_att, t, side, reach, chain_lo, chain_hi, version; uniform uint P[8]; uniform uint K[8]; uniform uint header[8];\n"
    "void put32be(uint o, uint v) { sb[o] = v >> 24; sb[o + 1u] = (v >> 16) & 255u; sb[o + 2u] = (v >> 8) & 255u; sb[o + 3u] = v & 255u; }\n"
    "void put_words_le(uint o, uint w[8]) { for (int i = 0; i < 8; i++) { sb[o + 4u * uint(i)] = w[i] & 255u; sb[o + 4u * uint(i) + 1u] = (w[i] >> 8) & 255u; sb[o + 4u * uint(i) + 2u] = (w[i] >> 16) & 255u; sb[o + 4u * uint(i) + 3u] = (w[i] >> 24) & 255u; } }\n"
    "int clampv(int v) { return v < 0 ? 0 : v > int(side) ? int(side) : v; }\n"
    "void main() {\n"
    "  uint a = gl_GlobalInvocationID.x; if (a >= n_att) return;\n"
    "  uint o = 0u; for (int i = 0; i < 20; i++) sb[o++] = TAG_SEED[i]; put32be(o, chain_hi); put32be(o + 4u, chain_lo); o += 8u; put32be(o, version); o += 4u;\n"
    "  uint pw[8] = uint[8](P[0], P[1], P[2], P[3], P[4], P[5], P[6], P[7]); put_words_le(o, pw); o += 32u;\n"
    "  uint hw[8] = uint[8](header[0], header[1], header[2], header[3], header[4], header[5], header[6], header[7]); put_words_le(o, hw); o += 32u;\n"
    "  uvec2 nn = nonce[a]; put32be(o, nn.y); put32be(o + 4u, nn.x); o += 8u;\n"
    "  uint S[8]; hash_chunk(o, 0u, true, S);\n"
    "  for (int i = 0; i < 8; i++) seedw[8u * a + uint(i)] = S[i];\n"
    "  o = 0u; for (int i = 0; i < 20; i++) sb[o++] = TAG_INIT[i]; uint kw[8] = uint[8](K[0], K[1], K[2], K[3], K[4], K[5], K[6], K[7]); put_words_le(o, kw); o += 32u; put_words_le(o, S); o += 32u;\n"
    "  uint st[8]; hash_chunk(o, 0u, true, st);\n"
    "  uint bits = reach == 31u ? 6u : 5u, mask = (1u << bits) - 1u; int rr = int(reach);\n"
    "  for (uint i = 0u; i < t; i++) { uint l = i & 7u; uint w = st[l] ^ kw[(i >> 3) & 7u];\n"
    "    uint u = fmix32(w ^ 0x2545F491u), col = fmix32(w ^ 0x9E3779B9u);\n"
    "    int x0, y0; if (side == 32768u) { x0 = int(w & 32767u); y0 = int((w >> 15) & 32767u); } else { x0 = int(w & 16383u); y0 = int((w >> 14) & 16383u); }\n"
    "    int x1 = clampv(x0 + int(u & mask) - rr), y1 = clampv(y0 + int((u >> bits) & mask) - rr), x2 = clampv(x0 + int((u >> (2u * bits)) & mask) - rr), y2 = clampv(y0 + int((u >> (3u * bits)) & mask) - rr);\n"
    "    int minx = min(x0, min(x1, x2)), miny = min(y0, min(y1, y2)), maxx = max(x0, max(x1, x2)), maxy = max(y0, max(y1, y2));\n"
    "    uint b = a * t + i; rec[4u * b] = uvec4(uint(x0), uint(y0), uint(x1), uint(y1)); rec[4u * b + 1u] = uvec4(uint(x2), uint(y2), uint(minx), uint(miny));\n"
    "    rec[4u * b + 2u] = uvec4(uint(maxx - minx), uint(maxy - miny), col, w); rec[4u * b + 3u] = uvec4(fmix32(w ^ (i * 0x9E3779B1u)), 0u, 0u, 0u);\n"
    "    st[l] = fmix32(st[l] ^ col ^ (i * 0x9E3779B1u)); }\n"
    "}\n";
// pass H: M over the accumulators, the final, the target compare
static const char *kV19Hash =
    "layout(local_size_x = 64) in;\n"
    "layout(std430, binding = 2) readonly buffer Acc { uint acc[]; };\n"
    "layout(std430, binding = 4) readonly buffer Nonces { uvec2 nonce[]; };\n"
    "layout(std430, binding = 5) readonly buffer Seeds { uint seedw[]; };\n"
    "layout(std430, binding = 6) writeonly buffer Outp { uint outw[]; };\n"   // 17 words an attempt: M (8), F (8), hit
    "layout(std430, binding = 7) buffer Hits { uint hits; uint hitn[]; };\n"   // the count, then the attempt indices
    "uniform uint n_att, t, chain_lo, chain_hi, version; uniform uint P[8]; uniform uint K[8]; uniform uint target[8];\n"
    "uint sbyte(uint a, uint o) { if (o < 19u) return TAG_MIX[o]; if (o < 51u) { uint k = o - 19u; return (K[k >> 2] >> (8u * (k & 3u))) & 255u; } uint r = o - 51u; uint w = acc[2u * a * t + (r >> 2)]; return (w >> (8u * (r & 3u))) & 255u; }\n"
    "void put32be(uint o, uint v) { sb[o] = v >> 24; sb[o + 1u] = (v >> 16) & 255u; sb[o + 2u] = (v >> 8) & 255u; sb[o + 3u] = v & 255u; }\n"
    "void put_words_le(uint o, uint w[8]) { for (int i = 0; i < 8; i++) { sb[o + 4u * uint(i)] = w[i] & 255u; sb[o + 4u * uint(i) + 1u] = (w[i] >> 8) & 255u; sb[o + 4u * uint(i) + 2u] = (w[i] >> 16) & 255u; sb[o + 4u * uint(i) + 3u] = (w[i] >> 24) & 255u; } }\n"
    "void main() {\n"
    "  uint a = gl_GlobalInvocationID.x; if (a >= n_att) return;\n"
    "  uint total = 51u + 8u * t; uint nch = (total + 1023u) / 1024u;\n"
    "  uint stackv[8][8]; uint sl = 0u; uint cv[8];\n"
    "  for (uint c = 0u; c < nch; c++) { uint start = c * 1024u; uint len = min(1024u, total - start);\n"
    "    for (uint i = 0u; i < len; i++) sb[i] = sbyte(a, start + i);\n"
    "    hash_chunk(len, c, nch == 1u, cv);\n"
    "    if (c + 1u < nch) {\n"   // the host's push: merge while the count of completed chunks has trailing zero bits
    "      uint n = c + 1u; while ((n & 1u) == 0u) { uint pm[16]; for (int i = 0; i < 8; i++) { pm[i] = stackv[sl - 1u][i]; pm[8 + i] = cv[i]; } sl--; uint iv[8] = uint[8](IV[0], IV[1], IV[2], IV[3], IV[4], IV[5], IV[6], IV[7]); compress(iv, pm, 0u, 64u, 4u); for (int i = 0; i < 8; i++) cv[i] = iv[i]; n >>= 1u; }\n"
    "      for (int i = 0; i < 8; i++) stackv[sl][i] = cv[i]; sl++; } }\n"
    "  while (sl > 0u) { uint pm[16]; for (int i = 0; i < 8; i++) { pm[i] = stackv[sl - 1u][i]; pm[8 + i] = cv[i]; } sl--; uint iv[8] = uint[8](IV[0], IV[1], IV[2], IV[3], IV[4], IV[5], IV[6], IV[7]); compress(iv, pm, 0u, 64u, 4u | (sl == 0u ? 8u : 0u)); for (int i = 0; i < 8; i++) cv[i] = iv[i]; }\n"
    "  uint M[8]; for (int i = 0; i < 8; i++) M[i] = cv[i];\n"
    "  uint o = 0u; for (int i = 0; i < 21; i++) sb[o++] = TAG_FINAL[i]; put32be(o, chain_hi); put32be(o + 4u, chain_lo); o += 8u; put32be(o, version); o += 4u;\n"
    "  uint pw[8] = uint[8](P[0], P[1], P[2], P[3], P[4], P[5], P[6], P[7]); put_words_le(o, pw); o += 32u;\n"
    "  uint S[8]; for (int i = 0; i < 8; i++) S[i] = seedw[8u * a + uint(i)]; put_words_le(o, S); o += 32u; put_words_le(o, M); o += 32u;\n"
    "  uint F[8]; hash_chunk(o, 0u, true, F);\n"
    "  bool le = true; bool decided = false;\n"   // F <= target byte by byte (the words LE: byte k of word i)
    "  for (int i = 0; i < 8 && !decided; i++) for (uint k = 0u; k < 4u && !decided; k++) { uint fb = (F[i] >> (8u * k)) & 255u, tb = (target[i] >> (8u * k)) & 255u; if (fb < tb) { decided = true; le = true; } else if (fb > tb) { decided = true; le = false; } }\n"
    "  for (int i = 0; i < 8; i++) { outw[17u * a + uint(i)] = M[i]; outw[17u * a + 8u + uint(i)] = F[i]; } outw[17u * a + 16u] = le ? 1u : 0u;\n"
    "  if (le) { uint slot = atomicAdd(hits, 1u); if (slot < 256u) hitn[slot] = a; }\n"
    "}\n";
}  // namespace igm
namespace igm {
// PASS S, THE SORT (RASTER-V19-DESIGN.md section 11, pass 1): the batch's box records ordered by the surface row of their top (miny) by a
// counting sort over the side's rows (a histogram, one scan in one workgroup, a scatter), so that consecutive quads of the draw read
// consecutive DRAM rows of the base (2.07 row visits a 2 KB page at a batch of 256 by the section 9 count, 6.8 at 1,024, against one a
// row in draw order); the sorted record carries its original index in its fourth word (r3.y), the draw and the fold take the sorted
// buffer, the fold writes the accumulator by the original index, so the bytes are the batch's whatever the order (R3).
static const char *kV19SortHist =
    "layout(local_size_x = 64) in;\n"
    "layout(std430, binding = 1) readonly buffer Boxes { uvec4 rec[]; };\n"
    "layout(std430, binding = 8) buffer Hist { uint hist[]; };\n"   // side bins; after the scan: the bin's start; the scatter's cursor
    "uniform uint nbox;\n"
    "void main() { uint b = gl_GlobalInvocationID.x; if (b >= nbox) return; atomicAdd(hist[rec[4u * b + 1u].w], 1u); }\n";
static const char *kV19SortScan =
    "layout(local_size_x = 1024) in;\n"
    "layout(std430, binding = 8) buffer Hist { uint hist[]; };\n"
    "uniform uint nbins;\n"   // the side (a multiple of 1024)
    "shared uint part[1024];\n"
    "void main() {\n"   // each thread scans its slice of nbins / 1024 bins, the slice sums scanned in shared memory by thread 0, the offsets added
    "  uint tid = gl_LocalInvocationID.x; uint per = nbins / 1024u; uint base = tid * per; uint sum = 0u;\n"
    "  for (uint i = 0u; i < per; i++) { uint v = hist[base + i]; hist[base + i] = sum; sum += v; }\n"
    "  part[tid] = sum; barrier(); memoryBarrierShared();\n"
    "  if (tid == 0u) { uint acc = 0u; for (uint i = 0u; i < 1024u; i++) { uint v = part[i]; part[i] = acc; acc += v; } }\n"
    "  barrier(); memoryBarrierShared(); uint off = part[tid];\n"
    "  for (uint i = 0u; i < per; i++) hist[base + i] += off;\n"
    "}\n";
static const char *kV19SortScatter =
    "layout(local_size_x = 64) in;\n"
    "layout(std430, binding = 1) readonly buffer Boxes { uvec4 rec[]; };\n"
    "layout(std430, binding = 8) buffer Hist { uint hist[]; };\n"
    "layout(std430, binding = 9) writeonly buffer Sorted { uvec4 srec[]; };\n"
    "uniform uint nbox;\n"
    "void main() { uint b = gl_GlobalInvocationID.x; if (b >= nbox) return; uvec4 r1 = rec[4u * b + 1u]; uint pos = atomicAdd(hist[r1.w], 1u);\n"
    "  srec[4u * pos] = rec[4u * b]; srec[4u * pos + 1u] = r1; srec[4u * pos + 2u] = rec[4u * b + 2u]; uvec4 r3 = rec[4u * b + 3u]; r3.y = b; srec[4u * pos + 3u] = r3; }\n";
// the full path bound onto the card: the G and H programs, the buffers, the one call
// THE TIMING PROBE (IGM_R19_TIME=1, a figure form, never a row): a GL_TIME_ELAPSED query around each pass of every batch, the four
// milliseconds printed on stderr once a second as a mean over the batches since the last line (the card's own clock, the host's waits apart)
typedef void (*PF_GenQueries)(GLsizei, GLuint *); typedef void (*PF_BeginQuery)(GLenum, GLuint); typedef void (*PF_EndQuery)(GLenum); typedef void (*PF_GetQueryObjectui64v)(GLuint, GLenum, uint64_t *);
struct R19Timing { PF_GenQueries GenQueries = nullptr; PF_BeginQuery BeginQuery = nullptr; PF_EndQuery EndQuery = nullptr; PF_GetQueryObjectui64v GetQueryObjectui64v = nullptr; GLuint q[4] = {0, 0, 0, 0}; double ms[4] = {0, 0, 0, 0}; uint64_t n = 0; std::chrono::steady_clock::time_point last; bool on = false; };
inline bool r19_gl_full(RasterGl &gl, const IhParams &p, R19GlState *st, R19Card &card, size_t batch_n) {
    const uint32_t t = p.rounds, side = r19_side(p), reach = r19_reach(p);
    auto tm = std::make_shared<R19Timing>();
    if (getenv("IGM_R19_TIME")) { tm->GenQueries = (PF_GenQueries)gl_proc("glGenQueries"); tm->BeginQuery = (PF_BeginQuery)gl_proc("glBeginQuery"); tm->EndQuery = (PF_EndQuery)gl_proc("glEndQuery"); tm->GetQueryObjectui64v = (PF_GetQueryObjectui64v)gl_proc("glGetQueryObjectui64v");
        if (tm->GenQueries && tm->BeginQuery && tm->EndQuery && tm->GetQueryObjectui64v) { tm->GenQueries(4, tm->q); tm->on = true; tm->last = std::chrono::steady_clock::now(); fprintf(stderr, "raster: the timing probe is on (GL_TIME_ELAPSED a pass)\n"); } else fprintf(stderr, "raster: the timing probe needs glGenQueries, glBeginQuery, glEndQuery and glGetQueryObjectui64v; off\n"); }
    st->Uniform1uiv = (void (*)(GLint, GLsizei, const GLuint *))gl_proc("glUniform1uiv"); if (!st->Uniform1uiv) { fprintf(stderr, "raster: glUniform1uiv missing; the full card path is off\n"); return false; }
    const std::string tags = r19_glsl_tag("TAG_SEED", "IgneumHash/R0A/seed") + r19_glsl_tag("TAG_INIT", "IgneumHash/R0I/init") + r19_glsl_tag("TAG_MIX", "IgneumHash/R0I/mix") + r19_glsl_tag("TAG_FINAL", "IgneumHash/R0A/final");
    rgl_stage("raster_worker", "compiling the version 19 generate and digest programs");
    const std::string head = gl.is_es() ? kV19HeadCompES : kV19HeadCompGL;
    { const std::string g = head + tags + kV19B3 + kV19Gen; st->gprog = gl.compute_program(g.c_str()); if (!st->gprog) return false; }
    { const std::string h = head + tags + kV19B3 + kV19Hash; st->hprog = gl.compute_program(h.c_str()); if (!st->hprog) return false; }
    gl.UseProgram(st->gprog); st->g_n = gl.GetUniformLocation(st->gprog, "n_att"); st->g_t = gl.GetUniformLocation(st->gprog, "t"); st->g_side = gl.GetUniformLocation(st->gprog, "side"); st->g_reach = gl.GetUniformLocation(st->gprog, "reach"); st->g_clo = gl.GetUniformLocation(st->gprog, "chain_lo"); st->g_chi = gl.GetUniformLocation(st->gprog, "chain_hi"); st->g_ver = gl.GetUniformLocation(st->gprog, "version"); st->g_P = gl.GetUniformLocation(st->gprog, "P"); st->g_K = gl.GetUniformLocation(st->gprog, "K"); st->g_hdr = gl.GetUniformLocation(st->gprog, "header");
    gl.Uniform1ui(st->g_t, t); gl.Uniform1ui(st->g_side, side); gl.Uniform1ui(st->g_reach, reach); gl.Uniform1ui(st->g_clo, (uint32_t)p.chain_id); gl.Uniform1ui(st->g_chi, (uint32_t)(p.chain_id >> 32)); gl.Uniform1ui(st->g_ver, p.version);
    gl.UseProgram(st->hprog); st->h_n = gl.GetUniformLocation(st->hprog, "n_att"); st->h_t = gl.GetUniformLocation(st->hprog, "t"); st->h_clo = gl.GetUniformLocation(st->hprog, "chain_lo"); st->h_chi = gl.GetUniformLocation(st->hprog, "chain_hi"); st->h_ver = gl.GetUniformLocation(st->hprog, "version"); st->h_P = gl.GetUniformLocation(st->hprog, "P"); st->h_K = gl.GetUniformLocation(st->hprog, "K"); st->h_tg = gl.GetUniformLocation(st->hprog, "target");
    gl.Uniform1ui(st->h_t, t); gl.Uniform1ui(st->h_clo, (uint32_t)p.chain_id); gl.Uniform1ui(st->h_chi, (uint32_t)(p.chain_id >> 32)); gl.Uniform1ui(st->h_ver, p.version);
    gl.GenBuffers(1, &st->ssbo_nonce); gl.GenBuffers(1, &st->ssbo_seed); gl.GenBuffers(1, &st->ssbo_out); gl.GenBuffers(1, &st->ssbo_hits);
    // pass S: the three programs, the histogram over the side's rows, the sorted records and their vertex array
    { st->sprog_hist = gl.compute_program((head + kV19SortHist).c_str()); st->sprog_scan = gl.compute_program((head + kV19SortScan).c_str()); st->sprog_scatter = gl.compute_program((head + kV19SortScatter).c_str());
      if (!st->sprog_hist || !st->sprog_scan || !st->sprog_scatter) return false;
      gl.UseProgram(st->sprog_hist); st->sh_n = gl.GetUniformLocation(st->sprog_hist, "nbox"); gl.UseProgram(st->sprog_scan); gl.Uniform1ui(gl.GetUniformLocation(st->sprog_scan, "nbins"), side); gl.UseProgram(st->sprog_scatter); st->ss_n = gl.GetUniformLocation(st->sprog_scatter, "nbox");
      gl.GenBuffers(1, &st->ssbo_sorted); gl.GenBuffers(1, &st->ssbo_hist);
      gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_sorted); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)st->cells * 64, nullptr, GL_DYNAMIC_DRAW_);
      gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_hist); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)side * 4, nullptr, GL_DYNAMIC_DRAW_); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
      gl.GenVertexArrays(1, &st->vao_sorted); gl.BindVertexArray(st->vao_sorted); gl.BindBuffer(GL_ARRAY_BUFFER, st->ssbo_sorted);
      for (GLuint a = 0; a < 3; a++) { gl.EnableVertexAttribArray(a); st->VertexAttribIPointer(a, 4, GL_UNSIGNED_INT, 64, (const void *)(uintptr_t)(16 * a)); st->VertexAttribDivisor(a, 1); }
      gl.BindBuffer(GL_ARRAY_BUFFER, 0); gl.BindVertexArray(gl.vao); st->sorted = true; rgl_stage("raster_worker", "the sort pass is built", "the records by surface row"); }
    gl.UseProgram(gl.prog);
    auto words = [](const uint8_t *b, uint32_t w[8]) { for (int i = 0; i < 8; i++) w[i] = (uint32_t)b[4 * i] | ((uint32_t)b[4 * i + 1] << 8) | ((uint32_t)b[4 * i + 2] << 16) | ((uint32_t)b[4 * i + 3] << 24); };
    card.full = [&gl, st, t, words, batch_n, tm](const uint8_t *K, const uint8_t *P, const uint8_t *header, const std::vector<uint64_t> &nonces, const uint8_t *target, std::vector<std::array<uint8_t, 32>> &Ms, std::vector<std::array<uint8_t, 32>> &Fs, uint32_t &hits) -> bool {
        const GLenum kTimeElapsed = 0x88BF; auto tb = [&](int i) { if (tm->on) tm->BeginQuery(kTimeElapsed, tm->q[i]); }; auto te = [&]() { if (tm->on) tm->EndQuery(kTimeElapsed); };
        const uint32_t n = (uint32_t)nonces.size(); if (n == 0 || n > batch_n) { fprintf(stderr, "raster: a batch of %u attempts against %zu cells\n", n, batch_n); return false; }
        const uint32_t nbox = n * t; uint32_t Kw[8], Pw[8], Hw[8], Tw[8]; words(K, Kw); words(P, Pw); words(header, Hw); words(target, Tw);
        std::vector<uint32_t> nn((size_t)n * 2); for (uint32_t i = 0; i < n; i++) { nn[2 * i] = (uint32_t)nonces[i]; nn[2 * i + 1] = (uint32_t)(nonces[i] >> 32); }
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_nonce); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)(nn.size() * 4), nn.data(), GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_boxes); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)nbox * 64, nullptr, GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_seed); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)n * 32, nullptr, GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_acc); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)nbox * 8, nullptr, GL_DYNAMIC_DRAW_);
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_out); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)n * 68, nullptr, GL_DYNAMIC_DRAW_);
        { uint32_t z[257] = {0}; gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_hits); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, sizeof z, z, GL_DYNAMIC_DRAW_); }
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0);
        // pass G
        tb(0); gl.UseProgram(st->gprog); gl.Uniform1ui(st->g_n, n); st->Uniform1uiv(st->g_P, 8, Pw); st->Uniform1uiv(st->g_K, 8, Kw); st->Uniform1uiv(st->g_hdr, 8, Hw);
        gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 1, st->ssbo_boxes); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 4, st->ssbo_nonce); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 5, st->ssbo_seed);
        gl.DispatchCompute((n + 63u) / 64u, 1, 1);
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | 0x00000001u /* GL_VERTEX_ATTRIB_ARRAY_BARRIER_BIT */ | GL_BUFFER_UPDATE_BARRIER_BIT_); te();
        // pass S: the histogram (the bins cleared), the scan, the scatter into the sorted records
        if (st->sorted) {
            { static std::vector<uint32_t> zeros; if (zeros.size() != gl.side) zeros.assign(gl.side, 0u); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_hist); gl.BufferData(GL_SHADER_STORAGE_BUFFER_, (GLsizeiptr_)gl.side * 4, zeros.data(), GL_DYNAMIC_DRAW_); gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0); }
            gl.UseProgram(st->sprog_hist); gl.Uniform1ui(st->sh_n, nbox); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 1, st->ssbo_boxes); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 8, st->ssbo_hist);
            gl.DispatchCompute((nbox + 63u) / 64u, 1, 1); gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_);
            gl.UseProgram(st->sprog_scan); gl.DispatchCompute(1, 1, 1); gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_);
            gl.UseProgram(st->sprog_scatter); gl.Uniform1ui(st->ss_n, nbox); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 9, st->ssbo_sorted);
            gl.DispatchCompute((nbox + 63u) / 64u, 1, 1); gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | 0x00000001u | GL_BUFFER_UPDATE_BARRIER_BIT_);
        }
        // pass D
        tb(1); gl.BindFramebuffer(GL_FRAMEBUFFER_, st->fbo); glViewport(0, 0, (GLsizei)st->aw, (GLsizei)st->ah); glDisable(GL_BLEND);
        gl.UseProgram(st->prog); gl.BindVertexArray(st->sorted ? st->vao_sorted : st->vao);
        st->ActiveTexture(0x84C0); glBindTexture(GL_TEXTURE_2D, gl.tex);
        st->DrawArraysInstanced(GL_TRIANGLE_STRIP, 0, 4, (GLsizei)nbox);
        gl.MemoryBarrier(GL_FRAMEBUFFER_BARRIER_BIT_ | GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_ | GL_TEXTURE_FETCH_BARRIER_BIT_); te();
        // pass F
        tb(2); gl.UseProgram(st->fprog); gl.Uniform1ui(st->f_n, nbox); gl.Uniform1ui(gl.GetUniformLocation(st->fprog, "sorted"), st->sorted ? 1u : 0u); gl.BindImageTexture(0, st->atlas, 0, 0, 0, GL_READ_ONLY_, GL_R32UI_);
        gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 1, st->sorted ? st->ssbo_sorted : st->ssbo_boxes); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 2, st->ssbo_acc);
        gl.DispatchCompute((nbox + 63u) / 64u, 1, 1);
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_); te();
        // pass H
        tb(3); gl.UseProgram(st->hprog); gl.Uniform1ui(st->h_n, n); st->Uniform1uiv(st->h_P, 8, Pw); st->Uniform1uiv(st->h_K, 8, Kw); st->Uniform1uiv(st->h_tg, 8, Tw);
        gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 2, st->ssbo_acc); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 4, st->ssbo_nonce); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 5, st->ssbo_seed); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 6, st->ssbo_out); gl.BindBufferBase(GL_SHADER_STORAGE_BUFFER_, 7, st->ssbo_hits);
        gl.DispatchCompute((n + 63u) / 64u, 1, 1);
        gl.MemoryBarrier(GL_SHADER_STORAGE_BARRIER_BIT_ | GL_BUFFER_UPDATE_BARRIER_BIT_); te();
        if (tm->on) { for (int i = 0; i < 4; i++) { uint64_t ns = 0; tm->GetQueryObjectui64v(tm->q[i], 0x8866 /* GL_QUERY_RESULT */, &ns); tm->ms[i] += ns / 1e6; } tm->n++;
            const auto now = std::chrono::steady_clock::now(); if (std::chrono::duration<double>(now - tm->last).count() >= 1.0) { fprintf(stderr, "raster: timing a batch of %u (mean of %llu): G %.3f ms, D %.3f ms, F %.3f ms, H %.3f ms\n", n, (unsigned long long)tm->n, tm->ms[0] / tm->n, tm->ms[1] / tm->n, tm->ms[2] / tm->n, tm->ms[3] / tm->n); for (double &m : tm->ms) m = 0; tm->n = 0; tm->last = now; } }
        // the readback: M, F and the hit an attempt (the bench reads the hit count alone through the same map)
        Ms.resize(n); Fs.resize(n); hits = 0;
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, st->ssbo_out);
        { const uint32_t *m = (const uint32_t *)gl.MapBufferRange(GL_SHADER_STORAGE_BUFFER_, 0, (GLsizeiptr_)n * 68, GL_MAP_READ_BIT_); if (!m) { fprintf(stderr, "raster: the outputs could not be mapped\n"); return false; }
          for (uint32_t a = 0; a < n; a++) { for (int i = 0; i < 8; i++) { const uint32_t mw = m[17 * a + i], fw = m[17 * a + 8 + i]; for (int k = 0; k < 4; k++) { Ms[a][4 * i + k] = (uint8_t)(mw >> (8 * k)); Fs[a][4 * i + k] = (uint8_t)(fw >> (8 * k)); } } hits += m[17 * a + 16]; }
          gl.UnmapBuffer(GL_SHADER_STORAGE_BUFFER_); }
        gl.BindBuffer(GL_SHADER_STORAGE_BUFFER_, 0); gl.BindFramebuffer(GL_FRAMEBUFFER_, gl.fbo); glViewport(0, 0, (GLsizei)gl.side, (GLsizei)gl.side); gl.UseProgram(gl.prog); gl.BindVertexArray(gl.vao); glEnable(GL_BLEND);
        { const GLenum e = glGetError(); if (e != GL_NO_ERROR) { fprintf(stderr, "raster: the version 19 full batch left GL error 0x%x\n", (unsigned)e); return false; } }
        return true;
    };
    return true;
}
}  // namespace igm
