// engine_x.mm: see engine_x.h. Source only on this Mac; built on the mini.
#import "engine_x.h"
#include "texture_probe.h"
#import <Foundation/Foundation.h>
#include "igneumhash.h"   // ignh_test_sk (version 8 bit 10)
#include "refbridge.h"
#include "r0d_gen.h"
#include "kernel_source.h"
#include "sha256.h"
#include <sys/stat.h>
#include <mach/mach.h>
#include <mach/task_info.h>
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

// must match the wrappers
struct FhJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t items1024; uint32_t hashes; uint32_t max_results; };
struct FhResult { uint32_t nonce_lo, nonce_hi; uint32_t mix[8]; uint32_t digest[8]; };
struct AlJob { uint8_t header[32]; uint8_t target[32]; uint32_t nonce_lo, nonce_hi; uint32_t N; uint32_t hashes; uint32_t max_results; };
struct AlResult { uint32_t nonce_lo, nonce_hi; uint8_t mix[32]; uint8_t digest[32]; };
struct TableBatch { uint32_t first, count, height, pad; };
struct DagBatch { uint32_t first_item, count, pad0, pad1; };
struct IhConsts { uint32_t c_hi, c_lo, v; uint32_t p[8]; uint32_t e[8]; uint32_t n_items_mask; uint32_t slice_words; uint32_t slice_lines_mask; uint32_t rounds; uint32_t item_chain; uint32_t n_cache_mask; uint32_t parents; uint32_t version; uint32_t item_words; uint32_t sub_blocks; uint32_t blocks; uint32_t registers; uint32_t features; uint32_t n_items; uint32_t fastrange; uint32_t program_ops; uint32_t fold_every; uint32_t batch_log2;  uint32_t sk[8]; };
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
    k.n_cache_mask = p.n_cache() - 1; k.parents = p.parents; k.version = p.version; k.item_words = p.item_words(); k.sub_blocks = p.sub_blocks(); k.blocks = p.blocks; k.registers = p.registers; k.features = p.features; k.n_items = p.n_items(); k.fastrange = p.fastrange() ? 1u : 0u; k.program_ops = p.program_ops; k.fold_every = p.fold_every; k.batch_log2 = p.keyed_rounds;
    return k;
}

}  // namespace

MetalEngineX::MetalEngineX(Device &dev, const std::string &shader_dir) : dev_(dev), shader_dir_(shader_dir) { queue_ = [dev_.metal() newCommandQueue]; build_queue_ = [dev_.metal() newCommandQueue]; }
MetalEngineX::~MetalEngineX() { close(); }

Json MetalEngineX::device_json() const {
    DeviceInfo i = dev_.info(); HostMemory hm = dev_.host_memory();
    return Json::object().set("index", 0).set("backend", "metal").set("name", i.name).set("vendor", "apple").set("id", i.registry_id)
        .set("memory_total", (unsigned long long)i.physical_memory).set("memory_free", (unsigned long long)hm.available_bytes())
        .set("compute", i.compute).set("driver", "macOS " + i.macos).set("fingerprint", i.fingerprint).set("identity", i.identity).set("intrinsic_id", i.platform_serial.empty() ? i.registry_id : i.platform_serial)
        .set("unified_memory", i.unified_memory).set("recommended_working_set", (unsigned long long)i.recommended_working_set)
        .set("current_allocated", (unsigned long long)i.current_allocated).set("model", i.model_identifier).set("fanless", i.fanless);
}

Json MetalEngineX::knob_decls() const {
    return Json::array()
        .push(Json::object().set("name", "threadgroup").set("min", 1).set("max", 32).set("step", 1).set("default", 8))
        .push(Json::object().set("name", "batch_ms").set("min", 10).set("max", 500).set("step", 10).set("default", 50))
        .push(Json::object().set("name", "duty").set("min", 0.25).set("max", 1.0).set("step", 0.25).set("default", 1.0))
        .push(Json::object().set("name", "duty_hold").set("min", 0).set("max", 1).set("step", 1).set("default", 0))
        .push(Json::object().set("name", "working_set_mb").set("min", 0).set("max", 65536).set("step", 256).set("default", 0));
}

uint64_t MetalEngineX::allocated_bytes() const {
    uint64_t b = 0;
    for (id<MTLBuffer> x : { dataset_, dataset2_, next_dataset_, next_dataset2_, spare_, spare2_, staging_, light_, ih_next_light_, ih_scratch_, ih_st_ }) if (x) b += [x allocatedSize];
    for (int i = 0; i < 8; i++) for (id<MTLBuffer> x : { job_buf_[i], results_[i], count_[i] }) if (x) b += [x allocatedSize];
    return b;
}

void MetalEngineX::wipe_secret() {
    static const uint8_t zeros[sizeof(IhJob) > sizeof(RxJob) ? sizeof(IhJob) : sizeof(RxJob)] = {0};
    for (id<MTLBuffer> b : job_buf_) if (b) raw_write(b, 0, std::min<uint64_t>([b length], sizeof zeros), zeros);
}
bool MetalEngineX::keyed_document() const { return cand_ && cand_->kind == Kind::igneumhash && ih_.keyed_rounds > 0; }
void MetalEngineX::telemetry(Json &t) const {
    SystemState s = dev_.state();
    Json throttle = Json::array();
    if (s.thermal == Thermal::serious || s.thermal == Thermal::critical) throttle.push("thermal");
    if (s.low_power_mode) throttle.push("power");
    const char *tp = s.thermal == Thermal::nominal ? "nominal" : s.thermal == Thermal::fair ? "fair" : s.thermal == Thermal::serious ? "serious" : "critical";
    t.set("throttle", throttle).set("thermal_pressure", tp).set("on_battery", s.power == Power::battery).set("low_power_mode", s.low_power_mode)
     .set("memory_pressure", s.memory_pressure).set("asleep", s.asleep).set("duty_effective", duty_for_thermal()).set("build_in_flight", !prepare_ready()).set("build_progress", ih_next_items_ ? (double)ih_next_cursor_ / ih_next_items_ : 0.0).set("last_swap_wait_ms", last_swap_wait_ms_)
     .set("gpu_allocated_bytes", (unsigned long long)dev_.info().current_allocated).set("last_table_build_ms", last_table_ms_);
    memory_stats(t);
    if (const char *sim = getenv("IGM_SIM_STATE")) t.set("sim_state", std::string(sim));   // a policy-proof row: the state above is simulated, never a machine reading
}

// ---- Orchard OR-0 and OR-6: the allocation forms (the Apple memory seat, 10 Oct 2026) ----
// The process and host memory counters for the transition rows: the physical footprint and resident size of this process
// (task_info TASK_VM_INFO), and the host's cumulative faults, page-ins, page-outs, compressions, decompressions and swap-outs
// with the compressor's current size (host_statistics64). Cumulative counters: the harness takes deltas and peaks.
void MetalEngineX::memory_stats(Json &t) const {
    task_vm_info_data_t vi; mach_msg_type_number_t n = TASK_VM_INFO_COUNT;
    if (task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&vi, &n) == KERN_SUCCESS) t.set("phys_footprint_bytes", (unsigned long long)vi.phys_footprint).set("resident_bytes", (unsigned long long)vi.resident_size);
    vm_statistics64_data_t vm; mach_msg_type_number_t c = HOST_VM_INFO64_COUNT; vm_size_t page = 0; host_page_size(mach_host_self(), &page);
    if (host_statistics64(mach_host_self(), HOST_VM_INFO64, (host_info64_t)&vm, &c) == KERN_SUCCESS)
        t.set("vm_faults", (unsigned long long)vm.faults).set("vm_pageins", (unsigned long long)vm.pageins).set("vm_pageouts", (unsigned long long)vm.pageouts)
         .set("vm_compressions", (unsigned long long)vm.compressions).set("vm_decompressions", (unsigned long long)vm.decompressions).set("vm_swapouts", (unsigned long long)vm.swapouts).set("vm_swapins", (unsigned long long)vm.swapins)
         .set("compressor_bytes", (unsigned long long)vm.compressor_page_count * page).set("host_free_bytes", (unsigned long long)vm.free_count * page);
    t.set("guard_mb", (double)(guard_bytes_ >> 20)).set("shift_bytes", (double)(pay_off_ - guard_bytes_)).set("storage", knobs_.storage.empty() ? "shared" : knobs_.storage).set("pretouch", knobs_.pretouch.empty() ? "gpu" : knobs_.pretouch).set("persist", persist_pool() ? "pool" : "reconstruct-purge")
     .set("pool_reuses", (double)pool_reuses_).set("pool_allocs", (double)pool_allocs_).set("purged", (double)purged_);
}

MTLResourceOptions MetalEngineX::dataset_options() const {
    return (knobs_.storage == "private" ? MTLResourceStorageModePrivate : MTLResourceStorageModeShared) | MTLResourceHazardTrackingModeUntracked;
}

void MetalEngineX::gpu_idle() {
    if (build_last_) { [build_last_ waitUntilCompleted]; build_last_ = nil; }
    @autoreleasepool { id<MTLCommandBuffer> cb = [queue_ commandBuffer]; [cb commit]; [cb waitUntilCompleted]; }   // the queue is in order: this completes after everything before it
}

static inline uint32_t guard_canary(uint64_t raw_off) { return (uint32_t)((raw_off * 0x9E3779B97F4A7C15ull) >> 32) ^ 0xC0FFEE11u; }   // one word per 4-byte raw offset

bool MetalEngineX::raw_read(id<MTLBuffer> buf, uint64_t off, uint64_t len, void *dst) {
    if (!buf || off + len > [buf length]) return false;
    if ([buf storageMode] != MTLStorageModePrivate) { std::memcpy(dst, (const uint8_t *)[buf contents] + off, (size_t)len); return true; }
    const uint64_t win = 64ull << 20;
    if (!staging_) { staging_ = [dev_.metal() newBufferWithLength:win options:MTLResourceStorageModeShared]; if (!staging_) return false; }
    for (uint64_t o = 0; o < len; o += win) { @autoreleasepool {
        uint64_t n = len - o < win ? len - o : win;
        id<MTLCommandBuffer> cb = [queue_ commandBuffer]; id<MTLBlitCommandEncoder> bl = [cb blitCommandEncoder];
        [bl copyFromBuffer:buf sourceOffset:off + o toBuffer:staging_ destinationOffset:0 size:n]; [bl endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) return false;
        std::memcpy((uint8_t *)dst + o, [staging_ contents], (size_t)n);
    } }
    return true;
}

bool MetalEngineX::raw_write(id<MTLBuffer> buf, uint64_t off, uint64_t len, const void *src) {
    if (!buf || off + len > [buf length]) return false;
    if ([buf storageMode] != MTLStorageModePrivate) { std::memcpy((uint8_t *)[buf contents] + off, src, (size_t)len); return true; }
    const uint64_t win = 64ull << 20;
    if (!staging_) { staging_ = [dev_.metal() newBufferWithLength:win options:MTLResourceStorageModeShared]; if (!staging_) return false; }
    for (uint64_t o = 0; o < len; o += win) { @autoreleasepool {
        uint64_t n = len - o < win ? len - o : win;
        std::memcpy([staging_ contents], (const uint8_t *)src + o, (size_t)n);
        id<MTLCommandBuffer> cb = [queue_ commandBuffer]; id<MTLBlitCommandEncoder> bl = [cb blitCommandEncoder];
        [bl copyFromBuffer:staging_ sourceOffset:0 toBuffer:buf destinationOffset:off + o size:n]; [bl endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) return false;
    } }
    return true;
}

void MetalEngineX::arm_guards(id<MTLBuffer> buf, uint64_t payload) {
    if (!pay_off_ || !buf) return;
    // the front guard runs to the payload (the shifted bytes included), the back guard follows it
    for (int side = 0; side < 2; side++) {
        const uint64_t raw = side ? pay_off_ + payload : 0, len = side ? guard_bytes_ : pay_off_;
        if (!len) continue;
        std::vector<uint32_t> w((size_t)(len / 4));
        for (size_t i = 0; i < w.size(); i++) w[i] = guard_canary(raw + 4 * i);
        raw_write(buf, raw, len, w.data());
    }
}

bool MetalEngineX::pretouch(id<MTLBuffer> buf, uint64_t payload) {
    if (knobs_.pretouch == "cpu") {
        if ([buf storageMode] == MTLStorageModePrivate) { fprintf(stderr, "metal: pretouch cpu has no meaning on private storage; the GPU's first touch stands\n"); return true; }
        std::memset((uint8_t *)[buf contents] + pay_off_, 0, (size_t)payload); return true;
    }
    if (knobs_.pretouch == "fill") { @autoreleasepool {
        id<MTLCommandBuffer> cb = [queue_ commandBuffer]; id<MTLBlitCommandEncoder> bl = [cb blitCommandEncoder];
        [bl fillBuffer:buf range:NSMakeRange((NSUInteger)pay_off_, (NSUInteger)payload) value:0]; [bl endEncoding]; [cb commit]; [cb waitUntilCompleted];
        return [cb status] == MTLCommandBufferStatusCompleted;
    } }
    return true;   // "" or "gpu": the build kernel's write is the first touch
}

id<MTLBuffer> MetalEngineX::alloc_dataset(id<MTLBuffer> existing, uint64_t payload, const char *name, Refusal &r) {
    guard_bytes_ = (uint64_t)knobs_.guard_mb << 20; pay_off_ = guard_bytes_ + (knobs_.shift_bytes & ~15u);
    const uint64_t need = payload + pay_off_ + guard_bytes_;
    id<MTLBuffer> buf = nil; const char *how = "";
    if (existing && [existing length] >= need && [existing storageMode] == (knobs_.storage == "private" ? MTLStorageModePrivate : MTLStorageModeShared)) { buf = existing; how = "reused in place"; }
    else if (persist_pool()) {
        if (spare_ && [spare_ length] >= need) { buf = spare_; spare_ = nil; how = "from the pool"; pool_reuses_++; }
        else if (spare2_ && [spare2_ length] >= need) { buf = spare2_; spare2_ = nil; how = "from the pool"; pool_reuses_++; }
    }
    if (!buf) {
        if (need > [dev_.metal() maxBufferLength]) { r.code = "memory"; r.explain = std::string(name) + " with guards " + mb(need) + " exceeds the device's largest buffer"; return nil; }
        buf = [dev_.metal() newBufferWithLength:(NSUInteger)need options:dataset_options()]; how = "allocated"; pool_allocs_++;
        if (!buf) { r.code = "memory"; r.explain = std::string(name) + " allocation of " + mb(need) + " failed"; r.need_bytes = need; return nil; }
    }
    if (!pretouch(buf, payload)) { r.code = "driver"; r.explain = std::string(name) + ": the pre-touch fill failed"; return nil; }
    arm_guards(buf, payload);
    if (pay_off_ || knobs_.storage == "private" || !knobs_.persist.empty() || !knobs_.pretouch.empty())
        fprintf(stderr, "metal: %s %s: payload %s at offset %llu, guard %s each side, storage %s, pretouch %s\n", name, how, mb(payload).c_str(), (unsigned long long)pay_off_, mb(guard_bytes_).c_str(), knobs_.storage == "private" ? "private" : "shared", knobs_.pretouch.empty() ? "gpu" : knobs_.pretouch.c_str());
    return buf;
}

// The retired epoch's buffer. or-0-leak-1 (11 Oct 2026): a released shared buffer's pages are not reclaimed on this macOS (one
// dataset retained per epoch, swap by the fourth on 16 GB) while the pooled buffer, reused for the next epoch, holds the footprint
// flat; so the pool is the stock form ("" and "pool"), and the explicit "reconstruct" form marks the buffer purgeable-empty before
// its release (Metal's way to let the system reclaim it at once) and records whether that reclaims.
bool MetalEngineX::persist_pool() const { return knobs_.persist.empty() || knobs_.persist == "pool"; }

// Ember's memory-pressure pause (the product team, 11 Oct 2026): `set release_dataset 1` after `pause`. The loop acts on it here: every
// batch in flight has been collected by the caller; the dataset, the split half, the pool and any next epoch's buffers go
// (purgeable-empty first, so the system reclaims them at once); the cache, the context and the pipelines stay, so the rebuild on
// resume is the dataset's build alone through the cold path (ensure_dataset) before the first batch.
void MetalEngineX::release_dataset_now() {
    release_requested_ = false;
    if (build_last_) { [build_last_ waitUntilCompleted]; build_last_ = nil; }
    uint64_t freed = 0;
    for (id<MTLBuffer> __strong *b : { &dataset_, &dataset2_, &next_dataset_, &next_dataset2_, &spare_, &spare2_ }) {
        if (!*b) continue; freed += [*b length]; [*b setPurgeableState:MTLPurgeableStateEmpty]; *b = nil; purged_++;
    }
    if (ih_next_ctx_) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; }
    ih_next_light_ = nil; next_dataset_bytes_ = 0; next_height_ = ~0ull; ih_next_cursor_ = 0; ih_next_items_ = 0; ih_next_params_.clear();
    dataset_bytes_ = 0; released_at_ms_ = now_ms();
    fprintf(stderr, "metal: dataset released under memory pressure (%s freed; the cache and the context kept; the rebuild follows the first job after resume)\n", mb(freed).c_str());
}

