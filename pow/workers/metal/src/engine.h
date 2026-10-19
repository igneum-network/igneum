// engine.h: the Metal backend of the one worker core (EngineIface) for the
// ProgPoW/KAWPOW family: pipelines per period, the device-built dataset, bounded
// batches, cancellation, the unified-memory budget and the system policies
// (DESIGN.md 3.4 to 3.6). GPU engineer B, 9 October 2026. Source only; not built.
#pragma once
#import <Metal/Metal.h>
#include <string>
#include "candidates.h"
#include "device.h"
#include "engine_iface.h"
#include "ignpow.h"

namespace igm {

class MetalEngine : public EngineIface {
public:
    MetalEngine(Device &dev, const std::string &shader_dir);
    ~MetalEngine() override;

    std::string backend() const override { return "metal"; }
    Json device_json() const override;
    Json knob_decls() const override;
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) override;
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override;
    void close() override;
    void set_knobs(const Knobs &k) override;
    Knobs knobs() const override { return knobs_; }
    uint64_t allocated_bytes() const override;
    uint64_t dataset_bytes() const override { return dataset_bytes_; }
    Json budget_json() const override;
    Json code_json() const override;
    void telemetry(Json &t) const override;
    bool paused() const override;
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                 const std::function<void(const BatchStats &)> &on_progress,
                 const std::function<bool()> &should_stop) override;
    std::string describe() const override;

    void allow_battery(bool b) { battery_allowed_ = b; }
    void allow_low_power_mode(bool b) { lpm_allowed_ = b; }

private:
    bool build_dataset(uint64_t height, ignpow_ctx *&ctx, id<MTLBuffer> __strong &dataset, uint64_t &bytes, Refusal &r);
    bool build_pipeline(uint64_t prog_seed, std::string &explain);
    uint32_t hashes_for_duration() const;
    double duty_for_thermal() const;

    Device &dev_;
    std::string shader_dir_, base_source_, dag_source_;
    std::string algo_, params_;
    const char *params_c() const { return params_.empty() ? nullptr : params_.c_str(); }
    const Candidate *cand_ = nullptr;
    Knobs knobs_;
    bool battery_allowed_ = false, lpm_allowed_ = false;

    ignpow_ctx *ctx_ = nullptr;        // light context of the open epoch (A's reference)
    ignpow_ctx *next_ctx_ = nullptr;
    uint64_t height_ = 0;              // a height inside the open epoch
    uint64_t prog_seed_ = ~0ull;

    id<MTLCommandQueue> queue_ = nil;
    id<MTLComputePipelineState> search_pso_ = nil;
    id<MTLComputePipelineState> dag_pso_ = nil;
    id<MTLBinaryArchive> archive_ = nil;
    id<MTLBuffer> dataset_ = nil, next_dataset_ = nil, light_ = nil, fault_ = nil;
    id<MTLBuffer> job_buf_[2] = {nil, nil}, results_[2] = {nil, nil}, count_[2] = {nil, nil};
    uint64_t dataset_bytes_ = 0, next_dataset_bytes_ = 0;
    uint32_t simd_width_ = 0;
    bool lane_shuffle_ = false;
    uint32_t max_results_ = 1024;   // per batch; an overflow re-runs the batch in halves, never drops a share
    double last_gpu_ms_ = 0;
    uint32_t last_hashes_ = 0;
};

}  // namespace igm
