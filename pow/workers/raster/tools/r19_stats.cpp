// r19_stats: RASTER-V19-DESIGN.md section 9's statistics on a version 19 document and section 7's overlap and saturation classes by search
// (the GPU team, 18 October 2026). The boxes by the worker's host logic (raster19.h, exact against the reference on every set), the records' mix and
// hash by the reference's ignpow_hash over the full context (the keyed base when the document carries it). Usage:
//   r19_stats '<document json>' <height> <attempts> <out dir>
// Prints one JSON line of statistics (an attempt's distinct 256-byte items, 32-byte sectors, box bytes, rows and 2 KB pages, the mean over the
// attempts; the batch's page visits and distinct pages at 256 and 1,024 attempts) and writes overlap-16.json (sixteen attempts whose boxes
// overlap on the surface) and saturation-16.json (sixteen attempts with a covered pixel whose add saturates, the keyed base alone) in the
// vectors tool's form with the class names "overlap" and "saturation".
#include "../../common/raster19.h"
#include "ignpow.h"
extern "C" void *ignh_inner_create(const char *json, uint64_t height, int full, int *status);
extern "C" const uint8_t *ignh_inner_dataset(const void *ctx, uint64_t *bytes);
extern "C" void ignh_inner_destroy(void *ctx);
#include <set>
#include <unordered_set>
#include <fstream>
using namespace igm;
static std::string hex(const uint8_t *p, size_t n) { static const char *h = "0123456789abcdef"; std::string s; for (size_t i = 0; i < n; i++) { s.push_back(h[p[i] >> 4]); s.push_back(h[p[i] & 15]); } return s; }
int main(int argc, char **argv) {
    if (argc < 5) { fprintf(stderr, "usage: r19_stats '<document>' <height> <attempts> <out dir>\n"); return 2; }
    const std::string doc = argv[1]; const uint64_t height = strtoull(argv[2], nullptr, 10); const uint32_t natt = (uint32_t)atoi(argv[3]); const std::string out = argv[4];
    IhParams p; std::string err; if (!ih_parse_params(doc, p, err) || p.version != 19) { fprintf(stderr, "params: %s\n", err.c_str()); return 2; }
    int st = 0; ignpow_ctx *ctx = ignpow_ctx_create("igneumhash", doc.c_str(), height, 1, &st); if (!ctx) { fprintf(stderr, "the reference refused the context (%d)\n", st); return 2; }
    uint8_t P[32]; ih_P(p, P); uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); uint8_t K[32]; r18_key(p, height, K, err);
    const uint32_t side = r19_side(p); const uint64_t items_per_row = side / 64, pages_per_row = side / 512;
    // the base's words for the saturation test: the reference's full dataset through the context's dataset export
    void *inner = nullptr; const uint8_t *base = nullptr; uint64_t nbase = 0;
    if (p.dataset_bytes) { int rc = 0; inner = ignh_inner_create(doc.c_str(), height, 1, &rc); if (inner) base = ignh_inner_dataset(inner, &nbase); if (!base) { fprintf(stderr, "no dataset from the reference (%d)\n", rc); return 2; } }
    double s_items = 0, s_sect = 0, s_bytes = 0, s_rows = 0, s_pages = 0, s_cov = 0; uint64_t visits256 = 0, visits1024 = 0; std::unordered_set<uint64_t> pages256, pages1024;
    std::vector<uint64_t> overlap_n, sat_n; std::vector<uint32_t> sat_count;
    for (uint64_t nonce = 0; nonce < natt || overlap_n.size() < 16 || (p.dataset_bytes && sat_n.size() < 16); nonce++) {
        if (nonce > 100000) break;
        uint8_t S[32]; r18_seed(p, P, header, nonce, S); const std::vector<R19Box> boxes = r19_boxes(p, K, S);
        std::unordered_set<uint64_t> items, sects, pages; uint64_t bytes = 0, rows = 0, cov = 0, satp = 0; bool overlap = false;
        std::vector<std::pair<uint64_t, uint64_t>> spans;   // (row, minx..maxx) for the overlap test
        for (uint32_t i = 0; i < boxes.size(); i++) { const R19Box &b = boxes[i]; if (!b.w || !b.h) continue; bytes += (uint64_t)b.w * b.h * 4; rows += b.h;
            for (uint32_t ly = 0; ly < b.h; ly++) { const uint64_t y = (uint64_t)b.miny + ly; const uint64_t p0 = (y * side + b.minx) >> 9, p1 = (y * side + b.minx + b.w - 1) >> 9;
                for (uint64_t pg = p0; pg <= p1; pg++) { pages.insert(pg); if (nonce < 256) { visits256++; pages256.insert(pg); } if (nonce < 1024) { visits1024++; pages1024.insert(pg); } }
                for (uint32_t lx = 0; lx < b.w; lx++) { const uint64_t pix = y * side + b.minx + lx; items.insert(pix >> 6); sects.insert(pix >> 3);
                    if (r19_covered(b, lx, ly)) { cov++; if (base) { const uint32_t v = (uint32_t)base[4 * pix] | ((uint32_t)base[4 * pix + 1] << 8) | ((uint32_t)base[4 * pix + 2] << 16) | ((uint32_t)base[4 * pix + 3] << 24); for (int k = 0; k < 4; k++) if (((v >> (8 * k)) & 255u) + ((b.rgba >> (8 * k)) & 255u) > 255u) { satp++; break; } } } }
                // the overlap test: any earlier box's span on this row intersecting [minx, minx + w)
                for (uint32_t j = 0; j < i && !overlap; j++) { const R19Box &c = boxes[j]; if (!c.w || !c.h) continue; if (y >= (uint64_t)c.miny && y < (uint64_t)c.miny + c.h && (int64_t)b.minx < (int64_t)c.minx + c.w && (int64_t)c.minx < (int64_t)b.minx + b.w) overlap = true; }
            } }
        if (nonce < natt) { s_items += items.size(); s_sect += sects.size(); s_bytes += bytes; s_rows += rows; s_pages += pages.size(); s_cov += cov; }
        if (overlap && overlap_n.size() < 16) overlap_n.push_back(nonce);
        if (p.dataset_bytes && satp && sat_n.size() < 16) { sat_n.push_back(nonce); sat_count.push_back((uint32_t)satp); }
    }
    printf("{\"r19_stats\": {\"document\": %s, \"attempts\": %u, \"per_attempt\": {\"items_256b\": %.1f, \"sectors_32b\": %.1f, \"box_bytes\": %.1f, \"box_rows\": %.1f, \"pages_2k\": %.1f, \"covered_pixels\": %.1f}, \"batch_256\": {\"page_visits\": %llu, \"distinct_pages\": %zu}, \"batch_1024\": {\"page_visits\": %llu, \"distinct_pages\": %zu}, \"surface_pages\": %llu, \"overlap_attempts_searched\": \"the first 16 among the nonces walked\"}}\n",
           doc.c_str(), natt, s_items / natt, s_sect / natt, s_bytes / natt, s_rows / natt, s_pages / natt, s_cov / natt, (unsigned long long)visits256, pages256.size(), (unsigned long long)visits1024, pages1024.size(), (unsigned long long)(items_per_row * 0 + (uint64_t)side * pages_per_row));
    auto write_set = [&](const std::string &cls, const std::vector<uint64_t> &nonces, const std::vector<uint32_t> *counts) {
        std::ofstream o(out + "/v19" + (p.dataset_bytes ? "k" : "") + "-raster-t" + std::to_string(p.rounds) + "-" + cls + "-16.json");
        o << "{\n  \"candidate\": \"igneumhash\",\n  \"class\": \"" << cls << "\",\n  \"generator\": \"r19_stats on " << ignpow_build_string() << " (the worker's host logic for the search, the reference's hash for the records)\",\n  \"seed\": \"0\",\n  \"params\": " << doc << ",\n  \"count\": " << nonces.size() << ",\n  \"vectors\": [\n";
        for (size_t i = 0; i < nonces.size(); i++) { uint8_t mix[32], hash[32]; const int rc = ignpow_hash(ctx, height, header, nonces[i], mix, hash); if (rc != IGNPOW_OK) { fprintf(stderr, "hash refused (%d)\n", rc); exit(2); }
            o << "    {\"height\": " << height << ", \"header\": \"" << hex(header, 32) << "\", \"nonce\": \"" << nonces[i] << "\", \"mix\": \"" << hex(mix, 32) << "\", \"hash\": \"" << hex(hash, 32) << "\"" << (counts ? ", \"saturated_pixels\": " + std::to_string((*counts)[i]) : std::string()) << "}" << (i + 1 < nonces.size() ? "," : "") << "\n"; }
        o << "  ]\n}\n"; };
    write_set("overlap", overlap_n, nullptr); if (p.dataset_bytes) write_set("saturation", sat_n, &sat_count);
    printf("{\"r19_sets\": {\"overlap\": %zu, \"saturation\": %zu}}\n", overlap_n.size(), sat_n.size());
    if (inner) ignh_inner_destroy(inner); ignpow_ctx_free(ctx); return 0;
}
