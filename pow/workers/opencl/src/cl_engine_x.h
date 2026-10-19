// cl_engine_x.h: the OpenCL backend for the independent-lane candidates (FishHash and
// Autolykos v2), the shared bodies with the OpenCL prelude and wrappers. One work-item
// per hash. GPU engineer B, 9 October 2026.
#pragma once
#include "cl_shim.h"
#include <cstdlib>
#include <string>
#include <vector>
#include "candidates.h"
#include "cl_engine.h"
#include "engine_iface.h"
#include "ignpow.h"
#include "ih_params.h"
#include "rxe1_params.h"
#include "igneumhash.h"

namespace igm {

class ClEngineX : public EngineIface {
public:
    void wipe_secret() override;   // the seal verb: the job buffers (the constants with the secret) zeroed on the device
    ClEngineX(const ClDeviceInfo &info, int index, const std::string &kernel_dir);
    ~ClEngineX() override;
    std::string backend() const override { return "opencl"; }
    Json device_json() const override;
    Json knob_decls() const override;
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) override;
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override;
    void close() override;
    void set_knobs(const Knobs &k) override { take_knobs(k); }
    // The AMD wave lane path (the Riptide performance seat): a variant "wave" or a "-wave" suffix on any igneumhash variant
    // asks the kernel for the wave's cross-lane instructions in place of the local-memory exchange (prelude_cl.inc,
    // IGM_IH_AMD_WAVE); the base variant keeps its meaning. Inert on a compiler that is not AMD's LC (pocl, NVIDIA).
    // "-wave": the intrinsics and the streamed finalisation (IGM_IH_STREAM_FINAL, scratch 0 only); "-wavex": the intrinsics alone
    bool ih_wave_ = false, ih_stream_ = false, ih_xfer_ = false, ih_v6hoist_ = false; uint32_t ih_occ_ = 0; bool ih_lean_ = false, ih_dpp_ = false, ih_sel_ = false, ih_pl_ = false, ih_span_ = false, ih_x2_ = false, ih_v8s_ = false; uint32_t bw_keep_ = 0;
    size_t ih_local_words() const { const size_t wg = knobs_.threadgroup * wave_; const size_t stage = (!rxe1_ && (knobs_.variant == "form2" || knobs_.clopts.find("IGM_IH_V4_FORM=2") != std::string::npos)) ? wg / 8 * ih_.item_words() : 0; const size_t bw = (bw_loader_ == 5) ? wg * 4 : 0; return wg + stage + bw; }   // the local words before the version 8 snapshot (the same sum the launch allocates)
    size_t ih_v8s_words() const { return 0; }   // the v8s snapshot is a register copy again (11 October 03:1x UK); no local memory for it uint32_t bw_keep_ = 0;
    void take_knobs(const Knobs &k) {
        knobs_ = k; ih_wave_ = false; ih_stream_ = false; variant_full_ = k.variant; ih_xfer_ = false; ih_v6hoist_ = false; ih_occ_ = 0;
        // the residency pin (the outside review's "compile at every VGPR residency boundary", 10 October 21:4x UK): a variant ending "-occ<N>"
        // puts amdgpu_waves_per_eu(N, N) on the kernel (AMD only; the source pocl compiles is unchanged), so the card's compiler fits the
        // kernel to N waves per SIMD or spills (scratch on the row: the rule rejects it); parsed first so it composes with every form
        // the outermost suffixes first: "-occ<N>" and "-x2" sit at the end of a name and must be stripped before the form suffixes
        // (the pinned rows of 10 October ran with "-occ" stripped last, so "-wave" never matched and they ran the stock kernel: void)
        { std::string &v = knobs_.variant; size_t o = v.rfind("-occ"); if (o != std::string::npos && o + 4 < v.size() && v.find_first_not_of("0123456789", o + 4) == std::string::npos) { ih_occ_ = (uint32_t)std::stoul(v.substr(o + 4)); v.erase(o); } }
        { std::string &v = knobs_.variant; const std::string v8 = "-v8s"; ih_v8s_ = false; if (v.size() > v8.size() && v.compare(v.size() - v8.size(), v8.size(), v8) == 0) { v.erase(v.size() - v8.size()); ih_v8s_ = true; } }   // the snapshot form of version 8's step 1b (K, B, F at build time; the per-lane snapshot in local memory)
        { std::string &v = knobs_.variant; const std::string x2 = "-x2"; ih_x2_ = false; if (v.size() > x2.size() && v.compare(v.size() - x2.size(), x2.size(), x2) == 0) { v.erase(v.size() - x2.size()); ih_x2_ = true; } }   // the two-chain form (the outside review's item 7): two nonces per eight-lane group
        { std::string &v = knobs_.variant; const std::string sp = "-span"; ih_span_ = false; if (v.size() > sp.size() && v.compare(v.size() - sp.size(), sp.size(), sp) == 0) { v.erase(v.size() - sp.size()); ih_span_ = true; } }   // the one-wave load shape on the eight-lane relation (AMD only, W512)
        { std::string &v = knobs_.variant; const std::string q = "-pl"; ih_pl_ = false; if (v.size() > q.size() && v.compare(v.size() - q.size(), q.size(), q) == 0) { v.erase(v.size() - q.size()); ih_pl_ = true; } }   // the group reads on v_permlane16_var (AMD gfx12 only)
        { std::string &v = knobs_.variant; const std::string e = "-sel"; ih_sel_ = false; if (v.size() > e.size() && v.compare(v.size() - e.size(), e.size(), e) == 0) { v.erase(v.size() - e.size()); ih_sel_ = true; } }   // the branchless selects (the absorb's routing and the tree's pairs)
        { std::string &v = knobs_.variant; const std::string d = "-dpp"; ih_dpp_ = false; if (v.size() > d.size() && v.compare(v.size() - d.size(), d.size(), d) == 0) { v.erase(v.size() - d.size()); ih_dpp_ = true; } }   // the fixed-pattern exchanges as DPP moves (AMD only)
        { std::string &v = knobs_.variant; const std::string l = "-lean"; ih_lean_ = false; if (v.size() > l.size() && v.compare(v.size() - l.size(), l.size(), l) == 0) { v.erase(v.size() - l.size()); ih_lean_ = true; } }   // the S stream recomputed at the finaliser (the route to the 16-wave tier)
        { std::string &v = knobs_.variant; size_t o = v.rfind("-pin"); bw_keep_ = 0; if (o != std::string::npos && o + 4 < v.size() && v.find_first_not_of("0123456789", o + 4) == std::string::npos) { bw_keep_ = (uint32_t)std::stoul(v.substr(o + 4)); v.erase(o); } }   // the one-wave twin at a chosen VGPR tier (the residency control)
        { std::string &v = knobs_.variant; const std::string h = "-hoist"; if (v.size() > h.size() && v.compare(v.size() - h.size(), h.size(), h) == 0) { v.erase(v.size() - h.size()); ih_v6hoist_ = true; } }   // version 6's hoisted key gather
        { std::string &v = knobs_.variant; const std::string x = "-xfer"; if (v != "undertow-xfer" && v.size() > x.size() && v.compare(v.size() - x.size(), x.size(), x) == 0) { v.erase(v.size() - x.size()); ih_xfer_ = true; } }   // the outside review's matched-resource diagnostic on the form; "undertow-xfer" is Undertow's own twin, named whole below (the strip ran first on 10 October and the twin's first rows were the candidate's)
        auto strip = [&](const std::string &suf) { std::string &v = knobs_.variant; if (v == suf.substr(1)) { v.clear(); return true; } if (v.size() > suf.size() && v.compare(v.size() - suf.size(), suf.size(), suf) == 0) { v.erase(v.size() - suf.size()); return true; } return false; };
        if (strip("-wavex")) { ih_wave_ = true; }
        else if (strip("-wave")) { ih_wave_ = true; ih_stream_ = true; }
        // Breakwater: "bw-b<k>" (phase A, transfer-only) or "bw-b<k>-r<n>" (phase B); the variant string stays whole
        bw_loader_ = 0; bw_red_ = 0; bw_oldfin_ = strip("-oldfin");   // decision D's bw-p-new-old: the one-wave loader with the old (byte-array) builder
        const std::string &v = knobs_.variant;
        if (v.rfind("bw-b", 0) == 0 && v.size() >= 5 && v[4] >= '1' && v[4] <= '7') { bw_loader_ = v[4] - '0'; if (v.size() >= 8 && v.compare(5, 2, "-r") == 0) bw_red_ = v[7] - '0'; ih_wave_ = true; }
        // Undertow (phase D): "undertow" = the one-wave loader 1 with version 4's reduction on the packed layout (item_bytes / 16
        // lanes per nonce: 32 at W512, 16 at W256, 8 at W128), "undertow-xfer" = the same geometry transfer-only (the twin)
        if (v == "undertow") { bw_loader_ = 1; bw_red_ = 2; ih_wave_ = true; }
        else if (v == "undertow-xfer") { bw_loader_ = 1; bw_red_ = 0; ih_wave_ = true; }
    }
    uint32_t bw_loader_ = 0, bw_red_ = 0; bool bw_oldfin_ = false;
    cl_mem dataset_base_ = nullptr;   // the larger buffer behind a shifted dataset sub-buffer
    std::string src_sha256_, bin_sha256_, bin_path_, variant_full_; uint64_t bin_bytes_ = 0;   // the ten-field record: the compiled source and the card compiler's binary by hash
    bool r0f_static() const { return !cl_intel_defines(info_).empty() || knobs_.clopts.find("IGM_R0F_STATIC=1") != std::string::npos; }   // version 15: the generated programme on an Intel platform, or by the knob
    uint32_t ih_lanes() const { return ih_.forge() ? 16u : (knobs_.variant == "n1" || ih_.version >= 16) ? 1u : bw_loader_ ? (bw_loader_ == 4 ? 64u : ih_.item_words() / 4u) : 8u; }   // the work-items per group; the x2 form keeps eight (the dataset build's geometry reads this too: at four it built 2 GiB in 27 minutes, 11 October 00:4x UK)
    uint32_t ih_nonces_per_group() const { return ih_x2_ ? 2u : ih_ctx(); }   // nonces per eight-lane group: the x2 form's two, T-B10a's ctx, else one   // the one-wave family: 16 bytes per lane
    Knobs knobs() const override { return knobs_; }
    uint64_t allocated_bytes() const override { return allocated_; }
    uint64_t dataset_bytes() const override { return dataset_bytes_; }
    bool dataset_word(uint64_t word_index, uint32_t *value, bool write) override;
    Json budget_json() const override;
    Json code_json() const override;
    void telemetry(Json &t) const override;
    bool keyed_document() const override;   // version 8 bit 10 on the current document
    bool paused() const override { return false; }
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                 const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) override;
    std::string describe() const override;

