// vectors.cpp: see vectors.h.
#include "vectors.h"
#include "hex.h"
#include "ignpow.h"
#include "ih_params.h"
#include "igneumhash.h"
#include <chrono>
#include <cstdio>
#include <cstring>
#include <fstream>

namespace igm {

static bool g_last_refusal_room = false;
uint64_t g_vectors_total = 0, g_mismatch_total = 0, g_refused_total = 0, g_refused_room_total = 0;
Knobs g_vector_knobs;

namespace {

std::string json_escape(const std::string &s) { std::string o; for (char c : s) { if (c == '"' || c == '\\') o.push_back('\\'); if (c == '\n') o += "\\n"; else o.push_back(c); } return o; }

// The writer puts one vector per line and the file fields on their own lines; this
// reader accepts exactly that, as A's own reader does.
std::string field(const std::string &line, const char *key) {
    std::string k = std::string("\"") + key + "\": ";
    size_t p = line.find(k);
    if (p == std::string::npos) return "";
    p += k.size();
    if (p < line.size() && line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    size_t q = line.find_first_of(",}", p);
    return line.substr(p, q - p);
}
uint64_t u64(const std::string &s) { return strtoull(s.c_str(), nullptr, 10); }

bool le256(const uint8_t a[32], const uint8_t b[32]) {
    for (int i = 0; i < 32; i++) { if (a[i] < b[i]) return true; if (a[i] > b[i]) return false; }
    return true;
}

const char *reason_name(int st) {
    switch (st) {
        case IGNPOW_OK: return "ok"; case IGNPOW_E_ALGO: return "algo"; case IGNPOW_E_PARAMS: return "params";
        case IGNPOW_E_HEIGHT: return "height"; case IGNPOW_E_EPOCH: return "epoch"; case IGNPOW_E_BOUNDARY: return "boundary";
        case IGNPOW_E_MIX: return "mix"; case IGNPOW_E_ARG: return "arg"; case IGNPOW_E_MEMORY: return "memory"; case IGNPOW_E_VECTORS: return "vectors";
    }
    return "unknown";
}

}  // namespace

bool read_vector_file(const std::string &path, VectorFile &out) {
    std::ifstream in(path);
    if (!in) return false;
    std::string line;
    while (std::getline(in, line)) {
        if (line.find("\"candidate\"") != std::string::npos) out.candidate = field(line, "candidate");
        else if (line.find("\"class\"") != std::string::npos) out.cls = field(line, "class");
        else if (line.find("\"generator\"") != std::string::npos) out.generator = field(line, "generator");
        else if (line.find("\"program_seed\"") != std::string::npos && line.find("{\"height\"") == std::string::npos) out.program_seed = field(line, "program_seed");
        else if (line.find("\"seed\"") != std::string::npos && out.vectors.empty() && line.find("{\"height\"") == std::string::npos) out.seed = field(line, "seed");
        else if (line.find("{\"height\"") != std::string::npos) {
            Vector v;
            v.height = u64(field(line, "height"));
            v.header = field(line, "header");
            v.nonce = u64(field(line, "nonce"));
            v.mix = field(line, "mix");
            v.hash = field(line, "hash");
            v.boundary = field(line, "boundary");
            v.expect = field(line, "expect");
            v.nonce_count = u64(field(line, "nonce_count"));
            v.share_target = field(line, "share_target");
            v.min_nonce = u64(field(line, "min_nonce"));
            v.min_hash = field(line, "min_hash");
            v.shares = u64(field(line, "shares"));
            v.ctx_height = u64(field(line, "ctx_height"));
            v.E = field(line, "E"); v.D = field(line, "D"); v.cache_digest = field(line, "cache_digest"); v.dataset_digest = field(line, "dataset_digest");
            out.vectors.push_back(v);
        }
    }
    return !out.candidate.empty();
}

uint64_t run_vector_file(EngineIface &eng, const VectorFile &f) {
    uint64_t mismatches = 0, n = 0, unavailable = 0;   // unavailable: the worker could not open the epoch (a refusal), counted apart from a byte mismatch
    uint64_t open_height = ~0ull; bool opened = false;
    auto ensure_open = [&](uint64_t height) -> bool {
        int st = 0;
        uint64_t ep = ignpow_epoch_of(f.candidate.c_str(), g_vector_knobs.params.empty() ? nullptr : g_vector_knobs.params.c_str(), height, &st);
        if (st != IGNPOW_OK) { fprintf(stderr, "vectors: the reference refuses the params (status %d): the class reads as unavailable\n", st); return false; }   // 14 Oct 2026: this path was silent (a document with keyed_rounds 0 and no bit 10 read as 33 unavailable with no line)
        if (opened) {
            int st2 = 0; uint64_t cur = ignpow_epoch_of(f.candidate.c_str(), g_vector_knobs.params.empty() ? nullptr : g_vector_knobs.params.c_str(), open_height, &st2);
            if (st2 == IGNPOW_OK && cur == ep) return true;
            eng.close(); opened = false;
        }
        Refusal r; Knobs k = g_vector_knobs; k.no_warm = true;
        if (!f.program_seed.empty()) k.r0d_seed = f.program_seed;   // version 9: the file's test period
        auto t0 = std::chrono::steady_clock::now();
        if (!eng.open(f.candidate, height, k, 0, r)) {
            fprintf(stderr, "vectors: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str());
            if (r.code == "memory") g_last_refusal_room = true; else g_last_refusal_room = false;
            printf("{\"class\": \"%s\", \"height\": %llu, \"result\": \"refused\", \"reason\": \"%s: %s\"}\n", f.cls.c_str(), (unsigned long long)height, r.code.c_str(), json_escape(r.explain).c_str());
            fflush(stdout);
            return false;
        }
        double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
        opened = true; open_height = height;
        fprintf(stderr, "vectors: %s height %llu: %s\n", f.candidate.c_str(), (unsigned long long)height, eng.describe().c_str());
        printf("{\"prepared\": {\"epoch\": %llu, \"ms\": %.0f, \"bytes\": \"%llu\", \"mode\": \"cold\"}}\n", (unsigned long long)ep, ms, (unsigned long long)eng.allocated_bytes());
        fflush(stdout);
        return true;
    };
    auto hash_one = [&](uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix[32], uint8_t digest[32]) -> bool {
        Job j; j.job_id = "vector"; j.height = height; std::memcpy(j.header, header, 32); std::memset(j.target, 0xff, 32);
        j.nonce_start = nonce; j.nonce_count = 1;
        bool got = false;
        eng.run_job(j, [&](const Result &r) { if (r.nonce == nonce) { std::memcpy(mix, r.mix, 32); std::memcpy(digest, r.digest, 32); got = true; } },
                    [](const BatchStats &) {}, [] { return false; });
        return got;
    };

    for (const Vector &v : f.vectors) {
        n++;
        if (f.cls == "dataset") {
            // the dataset class (the lead's generator): per epoch the seed E, the reference's D, the BLAKE3 digest of the cache and of
            // the whole dataset; the engine builds the epoch on the device, reads it back and hashes it on the host
            if (!ensure_open(v.height)) { unavailable++; if (g_last_refusal_room) g_refused_room_total++; continue; }
            uint8_t E[32], D[32], cd[32], dg[32];
            if (!eng.dataset_digests(E, D, cd, dg)) { unavailable++; printf("{\"class\": \"dataset\", \"height\": %llu, \"result\": \"unavailable\", \"reason\": \"the engine exposes no dataset digest\"}\n", (unsigned long long)v.height); fflush(stdout); continue; }
            std::string hE = hex_of(E, 32), hD = hex_of(D, 32), hc = hex_of(cd, 32), hd = hex_of(dg, 32);
            bool cache_ok = v.cache_digest.empty() || hc == v.cache_digest;
            if (hE != v.E || hD != v.D || hd != v.dataset_digest || !cache_ok) {
                mismatches++;
                fprintf(stderr, "vectors: MISMATCH dataset height %llu: E %s D %s cache %s dataset %s\n", (unsigned long long)v.height, hE == v.E ? "ok" : "WRONG", hD == v.D ? "ok" : "WRONG", cache_ok ? "ok" : "WRONG", hd == v.dataset_digest ? "ok" : "WRONG");
                printf("{\"class\": \"dataset\", \"height\": %llu, \"result\": \"MISMATCH\", \"got_dataset_digest\": \"%s\", \"got_cache_digest\": \"%s\"}\n", (unsigned long long)v.height, hd.c_str(), hc.c_str());
            } else printf("{\"class\": \"dataset\", \"height\": %llu, \"result\": \"ok\", \"dataset_digest\": \"%s\"}\n", (unsigned long long)v.height, hd.c_str());
            fflush(stdout);
            continue;
        }
        uint8_t header[32];
        if (!hex_to(v.header, header, 32)) { mismatches++; fprintf(stderr, "vectors: %llu: header length\n", (unsigned long long)n); continue; }

        if (f.cls == "malformed") {
            // The worker's classification must equal the reference's reason code. Heights
            // outside the schedule and heights outside the context's epoch are the two
            // classes a worker can see; header and boundary length classes are the
            // supervisor's (a worker never receives a wrong-length field).
            int st = 0; ignpow_epoch_of(f.candidate.c_str(), g_vector_knobs.params.empty() ? nullptr : g_vector_knobs.params.c_str(), v.height, &st);
            std::string got = reason_name(st);
            if (st == IGNPOW_OK && v.ctx_height) {
                int st2 = 0; uint64_t e1 = ignpow_epoch_of(f.candidate.c_str(), g_vector_knobs.params.empty() ? nullptr : g_vector_knobs.params.c_str(), v.ctx_height, &st2);
                uint64_t e2 = ignpow_epoch_of(f.candidate.c_str(), g_vector_knobs.params.empty() ? nullptr : g_vector_knobs.params.c_str(), v.height, &st2);
                if (e1 != e2) got = "epoch";
            }
            if (got != v.expect && !(v.expect == "ok" && got == "ok")) {
                if (v.expect == "arg" || v.expect == "boundary" || v.expect == "mix") continue;   // not a worker-visible class
                mismatches++; fprintf(stderr, "vectors: MISMATCH %llu malformed: expect %s got %s\n", (unsigned long long)n, v.expect.c_str(), got.c_str());
            }
            continue;
        }

        if (!ensure_open(v.height)) { unavailable++; if (g_last_refusal_room) g_refused_room_total++; continue; }

        if (f.cls == "tails") {
            uint8_t target[32]; if (!hex_to(v.share_target, target, 32)) { mismatches++; continue; }
            Job j; j.job_id = "tail"; j.height = v.height; std::memcpy(j.header, header, 32); std::memcpy(j.target, target, 32);
            j.nonce_start = v.nonce; j.nonce_count = v.nonce_count;
            uint64_t shares = 0, best_nonce = 0; uint8_t best[32]; std::memset(best, 0xff, 32); bool any = false;
            eng.run_job(j, [&](const Result &r) { shares++; if (!any || le256(r.digest, best)) { any = true; std::memcpy(best, r.digest, 32); best_nonce = r.nonce; } },
                        [](const BatchStats &) {}, [] { return false; });
            if (shares == 0) {   // the minimum F of a range with no share: every nonce is a share under an all-ones target
                Job j2 = j; std::memset(j2.target, 0xff, 32); j2.job_id = "tail-min";
                eng.run_job(j2, [&](const Result &r) { if (!any || le256(r.digest, best)) { any = true; std::memcpy(best, r.digest, 32); best_nonce = r.nonce; } },
                            [](const BatchStats &) {}, [] { return false; });
            }
            std::string got_min = hex_of(best, 32);
            if (shares != v.shares || best_nonce != v.min_nonce || got_min != v.min_hash) {
                mismatches++;
                fprintf(stderr, "vectors: MISMATCH %llu tails: shares %llu/%llu min nonce %llu/%llu\n", (unsigned long long)n,
                        (unsigned long long)shares, (unsigned long long)v.shares, (unsigned long long)best_nonce, (unsigned long long)v.min_nonce);
            }
            continue;
        }

        // known, unseen, nonce-boundary, transitions: mix and hash byte for byte
        uint8_t want_mix[32], want_hash[32], mix[32], digest[32];
        if (!hex_to(v.mix, want_mix, 32) || !hex_to(v.hash, want_hash, 32)) { mismatches++; fprintf(stderr, "vectors: %llu: field length\n", (unsigned long long)n); continue; }
        if (!hash_one(v.height, header, v.nonce, mix, digest)) { mismatches++; fprintf(stderr, "vectors: MISMATCH %llu: no result\n", (unsigned long long)n); continue; }
        if (std::memcmp(mix, want_mix, 32) != 0 || std::memcmp(digest, want_hash, 32) != 0) {
            mismatches++;
            fprintf(stderr, "vectors: MISMATCH %llu height %llu nonce %llu\n  want mix %s hash %s\n  got  mix %s hash %s\n", (unsigned long long)n,
                    (unsigned long long)v.height, (unsigned long long)v.nonce, v.mix.c_str(), v.hash.c_str(), hex_of(mix, 32).c_str(), hex_of(digest, 32).c_str());
            printf("{\"class\": \"%s\", \"height\": %llu, \"nonce\": \"%llu\", \"result\": \"MISMATCH\", \"got_mix\": \"%s\", \"got_hash\": \"%s\"}\n", f.cls.c_str(),
                   (unsigned long long)v.height, (unsigned long long)v.nonce, hex_of(mix, 32).c_str(), hex_of(digest, 32).c_str());
            fflush(stdout);
        }
    }
    if (opened) eng.close();
    fprintf(stderr, "vectors: %s class %s: %llu vectors, %llu mismatches, %llu unavailable (open refused)\n", f.candidate.c_str(), f.cls.c_str(), (unsigned long long)n, (unsigned long long)mismatches, (unsigned long long)unavailable);
    printf("{\"class\": \"%s\", \"vectors\": %llu, \"mismatches\": %llu, \"refused\": %llu}\n", f.cls.c_str(), (unsigned long long)n, (unsigned long long)mismatches, (unsigned long long)unavailable);
    fflush(stdout);
    g_vectors_total += n; g_mismatch_total += mismatches; g_refused_total += unavailable;
    return mismatches + unavailable;   // the file fails either way; the line above says which
}

// ---- the flip trace (TP-125) ----
int write_flip_trace(EngineIface &eng, const std::string &ref_dir, const std::string &out_path, uint32_t probe_round, uint32_t max_nonces, const std::string &kernel_commit) {
    IhParams ip; std::string perr;
    if (g_vector_knobs.params.empty() || !ih_parse_params(g_vector_knobs.params, ip, perr)) { fprintf(stderr, "flip: a params document is required (--params): %s\n", perr.c_str()); return 0; }
    const uint32_t iw = ip.item_words();
    if (probe_round + 1 >= ip.rounds) probe_round = ip.rounds >= 2 ? ip.rounds - 2 : 0;
    // the nonces: the unseen class (64) then the trace class, (height, header, nonce) only
    std::vector<Vector> vs;
    for (const char *cls : {"unseen", "trace", "nonce-boundary"}) {
        std::ifstream in(ref_dir + "/" + cls + ".json"); std::string line;
        while (in && std::getline(in, line)) if (line.find("\"height\"") != std::string::npos) { Vector v; v.height = u64(field(line, "height")); v.header = field(line, "header"); v.nonce = u64(field(line, "nonce")); vs.push_back(v); }
        if (vs.size() >= max_nonces) break;
    }
    if (vs.size() > max_nonces) vs.resize(max_nonces);
    if (vs.empty()) { fprintf(stderr, "flip: no vectors under %s\n", ref_dir.c_str()); return 0; }
    // the class files hold 82 nonces at most (unseen 64, trace 2, nonce-boundary 16); a larger count (security's decision row:
    // 256) continues with the unseen class's headers and nonces offset by 1,000,003 k, k = 1, 2, ..., at the same height
    for (uint64_t k = 1; vs.size() < max_nonces; k++) {
        const size_t base = vs.size();
        for (size_t i = 0; i < 64 && i < base && vs.size() < max_nonces; i++) { Vector v = vs[i]; v.nonce += 1000003ull * k; vs.push_back(v); }
        if (base == 0) break;
    }
    std::vector<uint64_t> addr_changed(iw, 0), digest_changed(iw, 0), first_min(iw, ~0ull);
    uint64_t traced = 0, open_height = ~0ull; bool opened = false;
    const size_t stride = 130 + iw;
    for (const Vector &v : vs) {
        if (!opened || open_height != v.height) {
            if (opened) { eng.close(); opened = false; }
            Refusal r; Knobs k = g_vector_knobs; k.no_warm = true;
            if (!eng.open("igneumhash", v.height, k, 0, r)) { fprintf(stderr, "flip: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str()); return 0; }
            opened = true; open_height = v.height;
        }
        uint8_t header[32]; if (!hex_to(v.header, header, 32)) continue;
        std::vector<uint32_t> base; Refusal r;
        if (!eng.trace_one(v.height, header, v.nonce, base, r)) { fprintf(stderr, "flip: trace: %s: %s\n", r.code.c_str(), r.explain.c_str()); eng.close(); return 0; }
        const uint32_t idx = base[704 + (size_t)probe_round * stride];
        for (uint32_t j = 0; j < iw; j++) {
            const uint64_t wi = (uint64_t)idx * iw + j;
            uint32_t orig = 0, flipped;
            if (!eng.dataset_word(wi, &orig, false)) { fprintf(stderr, "flip: the engine has no dataset word path\n"); eng.close(); return 0; }
            flipped = orig ^ (1u << (j & 31u));
            eng.dataset_word(wi, &flipped, true);
            std::vector<uint32_t> w; bool ok = eng.trace_one(v.height, header, v.nonce, w, r);
            eng.dataset_word(wi, &orig, true);
            if (!ok) { fprintf(stderr, "flip: trace after the flip: %s\n", r.explain.c_str()); eng.close(); return 0; }
            // the next address: round probe_round + 1's idx; the digest: F at 568
            const uint32_t nb = base[704 + (size_t)(probe_round + 1) * stride], nw = w[704 + (size_t)(probe_round + 1) * stride];
            if (nb != nw) addr_changed[j]++;
            bool dch = false; for (int q = 0; q < 8; q++) if (base[568 + q] != w[568 + q]) { dch = true; break; }
            if (dch) digest_changed[j]++;
            for (uint32_t rr = probe_round + 1; rr < ip.rounds; rr++) if (base[704 + (size_t)rr * stride] != w[704 + (size_t)rr * stride]) { if (rr < first_min[j]) first_min[j] = rr; break; }
        }
        traced++;
        fprintf(stderr, "flip: nonce %llu traced (%llu of %zu)\n", (unsigned long long)v.nonce, (unsigned long long)traced, vs.size());
    }
    if (opened) eng.close();
    FILE *out = fopen(out_path.c_str(), "w");
    if (!out) { fprintf(stderr, "flip: cannot write %s\n", out_path.c_str()); return 0; }
    fprintf(out, "{\"class\": \"flip\", \"kernel\": \"%s\", \"generator\": \"%s\", \"params\": %s, \"probe_round\": %u, \"nonces\": %llu, \"words\": %u, \"flip\": \"bit (j mod 32) of word j of the probe round's item, the dataset restored after each trace\", \"widths\": {\"%u\": {\"rows\": [",
            json_escape(kernel_commit).c_str(), json_escape(eng.backend() + " worker: " + eng.describe()).c_str(), g_vector_knobs.params.c_str(), probe_round, (unsigned long long)traced, iw, ip.item_bytes);
    for (uint32_t j = 0; j < iw; j++)
        fprintf(out, "%s{\"word\": %u, \"next_address_changed_fraction\": %.6f, \"digest_changed_fraction\": %.6f, \"first_changed_round_min\": %lld}", j ? ", " : "", j,
                traced ? (double)addr_changed[j] / traced : 0.0, traced ? (double)digest_changed[j] / traced : 0.0, first_min[j] == ~0ull ? -1LL : (long long)first_min[j]);
    fprintf(out, "]}}}\n");
    fclose(out);
    return (int)traced;
}

// ---- the identity set ----
namespace {
// the value text of a key on a one-line vector record: from the colon to the end of the value (string or number or object)
bool replace_field(std::string &line, const char *key, const std::string &value_text) {
    std::string k = std::string("\"") + key + "\": ";
    size_t p = line.find(k);
    if (p == std::string::npos) return false;
    p += k.size();
    size_t q;
    if (line[p] == '"') q = line.find('"', p + 1) + 1;
    else if (line[p] == '{' || line[p] == '[') { int depth = 0; q = p; for (; q < line.size(); q++) { if (line[q] == '{' || line[q] == '[') depth++; else if (line[q] == '}' || line[q] == ']') { depth--; if (depth == 0) { q++; break; } } } }
    else q = line.find_first_of(",}", p);
    line.replace(p, q - p, value_text);
    return true;
}
std::string words_hex(const uint32_t *w, size_t n) { std::vector<uint8_t> b(n * 4); for (size_t i = 0; i < n; i++) for (int k = 0; k < 4; k++) b[4 * i + k] = (uint8_t)(w[i] >> (8 * k)); return hex_of(b.data(), b.size()); }
std::string regs_text(const uint32_t *w) { std::string o = "["; for (int l = 0; l < 8; l++) { o += l ? ", [" : "["; for (int q = 0; q < 16; q++) { o += q ? ", " : ""; o += std::to_string(w[16 * l + q]); } o += "]"; } return o + "]"; }
}  // namespace

int write_identity_set(EngineIface &eng, const std::string &ref_dir, const std::string &out_dir, const std::string &prefix) {
    IhParams ip; std::string perr;
    if (g_vector_knobs.params.empty() || !ih_parse_params(g_vector_knobs.params, ip, perr)) { fprintf(stderr, "trace: a params document is required (--params): %s\n", perr.c_str()); return 0; }
    const uint32_t iw = ip.item_words();
    int written = 0;
    uint64_t open_height = ~0ull; bool opened = false;
    auto ensure_open = [&](uint64_t height) -> bool {
        int st = 0;
        uint64_t ep = ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), height, &st);
        if (st != IGNPOW_OK) return false;
        if (opened) { int st2 = 0; uint64_t cur = ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), open_height, &st2); if (st2 == IGNPOW_OK && cur == ep) return true; eng.close(); opened = false; }
        Refusal r; Knobs k = g_vector_knobs; k.no_warm = true;
        if (!eng.open("igneumhash", height, k, 0, r)) { fprintf(stderr, "trace: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str()); return false; }
        opened = true; open_height = height; return true;
    };
    auto hash_one = [&](uint64_t height, const uint8_t header[32], uint64_t nonce, uint8_t mix[32], uint8_t digest[32]) -> bool {
        Job j; j.job_id = "identity"; j.height = height; std::memcpy(j.header, header, 32); std::memset(j.target, 0xff, 32); j.nonce_start = nonce; j.nonce_count = 1;
        bool got = false;
        eng.run_job(j, [&](const Result &r) { if (r.nonce == nonce) { std::memcpy(mix, r.mix, 32); std::memcpy(digest, r.digest, 32); got = true; } }, [](const BatchStats &) {}, [] { return false; });
        return got;
    };
    for (const char *cls : {"trace", "unseen", "nonce-boundary", "transitions", "malformed"}) {
        std::ifstream in(ref_dir + "/" + cls + ".json");
        if (!in) { fprintf(stderr, "trace: no reference %s/%s.json\n", ref_dir.c_str(), cls); continue; }
        std::string out_path = out_dir + "/" + prefix + cls + ".json";
        FILE *out = fopen(out_path.c_str(), "w");
        if (!out) { fprintf(stderr, "trace: cannot write %s\n", out_path.c_str()); continue; }
        std::string line; bool ok = true; uint64_t n = 0;
        while (std::getline(in, line)) {
            std::string ln = line;
            if (ln.find("\"generator\": ") != std::string::npos && ln.find("\"height\"") == std::string::npos) {
                replace_field(ln, "generator", "\"" + json_escape(eng.backend() + " worker: " + eng.describe()) + "\"");
            } else if (ln.find("\"height\"") != std::string::npos) {
                n++;
                Vector v; v.height = u64(field(line, "height")); v.header = field(line, "header"); v.nonce = u64(field(line, "nonce")); v.ctx_height = u64(field(line, "ctx_height")); v.expect = field(line, "expect");
                uint8_t header[32];
                if (!hex_to(v.header, header, 32)) { ok = false; break; }
                if (std::string(cls) == "malformed") {
                    int st = 0; ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), v.height, &st);
                    std::string got = reason_name(st);
                    if (st == IGNPOW_OK && v.ctx_height) { int st2 = 0; uint64_t e1 = ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), v.ctx_height, &st2), e2 = ignpow_epoch_of("igneumhash", g_vector_knobs.params.c_str(), v.height, &st2); if (e1 != e2) got = "epoch"; }
                    if (v.expect == "arg" || v.expect == "boundary" || v.expect == "mix") got = v.expect;   // the supervisor's classes: a worker never sees the field
                    replace_field(ln, "expect", "\"" + got + "\"");
                } else {
                    uint8_t mix[32], digest[32];
                    if (!ensure_open(v.height) || !hash_one(v.height, header, v.nonce, mix, digest)) { ok = false; break; }
                    replace_field(ln, "mix", "\"" + hex_of(mix, 32) + "\""); replace_field(ln, "hash", "\"" + hex_of(digest, 32) + "\"");
                    if (std::string(cls) == "trace") {
                        std::vector<uint32_t> w; Refusal r;
                        if (!eng.trace_one(v.height, header, v.nonce, w, r)) { fprintf(stderr, "trace: %s: %s\n", r.code.c_str(), r.explain.c_str()); ok = false; break; }
                        std::string t = "{\"S\": \"" + words_hex(&w[0], 8) + "\", \"E\": \"" + words_hex(&w[8], 8) + "\", \"P\": \"" + words_hex(&w[16], 8) + "\", \"init\": " + regs_text(&w[32]) + ", \"fill\": " + regs_text(&w[160]) + ", \"rounds\": [";
                        for (uint32_t rr = 0; rr < ip.rounds; rr++) {
                            const uint32_t *b = &w[704 + (size_t)rr * (130 + iw)];
                            t += rr ? ", {" : "{"; t += "\"r\": " + std::to_string(rr) + ", \"idx\": " + std::to_string(b[0]);
                            if (ip.version >= 4) t += ", \"h\": " + std::to_string(b[1]);
                            t += ", \"item\": ["; for (uint32_t k = 0; k < iw; k++) { t += k ? ", " : ""; t += std::to_string(b[2 + k]); } t += "], \"regs\": " + regs_text(&b[2 + iw]) + "}";
                        }
                        t += "], \"M\": \"" + words_hex(&w[560], 8) + "\", \"F\": \"" + words_hex(&w[568], 8) + "\"}";
                        replace_field(ln, "trace", t);
                    }
                }
            }
            fputs(ln.c_str(), out); fputc('\n', out);
        }
        fclose(out);
        if (!ok) { fprintf(stderr, "trace: %s: stopped at vector %llu (the file is incomplete)\n", cls, (unsigned long long)n); continue; }
        fprintf(stderr, "trace: wrote %s (%llu vectors)\n", out_path.c_str(), (unsigned long long)n); written++;
    }
    if (opened) eng.close();
    return written;
}

