// raster19.h: VERSION 19, the raster form that draws continuously (docs/gpu-b/RASTER-V19-DESIGN.md; the reference's namespace r19 on
// gpu-b/raster-v19-ref 3696b05ec). The host's part, shared by the GL and Metal workers: the key, the seed and the eight-lane chain are
// version 18's (raster_worker.h); the triangle takes the document's reach (15: five bits an offset, 31: six) and side (target_log2 14 or
// 15); R1 every box is computed from the keyed base alone (pixel = sat_add8x4(base, rgba) where the pixel centre is covered under the
// integer top-left rule, the base otherwise); R2 nothing is written to the surface; R4 the box fold (a, b) across the whole box
// row-major from a_0 = w_i and b_0 = fmix32(w_i ^ (i x 0x9E3779B1)) by a = fmix32(a ^ v ^ b), b = (b + a) ^ rotl(a, 13) (R4B); acc_i = a || b
// as eight LE bytes; M = BLAKE3(LP("IgneumHash/R0I/mix") || K || acc_0 .. acc_{t-1}); H the R0A final. The card's part is one call: a
// batch of attempts (each its t boxes) in, the accumulators out; the host digests them (this cut) and compares. the GPU team, 18 October 2026.
#pragma once
#include "raster_worker.h"
namespace igm {
struct R19Box { int32_t x0, y0, x1, y1, x2, y2; int32_t minx, miny; uint32_t w, h, rgba, a0, b0; };
inline uint32_t r19_side(const IhParams &p) { return p.registers == 15 ? 32768u : 16384u; }
inline uint32_t r19_reach(const IhParams &p) { return p.scratch_bytes == 31 ? 31u : 15u; }
inline uint32_t r19_cell(const IhParams &p) { return 2u * r19_reach(p) + 2u; }   // the atlas cell: a box's extent is at most 2 x reach + 1
inline uint32_t r19_rotl(uint32_t x, uint32_t n) { return (x << n) | (x >> (32u - n)); }
inline R19Box r19_box_of(uint32_t w, uint32_t i, uint32_t side, uint32_t reach) {
    const uint32_t u = rp_fmix32(w ^ 0x2545F491u), c = rp_fmix32(w ^ 0x9E3779B9u);
    const uint32_t bits = reach == 31u ? 6u : 5u, mask = (1u << bits) - 1u; const int32_t rr = (int32_t)reach;
    R19Box b;
    if (side == 32768u) { b.x0 = (int32_t)(w & 32767u); b.y0 = (int32_t)((w >> 15) & 32767u); } else { b.x0 = (int32_t)(w & 16383u); b.y0 = (int32_t)((w >> 14) & 16383u); }
    b.x1 = r18_clamp(b.x0 + (int32_t)(u & mask) - rr, side); b.y1 = r18_clamp(b.y0 + (int32_t)((u >> bits) & mask) - rr, side);
    b.x2 = r18_clamp(b.x0 + (int32_t)((u >> (2 * bits)) & mask) - rr, side); b.y2 = r18_clamp(b.y0 + (int32_t)((u >> (3 * bits)) & mask) - rr, side);
    b.rgba = c; b.minx = std::min(b.x0, std::min(b.x1, b.x2)); b.miny = std::min(b.y0, std::min(b.y1, b.y2));
    const int32_t maxx = std::max(b.x0, std::max(b.x1, b.x2)), maxy = std::max(b.y0, std::max(b.y1, b.y2));
    b.w = (uint32_t)(maxx - b.minx); b.h = (uint32_t)(maxy - b.miny); b.a0 = w; b.b0 = rp_fmix32(w ^ (i * 0x9E3779B1u));
    return b;
}
// the attempt's boxes from K and S: the eight-lane chain as version 18's (the triangle's word w = st[l] ^ Kw[(i >> 3) & 7], the lane stepped by the colour)
inline std::vector<R19Box> r19_boxes(const IhParams &p, const uint8_t K[32], const uint8_t S[32]) {
    uint32_t Kw[8]; for (int k = 0; k < 8; k++) Kw[k] = (uint32_t)K[4 * k] | ((uint32_t)K[4 * k + 1] << 8) | ((uint32_t)K[4 * k + 2] << 16) | ((uint32_t)K[4 * k + 3] << 24);
    uint32_t st[8]; { std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0I/init"); w.insert(w.end(), K, K + 32); w.insert(w.end(), S, S + 32); uint8_t d[32]; ih_blake3_full(w.data(), w.size(), d); for (int l = 0; l < 8; l++) st[l] = (uint32_t)d[4 * l] | ((uint32_t)d[4 * l + 1] << 8) | ((uint32_t)d[4 * l + 2] << 16) | ((uint32_t)d[4 * l + 3] << 24); }
    const uint32_t side = r19_side(p), reach = r19_reach(p), n = p.rounds;
    std::vector<R19Box> out(n);
    for (uint32_t i = 0; i < n; i++) { const uint32_t l = i & 7, w = st[l] ^ Kw[(i >> 3) & 7]; out[i] = r19_box_of(w, i, side, reach); st[l] = rp_fmix32(st[l] ^ out[i].rgba ^ (i * 0x9E3779B1u)); }
    return out;
}
inline uint32_t r19_sat_add(uint32_t v, uint32_t rgba) { uint32_t r = 0; for (int k = 0; k < 4; k++) { const uint32_t o = (v >> (8 * k)) & 255u, n = (rgba >> (8 * k)) & 255u; r |= std::min<uint32_t>(255u, o + n) << (8 * k); } return r; }
// the cover test as the reference's (integer edge functions, the top-left rule) in coordinates relative to the box (no overflow)
inline bool r19_covered(const R19Box &b, uint32_t lx, uint32_t ly) {
    const int64_t ax = b.x0 - b.minx, ay = b.y0 - b.miny, bx = b.x1 - b.minx, by = b.y1 - b.miny, cx = b.x2 - b.minx, cy = b.y2 - b.miny;
    const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax); if (area == 0) return false;
    const int64_t s = area > 0 ? 1 : -1; const int64_t px = 2 * (int64_t)lx + 1, py = 2 * (int64_t)ly + 1;
    const int64_t ex[3] = {ax, bx, cx}, ey[3] = {ay, by, cy}, fx[3] = {bx, cx, ax}, fy[3] = {by, cy, ay};
    for (int k = 0; k < 3; k++) { const int64_t dx = fx[k] - ex[k], dy = fy[k] - ey[k]; const bool tl = dy == 0 ? ((dx > 0 ? 1 : -1) == s) : ((-dy > 0 ? 1 : -1) == s);
        const int64_t w = ((2 * fx[k] - 2 * ex[k]) * (py - 2 * ey[k]) - (2 * fy[k] - 2 * ey[k]) * (px - 2 * ex[k])) * s; if (w < 0 || (w == 0 && !tl)) return false; }
    return true;
}
// the host fold of one box (the self-check's oracle; base_word gives the surface's word at (x, y), zero under the lab form)
template <class BaseFn> inline void r19_fold_host(const R19Box &b, BaseFn base_word, uint8_t acc[8]) {
    uint32_t a = b.a0, bb = b.b0;
    for (uint32_t ly = 0; ly < b.h; ly++) for (uint32_t lx = 0; lx < b.w; lx++) {
        uint32_t v = base_word((uint32_t)b.minx + lx, (uint32_t)b.miny + ly); if (r19_covered(b, lx, ly)) v = r19_sat_add(v, b.rgba);
        a = rp_fmix32(a ^ v ^ bb); bb = (bb + a) ^ r19_rotl(a, 13);   // R4B
    }
    for (int k = 0; k < 4; k++) { acc[k] = (uint8_t)(a >> (8 * k)); acc[4 + k] = (uint8_t)(bb >> (8 * k)); }
}
inline void r19_mix(const uint8_t K[32], const uint8_t *accs, size_t n_accs, uint8_t M[32]) {
    std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0I/mix"); w.insert(w.end(), K, K + 32); w.insert(w.end(), accs, accs + 8 * n_accs); ih_blake3_full(w.data(), w.size(), M);
}
// the card: a batch of attempts' boxes in, every box's eight accumulator bytes out (attempt-major, box order); the base through the version 18 card's dataset and fill
struct R19Card {
    std::function<bool(const std::vector<std::vector<R19Box>> &, std::vector<uint8_t> &)> batch;
    // THE FULL CARD PATH (the rate cut): the batch's nonces in; for each attempt M, F and the hit against the target out (the seed, the chain,
    // the draw, the fold, the mix and the final all on the card); hits the count of attempts under the target
    std::function<bool(const uint8_t *K, const uint8_t *P, const uint8_t *header, const std::vector<uint64_t> &nonces, const uint8_t *target, std::vector<std::array<uint8_t, 32>> &Ms, std::vector<std::array<uint8_t, 32>> &Fs, uint32_t &hits)> full;
    std::function<bool(const uint8_t *, uint64_t)> fill;
    std::function<const uint8_t *(uint64_t, uint64_t &, std::string &)> dataset;
    uint64_t epoch_filled = ~0ull;
};
inline bool r19_prepare(const IhParams &p, R19Card &card, uint64_t height, std::string &err) {
    if (!p.dataset_bytes) return true;
    const uint64_t ep = height / p.epoch_blocks; if (ep == card.epoch_filled) return true;
    if (!card.dataset || !card.fill) { err = "the card carries no dataset provider or fill for the keyed base"; return false; }
    fprintf(stderr, "raster: the keyed base: building the epoch dataset (%llu bytes) on the host\n", (unsigned long long)p.dataset_bytes); fflush(stderr);
    uint64_t n = 0; const uint8_t *d = card.dataset(height, n, err); if (!d) return false;
    if (n != p.dataset_bytes) { err = "the dataset is " + std::to_string(n) + " bytes, the base " + std::to_string(p.dataset_bytes); return false; }
    if (!card.fill(d, n)) { err = "the card refused the fill"; return false; }
    fprintf(stderr, "raster: the keyed base is filled for epoch %llu\n", (unsigned long long)ep); fflush(stderr);
    card.epoch_filled = ep; return true;
}
// the attempts through the card: the accumulators, the mix and the final on the host (this cut); Ms attempt-major
inline bool r19_attempts(const IhParams &p, R19Card &card, const std::vector<std::vector<R19Box>> &attempts, const uint8_t K[32], std::vector<std::array<uint8_t, 32>> &Ms) {
    if (!card.batch) { fprintf(stderr, "raster: the card's batch is not bound\n"); return false; }
    std::vector<uint8_t> accs; if (!card.batch(attempts, accs)) return false;
    if (accs.size() != attempts.size() * (size_t)p.rounds * 8) { fprintf(stderr, "raster: the card returned %zu accumulator bytes for %zu attempts\n", accs.size(), attempts.size()); return false; }
    Ms.resize(attempts.size()); for (size_t a = 0; a < attempts.size(); a++) r19_mix(K, &accs[a * (size_t)p.rounds * 8], p.rounds, Ms[a].data());
    return true;
}
inline int r19_vectors(const char *backend, const std::string &device, const IhParams &p, const std::vector<std::string> &files, R19Card &card, const std::string &note, size_t batch_n) {
    uint8_t P[32]; ih_P(p, P); size_t total = 0, mism = 0, unread = 0;
    for (const std::string &f : files) {
        std::string txt; { FILE *fp = fopen(f.c_str(), "rb"); if (!fp) { fprintf(stderr, "vectors: cannot read %s\n", f.c_str()); unread++; continue; } char buf[65536]; size_t n; while ((n = fread(buf, 1, sizeof buf, fp)) > 0) txt.append(buf, n); fclose(fp); }
        Json j; if (!Json::parse(txt, j)) { fprintf(stderr, "vectors: %s: not JSON\n", f.c_str()); unread++; continue; }
        const std::string cls = j.has("class") ? j["class"].as_string() : "?"; size_t n = 0, m = 0;
        const Json &vs = j["vectors"];
        struct Rec { uint64_t height, nonce; uint8_t header[32], want_mix[32], want_hash[32], S[32], K[32]; };
        std::vector<Rec> recs;
        for (size_t i = 0; i < vs.size(); i++) {
            const Json &v = vs.at(i); auto num = [](const Json &x) -> uint64_t { return x.type() == Json::String ? strtoull(x.as_string().c_str(), nullptr, 10) : x.as_u64(); };
            Rec r; r.height = num(v["height"]); r.nonce = num(v["nonce"]);
            if (!r18_hex(v["header"].as_string(), r.header, 32) || !r18_hex(v["mix"].as_string(), r.want_mix, 32) || !r18_hex(v["hash"].as_string(), r.want_hash, 32)) { fprintf(stderr, "vectors: a malformed record in %s\n", f.c_str()); return 2; }
            std::string err; r18_seed(p, P, r.header, r.nonce, r.S); if (!r18_key(p, r.height, r.K, err)) { fprintf(stderr, "vectors: key: %s\n", err.c_str()); return 2; }
            recs.push_back(r);
        }
        size_t i = 0;
        while (i < recs.size()) {
            size_t jx = i + 1; const uint64_t ep = recs[i].height / p.epoch_blocks;
            while (jx < recs.size() && jx - i < batch_n && memcmp(recs[jx].K, recs[i].K, 32) == 0 && recs[jx].height / p.epoch_blocks == ep) jx++;
            std::string err; if (!r19_prepare(p, card, recs[i].height, err)) { fprintf(stderr, "vectors: %s\n", err.c_str()); return 2; }
            std::vector<std::array<uint8_t, 32>> Ms, Fs;
            if (card.full && !getenv("IGM_R19_HOST_GEN")) {   // the full card path: the records and the digests on the card (the sets prove G, D, F and H together); one header a batch
                size_t kx = i + 1; while (kx < jx && memcmp(recs[kx].header, recs[i].header, 32) == 0) kx++; jx = kx;
                std::vector<uint64_t> nonces; for (size_t k = i; k < jx; k++) nonces.push_back(recs[k].nonce);
                static const uint8_t zero_target[32] = {0}; uint32_t hits = 0;
                if (!card.full(recs[i].K, P, recs[i].header, nonces, zero_target, Ms, Fs, hits)) { fprintf(stderr, "vectors: the card refused the batch\n"); return 2; }
            } else {
                std::vector<std::vector<R19Box>> batch; for (size_t k = i; k < jx; k++) batch.push_back(r19_boxes(p, recs[k].K, recs[k].S));
                if (!r19_attempts(p, card, batch, recs[i].K, Ms)) { fprintf(stderr, "vectors: the card refused the batch\n"); return 2; }
            }
            for (size_t k = i; k < jx; k++) {
                uint8_t H[32]; if (!Fs.empty()) memcpy(H, Fs[k - i].data(), 32); else ih_final_digest(p.chain_id, p.version, P, recs[k].header, recs[k].nonce, Ms[k - i].data(), H);
                n++; if (memcmp(Ms[k - i].data(), recs[k].want_mix, 32) != 0 || memcmp(H, recs[k].want_hash, 32) != 0) { m++; if (m <= 4) fprintf(stderr, "vectors: MISMATCH %zu height %llu nonce %llu\n  want mix %s hash %s\n  got  mix %s hash %s\n", m, (unsigned long long)recs[k].height, (unsigned long long)recs[k].nonce, hex_of(recs[k].want_mix, 32).c_str(), hex_of(recs[k].want_hash, 32).c_str(), hex_of(Ms[k - i].data(), 32).c_str(), hex_of(H, 32).c_str()); }
            }
            i = jx;
        }
        fprintf(stderr, "vectors: igneumhash class %s: %zu vectors, %zu mismatches, 0 unavailable (open refused)\n", cls.c_str(), n, m);
        printf("{\"class\": \"%s\", \"vectors\": %zu, \"mismatches\": %zu, \"refused\": 0}\n", cls.c_str(), n, m); total += n; mism += m;
    }
    printf("{\"summary\": {\"device\": \"%s\", \"arch\": \"%s\", \"vectors\": %zu, \"mismatches\": %zu, \"refused\": 0, \"refused_room\": 0, \"verdict\": \"%s\", \"notes\": \"%s %s\"}}\n", device.c_str(), device.c_str(), total, mism, mism == 0 && unread == 0 ? "exact" : "mismatch", backend, note.c_str());
    fflush(stdout); return mism == 0 && unread == 0 ? 0 : 3;
}
}  // namespace igm
