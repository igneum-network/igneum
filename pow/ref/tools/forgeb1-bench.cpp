// forgeb1-bench: vectors and measurements of Forge F1 under B1 (forgeb1.h; docs/igneumhash/forge/F1-B1.md).
//
//   forgeb1-bench --params '<json>' info
//       the variant tag, pid, program id, N, depth and proof bytes
//   forgeb1-bench --params '<json>' gen <out.json> [seed]
//       known vectors (text section 8): per (epoch_seed, fill_seed, transcript) r(v) for v in [2, 16), X[0 .. 16),
//       root, the challenge indices and the proof; each re-verified
//   forgeb1-bench --params '<json>' check <vectors.json>
//       re-proves and re-verifies every vector
//   forgeb1-bench --params '<json>' bench [fills]
//       the measurement line (text section 7): per fill the fill and commitment times, the fill's median and p99,
//       the prover's peak RSS, the traffic per fill, the proof bytes, the verifier's median and p99
//
// The params document is mandatory: {"schema": 1, "graph": 0|1, "memory_bytes": 67108864|268435456|1073741824|
// 2147483648, "label_bytes": 128|256|512|1024, "challenges": q, "batch": B, "chain_id": 7310, "epoch_blocks": 4096,
// "test_only": 0|1}.

#include "forgeb1.h"

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

uint32_t depth_of(const b1_params& p) { uint32_t d = 0; for (uint64_t t = b1_nodes(&p); t > 1; t >>= 1) ++d; return d; }

int info(const b1_params& p) {
    char tag[64]; b1_variant_tag(&p, tag, sizeof tag);
    uint8_t pid[32], prog[32]; b1_pid(&p, pid); b1_program_id(&p, prog);
    std::printf("variant %s\npid     %s\nprogram %s\nnodes   %llu\ndepth   %u\nproof_bytes %llu\n", tag, hex(pid, 32).c_str(), hex(prog, 32).c_str(),
                (unsigned long long)b1_nodes(&p), depth_of(p), (unsigned long long)b1_proof_bytes(&p));
    return 0;
}

int gen(const b1_params& p, const std::string& out, uint64_t seed) {
    rng64 r(seed);
    char tag[64]; b1_variant_tag(&p, tag, sizeof tag);
    uint8_t pid[32], prog[32]; b1_pid(&p, pid); b1_program_id(&p, prog);
    std::ofstream o(out);
    o << "{\n  \"candidate\": \"forgeb1\",\n  \"class\": \"known\",\n  \"variant\": \"" << tag << "\",\n  \"generator\": \"" << ignpow_build_string()
      << "\",\n  \"seed\": \"" << seed << "\",\n  \"params\": " << g_params << ",\n  \"pid\": \"" << hex(pid, 32) << "\",\n  \"program_id\": \"" << hex(prog, 32) << "\",\n  \"vectors\": [\n";
    bool first = true;
    for (int h = 0; h < 4; ++h) {
        uint8_t es[32], fs[32], tr[32];
        if (h == 0) { std::memset(es, 0, 32); std::memset(fs, 0, 32); std::memset(tr, 0, 32); }
        else if (h == 1) { std::memset(es, 0xff, 32); std::memset(fs, 0xff, 32); std::memset(tr, 0xff, 32); }
        else { r.bytes(es, 32); r.bytes(fs, 32); r.bytes(tr, 32); }
        uint64_t fill = 0, commit = 0; int st = 0;
        b1_prover* pr = b1_prover_create(&p, es, fs, &fill, &commit, &st);
        if (!pr) { std::fprintf(stderr, "prover: %d\n", st); return 2; }
        uint8_t root[32]; b1_prover_root(pr, root);
        std::fprintf(stderr, "triple %d: fill %llu ms, commit %llu ms\n", h, (unsigned long long)fill, (unsigned long long)commit);
        std::vector<uint8_t> proof(b1_proof_bytes(&p));
        std::vector<uint64_t> idx(p.challenges); b1_challenges(&p, tr, idx.data());
        if (b1_prove(pr, tr, proof.data()) != IGNPOW_OK) return 2;
        if (b1_verify(&p, es, fs, tr, prog, proof.data(), proof.size()) != IGNPOW_OK) { std::fprintf(stderr, "self-verification failed\n"); return 2; }
        o << (first ? "" : ",\n") << "    {\"epoch_seed\": \"" << hex(es, 32) << "\", \"fill_seed\": \"" << hex(fs, 32) << "\", \"transcript\": \"" << hex(tr, 32) << "\", \"parents\": [";
        for (uint64_t v = 2; v < 16; ++v) o << (v > 2 ? ", " : "") << b1_parent(&p, es, v);
        o << "], \"labels\": [";
        for (uint64_t v = 0; v < 16; ++v) o << (v ? ", " : "") << "\"" << hex(b1_prover_label(pr, v), p.label_bytes) << "\"";
        o << "], \"root\": \"" << hex(root, 32) << "\", \"challenges\": [";
        for (uint32_t k = 0; k < p.challenges; ++k) o << (k ? ", " : "") << idx[k];
        o << "], \"proof\": \"" << hex(proof.data(), proof.size()) << "\"}";
        first = false;
        b1_prover_free(pr);
    }
    o << "\n  ]\n}\n";
    std::printf("wrote known vectors to %s\n", out.c_str());
    return 0;
}

