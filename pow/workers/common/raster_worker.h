// The version 18 raster worker's host side, shared by the OpenGL program (pow/workers/raster) and the Metal worker's raster-vectors mode: the
// reference's r18 text (main a82b8a30) with the drawing on the card. Per attempt: K = BLAKE3(LP("IgneumHash/R0I/key") || LE64 chain ||
// LE64 period || anchor); st = BLAKE3(LP("IgneumHash/R0I/init") || K || S) as eight LE words, S the R0A seed of the header and nonce;
// triangle i: w = st[l] ^ Kw[(i >> 3) & 7], l = i & 7, the probe's derivation with w for its sequence word, st[l] = fmix32(st[l] ^ colour
// ^ (i * 0x9E3779B1)); the card draws them (the probe's rule), the boxes (the clamped vertices' bounding boxes, row-major, RGBA, in
// triangle order, read after every triangle is drawn) go with the key into M = BLAKE3(LP("IgneumHash/R0I/mix") || K || boxes); the final
// digest the R0A final over P, S and M (ih_final_digest). The card's duty: draw, hand back the boxes' bytes, clear the boxes.
#pragma once
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>
#include <array>
#include <functional>
#include <algorithm>
#include "ih_params.h"
#include "raster_probe.h"
#include "json.h"
#include "hex.h"

