// main.cpp: igneum-worker-opencl. GPU engineer B, 9 October 2026. Source only.
#include "cl_engine.h"
#include "cl_engine_x.h"
#include "adlx_probe.h"
#include "l0_probe.h"
#include <csignal>
#include <cstdlib>
#include "multi_engine.h"
#include "protocol.h"
#include "vectors_main.h"
#include "vectors.h"
#include "bench_main.h"
#include "ignpow.h"
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>
#ifdef __linux__
#include <unistd.h>
#include <libgen.h>
#endif

using namespace igm;

static std::string exe_dir(const char *argv0) {
#ifdef __linux__
    char buf[4096]; ssize_t n = readlink("/proc/self/exe", buf, sizeof buf - 1);
    if (n > 0) { buf[n] = 0; return std::string(dirname(buf)); }
#endif
    std::string s = argv0; size_t p = s.rfind('/');
    return p == std::string::npos ? "." : s.substr(0, p);
}

static void setenv_compat(const char *k, const char *v) {
#ifdef _WIN32
    _putenv_s(k, v);
#else
    setenv(k, v, 1);
#endif
}

namespace igm { int cl_texture_probe(const ClDeviceInfo &info); int cl_probe_main(const ClDeviceInfo &info, const std::string &kernel_dir, const std::string &which, double secs, const std::string &opts); int cl_fpcheck_main(const ClDeviceInfo &info, const std::string &kernel_dir, const std::string &path); }   // cl_probe.cpp
static int main_inner(int argc, char **argv) {
    std::string kernel_dir = exe_dir(argv[0]) + "/kernels";
    std::string probe_which, probe_opts, fpcheck_path; double probe_secs = 60;   // probe <primitive> <seconds> [stride_lines]: the escape search's primitive probe (cl_probe.cpp)
    bool texture_verb = false; bool telemetry_verb = false;
    std::vector<std::string> vectors; std::string trace_ref, trace_out, trace_prefix, dump_binary; bool boundary = false; uint32_t boundary_nonces = 16; std::string flip_ref, flip_out, kernel_commit; int flip_round = 10, flip_nonces = 64; bool probe = false, list = false; int device = 0; std::string vendor, device_name;
    uint64_t bench_height = 0; double bench_seconds = 0; bool bench = false; std::string transitions; double peak_gbs = 0.0, ceiling_gbs = 0.0; int share_bits = 32, verify_every = 0; int power_limit_pct = 0, clock_core_pct = 0, clock_mem_pct = 0, clock_core_offset = 0; bool clock_restore = false, clock_probe = false; bool facts = false; std::string algo = "kawpow-0.9.4"; Json knob_json = Json::object();
    for (int a = 1; a < argc; a++) {
        std::string s = argv[a];
        if (s == "--vectors") { while (a + 1 < argc && argv[a + 1][0] != '-') vectors.push_back(argv[++a]); }
        else if (s == "vectors" && a + 1 < argc) { std::string d = argv[++a]; for (const char *c : {"nonce-boundary", "unseen", "transitions", "malformed", "tails"}) vectors.push_back(d + "/" + c + ".json"); }
        else if (s == "dump-binary" && a + 1 < argc) { dump_binary = argv[++a]; }
        else if (s == "boundary") { boundary = true; if (a + 1 < argc && argv[a + 1][0] != '-') boundary_nonces = (uint32_t)atoi(argv[++a]); }   // the 2 GiB boundary test (the outside review's ruling): the split-aware read-back, the digest over both buffers, the traced rounds against the device   // the built device binary's raw bytes to this file (the runner's decision-lane record; the summary line's binary_blake3 is over the same bytes)
        else if (s == "trace" && a + 2 < argc) { trace_ref = argv[++a]; trace_out = argv[++a]; if (a + 1 < argc && argv[a + 1][0] != '-') trace_prefix = argv[++a]; }
        else if (s == "flip" && a + 2 < argc) { flip_ref = argv[++a]; flip_out = argv[++a]; }   // the flip trace (TP-125): <reference dir> <out.json>
        else if (s == "--flip-round" && a + 1 < argc) flip_round = atoi(argv[++a]);
        else if (s == "--flip-nonces" && a + 1 < argc) flip_nonces = atoi(argv[++a]);
        else if (s == "--kernel-commit" && a + 1 < argc) kernel_commit = argv[++a];
        else if (s == "bench" && a + 2 < argc) { bench = true; bench_height = strtoull(argv[++a], nullptr, 10); bench_seconds = atof(argv[++a]); }
            else if (s == "--transitions") transitions = "warm,cold";
            else if (s == "--peak-gbs" && a + 1 < argc) peak_gbs = atof(argv[++a]);
            else if (s == "--ceiling-gbs" && a + 1 < argc) ceiling_gbs = atof(argv[++a]);
            else if (s == "--ceiling-form" && a + 1 < argc) setenv_compat("IGM_CEILING_FORM", argv[++a]);   // A or B (the default B: the dependent chain, the outside review's rule)
            else if (s == "--independent-ceiling-gbs" && a + 1 < argc) setenv_compat("IGM_INDEPENDENT_CEILING_GBS", argv[++a]);   // form A beside the row   // the attainable ceiling for the row's request width (form A at that width, measured on this configuration)
            else if (s == "--share-bits" && a + 1 < argc) share_bits = atoi(argv[++a]);   // bench: the hit target 2^(256 - bits)
            else if (s == "--verify-every" && a + 1 < argc) verify_every = atoi(argv[++a]);   // bench: every n-th hit recomputed by the reference   // the configuration row's documented peak memory bandwidth, for the bandwidth fraction on the bench line
            else if (s == "--power-limit-pct" && a + 1 < argc) power_limit_pct = atoi(argv[++a]);   // the power-limit sweep: percent of the stock limit, set at the start, restored on every exit (ADLX manual power tuning)
            else if (s == "power-restore") { power_limit_pct = -1; }
            else if (s == "clock-restore") { clock_restore = true; }   // the factory reset of the manual tuning (every clock and power offset)
            else if (s == "clock-probe") { clock_probe = true; }   // read-only: the tuning ranges, the current maxima, the live clocks
            else if (s == "--clock" && a + 1 < argc) { std::string kv = argv[++a]; if (kv.rfind("core-offset=", 0) == 0) clock_core_offset = atoi(kv.c_str() + 12); else if (kv.rfind("core=", 0) == 0) clock_core_pct = atoi(kv.c_str() + 5); else if (kv.rfind("mem=", 0) == 0) clock_mem_pct = atoi(kv.c_str() + 4); }   // Breakwater phase C: the core or memory maximum as a percent of stock (ADLX, restored on exit)
            else if (s == "probe" && a + 1 < argc) { probe_which = argv[++a]; if (a + 1 < argc && argv[a + 1][0] != '-') probe_secs = atof(argv[++a]); while (a + 1 < argc && argv[a + 1][0] != '-' && strchr(argv[a + 1], '=')) { std::string kv = argv[++a]; probe_opts += kv + ";"; } }   // probe <prim> [seconds] [walk_bytes=.. write_pct=.. write_mode=random storm_ways=.. storm_stride=.. seed=.. ceiling_tops=.. cunits=.. rounds=..]
            else if (s == "fpcheck" && a + 1 < argc) fpcheck_path = argv[++a];   // the version 8 fp class on the device (cl_probe.cpp)
            else if (s == "facts") { facts = true; }   // the configuration row's facts: host CPU, the GPU's PCIe bus and lane width, the board name, the driver   // restore the stock value and exit (the job's restore step)
            else if (s == "--variant" && a + 1 < argc) knob_json.set("variant", Json(std::string(argv[++a])));
            else if (s == "--isa-verdict" && a + 1 < argc) knob_json.set("isa_verdict", Json(std::string(argv[++a])));   // Breakwater: the offline ISA read of this kernel commit, carried onto the row
            else if (s == "--request-ceiling-gps" && a + 1 < argc) knob_json.set("request_ceiling_gps", Json(atof(argv[++a])));   // Breakwater: the measured request ceiling (G per second) for fraction_of_request_ceiling
            else if (s == "--clopts" && a + 1 < argc) knob_json.set("clopts", Json(std::string(argv[++a])));   // OpenCL build options (a compile check, e.g. -cl-std=CL1.2)   // the tuning variant (TUNING-R0A.md), not a consensus value   // bench: three phases, a warm then a cold epoch move, each timed
        else if (s == "--algo" && a + 1 < argc) algo = argv[++a];
            else if (s == "--params" && a + 1 < argc) knob_json.set("params", Json(std::string(argv[++a])));   // the candidate's params document (JSON text)
            else if (s == "--param" && a + 1 < argc) {   // key=value, repeatable: the same document built member by member (survives argv quoting on Windows agents)
                std::string kv = argv[++a]; size_t eq = kv.find('=');
                if (eq == std::string::npos) { fprintf(stderr, "igneum-worker-opencl: --param wants key=value\n"); return 2; }
                std::string cur = knob_json.has("params") ? knob_json["params"].as_string() : std::string();
                std::string body = cur.size() > 2 ? cur.substr(1, cur.size() - 2) : std::string();
                knob_json.set("params", Json("{" + body + (body.empty() ? "" : ",") + "\"" + kv.substr(0, eq) + "\":" + kv.substr(eq + 1) + "}"));
            }
            else if (s == "--knob" && a + 1 < argc) { std::string kv = argv[++a]; size_t e = kv.find('='); if (e != std::string::npos) { std::string v = kv.substr(e + 1); char *end = nullptr; double d = strtod(v.c_str(), &end); knob_json.set(kv.substr(0, e), (end && *end == 0 && !v.empty()) ? Json(d) : Json(v)); } }   // a numeric value as a number, any other as a string (exchange=shuffle; found 10 Oct 17:0x UK: atof made every word 0 and the exchange knob never reached the engine)
        else if (s == "--kernels" && a + 1 < argc) kernel_dir = argv[++a];
        else if (s == "--device" && a + 1 < argc) device = atoi(argv[++a]);
        else if (s == "--vendor" && a + 1 < argc) vendor = argv[++a];
        else if (s == "--device-name" && a + 1 < argc) device_name = argv[++a];   // the first GPU whose name contains this text (case-insensitive); applied after --vendor   // the first GPU of this vendor (amd, intel, nvidia) or the first GPU of any vendor (first), whatever the platform order
        else if (s == "--probe" || s == "devices") probe = true;
        else if (s == "telemetry") telemetry_verb = true;
        else if (s == "texture") texture_verb = true;   // the texture determinism probe (14 Oct 2026): one JSON line, the card's digest beside the host's   // read-only, idle-safe: per device the power, temperature and clocks the vendor library reports (no kernel launched); reliability's console, 13 Oct 2026
        else if (s == "protocol") {}   // A's command word, accepted
        else if (s == "--list") list = true;
    }
    Knobs bench_knobs; bench_knobs.from_json(knob_json);
    std::vector<ClDeviceInfo> devs = enumerate_cl_devices();
    if (telemetry_verb) {   // {"telemetry": [{"index", "bus_id", "name", "vendor", "power_w", "temp_c", "core_mhz", "mem_mhz", ...}]}: the vendor library's readings (Level Zero sysman on Intel, ADLX on AMD under Windows, the amdgpu hwmon on Linux), absent fields omitted
        Json out = Json::array();
        for (size_t i = 0; i < devs.size(); i++) {
            Json t = Json::object().set("index", (double)i).set("bus_id", devs[i].pci).set("name", devs[i].name).set("vendor", devs[i].vendor);
#ifdef __linux__
            if (!devs[i].hwmon.empty()) { double v = 0; if (cl_sysfs_num(devs[i].hwmon + "/temp1_input", v)) t.set("temp_c", v / 1000.0); if (cl_sysfs_num(devs[i].hwmon + "/power1_average", v) || cl_sysfs_num(devs[i].hwmon + "/power1_input", v)) t.set("power_w", v / 1e6); if (cl_sysfs_num(devs[i].hwmon + "/freq1_input", v)) t.set("core_mhz", v / 1e6); if (cl_sysfs_num(devs[i].hwmon + "/freq2_input", v)) t.set("mem_mhz", v / 1e6); }
#endif
            cl_adlx_telemetry(devs[i], t); igm_adlx_close();
            cl_intel_telemetry(devs[i], t); igm_l0_close();
            out.push(t);
        }
        printf("%s\n", Json::object().set("telemetry", out).dump().c_str()); fflush(stdout); return 0;
    }
    if (list) {
        for (size_t i = 0; i < devs.size(); i++)
            fprintf(stderr, "%zu: %s | %s | %s | %s | %llu MB | subgroups %d shuffle %d\n", i, devs[i].platform_name.c_str(), devs[i].name.c_str(), devs[i].vendor.c_str(),
                    devs[i].driver.c_str(), (unsigned long long)(devs[i].global_mem >> 20), devs[i].subgroups, devs[i].subgroup_shuffle);
        return 0;
    }
    if (!vendor.empty()) {
        device = -1;
        for (size_t i = 0; i < devs.size(); i++) {
            std::string v = devs[i].vendor; for (auto &c : v) c = (char)tolower(c);
            bool gpu = devs[i].type == CL_DEVICE_TYPE_GPU;
            bool match = vendor == "first" || (vendor == "amd" && (v.find("advanced micro") != std::string::npos || v.find("amd") != std::string::npos)) || (vendor == "nvidia" && v.find("nvidia") != std::string::npos) || (vendor == "intel" && v.find("intel") != std::string::npos);
            if (gpu && match) { device = (int)i; break; }
        }
        if (device < 0) { fprintf(stderr, "igneum-worker-opencl: no %s GPU among %zu OpenCL devices\n", vendor.c_str(), devs.size()); return 1; }
    }
    if (!device_name.empty()) {
        std::string want = device_name; for (auto &c : want) c = (char)tolower(c);
        device = -1;
        for (size_t i = 0; i < devs.size(); i++) {
            std::string n = devs[i].name + " | " + devs[i].board; for (auto &c : n) c = (char)tolower(c);   // AMD reports the gfx code as the name and the product as the board name
            std::string v = devs[i].vendor; for (auto &c : v) c = (char)tolower(c);
            bool vend = vendor.empty() || vendor == "first" || (vendor == "amd" && (v.find("advanced micro") != std::string::npos || v.find("amd") != std::string::npos)) || (vendor == "nvidia" && v.find("nvidia") != std::string::npos) || (vendor == "intel" && v.find("intel") != std::string::npos);
            if (devs[i].type == CL_DEVICE_TYPE_GPU && vend && n.find(want) != std::string::npos) { device = (int)i; break; }
        }
        if (device < 0) { fprintf(stderr, "igneum-worker-opencl: no GPU named like \"%s\" among %zu OpenCL devices\n", device_name.c_str(), devs.size()); return 1; }
    }
    if (devs.empty() || device < 0 || (size_t)device >= devs.size()) { fprintf(stderr, "igneum-worker-opencl: no OpenCL device %d (%zu found)\n", device, devs.size()); return 1; }
    ClEngine eng_p(devs[device], device, kernel_dir);
    ClEngineX eng_x(devs[device], device, kernel_dir);
    MultiEngine eng(&eng_p, &eng_x, "opencl");
    if (!vectors.empty()) { g_vector_knobs = bench_knobs; return vectors_main(eng, vectors); }
    if (boundary) { g_vector_knobs = bench_knobs; return boundary_check(eng, bench_height ? bench_height : 1, boundary_nonces) == 0 ? 0 : 1; }
    if (!dump_binary.empty()) {
        Refusal r; Knobs k = bench_knobs;
        if (!eng.open(algo, bench_height ? bench_height : 1, k, 0, r)) { fprintf(stderr, "dump-binary: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str()); return 1; }
        std::vector<unsigned char> bin; bool ok = eng.program_binary(bin);
        Json code = eng.code_json(); eng.close();
        if (!ok) { fprintf(stderr, "dump-binary: the runtime returned no program binary\n"); return 1; }
        FILE *f = fopen(dump_binary.c_str(), "wb"); if (!f) { fprintf(stderr, "dump-binary: cannot write %s\n", dump_binary.c_str()); return 1; }
        fwrite(bin.data(), 1, bin.size(), f); fclose(f);
        printf("{\"dump_binary\": {\"file\": \"%s\", \"bytes\": %zu, \"binary_blake3\": %s}}\n", dump_binary.c_str(), bin.size(), code.has("binary_blake3") ? ("\"" + code["binary_blake3"].as_string() + "\"").c_str() : "null");
        return 0;
    }
    if (!flip_ref.empty()) { g_vector_knobs = bench_knobs; int n = write_flip_trace(eng, flip_ref, flip_out, (uint32_t)flip_round, (uint32_t)flip_nonces, kernel_commit); return n > 0 ? 0 : 1; }
    if (!trace_ref.empty()) { g_vector_knobs = bench_knobs; int n = write_identity_set(eng, trace_ref, trace_out, trace_prefix); return n == 5 ? 0 : 1; }   // the identity set (strata/RIPTIDE.md 2)
    if (facts) {
        igm_adlx_open(devs[device].board.empty() ? devs[device].name.c_str() : devs[device].board.c_str());
        int bt = 0, lw = 0; int have = igm_adlx_pcie(&bt, &lw); int hd = igm_adlx_has_desktops();
        printf("{\"facts\": {\"host_cpu\": \"%s\", \"gpu_name\": \"%s\", \"board\": \"%s\", \"driver\": \"%s\", \"platform\": \"%s\", \"adlx\": \"%s\", \"adlx_gpu\": \"%s\", \"pcie_bus_type\": %d, \"pcie_lane_width\": %d, \"pcie_known\": %s, \"drives_desktop\": \"%s\", \"global_mem\": \"%llu\", \"max_alloc\": \"%llu\"}}\n",
               igm_host_cpu_name(), devs[device].name.c_str(), devs[device].board.c_str(), devs[device].driver.c_str(), devs[device].platform_name.c_str(), igm_adlx_state(), igm_adlx_name(), bt, lw, have ? "true" : "false", hd == 1 ? "yes" : hd == 0 ? "no" : "not known",
               (unsigned long long)devs[device].global_mem, (unsigned long long)devs[device].max_alloc);
        return 0;
    }
    if (clock_restore || clock_probe) {
        igm_adlx_open(devs[device].board.empty() ? devs[device].name.c_str() : devs[device].board.c_str());
        char pb[1024]; pb[0] = 0;
        if (clock_restore) { const char *why = ""; int ok = igm_adlx_reset_to_factory(&why); printf("{\"clock_restore\": {\"applied\": %s, \"why\": \"%s\"}}\n", ok ? "true" : "false", why); if (!ok) return 5; }
        if (igm_adlx_clock_probe(pb, sizeof pb)) printf("%s\n", pb); else printf("{\"clock_probe\": {\"note\": \"not exposed (no ADLX)\"}}\n");
        fflush(stdout); return 0;
    }
    if (clock_core_pct != 0 || clock_mem_pct != 0 || clock_core_offset != 0) {
        igm_adlx_open(devs[device].board.empty() ? devs[device].name.c_str() : devs[device].board.c_str());
        if (clock_core_offset != 0) {   // the signed-offset form (RDNA4): a negative MHz around the stock boost, read back, restored on exit
            int ap = 0; const char *why = "";
            if (!igm_adlx_core_offset_set(clock_core_offset, &ap, &why)) {
                fprintf(stderr, "igneum-worker-opencl: core offset %d MHz refused: %s\n", clock_core_offset, why);
                printf("{\"clock\": {\"which\": \"core\", \"requested_offset_mhz\": %d, \"applied\": false, \"why\": \"%s\"}}\n", clock_core_offset, why);
                igm_adlx_clocks_restore(); return 5;
            }
            printf("{\"clock\": {\"which\": \"core\", \"requested_offset_mhz\": %d, \"applied\": true, \"offset_mhz\": %d}}\n", clock_core_offset, ap); fflush(stdout);
        }
        for (int which = 0; which < 2; which++) {
            int pct = which == 0 ? clock_core_pct : clock_mem_pct; if (pct == 0) continue;
            int ap = 0, cur = 0, stock = 0; const char *why = "";
            bool ok = which == 0 ? igm_adlx_core_max_set(pct, &ap, &why) != 0 : igm_adlx_mem_max_set(pct, &ap, &why) != 0;
            if (which == 0) igm_adlx_core_max_get(&cur, &stock); else igm_adlx_mem_max_get(&cur, &stock);
            if (!ok) {
                fprintf(stderr, "igneum-worker-opencl: %s clock %d percent of stock refused: %s\n", which == 0 ? "core" : "memory", pct, why);
                printf("{\"clock\": {\"which\": \"%s\", \"requested_pct\": %d, \"applied\": false, \"why\": \"%s\", \"stock_mhz\": %d}}\n", which == 0 ? "core" : "mem", pct, why, stock);
                igm_adlx_clocks_restore(); return 5;
            }
            printf("{\"clock\": {\"which\": \"%s\", \"requested_pct\": %d, \"applied\": true, \"max_mhz\": %d, \"stock_mhz\": %d}}\n", which == 0 ? "core" : "mem", pct, ap, stock); fflush(stdout);
        }
        std::atexit(igm_adlx_clocks_restore);
        std::signal(SIGINT, [](int) { igm_adlx_clocks_restore(); igm_adlx_power_limit_restore(); std::_Exit(130); });
        std::signal(SIGTERM, [](int) { igm_adlx_clocks_restore(); igm_adlx_power_limit_restore(); std::_Exit(143); });
    }
    if (power_limit_pct != 0) {
        igm_adlx_open(devs[device].board.empty() ? devs[device].name.c_str() : devs[device].board.c_str());
        if (power_limit_pct == -1) { int off = 0; igm_adlx_power_limit_get(&off); { int ap = 0; const char *w = ""; igm_adlx_power_limit_set(100, &ap, &w); } igm_adlx_power_limit_get(&off); fprintf(stderr, "adlx: power limit offset now %d\n", off); printf("{\"power_restore\": {\"offset_pct\": %d}}\n", off); return off == 0 ? 0 : 5; }
        int mn = 0, mx = 0, st = 0, ap = 0; const char *why = "";
        bool have = igm_adlx_power_limit_range(&mn, &mx, &st) != 0;
        if (!igm_adlx_power_limit_set(power_limit_pct, &ap, &why)) {
            fprintf(stderr, "igneum-worker-opencl: power limit %d percent of stock refused: %s (range %d..%d step %d)\n", power_limit_pct, why, mn, mx, st);
            printf("{\"power_limit\": {\"requested_pct\": %d, \"applied\": false, \"why\": \"%s\", \"range_min\": %d, \"range_max\": %d, \"step\": %d}}\n", power_limit_pct, why, mn, mx, st);
            return 5;
        }
        printf("{\"power_limit\": {\"requested_pct\": %d, \"applied\": true, \"offset_pct\": %d, \"range_min\": %d, \"range_max\": %d, \"step\": %d}}\n", power_limit_pct, ap, mn, mx, st); fflush(stdout);
        std::atexit(igm_adlx_power_limit_restore);
        std::signal(SIGINT, [](int) { igm_adlx_power_limit_restore(); std::_Exit(130); });
        std::signal(SIGTERM, [](int) { igm_adlx_power_limit_restore(); std::_Exit(143); });
        (void)have;
    }
    if (!fpcheck_path.empty()) { int rc = igm::cl_fpcheck_main(devs[device], kernel_dir, fpcheck_path); fflush(stdout); std::_Exit(rc); }
    if (texture_verb) { int rc = igm::cl_texture_probe(devs[device]); fflush(stdout); std::_Exit(rc); }
    if (!probe_which.empty()) { int rc = igm::cl_probe_main(devs[device], kernel_dir, probe_which, probe_secs, probe_opts); fflush(stdout); igm_adlx_power_limit_restore(); std::_Exit(rc); }   // the probe leaves the way the bench does (the exit path hang)
    if (bench) return bench_main(eng, algo, bench_height, bench_seconds, "opencl; telemetry from the amdgpu hwmon node where present", bench_knobs, transitions, peak_gbs, ceiling_gbs, share_bits, verify_every);
    size_t n = 0; bool vectors_ok = ignpow_vectors_check(nullptr, &n) == IGNPOW_OK;   // the start-up self-test the hello line reports
    eng.set_knobs(bench_knobs);
    return run_worker_loop(eng, "igneum-worker-opencl", "0.0.1", algo, vectors_ok, probe);
}

// The process leaves through _Exit after its own work is flushed and the ADLX power limit restored: on the owned bench PC under the
// AMD runtime (10 Oct 2026, worker 3ec25e5e) the worker printed its whole output and then hung in the exit path (the
// static destructors and the ICD teardown), and every job timed out on it; the GPU memory and the OpenCL objects are
// released by the process end on Windows and Linux alike. The bisect of the teardown hang is a separate item.

int main(int argc, char **argv) {
    int rc = main_inner(argc, argv);
    fflush(stdout); fflush(stderr);
    igm_adlx_power_limit_restore();
    fflush(stderr);
    std::_Exit(rc);
}
