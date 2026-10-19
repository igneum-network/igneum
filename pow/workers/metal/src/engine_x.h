// engine_x.h: the Metal backend for the independent-lane candidates, FishHash (one
// fixed Ethash-lineage dataset from A's FishHash cache, 1024-bit items) and Autolykos v2
// (the element table of a height, rebuilt per height on the device). One thread per
// hash; the shared bodies in pow/workers/common/kernels with the Metal prelude and
// wrapper. GPU engineer B, 9 October 2026. Source only on this Mac.
#pragma once
#import <Metal/Metal.h>
#include <cstdlib>
#include <string>
#include "candidates.h"
#include "device.h"
#include "engine_iface.h"
#include "ignpow.h"
#include "ih_params.h"
#include "rxe1_params.h"
#include <vector>
#include <atomic>
#include "igneumhash.h"

namespace igm {

class MetalEngineX : public EngineIface {
public:
    MetalEngineX(Device &dev, const std::string &shader_dir);
    ~MetalEngineX() override;
    std::string backend() const override { return "metal"; }
    Json device_json() const override;
    Json knob_decls() const override;
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) override;
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override;
    void close() override;
    void set_knobs(const Knobs &k) override { knobs_ = k; if (k.release_dataset) release_requested_ = true; knobs_.release_dataset = 0; }   // the release is acted on by the run loop at its next iteration (the loop's thread owns the buffers)
    Knobs knobs() const override { return knobs_; }
    uint64_t allocated_bytes() const override;
    uint64_t dataset_bytes() const override { return dataset_bytes_; }
    Json budget_json() const override;
    Json code_json() const override;
    void telemetry(Json &t) const override;
    bool keyed_document() const override;   // version 8 bit 10 on the current document
    bool paused() const override;
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                 const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) override;
    std::string describe() const override;
    int fp_check(const std::string &path, uint32_t features);   // version 8 FP: the fp class's recorded cases through the compiled fop on this GPU (the project's fpcheck command)
    void allow_battery(bool b) { battery_allowed_ = b; }
    void allow_low_power_mode(bool b) { lpm_allowed_ = b; }

private:
    bool build_fishhash_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, Refusal &r);
    bool build_autolykos_table(uint64_t height, id<MTLBuffer> __strong &table, uint64_t &bytes, double &ms, Refusal &r, const std::function<bool()> *stop = nullptr);
    void warm_start(uint64_t height);                // allot the next height's table if the budget allows
    void warm_step(uint32_t elements);               // one bounded batch of the warm build, no wait
    void warm_finish();                              // wait for the outstanding batch; finish the rest synchronously
    bool build_pipelines(Refusal &r);
    void fingerprint_pipelines(id<MTLFunction> fn, id<MTLFunction> df, const std::string &src, MTLCompileOptions *opts);   // Orchard workstream 1: the execution fingerprint of the compiled pipelines
    Json fp_ = Json::object();   // the fingerprint block, rebuilt with the pipelines, printed on every row (code_json)
    bool ih_build_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, uint8_t E_out[32], id<MTLBuffer> __strong &light_out, Refusal &r, bool async = false);
    bool prepare_ready() const override;
    bool dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t c[32], uint8_t d[32]) override;
    double last_swap_wait_ms() const override { return last_swap_wait_ms_; }
    // Orchard OR-0 and OR-6 (the Apple memory seat, 10 Oct 2026): the host side of the dataset allocation. Every dataset buffer is
    // allocated as [front guard | payload | back guard] (knobs_.guard_mb each side, 0 = none) and bound to the kernels at the
    // payload offset, so no kernel knows the guard exists; the guards hold an address-derived canary the harness reads back.
    bool dataset_word(uint64_t word_index, uint32_t *value, bool write) override;
    bool trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) override;
    bool guard_status(Json &out) override;
    void memory_stats(Json &out) const override;
    uint32_t ih_ctx() const { return knobs_.variant == "ctx2" ? 2u : knobs_.variant == "ctx4" ? 4u : 1u; }
    // versions 12 to 14: the octets a threadgroup holds, each with its own copy of the document's local table in threadgroup memory
    // (14 Oct 2026, the Metal round fault: the wrapper sized the table array for two octets, the dispatch sent sixteen, eight octets
    // overwrote one table); the knob's threadgroup, cut to what 32 KB of threadgroup memory holds
    uint32_t ih_tg_octets() const { uint32_t o = (knobs_.threadgroup * (simd_width_ ? simd_width_ : 32u)) / 8u, m = 32768u / (ih_.table_words() * 4u); if (o < 1u) o = 1u; if (m < 1u) m = 1u; return o < m ? o : m; }
    uint32_t ih_threads_per_tg() const { return (cand_ && cand_->kind == Kind::igneumhash && ih_.forge()) ? 16u : (cand_ && cand_->kind == Kind::igneumhash && ih_.version >= 12 && ih_.version <= 14) ? ih_tg_octets() * 8u : knobs_.threadgroup * simd_width_; }   // version 20: one attempt a threadgroup (the 16 KiB scratchpad and the exchange under the 32 KiB)   // T-B10a nonce contexts per lane group
    std::string rx_probe_define() const { const std::string &v = knobs_.variant; int n = v == "probe-alu" ? 1 : v == "probe-shuffle" ? 2 : v == "probe-chain" ? 3 : v == "probe-decode" ? 4 : v == "probe-fp16mat" ? 5 : v == "probe-int8dot" ? 6 : v == "probe-mulrot" ? 7 : v == "probe-butterfly" ? 8 : v == "probe-byteperm" ? 9 : 0; return n ? "#define IGM_RX_PROBE " + std::to_string(n) + "\n" : std::string(); }   // the Mosaic lane microbenchmarks (diagnostics)
    void ih_warm_step(uint32_t items);   // one slice of the next epoch's dataset on the mining queue
    void ih_warm_finish();
    uint32_t ih_next_cursor_ = 0, ih_next_items_ = 0, ih_slice_ = 1u << 16;
    void ih_run_job(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop);
    uint32_t hashes_for_duration() const;
    double duty_for_thermal() const;

    Device &dev_;
    std::string shader_dir_, algo_, params_;
    const char *params_c() const { return params_.empty() ? nullptr : params_.c_str(); }
    const Candidate *cand_ = nullptr;
    Knobs knobs_;
    bool battery_allowed_ = false, lpm_allowed_ = false;
    ignpow_ctx *ctx_ = nullptr;
    uint64_t height_ = 0, next_height_ = ~0ull;
    id<MTLCommandQueue> queue_ = nil, build_queue_ = nil;   // build_queue_: the next epoch beside the mining
    id<MTLCommandBuffer> build_last_ = nil; double last_swap_wait_ms_ = 0.0;
    id<MTLComputePipelineState> search_pso_ = nil, table_pso_ = nil, dag_pso_ = nil;
    id<MTLBuffer> dataset_ = nil, next_dataset_ = nil, light_ = nil;
