// BeamHash III control (founder's decision-6, 10 Oct 2026): the pinned upstream implementation (Beam 7.5.14493,
// Apache 2.0, pow/third_party/beamhash3-7.5.14493/FETCH) driven exactly as Beam's Block::PoW drives it: the base
// state is InitialiseState, then H(input || nonce) with the 32-byte input and the 8-byte nonce; OptimisedSolve
// produces solutions of 104 bytes (32 indices of 26 bits) which IsValidSolution checks. Measured but not vendored.
//   beamhash3-control [--seconds N] [--seed hex32] [--threads T] [--json]
// Reports solutions per second, solve time per nonce, verify time per solution, the peak resident set (the solver's
// working memory), the proof size, and the round-trip verdict (every solution produced verifies; a flipped index
// byte is refused). No upstream known-answer vector is published in the tree; the row says so.
#include "beamHashIII.h"

#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstring>
#include <string>
#include <thread>
#include <vector>
#include <sys/resource.h>

namespace {
using clk = std::chrono::steady_clock;
double secs(clk::time_point a, clk::time_point b) { return std::chrono::duration<double>(b - a).count(); }

void base_state(blake2b_state& st, const uint8_t input[32], uint64_t nonce) {
    BeamHash_III s;
    s.InitialiseState(st);
    blake2b_update(&st, input, 32);
    uint8_t n[8];  // Beam's NonceType is a uintBig_t<8>: big-endian bytes
    for (int i = 0; i < 8; ++i) n[i] = (uint8_t)(nonce >> (56 - 8 * i));
    blake2b_update(&st, n, 8);
}

struct totals { std::atomic<uint64_t> nonces{0}, solutions{0}, verified{0}, refused_flip{0}, verify_ns{0}, solve_ns{0}, cancelled{0}; };
}  // namespace

int main(int argc, char** argv) {
    double seconds = 60; unsigned threads = 1; std::string seed = "49676e65756d2d4265616d48617368494949636f6e74726f6c2d323032362d3130";
    for (int i = 1; i + 1 < argc; ++i) {
        if (!std::strcmp(argv[i], "--seconds")) seconds = std::atof(argv[i + 1]);
        if (!std::strcmp(argv[i], "--threads")) threads = (unsigned)std::atoi(argv[i + 1]);
        if (!std::strcmp(argv[i], "--seed")) seed = argv[i + 1];
    }
    uint8_t input[32] = {0};
    for (size_t i = 0; i < 32 && 2 * i + 1 < seed.size(); ++i) input[i] = (uint8_t)std::stoul(seed.substr(2 * i, 2), nullptr, 16);
    totals T;
    const auto t0 = clk::now();
    std::vector<std::thread> pool;
    for (unsigned t = 0; t < threads; ++t) pool.emplace_back([&, t] {
        BeamHash_III scheme;
        for (uint64_t nonce = t;; nonce += threads) {
            if (secs(t0, clk::now()) >= seconds) return;
            blake2b_state st; base_state(st, input, nonce);
            std::vector<std::vector<unsigned char>> sols;
            const auto s0 = clk::now();
            try {
                scheme.OptimisedSolve(st, [&](const std::vector<unsigned char>& sol) { sols.push_back(sol); return false; },
                                      [&](SolverCancelCheck) { return secs(t0, clk::now()) >= seconds; });
            } catch (const SolverCancelledException&) {
                T.cancelled += 1;  // the deadline fell inside this solve: it is not counted
                return;
            }
            T.solve_ns += (uint64_t)(secs(s0, clk::now()) * 1e9);
            T.nonces += 1; T.solutions += sols.size();
            for (auto& sol : sols) {
                blake2b_state v; base_state(v, input, nonce);
                const auto v0 = clk::now();
                const bool ok = scheme.IsValidSolution(v, sol);
                T.verify_ns += (uint64_t)(secs(v0, clk::now()) * 1e9);
                if (ok) T.verified += 1;
                std::vector<unsigned char> bad = sol; bad[7] ^= 0x10;
                blake2b_state w; base_state(w, input, nonce);
                if (!scheme.IsValidSolution(w, bad)) T.refused_flip += 1;
            }
        }
    });
    for (auto& th : pool) th.join();
    const double el = secs(t0, clk::now());
    struct rusage ru; getrusage(RUSAGE_SELF, &ru);
    const uint64_t n = T.nonces.load(), s = T.solutions.load();
    const bool exact = (s > 0) && (T.verified.load() == s) && (T.refused_flip.load() == s);
    std::printf("{\"beamhash3_control\": {\"pin\": \"beam-7.5.14493 9c4366aa\", \"licence\": \"Apache-2.0\", \"implementation\": \"upstream CPU OptimisedSolve, not our protocol\", "
                "\"threads\": %u, \"seconds\": %.1f, \"nonces\": %llu, \"solves_cancelled_at_deadline\": %llu, \"solutions\": %llu, \"solutions_per_s\": %.4f, \"solutions_per_nonce\": %.3f, "
                "\"solve_ms_per_nonce\": %.1f, \"verify_us_per_solution\": %.1f, \"proof_size_bytes\": 104, \"nonce_bytes\": 8, "
                "\"working_memory_bytes\": %llu, \"working_memory_method\": \"peak resident set of the process (getrusage ru_maxrss)\", "
                "\"memory_traffic_bytes_per_solution\": \"n/a (no counters on this host)\", \"energy\": \"n/a (CPU host without a power reading)\", "
                "\"round_trip\": {\"verified\": %llu, \"flipped_byte_refused\": %llu, \"verdict\": \"%s\"}, \"known_answer\": \"none published upstream; a mainnet block past fork 2 pending\", \"grade\": \"cpu-control\"}}\n",
                threads, el, (unsigned long long)n, (unsigned long long)T.cancelled.load(), (unsigned long long)s, s / el, n ? (double)s / n : 0.0, n ? T.solve_ns.load() / 1e6 / n : 0.0,
                s ? T.verify_ns.load() / 1e3 / s : 0.0, (unsigned long long)ru.ru_maxrss * 1024ull, (unsigned long long)T.verified.load(), (unsigned long long)T.refused_flip.load(), exact ? "exact" : "FAULT");
    return exact ? 0 : 1;
}
