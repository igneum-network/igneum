// cl_engine.cpp: see cl_engine.h. Source only; not built, not run.
#include "cl_engine.h"
#include "adlx_probe.h"
#include "l0_probe.h"
#include "ignpow_ext.h"
#include "progpow_gen.h"
#include "refbridge.h"
#include "sha256.h"
#include "kernel_source.h"
#include <cstdio>
#include <cstdlib>
#include <chrono>
#include <cstring>
#include <fstream>
#include <sstream>
#include <thread>
#ifdef __linux__
#include <dirent.h>
#endif

namespace igm {
namespace {

std::string read_file(const std::string &path) { std::ifstream f(path, std::ios::binary); std::stringstream ss; ss << f.rdbuf(); return ss.str(); }
struct GpuJob { uint32_t header[8]; uint32_t target[8]; uint32_t nonce_lo, nonce_hi; uint32_t dag_entries; uint32_t hashes; uint32_t max_results; uint32_t pad_first[15]; uint32_t pad_final[9]; };
struct GpuResult { uint32_t nonce_lo, nonce_hi; uint32_t mix[8]; uint32_t digest[8]; };
inline uint32_t le32(const uint8_t *p) { return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24); }
inline uint32_t be32(const uint8_t *p) { return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | (uint32_t)p[3]; }
inline void put_le32(uint8_t *p, uint32_t v) { p[0] = v & 255; p[1] = (v >> 8) & 255; p[2] = (v >> 16) & 255; p[3] = (v >> 24) & 255; }
double now_ms() { using namespace std::chrono; return duration<double, std::milli>(steady_clock::now().time_since_epoch()).count(); }
std::string mb(uint64_t b) { return std::to_string(b >> 20) + " MB"; }

std::string dev_str(cl_device_id d, cl_device_info p) {
    size_t n = 0; clGetDeviceInfo(d, p, 0, nullptr, &n);
    if (!n) return "";
    std::string s(n, '\0'); clGetDeviceInfo(d, p, n, s.data(), nullptr);
    while (!s.empty() && s.back() == '\0') s.pop_back();
    return s;
}
std::string plat_str(cl_platform_id p, cl_platform_info i) {
    size_t n = 0; clGetPlatformInfo(p, i, 0, nullptr, &n);
    if (!n) return "";
    std::string s(n, '\0'); clGetPlatformInfo(p, i, n, s.data(), nullptr);
    while (!s.empty() && s.back() == '\0') s.pop_back();
    return s;
}
std::string build_log(cl_program prog, cl_device_id dev) {
    size_t n = 0; clGetProgramBuildInfo(prog, dev, CL_PROGRAM_BUILD_LOG, 0, nullptr, &n);
    std::string s(n, '\0'); clGetProgramBuildInfo(prog, dev, CL_PROGRAM_BUILD_LOG, n, s.data(), nullptr);
    return s.substr(0, 2000);
}
#ifndef CL_DEVICE_BOARD_NAME_AMD
#define CL_DEVICE_BOARD_NAME_AMD 0x4038
#endif
#ifndef CL_DEVICE_GLOBAL_FREE_MEMORY_AMD
#define CL_DEVICE_GLOBAL_FREE_MEMORY_AMD 0x4039
#endif
#ifndef CL_DEVICE_GFXIP_MAJOR_AMD
#define CL_DEVICE_GFXIP_MAJOR_AMD 0x404A
#define CL_DEVICE_GFXIP_MINOR_AMD 0x404B
#endif
#ifndef CL_DEVICE_UUID_KHR
#define CL_DEVICE_UUID_KHR 0x106A          // cl_khr_device_uuid: 16 bytes, the driver's own identity of the card
#endif
#ifndef CL_DEVICE_PCI_BUS_INFO_KHR
#define CL_DEVICE_PCI_BUS_INFO_KHR 0x410F  // cl_khr_pci_bus_info: domain, bus, device, function (rusticl, NEO; AMD's own topology query beside)
#endif
#ifndef CL_DEVICE_TOPOLOGY_AMD
#define CL_DEVICE_TOPOLOGY_AMD 0x4037
#endif

// Linux amdgpu hwmon: one value file, scaled; false when absent.
bool sysfs_num(const std::string &path, double &out) {
    std::ifstream f(path); if (!f) return false;
    double v = 0; f >> v; if (!f) return false; out = v; return true;
}

}  // namespace
bool cl_sysfs_num(const std::string &path, double &out) { return sysfs_num(path, out); }

