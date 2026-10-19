// igneum-worker-cuda: the CUDA worker of the KAWPOW family candidate.
//
//   igneum-worker-cuda devices
//   igneum-worker-cuda vectors <vectors dir> [--device N]       HW-01 corpus on the card
//   igneum-worker-cuda bench <height> <seconds> [--device N]    throughput with telemetry lines
//   igneum-worker-cuda protocol [--device N]                    docs/gpu-a/WORKER-PROTOCOL.md on stdin/stdout
//   --device-name <substring> picks the card by name on a machine with several (e.g. --device-name 5080)
//   every mode takes --algo <candidate id> (default kawpow-0.9.4) and --params '<json>' (the candidate's
//   parameter document, passed through to the reference; the ablation variables ride in it)
//
// Every mode runs the reference's embedded vectors first and refuses to start
// when they fail. Telemetry is diagnostic. The worker never changes a power
// limit, a clock or any system setting.

#include "engine.hpp"
#include "ignpow.h"

#ifdef _WIN32
#include <windows.h>
#include <io.h>
#else
#include <poll.h>
#include <unistd.h>
#include <sys/socket.h>   // the split verb's oracle link (the renter of GUARD-C-CLOSURE.md 1.4)
#include <netdb.h>
#include <netinet/tcp.h>
#include <netinet/in.h>   // the seal verb's local-peer guard (KEYED-SEAL-WORKER.md 1): the peer of fd 0
#include <sys/un.h>
#include <sys/stat.h>
#include <sys/resource.h>   // the custody of the secret (F-53): no core file, not dumpable
#include <sys/prctl.h>
#include <thread>
#include <atomic>
#endif

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <map>
#include <memory>
#include <cmath>
#include <algorithm>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>
#include <sstream>

using namespace igneum;

namespace {

std::string hex(const uint8_t* p, size_t n) {
    static const char* d = "0123456789abcdef";
    std::string s;
    for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; }
    return s;
}

bool unhex(const std::string& s, uint8_t* out, size_t n) {
    if (s.size() != 2 * n) return false;
    auto v = [](char c) -> int {
        if (c >= '0' && c <= '9') return c - '0';
        if (c >= 'a' && c <= 'f') return c - 'a' + 10;
        if (c >= 'A' && c <= 'F') return c - 'A' + 10;
        return -1;
    };
    for (size_t i = 0; i < n; ++i) {
        int a = v(s[2 * i]), b = v(s[2 * i + 1]);
        if (a < 0 || b < 0) return false;
        out[i] = static_cast<uint8_t>(a * 16 + b);
    }
    return true;
}

// Flat JSON field reader: the writer is ours (ignpow-vectors, the supervisor).
std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t p = line.find(k);
    if (p == std::string::npos) { k = std::string("\"") + key + "\": "; p = line.find(k); if (p == std::string::npos) return ""; }
    p += k.size();
    while (p < line.size() && line[p] == ' ') ++p;
    if (p >= line.size()) return "";
    if (line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    size_t q = line.find_first_of(",}", p);
    return line.substr(p, q - p);
}

uint64_t u64(const std::string& s) { return std::strtoull(s.c_str(), nullptr, 10); }

// protocol v0's version field is at most 32 bytes (Ember): the reference build's sha8; the whole build string rides in "build"
std::string build_sha8() {
    const std::string b = ignpow_build_string();
    const size_t p = b.find("build ");
    return p == std::string::npos ? std::string("unknown") : b.substr(p + 6, 8);
}

// A string field whose value is itself JSON text (the configure message's params document): backslash escapes undone.
std::string escaped_field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t p = line.find(k);
    if (p == std::string::npos) { k = std::string("\"") + key + "\": "; p = line.find(k); if (p == std::string::npos) return ""; }
    p += k.size();
    while (p < line.size() && line[p] == ' ') ++p;
    if (p < line.size() && line[p] == '{') {   // the document as a JSON object (Ember's configure), not an escaped string: the balanced text
        int depth = 0; bool instr = false;
        for (size_t i = p; i < line.size(); ++i) {
            const char c = line[i];
            if (instr) { if (c == '\\') ++i; else if (c == '"') instr = false; continue; }
            if (c == '"') instr = true;
            else if (c == '{') ++depth;
            else if (c == '}') { if (--depth == 0) return line.substr(p, i - p + 1); }
        }
        return "";
    }
    if (p >= line.size() || line[p] != '"') return "";
    std::string out;
    for (size_t i = p + 1; i < line.size(); ++i) {
        const char c = line[i];
        if (c == '\\' && i + 1 < line.size()) { out += line[++i]; continue; }
        if (c == '"') return out;
        out += c;
    }
    return "";
}

std::string json_escape(const std::string& s) {
    std::string o;
    for (char c : s) { if (c == '"' || c == '\\') o += '\\'; if (c == '\n') { o += "\\n"; continue; } o += c; }
    return o;
}

// protocol v0: telemetry.throttle is a list of reason words (the product team, 10 Oct 2026 22:4x UK), omitted when nothing throttles;
// NVML's clocksThrottleReasons mask mapped: 0x4 sw_power, 0x8 hw_slowdown, 0x20 and 0x40 thermal, 0x80 power (the HW brake),
// 0x10 sync_boost; 0x1 (idle) and 0x2 (app clocks) are not throttles. The raw mask rides in throttle_mask for the harness.
std::string throttle_json(unsigned long long m) {
    std::string out;
    auto add = [&](const char* w) { if (!out.empty()) out += ", "; out += "\""; out += w; out += "\""; };
    if (m & 0x4ull) add("sw_power");
    if (m & 0x8ull) add("hw_slowdown");
    if (m & 0x10ull) add("sync_boost");
    if (m & 0x60ull) add("thermal");
    if (m & 0x80ull) add("power");
    if (m & 0x100ull) add("display_clock");
    return out.empty() ? std::string() : ", \"throttle\": [" + out + "]";
}
std::string telemetry_json(const telemetry& t) {
    char b[512];
    std::snprintf(b, sizeof b,
                  "{\"ok\": %s, \"power_w\": %.1f, \"power_limit_w\": %.1f, \"temp_c\": %u, \"core_mhz\": %u, \"mem_mhz\": %u, "
                  "\"memory_used_bytes\": \"%llu\", \"memory_total_bytes\": \"%llu\", \"throttle_mask\": \"%llu\"%s}",
                  t.ok ? "true" : "false", t.power_w, t.power_limit_w, t.temp_c, t.core_mhz, t.mem_mhz,
                  (unsigned long long)t.mem_used, (unsigned long long)t.mem_total, t.throttle, throttle_json(t.throttle).c_str());
    return b;
}

int selftest() {
    size_t n = 0;
    int st = ignpow_vectors_check(nullptr, &n);
    std::fprintf(stderr, "%s\nreference self-test: %s (%zu vectors)\n", ignpow_build_string(), st == IGNPOW_OK ? "ok" : "FAILED", n);
    return st == IGNPOW_OK ? 0 : 3;
}

int device_arg(int argc, char** argv) {
    for (int i = 1; i + 1 < argc; ++i) {
        if (std::strcmp(argv[i], "--device") == 0) return std::atoi(argv[i + 1]);
        if (std::strcmp(argv[i], "--device-name") == 0) {  // a machine with several cards: pick by name
            const int d = engine::find_device(argv[i + 1]);
            if (d < 0) { std::fprintf(stderr, "no CUDA device named like %s\n", argv[i + 1]); std::exit(6); }
            std::fprintf(stderr, "device-name %s -> ordinal %d\n", argv[i + 1], d);
            return d;
        }
    }
    return 0;
}

// --params '<json>' or, where a launcher cannot carry quotes (the Windows agent's Start-Process drops them),
// repeatable --param key=value pairs assembled into the same document (integers as integers).
std::string g_params_doc;
const char* params_arg(int argc, char** argv) {
    for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], "--params") == 0) return argv[i + 1];
    std::string doc;
    for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], "--param") == 0) {
        const std::string kv = argv[i + 1];
        const size_t eq = kv.find('=');
        if (eq == std::string::npos) continue;
        const std::string k = kv.substr(0, eq), v = kv.substr(eq + 1);
        // an integer: digits only, no leading zero (but "0" itself), at most 19 digits; an all-digit 64-hex program_anchor is a STRING
        // (the KO's rig jobs refused the document when it was read as a number, 15 Oct 2026 13:37Z: owned by the GPU team)
        const bool num = !v.empty() && v.size() <= 19 && v.find_first_not_of("0123456789") == std::string::npos && (v.size() == 1 || v[0] != '0');
        doc += (doc.empty() ? "{" : ", ") + std::string("\"") + k + "\": " + (num ? v : "\"" + v + "\"");
    }
    if (!doc.empty()) g_params_doc = doc + "}";
    return g_params_doc.c_str();
}

const char* tune_arg(int argc, char** argv) {  // --tune read=2,minblocks=4,slots=8192 (TUNING-R0A.md)
    for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], "--tune") == 0) return argv[i + 1];
    return "";
}

const char* algo_arg(int argc, char** argv) {
    for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], "--algo") == 0) return argv[i + 1];
    return IGNPOW_ALGO_KAWPOW_094;
}
// The tune every verb applies: --tune when given; otherwise, for igneumhash, the ratified 5090 knee (words=1, 65536 slots, the batched tail at
// N=2). One default for bench, vectors, soak, grind and protocol: the bench path kept the engine's old default (4096 slots, no tail) after
// protocol mode got the knee, and the 5060 Ti's first Windows row read 19.51 MH/s on W128 against 45.65 at the knee (11 Oct 2026 06:1x UK).
// Other candidates keep an empty tune (their keys are their own).
static const char* kKneeTune = "words=1,slots=65536,tail=2";
std::string tune_default(int argc, char** argv) {
    std::string t = tune_arg(argc, argv);
    if (t.empty() && std::strcmp(algo_arg(argc, argv), "igneumhash") == 0) t = kKneeTune;
    return t;
}

// The seal count's options on bench and split (GUARD-C-CLOSURE.md 1.4's heights-lost count, the project's order of 11 Oct 2026): --header <hex64>
// and --target <hex64> replace the bench's fixed header and 2^240 target (a devnet template's header hash and D_seal), --until-hit 1 stops
// the window at the first hit at or under the target and prints a `seal` row (sealed, the seconds to it, the attempts) before the verb's
// own row; --nonce-start <n> sets the walk's first nonce. Without them every verb reads as before.
struct seal_opts {
    bool until_hit = false, header_set = false, target_set = false;
    uint8_t header[32], target[32];
    uint64_t nonce_start = 0; bool nonce_set = false;
};
static bool unhex32(const std::string& s, uint8_t* out) {
    std::string t = s; if (t.rfind("0x", 0) == 0) t = t.substr(2);
    if (t.size() != 64) return false;
    for (int i = 0; i < 32; ++i) { unsigned v; if (std::sscanf(t.c_str() + 2 * i, "%2x", &v) != 1) return false; out[i] = uint8_t(v); }
    return true;
}
seal_opts seal_args(int argc, char** argv) {
    seal_opts o;
    for (int i = 1; i + 1 < argc; ++i) {
        if (std::strcmp(argv[i], "--header") == 0) o.header_set = unhex32(argv[i + 1], o.header);
        else if (std::strcmp(argv[i], "--target") == 0) o.target_set = unhex32(argv[i + 1], o.target);
        else if (std::strcmp(argv[i], "--until-hit") == 0) o.until_hit = std::atoi(argv[i + 1]) != 0;
        else if (std::strcmp(argv[i], "--nonce-start") == 0) { o.nonce_start = u64(argv[i + 1]); o.nonce_set = true; }
    }
    return o;
}
static void print_seal(bool sealed, double seal_s, uint64_t attempts, const hit* h) {
    std::printf("{\"seal\": {\"sealed\": %s, \"seal_s\": %.3f, \"attempts\": \"%llu\", \"nonce\": \"%llu\"}}\n", sealed ? "true" : "false", seal_s,
                (unsigned long long)attempts, (unsigned long long)(h ? h->nonce : 0));
    std::fflush(stdout);
}

void print_device(const engine& e) {
    const auto& d = e.info();
    std::fprintf(stderr, "device %d: %s cc %d.%d memory %llu bytes pci %s driver %d arch %s%s\n", d.ordinal, d.name.c_str(),
                 d.cc_major, d.cc_minor, (unsigned long long)d.total_mem, d.pci.c_str(), d.driver_version,
                 d.arch_option.c_str(), d.ptx_fallback ? " (PTX fallback)" : "");
}

// precompile --algo igneumhash <params> --archs 75,80,86,89,120: the module for the document compiled for every named architecture
// into the cubin cache (IGNEUM_WORKER_CACHE), so a first run on any card finds its cubin (the product team's no-JIT form, 12 Oct 2026);
// needs one CUDA device for the context, compiles for the others by NVRTC's -arch alone
int cmd_precompile(int argc, char** argv) {
    std::string archs = "75,80,86,89,120";
    for (int i = 1; i + 1 < argc; ++i) if (!std::strcmp(argv[i], "--archs")) archs = argv[i + 1];
    engine e;
    e.init(device_arg(argc, argv), algo_arg(argc, argv), 256ull << 20, params_arg(argc, argv));
    std::printf("{\"precompile\": {\"device\": \"%s\", \"own_arch\": \"%s\", \"archs\": [", e.info().name.c_str(), e.info().arch_option.c_str());
    size_t pos = 0; bool first = true;
    while (pos < archs.size()) {
        size_t end = archs.find(',', pos); if (end == std::string::npos) end = archs.size();
        const int sm = std::atoi(archs.substr(pos, end - pos).c_str()); pos = end + 1;
        if (sm <= 0) continue;
        try { e.precompile_arch(sm); std::printf("%s{\"sm\": %d, \"ok\": true}", first ? "" : ", ", sm); }
        catch (const std::exception& ex) { std::printf("%s{\"sm\": %d, \"ok\": false, \"error\": \"%s\"}", first ? "" : ", ", sm, json_escape(ex.what()).c_str()); }
        first = false; std::fflush(stdout);
    }
    std::printf("], \"cache\": \"%s\"}}\n", json_escape(std::getenv("IGNEUM_WORKER_CACHE") ? std::getenv("IGNEUM_WORKER_CACHE") : "").c_str());
    return 0;
}

