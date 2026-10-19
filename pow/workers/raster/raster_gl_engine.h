// raster_gl_engine.h: the GL raster worker under the protocol (the `protocol` verb) for VERSION 19: an EngineIface over the GL card's full
// path (raster_gl_v19.h, raster_gl_v19_card.h), the twin of the Metal raster engine's version 19 (raster_metal_v19.h). The registered
// document (its anchor zero) draws P as the sealer does, the coordinate's anchor keys the period alone; the job's header, target and nonce
// range go through the card in batches, every hit a result with its M and F. The context is opened once by the worker's main (the card
// named by --device-name) and the engine binds the version 19 card onto it at configure. the GPU team, 18 October 2026.
#pragma once
#include "raster_gl_v19_card.h"
#include "engine_iface.h"
namespace igm {
class RasterGlEngine : public EngineIface {
public:
    explicit RasterGlEngine(RasterGl &gl) : gl_(gl) {}
    std::string backend() const override { return "opengl"; }
    Json device_json() const override {
        return Json::object().set("index", 0).set("backend", "opengl").set("name", gl_.device).set("vendor", "gl").set("id", gl_.device).set("memory_total", 0).set("memory_free", 0).set("compute", "raster").set("driver", "egl").set("fingerprint", "gl-raster").set("identity", "gl-raster:" + gl_.device).set("intrinsic_id", gl_.device);
    }
    Json knob_decls() const override { return Json::array(); }
    // THE THREAD RULE: a GL context is current on one thread at a time; the protocol loop configures on its main thread and runs a job on a job
    // thread, so the engine takes the context on whichever thread calls it (open, prepare, run_job) and releases it when the job ends
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t, Refusal &r) override {
        close(); gl_.make_current(); struct Release { RasterGl &g; ~Release() { glFinish(); g.release_current(); } } release{gl_};   // the main thread holds the context only inside this call
        if (algo != "igneumhash") { r.code = "algo"; r.explain = "the GL raster engine runs igneumhash version 19 only"; return false; }
        std::string err; if (!ih_parse_params(k.params, p_, err)) { r.code = "params"; r.explain = "params: " + err; return false; }
        if (p_.version != 19) { r.code = "params"; r.explain = "version " + std::to_string(p_.version) + " is not the raster form this engine serves (19)"; return false; }
        if (gl_.side != r19_side(p_)) { r.code = "device"; r.explain = "the context's target is " + std::to_string(gl_.side) + ", the document's base " + std::to_string(r19_side(p_)); return false; }
        std::string doc0 = k.params; { Json j; if (Json::parse(k.params, j) && j.is_object()) { j.set("program_anchor", std::string(64, '0')); doc0 = j.dump(); } }
        const size_t B = (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 64); batch_ = B;
        card_ = R19Card(); if (!r19_gl_card(gl_, p_, doc0, card_, B, st_)) { r.code = "device"; r.explain = "the version 19 card could not be built"; return false; }
        if (!r19_gl_full(gl_, p_, st_.get(), card_, B)) { r.code = "device"; r.explain = "the version 19 passes could not be built"; return false; }
        knobs_ = k; { IhParams p0 = p_; p0.program_anchor = std::string(64, '0'); ih_P(p0, P_); } apply_anchor();
        if (!r19_prepare(p_, card_, height, err)) { r.code = "dataset"; r.explain = err; return false; }
        open_ = true; return true;
    }
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override {
        if (!open_) { r.code = "state"; r.explain = "not configured"; return false; } gl_.make_current(); struct Release { RasterGl &g; ~Release() { glFinish(); g.release_current(); } } release{gl_};
        if (!params.empty() && params != knobs_.params) { r.code = "params"; r.explain = "a parameter change ahead is not warmed by the raster engine; reconfigure at the height"; return false; }
        std::string err; if (!r19_prepare(p_, card_, height, err)) { r.code = "dataset"; r.explain = err; return false; } return true;
    }
    void close() override { open_ = false; }
    void apply_anchor() { std::string a = knobs_.r0d_anchor; if (a.rfind("0x", 0) == 0) a = a.substr(2); if (a.size() == 64) p_.program_anchor = a; }
    void set_knobs(const Knobs &k) override { knobs_ = k; apply_anchor(); }
    Knobs knobs() const override { return knobs_; }
    uint64_t allocated_bytes() const override { return open_ ? (uint64_t)gl_.side * gl_.side * 4 + p_.dataset_bytes : 0; }
    uint64_t dataset_bytes() const override { return p_.dataset_bytes; }
    Json budget_json() const override { return Json::object().set("total_bytes", 0).set("free_bytes", 0).set("fit_cold_bytes", 0).set("fit_warm_bytes", 0).set("can_warm", false); }
    void telemetry(Json &) const override {}
    bool paused() const override { return false; }
    bool keyed_document() const override { return false; }
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) override {
        if (!open_) return;
        if (!gl_.make_current()) { fprintf(stderr, "raster: the context could not be made current on the job thread\n"); return; }
        struct Release { RasterGl &g; ~Release() { glFinish(); g.release_current(); } } release{gl_};
        std::string err; if (!r19_prepare(p_, card_, job.height, err)) { fprintf(stderr, "raster: %s\n", err.c_str()); return; }
        uint8_t K[32]; if (!r18_key(p_, job.height, K, err)) { fprintf(stderr, "raster: key: %s\n", err.c_str()); return; }
        const uint64_t end = job.nonce_start + job.nonce_count; uint64_t done = 0; auto t0 = std::chrono::steady_clock::now(), tp = t0; const uint64_t B = batch_;
        for (uint64_t nonce = job.nonce_start; nonce < end && !should_stop(); nonce += B) {
            const uint64_t n = (end - nonce) < B ? (end - nonce) : B; std::vector<uint64_t> nonces(n); for (uint64_t b = 0; b < n; b++) nonces[b] = nonce + b;
            std::vector<std::array<uint8_t, 32>> Ms, Fs; uint32_t hits = 0;
            if (!card_.full(K, P_, job.header, nonces, job.target, Ms, Fs, hits)) { fprintf(stderr, "raster: the card refused a batch\n"); return; }
            if (hits) for (uint64_t b = 0; b < n; b++) if (std::memcmp(Fs[b].data(), job.target, 32) <= 0) { Result rr; rr.nonce = nonce + b; std::memcpy(rr.mix, Ms[b].data(), 32); std::memcpy(rr.digest, Fs[b].data(), 32); on_result(rr); if (job.seal) return; }
            done += n;
            const auto now = std::chrono::steady_clock::now();
            if (std::chrono::duration<double>(now - tp).count() >= 1.0) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(now - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); tp = now; done = 0; t0 = now; }
        }
        if (done) { BatchStats s; s.hashes = done; s.wall_ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count(); s.gpu_ms = s.wall_ms; on_progress(s); }
    }
    std::string describe() const override { return "gl raster, version 19: the seed, the chain, one instanced draw a batch into the atlas, the fold, the mix and the final on the card"; }
private:
    RasterGl &gl_; R19Card card_; std::shared_ptr<R19GlState> st_; IhParams p_; Knobs knobs_; uint8_t P_[32] = {0}; bool open_ = false; size_t batch_ = 64;
};
}  // namespace igm