std::vector<ClDeviceInfo> enumerate_cl_devices() {
    std::vector<ClDeviceInfo> out;
#ifdef _WIN32
    if (!cl_fns().loaded) { fprintf(stderr, "opencl: no OpenCL runtime on this machine (%s not found)\n", cl_fns().missing ? cl_fns().missing : "OpenCL.dll"); return out; }
#endif
    cl_uint np = 0; clGetPlatformIDs(0, nullptr, &np);
    std::vector<cl_platform_id> ps(np); clGetPlatformIDs(np, ps.data(), nullptr);
    for (cl_platform_id p : ps) {
        cl_uint nd = 0; clGetDeviceIDs(p, CL_DEVICE_TYPE_ALL, 0, nullptr, &nd);
        std::vector<cl_device_id> ds(nd); if (nd) clGetDeviceIDs(p, CL_DEVICE_TYPE_ALL, nd, ds.data(), nullptr);
        for (cl_device_id d : ds) {
            ClDeviceInfo i; i.platform = p; i.device = d;
            i.platform_name = plat_str(p, CL_PLATFORM_NAME);
            i.name = dev_str(d, CL_DEVICE_NAME); i.vendor = dev_str(d, CL_DEVICE_VENDOR);
            i.driver = dev_str(d, CL_DRIVER_VERSION); i.version = dev_str(d, CL_DEVICE_VERSION); i.opencl_c = dev_str(d, CL_DEVICE_OPENCL_C_VERSION);
            i.extensions = dev_str(d, CL_DEVICE_EXTENSIONS);
            clGetDeviceInfo(d, CL_DEVICE_TYPE, sizeof i.type, &i.type, nullptr);
            clGetDeviceInfo(d, CL_DEVICE_GLOBAL_MEM_SIZE, sizeof i.global_mem, &i.global_mem, nullptr);
            clGetDeviceInfo(d, CL_DEVICE_MAX_MEM_ALLOC_SIZE, sizeof i.max_alloc, &i.max_alloc, nullptr);
            clGetDeviceInfo(d, CL_DEVICE_LOCAL_MEM_SIZE, sizeof i.local_mem, &i.local_mem, nullptr);
            clGetDeviceInfo(d, CL_DEVICE_MAX_WORK_GROUP_SIZE, sizeof i.max_wg, &i.max_wg, nullptr);
            { cl_uint cu = 0; clGetDeviceInfo(d, CL_DEVICE_MAX_COMPUTE_UNITS, sizeof cu, &cu, nullptr); i.compute_units = cu; }
            i.subgroups = i.extensions.find("cl_khr_subgroups") != std::string::npos;
            i.subgroup_shuffle = i.extensions.find("cl_khr_subgroup_shuffle") != std::string::npos;
            if (i.extensions.find("cl_amd_device_attribute_query") != std::string::npos) {
                i.board = dev_str(d, CL_DEVICE_BOARD_NAME_AMD);
                cl_uint maj = 0, min = 0;
                if (clGetDeviceInfo(d, CL_DEVICE_GFXIP_MAJOR_AMD, sizeof maj, &maj, nullptr) == CL_SUCCESS && clGetDeviceInfo(d, CL_DEVICE_GFXIP_MINOR_AMD, sizeof min, &min, nullptr) == CL_SUCCESS)
                    i.arch = "gfx" + std::to_string(maj) + std::to_string(min);
                // cl_device_topology_amd: raw[0..] = type, then bus, device, function at the pcie offsets
                unsigned char topo[24] = {0};
                if (clGetDeviceInfo(d, CL_DEVICE_TOPOLOGY_AMD, sizeof topo, topo, nullptr) == CL_SUCCESS && topo[0] == 1) {
                    char b[32]; snprintf(b, sizeof b, "0000:%02x:%02x.%d", topo[21], topo[22], topo[23]);
                    i.pci = b;
                }
            }
            if (i.pci.empty() && i.extensions.find("cl_khr_pci_bus_info") != std::string::npos) {
                cl_uint info4[4] = {0, 0, 0, 0};
                if (clGetDeviceInfo(d, CL_DEVICE_PCI_BUS_INFO_KHR, sizeof info4, info4, nullptr) == CL_SUCCESS) {
                    char b[32]; snprintf(b, sizeof b, "%04x:%02x:%02x.%d", info4[0] & 0xffffu, info4[1] & 0xffu, info4[2] & 0xffu, (int)(info4[3] & 7u));
                    i.pci = b;
                }
            }
            if (i.extensions.find("cl_khr_device_uuid") != std::string::npos) {
                unsigned char u[16] = {0};
                if (clGetDeviceInfo(d, CL_DEVICE_UUID_KHR, sizeof u, u, nullptr) == CL_SUCCESS) {
                    int nz = 0; for (unsigned char c : u) nz += c != 0;
                    // rusticl fills the UUID with the PCI address (the rig's RX 580 read 00000000030000..., its bus 03: the slot, not the
                    // card; gb-identity__rig1__devices-1, 21:30Z), so a UUID with fewer than four non-zero bytes is no identity: a driver's
                    // own identity is dense (NEO and ROCm hash the device; NVIDIA's is the GPU UUID); the sysfs unique_id or the bus follow
                    if (nz >= 4) { char h[33]; for (int k = 0; k < 16; k++) snprintf(h + 2 * k, 3, "%02x", u[k]); i.uuid = h; i.identity_note = "cl_khr_device_uuid offered, CL_DEVICE_UUID_KHR read"; }
                    else if (nz == 0) i.identity_note = "cl_khr_device_uuid offered, CL_DEVICE_UUID_KHR read all zeros (no identity)";
                    else { char h[33]; for (int k = 0; k < 16; k++) snprintf(h + 2 * k, 3, "%02x", u[k]); i.identity_note = std::string("cl_khr_device_uuid offered, CL_DEVICE_UUID_KHR ") + h + " encodes the bus address (rusticl), not an identity"; }
                } else i.identity_note = "cl_khr_device_uuid offered, CL_DEVICE_UUID_KHR query failed";
            } else i.identity_note = "cl_khr_device_uuid not offered";
#ifdef __linux__
            if (!i.pci.empty()) {
                std::ifstream uf("/sys/bus/pci/devices/" + i.pci + "/unique_id");
                std::string uid; if (uf && std::getline(uf, uid)) { while (!uid.empty() && (uid.back() == '\n' || uid.back() == ' ')) uid.pop_back(); if (!uid.empty() && uid != "0") i.unique_id = uid; }
                i.identity_note += i.unique_id.empty() ? "; sysfs unique_id absent or zero" : "; sysfs unique_id read";
            } else i.identity_note += "; no bus id known (no sysfs path)";
            if (!i.pci.empty()) {
                std::string base = "/sys/bus/pci/devices/" + i.pci + "/hwmon/";
                if (DIR *dir = opendir(base.c_str())) {
                    while (dirent *e = readdir(dir)) { if (e->d_name[0] != '.') { i.hwmon = base + e->d_name; break; } }
                    closedir(dir);
                }
            }
#endif
            out.push_back(i);
        }
    }
    return out;
}

