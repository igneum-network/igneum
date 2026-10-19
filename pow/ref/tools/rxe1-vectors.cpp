// rxe1-vectors: the spec-form vectors of SPEC-TEXT-v0.1.md section 12 for one
// RXE1 configuration, from the explicit harness inputs of section 6 (chain_id
// bytes, header_blob, epoch_seed, epoch_number, nonce) rather than the ABI's
// derived ones. Every file records the params document and the generator.
//
//   rxe1-vectors --params '<json>' info
//       the variant tag, the 72 record bytes, pid and the dataset binding constant
//   rxe1-vectors --params '<json>' gen <outdir> [seed] [--no-dataset]
//       writes trace.json (full intermediates: empty and maximum headers, two
//       epochs), known.json (the edge nonces 0, 1, 2^32-1, 2^32, 2^64-1 and
//       random nonces across epoch boundaries, with seed and out), schedule.json
//       (the descriptors of every epoch used, with the opcode census) and
//       dataset.json (cache and dataset digests per epoch, light and full)
//   rxe1-vectors --params '<json>' check <outdir>
//       recomputes every vector of trace.json and known.json
//   rxe1-vectors --params '<json>' campaign <count> <seed> <out.jsonl>
//       the differential campaign file: per nonce the inputs, seed, out and the trace digest (one line each)
//   rxe1-vectors --params '<json>' campaign-check <file.jsonl>
//   rxe1-vectors --params '<json>' eval <chain_id_hex> <header_hex> <epoch_seed_hex> <epoch_number> <nonce> [trace]
//       one evaluation; prints seed and out (and the trace object when asked)
//
// The chain id of the generated sets is the ASCII "igneum-rxe1-vectors" (19
// bytes); the epoch seeds are random per epoch from the recorded seed.

#include "rxe1.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>
#include <map>

