// cl_engine_x.cpp: see cl_engine_x.h.
#include <cstddef>
#include "cl_engine_x.h"
#include "r0d_gen.h"
#include "sha256.h"
#include "kernel_source.h"
#include "texture_probe.h"
#include "igneumhash.h"   // ignh_test_sk: the test secret of the keyed vector classes (version 8 bit 10)
#include "hex.h"
#include <chrono>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <sstream>
#include <thread>
#include <utility>
#include <vector>

namespace igm {
namespace {

std::string read_file(const std::string &path) { std::ifstream f(path, std::ios::binary); std::stringstream ss; ss << f.rdbuf(); return ss.str(); }
double now_ms() { using namespace std::chrono; return duration<double, std::milli>(steady_clock::now().time_since_epoch()).count(); }
std::string mb(uint64_t b) { return std::to_string(b >> 20) + " MB"; }
inline uint32_t le32(const uint8_t *p) { return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24); }
inline uint32_t be32(const uint8_t *p) { return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | (uint32_t)p[3]; }
inline void put_le32(uint8_t *p, uint32_t v) { p[0] = v & 255; p[1] = (v >> 8) & 255; p[2] = (v >> 16) & 255; p[3] = (v >> 24) & 255; }
std::string build_log(cl_program prog, cl_device_id dev) { size_t n = 0; clGetProgramBuildInfo(prog, dev, CL_PROGRAM_BUILD_LOG, 0, nullptr, &n); std::string s(n, '\0'); clGetProgramBuildInfo(prog, dev, CL_PROGRAM_BUILD_LOG, n, s.data(), nullptr); return s.substr(0, 2000); }
#ifndef CL_DEVICE_GLOBAL_FREE_MEMORY_AMD
#define CL_DEVICE_GLOBAL_FREE_MEMORY_AMD 0x4039
#endif
struct FhJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t items1024; uint32_t hashes; uint32_t max_results; };
struct FhResult { uint32_t nonce_lo, nonce_hi; uint32_t mix[8]; uint32_t digest[8]; };
struct AlJob { uint8_t header[32]; uint8_t target[32]; uint32_t nonce_lo, nonce_hi; uint32_t N; uint32_t hashes; uint32_t max_results; };
struct AlResult { uint32_t nonce_lo, nonce_hi; uint8_t mix[32]; uint8_t digest[32]; };
struct IhConsts { uint32_t c_hi, c_lo, v; uint32_t p[8]; uint32_t e[8]; uint32_t n_items_mask; uint32_t slice_words; uint32_t slice_lines_mask; uint32_t rounds; uint32_t item_chain; uint32_t n_cache_mask; uint32_t parents; uint32_t version; uint32_t item_words; uint32_t sub_blocks; uint32_t blocks, registers, features; uint32_t n_items, fastrange; uint32_t program_ops, fold_every, batch_log2;  uint32_t sk[8]; };   // blocks, registers, features: version 8 (section 0.8); n_items and fastrange: the multiply-high address map of fd9b16be (the kernel's ih_consts mirrors this struct field for field; a host struct shorter than the kernel's leaves the kernel reading past its end, the epoch-random mismatch of 11 Oct 00:2x UK)
struct IhJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t hashes; uint32_t max_results; uint32_t scratch_slots; IhConsts k; };
struct IhResult { uint32_t nonce_lo, nonce_hi; uint32_t mix[8]; uint32_t digest[8]; };
struct IhDatasetBatch { uint32_t first, count, pad0, pad1; IhConsts k; };
struct RxConsts { uint32_t cid_lo, cid_hi; uint32_t pid[8]; uint32_t ep_lo, ep_hi; uint32_t e[8]; uint32_t n_items_mask; uint32_t rounds; uint32_t regs; uint32_t blocks; uint32_t group; uint32_t route; uint32_t chunks; uint32_t payload; uint32_t n_regions_mask; uint32_t wpl; uint32_t schema; uint32_t mode; };
struct RxJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t hashes; uint32_t max_results; uint32_t slots; RxConsts k; };
RxConsts rx_consts_of(const Rxe1Params &p, const uint8_t pid[32], uint64_t epoch, const uint8_t E[32]) {
    RxConsts k{}; k.cid_lo = (uint32_t)p.chain_id; k.cid_hi = (uint32_t)(p.chain_id >> 32);
    for (int i = 0; i < 8; i++) { k.pid[i] = le32(pid + 4 * i); k.e[i] = le32(E + 4 * i); }
    k.ep_lo = (uint32_t)epoch; k.ep_hi = (uint32_t)(epoch >> 32); k.n_items_mask = p.n_items() - 1; k.rounds = p.rounds; k.regs = p.registers; k.blocks = p.blocks;
    k.group = p.group; k.route = p.route; k.chunks = p.chunks; k.payload = p.payload; k.n_regions_mask = p.n_regions() - 1; k.wpl = p.wpl(); k.schema = p.schema_version(); k.mode = p.mode;
    return k;
}
IhConsts ih_consts_of(const IhParams &p, const uint8_t P[32], const uint8_t E[32]) {
    IhConsts k{}; k.c_hi = (uint32_t)(p.chain_id >> 32); k.c_lo = (uint32_t)p.chain_id; k.v = p.version;
    for (int i = 0; i < 8; i++) { k.p[i] = le32(P + 4 * i); k.e[i] = le32(E + 4 * i); }
    k.n_items_mask = p.n_items() - 1; k.slice_words = p.slice_words(); k.slice_lines_mask = p.slice_lines() ? p.slice_lines() - 1 : 0; k.rounds = p.rounds; k.item_chain = p.item_chain;
    k.n_cache_mask = p.n_cache() - 1; k.parents = p.parents; k.version = p.version; k.item_words = p.item_words(); k.sub_blocks = p.sub_blocks();
    k.blocks = p.blocks; k.registers = p.registers; k.features = p.features;
    k.n_items = (uint32_t)p.n_items(); k.fastrange = p.fastrange() ? 1u : 0u; k.program_ops = p.program_ops; k.fold_every = p.fold_every; k.batch_log2 = p.keyed_rounds;
    return k;
}

}  // namespace

ClEngineX::ClEngineX(const ClDeviceInfo &info, int index, const std::string &kernel_dir) : info_(info), index_(index), kernel_dir_(kernel_dir) {
    cl_int err = 0;
    ctxcl_ = clCreateContext(nullptr, 1, &info_.device, nullptr, nullptr, &err);
    cl_queue_properties props[] = { CL_QUEUE_PROPERTIES, CL_QUEUE_PROFILING_ENABLE, 0 };
    queue_ = clCreateCommandQueueWithProperties(ctxcl_, info_.device, props, &err);
    build_queue_ = clCreateCommandQueueWithProperties(ctxcl_, info_.device, props, &err);
}

bool ClEngineX::program_binary(std::vector<unsigned char> &out) const {
    size_t nb = 0; if (!prog_ || clGetProgramInfo(prog_, CL_PROGRAM_BINARY_SIZES, sizeof nb, &nb, nullptr) != CL_SUCCESS || !nb) return false;
    out.assign(nb, 0); unsigned char *bp = out.data();
    return clGetProgramInfo(prog_, CL_PROGRAM_BINARIES, sizeof bp, &bp, nullptr) == CL_SUCCESS;
}

bool ClEngineX::trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) {
    if (!cand_ || cand_->kind != Kind::igneumhash || rxe1_ || !search_k_ || last_src_.empty()) { r.code = "unsupported"; r.explain = "the trace path is IgneumHash's on an open engine"; return false; }
    if (knobs_.variant == "n1" || ih_ctx() > 1 || knobs_.variant == "bucket") { r.code = "unsupported"; r.explain = "the trace path runs the eight-lane kernel only"; return false; }
    const size_t need = 704 + (size_t)ih_.rounds * (130 + ih_.item_words());
    if (!trace_k_ || trace_words_ != need) {
        if (trace_k_) { clReleaseKernel(trace_k_); trace_k_ = nullptr; } if (trace_prog_) { clReleaseProgram(trace_prog_); trace_prog_ = nullptr; } if (trace_buf_) { clReleaseMemObject(trace_buf_); trace_buf_ = nullptr; }
        std::string src = "#define IGM_IH_TRACE 1\n#define IGM_IH_TRACE_FULL 1\n" + last_src_;
        const char *cs = src.c_str(); size_t len = src.size(); cl_int err = 0;
        trace_prog_ = clCreateProgramWithSource(ctxcl_, 1, &cs, &len, &err);
        if (!trace_prog_ || clBuildProgram(trace_prog_, 1, &info_.device, knobs_.clopts.c_str(), nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "trace kernel build: " + (trace_prog_ ? build_log(trace_prog_, info_.device) : std::string("no program")); return false; }
        trace_k_ = clCreateKernel(trace_prog_, bw_loader_ ? "igm_igneumhash_bw_search" : "igm_igneumhash_search", &err);
        if (!trace_k_) { r.code = "driver"; r.explain = "trace kernel create failed"; return false; }
        trace_buf_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, need * 4, nullptr, &err);
        if (!trace_buf_) { r.code = "memory"; r.explain = "trace buffer allocation failed"; return false; }
        trace_words_ = need;
    }
    std::vector<uint32_t> zero(need, 0u);
    clEnqueueWriteBuffer(queue_, trace_buf_, CL_TRUE, 0, need * 4, zero.data(), 0, nullptr, nullptr);
    Job j; j.job_id = "trace"; j.height = height; std::memcpy(j.header, header, 32); std::memset(j.target, 0xff, 32); j.nonce_start = nonce; j.nonce_count = 1;
    bool got = false; use_trace_ = true;
    ih_run_job(j, [&](const Result &res) { if (res.nonce == nonce) got = true; }, [](const BatchStats &) {}, [] { return false; });
    use_trace_ = false;
    if (!got) { r.code = "driver"; r.explain = "the traced nonce returned no result"; return false; }
    words.assign(need, 0u);
    clEnqueueReadBuffer(queue_, trace_buf_, CL_TRUE, 0, need * 4, words.data(), 0, nullptr, nullptr);
    return true;
}

bool ClEngineX::dataset_word(uint64_t word_index, uint32_t *value, bool write) {
    if (!dataset_ || cand_ == nullptr || cand_->kind != Kind::igneumhash || rxe1_) return false;
    if (knobs_.variant == "swizzle") return false;   // the slot map would have to be applied; refused by name
    const uint64_t byte = word_index * 4;
    if (byte + 4 > dataset_bytes_) return false;
    cl_mem buf = dataset_; uint64_t off = byte;
    if (dataset2_) { const uint64_t piece = dataset_bytes_ / (dataset3_ ? 4 : 2); const uint64_t q = byte / piece; buf = q == 0 ? dataset_ : q == 1 ? dataset2_ : q == 2 ? dataset3_ : dataset4_; off = byte - q * piece; }
    cl_int e = write ? clEnqueueWriteBuffer(queue_, buf, CL_TRUE, off, 4, value, 0, nullptr, nullptr) : clEnqueueReadBuffer(queue_, buf, CL_TRUE, off, 4, value, 0, nullptr, nullptr);
    return e == CL_SUCCESS;
}

bool ClEngineX::dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t cd[32], uint8_t dg[32]) {
    if (!cand_ || cand_->kind != Kind::igneumhash || !ctx_ || !dataset_) return false;
    ih_warm_finish(); clFinish(queue_);
    std::memcpy(E, ih_E_, 32);
    { uint8_t e[32], cd2[32], dg2[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(ctx_, e, D, cd2, dg2, &bms, &hd) != IGNPOW_OK) return false; }
    // the cache as the reference holds it (the device copy is a byte copy of it); the dataset as the device holds it
    { const void *c = nullptr; uint64_t cb = 0; uint32_t ib = 0; std::memset(cd, 0, 32); if (ignpow_ctx_light_cache(ctx_, &c, &cb, &ib) == IGNPOW_OK && c && cb) ih_blake3_full((const uint8_t *)c, (size_t)cb, cd); }
    IhB3 hsh; const size_t chunk = 64u << 20; std::vector<uint8_t> buf(chunk);
    if (knobs_.variant == "swizzle") {   // the items are permuted on the device: the digest walks them in logical order through the slot map
        std::vector<uint8_t> all((size_t)dataset_bytes_);
        for (uint64_t off = 0; off < dataset_bytes_; off += chunk) {
            size_t n = (size_t)(dataset_bytes_ - off < chunk ? dataset_bytes_ - off : chunk);
            if (clEnqueueReadBuffer(queue_, dataset_, CL_TRUE, off, n, all.data() + off, 0, nullptr, nullptr) != CL_SUCCESS) return false;
        }
        const uint32_t iw4 = ih_.item_bytes, n_items = ih_.n_items(), mask = n_items - 1;
        for (uint32_t i = 0; i < n_items; i++) hsh.update(all.data() + (size_t)ih_swizzle_slot(i, mask) * iw4, iw4);
        hsh.finalize(dg);
        return true;
    }
    auto feed = [&](cl_mem m, uint64_t bytes) {
        for (uint64_t off = 0; off < bytes; off += chunk) {
            size_t n = (size_t)(bytes - off < chunk ? bytes - off : chunk);
            if (clEnqueueReadBuffer(queue_, m, CL_TRUE, off, n, buf.data(), 0, nullptr, nullptr) != CL_SUCCESS) return false;
            hsh.update(buf.data(), n);
        }
        return true;
    };
    if (dataset3_) { const uint64_t q = dataset_bytes_ / 4; if (!feed(dataset_, q) || !feed(dataset2_, q) || !feed(dataset3_, q) || !feed(dataset4_, dataset_bytes_ - 3 * q)) return false; }
    else if (dataset2_) { const uint64_t half = dataset_bytes_ / 2; if (!feed(dataset_, half) || !feed(dataset2_, dataset_bytes_ - half)) return false; }
    else if (!feed(dataset_, dataset_bytes_)) return false;
    hsh.finalize(dg);
    return true;
}

bool ClEngineX::prepare_ready() const { return !next_dataset_ || !ih_next_ctx_ || ih_next_cursor_ >= ih_next_items_; }

void ClEngineX::ih_warm_step(uint32_t items) {   // one slice of the next epoch's dataset on the mining queue
    if (!next_dataset_ || !ih_next_ctx_ || !ih_next_batchbuf_ || ih_next_cursor_ >= ih_next_items_) return;
    uint32_t count = ih_next_items_ - ih_next_cursor_ < items ? ih_next_items_ - ih_next_cursor_ : items;
    IhDatasetBatch b{ih_next_cursor_, count, 0, 0, ih_consts_of(ih_next_ih_, ih_next_P_, ih_next_E_)};
    clEnqueueWriteBuffer(queue_, ih_next_batchbuf_, CL_TRUE, 0, sizeof b, &b, 0, nullptr, nullptr);
    cl_mem cachearg = ih_next_light_ ? ih_next_light_ : next_dataset_;
    clSetKernelArg(dag_k_, 0, sizeof(cl_mem), &ih_next_batchbuf_); clSetKernelArg(dag_k_, 1, sizeof(cl_mem), &next_dataset_); clSetKernelArg(dag_k_, 2, sizeof(cl_mem), &cachearg);
    size_t global = count;
    double t0 = now_ms();
    if (clEnqueueNDRangeKernel(queue_, dag_k_, 1, nullptr, &global, nullptr, 0, nullptr, nullptr) != CL_SUCCESS) return;
    clFlush(queue_);
    ih_next_cursor_ += count; last_table_ms_ += now_ms() - t0;
}

void ClEngineX::ih_warm_finish() {   // the slices left at the swap, synchronously: the residual stall
    while (!prepare_ready()) ih_warm_step(1u << 18);
    clFinish(queue_);
}
ClEngineX::~ClEngineX() { close(); if (build_queue_) clReleaseCommandQueue(build_queue_); if (queue_) clReleaseCommandQueue(queue_); if (ctxcl_) clReleaseContext(ctxcl_); }

// The chip token of a driver's device line: the second comma-separated token inside the first parentheses ("AMD Radeon
// RX 580 Series (radeonsi, polaris10, ACO, DRM 3.61, ...)" gives polaris10); a line without that shape is cut to its
// first 32 bytes at a word boundary.
std::string cl_chip_token(const std::string &name) {
    auto lp = name.find('('), rp = name.find(')', lp == std::string::npos ? 0 : lp);
    if (lp != std::string::npos && rp != std::string::npos && rp > lp) {
        std::string inner = name.substr(lp + 1, rp - lp - 1);
        std::vector<std::string> toks; std::string cur;
        for (char c : inner) { if (c == ',') { toks.push_back(cur); cur.clear(); } else cur += c; }
        toks.push_back(cur);
        for (auto &t : toks) { size_t a = t.find_first_not_of(' '), b = t.find_last_not_of(' '); t = a == std::string::npos ? "" : t.substr(a, b - a + 1); }
        if (toks.size() >= 2 && !toks[1].empty() && toks[1].size() <= 32) return toks[1];
    }
    std::string cut = name.substr(0, 32);
    auto sp = cut.find_last_of(' ');
    if (sp != std::string::npos && sp > 8) cut = cut.substr(0, sp);
    return cut;
}

Json ClEngineX::device_json() const {
    std::string vendor = "other", v = info_.vendor; for (auto &c : v) c = (char)tolower(c);
    if (info_.type == CL_DEVICE_TYPE_GPU) { if (v.find("advanced micro") != std::string::npos || v.find("amd") != std::string::npos) vendor = "amd"; else if (v.find("nvidia") != std::string::npos) vendor = "nvidia"; else if (v.find("intel") != std::string::npos) vendor = "intel"; }
    std::string name = info_.board.empty() ? info_.name : info_.board;
    std::string id = info_.pci.empty() ? info_.platform_name + "/" + std::to_string(index_) : info_.pci;
    // Mesa's rusticl names a card by the driver's whole line ("AMD Radeon RX 580 Series (radeonsi, polaris10, ACO, DRM
    // 3.61, 6.12.111+deb13-amd64)", 74 to 83 bytes) and reports no gfx arch: the engine refused every such record on
    // its arch bound (the Octominer rig, 16 October 2026). Without an arch the compute word is the chip token inside the
    // parentheses (polaris10, navi33, gfx1200) and the whole line rides beside it; a short name stays as it was (the
    // fingerprint hashes the compute word, so nothing that fit before changes).
    std::string compute = info_.arch.empty() ? info_.name : info_.arch, compute_detail;
    if (info_.arch.empty() && info_.name.size() > 32) {
        compute_detail = info_.name;
        compute = cl_chip_token(info_.name);
    }
    const auto ident = cl_identity(info_, id);
    return Json::object().set("index", index_).set("backend", "opencl").set("name", name).set("vendor", vendor).set("id", id)
        .set("identity", ident.first).set("intrinsic_id", ident.second).set("identity_note", info_.identity_note).set("adlx_identity", cl_adlx_identity(info_)).set("fingerprint", cl_fingerprint(vendor, ident.second, name, compute)).set("memory_total", (unsigned long long)info_.global_mem)
        .set("memory_free", (unsigned long long)free_memory_estimate()).set("compute", compute).set("compute_detail", compute_detail).set("driver", info_.driver + " (" + info_.version + ")").set("platform", info_.platform_name);
}

