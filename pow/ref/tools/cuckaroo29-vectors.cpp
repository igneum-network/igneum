// cuckaroo29-vectors: the solver and verifier sides of the Tari Cuckaroo29 control (controls.h; SOURCES.md).
//
//   cuckaroo29-vectors solve <out.json> [seed] [count]   per (header, nonce): the keys, every 42-cycle (sorted edge
//                                                          indices), its packed proof and proof value
//   cuckaroo29-vectors verify <solve.json> <out.json>      per cycle found: the verifier's verdict on the proof and on
//                                                          the negative forms (a wrong edge, a non-ascending order,
//                                                          an edge too big, another header, another nonce, a
//                                                          non-zero packing extra)
//   cuckaroo29-vectors check <solve.json>                  re-solves every entry and compares
#include "controls.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <string>
#include <vector>

namespace {
std::string hex(const uint8_t* p, size_t n) { static const char* d = "0123456789abcdef"; std::string s; for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; } return s; }
bool unhex(const std::string& s, uint8_t* out, size_t n) { if (s.size() != 2 * n) return false; for (size_t i = 0; i < n; ++i) { auto v = [](char c) { return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : -1; }; int a = v(s[2 * i]), b = v(s[2 * i + 1]); if (a < 0 || b < 0) return false; out[i] = uint8_t(a * 16 + b); } return true; }
std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":"; size_t p = line.find(k); if (p == std::string::npos) return "";
    p += k.size(); while (p < line.size() && line[p] == ' ') ++p;
    if (line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    if (line[p] == '[') { int d = 0; size_t q = p; for (; q < line.size(); ++q) { if (line[q] == '[') ++d; else if (line[q] == ']' && --d == 0) break; } return line.substr(p, q - p + 1); }
    size_t q = line.find_first_of(",}", p); return line.substr(p, q - p);
}
struct rng64 { uint64_t s; explicit rng64(uint64_t seed) : s(seed ? seed : 0x9E3779B97F4A7C15ull) {} uint64_t next() { s ^= s >> 12; s ^= s << 25; s ^= s >> 27; return s * 0x2545F4914F6CDD1Dull; } void bytes(uint8_t* p, size_t n) { for (size_t i = 0; i < n; ++i) p[i] = uint8_t(next() >> 56); } };

std::string cycles_json(const std::vector<uint32_t>& sols, size_t n) {
    std::string s = "[";
    for (size_t k = 0; k < n; ++k) { s += k ? ", {" : "{"; uint8_t packed[153], ph[32]; cuckaroo29_pack(sols.data() + 42 * k, packed); cuckaroo29_proof_hash(packed, ph); s += "\"edges\": ["; for (int i = 0; i < 42; ++i) s += (i ? ", " : "") + std::to_string(sols[42 * k + i]); s += "], \"packed\": \"" + hex(packed, 153) + "\", \"proof_value\": \"" + hex(ph, 32) + "\"}"; }
    return s + "]";
}

int solve_cmd(const std::string& out, uint64_t seed, int count) {
    rng64 r(seed);
    std::ofstream o(out);
    o << "{\n  \"candidate\": \"cuckaroo29-tari\",\n  \"class\": \"solve\",\n  \"seed\": \"" << seed << "\",\n  \"vectors\": [\n";
    size_t with = 0;
    for (int i = 0; i < count; ++i) {
        uint8_t hdr[32]; if (i == 0) std::memset(hdr, 0, 32); else r.bytes(hdr, 32);
        const uint64_t nonce = i == 0 ? 0 : r.next();
        uint64_t keys[4]; cuckaroo29_keys(hdr, nonce, keys);
        std::vector<uint32_t> sols(42 * 4); size_t n = 0;
        if (cuckaroo29_solve(hdr, nonce, sols.data(), 4, &n) != IGNPOW_OK) { std::fprintf(stderr, "solve failed\n"); return 2; }
        if (n > 4) n = 4;
        if (n) ++with;
        char kb[4][20]; for (int k = 0; k < 4; ++k) std::snprintf(kb[k], sizeof kb[k], "%016llx", (unsigned long long)keys[k]);
        o << (i ? ",\n" : "") << "    {\"header\": \"" << hex(hdr, 32) << "\", \"nonce\": \"" << nonce << "\", \"keys\": [\"" << kb[0] << "\", \"" << kb[1] << "\", \"" << kb[2] << "\", \"" << kb[3] << "\"], \"cycles\": " << cycles_json(sols, n) << "}";
        std::fprintf(stderr, "solved %d: %zu cycle(s)\n", i, n);
    }
    o << "\n  ]\n}\n";
    std::printf("wrote %d solve vectors (%zu with a cycle) to %s\n", count, with, out.c_str());
    return 0;
}

std::vector<uint32_t> edges_of(const std::string& arr) {
    std::vector<uint32_t> e; size_t p = 1;
    while (p < arr.size()) { while (p < arr.size() && (arr[p] == ' ' || arr[p] == ',')) ++p; if (p >= arr.size() || arr[p] == ']') break; e.push_back(uint32_t(std::strtoul(arr.c_str() + p, nullptr, 10))); while (p < arr.size() && arr[p] >= '0' && arr[p] <= '9') ++p; }
    return e;
}