namespace {

struct rng64 {
    uint64_t s;
    explicit rng64(uint64_t seed) : s(seed ? seed : 0x9E3779B97F4A7C15ull) {}
    uint64_t next() { s ^= s >> 12; s ^= s << 25; s ^= s >> 27; return s * 0x2545F4914F6CDD1Dull; }
    void bytes(uint8_t* p, size_t n) { for (size_t i = 0; i < n; ++i) p[i] = uint8_t(next() >> 56); }
};

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

const char* g_params = nullptr;
const uint8_t kChain[] = "igneum-rxe1-vectors";
const size_t kChainLen = 19;

// "key": value with or without whitespace after the colon (the second implementation's files carry none).
std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t p = line.find(k);
    if (p == std::string::npos) return "";
    p += k.size();
    while (p < line.size() && (line[p] == ' ' || line[p] == '\t')) ++p;
    if (p >= line.size()) return "";
    if (line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    size_t q = line.find_first_of(",}", p);
    return line.substr(p, q - p);
}

struct epoch_ctx {
    uint64_t number;
    uint8_t seed[32];
    rxe1_ctx* light = nullptr;
};

std::string header(const rxe1_params& p, const char* cls, uint64_t seed) {
    char tag[96];
    rxe1_variant_tag(&p, tag, sizeof tag);
    uint8_t pid[32];
    rxe1_pid(&p, pid);
    std::ostringstream o;
    o << "{\n  \"candidate\": \"rxe1\",\n  \"class\": \"" << cls << "\",\n  \"variant\": \"" << tag << "\",\n  \"generator\": \"" << ignpow_build_string()
      << "\",\n  \"seed\": \"" << seed << "\",\n  \"params\": " << g_params << ",\n  \"pid\": \"" << hex(pid, 32) << "\",\n  \"chain_id\": \"" << hex(kChain, kChainLen) << "\",\n";
    return o.str();
}

int info() {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    char tag[96]; rxe1_variant_tag(&p, tag, sizeof tag);
    uint8_t rec[RXE1_RECORD_BYTES], pid[32], dsd[32];
    rxe1_record_bytes(&p, rec); rxe1_pid(&p, pid); rxe1_dataset_spec_digest(dsd);
    std::printf("variant %s\nrecord  %s\npid     %s\ndataset_spec_digest %s\n", tag, hex(rec, rxe1_record_len(&p)).c_str(), hex(pid, 32).c_str(), hex(dsd, 32).c_str());
    return 0;
}

int gen(const std::string& dir, uint64_t seed, bool with_dataset) {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    rng64 r(seed);
    // epochs: 0, 1 (a boundary pair), 4095 and 4096 (the ABI's first epoch boundary at 4096 blocks), 598
    const uint64_t numbers[] = {0, 1, 4095, 4096, 598};
    std::vector<epoch_ctx> eps;
    for (uint64_t n : numbers) {
        epoch_ctx e; e.number = n; r.bytes(e.seed, 32);
        int st = 0;
        e.light = rxe1_ctx_create(&p, e.seed, 0, &st);
        if (!e.light) { std::fprintf(stderr, "context for epoch %llu: %d\n", (unsigned long long)n, st); return 2; }
        eps.push_back(e);
    }
    std::vector<uint8_t> maxhdr(4096);
    r.bytes(maxhdr.data(), maxhdr.size());
    // trace.json: empty header at epoch 0 nonce 0; maximum header at epoch 1 nonce random; a 32-byte header at epoch 4096
    {
        std::ofstream o(dir + "/trace.json");
        o << header(p, "trace", seed) << "  \"vectors\": [\n";
        struct tv { size_t ep; const uint8_t* h; size_t hl; uint64_t nonce; };
        uint8_t h32[32]; r.bytes(h32, 32);
        const tv tvs[] = {{0, nullptr, 0, 0}, {1, maxhdr.data(), 4096, r.next()}, {3, h32, 32, 0xFFFFFFFFull}};
        for (size_t i = 0; i < 3; ++i) {
            const tv& t = tvs[i];
            char* buf = nullptr; size_t len = 0;
            FILE* mem = open_memstream(&buf, &len);
            uint8_t out[32], sd[32];
            if (rxe1_eval(eps[t.ep].light, kChain, kChainLen, t.h, t.hl, eps[t.ep].number, t.nonce, out, sd, mem) != IGNPOW_OK) return 2;
            std::fclose(mem);
            o << "    " << std::string(buf, len) << (i + 1 < 3 ? ",\n" : "\n");
            std::free(buf);
        }
        o << "  ]\n}\n";
    }
    // known.json: per epoch, the edge nonces on one random 80-byte header, then random (header, nonce) pairs
    {
        std::ofstream o(dir + "/known.json");
        o << header(p, "known", seed) << "  \"vectors\": [\n";
        const uint64_t edges[] = {0, 1, 0xFFFFFFFFull, 0x100000000ull, 0xFFFFFFFFFFFFFFFFull};
        bool first = true;
        auto line = [&](const epoch_ctx& e, const uint8_t* h, size_t hl, uint64_t nonce) -> bool {
            uint8_t out[32], sd[32];
            if (rxe1_eval(e.light, kChain, kChainLen, h, hl, e.number, nonce, out, sd, nullptr) != IGNPOW_OK) return false;
            o << (first ? "" : ",\n") << "    {\"epoch_number\": " << e.number << ", \"epoch_seed\": \"" << hex(e.seed, 32) << "\", \"header_blob\": \"" << hex(h, hl)
              << "\", \"nonce\": \"" << nonce << "\", \"seed\": \"" << hex(sd, 32) << "\", \"out\": \"" << hex(out, 32) << "\"}";
            first = false;
            return true;
        };
        for (const epoch_ctx& e : eps) {
            uint8_t h80[80]; r.bytes(h80, 80);
            for (uint64_t n : edges) if (!line(e, h80, 80, n)) return 2;
            if (!line(e, nullptr, 0, r.next())) return 2;           // empty header
            if (!line(e, maxhdr.data(), 4096, r.next())) return 2;  // maximum header
            for (int i = 0; i < 6; ++i) { uint8_t h[32]; r.bytes(h, 32); if (!line(e, h, 32, r.next())) return 2; }
        }
        o << "\n  ]\n}\n";
    }
    // schedule.json: the descriptors per epoch (mode F repeats the same schedule; E differs per epoch) and the opcode census
    {
        std::ofstream o(dir + "/schedule.json");
        o << header(p, "schedule", seed) << "  \"epochs\": [\n";
        for (size_t i = 0; i < eps.size(); ++i) {
            char* buf = nullptr; size_t len = 0;
            FILE* mem = open_memstream(&buf, &len);
            // mode P (3): the schedule is a nonce's; the file carries the program of nonce 0 on the empty header per epoch
            const int st_s = p.mode == 3 ? rxe1_schedule_for_nonce(eps[i].light, kChain, kChainLen, nullptr, 0, eps[i].number, 0, mem)
                                         : rxe1_schedule(eps[i].light, kChain, kChainLen, eps[i].number, mem);
            if (st_s != IGNPOW_OK) return 2;
            std::fclose(mem);
            std::string s(buf, len);
            std::free(buf);
            size_t census[4] = {0, 0, 0, 0};
            for (size_t q = s.find("\"opcode\": "); q != std::string::npos; q = s.find("\"opcode\": ", q + 10)) census[s[q + 10] - '0']++;
            o << "    {\"epoch_number\": " << eps[i].number << ", \"epoch_seed\": \"" << hex(eps[i].seed, 32) << "\", \"opcode_census\": ["
              << census[0] << ", " << census[1] << ", " << census[2] << ", " << census[3] << "], \"schedule\": " << s << "}" << (i + 1 < eps.size() ? ",\n" : "\n");
        }
        o << "  ]\n}\n";
    }
    // dataset.json: cache digest (light) and dataset digest (full) per epoch; full contexts are built one at a time
    if (with_dataset) {
        std::ofstream o(dir + "/dataset.json");
        o << header(p, "dataset", seed) << "  \"epochs\": [\n";
        for (size_t i = 0; i < eps.size(); ++i) {
            int st = 0;
            rxe1_ctx* full = rxe1_ctx_create(&p, eps[i].seed, 1, &st);
            if (!full) { std::fprintf(stderr, "full context for epoch %llu: %d\n", (unsigned long long)eps[i].number, st); return 2; }
            uint8_t cd[32], dd[32]; int has = 0;
            rxe1_ctx_digests(full, cd, dd, &has);
            uint64_t cb = 0, db = 0; rxe1_ctx_cache(full, &cb); rxe1_ctx_dataset(full, &db);
            o << "    {\"epoch_number\": " << eps[i].number << ", \"epoch_seed\": \"" << hex(eps[i].seed, 32) << "\", \"cache_bytes\": \"" << cb << "\", \"cache_digest\": \"" << hex(cd, 32)
              << "\", \"dataset_bytes\": \"" << db << "\", \"dataset_digest\": \"" << hex(dd, 32) << "\"}" << (i + 1 < eps.size() ? ",\n" : "\n");
            std::fprintf(stderr, "dataset epoch %llu: %llu bytes built\n", (unsigned long long)eps[i].number, (unsigned long long)db);
            rxe1_ctx_free(full);
        }
        o << "  ]\n}\n";
    }
    for (auto& e : eps) rxe1_ctx_free(e.light);
    std::printf("wrote trace.json, known.json, schedule.json and dataset.json under %s\n", dir.c_str());
    return 0;
}

int check(const std::string& dir) {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    size_t bad = 0, n = 0;
    std::map<std::string, rxe1_ctx*> ctxs;
    auto ctx_for = [&](const std::string& seedhex) -> rxe1_ctx* {
        auto it = ctxs.find(seedhex);
        if (it != ctxs.end()) return it->second;
        std::vector<uint8_t> s; unhex(seedhex, s);
        int st = 0;
        rxe1_ctx* c = rxe1_ctx_create(&p, s.data(), 0, &st);
        ctxs[seedhex] = c;
        return c;
    };
    for (const char* cls : {"known.json", "trace.json"}) {
        std::ifstream in(dir + "/" + cls);
        if (!in) { std::fprintf(stderr, "cannot read %s\n", cls); return 2; }
        std::string line;
        while (std::getline(in, line)) {
            if (line.find("\"epoch_number\"") == std::string::npos || line.find("\"out\"") == std::string::npos) continue;
            std::vector<uint8_t> hdr; unhex(field(line, "header_blob"), hdr);
            const uint64_t ep = std::strtoull(field(line, "epoch_number").c_str(), nullptr, 10);
            const uint64_t nonce = std::strtoull(field(line, "nonce").c_str(), nullptr, 10);
            rxe1_ctx* c = ctx_for(field(line, "epoch_seed"));
            if (!c) return 2;
            uint8_t out[32], sd[32];
            if (rxe1_eval(c, kChain, kChainLen, hdr.data(), hdr.size(), ep, nonce, out, sd, nullptr) != IGNPOW_OK) return 2;
            ++n;
            // the trace line's "out" is its last field; known lines carry seed and out
            const std::string want_out = line.substr(line.rfind("\"out\": \"") + 8, 64);
            if (hex(out, 32) != want_out || (field(line, "seed").size() == 64 && hex(sd, 32) != field(line, "seed"))) {
                ++bad; std::printf("MISMATCH %s epoch %llu nonce %llu\n", cls, (unsigned long long)ep, (unsigned long long)nonce);
            }
        }
    }
    for (auto& kv : ctxs) rxe1_ctx_free(kv.second);
    std::printf("rxe1 %s: %zu vectors, %zu mismatches\n", dir.c_str(), n, bad);
    return bad ? 1 : 0;
}

int campaign(uint64_t count, uint64_t seed, const std::string& path) {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    rng64 r(seed);
    uint8_t es[32]; r.bytes(es, 32);
    int st = 0;
    rxe1_ctx* c = rxe1_ctx_create(&p, es, 0, &st);
    if (!c) return 2;
    std::ofstream o(path);
    for (uint64_t i = 0; i < count; ++i) {
        uint8_t h[80]; r.bytes(h, 80);
        const uint64_t nonce = r.next();
        uint8_t out[32], dg[32];
        if (rxe1_trace_digest(c, kChain, kChainLen, h, 80, 1, nonce, out, dg) != IGNPOW_OK) return 2;
        o << "{\"chain_id\": \"" << hex(kChain, kChainLen) << "\", \"epoch_seed\": \"" << hex(es, 32) << "\", \"epoch_number\": 1, \"header_blob\": \"" << hex(h, 80)
          << "\", \"nonce\": \"" << nonce << "\", \"out\": \"" << hex(out, 32) << "\", \"trace_digest\": \"" << hex(dg, 32) << "\"}\n";
    }
    rxe1_ctx_free(c);
    std::printf("wrote %llu campaign lines to %s\n", (unsigned long long)count, path.c_str());
    return 0;
}

int campaign_check(const std::string& path) {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    std::ifstream in(path);
    if (!in) { std::fprintf(stderr, "cannot read %s\n", path.c_str()); return 2; }
    std::map<std::string, rxe1_ctx*> ctxs;
    size_t n = 0, bad = 0;
    std::string line;
    while (std::getline(in, line)) {
        if (line.find("\"trace_digest\"") == std::string::npos) continue;
        std::vector<uint8_t> cid, hdr, es;
        unhex(field(line, "chain_id"), cid); unhex(field(line, "header_blob"), hdr); unhex(field(line, "epoch_seed"), es);
        const std::string key = field(line, "epoch_seed");
        if (!ctxs.count(key)) { int st = 0; ctxs[key] = rxe1_ctx_create(&p, es.data(), 0, &st); }
        rxe1_ctx* c = ctxs[key];
        if (!c) return 2;
        uint8_t out[32], dg[32];
        const uint64_t ep = std::strtoull(field(line, "epoch_number").c_str(), nullptr, 10), nonce = std::strtoull(field(line, "nonce").c_str(), nullptr, 10);
        if (rxe1_trace_digest(c, cid.data(), cid.size(), hdr.data(), hdr.size(), ep, nonce, out, dg) != IGNPOW_OK) return 2;
        ++n;
        if (hex(out, 32) != field(line, "out") || hex(dg, 32) != field(line, "trace_digest")) { ++bad; std::printf("MISMATCH nonce %llu\n", (unsigned long long)nonce); }
    }
    for (auto& kv : ctxs) rxe1_ctx_free(kv.second);
    std::printf("rxe1 campaign %s: %zu lines, %zu mismatches\n", path.c_str(), n, bad);
    return bad ? 1 : 0;
}

int eval_one(int argc, char** argv) {
    rxe1_params p;
    if (rxe1_params_parse(g_params, &p) != IGNPOW_OK) { std::fprintf(stderr, "params document rejected\n"); return 2; }
    std::vector<uint8_t> cid, hdr, es;
    if (!unhex(argv[2], cid) || !unhex(argv[3], hdr) || !unhex(argv[4], es) || es.size() != 32) { std::fprintf(stderr, "bad hex\n"); return 2; }
    const uint64_t ep = std::strtoull(argv[5], nullptr, 10), nonce = std::strtoull(argv[6], nullptr, 10);
    int st = 0;
    rxe1_ctx* c = rxe1_ctx_create(&p, es.data(), 0, &st);
    if (!c) return 2;
    uint8_t out[32], sd[32];
    const bool tr = argc >= 8 && std::strcmp(argv[7], "trace") == 0;
    if (rxe1_eval(c, cid.data(), cid.size(), hdr.data(), hdr.size(), ep, nonce, out, sd, tr ? stdout : nullptr) != IGNPOW_OK) return 2;
    if (tr) std::printf("\n");
    std::printf("seed %s\nout  %s\n", hex(sd, 32).c_str(), hex(out, 32).c_str());
    rxe1_ctx_free(c);
    return 0;
}

}  // namespace

