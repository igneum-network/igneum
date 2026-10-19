// vectors_main.cpp: see vectors_main.h.
#include "vectors_main.h"
#include "vectors.h"
#include "ignpow.h"
#include <cstdio>

namespace igm {

int vectors_main(EngineIface &eng, const std::vector<std::string> &files) {
    size_t count = 0;
    int st = ignpow_vectors_check(nullptr, &count);
    fprintf(stderr, "%s\nreference self-test: %s (%zu embedded vectors)\n", ignpow_build_string(), st == IGNPOW_OK ? "ok" : "FAILED", count);
    if (st != IGNPOW_OK) return 3;
    if (files.empty()) { fprintf(stderr, "vectors: no files given\n"); return 2; }
    uint64_t total_mismatch = 0; int unreadable = 0;
    for (const std::string &p : files) {
        VectorFile f;
        if (!read_vector_file(p, f)) { fprintf(stderr, "vectors: cannot read %s\n", p.c_str()); unreadable++; continue; }
        uint64_t m = run_vector_file(eng, f);
        if (m == (uint64_t)-1) { unreadable++; continue; }
        total_mismatch += m;
    }
    fprintf(stderr, "vectors: %s: %llu mismatches, %d files unreadable\n", eng.describe().c_str(), (unsigned long long)total_mismatch, unreadable);
    Json dev = eng.device_json();
    // verdict and exit code, one shape with the GPU team's CUDA worker: exact 0; mismatch 1; every vector refused
    // for room (memory) 4; any other refusal 3; unreadable files 2
    const char *verdict = g_mismatch_total ? "mismatch" : (g_refused_total == 0 ? "exact" : (g_refused_room_total == g_refused_total && g_refused_total == g_vectors_total ? "refused-room" : (g_refused_room_total == g_refused_total ? "partial-room" : "refused")));
    printf("{\"summary\": {\"device\": \"%s\", \"arch\": \"%s\", \"vectors\": %llu, \"mismatches\": %llu, \"refused\": %llu, \"refused_room\": %llu, \"verdict\": \"%s\", \"notes\": \"%s\"}}\n",
           dev["name"].as_string().c_str(), dev["compute"].as_string().c_str(), (unsigned long long)g_vectors_total, (unsigned long long)g_mismatch_total,
           (unsigned long long)g_refused_total, (unsigned long long)g_refused_room_total, verdict, eng.describe().c_str());
    fflush(stdout);
    if (unreadable) return 2;
    if (g_mismatch_total) return 1;
    if (g_refused_total == 0) return 0;
    return g_refused_room_total == g_refused_total ? 4 : 3;
}

}  // namespace igm