bool MetalEngineX::ensure_dataset(uint64_t height) {
    if (dataset_ || !cand_ || cand_->kind != Kind::igneumhash) return true;
    Refusal r; id<MTLBuffer> nd = nil, nl = nil; uint64_t nb = 0; ignpow_ctx *nc = nullptr; uint8_t ne[32];
    double t0 = now_ms();
    if (!ih_build_dataset(height, nc, nd, nb, ne, nl, r)) { if (nc) ignpow_ctx_free(nc); fprintf(stderr, "metal: dataset rebuild after a release at height %llu: %s\n", (unsigned long long)height, r.explain.c_str()); return false; }
    if (ctx_) ignpow_ctx_free(ctx_);
    ctx_ = nc; dataset_ = nd; dataset_bytes_ = nb; light_ = nl; std::memcpy(ih_E_, ne, 32); height_ = height;
    fprintf(stderr, "metal: dataset rebuilt after the release in %.0f ms (the gap since the release %.0f ms)\n", now_ms() - t0, released_at_ms_ > 0 ? now_ms() - released_at_ms_ : 0.0);
    return true;
}
void MetalEngineX::retire_dataset(id<MTLBuffer> __strong &buf) {
    if (!buf) return;
    if (persist_pool()) { if (!spare_) spare_ = buf; else spare2_ = buf; }
    else { [buf setPurgeableState:MTLPurgeableStateEmpty]; purged_++; }
    buf = nil;
}

bool MetalEngineX::guard_check(id<MTLBuffer> buf, uint64_t payload, const char *name, Json &out) {
    if (!buf || !pay_off_) return true;
    bool all = true;
    for (int side = 0; side < 2; side++) {
        const uint64_t raw = side ? pay_off_ + payload : 0, len = side ? guard_bytes_ : pay_off_;
        if (!len) continue;
        std::vector<uint32_t> w((size_t)(len / 4));
        bool ok = raw_read(buf, raw, len, w.data()); uint64_t bad = 0, first = ~0ull;
        if (ok) for (size_t i = 0; i < w.size(); i++) if (w[i] != guard_canary(raw + 4 * i)) { bad++; if (first == ~0ull) first = 4 * i; }
        if (!ok || bad) all = false;
        Json g = Json::object().set("buffer", std::string(name)).set("side", side ? "back" : "front").set("readable", ok).set("bad_words", (double)bad)
            .set("first_bad_offset_from_guard_start", first == ~0ull ? -1.0 : (double)first).set("verdict", !ok ? "unreadable" : bad ? "WRITTEN" : "intact");
        out.push(g);
    }
    return all;
}

bool MetalEngineX::guard_status(Json &out) {
    if (!pay_off_) return false;
    gpu_idle();
    Json rows = Json::array(); bool all = true;
    all &= guard_check(dataset_, dataset2_ ? dataset_bytes_ / 2 : dataset_bytes_, "dataset", rows);
    all &= guard_check(dataset2_, dataset_bytes_ / 2, "dataset2", rows);
    all &= guard_check(next_dataset_, next_dataset2_ ? next_dataset_bytes_ / 2 : next_dataset_bytes_, "next_dataset", rows);
    all &= guard_check(next_dataset2_, next_dataset_bytes_ / 2, "next_dataset2", rows);
    out.set("guard_mb", (double)(guard_bytes_ >> 20)).set("shift_bytes", (double)(pay_off_ - guard_bytes_)).set("payload_offset", (double)pay_off_).set("buffers", rows).set("intact", all).set("canary", "word w at raw offset o: (o * 0x9E3779B97F4A7C15 >> 32) ^ 0xC0FFEE11; the kernels are bound at the payload offset and never see the guard");
    return true;
}

bool MetalEngineX::dataset_word(uint64_t word_index, uint32_t *value, bool write) {
    if (!dataset_ || !cand_ || cand_->kind != Kind::igneumhash || rxe1_) return false;
    if (knobs_.variant == "swizzle") return false;   // the slot map would have to be applied; refused by name
    const uint64_t byte = word_index * 4;
    if (byte + 4 > dataset_bytes_) return false;
    id<MTLBuffer> buf = dataset_; uint64_t off = byte;
    if (dataset2_) { const uint64_t half = dataset_bytes_ / 2; if (byte >= half) { buf = dataset2_; off = byte - half; } }
    if (build_last_) { [build_last_ waitUntilCompleted]; build_last_ = nil; }
    return write ? raw_write(buf, pay_off_ + off, 4, value) : raw_read(buf, pay_off_ + off, 4, value);
}

bool MetalEngineX::trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) {
    if (!cand_ || cand_->kind != Kind::igneumhash || rxe1_ || !search_pso_ || last_src_.empty()) { r.code = "unsupported"; r.explain = "the trace path is IgneumHash's on an open engine"; return false; }
    if (knobs_.variant == "n1" || ih_ctx() > 1) { r.code = "unsupported"; r.explain = "the trace path runs the eight-lane kernel only"; return false; }
    const size_t need = 704 + (size_t)ih_.rounds * (130 + ih_.item_words());
    if (!trace_pso_ || trace_words_ != need) {
        trace_pso_ = nil; trace_full_ = nil;
        std::string src = "#define IGM_IH_TRACE 1\n#define IGM_IH_TRACE_FULL 1\n" + last_src_;
        NSError *err = nil; MTLCompileOptions *opts = [[MTLCompileOptions alloc] init]; opts.fastMathEnabled = NO;
        id<MTLLibrary> lib = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:opts error:&err];
        if (!lib) { r.code = "driver"; r.explain = std::string("trace shader compile: ") + (err ? [[err localizedDescription] UTF8String] : "no error text"); return false; }
        id<MTLFunction> fn = [lib newFunctionWithName:search_fn_name_];
        if (!fn) { r.code = "driver"; r.explain = "trace search function missing"; return false; }
        trace_pso_ = [dev_.metal() newComputePipelineStateWithFunction:fn error:&err];
        if (!trace_pso_) { r.code = "driver"; r.explain = std::string("trace pipeline: ") + (err ? [[err localizedDescription] UTF8String] : "no error text"); return false; }
        trace_full_ = [dev_.metal() newBufferWithLength:need * 4 options:MTLResourceStorageModeShared];
        if (!trace_full_) { r.code = "memory"; r.explain = "trace buffer allocation failed"; return false; }
        trace_words_ = need;
    }
    std::memset([trace_full_ contents], 0, need * 4);
    Job j; j.job_id = "trace"; j.height = height; std::memcpy(j.header, header, 32); std::memset(j.target, 0xff, 32); j.nonce_start = nonce; j.nonce_count = 1;
    bool got = false; use_trace_ = true;
    ih_run_job(j, [&](const Result &res) { if (res.nonce == nonce) got = true; }, [](const BatchStats &) {}, [] { return false; });
    use_trace_ = false;
    if (!got) { r.code = "driver"; r.explain = "the traced nonce returned no result"; return false; }
    words.assign(need, 0u); std::memcpy(words.data(), [trace_full_ contents], need * 4);
    return true;
}

bool MetalEngineX::paused() const {
    SystemState s = dev_.state();
    return s.asleep || s.thermal == Thermal::critical || (s.power == Power::battery && !battery_allowed_) || (s.low_power_mode && !lpm_allowed_) || s.memory_pressure >= 2;
}

double MetalEngineX::duty_for_thermal() const {
    SystemState s = dev_.state(); double d = knobs_.duty;
    if (knobs_.duty_hold) return d < 0.25 ? 0.25 : d;   // duty_hold=1: the ladder off, the knob's duty under any thermal state (a diagnostic row, labelled by thermal_pressure)
    if (s.thermal == Thermal::fair) d = d > 0.75 ? 0.75 : d; else if (s.thermal == Thermal::serious) d = d > 0.5 ? 0.5 : d;
    return d < 0.25 ? 0.25 : d;
}

std::string MetalEngineX::describe() const {
    return "metal " + std::string(cand_ ? cand_->id : "?") + (cand_ && cand_->kind == Kind::rxe1 ? " " + rx_.variant() + " " + std::to_string(rx_.group) + " lanes per nonce" : "") + (cand_ && cand_->kind == Kind::igneumhash ? " " + ih_.variant() + " scratch in device memory" + (knobs_.variant.empty() ? "" : " kernel variant " + knobs_.variant + (knobs_.variant == "reach-half" || knobs_.variant.rfind("abl-", 0) == 0 || knobs_.variant.rfind("form1-abl-", 0) == 0 || knobs_.variant.rfind("duplex-", 0) == 0 ? " (DIAGNOSTIC: wrong hash by design, never a cell)" : "")) : "") + " width " + std::to_string(simd_width_) + (cand_ && (cand_->kind == Kind::igneumhash || (cand_->kind == Kind::fishhash && fh_lanes_ == 8)) ? " eight lanes per hash" : " one thread per hash") + " threadgroup " + std::to_string(knobs_.threadgroup) + (msl_osize_ ? " msl-osize" : "");
}

Json MetalEngineX::code_json() const {
    Json c = Json::object();
    if (!search_pso_) return c;
    uint32_t lanes = cand_ && cand_->kind == Kind::rxe1 ? rx_.group : cand_ && cand_->kind == Kind::igneumhash ? (ih_.forge() ? 16u : (knobs_.variant == "n1" || ih_.version >= 16) ? 1u : 8u) : cand_ && cand_->kind == Kind::fishhash ? fh_lanes_ : 1u;
    c.set("registers", "not exposed by Metal at run time (the compiler report is a build-time artefact)").set("threadgroup_memory_bytes", (double)[search_pso_ staticThreadgroupMemoryLength])
     .set("max_threads_per_threadgroup", (double)[search_pso_ maxTotalThreadsPerThreadgroup]).set("simd_width", (double)simd_width_).set("workgroup", (double)(knobs_.threadgroup * simd_width_)).set("lanes_per_nonce", (double)lanes).set("batches_in_flight", (double)knobs_.inflight).set("nonces_per_batch_max", (double)(ih_slots_ / (knobs_.inflight ? knobs_.inflight : 1)));
    if (cand_ && cand_->kind == Kind::rxe1) c.set("schedule_ops", (double)((uint64_t)rx_.rounds * rx_.blocks * rx_.registers)).set("reads_per_nonce", (double)rx_.rounds).set("bytes_read_per_nonce", (double)rx_.rounds * 128);
    else if (cand_ && cand_->kind == Kind::igneumhash) c.set("reads_per_nonce", (double)ih_.rounds).set("bytes_read_per_nonce", (double)ih_.rounds * ih_.item_bytes);
    if (cand_ && cand_->kind == Kind::igneumhash && (knobs_.variant == "diag-a" || knobs_.variant == "diag-b" || knobs_.variant == "diag-a-notail" || knobs_.variant == "diag-b-notail" || knobs_.variant == "diag-a-rand" || knobs_.variant == "diag-a-rand-notail" || knobs_.variant == "diag-a-ord" || knobs_.variant == "diag-a-ord-notail" || knobs_.variant == "diag-a-split")) c.set("bytes_read_per_nonce", (double)ih_.rounds * knobs_.diag_width).set("request_bytes", (double)knobs_.diag_width).set("request_shape", "eight lanes, W-aligned item, W/128 coalesced 128-byte steps of 16 bytes per lane (64 bytes: lanes 0 to 3 only)");
    if (cand_ && cand_->kind == Kind::igneumhash && knobs_.variant.rfind("diag-sg", 0) == 0) c.set("loader_form", knobs_.variant.rfind("diag-sg2-", 0) == 0 ? "two macro-items per SIMD group per instruction (W128, W256)" : knobs_.variant.rfind("diag-sgtg-", 0) == 0 ? "one macro-item per SIMD group staged through threadgroup memory (the control)" : "one macro-item per SIMD group per instruction").set("bytes_read_per_nonce", (double)ih_.rounds * knobs_.diag_width).set("request_bytes", (double)knobs_.diag_width).set("request_shape", "Orchard workstream 2: one W-byte macro-item per SIMD group per load instruction (W/16 lanes by 16 bytes; the four nonces of the group in turn), independent (a) or the nonce's own item words next (b)");
    if (cand_ && cand_->kind == Kind::igneumhash) c.set("params_id", "igneumhash-v" + std::to_string(ih_.version) + "-" + ih_.variant()).set("physical_bytes_per_hash", "not exposed by this runtime");
    else if (cand_) c.set("bytes_read_per_nonce", (double)bytes_read_per_hash(cand_->kind));
    c.set("nonces_per_batch_set", (double)knobs_.batch_nonces).set("nonces_per_batch_last", (double)last_hashes_);
    if (cand_ && (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1))
        c.set("allocation", dataset2_ ? "two buffers of " + std::to_string(dataset_bytes_ >> 21) + " MiB (shared storage)" : "one buffer of " + std::to_string(dataset_bytes_ >> 20) + " MiB (shared storage)")
         .set("page_arrangement", "not exposed by Metal (unified memory; the storage mode is the only control)");
    c.set("resident_waves", "not exposed by Metal at run time").set("memory_requests_per_payload_byte", "not exposed (the kernel's logical count: one 128-byte request per round per nonce)")
     .set("cache_behaviour", "not exposed").set("stalls", "not exposed").set("integer_throughput", "not exposed").set("lane_exchange", "SIMD-group shuffle")
     .set("physical_traffic", "not exposed at run time (Metal's GPU counters need the profiler)").set("occupancy", "not exposed").set("register_pressure", "not exposed at run time");
    c.set("fingerprint", fp_);   // Orchard workstream 1: the execution fingerprint on every row
    return c;
}

Json MetalEngineX::budget_json() const {
    DeviceInfo i = dev_.info(); HostMemory hm = dev_.host_memory();
    uint64_t next_bytes = dataset_bytes_;   // FishHash: one dataset for ever; Autolykos: the next height's table, about the same size
    BudgetDecision b = dev_.budget(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0);
    return Json::object().set("total_bytes", std::to_string(i.physical_memory)).set("free_bytes", std::to_string(hm.available_bytes()))
        .set("fit_cold_bytes", std::to_string(allocated_bytes())).set("fit_warm_bytes", std::to_string(allocated_bytes() + next_bytes))
        .set("can_warm", b.warm_fits).set("working_set_limit_bytes", std::to_string(b.limit)).set("recommended_max_working_set", std::to_string(i.recommended_working_set))
        .set("headroom_bytes", std::to_string(b.headroom)).set("unified_memory", i.unified_memory);
}

