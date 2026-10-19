// engine_iface.h: what a backend (Metal, OpenCL) implements so the one worker loop,
// the one vectors runner and the one protocol serve both. GPU engineer B, 9 Oct 2026.
#pragma once
#include <cstdint>
#include <functional>
#include <string>
#include <vector>
#include "json.h"

namespace igm {

struct Knobs {
    uint32_t threadgroup = 4;
    uint32_t inflight = 2;   // batches in flight (T-B9): 2 to 8      // SIMD groups (Metal) or wavefronts (OpenCL) per work-group
    uint32_t batch_ms = 50;        // target duration of one batch
    double   duty = 1.0;           // fraction of wall time the device is given work
    uint64_t working_set_mb = 0;   // 0: the budget decides
    bool no_warm = false;   // the vectors and trace runs: no next-epoch dataset is warmed, so the memory guard counts the resident one alone
    std::string params;
    std::string variant;
    std::string alloc;     // the allocation form of the dataset: "" (the rule: split above 2 GiB on OpenCL), "onebuf", "split2"; beside the variant so a diagnostic can name it
    std::string exchange;   // the lane exchange: "" (the runtime's extension string decides), "shuffle" (the sub-group path regardless; a build refusal is the row), "local" (the local-memory path regardless)
    uint32_t grid_pct = 0;       // Breakwater phase C: nonces per batch as a percent (25, 50, 75, 100) of the saturation batch (the slot bound); 0 = unset; batch_nonces wins when both are set
    uint32_t batch_nonces = 0;   // the occupancy ladder (the project's plan): a fixed number of nonces per batch in flight; 0 = the batch window decides
    uint32_t duty_hold = 0;      // Orchard (the M5 Max operator's ask, 19:0x UK): 1 holds the duty at the knob's value under thermal pressure (the product's ladder off), so a ceiling row measures the hardware under the system's own throttling; the row still carries thermal_pressure and duty_effective
    uint32_t copies = 1;         // the channel-coloured allocation (the outside review's AMD solution, 10 Oct 2026 21:2x UK): 1, 2 or 4 copies of the dataset, the copy chosen by the nonce's low bits (logical addresses unchanged; a host-side form plus one select in the kernel)
    uint32_t diag_chains = 1;   // the dual-chain gate: independent dependent chains per nonce in the diag-b twin (1 or 2), a diagnostic only
    uint32_t diag_width = 128;   // T-B13 form A: bytes per independent request (64, 128, 256, 512, 1024, 4096), a diagnostic only
    uint32_t bucket_bits = 10;   // T-B14: address buckets of the sort pass (2^bits; 0 = the pipeline in nonce order, no sort)
    uint32_t bucket_batch = 1u << 16;   // T-B14: nonces per pipeline batch
    uint32_t lds_pad_kib = 0;   // the occupancy limiter without a compiler pin (the Riptide seat, 10 October 23:4x UK): KiB of local memory added per workgroup that the kernel never reads, so fewer workgroups sit per WGP and fewer dependent chains per SIMD (the queue-depth test; the card's compiler spilled under amdgpu_waves_per_eu)
    uint32_t xfer_keep = 52;   // the matched-resource diagnostic (form1-wave-xfer): kept-alive words per lane pinning the footprint (chosen on the dry run)
    uint32_t states_per_wave = 1;   // Breakwater: nonce states per wave (1, 2, 4) on the bw-* forms
    double request_ceiling_gps = 0.0;   // Breakwater: the configuration's measured random-request ceiling (G requests per second; 1.30 on the 9070 XT) for fraction_of_request_ceiling
    std::string isa_verdict;   // Breakwater: the offline ISA read of this kernel commit (a build box, clang-18 gfx1201), carried onto the row as given; empty = n/a
    // Orchard OR-0 and OR-6 (the Apple memory seat, 10 Oct 2026): the allocation forms of the dataset, every one a host-side
    // allocation choice and never a kernel change; "" is the engine's stock form on every knob
    uint32_t release_dataset = 0; // Ember's memory-pressure pause (the product team, the project's 07:12 order, 11 Oct 2026): 1 asks the engine to drop the resident dataset (and its pool) at its next loop iteration, keeping the cache and context; the engine rebuilds through its cold path before the next batch it submits; the engine clears the flag once acted on
    uint32_t shift_bytes = 0;    // OR-2's deliberately shifted allocation (the architecture lead's ask): a multiple of 16 added to the payload offset inside the front guard, 0 = the natural alignment; the shifted bytes are guard too
    uint32_t guard_mb = 0;       // guard regions of this many MiB before and after every dataset buffer, filled with an address-derived canary the harness checks (0 = none)
    std::string storage;         // "" or "shared" (MTLResourceStorageModeShared, the stock form), "private" (GPU-private storage; the host reads it back through a blit)
    std::string pretouch;        // "" or "gpu" (the GPU's first touch is the build), "cpu" (the host writes every page before the build; shared storage only), "fill" (a blit fill before the build)
    std::string persist;         // "" or "pool" (the retired buffer is kept and reused for the next epoch: persistent allocation, the stock form on Metal since or-0-leak-1), "reconstruct" (the next epoch's buffer allocated at prepare, the old one marked purgeable-empty and released at the swap)
    std::string r0d_seed, r0d_anchor;   // version 9 (R0D): the period programme's seed as 64 hex chars (a bench row or a vector file), else the anchor the seed derives from (64 hex; zeros when absent)
    std::string clopts;    // OpenCL build options for a compile check (for example "-cl-std=CL1.2"), never a tuning value   // the tuning variant of the kernel ("" = the baseline; "load4", "n1", ...), never a consensus value            // the candidate's params document (JSON text) for the reference ABI; empty = the candidate's defaults
    Json to_json() const { return Json::object().set("threadgroup", threadgroup).set("inflight", inflight).set("batch_nonces", batch_nonces).set("grid_pct", grid_pct).set("exchange", exchange).set("copies", copies).set("diag_chains", diag_chains).set("diag_width", diag_width).set("duty_hold", duty_hold).set("alloc", alloc).set("bucket_bits", bucket_bits).set("bucket_batch", bucket_batch).set("batch_ms", batch_ms).set("duty", duty).set("working_set_mb", (unsigned long long)working_set_mb).set("states_per_wave", states_per_wave).set("xfer_keep", xfer_keep).set("lds_pad_kib", lds_pad_kib).set("request_ceiling_gps", request_ceiling_gps).set("isa_verdict", isa_verdict).set("guard_mb", guard_mb).set("shift_bytes", shift_bytes).set("release_dataset", release_dataset).set("storage", storage).set("pretouch", pretouch).set("persist", persist); }
    void from_json(const Json &p) {
        if (p.has("threadgroup")) threadgroup = (uint32_t)p["threadgroup"].as_u64(threadgroup);
        if (p.has("inflight")) { inflight = (uint32_t)p["inflight"].as_u64(inflight); if (inflight < 1) inflight = 1; if (inflight > 8) inflight = 8; }
        if (p.has("batch_ms")) batch_ms = (uint32_t)p["batch_ms"].as_u64(batch_ms);
        if (p.has("duty")) duty = p["duty"].as_number(duty);
        if (p.has("working_set_mb")) working_set_mb = p["working_set_mb"].as_u64(working_set_mb);
        if (p.has("no_warm")) no_warm = p["no_warm"].as_u64(0) != 0;
        if (p.has("params")) params = p["params"].type() == Json::String ? p["params"].as_string() : p["params"].dump();
        if (p.has("variant")) variant = p["variant"].as_string();
        if (p.has("alloc")) alloc = p["alloc"].as_string();
        if (p.has("clopts")) clopts = p["clopts"].as_string();
        if (p.has("r0d_seed")) r0d_seed = p["r0d_seed"].as_string();
        if (p.has("r0d_anchor")) r0d_anchor = p["r0d_anchor"].as_string();
        if (p.has("batch_nonces")) batch_nonces = (uint32_t)p["batch_nonces"].as_u64(batch_nonces);
        if (p.has("exchange")) exchange = p["exchange"].as_string();
        if (p.has("grid_pct")) { grid_pct = (uint32_t)p["grid_pct"].as_u64(grid_pct); if (grid_pct > 100) grid_pct = 100; }
        if (p.has("duty_hold")) duty_hold = (uint32_t)p["duty_hold"].as_u64(duty_hold);
        if (p.has("copies")) { copies = (uint32_t)p["copies"].as_u64(copies); if (copies != 2 && copies != 4) copies = 1; }
        if (p.has("diag_chains")) { diag_chains = (uint32_t)p["diag_chains"].as_u64(diag_chains); if (diag_chains < 1) diag_chains = 1; if (diag_chains > 2) diag_chains = 2; }
        if (p.has("lds_pad_kib")) { lds_pad_kib = (uint32_t)p["lds_pad_kib"].as_u64(lds_pad_kib); if (lds_pad_kib > 128) lds_pad_kib = 128; }
        if (p.has("diag_width")) { diag_width = (uint32_t)p["diag_width"].as_u64(diag_width); if (diag_width < 64) diag_width = 64; if (diag_width > 4096) diag_width = 4096; }
        if (p.has("bucket_bits")) { bucket_bits = (uint32_t)p["bucket_bits"].as_u64(bucket_bits); if (bucket_bits > 12) bucket_bits = 12; }
        if (p.has("xfer_keep")) { xfer_keep = (uint32_t)p["xfer_keep"].as_u64(xfer_keep); if (xfer_keep < 1) xfer_keep = 1; if (xfer_keep > 192) xfer_keep = 192; }
        if (p.has("states_per_wave")) { states_per_wave = (uint32_t)p["states_per_wave"].as_u64(states_per_wave); if (states_per_wave < 1) states_per_wave = 1; if (states_per_wave > 4) states_per_wave = 4; }
        if (p.has("request_ceiling_gps")) request_ceiling_gps = p["request_ceiling_gps"].as_number(request_ceiling_gps);
        if (p.has("isa_verdict")) isa_verdict = p["isa_verdict"].type() == Json::String ? p["isa_verdict"].as_string() : std::string();
        if (p.has("release_dataset")) release_dataset = (uint32_t)p["release_dataset"].as_u64(release_dataset) ? 1u : 0u;
        if (p.has("shift_bytes")) { shift_bytes = (uint32_t)p["shift_bytes"].as_u64(shift_bytes); shift_bytes &= ~15u; if (shift_bytes > (64u << 20)) shift_bytes = 64u << 20; }   // up to 64 MiB: the OpenCL shifted allocation walks page and channel alignments (4 KiB, 64 KiB, 2 MiB)
        if (p.has("guard_mb")) { guard_mb = (uint32_t)p["guard_mb"].as_u64(guard_mb); if (guard_mb > 1024) guard_mb = 1024; }
        if (p.has("storage")) storage = p["storage"].type() == Json::String ? p["storage"].as_string() : std::string();
        if (p.has("pretouch")) pretouch = p["pretouch"].type() == Json::String ? p["pretouch"].as_string() : std::string();
        if (p.has("persist")) persist = p["persist"].type() == Json::String ? p["persist"].as_string() : std::string();
        if (p.has("bucket_batch")) { bucket_batch = (uint32_t)p["bucket_batch"].as_u64(bucket_batch); if (bucket_batch < 256) bucket_batch = 256; }
    }
};

struct Result { uint64_t nonce; uint8_t mix[32]; uint8_t digest[32]; };

struct Job {
    std::string job_id;
    uint64_t height = 0;           // the reference derives epoch and period from it
    uint8_t header[32] = {0};
    uint8_t target[32] = {0};      // big-endian 256-bit; results have digest <= target
    uint64_t nonce_start = 0;
    uint64_t nonce_count = 0;
    bool tuning = false;
    uint8_t sk[32] = {0};          // the keyed-job extension (version 8 bit 10): the bond secret on the job, present when has_sk
    bool has_sk = false;
    bool seal = false;             // the seal verb (KEYED-SEAL-WORKER.md 1): the first result at or under the boundary ends the job
    uint64_t deadline_ms = 0;      // the seal verb: the search ends at the round's end without a cancel (a done line, reason deadline)
};

struct BatchStats { uint64_t hashes = 0; double gpu_ms = 0; double wall_ms = 0; };

struct Refusal { std::string code; std::string explain; uint64_t need_bytes = 0, have_bytes = 0; };

class EngineIface {
public:
    virtual ~EngineIface() = default;
    virtual void wipe_secret() {}                            // the seal verb: zero the secret wherever the engine holds it (the device's job buffers); after a seal, a cancel, the end, a shutdown
    virtual bool keyed_document() const { return false; }   // true when the current document carries version 8 bit 10 (a job then needs sk)
    virtual std::string backend() const = 0;                 // "metal", "opencl"
    virtual Json device_json() const = 0;                    // one entry of `devices`
    virtual Json knob_decls() const = 0;                     // the `knobs` list of `hello`
    virtual bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) = 0;
    // prepare(height): the next epoch's dataset built beside mining. params, when given and different from the resident
    // document, is the parameter document that dataset is for (the node announces a parameter change ahead: nextParams,
    // nextParamsHeight); the engine keeps it across the configure that names the same document, so the change is warm
    // where two fit. An empty params means the resident document.
    virtual bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) = 0;
    virtual void close() = 0;
    virtual void set_knobs(const Knobs &k) = 0;
    virtual Knobs knobs() const = 0;
    virtual uint64_t allocated_bytes() const = 0;
    virtual uint64_t dataset_bytes() const = 0;
    // The budget terms of A's DESIGN.md section 6 for `ready`: total_bytes, free_bytes,
    // fit_cold_bytes, fit_warm_bytes, can_warm (and, on Apple, the working-set limit and
    // the headroom used).
    virtual Json budget_json() const = 0;
    virtual Json code_json() const { return Json::object(); }
    virtual bool program_binary(std::vector<unsigned char> &out) const { (void)out; return false; }   // the built device binary's raw bytes (the runner's decision-lane record)
    // the next epoch built beside the resident one on a second queue: prepare() returns once the build is enqueued;
    // prepare_ready() says whether it has finished; the swap in run_job waits for it and records the wait
    virtual bool prepare_ready() const { return true; }
    // the vectors' dataset class: the epoch seed E, the reference's D, the BLAKE3 digests of the resident cache and of the
    // resident dataset as the device holds it (read back and hashed on the host); false where the engine cannot
    virtual bool dataset_digests(uint8_t E[32], uint8_t D[32], uint8_t cache_digest[32], uint8_t dataset_digest[32]) { (void)E; (void)D; (void)cache_digest; (void)dataset_digest; return false; }
    virtual double last_swap_wait_ms() const { return 0.0; }
    // Orchard OR-0 (the guard regions around every dataset allocation): the canary state of every live dataset buffer's front and
    // back guard after the GPU is idle: {"guard_mb", "buffers": [{"name", "front", "back", "first_bad_offset"...}], "intact"}; false
    // where the engine has no guard path or no guard was asked for (guard_mb 0)
    virtual bool guard_status(Json &out) { (void)out; return false; }
    // the memory counters of the process and the host for the transition rows (OR-6, OR-8): phys_footprint_bytes, resident_bytes,
    // vm_faults, vm_pageins, vm_pageouts, vm_compressions, vm_decompressions, vm_swapouts, compressor_bytes (cumulative host
    // counters; the harness takes deltas and peaks); an engine without the path leaves the object empty
    virtual void memory_stats(Json &out) const { (void)out; }
    // The flip trace (security's TP-125, Breakwater): read or write one word of the resident dataset by its logical word
    // index (the current epoch), so a harness can flip a word of an item and trace the hash again; an engine without the
    // path returns false. The seat that uses it restores every word it writes.
    virtual bool dataset_word(uint64_t word_index, uint32_t *value, bool write) { (void)word_index; (void)value; (void)write; return false; }
    // The full trace of one nonce (strata/RIPTIDE.md section 2: the reference's trace record): the kernel built with
    // IGM_IH_TRACE_FULL writes [S, E, P, init, fill, ... rounds at 704 + r (130 + item_words): idx, h, item, regs; M at 560,
    // F at 568]; an engine without the path returns false (the identity set is then not written).
    virtual bool trace_one(uint64_t height, const uint8_t header[32], uint64_t nonce, std::vector<uint32_t> &words, Refusal &r) { (void)height; (void)header; (void)nonce; (void)words; (void)r; return false; }   // the generated code as the runtime reports it (SPEC-TEXT section 10): private and local memory, the launch shape, instruction counts where known
    // The telemetry object of `stat`: power_w, temp_c, fan_pct, util_pct, core_mhz,
    // mem_mhz, memory_used_bytes, throttle (list), thermal_pressure, on_battery; a field
    // the platform does not report is omitted, never guessed.
    virtual void telemetry(Json &t) const = 0;
    virtual bool paused() const = 0;                         // policy pause (battery, thermal, sleep, pressure)
    virtual void run_job(const Job &job,
                         const std::function<void(const Result &)> &on_result,
                         const std::function<void(const BatchStats &)> &on_progress,
                         const std::function<bool()> &should_stop) = 0;
    virtual std::string describe() const = 0;                // "width 32 lane path shuffle" for the run log
};

}  // namespace igm
