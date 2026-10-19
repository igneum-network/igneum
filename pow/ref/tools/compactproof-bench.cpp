// compactproof-bench: vectors and measurements of the compact-proof research reference (compactproof.h).
//
//   compactproof-bench --params '<json>' info
//       the variant tag, pid, T, depth and proof bytes
//   compactproof-bench --params '<json>' gen <out.json> [seed]
//       known vectors: per (header, nonce) the root, Y_L and the proof (hex), re-verified by the light verifier
//   compactproof-bench --params '<json>' check <vectors.json>
//       re-proves and re-verifies every vector
//   compactproof-bench --params '<json>' bench [attempts]
//       the measurement line the ablation runner reads (experimental points, never claimed secure settings): per
//       attempt (one fill under F0: the nonce is inside x) the fill and commitment times, the attempt's median and
//       p99, the prover's peak RSS, the memory traffic per attempt, the proof bytes, the light verifier's median and p99
//
// The params document is mandatory: {"schema": 1, "memory_bytes": 268435456|1073741824|2147483648, "label_bytes":
// 32|64|128, "challenges": L, "chain_id": 7310, "epoch_blocks": 4096, "test_only": 0|1}.

#include "compactproof.h"

#include <sys/resource.h>

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <string>
#include <vector>

namespace {

const char* g_params = nullptr;

std::string hex(const uint8_t* p, size_t n) {
    static const char* d = "0123456789abcdef";
    std::string s; s.reserve(2 * n);
    for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; }
    return s;
}

bool unhex(const std::string& s, std::vector<uint8_t>& out) {
    if (s.size() % 2) return false;
    out.resize(s.size() / 2);
    auto v = [](char c) -> int { if (c >= '0' && c <= '9') return c - '0'; if (c >= 'a' && c <= 'f') return c - 'a' + 10; return -1; };
    for (size_t i = 0; i < out.size(); ++i) { int a = v(s[2 * i]), b = v(s[2 * i + 1]); if (a < 0 || b < 0) return false; out[i] = uint8_t(a * 16 + b); }
    return true;
}

std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t p = line.find(k);
    if (p == std::string::npos) return "";
    p += k.size();
    while (p < line.size() && line[p] == ' ') ++p;
    if (line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    size_t q = line.find_first_of(",}", p);
    return line.substr(p, q - p);
}

struct rng64 {
    uint64_t s;
    explicit rng64(uint64_t seed) : s(seed ? seed : 0x9E3779B97F4A7C15ull) {}
    uint64_t next() { s ^= s >> 12; s ^= s << 25; s ^= s >> 27; return s * 0x2545F4914F6CDD1Dull; }
    void bytes(uint8_t* p, size_t n) { for (size_t i = 0; i < n; ++i) p[i] = uint8_t(next() >> 56); }
};

uint64_t peak_rss_bytes() {
    struct rusage ru; getrusage(RUSAGE_SELF, &ru);
#ifdef __APPLE__
    return uint64_t(ru.ru_maxrss);
#else
    return uint64_t(ru.ru_maxrss) * 1024;
#endif
}

int info(const cp_params& p) {
    char tag[64]; cp_variant_tag(&p, tag, sizeof tag);
    uint8_t pid[32]; cp_pid(&p, pid);
    uint32_t d = 0; for (uint64_t t = cp_labels(&p); t > 1; t >>= 1) ++d;
    std::printf("variant %s\npid     %s\nlabels  %llu\ndepth   %u\nproof_bytes %llu\n", tag, hex(pid, 32).c_str(), (unsigned long long)cp_labels(&p), d, (unsigned long long)cp_proof_bytes(&p));
    return 0;
}