Json ClEngineX::knob_decls() const {
    return Json::array()
        .push(Json::object().set("name", "threadgroup").set("min", 1).set("max", 16).set("step", 1).set("default", 4))
        .push(Json::object().set("name", "batch_ms").set("min", 10).set("max", 500).set("step", 10).set("default", 50))
        .push(Json::object().set("name", "duty").set("min", 0.25).set("max", 1.0).set("step", 0.25).set("default", 1.0))
        .push(Json::object().set("name", "working_set_mb").set("min", 0).set("max", 65536).set("step", 256).set("default", 0));
}

uint64_t ClEngineX::free_memory_estimate() const {
    if (info_.extensions.find("cl_amd_device_attribute_query") != std::string::npos) {
        size_t free_kb[2] = {0, 0};
        if (clGetDeviceInfo(info_.device, CL_DEVICE_GLOBAL_FREE_MEMORY_AMD, sizeof free_kb, free_kb, nullptr) == CL_SUCCESS) return (uint64_t)free_kb[0] * 1024;
    }
    return info_.global_mem > allocated_ ? info_.global_mem - allocated_ : 0;
}

void ClEngineX::wipe_secret() {
    if (!queue_) return;
    static const uint8_t zeros[sizeof(IhJob) > sizeof(RxJob) ? sizeof(IhJob) : sizeof(RxJob)] = {0};
    for (cl_mem b : job_buf_) if (b) clEnqueueWriteBuffer(queue_, b, CL_TRUE, 0, sizeof zeros, zeros, 0, nullptr, nullptr);   // the job buffers are created at exactly this size (the Windows link's shim carries no clGetMemObjectInfo)
    clFinish(queue_);
}
bool ClEngineX::keyed_document() const { return cand_ && cand_->kind == Kind::igneumhash && ih_.keyed_rounds > 0; }
void ClEngineX::telemetry(Json &t) const { t.set("throttle", Json::array()).set("memory_free_bytes", std::to_string(free_memory_estimate())).set("last_table_build_ms", last_table_ms_).set("build_in_flight", !prepare_ready()).set("build_progress", ih_next_items_ ? (double)ih_next_cursor_ / ih_next_items_ : 0.0).set("last_swap_wait_ms", last_swap_wait_ms_); cl_adlx_telemetry(info_, t); cl_intel_telemetry(info_, t); }   // the Intel hook here too: the X engine's telemetry is its own line, not the base's (the B580 probe-1 rows carried no power_source, 11 Oct 2026)

Json ClEngineX::budget_json() const {
    uint64_t free_now = free_memory_estimate();
    uint64_t budget = free_now > (256ull << 20) ? free_now - (256ull << 20) : 0;
    return Json::object().set("total_bytes", std::to_string(info_.global_mem)).set("free_bytes", std::to_string(free_now))
        .set("fit_cold_bytes", std::to_string(allocated_)).set("fit_warm_bytes", std::to_string(allocated_ + dataset_bytes_))
        .set("can_warm", dataset_bytes_ > 0 && dataset_bytes_ + (64ull << 20) <= budget).set("margin_bytes", std::to_string(256ull << 20)).set("display_overhead", "not measured on this host");
}

Json ClEngineX::code_json() const {
    Json c = Json::object();
    if (!search_k_) return c;
    cl_ulong priv = 0, loc = 0; size_t pref = 0, wgmax = 0;
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PRIVATE_MEM_SIZE, sizeof priv, &priv, nullptr);
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_LOCAL_MEM_SIZE, sizeof loc, &loc, nullptr);
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PREFERRED_WORK_GROUP_SIZE_MULTIPLE, sizeof pref, &pref, nullptr);
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_WORK_GROUP_SIZE, sizeof wgmax, &wgmax, nullptr);
    uint32_t lanes = cand_ && cand_->kind == Kind::rxe1 ? rx_.group : cand_ && cand_->kind == Kind::igneumhash ? ih_lanes() : cand_ && cand_->kind == Kind::fishhash ? fh_lanes_ : 1u;
    c.set("private_mem_bytes", (double)priv).set("local_mem_bytes", (double)loc + (double)(knobs_.threadgroup * wave_ * 4)).set("preferred_multiple", (double)pref).set("kernel_max_workgroup", (double)wgmax)
     .set("wave", (double)wave_).set("workgroup", (double)(knobs_.threadgroup * wave_)).set("lanes_per_nonce", (double)lanes).set("nonces_per_workgroup", (double)(knobs_.threadgroup * wave_ / lanes)).set("batches_in_flight", (double)knobs_.inflight).set("nonces_per_batch_max", (double)(ih_slots_ / (knobs_.inflight ? knobs_.inflight : 1)))
     .set("registers", "not exposed by this OpenCL runtime (private_mem_bytes is where spills land)");
    c.set("kernel_source_sha256", src_sha256_).set("kernel_binary_sha256", bin_sha256_.empty() ? "n/a" : bin_sha256_).set("kernel_binary_bytes", (double)bin_bytes_).set("kernel_binary_file", bin_path_.empty() ? "not written (IGNEUM_DUMP_BINARY unset)" : bin_path_)
     .set("opencl_c_version", info_.opencl_c).set("device_version", info_.version).set("driver", info_.driver)
     .set("binary_note", "the program binary of the card's own compiler (CL_PROGRAM_BINARIES), written beside the row when IGNEUM_DUMP_BINARY names a path; pow/harness/riptide/amd-binary-read.py reads its register, scratch and instruction counts on a build box");
    if (cand_ && cand_->kind == Kind::rxe1) c.set("schedule_ops", (double)((uint64_t)rx_.rounds * rx_.blocks * rx_.registers)).set("schedule_descriptors_bytes", (double)rx_sched_host_.size() * 4).set("reads_per_nonce", (double)rx_.rounds).set("bytes_read_per_nonce", (double)rx_.rounds * 128);
    if (cand_ && cand_->kind == Kind::igneumhash) c.set("reads_per_nonce", (double)ih_.rounds).set("bytes_read_per_nonce", (double)ih_.rounds * ih_.item_bytes);
    if (cand_ && cand_->kind == Kind::igneumhash && (knobs_.variant == "diag-a" || knobs_.variant == "diag-b")) c.set("bytes_read_per_nonce", (double)ih_.rounds * knobs_.diag_width * (knobs_.variant == "diag-b" ? knobs_.diag_chains : 1)).set("chains_per_nonce", (double)(knobs_.variant == "diag-b" ? knobs_.diag_chains : 1)).set("request_bytes", (double)knobs_.diag_width).set("request_shape", "eight lanes, W-aligned item, W/128 coalesced 128-byte steps of 16 bytes per lane (64 bytes: lanes 0 to 3 only)");
    if (cand_ && cand_->kind == Kind::igneumhash) c.set("dataset_copies", (double)knobs_.copies).set("dataset_shift_bytes", (double)knobs_.shift_bytes).set("layout", knobs_.copies > 1 ? "channel-coloured: " + std::to_string(knobs_.copies) + " copies, the copy by the nonce's low bits" : knobs_.shift_bytes ? "shifted allocation: the dataset " + std::to_string(knobs_.shift_bytes) + " bytes into a larger buffer" : "stock: one buffer, the natural origin");
    if (cand_ && cand_->kind == Kind::igneumhash) c.set("params_id", "igneumhash-v" + std::to_string(ih_.version) + "-" + ih_.variant()).set("physical_bytes_per_hash", "not exposed by this runtime");
    {   // the built program's binary hash (the runner's row record, decision D): the first device binary, BLAKE3 over its bytes
        size_t nb = 0; if (prog_ && clGetProgramInfo(prog_, CL_PROGRAM_BINARY_SIZES, sizeof nb, &nb, nullptr) == CL_SUCCESS && nb) {
            std::vector<unsigned char> bin(nb); unsigned char *bp = bin.data();
            if (clGetProgramInfo(prog_, CL_PROGRAM_BINARIES, sizeof bp, &bp, nullptr) == CL_SUCCESS) { IhB3 h; h.update(bin.data(), nb); uint8_t dg[32]; h.finalize(dg); c.set("binary_blake3", hex_of(dg, 32)).set("binary_bytes", (double)nb); }
        }
    }
    c.set("compiler", info_.driver + " (" + info_.version + "); the OpenCL C front end of that driver").set("vgpr", "not exposed by this OpenCL runtime").set("sgpr", "not exposed by this OpenCL runtime").set("occupancy", "not exposed by this OpenCL runtime").set("load_instructions_per_item", "not exposed (the offline gfx1201 compile is the source)").set("shuffle_count", "not exposed").set("store_count", "not exposed");
    c.set("nonces_per_batch_set", (double)knobs_.batch_nonces).set("grid_pct", (double)knobs_.grid_pct).set("nonces_per_batch_last", (double)last_hashes_);
    if (cand_ && cand_->kind == Kind::igneumhash && is_bucket()) c.set("solver", "bucket pipeline (T-B14): per round the request, the histogram, the scan, the scatter, the fetch in bucket order (eight work-items per request), the merge from global state one work-item per nonce (the T-B3 n1 lane form: no barrier in the pipeline)").set("bucket_bits", (double)knobs_.bucket_bits).set("bucket_batch", (double)ihb_batch_)
        .set("state_bytes_per_hash_per_round", 2.0 * 640).set("temporary_bytes_per_hash", 640.0 + 128.0 + 12.0).set("solver_note", "an experiment, not a protection: the energy on this row includes the queueing, the sort, the state movement and the temporary storage");
    if (cand_ && !c.has("bytes_read_per_nonce")) c.set("bytes_read_per_nonce", (double)bytes_read_per_hash(cand_->kind));
    // the allocation form as a recorded variable (research brief R1, section 7): which arrangement this row used
    if (cand_ && (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1))
        c.set("allocation", dataset3_ ? "four buffers of " + std::to_string(dataset_bytes_ >> 22) + " MiB" : dataset2_ ? "two buffers of " + std::to_string(dataset_bytes_ >> 21) + " MiB" : "one buffer of " + std::to_string(dataset_bytes_ >> 20) + " MiB")
         .set("page_arrangement", "not exposed by this OpenCL runtime (no page-size or placement control)");
    // the brief's counters (research brief R1, section 12) that this runtime does not expose, named as such
    c.set("resident_waves", "not exposed by this OpenCL runtime").set("memory_requests_per_payload_byte", "not exposed (the kernel's logical count: one 128-byte request per round per nonce)")
     .set("cache_behaviour", "not exposed").set("stalls", "not exposed").set("integer_throughput", "not exposed").set("lane_exchange", ih_wave_ ? "AMD wave intrinsics (ds_bpermute for the broadcast, ds_swizzle for the xor-partner levels; inert on a compiler that is not AMD LC, where the row's path is the next word): " + std::string(fh_shuffle_ ? "sub-group shuffle" : "local memory under workgroup barriers") : fh_shuffle_ ? "sub-group shuffle" : "local memory under workgroup barriers (the runtime reports no sub-group shuffle)").set("item_words_static", ih_.version >= 3).set("finalisation", ih_stream_ && ih_.scratch_bytes == 0 ? "streamed (static block words, the state gathered by the exchange, no state area)" : "byte builder over the state area in global memory")
     .set("physical_traffic", "not exposed by this OpenCL runtime (RDNA fetch-size and transaction counters need an external profiler)").set("occupancy", "not exposed").set("register_pressure", "not exposed (private_mem_bytes is the spill proxy)");
    return c;
}

std::string ClEngineX::describe() const { return "opencl " + std::string(cand_ ? cand_->id : "?") + (cand_ && cand_->kind == Kind::rxe1 ? " " + rx_.variant() + " " + std::to_string(rx_.group) + " lanes per nonce" : "") + (cand_ && cand_->kind == Kind::igneumhash ? " " + ih_.variant() + " scratch in global memory" + (knobs_.variant.empty() ? "" : " kernel variant " + knobs_.variant + (knobs_.variant == "reach-half" ? " (DIAGNOSTIC: wrong hash by design, never a cell)" : "")) + (dataset2_ ? " (dataset in two buffers)" : "") + (ih_v6hoist_ ? " version 6 hoisted key gather" : "") + (ih_occ_ ? " residency pinned at " + std::to_string(ih_occ_) + " waves per SIMD" : "") + (ih_lean_ ? " lean walk (S recomputed at the finaliser)" : "") + (ih_dpp_ ? " DPP exchanges" : "") + (ih_sel_ ? " branchless selects" : "") + (ih_pl_ ? " permlane group reads" : "") + (ih_span_ ? " span loads (one item per instruction across the wave)" : "") + (ih_x2_ ? " two chains per group" : "") + (ih_v8s_ ? " version 8 snapshot form (K, B, F at build time; the snapshot in local memory)" : "") + (knobs_.lds_pad_kib ? " LDS pad " + std::to_string(knobs_.lds_pad_kib) + " KiB per workgroup (the occupancy limiter)" : "") + (bw_keep_ ? " twin register pin " + std::to_string(bw_keep_) + " words" : "") + (ih_xfer_ ? " XFER DIAGNOSTIC (the matched-resource dependent ceiling: wrong hash by design, never a cell)" : "") + (bw_loader_ ? " Breakwater loader " + std::to_string(bw_loader_) + (bw_red_ ? " reduction r" + std::to_string(bw_red_) : " transfer-only") + " (" + std::to_string(ih_lanes()) + " lanes per nonce)" + (bw_oldfin_ ? " old builder" : "") : ih_wave_ ? (ih_stream_ ? " wave intrinsics, streamed finalisation" : " wave intrinsics") : "") : "") + " " + info_.platform_name + " wave " + std::to_string(wave_) + (cand_ && (cand_->kind == Kind::igneumhash || (cand_->kind == Kind::fishhash && fh_lanes_ == 8)) ? (fh_shuffle_ ? " eight lanes per hash (shuffle)" : " eight lanes per hash (local exchange)") : " one work-item per hash") + " workgroup " + std::to_string(knobs_.threadgroup * wave_); }

// the dataset's allocation form, ONE predicate for the kernel's compile (IGM_IH_SPLIT2) and the allocation: two buffers of half the size when the
// knobs say so, or when one buffer would exceed 2 GiB (T-B8: the AMD driver loses 17 percent above it) or the device's largest allocation
bool ClEngineX::split_form(uint64_t bytes) const { return split_pieces(bytes) > 1; }
// the piece count: 1, 2 (halves) or 4 (quarters, when a half still exceeds the device's largest allocation: version 15's 4 GiB on rusticl's 2,047 MB cap, 13 Oct 2026)
unsigned ClEngineX::split_pieces(uint64_t bytes) const {
    if (knobs_.variant == "onebuf" || knobs_.alloc == "onebuf") return 1;
    if (knobs_.alloc == "split4") return 4;   // the forced four-piece form (the exactness read of the quarter path on a device with room)
    const bool two = knobs_.variant == "split2" || knobs_.alloc == "split2" || bytes > (2ull << 30) || bytes > info_.max_alloc;
    if (!two) return 1;
    return (bytes / 2 > info_.max_alloc) ? 4 : 2;
}

