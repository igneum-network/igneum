// engine.mm: see engine.h. Source only; not built, not run.
#import "engine.h"
#import <Foundation/Foundation.h>
#include "ignpow_ext.h"
#include "progpow_gen.h"
#include "refbridge.h"
#include "kernel_source.h"
#include <cstdio>
#include <chrono>
#include <cstring>
#include <fstream>
#include <sstream>
#include <thread>
#include <utility>
#include <vector>

namespace igm {
namespace {

std::string read_file(const std::string &path) { std::ifstream f(path, std::ios::binary); std::stringstream ss; ss << f.rdbuf(); return ss.str(); }

// must match igm_job in igm_progpow.metal
struct GpuJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t dag_entries; uint32_t hashes; uint32_t max_results; uint32_t pad_first[15]; uint32_t pad_final[9]; };
struct GpuResult { uint32_t nonce_lo, nonce_hi; uint32_t mix[8]; uint32_t digest[8]; };
struct GpuDagBatch { uint32_t first_item, count, pad0, pad1; };

inline uint32_t le32(const uint8_t *p) { return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24); }
inline uint32_t be32(const uint8_t *p) { return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | (uint32_t)p[3]; }
inline void put_le32(uint8_t *p, uint32_t v) { p[0] = v & 255; p[1] = (v >> 8) & 255; p[2] = (v >> 16) & 255; p[3] = (v >> 24) & 255; }
double now_ms() { using namespace std::chrono; return duration<double, std::milli>(steady_clock::now().time_since_epoch()).count(); }
std::string mb(uint64_t b) { return std::to_string(b >> 20) + " MB"; }

}  // namespace

MetalEngine::MetalEngine(Device &dev, const std::string &shader_dir) : dev_(dev), shader_dir_(shader_dir) {
    base_source_ = kernel_source(shader_dir_, "igm_progpow.metal");
    dag_source_ = kernel_source(shader_dir_, "igm_dag.metal");
    queue_ = [dev_.metal() newCommandQueue];
}

MetalEngine::~MetalEngine() { close(); }

Json MetalEngine::device_json() const {
    DeviceInfo i = dev_.info();
    HostMemory hm = dev_.host_memory();
    return Json::object().set("index", 0).set("backend", "metal").set("name", i.name).set("vendor", "apple").set("id", i.registry_id)
        .set("memory_total", (unsigned long long)i.physical_memory).set("memory_free", (unsigned long long)hm.available_bytes())
        .set("compute", i.compute).set("driver", "macOS " + i.macos).set("fingerprint", i.fingerprint).set("identity", i.identity).set("intrinsic_id", i.platform_serial.empty() ? i.registry_id : i.platform_serial)
        .set("unified_memory", i.unified_memory).set("recommended_working_set", (unsigned long long)i.recommended_working_set)
        .set("current_allocated", (unsigned long long)i.current_allocated).set("model", i.model_identifier).set("fanless", i.fanless);
}

Json MetalEngine::knob_decls() const {
    return Json::array()
        .push(Json::object().set("name", "threadgroup").set("min", 1).set("max", 32).set("step", 1).set("default", 4))
        .push(Json::object().set("name", "batch_ms").set("min", 10).set("max", 500).set("step", 10).set("default", 50))
        .push(Json::object().set("name", "duty").set("min", 0.25).set("max", 1.0).set("step", 0.25).set("default", 1.0))
        .push(Json::object().set("name", "working_set_mb").set("min", 0).set("max", 65536).set("step", 256).set("default", 0));
}

uint64_t MetalEngine::allocated_bytes() const {
    uint64_t b = 0;
    for (id<MTLBuffer> x : { dataset_, next_dataset_, light_, job_buf_[0], job_buf_[1], results_[0], results_[1], count_[0], count_[1], fault_ }) if (x) b += [x allocatedSize];
    return b;
}

