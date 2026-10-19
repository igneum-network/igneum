// the per-header cost of one document on one core: a light context at height 1, N hashes over consecutive nonces, ms per hash
#include "ignpow.h"
#include <cstdio>
#ifdef IGNH_OPERAND_STATS
extern "C" void ignh_operand_stats_dump(FILE*);
#endif
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
int main(int argc, char** argv) {
    if (argc < 3) { std::fprintf(stderr, "usage: r0e-bench '<params json>' N\n"); return 2; }
    const int full = argc > 3 ? std::atoi(argv[3]) : 0;  // 1: the resident dataset (the items read, not computed from their parents)
    int st = 0; ignpow_ctx* c = ignpow_ctx_create("igneumhash", argv[1], 1, full, &st);
    if (!c) { std::fprintf(stderr, "ctx %d\n", st); return 1; }
    const long n = std::atol(argv[2]);
    uint8_t hdr[32]; for (int i = 0; i < 32; ++i) hdr[i] = uint8_t(i * 13 + 7);
    uint8_t mix[32], out[32]; uint32_t acc = 0;
    for (int w = 0; w < 4; ++w) ignpow_hash(c, 1, hdr, 1000 + w, mix, out);  // warm
    const auto t0 = std::chrono::steady_clock::now();
    for (long i = 0; i < n; ++i) { ignpow_hash(c, 1, hdr, 5000 + i, mix, out); acc ^= out[0]; }
    const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
    std::printf("%ld hashes in %.1f ms: %.3f ms per hash (%s context, acc %u)\n", n, ms, ms / n, full ? "resident" : "light", acc);
#ifdef IGNH_OPERAND_STATS
    ignh_operand_stats_dump(stdout);
#endif
    ignpow_ctx_free(c);
    return 0;
}