int boundary_check(EngineIface &eng, uint64_t height, uint32_t nonces) {
    IhParams ip; std::string perr;
    if (g_vector_knobs.params.empty() || !ih_parse_params(g_vector_knobs.params, ip, perr)) { fprintf(stderr, "boundary: a params document is required (--params): %s\n", perr.c_str()); return 1; }
    Refusal r; Knobs k = g_vector_knobs; k.no_warm = true;
    if (!eng.open("igneumhash", height, k, 0, r)) { fprintf(stderr, "boundary: open refused (%s): %s\n", r.code.c_str(), r.explain.c_str()); return 1; }
    const uint32_t iw = ip.item_words(); const uint64_t n_items = ip.n_items(), half = n_items / 2;
    int faults = 0;
    // the build side: the digest over both buffers against the reference's
    { uint8_t E[32], D[32], cd[32], dg[32];
      if (!eng.dataset_digests(E, D, cd, dg)) { printf("{\"boundary\": {\"digest\": \"unavailable\"}}\n"); faults++; }
      else { uint8_t e2[32], d2[32], cd2[32], dg2[32]; uint64_t bms = 0; int hd = 0; int st = -1;
             ignpow_ctx *rc = ignpow_ctx_create("igneumhash", g_vector_knobs.params.c_str(), height, 1, &st);   // the reference's own full dataset: its digest is the ground truth over both device buffers
             if (rc && st == IGNPOW_OK) { st = ignh_dataset_info(rc, e2, d2, cd2, dg2, &bms, &hd); ignpow_ctx_free(rc); }
             bool ok = st == IGNPOW_OK && std::memcmp(dg, dg2, 32) == 0; if (!ok) faults++;
             printf("{\"boundary\": {\"digest\": \"%s\", \"device\": \"%s\", \"reference\": \"%s\"}}\n", ok ? "match" : "MISMATCH", hex_of(dg, 32).c_str(), st == IGNPOW_OK ? hex_of(dg2, 32).c_str() : "unavailable"); } }
    // the explicit items: read back through the split-aware path (a read that fails names the item)
    for (uint64_t idx : { (uint64_t)0, half - 1, half, half + 1, n_items - 1 }) {
        uint32_t w0 = 0, wl = 0; bool ok = eng.dataset_word(idx * iw, &w0, false) && eng.dataset_word(idx * iw + iw - 1, &wl, false);
        printf("{\"boundary\": {\"item\": %llu, \"side\": \"%s\", \"readable\": %s, \"first_word\": %u, \"last_word\": %u}}\n", (unsigned long long)idx, idx < half ? "first buffer" : "second buffer", ok ? "true" : "false", w0, wl);
        if (!ok) faults++;
    }
    // the read side: the kernel's item words of every traced round against the device's words at the round's address
    uint64_t lower = 0, upper = 0, mism = 0, rounds = 0;
    uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(0x5a ^ i);
    for (uint32_t n = 0; n < nonces; n++) {
        std::vector<uint32_t> w; Refusal tr;
        if (!eng.trace_one(height, header, 0x1000000ull + n, w, tr)) { fprintf(stderr, "boundary: trace %u: %s: %s\n", n, tr.code.c_str(), tr.explain.c_str()); faults++; break; }
        for (uint32_t rr = 0; rr < ip.rounds; rr++) {
            const uint32_t *b = &w[704 + (size_t)rr * (130 + iw)]; const uint64_t idx = b[0];
            if (idx >= half) upper++; else lower++;
            rounds++;
            for (uint32_t j = 0; j < iw; j++) { uint32_t v = 0; if (!eng.dataset_word(idx * iw + j, &v, false) || v != b[2 + j]) { mism++; break; } }
        }
    }
    if (mism || upper == 0 || lower == 0) faults++;
    // the gate's form (security's TP-146): the three guarded tests as a record field; below = the last item of the first buffer and every
    // traced round there, at = the first of the second and every traced round there, above = the one past it; each ok only with the digest
    { bool rb = lower > 0 && mism == 0 && faults == 0, ra = upper > 0 && mism == 0 && faults == 0;
      printf("{\"boundary_test\": {\"below_2g\": \"%s\", \"at_2g\": \"%s\", \"above_2g\": \"%s\", \"split_item\": %llu}}\n", rb ? "ok" : "FAULT", ra ? "ok" : "FAULT", ra ? "ok" : "FAULT", (unsigned long long)half); }
    printf("{\"boundary\": {\"traced_nonces\": %u, \"rounds\": %llu, \"rounds_first_buffer\": %llu, \"rounds_second_buffer\": %llu, \"item_word_mismatches\": %llu, \"verdict\": \"%s\"}}\n", nonces, (unsigned long long)rounds, (unsigned long long)lower, (unsigned long long)upper, (unsigned long long)mism, faults ? "FAULT" : "boundary clean");
    eng.close();
    return faults;
}

}  // namespace igm