void MetalEngine::telemetry(Json &t) const {
    // Reported: thermal_pressure (ProcessInfo.thermalState), on_battery (IOKit power
    // source), low_power_mode, memory_pressure, asleep, the effective duty, the GPU
    // allocation. Not reported, so omitted: fan_pct, util_pct, power_w, temp_c, clocks
    // (no public API without root; the wall meter is the power measure).
    SystemState s = dev_.state();
    Json throttle = Json::array();
    if (s.thermal == Thermal::serious || s.thermal == Thermal::critical) throttle.push("thermal");
    if (s.low_power_mode) throttle.push("power");
    const char *tp = s.thermal == Thermal::nominal ? "nominal" : s.thermal == Thermal::fair ? "fair" : s.thermal == Thermal::serious ? "serious" : "critical";
    t.set("throttle", throttle).set("thermal_pressure", tp).set("on_battery", s.power == Power::battery)
     .set("low_power_mode", s.low_power_mode).set("memory_pressure", s.memory_pressure).set("asleep", s.asleep)
     .set("duty_effective", duty_for_thermal()).set("gpu_allocated_bytes", (unsigned long long)dev_.info().current_allocated);
}

Json MetalEngine::code_json() const {
    Json c = Json::object();
    if (!search_pso_) return c;
    c.set("registers", "not exposed by Metal at run time (the compiler report is a build-time artefact)").set("threadgroup_memory_bytes", (double)[search_pso_ staticThreadgroupMemoryLength])
     .set("max_threads_per_threadgroup", (double)[search_pso_ maxTotalThreadsPerThreadgroup]).set("simd_width", (double)[search_pso_ threadExecutionWidth]).set("workgroup", (double)(knobs_.threadgroup * [search_pso_ threadExecutionWidth]))
     .set("bytes_read_per_nonce", (double)bytes_read_per_hash(Kind::progpow));
    return c;
}

Json MetalEngine::budget_json() const {
    DeviceInfo i = dev_.info();
    HostMemory hm = dev_.host_memory();
    uint64_t next_bytes = 0; int st = 0;
    if (cand_ && !algo_.empty()) {
        uint64_t e = ignpow_epoch_of(algo_.c_str(), params_c(), height_, &st);
        uint64_t h = height_;
        while (st == IGNPOW_OK && ignpow_epoch_of(algo_.c_str(), params_c(), h, &st) == e && h < height_ + 200000) h += 500;
        if (st == IGNPOW_OK) next_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), h, &st);
    }
    BudgetDecision b = dev_.budget(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0);
    return Json::object().set("total_bytes", std::to_string(i.physical_memory)).set("free_bytes", std::to_string(hm.available_bytes()))
        .set("fit_cold_bytes", std::to_string(allocated_bytes())).set("fit_warm_bytes", std::to_string(allocated_bytes() + next_bytes))
        .set("can_warm", b.warm_fits).set("working_set_limit_bytes", std::to_string(b.limit)).set("recommended_max_working_set", std::to_string(i.recommended_working_set))
        .set("headroom_bytes", std::to_string(b.headroom)).set("unified_memory", i.unified_memory);
}

bool MetalEngine::paused() const {
    SystemState s = dev_.state();
    if (s.asleep) return true;
    if (s.thermal == Thermal::critical) return true;
    if (s.power == Power::battery && !battery_allowed_) return true;
    if (s.low_power_mode && !lpm_allowed_) return true;
    if (s.memory_pressure >= 2) return true;
    return false;
}

double MetalEngine::duty_for_thermal() const {
    // the thermal ladder of DESIGN.md 3.6 on top of the user's duty knob
    SystemState s = dev_.state();
    double d = knobs_.duty;
    if (s.thermal == Thermal::fair) d = d > 0.75 ? 0.75 : d;
    else if (s.thermal == Thermal::serious) d = d > 0.5 ? 0.5 : d;
    return d < 0.25 ? 0.25 : d;
}

std::string MetalEngine::describe() const {
    return "metal width " + std::to_string(simd_width_) + " lane path " + (lane_shuffle_ ? "shuffle" : "barrier") + " threadgroup " + std::to_string(knobs_.threadgroup);
}