int cmd_devices() {
    int n = engine::device_count();
    std::printf("{\"devices\": %d}\n", n);
    for (int i = 0; i < n; ++i) {
        try { engine e; e.init(i, IGNPOW_ALGO_KAWPOW_094); print_device(e);
              budget b = e.budget_for(1);
              std::printf("{\"device\": \"cuda:%d\", \"name\": \"%s\", \"memory_bytes\": \"%llu\", \"free_bytes\": \"%llu\", \"arch\": \"%s\"}\n", i,
                          e.info().name.c_str(), (unsigned long long)b.total, (unsigned long long)b.free_before, e.info().arch_option.c_str());
        } catch (const std::exception& ex) { std::fprintf(stderr, "device %d: %s\n", i, ex.what()); }
    }
    return 0;
}

struct vec { uint64_t height = 0, nonce = 0, nonce_count = 0, min_nonce = 0, shares = 0; std::string header, mix, hash, share_target, min_hash; };

bool read_vectors(const std::string& path, std::string& cls, std::vector<vec>& vs) {
    std::ifstream in(path);
    if (!in) return false;
    std::string line;
    while (std::getline(in, line)) {
        if (line.find("\"class\"") != std::string::npos) cls = field(line, "class");
        else if (line.find("{\"height\"") != std::string::npos) {
            vec v;
            v.height = u64(field(line, "height")); v.nonce = u64(field(line, "nonce")); v.header = field(line, "header");
            v.mix = field(line, "mix"); v.hash = field(line, "hash"); v.nonce_count = u64(field(line, "nonce_count"));
            v.share_target = field(line, "share_target"); v.min_nonce = u64(field(line, "min_nonce")); v.min_hash = field(line, "min_hash");
            v.shares = u64(field(line, "shares"));
            vs.push_back(v);
        }
    }
    return true;
}

int cmd_vectors(int argc, char** argv) {
    if (argc < 3) return 2;
    const std::string dir = argv[2];
    engine e;
    e.set_tune(tune_default(argc, argv));
    e.init(device_arg(argc, argv), algo_arg(argc, argv), 256ull << 20, params_arg(argc, argv));
    print_device(e);
    const char* classes[] = {"unseen", "nonce-boundary", "transitions", "tails", "keyed"};   // keyed (version 8 bit 10): rows under the test secret, checked as mix and hash through the prover's path
    size_t total = 0, bad = 0, refused = 0, faults = 0;  // refused: the card has no room (an expected outcome); faults: any other refusal
    for (const char* cls_name : classes) {
        std::string cls; std::vector<vec> vs;
        if (!read_vectors(dir + "/" + cls_name + ".json", cls, vs)) { std::fprintf(stderr, "no %s.json\n", cls_name); continue; }
        size_t cbad = 0, cref = 0;
        for (const vec& v : vs) {
            std::string reason; double ms = 0; uint64_t bytes = 0;
            const bool had = e.has_dataset();
            const uint64_t before = e.current_epoch();
            if (!e.prepare(v.height, reason, &ms, &bytes)) {
                const bool memory = reason.rfind("dataset does not fit", 0) == 0;
                if (memory) ++cref; else ++faults;
                std::printf("{\"class\": \"%s\", \"height\": %llu, \"result\": \"%s\", \"reason\": \"%s\"}\n", cls.c_str(), (unsigned long long)v.height, memory ? "refused" : "fault", json_escape(reason).c_str());
                continue;
            }
            if (!had || before != e.current_epoch())
                std::printf("{\"prepared\": {\"epoch\": %llu, \"ms\": %.0f, \"bytes\": \"%llu\", \"mode\": \"cold\"}}\n", (unsigned long long)e.current_epoch(), ms, (unsigned long long)bytes);
            uint8_t header[32], target[32];
            unhex(v.header, header, 32);
            std::vector<hit> hits;
            if (cls == "tails") {
                unhex(v.share_target, target, 32);
                e.search(v.height, header, target, v.nonce, v.nonce_count, hits);
                uint8_t best[32]; std::memset(best, 0xff, 32); uint64_t best_n = 0;
                for (const hit& h : hits) if (std::memcmp(h.hash, best, 32) < 0) { std::memcpy(best, h.hash, 32); best_n = h.nonce; }
                if (hits.empty()) {
                    // no share in the range: the minimum hash of the range comes from every hash, in search_all chunks
                    for (uint64_t off = 0; off < v.nonce_count; off += (1u << 20)) {
                        const uint32_t n = static_cast<uint32_t>(std::min<uint64_t>(1u << 20, v.nonce_count - off));
                        std::vector<hit> all;
                        e.search_all(v.height, header, v.nonce + off, n, all);
                        for (const hit& h : all) if (std::memcmp(h.hash, best, 32) < 0) { std::memcpy(best, h.hash, 32); best_n = h.nonce; }
                    }
                }
                const bool ok = hits.size() == v.shares && best_n == v.min_nonce && hex(best, 32) == v.min_hash;
                if (!ok) ++cbad;
                std::printf("{\"class\": \"tails\", \"nonce_count\": \"%llu\", \"shares\": %zu, \"expected_shares\": %llu, \"min_nonce\": \"%llu\", \"expected_min_nonce\": \"%llu\", \"result\": \"%s\"}\n",
                            (unsigned long long)v.nonce_count, hits.size(), (unsigned long long)v.shares, (unsigned long long)best_n, (unsigned long long)v.min_nonce, ok ? "match" : "MISMATCH");
            } else {
                std::memset(target, 0xff, 32);
                e.search(v.height, header, target, v.nonce, 1, hits);
                const bool ok = hits.size() == 1 && hits[0].nonce == v.nonce && hex(hits[0].mix, 32) == v.mix && hex(hits[0].hash, 32) == v.hash;
                if (!ok) {
                    ++cbad;
                    std::printf("{\"class\": \"%s\", \"height\": %llu, \"nonce\": \"%llu\", \"result\": \"MISMATCH\", \"got_mix\": \"%s\", \"got_hash\": \"%s\", \"hits\": %zu}\n",
                                cls.c_str(), (unsigned long long)v.height, (unsigned long long)v.nonce, hits.empty() ? "" : hex(hits[0].mix, 32).c_str(), hits.empty() ? "" : hex(hits[0].hash, 32).c_str(), hits.size());
                }
            }
        }
        std::printf("{\"class\": \"%s\", \"vectors\": %zu, \"mismatches\": %zu, \"refused\": %zu}\n", cls.c_str(), vs.size(), cbad, cref);
        total += vs.size(); bad += cbad; refused += cref;
    }
    // verdict: exact only when every vector was checked and agreed; a memory refusal is "refused", anything else a fault
    // a set with no vectors read is a fault, never exact (a job on pc2 read an absent set as "exact" on zero vectors, 10 Oct 2026 17:3x UK)
    if (total == 0) ++faults;
    const char* verdict = bad ? "mismatch" : faults ? "fault" : refused ? (refused == total ? "refused" : "partial") : "exact";
    std::printf("{\"summary\": {\"device\": \"%s\", \"arch\": \"%s\", \"vectors\": %zu, \"checked\": %zu, \"mismatches\": %zu, \"refused\": %zu, \"faults\": %zu, \"verdict\": \"%s\", \"notes\": \"%s\"}}\n",
                e.info().name.c_str(), e.info().arch_option.c_str(), total, total - refused - faults, bad, refused, faults, verdict, json_escape(e.build_notes()).c_str());
    return bad ? 1 : faults ? 3 : refused ? 4 : 0;
}

// soak: sustained operation driven by the worker itself, for a machine that gets one binary and no python (the project's
// PCs through the bench agent). The same shape as jobd's run (docs/gpu-a/WORKER-PROTOCOL.md, pow/harness/jobd.py): a job
// every job_ms with the height advancing, the share target 2^(256 - bits), every share verified by the linked CPU reference
// (never the device), stale decided at receipt against the superseded job, a forced reconnect every N seconds by tearing the
// engine down and initialising it again (the device context, the module, the dataset), a power-limit step every M seconds
// through nvidia-smi (applied or refused, recorded), a stat line per second with the tallies, a checkpoint record every
// verify interval, and the run record at the end in jobd's field names so the same table reads it.
//   igneum-worker-cuda soak --seconds S --algo A [--param k=v ...] [--height H] [--share-bits 20] [--job-ms 15000]
//       [--advance-height 10] [--reconnect-every-s N] [--power-step-every-s M] [--power-limits default,0.8,default,0.6]
//       [--verify-every-s V] [--out soak.json] [--device N] [--device-name X] [--gpu-index I]
// a deterministic 32-byte header per soak job from its number (splitmix64; the header's content is not part of any verdict)
static void blake_like_header(const std::string& seed, uint8_t out[32]) {
    uint64_t x = 0x9E3779B97F4A7C15ull; for (char c : seed) x = (x ^ (uint8_t)c) * 0x100000001B3ull;
    for (int i = 0; i < 4; ++i) { x += 0x9E3779B97F4A7C15ull; uint64_t z = x; z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ull; z = (z ^ (z >> 27)) * 0x94D049BB133111EBull; z ^= z >> 31; for (int b = 0; b < 8; ++b) out[8 * i + b] = (uint8_t)(z >> (8 * b)); }
}

