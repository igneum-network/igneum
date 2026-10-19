// kiln-verify-bench: the Kiln verifier's exactness and cost (version 22, FORM-FORGE.md section 6; the Kiln verifier seat).
//
//   kiln-verify-bench --params '<json>' [--height H] [--n N] [--list L] [--light] [--json out.json] [--vectors set.json ...]
//
// --vectors: a vector set of the document (ignpow-vectors' unseen, nonce-boundary or transitions class, rows of height, header,
// nonce, mix, hash) checked through the FAST path on this context (resident unless --light): every row's mix and hash must equal
// the file's, which the reference render wrote; the count of rows and mismatches is in the line.
//
// Creates the document's context (full: the 1 GiB dataset resident; --light: the cache only, the rows derived), draws N random
// (header, nonce) pairs, hashes each by BOTH paths (0 the reference render kiln::attempt, 1 the fast render kiln::attempt_fast)
// and counts mismatches of mix and hash; then times the fast path and the reference path as a node sees them: a hash (median,
// p99 over N) and a LIST of L sequential verifies (ms, the best of three lists), for a list of random nonces (every hash its own
// batch: the programme cache misses) and a list of consecutive nonces of one batch (the cache hits after the first). Also the
// distinct dataset rows (256-byte items) the fast render's TEX fetches touched a hash (mean over N) and the bytes that is.
// Pin the process to one core (taskset -c) for the figure of record.
#include "ignpow.h"
#include "igneumhash.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace {
struct rng64 {
    uint64_t s;
    explicit rng64(uint64_t seed) : s(seed ? seed : 0x9E3779B97F4A7C15ull) {}
    uint64_t next() { s ^= s << 13; s ^= s >> 7; s ^= s << 17; return s; }
    void bytes(uint8_t* p, size_t n) { for (size_t i = 0; i < n; ++i) p[i] = uint8_t(next()); }
};
using clk = std::chrono::steady_clock;
double us_between(clk::time_point a, clk::time_point b) { return std::chrono::duration<double, std::micro>(b - a).count(); }

struct row { std::vector<uint8_t> h; uint64_t nonce; };

// a list of L verifies by one path, three times, the best total in ms; the per-hash samples of the first pass
double time_list(ignpow_ctx* c, uint64_t height, const std::vector<row>& rows, int path, std::vector<double>* samples) {
    double best = 1e300;
    for (int rep = 0; rep < 3; ++rep) {
        const auto t0 = clk::now();
        for (const auto& r : rows) {
            uint8_t mix[32], out[32];
            const auto a = clk::now();
            ignh_kiln_hash_path(c, height, r.h.data(), r.nonce, mix, out, path);
            if (rep == 0 && samples) samples->push_back(us_between(a, clk::now()));
        }
        best = std::min(best, us_between(t0, clk::now()) / 1000.0);
    }
    return best;
}
// a vector set's rows: height, header, nonce, mix, hash (the generator's own row shape; other keys ignored)
struct vrow { uint64_t height, nonce; uint8_t header[32], mix[32], hash[32]; };
bool unhex32(const std::string& h, uint8_t* out) { if (h.size() != 64) return false; for (int i = 0; i < 32; ++i) out[i] = uint8_t(std::stoul(h.substr(size_t(i) * 2, 2), nullptr, 16)); return true; }
bool read_vectors(const char* path, std::vector<vrow>& rows) {
    FILE* f = std::fopen(path, "rb"); if (!f) return false;
    std::string t; char buf[65536]; size_t k; while ((k = std::fread(buf, 1, sizeof buf, f)) > 0) t.append(buf, k); std::fclose(f);
    auto field = [&](size_t from, const char* key, std::string& out) -> bool {
        const size_t p = t.find(std::string("\"") + key + "\":", from); if (p == std::string::npos) return false;
        size_t q = t.find_first_not_of(" ", p + std::strlen(key) + 3); if (q == std::string::npos) return false;
        if (t[q] == '"') { const size_t e = t.find('"', q + 1); out = t.substr(q + 1, e - q - 1); } else { const size_t e = t.find_first_of(",}", q); out = t.substr(q, e - q); }
        return true;
    };
    size_t pos = t.find("\"vectors\"");
    if (pos == std::string::npos) return false;
    while ((pos = t.find("{\"height\"", pos)) != std::string::npos) {
        const size_t end = t.find('}', pos);
        std::string h, hd, n, m, hs; vrow r;
        if (!field(pos, "height", h) || !field(pos, "header", hd) || !field(pos, "nonce", n) || !field(pos, "mix", m) || !field(pos, "hash", hs)) return false;
        r.height = std::strtoull(h.c_str(), nullptr, 10); r.nonce = std::strtoull(n.c_str(), nullptr, 10);
        if (!unhex32(hd, r.header) || !unhex32(m, r.mix) || !unhex32(hs, r.hash)) return false;
        rows.push_back(r); pos = end;
    }
    return !rows.empty();
}
double median(std::vector<double> v) { std::sort(v.begin(), v.end()); return v.empty() ? 0 : v[v.size() / 2]; }
double p99(std::vector<double> v) { std::sort(v.begin(), v.end()); return v.empty() ? 0 : v[(v.size() * 99) / 100]; }
}  // namespace