bool ClEngineX::build_programs(Refusal &r) {
    if (search_k_) return true;
    cl_int err = 0;
    fh_shuffle_ = info_.subgroups && info_.subgroup_shuffle;   // the sub-group size is read after the build; a width not a multiple of eight falls back to the local exchange
    if (knobs_.exchange == "shuffle") fh_shuffle_ = true;   // forced (the probe of 10 Oct 2026: the AMD Windows runtime names no cl_khr_subgroup_shuffle; the builtins may exist under -cl-std=CL2.0 regardless); a build refusal is the row
    else if (knobs_.exchange == "local") fh_shuffle_ = false;
    if (!r0d_from_job_) r0d_height_ = height_;   // the programme's period: the open height, or the job's when run_job rebuilt for a new period
    r0d_from_job_ = false; r0d_period_ = (ih_.version >= 9 && ih_.period_blocks) ? r0d_height_ / ih_.period_blocks : ~0ull;
    // F-66 (the security review, 12 Oct 2026): the bit 5 compile rules are explicit and a build option that would relax them is refused
    for (const char *bad : {"-cl-fast-relaxed-math", "-cl-unsafe-math-optimizations", "-cl-mad-enable", "-cl-finite-math-only", "-cl-no-signed-zeros"})
        if (knobs_.clopts.find(bad) != std::string::npos) { r.code = "params"; r.explain = std::string("the build option ") + bad + " relaxes the bit 5 compile rules (F-66): refused"; return false; }
    const auto r0d_t0 = std::chrono::steady_clock::now();
    std::string r0d_src, r0d_err;   // version 9 (R0D): the period programme generated on the host from the seed knob (or derived), one function in the source
    if (cand_->kind == Kind::igneumhash && ih_.version == 16) { if (!r16f_program_source(ih_, knobs_, r0d_height_, false, r0d_src, r16f_key_, r0d_err) || r0d_src.empty()) { r.code = "params"; r.explain = "R0G programme: " + r0d_err; return false; } r0d_src = "#define IGM_R16F_TAILS " + std::to_string(ih_.program_distance) + "\n" + r0d_src; }   // VERSION 16: the period's static-fabric programme as one function in the source, the key kept for the kernel's buffer
    else if (cand_->kind == Kind::igneumhash && ih_.version == 15 && r0f_static()) { if (!r0f_program_source(ih_, knobs_, r0d_height_, false, r0d_src, r16f_key_, r0d_err) || r0d_src.empty()) { r.code = "params"; r.explain = "R0F programme: " + r0d_err; return false; } r0d_src = "#define IGM_R0F_STATIC 1\n" + r0d_src; }
    else if (cand_->kind == Kind::igneumhash && ih_.version >= 9 && ih_.version != 15 && ih_.version != 17 && !ih_.forge()) { r0d_src = r0d_program_source(ih_, knobs_, r0d_height_, false, r0d_err); if (r0d_src.empty()) { r.code = "params"; r.explain = "R0D programme: " + r0d_err; return false; } }
    if (cand_->kind == Kind::igneumhash && ih_.version == 15 && r0f_trace_on_) r0d_src = "#define IGM_R0F_TRACE 1\n" + r0d_src;
    std::string src = cl_intel_defines(info_) + ((cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14 && r0e_optrace_on_) ? "#define IGM_R0E_OPTRACE 1\n#define IGM_R0E_OPS " + std::to_string(ih_.program_ops) + "u\n" : std::string()) + kernel_source(kernel_dir_, "prelude_cl.inc");   // the per-operation trace defines ahead of the prelude (its macros read them)   // IGM_INTEL_ROTR_FOLD on an Intel platform (the rotate fold of 7 October 2026); empty elsewhere
    if (cand_->kind == Kind::fishhash) src = "#define IGM_FH_SHUFFLE " + std::string(fh_shuffle_ ? "1" : "0") + "\n" + src + kernel_source(kernel_dir_, "igm_fishhash.inc") + kernel_source(kernel_dir_, "wrap_fishhash_cl.inc") + kernel_source(kernel_dir_, "igm_fishhash8.inc") + kernel_source(kernel_dir_, "wrap_fishhash8_cl.inc");
    else if (cand_->kind == Kind::rxe1) src = "#define IGM_FH_SHUFFLE " + std::string(fh_shuffle_ ? "1" : "0") + "\n" + rx_probe_define() + src + kernel_source(kernel_dir_, "igm_fishhash.inc") + kernel_source(kernel_dir_, "wrap_fishhash_cl.inc") + kernel_source(kernel_dir_, "igm_igneumhash.inc") + kernel_source(kernel_dir_, "igm_igneumhash1.inc") + kernel_source(kernel_dir_, "wrap_igneumhash_cl.inc") + kernel_source(kernel_dir_, "igm_rxe1.inc") + kernel_source(kernel_dir_, "wrap_rxe1_cl.inc");
    else if (cand_->kind == Kind::igneumhash) src = "#define IGM_FH_SHUFFLE " + std::string(fh_shuffle_ ? "1" : "0") + "\n#define IGM_IH_VERSION " + std::to_string(ih_.version) + "\n#define IGM_R17_ROUNDS " + std::to_string(ih_.version == 17 ? ih_.rounds : 512u) + "u" + "\n#define IGM_FORGE_DRAWN " + std::to_string(ih_.version == 20 ? (ih_.program_distance & 1u) : 0u) + "\n#define IGM_FORGE_OLDCARD " + std::to_string(ih_.version == 25 ? ih_.scratch_bytes : 0u) + "u\n#define IGM_FORGE_FPMODE " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 1) & 7u) : 0u) + "\n#define IGM_FORGE_PAIR_LOG2 " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 4) & 15u) : 0u) + "\n#define IGM_FORGE_LEAN " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 15) & 1u) : 0u) + "\n#define IGM_FORGE_PERIOD_TABLE " + std::to_string(ih_.forge2() && ih_.period_blocks && !getenv("IGM_FORGE_NO_TABLE") ? 1u : 0u) + "\n#define IGM_FORGE_PREFETCH " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 8) & 127u) : 0u) + "\n#define IGM_FORGE_TRACE " + std::string(getenv("IGM_FORGE_TRACE") ? "1" : "0") + "\n#define IGM_IH_ITEM_WORDS " + std::to_string(ih_.item_words()) + "u\n#define IGM_IH_SLICE_WORDS " + std::to_string(ih_.slice_words()) + "u\n#define IGM_IH_KEYED_S " + std::to_string(ih_.keyed_rounds) + "u\n" + (ih_.version >= 12 ? "#define IGM_IH_TKW " + std::to_string(ih_.table_words()) + "u\n#define IGM_IH_TILE_LAYOUT " + std::to_string(ih_.tile_layout) + "u\n#define IGM_IH_AW " + std::to_string(ih_.access_bytes / 4) + "u\n#define IGM_IH_SHW " + std::to_string(ih_.shared_words()) + "u\n" : std::string()) + "" + (ih_.rw() != 16 ? "#define IGM_IH_RW " + std::to_string(ih_.rw()) + "u\n" : "") + (ih_wave_ ? "#define IGM_IH_AMD_WAVE 1\n" : "") + (knobs_.copies > 1 ? "#define IGM_IH_COPIES " + std::to_string(knobs_.copies) + "u\n" : "") + (ih_xfer_ ? "#define IGM_IH_XFER_DIAG 1\n#define IGM_IH_XFER_KEEP " + std::to_string(knobs_.xfer_keep) + "u\n" : "") + (ih_v6hoist_ ? "#define IGM_IH_V6_HOIST 1\n" : "") + (ih_occ_ ? "#define IGM_IH_OCC " + std::to_string(ih_occ_) + "\n" : "") + (ih_lean_ ? "#define IGM_IH_LEAN 1\n" : "") + (ih_dpp_ ? "#define IGM_IH_DPP 1\n" : "") + (ih_sel_ ? "#define IGM_IH_SEL 1\n" : "") + (ih_pl_ ? "#define IGM_IH_PL 1\n" : "") + (ih_span_ ? "#define IGM_IH_SPAN 1\n" : "") + (ih_x2_ ? "#define IGM_IH_X2 1\n" : "") + (ih_v8s_ && ih_.version == 8 ? "#define IGM_IH_V8S 1\n#define IGM_IH_V8_K " + std::to_string(ih_.registers) + "u\n#define IGM_IH_V8_B " + std::to_string(ih_.blocks) + "u\n#define IGM_IH_V8_F " + std::to_string(ih_.features) + "u\n#define IGM_IH_V8S_OFF " + std::to_string(ih_local_words()) + "u\n" : "") + (bw_keep_ ? "#define IGM_BW_KEEP " + std::to_string(bw_keep_) + "u\n" : "") + (bw_loader_ ? "#define IGM_BW_LOADER " + std::to_string(bw_loader_) + "\n#define IGM_BW_RED " + std::to_string(bw_red_) + "\n" + (bw_loader_ == 4 ? "#define IGM_BW_LANES 64u\n" : "") + (bw_oldfin_ ? "#define IGM_BW_OLDFIN 1\n" : "") + "#define IGM_WAVE " + std::to_string(wave_) + "\n" : "") + (ih_stream_ && ih_.scratch_bytes == 0 ? "#define IGM_IH_STREAM_FINAL 1\n" : "") + (knobs_.variant == "form1" ? "#define IGM_IH_V4_FORM 1\n" : knobs_.variant == "form2" ? "#define IGM_IH_V4_FORM 2\n" : "") + (knobs_.variant == "load4" ? "#define IGM_IH_LOAD4 1\n" : "") + (knobs_.variant == "reach-half" ? "#define IGM_IH_REACH_MASK 1\n" : "") + (knobs_.variant == "diag-a" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : "") + (knobs_.variant == "diag-b" ? "#define IGM_IH_DIAG_B 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : "") + (knobs_.variant == "diag-b" && knobs_.diag_chains > 1 ? "#define IGM_IH_DIAG_CHAINS " + std::to_string(knobs_.diag_chains) + "\n" : "") + (knobs_.variant == "swizzle" ? "#define IGM_IH_SWIZZLE 1\n" : "") + (knobs_.variant == "overfetch256" ? "#define IGM_IH_OVERFETCH 256u\n" : knobs_.variant == "overfetch512" ? "#define IGM_IH_OVERFETCH 512u\n" : "") + (ih_ctx() > 1 ? "#define IGM_IH_CTX " + std::to_string(ih_ctx()) + "\n" : "") + (split_pieces(ih_.dataset_bytes) == 4 ? "#define IGM_IH_SPLIT4 1\n" : split_pieces(ih_.dataset_bytes) == 2 ? "#define IGM_IH_SPLIT2 1\n" : "") + src + kernel_source(kernel_dir_, "igm_fishhash.inc") + kernel_source(kernel_dir_, "wrap_fishhash_cl.inc") + (ih_.version >= 12 ? kernel_source(kernel_dir_, "igm_r0e_access.inc") : std::string()) + r0d_src + kernel_source(kernel_dir_, "igm_igneumhash.inc") + (ih_.version == 15 ? kernel_source(kernel_dir_, "igm_r0f.inc") : ih_.version == 16 ? kernel_source(kernel_dir_, "igm_r16f.inc") : ih_.version == 17 ? kernel_source(kernel_dir_, "igm_r17.inc") : ih_.forge() ? kernel_source(kernel_dir_, "igm_forge.inc") : std::string()) + kernel_source(kernel_dir_, "igm_igneumhash1.inc") + (ih_x2_ ? kernel_source(kernel_dir_, "igm_igneumhash_x2.inc") : std::string()) + kernel_source(kernel_dir_, "wrap_igneumhash_cl.inc") + (bw_loader_ ? kernel_source(kernel_dir_, "igm_igneumhash_bw.inc") + kernel_source(kernel_dir_, "wrap_igneumhash_bw_cl.inc") : std::string()) + (ih_ctx() > 1 ? kernel_source(kernel_dir_, "igm_igneumhash_ctx.inc") + kernel_source(kernel_dir_, "wrap_igneumhash_ctx_cl.inc") : std::string()) + (is_bucket() ? kernel_source(kernel_dir_, "igm_igneumhash_bucket.inc") + kernel_source(kernel_dir_, "wrap_igneumhash_bucket_cl.inc") : std::string());
    else src += kernel_source(kernel_dir_, "igm_autolykos.inc") + kernel_source(kernel_dir_, "wrap_autolykos_cl.inc");
    last_src_ = src; ih_built_version_ = ih_.version * 1024u + ih_.item_words() + ih_.rw() * 65536u + r0d_key(ih_, knobs_, r0d_height_) + (ih_v8s_ ? (ih_.registers * 7u + ih_.blocks * 131u + ih_.features) * 4194304u : 0u);   // the snapshot form bakes K, B and F into the program: a new document rebuilds it
    const char *s = src.c_str(); size_t len = src.size();
    prog_ = clCreateProgramWithSource(ctxcl_, 1, &s, &len, &err);
    const std::string bopts = knobs_.clopts + (bw_loader_ == 4 ? " -mwavefrontsize64" : "");   // Breakwater loader 4: the wave64 comparison (a refusal by the runtime is the row)
    if (clBuildProgram(prog_, 1, &info_.device, bopts.c_str(), nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "kernel build: " + build_log(prog_, info_.device); return false; }
    {   // the ten-field row record (Breakwater, decision D): the source the runtime compiled and the binary its compiler produced, by hash; the binary written beside the row when the job asks (IGNEUM_DUMP_BINARY)
        src_sha256_ = sha256_hex(src); bin_sha256_.clear(); bin_bytes_ = 0;
        size_t nb = 0; clGetProgramInfo(prog_, CL_PROGRAM_BINARY_SIZES, sizeof nb, &nb, nullptr);
        if (nb > 0) {
            std::string bin(nb, '\0'); unsigned char *bp = (unsigned char *)bin.data();
            if (clGetProgramInfo(prog_, CL_PROGRAM_BINARIES, sizeof bp, &bp, nullptr) == CL_SUCCESS) {
                bin_sha256_ = sha256_hex(bin); bin_bytes_ = nb;
                if (const char *dump = getenv("IGNEUM_DUMP_BINARY")) {   // a path ending in .bin, or "1": kernel-<variant>-w<item_bytes>.bin under IGNEUM_OUT (the agent sets no per-step environment)
                    std::string path = dump;
                    if (path.size() < 4 || path.compare(path.size() - 4, 4, ".bin") != 0) { const char *o = getenv("IGNEUM_OUT"); path = std::string(o && *o ? o : ".") + "/kernel-" + (variant_full_.empty() ? "stock" : variant_full_) + "-w" + std::to_string(ih_.item_bytes) + ".bin"; }
                    FILE *f = fopen(path.c_str(), "wb"); if (f) { fwrite(bin.data(), 1, nb, f); fclose(f); bin_path_ = path; }
                }
            }
        }
    }
    search_k_ = clCreateKernel(prog_, cand_->kind == Kind::fishhash ? (fh_lanes_ == 8 ? "igm_fishhash8_search" : "igm_fishhash_search") : cand_->kind == Kind::igneumhash ? (ih_.forge() ? "igm_forge_search" : ih_.version == 17 ? "igm_r17_search" : ih_.version == 16 ? "igm_r16f_search" : ih_.version == 15 ? "igm_r0f_search" : bw_loader_ ? "igm_igneumhash_bw_search" : knobs_.variant == "n1" ? "igm_igneumhash1_search" : ih_ctx() > 1 ? "igm_igneumhash_ctx_search" : "igm_igneumhash_search") : cand_->kind == Kind::rxe1 ? "igm_rxe1_search" : "igm_autolykos_search", &err);
    if (ih_.version >= 9 && ih_.version != 15 && ih_.version != 17 && !ih_.forge()) fprintf(stderr, "opencl: the period programme (%u operations) compiled in %lld ms (TP-245)\n", ih_.program_ops, (long long)std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - r0d_t0).count());
    if (err != CL_SUCCESS) {   // the error code and the kernels the program does carry (14 Oct 2026: version 17's kernel missing from the program on pocl)
        char names[4096] = {0}; clGetProgramInfo(prog_, CL_PROGRAM_KERNEL_NAMES, sizeof names - 1, names, nullptr);
        r.code = "driver"; r.explain = "search kernel create failed (" + std::to_string(err) + "); the program carries: " + std::string(names).substr(0, 600); return false;
    }
    if (cand_->kind == Kind::igneumhash && ih_.forge()) {   // FORGE: the version 15 argument tail bound with dummy buffers (no period key, no programme buffer)
        cl_int ke = 0; if (!r0f_key_) r0f_key_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 32, nullptr, &ke); if (!r0f_prog_) r0f_prog_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 16, nullptr, &ke);
        if (!r0f_key_ || !r0f_prog_) { r.code = "memory"; r.explain = "the forge dummy buffers failed"; return false; }
    }
    if (cand_->kind == Kind::igneumhash && ih_.version == 16) {   // VERSION 16: the period key words into the kernel's buffer (the programme is in the text); the dummy programme buffer keeps version 15's argument layout
        cl_int ke = 0; if (!r0f_key_) r0f_key_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 32, nullptr, &ke); if (!r0f_prog_) r0f_prog_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 16, nullptr, &ke);
        if (!r0f_key_ || !r0f_prog_) { r.code = "memory"; r.explain = "the version 16 key buffer failed"; return false; }
        clEnqueueWriteBuffer(queue_, r0f_key_, CL_TRUE, 0, 32, r16f_key_, 0, nullptr, nullptr); r0f_period_ = r0d_period_;
    }
    if (cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14 && r0e_optrace_on_ && !r0e_optrace_buf_) { cl_int te = 0; r0e_optrace_words_ = (size_t)4 * ih_.program_ops * 128; r0e_optrace_buf_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, r0e_optrace_words_ * 4, nullptr, &te); if (!r0e_optrace_buf_) { r.code = "memory"; r.explain = "the per-operation trace buffer failed"; return false; } }
    if (cand_->kind == Kind::igneumhash && ih_.version == 15 && r0f_trace_on_ && !r0f_trace_buf_) { cl_int te = 0; r0f_trace_buf_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, 2176 * 4, nullptr, &te); if (!r0f_trace_buf_) { r.code = "memory"; r.explain = "the version 15 trace buffer failed"; return false; } }
    size_t pref = 0; clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PREFERRED_WORK_GROUP_SIZE_MULTIPLE, sizeof pref, &pref, nullptr);
    {   // the compiled kernel's register and scratch figures as the runtime reports them (the 9060 XT port reading of 13 October 2026: rusticl prints no ACO statistics): private (scratch) bytes per work-item, the spill bytes where the runtime has them
        cl_ulong priv = 0, spill = 0; clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PRIVATE_MEM_SIZE, sizeof priv, &priv, nullptr);
#ifdef CL_KERNEL_SPILL_MEM_SIZE_AMD
        clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_SPILL_MEM_SIZE_AMD, sizeof spill, &spill, nullptr);
