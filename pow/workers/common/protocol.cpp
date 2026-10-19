// protocol.cpp: see protocol.h.
#include "protocol.h"
#include "candidates.h"
#include "hex.h"
#include "ignpow.h"
#include "igneumhash.h"   // the seal verb: ignh_hash_keyed_m, the reference's replay of the sealed nonce (shares and m)
#include "ih_params.h"
#include <functional>
#ifdef _WIN32
#include <windows.h>
#else
#include <sys/stat.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <netinet/in.h>
#include <sys/resource.h>
#include <sys/mman.h>
#endif
#ifdef __linux__
#include <sys/prctl.h>
#endif
#ifdef __APPLE__
#include <sys/ptrace.h>
#endif
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstring>
#include <iostream>
#include <mutex>
#include <thread>

namespace igm {

namespace {

std::mutex g_wmu;
uint64_t g_seq = 0;

std::string dec(uint64_t v) { return std::to_string(v); }

void emit(Json j) {
    std::string s;
    {
        std::lock_guard<std::mutex> g(g_wmu);
        j.set("seq", (unsigned long long)++g_seq);
        s = j.dump();
        fwrite(s.data(), 1, s.size(), stdout);
        fputc('\n', stdout);
        fflush(stdout);
    }
}

void fault(const std::string &code, const std::string &message, bool recoverable, uint64_t re = 0) {
    Json j = Json::object().set("t", "fault").set("code", code).set("message", message.substr(0, 512)).set("recoverable", recoverable);
    if (re) j.set("re", (unsigned long long)re);
    emit(j);
}

// The hello's `algos`: every candidate this host carries (the registry the MultiEngine opens from, candidates.h),
// each with its own vectors_ok from the reference's embedded vectors (ignpow_vectors_check(id)), as the CUDA host
// declares its six; the host's named --algo first with the self-test result the host passed, so a supervisor that
// reads one entry still reads the one it asked for. Before 11 October 2026 the Metal and OpenCL hosts declared the
// --algo value alone (kawpow-0.9.4 by default), and Ember's Mac parity row stopped on "no enabled worker carries the
// source's algorithm" (the product team, 08:1x UK); the project's order of 10:20 UK.
Json hello_algos(const std::string &algo_id, bool vectors_ok) {
    static const Candidate *const kCarried[] = { &kIgneumhashR0a, &kRxe1, &kKawpow094, &kFishhash1, &kAutolykos2 };
    Json algos = Json::array().push(Json::object().set("id", algo_id).set("vectors_ok", vectors_ok));
    for (const Candidate *c : kCarried) {
        if (algo_id == c->id) continue;
        size_t n = 0;
        const bool ok = ignpow_vectors_check(c->id, &n) == IGNPOW_OK;
        algos.push(Json::object().set("id", std::string(c->id)).set("vectors_ok", ok));
    }
    return algos;
}

// The custody guard of the seal verb (KEYED-SEAL-WORKER.md 1, the CUDA worker's form): a keyed job (sk on the line) is taken only
// from a local peer, whoever holds the other end of fd 0: a pipe, a terminal or a file (the parent on this host), a Unix socket, or
// a TCP socket whose peer is a loopback address; any other socket peer is refused with the fault keyed-peer and the job is not taken.
static bool local_peer() {
#ifdef _WIN32
    const DWORD ft = GetFileType(GetStdHandle(STD_INPUT_HANDLE));
    return ft == FILE_TYPE_PIPE || ft == FILE_TYPE_CHAR || ft == FILE_TYPE_DISK;
#else
    struct stat st; if (fstat(0, &st) != 0) return false;
    if (!S_ISSOCK(st.st_mode)) return true;
    sockaddr_storage ss; socklen_t sl = sizeof ss;
    if (getpeername(0, reinterpret_cast<sockaddr *>(&ss), &sl) != 0) return false;
    if (ss.ss_family == AF_UNIX) return true;
    if (ss.ss_family == AF_INET) { const auto *a = reinterpret_cast<const sockaddr_in *>(&ss); return (ntohl(a->sin_addr.s_addr) >> 24) == 127; }
    if (ss.ss_family == AF_INET6) { const auto *a = reinterpret_cast<const sockaddr_in6 *>(&ss); if (IN6_IS_ADDR_LOOPBACK(&a->sin6_addr)) return true; if (IN6_IS_ADDR_V4MAPPED(&a->sin6_addr)) return a->sin6_addr.s6_addr[12] == 127; }
    return false;
#endif
}
static void zero32(uint8_t *b) { volatile uint8_t *z = b; for (int i = 0; i < 32; i++) z[i] = 0; }

// The secret's custody on the host (the security review's F-53, the hash team's line of 12 Oct 2026): a crash must not write the bond secret
// into a core file on the host's disk (a rented host's landlord's), and the host copy of sk must not be paged out. At the first keyed
// job: the core limit set to 0, the process made non-dumpable (Linux: PR_SET_DUMPABLE 0; macOS: PT_DENY_ATTACH, which also refuses a
// debugger), and the 32-byte host copy locked in memory (mlock); on Windows the core limit has no equivalent and the dump policy is
// the system's (named "unavailable" in the custody line). The device copy of the secret is the driver's, wiped by wipe_secret. The
// custody line goes out once, on the first keyed job, naming what the platform gave; the test reads the core limit as 0 while a keyed
// job runs.
static std::string custody_arm(uint8_t *sk_host) {   // sk_host null: the process part alone (the lock is the job thread's, on its own copy)
    std::string what;
#ifdef _WIN32
    // Windows: no core limit and no deny-attach; a crash would otherwise hand the process (and the secret) to Windows Error Reporting,
    // so the unhandled-exception filter ends the process at once without a dump; the secret's copies are locked by VirtualLock.
    SetErrorMode(SetErrorMode(0) | SEM_NOGPFAULTERRORBOX | SEM_FAILCRITICALERRORS);
    SetUnhandledExceptionFilter([](EXCEPTION_POINTERS *) -> LONG { TerminateProcess(GetCurrentProcess(), 3); return EXCEPTION_EXECUTE_HANDLER; });
    what = "core limit unavailable (Windows), no dump on a crash (the exception filter ends the process), attachable (Windows offers no deny-attach)";
    if (sk_host) what += VirtualLock(sk_host, 32) ? ", the host secret locked (VirtualLock 32 bytes)" : ", the host secret NOT locked (VirtualLock refused)";
#else
    struct rlimit rl; rl.rlim_cur = 0; rl.rlim_max = 0;
    what += setrlimit(RLIMIT_CORE, &rl) == 0 ? "core limit 0" : "core limit NOT set";
#ifdef __linux__
    what += prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) == 0 ? ", non-dumpable (PR_SET_DUMPABLE 0)" : ", dumpable (PR_SET_DUMPABLE refused)";
#elif defined(__APPLE__)
    what += ptrace(PT_DENY_ATTACH, 0, nullptr, 0) == 0 ? ", non-attachable (PT_DENY_ATTACH)" : ", attachable (PT_DENY_ATTACH refused)";
#endif
    if (sk_host) what += mlock(sk_host, 32) == 0 ? ", the host secret locked (mlock 32 bytes)" : ", the host secret NOT locked (mlock refused)";
#endif
    return what;
}

Json hello_message(EngineIface &eng, const std::string &worker, const std::string &version, const std::string &algo_id, bool vectors_ok) {
    Json d = eng.device_json();
    Json dev = Json::object().set("id", d["id"]).set("name", d["name"]).set("memory_bytes", dec(d["memory_total"].as_u64()))
                   .set("arch", d["compute"]).set("driver", d["driver"]).set("runtime", d.has("platform") ? d["platform"] : Json(eng.backend()))
                   .set("vendor", d["vendor"]).set("fingerprint", d["fingerprint"]).set("recommended_working_set", d.has("recommended_working_set") ? d["recommended_working_set"] : Json());
    if (d.has("compute_detail") && !d["compute_detail"].as_string().empty()) dev.set("arch_detail", d["compute_detail"]);   // the driver's whole device line where the compute word is the chip token alone
    dev.set("bus_id", d["id"]);   // the stable address (the PCI bus id, or the registry id on Apple); the supervisor's bus-id form when no intrinsic id is read
    if (d.has("identity") && d.has("intrinsic_id")) dev.set("identity", d["identity"]).set("intrinsic_id", d["intrinsic_id"]);
    if (d.has("identity_note")) dev.set("identity_note", d["identity_note"]);
    if (d.has("adlx_identity") && !d["adlx_identity"].is_null()) dev.set("adlx_identity", d["adlx_identity"]);   // the Windows AMD reading (read-only)   // the cause of the identity word (the devices verb prints this message; the record reads it)   // the project's ruling of 11 Oct 2026 21:07 UK: the card's own identity for the fingerprint, with its word
    return Json::object().set("t", "hello").set("proto", 0).set("worker", worker).set("version", version).set("backend", eng.backend()).set("extensions", Json::array().push(Json(std::string("keyed-job"))).push(Json(std::string("seal")))).set("seal", true)
        .set("algos", hello_algos(algo_id, vectors_ok))
        .set("devices", Json::array().push(dev)).set("knobs", eng.knob_decls());
}

uint64_t u64_field(const Json &m, const char *k, uint64_t d = 0) {
    const Json &v = m[k];
    if (v.type() == Json::String) return strtoull(v.as_string().c_str(), nullptr, 10);
    return v.as_u64(d);
}

}  // namespace

