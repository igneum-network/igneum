// The device engine: context, memory budget, dataset generation, period
// modules with a compile cache, exact nonce-range search, telemetry.
#pragma once

#include <cuda.h>
#include "igneumhash.h"
#include "rxe1.h"
#include <nvml.h>

#include <cstdint>
#include <cstring>
#include <cstdlib>
#include <functional>
#include <future>
#include <map>
#include <mutex>
#include <string>
#include <vector>

namespace igneum {

struct hit {
    uint64_t nonce;
    uint8_t mix[32];
    uint8_t hash[32];
};

struct telemetry {
    bool ok = false;
    double power_w = 0;
    unsigned temp_c = 0;
    unsigned core_mhz = 0, mem_mhz = 0;
    uint64_t mem_used = 0, mem_total = 0;
    double power_limit_w = 0;
    unsigned long long throttle = 0;
};

struct budget {
    uint64_t total = 0, free_before = 0;
    uint64_t cache = 0, dataset = 0, scratch = 0, margin = 0;
    uint64_t fit_cold = 0, fit_warm = 0;
    bool can_cold = false, can_warm = false;
};

struct device_info {
    int ordinal = 0;
    std::string name, pci;
    int cc_major = 0, cc_minor = 0;
    uint64_t total_mem = 0;
    std::string arch_option;  // the NVRTC -arch used (sm_XY or compute_XY)
    bool ptx_fallback = false;
    int driver_version = 0;
    unsigned slowdown_c = 0;  // the driver's slowdown temperature, 0 when not reported
    bool integrated = false;
};

class engine {
public:
    engine();
    ~engine();
    engine(const engine&) = delete;
    engine& operator=(const engine&) = delete;

    static int device_count();
    // the first device whose name contains the substring (case-insensitive), or -1
    static int find_device(const std::string& name_part);
    // Throws std::runtime_error on failure. margin_bytes is the fixed safety margin.
    void init(int ordinal, const std::string& algo, uint64_t margin_bytes = 256ull << 20, const std::string& params_json = "", bool defer_static = false);
    // the precompile verb (the product team, 12 Oct 2026): the module compiled for another architecture into the cubin cache, no load
    void precompile_arch(int sm);
    bool compile_only_ = false;
    // two engines on one device (the warm prepare path, 11 Oct 2026 05:2x UK): each owns a context, and every entry point makes its own current
    void make_current();  // body in engine.cpp, after the loader include (the Windows build binds the driver by name)
    const char* params() const { return params_.empty() ? nullptr : params_.c_str(); }
    // tuning round 1: "read=N,minblocks=N,slots=N" (TUNING-R0A.md); set before init
    void set_tune(const std::string& t) { tune_ = t; }
    // the programme anchor (versions 9 to 14; the product team's router, 03:2x UK 13 Oct): the pool's program_anchor per coordinate notify, applied
    // at the next prepare by a module rebuild for the period (the programme is compiled in), the dataset untouched; false = not 64 hex
    bool set_anchor(const std::string& hex64) {
        if (hex64.size() != 64) return false;
        uint8_t a[32]; for (int i = 0; i < 32; ++i) { char* e = nullptr; const std::string b = hex64.substr(2 * i, 2); a[i] = (uint8_t)std::strtoul(b.c_str(), &e, 16); if (!e || *e) return false; }
        if (std::memcmp(a, ihp_.program_anchor, 32) == 0) return true;
        std::memcpy(ihp_.program_anchor, a, 32); ih_r0d_period_ = ~0ull;   // the next prepare rebuilds the period's module with the new anchor
        return true;
    }
    uint64_t tune(const char* key, uint64_t dflt) const;
    uint64_t ih_minblocks() const;   // the launch-bounds occupancy floor from minblocks or the regs cap (the ladder's family 2)
    const device_info& info() const { return info_; }

    // Memory terms for the epoch of height, with the current free memory.
    budget budget_for(uint64_t height);
    // Prepare the epoch of height (cold: release the old dataset first).
    // Returns false and sets reason when the fit exceeds the budget.
    bool prepare(uint64_t height, std::string& reason, double* ms_out = nullptr, uint64_t* bytes_out = nullptr);
    uint64_t current_epoch() const { return cur_epoch_; }
    bool has_dataset() const { return d_dag_ != 0; }