std::pair<std::string, std::string> cl_identity(const ClDeviceInfo &info, const std::string &id) {
    if (!info.uuid.empty()) return {"uuid", info.uuid};
    if (!info.unique_id.empty()) return {"amdgpu-unique-id", info.unique_id};
    return {"bus", id};
}
std::string cl_fingerprint(const std::string &vendor, const std::string &term, const std::string &name, const std::string &arch) {
    std::string m; for (const std::string &p : {std::string("opencl"), vendor, term, name, arch}) { m += p; m.push_back('\0'); }
    return sha256_hex(m);
}

ClEngine::ClEngine(const ClDeviceInfo &info, int index, const std::string &kernel_dir) : info_(info), index_(index), kernel_dir_(kernel_dir) {
    base_source_ = kernel_source(kernel_dir_, "igm_progpow.cl");
    dag_source_ = kernel_source(kernel_dir_, "igm_dag.cl");
    cl_int err = 0;
    ctxcl_ = clCreateContext(nullptr, 1, &info_.device, nullptr, nullptr, &err);
    cl_queue_properties props[] = { CL_QUEUE_PROPERTIES, CL_QUEUE_PROFILING_ENABLE, 0 };
    queue_ = clCreateCommandQueueWithProperties(ctxcl_, info_.device, props, &err);
}

ClEngine::~ClEngine() {
    close();
    if (queue_) clReleaseCommandQueue(queue_);
    if (ctxcl_) clReleaseContext(ctxcl_);
}

Json ClEngine::device_json() const {
    std::string vendor = "other";
    std::string v = info_.vendor; for (auto &c : v) c = (char)tolower(c);
    if (info_.type == CL_DEVICE_TYPE_GPU) {
        if (v.find("advanced micro") != std::string::npos || v.find("amd") != std::string::npos) vendor = "amd";
        else if (v.find("nvidia") != std::string::npos) vendor = "nvidia";
        else if (v.find("intel") != std::string::npos) vendor = "intel";
        else if (v.find("apple") != std::string::npos) vendor = "apple";
    }   // a CPU device (pocl) is "other": its vendor string names the CPU maker, not a GPU
    std::string name = info_.board.empty() ? info_.name : info_.board;
    std::string id = info_.pci.empty() ? info_.platform_name + "/" + std::to_string(index_) : info_.pci;
    std::string compute = info_.arch.empty() ? info_.name : info_.arch;
    const auto ident = cl_identity(info_, id);
    return Json::object().set("index", index_).set("backend", "opencl").set("name", name).set("vendor", vendor)
        .set("id", id).set("identity", ident.first).set("intrinsic_id", ident.second).set("identity_note", info_.identity_note).set("adlx_identity", cl_adlx_identity(info_)).set("fingerprint", cl_fingerprint(vendor, ident.second, name, compute))
        .set("memory_total", (unsigned long long)info_.global_mem)
        .set("memory_free", (unsigned long long)free_memory_estimate()).set("compute", info_.arch.empty() ? info_.name : info_.arch)
        .set("driver", info_.driver + " (" + info_.version + ")").set("platform", info_.platform_name)
        .set("subgroups", info_.subgroups).set("subgroup_shuffle", info_.subgroup_shuffle);
}

Json ClEngine::knob_decls() const {
    return Json::array()
        .push(Json::object().set("name", "threadgroup").set("min", 1).set("max", 16).set("step", 1).set("default", 2))
        .push(Json::object().set("name", "batch_ms").set("min", 10).set("max", 500).set("step", 10).set("default", 50))
        .push(Json::object().set("name", "duty").set("min", 0.25).set("max", 1.0).set("step", 0.25).set("default", 1.0))
        .push(Json::object().set("name", "working_set_mb").set("min", 0).set("max", 65536).set("step", 256).set("default", 0));
}

std::string cl_intel_defines(const ClDeviceInfo &info) {
    // The Intel OpenCL compiler folds a rotate with a negated variable count into a left rotate (the Arc B580 register-trace
    // bisect of 7 October 2026, the old tree's proto-opencl/intel_rotr.h). On an Intel platform every program source is
    // built with IGM_INTEL_ROTR_FOLD defined and the preludes' IGM_ROTR_VAR becomes the shift form; nothing else changes.
    // IGM_INTEL beside the fold define: the preludes make the shared helpers static on Intel as on AMD (the fixed 33 KB private block of
    // the B580, 11 October 2026); IGM_INTEL_NO_STATIC keeps the external-linkage form for the control row on the card.
    const char *intel = std::getenv("IGM_INTEL_NO_STATIC") ? "#define IGM_INTEL_ROTR_FOLD 1\n" : "#define IGM_INTEL_ROTR_FOLD 1\n#define IGM_INTEL 1\n";
    if (info.vendor.find("Intel") != std::string::npos || info.platform_name.find("Intel") != std::string::npos) return intel;
    if (std::getenv("IGM_IH_STATIC")) return "#define IGM_INTEL_ROTR_FOLD 1\n#define IGM_IH_STATIC 1\n";   // the proof hook for the static-index form: the Intel rotate text and the unroll hints on any platform (pocl, the 9070 XT), never the static helpers (pocl segfaults on them)
    if (std::getenv("IGM_INTEL_ROTR_FOLD")) return "#define IGM_INTEL_ROTR_FOLD 1\n";   // the proof hook: the Intel rotate text on any platform (pocl's exactness run); never the static helpers there (pocl's work-item compiler segfaults on them, exit 139, 11 Oct 2026, as on 10 Oct)
    return "";
}