namespace igm {
struct R18Tri { int32_t x0, y0, x1, y1, x2, y2; uint32_t rgba; int32_t minx, maxx, miny, maxy;
    // F-441 (KEYED-RELEASED-VERSIONS-SPEC.md section 3): a bond-keyed triangle carries its share s_k as eight LE32 words; the card XORs them
    // into the first eight pixels of the box (row-major from (minx, miny), wrapping on a narrower box, never past maxy; an empty box takes
    // none) BEFORE the triangle is drawn, on the target's word at that moment; the restore after the attempt undoes it
    uint32_t kw[8] = {0, 0, 0, 0, 0, 0, 0, 0}; int32_t keyed = -1;   // keyed = the share index k, or -1
};
// the eight pixels of a keyed triangle's box in the share's order (the reference's keyed_tri::pixels): fn(j, x, y) for j = 0..7 while the box has them
template <class F> inline void r18_keyed_pixels(const R18Tri &t, F fn) {
    int32_t x = t.minx, y = t.miny;
    for (int j = 0; j < 8; ++j) { if (y >= t.maxy || t.minx >= t.maxx) return; fn(j, x, y); if (++x >= t.maxx) { x = t.minx; ++y; } }
}
// 18b (the project's recut on F-129): "target_log2" 14 = a 16384 x 16384 target (registers 14 in the canonical bytes), 14 bits a coordinate; 13 or absent the 8192 form
inline uint32_t r18_side(const IhParams &p) { return p.registers == 14 ? 16384u : 8192u; }
inline int32_t r18_clamp(int32_t v, uint32_t side) { return v < 0 ? 0 : v > (int32_t)side ? (int32_t)side : v; }
inline R18Tri r18_triangle_of(uint32_t w, uint32_t side) {
    const uint32_t u = rp_fmix32(w ^ 0x2545F491u), c = rp_fmix32(w ^ 0x9E3779B9u);
    R18Tri r; if (side == 16384u) { r.x0 = (int32_t)(w & 16383u); r.y0 = (int32_t)((w >> 14) & 16383u); } else { r.x0 = (int32_t)(w & 8191u); r.y0 = (int32_t)((w >> 13) & 8191u); }
    r.x1 = r18_clamp(r.x0 + (int32_t)(u & 31u) - 15, side); r.y1 = r18_clamp(r.y0 + (int32_t)((u >> 5) & 31u) - 15, side);
    r.x2 = r18_clamp(r.x0 + (int32_t)((u >> 10) & 31u) - 15, side); r.y2 = r18_clamp(r.y0 + (int32_t)((u >> 15) & 31u) - 15, side);
    r.rgba = c; r.minx = std::min(r.x0, std::min(r.x1, r.x2)); r.maxx = std::max(r.x0, std::max(r.x1, r.x2)); r.miny = std::min(r.y0, std::min(r.y1, r.y2)); r.maxy = std::max(r.y0, std::max(r.y1, r.y2));
    return r;
}
inline void r18_lp(std::vector<uint8_t> &v, const char *t) { const size_t n = strlen(t); v.push_back((uint8_t)n); v.insert(v.end(), t, t + n); }
inline void r18_le64(std::vector<uint8_t> &v, uint64_t x) { for (int i = 0; i < 8; i++) v.push_back((uint8_t)(x >> (8 * i))); }
// the R0A seed S of a header and nonce (the first half of ih_final_digest)
inline void r18_seed(const IhParams &p, const uint8_t P[32], const uint8_t header[32], uint64_t nonce, uint8_t S[32]) {
    std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0A/seed"); for (int i = 7; i >= 0; --i) w.push_back((uint8_t)(p.chain_id >> (8 * i))); for (int i = 3; i >= 0; --i) w.push_back((uint8_t)(p.version >> (8 * i)));
    w.insert(w.end(), P, P + 32); w.insert(w.end(), header, header + 32); for (int i = 7; i >= 0; --i) w.push_back((uint8_t)(nonce >> (8 * i)));
    ih_blake3_full(w.data(), w.size(), S);
}
inline bool r18_key(const IhParams &p, uint64_t height, uint8_t K[32], std::string &err) {
    uint8_t anchor[32] = {0}; if (!p.program_anchor.empty()) { if (p.program_anchor.size() != 64) { err = "anchor"; return false; } for (int i = 0; i < 32; i++) anchor[i] = (uint8_t)strtoul(p.program_anchor.substr(2 * i, 2).c_str(), nullptr, 16); }
    std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0I/key"); r18_le64(w, p.chain_id); r18_le64(w, p.period_blocks ? height / p.period_blocks : 0); w.insert(w.end(), anchor, anchor + 32);
    ih_blake3_full(w.data(), w.size(), K); return true;
}
// the attempt's triangles from K and S
// F-441: the bond-keyed triangles (features bit 10, keyed_rounds = S): triangle i is keyed iff (i x S) mod rounds < S, its index
// k = (i x S) div rounds; m_0 = the work seed S, m_k (k >= 1) = the eight state words st[0..7] as LE32 at the keyed triangle's turn,
// BEFORE its word w is formed and before the state update; s_k = ignh_bond_share(sk, k, m_k) (the reference library's one function);
// sk null = the unkeyed walk (the twin: no triangle keyed); shares_out (S x 32 bytes) and m_out (S x 32) written when given
#ifndef R18_NO_KEYED
extern "C" void ignh_bond_share(const uint8_t sk[32], uint32_t i, const uint8_t m[32], uint8_t share_out[32]);
#endif
inline std::vector<R18Tri> r18_triangles(const IhParams &p, const uint8_t K[32], const uint8_t S[32], const uint8_t *sk = nullptr, uint8_t *shares_out = nullptr, uint8_t *m_out = nullptr) {
    uint32_t Kw[8]; for (int k = 0; k < 8; k++) Kw[k] = (uint32_t)K[4 * k] | ((uint32_t)K[4 * k + 1] << 8) | ((uint32_t)K[4 * k + 2] << 16) | ((uint32_t)K[4 * k + 3] << 24);
    uint8_t d[32]; { std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0I/init"); w.insert(w.end(), K, K + 32); w.insert(w.end(), S, S + 32); ih_blake3_full(w.data(), w.size(), d); }
    uint32_t st[8]; for (int l = 0; l < 8; l++) st[l] = (uint32_t)d[4 * l] | ((uint32_t)d[4 * l + 1] << 8) | ((uint32_t)d[4 * l + 2] << 16) | ((uint32_t)d[4 * l + 3] << 24);
    std::vector<R18Tri> t(p.rounds); const uint32_t side = r18_side(p);
    const uint32_t KS = ((p.features & 1024u) && sk) ? p.keyed_rounds : 0u;
    for (uint32_t i = 0; i < p.rounds; i++) {
        const uint32_t l = i & 7;
#ifndef R18_NO_KEYED
        if (KS && ((uint64_t)i * KS) % p.rounds < KS) {   // the keyed triangle's turn: the message from the state as it stands, the share by the reference's function
            const uint32_t kk = (uint32_t)(((uint64_t)i * KS) / p.rounds); uint8_t m[32], sh[32];
            if (kk == 0) std::memcpy(m, S, 32); else for (int q = 0; q < 8; q++) { m[4 * q] = (uint8_t)st[q]; m[4 * q + 1] = (uint8_t)(st[q] >> 8); m[4 * q + 2] = (uint8_t)(st[q] >> 16); m[4 * q + 3] = (uint8_t)(st[q] >> 24); }
            ignh_bond_share(sk, kk, m, sh);
            if (shares_out) std::memcpy(shares_out + (size_t)kk * 32, sh, 32); if (m_out) std::memcpy(m_out + (size_t)kk * 32, m, 32);
            const uint32_t w = st[l] ^ Kw[(i >> 3) & 7]; t[i] = r18_triangle_of(w, side); t[i].keyed = (int32_t)kk;
            for (int q = 0; q < 8; q++) t[i].kw[q] = (uint32_t)sh[4 * q] | ((uint32_t)sh[4 * q + 1] << 8) | ((uint32_t)sh[4 * q + 2] << 16) | ((uint32_t)sh[4 * q + 3] << 24);
            st[l] = rp_fmix32(st[l] ^ t[i].rgba ^ (i * 0x9E3779B1u)); continue;
        }
#endif
        const uint32_t w = st[l] ^ Kw[(i >> 3) & 7]; t[i] = r18_triangle_of(w, side); st[l] = rp_fmix32(st[l] ^ t[i].rgba ^ (i * 0x9E3779B1u));
    }
    return t;
}
// the mix from the boxes the card handed back (one byte vector per triangle, the box row-major RGBA; empty for an empty box)
inline void r18_mix(const uint8_t K[32], const std::vector<std::vector<uint8_t>> &boxes, uint8_t M[32]) {
    std::vector<uint8_t> w; r18_lp(w, "IgneumHash/R0I/mix"); w.insert(w.end(), K, K + 32); for (const auto &b : boxes) w.insert(w.end(), b.begin(), b.end()); ih_blake3_full(w.data(), w.size(), M);
}
// the card's part: draw the triangles, hand back the boxes, restore the boxes (to zero under the first rule; to the keyed target's content under
// 18c). 18c (the panel's cure on 18b): the target PRE-FILLED with the keyed epoch dataset, pixel (i, j) = the LE32 word j x side + i (the
// dataset's bytes row-major ARE the target's bytes), every covered pixel a read-modify-write against it, the boxes digested as before (the
// keyed content where untouched). The host copy of the dataset lives with the card (fill keeps the pointer, restore reads it); the
// dataset provider (the reference's full context: ignh_inner_create / ignh_inner_dataset) is the card's too, asked once an epoch.
struct R18Card {
    std::function<bool(const std::vector<R18Tri> &)> draw;
    std::function<bool(const std::vector<R18Tri> &, std::vector<std::vector<uint8_t>> &)> read_boxes;
    std::function<bool(const std::vector<R18Tri> &)> restore;                       // the boxes back to the target's base content (zero, or the keyed bytes)
    std::function<bool(const uint8_t *, uint64_t)> fill;                             // 18c: the whole target from the dataset's bytes, once an epoch
    std::function<bool(const std::vector<R18Tri> &, const uint8_t *, uint8_t *)> digest;   // the mix ON THE CARD (the project's order 15 October 2026 15:4x UK): the boxes digested where they lie, the host reads one small buffer; (tris, K) -> M; when set, read_boxes and r18_mix are not called
    std::function<bool(const std::vector<std::vector<R18Tri>> &, const uint8_t *, std::vector<std::array<uint8_t, 32>> &)> batch;   // ATTEMPTS IN FLIGHT (the founder's order 18:0x UK): M attempts queued on the card back to back (draw, digest, restore each), the chunk values read ONCE a batch; (attempts, K) -> the M mixes
    std::function<const uint8_t *(uint64_t, uint64_t &, std::string &)> dataset;    // 18c: the epoch dataset for a height (the bytes stay valid until the next call)
    uint64_t epoch_filled = ~0ull;
};
// 18c: the target filled for the vector's epoch when it changes; true when nothing was needed or the fill went through
inline bool r18_prepare(const IhParams &p, R18Card &card, uint64_t height, std::string &err) {
    if (!p.dataset_bytes) return true;
    const uint64_t ep = height / p.epoch_blocks; if (ep == card.epoch_filled) return true;
    if (!card.dataset || !card.fill) { err = "the card carries no dataset provider or fill for the keyed target (18c)"; return false; }
    fprintf(stderr, "raster: the keyed target: building the epoch dataset (%llu bytes) on the host\n", (unsigned long long)p.dataset_bytes); fflush(stderr);
    uint64_t n = 0; const uint8_t *d = card.dataset(height, n, err); if (!d) return false;
    if (n != p.dataset_bytes) { err = "the dataset is " + std::to_string(n) + " bytes, the target " + std::to_string(p.dataset_bytes); return false; }
    fprintf(stderr, "raster: the keyed target: filling the card\n"); fflush(stderr);
    if (!card.fill(d, n)) { err = "the card refused the fill"; return false; }
    fprintf(stderr, "raster: the keyed target is filled for epoch %llu\n", (unsigned long long)ep); fflush(stderr);
    card.epoch_filled = ep; return true;
}
inline bool r18_hex(const std::string &h, uint8_t *out, size_t n) { if (h.size() != 2 * n) return false; for (size_t i = 0; i < n; i++) out[i] = (uint8_t)strtoul(h.substr(2 * i, 2).c_str(), nullptr, 16); return true; }
// one attempt on the card: the draw, the mix (on the card when the card digests, else the boxes read back and mixed here), the restore
// a card whose callbacks are not bound refuses with a line, never a throw (an empty std::function throws std::bad_function_call and ends the
// worker: d12-sealer-2's GL worker at the anchor boundary, 16 October 2026 06:24:55Z, a reconfigure rebuilding the card under a running job)
inline bool r18_card_bound(const R18Card &card) {
    if (card.draw && card.restore && (card.digest || card.read_boxes)) return true;
    fprintf(stderr, "raster: the card's callbacks are not bound (the card is being rebuilt, or was never made): the attempt is refused\n"); return false;
}
inline bool r18_attempt(R18Card &card, const std::vector<R18Tri> &tris, const uint8_t K[32], uint8_t M[32]) {
    if (!r18_card_bound(card)) return false;
    if (!card.draw(tris)) return false;
    if (card.digest) { if (!card.digest(tris, K, M)) return false; }
    else { std::vector<std::vector<uint8_t>> boxes; if (!card.read_boxes(tris, boxes)) return false; r18_mix(K, boxes, M); }
    return card.restore(tris);
}
// a batch of attempts: the card's batch when it has one, else one r18_attempt each
inline bool r18_attempts(R18Card &card, const std::vector<std::vector<R18Tri>> &attempts, const uint8_t K[32], std::vector<std::array<uint8_t, 32>> &Ms) {
    if (!r18_card_bound(card)) return false;
    if (card.batch) return card.batch(attempts, K, Ms);
    Ms.resize(attempts.size()); for (size_t i = 0; i < attempts.size(); i++) if (!r18_attempt(card, attempts[i], K, Ms[i].data())) return false; return true;
}
// the vectors run: every file's records against the card; the class lines as the vectors runner prints them, then the summary JSON; 0 on exact
inline int r18_vectors(const char *backend, const std::string &device, const IhParams &p, const std::vector<std::string> &files, R18Card &card, const std::string &note) {
    uint8_t P[32]; ih_P(p, P); size_t total = 0, mism = 0, unread = 0;
    for (const std::string &f : files) {
        std::string txt; { FILE *fp = fopen(f.c_str(), "rb"); if (!fp) { fprintf(stderr, "vectors: cannot read %s\n", f.c_str()); unread++; continue; } char buf[65536]; size_t n; while ((n = fread(buf, 1, sizeof buf, fp)) > 0) txt.append(buf, n); fclose(fp); }
        Json j; if (!Json::parse(txt, j)) { fprintf(stderr, "vectors: %s: not JSON\n", f.c_str()); unread++; continue; }
        const std::string cls = j.has("class") ? j["class"].as_string() : "?"; size_t n = 0, m = 0;
        const Json &vs = j["vectors"];
        // the vectors in batches of up to 16 sharing a period key (and so one dataset epoch): the card's batch path is what the sets prove when the card has one
        struct Rec { uint64_t height, nonce; uint8_t header[32], want_mix[32], want_hash[32], S[32], K[32]; bool keyed = false; uint8_t sk[32]; std::string want_shares, want_m; };   // F-441: the keyed class carries test_sk, shares and m
        std::vector<Rec> recs;
        for (size_t i = 0; i < vs.size(); i++) {
            const Json &v = vs.at(i); auto num = [](const Json &x) -> uint64_t { return x.type() == Json::String ? strtoull(x.as_string().c_str(), nullptr, 10) : x.as_u64(); };
            Rec r; r.height = num(v["height"]); r.nonce = num(v["nonce"]);
            if (!r18_hex(v["header"].as_string(), r.header, 32) || !r18_hex(v["mix"].as_string(), r.want_mix, 32) || !r18_hex(v["hash"].as_string(), r.want_hash, 32)) { fprintf(stderr, "vectors: a malformed record in %s\n", f.c_str()); return 2; }
            if (v.has("test_sk")) { if (!(p.features & 1024u)) { fprintf(stderr, "vectors: a keyed record under an unkeyed document (%s)\n", f.c_str()); return 2; } if (!r18_hex(v["test_sk"].as_string(), r.sk, 32)) { fprintf(stderr, "vectors: a malformed test_sk in %s\n", f.c_str()); return 2; } r.keyed = true; r.want_shares = v.has("shares") ? v["shares"].as_string() : ""; r.want_m = v.has("m") ? v["m"].as_string() : ""; }
            else if (p.features & 1024u) { fprintf(stderr, "vectors: a keyed document needs test_sk on every record (%s)\n", f.c_str()); return 2; }
            std::string err; r18_seed(p, P, r.header, r.nonce, r.S); if (!r18_key(p, r.height, r.K, err)) { fprintf(stderr, "vectors: key: %s\n", err.c_str()); return 2; }
            recs.push_back(r);
        }
        size_t i = 0;
        while (i < recs.size()) {
            size_t jx = i + 1; const uint64_t ep = recs[i].height / p.epoch_blocks;
            while (jx < recs.size() && jx - i < 16 && memcmp(recs[jx].K, recs[i].K, 32) == 0 && recs[jx].height / p.epoch_blocks == ep) jx++;
            std::string err; if (!r18_prepare(p, card, recs[i].height, err)) { fprintf(stderr, "vectors: %s\n", err.c_str()); return 2; }
            std::vector<std::vector<R18Tri>> batch;
            for (size_t k = i; k < jx; k++) {
                if (!recs[k].keyed) { batch.push_back(r18_triangles(p, recs[k].K, recs[k].S)); continue; }
#ifdef R18_NO_KEYED
                fprintf(stderr, "vectors: this build carries no reference library: the keyed class cannot run\n"); return 2;
#else
                std::vector<uint8_t> sh((size_t)p.keyed_rounds * 32), mm((size_t)p.keyed_rounds * 32);   // F-441: the shares and the messages, checked against the record
                batch.push_back(r18_triangles(p, recs[k].K, recs[k].S, recs[k].sk, sh.data(), mm.data()));
                std::string hs, hm; for (uint8_t b : sh) { static const char *hx = "0123456789abcdef"; hs.push_back(hx[b >> 4]); hs.push_back(hx[b & 15]); } for (uint8_t b : mm) { static const char *hx = "0123456789abcdef"; hm.push_back(hx[b >> 4]); hm.push_back(hx[b & 15]); }
                if ((!recs[k].want_shares.empty() && recs[k].want_shares != hs) || (!recs[k].want_m.empty() && recs[k].want_m != hm)) { m++; if (m <= 4) fprintf(stderr, "vectors: KEYED MISMATCH (the shares or the messages) height %llu nonce %llu\n", (unsigned long long)recs[k].height, (unsigned long long)recs[k].nonce); }
#endif
            }
            std::vector<std::array<uint8_t, 32>> Ms; if (!r18_attempts(card, batch, recs[i].K, Ms)) { fprintf(stderr, "vectors: the card refused the attempt\n"); return 2; }
            for (size_t k = i; k < jx; k++) {
                uint8_t H[32]; ih_final_digest(p.chain_id, p.version, P, recs[k].header, recs[k].nonce, Ms[k - i].data(), H);
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
}