#endif
        size_t maxwg = 0; clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_WORK_GROUP_SIZE, sizeof maxwg, &maxwg, nullptr);
        fprintf(stderr, "opencl: kernel: private (scratch) %llu bytes per work-item, spill %llu bytes, preferred multiple %zu, max work-group %zu\n", (unsigned long long)priv, (unsigned long long)spill, pref, maxwg);
    }
    wave_ = pref ? (uint32_t)pref : 64;
    if (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) {
        dag_k_ = clCreateKernel(prog_, knobs_.variant == "build8" ? "igm_igneumhash_dataset8" : "igm_igneumhash_dataset", &err);
        if (ih_.version >= 12) { if (r12_fill_k_) clReleaseKernel(r12_fill_k_); r12_fill_k_ = clCreateKernel(prog_, "igm_r12_shared_fill", &err); r12_period_ = ~0ull; }   // the shared table's fill kernel (versions 12 to 14); the period's table is refilled on the next job
        if (!dag_k_) { r.code = "driver"; r.explain = "igneumhash dataset kernel missing"; return false; }
    }
    if (cand_->kind == Kind::igneumhash && is_bucket()) {
        struct { cl_kernel *k; const char *n; } ks[] = { {&ihb_setup_k_, "igm_ihb_setup"}, {&ihb_hist_k_, "igm_ihb_hist"}, {&ihb_scan_k_, "igm_ihb_scan"}, {&ihb_scatter_k_, "igm_ihb_scatter"}, {&ihb_fetch_k_, "igm_ihb_fetch"}, {&ihb_mix_k_, "igm_ihb_mix"}, {&ihb_final_k_, "igm_ihb_final"} };
        for (auto &e : ks) { *e.k = clCreateKernel(prog_, e.n, &err); if (!*e.k) { r.code = "driver"; r.explain = std::string("bucket kernel missing: ") + e.n; return false; } }
    }
    if ((cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1 || (cand_->kind == Kind::fishhash && fh_lanes_ == 8)) && fh_shuffle_ && wave_ % (cand_->kind == Kind::rxe1 ? rx_.group : 8) != 0) {
        if (knobs_.exchange == "shuffle") { r.code = "driver"; r.explain = "the forced sub-group path: the sub-group width is not a multiple of the lane count"; return false; }
        fprintf(stderr, "opencl: sub-group width %u is not a multiple of eight; FishHash uses the local exchange\n", wave_);
        clReleaseKernel(search_k_); search_k_ = nullptr; clReleaseProgram(prog_); prog_ = nullptr; fh_shuffle_ = false;
        info_.subgroup_shuffle = false; return build_programs(r);
    }
    if (cand_->kind == Kind::autolykos) {
        table_k_ = clCreateKernel(prog_, "igm_autolykos_table", &err);
        if (err != CL_SUCCESS) { r.code = "driver"; r.explain = "table kernel create failed"; return false; }
    } else if (cand_->kind == Kind::fishhash) {
        std::string dag = kernel_source(kernel_dir_, "igm_dag.cl");
        const char *d = dag.c_str(); size_t dl = dag.size();
        dag_prog_ = clCreateProgramWithSource(ctxcl_, 1, &d, &dl, &err);
        std::string opts = "-DIGM_DATASET_PARENTS=512 -DIGM_CACHE_NODES=1179641";
        if (clBuildProgram(dag_prog_, 1, &info_.device, opts.c_str(), nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel build: " + build_log(dag_prog_, info_.device); return false; }
        dag_k_ = clCreateKernel(dag_prog_, "igm_dag_generate", &err);
        if (err != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel create failed"; return false; }
    }
    return true;
}

bool ClEngineX::ih_build_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, uint8_t E_out[32], cl_mem &light_out, Refusal &r, bool async) {
    int st = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a context (status " + std::to_string(st) + ")"; return false; }
    cl_int err = 0;
    if (rxe1_ && rx_.memory_free()) {   // schema 5 (section 11): no dataset, no cache; the harness epoch seed as E; a dummy buffer for the kernel argument
        int st3 = 0; uint64_t epn = ignpow_epoch_of(algo_.c_str(), params_c(), height, &st3); rxe1_harness_epoch_seed(rx_.chain_id, epn, E_out);
        if (light_out) { clReleaseMemObject(light_out); light_out = nullptr; }
        dataset = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 4096, nullptr, &err); bytes = 0;
        if (!dataset) { r.code = "memory"; r.explain = "the dummy dataset buffer failed"; return false; }
        return true;
    }
    if (ih_.version == 16 || ih_.version == 17) {   // VERSION 16: no dataset and no cache (the static fabric); VERSION 17: the texture in place of a dataset; a dummy buffer for the kernel argument; E from the context for the record
        if (ih_.version == 17 && !r17_tex_) {   // the static 4096 x 4096 R32 uint texture (texel (x, y) = fmix32(y * 4096 + x)), 64 MiB, host-filled once
            cl_bool img_ok = CL_FALSE; clGetDeviceInfo(info_.device, CL_DEVICE_IMAGE_SUPPORT, sizeof img_ok, &img_ok, nullptr);
            if (!img_ok) { r.code = "driver"; r.explain = "version 17 needs image2d support and this runtime reports none"; return false; }
            std::vector<uint32_t> tex = tp_texture();
            cl_image_format fmt; fmt.image_channel_order = CL_R; fmt.image_channel_data_type = CL_UNSIGNED_INT32;
            cl_image_desc desc; std::memset(&desc, 0, sizeof desc); desc.image_type = CL_MEM_OBJECT_IMAGE2D; desc.image_width = kTpWidth; desc.image_height = kTpHeight;
            cl_int ie = 0; r17_tex_ = clCreateImage(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, &fmt, &desc, tex.data(), &ie);
            if (!r17_tex_) { r.code = "memory"; r.explain = "the version 17 texture (64 MiB R32_UINT image2d) was refused by the runtime (" + std::to_string(ie) + ")"; return false; }
        }
        uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(ctx, E_out, D, cd, dg, &bms, &hd) != IGNPOW_OK) std::memset(E_out, 0, 32);
        if (light_out) { clReleaseMemObject(light_out); light_out = nullptr; }
        dataset = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 4096, nullptr, &err); bytes = 0;
        if (!dataset) { r.code = "memory"; r.explain = "the dummy dataset buffer failed"; return false; }
        return true;
    }
    const void *e = nullptr; uint64_t eb = 0; uint32_t ib = 0;
    if (ignpow_ctx_light_cache(ctx, &e, &eb, &ib) != IGNPOW_OK || !e) { r.code = "unsupported"; r.explain = "reference exposes no light cache"; return false; }
    if (light_out) { clReleaseMemObject(light_out); light_out = nullptr; }
    if (ih_.version == 0) {
        if (eb != 32) { r.code = "unsupported"; r.explain = "version 0 expects the 32-byte epoch seed as the light cache"; return false; }
        std::memcpy(E_out, e, 32);
    } else {
        if (eb != ih_.cache_bytes || ib != 128) { r.code = "unsupported"; r.explain = "version 1 expects a " + mb(ih_.cache_bytes) + " cache of 128-byte items as the light cache, got " + std::to_string(eb) + " bytes of " + std::to_string(ib); return false; }
        if (rxe1_) { int st3 = 0; uint64_t epn = ignpow_epoch_of(algo_.c_str(), params_c(), height, &st3); rxe1_harness_epoch_seed(rx_.chain_id, epn, E_out); }
        else { uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0;
        if (ignh_dataset_info(ctx, E_out, D, cd, dg, &bms, &hd) != IGNPOW_OK) { r.code = "unsupported"; r.explain = "reference exposes no epoch seed E (ignh_dataset_info)"; return false; } }
        light_out = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, eb, (void *)e, &err);
        if (!light_out || err != CL_SUCCESS) { r.code = "memory"; r.explain = "cache allocation of " + mb(eb) + " failed (" + std::to_string(err) + ")"; r.need_bytes = eb; return false; }
        allocated_ += eb;
    }
    bytes = ih_.dataset_bytes;
    uint64_t ref_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st == IGNPOW_OK && ref_bytes != bytes) { r.code = "unsupported"; r.explain = "dataset size disagrees with the reference: " + std::to_string(bytes) + " vs " + std::to_string(ref_bytes); return false; }
    // T-B8 (landed on the 9070 XT): one buffer above 2 GiB loses 17 percent on this driver, two buffers of half the size lose nothing.
    // The split also when one buffer would exceed the device's largest allocation (rusticl on the RX 6600 caps an allocation at 2047 MB,
    // one MB under the 2 GiB dataset: the rig's first row refused every vector class "exceeds the device's largest allocation",
    // gb-rig-row__rx6600-8g__wall-1, 19:26Z); the halves are checked against the cap below, so the refusal names what cannot fit at all
    const unsigned pieces = split_pieces(bytes); const bool split2 = pieces > 1;   // the same predicate as the kernel's compile (13 Oct 2026): a dataset split at the allocation was read by a kernel built without IGM_IH_SPLIT2 when the size was exactly 2 GiB on a device whose largest allocation is smaller (rusticl), and the host's second-buffer argument then landed on the kernel's shared-table parameter (every version 13 class wrong on the 9060 XT)
    if (pieces == 4 && ih_.version != 15) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " needs four pieces on this device (largest allocation " + mb(info_.max_alloc) + "), a form only version 15 reads"; r.need_bytes = bytes; return false; }
    if (bytes / pieces > info_.max_alloc) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + (split2 ? " in two halves" : "") + " exceeds the device's largest allocation " + mb(info_.max_alloc); r.need_bytes = bytes; return false; }
    const uint32_t copies = (cand_->kind == Kind::igneumhash && !split2) ? knobs_.copies : 1;   // the channel-coloured allocation: copies x bytes in one buffer (never with the split)
    const uint64_t shift = (cand_->kind == Kind::igneumhash && !split2) ? knobs_.shift_bytes : 0;   // the physical permutation: the dataset starts shift bytes into a larger buffer (a sub-buffer at that origin)
    if (!dataset) {
        if (copies > 1 && (uint64_t)bytes * copies > info_.max_alloc) { r.code = "memory"; r.explain = std::to_string(copies) + " copies of " + mb(bytes) + " exceed the device's largest allocation " + mb(info_.max_alloc); r.need_bytes = bytes * copies; return false; }
        const uint64_t want = split2 ? bytes / pieces : (uint64_t)bytes * copies + shift;
        cl_mem base = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, want, nullptr, &err);
        if (!base || err != CL_SUCCESS) { r.code = "memory"; r.explain = "dataset allocation of " + mb(want) + " failed (" + std::to_string(err) + ")"; r.need_bytes = want; return false; }
        allocated_ += want;
        if (shift) {   // the sub-buffer at the shifted origin (the origin on the device's base alignment, else a refusal: the row)
            cl_uint align_bits = 0; clGetDeviceInfo(info_.device, CL_DEVICE_MEM_BASE_ADDR_ALIGN, sizeof align_bits, &align_bits, nullptr);
            const size_t align = align_bits ? align_bits / 8 : 4096;
            if (shift % align) { clReleaseMemObject(base); r.code = "unsupported"; r.explain = "shift_bytes " + std::to_string(shift) + " is not a multiple of the device's base alignment " + std::to_string(align); return false; }
            cl_buffer_region reg{(size_t)shift, (size_t)bytes * copies};
            dataset = clCreateSubBuffer(base, CL_MEM_READ_WRITE, CL_BUFFER_CREATE_TYPE_REGION, &reg, &err);
            if (!dataset || err != CL_SUCCESS) { clReleaseMemObject(base); r.code = "driver"; r.explain = "the shifted sub-buffer failed (" + std::to_string(err) + ")"; return false; }
            dataset_base_ = base;   // released with the dataset
        } else dataset = base;
        if (pieces == 4) { dataset3_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes / 4, nullptr, &err); if (dataset3_ && err == CL_SUCCESS) dataset4_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes / 4, nullptr, &err); if (!dataset3_ || !dataset4_ || err != CL_SUCCESS) { r.code = "memory"; r.explain = "the third or fourth quarter of the dataset failed (" + std::to_string(err) + ")"; return false; } allocated_ += bytes / 2; }
        if (split2) { dataset2_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes / pieces, nullptr, &err); if (!dataset2_ || err != CL_SUCCESS) { r.code = "memory"; r.explain = "the second half of the dataset failed (" + std::to_string(err) + ")"; return false; } allocated_ += bytes / 2; }
    }
    IhDatasetBatch b{0, 0, 0, 0, ih_consts_of(ih_, ih_P_, E_out)};
    cl_mem bb = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, sizeof b, nullptr, &err);
    uint32_t items = ih_.n_items(); const uint32_t per = 1u << 18;
    const bool build8 = knobs_.variant == "build8" && ih_.version >= 1;
    cl_mem gatherbuf = nullptr;
    if (build8) { gatherbuf = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)per * 128 + 4096, nullptr, &err); if (!gatherbuf) { clReleaseMemObject(bb); r.code = "memory"; r.explain = "gather area for the eight-lane build failed"; return false; } }
    double t0 = now_ms();
    for (uint32_t first = 0; first < items; first += b.count) {
        b.first = first; b.count = items - first < per ? items - first : per;
        const uint32_t piece_items = split2 ? items / pieces : items, piece = split2 ? first / piece_items : 0;
        if (split2 && first + b.count > (piece + 1) * piece_items) b.count = (piece + 1) * piece_items - first;   // a batch never straddles a piece: the destination buffer is chosen per batch
        b.pad0 = piece * piece_items;   // the destination index offset: piece p writes into its buffer from item 0
        clEnqueueWriteBuffer(queue_, bb, CL_TRUE, 0, sizeof b, &b, 0, nullptr, nullptr);
        cl_mem cachearg = light_out ? light_out : dataset;
        cl_mem dst = piece == 0 ? dataset : piece == 1 ? dataset2_ : piece == 2 ? dataset3_ : dataset4_;
        cl_int a0 = clSetKernelArg(dag_k_, 0, sizeof(cl_mem), &bb), a1 = clSetKernelArg(dag_k_, 1, sizeof(cl_mem), &dst), a2 = clSetKernelArg(dag_k_, 2, sizeof(cl_mem), &cachearg);
        if (a0 != CL_SUCCESS || a1 != CL_SUCCESS || a2 != CL_SUCCESS) { clReleaseMemObject(bb); r.code = "driver"; r.explain = "igneumhash dataset kernel args refused (" + std::to_string(a0) + ", " + std::to_string(a1) + ")"; return false; }
        size_t global = b.count; size_t local8 = knobs_.threadgroup * wave_;
        if (build8) { global = ((size_t)b.count * 8 + local8 - 1) / local8 * local8; clSetKernelArg(dag_k_, 3, sizeof(cl_mem), &gatherbuf); clSetKernelArg(dag_k_, 4, local8 * 4, nullptr); }
        cl_command_queue q = async ? build_queue_ : queue_;
        cl_mem bbatch = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, sizeof b, &b, &err);   // one constant per batch: no host wait between batches
        clSetKernelArg(dag_k_, 0, sizeof(cl_mem), &bbatch);
        cl_event ev = nullptr;
        cl_int e2 = clEnqueueNDRangeKernel(q, dag_k_, 1, nullptr, &global, build8 ? &local8 : nullptr, 0, nullptr, &ev);
        clReleaseMemObject(bbatch);
        if (e2 != CL_SUCCESS) { clReleaseMemObject(bb); r.code = "driver"; r.explain = "igneumhash dataset kernel enqueue failed (" + std::to_string(e2) + ")"; return false; }
        if (async) { if (build_done_) clReleaseEvent(build_done_); build_done_ = ev; clFlush(q); }
        else { clWaitForEvents(1, &ev); clReleaseEvent(ev); }
    }
    if (copies > 1) {   // the other copies by device copy from copy 0 (the same bytes, a different physical placement)
        cl_command_queue q = async ? build_queue_ : queue_;
        for (uint32_t c = 1; c < copies; c++) { cl_int ec = clEnqueueCopyBuffer(q, dataset, dataset, 0, (size_t)bytes * c, (size_t)bytes, 0, nullptr, nullptr); if (ec != CL_SUCCESS) { clReleaseMemObject(bb); r.code = "driver"; r.explain = "the dataset copy " + std::to_string(c) + " failed (" + std::to_string(ec) + ")"; return false; } }
        if (!async) clFinish(q);
    }
    clReleaseMemObject(bb); if (gatherbuf) { if (async) clFinish(build_queue_); clReleaseMemObject(gatherbuf); }
    last_table_ms_ = now_ms() - t0;
    return true;
}

bool ClEngineX::build_fishhash_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, Refusal &r) {
    int st = 0; cl_int err = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a light context (status " + std::to_string(st) + "); FishHash needs the reference built with IGNPOW_HAVE_FISHHASH"; return false; }
    bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    const void *light = nullptr; uint64_t light_bytes = 0; uint32_t item = 0;
    if (ignpow_ctx_light_cache(ctx, &light, &light_bytes, &item) != IGNPOW_OK || !light || light_bytes == 0) { r.code = "unsupported"; r.explain = "reference exposes no FishHash cache"; return false; }
    if (light_bytes / 64 != 1179641u) { r.code = "unsupported"; r.explain = "FishHash cache item count differs from the pinned 1179641"; return false; }
    if (bytes > info_.max_alloc) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " exceeds the device's largest allocation " + mb(info_.max_alloc); r.need_bytes = bytes; r.have_bytes = info_.max_alloc; return false; }
    light_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, light_bytes, const_cast<void *>(light), &err);
    if (err != CL_SUCCESS) { r.code = "memory"; r.explain = "cache allocation failed"; return false; }
    dataset = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes, nullptr, &err);
    if (err != CL_SUCCESS) { r.code = "memory"; r.explain = "dataset allocation of " + mb(bytes) + " failed (" + std::to_string(err) + ")"; r.need_bytes = bytes; return false; }
    allocated_ += light_bytes + bytes;
    uint32_t items = (uint32_t)(bytes / 64);
    const uint32_t per = 1u << 20;
    for (uint32_t first = 0; first < items; first += per) {
        uint32_t count = items - first < per ? items - first : per;
        clSetKernelArg(dag_k_, 0, sizeof first, &first); clSetKernelArg(dag_k_, 1, sizeof count, &count);
        clSetKernelArg(dag_k_, 2, sizeof light_, &light_); clSetKernelArg(dag_k_, 3, sizeof dataset, &dataset);
        size_t global = ((count + 255) / 256) * 256, local = 256;
        if (clEnqueueNDRangeKernel(queue_, dag_k_, 1, nullptr, &global, &local, 0, nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel enqueue failed"; return false; }
        clFinish(queue_);
    }
    return true;
}

bool ClEngineX::build_autolykos_table(uint64_t height, cl_mem &table, uint64_t &bytes, double &ms, Refusal &r, const std::function<bool()> *stop) {
    cl_int err = 0;
    uint32_t N = autolykos_n(height);
    bytes = (uint64_t)N * 32;
    if (bytes > info_.max_alloc) { r.code = "memory"; r.explain = "table " + mb(bytes) + " exceeds the device's largest allocation"; r.need_bytes = bytes; return false; }
    table = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes, nullptr, &err);
    if (err != CL_SUCCESS) { r.code = "memory"; r.explain = "table allocation of " + mb(bytes) + " failed"; r.need_bytes = bytes; return false; }
    allocated_ += bytes;
    double t0 = now_ms();
    const uint32_t per = 1u << 18;
    uint32_t h32 = (uint32_t)height;
    for (uint32_t first = 0; first < N; first += per) {
        if (stop && (*stop)()) { r.code = "cancelled"; r.explain = "table build cancelled at element " + std::to_string(first); clReleaseMemObject(table); table = nullptr; allocated_ -= bytes; return false; }
        if (next_dataset_ && next_height_ != height) warm_step(per);
        uint32_t count = N - first < per ? N - first : per;
        clSetKernelArg(table_k_, 0, sizeof first, &first); clSetKernelArg(table_k_, 1, sizeof count, &count);
        clSetKernelArg(table_k_, 2, sizeof h32, &h32); clSetKernelArg(table_k_, 3, sizeof table, &table);
        size_t global = ((count + 255) / 256) * 256, local = 256;
        if (clEnqueueNDRangeKernel(queue_, table_k_, 1, nullptr, &global, &local, 0, nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "table kernel enqueue failed"; return false; }
        clFinish(queue_);
    }
    ms = now_ms() - t0; last_table_ms_ = ms;
    return true;
}


// The warm build of the next height's table beside the current one (see the Metal engine).
void ClEngineX::warm_start(uint64_t height) {
    if (cand_->kind != Kind::autolykos || next_height_ == height) return;
    if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; }
    next_dataset_ = nullptr; next_dataset_bytes_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0; if (next_ev_) { clReleaseEvent(next_ev_); next_ev_ = nullptr; }
    uint64_t next_bytes = (uint64_t)autolykos_n(height) * 32;
    uint64_t budget = free_memory_estimate(); if (budget > (256ull << 20)) budget -= (256ull << 20); else budget = 0;
    if (next_bytes + (64ull << 20) > budget) { static bool said = false; if (!said) { fprintf(stderr, "opencl: warm table for height %llu does not fit (budget %s); transitions will be cold\n", (unsigned long long)height, mb(budget).c_str()); said = true; } return; }
    cl_int err = 0;
    cl_mem t = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, next_bytes, nullptr, &err);
    if (err != CL_SUCCESS) return;
    allocated_ += next_bytes;
    next_dataset_ = t; next_dataset_bytes_ = next_bytes; next_height_ = height; next_cursor_ = 0; next_n_ = autolykos_n(height);
}

void ClEngineX::warm_step(uint32_t elements) {
    if (!next_dataset_ || next_cursor_ >= next_n_) return;
    uint32_t count = next_n_ - next_cursor_ < elements ? next_n_ - next_cursor_ : elements;
    uint32_t first = next_cursor_, h32 = (uint32_t)next_height_;
    clSetKernelArg(table_k_, 0, sizeof first, &first); clSetKernelArg(table_k_, 1, sizeof count, &count);
    clSetKernelArg(table_k_, 2, sizeof h32, &h32); clSetKernelArg(table_k_, 3, sizeof next_dataset_, &next_dataset_);
    size_t global = ((count + 255) / 256) * 256, local = 256;
    if (next_ev_) { clReleaseEvent(next_ev_); next_ev_ = nullptr; }
    if (build_done_) { clFinish(build_queue_); clReleaseEvent(build_done_); build_done_ = nullptr; }
    if (clEnqueueNDRangeKernel(queue_, table_k_, 1, nullptr, &global, &local, 0, nullptr, &next_ev_) != CL_SUCCESS) return;
    clFlush(queue_);
    next_cursor_ += count;
}

void ClEngineX::warm_finish() {
    if (!next_dataset_) return;
    double t0 = now_ms(); uint32_t before = next_cursor_;
    while (next_cursor_ < next_n_) warm_step(1u << 18);
    if (next_ev_) { clWaitForEvents(1, &next_ev_); clReleaseEvent(next_ev_); next_ev_ = nullptr; }
    if (before < next_n_) { last_table_ms_ = now_ms() - t0; fprintf(stderr, "opencl: warm table for height %llu finished synchronously: %u of %u elements, %.0f ms of transition downtime\n", (unsigned long long)next_height_, next_n_ - before, next_n_, last_table_ms_); }
    else last_table_ms_ = 0;
}