int main(int argc, char** argv) {
    const char* params = std::getenv("IGNPOW_PARAMS");
    uint64_t height = 0; int n = 256, list = 256; bool light = false; const char* json_out = nullptr; std::vector<const char*> vector_files;
    for (int i = 1; i < argc; ++i) {
        const std::string a = argv[i];
        if (a == "--params" && i + 1 < argc) params = argv[++i];
        else if (a == "--height" && i + 1 < argc) height = std::strtoull(argv[++i], nullptr, 10);
        else if (a == "--n" && i + 1 < argc) n = std::atoi(argv[++i]);
        else if (a == "--list" && i + 1 < argc) list = std::atoi(argv[++i]);
        else if (a == "--light") light = true;
        else if (a == "--json" && i + 1 < argc) json_out = argv[++i];
        else if (a == "--vectors" && i + 1 < argc) vector_files.push_back(argv[++i]);
        else { std::fprintf(stderr, "usage: kiln-verify-bench --params '<json>' [--height H] [--n N] [--list L] [--light] [--json out.json]\n"); return 2; }
    }
    if (!params) { std::fprintf(stderr, "--params or IGNPOW_PARAMS is needed\n"); return 2; }
    int st = 0;
    const auto t0 = clk::now();
    ignpow_ctx* c = ignpow_ctx_create(IGNPOW_ALGO_IGNEUMHASH, params, height, light ? 0 : 1, &st);
    if (!c) { std::fprintf(stderr, "context refused: status %d\n", st); return 2; }
    const double build_ms = us_between(t0, clk::now()) / 1000.0;
    const uint64_t dataset = ignpow_dataset_bytes(IGNPOW_ALGO_IGNEUMHASH, params, height, &st);
    ignh_params hp; if (ignh_params_parse(params, &hp) != IGNPOW_OK) { std::fprintf(stderr, "params refused\n"); return 2; }
    const uint64_t epoch_blocks = hp.epoch_blocks;

    // (1) exactness: N random (header, nonce) by both paths
    rng64 r(0x4B494C4Eull ^ height);
    std::vector<row> rows; rows.resize(size_t(n));
    for (auto& w : rows) { w.h.resize(32); r.bytes(w.h.data(), 32); w.nonce = r.next(); }
    size_t mism = 0; double rows_sum = 0;
    ignh_kiln_count_rows(1);
    for (const auto& w : rows) {
        uint8_t m0[32], o0[32], m1[32], o1[32];
        if (ignh_kiln_hash_path(c, height, w.h.data(), w.nonce, m0, o0, 0) != IGNPOW_OK) { std::fprintf(stderr, "reference path refused\n"); return 2; }
        if (ignh_kiln_hash_path(c, height, w.h.data(), w.nonce, m1, o1, 1) != IGNPOW_OK) { std::fprintf(stderr, "fast path refused\n"); return 2; }
        rows_sum += double(ignh_kiln_rows_read());
        if (std::memcmp(m0, m1, 32) || std::memcmp(o0, o1, 32)) ++mism;
    }
    ignh_kiln_count_rows(0);
    const double rows_mean = rows_sum / n;

    // (1b) the document's vector sets through the fast path
    std::string vec_line; size_t vec_rows = 0, vec_bad = 0;
    for (const char* vf : vector_files) {
        std::vector<vrow> vs;
        if (!read_vectors(vf, vs)) { std::fprintf(stderr, "cannot read the vector set %s\n", vf); return 2; }
        // a row of this context's epoch runs on it (resident unless --light); a row of another epoch on a light context of its own
        // epoch (the cache only, the rows derived: the scalar path), one per epoch, so a set that spans epochs is checked whole
        size_t bad = 0, here = 0, elsewhere = 0;
        std::vector<std::pair<uint64_t, ignpow_ctx*>> others;
        for (const auto& v : vs) {
            uint8_t m1[32], o1[32];
            ignpow_ctx* cv = c;
            if (ignpow_ctx_epoch(c) != v.height / epoch_blocks) {
                for (auto& o : others) if (o.first == v.height / epoch_blocks) cv = o.second;
                if (cv == c) { int st2 = 0; cv = ignpow_ctx_create(IGNPOW_ALGO_IGNEUMHASH, params, v.height, 0, &st2); if (!cv) { std::fprintf(stderr, "context for height %llu refused\n", (unsigned long long)v.height); return 2; } others.emplace_back(v.height / epoch_blocks, cv); }
                ++elsewhere;
            } else ++here;
            const int st = ignh_kiln_hash_path(cv, v.height, v.header, v.nonce, m1, o1, 1);
            if (st != IGNPOW_OK || std::memcmp(m1, v.mix, 32) || std::memcmp(o1, v.hash, 32)) ++bad;
        }
        for (auto& o : others) ignpow_ctx_free(o.second);
        vec_rows += vs.size(); vec_bad += bad;
        vec_line += std::string(vec_line.empty() ? "" : ", ") + vf + " " + std::to_string(vs.size()) + " rows (" + std::to_string(here) + " on this context, " + std::to_string(elsewhere) + " on light contexts of their epochs) " + std::to_string(bad) + " mismatches";
    }
    mism += vec_bad;

    // (2) the lists: random nonces (a batch each) and one batch's consecutive nonces
    std::vector<row> mixed(rows.begin(), rows.begin() + std::min<size_t>(rows.size(), size_t(list)));
    while (mixed.size() < size_t(list)) { row w; w.h.resize(32); r.bytes(w.h.data(), 32); w.nonce = r.next(); mixed.push_back(w); }
    std::vector<row> one; one.resize(size_t(list));
    { uint8_t h[32]; r.bytes(h, 32); const uint64_t base = (r.next() >> 20) << 20; for (int i = 0; i < list; ++i) { one[i].h.assign(h, h + 32); one[i].nonce = base + uint64_t(i); } }
    std::vector<double> fast_us, ref_us;
    const double fast_mixed_ms = time_list(c, height, mixed, 1, &fast_us);
    const double fast_one_ms = time_list(c, height, one, 1, nullptr);
    const double ref_mixed_ms = time_list(c, height, mixed, 0, &ref_us);
    const double ref_one_ms = time_list(c, height, one, 0, nullptr);

    std::printf("kiln-verify-bench %s height %llu %s: dataset %llu bytes, context %.0f ms; exactness fast vs reference over %d random (header, nonce) plus the sets: %zu mismatches; "
                "rows read a hash (fast, distinct 256-byte items) %.1f = %.1f KiB; "
                "vector sets through the fast path: %s; "
                "fast: a hash median %.2f us p99 %.2f us, a %d-list %.3f ms (random batches) %.3f ms (one batch); "
                "reference: a hash median %.2f us p99 %.2f us, a %d-list %.3f ms (random batches) %.3f ms (one batch); build %s\n",
                light ? "light" : "resident", (unsigned long long)height, mism ? "MISMATCH" : "exact", (unsigned long long)dataset, build_ms, n, mism,
                rows_mean, rows_mean * 256.0 / 1024.0, vec_line.empty() ? "none" : vec_line.c_str(),
                median(fast_us), p99(fast_us), list, fast_mixed_ms, fast_one_ms,
                median(ref_us), p99(ref_us), list, ref_mixed_ms, ref_one_ms, ignpow_build_string());
    if (json_out) {
        FILE* f = std::fopen(json_out, "w");
        if (f) {
            std::fprintf(f, "{\"params\": %s, \"height\": %llu, \"context\": \"%s\", \"dataset_bytes\": %llu, \"context_ms\": %.0f, \"n\": %d, \"mismatches\": %zu, "
                        "\"rows_read_mean\": %.2f, \"bytes_read_mean\": %.0f, \"vector_rows\": %zu, \"vector_mismatches\": %zu, \"list\": %d, "
                        "\"fast\": {\"hash_median_us\": %.3f, \"hash_p99_us\": %.3f, \"list_random_batches_ms\": %.3f, \"list_one_batch_ms\": %.3f}, "
                        "\"reference\": {\"hash_median_us\": %.3f, \"hash_p99_us\": %.3f, \"list_random_batches_ms\": %.3f, \"list_one_batch_ms\": %.3f}, "
                        "\"build\": \"%s\"}\n",
                        params, (unsigned long long)height, light ? "light" : "resident", (unsigned long long)dataset, build_ms, n, mism, rows_mean, rows_mean * 256.0, vec_rows, vec_bad, list,
                        median(fast_us), p99(fast_us), fast_mixed_ms, fast_one_ms, median(ref_us), p99(ref_us), ref_mixed_ms, ref_one_ms, ignpow_build_string());
            std::fclose(f);
        }
    }
    ignpow_ctx_free(c);
    return mism ? 1 : 0;
}