// Known vectors: per seed the root, the first sixteen labels with the parents of 2..16 (the localiser), y and the proof.
int gen(const cp_params& p, const std::string& out, uint64_t seed) {
    rng64 r(seed);
    char tag[64]; cp_variant_tag(&p, tag, sizeof tag);
    uint8_t pid[32], prog[32]; cp_pid(&p, pid); cp_program_id(&p, prog);
    std::ofstream o(out);
    o << "{\n  \"candidate\": \"compactproof\",\n  \"class\": \"known\",\n  \"variant\": \"" << tag << "\",\n  \"generator\": \"" << ignpow_build_string()
      << "\",\n  \"seed\": \"" << seed << "\",\n  \"params\": " << g_params << ",\n  \"pid\": \"" << hex(pid, 32) << "\",\n  \"program_id\": \"" << hex(prog, 32) << "\",\n  \"vectors\": [\n";
    bool first = true;
    for (int h = 0; h < 4; ++h) {
        uint8_t sd[32];
        if (h == 0) std::memset(sd, 0, 32); else if (h == 1) std::memset(sd, 0xff, 32); else r.bytes(sd, 32);
        uint64_t fill = 0, commit = 0; int st = 0;
        cp_prover* pr = cp_prover_create(&p, sd, &fill, &commit, &st);
        if (!pr) { std::fprintf(stderr, "prover: %d\n", st); return 2; }
        uint8_t root[32]; cp_prover_root(pr, root);
        std::fprintf(stderr, "seed %d: fill %llu ms, commit %llu ms\n", h, (unsigned long long)fill, (unsigned long long)commit);
        std::vector<uint8_t> proof(cp_proof_bytes(&p));
        uint8_t y[32], y2[32];
        if (cp_prove(pr, y, proof.data()) != IGNPOW_OK) return 2;
        if (cp_verify(&p, sd, prog, y, proof.data(), proof.size(), y2) != IGNPOW_OK || std::memcmp(y, y2, 32)) { std::fprintf(stderr, "self-verification failed\n"); return 2; }
        o << (first ? "" : ",\n") << "    {\"seed\": \"" << hex(sd, 32) << "\", \"root\": \"" << hex(root, 32) << "\", \"labels\": [";
        for (uint64_t i = 0; i < 16; ++i) o << (i ? ", " : "") << "\"" << hex(cp_prover_label(pr, i), p.label_bytes) << "\"";
        o << "], \"parents\": [";
        for (uint64_t i = 2; i < 16; ++i) o << (i > 2 ? ", " : "") << (cp_parent(&p, i + 1) - 1);
        o << "], \"y\": \"" << hex(y, 32) << "\", \"proof\": \"" << hex(proof.data(), proof.size()) << "\"}";
        first = false;
        cp_prover_free(pr);
    }
    o << "\n  ]\n}\n";
    std::printf("wrote known vectors to %s\n", out.c_str());
    return 0;
}

int check(const cp_params& p, const std::string& path) {
    std::ifstream in(path);
    if (!in) { std::fprintf(stderr, "cannot read %s\n", path.c_str()); return 2; }
    uint8_t prog[32]; cp_program_id(&p, prog);
    std::string line;
    size_t n = 0, bad = 0;
    std::vector<uint8_t> proof(cp_proof_bytes(&p));
    while (std::getline(in, line)) {
        if (line.find("\"root\"") == std::string::npos) continue;
        std::vector<uint8_t> sd; unhex(field(line, "seed"), sd);
        int st = 0;
        cp_prover* pr = cp_prover_create(&p, sd.data(), nullptr, nullptr, &st);
        if (!pr) return 2;
        uint8_t root[32], y[32], y2[32]; cp_prover_root(pr, root);
        cp_prove(pr, y, proof.data());
        std::vector<uint8_t> want_proof; unhex(field(line, "proof"), want_proof);
        std::vector<uint8_t> want_y; unhex(field(line, "y"), want_y);
        const bool ok_prove = hex(root, 32) == field(line, "root") && hex(y, 32) == field(line, "y") && want_proof == proof;
        const bool ok_verify = want_y.size() == 32 && cp_verify(&p, sd.data(), prog, want_y.data(), want_proof.data(), want_proof.size(), y2) == IGNPOW_OK;
        ++n;
        if (!ok_prove || !ok_verify) { ++bad; std::printf("MISMATCH seed %s (prove %d verify %d)\n", field(line, "seed").substr(0, 16).c_str(), ok_prove, ok_verify); }
        cp_prover_free(pr);
    }
    std::printf("compactproof %s: %zu vectors, %zu mismatches\n", path.c_str(), n, bad);
    return bad ? 1 : 0;
}