bool MetalEngineX::build_pipelines(Refusal &r) {
    if (search_pso_) return true;
    NSError *err = nil;
    // The keyed-path experiment defines (the M5 Max compiler fault, 12 Oct 2026): tokens in the variant string compile the keyed kernel
    // with one piece removed, for the bisect on the chip without a build per step: kx-noshare (the share words are zero, the BLAKE3 of the
    // share not run), kx-noinject (the share computed, not folded into the item words), kx-nobcast (m_i from S alone, no lane broadcast of
    // R), kx-noloop (the keyed branch never taken). Diagnostics only: every one mismatches by construction; the digest's first word says
    // whether the fault followed the piece.
    std::string kx;
    for (const char *t : {"kx-noshare", "kx-noinject", "kx-nobcast", "kx-noloop", "kx-inj-idx", "kx-inj-opnd", "kx-inj-mask", "kx-inj-vol"}) if (knobs_.variant.find(t) != std::string::npos) { std::string d = t; for (auto &c : d) c = (char)toupper(c == '-' ? '_' : c); kx += "#define IGM_" + d + " 1\n"; }
    if (!r0d_from_job_) r0d_height_ = height_;   // the programme's period: the open height, or the job's when run_job rebuilt for a new period
    r0d_from_job_ = false; r0d_period_ = (ih_.version >= 9 && ih_.period_blocks) ? r0d_height_ / ih_.period_blocks : ~0ull;
    std::string r0d_src, r0d_err;   // version 9 (R0D): the period programme generated on the host from the seed knob (or derived), one function in the source
    if (cand_->kind == Kind::igneumhash && ih_.version == 16) { if (!r16f_program_source(ih_, knobs_, r0d_height_, true, r0d_src, r16f_key_, r0d_err) || r0d_src.empty()) { r.code = "params"; r.explain = "R0G programme: " + r0d_err; return false; } r0d_src = "#define IGM_R16F_TAILS " + std::to_string(ih_.program_distance) + "\n" + r0d_src; }   // VERSION 16: the period's static-fabric programme as one function in the source, the key kept for the kernel's buffer
    else if (cand_->kind == Kind::igneumhash && ih_.version >= 9 && ih_.version != 15 && ih_.version != 17 && !ih_.forge()) { r0d_src = r0d_program_source(ih_, knobs_, r0d_height_, true, r0d_err); if (r0d_src.empty()) { r.code = "params"; r.explain = "R0D programme: " + r0d_err; return false; } }
    std::string src = kernel_source(shader_dir_, "prelude_msl.inc");
    if (cand_->kind == Kind::fishhash) src += kernel_source(shader_dir_, "igm_fishhash.inc") + kernel_source(shader_dir_, "wrap_fishhash_msl.inc") + kernel_source(shader_dir_, "igm_fishhash8.inc") + kernel_source(shader_dir_, "wrap_fishhash8_msl.inc");
    else if (cand_->kind == Kind::rxe1) src += rx_probe_define() + kernel_source(shader_dir_, "igm_fishhash.inc") + kernel_source(shader_dir_, "wrap_fishhash_msl.inc") + kernel_source(shader_dir_, "igm_igneumhash.inc") + kernel_source(shader_dir_, "igm_igneumhash1.inc") + kernel_source(shader_dir_, "wrap_igneumhash_msl.inc") + kernel_source(shader_dir_, "igm_rxe1.inc") + kernel_source(shader_dir_, "wrap_rxe1_msl.inc");
    else if (cand_->kind == Kind::igneumhash) src = ((cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14 && r0e_optrace_on_) ? "#define IGM_R0E_OPTRACE 1\n#define IGM_R0E_OPS " + std::to_string(ih_.program_ops) + "u\n" : std::string()) + kx + std::string(getenv("IGM_IH_TRACE") ? "#define IGM_IH_TRACE 1\n" : "") + "#define IGM_IH_VERSION " + std::to_string(ih_.version) + "\n#define IGM_R17_ROUNDS " + std::to_string(ih_.version == 17 ? ih_.rounds : 512u) + "u" + "\n#define IGM_FORGE_DRAWN " + std::to_string(ih_.version == 20 ? (ih_.program_distance & 1u) : 0u) + "\n#define IGM_FORGE_OLDCARD " + std::to_string(ih_.version == 25 ? ih_.scratch_bytes : 0u) + "u\n#define IGM_FORGE_FPMODE " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 1) & 7u) : 0u) + "\n#define IGM_FORGE_PAIR_LOG2 " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 4) & 15u) : 0u) + "\n#define IGM_FORGE_LEAN " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 15) & 1u) : 0u) + "\n#define IGM_FORGE_PREFETCH " + std::to_string(ih_.forge2() ? ((ih_.program_distance >> 8) & 127u) : 0u) + "\n#define IGM_FORGE_TRACE " + std::string(getenv("IGM_FORGE_TRACE") ? "1" : "0") + "\n#define IGM_IH_ITEM_WORDS " + std::to_string(ih_.item_words()) + "u\n#define IGM_IH_KEYED_S " + std::to_string(ih_.keyed_rounds) + "u\n" + (ih_.version >= 12 ? "#define IGM_IH_TKW " + std::to_string(ih_.table_words()) + "u\n#define IGM_IH_TILE_LAYOUT " + std::to_string(ih_.tile_layout) + "u\n#define IGM_IH_TG_OCTETS " + std::to_string(ih_tg_octets()) + "u\n#define IGM_IH_AW " + std::to_string(ih_.access_bytes / 4) + "u\n#define IGM_IH_SHW " + std::to_string(ih_.shared_words()) + "u\n" : std::string()) + "#define IGM_IH_RW " + std::to_string(ih_.rw()) + "u\n" + ((knobs_.variant == "v8s" && ih_.version == 8) ? "#define IGM_IH_V8S 1\n#define IGM_IH_V8_K " + std::to_string(ih_.registers) + "u\n#define IGM_IH_V8_B " + std::to_string(ih_.blocks) + "u\n#define IGM_IH_V8_F " + std::to_string(ih_.features) + "u\n" : "") + ((knobs_.variant == "form1" || knobs_.variant.rfind("form1-", 0) == 0) ? "#define IGM_IH_V4_FORM 1\n" : knobs_.variant == "form2" ? "#define IGM_IH_V4_FORM 2\n" : "") + (knobs_.variant == "load4" ? "#define IGM_IH_LOAD4 1\n" : "") + (knobs_.variant == "reach-half" ? "#define IGM_IH_REACH_MASK 1\n" : "") + (knobs_.variant == "diag-a" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : "") + (knobs_.variant == "diag-b" ? "#define IGM_IH_DIAG_B 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : "") + (knobs_.variant == "diag-b-notail" ? "#define IGM_IH_DIAG_B 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n#define IGM_IH_ABL 16\n" : knobs_.variant == "diag-a-notail" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n#define IGM_IH_ABL 16\n" : knobs_.variant == "diag-a-split" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_A_SPLIT 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : knobs_.variant == "diag-a-rand" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_A_RAND 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : knobs_.variant == "diag-a-rand-notail" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_A_RAND 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n#define IGM_IH_ABL 16\n" : knobs_.variant == "diag-a-ord" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_A_ORDERED 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" : knobs_.variant == "diag-a-ord-notail" ? "#define IGM_IH_DIAG_A 1\n#define IGM_IH_DIAG_A_ORDERED 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n#define IGM_IH_ABL 16\n" : "") + (knobs_.variant.rfind("diag-sg", 0) == 0 ? std::string(knobs_.variant.back() == 'a' ? "#define IGM_IH_DIAG_A 1\n" : "#define IGM_IH_DIAG_B 1\n") + "#define IGM_IH_DIAG_SG 1\n#define IGM_IH_DIAG_WIDTH " + std::to_string(knobs_.diag_width) + "u\n" + (knobs_.variant.rfind("diag-sg2-", 0) == 0 ? "#define IGM_IH_DIAG_SG_PAIR 1\n" : knobs_.variant.rfind("diag-sgtg-", 0) == 0 ? "#define IGM_IH_DIAG_SG_TG 1\n" : "") : "") + (knobs_.variant == "swizzle" ? "#define IGM_IH_SWIZZLE 1\n" : "") + (knobs_.variant == "duplex-mem" ? "#define IGM_IH_DUPLEX 1\n" : knobs_.variant == "duplex-compute" ? "#define IGM_IH_DUPLEX 2\n" : knobs_.variant == "duplex-seq" ? "#define IGM_IH_DUPLEX 3\n" : knobs_.variant == "duplex-sim" ? "#define IGM_IH_DUPLEX 4\n" : knobs_.variant == "duplex-split" ? "#define IGM_IH_DUPLEX 5\n" : "") + (knobs_.variant.rfind("abl-", 0) == 0 ? "#define IGM_IH_ABL " + knobs_.variant.substr(4) + "\n" : knobs_.variant.rfind("form1-abl-", 0) == 0 ? "#define IGM_IH_ABL " + knobs_.variant.substr(10) + "\n" : "") + ((knobs_.variant == "stream-final" || knobs_.variant == "form1-stream-final") && ih_.scratch_bytes == 0 ? "#define IGM_IH_STREAM_FINAL 1\n" : "") + (knobs_.variant == "overfetch256" ? "#define IGM_IH_OVERFETCH 256u\n" : knobs_.variant == "overfetch512" ? "#define IGM_IH_OVERFETCH 512u\n" : "") + (ih_ctx() > 1 ? "#define IGM_IH_CTX " + std::to_string(ih_ctx()) + "\n" : "") + ((knobs_.variant == "split2" || knobs_.alloc == "split2") ? "#define IGM_IH_SPLIT2 1\n" : "") + src + kernel_source(shader_dir_, "igm_fishhash.inc") + kernel_source(shader_dir_, "wrap_fishhash_msl.inc") + (ih_.version >= 12 ? kernel_source(shader_dir_, "igm_r0e_access.inc") : std::string()) + r0d_src + kernel_source(shader_dir_, "igm_igneumhash.inc") + (ih_.version == 15 ? kernel_source(shader_dir_, "igm_r0f.inc") : ih_.version == 16 ? kernel_source(shader_dir_, "igm_r16f.inc") : ih_.version == 17 ? kernel_source(shader_dir_, "igm_r17.inc") : ih_.forge() ? kernel_source(shader_dir_, "igm_forge.inc") : std::string()) + kernel_source(shader_dir_, "igm_igneumhash1.inc") + kernel_source(shader_dir_, "wrap_igneumhash_msl.inc") + (ih_ctx() > 1 ? kernel_source(shader_dir_, "igm_igneumhash_ctx.inc") + kernel_source(shader_dir_, "wrap_igneumhash_ctx_msl.inc") : std::string());
    else src += kernel_source(shader_dir_, "igm_autolykos.inc") + kernel_source(shader_dir_, "wrap_autolykos_msl.inc");
    MTLCompileOptions *opts = [[MTLCompileOptions alloc] init]; opts.fastMathEnabled = NO;
    if (@available(macOS 15.0, *)) opts.mathMode = MTLMathModeSafe;   // version 8 bit 4 (SPEC 0.8): IEEE binary32, nearest-even, no contraction (the kernel's fp contract pragma), no fast math
    // F-66 (the security review, 12 Oct 2026): the bit 5 compile rules explicit and asserted: fast math off, the safe math mode (no contraction; the FMA fused only when called)
    if (opts.fastMathEnabled) { r.code = "driver"; r.explain = "the Metal compile options report fast math on (F-66): refused"; return false; }
    const auto r0d_t0 = std::chrono::steady_clock::now();
    // The M5 Max's Metal compiler produced a digest with its first word zero on a keyed document (version 8 bit 10) at its default
    // optimisation (the mix exact, every round exact under the full trace, the M6 exact): a keyed document's search library is compiled
    // at the size optimisation level (a different optimiser) unless the variant says msl-ofull; msl-osize asks it for any document.
    // 12 Oct 2026, the the GPU team and Apple seat.
    if (@available(macOS 13.0, *)) {
        const bool keyed = cand_ && cand_->kind == Kind::igneumhash && ih_.keyed_rounds > 0;
        const bool osize = knobs_.variant.find("msl-osize") != std::string::npos || (keyed && knobs_.variant.find("msl-ofull") == std::string::npos);
        if (osize) opts.optimizationLevel = MTLLibraryOptimizationLevelSize;
        msl_osize_ = osize;
    }
    if (getenv("IGM_SHADER_DUMP")) { fprintf(stderr, "=== shader source begin (%zu bytes)\n%s\n=== shader source end\n", src.size(), src.c_str()); }   // the assembled MSL on stderr (14 Oct 2026: the version 17 compile on the M6)
    id<MTLLibrary> lib = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:opts error:&err];
    if (!lib) { r.code = "driver"; r.explain = std::string("shader compile: ") + [[err localizedDescription] UTF8String]; return false; }
    id<MTLFunction> fn = [lib newFunctionWithName:(cand_->kind == Kind::fishhash ? (fh_lanes_ == 8 ? @"igm_fishhash8_search" : @"igm_fishhash_search") : cand_->kind == Kind::igneumhash ? (ih_.forge() ? @"igm_forge_search" : ih_.version == 17 ? @"igm_r17_search" : ih_.version == 16 ? @"igm_r16f_search" : ih_.version == 15 ? @"igm_r0f_search" : knobs_.variant == "n1" ? @"igm_igneumhash1_search" : ih_ctx() > 1 ? @"igm_igneumhash_ctx_search" : @"igm_igneumhash_search") : cand_->kind == Kind::rxe1 ? @"igm_rxe1_search" : @"igm_autolykos_search")];
    if (!fn) { r.code = "driver"; r.explain = "search function missing"; return false; }
    last_src_ = src; search_fn_name_ = [fn name]; trace_pso_ = nil; trace_full_ = nil; trace_words_ = 0;   // the trace path (trace_one) recompiles this source with the full trace
    search_pso_ = [dev_.metal() newComputePipelineStateWithFunction:fn error:&err]; ih_built_version_ = ih_.version * 1024u + ih_.item_words() + ih_.rw() * 65536u + r0d_key(ih_, knobs_, r0d_height_) + ((knobs_.variant == "v8s" && ih_.version == 8) ? (ih_.registers * 7u + ih_.blocks * 131u + ih_.features) * 4194304u : 0u);   // the snapshot form (v8s) compiles K, B and F in
    if (ih_.version >= 9) fprintf(stderr, "metal: the period programme (%u operations) compiled in %lld ms (TP-245)\n", ih_.program_ops, (long long)std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - r0d_t0).count());
    if (!search_pso_) { r.code = "driver"; r.explain = std::string("search pipeline: ") + [[err localizedDescription] UTF8String]; return false; }
    if (cand_->kind == Kind::igneumhash && ih_.version == 16) {   // VERSION 16: the period key words into buffer 9 (the programme is in the library); the dummy programme buffer keeps version 15's layout
        r0f_key_ = [dev_.metal() newBufferWithBytes:r16f_key_ length:32 options:MTLResourceStorageModeShared]; if (!r0f_prog_) r0f_prog_ = [dev_.metal() newBufferWithLength:16 options:MTLResourceStorageModeShared];
        if (!r0f_key_ || !r0f_prog_) { r.code = "memory"; r.explain = "the version 16 key buffer failed"; return false; }
        r0f_period_ = r0d_period_;
    }
    simd_width_ = (uint32_t)[search_pso_ threadExecutionWidth];
    if (cand_->kind == Kind::fishhash && fh_lanes_ == 8 && simd_width_ % 8 != 0) { fprintf(stderr, "metal: SIMD width %u is not a multiple of eight; FishHash falls back to one thread per hash\n", simd_width_); fh_lanes_ = 1; search_pso_ = nil; return build_pipelines(r); }
    if (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) {
        id<MTLFunction> df = [lib newFunctionWithName:(knobs_.variant == "build8" ? @"igm_igneumhash_dataset8" : @"igm_igneumhash_dataset")];
        dag_pso_ = df ? [dev_.metal() newComputePipelineStateWithFunction:df error:&err] : nil;
        if (ih_.version >= 12) { id<MTLFunction> sf = [lib newFunctionWithName:@"igm_r12_shared_fill"]; r12_fill_pso_ = sf ? [dev_.metal() newComputePipelineStateWithFunction:sf error:&err] : nil; r12_period_ = ~0ull; }   // the shared table's fill kernel (versions 12 to 14)
        if (!dag_pso_) { r.code = "driver"; r.explain = "igneumhash dataset pipeline failed"; return false; }
        if (simd_width_ % (cand_->kind == Kind::rxe1 ? rx_.group : 8) != 0) { r.code = "unsupported"; r.explain = "SIMD width " + std::to_string(simd_width_) + " is not a multiple of the lane group"; return false; }
        fingerprint_pipelines(fn, df, src, opts);
    } else if (cand_->kind == Kind::autolykos) {
        id<MTLFunction> tf = [lib newFunctionWithName:@"igm_autolykos_table"];
        table_pso_ = tf ? [dev_.metal() newComputePipelineStateWithFunction:tf error:&err] : nil;
        if (!table_pso_) { r.code = "driver"; r.explain = "table pipeline failed"; return false; }
        fingerprint_pipelines(fn, nil, src, opts);
    } else {
        std::string dag = kernel_source(shader_dir_, "igm_dag.metal");
        id<MTLLibrary> dl = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:dag.c_str()] options:opts error:&err];
        if (!dl) { r.code = "driver"; r.explain = std::string("dag shader compile: ") + [[err localizedDescription] UTF8String]; return false; }
        MTLFunctionConstantValues *fc = [[MTLFunctionConstantValues alloc] init];
        uint32_t parents = cand_->dataset_parents, nodes = 1179641u;   // FishHash.cpp line 28; checked against the cache's byte count at build
        [fc setConstantValue:&parents type:MTLDataTypeUInt atIndex:0];
        [fc setConstantValue:&nodes type:MTLDataTypeUInt atIndex:1];
        id<MTLFunction> df = [dl newFunctionWithName:@"igm_dag_generate" constantValues:fc error:&err];
        dag_pso_ = df ? [dev_.metal() newComputePipelineStateWithFunction:df error:&err] : nil;
        if (!dag_pso_) { r.code = "driver"; r.explain = "dag pipeline failed"; return false; }
        fingerprint_pipelines(fn, nil, src, opts);
    }
    return true;
}