int cmd_soak(int argc, char** argv) {
    auto opt = [&](const char* k, const char* d) -> const char* { for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], k) == 0) return argv[i + 1]; return d; };
    const uint64_t seconds = u64(opt("--seconds", "0"));
    if (!seconds) { std::fprintf(stderr, "soak: --seconds required\n"); return 2; }
    uint64_t height = u64(opt("--height", "1"));
    const int bits = std::atoi(opt("--share-bits", "20"));
    const double job_s = std::atoi(opt("--job-ms", "15000")) / 1000.0;
    const uint64_t advance = u64(opt("--advance-height", "10"));
    const uint64_t reconnect_every = u64(opt("--reconnect-every-s", "0"));
    const uint64_t power_every = u64(opt("--power-step-every-s", "0"));
    const uint64_t verify_every = u64(opt("--verify-every-s", "600"));
    const std::string out_path = opt("--out", "soak.json");
    const std::string limits_s = opt("--power-limits", "default,0.8,default,0.6");
    const int gpu_index = std::atoi(opt("--gpu-index", "0"));
    const char* algo = algo_arg(argc, argv);
    const char* params = params_arg(argc, argv);
    if (params && !*params) params = nullptr;
    std::vector<std::string> limits;
    { std::string cur; for (char c : limits_s) { if (c == ',') { if (!cur.empty()) limits.push_back(cur); cur.clear(); } else cur += c; } if (!cur.empty()) limits.push_back(cur); }

    // the share target: 2^(256 - bits) as 32 big-endian bytes
    uint8_t target[32]; std::memset(target, 0, 32);
    { const int bit = 256 - bits; target[31 - bit / 8] = static_cast<uint8_t>(1u << (bit % 8)); }
    const double share_difficulty = std::ldexp(1.0, bits);

    std::unique_ptr<engine> e;
    std::string worker_err;
    auto bring_up = [&]() -> bool {
        try {
            e.reset(new engine());
            e->set_tune(tune_default(argc, argv));
            e->init(device_arg(argc, argv), algo, 256ull << 20, params ? params : "");
            return true;
        } catch (const std::exception& ex) { worker_err = ex.what(); e.reset(); return false; }
    };
    if (!bring_up()) { std::fprintf(stderr, "soak: %s\n", worker_err.c_str()); return 3; }

    // the CPU reference for verification: one light context per epoch
    std::vector<std::pair<uint64_t, ignpow_ctx*>> ref;
    auto ref_for = [&](uint64_t h) -> ignpow_ctx* {
        int st = 0; const uint64_t ep = ignpow_epoch_of(algo, params, h, &st);
        if (st != IGNPOW_OK) return nullptr;
        for (auto& c : ref) if (c.first == ep) return c.second;
        ignpow_ctx* c = ignpow_ctx_create(algo, params, h, 0, &st);
        if (c) ref.emplace_back(ep, c);
        return c;
    };

    struct job_t { std::string id; uint64_t height; uint8_t header[32]; double sent, superseded; bool has_superseded; };
    struct share_t { double t; size_t job; uint64_t nonce; uint8_t mix[32], hash[32]; int verdict; };  // verdict 0 unverified, 1 accepted, 2 stale, 3 rejected-mix, 4 rejected-target, 5 rejected-ref
    std::vector<job_t> jobs; std::vector<share_t> shares;
    uint64_t accepted = 0, stale = 0, rejected = 0, verified_upto = 0, stale_at_receipt = 0;
    std::map<std::string, uint64_t> rejected_by;
    struct ev_t { double t; std::string line; }; std::vector<ev_t> events;
    uint64_t hashes = 0, reconnects = 0, transitions = 0; double wh = 0; unsigned temp_min = 1000, temp_max = 0;
    std::map<std::string, double> downtime; // dataset_build, reconnect, power_step
    std::vector<std::string> stats_tail;  // the last 120 stat lines for the checkpoint
    std::vector<std::string> power_steps;

    auto now_s = [t0 = std::chrono::steady_clock::now()]() { return std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(); };
    auto evline = [&](const std::string& l) { events.push_back({now_s(), l}); std::printf("%s\n", l.c_str()); std::fflush(stdout); };
    auto smi = [&](const std::string& args) -> std::pair<int, std::string> {
        const std::string cmd = "nvidia-smi " + args + " 2>&1";
#ifdef _WIN32
        std::string out; FILE* f = _popen(cmd.c_str(), "r"); if (!f) return {-1, "no nvidia-smi"};
        char buf[256]; while (fgets(buf, sizeof buf, f)) out += buf; const int rc = _pclose(f);
#else
        std::string out; FILE* f = popen(cmd.c_str(), "r"); if (!f) return {-1, "no nvidia-smi"};
        char buf[256]; while (fgets(buf, sizeof buf, f)) out += buf; const int rc = pclose(f);
#endif
        if (out.size() > 300) out.resize(300);
        return {rc, out};
    };
    double default_w = 0;
    if (power_every) { auto r = smi("-i " + std::to_string(gpu_index) + " --query-gpu=power.default_limit --format=csv,noheader,nounits"); if (r.first == 0) default_w = std::atof(r.second.c_str()); }

    auto verify = [&]() {
        for (size_t i = verified_upto; i < shares.size(); ++i) {
            share_t& s = shares[i]; const job_t& j = jobs[s.job];
            ignpow_ctx* c = ref_for(j.height);
            uint8_t mix[32], digest[32];
            const int st = c ? ignpow_hash(c, j.height, j.header, s.nonce, mix, digest) : IGNPOW_E_MEMORY;
            if (st != IGNPOW_OK) { s.verdict = 5; ++rejected; ++rejected_by["ref-code-" + std::to_string(st)]; continue; }
            if (std::memcmp(mix, s.mix, 32) || std::memcmp(digest, s.hash, 32)) { s.verdict = 3; ++rejected; ++rejected_by["mix"]; continue; }
            if (std::memcmp(s.hash, target, 32) > 0) { s.verdict = 4; ++rejected; ++rejected_by["target"]; continue; }
            if (j.has_superseded && s.t > j.superseded) { s.verdict = 2; ++stale; continue; }
            s.verdict = 1; ++accepted;
        }
        verified_upto = shares.size();
    };
    auto write_record = [&](bool checkpoint) {
        std::string path = checkpoint ? out_path + ".checkpoint.json" : out_path;
        std::ofstream o(path + ".tmp");
        o << "{\"checkpoint\": " << (checkpoint ? "true" : "false") << ", \"seconds\": " << now_s() << ", \"accepted\": " << accepted << ", \"rejected\": " << rejected
          << ", \"stale\": " << stale << ", \"submitted\": " << shares.size() << ", \"stale_at_receipt\": " << stale_at_receipt
          << ", \"share_difficulty_hashes\": " << share_difficulty << ", \"hashes_reported\": " << hashes << ", \"telemetry_kwh\": " << wh / 1000.0
          << ", \"reconnects\": " << reconnects << ", \"epoch_transitions\": " << transitions << ", \"jobs\": " << jobs.size()
          << ", \"height_first\": " << (jobs.empty() ? height : jobs.front().height) << ", \"height_last\": " << (jobs.empty() ? height : jobs.back().height)
          << ", \"gpu_temp_band_c\": [" << (temp_max ? temp_min : 0) << ", " << temp_max << "], \"worker\": \"igneum-worker-cuda soak\", \"algo\": \"" << algo
          << "\", \"params\": " << (params ? params : "null") << ", \"device\": \"" << json_escape(e ? e->info().name : "") << "\", \"build\": \"" << json_escape(ignpow_build_string()) << "\"";
        o << ", \"rejected_by_reason\": {"; bool first = true; for (auto& kv : rejected_by) { o << (first ? "" : ", ") << "\"" << kv.first << "\": " << kv.second; first = false; } o << "}";
        o << ", \"downtime_seconds_by_cause\": {"; first = true; for (auto& kv : downtime) { o << (first ? "" : ", ") << "\"" << kv.first << "\": " << kv.second; first = false; } o << "}";
        o << ", \"power_steps\": [" ; for (size_t i = 0; i < power_steps.size(); ++i) o << (i ? ", " : "") << power_steps[i]; o << "]";
        o << ", \"events\": ["; for (size_t i = 0; i < events.size(); ++i) o << (i ? ", " : "") << "{\"t\": " << events[i].t << ", \"line\": " << "\"" << json_escape(events[i].line) << "\"}"; o << "]";
        if (!checkpoint) {
            const double expected = share_difficulty > 0 ? hashes / share_difficulty : 0;
            const double ratio = expected > 0 ? (accepted + stale) / expected : 0;
            o << ", \"expected_shares\": " << expected << ", \"share_ratio\": " << ratio << ", \"share_check\": \"" << (expected < 20 ? "low-count" : (ratio >= 0.5 && ratio <= 2.0 ? "ok" : "DISAGREE")) << "\"";
            o << ", \"jobs_issued\": ["; for (size_t i = 0; i < jobs.size(); ++i) { const job_t& j = jobs[i]; o << (i ? ", " : "") << "{\"job_id\": \"" << j.id << "\", \"height\": " << j.height << ", \"header\": \"" << hex(j.header, 32) << "\", \"sent\": " << j.sent << ", \"superseded\": " << (j.has_superseded ? std::to_string(j.superseded) : "null") << "}"; } o << "]";
            static const char* vn[] = {"unverified", "accepted", "stale", "rejected-mix", "rejected-target", "rejected-ref"};
            o << ", \"shares\": ["; for (size_t i = 0; i < shares.size(); ++i) { const share_t& s = shares[i]; o << (i ? ", " : "") << "{\"t\": " << s.t << ", \"job_id\": \"" << jobs[s.job].id << "\", \"nonce\": \"" << s.nonce << "\", \"hash\": \"" << hex(s.hash, 32) << "\", \"verdict\": \"" << vn[s.verdict] << "\"}"; } o << "]";
        } else {
            o << ", \"stats_tail\": ["; for (size_t i = 0; i < stats_tail.size(); ++i) o << (i ? ", " : "") << stats_tail[i]; o << "]";
        }
        o << "}\n"; o.close();
        std::rename((path + ".tmp").c_str(), path.c_str());
    };

    // the run
    double next_job = 0, next_reconnect = reconnect_every ? (double)reconnect_every : -1, next_power = power_every ? (double)power_every : -1, next_verify = (double)verify_every, next_stat = 1.0;
    size_t cur = 0; bool have_job = false; uint64_t nonce = 0; uint64_t job_n = 0; size_t limit_i = 0;
    uint64_t since = 0; double last_stat_t = 0; uint64_t cur_epoch = UINT64_MAX; double build_total = 0;
    std::vector<hit> hits;
    while (now_s() < (double)seconds) {
        const double t = now_s();
        if (next_verify > 0 && t >= next_verify) { verify(); write_record(true); next_verify += verify_every; }
        if (next_power > 0 && t >= next_power) {
            const std::string lim = limits[limit_i++ % limits.size()];
            double watts = default_w > 0 ? (lim == "default" ? default_w : (std::atof(lim.c_str()) <= 1.0 ? default_w * std::atof(lim.c_str()) : std::atof(lim.c_str()))) : 0;
            std::pair<int, std::string> r = watts > 0 ? smi("-i " + std::to_string(gpu_index) + " -pl " + std::to_string((int)(watts + 0.5))) : std::make_pair(-1, std::string("no default limit read"));
            const std::string rec = "{\"t\": " + std::to_string(t) + ", \"limit\": \"" + lim + "\", \"watts\": " + std::to_string(watts) + ", \"rc\": " + std::to_string(r.first) + ", \"result\": \"" + (r.first == 0 ? "applied" : "refused by the host") + "\"}";
            power_steps.push_back(rec); evline("{\"t\": \"event\", \"event\": \"power_step\", \"record\": " + rec + ", \"output\": \"" + json_escape(r.second) + "\"}");
            next_power += power_every;
        }
        if (next_reconnect > 0 && t >= next_reconnect) {
            evline("{\"t\": \"event\", \"event\": \"reconnect\", \"phase\": \"disconnect\"}");
            const double td = now_s();
            e.reset(); cur_epoch = UINT64_MAX;
            if (!bring_up()) { evline("{\"t\": \"event\", \"event\": \"reconnect\", \"phase\": \"failed\", \"reason\": \"" + json_escape(worker_err) + "\"}"); break; }
            downtime["reconnect"] += now_s() - td; ++reconnects;
            evline("{\"t\": \"event\", \"event\": \"reconnect\", \"phase\": \"reconnected\"}");
            next_reconnect += reconnect_every;
        }
        if (t >= next_job) {
            ++job_n;
            job_t j; j.id = "j" + std::to_string(job_n); j.height = height; j.sent = t; j.superseded = 0; j.has_superseded = false;
            { std::string seedline = "soak:" + std::to_string(job_n); uint8_t h[32]; blake_like_header(seedline, h); std::memcpy(j.header, h, 32); }
            if (have_job) { jobs[cur].superseded = t; jobs[cur].has_superseded = true; }
            jobs.push_back(j); cur = jobs.size() - 1; have_job = true; nonce = (job_n << 40);
            height += advance; next_job += job_s;
        }
        if (!have_job) { continue; }
        const job_t& j = jobs[cur];
        int st = 0; const uint64_t ep = ignpow_epoch_of(algo, params, j.height, &st);
        if (ep != cur_epoch) {
            std::string reason; double ms = 0; uint64_t bytes = 0;
            const double tb = now_s();
            if (!e->prepare(j.height, reason, &ms, &bytes)) { evline("{\"t\": \"refuse\", \"height\": " + std::to_string(j.height) + ", \"reason\": \"" + json_escape(reason) + "\"}"); break; }
            downtime["dataset_build"] += now_s() - tb; build_total += ms; if (cur_epoch != UINT64_MAX) ++transitions; cur_epoch = ep;
            evline("{\"t\": \"prepared\", \"epoch\": " + std::to_string(ep) + ", \"ms\": " + std::to_string((long long)ms) + ", \"bytes\": \"" + std::to_string(bytes) + "\"}");
        }
        hits.clear();
        const uint64_t n = e->search(j.height, j.header, target, nonce, e->batch(), hits);
        nonce += n; hashes += n; since += n;
        const double tr = now_s();
        for (const hit& h : hits) {
            share_t s; s.t = tr; s.job = cur; s.nonce = h.nonce; std::memcpy(s.mix, h.mix, 32); std::memcpy(s.hash, h.hash, 32); s.verdict = 0;
            shares.push_back(s);
        }
        if (tr >= next_stat) {
            telemetry tl = e->read_telemetry();
            const double dt = tr - last_stat_t; last_stat_t = tr; next_stat = tr + 1.0;
            if (tl.ok) { wh += tl.power_w * dt / 3600.0; if (tl.temp_c) { temp_min = std::min(temp_min, tl.temp_c); temp_max = std::max(temp_max, tl.temp_c); } }
            char line[1024];
            std::snprintf(line, sizeof line, "{\"t\": \"stat\", \"t_run\": %.1f, \"job_id\": \"%s\", \"hashes\": \"%llu\", \"elapsed_ms\": %d, \"telemetry\": %s, \"tally\": {\"accepted\": %llu, \"stale\": %llu, \"rejected\": %llu, \"unverified\": %llu, \"stale_at_receipt\": %llu}}",
                          tr, j.id.c_str(), (unsigned long long)since, (int)(dt * 1000), telemetry_json(tl).c_str(), (unsigned long long)accepted, (unsigned long long)stale, (unsigned long long)rejected, (unsigned long long)(shares.size() - verified_upto), (unsigned long long)stale_at_receipt);
            std::printf("%s\n", line); std::fflush(stdout);
            stats_tail.push_back(line); if (stats_tail.size() > 120) stats_tail.erase(stats_tail.begin());
            since = 0;
        }
    }
    verify();
    write_record(false);
    for (auto& c : ref) ignpow_ctx_free(c.second);
    std::printf("{\"soak\": {\"seconds\": %.1f, \"accepted\": %llu, \"stale\": %llu, \"rejected\": %llu, \"reconnects\": %llu, \"epoch_transitions\": %llu, \"telemetry_wh\": %.3f, \"hashes\": \"%llu\", \"bytes_per_hash\": %llu, \"achieved_gbs\": %.2f, \"uj_per_hash\": %.3f, \"kernels\": %s}}\n",
                now_s(), (unsigned long long)accepted, (unsigned long long)stale, (unsigned long long)rejected, (unsigned long long)reconnects, (unsigned long long)transitions, wh, (unsigned long long)hashes,
                (unsigned long long)(e ? e->bytes_per_hash() : 0), e && now_s() > 0 ? hashes * (double)e->bytes_per_hash() / now_s() / 1e9 : 0.0, hashes ? wh * 3600.0 * 1e6 / hashes : 0.0, e ? e->kernels_json().c_str() : "[]");
    return 0;
}