bool MetalEngine::build_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, Refusal &r) {
    int st = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a light context for height " + std::to_string(height) + " (status " + std::to_string(st) + ")"; return false; }
    bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference has no dataset size for height " + std::to_string(height); return false; }
    if (bytes > [dev_.metal() maxBufferLength]) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " exceeds the device's largest buffer"; return false; }

    const void *light = nullptr; uint64_t light_bytes = 0; uint32_t light_item = 0;
    if (ignpow_ctx_light_cache(ctx, &light, &light_bytes, &light_item) != IGNPOW_OK || !light || light_bytes == 0) { r.code = "unsupported"; r.explain = "reference exposes no light cache for this candidate"; return false; }
    if (light_item != cand_->cache_item_bytes) { r.code = "unsupported"; r.explain = "light cache item size " + std::to_string(light_item) + " differs from the kernel's " + std::to_string(cand_->cache_item_bytes); return false; }

    light_ = [dev_.metal() newBufferWithBytes:light length:light_bytes options:MTLResourceStorageModeShared];
    if (!light_) { r.code = "memory"; r.explain = "light cache allocation of " + mb(light_bytes) + " failed"; return false; }
    dataset = [dev_.metal() newBufferWithLength:bytes options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
    if (!dataset) { r.code = "memory"; r.explain = "dataset allocation of " + mb(bytes) + " failed"; r.need_bytes = bytes; return false; }

    if (!dag_pso_) {
        NSError *err = nil;
        id<MTLLibrary> lib = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:dag_source_.c_str()] options:[[MTLCompileOptions alloc] init] error:&err];
        if (!lib) { r.code = "driver"; r.explain = std::string("dag shader compile: ") + [[err localizedDescription] UTF8String]; return false; }
        MTLFunctionConstantValues *fc = [[MTLFunctionConstantValues alloc] init];
        uint32_t parents = cand_->dataset_parents, nodes = (uint32_t)(light_bytes / cand_->cache_item_bytes);
        [fc setConstantValue:&parents type:MTLDataTypeUInt atIndex:0];
        [fc setConstantValue:&nodes type:MTLDataTypeUInt atIndex:1];
        id<MTLFunction> fn = [lib newFunctionWithName:@"igm_dag_generate" constantValues:fc error:&err];
        if (!fn) { r.code = "driver"; r.explain = "dag function specialisation failed"; return false; }
        dag_pso_ = [dev_.metal() newComputePipelineStateWithFunction:fn error:&err];
        if (!dag_pso_) { r.code = "driver"; r.explain = std::string("dag pipeline: ") + [[err localizedDescription] UTF8String]; return false; }
    }

    uint32_t items = (uint32_t)(bytes / cand_->item_bytes);
    const uint32_t per = 1u << 20;   // bounded batches: a cancel or a thermal pause lands within one
    for (uint32_t first = 0; first < items; first += per) {
        GpuDagBatch b{first, items - first < per ? items - first : per, 0, 0};
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:dag_pso_];
        [enc setBytes:&b length:sizeof(b) atIndex:0];
        [enc setBuffer:light_ offset:0 atIndex:1];
        [enc setBuffer:dataset offset:0 atIndex:2];
        [enc dispatchThreads:MTLSizeMake(b.count, 1, 1) threadsPerThreadgroup:MTLSizeMake([dag_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
        [enc endEncoding];
        [cb commit];
        [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { r.code = "driver"; r.explain = "dag generation command buffer failed"; return false; }
    }

    // the CPU second opinion on a sample of items before the dataset is trusted
    uint8_t cpu[64];
    const uint64_t sample[] = { 0, 1, 2, 3, items / 2, (uint64_t)items - 1, 12345 % items, 0xABCDEF % items };
    static bool warned = false;
    for (uint64_t idx : sample) {
        int rc = ref_dataset_item(ctx, idx, cpu, sizeof cpu);
        if (rc == IGNPOW_E_ARG) { if (!warned) { fprintf(stderr, "dataset: no CPU item accessor in the ABI yet; items not sampled, the vector comparison is the check\n"); warned = true; } break; }
        if (rc != IGNPOW_OK) { r.code = "driver"; r.explain = "reference dataset item " + std::to_string(idx) + " failed (" + std::to_string(rc) + ")"; return false; }
        if (std::memcmp(cpu, (const uint8_t *)[dataset contents] + idx * cand_->item_bytes, cand_->item_bytes) != 0) { r.code = "driver"; r.explain = "dataset item " + std::to_string(idx) + " differs from the CPU reference"; return false; }
    }
    return true;
}

bool MetalEngine::build_pipeline(uint64_t prog_seed, std::string &explain) {
    if (prog_seed == prog_seed_ && search_pso_) return true;
    std::string src = splice_period_program(base_source_, generate_period_program(*cand_, prog_seed, Dialect::msl));
    NSError *err = nil;
    MTLCompileOptions *opts = [[MTLCompileOptions alloc] init];
    opts.fastMathEnabled = NO;
    id<MTLLibrary> lib = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:opts error:&err];
    if (!lib) { explain = std::string("shader compile: ") + [[err localizedDescription] UTF8String]; return false; }

    // probe for the width, then specialise with the lane path that width permits
    MTLFunctionConstantValues *fc = [[MTLFunctionConstantValues alloc] init];
    uint32_t lanes = cand_->lanes, regs = cand_->regs, loads = cand_->dag_loads, cache_words = cand_->cache_bytes / 4, rounds = cand_->rounds;
    bool shuffle = false; uint32_t width = 32;
    [fc setConstantValue:&lanes type:MTLDataTypeUInt atIndex:0];
    [fc setConstantValue:&regs type:MTLDataTypeUInt atIndex:1];
    [fc setConstantValue:&loads type:MTLDataTypeUInt atIndex:2];
    [fc setConstantValue:&cache_words type:MTLDataTypeUInt atIndex:3];
    [fc setConstantValue:&rounds type:MTLDataTypeUInt atIndex:4];
    [fc setConstantValue:&shuffle type:MTLDataTypeBool atIndex:5];
    [fc setConstantValue:&width type:MTLDataTypeUInt atIndex:6];
    id<MTLFunction> probe = [lib newFunctionWithName:@"igm_progpow_search" constantValues:fc error:&err];
    if (!probe) { explain = "function specialisation failed"; return false; }
    id<MTLComputePipelineState> probe_pso = [dev_.metal() newComputePipelineStateWithFunction:probe error:&err];
    if (!probe_pso) { explain = std::string("probe pipeline: ") + [[err localizedDescription] UTF8String]; return false; }
    width = (uint32_t)[probe_pso threadExecutionWidth];
    shuffle = (width >= lanes) && (width % lanes == 0);
    [fc setConstantValue:&shuffle type:MTLDataTypeBool atIndex:5];
    [fc setConstantValue:&width type:MTLDataTypeUInt atIndex:6];

    MTLComputePipelineDescriptor *pd = [[MTLComputePipelineDescriptor alloc] init];
    id<MTLFunction> fn = [lib newFunctionWithName:@"igm_progpow_search" constantValues:fc error:&err];
    if (!fn) { explain = "function specialisation (final) failed"; return false; }
    pd.computeFunction = fn;
    pd.maxTotalThreadsPerThreadgroup = knobs_.threadgroup * width;   // R25: let the compiler budget registers for the known size
    pd.threadGroupSizeIsMultipleOfThreadExecutionWidth = YES;
    if (archive_) pd.binaryArchives = @[archive_];
    id<MTLComputePipelineState> pso = [dev_.metal() newComputePipelineStateWithDescriptor:pd options:0 reflection:nil error:&err];
    if (!pso) { explain = std::string("search pipeline: ") + [[err localizedDescription] UTF8String]; return false; }
    if ([pso threadExecutionWidth] != width) { explain = "thread execution width changed between probe and pipeline"; return false; }
    if (archive_) [archive_ addComputePipelineFunctionsWithDescriptor:pd error:nil];
    search_pso_ = pso; simd_width_ = width; lane_shuffle_ = shuffle; prog_seed_ = prog_seed;
    return true;
}

bool MetalEngine::open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) {
    close();
    algo_ = algo; knobs_ = k; params_ = k.params; height_ = height;
    cand_ = find_candidate(algo.c_str());
    if (!cand_) { r.code = "unsupported"; r.explain = "no kernel family for " + algo + " in this worker"; return false; }
    int st = 0;
    uint64_t bytes = ignpow_dataset_bytes(algo.c_str(), params_c(), height, &st);
    uint64_t cache = ignpow_cache_bytes(algo.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference: height " + std::to_string(height) + " outside the schedule (status " + std::to_string(st) + ")"; return false; }
    uint64_t overhead = cache + 2 * (sizeof(GpuJob) + max_results_ * sizeof(GpuResult) + 4096) + (64ull << 20);
    uint64_t working_set = bytes + overhead;
    uint64_t next_bytes = 0; int st2 = 0;
    uint64_t next_height = height + 1; { uint64_t e = ignpow_epoch_of(algo.c_str(), params_c(), height, &st2); while (st2 == IGNPOW_OK && ignpow_epoch_of(algo.c_str(), params_c(), next_height, &st2) == e && next_height < height + 100000) next_height += 1000; }
    if (st2 == IGNPOW_OK) next_bytes = ignpow_dataset_bytes(algo.c_str(), params_c(), next_height, &st2);
    uint64_t cap = supervisor_budget;   // the working_set_mb knob caps the limit, as a supervisor budget does
    if (k.working_set_mb && (!cap || (k.working_set_mb << 20) < cap)) cap = k.working_set_mb << 20;
    BudgetDecision b = dev_.budget_settled(working_set, next_bytes, cap, 0, 3000);
    if (!b.fits) { r.code = "memory"; r.explain = "unsafe allocation refused: " + b.explain; r.need_bytes = working_set; r.have_bytes = b.limit; return false; }

    archive_ = [dev_.metal() newBinaryArchiveWithDescriptor:[[MTLBinaryArchiveDescriptor alloc] init] error:nil];
    if (!build_dataset(height, ctx_, dataset_, dataset_bytes_, r)) { close(); return false; }
    for (int i = 0; i < 2; i++) {
        job_buf_[i] = [dev_.metal() newBufferWithLength:sizeof(GpuJob) options:MTLResourceStorageModeShared];
        results_[i] = [dev_.metal() newBufferWithLength:max_results_ * sizeof(GpuResult) options:MTLResourceStorageModeShared];
        count_[i] = [dev_.metal() newBufferWithLength:4 options:MTLResourceStorageModeShared];
    }
    fault_ = [dev_.metal() newBufferWithLength:4 options:MTLResourceStorageModeShared];
    uint64_t seed = 0; bool from_abi = false;
    if (!ref_program_seed(*cand_, height, seed, from_abi)) { r.code = "unsupported"; r.explain = "no program seed for " + algo; close(); return false; }
    std::string e;
    if (!build_pipeline(seed, e)) { r.code = "driver"; r.explain = e; close(); return false; }
    return true;
}

bool MetalEngine::prepare(uint64_t height, Refusal &r, const std::string &params) {
    (void)params;
    int st = 0;
    uint64_t next_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK) { r.code = "unsupported"; r.explain = "no dataset size for the next height"; return false; }
    BudgetDecision b = dev_.budget_settled(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0, 1000);
    if (!b.warm_fits) { r.code = "memory"; r.explain = "cold transition: " + b.explain; r.need_bytes = allocated_bytes() + next_bytes; r.have_bytes = b.limit; return false; }
    id<MTLBuffer> nd = nil; uint64_t nb = 0; ignpow_ctx *nc = nullptr;
    if (!build_dataset(height, nc, nd, nb, r)) { if (nc) ignpow_ctx_free(nc); return false; }
    if (next_ctx_) ignpow_ctx_free(next_ctx_);
    next_ctx_ = nc; next_dataset_ = nd; next_dataset_bytes_ = nb;
    return true;
}