bool ClEngineX::open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) {
    if (cand_ && cand_->kind == Kind::igneumhash && !rxe1_ && algo_ == algo && knobs_.variant == k.variant && ih_next_ctx_ && !ih_next_params_.empty() && k.params == ih_next_params_ && dataset_ && search_k_ && dag_k_) {
        // the warm parameter change: the configure names the document the next dataset was prepared for (prepare.params);
        // the prepared epoch must be this height's, else the change is cold below
        int st = 0; uint64_t ep = ignpow_epoch_of(algo.c_str(), k.params.c_str(), height, &st);
        if (st == IGNPOW_OK && ep == ignpow_ctx_epoch(ih_next_ctx_)) {
            double w0 = now_ms(); ih_warm_finish(); last_swap_wait_ms_ = now_ms() - w0;
            const bool was_split = dataset2_ != nullptr, want_split = k.variant == "split2" || k.alloc == "split2" || (k.variant != "onebuf" && k.alloc != "onebuf" && (ih_next_ih_.dataset_bytes > (2ull << 30) || ih_next_ih_.dataset_bytes > info_.max_alloc));
            const uint32_t old_scratch = ih_.scratch_bytes, old_st = ih_.st_words();
            if (ctx_) ignpow_ctx_free(ctx_);
            if (dataset_) { clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_; }
            if (dataset2_) { clReleaseMemObject(dataset2_); dataset2_ = nullptr; }
            if (dataset3_) { clReleaseMemObject(dataset3_); dataset3_ = nullptr; } if (dataset4_) { clReleaseMemObject(dataset4_); dataset4_ = nullptr; }
            if (light_) clReleaseMemObject(light_);
            ctx_ = ih_next_ctx_; ih_next_ctx_ = nullptr; dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0;
            light_ = ih_next_light_; ih_next_light_ = nullptr; std::memcpy(ih_E_, ih_next_E_, 32);
            ih_ = ih_next_ih_; std::memcpy(ih_P_, ih_next_P_, 32); params_ = k.params; take_knobs(k); height_ = height; next_height_ = ~0ull; ih_next_params_.clear();
            if (ih_.scratch_bytes != old_scratch || ih_.st_words() != old_st) {   // the scratch and state areas follow the document (the state area the width too)
                ih_slots_ = (uint32_t)((256ull << 20) / (ih_eff_scratch() ? ih_eff_scratch() : 640u));
                uint64_t scratch_bytes = (uint64_t)ih_slots_ * ih_eff_scratch(), st_bytes = (uint64_t)ih_slots_ * 4 * ih_.st_words();   // 640 bytes at the 16-register state
                if (ih_scratch_) clReleaseMemObject(ih_scratch_); if (ih_st_) clReleaseMemObject(ih_st_);
                cl_int err = 0;
                ih_scratch_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, scratch_bytes ? scratch_bytes : 4096, nullptr, &err);
                ih_st_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, st_bytes, nullptr, &err);
                if (!ih_scratch_ || !ih_st_) { r.code = "memory"; r.explain = "scratch allocation of " + mb(scratch_bytes + st_bytes) + " failed"; close(); return false; }
            }
            if (was_split != want_split || ih_.version * 1024u + ih_.item_words() + ih_.rw() * 65536u + r0d_key(ih_, knobs_, r0d_height_) != ih_built_version_) {   // the state width is a define too   // the program's allocation form follows the dataset size and its round the document's version: rebuild the kernels (the dataset stays)
                for (cl_kernel *kk : { &search_k_, &dag_k_ }) if (*kk) { clReleaseKernel(*kk); *kk = nullptr; }
                if (prog_) { clReleaseProgram(prog_); prog_ = nullptr; }
                if (!build_programs(r)) { close(); return false; }
                if (want_split) fprintf(stderr, "opencl: the prepared dataset is one buffer above 2 GiB; the split form applies from the next build\n");
            }
            fprintf(stderr, "opencl: parameter change adopted warm (the dataset prepared for the new document; swap wait %.0f ms)\n", last_swap_wait_ms_);
            return true;
        }
    }
    if (cand_ && (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) && algo_ == algo && params_ == k.params && knobs_.variant == k.variant && dataset_ && search_k_ && dag_k_) {   // the same variant: rebuild in place
        take_knobs(k); height_ = height;
        if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
        if (ih_next_ctx_) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; }
        if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0; }
        if (ih_next_light_) { clReleaseMemObject(ih_next_light_); ih_next_light_ = nullptr; }
        next_height_ = ~0ull;
        if (!ih_build_dataset(height, ctx_, dataset_, dataset_bytes_, ih_E_, light_, r)) { close(); return false; }
        return true;
    }
    close();
    algo_ = algo; take_knobs(k); params_ = k.params; height_ = height;
    cand_ = find_candidate(algo.c_str());
    if (!cand_ || cand_->lane_model != LaneModel::independent) { r.code = "unsupported"; r.explain = "not an independent-lane candidate: " + algo; return false; }
    int st = 0;
    rxe1_ = cand_->kind == Kind::rxe1;
    if (rxe1_) {   // IH 06: the RXE1 record; the dataset is the R0A v1 cache-to-dataset build at the record's sizes
        std::string perr;
        if (!rxe1_parse_params(params_, rx_, perr)) { r.code = "unsupported"; r.explain = "params: " + perr; return false; }
        
        rxe1_pid(rx_, rx_pid_);
        ih_ = IhParams(); ih_.version = 1; ih_.dataset_bytes = rx_.dataset_bytes; ih_.scratch_bytes = 0; ih_.cache_bytes = rx_.cache_bytes; ih_.parents = rx_.parents; ih_.rounds = rx_.rounds; ih_.epoch_blocks = rx_.epoch_blocks; ih_.chain_id = rx_.chain_id;
        std::memset(ih_P_, 0, 32);
    }
    if (cand_->kind == Kind::igneumhash || rxe1_) {
        std::string perr;
        if (!rxe1_ && !ih_parse_params(params_, ih_, perr)) { r.code = "unsupported"; r.explain = "params: " + perr; return false; }
        if (!rxe1_ && knobs_.variant == "swizzle" && (knobs_.alloc == "split2" || (knobs_.alloc != "onebuf" && ih_.dataset_bytes > (2ull << 30)))) { r.code = "unsupported"; r.explain = "the swizzle needs one buffer (its permutation crosses the split form's two): --knob alloc=onebuf"; return false; }
        if (!rxe1_ && ih_.version >= 3) {   // version 3 (the item width): the stock eight-lane kernel and the one-thread build serve it; the other forms refuse by name
            const std::string &v = knobs_.variant;
            if (ih_x2_ && (ih_.item_bytes != 512 || !ih_wave_ || ih_.version != 4 || ih_.scratch_bytes != 0)) { r.code = "unsupported"; r.explain = "the two-chain form takes version 4 at the 512-byte item with scratch 0 on the wave form (form1-wave...-x2)"; return false; }
            if (ih_span_ && (ih_.item_bytes != 512 || !ih_wave_)) { r.code = "unsupported"; r.explain = "the span form takes the 512-byte item on the wave form (form1-wave...-span)"; return false; }
            if (bw_loader_ == 4 && ih_.item_bytes != 512) { r.code = "unsupported"; r.explain = "Breakwater loader 4 (wave64) takes the 512-byte macro-item only (item_bytes " + std::to_string(ih_.item_bytes) + ")"; return false; }
            if (bw_loader_ && ih_.item_bytes != 512 && ih_.item_bytes != 256 && ih_.item_bytes != 128) { r.code = "unsupported"; r.explain = "the one-wave family takes item_bytes 512, 256 or 128 (16 bytes per lane: 32, 16 or 8 lanes per nonce); the document says " + std::to_string(ih_.item_bytes); return false; }
            if (bw_loader_ && bw_red_ > 3) { r.code = "unsupported"; r.explain = "Breakwater reduction r" + std::to_string(bw_red_) + " does not exist (r1 the control, r2 version 4, r3 version 6)"; return false; }
            if (bw_loader_ && bw_red_ == 2 && ih_.version != 4) { r.code = "unsupported"; r.explain = "Breakwater r2 is version 4's reduction: the document is version " + std::to_string(ih_.version); return false; }
            if (bw_loader_ && bw_red_ == 3 && ih_.version != 6) { r.code = "unsupported"; r.explain = "Breakwater r3 is version 6's reduction (the keyed leaves and the finalised root): the document is version " + std::to_string(ih_.version); return false; }
            if (bw_loader_ == 7 && ih_.dataset_bytes > (2ull << 30) && knobs_.alloc != "onebuf") { r.code = "unsupported"; r.explain = "Breakwater loader 7 (the buffer resource) addresses one buffer: above 2 GiB it takes --knob alloc=onebuf"; return false; }
            if (bw_loader_ && bw_red_ && (bw_loader_ == 4 || ih_.scratch_bytes != 0)) { r.code = "unsupported"; r.explain = "the wave layout of the relation takes a wave32 loader (not loader 4) and scratch 0"; return false; }
            if ((knobs_.copies > 1 || knobs_.shift_bytes) && (knobs_.alloc == "split2" || (knobs_.alloc != "onebuf" && ih_.dataset_bytes > (2ull << 30)))) { r.code = "unsupported"; r.explain = "the channel-coloured and shifted allocations take one buffer: the document at or under 2 GiB (or --knob alloc=onebuf)"; return false; }
            if (v == "n1" || v == "ctx2" || v == "ctx4" || v == "bucket" || v == "build8" || v == "diag-a" || v == "diag-b" || v == "reach-half") { r.code = "unsupported"; r.explain = "variant " + v + " is not implemented for version 3 (item_bytes); the stock kernel serves it"; return false; }
        }
        if (!rxe1_) ih_P(ih_, ih_P_);
        ih_slots_ = (uint32_t)((256ull << 20) / (ih_eff_scratch() ? ih_eff_scratch() : 640u));
        uint64_t scratch_bytes = (uint64_t)ih_slots_ * ih_eff_scratch(), st_bytes = (uint64_t)ih_slots_ * (rxe1_ ? 2048 : 4 * ih_.st_words());
        uint64_t need = ih_.dataset_bytes + (ih_.version >= 1 ? ih_.cache_bytes : 0) + scratch_bytes + st_bytes + (64ull << 20);
        auto budget_now = [&]() { uint64_t b = free_memory_estimate(); if (b > (256ull << 20)) b -= (256ull << 20); else b = 0; if (supervisor_budget && supervisor_budget < b) b = supervisor_budget; if (k.working_set_mb && (k.working_set_mb << 20) < b) b = k.working_set_mb << 20; return b; };
        uint64_t budget = budget_now();
        if (need > budget) { r.code = "memory"; r.explain = "unsafe allocation refused: need " + mb(need) + ", budget " + mb(budget) + " after the 256 MiB margin"; r.need_bytes = need; r.have_bytes = budget; return false; }
        if (!build_programs(r)) { close(); return false; }
        if (!ih_build_dataset(height, ctx_, dataset_, dataset_bytes_, ih_E_, light_, r)) { close(); return false; }
        cl_int err = 0;
        ih_scratch_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, scratch_bytes ? scratch_bytes : 4096, nullptr, &err);
        ih_st_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, st_bytes, nullptr, &err);
        if (!ih_scratch_ || !ih_st_) { r.code = "memory"; r.explain = "scratch allocation of " + mb(scratch_bytes + st_bytes) + " failed"; close(); return false; }
        allocated_ += scratch_bytes + st_bytes;
        for (uint32_t i = 0; i < knobs_.inflight; i++) {
            job_buf_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, sizeof(IhJob) > sizeof(RxJob) ? sizeof(IhJob) : sizeof(RxJob), nullptr, &err);
            results_[i] = clCreateBuffer(ctxcl_, CL_MEM_WRITE_ONLY, max_results_ * sizeof(IhResult), nullptr, &err);
            count_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, 4, nullptr, &err);
        }
        allocated_ += knobs_.inflight * (sizeof(IhJob) + max_results_ * sizeof(IhResult) + 4);
        if (is_bucket() && !rxe1_) {   // T-B14: the pipeline's state, requests, buckets, order and item slots for one batch
            ihb_batch_ = knobs_.bucket_batch < ih_slots_ ? knobs_.bucket_batch : ih_slots_;
            const uint32_t nb = knobs_.bucket_bits ? (1u << knobs_.bucket_bits) : 1u;
            uint64_t bb = (uint64_t)ihb_batch_ * (640 + 4 + 4 + 128) + (uint64_t)nb * 8 + 256;
            ihb_state_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)ihb_batch_ * 640, nullptr, &err);
            ihb_req_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)ihb_batch_ * 4, nullptr, &err);
            ihb_hist_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)nb * 4, nullptr, &err);
            ihb_cursor_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)nb * 4, nullptr, &err);
            ihb_order_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)ihb_batch_ * 4, nullptr, &err);
            ihb_items_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, (size_t)ihb_batch_ * 128, nullptr, &err);
            ihb_job_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 256, nullptr, &err);
            if (!ihb_state_ || !ihb_req_ || !ihb_hist_ || !ihb_cursor_ || !ihb_order_ || !ihb_items_ || !ihb_job_) { r.code = "memory"; r.explain = "bucket pipeline allocation of " + mb(bb) + " failed"; close(); return false; }
            allocated_ += bb;
        }
        return true;
    }
    uint64_t bytes = cand_->kind == Kind::autolykos ? (uint64_t)autolykos_n(height) * 32 : ignpow_dataset_bytes(algo.c_str(), params_c(), height, &st);
    uint64_t cache = cand_->kind == Kind::autolykos ? 0 : ignpow_cache_bytes(algo.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference: height outside the schedule"; return false; }
    uint64_t need = bytes + cache + (32ull << 20);
    auto budget_now = [&]() { uint64_t b = free_memory_estimate(); if (b > (256ull << 20)) b -= (256ull << 20); else b = 0; if (supervisor_budget && supervisor_budget < b) b = supervisor_budget; if (k.working_set_mb && (k.working_set_mb << 20) < b) b = k.working_set_mb << 20; return b; };
    uint64_t budget = budget_now();
    for (unsigned waited = 0; need > budget && waited < 3000; waited += 100) { std::this_thread::sleep_for(std::chrono::milliseconds(100)); budget = budget_now(); }
    if (need > budget) { r.code = "memory"; r.explain = "unsafe allocation refused: need " + mb(need) + ", budget " + mb(budget) + " after the 256 MiB margin"; r.need_bytes = need; r.have_bytes = budget; return false; }
    if (!build_programs(r)) { close(); return false; }
    double ms = 0;
    bool ok = cand_->kind == Kind::fishhash ? build_fishhash_dataset(height, ctx_, dataset_, dataset_bytes_, r) : build_autolykos_table(height, dataset_, dataset_bytes_, ms, r);
    if (!ok) { close(); return false; }
    cl_int err = 0;
    for (int i = 0; i < 2; i++) {
        job_buf_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, sizeof(AlJob), nullptr, &err);
        results_[i] = clCreateBuffer(ctxcl_, CL_MEM_WRITE_ONLY, max_results_ * sizeof(AlResult), nullptr, &err);
        count_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, 4, nullptr, &err);
    }
    allocated_ += 2 * (sizeof(AlJob) + max_results_ * sizeof(AlResult) + 4);
    return true;
}

bool ClEngineX::prepare(uint64_t height, Refusal &r, const std::string &params) {
    // a parameter change announced ahead (the product team, 10 Oct 2026): the next dataset is built for that document and kept
    // across the configure that names it; until then the resident document and kernels are untouched
    const bool change = !params.empty() && params != params_;
    IhParams np = ih_; uint8_t nP[32]; std::memcpy(nP, ih_P_, 32);
    if (change) {
        if (rxe1_ || cand_->kind != Kind::igneumhash) { r.code = "unsupported"; r.explain = "a parameter change ahead is an IgneumHash feature"; return false; }
        std::string perr; if (!ih_parse_params(params, np, perr)) { r.code = "unsupported"; r.explain = "next params: " + perr; return false; }
        ih_P(np, nP);
    }
    const char *npc = change ? params.c_str() : params_c();
    if (cand_->kind == Kind::fishhash) { r.code = "unsupported"; r.explain = "FishHash has one dataset for ever: nothing to prepare"; return false; }
    if (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) {   // the warm next epoch: its cache and dataset beside the resident ones
        int st = 0;
        uint64_t ep = ignpow_epoch_of(algo_.c_str(), npc, height, &st);
        if (st != IGNPOW_OK) { r.code = "unsupported"; r.explain = "reference: height outside the schedule"; return false; }
        if (ctx_ && !change && ep == ignpow_ctx_epoch(ctx_)) { r.code = "unsupported"; r.explain = "that epoch is the resident one"; return false; }
        uint64_t next_bytes = np.dataset_bytes + (np.version >= 1 ? np.cache_bytes : 0);
        uint64_t budget = free_memory_estimate(); if (budget > (256ull << 20)) budget -= (256ull << 20); else budget = 0;
        if (next_bytes + (64ull << 20) > budget) { r.code = "memory"; r.explain = "cold transition: next epoch " + mb(next_bytes) + " does not fit beside the resident one (budget " + mb(budget) + ")"; r.need_bytes = next_bytes; r.have_bytes = budget; return false; }
        // the next epoch's context, cache and E now; its dataset in slices on the mining queue from here (ih_warm_step), so the
        // mining keeps its rate while the build runs and the swap pays only the slices left (the founder's epoch-overlap row)
        int st2 = 0; ignpow_ctx *nc = ignpow_ctx_create(algo_.c_str(), npc, height, 0, &st2);
        if (!nc) { r.code = "unsupported"; r.explain = "reference refused a context (status " + std::to_string(st2) + ")"; return false; }
        const void *e = nullptr; uint64_t eb = 0; uint32_t ib = 0; cl_int err = 0;
        if (ignpow_ctx_light_cache(nc, &e, &eb, &ib) != IGNPOW_OK || !e) { ignpow_ctx_free(nc); r.code = "unsupported"; r.explain = "reference exposes no light cache"; return false; }
        uint8_t ne[32]; cl_mem nl = nullptr;
        if (np.version == 0) std::memcpy(ne, e, 32);
        else {
            if (rxe1_) { uint64_t epn = ignpow_epoch_of(algo_.c_str(), npc, height, &st2); rxe1_harness_epoch_seed(rx_.chain_id, epn, ne); }
            else { uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(nc, ne, D, cd, dg, &bms, &hd) != IGNPOW_OK) { ignpow_ctx_free(nc); r.code = "unsupported"; r.explain = "no epoch seed E"; return false; } }
            nl = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, eb, (void *)e, &err);
            if (!nl) { ignpow_ctx_free(nc); r.code = "memory"; r.explain = "the next cache failed"; return false; }
            allocated_ += eb;
        }
        const bool split2 = split_form(ih_.dataset_bytes);
        (void)split2;   // the next dataset is one buffer in this first form; the split form of the resident dataset is unchanged
        cl_mem nd = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, np.dataset_bytes, nullptr, &err);
        if (!nd || err != CL_SUCCESS) { ignpow_ctx_free(nc); if (nl) clReleaseMemObject(nl); r.code = "memory"; r.explain = "the next dataset allocation of " + mb(np.dataset_bytes) + " failed"; return false; }
        allocated_ += np.dataset_bytes;
        if (ih_next_ctx_) ignpow_ctx_free(ih_next_ctx_);
        if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; }
        if (ih_next_light_) clReleaseMemObject(ih_next_light_);
        ih_next_ctx_ = nc; next_dataset_ = nd; next_dataset_bytes_ = np.dataset_bytes; ih_next_light_ = nl; std::memcpy(ih_next_E_, ne, 32); next_height_ = height;
        ih_next_cursor_ = 0; ih_next_items_ = np.n_items();
        ih_next_ih_ = np; std::memcpy(ih_next_P_, nP, 32); ih_next_params_ = change ? params : std::string();
        IhDatasetBatch b{0, 0, 0, 0, ih_consts_of(ih_next_ih_, ih_next_P_, ih_next_E_)};
        if (ih_next_batchbuf_) clReleaseMemObject(ih_next_batchbuf_);
        ih_next_batchbuf_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, sizeof b, &b, &err);
        last_table_ms_ = 0;
        return true;
    }
    uint64_t next_bytes = (uint64_t)autolykos_n(height) * 32;
    uint64_t budget = free_memory_estimate(); if (budget > (256ull << 20)) budget -= (256ull << 20); else budget = 0;
    if (next_bytes + (64ull << 20) > budget) { r.code = "memory"; r.explain = "cold transition: next table " + mb(next_bytes) + " does not fit beside the current one (budget " + mb(budget) + ")"; r.need_bytes = next_bytes; r.have_bytes = budget; return false; }
    cl_mem t = nullptr; uint64_t nb = 0; double ms = 0;
    if (!build_autolykos_table(height, t, nb, ms, r)) { if (t) clReleaseMemObject(t); return false; }
    if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; }
    next_dataset_ = t; next_dataset_bytes_ = nb; next_height_ = height; next_n_ = autolykos_n(height); next_cursor_ = next_n_;
    return true;
}