int check(const b1_params& p, const std::string& path) {
    std::ifstream in(path);
    if (!in) { std::fprintf(stderr, "cannot read %s\n", path.c_str()); return 2; }
    uint8_t prog[32]; b1_program_id(&p, prog);
    std::string line;
    size_t n = 0, bad = 0;
    std::vector<uint8_t> proof(b1_proof_bytes(&p));
    while (std::getline(in, line)) {
        if (line.find("\"root\"") == std::string::npos) continue;
        std::vector<uint8_t> es, fs, tr; unhex(field(line, "epoch_seed"), es); unhex(field(line, "fill_seed"), fs); unhex(field(line, "transcript"), tr);
        if (es.size() != 32 || fs.size() != 32 || tr.size() != 32) { ++n; ++bad; continue; }
        int st = 0;
        b1_prover* pr = b1_prover_create(&p, es.data(), fs.data(), nullptr, nullptr, &st);
        if (!pr) return 2;
        uint8_t root[32]; b1_prover_root(pr, root);
        b1_prove(pr, tr.data(), proof.data());
        std::vector<uint8_t> want_proof; unhex(field(line, "proof"), want_proof);
        const bool ok_prove = hex(root, 32) == field(line, "root") && want_proof == proof;
        const bool ok_verify = b1_verify(&p, es.data(), fs.data(), tr.data(), prog, want_proof.data(), want_proof.size()) == IGNPOW_OK;
        ++n;
        if (!ok_prove || !ok_verify) { ++bad; std::printf("MISMATCH fill_seed %s (prove %d verify %d)\n", field(line, "fill_seed").substr(0, 16).c_str(), ok_prove, ok_verify); }
        b1_prover_free(pr);
    }
    std::printf("forgeb1 %s: %zu vectors, %zu mismatches\n", path.c_str(), n, bad);
    return bad ? 1 : 0;
}

