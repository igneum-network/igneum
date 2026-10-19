// The raster determinism probe (14 Oct 2026, the project's gate for the RASTER lane, on the texture probe's pattern): a 8192 x 8192 RGBA8 framebuffer
// (256 MiB), 1,000,000 small triangles with INTEGER vertex coordinates (pixel corners, 0 .. 8192) from the fixed sequence below, drawn
// through the rasteriser under the API's own coverage rule (no multisampling, no depth, no scissor, cull none), integer additive blending
// (ONE + ONE, ADD, the UNORM8 target saturating), then the framebuffer digested (BLAKE3, row-major from the top-left, RGBA byte order).
// Framebuffer space: x right, y DOWN, origin top-left; pixel (i, j) covers [i, i + 1) x [j, j + 1), its sample at (i + 0.5, j + 0.5).
// The host rasteriser below is the same rule in software: exact integer edge functions (doubled coordinates), the top-left rule in the
// y-down space (a horizontal edge is TOP when the triangle lies below it, a non-horizontal edge is LEFT when the triangle lies to its
// right), additive saturating bytes; a line reads "card <digest> host <digest> equal/differs" with the first differing pixel named.
#pragma once
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <algorithm>
#include <string>
#include <vector>
#include "ih_params.h"   // ih_blake3_full
#include "hex.h"