void ClEngineX::close() {
    for (cl_kernel *k : { &search_k_, &table_k_, &dag_k_ }) if (*k) { clReleaseKernel(*k); *k = nullptr; }
    for (cl_program *p : { &prog_, &dag_prog_ }) if (*p) { clReleaseProgram(*p); *p = nullptr; }
    if (trace_k_) { clReleaseKernel(trace_k_); trace_k_ = nullptr; } if (trace_prog_) { clReleaseProgram(trace_prog_); trace_prog_ = nullptr; } if (trace_buf_) { clReleaseMemObject(trace_buf_); trace_buf_ = nullptr; } trace_words_ = 0; use_trace_ = false;
    clFinish(queue_); if (build_queue_) clFinish(build_queue_);   // every command retired before the releases (14 Oct 2026, F-110: the next epoch's 4 GiB allocation must not meet a dataset still held by the driver on an 8 GB board)
    for (cl_mem *m : { &dataset_, &dataset3_, &dataset4_, &next_dataset_, &light_, &r17_tex_, &job_buf_[0], &job_buf_[1], &results_[0], &results_[1], &count_[0], &count_[1], &ih_scratch_, &ih_st_, &ih_next_light_, &rx_sched_, &rx_rsched_, &dataset2_, &ih_next_batchbuf_, &ihb_state_, &ihb_req_, &ihb_hist_, &ihb_cursor_, &ihb_order_, &ihb_items_, &ihb_job_ }) if (*m) { clReleaseMemObject(*m); *m = nullptr; }
    clFinish(queue_);   // the releases retired before the caller allocates again
    for (cl_kernel *k : { &ihb_setup_k_, &ihb_hist_k_, &ihb_scan_k_, &ihb_scatter_k_, &ihb_fetch_k_, &ihb_mix_k_, &ihb_final_k_ }) if (*k) { clReleaseKernel(*k); *k = nullptr; }
    ih_next_cursor_ = 0; ih_next_items_ = 0;
    rx_epoch_ = ~0ull;
    if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
    if (ih_next_ctx_) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; }
    if (next_ev_) { clReleaseEvent(next_ev_); next_ev_ = nullptr; }
    if (build_done_) { clFinish(build_queue_); clReleaseEvent(build_done_); build_done_ = nullptr; }
    dataset_bytes_ = 0; next_dataset_bytes_ = 0; allocated_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0;
}

uint32_t ClEngineX::work_group() const {   // version 12: two octets per work-group (32 KB of local table); FORGE (20, 21): sixteen work-items an attempt and 16,448 bytes of local memory each, so the group is clamped to the attempts the device's local memory holds (the 9060 XT on rusticl: 64 KiB = 3 attempts; a 128-item group asked 128.5 KiB and the scratchpads aliased, F-132's OpenCL half, 15 Oct 2026)
    if (cand_ && cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14) return 16u;
    uint32_t wg = knobs_.threadgroup * wave_;
    if (cand_ && cand_->kind == Kind::igneumhash && ih_.forge()) {
        uint32_t attempts = wg / 16u; if (attempts < 1u) attempts = 1u;
        if (info_.local_mem) { const uint32_t fit = (uint32_t)(info_.local_mem / ih_.forge_local_bytes()); if (fit >= 1u && attempts > fit) attempts = fit; }
        wg = attempts * 16u;
    }
    return wg;
}

uint32_t ClEngineX::hashes_for_duration() const {
    uint32_t wg = work_group();
    uint32_t lanes = (cand_ && cand_->kind == Kind::fishhash) ? fh_lanes_ : (cand_ && cand_->kind == Kind::rxe1) ? rx_.group : (cand_ && cand_->kind == Kind::igneumhash) ? ih_lanes() : 1u;
    uint32_t wgs = 64;
    if (last_gpu_ms_ > 0 && last_hashes_ > 0) { double want = (double)last_hashes_ * (double)knobs_.batch_ms / last_gpu_ms_; wgs = (uint32_t)(want * lanes / wg); if (wgs < 1) wgs = 1; if (wgs > (1u << 20)) wgs = 1u << 20; }
    return wgs * wg / lanes;
}

// version 12's shared table (SPEC-R0E-v0 2c, main 2a12dee9): shared_kib x 256 words per (period, anchor), filled once by the fill kernel from the
// period's seed; a 16-byte stand-in buffer when the document has none (the kernel's argument is unconditional under versions 12 to 14)
void ClEngineX::r12_ensure_shared() {
    cl_int err = 0;
    const size_t bytes = ih_.shared_words() ? (size_t)ih_.shared_words() * 4 : 16;
    if (!r12_shared_) { r12_shared_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes, nullptr, &err); r12_period_ = ~0ull; }
    if (!r12_seed_) r12_seed_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 32, nullptr, &err);
    if (!r12_batch_) r12_batch_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, sizeof(IhDatasetBatch), nullptr, &err);
}
// VERSION 15: the period key's eight words and the packed programme (r0f_period) into their buffers; called at the job's start and on every period change
bool ClEngineX::r0f_fill_period(uint64_t height) {
    cl_int err = 0; uint8_t key[32]; std::vector<uint32_t> prog; std::string e;
    if (ih_.version == 17) { if (!r17_key(ih_, knobs_, height, key, e)) { fprintf(stderr, "opencl: version 17 period: %s\n", e.c_str()); return false; } prog.assign(4, 0u); }   // the texture form: the key alone, a dummy programme buffer
    else if (!r0f_period(ih_, knobs_, height, key, prog, e)) { fprintf(stderr, "opencl: version 15 period: %s\n", e.c_str()); return false; }
    uint32_t kw[8]; for (int i = 0; i < 8; i++) kw[i] = (uint32_t)key[4 * i] | ((uint32_t)key[4 * i + 1] << 8) | ((uint32_t)key[4 * i + 2] << 16) | ((uint32_t)key[4 * i + 3] << 24);
    if (!r0f_key_) r0f_key_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, 32, nullptr, &err);
    if (!r0f_prog_) r0f_prog_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, prog.size() * 4, nullptr, &err);
    if (!r0f_key_ || !r0f_prog_) return false;
    clEnqueueWriteBuffer(queue_, r0f_key_, CL_TRUE, 0, 32, kw, 0, nullptr, nullptr);
    clEnqueueWriteBuffer(queue_, r0f_prog_, CL_TRUE, 0, prog.size() * 4, prog.data(), 0, nullptr, nullptr);
    r0f_period_ = ih_.period_blocks ? height / ih_.period_blocks : 0;
    return true;
}
bool ClEngineX::r12_fill_shared(uint64_t height) {
    if (ih_.version < 12 || !ih_.shared_words()) return true;
    const uint64_t period = ih_.period_blocks ? height / ih_.period_blocks : 0;
    if (r12_period_ == period && r12_shared_) return true;
    r12_ensure_shared(); if (!r12_fill_k_ || !r12_shared_ || !dataset_) return false;
    uint8_t seed[32]; std::string err; if (!r12_shared_seed(ih_, knobs_, height, seed, err)) { fprintf(stderr, "opencl: shared table seed: %s\n", err.c_str()); return false; }
    uint32_t s8[8]; for (int i = 0; i < 8; i++) s8[i] = (uint32_t)seed[4 * i] | ((uint32_t)seed[4 * i + 1] << 8) | ((uint32_t)seed[4 * i + 2] << 16) | ((uint32_t)seed[4 * i + 3] << 24);
    IhDatasetBatch b{0, 0, 0, 0, ih_consts_of(ih_, ih_P_, ih_E_)};
    clEnqueueWriteBuffer(queue_, r12_seed_, CL_TRUE, 0, 32, s8, 0, nullptr, nullptr);
    clEnqueueWriteBuffer(queue_, r12_batch_, CL_TRUE, 0, sizeof b, &b, 0, nullptr, nullptr);
    const cl_uint words = ih_.shared_words();
    clSetKernelArg(r12_fill_k_, 0, sizeof(cl_mem), &r12_batch_); clSetKernelArg(r12_fill_k_, 1, sizeof(cl_mem), &dataset_); clSetKernelArg(r12_fill_k_, 2, sizeof(cl_mem), &r12_seed_);
    clSetKernelArg(r12_fill_k_, 3, sizeof(cl_mem), &r12_shared_); clSetKernelArg(r12_fill_k_, 4, sizeof(cl_uint), &words); if (dataset2_) clSetKernelArg(r12_fill_k_, 5, sizeof(cl_mem), &dataset2_);
    size_t g = 1, l = 1;
    if (clEnqueueNDRangeKernel(queue_, r12_fill_k_, 1, nullptr, &g, &l, 0, nullptr, nullptr) != CL_SUCCESS) return false;
    clFinish(queue_); r12_period_ = period;
    if (getenv("IGM_R12_DUMP")) {   // the diagnostic read-back: the table's BLAKE3 digest and its edge words against the reference trace's shared_table record
        std::vector<uint32_t> t(words); clEnqueueReadBuffer(queue_, r12_shared_, CL_TRUE, 0, (size_t)words * 4, t.data(), 0, nullptr, nullptr);
        uint8_t dg[32]; ih_blake3_full(reinterpret_cast<const uint8_t *>(t.data()), (size_t)words * 4, dg);
        fprintf(stderr, "r12-dump: period %llu words %u digest %s words0..3 %u %u %u %u words64..67 %u %u %u %u last4 %u %u %u %u %s\n", (unsigned long long)period, words, hex_of(dg, 32).c_str(), t[0], t[1], t[2], t[3], t[64], t[65], t[66], t[67], t[words - 4], t[words - 3], t[words - 2], t[words - 1], dataset2_ ? "(two buffers)" : "(one buffer)");
    }
    return true;
}

void ClEngineX::forge_trace_print(const Job &job) {   // FORGE: attempt 0's lane 0 dump from the global scratch buffer (IGM_FORGE_TRACE): E, prog word 0, phash0, the init registers, lane 1's eight, sp words, the item, after the read, after step 0
    std::vector<uint32_t> w(128, 0u); if (!ih_scratch_ || clEnqueueReadBuffer(queue_, ih_scratch_, CL_TRUE, 0, 512, w.data(), 0, nullptr, nullptr) != CL_SUCCESS) return;
    auto pr = [&](const char *what, size_t a, size_t n) { fprintf(stderr, "forge trace nonce %llu %s:", (unsigned long long)job.nonce_start, what); for (size_t i = 0; i < n; i++) fprintf(stderr, " %08x", w[a + i]); fprintf(stderr, "\n"); };
    pr("E", 0, 8); pr("prog0", 8, 4); pr("phash0", 12, 1); pr("init lane0", 16, 32); pr("init lane1", 48, 8); pr("sp0", 56, 4); pr("sp256", 60, 4); pr("item", 64, 3); pr("n_items_mask xlL ds0 ds926986 fold_every", 67, 5); pr("after-read0 lane0", 72, 32); pr("step0 lane0", 104, 24);
}
void ClEngineX::r0e_optrace_print(const Job &job) {   // the first octet's half: round r, op i, lane l, sixteen words at ((r * ops + i) * 8 + l) * 16, as hex words
    const size_t half = (size_t)2 * ih_.program_ops * 128; std::vector<uint32_t> w(half, 0u);
    if (clEnqueueReadBuffer(queue_, r0e_optrace_buf_, CL_TRUE, 0, half * 4, w.data(), 0, nullptr, nullptr) != CL_SUCCESS) { fprintf(stderr, "r0e optrace nonce %llu: read-back refused\n", (unsigned long long)job.nonce_start); return; }
    std::string out; out.reserve(half * 9); char b[12];
    for (size_t q = 0; q < half; q++) { snprintf(b, sizeof b, "%08x", w[q]); out += b; }
    fprintf(stderr, "r0e optrace nonce %llu height %llu ops %u: %s\n", (unsigned long long)job.nonce_start, (unsigned long long)job.height, ih_.program_ops, out.c_str());
}

void ClEngineX::r0f_trace_print(const Job &job) {
    std::vector<uint32_t> w(1088, 0u);
    if (clEnqueueReadBuffer(queue_, r0f_trace_buf_, CL_TRUE, 0, 1088 * 4, w.data(), 0, nullptr, nullptr) != CL_SUCCESS) { fprintf(stderr, "r0f trace nonce %llu: read-back refused\n", (unsigned long long)job.nonce_start); return; }
    auto hexw = [&](size_t off, size_t n) { std::string h; char b[9]; for (size_t i = 0; i < n; i++) { uint32_t v = w[off + i]; for (int q = 0; q < 4; q++) { snprintf(b, sizeof b, "%02x", (v >> (8 * q)) & 255u); h += b; } } return h; };
    auto regs = [&](size_t off) { std::string o = "["; for (int l = 0; l < 8; l++) { o += l ? ",[" : "["; for (int q = 0; q < 32; q++) o += (q ? "," : "") + std::to_string(w[off + 32 * l + q]); o += "]"; } return o + "]"; };
    auto addrs = [&](size_t off) { std::string o = "["; for (int q = 0; q < 16; q++) o += (q ? "," : "") + std::to_string(w[off + q]); return o + "]"; };
    fprintf(stderr, "r0f trace nonce %llu height %llu: {\"stage\": %u, \"tail\": [%u, %u, %u, %u, %u, %u, %u], \"S\": \"%s\", \"init\": %s, \"it0_addr\": %s, \"it0_regs\": %s, \"it1_addr\": %s, \"it1_regs\": %s, \"last_regs\": %s, \"M\": \"%s\", \"F\": \"%s\"}\n",
            (unsigned long long)job.nonce_start, (unsigned long long)job.height, w[0], w[1], w[2], w[3], w[4], w[5], w[6], w[7], hexw(8, 8).c_str(), regs(16).c_str(), addrs(272).c_str(), regs(288).c_str(), addrs(544).c_str(), regs(560).c_str(), regs(816).c_str(), hexw(1072, 8).c_str(), hexw(1080, 8).c_str());
}