    // Search nonces [start, start + count) at height for hashes <= target
    // (32 bytes, big-endian integer). Hits are appended to out. Returns the
    // number of nonces hashed; stops early and returns less only when
    // should_stop() says so between launches. batch is nonces per launch.
    uint64_t search(uint64_t height, const uint8_t header[32], const uint8_t target[32], uint64_t start,
                    uint64_t count, std::vector<hit>& out, const std::function<bool()>& should_stop = nullptr);
    // Every hash of [start, start + count) in nonce order (count <= 2^20): the
    // exactness path for range vectors; the hit list is complete by construction.
    void search_all(uint64_t height, const uint8_t header[32], uint64_t start, uint32_t count, std::vector<hit>& out);
    void set_batch(uint32_t nonces) { batch_ = nonces; }
    uint32_t batch() const { return batch_; }

    telemetry read_telemetry();
    uint64_t free_bytes();
    std::string build_notes() const { return notes_; }
    // the compiled kernels' resource facts (registers, local and shared bytes, max threads per block) as a JSON array,
    // one entry per loaded function, read through cuFuncGetAttribute after each cuModuleGetFunction (TUNING-R0A.md: the
    // counters every tuned cell reports); and the bytes the kernel moves per hash from its own count
    std::string kernels_json() const;
    uint64_t bytes_per_hash() const;
    // the product team's optional fields: the dataset commitment of the prepared epoch (hex, igneumhash only) and the compiled code bytes resident
    std::string dataset_digest_hex() const { return dataset_digest_; }
    uint64_t code_bytes() const { return code_bytes_; }
    uint32_t slots() const { return ih_slots_; }
    // a read-only probe of the card's memory service: linear and random 128-byte-line reads over a buffer of bytes
    // (GB/s each), the peak a tuned cell's achieved bandwidth is a fraction of
    int sm_count() const { return sm_count_; }
    double latency_ns(uint64_t bytes, uint32_t chases);
    void ceiling_width(uint64_t bytes, uint32_t width, uint32_t contexts, uint32_t reads, double& gbs, double& req_per_s, double& power_w, uint32_t& regs);
    // block and shared_bytes: the matched-resource form (the outside review's control, 10 Oct 2026 18:1x UK): the dependent chain launched
    // with the rung kernel's block and its dynamic shared memory per block, so the probe's resident occupancy is the kernel's
    // (the builder removed, the register, shared-memory and occupancy footprint kept); 256 and 0 are the plain probe
    void fp_check(const std::vector<uint32_t>& xyz, std::vector<uint32_t>& out, bool wide = false);
    void grind_read(std::vector<uint32_t>& out);   // the per-nonce cycle counts of the last launch under --tune grind=1
    uint32_t grind_words_ = 0;   // the fp class (version 8 bit 4) on the device
    void ceiling_lanes(uint64_t bytes, uint32_t width, uint32_t contexts, uint32_t reads, bool dependent, double& gbs, double& req_per_s, double& power_w, uint32_t& regs, unsigned block = 256, unsigned shared_bytes = 0, unsigned lanes = 8);
    void bwprobe(uint64_t bytes, double& linear_gbs, double& random_gbs);
    void ceiling(uint64_t bytes, uint32_t depth, uint32_t contexts, bool dependent, double& gbs, double& req_per_s, double& power_w, uint32_t& regs);

private:
    CUmodule module_for_period(uint64_t period);
    void load_static_module();
    std::string compile(const std::string& src, const std::string& key, bool& used_ptx);
    void check(CUresult r, const char* what);
    void release_dataset();