namespace igm {
constexpr uint32_t kRpSize = 8192, kRpTriangles = 1000000u;
inline uint32_t rp_fmix32(uint32_t h) { h ^= h >> 16; h *= 0x85ebca6bu; h ^= h >> 13; h *= 0xc2b2ae35u; h ^= h >> 16; return h; }
struct RpTri { int32_t x0, y0, x1, y1, x2, y2; uint32_t rgba; };   // rgba: R in the low byte
inline int32_t rp_clamp(int32_t v) { return v < 0 ? 0 : v > (int32_t)kRpSize ? (int32_t)kRpSize : v; }
inline RpTri rp_triangle(uint32_t t) {
    const uint32_t s = rp_fmix32(t * 0x9E3779B1u + 1u), u = rp_fmix32(s ^ 0x2545F491u), c = rp_fmix32(s ^ 0x9E3779B9u);
    RpTri r; r.x0 = (int32_t)(s & 8191u); r.y0 = (int32_t)((s >> 13) & 8191u);
    r.x1 = rp_clamp(r.x0 + (int32_t)(u & 31u) - 15); r.y1 = rp_clamp(r.y0 + (int32_t)((u >> 5) & 31u) - 15);
    r.x2 = rp_clamp(r.x0 + (int32_t)((u >> 10) & 31u) - 15); r.y2 = rp_clamp(r.y0 + (int32_t)((u >> 15) & 31u) - 15);
    r.rgba = c; return r;
}
// the host rasteriser: the framebuffer as RGBA bytes, row-major from the top-left
inline void rp_host_raster(std::vector<uint8_t> &fb) {
    fb.assign((size_t)kRpSize * kRpSize * 4, 0);
    for (uint32_t t = 0; t < kRpTriangles; t++) {
        const RpTri tr = rp_triangle(t);
        const int64_t ax = tr.x0, ay = tr.y0, bx = tr.x1, by = tr.y1, cx = tr.x2, cy = tr.y2;
        const int64_t area = (bx - ax) * (cy - ay) - (by - ay) * (cx - ax);   // twice the signed area; zero draws nothing
        if (area == 0) continue;
        const int64_t s = area > 0 ? 1 : -1;   // the interior's sign of the edge functions
        // edge e of (a -> b) with the third vertex c: TOP when horizontal and the interior below (y down), LEFT when the interior is to its +x side
        struct E { int64_t ax, ay, bx, by; bool tl; } e[3] = { {ax, ay, bx, by, false}, {bx, by, cx, cy, false}, {cx, cy, ax, ay, false} };
        for (int k = 0; k < 3; k++) {
            const int64_t dx = e[k].bx - e[k].ax, dy = e[k].by - e[k].ay;
            if (dy == 0) e[k].tl = (dx > 0 ? 1 : -1) == s;      // orient2d(a, b, (a.x, a.y + 1)) = dx: the interior below the edge
            else e[k].tl = (-dy > 0 ? 1 : -1) == s;             // orient2d(a, b, (a.x + 1, a.y)) = -dy: the interior to the right
        }
        int64_t minx = std::min(ax, std::min(bx, cx)), maxx = std::max(ax, std::max(bx, cx)), miny = std::min(ay, std::min(by, cy)), maxy = std::max(ay, std::max(by, cy));
        const uint8_t R = (uint8_t)tr.rgba, G = (uint8_t)(tr.rgba >> 8), B = (uint8_t)(tr.rgba >> 16), A = (uint8_t)(tr.rgba >> 24);
        for (int64_t j = miny; j < maxy; j++) for (int64_t i = minx; i < maxx; i++) {
            // the sample (i + 0.5, j + 0.5) in doubled coordinates: P = (2i + 1, 2j + 1), the vertices doubled
            const int64_t px = 2 * i + 1, py = 2 * j + 1; bool in = true;
            for (int k = 0; k < 3 && in; k++) {
                const int64_t w = ((2 * e[k].bx - 2 * e[k].ax) * (py - 2 * e[k].ay) - (2 * e[k].by - 2 * e[k].ay) * (px - 2 * e[k].ax)) * s;
                if (w < 0 || (w == 0 && !e[k].tl)) in = false;
            }
            if (!in) continue;
            uint8_t *p = &fb[((size_t)j * kRpSize + (size_t)i) * 4];
            p[0] = (uint8_t)std::min<int>(255, p[0] + R); p[1] = (uint8_t)std::min<int>(255, p[1] + G); p[2] = (uint8_t)std::min<int>(255, p[2] + B); p[3] = (uint8_t)std::min<int>(255, p[3] + A);
        }
    }
}
inline std::string rp_digest(const std::vector<uint8_t> &fb) { uint8_t d[32]; ih_blake3_full(fb.data(), fb.size(), d); return hex_of(d, 32); }
// the one line and the JSON record; the first differing pixel named when they differ; the cost figures when the card ran its loop
inline int rp_report(const char *backend, const std::string &device, const std::vector<uint8_t> &card, const std::string &note, double tri_per_s, double seconds, const std::string &power_note) {
    std::vector<uint8_t> host; rp_host_raster(host);
    const std::string dc = rp_digest(card), dh = rp_digest(host); const bool eq = dc == dh;
    long long fx = -1, fy = -1; uint32_t cv = 0, hv = 0; size_t ndiff = 0;
    if (!eq) for (size_t p = 0; p < host.size() / 4 && p < card.size() / 4; p++) { if (std::memcmp(&card[4 * p], &host[4 * p], 4) != 0) { if (ndiff == 0) { fx = (long long)(p % kRpSize); fy = (long long)(p / kRpSize); std::memcpy(&cv, &card[4 * p], 4); std::memcpy(&hv, &host[4 * p], 4); } ndiff++; } }
    fprintf(stderr, "raster probe: %s %s: card %s host %s %s%s\n", backend, device.c_str(), dc.c_str(), dh.c_str(), eq ? "equal" : "DIFFERS", eq ? "" : (" (" + std::to_string(ndiff) + " pixels; the first at " + std::to_string(fx) + "," + std::to_string(fy) + ")").c_str());
    printf("{\"raster_probe\": {\"backend\": \"%s\", \"device\": \"%s\", \"width\": %u, \"height\": %u, \"format\": \"RGBA8\", \"triangles\": %u, \"blend\": \"one plus one, add, saturating\", \"card_digest\": \"%s\", \"host_digest\": \"%s\", \"equal\": %s, \"differing_pixels\": %llu, \"first_differing_pixel\": [%lld, %lld], \"card_rgba\": %u, \"host_rgba\": %u, \"triangles_per_s\": %.0f, \"loop_seconds\": %.1f, \"power\": \"%s\", \"note\": \"%s\"}}\n",
           backend, device.c_str(), kRpSize, kRpSize, kRpTriangles, dc.c_str(), dh.c_str(), eq ? "true" : "false", (unsigned long long)ndiff, fx, fy, cv, hv, tri_per_s, seconds, power_note.c_str(), note.c_str());
    fflush(stdout); return eq ? 0 : 3;
}
}