void ClEngineX::ih_run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                           const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    // VERSION 15 staged trace (IGM_R0F_TRACE=1, one-nonce jobs: the vectors path): the buffer zeroed before the launch and printed after it, whatever the launch returned
    struct R0fTracePrint { ClEngineX *e; const Job *j; bool on; ~R0fTracePrint() { if (on) e->r0f_trace_print(*j); } } r0f_tp{this, &job, cand_ && cand_->kind == Kind::igneumhash && ih_.version == 15 && r0f_trace_on_ && r0f_trace_buf_ && job.nonce_count == 1};
    if (r0f_tp.on) { std::vector<uint32_t> z(2176, 0u); clEnqueueWriteBuffer(queue_, r0f_trace_buf_, CL_TRUE, 0, 2176 * 4, z.data(), 0, nullptr, nullptr); }
    struct ForgeTracePrint { ClEngineX *e; const Job *j; bool on; ~ForgeTracePrint() { if (on) e->forge_trace_print(*j); } } forge_tp{this, &job, cand_ && cand_->kind == Kind::igneumhash && ih_.forge() && getenv("IGM_FORGE_TRACE") != nullptr};
    struct R0eOptracePrint { ClEngineX *e; const Job *j; bool on; ~R0eOptracePrint() { if (on) e->r0e_optrace_print(*j); } } r0e_tp{this, &job, cand_ && cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14 && r0e_optrace_on_ && r0e_optrace_buf_ && job.nonce_count == 1};
    if (r0e_tp.on) { std::vector<uint32_t> z(r0e_optrace_words_, 0u); clEnqueueWriteBuffer(queue_, r0e_optrace_buf_, CL_TRUE, 0, r0e_optrace_words_ * 4, z.data(), 0, nullptr, nullptr); }
    int st = 0;
    uint64_t job_epoch = ignpow_epoch_of(algo_.c_str(), params_c(), job.height, &st);
    if (st != IGNPOW_OK) return;
    if (job_epoch != ignpow_ctx_epoch(ctx_)) {
        if (dataset_) { clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_; dataset_ = nullptr; dataset_bytes_ = 0; }
        if (dataset2_) { clReleaseMemObject(dataset2_); dataset2_ = nullptr; } if (dataset3_) { clReleaseMemObject(dataset3_); dataset3_ = nullptr; } if (dataset4_) { clReleaseMemObject(dataset4_); dataset4_ = nullptr; }
        clFinish(queue_);   // F-110: the old epoch's pieces retired by the driver before the new epoch's allocation (an 8 GB board holds one 4 GiB dataset, not two)
        if (dataset_base_) { clReleaseMemObject(dataset_base_); dataset_base_ = nullptr; }
        if (light_) { clReleaseMemObject(light_); light_ = nullptr; }
        if (ih_next_ctx_ && ih_next_params_.empty() && ignpow_ctx_epoch(ih_next_ctx_) == job_epoch) {   // the warm one: finish the slices left (the residual stall)
            double w0 = now_ms();
            ih_warm_finish();
            last_swap_wait_ms_ = now_ms() - w0;
            ignpow_ctx_free(ctx_); ctx_ = ih_next_ctx_; ih_next_ctx_ = nullptr;
            dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0;
            light_ = ih_next_light_; ih_next_light_ = nullptr; std::memcpy(ih_E_, ih_next_E_, 32); height_ = job.height; next_height_ = ~0ull; r12_period_ = ~0ull;
        } else {
            Refusal r; cl_mem nd = nullptr, nl = nullptr; uint64_t nb = 0; ignpow_ctx *nc = nullptr; uint8_t ne[32];
            if (ih_next_ctx_ && ih_next_params_.empty()) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0; } if (ih_next_light_) { clReleaseMemObject(ih_next_light_); ih_next_light_ = nullptr; } }
            if (!ih_build_dataset(job.height, nc, nd, nb, ne, nl, r)) { if (nc) ignpow_ctx_free(nc); if (nd) clReleaseMemObject(nd); if (nl) clReleaseMemObject(nl); fprintf(stderr, "opencl: igneumhash dataset for height %llu: %s\n", (unsigned long long)job.height, r.explain.c_str()); return; }
            ignpow_ctx_free(ctx_); ctx_ = nc; dataset_ = nd; dataset_bytes_ = nb; light_ = nl; std::memcpy(ih_E_, ne, 32); height_ = job.height; r12_period_ = ~0ull;   // the new epoch's dataset: the shared table refills even inside one period
        }
    }
    // versions 12 to 14: the period's shared table is filled from the dataset of the JOB's epoch, so after the epoch swap above (13 Oct 2026: filled before it, a
    // job in a new epoch read a table built from the previous epoch's dataset)
    if (cand_->kind == Kind::igneumhash && ih_.version >= 12 && !r12_fill_shared(job.height)) { fprintf(stderr, "opencl: the period's shared table could not be filled\n"); return; }
    IhJob base{};
    for (int i = 0; i < 8; i++) { base.header[i] = le32(job.header + 4 * i); base.target[i] = be32(job.target + 4 * i); }
    base.max_results = max_results_; base.scratch_slots = ih_slots_; base.k = ih_consts_of(ih_, ih_P_, ih_E_);
    if (cand_ && cand_->kind == Kind::igneumhash && ih_.keyed_rounds) {   // version 8 bit 10: the job's secret (the keyed-job extension) or the test secret of the header (the vectors and the bench)
        uint8_t sk[32]; if (job.has_sk) std::memcpy(sk, job.sk, 32); else ignh_test_sk(job.header, sk);
        for (int i = 0; i < 8; i++) base.k.sk[i] = le32(sk + 4 * i);
    }
    if (cand_ && cand_->kind == Kind::igneumhash && ih_.forge2() && ih_.period_blocks) {   // THE CHAIN FORM (F-141): the period seed BLAKE3(Forge/period || E || LE64 chain_id || LE64 (height / period_blocks) || anchor) on the host per job, carried to the kernel in the sk words (the document's test anchor; a runtime anchor is the anchored call's, not yet in this worker)
        uint8_t anchor[32] = {0}; if (ih_.program_anchor.size() == 64) for (int i = 0; i < 32; i++) anchor[i] = (uint8_t)strtoul(ih_.program_anchor.substr(2 * i, 2).c_str(), nullptr, 16);
        uint8_t w[120]; size_t n = 0; const char *tag = "IgneumHash/Forge/period"; w[n++] = 23; std::memcpy(w + n, tag, 23); n += 23; std::memcpy(w + n, ih_E_, 32); n += 32;
        for (int i = 0; i < 8; i++) w[n++] = (uint8_t)(ih_.chain_id >> (8 * i)); const uint64_t per = job.height / ih_.period_blocks; for (int i = 0; i < 8; i++) w[n++] = (uint8_t)(per >> (8 * i)); std::memcpy(w + n, anchor, 32); n += 32;
        uint8_t ps[32]; ih_blake3_full(w, n, ps); for (int i = 0; i < 8; i++) base.k.sk[i] = le32(ps + 4 * i);
        // THE PERIOD TABLE (the scalar path): the shapes' words, class orders and slot immediates from the period seed, once per job, into the r0f_prog slot
        if (std::memcmp(ps, forge_table_seed_, 32) != 0) {
            const uint32_t W = ih_.program_ops, fpm = (ih_.program_distance >> 1) & 7u; const bool occ = fpm >= 3;
            static const uint8_t quota[32] = { 3, 0, 4, 3, 9, 1, 3, 5, 3, 2, 4, 3, 10, 0, 3, 4, 6, 1, 3, 7, 4, 3, 9, 2, 3, 0, 4, 11, 1, 3, 8, 5 }, quota_int[32] = { 3, 3, 4, 3, 9, 3, 3, 5, 3, 4, 4, 3, 10, 3, 3, 4, 6, 3, 3, 5, 4, 3, 9, 9, 3, 3, 4, 11, 10, 3, 3, 5 };
            auto fchain = [](uint32_t x, uint32_t v) { x = ((x ^ v) << 5 | (x ^ v) >> 27) + v; x ^= x >> 16; x *= 0x9E3779B1u; x ^= x >> 15; x *= 0x85EBCA6Bu; x ^= x >> 13; return x; };
            std::vector<uint32_t> tab((size_t)4 * W + (size_t)96 * W, 0u); uint32_t x[8]; for (int i = 0; i < 8; i++) x[i] = le32(ps + 4 * i);
            for (uint32_t i = 0; i < 4 * W; i++) { x[i & 7] = fchain(x[i & 7], i); tab[i] = x[i & 7]; }
            for (uint32_t i = 0; i < W; i++) { uint32_t *cls = &tab[4 * W + 96 * i], *im = cls + 32, *qv = cls + 64; for (int q = 0; q < 32; q++) cls[q] = (fpm == 1 || occ) ? quota_int[q] : quota[q];
                for (uint32_t qq = 31; qq > 0; qq--) { const uint32_t j = fchain(tab[4 * i], qq) % (qq + 1); std::swap(cls[qq], cls[j]); }
                for (uint32_t q = 0; q < 32; q++) { im[q] = fchain(tab[4 * i + 1], q); qv[q] = fchain(tab[4 * i + 2], q); } }
            cl_int err = 0; if (r0f_prog_) { clReleaseMemObject(r0f_prog_); r0f_prog_ = nullptr; }
            r0f_prog_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, tab.size() * 4, tab.data(), &err);
            if (r0f_prog_) std::memcpy(forge_table_seed_, ps, 32); else fprintf(stderr, "opencl: the period table buffer was refused (%d)\n", err);
        }
    }
    RxJob rbase{};
    if (rxe1_) {   // the schedule of this epoch (mode F: of the pid; mode E: of the epoch), a constant buffer
        uint64_t epn = ignpow_ctx_epoch(ctx_);
        if (rx_epoch_ != epn || !rx_sched_) {
            uint8_t ps[32] = {0}; rx_sched_host_.clear();
            if (rx_.blocks > 0) { rxe1_program_seed(rx_, rx_pid_, epn, ih_E_, ps); rxe1_schedule(rx_, ps, rx_sched_host_); }
            if (rx_sched_host_.empty()) rx_sched_host_.push_back(0u);
            cl_int e2 = 0; if (rx_sched_) clReleaseMemObject(rx_sched_);
            rx_sched_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, rx_sched_host_.size() * 4, rx_sched_host_.data(), &e2);
            std::vector<uint32_t> rdesc;   // route 2: the epoch's routing descriptors (schema 3)
            if (rx_.schema_version() >= 3 && rx_.route == 2) { uint8_t rs[32]; rxe1_route_seed(rx_, rx_pid_, epn, ih_E_, rs); rxe1_route_descriptors(rx_, rs, rdesc); }
            if (rdesc.empty()) rdesc.push_back(0u);
            if (rx_rsched_) clReleaseMemObject(rx_rsched_);
            rx_rsched_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, rdesc.size() * 4, rdesc.data(), &e2);
            rx_epoch_ = epn;
        }
        for (int i = 0; i < 8; i++) { rbase.header[i] = base.header[i]; rbase.target[i] = base.target[i]; }
        rbase.max_results = max_results_; rbase.slots = ih_slots_; rbase.k = rx_consts_of(rx_, rx_pid_, epn, ih_E_);
    }
    {   // a work-group above the kernel's maximum on this runtime is a named refusal before any enqueue (T-B9's workgroup 512 cells on the 9070 XT: CL_INVALID_WORK_GROUP_SIZE)
        size_t wgmax = 0; clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_WORK_GROUP_SIZE, sizeof wgmax, &wgmax, nullptr);
        const uint32_t wg = work_group();
        if (wgmax && wg > wgmax) { fprintf(stderr, "opencl: work-group %u exceeds the kernel's maximum %zu on this runtime: refused before the enqueue (threadgroup %u x wave %u)\n", wg, wgmax, knobs_.threadgroup, wave_); ring_fault_ = true; return; }
    }
    if (is_bucket() && !rxe1_) { run_bucket(job, &base, on_result, on_progress, should_stop); return; }   // T-B14
    uint64_t done = 0; int slot = 0;
    const uint32_t NF = knobs_.inflight < 1 ? 1 : knobs_.inflight > 8 ? 8 : knobs_.inflight;   // T-B9: batches in flight
    cl_event inflight[8] = {nullptr};
    uint32_t inflight_hashes[8] = {0}; uint64_t inflight_nonce[8] = {0}; double wall_start[8] = {0};
    const uint32_t zero = 0;
    std::vector<std::pair<uint64_t, uint32_t>> redo;
    // every batch in flight has its own scratch and state slots: slot s uses [s * max_batch, (s + 1) * max_batch). A sub-buffer's
    // origin must sit on the device's base address alignment (CL_DEVICE_MEM_BASE_ADDR_ALIGN, bits), so max_batch is rounded down
    // until every slot's origin in both the state and the scratch areas is aligned: a misaligned origin (10 Oct 2026, the first
    // T-B9 rows on the 9070 XT) made clCreateSubBuffer fail, the kernel ran on a null state buffer, the device reset and the
    // runtime stayed wedged for every later process on the machine.
    uint32_t max_batch = ih_slots_ / NF;
    if (NF > 1) {
        cl_uint align_bits = 0; clGetDeviceInfo(info_.device, CL_DEVICE_MEM_BASE_ADDR_ALIGN, sizeof align_bits, &align_bits, nullptr);
        const size_t align = align_bits ? align_bits / 8 : 4096, st_slot = rxe1_ ? 2048 : 640, scr_slot = ih_.scratch_bytes ? ih_.scratch_bytes : 0;
        auto aligned = [&](uint32_t mb) { return ((size_t)mb * st_slot) % align == 0 && (!scr_slot || ((size_t)mb * scr_slot) % align == 0); };
        while (max_batch > 1 && !aligned(max_batch)) max_batch--;
    }
    bool ring_fault = false;
    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) -> bool {
        if (rxe1_) { RxJob gj = rbase; gj.nonce_lo = (uint32_t)nonce; gj.nonce_hi = (uint32_t)(nonce >> 32); gj.hashes = hashes; gj.slots = max_batch; gj.max_results = max_results_; clEnqueueWriteBuffer(queue_, job_buf_[s], CL_TRUE, 0, sizeof gj, &gj, 0, nullptr, nullptr); }
        else { IhJob gj = base; gj.nonce_lo = (uint32_t)nonce; gj.nonce_hi = (uint32_t)(nonce >> 32); gj.hashes = hashes; gj.scratch_slots = max_batch; clEnqueueWriteBuffer(queue_, job_buf_[s], CL_TRUE, 0, sizeof gj, &gj, 0, nullptr, nullptr); }
        clEnqueueWriteBuffer(queue_, count_[s], CL_TRUE, 0, 4, &zero, 0, nullptr, nullptr);
        cl_kernel ks = (use_trace_ && trace_k_) ? trace_k_ : search_k_;   // the trace program (trace_one) runs the same job path
        clSetKernelArg(ks, 0, sizeof(cl_mem), &job_buf_[s]); clSetKernelArg(ks, 1, sizeof(cl_mem), &dataset_);
        clSetKernelArg(ks, 2, sizeof(cl_mem), &results_[s]); clSetKernelArg(ks, 3, sizeof(cl_mem), &count_[s]);
        // slot s's scratch and state areas: sub-buffers at s * max_batch slots (the kernels index from their base)
        cl_mem scr_s = ih_scratch_, st_s = ih_st_;
        if (NF > 1) {
            cl_int se = 0;
            size_t st_slot = rxe1_ ? 2048 : 640, scr_slot = ih_.scratch_bytes ? ih_.scratch_bytes : 0;
            cl_buffer_region rs{ (size_t)s * max_batch * st_slot, (size_t)max_batch * st_slot };
            st_s = clCreateSubBuffer(ih_st_, CL_MEM_READ_WRITE, CL_BUFFER_CREATE_TYPE_REGION, &rs, &se);
            if (se != CL_SUCCESS || !st_s) { fprintf(stderr, "opencl: ring slot %d state sub-buffer refused (%d): the run ends as a fault\n", s, (int)se); ring_fault = true; return false; }
            if (scr_slot) {
                cl_buffer_region rc{ (size_t)s * max_batch * scr_slot, (size_t)max_batch * scr_slot }; scr_s = clCreateSubBuffer(ih_scratch_, CL_MEM_READ_WRITE, CL_BUFFER_CREATE_TYPE_REGION, &rc, &se);
                if (se != CL_SUCCESS || !scr_s) { fprintf(stderr, "opencl: ring slot %d scratch sub-buffer refused (%d): the run ends as a fault\n", s, (int)se); clReleaseMemObject(st_s); ring_fault = true; return false; }
            }
        }
        if (rxe1_) { clSetKernelArg(ks, 4, sizeof(cl_mem), &rx_sched_); clSetKernelArg(ks, 5, sizeof(cl_mem), &st_s); }
        else { clSetKernelArg(ks, 4, sizeof(cl_mem), &scr_s); clSetKernelArg(ks, 5, sizeof(cl_mem), &st_s); }
        uint32_t wg = work_group();
        // the local words: one per work-item for the exchange, plus the Riptide form 2 staging area (item_words per eight-lane group)
        const size_t stage_words = (!rxe1_ && (knobs_.variant == "form2" || knobs_.clopts.find("IGM_IH_V4_FORM=2") != std::string::npos)) ? (size_t)(wg / 8) * ih_.item_words() : 0;
        const size_t bw_words = (bw_loader_ == 5) ? (size_t)wg * 4 : 0;   // Breakwater loader 5: 16 bytes per lane of LDS past the exchange words
        if (cand_->kind == Kind::igneumhash && ih_.forge()) clSetKernelArg(ks, 6, (size_t)(wg / 16) * ih_.forge_local_bytes(), nullptr);   // VERSION 20: the 16 KiB scratchpad an attempt, sixteen work-items an attempt (FORM-FORGE 1(5))
        else clSetKernelArg(ks, 6, (wg + stage_words + bw_words + ih_v8s_words()) * 4 + (size_t)knobs_.lds_pad_kib * 1024, nullptr);   // the version 8 snapshot (v8s) past the exchange words; the LDS pad: the occupancy limiter (lds_pad_kib), never read
        if (rxe1_) clSetKernelArg(ks, 7, sizeof(cl_mem), &rx_rsched_);
        else if (bw_loader_) { int a = 7; if (ks == trace_k_) clSetKernelArg(ks, a++, sizeof(cl_mem), &trace_buf_); if (dataset2_) clSetKernelArg(ks, a++, sizeof(cl_mem), &dataset2_); }   // the one-wave wrapper takes the trace buffer then the second buffer
        else { int a = 7; if (ks == trace_k_) clSetKernelArg(ks, a++, sizeof(cl_mem), &trace_buf_); if (dataset2_) clSetKernelArg(ks, a++, sizeof(cl_mem), &dataset2_); if (dataset3_) { clSetKernelArg(ks, a++, sizeof(cl_mem), &dataset3_); clSetKernelArg(ks, a++, sizeof(cl_mem), &dataset4_); } if (cand_->kind == Kind::igneumhash && ih_.version >= 15) { clSetKernelArg(ks, a++, sizeof(cl_mem), &r0f_key_); clSetKernelArg(ks, a++, sizeof(cl_mem), &r0f_prog_); const cl_uint nops = ih_.program_ops; clSetKernelArg(ks, a++, sizeof(cl_uint), &nops); const cl_uint jlo = (cl_uint)nonce, jhi = (cl_uint)(nonce >> 32), jh = hashes; clSetKernelArg(ks, a++, sizeof(cl_uint), &jlo); clSetKernelArg(ks, a++, sizeof(cl_uint), &jhi); clSetKernelArg(ks, a++, sizeof(cl_uint), &jh); if (r0f_trace_on_ && r0f_trace_buf_) clSetKernelArg(ks, a++, sizeof(cl_mem), &r0f_trace_buf_); if (ih_.version == 17) clSetKernelArg(ks, a++, sizeof(cl_mem), &r17_tex_); } else if (cand_->kind == Kind::igneumhash && ih_.version >= 12) { if (!r12_shared_) r12_ensure_shared(); clSetKernelArg(ks, a++, sizeof(cl_mem), &r12_shared_); if (r0e_optrace_on_ && r0e_optrace_buf_) clSetKernelArg(ks, a++, sizeof(cl_mem), &r0e_optrace_buf_); } }   // the eight-lane wrapper takes the trace buffer then the second buffer (both at 4 GiB with the identity command: one set alone refused the enqueue, -52)
        uint32_t lanes = rxe1_ ? rx_.group : ih_lanes();
        const uint32_t ctx = rxe1_ ? 1u : ih_nonces_per_group();   // T-B10a's ctx or the x2 form's two nonces per eight-lane group
        size_t total = (((uint64_t)hashes + ctx - 1) / ctx * lanes + wg - 1) / wg * wg, local = wg;
        wall_start[s] = now_ms();
        cl_event ev = nullptr;
        cl_int ek = clEnqueueNDRangeKernel(queue_, ks, 1, nullptr, &total, &local, 0, nullptr, &ev);
        if (NF > 1) { if (st_s != ih_st_) clReleaseMemObject(st_s); if (scr_s != ih_scratch_) clReleaseMemObject(scr_s); }   // the sub-buffers are retained by the enqueued kernel
        if (ek != CL_SUCCESS) { fprintf(stderr, "opencl: ring slot %d enqueue refused (%d): the run ends as a fault\n", s, (int)ek); ring_fault = true; return false; }
        clFlush(queue_);
        inflight[s] = ev; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
        return true;
    };
    auto collect = [&](int s) {
        if (!inflight[s]) return;
        clWaitForEvents(1, &inflight[s]);
        cl_int exec = 0; clGetEventInfo(inflight[s], CL_EVENT_COMMAND_EXECUTION_STATUS, sizeof exec, &exec, nullptr);
        if (exec < 0) {   // a batch that the device failed: its hashes are not counted and the run ends as a fault
            fprintf(stderr, "opencl: ring slot %d batch failed on the device (execution status %d): the run ends as a fault\n", s, (int)exec);
            clReleaseEvent(inflight[s]); inflight[s] = nullptr; ring_fault = true; return;
        }
        cl_ulong t0 = 0, t1 = 0;
        clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_START, sizeof t0, &t0, nullptr); clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_END, sizeof t1, &t1, nullptr);
        BatchStats stt; stt.hashes = inflight_hashes[s]; stt.gpu_ms = (double)(t1 - t0) / 1e6; stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        clReleaseEvent(inflight[s]); inflight[s] = nullptr;
        if (getenv("IGM_IHB_DEBUG")) { uint32_t w[128]; clEnqueueReadBuffer(queue_, ih_st_, CL_TRUE, 0, 512, w, 0, nullptr, nullptr); fprintf(stderr, "ihb-debug stock st_area slot0:"); for (int q = 0; q < 8; q++) fprintf(stderr, " %08x", w[q]); fprintf(stderr, " | lane1: %08x %08x\n", w[16], w[17]); }
        uint32_t n = 0; clEnqueueReadBuffer(queue_, count_[s], CL_TRUE, 0, 4, &n, 0, nullptr, nullptr);
        if (n > max_results_) { fprintf(stderr, "opencl: %u shares exceed the %u-entry buffer; re-running in halves\n", n, max_results_); redo.push_back({inflight_nonce[s], inflight_hashes[s]}); return; }
        std::vector<IhResult> rr(n);
        if (n) clEnqueueReadBuffer(queue_, results_[s], CL_TRUE, 0, n * sizeof(IhResult), rr.data(), 0, nullptr, nullptr);
        for (uint32_t i = 0; i < n; i++) {
            Result out; out.nonce = ((uint64_t)rr[i].nonce_hi << 32) | rr[i].nonce_lo;
            for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, rr[i].mix[w]); put_le32(out.digest + 4 * w, rr[i].digest[w]); }
            on_result(out);
        }
        on_progress(stt);
    };
    auto drain_redo = [&]() {
        for (uint32_t q = 0; q < NF; q++) collect((int)q);
        while (!redo.empty()) { auto [n0, h0] = redo.back(); redo.pop_back(); if (h0 <= 1) continue; uint32_t a = h0 / 2, b = h0 - a; if (submit(0, n0, a)) collect(0); if (submit(0, n0 + a, b)) collect(0); }
    };
    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (!redo.empty()) drain_redo();
        uint32_t want = knobs_.batch_nonces ? knobs_.batch_nonces : knobs_.grid_pct ? (uint32_t)((uint64_t)max_batch * knobs_.grid_pct / 100) : hashes_for_duration();   // a ladder rung fixes the nonces per batch; a grid percent takes that share of the saturation batch (the slot bound)
        if (want > max_batch) want = max_batch;
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;
        collect(slot);
        if (ring_fault) break;
        if (!submit(slot, job.nonce_start + done, hashes)) break;
        if (!prepare_ready()) ih_warm_step(ih_slice_);   // the next epoch's slice behind this batch on the same queue
        done += hashes; slot = (slot + 1) % (int)NF;
        if (knobs_.duty < 1.0 && last_gpu_ms_ > 0) std::this_thread::sleep_for(std::chrono::microseconds((long long)(last_gpu_ms_ * (1.0 - knobs_.duty) / knobs_.duty * 1000.0)));
    }
    if (!ring_fault) drain_redo(); else { for (uint32_t q = 0; q < NF; q++) if (inflight[q]) { clReleaseEvent(inflight[q]); inflight[q] = nullptr; } ring_fault_ = true; }
}