int bench(const cp_params& p, int attempts) {
    char tag[64]; cp_variant_tag(&p, tag, sizeof tag);
    uint8_t prog[32]; cp_program_id(&p, prog);
    const uint64_t T = cp_labels(&p), lb = p.label_bytes, pb = cp_proof_bytes(&p);
    uint32_t d = 0; for (uint64_t t = T; t > 1; t >>= 1) ++d;
    std::vector<uint8_t> proof(pb);
    std::vector<double> attempt_ms, verify_us;
    uint64_t rss_peak = 0, fill_sum = 0, commit_sum = 0;
    const int na = attempts < 1 ? 1 : attempts;
    for (int i = 0; i < na; ++i) {
        uint8_t sd[32]; for (int k = 0; k < 32; ++k) sd[k] = uint8_t(7 * k + 1 + i);
        uint64_t fill = 0, commit = 0; int st = 0;
        auto a = std::chrono::steady_clock::now();
        cp_prover* pr = cp_prover_create(&p, sd, &fill, &commit, &st);
        if (!pr) { std::fprintf(stderr, "prover: %d\n", st); return 2; }
        uint8_t y[32], y2[32];
        cp_prove(pr, y, proof.data());
        auto b = std::chrono::steady_clock::now();
        if (cp_verify(&p, sd, prog, y, proof.data(), proof.size(), y2) != IGNPOW_OK) { std::fprintf(stderr, "verification failed\n"); return 2; }
        auto c = std::chrono::steady_clock::now();
        attempt_ms.push_back(std::chrono::duration<double, std::milli>(b - a).count());
        verify_us.push_back(std::chrono::duration<double, std::micro>(c - b).count());
        fill_sum += fill; commit_sum += commit;
        rss_peak = peak_rss_bytes();
        cp_prover_free(pr);
    }
    std::sort(attempt_ms.begin(), attempt_ms.end()); std::sort(verify_us.begin(), verify_us.end());
    // traffic per attempt: the fill writes T labels and reads two per label; the commitment reads T labels and writes 2T - 1
    // nodes; the proof reads 3L labels and 3L x depth nodes (the reference allocation; the cheapest schedule is the notes')
    const uint64_t fill_bytes = T * lb * 3, commit_bytes = T * lb + (2 * T - 1) * 32, proof_read = uint64_t(p.challenges) * (3 * lb + 3ull * d * 32);
    std::printf("bench compactproof %s: labels %llu x %llu B, depth %u, prover peak RSS %llu bytes, per attempt (one fill, F0): fill %llu ms, commitment %llu ms and %llu bytes, "
                "attempt median %.0f ms p99 %.0f ms, traffic per attempt %llu bytes (fill %llu, commitment %llu, proof %llu), proof %llu bytes, "
                "verify median %.1f us p99 %.1f us max %.1f us (n=%d, light verifier, single thread)\n",
                tag, (unsigned long long)T, (unsigned long long)lb, d, (unsigned long long)rss_peak,
                (unsigned long long)(fill_sum / uint64_t(na)), (unsigned long long)(commit_sum / uint64_t(na)), (unsigned long long)((2 * T - 1) * 32),
                attempt_ms[attempt_ms.size() / 2], attempt_ms[(attempt_ms.size() * 99) / 100],
                (unsigned long long)(fill_bytes + commit_bytes + proof_read), (unsigned long long)fill_bytes, (unsigned long long)commit_bytes, (unsigned long long)proof_read,
                (unsigned long long)pb, verify_us[verify_us.size() / 2], verify_us[(verify_us.size() * 99) / 100], verify_us.back(), na);
    return 0;
}

}  // namespace

int main(int argc, char** argv) {
    g_params = std::getenv("IGNPOW_PARAMS");
    if (argc >= 3 && std::strcmp(argv[1], "--params") == 0) { g_params = argv[2]; argv += 2; argc -= 2; }
    if (argc < 2 || !g_params) { std::fprintf(stderr, "usage: see file header (the params document is mandatory)\n"); return 2; }
    size_t n = 0;
    if (ignpow_vectors_check(IGNPOW_ALGO_COMPACTPROOF, &n) != IGNPOW_OK) { std::fprintf(stderr, "self-test failed\n"); return 3; }
    cp_params p;
    if (cp_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    std::string cmd = argv[1];
    if (cmd == "info") return info(p);
    if (cmd == "gen" && argc >= 3) return gen(p, argv[2], argc >= 4 ? std::strtoull(argv[3], nullptr, 10) : 20261010ull);
    if (cmd == "check" && argc >= 3) return check(p, argv[2]);
    if (cmd == "bench") return bench(p, argc >= 3 ? std::atoi(argv[2]) : 5);
    std::fprintf(stderr, "usage: see file header\n");
    return 2;
}