public:
    void wipe_secret() override;   // the seal verb: the job buffers (the constants with the secret) zeroed
private:
    id<MTLBuffer> job_buf_[8] = {nil}, results_[8] = {nil}, count_[8] = {nil};   // the batch ring: knobs_.inflight slots (T-B9)
    uint64_t dataset_bytes_ = 0, next_dataset_bytes_ = 0;
    uint32_t next_cursor_ = 0, next_n_ = 0;          // the warm table build in progress: elements done, elements total
    id<MTLCommandBuffer> next_cb_ = nil;             // the last warm table batch committed
    uint32_t simd_width_ = 0, max_results_ = 1024;
    uint32_t fh_lanes_ = 8;   // FishHash lanes per nonce: 8 (the lane shape) or 1 (the fallback)
    uint32_t ih_eff_scratch() const { return ih_.version == 10 ? 16384u : ih_.version == 11 ? 4096u : ih_.scratch_bytes; }   // version 10: the nonce's 16 KB gather table lives in the scratch buffer (the document's scratch is 0)
    uint32_t ih_built_version_ = ~0u; uint64_t r0d_height_ = 0, r0d_period_ = ~0ull; bool r0d_from_job_ = false;   // version 9 (R0D): the height and period the programme in the built source belongs to   // the document version the search pipeline was compiled for (IGM_IH_VERSION)
    IhParams ih_;             // IgneumHash: the parsed params document
    Rxe1Params rx_; uint8_t rx_pid_[32] = {0}; id<MTLBuffer> rx_sched_ = nil, rx_rsched_ = nil; uint64_t rx_epoch_ = ~0ull; bool rxe1_ = false;   // RXE1 (IH 06)
    uint8_t ih_P_[32] = {0}, ih_E_[32] = {0}, ih_next_E_[32] = {0};
    ignpow_ctx *ih_next_ctx_ = nullptr; id<MTLBuffer> ih_next_light_ = nil;   // the warm next epoch (prepare)
    IhParams ih_next_ih_; uint8_t ih_next_P_[32] = {0}; std::string ih_next_params_;   // the document the next dataset is for (prepare.params); empty = the resident one
    id<MTLBuffer> ih_scratch_ = nil, ih_st_ = nil, ih_trace_ = nil, dataset2_ = nil;   // dataset2_: T-B8 split2, the upper half
    id<MTLComputePipelineState> r12_fill_pso_ = nil; id<MTLBuffer> r12_shared_ = nil, r12_seed_ = nil, r12_batch_ = nil; uint64_t r12_period_ = ~0ull;   // version 12's shared table (versions 12 to 14)
    void r12_ensure_shared(); bool r12_fill_shared(uint64_t height);
    id<MTLBuffer> r0e_optrace_buf_ = nil; size_t r0e_optrace_words_ = 0; bool r0e_optrace_on_ = getenv("IGM_R0E_OPTRACE") != nullptr; void r0e_optrace_print(const Job &job);   // the per-operation trace of the R0E programme (14 Oct 2026)
    id<MTLBuffer> r0f_key_ = nil, r0f_prog_ = nil; bool r0f_fill_period(uint64_t height); uint64_t r0f_period_ = ~0ull; uint8_t r16f_key_[32] = {};   // version 15: the period key words and the packed programme
    id<MTLTexture> r17_tex_ = nil;   // version 17: the static texture (64 MiB R32Uint)
    uint32_t ih_slots_ = 0;   // nonces per batch the scratch buffer holds
    // the allocation forms (OR-0, OR-6): the guard, the storage mode, the pre-touch, the pool
    uint64_t guard_bytes_ = 0;                 // the guard each side of every dataset buffer, from knobs_.guard_mb at allocation
    uint64_t pay_off_ = 0;                     // the payload's offset in every dataset buffer: guard_bytes_ + knobs_.shift_bytes (the shifted bytes are front guard too)
    id<MTLBuffer> next_dataset2_ = nil;        // split2: the upper half of the next epoch's dataset (the warm form of the split allocation)
    id<MTLBuffer> spare_ = nil, spare2_ = nil; // persist "pool": the retired dataset buffers kept for the next prepare
    id<MTLBuffer> staging_ = nil;              // private storage: the host-side window for read-back and canary writes (64 MiB, shared)
    uint64_t pool_reuses_ = 0, pool_allocs_ = 0, purged_ = 0; double released_at_ms_ = 0;   // the record: how many prepares took a pooled buffer, how many allocated, how many retired buffers were marked purgeable-empty
    bool persist_pool() const;
    std::atomic<bool> release_requested_{false};   // Ember's memory-pressure release (release_dataset): honoured by the run loop, paused or not
    void release_dataset_now();                    // drop the resident dataset, the pool and the next epoch's buffers (purgeable-empty), keep the cache and the context
    bool ensure_dataset(uint64_t height);          // no dataset resident (released): rebuild through the cold path before the next submit   // the stock form on Metal is the pool (or-0-leak-1); "reconstruct" is the explicit, purging form
    bool msl_osize_ = false;   // the search library compiled at the size optimisation level (a keyed document's default; the M5 Max compiler fault)
    std::string last_src_; NSString *search_fn_name_ = nil;   // the search kernel's source and entry as compiled (trace_one recompiles them with the full trace)
    id<MTLComputePipelineState> trace_pso_ = nil; id<MTLBuffer> trace_full_ = nil; size_t trace_words_ = 0; bool use_trace_ = false;
    MTLResourceOptions dataset_options() const;
    id<MTLBuffer> alloc_dataset(id<MTLBuffer> existing, uint64_t payload, const char *name, Refusal &r);   // existing (re-armed) when it holds payload plus the guards; else the pool's; else new
    void arm_guards(id<MTLBuffer> buf, uint64_t payload);
    bool pretouch(id<MTLBuffer> buf, uint64_t payload);
    bool raw_read(id<MTLBuffer> buf, uint64_t off, uint64_t len, void *dst);      // raw buffer offsets, storage-agnostic
    bool raw_write(id<MTLBuffer> buf, uint64_t off, uint64_t len, const void *src);
    bool read_payload(id<MTLBuffer> buf, uint64_t off, uint64_t len, void *dst) { return raw_read(buf, pay_off_ + off, len, dst); }
    void gpu_idle();   // everything committed on the queues completed
    bool guard_check(id<MTLBuffer> buf, uint64_t payload, const char *name, Json &out);
    void retire_dataset(id<MTLBuffer> __strong &buf);   // the swap's old buffer: to the pool under persist "pool", else released
    double last_gpu_ms_ = 0, last_table_ms_ = 0;
    uint32_t last_hashes_ = 0;
};

}  // namespace igm