void cl_intel_telemetry(const ClDeviceInfo &info, Json &t) {
    // Intel Arc on Windows and Linux: board power from Level Zero Sysman's energy counter (the card power domain where the
    // driver exposes one, else power domain 0), named as the source; absent runtime: power_source "none (level zero: <why>)".
    std::string v = info.vendor; for (auto &c : v) c = (char)tolower(c);
    if (v.find("intel") == std::string::npos) return;
    static bool tried = false; static bool ok = false;
    if (!tried) { tried = true; ok = igm_l0_open(info.board.empty() ? info.name.c_str() : info.board.c_str()) != 0; }
    if (!ok) { t.set("power_source", std::string("none (level zero: ") + igm_l0_state() + ")"); return; }
    igm_l0_sample_t s;
    if (!igm_l0_sample(&s)) return;
    if (s.power_kind) { t.set("power_w", s.power_w); t.set("power_source", s.power_kind == 2 ? "level-zero sysman card" : ("level-zero sysman domain " + std::to_string(s.domain))); }
    else t.set("power_source", std::string("level-zero sysman: ") + igm_l0_state());   // "ok" on the first samples (the counters' baseline); after three, the static-counter reason when no domain advanced
    if (s.core_mhz) t.set("core_mhz", (double)s.core_mhz);
    if (s.mem_mhz) t.set("mem_mhz", (double)s.mem_mhz);
    if (s.temp_c) t.set("temp_c", s.temp_c);
}

Json cl_adlx_identity(const ClDeviceInfo &info) {
    std::string v = info.vendor; for (auto &c : v) c = (char)tolower(c);
    if (info.type != CL_DEVICE_TYPE_GPU || (v.find("advanced micro") == std::string::npos && v.find("amd") == std::string::npos)) return Json();
    if (igm_adlx_open(info.board.empty() ? info.name.c_str() : info.board.c_str()) == 0) return Json::object().set("state", std::string(igm_adlx_state()));
    char buf[1024]; if (!igm_adlx_identity(buf, sizeof buf)) return Json::object().set("state", std::string(igm_adlx_state()));
    Json j; if (!Json::parse(std::string(buf), j)) return Json::object().set("raw", std::string(buf)); return j;
}

void cl_adlx_telemetry(const ClDeviceInfo &info, Json &t) {
    std::string v = info.vendor; for (auto &c : v) c = (char)tolower(c);
    if (v.find("advanced micro") == std::string::npos && v.find("amd") == std::string::npos) return;
    static bool tried = false; static bool ok = false;
    if (!tried) { tried = true; ok = igm_adlx_open(info.board.empty() ? info.name.c_str() : info.board.c_str()) != 0; }
    if (!ok) { t.set("power_source", std::string("none (adlx: ") + igm_adlx_state() + ")"); return; }
    igm_adlx_sample_t s;
    if (!igm_adlx_sample(&s)) return;
    if (s.power_kind) { t.set("power_w", s.power_w); t.set("power_source", s.power_kind == 2 ? "adlx board" : "adlx gpu"); }
    if (s.core_mhz) t.set("core_mhz", (double)s.core_mhz);
    if (s.mem_mhz) t.set("mem_mhz", (double)s.mem_mhz);
    if (s.temp_c) t.set("temp_c", s.temp_c);
    if (s.hotspot_c) t.set("hotspot_c", s.hotspot_c);
    if (s.fan_rpm) t.set("fan_rpm", (double)s.fan_rpm);
    t.set("util_pct", (double)s.util_pct);
    int off = 0; if (igm_adlx_power_limit_get(&off)) { t.set("power_limit_offset_pct", (double)off); t.set("power_limit_pct_of_stock", (double)(100 + off)); }
}

Json ClEngine::code_json() const {
    Json c = Json::object();
    if (!search_k_) return c;
    cl_ulong priv = 0, loc = 0; size_t pref = 0;
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PRIVATE_MEM_SIZE, sizeof priv, &priv, nullptr);
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_LOCAL_MEM_SIZE, sizeof loc, &loc, nullptr);
    clGetKernelWorkGroupInfo(search_k_, info_.device, CL_KERNEL_PREFERRED_WORK_GROUP_SIZE_MULTIPLE, sizeof pref, &pref, nullptr);
    c.set("private_mem_bytes", (double)priv).set("local_mem_bytes", (double)loc).set("preferred_multiple", (double)pref).set("wave", (double)wave_).set("workgroup", (double)(knobs_.threadgroup * wave_))
     .set("registers", "not exposed by this OpenCL runtime (private_mem_bytes is where spills land)").set("bytes_read_per_nonce", (double)bytes_read_per_hash(Kind::progpow));
    return c;
}

void ClEngine::telemetry(Json &t) const {
    // What the driver exposes: on Linux with amdgpu, the hwmon node of this device gives
    // temperature, power, fan (pwm1 of pwm1_max) and the DRM node gives gpu_busy_percent
    // and the current sclk and mclk; the throttle list carries "thermal" when the
    // temperature is at or above the critical point the driver reports. Nothing is read
    // through OpenCL itself (it exposes no telemetry); anything absent is omitted.
    // thermal_pressure and on_battery are Apple signals and are not reported here.
    t.set("throttle", Json::array()).set("memory_free_bytes", std::to_string(free_memory_estimate()));
#ifdef __linux__
    if (!info_.hwmon.empty()) {
        double v = 0, m = 0;
        Json throttle = Json::array();
        if (sysfs_num(info_.hwmon + "/temp1_input", v)) {
            t.set("temp_c", v / 1000.0);
            double crit = 0;
            if (sysfs_num(info_.hwmon + "/temp1_crit", crit) && v >= crit) throttle.push("thermal");
        }
        if (sysfs_num(info_.hwmon + "/power1_average", v) || sysfs_num(info_.hwmon + "/power1_input", v)) t.set("power_w", v / 1e6);
        if (sysfs_num(info_.hwmon + "/pwm1", v) && sysfs_num(info_.hwmon + "/pwm1_max", m) && m > 0) t.set("fan_pct", 100.0 * v / m);
        else if (sysfs_num(info_.hwmon + "/fan1_input", v)) t.set("fan_rpm", v);
        if (sysfs_num(info_.hwmon + "/freq1_input", v)) t.set("core_mhz", v / 1e6);
        if (sysfs_num(info_.hwmon + "/freq2_input", v)) t.set("mem_mhz", v / 1e6);
        std::string drm = "/sys/bus/pci/devices/" + info_.pci + "/gpu_busy_percent";
        if (sysfs_num(drm, v)) t.set("util_pct", v);
        t.set("throttle", throttle);
    }
#endif
    cl_adlx_telemetry(info_, t);
    cl_intel_telemetry(info_, t);
}