// split <height> <seconds> --oracle host:port [--link-mbps L] [--rtt-ms R] [--conns N] [--count N] --algo igneumhash --param ...
// The renter of GUARD-C-CLOSURE.md 1.4 (O3's renter half): the walk on this card with the keyed rounds answered by a REMOTE oracle
// over the hash team's wire form (request u64 nonce LE || u32 i LE || m_i, 44 bytes; the answer 32 bytes, in request order,
// pipelined over N connections). The link is capped in software: an exchange of B payload bytes may not finish before
// B x 8 / L + RTT seconds after it began (the renter sleeps the remainder), so the row shows the link binding when it does.
// First a reference check (64 nonces: the renter's mix and hash against ignh_hash_keyed under the header's test secret, the
// oracle serving that secret), then windows of `count` nonces for `seconds`. One JSON row at the end.
#ifndef _WIN32
struct oracle_link {
    std::vector<int> fds;
    double link_bps = 0, rtt_s = 0;          // 0: no cap
    uint64_t bytes_out = 0, bytes_in = 0, requests = 0, exchanges = 0;
    double wire_s = 0, sleep_s = 0;         // the real exchange time and the emulated link's remainder
    bool connect(const std::string& hp, int n) {
        const size_t c = hp.rfind(':'); if (c == std::string::npos) return false;
        const std::string host = hp.substr(0, c), port = hp.substr(c + 1);
        addrinfo hints{}; hints.ai_socktype = SOCK_STREAM; addrinfo* ai = nullptr;
        if (getaddrinfo(host.c_str(), port.c_str(), &hints, &ai) != 0) return false;
        for (int i = 0; i < n; ++i) {
            int fd = -1;
            for (addrinfo* p = ai; p; p = p->ai_next) { fd = socket(p->ai_family, p->ai_socktype, p->ai_protocol); if (fd >= 0 && ::connect(fd, p->ai_addr, p->ai_addrlen) == 0) break; if (fd >= 0) { close(fd); fd = -1; } }
            if (fd < 0) { freeaddrinfo(ai); return false; }
            int one = 1; setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
            fds.push_back(fd);
        }
        freeaddrinfo(ai);
        return true;
    }
    static bool write_all(int fd, const uint8_t* p, size_t n) { while (n) { ssize_t w = ::send(fd, p, n, 0); if (w <= 0) return false; p += w; n -= size_t(w); } return true; }
    static bool read_all(int fd, uint8_t* p, size_t n) { while (n) { ssize_t r = ::recv(fd, p, n, 0); if (r <= 0) return false; p += r; n -= size_t(r); } return true; }
    // one exchange: count requests of slot i over the connections (a sender and a reader thread per connection)
    bool exchange(uint32_t i, uint64_t start, uint32_t count, uint32_t* m) {
        const auto t0 = std::chrono::steady_clock::now();
        const size_t nc = fds.size();
        std::atomic<bool> ok{true};
        std::vector<std::thread> th;
        for (size_t c = 0; c < nc; ++c) {
            const uint32_t g0 = uint32_t(size_t(count) * c / nc), g1 = uint32_t(size_t(count) * (c + 1) / nc);
            th.emplace_back([&, c, g0, g1]() {
                std::thread reader([&, c, g0, g1]() {
                    std::vector<uint8_t> buf(size_t(g1 - g0) * 32);
                    if (!read_all(fds[c], buf.data(), buf.size())) { ok = false; return; }
                    std::memcpy(m + size_t(g0) * 8, buf.data(), buf.size());
                });
                // the reference oracle's wire form (pow/ref/tools/bond-oracle.cpp): at round 0 the request is the nonce and i alone
                // (12 bytes; the oracle computes m_0 = S from the template it holds), else u64 nonce || u32 i || m_i (44 bytes)
                const size_t rl = i == 0 ? 12 : 44;
                std::vector<uint8_t> req(size_t(g1 - g0) * rl);
                for (uint32_t g = g0; g < g1; ++g) {
                    uint8_t* p = req.data() + size_t(g - g0) * rl;
                    const uint64_t nonce = start + g;
                    for (int k = 0; k < 8; ++k) p[k] = uint8_t(nonce >> (8 * k));
                    for (int k = 0; k < 4; ++k) p[8 + k] = uint8_t(i >> (8 * k));
                    if (rl == 44) std::memcpy(p + 12, m + size_t(g) * 8, 32);
                }
                if (!write_all(fds[c], req.data(), req.size())) ok = false;
                reader.join();
            });
        }
        for (auto& t : th) t.join();
        const double real = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        const uint64_t bo = uint64_t(count) * (i == 0 ? 12 : 44), bi = uint64_t(count) * 32;
        bytes_out += bo; bytes_in += bi; requests += count; ++exchanges; wire_s += real;
        if (link_bps > 0) {
            const double floor_s = double(bo + bi) * 8.0 / link_bps + rtt_s;
            if (real < floor_s) { std::this_thread::sleep_for(std::chrono::duration<double>(floor_s - real)); sleep_s += floor_s - real; }
        }
        return ok;
    }
};
int cmd_split(int argc, char** argv) {
    if (argc < 4) { std::fprintf(stderr, "split <height> <seconds> --oracle host:port [--link-mbps L] [--rtt-ms R] [--conns N] [--count N]\n"); return 2; }
    const uint64_t height = u64(argv[2]);
    const int seconds = std::atoi(argv[3]);
    auto opt = [&](const char* k, const char* d) -> std::string { for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], k) == 0) return argv[i + 1]; return d; };
    const std::string oracle_hp = opt("--oracle", "");
    const double link_mbps = std::atof(opt("--link-mbps", "0").c_str()), rtt_ms = std::atof(opt("--rtt-ms", "0").c_str());
    const int conns = std::atoi(opt("--conns", "4").c_str());
    const uint32_t count = static_cast<uint32_t>(std::min<uint64_t>(u64(opt("--count", "1048576")), 1u << 20));
    if (oracle_hp.empty()) { std::fprintf(stderr, "split: --oracle host:port\n"); return 2; }
    engine e;
    std::string tune = tune_default(argc, argv);
    if (tune.find("split=") == std::string::npos) tune += ",split=1";
    e.set_tune(tune);
    e.init(device_arg(argc, argv), algo_arg(argc, argv), 256ull << 20, params_arg(argc, argv));
    print_device(e);
    if (!e.keyed_document()) { std::printf("{\"refuse\": \"split needs a bit 10 document (keyed_rounds)\"}\n"); return 4; }
    std::string reason; double ms = 0; uint64_t bytes = 0;
    if (!e.prepare(height, reason, &ms, &bytes)) { std::printf("{\"refuse\": \"%s\"}\n", json_escape(reason).c_str()); return 4; }
    oracle_link link; link.link_bps = link_mbps * 1e6; link.rtt_s = rtt_ms / 1e3;
    if (!link.connect(oracle_hp, conns)) { std::printf("{\"refuse\": \"oracle %s unreachable\"}\n", json_escape(oracle_hp).c_str()); return 4; }
    uint8_t header[32], target[32];
    for (int i = 0; i < 32; ++i) header[i] = static_cast<uint8_t>(i * 7 + 1);   // the bench's header: the oracle serves its test secret
    const seal_opts so = seal_args(argc, argv);
    if (so.header_set) std::memcpy(header, so.header, 32);   // the count: a template's header hash, the oracle started on it (template --header)
    auto oracle = [&](uint32_t i, uint64_t start, uint32_t n, uint32_t* m) { if (!link.exchange(i, start, n, m)) throw std::runtime_error("the oracle link failed"); };
    // the reference check: 64 nonces through the split path against ignh_hash_keyed under the header's test secret
    size_t checked = 0, mismatches = 0;
    {
        std::memset(target, 0xff, 32);
        std::vector<hit> hits;
        e.split_search(height, header, target, 0, 64, oracle, hits);
        int st = 0;
        ignpow_ctx* ref = ignpow_ctx_create(algo_arg(argc, argv), params_arg(argc, argv), height, 0, &st);
        uint8_t sk[32]; ignh_test_sk(header, sk);
        std::vector<uint8_t> shares(size_t(e.keyed_rounds()) * 32);
        for (uint32_t n = 0; n < 64; ++n) {
            uint8_t mix[32], hash[32];
            if (!ref || ignh_hash_keyed(ref, height, header, n, sk, shares.data(), mix, hash) != IGNPOW_OK) { ++mismatches; continue; }
            const hit* h = nullptr; for (const hit& x : hits) if (x.nonce == n) h = &x;
            ++checked;
            if (!h || std::memcmp(h->mix, mix, 32) || std::memcmp(h->hash, hash, 32)) ++mismatches;
        }
        if (ref) ignpow_ctx_free(ref);
        std::printf("{\"split_check\": {\"nonces\": 64, \"checked\": %zu, \"mismatches\": %zu}}\n", checked, mismatches);
        std::fflush(stdout);
        if (mismatches) return 1;
    }
    std::memset(target, 0, 32); target[2] = 0x01;
    if (so.target_set) std::memcpy(target, so.target, 32);
    std::vector<hit> hits;
    uint64_t nonce = so.nonce_set ? so.nonce_start : (1u << 20), total = 0, since = 0;
    const auto t0 = std::chrono::steady_clock::now();
    auto last = t0;
    double energy_ws = 0;
    bool sealed = false; double seal_s = 0; hit seal_hit{};
    while (true) {
        const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (el >= seconds) break;
        hits.clear();
        e.split_search(height, header, target, nonce, count, oracle, hits);
        nonce += count; total += count; since += count;
        if (so.until_hit && !hits.empty()) { sealed = true; seal_s = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(); seal_hit = hits[0]; break; }
        const double dt = std::chrono::duration<double>(std::chrono::steady_clock::now() - last).count();
        if (dt >= 1.0) {
            telemetry t = e.read_telemetry();
            energy_ws += t.power_w * dt;
            std::printf("{\"t\": %.1f, \"mhs\": %.3f, \"hits\": %zu, \"wire_s\": %.2f, \"sleep_s\": %.2f, \"telemetry\": %s}\n", el, since / dt / 1e6, hits.size(), link.wire_s, link.sleep_s, telemetry_json(t).c_str());
            std::fflush(stdout);
            last = std::chrono::steady_clock::now(); since = 0;
        }
    }
    const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    const double bits = double(link.bytes_out + link.bytes_in) * 8.0;
    if (so.until_hit) print_seal(sealed, seal_s, total, sealed ? &seal_hit : nullptr);
    std::printf("{\"split\": {\"device\": \"%s\", \"build\": \"%s\", \"height\": %llu, \"seconds\": %.1f, \"attempts\": \"%llu\", \"mhs\": %.4f, \"keyed_rounds\": %u, \"count_per_batch\": %u, \"conns\": %d, "
                "\"oracle\": \"%s\", \"link_cap_mbps\": %.1f, \"rtt_added_ms\": %.2f, \"requests\": \"%llu\", \"exchanges\": \"%llu\", \"payload_bytes_out\": \"%llu\", \"payload_bytes_in\": \"%llu\", "
                "\"payload_bits_per_attempt\": %.1f, \"payload_mbps_achieved\": %.2f, \"requests_per_s\": %.0f, \"wire_s\": %.2f, \"sleep_s\": %.2f, \"telemetry_wh\": %.3f, \"uj_per_attempt\": %.3f, \"tune\": \"%s\", \"check\": {\"nonces\": 64, \"mismatches\": %zu}}}\n",
                e.info().name.c_str(), json_escape(ignpow_build_string()).c_str(), (unsigned long long)height, el, (unsigned long long)total, total / el / 1e6, e.keyed_rounds(), count, conns,
                json_escape(oracle_hp).c_str(), link_mbps, rtt_ms, (unsigned long long)link.requests, (unsigned long long)link.exchanges, (unsigned long long)link.bytes_out, (unsigned long long)link.bytes_in,
                total ? bits / double(total) : 0.0, bits / el / 1e6, link.requests / el, link.wire_s, link.sleep_s, energy_ws / 3600.0, total ? energy_ws * 1e6 / total : 0.0, json_escape(tune).c_str(), mismatches);
    for (int fd : link.fds) close(fd);
    return 0;
}
#else
int cmd_split(int, char**) { std::fprintf(stderr, "split: the renter row runs on Linux\n"); return 2; }
#endif

int cmd_bench(int argc, char** argv) {
    if (argc < 4) return 2;
    const uint64_t height = u64(argv[2]);
    const int seconds = std::atoi(argv[3]);
    engine e;
    e.set_tune(tune_default(argc, argv));
    e.init(device_arg(argc, argv), algo_arg(argc, argv), 256ull << 20, params_arg(argc, argv));
    print_device(e);
    budget b = e.budget_for(height);
    std::printf("{\"budget\": {\"total\": \"%llu\", \"free_before\": \"%llu\", \"cache\": \"%llu\", \"dataset\": \"%llu\", \"scratch\": \"%llu\", \"margin\": \"%llu\", \"fit_cold\": \"%llu\", \"fit_warm\": \"%llu\", \"can_cold\": %s, \"can_warm\": %s}}\n",
                (unsigned long long)b.total, (unsigned long long)b.free_before, (unsigned long long)b.cache, (unsigned long long)b.dataset, (unsigned long long)b.scratch,
                (unsigned long long)b.margin, (unsigned long long)b.fit_cold, (unsigned long long)b.fit_warm, b.can_cold ? "true" : "false", b.can_warm ? "true" : "false");
    std::string reason; double ms = 0; uint64_t bytes = 0;
    if (!e.prepare(height, reason, &ms, &bytes)) { std::printf("{\"refuse\": \"%s\"}\n", json_escape(reason).c_str()); return 4; }
    std::printf("{\"prepared\": {\"epoch\": %llu, \"ms\": %.0f, \"bytes\": \"%llu\", \"free_after\": \"%llu\"}}\n", (unsigned long long)e.current_epoch(), ms, (unsigned long long)bytes, (unsigned long long)e.free_bytes());
    uint8_t header[32], target[32];
    for (int i = 0; i < 32; ++i) header[i] = static_cast<uint8_t>(i * 7 + 1);
    std::memset(target, 0, 32); target[2] = 0x01;  // 2^240: about one hit per 65536 nonces, keeps the result path exercised
    const seal_opts so = seal_args(argc, argv);
    if (so.header_set) std::memcpy(header, so.header, 32);
    if (so.target_set) std::memcpy(target, so.target, 32);
    std::vector<hit> hits;
    uint64_t nonce = so.nonce_set ? so.nonce_start : 0, total = 0;
    const auto t0 = std::chrono::steady_clock::now();
    auto last = t0;
    uint64_t since = 0;
    double energy_ws = 0;
    bool sealed = false; double seal_s = 0; hit seal_hit{};
    while (true) {
        const auto now = std::chrono::steady_clock::now();
        const double el = std::chrono::duration<double>(now - t0).count();
        if (el >= seconds) break;
        hits.clear();
        const uint64_t n = e.search(height, header, target, nonce, e.batch(), hits);
        nonce += n; total += n; since += n;
        if (so.until_hit && !hits.empty()) { sealed = true; seal_s = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(); seal_hit = hits[0]; break; }
        const double dt = std::chrono::duration<double>(std::chrono::steady_clock::now() - last).count();
        if (dt >= 1.0) {
            telemetry t = e.read_telemetry();
            energy_ws += t.power_w * dt;
            std::printf("{\"t\": %.1f, \"mhs\": %.2f, \"hits\": %zu, \"telemetry\": %s}\n", el, since / dt / 1e6, hits.size(), telemetry_json(t).c_str());
            std::fflush(stdout);
            last = std::chrono::steady_clock::now(); since = 0;
        }
    }
    const double el = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    const double bph = (double)e.bytes_per_hash();
    if (so.until_hit) print_seal(sealed, seal_s, total, sealed ? &seal_hit : nullptr);
    std::printf("{\"bench\": {\"device\": \"%s\", \"arch\": \"%s\", \"algo\": \"%s\", \"build\": \"%s\", \"height\": %llu, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.2f, \"telemetry_wh\": %.3f, \"batch\": %u, "
                "\"bytes_per_hash\": %.0f, \"achieved_gbs\": %.2f, \"uj_per_hash\": %.3f, \"tune\": \"%s\", \"slots\": %u, \"kernels\": %s, \"notes\": \"%s\"}}\n",
                e.info().name.c_str(), e.info().arch_option.c_str(), algo_arg(argc, argv), json_escape(ignpow_build_string()).c_str(), (unsigned long long)height, el, (unsigned long long)total, total / el / 1e6, energy_ws / 3600.0, e.batch(),
                bph, total * bph / el / 1e9, total ? energy_ws * 1e6 / total : 0.0, json_escape(tune_default(argc, argv)).c_str(), e.slots(), e.kernels_json().c_str(), json_escape(e.build_notes()).c_str());
    return 0;
}

