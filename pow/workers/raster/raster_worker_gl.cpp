// igneum-raster-worker: VERSION 18, the raster form, through OpenGL (raster_worker.h holds the host logic: the key, the seed, the triangles, the
// mix and the final; raster_gl_context.h the context and the pipeline the probe shares). Modes: --vectors <files> --params <doc> (the
// exactness sets; the class lines and the summary JSON as the workers print them); bench <seconds> --params <doc> (attempts a second: the
// attempt drawn, its boxes read, its boxes cleared, in a loop); --device-name <substring of GL_RENDERER> picks the card among the EGL
// devices (the rig's nine cards, 15 October 2026); --vendor is accepted and ignored. GPU engineer B, 14 October 2026.
#include "raster_gl_card.h"
#include "raster_gl_v19.h"
#include "raster_gl_v19_card.h"
#include "raster_gl_engine.h"
#include "protocol.h"
#include <chrono>
using namespace igm;
int main(int argc, char **argv) {
    std::vector<std::string> files; std::string params, want; double bench_s = 0; bool bench = false;
    bool protocol = false;
    for (int a = 1; a < argc; a++) { std::string s = argv[a]; if (s == "protocol") { protocol = true; continue; } if (s == "--vectors") { while (a + 1 < argc && argv[a + 1][0] != '-') files.push_back(argv[++a]); } else if (s == "--params" && a + 1 < argc) params = argv[++a]; else if (s == "--device-name" && a + 1 < argc) want = argv[++a]; else if (s == "--vendor" && a + 1 < argc) a++; else if (s == "bench" && a + 1 < argc) { bench = true; bench_s = atof(argv[++a]); } else if (s == "--algo" && a + 1 < argc) a++; }
    IhParams p; std::string err; if (!ih_parse_params(params, p, err)) { printf("{\"raster_worker\": {\"refused\": \"params: %s\"}}\n", err.c_str()); return 2; }
    if (p.version != 18 && p.version != 19) { printf("{\"raster_worker\": {\"refused\": \"version %u is not a raster form (18 or 19)\"}}\n", p.version); return 2; }
    const uint32_t side = p.version == 19 ? r19_side(p) : r18_side(p);
    RasterGl gl; if (!gl.open("raster_worker", want) || !gl.pipeline("raster_worker", side)) return 2;
    if (protocol) {   // THE PROTOCOL (Ember's raster lane on Linux under version 19): the engine over the version 19 card on the context opened above; the document comes with the configure
        if (p.version != 19) { printf("{\"raster_worker\": {\"refused\": \"the protocol verb serves version 19 (the version 18 lane keeps its own worker)\"}}\n"); return 2; }
        gl.release_current();   // the loop takes the context on the thread that configures and on the job thread (raster_gl_engine.h's thread rule)
        RasterGlEngine eng(gl); return run_worker_loop(eng, "igneum-raster-worker", "0.1.0", "igneumhash", true, false);
    }
    if (p.version == 19) {   // VERSION 19 (raster_gl_v19.h): the vectors and the bench through the full card path
        if (want.empty()) { printf("{\"raster_worker\": {\"refused\": \"no --device-name: every row names its card (the project's ruling, 15 October 2026)\"}}\n"); return 2; }
        const size_t B = (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 16);
        R19Card card19; std::shared_ptr<R19GlState> st19; if (!r19_gl_card(gl, p, params, card19, B, st19)) return 2;
        if (!r19_gl_full(gl, p, st19.get(), card19, B)) return 2;   // the full card path: the records and the digests on the card
        if (bench) {   // the bench: sequential nonces under a zero target (no hit); attempts a second through the full path
            uint8_t P[32]; ih_P(p, P); uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); uint8_t K[32]; r18_key(p, 1, K, err); static const uint8_t zt[32] = {0};
            if (!r19_prepare(p, card19, 1, err)) { printf("{\"raster_worker\": {\"refused\": \"%s\"}}\n", err.c_str()); return 2; }
            auto t0 = std::chrono::steady_clock::now(); uint64_t attempts = 0; uint32_t hits_total = 0;
            while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s) {
                std::vector<uint64_t> nonces(B); for (size_t b = 0; b < B; b++) nonces[b] = attempts + b;
                std::vector<std::array<uint8_t, 32>> Ms, Fs; uint32_t hits = 0; if (!card19.full(K, P, header, nonces, zt, Ms, Fs, hits)) break; attempts += B; hits_total += hits;
            }
            const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
            const char *r = (const char *)glGetString(GL_RENDERER); const std::string now = r ? r : ""; const bool ok = !now.empty() && now.find(want) != std::string::npos && gl.device.find(want) != std::string::npos;
            printf("{\"bench\": {\"device\": \"%s\", \"backend\": \"opengl\", \"version\": 19, \"rounds\": %u, \"reach\": %u, \"side\": %u, \"keyed\": %u, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.4f, \"batch\": %zu, \"hits\": %u, \"note\": \"version 19 through the graphics pipeline: the seed, the chain, the draw, the fold, the mix and the final on the card; the batch's nonces in, the hits out\"}}\n", gl.device.c_str(), p.rounds, r19_reach(p), side, p.dataset_bytes ? 1u : 0u, secs, (unsigned long long)attempts, attempts / secs / 1e6, B, hits_total);
            printf("{\"card_check\": {\"named\": \"%s\", \"opened\": \"%s\", \"renderer_at_end\": \"%s\", \"verdict\": \"%s\"}}\n", want.c_str(), gl.device.c_str(), now.c_str(), ok ? "match" : "MISMATCH"); fflush(stdout);
            return ok ? 0 : 4;
        }
        const int rc = r19_vectors("opengl", gl.device, p, files, card19, "GL 4.3 core, version 19: the seed, the chain, one instanced draw a batch into the atlas, the fold, the mix and the final on the card", B);
        const char *r = (const char *)glGetString(GL_RENDERER); const std::string now = r ? r : ""; const bool ok = !now.empty() && now.find(want) != std::string::npos && gl.device.find(want) != std::string::npos;
        printf("{\"card_check\": {\"named\": \"%s\", \"opened\": \"%s\", \"renderer_at_end\": \"%s\", \"verdict\": \"%s\"}}\n", want.c_str(), gl.device.c_str(), now.c_str(), ok ? "match" : "MISMATCH"); fflush(stdout);
        return ok ? rc : 4;
    }
    R18Card card; if (!r18_gl_card(gl, p, params, card)) return 2;
    // THE CARD CHECK (the project's ruling 15 October 2026 17:1x UK): every row names its card and the device string is read back at the end and
    // checked against the name; a mismatch or an empty string fails the row (a 9060 XT row ran the rig's 5060 Ti when the NVIDIA EGL vendor
    // began to enumerate first, 15:41 UK). A row without --device-name is refused outright.
    if (want.empty()) { printf("{\"raster_worker\": {\"refused\": \"no --device-name: every row names its card (the project's ruling, 15 October 2026)\"}}\n"); return 2; }
    auto card_check = [&]() -> bool { const char *r = (const char *)glGetString(GL_RENDERER); const std::string now = r ? r : ""; const bool ok = !now.empty() && now.find(want) != std::string::npos && gl.device.find(want) != std::string::npos;
        printf("{\"card_check\": {\"named\": \"%s\", \"opened\": \"%s\", \"renderer_at_end\": \"%s\", \"verdict\": \"%s\"}}\n", want.c_str(), gl.device.c_str(), now.c_str(), ok ? "match" : "MISMATCH"); fflush(stdout); return ok; };
    if (bench) {
        uint8_t P[32]; ih_P(p, P); uint8_t header[32]; for (int i = 0; i < 32; i++) header[i] = (uint8_t)(i * 7 + 1); uint8_t K[32]; r18_key(p, 1, K, err);
        if (!r18_prepare(p, card, 1, err)) { printf("{\"raster_worker\": {\"refused\": \"%s\"}}\n", err.c_str()); return 2; }
        auto t0 = std::chrono::steady_clock::now(); uint64_t attempts = 0;
        const size_t B = card.batch ? (size_t)(getenv("IGM_RASTER_BATCH") ? atoi(getenv("IGM_RASTER_BATCH")) : 16) : 1;   // attempts in flight a batch (the founder's order 18:0x UK)
        while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < bench_s) {
            std::vector<std::vector<R18Tri>> batch(B); for (size_t b = 0; b < B; b++) { uint8_t S[32]; r18_seed(p, P, header, attempts + b, S); batch[b] = r18_triangles(p, K, S); }
            std::vector<std::array<uint8_t, 32>> Ms; if (!r18_attempts(card, batch, K, Ms)) break; attempts += B;
        }
        const double secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (!card_check()) return 4;
        printf("{\"bench\": {\"device\": \"%s\", \"backend\": \"opengl\", \"version\": 18, \"rounds\": %u, \"seconds\": %.1f, \"hashes\": \"%llu\", \"mhs\": %.4f, \"side\": %u, \"keyed\": %u, \"digest\": \"%s\", \"batch\": %zu, \"note\": \"attempts queued on the card a batch at a time (the draw, the digest pass, the restore each; the chunk values read once a batch), or one a pass where the card has no batch\"}}\n", gl.device.c_str(), p.rounds, secs, (unsigned long long)attempts, attempts / secs / 1e6, side, p.dataset_bytes ? 1u : 0u, card.digest ? "card" : "host", B);
        return 0;
    }
    const int rc = r18_vectors("opengl", gl.device, p, files, card, "GL 3.3 core, one draw an attempt, the boxes by glReadPixels, the boxes restored (scissored clears, or the keyed bytes by glTexSubImage2D under 18c)");
    if (!card_check()) return 4;   // the sets' lines stand only with the card confirmed at the end
    return rc;
}
