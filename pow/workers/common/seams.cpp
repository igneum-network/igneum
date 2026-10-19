// seams.cpp: see seams.h. The Apple memory seat, 10 October 2026.
#include "seams.h"
#include "vectors.h"
#include "hex.h"
#include "ih_params.h"
#include "ignpow.h"
#include "igneumhash.h"
#include "refbridge.h"
#include <cstdio>
#include <cstring>
#include <vector>

namespace igm {
namespace {
std::string esc(const std::string &s) { std::string o; for (char c : s) { if (c == '"' || c == '\\') o += '\\'; if (c == '\n') { o += "\\n"; continue; } o += c; } return o; }
}

int seam_check(EngineIface &eng, uint64_t height, uint32_t nonces, const std::string &dataset_vectors) {
    IhParams ip; std::string perr;
    if (g_vector_knobs.params.empty() || !ih_parse_params(g_vector_knobs.params, ip, perr)) { fprintf(stderr, "seams: a params document is required (--params): %s\n", perr.c_str()); return 1; }
    Refusal r; Knobs k = g_vector_knobs;
    if (!eng.open("igneumhash", height, k, 0, r)) { printf("{\"seam_test\": {\"verdict\": \"refused\", \"reason\": \"%s: %s\"}}\n", r.code.c_str(), esc(r.explain).c_str()); fprintf(stderr, "seams: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str()); return 1; }
    const uint32_t iw = ip.item_words(), ib = ip.item_bytes; const uint64_t n_items = ip.n_items(), bytes = ip.dataset_bytes;
    const bool split = k.alloc == "split2" || k.variant == "split2";
    const uint64_t two_gib = 1ull << 31;
    int faults = 0;
    // 1. the build side: the device digest against the ground truth
    std::string digest_source = "none";
    { uint8_t E[32], D[32], cd[32], dg[32];
      if (!eng.dataset_digests(E, D, cd, dg)) { printf("{\"seam_digest\": {\"verdict\": \"unavailable\", \"reason\": \"the engine exposes no dataset digest\"}}\n"); faults++; }
      else {
          std::string want, want_cache;
          if (!dataset_vectors.empty()) {   // the hash team's dataset class for this document: the digest per height
              VectorFile vf; if (read_vector_file(dataset_vectors, vf) && vf.cls == "dataset") for (const Vector &v : vf.vectors) if (v.height == height || (want.empty() && v.height == 0)) { want = v.dataset_digest; want_cache = v.cache_digest; digest_source = "the vector set's dataset class (" + dataset_vectors + ")"; }
              if (want.empty()) for (const Vector &v : vf.vectors) { int s1 = 0, s2 = 0; if (ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), v.height, &s1) == ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), height, &s2) && s1 == IGNPOW_OK && s2 == IGNPOW_OK) { want = v.dataset_digest; want_cache = v.cache_digest; digest_source = "the vector set's dataset class (" + dataset_vectors + ", the same epoch at height " + std::to_string(v.height) + ")"; break; } }
          }
          if (want.empty()) {   // the reference's own full dataset on the host: the ground truth over every device buffer
              uint8_t e2[32], d2[32], cd2[32], dg2[32]; uint64_t bms = 0; int hd = 0; int st = -1;
              ignpow_ctx *rc = ignpow_ctx_create("igneumhash", g_vector_knobs.params.c_str(), height, 1, &st);
              if (rc && st == IGNPOW_OK) { st = ignh_dataset_info(rc, e2, d2, cd2, dg2, &bms, &hd); ignpow_ctx_free(rc); }
              if (st == IGNPOW_OK) { want = hex_of(dg2, 32); want_cache = hex_of(cd2, 32); digest_source = "the reference's full dataset built on the host"; }
          }
          const std::string got = hex_of(dg, 32), gotc = hex_of(cd, 32);
          bool ok = !want.empty() && got == want, cache_ok = want_cache.empty() || gotc == want_cache;
          if (!ok || !cache_ok) faults++;
          printf("{\"seam_digest\": {\"verdict\": \"%s\", \"device\": \"%s\", \"expected\": \"%s\", \"cache\": \"%s\", \"source\": \"%s\"}}\n", want.empty() ? "no ground truth" : ok ? "match" : "MISMATCH", got.c_str(), want.empty() ? "unavailable" : want.c_str(), want_cache.empty() ? "not compared" : cache_ok ? "match" : "MISMATCH", esc(digest_source).c_str());
      } }
    // 2. the guards
    bool guard_intact = true; std::string guard_note;
    { Json g = Json::object();
      if (eng.guard_status(g)) { guard_intact = g["intact"].as_bool(); printf("{\"guard_test\": %s}\n", g.dump().c_str()); if (!guard_intact) faults++; }
      else { guard_note = k.guard_mb ? "the engine has no guard path" : "no guard asked for (guard_mb 0)"; printf("{\"guard_test\": {\"intact\": null, \"note\": \"%s\"}}\n", guard_note.c_str()); } }
    // 3. the seam items
    struct Seam { const char *name; uint64_t item; };
    std::vector<Seam> seams;
    if (bytes > two_gib) seams.push_back({"2gib", two_gib / ib});
    if (split) seams.push_back({"split-half", n_items / 2});
    seams.push_back({"end", n_items - 1});
    uint64_t seam_items_ok = 0, seam_items_bad = 0, seam_items_ref = 0;
    ignpow_ctx *rctx = nullptr; { int st = 0; rctx = ignpow_ctx_create("igneumhash", g_vector_knobs.params.c_str(), height, 0, &st); if (st != IGNPOW_OK) rctx = nullptr; }
    std::vector<uint32_t> dev_item(iw), ref_item(iw);
    for (const Seam &sm : seams) {
        for (int d = -1; d <= 1; d++) {
            if ((d < 0 && sm.item == 0) || (d > 0 && sm.item + 1 >= n_items)) continue;
            const uint64_t idx = sm.item + d;
            bool readable = true; for (uint32_t j = 0; j < iw && readable; j++) readable = eng.dataset_word(idx * iw + j, &dev_item[j], false);
            const char *ref = "not exposed"; bool ref_ok = true;
            if (readable && rctx) { int rc = ref_dataset_item(rctx, idx, ref_item.data(), iw * 4); if (rc == IGNPOW_OK) { seam_items_ref++; ref_ok = std::memcmp(ref_item.data(), dev_item.data(), iw * 4) == 0; ref = ref_ok ? "match" : "MISMATCH"; } }
            if (readable && ref_ok) seam_items_ok++; else { seam_items_bad++; faults++; }
            printf("{\"seam_item\": {\"seam\": \"%s\", \"position\": \"%s\", \"item\": %llu, \"byte_offset\": %llu, \"side_of_2gib\": \"%s\", \"readable\": %s, \"reference\": \"%s\", \"first_word\": %u, \"last_word\": %u}}\n",
                   sm.name, d < 0 ? "below" : d > 0 ? "above" : "at", (unsigned long long)idx, (unsigned long long)(idx * ib), idx * ib < two_gib ? "below" : "above", readable ? "true" : "false", ref, readable ? dev_item[0] : 0u, readable ? dev_item[iw - 1] : 0u);
        }
    }
    if (rctx) ignpow_ctx_free(rctx);
    // 4. the read side: the traced rounds against the device's words
    uint64_t lower = 0, upper = 0, mism = 0, rounds = 0, seam_hits = 0, traced = 0; std::string trace_note;
    uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(0x5a ^ i);
    for (uint32_t n = 0; n < nonces; n++) {
        std::vector<uint32_t> w; Refusal tr;
        if (!eng.trace_one(height, header, 0x1000000ull + n, w, tr)) { trace_note = tr.code + ": " + tr.explain; fprintf(stderr, "seams: trace %u: %s\n", n, trace_note.c_str()); faults++; break; }
        traced++;
        for (uint32_t rr = 0; rr < ip.rounds; rr++) {
            const uint32_t *b = &w[704 + (size_t)rr * (130 + iw)]; const uint64_t idx = b[0];
            if (idx * ib >= two_gib) upper++; else lower++;
            rounds++;
            for (const Seam &sm : seams) if (idx + 1 >= sm.item && idx <= sm.item + 1) seam_hits++;
            for (uint32_t j = 0; j < iw; j++) { uint32_t v = 0; if (!eng.dataset_word(idx * iw + j, &v, false) || v != b[2 + j]) { mism++; break; } }
        }
    }
    if (mism || (traced && lower == 0) || (bytes > two_gib && traced && upper == 0)) faults++;
    // 5. the letter of security's TP-146, and the seam summary
    const bool reach_2g = bytes > two_gib;
    { const bool rb = traced > 0 && lower > 0 && mism == 0 && seam_items_bad == 0 && faults == 0, ra = reach_2g && traced > 0 && upper > 0 && mism == 0 && seam_items_bad == 0 && faults == 0;
      printf("{\"boundary_test\": {\"below_2g\": \"%s\", \"at_2g\": \"%s\", \"above_2g\": \"%s\", \"split_item\": %llu, \"seam_item_2gib\": %llu}}\n",
             rb ? "ok" : "FAULT", !reach_2g ? "n/a" : ra ? "ok" : "FAULT", !reach_2g ? "n/a" : ra ? "ok" : "FAULT", (unsigned long long)(split ? n_items / 2 : 0), (unsigned long long)(reach_2g ? two_gib / ib : 0)); }
    printf("{\"seam_test\": {\"dataset_bytes\": %llu, \"item_bytes\": %u, \"items\": %llu, \"allocation\": \"%s\", \"storage\": \"%s\", \"guard_mb\": %u, \"guard_intact\": %s, \"seams\": %zu, \"seam_items_ok\": %llu, \"seam_items_bad\": %llu, \"seam_items_reference_compared\": %llu, \"traced_nonces\": %llu, \"rounds\": %llu, \"rounds_below_2gib\": %llu, \"rounds_above_2gib\": %llu, \"rounds_on_a_seam_item\": %llu, \"item_word_mismatches\": %llu, \"trace_note\": \"%s\", \"verdict\": \"%s\", \"note\": \"%s\"}}\n",
           (unsigned long long)bytes, ib, (unsigned long long)n_items, split ? "split2 (two half buffers)" : "one buffer", k.storage.empty() ? "shared" : k.storage.c_str(), k.guard_mb, guard_intact ? "true" : "false", seams.size(),
           (unsigned long long)seam_items_ok, (unsigned long long)seam_items_bad, (unsigned long long)seam_items_ref, (unsigned long long)traced, (unsigned long long)rounds, (unsigned long long)lower, (unsigned long long)upper, (unsigned long long)seam_hits, (unsigned long long)mism, esc(trace_note).c_str(),
           faults ? "FAULT" : "seams clean",
           "the digest covers every item the device holds (the build side at every address); the traced rounds cover the kernel's reads banded by the 2 GiB address (the read side); a traced round on a seam item is chance (the addresses are the relation's) and its count is reported, never required");
    fflush(stdout);
    eng.close();
    return faults;
}

}  // namespace igm
