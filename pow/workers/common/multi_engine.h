// multi_engine.h: one EngineIface over several backend engines, chosen by the candidate
// named at open (the lane-cooperative family and the independent-lane candidates have
// different engines); everything else forwards to the active one.
#pragma once
#include <map>
#include <memory>
#include <string>
#include "candidates.h"
#include "engine_iface.h"
#include "ih_params.h"

namespace igm {

class MultiEngine : public EngineIface {
public:
    // raster: the version 18 engine (the raster form), chosen when igneumhash's document parses to version 18 (15 October 2026); absent on the workers without one
    MultiEngine(EngineIface *progpow, EngineIface *independent, const std::string &backend, EngineIface *raster = nullptr) : p_(progpow), x_(independent), r_(raster), active_(progpow), backend_(backend) {}
    std::string backend() const override { return backend_; }
    Json device_json() const override { return active_->device_json(); }
    Json knob_decls() const override { return active_->knob_decls(); }
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) override {
        const Candidate *c = find_candidate(algo.c_str());
        if (!c) { r.code = "unsupported"; r.explain = "unknown candidate " + algo; return false; }
        EngineIface *want = c->lane_model == LaneModel::cooperative ? p_ : x_;
        if (r_ && algo == "igneumhash") { IhParams ip; std::string perr; if (ih_parse_params(k.params, ip, perr) && (ip.version == 18 || ip.version == 19)) want = r_; }   // 19: the raster form that draws continuously (raster19.h)
        if (want != active_) { active_->close(); active_ = want; }
        return active_->open(algo, height, k, supervisor_budget, r);
    }
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override { return active_->prepare(height, r, params); }
    void close() override { active_->close(); }
    void set_knobs(const Knobs &k) override { active_->set_knobs(k); }
    Knobs knobs() const override { return active_->knobs(); }
    uint64_t allocated_bytes() const override { return active_->allocated_bytes(); }
    uint64_t dataset_bytes() const override { return active_->dataset_bytes(); }
    Json budget_json() const override { return active_->budget_json(); }
    Json code_json() const override { return active_->code_json(); }
    bool prepare_ready() const override { return active_->prepare_ready(); }
    bool dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t c[32], uint8_t d[32]) override { return active_->dataset_digests(E, D, c, d); }
    bool program_binary(std::vector<unsigned char> &out) const override { return active_->program_binary(out); }
    bool dataset_word(uint64_t word_index, uint32_t *value, bool write) override { return active_->dataset_word(word_index, value, write); }
    bool trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) override { return active_->trace_one(height, header, nonce, words, r); }   // the identity set and the flip trace reach the engine through the wrapper
    double last_swap_wait_ms() const override { return active_->last_swap_wait_ms(); }
    bool guard_status(Json &out) override { return active_->guard_status(out); }   // the guard regions and the memory counters reach the engine through the wrapper (or-0-guard-1's first take read "no guard path" here)
    void memory_stats(Json &out) const override { active_->memory_stats(out); }
    void telemetry(Json &t) const override { active_->telemetry(t); }
    bool paused() const override { return active_->paused(); }
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) override {
        active_->run_job(job, on_result, on_progress, should_stop);
    }
    std::string describe() const override { return active_->describe(); }
private:
    EngineIface *p_, *x_, *r_, *active_;
    std::string backend_;
};

}  // namespace igm