// The tests/pow-diff backend: igneum-worker-cuda diff --in X --out Y [--device N].
// Same line contract as the cpu backend (ignpow-vectors diff): single vectors,
// range vectors (every nonce hashed, the best reported, coverage listed) and the
// raw malformed cases (parsed on the host exactly as the reference does; the
// hash of a well-formed case runs on the device, verify's reason comes from the
// reference on the device's mix and digest).
bool range_of(const std::string& line, uint64_t& start, uint64_t& count) {
    size_t p = line.find("\"range\"");
    if (p == std::string::npos) return false;
    p = line.find('[', p);
    if (p == std::string::npos) return false;
    start = std::strtoull(line.c_str() + p + 1, nullptr, 10);
    size_t q = line.find(',', p);
    if (q == std::string::npos) return false;
    count = std::strtoull(line.c_str() + q + 1, nullptr, 10);
    return true;
}

uint64_t height_of(const std::string& line, const char* algo) {
    std::string h = field(line, "height");
    if (!h.empty()) return u64(h);
    const uint64_t e = u64(field(line, "epoch"));
    return std::strcmp(algo, IGNPOW_ALGO_KAWPOW_094) == 0 ? e * 7500 : e;  // KAWPOW: the epoch's first block; others: the epoch is the height
}

// refdiff: the same input and output as `ignpow-vectors diff` and the worker's own diff, computed through the linked CPU
// reference (ignpow_hash on a light context), never the device. It is the verifier jobd uses on a machine that gets one
// binary (the project's PCs through the bench agent): the GPU's shares checked by the CPU reference inside the same file.
int cmd_refdiff(int argc, char** argv) {
    std::string in_path, out_path;
    for (int i = 2; i + 1 < argc; ++i) { if (std::strcmp(argv[i], "--in") == 0) in_path = argv[i + 1]; if (std::strcmp(argv[i], "--out") == 0) out_path = argv[i + 1]; }
    if (in_path.empty() || out_path.empty()) return 2;
    std::ifstream in(in_path);
    if (!in) return 2;
    std::ofstream out(out_path);
    const char* algo = algo_arg(argc, argv);
    const char* params = params_arg(argc, argv);
    if (params && !*params) params = nullptr;
    std::vector<std::pair<uint64_t, ignpow_ctx*>> ctxs;  // one light context per epoch
    auto ctx_for = [&](uint64_t height, int& st) -> ignpow_ctx* {
        const uint64_t ep = ignpow_epoch_of(algo, params, height, &st);
        if (st != IGNPOW_OK) return nullptr;
        for (auto& c : ctxs) if (c.first == ep) return c.second;
        ignpow_ctx* c = ignpow_ctx_create(algo, params, height, 0, &st);
        if (c) ctxs.emplace_back(ep, c);
        return c;
    };
    std::string line;
    while (std::getline(in, line)) {
        if (line.empty()) continue;
        const std::string i = field(line, "i");
        uint8_t header[32];
        if (!unhex(field(line, "header"), header, 32)) { out << "{\"i\": " << i << ", \"code\": " << IGNPOW_E_ARG << "}\n"; continue; }
        const uint64_t height = u64(field(line, "height"));
        const uint64_t nonce = u64(field(line, "nonce"));
        int st = 0;
        ignpow_ctx* c = ctx_for(height, st);
        if (!c) { out << "{\"i\": " << i << ", \"code\": " << (st ? st : IGNPOW_E_MEMORY) << "}\n"; continue; }
        uint8_t mix[32], digest[32];
        st = ignpow_hash(c, height, header, nonce, mix, digest);
        if (st != IGNPOW_OK) { out << "{\"i\": " << i << ", \"code\": " << st << "}\n"; continue; }
        out << "{\"i\": " << i << ", \"code\": 0, \"mix\": \"" << hex(mix, 32) << "\", \"digest\": \"" << hex(digest, 32) << "\"}\n";
    }
    for (auto& c : ctxs) ignpow_ctx_free(c.second);
    return 0;
}

int cmd_diff(int argc, char** argv) {
    std::string in_path, out_path;
    for (int i = 2; i + 1 < argc; ++i) { if (std::strcmp(argv[i], "--in") == 0) in_path = argv[i + 1]; if (std::strcmp(argv[i], "--out") == 0) out_path = argv[i + 1]; }
    if (in_path.empty() || out_path.empty()) return 2;
    std::ifstream in(in_path);
    if (!in) return 2;
    std::ofstream out(out_path);
    engine e;
    const char* algo = algo_arg(argc, argv);
    e.set_tune(tune_default(argc, argv));
    e.init(device_arg(argc, argv), algo, 256ull << 20, params_arg(argc, argv));
    std::string line;
    auto prep = [&](uint64_t height) -> int {
        std::string reason;
        if (!e.prepare(height, reason)) { std::fprintf(stderr, "height %llu: %s\n", (unsigned long long)height, reason.c_str()); return IGNPOW_E_MEMORY; }
        return IGNPOW_OK;
    };
    while (std::getline(in, line)) {
        if (line.find('{') == std::string::npos) continue;
        const std::string i = field(line, "i");
        if (line.find("\"raw\"") != std::string::npos) {
            const std::string rawhex = field(line, "raw");
            std::vector<uint8_t> raw(rawhex.size() / 2);
            int code = IGNPOW_OK;
            if (rawhex.size() % 2 || !unhex(rawhex, raw.data(), raw.size()) || raw.size() != 112) code = IGNPOW_E_ARG;
            uint64_t height = 0, nonce = 0;
            if (code == IGNPOW_OK) {
                for (int k = 0; k < 8; ++k) { height = (height << 8) | raw[k]; nonce = (nonce << 8) | raw[40 + k]; }
                int st = 0;
                ignpow_epoch_of(algo, e.params(), height, &st);
                code = st;
            }
            std::vector<hit> hits;
            if (code == IGNPOW_OK) code = prep(height);
            if (code == IGNPOW_OK) {
                e.search_all(height, raw.data() + 8, nonce, 1, hits);
                if (hits.size() != 1) code = IGNPOW_E_VECTORS;
            }
            if (code == IGNPOW_OK) {
                // the reason codes, in the reference's order: mix first, then boundary
                const bool mix_ok = std::memcmp(hits[0].mix, raw.data() + 48, 32) == 0;
                bool le = true;
                for (int k = 0; k < 32; ++k) { if (hits[0].hash[k] < raw[80 + k]) { le = true; break; } if (hits[0].hash[k] > raw[80 + k]) { le = false; break; } }
                code = !mix_ok ? IGNPOW_E_MIX : !le ? IGNPOW_E_BOUNDARY : IGNPOW_OK;
            }
            out << "{\"i\": " << i << ", \"code\": " << code;
            if (code == IGNPOW_OK) out << ", \"mix\": \"" << hex(hits[0].mix, 32) << "\", \"digest\": \"" << hex(hits[0].hash, 32) << "\"";
            out << "}\n";
            continue;
        }
        const uint64_t height = height_of(line, algo);
        uint8_t header[32];
        if (!unhex(field(line, "header"), header, 32)) { out << "{\"i\": " << i << ", \"code\": " << IGNPOW_E_ARG << "}\n"; continue; }
        uint64_t start = 0, count = 0;
        const bool ranged = range_of(line, start, count);
        if (!ranged) { start = u64(field(line, "nonce")); count = 1; }
        int st = 0;
        ignpow_epoch_of(algo, e.params(), height, &st);
        if (st == IGNPOW_OK) st = prep(height);
        if (st != IGNPOW_OK) { out << "{\"i\": " << i << ", \"code\": " << st << "}\n"; continue; }
        std::vector<hit> hits;
        for (uint64_t done = 0; done < count; done += (1u << 20)) {
            const uint32_t n = static_cast<uint32_t>(std::min<uint64_t>(1u << 20, count - done));
            e.search_all(height, header, start + done, n, hits);
        }
        if (hits.size() != count) { out << "{\"i\": " << i << ", \"code\": " << IGNPOW_E_VECTORS << ", \"note\": \"" << hits.size() << " of " << count << " nonces returned\"}\n"; continue; }
        size_t best = 0;
        for (size_t k = 1; k < hits.size(); ++k) if (std::memcmp(hits[k].hash, hits[best].hash, 32) < 0) best = k;
        out << "{\"i\": " << i << ", \"code\": 0, \"mix\": \"" << hex(hits[best].mix, 32) << "\", \"digest\": \"" << hex(hits[best].hash, 32) << "\"";
        if (ranged) {
            out << ", \"best_nonce\": " << hits[best].nonce << ", \"evaluated_count\": " << count;
            if (count <= 4096) { out << ", \"evaluated\": ["; for (size_t k = 0; k < hits.size(); ++k) out << (k ? "," : "") << hits[k].nonce; out << "]"; }
            else out << ", \"evaluated\": [" << start << ", " << (start + count - 1) << "]";
        }
        out << "}\n";
    }
    return 0;
}

// Protocol mode (WORKER-PROTOCOL.md draft 0): single device, single job at a time.
// The seal verb (KEYED-SEAL-WORKER.md section 1, the hash team's order of 12 Oct 2026): a job with "seal": true runs the keyed search
// at the job's boundary, returns the FIRST result at or under it as one "result" line and stops; "deadline_ms" ends the search at the
// round's end without a cancel (a "done" line names the reason); the share lane's job returns every share and continues, as before.
struct job { std::string id; uint64_t height = 0; uint8_t header[32]; uint8_t boundary[32]; uint8_t share_target[32]; uint64_t nonce_start = 0, nonce_count = 0, done = 0; bool active = false;
             bool seal = false; uint64_t deadline_ms = 0; std::chrono::steady_clock::time_point started; };

// The custody guard of the seal verb: a keyed job (sk on the line) is taken only from a local peer. The protocol runs on stdin, so
// the peer is whoever holds the other end of fd 0: a pipe or a terminal (the parent process on this host), a Unix socket, or a
// TCP socket whose peer is a loopback address; any other socket peer is refused with the fault "keyed-peer" (recoverable false)
// and the job is not taken. A read failure on fd 0 counts as not local.
bool local_peer() {
#ifdef _WIN32
    HANDLE h = GetStdHandle(STD_INPUT_HANDLE);
    const DWORD ft = GetFileType(h);
    return ft == FILE_TYPE_PIPE || ft == FILE_TYPE_CHAR || ft == FILE_TYPE_DISK;   // a socket handle reads FILE_TYPE_PIPE on Windows; the dispatcher there is a pipe by Ember's form
#else
    struct stat st;
    if (fstat(0, &st) != 0) return false;
    if (!S_ISSOCK(st.st_mode)) return true;   // a pipe, a tty or a file: the parent on this host
    sockaddr_storage ss; socklen_t sl = sizeof ss;
    if (getpeername(0, reinterpret_cast<sockaddr*>(&ss), &sl) != 0) return false;
    if (ss.ss_family == AF_UNIX) return true;
    if (ss.ss_family == AF_INET) { const auto* a = reinterpret_cast<const sockaddr_in*>(&ss); return (ntohl(a->sin_addr.s_addr) >> 24) == 127; }
    if (ss.ss_family == AF_INET6) {
        const auto* a = reinterpret_cast<const sockaddr_in6*>(&ss);
        if (IN6_IS_ADDR_LOOPBACK(&a->sin6_addr)) return true;
        if (IN6_IS_ADDR_V4MAPPED(&a->sin6_addr)) return a->sin6_addr.s6_addr[12] == 127;
    }
    return false;
#endif
}

// A line is ready when bytes wait on the pipe or already sit in std::cin's buffer behind the line last consumed
// (after a blocking prepare two job lines arrive together; polling the pipe alone left the second unseen until the
// next line reached the pipe, so a superseded job was searched for a whole job interval: the 2060 FishHash row).
bool stdin_ready() {
    if (std::cin.rdbuf()->in_avail() > 0) return true;
#ifdef _WIN32
    HANDLE h = GetStdHandle(STD_INPUT_HANDLE);
    DWORD avail = 0;
    if (PeekNamedPipe(h, nullptr, 0, nullptr, &avail, nullptr)) return avail > 0;
    return WaitForSingleObject(h, 0) == WAIT_OBJECT_0;  // a console or a file
#else
    pollfd p{0, POLLIN, 0};
    return poll(&p, 1, 0) > 0;
#endif
}

void sleep_ms(int ms) {
#ifdef _WIN32
    Sleep(ms);
#else
    usleep(ms * 1000);
#endif
}