// Version 8 FP: the device check of the fp class on this GPU through the same compiled fop (mathMode safe, fp contract off):
// the class file's edge and random rows (x, y, z, out[, opcode]); prints one JSON line and returns the mismatch count
int MetalEngineX::fp_check(const std::string &path, uint32_t features) {
    std::string txt = read_file(path);
    if (txt.empty()) { printf("{\"fpcheck\": {\"refused\": \"cannot read %s\"}}\n", path.c_str()); return -1; }
    struct Case { uint32_t x, y, z, out, op; bool wide; };
    std::vector<Case> cases;
    auto num_after = [&](size_t from, const char *key, size_t limit, uint64_t &v) -> bool {
        size_t k = txt.find(key, from); if (k == std::string::npos || k > limit) return false;
        k += strlen(key); while (k < txt.size() && (txt[k] == ' ' || txt[k] == ':')) k++;
        v = strtoull(txt.c_str() + k, nullptr, 10); return true;
    };
    size_t pos = 0;
    while ((pos = txt.find("\"x\"", pos)) != std::string::npos) {
        size_t end = txt.find('}', pos); if (end == std::string::npos) break;
        uint64_t x = 0, y = 0, z = 0, o = 0, op = 2;
        // a row is a dict {"x","y","z","out"} (the bit 4 multiply form) or {"op","x","y","z","out"} (a wide-form case, revision 2's
        // "wide" rows: every opcode binary32); the row's own shape decides its form, not the job's feature bit
        size_t rs = txt.rfind('{', pos); if (rs == std::string::npos) rs = pos;
        bool ok = num_after(pos, "\"x\"", end, x) && num_after(pos, "\"y\"", end, y) && num_after(pos, "\"z\"", end, z) && num_after(pos, "\"out\"", end, o);
        bool wide = num_after(rs, "\"op\"", end, op) || num_after(rs, "\"opcode\"", end, op);
        if (ok) cases.push_back({(uint32_t)x, (uint32_t)y, (uint32_t)z, (uint32_t)o, (uint32_t)op, wide});
        pos = end;
    }
    if (cases.empty()) { printf("{\"fpcheck\": {\"refused\": \"no cases in %s\"}}\n", path.c_str()); return -1; }
    NSError *err = nil;
    std::string src = "#define IGM_IH_VERSION 8\n#define IGM_IH_ITEM_WORDS 32u\n" + kernel_source(shader_dir_, "prelude_msl.inc") + kernel_source(shader_dir_, "igm_fishhash.inc") + kernel_source(shader_dir_, "wrap_fishhash_msl.inc") + kernel_source(shader_dir_, "igm_igneumhash.inc") + kernel_source(shader_dir_, "igm_igneumhash1.inc") + kernel_source(shader_dir_, "wrap_igneumhash_msl.inc");
    MTLCompileOptions *opts = [[MTLCompileOptions alloc] init]; opts.fastMathEnabled = NO;
    if (@available(macOS 15.0, *)) opts.mathMode = MTLMathModeSafe;
    id<MTLLibrary> lib = [dev_.metal() newLibraryWithSource:[NSString stringWithUTF8String:src.c_str()] options:opts error:&err];
    if (!lib) { printf("{\"fpcheck\": {\"refused\": \"shader compile: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return -1; }
    id<MTLFunction> fn = [lib newFunctionWithName:@"igm_v8_fpcheck"];
    id<MTLComputePipelineState> pso = fn ? [dev_.metal() newComputePipelineStateWithFunction:fn error:&err] : nil;
    if (!pso) { printf("{\"fpcheck\": {\"refused\": \"pipeline: %s\"}}\n", err ? [[err localizedDescription] UTF8String] : "?"); return -1; }
    const uint32_t n = (uint32_t)cases.size(); const uint32_t mode = features;
    std::vector<uint32_t> in(4 * n); uint32_t nwide = 0; for (uint32_t i = 0; i < n; i++) { in[4 * i] = cases[i].x; in[4 * i + 1] = cases[i].y; in[4 * i + 2] = cases[i].z; in[4 * i + 3] = cases[i].wide ? (0x100u | cases[i].op) : 0xFFFFFFFFu; if (cases[i].wide) nwide++; }   // the per-case form: 0x100 | opcode for a wide row, ~0 for the multiply form
    id<MTLBuffer> bin = [dev_.metal() newBufferWithBytes:in.data() length:in.size() * 4 options:MTLResourceStorageModeShared];
    id<MTLBuffer> bout = [dev_.metal() newBufferWithLength:n * 4 options:MTLResourceStorageModeShared];
    id<MTLCommandBuffer> cb = [queue_ commandBuffer]; id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
    [enc setComputePipelineState:pso]; [enc setBuffer:bin offset:0 atIndex:0]; [enc setBuffer:bout offset:0 atIndex:1]; [enc setBytes:&mode length:4 atIndex:2]; [enc setBytes:&n length:4 atIndex:3];
    [enc dispatchThreads:MTLSizeMake(n, 1, 1) threadsPerThreadgroup:MTLSizeMake(64, 1, 1)]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
    const uint32_t *res = (const uint32_t *)[bout contents]; uint32_t mism = 0; std::string first;
    for (uint32_t i = 0; i < n; i++) if (res[i] != cases[i].out) { if (mism < 3) { char b[160]; snprintf(b, sizeof b, "%s{\"i\":%u,\"x\":%u,\"y\":%u,\"z\":%u,\"op\":%u,\"expected\":%u,\"got\":%u}", first.empty() ? "" : ",", i, cases[i].x, cases[i].y, cases[i].z, cases[i].op, cases[i].out, res[i]); first += b; } mism++; }
    printf("{\"fpcheck\": {\"class\": \"%s\", \"features\": %u, \"cases\": %u, \"wide_cases\": %u, \"mismatches\": %u, \"verdict\": \"%s\", \"first\": [%s], \"semantics\": \"Metal runtime compiler, mathMode safe, fast math off, fp contract off (the prelude pragma); the operands masked to positive normals; the product or inner operation and the sum rounded separately to nearest-even\", \"fingerprint\": %s}}\n", path.c_str(), mode, n, nwide, mism, mism ? "MISMATCH" : "exact", first.c_str(), fp_.dump().c_str());
    fflush(stdout);
    return (int)mism;
}

#ifndef IGM_BUILD_COMMIT
#define IGM_BUILD_COMMIT "unrecorded"
#endif
#ifndef IGM_BUILD_TOOLCHAIN
#define IGM_BUILD_TOOLCHAIN "unrecorded"
#endif
#ifndef IGM_BUILD_SDK
#define IGM_BUILD_SDK "unrecorded"
#endif

// Orchard workstream 1 (docs/igneumhash/strata/ORCHARD.md): Apple exposes no stable ISA route, so the gate is a fingerprint on
// every row. What this records, each read at run time from the objects that execute, never hard-coded: the serialised
// MTLBinaryArchive of the compiled pipelines (the compiled binary; its sha256 and byte count; the file kept beside the row
// under IGM_FINGERPRINT_DIR when set, else the working directory), the sha256 of the MSL source the runtime compiler
// received, the compiler (the runtime compiler of this macOS: Metal has no offline route without Xcode, named so), the
// language version, threadExecutionWidth, maxTotalThreadsPerThreadgroup and staticThreadgroupMemoryLength of the search
// pipeline, the GPU family probe (every MTLGPUFamily value the device answers yes to, by number, so a family newer than
// the SDK's names still reads as its number), the architecture name, macOS, the counter sets the device lists and the
// sampling points it supports (the Xcode GPU capture: supported or not, never taken here: no Xcode on the host to read a
// trace, and a trace of a 2 GiB dataset dispatch would carry the dataset), the build's commit and toolchain (compile-time
// strings the build script passes) and the worker binary's own sha256 (the host sets IGM_WORKER_SHA256 at start).
void MetalEngineX::fingerprint_pipelines(id<MTLFunction> fn, id<MTLFunction> df, const std::string &src, MTLCompileOptions *opts) {
    Json f = Json::object();
    id<MTLDevice> d = dev_.metal();
    NSError *err = nil;
    f.set("msl_source_sha256", sha256_hex(src)).set("msl_source_bytes", (double)src.size());
    f.set("compiler", std::string("Metal runtime compiler of macOS (MTLDevice newLibraryWithSource); no offline metallib: Xcode and the metal toolchain are absent on the host"));
    { char buf[32]; snprintf(buf, sizeof buf, "0x%llx", (unsigned long long)[opts languageVersion]); f.set("msl_language_version", std::string(buf)); }
    f.set("fast_math", (bool)[opts fastMathEnabled]);
    f.set("build_commit", std::string(IGM_BUILD_COMMIT)).set("build_toolchain", std::string(IGM_BUILD_TOOLCHAIN)).set("build_sdk", std::string(IGM_BUILD_SDK));
    const char *wsha = getenv("IGM_WORKER_SHA256"); f.set("worker_sha256", std::string(wsha ? wsha : "unrecorded"));
    // the pipeline's run-time facts
    if (search_pso_) {
        f.set("thread_execution_width", (double)[search_pso_ threadExecutionWidth])
         .set("max_total_threads_per_threadgroup", (double)[search_pso_ maxTotalThreadsPerThreadgroup])
         .set("static_threadgroup_memory_bytes", (double)[search_pso_ staticThreadgroupMemoryLength]);
        if ([search_pso_ respondsToSelector:@selector(label)] && [search_pso_ label]) f.set("search_pipeline_label", std::string([[search_pso_ label] UTF8String]));
    }
    if (dag_pso_) f.set("dataset_pipeline_thread_execution_width", (double)[dag_pso_ threadExecutionWidth]).set("dataset_pipeline_max_total_threads", (double)[dag_pso_ maxTotalThreadsPerThreadgroup]);
    // the device
    f.set("gpu_name", std::string([[d name] UTF8String])).set("registry_id", std::to_string((unsigned long long)[d registryID]));
    {
        Json fams = Json::array(); int highest_apple = 0; bool m3 = false, m4 = false;
        for (int v = 1001; v <= 1020; v++) { if ([d supportsFamily:(MTLGPUFamily)v]) { fams.push(Json((double)v)); highest_apple = v - 1000; } }
        for (int v = 2001; v <= 2003; v++) if ([d supportsFamily:(MTLGPUFamily)v]) fams.push(Json((double)v));
        for (int v = 5001; v <= 5003; v++) if ([d supportsFamily:(MTLGPUFamily)v]) { fams.push(Json((double)v)); if (v == 5001) m3 = true; if (v == 5002) m4 = true; }
        f.set("gpu_families_supported", fams).set("gpu_family_apple", highest_apple ? "apple" + std::to_string(highest_apple) : std::string("none")).set("metal3", m3).set("metal4", m4);
        if ([d respondsToSelector:@selector(architecture)]) { id a = [d architecture]; if (a && [a respondsToSelector:@selector(name)]) f.set("architecture", std::string([[a name] UTF8String])); }
    }
    { NSOperatingSystemVersion v = [[NSProcessInfo processInfo] operatingSystemVersion]; f.set("macos", std::to_string(v.majorVersion) + "." + std::to_string(v.minorVersion) + "." + std::to_string(v.patchVersion)); }
    // the counters the device exposes (Apple GPUs list timestamp sets; stage utilisation and statistics only where the family has them)
    {
        Json sets = Json::array();
        for (id<MTLCounterSet> cs in [d counterSets]) {
            Json one = Json::object().set("name", std::string([[cs name] UTF8String]));
            Json names = Json::array(); for (id<MTLCounter> c in [cs counters]) names.push(Json(std::string([[c name] UTF8String])));
            one.set("counters", names); sets.push(one);
        }
        f.set("counter_sets", sets)
         .set("counter_sampling_stage_boundary", (bool)[d supportsCounterSampling:MTLCounterSamplingPointAtStageBoundary])
         .set("counter_sampling_dispatch_boundary", (bool)[d supportsCounterSampling:MTLCounterSamplingPointAtDispatchBoundary])
         .set("counter_sampling_blit_boundary", (bool)[d supportsCounterSampling:MTLCounterSamplingPointAtBlitBoundary]);
        f.set("gpu_capture_supported", (bool)[[MTLCaptureManager sharedCaptureManager] supportsDestination:MTLCaptureDestinationGPUTraceDocument])
         .set("gpu_capture_note", std::string("not taken: no Xcode on the host to read a trace document, and a capture of a dataset dispatch would carry the dataset; occupancy, stall, ALU and cache counters are absent on this host until the capture tooling exists"));
    }
    // the compiled binary: the serialised archive of the pipelines that run
    {
        std::string note; std::string dir = getenv("IGM_FINGERPRINT_DIR") ? getenv("IGM_FINGERPRINT_DIR") : ".";
        [[NSFileManager defaultManager] createDirectoryAtPath:[NSString stringWithUTF8String:dir.c_str()] withIntermediateDirectories:YES attributes:nil error:nil];   // the directory is made here, so a job needs no mkdir step
        MTLBinaryArchiveDescriptor *bd = [[MTLBinaryArchiveDescriptor alloc] init];
        id<MTLBinaryArchive> ar = [d newBinaryArchiveWithDescriptor:bd error:&err];
        if (!ar) note = std::string("binary archive refused: ") + (err ? [[err localizedDescription] UTF8String] : "no error text");
        else {
            MTLComputePipelineDescriptor *pd = [[MTLComputePipelineDescriptor alloc] init]; pd.computeFunction = fn;
            if (![ar addComputePipelineFunctionsWithDescriptor:pd error:&err]) note = std::string("search pipeline not archived: ") + (err ? [[err localizedDescription] UTF8String] : "no error text");
            if (df) { MTLComputePipelineDescriptor *dd = [[MTLComputePipelineDescriptor alloc] init]; dd.computeFunction = df; if (![ar addComputePipelineFunctionsWithDescriptor:dd error:&err]) note += std::string(" dataset pipeline not archived: ") + (err ? [[err localizedDescription] UTF8String] : "no error text"); }
            std::string path = dir + "/binary-archive-" + sha256_hex(src).substr(0, 12) + ".metallib";
            if (![ar serializeToURL:[NSURL fileURLWithPath:[NSString stringWithUTF8String:path.c_str()]] error:&err]) note += std::string(" serialise refused: ") + (err ? [[err localizedDescription] UTF8String] : "no error text");
            else {
                std::string bytes = read_file(path);
                f.set("binary_sha256", sha256_hex(bytes)).set("binary_bytes", (double)bytes.size()).set("binary_path", path)
                 .set("binary_note", std::string("the MTLBinaryArchive of the search and dataset pipelines serialised after compilation: the compiled binary this row ran; its sha256 is the row's binary hash"));
            }
        }
        if (!note.empty()) { f.set("binary_sha256", std::string("absent")).set("binary_note", note); fprintf(stderr, "metal fingerprint: %s\n", note.c_str()); }
    }
    f.set("execution_truth", std::string("runtime-compiled MSL; the serialised binary archive is the compiled binary; no offline metallib and no Xcode capture on this host (named absent, not skipped); thread_execution_width read from the pipeline at run time"));
    fp_ = f;
}

bool MetalEngineX::build_fishhash_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, Refusal &r) {
    int st = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a light context (status " + std::to_string(st) + "); FishHash needs the reference built with IGNPOW_HAVE_FISHHASH"; return false; }
    bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    const void *light = nullptr; uint64_t light_bytes = 0; uint32_t item = 0;
    if (ignpow_ctx_light_cache(ctx, &light, &light_bytes, &item) != IGNPOW_OK || !light || light_bytes == 0) { r.code = "unsupported"; r.explain = "reference exposes no FishHash cache"; return false; }
    if (light_bytes / 64 != 1179641u) { r.code = "unsupported"; r.explain = "FishHash cache item count " + std::to_string(light_bytes / 64) + " differs from the pinned 1179641"; return false; }
    if (bytes > [dev_.metal() maxBufferLength]) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " exceeds the device's largest buffer " + mb([dev_.metal() maxBufferLength]); return false; }
    light_ = [dev_.metal() newBufferWithBytes:light length:light_bytes options:MTLResourceStorageModeShared];
    dataset = [dev_.metal() newBufferWithLength:bytes options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
    if (!light_ || !dataset) { r.code = "memory"; r.explain = "allocation of " + mb(bytes) + " failed"; r.need_bytes = bytes; return false; }
    uint32_t items = (uint32_t)(bytes / 64);   // 512-bit items; the kernel indexes pairs
    const uint32_t per = 1u << 20;
    for (uint32_t first = 0; first < items; first += per) { @autoreleasepool {
        DagBatch b{first, items - first < per ? items - first : per, 0, 0};
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:dag_pso_];
        [enc setBytes:&b length:sizeof(b) atIndex:0];
        [enc setBuffer:light_ offset:0 atIndex:1];
        [enc setBuffer:dataset offset:0 atIndex:2];
        [enc dispatchThreads:MTLSizeMake(b.count, 1, 1) threadsPerThreadgroup:MTLSizeMake([dag_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
        [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { r.code = "driver"; r.explain = "dataset generation command buffer failed"; return false; }
    } }
    return true;
}

bool MetalEngineX::build_autolykos_table(uint64_t height, id<MTLBuffer> __strong &table, uint64_t &bytes, double &ms, Refusal &r, const std::function<bool()> *stop) {
    uint32_t N = autolykos_n(height);
    bytes = (uint64_t)N * 32;
    if (bytes > [dev_.metal() maxBufferLength]) { r.code = "memory"; r.explain = "table " + mb(bytes) + " exceeds the device's largest buffer"; r.need_bytes = bytes; return false; }
    table = [dev_.metal() newBufferWithLength:bytes options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
    if (!table) { r.code = "memory"; r.explain = "table allocation of " + mb(bytes) + " failed"; r.need_bytes = bytes; return false; }
    double t0 = now_ms();
    const uint32_t per = 1u << 18;   // bounded batches: 262,144 elements of 65 BLAKE2b compressions each
    for (uint32_t first = 0; first < N; first += per) {
        if (stop && (*stop)()) { r.code = "cancelled"; r.explain = "table build cancelled at element " + std::to_string(first) + " of " + std::to_string(N); table = nil; return false; }
        if (next_dataset_ && next_height_ != height) warm_step(per);   // the next height's table advances beside the cold build
        @autoreleasepool {
        TableBatch b{first, N - first < per ? N - first : per, (uint32_t)height, 0};
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:table_pso_];
        [enc setBytes:&b length:sizeof(b) atIndex:0];
        [enc setBuffer:table offset:0 atIndex:1];
        [enc dispatchThreads:MTLSizeMake(b.count, 1, 1) threadsPerThreadgroup:MTLSizeMake([table_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
        [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { r.code = "driver"; r.explain = "table command buffer failed"; return false; }
        }
    }
    ms = now_ms() - t0; last_table_ms_ = ms;
    return true;
}


// The warm build: Autolykos needs a new table for every height and the next height is
// always h + 1, so the next table is built beside the current one, one bounded batch per
// search batch, and swapped in at the job change; if the job arrives before the build
// ends, the rest is built synchronously and that remainder is the transition downtime.
void MetalEngineX::warm_start(uint64_t height) {
    if (cand_->kind != Kind::autolykos || next_height_ == height) return;
    next_dataset_ = nil; next_dataset_bytes_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0; next_cb_ = nil;
    uint64_t next_bytes = (uint64_t)autolykos_n(height) * 32;
    BudgetDecision b = dev_.budget(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0);
    if (!b.warm_fits) { static bool said = false; if (!said) { fprintf(stderr, "metal: warm table for height %llu does not fit (%s); transitions will be cold\n", (unsigned long long)height, b.explain.c_str()); said = true; } return; }
    id<MTLBuffer> t = [dev_.metal() newBufferWithLength:next_bytes options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
    if (!t) return;
    next_dataset_ = t; next_dataset_bytes_ = next_bytes; next_height_ = height; next_cursor_ = 0; next_n_ = autolykos_n(height);
}

void MetalEngineX::warm_step(uint32_t elements) {
    if (!next_dataset_ || next_cursor_ >= next_n_) return;
    uint32_t count = next_n_ - next_cursor_ < elements ? next_n_ - next_cursor_ : elements;
    @autoreleasepool {
    TableBatch b{next_cursor_, count, (uint32_t)next_height_, 0};
    id<MTLCommandBuffer> cb = [queue_ commandBuffer];
    id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
    [enc setComputePipelineState:table_pso_];
    [enc setBytes:&b length:sizeof(b) atIndex:0];
    [enc setBuffer:next_dataset_ offset:0 atIndex:1];
    [enc dispatchThreads:MTLSizeMake(count, 1, 1) threadsPerThreadgroup:MTLSizeMake([table_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
    [enc endEncoding]; [cb commit];
    next_cb_ = cb; next_cursor_ += count;
    }
}

void MetalEngineX::warm_finish() {
    if (!next_dataset_) return;
    double t0 = now_ms();
    uint32_t before = next_cursor_;
    while (next_cursor_ < next_n_) warm_step(1u << 18);
    if (next_cb_) { [next_cb_ waitUntilCompleted]; next_cb_ = nil; }
    if (before < next_n_) { last_table_ms_ = now_ms() - t0; fprintf(stderr, "metal: warm table for height %llu finished synchronously: %u of %u elements, %.0f ms of transition downtime\n", (unsigned long long)next_height_, next_n_ - before, next_n_, last_table_ms_); }
    else last_table_ms_ = 0;
}

bool MetalEngineX::dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t cd[32], uint8_t dg[32]) {
    if (!cand_ || cand_->kind != Kind::igneumhash || !ctx_ || !dataset_) return false;
    ih_warm_finish();
    std::memcpy(E, ih_E_, 32);
    { uint8_t e[32], cd2[32], dg2[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(ctx_, e, D, cd2, dg2, &bms, &hd) != IGNPOW_OK) return false; }
    { const void *c = nullptr; uint64_t cb = 0; uint32_t ib = 0; std::memset(cd, 0, 32); if (ignpow_ctx_light_cache(ctx_, &c, &cb, &ib) == IGNPOW_OK && c && cb) ih_blake3_full((const uint8_t *)c, (size_t)cb, cd); }
    // the buffers are shared storage: hashed in place, in order (the split form's second buffer after the first)
    IhB3 hsh;
    gpu_idle();
    // the buffers are read through the payload window (the guard offset applied; private storage through the staging blit)
    if (knobs_.variant == "swizzle") {   // the items are permuted on the device: the digest walks them in logical order through the slot map
        std::vector<uint8_t> all((size_t)dataset_bytes_); if (!read_payload(dataset_, 0, dataset_bytes_, all.data())) return false;
        const uint32_t iw4 = ih_.item_bytes, n_items = ih_.n_items(), mask = n_items - 1;
        for (uint32_t i = 0; i < n_items; i++) hsh.update(all.data() + (size_t)ih_swizzle_slot(i, mask) * iw4, iw4);
        hsh.finalize(dg); return true;
    }
    const uint64_t chunk = 64ull << 20; std::vector<uint8_t> tmp((size_t)chunk);
    auto feed = [&](id<MTLBuffer> b, uint64_t bytes) { for (uint64_t off = 0; off < bytes; off += chunk) { uint64_t n = bytes - off < chunk ? bytes - off : chunk; if (!read_payload(b, off, n, tmp.data())) return false; hsh.update(tmp.data(), (size_t)n); } return true; };
    if (dataset2_) { const uint64_t half = dataset_bytes_ / 2; if (!feed(dataset_, half) || !feed(dataset2_, dataset_bytes_ - half)) return false; }
    else if (!feed(dataset_, dataset_bytes_)) return false;
    hsh.finalize(dg);
    return true;
}

bool MetalEngineX::prepare_ready() const { return !next_dataset_ || !ih_next_ctx_ || ih_next_cursor_ >= ih_next_items_; }

void MetalEngineX::ih_warm_step(uint32_t items) {
    if (!next_dataset_ || !ih_next_ctx_ || ih_next_cursor_ >= ih_next_items_) return;
    @autoreleasepool {
    uint32_t count = ih_next_items_ - ih_next_cursor_ < items ? ih_next_items_ - ih_next_cursor_ : items;
    const uint32_t half = ih_next_items_ / 2;
    if (next_dataset2_ && ih_next_cursor_ < half && ih_next_cursor_ + count > half) count = half - ih_next_cursor_;   // a slice never crosses the split
    const bool upper = next_dataset2_ && ih_next_cursor_ >= half;
    IhDatasetBatch b{ih_next_cursor_, count, upper ? half : 0, 0, ih_consts_of(ih_next_ih_, ih_next_P_, ih_next_E_)};
    double t0 = now_ms();
    id<MTLCommandBuffer> cb = [queue_ commandBuffer];
    id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
    [enc setComputePipelineState:dag_pso_];
    [enc setBytes:&b length:sizeof(b) atIndex:0];
    [enc setBuffer:(upper ? next_dataset2_ : next_dataset_) offset:pay_off_ atIndex:1];
    [enc setBuffer:(ih_next_light_ ? ih_next_light_ : next_dataset_) offset:(ih_next_light_ ? 0 : pay_off_) atIndex:2];
    [enc dispatchThreads:MTLSizeMake(count, 1, 1) threadsPerThreadgroup:MTLSizeMake([dag_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
    [enc endEncoding]; [cb commit];
    build_last_ = cb;
    ih_next_cursor_ += count; last_table_ms_ += now_ms() - t0;
    }
}

void MetalEngineX::ih_warm_finish() {
    while (!prepare_ready()) ih_warm_step(1u << 18);
    if (build_last_) { [build_last_ waitUntilCompleted]; build_last_ = nil; }
}

bool MetalEngineX::ih_build_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, uint8_t E_out[32], id<MTLBuffer> __strong &light_out, Refusal &r, bool async) {
    int st = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a context (status " + std::to_string(st) + ")"; return false; }
    if (rxe1_ && rx_.memory_free()) {   // schema 5 (section 11): no dataset, no cache; the harness epoch seed as E; a dummy buffer for the kernel argument
        int st3 = 0; uint64_t epn = ignpow_epoch_of(algo_.c_str(), params_c(), height, &st3); rxe1_harness_epoch_seed(rx_.chain_id, epn, E_out);
        light_out = nil;
        dataset = [dev_.metal() newBufferWithLength:4096 options:MTLResourceStorageModeShared]; bytes = 0;
        if (!dataset) { r.code = "memory"; r.explain = "the dummy dataset buffer failed"; return false; }
        return true;
    }
    if (ih_.version == 16 || ih_.version == 17) {   // VERSION 16: no dataset and no cache (the static fabric); VERSION 17: the texture in place of a dataset; a dummy buffer for the kernel argument; E from the context for the record
        if (ih_.version == 17 && !r17_tex_) {   // the static 4096 x 4096 R32Uint texture (texel (x, y) = fmix32(y * 4096 + x)), 64 MiB, host-filled once
            std::vector<uint32_t> tex = tp_texture();
            MTLTextureDescriptor *td = [MTLTextureDescriptor texture2DDescriptorWithPixelFormat:MTLPixelFormatR32Uint width:kTpWidth height:kTpHeight mipmapped:NO];
            td.usage = MTLTextureUsageShaderRead; td.storageMode = MTLStorageModeShared;
            r17_tex_ = [dev_.metal() newTextureWithDescriptor:td];
            if (!r17_tex_) { r.code = "memory"; r.explain = "the version 17 texture (64 MiB R32Uint) was refused"; return false; }
            [r17_tex_ replaceRegion:MTLRegionMake2D(0, 0, kTpWidth, kTpHeight) mipmapLevel:0 withBytes:tex.data() bytesPerRow:(NSUInteger)kTpWidth * 4];
        }
        uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(ctx, E_out, D, cd, dg, &bms, &hd) != IGNPOW_OK) std::memset(E_out, 0, 32);
        light_out = nil; dataset = [dev_.metal() newBufferWithLength:4096 options:MTLResourceStorageModeShared]; bytes = 0;
        if (!dataset) { r.code = "memory"; r.explain = "the dummy dataset buffer failed"; return false; }
        return true;
    }
    const void *e = nullptr; uint64_t eb = 0; uint32_t ib = 0;
    if (ignpow_ctx_light_cache(ctx, &e, &eb, &ib) != IGNPOW_OK || !e) { r.code = "unsupported"; r.explain = "reference exposes no light cache"; return false; }
    if (ih_.version == 0) {
        if (eb != 32) { r.code = "unsupported"; r.explain = "version 0 expects the 32-byte epoch seed as the light cache"; return false; }
        std::memcpy(E_out, e, 32);
        light_out = nil;
    } else {
        if (eb != ih_.cache_bytes || ib != 128) { r.code = "unsupported"; r.explain = "version 1 expects a " + mb(ih_.cache_bytes) + " cache of 128-byte items as the light cache, got " + std::to_string(eb) + " bytes of " + std::to_string(ib); return false; }
        if (rxe1_) { int st3 = 0; uint64_t epn = ignpow_epoch_of(algo_.c_str(), params_c(), height, &st3); rxe1_harness_epoch_seed(rx_.chain_id, epn, E_out); }
        else { uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0;
        if (ignh_dataset_info(ctx, E_out, D, cd, dg, &bms, &hd) != IGNPOW_OK) { r.code = "unsupported"; r.explain = "reference exposes no epoch seed E (ignh_dataset_info)"; return false; } }
        light_out = [dev_.metal() newBufferWithBytes:e length:eb options:MTLResourceStorageModeShared];
        if (!light_out) { r.code = "memory"; r.explain = "cache allocation of " + mb(eb) + " failed"; r.need_bytes = eb; return false; }
    }
    bytes = ih_.dataset_bytes;
    uint64_t ref_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st == IGNPOW_OK && ref_bytes != bytes) { r.code = "unsupported"; r.explain = "dataset size disagrees with the reference: " + std::to_string(bytes) + " vs " + std::to_string(ref_bytes); return false; }
    if (bytes > [dev_.metal() maxBufferLength]) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " exceeds the device's largest buffer"; return false; }
    const bool split2 = knobs_.variant == "split2" || knobs_.alloc == "split2";
    uint64_t each = split2 ? bytes / 2 : bytes;
    dataset = alloc_dataset(dataset, each, "dataset", r);
    if (!dataset) return false;
    if (split2) { dataset2_ = alloc_dataset(dataset2_, each, "dataset2", r); if (!dataset2_) return false; } else dataset2_ = nil;
    IhConsts k = ih_consts_of(ih_, ih_P_, E_out);
    uint32_t items = ih_.n_items();
    const uint32_t per = 1u << 18;
    const bool build8 = knobs_.variant == "build8" && ih_.version >= 1;
    id<MTLBuffer> gather = build8 ? [dev_.metal() newBufferWithLength:(NSUInteger)per * 128 + 4096 options:MTLResourceStorageModePrivate] : nil;
    double t0 = now_ms();
    for (uint32_t first = 0; first < items; first += per) { @autoreleasepool {
        IhDatasetBatch b{first, items - first < per ? items - first : per, (split2 && first >= items / 2) ? items / 2 : 0, 0, k};
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:dag_pso_];
        [enc setBytes:&b length:sizeof(b) atIndex:0];
        [enc setBuffer:((split2 && first >= items / 2) ? dataset2_ : dataset) offset:pay_off_ atIndex:1];
        [enc setBuffer:(light_out ? light_out : dataset) offset:(light_out ? 0 : pay_off_) atIndex:2];
        if (build8) { [enc setBuffer:gather offset:0 atIndex:3]; uint32_t tpg = ih_.version >= 12 ? 16u : knobs_.threadgroup * simd_width_; uint64_t tgs = ((uint64_t)b.count * 8 + tpg - 1) / tpg; /* version 12: two octets per threadgroup (32 KB) */ [enc dispatchThreadgroups:MTLSizeMake(tgs, 1, 1) threadsPerThreadgroup:MTLSizeMake(tpg, 1, 1)]; }
        else [enc dispatchThreads:MTLSizeMake(b.count, 1, 1) threadsPerThreadgroup:MTLSizeMake([dag_pso_ maxTotalThreadsPerThreadgroup], 1, 1)];
        [enc endEncoding]; [cb commit];
        if (async) { build_last_ = cb; continue; }   // the mining continues on queue_; the swap waits on build_last_
        [cb waitUntilCompleted];
        if ([cb status] != MTLCommandBufferStatusCompleted) { r.code = "driver"; r.explain = "igneumhash dataset command buffer failed"; return false; }
    } }
    last_table_ms_ = now_ms() - t0;
    return true;
}

bool MetalEngineX::open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) {
    // A reconfigure of the same IgneumHash variant rebuilds the dataset in the resident buffer: no release,
    // so no wait on macOS taking the old pages back (a 4.6 GB release took longer than the 3 s settle and
    // the horizon reopen of the 4 GiB memory row was refused against our own freed dataset).
    if (cand_ && cand_->kind == Kind::igneumhash && !rxe1_ && algo_ == algo && knobs_.variant == k.variant && ih_next_ctx_ && !ih_next_params_.empty() && k.params == ih_next_params_ && dataset_ && search_pso_ && dag_pso_) {
        // the warm parameter change: the configure names the document the next dataset was prepared for (prepare.params)
        int st = 0; uint64_t ep = ignpow_epoch_of(algo.c_str(), k.params.c_str(), height, &st);
        if (st == IGNPOW_OK && ep == ignpow_ctx_epoch(ih_next_ctx_)) {
            double w0 = now_ms(); ih_warm_finish(); last_swap_wait_ms_ = now_ms() - w0;
            const uint32_t old_scratch = ih_.scratch_bytes, old_st_words = ih_.st_words();
            if (ctx_) ignpow_ctx_free(ctx_);
            ctx_ = ih_next_ctx_; ih_next_ctx_ = nullptr; retire_dataset(dataset_); retire_dataset(dataset2_); dataset_ = next_dataset_; dataset2_ = next_dataset2_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nil; next_dataset2_ = nil; next_dataset_bytes_ = 0;
            light_ = ih_next_light_; ih_next_light_ = nil; std::memcpy(ih_E_, ih_next_E_, 32);
            ih_ = ih_next_ih_; std::memcpy(ih_P_, ih_next_P_, 32); params_ = k.params; knobs_ = k; height_ = height; next_height_ = ~0ull; ih_next_params_.clear();
            if (ih_.scratch_bytes != old_scratch || ih_.st_words() != old_st_words) {   // the state area is 4 (8 RW + 32) bytes per slot (640 at the 16-register state): a wider document re-allocates it
                ih_slots_ = (uint32_t)((256ull << 20) / (ih_eff_scratch() ? ih_eff_scratch() : 4ull * ih_.st_words()));
                uint64_t scratch_bytes = (uint64_t)ih_slots_ * ih_eff_scratch(), st_bytes = (uint64_t)ih_slots_ * 4 * ih_.st_words();
                ih_scratch_ = [dev_.metal() newBufferWithLength:(scratch_bytes ? scratch_bytes : 4096) options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
                ih_st_ = [dev_.metal() newBufferWithLength:st_bytes options:(MTLResourceStorageModeShared | MTLResourceHazardTrackingModeUntracked)];
                if (!ih_scratch_ || !ih_st_) { r.code = "memory"; r.explain = "scratch allocation of " + mb(scratch_bytes + st_bytes) + " failed"; close(); return false; }
            }
            if (ih_.version * 1024u + ih_.item_words() + ih_.rw() * 65536u + r0d_key(ih_, knobs_, r0d_height_) + ((knobs_.variant == "v8s" && ih_.version == 8) ? (ih_.registers * 7u + ih_.blocks * 131u + ih_.features) * 4194304u : 0u) != ih_built_version_) {   // the state width (IGM_IH_RW) is compiled in too   // the round's version is compiled in (IGM_IH_VERSION): rebuild the pipelines for the new document
                search_pso_ = nil; dag_pso_ = nil;
                if (!build_pipelines(r)) { close(); return false; }
            }
            fprintf(stderr, "metal: parameter change adopted warm (the dataset prepared for the new document; swap wait %.0f ms)\n", last_swap_wait_ms_);
            return true;
        }
    }
    if (cand_ && (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) && algo_ == algo && params_ == k.params && knobs_.variant == k.variant && dataset_ && search_pso_ && dag_pso_) {
        knobs_ = k; height_ = height;
        if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
        if (ih_next_ctx_) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; }
        next_dataset_ = nil; next_dataset_bytes_ = 0; ih_next_light_ = nil; next_height_ = ~0ull; light_ = nil;
        if (!ih_build_dataset(height, ctx_, dataset_, dataset_bytes_, ih_E_, light_, r)) { close(); return false; }
        return true;
    }
    close();
    algo_ = algo; knobs_ = k; params_ = k.params; height_ = height;
    cand_ = find_candidate(algo.c_str());
    if (!cand_ || cand_->lane_model != LaneModel::independent) { r.code = "unsupported"; r.explain = "not an independent-lane candidate: " + algo; return false; }
    int st = 0;
    rxe1_ = cand_->kind == Kind::rxe1;
    if (rxe1_) {
        std::string perr;
        if (!rxe1_parse_params(params_, rx_, perr)) { r.code = "unsupported"; r.explain = "params: " + perr; return false; }
        
        rxe1_pid(rx_, rx_pid_);
        ih_ = IhParams(); ih_.version = 1; ih_.dataset_bytes = rx_.dataset_bytes; ih_.scratch_bytes = 0; ih_.cache_bytes = rx_.cache_bytes; ih_.parents = rx_.parents; ih_.rounds = rx_.rounds; ih_.epoch_blocks = rx_.epoch_blocks; ih_.chain_id = rx_.chain_id;
        std::memset(ih_P_, 0, 32);
    }
    if (cand_->kind == Kind::igneumhash || rxe1_) {
        std::string perr;
        if (!rxe1_ && !ih_parse_params(params_, ih_, perr)) { r.code = "unsupported"; r.explain = "params: " + perr; return false; }
        // The M5 family's Metal compiler miscompiles the keyed kernel (12 Oct 2026: the digest's first word zero on every keyed vector and
        // every protocol-mode hit on the M5 Max, the mix exact, the M6 exact; the self-check of thirty-two nonces under two headers
        // passes on that chip while the vectors' header fails, so the check is no guard there): a keyed document is refused on the M5
        // family by the device's name until a worker build names the chip exact (IGM_KEYED_CHIP_OK=1 in the environment lifts it for a
        // reading on the bench, never for mining).
        if (!rxe1_ && ih_.keyed_rounds > 0 && !getenv("IGM_KEYED_CHIP_OK")) {
            const std::string nm = dev_.info().name;
            if (nm.find("M5") != std::string::npos) { r.code = "keyed-chip"; r.explain = "a keyed document (features bit 10) is refused on " + nm + ": this chip's Metal compiler miscompiles the keyed kernel (the digest's first word zero, the mix exact); the worker will not mine a keyed document on it until a worker build names the chip exact"; return false; }
        }
        if (!rxe1_ && ih_.version >= 3) {   // version 3 (the item width): the stock eight-lane kernel serves it; the other forms refuse by name
            const std::string &v = knobs_.variant;
            if (v == "n1" || v == "ctx2" || v == "ctx4" || v == "build8" || v == "diag-a" || v == "diag-b" || v == "diag-a-notail" || v == "diag-b-notail" || v == "diag-a-rand" || v == "diag-a-rand-notail" || v == "diag-a-ord" || v == "diag-a-ord-notail" || v == "diag-a-split" || v.rfind("diag-sg", 0) == 0 || v == "reach-half" || v == "split2") { r.code = "unsupported"; r.explain = "variant " + v + " is not implemented for version 3 (item_bytes); the stock kernel serves it"; return false; }
        }
        if (!rxe1_) ih_P(ih_, ih_P_);
        uint64_t bytes = ih_.dataset_bytes;
        ih_slots_ = (uint32_t)((256ull << 20) / (ih_eff_scratch() ? ih_eff_scratch() : 4ull * ih_.st_words()));   // a 256 MiB scratch budget bounds the batch
        if (ih_.version >= 15) ih_slots_ = 16384;   // version 15 uses no scratch and no state area: the slots bound the batch alone (the 256 MiB budget was the mini's margin)
        uint64_t scratch_bytes = (uint64_t)ih_slots_ * ih_eff_scratch(), st_bytes = (uint64_t)ih_slots_ * (rxe1_ ? 2048 : 4 * ih_.st_words());   // the state area 4 (8 RW + 32) bytes per slot (640 at 16)
        uint64_t working_set = bytes + (ih_.version >= 1 ? ih_.cache_bytes : 0) + scratch_bytes + st_bytes + 2 * (sizeof(IhJob) + max_results_ * sizeof(IhResult) + 4096) + (64ull << 20);
        uint64_t cap = supervisor_budget;
        if (k.working_set_mb && (!cap || (k.working_set_mb << 20) < cap)) cap = k.working_set_mb << 20;
        // a reconfigure that keeps the resident dataset buffer (above) is measured for what it ADDS: the budget's device term already
        // subtracts what this process holds, so the resident bytes counted again read a budget of 0.0 on a loaded machine (the founder's
        // M5 Max, 15 October 2026 18:0x UK, the product team's reading: the second configure without a close)
        const uint64_t resident = (dataset_ && dataset_bytes_ <= working_set) ? dataset_bytes_ : 0;
        BudgetDecision b = dev_.budget_settled(working_set - resident, knobs_.no_warm ? 0 : bytes, cap, 0, 3000);   // the next epoch's dataset counted only when a warm build will come (the vectors and trace runs never warm: version 15's 4 GiB twice refused the mini for room, 13 Oct 2026)
        if (!b.fits) { r.code = "memory"; r.explain = "unsafe allocation refused: " + b.explain; r.need_bytes = working_set; r.have_bytes = b.limit; return false; }
        if (!build_pipelines(r)) { close(); return false; }
        if (!ih_build_dataset(height, ctx_, dataset_, dataset_bytes_, ih_E_, light_, r)) { close(); return false; }
        ih_scratch_ = [dev_.metal() newBufferWithLength:(scratch_bytes ? scratch_bytes : 4096) options:(MTLResourceStorageModePrivate)];
        if (getenv("IGM_IH_TRACE")) ih_trace_ = [dev_.metal() newBufferWithLength:4096 options:MTLResourceStorageModeShared];
        ih_st_ = [dev_.metal() newBufferWithLength:st_bytes options:(MTLResourceStorageModePrivate)];
        if (!ih_scratch_ || !ih_st_) { r.code = "memory"; r.explain = "scratch allocation of " + mb(scratch_bytes + st_bytes) + " failed"; close(); return false; }
        for (uint32_t i = 0; i < knobs_.inflight; i++) {
            job_buf_[i] = [dev_.metal() newBufferWithLength:(sizeof(IhJob) > sizeof(RxJob) ? sizeof(IhJob) : sizeof(RxJob)) options:MTLResourceStorageModeShared];
            results_[i] = [dev_.metal() newBufferWithLength:max_results_ * sizeof(IhResult) options:MTLResourceStorageModeShared];
            count_[i] = [dev_.metal() newBufferWithLength:4 options:MTLResourceStorageModeShared];
        }
        return true;
    }
    uint64_t bytes = cand_->kind == Kind::autolykos ? (uint64_t)autolykos_n(height) * 32 : ignpow_dataset_bytes(algo.c_str(), params_c(), height, &st);
    uint64_t cache = cand_->kind == Kind::autolykos ? 0 : ignpow_cache_bytes(algo.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference: height " + std::to_string(height) + " outside the schedule"; return false; }
    uint64_t working_set = bytes + cache + 2 * (sizeof(AlJob) + max_results_ * sizeof(AlResult) + 4096) + (64ull << 20);
    uint64_t cap = supervisor_budget;
    if (k.working_set_mb && (!cap || (k.working_set_mb << 20) < cap)) cap = k.working_set_mb << 20;
    BudgetDecision b = dev_.budget_settled(working_set, cand_->kind == Kind::autolykos ? bytes : 0, cap, 0, 3000);
    if (!b.fits) { r.code = "memory"; r.explain = "unsafe allocation refused: " + b.explain; r.need_bytes = working_set; r.have_bytes = b.limit; return false; }
    if (!build_pipelines(r)) { close(); return false; }
    double ms = 0;
    bool ok = cand_->kind == Kind::fishhash ? build_fishhash_dataset(height, ctx_, dataset_, dataset_bytes_, r) : build_autolykos_table(height, dataset_, dataset_bytes_, ms, r);
    if (!ok) { close(); return false; }
    for (int i = 0; i < 2; i++) {
        job_buf_[i] = [dev_.metal() newBufferWithLength:sizeof(AlJob) options:MTLResourceStorageModeShared];
        results_[i] = [dev_.metal() newBufferWithLength:max_results_ * sizeof(AlResult) options:MTLResourceStorageModeShared];
        count_[i] = [dev_.metal() newBufferWithLength:4 options:MTLResourceStorageModeShared];
    }
    return true;
}

bool MetalEngineX::prepare(uint64_t height, Refusal &r, const std::string &params) {
    // a parameter change announced ahead (the product team, 10 Oct 2026): the next dataset is built for that document and kept
    // across the configure that names it; until then the resident document and pipelines are untouched
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
        BudgetDecision b = dev_.budget_settled(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0, 1000);
        if (!b.warm_fits) { r.code = "memory"; r.explain = "cold transition: " + b.explain; r.need_bytes = allocated_bytes() + next_bytes; r.have_bytes = b.limit; return false; }
        // the next epoch's context, cache and E now; its dataset in slices on the mining queue (ih_warm_step), one slice behind
        // every mining batch, so the rate holds while it builds and the swap pays only the slices left
        int st2 = 0; ignpow_ctx *nc = ignpow_ctx_create(algo_.c_str(), npc, height, 0, &st2);
        if (!nc) { r.code = "unsupported"; r.explain = "reference refused a context (status " + std::to_string(st2) + ")"; return false; }
        const void *e = nullptr; uint64_t eb = 0; uint32_t ib = 0;
        if (ignpow_ctx_light_cache(nc, &e, &eb, &ib) != IGNPOW_OK || !e) { ignpow_ctx_free(nc); r.code = "unsupported"; r.explain = "reference exposes no light cache"; return false; }
        uint8_t ne[32]; id<MTLBuffer> nl = nil;
        if (np.version == 0) std::memcpy(ne, e, 32);
        else {
            if (rxe1_) { uint64_t epn = ignpow_epoch_of(algo_.c_str(), npc, height, &st2); rxe1_harness_epoch_seed(rx_.chain_id, epn, ne); }
            else { uint8_t D[32], cd[32], dg[32]; uint64_t bms = 0; int hd = 0; if (ignh_dataset_info(nc, ne, D, cd, dg, &bms, &hd) != IGNPOW_OK) { ignpow_ctx_free(nc); r.code = "unsupported"; r.explain = "no epoch seed E"; return false; } }
            nl = [dev_.metal() newBufferWithBytes:e length:eb options:MTLResourceStorageModeShared];
            if (!nl) { ignpow_ctx_free(nc); r.code = "memory"; r.explain = "the next cache failed"; return false; }
        }
        const bool split2 = knobs_.variant == "split2" || knobs_.alloc == "split2";
        const uint64_t each = split2 ? np.dataset_bytes / 2 : np.dataset_bytes;
        id<MTLBuffer> nd = alloc_dataset(nil, each, "next dataset", r), nd2 = nil;
        if (!nd) { ignpow_ctx_free(nc); return false; }
        if (split2) { nd2 = alloc_dataset(nil, each, "next dataset2", r); if (!nd2) { ignpow_ctx_free(nc); return false; } }
        if (ih_next_ctx_) ignpow_ctx_free(ih_next_ctx_);
        ih_next_ctx_ = nc; next_dataset_ = nd; next_dataset2_ = nd2; next_dataset_bytes_ = np.dataset_bytes; ih_next_light_ = nl; std::memcpy(ih_next_E_, ne, 32); next_height_ = height;
        ih_next_cursor_ = 0; ih_next_items_ = np.n_items(); last_table_ms_ = 0;
        ih_next_ih_ = np; std::memcpy(ih_next_P_, nP, 32); ih_next_params_ = change ? params : std::string();
        return true;
    }
    uint64_t next_bytes = (uint64_t)autolykos_n(height) * 32;
    BudgetDecision b = dev_.budget_settled(allocated_bytes(), next_bytes, knobs_.working_set_mb ? (knobs_.working_set_mb << 20) : 0, 0, 1000);
    if (!b.warm_fits) { r.code = "memory"; r.explain = "cold transition: " + b.explain; r.need_bytes = allocated_bytes() + next_bytes; r.have_bytes = b.limit; return false; }
    id<MTLBuffer> t = nil; uint64_t nb = 0; double ms = 0;
    if (!build_autolykos_table(height, t, nb, ms, r)) return false;
    next_dataset_ = t; next_dataset_bytes_ = nb; next_height_ = height; next_n_ = autolykos_n(height); next_cursor_ = next_n_; next_cb_ = nil;
    return true;
}

void MetalEngineX::close() {
    search_pso_ = nil; table_pso_ = nil; dag_pso_ = nil;
    dataset_ = nil; next_dataset_ = nil; light_ = nil; ih_scratch_ = nil; ih_st_ = nil; ih_trace_ = nil; ih_next_light_ = nil; rx_sched_ = nil; rx_rsched_ = nil; rx_epoch_ = ~0ull; dataset2_ = nil;
    next_dataset2_ = nil; spare_ = nil; spare2_ = nil; staging_ = nil; trace_pso_ = nil; trace_full_ = nil; trace_words_ = 0; use_trace_ = false;
    if (ih_next_ctx_) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; }
    for (int i = 0; i < 8; i++) { job_buf_[i] = nil; results_[i] = nil; count_[i] = nil; }
    if (build_last_) { [build_last_ waitUntilCompleted]; build_last_ = nil; }
    ih_next_cursor_ = 0; ih_next_items_ = 0;
    if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
    dataset_bytes_ = 0; next_dataset_bytes_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0; next_cb_ = nil;
}

uint32_t MetalEngineX::hashes_for_duration() const {
    uint32_t threads_per_tg = ih_threads_per_tg();
    uint32_t lanes = (cand_ && cand_->kind == Kind::fishhash) ? fh_lanes_ : (cand_ && cand_->kind == Kind::rxe1) ? rx_.group : (cand_ && cand_->kind == Kind::igneumhash) ? (ih_.forge() ? 16u : (knobs_.variant == "n1" || ih_.version >= 16) ? 1u : 8u) : 1u;
    uint32_t tgs = 64;
    if (last_gpu_ms_ > 0 && last_hashes_ > 0) {
        double want = (double)last_hashes_ * (double)knobs_.batch_ms / last_gpu_ms_;
        tgs = (uint32_t)(want * lanes / threads_per_tg); if (tgs < 1) tgs = 1; if (tgs > (1u << 20)) tgs = 1u << 20;
    }
    return tgs * threads_per_tg / lanes;
}

// version 12's shared table (SPEC-R0E-v0 2c, main 2a12dee9): shared_kib x 256 words per (period, anchor), filled once by the fill kernel from the
// period's seed; a 16-byte stand-in buffer when the document has none (buffer 8 is unconditional under versions 12 to 14)
void MetalEngineX::r12_ensure_shared() {
    const NSUInteger bytes = ih_.shared_words() ? (NSUInteger)ih_.shared_words() * 4 : 16;
    if (!r12_shared_) { r12_shared_ = [dev_.metal() newBufferWithLength:bytes options:MTLResourceStorageModePrivate]; r12_period_ = ~0ull; }
    if (!r12_seed_) r12_seed_ = [dev_.metal() newBufferWithLength:32 options:MTLResourceStorageModeShared];
    if (!r12_batch_) r12_batch_ = [dev_.metal() newBufferWithLength:sizeof(IhDatasetBatch) options:MTLResourceStorageModeShared];
}
// VERSION 15: the period key's eight words and the packed programme (r0f_period) into their buffers; at the job's start and on every period change
bool MetalEngineX::r0f_fill_period(uint64_t height) {
    uint8_t key[32]; std::vector<uint32_t> prog; std::string e;
    if (ih_.version == 17) { if (!r17_key(ih_, knobs_, height, key, e)) { fprintf(stderr, "metal: version 17 period: %s\n", e.c_str()); return false; } prog.assign(4, 0u); }   // the texture form: the key alone, a dummy programme buffer
    else if (!r0f_period(ih_, knobs_, height, key, prog, e)) { fprintf(stderr, "metal: version 15 period: %s\n", e.c_str()); return false; }
    uint32_t kw[8]; for (int i = 0; i < 8; i++) kw[i] = (uint32_t)key[4 * i] | ((uint32_t)key[4 * i + 1] << 8) | ((uint32_t)key[4 * i + 2] << 16) | ((uint32_t)key[4 * i + 3] << 24);
    r0f_key_ = [dev_.metal() newBufferWithBytes:kw length:32 options:MTLResourceStorageModeShared];
    r0f_prog_ = [dev_.metal() newBufferWithBytes:prog.data() length:prog.size() * 4 options:MTLResourceStorageModeShared];
    if (!r0f_key_ || !r0f_prog_) return false;
    r0f_period_ = ih_.period_blocks ? height / ih_.period_blocks : 0;
    return true;
}
bool MetalEngineX::r12_fill_shared(uint64_t height) {
    if (ih_.version < 12 || !ih_.shared_words()) return true;
    const uint64_t period = ih_.period_blocks ? height / ih_.period_blocks : 0;
    if (r12_period_ == period && r12_shared_) return true;
    r12_ensure_shared(); if (!r12_fill_pso_ || !r12_shared_ || !dataset_) return false;
    uint8_t seed[32]; std::string err; if (!r12_shared_seed(ih_, knobs_, height, seed, err)) { fprintf(stderr, "metal: shared table seed: %s\n", err.c_str()); return false; }
    uint32_t *s8 = (uint32_t *)[r12_seed_ contents]; for (int i = 0; i < 8; i++) s8[i] = (uint32_t)seed[4 * i] | ((uint32_t)seed[4 * i + 1] << 8) | ((uint32_t)seed[4 * i + 2] << 16) | ((uint32_t)seed[4 * i + 3] << 24);
    IhDatasetBatch b{0, 0, 0, 0, ih_consts_of(ih_, ih_P_, ih_E_)}; std::memcpy([r12_batch_ contents], &b, sizeof b);
    const uint32_t words = ih_.shared_words();
    id<MTLCommandBuffer> cb = [queue_ commandBuffer]; id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
    [enc setComputePipelineState:r12_fill_pso_]; [enc setBuffer:r12_batch_ offset:0 atIndex:0]; [enc setBuffer:dataset_ offset:pay_off_ atIndex:1]; [enc setBuffer:r12_seed_ offset:0 atIndex:2];
    [enc setBuffer:r12_shared_ offset:0 atIndex:3]; [enc setBytes:&words length:4 atIndex:4]; if (dataset2_) [enc setBuffer:dataset2_ offset:pay_off_ atIndex:5];
    [enc dispatchThreads:MTLSizeMake(1, 1, 1) threadsPerThreadgroup:MTLSizeMake(1, 1, 1)]; [enc endEncoding]; [cb commit]; [cb waitUntilCompleted];
    if ([cb status] != MTLCommandBufferStatusCompleted) return false;
    r12_period_ = period;
    if (getenv("IGM_R12_DUMP")) {   // the diagnostic read-back (the OpenCL engine's form): the table's BLAKE3 digest and its edge words against the reference trace's shared_table record
        std::vector<uint32_t> t(words); if (raw_read(r12_shared_, 0, (uint64_t)words * 4, t.data())) {
            uint8_t dg[32]; ih_blake3_full(reinterpret_cast<const uint8_t *>(t.data()), (size_t)words * 4, dg); char hx[65]; for (int i = 0; i < 32; i++) snprintf(hx + 2 * i, 3, "%02x", dg[i]);
            fprintf(stderr, "r12-dump: period %llu words %u digest %s words0..3 %u %u %u %u words64..67 %u %u %u %u last4 %u %u %u %u %s\n", (unsigned long long)period, words, hx, t[0], t[1], t[2], t[3], t[64], t[65], t[66], t[67], t[words - 4], t[words - 3], t[words - 2], t[words - 1], dataset2_ ? "(two buffers)" : "(one buffer)");
        } else fprintf(stderr, "r12-dump: read-back refused\n");
    }
    return true;
}

void MetalEngineX::r0e_optrace_print(const Job &job) {   // the first octet's half: round r, op i, lane l, sixteen words at ((r * ops + i) * 8 + l) * 16, as hex words
    const size_t half = (size_t)2 * ih_.program_ops * 128; const uint32_t *w = (const uint32_t *)[r0e_optrace_buf_ contents];
    std::string out; out.reserve(half * 9); char b[12];
    for (size_t q = 0; q < half; q++) { snprintf(b, sizeof b, "%08x", w[q]); out += b; }
    fprintf(stderr, "r0e optrace nonce %llu height %llu ops %u: %s\n", (unsigned long long)job.nonce_start, (unsigned long long)job.height, ih_.program_ops, out.c_str());
}

void MetalEngineX::ih_run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                              const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    int st = 0;
    struct R0eOptracePrint { MetalEngineX *e; const Job *j; bool on; ~R0eOptracePrint() { if (on) e->r0e_optrace_print(*j); } } r0e_tp{this, &job, cand_ && cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14 && r0e_optrace_on_ && job.nonce_count == 1};
    if (r0e_tp.on) { if (!r0e_optrace_buf_) { r0e_optrace_words_ = (size_t)4 * ih_.program_ops * 128; r0e_optrace_buf_ = [dev_.metal() newBufferWithLength:r0e_optrace_words_ * 4 options:MTLResourceStorageModeShared]; } std::memset([r0e_optrace_buf_ contents], 0, r0e_optrace_words_ * 4); }
    uint64_t job_epoch = ignpow_epoch_of(algo_.c_str(), params_c(), job.height, &st);
    if (st != IGNPOW_OK) return;
    if (job_epoch != ignpow_ctx_epoch(ctx_)) {   // a new epoch: the warm one when prepared, else a cold rebuild (its time is the downtime)
        if (ih_next_ctx_ && ih_next_params_.empty() && ignpow_ctx_epoch(ih_next_ctx_) == job_epoch) {
            double w0 = now_ms();
            ih_warm_finish();
            last_swap_wait_ms_ = now_ms() - w0;
            ignpow_ctx_free(ctx_); ctx_ = ih_next_ctx_; ih_next_ctx_ = nullptr;
            retire_dataset(dataset_); retire_dataset(dataset2_);
            dataset_ = next_dataset_; dataset2_ = next_dataset2_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nil; next_dataset2_ = nil; next_dataset_bytes_ = 0;
            light_ = ih_next_light_; ih_next_light_ = nil; std::memcpy(ih_E_, ih_next_E_, 32); height_ = job.height; next_height_ = ~0ull; r12_period_ = ~0ull;   // the new epoch's dataset: the shared table refills even inside one period
        } else {
            Refusal r; id<MTLBuffer> nd = nil, nl = nil; uint64_t nb = 0; ignpow_ctx *nc = nullptr; uint8_t ne[32];
            if (ih_next_ctx_ && ih_next_params_.empty()) { ignpow_ctx_free(ih_next_ctx_); ih_next_ctx_ = nullptr; next_dataset_ = nil; next_dataset2_ = nil; next_dataset_bytes_ = 0; ih_next_light_ = nil; }
            // the cold rebuild (the single-buffer control): under persist "pool" the resident buffer is rebuilt in place (no free, no allocate); else freed first
            if (!persist_pool()) { retire_dataset(dataset_); retire_dataset(dataset2_); } else nd = dataset_;
            dataset_bytes_ = 0; light_ = nil;
            if (!ih_build_dataset(job.height, nc, nd, nb, ne, nl, r)) { if (nc) ignpow_ctx_free(nc); fprintf(stderr, "metal: igneumhash dataset for height %llu: %s\n", (unsigned long long)job.height, r.explain.c_str()); return; }
            ignpow_ctx_free(ctx_); ctx_ = nc; dataset_ = nd; dataset_bytes_ = nb; light_ = nl; std::memcpy(ih_E_, ne, 32); height_ = job.height; r12_period_ = ~0ull;   // the new epoch's dataset: the shared table refills even inside one period
        }
    }
    // versions 12 to 14: the period's shared table is filled from the dataset of the JOB's epoch, so after the epoch swap above (14 Oct 2026, the Metal round fault:
    // filled before it, the first job of an epoch read a table built from the previous or the unbuilt dataset and cached it for the period; the per-operation trace
    // against the 9060 XT placed the divergence at round 0 op 1, the first shared GATHER, every lane; the OpenCL engine took this order on 13 Oct)
    if (cand_->kind == Kind::igneumhash && ih_.version >= 12 && !r12_fill_shared(job.height)) { fprintf(stderr, "metal: the period's shared table could not be filled\n"); return; }
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
    }
    RxJob rbase{};
    if (rxe1_) {
        uint64_t epn = ignpow_ctx_epoch(ctx_);
        if (rx_epoch_ != epn || !rx_sched_) {
            uint8_t ps[32] = {0}; std::vector<uint32_t> sched;
            if (rx_.blocks > 0) { rxe1_program_seed(rx_, rx_pid_, epn, ih_E_, ps); rxe1_schedule(rx_, ps, sched); }
            if (sched.empty()) sched.push_back(0u);
            rx_sched_ = [dev_.metal() newBufferWithBytes:sched.data() length:sched.size() * 4 options:MTLResourceStorageModeShared];
            std::vector<uint32_t> rdesc;
            if (rx_.schema_version() >= 3 && rx_.route == 2) { uint8_t rs[32]; rxe1_route_seed(rx_, rx_pid_, epn, ih_E_, rs); rxe1_route_descriptors(rx_, rs, rdesc); }
            if (rdesc.empty()) rdesc.push_back(0u);
            rx_rsched_ = [dev_.metal() newBufferWithBytes:rdesc.data() length:rdesc.size() * 4 options:MTLResourceStorageModeShared];
            rx_epoch_ = epn;
        }
        for (int i = 0; i < 8; i++) { rbase.header[i] = base.header[i]; rbase.target[i] = base.target[i]; }
        rbase.max_results = max_results_; rbase.slots = ih_slots_; rbase.k = rx_consts_of(rx_, rx_pid_, epn, ih_E_);
    }
    uint64_t done = 0; int slot = 0;
    const uint32_t NF = knobs_.inflight < 1 ? 1 : knobs_.inflight > 8 ? 8 : knobs_.inflight;   // T-B9: batches in flight
    id<MTLCommandBuffer> inflight[8] = {nil};
    uint32_t inflight_hashes[8] = {0}; uint64_t inflight_nonce[8] = {0}; double wall_start[8] = {0};
    std::vector<std::pair<uint64_t, uint32_t>> redo;
    const uint32_t max_batch = ih_slots_ / NF;   // every batch in flight has its own scratch and state slots
    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) { @autoreleasepool {   // the pool: a command buffer and its encoder are autoreleased; without a pool on this thread every one lives for the thread's life and holds the dataset it encoded (one dataset leaked per epoch; or-0-guard-1c)
        if (rxe1_) { RxJob *rj = (RxJob *)[job_buf_[s] contents]; *rj = rbase; rj->nonce_lo = (uint32_t)nonce; rj->nonce_hi = (uint32_t)(nonce >> 32); rj->hashes = hashes; rj->slots = max_batch; }
        else { IhJob *gj = (IhJob *)[job_buf_[s] contents]; *gj = base; gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->hashes = hashes; gj->scratch_slots = max_batch; }
        ((uint32_t *)[count_[s] contents])[0] = 0;
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:(use_trace_ && trace_pso_) ? trace_pso_ : search_pso_];
        [enc setBuffer:job_buf_[s] offset:0 atIndex:0];
        [enc setBuffer:dataset_ offset:pay_off_ atIndex:1];
        [enc setBuffer:results_[s] offset:0 atIndex:2];
        [enc setBuffer:count_[s] offset:0 atIndex:3];
        if (knobs_.variant.rfind("diag-sgtg-", 0) == 0) [enc setThreadgroupMemoryLength:(NSUInteger)(knobs_.threadgroup * 32u * 16u) atIndex:0];
        if (cand_ && cand_->kind == Kind::igneumhash && ih_.forge()) [enc setThreadgroupMemoryLength:(NSUInteger)(ih_threads_per_tg() / 16u) * ih_.forge_local_bytes() atIndex:0];   // VERSION 20: the 16 KiB scratchpad an attempt, sixteen threads an attempt   // 512 bytes of staging per SIMD group (the threadgroup-memory control)
        NSUInteger st_slot = rxe1_ ? 2048 : 4 * ih_.st_words(), scr_slot = ih_.scratch_bytes;   // slot s's areas at s * max_batch slots
        NSUInteger st_off = (NSUInteger)s * max_batch * st_slot, scr_off = (NSUInteger)s * max_batch * scr_slot;
        if (rxe1_) { [enc setBuffer:rx_sched_ offset:0 atIndex:4]; [enc setBuffer:ih_st_ offset:st_off atIndex:5]; [enc setBuffer:rx_rsched_ offset:0 atIndex:6]; }
        else { [enc setBuffer:ih_scratch_ offset:scr_off atIndex:4]; [enc setBuffer:ih_st_ offset:st_off atIndex:5]; id<MTLBuffer> tb = use_trace_ ? trace_full_ : ih_trace_; if (tb) [enc setBuffer:tb offset:0 atIndex:6]; if (dataset2_) [enc setBuffer:dataset2_ offset:pay_off_ atIndex:7]; if (ih_.version >= 15) { [enc setBuffer:r0f_key_ offset:0 atIndex:9]; [enc setBuffer:r0f_prog_ offset:0 atIndex:10]; uint32_t nops = ih_.program_ops; [enc setBytes:&nops length:4 atIndex:11]; if (ih_.version == 17) [enc setTexture:r17_tex_ atIndex:0]; } else if (ih_.version >= 12) { if (!r12_shared_) r12_ensure_shared(); [enc setBuffer:r12_shared_ offset:0 atIndex:8]; if (r0e_optrace_on_) { if (!r0e_optrace_buf_) { r0e_optrace_words_ = (size_t)4 * ih_.program_ops * 128; r0e_optrace_buf_ = [dev_.metal() newBufferWithLength:r0e_optrace_words_ * 4 options:MTLResourceStorageModeShared]; } [enc setBuffer:r0e_optrace_buf_ offset:0 atIndex:13]; } } }
        uint32_t threads_per_tg = ih_threads_per_tg();
        const uint32_t ctx = rxe1_ ? 1u : ih_ctx();   // T-B10a: ctx nonces per eight-lane group
        uint64_t total_threads = (((uint64_t)hashes + ctx - 1) / ctx) * (rxe1_ ? rx_.group : (ih_.forge() ? 16u : (knobs_.variant == "n1" || ih_.version >= 16) ? 1u : 8u));
        uint64_t tgs = (total_threads + threads_per_tg - 1) / threads_per_tg;
        [enc dispatchThreadgroups:MTLSizeMake(tgs, 1, 1) threadsPerThreadgroup:MTLSizeMake(threads_per_tg, 1, 1)];
        [enc endEncoding];
        wall_start[s] = now_ms(); [cb commit];
        inflight[s] = cb; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
    } };
    auto collect = [&](int s) { @autoreleasepool {
        id<MTLCommandBuffer> cb = inflight[s];
        if (!cb) return;
        [cb waitUntilCompleted];
        BatchStats stt; stt.hashes = inflight_hashes[s]; stt.gpu_ms = ([cb GPUEndTime] - [cb GPUStartTime]) * 1000.0; stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        inflight[s] = nil;
        if ([cb status] != MTLCommandBufferStatusCompleted) { fprintf(stderr, "metal: igneumhash batch at nonce %llu failed: status %ld\n", (unsigned long long)inflight_nonce[s], (long)[cb status]); return; }
        uint32_t n = ((uint32_t *)[count_[s] contents])[0];
        if (ih_trace_ && !use_trace_) {   // diagnostics: nonce 0 of this batch, the layout of igm_igneumhash.inc
            const uint32_t *t = (const uint32_t *)[ih_trace_ contents];
            auto hex = [&](uint32_t off, uint32_t nw) { std::string h; char b[9]; for (uint32_t i = 0; i < nw; i++) { uint32_t v = t[off + i]; for (int q = 0; q < 4; q++) { snprintf(b, sizeof b, "%02x", (v >> (8 * q)) & 255); h += b; } } return h; };
            auto regs = [&](uint32_t off) { std::string o = "["; for (int l = 0; l < 8; l++) { o += l ? ",[" : "["; for (int q = 0; q < 16; q++) o += (q ? "," : "") + std::to_string(t[off + 16 * l + q]); o += "]"; } return o + "]"; };
            fprintf(stderr, "ih trace nonce %llu: {\"S\": \"%s\", \"E\": \"%s\", \"P\": \"%s\", \"init\": %s, \"fill\": %s, \"r0\": {\"idx\": %u, \"regs\": %s}, \"r1\": {\"idx\": %u, \"regs\": %s}, \"M\": \"%s\", \"F\": \"%s\", \"fold0\": [%u,%u,%u,%u], \"S_in\": \"%s\", \"S_len\": %u, \"blk0\": \"%s\", \"blk1\": \"%s\", \"m1\": \"%s\"}\n",
                    (unsigned long long)inflight_nonce[s], hex(0, 8).c_str(), hex(8, 8).c_str(), hex(16, 8).c_str(), regs(32).c_str(), regs(160).c_str(), t[288], regs(296).c_str(), t[424], regs(432).c_str(), hex(560, 8).c_str(), hex(568, 8).c_str(), t[576], t[577], t[578], t[579], hex(608, 26).c_str(), t[634], hex(640, 16).c_str(), hex(656, 16).c_str(), hex(672, 16).c_str());
        }
        if (n > max_results_) { redo.push_back({inflight_nonce[s], inflight_hashes[s]}); return; }
        const IhResult *rr = (const IhResult *)[results_[s] contents];
        if (n && getenv("IGM_IH_RESULT_DUMP")) { const uint32_t *w = (const uint32_t *)rr; fprintf(stderr, "result-dump: n %u words", n); for (int q = 0; q < 18; q++) fprintf(stderr, " %08x", w[q]); fprintf(stderr, "\n"); }   // the raw struct as the kernel left it
        for (uint32_t i = 0; i < n; i++) {
            Result out; out.nonce = ((uint64_t)rr[i].nonce_hi << 32) | rr[i].nonce_lo;
            for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, rr[i].mix[w]); put_le32(out.digest + 4 * w, rr[i].digest[w]); }
            if (keyed_document() || (cand_ && cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14)) {   // the digest from the mix on the host: a keyed document, and every version 12 to 14 document (14 Oct 2026: the M6 left the k8l light classes' digest first word zero where the mix was exact, the same compiler fault as the keyed library's) (the Metal compiler's keyed library leaves its first word zero on the M5 and M6): the kernel's compare ties on that word
                ih_final_digest(ih_.chain_id, ih_.version, ih_P_, job.header, out.nonce, out.mix, out.digest);   // against a target whose first word is zero (any difficulty of 2^32 or more) and decides on the true words 1 to 7, so the
                if (std::memcmp(out.digest, job.target, 32) > 0) continue;   // candidates it passes are the true shares plus the false ones whose word 0 is not zero; the host finalises each (two BLAKE3 compressions) and keeps the ones under the target
            }
            on_result(out);
        }
        on_progress(stt);
    } };
    auto drain_redo = [&]() {
        for (uint32_t q = 0; q < NF; q++) collect((int)q);
        while (!redo.empty()) { auto [n0, h0] = redo.back(); redo.pop_back(); if (h0 <= 1) continue; uint32_t a = h0 / 2, b = h0 - a; submit(0, n0, a); collect(0); submit(0, n0 + a, b); collect(0); }
    };
    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (release_requested_) { drain_redo(); release_dataset_now(); }   // Ember's memory-pressure release, paused or not
        if (paused()) { drain_redo(); std::this_thread::sleep_for(std::chrono::milliseconds(100)); continue; }
        if (!dataset_ && !ensure_dataset(job.height)) { std::this_thread::sleep_for(std::chrono::milliseconds(500)); continue; }   // after a release: the cold rebuild before the first batch
        if (!redo.empty()) drain_redo();
        uint32_t want = knobs_.batch_nonces ? knobs_.batch_nonces : hashes_for_duration();   // a ladder rung fixes the nonces per batch
        if (want > max_batch) want = max_batch;
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;
        collect(slot);
        submit(slot, job.nonce_start + done, hashes);
        if (!prepare_ready()) ih_warm_step(ih_slice_);   // the next epoch's slice behind this batch on the same queue
        done += hashes; slot = (slot + 1) % (int)NF;
        // the duty as pacing (the memory seat, or-policy-1, 11 Oct 2026): a sleep after the submit idled nothing while another batch
        // was in flight (duty 0.75 read the full rate, 0.5 read three quarters), so the next submit is held until one batch's GPU
        // time over the duty has passed since this one; the duty is then the GPU's busy fraction whatever the ring's depth
        double duty = duty_for_thermal();
        if (duty < 1.0 && last_gpu_ms_ > 0) { double period_ms = last_gpu_ms_ / duty; double until = wall_start[slot == 0 ? (int)NF - 1 : slot - 1] + period_ms; double now = now_ms(); if (until > now) std::this_thread::sleep_for(std::chrono::microseconds((long long)((until - now) * 1000.0))); }
    }
    drain_redo();
}