Json ClEngine::budget_json() const {
    uint64_t free_now = free_memory_estimate();
    uint64_t budget = free_now > (256ull << 20) ? free_now - (256ull << 20) : 0;
    uint64_t next_bytes = 0;
    if (cand_ && !algo_.empty() && ctx_) {
        int st = 0; uint64_t e = ignpow_ctx_epoch(ctx_);
        uint64_t h = 0; while (st == IGNPOW_OK && ignpow_epoch_of(algo_.c_str(), params_c(), h, &st) <= e && h < (e + 2) * 100000) h += 500;
        if (st == IGNPOW_OK) next_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), h, &st);
    }
    return Json::object().set("total_bytes", std::to_string(info_.global_mem)).set("free_bytes", std::to_string(free_now))
        .set("fit_cold_bytes", std::to_string(allocated_)).set("fit_warm_bytes", std::to_string(allocated_ + next_bytes))
        .set("can_warm", next_bytes > 0 && next_bytes + (64ull << 20) <= budget).set("margin_bytes", std::to_string(256ull << 20))
        .set("display_overhead", "not measured on this host");
}

std::string ClEngine::describe() const {
    return "opencl " + info_.platform_name + " wave " + std::to_string(wave_) + " lane path " + (lane_shuffle_ ? "shuffle" : "barrier") + " workgroup " + std::to_string(knobs_.threadgroup * wave_);
}

uint64_t ClEngine::free_memory_estimate() const {
    // AMD exposes free memory; others do not. A's budget method subtracts a 256 MiB
    // margin from what the driver reports; the display overhead is a measured term on
    // physical hosts, "not measured" on pods.
    if (info_.extensions.find("cl_amd_device_attribute_query") != std::string::npos) {
        size_t free_kb[2] = {0, 0};
        if (clGetDeviceInfo(info_.device, CL_DEVICE_GLOBAL_FREE_MEMORY_AMD, sizeof free_kb, free_kb, nullptr) == CL_SUCCESS) return (uint64_t)free_kb[0] * 1024;
    }
    return info_.global_mem > allocated_ ? info_.global_mem - allocated_ : 0;
}