int main(int argc, char** argv) {
    g_params = std::getenv("IGNPOW_PARAMS");
    if (argc >= 3 && std::strcmp(argv[1], "--params") == 0) { g_params = argv[2]; argv += 2; argc -= 2; }
    if (argc < 2 || !g_params) { std::fprintf(stderr, "usage: see file header (the params document is mandatory)\n"); return 2; }
    size_t n = 0;
    if (ignpow_vectors_check(IGNPOW_ALGO_RXE1, &n) != IGNPOW_OK) { std::fprintf(stderr, "self-test failed\n"); return 3; }
    std::string cmd = argv[1];
    if (cmd == "info") return info();
    if (cmd == "gen" && argc >= 3) {
        bool with_dataset = true;
        uint64_t seed = 20261010ull;
        for (int i = 3; i < argc; ++i) { if (std::strcmp(argv[i], "--no-dataset") == 0) with_dataset = false; else seed = std::strtoull(argv[i], nullptr, 10); }
        return gen(argv[2], seed, with_dataset);
    }
    if (cmd == "check" && argc >= 3) return check(argv[2]);
    if (cmd == "campaign" && argc >= 5) return campaign(std::strtoull(argv[2], nullptr, 10), std::strtoull(argv[3], nullptr, 10), argv[4]);
    if (cmd == "campaign-check" && argc >= 3) return campaign_check(argv[2]);
    if (cmd == "eval" && argc >= 7) return eval_one(argc, argv);
    std::fprintf(stderr, "usage: see file header\n");
    return 2;
}