private:
    bool build_programs(Refusal &r);
    bool split_form(uint64_t bytes) const;   // the dataset in two buffers: one predicate for the compile and the allocation
    unsigned split_pieces(uint64_t bytes) const;   // 1, 2 or 4 pieces (quarters when a half exceeds the largest allocation: version 15 on rusticl)
    cl_mem dataset3_ = nullptr, dataset4_ = nullptr;   // the third and fourth quarters under the four-piece form
    bool ih_build_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, uint8_t E_out[32], cl_mem &light_out, Refusal &r, bool async = false);
    bool prepare_ready() const override;
    bool dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t c[32], uint8_t d[32]) override;
    bool program_binary(std::vector<unsigned char> &out) const override;
    bool trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) override;
    double last_swap_wait_ms() const override { return last_swap_wait_ms_; }
    bool ring_fault() const { return ring_fault_; }
    uint32_t ih_ctx() const { return knobs_.variant == "ctx2" ? 2u : knobs_.variant == "ctx4" ? 4u : 1u; }   // T-B10a nonce contexts per lane group
    bool ring_fault_ = false;   // a batch the device failed or a ring slot the runtime refused: the run ended as a fault
    // IgneumHash and RXE1: the next epoch built in slices on the mining queue (one slice per mining batch), so both make progress
    std::string rx_probe_define() const { const std::string &v = knobs_.variant; int n = v == "probe-alu" ? 1 : v == "probe-shuffle" ? 2 : v == "probe-chain" ? 3 : v == "probe-decode" ? 4 : 0; return n ? "#define IGM_RX_PROBE " + std::to_string(n) + "\n" : std::string(); }   // the Mosaic lane microbenchmarks (diagnostics)
    void ih_warm_step(uint32_t items);
    void ih_warm_finish();
    uint32_t ih_next_cursor_ = 0, ih_next_items_ = 0; cl_mem ih_next_batchbuf_ = nullptr; uint32_t ih_slice_ = 1u << 16;
    void ih_run_job(const Job &job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop);
    bool build_fishhash_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, Refusal &r);
    bool build_autolykos_table(uint64_t height, cl_mem &table, uint64_t &bytes, double &ms, Refusal &r, const std::function<bool()> *stop = nullptr);
    void warm_start(uint64_t height);
    void warm_step(uint32_t elements);
    void warm_finish();
    uint64_t free_memory_estimate() const;
    uint32_t hashes_for_duration() const;
    uint32_t work_group() const;
    uint8_t forge_table_seed_[32] = {0};   // the period seed the r0f_prog slot's period table was built from (version 21, the chain form)   // the launch's work-group size (FORGE: clamped to the device's local memory)

    ClDeviceInfo info_;
    int index_;
    std::string kernel_dir_, algo_, params_;
    const char *params_c() const { return params_.empty() ? nullptr : params_.c_str(); }
    const Candidate *cand_ = nullptr;
    Knobs knobs_;
    ignpow_ctx *ctx_ = nullptr;
    uint64_t height_ = 0, next_height_ = ~0ull;
    cl_context ctxcl_ = nullptr;
    cl_command_queue queue_ = nullptr, build_queue_ = nullptr;   // build_queue_: the next epoch's dataset beside the mining (the founder's epoch-overlap row)
    cl_event build_done_ = nullptr; double last_swap_wait_ms_ = 0.0;
    cl_program prog_ = nullptr, dag_prog_ = nullptr;
    std::string last_src_; uint32_t ih_eff_scratch() const { return ih_.version == 10 ? 16384u : ih_.version == 11 ? 4096u : ih_.scratch_bytes; }   // version 10: the nonce's 16 KB gather table lives in the scratch buffer (the document's scratch is 0)
    uint32_t ih_built_version_ = ~0u; uint64_t r0d_height_ = 0, r0d_period_ = ~0ull; bool r0d_from_job_ = false;   // version 9 (R0D): the height and period the programme in the built source belongs to   // the document version the search program was compiled for (IGM_IH_VERSION)   // the search program's source as built (the trace program is the same text under IGM_IH_TRACE_FULL)
    cl_program trace_prog_ = nullptr; cl_kernel trace_k_ = nullptr; cl_mem trace_buf_ = nullptr; size_t trace_words_ = 0; bool use_trace_ = false;
    cl_kernel search_k_ = nullptr, table_k_ = nullptr, dag_k_ = nullptr;
    cl_mem dataset_ = nullptr, next_dataset_ = nullptr, light_ = nullptr;
    cl_mem job_buf_[8] = {nullptr}, results_[8] = {nullptr}, count_[8] = {nullptr};   // the batch ring: knobs_.inflight slots (T-B9)
    uint64_t dataset_bytes_ = 0, next_dataset_bytes_ = 0, allocated_ = 0;
    uint32_t next_cursor_ = 0, next_n_ = 0;
    cl_event next_ev_ = nullptr;
    uint32_t wave_ = 64, max_results_ = 1024;
    uint32_t fh_lanes_ = 8; bool fh_shuffle_ = false;
    IhParams ih_; uint8_t ih_P_[32] = {0}, ih_E_[32] = {0}, ih_next_E_[32] = {0};
    Rxe1Params rx_; uint8_t rx_pid_[32] = {0}; cl_mem rx_sched_ = nullptr, rx_rsched_ = nullptr; std::vector<uint32_t> rx_sched_host_; uint64_t rx_epoch_ = ~0ull;   // RXE1 (IH 06)
    bool rxe1_ = false;
    cl_mem dataset2_ = nullptr;   // T-B8 split2: the upper half of the dataset in its own buffer
    cl_kernel r12_fill_k_ = nullptr; cl_mem r12_shared_ = nullptr, r12_seed_ = nullptr, r12_batch_ = nullptr; uint64_t r12_period_ = ~0ull;
    cl_mem r0f_key_ = nullptr, r0f_prog_ = nullptr; uint64_t r0f_period_ = ~0ull; uint8_t r16f_key_[32] = {};   // version 15: the period key words and the packed programme; version 16: the period key drawn with the programme (r16f_program_source)
    cl_mem r17_tex_ = nullptr;   // version 17: the static texture (64 MiB R32 uint image2d)
    cl_mem r0f_trace_buf_ = nullptr; bool r0f_trace_on_ = getenv("IGM_R0F_TRACE") != nullptr; void r0f_trace_print(const Job &job);
    cl_mem r0e_optrace_buf_ = nullptr; size_t r0e_optrace_words_ = 0; bool r0e_optrace_on_ = getenv("IGM_R0E_OPTRACE") != nullptr; void r0e_optrace_print(const Job &job); void forge_trace_print(const Job &job);   // the per-operation trace of the R0E programme (14 Oct 2026)   // version 15 staged trace (13 Oct 2026, the Arc reading)
    bool r0f_fill_period(uint64_t height);   // version 12's shared table (versions 12 to 14): the period's table, its seed, the fill kernel
    void r12_ensure_shared(); bool r12_fill_shared(uint64_t height);
    ignpow_ctx *ih_next_ctx_ = nullptr; cl_mem ih_next_light_ = nullptr;
    IhParams ih_next_ih_; uint8_t ih_next_P_[32] = {0}; std::string ih_next_params_;   // the document the next dataset is for (prepare.params); empty = the resident one
    cl_mem ih_scratch_ = nullptr, ih_st_ = nullptr; uint32_t ih_slots_ = 0;
    // T-B14 the bucket solver: its kernels and buffers (variant "bucket")
    bool is_bucket() const { return knobs_.variant == "bucket"; }
    cl_kernel ihb_setup_k_ = nullptr, ihb_hist_k_ = nullptr, ihb_scan_k_ = nullptr, ihb_scatter_k_ = nullptr, ihb_fetch_k_ = nullptr, ihb_mix_k_ = nullptr, ihb_final_k_ = nullptr;
    cl_mem ihb_state_ = nullptr, ihb_req_ = nullptr, ihb_hist_ = nullptr, ihb_cursor_ = nullptr, ihb_order_ = nullptr, ihb_items_ = nullptr, ihb_job_ = nullptr;
    uint32_t ihb_batch_ = 0;
    void run_bucket(const Job &job, const void *base_job, const std::function<void(const Result &)> &on_result, const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop);
    double last_gpu_ms_ = 0, last_table_ms_ = 0;
    uint32_t last_hashes_ = 0;
};

}  // namespace igm