bool ClEngine::build_dataset(uint64_t height, ignpow_ctx *&ctx, cl_mem &dataset, uint64_t &bytes, Refusal &r) {
    int st = 0; cl_int err = 0;
    ctx = ignpow_ctx_create(algo_.c_str(), params_c(), height, 0, &st);
    if (!ctx) { r.code = "unsupported"; r.explain = "reference refused a light context for height " + std::to_string(height) + " (status " + std::to_string(st) + ")"; return false; }
    bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference has no dataset size for height " + std::to_string(height); return false; }
    if (bytes > info_.max_alloc) { r.code = "memory"; r.explain = "dataset " + mb(bytes) + " exceeds the device's largest allocation " + mb(info_.max_alloc); r.need_bytes = bytes; r.have_bytes = info_.max_alloc; return false; }
    const void *light = nullptr; uint64_t light_bytes = 0; uint32_t light_item = 0;
    if (ignpow_ctx_light_cache(ctx, &light, &light_bytes, &light_item) != IGNPOW_OK || !light || light_bytes == 0) { r.code = "unsupported"; r.explain = "reference exposes no light cache for this candidate"; return false; }
    if (light_item != cand_->cache_item_bytes) { r.code = "unsupported"; r.explain = "light cache item size " + std::to_string(light_item) + " differs from the kernel's " + std::to_string(cand_->cache_item_bytes); return false; }

    light_ = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY | CL_MEM_COPY_HOST_PTR, light_bytes, const_cast<void *>(light), &err);
    if (err != CL_SUCCESS) { r.code = "memory"; r.explain = "light cache allocation failed (" + std::to_string(err) + ")"; return false; }
    dataset = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, bytes, nullptr, &err);
    if (err != CL_SUCCESS) { r.code = "memory"; r.explain = "dataset allocation of " + mb(bytes) + " failed (" + std::to_string(err) + ")"; r.need_bytes = bytes; return false; }
    allocated_ += light_bytes + bytes;

    if (!dag_prog_) {
        const char *src = dag_source_.c_str(); size_t len = dag_source_.size();
        dag_prog_ = clCreateProgramWithSource(ctxcl_, 1, &src, &len, &err);
        std::string opts = "-DIGM_DATASET_PARENTS=" + std::to_string(cand_->dataset_parents) + " -DIGM_CACHE_NODES=" + std::to_string(light_bytes / cand_->cache_item_bytes);
        if (clBuildProgram(dag_prog_, 1, &info_.device, opts.c_str(), nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel build: " + build_log(dag_prog_, info_.device); return false; }
        dag_k_ = clCreateKernel(dag_prog_, "igm_dag_generate", &err);
        if (err != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel create failed"; return false; }
    }
    uint32_t items = (uint32_t)(bytes / cand_->item_bytes);
    const uint32_t per = 1u << 20;
    for (uint32_t first = 0; first < items; first += per) {
        uint32_t count = items - first < per ? items - first : per;
        clSetKernelArg(dag_k_, 0, sizeof first, &first);
        clSetKernelArg(dag_k_, 1, sizeof count, &count);
        clSetKernelArg(dag_k_, 2, sizeof light_, &light_);
        clSetKernelArg(dag_k_, 3, sizeof dataset, &dataset);
        size_t global = ((count + 255) / 256) * 256, local = 256;
        if (clEnqueueNDRangeKernel(queue_, dag_k_, 1, nullptr, &global, &local, 0, nullptr, nullptr) != CL_SUCCESS) { r.code = "driver"; r.explain = "dag kernel enqueue failed"; return false; }
        clFinish(queue_);
    }

    uint8_t cpu[64], gpu[64];
    const uint64_t sample[] = { 0, 1, 2, 3, items / 2, (uint64_t)items - 1, 12345 % items, 0xABCDEF % items };
    static bool warned = false;
    for (uint64_t idx : sample) {
        int rc = ref_dataset_item(ctx, idx, cpu, sizeof cpu);
        if (rc == IGNPOW_E_ARG) { if (!warned) { fprintf(stderr, "dataset: no CPU item accessor in the ABI yet; items not sampled, the vector comparison is the check\n"); warned = true; } break; }
        if (rc != IGNPOW_OK) { r.code = "driver"; r.explain = "reference dataset item " + std::to_string(idx) + " failed (" + std::to_string(rc) + ")"; return false; }
        clEnqueueReadBuffer(queue_, dataset, CL_TRUE, idx * cand_->item_bytes, cand_->item_bytes, gpu, 0, nullptr, nullptr);
        if (std::memcmp(cpu, gpu, cand_->item_bytes) != 0) { r.code = "driver"; r.explain = "dataset item " + std::to_string(idx) + " differs from the CPU reference"; return false; }
    }
    return true;
}

bool ClEngine::build_program(uint64_t prog_seed, std::string &explain) {
    if (prog_seed == prog_seed_ && search_k_) return true;
    if (search_k_) { clReleaseKernel(search_k_); search_k_ = nullptr; }
    if (search_prog_) { clReleaseProgram(search_prog_); search_prog_ = nullptr; }
    std::string src = cl_intel_defines(info_) + splice_period_program(base_source_, generate_period_program(*cand_, prog_seed, Dialect::opencl));   // the Intel rotate-fold guard before the period program's text
    cl_int err = 0;
    const char *s = src.c_str(); size_t len = src.size();
    search_prog_ = clCreateProgramWithSource(ctxcl_, 1, &s, &len, &err);

    // Wave width: the kernel's preferred work-group size multiple, read from a probe
    // build; the shuffle path only when sub-groups and the shuffle extension exist and
    // the width is a multiple of the logical group.
    auto opts_for = [&](bool shuffle, uint32_t wave) {
        return "-DIGM_LANES=" + std::to_string(cand_->lanes) + " -DIGM_REGS=" + std::to_string(cand_->regs) + " -DIGM_DAG_LOADS=" + std::to_string(cand_->dag_loads) +
               " -DIGM_CACHE_WORDS=" + std::to_string(cand_->cache_bytes / 4) + " -DIGM_ROUNDS=" + std::to_string(cand_->rounds) +
               " -DIGM_WAVE=" + std::to_string(wave) + " -DIGM_LANE_SHUFFLE=" + (shuffle ? "1" : "0");
    };
    std::string opts = opts_for(false, 64);
    if (clBuildProgram(search_prog_, 1, &info_.device, opts.c_str(), nullptr, nullptr) != CL_SUCCESS) { explain = "search kernel build (probe): " + build_log(search_prog_, info_.device); return false; }
    cl_kernel probe = clCreateKernel(search_prog_, "igm_progpow_search", &err);
    if (err != CL_SUCCESS) { explain = "search kernel create (probe) failed"; return false; }
    size_t pref = 0; clGetKernelWorkGroupInfo(probe, info_.device, CL_KERNEL_PREFERRED_WORK_GROUP_SIZE_MULTIPLE, sizeof pref, &pref, nullptr);
    clReleaseKernel(probe);
    wave_ = pref ? (uint32_t)pref : 64;
    lane_shuffle_ = info_.subgroups && info_.subgroup_shuffle && wave_ >= cand_->lanes && (wave_ % cand_->lanes == 0);
    clReleaseProgram(search_prog_);
    search_prog_ = clCreateProgramWithSource(ctxcl_, 1, &s, &len, &err);
    opts = opts_for(lane_shuffle_, wave_);
    if (clBuildProgram(search_prog_, 1, &info_.device, opts.c_str(), nullptr, nullptr) != CL_SUCCESS) { explain = "search kernel build: " + build_log(search_prog_, info_.device); return false; }
    search_k_ = clCreateKernel(search_prog_, "igm_progpow_search", &err);
    if (err != CL_SUCCESS) { explain = "search kernel create failed"; return false; }
    prog_seed_ = prog_seed;
    return true;
}

bool ClEngine::open(const std::string &algo, uint64_t height, const Knobs &k, uint64_t supervisor_budget, Refusal &r) {
    close();
    algo_ = algo; knobs_ = k; params_ = k.params;
    cand_ = find_candidate(algo.c_str());
    if (!cand_) { r.code = "unsupported"; r.explain = "no kernel family for " + algo + " in this worker"; return false; }
    int st = 0;
    uint64_t bytes = ignpow_dataset_bytes(algo.c_str(), params_c(), height, &st);
    uint64_t cache = ignpow_cache_bytes(algo.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK || bytes == 0) { r.code = "unsupported"; r.explain = "reference: height " + std::to_string(height) + " outside the schedule"; return false; }
    uint64_t need = bytes + cache + (32ull << 20);
    auto budget_now = [&]() {
        uint64_t b = free_memory_estimate();
        if (b > (256ull << 20)) b -= (256ull << 20); else b = 0;   // A's fixed safety margin
        if (supervisor_budget && supervisor_budget < b) b = supervisor_budget;
        if (k.working_set_mb && (k.working_set_mb << 20) < b) b = k.working_set_mb << 20;
        return b;
    };
    uint64_t budget = budget_now();
    for (unsigned waited = 0; need > budget && waited < 3000; waited += 100) {   // a just-released dataset is reclaimed a little after the free
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
        budget = budget_now();
    }
    if (need > budget) { r.code = "memory"; r.explain = "unsafe allocation refused: need " + mb(need) + " (dataset " + mb(bytes) + " plus cache and buffers), budget " + mb(budget) + " after the 256 MiB margin"; r.need_bytes = need; r.have_bytes = budget; return false; }

    cl_int err = 0;
    if (!build_dataset(height, ctx_, dataset_, dataset_bytes_, r)) { close(); return false; }
    for (int i = 0; i < 2; i++) {
        job_buf_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_ONLY, sizeof(GpuJob), nullptr, &err);
        results_[i] = clCreateBuffer(ctxcl_, CL_MEM_WRITE_ONLY, max_results_ * sizeof(GpuResult), nullptr, &err);
        count_[i] = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, 4, nullptr, &err);
    }
    fault_ = clCreateBuffer(ctxcl_, CL_MEM_READ_WRITE, 4, nullptr, &err);
    allocated_ += 2 * (sizeof(GpuJob) + max_results_ * sizeof(GpuResult) + 4) + 4;
    uint64_t seed = 0; bool from_abi = false;
    if (!ref_program_seed(*cand_, height, seed, from_abi)) { r.code = "unsupported"; r.explain = "no program seed for " + algo; close(); return false; }
    std::string e;
    if (!build_program(seed, e)) { r.code = "driver"; r.explain = e; close(); return false; }
    return true;
}

