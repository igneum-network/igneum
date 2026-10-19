// vectors.h: reader for GPU engineer A's vector files (pow/vectors/<candidate>/*.json,
// written by pow/ref/tools/ignpow-vectors.cpp) and the HW-01 runner over an EngineIface.
// GPU engineer B, 9 October 2026.
#pragma once
#include <cstdint>
#include <string>
#include <vector>
#include "engine_iface.h"

namespace igm {

struct Vector {
    uint64_t height = 0;
    std::string header, mix, hash, boundary, expect;
    uint64_t nonce = 0;
    uint64_t nonce_count = 0;      // tails
    std::string share_target;      // tails
    uint64_t min_nonce = 0;        // tails
    std::string min_hash;          // tails
    uint64_t shares = 0;           // tails
    uint64_t ctx_height = 0;
    std::string E, D, cache_digest, dataset_digest;   // the dataset class       // malformed
};

struct VectorFile { std::string candidate, cls, generator, seed; std::string program_seed; std::vector<Vector> vectors; };   // program_seed: version 9 (R0D), the test period's programme seed (64 hex) the file carries

bool read_vector_file(const std::string &path, VectorFile &out);

// Totals across the files of one run, for the summary line vectors_main prints.
extern uint64_t g_vectors_total, g_mismatch_total, g_refused_total, g_refused_room_total;   // room: refusals with code "memory"
extern Knobs g_vector_knobs;   // the knobs (and params document) every vector open uses

// Runs one file; prints per-vector findings on stderr; returns the mismatch count
// (0 is PASS for that file), or (uint64_t)-1 when the run could not be made at all.
uint64_t run_vector_file(EngineIface &eng, const VectorFile &f);

// The identity set (strata/RIPTIDE.md section 2): the kernel's own trace, unseen, nonce-boundary, transitions and malformed
// files for the reference set at ref_dir, written to out_dir in the reference's form (the inputs copied line for line, the
// outputs the kernel's); tools/strata-trace-identity.py compares them field by field. Returns the number of files written.
// The flip trace (security's TP-125): for each nonce of the class file, the baseline trace, then every word of the probe
// round's item flipped (one bit) in the resident dataset, the trace again, the next address and the digest compared,
// the word restored; the rows per word in the security review's s1_subset.py shape. Returns the nonces traced, 0 on refusal.
int write_flip_trace(EngineIface &eng, const std::string &ref_dir, const std::string &out_path, uint32_t probe_round, uint32_t max_nonces, const std::string &kernel_commit);
// The 2 GiB boundary test (the outside review's ruling, 10 Oct 2026): on a document above 2 GiB (two device buffers) the items
// immediately below, at and above the split (the last of the first buffer, the first of the second, one past each) and
// the ends are read back from the device through the engine's split-aware word path and compared with the kernel's own
// reads: `nonces` full traces are taken and every round's item words are compared with the device's words at the
// round's address, counting the rounds that fell in each half; the build side is the dataset digest over both buffers.
// Returns 0 when every compared word agrees, both halves were visited and the digest matches; a positive count of faults.
int boundary_check(EngineIface &eng, uint64_t height, uint32_t nonces);

int write_identity_set(EngineIface &eng, const std::string &ref_dir, const std::string &out_dir, const std::string &prefix = std::string());   // prefix: <prefix><class>.json (a bench job stores flat files)

}  // namespace igm
