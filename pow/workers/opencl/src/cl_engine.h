// cl_engine.h: the OpenCL backend of the one worker core (EngineIface): the portable
// baseline with the sub-group path when the device allows it, the device-built dataset,
// bounded batches, cancellation, the memory budget by A's method (DESIGN.md 4.1).
// GPU engineer B, 9 October 2026. Source only; not built, not run.
#pragma once
#include "cl_shim.h"
#include <string>
#include <vector>
#include "candidates.h"
#include "engine_iface.h"
#include "ignpow.h"

namespace igm {

struct ClDeviceInfo {
    cl_platform_id platform = nullptr;
    cl_device_id device = nullptr;
    std::string platform_name, name, vendor, driver, version, extensions, board, opencl_c;
    cl_device_type type = 0;
    cl_ulong global_mem = 0, max_alloc = 0, local_mem = 0;
    size_t max_wg = 0;
    unsigned compute_units = 0;   // the probe's native-ceiling arithmetic
    bool subgroups = false, subgroup_shuffle = false;
    std::string arch;   // "gfx1030" from cl_amd_device_attribute_query where available, else ""
    std::string pci;    // "0000:03:00.0" from cl_amd_device_topology where available, else ""
    std::string hwmon;  // Linux: the amdgpu hwmon directory for this device, else ""
    std::string uuid;       // cl_khr_device_uuid: CL_DEVICE_UUID_KHR as 32 hex characters where offered (Adrenalin, ROCm, NEO, rusticl), else ""
    std::string unique_id;  // Linux amdgpu: /sys/bus/pci/devices/<pci>/unique_id where the bus is known and the file answers, else ""
    std::string identity_note;  // why the identity is what it is: the extension offered or not, a zero UUID, the sysfs file read or absent (the record's cause)
};

// The card's identity for the fingerprint (the project's ruling of 11 Oct 2026 21:07 UK, the five-term hash of the product team's supervisor: backend,
// vendor, the intrinsic id, the name, the arch, each followed by a zero byte): the identity word and the term. "uuid" (CL_DEVICE_UUID_KHR),
// else "amdgpu-unique-id" (the Linux sysfs file), else "bus" (the PCI bus id, or the platform's ordinal when no bus is known), named as such.
std::pair<std::string, std::string> cl_identity(const ClDeviceInfo &info, const std::string &id);
/// The chip token of a driver's device line (rusticl: the second token inside the parentheses), else the line cut to 32 bytes.
std::string cl_chip_token(const std::string &name);
std::string cl_fingerprint(const std::string &vendor, const std::string &term, const std::string &name, const std::string &arch);
// The ADLX identity fields of an AMD device on Windows (UniqueId, the PCI ids, the type), as a Json object for the listing, or null:
// the reading that decides whether a Windows AMD intrinsic form exists (the project's order of 11 Oct 2026 23:03 UK); read-only, bound by board name.
Json cl_adlx_identity(const ClDeviceInfo &info);

// ADLX telemetry (Windows, AMD): binds once for this device by board name and adds power_w (with
// power_source), core_mhz, mem_mhz, temp_c, hotspot_c, fan_rpm, util_pct when the library answers.
void cl_adlx_telemetry(const ClDeviceInfo &info, Json &t);
// Level Zero Sysman telemetry (Intel Arc, Windows and Linux): binds once by device name and adds power_w (with
// power_source "level-zero sysman card" or "... domain 0"), core_mhz, mem_mhz, temp_c when the loader answers.
void cl_intel_telemetry(const ClDeviceInfo &info, Json &t);
// The Intel rotate-fold guard: "#define IGM_INTEL_ROTR_FOLD 1\n" on an Intel platform, empty elsewhere; prefixed to
// every program source before clCreateProgramWithSource (prelude_cl.inc and igm_progpow.cl read it).
std::string cl_intel_defines(const ClDeviceInfo &info);

std::vector<ClDeviceInfo> enumerate_cl_devices();
bool cl_sysfs_num(const std::string &path, double &out);   // Linux sysfs: one number from a file (the hwmon readings; the telemetry verb)

class ClEngine : public EngineIface {
public:
    ClEngine(const ClDeviceInfo &info, int index, const std::string &kernel_dir);
    ~ClEngine() override;

    std::string backend() const override { return "opencl"; }
    Json device_json() const override;
    Json knob_decls() const override;
    bool open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) override;
    bool prepare(uint64_t height, Refusal &r, const std::string &params = std::string()) override;
    void close() override;
    void set_knobs(const Knobs &k) override;
    Knobs knobs() const override { return knobs_; }
    uint64_t allocated_bytes() const override { return allocated_; }
    uint64_t dataset_bytes() const override { return dataset_bytes_; }
    Json budget_json() const override;
    Json code_json() const override;
    void telemetry(Json &t) const override;
    bool paused() const override { return false; }   // no host policy sources on a pod; the supervisor pauses by cancel
    void run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                 const std::function<void(const BatchStats &)> &on_progress,
                 const std::function<bool()> &should_stop) override;
    std::string describe() const override;

private:
    bool build_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, Refusal &r);
    bool build_program(uint64_t prog_seed, std::string &explain);
    uint64_t free_memory_estimate() const;
    uint32_t hashes_for_duration() const;

    ClDeviceInfo info_;
    int index_;
    std::string kernel_dir_, base_source_, dag_source_, algo_, params_;
    const char *params_c() const { return params_.empty() ? nullptr : params_.c_str(); }
    const Candidate *cand_ = nullptr;
    Knobs knobs_;
    ignpow_ctx *ctx_ = nullptr, *next_ctx_ = nullptr;
    uint64_t prog_seed_ = ~0ull;

    cl_context ctxcl_ = nullptr;
    cl_command_queue queue_ = nullptr;
    cl_program search_prog_ = nullptr, dag_prog_ = nullptr;
    cl_kernel search_k_ = nullptr, dag_k_ = nullptr;
    cl_mem dataset_ = nullptr, next_dataset_ = nullptr, light_ = nullptr, fault_ = nullptr;
    cl_mem job_buf_[2] = {nullptr, nullptr}, results_[2] = {nullptr, nullptr}, count_[2] = {nullptr, nullptr};
    uint64_t dataset_bytes_ = 0, next_dataset_bytes_ = 0, allocated_ = 0;
    uint32_t wave_ = 64;
    bool lane_shuffle_ = false;
    uint32_t max_results_ = 1024;   // per batch; an overflow re-runs the batch in halves, never drops a share
    double last_gpu_ms_ = 0;
    uint32_t last_hashes_ = 0;
};

}  // namespace igm