// The rented-host custody of the keyed job's secret (F-53, the security review's reading of the custody pod, 12 Oct 2026): a worker that holds a
// bond secret must never write a core file, because on a rented pod the host's core_pattern lands the dump on the landlord's disk. At
// protocol start the core limit goes to zero and the process marks itself not dumpable (no core, no /proc/<pid>/mem read by another
// user); the host copy of the secret is locked in memory by the engine (set_secret) so it never reaches swap. The record reads it in
// /proc/<pid>/limits ("Max core file size 0 0") and the owner mode of /proc/<pid>/mem while a keyed job runs. Windows: the error-mode
// bits stop the fault box; a dump policy of the crash handler is a system setting, named unavailable here.
static void custody_no_core() {
#ifdef _WIN32
    SetErrorMode(SetErrorMode(0) | SEM_FAILCRITICALERRORS | SEM_NOGPFAULTERRORBOX);
#else
    struct rlimit rl; rl.rlim_cur = 0; rl.rlim_max = 0;
    setrlimit(RLIMIT_CORE, &rl);
    prctl(PR_SET_DUMPABLE, 0, 0, 0, 0);
#endif
}

int cmd_protocol(int argc, char** argv) {
    custody_no_core();
    std::unique_ptr<engine> ep(new engine());
    std::unique_ptr<engine> standby;   // the warm prepare path's second engine (the announced document's dataset beside the current one)
    std::string standby_params; uint64_t standby_bytes = 0;
    const int dev = device_arg(argc, argv);
    std::string algo_s = algo_arg(argc, argv);
    const char* algo = algo_s.c_str();
    std::string params_s = params_arg(argc, argv) ? params_arg(argc, argv) : "";
    // protocol mode's default tune is the ratified 5090 knee for igneumhash (words=1, 65536 slots, the batched tail at N=2): Ember launches the
    // worker bare, and under the engine's old default (4096 slots, no tail) the 5090 read 13.5 MH/s on the IgneumHash document and 49.5 on W128
    // against 40.6 and 166 at the knee (the product team's pair on loomc, 11 Oct 2026 05:2x UK); a --tune on the command line overrides it
    std::string tune_s = tune_arg(argc, argv);
    if (tune_s.empty()) tune_s = kKneeTune;   // unconditional here: the candidate arrives in configure, after this line
    ep->set_tune(tune_s);
    ep->init(dev, algo, 256ull << 20, params_s, true);   // the static module after the hello (Ember's ten-second handshake)
    uint64_t seq = 0;
    auto send = [&](const std::string& t, const std::string& body) {
        std::printf("{\"t\": \"%s\", \"seq\": %llu%s%s}\n", t.c_str(), (unsigned long long)++seq, body.empty() ? "" : ", ", body.c_str());
        std::fflush(stdout);
    };
    const bool vectors_ok = true;   // the project's selftest() ran the reference vectors before this mode was entered (a failure exits 3 there)
    {
        // the hello lists EVERY device the process sees (F-69, the product team's read on the three-card test machine, 22:08 UK 12 Oct 2026: the one-device hello
        // left Ember starting one card of three), each in the form of the device this process serves; the others are read through a
        // context of their own with the static module deferred (no compile), and a device whose init throws is named on stderr and left out
        std::string devs;
        const int ndev = engine::device_count();
        for (int i = 0; i < ndev; ++i) {
            std::unique_ptr<engine> other;
            const device_info* dp = nullptr;
            if (i == dev) dp = &ep->info();
            else { try { other.reset(new engine()); other->init(i, algo, 256ull << 20, params_s, true); dp = &other->info(); } catch (const std::exception& ex) { std::fprintf(stderr, "device %d: %s\n", i, ex.what()); continue; } }
            const auto& d = *dp;
            std::string slow = d.slowdown_c ? ", \"slowdown_c\": " + std::to_string(d.slowdown_c) : "";
            char e[1024];
            std::snprintf(e, sizeof e,
                          "%s{\"id\": \"cuda:%d\", \"name\": \"%s\", \"vendor\": \"nvidia\", \"kind\": \"%s\", \"bus_id\": \"%s\", \"memory_bytes\": \"%llu\", \"arch\": \"sm_%d%d\", \"driver\": \"%d\", \"runtime\": \"%s\"%s, "
                          "\"knobs\": [{\"name\": \"batch_nonces\", \"min\": 16384, \"max\": 16777216, \"step\": \"x2\", \"default\": 1048576, \"unit\": \"nonces\"}, "
                          "{\"name\": \"batch_ms\", \"min\": 0, \"max\": 2000, \"step\": 10, \"default\": 40, \"unit\": \"ms\"}]}",
                          devs.empty() ? "" : ", ", d.ordinal, d.name.c_str(), d.integrated ? "integrated" : "discrete", d.pci.c_str(), (unsigned long long)d.total_mem, d.cc_major, d.cc_minor, d.driver_version, d.arch_option.c_str(), slow.c_str());
            devs += e;
        }
        // 16 KiB: the build string alone is near 600 bytes and every device adds about 400 (a 1 KiB buffer cut the 5090's hello at its last bracket on 11 Oct)
        std::string b = "\"proto\": 0, \"worker\": \"igneum-worker-cuda\", \"version\": \"" + build_sha8() + "\", \"build\": \"" + json_escape(ignpow_build_string()) + "\", \"backend\": \"cuda\", \"extensions\": [\"keyed-job\", \"seal\"], \"seal\": true, "
                        "\"algos\": [{\"id\": \"igneumhash\", \"vectors_ok\": true}, {\"id\": \"kawpow-0.9.4\", \"vectors_ok\": true}, {\"id\": \"progpow-epic\", \"vectors_ok\": true}, {\"id\": \"autolykos-2\", \"vectors_ok\": true}, {\"id\": \"fishhash-1\", \"vectors_ok\": true}, {\"id\": \"rxe1\", \"vectors_ok\": true}], "
                        "\"devices\": [" + devs + "]";
        if (b.size() >= 16384) { std::fprintf(stderr, "hello does not fit its buffer (%zu bytes)\n", b.size()); return 3; }
        send("hello", b.c_str());
    }
    if (!vectors_ok) { send("fault", "\"code\": \"vectors\", \"message\": \"reference self-test failed\", \"recoverable\": false"); return 3; }
    job cur;
    ignpow_ctx* seal_ref = nullptr; uint64_t seal_ref_height = 0;   // the seal verb's reference context for the one-nonce replay (shares and m)
    bool paused = false, configured = false;
    int stat_ms = 1000;
    int batch_ms = 40;  // the batch is resized toward this launch duration (0: fixed batch). Default 40 ms since 11 Oct 2026 07:2x UK: the per-batch
                        // cost (the count readback and the loop around each launch) read 2.5 percent of the bench at 1M nonces on the 5090 (5.7 ms launches)
                        // and 0.9 percent at 40 ms launches (cell igneumhash-rung-batch-overhead); adaptive, so a 5060 Ti sizes to about 2M; a set batch_nonces fixes it
    auto last_stat = std::chrono::steady_clock::now();
    uint64_t hashes_since = 0;
    bool job_started = false;
    std::string line;
    // The protocol loop reads every line the supervisor has sent before each batch. With std::cin synced to C stdio (the default), a burst
    // of lines read into stdio's buffer left in_avail() at 0 and poll() silent, so the loop took ONE line per burst and the rest queued:
    // under Ember on devnet2 (a cancel and a job every 0.6 s) the worker fell one message behind per block, mined cancelled jobs and
    // idled on live ones (the product team's teed pipes on r0a-igneumhash-5090c, 11 Oct 2026 05:2x UK). cin owns its buffer from here on.
    std::ios::sync_with_stdio(false);
    while (true) {
        while (stdin_ready()) {
            if (!std::getline(std::cin, line)) { send("bye", ""); return 0; }
            const std::string t = field(line, "t");
            const std::string re = field(line, "seq");
            if (t == "configure") {
                // the configure message carries the candidate and its params document (protocol v0: noded.py, Ember);
                // a different pair than the engine holds rebuilds the engine on the same device (the cold transition)
                const std::string ca = field(line, "algo");
                const std::string cp = escaped_field(line, "params");
                if (!cp.empty() && cp != params_s && standby && standby_params == cp && (ca.empty() || ca == algo_s)) {
                    // the warm swap: the configure names the document the standby holds; the engines trade places, nothing is rebuilt
                    ep.swap(standby); params_s = cp; standby_params.clear(); standby.reset();
                    ep->make_current();
                    cur.active = false; job_started = false;
                } else if ((!ca.empty() && ca != algo_s) || (!cp.empty() && cp != params_s)) {
                    try {
                        ep.reset(new engine());
                        if (!ca.empty()) algo_s = ca;
                        if (!cp.empty()) params_s = cp;
                        algo = algo_s.c_str();
                        ep->set_tune(tune_s);
                        ep->init(dev, algo, 256ull << 20, params_s);
                        cur.active = false; job_started = false;
                    } catch (const std::exception& ex) {
                        send("fault", "\"code\": \"configure\", \"message\": \"" + json_escape(ex.what()) + "\", \"recoverable\": false");
                        return 3;
                    }
                }
                configured = true;
                const std::string sm = field(line, "stat_ms");
                if (!sm.empty()) stat_ms = std::max(100, std::atoi(sm.c_str()));
                budget b = ep->budget_for(1);
                char buf[1024];
                std::snprintf(buf, sizeof buf, "\"re\": %s, \"device\": \"cuda:%d\", \"algo\": \"%s\", \"budget\": {\"total_bytes\": \"%llu\", \"free_bytes\": \"%llu\", \"fit_cold_bytes\": \"%llu\", \"fit_warm_bytes\": \"%llu\", \"can_warm\": %s}",
                              re.empty() ? "0" : re.c_str(), dev, algo, (unsigned long long)b.total, (unsigned long long)b.free_before,
                              (unsigned long long)b.fit_cold, (unsigned long long)b.fit_warm, b.can_warm ? "true" : "false");
                send("ready", buf);
            } else if (t == "prepare" || t == "job") {
                if (!configured) { send("fault", "\"code\": \"protocol\", \"message\": \"job before configure\", \"recoverable\": true"); continue; }
                const uint64_t height = u64(field(line, "height"));
                std::string reason; double ms = 0; uint64_t bytes = 0;
                // a prepare naming another document (Ember's transition prepare, 11 Oct 2026 05:1x UK) is answered: the engine is
                // rebuilt on that document (the cold path on this worker; a warm build beside the current dataset is not in the
                // engine yet) and the prepared line says mode cold, so the supervisor never waits on silence
                {
                    // the warm prepare path (the project's 05:15 order): a prepare naming another document builds that document's dataset on a
                    // STANDBY engine beside the current one (its own context), answers prepared with mode warm and the digest, and the
                    // swap is the configure Ember sends at the height (no rebuild then); refuse with the card's reason when it cannot be built
                    const std::string pp = escaped_field(line, "params");
                    if (!pp.empty() && pp != params_s) {
                        if (standby && standby_params == pp) {
                            send("prepared", "\"epoch\": " + std::to_string(standby->current_epoch()) + ", \"ms\": 0, \"bytes\": \"" + std::to_string(standby_bytes) + "\", \"mode\": \"warm\"" +
                                 (standby->dataset_digest_hex().empty() ? std::string() : ", \"digest\": \"" + standby->dataset_digest_hex() + "\""));
                            ep->make_current();
                            continue;
                        }
                        try {
                            std::unique_ptr<engine> sb(new engine());
                            sb->set_tune(tune_s);
                            sb->init(dev, algo, 256ull << 20, pp);
                            std::string sreason; double sms = 0; uint64_t sbytes = 0;
                            if (!sb->prepare(height, sreason, &sms, &sbytes)) { ep->make_current(); send("refuse", "\"height\": " + std::to_string(height) + ", \"reason\": \"" + json_escape(sreason) + "\""); continue; }
                            standby = std::move(sb); standby_params = pp; standby_bytes = sbytes;
                            send("prepared", "\"epoch\": " + std::to_string(standby->current_epoch()) + ", \"ms\": " + std::to_string((long long)sms) + ", \"bytes\": \"" + std::to_string(sbytes) + "\", \"mode\": \"warm\"" +
                                 (standby->dataset_digest_hex().empty() ? std::string() : ", \"digest\": \"" + standby->dataset_digest_hex() + "\""));
                        } catch (const std::exception& ex) {
                            send("refuse", "\"height\": " + std::to_string(height) + ", \"reason\": \"" + json_escape(ex.what()) + "\"");
                        }
                        ep->make_current();
                        continue;
                    }
                }
                const bool had = ep->has_dataset(); const uint64_t before = ep->current_epoch();
                if (!ep->prepare(height, reason, &ms, &bytes)) { send("refuse", "\"height\": " + std::to_string(height) + ", \"reason\": \"" + json_escape(reason) + "\""); cur.active = false; continue; }
                if (!had || before != ep->current_epoch())
                    send("prepared", "\"epoch\": " + std::to_string(ep->current_epoch()) + ", \"ms\": " + std::to_string((long long)ms) + ", \"bytes\": \"" + std::to_string(bytes) + "\", \"mode\": \"cold\"" +
                         (ep->dataset_digest_hex().empty() ? std::string() : ", \"digest\": \"" + ep->dataset_digest_hex() + "\""));
                if (t == "job") {
                    job j; j.id = field(line, "job_id"); j.height = height;
                    if (!unhex(field(line, "header"), j.header, 32) || !unhex(field(line, "boundary"), j.boundary, 32)) { send("fault", "\"code\": \"job\", \"message\": \"bad header or boundary\", \"recoverable\": true"); continue; }
                    if (!unhex(field(line, "share_target"), j.share_target, 32)) std::memcpy(j.share_target, j.boundary, 32);
                    // the keyed-job extension (version 8 bit 10, the product team's job line): sk, hex of 32 bytes, per job, present only under a
                    // keyed document; a keyed document with no sk on the job is the named fault "keyed" and the job is not taken; the
                    // secret goes to the engine's params buffer and nowhere else (never echoed in a line)
                    {
                        uint8_t sk[32];
                        const bool has_sk = unhex(field(line, "sk"), sk, 32);
                        if (ep->keyed_document() && !has_sk) { send("fault", "\"code\": \"keyed\", \"message\": \"a keyed document (features bit 10) needs sk on the job\", \"recoverable\": true"); continue; }
                        // the seal verb's custody guard: a keyed job is taken only from a local peer (a pipe, a Unix socket or a loopback
                        // address on fd 0); from any other peer the fault keyed-peer (recoverable false), no job, no secret set
                        if (has_sk && !local_peer()) { std::memset(sk, 0, 32); ep->wipe_secret(); send("fault", "\"code\": \"keyed-peer\", \"message\": \"a keyed job is taken only from a loopback or Unix-socket peer\", \"recoverable\": false"); continue; }
                        ep->set_secret(has_sk ? sk : nullptr);
                        std::memset(sk, 0, 32);
                    }
                    j.nonce_start = u64(field(line, "nonce_start")); j.nonce_count = u64(field(line, "nonce_count")); j.active = true;
                    // the seal verb: "seal": true and "deadline_ms"; a seal job searches at its boundary (the share target is the boundary)
                    j.seal = field(line, "seal") == "true";
                    j.deadline_ms = u64(field(line, "deadline_ms"));
                    if (j.seal) std::memcpy(j.share_target, j.boundary, 32);
                    j.started = std::chrono::steady_clock::now();
                    cur = j;
                    job_started = true;  // one stat the moment the new job's first batch runs (the switch figure)
                }
            } else if (t == "cancel") { if (cur.id == field(line, "job_id")) { cur.active = false; ep->wipe_secret(); } }
            else if (t == "set") {
                const std::string knob = field(line, "knob");
                const uint64_t v = u64(field(line, "value"));
                if (knob == "batch_nonces") {
                    uint32_t b = 16384;
                    while (b < v && b < 16777216u) b <<= 1;  // clamp to the doubling ladder
                    ep->set_batch(b); batch_ms = 0;
                    send("knob", "\"re\": " + (re.empty() ? std::string("0") : re) + ", \"knob\": \"batch_nonces\", \"value\": " + std::to_string(b));
                } else if (knob == "batch_ms") {
                    batch_ms = static_cast<int>(std::min<uint64_t>(v, 2000) / 10 * 10);
                    send("knob", "\"re\": " + (re.empty() ? std::string("0") : re) + ", \"knob\": \"batch_ms\", \"value\": " + std::to_string(batch_ms));
                } else if (knob == "r0d_anchor") {
                    // the value a string of 64 hex (with or without 0x): the period's programme anchor; applied at the next prepare (a module rebuild, no dataset work)
                    std::string a = field(line, "value"); if (a.rfind("0x", 0) == 0) a = a.substr(2);
                    if (ep->set_anchor(a)) send("knob", "\"re\": " + (re.empty() ? std::string("0") : re) + ", \"knob\": \"r0d_anchor\", \"value\": 0, \"anchor\": \"" + a + "\"");   // the ack's value is a number (Ember reads knob values as f64: the hex echo restarted the worker and the anchor never applied, 16:53 UK 13 Oct); the anchor rides its own field
                    else send("fault", "\"code\": \"knob\", \"message\": \"r0d_anchor takes 64 hex characters\", \"recoverable\": true");
                } else {
                    send("fault", "\"code\": \"knob\", \"message\": \"unknown knob " + json_escape(knob) + "\", \"recoverable\": true");
                }
            }
            else if (t == "pause") paused = true;
            else if (t == "resume") paused = false;
            else if (t == "shutdown") { ep->wipe_secret(); send("bye", ""); return 0; }
        }
        if (cur.active && cur.seal && cur.deadline_ms && std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - cur.started).count() >= double(cur.deadline_ms)) {
            // the deadline: the round's end without a cancel; the job ends, the secret is wiped, the node reads the reason
            cur.active = false; ep->wipe_secret();
            send("done", "\"job_id\": \"" + cur.id + "\", \"reason\": \"deadline\", \"nonces\": \"" + std::to_string(cur.done) + "\"");
        }
        if (cur.active && !paused && cur.done < cur.nonce_count) {
            std::vector<hit> hits;
            const uint64_t n = std::min<uint64_t>(ep->batch(), cur.nonce_count - cur.done);
            const auto t0 = std::chrono::steady_clock::now();
            const uint64_t got = ep->search(cur.height, cur.header, cur.share_target, cur.nonce_start + cur.done, n, hits);
            const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
            if (batch_ms > 0 && n == ep->batch()) {  // size the batch toward the target duration on the doubling ladder
                if (ms < batch_ms * 0.5 && ep->batch() < 16777216u) ep->set_batch(ep->batch() * 2);
                else if (ms > batch_ms * 1.5 && ep->batch() > 16384u) ep->set_batch(ep->batch() / 2);
            }
            cur.done += got; hashes_since += got;
            if (cur.seal) {
                // the seal verb: the FIRST result at or under the boundary (the lowest nonce of the batch's hits, so the answer is the
                // range's first), its shares and m_i from the reference's replay of that one nonce under the job's secret
                // (ignh_hash_keyed_m: the m_i the kernel entered the keyed rounds with, the shares it folded in; the replay's mix and
                // hash must equal the kernel's, else the fault seal-replay and the search goes on), then the job ends and the secret
                // is wiped. The card's own hit is the proof of work; the replay is one CPU hash per seal.
                const hit* first = nullptr;
                for (const hit& h : hits) if (!first || h.nonce < first->nonce) first = &h;
                if (first) {
                    const uint32_t K = ep->keyed_rounds();
                    std::vector<uint8_t> shares(size_t(K) * 32), m(size_t(K) * 32);
                    uint8_t mix[32], hash[32], sk[32]; bool ok = false;
                    if (ep->secret_copy(sk)) {
                        int st = 0;
                        if (!seal_ref || seal_ref_height != cur.height) { if (seal_ref) ignpow_ctx_free(seal_ref); seal_ref = ignpow_ctx_create(algo, params_s.c_str(), cur.height, 0, &st); seal_ref_height = cur.height; }
                        ok = seal_ref && ignh_hash_keyed_m(seal_ref, cur.height, cur.header, first->nonce, sk, shares.data(), mix, hash, m.data()) == IGNPOW_OK
                             && std::memcmp(mix, first->mix, 32) == 0 && std::memcmp(hash, first->hash, 32) == 0;
                    }
                    volatile uint8_t* z = sk; for (int i = 0; i < 32; ++i) z[i] = 0;
                    if (ok) {
                        std::string sh = "[", mm = "[";
                        for (uint32_t i = 0; i < K; ++i) { sh += (i ? ",\"" : "\"") + hex(shares.data() + size_t(i) * 32, 32) + "\""; mm += (i ? ",\"" : "\"") + hex(m.data() + size_t(i) * 32, 32) + "\""; }
                        sh += "]"; mm += "]";
                        send("result", "\"job_id\": \"" + cur.id + "\", \"nonce\": \"" + std::to_string(first->nonce) + "\", \"mix\": \"" + hex(first->mix, 32) + "\", \"hash\": \"" + hex(first->hash, 32) + "\", \"shares\": " + sh + ", \"m\": " + mm);
                        cur.active = false; ep->wipe_secret();
                    } else {
                        send("fault", "\"code\": \"seal-replay\", \"message\": \"the reference's replay of nonce " + std::to_string(first->nonce) + " did not match the card's hit; the search goes on\", \"recoverable\": true");
                    }
                }
                if (cur.active && cur.done >= cur.nonce_count) { cur.active = false; ep->wipe_secret(); send("done", "\"job_id\": \"" + cur.id + "\", \"reason\": \"exhausted\", \"nonces\": \"" + std::to_string(cur.done) + "\""); }
            } else {
                for (const hit& h : hits)
                    send("share", "\"job_id\": \"" + cur.id + "\", \"nonce\": \"" + std::to_string(h.nonce) + "\", \"mix\": \"" + hex(h.mix, 32) + "\", \"hash\": \"" + hex(h.hash, 32) + "\"");
                // the share lane's range exhausted: the job ends and the supervisor hears it at once ("done", reason exhausted, the line the seal
                // lane sends) instead of at the next stat up to STAT_MS later (Ember 0.2.19 on a 5090: a gap after every 2^28 chunk, 16 Oct 2026
                // 03:2x UK); a supervisor that does not name the kind reads it as Unknown and drops it (igneum-worker-proto FromWorker)
                if (cur.done >= cur.nonce_count) { cur.active = false; if (ep->keyed_document()) ep->wipe_secret(); send("done", "\"job_id\": \"" + cur.id + "\", \"reason\": \"exhausted\", \"nonces\": \"" + std::to_string(cur.done) + "\""); }
            }
        } else {
            sleep_ms(5);
        }
        const auto now = std::chrono::steady_clock::now();
        const double dt = std::chrono::duration<double>(now - last_stat).count();
        // a stat on the period, and one the moment a new job's first batch has run (the supervisor's switch figure)
        if (dt * 1000 >= stat_ms || (job_started && cur.done > 0)) {
            const char* state = !configured ? "idle" : paused ? "paused" : cur.active ? "mining" : "idle";
            std::string tel = telemetry_json(ep->read_telemetry());
            tel.insert(tel.size() - 1, ", \"code_bytes\": \"" + std::to_string(ep->code_bytes()) + "\"");  // the product team: the compiled code resident
            send("stat", "\"job_id\": \"" + cur.id + "\", \"hashes\": \"" + std::to_string(hashes_since) + "\", \"elapsed_ms\": " + std::to_string((long long)(dt * 1000)) +
                             ", \"telemetry\": " + tel + ", \"state\": \"" + state + "\"" + (job_started && cur.done > 0 ? ", \"job_start\": true" : ""));
            last_stat = now; hashes_since = 0;
            if (cur.done > 0) job_started = false;
        }
    }
}

}  // namespace

