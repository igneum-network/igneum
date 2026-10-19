// cl_probe.cpp: the OpenCL port of the ladder lead's primitive probe (pow/harness/prim_probe.cu; docs/ladder/PRIM-PROBE.md,
// the shapes fixed 10 Oct 2026 22:3x UK) for the RDNA4 rows of the escape search. One primitive per run, --seconds (60),
// ADLX board power once a second with the thermal state, the weighted checksum of every output word, deterministic inputs
// from the same counter fills (seed 20261010), no dataset. The per-second lines precede one final record {"prim_probe": {...}}
// with the CUDA record's keys (the NVML fields replaced by ADLX's, labelled), so the security review reads both with one script.
// Geometry: CUs x 8 workgroups of 256 in place of SMs x 8 blocks of 256; wave32 in place of warps of 32.
#include "cl_shim.h"
#include "cl_engine.h"
#include "kernel_source.h"
#include "adlx_probe.h"
#include <chrono>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>
#include "texture_probe.h"

namespace igm {
namespace {
double pr_now() { return std::chrono::duration<double>(std::chrono::steady_clock::now().time_since_epoch()).count(); }
struct Power { bool ok = false; double wsum = 0; int n = 0, kind = 0; double last_w = 0, tsum = 0, temp_max = 0, hot_max = 0; int core_mhz = 0, mem_mhz = 0, fan = 0; double clk_sum = 0; int clk_n = 0;
    void sample() { igm_adlx_sample_t s; if (!igm_adlx_sample(&s)) { ok = false; return; } ok = true; last_w = s.power_w; wsum += s.power_w; ++n; kind = s.power_kind; tsum += s.temp_c; if (s.temp_c > temp_max) temp_max = s.temp_c; if (s.hotspot_c > hot_max) hot_max = s.hotspot_c; core_mhz = s.core_mhz; mem_mhz = s.mem_mhz; fan = s.fan_rpm; if (s.core_mhz > 0) { clk_sum += s.core_mhz; ++clk_n; } }
    double mean() const { return n ? wsum / n : 0; } };
struct Ceiling { double value; const char *unit; const char *basis; };
Ceiling ceiling_for(const std::string &prim, const ClDeviceInfo &d, double ghz, double override_tops) {
    // RDNA4: 64 lanes per CU per clock for 32-bit integer ops (two SIMD32, dual issue not counted), the same datapath for v_perm;
    // one 32-lane cross-lane op (ds_bpermute or ds_swizzle) per CU per clock taken as the shuffle ceiling (labelled: the LDS crossbar's rate)
    const double lanes = (double)d.compute_units * 64.0 * ghz * 1e9;
    if (prim == "gemm-int8" || prim == "gemm-fp16") {
        double tops = override_tops;
        if (tops <= 0) { if (d.arch == "gfx1201") tops = prim == "gemm-int8" ? 779.0 : 389.0; else if (d.arch == "gfx1200") tops = prim == "gemm-int8" ? 410.0 : 205.0; else tops = 0; }   // 9070 XT: the project's 779 TOPS; 9060 XT: 410 = 821/1557 x 779 (the security review: AMD's sheet gives 821 against the 9070 XT's 1557 peak AI TOPS, the basis INT4 sparse; the basis of the 779 itself is open on both cards; the measured gemm row replaces both)
        return {tops * 1e12, "ops/s (2 per MAC)", prim == "gemm-fp16" ? "CAPABILITY ROW ONLY (floating accumulation, not a consensus primitive); the published dense FP16 rate; reported, not measured" : "the wave matrix INT8 with 32-bit accumulation (the consensus form); the published dense INT8 rate (9070 XT: the security review's default 779 TOPS; 9060 XT: scaled by CUs and sheet clock, not the sheet); reported"};
    }
    if (prim == "intround") return {lanes, "int32 ops/s", "the SIMD 32-bit integer issue rate: 64 lanes per CU per clock (RDNA4, dual issue not counted) x CUs x the run's mean core clock (ADLX)"};
    if (prim == "byteperm") return {lanes, "v_perm ops/s", "the v_perm_b32 rate taken as the 32-bit lane rate (the same datapath); the clock measured"};
    if (prim == "shuffle") return {(double)d.compute_units * 32.0 * ghz * 1e9, "lane-shuffles/s", "one wave-wide cross-lane op (32 lanes, ds_swizzle or ds_bpermute) per CU per clock, the clock measured (the LDS crossbar's rate, labelled)"};
    return {0, "items/s", "the plain walk's own items per second on this run (the memory-side ceiling for the side-work variants)"};
}
struct Dev {
    const ClDeviceInfo &info; cl_context ctx = nullptr; cl_command_queue q = nullptr; cl_program prog = nullptr; bool wmma = false; std::string wmma_why;
    Dev(const ClDeviceInfo &i) : info(i) {}
    bool open(const std::string &kdir, std::string &why, bool want_wmma) {
        cl_int err = 0; ctx = clCreateContext(nullptr, 1, &info.device, nullptr, nullptr, &err); if (!ctx) { why = "context"; return false; }
        q = clCreateCommandQueueWithProperties(ctx, info.device, nullptr, &err); if (!q) { why = "queue"; return false; }
        std::string src = kernel_source(kdir, "igm_probe.cl"); if (src.empty()) { why = "igm_probe.cl missing"; return false; }
        const bool amd12 = info.arch.rfind("gfx12", 0) == 0;
        for (int attempt = 0; attempt < 2; attempt++) {
            const bool with = attempt == 0 && amd12 && want_wmma;
            std::string s = (with ? "#define IGM_PROBE_WMMA 1\n" : "") + src; const char *p = s.c_str(); size_t n = s.size();
            cl_program pr = clCreateProgramWithSource(ctx, 1, &p, &n, &err);
            if (clBuildProgram(pr, 1, &info.device, "", nullptr, nullptr) == CL_SUCCESS) { prog = pr; wmma = with; if (!with && amd12 && want_wmma) wmma_why = "the wave matrix builtin did not compile on this runtime"; return true; }
            size_t ln = 0; clGetProgramBuildInfo(pr, info.device, CL_PROGRAM_BUILD_LOG, 0, nullptr, &ln); std::string log(ln, '\0'); clGetProgramBuildInfo(pr, info.device, CL_PROGRAM_BUILD_LOG, ln, log.data(), nullptr); clReleaseProgram(pr);
            for (auto &ch : log) if (ch == '\n' || ch == '\r' || ch == '"' || ch == '\\') ch = ' ';   // one JSON-safe line
            if (with) { wmma_why = "the wave matrix builtin refused: " + log.substr(0, 300); continue; }
            why = "build: " + log.substr(0, 600); return false;
        }
        return false;
    }
    cl_mem buf(uint64_t bytes) { cl_int e = 0; cl_mem m = clCreateBuffer(ctx, CL_MEM_READ_WRITE, bytes, nullptr, &e); return e == CL_SUCCESS ? m : nullptr; }
    cl_kernel kern(const char *n) { cl_int e = 0; return clCreateKernel(prog, n, &e); }
    bool run(cl_kernel k, size_t gx, size_t gy, size_t lx) { size_t g[2] = {gx, gy}, l[2] = {lx, 1}; return clEnqueueNDRangeKernel(q, k, gy > 1 ? 2 : 1, nullptr, g, l, 0, nullptr, nullptr) == CL_SUCCESS; }
    uint64_t checksum(cl_mem words, uint64_t n, bool f32) {   // the CUDA weighted sum: every word x (i mod 1021 + 1), summed in 64 bits
        const size_t wg = 256; size_t groups = (n + wg - 1) / wg; cl_mem part = buf(groups * 8); if (!part) return 0;
        cl_kernel k = kern(f32 ? "pr_sum_f32" : "pr_sum_u32"); cl_ulong nn = n; clSetKernelArg(k, 0, sizeof words, &words); clSetKernelArg(k, 1, sizeof nn, &nn); clSetKernelArg(k, 2, sizeof part, &part); clSetKernelArg(k, 3, wg * 8, nullptr);
        size_t g = groups * wg; clEnqueueNDRangeKernel(q, k, 1, nullptr, &g, &wg, 0, nullptr, nullptr); clFinish(q);
        std::vector<cl_ulong> h(groups); clEnqueueReadBuffer(q, part, CL_TRUE, 0, groups * 8, h.data(), 0, nullptr, nullptr); uint64_t s = 0; for (auto v : h) s += v;
        clReleaseKernel(k); clReleaseMemObject(part); return s;
    }
};
void fill_u32(Dev &d, cl_mem m, uint64_t words, uint32_t seed) { cl_kernel k = d.kern("pr_fill_u32"); cl_ulong n = words; clSetKernelArg(k, 0, sizeof m, &m); clSetKernelArg(k, 1, sizeof n, &n); clSetKernelArg(k, 2, 4, &seed); size_t g = (words + 255) / 256 * 256, l = 256; clEnqueueNDRangeKernel(d.q, k, 1, nullptr, &g, &l, 0, nullptr, nullptr); clFinish(d.q); clReleaseKernel(k); }
void fill_i8(Dev &d, cl_mem m, uint64_t bytes, uint32_t seed) { cl_kernel k = d.kern("pr_fill_i8"); cl_ulong n = bytes; clSetKernelArg(k, 0, sizeof m, &m); clSetKernelArg(k, 1, sizeof n, &n); clSetKernelArg(k, 2, 4, &seed); size_t g = (bytes + 255) / 256 * 256, l = 256; clEnqueueNDRangeKernel(d.q, k, 1, nullptr, &g, &l, 0, nullptr, nullptr); clFinish(d.q); clReleaseKernel(k); }
}   // namespace

// probe <prim> [seconds] [key=value ...]: walk_bytes, write_pct, write_mode=random, storm_ways, storm_stride (bytes), seed, ceiling_tops, cunits, rounds
// The texture determinism probe (14 Oct 2026, the project's gate for the texture discipline; texture_probe.h holds the five shared lines):
// a 4096 x 4096 R32_UINT image2d sampled 1,000,000 times with CLK_FILTER_NEAREST at the texel centre through the texture unit,
// one work-item per sample; the card's digest beside the host's.
int cl_texture_probe(const ClDeviceInfo &info) {
    cl_bool img_ok = CL_FALSE; clGetDeviceInfo(info.device, CL_DEVICE_IMAGE_SUPPORT, sizeof img_ok, &img_ok, nullptr);
    if (!img_ok) { printf("{\"texture_probe\": {\"backend\": \"opencl\", \"device\": \"%s\", \"refused\": \"the runtime reports no image support (CL_DEVICE_IMAGE_SUPPORT false)\"}}\n", info.name.c_str()); return 2; }
    cl_int err = 0; cl_context ctx = clCreateContext(nullptr, 1, &info.device, nullptr, nullptr, &err); if (!ctx) { printf("{\"texture_probe\": {\"refused\": \"context %d\"}}\n", err); return 2; }
    cl_command_queue q = clCreateCommandQueueWithProperties(ctx, info.device, nullptr, &err); if (!q) { printf("{\"texture_probe\": {\"refused\": \"queue %d\"}}\n", err); return 2; }
    std::vector<uint32_t> tex = tp_texture();
    cl_image_format fmt; fmt.image_channel_order = CL_R; fmt.image_channel_data_type = CL_UNSIGNED_INT32;
    cl_image_desc desc; std::memset(&desc, 0, sizeof desc); desc.image_type = CL_MEM_OBJECT_IMAGE2D; desc.image_width = kTpWidth; desc.image_height = kTpHeight;
    cl_mem img = clCreateImage(ctx, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, &fmt, &desc, tex.data(), &err);
    if (!img) { printf("{\"texture_probe\": {\"backend\": \"opencl\", \"device\": \"%s\", \"refused\": \"the 64 MiB R32_UINT image2d was refused (%d)\"}}\n", info.name.c_str(), err); return 2; }
    const char *src =
        "__constant sampler_t tp_smp = CLK_NORMALIZED_COORDS_FALSE | CLK_ADDRESS_CLAMP_TO_EDGE | CLK_FILTER_NEAREST;\n"
        "uint tp_fmix(uint h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }\n"
        "__kernel void tp_probe(__read_only image2d_t img, __global uint *out, uint n) {\n"
        "    const uint i = get_global_id(0); if (i >= n) return;\n"
        "    const uint s = tp_fmix(i * 0x9E3779B1u + 1u);\n"
        "    const float2 c = (float2)((float)(s & 4095u) + 0.5f, (float)((s >> 12) & 4095u) + 0.5f);\n"
        "    out[i] = read_imageui(img, tp_smp, c).x;\n"
        "}\n";
    size_t n = std::strlen(src); cl_program pr = clCreateProgramWithSource(ctx, 1, &src, &n, &err);
    if (!pr || clBuildProgram(pr, 1, &info.device, "", nullptr, nullptr) != CL_SUCCESS) {
        char log[4096] = {0}; if (pr) clGetProgramBuildInfo(pr, info.device, CL_PROGRAM_BUILD_LOG, sizeof log - 1, log, nullptr);
        printf("{\"texture_probe\": {\"backend\": \"opencl\", \"device\": \"%s\", \"refused\": \"kernel build: %s\"}}\n", info.name.c_str(), std::string(log).substr(0, 300).c_str()); return 2;
    }
    cl_kernel k = clCreateKernel(pr, "tp_probe", &err);
    cl_mem out = clCreateBuffer(ctx, CL_MEM_WRITE_ONLY, (size_t)kTpSamples * 4, nullptr, &err);
    cl_uint ns = kTpSamples; clSetKernelArg(k, 0, sizeof img, &img); clSetKernelArg(k, 1, sizeof out, &out); clSetKernelArg(k, 2, sizeof ns, &ns);
    size_t g = ((size_t)kTpSamples + 255) / 256 * 256, l = 256;
    if (clEnqueueNDRangeKernel(q, k, 1, nullptr, &g, &l, 0, nullptr, nullptr) != CL_SUCCESS) { printf("{\"texture_probe\": {\"refused\": \"launch\"}}\n"); return 2; }
    std::vector<uint32_t> card(kTpSamples); clEnqueueReadBuffer(q, out, CL_TRUE, 0, (size_t)kTpSamples * 4, card.data(), 0, nullptr, nullptr); clFinish(q);
    const int rc = tp_report("opencl", info.name + " (" + info.platform_name + ")", card, "read_imageui, CLK_NORMALIZED_COORDS_FALSE | CLK_ADDRESS_CLAMP_TO_EDGE | CLK_FILTER_NEAREST, float2 texel centre, 256 work-items a group");
    clReleaseMemObject(out); clReleaseMemObject(img); clReleaseKernel(k); clReleaseProgram(pr); clReleaseCommandQueue(q); clReleaseContext(ctx);
    return rc;
}

int cl_probe_main(const ClDeviceInfo &info, const std::string &kernel_dir, const std::string &prim, double seconds, const std::string &opts) {
    // the options as key=value words after the seconds (the agent sets no per-step environment)
    auto opt = [&](const char *key) -> std::string { std::string k = std::string(key) + "="; size_t i = 0; while (i < opts.size()) { size_t j = opts.find(';', i); if (j == std::string::npos) j = opts.size(); std::string kv = opts.substr(i, j - i); if (kv.rfind(k, 0) == 0) return kv.substr(k.size()); i = j + 1; } return ""; };
    auto optu = [&](const char *key, uint64_t dflt) { std::string v = opt(key); return v.empty() ? dflt : strtoull(v.c_str(), nullptr, 10); };
    uint64_t walk_bytes = optu("walk_bytes", 2ull << 30); uint32_t seed = (uint32_t)optu("seed", 20261010u), write_pct = (uint32_t)optu("write_pct", 0);
    int cunits = (int)optu("cunits", 1), rounds = (int)optu("rounds", 64); int write_random = opt("write_mode") == "random";
    uint32_t storm_ways = (uint32_t)optu("storm_ways", 0); uint64_t storm_stride = optu("storm_stride", 0); double override_tops = opt("ceiling_tops").empty() ? 0.0 : atof(opt("ceiling_tops").c_str());
    if (storm_ways && storm_stride < 128) { fprintf(stderr, "probe: the storm stride is bytes, a multiple of 128\n"); return 2; }
    const bool duplex = prim.rfind("duplex", 0) == 0;
    if (duplex && walk_bytes == (2ull << 30)) walk_bytes = 4ull << 30;
    if (prim == "duplex-mem" && write_pct == 0) write_pct = 100;
    if (prim == "duplex-compute") cunits = cunits * 64;
    if (duplex && walk_bytes > info.max_alloc) { fprintf(stderr, "probe: the %llu MiB duplex buffer exceeds the device's largest allocation; 2 GiB taken (labelled)\n", (unsigned long long)(walk_bytes >> 20)); walk_bytes = 2ull << 30; }
    Dev d(info); std::string why; if (!d.open(kernel_dir, why, prim == "gemm-int8" || prim == "walk-gemm")) { printf("{\"prim_probe\": {\"prim\": \"%s\", \"refused\": \"%s\"}}\n", prim.c_str(), why.c_str()); return 2; }
    igm_adlx_open(info.board.empty() ? info.name.c_str() : info.board.c_str());
    Power pw;
    const int cus = (int)info.compute_units; const size_t threads = 256, blocks = (size_t)cus * 8; const int passes = 64; const uint32_t walk_ctx = 65536, walk_reads = 64;
    const int M = 4096, N = 4096, K = 4096;
    double ops_per_launch = 0; const char *opname = "op"; std::string notes = "wave32 for warps of 32; CUs x 8 workgroups for SMs x 8 blocks; ds_bpermute for __shfl_xor_sync; v_perm_b32 for PRMT (the selector's byte numbering mapped)";
    cl_mem A = nullptr, B = nullptr, C = nullptr, out = nullptr, wbuf = nullptr; size_t out_words = 0; bool out_f32 = false; cl_kernel k = nullptr;
    if (prim == "gemm-int8") {
        if (!d.wmma) { printf("{\"prim_probe\": {\"prim\": \"gemm-int8\", \"refused\": \"%s\"}}\n", d.wmma_why.c_str()); return 2; }
        A = d.buf((uint64_t)M * K); B = d.buf((uint64_t)N * K); C = d.buf((uint64_t)M * N * 4); if (!A || !B || !C) { printf("{\"prim_probe\": {\"prim\": \"gemm-int8\", \"refused\": \"the matrix buffers\"}}\n"); return 2; }
        fill_i8(d, A, (uint64_t)M * K, seed); fill_i8(d, B, (uint64_t)N * K, seed ^ 0x55u); ops_per_launch = 2.0 * M * N * K; opname = "MAC";
        k = d.kern("pr_gemm_int8"); clSetKernelArg(k, 0, sizeof A, &A); clSetKernelArg(k, 1, sizeof B, &B); clSetKernelArg(k, 2, sizeof C, &C); clSetKernelArg(k, 3, 4, &M); clSetKernelArg(k, 4, 4, &N); clSetKernelArg(k, 5, 4, &K);
        notes += "; the gfx12 wave matrix builtin 16x16x16 int8 (32-bit accumulation) for mma.sync, the fragment layouts of the ISA (lane l: row l mod 16, k from 8 x (l div 16))";
    } else if (prim == "gemm-fp16") { printf("{\"prim_probe\": {\"prim\": \"gemm-fp16\", \"refused\": \"the FP16 wave matrix layout is not attempted tonight (capability row only); the INT8 row is the consensus candidate\"}}\n"); return 2; }
    else if (prim == "intround" || prim == "shuffle" || prim == "byteperm") {
        out = d.buf(blocks * threads * 4); out_words = blocks * threads; k = d.kern(prim == "intround" ? "pr_intround" : prim == "shuffle" ? "pr_shuffle" : "pr_byteperm");
        clSetKernelArg(k, 0, sizeof out, &out); clSetKernelArg(k, 1, 4, &seed); clSetKernelArg(k, 2, 4, &passes); if (prim == "shuffle") clSetKernelArg(k, 3, threads * 4, nullptr);
        ops_per_launch = (double)blocks * threads * passes * 64 * (prim == "intround" ? 4 : 1); opname = prim == "intround" ? "round (4 chains x 64 per pass; 3 int ops each)" : prim == "shuffle" ? "32-byte permutation (5 stages x 8 shuffles)" : "32-byte permutation (8 v_perm)";
    } else if (duplex) {
        if (prim != "duplex-mem" && prim != "duplex-compute" && prim != "duplex-seq" && prim != "duplex-sim") { fprintf(stderr, "probe: unknown primitive %s\n", prim.c_str()); return 2; }
        if (prim != "duplex-compute") { wbuf = d.buf(walk_bytes); if (!wbuf) { printf("{\"prim_probe\": {\"prim\": \"%s\", \"refused\": \"the %llu MiB buffer\"}}\n", prim.c_str(), (unsigned long long)(walk_bytes >> 20)); return 2; } fill_u32(d, wbuf, walk_bytes / 4, seed); }
        out = d.buf((uint64_t)cus * 8 * 256 * 4); out_words = (size_t)cus * 8 * (prim == "duplex-sim" ? 256 : 128);
        if (prim == "duplex-mem") { ops_per_launch = (double)cus * 8 * 16 * walk_reads; opname = "item (128-byte dependent read, write-back per write_pct)"; }
        else if (prim == "duplex-compute") { ops_per_launch = (double)cus * 8 * 128 * cunits; opname = "compute unit (rounds x 4 chains of mul-rotate-xor + one 32-byte byte permutation)"; }
        else { ops_per_launch = (double)cus * 8 * 16 * walk_reads; opname = "item (with cunits compute units per item in the same work-item, or in the paired waves)"; }
        if (prim == "duplex-mem" || prim == "duplex-seq") write_pct = write_pct ? write_pct : 100;
        k = d.kern(prim == "duplex-mem" ? "pr_duplex_mem" : prim == "duplex-compute" ? "pr_duplex_compute" : prim == "duplex-seq" ? "pr_duplex_seq" : "pr_duplex_sim");
    } else if (prim == "walk" || prim == "walk-int" || prim == "walk-gemm") {
        if (prim == "walk-gemm") { printf("{\"prim_probe\": {\"prim\": \"walk-gemm\", \"refused\": \"the per-item wave matrix tile is not ported tonight (the walk-int side work stands; walk-gemm in the morning)\"}}\n"); return 2; }
        wbuf = d.buf(walk_bytes); if (!wbuf) { printf("{\"prim_probe\": {\"prim\": \"%s\", \"refused\": \"the %llu MiB buffer\"}}\n", prim.c_str(), (unsigned long long)(walk_bytes >> 20)); return 2; } fill_u32(d, wbuf, walk_bytes / 4, seed);
        out = d.buf((uint64_t)walk_ctx * 8 * 4); out_words = (size_t)walk_ctx * 8; ops_per_launch = (double)walk_ctx * walk_reads; opname = "item (one 128-byte dependent read per context)";
        k = d.kern((prim == "walk" && (write_pct || storm_ways)) ? "pr_duplex_mem" : prim == "walk" ? "pr_walk" : "pr_walk_int");
    } else { fprintf(stderr, "probe: unknown primitive %s\n", prim.c_str()); return 2; }
    const bool dup_form = duplex || (prim == "walk" && (write_pct || storm_ways));
    if (dup_form) {   // the duplex kernel's arguments
        cl_ulong items = walk_bytes / 128, stride_items = storm_stride / 128; int rd = (int)walk_reads; int cu = prim == "duplex-sim" ? cunits * (int)walk_reads : (prim == "duplex-mem" || prim == "walk") ? 0 : cunits; uint32_t wp = prim == "duplex-sim" ? (write_pct ? write_pct : 100) : write_pct; uint32_t sw = prim == "walk" ? storm_ways : 0; cl_ulong si = prim == "walk" ? stride_items : 0; int wr = prim == "walk" ? write_random : 0;
        cl_mem nb = wbuf ? wbuf : d.buf(4096);
        clSetKernelArg(k, 0, sizeof nb, &nb); clSetKernelArg(k, 1, sizeof items, &items); clSetKernelArg(k, 2, 4, &rd); clSetKernelArg(k, 3, 4, &cu); clSetKernelArg(k, 4, 4, &rounds); clSetKernelArg(k, 5, 4, &wp); clSetKernelArg(k, 6, sizeof out, &out); clSetKernelArg(k, 7, 4, &seed); clSetKernelArg(k, 8, 4, &wr); clSetKernelArg(k, 9, 4, &sw); clSetKernelArg(k, 10, sizeof si, &si); clSetKernelArg(k, 11, 256 * 4, nullptr);
    } else if (prim == "walk" || prim == "walk-int") { cl_ulong items = walk_bytes / 128; int rd = (int)walk_reads; clSetKernelArg(k, 0, sizeof wbuf, &wbuf); clSetKernelArg(k, 1, sizeof items, &items); clSetKernelArg(k, 2, 4, &rd); clSetKernelArg(k, 3, sizeof out, &out); clSetKernelArg(k, 4, 4, &seed); clSetKernelArg(k, 5, 256 * 4, nullptr); }
    auto launch = [&]() {
        if (prim == "gemm-int8") return d.run(k, (size_t)N / 64 * 128, (size_t)M / 64, 128);
        if (prim == "intround" || prim == "shuffle" || prim == "byteperm") return d.run(k, blocks * threads, 1, threads);
        if (prim == "duplex-sim") return d.run(k, (size_t)cus * 8 * 256, 1, 256);
        if (duplex) return d.run(k, (size_t)cus * 8 * 128, 1, 128);
        return d.run(k, (size_t)walk_ctx * 8, 1, 256);
    };
    for (int i = 0; i < 2; ++i) if (!launch()) { printf("{\"prim_probe\": {\"prim\": \"%s\", \"refused\": \"the launch failed\"}}\n", prim.c_str()); return 2; }
    clFinish(d.q);
    double t0 = pr_now(), next = t0 + 1.0; uint64_t launches = 0; double pw_before = 0;
    pw.sample(); pw_before = pw.last_w; pw.wsum = 0; pw.n = 0; pw.tsum = 0; pw.clk_sum = 0; pw.clk_n = 0;
    while (pr_now() - t0 < seconds) {
        launch(); clFinish(d.q); ++launches;
        if (pr_now() >= next) { pw.sample(); next += 1.0;
            printf("{\"t\": %.1f, \"launches\": %llu, \"power_w\": %.1f, \"power_limit_w\": 0, \"temp_c\": %.0f, \"sm_mhz\": %d, \"mem_mhz\": %d, \"throttle\": \"adlx: hotspot %.0f C, fan %d rpm\"}\n", pr_now() - t0, (unsigned long long)launches, pw.last_w, pw.temp_max, pw.core_mhz, pw.mem_mhz, pw.hot_max, pw.fan); fflush(stdout); }
    }
    const double el = pr_now() - t0;
    uint64_t sum = prim == "gemm-int8" ? d.checksum(C, (uint64_t)M * N, false) : d.checksum(out, out_words, out_f32);
    const double ops = ops_per_launch * launches, ops_s = ops / el, ghz = pw.clk_n ? pw.clk_sum / pw.clk_n / 1000.0 : 0;
    const Ceiling c = ceiling_for(prim, info, ghz, override_tops);
    const double ceil = (prim.rfind("walk", 0) == 0 || duplex) ? 0 : c.value;
    const double items_per_launch = (prim == "duplex-mem" || prim == "duplex-seq" || prim == "duplex-sim") ? (double)cus * 8 * 16 * walk_reads : 0;
    const double units_per_launch = prim == "duplex-compute" ? (double)cus * 8 * 128 * cunits : (prim == "duplex-seq" || prim == "duplex-sim") ? (double)cus * 8 * 128 * cunits * walk_reads : 0;
    const double rate_ops = (prim == "intround") ? ops_s * 3 : ops_s * ((prim == "shuffle") ? 40.0 : (prim == "byteperm" ? 8.0 : 1.0));
    const double mean_w = pw.mean(), j_per_op = mean_w > 0 && ops_s > 0 ? mean_w / ops_s : 0;
    printf("{\"prim_probe\": {\"prim\": \"%s\", \"device\": \"%s\", \"sm\": \"%s\", \"sms\": %d, \"seconds\": %.1f, \"launches\": %llu, \"ops\": %.6g, \"ops_per_s\": %.6g, \"op\": \"%s\", "
           "\"ceiling\": %.6g, \"ceiling_unit\": \"%s\", \"ceiling_basis\": \"%s\", \"rate_in_ceiling_units\": %.6g, \"fraction_of_ceiling\": %.4f, "
           "\"mean_power_w\": %.1f, \"power_before_w\": %.1f, \"mean_sm_mhz\": %.0f, \"j_per_op\": %.6g, \"nj_per_op\": %.4f, \"checksum\": \"%016llx\", \"seed\": %u, \"grade\": \"telemetry (ADLX %s power at 1 s)\", "
           "\"thermal\": {\"temp_mean_c\": %.1f, \"temp_max_c\": %.0f, \"mem_mhz_last\": %d, \"power_limit_w\": 0, \"throttle_union\": \"adlx: hotspot max %.0f C, fan max %d rpm (no throttle mask on ADLX)\"}, "
           "\"duplex\": {\"items_per_s\": %.6g, \"compute_units_per_s\": %.6g, \"write_pct\": %u, \"write_mode\": \"%s\", \"storm_ways\": %u, \"storm_stride_bytes\": %llu, \"cunits_per_item\": %d, \"rounds_per_unit\": %d, \"buffer_bytes\": %llu}, "
           "\"shape\": \"%s\", \"shape_source\": \"docs/ladder/PRIM-PROBE.md (the ladder lead's fixed shapes); OpenCL port by the GPU team\", \"departures\": \"%s\", \"wave_matrix\": %s}}\n",
           prim.c_str(), info.name.c_str(), info.arch.c_str(), cus, el, (unsigned long long)launches, ops, ops_s, opname, ceil, c.unit, c.basis,
           rate_ops, ceil > 0 ? rate_ops / ceil : 0.0, mean_w, pw_before, ghz * 1000.0, j_per_op, j_per_op * 1e9, (unsigned long long)sum, seed, pw.kind == 2 ? "board" : "gpu",
           pw.n ? pw.tsum / pw.n : 0.0, pw.temp_max, pw.mem_mhz, pw.hot_max, pw.fan,
           items_per_launch * launches / el, units_per_launch * launches / el, write_pct, write_random ? "random" : "same", storm_ways, (unsigned long long)storm_stride, cunits, rounds, (unsigned long long)(wbuf ? walk_bytes : 0),
           duplex ? "CUs x 8 workgroups; walk workgroups 128 work-items = 16 contexts x 64 items of 128 B over the buffer; compute 64 units per work-item per launch (rounds x 4 chains + one 8-word byte permutation); sim = 256-item workgroups, waves 0..3 walk, 4..7 compute" :
           prim.rfind("gemm", 0) == 0 ? "4096 x 4096 x 4096, 64 x 64 tile per workgroup of 4 wave32s, wave matrix 16x16x16" : prim.rfind("walk", 0) == 0 ? "65536 contexts x 64 dependent 128-byte reads over the buffer" : "CUs x 8 workgroups x 256 work-items, 64 passes x 64 per launch",
           notes.c_str(), d.wmma ? "true" : "false");
    fflush(stdout);
    return 0;
}
}   // namespace igm