void MetalEngine::close() {
    search_pso_ = nil; dag_pso_ = nil; archive_ = nil;
    dataset_ = nil; next_dataset_ = nil; light_ = nil; fault_ = nil;
    for (int i = 0; i < 2; i++) { job_buf_[i] = nil; results_[i] = nil; count_[i] = nil; }
    if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
    if (next_ctx_) { ignpow_ctx_free(next_ctx_); next_ctx_ = nullptr; }
    dataset_bytes_ = 0; next_dataset_bytes_ = 0; prog_seed_ = ~0ull;
}

void MetalEngine::set_knobs(const Knobs &k) {
    bool geometry_changed = k.threadgroup != knobs_.threadgroup;
    knobs_ = k;
    if (geometry_changed && search_pso_) { uint64_t s = prog_seed_; prog_seed_ = ~0ull; std::string e; build_pipeline(s, e); }
}

uint32_t MetalEngine::hashes_for_duration() const {
    uint32_t threads_per_tg = knobs_.threadgroup * simd_width_;
    uint32_t hashes_per_tg = threads_per_tg / cand_->lanes;
    uint32_t tgs = 64;
    if (last_gpu_ms_ > 0 && last_hashes_ > 0) {
        double want = (double)last_hashes_ * (double)knobs_.batch_ms / last_gpu_ms_;
        tgs = (uint32_t)(want / hashes_per_tg);
        if (tgs < 1) tgs = 1;
        if (tgs > (1u << 20)) tgs = 1u << 20;
    }
    return tgs * hashes_per_tg;
}