int verify_cmd(const std::string& in, const std::string& out) {
    std::ifstream f(in); if (!f) return 2;
    std::ofstream o(out);
    o << "{\n  \"candidate\": \"cuckaroo29-tari\",\n  \"class\": \"verify\",\n  \"codes\": \"0 ok, 2 edge too big, 3 not ascending, 4 endpoints, 5 branch, 6 dead end, 7 short cycle; pack: ok or extra\",\n  \"vectors\": [\n";
    std::string line; bool first = true; size_t n = 0;
    while (std::getline(f, line)) {
        if (line.find("\"cycles\"") == std::string::npos) continue;
        uint8_t hdr[32]; unhex(field(line, "header"), hdr, 32);
        const uint64_t nonce = std::strtoull(field(line, "nonce").c_str(), nullptr, 10);
        std::string cyc = field(line, "cycles");
        size_t p = 0;
        while ((p = cyc.find("\"edges\":", p)) != std::string::npos) {
            std::vector<uint32_t> e = edges_of(field(cyc.substr(p), "edges")); p += 8;
            if (e.size() != 42) continue;
            auto emit = [&](const char* name, const uint8_t* h, uint64_t nn, const uint32_t* ed) {
                o << (first ? "" : ",\n") << "    {\"case\": \"" << name << "\", \"header\": \"" << hex(h, 32) << "\", \"nonce\": \"" << nn << "\", \"edges\": [";
                for (int i = 0; i < 42; ++i) o << (i ? ", " : "") << ed[i];
                o << "], \"code\": " << cuckaroo29_verify(h, nn, ed) << "}"; first = false; ++n;
            };
            emit("valid", hdr, nonce, e.data());
            { std::vector<uint32_t> x = e; x[7] ^= 1; emit("wrong-edge", hdr, nonce, x.data()); }
            { std::vector<uint32_t> x = e; std::swap(x[3], x[4]); emit("not-ascending", hdr, nonce, x.data()); }
            { std::vector<uint32_t> x = e; x[41] = (1u << 29) + x[41]; emit("edge-too-big", hdr, nonce, x.data()); }
            { uint8_t h2[32]; std::memcpy(h2, hdr, 32); h2[0] ^= 1; emit("other-header", h2, nonce, e.data()); }
            emit("other-nonce", hdr, nonce + 1, e.data());
            { std::vector<uint32_t> x = e; x[0] = x[1]; emit("repeated-edge", hdr, nonce, x.data()); }
            {   // the packing extra: the 153rd byte carries bits beyond 42 x 29 = 1218; a non-zero extra is refused by unpack
                uint8_t packed[153]; cuckaroo29_pack(e.data(), packed); packed[152] |= 0x80; uint32_t back[42];
                o << ",\n    {\"case\": \"packing-extra\", \"header\": \"" << hex(hdr, 32) << "\", \"nonce\": \"" << nonce << "\", \"packed\": \"" << hex(packed, 153) << "\", \"unpack\": \"" << (cuckaroo29_unpack(packed, back) == IGNPOW_OK ? "ok" : "extra") << "\"}"; ++n;
            }
        }
    }
    o << "\n  ]\n}\n";
    std::printf("wrote %zu verify vectors to %s\n", n, out.c_str());
    return 0;
}

int check_cmd(const std::string& in) {
    std::ifstream f(in); if (!f) return 2;
    std::string line; size_t n = 0, bad = 0;
    while (std::getline(f, line)) {
        if (line.find("\"cycles\"") == std::string::npos) continue;
        uint8_t hdr[32]; unhex(field(line, "header"), hdr, 32);
        const uint64_t nonce = std::strtoull(field(line, "nonce").c_str(), nullptr, 10);
        std::vector<uint32_t> sols(42 * 4); size_t k = 0;
        if (cuckaroo29_solve(hdr, nonce, sols.data(), 4, &k) != IGNPOW_OK) return 2;
        if (k > 4) k = 4;
        const std::string want = field(line, "cycles");
        if (cycles_json(sols, k) != want) { ++bad; std::printf("MISMATCH nonce %llu\n", (unsigned long long)nonce); }
        ++n;
    }
    std::printf("cuckaroo29-tari %s: %zu vectors, %zu mismatches\n", in.c_str(), n, bad);
    return bad ? 1 : 0;
}
}  // namespace

int main(int argc, char** argv) {
    if (argc < 3) { std::fprintf(stderr, "usage: see file header\n"); return 2; }
    size_t n = 0;
    if (ignpow_vectors_check(IGNPOW_ALGO_CUCKAROO29_TARI, &n) != IGNPOW_OK) { std::fprintf(stderr, "self-test failed\n"); return 3; }
    const std::string cmd = argv[1];
    if (cmd == "solve") return solve_cmd(argv[2], argc >= 4 ? std::strtoull(argv[3], nullptr, 10) : 20261010ull, argc >= 5 ? std::atoi(argv[4]) : 128);
    if (cmd == "verify" && argc >= 4) return verify_cmd(argv[2], argv[3]);
    if (cmd == "check") return check_cmd(argv[2]);
    std::fprintf(stderr, "usage: see file header\n");
    return 2;
}