// fpcheck <fp.json>: the version 8 fp class (SPEC 0.8 bit 4) evaluated on the device, every case bit for bit; the gate's jsonl
#include "json.h"
#include <fstream>
#include <sstream>
namespace igm {
int cl_fpcheck_main(const ClDeviceInfo &info, const std::string &kernel_dir, const std::string &path) {
    std::ifstream f(path, std::ios::binary); if (!f) { printf("{\"class\": \"fp\", \"refused\": \"cannot read %s\"}\n", path.c_str()); return 2; }
    std::stringstream ss; ss << f.rdbuf(); Json j; if (!Json::parse(ss.str(), j)) { printf("{\"class\": \"fp\", \"refused\": \"parse\"}\n"); return 2; }
    std::vector<uint32_t> cases; std::vector<uint32_t> want; std::vector<std::string> group;
    for (const char *k : {"edge", "random"}) { if (!j.has(k)) continue; const Json &arr = j[k]; for (size_t i = 0; i < arr.size(); i++) { const Json &c = arr.at(i); cases.push_back((uint32_t)c["x"].as_u64()); cases.push_back((uint32_t)c["y"].as_u64()); cases.push_back((uint32_t)c["z"].as_u64()); cases.push_back((uint32_t)(c.has("opcode") ? c["opcode"].as_u64() : 2)); want.push_back((uint32_t)c["out"].as_u64()); group.push_back(k); } }
    const uint32_t wide = (j.has("features") && (j["features"].as_u64() & 32)) || (j.has("params") && j["params"].has("features") && (j["params"]["features"].as_u64() & 32)) || (j.has("feature_bit") && j["feature_bit"].as_u64() == 5) ? 1u : 0u;   // bit 5: every opcode binary32
    const size_t n = want.size(); if (!n) { printf("{\"class\": \"fp\", \"refused\": \"no cases\"}\n"); return 2; }
    Dev d(info); std::string why; if (!d.open(kernel_dir, why, false)) { printf("{\"class\": \"fp\", \"refused\": \"%s\"}\n", why.c_str()); return 2; }
    cl_mem cb = d.buf(cases.size() * 4), ob = d.buf(n * 4); clEnqueueWriteBuffer(d.q, cb, CL_TRUE, 0, cases.size() * 4, cases.data(), 0, nullptr, nullptr);
    cl_kernel k = d.kern("igm_v8_fop_check"); uint32_t nn = (uint32_t)n; clSetKernelArg(k, 0, sizeof cb, &cb); clSetKernelArg(k, 1, sizeof ob, &ob); clSetKernelArg(k, 2, 4, &nn); clSetKernelArg(k, 3, 4, &wide);
    size_t g = (n + 255) / 256 * 256, l = 256; if (clEnqueueNDRangeKernel(d.q, k, 1, nullptr, &g, &l, 0, nullptr, nullptr) != CL_SUCCESS) { printf("{\"class\": \"fp\", \"refused\": \"launch\"}\n"); return 2; }
    clFinish(d.q); std::vector<uint32_t> got(n); clEnqueueReadBuffer(d.q, ob, CL_TRUE, 0, n * 4, got.data(), 0, nullptr, nullptr);
    size_t bad = 0, bad_edge = 0, bad_random = 0; std::string first;
    for (size_t i = 0; i < n; i++) if (got[i] != want[i]) { bad++; if (group[i] == "edge") bad_edge++; else bad_random++; if (first.empty()) { char b[200]; snprintf(b, sizeof b, "case %zu (%s): x %08x y %08x z %08x opcode %u want %08x got %08x", i, group[i].c_str(), cases[4 * i], cases[4 * i + 1], cases[4 * i + 2], cases[4 * i + 3], want[i], got[i]); first = b; } }
    printf("{\"class\": \"fp\", \"candidate\": \"igneumhash\", \"version\": 8, \"feature_bit\": %u, \"rule\": \"%s\", \"cases\": %zu, \"edge\": %zu, \"random\": %zu, \"mismatches\": %zu, \"mismatches_edge\": %zu, \"mismatches_random\": %zu, \"contract_differs_in_set\": %llu, \"device\": \"%s\", \"arch\": \"%s\", \"build\": \"FP_CONTRACT OFF in the kernel; no fast-math or denormal option on the build; the product in a volatile\", \"verdict\": \"%s\"%s%s%s}\n",
           wide ? 5u : 4u, j.has("rule") ? j["rule"].as_string().c_str() : "", n, n - (size_t)(j.has("random") ? j["random"].size() : 0), (size_t)(j.has("random") ? j["random"].size() : 0), bad, bad_edge, bad_random, (unsigned long long)(j.has("contract_differs") ? j["contract_differs"].as_u64() : 0), info.name.c_str(), info.arch.c_str(), bad ? "mismatch" : "exact", first.empty() ? "" : ", \"first_mismatch\": \"", first.c_str(), first.empty() ? "" : "\"");
    fflush(stdout); return bad ? 1 : 0;
}
}   // namespace igm
