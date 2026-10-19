// bench_main.cpp: see bench_main.h.
#include "bench_main.h"
#include "ignpow.h"
#include <chrono>
#include <cstdio>
#include <cstring>
#include <cmath>
#include <vector>
#include <utility>

namespace igm {

namespace {
double now_s() { using namespace std::chrono; return duration<double>(steady_clock::now().time_since_epoch()).count(); }
std::string esc(const std::string &s) { std::string o; for (char c : s) { if (c == '"' || c == '\\') o.push_back('\\'); if (c == '\n') o += "\\n"; else o.push_back(c); } return o; }
}

int bench_main(EngineIface &eng, const std::string &algo, uint64_t height, double seconds, const std::string &notes, const Knobs &knobs, const std::string &transitions, double peak_gbs, double ceiling_gbs, int share_bits, int verify_every, double release_after_s) {
    int st = 0;
    const char *pj = knobs.params.empty() ? nullptr : knobs.params.c_str();
    uint64_t dataset = ignpow_dataset_bytes(algo.c_str(), pj, height, &st);
    uint64_t cache = ignpow_cache_bytes(algo.c_str(), pj, height, &st);
    Json dev = eng.device_json();
    Json b0 = eng.budget_json();
    // A's budget line: total, free_before, cache, dataset, scratch, margin, fit_cold, fit_warm, can_cold, can_warm
    uint64_t total = strtoull(b0["total_bytes"].as_string().c_str(), nullptr, 10);
    uint64_t free_before = strtoull(b0["free_bytes"].as_string().c_str(), nullptr, 10);
    uint64_t scratch = 2 * (4096 + 1024 * 72) + (64ull << 20);
    uint64_t margin = b0.has("headroom_bytes") ? strtoull(b0["headroom_bytes"].as_string().c_str(), nullptr, 10) : (256ull << 20);
    uint64_t fit_cold = dataset + cache + scratch, fit_warm = 2 * (dataset + cache) + scratch;
    uint64_t limit = b0.has("working_set_limit_bytes") ? strtoull(b0["working_set_limit_bytes"].as_string().c_str(), nullptr, 10) : (free_before > margin ? free_before - margin : 0);
    printf("{\"budget\": {\"total\": \"%llu\", \"free_before\": \"%llu\", \"cache\": \"%llu\", \"dataset\": \"%llu\", \"scratch\": \"%llu\", \"margin\": \"%llu\", \"fit_cold\": \"%llu\", \"fit_warm\": \"%llu\", \"can_cold\": %s, \"can_warm\": %s, \"limit\": \"%llu\"}}\n",
           (unsigned long long)total, (unsigned long long)free_before, (unsigned long long)cache, (unsigned long long)dataset, (unsigned long long)scratch, (unsigned long long)margin,
           (unsigned long long)fit_cold, (unsigned long long)fit_warm, fit_cold <= limit ? "true" : "false", fit_warm <= limit ? "true" : "false", (unsigned long long)limit);
    fflush(stdout);

    Knobs k = knobs; Refusal r;
    double w_before = 0; { Json t = Json::object(); eng.telemetry(t); if (t.has("power_w")) w_before = t["power_w"].as_number(); }
    double t0 = now_s();
    if (!eng.open(algo, height, k, 0, r)) { printf("{\"refuse\": \"%s: %s\"}\n", r.code.c_str(), esc(r.explain).c_str()); fflush(stdout); return 4; }
    double prep_ms = (now_s() - t0) * 1000.0;
    double w_after = 0; { Json t = Json::object(); eng.telemetry(t); if (t.has("power_w")) w_after = t["power_w"].as_number(); }
    double build_wh = (w_before > 0 || w_after > 0) ? ((w_before + w_after) / 2.0) * (prep_ms / 1000.0) / 3600.0 : 0.0;
    Json b1 = eng.budget_json();
    printf("{\"prepared\": {\"epoch\": %llu, \"ms\": %.0f, \"bytes\": \"%llu\", \"free_after\": \"%s\", \"mode\": \"cold\", \"build_wh\": %.4f, \"build_w_before\": %.0f, \"build_w_after\": %.0f, \"build_energy_note\": \"%s\"}}\n",
           (unsigned long long)ignpow_epoch_of(algo.c_str(), pj, height, &st), prep_ms, (unsigned long long)eng.allocated_bytes(), b1["free_bytes"].as_string().c_str(), build_wh, w_before, w_after,
           (w_before > 0 || w_after > 0) ? "the board power sampled at the build's start and end, their mean over the build time (telemetry grade)" : "no power reading on this backend");
    fflush(stdout);

    // the run: one job over a huge range, a loose target so hits are counted but rare; with transitions, three
    // phases at three heights, the epoch moves timed (downtime = from the phase's start to its first batch)
    uint64_t hashes = 0, since = 0; size_t hits = 0, verified = 0, failed = 0; double last = now_s(), start = last, wh = 0.0, last_w = 0.0;
    bool have_power = false; std::string power_source;
    std::string transitions_json;   // the summary's transitions array
    std::vector<std::pair<uint64_t, std::string>> phases;   // height, kind
    phases.push_back({height, "resident"});
    if (!transitions.empty()) {
        uint64_t ep = ignpow_epoch_of(algo.c_str(), pj, height, &st);
        auto next_epoch_height = [&](uint64_t from) { uint64_t e0 = ignpow_epoch_of(algo.c_str(), pj, from, &st), h = from; for (uint64_t i = 0; i < (1ull << 22); i++) { h++; int s2 = 0; uint64_t e = ignpow_epoch_of(algo.c_str(), pj, h, &s2); if (s2 != IGNPOW_OK) return (uint64_t)0; if (e != e0) return h; } return (uint64_t)0; };
        uint64_t h1 = next_epoch_height(height), h2 = h1 ? next_epoch_height(h1) : 0;
        if (h1) phases.push_back({h1, "warm"});
        if (h2) phases.push_back({h2, "cold"});
        (void)ep;
    }
    double phase_s = seconds / (double)phases.size();
    for (size_t pi = 0; pi < phases.size(); pi++) {
        uint64_t h = phases[pi].first; const std::string &kind = phases[pi].second;
        double tr_prep_ms = 0; bool prepared = false; std::string prep_note;
        double overlap_ms = 0, rate_while_building = 0, rate_after_build = 0, first_batch_ms = -1; uint64_t hb = 0, ha = 0; double tb = 0, ta = 0;
        if (kind == "warm") {   // prepare the next epoch beside the resident one on the engine's second queue, then keep mining the
            // current epoch for half the phase: the rate while the build runs and after it is ready are the project's epoch-overlap row
            double p0 = now_s(); Refusal pr;
            prepared = eng.prepare(h, pr); tr_prep_ms = (now_s() - p0) * 1000.0;
            if (!prepared) prep_note = pr.code + ": " + pr.explain;
            uint64_t hprev = phases[pi - 1].first;
            Job jp; jp.job_id = "bench-overlap"; jp.height = hprev;
            for (int i = 0; i < 32; i++) jp.header[i] = (uint8_t)(0x5A ^ (i * 3) ^ (hprev >> (i % 8)));
            std::memset(jp.target, 0, 32); { int bits = share_bits < 1 ? 1 : share_bits > 255 ? 255 : share_bits; int bit = 256 - bits; jp.target[31 - bit / 8] = (uint8_t)(1u << (bit % 8)); }
            jp.nonce_start = 1ull << 40; jp.nonce_count = ~0ull;
            double o0 = now_s(); bool ready = eng.prepare_ready(); double ready_at = ready ? o0 : 0;
            eng.run_job(jp, [&](const Result &) { hits++; }, [&](const BatchStats &st2) {
                double now = now_s();
                if (first_batch_ms < 0) first_batch_ms = (now - o0) * 1000.0;
                if (!ready) { ready = eng.prepare_ready(); if (ready) ready_at = now; }
                hashes += st2.hashes; since += st2.hashes;
                if (!ready) { hb += st2.hashes; tb = now - o0; } else { ha += st2.hashes; ta = now - (ready_at > 0 ? ready_at : o0); }
                if (now - last >= 1.0) {
                    Json t = Json::object(); eng.telemetry(t);
                    if (t.has("power_w")) { double w = t["power_w"].as_number(); wh += (last_w > 0 ? (w + last_w) / 2 : w) * (now - last) / 3600.0; last_w = w; have_power = true; }
                    printf("{\"t\": %.1f, \"mhs\": %.2f, \"hits\": %zu, \"height\": %llu, \"overlap\": %s, \"telemetry\": %s}\n", now - start, since / (now - last) / 1e6, hits, (unsigned long long)hprev, ready ? "false" : "true", t.dump().c_str());
                    fflush(stdout); since = 0; last = now;
                }
            }, [&] { return now_s() - o0 >= phase_s / 2; });
            overlap_ms = (ready_at > o0 ? ready_at - o0 : now_s() - o0) * 1000.0;
            rate_while_building = tb > 0 ? hb / tb / 1e6 : 0; rate_after_build = ta > 0 ? ha / ta / 1e6 : 0;
        }
        Job j; j.job_id = "bench"; j.height = h;
        for (int i = 0; i < 32; i++) j.header[i] = (uint8_t)(0xA5 ^ (i * 7) ^ (h >> (i % 8)));
        std::memset(j.target, 0, 32);
        { int bits = share_bits < 1 ? 1 : share_bits > 255 ? 255 : share_bits; int bit = 256 - bits; j.target[31 - bit / 8] = (uint8_t)(1u << (bit % 8)); }   // the target 2^(256 - share_bits), big-endian
        j.nonce_start = 0; j.nonce_count = ~0ull;
        double ph_start = now_s(); bool first = true; double downtime_ms = 0;
        bool released = false; double release_at = 0, release_gap_ms = -1; uint64_t release_hashes = 0;
        ignpow_ctx *vctx = nullptr;
        if (verify_every > 0) { int vst = 0; vctx = ignpow_ctx_create(algo.c_str(), pj, h, 0, &vst); }
        eng.run_job(j,
            [&](const Result &res) {
                hits++;
                if (vctx && verify_every > 0 && (hits % (size_t)verify_every) == 0) {
                    uint8_t m[32], d[32]; int vs = ignpow_hash(vctx, h, j.header, res.nonce, m, d);
                    if (vs == IGNPOW_OK && std::memcmp(d, res.digest, 32) == 0 && std::memcmp(m, res.mix, 32) == 0) verified++; else failed++;
                }
            },
            [&](const BatchStats &s) {
                double now = now_s();
                if (first) { first = false; downtime_ms = (now - ph_start) * 1000.0; }
                hashes += s.hashes; since += s.hashes;
                if (release_after_s > 0 && !released && now - ph_start >= release_after_s) {   // Ember's memory-pressure release, proved: the knob set mid-run, the engine's next batch waits for the rebuild
                    Knobs k2 = eng.knobs(); k2.release_dataset = 1; eng.set_knobs(k2); released = true; release_at = now; release_hashes = hashes;
                    printf("{\"release\": {\"t\": %.1f, \"note\": \"release_dataset set; the engine drops the dataset at its next iteration and rebuilds before its next batch\"}}\n", now - start); fflush(stdout);
                // the first batch SUBMITTED after the release (a batch in flight at the release completes first and is not the gap)
                } else if (released && release_gap_ms < 0 && s.hashes > 0 && now - s.wall_ms / 1000.0 > release_at) { release_gap_ms = (now - release_at) * 1000.0; printf("{\"released\": {\"gap_ms\": %.0f, \"hashes_before\": \"%llu\", \"note\": \"the first batch submitted after the release; the gap is the drop plus the rebuild\"}}\n", release_gap_ms, (unsigned long long)release_hashes); fflush(stdout); }
                if (now - last >= 1.0) {
                    Json t = Json::object(); eng.telemetry(t);
                    if (t.has("power_w")) { double w = t["power_w"].as_number(); wh += (last_w > 0 ? (w + last_w) / 2 : w) * (now - last) / 3600.0; last_w = w; have_power = true; if (power_source.empty() && t.has("power_source")) power_source = t["power_source"].as_string(); }
                    printf("{\"t\": %.1f, \"mhs\": %.2f, \"hits\": %zu, \"height\": %llu, \"telemetry\": %s}\n", now - start, since / (now - last) / 1e6, hits, (unsigned long long)h, t.dump().c_str());
                    fflush(stdout);
                    since = 0; last = now;
                }
            },
            [&] { return now_s() - ph_start >= (kind == "warm" ? phase_s / 2 : phase_s); });
        if (vctx) ignpow_ctx_free(vctx);
        if (first) downtime_ms = (now_s() - ph_start) * 1000.0;   // no batch ran: the whole phase was downtime (a refusal)
        if (kind != "resident") {
            char line[768];
            snprintf(line, sizeof line, "{\"kind\": \"%s\", \"to_height\": %llu, \"to_epoch\": %llu, \"prepared\": %s, \"prepare_enqueue_ms\": %.0f, \"build_overlap_ms\": %.0f, \"rate_while_building_mhs\": %.2f, \"rate_after_build_mhs\": %.2f, \"first_batch_ms\": %.0f, \"swap_wait_ms\": %.0f, \"downtime_ms\": %.0f, \"ran\": %s, \"note\": \"%s\"}",
                     kind.c_str(), (unsigned long long)h, (unsigned long long)ignpow_epoch_of(algo.c_str(), pj, h, &st), prepared ? "true" : "false", tr_prep_ms, overlap_ms, rate_while_building, rate_after_build, first_batch_ms, eng.last_swap_wait_ms(), downtime_ms, first ? "false" : "true", esc(prep_note).c_str());
            printf("{\"transition\": %s}\n", line); fflush(stdout);
            transitions_json += (transitions_json.empty() ? "" : ", ") + std::string(line);
        }
    }
    double el = now_s() - start;
    std::string note = notes;
    if (!have_power) note += (note.empty() ? "" : "; ") + std::string("power not reported by this backend: telemetry_wh is 0, a wall meter is the measure");
    else if (!power_source.empty()) note += (note.empty() ? "" : "; ") + std::string("power from ") + power_source + " (telemetry grade)";
    Json code = eng.code_json();
    // the round's first objective (main, 10 Oct 10:4x Mac clock): the achieved logical read bandwidth as a fraction of the
    // card's documented peak, from the kernel's own byte count and the wall, and the energy per hash from the telemetry
    double bytes_per_hash = code.has("bytes_read_per_nonce") ? code["bytes_read_per_nonce"].as_number() : 0.0;
    double achieved_gbs = (el > 0 && bytes_per_hash > 0) ? (double)hashes * bytes_per_hash / el / 1e9 : 0.0;
    double fraction = (peak_gbs > 0 && achieved_gbs > 0) ? achieved_gbs / peak_gbs : 0.0;
    // main, 10 Oct 12:5x UK: every tuning row reports its achieved bandwidth first as a fraction of the attainable ceiling for
    // its request width (form A at that width, measured on the configuration: --ceiling-gbs) and second as a fraction of the
    // documented peak; without the ceiling figure the first fraction is named absent, never substituted by the second
    const double fraction_ceiling = (ceiling_gbs > 0 && achieved_gbs > 0) ? achieved_gbs / ceiling_gbs : 0.0;
    // the ceiling's form (the outside review's rule, 10 Oct 2026, security's TP-147): --ceiling-gbs is the DEPENDENT-chain ceiling (form B) at the
    // row's width unless IGM_CEILING_FORM names A; the independent ceiling (form A) rides beside it from --independent-ceiling-gbs
    const char *cf = getenv("IGM_CEILING_FORM"); const std::string ceiling_form = (cf && *cf) ? std::string(cf) : std::string("B");
    const char *ic = getenv("IGM_INDEPENDENT_CEILING_GBS"); const double independent_gbs = ic ? atof(ic) : 0.0;
    code.set("attainable_ceiling_gbs", ceiling_gbs).set("fraction_of_attainable_ceiling", fraction_ceiling)
        .set("ceiling_form", ceiling_gbs > 0 ? ceiling_form : std::string("none"))
        .set("attainable_ceiling_gbs_form_b", ceiling_form == "B" ? ceiling_gbs : 0.0).set("attainable_ceiling_gbs_form_a", ceiling_form == "A" ? ceiling_gbs : independent_gbs)
        .set("independent_ceiling_gbs", independent_gbs).set("fraction_of_independent_ceiling", (independent_gbs > 0 && achieved_gbs > 0) ? achieved_gbs / independent_gbs : 0.0)
        .set("fraction_of_documented_peak", fraction)
        .set("ceiling_note", ceiling_gbs > 0 ? (ceiling_form == "B" ? "the ceiling is form B (the dependent chain at this row's request width, reads, dataset and cache policy, concurrent chains and address dependency) on this configuration; the first fraction of the row; the independent ceiling (form A) beside it" : "the ceiling is form A (independent requests) at this row's request width; the dependent ceiling is not given, the fraction is not the outside review's") : "no ceiling given (--ceiling-gbs): the fraction of the attainable ceiling is absent; the fraction of documented peak is the second fraction only");
    double uj_per_hash = (have_power && hashes > 0) ? wh * 3600.0 / (double)hashes * 1e6 : 0.0;
    // accepted work (SPEC-TEXT section 14): expected trials per hit at the integer target 2^(256 - bits) are 2^bits; the hits are
    // unique nonces of one job; sampled verification by the reference (never consensus verification)
    double accepted_work = (double)hits * std::ldexp(1.0, share_bits);
    double uj_per_accepted = (have_power && accepted_work > 0) ? wh * 3600.0 / accepted_work * 1e6 : 0.0;
    code.set("share_bits", (double)share_bits).set("hits", (double)hits).set("accepted_work_hashes", accepted_work).set("uj_per_accepted_hash", uj_per_accepted)
        .set("verified_sample", (double)verified).set("failed_sample", (double)failed).set("verify_every", (double)verify_every)
        .set("accepted_note", failed ? "FAILED verification in the sample: this row is invalid" : (verify_every ? "every n-th hit recomputed by the linked reference" : "hits not verified (verify_every 0)"))
        // the governing measurement (the project's correction, 10 Oct): energy per accepted hash, the energy boundary named
        .set("energy_per_accepted_hash_uj", uj_per_accepted).set("device_telemetry_kwh", wh / 1000.0).set("device_telemetry_joules", wh * 3600.0).set("energy_boundary", have_power ? (power_source.empty() ? "board telemetry" : power_source + " (board telemetry, not the wall)") : "none: no power reading on this backend")
        .set("dependent_read_depth", code.has("reads_per_nonce") ? code["reads_per_nonce"].as_number() : (bytes_per_hash > 0 ? bytes_per_hash / 128.0 : 0.0)).set("dependent_read_note", "the chain of 128-byte reads per nonce, each address from the previous read's result")
        .set("useful_bytes_note", "achieved_read_gbs is the kernel's own logical bytes; physical traffic (fetch size, transactions) is a separate counter where the runtime exposes it, else named not exposed in this record");
    // the project's row form (12:0x UK, 10 Oct 2026): after the id, useful bytes per hash beside the physical bytes the runtime
    // exposes (or "not exposed by this runtime"), accepted work per second beside joules per accepted hash with the measured
    // mean board power on the row (never an assumed flat power), the fraction as a diagnostic; the pid unchanged on every tuned row
    double board_w_mean = (have_power && el > 0) ? wh * 3600.0 / el : 0.0;
    code.set("useful_bytes_per_hash", bytes_per_hash).set("physical_bytes_per_hash", code.has("physical_bytes_per_hash") ? code["physical_bytes_per_hash"] : Json("not exposed by this runtime"))
        .set("accepted_work_per_s", el > 0 ? accepted_work / el : 0.0).set("joules_per_accepted_hash", uj_per_accepted / 1e6)
        .set("board_power_w_mean", board_w_mean).set("board_power_note", have_power ? "the metered mean over the run (telemetry grade): the energy is the integral of the samples, not a flat figure" : "no power reading on this backend")
        .set("semantics", "fixed: the params document and its id are those of the untuned row; a tuned row changes only the engine's knobs and variant");
    {   // the ceiling diagnostic (the project's order, 12:1x UK): the form named on the row and the energy per 128-byte request
        const std::string v = knobs.variant;
        const bool probe = v == "probe-alu" || v == "probe-shuffle" || v == "probe-chain" || v == "probe-decode";
        const bool bw = v.rfind("bw-b", 0) == 0;   // Breakwater: a loader form; phase A (no -r<n>) is transfer-only
        const bool bw_xfer = bw && v.find("-r") == std::string::npos;
        const bool xfer = v.size() > 5 && v.compare(v.size() - 5, 5, "-xfer") == 0;   // the outside review's matched-resource dependent ceiling on a form (DIAGNOSTIC, never a cell)
        const bool diag = v == "diag-a" || v == "diag-b" || v == "reach-half" || probe || bw_xfer || xfer;
        if (xfer) code.set("xfer_diagnostic", "the form's loads at the relation's count, the next address from the loaded words, no absorb, tree, cell, exchange or finalisation; the footprint pinned by a kept-alive register array (xfer_keep words per lane; the dry run states the match to the form's VGPR count); the hash wrong by design");
        if (bw) code.set("breakwater", bw_xfer ? "phase A loader " + v.substr(3) + ": one 512-byte macro-item per wave, the loaded words folded into the next address by R1 (the control), no S, L, M or F; transfer-only, the hash wrong by design, never a cell beyond phase A" : "phase B: loader " + v.substr(3, v.find("-r") - 3) + " with reduction " + v.substr(v.find("-r") + 1));
        if (v == "form1") code.set("form1", "Riptide fused form 1: the macro-item as 16-byte loads per lane per 128-byte step, all steps in flight before any arithmetic, the tree on the loaded layout (two levels local, three through the exchange), the absorb routed to the relation's lanes through the exchange (strata/RIPTIDE.md; the project's specification 10 Oct 15:3x UK)");
        if (v == "form2") code.set("form2", "Riptide fused form 2: form 1's loads and tree, the absorb routed through local memory (one 16-byte store per step, one strided read per register): the LDS-staging row the project asked for as a measured comparison");
        if (v == "swizzle") code.set("swizzle", "the AMD logical-to-physical dataset swizzle (the convergence design): items stored at slot(i) = i ^ ((i >> 8) & 255) ^ (((i >> 16) & 255) << 4); the relation unchanged, an exact kernel");
        if (v == "overfetch256" || v == "overfetch512") code.set("overfetch", "the W128 relation with the request widened to the " + v.substr(9) + "-byte aligned span (the Turing twin): an exact kernel, the extra half read and discarded outside the hash");
        const double requests = code.has("reads_per_nonce") ? code["reads_per_nonce"].as_number() : (bytes_per_hash > 0 ? bytes_per_hash / 128.0 : 0.0);   // one request per round whatever its width (the width sweep, version 3)
        code.set("form", xfer ? "the form's dependent macro-item chain at its own footprint, no reduction (the matched-resource ceiling; DIAGNOSTIC, never a cell)" : v == "diag-a" ? "A: independent requests (DIAGNOSTIC, never a cell)" : v == "diag-b" ? "B: dependent chains, many nonces in flight (DIAGNOSTIC, never a cell)" : v == "reach-half" ? "reach-half (DIAGNOSTIC, never a cell)" : probe ? "Mosaic lane microbenchmark " + v + " at the lane's shape (DIAGNOSTIC, never a cell): the resource's ceiling in nonce loops per second" : "C: v1 itself")
            .set("requests_per_s", el > 0 && requests > 0 ? (double)hashes * requests / el : 0.0)
            .set("uj_per_request", requests > 0 ? uj_per_hash / requests : 0.0)
            .set("cell", diag ? "none: a diagnostic row, the hash wrong by design" : "a cell when the vectors pass");
    }
    {   // Breakwater's row fields (amendment B20): the request-ceiling fraction, the spills, the ISA verdict as given
        const double rps = code.has("requests_per_s") ? code["requests_per_s"].as_number() : 0.0;
        code.set("request_ceiling_gps", knobs.request_ceiling_gps)
            .set("fraction_of_request_ceiling", knobs.request_ceiling_gps > 0 && rps > 0 ? rps / (knobs.request_ceiling_gps * 1e9) : 0.0)
            .set("fraction_of_request_ceiling_note", knobs.request_ceiling_gps > 0 ? "requests per second (one macro-item per round) over the configuration's measured random-request ceiling (--request-ceiling-gps)" : "n/a: no request ceiling given (--request-ceiling-gps)")
            .set("spills", code.has("private_mem_bytes") ? code["private_mem_bytes"] : Json("n/a")).set("spills_note", "CL_KERNEL_PRIVATE_MEM_SIZE in bytes (0 is the zero-spill condition); the offline compile's scratch_bytes stands beside it in the job")
            .set("isa_verdict", knobs.isa_verdict.empty() ? "n/a" : knobs.isa_verdict).set("states_per_wave", (double)knobs.states_per_wave);
    }
    code.set("bytes_read_per_nonce", bytes_per_hash).set("achieved_read_gbs", achieved_gbs).set("peak_gbs", peak_gbs).set("bandwidth_fraction", fraction).set("uj_per_hash", uj_per_hash)
        .set("bandwidth_note", peak_gbs > 0 ? "fraction of the configuration's documented peak (logical bytes: the kernel's own read count over the wall; a cache may serve some)" : "peak not given (--peak-gbs): the fraction is omitted");
    printf("{\"bench\": {\"device\": \"%s\", \"arch\": \"%s\", \"height\": %llu, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.2f, \"telemetry_wh\": %.3f, \"batch\": %u, \"describe\": \"%s\", \"notes\": \"%s\", \"transitions\": [%s], \"code\": %s}}\n",
           esc(dev["name"].as_string()).c_str(), esc(dev["compute"].as_string()).c_str(), (unsigned long long)height, el, (unsigned long long)hashes, el > 0 ? hashes / el / 1e6 : 0.0, wh,
           eng.knobs().threadgroup, esc(eng.describe() + " batch_ms " + std::to_string(eng.knobs().batch_ms) + " duty " + std::to_string(eng.knobs().duty)).c_str(), esc(note).c_str(), transitions_json.c_str(), code.dump().c_str());
    fflush(stdout);
    eng.close();
    return 0;
}

}  // namespace igm
