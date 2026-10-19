// epochs.cpp: see epochs.h. The Apple memory seat, 10 October 2026.
#include "epochs.h"
#include "ignpow.h"
#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <string>
#include <vector>

namespace igm {
namespace {
double now_s() { using namespace std::chrono; return duration<double>(steady_clock::now().time_since_epoch()).count(); }
std::string utc_now() { char b[32]; time_t t = time(nullptr); struct tm g; gmtime_r(&t, &g); strftime(b, sizeof b, "%Y-%m-%dT%H:%M:%SZ", &g); return b; }
std::string esc(const std::string &s) { std::string o; for (char c : s) { if (c == '"' || c == '\\') o += '\\'; if (c == '\n') { o += "\\n"; continue; } o += c; } return o; }
int thermal_rank(const std::string &t) { return t == "nominal" ? 0 : t == "fair" ? 1 : t == "serious" ? 2 : t == "critical" ? 3 : 0; }
const char *thermal_name(int r) { return r == 0 ? "nominal" : r == 1 ? "fair" : r == 2 ? "serious" : "critical"; }

struct MemSample { double footprint = 0, resident = 0, faults = 0, pageins = 0, pageouts = 0, compressions = 0, decompressions = 0, swapouts = 0, compressor = 0; bool have = false; };
MemSample mem_of(const Json &t) {
    MemSample m; if (!t.has("phys_footprint_bytes")) return m; m.have = true;
    auto g = [&](const char *k) { return t.has(k) ? t[k].as_number() : 0.0; };
    m.footprint = g("phys_footprint_bytes"); m.resident = g("resident_bytes"); m.faults = g("vm_faults"); m.pageins = g("vm_pageins"); m.pageouts = g("vm_pageouts");
    m.compressions = g("vm_compressions"); m.decompressions = g("vm_decompressions"); m.swapouts = g("vm_swapouts"); m.compressor = g("compressor_bytes");
    return m;
}
}  // namespace

int epochs_main(EngineIface &eng, const std::string &algo, uint64_t height, uint32_t transitions, bool cold, double epoch_s, const std::string &notes, const Knobs &knobs, int share_bits, int verify_every) {
    int st = 0;
    const char *pj = knobs.params.empty() ? nullptr : knobs.params.c_str();
    auto next_epoch_height = [&](uint64_t from) { int s0 = 0; uint64_t e0 = ignpow_epoch_of(algo.c_str(), pj, from, &s0), h = from; if (s0 != IGNPOW_OK) return (uint64_t)0; for (uint64_t i = 0; i < (1ull << 22); i++) { h++; int s2 = 0; uint64_t e = ignpow_epoch_of(algo.c_str(), pj, h, &s2); if (s2 != IGNPOW_OK) return (uint64_t)0; if (e != e0) return h; } return (uint64_t)0; };
    Knobs k = knobs; Refusal r;
    Json dev = eng.device_json();
    double t0 = now_s();
    if (!eng.open(algo, height, k, 0, r)) { printf("{\"refuse\": \"%s: %s\"}\n", r.code.c_str(), esc(r.explain).c_str()); fflush(stdout); return 4; }
    double open_ms = (now_s() - t0) * 1000.0;
    Json b1 = eng.budget_json();
    printf("{\"prepared\": {\"epoch\": %llu, \"ms\": %.0f, \"bytes\": \"%llu\", \"free_after\": \"%s\", \"mode\": \"cold\", \"form\": \"%s\", \"budget\": %s}}\n",
           (unsigned long long)ignpow_epoch_of(algo.c_str(), pj, height, &st), open_ms, (unsigned long long)eng.allocated_bytes(), b1["free_bytes"].as_string().c_str(), cold ? "cold single-buffer" : "warm double-buffered", b1.dump().c_str());
    fflush(stdout);
    const double start = now_s();
    uint64_t h = height; size_t hits = 0, verified = 0, failed = 0; uint64_t total_hashes = 0;
    double last = start; uint64_t since = 0; int worst_thermal_all = 0;
    MemSample mem_first, mem_last; double peak_footprint = 0;
    // one mining window at a height: returns the hashes and the seconds, calls tick each batch, stops on the predicate
    auto make_job = [&](uint64_t hh, const char *id) { Job j; j.job_id = id; j.height = hh; for (int i = 0; i < 32; i++) j.header[i] = (uint8_t)(0xA5 ^ (i * 7) ^ (hh >> (i % 8))); std::memset(j.target, 0, 32); int bits = share_bits < 1 ? 1 : share_bits > 255 ? 255 : share_bits; int bit = 256 - bits; j.target[31 - bit / 8] = (uint8_t)(1u << (bit % 8)); j.nonce_start = (uint64_t)hh << 32; j.nonce_count = ~0ull; return j; };
    int worst_thermal = 0;
    auto sample = [&](uint64_t hh, const char *phase) {
        double now = now_s();
        if (now - last >= 1.0) {
            Json t = Json::object(); eng.telemetry(t);
            MemSample m = mem_of(t); if (m.have) { mem_last = m; if (!mem_first.have) mem_first = m; if (m.footprint > peak_footprint) peak_footprint = m.footprint; }
            if (t.has("thermal_pressure")) { int tr = thermal_rank(t["thermal_pressure"].as_string()); if (tr > worst_thermal) worst_thermal = tr; if (tr > worst_thermal_all) worst_thermal_all = tr; }
            printf("{\"t\": %.1f, \"mhs\": %.2f, \"hits\": %zu, \"height\": %llu, \"phase\": \"%s\", \"telemetry\": %s}\n", now - start, since / (now - last) / 1e6, hits, (unsigned long long)hh, phase, t.dump().c_str());
            fflush(stdout); since = 0; last = now;
        }
    };
    auto mine = [&](uint64_t hh, const char *phase, const std::function<bool()> &stop, double &first_batch_ms, uint64_t &hashes, double &secs, ignpow_ctx *vctx) {
        Job j = make_job(hh, phase); double w0 = now_s(); bool first = true; hashes = 0; first_batch_ms = -1;
        eng.run_job(j, [&](const Result &res) { hits++; if (vctx && verify_every > 0 && (hits % (size_t)verify_every) == 0) { uint8_t m[32], d[32]; int vs = ignpow_hash(vctx, hh, j.header, res.nonce, m, d); if (vs == IGNPOW_OK && std::memcmp(d, res.digest, 32) == 0 && std::memcmp(m, res.mix, 32) == 0) verified++; else failed++; } },
                    [&](const BatchStats &s) { double now = now_s(); if (first) { first = false; first_batch_ms = (now - w0) * 1000.0; } hashes += s.hashes; since += s.hashes; total_hashes += s.hashes; sample(hh, phase); }, stop);
        secs = now_s() - w0;
        if (first) first_batch_ms = secs * 1000.0;   // no batch at all: the whole window was downtime
    };
    std::string lines; uint32_t done = 0;
    double sum_build_ms = 0, sum_lost = 0, sum_downtime_ms = 0, sum_steady = 0, sum_building = 0, sum_after = 0, max_switch_ms = 0, sum_switch_ms = 0, sum_swap_wait = 0, sum_steady_work = 0; uint64_t refused = 0;
    const double settle_s = epoch_s * 0.3, post_s = epoch_s * 0.3;
    for (uint32_t n = 0; n < transitions; n++) {
        uint64_t hn = next_epoch_height(h);
        if (!hn) { fprintf(stderr, "epochs: no next epoch after height %llu in the schedule\n", (unsigned long long)h); break; }
        worst_thermal = 0; MemSample m0 = mem_last; const std::string utc0 = utc_now(); const double tr0 = now_s();
        ignpow_ctx *vctx = nullptr; if (verify_every > 0) { int vst = 0; vctx = ignpow_ctx_create(algo.c_str(), pj, h, 0, &vst); }
        // the steady window at the resident epoch
        double fb = 0, secs = 0; uint64_t hs = 0; double w0 = now_s();
        mine(h, "steady", [&] { return now_s() - w0 >= settle_s; }, fb, hs, secs, vctx);
        const double steady_mhs = secs > 0 ? hs / secs / 1e6 : 0;
        // the build beside the mining (warm) or nothing (cold)
        double prep_ms = 0, build_ms = 0, building_mhs = 0, after_mhs = 0; bool prepared = false; std::string prep_note;
        if (!cold) {
            double p0 = now_s(); Refusal pr; prepared = eng.prepare(hn, pr); prep_ms = (now_s() - p0) * 1000.0;
            if (!prepared) { prep_note = pr.code + ": " + pr.explain; refused++; }
            else {
                double b0 = now_s(); bool ready = false; double ready_at = 0; uint64_t hb = 0; double tb = 0;
                Job j = make_job(h, "building"); double since_b = 0; (void)since_b;
                eng.run_job(j, [&](const Result &) { hits++; }, [&](const BatchStats &s2) { double now = now_s(); hb += s2.hashes; since += s2.hashes; total_hashes += s2.hashes; tb = now - b0; if (!ready) { ready = eng.prepare_ready(); if (ready) ready_at = now; } sample(h, "building"); },
                            [&] { return eng.prepare_ready() || now_s() - b0 >= epoch_s; });
                if (!ready) { ready = eng.prepare_ready(); ready_at = now_s(); }
                build_ms = (ready_at - b0) * 1000.0; building_mhs = tb > 0 ? hb / tb / 1e6 : 0;
                double a0 = now_s(); uint64_t ha = 0; double ta = 0, fba = 0;
                mine(h, "after-build", [&] { return now_s() - a0 >= post_s; }, fba, ha, ta, vctx);
                after_mhs = ta > 0 ? ha / ta / 1e6 : 0;
            }
        }
        if (vctx) { ignpow_ctx_free(vctx); vctx = nullptr; }
        // the move: the job at the next epoch; the first batch after it is the switch latency (the cold rebuild inside it)
        if (verify_every > 0) { int vst = 0; vctx = ignpow_ctx_create(algo.c_str(), pj, hn, 0, &vst); }
        double sw0 = now_s(), first_ms = 0, ssecs = 0; uint64_t shs = 0;
        mine(hn, "switched", [&] { return now_s() - sw0 >= post_s; }, first_ms, shs, ssecs, vctx);
        if (vctx) ignpow_ctx_free(vctx);
        const double switched_mhs = ssecs > 0 ? shs / ssecs / 1e6 : 0;
        const double swap_wait = eng.last_swap_wait_ms();
        const double lost = steady_mhs * 1e6 * first_ms / 1000.0;   // the hashes the steady rate would have given over the switch gap
        const double downtime_ms = first_ms;
        const double tr_s = now_s() - tr0;
        MemSample m1 = mem_last; const bool mem_ok = m0.have && m1.have;
        Json g = Json::object(); const bool have_guard = eng.guard_status(g);
        char line[2048];
        snprintf(line, sizeof line, "{\"n\": %u, \"from_height\": %llu, \"to_height\": %llu, \"form\": \"%s\", \"utc_start\": \"%s\", \"utc_end\": \"%s\", \"seconds\": %.1f, \"steady_mhs\": %.3f, \"prepared\": %s, \"prepare_enqueue_ms\": %.0f, \"build_ms\": %.0f, \"rate_while_building_mhs\": %.3f, \"rate_after_build_mhs\": %.3f, \"rate_after_switch_mhs\": %.3f, \"retained_during_build\": %.4f, \"swap_wait_ms\": %.0f, \"job_switch_first_batch_ms\": %.0f, \"downtime_ms\": %.0f, \"lost_hashes_at_swap\": %.0f, \"peak_phys_footprint_bytes\": %.0f, \"phys_footprint_end_bytes\": %.0f, \"vm_faults\": %.0f, \"vm_pageins\": %.0f, \"vm_pageouts\": %.0f, \"vm_compressions\": %.0f, \"vm_decompressions\": %.0f, \"vm_swapouts\": %.0f, \"compressor_bytes_end\": %.0f, \"memory_counters\": \"%s\", \"worst_thermal\": \"%s\", \"guard_intact\": %s, \"energy\": \"not measured: no power reading on this backend; a wall meter's samples slice onto utc_start and utc_end\", \"note\": \"%s\"}",
                 n + 1, (unsigned long long)h, (unsigned long long)hn, cold ? "cold" : "warm", utc0.c_str(), utc_now().c_str(), tr_s, steady_mhs, prepared ? "true" : "false", prep_ms, build_ms, building_mhs, after_mhs, switched_mhs,
                 steady_mhs > 0 ? building_mhs / steady_mhs : 0.0, swap_wait, first_ms, downtime_ms, lost, peak_footprint, m1.footprint,
                 mem_ok ? m1.faults - m0.faults : -1.0, mem_ok ? m1.pageins - m0.pageins : -1.0, mem_ok ? m1.pageouts - m0.pageouts : -1.0, mem_ok ? m1.compressions - m0.compressions : -1.0, mem_ok ? m1.decompressions - m0.decompressions : -1.0, mem_ok ? m1.swapouts - m0.swapouts : -1.0, m1.compressor,
                 mem_ok ? "host-wide deltas over the transition (the process's footprint is its own)" : "not exposed by this engine", thermal_name(worst_thermal), have_guard ? (g["intact"].as_bool() ? "true" : "false") : "null", esc(prep_note).c_str());
        printf("{\"epoch_transition\": %s}\n", line); fflush(stdout);
        lines += (lines.empty() ? "" : ", ") + std::string(line);
        sum_build_ms += build_ms; sum_lost += lost; sum_downtime_ms += downtime_ms; sum_steady += steady_mhs; sum_building += building_mhs; sum_after += after_mhs; sum_switch_ms += first_ms; if (first_ms > max_switch_ms) max_switch_ms = first_ms; sum_swap_wait += swap_wait;
        sum_steady_work += steady_mhs * 1e6 * tr_s;
        done++; h = hn;
    }
    const double el = now_s() - start;
    // the overhead the gate reads: the work the steady rate would have given over the campaign, less the work done, over the former
    const double overhead = sum_steady_work > 0 ? (sum_steady_work - (double)total_hashes) / sum_steady_work : 0.0;
    Json code = eng.code_json();
    Json g = Json::object(); const bool have_guard = eng.guard_status(g);
    printf("{\"epochs\": {\"device\": \"%s\", \"arch\": \"%s\", \"form\": \"%s\", \"transitions\": %u, \"refused_prepares\": %llu, \"seconds\": %.1f, \"epoch_window_s\": %.0f, \"hashes\": \"%llu\", \"mean_mhs\": %.3f, \"hits\": %zu, \"verified_sample\": %zu, \"failed_sample\": %zu, \"mean_build_ms\": %.0f, \"mean_steady_mhs\": %.3f, \"mean_rate_while_building_mhs\": %.3f, \"mean_rate_after_build_mhs\": %.3f, \"retained_during_build\": %.4f, \"total_lost_hashes_at_swap\": %.0f, \"total_downtime_ms\": %.0f, \"mean_job_switch_ms\": %.0f, \"max_job_switch_ms\": %.0f, \"mean_swap_wait_ms\": %.0f, \"epoch_overhead_fraction\": %.5f, \"peak_phys_footprint_bytes\": %.0f, \"vm_pageouts_total\": %.0f, \"vm_swapouts_total\": %.0f, \"vm_compressions_total\": %.0f, \"worst_thermal\": \"%s\", \"guard_test\": %s, \"energy\": \"not measured: no power reading on this backend and no meter on this machine; full-epoch joules per accepted work wait for the plug\", \"describe\": \"%s\", \"knobs\": %s, \"notes\": \"%s\", \"code\": %s, \"transitions_detail\": [%s]}}\n",
           esc(dev["name"].as_string()).c_str(), esc(dev["compute"].as_string()).c_str(), cold ? "cold single-buffer" : "warm double-buffered", done, (unsigned long long)refused, el, epoch_s, (unsigned long long)total_hashes, el > 0 ? total_hashes / el / 1e6 : 0.0, hits, verified, failed,
           done ? sum_build_ms / done : 0.0, done ? sum_steady / done : 0.0, done ? sum_building / done : 0.0, done ? sum_after / done : 0.0, sum_steady > 0 ? sum_building / sum_steady : 0.0, sum_lost, sum_downtime_ms, done ? sum_switch_ms / done : 0.0, max_switch_ms, done ? sum_swap_wait / done : 0.0, overhead,
           peak_footprint, mem_first.have ? mem_last.pageouts - mem_first.pageouts : -1.0, mem_first.have ? mem_last.swapouts - mem_first.swapouts : -1.0, mem_first.have ? mem_last.compressions - mem_first.compressions : -1.0, thermal_name(worst_thermal_all),
           have_guard ? g.dump().c_str() : "null", esc(eng.describe()).c_str(), eng.knobs().to_json().dump().c_str(), esc(notes + (failed ? "; FAILED verification in the sample: this row is invalid" : "")).c_str(), code.dump().c_str(), lines.c_str());
    fflush(stdout);
    eng.close();
    return failed ? 5 : 0;
}

}  // namespace igm