bool ClEngine::prepare(uint64_t height, Refusal &r, const std::string &params) {
    (void)params;
    int st = 0;
    uint64_t next_bytes = ignpow_dataset_bytes(algo_.c_str(), params_c(), height, &st);
    if (st != IGNPOW_OK) { r.code = "unsupported"; r.explain = "no dataset size for the next height"; return false; }
    uint64_t budget = free_memory_estimate();
    if (budget > (256ull << 20)) budget -= (256ull << 20); else budget = 0;
    if (next_bytes + (64ull << 20) > budget) { r.code = "memory"; r.explain = "cold transition: next dataset " + mb(next_bytes) + " does not fit beside the current one (budget " + mb(budget) + ")"; r.need_bytes = next_bytes; r.have_bytes = budget; return false; }
    cl_mem nd = nullptr; uint64_t nb = 0; ignpow_ctx *nc = nullptr;
    if (!build_dataset(height, nc, nd, nb, r)) { if (nc) ignpow_ctx_free(nc); if (nd) clReleaseMemObject(nd); return false; }
    if (next_ctx_) ignpow_ctx_free(next_ctx_);
    if (next_dataset_) { clReleaseMemObject(next_dataset_); allocated_ -= next_dataset_bytes_; }
    next_ctx_ = nc; next_dataset_ = nd; next_dataset_bytes_ = nb;
    return true;
}

void ClEngine::close() {
    if (search_k_) { clReleaseKernel(search_k_); search_k_ = nullptr; }
    if (search_prog_) { clReleaseProgram(search_prog_); search_prog_ = nullptr; }
    if (dag_k_) { clReleaseKernel(dag_k_); dag_k_ = nullptr; }
    if (dag_prog_) { clReleaseProgram(dag_prog_); dag_prog_ = nullptr; }
    for (cl_mem *m : { &dataset_, &next_dataset_, &light_, &fault_, &job_buf_[0], &job_buf_[1], &results_[0], &results_[1], &count_[0], &count_[1] })
        if (*m) { clReleaseMemObject(*m); *m = nullptr; }
    if (ctx_) { ignpow_ctx_free(ctx_); ctx_ = nullptr; }
    if (next_ctx_) { ignpow_ctx_free(next_ctx_); next_ctx_ = nullptr; }
    dataset_bytes_ = 0; next_dataset_bytes_ = 0; allocated_ = 0; prog_seed_ = ~0ull;
}

void ClEngine::set_knobs(const Knobs &k) { knobs_ = k; }

uint32_t ClEngine::hashes_for_duration() const {
    uint32_t wg = knobs_.threadgroup * wave_;
    uint32_t hashes_per_wg = wg / cand_->lanes;
    uint32_t wgs = 64;
    if (last_gpu_ms_ > 0 && last_hashes_ > 0) {
        double want = (double)last_hashes_ * (double)knobs_.batch_ms / last_gpu_ms_;
        wgs = (uint32_t)(want / hashes_per_wg);
        if (wgs < 1) wgs = 1;
        if (wgs > (1u << 20)) wgs = 1u << 20;
    }
    return wgs * hashes_per_wg;
}