void MetalEngineX::run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                           const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    if (!cand_ || !search_pso_) return;
    if (cand_->kind == Kind::igneumhash && (ih_.version == 15 || ih_.version == 17) && (!r0f_key_ || (ih_.period_blocks ? job.height / ih_.period_blocks : 0) != r0f_period_)) { if (!r0f_fill_period(job.height)) { fprintf(stderr, "metal: the version 15 period key could not be set\n"); return; } }   // the key follows the job's period, whatever the open height (the vectors cross epochs)
    else if (cand_->kind == Kind::igneumhash && ih_.version >= 9 && ih_.period_blocks && job.height / ih_.period_blocks != r0d_period_) {   // versions 9 to 14: a new period is a new programme (and a new shared table)   // R0D: a new period is a new programme, so a new kernel
        Refusal rr; r0d_height_ = job.height; r0d_from_job_ = true;
        search_pso_ = nil; dag_pso_ = nil;
        if (!build_pipelines(rr)) { fprintf(stderr, "metal: R0D period programme rebuild refused (%s): %s\n", rr.code.c_str(), rr.explain.c_str()); return; }
    }
    if (cand_->kind == Kind::igneumhash || cand_->kind == Kind::rxe1) { ih_run_job(job, on_result, on_progress, should_stop); return; }
    Refusal r;
    if (cand_->kind == Kind::autolykos && job.height != height_) {
        // a new height is a new table: the warm one if it is the right height (finished
        // synchronously if the job came early), else a cold rebuild (its time is the downtime)
        if (next_dataset_ && next_height_ == job.height) { warm_finish(); dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nil; next_dataset_bytes_ = 0; next_height_ = ~0ull; next_cursor_ = 0; next_n_ = 0; }
        else {
            // a cold table for this height; the warm build of the next height runs beside it from
            // the first batch, so a job arriving before the cold build ends does not start from zero
            if (next_height_ != job.height + 1) { next_dataset_ = nil; next_height_ = ~0ull; }
            dataset_ = nil; dataset_bytes_ = 0; double ms = 0;
            warm_start(job.height + 1);
            if (!build_autolykos_table(job.height, dataset_, dataset_bytes_, ms, r, &should_stop)) { fprintf(stderr, "metal: table for height %llu: %s (next height's table %u of %u elements ready)\n", (unsigned long long)job.height, r.explain.c_str(), next_cursor_, next_n_); return; }
        }
        height_ = job.height;
    }
    if (cand_->kind == Kind::autolykos) warm_start(job.height + 1);
    const bool fh = cand_->kind == Kind::fishhash;
    uint64_t done = 0; int slot = 0;
    id<MTLCommandBuffer> inflight[2] = {nil, nil};
    uint32_t inflight_hashes[2] = {0, 0}; uint64_t inflight_nonce[2] = {0, 0}; double wall_start[2] = {0, 0};
    std::vector<std::pair<uint64_t, uint32_t>> redo;

    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) { @autoreleasepool {   // one rule for the worker thread: every batch's command buffer and encoder released at the batch's end
        if (fh) {
            FhJob *gj = (FhJob *)[job_buf_[s] contents];
            for (int i = 0; i < 8; i++) { gj->header[i] = le32(job.header + 4 * i); gj->target[i] = be32(job.target + 4 * i); }
            gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->items1024 = (uint32_t)(dataset_bytes_ / 128); gj->hashes = hashes; gj->max_results = max_results_;
        } else {
            AlJob *gj = (AlJob *)[job_buf_[s] contents];
            std::memcpy(gj->header, job.header, 32); std::memcpy(gj->target, job.target, 32);
            gj->nonce_lo = (uint32_t)nonce; gj->nonce_hi = (uint32_t)(nonce >> 32); gj->N = (uint32_t)(dataset_bytes_ / 32); gj->hashes = hashes; gj->max_results = max_results_;
        }
        ((uint32_t *)[count_[s] contents])[0] = 0;
        id<MTLCommandBuffer> cb = [queue_ commandBuffer];
        id<MTLComputeCommandEncoder> enc = [cb computeCommandEncoder];
        [enc setComputePipelineState:search_pso_];
        [enc setBuffer:job_buf_[s] offset:0 atIndex:0];
        [enc setBuffer:dataset_ offset:0 atIndex:1];
        [enc setBuffer:results_[s] offset:0 atIndex:2];
        [enc setBuffer:count_[s] offset:0 atIndex:3];
        if (knobs_.variant.rfind("diag-sgtg-", 0) == 0) [enc setThreadgroupMemoryLength:(NSUInteger)(knobs_.threadgroup * 32u * 16u) atIndex:0];
        if (cand_ && cand_->kind == Kind::igneumhash && ih_.forge()) [enc setThreadgroupMemoryLength:(NSUInteger)(ih_threads_per_tg() / 16u) * ih_.forge_local_bytes() atIndex:0];   // VERSION 20: the 16 KiB scratchpad an attempt, sixteen threads an attempt   // 512 bytes of staging per SIMD group (the threadgroup-memory control)
        uint32_t threads_per_tg = ih_threads_per_tg();
        uint64_t total_threads = (uint64_t)hashes * (fh ? fh_lanes_ : 1u);
        uint64_t tgs = (total_threads + threads_per_tg - 1) / threads_per_tg;
        [enc dispatchThreadgroups:MTLSizeMake(tgs, 1, 1) threadsPerThreadgroup:MTLSizeMake(threads_per_tg, 1, 1)];
        [enc endEncoding];
        wall_start[s] = now_ms(); [cb commit];
        inflight[s] = cb; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
    } };
    auto collect = [&](int s) { @autoreleasepool {
        id<MTLCommandBuffer> cb = inflight[s];
        if (!cb) return;
        [cb waitUntilCompleted];
        BatchStats stt; stt.hashes = inflight_hashes[s]; stt.gpu_ms = ([cb GPUEndTime] - [cb GPUStartTime]) * 1000.0; stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        inflight[s] = nil;
        if ([cb status] != MTLCommandBufferStatusCompleted) { fprintf(stderr, "metal: batch at nonce %llu failed: status %ld\n", (unsigned long long)inflight_nonce[s], (long)[cb status]); return; }
        uint32_t n = ((uint32_t *)[count_[s] contents])[0];
        if (n > max_results_) { fprintf(stderr, "metal: %u shares exceed the %u-entry buffer; re-running in halves\n", n, max_results_); redo.push_back({inflight_nonce[s], inflight_hashes[s]}); return; }
        for (uint32_t i = 0; i < n; i++) {
            Result out;
            if (fh) {
                const FhResult *rr = (const FhResult *)[results_[s] contents] + i;
                out.nonce = ((uint64_t)rr->nonce_hi << 32) | rr->nonce_lo;
                for (int w = 0; w < 8; w++) { put_le32(out.mix + 4 * w, rr->mix[w]); put_le32(out.digest + 4 * w, rr->digest[w]); }
            } else {
                const AlResult *rr = (const AlResult *)[results_[s] contents] + i;
                out.nonce = ((uint64_t)rr->nonce_hi << 32) | rr->nonce_lo;
                std::memcpy(out.mix, rr->mix, 32); std::memcpy(out.digest, rr->digest, 32);
            }
            on_result(out);
        }
        on_progress(stt);
    } };
    auto drain_redo = [&]() {
        collect(0); collect(1);
        while (!redo.empty()) {
            auto [n0, h0] = redo.back(); redo.pop_back();
            if (h0 <= 1) continue;
            uint32_t a = h0 / 2, b = h0 - a;
            submit(0, n0, a); collect(0); submit(0, n0 + a, b); collect(0);
        }
    };
    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (paused()) { drain_redo(); std::this_thread::sleep_for(std::chrono::milliseconds(100)); continue; }
        if (!redo.empty()) drain_redo();
        uint32_t want = hashes_for_duration();
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;
        collect(slot);
        submit(slot, job.nonce_start + done, hashes);
        if (cand_->kind == Kind::autolykos) warm_step(1u << 18);   // one table batch beside each search batch
        done += hashes; slot ^= 1;
        // the duty as pacing (the memory seat, or-policy-1, 11 Oct 2026): a sleep after the submit idled nothing while another batch
        // was in flight (duty 0.75 read the full rate, 0.5 read three quarters), so the next submit is held until one batch's GPU
        // time over the duty has passed since this one; the duty is then the GPU's busy fraction whatever the ring's depth
        double duty = duty_for_thermal();
        if (duty < 1.0 && last_gpu_ms_ > 0) { double period_ms = last_gpu_ms_ / duty; double until = wall_start[slot == 0 ? 1 : 0] + period_ms; double now = now_ms(); if (until > now) std::this_thread::sleep_for(std::chrono::microseconds((long long)((until - now) * 1000.0))); }   // this ring holds two
    }
    drain_redo();
}

}  // namespace igm