    device_info info_;
    CUdevice dev_ = 0;
    CUcontext ctx_ = nullptr;
    CUmodule static_mod_ = nullptr;
    bool static_deferred_ = false;
    bool pe_ = false;  // progpow-epic: ProgPoW 0.9.2 on the KAWPOW engine (period 50, 256-parent nodes, plain keccak seeding)
    CUfunction f_dag_gen_ = nullptr;
    bool al_ = false;  // the Autolykos path: a per-height table, one static module
    CUfunction f_al_gen_ = nullptr, f_al_search_ = nullptr;
    uint32_t al_n_ = 0;
    bool fh_ = false;  // the FishHash path: one fixed dataset of 1024-bit items, one static module
    bool ih_ = false;  // the IgneumHash-R0A path: dataset from the epoch seed, scratch for the groups in flight
    bool rx_ = false;  // the RXE1 path: the R0A v1 dataset keyed with the harness epoch seed, a host-generated schedule in constant memory
    rxe1_params rxp_{};
    uint8_t rx_pid_[32] = {};
    CUfunction f_rx_search_ = nullptr;
    void rx_load();
    void rx_upload_schedule(uint64_t epoch, const uint8_t epoch_seed[32]);
    CUfunction f_ih_gen_ = nullptr, f_ih_search_ = nullptr;
    CUdeviceptr d_seed_ = 0, d_scratch_ = 0, d_env_ = 0;
    CUdeviceptr d_r0e_st_ = 0, d_r0e_st_seed_ = 0;   // versions 12 to 14, the shared form: the period's shared table and its seed
    CUdeviceptr d_trace_ = 0, d_trace_ops_ = 0;   // the trace mode's buffers (--tune trace=1): the per-round state and the per-op slots
    CUtexObject ih_tex17_ = 0; CUarray ih_tex17_arr_ = nullptr;   // version 17 (the texture form): the static 4096 x 4096 R32_UINT texture, built once
    // Anvil's compiled per-batch form (version 21, tune fg2c=1): the batch's shapes generated on the host from the period seed and
    // compiled into their own module (the kernel's defs saved from ih_load, E saved at prepare); the launches never cross a batch
    std::string ih_src_defs_; uint8_t ih_E_[32] = {};
    struct fg2_mod { std::string key; CUmodule mod; CUfunction fn; };
    std::vector<fg2_mod> fg2_mods_;
    CUfunction fg2_batch_fn(const uint8_t header[32], uint64_t batch);
    // the whole-entry compiled form (the GPU team, forge3_shapes.hpp): the batch's module holds the prep, search and mix kernels
    struct fg3_mod { std::string key; CUmodule mod; CUfunction prep, search, mix, queue, entry, acc; };
    std::vector<fg3_mod> fg3_mods_;
    const fg3_mod& fg3_batch(const uint8_t header[32], uint64_t batch);
    // the next batch's compile overlapped with this batch's run (the full form as mined: every batch its own shapes)
    void fg3_source(const uint8_t header[32], uint64_t batch, std::string& key, std::string& src);
    std::map<uint64_t, std::pair<std::string, std::future<std::string>>> fg3_ahead_;   // batch -> (key, the compile thread's image)
    std::mutex notes_mu_;
    CUdeviceptr d_fg3_state_ = 0; size_t fg3_state_bytes_ = 0; uint32_t fg3_state_words_ = 0;
    CUfunction f_r16_mix_ = nullptr; CUdeviceptr r16_state_global_ = 0, r16_state_written_ = 0;   // version 15's split form: the mix kernel and the module global holding the state buffer's pointer
    CUdeviceptr d_fg3_queue_ = 0, d_fg3_counts_ = 0; size_t fg3_queue_bytes_ = 0;   // the regrouping form's queues
    uint64_t ih_st_period_ = ~0ull;
    size_t env_len_ = 0;   // the envelope's length before the epoch seed's 32 bytes (version 8 bit 9)
    uint64_t ih_items_ = 0; uint32_t ih_scratch_words_ = 4096, ih_rounds_ = 32, ih_lanes_ = 8, ih_slots_ = 4096, ih_item_bytes_ = 128; unsigned ih_block_ = 128;
    ignh_params ihp_{};        // the parsed variant (the reference's parser; defaults by version)
    uint32_t ih_keyed_steps_ = 0;   // versions 21 and 25 under bit 10: the keyed-read steps S from the worker's own parser (ih_params.h keyed_steps), beside the reference's parse
    std::string tune_;         // the tuning document, part of the compile key
    std::string dataset_digest_;
    uint64_t code_bytes_ = 0;
    struct kfact { std::string name; int regs = 0, local_bytes = 0, shared_bytes = 0, max_threads = 0, blocks_per_sm = 0, block = 128; };
    int sm_count_ = 0;
    std::vector<kfact> kfacts_;
    void record_kernel(CUfunction f, const char* name, int block = 128);   // block: the launch's threads a block (the occupancy is read at it)
    uint32_t ih_cell_ = 0;     // the round-arithmetic cell the loaded module was compiled with (version 2: epoch mod SCHEDULE)
    uint64_t ih_r0d_period_ = ~0ull;   // version 9 (SPEC-R0D-v0.md): the period whose programme the loaded module carries as straight-line code
    uint64_t ih_r0d_height_ = 0;       // the height the programme was drawn for (its period)
    void ih_load(uint32_t cell);
    void ih_st_fill();   // the shared form: the period's table filled on the device once per period  // compile and load the igneumhash module for the variant and the cell
    void ih_secret_words(const uint8_t header[32], uint32_t out[8]) const;   // version 8 bit 10: the bond secret's words for the params buffer (zero without the bit)
    bool ih_sk_set_ = false; uint8_t ih_sk_[32] = {};   // the job's secret (the keyed-job extension); unset: the reference's test secret from the header
public:
    bool keyed_document() const { return ih_ && ((ihp_.version < 20 && ihp_.keyed_rounds != 0) || ih_keyed_steps_ != 0); }   // versions 21 and 25: bit 10 with keyed_steps (cb73f3b33 section 2); the version 8 family as before   // the variant carries bit 10 (a job must bring sk); version 21 keeps batch_log2 in keyed_rounds (the GPU team's finding on the pool build, 15 Oct 05:2x UK)
    uint32_t keyed_rounds() const { return ihp_.version >= 20 ? ih_keyed_steps_ : ihp_.keyed_rounds; }   // the count of keyed rounds of record (the shares and m): the keyed-read steps under 21 and 25
    // Split execution (GUARD-C-CLOSURE.md 1.4, the renter; tune split=1 on a bit 10 document): the walk over [start, start + count)
    // in keyed_rounds + 1 launches; between launches the oracle callback takes slot i's requests (count x 8 words, lane word l of
    // m_i for nonce start + g at g * 8 + l; m_0 = S) and writes the 32-byte shares over them in place. Every hash under the
    // target is returned as search_all does. The secret's words in the params buffer are zero: the card never holds it.
    void split_search(uint64_t height, const uint8_t header[32], const uint8_t target[32], uint64_t start, uint32_t count,
                      const std::function<void(uint32_t i, uint64_t start, uint32_t count, uint32_t* m_shares)>& oracle, std::vector<hit>& out);
    void set_secret(const uint8_t* sk32);   // the job's sk (32 bytes) or nullptr to clear; the body in engine.cpp
    // the seal verb's custody (KEYED-SEAL-WORKER.md 1, 12 Oct 2026): the secret's host copy and the eight words behind the header and
    // target in the device params buffer overwritten with zeros, at cancel, at the job's end and at exit; the body in engine.cpp
    void wipe_secret();
    bool secret_copy(uint8_t out[32]) const { if (!ih_sk_set_) return false; std::memcpy(out, ih_sk_, 32); return true; }   // the job's sk for the seal verb's replay (the caller zeroes its copy)
private:
    CUfunction f_fh_search_ = nullptr;
    uint32_t fh_items_1024_ = 0;
    struct period_mod { uint64_t period; CUmodule mod; CUfunction fn; };
    std::vector<period_mod> mods_;
    CUdeviceptr d_cache_ = 0, d_dag_ = 0, d_params_ = 0, d_results_ = 0;
    uint64_t cache_bytes_ = 0, dag_bytes_ = 0;
    uint32_t dag_items_2048_ = 0;
    uint64_t cur_epoch_ = UINT64_MAX;
    uint64_t margin_ = 0;
    uint32_t batch_ = 1u << 20;
    std::string algo_;
    std::string params_;
    std::string notes_;
    nvmlDevice_t nvml_ = nullptr;
    bool nvml_ok_ = false;
    std::string cache_dir_;
};

}  // namespace igneum