// T-B14 the bucket solver (the founder's second priority, 10 Oct 2026): one batch of ihb_batch_ nonces at a time through
// setup, then per round the request histogram, the scan, the scatter, the fetch in bucket order and the merge from the
// state in global memory, then the final; every launch on the mining queue in order; the batch's wall time is its cost
// (the energy on the row is the whole pipeline's). bucket_bits 0 serves the fetch in nonce order (no sort).
struct IhbJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t hashes; uint32_t max_results; uint32_t bucket_shift; uint32_t n_buckets; uint32_t round; uint32_t pad; IhConsts k; };
void ClEngineX::run_bucket(const Job &job, const void *base_job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    const IhJob &base = *(const IhJob *)base_job;
    IhbJob bj{};
    for (int i = 0; i < 8; i++) { bj.header[i] = base.header[i]; bj.target[i] = base.target[i]; }
    bj.max_results = max_results_; bj.k = base.k;
    const uint32_t bits = knobs_.bucket_bits;
    uint32_t log_items = 0; while ((1ull << log_items) < (uint64_t)ih_.n_items()) log_items++;
    bj.n_buckets = bits ? (1u << bits) : 0u; bj.bucket_shift = bits && log_items > bits ? log_items - bits : 0u;
    const uint32_t wg1 = wave_ ? wave_ : 64, zero = 0;   // one work-item per nonce for the setup, the merge and the final (the n1 lane form)
    const uint32_t nb = bj.n_buckets ? bj.n_buckets : 1u;
    uint64_t done = 0;
    while (done < job.nonce_count) {
        if (should_stop()) break;
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < ihb_batch_ ? (uint32_t)left : ihb_batch_;
        uint64_t nonce = job.nonce_start + done;
        bj.nonce_lo = (uint32_t)nonce; bj.nonce_hi = (uint32_t)(nonce >> 32); bj.hashes = hashes; bj.round = 0;
        double t0 = now_ms();
        clEnqueueWriteBuffer(queue_, ihb_job_, CL_TRUE, 0, sizeof bj, &bj, 0, nullptr, nullptr);
        clEnqueueWriteBuffer(queue_, count_[0], CL_TRUE, 0, 4, &zero, 0, nullptr, nullptr);
        auto launch = [&](cl_kernel k, size_t total, size_t local) -> bool {
            total = (total + local - 1) / local * local;
            cl_int e = clEnqueueNDRangeKernel(queue_, k, 1, nullptr, &total, &local, 0, nullptr, nullptr);
            if (e != CL_SUCCESS) { fprintf(stderr, "opencl: bucket pipeline enqueue refused (%d): the run ends as a fault\n", (int)e); ring_fault_ = true; return false; }
            return true;
        };
        const size_t lanes8 = (size_t)hashes * 8;
        // (0) setup
        clSetKernelArg(ihb_setup_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_setup_k_, 1, sizeof(cl_mem), &ihb_state_); clSetKernelArg(ihb_setup_k_, 2, sizeof(cl_mem), &ih_scratch_);
        clSetKernelArg(ihb_setup_k_, 3, sizeof(cl_mem), &ihb_req_);
        if (!launch(ihb_setup_k_, hashes, wg1)) return;
        const bool dbg = getenv("IGM_IHB_DEBUG") != nullptr;
        auto dump = [&](const char *what) {
            clFinish(queue_);
            uint32_t w[160], rq = 0; clEnqueueReadBuffer(queue_, ihb_state_, CL_TRUE, 0, 640, w, 0, nullptr, nullptr); clEnqueueReadBuffer(queue_, ihb_req_, CL_TRUE, 0, 4, &rq, 0, nullptr, nullptr);
            fprintf(stderr, "ihb-debug %s: R lane0 %08x %08x %08x %08x lane1 %08x %08x | S %08x %08x | idx %08x req %08x \n", what, w[0], w[1], w[2], w[3], w[16], w[17], w[128], w[129], w[144], rq);
        };
        if (dbg) dump("after setup");
        for (uint32_t r = 0; r < ih_.rounds; r++) {
            if (r) { bj.round = r; clEnqueueWriteBuffer(queue_, ihb_job_, CL_TRUE, offsetof(IhbJob, round), 4, &bj.round, 0, nullptr, nullptr); }   // blocking: a non-blocking write reads the host word at execution time (the KAWPOW lesson)
            if (bits) {
                clEnqueueFillBuffer(queue_, ihb_hist_, &zero, 4, 0, (size_t)nb * 4, 0, nullptr, nullptr);
                clSetKernelArg(ihb_hist_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_hist_k_, 1, sizeof(cl_mem), &ihb_req_); clSetKernelArg(ihb_hist_k_, 2, sizeof(cl_mem), &ihb_hist_);
                if (!launch(ihb_hist_k_, hashes, 256)) return;
                clSetKernelArg(ihb_scan_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_scan_k_, 1, sizeof(cl_mem), &ihb_hist_); clSetKernelArg(ihb_scan_k_, 2, sizeof(cl_mem), &ihb_cursor_); clSetKernelArg(ihb_scan_k_, 3, 256 * 4, nullptr);
                if (!launch(ihb_scan_k_, 256, 256)) return;
                clSetKernelArg(ihb_scatter_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_scatter_k_, 1, sizeof(cl_mem), &ihb_req_); clSetKernelArg(ihb_scatter_k_, 2, sizeof(cl_mem), &ihb_cursor_); clSetKernelArg(ihb_scatter_k_, 3, sizeof(cl_mem), &ihb_order_);
                if (!launch(ihb_scatter_k_, hashes, 256)) return;
            }
            clSetKernelArg(ihb_fetch_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_fetch_k_, 1, sizeof(cl_mem), &dataset_); clSetKernelArg(ihb_fetch_k_, 2, sizeof(cl_mem), &ihb_req_);
            clSetKernelArg(ihb_fetch_k_, 3, sizeof(cl_mem), &ihb_order_); clSetKernelArg(ihb_fetch_k_, 4, sizeof(cl_mem), &ihb_items_);
            if (dataset2_) clSetKernelArg(ihb_fetch_k_, 5, sizeof(cl_mem), &dataset2_);
            if (!launch(ihb_fetch_k_, lanes8, 256)) return;
            clSetKernelArg(ihb_mix_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_mix_k_, 1, sizeof(cl_mem), &ihb_state_); clSetKernelArg(ihb_mix_k_, 2, sizeof(cl_mem), &ih_scratch_);
            clSetKernelArg(ihb_mix_k_, 3, sizeof(cl_mem), &ihb_items_); clSetKernelArg(ihb_mix_k_, 4, sizeof(cl_mem), &ihb_req_);
            if (!launch(ihb_mix_k_, hashes, wg1)) return;
            if (dbg && r == 0) { clFinish(queue_); uint32_t it[8]; clEnqueueReadBuffer(queue_, ihb_items_, CL_TRUE, 0, 32, it, 0, nullptr, nullptr); fprintf(stderr, "ihb-debug items nonce0: %08x %08x %08x %08x | lane1 %08x\n", it[0], it[1], it[2], it[3], it[4]); dump("after round 0"); }
        }
        clSetKernelArg(ihb_final_k_, 0, sizeof(cl_mem), &ihb_job_); clSetKernelArg(ihb_final_k_, 1, sizeof(cl_mem), &ihb_state_); clSetKernelArg(ihb_final_k_, 2, sizeof(cl_mem), &ih_scratch_);
        clSetKernelArg(ihb_final_k_, 3, sizeof(cl_mem), &results_[0]); clSetKernelArg(ihb_final_k_, 4, sizeof(cl_mem), &count_[0]);
        if (!launch(ihb_final_k_, hashes, wg1)) return;
        clFinish(queue_);
        uint32_t n = 0; clEnqueueReadBuffer(queue_, count_[0], CL_TRUE, 0, 4, &n, 0, nullptr, nullptr);
        if (n > max_results_) n = max_results_;
        std::vector<IhResult> rr(n);
        if (n) clEnqueueReadBuffer(queue_, results_[0], CL_TRUE, 0, n * sizeof(IhResult), rr.data(), 0, nullptr, nullptr);
        for (uint32_t i = 0; i < n; i++) {
            Result out; out.nonce = ((uint64_t)rr[i].nonce_hi << 32) | rr[i].nonce_lo;
            for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, rr[i].mix[w]); put_le32(out.digest + 4 * w, rr[i].digest[w]); }
            on_result(out);
        }
        BatchStats stt; stt.hashes = hashes; stt.wall_ms = now_ms() - t0; stt.gpu_ms = stt.wall_ms;   // the pipeline's wall is its cost
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = hashes;
        on_progress(stt);
        done += hashes;
        if (!prepare_ready()) ih_warm_step(ih_slice_);
    }
}

void ClEngineX::run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                        const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    if (!cand_ || !search_k_) return;
    if (cand_->kind == Kind::igneumhash && ih_.version == 15 && r0f_static() && ih_.period_blocks && job.height / ih_.period_blocks != r0d_period_) {   // the static form: a new period is a new programme in the source
        Refusal rr; r0d_height_ = job.height; r0d_from_job_ = true;
        for (cl_kernel *kk : { &search_k_, &dag_k_ }) if (*kk) { clReleaseKernel(*kk); *kk = nullptr; }
        if (prog_) { clReleaseProgram(prog_); prog_ = nullptr; }
        if (!build_programs(rr)) { fprintf(stderr, "opencl: the version 15 period programme rebuild refused (%s): %s\n", rr.code.c_str(), rr.explain.c_str()); return; }
    }
    if (cand_->kind == Kind::igneumhash && (ih_.version == 15 || ih_.version == 17) && (!r0f_key_ || (ih_.period_blocks ? job.height / ih_.period_blocks : 0) != r0f_period_)) { if (!r0f_fill_period(job.height)) { fprintf(stderr, "opencl: the version 15 period key could not be set\n"); return; } }   // the key follows the job's period, whatever the open height (the vectors cross epochs)
    else if (cand_->kind == Kind::igneumhash && ih_.version >= 9 && ih_.period_blocks && job.height / ih_.period_blocks != r0d_period_) {   // versions 9 to 14: a new period is a new programme (and a new shared table under version 12's shared form)   // R0D: a new period is a new programme, so a new kernel
        Refusal rr; r0d_height_ = job.height; r0d_from_job_ = true;
        for (cl_kernel *kk : { &search_k_, &dag_k_ }) if (*kk) { clReleaseKernel(*kk); *kk = nullptr; }
        if (prog_) { clReleaseProgram(prog_); prog_ = nullptr; }
        if (!build_programs(rr)) { fprintf(stderr, "opencl: R0D period programme rebuild refused (%s): %s\n", rr.code.c_str(), rr.explain.c_str()); return; }
    }
    if (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) { ih_run_job(job, on_result, on_progress, should_stop); return; }
    Refusal r;
    if (cand_->kind == Kind::autolykos && job.height != height_) {
        if (next_dataset_ && next_height_ == job.height) { warm_finish(); if (dataset_) { clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_; } dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0; }
        else {
            if (next_dataset_ && next_height_ != job.height + 1) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0; next_height_ = ~0ull; }
            if (dataset_) { clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_; } dataset_ = nullptr; dataset_bytes_ = 0; double ms = 0;
            warm_start(job.height + 1);
            if (!build_autolykos_table(job.height, dataset_, dataset_bytes_, ms, r, &should_stop)) { fprintf(stderr, "opencl: table for height %llu: %s\n", (unsigned long long)job.height, r.explain.c_str()); return; }
        }
        height_ = job.height;
    }
    if (cand_->kind == Kind::autolykos) warm_start(job.height + 1);
    const bool fh = cand_->kind == Kind::fishhash;
    uint64_t done = 0; int slot = 0;
    cl_event inflight[2] = {nullptr, nullptr};
    uint32_t inflight_hashes[2] = {0, 0}; uint64_t inflight_nonce[2] = {0, 0}; double wall_start[2] = {0, 0};
    const uint32_t zero = 0;
    std::vector<std::pair<uint64_t, uint32_t>> redo;

    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) -> bool {
        uint8_t raw[sizeof(AlJob)]; std::memset(raw, 0, sizeof raw);
        if (fh) { FhJob *gj = (FhJob *)raw; for (int i = 0; i < 8; i++) { gj->header[i] = le32(job.header + 4 * i); gj->target[i] = be32(job.target + 4 * i); } gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->items1024 = (uint32_t)(dataset_bytes_ / 128); gj->hashes = hashes; gj->max_results = max_results_; }
        else { AlJob *gj = (AlJob *)raw; std::memcpy(gj->header, job.header, 32); std::memcpy(gj->target, job.target, 32); gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->N = (uint32_t)(dataset_bytes_ / 32); gj->hashes = hashes; gj->max_results = max_results_; }
        clEnqueueWriteBuffer(queue_, job_buf_[s], CL_TRUE, 0, sizeof raw, raw, 0, nullptr, nullptr);
        clEnqueueWriteBuffer(queue_, count_[s], CL_TRUE, 0, 4, &zero, 0, nullptr, nullptr);
        clSetKernelArg(search_k_, 0, sizeof(cl_mem), &job_buf_[s]); clSetKernelArg(search_k_, 1, sizeof(cl_mem), &dataset_);
        clSetKernelArg(search_k_, 2, sizeof(cl_mem), &results_[s]); clSetKernelArg(search_k_, 3, sizeof(cl_mem), &count_[s]);
        uint32_t wg = work_group();
        uint32_t lanes = fh ? fh_lanes_ : 1u;
        if (fh && fh_lanes_ == 8) clSetKernelArg(search_k_, 4, wg * 4, nullptr);
        size_t total = ((uint64_t)hashes * lanes + wg - 1) / wg * wg, local = wg;
        wall_start[s] = now_ms();
        cl_event ev = nullptr;
        if (clEnqueueNDRangeKernel(queue_, search_k_, 1, nullptr, &total, &local, 0, nullptr, &ev) != CL_SUCCESS) return false;
        clFlush(queue_);
        inflight[s] = ev; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
        return true;
    };
    auto collect = [&](int s) {
        if (!inflight[s]) return;
        clWaitForEvents(1, &inflight[s]);
        cl_ulong t0 = 0, t1 = 0;
        clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_START, sizeof t0, &t0, nullptr); clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_END, sizeof t1, &t1, nullptr);
        BatchStats stt; stt.hashes = inflight_hashes[s]; stt.gpu_ms = (double)(t1 - t0) / 1e6; stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        clReleaseEvent(inflight[s]); inflight[s] = nullptr;
        uint32_t n = 0; clEnqueueReadBuffer(queue_, count_[s], CL_TRUE, 0, 4, &n, 0, nullptr, nullptr);
        if (n > max_results_) { fprintf(stderr, "opencl: %u shares exceed the %u-entry buffer; re-running in halves\n", n, max_results_); redo.push_back({inflight_nonce[s], inflight_hashes[s]}); return; }
        std::vector<uint8_t> rr(n * sizeof(AlResult));
        if (n) clEnqueueReadBuffer(queue_, results_[s], CL_TRUE, 0, n * (fh ? sizeof(FhResult) : sizeof(AlResult)), rr.data(), 0, nullptr, nullptr);
        for (uint32_t i = 0; i < n; i++) {
            Result out;
            if (fh) { const FhResult *x = (const FhResult *)rr.data() + i; out.nonce = ((uint64_t)x->nonce_hi << 32) | x->nonce_lo; for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, x->mix[w]); put_le32(out.digest + 4 * w, x->digest[w]); } }
            else { const AlResult *x = (const AlResult *)rr.data() + i; out.nonce = ((uint64_t)x->nonce_hi << 32) | x->nonce_lo; std::memcpy(out.mix, x->mix, 32); std::memcpy(out.digest, x->digest, 32); }
            on_result(out);
        }
        on_progress(stt);
    };
    auto drain_redo = [&]() {
        collect(0); collect(1);
        while (!redo.empty()) { auto [n0, h0] = redo.back(); redo.pop_back(); if (h0 <= 1) continue; uint32_t a = h0 / 2, b = h0 - a; if (submit(0, n0, a)) collect(0); if (submit(0, n0 + a, b)) collect(0); }
    };
    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (!redo.empty()) drain_redo();
        uint32_t want = hashes_for_duration();
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;
        collect(slot);
        if (!submit(slot, job.nonce_start + done, hashes)) break;
        if (cand_->kind == Kind::autolykos) warm_step(1u << 18);
        done += hashes; slot ^= 1;
        if (knobs_.duty < 1.0 && last_gpu_ms_ > 0) std::this_thread::sleep_for(std::chrono::microseconds((long long)(last_gpu_ms_ * (1.0 - knobs_.duty) / knobs_.duty * 1000.0)));
    }
    drain_redo();
}

}  // namespace igm