int main(int argc, char** argv) {
    // no arguments: protocol v0 on stdin/stdout (Ember launches a worker bare and waits for hello; 10 Oct 2026 21:3x UK)
    if (argc < 2) { if (selftest() != 0) return 3; try { return cmd_protocol(argc, argv); } catch (const std::exception& e) { std::fprintf(stderr, "%s\n", e.what()); return 1; } }
    if (selftest() != 0) return 3;
    const std::string cmd = argv[1];
    try {
        if (cmd == "devices") return cmd_devices();
        if (cmd == "precompile") return cmd_precompile(argc, argv);
        if (cmd == "vectors") return cmd_vectors(argc, argv);
        if (cmd == "bench") return cmd_bench(argc, argv);
        if (cmd == "split") return cmd_split(argc, argv);   // the renter of GUARD-C-CLOSURE.md 1.4 (O3): the keyed rounds through a remote oracle
        if (cmd == "template") {
            // template <sk-file> --algo igneumhash --param ...: the reference oracle's inputs for the split verb's bench header: the header's
            // test secret written to <sk-file> as hex (the oracle reads it by path; public by construction), then one line "C V P H" (the
            // chain id as u64be hex, the version, the params hash P the work seed stands on, the header) for `bond-oracle serve`
            if (argc < 3) { std::fprintf(stderr, "template <sk-file> --algo igneumhash --param ...\n"); return 2; }
            ignh_params p{};
            if (ignh_params_parse(params_arg(argc, argv), &p) != IGNPOW_OK) { std::fprintf(stderr, "template: the reference refused the params\n"); return 2; }
            uint8_t header[32], sk[32], P[32];
            for (int i = 0; i < 32; ++i) header[i] = static_cast<uint8_t>(i * 7 + 1);
            { const seal_opts so = seal_args(argc, argv); if (so.header_set) std::memcpy(header, so.header, 32); }   // --header: a template's header hash (the count)
            ignh_test_sk(header, sk);
            ignh_params_hash(&p, P);
            { std::ofstream f(argv[2]); f << hex(sk, 32) << "\n"; }
            uint8_t C[8]; for (int i = 0; i < 8; ++i) C[i] = static_cast<uint8_t>(p.chain_id >> (8 * (7 - i)));
            std::printf("%s %u %s %s\n", hex(C, 8).c_str(), p.version, hex(P, 32).c_str(), hex(header, 32).c_str());
            return 0;
        }
        if (cmd == "protocol") return cmd_protocol(argc, argv);
        if (cmd == "diff") return cmd_diff(argc, argv);
        if (cmd == "refdiff") return cmd_refdiff(argc, argv);
        if (cmd == "soak") return cmd_soak(argc, argv);
        if (cmd == "grind") {   // grind <height> [--params ...] [--tune ...] [--device N]: one launch of 1 M nonces with the per-nonce cycle instrument; the cost distribution
            if (argc < 3) { std::fprintf(stderr, "grind <height>\n"); return 2; }
            const uint64_t height = u64(argv[2]);
            engine e;
            std::string tu = tune_default(argc, argv); tu += tu.empty() ? "grind=1" : ",grind=1";
            e.set_tune(tu);
            e.init(device_arg(argc, argv), algo_arg(argc, argv), 256ull << 20, params_arg(argc, argv));
            std::string reason; double ms = 0; uint64_t bytes = 0;
            if (!e.prepare(height, reason, &ms, &bytes)) { std::printf("{\"refuse\": \"%s\"}\n", json_escape(reason).c_str()); return 4; }
            uint8_t header[32], target[32];
            for (int i = 0; i < 32; ++i) header[i] = static_cast<uint8_t>(i * 7 + 1);
            std::memset(target, 0, 32); target[2] = 0x01;
            std::vector<hit> hits;
            const uint64_t N = 1u << 20;
            e.set_batch(static_cast<uint32_t>(N));
            e.search(height, header, target, 0, N, hits);   // warm (the compile, the first touch)
            const auto t0 = std::chrono::steady_clock::now();
            const uint64_t n = e.search(height, header, target, N, N, hits);
            const double lms = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() * 1e3;
            std::vector<uint32_t> cyc; e.grind_read(cyc); cyc.resize(static_cast<size_t>(n));
            std::vector<uint32_t> sorted(cyc.begin(), cyc.end()); std::sort(sorted.begin(), sorted.end());
            auto pct = [&](double q) { return sorted.empty() ? 0u : sorted[std::min(sorted.size() - 1, static_cast<size_t>(q * sorted.size()))]; };
            double mean = 0; for (uint32_t c : sorted) mean += c; mean /= std::max<size_t>(1, sorted.size());
            double var = 0; for (uint32_t c : sorted) var += (c - mean) * (c - mean); const double sd = sorted.size() > 1 ? std::sqrt(var / (sorted.size() - 1)) : 0.0;
            // a 64-bin histogram from the 0.1st to the 99.9th percentile (the security review's abort model takes it as a file beside the bundle)
            std::string hist; { const uint32_t lo = pct(0.001), hi = std::max(pct(0.999), lo + 64u); std::vector<uint64_t> bins(64, 0);
                for (uint32_t c : sorted) { if (c < lo) { ++bins[0]; continue; } size_t i = (size_t)((double)(c - lo) * 64 / (hi - lo)); if (i > 63) i = 63; ++bins[i]; }
                hist = "\"hist_lo\": " + std::to_string(lo) + ", \"hist_hi\": " + std::to_string(hi) + ", \"hist_bins\": [";
                for (size_t i = 0; i < 64; ++i) hist += (i ? "," : "") + std::to_string(bins[i]); hist += "]"; }
            // the early-abort arithmetic: a miner that drops the costliest fifth after its first round (the cost of a round is the
            // nonce's cycles over 64) pays one round on every nonce and the whole on the kept four fifths
            const size_t k = sorted.size() / 5; double kept = 0; for (size_t i = 0; i + k < sorted.size(); ++i) kept += sorted[i];
            const double all = mean * sorted.size(), abort_cost = all / 64.0 + kept;   // one round each, then the kept nonces' full cost
            const double gain = sorted.empty() ? 0 : (1.0 - 0.2) / (abort_cost / all);   // hashes kept per unit cost, relative to no abort (1.0)
            std::printf("{\"grind\": {\"device\": \"%s\", \"arch\": \"%s\", \"nonces\": %llu, \"launch_ms\": %.3f, \"cycles_p1\": %u, \"cycles_p50\": %u, \"cycles_p99\": %u, \"cycles_max\": %u, \"cycles_mean\": %.1f, \"cycles_sd\": %.1f, \"cv\": %.4f, %s, "
                        "\"spread_p99_over_p1\": %.4f, \"first_round_share_of_hash\": %.4f, \"abort_costliest_fifth_after_round1_relative_rate_per_cost\": %.4f, \"note\": \"cycles from the first address to the end of the rounds, lane 0 of the group, clock64; the same instruction count per nonce by construction on SIMT (every descriptor executes one operation whatever the data), so the spread measures memory timing and scheduling, not the programme's data-dependence\"}}\n",
                        e.info().name.c_str(), e.info().arch_option.c_str(), (unsigned long long)n, lms, pct(0.01), pct(0.5), pct(0.99), sorted.empty() ? 0u : sorted.back(), mean, sd, mean ? sd / mean : 0.0, hist.c_str(), pct(0.01) ? (double)pct(0.99) / pct(0.01) : 0.0, 1.0 / 64.0, gain);
            return 0;
        }
        if (cmd == "fpcheck") {   // fpcheck <fp.json> [--device N]: every edge and random row of the fp class through the device's floating form; mismatches listed
            if (argc < 3) { std::fprintf(stderr, "fpcheck <fp.json>\n"); return 2; }
            std::ifstream in(argv[2]); std::string text((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
            std::vector<uint32_t> xyz, want, contracted; std::vector<bool> has_c;
            const bool wide = text.find("\"wide\"") != std::string::npos;   // bit 5's rows carry the opcode; the narrow rows (bit 4) do not
            size_t p = 0;
            while ((p = text.find(wide ? "{\"op\":" : "{\"x\":", p)) != std::string::npos) {
                const size_t q = text.find('}', p); const std::string row = text.substr(p, q - p + 1); p = q;
                if (wide) xyz.push_back((uint32_t)u64(field(row, "op")));
                xyz.push_back((uint32_t)u64(field(row, "x"))); xyz.push_back((uint32_t)u64(field(row, "y"))); xyz.push_back((uint32_t)u64(field(row, "z")));
                want.push_back((uint32_t)u64(field(row, "out")));
                const std::string c = field(row, "contracted"); has_c.push_back(!c.empty()); contracted.push_back(c.empty() ? 0u : (uint32_t)u64(c));
            }
            engine e; e.init(device_arg(argc, argv), IGNPOW_ALGO_KAWPOW_094, 256ull << 20, "", true);
            std::vector<uint32_t> got; e.fp_check(xyz, got, wide);
            const uint32_t stride = wide ? 4 : 3;
            size_t bad = 0, contracted_hits = 0, with_c = 0;
            for (size_t i = 0; i < want.size(); ++i) { if (has_c[i]) ++with_c; if (got[i] != want[i]) { ++bad; if (has_c[i] && got[i] == contracted[i]) ++contracted_hits; if (bad <= 8) std::printf("{\"mismatch\": %zu, \"x\": %u, \"y\": %u, \"z\": %u, \"want\": %u, \"got\": %u}\n", i, xyz[stride * i + (wide ? 1 : 0)], xyz[stride * i + (wide ? 2 : 1)], xyz[stride * i + (wide ? 3 : 2)], want[i], got[i]); } }
            std::printf("{\"fpcheck\": {\"device\": \"%s\", \"arch\": \"%s\", \"form\": \"%s\", \"rows\": %zu, \"rows_with_contracted_value\": %zu, \"mismatches\": %zu, \"mismatches_equal_to_contracted\": %zu, \"verdict\": \"%s\"}}\n",
                        e.info().name.c_str(), e.info().arch_option.c_str(), wide ? "wide (bit 5)" : "narrow (bit 4)", want.size(), with_c, bad, contracted_hits, bad == 0 ? "exact" : (contracted_hits ? "contracted (an FMA on the path)" : "mismatch"));
            return bad == 0 ? 0 : 5;
        }
        if (cmd == "ceiling") {  // ceiling [--bytes N] [--depth 64] [--contexts a,b,c] [--device N]: forms A and B of the attainable-bandwidth diagnostic, one line per (form, contexts)
            engine e; e.init(device_arg(argc, argv), IGNPOW_ALGO_KAWPOW_094);
            uint64_t bytes = 2ull << 30; uint32_t depth = 64; std::string ctx = "1024,2048,4096,8192,16384,32768,65536", widths;
            unsigned mblock = 256, mshared = 0, mlanes = 8;   // the matched-resource form (--block, --shared: the rung kernel's block and shared bytes per block)
            for (int i = 1; i + 1 < argc; ++i) {
                if (std::strcmp(argv[i], "--bytes") == 0) bytes = u64(argv[i + 1]);
                if (std::strcmp(argv[i], "--depth") == 0) depth = (uint32_t)u64(argv[i + 1]);
                if (std::strcmp(argv[i], "--contexts") == 0) ctx = argv[i + 1];
                if (std::strcmp(argv[i], "--widths") == 0) widths = argv[i + 1];  // form A at request widths (bytes) at each contexts value; forms A and B at 128 are skipped
                if (std::strcmp(argv[i], "--block") == 0) mblock = (unsigned)u64(argv[i + 1]);
                if (std::strcmp(argv[i], "--shared") == 0) mshared = (unsigned)u64(argv[i + 1]);
                if (std::strcmp(argv[i], "--lanes") == 0) mlanes = (unsigned)u64(argv[i + 1]);   // 16: the CC-B twin (16 lanes x 8 bytes per request)
            }
            std::vector<uint32_t> cs; { std::stringstream ss(ctx); std::string t; while (std::getline(ss, t, ',')) if (!t.empty()) cs.push_back((uint32_t)u64(t.c_str())); }
            if (!widths.empty()) {
                std::vector<uint32_t> ws; { std::stringstream ss(widths); std::string t; while (std::getline(ss, t, ',')) if (!t.empty()) ws.push_back((uint32_t)u64(t.c_str())); }
                // the shared shape first (eight lanes, W/128 coalesced steps; forms A and B), then the wide-group form A
                for (int form = 0; form < 2; ++form) for (uint32_t w : ws) for (uint32_t c : cs) {
                    double gbs = 0, rps = 0, pw = 0; uint32_t regs = 0;
                    e.ceiling_lanes(bytes, w, c, depth, form == 1, gbs, rps, pw, regs, mblock, mshared, mlanes);
                    // the matched-resource form (the outside review's control, 18:1x UK): the same probe launched with the rung kernel's block and
                    // dynamic shared bytes per block, so the resident occupancy is the kernel's; named in the form and the fields
                    std::printf("{\"ceiling_width\": {\"device\": \"%s\", \"form\": \"%s%s\", \"shape\": \"%s\", \"bytes\": \"%llu\", \"width\": %u, \"contexts\": %u, \"reads_per_context\": %u, \"gbs\": %.1f, \"requests_per_s\": %.0f, \"power_w\": %.1f, \"uj_per_request\": %.4f, \"registers\": %u, \"block\": %u, \"shared_bytes\": %u, \"dram_sectors\": \"proposed (no profiler on the host)\", \"l2\": \"proposed (no profiler on the host)\", \"grade\": \"telemetry\"}}\n",
                                e.info().name.c_str(), form == 0 ? "A-independent" : "B-dependent", mshared ? "-matched" : "", mlanes == 16 ? "16-lanes-x-8-bytes-x-W/128-steps" : "8-lanes-x-W/128-steps", (unsigned long long)bytes, w, c, depth, gbs, rps, pw, pw > 0 ? pw / rps * 1e6 : 0.0, regs, mblock, mshared);
                    std::fflush(stdout);
                }
                for (uint32_t w : ws) for (uint32_t c : cs) {
                    double gbs = 0, rps = 0, pw = 0; uint32_t regs = 0;
                    e.ceiling_width(bytes, w, c, depth, gbs, rps, pw, regs);
                    std::printf("{\"ceiling_width\": {\"device\": \"%s\", \"form\": \"A-independent\", \"shape\": \"wide-group\", \"bytes\": \"%llu\", \"width\": %u, \"contexts\": %u, \"reads_per_context\": %u, \"gbs\": %.1f, \"requests_per_s\": %.0f, \"power_w\": %.1f, \"uj_per_request\": %.4f, \"registers\": %u, \"dram_sectors\": \"proposed (no profiler on the host)\", \"l2\": \"proposed (no profiler on the host)\", \"grade\": \"telemetry\"}}\n",
                                e.info().name.c_str(), (unsigned long long)bytes, w, c, depth, gbs, rps, pw, pw > 0 ? pw / rps * 1e6 : 0.0, regs);
                    std::fflush(stdout);
                }
                return 0;
            }
            // the card's dependent-read latency over the same footprint (single thread, pure pointer chase), then the
            // outstanding requests each point exposes by Little's law: achieved bytes per second times latency over 128
            const double lat = e.latency_ns(bytes, 200000);
            std::printf("{\"latency\": {\"device\": \"%s\", \"bytes\": \"%llu\", \"ns_per_dependent_read\": %.1f, \"method\": \"single-thread pointer chase, 200000 reads, full-period permutation over 128-byte lines\"}}\n", e.info().name.c_str(), (unsigned long long)bytes, lat);
            std::fflush(stdout);
            for (int form = 0; form < 2; ++form) for (uint32_t c : cs) {
                double gbs = 0, rps = 0, w = 0; uint32_t regs = 0;
                e.ceiling(bytes, depth, c, form == 1, gbs, rps, w, regs);
                const double uj = w > 0 ? w / rps * 1e6 : 0;
                const double outstanding = gbs * 1e9 * lat * 1e-9 / 128.0;
                std::printf("{\"ceiling\": {\"device\": \"%s\", \"form\": \"%s\", \"bytes\": \"%llu\", \"depth\": %u, \"contexts\": %u, \"gbs\": %.1f, \"requests_per_s\": %.0f, \"power_w\": %.1f, \"uj_per_request\": %.4f, \"registers\": %u, \"sm_count\": %u, \"latency_ns\": %.1f, \"outstanding_exposed\": %.0f, \"grade\": \"telemetry\"}}\n",
                            e.info().name.c_str(), form == 0 ? "A-independent" : "B-dependent", (unsigned long long)bytes, depth, c, gbs, rps, w, uj, regs, e.sm_count(), lat, outstanding);
                std::fflush(stdout);
            }
            return 0;
        }
        if (cmd == "bwprobe") {  // bwprobe [--bytes N] [--device N]: the card's linear and random-line read throughput
            engine e; e.init(device_arg(argc, argv), IGNPOW_ALGO_KAWPOW_094);
            uint64_t bytes = 1ull << 30;
            for (int i = 1; i + 1 < argc; ++i) if (std::strcmp(argv[i], "--bytes") == 0) bytes = u64(argv[i + 1]);
            double lin = 0, rnd = 0; e.bwprobe(bytes, lin, rnd);
            telemetry t = e.read_telemetry();
            std::printf("{\"bwprobe\": {\"device\": \"%s\", \"bytes\": \"%llu\", \"linear_gbs\": %.1f, \"random_128b_gbs\": %.1f, \"mem_mhz\": %u}}\n", e.info().name.c_str(), (unsigned long long)bytes, lin, rnd, t.mem_mhz);
            return 0;
        }
    } catch (const std::exception& ex) {
        std::fprintf(stderr, "fault: %s\n", ex.what());
        std::printf("{\"t\": \"fault\", \"code\": \"engine\", \"message\": \"%s\", \"recoverable\": false}\n", json_escape(ex.what()).c_str());
        return 5;
    }
    std::fprintf(stderr, "unknown command %s\n", cmd.c_str());
    return 2;
}