void MetalEngine::run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                          const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    if (!cand_ || !ctx_) return;
    int st = 0;
    uint64_t job_epoch = ignpow_epoch_of(algo_.c_str(), params_c(), job.height, &st);
    if (st != IGNPOW_OK) return;
    if (job_epoch != ignpow_ctx_epoch(ctx_)) {
        if (next_ctx_ && ignpow_ctx_epoch(next_ctx_) == job_epoch) {
            ignpow_ctx_free(ctx_); ctx_ = next_ctx_; next_ctx_ = nullptr;
            dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nil; next_dataset_bytes_ = 0;
        } else {
            Refusal r; id<MTLBuffer> nd = nil; uint64_t nb = 0; ignpow_ctx *nc = nullptr;
            dataset_ = nil; dataset_bytes_ = 0;   // cold transition: drain, release, rebuild
            if (!build_dataset(job.height, nc, nd, nb, r)) { if (nc) ignpow_ctx_free(nc); return; }
            ignpow_ctx_free(ctx_); ctx_ = nc; dataset_ = nd; dataset_bytes_ = nb;
        }
        height_ = job.height;
    }
    uint64_t seed = 0; bool from_abi = false; std::string e;
    if (!ref_program_seed(*cand_, job.height, seed, from_abi)) return;
    if (!build_pipeline(seed, e)) { fprintf(stderr, "metal: %s\n", e.c_str()); return; }

    GpuJob base{};
    for (int i = 0; i < 8; i++) { base.header[i] = le32(job.header + 4 * i); base.target[i] = be32(job.target + 4 * i); }
    base.dag_entries = (uint32_t)(dataset_bytes_ / cand_->entry_bytes);
    base.max_results = max_results_;
    std::memcpy(base.pad_first, cand_->pad_first, sizeof base.pad_first);
    std::memcpy(base.pad_final, cand_->pad_final, sizeof base.pad_final);

    uint64_t done = 0; int slot = 0;
    id<MTLCommandBuffer> inflight[2] = {nil, nil};
    uint32_t inflight_hashes[2] = {0, 0};
    uint64_t inflight_nonce[2] = {0, 0};
    double wall_start[2] = {0, 0};
    std::vector<std::pair<uint64_t, uint32_t>> redo;   // ranges whose results overflowed the buffer

    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) {
        GpuJob *gj = (GpuJob *)[job_buf_[s] contents];
        *gj = base;
        gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->hashes = hashes;
        ((uint32_t *)[count_[s] contents])[0] = 0;
        ((uint32_t *)[fault_ contents])[0] = 0;
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:search_pso_];
        [enc setBuffer:job_buf_[s] offset:0 atIndex:0];
        [enc setBuffer:dataset_ offset:0 atIndex:1];
        [enc setBuffer:results_[s] offset:0 atIndex:2];
        [enc setBuffer:count_[s] offset:0 atIndex:3];
        [enc setBuffer:fault_ offset:0 atIndex:4];
        uint32_t threads_per_tg = knobs_.threadgroup * simd_width_;
        uint32_t groups_per_tg = threads_per_tg / cand_->lanes;
        [enc setThreadgroupMemoryLength:(cand_->cache_bytes / 4 + groups_per_tg + groups_per_tg * cand_->lanes) * 4 atIndex:0];
        uint64_t total_threads = (uint64_t)hashes * cand_->lanes;
        uint64_t tgs = (total_threads + threads_per_tg - 1) / threads_per_tg;
        [enc dispatchThreadgroups:MTLSizeMake(tgs, 1, 1) threadsPerThreadgroup:MTLSizeMake(threads_per_tg, 1, 1)];
        [enc endEncoding];
        wall_start[s] = now_ms();
        [cb commit];
        inflight[s] = cb; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
    };

    // Collects one slot. A batch whose share count exceeds the buffer is NOT reported:
    // its range goes to `redo` and is run again in halves, so no share is ever lost.
    auto collect = [&](int s) {
        id<MTLCommandBuffer> cb = inflight[s];
        if (!cb) return;
        [cb waitUntilCompleted];
        BatchStats stt; stt.hashes = inflight_hashes[s];
        stt.gpu_ms = ([cb GPUEndTime] - [cb GPUStartTime]) * 1000.0;
        stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        inflight[s] = nil;
        if ([cb status] != MTLCommandBufferStatusCompleted || ((uint32_t *)[fault_ contents])[0] != 0) {
            fprintf(stderr, "metal: batch at nonce %llu (%u hashes) failed: status %ld fault %u\n", (unsigned long long)inflight_nonce[s], inflight_hashes[s], (long)[cb status], ((uint32_t *)[fault_ contents])[0]);
            return;
        }
        uint32_t n = ((uint32_t *)[count_[s] contents])[0];
        if (n > max_results_) {
            fprintf(stderr, "metal: %u shares in a batch of %u hashes exceed the %u-entry buffer; re-running in halves\n", n, inflight_hashes[s], max_results_);
            redo.push_back({inflight_nonce[s], inflight_hashes[s]});
            return;
        }
        const GpuResult *rr = (const GpuResult *)[results_[s] contents];
        for (uint32_t i = 0; i < n; i++) {
            Result out; out.nonce = ((uint64_t)rr[i].nonce_hi << 32) | rr[i].nonce_lo;
            for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, rr[i].mix[w]); put_le32(out.digest + 4 * w, rr[i].digest[w]); }
            on_result(out);
        }
        on_progress(stt);
    };

    auto drain_redo = [&]() {
        collect(0); collect(1);
        while (!redo.empty()) {
            auto [n0, h0] = redo.back(); redo.pop_back();
            if (h0 <= 1) { fprintf(stderr, "metal: a single hash overflowed the result buffer; impossible, batch dropped as a fault\n"); continue; }
            uint32_t a = h0 / 2, b = h0 - a;
            submit(0, n0, a); collect(0);
            submit(0, n0 + a, b); collect(0);
        }
    };

    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (paused()) { drain_redo(); std::this_thread::sleep_for(std::chrono::milliseconds(100)); continue; }
        if (!redo.empty()) drain_redo();
        uint32_t want = hashes_for_duration();
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;   // tails: every nonce exactly once
        collect(slot);
        submit(slot, job.nonce_start + done, hashes);
        done += hashes; slot ^= 1;
        double duty = duty_for_thermal();
        if (duty < 1.0 && last_gpu_ms_ > 0) std::this_thread::sleep_for(std::chrono::microseconds((long long)(last_gpu_ms_ * (1.0 - duty) / duty * 1000.0)));
    }
    drain_redo();
}

}  // namespace igm