int run_worker_loop(EngineIface &eng, const std::string &worker, const std::string &version, const std::string &algo_id, bool vectors_ok, bool probe_only) {
    if (probe_only) { emit(hello_message(eng, worker, version, algo_id, vectors_ok)); return 0; }
    emit(hello_message(eng, worker, version, algo_id, vectors_ok));

    std::mutex mu; std::condition_variable cv;
    bool configured = false, quit = false, sup_paused = false;
    uint32_t stat_ms = 1000;
    std::string state = "idle", current_job;
    std::atomic<bool> cancel{false};
    std::atomic<uint64_t> hashes_since{0};
    std::thread job_thread;
    bool first = true;

    std::thread stat([&] {
        auto last = std::chrono::steady_clock::now();
        while (true) {
            uint32_t ms;
            { std::unique_lock<std::mutex> g(mu); ms = stat_ms; if (cv.wait_for(g, std::chrono::milliseconds(ms), [&] { return quit; })) return; if (!configured) continue; }
            auto now = std::chrono::steady_clock::now();
            uint64_t h = hashes_since.exchange(0);
            Json t = Json::object().set("memory_used_bytes", dec(eng.allocated_bytes()));
            eng.telemetry(t);
            std::string st; std::string jid;
            { std::lock_guard<std::mutex> g(mu); st = state; jid = current_job; if (st == "mining" && (sup_paused || eng.paused())) st = "paused"; }
            emit(Json::object().set("t", "stat").set("job_id", jid).set("hashes", dec(h))
                     .set("elapsed_ms", (unsigned long long)std::chrono::duration_cast<std::chrono::milliseconds>(now - last).count())
                     .set("telemetry", t).set("state", st));
            last = now;
        }
    });

    auto stop_job = [&] { cancel = true; if (job_thread.joinable()) job_thread.join(); eng.wipe_secret(); std::lock_guard<std::mutex> g(mu); if (state == "mining") state = "idle"; };
    ignpow_ctx *seal_ref = nullptr; uint64_t seal_ref_height = ~0ull;   // the seal verb's reference context for the one-nonce replay

    std::string line; int rc = 0; bool custody_armed = false;
    while (std::getline(std::cin, line)) {
        if (line.size() > (1u << 20)) { fault("protocol", "line over 1 MiB", true); continue; }
        Json m;
        if (!Json::parse(line, m) || !m.is_object()) { fault("protocol", "malformed line", true); continue; }
        const std::string t = m["t"].as_string();
        uint64_t seq = m["seq"].as_u64(0);
        // The first line must be configure. The earlier form returned 64 here with the stat thread still joinable, and a
        // joinable std::thread's destructor calls std::terminate: SIGABRT from run_worker_loop, a crash report on the user's
        // Mac per launch whose first line was not configure (Ember 0.2.1 on the mini, 32 reports 11 to 12 Oct 2026, every
        // one at this site; the product team's reading 05:3x UK). The worker now leaves by the one exit path (the threads joined,
        // the engine closed, bye emitted) with the same code 64.
        if (first) { first = false; if (t == "shutdown") break; if (t != "configure") { fault("protocol", "first line must be configure", false); rc = 64; break; } }   // shutdown before any configure: the clean end (bye, code 0): Ember's card probe reads the hello and sends it (the product team, 05:4x UK)

        if (t == "configure") {
            // the command line's knobs (the candidate's params document among them) are the base; a configure
            // overrides what it names. Without this the IgneumHash variant on the command line was lost on
            // configure and the protocol path mined the default variant (the mini's 2 GiB smoke, 10 Oct 2026).
            Knobs k = eng.knobs(); k.from_json(m["params"]);
            if (m.has("params") && m["params"].is_object() && !m["params"].has("threadgroup") && !m["params"].has("batch_ms")) k.params = m["params"].dump();   // A's v0: params is the candidate's document, passed through unchanged
            { std::lock_guard<std::mutex> g(mu); if (m.has("stat_ms")) stat_ms = (uint32_t)m["stat_ms"].as_u64(1000); if (stat_ms < 100) stat_ms = 100; }
            std::string algo = m["algo"].as_string();
            uint64_t height = u64_field(m, "height", 0);
            // memory_margin_bytes is A's safety margin (256 MiB by default), a subtraction the
            // engines already make (the Apple headroom rule exceeds it; the OpenCL method
            // subtracts 256 MiB itself); it is never the supervisor's budget cap.
            uint64_t margin = u64_field(m, "memory_margin_bytes", 0); (void)margin;
            // A RECONFIGURE ENDS THE RUNNING JOB FIRST (16 October 2026 08:2x UK, the GPU team's read of d12-sealer-2: the GL raster worker died
            // with std::bad_function_call at the raster lane's anchor boundary, where the driver's configure carrying the new anchor
            // reached eng.open() while the job thread still drew on the engine's card; open() closes and rebuilds the card, and the job
            // thread's next attempt called an empty callback). The job branch already stops a running job on a clean job; the configure
            // branch did not. The job thread is joined here before the engine is touched, on every engine.
            stop_job();
            Refusal r;
            if (eng.open(algo, height, k, 0, r)) {
                // The keyed self-check (12 Oct 2026, the the GPU team and Apple seat): a chip whose compiler miscompiles the keyed kernel must
                // never mine a keyed document (the M5 Max's Metal compiler produced a digest with its first word zero on every keyed
                // class while the M6 and every other card were exact; three workarounds did not clear it). After a keyed document opens,
                // the card hashes one nonce under the header's test secret and the result is compared with the reference's keyed hash
                // of the same nonce; a mismatch refuses the document with the fault keyed-chip and closes the engine.
                if (eng.keyed_document()) {
                    // Thirty-two nonces under two headers (the M5 Max passed a one-nonce check under the bench header while every vector
                    // and every protocol-mode hit under the vectors' header came out wrong, so the fault is header- or nonce-dependent):
                    // the bench header and a header derived from the document's parameter bytes; any mismatch refuses the document.
                    uint8_t hdrs[2][32]; for (int i = 0; i < 32; i++) { hdrs[0][i] = (uint8_t)(i * 7 + 1); hdrs[1][i] = (uint8_t)(0x64 ^ (i * 13 + 5)); }
                    { IhParams ip; std::string perr; if (ih_parse_params(eng.knobs().params, ip, perr)) { uint8_t pb[32]; igm::ih_P(ip, pb); for (int i = 0; i < 32; i++) hdrs[1][i] ^= pb[i]; } }
                    int st = 0; ignpow_ctx *ref = ignpow_ctx_create(algo.c_str(), eng.knobs().params.c_str(), height, 0, &st);
                    std::string why; bool all_same = ref != nullptr; if (!ref) why = "the reference context for the self-check could not be created";
                    for (int h = 0; h < 2 && all_same; h++) {
                        uint8_t sk[32]; ignh_test_sk(hdrs[h], sk);
                        Job cj; cj.job_id = "keyed-self-check"; cj.height = height; std::memcpy(cj.header, hdrs[h], 32); std::memset(cj.target, 0xff, 32); cj.nonce_start = 1; cj.nonce_count = 16; cj.has_sk = true; std::memcpy(cj.sk, sk, 32);
                        uint8_t got_mix[16][32], got_hash[16][32]; bool got[16] = {false}; int n_got = 0;
                        eng.run_job(cj, [&](const Result &res) { if (res.nonce >= 1 && res.nonce <= 16 && !got[res.nonce - 1]) { std::memcpy(got_mix[res.nonce - 1], res.mix, 32); std::memcpy(got_hash[res.nonce - 1], res.digest, 32); got[res.nonce - 1] = true; n_got++; } }, [](const BatchStats &) {}, [&] { return n_got >= 16; });
                        zero32(cj.sk); eng.wipe_secret();
                        for (int n = 0; n < 16 && all_same; n++) {
                            uint8_t shares[64 * 32], rmix[32], rhash[32];
                            if (!got[n]) { all_same = false; why = "the card returned no result for self-check nonce " + std::to_string(n + 1) + " under header " + std::to_string(h); break; }
                            if (ignh_hash_keyed(ref, height, hdrs[h], (uint64_t)(n + 1), sk, shares, rmix, rhash) != IGNPOW_OK) { all_same = false; why = "the reference's keyed hash failed"; break; }
                            if (std::memcmp(got_mix[n], rmix, 32)) { all_same = false; why = "the card's mix of self-check nonce " + std::to_string(n + 1) + " under header " + std::to_string(h) + " differs from the reference's"; }
                            else if (std::memcmp(got_hash[n], rhash, 32)) { all_same = false; why = "the card's digest of self-check nonce " + std::to_string(n + 1) + " under header " + std::to_string(h) + " differs from the reference's (" + hex_of(got_hash[n], 8) + " against " + hex_of(rhash, 8) + " in the first eight bytes)"; }
                        }
                        zero32(sk);
                    }
                    if (ref) ignpow_ctx_free(ref);
                    if (!all_same) {
                        eng.close();
                        emit(Json::object().set("t", "refuse").set("re", (unsigned long long)seq).set("reason", "keyed-chip").set("explain", "a keyed document (features bit 10) is refused on this card: " + why + "; this chip's compiler miscompiles the keyed kernel and the worker will not mine a keyed document on it until a worker build names it exact").set("overflowing_term", ""));
                        continue;
                    }
                }
                { std::lock_guard<std::mutex> g(mu); configured = true; state = "idle"; }
                emit(Json::object().set("t", "ready").set("re", (unsigned long long)seq).set("device", m["device"]).set("algo", algo)
                         .set("budget", eng.budget_json()).set("describe", eng.describe()).set("params", eng.knobs().to_json()));
            } else {
                emit(Json::object().set("t", "refuse").set("re", (unsigned long long)seq).set("reason", r.code).set("explain", r.explain.substr(0, 512))
                         .set("overflowing_term", r.code == "memory" ? "dataset" : "").set("need_bytes", dec(r.need_bytes)).set("have_bytes", dec(r.have_bytes)));
            }
            continue;
        }
        if (t == "prepare") {
            Refusal r; uint64_t height = u64_field(m, "height", 0);
            auto t0 = std::chrono::steady_clock::now();
            // the optional params field (the product team, 10 Oct 2026): the document the next dataset is for; absent = the resident one
            std::string pp; if (m.has("params")) pp = m["params"].type() == Json::String ? m["params"].as_string() : m["params"].dump();
            const bool changed = !pp.empty() && pp != eng.knobs().params;
            bool ok = eng.prepare(height, r, pp);
            uint64_t ms = (uint64_t)std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count();
            if (ok) emit(Json::object().set("t", "prepared").set("re", (unsigned long long)seq).set("height", dec(height)).set("ms", (unsigned long long)ms).set("bytes", dec(eng.allocated_bytes())).set("mode", "warm").set("params_change", changed));
            else emit(Json::object().set("t", "refuse").set("re", (unsigned long long)seq).set("reason", r.code).set("explain", r.explain.substr(0, 512)).set("overflowing_term", "next_dataset"));
            continue;
        }
        if (t == "job") {
            Job j;
            j.job_id = m["job_id"].as_string();
            j.height = u64_field(m, "height", 0);
            uint8_t boundary[32];
            if (!hex_to(m["header"].as_string(), j.header, 32) || !hex_to(m["boundary"].as_string(), boundary, 32)) { fault("protocol", "job: header or boundary length", true, seq); continue; }
            if (!m.has("share_target") || !hex_to(m["share_target"].as_string(), j.target, 32)) std::memcpy(j.target, boundary, 32);
            j.nonce_start = u64_field(m, "nonce_start", 0);
            j.nonce_count = u64_field(m, "nonce_count", 0);
            if (m.has("sk")) j.has_sk = hex_to(m["sk"].as_string(), j.sk, 32);   // the keyed-job extension: the bond secret, 32 bytes hex, per job
            if (j.has_sk && !custody_armed) { custody_armed = true; fprintf(stderr, "custody: %s\n", custody_arm(nullptr).c_str()); }   // F-53: once, at the first keyed job, on stderr (a protocol line of a new type would be an unknown line to the supervisor, which stops a worker on the second)
            if (eng.keyed_document() && !j.has_sk) { fault("keyed", "a keyed document (features bit 10) needs sk on the job", true, seq); continue; }   // the named fault; the job is not taken
            if (j.has_sk && !local_peer()) { zero32(j.sk); fault("keyed-peer", "a keyed job is taken only from a loopback or Unix-socket peer", false, seq); continue; }   // the seal verb's custody guard
            j.seal = m.has("seal") && m["seal"].as_bool(false);
            j.deadline_ms = u64_field(m, "deadline_ms", 0);
            if (j.seal) std::memcpy(j.target, boundary, 32);   // a seal job searches at its boundary
            if (m["clean"].as_bool(true)) stop_job();
            else if (job_thread.joinable()) job_thread.join();
            cancel = false;
            { std::lock_guard<std::mutex> g(mu); state = "mining"; current_job = j.job_id; }
            job_thread = std::thread([&, j, seq]() mutable {
                uint64_t total = 0; bool sealed = false, deadline_hit = false; unsigned replay_faults = 0;   // eight consecutive replay faults end a seal job (a card hashing wrong under an easy boundary would otherwise replay every hit of the range)
#ifdef _WIN32
                const bool locked = j.has_sk && VirtualLock(j.sk, 32) != 0;   // F-53 on Windows: the job thread's own copy of the secret locked in memory for the job
#else
                const bool locked = j.has_sk && mlock(j.sk, 32) == 0;   // F-53: the job thread's own copy of the secret, the one that lives for the job, locked in memory; unlocked after the zeroing
#endif
                const auto started = std::chrono::steady_clock::now();
                eng.run_job(j,
                    [&](const Result &r) {
                        if (!j.seal) { emit(Json::object().set("t", "share").set("job_id", j.job_id).set("nonce", dec(r.nonce)).set("mix", hex_of(r.mix, 32)).set("hash", hex_of(r.digest, 32))); return; }
                        if (sealed || replay_faults >= 8) return;   // after the eighth replay fault the job is ending: the batch's remaining hits are not replayed (a chip whose digest is zero-led hits on every nonce; replaying each would hold the thread, and a shutdown behind it, for the whole batch)
                        // the seal verb: the first result at or under the boundary, its shares and m_i from the reference's replay of that
                        // one nonce under the job's secret (ignh_hash_keyed_m); the replay's mix and hash must equal the card's, else the
                        // fault seal-replay and the search goes on; the card's own hit is the proof of work, the replay one CPU hash per seal
                        IhParams ip; std::string perr; const uint32_t K = ih_parse_params(eng.knobs().params, ip, perr) ? (ip.version >= 20 ? ip.keyed_steps : ip.keyed_rounds) : 0;
                        std::vector<uint8_t> shares(size_t(K) * 32), mm(size_t(K) * 32); uint8_t mix[32], hash[32]; bool ok = false;
                        if (K && j.has_sk) {
                            int st = 0;
                            if (!seal_ref || seal_ref_height != j.height) { if (seal_ref) ignpow_ctx_free(seal_ref); seal_ref = ignpow_ctx_create(algo_id.c_str(), eng.knobs().params.c_str(), j.height, 0, &st); seal_ref_height = j.height; }
                            ok = seal_ref && ignh_hash_keyed_m(seal_ref, j.height, j.header, r.nonce, j.sk, shares.data(), mix, hash, mm.data()) == IGNPOW_OK
                                 && std::memcmp(mix, r.mix, 32) == 0 && std::memcmp(hash, r.digest, 32) == 0;
                        }
                        if (!ok) { if (++replay_faults <= 8) fault("seal-replay", "the reference's replay of nonce " + std::to_string(r.nonce) + " did not match the card's hit; the search goes on" + (replay_faults == 8 ? " (the eighth: the job ends)" : ""), true, seq); return; }
                        replay_faults = 0;
                        Json sh = Json::array(), mj = Json::array();
                        for (uint32_t i = 0; i < K; i++) { sh.push(Json(hex_of(shares.data() + size_t(i) * 32, 32))); mj.push(Json(hex_of(mm.data() + size_t(i) * 32, 32))); }
                        emit(Json::object().set("t", "result").set("job_id", j.job_id).set("nonce", dec(r.nonce)).set("mix", hex_of(r.mix, 32)).set("hash", hex_of(r.digest, 32)).set("shares", sh).set("m", mj));
                        sealed = true;
                    },
                    [&](const BatchStats &st) { total += st.hashes; hashes_since += st.hashes; },
                    [&] { bool p; { std::lock_guard<std::mutex> g(mu); p = sup_paused; } if (p) { std::this_thread::sleep_for(std::chrono::milliseconds(50)); }
                          if (j.seal && j.deadline_ms && !sealed && std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - started).count() >= (long long)j.deadline_ms) deadline_hit = true;
                          return cancel.load() || p || sealed || deadline_hit || replay_faults >= 8; });
                if (j.seal) {
                    if (!sealed && !cancel.load()) emit(Json::object().set("t", "done").set("job_id", j.job_id).set("reason", std::string(replay_faults >= 8 ? "seal-replay" : deadline_hit ? "deadline" : "exhausted")).set("nonces", dec(total)));
                    eng.wipe_secret();   // the device's copy of the secret leaves with the job
                }
                zero32(j.sk);   // the job thread's copy, every job
#ifdef _WIN32
                if (locked) VirtualUnlock(j.sk, 32);
#else
                if (locked) munlock(j.sk, 32);
#endif
                // a pause returns from run_job with the range unfinished: the supervisor resends after resume.
                // No message marks the range's end: protocol v0 (igneum-worker-proto FromWorker) names nine messages and
                // a `jobdone` line is unknown to the supervisor, which stops a worker on the second unknown line (the
                // Mac parity row pa-mini-parity-ember-macos-4, 11 Oct 2026: "the worker sent malformed output twice and
                // was stopped"); the hashes reach the supervisor through the stat line, the state through the state field.
                (void)seq; (void)total;
                std::lock_guard<std::mutex> g(mu); if (state == "mining") state = "idle";
            });
            zero32(j.sk);   // the main thread's copy: the job thread holds its own
            continue;
        }
        if (t == "set") {   // jobd's form: {"t":"set","knob":name,"value":v}; the v1 form {"params":{...}} is taken too
            Knobs k = eng.knobs();
            if (m.has("knob")) {
                // the knob ack (the product team's read of 13 Oct 00:46 UK: the host's parser wants `knob` and `value` on the answer, as the CUDA
                // worker gives them): a knob the Knobs form knows is applied and acked with the value the engine holds, or the requested
                // value for a one-shot knob the engine consumes (release_dataset); a knob it does not know is a recoverable fault
                const std::string name = m["knob"].as_string(); Json p = Json::object().set(name, m["value"]);
                const bool known = k.to_json().has(name);
                if (!known) { emit(Json::object().set("t", "fault").set("re", (unsigned long long)seq).set("kind", "knob").set("code", "knob").set("message", "unknown knob " + name).set("recoverable", true)); continue; }
                k.from_json(p); eng.set_knobs(k);
                const Json after = eng.knobs().to_json(); const Json applied = (name == "release_dataset") ? Json(1.0) : after[name];
                emit(Json::object().set("t", "knob").set("re", (unsigned long long)seq).set("knob", name).set("value", applied).set("params", after));
                continue;
            }
            k.from_json(m["params"]);
            eng.set_knobs(k);
            emit(Json::object().set("t", "knob").set("re", (unsigned long long)seq).set("params", eng.knobs().to_json()));
            continue;
        }
        if (t == "cancel") { std::string cid; { std::lock_guard<std::mutex> g(mu); cid = current_job; } if (!m.has("job_id") || m["job_id"].as_string() == cid) stop_job(); continue; }   // cancel by job_id (the dispatcher's form); a bare cancel stops any job
        if (t == "pause") { std::lock_guard<std::mutex> g(mu); sup_paused = true; continue; }
        if (t == "resume") { std::lock_guard<std::mutex> g(mu); sup_paused = false; continue; }
        if (t == "shutdown") { stop_job(); break; }
        // unknown types are ignored
    }
    stop_job();
    if (seal_ref) ignpow_ctx_free(seal_ref);
    eng.close();
    { std::lock_guard<std::mutex> g(mu); quit = true; }
    cv.notify_all();
    stat.join();
    emit(Json::object().set("t", "bye"));
    return rc;
}

}  // namespace igm