int bench(const b1_params& p, int fills) {
    char tag[64]; b1_variant_tag(&p, tag, sizeof tag);
    uint8_t prog[32]; b1_program_id(&p, prog);
    const uint64_t N = b1_nodes(&p), lb = p.label_bytes, pb = b1_proof_bytes(&p);
    const uint32_t d = depth_of(p);
    std::vector<uint8_t> proof(pb);
    std::vector<double> fill_ms_v, verify_us;
    uint64_t rss_peak = 0, fill_sum = 0, commit_sum = 0;
    const int na = fills < 1 ? 1 : fills;
    uint8_t es[32]; for (int k = 0; k < 32; ++k) es[k] = uint8_t(3 * k + 1);
    for (int i = 0; i < na; ++i) {
        uint8_t fs[32], tr[32]; for (int k = 0; k < 32; ++k) { fs[k] = uint8_t(7 * k + 1 + i); tr[k] = uint8_t(11 * k + 5 + i); }
        uint64_t fill = 0, commit = 0; int st = 0;
        auto a = std::chrono::steady_clock::now();
        b1_prover* pr = b1_prover_create(&p, es, fs, &fill, &commit, &st);
        if (!pr) { std::fprintf(stderr, "prover: %d\n", st); return 2; }
        b1_prove(pr, tr, proof.data());
        auto b = std::chrono::steady_clock::now();
        if (b1_verify(&p, es, fs, tr, prog, proof.data(), proof.size()) != IGNPOW_OK) { std::fprintf(stderr, "verification failed\n"); return 2; }
        auto c = std::chrono::steady_clock::now();
        fill_ms_v.push_back(std::chrono::duration<double, std::milli>(b - a).count());
        verify_us.push_back(std::chrono::duration<double, std::micro>(c - b).count());
        fill_sum += fill; commit_sum += commit;
        rss_peak = peak_rss_bytes();
        b1_prover_free(pr);
    }
    std::sort(fill_ms_v.begin(), fill_ms_v.end()); std::sort(verify_us.begin(), verify_us.end());
    // traffic per fill (text section 7): the fill writes N labels and reads two per label; the commitment reads N labels
    // and writes 2N - 1 nodes; the proof reads 3q labels and 3q x depth nodes
    const uint64_t fill_bytes = N * lb * 3, commit_bytes = N * lb + (2 * N - 1) * 32, proof_read = uint64_t(p.challenges) * (3 * lb + 3ull * d * 32);
    std::printf("bench forgeb1 %s: nodes %llu x %llu B, depth %u, one dependency chain per fill, prover peak RSS %llu bytes, per fill: fill %llu ms, commitment %llu ms and %llu bytes, "
                "fill+commit+prove median %.0f ms p99 %.0f ms, traffic per fill %llu bytes (fill %llu, commitment %llu, proof %llu), proof %llu bytes, "
                "verify median %.1f us p99 %.1f us max %.1f us (n=%d, single thread)\n",
                tag, (unsigned long long)N, (unsigned long long)lb, d, (unsigned long long)rss_peak,
                (unsigned long long)(fill_sum / uint64_t(na)), (unsigned long long)(commit_sum / uint64_t(na)), (unsigned long long)((2 * N - 1) * 32),
                fill_ms_v[fill_ms_v.size() / 2], fill_ms_v[(fill_ms_v.size() * 99) / 100],
                (unsigned long long)(fill_bytes + commit_bytes + proof_read), (unsigned long long)fill_bytes, (unsigned long long)commit_bytes, (unsigned long long)proof_read,
                (unsigned long long)pb, verify_us[verify_us.size() / 2], verify_us[(verify_us.size() * 99) / 100], verify_us.back(), na);
    return 0;
}

}  // namespace

int main(int argc, char** argv) {
    std::vector<char*> rest;
    for (int i = 1; i < argc; ++i) { if (std::strcmp(argv[i], "--params") == 0 && i + 1 < argc) g_params = argv[++i]; else rest.push_back(argv[i]); }
    if (rest.empty() || !g_params) { std::fprintf(stderr, "usage: see file header (the params document is mandatory)\n"); return 2; }
    size_t n = 0;
    if (ignpow_vectors_check(IGNPOW_ALGO_FORGEB1, &n) != IGNPOW_OK) { std::fprintf(stderr, "self-test failed\n"); return 3; }
    b1_params p;
    if (b1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    const std::string cmd = rest[0];
    if (cmd == "info") return info(p);
    if (cmd == "gen" && rest.size() >= 2) return gen(p, rest[1], rest.size() >= 3 ? std::strtoull(rest[2], nullptr, 10) : 20261010ull);
    if (cmd == "check" && rest.size() >= 2) return check(p, rest[1]);
    if (cmd == "bench") return bench(p, rest.size() >= 2 ? std::atoi(rest[1]) : 5);
    std::fprintf(stderr, "usage: see file header\n");
    return 2;
}