void ClEngine::run_job(const Job &job, const std::function<void(const Result &)> &on_result,
                       const std::function<void(const BatchStats &)> &on_progress, const std::function<bool()> &should_stop) {
    if (!cand_ || !ctx_) return;
    int st = 0;
    uint64_t job_epoch = ignpow_epoch_of(algo_.c_str(), params_c(), job.height, &st);
    if (st != IGNPOW_OK) return;
    if (job_epoch != ignpow_ctx_epoch(ctx_)) {
        if (next_ctx_ && ignpow_ctx_epoch(next_ctx_) == job_epoch) {
            ignpow_ctx_free(ctx_); ctx_ = next_ctx_; next_ctx_ = nullptr;
            clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_;
            dataset_ = next_dataset_; dataset_bytes_ = next_dataset_bytes_; next_dataset_ = nullptr; next_dataset_bytes_ = 0;
        } else {
            Refusal r; cl_mem nd = nullptr; uint64_t nb = 0; ignpow_ctx *nc = nullptr;
            clReleaseMemObject(dataset_); allocated_ -= dataset_bytes_; dataset_ = nullptr; dataset_bytes_ = 0;
            if (!build_dataset(job.height, nc, nd, nb, r)) { if (nc) ignpow_ctx_free(nc); return; }
            ignpow_ctx_free(ctx_); ctx_ = nc; dataset_ = nd; dataset_bytes_ = nb;
        }
    }
    uint64_t seed = 0; bool from_abi = false; std::string e;
    if (!ref_program_seed(*cand_, job.height, seed, from_abi)) return;
    if (!build_program(seed, e)) { fprintf(stderr, "opencl: %s\n", e.c_str()); return; }

    GpuJob base{};
    for (int i = 0; i < 8; i++) { base.header[i] = le32(job.header + 4 * i); base.target[i] = be32(job.target + 4 * i); }
    base.dag_entries = (uint32_t)(dataset_bytes_ / cand_->entry_bytes);
    base.max_results = max_results_;
    std::memcpy(base.pad_first, cand_->pad_first, sizeof base.pad_first);
    std::memcpy(base.pad_final, cand_->pad_final, sizeof base.pad_final);

    uint64_t done = 0; int slot = 0;
    cl_event inflight[2] = {nullptr, nullptr};
    uint32_t inflight_hashes[2] = {0, 0};
    uint64_t inflight_nonce[2] = {0, 0};
    double wall_start[2] = {0, 0};
    const uint32_t zero = 0;
    std::vector<std::pair<uint64_t, uint32_t>> redo;   // ranges whose results overflowed the buffer

    auto submit = [&](int s, uint64_t nonce, uint32_t hashes) -> bool {
        GpuJob gj = base;
        gj.nonce_lo = (uint32_t)nonce; gj.nonce_hi = (uint32_t)(nonce >> 32); gj.hashes = hashes;
        clEnqueueWriteBuffer(queue_, job_buf_[s], CL_TRUE, 0, sizeof gj, &gj, 0, nullptr, nullptr);
        clEnqueueWriteBuffer(queue_, count_[s], CL_TRUE, 0, 4, &zero, 0, nullptr, nullptr);
        clEnqueueWriteBuffer(queue_, fault_, CL_FALSE, 0, 4, &zero, 0, nullptr, nullptr);
        uint32_t wg = knobs_.threadgroup * wave_;
        uint32_t groups_per_wg = wg / cand_->lanes;
        size_t lmem = (cand_->cache_bytes / 4 + groups_per_wg + groups_per_wg * cand_->lanes) * 4;
        clSetKernelArg(search_k_, 0, sizeof(cl_mem), &job_buf_[s]);
        clSetKernelArg(search_k_, 1, sizeof(cl_mem), &dataset_);
        clSetKernelArg(search_k_, 2, sizeof(cl_mem), &results_[s]);
        clSetKernelArg(search_k_, 3, sizeof(cl_mem), &count_[s]);
        clSetKernelArg(search_k_, 4, sizeof(cl_mem), &fault_);
        clSetKernelArg(search_k_, 5, lmem, nullptr);
        size_t total = ((uint64_t)hashes * cand_->lanes + wg - 1) / wg * wg, local = wg;
        wall_start[s] = now_ms();
        cl_event ev = nullptr;
        if (clEnqueueNDRangeKernel(queue_, search_k_, 1, nullptr, &total, &local, 0, nullptr, &ev) != CL_SUCCESS) return false;
        clFlush(queue_);
        inflight[s] = ev; inflight_hashes[s] = hashes; inflight_nonce[s] = nonce;
        return true;
    };

    // A batch whose share count exceeds the buffer is NOT reported: its range goes to
    // `redo` and is run again in halves, so no share is ever lost.
    auto collect = [&](int s) {
        if (!inflight[s]) return;
        clWaitForEvents(1, &inflight[s]);
        cl_ulong t0 = 0, t1 = 0;
        clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_START, sizeof t0, &t0, nullptr);
        clGetEventProfilingInfo(inflight[s], CL_PROFILING_COMMAND_END, sizeof t1, &t1, nullptr);
        BatchStats stt; stt.hashes = inflight_hashes[s]; stt.gpu_ms = (double)(t1 - t0) / 1e6; stt.wall_ms = now_ms() - wall_start[s];
        last_gpu_ms_ = stt.gpu_ms; last_hashes_ = inflight_hashes[s];
        clReleaseEvent(inflight[s]); inflight[s] = nullptr;
        uint32_t f = 0, n = 0;
        clEnqueueReadBuffer(queue_, fault_, CL_TRUE, 0, 4, &f, 0, nullptr, nullptr);
        clEnqueueReadBuffer(queue_, count_[s], CL_TRUE, 0, 4, &n, 0, nullptr, nullptr);
        if (f != 0) { fprintf(stderr, "opencl: batch at nonce %llu (%u hashes) faulted (%u)\n", (unsigned long long)inflight_nonce[s], inflight_hashes[s], f); return; }
        if (n > max_results_) {
            fprintf(stderr, "opencl: %u shares in a batch of %u hashes exceed the %u-entry buffer; re-running in halves\n", n, inflight_hashes[s], max_results_);
            redo.push_back({inflight_nonce[s], inflight_hashes[s]});
            return;
        }
        std::vector<GpuResult> rr(n);
        if (n) clEnqueueReadBuffer(queue_, results_[s], CL_TRUE, 0, n * sizeof(GpuResult), rr.data(), 0, nullptr, nullptr);
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
            if (h0 <= 1) { fprintf(stderr, "opencl: a single hash overflowed the result buffer; impossible, batch dropped as a fault\n"); continue; }
            uint32_t a = h0 / 2, b = h0 - a;
            if (submit(0, n0, a)) collect(0);
            if (submit(0, n0 + a, b)) collect(0);
        }
    };

    while (done < job.nonce_count) {
        if (should_stop()) break;
        if (!redo.empty()) drain_redo();
        uint32_t want = hashes_for_duration();
        uint64_t left = job.nonce_count - done;
        uint32_t hashes = left < want ? (uint32_t)left : want;
        collect(slot);
        if (!submit(slot, job.nonce_start + done, hashes)) break;
        done += hashes; slot ^= 1;
        if (knobs_.duty < 1.0 && last_gpu_ms_ > 0) std::this_thread::sleep_for(std::chrono::microseconds((long long)(last_gpu_ms_ * (1.0 - knobs_.duty) / knobs_.duty * 1000.0)));
    }
    drain_redo();
}

}  // namespace igm
